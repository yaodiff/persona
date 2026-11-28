package node

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Native Antigravity companion sessions (agy-<uuid>) and other non-hex runtime session IDs must be classified by
// runtime ownership (no_terminal for session.stop, not_retirable for session.retire / assistant conversations) rather
// than rejected up front with "invalid session id" (#16231).
func TestStopAndRetireClassifyNonHexRuntimeSessions(t *testing.T) {
	n, _ := runtimeNode(t)

	agy := dialRuntime(t, n, map[string]any{
		"runtime": "agy",
		"name":    "Antigravity",
		"agent":   "agy",
		"version": "test",
		"caps":    map[string]any{"start": true, "send": true, "interrupt": true, "close": true, "assistant": true},
	})
	dsh := dialRuntime(t, n, dshHello())
	paseo := dialRuntime(t, n, map[string]any{
		"runtime": "paseo",
		"name":    "Paseo",
		"version": "test",
		"caps":    map[string]any{"start": true, "send": true, "interrupt": true, "close": true},
	})

	const (
		agyID     = "agy-11111111-2222-4333-8444-555555555555"
		agyAsstID = "agy-33333333-4444-4555-8666-777777777777"
		dshID     = "session-22222222-3333-4444-8555-666666666666"
		paseoID   = "paseo-agent-42"
	)

	notFoundOther := func(owned ...string) func(string, json.RawMessage) (any, error) {
		set := map[string]bool{}
		for _, id := range owned {
			set[id] = true
		}
		return func(method string, params json.RawMessage) (any, error) {
			if method == "session.items" {
				var p struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(params, &p)
				if !set[p.SessionID] {
					return nil, &rpc.Error{Code: codeNotFound, Message: "session not found"}
				}
				return map[string]any{"items": []any{}, "to_seq": 0}, nil
			}
			return map[string]any{"ok": true}, nil
		}
	}
	agy.answer = notFoundOther(agyID, agyAsstID)
	dsh.answer = notFoundOther(dshID)
	paseo.answer = notFoundOther(paseoID)

	asstDir := filepath.Join(n.dir, ".persona", "assistant")
	if err := agy.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []proto.Session{
			{SessionID: agyID, Title: "AGY task", Cwd: "/tmp/work", Status: proto.StatusWorking},
			{SessionID: agyAsstID, Title: "AGY assistant", Cwd: asstDir, Status: proto.StatusIdle},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dsh.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []proto.Session{
			{SessionID: dshID, Title: "DSH task", Cwd: "/tmp/work", Status: proto.StatusIdle},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := paseo.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []proto.Session{
			{SessionID: paseoID, Title: "Paseo task", Cwd: "/tmp/work", Status: proto.StatusDone},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Mark the assistant session in state so runtimeAssistant(agyAsstID) is true.
	n.state.put(agyAsstID, sessionState{Launcher: "agy", Cwd: asstDir, IsMain: true})
	// Also record state for agyID to verify a refused session.stop does not mutate its StopCause.
	n.state.put(agyID, sessionState{Launcher: "agy", Cwd: "/tmp/work"})

	waitFor(t, "runtime sessions owned by node", func() bool {
		h1, _, k1 := n.runtimes.belongs(agyID)
		h2, _, k2 := n.runtimes.belongs(dshID)
		h3, _, k3 := n.runtimes.belongs(paseoID)
		return h1 != nil && k1 && h2 != nil && k2 && h3 != nil && k3
	})

	for _, tc := range []struct {
		id        string
		closeable bool
	}{
		{id: agyID},
		{id: dshID},
		{id: paseoID, closeable: true},
	} {
		id := tc.id
		t.Run("stop/"+id, func(t *testing.T) {
			_, err := nodeCall(t, n, "session.stop", map[string]any{"session_id": id})
			if code, _ := refusalOf(t, err); code != noTerminal {
				t.Fatalf("session.stop(%s) = code %q (err=%v), want %q", id, code, err, noTerminal)
			}
		})
		t.Run("stop_retire/"+id, func(t *testing.T) {
			_, err := nodeCall(t, n, "session.stop", map[string]any{"session_id": id, "retire": true})
			if code, _ := refusalOf(t, err); code != notRetirable {
				t.Fatalf("session.stop(retire=true, %s) = code %q (err=%v), want %q", id, code, err, notRetirable)
			}
		})
		t.Run("retire/"+id, func(t *testing.T) {
			raw, err := nodeCall(t, n, "session.retire", map[string]any{"session_id": id, "close_terminal": true})
			var res struct {
				Closed bool `json:"closed"`
			}
			if tc.closeable {
				if err == nil {
					err = json.Unmarshal(raw, &res)
				}
				if err != nil || !res.Closed {
					t.Fatalf("session.retire(%s) = %v, %v, want closed", id, res, err)
				}
				if got := len(paseo.got("session.close")); got != 1 {
					t.Fatalf("runtime session.close calls = %d, want 1", got)
				}
				return
			}
			if code, _ := refusalOf(t, err); code != notRetirable {
				t.Fatalf("session.retire(%s) = code %q (err=%v), want %q", id, code, err, notRetirable)
			}
		})
	}

	if got := n.stopCause(agyID); got != proto.StopUnknown {
		t.Fatalf("refused session.stop mutated StopCause on runtime session: got %s, want unknown", got)
	}

	t.Run("runtime_assistant_refused_as_not_retirable", func(t *testing.T) {
		_, err := nodeCall(t, n, "session.stop", map[string]any{"session_id": agyAsstID})
		if code, _ := refusalOf(t, err); code != notRetirable || !strings.Contains(err.Error(), "assistant") {
			t.Fatalf("session.stop(%s) = code %q (err=%v), want %q assistant refusal", agyAsstID, code, err, notRetirable)
		}
	})

	t.Run("close_reaches_agy_runtime", func(t *testing.T) {
		if _, err := nodeCall(t, n, "session.close", map[string]any{"session_id": agyID}); err != nil {
			t.Fatalf("session.close(%s) failed: %v", agyID, err)
		}
		got := agy.got("session.close")
		if len(got) != 1 || !strings.Contains(string(got[0]), agyID) {
			t.Fatalf("agy runtime session.close calls = %s, want 1 call for %s", got, agyID)
		}
	})

	t.Run("malformed_ids_stay_invalid", func(t *testing.T) {
		for _, bad := range []string{"", "../escape", "bad id"} {
			_, err := nodeCall(t, n, "session.stop", map[string]any{"session_id": bad})
			if err == nil || !strings.Contains(err.Error(), "invalid session id") {
				t.Fatalf("session.stop(%q) = %v, want invalid session id", bad, err)
			}
		}
	})
}

func TestRetireRuntimeSessionRefusesDeclaredTerminal(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := dialRuntime(t, n, map[string]any{
		"runtime": "paseo",
		"name":    "Paseo",
		"version": "test",
		"caps":    map[string]any{"start": true, "send": true, "interrupt": true, "close": true},
	})
	const id = "paseo-terminal-session"
	if err := rt.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []proto.Session{{SessionID: id, Title: "Terminal runtime", Cwd: "/work", Status: proto.StatusIdle}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the runtime row", func() bool {
		h, _, known := n.runtimes.belongs(id)
		return h != nil && known
	})
	h, _, _ := n.runtimes.belongs(id)
	canClose := true
	h.mu.Lock()
	for i := range h.sessions {
		if h.sessions[i].SessionID == id {
			h.sessions[i].Capabilities = &proto.SessionCapabilities{Terminal: true, Close: &canClose}
		}
	}
	h.mu.Unlock()

	if _, err := nodeCall(t, n, "session.retire", map[string]any{"session_id": id, "close_terminal": true}); err == nil {
		t.Fatal("a runtime session declaring a terminal was retired")
	} else if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("session.retire code = %q (err=%v), want %q", code, err, notRetirable)
	}
	if got := rt.got("session.close"); len(got) != 0 {
		t.Fatalf("runtime session.close calls = %s, want none", got)
	}
}

// An older runtime session that dropped off runtime.sessions is still resolved via connected runtimes on session.stop:
// claimed sessions answer no_terminal, while a prefix-matched runtime's not_found surfaces as typed not_found (#16231).
func TestStopResolvesUnlistedRuntimeSession(t *testing.T) {
	n, _ := runtimeNode(t)

	const (
		unlistedOwned   = "agy-44444444-5555-4666-8777-888888888888"
		unlistedMissing = "agy-99999999-8888-4777-8666-555555555555"
	)

	agy := dialRuntime(t, n, map[string]any{
		"runtime": "agy",
		"name":    "Antigravity",
		"agent":   "agy",
		"version": "test",
		"caps":    map[string]any{"start": true, "send": true, "interrupt": true, "close": true},
	})
	agy.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.items" {
			var p struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(params, &p)
			if p.SessionID == unlistedOwned {
				return map[string]any{"items": []any{}, "to_seq": 0}, nil
			}
			return nil, &rpc.Error{Code: codeNotFound, Message: "Antigravity session not found"}
		}
		return map[string]any{"ok": true}, nil
	}

	// Verify neither ID is currently in belongs() before the stop call.
	if h, _, known := n.runtimes.belongs(unlistedOwned); h != nil || known {
		t.Fatalf("precondition failed: %s already belongs to runtime", unlistedOwned)
	}

	_, err := nodeCall(t, n, "session.stop", map[string]any{"session_id": unlistedOwned})
	if code, _ := refusalOf(t, err); code != noTerminal {
		t.Fatalf("unlisted owned session.stop = code %q (err=%v), want %q", code, err, noTerminal)
	}

	// Now session.close on that claimed session reaches the runtime.
	if _, err := nodeCall(t, n, "session.close", map[string]any{"session_id": unlistedOwned}); err != nil {
		t.Fatalf("session.close on claimed unlisted session failed: %v", err)
	}
	if got := agy.got("session.close"); len(got) != 1 || !strings.Contains(string(got[0]), unlistedOwned) {
		t.Fatalf("agy session.close calls = %s, want 1 call for %s", got, unlistedOwned)
	}

	_, err = nodeCall(t, n, "session.stop", map[string]any{"session_id": unlistedMissing})
	if code, _ := refusalOf(t, err); code != codeNotFound {
		t.Fatalf("unlisted missing session.stop = code %q (err=%v), want %q", code, err, codeNotFound)
	}
	var re *rpc.Error
	if !errors.As(err, &re) || !strings.Contains(string(re.Data), `"confirmed_missing":true`) {
		t.Fatalf("unlisted missing session.stop data = %s (err=%v), want confirmed_missing=true", re.Data, err)
	}
}
