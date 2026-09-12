package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/mailstore"
)

// reply is the message the format tests render. It carries the things that
// have historically been got wrong: a display name, a plus-addressed
// recipient, a quoted conversation below the reply, and a signature block.
func reply() (*mailstore.Message, []byte) {
	m := &mailstore.Message{
		ID:             "01jabcdefghijklmnopqrstuvw",
		ConversationID: 7,
		Direction:      mailstore.DirectionOutbound,
		MessageID:      "01jreply@mailman.local",
		InReplyTo:      "original@myapp.test",
		References:     []string{"original@myapp.test"},
		From:           []mailstore.Address{{Name: "Ada Lovelace", Address: "ada@example.test"}},
		To:             []mailstore.Address{{Name: "Support", Address: "order-4471+t3h2@mail.myapp.test"}},
		Cc:             []mailstore.Address{{Address: "watcher@example.test"}},
		Subject:        "Re: Your receipt",
		SentAt:         time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC),
		TextBody: "Please cancel this order.\n" +
			"\n" +
			"--\n" +
			"Ada\n" +
			"\n" +
			"On Fri, 11 Sep 2026 at 09:00, Support wrote:\n" +
			"> Here is your receipt.\n",
		HTMLBody: "<p>Please cancel this order.</p>",
		Envelope: mailstore.Envelope{
			From:       "ada@example.test",
			Recipients: []string{"order-4471+t3h2@mail.myapp.test", "watcher@example.test"},
		},
		CreatedAt: time.Date(2026, 9, 12, 10, 30, 0, 0, time.UTC),
	}

	raw := []byte("Message-ID: <01jreply@mailman.local>\r\n" +
		"In-Reply-To: <original@myapp.test>\r\n" +
		"From: \"Ada Lovelace\" <ada@example.test>\r\n" +
		"Subject: Re: Your\r\n\t receipt\r\n" +
		"\r\n" +
		"Please cancel this order.\r\n")

	return m, raw
}

// mustBuild builds or fails the test, for cases that only care about the result.
func mustBuild(t *testing.T, m *mailstore.Message, raw []byte, r config.Resolved) *Payload {
	t.Helper()

	payload, err := Build(m, raw, r)
	if err != nil {
		t.Fatalf("build %s: %v", r.Format, err)
	}
	return payload
}

func route(format, key string) config.Resolved {
	return config.Resolved{
		URL:        "http://myapp.test/inbound",
		Format:     format,
		SigningKey: key,
		Timeout:    5 * time.Second,
		VerifyTLS:  true,
	}
}

func TestBuildRejectsAnUnknownFormat(t *testing.T) {
	m, raw := reply()

	if _, err := Build(m, raw, route("carrier-pigeon", "")); err == nil {
		t.Fatal("an unknown format built a payload")
	}
}

func TestGenericPayload(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatGeneric, "s3cret"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if payload.ContentType != "application/json" {
		t.Errorf("content type = %q", payload.ContentType)
	}

	var body struct {
		MessageID    string   `json:"message_id"`
		InReplyTo    string   `json:"in_reply_to"`
		From         string   `json:"from"`
		To           []string `json:"to"`
		Recipient    string   `json:"recipient"`
		Sender       string   `json:"sender"`
		Subject      string   `json:"subject"`
		Text         string   `json:"text"`
		HTML         string   `json:"html"`
		StrippedText string   `json:"stripped_text"`
		Raw          string   `json:"raw"`
		Headers      [][2]string
	}
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.MessageID != "01jreply@mailman.local" {
		t.Errorf("message_id = %q", body.MessageID)
	}
	if body.InReplyTo != "original@myapp.test" {
		t.Errorf("in_reply_to = %q", body.InReplyTo)
	}
	if body.From != `"Ada Lovelace" <ada@example.test>` {
		t.Errorf("from = %q", body.From)
	}
	if len(body.To) != 1 || body.To[0] != "order-4471+t3h2@mail.myapp.test" {
		t.Errorf("to = %v", body.To)
	}
	if body.Recipient != "order-4471+t3h2@mail.myapp.test" {
		t.Errorf("recipient = %q, want the first envelope recipient", body.Recipient)
	}
	if body.Sender != "ada@example.test" {
		t.Errorf("sender = %q", body.Sender)
	}
	if !strings.Contains(body.Text, "> Here is your receipt.") {
		t.Error("text lost the quoted conversation, which it should carry verbatim")
	}
	// Mailman's own shape strips the quoted conversation and leaves the
	// signature, since it has no separate field for one.
	if body.StrippedText != "Please cancel this order.\n\n--\nAda" {
		t.Errorf("stripped_text = %q, want the quote gone", body.StrippedText)
	}
	if body.Raw != string(raw) {
		t.Error("raw is not the verbatim source")
	}

	// Signed over the exact bytes sent, so a handler can detect a change.
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(payload.Body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if got := payload.Header.Get("X-Mailman-Signature"); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if payload.Header.Get("X-Mailman-Timestamp") == "" {
		t.Error("no timestamp header")
	}
}

func TestGenericPayloadIsUnsignedWithoutAKey(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatGeneric, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := payload.Header.Get("X-Mailman-Signature"); got != "" {
		t.Errorf("signature = %q, want none with no signing key", got)
	}
}

// parseMultipart reads a built Mailgun payload back into its fields.
func parseMultipart(t *testing.T, payload *Payload) map[string]string {
	t.Helper()

	_, params, err := mime.ParseMediaType(payload.ContentType)
	if err != nil {
		t.Fatalf("parse content type %q: %v", payload.ContentType, err)
	}

	reader := multipart.NewReader(strings.NewReader(string(payload.Body)), params["boundary"])
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		t.Fatalf("read form: %v", err)
	}

	fields := make(map[string]string, len(form.Value))
	for name, values := range form.Value {
		fields[name] = values[0]
	}
	return fields
}

func TestMailgunPayload(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatMailgun, "key-abc123"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.HasPrefix(payload.ContentType, "multipart/form-data") {
		t.Fatalf("content type = %q, want multipart/form-data", payload.ContentType)
	}

	fields := parseMultipart(t, payload)

	want := map[string]string{
		"recipient":          "order-4471+t3h2@mail.myapp.test",
		"sender":             "ada@example.test",
		"from":               `"Ada Lovelace" <ada@example.test>`,
		"subject":            "Re: Your receipt",
		"stripped-text":      "Please cancel this order.",
		"stripped-signature": "Ada",
		"Message-Id":         "<01jreply@mailman.local>",
		"attachment-count":   "0",
		"body-html":          "<p>Please cancel this order.</p>",
	}
	for name, value := range want {
		if fields[name] != value {
			t.Errorf("%s = %q, want %q", name, fields[name], value)
		}
	}

	// body-plain is documented as always present and carries the whole body.
	if !strings.Contains(fields["body-plain"], "> Here is your receipt.") {
		t.Errorf("body-plain = %q, want the verbatim body", fields["body-plain"])
	}

	// Recompute the signature the way a handler would: the hex HMAC-SHA256 of
	// the timestamp concatenated with the token, keyed by the signing key.
	mac := hmac.New(sha256.New, []byte("key-abc123"))
	mac.Write([]byte(fields["timestamp"] + fields["token"]))

	if got, want := fields["signature"], hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if len(fields["token"]) != 50 {
		t.Errorf("token is %d characters, want 50", len(fields["token"]))
	}
	if fields["timestamp"] == "" {
		t.Error("no timestamp field")
	}

	// message-headers is a JSON list of ordered pairs, and the order is the
	// reason it is built from the raw block rather than a header map.
	var headers [][2]string
	if err := json.Unmarshal([]byte(fields["message-headers"]), &headers); err != nil {
		t.Fatalf("decode message-headers: %v", err)
	}
	if len(headers) != 4 || headers[0][0] != "Message-ID" || headers[2][0] != "From" {
		t.Errorf("message-headers = %v, want source order", headers)
	}
	if headers[3][1] != "Re: Your receipt" {
		t.Errorf("folded header = %q, want it unfolded", headers[3][1])
	}
}

// Mailgun posts every MIME header as a field of its own, alongside the
// message-headers JSON. Real handlers gate on those fields directly -- an
// `isset($data['In-Reply-To'])` check that fails drops the reply instead of
// threading it -- so omitting them breaks the exact promise of reproducing
// Mailgun's shape.
func TestMailgunPayloadPostsEachHeaderAsItsOwnField(t *testing.T) {
	m, raw := reply()

	fields := parseMultipart(t, mustBuild(t, m, raw, route(config.FormatMailgun, "key-abc123")))

	want := map[string]string{
		"In-Reply-To": "<original@myapp.test>",
		"Message-Id":  "<01jreply@mailman.local>",
		"From":        `"Ada Lovelace" <ada@example.test>`,
		// Folded in the source, so this also proves headers arrive unfolded.
		"Subject": "Re: Your receipt",
		// Mailgun stamps this itself on anything it received.
		"X-Mailgun-Incoming": "Yes",
		"domain":             "mail.myapp.test",
	}
	for name, value := range want {
		if fields[name] != value {
			t.Errorf("%s = %q, want %q", name, fields[name], value)
		}
	}

	// The raw header and Mailgun's parse of it are different fields carrying
	// different things, and both have to survive.
	if fields["from"] != fields["From"] {
		t.Errorf("from = %q but From = %q", fields["from"], fields["From"])
	}
	if fields["subject"] == "" || fields["Subject"] == "" {
		t.Error("the lower-case parsed field and the raw header must both be present")
	}
}

// A message carrying the header Mailgun stamps itself must not produce two
// fields of the same name.
func TestMailgunDoesNotDuplicateItsOwnIncomingHeader(t *testing.T) {
	m, _ := reply()
	raw := []byte("X-Mailgun-Incoming: No\r\nSubject: Spoofed\r\n\r\nbody\r\n")

	payload, err := Build(m, raw, route(config.FormatMailgun, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got := strings.Count(string(payload.Body), `name="X-Mailgun-Incoming"`); got != 1 {
		t.Errorf("X-Mailgun-Incoming appears %d times, want 1", got)
	}
	if fields := parseMultipart(t, payload); fields["X-Mailgun-Incoming"] != "Yes" {
		t.Errorf("X-Mailgun-Incoming = %q, want Mailman's own value to win",
			fields["X-Mailgun-Incoming"])
	}
}

func TestMailgunPayloadIsUnsignedWithoutAKey(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatMailgun, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	fields := parseMultipart(t, payload)
	if _, signed := fields["signature"]; signed {
		t.Error("a signature was sent with no signing key to compute it from")
	}
	if fields["token"] == "" || fields["timestamp"] == "" {
		t.Error("token and timestamp should travel even unsigned")
	}
}

func TestPostmarkPayload(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatPostmark, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if payload.ContentType != "application/json" {
		t.Errorf("content type = %q", payload.ContentType)
	}

	var body postmarkPayload
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if body.MessageStream != "inbound" {
		t.Errorf("MessageStream = %q", body.MessageStream)
	}
	if body.FromName != "Ada Lovelace" || body.From != "ada@example.test" {
		t.Errorf("From = %q / %q", body.FromName, body.From)
	}
	if body.FromFull.Email != "ada@example.test" || body.FromFull.Name != "Ada Lovelace" {
		t.Errorf("FromFull = %+v", body.FromFull)
	}
	if body.To != `"Support" <order-4471+t3h2@mail.myapp.test>` {
		t.Errorf("To = %q", body.To)
	}
	if len(body.ToFull) != 1 || body.ToFull[0].MailboxHash != "t3h2" {
		t.Errorf("ToFull = %+v", body.ToFull)
	}
	if body.Cc != "watcher@example.test" || len(body.CcFull) != 1 {
		t.Errorf("Cc = %q / %+v", body.Cc, body.CcFull)
	}
	if body.OriginalRecipient != "order-4471+t3h2@mail.myapp.test" {
		t.Errorf("OriginalRecipient = %q", body.OriginalRecipient)
	}
	// The plus-addressed record reference is the whole point of inbound
	// parsing for most apps.
	if body.MailboxHash != "t3h2" {
		t.Errorf("MailboxHash = %q, want %q", body.MailboxHash, "t3h2")
	}
	// MessageID is Postmark's own identifier. The RFC Message-ID belongs in
	// Headers, and a handler storing MessageID as a provider reference would
	// break if these were swapped.
	if body.MessageID == "01jreply@mailman.local" {
		t.Error("MessageID is the RFC Message-ID, want a Postmark-style UUID")
	}
	if len(body.MessageID) != 36 || strings.Count(body.MessageID, "-") != 4 {
		t.Errorf("MessageID = %q, want a UUID", body.MessageID)
	}
	if body.Date != "Sat, 12 Sep 2026 10:30:00 +0000" {
		t.Errorf("Date = %q", body.Date)
	}
	// Postmark has no signature field, so StrippedTextReply drops the
	// quoted conversation only.
	if body.StrippedTextReply != "Please cancel this order.\n\n--\nAda" {
		t.Errorf("StrippedTextReply = %q", body.StrippedTextReply)
	}
	if body.HtmlBody != "<p>Please cancel this order.</p>" {
		t.Errorf("HtmlBody = %q", body.HtmlBody)
	}
	// Headers keeps what Postmark does not promote, and drops what it does.
	if len(body.Headers) != 2 || body.Headers[0].Name != "Message-ID" {
		t.Errorf("Headers = %+v, want only the unpromoted ones", body.Headers)
	}
	for _, header := range body.Headers {
		if postmarkPromoted[header.Name] {
			t.Errorf("Headers repeats the promoted %q", header.Name)
		}
	}
	if body.Headers[0].Value != "<01jreply@mailman.local>" {
		t.Errorf("Message-ID header = %q, want the RFC value", body.Headers[0].Value)
	}

	// Always an array, never null: handlers index it without checking.
	if !strings.Contains(string(payload.Body), `"Attachments":[]`) {
		t.Error("Attachments should be an empty array")
	}
}

// Postmark signs nothing, so a key configured for a Postmark route can only
// be Basic credentials -- the way Postmark's own inbound URLs are secured.
func TestPostmarkUsesBasicAuthForASigningKey(t *testing.T) {
	m, raw := reply()

	payload, err := Build(m, raw, route(config.FormatPostmark, "inbound-user"))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if payload.BasicAuthUser != "inbound-user" {
		t.Errorf("basic auth user = %q", payload.BasicAuthUser)
	}
}

// An HTML-only reply must still arrive with a text body: every format
// documents its text field as always present, and a handler reading only that
// field would otherwise see an empty message.
func TestTextBodyIsGeneratedFromHTML(t *testing.T) {
	m, raw := reply()
	m.TextBody = ""
	m.HTMLBody = "<p>Cancel it, please.</p>"

	payload, err := Build(m, raw, route(config.FormatPostmark, ""))
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	var body postmarkPayload
	if err := json.Unmarshal(payload.Body, &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(body.TextBody, "Cancel it, please.") {
		t.Errorf("TextBody = %q, want it derived from the HTML", body.TextBody)
	}
}

func TestMailboxHash(t *testing.T) {
	cases := map[string]string{
		"order-4471+t3h2@mail.myapp.test": "t3h2",
		"plain@myapp.test":                "",
		"a+b+c@myapp.test":                "b+c",
		"bare+hash":                       "hash",
		"":                                "",
	}

	for address, want := range cases {
		if got := MailboxHash(address); got != want {
			t.Errorf("MailboxHash(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestHeaderPairsUnfoldsAndKeepsOrder(t *testing.T) {
	raw := []byte("Received: from one\r\nReceived: from two\r\n" +
		"Subject: a very\r\n  long subject\r\n\r\nbody\r\n")

	pairs := HeaderPairs(raw)

	if len(pairs) != 3 {
		t.Fatalf("got %d headers, want 3: %v", len(pairs), pairs)
	}
	// Repeated names survive, which a header map would collapse.
	if pairs[0][1] != "from one" || pairs[1][1] != "from two" {
		t.Errorf("repeated Received collapsed: %v", pairs)
	}
	if pairs[2][1] != "a very long subject" {
		t.Errorf("folded value = %q, want it unfolded", pairs[2][1])
	}
}

func TestStripQuoted(t *testing.T) {
	cases := map[string]string{
		"Reply.\n\nOn Mon, 1 Jan 2026 at 09:00, A wrote:\n> Original.": "Reply.",
		"Reply.\n> Original.":                             "Reply.",
		"Reply.\n\n-----Original Message-----\nOriginal.": "Reply.",
		"Nothing quoted here.":                            "Nothing quoted here.",
		"":                                                "",
	}

	for body, want := range cases {
		if got := StripQuoted(body); got != want {
			t.Errorf("StripQuoted(%q) = %q, want %q", body, got, want)
		}
	}
}

func TestStripSignature(t *testing.T) {
	body, signature := StripSignature("Reply.\n\n--\nAda\nAnalyst")

	if body != "Reply." {
		t.Errorf("body = %q", body)
	}
	if signature != "Ada\nAnalyst" {
		t.Errorf("signature = %q", signature)
	}

	body, signature = StripSignature("No signature.")
	if body != "No signature." || signature != "" {
		t.Errorf("body = %q, signature = %q", body, signature)
	}
}

// Payload.Header must never be nil for the sender, which copies it straight
// onto the request.
func TestEveryFormatReturnsUsableHeaders(t *testing.T) {
	m, raw := reply()

	for _, format := range []string{config.FormatGeneric, config.FormatMailgun, config.FormatPostmark} {
		payload, err := Build(m, raw, route(format, "key"))
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		var header http.Header = payload.Header
		for name := range header {
			if name == "" {
				t.Errorf("%s: empty header name", format)
			}
		}
	}
}
