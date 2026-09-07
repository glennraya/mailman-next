package mailmime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/glennraya/mailman/internal/conversation"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
)

// Ingestor stores captured and composed mail. It is the single path into the
// database: SMTP capture, the injection endpoint and Mailman's own replies
// all come through here, so threading and Bcc recovery behave identically
// whatever produced the message.
type Ingestor struct {
	store  *mailstore.Store
	broker *events.Broker
	logger *slog.Logger
}

// NewIngestor wires an ingestor. The broker may be nil, in which case nothing
// is announced -- useful in tests that only care about what was stored.
func NewIngestor(store *mailstore.Store, broker *events.Broker, logger *slog.Logger) *Ingestor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Ingestor{store: store, broker: broker, logger: logger}
}

// Ingest stores one message and returns it as persisted.
//
// The raw bytes are written to disk before anything is parsed, so even a
// message that defeats the parser is recoverable in full. If the parse fails
// outright the message is still stored, with the raw text as its body.
func (i *Ingestor) Ingest(ctx context.Context, raw []byte, envelope mailstore.Envelope, direction string) (*mailstore.Message, error) {
	id := strings.ToLower(ulid.Make().String())

	rawPath := filepath.ToSlash(filepath.Join("raw", id+".eml"))
	if err := i.writeFile(rawPath, raw); err != nil {
		return nil, err
	}

	message, err := i.persist(ctx, id, rawPath, raw, envelope, direction)
	if err != nil {
		// Nothing was committed, so the files are orphans. Remove them
		// rather than leaving the mail directory to grow on every failure.
		os.Remove(i.store.Path(rawPath))
		os.RemoveAll(filepath.Join(i.store.AttachmentDir(), id))
		return nil, err
	}

	i.announce(ctx, message)
	return message, nil
}

func (i *Ingestor) persist(ctx context.Context, id, rawPath string, raw []byte, envelope mailstore.Envelope, direction string) (*mailstore.Message, error) {
	now := mailstore.Now()

	message := &mailstore.Message{
		ID:        id,
		Direction: direction,
		Envelope:  envelope,
		SizeBytes: int64(len(raw)),
		RawPath:   rawPath,
		CreatedAt: now,
	}

	// Mailman's own replies are not news to the person who just wrote them.
	if direction == mailstore.DirectionOutbound {
		message.SeenAt = now
	}

	parsed, err := Parse(raw)
	if err != nil {
		// Unparseable, but not lost: keep the whole payload as the body so
		// the message is still visible and searchable in the inbox.
		i.logger.Warn("storing unparseable message as raw text",
			"message", id, "error", err)
		message.TextBody = string(raw)
	} else {
		applyParsed(message, parsed)
		if len(parsed.Warnings) > 0 {
			i.logger.Info("message parsed with warnings",
				"message", id, "warnings", len(parsed.Warnings), "degraded", parsed.Degraded)
		}
	}

	message.Bcc = DeriveBcc(envelope, message.To, message.Cc)

	attachments, err := i.writeParts(id, parsed)
	if err != nil {
		return nil, err
	}
	message.Attachments = attachments

	err = i.store.InTx(ctx, func(tx *mailstore.Tx) error {
		ancestors := conversation.Ancestors(message.InReplyTo, message.References)

		conversationID, err := tx.ResolveConversation(ctx, ancestors, message.Subject, now)
		if err != nil {
			return err
		}
		message.ConversationID = conversationID

		if err := tx.InsertMessage(ctx, message); err != nil {
			return err
		}

		for index := range message.Attachments {
			message.Attachments[index].MessageID = id
			if err := tx.InsertAttachment(ctx, &message.Attachments[index]); err != nil {
				return err
			}
		}

		return tx.TouchConversation(ctx, conversationID, message.Subject, now)
	})
	if err != nil {
		return nil, err
	}

	return message, nil
}

// applyParsed copies the decoded headers and bodies onto the message.
func applyParsed(message *mailstore.Message, parsed *Parsed) {
	message.MessageID = parsed.MessageID
	message.InReplyTo = parsed.InReplyTo
	message.References = parsed.References
	message.From = parsed.From
	message.To = parsed.To
	message.Cc = parsed.Cc
	message.ReplyTo = parsed.ReplyTo
	message.Subject = parsed.Subject
	message.SentAt = parsed.Date
	message.TextBody = parsed.Text
	message.HTMLBody = parsed.HTML
}

// writeParts puts every attachment on disk under the message's own directory
// and returns the rows to insert. Files land before the transaction opens, so
// the transaction stays short and never holds the write lock during I/O.
func (i *Ingestor) writeParts(messageID string, parsed *Parsed) ([]mailstore.Attachment, error) {
	if parsed == nil || len(parsed.Parts) == 0 {
		return nil, nil
	}

	out := make([]mailstore.Attachment, 0, len(parsed.Parts))

	for index, part := range parsed.Parts {
		filename := safeFilename(part.Filename, index)
		rel := filepath.ToSlash(filepath.Join("attachments", messageID,
			fmt.Sprintf("%d-%s", index, filename)))

		if err := i.writeFile(rel, part.Content); err != nil {
			return nil, err
		}

		mediaType := part.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		out = append(out, mailstore.Attachment{
			MessageID: messageID,
			Filename:  filename,
			MediaType: mediaType,
			ContentID: part.ContentID,
			Inline:    part.Inline,
			SizeBytes: int64(len(part.Content)),
			Path:      rel,
		})
	}

	return out, nil
}

func (i *Ingestor) writeFile(rel string, content []byte) error {
	path := i.store.Path(rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", rel, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// announce publishes the arrival and the fresh unread count. A failure to
// count is logged rather than returned: the message is already stored, and
// refusing the SMTP transaction at this point would make the sender retry a
// message Mailman already has.
func (i *Ingestor) announce(ctx context.Context, message *mailstore.Message) {
	if i.broker == nil {
		return
	}

	unread, err := i.store.CountUnread(ctx)
	if err != nil {
		i.logger.Warn("could not count unread mail", "error", err)
	}

	i.broker.Publish(events.Event{
		Type:           events.MessageStored,
		ConversationID: message.ConversationID,
		MessageID:      message.ID,
		Inbound:        message.Direction == mailstore.DirectionInbound,
		Unread:         unread,
	})
}

// safeFilename keeps an attachment name usable as a path component. A message
// is untrusted input, and "../../etc/passwd" is a filename a sender can
// choose, so only the base name survives.
func safeFilename(name string, index int) string {
	name = filepath.Base(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, string(filepath.Separator), "-")

	switch name {
	case "", ".", "..", string(filepath.Separator):
		return fmt.Sprintf("attachment-%d", index)
	}
	return name
}
