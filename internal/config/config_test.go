package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withHome points the loader at a scratch directory and clears any MAILMAN_*
// variables the developer running the tests happens to have set.
func withHome(t *testing.T) string {
	t.Helper()

	home := t.TempDir()
	t.Setenv("MAILMAN_HOME", home)

	for _, name := range []string{
		"MAILMAN_HTTP_ADDR", "MAILMAN_SMTP_ADDR", "MAILMAN_MAX_MESSAGE_BYTES",
		"MAILMAN_WEBHOOK_URL", "MAILMAN_WEBHOOK_FORMAT", "MAILMAN_WEBHOOK_SIGNING_KEY",
		"MAILMAN_WEBHOOK_TIMEOUT", "MAILMAN_WEBHOOK_VERIFY_TLS",
	} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	return home
}

func writeConfig(t *testing.T, home, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestLoadDefaults(t *testing.T) {
	home := withHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.HTTPAddr != DefaultHTTPAddr {
		t.Errorf("http addr = %q, want %q", cfg.HTTPAddr, DefaultHTTPAddr)
	}
	if cfg.SMTPAddr != DefaultSMTPAddr {
		t.Errorf("smtp addr = %q, want %q", cfg.SMTPAddr, DefaultSMTPAddr)
	}
	if cfg.Home != home {
		t.Errorf("home = %q, want %q", cfg.Home, home)
	}
	if !cfg.VerifyTLS() {
		t.Error("TLS verification defaulted to off")
	}
	if cfg.WebhookEnabled() {
		t.Error("webhook reported enabled with nothing configured")
	}
	if cfg.DBPath() != filepath.Join(home, "mailman.db") {
		t.Errorf("db path = %q", cfg.DBPath())
	}
}

func TestConfigFileIsRead(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, `{
		"http_addr": "127.0.0.1:9000",
		"webhook": {
			"url": "http://myapp.test/inbound",
			"format": "mailgun",
			"timeout_seconds": 12,
			"verify_tls": false,
			"routes": {
				"MAIL.MyApp.test": { "url": "http://other.test/hook" },
				"blank.test": { "url": "" }
			}
		}
	}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("http addr = %q", cfg.HTTPAddr)
	}
	if cfg.Webhook.Format != FormatMailgun {
		t.Errorf("format = %q", cfg.Webhook.Format)
	}
	if cfg.Webhook.Timeout.Duration() != 12*time.Second {
		t.Errorf("timeout = %v, want 12s", cfg.Webhook.Timeout.Duration())
	}
	if cfg.VerifyTLS() {
		t.Error("verify_tls: false was ignored; self-signed .test certificates would fail")
	}

	// Route keys are matched against a lowercased domain, so they have to be
	// stored lowercased whatever case the file used.
	if _, ok := cfg.Webhook.Routes["mail.myapp.test"]; !ok {
		t.Errorf("routes = %+v, want the key lowercased", cfg.Webhook.Routes)
	}

	// A route with no URL cannot be delivered to. Keeping it would make a
	// reply look routed when it is going nowhere.
	if _, ok := cfg.Webhook.Routes["blank.test"]; ok {
		t.Error("a route with no url was kept")
	}
}

func TestEnvironmentBeatsTheConfigFile(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, `{"http_addr": "127.0.0.1:9000", "webhook": {"url": "http://file.test/hook"}}`)

	t.Setenv("MAILMAN_HTTP_ADDR", "127.0.0.1:9999")
	t.Setenv("MAILMAN_WEBHOOK_URL", "http://env.test/hook")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.HTTPAddr != "127.0.0.1:9999" {
		t.Errorf("http addr = %q, want the environment to win", cfg.HTTPAddr)
	}
	if cfg.Webhook.URL != "http://env.test/hook" {
		t.Errorf("webhook url = %q, want the environment to win", cfg.Webhook.URL)
	}
}

// TestUnknownFormatIsRejected covers a mistake that would otherwise surface
// as an app bug: a reply delivered in a shape the receiving app cannot read.
func TestUnknownFormatIsRejected(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, `{"webhook": {"url": "http://x.test", "format": "sendgrid"}}`)

	if _, err := Load(); err == nil {
		t.Fatal("an unknown webhook format was accepted")
	}

	writeConfig(t, home, `{"webhook": {
		"routes": {"a.test": {"url": "http://x.test", "format": "nonsense"}}
	}}`)

	if _, err := Load(); err == nil {
		t.Fatal("an unknown format on a route was accepted")
	}
}

func TestBrokenConfigFileFailsLoudly(t *testing.T) {
	home := withHome(t)
	writeConfig(t, home, `{ not json`)

	// Silently falling back to defaults would leave webhooks quietly
	// disabled and the developer debugging their app instead of the file.
	if _, err := Load(); err == nil {
		t.Fatal("a malformed config file was ignored")
	}
}

func TestTruthy(t *testing.T) {
	for value, want := range map[string]bool{
		"":      false,
		"0":     false,
		"false": false,
		"FALSE": false,
		"no":    false,
		"off":   false,
		"1":     true,
		"true":  true,
		"yes":   true,
	} {
		if got := truthy(value); got != want {
			t.Errorf("truthy(%q) = %v, want %v", value, got, want)
		}
	}
}
