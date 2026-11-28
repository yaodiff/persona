package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func putAssistantSettingsReq(s *Service, body string, withTarget bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if withTarget {
		r.Header.Set(assistantwire.ManagerHeader, s.managerIdentity())
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestTodosV2AttentionPrefsValidationAndPersistence(t *testing.T) {
	ctx := context.Background()
	e := directionEnv(t, &memStore{})
	s := e.s

	// 1. explicitSettingsTarget enforcement on PUT /api/v1/assistant.
	for _, body := range []string{
		`{"todos_v2_quiet_hours":"22:00-08:00"}`,
		`{"todos_v2_quiet_hours":""}`,
		`{"todos_v2_digest_window_minutes":60}`,
	} {
		w := putAssistantSettingsReq(s, body, false)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s without manager header: status=%d body=%s, want 400", body, w.Code, w.Body.String())
		}
	}

	// 2. Reject invalid quiet hours and digest window values via PUT.
	for _, body := range []string{
		`{"todos_v2_quiet_hours":"22:00-22:00"}`,
		`{"todos_v2_quiet_hours":"24:00-08:00"}`,
		`{"todos_v2_quiet_hours":"22:60-08:00"}`,
		`{"todos_v2_quiet_hours":"9:00-17:00"}`,
		`{"todos_v2_quiet_hours":"invalid"}`,
		`{"todos_v2_quiet_hours":null}`,
		`{"todos_v2_digest_window_minutes":-1}`,
		`{"todos_v2_digest_window_minutes":15}`,
		`{"todos_v2_digest_window_minutes":45}`,
		`{"todos_v2_digest_window_minutes":240}`,
	} {
		w := putAssistantSettingsReq(s, body, true)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("PUT invalid %s: status=%d body=%s, want 400", body, w.Code, w.Body.String())
		}
	}

	// 3. Valid overnight, daytime, and clearing back to empty + valid digest windows.
	for _, tc := range []struct {
		body       string
		wantQuiet  string
		wantDigest int
	}{
		{`{"todos_v2_quiet_hours":"22:00-08:00","todos_v2_digest_window_minutes":30}`, "22:00-08:00", 30},
		{`{"todos_v2_quiet_hours":"13:00-15:00","todos_v2_digest_window_minutes":120}`, "13:00-15:00", 120},
		{`{"todos_v2_quiet_hours":"","todos_v2_digest_window_minutes":0}`, "", 0},
		{`{"todos_v2_digest_window_minutes":60}`, "", 60},
	} {
		w := putAssistantSettingsReq(s, tc.body, true)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT valid %s: status=%d body=%s, want 200", tc.body, w.Code, w.Body.String())
		}
		var view struct {
			TodosV2QuietHours          string `json:"todos_v2_quiet_hours"`
			TodosV2DigestWindowMinutes int    `json:"todos_v2_digest_window_minutes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode view: %v", err)
		}
		if view.TodosV2QuietHours != tc.wantQuiet || view.TodosV2DigestWindowMinutes != tc.wantDigest {
			t.Fatalf("after %s: view=(quiet=%q digest=%d), want (%q, %d)",
				tc.body, view.TodosV2QuietHours, view.TodosV2DigestWindowMinutes, tc.wantQuiet, tc.wantDigest)
		}
	}

	// 4. persona_settings exposure (get + set + validation + todosChanged).
	byKey := map[string]ownSetting{}
	for _, st := range s.ownSettings() {
		byKey[st.Key] = st
	}
	for _, key := range []string{todosV2QuietHoursSettingKey, todosV2DigestWindowMinutesSettingKey} {
		st, ok := byKey[key]
		if !ok {
			t.Fatalf("persona_settings missing %s", key)
		}
		if !slices.Contains(st.Actions, "get") || !slices.Contains(st.Actions, "set") {
			t.Fatalf("setting %s actions = %v, want get+set", key, st.Actions)
		}
	}

	// Invalid persona_settings values rejected.
	for _, tc := range []struct {
		key string
		raw string
	}{
		{todosV2QuietHoursSettingKey, `"10:00-10:00"`},
		{todosV2QuietHoursSettingKey, `"bad"`},
		{todosV2QuietHoursSettingKey, `null`},
		{todosV2QuietHoursSettingKey, `123`},
		{todosV2DigestWindowMinutesSettingKey, `45`},
		{todosV2DigestWindowMinutesSettingKey, `null`},
		{todosV2DigestWindowMinutesSettingKey, `"60"`},
	} {
		if _, err := byKey[tc.key].set(ctx, json.RawMessage(tc.raw)); err == nil {
			t.Fatalf("persona_settings set %s=%s unexpectedly succeeded", tc.key, tc.raw)
		}
	}

	// Unchanged set is a no-op (apply == nil).
	if ch, err := byKey[todosV2DigestWindowMinutesSettingKey].set(ctx, json.RawMessage(`60`)); err != nil || ch == nil || ch.apply != nil {
		t.Fatalf("unchanged set digest: ch=%+v err=%v", ch, err)
	}

	// Valid sets persist via apply and advance s.trevision.writes.
	for _, tc := range []struct {
		key     string
		raw     string
		wantGet string
	}{
		{todosV2QuietHoursSettingKey, `"23:30-07:30"`, "23:30-07:30"},
		{todosV2DigestWindowMinutesSettingKey, `120`, "120"},
		{todosV2QuietHoursSettingKey, `""`, ""},
	} {
		beforeWrites := s.trevision.writes.Load()
		ch, err := byKey[tc.key].set(ctx, json.RawMessage(tc.raw))
		if err != nil || ch == nil || ch.apply == nil {
			t.Fatalf("prepare set %s=%s: ch=%+v err=%v", tc.key, tc.raw, ch, err)
		}
		if _, err := ch.apply(ctx); err != nil {
			t.Fatalf("apply set %s=%s: %v", tc.key, tc.raw, err)
		}
		got, err := byKey[tc.key].get(ctx)
		if err != nil || got != tc.wantGet {
			t.Fatalf("get %s after set %s: (%q, %v), want %q", tc.key, tc.raw, got, err, tc.wantGet)
		}
		if afterWrites := s.trevision.writes.Load(); afterWrites <= beforeWrites {
			t.Fatalf("apply %s=%s did not trigger todosChanged", tc.key, tc.raw)
		}
	}
}

func TestTodosV2AttentionPushGateQuietHoursAndImmunity(t *testing.T) {
	ctx := context.Background()
	e := directionEnv(t, &memStore{})
	s := e.s
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}

	// Configure quiet hours 22:00-08:00 in Asia/Shanghai, with todos_v2 initially OFF.
	off := false
	win30 := 30
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.TodosV2 = &off
		v.Timezone = "Asia/Shanghai"
		v.TodosV2QuietHours = "22:00-08:00"
		v.TodosV2DigestWindowMinutes = &win30
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 23:15 local time is inside 22:00-08:00.
	now := time.Date(2026, 11, 26, 23, 15, 0, 0, shanghai)
	s.setTodosV2AttentionClock(func() time.Time { return now })

	// 1. todos_v2=false completely bypasses quiet hours and digest window.
	nBypass := Notification{Kind: NotifyPush, SessionID: "s1", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &nBypass)
	if nBypass.Level != assistantwire.LevelP1 {
		t.Fatalf("todos_v2=false should bypass, got level=%q", nBypass.Level)
	}

	// Enable todos_v2=true.
	on := true
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.TodosV2 = &on
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 2. Inside overnight quiet hours: routine P1 and unset level downgrade to LevelP2.
	if _, err := s.pushResult(ctx, Notification{SessionID: "s1", Title: "Routine", Level: assistantwire.LevelP1}); err != nil {
		t.Fatal(err)
	}
	pushes := e.f.pushed()
	if len(pushes) == 0 || pushes[len(pushes)-1].Level != assistantwire.LevelP2 {
		t.Fatalf("routine pushResult during quiet hours: got level=%q, want %q", pushes[len(pushes)-1].Level, assistantwire.LevelP2)
	}

	nDefault := Notification{Kind: NotifyPush, SessionID: "s1"}
	s.applyTodosV2PushAttention(ctx, &nDefault)
	if nDefault.Level != assistantwire.LevelP2 {
		t.Fatalf("default-level routine push during quiet hours: got %q, want %q", nDefault.Level, assistantwire.LevelP2)
	}

	// 3. Strict immunity inside quiet hours: P0, cardPush, CardID, and Asked (notify_user) stay untouched.
	immuneCases := []struct {
		name string
		n    Notification
		want string
	}{
		{"P0", Notification{Kind: NotifyPush, SessionID: "s1", Level: assistantwire.LevelP0}, assistantwire.LevelP0},
		{"notify_user Asked", Notification{Kind: NotifyPush, SessionID: "s1", Level: assistantwire.LevelP1, Asked: true}, assistantwire.LevelP1},
		{"interaction card", Notification{Kind: assistantwire.NotifyInput, SessionID: "s1", Level: assistantwire.LevelP1, Interaction: &proto.Interaction{ID: "ix1"}}, assistantwire.LevelP1},
		{"todo decision card", Notification{Kind: assistantwire.NotifyDecide, SessionID: "s1", Level: assistantwire.LevelP1, Todo: &assistantwire.NotifyTodo{ID: "t1"}}, assistantwire.LevelP1},
		{"card_id push", Notification{Kind: NotifyPush, SessionID: "s1", Level: assistantwire.LevelP1, CardID: "card_1"}, assistantwire.LevelP1},
	}
	for _, tc := range immuneCases {
		got := tc.n
		s.applyTodosV2PushAttention(ctx, &got)
		if got.Level != tc.want {
			t.Fatalf("immune case %s downgraded during quiet hours: got %q, want %q", tc.name, got.Level, tc.want)
		}
	}

	// 4. Outside overnight quiet hours (08:00 boundary is exclusive): routine push passes through untouched.
	now = time.Date(2026, 11, 27, 8, 0, 0, 0, shanghai)
	nMorning := Notification{Kind: NotifyPush, SessionID: "s-morning", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &nMorning)
	if nMorning.Level != assistantwire.LevelP1 {
		t.Fatalf("08:00 boundary should be outside 22:00-08:00, got level=%q", nMorning.Level)
	}

	// 5. Daytime quiet hours (13:00-15:00).
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.TodosV2QuietHours = "13:00-15:00"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 27, 13, 30, 0, 0, shanghai)
	nLunch := Notification{Kind: NotifyPush, SessionID: "s-lunch", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &nLunch)
	if nLunch.Level != assistantwire.LevelP2 {
		t.Fatalf("13:30 should be inside 13:00-15:00, got level=%q", nLunch.Level)
	}
}

func TestTodosV2AttentionPushGateDigestWindow(t *testing.T) {
	ctx := context.Background()
	e := directionEnv(t, &memStore{})
	s := e.s

	on := true
	win60 := 60
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.TodosV2 = &on
		v.TodosV2QuietHours = ""
		v.TodosV2DigestWindowMinutes = &win60
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	cur := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	s.setTodosV2AttentionClock(func() time.Time { return cur })

	// First routine push for session A goes through normally.
	n1 := Notification{Kind: NotifyPush, SessionID: "sess-a", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &n1)
	if n1.Level != assistantwire.LevelP1 {
		t.Fatalf("first push for sess-a: got %q, want %q", n1.Level, assistantwire.LevelP1)
	}

	// Burst follow-up 15m later for same session A is downgraded to LevelP2.
	cur = cur.Add(15 * time.Minute)
	n2 := Notification{Kind: NotifyPush, SessionID: "sess-a", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &n2)
	if n2.Level != assistantwire.LevelP2 {
		t.Fatalf("burst push for sess-a within 60m: got %q, want %q", n2.Level, assistantwire.LevelP2)
	}

	// Different session B has its own bucket and passes through.
	nB := Notification{Kind: NotifyPush, SessionID: "sess-b", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &nB)
	if nB.Level != assistantwire.LevelP1 {
		t.Fatalf("first push for sess-b: got %q, want %q", nB.Level, assistantwire.LevelP1)
	}

	// P0 and Asked pushes inside the window are never downgraded.
	nP0 := Notification{Kind: NotifyPush, SessionID: "sess-a", Level: assistantwire.LevelP0}
	s.applyTodosV2PushAttention(ctx, &nP0)
	if nP0.Level != assistantwire.LevelP0 {
		t.Fatalf("P0 inside digest window: got %q, want %q", nP0.Level, assistantwire.LevelP0)
	}
	nAsked := Notification{Kind: NotifyPush, SessionID: "sess-a", Level: assistantwire.LevelP1, Asked: true}
	s.applyTodosV2PushAttention(ctx, &nAsked)
	if nAsked.Level != assistantwire.LevelP1 {
		t.Fatalf("Asked inside digest window: got %q, want %q", nAsked.Level, assistantwire.LevelP1)
	}

	// After 60m window elapses (t=10:00 -> t=11:01), next push for sess-a passes through again.
	cur = time.Date(2026, 11, 26, 11, 1, 0, 0, time.UTC)
	n3 := Notification{Kind: NotifyPush, SessionID: "sess-a", Level: assistantwire.LevelP1}
	s.applyTodosV2PushAttention(ctx, &n3)
	if n3.Level != assistantwire.LevelP1 {
		t.Fatalf("push after digest window elapsed: got %q, want %q", n3.Level, assistantwire.LevelP1)
	}
}

func TestTodosV2AttentionDecisionGroups(t *testing.T) {
	ctx := context.Background()
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)

	getGroups := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/todos/decisions/groups", nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}

	// 1. Gated under todos_v2=true: returns 409 Conflict when disabled.
	if w := getGroups(); w.Code != http.StatusConflict {
		t.Fatalf("GET /api/v1/todos/decisions/groups with todos_v2=false: status=%d body=%s, want 409", w.Code, w.Body.String())
	}

	// Enable todos_v2=true.
	on := true
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.TodosV2 = &on
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	doneAt := now.Add(-time.Minute)

	seedNeedsUser := func(id, title, prURL, partOf, dirID, reason string, done bool) {
		td := &Todo{
			ID:          id,
			Title:       title,
			PRURL:       prURL,
			PartOf:      partOf,
			DirectionID: dirID,
			CreatedAt:   now,
			UpdatedAt:   now,
			Supervision: &Supervision{
				State: reviewNeedsUser,
				Reviews: []TaskReview{
					{At: now, Verdict: "needs_user", Reason: reason},
				},
			},
		}
		if done {
			td.DoneAt = &doneAt
		}
		s.todos.mu.Lock()
		s.todos.loaded = true
		s.todos.todos[id] = td
		s.todos.mu.Unlock()
	}

	// Parent goal (no pending decision itself, provides label for shared_parent group).
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.todos["goal_parent"] = &Todo{ID: "goal_parent", Title: "Ship v2 release", CreatedAt: now, UpdatedAt: now}
	s.todos.todos["open_no_ask"] = &Todo{ID: "open_no_ask", Title: "Working item", CreatedAt: now, UpdatedAt: now}
	s.todos.mu.Unlock()

	// Group A (shared_blocker, 3 items): even though t_blk_3 also has PartOf/PRURL, shared blocker wins.
	sharedBlockerURL := "https://github.com/example/repo/pull/999"
	seedNeedsUser("t_blk_1", "Task Blocker 1", "", "", "dir1", "Wait for upstream", false)
	seedNeedsUser("t_blk_2", "Task Blocker 2", "", "", "dir1", "Wait for upstream", false)
	seedNeedsUser("t_blk_3", "Task Blocker 3", "https://github.com/example/repo/pull/101", "goal_parent", "dir1", "Different reason", false)
	for _, id := range []string{"t_blk_1", "t_blk_2", "t_blk_3"} {
		if _, err := st.UpsertTodoBlocker(ctx, db.TodoBlockerRow{
			ID:        "blk_" + id,
			TodoID:    id,
			Kind:      "external_pr",
			TargetRef: sharedBlockerURL,
			State:     "active",
			CreatedAt: now,
			UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Group B (shared_pr, 2 items): same PRURL.
	sharedPR := "https://github.com/example/repo/pull/200"
	seedNeedsUser("t_pr_1", "Task PR 1", sharedPR, "goal_parent", "dir1", "Review API", false)
	seedNeedsUser("t_pr_2", "Task PR 2", sharedPR+"/", "goal_parent", "dir1", "Approve schema", false)

	// Group C (shared_parent, 2 items): distinct PRs, same PartOf.
	seedNeedsUser("t_par_1", "Child 1", "https://github.com/example/repo/pull/301", "goal_parent", "dir2", "Reason A", false)
	seedNeedsUser("t_par_2", "Child 2", "https://github.com/example/repo/pull/302", "goal_parent", "dir2", "Reason B", false)

	// Group D (shared_cause fallback, 2 items): same direction + normalized reason.
	seedNeedsUser("t_fb_1", "Fallback 1", "", "", "dir3", "Choose  rollout   window", false)
	seedNeedsUser("t_fb_2", "Fallback 2", "", "", "dir3", "choose rollout window", false)

	// Singleton (1 item).
	seedNeedsUser("t_solo", "Solo decision", "https://github.com/example/repo/pull/404", "", "dir4", "Unique question", false)

	// Completed item must be excluded.
	seedNeedsUser("t_done", "Completed decision", sharedPR, "", "dir1", "Old question", true)

	w := getGroups()
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos/decisions/groups: status=%d body=%s, want 200", w.Code, w.Body.String())
	}
	var got decisionGroupsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode groups: %v", err)
	}
	if got.TotalPending != 10 {
		t.Fatalf("total_pending = %d, want 10 (groups=%+v)", got.TotalPending, got.Groups)
	}
	if len(got.Groups) != 5 {
		t.Fatalf("len(groups) = %d, want 5: %+v", len(got.Groups), got.Groups)
	}

	// First group must be the 3-item shared_blocker group.
	g0 := got.Groups[0]
	if g0.CauseKind != "shared_blocker" || g0.Count != 3 || g0.URL != sharedBlockerURL ||
		!slices.Equal(g0.TodoIDs, []string{"t_blk_1", "t_blk_2", "t_blk_3"}) {
		t.Fatalf("groups[0] = %+v, want shared_blocker count=3", g0)
	}

	// Next three groups have count=2, sorted by key asc: cause:..., parent:..., pr:...
	if got.Groups[1].CauseKind != "shared_cause" || !slices.Equal(got.Groups[1].TodoIDs, []string{"t_fb_1", "t_fb_2"}) {
		t.Fatalf("groups[1] = %+v, want shared_cause [t_fb_1 t_fb_2]", got.Groups[1])
	}
	if got.Groups[2].CauseKind != "shared_parent" || got.Groups[2].Label != "Ship v2 release" ||
		!slices.Equal(got.Groups[2].TodoIDs, []string{"t_par_1", "t_par_2"}) {
		t.Fatalf("groups[2] = %+v, want shared_parent Ship v2 release [t_par_1 t_par_2]", got.Groups[2])
	}
	if got.Groups[3].CauseKind != "shared_pr" || got.Groups[3].URL != sharedPR ||
		!slices.Equal(got.Groups[3].TodoIDs, []string{"t_pr_1", "t_pr_2"}) {
		t.Fatalf("groups[3] = %+v, want shared_pr [t_pr_1 t_pr_2]", got.Groups[3])
	}
	if got.Groups[4].CauseKind != "singleton" || got.Groups[4].Count != 1 ||
		!slices.Equal(got.Groups[4].TodoIDs, []string{"t_solo"}) {
		t.Fatalf("groups[4] = %+v, want singleton [t_solo]", got.Groups[4])
	}
}
