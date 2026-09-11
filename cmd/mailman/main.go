// Command mailman captures mail from local development projects and lets you
// reply to it.
//
// One process runs both listeners: SMTP for capture, HTTP for the inbox, the
// JSON API and the event stream, over a single SQLite database.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	err := run(os.Args[1:])
	if err == nil {
		return
	}

	fmt.Fprintln(os.Stderr, "mailman:", err)

	// A supervised Mailman that cannot have its ports has not crashed, and
	// restarting it will not help until a human moves whatever is in the
	// way. Exiting zero is how that gets said: launchd's
	// KeepAlive/SuccessfulExit=false and systemd's Restart=on-failure both
	// read a clean exit as "leave it alone", which turns what would be a
	// restart every few seconds, forever, into one logged explanation.
	var conflict *portConflict
	if errors.As(err, &conflict) && conflict.supervised {
		os.Exit(0)
	}

	os.Exit(1)
}

// run dispatches on the first argument.
//
// A subcommand can only ever be a bare word in first position, so every
// existing invocation still reaches serve: `mailman`, `mailman -v` and
// `mailman --http=:8383` all begin with either nothing or a dash.
func run(args []string) error {
	switch subcommand(args) {
	case "":
		return serve(args)
	case "serve":
		return serve(args[1:])
	case "service":
		return serviceCommand(args[1:])
	case "help":
		usage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q -- try `mailman help`", args[0])
	}
}

// subcommand reports which command args names, or "" for the flags-only form
// that serve handles. It is separate from run so the dispatch can be tested
// without starting a server.
func subcommand(args []string) string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return ""
	}

	return args[0]
}

func usage(w io.Writer) {
	fmt.Fprint(w, `Mailman captures mail from local projects and lets you reply to it.

Usage:
  mailman [flags]              capture mail and serve the inbox
  mailman service <command>    run Mailman at login, in the background
  mailman help                 this message

Flags:
  -http addr        address for the inbox, API and event stream
  -smtp addr        address for the SMTP capture server
  -home dir         data directory (default ~/.mailman)
  -max-size bytes   largest message to accept
  -v                log every request
  -version          print the version and exit

Service commands:
  install    register Mailman to start at login, and start it now
  uninstall  stop Mailman and remove the login registration
  status     what is registered, what is running, what is answering
  start      start the registered service
  stop       stop it, leaving it registered

Run "mailman service <command> -h" for the flags each one takes.
`)
}
