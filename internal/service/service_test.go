package service

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// queries are the commands that ask a question rather than change something.
// The fake fails them unless a test supplies an answer, because that is what
// they really do when the thing asked about does not exist -- and a fake that
// answered every question successfully would report a Homebrew agent on a
// machine that has none.
var queries = []string{"launchctl list", "systemctl --user show", "loginctl show-user"}

// fakeRunner records what would have been run instead of running it. Every
// test in this package uses one: registering a real login item on the machine
// running the suite is not a risk worth taking for any amount of coverage.
type fakeRunner struct {
	calls []string
	out   map[string]string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	f.calls = append(f.calls, call)

	for prefix, out := range f.out {
		if strings.HasPrefix(call, prefix) {
			return out, nil
		}
	}
	for _, prefix := range queries {
		if strings.HasPrefix(call, prefix) {
			return "", os.ErrNotExist
		}
	}

	return "", nil
}

// testManager builds a Manager for goos whose every path lands in t's
// temporary directory.
func testManager(t *testing.T, goos string, runner Runner) Manager {
	t.Helper()

	dir := t.TempDir()
	manager, err := New(Options{
		GOOS:     goos,
		ExecPath: "/opt/homebrew/bin/mailman",
		Home:     filepath.Join(dir, ".mailman"),
		UnitDir:  filepath.Join(dir, "units"),
		UID:      501,
		Runner:   runner,
	})
	if err != nil {
		t.Fatalf("New(%s): %v", goos, err)
	}

	return manager
}

func TestNewRejectsAPlatformWithNoLoginManager(t *testing.T) {
	_, err := New(Options{GOOS: "plan9", ExecPath: "/bin/mailman", Home: "/tmp", Runner: &fakeRunner{}})
	if err == nil {
		t.Fatal("an unsupported platform was accepted")
	}
	// The user is not stuck: the foreground binary still works, and the
	// message has to say so rather than just refusing.
	if !strings.Contains(err.Error(), "mailman") {
		t.Errorf("error %q does not say what still works", err)
	}
}

// The launch agent is only as good as the keys that make it behave under a
// supervisor, and each of these is load-bearing enough that losing one in a
// refactor would be invisible until a user's laptop started a restart loop.
func TestLaunchdRendersTheLoadBearingKeys(t *testing.T) {
	manager := testManager(t, "darwin", &fakeRunner{})

	plist, err := manager.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	text := string(plist)

	for _, want := range []string{
		"<key>Label</key>",
		Label,
		"/opt/homebrew/bin/mailman",
		"<string>-supervised</string>",
		"<key>RunAtLoad</key>",
		// A KeepAlive dict rather than a bare true: the difference
		// between a stop that sticks and one launchd undoes.
		"<key>SuccessfulExit</key>",
		"<false/>",
		"<string>Adaptive</string>",
		"mailman.log",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the plist is missing %q", want)
		}
	}

	// A plain <true/> for KeepAlive would make `launchctl bootout` useless,
	// since launchd would restart the job it had just stopped.
	if strings.Contains(text, "<key>KeepAlive</key>\n\t<true/>") {
		t.Error("KeepAlive is a bare true, so a deliberate stop would be undone")
	}
}

func TestSystemdRendersTheLoadBearingDirectives(t *testing.T) {
	manager := testManager(t, "linux", &fakeRunner{})

	unit, err := manager.Render()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	text := string(unit)

	for _, want := range []string{
		"ExecStart=/opt/homebrew/bin/mailman -supervised",
		// on-failure, not always: a Mailman that exits zero because
		// something took its port must stay stopped.
		"Restart=on-failure",
		"WantedBy=default.target",
		"StartLimitBurst=5",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the unit is missing %q", want)
		}
	}

	if strings.Contains(text, "Restart=always") {
		t.Error("Restart=always would restart a clean stop, which is the crash loop this avoids")
	}
}

// A home directory with an ampersand or a percent in it is rare and entirely
// legal, and unescaped it produces a unit the manager rejects with no useful
// explanation -- the kind of failure that is invisible until someone reports
// it from a machine you cannot see.
func TestRenderersEscapeHostileHomeDirectories(t *testing.T) {
	const home = "/Users/ann & bob/100% mine"

	t.Run("launchd produces parseable XML", func(t *testing.T) {
		manager, err := New(Options{
			GOOS: "darwin", ExecPath: "/usr/local/bin/mail&man",
			Home: home, UnitDir: t.TempDir(), UID: 501, Runner: &fakeRunner{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		plist, err := manager.Render()
		if err != nil {
			t.Fatalf("render: %v", err)
		}

		// Walking every token is what catches a raw ampersand; a
		// substring check would not.
		decoder := xml.NewDecoder(strings.NewReader(string(plist)))
		for {
			_, err := decoder.Token()
			if err != nil {
				if err.Error() == "EOF" {
					break
				}
				t.Fatalf("the plist is not well-formed XML: %v", err)
			}
		}
	})

	t.Run("systemd doubles the percent", func(t *testing.T) {
		manager, err := New(Options{
			GOOS: "linux", ExecPath: "/usr/local/bin/mailman",
			Home: home, UnitDir: t.TempDir(), UID: 501, Runner: &fakeRunner{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		unit, err := manager.Render()
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		text := string(unit)

		// A single % introduces a systemd specifier, so the literal
		// has to arrive doubled.
		if !strings.Contains(text, "100%% mine") {
			t.Errorf("the percent was not doubled:\n%s", text)
		}
	})

	t.Run("systemd quotes a path with spaces", func(t *testing.T) {
		manager, err := New(Options{
			GOOS: "linux", ExecPath: "/opt/my tools/mailman",
			Home: "/tmp", UnitDir: t.TempDir(), UID: 501, Runner: &fakeRunner{},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		unit, err := manager.Render()
		if err != nil {
			t.Fatalf("render: %v", err)
		}

		if !strings.Contains(string(unit), `ExecStart="/opt/my tools/mailman" -supervised`) {
			t.Errorf("a path with spaces was not quoted:\n%s", unit)
		}
	})
}

// The launchctl sequence is the part most likely to be "tidied" by someone
// who does not know why enable comes first, so the order is pinned here.
func TestLaunchdInstallRunsTheRightSequence(t *testing.T) {
	runner := &fakeRunner{}
	manager := testManager(t, "darwin", runner)

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	want := []string{
		// The brew check comes first and writes nothing, so a refusal
		// leaves the machine exactly as it was.
		"launchctl list " + brewLabel,
		// enable clears the persistent disabled flag that survives
		// bootout; without it a previously disabled label silently
		// never starts again.
		"launchctl enable gui/501/" + Label,
		"launchctl bootout gui/501/" + Label,
		"launchctl bootstrap gui/501 " + manager.UnitPath(),
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("ran %q, want %q", runner.calls, want)
	}
	for i, call := range want {
		if runner.calls[i] != call {
			t.Errorf("call %d = %q, want %q", i, runner.calls[i], call)
		}
	}

	if _, err := os.Stat(manager.UnitPath()); err != nil {
		t.Errorf("the plist was not written: %v", err)
	}
}

// The log records every captured message's sender and subject. launchd keeps
// whatever mode the file already has, so creating it first is the only chance
// to make it owner-only.
func TestLaunchdInstallCreatesThePrivateLog(t *testing.T) {
	manager := testManager(t, "darwin", &fakeRunner{})

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	info, err := os.Stat(manager.LogHint())
	if err != nil {
		t.Fatalf("the log was not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("log mode = %o, want 600: it holds senders and subject lines", mode)
	}
}

// Two managers driving Mailman means two processes fighting for the same two
// ports. Refusing is better than tearing down an agent the user did not ask
// this command to touch.
func TestLaunchdInstallRefusesWhenBrewServicesIsInCharge(t *testing.T) {
	dir := t.TempDir()
	unitDir := filepath.Join(dir, "units")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	brewPlist := filepath.Join(unitDir, brewLabel+".plist")
	if err := os.WriteFile(brewPlist, []byte("<plist/>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	runner := &fakeRunner{}
	manager, err := New(Options{
		GOOS: "darwin", ExecPath: "/opt/homebrew/bin/mailman",
		Home: filepath.Join(dir, ".mailman"), UnitDir: unitDir, UID: 501, Runner: runner,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = manager.Install(context.Background())
	if err == nil {
		t.Fatal("install proceeded alongside brew services")
	}
	// The user needs the way out, not just the refusal.
	if !strings.Contains(err.Error(), "brew services stop mailman") {
		t.Errorf("error %q does not say how to resolve it", err)
	}
	if len(runner.calls) != 0 {
		t.Errorf("it ran %q after refusing", runner.calls)
	}
	if _, err := os.Stat(manager.UnitPath()); err == nil {
		t.Error("it wrote its own plist after refusing")
	}
}

func TestSystemdUninstallClearsTheFailedState(t *testing.T) {
	runner := &fakeRunner{}
	manager := testManager(t, "linux", runner)

	if err := manager.Uninstall(context.Background()); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Without reset-failed, a unit that failed before removal leaves that
	// state behind for the next install to inherit.
	var found bool
	for _, call := range runner.calls {
		if strings.Contains(call, "reset-failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("uninstall ran %q, with no reset-failed", runner.calls)
	}
}

func TestLaunchdStatusReadsTheListOutput(t *testing.T) {
	runner := &fakeRunner{
		out: map[string]string{
			"launchctl list " + Label: `{
	"PID" = 4821;
	"LastExitStatus" = 0;
	"Label" = "` + Label + `";
}`,
		},
	}
	manager := testManager(t, "darwin", runner)

	if err := manager.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.Installed {
		t.Error("a written plist was not reported as installed")
	}
	if !status.Running || status.PID != 4821 {
		t.Errorf("running = %v, pid = %d, want true and 4821", status.Running, status.PID)
	}
}

// A Cellar path is deleted by the next `brew upgrade`, which would leave a
// launch agent pointing at a binary that no longer exists.
func TestStableExecPathPrefersTheHomebrewShim(t *testing.T) {
	prefix := t.TempDir()
	cellar := filepath.Join(prefix, "Cellar", "mailman", "0.2.0", "bin")
	if err := os.MkdirAll(cellar, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	real := filepath.Join(cellar, "mailman")
	if err := os.WriteFile(real, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}

	binDir := filepath.Join(prefix, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	shim := filepath.Join(binDir, "mailman")
	if err := os.Symlink(real, shim); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, ok := homebrewShim(real)
	if !ok {
		t.Fatal("a Cellar path was not recognised")
	}
	if got != shim {
		t.Errorf("shim = %q, want %q", got, shim)
	}
}

// A path that merely contains /Cellar/ without a matching shim must be left
// alone, rather than rewritten to something that does not exist.
func TestStableExecPathLeavesUnrelatedPathsAlone(t *testing.T) {
	if _, ok := homebrewShim("/usr/local/bin/mailman"); ok {
		t.Error("a plain path was rewritten")
	}
	if _, ok := homebrewShim(filepath.Join(t.TempDir(), "Cellar", "mailman", "1.0", "bin", "mailman")); ok {
		t.Error("a Cellar path with no shim was rewritten")
	}
}
