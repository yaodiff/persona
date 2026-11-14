package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// paseoHello is a runtime running Claude Code and other agents through their SDKs (Paseo, docs/integrations/paseo.md).
func paseoHello() map[string]any {
	return map[string]any{"runtime": "paseo", "name": "Paseo", "version": "test",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "rename": true, "close": true}}
}

const sdkSession = "dddddddd-0000-4000-8000-000000000001"

// freshOffline drops the cached scan of offline transcripts (20 s), so the next list sees the ones a test just wrote.
func freshOffline() {
	offlineCache.Lock()
	offlineCache.at, offlineCache.list = time.Time{}, nil
	offlineCache.Unlock()
}

// sdkTranscript writes the Claude transcript of session id in the test home: a prompt and an answer, padded past the
// size under which a transcript is not listed offline. Returns its path.
func sdkTranscript(t *testing.T, n *Node, id, answer string) string {
	t.Helper()
	t.Cleanup(freshOffline) // the scan is the package's: the next test must not list this test's transcript
	return writeTranscript(t, n.home, "/w/app", id, time.Now().Add(-time.Minute),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T10:00:00Z","cwd":"/w/app","message":{"role":"user","content":"fix the build"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-04T10:00:05Z","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"`+answer+`"}]}}`,
		strings.Repeat(" ", 4096))
}

// runtimeSessionCall drives the real RPC connection, so watch lifetimes are tested against ConnContext rather than a
// direct handler call's request context.
func runtimeSessionCall(t *testing.T, hub *runtimeHub, method string, params any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hub.nodePeer.Call(ctx, method, params, nil); err != nil {
		t.Fatal(err)
	}
}

// A runtime's session whose conversation is a Claude transcript here: listed once, as the runtime says (status,
// title, modes) with what the transcript says (the last words, its revision); its content, info and watch are the
// node's, from the transcript, and the runtime pushes only live text for it.
func TestRuntimeSessionInATranscript(t *testing.T) {
	n, hub := runtimeNode(t)
	path := sdkTranscript(t, n, sdkSession, "the build is fixed")
	host := dialRuntime(t, n, paseoHello())
	if f, _ := host.helloR["features"].([]any); !slices.Contains(f, any("transcript")) {
		t.Fatalf("features %v", host.helloR["features"])
	}
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a1", "transcript": sdkSession, "agent": "claude", "title": "Fix the build", "cwd": "/w/app",
		"status": "working", "permission_mode": "acceptEdits", "effort": "high", "space_id": "wks_1", "space_name": "app"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a1"]; return ok })
	freshOffline()
	got := listed(t, n)
	s := got["paseo-a1"]
	st, _ := os.Stat(path)
	if s.Title != "Fix the build" || s.Status != proto.StatusWorking || s.Agent != "claude" || s.AgentLabel != "" ||
		s.LastText != "the build is fixed" || s.Rev != st.Size() || s.PermissionMode != "acceptEdits" || s.Effort != "high" ||
		s.SourceKind != proto.SourceRuntime || s.SpaceKind != "paseo" {
		t.Fatalf("listed as %+v", s)
	}
	if _, twice := got[sdkSession]; twice {
		t.Fatal("the transcript's own row is listed beside the runtime's")
	}

	// The content is the transcript's: the runtime is not asked for it.
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "paseo-a1", "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items   []proto.Item `json:"items"`
		History string       `json:"history"`
	}
	_ = json.Unmarshal(raw, &page)
	if len(page.Items) != 2 || page.Items[1].Text != "the build is fixed" || page.History == "" {
		t.Fatalf("page %s", raw)
	}
	if len(host.got("session.items")) != 0 {
		t.Fatal("the runtime was asked for the transcript's content")
	}
	info, err := nodeCall(t, n, "session.info", map[string]any{"session_id": "paseo-a1"})
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	_ = json.Unmarshal(info, &in)
	if in["transcript_path"] != path || in["permission_mode"] != "acceptEdits" || in["effort"] != "high" || in["turns"] == nil {
		t.Fatalf("info %s", info)
	}

	// Watched: the node tails the transcript; the runtime only streams live text (transcript: true).
	runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a1", "since_seq": 2})
	waitFor(t, "the runtime told to stream", func() bool { return len(host.got("session.watch")) == 1 })
	var w map[string]any
	_ = json.Unmarshal(host.got("session.watch")[0], &w)
	if w["transcript"] != true {
		t.Fatalf("watch %v", w)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"and the tests pass"}]}}` + "\n")
	_ = f.Close()
	_ = host.peer.Notify("session.items", map[string]any{"session_id": "paseo-a1", "to_seq": 9, "history": "paseo:x",
		"items": []proto.Item{{ID: "9#0", Seq: 9, Role: "assistant", Kind: proto.KindText, Text: "from the runtime"}}})
	_ = host.peer.Notify("session.delta", map[string]any{"session_id": "paseo-a1", "text": "and the te"})
	waitFor(t, "the appended line pushed", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, raw := range hub.items {
			if strings.Contains(string(raw), "and the tests pass") {
				return true
			}
		}
		return false
	})
	waitFor(t, "the live text", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.deltas) == 1 })
	time.Sleep(100 * time.Millisecond)
	hub.mu.Lock()
	for _, raw := range hub.items {
		if strings.Contains(string(raw), "from the runtime") {
			hub.mu.Unlock()
			t.Fatal("the runtime's items for a transcript session reached the hub")
		}
	}
	hub.mu.Unlock()

	// Control stays the runtime's.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "paseo-a1", "text": "thanks", "client_msg_id": "m1"}); err != nil {
		t.Fatal(err)
	}
	if len(host.got("session.send")) != 1 {
		t.Fatal("the send did not reach the runtime")
	}
	runtimeSessionCall(t, hub, "session.unwatch", map[string]any{"session_id": "paseo-a1"})
	if len(host.got("session.unwatch")) != 1 || n.runtimes.isWatched("paseo-a1") {
		t.Fatal("unwatch not passed on")
	}
}

func TestRuntimeTranscriptWatchStopsWithItsConnection(t *testing.T) {
	for _, tc := range []struct {
		name  string
		moved bool
	}{
		{name: "initial watch"},
		{name: "runtime-only watch moved to a transcript", moved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, hub := runtimeNode(t)
			host := dialRuntime(t, n, paseoHello())
			var path string
			if !tc.moved {
				path = sdkTranscript(t, n, sdkSession, "ready")
			}
			list := map[string]any{"sessions": []map[string]any{{"session_id": "paseo-a1", "transcript": sdkSession, "status": "working"}}}
			_ = host.peer.Notify("runtime.sessions", list)
			waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a1"]; return ok })
			runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a1", "since_seq": 0})
			if tc.moved {
				path = sdkTranscript(t, n, sdkSession, "written now")
				_ = host.peer.Notify("runtime.sessions", list)
			}
			watchExists := func() bool {
				n.mu.Lock()
				defer n.mu.Unlock()
				_, ok := n.watches["paseo-a1"]
				return ok
			}
			waitFor(t, "the transcript watch", watchExists)
			hub.cancel()
			waitFor(t, "the watch to stop with its connection", func() bool { return !watchExists() })

			before := len(hub.items)
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
			_, _ = f.WriteString(`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"after the connection"}]}}` + "\n")
			_ = f.Close()
			time.Sleep(700 * time.Millisecond) // sleep-ok: a negative check, several 250 ms watch polls must pass with nothing pushed
			if after := len(hub.items); after != before {
				t.Fatalf("the watch kept pushing after its connection ended: items %d -> %d", before, after)
			}
		})
	}
}

// A runtime may list a session under the id of its Claude transcript (a session that ran in it before it came to
// Persona): one row, the transcript's content, the runtime's control.
func TestRuntimeSessionUnderItsTranscriptID(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "done")
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": sdkSession, "transcript": sdkSession, "agent": "claude", "status": "idle", "cwd": "/w/app"}}})
	waitFor(t, "listed by the runtime", func() bool { freshOffline(); return listed(t, n)[sdkSession].SourceKind == proto.SourceRuntime })
	if s := listed(t, n)[sdkSession]; s.Status != proto.StatusIdle || s.LastText != "done" {
		t.Fatalf("listed as %+v", s)
	}
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": sdkSession, "limit": 20})
	if err != nil || !strings.Contains(string(raw), `"done"`) {
		t.Fatalf("page %s %v", raw, err)
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "go on", "client_msg_id": "m2"}); err != nil {
		t.Fatal(err)
	}
	if len(host.got("session.send")) != 1 {
		t.Fatal("the send did not reach the runtime (a Claude session resumed in a terminal instead?)")
	}
}

// A transcript this node does not have (a config directory it does not know): the content stays the runtime's.
func TestRuntimeTranscriptNotHere(t *testing.T) {
	n, hub := runtimeNode(t)
	host := dialRuntime(t, n, paseoHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.items" {
			return map[string]any{"items": []proto.Item{{ID: "1#0", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "from the runtime"}},
				"history": "paseo:a2", "to_seq": 1}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a2", "transcript": "dddddddd-0000-4000-8000-0000000000ff", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a2"]; return ok })
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "paseo-a2", "limit": 20})
	if err != nil || !strings.Contains(string(raw), "from the runtime") {
		t.Fatalf("page %s %v", raw, err)
	}
	runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a2", "since_seq": 1})
	var w map[string]any
	_ = json.Unmarshal(host.got("session.watch")[0], &w)
	if w["transcript"] != nil || w["since_seq"] != float64(1) {
		t.Fatalf("watch %v", w)
	}
}

// A session started from the phone has no transcript until its first turn: the runtime's content first, then — the
// row names the transcript — the node's, the watch moved to it (another history: the apps take it again from the top).
func TestRuntimeTranscriptComesLater(t *testing.T) {
	n, hub := runtimeNode(t)
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-a3", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a3"]; return ok })
	runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a3", "since_seq": 0})
	path := sdkTranscript(t, n, sdkSession, "started")
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a3", "transcript": sdkSession, "status": "working"}}})
	waitFor(t, "the runtime told to stream only", func() bool {
		for _, raw := range host.got("session.watch") {
			if strings.Contains(string(raw), `"transcript":true`) {
				return true
			}
		}
		return false
	})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a2","timestamp":"2026-11-04T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"first turn"}]}}` + "\n")
	_ = f.Close()
	waitFor(t, "the transcript's line pushed", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, raw := range hub.items {
			if strings.Contains(string(raw), "first turn") && strings.Contains(string(raw), `"session_id":"paseo-a3"`) {
				return true
			}
		}
		return false
	})
}

// A subagent of such a session reads the transcript's subagent file.
func TestRuntimeTranscriptSubagent(t *testing.T) {
	n, _ := runtimeNode(t)
	path := sdkTranscript(t, n, sdkSession, "delegated")
	dir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "agent-x1.jsonl"), []byte(
		`{"type":"assistant","uuid":"s1","timestamp":"2026-11-04T10:00:07Z","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent says hi"}]}}`+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "agent-x1.meta.json"), []byte(`{"agentType":"Explore","description":"look around","toolUseId":"toolu_1"}`), 0o600)
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a4", "transcript": sdkSession, "status": "working"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a4"]; return ok })
	subs := listed(t, n)["paseo-a4"].Subagents
	if len(subs) != 1 || subs[0].SessionID != "paseo-a4@x1" {
		t.Fatalf("subagents %+v", subs)
	}
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "paseo-a4@x1", "limit": 20})
	if err != nil || !strings.Contains(string(raw), "subagent says hi") {
		t.Fatalf("subagent page %s %v", raw, err)
	}
	if len(host.got("session.items")) != 0 {
		t.Fatal("the runtime was asked for the subagent")
	}
}

// The background work a runtime lists for a session stays when its conversation is a transcript here, beside what the
// transcript lists: the agents a Paseo agent created are Paseo's to know, not Claude Code's. Their own rows name the
// session as their parent, and the list gives them its root (#4494).
func TestRuntimeTranscriptKeepsTheRuntimesBackground(t *testing.T) {
	n, _ := runtimeNode(t)
	t.Cleanup(freshOffline)
	now := time.Now().UTC().Format(time.RFC3339)
	writeTranscript(t, n.home, "/w/app", sdkSession, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"`+now+`","cwd":"/w/app","message":{"role":"user","content":"fix the build"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"`+now+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"make watch","run_in_background":true}}]}}`,
		`{"type":"user","uuid":"u2","timestamp":"`+now+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Command running in background with ID: b1. Output is being written to: /tmp/tasks/b1.output"}]}}`,
		strings.Repeat(" ", 4096))
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "paseo-a5", "transcript": sdkSession, "agent": "claude", "title": "Fix the build", "cwd": "/w/app",
			"status": "idle", "space_id": "prj_1", "space_name": "app",
			"background": []map[string]any{{"id": "paseo-b5", "kind": "agent", "description": "Review the fix"}}},
		{"session_id": "paseo-b5", "agent": "codex", "title": "Review the fix", "cwd": "/w/app", "status": "working",
			"space_id": "prj_1", "space_name": "app", "is_subagent": true, "parent_session_id": "paseo-a5"},
	}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-b5"]; return ok })
	got := listed(t, n)
	var tasks []string
	for _, b := range got["paseo-a5"].Background {
		tasks = append(tasks, b.Kind+" "+b.ID+" "+b.Description)
	}
	if !slices.Equal(tasks, []string{"shell b1 ", "agent paseo-b5 Review the fix"}) {
		t.Fatalf("background %q", tasks)
	}
	child := got["paseo-b5"]
	if !child.IsSubagent || child.ParentSessionID != "paseo-a5" || child.RootSessionID != "paseo-a5" ||
		child.SourceKind != proto.SourceRuntime || child.ParentThreadID != "" {
		t.Fatalf("the agent it created: %+v", child)
	}
	if parent := got["paseo-a5"]; parent.IsSubagent || parent.ParentSessionID != "" || parent.RootSessionID != "" {
		t.Fatalf("its creator: %+v", parent)
	}
}

// A runtime's session that runs as a 1M variant has the 1M window from its first reply, not from the first one past
// 200k: the transcript records the model without the suffix (#4496).
func TestRuntimeTranscriptWindowIsTheRunAsModels(t *testing.T) {
	n, _ := runtimeNode(t)
	t.Cleanup(freshOffline)
	writeTranscript(t, n.home, "/w/app", sdkSession, time.Now().Add(-time.Minute),
		`{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/w/app","message":{"role":"user","content":"fix the build"}}`,
		ctxReply("msg_1", 2, 80_000, 1_000), strings.Repeat(" ", 4096))
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "paseo-a6", "transcript": sdkSession, "agent": "claude", "model": "claude-opus-5-5[1m]", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a6"]; return ok })
	if s := listed(t, n)["paseo-a6"]; s.Model != "claude-opus-5-5[1m]" || s.ContextTokens != 81_002 || s.ContextWindow != 1_000_000 {
		t.Fatalf("listed with %q %d / %d", s.Model, s.ContextTokens, s.ContextWindow)
	}
}

// A runtime running several agents: each session's card is its own agent's (Claude Code's for a Claude session).
func TestRuntimeCardIsTheSessionsAgent(t *testing.T) {
	n, hub := runtimeNode(t)
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "paseo-c1", "agent": "claude", "status": "working"},
		{"session_id": "paseo-o1", "status": "working"},
	}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-o1"]; return ok })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var res proto.Resolution
	for _, id := range []string{"paseo-c1", "paseo-o1"} {
		if err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": id, "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}}, &res); err != nil {
			t.Fatal(err)
		}
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if a := hub.asked[0]; a.Agent != "" || a.AgentLabel != "" {
		t.Fatalf("a Claude session's card: %q %q", a.Agent, a.AgentLabel)
	}
	if a := hub.asked[1]; a.Agent != "paseo" || a.AgentLabel != "Paseo" {
		t.Fatalf("another agent's card: %q %q", a.Agent, a.AgentLabel)
	}
}

// What a runtime's session info says without a transcript: the counts the apps decode as numbers are there (zero).
func TestRuntimeSessionInfoCounts(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, dshHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-i1", "model": "m1", "cwd": "/w"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-i1"]; return ok })
	raw, err := nodeCall(t, n, "session.info", map[string]any{"session_id": "session-i1"})
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	for _, k := range []string{"context_tokens", "output_tokens", "compactions", "turns"} {
		if in[k] != float64(0) {
			t.Fatalf("%s: %v", k, in[k])
		}
	}
	if in["model"] != "m1" || in["cwd"] != "/w" {
		t.Fatalf("info %v", in)
	}
}

// A runtime may name a transcript before Claude Code has written it (its first turn just started): the content stays
// the runtime's until the file is there, then the session moves to it with the next list.
func TestRuntimeTranscriptWrittenLater(t *testing.T) {
	n, hub := runtimeNode(t)
	host := dialRuntime(t, n, paseoHello())
	list := map[string]any{"sessions": []map[string]any{{"session_id": "paseo-a5", "transcript": sdkSession, "status": "working"}}}
	_ = host.peer.Notify("runtime.sessions", list)
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a5"]; return ok })
	if n.runtimes.transcriptOf("paseo-a5") != "" {
		t.Fatal("a transcript not written yet counts")
	}
	runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a5", "since_seq": 0})
	var w map[string]any
	_ = json.Unmarshal(host.got("session.watch")[0], &w)
	if w["transcript"] != nil {
		t.Fatalf("watched as a transcript before there is one: %v", w)
	}
	path := sdkTranscript(t, n, sdkSession, "written now")
	_ = host.peer.Notify("runtime.sessions", list)
	waitFor(t, "moved to the transcript", func() bool { return n.runtimes.transcriptOf("paseo-a5") == sdkSession })
	waitFor(t, "the runtime told to stream only", func() bool {
		for _, raw := range host.got("session.watch") {
			if strings.Contains(string(raw), `"transcript":true`) {
				return true
			}
		}
		return false
	})
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"assistant","uuid":"a9","timestamp":"2026-11-04T10:00:09Z","message":{"role":"assistant","content":[{"type":"text","text":"next line"}]}}` + "\n")
	_ = f.Close()
	waitFor(t, "the transcript's line pushed", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, raw := range hub.items {
			if strings.Contains(string(raw), "next line") && strings.Contains(string(raw), `"session_id":"paseo-a5"`) {
				return true
			}
		}
		return false
	})
}

// A message the phone sends to such a session is found in its transcript (the runtime gives the line its own id):
// the line goes out with the message's client_msg_id, so the app's pending bubble becomes it.
func TestRuntimeTranscriptEcho(t *testing.T) {
	n, hub := runtimeNode(t)
	path := sdkTranscript(t, n, sdkSession, "ready")
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a6", "transcript": sdkSession, "agent": "claude", "status": "idle"}}})
	waitFor(t, "listed", func() bool { return n.runtimes.transcriptOf("paseo-a6") == sdkSession })
	runtimeSessionCall(t, hub, "session.watch", map[string]any{"session_id": "paseo-a6", "since_seq": 3})
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "paseo-a6", "text": "run the tests please", "client_msg_id": "cm-echo"}); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(`{"type":"user","uuid":"u9","timestamp":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","message":{"role":"user","content":"run the tests please"}}` + "\n")
	_ = f.Close()
	waitFor(t, "the line pushed with the message's id", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, raw := range hub.items {
			if strings.Contains(string(raw), "run the tests please") && strings.Contains(string(raw), `"client_msg_id":"cm-echo"`) {
				return true
			}
		}
		return false
	})
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "paseo-a6", "limit": 20})
	if err != nil || !strings.Contains(string(raw), `"client_msg_id":"cm-echo"`) {
		t.Fatalf("page %s %v", raw, err)
	}
}

// A runtime's session in a Claude transcript shows the agent's plan, as the same transcript in a terminal does
// (#5421 set Session.plan on the pane and background rows only).
func TestRuntimeTranscriptShowsThePlan(t *testing.T) {
	n, _ := runtimeNode(t)
	t.Cleanup(freshOffline)
	list := `{"todos":[{"content":"a","status":"completed","activeForm":"Doing a"},{"content":"b","status":"in_progress","activeForm":"Reproducing the crash"},{"content":"c","status":"pending","activeForm":"Doing c"}]}`
	writeTranscript(t, n.home, "/w/app", sdkSession, time.Now().Add(-time.Minute),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-06T10:00:00Z","cwd":"/w/app","message":{"role":"user","content":"fix the crash"}}`,
		`{"type":"assistant","uuid":"c1","timestamp":"2026-11-06T10:00:02Z","message":{"role":"assistant","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"TodoWrite","input":`+list+`}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-06T10:00:03Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}}`,
		strings.Repeat(" ", 4096))
	host := dialRuntime(t, n, paseoHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": "paseo-a1", "transcript": sdkSession, "agent": "claude", "title": "Fix the crash", "cwd": "/w/app", "status": "working"}}})
	waitFor(t, "listed with its plan", func() bool {
		freshOffline()
		s, ok := listed(t, n)["paseo-a1"]
		return ok && s.Plan != nil && s.Plan.Done == 1 && s.Plan.Total == 3 && s.Plan.Current == "Reproducing the crash"
	})
}

// A runtime's session whose conversation is a Claude transcript here can be branched (#6298): the copy is of that
// transcript, under a new Claude id written into every line, named after the session, listed offline at once (the node
// keeps state for it, so the copy is not taken for a script's run); not while its turn is being written, nor a session
// without a transcript here.
func TestRuntimeSessionForksFromItsTranscript(t *testing.T) {
	n, _ := runtimeNode(t)
	path := sdkTranscript(t, n, sdkSession, "the build is fixed")
	host := dialRuntime(t, n, paseoHello())
	row := func(status string) map[string]any {
		return map[string]any{"session_id": "paseo-a1", "transcript": sdkSession, "agent": "claude", "title": "Fix the build", "cwd": "/w/app",
			"status": status, "model": "claude-opus-5-5"}
	}
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{row("idle"),
		{"session_id": "paseo-g1", "agent": "paseo", "title": "Grok", "cwd": "/w/app", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-g1"]; return ok })
	got := listed(t, n)
	if !got["paseo-a1"].CanFork || got["paseo-g1"].CanFork {
		t.Fatalf("can_fork: transcript %v, none %v", got["paseo-a1"].CanFork, got["paseo-g1"].CanFork)
	}
	id, err := n.fork(context.Background(), "paseo-a1", "")
	if err != nil || !validID(id) || id == sdkSession {
		t.Fatalf("fork: %q %v", id, err)
	}
	b, err := os.ReadFile(filepath.Join(filepath.Dir(path), id+".jsonl"))
	if err != nil || strings.Contains(string(b), sdkSession) || !strings.Contains(string(b), "the build is fixed") {
		t.Fatalf("the copy: %v\n%s", err, b)
	}
	if !strings.Contains(string(b), "分支 · Fix the build") {
		t.Fatalf("named after the session:\n%s", b)
	}
	if st, ok := n.state.get(id); !ok || st.Cwd != "/w/app" || st.Model != "claude-opus-5-5" {
		t.Fatalf("its state: %+v %v", st, ok)
	}
	waitFor(t, "the branch listed", func() bool { s, ok := listed(t, n)[id]; return ok && s.Status == proto.StatusOffline })
	if _, err := n.fork(context.Background(), "paseo-g1", ""); rpcCode(err) != "fork_unsupported" {
		t.Fatalf("a session without a transcript: %v", err)
	}
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{row("working")}})
	waitFor(t, "working", func() bool { return listed(t, n)["paseo-a1"].Status == proto.StatusWorking })
	if _, err := n.fork(context.Background(), "paseo-a1", ""); rpcCode(err) != "fork_turn_running" {
		t.Fatalf("its turn still being written: %v", err)
	}
}

// session.seen goes to a runtime that keeps an unread mark of its own (caps.seen, #6299); any other is answered here.
func TestRuntimeSeenGoesToARuntimeThatAsks(t *testing.T) {
	for _, asks := range []bool{true, false} {
		n, _ := runtimeNode(t)
		hello := paseoHello()
		if asks {
			hello["caps"].(map[string]any)["seen"] = true
		}
		host := dialRuntime(t, n, hello)
		_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-a1", "status": "done"}}})
		waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-a1"]; return ok })
		if _, err := nodeCall(t, n, "session.seen", map[string]any{"session_id": "paseo-a1"}); err != nil {
			t.Fatalf("seen (asks %v): %v", asks, err)
		}
		if got := len(host.got("session.seen")); got != map[bool]int{true: 1, false: 0}[asks] {
			t.Fatalf("told %d times (asks %v)", got, asks)
		}
	}
	n, _ := runtimeNode(t)
	if _, err := nodeCall(t, n, "session.seen", map[string]any{"session_id": "11111111-2222-4333-8444-555555555555"}); err != nil {
		t.Fatalf("a session of no runtime: %v", err)
	}
}
