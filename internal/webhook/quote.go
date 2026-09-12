package webhook

import (
	"regexp"
	"strings"
)

// attribution matches the line a mail client writes above the text it quotes.
// Only the common English and a few localized forms are here: the cost of
// missing one is a stripped body that still contains the quote, which is
// recoverable, while a false positive would truncate what the sender wrote.
var attribution = regexp.MustCompile(
	`(?i)^\s*(on\s.+\swrote:|-{2,}\s*original message\s*-{2,}|-{2,}\s*forwarded message\s*-{2,}|am\s.+\sschrieb\s.+:|le\s.+\sa\s.+écrit\s*:)\s*$`)

// signatureDelimiter is the standard "-- " sig separator from RFC 3676.
const signatureDelimiter = "--"

// StripQuoted returns the body without the quoted conversation below it.
//
// Mailgun's stripped-text and Postmark's StrippedTextReply both hold this,
// and an app that keys off either one -- matching a reply to a support ticket,
// say -- would otherwise re-import the entire thread on every reply. Replies
// are top-posted, so cutting at the first quote marker is the whole rule.
func StripQuoted(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, ">") || attribution.MatchString(line) {
			return strings.TrimRight(strings.Join(lines[:index], "\n"), " \t\n")
		}
	}

	return strings.TrimRight(text, " \t\n")
}

// StripSignature splits a body at the "-- " delimiter, returning the message
// and the signature block. Mailgun reports these separately.
func StripSignature(text string) (body, signature string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")

	for index, line := range lines {
		if strings.TrimRight(line, " \t") != signatureDelimiter {
			continue
		}
		body = strings.TrimRight(strings.Join(lines[:index], "\n"), " \t\n")
		signature = strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
		return body, signature
	}

	return strings.TrimRight(text, " \t\n"), ""
}
