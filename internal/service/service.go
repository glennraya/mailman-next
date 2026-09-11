// Package service registers Mailman with the operating system's login
// manager, so that it is already listening when a project tries to send mail
// rather than waiting for someone to open a terminal.
//
// Mailman is local infrastructure: every project's .env points at its SMTP
// port. Something that has to be started by hand is not infrastructure, it is
// a chore you forget until mail silently fails to arrive.
//
// Nothing in this package is behind a build tag. The per-platform work is
// writing a text file and running one CLI tool, neither of which needs a
// platform-specific syscall, so selection happens on Options.GOOS instead.
// The payoff is that the launchd renderer is genuinely tested on the Linux CI
// runner rather than only ever on a developer's Mac.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Label identifies Mailman to the login manager. Reverse DNS is what launchd
// expects, and it cannot collide with homebrew.mxcl.mailman, which is what
// `brew services` would install.
const Label = "com.glennraya.mailman"

// UnitName is the systemd user unit's filename.
const UnitName = "mailman.service"

// ErrUnsupported is returned on a platform with no login manager Mailman
// knows how to drive.
var ErrUnsupported = errors.New("unsupported platform")

// Options is everything a Manager needs. Every path the operating system
// would otherwise supply is a field so that a test can point the whole
// package at a temporary directory and never touch the real login items.
type Options struct {
	// GOOS selects the manager. Empty means the running platform.
	GOOS string

	// ExecPath is the binary to register. Empty means this one.
	ExecPath string

	// Args are the arguments the unit passes. Empty means -supervised
	// alone, which is what makes a busy port a clean stop rather than a
	// restart loop.
	Args []string

	// Home is Mailman's data directory, used as the working directory and
	// as the place the log file lives.
	Home string

	// UnitDir is where the plist or unit file is written. Empty means the
	// platform's own location.
	UnitDir string

	// UID owns the launchd domain. Zero means the current user.
	UID int

	// Runner runs launchctl or systemctl. Required: a nil Runner is a
	// programming error rather than something to paper over, since the
	// fallback would be running real commands during a test.
	Runner Runner
}

// Manager drives one platform's login manager.
type Manager interface {
	// Install writes the unit and registers it, so that Mailman starts now
	// and at every login.
	Install(ctx context.Context) error

	// Uninstall stops Mailman and removes the registration.
	Uninstall(ctx context.Context) error

	// Start starts an already-registered Mailman.
	Start(ctx context.Context) error

	// Stop stops it, leaving it registered.
	Stop(ctx context.Context) error

	// Status reports what the login manager believes.
	Status(ctx context.Context) (Status, error)

	// UnitPath is the file Install writes.
	UnitPath() string

	// Render returns the unit's contents without writing anything, which is
	// what makes the format testable.
	Render() ([]byte, error)

	// LogHint tells a user where this platform's output went.
	LogHint() string
}

// Status is what the login manager knows. It is deliberately separate from
// what a probe finds by connecting: a service can be registered and running
// while still waiting on a port, and the two together are what explain it.
type Status struct {
	// Manager names the mechanism, for the user rather than for code.
	Manager string

	// Installed is whether the unit file exists and is registered.
	Installed bool

	// Running is whether a process is alive right now.
	Running bool

	// PID is meaningful only when Running.
	PID int

	// LastExit is the previous run's exit status, which is how a service
	// that stopped on a port conflict explains itself.
	LastExit int

	// Conflict is non-empty when another manager is also driving Mailman.
	Conflict string

	// Note carries anything else worth printing, such as a systemd unit
	// sitting in the failed state.
	Note string
}

// New returns the Manager for opts.GOOS, with every empty field filled in
// from the running machine.
func New(opts Options) (Manager, error) {
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.Runner == nil {
		opts.Runner = execRunner{}
	}
	if opts.UID == 0 {
		opts.UID = os.Getuid()
	}
	if len(opts.Args) == 0 {
		opts.Args = []string{"-supervised"}
	}

	if opts.ExecPath == "" {
		path, err := StableExecPath()
		if err != nil {
			return nil, err
		}
		opts.ExecPath = path
	}
	if !filepath.IsAbs(opts.ExecPath) {
		// A relative path in a unit file resolves against whatever
		// directory the login manager happens to be in, which is never
		// the one the user was in when they ran install.
		abs, err := filepath.Abs(opts.ExecPath)
		if err != nil {
			return nil, fmt.Errorf("resolve %q: %w", opts.ExecPath, err)
		}
		opts.ExecPath = abs
	}

	if opts.Home == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find the home directory: %w", err)
		}
		opts.Home = filepath.Join(home, ".mailman")
	}

	switch opts.GOOS {
	case "darwin":
		return newLaunchd(opts)
	case "linux":
		return newSystemd(opts)
	default:
		return nil, fmt.Errorf("%w: mailman cannot register a login service on %s yet. "+
			"It still runs in the foreground with `mailman`", ErrUnsupported, opts.GOOS)
	}
}

// LogPath is where a supervised Mailman's output is collected on the
// platforms that need a file for it.
func (o Options) LogPath() string { return filepath.Join(o.Home, "mailman.log") }

// StableExecPath is the path to record in a unit file.
//
// os.Executable is the starting point, but on macOS it returns the path used
// to exec rather than the resolved one, and that distinction matters for
// Homebrew. Running `mailman` off PATH gives /opt/homebrew/bin/mailman, the
// stable symlink -- which is what we want. Running the Cellar path directly
// gives a versioned directory that `brew upgrade` deletes, leaving a login
// item pointing at nothing. So a Cellar path is walked back to its shim when
// the shim leads to the same file.
func StableExecPath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("find the mailman binary: %w", err)
	}

	if shim, ok := homebrewShim(path); ok {
		return shim, nil
	}

	return path, nil
}

// homebrewShim maps <prefix>/Cellar/<name>/<version>/bin/<name> back to
// <prefix>/bin/<name>, when that link really does lead to the same binary.
func homebrewShim(path string) (string, bool) {
	const cellar = "/Cellar/"

	index := strings.Index(path, cellar)
	if index < 0 {
		return "", false
	}

	prefix := path[:index]
	shim := filepath.Join(prefix, "bin", filepath.Base(path))

	target, err := filepath.EvalSymlinks(shim)
	if err != nil {
		return "", false
	}

	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false
	}

	return shim, target == resolved
}
