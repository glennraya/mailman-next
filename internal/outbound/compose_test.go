package outbound

import (
	"strings"
	"testing"

	"github.com/glennraya/mailman/internal/mailmime"
	"github.com/glennraya/mailman/internal/mailstore"
)

// buildAndParse renders a message and reads it back, which is the only
// meaningful check: what matters is that a parser sees what was intended.
func buildAndParse(t *testing.T, m *Message) *mailmime.Parsed {
	t.Helper()

	raw, err := m.Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	parsed, err := mailmime.Parse(raw)
	if err != nil {
		t.Fatalf("parse the message we just built: %v", err)
	}
	return parsed
}

func TestBuildProducesBothBodies(t *testing.T) {
	parsed := buildAndParse(t, &Message{
		From:    "Mailman <glenn@myapp.test>",
		To:      []string{"support@acme.test"},
		Subject: "Re: Order 4471",
		HTML:    "<p>Thanks, that <strong>worked</strong>.</p>",
	})

	if parsed.Subject != "Re: Order 4471" {
		t.Errorf("subject = %q", parsed.Subject)
	}
	if !strings.Contains(parsed.HTML, "<strong>worked</strong>") {
		t.Errorf("html body = %q", parsed.HTML)
	}

	// An HTML-only reply arrives blank in anything that reads text/plain,
	// which includes several inbound parsers, so a text alternative is
	// always generated.
	if !strings.Contains(parsed.Text, "worked") {
		t.Errorf("text alternative = %q, want the HTML downconverted", parsed.Text)
	}
}

func TestBuildSetsThreadingHeaders(t *testing.T) {
	parsed := buildAndParse(t, &Message{
		From:       "glenn@myapp.test",
		To:         []string{"support@acme.test"},
		Subject:    "Re: Order 4471",
		Text:       "Thanks.",
		MessageID:  "reply-1@mailman.local",
		InReplyTo:  "order-4471@acme.test",
		References: []string{"order-4471@acme.test"},
	})

	if parsed.MessageID != "reply-1@mailman.local" {
		t.Errorf("message id = %q", parsed.MessageID)
	}

	// Without these the reply starts a new thread in the receiving app,
	// which is the whole failure this product exists to avoid.
	if parsed.InReplyTo != "order-4471@acme.test" {
		t.Errorf("in-reply-to = %q", parsed.InReplyTo)
	}
	if len(parsed.References) != 1 || parsed.References[0] != "order-4471@acme.test" {
		t.Errorf("references = %v", parsed.References)
	}
}

func TestBuildMintsAMessageIDWhenNoneGiven(t *testing.T) {
	parsed := buildAndParse(t, &Message{
		From: "glenn@myapp.test",
		To:   []string{"support@acme.test"},
		Text: "Hello.",
	})

	if parsed.MessageID == "" {
		t.Fatal("no Message-ID was generated")
	}
	if !strings.HasSuffix(parsed.MessageID, "@"+messageIDDomain) {
		t.Errorf("message id = %q, want it on %s", parsed.MessageID, messageIDDomain)
	}
}

func TestBuildCarriesAttachments(t *testing.T) {
	parsed := buildAndParse(t, &Message{
		From: "glenn@myapp.test",
		To:   []string{"support@acme.test"},
		Text: "See attached.",
		Attachments: []Attachment{
			{Filename: "notes.txt", MediaType: "text/plain", Content: []byte("some notes")},
		},
	})

	if len(parsed.Parts) != 1 {
		t.Fatalf("message carries %d parts, want 1", len(parsed.Parts))
	}
	if parsed.Parts[0].Filename != "notes.txt" {
		t.Errorf("attachment filename = %q", parsed.Parts[0].Filename)
	}
	if string(parsed.Parts[0].Content) != "some notes" {
		t.Errorf("attachment content = %q", parsed.Parts[0].Content)
	}
}

func TestRecipientsIsTheEnvelope(t *testing.T) {
	m := &Message{
		To:  []string{"a@test", "Someone <b@test>"},
		Cc:  []string{"c@test"},
		Bcc: []string{"d@test", "A@test"},
	}

	got := m.Recipients()
	want := []string{"a@test", "b@test", "c@test", "d@test"}

	if len(got) != len(want) {
		t.Fatalf("recipients = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("recipient %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuildRejectsIncompleteMessages(t *testing.T) {
	cases := map[string]*Message{
		"no sender":     {To: []string{"a@test"}, Text: "hi"},
		"no recipients": {From: "a@test", Text: "hi"},
		"no body":       {From: "a@test", To: []string{"b@test"}},
		"bad address":   {From: "not an address", To: []string{"b@test"}, Text: "hi"},
	}

	for name, m := range cases {
		if _, err := m.Build(); err == nil {
			t.Errorf("%s: build succeeded, want an error", name)
		}
	}
}

func TestAddressesPreservesDisplayNames(t *testing.T) {
	got := Addresses([]mailstore.Address{
		{Name: "Acme Billing", Address: "billing@acme.test"},
		{Address: "bare@acme.test"},
	})

	if got[0] != `"Acme Billing" <billing@acme.test>` {
		t.Errorf("addressed = %q", got[0])
	}
	if got[1] != "bare@acme.test" {
		t.Errorf("bare address = %q", got[1])
	}
}

func TestHTMLToText(t *testing.T) {
	got := HTMLToText("<p>First line.</p><p>Second line.</p>")

	if !strings.Contains(got, "First line.") || !strings.Contains(got, "Second line.") {
		t.Errorf("HTMLToText() = %q", got)
	}
	if strings.Contains(got, "<p>") {
		t.Errorf("HTMLToText() left markup in: %q", got)
	}
	if HTMLToText("   ") != "" {
		t.Error("blank HTML produced text")
	}
}
