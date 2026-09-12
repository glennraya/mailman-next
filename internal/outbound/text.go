package outbound

import (
	"html"
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

// TextToHTML renders a plain-text body as the HTML alternative.
//
// The mirror of HTMLToText, and needed for the same reason in reverse: a
// receiving application may read only the HTML part. Real mail clients send
// multipart/alternative for anything a person typed, so a reply carrying only
// text/plain is the outlier -- and an app that stores the HTML body would
// record an empty message for it, which looks like the reply arrived blank.
//
// The text is escaped before any markup is added. A reply is typed by a
// person, so it is untrusted input as far as the receiving app's stored HTML
// is concerned, and "<b>" in a message must arrive as those five characters
// rather than as emphasis.
func TextToHTML(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}

	var paragraphs []string
	for _, block := range strings.Split(collapseBlankLines(text), "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}

		lines := strings.Split(strings.TrimRight(block, "\n"), "\n")
		for i, line := range lines {
			lines[i] = html.EscapeString(line)
		}
		paragraphs = append(paragraphs, "<p>"+strings.Join(lines, "<br>")+"</p>")
	}

	return strings.Join(paragraphs, "\n")
}
