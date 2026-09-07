package main

import (
	"net"
	"strings"
	"testing"
)

func TestEnsureAvailableAllowsAFreePort(t *testing.T) {
	// Bind and release, so the address is known to have been usable and is
	// now free again.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	if err := ensureAvailable("SMTP", addr); err != nil {
		t.Errorf("a free port was reported busy: %v", err)
	}
}

// TestEnsureAvailableRejectsAnOccupiedPort is the check that a plain bind
// cannot make. On macOS a listener on 127.0.0.1:1983 binds happily next to
// another process holding *:1983, so both processes start and mail goes to
// whichever the sender's resolver happened to reach. Dialing catches it.
func TestEnsureAvailableRejectsAnOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	err = ensureAvailable("SMTP", listener.Addr().String())
	if err == nil {
		t.Fatal("an occupied port was reported available")
	}

	// The message has to name the port and the way out, since this is the
	// first thing a new user hits when they already run Mailpit.
	for _, want := range []string{listener.Addr().String(), "Mailpit", "-smtp"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A wildcard listener is the exact shape Mailpit uses, and the one a
// loopback bind fails to conflict with.
func TestEnsureAvailableSeesAWildcardListener(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	if err := ensureAvailable("SMTP", "127.0.0.1:"+port); err == nil {
		t.Error("a wildcard listener on the same port was not detected")
	}
}

func TestEnsureAvailableSkipsEphemeralPorts(t *testing.T) {
	// Port 0 means "any free port", so there is nothing to probe. The
	// end-to-end tests rely on this.
	if err := ensureAvailable("SMTP", "127.0.0.1:0"); err != nil {
		t.Errorf("port 0 was probed: %v", err)
	}
}

func TestEnsureAvailableRejectsAMalformedAddress(t *testing.T) {
	if err := ensureAvailable("SMTP", "not-an-address"); err == nil {
		t.Error("a malformed address was accepted")
	}
}
