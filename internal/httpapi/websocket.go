package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/glennraya/mailman/internal/events"
)

const (
	// pingInterval is how often a silent connection is probed. An inbox can
	// sit untouched for hours, and without traffic neither side would notice
	// the other had gone.
	pingInterval = 30 * time.Second

	// writeTimeout bounds a single frame. A browser that has stopped reading
	// must not pin the goroutine.
	writeTimeout = 10 * time.Second
)

// streamEvents pushes mailbox changes to one client for as long as it stays
// connected.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// The request's own origin is always allowed, so a built binary
		// serving the SPA from this same address needs nothing here. These
		// cover the Vite dev server, which is a different port and so a
		// different origin.
		OriginPatterns: []string{"localhost:5173", "127.0.0.1:5173"},
	})
	if err != nil {
		s.logger.Debug("websocket upgrade refused", "error", err)
		return
	}
	defer conn.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	stream, unsubscribe := s.broker.Subscribe()
	defer unsubscribe()

	// This socket only ever writes: the browser sends nothing after the
	// handshake. Without a reader running, close frames are never processed
	// and pongs never arrive, so a client that went away would hold its
	// subscription until the process exited. CloseRead runs that reader in
	// the background and cancels ctx the moment the peer disconnects.
	ctx = conn.CloseRead(ctx)

	// An immediate hello lets the client render the unread badge without a
	// separate request, and confirms the stream is live.
	if unread, err := s.store.CountUnread(ctx); err == nil {
		if !s.write(ctx, conn, events.Event{Type: "hello", Unread: unread}) {
			return
		}
	}

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case event, open := <-stream:
			if !open {
				return
			}
			if !s.write(ctx, conn, event) {
				return
			}

		case <-ticker.C:
			pingCtx, cancelPing := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pingCtx)
			cancelPing()

			if err != nil {
				s.logger.Debug("websocket ping failed, closing", "error", err)
				return
			}
		}
	}
}

// write sends one event, reporting whether the connection is still usable.
func (s *Server) write(ctx context.Context, conn *websocket.Conn, event events.Event) bool {
	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	if err := wsjson.Write(writeCtx, conn, event); err != nil {
		// A client closing a tab is the normal way this ends, not a fault.
		if !errors.Is(err, context.Canceled) {
			s.logger.Debug("websocket write failed", "error", err)
		}
		return false
	}
	return true
}
