// Package outbound assembles the mail Mailman sends and hands it to the app
// under test.
//
// A reply written in the inbox becomes a real RFC 5322 message: correct
// Message-ID, In-Reply-To and References, so it threads in Mailman and so the
// receiving app sees the headers it would see from a real mail provider.
package outbound

import (
	"bytes"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/jhillyerd/enmime/v2"
	"github.com/oklog/ulid/v2"

	"github.com/glennraya/mailman/internal/conversation"
	"github.com/glennraya/mailman/internal/mailstore"
)

// messageIDDomain is the right-hand side of the Message-IDs Mailman mints.
// It is deliberately not a real domain: nothing should ever try to deliver
// to it, and seeing it in a header makes the message's origin obvious.
const messageIDDomain = "mailman.local"

// Attachment is a file to include in a composed message.
type Attachment struct {
	Filename  string
	MediaType string
	Content   []byte
}

// Message is a mail to be assembled. Only From, at least one recipient, and
// one of Text or HTML are required.
type Message struct {
	From    string
	To      []string
	Cc      []string
	Bcc     []string
	ReplyTo string

	Subject string
	Text    string
	HTML    string

	// MessageID is generated when empty.
	MessageID  string
	InReplyTo  string
	References []string

	Date        time.Time
	Attachments []Attachment
}

// NewMessageID mints an identifier for a message Mailman is sending.
func NewMessageID() string {
	return strings.ToLower(ulid.Make().String()) + "@" + messageIDDomain
}

// Build renders the message as RFC 5322 bytes.
func (m *Message) Build() ([]byte, error) {
	from, err := parseAddress(m.From)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}

	to, err := parseAddresses(m.To)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	cc, err := parseAddresses(m.Cc)
	if err != nil {
		return nil, fmt.Errorf("cc: %w", err)
	}
	bcc, err := parseAddresses(m.Bcc)
	if err != nil {
		return nil, fmt.Errorf("bcc: %w", err)
	}

	if len(to)+len(cc)+len(bcc) == 0 {
		return nil, fmt.Errorf("a message needs at least one recipient")
	}
	if strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.HTML) == "" {
		return nil, fmt.Errorf("a message needs a text or html body")
	}

	date := m.Date
	if date.IsZero() {
		date = time.Now()
	}

	messageID := m.MessageID
	if messageID == "" {
		messageID = NewMessageID()
	}

	builder := enmime.Builder().
		From(from.Name, from.Address).
		ToAddrs(to).
		CCAddrs(cc).
		BCCAddrs(bcc).
		Subject(m.Subject).
		Date(date).
		Header("Message-ID", conversation.Bracket(messageID))

	if m.ReplyTo != "" {
		replyTo, err := parseAddress(m.ReplyTo)
		if err != nil {
			return nil, fmt.Errorf("reply-to: %w", err)
		}
		builder = builder.ReplyTo(replyTo.Name, replyTo.Address)
	}

	// Threading headers. In-Reply-To names the immediate parent; References
	// carries the whole chain, which is what clients walk when they group a
	// conversation. Both are required for a reply to thread correctly in the
	// app that receives it.
	if m.InReplyTo != "" {
		builder = builder.Header("In-Reply-To", conversation.Bracket(m.InReplyTo))
	}
	if len(m.References) > 0 {
		bracketed := make([]string, 0, len(m.References))
		for _, reference := range m.References {
			if wrapped := conversation.Bracket(reference); wrapped != "" {
				bracketed = append(bracketed, wrapped)
			}
		}
		if len(bracketed) > 0 {
			builder = builder.Header("References", strings.Join(bracketed, " "))
		}
	}

	// A text alternative is always included. Some clients and inbound
	// parsers only read text/plain, and an HTML-only reply would arrive
	// blank in them.
	text := m.Text
	if strings.TrimSpace(text) == "" {
		text = HTMLToText(m.HTML)
	}
	builder = builder.Text([]byte(text))

	if strings.TrimSpace(m.HTML) != "" {
		builder = builder.HTML([]byte(m.HTML))
	}

	for _, attachment := range m.Attachments {
		mediaType := attachment.MediaType
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		builder = builder.AddAttachment(attachment.Content, mediaType, attachment.Filename)
	}

	part, err := builder.Build()
	if err != nil {
		return nil, fmt.Errorf("build message: %w", err)
	}

	var out bytes.Buffer
	if err := part.Encode(&out); err != nil {
		return nil, fmt.Errorf("encode message: %w", err)
	}
	return out.Bytes(), nil
}

// Recipients is the envelope: everyone the message is actually addressed to,
// Bcc included, deduplicated. This is what the SMTP conversation would have
// carried, and what Bcc recovery reads back later.
func (m *Message) Recipients() []string {
	seen := make(map[string]struct{})
	var out []string

	for _, group := range [][]string{m.To, m.Cc, m.Bcc} {
		for _, raw := range group {
			addr, err := parseAddress(raw)
			if err != nil {
				continue
			}
			key := strings.ToLower(addr.Address)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, addr.Address)
		}
	}
	return out
}

func parseAddress(raw string) (mail.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return mail.Address{}, fmt.Errorf("address is empty")
	}

	parsed, err := mail.ParseAddress(raw)
	if err != nil {
		return mail.Address{}, fmt.Errorf("%q is not a valid address", raw)
	}
	return *parsed, nil
}

func parseAddresses(raw []string) ([]mail.Address, error) {
	var out []mail.Address

	for _, entry := range raw {
		// A single field may hold a comma-separated list, which is how the
		// composer's recipient inputs arrive.
		parsed, err := mail.ParseAddressList(entry)
		if err != nil {
			single, singleErr := parseAddress(entry)
			if singleErr != nil {
				return nil, singleErr
			}
			out = append(out, single)
			continue
		}

		for _, addr := range parsed {
			out = append(out, *addr)
		}
	}
	return out, nil
}

// Addresses converts stored addresses back into the strings the builder
// takes, preserving display names.
func Addresses(list []mailstore.Address) []string {
	out := make([]string, 0, len(list))
	for _, addr := range list {
		if addr.Name == "" {
			out = append(out, addr.Address)
			continue
		}
		formatted := mail.Address{Name: addr.Name, Address: addr.Address}
		out = append(out, formatted.String())
	}
	return out
}
