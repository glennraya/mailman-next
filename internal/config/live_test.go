package config

import (
	"fmt"
	"sync"
	"testing"
)

func TestLiveGetReturnsTheLatestSnapshot(t *testing.T) {
	first := &Config{Webhook: Webhook{URL: "http://first.test"}}
	live := NewLive(first)

	if live.Get() != first {
		t.Error("Get did not return the published snapshot")
	}

	second := &Config{Webhook: Webhook{URL: "http://second.test"}}
	live.Set(second)

	if got := live.Get(); got.Webhook.URL != "http://second.test" {
		t.Errorf("url = %q, want the new snapshot", got.Webhook.URL)
	}
}

// The reason Live exists. Under -race, readers ranging the routes map while
// saves swap the snapshot must be clean; the same test against a config
// mutated in place would fail.
func TestLiveSurvivesConcurrentReadsAndSwaps(t *testing.T) {
	live := NewLive(snapshot(0))

	const readers = 8
	const swaps = 200

	var wait sync.WaitGroup
	stop := make(chan struct{})

	for range readers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				// Exactly what the handlers do: resolve a route, which
				// ranges the map, and read the derived helpers.
				cfg := live.Get()
				cfg.RouteFor("someone@a.myapp.test")
				cfg.WebhookEnabled()
				cfg.VerifyTLS()
				for domain := range cfg.Webhook.Routes {
					_ = domain
				}
			}
		}()
	}

	for i := 1; i <= swaps; i++ {
		live.Set(snapshot(i))
	}

	close(stop)
	wait.Wait()
}

// snapshot builds a distinct configuration per generation, so a reader that
// held on to an old pointer is still reading a coherent value.
func snapshot(generation int) *Config {
	verify := generation%2 == 0

	routes := map[string]Route{
		"*.myapp.test": {URL: fmt.Sprintf("http://myapp.test/%d", generation)},
	}
	for i := range 4 {
		domain := fmt.Sprintf("mail%d.myapp.test", i)
		routes[domain] = Route{URL: fmt.Sprintf("http://myapp.test/%d/%d", generation, i)}
	}

	return &Config{
		Webhook: Webhook{
			URL:       fmt.Sprintf("http://fallback.test/%d", generation),
			Format:    FormatGeneric,
			VerifyTLS: &verify,
			Routes:    routes,
		},
	}
}
