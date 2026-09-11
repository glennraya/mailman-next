package main

import (
	"strings"
	"testing"
)

// Adding subcommands must not cost the flags-only form anything. Every one of
// these has shipped in the README, so any of them reaching the dispatch table
// instead of serve would be a break.
func TestSubcommandLeavesTheFlagsOnlyFormAlone(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no arguments at all", args: nil, want: ""},
		{name: "a single-dash flag", args: []string{"-v"}, want: ""},
		{name: "a double-dash flag", args: []string{"--http", ":8383"}, want: ""},
		{name: "a flag with an inline value", args: []string{"-http=:8383"}, want: ""},
		{name: "a subcommand", args: []string{"service", "status"}, want: "service"},
		{name: "the explicit serve command", args: []string{"serve", "-v"}, want: "serve"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subcommand(tt.args); got != tt.want {
				t.Errorf("subcommand(%q) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunRejectsAnUnknownCommand(t *testing.T) {
	err := run([]string{"nonsense"})
	if err == nil {
		t.Fatal("an unknown command was accepted")
	}
	// A user who mistypes needs to be told where the list is.
	if !strings.Contains(err.Error(), "mailman help") {
		t.Errorf("error %q does not point at the help", err)
	}
}

func TestRunRejectsServiceWithNoCommand(t *testing.T) {
	if err := run([]string{"service"}); err == nil {
		t.Error("`mailman service` with no command was accepted")
	}
}
