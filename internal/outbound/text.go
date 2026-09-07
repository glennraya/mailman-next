package outbound

import (
	"regexp"
	"strings"

	"github.com/inbucket/html2text"
)

// HTMLToText renders an HTML body as plain text for the text/plain
// alternative.
//
// The alternative is not decoration: plenty of inbound parsers and mail
// clients read only text/plain, and an HTML-only reply arrives blank in them.
// Getting this wrong would look like the reply never had a body.
func HTMLToText(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}

	text, err := html2text.FromString(body, html2text.Options{OmitLinks: false})
	if err != nil {
		// Falling back to a tag strip is worse output than html2text gives,
		// but it is still the message rather than nothing.
		return collapseBlankLines(stripTags(body))
	}
	return collapseBlankLines(text)
}

var tagPattern = regexp.MustCompile(`(?s)<[^>]*>`)

func stripTags(body string) string {
	body = strings.ReplaceAll(body, "<br>", "\n")
	body = strings.ReplaceAll(body, "<br/>", "\n")
	body = strings.ReplaceAll(body, "<br />", "\n")
	body = strings.ReplaceAll(body, "</p>", "\n\n")
	return strings.TrimSpace(tagPattern.ReplaceAllString(body, ""))
}

var blankLines = regexp.MustCompile(`\n{3,}`)

func collapseBlankLines(text string) string {
	return strings.TrimSpace(blankLines.ReplaceAllString(text, "\n\n"))
}
