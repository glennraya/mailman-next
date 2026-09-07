package mailstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNotFound is returned when an id names nothing. Callers map it to 404.
var ErrNotFound = errors.New("not found")

// ListOptions filters and pages the conversation list.
type ListOptions struct {
	// Query matches subjects, bodies and addresses. Empty means everything.
	Query string

	// Limit caps the page. Zero means DefaultListLimit.
	Limit int

	// Before pages backwards: only conversations less recently active than
	// this id are returned. Keyset paging rather than OFFSET, so a message
	// arriving mid-scroll cannot shift the page boundaries.
	Before int64
}

const (
	DefaultListLimit = 50
	MaxListLimit     = 200
)

// EffectiveLimit is the page size that will actually be used, after the
// default and the cap are applied. Callers need it to tell a full page from a
// short one, which is what decides whether there is a next page at all.
func (o ListOptions) EffectiveLimit() int {
	switch {
	case o.Limit <= 0:
		return DefaultListLimit
	case o.Limit > MaxListLimit:
		return MaxListLimit
	default:
		return o.Limit
	}
}

// ListConversations returns one page of the inbox, most recently active
// first, each row carrying enough for the list UI to render without a second
// query per conversation.
func (s *Store) ListConversations(ctx context.Context, opts ListOptions) ([]Conversation, error) {
	limit := opts.EffectiveLimit()

	where := []string{"1 = 1"}
	args := []any{}

	if query := strings.TrimSpace(opts.Query); query != "" {
		// LIKE with a wrapped term. Good enough at inbox scale; an FTS5
		// index is the upgrade path if this ever gets slow.
		term := "%" + escapeLike(query) + "%"
		where = append(where, `EXISTS (
			SELECT 1 FROM messages m
			 WHERE m.conversation_id = c.id
			   AND (m.subject   LIKE ? ESCAPE '\'
			     OR m.text_body LIKE ? ESCAPE '\'
			     OR m.from_addr LIKE ? ESCAPE '\'
			     OR m.to_addr   LIKE ? ESCAPE '\')
		)`)
		args = append(args, term, term, term, term)
	}

	if opts.Before > 0 {
		where = append(where, `c.last_activity_at < (
			SELECT last_activity_at FROM conversations WHERE id = ?
		)`)
		args = append(args, opts.Before)
	}

	args = append(args, limit)

	// The correlated subqueries all key off messages(conversation_id, id),
	// so this stays one index walk per conversation on the page.
	query := `
		SELECT c.id, c.subject, c.last_activity_at, c.created_at,
		       (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id),
		       (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id AND m.seen_at IS NULL),
		       (SELECT EXISTS (
		            SELECT 1 FROM attachments a
		              JOIN messages m ON m.id = a.message_id
		             WHERE m.conversation_id = c.id AND a.inline = 0)),
		       (SELECT m.from_addr FROM messages m WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1),
		       (SELECT m.text_body FROM messages m WHERE m.conversation_id = c.id ORDER BY m.id DESC LIMIT 1)
		  FROM conversations c
		 WHERE ` + strings.Join(where, " AND ") + `
		 ORDER BY c.last_activity_at DESC, c.id DESC
		 LIMIT ?`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	out := []Conversation{}
	for rows.Next() {
		var (
			c            Conversation
			lastActivity any
			createdAt    any
			fromAddr     any
			preview      any
		)

		if err := rows.Scan(&c.ID, &c.Subject, &lastActivity, &createdAt,
			&c.MessageCount, &c.UnreadCount, &c.HasAttach, &fromAddr, &preview); err != nil {
			return nil, fmt.Errorf("list conversations: %w", err)
		}

		if c.LastActivityAt, err = scanTime(lastActivity); err != nil {
			return nil, err
		}
		if c.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(fromAddr, &c.LastFrom); err != nil {
			return nil, fmt.Errorf("decode from address: %w", err)
		}
		c.Preview = summarize(scanString(preview), 140)

		out = append(out, c)
	}
	return out, rows.Err()
}

// GetConversation returns a thread with every message and attachment loaded,
// oldest first, which is the order it is read in.
func (s *Store) GetConversation(ctx context.Context, id int64) (*Conversation, []Message, error) {
	var (
		c            Conversation
		lastActivity any
		createdAt    any
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, subject, subject_key, last_activity_at, created_at
		   FROM conversations WHERE id = ?`, id).
		Scan(&c.ID, &c.Subject, &c.SubjectKey, &lastActivity, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read conversation: %w", err)
	}

	if c.LastActivityAt, err = scanTime(lastActivity); err != nil {
		return nil, nil, err
	}
	if c.CreatedAt, err = scanTime(createdAt); err != nil {
		return nil, nil, err
	}

	messages, err := s.messagesWhere(ctx, `conversation_id = ? ORDER BY id ASC`, id)
	if err != nil {
		return nil, nil, err
	}

	c.MessageCount = len(messages)
	for _, m := range messages {
		if !m.Seen() {
			c.UnreadCount++
		}
	}

	return &c, messages, nil
}

// GetMessage returns one message with its attachments.
func (s *Store) GetMessage(ctx context.Context, id string) (*Message, error) {
	messages, err := s.messagesWhere(ctx, `id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, ErrNotFound
	}
	return &messages[0], nil
}

// GetAttachment returns one attachment row, including its on-disk path.
func (s *Store) GetAttachment(ctx context.Context, id int64) (*Attachment, error) {
	var a Attachment
	err := s.db.QueryRowContext(ctx,
		`SELECT id, message_id, filename, content_type, content_id, inline, size_bytes, path
		   FROM attachments WHERE id = ?`, id).
		Scan(&a.ID, &a.MessageID, &a.Filename, &a.MediaType, &a.ContentID,
			&a.Inline, &a.SizeBytes, &a.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read attachment: %w", err)
	}
	return &a, nil
}

// FindInlineAttachment resolves a cid: reference within one message, which is
// how an HTML body's embedded images are served.
func (s *Store) FindInlineAttachment(ctx context.Context, messageID, contentID string) (*Attachment, error) {
	var a Attachment
	err := s.db.QueryRowContext(ctx,
		`SELECT id, message_id, filename, content_type, content_id, inline, size_bytes, path
		   FROM attachments WHERE message_id = ? AND content_id = ? LIMIT 1`,
		messageID, contentID).
		Scan(&a.ID, &a.MessageID, &a.Filename, &a.MediaType, &a.ContentID,
			&a.Inline, &a.SizeBytes, &a.Path)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve inline attachment: %w", err)
	}
	return &a, nil
}

// CountUnread reports the inbox badge number.
func (s *Store) CountUnread(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM messages WHERE seen_at IS NULL`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return count, nil
}

// ListDeliveries returns every webhook attempt for a message, newest first.
func (s *Store) ListDeliveries(ctx context.Context, messageID string) ([]Delivery, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, message_id, target_url, format, attempt, status_code, error, duration_ms, created_at
		   FROM deliveries WHERE message_id = ? ORDER BY id DESC`, messageID)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()

	out := []Delivery{}
	for rows.Next() {
		var (
			d         Delivery
			createdAt any
		)
		if err := rows.Scan(&d.ID, &d.MessageID, &d.TargetURL, &d.Format, &d.Attempt,
			&d.StatusCode, &d.Error, &d.DurationMS, &createdAt); err != nil {
			return nil, fmt.Errorf("list deliveries: %w", err)
		}
		if d.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetDelivery returns one attempt, which a retry uses to find its target.
func (s *Store) GetDelivery(ctx context.Context, id int64) (*Delivery, error) {
	var (
		d         Delivery
		createdAt any
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, message_id, target_url, format, attempt, status_code, error, duration_ms, created_at
		   FROM deliveries WHERE id = ?`, id).
		Scan(&d.ID, &d.MessageID, &d.TargetURL, &d.Format, &d.Attempt,
			&d.StatusCode, &d.Error, &d.DurationMS, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read delivery: %w", err)
	}
	if d.CreatedAt, err = scanTime(createdAt); err != nil {
		return nil, err
	}
	return &d, nil
}

// messagesWhere runs one message query, then hydrates the attachments for
// the whole result in a second query -- two round trips for a thread rather
// than one per message.
//
// The first result set is closed before the second query opens. With a
// single-connection pool, querying while an *sql.Rows is still open would
// wait for a connection that this function itself is holding.
func (s *Store) messagesWhere(ctx context.Context, clause string, args ...any) ([]Message, error) {
	out, index, err := s.scanMessages(ctx, clause, args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}

	if err := s.attachMessageFiles(ctx, out, index); err != nil {
		return nil, err
	}
	return out, nil
}

// scanMessages reads the message rows and closes the result set before
// returning, so the caller is free to query again.
func (s *Store) scanMessages(ctx context.Context, clause string, args ...any) ([]Message, map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, conversation_id, direction, message_id, in_reply_to, refs,
		        from_addr, to_addr, cc_addr, bcc_addr, reply_to_addr,
		        envelope_from, envelope_rcpt, subject, sent_at,
		        text_body, html_body, size_bytes, seen_at, raw_path, created_at
		   FROM messages WHERE `+clause, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("read messages: %w", err)
	}
	defer rows.Close()

	var (
		out   []Message
		index = map[string]int{}
	)

	for rows.Next() {
		var (
			m                                      Message
			refs, from, to, cc, bcc, replyTo, rcpt any
			sentAt, seenAt, createdAt              any
		)

		if err := rows.Scan(&m.ID, &m.ConversationID, &m.Direction, &m.MessageID, &m.InReplyTo, &refs,
			&from, &to, &cc, &bcc, &replyTo,
			&m.Envelope.From, &rcpt, &m.Subject, &sentAt,
			&m.TextBody, &m.HTMLBody, &m.SizeBytes, &seenAt, &m.RawPath, &createdAt); err != nil {
			return nil, nil, fmt.Errorf("read messages: %w", err)
		}

		for _, column := range []struct {
			src  any
			dst  any
			what string
		}{
			{refs, &m.References, "references"},
			{from, &m.From, "from"},
			{to, &m.To, "to"},
			{cc, &m.Cc, "cc"},
			{bcc, &m.Bcc, "bcc"},
			{replyTo, &m.ReplyTo, "reply-to"},
			{rcpt, &m.Envelope.Recipients, "envelope recipients"},
		} {
			if err := decodeJSON(column.src, column.dst); err != nil {
				return nil, nil, fmt.Errorf("decode %s for message %s: %w", column.what, m.ID, err)
			}
		}

		if m.SentAt, err = scanTime(sentAt); err != nil {
			return nil, nil, err
		}
		if m.SeenAt, err = scanTime(seenAt); err != nil {
			return nil, nil, err
		}
		if m.CreatedAt, err = scanTime(createdAt); err != nil {
			return nil, nil, err
		}

		index[m.ID] = len(out)
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read messages: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, fmt.Errorf("read messages: %w", err)
	}

	return out, index, nil
}

// attachMessageFiles loads every attachment for a page of messages at once.
func (s *Store) attachMessageFiles(ctx context.Context, messages []Message, index map[string]int) error {
	ids := make([]any, 0, len(messages))
	for _, m := range messages {
		ids = append(ids, m.ID)
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, message_id, filename, content_type, content_id, inline, size_bytes, path
		   FROM attachments WHERE message_id IN (`+placeholders(len(ids))+`) ORDER BY id ASC`, ids...)
	if err != nil {
		return fmt.Errorf("read attachments: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.MessageID, &a.Filename, &a.MediaType,
			&a.ContentID, &a.Inline, &a.SizeBytes, &a.Path); err != nil {
			return fmt.Errorf("read attachments: %w", err)
		}
		if i, ok := index[a.MessageID]; ok {
			messages[i].Attachments = append(messages[i].Attachments, a)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read attachments: %w", err)
	}
	return nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// escapeLike neutralizes the wildcards so a search for "50% off" does not
// match everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// summarize squeezes a body down to a one-line preview.
func summarize(body string, max int) string {
	preview := strings.Join(strings.Fields(body), " ")
	if len(preview) <= max {
		return preview
	}
	return strings.TrimSpace(preview[:max]) + "…"
}
