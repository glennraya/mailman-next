// Package config resolves Mailman's settings from four layers, each one
// overriding the last: built-in defaults, the config file in the data
// directory, MAILMAN_* environment variables, and finally command-line flags
// (applied by the caller, since only it knows which flags were passed).
//
// Webhook routes live in the config file rather than an environment variable.
// They are a nested structure, and squeezing JSON into a shell variable makes
// them painful to edit and impossible to comment.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults. The ports are the ones every framework's documentation already
// uses for a local mail catcher, so pointing a project at Mailman takes no
// thought. They collide with a running Mailpit by design -- Mailman replaces
// it -- and both move with a single flag.
const (
	DefaultHTTPAddr    = "127.0.0.1:8025"
	DefaultSMTPAddr    = "127.0.0.1:1025"
	DefaultMaxSize     = 25 << 20 // 25 MiB, matching what most providers accept
	DefaultTimeout     = 5 * time.Second
	DefaultFormat      = FormatGeneric
	DefaultMaxAttempts = 1
)

// Webhook payload formats. Generic is Mailman's own shape; the other two
// reproduce what the named provider posts to an inbound route, so an app's
// existing production handler works against Mailman unchanged.
const (
	FormatGeneric  = "generic"
	FormatMailgun  = "mailgun"
	FormatPostmark = "postmark"
)

// Config is the resolved configuration for one run.
type Config struct {
	// Home is the data directory: database, stored mail, config file.
	Home string

	HTTPAddr string
	SMTPAddr string

	// MaxMessageBytes caps a single captured message. Larger mail is
	// rejected at the SMTP layer rather than half-stored.
	MaxMessageBytes int64

	Webhook Webhook
}

// Webhook holds the top-level delivery settings plus the per-domain routes.
// A route inherits every field it leaves unset from this level, so a project
// that speaks the default format needs only a URL.
type Webhook struct {
	URL        string           `json:"url,omitempty"`
	Format     string           `json:"format,omitempty"`
	SigningKey string           `json:"signing_key,omitempty"`
	Timeout    Seconds          `json:"timeout_seconds,omitempty"`
	VerifyTLS  *bool            `json:"verify_tls,omitempty"`
	Routes     map[string]Route `json:"routes,omitempty"`
}

// Route targets one domain. Every field is optional except the URL, and the
// pointer types are what let "unset" mean "inherit" rather than "zero".
type Route struct {
	URL        string  `json:"url"`
	Format     string  `json:"format,omitempty"`
	SigningKey string  `json:"signing_key,omitempty"`
	Timeout    Seconds `json:"timeout_seconds,omitempty"`
	VerifyTLS  *bool   `json:"verify_tls,omitempty"`
}

// Seconds is a duration written as a number of seconds in JSON, so the config
// file reads `"timeout_seconds": 5` instead of Go's "5s" string form.
type Seconds time.Duration

func (s Seconds) Duration() time.Duration { return time.Duration(s) }

func (s Seconds) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(s).Seconds())
}

func (s *Seconds) UnmarshalJSON(b []byte) error {
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*s = Seconds(time.Duration(f * float64(time.Second)))
	return nil
}

// file mirrors the on-disk config document. It is separate from Config so the
// file can stay a small, hand-editable subset of the resolved settings.
type file struct {
	HTTPAddr        string  `json:"http_addr,omitempty"`
	SMTPAddr        string  `json:"smtp_addr,omitempty"`
	MaxMessageBytes int64   `json:"max_message_bytes,omitempty"`
	Webhook         Webhook `json:"webhook"`
}

// Load resolves the configuration. It creates the data directory if it is
// missing, but never writes a config file -- Mailman runs fine without one.
func Load() (*Config, error) {
	home, err := resolveHome()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Home:            home,
		HTTPAddr:        DefaultHTTPAddr,
		SMTPAddr:        DefaultSMTPAddr,
		MaxMessageBytes: DefaultMaxSize,
		Webhook: Webhook{
			Format:  DefaultFormat,
			Timeout: Seconds(DefaultTimeout),
			Routes:  map[string]Route{},
		},
	}

	if err := cfg.applyFile(); err != nil {
		return nil, err
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}

	cfg.normalize()
	return cfg, cfg.Validate()
}

// ConfigPath is where routes and any persisted overrides live.
func (c *Config) ConfigPath() string { return filepath.Join(c.Home, "config.json") }

// DBPath is the single SQLite file holding every message.
func (c *Config) DBPath() string { return filepath.Join(c.Home, "mailman.db") }

// MailDir roots the on-disk payloads. Raw messages and attachments are files
// rather than blobs so the database stays small and a message can be opened
// with any mail client.
func (c *Config) MailDir() string { return filepath.Join(c.Home, "mail") }

func (c *Config) RawDir() string { return filepath.Join(c.MailDir(), "raw") }

func (c *Config) AttachmentDir() string { return filepath.Join(c.MailDir(), "attachments") }

// VerifyTLS reports the top-level TLS verification setting, defaulting to on.
// Local development certificates are usually self-signed, so this is the knob
// people reach for most often.
func (c *Config) VerifyTLS() bool { return c.Webhook.VerifyTLS == nil || *c.Webhook.VerifyTLS }

// WebhookEnabled reports whether a reply has anywhere at all to go.
func (c *Config) WebhookEnabled() bool {
	return c.Webhook.URL != "" || len(c.Webhook.Routes) > 0
}

func (c *Config) applyFile() error {
	raw, err := os.ReadFile(c.ConfigPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", c.ConfigPath(), err)
	}

	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return fmt.Errorf("parse %s: %w", c.ConfigPath(), err)
	}

	if f.HTTPAddr != "" {
		c.HTTPAddr = f.HTTPAddr
	}
	if f.SMTPAddr != "" {
		c.SMTPAddr = f.SMTPAddr
	}
	if f.MaxMessageBytes > 0 {
		c.MaxMessageBytes = f.MaxMessageBytes
	}

	w := f.Webhook
	if w.URL != "" {
		c.Webhook.URL = w.URL
	}
	if w.Format != "" {
		c.Webhook.Format = w.Format
	}
	if w.SigningKey != "" {
		c.Webhook.SigningKey = w.SigningKey
	}
	if w.Timeout > 0 {
		c.Webhook.Timeout = w.Timeout
	}
	if w.VerifyTLS != nil {
		c.Webhook.VerifyTLS = w.VerifyTLS
	}
	for domain, route := range w.Routes {
		c.Webhook.Routes[domain] = route
	}
	return nil
}

func (c *Config) applyEnv() error {
	if v := os.Getenv("MAILMAN_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
	}
	if v := os.Getenv("MAILMAN_SMTP_ADDR"); v != "" {
		c.SMTPAddr = v
	}
	if v := os.Getenv("MAILMAN_MAX_MESSAGE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("MAILMAN_MAX_MESSAGE_BYTES: %w", err)
		}
		c.MaxMessageBytes = n
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_URL"); v != "" {
		c.Webhook.URL = v
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_FORMAT"); v != "" {
		c.Webhook.Format = v
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_SIGNING_KEY"); v != "" {
		c.Webhook.SigningKey = v
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_TIMEOUT"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("MAILMAN_WEBHOOK_TIMEOUT: %w", err)
		}
		c.Webhook.Timeout = Seconds(time.Duration(f * float64(time.Second)))
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_VERIFY_TLS"); v != "" {
		b := truthy(v)
		c.Webhook.VerifyTLS = &b
	}
	return nil
}

// normalize lowercases the route keys so matching can be a plain lookup, and
// drops routes with no URL -- an entry that cannot be delivered to is a
// configuration mistake, and silently keeping it would make a reply look
// routed when it is not.
func (c *Config) normalize() {
	routes := make(map[string]Route, len(c.Webhook.Routes))
	for domain, route := range c.Webhook.Routes {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" || strings.TrimSpace(route.URL) == "" {
			continue
		}
		routes[domain] = route
	}
	c.Webhook.Routes = routes
}

// Validate catches the mistakes worth failing startup over. An unknown
// payload format is one: the reply would be delivered in a shape the app
// under test cannot read, and the failure would look like an app bug.
func (c *Config) Validate() error {
	if err := validFormat(c.Webhook.Format); err != nil {
		return err
	}
	for domain, route := range c.Webhook.Routes {
		if route.Format == "" {
			continue
		}
		if err := validFormat(route.Format); err != nil {
			return fmt.Errorf("route %q: %w", domain, err)
		}
	}
	if c.MaxMessageBytes <= 0 {
		return errors.New("max message size must be positive")
	}
	return nil
}

func validFormat(format string) error {
	switch format {
	case FormatGeneric, FormatMailgun, FormatPostmark:
		return nil
	default:
		return fmt.Errorf("unknown webhook format %q (want %s, %s or %s)",
			format, FormatGeneric, FormatMailgun, FormatPostmark)
	}
}

// resolveHome picks the data directory and makes sure it exists.
func resolveHome() (string, error) {
	home := os.Getenv("MAILMAN_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory: %w", err)
		}
		home = filepath.Join(userHome, ".mailman")
	}

	home, err := filepath.Abs(home)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", home, err)
	}
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", home, err)
	}
	return home, nil
}

// truthy reads the loose booleans people actually type in shell variables.
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
