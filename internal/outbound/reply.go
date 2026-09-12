package outbound

import (
	"github.com/glennraya/mailman/internal/conversation"
	"github.com/glennraya/mailman/internal/mailstore"
)

// NewReply returns the message a reply to parent starts from: the addresses
// swapped, the subject prefixed, and the threading chain continued. The caller
// overlays whatever was actually typed on top.
//
// This is a pure function of the parent, so the seeding the composer shows and
// the seeding the server applies cannot drift apart.
func NewReply(parent *mailstore.Message) Message {
	reply := Message{
		Subject:   conversation.ReplySubject(parent.Subject),
		MessageID: NewMessageID(),
		InReplyTo: parent.MessageID,

		// Ancestors is exactly RFC 5322's rule for a reply's References: the
		// parent's own chain, then the parent itself, most distant first,
		// deduplicated. It is the same function the ingest path threads with,
		// which is why a reply Mailman sends groups the way one it receives
		// would.
		References: conversation.Ancestors(parent.MessageID, parent.References),
	}

	// Reply-To wins over From when the sender named one. This is the whole
	// mechanism behind an address like order-4471@mail.myapp.test: the app
	// puts it in Reply-To precisely so an answer comes back to it rather than
	// to the no-reply address the mail was sent from.
	recipients := parent.ReplyTo
	if len(recipients) == 0 {
		recipients = parent.From
	}
	reply.To = Addresses(recipients)

	// Answer as whoever the mail was addressed to, so the reply looks like it
	// came from the person reading the inbox.
	if senders := Addresses(parent.To); len(senders) > 0 {
		reply.From = senders[0]
	} else if len(parent.Envelope.Recipients) > 0 {
		reply.From = parent.Envelope.Recipients[0]
	}

	return reply
}
