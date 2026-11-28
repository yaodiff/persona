package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

// gradingStore is the in-memory settings store plus the device-table methods newPusher and setPrefs touch, and the
// delivered-push table (pushSentStore) kept as a slice with the same rules as internal/db/push_sent.go.
type gradingStore struct {
	settingsStore
	sentMu   sync.Mutex
	sentRows []db.PushSent
	// sentAdds counts AddPushSent calls and sentAddBytes the JSON size of the rows they carried: what one push writes.
	sentAdds     int
	sentAddBytes int
	// landThenFail makes that many AddPushSent calls keep their rows and still return an error, as a write the
	// database committed reports a timeout or a dropped connection.
	landThenFail int
}

func newGradingStore() *gradingStore {
	return &gradingStore{settingsStore: settingsStore{vals: map[string][]byte{}}}
}

func (s *gradingStore) AddPushSent(_ context.Context, entries []db.PushSent) error {
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	s.addRowsLocked(entries)
	s.sentAdds++
	s.sentAddBytes += len(b)
	if s.landThenFail > 0 {
		s.landThenFail--
		return errors.New("timeout after commit")
	}
	return nil
}

// addRowsLocked appends the entries whose ID is not there yet, as the table's UNIQUE push_id with ON CONFLICT DO
// NOTHING does, and returns how many it added. sentMu is held.
func (s *gradingStore) addRowsLocked(entries []db.PushSent) int {
	have := map[string]bool{}
	for _, e := range s.sentRows {
		have[e.ID] = true
	}
	added := 0
	for _, e := range entries {
		if e.ID == "" {
			panic("push sent entry without an id")
		}
		if !have[e.ID] {
			have[e.ID] = true
			s.sentRows = append(s.sentRows, e)
			added++
		}
	}
	return added
}

func (s *gradingStore) PushSentAfter(_ context.Context, after time.Time, limit int) ([]db.PushSent, error) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	out := []db.PushSent{}
	for i := len(s.sentRows) - 1; i >= 0 && len(out) < limit; i-- {
		if s.sentRows[i].At.After(after) {
			out = append(out, s.sentRows[i])
		}
	}
	slices.Reverse(out)
	return out, nil
}

func (s *gradingStore) PrunePushSent(_ context.Context, before time.Time, keep int) (int64, error) {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	first := max(len(s.sentRows)-keep, 0)
	kept := []db.PushSent{}
	for i, e := range s.sentRows {
		if i >= first && e.At.After(before) {
			kept = append(kept, e)
		}
	}
	gone := len(s.sentRows) - len(kept)
	s.sentRows = kept
	return int64(gone), nil
}

func (s *gradingStore) AdoptPushSentSetting(_ context.Context, key string, after time.Time, keep int) (int, bool, error) {
	s.mu.Lock()
	raw, ok := s.vals[key] // left where it is, like the database's row
	s.mu.Unlock()
	var entries []db.PushSent
	if ok {
		if err := json.Unmarshal(raw, &entries); err != nil {
			return 0, false, err
		}
	}
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	missing, behind := db.MergePushSentSetting(entries, s.sentRows, after, keep)
	return s.addRowsLocked(missing), behind, nil
}

// sentTable is a copy of the delivered-push rows.
func (s *gradingStore) sentTable() []db.PushSent {
	s.sentMu.Lock()
	defer s.sentMu.Unlock()
	return slices.Clone(s.sentRows)
}

func (s *gradingStore) ReconcilePushDevices(context.Context, time.Time, time.Duration) (ret []db.PushDeviceRetirement, err error) {
	return nil, nil
}
func (s *gradingStore) Devices(context.Context) ([]db.Device, error)  { return nil, nil }
func (s *gradingStore) UpsertDevice(context.Context, db.Device) error { return nil }
func (s *gradingStore) SetDevicePrefs(context.Context, string, string, json.RawMessage) error {
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

// gradingHub is a pusher with one directly-pushed device and an in-memory settings store, so the ln mode switch and the
// held-push list persist like hub_settings. The pusher is built with no store (device registration needs no
// database in tests); the settings store is attached before the ln-mode state loads.
func gradingHub(t *testing.T) (*Hub, *fakeAPNs) {
	t.Helper()
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	store := newGradingStore()
	p := newPusher(t.TempDir(), nil, t.Logf)
	t.Cleanup(p.sends.Wait) // before srv.Close (cleanups run last first): a post still on its way logs to this test
	p.store = store
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	p.direct.Base = srv.URL
	p.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "tok-phone", Topic: "app.example"})
	if err := p.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	h := &Hub{push: p}
	h.push.quiesce = &h.quiesce
	setGradingClock(h, time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC))
	return h, f
}

// setGradingClock points the pusher's clock at a fixed time behind an atomic: the background refresh reads it while the
// test moves it.
func setGradingClock(h *Hub, at time.Time) {
	var clock atomic.Int64
	clock.Store(at.UnixNano())
	h.push.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	gradingClocks.Store(h.push, &clock)
}

var gradingClocks sync.Map // *Pusher → *atomic.Int64

// restartPusher is h's hub started again on store at h's moment: a new Pusher that reads h's clock and gets the store
// only then, so its startup load (the grading switch, the held pushes, the delivered pushes) runs on the fixture's
// clock, as gradingHub builds its own. newPusher(dir, store, …) loads on the wall clock, and held and delivered rows
// expire after heldPushMaxAge: rows stamped by gradingHub's fixed clock would all count as expired once the real
// date passes it by that much.
func restartPusher(t *testing.T, h *Hub, store Store) *Pusher {
	t.Helper()
	p := newPusher(t.TempDir(), nil, t.Logf)
	p.now = h.push.now
	p.store = store
	p.loadPushGradingState()
	return p
}

// moveGradingClock sets the clock setGradingClock made.
func moveGradingClock(h *Hub, at time.Time) {
	c, _ := gradingClocks.Load(h.push)
	c.(*atomic.Int64).Store(at.UnixNano())
}

// gradingAttempts counts APNs alert attempts (Live Activity pushes use a different type).
func gradingAttempts(h *Hub) *int {
	n := 0
	h.push.traffic = func(key trafficKey, _ int64, _ time.Duration) {
		if key.route == "apns" && key.action == "attempt" {
			n++
		}
	}
	return &n
}

func gradingNotification(level, key, title string) assistantwire.Notification {
	return assistantwire.Notification{
		ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyPush, Subtitle: "Persona",
		Title: title, Body: "body of " + title, Level: level, RelatedKey: key,
	}
}

// With the switch off every push goes out, whatever level it carries: existing behavior is unchanged.
func TestPushGradingOffPushesEverything(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	for _, level := range []string{assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2} {
		if err := h.deliverPush(gradingNotification(level, "key-"+level, "card "+level)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("got %d pushes with ln mode off, want 3", *attempts)
	}
	if calls := len(f.calls(t, 3)); calls != 3 {
		t.Fatalf("APNs got %d calls, want 3", calls)
	}
	if got := h.push.heldPushesView("", 0); len(got) != 0 {
		t.Fatalf("nothing may be intercepted with the switch off, got %d", len(got))
	}
}

// P0 always interrupts: the 12-hour check runs after the level, so no earlier push with the same key swallows what the
// owner must do or decide themself. Its key then keeps a lower-level push about the same thing quiet.
func TestQuietP0IsNeverSwallowedByTheSameKey(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	const pr = "https://example.test/pulls/12"
	for i := range 3 {
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP0, pr, "decision "+string(rune('1'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("got %d pushes for three P0 pushes with one key, want 3", *attempts)
	}
	if calls := len(f.calls(t, 3)); calls != 3 {
		t.Fatalf("APNs got %d calls, want 3", calls)
	}
	if view := h.push.heldPushesView("", 0); len(view) != 0 {
		t.Fatalf("P0 pushes were held: %+v", view)
	}
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP2, pr, "background about the PR")); err != nil {
		t.Fatal(err)
	}
	view := h.push.heldPushesView("", 0)
	if *attempts != 3 || len(view) != 1 || view[0].Reason != interceptReasonDedup || view[0].RelatedKey != pr {
		t.Fatalf("after the P0: %d pushes, intercepts %+v; want the P2 held as a repeat", *attempts, view)
	}
}

// After the 12-hour window the same key may push again.
func TestPushGradingDedupWindowExpires(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	key := "todo:42"
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP0, key, "first")); err != nil {
		t.Fatal(err)
	}
	base := h.push.now()
	moveGradingClock(h, base.Add(12*time.Hour+time.Minute))
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP0, key, "second")); err != nil {
		t.Fatal(err)
	}
	if *attempts != 2 {
		t.Fatalf("got %d pushes across the 12-hour window, want 2", *attempts)
	}
	f.calls(t, 2) // let both APNs goroutines finish before the fake server closes
}

// P2 never alerts in ln mode; it is recorded as a silent intercept instead.
func TestPushGradingP2IsSilentAndRecorded(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP2, "subject-1", "background news")); err != nil {
		t.Fatal(err)
	}
	if *attempts != 0 {
		t.Fatalf("a P2 push alerted %d times in ln mode, want 0", *attempts)
	}
	if calls := len(f.calls(t, 0)); calls != 0 {
		t.Fatalf("APNs got %d calls for a P2 push, want 0", calls)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || view[0].Reason != interceptReasonP2 || view[0].Level != assistantwire.LevelP2 {
		t.Fatalf("intercepts = %+v, want one P2 silent entry", view)
	}
}

// P1 is held for the briefing, and several P1 conclusion pushes arriving in the same minute merge into one entry.
func TestPushGradingP1HeldAndMergedWithinMinute(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
	for _, title := range []string{"card one", "card two", "card three"} {
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, "p1-"+title, title)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 0 {
		t.Fatalf("P1 alerted %d times in ln mode, want 0", *attempts)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 {
		t.Fatalf("same-minute P1 cards made %d entries, want 1 merged", len(view))
	}
	if len(view[0].Also) != 2 || view[0].Reason != interceptReasonP1 {
		t.Fatalf("merged entry = %+v, want the first card plus two also titles", view[0])
	}
}

// A push without an explicit level is derived from its kind: cards and decisions P0, finished turns P2, other
// assistant alerts P1.
func TestPushGradingLevelDerivedFromKind(t *testing.T) {
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
func TestPushGradingSwitchAndInterceptsPersist(t *testing.T) {
	h, _ := gradingHub(t)
	store := h.push.store
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP2, "persisted", "held")); err != nil {
		t.Fatal(err)
	}
	if !h.push.waitInterceptsSaved(2 * time.Second) {
		t.Fatal("the held pushes were not saved")
	}
	restarted := restartPusher(t, h, store)
	if !restarted.pushGradingOn() {
		t.Fatal("the ln mode switch did not survive a restart")
	}
	view := restarted.heldPushesView("", 0)
	if len(view) != 1 || view[0].RelatedKey != "persisted" {
		t.Fatalf("restored intercepts = %+v, want the held P2 entry", view)
	}
	// The held row lives heldPushMaxAge: a restart a second short of that still has it, one at the limit drops it.
	held := view[0].At
	moveGradingClock(h, held.Add(heldPushMaxAge-time.Second))
	if view := restartPusher(t, h, store).heldPushesView("", 0); len(view) != 1 {
		t.Fatalf("a restart inside heldPushMaxAge restored %+v, want the held P2 entry", view)
	}
	moveGradingClock(h, held.Add(heldPushMaxAge))
	if view := restartPusher(t, h, store).heldPushesView("", 0); len(view) != 0 {
		t.Fatalf("a restart heldPushMaxAge later restored %+v, want the expired row dropped", view)
	}
}

// The read-only intercepts view is newest first and its JSON shape is what extensions and the nightly review read.
func TestPushGradingInterceptsViewShape(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP2, "k1", "first")); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, "k2", "second")); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"intercepts": h.push.heldPushesView("", 0)})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Intercepts []heldPushRecord `json:"intercepts"`
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
func TestPushGradingInterceptsHeldToManager(t *testing.T) {
	h, _ := gradingHub(t)
	h.cfg = &Config{InterceptReaders: []string{"ext-held-reads"}}
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP2, "a", "from a")); err != nil {
		t.Fatal(err)
	}
	h.push.recordIntercept(heldPushRecord{At: h.push.now().UTC(), Level: assistantwire.LevelP2, Reason: interceptReasonP2,
		Title: "from b", Manager: "manager-b"})
	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	read := func(p *principal) []heldPushRecord {
		r := httptest.NewRequest("GET", "/api/v1/push/intercepts", nil)
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var out struct {
			Intercepts []heldPushRecord `json:"intercepts"`
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
	if w.Code != http.StatusForbidden || !h.push.pushGradingOn() {
		t.Fatalf("an extension token changed the switch: %d %s", w.Code, w.Body.String())
	}
}

// Review fix: with the switch off a payload without aps (an extension's push_localized may send one) goes out as
// before instead of panicking while the held-push text is read.
func TestPushGradingOffPayloadWithoutAPS(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	build := func(i18n.Lang) map[string]any { return map[string]any{"custom": "x"} }
	h.push.sendFrom("manager-a", build, "", nil, quietNone, nil)
	if *attempts != 1 {
		t.Fatalf("got %d pushes, want 1", *attempts)
	}
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	h.push.sendFrom("manager-a", build, "", nil, quietNone, nil) // P1: held, no panic
	if *attempts != 1 || len(h.push.heldPushesView("", 0)) != 1 {
		t.Fatalf("pushes %d, intercepts %d; want 1 and 1", *attempts, len(h.push.heldPushesView("", 0)))
	}
	f.calls(t, 1) // the off-mode push's APNs goroutine must finish before the fake server closes
}

// Review fix: same-minute P1 entries merge only within one manager, so one extension never reads another's titles.
func TestPushGradingMergeStaysWithinManager(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"manager-a", "manager-b"} {
		if _, err := h.deliverPushFrom(from, gradingNotification(assistantwire.LevelP1, "", "from "+from)); err != nil {
			t.Fatal(err)
		}
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 2 || len(view[0].Also) != 0 || len(view[1].Also) != 0 {
		t.Fatalf("view = %+v, want two separate entries", view)
	}
}

// Three P1 conclusion notices about one PR at different times are one held entry, not three.
func TestPushGradingSamePRP1IsOneEntry(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	const pr = "https://example.test/pulls/12"
	base := h.push.now()
	for i := range 3 {
		moveGradingClock(h, base.Add(time.Duration(i)*10*time.Minute))
		if err := h.deliverPush(gradingNotification(assistantwire.LevelP1, pr, "accepted "+string(rune('1'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || len(view[0].Also) != 2 || view[0].RelatedKey != pr {
		t.Fatalf("view = %+v, want one entry for the PR with two merged titles", view)
	}
}

// A decision is P0: it rings every time it is pushed, the same round's reminder too, and a new round (asked again).
func TestPushGradingDecisionRoundsRingAgain(t *testing.T) {
	h, f := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	attempts := gradingAttempts(h)
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
func TestPushGradingDedupIsAtomic(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
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
			if ok, _ := h.push.applyPushGrading(pushPolicy{level: level, relatedKey: "same"}); ok && level == assistantwire.LevelP0 {
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
func TestPushGradingStaleRefreshDoesNotUndoSet(t *testing.T) {
	h, _ := gradingHub(t)
	p := h.push
	p.gradingMu.Lock()
	started := p.gradingVersion // a refresh starts and reads "off"
	p.gradingRefreshing = true
	p.gradingMu.Unlock()
	if err := p.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	st := p.store.(*gradingStore)
	st.mu.Lock()
	st.vals[gradingSwitchSettingKey] = []byte("false") // what the slow read saw
	st.mu.Unlock()
	p.refreshPushGrading(started)
	if !p.pushGradingOn() {
		t.Fatal("a stale refresh turned the switch back off")
	}
}
