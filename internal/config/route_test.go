package config

import (
	"testing"
	"time"
)

// routed is the config the matching tests share: a fallback, two exact
// routes and two overlapping wildcards, which is the shape that makes
// precedence observable.
func routed() *Config {
	verify := false

	return &Config{
		Webhook: Webhook{
			URL:        "http://fallback.test/inbound",
			Format:     FormatGeneric,
			SigningKey: "top-level-key",
			Timeout:    Seconds(3 * time.Second),
			Routes: map[string]Route{
				"mail.myapp.test": {
					URL:        "http://myapp.test/mailgun",
					Format:     FormatMailgun,
					SigningKey: "route-key",
				},
				"*.myapp.test":      {URL: "http://myapp.test/wildcard"},
				"*.mail.myapp.test": {URL: "http://myapp.test/specific"},
				"insecure.test":     {URL: "https://insecure.test/inbound", VerifyTLS: &verify},
				"slow.test":         {URL: "http://slow.test/inbound", Timeout: Seconds(30 * time.Second)},
			},
		},
	}
}

func TestRouteForPrecedence(t *testing.T) {
	cfg := routed()

	cases := []struct {
		name      string
		recipient string
		wantURL   string
		// wantDomain is the route key that should have matched, empty for
		// the top-level fallback.
		wantDomain string
	}{
		{"exact key wins over wildcard", "order-4471@mail.myapp.test",
			"http://myapp.test/mailgun", "mail.myapp.test"},
		{"longest wildcard wins", "a@deep.mail.myapp.test",
			"http://myapp.test/specific", "*.mail.myapp.test"},
		{"wildcard covers a subdomain", "a@other.myapp.test",
			"http://myapp.test/wildcard", "*.myapp.test"},
		{"wildcard covers the bare domain", "a@myapp.test",
			"http://myapp.test/wildcard", "*.myapp.test"},
		{"unmatched domain falls back", "a@elsewhere.test",
			"http://fallback.test/inbound", ""},
		{"a bare domain resolves like an address", "myapp.test",
			"http://myapp.test/wildcard", "*.myapp.test"},
		{"matching ignores case", "A@MAIL.MyApp.Test",
			"http://myapp.test/mailgun", "mail.myapp.test"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			route, ok := cfg.RouteFor(c.recipient)
			if !ok {
				t.Fatalf("RouteFor(%q) found nothing", c.recipient)
			}
			if route.URL != c.wantURL {
				t.Errorf("url = %q, want %q", route.URL, c.wantURL)
			}
			if route.Domain != c.wantDomain {
				t.Errorf("domain = %q, want %q", route.Domain, c.wantDomain)
			}
		})
	}
}

func TestRouteForInheritsUnsetFields(t *testing.T) {
	cfg := routed()

	// A route with nothing but a URL: every other field comes from the top
	// level, which is what lets a project speaking the default format
	// configure one line.
	bare, ok := cfg.RouteFor("a@other.myapp.test")
	if !ok {
		t.Fatal("wildcard route not found")
	}
	if bare.Format != FormatGeneric {
		t.Errorf("format = %q, want inherited %q", bare.Format, FormatGeneric)
	}
	if bare.SigningKey != "top-level-key" {
		t.Errorf("signing key = %q, want inherited", bare.SigningKey)
	}
	if bare.Timeout != 3*time.Second {
		t.Errorf("timeout = %s, want inherited 3s", bare.Timeout)
	}
	if !bare.VerifyTLS {
		t.Error("TLS verification inherited as off, want on")
	}

	// A route that sets them keeps its own.
	own, ok := cfg.RouteFor("a@mail.myapp.test")
	if !ok {
		t.Fatal("exact route not found")
	}
	if own.Format != FormatMailgun {
		t.Errorf("format = %q, want own %q", own.Format, FormatMailgun)
	}
	if own.SigningKey != "route-key" {
		t.Errorf("signing key = %q, want own", own.SigningKey)
	}

	slow, _ := cfg.RouteFor("a@slow.test")
	if slow.Timeout != 30*time.Second {
		t.Errorf("timeout = %s, want own 30s", slow.Timeout)
	}
}

// A route turning verification off has to beat an inherited on. This is the
// one field where "unset" and "false" are different answers, and getting it
// wrong would silently re-enable verification against a self-signed
// development certificate.
func TestRouteForVerifyTLSOverridesAnInheritedTrue(t *testing.T) {
	cfg := routed()

	route, ok := cfg.RouteFor("a@insecure.test")
	if !ok {
		t.Fatal("route not found")
	}
	if route.VerifyTLS {
		t.Error("TLS verification stayed on, want the route's explicit off")
	}
}

func TestRouteForWithNothingConfigured(t *testing.T) {
	cfg := &Config{}

	if _, ok := cfg.RouteFor("a@anywhere.test"); ok {
		t.Error("RouteFor found a target with no webhook configured")
	}
}

// A route matching but the fallback missing must still resolve: the route is
// the answer, and "no fallback" says nothing about it.
func TestRouteForNeedsNoFallback(t *testing.T) {
	cfg := &Config{
		Webhook: Webhook{
			Routes: map[string]Route{"myapp.test": {URL: "http://myapp.test/inbound"}},
		},
	}

	route, ok := cfg.RouteFor("a@myapp.test")
	if !ok {
		t.Fatal("route not found")
	}
	if route.Format != DefaultFormat {
		t.Errorf("format = %q, want %q", route.Format, DefaultFormat)
	}
	if route.Timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", route.Timeout, DefaultTimeout)
	}

	if _, ok := cfg.RouteFor("a@elsewhere.test"); ok {
		t.Error("an unmatched domain resolved with no fallback URL")
	}
}

func TestDomain(t *testing.T) {
	cases := map[string]string{
		"order-4471@mail.myapp.test": "mail.myapp.test",
		"MyApp.Test":                 "myapp.test",
		`"weird@local"@myapp.test`:   "myapp.test",
		"  a@myapp.test  ":           "myapp.test",
		"a@[127.0.0.1]":              "127.0.0.1",
		"":                           "",
	}

	for recipient, want := range cases {
		if got := Domain(recipient); got != want {
			t.Errorf("Domain(%q) = %q, want %q", recipient, got, want)
		}
	}
}
