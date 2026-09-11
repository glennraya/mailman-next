package probe

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPIdentifiesWhatIsAnswering(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		want       Owner
		wantDetail string
	}{
		{
			name:       "a Mailman naming its version",
			body:       `{"app":"mailman","status":"ok","version":"0.2.0"}`,
			want:       Mailman,
			wantDetail: "mailman 0.2.0",
		},
		{
			// A build from before the app field existed. Reading as
			// foreign is the safe direction: the worst case is a
			// slightly vaguer error message.
			name:       "an older Mailman without the app field",
			body:       `{"status":"ok","version":"0.1.0"}`,
			want:       Foreign,
			wantDetail: "an HTTP server (200 OK)",
		},
		{
			name:       "some other JSON API",
			body:       `{"app":"grafana"}`,
			want:       Foreign,
			wantDetail: "an HTTP server (200 OK)",
		},
		{
			name:       "a plain web server",
			body:       "<html>hello</html>",
			want:       Foreign,
			wantDetail: "an HTTP server (200 OK)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Write([]byte(tt.body))
			}))
			defer server.Close()

			got := HTTP(context.Background(), strings.TrimPrefix(server.URL, "http://"))
			if got.Owner != tt.want {
				t.Errorf("owner = %v, want %v", got.Owner, tt.want)
			}
			if got.Detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", got.Detail, tt.wantDetail)
			}
		})
	}
}

func TestHTTPReportsAnUnusedPortAsFree(t *testing.T) {
	// Bind and release, so the address is known to have been usable and is
	// now free again.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	if got := HTTP(context.Background(), addr); !got.Free() {
		t.Errorf("owner = %v, want free", got.Owner)
	}
}

// A listener that accepts and then says nothing still holds the port, and
// that is the case the whole check exists for -- a silent wildcard listener
// is exactly what a loopback bind fails to conflict with. Reporting it free
// would put Mailman back to starting alongside it and losing mail.
//
// It must also not hang startup: the probe's own timeout is the only thing
// standing between a wedged server and a Mailman that never finishes booting.
func TestHTTPReportsASilentServerAsForeign(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		// Hold the connection open without writing anything, until the
		// test closes the listener.
		defer conn.Close()
		buf := make([]byte, 1)
		conn.Read(buf)
	}()

	started := time.Now()
	got := HTTP(context.Background(), listener.Addr().String())

	if got.Owner != Foreign {
		t.Errorf("owner = %v, want foreign", got.Owner)
	}
	if elapsed := time.Since(started); elapsed > 2*timeout {
		t.Errorf("the probe took %v, which is long enough to stall startup", elapsed)
	}
}

func TestSMTPIdentifiesTheGreeting(t *testing.T) {
	tests := []struct {
		name   string
		banner string
		want   Owner
	}{
		{
			name:   "Mailman",
			banner: "220 mailman.local ESMTP Service Ready",
			want:   Mailman,
		},
		{
			// The two tools Mailman deliberately avoids the ports of.
			// Their banners are what a user will see quoted back.
			name:   "Mailpit",
			banner: "220 Mailpit ESMTP Service ready",
			want:   Foreign,
		},
		{
			name:   "MailHog",
			banner: "220 mailhog.example ESMTP MailHog",
			want:   Foreign,
		},
		{
			name:   "a real mail server",
			banner: "220 smtp.example.com ESMTP Postfix",
			want:   Foreign,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := greeter(t, tt.banner+"\r\n")

			got := SMTP(context.Background(), addr)
			if got.Owner != tt.want {
				t.Errorf("owner = %v, want %v", got.Owner, tt.want)
			}
			// The banner is quoted into the startup error, so it has
			// to survive the round trip without its line ending.
			if got.Detail != tt.banner {
				t.Errorf("detail = %q, want %q", got.Detail, tt.banner)
			}
		})
	}
}

// Something holding the port without speaking SMTP is still a reason not to
// start, so it must not be reported as free.
func TestSMTPReportsASilentListenerAsForeign(t *testing.T) {
	addr := greeter(t, "")

	got := SMTP(context.Background(), addr)
	if got.Owner != Foreign {
		t.Errorf("owner = %v, want foreign", got.Owner)
	}
}

func TestSMTPReportsAnUnusedPortAsFree(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	if got := SMTP(context.Background(), addr); !got.Free() {
		t.Errorf("owner = %v, want free", got.Owner)
	}
}

// greeter starts a listener that writes banner to the first connection and
// then waits to be closed, which is the shape of every SMTP server's opening
// move. An empty banner produces a listener that accepts and says nothing.
func greeter(t *testing.T, banner string) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if banner != "" {
			conn.Write([]byte(banner))
		}
		// Read until the probe's QUIT or its close, so the connection
		// stays open for as long as the probe wants it.
		buf := make([]byte, 64)
		conn.Read(buf)
	}()

	return listener.Addr().String()
}
