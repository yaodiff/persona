package hub

import (
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
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type heldStartTestManager struct {
	mu           sync.Mutex
	holdReqs     []devicewire.HoldStartRequest
	nodeCalls    []devicewire.NodeCall
	todoHandoffs []assistantwire.TodoHandoff
	todoLinks    []assistantwire.TodoLinkParams
	onHold       func(devicewire.HoldStartRequest) (any, error)
	onStartParam func(assistantwire.AgentStartParams) (any, error)
	onNodeCall   func(devicewire.NodeCall) (any, error)
	onTodoHand   func(assistantwire.TodoHandoff) (any, error)
}

func heldStartHub(t *testing.T, connected bool) (*Hub, *Machine, *heldStartTestManager, *guardManager) {
	t.Helper()
	h := newTestHub(t)
	h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	m := newMachine(h, MachineConfig{
		ID:        "box",
		Name:      "Studio Mac",
		Launchers: []assistantwire.ConfiguredLauncher{{Name: "claude", AgentType: "claude"}, {Name: "codex", AgentType: "codex"}},
	})
	m.managed = true
	m.connected = connected
	m.peer = &busNode{h: h, id: "box", done: make(chan struct{})}
	registerMachine(h, m)

	h.bus.idents = &memIdents{}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	token, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
	if err != nil {
		t.Fatal(err)
	}
	mgr := &heldStartTestManager{}
	gm := connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, token, func(method string, raw json.RawMessage) (any, error) {
		switch method {
		case devicewire.MethodHoldStart:
			var req devicewire.HoldStartRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			mgr.mu.Lock()
			mgr.holdReqs = append(mgr.holdReqs, req)
			fn := mgr.onHold
			mgr.mu.Unlock()
			if fn != nil {
				return fn(req)
			}
			return devicewire.HoldStartReply{
				Delivered: "later",
				HeldStart: proto.HeldStart{
					OpID:      req.Call.OpID,
					MachineID: req.Call.MachineID,
					Computer:  "Studio Mac",
					Cwd:       req.Call.Meta.Cwd,
					Launcher:  req.Call.Meta.Launcher,
					State:     proto.HeldStartWaiting,
					Error:     "Will start when Studio Mac is back",
					SentAt:    time.Now().UTC(),
					HeldAt:    time.Now().UTC(),
					CanCancel: true,
				},
			}, nil
		case assistantwire.MethodAgentStartParams:
			var p assistantwire.AgentStartParams
			_ = json.Unmarshal(raw, &p)
			mgr.mu.Lock()
			fn := mgr.onStartParam
			mgr.mu.Unlock()
			if fn != nil {
				return fn(p)
			}
			return map[string]any{"params": p.Params}, nil
		case assistantwire.MethodTodoHandoff:
			var p assistantwire.TodoHandoff
			_ = json.Unmarshal(raw, &p)
			mgr.mu.Lock()
			mgr.todoHandoffs = append(mgr.todoHandoffs, p)
			fn := mgr.onTodoHand
			mgr.mu.Unlock()
			if fn != nil {
				return fn(p)
			}
			return assistantwire.TodoHandoffResult{Found: true, Title: "Ship PR", Text: "[context]\n" + p.Text}, nil
		case assistantwire.MethodTodoLink:
			var p assistantwire.TodoLinkParams
			_ = json.Unmarshal(raw, &p)
			mgr.mu.Lock()
			mgr.todoLinks = append(mgr.todoLinks, p)
			mgr.mu.Unlock()
			return assistantwire.TodoLinkResult{Title: "Ship PR"}, nil
		case devicewire.MethodCall:
			var c devicewire.NodeCall
			_ = json.Unmarshal(raw, &c)
			mgr.mu.Lock()
			mgr.nodeCalls = append(mgr.nodeCalls, c)
			fn := mgr.onNodeCall
			mgr.mu.Unlock()
			if fn != nil {
				return fn(c)
			}
			return nil, &rpc.Error{Code: devicewire.CodeMachineOffline, Message: "Studio Mac is offline"}
		case assistantwire.MethodHTTP:
			return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
		}
		return map[string]any{}, nil
	})
	h.devices.mu.Lock()
	h.devices.upOpen = map[string]bool{devicesOwner: true}
	h.devices.mu.Unlock()
	return h, m, mgr, gm
}

func postStart(h *Hub, who principal, features, opID, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, who))
	if features != "" {
		r.Header.Set(featuresHeader, features)
	}
	if opID != "" {
		r.Header.Set("X-Persona-Op-Id", opID)
	}
	w := httptest.NewRecorder()
	h.withClientOp("start", h.startSession)(w, r)
	return w
}

func TestStartSessionHoldsWhenComputerOfflineAndReplaysReceipt(t *testing.T) {
	h, _, mgr, _ := heldStartHub(t, false)
	const opID = "11111111-1111-4111-8111-111111111111"
	body := `{"machine_id":"box","cwd":"/work/repo","launcher":"claude","prompt":"ship it","todo_id":"todo-1"}`

	w := postStart(h, ownerPhone, proto.FeatureAgentStartHold, opID, body)
	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d %s", w.Code, w.Body)
	}
	var out devicewire.HoldStartReply
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Delivered != "later" ||
		out.HeldStart.OpID != opID || out.HeldStart.State != proto.HeldStartWaiting || out.HeldStart.Computer != "Studio Mac" {
		t.Fatalf("unexpected 202 body: %s %v", w.Body, err)
	}
	mgr.mu.Lock()
	if len(mgr.holdReqs) != 1 || !mgr.holdReqs[0].OnlyIfOffline || mgr.holdReqs[0].Call.Meta.TodoID != "todo-1" ||
		len(mgr.todoHandoffs) != 0 || len(mgr.nodeCalls) != 0 {
		t.Fatalf("holdReqs=%+v todoHandoffs=%d nodeCalls=%d", mgr.holdReqs, len(mgr.todoHandoffs), len(mgr.nodeCalls))
	}
	mgr.mu.Unlock()

	// HTTP retry with the same X-Persona-Op-Id replays the 202 receipt idempotently without holding twice.
	again := postStart(h, ownerPhone, proto.FeatureAgentStartHold, opID, body)
	if again.Code != http.StatusAccepted || again.Body.String() != w.Body.String() {
		t.Fatalf("retry reply: %d %s, want %d %s", again.Code, again.Body, w.Code, w.Body)
	}
	mgr.mu.Lock()
	if len(mgr.holdReqs) != 1 {
		t.Fatalf("expected 1 hold request after HTTP retry, got %d", len(mgr.holdReqs))
	}
	mgr.mu.Unlock()
}

func TestStartSessionOnlinePassThroughAndMidRaceOfflineFallback(t *testing.T) {
	// 1. Hub thinks machine is offline, manager says CodeMachineOnline -> Hub proceeds to live session.start with op_id.
	h, _, mgr, _ := heldStartHub(t, false)
	mgr.onHold = func(req devicewire.HoldStartRequest) (any, error) {
		if req.OnlyIfOffline {
			return nil, &rpc.Error{Code: devicewire.CodeMachineOnline, Message: "machine is online"}
		}
		return nil, &rpc.Error{Code: "unexpected", Message: "should not fallback when start succeeds"}
	}
	var gotOpID string
	mgr.onNodeCall = func(c devicewire.NodeCall) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		gotOpID, _ = p["op_id"].(string)
		return map[string]any{"session": proto.Session{SessionID: "sess-live-1", Launcher: "claude", Cwd: "/work"}}, nil
	}
	const op1 = "22222222-2222-4222-8222-222222222222"
	w := postStart(h, ownerPhone, proto.FeatureAgentStartHold, op1, `{"machine_id":"box","cwd":"/work","launcher":"claude","prompt":"go"}`)
	if w.Code != http.StatusOK || gotOpID != op1 {
		t.Fatalf("online pass-through: %d %s op_id=%q", w.Code, w.Body, gotOpID)
	}

	// 2. Hub thinks machine is online, node call returns CodeMachineOffline -> Hub falls back to holdStart(OnlyIfOffline: false).
	h2, _, mgr2, _ := heldStartHub(t, true)
	const op2 = "33333333-3333-4333-8333-333333333333"
	w2 := postStart(h2, ownerPhone, proto.FeatureAgentStartHold, op2, `{"machine_id":"box","cwd":"/work","launcher":"claude","prompt":"fallback"}`)
	if w2.Code != http.StatusAccepted {
		t.Fatalf("mid-race fallback: %d %s", w2.Code, w2.Body)
	}
	mgr2.mu.Lock()
	if len(mgr2.holdReqs) != 1 || mgr2.holdReqs[0].OnlyIfOffline {
		t.Fatalf("fallback hold req: %+v", mgr2.holdReqs)
	}
	mgr2.mu.Unlock()
}

func TestStartSessionValidationErrorsNeverHeldAndGating(t *testing.T) {
	h, m, mgr, _ := heldStartHub(t, false)

	cases := []struct {
		name     string
		who      principal
		features string
		opID     string
		body     string
		wantCode int
	}{
		{
			name: "unknown machine", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000001", body: `{"machine_id":"missing","cwd":"/work","launcher":"claude"}`,
			wantCode: http.StatusNotFound,
		},
		{
			name: "bad work_session", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000002", body: `{"machine_id":"box","work_session":true,"cwd":"/work"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "invalid agent_type", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000003", body: `{"machine_id":"box","cwd":"/work","agent_type":"bad agent!"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "reserved client_msg_id", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000004", body: `{"machine_id":"box","cwd":"/work","client_msg_id":"` + proto.A2ADeliveryPrefix + `bad"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name: "client without agent_start_hold", who: ownerPhone, features: "",
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000005", body: `{"machine_id":"box","cwd":"/work","launcher":"claude"}`,
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "missing op_id", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "", body: `{"machine_id":"box","cwd":"/work","launcher":"claude"}`,
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "hub token not person", who: principal{Role: roleFull}, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000006", body: `{"machine_id":"box","cwd":"/work","launcher":"claude"}`,
			wantCode: http.StatusServiceUnavailable,
		},
		{
			name: "resume is not held", who: ownerPhone, features: proto.FeatureAgentStartHold,
			opID: "aaaaaaaa-aaaa-4aaa-8aaa-000000000007", body: `{"machine_id":"box","cwd":"/work","launcher":"claude","resume_session_id":"old-1"}`,
			wantCode: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := postStart(h, tc.who, tc.features, tc.opID, tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("%s: got %d %s, want %d", tc.name, w.Code, w.Body, tc.wantCode)
			}
		})
	}

	// Untrusted machine fails 409 machine_pending and is never held.
	h.trust.mu.Lock()
	h.trust.pending = map[string]time.Time{m.cfg.ID: time.Now()}
	h.trust.mu.Unlock()
	w := postStart(h, ownerPhone, proto.FeatureAgentStartHold, "aaaaaaaa-aaaa-4aaa-8aaa-000000000008",
		`{"machine_id":"box","cwd":"/work","launcher":"claude"}`)
	if w.Code != http.StatusConflict || errorCodeOf(w.Body.Bytes()) != "machine_pending" {
		t.Fatalf("untrusted machine: %d %s", w.Code, w.Body)
	}
	h.trust.mu.Lock()
	h.trust.pending = nil
	h.trust.mu.Unlock()

	mgr.mu.Lock()
	if len(mgr.holdReqs) != 0 {
		t.Fatalf("validation/gating cases unexpectedly called hold_start %d times", len(mgr.holdReqs))
	}
	mgr.mu.Unlock()

	// held_start_full from manager surfaces localized error without falling through to 503.
	mgr.onHold = func(devicewire.HoldStartRequest) (any, error) {
		return nil, &rpc.Error{Code: devicewire.CodeHeldStartFull, Message: "too many held starts"}
	}
	full := postStart(h, ownerPhone, proto.FeatureAgentStartHold, "aaaaaaaa-aaaa-4aaa-8aaa-000000000009",
		`{"machine_id":"box","cwd":"/work","launcher":"claude"}`)
	if errorCodeOf(full.Body.Bytes()) != devicewire.CodeHeldStartFull {
		t.Fatalf("held_start_full: %d %s", full.Code, full.Body)
	}
}

func TestBusReplayHeldStartChecksRoleBeforeDecode(t *testing.T) {
	h := newTestHub(t)
	handle := busMethods[devicewire.MethodReplayHeldStart]
	for _, tc := range []struct {
		conn         *busConn
		code, reason string
	}{
		{&busConn{}, "forbidden", "only managers can replay held starts"},
		{&busConn{ident: db.BusIdentity{Role: bus.RoleManager}}, bus.CodeInvalidParams, "call.machine_id and call.op_id are required"},
	} {
		out, err := handle(h, t.Context(), tc.conn, json.RawMessage(`{"call":false}`))
		var re *rpc.Error
		if out != nil || !errors.As(err, &re) || re.Code != tc.code || re.Message != tc.reason {
			t.Errorf("role %q: out %v, err %v", tc.conn.ident.Role, out, err)
		}
	}
}

func TestBusReplayHeldStartFullPathAndOutcomes(t *testing.T) {
	h, _, mgr, gm := heldStartHub(t, true)
	const opID = "44444444-4444-4444-8444-444444444444"

	// 1. Full replay: runs todoHandoff, agentStartParams, session.start (with op_id), and TodoLink.
	mgr.onStartParam = func(p assistantwire.AgentStartParams) (any, error) {
		p.Params["model"] = "opus"
		return map[string]any{"params": p.Params}, nil
	}
	var startedMethod, startedOp, startedPrompt, startedModel string
	mgr.onNodeCall = func(c devicewire.NodeCall) (any, error) {
		startedMethod = c.Method
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		startedOp, _ = p["op_id"].(string)
		startedPrompt, _ = p["prompt"].(string)
		startedModel, _ = p["model"].(string)
		return map[string]any{"session": proto.Session{SessionID: "sess-replayed-1", Launcher: "claude", Cwd: "/work/repo", Agent: "claude"}}, nil
	}

	params, _ := json.Marshal(sessionStartRequest{
		MachineID: "box", Cwd: "/work/repo", Launcher: "claude", Prompt: "build feature", TodoID: "todo-42",
	})
	var reply devicewire.ReplayHeldStartReply
	if err := gm.call(t, devicewire.MethodReplayHeldStart, devicewire.ReplayHeldStartRequest{
		Call: devicewire.HeldStartCall{
			MachineID: "box", OpID: opID, Params: params,
			Principal: devicewire.HeldStartPrincipal{ID: "dev-phone", Role: roleFull},
			Caller:    &devicewire.Caller{Kind: "device", CredentialID: "dev-phone"},
			Lang:      "en",
		},
	}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != devicewire.ReplayOutcomeStarted || reply.SessionID != "sess-replayed-1" {
		t.Fatalf("replay reply: %+v", reply)
	}
	if startedMethod != "session.start" || startedOp != opID || startedModel != "opus" || !strings.Contains(startedPrompt, "[context]") {
		t.Fatalf("forwarded node start: method=%q op=%q model=%q prompt=%q", startedMethod, startedOp, startedModel, startedPrompt)
	}
	mgr.mu.Lock()
	if len(mgr.todoHandoffs) != 1 || len(mgr.todoLinks) != 1 || mgr.todoLinks[0].SessionID != "sess-replayed-1" {
		t.Fatalf("todo handoffs=%+v links=%+v", mgr.todoHandoffs, mgr.todoLinks)
	}
	mgr.mu.Unlock()

	// 2. WorkSession revalidation refusal when launcher drifted.
	mgr.onStartParam = func(p assistantwire.AgentStartParams) (any, error) {
		p.Params["launcher"] = "other-launcher"
		return map[string]any{"params": p.Params}, nil
	}
	workParams, _ := json.Marshal(sessionStartRequest{
		MachineID: "box", Cwd: "/work/repo", Launcher: "claude", AgentType: "claude", WorkSession: true,
	})
	reply = devicewire.ReplayHeldStartReply{}
	if err := gm.call(t, devicewire.MethodReplayHeldStart, devicewire.ReplayHeldStartRequest{
		Call: devicewire.HeldStartCall{
			MachineID: "box", OpID: "55555555-5555-4555-8555-555555555555", Params: workParams,
			Principal: devicewire.HeldStartPrincipal{ID: "dev-phone", Role: roleFull}, Lang: "en",
		},
	}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != devicewire.ReplayOutcomeRefused || reply.Reason == "" {
		t.Fatalf("expected work_target_changed refusal, got %+v", reply)
	}

	// 3. Mid-flight disconnect (CodeNodeClosed) -> ReplayOutcomeUnclear.
	mgr.onStartParam = nil
	mgr.onNodeCall = func(devicewire.NodeCall) (any, error) {
		return nil, &rpc.Error{Code: devicewire.CodeNodeClosed, Message: "connection closed mid-flight"}
	}
	reply = devicewire.ReplayHeldStartReply{}
	if err := gm.call(t, devicewire.MethodReplayHeldStart, devicewire.ReplayHeldStartRequest{
		Call: devicewire.HeldStartCall{
			MachineID: "box", OpID: "66666666-6666-4666-8666-666666666666", Params: params,
			Principal: devicewire.HeldStartPrincipal{ID: "dev-phone", Role: roleFull}, Lang: "en",
		},
	}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != devicewire.ReplayOutcomeUnclear {
		t.Fatalf("expected unclear on mid-flight disconnect, got %+v", reply)
	}

	// 4. Reconciled fast-path: skips node call completely, adds h.fresh for empty-prompt todo, and links todo.
	var nodeCalled atomic.Int32
	mgr.onNodeCall = func(devicewire.NodeCall) (any, error) {
		nodeCalled.Add(1)
		return nil, nil
	}
	emptyTodoParams, _ := json.Marshal(sessionStartRequest{
		MachineID: "box", Cwd: "/work/repo", Launcher: "claude", TodoID: "todo-99",
	})
	reply = devicewire.ReplayHeldStartReply{}
	if err := gm.call(t, devicewire.MethodReplayHeldStart, devicewire.ReplayHeldStartRequest{
		Call: devicewire.HeldStartCall{
			MachineID: "box", OpID: "77777777-7777-4777-8777-777777777777", Params: emptyTodoParams,
			Principal: devicewire.HeldStartPrincipal{ID: "dev-phone", Role: roleFull}, Lang: "en",
		},
		ReconciledSessionID: "sess-reconciled-99",
	}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Outcome != devicewire.ReplayOutcomeStarted || reply.SessionID != "sess-reconciled-99" || nodeCalled.Load() != 0 {
		t.Fatalf("reconciled reply=%+v nodeCalled=%d", reply, nodeCalled.Load())
	}
	if f, ok := h.fresh.peek("box", "sess-reconciled-99"); !ok || f.todoID != "todo-99" {
		t.Fatalf("expected h.fresh registration for reconciled empty-prompt todo start: %+v %v", f, ok)
	}
}

func TestHeldStartBroadcastAndRoutesGating(t *testing.T) {
	h, _, _, gm := heldStartHub(t, false)

	fullWant := &client{hub: h, role: roleFull, out: make(chan []byte, 8), urlFeatures: map[string]bool{proto.FeatureAgentStartHold: true}}
	fullLegacy := &client{hub: h, role: roleFull, out: make(chan []byte, 8)}
	watchWant := &client{hub: h, role: roleAnswer, out: make(chan []byte, 8), urlFeatures: map[string]bool{proto.FeatureAgentStartHold: true}}
	h.mu.Lock()
	h.clients[fullWant] = struct{}{}
	h.clients[fullLegacy] = struct{}{}
	h.clients[watchWant] = struct{}{}
	h.mu.Unlock()
	t.Cleanup(func() {
		h.mu.Lock()
		delete(h.clients, fullWant)
		delete(h.clients, fullLegacy)
		delete(h.clients, watchWant)
		h.mu.Unlock()
	})

	msg, _ := json.Marshal(map[string]any{
		"type":       proto.HeldStartEventType,
		"machine_id": "box",
		"held_start": proto.HeldStart{
			OpID: "88888888-8888-4888-8888-888888888888", MachineID: "box", State: proto.HeldStartWaiting,
		},
	})
	if err := gm.peer.Notify(assistantwire.EventBroadcast, assistantwire.Broadcast{Message: msg}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-fullWant.out:
		if !strings.Contains(string(got), `"type":"held_start"`) {
			t.Fatalf("unexpected broadcast: %s", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("full client declaring agent_start_hold did not receive held_start broadcast")
	}
	if len(fullLegacy.out) != 0 {
		t.Fatalf("legacy full client unexpectedly received held_start: %s", <-fullLegacy.out)
	}
	if len(watchWant.out) != 0 {
		t.Fatalf("answer-only client unexpectedly received held_start: %s", <-watchWant.out)
	}

	// Relayed held-starts routes refuse non-person credentials at the hub boundary.
	api := http.NewServeMux()
	h.deviceRoutesRelayed(api)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/machines/box/held-starts"},
		{http.MethodPost, "/api/v1/machines/box/held-starts/op1/retry"},
		{http.MethodDelete, "/api/v1/machines/box/held-starts/op1"},
	} {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{Role: roleFull})) // hub token: no ID
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s %s with hub token: %d %s, want 403", tc.method, tc.path, w.Code, w.Body)
		}
		r2 := httptest.NewRequest(tc.method, tc.path, nil)
		r2 = r2.WithContext(context.WithValue(r2.Context(), principalKey{}, ownerPhone))
		w2 := httptest.NewRecorder()
		api.ServeHTTP(w2, r2)
		if w2.Code != http.StatusOK {
			t.Fatalf("%s %s with person full device: %d %s, want 200", tc.method, tc.path, w2.Code, w2.Body)
		}
	}
}
