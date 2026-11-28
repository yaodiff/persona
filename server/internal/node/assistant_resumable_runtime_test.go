package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestResumeOnlyPersistedRuntimeSessionAwayBeforeConnect(t *testing.T) {
	n, _ := runtimeNode(t)
	const sid = "agy-00000000-0000-0000-0000-000000000001"
	n.state.put(sid, sessionState{
		Launcher:  agyRuntimeID,
		Cwd:       identity.AssistantDir(n.home),
		StartedAt: time.Now().UTC(),
	})

	_, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":  sid,
		"text":        "handover after restart",
		"resume_only": true,
	})
	if err == nil {
		t.Fatal("expected runtime_away error while runtime is disconnected, got nil")
	}
	if rpcCode(err) != "runtime_away" {
		t.Fatalf("err = %v (code %q), want runtime_away", err, rpcCode(err))
	}
	if strings.Contains(err.Error(), "is gone") {
		t.Fatalf("must not misclassify disconnected runtime session as gone: %v", err)
	}
}

func TestResumeOnlyRuntimeSessionAwayBeforeInitialSessionsList(t *testing.T) {
	n, _ := runtimeNode(t)
	const sid = "agy-00000000-0000-0000-0000-000000000001"
	cwd := identity.AssistantDir(n.home)
	n.state.put(sid, sessionState{
		Launcher:  agyRuntimeID,
		Cwd:       cwd,
		StartedAt: time.Now().UTC(),
	})

	rt := dialRuntime(t, n, agyAssistantHello())
	// Simulate agy right after hello, before its initial disk scan completes: session.items would fail with not_found.
	rt.answer = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session.items":
			return nil, &rpc.Error{Code: codeNotFound, Message: "not indexed yet"}
		case "session.send":
			return map[string]any{"ok": true}, nil
		}
		return nil, nil
	}

	_, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":  sid,
		"text":        "handover during cold scan",
		"resume_only": true,
	})
	if err == nil || rpcCode(err) != "runtime_away" {
		t.Fatalf("before initial runtime.sessions: err = %v (code %q), want runtime_away", err, rpcCode(err))
	}
	if got := len(rt.got("session.items")); got != 0 {
		t.Fatalf("probed runtime with session.items %d times before initial sessions list; want 0", got)
	}

	// Once the runtime pushes its initial session list, the same resume_only send succeeds.
	row := proto.Session{SessionID: sid, Cwd: cwd, Status: proto.StatusIdle, Launcher: agyRuntimeID}
	if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{row}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "runtime listed session", func() bool {
		_, _, ok := n.runtimes.rowOf(sid)
		return ok
	})

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id":  sid,
		"text":        "handover after initial scan",
		"resume_only": true,
	}); err != nil {
		t.Fatalf("after initial runtime.sessions: %v", err)
	}
	if got := len(rt.got("session.send")); got != 1 {
		t.Fatalf("session.send calls = %d, want 1", got)
	}
}

func TestResumeOnlyRuntimeSessionProbesReadyRuntimeOutsideRecentList(t *testing.T) {
	const sid = "agy-11111111-2222-3333-4444-555555555555"

	t.Run("claimed when ready runtime still holds older session", func(t *testing.T) {
		n, _ := runtimeNode(t)
		cwd := identity.AssistantDir(n.home)
		n.state.put(sid, sessionState{Launcher: agyRuntimeID, Cwd: cwd, StartedAt: time.Now().UTC()})
		rt := dialRuntime(t, n, agyAssistantHello())
		rt.answer = func(method string, params json.RawMessage) (any, error) {
			switch method {
			case "session.items":
				return map[string]any{"items": []any{}}, nil
			case "session.send":
				return map[string]any{"ok": true}, nil
			}
			return nil, nil
		}
		if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial empty runtime list", func() bool {
			for _, h := range n.runtimes.ready() {
				if h.listedSessions() {
					return true
				}
			}
			return false
		})

		if _, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id":  sid,
			"text":        "older runtime conversation",
			"resume_only": true,
		}); err != nil {
			t.Fatalf("expected probe+claim to succeed, got %v", err)
		}
		if got := len(rt.got("session.send")); got != 1 {
			t.Fatalf("session.send calls = %d, want 1", got)
		}
	})

	t.Run("gone when ready runtime confirms not_found", func(t *testing.T) {
		n, _ := runtimeNode(t)
		cwd := identity.AssistantDir(n.home)
		n.state.put(sid, sessionState{Launcher: agyRuntimeID, Cwd: cwd, StartedAt: time.Now().UTC()})
		rt := dialRuntime(t, n, agyAssistantHello())
		rt.answer = func(method string, params json.RawMessage) (any, error) {
			if method == "session.items" {
				return nil, &rpc.Error{Code: codeNotFound, Message: "deleted"}
			}
			return nil, nil
		}
		if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial empty runtime list", func() bool {
			for _, h := range n.runtimes.ready() {
				if h.listedSessions() {
					return true
				}
			}
			return false
		})

		_, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id":  sid,
			"text":        "deleted runtime conversation",
			"resume_only": true,
		})
		if err == nil || !strings.Contains(err.Error(), "is gone") {
			t.Fatalf("err = %v, want is gone", err)
		}
	})

	t.Run("runtime_away when ready runtime probe fails transiently", func(t *testing.T) {
		n, _ := runtimeNode(t)
		cwd := identity.AssistantDir(n.home)
		n.state.put(sid, sessionState{Launcher: agyRuntimeID, Cwd: cwd, StartedAt: time.Now().UTC()})
		rt := dialRuntime(t, n, agyAssistantHello())
		rt.answer = func(method string, params json.RawMessage) (any, error) {
			if method == "session.items" {
				return nil, errors.New("temporary backend error")
			}
			return nil, nil
		}
		if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "initial empty runtime list", func() bool {
			for _, h := range n.runtimes.ready() {
				if h.listedSessions() {
					return true
				}
			}
			return false
		})

		_, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id":  sid,
			"text":        "transient probe failure",
			"resume_only": true,
		})
		if err == nil || rpcCode(err) != "runtime_away" {
			t.Fatalf("err = %v (code %q), want runtime_away", err, rpcCode(err))
		}
	})
}

func TestResumeOnlyWaitsForStateLoadBeforeDeciding(t *testing.T) {
	n, _ := runtimeNode(t)
	const sid = "agy-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	record := sessionState{Launcher: agyRuntimeID, Cwd: identity.AssistantDir(n.home), StartedAt: time.Now().UTC()} // the real clock, not the bubble's
	synctest.Test(t, func(t *testing.T) {
		// The load begins inside the bubble, so a check waiting on it is durably blocked and synctest.Wait returns.
		n.state.beginLoad()
		done := make(chan error, 1)
		go func() { done <- n.checkAssistantResumable(context.Background(), sid, "") }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("decided before the state loaded: %v", err)
		default:
		}
		n.state.merge(map[string]sessionState{sid: record})
		n.state.endLoad()
		if err := <-done; err == nil || rpcCode(err) != "runtime_away" {
			t.Fatalf("err = %v (code %q), want runtime_away after state load", err, rpcCode(err))
		}
	})
}

func TestResumeOnlyStillReportsGoneForTrulyMissingSessions(t *testing.T) {
	n, _ := runtimeNode(t)
	for _, sid := range []string{"missing", "11111111-2222-3333-4444-555555555555"} {
		_, err := nodeCall(t, n, "assistant.send", map[string]any{
			"session_id":  sid,
			"text":        "hello",
			"resume_only": true,
		})
		if err == nil || !strings.Contains(err.Error(), "is gone") {
			t.Fatalf("sid=%s: err = %v, want is gone", sid, err)
		}
	}
}
