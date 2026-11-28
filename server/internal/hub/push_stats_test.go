package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// statsRequest serves one GET against the push grading routes with an optional principal (nil is a full credential).
func statsRequest(t *testing.T, h *Hub, p *principal, target string) (int, []byte) {
	t.Helper()
	mux := http.NewServeMux()
	h.pushGradingRoutes(mux)
	r := httptest.NewRequest("GET", target, nil)
	if p != nil {
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *p))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func extPrincipal(manager string) *principal {
	return &principal{Role: roleExtension, Manager: manager}
}

// An extension token with the notify scope may read the switch state; only a full credential may change it. The PUT
// guard itself is covered in TestPushGradingInterceptsHeldToManager.
func TestPushGradingSwitchReadableByNotifyExtension(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	code, body := statsRequest(t, h, extPrincipal("manager-a"), "/api/v1/push/ln-mode")
	if code != http.StatusOK {
		t.Fatalf("an extension token read the switch: %d %s", code, body)
	}
	var got struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(body, &got); err != nil || !got.Enabled {
		t.Fatalf("switch answer = %s, want enabled true", body)
	}
}

// The neutral switch alias and the legacy route share one handler: GET/PUT parity, extension authorization and the
// original single storage key are pinned together so the alias cannot drift or introduce a persistence migration.
func TestPushGradingRouteAliasMatchesLegacy(t *testing.T) {
	h, _ := gradingHub(t)
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	paths := []string{"/api/v1/push/ln-mode", "/api/v1/push/grading"}
	for _, path := range paths {
		for _, p := range []*principal{nil, extPrincipal("manager-a")} {
			code, body := statsRequest(t, h, p, path)
			if code != http.StatusOK || string(body) != "{\"enabled\":true}\n" {
				t.Fatalf("GET %s (extension=%v) = %d %s, want the same enabled response", path, p != nil, code, body)
			}
		}
	}
	put := func(path string, p *principal, enabled bool) (int, []byte) {
		t.Helper()
		mux := http.NewServeMux()
		h.pushGradingRoutes(mux)
		value := "false"
		if enabled {
			value = "true"
		}
		r := httptest.NewRequest("PUT", path, strings.NewReader(`{"enabled":`+value+`}`))
		if p != nil {
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, *p))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.Bytes()
	}
	oldCode, oldBody := put(paths[0], extPrincipal("manager-a"), false)
	newCode, newBody := put(paths[1], extPrincipal("manager-a"), false)
	if oldCode != http.StatusForbidden || newCode != oldCode || !bytes.Equal(newBody, oldBody) {
		t.Fatalf("extension PUT parity = old %d %s, new %d %s; want identical 403", oldCode, oldBody, newCode, newBody)
	}
	if code, body := put(paths[1], nil, false); code != http.StatusOK || string(body) != "{\"enabled\":false}\n" {
		t.Fatalf("full-credential PUT on the alias = %d %s", code, body)
	}
	if code, body := statsRequest(t, h, nil, paths[0]); code != http.StatusOK || string(body) != "{\"enabled\":false}\n" {
		t.Fatalf("legacy GET after alias PUT = %d %s", code, body)
	}
	if code, body := put(paths[0], nil, true); code != http.StatusOK {
		t.Fatalf("full-credential PUT on the legacy route = %d %s", code, body)
	}
	if code, body := statsRequest(t, h, nil, paths[1]); code != http.StatusOK || string(body) != "{\"enabled\":true}\n" {
		t.Fatalf("alias GET after legacy PUT = %d %s", code, body)
	}
	store := h.push.store.(*gradingStore)
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.vals) != 1 || string(store.vals[gradingSwitchSettingKey]) != "true" {
		t.Fatalf("stored settings = %#v, want only the original grading switch key set to true", store.vals)
	}
}

// The manager filter runs before the 200-entry limit: another manager's burst cannot push this extension's entries
// out of its view. manager-b's three entries are the oldest, so a truncate-then-filter implementation would hide all
// of them behind manager-a's 220 newer ones.
func TestPushInterceptsManagerFilterBeforeLimit(t *testing.T) {
	h, _ := gradingHub(t)
	h.cfg = &Config{InterceptReaders: []string{"ext-intercept-panels"}}
	now := h.push.now()
	for i := range 3 {
		h.push.recordIntercept(heldPushRecord{At: now.Add(time.Duration(i) * time.Second).UTC(), Level: assistantwire.LevelP2,
			Reason: interceptReasonP2, Manager: "manager-b"})
	}
	for i := range 220 {
		h.push.recordIntercept(heldPushRecord{At: now.Add(time.Minute + time.Duration(i)*time.Second).UTC(), Level: assistantwire.LevelP2,
			Reason: interceptReasonP2, Manager: "manager-a"})
	}
	if all := h.push.heldPushesView("", 0); len(all) != heldPushViewLimitMax {
		t.Fatalf("full credential view = %d entries, want the 200 cap", len(all))
	}
	own := h.push.heldPushesView("manager-b", 0)
	if len(own) != 3 {
		t.Fatalf("manager-b view = %d entries, want all 3 of its own past the 200 cap", len(own))
	}
	for _, e := range own {
		if e.Manager != "manager-b" {
			t.Fatalf("manager-b view contains %q", e.Manager)
		}
	}
	code, body := statsRequest(t, h, &principal{Role: roleExtension, Manager: "manager-b", Extension: "ext-intercept-panels"},
		"/api/v1/push/intercepts")
	if code != http.StatusOK {
		t.Fatalf("intercepts over HTTP: %d %s", code, body)
	}
	var viaHTTP struct {
		Intercepts []heldPushRecord `json:"intercepts"`
	}
	if err := json.Unmarshal(body, &viaHTTP); err != nil {
		t.Fatal(err)
	}
	if len(viaHTTP.Intercepts) != 3 {
		t.Fatalf("manager-b over HTTP read %d entries, want 3", len(viaHTTP.Intercepts))
	}
}

// Delivered pushes are counted with the switch off as well as on, held pushes carry their reason, and the counts read
// the full records so they stay exact past the 200-entry view limit.
func TestPushDailyStatsCounts(t *testing.T) {
	h, f := gradingHub(t)
	// Switch off: two pushes go out and still count as delivered.
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "off-1", "one")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP2, "off-2", "two")); err != nil {
		t.Fatal(err)
	}
	if err := h.push.setPushGrading(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP0, "p0-key", "rings")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP2, "p2-key", "silent")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "p1-key", "held")); err != nil {
		t.Fatal(err)
	}
	// Wait for the three pushes that actually went out (two with the switch off, one P0): their APNs goroutines must
	// finish before the test's fake server closes, or they log after the test and panic under -race.
	f.calls(t, 3)
	// A busy day: 220 more held pushes, beyond the 200-entry view.
	now := h.push.now()
	for i := range 220 {
		h.push.recordIntercept(heldPushRecord{At: now.Add(time.Duration(i) * time.Millisecond).UTC(), Level: assistantwire.LevelP2,
			Reason: interceptReasonP2, Manager: "manager-a"})
	}
	stats := h.push.pushDailyStats(now.Add(-24*time.Hour), "")
	if len(stats) != 1 {
		t.Fatalf("stats = %+v, want one UTC day", stats)
	}
	got := stats[0]
	if got.Manager != "manager-a" || got.Sent != 3 {
		t.Fatalf("stats = %+v, want 3 delivered (two with the switch off, one P0)", got)
	}
	// 1 P1 + 1 P2 from the delivered pushes + 220 recorded P2 entries.
	if got.Intercepted != 222 || got.ByReason[interceptReasonP2] != 221 || got.ByReason[interceptReasonP1] != 1 {
		t.Fatalf("stats = %+v, want 222 held (221 P2, 1 P1), exact past 200", got)
	}
}

// gradingZonedClock installs one race-free clock whose instant and UTC offset can move independently.
func gradingZonedClock(h *Hub) (instant, offset *atomic.Int64) {
	instant, offset = &atomic.Int64{}, &atomic.Int64{}
	instant.Store(time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC).UnixNano())
	h.push.now = func() time.Time {
		return time.Unix(0, instant.Load()).In(time.FixedZone("", int(offset.Load())))
	}
	return instant, offset
}

func TestPushDailyStatsWindowUsesUTCCalendarDay(t *testing.T) {
	read := func(t *testing.T, h *Hub, target string) (string, []pushDayStats) {
		t.Helper()
		code, body := statsRequest(t, h, extPrincipal("manager-a"), target)
		if code != http.StatusOK {
			t.Fatalf("stats: %d %s", code, body)
		}
		var out struct {
			Since string         `json:"since"`
			Days  []pushDayStats `json:"days"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Since, out.Days
	}

	t.Run("UTC+8 does not start in the future", func(t *testing.T) {
		h, f := gradingHub(t)
		clock, offset := gradingZonedClock(h)
		clock.Store(time.Date(2026, 11, 14, 23, 30, 0, 0, time.UTC).UnixNano())
		offset.Store(8 * 60 * 60) // local 2026-11-15 07:30 +08:00
		if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "utc-east-day", "one")); err != nil {
			t.Fatal(err)
		}
		f.calls(t, 1)
		for _, target := range []string{"/api/v1/push/stats/daily", "/api/v1/push/stats/daily?days=1"} {
			since, days := read(t, h, target)
			if since != "2026-11-14T00:00:00Z" || len(days) != 1 || days[0].Date != "2026-11-14" ||
				days[0].Manager != "manager-a" || days[0].Sent != 1 {
				t.Fatalf("%s: since %s days %+v, want the current UTC day with one sent push", target, since, days)
			}
		}
	})

	t.Run("UTC-8 does not include the previous UTC day", func(t *testing.T) {
		h, f := gradingHub(t)
		clock, offset := gradingZonedClock(h)
		clock.Store(time.Date(2026, 11, 14, 23, 30, 0, 0, time.UTC).UnixNano())
		offset.Store(8 * 60 * 60) // the previous notification is at local 2026-11-15 07:30 +08:00
		if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "utc-previous-day", "old")); err != nil {
			t.Fatal(err)
		}
		clock.Store(time.Date(2026, 11, 15, 0, 30, 0, 0, time.UTC).UnixNano())
		offset.Store(-8 * 60 * 60) // the current clock reads local 2026-11-14 16:30 -08:00
		if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "utc-current-day", "new")); err != nil {
			t.Fatal(err)
		}
		f.calls(t, 2)
		since, days := read(t, h, "/api/v1/push/stats/daily?days=1")
		if since != "2026-11-15T00:00:00Z" || len(days) != 1 || days[0].Date != "2026-11-15" ||
			days[0].Manager != "manager-a" || days[0].Sent != 1 {
			t.Fatalf("since %s days %+v, want only the current UTC day with one sent push", since, days)
		}
	})
}

// The stats endpoint answers only the extension's own manager, understands since/days and rejects bad windows. Days
// bucket by UTC calendar day.
func TestPushDailyStatsHeldToManager(t *testing.T) {
	h, f := gradingHub(t)
	base := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	setGradingClock(h, base)
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "a-today", "a")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.deliverPushFrom("manager-b", gradingNotification(assistantwire.LevelP1, "b-today", "b")); err != nil {
		t.Fatal(err)
	}
	moveGradingClock(h, base.AddDate(0, 0, -1))
	if _, err := h.deliverPushFrom("manager-b", gradingNotification(assistantwire.LevelP1, "b-yesterday", "b old")); err != nil {
		t.Fatal(err)
	}
	moveGradingClock(h, base)
	// The switch is off, so all three went to the fake APNs; wait out their goroutines before the server closes.
	f.calls(t, 3)

	read := func(target string) (int, struct {
		Since string         `json:"since"`
		Days  []pushDayStats `json:"days"`
	}) {
		code, body := statsRequest(t, h, extPrincipal("manager-b"), target)
		var out struct {
			Since string         `json:"since"`
			Days  []pushDayStats `json:"days"`
		}
		if code == http.StatusOK {
			if err := json.Unmarshal(body, &out); err != nil {
				t.Fatal(err)
			}
		}
		return code, out
	}
	code, out := read("/api/v1/push/stats/daily")
	if code != http.StatusOK {
		t.Fatalf("default window: %d", code)
	}
	if len(out.Days) != 1 || out.Days[0].Manager != "manager-b" || out.Days[0].Sent != 1 || out.Days[0].Date != "2026-11-14" {
		t.Fatalf("today for manager-b = %+v, want one delivered push on 2026-11-14", out)
	}
	code, out = read("/api/v1/push/stats/daily?days=2")
	if code != http.StatusOK || len(out.Days) != 2 {
		t.Fatalf("days=2: code %d days %+v", code, out.Days)
	}
	if out.Days[0].Date != "2026-11-13" || out.Days[1].Date != "2026-11-14" {
		t.Fatalf("day buckets = %+v, want ascending UTC days", out.Days)
	}
	for _, d := range out.Days {
		if d.Manager != "manager-b" {
			t.Fatalf("manager-b's stats contain %q", d.Manager)
		}
	}
	since := base.AddDate(0, 0, -1).Format(time.RFC3339)
	if code, out = read("/api/v1/push/stats/daily?since=" + since); code != http.StatusOK || len(out.Days) != 2 {
		t.Fatalf("since: code %d days %+v", code, out.Days)
	}
	for _, bad := range []string{"/api/v1/push/stats/daily?days=0", "/api/v1/push/stats/daily?days=99", "/api/v1/push/stats/daily?days=two",
		"/api/v1/push/stats/daily?since=not-a-time"} {
		if code, _ := read(bad); code != http.StatusBadRequest {
			t.Fatalf("%s answered %d, want 400", bad, code)
		}
	}
	// A full credential sees every manager.
	code, body := statsRequest(t, h, nil, "/api/v1/push/stats/daily?days=2")
	if code != http.StatusOK {
		t.Fatalf("full credential: %d %s", code, body)
	}
	var all struct {
		Days []pushDayStats `json:"days"`
	}
	if err := json.Unmarshal(body, &all); err != nil || len(all.Days) != 3 {
		t.Fatalf("full credential days = %+v (%s), want three manager-day rows", all.Days, body)
	}
}

// The delivered-push record lives in the store (hub_push_sent), so a restart keeps the counts like the switch and the
// held list.
func TestPushSentPersists(t *testing.T) {
	h, f := gradingHub(t)
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "persisted-sent", "x")); err != nil {
		t.Fatal(err)
	}
	f.calls(t, 1) // let the APNs goroutine finish before the fake server closes
	if !h.push.waitInterceptsSaved(2 * time.Second) {
		t.Fatal("the delivered pushes were not saved")
	}
	restarted := restartPusher(t, h, h.push.store)
	stats := restarted.pushDailyStats(h.push.now().Add(-24*time.Hour), "")
	if len(stats) != 1 || stats[0].Sent != 1 || stats[0].Manager != "manager-a" {
		t.Fatalf("restored stats = %+v, want one delivered push", stats)
	}
}

// A pairing receipt bypasses the push grading filter on purpose (sendBuilt), so notifyComputerCode records it explicitly:
// the phone visibly received a hub alert and the daily sent count must include it, under the hub's empty manager.
func TestPushPairingReceiptCountsAsSent(t *testing.T) {
	h, f := gradingHub(t)
	if !h.push.ready() {
		t.Fatal("the fixture phone is not reachable")
	}
	h.notifyComputerCode(principal{Name: "fixture-phone", ID: "cred-issuer"}, "computer-pair-1")
	f.calls(t, 1) // the receipt really went out; let its APNs goroutine finish before the fake server closes
	stats := h.push.pushDailyStats(h.push.now().Add(-time.Hour), "")
	if len(stats) != 1 || stats[0].Sent != 1 || stats[0].Manager != "" {
		t.Fatalf("stats = %+v, want one sent hub pairing receipt with an empty manager", stats)
	}
}

// flakyPutStore fails the first failsLeft PutSetting and AddPushSent calls, then delegates to the real in-memory store.
type flakyPutStore struct {
	*gradingStore
	mu        sync.Mutex
	failsLeft int
}

func (s *flakyPutStore) fail() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failsLeft > 0 {
		s.failsLeft--
		return true
	}
	return false
}

func (s *flakyPutStore) PutSetting(ctx context.Context, key string, v any) error {
	if s.fail() {
		return errors.New("database down")
	}
	return s.gradingStore.PutSetting(ctx, key, v)
}

func (s *flakyPutStore) AddPushSent(ctx context.Context, entries []db.PushSent) error {
	if s.fail() {
		return errors.New("database down")
	}
	return s.gradingStore.AddPushSent(ctx, entries)
}

// A failed write is retried instead of dropped: waitInterceptsSaved must not report idle while the change is unsaved,
// and the row eventually lands, once.
func TestPushWriterRetriesFailedSave(t *testing.T) {
	h, f := gradingHub(t)
	store := &flakyPutStore{gradingStore: h.push.store.(*gradingStore), failsLeft: 2}
	h.push.store = store
	if _, err := h.deliverPushFrom("manager-a", gradingNotification(assistantwire.LevelP1, "retry-me", "x")); err != nil {
		t.Fatal(err)
	}
	f.calls(t, 1) // wait for the push goroutine; the store retries are what this test measures
	if !h.push.waitInterceptsSaved(10 * time.Second) {
		t.Fatal("the writer did not finish after retries")
	}
	store.mu.Lock()
	left := store.failsLeft
	store.mu.Unlock()
	if left != 0 {
		t.Fatalf("the flaky store still has %d failures budgeted, want all retries spent", left)
	}
	if rows := store.sentTable(); len(rows) != 1 || rows[0].RelatedKey != "retry-me" {
		t.Fatalf("saved rows = %+v, want one delivered push", rows)
	}
	h.push.gradingMu.Lock()
	idle := !h.push.gradingWriting && !h.push.heldDirty && len(h.push.sentPending) == 0
	h.push.gradingMu.Unlock()
	if !idle {
		t.Fatal("the writer stayed busy after the retry landed")
	}
}

// Security: the notify scope alone does not open held pushes. Only an extension whose ID is on the hub's
// intercept_readers allow-list may read them (its own manager's entries); every other extension token gets 403, and a
// full credential keeps reading every manager.
func TestPushInterceptsAllowlist(t *testing.T) {
	h, _ := gradingHub(t)
	h.cfg = &Config{InterceptReaders: []string{"ext-push-panels"}}
	now := h.push.now()
	h.push.recordIntercept(heldPushRecord{At: now.UTC(), Level: assistantwire.LevelP0, Reason: interceptReasonDedup,
		Title: "a card", Body: "summary for a", Manager: "manager-a", TodoID: "todo_a", MachineID: "machine-a", SessionID: "session-a"})
	h.push.recordIntercept(heldPushRecord{At: now.Add(time.Second).UTC(), Level: assistantwire.LevelP1, Reason: interceptReasonP1,
		Title: "b card", Body: "summary for b", Manager: "manager-b", TodoID: "todo_b", MachineID: "machine-b", SessionID: "session-b"})

	// No allow-list entry matches: extension tokens are refused even though they carry the notify scope.
	if code, _ := statsRequest(t, h, &principal{Role: roleExtension, Manager: "manager-a", Extension: "ext-other"},
		"/api/v1/push/intercepts"); code != http.StatusForbidden {
		t.Fatalf("an extension not on the allow-list got %d, want 403", code)
	}
	if code, _ := statsRequest(t, h, &principal{Role: roleExtension, Manager: "manager-b", Extension: "ext-other"},
		"/api/v1/push/intercepts"); code != http.StatusForbidden {
		t.Fatalf("a second extension not on the allow-list got %d, want 403", code)
	}

	// The allow-listed extension gets exactly its own manager's entry.
	code, body := statsRequest(t, h, &principal{Role: roleExtension, Manager: "manager-a", Extension: "ext-push-panels"},
		"/api/v1/push/intercepts")
	if code != http.StatusOK {
		t.Fatalf("the allow-listed extension got %d: %s", code, body)
	}
	var out struct {
		Intercepts []heldPushRecord `json:"intercepts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Intercepts) != 1 || out.Intercepts[0].Manager != "manager-a" {
		t.Fatalf("allow-listed extension read %+v, want only manager-a's entry", out.Intercepts)
	}
	var rawOut struct {
		Intercepts []map[string]any `json:"intercepts"`
	}
	if err := json.Unmarshal(body, &rawOut); err != nil || len(rawOut.Intercepts) != 1 {
		t.Fatalf("raw allow-listed response = %s (%v), want one entry", body, err)
	}
	for key, want := range map[string]string{"todo_id": "todo_a", "machine_id": "machine-a", "session_id": "session-a"} {
		if got := rawOut.Intercepts[0][key]; got != want {
			t.Fatalf("allow-listed response %s = %#v, want %q: %s", key, got, want, body)
		}
	}

	// A full credential still sees both managers.
	code, body = statsRequest(t, h, nil, "/api/v1/push/intercepts")
	if code != http.StatusOK {
		t.Fatalf("full credential got %d: %s", code, body)
	}
	if err := json.Unmarshal(body, &out); err != nil || len(out.Intercepts) != 2 {
		t.Fatalf("full credential read %+v (%s), want both entries", out.Intercepts, body)
	}
}

// A held push's full body is never stored, even before any read: the record keeps the title plus a summary capped at
// pushInterceptBodyMaxRunes runes. Merged titles and short bodies are unaffected.
func TestPushInterceptsBodyStoredAsSummary(t *testing.T) {
	h, _ := gradingHub(t)
	longBody := strings.Repeat("秘", pushInterceptBodyMaxRunes+50)
	h.push.recordIntercept(heldPushRecord{At: h.push.now().UTC(), Level: assistantwire.LevelP2, Reason: interceptReasonP2,
		Title: "long card", Body: longBody, Manager: "manager-a"})
	h.push.recordIntercept(heldPushRecord{At: h.push.now().Add(time.Second).UTC(), Level: assistantwire.LevelP1,
		Reason: interceptReasonP1, Title: "short card", Body: "one short line", Manager: "manager-a"})

	if !h.push.waitInterceptsSaved(2 * time.Second) {
		t.Fatal("the intercepts were not saved")
	}
	var raw []byte
	store := h.push.store.(*gradingStore)
	store.mu.Lock()
	raw = append([]byte(nil), store.vals[heldPushesSettingKey]...)
	store.mu.Unlock()
	if bytes.Contains(raw, []byte(longBody)) {
		t.Fatal("the full long body was persisted into the intercepts setting")
	}
	var stored []heldPushRecord
	if err := json.Unmarshal(raw, &stored); err != nil || len(stored) != 2 {
		t.Fatalf("stored rows = %+v (err %v), want 2", stored, err)
	}
	var long *heldPushRecord
	for i := range stored {
		if stored[i].Title == "long card" {
			long = &stored[i]
		}
	}
	if long == nil {
		t.Fatal("the long-body entry is missing from storage")
	}
	if n := len([]rune(long.Body)); n != pushInterceptBodyMaxRunes || !strings.HasSuffix(long.Body, "…") {
		t.Fatalf("stored summary length %d (suffix ellipsis %v), want %d runes ending with …", n, strings.HasSuffix(long.Body, "…"), pushInterceptBodyMaxRunes)
	}
}

// Rows written before the summary limit may still hold a full body: a restart must sanitize them in memory and write
// the sanitized list back, so the old full text does not stay in the database until some unrelated write.
func TestPushInterceptsSanitizeOldRowsOnLoad(t *testing.T) {
	h, _ := gradingHub(t)
	store := h.push.store.(*gradingStore)
	longBody := strings.Repeat("密", pushInterceptBodyMaxRunes+80)
	legacy := []map[string]any{{
		"at": h.push.now().UTC(), "level": assistantwire.LevelP1, "reason": interceptReasonP1,
		"title": "legacy card", "body": longBody, "manager": "manager-a",
	}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.vals[heldPushesSettingKey] = append([]byte(nil), raw...)
	store.mu.Unlock()

	restarted := restartPusher(t, h, store)
	view := restarted.heldPushesView("", 0)
	if len(view) != 1 {
		t.Fatalf("reloaded view = %d entries, want 1", len(view))
	}
	if view[0].TodoID != "" || view[0].MachineID != "" || view[0].SessionID != "" {
		t.Fatalf("legacy row gained a source on load: %+v", view[0])
	}
	if n := len([]rune(view[0].Body)); n != pushInterceptBodyMaxRunes || !strings.HasSuffix(view[0].Body, "…") {
		t.Fatalf("reloaded body length %d, want a %d-rune summary", n, pushInterceptBodyMaxRunes)
	}
	if !restarted.waitInterceptsSaved(5 * time.Second) {
		t.Fatal("the sanitized list was not written back")
	}
	store.mu.Lock()
	persisted := append([]byte(nil), store.vals[heldPushesSettingKey]...)
	store.mu.Unlock()
	if bytes.Contains(persisted, []byte(longBody)) {
		t.Fatal("the full legacy body is still in the stored setting row after reload")
	}
	var back []heldPushRecord
	if err := json.Unmarshal(persisted, &back); err != nil || len(back) != 1 {
		t.Fatalf("persisted rows = %+v (err %v), want one sanitized row", back, err)
	}
	if len([]rune(back[0].Body)) != pushInterceptBodyMaxRunes {
		t.Fatalf("persisted body length %d, want the summary limit", len([]rune(back[0].Body)))
	}
	var rawBack []map[string]any
	if err := json.Unmarshal(persisted, &rawBack); err != nil || len(rawBack) != 1 {
		t.Fatalf("raw persisted rows = %s (%v), want one", persisted, err)
	}
	for _, key := range []string{"todo_id", "machine_id", "session_id"} {
		if value, present := rawBack[0][key]; present {
			t.Fatalf("legacy row was written back with %s = %#v; want the optional key omitted", key, value)
		}
	}
}
