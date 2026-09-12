package outbound

import (
	"strings"
	"testing"
)

func TestTextToHTML(t *testing.T) {
	cases := map[string]string{
		"Hello.":               "<p>Hello.</p>",
		"One\ntwo":             "<p>One<br>two</p>",
		"First.\n\nSecond.":    "<p>First.</p>\n<p>Second.</p>",
		"Windows\r\nnewlines":  "<p>Windows<br>newlines</p>",
		"Collapse\n\n\n\ngaps": "<p>Collapse</p>\n<p>gaps</p>",
		"":                     "",
		"   \n  ":              "",
	}

	for text, want := range cases {
		if got := TextToHTML(text); got != want {
			t.Errorf("TextToHTML(%q) = %q, want %q", text, got, want)
		}
	}
}

// The reply is typed by a person and ends up stored as HTML by the receiving
// application. Markup in it must arrive as characters, not as markup.
func TestTextToHTMLEscapes(t *testing.T) {
	got := TextToHTML(`Use <b>bold</b> & "quotes" <script>alert(1)</script>`)

	for _, unwanted := range []string{"<b>", "<script>", "</script>"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("%q survived escaping: %s", unwanted, got)
		}
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("script tag was not escaped: %s", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Errorf("ampersand was not escaped: %s", got)
	}
}

// The two directions have to agree: text through HTML and back is still the
// same message.
func TestTextToHTMLRoundTrips(t *testing.T) {
	original := "Please cancel this order.\n\nThanks,\nAda"

	if back := HTMLToText(TextToHTML(original)); !strings.Contains(back, "Please cancel this order.") ||
		!strings.Contains(back, "Ada") {
		t.Errorf("round trip lost content: %q", back)
	}
}
