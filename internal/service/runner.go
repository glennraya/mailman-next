package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs the one external command each platform's service manager is
// only reachable through: launchctl, systemctl.
//
// It is an interface for one reason. Registering a login item is the kind of
// side effect a test must never perform on the machine running it, and a
// fake Runner is the seam that makes the whole package testable without any
// build tags -- a Linux CI box can exercise the launchd command sequence and
// vice versa.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// execRunner is the only implementation that touches the machine. It is
// constructed in New and nowhere else.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	// CombinedOutput because launchctl and systemctl both put their most
	// useful diagnostics on stderr, and a service failure the user cannot
	// read is worse than no failure message at all.
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}

	return text, nil
}
