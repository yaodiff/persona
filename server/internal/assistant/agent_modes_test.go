package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func agentModesRig(t *testing.T) *fleetTest {
	t.Helper()
	ft, _ := switchRig(t)
	return ft
}

func runAgentModes(t *testing.T, ft *fleetTest, conv string, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return ft.s.runTool(context.Background(), "server", conv, "agent_modes", raw)
}

func waitAgentModesCard(t *testing.T, s *Service) proto.Interaction {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)
	for {
		if qs := s.pendingQuestions(); len(qs) == 1 {
			return qs[0]
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for agent_modes confirmation card")
		case <-ticker.C:
		}
	}
}

func answerAgentModesCard(t *testing.T, s *Service, id string, res proto.Resolution) {
	t.Helper()
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: id, Answer: raw}); err != nil {
		t.Fatalf("answer card %s: %v", id, err)
	}
}

func runningAgentSession(sid string) proto.Session {
	return proto.Session{
		SessionID:      sid,
		MachineID:      "mac",
		Cwd:            "/code/app",
		Agent:          "claude",
		Launcher:       "claude",
		PermissionMode: "default",
		Status:         proto.StatusIdle,
		State:          proto.StateIdle,
		UpdatedAt:      time.Now(),
		Capabilities: &proto.SessionCapabilities{
			Read:  true,
			Send:  true,
			Modes: proto.CanModes(true),
			Model: proto.Bool(true),
		},
	}
}

func TestAgentModesModelAndEffort(t *testing.T) {
	t.Run("updates model and effort without permission card", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": true}, nil
			},
		}

		res, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine":    "mac",
			"session_id": "s1abcdef",
			"model":      "opus",
			"effort":     "high",
		})
		if err != nil {
			t.Fatalf("agent_modes: %v", err)
		}
		if !strings.Contains(res, "Updated") || !strings.Contains(res, "model=opus") || !strings.Contains(res, "effort=high") {
			t.Fatalf("unexpected reply: %s", res)
		}
		if qs := ft.s.pendingQuestions(); len(qs) != 0 {
			t.Fatalf("unexpected confirmation cards: %+v", qs)
		}
		calls := nodeCalls(ft.f)
		if len(calls) != 1 || calls[0].Method != DeviceSessionModes {
			t.Fatalf("calls: %+v", calls)
		}
		p := callParams(t, calls[0])
		if p["session_id"] != "s1abcdef01" || p["model"] != "opus" || p["effort"] != "high" {
			t.Fatalf("params = %v", p)
		}
		if _, hasPerm := p["permission_mode"]; hasPerm {
			t.Fatalf("unexpected permission_mode in params: %v", p)
		}
		if _, hasLauncher := p["launcher"]; hasLauncher {
			t.Fatalf("unexpected launcher in params: %v", p)
		}
	})

	t.Run("reports unchanged and queued effort outcomes faithfully", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": false}, nil
			},
		}
		res, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": "s1abcdef", "model": "sonnet",
		})
		if err != nil || !strings.Contains(res, "already matched") {
			t.Fatalf("unchanged reply = %q, err = %v", res, err)
		}

		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": true, "pending_effort": "max"}, nil
			},
		}
		res, err = runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": "s1abcdef", "effort": "max",
		})
		if err != nil || !strings.Contains(res, "effort max queued") {
			t.Fatalf("pending effort reply = %q, err = %v", res, err)
		}
	})

	t.Run("local nodeCall attaches AssistantModeActor for audit trail", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		d := &modeActorDevices{}
		ft.s.devs = d
		if err := ft.s.nodeCall(context.Background(), "box", DeviceSessionModes, map[string]any{
			"session_id":      "s1abcdef01",
			"permission_mode": "plan",
		}, nil); err != nil {
			t.Fatal(err)
		}
		if len(d.actors) != 1 || d.actors[0] == nil || d.actors[0].Kind != "assistant" {
			t.Fatalf("actors = %+v, want assistant", d.actors)
		}
	})
}

func TestAgentModesPermissionConfirmation(t *testing.T) {
	t.Run("requires owner approval card and applies restart outcome", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.f.hello.Locale = "en"
		ft.sessions("mac", runningAgentSession("s1abcdef01"))
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": true, "applied": "restart"}, nil
			},
		}

		done := make(chan struct {
			res string
			err error
		}, 1)
		go func() {
			res, err := runAgentModes(t, ft, "conv1", map[string]any{
				"machine":         "mac",
				"session_id":      "s1abcdef",
				"permission_mode": proto.ModeBypass,
				"effort":          "high",
			})
			done <- struct {
				res string
				err error
			}{res, err}
		}()

		ix := waitAgentModesCard(t, ft.s)
		if !ix.OwnerProposal || ix.ToolName != "agent_modes" || ix.Kind != proto.IxPermission {
			t.Fatalf("card = %+v, want OwnerProposal agent_modes IxPermission", ix)
		}
		if !strings.Contains(ix.Summary, "permission_mode=default→bypassPermissions") || !strings.Contains(ix.Summary, "permission prompts") {
			t.Fatalf("bypass card summary missing details: %q", ix.Summary)
		}
		var input map[string]string
		if err := json.Unmarshal(ix.ToolInput, &input); err != nil {
			t.Fatal(err)
		}
		if input["machine"] != "mac" || input["session_id"] != "s1abcdef01" || input["from_permission_mode"] != "default" || input["permission_mode"] != proto.ModeBypass {
			t.Fatalf("canonical tool input = %v", input)
		}

		allow := true
		answerAgentModesCard(t, ft.s, ix.ID, proto.Resolution{Allow: &allow})
		out := <-done
		if out.err != nil {
			t.Fatalf("agent_modes error: %v", out.err)
		}
		if !strings.Contains(out.res, "Updated") || !strings.Contains(out.res, "restart queued") {
			t.Fatalf("unexpected reply: %s", out.res)
		}
		calls := nodeCalls(ft.f)
		if len(calls) != 1 || calls[0].Method != DeviceSessionModes {
			t.Fatalf("calls = %+v", calls)
		}
		p := callParams(t, calls[0])
		if p["permission_mode"] != proto.ModeBypass || p["effort"] != "high" {
			t.Fatalf("params = %v", p)
		}
	})

	t.Run("rejects assistant self-approval of permission card", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))

		done := make(chan struct {
			res string
			err error
		}, 1)
		go func() {
			res, err := runAgentModes(t, ft, "conv1", map[string]any{
				"machine":         "mac",
				"session_id":      "s1abcdef",
				"permission_mode": "acceptEdits",
			})
			done <- struct {
				res string
				err error
			}{res, err}
		}()

		ix := waitAgentModesCard(t, ft.s)
		allow := true
		answerAgentModesCard(t, ft.s, ix.ID, proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant})
		out := <-done
		if out.err != nil {
			t.Fatalf("unexpected error: %v", out.err)
		}
		if strings.Contains(out.res, "Updated") || len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("assistant self-approval bypassed owner gate: res=%q calls=%+v", out.res, nodeCalls(ft.f))
		}
	})

	t.Run("owner decline calls no node RPC", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))

		done := make(chan struct {
			res string
			err error
		}, 1)
		go func() {
			res, err := runAgentModes(t, ft, "conv1", map[string]any{
				"machine":         "mac",
				"session_id":      "s1abcdef",
				"permission_mode": "plan",
			})
			done <- struct {
				res string
				err error
			}{res, err}
		}()

		ix := waitAgentModesCard(t, ft.s)
		deny := false
		answerAgentModesCard(t, ft.s, ix.ID, proto.Resolution{Allow: &deny})
		out := <-done
		if out.err != nil || !strings.Contains(out.res, "declined") {
			t.Fatalf("declined = %q, err = %v", out.res, out.err)
		}
		if len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("node called after decline: %+v", nodeCalls(ft.f))
		}
	})
}

func TestAgentModesNoOpDeduplication(t *testing.T) {
	ft := agentModesRig(t)
	s := runningAgentSession("s1abcdef01")
	s.PermissionMode = "plan"
	ft.sessions("mac", s)

	res, err := runAgentModes(t, ft, "conv1", map[string]any{
		"machine": "mac", "session_id": "s1abcdef", "permission_mode": "plan",
	})
	if err != nil || !strings.Contains(res, "already in permission_mode plan") {
		t.Fatalf("noop = %q, err = %v", res, err)
	}
	if len(nodeCalls(ft.f)) != 0 || len(ft.s.pendingQuestions()) != 0 {
		t.Fatalf("noop triggered RPC or card: calls=%+v", nodeCalls(ft.f))
	}

	// Same permission_mode + new model strips permission_mode and runs without a permission card.
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionModes: func(string, json.RawMessage) (any, error) {
			return map[string]any{"ok": true, "changed": true}, nil
		},
	}
	res, err = runAgentModes(t, ft, "conv1", map[string]any{
		"machine": "mac", "session_id": "s1abcdef", "model": "opus", "permission_mode": "plan",
	})
	if err != nil || !strings.Contains(res, "model=opus") {
		t.Fatalf("mixed noop perm = %q, err = %v", res, err)
	}
	calls := nodeCalls(ft.f)
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	if _, hasPerm := callParams(t, calls[0])["permission_mode"]; hasPerm {
		t.Fatalf("redundant permission_mode was sent: %v", callParams(t, calls[0]))
	}
}

func TestAgentModesStrictInputValidation(t *testing.T) {
	ft := agentModesRig(t)
	ft.sessions("mac", runningAgentSession("s1abcdef01"))

	bad := []struct {
		name string
		raw  string
		want string
	}{
		{"empty changes", `{"machine":"mac","session_id":"s1abcdef"}`, "at least one of model, effort, or permission_mode"},
		{"unknown launcher field", `{"machine":"mac","session_id":"s1abcdef","launcher":"other","model":"opus"}`, "unknown field"},
		{"unknown ultracode field", `{"machine":"mac","session_id":"s1abcdef","ultracode":true}`, "unknown field"},
		{"unknown service_tier field", `{"machine":"mac","session_id":"s1abcdef","service_tier":"fast"}`, "unknown field"},
		{"trailing JSON object", `{"machine":"mac","session_id":"s1abcdef","model":"opus"}{}`, "exactly one JSON object"},
		{"invalid permission_mode", `{"machine":"mac","session_id":"s1abcdef","permission_mode":"root"}`, "invalid permission_mode"},
		{"invalid effort", `{"machine":"mac","session_id":"s1abcdef","effort":"turbo"}`, "invalid effort"},
		{"control char in model", `{"machine":"mac","session_id":"s1abcdef","model":"opus\n/exit"}`, "control characters"},
		{"oversized model", `{"machine":"mac","session_id":"s1abcdef","model":"` + strings.Repeat("a", 201) + `"}`, "exceeds"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ft.s.runTool(context.Background(), "server", "conv1", "agent_modes", json.RawMessage(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
	if len(nodeCalls(ft.f)) != 0 {
		t.Fatalf("invalid inputs called node: %+v", nodeCalls(ft.f))
	}
}

func TestAgentModesTargetAndSafetyRefusals(t *testing.T) {
	t.Run("assistant, stopped, external, safety-refused, and capability refusals", func(t *testing.T) {
		ft := agentModesRig(t)
		macDev, _ := ft.s.fleetMachine("mac")
		asst := runningAgentSession("a1abcdef01")
		asst.Cwd = macDev.assistantCwd()
		asst.Assistant = true

		stopped := runningAgentSession("s2abcdef01")
		stopped.Status = proto.StatusOffline

		ext := runningAgentSession("s3abcdef01")
		ext.SourceKind = proto.SourceRuntime
		ext.StartedByPersona = false

		safety := runningAgentSession("s4abcdef01")
		safety.ModelFallback = &proto.ModelRefusal{
			Category:    proto.ModelRecoveryReasonSafeguards,
			Explanation: "safety policy blocked",
		}

		noModes := runningAgentSession("s5abcdef01")
		noModes.Capabilities = &proto.SessionCapabilities{
			Read:  true,
			Send:  true,
			Modes: proto.CanModes(false),
			Model: proto.Bool(false),
		}
		noModes.ReadOnlyReason = "desktop app"

		paseo := runningAgentSession("s6abcdef01")
		paseo.Agent = "codex"
		paseo.Efforts = []string{"low", "high"}
		paseo.Capabilities = &proto.SessionCapabilities{
			Read:            true,
			Send:            true,
			Modes:           proto.CanModes(false),
			Model:           proto.Bool(true),
			PermissionModes: proto.PermissionModeSetOf("default", proto.ModeBypass),
		}

		legacy := proto.Session{
			SessionID: "s7abcdef01", MachineID: "mac", Cwd: "/code/app", Agent: "claude",
			Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now(),
		}

		ft.sessions("mac", asst, stopped, ext, safety, noModes, paseo, legacy)

		cases := []struct {
			name string
			args map[string]any
			want string
		}{
			{"assistant conversation", map[string]any{"machine": "mac", "session_id": asst.SessionID, "model": "opus"}, "assistant conversation"},
			{"stopped session", map[string]any{"machine": "mac", "session_id": stopped.SessionID, "model": "opus"}, "stopped or is offline"},
			{"unmanaged external runtime", map[string]any{"machine": "mac", "session_id": ext.SessionID, "model": "opus"}, "external runtime session"},
			{"provider safety refusal", map[string]any{"machine": "mac", "session_id": safety.SessionID, "model": "opus"}, "safety refusal"},
			{"modes=false forbids permission_mode", map[string]any{"machine": "mac", "session_id": noModes.SessionID, "permission_mode": "plan"}, "desktop app"},
			{"model=false forbids model", map[string]any{"machine": "mac", "session_id": noModes.SessionID, "model": "opus"}, "does not support changing model"},
			{"modes=false no efforts forbids effort", map[string]any{"machine": "mac", "session_id": noModes.SessionID, "effort": "high"}, "does not support changing effort"},
			{"PermissionModes set rejects unsupported mode", map[string]any{"machine": "mac", "session_id": paseo.SessionID, "permission_mode": "plan"}, "supported: default, bypassPermissions"},
			{"Efforts list rejects unsupported effort", map[string]any{"machine": "mac", "session_id": paseo.SessionID, "effort": "xhigh"}, "supported: low, high"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := runAgentModes(t, ft, "conv1", tc.args)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("err = %v, want %q", err, tc.want)
				}
			})
		}
		if len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("refused cases called node: %+v", nodeCalls(ft.f))
		}

		// Older node with nil Capabilities passes through and surfaces node's modes_on_computer refusal verbatim.
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return nil, &rpc.Error{Code: proto.CodeModesOnComputer, Message: "change modes on the computer"}
			},
		}
		_, err := runAgentModes(t, ft, "conv1", map[string]any{"machine": "mac", "session_id": legacy.SessionID, "model": "opus"})
		var re *rpc.Error
		if err == nil || !errors.As(err, &re) || re.Code != proto.CodeModesOnComputer {
			t.Fatalf("legacy refusal = %v, want rpc code %s", err, proto.CodeModesOnComputer)
		}
	})

	t.Run("read-only task blocks permission_mode but allows model and effort", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))
		todo := Todo{
			ID:        "todo_ro_1",
			Origin:    "conv1",
			Title:     "Inspect code",
			CreatedAt: time.Now(),
		}
		ft.s.todos.mu.Lock()
		ft.s.todos.loaded = true
		ft.s.todos.todos = map[string]*Todo{todo.ID: &todo}
		ft.s.todos.activeBySession = map[[2]string]string{{"mac", "s1abcdef01"}: todo.ID}
		ft.s.todos.mu.Unlock()

		ft.s.tasks.mu.Lock()
		ft.s.tasks.plans["plan_ro"] = &TaskPlan{
			ID:           "plan_ro",
			Conversation: "conv1",
			State:        planStarted,
			Tasks:        []PlanTask{{TodoID: todo.ID, Readonly: true}},
		}
		ft.s.tasks.mu.Unlock()

		_, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": "s1abcdef", "permission_mode": "acceptEdits",
		})
		if err == nil || !strings.Contains(err.Error(), "read-only task") {
			t.Fatalf("readonly perm err = %v, want read-only task refusal", err)
		}
		if len(nodeCalls(ft.f)) != 0 || len(ft.s.pendingQuestions()) != 0 {
			t.Fatalf("readonly refusal triggered RPC or card")
		}

		// Model/effort-only change is still allowed on a read-only task.
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": true}, nil
			},
		}
		res, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": "s1abcdef", "effort": "high",
		})
		if err != nil || !strings.Contains(res, "effort=high") {
			t.Fatalf("readonly effort = %q, err = %v", res, err)
		}
	})

	t.Run("unresolved approval blocks permission_mode but allows model", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.sessions("mac", runningAgentSession("s1abcdef01"))
		waitIx := proto.Interaction{
			ID: "ix_wait", MachineID: "mac", SessionID: "s1abcdef01", Kind: proto.IxPermission,
			ToolName: "Bash", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
		}
		ft.s.fleet.mu.Lock()
		ft.s.ixMapLocked()["ix_wait"] = &pending{ix: waitIx, done: make(chan proto.Resolution, 1)}
		ft.s.fleet.mu.Unlock()

		_, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": "s1abcdef", "permission_mode": proto.ModeBypass,
		})
		if err == nil || !strings.Contains(err.Error(), "unresolved approval") {
			t.Fatalf("unresolved approval err = %v", err)
		}
		if len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("node called while approval unresolved")
		}
	})
}

func TestAgentModesPrivacySpaceAndTOCTOU(t *testing.T) {
	t.Run("enforces privacy space isolation and isolated-space confirmation", func(t *testing.T) {
		ft := agentModesRig(t)
		ft.f.hello.Assistant.MachineID = "server"
		ft.d.add("server", "Server")
		healthChat := proto.Session{
			SessionID: "health-chat", MachineID: "server", Cwd: "/home/me/.persona/health",
			Assistant: true, Space: SpaceHealth, Status: proto.StatusIdle, UpdatedAt: time.Now(),
		}
		ft.sessions("server", healthChat)

		healthAgent := runningAgentSession("h1abcdef01")
		healthAgent.Space = SpaceHealth
		defaultAgent := runningAgentSession("d1abcdef01")
		ft.sessions("mac", healthAgent, defaultAgent)

		// Default space cannot see or change a Health-space Agent.
		if _, err := runAgentModes(t, ft, "conv1", map[string]any{
			"machine": "mac", "session_id": healthAgent.SessionID, "model": "opus",
		}); err == nil || !strings.Contains(err.Error(), "no agent") {
			t.Fatalf("cross-space lookup err = %v, want no agent", err)
		}

		// Health space cannot see default-space Agent.
		if _, err := runAgentModes(t, ft, "health-chat", map[string]any{
			"machine": "mac", "session_id": defaultAgent.SessionID, "model": "opus",
		}); err == nil || !strings.Contains(err.Error(), "no agent") {
			t.Fatalf("health->default lookup err = %v, want no agent", err)
		}

		// Health space model-only change still requires confirmOutbound owner card.
		ft.f.nodes = map[string]nodeAnswer{
			DeviceSessionModes: func(string, json.RawMessage) (any, error) {
				return map[string]any{"ok": true, "changed": true}, nil
			},
		}
		done := make(chan struct {
			res string
			err error
		}, 1)
		go func() {
			res, err := runAgentModes(t, ft, "health-chat", map[string]any{
				"machine": "mac", "session_id": healthAgent.SessionID, "model": "opus",
			})
			done <- struct {
				res string
				err error
			}{res, err}
		}()
		ix := waitAgentModesCard(t, ft.s)
		if !ix.OwnerProposal || ix.ToolName != "agent_modes" {
			t.Fatalf("health outbound card = %+v", ix)
		}
		allow := true
		answerAgentModesCard(t, ft.s, ix.ID, proto.Resolution{Allow: &allow})
		out := <-done
		if out.err != nil || !strings.Contains(out.res, "model=opus") {
			t.Fatalf("health model change = %q, err = %v", out.res, out.err)
		}
	})

	t.Run("rechecks target after confirmation wait (TOCTOU)", func(t *testing.T) {
		ft := agentModesRig(t)
		s := runningAgentSession("s1abcdef01")
		ft.sessions("mac", s)

		done := make(chan struct {
			res string
			err error
		}, 1)
		go func() {
			res, err := runAgentModes(t, ft, "conv1", map[string]any{
				"machine": "mac", "session_id": "s1abcdef", "permission_mode": "acceptEdits",
			})
			done <- struct {
				res string
				err error
			}{res, err}
		}()

		ix := waitAgentModesCard(t, ft.s)
		// While the owner card is pending, the target stops.
		stopped := s
		stopped.Status = proto.StatusOffline
		ft.sessions("mac", stopped)

		allow := true
		answerAgentModesCard(t, ft.s, ix.ID, proto.Resolution{Allow: &allow})
		out := <-done
		if out.err == nil || !strings.Contains(out.err.Error(), "stopped or is offline") {
			t.Fatalf("TOCTOU stopped err = %v, want stopped refusal", out.err)
		}
		if len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("node called despite target stopping during approval wait")
		}
	})
}

var _ = devicewire.AssistantModeActor
