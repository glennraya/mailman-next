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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults. Deliberately not 1025/8025: that is where Mailpit and MailHog
// listen, and a developer trying Mailman usually still has one of them
// running. Sharing their ports means whichever started first wins, or worse,
// both bind and mail goes to whichever the sender happened to resolve to.
// Sitting alongside them costs one line of config and removes a whole class
// of "where did my mail go".
//
// 8383 is picked on the same principle: it is not a port any common
// development service claims, so the inbox is reachable on a fresh machine
// without an argument about who was there first.
const (
	DefaultHTTPAddr    = "127.0.0.1:8383"
	DefaultSMTPAddr    = "127.0.0.1:1983"
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

	// Overrides records what outranked the config file, keyed by the
	// document path of the field: "webhook.url" -> "MAILMAN_WEBHOOK_URL",
	// "http_addr" -> "-http".
	//
	// Without this the settings page could save a value, report success, and
	// have an environment variable go on quietly winning. Knowing which layer
	// supplied a setting is the difference between a page that configures
	// Mailman and one that only appears to.
	Overrides map[string]string
}

// Document paths, used as Overrides keys and by the settings API. Naming them
// once keeps the Go field, the JSON key and the UI label from drifting apart.
const (
	FieldHTTPAddr          = "http_addr"
	FieldSMTPAddr          = "smtp_addr"
	FieldMaxMessageBytes   = "max_message_bytes"
	FieldWebhookURL        = "webhook.url"
	FieldWebhookFormat     = "webhook.format"
	FieldWebhookSigningKey = "webhook.signing_key"
	FieldWebhookTimeout    = "webhook.timeout_seconds"
	FieldWebhookVerifyTLS  = "webhook.verify_tls"
)

// Override records that something outside the config file supplied a field.
// Callers that apply command-line flags use this too, since only they know
// which flags were actually passed.
func (c *Config) Override(field, source string) {
	if c.Overrides == nil {
		c.Overrides = map[string]string{}
	}
	c.Overrides[field] = source
}

// OverriddenBy names what outranks the config file for a field, or "" when
// the file is free to decide it.
func (c *Config) OverriddenBy(field string) string { return c.Overrides[field] }

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

// Document is config.json exactly as written. It is separate from Config so
// the file can stay a small, hand-editable subset of the resolved settings --
// and so that saving writes back only what someone actually chose, rather
// than the resolved values that a MAILMAN_* variable may have supplied.
type Document struct {
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

	document, err := ReadDocument(DocumentPath(home))
	if err != nil {
		return nil, err
	}

	return Resolve(home, document)
}

// Resolve applies the layers to a document already in hand: built-in
// defaults, then the document, then the environment.
//
// It is separate from Load so that a document can be proved to resolve
// *before* it is written to disk. That is the settings API's only chance to
// refuse a file the next startup would fail on -- and once such a file is
// saved there is no settings page left to fix it from.
func Resolve(home string, document *Document) (*Config, error) {
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
		Overrides: map[string]string{},
	}

	if document != nil {
		cfg.applyDocument(document)
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}

	cfg.normalize()
	return cfg, cfg.Validate()
}

// DocumentPath is where the config file lives inside a data directory.
func DocumentPath(home string) string { return filepath.Join(home, "config.json") }

// ConfigPath is where routes and any persisted settings live.
func (c *Config) ConfigPath() string { return DocumentPath(c.Home) }

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

// applyDocument overlays the config file. It does no I/O: ReadDocument is the
// only thing that reads config.json, so startup and a settings save cannot
// end up disagreeing about what the file says.
func (c *Config) applyDocument(f *Document) {
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
}

// applyEnv is the layer above the file, and every value it sets is also
// recorded in Overrides -- the settings page needs to be able to say that a
// field it cannot change is held by a variable rather than pretend it saved.
func (c *Config) applyEnv() error {
	if v := os.Getenv("MAILMAN_HTTP_ADDR"); v != "" {
		c.HTTPAddr = v
		c.Override(FieldHTTPAddr, "MAILMAN_HTTP_ADDR")
	}
	if v := os.Getenv("MAILMAN_SMTP_ADDR"); v != "" {
		c.SMTPAddr = v
		c.Override(FieldSMTPAddr, "MAILMAN_SMTP_ADDR")
	}
	if v := os.Getenv("MAILMAN_MAX_MESSAGE_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("MAILMAN_MAX_MESSAGE_BYTES: %w", err)
		}
		c.MaxMessageBytes = n
		c.Override(FieldMaxMessageBytes, "MAILMAN_MAX_MESSAGE_BYTES")
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_URL"); v != "" {
		c.Webhook.URL = v
		c.Override(FieldWebhookURL, "MAILMAN_WEBHOOK_URL")
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_FORMAT"); v != "" {
		c.Webhook.Format = v
		c.Override(FieldWebhookFormat, "MAILMAN_WEBHOOK_FORMAT")
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_SIGNING_KEY"); v != "" {
		c.Webhook.SigningKey = v
		c.Override(FieldWebhookSigningKey, "MAILMAN_WEBHOOK_SIGNING_KEY")
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_TIMEOUT"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("MAILMAN_WEBHOOK_TIMEOUT: %w", err)
		}
		c.Webhook.Timeout = Seconds(time.Duration(f * float64(time.Second)))
		c.Override(FieldWebhookTimeout, "MAILMAN_WEBHOOK_TIMEOUT")
	}
	if v := os.Getenv("MAILMAN_WEBHOOK_VERIFY_TLS"); v != "" {
		b := truthy(v)
		c.Webhook.VerifyTLS = &b
		c.Override(FieldWebhookVerifyTLS, "MAILMAN_WEBHOOK_VERIFY_TLS")
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
