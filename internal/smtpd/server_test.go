package smtpd

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"

	"github.com/glennraya/mailman/internal/mailstore"
)

// recorder stands in for the ingest pipeline, so these tests cover the SMTP
// conversation only.
type recorder struct {
	mu       sync.Mutex
	messages []captured
	err      error
}

type captured struct {
	raw      []byte
	envelope mailstore.Envelope
}

func (r *recorder) Ingest(_ context.Context, raw []byte, envelope mailstore.Envelope, _ string) (*mailstore.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.err != nil {
		return nil, r.err
	}

	r.messages = append(r.messages, captured{raw: raw, envelope: envelope})
	return &mailstore.Message{ID: "test", SizeBytes: int64(len(raw))}, nil
}

func (r *recorder) last(t *testing.T) captured {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.messages) == 0 {
		t.Fatal("no message was captured")
	}
	return r.messages[len(r.messages)-1]
}

func start(t *testing.T, ingestor Ingestor, maxSize int64) string {
	t.Helper()

	if maxSize == 0 {
		maxSize = 1 << 20
	}

	server := New(Options{
		Addr:            "127.0.0.1:0",
		MaxMessageBytes: maxSize,
		Ingestor:        ingestor,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	listener, err := server.Listen()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		if err := server.Serve(ctx, listener); err != nil {
			t.Errorf("serve: %v", err)
		}
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	return server.Addr()
}

// dial opens a raw connection and reads the greeting, for the tests that need
// to drive the protocol by hand.
func dial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)

	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read greeting: %v", err)
	}
	return conn, reader
}

func command(t *testing.T, conn net.Conn, reader *bufio.Reader, line string) string {
	t.Helper()

	if _, err := fmt.Fprintf(conn, "%s\r\n", line); err != nil {
		t.Fatalf("write %q: %v", line, err)
	}

	response, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read response to %q: %v", line, err)
	}
	return strings.TrimRight(response, "\r\n")
}

// drainEHLO issues EHLO and consumes the whole multiline response.
func drainEHLO(t *testing.T, conn net.Conn, reader *bufio.Reader) []string {
	t.Helper()

	if _, err := fmt.Fprintf(conn, "EHLO tester\r\n"); err != nil {
		t.Fatalf("write EHLO: %v", err)
	}

	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read EHLO response: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		lines = append(lines, line)

		// A hyphen after the code means more lines follow.
		if len(line) < 4 || line[3] != '-' {
			break
		}
	}
	return lines
}

func TestCapturesAMessage(t *testing.T) {
	sink := &recorder{}
	addr := start(t, sink, 0)

	body := "From: app@myapp.test\r\nTo: someone@example.test\r\nSubject: Hello\r\n\r\nBody text\r\n"

	if err := smtp.SendMail(addr, nil, "app@myapp.test", []string{"someone@example.test"}, []byte(body)); err != nil {
		t.Fatalf("send: %v", err)
	}

	got := sink.last(t)
	if !strings.Contains(string(got.raw), "Subject: Hello") {
		t.Errorf("captured payload = %q", got.raw)
	}
	if got.envelope.From != "app@myapp.test" {
		t.Errorf("envelope from = %q", got.envelope.From)
	}
	if len(got.envelope.Recipients) != 1 || got.envelope.Recipients[0] != "someone@example.test" {
		t.Errorf("envelope recipients = %v", got.envelope.Recipients)
	}
}

func TestRecordsEveryEnvelopeRecipient(t *testing.T) {
	sink := &recorder{}
	addr := start(t, sink, 0)

	// The third recipient is in no header. Capturing it here is the only
	// way Bcc can be reconstructed later.
	recipients := []string{"to@example.test", "cc@example.test", "bcc@example.test"}
	body := "From: app@myapp.test\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Hi\r\n\r\nBody\r\n"

	if err := smtp.SendMail(addr, nil, "app@myapp.test", recipients, []byte(body)); err != nil {
		t.Fatalf("send: %v", err)
	}

	if got := sink.last(t).envelope.Recipients; len(got) != 3 {
		t.Errorf("envelope recipients = %v, want all three", got)
	}
}

func TestAdvertisesAuthWithoutTLS(t *testing.T) {
	addr := start(t, &recorder{}, 0)
	conn, reader := dial(t, addr)

	var advertised string
	for _, line := range drainEHLO(t, conn, reader) {
		if strings.Contains(strings.ToUpper(line), "AUTH") {
			advertised = line
		}
	}

	// go-smtp hides AUTH entirely unless the connection is TLS or insecure
	// auth is allowed. If this line disappears, every client configured with
	// a username fails before it can send anything.
	if advertised == "" {
		t.Fatal("EHLO did not advertise AUTH on a plaintext connection")
	}
	for _, mechanism := range []string{"LOGIN", "PLAIN"} {
		if !strings.Contains(strings.ToUpper(advertised), mechanism) {
			t.Errorf("EHLO advertised %q, missing %s", advertised, mechanism)
		}
	}
}

func TestAcceptsAnyPlainCredentials(t *testing.T) {
	sink := &recorder{}
	addr := start(t, sink, 0)

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Hello("tester"); err != nil {
		t.Fatalf("ehlo: %v", err)
	}
	if err := client.Auth(smtp.PlainAuth("", "anyone", "anything", "127.0.0.1")); err != nil {
		t.Fatalf("PLAIN auth rejected: %v", err)
	}
}

// TestAuthLoginDialects covers the three ways a client can start AUTH LOGIN.
// The middle case is the one that hangs a naive implementation: a client is
// allowed to send an empty username, and treating empty as "not yet asked"
// re-prompts for the password until the read timeout fires.
func TestAuthLoginDialects(t *testing.T) {
	encode := func(s string) string {
		return base64.StdEncoding.EncodeToString([]byte(s))
	}

	cases := []struct {
		name     string
		open     string
		username string
	}{
		{"bare AUTH LOGIN", "AUTH LOGIN", "anyone"},
		{"empty username", "AUTH LOGIN", ""},
		{"username as initial response", "AUTH LOGIN " + encode("anyone"), ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := start(t, &recorder{}, 0)
			conn, reader := dial(t, addr)
			drainEHLO(t, conn, reader)

			response := command(t, conn, reader, tc.open)

			// A bare AUTH LOGIN is answered with a username challenge; an
			// inline username jumps straight to the password challenge.
			if !strings.HasPrefix(response, "334") {
				t.Fatalf("AUTH LOGIN answered %q, want a 334 challenge", response)
			}

			if tc.open == "AUTH LOGIN" {
				if decoded := decodeChallenge(t, response); decoded != "Username:" {
					t.Fatalf("first challenge = %q, want \"Username:\"", decoded)
				}
				response = command(t, conn, reader, encode(tc.username))
				if !strings.HasPrefix(response, "334") {
					t.Fatalf("after username, got %q, want a password challenge", response)
				}
			}

			if decoded := decodeChallenge(t, response); decoded != "Password:" {
				t.Fatalf("second challenge = %q, want \"Password:\"", decoded)
			}

			if response := command(t, conn, reader, encode("anything")); !strings.HasPrefix(response, "235") {
				t.Fatalf("authentication answered %q, want 235", response)
			}
		})
	}
}

func TestAcceptsGoSASLLoginClient(t *testing.T) {
	addr := start(t, &recorder{}, 0)

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Hello("tester"); err != nil {
		t.Fatalf("ehlo: %v", err)
	}
	if err := client.Auth(loginAuth{sasl.NewLoginClient("anyone", "anything")}); err != nil {
		t.Fatalf("LOGIN auth rejected: %v", err)
	}
}

func TestRejectsOversizedMail(t *testing.T) {
	addr := start(t, &recorder{}, 1024)

	body := "From: app@myapp.test\r\nTo: someone@example.test\r\nSubject: Big\r\n\r\n" +
		strings.Repeat("x", 4096) + "\r\n"

	err := smtp.SendMail(addr, nil, "app@myapp.test", []string{"someone@example.test"}, []byte(body))
	if err == nil {
		t.Fatal("oversized message was accepted")
	}
}

func TestReportsAStorageFailureAsTemporary(t *testing.T) {
	// A permanent rejection would make the app under test log a delivery
	// error; a 4xx tells it to retry, which is the truth when Mailman could
	// not write the file.
	sink := &recorder{err: fmt.Errorf("disk on fire")}
	addr := start(t, sink, 0)

	body := "From: app@myapp.test\r\nTo: someone@example.test\r\nSubject: Hi\r\n\r\nBody\r\n"

	err := smtp.SendMail(addr, nil, "app@myapp.test", []string{"someone@example.test"}, []byte(body))
	if err == nil {
		t.Fatal("a failed store was reported as success")
	}
	if !strings.Contains(err.Error(), "451") {
		t.Errorf("error = %v, want a 451 so the sender retries", err)
	}
}

func TestResetClearsTheEnvelope(t *testing.T) {
	sink := &recorder{}
	addr := start(t, sink, 0)

	client, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Mail("first@myapp.test"); err != nil {
		t.Fatalf("mail: %v", err)
	}
	if err := client.Rcpt("first-recipient@example.test"); err != nil {
		t.Fatalf("rcpt: %v", err)
	}
	if err := client.Reset(); err != nil {
		t.Fatalf("rset: %v", err)
	}

	if err := client.Mail("second@myapp.test"); err != nil {
		t.Fatalf("mail after reset: %v", err)
	}
	if err := client.Rcpt("second-recipient@example.test"); err != nil {
		t.Fatalf("rcpt after reset: %v", err)
	}

	w, err := client.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	fmt.Fprint(w, "Subject: Second\r\n\r\nBody\r\n")
	if err := w.Close(); err != nil {
		t.Fatalf("close data: %v", err)
	}

	envelope := sink.last(t).envelope
	if envelope.From != "second@myapp.test" {
		t.Errorf("envelope from = %q; the reset envelope leaked", envelope.From)
	}
	if len(envelope.Recipients) != 1 || envelope.Recipients[0] != "second-recipient@example.test" {
		t.Errorf("envelope recipients = %v; the reset envelope leaked", envelope.Recipients)
	}
}

func decodeChallenge(t *testing.T, response string) string {
	t.Helper()

	_, encoded, found := strings.Cut(response, " ")
	if !found {
		return ""
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		t.Fatalf("decode challenge %q: %v", response, err)
	}
	return string(decoded)
}

// loginAuth adapts a go-sasl client to net/smtp's Auth interface, so the
// server can be tested against the same client implementation the real
// mailers use.
type loginAuth struct{ client sasl.Client }

func (a loginAuth) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	return a.client.Start()
}

func (a loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return a.client.Next(fromServer)
}
