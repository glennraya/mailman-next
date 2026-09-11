package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/httpapi"
	"github.com/glennraya/mailman/internal/mailmime"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/smtpd"
	"github.com/glennraya/mailman/web"
)

func serve(args []string) error {
	// Every flag defaults to its zero value so fs.Visit can tell which
	// ones were actually given; the real defaults live in the config layer,
	// which the file and environment also feed into.
	fs := flag.NewFlagSet("mailman", flag.ExitOnError)
	fs.Usage = func() { usage(fs.Output()) }

	var (
		httpAddr = fs.String("http", "", "address for the inbox, API and event stream (default "+config.DefaultHTTPAddr+")")
		smtpAddr = fs.String("smtp", "", "address for the SMTP capture server (default "+config.DefaultSMTPAddr+")")
		home     = fs.String("home", "", "data directory (default ~/.mailman)")
		maxSize  = fs.Int64("max-size", 0, "largest message to accept, in bytes")
		verbose  = fs.Bool("v", false, "log every request")
		showVer  = fs.Bool("version", false, "print the version and exit")

		// supervised is passed only by the unit files `mailman service
		// install` writes. It changes nothing about how Mailman runs --
		// only what a busy port costs, which has to be a clean exit
		// under a supervisor and a visible failure in a terminal.
		supervised = fs.Bool("supervised", false, "running under launchd or systemd: exit cleanly rather than fail when a port is taken")
	)

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVer {
		fmt.Println("mailman", version)
		return nil
	}

	// -home has to be applied before the config loads, since it decides
	// which directory the config file is read from.
	if *home != "" {
		if err := os.Setenv("MAILMAN_HOME", *home); err != nil {
			return fmt.Errorf("set data directory: %w", err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "http":
			cfg.HTTPAddr = *httpAddr
		case "smtp":
			cfg.SMTPAddr = *smtpAddr
		case "max-size":
			cfg.MaxMessageBytes = *maxSize
		}
	})

	if err := cfg.Validate(); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	store, err := mailstore.Open(cfg.DBPath(), cfg.MailDir())
	if err != nil {
		return err
	}
	defer store.Close()

	broker := events.New()
	ingestor := mailmime.NewIngestor(store, broker, logger)

	capture := smtpd.New(smtpd.Options{
		Addr:            cfg.SMTPAddr,
		MaxMessageBytes: cfg.MaxMessageBytes,
		Ingestor:        ingestor,
		Logger:          logger,
	})

	api := httpapi.New(httpapi.Options{
		Store:    store,
		Broker:   broker,
		Config:   cfg,
		Ingestor: ingestor,
		Assets:   web.Handler(),
		Version:  version,
		Logger:   logger,
	})

	// Binding is not enough to tell whether a port is free -- see
	// preflight, which explains why it dials instead.
	preflightCtx, cancelPreflight := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelPreflight()

	if err := preflight(preflightCtx, "SMTP", cfg.SMTPAddr, *supervised); err != nil {
		return err
	}
	if err := preflight(preflightCtx, "HTTP", cfg.HTTPAddr, *supervised); err != nil {
		return err
	}

	// Both ports are bound before either starts serving, so a port that is
	// taken between the check above and here still fails startup outright
	// rather than leaving Mailman half up with one listener running.
	captureListener, err := capture.Listen()
	if err != nil {
		return err
	}

	httpListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		captureListener.Close()
		return fmt.Errorf("listen for HTTP on %s: %w", cfg.HTTPAddr, err)
	}

	server := &http.Server{
		Handler: api.Handler(),
		// The event stream is a long-lived connection, so there is no
		// whole-request timeout here; only the headers are bounded.
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	failed := make(chan error, 2)

	go func() {
		if err := capture.Serve(ctx, captureListener); err != nil {
			failed <- err
		}
	}()

	go func() {
		if err := server.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- fmt.Errorf("http server: %w", err)
		}
	}()

	logger.Info("mailman ready",
		"inbox", "http://"+httpListener.Addr().String(),
		"smtp", capture.Addr(),
		"data", cfg.Home,
		"version", version)

	if !cfg.WebhookEnabled() {
		logger.Info("replies will not be forwarded: no webhook configured",
			"config", cfg.ConfigPath())
	}

	select {
	case err := <-failed:
		return err
	case <-ctx.Done():
		logger.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return server.Shutdown(shutdownCtx)
}
