package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// configMode is deliberately tighter than the 0o644 the rest of Mailman uses
// for generated files. This one can hold webhook.signing_key, which is a
// credential for the app under test -- and on a shared machine a
// world-readable one is a credential leak for the price of a default.
const configMode = 0o600

// ReadDocument reads config.json.
//
// A missing file is a zero Document rather than an error: Mailman runs fine
// without one, and the settings page has to be able to write the first.
func ReadDocument(path string) (*Document, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Document{Webhook: Webhook{Routes: map[string]Route{}}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var document Document
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if document.Webhook.Routes == nil {
		document.Webhook.Routes = map[string]Route{}
	}
	return &document, nil
}

// WriteDocument replaces config.json.
//
// The write goes to a temporary file in the same directory and is then
// renamed over the target, because rename within a directory is atomic: a
// reader either sees the whole previous file or the whole new one. A plain
// truncate-and-write can be interrupted, and a half-written config.json is a
// file Mailman refuses to start from -- a settings save must not be able to
// leave the installation unbootable.
func WriteDocument(path string, document *Document) error {
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return fmt.Errorf("encode configuration: %w", err)
	}
	encoded = append(encoded, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	// Same directory as the target, so the rename cannot cross a filesystem
	// boundary and fall back to a copy.
	temp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("create temporary file in %s: %w", dir, err)
	}
	name := temp.Name()

	// Any failure from here on leaves the temporary file behind, so the
	// cleanup runs on every path that does not end in a successful rename.
	defer os.Remove(name)

	if err := temp.Chmod(configMode); err != nil {
		temp.Close()
		return fmt.Errorf("set permissions on %s: %w", name, err)
	}
	if _, err := temp.Write(encoded); err != nil {
		temp.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	// Flush before the rename. Without this the rename can be durable while
	// the contents are not, which is how a crash produces an empty file that
	// parses as nothing.
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("flush %s: %w", name, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}

	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// Validate checks a document on its own terms, before it is written.
//
// Deliberately stricter than the resolved Config's validation. normalize()
// quietly drops a route with no domain or no URL, which is the forgiving
// thing to do with a hand-edited file and the wrong thing to do with a row
// someone just typed into a form -- there, silence means the row vanishes on
// save with no explanation. And an unparseable listen address is worse still:
// once it is on disk the next startup fails, and there is no settings page
// left to correct it from.
func (d *Document) Validate() error {
	if err := validAddr("http_addr", d.HTTPAddr); err != nil {
		return err
	}
	if err := validAddr("smtp_addr", d.SMTPAddr); err != nil {
		return err
	}
	if d.MaxMessageBytes < 0 {
		return errors.New("max message size cannot be negative")
	}

	if d.Webhook.Format != "" {
		if err := validFormat(d.Webhook.Format); err != nil {
			return err
		}
	}
	if err := validTarget("webhook url", d.Webhook.URL); err != nil {
		return err
	}
	if d.Webhook.Timeout < 0 {
		return errors.New("webhook timeout cannot be negative")
	}

	for domain, route := range d.Webhook.Routes {
		if strings.TrimSpace(domain) == "" {
			return errors.New("a route needs a domain")
		}
		if strings.TrimSpace(route.URL) == "" {
			return fmt.Errorf("the route for %s needs a URL", domain)
		}
		if err := validTarget(fmt.Sprintf("the route for %s", domain), route.URL); err != nil {
			return err
		}
		if route.Format != "" {
			if err := validFormat(route.Format); err != nil {
				return fmt.Errorf("route %q: %w", domain, err)
			}
		}
		if route.Timeout < 0 {
			return fmt.Errorf("the timeout for %s cannot be negative", domain)
		}
	}

	return nil
}

// validAddr accepts an empty address, which falls back to the default, and
// otherwise requires something net.Listen could use. Port 0 stays legal: the
// operating system picks one, which is how the tests bind.
func validAddr(field, addr string) error {
	if strings.TrimSpace(addr) == "" {
		return nil
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s must look like host:port, not %q", field, addr)
	}
	if host == "" {
		return fmt.Errorf("%s needs a host, so that Mailman does not listen on every interface by accident", field)
	}

	number, err := strconv.Atoi(port)
	if err != nil || number < 0 || number > 65535 {
		return fmt.Errorf("%s has an invalid port %q", field, port)
	}
	return nil
}

// validTarget accepts an empty URL -- having no fallback is a real state, and
// the UI says so rather than pretending a reply was delivered -- and
// otherwise requires an absolute http or https URL, since that is the only
// thing the sender can post to.
func validTarget(field, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s is not a valid URL: %v", field, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must start with http:// or https://", field)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s needs a host", field)
	}
	return nil
}
