// Package conversation decides which messages belong together.
//
// The rules are the ones mail clients converged on. Threading headers are
// authoritative when present; subject matching is a fallback and is
// deliberately restricted, because transactional mail reuses subjects
// endlessly and merging two unrelated "Your receipt" messages is worse than
// leaving a genuine reply on its own.
package conversation

import (
	"regexp"
	"strings"
)

// replyPrefix matches a single leading reply or forward marker, including the
// counted form some clients emit (Re[2]:) and the German and Scandinavian
// spellings, which turn up whenever a localized app sends the mail.
var replyPrefix = regexp.MustCompile(`(?i)^\s*(re|fwd?|aw|sv|vs)(\[\d+\])?\s*:`)

// replyPrefixRun matches a whole chain of them, so "Re: Fwd: Re: Hello"
// collapses in one pass.
var replyPrefixRun = regexp.MustCompile(`(?i)^(\s*(re|fwd?|aw|sv|vs)(\[\d+\])?\s*:\s*)+`)

// HasReplyPrefix reports whether a subject looks like a reply or forward.
// This is the gate on subject-based grouping: without it, every message
// sharing a subject would collapse into one conversation.
func HasReplyPrefix(subject string) bool {
	return replyPrefix.MatchString(subject)
}

// StripReplyPrefix removes every leading reply or forward marker.
func StripReplyPrefix(subject string) string {
	return strings.TrimSpace(replyPrefixRun.ReplaceAllString(subject, ""))
}

// Key normalizes a subject for grouping: prefixes removed, whitespace
// collapsed, lowercased. The second result is false when nothing usable is
// left, which is the caller's signal not to group by subject at all.
func Key(subject string) (string, bool) {
	stripped := StripReplyPrefix(subject)
	stripped = strings.Join(strings.Fields(stripped), " ")
	if stripped == "" {
		return "", false
	}
	return strings.ToLower(stripped), true
}

// Ancestors returns the Message-IDs a message claims to descend from, most
// distant first, with In-Reply-To last because it is the immediate parent and
// therefore the strongest signal. Duplicates and blanks are dropped, and the
// angle brackets that RFC 5322 wraps these in are stripped so the values
// compare directly against stored IDs.
func Ancestors(inReplyTo string, references []string) []string {
	seen := make(map[string]struct{}, len(references)+1)
	out := make([]string, 0, len(references)+1)

	add := func(id string) {
		id = Unbracket(id)
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	for _, ref := range references {
		add(ref)
	}
	add(inReplyTo)

	return out
}

// ParseReferences splits a raw References header into individual Message-IDs.
// The header is whitespace-separated in theory; in practice clients also use
// commas, so both are treated as separators.
func ParseReferences(header string) []string {
	fields := strings.FieldsFunc(header, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n' || r == ','
	})

	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if id := Unbracket(field); id != "" {
			out = append(out, id)
		}
	}
	return out
}

// Unbracket strips the surrounding angle brackets from a Message-ID.
func Unbracket(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	id = strings.TrimSuffix(id, ">")
	return strings.TrimSpace(id)
}

// Bracket puts them back, for writing a header.
func Bracket(id string) string {
	if id = Unbracket(id); id == "" {
		return ""
	}
	return "<" + id + ">"
}

// ReplySubject prefixes a subject with "Re:" unless it already carries a
// reply marker, so a long exchange does not accumulate "Re: Re: Re:".
func ReplySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re:"
	}
	if HasReplyPrefix(subject) {
		return subject
	}
	return "Re: " + subject
}
