package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

// brewLabel is the launch agent `brew services start mailman` would install.
// Two agents on the same two ports is a fight neither wins, so Install
// refuses when this one is present.
const brewLabel = "homebrew.mxcl.mailman"

type launchd struct {
	opts Options
}

func newLaunchd(opts Options) (Manager, error) {
	if opts.UnitDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find the home directory: %w", err)
		}
		opts.UnitDir = filepath.Join(home, "Library", "LaunchAgents")
	}

	return &launchd{opts: opts}, nil
}

func (l *launchd) UnitPath() string {
	return filepath.Join(l.opts.UnitDir, Label+".plist")
}

func (l *launchd) LogHint() string {
	return l.opts.LogPath()
}

// domain is the launchd target a user's agents live in. gui/<uid> is the
// per-login-session domain, which is what makes this a login item rather than
// a system daemon needing root.
func (l *launchd) domain() string {
	return "gui/" + strconv.Itoa(l.opts.UID)
}

func (l *launchd) target() string {
	return l.domain() + "/" + Label
}

// plistTemplate is the launch agent.
//
// Three of these keys carry a decision worth knowing about:
//
// KeepAlive is a dict rather than <true/>. A plain true restarts after any
// exit at all, including the clean one that follows `launchctl bootout`, so
// stopping the service would be impossible. SuccessfulExit=false with
// Crashed=true means "restart failures and crashes, respect a deliberate
// stop" -- and it is the half of the port-conflict design that lives outside
// the Go code, since a Mailman that exits zero on a taken port stays down.
//
// ProcessType is Adaptive, not the more usual Background. Background gets
// throttled I/O and CPU, which is wrong for something a browser is talking
// to; Adaptive is documented as exactly this case and is promoted to
// interactive priority when a user-facing process starts using it.
//
// The log paths are the only place a supervised Mailman's output can go.
// There is no terminal attached, so without these the first question every
// user asks -- "is it running, and why not" -- has no answer.
var plistTemplate = template.Must(template.New("plist").Funcs(template.FuncMap{
	"xml": xmlEscape,
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label | xml}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.ExecPath | xml}}</string>
{{- range .Args}}
		<string>{{. | xml}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
		<key>Crashed</key>
		<true/>
	</dict>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>ProcessType</key>
	<string>Adaptive</string>
	<key>WorkingDirectory</key>
	<string>{{.Home | xml}}</string>
	<key>StandardOutPath</key>
	<string>{{.LogPath | xml}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath | xml}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>MAILMAN_HOME</key>
		<string>{{.Home | xml}}</string>
	</dict>
</dict>
</plist>
`))

func (l *launchd) Render() ([]byte, error) {
	var buf bytes.Buffer
	err := plistTemplate.Execute(&buf, struct {
		Label    string
		ExecPath string
		Args     []string
		Home     string
		LogPath  string
	}{
		Label:    Label,
		ExecPath: l.opts.ExecPath,
		Args:     l.opts.Args,
		Home:     l.opts.Home,
		LogPath:  l.opts.LogPath(),
	})
	if err != nil {
		return nil, fmt.Errorf("render the launch agent: %w", err)
	}

	return buf.Bytes(), nil
}

func (l *launchd) Install(ctx context.Context) error {
	if conflict := l.brewConflict(ctx); conflict != "" {
		return fmt.Errorf(`brew services is already managing Mailman.

Homebrew installed its own launch agent at
  %s
and it would fight this one for Mailman's two ports.

Pick one:
  brew services stop mailman && mailman service install
  (or leave brew services in charge and skip this)`, conflict)
	}

	plist, err := l.Render()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(l.opts.UnitDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", l.opts.UnitDir, err)
	}
	if err := os.WriteFile(l.UnitPath(), plist, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", l.UnitPath(), err)
	}
	if err := l.prepareLog(); err != nil {
		return err
	}

	// launchd keeps a per-label disabled flag that outlives both bootout
	// and a reinstall. If the user ever ran `launchctl disable` on this
	// label, bootstrap silently does nothing, forever, with no error to
	// explain it. One line up front removes an otherwise baffling failure.
	_, _ = l.opts.Runner.Run(ctx, "launchctl", "enable", l.target())

	// An already-loaded job makes bootstrap fail rather than replace, so
	// clear it first. A job that was not loaded makes this fail harmlessly.
	_, _ = l.opts.Runner.Run(ctx, "launchctl", "bootout", l.target())

	// bootstrap rather than the deprecated load: load warns on current
	// macOS and reports failures as a bare "Load failed", where bootstrap
	// returns a diagnosable error.
	if _, err := l.opts.Runner.Run(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath()); err != nil {
		return fmt.Errorf("register the launch agent: %w", err)
	}

	return nil
}

func (l *launchd) Uninstall(ctx context.Context) error {
	_, _ = l.opts.Runner.Run(ctx, "launchctl", "bootout", l.target())

	if err := os.Remove(l.UnitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", l.UnitPath(), err)
	}

	return nil
}

func (l *launchd) Start(ctx context.Context) error {
	if _, err := os.Stat(l.UnitPath()); err != nil {
		return errors.New("mailman is not registered to run at login -- run `mailman service install` first")
	}

	// bootstrap loads a job that is not loaded; kickstart starts one that
	// is. Trying in that order covers both without asking first.
	if _, err := l.opts.Runner.Run(ctx, "launchctl", "bootstrap", l.domain(), l.UnitPath()); err == nil {
		return nil
	}

	if _, err := l.opts.Runner.Run(ctx, "launchctl", "kickstart", "-k", l.target()); err != nil {
		return fmt.Errorf("start the launch agent: %w", err)
	}

	return nil
}

func (l *launchd) Stop(ctx context.Context) error {
	if _, err := l.opts.Runner.Run(ctx, "launchctl", "bootout", l.target()); err != nil {
		return fmt.Errorf("stop the launch agent: %w", err)
	}

	return nil
}

var (
	pidPattern      = regexp.MustCompile(`"PID"\s*=\s*(\d+);`)
	lastExitPattern = regexp.MustCompile(`"LastExitStatus"\s*=\s*(-?\d+);`)
)

func (l *launchd) Status(ctx context.Context) (Status, error) {
	status := Status{Manager: "launchd (" + Label + ")"}

	if _, err := os.Stat(l.UnitPath()); err == nil {
		status.Installed = true
	}
	status.Conflict = l.brewConflict(ctx)

	// `launchctl list <label>` is parsed rather than `launchctl print`:
	// list emits a stable dictionary, while print's layout has changed
	// between macOS releases and is documented as human-only.
	out, err := l.opts.Runner.Run(ctx, "launchctl", "list", Label)
	if err != nil {
		// A label launchd does not know is not an error to report; it
		// is simply not running.
		return status, nil
	}

	if match := pidPattern.FindStringSubmatch(out); match != nil {
		status.PID, _ = strconv.Atoi(match[1])
		status.Running = status.PID > 0
	}
	if match := lastExitPattern.FindStringSubmatch(out); match != nil {
		status.LastExit, _ = strconv.Atoi(match[1])
	}

	if status.Installed && !status.Running && status.LastExit == 0 {
		// The shape of a clean stop on a taken port: registered, not
		// running, and launchd was told not to retry.
		status.Note = "registered but not running -- see the log for why it stopped"
	}

	return status, nil
}

// brewConflict returns the path of Homebrew's launch agent when one exists.
func (l *launchd) brewConflict(ctx context.Context) string {
	path := filepath.Join(l.opts.UnitDir, brewLabel+".plist")
	if _, err := os.Stat(path); err == nil {
		return path
	}

	// A loaded agent whose plist has moved still holds the ports.
	if _, err := l.opts.Runner.Run(ctx, "launchctl", "list", brewLabel); err == nil {
		return brewLabel + " (loaded)"
	}

	return ""
}

// prepareLog creates the log file before launchd does.
//
// launchd opens StandardOutPath itself, and whatever mode it creates is what
// the file keeps. The log records every captured message's sender and
// subject, so it has to be readable only by its owner -- creating it first is
// how that mode gets chosen.
func (l *launchd) prepareLog() error {
	if err := os.MkdirAll(l.opts.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", l.opts.Home, err)
	}

	file, err := os.OpenFile(l.opts.LogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", l.opts.LogPath(), err)
	}

	return file.Close()
}

// xmlEscape makes a value safe inside a plist <string>. A home directory
// containing an ampersand is rare and entirely legal, and without this it
// produces a plist launchd rejects with no useful explanation.
func xmlEscape(value string) string {
	var buf strings.Builder
	for _, r := range value {
		switch r {
		case '&':
			buf.WriteString("&amp;")
		case '<':
			buf.WriteString("&lt;")
		case '>':
			buf.WriteString("&gt;")
		default:
			buf.WriteRune(r)
		}
	}

	return buf.String()
}
