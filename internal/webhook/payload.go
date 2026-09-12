// Package webhook delivers a reply written in Mailman's inbox to the
// application under test, as the inbound-email webhook that application
// already knows how to handle.
//
// Three shapes are supported. "generic" is Mailman's own, for a handler
// written against Mailman. "mailgun" and "postmark" reproduce what those
// providers POST to an inbound route, field for field, so an app's existing
// production handler can be exercised locally without a branch in it. The
// point is that nothing about the app has to change to be testable.
package webhook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/outbound"
)

// Payload is one ready-to-send request.
type Payload struct {
	Body        []byte
	ContentType string

	// Header carries whatever the format signs with. Mailgun signs inside the
	// body instead, so this is empty for it.
	Header http.Header

	// BasicAuthUser is set when a format secures its endpoint with HTTP Basic
	// rather than a signature, which is how Postmark's inbound URLs work.
	BasicAuthUser string
}

// Build renders a message in the shape the route asks for.
func Build(m *mailstore.Message, raw []byte, route config.Resolved) (*Payload, error) {
	switch route.Format {
	case config.FormatMailgun:
		return buildMailgun(m, raw, route)
	case config.FormatPostmark:
		return buildPostmark(m, raw, route)
	case config.FormatGeneric, "":
		return buildGeneric(m, raw, route)
	default:
		return nil, fmt.Errorf("unknown webhook format %q", route.Format)
	}
}

// genericPayload is Mailman's own shape: the parsed message plus the verbatim
// source, so a handler can use whichever it prefers.
type genericPayload struct {
	MessageID  string   `json:"message_id"`
	InReplyTo  string   `json:"in_reply_to,omitempty"`
	References []string `json:"references,omitempty"`

	From    string   `json:"from"`
	To      []string `json:"to"`
	Cc      []string `json:"cc,omitempty"`
	Bcc     []string `json:"bcc,omitempty"`
	ReplyTo string   `json:"reply_to,omitempty"`

	Recipient string `json:"recipient"`
	Sender    string `json:"sender"`

	Subject        string `json:"subject"`
	Date           string `json:"date"`
	Text           string `json:"text"`
	HTML           string `json:"html,omitempty"`
	StrippedText   string `json:"stripped_text"`
	ConversationID int64  `json:"conversation_id"`

	Headers [][2]string `json:"headers"`
	Raw     string      `json:"raw"`
}

// buildGeneric signs the exact bytes it sends with an HMAC over the body.
//
// This is Mailman's own convention rather than an imitation of a provider:
// X-Mailman-Signature is "sha256=" plus the hex digest of the request body
// keyed by the configured signing key, and X-Mailman-Timestamp is when it was
// sent. Signing the body itself -- rather than a token pair -- is what lets a
// handler detect a modified payload, not merely a replayed one.
func buildGeneric(m *mailstore.Message, raw []byte, route config.Resolved) (*Payload, error) {
	text := bodyText(m)

	body, err := json.Marshal(genericPayload{
		MessageID:      m.MessageID,
		InReplyTo:      m.InReplyTo,
		References:     m.References,
		From:           formatList(m.From),
		To:             bare(m.To),
		Cc:             bare(m.Cc),
		Bcc:            bare(m.Bcc),
		ReplyTo:        formatList(m.ReplyTo),
		Recipient:      firstRecipient(m),
		Sender:         m.Envelope.From,
		Subject:        m.Subject,
		Date:           sentAt(m).Format(time.RFC1123Z),
		Text:           text,
		HTML:           m.HTMLBody,
		StrippedText:   StripQuoted(text),
		ConversationID: m.ConversationID,
		Headers:        HeaderPairs(raw),
		Raw:            string(raw),
	})
	if err != nil {
		return nil, fmt.Errorf("encode generic payload: %w", err)
	}

	header := http.Header{}
	header.Set("X-Mailman-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	if route.SigningKey != "" {
		header.Set("X-Mailman-Signature", "sha256="+hmacHex(route.SigningKey, body))
	}

	return &Payload{Body: body, ContentType: "application/json", Header: header}, nil
}

// buildMailgun reproduces the multipart/form-data POST Mailgun sends to a
// forward() action. Field names are Mailgun's, hyphens and capitalization
// included, because the receiving handler reads them by name.
func buildMailgun(m *mailstore.Message, raw []byte, route config.Resolved) (*Payload, error) {
	plain := bodyText(m)
	stripped, signature := StripSignature(StripQuoted(plain))

	headers, err := json.Marshal(HeaderPairs(raw))
	if err != nil {
		return nil, fmt.Errorf("encode message headers: %w", err)
	}

	// Mailgun's signature is the hex HMAC-SHA256 of the timestamp
	// concatenated with the token -- no separator between them -- keyed by
	// the webhook signing key. Handlers recompute exactly that, so the two
	// values have to travel with it.
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	token, err := newToken()
	if err != nil {
		return nil, err
	}

	fields := [][2]string{
		{"recipient", firstRecipient(m)},
		{"sender", m.Envelope.From},
		{"from", formatList(m.From)},
		{"subject", m.Subject},
		{"body-plain", plain},
		{"stripped-text", stripped},
		{"stripped-signature", signature},
		{"message-headers", string(headers)},
		{"attachment-count", "0"},
		{"timestamp", timestamp},
		{"token", token},
		{"domain", config.Domain(firstRecipient(m))},
		{"X-Mailgun-Incoming", "Yes"},
	}

	if m.HTMLBody != "" {
		fields = append(fields,
			[2]string{"body-html", m.HTMLBody},
			[2]string{"stripped-html", m.HTMLBody})
	}

	// Every MIME header also travels as a field of its own, in Mailgun's
	// canonical casing, alongside the message-headers JSON.
	//
	// This is not redundancy for its own sake. Real handlers read these
	// directly -- `$data['In-Reply-To']` is a gate in production code, and a
	// handler that never sees the field drops the reply rather than
	// processing it. The JSON blob is the complete, ordered record; these are
	// what code actually indexes.
	// Mailgun's parsed fields are all lower-case and canonical header casing
	// is always capitalized, so `From` and `from` are two distinct fields
	// carrying two different things -- the raw header and Mailgun's parse of
	// it. Real Mailgun sends both, so no header needs suppressing here. The
	// one exception is the field Mailman synthesizes in canonical form
	// itself, which a message could otherwise duplicate.
	for _, header := range HeaderPairs(raw) {
		name := textproto.CanonicalMIMEHeaderKey(header[0])
		if name == "X-Mailgun-Incoming" {
			continue
		}
		fields = append(fields, [2]string{name, header[1]})
	}

	// With no signing key there is nothing to sign with, and a signature
	// computed from an empty key would be worse than none: it would look
	// valid in shape and fail verification for a reason nothing explains.
	// An app that verifies then rejects the delivery, visibly, which is the
	// correct outcome and says exactly what to configure.
	if route.SigningKey != "" {
		fields = append(fields,
			[2]string{"signature", hmacHex(route.SigningKey, []byte(timestamp+token))})
	}

	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for _, field := range fields {
		if err := form.WriteField(field[0], field[1]); err != nil {
			return nil, fmt.Errorf("write %s field: %w", field[0], err)
		}
	}
	if err := form.Close(); err != nil {
		return nil, fmt.Errorf("close multipart form: %w", err)
	}

	return &Payload{Body: buf.Bytes(), ContentType: form.FormDataContentType()}, nil
}

// postmarkAddress is Postmark's expanded form of one address.
type postmarkAddress struct {
	Email       string `json:"Email"`
	Name        string `json:"Name"`
	MailboxHash string `json:"MailboxHash"`
}

// postmarkPromoted are the headers Postmark lifts into named top-level fields
// and so does not repeat in Headers.
var postmarkPromoted = map[string]bool{
	"From": true, "To": true, "Cc": true, "Bcc": true,
	"Subject": true, "Date": true, "Reply-To": true,
}

type postmarkHeader struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}

type postmarkPayload struct {
	FromName      string            `json:"FromName"`
	MessageStream string            `json:"MessageStream"`
	From          string            `json:"From"`
	FromFull      postmarkAddress   `json:"FromFull"`
	To            string            `json:"To"`
	ToFull        []postmarkAddress `json:"ToFull"`
	Cc            string            `json:"Cc,omitempty"`
	CcFull        []postmarkAddress `json:"CcFull,omitempty"`
	Bcc           string            `json:"Bcc,omitempty"`
	BccFull       []postmarkAddress `json:"BccFull,omitempty"`

	OriginalRecipient string `json:"OriginalRecipient"`
	Subject           string `json:"Subject"`
	MessageID         string `json:"MessageID"`
	ReplyTo           string `json:"ReplyTo,omitempty"`
	MailboxHash       string `json:"MailboxHash"`
	Date              string `json:"Date"`

	TextBody          string `json:"TextBody"`
	HtmlBody          string `json:"HtmlBody"`
	StrippedTextReply string `json:"StrippedTextReply"`

	Headers     []postmarkHeader `json:"Headers"`
	Attachments []struct{}       `json:"Attachments"`
}

// buildPostmark reproduces Postmark's inbound webhook JSON.
//
// Two details are easy to get backwards and both would break the app under
// test in ways that look like its own bug:
//
// MessageID is a Postmark-assigned UUID, not the message's RFC 5322
// Message-ID. Putting the RFC value there would be more informative and
// completely unfaithful -- a handler that stores MessageID as a provider
// reference would start storing something else entirely. The RFC Message-ID
// travels in Headers, which is exactly where Postmark puts it and where an
// app that threads already looks.
//
// Headers excludes the fields promoted to the top level. Postmark does not
// repeat From, To, Cc, Bcc, Subject, Date or Reply-To down there, so neither
// does Mailman.
func buildPostmark(m *mailstore.Message, raw []byte, route config.Resolved) (*Payload, error) {
	recipient := firstRecipient(m)
	text := bodyText(m)

	messageID, err := newUUID()
	if err != nil {
		return nil, err
	}

	var from mailstore.Address
	if len(m.From) > 0 {
		from = m.From[0]
	}

	var headers []postmarkHeader
	for _, pair := range HeaderPairs(raw) {
		if postmarkPromoted[textproto.CanonicalMIMEHeaderKey(pair[0])] {
			continue
		}
		headers = append(headers, postmarkHeader{Name: pair[0], Value: pair[1]})
	}
	if headers == nil {
		headers = []postmarkHeader{}
	}

	body, err := json.Marshal(postmarkPayload{
		FromName:      from.Name,
		MessageStream: "inbound",
		From:          from.Address,
		FromFull:      postmarkAddresses(m.From)[0],
		To:            formatList(m.To),
		ToFull:        postmarkAddresses(m.To),
		Cc:            formatList(m.Cc),
		CcFull:        postmarkAddresses(m.Cc),
		Bcc:           formatList(m.Bcc),
		BccFull:       postmarkAddresses(m.Bcc),

		OriginalRecipient: recipient,
		Subject:           m.Subject,
		MessageID:         messageID,
		ReplyTo:           formatList(m.ReplyTo),
		// The part after "+" in the recipient. This is the mechanism an app
		// uses to tie a reply back to a record -- order-4471+t3h2@... -- so
		// leaving it blank would break the one feature inbound parsing is
		// most often bought for.
		MailboxHash: MailboxHash(recipient),
		Date:        sentAt(m).Format(time.RFC1123Z),

		TextBody: text,
		HtmlBody: m.HTMLBody,
		// Postmark's StrippedTextReply isolates the reply from the quoted
		// conversation, and nothing more -- it has no separate signature
		// field, so a signature block stays part of the reply.
		StrippedTextReply: StripQuoted(text),

		Headers:     headers,
		Attachments: []struct{}{},
	})
	if err != nil {
		return nil, fmt.Errorf("encode postmark payload: %w", err)
	}

	payload := &Payload{Body: body, ContentType: "application/json", Header: http.Header{}}

	// Postmark signs nothing; an inbound URL is secured by being secret, and
	// optionally by HTTP Basic credentials on the URL itself. A signing key
	// configured for a Postmark route is therefore sent as the Basic user,
	// which is the only thing a Postmark-shaped handler could check.
	if route.SigningKey != "" {
		payload.BasicAuthUser = route.SigningKey
	}

	return payload, nil
}

// postmarkAddresses always returns at least one entry: FromFull is a single
// object in Postmark's schema, and indexing it is the caller's shortcut.
func postmarkAddresses(list []mailstore.Address) []postmarkAddress {
	out := make([]postmarkAddress, 0, len(list))
	for _, addr := range list {
		out = append(out, postmarkAddress{
			Email:       addr.Address,
			Name:        addr.Name,
			MailboxHash: MailboxHash(addr.Address),
		})
	}
	if len(out) == 0 {
		out = append(out, postmarkAddress{})
	}
	return out
}

// MailboxHash returns the part of an address's local part after the first
// "+", which is how plus addressing carries a record reference.
func MailboxHash(address string) string {
	local, _, found := strings.Cut(address, "@")
	if !found {
		local = address
	}
	_, hash, found := strings.Cut(local, "+")
	if !found {
		return ""
	}
	return hash
}

// bodyText is the plain-text body, generated from the HTML when the message
// has no text part. Every format's text field is documented as always
// present, and a handler reading only that field would otherwise see nothing.
func bodyText(m *mailstore.Message) string {
	if strings.TrimSpace(m.TextBody) != "" {
		return m.TextBody
	}
	return outbound.HTMLToText(m.HTMLBody)
}

// firstRecipient is the envelope recipient a route was matched against, so
// the payload names the same address the routing decision used.
func firstRecipient(m *mailstore.Message) string {
	if len(m.Envelope.Recipients) > 0 {
		return m.Envelope.Recipients[0]
	}
	if len(m.To) > 0 {
		return m.To[0].Address
	}
	return ""
}

func sentAt(m *mailstore.Message) time.Time {
	if !m.SentAt.IsZero() {
		return m.SentAt
	}
	return m.CreatedAt
}

func formatList(list []mailstore.Address) string {
	out := make([]string, 0, len(list))
	for _, addr := range list {
		if addr.Name == "" {
			out = append(out, addr.Address)
			continue
		}
		formatted := mail.Address{Name: addr.Name, Address: addr.Address}
		out = append(out, formatted.String())
	}
	return strings.Join(out, ", ")
}

func bare(list []mailstore.Address) []string {
	out := make([]string, 0, len(list))
	for _, addr := range list {
		out = append(out, addr.Address)
	}
	return out
}
