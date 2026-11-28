package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// memA2AStore embeds memStore and adds an in-memory a2aStore implementation so A2A
// subagent launch, visibility decoupling, and restart-persistence tests always execute
// under `go test -race` even when PERSONA_TEST_DATABASE_URL is not configured.
type memA2AStore struct {
	memStore
	mu            sync.Mutex
	rows          map[string]db.A2ARow
	launchQueries int
}

func newMemA2AStore() *memA2AStore {
	return &memA2AStore{rows: map[string]db.A2ARow{}}
}

func (m *memA2AStore) A2ACreate(_ context.Context, r db.A2ARow) (*db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.rows {
		if old.Owner == r.Owner && old.Request == r.Request {
			cp := old
			return &cp, nil
		}
	}
	if r.Created.IsZero() {
		r.Created = time.Now().UTC()
	}
	if r.State == a2aSubmitted {
		for _, old := range m.rows {
			if old.TargetMachine == r.TargetMachine && old.TargetSession == r.TargetSession && !a2aTerminal(old.State) {
				r.State = a2aQueued
				break
			}
		}
	}
	r.Revision = 1
	m.rows[r.ID] = r
	cp := r
	return &cp, nil
}

func (m *memA2AStore) A2ARequest(_ context.Context, owner, request string) (*db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.Owner == owner && r.Request == request {
			cp := r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memA2AStore) A2AGet(_ context.Context, id string) (*db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, nil
	}
	cp := r
	return &cp, nil
}

func (m *memA2AStore) A2AList(_ context.Context, owner, machine, session, contextID, state, _ string, participant bool, _ *time.Time, limit int) ([]db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.A2ARow
	for _, r := range m.rows {
		ownerMatch := r.Owner == owner && (participant || machine == "" || (r.TargetMachine == machine && r.TargetSession == session))
		targetMatch := participant && r.TargetMachine == machine && r.TargetSession == session
		if !ownerMatch && !targetMatch {
			continue
		}
		if contextID != "" && r.Context != contextID {
			continue
		}
		if state != "" && r.State != state && !(state == a2aSubmitted && r.State == a2aQueued) {
			continue
		}
		out = append(out, r)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memA2AStore) A2ACount(ctx context.Context, owner, machine, session, contextID, state string, after *time.Time) (int, error) {
	rows, err := m.A2AList(ctx, owner, machine, session, contextID, state, "", false, after, 0)
	return len(rows), err
}

func (m *memA2AStore) A2AUpdate(_ context.Context, r db.A2ARow, _ time.Time, _ *time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.rows[r.ID]
	if !ok || old.Revision != r.Revision {
		return false, nil
	}
	r.Revision++
	m.rows[r.ID] = r
	return true, nil
}

func (m *memA2AStore) A2AClaim(_ context.Context, limit int) ([]db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.A2ARow
	for _, r := range m.rows {
		if !a2aTerminal(r.State) && r.State != a2aQueued {
			out = append(out, r)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (m *memA2AStore) A2AReady(_ context.Context, _, _ string) error { return nil }

func (m *memA2AStore) A2ADispatches(_ context.Context, owner, machine, session string, since time.Time) ([]db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.A2ARow
	for _, r := range m.rows {
		var body a2aBody
		if json.Unmarshal(r.Body, &body) != nil || body.Launch == nil {
			continue
		}
		isTarget := r.TargetMachine == machine && r.TargetSession == session
		if r.Owner != owner && !isTarget {
			continue
		}
		if isTarget || !r.Created.Before(since) || !a2aTerminal(r.State) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memA2AStore) A2ALaunchesForTargets(_ context.Context, machine string, sessions []string) ([]db.A2ARow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.launchQueries++
	want := make(map[string]bool, len(sessions))
	for _, sid := range sessions {
		want[sid] = true
	}
	var out []db.A2ARow
	for _, r := range m.rows {
		if r.TargetMachine != machine || !want[r.TargetSession] || r.SourceSession == "" {
			continue
		}
		var body a2aBody
		if json.Unmarshal(r.Body, &body) != nil || body.Launch == nil {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

func memA2AFixture(t *testing.T) (*Service, *a2aTestHub, *memA2AStore, a2aCaller, a2aRef) {
	t.Helper()
	st := newMemA2AStore()
	f := newFakeHub()
	h := &a2aTestHub{fakeHub: f}
	f.hello.Agents.A2A = assistantwire.A2AConfig{} // default scope: Machines == nil, Launch == nil
	f.hello.Agents.Dispatch.Enabled = nil          // dispatch switch off by default
	from, peer := a2aRef{"mac", "caller-0001"}, a2aRef{"box", "runtime.peer-0001"}
	f.sessions[from.Machine] = []proto.Session{{SessionID: from.Session, Status: proto.StatusWorking, Agent: "claude", PermissionMode: "acceptEdits"}}
	f.sessions[peer.Machine] = []proto.Session{{SessionID: peer.Session, Status: proto.StatusIdle, Agent: "pi",
		SourceKind: "runtime", Capabilities: &proto.SessionCapabilities{Send: true, Read: true}}}
	s := New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)})
	return s, h, st, a2aSessionCaller(from), peer
}

func processMemA2A(t *testing.T, s *Service, st *memA2AStore, cfg assistantwire.A2AConfig, id string) *a2aTask {
	t.Helper()
	ctx := t.Context()
	task, err := decodeA2A(st.A2AGet(ctx, id))
	if err != nil || task == nil {
		t.Fatalf("get task %s: %v %v", id, task, err)
	}
	if err := s.a2aProcess(ctx, st, cfg, task); err != nil {
		t.Fatal(err)
	}
	task, err = decodeA2A(st.A2AGet(ctx, id))
	if err != nil || task == nil {
		t.Fatalf("get processed task %s: %v %v", id, task, err)
	}
	return task
}

// Verifies default-enabled cross-machine & same-machine cross-harness A2A subagent launches,
// config opt-outs, permission_mode capping, durable subagent provenance across restarts,
// peer preservation, and a2aDiscoverable vs a2aTaskEndpointAllowed decoupling without Postgres.
func TestA2ADefaultSubagentFlowWithoutPostgres(t *testing.T) {
	s, h, st, parent, peer := memA2AFixture(t)
	ctx := t.Context()
	h.fakeModel(t)
	h.startReply = func(args map[string]any, row *proto.Session) *proto.StartInstead {
		row.SessionID = args["session_id"].(string)
		return nil
	}

	// 1. Default cross-machine launch works without agents.dispatch.enabled or explicit machines list.
	rawCross, _ := json.Marshal(map[string]any{
		"machine_id": "box", "cwd": "/code/app", "launcher": "codex",
		"message_id": "m-cross-1", "text": "Run Linux checks.",
	})
	resCross := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawCross, false)
	if resCross.IsError {
		t.Fatalf("default cross-machine launch failed: %s", resultText(resCross))
	}
	var crossView a2aTaskView
	if err := json.Unmarshal([]byte(resultText(resCross)), &crossView); err != nil {
		t.Fatal(err)
	}
	cfg := s.a2aConfig(ctx)
	processMemA2A(t, s, st, cfg, crossView.ID)
	var tk *a2aTask
	waitFor(t, "cross launch finished", func() bool {
		var err error
		tk, err = decodeA2A(st.A2AGet(ctx, crossView.ID))
		return err == nil && tk != nil && tk.Launch != nil && !tk.Launch.Pending
	})
	if !tk.Launch.Subagent || len(h.starts) != 1 || h.starts[0]["permission_mode"] != "plan" {
		t.Fatalf("cross launch = %+v, starts = %+v", tk.Launch, h.starts)
	}

	// 2. Default same-machine cross-harness launch (mac Claude -> mac Codex) with live bypass attestation
	// allows acceptEdits; cached session mode alone without live attestation must fail closed.
	rawSame, _ := json.Marshal(map[string]any{
		"machine_id": parent.Ref.Machine, "cwd": "/code/app", "launcher": "codex",
		"permission_mode": "acceptEdits",
		"message_id":      "m-same-1", "text": "Review patch with Codex.",
	})
	if unattested := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawSame, false); !unattested.IsError {
		t.Fatalf("acceptEdits without live source-node attestation must fail closed: %s", resultText(unattested))
	}
	bypassCtx := devicewire.WithSourcePermission(ctx, testSourcePermission(parent.Ref.Session, "a2a_send", rawSame, "bypass"))
	resSame := s.mcpCall(bypassCtx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawSame, false)
	if resSame.IsError {
		t.Fatalf("same-machine cross-harness A2A launch with bypass attestation failed: %s", resultText(resSame))
	}
	var sameView a2aTaskView
	if err := json.Unmarshal([]byte(resultText(resSame)), &sameView); err != nil {
		t.Fatal(err)
	}
	processMemA2A(t, s, st, cfg, sameView.ID)
	waitFor(t, "same-machine launch finished", func() bool {
		t2, err := decodeA2A(st.A2AGet(ctx, sameView.ID))
		return err == nil && t2 != nil && t2.Launch != nil && !t2.Launch.Pending
	})
	if len(h.starts) != 2 || h.starts[1]["permission_mode"] != "acceptEdits" {
		t.Fatalf("same-machine launch permission_mode not honored: %+v", h.starts)
	}

	// 3. Permission policy: every proto.PermissionModes member is explicitly ranked; "auto" is never
	// treated as "plan", unknown modes fail closed, and prompting attestation caps at "default".
	for _, m := range proto.PermissionModes {
		rank, ok := a2aPermissionRank(m)
		if !ok {
			t.Fatalf("proto.PermissionModes entry %q missing from a2aPermissionRank", m)
		}
		if (m == "plan") != (rank == 0) {
			t.Fatalf("mode %q rank=%d, only plan may have rank 0", m, rank)
		}
	}
	if _, ok := a2aPermissionRank("unknown_mode"); ok {
		t.Fatal("unknown permission mode must fail closed")
	}
	// Prompting attestation allows "default" but rejects "acceptEdits", "auto", and "bypassPermissions".
	for _, mode := range []string{"acceptEdits", "auto", "bypassPermissions", "dontAsk"} {
		rawMode, _ := json.Marshal(map[string]any{
			"machine_id": "box", "cwd": "/code/app", "permission_mode": mode,
			"message_id": "m-mode-" + mode, "text": "Check.",
		})
		promptCtx := devicewire.WithSourcePermission(ctx, testSourcePermission(parent.Ref.Session, "a2a_send", rawMode, "prompting"))
		if r := s.mcpCall(promptCtx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawMode, false); !r.IsError {
			t.Fatalf("prompting attestation allowed %q: %s", mode, resultText(r))
		}
		// Even with cached bypassPermissions on the session row, missing live attestation rejects "auto" and above.
		h.sessions[parent.Ref.Machine][0].PermissionMode = "bypassPermissions"
		if r := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawMode, false); !r.IsError {
			t.Fatalf("cached bypass session without live attestation allowed %q: %s", mode, resultText(r))
		}
	}
	// Idempotent retry of m-same-1 MUST succeed even without live bypass attestation AND even if Launch=false!
	offToggle := false
	h.hello.Agents.A2A = assistantwire.A2AConfig{Launch: &offToggle}
	retrySame := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawSame, false)
	if retrySame.IsError {
		t.Fatalf("idempotent retry of m-same-1 was blocked by changed permission/launch policy: %s", resultText(retrySame))
	}
	var retryView a2aTaskView
	if err := json.Unmarshal([]byte(resultText(retrySame)), &retryView); err != nil || retryView.ID != sameView.ID {
		t.Fatalf("retry returned wrong task: %+v vs %+v (%v)", retryView, sameView, err)
	}
	h.hello.Agents.A2A = assistantwire.A2AConfig{}

	// 4. Ordinary A2A message to an existing independent peer NEVER marks it as a subagent.
	rawPeer, _ := json.Marshal(map[string]any{
		"machine_id": peer.Machine, "session_id": peer.Session,
		"message_id": "m-peer-1", "text": "Please review.",
	})
	if r := s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawPeer, false); r.IsError {
		t.Fatalf("peer send failed: %s", resultText(r))
	}

	// 5. Simulated restart: even a 90-day-old A2A subagent still listed recovers full provenance
	// via A2ALaunchesForTargets (no 30-day or 2000-row truncation).
	st.mu.Lock()
	oldRow := st.rows[tk.ID]
	oldRow.Created = time.Now().Add(-90 * 24 * time.Hour).UTC()
	st.rows[tk.ID] = oldRow
	st.mu.Unlock()
	restarted := New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)})
	subRef := tk.target()
	boxRows := []proto.Session{
		{MachineID: peer.Machine, SessionID: peer.Session, Status: proto.StatusIdle, Agent: "pi",
			SourceKind: "runtime", Capabilities: &proto.SessionCapabilities{Send: true, Read: true}},
		{MachineID: subRef.Machine, SessionID: subRef.Session, Status: proto.StatusIdle, Agent: "codex",
			Capabilities: &proto.SessionCapabilities{Send: true, Read: true}},
	}
	restarted.decorateSubagents(ctx, "box", boxRows)
	if boxRows[0].IsSubagent || boxRows[0].A2ATaskID() != "" || boxRows[0].ParentSessionID != "" {
		t.Fatalf("ordinary peer was marked as subagent: %+v %+v", boxRows[0], boxRows[0].Hierarchy)
	}
	if sub := boxRows[1]; !sub.IsSubagent || sub.ParentSessionID != parent.Ref.Session ||
		sub.ParentMachineID() != parent.Ref.Machine || sub.A2ATaskID() != tk.ID || sub.SubagentOrigin() != proto.SubagentOriginA2A {
		t.Fatalf("90-day-old A2A subagent lost provenance after restart: %+v %+v", sub, sub.Hierarchy)
	}
	// Same-machine subagent omits parent_machine_id.
	sameRows := []proto.Session{{MachineID: parent.Ref.Machine, SessionID: sameView.Target.Session, Status: proto.StatusIdle, Agent: "codex"}}
	restarted.decorateSubagents(ctx, parent.Ref.Machine, sameRows)
	if !sameRows[0].IsSubagent || sameRows[0].ParentSessionID != parent.Ref.Session || sameRows[0].ParentMachineID() != "" ||
		sameRows[0].SubagentOrigin() != proto.SubagentOriginA2A {
		t.Fatalf("same-machine subagent provenance wrong: %+v %+v", sameRows[0], sameRows[0].Hierarchy)
	}

	// Put decorated rows back into fakeHub so a2aDiscover / a2aDeliver see IsSubagent=true.
	h.mu.Lock()
	h.sessions["box"] = boxRows
	h.mu.Unlock()

	// 6. Subagent excluded from peer discovery & unsolicited peer send, YET allowed on its own task!
	disc := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_sessions", json.RawMessage(`{}`), false)
	if disc.IsError || strings.Contains(resultText(disc), subRef.Session) || !strings.Contains(resultText(disc), peer.Session) {
		t.Fatalf("a2a_sessions leaked subagent or missed peer: %s", resultText(disc))
	}
	if _, err := restarted.a2aStart(ctx, st, cfg, parent, subRef, "m-unsolicited", "", "Direct ping.", false, nil); !errors.Is(err, errA2AUnavailable) {
		t.Fatalf("unsolicited peer send to subagent got %v, want errA2AUnavailable", err)
	}
	tools, err := restarted.sessionMCPToolsChecked(ctx, subRef.Machine, subRef.Session, false, false)
	if err != nil || !toolListed(tools, "a2a_report") || !toolListed(tools, "a2a_get") {
		t.Fatalf("A2A subagent missing A2A tools: err=%v", err)
	}
	repArgs, _ := json.Marshal(map[string]any{"task_id": tk.ID, "message_id": "r-ask", "state": a2aInput, "text": "Which target?"})
	if r := restarted.mcpCall(ctx, subRef.Machine, subRef.Session, "a2a_report", repArgs, false); r.IsError {
		t.Fatalf("A2A subagent a2a_report failed: %s", resultText(r))
	}
	ansArgs, _ := json.Marshal(map[string]any{"task_id": tk.ID, "message_id": "p-ans", "text": "Use integration."})
	if r := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", ansArgs, false); r.IsError {
		t.Fatalf("parent answer to subagent failed: %s", resultText(r))
	}
	tk = processMemA2A(t, restarted, st, cfg, tk.ID)
	if tk.State != a2aWorking || len(h.sentCalls()) == 0 || !strings.Contains(string(h.sentCalls()[len(h.sentCalls())-1].Params), "Use integration.") {
		t.Fatalf("follow-up was not delivered to IsSubagent=true target: state=%s sends=%+v", tk.State, h.sentCalls())
	}
	// Depth-1 guard still prevents subagent from launching a grandchild.
	gcArgs, _ := json.Marshal(map[string]any{"machine_id": "mac", "cwd": "/code/app", "message_id": "m-gc", "text": "Grandchild."})
	if r := restarted.mcpCall(ctx, subRef.Machine, subRef.Session, "a2a_send", gcArgs, false); !r.IsError || !strings.Contains(resultText(r), "does not dispatch") {
		t.Fatalf("subagent bypassed depth-1 guard: %s", resultText(r))
	}
	doneArgs, _ := json.Marshal(map[string]any{"task_id": tk.ID, "message_id": "r-done", "state": a2aComplete, "text": "All green."})
	if r := restarted.mcpCall(ctx, subRef.Machine, subRef.Session, "a2a_report", doneArgs, false); r.IsError {
		t.Fatalf("subagent completion report failed: %s", resultText(r))
	}

	// 7. Offline queued launch fails IMMEDIATELY when agents.a2a.launch=false (even while target is still
	// offline), without affecting already-attempted launches or ordinary peer messages.
	h.mu.Lock()
	h.offline = map[string]bool{"box": true}
	h.mu.Unlock()
	rawQueued, _ := json.Marshal(map[string]any{"machine_id": "box", "cwd": "/code/app", "message_id": "m-queued-offline", "text": "Run when box returns."})
	resQueued := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawQueued, false)
	if resQueued.IsError {
		t.Fatalf("offline queued launch failed to enqueue: %s", resultText(resQueued))
	}
	var queuedView a2aTaskView
	if err := json.Unmarshal([]byte(resultText(resQueued)), &queuedView); err != nil {
		t.Fatal(err)
	}
	// Also enqueue a second launch and mark it already attempted (a2aSending) to verify launch=false does not clobber it.
	rawInflight, _ := json.Marshal(map[string]any{"machine_id": "box", "cwd": "/code/app", "message_id": "m-inflight-offline", "text": "Already sent."})
	resInflight := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawInflight, false)
	if resInflight.IsError {
		t.Fatalf("inflight launch enqueue failed: %s", resultText(resInflight))
	}
	var inflightView a2aTaskView
	if err := json.Unmarshal([]byte(resultText(resInflight)), &inflightView); err != nil {
		t.Fatal(err)
	}
	inflightTask, err := decodeA2A(st.A2AGet(ctx, inflightView.ID))
	if err != nil || inflightTask == nil {
		t.Fatalf("load inflight task: %v", err)
	}
	inflightTask.Events[0].Delivery = a2aSending
	inflightTask.Events[0].Attempted = time.Now().UTC()
	if err := restarted.a2aSave(ctx, st, inflightTask); err != nil {
		t.Fatal(err)
	}

	startsBefore := len(h.starts)
	off := false
	h.hello.Agents.A2A = assistantwire.A2AConfig{Launch: &off}
	// Target machine "box" is STILL OFFLINE: never-attempted queued launch must fail right away.
	queuedDone := processMemA2A(t, restarted, st, restarted.a2aConfig(ctx), queuedView.ID)
	if queuedDone.State != a2aFailed || queuedDone.Launch.Pending || len(h.starts) != startsBefore {
		t.Fatalf("disabled offline queued launch did not fail immediately before online gate: state=%s pending=%v starts=%d (was %d)",
			queuedDone.State, queuedDone.Launch.Pending, len(h.starts), startsBefore)
	}
	// Already-attempted launch stays pending reconciliation while offline.
	inflightAfter := processMemA2A(t, restarted, st, restarted.a2aConfig(ctx), inflightView.ID)
	if inflightAfter.State == a2aFailed || !inflightAfter.Launch.Pending {
		t.Fatalf("already-attempted offline launch was prematurely failed by launch=false: state=%s pending=%v",
			inflightAfter.State, inflightAfter.Launch.Pending)
	}
	h.mu.Lock()
	h.offline = nil
	h.mu.Unlock()

	// 8. Config opt-outs: Launch=false disables new launches while preserving peer discovery; Machines=[] disables all.
	rawDisabled, _ := json.Marshal(map[string]any{"machine_id": "box", "cwd": "/code/app", "message_id": "m-off", "text": "No."})
	if r := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawDisabled, false); !r.IsError {
		t.Fatalf("Launch=false allowed launch: %s", resultText(r))
	}
	if r := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_sessions", json.RawMessage(`{}`), false); r.IsError {
		t.Fatalf("Launch=false broke discovery: %s", resultText(r))
	}
	h.hello.Agents.A2A = assistantwire.A2AConfig{Machines: []string{}}
	if r := restarted.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, "a2a_send", rawDisabled, false); !r.IsError {
		t.Fatalf("Machines=[] allowed launch: %s", resultText(r))
	}
	_ = slices.Contains(proto.PermissionModes, "auto")
}

// End-to-end manager mcp.call envelope verification for a2a_send live SourcePermission attestation:
// bypass permits acceptEdits, prompting permits default and rejects acceptEdits, missing/mismatched fails closed.
func TestA2ASendNodeRequestLiveSourcePermission(t *testing.T) {
	ctx := context.Background()
	s, h, _, parent, _ := memA2AFixture(t)
	d := newFleetDevs()
	d.add("mac", "Mac")
	d.add("box", "Box")
	d.setSessions("mac", h.sessions["mac"])
	d.setSessions("box", h.sessions["box"])
	s.devs = d
	call := func(args map[string]any, proof proto.SourcePermission) proto.ToolResult {
		t.Helper()
		rawArgs, _ := json.Marshal(args)
		env := map[string]any{
			"name":       "a2a_send",
			"session_id": parent.Ref.Session,
			"args":       json.RawMessage(rawArgs),
		}
		if proof.Valid() {
			env["source_permission"] = proof
		}
		rawEnv, _ := json.Marshal(env)
		out, handled, err := s.NodeRequest(ctx, parent.Ref.Machine, "mcp.call", rawEnv)
		if err != nil || !handled {
			t.Fatalf("NodeRequest mcp.call: handled=%v err=%v", handled, err)
		}
		return out.(proto.ToolResult)
	}

	argsEdit := map[string]any{"machine_id": "box", "cwd": "/code/app", "permission_mode": "acceptEdits", "message_id": "m-nr-1", "text": "Edit."}
	rawEdit, _ := json.Marshal(argsEdit)
	// Missing proof fails closed.
	if res := call(argsEdit, proto.SourcePermission{}); !res.IsError {
		t.Fatalf("missing attestation allowed acceptEdits: %s", resultText(res))
	}
	// Prompting proof rejects acceptEdits...
	if res := call(argsEdit, testSourcePermission(parent.Ref.Session, "a2a_send", rawEdit, "prompting")); !res.IsError {
		t.Fatalf("prompting attestation allowed acceptEdits: %s", resultText(res))
	}
	// ...and accepts default.
	argsDef := map[string]any{"machine_id": "box", "cwd": "/code/app", "permission_mode": "default", "message_id": "m-nr-2", "text": "Check."}
	rawDef, _ := json.Marshal(argsDef)
	if res := call(argsDef, testSourcePermission(parent.Ref.Session, "a2a_send", rawDef, "prompting")); res.IsError {
		t.Fatalf("prompting attestation rejected default: %s", resultText(res))
	}
	// Bypass proof accepts acceptEdits.
	if res := call(argsEdit, testSourcePermission(parent.Ref.Session, "a2a_send", rawEdit, "bypass")); res.IsError {
		t.Fatalf("bypass attestation rejected acceptEdits: %s", resultText(res))
	}
	// Mismatched digest fails closed.
	wrongDigest := testSourcePermission(parent.Ref.Session, "a2a_send", json.RawMessage(`{}`), "bypass")
	argsEdit2 := map[string]any{"machine_id": "box", "cwd": "/code/app", "permission_mode": "acceptEdits", "message_id": "m-nr-3", "text": "Edit2."}
	if res := call(argsEdit2, wrongDigest); !res.IsError {
		t.Fatalf("mismatched digest attestation allowed acceptEdits: %s", resultText(res))
	}
}

// Warm refreshes over an unchanged live + archived session list must make zero A2ALaunchesForTargets
// queries, and pruning drops only removed sessions from that machine's bucket without affecting other machines.
func TestSubagentCheckedWarmRefreshZeroQueriesAndPerMachinePrune(t *testing.T) {
	ctx := context.Background()
	s, _, st, _, _ := memA2AFixture(t)
	// Seed one known A2A subagent on mac.
	body, _ := json.Marshal(a2aBody{Launch: &a2aLaunch{CWD: "/code/app", Subagent: true}})
	if _, err := st.A2ACreate(ctx, db.A2ARow{
		ID: "task-warm-1", Owner: "mac/lead", Request: "req-warm-1", Context: "ctx-warm-1",
		SourceMachine: "mac", SourceSession: "lead", TargetMachine: "mac", TargetSession: "sub-1",
		State: a2aWorking, Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	buildComplete := func(liveIDs, archIDs []string) []proto.Session {
		live := make([]proto.Session, 0, len(liveIDs))
		for _, id := range liveIDs {
			live = append(live, proto.Session{MachineID: "mac", SessionID: id, Status: proto.StatusIdle, Cwd: "/code/app", Agent: "claude"})
		}
		list := s.decorateInventory("mac", live, true)
		for _, id := range archIDs {
			list = append(list, proto.Session{MachineID: "mac", SessionID: id, Status: proto.StatusDone, Archived: true, Cwd: "/code/app", Agent: "claude"})
		}
		s.foldInventory("mac", list)
		return list
	}

	// Prime another machine's bucket to verify per-machine pruning never touches it.
	s.decorateSubagents(ctx, "box", []proto.Session{{MachineID: "box", SessionID: "box-live-1", Status: proto.StatusIdle}})

	// Pass 1: cold pass queries mac live + archived rows (at most 2 queries: live then archived).
	rows1 := buildComplete([]string{"live-1", "live-2", "sub-1"}, []string{"arch-1"})
	if !rows1[2].IsSubagent || rows1[2].A2ATaskID() != "task-warm-1" {
		t.Fatalf("sub-1 not decorated on cold pass: %+v %+v", rows1[2], rows1[2].Hierarchy)
	}
	st.mu.Lock()
	afterCold := st.launchQueries
	st.mu.Unlock()
	if afterCold == 0 {
		t.Fatal("expected cold pass to query A2ALaunchesForTargets")
	}

	// Pass 2: unchanged warm refresh (live + archived) MUST perform ZERO A2ALaunchesForTargets queries.
	_ = buildComplete([]string{"live-1", "live-2", "sub-1"}, []string{"arch-1"})
	st.mu.Lock()
	afterWarm := st.launchQueries
	st.mu.Unlock()
	if afterWarm != afterCold {
		t.Fatalf("warm refresh queried A2ALaunchesForTargets %d extra times (cold=%d warm=%d); archived negative cache was pruned prematurely",
			afterWarm-afterCold, afterCold, afterWarm)
	}

	// Pass 3: removing live-2 from mac prunes only mac/live-2 while keeping mac/arch-1, mac/sub-1, and box/box-live-1.
	_ = buildComplete([]string{"live-1", "sub-1"}, []string{"arch-1"})
	s.fleet.subagentsMu.RLock()
	_, macLive1 := s.fleet.subagentsChecked["mac"]["live-1"]
	_, macLive2 := s.fleet.subagentsChecked["mac"]["live-2"]
	_, macArch1 := s.fleet.subagentsChecked["mac"]["arch-1"]
	_, macSub1 := s.fleet.subagentsChecked["mac"]["sub-1"]
	_, boxLive1 := s.fleet.subagentsChecked["box"]["box-live-1"]
	s.fleet.subagentsMu.RUnlock()
	if !macLive1 || macLive2 || !macArch1 || !macSub1 || !boxLive1 {
		t.Fatalf("per-machine prune state wrong: macLive1=%v macLive2=%v macArch1=%v macSub1=%v boxLive1=%v",
			macLive1, macLive2, macArch1, macSub1, boxLive1)
	}
}

type legacySpawnTodoStore struct {
	*memA2AStore
	todoStore
	agentsMu     sync.Mutex
	todos        []db.TodoRow
	agents       []db.TodoAgentRow
	agentsErr    error
	agentsCalls  int
	listTodosHit int
}

func (st *legacySpawnTodoStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	st.agentsMu.Lock()
	defer st.agentsMu.Unlock()
	st.listTodosHit++
	return slices.Clone(st.todos), nil
}

func (st *legacySpawnTodoStore) ListAllTodoSpaces(context.Context) ([]db.TodoSpaceRow, error) {
	return nil, nil
}

func (st *legacySpawnTodoStore) ListTodoThreads(context.Context) (map[string]db.TodoThread, error) {
	return map[string]db.TodoThread{}, nil
}

func (st *legacySpawnTodoStore) ListTodoSources(context.Context) (map[string]db.TodoSource, error) {
	return map[string]db.TodoSource{}, nil
}

func (st *legacySpawnTodoStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) {
	st.agentsMu.Lock()
	defer st.agentsMu.Unlock()
	st.agentsCalls++
	if st.agentsErr != nil {
		return nil, st.agentsErr
	}
	return slices.Clone(st.agents), nil
}

// Legacy task_spawn subagent provenance restores directly from todo_agents (or already-loaded
// in-memory todos) without invoking ensureTodosLoaded, keeping decorateInventory free of todo
// startup side effects and call-graph cycles (#14377).
func TestLegacySpawnedSubagentLoadsWithoutInitializingTodos(t *testing.T) {
	ctx := context.Background()
	st := &legacySpawnTodoStore{
		memA2AStore: newMemA2AStore(),
		todos:       []db.TodoRow{{ID: "t1", Title: "Parent task"}},
		agents: []db.TodoAgentRow{
			{TodoID: "t1", MachineID: "box", SessionID: "spawn-cross", Source: sourceSpawned, Via: "mac/lead"},
			{TodoID: "t1", MachineID: "mac", SessionID: "spawn-local", Source: sourceSpawned, Via: "mac/lead"},
			{TodoID: "t1", MachineID: "box", SessionID: "manual-peer", Source: "manual", Via: "mac/lead"},
			{TodoID: "t1", MachineID: "box", SessionID: "bad-via", Source: sourceSpawned, Via: "no-slash"},
		},
		agentsErr: errors.New("transient todo_agents read error"),
	}
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return st }, Logf: testLogf(t)})

	// 1. Transient read failure retries on the next call and does not load full todos.
	rows := []proto.Session{
		{MachineID: "box", SessionID: "spawn-cross", Status: proto.StatusWorking},
		{MachineID: "box", SessionID: "manual-peer", Status: proto.StatusIdle},
		{MachineID: "box", SessionID: "bad-via", Status: proto.StatusIdle},
	}
	s.decorateSubagents(ctx, "box", rows)
	if rows[0].IsSubagent {
		t.Fatalf("expected transient error to leave link unloaded, got %+v", rows[0].Hierarchy)
	}
	s.fleet.subagentsMu.RLock()
	loadedAfterErr := s.fleet.subagentsLegacyLoaded
	s.fleet.subagentsMu.RUnlock()
	if loadedAfterErr {
		t.Fatal("subagentsLegacyLoaded became true despite ListAllTodoAgents error")
	}

	// 2. Next call recovers both cross-machine and same-machine task_spawn provenance directly
	// without calling ListTodos or marking s.todos.loaded.
	st.agentsMu.Lock()
	st.agentsErr = nil
	st.agentsMu.Unlock()
	s.decorateSubagents(ctx, "box", rows)
	if !rows[0].IsSubagent || rows[0].ParentSessionID != "lead" || rows[0].ParentMachineID() != "mac" ||
		rows[0].SubagentOrigin() != proto.SubagentOriginSpawned {
		t.Fatalf("cross-machine task_spawn provenance wrong: %+v %+v", rows[0], rows[0].Hierarchy)
	}
	if rows[1].IsSubagent || rows[2].IsSubagent {
		t.Fatalf("non-spawned or malformed via row tagged: manual=%+v bad=%+v", rows[1].Hierarchy, rows[2].Hierarchy)
	}
	localRows := []proto.Session{{MachineID: "mac", SessionID: "spawn-local", Status: proto.StatusWorking}}
	s.decorateSubagents(ctx, "mac", localRows)
	if !localRows[0].IsSubagent || localRows[0].ParentSessionID != "lead" || localRows[0].ParentMachineID() != "" ||
		localRows[0].SubagentOrigin() != proto.SubagentOriginSpawned {
		t.Fatalf("same-machine task_spawn provenance wrong: %+v %+v", localRows[0], localRows[0].Hierarchy)
	}
	s.todos.mu.RLock()
	todosLoaded := s.todos.loaded
	s.todos.mu.RUnlock()
	st.agentsMu.Lock()
	agentsCalls, listTodosHit := st.agentsCalls, st.listTodosHit
	st.agentsMu.Unlock()
	if todosLoaded || listTodosHit != 0 {
		t.Fatalf("decorateSubagents initialized todos: todosLoaded=%v listTodosHit=%d", todosLoaded, listTodosHit)
	}
	if agentsCalls != 2 {
		t.Fatalf("ListAllTodoAgents calls = %d, want 2 (1 failed + 1 cached success)", agentsCalls)
	}

	// 3. When todos are already loaded in memory, ensureLegacySubagentsLoaded reads them with zero DB queries.
	st2 := &legacySpawnTodoStore{
		memA2AStore: newMemA2AStore(),
		todos:       []db.TodoRow{{ID: "t2", Title: "In-memory parent task"}},
		agents: []db.TodoAgentRow{
			{TodoID: "t2", MachineID: "box", SessionID: "mem-spawn", Source: sourceSpawned, Via: "mac/lead"},
		},
	}
	s2 := New(Options{Hub: newFakeHub(), Store: func() Store { return st2 }, Logf: testLogf(t)})
	s2.todos.mu.Lock()
	s2.todos.loaded = true
	s2.todos.todos = map[string]*Todo{
		"t2": {ID: "t2", Agents: []TodoAgent{{MachineID: "box", SessionID: "mem-spawn", Source: sourceSpawned, Via: "mac/lead"}}},
	}
	s2.todos.mu.Unlock()
	memRows := []proto.Session{{MachineID: "box", SessionID: "mem-spawn", Status: proto.StatusWorking}}
	s2.decorateSubagents(ctx, "box", memRows)
	if !memRows[0].IsSubagent || memRows[0].ParentSessionID != "lead" || memRows[0].ParentMachineID() != "mac" ||
		memRows[0].SubagentOrigin() != proto.SubagentOriginSpawned {
		t.Fatalf("in-memory todo spawned provenance wrong: %+v %+v", memRows[0], memRows[0].Hierarchy)
	}
	st2.agentsMu.Lock()
	st2Calls := st2.agentsCalls
	st2.agentsMu.Unlock()
	if st2Calls != 0 {
		t.Fatalf("already-loaded todos still queried ListAllTodoAgents %d times", st2Calls)
	}
}

func archivedSubagentCacheFixture(tb testing.TB, liveCount, listedArchCount, tailArchCount int) (*Service, *fleetDevs, *memA2AStore, []proto.Session) {
	tb.Helper()
	ctx := context.Background()
	st := newMemA2AStore()
	f := newFakeHub()
	f.hello.Assistant.MachineID = "mac"
	f.hello.Assistant.Machines = []string{"mac", "box"}
	f.hello.ArchivedApart = true
	f.devices = []Device{{ID: "mac", Home: "/home/u"}, {ID: "box", Home: "/home/u"}}
	d := newFleetDevs()
	d.add("mac", "Mac")
	d.add("box", "Box")
	s := New(Options{Hub: f, Devices: d, Store: func() Store { return st }, Logf: func(string, ...any) {}})

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	rows := make([]proto.Session, 0, liveCount+listedArchCount+1)
	for i := range liveCount {
		rows = append(rows, proto.Session{
			MachineID: "mac", SessionID: fmt.Sprintf("live-%03d", i),
			Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: at,
		})
	}
	rows = append(rows, proto.Session{
		MachineID: "mac", SessionID: "sub-1",
		Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: at,
	})
	records := map[string]agentArchive{}
	for i := range listedArchCount {
		sess := proto.Session{
			MachineID: "mac", SessionID: fmt.Sprintf("listed-arch-%03d", i),
			Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: at,
		}
		rows = append(rows, sess)
		records[runtimeListID("mac", sess.SessionID)] = agentArchive{
			Session: archiveSnapshot(sess), Archived: true, ChangedAt: at.Add(time.Hour),
		}
	}
	for i := range tailArchCount {
		sess := proto.Session{
			MachineID: "mac", SessionID: fmt.Sprintf("tail-arch-%03d", i),
			Agent: "codex", Cwd: "/code/app", Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: at.Add(-48 * time.Hour),
		}
		records[runtimeListID("mac", sess.SessionID)] = agentArchive{
			Session: archiveSnapshot(sess), Archived: true, ChangedAt: at.Add(-24 * time.Hour),
		}
	}
	restoredSess := proto.Session{
		MachineID: "mac", SessionID: "tail-restored-1",
		Agent: "codex", Cwd: "/code/app", Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: at.Add(-24 * time.Hour),
	}
	records[runtimeListID("mac", restoredSess.SessionID)] = agentArchive{
		Session: archiveSnapshot(restoredSess), Archived: false, ChangedAt: at.Add(-12 * time.Hour),
	}
	if err := st.PutSetting(ctx, agentArchivesKey, records); err != nil {
		tb.Fatal(err)
	}
	body, _ := json.Marshal(a2aBody{Launch: &a2aLaunch{CWD: "/code/app", Subagent: true}})
	if _, err := st.A2ACreate(ctx, db.A2ARow{
		ID: "task-arch-cache-1", Owner: "mac/live-000", Request: "req-arch-cache-1", Context: "ctx-arch-cache-1",
		SourceMachine: "mac", SourceSession: "live-000", TargetMachine: "mac", TargetSession: "sub-1",
		State: a2aWorking, Body: body,
	}); err != nil {
		tb.Fatal(err)
	}
	d.setSessions("mac", rows)
	d.setSessions("box", []proto.Session{{
		MachineID: "box", SessionID: "box-live-1",
		Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: at,
	}})
	return s, d, st, rows
}

// Alternating publishList (Hello.ArchivedApart -> decoratedApart) and live refresh (decoratedInto)
// must retain archived sessions in subagentsChecked without including them in visible folding or foldMeta.
func TestSubagentCheckedRetainsArchivedAcrossPublishAndRefresh(t *testing.T) {
	ctx := context.Background()
	s, d, st, rows := archivedSubagentCacheFixture(t, 3, 2, 3)

	// Add a cross-machine archived row on mac referencing box so we verify decoratedApart
	// retains it in cache without pulling it into crossParents or foldMeta.
	var records map[string]agentArchive
	if _, err := st.Setting(ctx, agentArchivesKey, &records); err != nil {
		t.Fatal(err)
	}
	crossArch := proto.Session{
		MachineID: "mac", SessionID: "listed-cross-arch",
		Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, State: proto.StateIdle,
		ParentSessionID: "box-live-1", IsSubagent: true,
		UpdatedAt: time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
	}
	crossArch.SetSubagentProvenance("box", "task-cross-arch", proto.SubagentOriginA2A)
	rows = append(rows, crossArch)
	records[runtimeListID("mac", crossArch.SessionID)] = agentArchive{
		Session: archiveSnapshot(crossArch), Archived: true, ChangedAt: crossArch.UpdatedAt.Add(time.Hour),
	}
	if err := st.PutSetting(ctx, agentArchivesKey, records); err != nil {
		t.Fatal(err)
	}
	d.setSessions("mac", rows)

	// Prime another machine's bucket to verify per-machine pruning never touches it.
	s.decorateSubagents(ctx, "box", []proto.Session{{MachineID: "box", SessionID: "box-live-1", Status: proto.StatusIdle}})

	// Cold pass: publish (decoratedApart) then live refresh (decoratedInto).
	shown, archived := s.decoratedApart("mac", s.archiveMark())
	for _, row := range shown {
		if row.Archived && !row.Assistant {
			t.Fatalf("decoratedApart leaked archived row %s into shown", row.SessionID)
		}
	}
	if len(archived.rows) != 3 || len(archived.keys) != 3 {
		t.Fatalf("archived partition = %d rows / %d keys, want 3 / 3", len(archived.rows), len(archived.keys))
	}
	// Archived cross-machine row must NOT enter foldMeta or crossParents during decoratedApart.
	s.fleet.foldMu.Lock()
	hasCrossParent := len(s.fleet.crossParents["mac"]) > 0
	_, hasFoldMeta := s.fleet.foldMeta["mac"]
	s.fleet.foldMu.Unlock()
	if hasCrossParent || hasFoldMeta {
		t.Fatalf("decoratedApart polluted visible fold metadata with archived rows: crossParents=%v foldMeta=%v", hasCrossParent, hasFoldMeta)
	}

	var buf []proto.Session
	buf = s.decoratedInto("mac", buf)
	foundSub := false
	for _, row := range buf {
		if row.SessionID == "sub-1" && row.IsSubagent && row.A2ATaskID() == "task-arch-cache-1" {
			foundSub = true
		}
	}
	if !foundSub {
		t.Fatal("sub-1 missing expected A2A subagent provenance after live refresh")
	}

	st.mu.Lock()
	afterCold := st.launchQueries
	st.mu.Unlock()
	if afterCold == 0 {
		t.Fatal("expected cold pass to query A2ALaunchesForTargets")
	}

	// Warm alternating publish + live refresh rounds MUST make ZERO A2ALaunchesForTargets queries.
	for round := range 4 {
		s.publishList("mac")
		buf = s.decoratedInto("mac", buf)
		st.mu.Lock()
		got := st.launchQueries
		st.mu.Unlock()
		if got != afterCold {
			t.Fatalf("warm alternating round %d queried A2ALaunchesForTargets %d extra times (cold=%d now=%d)",
				round, got-afterCold, afterCold, got)
		}
	}

	// Consecutive publishList calls with node-listed archived rows MUST also make ZERO queries.
	for round := range 3 {
		s.publishList("mac")
		st.mu.Lock()
		got := st.launchQueries
		st.mu.Unlock()
		if got != afterCold {
			t.Fatalf("warm consecutive publish %d queried A2ALaunchesForTargets %d extra times", round, got-afterCold)
		}
	}

	// Removing a live unarchived session (live-001) prunes only that session on mac,
	// retaining remaining live rows, archived rows, archived tail rows, restored tail rows, and other machines.
	prunedRows := make([]proto.Session, 0, len(rows)-1)
	for _, r := range rows {
		if r.SessionID != "live-001" {
			prunedRows = append(prunedRows, r)
		}
	}
	d.setSessions("mac", prunedRows)
	s.publishList("mac")

	s.fleet.subagentsMu.RLock()
	_, hasLive0 := s.fleet.subagentsChecked["mac"]["live-000"]
	_, hasLive1 := s.fleet.subagentsChecked["mac"]["live-001"]
	_, hasSub1 := s.fleet.subagentsChecked["mac"]["sub-1"]
	_, hasListedArch := s.fleet.subagentsChecked["mac"]["listed-arch-000"]
	_, hasTailArch := s.fleet.subagentsChecked["mac"]["tail-arch-000"]
	_, hasTailRestored := s.fleet.subagentsChecked["mac"]["tail-restored-1"]
	_, hasBoxLive := s.fleet.subagentsChecked["box"]["box-live-1"]
	s.fleet.subagentsMu.RUnlock()
	if !hasLive0 || hasLive1 || !hasSub1 || !hasListedArch || !hasTailArch || !hasTailRestored || !hasBoxLive {
		t.Fatalf("prune state mismatch after live removal: live0=%v live1=%v sub1=%v listedArch=%v tailArch=%v tailRestored=%v boxLive=%v",
			hasLive0, hasLive1, hasSub1, hasListedArch, hasTailArch, hasTailRestored, hasBoxLive)
	}

	// Removing an archived record from storage prunes it on the next decoratedApart pass.
	delete(records, runtimeListID("mac", "tail-arch-002"))
	if err := st.PutSetting(ctx, agentArchivesKey, records); err != nil {
		t.Fatal(err)
	}
	if err := s.agentArchives.lock(ctx); err != nil {
		t.Fatal(err)
	}
	s.agentArchives.loaded = false
	s.agentArchives.tails = nil
	s.agentArchives.unlock()
	s.publishList("mac")

	s.fleet.subagentsMu.RLock()
	_, hasRemovedTailArch := s.fleet.subagentsChecked["mac"]["tail-arch-002"]
	_, stillHasTailArch := s.fleet.subagentsChecked["mac"]["tail-arch-000"]
	s.fleet.subagentsMu.RUnlock()
	if hasRemovedTailArch || !stillHasTailArch {
		t.Fatalf("removed archive tail entry prune mismatch: removed=%v kept=%v", hasRemovedTailArch, stillHasTailArch)
	}
}

func BenchmarkSubagentCheckedAlternatingPublishAndRefresh(b *testing.B) {
	s, d, st, rows := archivedSubagentCacheFixture(b, 50, 10, 100)
	var buf []proto.Session
	// Warm both paths once before measuring steady-state rounds.
	s.publishList("mac")
	buf = s.decoratedInto("mac", buf)

	st.mu.Lock()
	startQueries := st.launchQueries
	st.mu.Unlock()

	b.ReportAllocs()
	n := 0
	for b.Loop() {
		n++
		rows[n%50].LastText = fmt.Sprintf("turn-%d", n)
		d.setSessions("mac", rows)
		s.publishList("mac")
		buf = s.decoratedInto("mac", buf)
	}

	st.mu.Lock()
	queries := st.launchQueries - startQueries
	st.mu.Unlock()
	b.ReportMetric(float64(queries)/float64(n), "queries/op")
}
