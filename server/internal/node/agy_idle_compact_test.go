package node

import (
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

type testAgyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testAgyClock) get() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testAgyClock) set(at time.Time) {
	c.mu.Lock()
	c.now = at
	c.mu.Unlock()
}

func setAgyIdleClock(n *Node, start time.Time) *testAgyClock {
	c := &testAgyClock{now: start}
	n.agyIdleMu.Lock()
	n.agyIdleClock = c.get
	n.agyIdleMu.Unlock()
	return c
}

func pushAgyRow(t *testing.T, n *Node, host *fakeRuntime, rows ...proto.Session) {
	t.Helper()
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": rows}); err != nil {
		t.Fatal(err)
	}
	for _, want := range rows {
		waitFor(t, "agy row updated", func() bool {
			got, _, ok := n.runtimes.rowOf(want.SessionID)
			return ok && got.Status == want.Status && got.Rev == want.Rev &&
				got.ContextTokens == want.ContextTokens && got.TurnError == want.TurnError
		})
	}
}

func TestAgyIdleAutoCompactSweepTaskAgentAndAssistant(t *testing.T) {
	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)

	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	assistantID := runtimeCompactTestID
	taskID := "agy-22222222-3333-4444-5555-666666666666"
	n.state.put(assistantID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, ".persona", "assistant")})
	n.state.put(taskID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj")})

	// Below token threshold (199,999) and below idle threshold (9m) must not compact.
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 1, ContextTokens: 199_999, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusIdle, Rev: 1, ContextTokens: 250_000, UpdatedAt: base},
	)
	clock.set(base.Add(9 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	if sends := host.got("session.send"); len(sends) != 0 {
		t.Fatalf("unexpected compact before idle threshold: %s", sends)
	}

	// Cross both thresholds (10m idle, >= 200,000 tokens) for both sessions.
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 2, ContextTokens: 210_000, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusIdle, Rev: 1, ContextTokens: 250_000, UpdatedAt: base},
	)
	clock.set(base.Add(10 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())

	waitFor(t, "both sessions receive /compact", func() bool {
		return len(host.got("session.send")) == 2
	})
	var sawAssistant, sawTask bool
	for _, raw := range host.got("session.send") {
		var p map[string]any
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p["text"] != "/compact" {
			t.Fatalf("expected /compact, got %v", p)
		}
		switch p["session_id"] {
		case assistantID:
			if p["assistant"] != true {
				t.Fatalf("assistant session /compact missing assistant=true: %v", p)
			}
			sawAssistant = true
		case taskID:
			if _, ok := p["assistant"]; ok {
				t.Fatalf("task Agent /compact should not set assistant flag: %v", p)
			}
			sawTask = true
		default:
			t.Fatalf("unexpected session_id: %v", p)
		}
	}
	if !sawAssistant || !sawTask {
		t.Fatalf("missing compact call: assistant=%v task=%v", sawAssistant, sawTask)
	}

	// While runtime reports working during silent compaction, Node preserves pre-compaction status.
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusWorking, Rev: 3, ContextTokens: 210_000, UpdatedAt: clock.get()},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusWorking, Rev: 2, ContextTokens: 250_000, UpdatedAt: clock.get()},
	)
	rows := listed(t, n)
	if rows[assistantID].Status != proto.StatusDone {
		t.Fatalf("silent assistant compaction changed status to %q, want %q", rows[assistantID].Status, proto.StatusDone)
	}
	if rows[taskID].Status != proto.StatusIdle {
		t.Fatalf("silent task compaction changed status to %q, want %q", rows[taskID].Status, proto.StatusIdle)
	}

	// Settle compaction without shrinking context yet: latch must prevent duplicate sweep compaction.
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 4, ContextTokens: 210_000, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusIdle, Rev: 3, ContextTokens: 50_000, UpdatedAt: clock.get()},
	)
	waitFor(t, "both compactions finished", func() bool {
		return !n.runtimes.compacting(assistantID) && !n.runtimes.compacting(taskID)
	})
	clock.set(clock.get().Add(15 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	if sends := host.got("session.send"); len(sends) != 2 {
		t.Fatalf("latched session compacted again: %s", sends)
	}

	// Task Agent dropped to 50k (rearmed), then grows back to 220k and idles 10m -> compacts again.
	growAt := clock.get()
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 4, ContextTokens: 210_000, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone, Rev: 4, ContextTokens: 220_000, UpdatedAt: growAt},
	)
	clock.set(growAt.Add(10 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	waitFor(t, "rearmed task session compacted again", func() bool {
		return len(host.got("session.send")) == 3
	})
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 4, ContextTokens: 210_000, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone, Rev: 5, ContextTokens: 45_000, UpdatedAt: clock.get()},
	)
	waitFor(t, "third compaction settled", func() bool {
		return !n.runtimes.compacting(taskID)
	})
}

func TestAgyIdleAutoCompactBeforeInputTaskAgentAndAssistant(t *testing.T) {
	base := time.Date(2026, 11, 27, 11, 0, 0, 0, time.UTC)

	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	assistantID := runtimeCompactTestID
	taskID := "agy-33333333-4444-5555-6666-777777777777"
	n.state.put(assistantID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, ".persona", "assistant")})
	n.state.put(taskID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj")})

	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 1, ContextTokens: 260_000, UpdatedAt: base},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base},
	)

	var orderMu sync.Mutex
	var order []string
	var compactFinished bool
	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method != "session.send" {
			return map[string]any{"ok": true}, nil
		}
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		_ = json.Unmarshal(params, &p)
		orderMu.Lock()
		order = append(order, p.SessionID+":"+p.Text)
		if p.Text == "/compact" {
			compactFinished = false
		} else if !compactFinished {
			orderMu.Unlock()
			return nil, errors.New("input arrived before compaction settled")
		}
		orderMu.Unlock()
		if p.Text == "/compact" {
			go func(sid string) {
				orderMu.Lock()
				compactFinished = true
				orderMu.Unlock()
				cur, _, _ := n.runtimes.rowOf(sid)
				cur.Rev++
				cur.Status = proto.StatusDone
				cur.ContextTokens = 40_000
				cur.UpdatedAt = clock.get()
				otherID := assistantID
				if sid == assistantID {
					otherID = taskID
				}
				other, _, _ := n.runtimes.rowOf(otherID)
				_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{cur, other}})
			}(p.SessionID)
		}
		return map[string]any{"ok": true}, nil
	}
	host.mu.Unlock()

	// Advance past 10 minutes idle and send new input to task Agent via session.send.
	clock.set(base.Add(12 * time.Minute))
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": taskID, "text": "continue task", "client_msg_id": "m-task-1",
	}); err != nil {
		t.Fatalf("task session.send: %v", err)
	}

	// Send new input to AGY assistant session via assistant.send (assistantSendRuntime holds typing.lock).
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": assistantID, "text": "hello assistant", "launcher": "agy",
	}); err != nil {
		t.Fatalf("assistant.send: %v", err)
	}

	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	wantOrder := []string{
		taskID + ":/compact",
		taskID + ":continue task",
		assistantID + ":/compact",
		assistantID + ":hello assistant",
	}
	if strings.Join(gotOrder, "|") != strings.Join(wantOrder, "|") {
		t.Fatalf("delivery order = %v, want %v", gotOrder, wantOrder)
	}

	// Sending /compact directly when over thresholds must NOT send a duplicate /compact.
	t2 := base.Add(30 * time.Minute)
	pushAgyRow(t, n, host,
		proto.Session{SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 10, ContextTokens: 260_000, UpdatedAt: t2},
		proto.Session{SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone, Rev: 10, ContextTokens: 260_000, UpdatedAt: t2},
	)
	clock.set(t2.Add(15 * time.Minute))
	orderMu.Lock()
	order = nil
	orderMu.Unlock()
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": taskID, "text": "/compact", "client_msg_id": "m-compact-1",
	}); err != nil {
		t.Fatalf("manual /compact: %v", err)
	}
	orderMu.Lock()
	gotManual := append([]string(nil), order...)
	orderMu.Unlock()
	if len(gotManual) != 1 || gotManual[0] != taskID+":/compact" {
		t.Fatalf("manual /compact triggered extra compaction: %v", gotManual)
	}
}

func TestAgyIdleAutoCompactWaitsForInFlightSweepBeforeAssistantInput(t *testing.T) {
	base := time.Date(2026, 11, 27, 11, 30, 0, 0, time.UTC)

	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	assistantID := runtimeCompactTestID
	n.state.put(assistantID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, ".persona", "assistant")})
	pushAgyRow(t, n, host, proto.Session{
		SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 1, ContextTokens: 250_000, UpdatedAt: base,
	})

	clock.set(base.Add(11 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	waitFor(t, "sweep started silent compaction", func() bool {
		return n.runtimes.compacting(assistantID)
	})

	done := make(chan error, 1)
	go func() {
		_, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id": assistantID, "text": "after sweep", "launcher": "agy",
		})
		done <- err
	}()

	waitFor(t, "assistant.send waiting on in-flight silent sweep", func() bool {
		unlock, ok := n.typing.tryLock(assistantID)
		if ok {
			unlock()
			return false
		}
		return true
	})
	select {
	case err := <-done:
		t.Fatalf("assistant.send returned before silent compaction settled: %v", err)
	default:
	}

	// Settle the silent compaction; assistant.send must now succeed instead of refusing with "compacting".
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{{
		SessionID: assistantID, Cwd: filepath.Join(n.home, ".persona", "assistant"), Status: proto.StatusDone, Rev: 2, ContextTokens: 35_000, UpdatedAt: clock.get(),
	}}}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("assistant.send after silent sweep failed: %v", err)
	}
	sends := host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("expected 1 sweep /compact + 1 user message, got %d: %s", len(sends), sends)
	}
}

func TestAgyIdleAutoCompactBeforeInputFailureStillDelivers(t *testing.T) {
	base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	taskID := "agy-44444444-5555-6666-7777-888888888888"
	n.state.put(taskID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj")})
	pushAgyRow(t, n, host, proto.Session{
		SessionID: taskID, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone, Rev: 1, ContextTokens: 300_000, UpdatedAt: base,
	})

	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.send" {
			var p struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Text == "/compact" {
				return nil, &rpc.Error{Code: "compact_failed", Message: "temporary failure"}
			}
		}
		return map[string]any{"ok": true}, nil
	}
	host.mu.Unlock()

	clock.set(base.Add(15 * time.Minute))
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": taskID, "text": "still deliver me", "client_msg_id": "m-fail-1",
	}); err != nil {
		t.Fatalf("message should still be delivered after best-effort compaction failure: %v", err)
	}
	sends := host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("expected /compact attempt + user message, got %d: %s", len(sends), sends)
	}
}

func TestAgyIdleAutoCompactGuardsAndConfiguration(t *testing.T) {
	base := time.Date(2026, 11, 27, 13, 0, 0, 0, time.UTC)

	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	h := n.runtimes.ownerOf(runtimeCompactTestID)
	managedID := runtimeCompactTestID
	unmanagedID := "agy-55555555-6666-7777-8888-999999999999"
	n.state.put(managedID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj")})

	readyRow := func(id string) proto.Session {
		return proto.Session{
			SessionID: id, Cwd: filepath.Join(n.home, "proj"), Status: proto.StatusDone,
			Rev: 1, ContextTokens: 250_000, UpdatedAt: base,
		}
	}

	clock.set(base.Add(15 * time.Minute))
	cases := []struct {
		name  string
		setup func() (proto.Session, func())
	}{
		{"unmanaged session", func() (proto.Session, func()) { return readyRow(unmanagedID), nil }},
		{"working", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Status = proto.StatusWorking
			return r, nil
		}},
		{"blocked", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Status = proto.StatusBlocked
			return r, nil
		}},
		{"starting", func() (proto.Session, func()) { r := readyRow(managedID); r.Starting = true; return r, nil }},
		{"archived", func() (proto.Session, func()) { r := readyRow(managedID); r.Archived = true; return r, nil }},
		{"subagent", func() (proto.Session, func()) { r := readyRow(managedID); r.IsSubagent = true; return r, nil }},
		{"needs you state", func() (proto.Session, func()) { r := readyRow(managedID); r.State = proto.StateNeedsYou; return r, nil }},
		{"pending interactions on row", func() (proto.Session, func()) { r := readyRow(managedID); r.PendingInteractions = 1; return r, nil }},
		{"background working", func() (proto.Session, func()) { r := readyRow(managedID); r.BackgroundWorking = true; return r, nil }},
		{"background waiting", func() (proto.Session, func()) { r := readyRow(managedID); r.BackgroundWaiting = true; return r, nil }},
		{"background tasks list", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Background = []proto.BackgroundTask{{ID: "bg1"}}
			return r, nil
		}},
		{"woke by task", func() (proto.Session, func()) { r := readyRow(managedID); r.WokeByTask = true; return r, nil }},
		{"loop active", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Loop = &proto.LoopInfo{Prompt: "watch"}
			return r, nil
		}},
		{"running subagent", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Subagents = []proto.Subagent{{ID: "s1", Running: true}}
			return r, nil
		}},
		{"running workflow", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Workflows = []proto.Workflow{{TaskID: "w1", Running: true}}
			return r, nil
		}},
		{"queued messages", func() (proto.Session, func()) {
			r := readyRow(managedID)
			r.Queued = []string{"wait"}
			return r, nil
		}},
		{"node active ask", func() (proto.Session, func()) {
			_, leave := n.activeAsks.enter(proto.Interaction{SessionID: managedID, Kind: proto.IxQuestion})
			return readyRow(managedID), func() { leave() }
		}},
		{"hub pending interaction via node.configure", func() (proto.Session, func()) {
			if _, err := nodeCall(t, n, "node.configure", map[string]any{"pending_interaction_sessions": []string{managedID}}); err != nil {
				t.Fatal(err)
			}
			return readyRow(managedID), func() {
				_, _ = nodeCall(t, n, "node.configure", map[string]any{"pending_interaction_sessions": []string{}})
			}
		}},
		{"state tasks running", func() (proto.Session, func()) {
			n.state.put(managedID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj"), Tasks: []string{"t1"}})
			return readyRow(managedID), func() {
				n.state.put(managedID, sessionState{Launcher: "agy", Cwd: filepath.Join(n.home, "proj")})
			}
		}},
		{"external terminal presence lock", func() (proto.Session, func()) {
			prev := agyPresenceLockProbe
			agyPresenceLockProbe = func(string) agyLockState { return agyLockHeld }
			return readyRow(managedID), func() { agyPresenceLockProbe = prev }
		}},
	}

	policy := n.agyIdleCompactionPolicy()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row, cleanup := tc.setup()
			if cleanup != nil {
				defer cleanup()
			}
			if _, ready := n.agyIdleReady(h, row, policy, clock.get()); ready {
				t.Fatalf("guard %q unexpectedly allowed compaction", tc.name)
			}
		})
	}

	// Custom thresholds and disable toggle via node.configure (preserving existing claude_dirs).
	n.setKnownClaudeDirs([]string{"/tmp/kept-claude-dir"})
	if _, err := nodeCall(t, n, "node.configure", map[string]any{
		"agy_idle_compaction": assistantwire.AgyIdleCompaction{Enabled: false, IdleMinutes: 5, ContextTokens: 150_000},
	}); err != nil {
		t.Fatal(err)
	}
	if n.agyIdleCompactionPolicy().Enabled {
		t.Fatal("expected policy to be disabled via node.configure")
	}
	if _, ready := n.agyIdleReady(h, readyRow(managedID), n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("disabled policy should block compaction")
	}
	if _, err := nodeCall(t, n, "node.configure", map[string]any{
		"agy_idle_compaction": assistantwire.AgyIdleCompaction{Enabled: true, IdleMinutes: 5, ContextTokens: 150_000},
	}); err != nil {
		t.Fatal(err)
	}
	r := readyRow(managedID)
	r.ContextTokens = 160_000
	r.UpdatedAt = clock.get()
	pushAgyRow(t, n, host, r)
	clock.set(clock.get().Add(5 * time.Minute))
	if _, ready := n.agyIdleReady(h, r, n.agyIdleCompactionPolicy(), clock.get()); !ready {
		t.Fatal("custom thresholds (5m / 150k) should allow compaction at 5m / 160k")
	}
}

func TestAgyTranscriptMapsCompactToDividerNotices(t *testing.T) {
	raw := strings.Join([]string{
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-11-27T01:00:00Z","content":"<USER_REQUEST>\nhi\n</USER_REQUEST>"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","source":"MODEL","status":"DONE","created_at":"2026-11-27T01:00:05Z","content":"hello","input_tokens":4000,"cache_read_tokens":246000,"output_tokens":120}`,
		`{"step_index":2,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-11-27T01:15:00Z","content":"<USER_REQUEST>\n/compact\n</USER_REQUEST>","client_msg_id":"cmp-1"}`,
		`{"step_index":3,"type":"CHECKPOINT","source":"SYSTEM","status":"DONE","created_at":"2026-11-27T01:15:10Z","content":"# Resuming","input_tokens":3800,"cache_read_tokens":250200,"output_tokens":6000}`,
		`{"step_index":4,"type":"USER_INPUT","source":"USER_EXPLICIT","status":"DONE","created_at":"2026-11-27T01:16:00Z","content":"<USER_REQUEST>\nnext\n</USER_REQUEST>"}`,
	}, "\n") + "\n"

	steps, _ := parseAgySteps([]byte(raw), agyTestID)
	h := mapAgySteps(steps, time.Now(), nil)
	if h.usage.turns != 2 {
		t.Fatalf("turns = %d, want 2 (/compact must not count as user turn)", h.usage.turns)
	}
	if len(h.items) != 5 {
		t.Fatalf("items count = %d, want 5: %+v", len(h.items), h.items)
	}
	if h.items[2].Role != "system" || h.items[2].Kind != proto.KindNotice || h.items[2].Text != transcript.CompactNotice || h.items[2].ClientMsgID != "cmp-1" {
		t.Fatalf("item[2] = %+v, want CompactNotice", h.items[2])
	}
	if h.items[3].Role != "system" || h.items[3].Kind != proto.KindNotice || h.items[3].Text != transcript.CompactedNotice || h.items[3].Compact == nil || h.items[3].Compact.PreTokens != 254_000 {
		t.Fatalf("item[3] = %+v, want CompactedNotice with pre_tokens=254000", h.items[3])
	}
	usage := extractAgyUsage(steps)
	if usage.turns != 2 || usage.compactions != 1 || usage.contextTokens != 0 {
		t.Fatalf("extractAgyUsage = %+v", usage)
	}
}

func TestAgyIdleAutoCompactMultiCompanionIsolationAndCleanup(t *testing.T) {
	oldCap := agyIdleCompactMaxConcurrent
	agyIdleCompactMaxConcurrent = 3
	t.Cleanup(func() { agyIdleCompactMaxConcurrent = oldCap })
	base := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)

	n, host1, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)

	hello2 := assistantRuntimeHello()
	hello2["runtime"], hello2["name"], hello2["launcher"], hello2["agent"] = "agy-alt", "Antigravity Alt", "agy-alt", "agy"
	host2 := dialRuntime(t, n, hello2)

	latchedID := runtimeCompactTestID
	failedID := "agy-66666666-7777-8888-9999-aaaaaaaaaaaa"
	otherID := "agy-77777777-8888-9999-aaaa-bbbbbbbbbbbb"
	failoverID := "agy-88888888-9999-aaaa-bbbb-cccccccccccc"
	cwd1 := filepath.Join(n.home, "proj1")
	cwd2 := filepath.Join(n.home, "proj2")
	n.state.put(latchedID, sessionState{Launcher: "agy", Cwd: cwd1})
	n.state.put(failedID, sessionState{Launcher: "agy", Cwd: cwd1})
	n.state.put(otherID, sessionState{Launcher: "agy-alt", Cwd: cwd2})
	n.state.put(failoverID, sessionState{Launcher: "agy", Cwd: cwd1})

	// Fail compaction for failedID so it enters the 5-minute retry cooldown.
	host1.mu.Lock()
	host1.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.send" {
			var p struct {
				SessionID string `json:"session_id"`
				Text      string `json:"text"`
			}
			_ = json.Unmarshal(params, &p)
			if p.SessionID == failedID && p.Text == "/compact" {
				return nil, &rpc.Error{Code: "compact_failed", Message: "temporary failure"}
			}
		}
		return map[string]any{"ok": true}, nil
	}
	host1.mu.Unlock()

	pushAgyRow(t, n, host1,
		proto.Session{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 2, ContextTokens: 240_000, UpdatedAt: base},
		proto.Session{SessionID: failedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base},
		proto.Session{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base},
	)
	pushAgyRow(t, n, host2,
		proto.Session{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 1, ContextTokens: 100_000, UpdatedAt: base},
	)

	// Sweep compacts latchedID and failoverID (both stay >= 200k -> latched) and fails failedID (5m cooldown).
	clock.set(base.Add(12 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	waitFor(t, "host1 received initial /compact attempts", func() bool {
		return len(host1.got("session.send")) == 3
	})
	// Wait until failedID's failed attempt has recorded its 5-minute cooldown in agyIdleSessions.
	waitFor(t, "failedID recorded failure cooldown", func() bool {
		n.agyIdleMu.Lock()
		defer n.agyIdleMu.Unlock()
		st := n.agyIdleSessions[failedID]
		return st != nil && !st.attempted && !st.lastAttempt.IsZero()
	})
	// Settle latchedID and failoverID without shrinking tokens (staying >= 200k keeps attempted=true).
	pushAgyRow(t, n, host1,
		proto.Session{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base},
		proto.Session{SessionID: failedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base},
		proto.Session{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusDone, Rev: 2, ContextTokens: 240_000, UpdatedAt: base},
	)
	waitFor(t, "host1 compactions settled", func() bool {
		return !n.runtimes.compacting(latchedID) && !n.runtimes.compacting(failoverID)
	})

	// 1. Companion 2 pushes its own list -> MUST NOT wipe companion 1's latch or 5-minute retry cooldown.
	//    Push a second serial runtime.sessions on host2 as an OnEvent barrier so the first observeAgySessions finishes.
	pushAgyRow(t, n, host2,
		proto.Session{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 2, ContextTokens: 120_000, UpdatedAt: clock.get()},
	)
	pushAgyRow(t, n, host2,
		proto.Session{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 3, ContextTokens: 120_000, UpdatedAt: clock.get()},
	)
	clock.set(base.Add(14 * time.Minute)) // only 2m since failedID failed (< 5m cooldown)
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	if _, ready := n.agyIdleReady(n.runtimes.ownerOf(latchedID), proto.Session{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base}, n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("companion 2 session list wiped companion 1 attempted latch")
	}
	if _, ready := n.agyIdleReady(n.runtimes.ownerOf(failedID), proto.Session{SessionID: failedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base}, n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("companion 2 session list wiped companion 1 failure cooldown")
	}
	if sends := host1.got("session.send"); len(sends) != 3 {
		t.Fatalf("companion 1 sessions unexpectedly re-compacted after companion 2 push: got %d sends", len(sends))
	}

	// 2. Non-owner companion 2 also listing host1's latchedID (offline with ContextTokens=0) must NOT reset host1's latch.
	if err := host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 4, ContextTokens: 120_000, UpdatedAt: clock.get()},
		{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusOffline, Rev: 1, ContextTokens: 0, UpdatedAt: clock.get()},
		{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusOffline, Rev: 2, ContextTokens: 240_000, UpdatedAt: base},
	}}); err != nil {
		t.Fatal(err)
	}
	// Serial OnEvent barrier on host2: observing rev 5 guarantees rev 4's observeAgySessions has completed.
	if err := host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 5, ContextTokens: 120_000, UpdatedAt: clock.get()},
		{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusOffline, Rev: 1, ContextTokens: 0, UpdatedAt: clock.get()},
		{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusOffline, Rev: 2, ContextTokens: 240_000, UpdatedAt: base},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host2 rev 5 observed", func() bool {
		got, _, ok := n.runtimes.rowOf(otherID)
		return ok && got.Rev == 5
	})
	if owner := n.runtimes.ownerOf(latchedID); owner == nil || owner.id() != "agy" {
		t.Fatalf("expected host1 (agy) to remain owner of latchedID, got %v", owner)
	}
	if _, ready := n.agyIdleReady(n.runtimes.ownerOf(latchedID), proto.Session{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base}, n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("non-owner companion reporting 0 tokens wiped owner attempted latch")
	}

	// 3. Ownership failover: host1 stops listing failoverID (and failedID), while host2 already lists failoverID.
	//    failedID (no longer owned by anyone) is pruned; failoverID (now owned by host2) keeps its attempted latch.
	pushAgyRow(t, n, host1,
		proto.Session{SessionID: latchedID, Cwd: cwd1, Status: proto.StatusDone, Rev: 4, ContextTokens: 240_000, UpdatedAt: base},
	)
	waitFor(t, "failoverID transferred to host2 and failedID pruned", func() bool {
		if n.runtimes.ownerOf(failoverID) == nil || n.runtimes.ownerOf(failoverID).id() != "agy-alt" || n.runtimes.ownerOf(failedID) != nil {
			return false
		}
		n.agyIdleMu.Lock()
		_, failedKept := n.agyIdleSessions[failedID]
		n.agyIdleMu.Unlock()
		return !failedKept
	})
	n.agyIdleMu.Lock()
	_, failoverKept := n.agyIdleSessions[failoverID]
	n.agyIdleMu.Unlock()
	if !failoverKept {
		t.Fatal("session handed off to another AGY companion lost its agyIdleSessions latch")
	}
	// host2 promotes failoverID to live (StatusDone, still >= 200k tokens): latch must still hold.
	if err := host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 6, ContextTokens: 120_000, UpdatedAt: clock.get()},
		{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base},
	}}); err != nil {
		t.Fatal(err)
	}
	// Serial OnEvent barrier on host2: observing rev 4 guarantees rev 3's observeAgySessions has completed.
	if err := host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: otherID, Cwd: cwd2, Status: proto.StatusDone, Rev: 7, ContextTokens: 120_000, UpdatedAt: clock.get()},
		{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusDone, Rev: 4, ContextTokens: 240_000, UpdatedAt: base},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failoverID live on host2", func() bool {
		got, _, ok := n.runtimes.rowOf(failoverID)
		return ok && got.Status == proto.StatusDone && got.Rev == 4
	})
	if _, ready := n.agyIdleReady(n.runtimes.ownerOf(failoverID), proto.Session{SessionID: failoverID, Cwd: cwd1, Status: proto.StatusDone, Rev: 4, ContextTokens: 240_000, UpdatedAt: base}, n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("failover session unexpectedly re-compacted after ownership transfer")
	}

	// 4. Disconnecting host2 cleans up host2's unowned sessions on the next sweep (no memory leak).
	_ = host2.conn.Close()
	waitFor(t, "host2 disconnected", func() bool {
		return n.runtimes.ownerOf(otherID) == nil && n.runtimes.ownerOf(failoverID) == nil
	})
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	n.agyIdleMu.Lock()
	_, otherKept := n.agyIdleSessions[otherID]
	_, failoverStillKept := n.agyIdleSessions[failoverID]
	_, latchedStillKept := n.agyIdleSessions[latchedID]
	n.agyIdleMu.Unlock()
	if otherKept || failoverStillKept {
		t.Fatalf("disconnected companion sessions leaked after sweep: other=%v failover=%v", otherKept, failoverStillKept)
	}
	if !latchedStillKept {
		t.Fatal("connected companion session was unexpectedly pruned during sweep")
	}
}

func TestAgyIdleAutoCompactFailoverWindowReconnectStaleSnapshotAndClockRollback(t *testing.T) {
	base := time.Date(2026, 11, 27, 15, 0, 0, 0, time.UTC)

	n, host1, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)
	cwd := filepath.Join(n.home, "proj")
	s1 := runtimeCompactTestID
	n.state.put(s1, sessionState{Launcher: "agy", Cwd: cwd})
	pushAgyRow(t, n, host1, proto.Session{
		SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 2, ContextTokens: 240_000, UpdatedAt: base,
	})
	// Serial OnEvent barrier on host1 so rev 2's observeAgySessions finishes before direct state writes.
	pushAgyRow(t, n, host1, proto.Session{
		SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base,
	})
	h1 := n.runtimes.ownerOf(s1)
	if h1 == nil {
		t.Fatal("expected host1 to own s1")
	}

	// 1. Clock rollback: safeIdleSince in the future relative to now clamps to now (NOT older row.UpdatedAt),
	//    so rollback never triggers an instant false compaction.
	n.agyIdleMu.Lock()
	n.agyIdleSessions[s1] = &agyIdleState{
		host:          h1,
		safeIdleSince: base.Add(30 * time.Minute),
		lastRowTime:   base,
		lastTokens:    240_000,
	}
	n.agyIdleMu.Unlock()
	clock.set(base.Add(25 * time.Minute))
	rowS1 := proto.Session{SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 3, ContextTokens: 240_000, UpdatedAt: base}
	if _, ready := n.agyIdleReady(h1, rowS1, n.agyIdleCompactionPolicy(), clock.get()); ready {
		t.Fatal("clock rollback fell back to older row.UpdatedAt and triggered instant compaction")
	}
	n.agyIdleMu.Lock()
	gotSince := n.agyIdleSessions[s1].safeIdleSince
	n.agyIdleMu.Unlock()
	if !gotSince.Equal(clock.get()) {
		t.Fatalf("safeIdleSince after rollback = %v, want %v", gotSince, clock.get())
	}

	// 2. Stale snapshot / non-owner companion must NOT revert st.host, send /compact, or poison cooldown.
	hello2 := assistantRuntimeHello()
	hello2["runtime"], hello2["name"], hello2["launcher"], hello2["agent"] = "agy-alt", "Antigravity Alt", "agy-alt", "agy"
	host2 := dialRuntime(t, n, hello2)
	pushAgyRow(t, n, host2, proto.Session{
		SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 4, ContextTokens: 240_000, UpdatedAt: base,
	})
	// Serial OnEvent barrier on host2 so rev 4's observeAgySessions finishes before direct state writes.
	pushAgyRow(t, n, host2, proto.Session{
		SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 5, ContextTokens: 240_000, UpdatedAt: base,
	})
	h2 := n.runtimes.ownerOf(s1)
	if h2 == nil || h2 == h1 || h2.id() != "agy-alt" {
		t.Fatalf("expected host2 (agy-alt) to take over s1, got %v", h2)
	}
	// Advance past idle threshold so h1's stale row would look ready if ownership weren't checked.
	clock.set(base.Add(40 * time.Minute))
	n.agyIdleMu.Lock()
	n.agyIdleSessions[s1] = &agyIdleState{
		host:          h2,
		safeIdleSince: base.Add(25 * time.Minute),
		lastRowTime:   base,
		lastTokens:    240_000,
	}
	n.agyIdleMu.Unlock()
	// Stale snapshot claiming h1 owns s1 must not revert st.host from h2 back to h1.
	n.pruneStaleAgyIdleStates([]agyRuntimeSnapshot{{host: h1, row: rowS1}})
	n.agyIdleMu.Lock()
	ownerAfterStaleSnap := n.agyIdleSessions[s1].host
	n.agyIdleMu.Unlock()
	if ownerAfterStaleSnap != h2 {
		t.Fatalf("stale snapshot reverted st.host to %v, want h2 (%v)", ownerAfterStaleSnap, h2)
	}
	// Non-owner h1 still has rowS1 in h1.sessions, so h1.row(s1) is true; runAgyIdleCompaction must refuse it.
	n.runAgyIdleCompaction(t.Context(), h1, s1, 240_000, 15*time.Minute)
	if sends := host1.got("session.send"); len(sends) != 0 {
		t.Fatalf("non-owner companion unexpectedly received /compact: %s", sends)
	}
	n.agyIdleMu.Lock()
	stAfterNonOwner := *n.agyIdleSessions[s1]
	n.agyIdleMu.Unlock()
	if stAfterNonOwner.host != h2 || stAfterNonOwner.attempted || !stAfterNonOwner.lastAttempt.IsZero() {
		t.Fatalf("non-owner runAgyIdleCompaction mutated state: %+v", stAfterNonOwner)
	}

	// 3. Reconnect while replacement companion has said hello (!sessionsListed) must preserve attempted latch.
	s2 := "agy-99999999-aaaa-bbbb-cccc-dddddddddddd"
	n.state.put(s2, sessionState{Launcher: "agy-alt", Cwd: cwd})
	hello3 := assistantRuntimeHello()
	hello3["runtime"], hello3["name"], hello3["launcher"], hello3["agent"], hello3["instance"] = "agy-reconn", "Antigravity Reconn", "agy-reconn", "agy", "inst-1"
	host3Old := dialRuntime(t, n, hello3)
	pushAgyRow(t, n, host3Old, proto.Session{
		SessionID: s2, Cwd: cwd, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000, UpdatedAt: base,
	})
	h3Old := n.runtimes.ownerOf(s2)
	n.markAgyIdleAttempt(h3Old, s2, clock.get())
	// Replacement connection says hello with the same runtime+instance -> supersedes host3Old before pushing sessions.
	host3New := dialRuntime(t, n, hello3)
	waitFor(t, "host3Old removed while host3New has not listed sessions yet", func() bool {
		return n.runtimes.ownerOf(s2) == nil && runtimeConnClosed(h3Old)
	})
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	n.agyIdleMu.Lock()
	stReconn := n.agyIdleSessions[s2]
	n.agyIdleMu.Unlock()
	if stReconn == nil || !stReconn.attempted {
		t.Fatalf("sweep wiped attempted latch while reconnecting companion had not listed sessions yet: %+v", stReconn)
	}
	// Once host3New pushes an empty list (sessionsListed=true) without s2, sweep prunes s2 cleanly.
	if err := host3New.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "host3New listed sessions", func() bool {
		return !n.runtimes.hasUnlistedAgyRuntime("agy-reconn")
	})
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	n.agyIdleMu.Lock()
	_, s2StillKept := n.agyIdleSessions[s2]
	n.agyIdleMu.Unlock()
	if s2StillKept {
		t.Fatal("stale session was not pruned after reconnecting companion listed sessions without it")
	}

	// 4. Failover window between peer.Close() and reg.remove(h): ownerOf is still non-nil -> latch must NOT be pruned.
	s3 := "agy-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	c1, c2 := net.Pipe()
	closedPeer := rpc.New("closed", c1)
	go func() { _ = closedPeer.Serve(t.Context(), c1) }()
	_ = c2.Close()
	<-closedPeer.Done()
	hClosed := n.runtimes.add(closedPeer)
	hClosed.mu.Lock()
	hClosed.hello = runtimeHello{Runtime: "agy-closing", Agent: agyAgent}
	hClosed.ready = true
	hClosed.mu.Unlock()
	n.runtimes.setSessions(hClosed, []proto.Session{{SessionID: s3, Cwd: cwd, Status: proto.StatusDone, Rev: 1, ContextTokens: 240_000}})
	n.markAgyIdleAttempt(hClosed, s3, clock.get())
	// Sweep while hClosed.peer is closed but reg.remove(hClosed) has not run yet (ownerOf(s3) == hClosed).
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	n.agyIdleMu.Lock()
	stClosing := n.agyIdleSessions[s3]
	n.agyIdleMu.Unlock()
	if stClosing == nil || !stClosing.attempted {
		t.Fatalf("sweep wiped latch during peer.Close()->reg.remove failover window: %+v", stClosing)
	}
	// Also verify closed hClosed cannot execute runAgyIdleCompaction.
	n.runAgyIdleCompaction(t.Context(), hClosed, s3, 240_000, 15*time.Minute)
	n.runtimes.remove(hClosed)
	// After remove(hClosed), s3 is truly unowned -> sweep prunes it.
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	n.agyIdleMu.Lock()
	_, s3StillKept := n.agyIdleSessions[s3]
	n.agyIdleMu.Unlock()
	if s3StillKept {
		t.Fatal("truly unowned session after remove(hClosed) was not pruned")
	}

	// 5. Nil-host orphan entry in agyIdleSessions is pruned consistently when unowned.
	orphanID := "agy-bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	n.agyIdleMu.Lock()
	n.agyIdleSessions[orphanID] = &agyIdleState{host: nil, attempted: true}
	n.agyIdleMu.Unlock()
	n.pruneStaleAgyIdleStates(nil)
	n.agyIdleMu.Lock()
	_, orphanKept := n.agyIdleSessions[orphanID]
	n.agyIdleMu.Unlock()
	if orphanKept {
		t.Fatal("nil-host orphan entry was not pruned by pruneStaleAgyIdleStates")
	}

	// 6. Unlock/relock TOCTOU generation & pointer guard prevents deleting updated or ABA-recreated state.
	toctouID := "agy-cccccccc-dddd-eeee-ffff-000000000000"
	orig := &agyIdleState{host: h1, gen: 1}
	n.agyIdleMu.Lock()
	n.agyIdleSessions[toctouID] = orig
	n.agyIdleMu.Unlock()
	staleCand := agyIdlePruneCandidate{id: toctouID, host: h1, state: orig, gen: orig.gen}
	// Same pointer mutated (gen bumped by markAgyIdleAttempt) must NOT be deleted by stale candidate.
	n.markAgyIdleAttempt(h1, toctouID, clock.get())
	n.deleteAgyIdleCandidates([]agyIdlePruneCandidate{staleCand})
	n.agyIdleMu.Lock()
	afterGenBump := n.agyIdleSessions[toctouID]
	n.agyIdleMu.Unlock()
	if afterGenBump != orig || !afterGenBump.attempted {
		t.Fatalf("stale prune candidate deleted gen-bumped entry: %+v", afterGenBump)
	}
	// ABA pointer replacement with the same host and same gen=1 must NOT be deleted by stale candidate.
	recreated := &agyIdleState{host: h1, gen: 1, attempted: true}
	n.agyIdleMu.Lock()
	n.agyIdleSessions[toctouID] = recreated
	n.agyIdleMu.Unlock()
	n.deleteAgyIdleCandidates([]agyIdlePruneCandidate{staleCand})
	n.agyIdleMu.Lock()
	afterABA := n.agyIdleSessions[toctouID]
	n.agyIdleMu.Unlock()
	if afterABA != recreated {
		t.Fatalf("stale prune candidate deleted ABA-replaced entry: %+v", afterABA)
	}
	// Exact pointer + gen match deletes cleanly.
	n.deleteAgyIdleCandidates([]agyIdlePruneCandidate{{id: toctouID, host: h1, state: recreated, gen: recreated.gen}})
	n.agyIdleMu.Lock()
	_, afterExact := n.agyIdleSessions[toctouID]
	n.agyIdleMu.Unlock()
	if afterExact {
		t.Fatal("exact candidate failed to delete entry")
	}

	// 7. Stale-owner failure callback uses attemptToken so:
	//    (a) even after h2 already updated st.host before h1's failure fires, h1's attempted=true is rolled back
	//        cleanly with zero cooldown (preventing permanent lockout); and
	//    (b) if h2 already started a new attempt (new attemptToken), h1's late failure is a strict no-op.
	n.agyIdleMu.Lock()
	n.agyIdleSessions[s1] = &agyIdleState{host: h1, gen: 1}
	n.agyIdleMu.Unlock()
	tok1, prevAtt1, prevLast1 := n.markAgyIdleAttempt(h1, s1, clock.get())
	// h2 (current owner) updates st.host to h2 before h1's failure callback runs.
	n.updateAgyIdleState(h2, proto.Session{SessionID: s1, Cwd: cwd, Status: proto.StatusDone, Rev: 5, ContextTokens: 240_000, UpdatedAt: base}, true, 200_000, clock.get())
	n.recordAgyIdleFailure(h1, s1, clock.get(), tok1, prevAtt1, prevLast1)
	n.agyIdleMu.Lock()
	stRestored := *n.agyIdleSessions[s1]
	n.agyIdleMu.Unlock()
	if stRestored.host != h2 || stRestored.attempted || !stRestored.lastAttempt.IsZero() || stRestored.attemptToken != 0 {
		t.Fatalf("stale-owner failure after host takeover did not roll back cleanly: %+v", stRestored)
	}

	tokOld, prevAttOld, prevLastOld := n.markAgyIdleAttempt(h1, s1, clock.get())
	tNew := clock.get().Add(time.Minute)
	tokNew, _, _ := n.markAgyIdleAttempt(h2, s1, tNew)
	n.recordAgyIdleFailure(h1, s1, tNew.Add(time.Second), tokOld, prevAttOld, prevLastOld)
	n.agyIdleMu.Lock()
	stNewAttempt := *n.agyIdleSessions[s1]
	n.agyIdleMu.Unlock()
	if stNewAttempt.host != h2 || !stNewAttempt.attempted || !stNewAttempt.lastAttempt.Equal(tNew) || stNewAttempt.attemptToken != tokNew {
		t.Fatalf("stale-owner late failure clobbered newer owner attempt: %+v", stNewAttempt)
	}
}

func TestAgyIdleAutoCompactSweepCapsConcurrency(t *testing.T) {
	base := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	n, host, _ := runtimeCompactFixture(t)
	clock := setAgyIdleClock(n, base)

	ids := []string{
		"agy-11111111-1111-1111-1111-111111111111",
		"agy-22222222-2222-2222-2222-222222222222",
		"agy-33333333-3333-3333-3333-333333333333",
		"agy-44444444-4444-4444-4444-444444444444",
		"agy-55555555-5555-5555-5555-555555555555",
	}
	rows := make([]proto.Session, 0, len(ids))
	for _, id := range ids {
		cwd := filepath.Join(n.home, "proj", id)
		n.state.put(id, sessionState{Launcher: "agy", Cwd: cwd})
		rows = append(rows, proto.Session{
			SessionID: id, Cwd: cwd, Status: proto.StatusDone, Rev: 1, ContextTokens: 250_000, UpdatedAt: base,
		})
	}
	pushAgyRow(t, n, host, rows...)

	// A single sweep with 5 ready sessions must cap concurrent auto-compactions at agyIdleCompactMaxConcurrent (2).
	clock.set(base.Add(15 * time.Minute))
	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	waitFor(t, "first batch of 2 compactions started", func() bool {
		return len(host.got("session.send")) == agyIdleCompactMaxConcurrent
	})
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	case <-timer.C:
	}
	if got := len(host.got("session.send")); got != agyIdleCompactMaxConcurrent {
		t.Fatalf("sweep started %d concurrent compactions, want cap %d", got, agyIdleCompactMaxConcurrent)
	}

	// Settle the first batch (drop tokens to 50k so they are no longer eligible) and verify the next sweep picks up 2 more.
	started := map[string]bool{}
	for _, raw := range host.got("session.send") {
		var p struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		started[p.SessionID] = true
	}
	for i := range rows {
		if started[rows[i].SessionID] {
			rows[i].Rev = 2
			rows[i].ContextTokens = 50_000
			rows[i].UpdatedAt = clock.get()
		}
	}
	pushAgyRow(t, n, host, rows...)
	waitFor(t, "first batch settled and slots released", func() bool {
		n.agyIdleMu.Lock()
		defer n.agyIdleMu.Unlock()
		return n.agyIdleInFlight == 0
	})

	n.sweepAgyIdleCompactions(t.Context(), clock.get())
	waitFor(t, "second batch of 2 compactions started", func() bool {
		return len(host.got("session.send")) == agyIdleCompactMaxConcurrent*2
	})
}
