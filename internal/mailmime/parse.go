// Package mailmime turns captured bytes into something Mailman can store.
//
// Parsing is deliberately forgiving. Mail arriving here was produced by
// whatever library a developer happened to be using, often mid-debugging, and
// a message that cannot be fully understood is still worth keeping: the whole
// point of a capture tool is to show what was actually sent. Nothing in this
// package treats a malformed message as a reason to lose it.
package mailmime

import (
	"bytes"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jhillyerd/enmime/v2"

	"github.com/glennraya/mailman/internal/conversation"
	"github.com/glennraya/mailman/internal/mailstore"
)

// parser is configured once and reused. Every option here trades strictness
// for keeping the message:
//
//   - SkipMalformedParts keeps the parts that did decode when one does not.
//   - MultipartWOBoundaryAsSinglePart rescues mail whose Content-Type claims
//     multipart but names no boundary, which some hand-rolled senders emit.
//   - AllowCorruptTextPartErrorPolicy keeps a text part whose base64 or
//     quoted-printable encoding is broken, rather than discarding the body.
//   - MaxStoredPartErrors caps how many complaints one message can carry, so
//     a pathological message cannot balloon in memory.
var parser = enmime.NewParser(
	enmime.SkipMalformedParts(true),
	enmime.MultipartWOBoundaryAsSinglePart(true),
	enmime.SetReadPartErrorPolicy(enmime.AllowCorruptTextPartErrorPolicy),
	enmime.MaxStoredPartErrors(50),
)

// Parsed is a message decomposed into the pieces Mailman stores. Part
// contents are still in memory here; writing them out is the ingestor's job.
type Parsed struct {
	MessageID  string
	InReplyTo  string
	References []string

	From    []mailstore.Address
	To      []mailstore.Address
	Cc      []mailstore.Address
	ReplyTo []mailstore.Address

	Subject string
	Date    time.Time
	Text    string
	HTML    string

	Parts []Part

	// Warnings are the non-fatal complaints the parser collected. They are
	// kept so the UI can mark a message as imperfectly parsed instead of
	// quietly showing a truncated body.
	Warnings []string

	// Degraded reports that at least one warning was severe, meaning part
	// of the message was lost. The raw .eml on disk is still complete, so
	// this is the signal to point the reader at the source view.
	Degraded bool
}

// Part is one attachment or inline entity, with its decoded bytes.
type Part struct {
	Filename  string
	MediaType string
	ContentID string
	Inline    bool
	Content   []byte
}

// Parse decodes a raw RFC 5322 message.
//
// It returns an error only when nothing at all could be made of the input.
// Everything short of that comes back as a Parsed with Warnings set, because
// a partially readable message is still the thing the developer wants to see.
func Parse(raw []byte) (*Parsed, error) {
	envelope, err := parser.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}

	p := &Parsed{
		MessageID:  conversation.Unbracket(envelope.GetHeader("Message-ID")),
		InReplyTo:  conversation.Unbracket(envelope.GetHeader("In-Reply-To")),
		References: conversation.ParseReferences(envelope.GetHeader("References")),
		Subject:    envelope.GetHeader("Subject"),
		Text:       envelope.Text,
		HTML:       envelope.HTML,
	}

	p.From = addresses(envelope, "From")
	p.To = addresses(envelope, "To")
	p.Cc = addresses(envelope, "Cc")
	p.ReplyTo = addresses(envelope, "Reply-To")

	if date, err := envelope.Date(); err == nil {
		p.Date = date.UTC()
	}

	// A nil error from ReadEnvelope does not mean a clean parse -- enmime
	// reports recoverable problems here instead, and a body can be truncated
	// without the call ever failing.
	for _, e := range envelope.Errors {
		if e == nil {
			continue
		}
		p.Warnings = append(p.Warnings, e.Error())
		p.Degraded = p.Degraded || e.Severe
	}

	p.Parts = collectParts(envelope)
	p.recoverBody(envelope)

	return p, nil
}

// recoverBody rescues the text of a message enmime could not classify.
//
// A Content-Type of multipart with no boundary is the common case: enmime
// keeps the payload on the root part but, since the declared type is not
// text, files it as an attachment and leaves both bodies empty. The message
// would then show as blank in the inbox with a nonsense attachment beside
// it, which is exactly the mail a developer is most likely trying to debug.
func (p *Parsed) recoverBody(envelope *enmime.Envelope) {
	if p.Text != "" || p.HTML != "" {
		return
	}
	if envelope.Root == nil || len(envelope.Root.Content) == 0 {
		return
	}
	if !utf8.Valid(envelope.Root.Content) {
		return
	}

	body := envelope.Root.Content
	p.Text = string(body)

	// The same bytes are now the body, so drop the part that was standing
	// in for it rather than offering it as a download too.
	kept := p.Parts[:0]
	for _, part := range p.Parts {
		if bytes.Equal(part.Content, body) {
			continue
		}
		kept = append(kept, part)
	}
	p.Parts = kept
}

// collectParts gathers everything that is not a body. Inlines carry the
// images an HTML body refers to by cid:; OtherParts catches the entities that
// fit neither category, which are still worth keeping rather than dropping.
func collectParts(envelope *enmime.Envelope) []Part {
	groups := [][]*enmime.Part{envelope.Attachments, envelope.Inlines, envelope.OtherParts}

	var parts []Part
	for _, group := range groups {
		for _, raw := range group {
			if raw == nil {
				continue
			}

			contentID := conversation.Unbracket(raw.ContentID)
			parts = append(parts, Part{
				Filename:  strings.TrimSpace(raw.FileName),
				MediaType: raw.ContentType,
				ContentID: contentID,
				// A Content-ID means an HTML body can reference this part,
				// which makes it inline whatever the disposition claims.
				Inline:  contentID != "" || strings.EqualFold(raw.Disposition, "inline"),
				Content: raw.Content,
			})
		}
	}
	return parts
}

// addresses reads one address header, tolerating the malformed lists that
// turn up in real mail: anything unparseable is skipped rather than failing
// the whole message.
func addresses(envelope *enmime.Envelope, header string) []mailstore.Address {
	list, err := envelope.AddressList(header)
	if err != nil || len(list) == 0 {
		return nil
	}
	return convertAddresses(list)
}

func convertAddresses(list []*mail.Address) []mailstore.Address {
	out := make([]mailstore.Address, 0, len(list))
	for _, addr := range list {
		if addr == nil || strings.TrimSpace(addr.Address) == "" {
			continue
		}
		out = append(out, mailstore.Address{
			Name:    strings.TrimSpace(addr.Name),
			Address: strings.TrimSpace(addr.Address),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// DeriveBcc recovers the blind-copied recipients.
//
// Bcc is never written into the headers -- that is the point of it -- so the
// only trace is the difference between who the SMTP conversation named and
// who the headers admit to. Anyone in the first set and not the second was
// blind-copied.
func DeriveBcc(envelope mailstore.Envelope, to, cc []mailstore.Address) []mailstore.Address {
	if len(envelope.Recipients) == 0 {
		return nil
	}

	named := make(map[string]struct{}, len(to)+len(cc))
	for _, group := range [][]mailstore.Address{to, cc} {
		for _, addr := range group {
			named[strings.ToLower(addr.Address)] = struct{}{}
		}
	}

	var bcc []mailstore.Address
	seen := make(map[string]struct{}, len(envelope.Recipients))

	for _, recipient := range envelope.Recipients {
		key := strings.ToLower(strings.TrimSpace(recipient))
		if key == "" {
			continue
		}
		if _, header := named[key]; header {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		bcc = append(bcc, mailstore.Address{Address: strings.TrimSpace(recipient)})
	}

	return bcc
}
