package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"strings"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/outbound"
)

// replyRequest is the body of POST /api/v1/replies.
//
// ParentID is what makes this a reply rather than a new message: the parent's
// addresses, subject and threading chain seed the fields left empty. Anything
// set here wins over what was seeded.
type replyRequest struct {
	ParentID string `json:"parent_id"`

	From    string   `json:"from"`
	To      []string `json:"to"`
	Cc      []string `json:"cc"`
	Bcc     []string `json:"bcc"`
	ReplyTo string   `json:"reply_to"`

	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`

	// TextOnly suppresses the HTML alternative that a text reply otherwise
	// gets. Only useful for deliberately exercising a handler against a
	// text/plain-only message, which is what a reply from a terminal mail
	// client looks like.
	TextOnly bool `json:"text_only"`
}

// createReply sends a message and delivers it to the application under test.
//
// One endpoint covers both replying and composing from scratch, because the
// difference is only which fields get seeded -- a mail addressed by hand to
// order-4471@mail.myapp.test has to route and deliver exactly like a reply
// to one, and that is the behaviour being tested.
//
// This is deliberately not POST /api/v1/messages. That endpoint captures
// inbound mail for CI and must keep doing only that; delivering a webhook
// from it would change what existing tests mean.
func (s *Server) createReply(w http.ResponseWriter, r *http.Request) {
	var request replyRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxInjectBytes))
	if err := decoder.Decode(&request); err != nil {
		s.badRequest(w, r, "body must be JSON")
		return
	}

	message, err := s.buildReply(r, request)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if message == nil {
		return // buildReply already answered
	}

	// The same three rules the injection endpoint enforces, worded the same
	// way, because the compose form displays whichever it gets back.
	if message.From == "" {
		s.badRequest(w, r, "from is required")
		return
	}
	if len(message.To)+len(message.Cc)+len(message.Bcc) == 0 {
		s.badRequest(w, r, "at least one recipient is required")
		return
	}
	if strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.HTML) == "" {
		s.badRequest(w, r, "either text or html is required")
		return
	}

	raw, err := message.Build()
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	// Recipients() is the envelope the SMTP conversation would have carried:
	// To, Cc and Bcc, deduplicated. Bcc has to be in there for Bcc recovery
	// to find it again, which is the whole reason the envelope is stored
	// separately from the headers.
	recipients := message.Recipients()
	envelope := mailstore.Envelope{From: addressOf(message.From), Recipients: recipients}

	// Store before delivering. A reply that cannot be delivered is still the
	// text someone just wrote, and losing it because their config is wrong is
	// the one outcome with no recovery.
	stored, err := s.ingestor.Ingest(r.Context(), raw, envelope, mailstore.DirectionOutbound)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.respond(w, r, http.StatusCreated, s.deliver(r, stored, raw, recipients))
}

// buildReply assembles the message to send. A nil message with a nil error
// means the request was already answered.
func (s *Server) buildReply(r *http.Request, request replyRequest) (*outbound.Message, error) {
	var message outbound.Message

	if request.ParentID != "" {
		parent, err := s.store.GetMessage(r.Context(), request.ParentID)
		if err != nil {
			return nil, err
		}
		message = outbound.NewReply(parent)
	}

	// Every field the composer actually filled in overrides the seed.
	if request.From != "" {
		message.From = request.From
	}
	if len(request.To) > 0 {
		message.To = request.To
	}
	if len(request.Cc) > 0 {
		message.Cc = request.Cc
	}
	if len(request.Bcc) > 0 {
		message.Bcc = request.Bcc
	}
	if request.ReplyTo != "" {
		message.ReplyTo = request.ReplyTo
	}
	if request.Subject != "" {
		message.Subject = request.Subject
	}
	if request.Text != "" {
		message.Text = request.Text
	}
	if request.HTML != "" {
		message.HTML = request.HTML
	}

	if message.MessageID == "" {
		message.MessageID = outbound.NewMessageID()
	}

	// Give a text reply an HTML alternative, which is what every mail client
	// a person actually replies from does. Without it the message is
	// text/plain only, and an application that reads the HTML part -- a very
	// common thing for an inbound handler to do, since that is what a real
	// reply almost always carries -- stores an empty body and the reply looks
	// like it arrived blank.
	//
	// Build already fills in the reverse direction, deriving text from HTML,
	// for the same reason.
	if !request.TextOnly && message.HTML == "" {
		message.HTML = outbound.TextToHTML(message.Text)
	}

	return &message, nil
}

// replyResult is what the composer needs to report the outcome: the stored
// message, the attempt if one was made, and -- when no route matched -- a
// plain statement that the reply went nowhere.
//
// Routed means a route matched and a request was made. It says nothing about
// whether the app accepted it: that is Delivery, whose StatusCode and Error
// are the outcome. Conflating the two into one "delivered" flag would let
// Mailman report success for a reply that was refused, which is the exact
// failure this whole feature exists to make visible.
type replyResult struct {
	Message  *mailstore.Message  `json:"message"`
	Delivery *mailstore.Delivery `json:"delivery"`
	Route    *routeInfo          `json:"route"`
	Routed   bool                `json:"routed"`
	Reason   string              `json:"reason,omitempty"`
}

// routeInfo describes where a reply went, without the signing key. The UI
// shows this so a developer can see which line of their config was followed.
type routeInfo struct {
	Matched   string `json:"matched,omitempty"`
	URL       string `json:"url"`
	Format    string `json:"format"`
	Fallback  bool   `json:"fallback"`
	HasSecret bool   `json:"has_secret"`
}

func describe(route config.Resolved) *routeInfo {
	return &routeInfo{
		Matched:   route.Domain,
		URL:       route.URL,
		Format:    route.Format,
		Fallback:  route.Domain == "",
		HasSecret: route.SigningKey != "",
	}
}

// deliver resolves the route and posts, shared by sending and retrying so
// attempt numbering and route resolution cannot drift apart.
func (s *Server) deliver(r *http.Request, message *mailstore.Message, raw []byte, recipients []string) replyResult {
	result := replyResult{Message: message}

	recipient := ""
	if len(recipients) > 0 {
		recipient = recipients[0]
	}

	if s.webhook == nil {
		result.Reason = "delivery is not configured"
		return result
	}

	route, ok := s.settings().RouteFor(recipient)
	if !ok {
		// The README is explicit that this must not look like success.
		result.Reason = fmt.Sprintf(
			"no webhook route matches %s, so this reply was stored but not forwarded",
			config.Domain(recipient))
		return result
	}

	result.Route = describe(route)

	// The delivery must outlive the request that started it. If the browser
	// navigates away mid-send, cancelling here would record "context
	// canceled" for a POST the app may have processed in full -- a lie in the
	// one table that exists to be believed.
	ctx := context.WithoutCancel(r.Context())

	delivery, err := s.webhook.Send(ctx, message, raw, route)
	if err != nil {
		s.logger.Error("could not attempt delivery",
			"message", message.ID, "url", route.URL, "error", err)
		result.Reason = err.Error()
		return result
	}

	result.Delivery = delivery
	result.Routed = true
	return result
}

// listDeliveries reports every webhook attempt for one message, newest first.
func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	deliveries, err := s.store.ListDeliveries(r.Context(), message.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	body := map[string]any{"deliveries": deliveries}

	// Saying where this message would go now -- not where it went -- is what
	// makes the retry button explicable after a config change.
	if len(message.Envelope.Recipients) > 0 {
		if route, ok := s.settings().RouteFor(message.Envelope.Recipients[0]); ok {
			body["route"] = describe(route)
		}
	}

	s.respond(w, r, http.StatusOK, body)
}

// retryDelivery posts a stored reply again.
//
// It is keyed on the message rather than on a previous attempt, and it
// re-resolves the route rather than replaying the URL that was tried. That
// matters for two cases the delivery-keyed shape cannot serve: a reply that
// was never forwarded at all has no attempt to replay, and a reply sent
// before a config fix should follow the corrected route once Mailman has
// been restarted -- the message outlives the process, so the retry is still
// there to make.
func (s *Server) retryDelivery(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if message.Direction != mailstore.DirectionOutbound {
		s.badRequest(w, r, "only a message Mailman sent can be delivered")
		return
	}

	raw, err := os.ReadFile(s.store.Path(message.RawPath))
	if err != nil {
		s.fail(w, r, fmt.Errorf("read raw message: %w", err))
		return
	}

	s.respond(w, r, http.StatusOK, s.deliver(r, message, raw, message.Envelope.Recipients))
}

// resolveRoute answers where a reply to an address would be delivered.
//
// The compose form calls this as the recipient is typed, so the wildcard and
// inheritance rules are implemented and tested once in Go rather than
// reimplemented in TypeScript for a preview.
func (s *Server) resolveRoute(w http.ResponseWriter, r *http.Request) {
	recipient := strings.TrimSpace(r.URL.Query().Get("recipient"))
	if recipient == "" {
		s.badRequest(w, r, "recipient is required")
		return
	}

	route, ok := s.settings().RouteFor(recipient)
	if !ok {
		s.respond(w, r, http.StatusOK, map[string]any{
			"routed": false,
			"reason": fmt.Sprintf("no webhook route matches %s",
				config.Domain(recipient)),
		})
		return
	}

	s.respond(w, r, http.StatusOK, map[string]any{
		"routed": true,
		"route":  describe(route),
	})
}

// addressOf reduces a possibly-named address to the bare one the envelope
// carries. A parse failure leaves the original as-is: Build has already
// validated this field, so there is nothing here worth a second error path.
func addressOf(raw string) string {
	if parsed, err := mail.ParseAddress(raw); err == nil {
		return parsed.Address
	}
	return strings.TrimSpace(raw)
}
