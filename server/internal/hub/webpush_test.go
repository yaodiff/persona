package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/webpush"
	"github.com/nwverse/persona/server/internal/webpush/webpushtest"
)

// fakePushService is a push service over TLS: it keeps what it is sent and answers with the statuses it is told to
// (then 201 Created).
type fakePushService struct {
	srv      *httptest.Server
	mu       sync.Mutex
	statuses []int
	got      chan pushedRequest
}

type pushedRequest struct {
	path   string
	header http.Header
	body   []byte
}

func newFakePushService(t *testing.T) *fakePushService {
	t.Helper()
	f := &fakePushService{got: make(chan pushedRequest, 64)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		status := http.StatusCreated
		if len(f.statuses) > 0 {
			status, f.statuses = f.statuses[0], f.statuses[1:]
		}
		f.mu.Unlock()
		f.got <- pushedRequest{path: r.URL.Path, header: r.Header.Clone(), body: body}
		w.WriteHeader(status)
		if status >= 400 {
			_, _ = io.WriteString(w, "refused for the test")
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushService) answer(statuses ...int) {
	f.mu.Lock()
	f.statuses = append(f.statuses, statuses...)
	f.mu.Unlock()
}

func (f *fakePushService) next(t *testing.T) pushedRequest {
	t.Helper()
	select {
	case r := <-f.got:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("no push reached the push service")
	}
	return pushedRequest{}
}

func (f *fakePushService) none(t *testing.T) {
	t.Helper()
	select {
	case r := <-f.got:
		t.Fatalf("a push went out: %s %s", r.path, r.header)
	case <-time.After(150 * time.Millisecond):
	}
}

// browser subscribes a browser at the fake service under path.
func (f *fakePushService) browser(t *testing.T, path string) *webpushtest.Browser {
	t.Helper()
	b, err := webpushtest.NewBrowser(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// webPushHub is a test hub whose Web Push posts to f, without waits between tries.
func webPushHub(t *testing.T, f *fakePushService) *Hub {
	t.Helper()
	h := newTestHub(t)
	w := h.push.web
	w.client = f.srv.Client()
	host, _ := url.Parse(f.srv.URL)
	w.check = func(raw string) (string, error) {
		if u, err := url.Parse(raw); err == nil && u.Host == host.Host {
			return u.Hostname(), nil
		}
		return webpush.CheckEndpoint(raw)
	}
	w.retryWaits = nil
	return h
}

// subscribeBody is what a page PUTs for b.
func subscribeBody(t *testing.T, h *Hub, b *webpushtest.Browser, extra string) string {
	t.Helper()
	key, err := h.push.web.key(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"endpoint":%q,"keys":{"p256dh":%q,"auth":%q},"key":%q,"locale":"en","browser":"Chrome on macOS"%s}`,
		b.Sub.Endpoint, b.Sub.P256DH, b.Sub.Auth, key.Public, extra)
}

func subscribe(t *testing.T, h *Hub, mux http.Handler, device string, b *webpushtest.Browser, extra string) map[string]any {
	t.Helper()
	code, out := call(t, mux, "PUT", "/api/v1/push/web/"+device, h.cfg.Token, subscribeBody(t, h, b, extra))
	if code != 200 || out["subscribed"] != true {
		t.Fatalf("subscribe %s: %d %v", device, code, out)
	}
	return out
}

// opened is what b reads out of a push.
func webOpened(t *testing.T, b *webpushtest.Browser, r pushedRequest) map[string]any {
	t.Helper()
	plain, err := b.Open(r.body)
	if err != nil {
		t.Fatalf("open push: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(plain, &msg); err != nil {
		t.Fatalf("%s: %v", plain, err)
	}
	return msg
}

// A browser subscribes with the hub's key and gets a finished turn and a card as the phone's push words them,
// encrypted to it, with a VAPID token of the hub's key, a TTL, an urgency and a topic per notification.
func TestWebPushDeliversNotifyEventsToASubscribedBrowser(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	mux := h.routes()
	code, out := call(t, mux, "GET", "/api/v1/push/web", h.cfg.Token, "")
	key, _ := out["key"].(string)
	if code != 200 || len(key) != 87 { // 65 bytes, base64url
		t.Fatalf("key: %d %v", code, out)
	}
	b := f.browser(t, "/push/one")
	st := subscribe(t, h, mux, "web-one", b, `,"features":["elicitation","other"]`)
	if st["current"] != true || st["browser"] != "Chrome on macOS" || st["host"] != "127.0.0.1" {
		t.Fatalf("status %v", st)
	}
	if s, _ := h.push.web.get("web-one"); len(s.Features) != 1 || s.Features[0] != "elicitation" || s.Locale != "en" {
		t.Fatalf("kept %+v", s)
	}

	h.notifyDone(assistantwire.AssistantID, "done", "m1", "Laptop", "s1", "Fix the login", "All tests pass", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	r := f.next(t)
	msg := webOpened(t, b, r)
	want := donePayloadIn(i18n.En, "m1", "Laptop", "s1", "Fix the login", "All tests pass")["aps"].(map[string]any)["alert"].(map[string]any)
	if msg["kind"] != "done" || msg["machine_id"] != "m1" || msg["session_id"] != "s1" || msg["title"] != want["title"] || msg["body"] != want["body"] || msg["quiet"] != nil {
		t.Fatalf("done push %v, alert %v", msg, want)
	}
	if r.path != "/push/one" || r.header.Get("Content-Encoding") != "aes128gcm" || r.header.Get("TTL") != "1800" ||
		r.header.Get("Urgency") != "normal" || r.header.Get("Topic") != webpush.TopicFor("m1/s1#finished") {
		t.Fatalf("done push headers %v", r.header)
	}
	if auth := r.header.Get("Authorization"); !strings.HasPrefix(auth, "vapid t=") || !strings.HasSuffix(auth, ", k="+key) {
		t.Fatalf("authorization %q", auth)
	}

	ix := proto.Interaction{ID: "ix1", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "Bash: npm test"}
	h.notifyInteraction(assistantwire.AssistantID, ix, "Laptop", false)
	r = f.next(t)
	msg = webOpened(t, b, r)
	alert := interactionPayloadIn(i18n.En, ix, "Laptop")["aps"].(map[string]any)["alert"].(map[string]any)
	if msg["kind"] != "interaction" || msg["interaction_id"] != "ix1" || msg["title"] != alert["title"] || msg["body"] != alert["body"] {
		t.Fatalf("card push %v, alert %v", msg, alert)
	}
	if r.header.Get("Urgency") != "high" || r.header.Get("Topic") != webpush.TopicFor("m1/s1#needs") {
		t.Fatalf("card push headers %v", r.header)
	}
	// An MCP form goes only to a browser that said it can show one.
	h.notifyInteraction(assistantwire.AssistantID, proto.Interaction{ID: "ix2", MachineID: "m1", SessionID: "s1", Kind: proto.IxElicitation}, "Laptop", false)
	if msg := webOpened(t, b, f.next(t)); msg["interaction_id"] != "ix2" {
		t.Fatalf("elicitation %v", msg)
	}
	subscribe(t, h, mux, "web-one", b, "")
	h.notifyInteraction(assistantwire.AssistantID, proto.Interaction{ID: "ix3", MachineID: "m1", SessionID: "s1", Kind: proto.IxElicitation}, "Laptop", false)
	f.none(t)

	// Its delivery is in its status.
	_, st = call(t, mux, "GET", "/api/v1/push/web/web-one", h.cfg.Token, "")
	if st["last_ok_at"] == nil || st["failed_at"] != nil {
		t.Fatalf("status after pushes %v", st)
	}
}

// While a page of the browser is connected it notifies by itself: no push then. A page names its device in its
// client_hello; one from before that is known by its credential.
func TestWebPushSkipsABrowserWithAPageOpen(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	mux := h.routes()
	b := f.browser(t, "/push/open")
	subscribe(t, h, mux, "web-open", b, "")
	done := func() {
		h.notifyDone(assistantwire.AssistantID, "done", "m1", "Laptop", "s1", "t", "x", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	}
	page := &client{device: "credential-1", out: make(chan []byte, 8)}
	h.mu.Lock()
	h.clients[page] = struct{}{}
	h.mu.Unlock()
	h.onClientMessage(context.Background(), page, []byte(`{"type":"client_hello","device_id":"web-open","platform":"web"}`))
	done()
	f.none(t)

	// Closed: the next one is pushed.
	h.mu.Lock()
	delete(h.clients, page)
	h.mu.Unlock()
	done()
	webOpened(t, b, f.next(t))

	// A page from before device_id, signed in with the credential that subscribed.
	h.push.web.mu.Lock()
	s := h.push.web.subs["web-open"]
	s.Credential = "credential-2"
	h.push.web.subs["web-open"] = s
	h.push.web.mu.Unlock()
	old := &client{device: "credential-2", out: make(chan []byte, 8)}
	h.mu.Lock()
	h.clients[old] = struct{}{}
	h.mu.Unlock()
	done()
	f.none(t)
}

// Each browser's own Settings › Notifications decide, as for its socket: "Needs you" off, "Done" choices, a manager
// it muted, and quiet hours (a card without a sound, no finished turn at all).
func TestWebPushFollowsTheBrowsersSettings(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	mux := h.routes()
	b := f.browser(t, "/push/prefs")
	subscribe(t, h, mux, "web-prefs", b, "")
	card := func(id string) {
		h.notifyInteraction(assistantwire.AssistantID, proto.Interaction{ID: id, MachineID: "m1", SessionID: "s1", Kind: proto.IxQuestion, Summary: "Which one?"}, "Laptop", false)
	}
	finished := func(took time.Duration) {
		h.notifyDone(assistantwire.AssistantID, "done", "m1", "Laptop", "s1", "t", "x", pushWants{Kind: "done", Took: took, Long: time.Minute})
	}
	set := func(p NotifyPrefs) {
		t.Helper()
		if err := h.push.setPrefs("web-prefs", p); err != nil {
			t.Fatal(err)
		}
	}

	set(NotifyPrefs{Input: false, Done: DoneAlways})
	card("q1")
	f.none(t)
	finished(time.Second)
	webOpened(t, b, f.next(t))

	set(NotifyPrefs{Input: true, Done: DoneLong})
	finished(time.Second)
	f.none(t)
	card("q2")
	webOpened(t, b, f.next(t))

	set(NotifyPrefs{Input: true, Done: DoneAlways, MutedManagers: []string{assistantwire.AssistantID}})
	card("q3")
	finished(time.Hour)
	f.none(t)

	set(NotifyPrefs{Input: true, Done: DoneAlways, Quiet: QuietHours{Enabled: true, Start: "00:00", End: "23:59", TZ: "UTC"}})
	h.push.now = func() time.Time { return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) }
	finished(time.Hour)
	f.none(t)
	card("q4")
	if msg := webOpened(t, b, f.next(t)); msg["quiet"] != true || msg["interaction_id"] != "q4" {
		t.Fatalf("quiet card %v", msg)
	}
}

// A push is written in the language the browser subscribed in.
func TestWebPushIsWrittenInTheBrowsersLanguage(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	mux := h.routes()
	b := f.browser(t, "/push/ja")
	body := strings.Replace(subscribeBody(t, h, b, ""), `"locale":"en"`, `"locale":"ja-JP"`, 1)
	if code, out := call(t, mux, "PUT", "/api/v1/push/web/web-ja", h.cfg.Token, body); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	ix := proto.Interaction{ID: "p1", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "Bash: ls"}
	h.notifyInteraction(assistantwire.AssistantID, ix, "Laptop", false)
	msg := webOpened(t, b, f.next(t))
	ja := interactionPayloadIn(i18n.Ja, ix, "Laptop")["aps"].(map[string]any)["alert"].(map[string]any)
	en := interactionPayloadIn(i18n.En, ix, "Laptop")["aps"].(map[string]any)["alert"].(map[string]any)
	if msg["title"] != ja["title"] || ja["title"] == en["title"] {
		t.Fatalf("title %v, ja %v, en %v", msg["title"], ja["title"], en["title"])
	}
}

// A push the service may take later is tried again; one it says is gone drops the subscription, so nothing more is
// sent to it.
func TestWebPushRetriesAndDropsAGoneSubscription(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	h.push.web.retryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	mux := h.routes()
	b := f.browser(t, "/push/gone")
	subscribe(t, h, mux, "web-gone", b, "")
	results := make(chan int, 8)
	h.push.web.sent = func(_ string, status int, _ error) { results <- status }
	done := func() {
		h.notifyDone(assistantwire.AssistantID, "done", "m1", "Laptop", "s1", "t", "x", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	}
	f.answer(http.StatusServiceUnavailable)
	done()
	f.next(t)
	webOpened(t, b, f.next(t))
	if s := <-results; s != http.StatusCreated {
		t.Fatalf("after a retry: %d", s)
	}

	f.answer(http.StatusGone)
	done()
	f.next(t)
	if s := <-results; s != http.StatusGone {
		t.Fatalf("gone: %d", s)
	}
	if _, ok := h.push.web.get("web-gone"); ok {
		t.Fatal("a gone subscription was kept")
	}
	done()
	f.none(t)
}

// The test route pushes now, page open or not, and says what the push service answered.
func TestWebPushTestRouteSaysWhatThePushServiceAnswered(t *testing.T) {
	f := newFakePushService(t)
	h := webPushHub(t, f)
	mux := h.routes()
	if code, out := call(t, mux, "POST", "/api/v1/push/web/web-none/test", h.cfg.Token, ""); code != 404 || out["code"] != "not_subscribed" {
		t.Fatalf("no subscription: %d %v", code, out)
	}
	b := f.browser(t, "/push/test")
	subscribe(t, h, mux, "web-test", b, "")
	code, out := call(t, mux, "POST", "/api/v1/push/web/web-test/test", h.cfg.Token, "")
	if code != 200 || out["push_status"] != float64(201) {
		t.Fatalf("test: %d %v", code, out)
	}
	r := f.next(t)
	if msg := webOpened(t, b, r); msg["kind"] != "test" || msg["title"] != i18n.T(i18n.En, "hub-webpush.test_title") {
		t.Fatalf("test push %v", msg)
	}
	if r.header.Get("TTL") != "300" || r.header.Get("Urgency") != "high" {
		t.Fatalf("test headers %v", r.header)
	}
	f.answer(http.StatusForbidden)
	code, out = call(t, mux, "POST", "/api/v1/push/web/web-test/test", h.cfg.Token, "")
	if code != 502 || out["code"] != "push_failed" || out["push_status"] != float64(403) || !strings.Contains(out["error"].(string), "403") {
		t.Fatalf("refused test: %d %v", code, out)
	}
	_, st := call(t, mux, "GET", "/api/v1/push/web/web-test", h.cfg.Token, "")
	if st["failed_at"] == nil || st["status"] != float64(403) {
		t.Fatalf("status after a refused push %v", st)
	}
}

// What a page may subscribe: a known push service's https endpoint, keys that parse, and the hub's key; a key the
// hub no longer has is answered with the one to subscribe with.
func TestWebPushSubscribeChecksWhatItIsGiven(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	b, _ := webpushtest.NewBrowser("https://fcm.googleapis.com/fcm/send/abc")
	good := subscribeBody(t, h, b, "")
	for name, tc := range map[string]struct {
		device, body, code string
		status             int
	}{
		"unknown service":  {"web-a", strings.Replace(good, "https://fcm.googleapis.com/fcm/send/abc", "https://example.com/push", 1), "unknown_push_service", 400},
		"internal address": {"web-a", strings.Replace(good, "https://fcm.googleapis.com/fcm/send/abc", "https://127.0.0.1/push", 1), "unknown_push_service", 400},
		"plain http":       {"web-a", strings.Replace(good, "https://fcm", "http://fcm", 1), "bad_endpoint", 400},
		"bad keys":         {"web-a", strings.Replace(good, b.Sub.P256DH, "AAAA", 1), "bad_keys", 400},
		"bad device":       {"web%20a", good, "bad_device", 400},
		"stale key":        {"web-a", strings.Replace(good, h.push.web.currentKey(), "BOldKey", 1), "key_changed", 409},
	} {
		code, out := call(t, mux, "PUT", "/api/v1/push/web/"+tc.device, h.cfg.Token, tc.body)
		if code != tc.status || out["code"] != tc.code {
			t.Errorf("%s: %d %v", name, code, out)
		}
		if tc.code == "key_changed" && out["key"] != h.push.web.currentKey() {
			t.Errorf("stale key answer %v", out)
		}
	}
	if code, out := call(t, mux, "PUT", "/api/v1/push/web/web-a", h.cfg.Token, good); code != 200 || out["service"] != "FCM" || out["current"] != true {
		t.Fatalf("good: %d %v", code, out)
	}
	if code, out := call(t, mux, "DELETE", "/api/v1/push/web/web-a", h.cfg.Token, ""); code != 200 || out["removed"] != true {
		t.Fatalf("delete: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/push/web/web-a", h.cfg.Token, ""); code != 200 || out["subscribed"] != false {
		t.Fatalf("after delete: %d %v", code, out)
	}
}

// A subscription is its credential's: another may replace or drop it only by presenting its endpoint (the same
// browser, signed in again). Revoking the credential drops it; one endpoint is one row; the hub keeps webPushMax.
func TestWebPushSubscriptionsBelongToTheirCredential(t *testing.T) {
	w := newWebPusher(nil, t.Logf)
	sub := func(device, credential, endpoint string) webSub {
		return webSub{Device: device, Credential: credential, Endpoint: endpoint, Browser: device}
	}
	if err := w.subscribe(sub("web-1", "cred-a", "https://fcm.googleapis.com/a")); err != nil {
		t.Fatal(err)
	}
	if err := w.subscribe(sub("web-1", "cred-b", "https://fcm.googleapis.com/b")); !errors.Is(err, errWebPushOwned) {
		t.Fatalf("another credential replaced it: %v", err)
	}
	if _, err := w.unsubscribe("web-1", "cred-b", ""); !errors.Is(err, errWebPushOwned) {
		t.Fatalf("another credential dropped it: %v", err)
	}
	if err := w.subscribe(sub("web-1", "cred-b", "https://fcm.googleapis.com/a")); err != nil {
		t.Fatalf("the same browser signed in again: %v", err)
	}
	if s, _ := w.get("web-1"); s.Credential != "cred-b" {
		t.Fatalf("kept %+v", s)
	}
	if ok, err := w.unsubscribe("web-1", "cred-a", "https://fcm.googleapis.com/a"); !ok || err != nil {
		t.Fatalf("drop with its endpoint: %v %v", ok, err)
	}

	// The same endpoint under a new device id (the browser's storage was cleared) replaces the old row.
	_ = w.subscribe(sub("web-2", "cred-a", "https://fcm.googleapis.com/two"))
	_ = w.subscribe(sub("web-3", "cred-a", "https://fcm.googleapis.com/two"))
	if _, ok := w.get("web-2"); ok || len(w.list()) != 1 {
		t.Fatalf("one endpoint, two rows: %v", w.list())
	}
	w.signedOut("cred-a")
	if len(w.list()) != 0 {
		t.Fatalf("signed out, kept %v", w.list())
	}

	// At the cap, the one renewed longest ago goes.
	at := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	for i := range webPushMax + 1 {
		w.now = func() time.Time { return at.Add(time.Duration(i) * time.Minute) }
		if err := w.subscribe(sub(fmt.Sprintf("web-%02d", i), "", fmt.Sprintf("https://fcm.googleapis.com/%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := w.get("web-00"); ok || len(w.list()) != webPushMax {
		t.Fatalf("%d kept, oldest there: %v", len(w.list()), ok)
	}
}

// Revoking a browser's device token (Settings › Devices) drops its subscription with its other pushes.
func TestWebPushSignedOutWithItsCredential(t *testing.T) {
	h := newTestHub(t)
	_ = h.push.web.subscribe(webSub{Device: "web-x", Credential: "cred-x", Endpoint: "https://fcm.googleapis.com/x"})
	_ = h.push.web.subscribe(webSub{Device: "web-y", Credential: "cred-y", Endpoint: "https://fcm.googleapis.com/y"})
	h.push.signedOut("cred-x")
	if _, ok := h.push.web.get("web-x"); ok {
		t.Fatal("kept a signed-out browser")
	}
	if _, ok := h.push.web.get("web-y"); !ok {
		t.Fatal("dropped another browser")
	}
}

// webPushStore keeps settings as JSON, as Postgres does; failWrite fails the next write.
type webPushStore struct {
	memStore
	mu        sync.Mutex
	values    map[string]json.RawMessage
	failWrite bool
}

func (s *webPushStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.values[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(v, out)
}

func (s *webPushStore) PutSetting(_ context.Context, key string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failWrite {
		s.failWrite = false
		return errors.New("synthetic write failure")
	}
	b, err := json.Marshal(v)
	if err == nil {
		s.values[key] = b
	}
	return err
}

// The key is made once and kept; the subscriptions are kept whole and read back at the next start; a write that
// fails changes nothing.
func TestWebPushKeepsItsKeyAndSubscriptionsInTheHubsSettings(t *testing.T) {
	store := &webPushStore{memStore: *newMemStore(), values: map[string]json.RawMessage{}}
	w := newWebPusher(store, t.Logf)
	k1, err := w.key(context.Background())
	if err != nil || !k1.Valid() {
		t.Fatal(k1, err)
	}
	if _, ok := store.values[webPushKeysSetting]; !ok {
		t.Fatal("key not stored")
	}
	if err := w.subscribe(webSub{Device: "web-1", Credential: "c", Endpoint: "https://fcm.googleapis.com/1", Key: k1.Public}); err != nil {
		t.Fatal(err)
	}
	store.failWrite = true
	if err := w.subscribe(webSub{Device: "web-2", Endpoint: "https://fcm.googleapis.com/2", Key: k1.Public}); err == nil {
		t.Fatal("a failed write was taken")
	}
	if _, ok := w.get("web-2"); ok {
		t.Fatal("a failed write changed the list")
	}

	again := newWebPusher(store, t.Logf)
	k2, _ := again.key(context.Background())
	if k2 != k1 {
		t.Fatal("a second start made another key")
	}
	if s, ok := again.get("web-1"); !ok || s.Credential != "c" || s.Key != k1.Public {
		t.Fatalf("read back %+v %v", s, ok)
	}
	if _, ok := again.get("web-2"); ok {
		t.Fatal("read back the failed one")
	}
}

// A long notification is shortened to fit one push; the tag (and topic) is the page's for the same event.
func TestWebPushMessageFitsAndIsTaggedAsThePageTagsIt(t *testing.T) {
	msg := webPushMessage("done", map[string]any{"type": "notify", "title": strings.Repeat("t", 3000), "body": strings.Repeat("b", 6000), "machine_id": "m", "session_id": "s"}, false)
	plain, err := fitWebPush(msg)
	if err != nil || len(plain) > webpush.MaxPlaintext {
		t.Fatalf("%d bytes, %v", len(plain), err)
	}
	for _, tc := range []struct {
		msg  map[string]any
		want string
	}{
		{map[string]any{"kind": "interaction", "machine_id": "m", "session_id": "s", "interaction_id": "i"}, "m/s#needs"},
		{map[string]any{"kind": "reply", "machine_id": "m", "session_id": "s"}, "m/s#needs"},
		{map[string]any{"kind": "interaction", "interaction_id": "i"}, "card:i#needs"},
		{map[string]any{"kind": "done", "machine_id": "m", "session_id": "s"}, "m/s#finished"},
		{map[string]any{"kind": "assistant", "todo_id": "td"}, "todo:td"},
		{map[string]any{"kind": "alert", "id": "a1"}, "alert:a1"},
		{map[string]any{"kind": "alert"}, ""},
	} {
		if got := webPushTag(tc.msg); got != tc.want {
			t.Errorf("%v: %q, want %q", tc.msg, got, tc.want)
		}
	}
	if m := webPushMessage("assistant", map[string]any{"todo": assistantwire.NotifyTodo{ID: "td1"}}, true); m["todo_id"] != "td1" || m["quiet"] != true {
		t.Fatalf("decision %v", m)
	}
}

// GET /api/v1/devices/push lists the browsers too, for `hub doctor`.
func TestDevicesPushListsTheBrowsers(t *testing.T) {
	h := newTestHub(t)
	_ = h.push.web.subscribe(webSub{Device: "web-1", Endpoint: "https://web.push.apple.com/x", Browser: "Safari on iPhone"})
	code, out := call(t, h.routes(), "GET", "/api/v1/devices/push", h.cfg.Token, "")
	web, _ := out["web"].([]any)
	if code != 200 || len(web) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	row := web[0].(map[string]any)
	if row["device_id"] != "web-1" || row["service"] != "Apple" || row["browser"] != "Safari on iPhone" || row["current"] != false {
		t.Fatalf("row %v", row)
	}
}

// `hub doctor` lists the browsers with Web Push after the devices: through which service, and the ones whose last
// push did not go out or that subscribed with an earlier key.
func TestDoctorListsWebPushBrowsers(t *testing.T) {
	h := newTestHub(t)
	key, _ := h.push.web.key(context.Background())
	_ = h.push.web.subscribe(webSub{Device: "web-ok", Endpoint: "https://fcm.googleapis.com/ok", Key: key.Public, Browser: "Chrome on macOS"})
	_ = h.push.web.subscribe(webSub{Device: "web-old", Endpoint: "https://web.push.apple.com/old", Key: "BOlder", Browser: "Safari on iPhone"})
	_ = h.push.web.subscribe(webSub{Device: "web-bad", Endpoint: "https://updates.push.services.mozilla.com/bad", Key: key.Public, Browser: "Firefox on Linux"})
	s, _ := h.push.web.get("web-bad")
	h.push.web.noted(s, 401, errors.New("401 invalid token"))
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	get := func(ctx context.Context, path string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		return http.DefaultClient.Do(req)
	}
	d := &doctorReport{l: i18n.En}
	d.pushRoutes(context.Background(), get)
	text := reportText(d)
	for _, want := range []string{
		"Problem | Web Push: 2 browsers were subscribed with an earlier key or their last push didn’t go out",
		"  Chrome on macOS: Through FCM\n",
		"  Safari on iPhone: Through Apple, but subscribed with an earlier key",
		"  Firefox on Linux: Through Mozilla, but the last push at ",
		"(401 invalid token)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q:\n%s", want, text)
		}
	}
	// No browser, no line.
	none := newTestHub(t)
	srv2 := httptest.NewServer(none.routes())
	defer srv2.Close()
	d = &doctorReport{l: i18n.En}
	d.pushRoutes(context.Background(), func(ctx context.Context, path string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv2.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+none.cfg.Token)
		return http.DefaultClient.Do(req)
	})
	if text := reportText(d); strings.Contains(text, "Web Push") {
		t.Fatalf("a line without browsers:\n%s", text)
	}
}
