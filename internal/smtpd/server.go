// Package smtpd is the capture server: the port a local project points its
// mailer at.
//
// It accepts everything. Any credentials authenticate, no TLS is required,
// and nothing is ever relayed onward -- mail that arrives here has reached
// its destination. That is the whole contract, and it is what lets a project
// be pointed at Mailman by changing two lines of its .env.
package smtpd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"github.com/glennraya/mailman/internal/mailstore"
)

// Ingestor is the half of the pipeline this package needs. Taking an
// interface keeps the SMTP tests free of a database.
type Ingestor interface {
	Ingest(ctx context.Context, raw []byte, envelope mailstore.Envelope, direction string) (*mailstore.Message, error)
}

// Options configures the capture server.
type Options struct {
	Addr            string
	MaxMessageBytes int64
	Ingestor        Ingestor
	Logger          *slog.Logger
}

// Server wraps go-smtp with Mailman's backend.
type Server struct {
	addr   string
	smtp   *smtp.Server
	logger *slog.Logger
}

// New builds the server without binding anything yet.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{addr: opts.Addr, logger: logger}

	server := smtp.NewServer(&backend{ingestor: opts.Ingestor, logger: logger})
	server.Addr = opts.Addr
	server.Domain = "mailman.local"
	server.MaxMessageBytes = opts.MaxMessageBytes
	server.MaxRecipients = 100
	server.ReadTimeout = 60 * time.Second
	server.WriteTimeout = 60 * time.Second

	// Without this, go-smtp omits AUTH from the EHLO response entirely
	// unless the connection is TLS, and every client configured with
	// credentials fails before it can send anything. Mailman listens on
	// loopback and relays nowhere, so there is nothing here to protect.
	server.AllowInsecureAuth = true

	// SMTPUTF8 lets addresses carry non-ASCII, which apps with
	// international users do send. BINARYMIME is deliberately left off:
	// go-smtp answers DATA with a hard 502 once a message declares it,
	// forcing clients onto BDAT for no benefit to a capture tool.
	server.EnableSMTPUTF8 = true

	s.smtp = server
	return s
}

// Addr reports the address in use. After Listen this is the resolved address,
// so a configured port of 0 comes back as the port actually bound.
func (s *Server) Addr() string { return s.addr }

// Listen binds the port. It is separate from Serve so startup can fail before
// anything else is running -- a port already in use should stop Mailman, not
// leave it half up.
func (s *Server) Listen() (net.Listener, error) {
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		return nil, fmt.Errorf("listen for SMTP on %s: %w", s.addr, err)
	}

	s.addr = listener.Addr().String()
	return listener, nil
}

// Serve accepts connections until the context is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	done := make(chan error, 1)

	go func() { done <- s.smtp.Serve(listener) }()

	select {
	case <-ctx.Done():
		s.smtp.Close()
		<-done
		return nil
	case err := <-done:
		// Close races with in-flight accepts; the resulting error is the
		// shutdown itself, not a failure.
		if err != nil && !errors.Is(err, net.ErrClosed) &&
			!strings.Contains(err.Error(), "use of closed network connection") {
			return fmt.Errorf("smtp server: %w", err)
		}
		return nil
	}
}

// backend hands each connection its own session.
type backend struct {
	ingestor Ingestor
	logger   *slog.Logger
}

func (b *backend) NewSession(conn *smtp.Conn) (smtp.Session, error) {
	return &session{backend: b}, nil
}

// session tracks one SMTP conversation. The envelope it accumulates is the
// only record of who was really sent the mail -- Bcc recipients appear here
// and nowhere in the headers.
type session struct {
	backend *backend

	from       string
	recipients []string
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.recipients = append(s.recipients, to)
	return nil
}

// Reset clears the envelope between messages on a reused connection. Missing
// this would attribute one message's recipients to the next.
func (s *session) Reset() {
	s.from = ""
	s.recipients = nil
}

func (s *session) Logout() error { return nil }

// AuthMechanisms advertises what the server will accept. LOGIN is listed
// first because the clients that need it tend to take the first one offered.
func (s *session) AuthMechanisms() []string {
	return []string{sasl.Login, sasl.Plain}
}

// Auth accepts any credentials at all. Mailman is a development tool with no
// accounts; rejecting a password would only make people guess at one.
func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
	case sasl.Login:
		return newLoginServer(func(_, _ string) error { return nil }), nil
	default:
		return nil, smtp.ErrAuthUnknownMechanism
	}
}

// Data reads and stores the message.
func (s *session) Data(r io.Reader) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		// go-smtp reports an oversized message here, and the sender needs
		// to hear that it was too big rather than a generic failure.
		return err
	}

	envelope := mailstore.Envelope{From: s.from, Recipients: s.recipients}

	// Not the connection's context: the message has been fully received and
	// storing it should finish even if the client hangs up mid-response.
	message, err := s.backend.ingestor.Ingest(context.Background(), raw, envelope, mailstore.DirectionInbound)
	if err != nil {
		s.backend.logger.Error("could not store captured message",
			"from", s.from, "recipients", len(s.recipients), "error", err)

		// A 4xx tells the sender to try again later. Mailman failed to
		// write the message, so a retry is genuinely worth making -- and
		// the app under test sees a realistic transient failure rather
		// than a permanent rejection it will log as a bug.
		return &smtp.SMTPError{
			Code:         451,
			EnhancedCode: smtp.EnhancedCode{4, 3, 0},
			Message:      "Could not store message",
		}
	}

	s.backend.logger.Info("captured",
		"id", message.ID,
		"from", s.from,
		"subject", message.Subject,
		"size", message.SizeBytes)

	return nil
}
