package webhook

import (
	"bytes"
	"strings"
)

// HeaderPairs returns a message's headers in the order they appear.
//
// Both Mailgun and Postmark promise ordered headers, and neither
// net/mail.Header nor textproto.MIMEHeader can keep that promise -- they are
// maps, so they lose order and collapse repeated names like Received. The
// header block is therefore walked directly.
func HeaderPairs(raw []byte) [][2]string {
	block := headerBlock(raw)
	if len(block) == 0 {
		return nil
	}

	var out [][2]string

	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}

		// A line starting with whitespace continues the previous header.
		// Unfolding joins it back on with a single space, which is what
		// RFC 5322 says the value means.
		if line[0] == ' ' || line[0] == '\t' {
			if len(out) == 0 {
				continue
			}
			last := len(out) - 1
			out[last][1] = strings.TrimSpace(out[last][1] + " " + strings.TrimSpace(line))
			continue
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			continue
		}
		out = append(out, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
	}

	return out
}

// headerBlock is everything before the first blank line. A message with no
// blank line at all is all headers, which is what a bodiless message is.
func headerBlock(raw []byte) []byte {
	for _, separator := range [][]byte{[]byte("\r\n\r\n"), []byte("\n\n")} {
		if index := bytes.Index(raw, separator); index >= 0 {
			return raw[:index]
		}
	}
	return raw
}
