package outbound

import (
	"strings"
	"testing"

	"github.com/glennraya/mailman/internal/mailstore"
)

func parent() *mailstore.Message {
	return &mailstore.Message{
		MessageID:  "receipt-1@myapp.test",
		References: []string{"thread-root@myapp.test"},
		From:       []mailstore.Address{{Name: "MyApp", Address: "no-reply@myapp.test"}},
		To:         []mailstore.Address{{Name: "Ada Lovelace", Address: "ada@example.test"}},
		Subject:    "Your receipt",
		Envelope: mailstore.Envelope{
			From:       "no-reply@myapp.test",
			Recipients: []string{"ada@example.test"},
		},
	}
}

func TestNewReplyThreadsAndSwapsAddresses(t *testing.T) {
	reply := NewReply(parent())

	if reply.Subject != "Re: Your receipt" {
		t.Errorf("subject = %q", reply.Subject)
	}
	if reply.InReplyTo != "receipt-1@myapp.test" {
		t.Errorf("in-reply-to = %q", reply.InReplyTo)
	}
	// The parent's chain, then the parent: most distant first.
	want := []string{"thread-root@myapp.test", "receipt-1@myapp.test"}
	if strings.Join(reply.References, " ") != strings.Join(want, " ") {
		t.Errorf("references = %v, want %v", reply.References, want)
	}
	if len(reply.To) != 1 || reply.To[0] != `"MyApp" <no-reply@myapp.test>` {
		t.Errorf("to = %v", reply.To)
	}
	if reply.From != `"Ada Lovelace" <ada@example.test>` {
		t.Errorf("from = %q", reply.From)
	}
	if reply.MessageID == "" {
		t.Error("no message id was minted")
	}
}

// Reply-To is how an app routes an answer back to a record, so it has to beat
// the From address it was sent from.
func TestNewReplyPrefersReplyTo(t *testing.T) {
	p := parent()
	p.ReplyTo = []mailstore.Address{{Address: "order-4471@mail.myapp.test"}}

	reply := NewReply(p)

	if len(reply.To) != 1 || reply.To[0] != "order-4471@mail.myapp.test" {
		t.Errorf("to = %v, want the Reply-To address", reply.To)
	}
}

func TestNewReplyDoesNotStackReplyPrefixes(t *testing.T) {
	p := parent()
	p.Subject = "Re: Your receipt"

	if reply := NewReply(p); reply.Subject != "Re: Your receipt" {
		t.Errorf("subject = %q, want no second prefix", reply.Subject)
	}
}

// A mail with no To header still has an envelope, which is the only record of
// who it was for.
func TestNewReplyFallsBackToTheEnvelope(t *testing.T) {
	p := parent()
	p.To = nil

	if reply := NewReply(p); reply.From != "ada@example.test" {
		t.Errorf("from = %q, want the envelope recipient", reply.From)
	}
}

// A reply to a message with no Message-ID cannot thread on headers. It must
// still be sendable: the subject prefix is what groups it, via the same
// fallback the ingest path uses.
func TestNewReplyToAMessageWithNoID(t *testing.T) {
	p := parent()
	p.MessageID = ""
	p.References = nil

	reply := NewReply(p)

	if reply.InReplyTo != "" {
		t.Errorf("in-reply-to = %q, want empty", reply.InReplyTo)
	}
	if len(reply.References) != 0 {
		t.Errorf("references = %v, want none", reply.References)
	}
	if reply.Subject != "Re: Your receipt" {
		t.Errorf("subject = %q", reply.Subject)
	}
}
