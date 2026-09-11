// Package probe answers a narrow question: when something is already
// listening on one of Mailman's ports, what is it?
//
// A plain dial only proves that someone is there. That is enough to refuse to
// start, but not enough to say anything useful about it. "Mailman is already
// running, open the inbox" and "Mailpit has your SMTP port" are the same
// failed dial and completely different problems, and the second one is the
// first thing a new user hits. Both protocols happen to identify themselves
// for free -- the health endpoint names the application, and an SMTP server
// announces itself in its greeting -- so asking costs one short round trip.
package probe

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// timeout bounds every probe. These are loopback ports: anything that has not
// answered in this long is not going to, and startup should not hang on it.
const timeout = 300 * time.Millisecond

// Owner says who holds a port.
type Owner int

const (
	// Free means nothing answered -- the port is available.
	Free Owner = iota
	// Mailman means another Mailman is already serving here.
	Mailman
	// Foreign means something answered, and it is not Mailman.
	Foreign
)

func (o Owner) String() string {
	switch o {
	case Free:
		return "free"
	case Mailman:
		return "mailman"
	default:
		return "foreign"
	}
}

// Result is what a probe found. Detail carries whatever the other end said
// about itself -- a version for Mailman, a greeting banner for a foreign SMTP
// server -- so an error message can quote it rather than guess.
type Result struct {
	Owner  Owner
	Detail string
}

// Free reports whether the port can be taken.
func (r Result) Free() bool { return r.Owner == Free }

// HTTP probes Mailman's inbox port by asking the health endpoint who it is.
//
// Whether the port is taken is decided by the dial alone, never by the
// request. Something can hold a port and answer nothing -- and that case is
// precisely the one this whole check exists for, since a silent wildcard
// listener is what a loopback bind fails to conflict with. Only the
// identification depends on getting an answer.
//
// Anything that answers is Foreign unless it names itself as Mailman: a
// random web server on the port is still a reason not to start, and
// mistaking one for a Mailman would be worse than not recognising a Mailman.
func HTTP(ctx context.Context, addr string) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{Owner: Free}
	}
	conn.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return Result{Owner: Foreign, Detail: "a server that could not be identified"}
	}

	// A dedicated client, so a probe never picks up proxy settings or
	// connection reuse from the default transport.
	client := &http.Client{Transport: &http.Transport{}}
	defer client.CloseIdleConnections()

	resp, err := client.Do(req)
	if err != nil {
		// The dial above already proved something is there; it simply
		// does not speak HTTP, or would not answer in time.
		return Result{Owner: Foreign, Detail: "a server that sent no HTTP response"}
	}
	defer resp.Body.Close()

	var health struct {
		App     string `json:"app"`
		Version string `json:"version"`
	}
	// A body that is not JSON, or is JSON without the app field, leaves the
	// zero value and falls through to Foreign.
	_ = json.NewDecoder(resp.Body).Decode(&health)

	if health.App == "mailman" {
		detail := "mailman"
		if health.Version != "" {
			detail += " " + health.Version
		}
		return Result{Owner: Mailman, Detail: detail}
	}

	return Result{Owner: Foreign, Detail: fmt.Sprintf("an HTTP server (%s)", resp.Status)}
}

// SMTP probes the capture port by reading the greeting.
//
// Every SMTP server sends a 220 line naming itself before it will accept a
// command, which makes this the cheapest identification available. Mailman's
// own greeting carries mailmanDomain because internal/smtpd sets it as the
// server domain; Mailpit and MailHog put their own names there, so a foreign
// banner is worth quoting back verbatim.
func SMTP(ctx context.Context, addr string) Result {
	var dialer net.Dialer
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return Result{Owner: Free}
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && banner == "" {
		// Something accepted the connection but said nothing in time. It
		// is not an SMTP server, but it does hold the port.
		return Result{Owner: Foreign, Detail: "a server that sent no SMTP greeting"}
	}
	banner = strings.TrimRight(banner, "\r\n")

	// Leave politely, so the other end logs a clean session rather than a
	// dropped connection.
	_, _ = conn.Write([]byte("QUIT\r\n"))

	if strings.Contains(banner, mailmanDomain) {
		return Result{Owner: Mailman, Detail: banner}
	}
	return Result{Owner: Foreign, Detail: banner}
}

// mailmanDomain is the SMTP server domain set in internal/smtpd, which go-smtp
// puts into the greeting. It is duplicated rather than imported so that a
// probe -- which may be talking to an entirely different build of Mailman --
// does not drag the whole SMTP server into anything that wants to ask a
// question about a port.
const mailmanDomain = "mailman.local"
