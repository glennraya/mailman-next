// Package e2e boots the whole of Mailman -- SMTP capture, storage, the JSON
// API and the event stream -- and drives it the way a developer's project
// would. It is the test that proves the product works, as opposed to the unit
// tests that prove each piece does.
package e2e

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/glennraya/mailman/internal/webhook"
)

// instance is a running Mailman, on ports the operating system chose.
type instance struct {
	httpAddr string
	smtpAddr string
	store    *mailstore.Store
}

// boot runs Mailman with no webhook configured, which is how it runs out of
// the box.
func boot(t *testing.T) *instance {
	return bootWith(t, func(*config.Config) {})
}

// bootWith runs Mailman with the configuration a test needs, so the reply
// tests can point it at a fake app without changing what the capture tests
// mean.
func bootWith(t *testing.T, configure func(*config.Config)) *instance {
	t.Helper()

	home := t.TempDir()

	cfg := &config.Config{
		Home:            home,
		MaxMessageBytes: config.DefaultMaxSize,
		Webhook: config.Webhook{
			Format: config.DefaultFormat,
			Routes: map[string]config.Route{},
		},
	}
	configure(cfg)

	// No reload: this configuration was built in code and the config file
	// knows nothing about it, so saving would have nothing coherent to write
	// back into. A test that means to exercise saving uses bootSaved.
	return start(t, home, cfg, nil)
}

// bootSaved runs Mailman the way the binary does, resolving its configuration
// from a real config file in a scratch home. That is what lets a test save a
// setting through the API and watch the reload pick it up -- bootWith cannot,
// because its configuration exists only in memory.
func bootSaved(t *testing.T, document *config.Document) *instance {
	t.Helper()

	home := t.TempDir()
	t.Setenv("MAILMAN_HOME", home)

	// Clear anything the developer running the tests happens to have set, or
	// their environment would shadow what these tests save.
	for _, name := range []string{
		"MAILMAN_HTTP_ADDR", "MAILMAN_SMTP_ADDR", "MAILMAN_MAX_MESSAGE_BYTES",
		"MAILMAN_WEBHOOK_URL", "MAILMAN_WEBHOOK_FORMAT", "MAILMAN_WEBHOOK_SIGNING_KEY",
		"MAILMAN_WEBHOOK_TIMEOUT", "MAILMAN_WEBHOOK_VERIFY_TLS",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	if document != nil {
		if err := config.WriteDocument(config.DocumentPath(home), document); err != nil {
			t.Fatalf("seed config: %v", err)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	return start(t, home, cfg, config.Load)
}

// start stands up the whole stack on ports the operating system chooses.
func start(t *testing.T, home string, cfg *config.Config, reload func() (*config.Config, error)) *instance {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	store, err := mailstore.Open(filepath.Join(home, "mailman.db"), filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	broker := events.New()
	ingestor := mailmime.NewIngestor(store, broker, logger)
	sender := webhook.New(webhook.Options{Store: store, Broker: broker, Logger: logger})

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

	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for HTTP: %v", err)
	}

	// Built after both binds, so it can report the addresses actually in use
	// rather than the ones configured -- which here are always port 0.
	api := httpapi.New(httpapi.Options{
		Store:    store,
		Broker:   broker,
		Config:   cfg,
		Reload:   reload,
		Ingestor: ingestor,
		Webhook:  sender,
		Version:  "test",
		Logger:   logger,
		Listening: httpapi.Listening{
			HTTP:   httpListener.Addr().String(),
			SMTP:   capture.Addr(),
			Booted: cfg,
		},
	})

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

// post sends a JSON write. Writes are cross-origin protected, so the header
// a browser would attach has to be attached here too.
func (i *instance) post(t *testing.T, path string, body any, into any) int {
	t.Helper()
	return i.write(t, http.MethodPost, path, body, into)
}

func (i *instance) write(t *testing.T, method, path string, body any, into any) int {
	t.Helper()

	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode %s body: %v", path, err)
	}

	request, err := http.NewRequest(method, i.url(path), bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer response.Body.Close()

	raw, _ := io.ReadAll(response.Body)
	if into != nil {
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatalf("decode %s %s (%d): %v: %s", method, path, response.StatusCode, err, raw)
		}
	}
	return response.StatusCode
}

// put sends a JSON write with PUT, for the settings endpoint.
func (i *instance) put(t *testing.T, path string, body any, into any) int {
	t.Helper()
	return i.write(t, http.MethodPut, path, body, into)
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

// fakeApp is the application under test: it records what arrives at its
// inbound route and answers with whatever the test asked for.
type fakeApp struct {
	server   *httptest.Server
	received chan *receivedWebhook

	mu      sync.Mutex
	replies []int
}

type receivedWebhook struct {
	ContentType string
	Header      http.Header
	Form        url.Values
	Body        []byte
}

// newFakeApp answers with each status in turn, repeating the last one. Giving
// it 500 then 200 is how the retry path is exercised.
func newFakeApp(t *testing.T, statuses ...int) *fakeApp {
	t.Helper()

	if len(statuses) == 0 {
		statuses = []int{http.StatusOK}
	}

	app := &fakeApp{received: make(chan *receivedWebhook, 8), replies: statuses}
	app.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		got := &receivedWebhook{
			ContentType: r.Header.Get("Content-Type"),
			Header:      r.Header.Clone(),
			Body:        body,
		}

		// Re-parse the body rather than reading r.Form, so the test sees
		// exactly the bytes that were sent.
		r.Body = io.NopCloser(bytes.NewReader(body))
		if err := r.ParseMultipartForm(1 << 20); err == nil {
			got.Form = r.MultipartForm.Value
		}

		app.received <- got
		w.WriteHeader(app.next())
	}))
	t.Cleanup(app.server.Close)

	return app
}

func (a *fakeApp) next() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	status := a.replies[0]
	if len(a.replies) > 1 {
		a.replies = a.replies[1:]
	}
	return status
}

func (a *fakeApp) await(t *testing.T) *receivedWebhook {
	t.Helper()

	select {
	case got := <-a.received:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the app under test received no webhook")
		return nil
	}
}

type replyResult struct {
	Message struct {
		ID             string `json:"id"`
		ConversationID int64  `json:"conversation_id"`
		Direction      string `json:"direction"`
		MessageID      string `json:"message_id"`
		InReplyTo      string `json:"in_reply_to"`
		Subject        string `json:"subject"`
	} `json:"message"`
	Delivery *struct {
		ID         int64  `json:"id"`
		TargetURL  string `json:"target_url"`
		Format     string `json:"format"`
		Attempt    int    `json:"attempt"`
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
	} `json:"delivery"`
	Route *struct {
		Matched  string `json:"matched"`
		URL      string `json:"url"`
		Format   string `json:"format"`
		Fallback bool   `json:"fallback"`
	} `json:"route"`
	Routed bool   `json:"routed"`
	Reason string `json:"reason"`
}

type deliveryList struct {
	Deliveries []struct {
		ID         int64  `json:"id"`
		Attempt    int    `json:"attempt"`
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
	} `json:"deliveries"`
}

// capture puts one mail in the inbox and returns the stored id of the message
// a reply should answer, plus its conversation.
func (i *instance) capture(t *testing.T) (messageID string, conversationID int64) {
	t.Helper()

	i.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Order 4471 has shipped", "order-4471@acme.test", "",
			"Reply-To: order-4471+t3h2@mail.acme.test"))

	var list conversationList
	i.get(t, "/api/v1/conversations", &list)
	if len(list.Conversations) != 1 {
		t.Fatalf("inbox holds %d conversations, want 1", len(list.Conversations))
	}

	var detail conversationDetail
	i.get(t, fmt.Sprintf("/api/v1/conversations/%d", list.Conversations[0].ID), &detail)
	if len(detail.Messages) != 1 {
		t.Fatalf("thread holds %d messages, want 1", len(detail.Messages))
	}

	// Opening a thread marks it read, which is what the UI does before
	// anyone can click Reply. Without it the unread count would still carry
	// the mail being answered.
	i.post(t, fmt.Sprintf("/api/v1/conversations/%d/seen", list.Conversations[0].ID), nil, nil)

	return detail.Messages[0].ID, list.Conversations[0].ID
}

// TestReplyReachesTheAppAsAnInboundWebhook is the whole reason Mailman
// exists: an application that expects an answer to the mail it sent gets one,
// locally, in the shape its production handler already reads.
func TestReplyReachesTheAppAsAnInboundWebhook(t *testing.T) {
	app := newFakeApp(t)

	m := bootWith(t, func(cfg *config.Config) {
		cfg.Webhook.Routes["mail.acme.test"] = config.Route{
			URL:        app.server.URL + "/webhooks/mailgun/inbound",
			Format:     config.FormatMailgun,
			SigningKey: "key-abc123",
		}
	})

	parentID, conversationID := m.capture(t)

	var result replyResult
	status := m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": parentID,
		"text":      "Please cancel this order.",
	}, &result)

	if status != http.StatusCreated {
		t.Fatalf("POST /replies returned %d, want 201: %+v", status, result)
	}
	if !result.Routed {
		t.Fatalf("reply was not routed: %s", result.Reason)
	}
	if result.Route == nil || result.Route.Matched != "mail.acme.test" {
		t.Errorf("route = %+v, want the exact key to have matched", result.Route)
	}
	if result.Delivery == nil || result.Delivery.StatusCode != http.StatusOK {
		t.Fatalf("delivery = %+v, want a 200", result.Delivery)
	}
	if result.Delivery.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", result.Delivery.Attempt)
	}

	// What the app actually received is the only assertion that proves the
	// feature. Everything above only proves Mailman believes it worked.
	got := app.await(t)

	if !strings.HasPrefix(got.ContentType, "multipart/form-data") {
		t.Errorf("content type = %q, want Mailgun's multipart form", got.ContentType)
	}
	// Reply-To decided the recipient, which is how an app ties an answer back
	// to a record.
	if recipient := got.Form.Get("recipient"); recipient != "order-4471+t3h2@mail.acme.test" {
		t.Errorf("recipient = %q, want the Reply-To address", recipient)
	}
	if sender := got.Form.Get("sender"); sender != "glenn@myapp.test" {
		t.Errorf("sender = %q, want the address the mail was addressed to", sender)
	}
	if body := got.Form.Get("body-plain"); !strings.Contains(body, "Please cancel this order.") {
		t.Errorf("body-plain = %q", body)
	}

	// A text reply still carries an HTML alternative, because that is what a
	// real reply carries and because plenty of inbound handlers read only the
	// HTML part -- Movepro's does. Without these two fields the reply is
	// stored with an empty body and looks like it arrived blank.
	for _, field := range []string{"body-html", "stripped-html"} {
		if value := got.Form.Get(field); !strings.Contains(value, "Please cancel this order.") {
			t.Errorf("%s = %q, want the reply rendered as HTML", field, value)
		}
	}
	if subject := got.Form.Get("subject"); subject != "Re: Order 4471 has shipped" {
		t.Errorf("subject = %q", subject)
	}

	// A production Mailgun handler verifies before it does anything else, so
	// if this does not match, nothing downstream of it runs.
	mac := hmac.New(sha256.New, []byte("key-abc123"))
	mac.Write([]byte(got.Form.Get("timestamp") + got.Form.Get("token")))
	if want := hex.EncodeToString(mac.Sum(nil)); got.Form.Get("signature") != want {
		t.Errorf("signature = %q, want %q", got.Form.Get("signature"), want)
	}

	var headers [][2]string
	if err := json.Unmarshal([]byte(got.Form.Get("message-headers")), &headers); err != nil {
		t.Fatalf("decode message-headers: %v", err)
	}
	var inReplyTo string
	for _, header := range headers {
		if strings.EqualFold(header[0], "In-Reply-To") {
			inReplyTo = header[1]
		}
	}
	if inReplyTo != "<order-4471@acme.test>" {
		t.Errorf("In-Reply-To in message-headers = %q, want the original's Message-ID", inReplyTo)
	}

	// And as its own field, which is how a real handler reads it. Movepro's
	// production controller gates on isset($data['In-Reply-To']) and drops
	// the reply without it, so this is the assertion that proves the payload
	// is usable rather than merely complete.
	if field := got.Form.Get("In-Reply-To"); field != "<order-4471@acme.test>" {
		t.Errorf("In-Reply-To field = %q, want the original's Message-ID", field)
	}
	if field := got.Form.Get("Message-Id"); field == "" {
		t.Error("no Message-Id field")
	}
	if got.Form.Get("X-Mailgun-Incoming") != "Yes" {
		t.Errorf("X-Mailgun-Incoming = %q", got.Form.Get("X-Mailgun-Incoming"))
	}
	if domain := got.Form.Get("domain"); domain != "mail.acme.test" {
		t.Errorf("domain = %q", domain)
	}

	// The reply belongs in the thread it answers, and it is not news to the
	// person who just wrote it.
	var detail conversationDetail
	m.get(t, fmt.Sprintf("/api/v1/conversations/%d", conversationID), &detail)

	if len(detail.Messages) != 2 {
		t.Fatalf("thread holds %d messages, want 2", len(detail.Messages))
	}
	if result.Message.ConversationID != conversationID {
		t.Errorf("reply landed in conversation %d, want %d",
			result.Message.ConversationID, conversationID)
	}
	if result.Message.Direction != "outbound" {
		t.Errorf("direction = %q, want outbound", result.Message.Direction)
	}

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	if list.Unread != 0 {
		t.Errorf("unread = %d, want 0: a reply you wrote is not unread mail", list.Unread)
	}

	// And the attempt is readable back, which is what the UI shows.
	var deliveries deliveryList
	m.get(t, "/api/v1/messages/"+result.Message.ID+"/deliveries", &deliveries)
	if len(deliveries.Deliveries) != 1 || deliveries.Deliveries[0].StatusCode != http.StatusOK {
		t.Errorf("deliveries = %+v, want one successful attempt", deliveries.Deliveries)
	}
}

// A reply with nowhere to go must still be stored, and Mailman must say so
// rather than report a delivery that never happened.
func TestAReplyWithNoRouteIsStoredButNotForwarded(t *testing.T) {
	m := boot(t)

	parentID, conversationID := m.capture(t)

	var result replyResult
	status := m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": parentID,
		"text":      "Please cancel this order.",
	}, &result)

	if status != http.StatusCreated {
		t.Fatalf("POST /replies returned %d, want 201", status)
	}
	if result.Routed {
		t.Error("a reply was reported as routed with no webhook configured")
	}
	if result.Delivery != nil {
		t.Errorf("delivery = %+v, want none", result.Delivery)
	}
	if !strings.Contains(result.Reason, "mail.acme.test") {
		t.Errorf("reason = %q, want it to name the unmatched domain", result.Reason)
	}

	// Losing what someone typed because their config is wrong would be the
	// one unrecoverable outcome.
	var detail conversationDetail
	m.get(t, fmt.Sprintf("/api/v1/conversations/%d", conversationID), &detail)
	if len(detail.Messages) != 2 {
		t.Fatalf("thread holds %d messages, want the reply kept", len(detail.Messages))
	}

	var deliveries deliveryList
	m.get(t, "/api/v1/messages/"+result.Message.ID+"/deliveries", &deliveries)
	if len(deliveries.Deliveries) != 0 {
		t.Errorf("deliveries = %+v, want none", deliveries.Deliveries)
	}
}

// The app under test throwing is not Mailman failing. The attempt is recorded
// with the app's own error, and retrying once it recovers delivers.
func TestRetryDeliversAgainAfterTheAppRecovers(t *testing.T) {
	app := newFakeApp(t, http.StatusInternalServerError, http.StatusOK)

	m := bootWith(t, func(cfg *config.Config) {
		cfg.Webhook.URL = app.server.URL + "/inbound"
		cfg.Webhook.Format = config.FormatPostmark
	})

	parentID, _ := m.capture(t)

	var first replyResult
	if status := m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": parentID,
		"text":      "Please cancel this order.",
	}, &first); status != http.StatusCreated {
		t.Fatalf("POST /replies returned %d, want 201", status)
	}

	// A rejected delivery is still a successful request: the reply exists and
	// the reason is recorded.
	if !first.Routed || first.Delivery == nil {
		t.Fatalf("reply was not attempted: %+v", first)
	}
	if first.Delivery.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", first.Delivery.StatusCode)
	}
	if first.Route == nil || !first.Route.Fallback {
		t.Errorf("route = %+v, want the top-level fallback", first.Route)
	}

	got := app.await(t)
	if got.ContentType != "application/json" {
		t.Errorf("content type = %q, want Postmark's JSON", got.ContentType)
	}

	var postmark struct {
		MessageStream     string `json:"MessageStream"`
		OriginalRecipient string `json:"OriginalRecipient"`
		MailboxHash       string `json:"MailboxHash"`
		StrippedTextReply string `json:"StrippedTextReply"`
	}
	if err := json.Unmarshal(got.Body, &postmark); err != nil {
		t.Fatalf("decode postmark payload: %v", err)
	}
	if postmark.MessageStream != "inbound" {
		t.Errorf("MessageStream = %q", postmark.MessageStream)
	}
	if postmark.OriginalRecipient != "order-4471+t3h2@mail.acme.test" {
		t.Errorf("OriginalRecipient = %q", postmark.OriginalRecipient)
	}
	// The plus-addressed record reference an app keys off.
	if postmark.MailboxHash != "t3h2" {
		t.Errorf("MailboxHash = %q, want %q", postmark.MailboxHash, "t3h2")
	}
	if postmark.StrippedTextReply != "Please cancel this order." {
		t.Errorf("StrippedTextReply = %q", postmark.StrippedTextReply)
	}

	// Retry: same message, resolved afresh, appended as a second attempt.
	var second replyResult
	if status := m.post(t, "/api/v1/messages/"+first.Message.ID+"/deliveries", nil, &second); status != http.StatusOK {
		t.Fatalf("retry returned %d, want 200", status)
	}
	if second.Delivery == nil || second.Delivery.StatusCode != http.StatusOK {
		t.Fatalf("retry delivery = %+v, want a 200", second.Delivery)
	}
	if second.Delivery.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", second.Delivery.Attempt)
	}
	app.await(t)

	var deliveries deliveryList
	m.get(t, "/api/v1/messages/"+first.Message.ID+"/deliveries", &deliveries)
	if len(deliveries.Deliveries) != 2 {
		t.Fatalf("got %d attempts, want 2", len(deliveries.Deliveries))
	}
	// Newest first, so the successful retry leads.
	if deliveries.Deliveries[0].Attempt != 2 || deliveries.Deliveries[1].Attempt != 1 {
		t.Errorf("attempts = %+v, want newest first", deliveries.Deliveries)
	}
	if deliveries.Deliveries[1].Error == "" {
		t.Error("the failed attempt recorded no reason")
	}
}

// Composing from scratch routes and delivers exactly like a reply: an address
// like order-4471@mail.acme.test has to work whether it was typed or seeded.
func TestAComposedMessageIsDeliveredToo(t *testing.T) {
	app := newFakeApp(t)

	m := bootWith(t, func(cfg *config.Config) {
		cfg.Webhook.Routes["*.acme.test"] = config.Route{URL: app.server.URL + "/inbound"}
	})

	var result replyResult
	status := m.post(t, "/api/v1/replies", map[string]any{
		"from":    "glenn@myapp.test",
		"to":      []string{"order-4471@mail.acme.test"},
		"subject": "Cancel my order",
		"text":    "Please cancel it.",
	}, &result)

	if status != http.StatusCreated {
		t.Fatalf("POST /replies returned %d, want 201: %+v", status, result)
	}
	if !result.Routed || result.Delivery == nil {
		t.Fatalf("composed message was not routed: %s", result.Reason)
	}
	if result.Route == nil || result.Route.Matched != "*.acme.test" {
		t.Errorf("route = %+v, want the wildcard to have matched", result.Route)
	}
	if result.Route.Format != config.FormatGeneric {
		t.Errorf("format = %q, want the inherited default", result.Route.Format)
	}

	got := app.await(t)

	var generic struct {
		Recipient string `json:"recipient"`
		Subject   string `json:"subject"`
		Text      string `json:"text"`
		Raw       string `json:"raw"`
	}
	if err := json.Unmarshal(got.Body, &generic); err != nil {
		t.Fatalf("decode generic payload: %v", err)
	}
	if generic.Recipient != "order-4471@mail.acme.test" {
		t.Errorf("recipient = %q", generic.Recipient)
	}
	if generic.Subject != "Cancel my order" {
		t.Errorf("subject = %q", generic.Subject)
	}
	if !strings.Contains(generic.Raw, "Subject: Cancel my order") {
		t.Error("raw is not the verbatim message")
	}
}

// text_only reproduces the rarer reply that carries no HTML at all, which is
// what a terminal mail client sends.
func TestATextOnlyReplyCarriesNoHTML(t *testing.T) {
	app := newFakeApp(t)

	m := bootWith(t, func(cfg *config.Config) {
		cfg.Webhook.Routes["mail.acme.test"] = config.Route{
			URL:    app.server.URL + "/inbound",
			Format: config.FormatMailgun,
		}
	})

	parentID, _ := m.capture(t)

	var result replyResult
	m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": parentID,
		"text":      "Cancel it.",
		"text_only": true,
	}, &result)

	got := app.await(t)

	if got.Form.Get("body-plain") != "Cancel it." {
		t.Errorf("body-plain = %q", got.Form.Get("body-plain"))
	}
	if _, present := got.Form["body-html"]; present {
		t.Errorf("body-html = %q, want it absent", got.Form.Get("body-html"))
	}
}

// A real HTML email is a whole document, and its <style> survives being
// nested inside the frame's own. Every template resets body padding, so a
// gutter that lives on body disappears on exactly the messages worth reading.
func TestTheHTMLFrameKeepsAGutterAgainstATemplateThatResetsBody(t *testing.T) {
	m := boot(t)

	// The shape a transactional template actually has.
	body := "MIME-Version: 1.0\r\n" +
		"From: Movers <hello@acme.test>\r\n" +
		"To: Glenn <glenn@myapp.test>\r\n" +
		"Subject: We have found a truck\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>body{margin:0;padding:0;background:#f4f4f5}</style></head>" +
		"<body><p>Hi Chloe, great news.</p></body></html>\r\n"

	m.send(t, "hello@acme.test", []string{"glenn@myapp.test"}, body)

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	var detail conversationDetail
	m.get(t, fmt.Sprintf("/api/v1/conversations/%d", list.Conversations[0].ID), &detail)

	response, err := http.Get(m.url("/api/v1/messages/" + detail.Messages[0].ID + "/html"))
	if err != nil {
		t.Fatalf("fetch html: %v", err)
	}
	defer response.Body.Close()

	rendered, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	frame := string(rendered)

	// The gutter is inline on a wrapper, so the template's reset cannot
	// reach it.
	if !strings.Contains(frame, `<div style="padding:16px">`) {
		t.Error("no padded wrapper around the body")
	}
	// And it has to sit outside the message, not inside it.
	wrapper := strings.Index(frame, `<div style="padding:16px">`)
	message := strings.Index(frame, "Hi Chloe")
	if wrapper < 0 || message < 0 || wrapper > message {
		t.Errorf("wrapper at %d, message at %d: the wrapper must enclose the message", wrapper, message)
	}
	// The template's own reset is still present -- it is the message, and
	// the point is that it no longer decides the gutter.
	if !strings.Contains(frame, "padding:0") {
		t.Error("the template's own CSS was altered; the body must be served verbatim")
	}
}

// Validation has to match what the compose form already displays.
func TestReplyValidation(t *testing.T) {
	m := boot(t)

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"no sender", map[string]any{"to": []string{"a@b.test"}, "text": "hi"}, "from is required"},
		{"no recipient", map[string]any{"from": "a@b.test", "text": "hi"}, "at least one recipient is required"},
		{"no body", map[string]any{"from": "a@b.test", "to": []string{"c@d.test"}}, "either text or html is required"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var failure struct {
				Error string `json:"error"`
			}
			if status := m.post(t, "/api/v1/replies", c.body, &failure); status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", status)
			}
			if failure.Error != c.want {
				t.Errorf("error = %q, want %q", failure.Error, c.want)
			}
		})
	}

	var failure struct {
		Error string `json:"error"`
	}
	if status := m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": "01jnosuchmessage",
		"text":      "hi",
	}, &failure); status != http.StatusNotFound {
		t.Errorf("replying to a missing message returned %d, want 404", status)
	}
}
