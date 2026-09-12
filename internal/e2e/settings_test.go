package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/glennraya/mailman/internal/config"
	"github.com/glennraya/mailman/internal/events"
)

type settingsResponse struct {
	Home      string         `json:"home"`
	HTTPAddr  string         `json:"http_addr"`
	SMTPAddr  string         `json:"smtp_addr"`
	Overrides map[string]any `json:"overrides"`
	Editable  bool           `json:"editable"`
	Listening struct {
		HTTP         string `json:"http"`
		SMTP         string `json:"smtp"`
		NeedsRestart bool   `json:"needs_restart"`
	} `json:"listening"`
	Webhook struct {
		Enabled   bool   `json:"enabled"`
		URL       string `json:"url"`
		Format    string `json:"format"`
		HasSecret bool   `json:"has_secret"`
		VerifyTLS bool   `json:"verify_tls"`
		Routes    map[string]struct {
			URL       string `json:"url"`
			Format    string `json:"format"`
			HasSecret bool   `json:"has_secret"`
		} `json:"routes"`
	} `json:"webhook"`
}

func (i *instance) settings(t *testing.T) settingsResponse {
	t.Helper()

	var got settingsResponse
	i.get(t, "/api/v1/config", &got)
	return got
}

// save sends a settings patch and decodes the response into a fresh value.
//
// Fresh matters: encoding/json merges into an existing map rather than
// replacing it, so reusing one response across two saves would show the union
// of both route tables and quietly hide a removal.
func (i *instance) save(t *testing.T, patch any) (settingsResponse, int) {
	t.Helper()

	var got settingsResponse
	status := i.put(t, "/api/v1/config", patch, &got)
	return got, status
}

// document reads config.json off disk, which is where a save has to land for
// it to survive a restart.
func (i *instance) document(t *testing.T, home string) *config.Document {
	t.Helper()

	document, err := config.ReadDocument(config.DocumentPath(home))
	if err != nil {
		t.Fatalf("read document: %v", err)
	}
	return document
}

// TestSavingAWebhookURLTakesEffectWithoutARestart is the whole feature in one
// test: a fresh install with no config file, a URL saved through the API, and
// a reply reaching the app without the process being restarted.
func TestSavingAWebhookURLTakesEffectWithoutARestart(t *testing.T) {
	app := newFakeApp(t)
	m := bootSaved(t, nil)

	before := m.settings(t)
	if before.Webhook.Enabled {
		t.Fatal("a fresh install reported a webhook")
	}
	if !before.Editable {
		t.Fatal("settings reported as not editable, so the page would be read-only")
	}

	saved, status := m.save(t, map[string]any{
		"webhook": map[string]any{
			"url":         app.server.URL + "/inbound",
			"format":      config.FormatGeneric,
			"signing_key": "from-the-settings-page",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT returned %d, want 200", status)
	}

	if !saved.Webhook.Enabled || saved.Webhook.URL != app.server.URL+"/inbound" {
		t.Fatalf("the response does not reflect the save: %+v", saved.Webhook)
	}
	// The key went in but must never come back out.
	if !saved.Webhook.HasSecret {
		t.Error("has_secret is false after saving a key")
	}
	if strings.Contains(fmt.Sprint(saved), "from-the-settings-page") {
		t.Error("the signing key was echoed back")
	}

	// And now the reply loop, with no restart in between.
	parentID, _ := m.capture(t)

	var result replyResult
	if status := m.post(t, "/api/v1/replies", map[string]any{
		"parent_id": parentID,
		"text":      "Saved from the settings page.",
	}, &result); status != http.StatusCreated {
		t.Fatalf("POST /replies returned %d", status)
	}
	if !result.Routed || result.Delivery == nil {
		t.Fatalf("the saved URL was not used: %+v", result)
	}
	if result.Delivery.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", result.Delivery.StatusCode)
	}

	got := app.await(t)
	if !strings.Contains(string(got.Body), "Saved from the settings page.") {
		t.Errorf("the app did not receive the reply: %s", got.Body)
	}
}

func TestSavingRoutesAddsAndRemovesThem(t *testing.T) {
	m := bootSaved(t, nil)
	home := m.settings(t).Home

	added, status := m.save(t, map[string]any{
		"webhook": map[string]any{
			"routes": map[string]any{
				"mail.acme.test":  map[string]any{"url": "http://acme.test/hook", "format": config.FormatMailgun},
				"mail.other.test": map[string]any{"url": "http://other.test/hook"},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT returned %d", status)
	}

	if len(added.Webhook.Routes) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(added.Webhook.Routes), added.Webhook.Routes)
	}
	if len(m.document(t, home).Webhook.Routes) != 2 {
		t.Error("the routes did not reach the file, so they would not survive a restart")
	}

	// Removing a row means sending the table without it. Omission alone could
	// never delete one, because the loader merges routes per key.
	removed, status := m.save(t, map[string]any{
		"webhook": map[string]any{
			"routes": map[string]any{
				"mail.acme.test": map[string]any{"url": "http://acme.test/hook"},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("the removal returned %d", status)
	}

	if len(removed.Webhook.Routes) != 1 {
		t.Errorf("got %d routes after a removal, want 1: %+v", len(removed.Webhook.Routes), removed.Webhook.Routes)
	}
	if _, gone := m.document(t, home).Webhook.Routes["mail.other.test"]; gone {
		t.Error("the removed route is still in the file")
	}
}

// The page is never shown a signing key, so a row it round-trips carries
// none. Saving must not silently unsign every route.
func TestARouteKeepsItsKeyAcrossASaveThatDoesNotMentionIt(t *testing.T) {
	m := bootSaved(t, &config.Document{Webhook: config.Webhook{
		Routes: map[string]config.Route{
			"mail.acme.test": {URL: "http://acme.test/hook", SigningKey: "key-abc123"},
		},
	}})
	home := m.settings(t).Home

	kept, status := m.save(t, map[string]any{
		"webhook": map[string]any{
			"routes": map[string]any{
				// Exactly what the page sends: the row, with no key.
				"mail.acme.test": map[string]any{"url": "http://acme.test/hook", "format": config.FormatMailgun},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("PUT returned %d", status)
	}

	if route := kept.Webhook.Routes["mail.acme.test"]; !route.HasSecret {
		t.Error("has_secret is false, so the key was dropped")
	}
	if got := m.document(t, home).Webhook.Routes["mail.acme.test"].SigningKey; got != "key-abc123" {
		t.Errorf("stored key = %q, want it kept", got)
	}

	// An explicit empty string is how the page clears one.
	if _, status := m.save(t, map[string]any{
		"webhook": map[string]any{
			"routes": map[string]any{
				"mail.acme.test": map[string]any{"url": "http://acme.test/hook", "signing_key": ""},
			},
		},
	}); status != http.StatusOK {
		t.Fatalf("clearing the key returned %d", status)
	}

	if got := m.document(t, home).Webhook.Routes["mail.acme.test"].SigningKey; got != "" {
		t.Errorf("stored key = %q, want it cleared", got)
	}
}

// A field an environment variable holds cannot be saved. Accepting it would
// write the file and then have the reload overwrite it, answering with the
// old value for something the caller just changed.
func TestAShadowedFieldCannotBeSaved(t *testing.T) {
	m := bootSaved(t, nil)
	home := m.settings(t).Home

	// Set after boot and reloaded, which is what a save does anyway.
	t.Setenv("MAILMAN_WEBHOOK_URL", "http://from-env.test/inbound")
	m.put(t, "/api/v1/config", map[string]any{"webhook": map[string]any{"format": config.FormatPostmark}}, nil)

	got := m.settings(t)
	if got.Overrides["webhook.url"] != "MAILMAN_WEBHOOK_URL" {
		t.Fatalf("overrides = %v, want the variable named", got.Overrides)
	}

	var failure struct {
		Error string `json:"error"`
		Field string `json:"field"`
		SetBy string `json:"set_by"`
	}
	status := m.put(t, "/api/v1/config",
		map[string]any{"webhook": map[string]any{"url": "http://from-the-page.test/inbound"}}, &failure)

	if status != http.StatusConflict {
		t.Fatalf("status = %d, want 409", status)
	}
	if failure.SetBy != "MAILMAN_WEBHOOK_URL" || failure.Field != "webhook.url" {
		t.Errorf("failure = %+v, want it to name the variable and field", failure)
	}
	if url := m.document(t, home).Webhook.URL; url != "" {
		t.Errorf("the file was written anyway: url = %q", url)
	}
}

// An invalid setting is refused before anything is written, so a save can
// never leave behind a file the next startup refuses to load.
func TestAnInvalidSettingIsRefusedAndNothingIsWritten(t *testing.T) {
	m := bootSaved(t, &config.Document{Webhook: config.Webhook{URL: "http://good.test/inbound"}})
	home := m.settings(t).Home

	before, err := os.ReadFile(config.DocumentPath(home))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	cases := []map[string]any{
		{"webhook": map[string]any{"format": "sendgrid"}},
		{"webhook": map[string]any{"url": "myapp.test/inbound"}},
		{"smtp_addr": "127.0.0.1"},
		{"smtp_addr": "127.0.0.1:99999"},
		{"webhook": map[string]any{"routes": map[string]any{"a.test": map[string]any{"url": ""}}}},
		{"max_message_bytes": -1},
	}

	for _, body := range cases {
		if status := m.put(t, "/api/v1/config", body, nil); status != http.StatusBadRequest {
			t.Errorf("%v returned %d, want 400", body, status)
		}
	}

	after, err := os.ReadFile(config.DocumentPath(home))
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Errorf("the file changed despite every save being refused:\n%s\n%s", before, after)
	}
	if got := m.settings(t).Webhook.URL; got != "http://good.test/inbound" {
		t.Errorf("the live configuration changed: %q", got)
	}
}

// A port change is saved but cannot apply until a restart, and the page has
// to say so rather than report it as in effect.
func TestChangingAPortIsSavedButNotInEffect(t *testing.T) {
	m := bootSaved(t, nil)

	before := m.settings(t)
	if before.Listening.NeedsRestart {
		t.Fatal("a fresh install already wants a restart")
	}

	saved, status := m.save(t, map[string]any{"smtp_addr": "127.0.0.1:2525"})
	if status != http.StatusOK {
		t.Fatalf("PUT returned %d", status)
	}

	if saved.SMTPAddr != "127.0.0.1:2525" {
		t.Errorf("configured address = %q, want the saved one", saved.SMTPAddr)
	}
	if !saved.Listening.NeedsRestart {
		t.Error("needs_restart is false after moving a port")
	}
	// The bound address is still the real one, not the aspiration.
	if saved.Listening.SMTP != m.smtpAddr {
		t.Errorf("listening.smtp = %q, want the bound %q", saved.Listening.SMTP, m.smtpAddr)
	}

	// And capture still works on the port actually bound.
	m.send(t, "billing@acme.test", []string{"glenn@myapp.test"},
		mail("Still listening", "still@acme.test", ""))

	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	if len(list.Conversations) != 1 {
		t.Errorf("the old port stopped capturing: %d conversations", len(list.Conversations))
	}
}

// Other open tabs have to learn about a save, or a second window would keep
// showing settings that are no longer in effect.
func TestSavingAnnouncesItself(t *testing.T) {
	m := bootSaved(t, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws://"+m.httpAddr+"/api/v1/events", nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()

	var hello events.Event
	if err := wsjson.Read(ctx, conn, &hello); err != nil {
		t.Fatalf("read hello: %v", err)
	}

	m.put(t, "/api/v1/config", map[string]any{"webhook": map[string]any{"url": "http://myapp.test/inbound"}}, nil)

	var event events.Event
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if event.Type != events.ConfigChanged {
		t.Errorf("event type = %q, want %q", event.Type, events.ConfigChanged)
	}
}

// The test button proves a URL and records nothing: there is no stored
// message for an attempt to belong to, and the delivery log exists to
// describe real replies.
func TestTestingAURLReportsTheAnswerAndStoresNothing(t *testing.T) {
	app := newFakeApp(t, http.StatusOK, http.StatusInternalServerError)
	m := bootSaved(t, nil)

	var result struct {
		StatusCode int    `json:"status_code"`
		Error      string `json:"error"`
		OK         bool   `json:"ok"`
		Format     string `json:"format"`
	}

	if status := m.post(t, "/api/v1/webhook/test", map[string]any{
		"url":       app.server.URL + "/inbound",
		"format":    config.FormatMailgun,
		"recipient": "order-4471+t3h2@mail.acme.test",
	}, &result); status != http.StatusOK {
		t.Fatalf("test returned %d", status)
	}
	if !result.OK || result.StatusCode != http.StatusOK {
		t.Errorf("result = %+v, want a success", result)
	}

	// The app received a real, parseable message.
	got := app.await(t)
	if recipient := got.Form.Get("recipient"); recipient != "order-4471+t3h2@mail.acme.test" {
		t.Errorf("recipient = %q", recipient)
	}
	if got.Form.Get("In-Reply-To") == "" {
		t.Error("no In-Reply-To, so a handler gating on it would drop the test")
	}
	if body := got.Form.Get("body-html"); body == "" {
		t.Error("no body-html, so a handler reading only HTML would see nothing")
	}

	// A rejection is reported, not raised.
	if status := m.post(t, "/api/v1/webhook/test", map[string]any{
		"url": app.server.URL + "/inbound",
	}, &result); status != http.StatusOK {
		t.Fatalf("a refused test returned %d, want 200", status)
	}
	if result.OK {
		t.Error("a 500 was reported as ok")
	}

	// Nothing was captured and nothing was logged.
	var list conversationList
	m.get(t, "/api/v1/conversations", &list)
	if len(list.Conversations) != 0 {
		t.Errorf("testing a URL put %d conversations in the mailbox", len(list.Conversations))
	}

	// sampleMessage uses a fixed id, so this is the row a probe would have
	// written if it recorded one.
	attempts, err := m.store.ListDeliveries(context.Background(), "settings-test")
	if err != nil {
		t.Fatalf("list deliveries: %v", err)
	}
	if len(attempts) != 0 {
		t.Errorf("testing a URL recorded %d deliveries", len(attempts))
	}
}

// Testing before saving is the point: a URL can be proved without committing
// to it.
func TestTestingDoesNotSaveAnything(t *testing.T) {
	app := newFakeApp(t)
	m := bootSaved(t, nil)

	m.post(t, "/api/v1/webhook/test", map[string]any{"url": app.server.URL + "/inbound"}, nil)
	app.await(t)

	if got := m.settings(t); got.Webhook.Enabled || got.Webhook.URL != "" {
		t.Errorf("testing a URL saved it: %+v", got.Webhook)
	}
}

// A caller-supplied URL must never be signed with the stored key, or this
// endpoint becomes a signing oracle for a credential the browser is
// deliberately never shown.
func TestTestingAnUnsavedURLIsNotSignedWithTheStoredKey(t *testing.T) {
	app := newFakeApp(t)
	m := bootSaved(t, &config.Document{Webhook: config.Webhook{
		URL:        "http://configured.test/inbound",
		Format:     config.FormatMailgun,
		SigningKey: "the-stored-secret",
	}})

	m.post(t, "/api/v1/webhook/test", map[string]any{
		"url":    app.server.URL + "/somewhere-else",
		"format": config.FormatMailgun,
	}, nil)

	got := app.await(t)
	if signature := got.Form.Get("signature"); signature != "" {
		t.Errorf("signature = %q, want none for a URL the request chose", signature)
	}
	if strings.Contains(string(got.Body), "the-stored-secret") {
		t.Error("the stored key was sent")
	}
}

// Saving while the inbox is being read is the case Live exists for: readers
// range the routes map on their own goroutines while a save swaps it.
func TestSavingIsSafeWhileTheInboxIsRead(t *testing.T) {
	m := bootSaved(t, nil)

	done := make(chan struct{})
	failures := make(chan string, 16)

	for range 4 {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
				}

				response, err := http.Get(m.url("/api/v1/webhook/route?recipient=a@mail.acme.test"))
				if err != nil {
					failures <- err.Error()
					return
				}
				response.Body.Close()

				response, err = http.Get(m.url("/api/v1/config"))
				if err != nil {
					failures <- err.Error()
					return
				}
				json.NewDecoder(response.Body).Decode(&struct{}{})
				response.Body.Close()
			}
		}()
	}

	for i := range 40 {
		body := map[string]any{"webhook": map[string]any{
			"url": fmt.Sprintf("http://myapp.test/%d", i),
			"routes": map[string]any{
				"mail.acme.test": map[string]any{"url": fmt.Sprintf("http://acme.test/%d", i)},
			},
		}}
		if status := m.put(t, "/api/v1/config", body, nil); status != http.StatusOK {
			t.Fatalf("save %d returned %d", i, status)
		}
	}

	close(done)

	select {
	case failure := <-failures:
		t.Errorf("a reader failed during a save: %s", failure)
	default:
	}
}
