package mailstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glennraya/mailman/internal/conversation"
)

// Tx is a write transaction. Ingesting a message touches three tables and the
// filesystem, and a half-written message is worse than a dropped one, so the
// whole thing commits together.
type Tx struct {
	tx *sql.Tx
}

// InTx runs fn inside a transaction, committing when it returns nil and
// rolling back otherwise. A panic rolls back before it continues unwinding.
func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()

	if err := fn(&Tx{tx: tx}); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ResolveConversation finds the conversation a message belongs to, creating
// one if nothing matches.
//
// Threading headers win: if the message names an ancestor Mailman has stored,
// that ancestor's conversation is the answer. Subject matching is the
// fallback and only applies when the subject carries a reply marker --
// without that guard, every "Your receipt" from an application would collapse
// into a single thread.
func (t *Tx) ResolveConversation(ctx context.Context, ancestors []string, subject string, at time.Time) (int64, error) {
	// Walk backwards: Ancestors puts the immediate parent last, and the
	// closest known ancestor is the most likely conversation.
	for i := len(ancestors) - 1; i >= 0; i-- {
		id, err := t.conversationByMessageID(ctx, ancestors[i])
		if err != nil {
			return 0, err
		}
		if id != 0 {
			return id, nil
		}
	}

	if conversation.HasReplyPrefix(subject) {
		if key, ok := conversation.Key(subject); ok {
			id, err := t.conversationBySubject(ctx, key)
			if err != nil {
				return 0, err
			}
			if id != 0 {
				return id, nil
			}
		}
	}

	return t.createConversation(ctx, subject, at)
}

func (t *Tx) conversationByMessageID(ctx context.Context, messageID string) (int64, error) {
	if messageID == "" {
		return 0, nil
	}

	var id int64
	err := t.tx.QueryRowContext(ctx,
		`SELECT conversation_id FROM messages WHERE message_id = ? ORDER BY id DESC LIMIT 1`,
		messageID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("look up conversation by message id: %w", err)
	}
	return id, nil
}

func (t *Tx) conversationBySubject(ctx context.Context, key string) (int64, error) {
	var id int64
	err := t.tx.QueryRowContext(ctx,
		`SELECT id FROM conversations WHERE subject_key = ? ORDER BY last_activity_at DESC LIMIT 1`,
		key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("look up conversation by subject: %w", err)
	}
	return id, nil
}

func (t *Tx) createConversation(ctx context.Context, subject string, at time.Time) (int64, error) {
	key, _ := conversation.Key(subject)

	result, err := t.tx.ExecContext(ctx,
		`INSERT INTO conversations (subject, subject_key, last_activity_at, created_at)
		 VALUES (?, ?, ?, ?)`,
		conversation.StripReplyPrefix(subject), key, formatTime(at), formatTime(at))
	if err != nil {
		return 0, fmt.Errorf("create conversation: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create conversation: %w", err)
	}
	return id, nil
}

// TouchConversation moves a conversation to the top of the inbox. The subject
// is only filled in when the conversation has none, so a thread keeps the
// wording it started with instead of adopting a "Re:" from a later reply.
func (t *Tx) TouchConversation(ctx context.Context, id int64, subject string, at time.Time) error {
	stripped := conversation.StripReplyPrefix(subject)
	key, _ := conversation.Key(subject)

	_, err := t.tx.ExecContext(ctx,
		`UPDATE conversations
		    SET last_activity_at = ?,
		        subject     = CASE WHEN subject     = '' THEN ? ELSE subject     END,
		        subject_key = CASE WHEN subject_key = '' THEN ? ELSE subject_key END
		  WHERE id = ?`,
		formatTime(at), stripped, key, id)
	if err != nil {
		return fmt.Errorf("touch conversation: %w", err)
	}
	return nil
}

// InsertMessage writes one message row. The caller supplies the ID so the
// same value can name the raw file on disk before the transaction opens.
func (t *Tx) InsertMessage(ctx context.Context, m *Message) error {
	refs, err := encodeJSON(m.References)
	if err != nil {
		return fmt.Errorf("encode references: %w", err)
	}

	from, err := encodeJSON(m.From)
	if err != nil {
		return fmt.Errorf("encode from: %w", err)
	}
	to, err := encodeJSON(m.To)
	if err != nil {
		return fmt.Errorf("encode to: %w", err)
	}
	cc, err := encodeJSON(m.Cc)
	if err != nil {
		return fmt.Errorf("encode cc: %w", err)
	}
	bcc, err := encodeJSON(m.Bcc)
	if err != nil {
		return fmt.Errorf("encode bcc: %w", err)
	}
	replyTo, err := encodeJSON(m.ReplyTo)
	if err != nil {
		return fmt.Errorf("encode reply-to: %w", err)
	}
	rcpt, err := encodeJSON(m.Envelope.Recipients)
	if err != nil {
		return fmt.Errorf("encode envelope recipients: %w", err)
	}

	_, err = t.tx.ExecContext(ctx,
		`INSERT INTO messages (
			id, conversation_id, direction, message_id, in_reply_to, refs,
			from_addr, to_addr, cc_addr, bcc_addr, reply_to_addr,
			envelope_from, envelope_rcpt, subject, sent_at,
			text_body, html_body, size_bytes, seen_at, raw_path, created_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ConversationID, m.Direction, m.MessageID, m.InReplyTo, refs,
		from, to, cc, bcc, replyTo,
		m.Envelope.From, rcpt, m.Subject, nullTime(m.SentAt),
		m.TextBody, m.HTMLBody, m.SizeBytes, nullTime(m.SeenAt),
		m.RawPath, formatTime(m.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	return nil
}

// InsertAttachment writes one attachment row and fills in its ID.
func (t *Tx) InsertAttachment(ctx context.Context, a *Attachment) error {
	result, err := t.tx.ExecContext(ctx,
		`INSERT INTO attachments
		   (message_id, filename, content_type, content_id, inline, size_bytes, path)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.MessageID, a.Filename, a.MediaType, a.ContentID, a.Inline, a.SizeBytes, a.Path)
	if err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}

	if a.ID, err = result.LastInsertId(); err != nil {
		return fmt.Errorf("insert attachment: %w", err)
	}
	return nil
}

// MarkSeen records that a message has been read. It reports whether the row
// changed, so the caller can skip broadcasting an unread count that did not
// actually move.
func (s *Store) MarkSeen(ctx context.Context, messageID string) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE messages SET seen_at = ? WHERE id = ? AND seen_at IS NULL`,
		formatTime(Now()), messageID)
	if err != nil {
		return false, fmt.Errorf("mark message seen: %w", err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark message seen: %w", err)
	}
	return changed > 0, nil
}

// MarkConversationSeen marks every message in a conversation read.
func (s *Store) MarkConversationSeen(ctx context.Context, id int64) (bool, error) {
	result, err := s.db.ExecContext(ctx,
		`UPDATE messages SET seen_at = ? WHERE conversation_id = ? AND seen_at IS NULL`,
		formatTime(Now()), id)
	if err != nil {
		return false, fmt.Errorf("mark conversation seen: %w", err)
	}

	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("mark conversation seen: %w", err)
	}
	return changed > 0, nil
}

// RecordDelivery appends one webhook attempt and fills in its ID.
func (s *Store) RecordDelivery(ctx context.Context, d *Delivery) error {
	if d.CreatedAt.IsZero() {
		d.CreatedAt = Now()
	}

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO deliveries
		   (message_id, target_url, format, attempt, status_code, error, duration_ms, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		d.MessageID, d.TargetURL, d.Format, d.Attempt,
		d.StatusCode, d.Error, d.DurationMS, formatTime(d.CreatedAt))
	if err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}

	if d.ID, err = result.LastInsertId(); err != nil {
		return fmt.Errorf("record delivery: %w", err)
	}
	return nil
}

// NextAttempt reports which attempt number a retry of this message would be.
func (s *Store) NextAttempt(ctx context.Context, messageID string) (int, error) {
	var highest sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(attempt) FROM deliveries WHERE message_id = ?`, messageID).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("read delivery attempts: %w", err)
	}
	return int(highest.Int64) + 1, nil
}

// DeleteMessage removes one message and everything it owns, on disk as well
// as in the database. A conversation left with no messages goes too, and the
// second return value reports whether that happened so the UI knows to move
// the selection somewhere else.
func (s *Store) DeleteMessage(ctx context.Context, id string) (conversationID int64, conversationGone bool, err error) {
	paths, err := s.messagePaths(ctx, id)
	if err != nil {
		return 0, false, err
	}

	err = s.db.QueryRowContext(ctx,
		`SELECT conversation_id FROM messages WHERE id = ?`, id).Scan(&conversationID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrNotFound
	}
	if err != nil {
		return 0, false, fmt.Errorf("delete message: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
		return 0, false, fmt.Errorf("delete message: %w", err)
	}

	var remaining int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE conversation_id = ?`, conversationID).Scan(&remaining); err != nil {
		return 0, false, fmt.Errorf("delete message: %w", err)
	}

	if remaining == 0 {
		if _, err := s.db.ExecContext(ctx,
			`DELETE FROM conversations WHERE id = ?`, conversationID); err != nil {
			return 0, false, fmt.Errorf("delete empty conversation: %w", err)
		}
		conversationGone = true
	}

	s.removeFiles(paths)
	s.removeAttachmentDir(id)
	return conversationID, conversationGone, nil
}

// DeleteConversation removes a whole thread and its files.
func (s *Store) DeleteConversation(ctx context.Context, id int64) error {
	ids, err := s.messageIDs(ctx, id)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return ErrNotFound
	}

	var paths []string
	for _, messageID := range ids {
		p, err := s.messagePaths(ctx, messageID)
		if err != nil {
			return err
		}
		paths = append(paths, p...)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM conversations WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete conversation: %w", err)
	}

	s.removeFiles(paths)
	for _, messageID := range ids {
		s.removeAttachmentDir(messageID)
	}
	return nil
}

// Clear empties the mailbox. It reports how many messages went, which is what
// a CI reset wants to assert on.
func (s *Store) Clear(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&count); err != nil {
		return 0, fmt.Errorf("clear mailbox: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM conversations`); err != nil {
		return 0, fmt.Errorf("clear mailbox: %w", err)
	}
	// Messages hang off conversations, but a message whose conversation was
	// already gone would survive the cascade, so sweep explicitly.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages`); err != nil {
		return 0, fmt.Errorf("clear mailbox: %w", err)
	}

	// The mail directories are rebuilt rather than walked: it is one
	// operation instead of thousands, and nothing else writes there.
	for _, dir := range []string{s.RawDir(), s.AttachmentDir()} {
		if err := os.RemoveAll(dir); err != nil {
			return count, fmt.Errorf("clear %s: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return count, fmt.Errorf("recreate %s: %w", dir, err)
		}
	}

	return count, nil
}

func (s *Store) messageIDs(ctx context.Context, conversationID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM messages WHERE conversation_id = ?`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list conversation messages: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// messagePaths collects every file a message owns, before its rows go away.
func (s *Store) messagePaths(ctx context.Context, messageID string) ([]string, error) {
	var paths []string

	var raw string
	err := s.db.QueryRowContext(ctx,
		`SELECT raw_path FROM messages WHERE id = ?`, messageID).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read message path: %w", err)
	}
	if raw != "" {
		paths = append(paths, raw)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT path FROM attachments WHERE message_id = ?`, messageID)
	if err != nil {
		return nil, fmt.Errorf("read attachment paths: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("read attachment paths: %w", err)
		}
		paths = append(paths, path)
	}
	return paths, rows.Err()
}

// removeFiles deletes payloads best-effort. The rows are already gone, so a
// leftover file is wasted disk rather than a correctness problem, and failing
// the request over it would be worse.
func (s *Store) removeFiles(paths []string) {
	for _, rel := range paths {
		if rel == "" {
			continue
		}
		os.Remove(s.Path(rel))
	}
}

func (s *Store) removeAttachmentDir(messageID string) {
	if messageID == "" {
		return
	}
	os.Remove(filepath.Join(s.AttachmentDir(), messageID))
}
