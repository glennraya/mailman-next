package httpapi

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"

	"github.com/glennraya/mailman/internal/mailstore"
)

// getMessageHTML renders a message body for display in an iframe.
//
// The body was written by whatever sent it and must be treated as hostile.
// Two things keep it contained: the Content-Security-Policy set here, which
// forbids scripts outright, and the iframe's own sandbox attribute on the
// client, which withholds allow-scripts. Either alone would be enough; both
// together mean a mistake in one is not a hole.
func (s *Server) getMessageHTML(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.GetMessage(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Remote images load by default: seeing a template the way its recipients
	// will is most of the reason to open it here. The per-message toggle sends
	// images=0 to put the block back, which is what a captured message
	// carrying a tracking pixel deserves -- fetching one would tell its sender
	// a developer read it. images=1 still means allow, so anything already
	// pointing at this endpoint keeps working.
	blockRemoteImages := r.URL.Query().Get("images") == "0"

	body := renderBody(message)
	body = rewriteInlineImages(body, message.Attachments)

	imagePolicy := "img-src * data:"
	if blockRemoteImages {
		imagePolicy = "img-src 'self' data:"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", strings.Join([]string{
		"default-src 'none'",
		"style-src 'unsafe-inline'",
		"font-src data:",
		imagePolicy,
	}, "; "))

	fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><base target="_blank">
<style>
 body { font: 15px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
        margin: 0; padding: 16px; color: #18181b; background: #fff;
        word-break: break-word; }
 pre  { white-space: pre-wrap; font: 13px/1.6 ui-monospace, SFMono-Regular, Menlo, monospace; margin: 0; }
 img  { max-width: 100%%; height: auto; }
 blockquote { margin: 0 0 0 8px; padding-left: 12px; border-left: 2px solid #e4e4e7; color: #52525b; }
 @media (prefers-color-scheme: dark) {
   body { color: #fafafa; background: #09090b; }
   blockquote { border-color: #27272a; color: #a1a1aa; }
 }
</style></head><body>%s</body></html>`, body)
}

// renderBody picks the HTML body, falling back to the plain text one wrapped
// so its formatting survives. Escaping the text is what stops a text/plain
// message full of markup from becoming live HTML.
func renderBody(message *mailstore.Message) string {
	if strings.TrimSpace(message.HTMLBody) != "" {
		return message.HTMLBody
	}
	if strings.TrimSpace(message.TextBody) != "" {
		return "<pre>" + html.EscapeString(message.TextBody) + "</pre>"
	}
	return `<p style="color:#71717a">This message has no body.</p>`
}

var cidPattern = regexp.MustCompile(`(?i)cid:([^"'\s>)]+)`)

// rewriteInlineImages points cid: references at the attachment endpoint, so
// embedded images resolve without the CSP having to allow anything external.
func rewriteInlineImages(body string, attachments []mailstore.Attachment) string {
	if len(attachments) == 0 || !strings.Contains(strings.ToLower(body), "cid:") {
		return body
	}

	byContentID := make(map[string]int64, len(attachments))
	for _, attachment := range attachments {
		if attachment.ContentID != "" {
			byContentID[strings.ToLower(attachment.ContentID)] = attachment.ID
		}
	}

	return cidPattern.ReplaceAllStringFunc(body, func(match string) string {
		contentID := strings.ToLower(strings.TrimPrefix(strings.ToLower(match), "cid:"))
		id, ok := byContentID[contentID]
		if !ok {
			// An unresolved reference is left alone. The CSP will block it,
			// which is the right outcome for a part that is not here.
			return match
		}
		return fmt.Sprintf("/api/v1/attachments/%d?disposition=inline", id)
	})
}
