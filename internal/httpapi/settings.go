package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
	"github.com/glennraya/mailman/internal/outbound"
)

// maxSettingsBytes caps the settings document. It is a handful of fields and a
// routes table, so anything larger is a mistake or an attack.
const maxSettingsBytes = 1 << 20

// getConfig reports the settings the UI needs to explain itself and to offer
// them for editing: which webhook routes exist, and therefore where a reply
// to a given address would be delivered.
//
// Signing keys are never returned -- only whether one is set. The UI does not
// need them, and anything that can reach this endpoint would otherwise be able
// to read a credential for the app under test.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	s.respond(w, r, http.StatusOK, s.describeSettings())
}

func (s *Server) describeSettings() map[string]any {
	cfg := s.settings()

	routes := make(map[string]any, len(cfg.Webhook.Routes))
	for domain, route := range cfg.Webhook.Routes {
		routes[domain] = map[string]any{
			"url":        route.URL,
			"format":     firstNonEmpty(route.Format, cfg.Webhook.Format),
			"has_secret": route.SigningKey != "" || cfg.Webhook.SigningKey != "",
			"verify_tls": route.VerifyTLS == nil && cfg.VerifyTLS() || route.VerifyTLS != nil && *route.VerifyTLS,
		}
	}

	return map[string]any{
		"version":           s.version,
		"home":              cfg.Home,
		"smtp_addr":         cfg.SMTPAddr,
		"http_addr":         cfg.HTTPAddr,
		"max_message_bytes": cfg.MaxMessageBytes,

		// Where the process actually bound, which is not always what was
		// configured -- a port of 0 resolves to a real one, and a saved port
		// change does not apply until a restart. Reporting only the
		// configured value would make a pending change look applied.
		//
		// needs_restart compares against the snapshot the process booted
		// from, not against the bound address. Comparing against the bound
		// address would report a restart as pending forever whenever port 0
		// was asked for, or when a host resolves to a different spelling.
		"listening": map[string]any{
			"http":          s.listening.HTTP,
			"smtp":          s.listening.SMTP,
			"needs_restart": s.needsRestart(cfg),
		},

		// What outranks the config file, so the UI can lock a field and name
		// the variable holding it rather than accept an edit that will not
		// take effect.
		"overrides": cfg.Overrides,
		"editable":  s.reload != nil,

		"webhook": map[string]any{
			"enabled":         cfg.WebhookEnabled(),
			"url":             cfg.Webhook.URL,
			"format":          cfg.Webhook.Format,
			"has_secret":      cfg.Webhook.SigningKey != "",
			"verify_tls":      cfg.VerifyTLS(),
			"timeout_seconds": cfg.Webhook.Timeout.Duration().Seconds(),
			"timeout_ms":      cfg.Webhook.Timeout.Duration().Milliseconds(),
			"routes":          routes,
		},
	}
}

// needsRestart reports whether a saved setting is waiting on a restart.
//
// The comparison is against the configuration the process started with,
// which is the only honest basis: the listeners were bound from those values
// and cannot be rebound, while the live snapshot already holds whatever was
// saved since.
func (s *Server) needsRestart(cfg *config.Config) bool {
	booted := s.listening.Booted
	if booted == nil {
		return false
	}

	return cfg.HTTPAddr != booted.HTTPAddr ||
		cfg.SMTPAddr != booted.SMTPAddr ||
		cfg.MaxMessageBytes != booted.MaxMessageBytes
}

// routePatch is one row of the routes table.
//
// SigningKey is a pointer so the three intents stay distinguishable: absent
// leaves whatever is stored, "" clears it, and a value replaces it. A plain
// string could not express "leave it alone", which matters because the UI is
// never sent the current key and so cannot send it back.
type routePatch struct {
	URL        string   `json:"url"`
	Format     string   `json:"format,omitempty"`
	SigningKey *string  `json:"signing_key,omitempty"`
	Timeout    *float64 `json:"timeout_seconds,omitempty"`
	VerifyTLS  *bool    `json:"verify_tls,omitempty"`
}

// webhookPatch mirrors the webhook section of the document. Every field is a
// pointer, so a request changes only what it names.
type webhookPatch struct {
	URL        *string                `json:"url,omitempty"`
	Format     *string                `json:"format,omitempty"`
	SigningKey *string                `json:"signing_key,omitempty"`
	Timeout    *float64               `json:"timeout_seconds,omitempty"`
	VerifyTLS  *bool                  `json:"verify_tls,omitempty"`
	Routes     *map[string]routePatch `json:"routes,omitempty"`
}

// settingsPatch is the body of PUT /api/v1/config.
type settingsPatch struct {
	HTTPAddr        *string       `json:"http_addr,omitempty"`
	SMTPAddr        *string       `json:"smtp_addr,omitempty"`
	MaxMessageBytes *int64        `json:"max_message_bytes,omitempty"`
	Webhook         *webhookPatch `json:"webhook,omitempty"`
}

// fields names the settings this patch would change, using the same document
// paths the provenance table uses.
//
// It exists so the locked check and the wire format cannot drift apart: a
// field added to the patch and forgotten here would be writable while an
// environment variable quietly held it. Routes are absent deliberately --
// no variable can shadow them, so they are never locked.
func (p settingsPatch) fields() []string {
	var fields []string

	if p.HTTPAddr != nil {
		fields = append(fields, config.FieldHTTPAddr)
	}
	if p.SMTPAddr != nil {
		fields = append(fields, config.FieldSMTPAddr)
	}
	if p.MaxMessageBytes != nil {
		fields = append(fields, config.FieldMaxMessageBytes)
	}

	if p.Webhook == nil {
		return fields
	}

	for field, changed := range map[string]bool{
		config.FieldWebhookURL:        p.Webhook.URL != nil,
		config.FieldWebhookFormat:     p.Webhook.Format != nil,
		config.FieldWebhookSigningKey: p.Webhook.SigningKey != nil,
		config.FieldWebhookTimeout:    p.Webhook.Timeout != nil,
		config.FieldWebhookVerifyTLS:  p.Webhook.VerifyTLS != nil,
	} {
		if changed {
			fields = append(fields, field)
		}
	}

	sort.Strings(fields)
	return fields
}

// saveConfig writes the settings and republishes them.
//
// The order matters and is deliberate: the merged document is validated
// before anything is written, so a bad format string is a 400 rather than a
// config.json that the next startup refuses to parse. Only then is the file
// replaced, and only then is the running configuration rebuilt from it.
func (s *Server) saveConfig(w http.ResponseWriter, r *http.Request) {
	if s.reload == nil {
		s.respond(w, r, http.StatusNotImplemented, map[string]string{
			"error": "this server cannot reload its configuration",
		})
		return
	}

	var patch settingsPatch
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxSettingsBytes))
	if err := decoder.Decode(&patch); err != nil {
		s.badRequest(w, r, "body must be JSON")
		return
	}

	path := s.settings().ConfigPath()

	document, err := config.ReadDocument(path)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Refuse a field something above the file holds, rather than write it and
	// let the reload's environment layer overwrite it. Accepting would mean
	// answering with the old value for a field the caller just changed --
	// precisely the silent shadowing this page exists to prevent. A UI that
	// renders such a field locked never sends it; a stale one gets told why.
	cfg := s.settings()
	for _, field := range patch.fields() {
		if source := cfg.OverriddenBy(field); source != "" {
			s.respond(w, r, http.StatusConflict, map[string]string{
				"error":  fmt.Sprintf("%s is set by %s, so it cannot be changed here", field, source),
				"field":  field,
				"set_by": source,
			})
			return
		}
	}

	if err := apply(document, patch); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	// Two checks before anything reaches the disk, and neither can be
	// skipped. Validate catches what a form can get wrong; Resolve proves the
	// document actually loads, using the same function startup uses, so a
	// saved file cannot be one the next boot refuses. A rejection here leaves
	// the installation exactly as it was.
	if err := document.Validate(); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}
	if _, err := config.Resolve(cfg.Home, document); err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	if err := config.WriteDocument(path, document); err != nil {
		s.fail(w, r, err)
		return
	}

	reloaded, err := s.reload()
	if err != nil {
		// The file is written but the process could not adopt it. Say so
		// rather than report success: the settings are on disk and will apply
		// at the next start, but they are not in effect now.
		s.logger.Error("saved settings could not be adopted", "error", err)
		s.respond(w, r, http.StatusInternalServerError, map[string]string{
			"error": "the settings were saved but could not be applied: " + err.Error(),
		})
		return
	}

	s.config.Set(reloaded)
	s.logger.Info("settings saved", "config", path, "webhook", reloaded.WebhookEnabled())

	// Through publish, not the broker directly: the client assigns every
	// event's unread count to its badge, and publish fills in a count the
	// caller left at zero.
	s.publish(r, events.Event{Type: events.ConfigChanged})

	s.respond(w, r, http.StatusOK, s.describeSettings())
}

// apply merges a patch into the document. Only named fields change, so a UI
// that renders a locked field read-only and omits it cannot blank it.
func apply(document *config.Document, patch settingsPatch) error {
	if patch.HTTPAddr != nil {
		document.HTTPAddr = strings.TrimSpace(*patch.HTTPAddr)
	}
	if patch.SMTPAddr != nil {
		document.SMTPAddr = strings.TrimSpace(*patch.SMTPAddr)
	}
	if patch.MaxMessageBytes != nil {
		if *patch.MaxMessageBytes <= 0 {
			return fmt.Errorf("max message size must be positive")
		}
		document.MaxMessageBytes = *patch.MaxMessageBytes
	}

	if patch.Webhook == nil {
		return nil
	}
	w := patch.Webhook

	if w.URL != nil {
		document.Webhook.URL = strings.TrimSpace(*w.URL)
	}
	if w.Format != nil {
		document.Webhook.Format = strings.TrimSpace(*w.Format)
	}
	if w.SigningKey != nil {
		document.Webhook.SigningKey = *w.SigningKey
	}
	if w.Timeout != nil {
		if *w.Timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		document.Webhook.Timeout = config.Seconds(time.Duration(*w.Timeout * float64(time.Second)))
	}
	if w.VerifyTLS != nil {
		verify := *w.VerifyTLS
		document.Webhook.VerifyTLS = &verify
	}

	// The routes table is replaced wholesale when present, because that is
	// the only way a removed row actually goes away -- the loader merges
	// routes per key, so omission alone would never delete one.
	if w.Routes != nil {
		routes := make(map[string]config.Route, len(*w.Routes))

		for domain, row := range *w.Routes {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" {
				return fmt.Errorf("a route needs a domain")
			}
			if strings.TrimSpace(row.URL) == "" {
				return fmt.Errorf("the route for %s needs a URL", domain)
			}

			route := config.Route{
				URL:    strings.TrimSpace(row.URL),
				Format: strings.TrimSpace(row.Format),
			}

			// An existing key is kept when the request does not mention one,
			// since the UI is never given it to send back.
			if row.SigningKey != nil {
				route.SigningKey = *row.SigningKey
			} else if existing, ok := document.Webhook.Routes[domain]; ok {
				route.SigningKey = existing.SigningKey
			}

			if row.Timeout != nil {
				if *row.Timeout <= 0 {
					return fmt.Errorf("the timeout for %s must be positive", domain)
				}
				route.Timeout = config.Seconds(time.Duration(*row.Timeout * float64(time.Second)))
			}
			if row.VerifyTLS != nil {
				verify := *row.VerifyTLS
				route.VerifyTLS = &verify
			}

			routes[domain] = route
		}

		document.Webhook.Routes = routes
	}

	return nil
}

// testTarget is the body of POST /api/v1/webhook/test. The values come from
// the form rather than from what is saved, so a URL can be proved before
// anyone commits to it.
type testTarget struct {
	URL        string  `json:"url"`
	Format     string  `json:"format"`
	SigningKey string  `json:"signing_key"`
	VerifyTLS  *bool   `json:"verify_tls"`
	Timeout    float64 `json:"timeout_seconds"`
	Recipient  string  `json:"recipient"`
}

// testWebhook posts a synthetic message and reports what the app said.
//
// This is the fastest way to find out that a handler rejects the payload --
// a missing signature, a route behind CSRF, an app that is not running -- and
// it answers in the settings dialog rather than after composing a real reply.
func (s *Server) testWebhook(w http.ResponseWriter, r *http.Request) {
	if s.webhook == nil {
		s.respond(w, r, http.StatusNotImplemented, map[string]string{
			"error": "this server cannot deliver",
		})
		return
	}

	var target testTarget
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxSettingsBytes))
	if err := decoder.Decode(&target); err != nil {
		s.badRequest(w, r, "body must be JSON")
		return
	}

	target.URL = strings.TrimSpace(target.URL)
	if target.URL == "" {
		s.badRequest(w, r, "a URL is required")
		return
	}

	cfg := s.settings()

	route := config.Resolved{
		URL:        target.URL,
		Format:     firstNonEmpty(target.Format, cfg.Webhook.Format, config.DefaultFormat),
		SigningKey: target.SigningKey,
		Timeout:    cfg.Webhook.Timeout.Duration(),
		VerifyTLS:  cfg.VerifyTLS(),
	}
	if target.Timeout > 0 {
		route.Timeout = time.Duration(target.Timeout * float64(time.Second))
	}
	if route.Timeout <= 0 {
		route.Timeout = config.DefaultTimeout
	}
	if target.VerifyTLS != nil {
		route.VerifyTLS = *target.VerifyTLS
	}

	// A blank key means the form's field was left untouched, which for a
	// saved route means "use the stored one" -- otherwise testing an existing
	// route would send it unsigned and the app would rightly refuse.
	//
	// But only for a URL that is already configured. Signing a caller-chosen
	// URL with a stored key would make this endpoint a signing oracle: point
	// it at a server you control and Mailman hands you a valid HMAC over data
	// you chose, using a credential the browser is deliberately never shown.
	if route.SigningKey == "" {
		if stored, ok := cfg.Webhook.Routes[config.Domain(target.Recipient)]; ok && stored.URL == route.URL {
			route.SigningKey = stored.SigningKey
		} else if route.URL == cfg.Webhook.URL {
			route.SigningKey = cfg.Webhook.SigningKey
		}
	}

	message, raw, err := sampleMessage(target.Recipient)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	status, failure, err := s.webhook.Probe(r.Context(), message, raw, route)
	if err != nil {
		s.badRequest(w, r, err.Error())
		return
	}

	s.respond(w, r, http.StatusOK, map[string]any{
		"url":         route.URL,
		"format":      route.Format,
		"status_code": status,
		"error":       failure,
		"ok":          failure == "" && status >= 200 && status < 300,
	})
}

// sampleMessage builds the mail a test delivery describes. It is shaped like
// a real reply -- a plus-addressed recipient so MailboxHash and the order-ref
// mechanism are exercised, threading headers so a handler that gates on
// In-Reply-To sees one, and both a text and an HTML body so a handler that
// reads only one of them still finds content.
func sampleMessage(recipient string) (*mailstore.Message, []byte, error) {
	recipient = strings.TrimSpace(recipient)
	if recipient == "" {
		recipient = "order-1234+test@mail.example.test"
	}

	const (
		sender = "settings-test@mailman.local"
		parent = "mailman-test-parent@mailman.local"
		text   = "This is a test delivery from Mailman's settings page.\n\n" +
			"Nothing was captured and no reply was sent to a real mailbox."
	)

	now := time.Now()
	id := outbound.NewMessageID()

	built := outbound.Message{
		From:       "Mailman <" + sender + ">",
		To:         []string{recipient},
		Subject:    "Re: Mailman settings test",
		Text:       text,
		HTML:       outbound.TextToHTML(text),
		MessageID:  id,
		InReplyTo:  parent,
		References: []string{parent},
		Date:       now,
	}

	raw, err := built.Build()
	if err != nil {
		return nil, nil, fmt.Errorf("build test message: %w", err)
	}

	// Assembled rather than parsed back: these are the only fields the
	// payload builders read, and naming them here keeps the test delivery
	// obvious in the payload the app receives.
	message := &mailstore.Message{
		ID:         "settings-test",
		Direction:  mailstore.DirectionOutbound,
		MessageID:  id,
		InReplyTo:  parent,
		References: []string{parent},
		From:       []mailstore.Address{{Name: "Mailman", Address: sender}},
		To:         []mailstore.Address{{Address: recipient}},
		Subject:    built.Subject,
		SentAt:     now,
		TextBody:   text,
		HTMLBody:   built.HTML,
		SizeBytes:  int64(len(raw)),
		Envelope:   mailstore.Envelope{From: sender, Recipients: built.Recipients()},
		CreatedAt:  now,
	}

	return message, raw, nil
}
