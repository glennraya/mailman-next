package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/probe"
	"github.com/glennraya/mailman/internal/service"
)

// serviceCommand is the presentation half of `mailman service`. The
// behaviour lives in internal/service; what is here is argument parsing and
// the words a user reads, which is the same split the rest of cmd/ uses.
func serviceCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("mailman service needs a command: install, uninstall, status, start or stop")
	}

	command, rest := args[0], args[1:]

	fs := flag.NewFlagSet("mailman service "+command, flag.ExitOnError)
	execPath := fs.String("exec-path", "", "the mailman binary to register (default: this one)")
	if err := fs.Parse(rest); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	manager, err := service.New(service.Options{
		ExecPath: *execPath,
		Home:     cfg.Home,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch command {
	case "install":
		return install(ctx, manager, cfg)
	case "uninstall":
		if err := manager.Uninstall(ctx); err != nil {
			return err
		}
		fmt.Println("Mailman will no longer start at login.")
		return nil
	case "start":
		if err := manager.Start(ctx); err != nil {
			return err
		}
		fmt.Println("Mailman started.")
		return nil
	case "stop":
		if err := manager.Stop(ctx); err != nil {
			return err
		}
		fmt.Println("Mailman stopped. It will start again at your next login.")
		return nil
	case "status":
		return status(ctx, manager, cfg)
	default:
		return fmt.Errorf("unknown service command %q -- try install, uninstall, status, start or stop", command)
	}
}

func install(ctx context.Context, manager service.Manager, cfg *config.Config) error {
	if err := manager.Install(ctx); err != nil {
		return err
	}

	fmt.Printf(`Mailman is registered to start at login, and is starting now.

  Inbox   http://%s
  SMTP    %s
  Data    %s
  Unit    %s
  Log     %s

Point a project at it with MAIL_HOST=127.0.0.1 and MAIL_PORT=%s.
`,
		cfg.HTTPAddr, cfg.SMTPAddr, cfg.Home, manager.UnitPath(), manager.LogHint(),
		port(cfg.SMTPAddr))

	return nil
}

// status reports both halves of the picture: what the login manager believes,
// and what is actually answering on the ports. They can disagree -- a service
// can be registered and stopped because something else took its port -- and
// the disagreement is usually the answer the user came for.
func status(ctx context.Context, manager service.Manager, cfg *config.Config) error {
	state, err := manager.Status(ctx)
	if err != nil {
		return err
	}

	fmt.Println("Mailman service")
	line("Manager", state.Manager)
	line("Unit", manager.UnitPath())

	switch {
	case !state.Installed:
		line("Registered", "no -- run `mailman service install`")
	case state.Running:
		line("Registered", fmt.Sprintf("yes, running at login (pid %d)", state.PID))
	default:
		line("Registered", "yes, but not running")
	}

	line("Inbox", "http://"+cfg.HTTPAddr+"  "+answering(probe.HTTP(ctx, cfg.HTTPAddr)))
	line("SMTP", cfg.SMTPAddr+"  "+answering(probe.SMTP(ctx, cfg.SMTPAddr)))
	line("Log", manager.LogHint())

	if state.Conflict != "" {
		fmt.Printf("\nAnother manager also has Mailman registered:\n  %s\n", state.Conflict)
	}
	if state.Note != "" {
		fmt.Printf("\n%s\n", state.Note)
	}

	// A status that exits non-zero when the thing is not running is what
	// makes it usable from a script, and matches what systemctl does.
	if !state.Running {
		os.Exit(1)
	}

	return nil
}

// line keeps the labels in one column, which is the only thing that makes
// six unrelated facts readable at a glance.
func line(label, value string) {
	fmt.Printf("  %-11s%s\n", label, value)
}

func answering(result probe.Result) string {
	switch result.Owner {
	case probe.Mailman:
		return "answering (" + result.Detail + ")"
	case probe.Foreign:
		return "held by something else: " + result.Detail
	default:
		return "nothing is listening"
	}
}

// port is the port half of a host:port address, for the line that tells the
// user what to put in their .env.
func port(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}

	return addr
}
