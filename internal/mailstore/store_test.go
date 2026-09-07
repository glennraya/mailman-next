package mailstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()

	home := t.TempDir()
	store, err := Open(filepath.Join(home, "mailman.db"), filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

// insert writes a message plus one attachment, with both payloads on disk, so
// the deletion tests have real files to account for.
func insert(t *testing.T, s *Store, id, messageID, inReplyTo, subject string) *Message {
	t.Helper()

	ctx := context.Background()
	now := Now()

	rawPath := filepath.ToSlash(filepath.Join("raw", id+".eml"))
	attachmentPath := filepath.ToSlash(filepath.Join("attachments", id, "0-notes.txt"))

	for path, content := range map[string]string{
		rawPath:        "raw source of " + id,
		attachmentPath: "attachment of " + id,
	} {
		full := s.Path(path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("create directory: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	message := &Message{
		ID:        id,
		Direction: DirectionInbound,
		MessageID: messageID,
		InReplyTo: inReplyTo,
		From:      []Address{{Name: "Acme", Address: "billing@acme.test"}},
		To:        []Address{{Address: "glenn@myapp.test"}},
		Envelope:  Envelope{From: "billing@acme.test", Recipients: []string{"glenn@myapp.test"}},
		Subject:   subject,
		TextBody:  "body of " + subject,
		SizeBytes: 100,
		RawPath:   rawPath,
		CreatedAt: now,
	}

	err := s.InTx(ctx, func(tx *Tx) error {
		var ancestors []string
		if inReplyTo != "" {
			ancestors = []string{inReplyTo}
		}

		conversationID, err := tx.ResolveConversation(ctx, ancestors, subject, now)
		if err != nil {
			return err
		}
		message.ConversationID = conversationID

		if err := tx.InsertMessage(ctx, message); err != nil {
			return err
		}

		attachment := Attachment{
			MessageID: id,
			Filename:  "notes.txt",
			MediaType: "text/plain",
			SizeBytes: 18,
			Path:      attachmentPath,
		}
		if err := tx.InsertAttachment(ctx, &attachment); err != nil {
			return err
		}
		message.Attachments = []Attachment{attachment}

		return tx.TouchConversation(ctx, conversationID, subject, now)
	})
	if err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}

	return message
}

func TestOpenIsIdempotent(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "mailman.db")
	mail := filepath.Join(home, "mail")

	for attempt := range 3 {
		store, err := Open(path, mail)
		if err != nil {
			t.Fatalf("open %d: %v", attempt, err)
		}

		version, err := store.currentVersion(context.Background())
		if err != nil {
			t.Fatalf("read version: %v", err)
		}
		if version != schemaVersion {
			t.Errorf("schema version = %d, want %d", version, schemaVersion)
		}
		store.Close()
	}

	for _, dir := range []string{"raw", "attachments"} {
		if _, err := os.Stat(filepath.Join(mail, dir)); err != nil {
			t.Errorf("%s directory missing: %v", dir, err)
		}
	}
}

// TestFutureSchemaIsRefused stops a downgraded binary from writing into a
// database it does not understand. Losing mail here would be silent.
func TestFutureSchemaIsRefused(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "mailman.db")

	store, err := Open(path, filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE schema_version SET version = ?`, schemaVersion+5); err != nil {
		t.Fatalf("bump version: %v", err)
	}
	store.Close()

	if _, err := Open(path, filepath.Join(home, "mail")); err == nil {
		t.Fatal("a database from a newer build was opened anyway")
	}
}

func TestRoundTripPreservesEverything(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	written := insert(t, store, "m1", "a@acme.test", "", "Order shipped")

	got, err := store.GetMessage(ctx, "m1")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if got.Subject != written.Subject {
		t.Errorf("subject = %q", got.Subject)
	}
	if len(got.From) != 1 || got.From[0].Name != "Acme" {
		t.Errorf("from = %+v, want the display name preserved", got.From)
	}
	if got.Envelope.From != "billing@acme.test" || len(got.Envelope.Recipients) != 1 {
		t.Errorf("envelope = %+v", got.Envelope)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Filename != "notes.txt" {
		t.Errorf("attachments = %+v", got.Attachments)
	}
	if got.Seen() {
		t.Error("a captured message came back already read")
	}
	// Timestamps go through a fixed-width text column; they have to survive
	// as the same instant.
	if !got.CreatedAt.Equal(written.CreatedAt.Truncate(time.Second)) {
		t.Errorf("created at = %v, want %v", got.CreatedAt, written.CreatedAt)
	}
}

func TestDeleteMessageRemovesItsFiles(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	first := insert(t, store, "m1", "a@acme.test", "", "Invoice")
	insert(t, store, "m2", "b@acme.test", "a@acme.test", "Re: Invoice")

	rawPath := store.Path(first.RawPath)
	attachmentPath := store.Path(first.Attachments[0].Path)

	conversationID, gone, err := store.DeleteMessage(ctx, "m1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if conversationID != first.ConversationID {
		t.Errorf("reported conversation %d, want %d", conversationID, first.ConversationID)
	}
	// The reply is still there, so the conversation must survive.
	if gone {
		t.Error("the conversation was reported deleted while a message remained")
	}

	if _, err := store.GetMessage(ctx, "m1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted message still readable: %v", err)
	}

	// Files have to go too, or the mail directory grows without bound while
	// the database says the message is gone.
	for _, path := range []string{rawPath, attachmentPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the delete", path)
		}
	}
}

func TestDeletingTheLastMessageRemovesTheConversation(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	message := insert(t, store, "m1", "a@acme.test", "", "Only one")

	_, gone, err := store.DeleteMessage(ctx, "m1")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !gone {
		t.Fatal("an emptied conversation was left behind")
	}

	if _, _, err := store.GetConversation(ctx, message.ConversationID); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty conversation still readable: %v", err)
	}
}

func TestDeleteConversationTakesEveryMessage(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	first := insert(t, store, "m1", "a@acme.test", "", "Invoice")
	insert(t, store, "m2", "b@acme.test", "a@acme.test", "Re: Invoice")

	if err := store.DeleteConversation(ctx, first.ConversationID); err != nil {
		t.Fatalf("delete conversation: %v", err)
	}

	for _, id := range []string{"m1", "m2"} {
		if _, err := store.GetMessage(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("message %s survived: %v", id, err)
		}
	}

	entries, err := os.ReadDir(store.RawDir())
	if err != nil {
		t.Fatalf("read raw directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d raw files survived the delete", len(entries))
	}
}

func TestClearEmptiesTheDatabaseAndTheDisk(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	insert(t, store, "m1", "a@acme.test", "", "One")
	insert(t, store, "m2", "b@acme.test", "", "Two")

	deleted, err := store.Clear(ctx)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if deleted != 2 {
		t.Errorf("cleared %d messages, want 2", deleted)
	}

	conversations, err := store.ListConversations(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(conversations) != 0 {
		t.Errorf("%d conversations survived a clear", len(conversations))
	}

	// The directories must still exist, or the next capture fails.
	for _, dir := range []string{store.RawDir(), store.AttachmentDir()} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		if len(entries) != 0 {
			t.Errorf("%s still holds %d entries", dir, len(entries))
		}
	}
}

func TestSearchEscapesLikeWildcards(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	insert(t, store, "m1", "a@acme.test", "", "50% off everything")
	insert(t, store, "m2", "b@acme.test", "", "Order shipped")

	// A bare % is a LIKE wildcard. Without escaping this matches every row,
	// which makes search look broken in the least obvious way.
	got, err := store.ListConversations(ctx, ListOptions{Query: "%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("searching for a literal %% returned %d conversations, want 1", len(got))
	}

	got, err = store.ListConversations(ctx, ListOptions{Query: "50%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("searching for \"50%%\" returned %d conversations, want 1", len(got))
	}
}

func TestPagingIsStableAcrossPages(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	for i, subject := range []string{"One", "Two", "Three", "Four"} {
		id := string(rune('a' + i))
		message := insert(t, store, id, id+"@acme.test", "", subject)

		// Timestamps hold whole seconds, so four messages inserted in the
		// same second would order arbitrarily. Space them out directly
		// rather than sleeping through four seconds of test time.
		when := formatTime(Now().Add(time.Duration(i) * time.Minute))
		if _, err := store.db.ExecContext(ctx,
			`UPDATE conversations SET last_activity_at = ? WHERE id = ?`,
			when, message.ConversationID); err != nil {
			t.Fatalf("set activity time: %v", err)
		}
	}

	first, err := store.ListConversations(ctx, ListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first page held %d rows, want 2", len(first))
	}

	second, err := store.ListConversations(ctx, ListOptions{Limit: 2, Before: first[1].ID})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second page held %d rows, want 2", len(second))
	}

	// No row may appear on both pages.
	for _, a := range first {
		for _, b := range second {
			if a.ID == b.ID {
				t.Errorf("conversation %d appeared on both pages", a.ID)
			}
		}
	}
}

func TestMarkSeenReportsOnlyRealChanges(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	insert(t, store, "m1", "a@acme.test", "", "Unread")

	unread, err := store.CountUnread(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if unread != 1 {
		t.Fatalf("unread = %d, want 1", unread)
	}

	changed, err := store.MarkSeen(ctx, "m1")
	if err != nil {
		t.Fatalf("mark seen: %v", err)
	}
	if !changed {
		t.Error("marking an unread message reported no change")
	}

	// Re-reading an already-read message must not announce anything, or
	// every open tab redraws its badge for nothing.
	changed, err = store.MarkSeen(ctx, "m1")
	if err != nil {
		t.Fatalf("mark seen again: %v", err)
	}
	if changed {
		t.Error("re-reading a read message reported a change")
	}

	if unread, _ = store.CountUnread(ctx); unread != 0 {
		t.Errorf("unread = %d after reading, want 0", unread)
	}
}

func TestConversationListCarriesWhatTheListNeeds(t *testing.T) {
	store := open(t)
	ctx := context.Background()

	insert(t, store, "m1", "a@acme.test", "", "Invoice")
	insert(t, store, "m2", "b@acme.test", "a@acme.test", "Re: Invoice")

	got, err := store.ListConversations(ctx, ListOptions{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d conversations, want 1", len(got))
	}

	row := got[0]
	if row.MessageCount != 2 {
		t.Errorf("message count = %d, want 2", row.MessageCount)
	}
	if row.UnreadCount != 2 {
		t.Errorf("unread count = %d, want 2", row.UnreadCount)
	}
	if !row.HasAttach {
		t.Error("has_attachments was false with attachments present")
	}
	if row.Preview == "" {
		t.Error("no preview text")
	}
	if len(row.LastFrom) != 1 {
		t.Errorf("last_from = %+v", row.LastFrom)
	}
	// The thread keeps the subject it started with rather than adopting the
	// reply's "Re:".
	if row.Subject != "Invoice" {
		t.Errorf("subject = %q, want the original wording", row.Subject)
	}
}
