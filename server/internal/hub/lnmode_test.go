package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// lnStore is the in-memory settings store plus the device-table methods newPusher and setPrefs touch.
type lnStore struct {
	settingsStore
}

func (s *lnStore) ReconcilePushDevices(context.Context, time.Time, time.Duration) (ret []db.PushDeviceRetirement, err error) {
	return nil, nil
}
func (s *lnStore) Devices(context.Context) ([]db.Device, error)  { return nil, nil }
func (s *lnStore) UpsertDevice(context.Context, db.Device) error { return nil }
func (s *lnStore) SetDevicePrefs(context.Context, string, string, json.RawMessage) error {
	return nil
}

// The device-focused test stores embed the Store interface without implementing its settings methods; newPusher now
// reads the ln-mode state at startup, so give them no-op settings rows (they never test settings).
func (s *forgetStore) Setting(context.Context, string, any) (bool, error) { return false, nil }
func (s *forgetStore) PutSetting(context.Context, string, any) error      { return nil }
func (s *deviceRows) Setting(context.Context, string, any) (bool, error)  { return false, nil }
func (s *deviceRows) PutSetting(context.Context, string, any) error       { return nil }
func (s *stuckPrefsStore) Setting(context.Context, string, any) (bool, error) {
	return false, nil
}
func (s *stuckPrefsStore) PutSetting(context.Context, string, any) error { return nil }

// lnHub is a pusher with one directly-pushed device and an in-memory settings store, so the ln mode switch and the
// held-push list persist like hub_settings. The pusher is built with no store (device registration needs no
// database in tests); the settings store is attached before the ln-mode state loads.
func lnHub(t *testing.T) (*Hub, *fakeAPNs) {
	t.Helper()
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	store := &lnStore{settingsStore{vals: map[string][]byte{}}}
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.store = store
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	p.direct.Base = srv.URL
	p.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "tok-phone", Topic: "app.example"})
	if err := p.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	h := &Hub{push: p}
	lnSetClock(h, time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))
	return h, f
}

// lnSetClock points the pusher's clock at a fixed time behind an atomic: the background refresh reads it while the
// test moves it.
func lnSetClock(h *Hub, at time.Time) {
	var clock atomic.Int64
	clock.Store(at.UnixNano())
	h.push.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	lnClocks.Store(h.push, &clock)
}

var lnClocks sync.Map // *Pusher → *atomic.Int64

// lnMoveClock sets the clock lnSetClock made.
func lnMoveClock(h *Hub, at time.Time) {
	c, _ := lnClocks.Load(h.push)
	c.(*atomic.Int64).Store(at.UnixNano())
}

// lnAttempts counts APNs alert attempts (Live Activity pushes use a different type).
func lnAttempts(h *Hub) *int {
	n := 0
	h.push.traffic = func(key trafficKey, _ int64, _ time.Duration) {
		if key.route == "apns" && key.action == "attempt" {
			n++
		}
	}
	return &n
}

func lnNotification(level, key, title string) assistantwire.Notification {
	return assistantwire.Notification{
		ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyPush, Subtitle: "Persona",
		Title: title, Body: "body of " + title, Level: level, RelatedKey: key,
	}
}

// With the switch off every push goes out, whatever level it carries: existing behavior is unchanged.
func TestLnModeOffPushesEverything(t *testing.T) {
	h, f := lnHub(t)
	attempts := lnAttempts(h)
	for _, level := range []string{assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2} {
		if err := h.deliverPush(lnNotification(level, "key-"+level, "card "+level)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("got %d pushes with ln mode off, want 3", *attempts)
	}
	if calls := len(f.calls(t, 3)); calls != 3 {
		t.Fatalf("APNs got %d calls, want 3", calls)
	}
	if got := h.push.lnInterceptsView("", 0); len(got) != 0 {
		t.Fatalf("nothing may be intercepted with the switch off, got %d", len(got))
	}
}

// P0 always interrupts: the 12-hour check runs after the level, so no earlier push with the same key swallows what the
// owner must do or decide themself. Its key then keeps a lower-level push about the same thing quiet.
func TestQuietP0IsNeverSwallowedByTheSameKey(t *testing.T) {
	h, f := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	const pr = "https://example.test/pulls/12"
	for i := range 3 {
		if err := h.deliverPush(lnNotification(assistantwire.LevelP0, pr, "decision "+string(rune('1'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("got %d pushes for three P0 pushes with one key, want 3", *attempts)
	}
	if calls := len(f.calls(t, 3)); calls != 3 {
		t.Fatalf("APNs got %d calls, want 3", calls)
	}
	if view := h.push.lnInterceptsView("", 0); len(view) != 0 {
		t.Fatalf("P0 pushes were held: %+v", view)
	}
	if err := h.deliverPush(lnNotification(assistantwire.LevelP2, pr, "background about the PR")); err != nil {
		t.Fatal(err)
	}
	view := h.push.lnInterceptsView("", 0)
	if *attempts != 3 || len(view) != 1 || view[0].Reason != interceptReasonDedup || view[0].RelatedKey != pr {
		t.Fatalf("after the P0: %d pushes, intercepts %+v; want the P2 held as a repeat", *attempts, view)
	}
}

// After the 12-hour window the same key may push again.
func TestLnModeDedupWindowExpires(t *testing.T) {
	h, f := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	key := "todo:42"
	if err := h.deliverPush(lnNotification(assistantwire.LevelP0, key, "first")); err != nil {
		t.Fatal(err)
	}
	base := h.push.now()
	lnMoveClock(h, base.Add(12*time.Hour+time.Minute))
	if err := h.deliverPush(lnNotification(assistantwire.LevelP0, key, "second")); err != nil {
		t.Fatal(err)
	}
	if *attempts != 2 {
		t.Fatalf("got %d pushes across the 12-hour window, want 2", *attempts)
	}
	f.calls(t, 2) // let both APNs goroutines finish before the fake server closes
}

// P2 never alerts in ln mode; it is recorded as a silent intercept instead.
func TestLnModeP2IsSilentAndRecorded(t *testing.T) {
	h, f := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	if err := h.deliverPush(lnNotification(assistantwire.LevelP2, "subject-1", "background news")); err != nil {
		t.Fatal(err)
	}
	if *attempts != 0 {
		t.Fatalf("a P2 push alerted %d times in ln mode, want 0", *attempts)
	}
	if calls := len(f.calls(t, 0)); calls != 0 {
		t.Fatalf("APNs got %d calls for a P2 push, want 0", calls)
	}
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP2 || view[0].Level != assistantwire.LevelP2 {
		t.Fatalf("intercepts = %+v, want one P2 silent entry", view)
	}
}

// P1 is held for the briefing, and several P1 conclusion pushes arriving in the same minute merge into one entry.
func TestLnModeP1HeldAndMergedWithinMinute(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	for _, title := range []string{"card one", "card two", "card three"} {
		if err := h.deliverPush(lnNotification(assistantwire.LevelP1, "p1-"+title, title)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 0 {
		t.Fatalf("P1 alerted %d times in ln mode, want 0", *attempts)
	}
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 1 {
		t.Fatalf("same-minute P1 cards made %d entries, want 1 merged", len(view))
	}
	if len(view[0].Also) != 2 || view[0].Reason != interceptReasonP1 {
		t.Fatalf("merged entry = %+v, want the first card plus two also titles", view[0])
	}
}

// A push without an explicit level is derived from its kind: cards and decisions P0, finished turns P2, other
// assistant alerts P1.
func TestLnLevelDerivedFromKind(t *testing.T) {
	cases := map[string]string{
		assistantwire.NotifyInput:  assistantwire.LevelP0,
		assistantwire.NotifyDecide: assistantwire.LevelP0,
		assistantwire.NotifyReply:  assistantwire.LevelP0,
		assistantwire.NotifyDone:   assistantwire.LevelP2,
		assistantwire.NotifyPush:   assistantwire.LevelP1,
	}
	for kind, want := range cases {
		if got := levelFor(kind, ""); got != want {
			t.Errorf("levelFor(%q) = %q, want %q", kind, got, want)
		}
		if got := levelFor(kind, assistantwire.LevelP0); got != assistantwire.LevelP0 {
			t.Errorf("an explicit level was overridden for kind %q: %q", kind, got)
		}
	}
}

// The switch and the held-push list live in the settings store, so a restart keeps both.
func TestLnModeSwitchAndInterceptsPersist(t *testing.T) {
	h, _ := lnHub(t)
	store := h.push.store
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(lnNotification(assistantwire.LevelP2, "persisted", "held")); err != nil {
		t.Fatal(err)
	}
	if !h.push.waitInterceptsSaved(2 * time.Second) {
		t.Fatal("the held pushes were not saved")
	}
	restarted := newPusher(t.TempDir(), store, t.Logf)
	if !restarted.lnModeOn() {
		t.Fatal("the ln mode switch did not survive a restart")
	}
	view := restarted.lnInterceptsView("", 0)
	if len(view) != 1 || view[0].RelatedKey != "persisted" {
		t.Fatalf("restored intercepts = %+v, want the held P2 entry", view)
	}
}

// The read-only intercepts view is newest first and its JSON shape is what extensions and the nightly review read.
func TestLnInterceptsViewShape(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(lnNotification(assistantwire.LevelP2, "k1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(lnNotification(assistantwire.LevelP1, "k2", "second")); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"intercepts": h.push.lnInterceptsView("", 0)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Intercepts []pushIntercept `json:"intercepts"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Intercepts) != 2 || got.Intercepts[0].RelatedKey != "k2" {
		t.Fatalf("view = %s, want newest first with two entries", raw)
	}
}

// GET /api/v1/push/intercepts: a full credential reads every manager's held pushes, an extension token only its own
// manager's; PUT /api/v1/push/ln-mode is refused to an extension token.
func TestLnInterceptsHeldToManager(t *testing.T) {
	h, _ := lnHub(t)
	h.cfg = &Config{InterceptReaders: []string{"ext-held-reads"}}
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", lnNotification(assistantwire.LevelP2, "a", "from a")); err != nil {
		t.Fatal(err)
	}
	h.push.recordIntercept(pushIntercept{At: h.push.now().UTC(), Level: assistantwire.LevelP2, Reason: interceptReasonP2,
		Title: "from b", Manager: "manager-b"})
	mux := http.NewServeMux()
	h.lnRoutes(mux)
	read := func(p *principal) []pushIntercept {
		r := httptest.NewRequest("GET", "/api/v1/push/intercepts", nil)
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out struct {
			Intercepts []pushIntercept `json:"intercepts"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %v", w.Body.String(), err)
		}
		return out.Intercepts
	}
	if all := read(nil); len(all) != 2 {
		t.Fatalf("a full credential read %d entries, want 2", len(all))
	}
	ext := read(&principal{Role: roleExtension, Manager: "manager-b", Extension: "ext-held-reads"})
	if len(ext) != 1 || ext[0].Manager != "manager-b" {
		t.Fatalf("manager-b's extension read %+v, want only its own entry", ext)
	}
	r := httptest.NewRequest("PUT", "/api/v1/push/ln-mode", strings.NewReader(`{"enabled":false}`))
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleExtension, Manager: "manager-b"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !h.push.lnModeOn() {
		t.Fatalf("an extension token changed the switch: %d %s", w.Code, w.Body.String())
	}
}

// Review fix: with the switch off a payload without aps (an extension's push_localized may send one) goes out as
// before instead of panicking while the held-push text is read.
func TestLnOffPayloadWithoutAPS(t *testing.T) {
	h, f := lnHub(t)
	attempts := lnAttempts(h)
	build := func(i18n.Lang) map[string]any { return map[string]any{"custom": "x"} }
	h.push.sendFrom("manager-a", build, "", nil, quietNone, nil)
	if *attempts != 1 {
		t.Fatalf("got %d pushes, want 1", *attempts)
	}
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	h.push.sendFrom("manager-a", build, "", nil, quietNone, nil) // P1: held, no panic
	if *attempts != 1 || len(h.push.lnInterceptsView("", 0)) != 1 {
		t.Fatalf("pushes %d, intercepts %d; want 1 and 1", *attempts, len(h.push.lnInterceptsView("", 0)))
	}
	f.calls(t, 1) // the off-mode push's APNs goroutine must finish before the fake server closes
}

// Review fix: same-minute P1 entries merge only within one manager, so one extension never reads another's titles.
func TestLnMergeStaysWithinManager(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"manager-a", "manager-b"} {
		if _, err := h.deliverPushFrom(from, lnNotification(assistantwire.LevelP1, "", "from "+from)); err != nil {
			t.Fatal(err)
		}
	}
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 2 || len(view[0].Also) != 0 || len(view[1].Also) != 0 {
		t.Fatalf("view = %+v, want two separate entries", view)
	}
}

// Three P1 conclusion notices about one PR at different times are one held entry, not three.
func TestLnSamePRP1IsOneEntry(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	const pr = "https://example.test/pulls/12"
	base := h.push.now()
	for i := range 3 {
		lnMoveClock(h, base.Add(time.Duration(i)*10*time.Minute))
		if err := h.deliverPush(lnNotification(assistantwire.LevelP1, pr, "accepted "+string(rune('1'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	view := h.push.lnInterceptsView("", 0)
	if len(view) != 1 || len(view[0].Also) != 2 || view[0].RelatedKey != pr {
		t.Fatalf("view = %+v, want one entry for the PR with two merged titles", view)
	}
}

// A decision is P0: it rings every time it is pushed, the same round's reminder too, and a new round (asked again).
func TestLnDecisionRoundsRingAgain(t *testing.T) {
	h, f := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := lnAttempts(h)
	todo := assistantwire.NotifyTodo{ID: "todo_1", AskedAt: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), Recommend: "ok"}
	h.notifyDecide("", "", "", "", "decide", "body", todo)
	h.notifyDecide("", "", "", "", "decide", "body", todo)
	todo.AskedAt = todo.AskedAt.Add(time.Hour)
	h.notifyDecide("", "", "", "", "decide again", "body", todo)
	if *attempts != 3 {
		t.Fatalf("got %d decision pushes, want 3 (P0 is never held as a repeat)", *attempts)
	}
	f.calls(t, 2) // both decisions' APNs goroutines must finish before the fake server closes
}

// Concurrent pushes with one key: every P0 passes, and every lower-level one racing them is held, never alerted.
func TestLnDedupIsAtomic(t *testing.T) {
	h, _ := lnHub(t)
	if err := h.push.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var p0, low atomic.Int32
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			level := assistantwire.LevelP0
			if i%2 == 1 {
				level = assistantwire.LevelP2
			}
			if ok, _ := h.push.applyLn(pushPolicy{level: level, relatedKey: "same"}); ok && level == assistantwire.LevelP0 {
				p0.Add(1)
			} else if ok {
				low.Add(1)
			}
		}()
	}
	wg.Wait()
	if p0.Load() != 16 || low.Load() != 0 {
		t.Fatalf("%d P0 and %d P2 passed, want 16 and 0", p0.Load(), low.Load())
	}
}

// Review fix: a background refresh that read the old value before a PUT does not undo the PUT.
func TestLnStaleRefreshDoesNotUndoSet(t *testing.T) {
	h, _ := lnHub(t)
	p := h.push
	p.lnMu.Lock()
	started := p.lnVersion // a refresh starts and reads "off"
	p.lnRefreshing = true
	p.lnMu.Unlock()
	if err := p.setLnMode(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	st := p.store.(*lnStore)
	st.mu.Lock()
	st.vals[lnModeSettingKey] = []byte("false") // what the slow read saw
	st.mu.Unlock()
	p.refreshLnMode(started)
	if !p.lnModeOn() {
		t.Fatal("a stale refresh turned the switch back off")
	}
}
