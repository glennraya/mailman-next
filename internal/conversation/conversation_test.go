package conversation

import (
	"reflect"
	"testing"
)

func TestHasReplyPrefix(t *testing.T) {
	cases := map[string]bool{
		"Re: Order shipped":   true,
		"RE:Order shipped":    true,
		"re : Order shipped":  true,
		"Fwd: Order shipped":  true,
		"Fw: Order shipped":   true,
		"Aw: Bestellung":      true,
		"Sv: Order":           true,
		"Re[2]: Order":        true,
		"  Re: leading space": true,
		"Order shipped":       false,
		"Rebate approved":     false,
		"Reminder: pay up":    false,
		"":                    false,
	}

	for subject, want := range cases {
		if got := HasReplyPrefix(subject); got != want {
			t.Errorf("HasReplyPrefix(%q) = %v, want %v", subject, got, want)
		}
	}
}

func TestStripReplyPrefix(t *testing.T) {
	cases := map[string]string{
		"Re: Order shipped":         "Order shipped",
		"Re: Fwd: Re: Order":        "Order",
		"Re[2]: Order":              "Order",
		"Order shipped":             "Order shipped",
		"Reminder: pay up":          "Reminder: pay up",
		"Re:":                       "",
		"  Fwd:  Re:  Spaced  out ": "Spaced  out",
	}

	for subject, want := range cases {
		if got := StripReplyPrefix(subject); got != want {
			t.Errorf("StripReplyPrefix(%q) = %q, want %q", subject, got, want)
		}
	}
}

func TestKey(t *testing.T) {
	got, ok := Key("Re:  Order   shipped ")
	if !ok || got != "order shipped" {
		t.Fatalf("Key() = %q, %v; want \"order shipped\", true", got, ok)
	}

	// A subject that is nothing but a reply marker carries no information,
	// so it must not become a grouping key -- otherwise every such message
	// would land in the same conversation.
	if _, ok := Key("Re:"); ok {
		t.Error("Key(\"Re:\") reported a usable key; want none")
	}
	if _, ok := Key("   "); ok {
		t.Error("Key(blank) reported a usable key; want none")
	}
}

func TestAncestors(t *testing.T) {
	got := Ancestors("<c@test>", []string{"<a@test>", "<b@test>", "<a@test>"})
	want := []string{"a@test", "b@test", "c@test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ancestors() = %v, want %v", got, want)
	}

	// In-Reply-To is the immediate parent and must come last even when it
	// already appears in References, so the closest ancestor is tried first
	// when the caller walks the list in reverse.
	got = Ancestors("<a@test>", []string{"<a@test>", "<b@test>"})
	want = []string{"a@test", "b@test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Ancestors() with duplicate parent = %v, want %v", got, want)
	}

	if got := Ancestors("", nil); len(got) != 0 {
		t.Errorf("Ancestors() with nothing = %v, want empty", got)
	}
}

func TestParseReferences(t *testing.T) {
	got := ParseReferences("<a@test>\r\n <b@test>,\t<c@test>")
	want := []string{"a@test", "b@test", "c@test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseReferences() = %v, want %v", got, want)
	}

	if got := ParseReferences("   "); len(got) != 0 {
		t.Errorf("ParseReferences(blank) = %v, want empty", got)
	}
}

func TestReplySubject(t *testing.T) {
	cases := map[string]string{
		"Order shipped":     "Re: Order shipped",
		"Re: Order shipped": "Re: Order shipped",
		"Fwd: Order":        "Fwd: Order",
		"":                  "Re:",
	}

	for subject, want := range cases {
		if got := ReplySubject(subject); got != want {
			t.Errorf("ReplySubject(%q) = %q, want %q", subject, got, want)
		}
	}
}

func TestBracketRoundTrip(t *testing.T) {
	if got := Bracket("a@test"); got != "<a@test>" {
		t.Errorf("Bracket() = %q", got)
	}
	if got := Bracket("<a@test>"); got != "<a@test>" {
		t.Errorf("Bracket() double-wrapped: %q", got)
	}
	if got := Bracket("  "); got != "" {
		t.Errorf("Bracket(blank) = %q, want empty", got)
	}
}
