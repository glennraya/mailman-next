package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

type systemd struct {
	opts Options
}

func newSystemd(opts Options) (Manager, error) {
	if opts.UnitDir == "" {
		config, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("find the config directory: %w", err)
		}
		// os.UserConfigDir honours XDG_CONFIG_HOME, which is the same
		// lookup systemd does, so the two agree even on a machine that
		// has moved it.
		opts.UnitDir = filepath.Join(config, "systemd", "user")
	}

	return &systemd{opts: opts}, nil
}

func (s *systemd) UnitPath() string {
	return filepath.Join(s.opts.UnitDir, UnitName)
}

func (s *systemd) LogHint() string {
	return "journalctl --user -u " + UnitName + " -f"
}

// unitTemplate is the systemd user unit.
//
// There is no After=network.target: it means nothing in the user manager, and
// Mailman binds loopback, which is up before anything else is.
//
// Restart=on-failure rather than always is the other half of the
// port-conflict design. A Mailman that cannot have its ports exits zero, and
// on-failure reads that as "it meant to stop" -- so a Mailpit holding 1983
// produces one logged explanation instead of a restart every five seconds
// until someone notices.
//
// The StartLimit pair then bounds the genuine failures: an unwritable
// database or a broken config lands the unit in `failed` after five tries,
// where `mailman service status` can report it, rather than retrying forever.
var unitTemplate = template.Must(template.New("unit").Funcs(template.FuncMap{
	"unit": unitEscape,
}).Parse(`[Unit]
Description=Mailman, a localhost mail catcher you can reply from
Documentation=https://github.com/glennraya/mailman
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Type=simple
ExecStart={{.ExecStart}}
WorkingDirectory={{.Home | unit}}
Environment=MAILMAN_HOME={{.Home | unit}}
Restart=on-failure
RestartSec=5
TimeoutStopSec=15

[Install]
WantedBy=default.target
`))

func (s *systemd) Render() ([]byte, error) {
	command := make([]string, 0, len(s.opts.Args)+1)
	command = append(command, unitArgument(s.opts.ExecPath))
	for _, arg := range s.opts.Args {
		command = append(command, unitArgument(arg))
	}

	var buf bytes.Buffer
	err := unitTemplate.Execute(&buf, struct {
		ExecStart string
		Home      string
	}{
		ExecStart: strings.Join(command, " "),
		Home:      s.opts.Home,
	})
	if err != nil {
		return nil, fmt.Errorf("render the systemd unit: %w", err)
	}

	return buf.Bytes(), nil
}

func (s *systemd) Install(ctx context.Context) error {
	if err := s.requireSystemd(); err != nil {
		return err
	}

	unit, err := s.Render()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(s.opts.UnitDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", s.opts.UnitDir, err)
	}
	if err := os.WriteFile(s.UnitPath(), unit, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", s.UnitPath(), err)
	}
	if err := os.MkdirAll(s.opts.Home, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", s.opts.Home, err)
	}

	if _, err := s.opts.Runner.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("reload the user units: %w", err)
	}
	if _, err := s.opts.Runner.Run(ctx, "systemctl", "--user", "enable", "--now", UnitName); err != nil {
		return fmt.Errorf("enable the user service: %w", err)
	}

	return nil
}

func (s *systemd) Uninstall(ctx context.Context) error {
	_, _ = s.opts.Runner.Run(ctx, "systemctl", "--user", "disable", "--now", UnitName)

	if err := os.Remove(s.UnitPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove %s: %w", s.UnitPath(), err)
	}

	_, _ = s.opts.Runner.Run(ctx, "systemctl", "--user", "daemon-reload")

	// Without this a unit that failed before being removed leaves its
	// failed state behind, and the next install inherits it.
	_, _ = s.opts.Runner.Run(ctx, "systemctl", "--user", "reset-failed", UnitName)

	return nil
}

func (s *systemd) Start(ctx context.Context) error {
	if _, err := os.Stat(s.UnitPath()); err != nil {
		return errors.New("mailman is not registered to run at login -- run `mailman service install` first")
	}

	if _, err := s.opts.Runner.Run(ctx, "systemctl", "--user", "start", UnitName); err != nil {
		return fmt.Errorf("start the user service: %w", err)
	}

	return nil
}

func (s *systemd) Stop(ctx context.Context) error {
	if _, err := s.opts.Runner.Run(ctx, "systemctl", "--user", "stop", UnitName); err != nil {
		return fmt.Errorf("stop the user service: %w", err)
	}

	return nil
}

func (s *systemd) Status(ctx context.Context) (Status, error) {
	status := Status{Manager: "systemd user service (" + UnitName + ")"}

	if _, err := os.Stat(s.UnitPath()); err == nil {
		status.Installed = true
	}

	// One `show` call with --value returns the fields in the order asked
	// for, one per line, with no format to guess at -- unlike `status`,
	// which is explicitly documented as being for humans.
	out, err := s.opts.Runner.Run(ctx, "systemctl", "--user", "show", UnitName,
		"-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "ExecMainStatus", "--value")
	if err != nil {
		return status, nil
	}

	fields := strings.Split(strings.TrimSpace(out), "\n")
	if len(fields) >= 4 {
		active, sub := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
		status.PID, _ = strconv.Atoi(strings.TrimSpace(fields[2]))
		status.LastExit, _ = strconv.Atoi(strings.TrimSpace(fields[3]))
		status.Running = active == "active" && status.PID > 0

		if active == "failed" {
			status.Note = "the unit is in the failed state (" + sub + ") -- " + s.LogHint()
		}
	}

	if status.Installed && !status.Running && status.Note == "" {
		status.Note = "registered but not running -- " + s.LogHint()
	}
	if note := s.lingerNote(ctx); note != "" {
		status.Note = strings.TrimSpace(status.Note + "\n" + note)
	}

	return status, nil
}

// requireSystemd refuses early on a machine with no user manager to register
// with, rather than failing later as a confusing exec error. /run/systemd/system
// is the canonical test -- it is what sd_booted does -- and it is correct on
// WSL1, on OpenRC distributions and inside most containers.
func (s *systemd) requireSystemd() error {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return nil
	}

	return errors.New(`this system is not running systemd, so there is no user service to register.

Mailman still runs in the foreground with "mailman". To start it at login,
add it to your desktop session's autostart or to whatever supervisor your
init system provides`)
}

// lingerNote explains the one thing about a user service that surprises
// people: it stops when the last session ends. That is the right default on a
// laptop and the wrong one on a headless box, and enabling it is a system
// change that usually raises a polkit prompt -- so this reports rather than
// acts.
func (s *systemd) lingerNote(ctx context.Context) string {
	user := os.Getenv("USER")
	if user == "" {
		return ""
	}

	out, err := s.opts.Runner.Run(ctx, "loginctl", "show-user", user, "-p", "Linger", "--value")
	if err != nil || strings.TrimSpace(out) == "yes" {
		return ""
	}

	return "Mailman runs while you are logged in. To keep it running after logout:\n" +
		"    loginctl enable-linger " + user
}

// unitEscape makes a value safe in a systemd directive. Percent introduces a
// specifier, so a literal one has to be doubled -- a home directory with a %
// in it would otherwise expand to something else entirely.
func unitEscape(value string) string {
	return strings.ReplaceAll(value, "%", "%%")
}

// unitArgument escapes a value for ExecStart, where a space would otherwise
// split one argument into two.
func unitArgument(value string) string {
	escaped := unitEscape(value)
	if strings.ContainsAny(escaped, " \t\"") {
		return strconv.Quote(escaped)
	}

	return escaped
}
