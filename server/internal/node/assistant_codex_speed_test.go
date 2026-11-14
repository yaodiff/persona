package node

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexAssistantStartSpeedPrecedesFirstTurn(t *testing.T) {
	for _, tc := range []struct {
		name, tier, refuse string
	}{
		{name: "Fast and effort stay independent", tier: "priority"},
		{name: "untouched start has no speed override"},
		{name: "a native refusal prevents the first turn", tier: "priority", refuse: "Fast unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			probedLaunchers(t, map[string]string{"cx_test": "codex"})
			withFakeHub(t, n)
			cwd := t.TempDir()
			p, c, f := newFakeCodex(t, n, cwd)
			f.results["thread/start"] = map[string]any{
				"thread": map[string]any{"id": testThread}, "model": "example", "serviceTier": "default",
			}
			f.results["model/list"] = map[string]any{"data": []any{map[string]any{
				"model": "example", "serviceTiers": []proto.ServiceTier{{ID: "priority", Name: "Fast"}},
			}}}
			f.results["turn/start"] = map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress"}}
			if err := c.start(context.Background(), "", cwd, false); err != nil {
				t.Fatal(err)
			}
			// Adopt an already-open warm thread: even then the explicit choice is applied before its first turn.
			p.model = modelExecution{Effort: "high"}
			p.cmd = &exec.Cmd{} // the fake has pipes but no OS process; stop still exercises registry cleanup
			p.fingerprint = n.executionFingerprint("cx_test", p.model)
			n.spare = p
			if tc.refuse != "" {
				f.mu.Lock()
				f.errors = map[string]string{"thread/settings/update": tc.refuse}
				f.mu.Unlock()
			}
			id, err := n.assistantSend(context.Background(), "", cwd, "cx_test", "hello", nil, proto.ModeBypass, "high",
				modelExecution{Effort: "high", ServiceTier: tc.tier})
			if tc.refuse != "" {
				if err == nil || len(f.sent("turn/start")) != 0 {
					t.Fatalf("refused speed delivered a turn: id %q err %v", id, err)
				}
				if refusalCode(err) != "assistant_service_tier_refused" {
					t.Fatalf("native refusal lost its code: %v", err)
				}
				n.mu.Lock()
				_, registered := n.procs[testThread]
				spare := n.spare
				n.mu.Unlock()
				if registered || spare != nil {
					t.Fatalf("refused start left a process: registered %t spare %p", registered, spare)
				}
				if _, known := n.state.get(testThread); known {
					t.Fatal("refused start persisted a conversation")
				}
				for _, row := range n.assistantSessions() {
					if row.SessionID == testThread {
						t.Fatalf("refused start is still listed: %+v", row)
					}
				}
				return
			}
			if err != nil || id != testThread {
				t.Fatalf("start: id %q err %v", id, err)
			}
			updates := f.sent("thread/settings/update")
			if tc.tier == "" {
				if len(updates) != 0 {
					t.Fatalf("untouched start changed native speed: %v", updates)
				}
			} else {
				if len(updates) != 1 {
					t.Fatalf("speed updates: %v", updates)
				}
				var params map[string]any
				_ = json.Unmarshal(updates[0].Params, &params)
				if len(params) != 2 || params["threadId"] != testThread || params["serviceTier"] != "priority" {
					t.Fatalf("speed changed other settings: %v", params)
				}
			}
			turns := f.sent("turn/start")
			if len(turns) != 1 {
				t.Fatalf("first turns: %v", turns)
			}
			var turn map[string]any
			_ = json.Unmarshal(turns[0].Params, &turn)
			if turn["effort"] != "high" {
				t.Fatalf("speed changed effort: %v", turn)
			}
			f.mu.Lock()
			speedAt, turnAt := -1, -1
			for i, m := range f.got {
				if m.Method == "thread/settings/update" {
					speedAt = i
				}
				if m.Method == "turn/start" && turnAt < 0 {
					turnAt = i
				}
			}
			f.mu.Unlock()
			if tc.tier != "" && (speedAt < 0 || speedAt > turnAt) {
				t.Fatalf("speed was not acknowledged before the first turn: update %d turn %d", speedAt, turnAt)
			}
		})
	}
}

func TestAssistantServiceTierRefusesBeforeNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, id, code string
		guest          bool
	}{
		{name: "a resumed guest cannot lose the tier with its options", id: testThread, code: "assistant_service_tier_start_only", guest: true},
		{name: "a non-Codex start is coded and never spawns", code: "assistant_service_tier_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			probedLaunchers(t, map[string]string{"claude": "claude"})
			withFakeHub(t, n)
			if tc.guest {
				n.state.put(tc.id, sessionState{Launcher: "claude", Guest: []string{"lark_agent_reply"}})
			}
			id, err := n.assistantSend(context.Background(), tc.id, t.TempDir(), "claude", "hello", nil, proto.ModeBypass, "",
				modelExecution{ServiceTier: "priority"})
			if id != "" || refusalCode(err) != tc.code {
				t.Fatalf("start: id %q code %q err %v, want %q", id, refusalCode(err), err, tc.code)
			}
			n.mu.Lock()
			processes := len(n.procs)
			n.mu.Unlock()
			if processes != 0 {
				t.Fatalf("refused request spawned %d processes", processes)
			}
		})
	}
}

func TestCodexAssistantSpeed(t *testing.T) {
	n := codexTestNode(t)
	p, c, f := newFakeCodex(t, n, t.TempDir())
	f.results["thread/start"] = map[string]any{
		"thread": map[string]any{"id": testThread},
		"model":  "example", "serviceTier": "default",
	}
	f.results["model/list"] = map[string]any{"data": []any{map[string]any{
		"model": "example", "id": "alias",
		"serviceTiers": []proto.ServiceTier{{ID: "priority", Name: "Fast"}},
	}}}
	if err := c.start(context.Background(), "", p.cwd, false); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.procs[testThread] = p
	n.mu.Unlock()
	rows := n.assistantSessions()
	if len(rows) != 1 || rows[0].ServiceTier != "default" || len(rows[0].ServiceTiers) != 2 {
		t.Fatalf("assistant speed missing: %+v", rows)
	}
	_, err := n.handle(context.Background(), "session.speed", json.RawMessage(`{"session_id":"`+testThread+`","service_tier":"priority"}`))
	if err != nil {
		t.Fatal(err)
	}
	requests := f.sent("thread/settings/update")
	if len(requests) != 1 {
		t.Fatalf("speed requests: %v", requests)
	}
	var params map[string]any
	if err := json.Unmarshal(requests[0].Params, &params); err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 || params["serviceTier"] != "priority" || params["threadId"] != testThread {
		t.Fatalf("speed changed other settings: %v", params)
	}
	row := proto.Session{Model: "example"}
	c.speedRow(&row)
	if row.ServiceTier != "priority" {
		t.Fatal("Fast was not acknowledged")
	}
	c.notification("thread/settings/updated", json.RawMessage(`{"threadId":"`+testThread+`","threadSettings":{"model":"example","serviceTier":null}}`))
	c.speedRow(&row)
	if row.ServiceTier != "default" {
		t.Fatal("native Standard was not synchronized")
	}
	f.mu.Lock()
	f.errors = map[string]string{"thread/settings/update": "speed unavailable"}
	f.mu.Unlock()
	if err := c.setSpeed(context.Background(), "priority"); err == nil {
		t.Fatal("native refusal became success")
	}
	c.speedRow(&row)
	if row.ServiceTier != "default" {
		t.Fatal("refused speed was advertised")
	}
}
