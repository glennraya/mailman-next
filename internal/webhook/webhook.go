package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
)

// errorExcerpt caps how much of a failed response is kept. A development
// framework answers an unhandled exception with a whole HTML debug page, and
// megabytes of it in a SQLite column would be useless. The opening bytes,
// though, are usually the exception message itself -- the one thing the
// developer actually needs.
const errorExcerpt = 2 << 10

// Store is the part of mailstore the sender writes to. Narrowed to an
// interface so the delivery tests need no database.
type Store interface {
	NextAttempt(ctx context.Context, messageID string) (int, error)
	RecordDelivery(ctx context.Context, delivery *mailstore.Delivery) error
	CountUnread(ctx context.Context) (int, error)
}

// Options wires a sender.
type Options struct {
	Store  Store
	Broker *events.Broker
	Logger *slog.Logger
}

// Sender posts replies to the application under test.
type Sender struct {
	store  Store
	broker *events.Broker
	logger *slog.Logger

	// Two clients, built once. Skipping verification needs its own
	// transport, and a transport built per request would abandon its
	// connection pool every time.
	verifying *http.Client
	insecure  *http.Client
}

// New builds a sender. Per-route timeouts are applied with a context rather
// than on the client, which is what lets one client serve every route.
func New(opts Options) *Sender {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	insecure := http.DefaultTransport.(*http.Transport).Clone()
	insecure.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	return &Sender{
		store:     opts.Store,
		broker:    opts.Broker,
		logger:    logger,
		verifying: &http.Client{},
		insecure:  &http.Client{Transport: insecure},
	}
}

// Send posts one message to its resolved route and records the attempt.
//
// A refused connection, a timeout or a 500 from the app under test is a
// recorded failure, not an error return: the caller wants the Delivery row
// either way, and that row is the only place the reason is visible. The error
// return is reserved for not being able to build or record the attempt at
// all.
func (s *Sender) Send(ctx context.Context, m *mailstore.Message, raw []byte, route config.Resolved) (*mailstore.Delivery, error) {
	attempt, err := s.store.NextAttempt(ctx, m.ID)
	if err != nil {
		return nil, err
	}

	delivery := &mailstore.Delivery{
		MessageID: m.ID,
		TargetURL: route.URL,
		Format:    route.Format,
		Attempt:   attempt,
	}

	payload, err := Build(m, raw, route)
	if err != nil {
		return nil, err
	}

	started := time.Now()
	status, failure := s.post(ctx, payload, route)
	delivery.DurationMS = time.Since(started).Milliseconds()
	delivery.StatusCode = status
	delivery.Error = failure

	if err := s.store.RecordDelivery(ctx, delivery); err != nil {
		return nil, err
	}

	if delivery.OK() {
		s.logger.Info("reply delivered",
			"message", m.ID, "url", route.URL, "format", route.Format,
			"status", status, "duration", time.Duration(delivery.DurationMS)*time.Millisecond)
	} else {
		s.logger.Warn("reply not delivered",
			"message", m.ID, "url", route.URL, "format", route.Format,
			"status", status, "attempt", attempt, "error", failure)
	}

	s.announce(ctx, m, delivery)
	return delivery, nil
}

// announce tells every open tab how the delivery went.
//
// ConversationID is not decoration here: the client refreshes the open thread
// only when a change was in it, so an event without it would leave a retry's
// outcome invisible until something else happened to trigger a reload.
func (s *Sender) announce(ctx context.Context, m *mailstore.Message, delivery *mailstore.Delivery) {
	if s.broker == nil {
		return
	}

	// Every event carries the mailbox-wide unread count, because the client
	// reads it straight onto its badge. Publishing a zero here would blank
	// the badge on a delivery that changed nothing about what is unread.
	unread, err := s.store.CountUnread(ctx)
	if err != nil {
		s.logger.Warn("could not count unread mail", "error", err)
	}

	s.broker.Publish(events.Event{
		Type:           events.DeliveryCompleted,
		ConversationID: m.ConversationID,
		MessageID:      m.ID,
		DeliveryID:     delivery.ID,
		Unread:         unread,
		OK:             delivery.OK(),
	})
}

// post makes the request and reports the status code and, if it did not
// succeed, why. The two results are exclusive: a 2xx never carries an error
// string, because Delivery.OK reads both.
func (s *Sender) post(ctx context.Context, payload *Payload, route config.Resolved) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, route.Timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, route.URL, bytes.NewReader(payload.Body))
	if err != nil {
		return 0, fmt.Sprintf("build request: %v", err)
	}

	request.Header.Set("Content-Type", payload.ContentType)
	request.Header.Set("User-Agent", "Mailman")
	for name, values := range payload.Header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	if payload.BasicAuthUser != "" {
		request.SetBasicAuth(payload.BasicAuthUser, "")
	}

	client := s.verifying
	if !route.VerifyTLS {
		client = s.insecure
	}

	response, err := client.Do(request)
	if err != nil {
		// The URL can carry a signing key in its credentials, so the error
		// is reported without it rather than echoed whole.
		return 0, requestError(err, route.URL)
	}
	defer response.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(response.Body, errorExcerpt))
	// Drain the rest so the connection can be reused rather than dropped
	// mid-response, which would cost a new handshake on every reply.
	io.Copy(io.Discard, response.Body)

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, responseError(response.Status, body)
	}
	return response.StatusCode, ""
}

// requestError describes a transport failure in one line a developer can act
// on. The two cases worth naming are the app not listening and the app taking
// too long, because the fix differs: start it, or look at what it is doing.
func requestError(err error, url string) string {
	message := err.Error()

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "the app under test did not answer in time"
	case strings.Contains(message, "connection refused"):
		return "nothing is listening at " + url
	}

	return message
}

// responseError keeps the status line and whatever the app said, collapsed to
// one line so it reads in a table. A framework's exception page opens with the
// message, which is the part worth surfacing.
func responseError(status string, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return status
	}

	text = strings.Join(strings.Fields(text), " ")
	return status + ": " + text
}
