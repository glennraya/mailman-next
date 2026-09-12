package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadDocumentOfAMissingFile(t *testing.T) {
	document, err := ReadDocument(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Mailman runs without a config file, so its absence is a starting
	// point rather than a failure -- and the settings page has to be able to
	// write the first one.
	if document.Webhook.Routes == nil {
		t.Error("routes map is nil, so adding the first route would panic")
	}
	if document.Webhook.URL != "" || document.HTTPAddr != "" {
		t.Errorf("a missing file produced values: %+v", document)
	}
}

func TestDocumentRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	verify := false

	written := &Document{
		HTTPAddr:        "127.0.0.1:9999",
		MaxMessageBytes: 1 << 20,
		Webhook: Webhook{
			URL:        "http://myapp.test/inbound",
			Format:     FormatMailgun,
			SigningKey: "key-abc",
			Timeout:    Seconds(9 * time.Second),
			VerifyTLS:  &verify,
			Routes: map[string]Route{
				"mail.myapp.test": {URL: "http://myapp.test/mailgun", Format: FormatPostmark},
			},
		},
	}

	if err := WriteDocument(path, written); err != nil {
		t.Fatalf("write: %v", err)
	}

	read, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	if read.HTTPAddr != written.HTTPAddr || read.MaxMessageBytes != written.MaxMessageBytes {
		t.Errorf("addresses or size did not survive: %+v", read)
	}
	if read.Webhook.URL != written.Webhook.URL || read.Webhook.Format != written.Webhook.Format {
		t.Errorf("webhook did not survive: %+v", read.Webhook)
	}
	if read.Webhook.SigningKey != "key-abc" {
		t.Errorf("signing key = %q", read.Webhook.SigningKey)
	}
	if read.Webhook.Timeout.Duration() != 9*time.Second {
		t.Errorf("timeout = %s", read.Webhook.Timeout.Duration())
	}
	// The pointer is what makes "off" distinguishable from "unset", so it has
	// to survive as false rather than come back nil.
	if read.Webhook.VerifyTLS == nil || *read.Webhook.VerifyTLS {
		t.Errorf("verify_tls = %v, want an explicit false", read.Webhook.VerifyTLS)
	}
	if route, ok := read.Webhook.Routes["mail.myapp.test"]; !ok || route.Format != FormatPostmark {
		t.Errorf("routes did not survive: %+v", read.Webhook.Routes)
	}

	// And it has to be the shape a person can edit by hand, since the file
	// stays the documented interface.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	var plain map[string]any
	if err := json.Unmarshal(raw, &plain); err != nil {
		t.Fatalf("the written file is not valid JSON: %v", err)
	}
	if _, ok := plain["webhook"]; !ok {
		t.Errorf("no webhook key in the document: %s", raw)
	}
}

// The file can hold a signing key for the app under test, which is a
// credential and must not be world-readable.
func TestWriteDocumentIsNotWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	if err := WriteDocument(path, &Document{}); err != nil {
		t.Fatalf("write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != configMode {
		t.Errorf("mode = %#o, want %#o", mode, configMode)
	}
}

// A save must not be able to leave the installation unbootable, so the
// replacement is atomic and leaves no debris beside it.
func TestWriteDocumentReplacesCleanly(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "config.json")

	if err := WriteDocument(path, &Document{Webhook: Webhook{URL: "http://first.test"}}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteDocument(path, &Document{Webhook: Webhook{URL: "http://second.test"}}); err != nil {
		t.Fatalf("second write: %v", err)
	}

	document, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if document.Webhook.URL != "http://second.test" {
		t.Errorf("url = %q, want the second write", document.Webhook.URL)
	}

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only config.json", names)
	}
}

func TestReadDocumentRejectsMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := ReadDocument(path); err == nil {
		t.Fatal("malformed JSON was accepted")
	}
}

// The loader and the document have to agree about the file, or the settings
// page would write something startup then reads differently.
func TestLoadReadsWhatWriteDocumentWrote(t *testing.T) {
	home := withHome(t)

	if err := WriteDocument(filepath.Join(home, "config.json"), &Document{
		Webhook: Webhook{
			URL:    "http://myapp.test/inbound",
			Format: FormatPostmark,
			Routes: map[string]Route{"mail.myapp.test": {URL: "http://myapp.test/route"}},
		},
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Webhook.URL != "http://myapp.test/inbound" {
		t.Errorf("url = %q", cfg.Webhook.URL)
	}
	if cfg.Webhook.Format != FormatPostmark {
		t.Errorf("format = %q", cfg.Webhook.Format)
	}
	if route, ok := cfg.Webhook.Routes["mail.myapp.test"]; !ok || route.URL != "http://myapp.test/route" {
		t.Errorf("routes = %+v", cfg.Webhook.Routes)
	}
}

func TestDocumentValidateAccepts(t *testing.T) {
	ok := []*Document{
		{},
		{HTTPAddr: "127.0.0.1:8383", SMTPAddr: "127.0.0.1:1983"},
		// Port 0 is how the tests bind: the operating system picks one.
		{HTTPAddr: "127.0.0.1:0"},
		{Webhook: Webhook{URL: "https://myapp.test/inbound", Format: FormatPostmark}},
		{Webhook: Webhook{Routes: map[string]Route{
			"mail.myapp.test": {URL: "http://myapp.test/hook", Format: FormatMailgun},
		}}},
	}

	for i, document := range ok {
		if err := document.Validate(); err != nil {
			t.Errorf("case %d rejected: %v", i, err)
		}
	}
}

// Stricter than the resolved config's validation on purpose: normalize()
// quietly drops a bad route, which is right for a hand-edited file and wrong
// for a row someone just typed -- and a bad listen address on disk breaks the
// next startup, with no settings page left to fix it from.
func TestDocumentValidateRejects(t *testing.T) {
	cases := map[string]*Document{
		"no port":            {HTTPAddr: "127.0.0.1"},
		"port out of range":  {SMTPAddr: "127.0.0.1:70000"},
		"port not a number":  {SMTPAddr: "127.0.0.1:smtp"},
		"no host":            {HTTPAddr: ":8383"},
		"negative size":      {MaxMessageBytes: -1},
		"unknown format":     {Webhook: Webhook{Format: "sendgrid"}},
		"relative url":       {Webhook: Webhook{URL: "myapp.test/inbound"}},
		"url with no host":   {Webhook: Webhook{URL: "http://"}},
		"negative timeout":   {Webhook: Webhook{Timeout: -1}},
		"route with no url":  {Webhook: Webhook{Routes: map[string]Route{"a.test": {}}}},
		"route with no name": {Webhook: Webhook{Routes: map[string]Route{"": {URL: "http://a.test"}}}},
		"route bad format": {Webhook: Webhook{Routes: map[string]Route{
			"a.test": {URL: "http://a.test", Format: "carrier-pigeon"},
		}}},
		"route relative url": {Webhook: Webhook{Routes: map[string]Route{
			"a.test": {URL: "a.test/hook"},
		}}},
	}

	for name, document := range cases {
		t.Run(name, func(t *testing.T) {
			if err := document.Validate(); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// The document names the offending route, because "one of your routes is
// wrong" is not something a form can act on.
func TestDocumentValidateNamesTheRoute(t *testing.T) {
	document := &Document{Webhook: Webhook{Routes: map[string]Route{
		"mail.myapp.test": {},
	}}}

	err := document.Validate()
	if err == nil {
		t.Fatal("accepted a route with no URL")
	}
	if !strings.Contains(err.Error(), "mail.myapp.test") {
		t.Errorf("error = %q, want it to name the route", err)
	}
}

// Resolve is what the settings API uses to prove a document loads before
// writing it, so it has to agree with Load for the same file.
func TestResolveMatchesLoad(t *testing.T) {
	home := withHome(t)
	path := DocumentPath(home)

	document := &Document{
		SMTPAddr: "127.0.0.1:2525",
		Webhook: Webhook{
			URL:    "http://myapp.test/inbound",
			Format: FormatMailgun,
			Routes: map[string]Route{"mail.myapp.test": {URL: "http://myapp.test/route"}},
		},
	}
	if err := WriteDocument(path, document); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	read, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resolved, err := Resolve(home, read)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	if loaded.SMTPAddr != resolved.SMTPAddr || loaded.Webhook.URL != resolved.Webhook.URL {
		t.Errorf("Load and Resolve disagree:\n load     %+v\n resolve  %+v", loaded, resolved)
	}
	if len(loaded.Webhook.Routes) != len(resolved.Webhook.Routes) {
		t.Errorf("routes differ: %v vs %v", loaded.Webhook.Routes, resolved.Webhook.Routes)
	}
}

// A save must never persist a value an environment variable supplied, or
// unsetting the variable later would silently leave its value behind.
func TestSavingDoesNotPersistAnEnvironmentValue(t *testing.T) {
	home := withHome(t)
	path := DocumentPath(home)

	t.Setenv("MAILMAN_WEBHOOK_URL", "http://from-env.test/inbound")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Webhook.URL != "http://from-env.test/inbound" {
		t.Fatalf("the environment did not win: %q", cfg.Webhook.URL)
	}
	if cfg.OverriddenBy(FieldWebhookURL) != "MAILMAN_WEBHOOK_URL" {
		t.Errorf("overrides = %v, want the variable recorded", cfg.Overrides)
	}

	// A save reads and rewrites the document, never the resolved config.
	document, err := ReadDocument(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	document.Webhook.Format = FormatPostmark
	if err := WriteDocument(path, document); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(string(raw), "from-env.test") {
		t.Errorf("the environment's value was baked into the file:\n%s", raw)
	}
}

func TestOverridesRecordEveryEnvironmentVariable(t *testing.T) {
	withHome(t)

	for variable, field := range map[string]string{
		"MAILMAN_HTTP_ADDR":           FieldHTTPAddr,
		"MAILMAN_SMTP_ADDR":           FieldSMTPAddr,
		"MAILMAN_WEBHOOK_URL":         FieldWebhookURL,
		"MAILMAN_WEBHOOK_FORMAT":      FieldWebhookFormat,
		"MAILMAN_WEBHOOK_SIGNING_KEY": FieldWebhookSigningKey,
		"MAILMAN_WEBHOOK_VERIFY_TLS":  FieldWebhookVerifyTLS,
	} {
		t.Run(variable, func(t *testing.T) {
			value := "127.0.0.1:9000"
			switch field {
			case FieldWebhookURL:
				value = "http://myapp.test/inbound"
			case FieldWebhookFormat:
				value = FormatPostmark
			case FieldWebhookSigningKey:
				value = "secret"
			case FieldWebhookVerifyTLS:
				value = "false"
			}
			t.Setenv(variable, value)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if got := cfg.OverriddenBy(field); got != variable {
				t.Errorf("%s reported as set by %q, want %q", field, got, variable)
			}
		})
	}
}

// Routes are file-only by design, so nothing can shadow them and the settings
// page must never show them locked.
func TestRoutesAreNeverOverridden(t *testing.T) {
	withHome(t)
	t.Setenv("MAILMAN_WEBHOOK_URL", "http://from-env.test/inbound")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for field := range cfg.Overrides {
		if strings.Contains(field, "routes") {
			t.Errorf("routes reported as overridden by %q", cfg.Overrides[field])
		}
	}
}
