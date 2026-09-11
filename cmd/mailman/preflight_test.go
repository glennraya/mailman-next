package main

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestPreflightAllowsAFreePort(t *testing.T) {
	// Bind and release, so the address is known to have been usable and is
	// now free again.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	if err := preflight(context.Background(), "SMTP", addr, false); err != nil {
		t.Errorf("a free port was reported busy: %v", err)
	}
}

// TestPreflightRejectsAnOccupiedPort is the check that a plain bind cannot
// make. On macOS a listener on 127.0.0.1:1983 binds happily next to another
// process holding *:1983, so both processes start and mail goes to whichever
// the sender's resolver happened to reach. Dialing catches it.
func TestPreflightRejectsAnOccupiedPort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	err = preflight(context.Background(), "SMTP", listener.Addr().String(), false)
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
func TestPreflightSeesAWildcardListener(t *testing.T) {
	listener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	if err := preflight(context.Background(), "SMTP", "127.0.0.1:"+port, false); err == nil {
		t.Error("a wildcard listener on the same port was not detected")
	}
}

func TestPreflightSkipsEphemeralPorts(t *testing.T) {
	// Port 0 means "any free port", so there is nothing to probe. The
	// end-to-end tests rely on this.
	if err := preflight(context.Background(), "SMTP", "127.0.0.1:0", false); err != nil {
		t.Errorf("port 0 was probed: %v", err)
	}
}

func TestPreflightRejectsAMalformedAddress(t *testing.T) {
	if err := preflight(context.Background(), "SMTP", "not-an-address", false); err == nil {
		t.Error("a malformed address was accepted")
	}
}

// A Mailman that is already running deserves a different sentence from a
// squatter: there is nothing wrong, and telling the user to stop Mailpit
// would send them looking for a process that does not exist.
func TestPreflightNamesAnotherMailman(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("220 mailman.local ESMTP Service Ready\r\n"))
		conn.Read(make([]byte, 64))
	}()

	err = preflight(context.Background(), "SMTP", listener.Addr().String(), false)
	if err == nil {
		t.Fatal("an occupied port was reported available")
	}
	if !strings.Contains(err.Error(), "another Mailman") {
		t.Errorf("error %q does not identify the other Mailman", err)
	}
	if strings.Contains(err.Error(), "Mailpit") {
		t.Errorf("error %q blames Mailpit for another Mailman", err)
	}
}

// The supervised flag is what main reads to decide between an exit status
// that asks for a restart and one that asks to be left alone. It has to
// survive being wrapped in the error.
func TestPreflightCarriesTheSupervisedFlag(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	err = preflight(context.Background(), "HTTP", listener.Addr().String(), true)

	var conflict *portConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("error %v is not a port conflict", err)
	}
	if !conflict.supervised {
		t.Error("the supervised flag was lost")
	}
}
