// Package httpapi serves the inbox: a JSON API for scripting and CI, a
// WebSocket that pushes changes as they happen, and the embedded single-page
// app on everything else.
//
// There is no authentication. Mailman binds to loopback and holds a
// developer's own test mail; a login would be a lock on an empty room. What
// the server does defend against is a web page the developer happens to have
// open reaching in from another origin -- see the cross-origin protection in
// Handler.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
	"github.com/glennraya/mailman/internal/mailstore"
)

// Ingestor is how the API injects mail, for the CI endpoint and, later, for
// replies. It matches the ingest pipeline's signature.
type Ingestor interface {
	Ingest(ctx context.Context, raw []byte, envelope mailstore.Envelope, direction string) (*mailstore.Message, error)
}

// Options wires the server.
type Options struct {
	Store    *mailstore.Store
	Broker   *events.Broker
	Config   *config.Config
	Ingestor Ingestor
	Assets   http.Handler
	Version  string
	Logger   *slog.Logger
}

// Server holds the API's dependencies.
type Server struct {
	store    *mailstore.Store
	broker   *events.Broker
	config   *config.Config
	ingestor Ingestor
	assets   http.Handler
	version  string
	logger   *slog.Logger
}

// New builds the server.
func New(opts Options) *Server {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Server{
		store:    opts.Store,
		broker:   opts.Broker,
		config:   opts.Config,
		ingestor: opts.Ingestor,
		assets:   opts.Assets,
		version:  opts.Version,
		logger:   logger,
	}
}

// Handler returns the routed, wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)

	mux.HandleFunc("GET /api/v1/config", s.getConfig)

	mux.HandleFunc("GET /api/v1/conversations", s.listConversations)
	mux.HandleFunc("GET /api/v1/conversations/{id}", s.getConversation)
	mux.HandleFunc("DELETE /api/v1/conversations/{id}", s.deleteConversation)
	mux.HandleFunc("POST /api/v1/conversations/{id}/seen", s.markConversationSeen)

	mux.HandleFunc("POST /api/v1/messages", s.injectMessage)
	mux.HandleFunc("DELETE /api/v1/messages", s.clearMailbox)
	mux.HandleFunc("GET /api/v1/messages/{id}", s.getMessage)
	mux.HandleFunc("DELETE /api/v1/messages/{id}", s.deleteMessage)
	mux.HandleFunc("GET /api/v1/messages/{id}/raw", s.getMessageRaw)
	mux.HandleFunc("GET /api/v1/messages/{id}/html", s.getMessageHTML)
	mux.HandleFunc("POST /api/v1/messages/{id}/seen", s.markMessageSeen)

	mux.HandleFunc("GET /api/v1/attachments/{id}", s.getAttachment)

	mux.HandleFunc("GET /api/v1/events", s.streamEvents)

	if s.assets != nil {
		mux.Handle("GET /", s.assets)
	}

	// Mailman answers on localhost while the developer browses the whole
	// internet. Without this, any page they visit could POST to the API and
	// wipe the mailbox -- the browser would attach no credentials, but the
	// API needs none. Safe methods are always allowed, so the SPA and the
	// WebSocket handshake are unaffected.
	protection := http.NewCrossOriginProtection()
	for _, origin := range devOrigins {
		// The Vite dev server runs on a different port, which makes every
		// request from it cross-origin.
		protection.AddTrustedOrigin(origin)
	}

	return s.recover(s.log(protection.Handler(mux)))
}

// devOrigins are the Vite dev server's addresses. They are trusted so the
// hot-reloading UI can talk to a real server; in a built binary the SPA is
// served from this same origin and none of these apply.
var devOrigins = []string{
	"http://localhost:5173",
	"http://127.0.0.1:5173",
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	unread, err := s.store.CountUnread(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.respond(w, r, http.StatusOK, map[string]any{
		"status":    "ok",
		"version":   s.version,
		"unread":    unread,
		"listeners": s.broker.Subscribers(),
	})
}

// getConfig reports the settings the UI needs to explain itself: which
// webhook routes exist, and therefore where a reply to a given address would
// be delivered. Signing keys are omitted -- the UI never needs them, and they
// would otherwise be readable by anything that can reach the API.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	routes := make(map[string]any, len(s.config.Webhook.Routes))
	for domain, route := range s.config.Webhook.Routes {
		routes[domain] = map[string]any{
			"url":        route.URL,
			"format":     firstNonEmpty(route.Format, s.config.Webhook.Format),
			"has_secret": route.SigningKey != "" || s.config.Webhook.SigningKey != "",
		}
	}

	s.respond(w, r, http.StatusOK, map[string]any{
		"version":           s.version,
		"smtp_addr":         s.config.SMTPAddr,
		"http_addr":         s.config.HTTPAddr,
		"home":              s.config.Home,
		"max_message_bytes": s.config.MaxMessageBytes,
		"webhook": map[string]any{
			"enabled":    s.config.WebhookEnabled(),
			"url":        s.config.Webhook.URL,
			"format":     s.config.Webhook.Format,
			"verify_tls": s.config.VerifyTLS(),
			"timeout_ms": s.config.Webhook.Timeout.Duration().Milliseconds(),
			"routes":     routes,
		},
	})
}

// respond writes a JSON body. Encoding into a buffer first means a failure
// halfway through does not produce a 200 with a truncated body.
func (s *Server) respond(w http.ResponseWriter, r *http.Request, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		s.logger.Error("could not encode response", "path", r.URL.Path, "error", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	w.Write(encoded)
}

// fail maps an error to a status. A missing row is a 404; anything else is
// logged in full and reported as a generic 500, since the client can do
// nothing useful with an internal message.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, mailstore.ErrNotFound) {
		s.respond(w, r, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	s.logger.Error("request failed",
		"method", r.Method, "path", r.URL.Path, "error", err)
	s.respond(w, r, http.StatusInternalServerError, map[string]string{"error": "internal error"})
}

func (s *Server) badRequest(w http.ResponseWriter, r *http.Request, message string) {
	s.respond(w, r, http.StatusBadRequest, map[string]string{"error": message})
}

// log records one line per request. Static assets are skipped so a page load
// does not bury the SMTP and API lines that matter.
func (s *Server) log(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(recorder, r)

		if !strings.HasPrefix(r.URL.Path, "/api/") && r.URL.Path != "/healthz" {
			return
		}

		s.logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration", time.Since(started))
	})
}

// recover turns a panic into a 500 rather than a dropped connection and a
// dead server. One malformed message must not take the inbox down.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			p := recover()
			if p == nil {
				return
			}
			// A panic after the response started cannot be turned into a
			// clean error; log it and let the connection break.
			s.logger.Error("panic serving request",
				"method", r.Method, "path", r.URL.Path,
				"panic", p, "stack", string(debug.Stack()))

			if recorder, ok := w.(*statusRecorder); !ok || !recorder.written {
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for logging and lets the panic
// handler tell whether a response has already begun.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.written {
		return
	}
	r.status = status
	r.written = true
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// Unwrap lets the WebSocket upgrade reach the underlying ResponseWriter,
// which must implement http.Hijacker.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func parseID(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
