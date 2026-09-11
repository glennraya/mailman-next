package main

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/glennraya/mailman/internal/probe"
)

// portConflict is the error for a port Mailman cannot have.
//
// It carries supervised because the same condition means two different things
// depending on who started the process. In a terminal it is a failure the
// user should see and fix. Under launchd or systemd it is a reason to stay
// down, and saying so with an exit status is the only way to tell a
// supervisor that restarting will not help -- see main.
type portConflict struct {
	what       string // "SMTP" or "HTTP", as the user sees it
	addr       string
	found      probe.Result
	supervised bool
}

func (c *portConflict) Error() string {
	if c.found.Owner == probe.Mailman {
		return fmt.Sprintf(
			"another Mailman is already serving %s on %s (%s). "+
				"There is nothing to do unless you meant to replace it, in which case stop that one first",
			c.what, c.addr, c.found.Detail)
	}

	if c.what == "SMTP" {
		// Another mail catcher accounts for nearly every real
		// occurrence on this port, so name the two by name rather than
		// leave the user guessing.
		return fmt.Sprintf(
			"SMTP port %s is already serving: %s. Another mail catcher (Mailpit or MailHog "+
				"listen here by default) is most likely running. Stop it, or move Mailman "+
				"with -smtp",
			c.addr, c.found.Detail)
	}

	// On the inbox port there is no such likely culprit, so quote what the
	// probe found and leave the diagnosis to the user.
	return fmt.Sprintf(
		"%s port %s is already serving: %s. Stop it, or move Mailman with -%s",
		c.what, c.addr, c.found.Detail, strings.ToLower(c.what))
}

// preflight reports an error when something is already serving addr.
//
// It dials rather than binds because a bind can succeed against an address
// another process is already answering on. On macOS a listener on
// 127.0.0.1:1983 binds cleanly alongside another process holding *:1983, so
// Mailman and a running Mailpit would both start and which one receives a
// message depends on whether the sender resolved to IPv4 or IPv6. Mail then
// vanishes into the other tool with nothing logged anywhere. Checking first
// turns that into a startup error.
//
// Having dialled, it costs one more round trip to ask what answered, which is
// the difference between "your other Mailman has it" and a guess.
func preflight(ctx context.Context, what, addr string, supervised bool) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s address %q is not host:port: %w", what, addr, err)
	}
	// Port 0 means "give me any free port", so there is nothing to check.
	if port == "0" {
		return nil
	}

	found := probe.SMTP(ctx, addr)
	if what != "SMTP" {
		found = probe.HTTP(ctx, addr)
	}
	if found.Free() {
		return nil
	}

	return &portConflict{what: what, addr: addr, found: found, supervised: supervised}
}
