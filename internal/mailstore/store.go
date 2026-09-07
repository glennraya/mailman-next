// Package mailstore owns every read and write against Mailman's SQLite
// database, plus the on-disk layout for raw messages and attachments.
//
// Payloads are files rather than blobs. A captured message stays openable in
// any mail client, the database stays small enough to copy around, and a
// 20 MB attachment never has to travel through a SQL driver.
package mailstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Direction distinguishes mail Mailman captured from mail Mailman sent.
// Outbound messages are the replies -- they share the pipeline and the
// conversation with what they answer, so a thread reads as a conversation.
const (
	DirectionInbound  = "inbound"
	DirectionOutbound = "outbound"
)

// timeLayout is what every timestamp column holds. SQLite has no date type,
// and a fixed-width UTC string sorts correctly as text, which keeps ordering
// working without a conversion in every query.
const timeLayout = "2006-01-02 15:04:05"

// Store is the handle on the database and the mail directory.
type Store struct {
	db      *sql.DB
	mailDir string
}

// Address is one parsed mail address. Name is empty for a bare address.
type Address struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address"`
}

// Envelope is what the SMTP conversation itself carried, as opposed to what
// the headers claim. The difference is how Bcc is recovered: a recipient the
// server was handed but who appears in no header was blind-copied.
type Envelope struct {
	From       string   `json:"from,omitempty"`
	Recipients []string `json:"recipients,omitempty"`
}

// Conversation groups messages that belong to one exchange.
type Conversation struct {
	ID             int64     `json:"id"`
	Subject        string    `json:"subject"`
	SubjectKey     string    `json:"-"`
	LastActivityAt time.Time `json:"last_activity_at"`
	CreatedAt      time.Time `json:"created_at"`

	// Populated by list and detail queries, not stored as columns.
	MessageCount int       `json:"message_count,omitempty"`
	UnreadCount  int       `json:"unread_count,omitempty"`
	HasAttach    bool      `json:"has_attachments,omitempty"`
	Preview      string    `json:"preview,omitempty"`
	LastFrom     []Address `json:"last_from,omitempty"`
}

// Message is one mail, captured or sent.
type Message struct {
	ID             string `json:"id"`
	ConversationID int64  `json:"conversation_id"`
	Direction      string `json:"direction"`

	MessageID  string   `json:"message_id,omitempty"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`

	From    []Address `json:"from,omitempty"`
	To      []Address `json:"to,omitempty"`
	Cc      []Address `json:"cc,omitempty"`
	Bcc     []Address `json:"bcc,omitempty"`
	ReplyTo []Address `json:"reply_to,omitempty"`

	Envelope Envelope `json:"envelope"`

	Subject   string    `json:"subject"`
	SentAt    time.Time `json:"sent_at"`
	TextBody  string    `json:"text_body,omitempty"`
	HTMLBody  string    `json:"html_body,omitempty"`
	SizeBytes int64     `json:"size_bytes"`

	// SeenAt is zero while the message is unread.
	SeenAt    time.Time `json:"seen_at,omitzero"`
	RawPath   string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`

	Attachments []Attachment `json:"attachments,omitempty"`
}

// Seen reports whether the message has been opened.
func (m Message) Seen() bool { return !m.SeenAt.IsZero() }

// Attachment is one part held on disk.
type Attachment struct {
	ID        int64  `json:"id"`
	MessageID string `json:"message_id"`
	Filename  string `json:"filename"`
	MediaType string `json:"content_type"`

	// ContentID is set for parts an HTML body refers to with cid:.
	ContentID string `json:"content_id,omitempty"`
	Inline    bool   `json:"inline"`
	SizeBytes int64  `json:"size_bytes"`
	Path      string `json:"-"`
}

// Delivery records one attempt to hand a reply to the app under test. Every
// attempt is a row, including the failures -- when a reply does not arrive,
// the reason has to be visible somewhere.
type Delivery struct {
	ID         int64     `json:"id"`
	MessageID  string    `json:"message_id"`
	TargetURL  string    `json:"target_url"`
	Format     string    `json:"format"`
	Attempt    int       `json:"attempt"`
	StatusCode int       `json:"status_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	CreatedAt  time.Time `json:"created_at"`
}

// OK reports whether the app under test accepted the delivery.
func (d Delivery) OK() bool {
	return d.Error == "" && d.StatusCode >= 200 && d.StatusCode < 300
}

// Open connects to the database, brings the schema up to date, and makes sure
// the mail directories exist.
func Open(path, mailDir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	// WAL lets the HTTP reads run while SMTP is writing. busy_timeout covers
	// the moment a writer holds the lock; foreign_keys is off by default in
	// SQLite and the cascades depend on it; synchronous(NORMAL) is the
	// standard WAL companion, since a full fsync per commit buys durability
	// guarantees a development mailbox does not need.
	//
	// _txlock=immediate takes the write lock at BEGIN instead of at the
	// first write. A deferred transaction that reads and then writes can be
	// refused outright on the upgrade, and busy_timeout does not retry a
	// lock upgrade -- so this is the difference between waiting and failing
	// the moment anything else has the file open.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"+
			"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate",
		path,
	)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// One connection. SQLite serializes writes anyway, and a single
	// connection keeps the pragmas above from having to be re-applied per
	// connection -- a classic source of "why is foreign_keys off again".
	//
	// The cost is that this package must never hold an open *sql.Rows while
	// issuing another query: the second call would wait for the connection
	// the first is holding, and wait forever. Every query here is drained
	// and closed before the next one starts.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	s := &Store{db: db, mailDir: mailDir}

	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}

	for _, dir := range []string{s.RawDir(), s.AttachmentDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			db.Close()
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// MailDir is the root of the on-disk payloads.
func (s *Store) MailDir() string { return s.mailDir }

// RawDir holds the verbatim .eml of every message.
func (s *Store) RawDir() string { return filepath.Join(s.mailDir, "raw") }

// AttachmentDir holds one subdirectory per message.
func (s *Store) AttachmentDir() string { return filepath.Join(s.mailDir, "attachments") }

// Path turns a stored relative path into an absolute one. Paths are stored
// relative so the whole directory can be moved or copied between machines.
func (s *Store) Path(rel string) string { return filepath.Join(s.mailDir, rel) }

// Now is the timestamp every write uses: UTC, whole seconds, matching the
// precision the columns can hold.
func Now() time.Time { return time.Now().UTC().Truncate(time.Second) }

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// nullTime renders a zero time as SQL NULL, which is how "unread" and "never
// happened" are stored.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func scanTime(v any) (time.Time, error) {
	switch value := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return value.UTC(), nil
	case string:
		if value == "" {
			return time.Time{}, nil
		}
		t, err := time.Parse(timeLayout, value)
		if err != nil {
			// Tolerate a stricter RFC 3339 value, which is what an
			// external tool writing to the file would most likely use.
			if alt, altErr := time.Parse(time.RFC3339, value); altErr == nil {
				return alt.UTC(), nil
			}
			return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
		}
		return t.UTC(), nil
	case []byte:
		return scanTime(string(value))
	default:
		return time.Time{}, fmt.Errorf("unexpected timestamp type %T", v)
	}
}

// encodeJSON stores a slice or struct in a TEXT column, using NULL for empty
// so the database does not fill up with "[]".
func encodeJSON(v any) (any, error) {
	switch value := v.(type) {
	case []Address:
		if len(value) == 0 {
			return nil, nil
		}
	case []string:
		if len(value) == 0 {
			return nil, nil
		}
	}

	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func decodeJSON(v any, dst any) error {
	var raw []byte
	switch value := v.(type) {
	case nil:
		return nil
	case string:
		raw = []byte(value)
	case []byte:
		raw = value
	default:
		return fmt.Errorf("unexpected JSON column type %T", v)
	}

	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, dst)
}

func scanString(v any) string {
	switch value := v.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}
