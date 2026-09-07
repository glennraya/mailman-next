// Package e2e boots the whole of Mailman -- SMTP capture, storage, the JSON
// API and the event stream -- and drives it the way a developer's project
// would. It is the test that proves the product works, as opposed to the unit
// tests that prove each piece does.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/httpapi"
	"github.com/glennraya/mailman/internal/mailmime"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/smtpd"
)

// instance is a running Mailman, on ports the operating system chose.
type instance struct {
	httpAddr string
	smtpAddr string
	store    *mailstore.Store
}

func boot(t *testing.T) *instance {
	t.Helper()

	home := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := mailstore.Open(filepath.Join(home, "mailman.db"), filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	broker := events.New()
	ingestor := mailmime.NewIngestor(store, broker, logger)

	cfg := &config.Config{
		Home:            home,
		MaxMessageBytes: config.DefaultMaxSize,
		Webhook:         config.Webhook{Format: config.DefaultFormat},
	}

	capture := smtpd.New(smtpd.Options{
		Addr:            "127.0.0.1:0",
		MaxMessageBytes: cfg.MaxMessageBytes,
		Ingestor:        ingestor,
		Logger:          logger,
	})

	captureListener, err := capture.Listen()
	if err != nil {
		t.Fatalf("listen for SMTP: %v", err)
	}

	api := httpapi.New(httpapi.Options{
		Store:    store,
		Broker:   broker,
		Config:   cfg,
		Ingestor: ingestor,
		Version:  "test",
		Logger:   logger,
	})

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for HTTP: %v", err)
	}

	server := &http.Server{Handler: api.Handler()}

	ctx, cancel := context.WithCancel(context.Background())
	captureDone := make(chan struct{})

	go func() {
		defer close(captureDone)
		capture.Serve(ctx, captureListener)
	}()
	go server.Serve(httpListener)

	t.Cleanup(func() {
		cancel()
		server.Close()
		<-captureDone
	})

	return &instance{
		httpAddr: httpListener.Addr().String(),
		smtpAddr: capture.Addr(),
		store:    store,
	}
}

func (i *instance) url(path string) string { return "http://" + i.httpAddr + path }

// get fetches and decodes a JSON response.
func (i *instance) get(t *testing.T, path string, into any) {
	t.Helper()

	response, err := http.Get(i.url(path))
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET %s returned %d: %s", path, response.StatusCode, body)
	}
	if into == nil {
		return
	}
	if err := json.NewDecoder(response.Body).Decode(into); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// send delivers a message the way a local project would: plain SMTP, with
// credentials the server is expected to wave through.
func (i *instance) send(t *testing.T, from string, to []string, body string) {
	t.Helper()

	client, err := smtp.Dial(i.smtpAddr)
	if err != nil {
		t.Fatalf("dial SMTP: %v", err)
	}
	defer client.Close()

	if err := client.Hello("myapp.test"); err != nil {
		t.Fatalf("ehlo: %v", err)
	}
	if err := client.Auth(smtp.PlainAuth("", "app", "secret", "127.0.0.1")); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := client.Mail(from); err != nil {
		t.Fatalf("mail from: %v", err)
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			t.Fatalf("rcpt to %s: %v", recipient, err)
		}
	}

	w, err := client.Data()
	if err != nil {
		t.Fatalf("data: %v", err)
	}
	if _, err := io.WriteString(w, body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close data: %v", err)
	}
	if err := client.Quit(); err != nil {
		t.Fatalf("quit: %v", err)
	}
}

type conversationList struct {
	Conversations []struct {
		ID           int64  `json:"id"`
		Subject      string `json:"subject"`
		MessageCount int    `json:"message_count"`
		UnreadCount  int    `json:"unread_count"`
		HasAttach    bool   `json:"has_attachments"`
		Preview      string `json:"preview"`
	} `json:"conversations"`
	Unread int `json:"unread"`
}

type conversationDetail struct {
	Conversation struct {
		ID      int64  `json:"id"`
		Subject string `json:"subject"`
	} `json:"conversation"`
	Messages []struct {
		ID        string `json:"id"`
		Subject   string `json:"subject"`
		MessageID string `json:"message_id"`
		InReplyTo string `json:"in_reply_to"`
		TextBody  string `json:"text_body"`
		HTMLBody  string `json:"html_body"`
		Bcc       []struct {
			Address string `json:"address"`
		} `json:"bcc"`
		ReplyTo []struct {
			Address string `json:"address"`
		} `json:"reply_to"`
	} `json:"messages"`
}

func mail(subject, messageID, inReplyTo string, extra ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: Acme Billing <billing@acme.test>\r\n")
	fmt.Fprintf(&b, "To: Glenn <glenn@myapp.test>\r\n")
	for _, header := range extra {
		fmt.Fprintf(&b, "%s\r\n", header)
	}
	fmt.Fprintf(&b, "Subject: %s\r\nMessage-ID: <%s>\r\n", subject, messageID)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", inReplyTo)
	}
	fmt.Fprintf(&b, "\r\nBody of %s\r\n", subject)
	return b.String()
}

// TestCaptureReachesTheInbox is the walk a developer takes on first run:
// point a project at the SMTP port, send mail, and see it in the inbox.
func TestCaptureReachesTheInbox(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test",
		[]string{"glenn@myapp.test", "audit@myapp.test"},
		mail("Order 4471 has shipped", "order-4471@acme.test", "",
			"Reply-To: order-4471@mail.acme.test"))

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)

	if len(list.Conversations) != 1 {
		t.Fatalf("inbox holds %d conversations, want 1", len(list.Conversations))
	}
	if list.Conversations[0].Subject != "Order 4471 has shipped" {
		t.Errorf("subject = %q", list.Conversations[0].Subject)
	}
	if list.Unread != 1 {
		t.Errorf("unread = %d, want 1", list.Unread)
	}

	var detail conversationDetail
	m.get(t, fmt.Sprintf("/api/v1/conversations/%d", list.Conversations[0].ID), &detail)

	if len(detail.Messages) != 1 {
		t.Fatalf("thread holds %d messages, want 1", len(detail.Messages))
	}

	message := detail.Messages[0]

	// audit@ was in the envelope but no header. Recovering it as Bcc is
	// something a capture tool has to do at receive time or not at all.
	if len(message.Bcc) != 1 || message.Bcc[0].Address != "audit@myapp.test" {
		t.Errorf("bcc = %+v, want audit@myapp.test", message.Bcc)
	}

	// Reply-To decides where a reply is addressed, and therefore which
	// project's webhook it will be routed to.
	if len(message.ReplyTo) != 1 || message.ReplyTo[0].Address != "order-4471@mail.acme.test" {
		t.Errorf("reply-to = %+v", message.ReplyTo)
	}
}

// TestLiveUpdateArrivesOverTheWebSocket covers the reason there is a socket at
// all: mail captured while the inbox is open shows up without a refresh.
func TestLiveUpdateArrivesOverTheWebSocket(t *testing.T) {
	m := boot(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws://"+m.httpAddr+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()

	// The stream opens with a hello carrying the current unread count, so a
	// client can paint its badge without a second request.
	var hello events.Event
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if hello.Type != "hello" {
		t.Fatalf("first frame = %q, want hello", hello.Type)
	}

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Live update", "live-1@acme.test", ""))

	var event events.Event
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatalf("read event: %v", err)
	}

	if event.Type != events.MessageStored {
		t.Errorf("event type = %q, want %q", event.Type, events.MessageStored)
	}
	if event.MessageID == "" {
		t.Error("event carried no message id")
	}
	if !event.Inbound {
		t.Error("captured mail was not flagged inbound; the chime keys off this")
	}
	if event.Unread != 1 {
		t.Errorf("event unread = %d, want 1", event.Unread)
	}
	if event.ConversationID == 0 {
		t.Error("event carried no conversation id, so a client cannot place it")
	}
}

// TestRepliesThreadAndReceiptsDoNot is the threading contract in one test:
// genuine replies join their conversation, and mail that merely shares a
// subject does not.
func TestRepliesThreadAndReceiptsDoNot(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Invoice 12", "inv-12@acme.test", ""))
	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Re: Invoice 12", "inv-12-reply@acme.test", "inv-12@acme.test"))

	// Two receipts with an identical subject and no threading headers.
	m.send(t, "shop@acme.test", []string{"glenn@myapp.test"},
		mail("Your receipt", "receipt-1@acme.test", ""))
	m.send(t, "shop@acme.test", []string{"glenn@myapp.test"},
		mail("Your receipt", "receipt-2@acme.test", ""))

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)

	if len(list.Conversations) != 3 {
		t.Fatalf("inbox holds %d conversations, want 3 (one threaded pair, two separate receipts)",
			len(list.Conversations))
	}

	var invoice int
	for _, c := range list.Conversations {
		if c.Subject == "Invoice 12" {
			invoice = c.MessageCount
		}
	}
	if invoice != 2 {
		t.Errorf("the invoice thread holds %d messages, want 2", invoice)
	}
}

func TestSearchMatchesBodiesAndAddresses(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Order shipped", "s-1@acme.test", ""))
	m.send(t, "noreply@other.test", []string{"glenn@myapp.test"},
		"From: noreply@other.test\r\nTo: glenn@myapp.test\r\nSubject: Newsletter\r\nMessage-ID: <n-1@other.test>\r\n\r\nNothing to see\r\n")

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"Order", 1},
		{"other.test", 1},
		{"Nothing to see", 1},
		{"nonexistent", 0},
	} {
		var list conversationList
		m.get(t, "/api/v1/conversations?q="+url.QueryEscape(tc.query), &list)
		if len(list.Conversations) != tc.want {
			t.Errorf("search %q returned %d conversations, want %d",
				tc.query, len(list.Conversations), tc.want)
		}
	}
}

func TestReadingAThreadClearsItsUnreadCount(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Unread me", "u-1@acme.test", ""))

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	id := list.Conversations[0].ID

	response, err := http.Post(m.url(fmt.Sprintf("/api/v1/conversations/%d/seen", id)), "", nil)
	if err != nil {
		t.Fatalf("mark seen: %v", err)
	}
	response.Body.Close()

	m.get(t, "/api/v1/conversations", &list)
	if list.Unread != 0 {
		t.Errorf("unread = %d after reading the thread, want 0", list.Unread)
	}
}

// TestClearEmptiesEverything covers the CI reset: the endpoint a test suite
// calls between cases so it can assert against a known-empty inbox.
func TestClearEmptiesEverything(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("First", "c-1@acme.test", ""))
	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Second", "c-2@acme.test", ""))

	request, _ := http.NewRequest(http.MethodDelete, m.url("/api/v1/messages"), nil)
	// A cross-origin POST would be refused, so speak as the app itself.
	request.Header.Set("Sec-Fetch-Site", "same-origin")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("clear returned %d: %s", response.StatusCode, body)
	}

	var cleared struct {
		Deleted int `json:"deleted"`
	}
	json.NewDecoder(response.Body).Decode(&cleared)
	if cleared.Deleted != 2 {
		t.Errorf("cleared %d messages, want 2", cleared.Deleted)
	}

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	if len(list.Conversations) != 0 {
		t.Errorf("inbox still holds %d conversations after a clear", len(list.Conversations))
	}

	// The payloads have to go too, or a long CI run fills the disk with
	// mail nothing references.
	entries, err := os.ReadDir(filepath.Join(m.store.MailDir(), "raw"))
	if err != nil {
		t.Fatalf("read raw directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d raw files survived the clear", len(entries))
	}
}

// TestCrossOriginWriteIsRefused covers the one real exposure of a local tool:
// Mailman listens on loopback while the developer browses the web, and any
// page they open can reach it. Reads are harmless; a write must not be.
func TestCrossOriginWriteIsRefused(t *testing.T) {
	m := boot(t)

	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Keep me", "k-1@acme.test", ""))

	request, _ := http.NewRequest(http.MethodDelete, m.url("/api/v1/messages"), nil)
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	request.Header.Set("Origin", "https://evil.test")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusOK {
		t.Fatal("a cross-site request emptied the mailbox")
	}

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	if len(list.Conversations) != 1 {
		t.Errorf("mailbox holds %d conversations, want the message left alone", len(list.Conversations))
	}
}
