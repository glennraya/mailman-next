package config

import (
	"strings"
	"time"
)

// Resolved is a webhook target with every inherited field filled in, so the
// caller never has to know whether a value came from the route or the top
// level. This is what makes a route with nothing but a URL work: the shape
// the sender needs is complete either way.
type Resolved struct {
	URL        string
	Format     string
	SigningKey string
	Timeout    time.Duration
	VerifyTLS  bool

	// Domain is the route key that matched, empty when the top-level URL was
	// used as the fallback. The UI reports this, so a developer can see which
	// line of their config a reply actually followed.
	Domain string
}

// RouteFor resolves where a reply to the given recipient should be delivered.
// The recipient may be a full address or a bare domain.
//
// The order is exact key, then the longest matching wildcard, then the
// top-level URL. The second result is false when none of those produced a
// URL -- which is a real and expected state, not an error: Mailman runs fine
// with no webhook configured, and a reply is still stored and threaded. The
// caller's job is to say so rather than report a delivery that never
// happened.
func (c *Config) RouteFor(recipient string) (Resolved, bool) {
	domain := Domain(recipient)

	if domain != "" {
		if route, ok := c.Webhook.Routes[domain]; ok {
			return c.resolve(route, domain), true
		}
		if route, key, ok := c.wildcard(domain); ok {
			return c.resolve(route, key), true
		}
	}

	if c.Webhook.URL == "" {
		return Resolved{}, false
	}

	return Resolved{
		URL:        c.Webhook.URL,
		Format:     c.format(""),
		SigningKey: c.Webhook.SigningKey,
		Timeout:    c.timeout(0),
		VerifyTLS:  c.VerifyTLS(),
	}, true
}

// wildcard finds the most specific `*.` route covering a domain. Longest wins,
// so a `*.mail.myapp.test` route is not shadowed by a broader
// `*.myapp.test` one sitting in the same config.
func (c *Config) wildcard(domain string) (Route, string, bool) {
	var (
		best  Route
		key   string
		found bool
	)

	for pattern, route := range c.Webhook.Routes {
		suffix, ok := strings.CutPrefix(pattern, "*.")
		if !ok || suffix == "" {
			continue
		}

		// `*.example.test` covers the bare domain as well as its
		// subdomains. A developer who writes one pattern for a project does
		// not expect mail to the apex to fall through to something else.
		if domain != suffix && !strings.HasSuffix(domain, "."+suffix) {
			continue
		}

		if !found || len(suffix) > len(strings.TrimPrefix(key, "*.")) {
			best, key, found = route, pattern, true
		}
	}

	return best, key, found
}

// resolve fills a route's unset fields from the top level. The pointer and
// zero-value checks here are the whole reason Route uses `*bool` and a
// Seconds that can be zero: "unset" has to stay distinguishable from "off".
func (c *Config) resolve(route Route, domain string) Resolved {
	resolved := Resolved{
		URL:        route.URL,
		Format:     c.format(route.Format),
		SigningKey: route.SigningKey,
		Timeout:    c.timeout(route.Timeout),
		VerifyTLS:  c.VerifyTLS(),
		Domain:     domain,
	}

	if route.SigningKey == "" {
		resolved.SigningKey = c.Webhook.SigningKey
	}
	if route.VerifyTLS != nil {
		resolved.VerifyTLS = *route.VerifyTLS
	}

	return resolved
}

func (c *Config) format(format string) string {
	return firstNonEmpty(format, c.Webhook.Format, DefaultFormat)
}

// timeout falls back through the top level to the built-in default, because a
// zero timeout on an http.Client means "wait forever" -- and an app under
// test that never answers would hang the reply request that is waiting on it.
func (c *Config) timeout(timeout Seconds) time.Duration {
	for _, candidate := range []Seconds{timeout, c.Webhook.Timeout} {
		if candidate > 0 {
			return candidate.Duration()
		}
	}
	return DefaultTimeout
}

// Domain extracts the part a route is matched against. A full address is
// reduced to its domain; a bare domain is returned as-is. The last `@` wins,
// since a quoted local part may legally contain one.
func Domain(recipient string) string {
	recipient = strings.ToLower(strings.TrimSpace(recipient))
	if at := strings.LastIndex(recipient, "@"); at >= 0 {
		recipient = recipient[at+1:]
	}
	return strings.Trim(recipient, "[]")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
