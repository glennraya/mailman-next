package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/outbound"
)

// maxInjectBytes caps the injection endpoint. Captured mail is bounded by the
// SMTP server's own limit; this is the equivalent for the HTTP door.
const maxInjectBytes = 32 << 20

func (s *Server) listConversations(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	opts := mailstore.ListOptions{Query: query.Get("q")}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			s.badRequest(w, r, "limit must be a number")
			return
		}
		opts.Limit = limit
	}

	// Keyset paging: the client sends back the last id it saw rather than an
	// offset, so mail arriving mid-scroll cannot shift the page boundary and
	// duplicate or skip a row.
	if raw := query.Get("before"); raw != "" {
		before, err := parseID(raw)
		if err != nil {
			s.badRequest(w, r, "before must be a conversation id")
			return
		}
		opts.Before = before
	}

	conversations, err := s.store.ListConversations(r.Context(), opts)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	unread, err := s.store.CountUnread(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	body := map[string]any{
		"conversations": conversations,
		"unread":        unread,
	}
	// Only a full page can have more behind it, so a short one ends the
	// list and sends no cursor.
	if len(conversations) == opts.EffectiveLimit() {
		body["next_cursor"] = conversations[len(conversations)-1].ID
	}

	s.respond(w, r, http.StatusOK, body)
}

func (s *Server) getConversation(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		s.badRequest(w, r, "conversation id must be a number")
		return
	}

	conversation, messages, err := s.store.GetConversation(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.respond(w, r, http.StatusOK, map[string]any{
		"conversation": conversation,
		"messages":     messages,
	})
}

func (s *Server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		s.badRequest(w, r, "conversation id must be a number")
		return
	}

	if err := s.store.DeleteConversation(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}

	s.publish(r, events.Event{Type: events.ConversationDeleted, ConversationID: id})
	s.respond(w, r, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) markConversationSeen(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		s.badRequest(w, r, "conversation id must be a number")
		return
	}

	changed, err := s.store.MarkConversationSeen(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Only announce a change that happened. Re-opening a read thread should
	// not make every other tab redraw its badge.
	if changed {
		s.publish(r, events.Event{Type: events.ConversationRead, ConversationID: id})
	}

	unread, err := s.store.CountUnread(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.respond(w, r, http.StatusOK, map[string]any{"unread": unread})
}

func (s *Server) getMessage(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, http.StatusOK, message)
}

// getMessageRaw serves the .eml exactly as captured. This is what a developer
// reaches for when a body looks wrong and they need to see the headers and
// encoding that produced it.
func (s *Server) getMessageRaw(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	raw, err := os.ReadFile(s.store.Path(message.RawPath))
	if err != nil {
		s.fail(w, r, fmt.Errorf("read raw message: %w", err))
		return
	}

	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", message.ID+".eml"))
	}

	w.Header().Set("Content-Type", "message/rfc822")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(raw)
}

func (s *Server) markMessageSeen(w http.ResponseWriter, r *http.Request) {
	changed, err := s.store.MarkSeen(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	unread, err := s.store.CountUnread(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if changed {
		s.publish(r, events.Event{
			Type:      events.ConversationRead,
			MessageID: r.PathValue("id"),
			Unread:    unread,
		})
	}

	s.respond(w, r, http.StatusOK, map[string]any{"unread": unread})
}

func (s *Server) deleteMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	conversationID, conversationGone, err := s.store.DeleteMessage(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	unread, err := s.store.CountUnread(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.publish(r, events.Event{
		Type:           events.MessageDeleted,
		ConversationID: conversationID,
		MessageID:      id,
		Unread:         unread,
	})

	s.respond(w, r, http.StatusOK, map[string]any{
		"deleted": true,
		// The UI has to move its selection when the last message in a
		// thread goes, so say whether the thread went with it.
		"conversation_deleted": conversationGone,
		"conversation_id":      conversationID,
	})
}

// clearMailbox empties everything. This is the CI reset: run it between test
// cases and assert against a known-empty inbox.
func (s *Server) clearMailbox(w http.ResponseWriter, r *http.Request) {
	deleted, err := s.store.Clear(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.publish(r, events.Event{Type: events.MailboxCleared})
	s.respond(w, r, http.StatusOK, map[string]any{"deleted": deleted})
}

// injectRequest is the body of POST /api/v1/messages. Either a complete raw
// message, or the pieces to build one.
type injectRequest struct {
	Raw string `json:"raw"`

	From    string   `json:"from"`
	To      []string `json:"to"`
	Cc      []string `json:"cc"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html"`
}

// injectMessage puts a message into the mailbox without going through SMTP.
// A test that wants a specific fixture in the inbox can post it here instead
// of standing up a mailer.
func (s *Server) injectMessage(w http.ResponseWriter, r *http.Request) {
	var request injectRequest

	decoder := json.NewDecoder(io.LimitReader(r.Body, maxInjectBytes))
	if err := decoder.Decode(&request); err != nil {
		s.badRequest(w, r, "body must be JSON")
		return
	}

	raw, envelope, err := buildInjected(request)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	message, err := s.ingestor.Ingest(r.Context(), raw, envelope, mailstore.DirectionInbound)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.respond(w, r, http.StatusCreated, message)
}

// buildInjected turns the request into raw bytes and an envelope. A raw
// message is taken as-is; otherwise the fields are assembled into one.
func buildInjected(request injectRequest) ([]byte, mailstore.Envelope, error) {
	if raw := strings.TrimSpace(request.Raw); raw != "" {
		envelope := mailstore.Envelope{From: request.From, Recipients: request.To}
		return []byte(raw), envelope, nil
	}

	if request.From == "" {
		return nil, mailstore.Envelope{}, fmt.Errorf("from is required")
	}
	if len(request.To) == 0 {
		return nil, mailstore.Envelope{}, fmt.Errorf("at least one recipient is required")
	}
	if request.Text == "" && request.HTML == "" {
		return nil, mailstore.Envelope{}, fmt.Errorf("either text or html is required")
	}

	message := outbound.Message{
		From:    request.From,
		To:      request.To,
		Cc:      request.Cc,
		Subject: request.Subject,
		Text:    request.Text,
		HTML:    request.HTML,
	}

	raw, err := message.Build()
	if err != nil {
		return nil, mailstore.Envelope{}, err
	}

	envelope := mailstore.Envelope{
		From:       request.From,
		Recipients: append(append([]string{}, request.To...), request.Cc...),
	}
	return raw, envelope, nil
}

// getAttachment serves one stored part. Inline parts are served inline so an
// HTML body's images render; everything else downloads.
func (s *Server) getAttachment(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(r.PathValue("id"))
	if err != nil {
		s.badRequest(w, r, "attachment id must be a number")
		return
	}

	attachment, err := s.store.GetAttachment(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	file, err := os.Open(s.store.Path(attachment.Path))
	if err != nil {
		s.fail(w, r, fmt.Errorf("open attachment: %w", err))
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		s.fail(w, r, fmt.Errorf("stat attachment: %w", err))
		return
	}

	disposition := "attachment"
	if attachment.Inline || r.URL.Query().Get("disposition") == "inline" {
		disposition = "inline"
	}

	w.Header().Set("Content-Type", attachment.MediaType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("%s; filename=%q", disposition, attachment.Filename))
	// The bytes came from an untrusted sender. Never let a browser decide
	// this is something more executable than what the part declared.
	w.Header().Set("X-Content-Type-Options", "nosniff")

	http.ServeContent(w, r, attachment.Filename, info.ModTime(), file)
}

// publish announces a change, filling in the unread count when the caller has
// not already.
func (s *Server) publish(r *http.Request, event events.Event) {
	if s.broker == nil {
		return
	}

	if event.Unread == 0 {
		if unread, err := s.store.CountUnread(r.Context()); err == nil {
			event.Unread = unread
		}
	}

	s.broker.Publish(event)
}
