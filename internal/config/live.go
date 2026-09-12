package config

import "sync/atomic"

// Live holds the configuration the running process reads.
//
// A published snapshot is immutable. Saving settings builds a whole new
// Config and swaps the pointer, rather than writing into the one already in
// use -- which matters because the request handlers range Webhook.Routes, and
// ranging a map another goroutine is writing is a data race the race detector
// will and should fail the build over.
//
// The cost of the discipline is one allocation per save, on a page a
// developer touches a handful of times. The alternative -- a mutex around
// every read, including the route lookup that fires on each keystroke in the
// compose form -- buys nothing in return.
type Live struct {
	snapshot atomic.Pointer[Config]
}

// NewLive publishes the first snapshot.
func NewLive(cfg *Config) *Live {
	live := &Live{}
	live.snapshot.Store(cfg)
	return live
}

// Get returns the current snapshot. Callers should read it once and use that
// one value for the whole of a request, so a save landing midway cannot show
// them half of one configuration and half of another.
func (l *Live) Get() *Config { return l.snapshot.Load() }

// Set publishes a new snapshot. The Config passed in must not be mutated
// afterwards by anyone.
func (l *Live) Set(cfg *Config) { l.snapshot.Store(cfg) }
