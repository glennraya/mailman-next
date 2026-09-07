package mailmime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
)

func newIngestor(t *testing.T) (*Ingestor, *mailstore.Store) {
	t.Helper()

	home := t.TempDir()
	store, err := mailstore.Open(filepath.Join(home, "mailman.db"), filepath.Join(home, "mail"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return NewIngestor(store, events.New(), discardLogger()), store
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return raw
}

// message builds a minimal RFC 5322 message for the threading tests, where
// the headers matter and the body does not.
func message(subject, messageID, inReplyTo, references string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "From: sender@acme.test\r\nTo: glenn@myapp.test\r\n")
	fmt.Fprintf(&b, "Subject: %s\r\nMessage-ID: <%s>\r\n", subject, messageID)
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\n", inReplyTo)
	}
	if references != "" {
		fmt.Fprintf(&b, "References: %s\r\n", references)
	}
	fmt.Fprintf(&b, "\r\nbody text\r\n")
	return []byte(b.String())
}

func TestIngestStoresHeadersAndBothBodies(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	envelope := mailstore.Envelope{
		From:       "billing@acme.test",
		Recipients: []string{"glenn@myapp.test", "accounts@myapp.test"},
	}

	stored, err := ingestor.Ingest(ctx, fixture(t, "simple.eml"), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	if got.Subject != "Order 4471 has shipped" {
		t.Errorf("subject = %q", got.Subject)
	}
	if got.MessageID != "order-4471@acme.test" {
		t.Errorf("message id = %q, want it unbracketed", got.MessageID)
	}
	if len(got.From) != 1 || got.From[0].Address != "billing@acme.test" || got.From[0].Name != "Acme Billing" {
		t.Errorf("from = %+v", got.From)
	}
	if len(got.ReplyTo) != 1 || got.ReplyTo[0].Address != "order-4471@mail.acme.test" {
		t.Errorf("reply-to = %+v; it decides where a reply is routed", got.ReplyTo)
	}
	if !strings.Contains(got.TextBody, "on its way") {
		t.Errorf("text body = %q", got.TextBody)
	}
	if !strings.Contains(got.HTMLBody, "<strong>4471</strong>") {
		t.Errorf("html body = %q", got.HTMLBody)
	}
	if got.Seen() {
		t.Error("captured mail arrived already marked read")
	}
	if got.SizeBytes == 0 {
		t.Error("size not recorded")
	}

	// The raw source has to survive verbatim -- it is what the "view source"
	// view shows and what a raw-format webhook would forward.
	raw, err := os.ReadFile(store.Path(got.RawPath))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if !strings.Contains(string(raw), "Message-ID: <order-4471@acme.test>") {
		t.Error("raw source does not match what was captured")
	}
}

func TestIngestSeparatesInlineFromAttached(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	stored, err := ingestor.Ingest(ctx, fixture(t, "inline-image.eml"),
		mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got.Attachments) != 2 {
		t.Fatalf("stored %d attachments, want 2: %+v", len(got.Attachments), got.Attachments)
	}

	var inline, attached *mailstore.Attachment
	for i := range got.Attachments {
		if got.Attachments[i].Inline {
			inline = &got.Attachments[i]
		} else {
			attached = &got.Attachments[i]
		}
	}

	if inline == nil {
		t.Fatal("no inline part; the cid: image would not render")
	}
	if inline.ContentID != "proof-image@acme.test" {
		t.Errorf("content id = %q, want it unbracketed for cid: lookup", inline.ContentID)
	}
	if inline.Filename != "proof.png" {
		t.Errorf("inline filename = %q", inline.Filename)
	}

	if attached == nil {
		t.Fatal("no attached part")
	}
	if attached.Filename != "invoice.pdf" {
		t.Errorf("attachment filename = %q", attached.Filename)
	}

	// Both must be on disk where the row says they are.
	for _, a := range got.Attachments {
		if _, err := os.Stat(store.Path(a.Path)); err != nil {
			t.Errorf("attachment %q missing from disk: %v", a.Filename, err)
		}
	}

	// And the cid: resolver has to find the inline one.
	found, err := store.FindInlineAttachment(ctx, got.ID, "proof-image@acme.test")
	if err != nil {
		t.Fatalf("resolve cid: %v", err)
	}
	if found.ID != inline.ID {
		t.Errorf("cid resolved to attachment %d, want %d", found.ID, inline.ID)
	}
}

func TestIngestKeepsMailWithNoBoundary(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	// A Content-Type of multipart with no boundary is malformed, but the
	// developer still needs to see what their app sent.
	stored, err := ingestor.Ingest(ctx, fixture(t, "no-boundary.eml"),
		mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest refused a malformed message: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Subject != "Password reset" {
		t.Errorf("subject = %q; headers should still have parsed", got.Subject)
	}
	if !strings.Contains(got.TextBody, "never opens a boundary") {
		t.Errorf("body lost: %q", got.TextBody)
	}
}

func TestIngestKeepsCompleteGarbage(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	stored, err := ingestor.Ingest(ctx, []byte("this is not a mail message at all"),
		mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(got.TextBody, "not a mail message") {
		t.Errorf("payload lost: %q", got.TextBody)
	}
}

func TestIngestDerivesBccFromTheEnvelope(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	// audit@myapp.test appears in no header, so the only way to know it was
	// a recipient is the SMTP conversation.
	envelope := mailstore.Envelope{
		From: "billing@acme.test",
		Recipients: []string{
			"glenn@myapp.test", "accounts@myapp.test", "audit@myapp.test",
		},
	}

	stored, err := ingestor.Ingest(ctx, fixture(t, "simple.eml"), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(got.Bcc) != 1 || got.Bcc[0].Address != "audit@myapp.test" {
		t.Errorf("bcc = %+v, want just audit@myapp.test", got.Bcc)
	}
}

func TestIngestThreadsByInReplyTo(t *testing.T) {
	ingestor, _ := newIngestor(t)
	ctx := context.Background()
	envelope := mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}

	first, err := ingestor.Ingest(ctx, message("Invoice 12", "a@acme.test", "", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest first: %v", err)
	}

	second, err := ingestor.Ingest(ctx, message("Re: Invoice 12", "b@acme.test", "a@acme.test", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest reply: %v", err)
	}

	if first.ConversationID != second.ConversationID {
		t.Errorf("reply landed in conversation %d, want %d",
			second.ConversationID, first.ConversationID)
	}
}

func TestIngestKeepsUnrelatedMailWithTheSameSubjectApart(t *testing.T) {
	ingestor, _ := newIngestor(t)
	ctx := context.Background()
	envelope := mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}

	// Two receipts with an identical subject and no threading headers are
	// two separate events, not a conversation. Merging them is the failure
	// mode that makes subject-only threading unusable for transactional mail.
	first, err := ingestor.Ingest(ctx, message("Your receipt", "r1@acme.test", "", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest first: %v", err)
	}
	second, err := ingestor.Ingest(ctx, message("Your receipt", "r2@acme.test", "", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest second: %v", err)
	}

	if first.ConversationID == second.ConversationID {
		t.Error("two unrelated receipts were merged into one conversation")
	}
}

func TestIngestFallsBackToSubjectForAReply(t *testing.T) {
	ingestor, _ := newIngestor(t)
	ctx := context.Background()
	envelope := mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}

	// Some clients drop In-Reply-To entirely. A "Re:" subject is then the
	// only signal left, and it is good enough precisely because the prefix
	// says this is an answer to something.
	first, err := ingestor.Ingest(ctx, message("Support request", "s1@acme.test", "", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest first: %v", err)
	}
	second, err := ingestor.Ingest(ctx, message("Re: Support request", "s2@acme.test", "", ""), envelope, mailstore.DirectionInbound)
	if err != nil {
		t.Fatalf("ingest reply: %v", err)
	}

	if first.ConversationID != second.ConversationID {
		t.Error("a Re: reply with no threading headers started a new conversation")
	}
}

func TestIngestMarksOutboundMailRead(t *testing.T) {
	ingestor, store := newIngestor(t)
	ctx := context.Background()

	stored, err := ingestor.Ingest(ctx, fixture(t, "simple.eml"),
		mailstore.Envelope{Recipients: []string{"glenn@myapp.test"}}, mailstore.DirectionOutbound)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}

	got, err := store.GetMessage(ctx, stored.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !got.Seen() {
		t.Error("a reply Mailman itself sent was left unread")
	}

	unread, err := store.CountUnread(ctx)
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if unread != 0 {
		t.Errorf("unread = %d, want 0 -- your own reply must not ring the bell", unread)
	}
}

func TestSafeFilenameRejectsTraversal(t *testing.T) {
	cases := map[string]string{
		"invoice.pdf":        "invoice.pdf",
		"../../etc/passwd":   "passwd",
		"/absolute/path.txt": "path.txt",
		"":                   "attachment-3",
		"..":                 "attachment-3",
		".":                  "attachment-3",
	}

	for name, want := range cases {
		if got := safeFilename(name, 3); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", name, got, want)
		}
	}
}
