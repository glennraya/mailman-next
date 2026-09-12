package webhook

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
)

// sender stands up a real store in a scratch directory, the way the mailstore
// tests do. Delivery is mostly about what gets written down, so a fake store
// would test the wrong half.
func sender(t *testing.T) (*Sender, *mailstore.Store, *events.Broker) {
	t.Helper()

	home := t.TempDir()
	store, err := mailstore.Open(filepath.Join(home, "mailman.db"), filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	broker := events.New()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	return New(Options{Store: store, Broker: broker, Logger: logger}), store, broker
}

// stored puts the reply fixture in the database, because a delivery row
// references a real message.
func stored(t *testing.T, store *mailstore.Store) *mailstore.Message {
	t.Helper()

	m, _ := reply()
	err := store.InTx(context.Background(), func(tx *mailstore.Tx) error {
		id, err := tx.ResolveConversation(context.Background(), nil, m.Subject, mailstore.Now())
		if err != nil {
			return err
		}
		m.ConversationID = id
		return tx.InsertMessage(context.Background(), m)
	})
	if err != nil {
		t.Fatalf("store message: %v", err)
	}

	return m
}

func target(url string, timeout time.Duration) config.Resolved {
	return config.Resolved{
		URL:       url,
		Format:    config.FormatGeneric,
		Timeout:   timeout,
		VerifyTLS: true,
	}
}

func TestSendRecordsASuccess(t *testing.T) {
	send, store, broker := sender(t)
	m := stored(t, store)

	subscribed, cancel := broker.Subscribe()
	defer cancel()

	var got *http.Request
	var body []byte
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer app.Close()

	raw := []byte("Message-ID: <01jreply@mailman.local>\r\n\r\nbody\r\n")

	delivery, err := send.Send(context.Background(), m, raw, target(app.URL, time.Second))
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if !delivery.OK() {
		t.Errorf("delivery not OK: status %d, error %q", delivery.StatusCode, delivery.Error)
	}
	if delivery.StatusCode != http.StatusAccepted {
		t.Errorf("status = %d, want 202", delivery.StatusCode)
	}
	if delivery.Attempt != 1 {
		t.Errorf("attempt = %d, want 1", delivery.Attempt)
	}
	if delivery.ID == 0 {
		t.Error("delivery was not given an id, so it was never recorded")
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("content type = %q", got.Header.Get("Content-Type"))
	}
	if len(body) == 0 {
		t.Error("the app received an empty body")
	}

	// The row has to be readable back, since that is what the UI shows.
	attempts, err := store.ListDeliveries(context.Background(), m.ID)
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d recorded attempts, want 1", len(attempts))
	}

	select {
	case event := <-subscribed:
		if event.Type != events.DeliveryCompleted {
			t.Errorf("event type = %q", event.Type)
		}
		if event.ConversationID != m.ConversationID {
			t.Errorf("event conversation = %d, want %d", event.ConversationID, m.ConversationID)
		}
		if event.DeliveryID != delivery.ID || !event.OK {
			t.Errorf("event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Error("no delivery.completed event was published")
	}
}

// The app under test rejecting a reply is not Mailman failing. The whole
// point of the deliveries table is that this outcome is recorded and visible
// rather than raised as a server error.
func TestSendRecordsARejectionWithoutFailing(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "SQLSTATE[23000]: Integrity constraint violation")
	}))
	defer app.Close()

	delivery, err := send.Send(context.Background(), m, nil, target(app.URL, time.Second))
	if err != nil {
		t.Fatalf("send returned an error for a rejected delivery: %v", err)
	}

	if delivery.OK() {
		t.Error("a 500 was recorded as OK")
	}
	if delivery.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", delivery.StatusCode)
	}
	// The app's own message is the most useful thing in the whole feature
	// when a reply does not arrive.
	if !strings.Contains(delivery.Error, "Integrity constraint violation") {
		t.Errorf("error = %q, want the app's response body", delivery.Error)
	}
}

func TestSendTruncatesAHugeErrorBody(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, strings.Repeat("x", 1<<20))
	}))
	defer app.Close()

	delivery, err := send.Send(context.Background(), m, nil, target(app.URL, 5*time.Second))
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// A framework's HTML debug page must not land in the database whole.
	if len(delivery.Error) > errorExcerpt+len("500 Internal Server Error: ") {
		t.Errorf("error is %d bytes, want it capped near %d", len(delivery.Error), errorExcerpt)
	}
}

func TestSendRecordsATimeout(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	release := make(chan struct{})
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer app.Close()
	defer close(release)

	delivery, err := send.Send(context.Background(), m, nil, target(app.URL, 50*time.Millisecond))
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if delivery.OK() {
		t.Error("a timed-out delivery was recorded as OK")
	}
	if !strings.Contains(delivery.Error, "did not answer in time") {
		t.Errorf("error = %q, want a timeout explanation", delivery.Error)
	}
}

func TestSendRecordsARefusedConnection(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	// A server that is closed before use gives us an address nothing holds,
	// which is what a developer sees before they start their app.
	app := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := app.URL
	app.Close()

	delivery, err := send.Send(context.Background(), m, nil, target(url, time.Second))
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if delivery.OK() {
		t.Error("a refused connection was recorded as OK")
	}
	if !strings.Contains(delivery.Error, "nothing is listening") {
		t.Errorf("error = %q, want a refused-connection explanation", delivery.Error)
	}
}

// Local development certificates are self-signed, which is the whole reason
// verify_tls exists as a knob.
func TestSendHonoursTLSVerification(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer app.Close()

	route := target(app.URL, 2*time.Second)

	refused, err := send.Send(context.Background(), m, nil, route)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if refused.OK() {
		t.Error("a self-signed certificate was accepted with verification on")
	}

	route.VerifyTLS = false
	accepted, err := send.Send(context.Background(), m, nil, route)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if !accepted.OK() {
		t.Errorf("verification off still refused: %q", accepted.Error)
	}
	if accepted.Attempt != 2 {
		t.Errorf("attempt = %d, want 2 on the second send", accepted.Attempt)
	}
}

// Postmark routes authenticate with Basic credentials rather than a
// signature, so the key has to arrive as one.
func TestSendSendsBasicAuthForAPostmarkKey(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	var user string
	var ok bool
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, _, ok = r.BasicAuth()
	}))
	defer app.Close()

	route := target(app.URL, time.Second)
	route.Format = config.FormatPostmark
	route.SigningKey = "inbound-user"

	if _, err := send.Send(context.Background(), m, nil, route); err != nil {
		t.Fatalf("send: %v", err)
	}

	if !ok || user != "inbound-user" {
		t.Errorf("basic auth user = %q (present: %v)", user, ok)
	}
}

func TestSendRefusesAnUnknownFormat(t *testing.T) {
	send, store, _ := sender(t)
	m := stored(t, store)

	route := target("http://127.0.0.1:1/inbound", time.Second)
	route.Format = "smoke-signal"

	if _, err := send.Send(context.Background(), m, nil, route); err == nil {
		t.Fatal("an unknown format was sent")
	}
}
