package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTodosV2ConsistencyPackDirectionTreeDirectionID(t *testing.T) {
	ctx := context.Background()
	e := treeEnv(t)

	explicitMove := e.task("Explicit direction overrides supervisor", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DirectionID = "conv_lead_b"
	})
	explicitUnknown := e.task("Explicit non-leader direction goes to secretary", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
		todo.DirectionID = "dir_non_leader"
	})
	originOnly := e.task("Unsupervised task with direction origin", func(todo *Todo) {
		todo.Origin = "conv_lead_b"
	})
	supFallback := e.task("Empty direction_id falls back to supervisor", func(todo *Todo) {
		todo.Supervision = &Supervision{Supervisor: "conv_lead_a"}
	})

	groupTasks := func(tree directionTree) map[string][]string {
		out := map[string][]string{}
		for _, d := range tree.Directions {
			key := d.ID
			if d.Secretary {
				key = "secretary"
			}
			out[key] = sortedIDs(taskIDs(d)...)
		}
		return out
	}

	// todos_v2=false (default): legacy supervisor-only grouping is completely unchanged.
	legacy := groupTasks(e.tree())
	if wantA := sortedIDs(explicitMove, explicitUnknown, supFallback); !slices.Equal(legacy["conv_lead_a"], wantA) {
		t.Fatalf("todos_v2=false conv_lead_a = %v, want %v", legacy["conv_lead_a"], wantA)
	}
	if len(legacy["conv_lead_b"]) != 0 {
		t.Fatalf("todos_v2=false conv_lead_b = %v, want empty", legacy["conv_lead_b"])
	}
	if wantSec := sortedIDs(originOnly); !slices.Equal(legacy["secretary"], wantSec) {
		t.Fatalf("todos_v2=false secretary = %v, want %v", legacy["secretary"], wantSec)
	}

	// Enable todos_v2=true: explicit DirectionID wins (matching board.direction_groups);
	// unset DirectionID falls back to Supervisor / Origin.
	on := true
	setSettings(t, e.s, func(v *Settings) { v.TodosV2 = &on })
	v2 := groupTasks(e.tree())
	if wantA := sortedIDs(supFallback); !slices.Equal(v2["conv_lead_a"], wantA) {
		t.Fatalf("todos_v2=true conv_lead_a = %v, want %v", v2["conv_lead_a"], wantA)
	}
	if wantB := sortedIDs(explicitMove, originOnly); !slices.Equal(v2["conv_lead_b"], wantB) {
		t.Fatalf("todos_v2=true conv_lead_b = %v, want %v", v2["conv_lead_b"], wantB)
	}
	if wantSec := sortedIDs(explicitUnknown); !slices.Equal(v2["secretary"], wantSec) {
		t.Fatalf("todos_v2=true secretary = %v, want %v", v2["secretary"], wantSec)
	}

	// Verify single-source-of-truth parity with buildTodoBoardProjection for explicitMove.
	listed, err := e.s.listedTodos(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	board := buildTodoBoardProjection(listed, map[string]string{
		"conv_lead_a": "Android parity",
		"conv_lead_b": "Push relay",
	})
	var boardLeadB []string
	for _, g := range board.DirectionGroups {
		if g.DirectionID == "conv_lead_b" {
			boardLeadB = sortedIDs(g.TodoIDs...)
		}
	}
	if !slices.Equal(boardLeadB, v2["conv_lead_b"]) {
		t.Fatalf("board.direction_groups[conv_lead_b] = %v != direction_tree = %v", boardLeadB, v2["conv_lead_b"])
	}

	// Toggle todos_v2 back off: immediately reverts to legacy grouping.
	off := false
	setSettings(t, e.s, func(v *Settings) { v.TodosV2 = &off })
	reverted := groupTasks(e.tree())
	if !slices.Equal(reverted["conv_lead_a"], legacy["conv_lead_a"]) ||
		!slices.Equal(reverted["conv_lead_b"], legacy["conv_lead_b"]) ||
		!slices.Equal(reverted["secretary"], legacy["secretary"]) {
		t.Fatalf("todos_v2=false rollback mismatch: got %+v, want %+v", reverted, legacy)
	}
}

func TestTodosV2ConsistencyPackReviewNudgeSLA(t *testing.T) {
	ctx := context.Background()

	newPending := func(t *testing.T) (*Service, string, time.Time) {
		t.Helper()
		s, f, todo := newSupervisedService(t)
		quietDigest(s)
		holdDigest(s)
		waitsForReview(t, s, f, todo.ID, "Ready for review.")
		cur := todoNow(s, todo.ID)
		if cur.Supervision == nil || cur.Supervision.State != reviewPending || cur.Supervision.PendingSince == nil {
			t.Fatalf("expected pending_review with PendingSince, got %+v", cur.Supervision)
		}
		if w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full",
			`{"todos_v2":true,"expected_todos_v2":false}`); w.Code != http.StatusOK {
			t.Fatalf("enable todos_v2: code=%d body=%s", w.Code, w.Body.String())
		}
		return s, todo.ID, *cur.Supervision.PendingSince
	}

	setSLA := func(t *testing.T, s *Service, hours int) {
		t.Helper()
		body := fmt.Sprintf(`{"todos_v2_pending_review_sla_hours":%d}`, hours)
		if w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full", body); w.Code != http.StatusOK {
			t.Fatalf("set pending_review_sla_hours=%d: code=%d body=%s", hours, w.Code, w.Body.String())
		}
	}

	t.Run("sla_0_suppresses_nudge", func(t *testing.T) {
		s, todoID, since := newPending(t)
		setSLA(t, s, 0)
		s.superviseWatch(ctx, since.Add(3*time.Hour))
		if slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=0 should suppress %s, got %v", sigReviewNudge, pendingKinds(s, todoID))
		}
	})

	t.Run("sla_12_caps_window_at_12h", func(t *testing.T) {
		s, todoID, since := newPending(t)
		setSLA(t, s, 12)
		// Before min(2h, 6h) = 2h -> no nudge.
		s.superviseWatch(ctx, since.Add(2*time.Hour-time.Minute))
		if slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=12 emitted %s early: %v", sigReviewNudge, pendingKinds(s, todoID))
		}
		// At/past 12h ceiling -> no nudge (already escalated to needs_you).
		s.superviseWatch(ctx, since.Add(12*time.Hour))
		if slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=12 emitted %s at ceiling: %v", sigReviewNudge, pendingKinds(s, todoID))
		}
		// Between 2h and 12h -> emits nudge.
		s.superviseWatch(ctx, since.Add(3*time.Hour))
		if !slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=12 did not emit %s inside [2h,12h): %v", sigReviewNudge, pendingKinds(s, todoID))
		}
	})

	t.Run("sla_48_extends_window_past_24h", func(t *testing.T) {
		s, todoID, since := newPending(t)
		setSLA(t, s, 48)
		// Past legacy hardcoded 24h, still < 48h -> must still emit nudge.
		s.superviseWatch(ctx, since.Add(30*time.Hour))
		if !slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=48 did not emit %s at 30h: %v", sigReviewNudge, pendingKinds(s, todoID))
		}
	})

	t.Run("sla_48_stops_at_48h_and_todos_v2_off_bypasses", func(t *testing.T) {
		s, todoID, since := newPending(t)
		setSLA(t, s, 48)
		s.superviseWatch(ctx, since.Add(48*time.Hour))
		if slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("sla=48 emitted %s at 48h ceiling: %v", sigReviewNudge, pendingKinds(s, todoID))
		}
		// Disable todos_v2 -> no nudge even inside [2h, 48h).
		if w, _ := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", s.managerIdentity(), "full",
			`{"todos_v2":false,"expected_todos_v2":true}`); w.Code != http.StatusOK {
			t.Fatalf("disable todos_v2: code=%d body=%s", w.Code, w.Body.String())
		}
		s.superviseWatch(ctx, since.Add(3*time.Hour))
		if slices.Contains(pendingKinds(s, todoID), sigReviewNudge) {
			t.Fatalf("todos_v2=false emitted %s: %v", sigReviewNudge, pendingKinds(s, todoID))
		}
	})
}

func TestTodosV2ConsistencyPackReorganizePreservesValidAfter(t *testing.T) {
	ctx := context.Background()
	s, _ := transferEnv(t)
	setBlockersTodosV2Flag(t, s, true, false)

	goalA, err := s.createTodo(ctx, "Goal A", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	goalB, err := s.createTodo(ctx, "Goal B", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step1, err := s.createTodo(ctx, "Step 1", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step2, err := s.createTodo(ctx, "Step 2", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	step3, err := s.createTodo(ctx, "Step 3", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	cyclicPeer, err := s.createTodo(ctx, "Cyclic peer", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Set initial part_of and After links.
	s.todos.mu.Lock()
	s.todos.todos[step1.ID].PartOf = goalA.ID
	s.todos.todos[step2.ID].PartOf = goalA.ID
	s.todos.todos[step2.ID].After = []string{step1.ID}
	s.todos.todos[step3.ID].PartOf = goalA.ID
	s.todos.todos[step3.ID].After = []string{step2.ID, step3.ID, "todo_missing", cyclicPeer.ID, step2.ID}
	s.todos.todos[cyclicPeer.ID].After = []string{step3.ID}
	s.todos.mu.Unlock()

	// 1. Reparenting step2 from goalA -> goalB must preserve its valid acyclic After=[step1].
	w := reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, step2.ID, goalB.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("reparent step2: code=%d body=%s", w.Code, w.Body.String())
	}
	got2, _ := s.todoNow(step2.ID)
	if got2.PartOf != goalB.ID || !slices.Equal(got2.After, []string{step1.ID}) {
		t.Fatalf("reparent step2: part_of=%q after=%v, want %q / [%s]", got2.PartOf, got2.After, goalB.ID, step1.ID)
	}

	// 2. Promoting step2 to top-level (part_of: "") must still preserve After=[step1].
	w = reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":""}`, step2.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("promote step2: code=%d body=%s", w.Code, w.Body.String())
	}
	got2, _ = s.todoNow(step2.ID)
	if got2.PartOf != "" || !slices.Equal(got2.After, []string{step1.ID}) {
		t.Fatalf("promote step2: part_of=%q after=%v, want \"\" / [%s]", got2.PartOf, got2.After, step1.ID)
	}

	// 3. Reparenting step3 prunes self-reference, missing target, duplicate, and cycle-inducing edge,
	// while keeping the valid acyclic edge [step2].
	w = reorganizeRequest(s, "full", fmt.Sprintf(`{"ids":[%q],"part_of":%q}`, step3.ID, goalB.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("reparent step3: code=%d body=%s", w.Code, w.Body.String())
	}
	got3, _ := s.todoNow(step3.ID)
	if got3.PartOf != goalB.ID || !slices.Equal(got3.After, []string{step2.ID}) {
		t.Fatalf("reparent step3: part_of=%q after=%v, want %q / [%s]", got3.PartOf, got3.After, goalB.ID, step2.ID)
	}
}

func TestTodosV2ConsistencyPackPersonaSettings(t *testing.T) {
	ctx := context.Background()
	e := directionEnv(t, &memStore{})
	s := e.s

	wantKeys := []string{
		todosV2AutoSeenSettingKey,
		todosV2PendingReviewSLASettingKey,
		todosV2CanWaitSLASettingKey,
		todosV2FoldChildrenSettingKey,
	}

	// 1. All 4 keys must be registered in ownSettings() with get + set actions.
	byKey := map[string]ownSetting{}
	for _, st := range s.ownSettings() {
		byKey[st.Key] = st
	}
	for _, key := range wantKeys {
		st, ok := byKey[key]
		if !ok {
			t.Fatalf("persona_settings missing %s", key)
		}
		if !slices.Contains(st.Actions, "get") || !slices.Contains(st.Actions, "set") {
			t.Fatalf("setting %s actions = %v, want get+set", key, st.Actions)
		}
	}

	// 2. Verify defaults via get.
	defaults := map[string]string{
		todosV2AutoSeenSettingKey:         todosV2AutoSeenAllAutonomous,
		todosV2PendingReviewSLASettingKey: "24",
		todosV2CanWaitSLASettingKey:       "48",
		todosV2FoldChildrenSettingKey:     "true",
	}
	for key, want := range defaults {
		got, err := byKey[key].get(ctx)
		if err != nil || got != want {
			t.Fatalf("get %s = (%q, %v), want %q", key, got, err, want)
		}
	}

	// 3. Invalid values must be rejected clearly (including null for int/bool keys).
	invalidCases := []struct {
		key string
		raw string
	}{
		{todosV2AutoSeenSettingKey, `"bogus"`},
		{todosV2AutoSeenSettingKey, `123`},
		{todosV2PendingReviewSLASettingKey, `36`},
		{todosV2PendingReviewSLASettingKey, `null`},
		{todosV2PendingReviewSLASettingKey, `"24"`},
		{todosV2CanWaitSLASettingKey, `12`},
		{todosV2CanWaitSLASettingKey, `null`},
		{todosV2FoldChildrenSettingKey, `"yes"`},
		{todosV2FoldChildrenSettingKey, `null`},
	}
	for _, tc := range invalidCases {
		if _, err := byKey[tc.key].set(ctx, json.RawMessage(tc.raw)); err == nil {
			t.Fatalf("set %s=%s unexpectedly succeeded", tc.key, tc.raw)
		}
	}

	// 4. Setting to current default is a no-op (apply == nil).
	if ch, err := byKey[todosV2AutoSeenSettingKey].set(ctx, json.RawMessage(`"all_autonomous"`)); err != nil || ch == nil || ch.apply != nil {
		t.Fatalf("unchanged set %s: ch=%+v err=%v", todosV2AutoSeenSettingKey, ch, err)
	}

	// 5. Valid sets persist through apply, update get, and trigger todosChanged (advancing s.trevision.writes).
	validSets := []struct {
		key     string
		raw     string
		wantGet string
	}{
		{todosV2AutoSeenSettingKey, `"subtasks"`, "subtasks"},
		{todosV2PendingReviewSLASettingKey, `0`, "0"},
		{todosV2CanWaitSLASettingKey, `72`, "72"},
		{todosV2FoldChildrenSettingKey, `false`, "false"},
	}
	for _, tc := range validSets {
		beforeWrites := s.trevision.writes.Load()
		ch, err := byKey[tc.key].set(ctx, json.RawMessage(tc.raw))
		if err != nil || ch == nil || ch.apply == nil {
			t.Fatalf("prepare set %s=%s: ch=%+v err=%v", tc.key, tc.raw, ch, err)
		}
		if strings.TrimSpace(ch.card(s.lang(ctx))) == "" {
			t.Fatalf("empty approval card text for %s=%s", tc.key, tc.raw)
		}
		if _, err := ch.apply(ctx); err != nil {
			t.Fatalf("apply set %s=%s: %v", tc.key, tc.raw, err)
		}
		got, err := byKey[tc.key].get(ctx)
		if err != nil || got != tc.wantGet {
			t.Fatalf("after set %s=%s: get=(%q, %v), want %q", tc.key, tc.raw, got, err, tc.wantGet)
		}
		if afterWrites := s.trevision.writes.Load(); afterWrites <= beforeWrites {
			t.Fatalf("apply %s=%s did not trigger todosChanged (writes before=%d after=%d)", tc.key, tc.raw, beforeWrites, afterWrites)
		}
	}

	// 6. Verify Settings persisted all 4 values cleanly and still do not enable todos_v2 by themselves.
	cfg := s.settings(ctx)
	if cfg.todosV2On() {
		t.Fatal("setting todos_v2_* prefs must not flip todos_v2 on")
	}
	if cfg.todosV2AutoSeenMode() != todosV2AutoSeenSubtasks ||
		cfg.todosV2PendingReviewSLAHours() != 0 ||
		cfg.todosV2CanWaitSLAHours() != 72 ||
		cfg.todosV2FoldChildrenOn() {
		t.Fatalf("persisted prefs mismatch: auto_seen=%q review_sla=%d can_wait_sla=%d fold=%v",
			cfg.todosV2AutoSeenMode(), cfg.todosV2PendingReviewSLAHours(), cfg.todosV2CanWaitSLAHours(), cfg.todosV2FoldChildrenOn())
	}
}
