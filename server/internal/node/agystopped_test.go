package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The user's Close on an agy pane lists its conversation as stopped by them (#14520), the plain close (session.close)
// and archive and close's (session.retire with close_terminal) alike: the companion's row for it, listed once the pane
// is gone, is offline with the user's stop cause and keeps what the companion lets it do, where the companion says
// done. Another conversation's row is left as the companion says it, and the mark goes once a pane shows the
// conversation again.
func TestAgyCloseListsItsConversationStopped(t *testing.T) {
	for _, how := range []struct {
		method string
		params map[string]any
	}{
		{"session.close", map[string]any{"session_id": agyTestRow}},
		{"session.retire", map[string]any{"session_id": agyTestRow, "close_terminal": true}},
	} {
		t.Run(how.method, func(t *testing.T) {
			n, herdr, home := herdrNode(t)
			const (
				convRow    = "agy-" + agyTestID
				otherRow   = "agy-" + agyOtherTestID
				secondPane = "w1:p2"
			)
			n.runtimes = newRuntimeRegistry()
			runtime := n.runtimes.add(nil)
			runtime.ready, runtime.hello.Runtime = true, agyAgent
			caps := &proto.SessionCapabilities{Read: true, Send: true}
			n.runtimes.setSessions(runtime, []proto.Session{
				{SessionID: convRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone, Title: "Release notes", Capabilities: caps},
				{SessionID: otherRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone, Title: "Login page", Capabilities: caps},
			})
			rows := func() map[string]proto.Session {
				out := map[string]proto.Session{}
				for _, row := range listSessions(t, n) {
					if row.Agent == agyAgent {
						out[row.SessionID] = row
					}
				}
				return out
			}
			herdr.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
			if got := rows(); len(got) != 2 || got[agyTestRow].SessionID == "" || got[otherRow].SessionID == "" {
				t.Fatalf("before the close: %v", got)
			}
			if _, err := request(t, n, how.method, how.params); err != nil {
				t.Fatal(err)
			}
			if calls := herdr.trace("pane.close", "tab.close"); len(calls) != 1 {
				t.Fatalf("the pane was not closed: %v", calls)
			}

			herdr.reply("session.snapshot", snapshot(nil, nil, nil, nil)) // the pane is gone
			got := rows()
			if c := got[convRow]; c.Status != proto.StatusOffline || c.Capabilities == nil || c.Capabilities.StopCause != proto.StopUser ||
				!c.Capabilities.Send || !c.Capabilities.Read {
				t.Fatalf("the closed conversation: %+v %+v", c, c.Capabilities)
			}
			if c := got[otherRow]; c.Status != proto.StatusDone || c.Capabilities == nil || c.Capabilities.StopCause != "" {
				t.Fatalf("another conversation: %+v %+v", c, c.Capabilities)
			}
			if caps.StopCause != "" {
				t.Fatal("the companion's own capabilities changed")
			}

			// Opened in another pane, it runs again: once that pane is gone as well, its row is as the companion says.
			herdr.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(secondPane, home, agyTestID)}, nil))
			rows()
			herdr.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			if c := rows()[convRow]; c.Status != proto.StatusDone || c.Capabilities == nil || c.Capabilities.StopCause != "" {
				t.Fatalf("after another pane ran it: %+v %+v", c, c.Capabilities)
			}
		})
	}
}

// What the conversation does in the minute after the close is the close's own; after it, work, a change or a live
// pane says it runs again, and its mark goes. A pane the close did not take away is one after that minute.
func TestAgyStopMarkGoesWhenItRunsAgain(t *testing.T) {
	const conv = "agy-" + agyTestID
	for _, c := range []struct {
		name    string
		ago     time.Duration
		status  string
		changed bool
		live    map[string]string
		stopped bool
	}{
		{"done after the close", 0, proto.StatusDone, false, nil, true},
		{"working in the close's minute", 0, proto.StatusWorking, false, nil, true},
		{"changed in the close's minute", 0, proto.StatusDone, true, nil, true},
		{"its pane still there in that minute", 0, proto.StatusDone, false, map[string]string{agyTestRow: conv}, true},
		{"working after it", 2 * time.Minute, proto.StatusWorking, false, nil, false},
		{"waiting on the user after it", 2 * time.Minute, proto.StatusBlocked, false, nil, false},
		{"changed after it", 2 * time.Minute, proto.StatusDone, true, nil, false},
		{"its pane still there after it", 2 * time.Minute, proto.StatusDone, false, map[string]string{agyTestRow: conv}, false},
		{"another pane shows it", 0, proto.StatusDone, false, map[string]string{"agy-w1-p2": conv}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			closed := time.Now().Add(-c.ago)
			n := &Node{agyStopped: map[string]agyStop{agyTestID: {row: agyTestRow, at: closed}}}
			row := proto.Session{SessionID: conv, Agent: agyAgent, Status: c.status, UpdatedAt: closed.Add(-time.Hour)}
			if c.changed {
				row.UpdatedAt = time.Now()
			}
			list := []proto.Session{row}
			n.markAgyStopped(list, c.live)
			stopped := list[0].Status == proto.StatusOffline && list[0].Capabilities != nil && list[0].Capabilities.StopCause == proto.StopUser
			if _, kept := n.agyStopped[agyTestID]; stopped != c.stopped || kept != c.stopped {
				t.Fatalf("stopped %v (row %+v), mark kept %v; want %v", stopped, list[0], kept, c.stopped)
			}
		})
	}
}

// Antigravity keeps no archive of its own: the node says so for an agy row no runtime takes, a pane's or a
// conversation's, instead of refusing its id (#14520).
func TestNativeArchiveOfAnAgyRow(t *testing.T) {
	n := &Node{}
	for _, id := range []string{agyTestRow, "agy-" + agyTestID} {
		got, err := n.nativeArchive(t.Context(), id, true)
		if err != nil || got.Native != nativeArchiveUnsupported {
			t.Fatalf("%s: %+v %v", id, got, err)
		}
	}
	if _, err := n.nativeArchive(t.Context(), "not a session", true); err == nil {
		t.Fatal("an invalid id was taken")
	}
}

func agyCompanionStoppedRow(sessions []proto.Session, id string) (proto.Session, bool) {
	for _, s := range sessions {
		if s.SessionID == id {
			return s, true
		}
	}
	return proto.Session{}, false
}

func isUserStoppedRow(s proto.Session) bool {
	return s.Status == proto.StatusOffline && s.Capabilities != nil && s.Capabilities.StopCause == proto.StopUser
}

func setRuntimeAnswer(host *fakeRuntime, fn func(string, json.RawMessage) (any, error)) {
	host.mu.Lock()
	host.answer = fn
	host.mu.Unlock()
}

func dialAgyCompanionForStoppedTest(t *testing.T, n *Node, convRow string, updatedAt time.Time) *fakeRuntime {
	t.Helper()
	hello := assistantRuntimeHello()
	hello["runtime"], hello["agent"], hello["launcher"] = agyAgent, agyAgent, agyAgent
	caps := hello["caps"].(map[string]any)
	caps["queue_send_now"] = true
	host := dialRuntime(t, n, hello)
	if err := host.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{{
			"session_id": convRow,
			"cwd":        n.home,
			"status":     proto.StatusDone,
			"title":      "Stopped conversation",
			"updated_at": updatedAt.UTC().Format(time.RFC3339Nano),
			"capabilities": map[string]any{
				"read":             true,
				"send":             true,
				"a2a_wake_on_send": true,
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agy companion session listed", func() bool {
		return n.runtimes.ownerOf(convRow) != nil
	})
	return host
}

// Continuing a user-stopped agy-<conversation> companion session within agyStopGrace must clear n.agyStopped so the
// resumed turn is neither masked as StatusOffline (StopUser) nor permanently trapped offline when it finishes inside
// the grace window. Failed/refused sends and newer concurrent closes must keep the stop mark intact.
func TestAgyCompanionSendClearsStoppedMark(t *testing.T) {
	const convRow = "agy-" + agyTestID

	t.Run("session.send unmasks working turn even when runtime.sessions races ahead and stays unmasked on fast completion", func(t *testing.T) {
		n, hub := runtimeNode(t)
		closedAt := time.Now()
		n.mu.Lock()
		n.agyStopped = map[string]agyStop{agyTestID: {row: agyTestRow, at: closedAt}}
		n.mu.Unlock()

		host := dialAgyCompanionForStoppedTest(t, n, convRow, closedAt.Add(-time.Minute))
		waitFor(t, "initial stopped row pushed to hub", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && isUserStoppedRow(row)
		})

		// Companion pushes StatusWorking inside agyStopGrace BEFORE session.send returns.
		workingUpdatedAt := closedAt.Add(5 * time.Second)
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			if method != "session.send" {
				return map[string]any{"ok": true}, nil
			}
			if err := host.peer.Notify("runtime.sessions", map[string]any{
				"sessions": []map[string]any{{
					"session_id": convRow,
					"cwd":        n.home,
					"status":     proto.StatusWorking,
					"title":      "Stopped conversation",
					"rev":        2,
					"updated_at": workingUpdatedAt.UTC().Format(time.RFC3339Nano),
				}},
			}); err != nil {
				return nil, err
			}
			// Wait on the RPC goroutine without calling testing.T helpers off the test goroutine.
			deadline := time.Now().Add(5 * time.Second)
			for {
				if owner := n.runtimes.ownerOf(convRow); owner != nil {
					if row, ok := owner.row(convRow); ok && row.Status == proto.StatusWorking {
						break
					}
				}
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("timed out waiting for in-flight runtime.sessions StatusWorking")
				}
				<-time.After(5 * time.Millisecond)
			}
			return map[string]any{"ok": true}, nil
		})

		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": convRow, "text": "continue"}); err != nil {
			t.Fatalf("session.send: %v", err)
		}

		// Clearing agyStopped must schedule a fresh hub publish so the active turn becomes visible immediately.
		waitFor(t, "hub observes unmasked StatusWorking", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Status == proto.StatusWorking && (row.Capabilities == nil || row.Capabilities.StopCause == "")
		})

		// If the turn finishes inside agyStopGrace (UpdatedAt <= closedAt + agyStopGrace), it must show StatusDone,
		// not fall back into permanent StatusOffline (StopUser).
		doneUpdatedAt := closedAt.Add(10 * time.Second)
		if err := host.peer.Notify("runtime.sessions", map[string]any{
			"sessions": []map[string]any{{
				"session_id": convRow,
				"cwd":        n.home,
				"status":     proto.StatusDone,
				"title":      "Stopped conversation",
				"rev":        3,
				"updated_at": doneUpdatedAt.UTC().Format(time.RFC3339Nano),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "hub observes unmasked StatusDone", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Status == proto.StatusDone && (row.Capabilities == nil || row.Capabilities.StopCause == "")
		})
	})

	t.Run("failed send, refused A2A send, and concurrent newer close preserve agyStopped", func(t *testing.T) {
		n, hub := runtimeNode(t)
		closedAt := time.Now()
		n.mu.Lock()
		n.agyStopped = map[string]agyStop{agyTestID: {row: agyTestRow, at: closedAt}}
		n.mu.Unlock()

		host := dialAgyCompanionForStoppedTest(t, n, convRow, closedAt.Add(-time.Minute))
		waitFor(t, "initial stopped row pushed to hub", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && isUserStoppedRow(row) && row.Capabilities.A2AWakeOnSend
		})

		// 1. Companion RPC error keeps agyStopped.
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			if method == "session.send" {
				return nil, errors.New("companion busy")
			}
			return map[string]any{"ok": true}, nil
		})
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": convRow, "text": "retry"}); err == nil {
			t.Fatal("expected session.send error")
		}
		row, ok := agyCompanionStoppedRow(listSessions(t, n), convRow)
		if !ok || !isUserStoppedRow(row) {
			t.Fatalf("failed send cleared agyStopped: %+v %+v", row, row.Capabilities)
		}

		// 2. Refused A2A send (via session.a2a.send with a2a_wake_on_send: true) must be blocked by StopUser and
		// never reach the companion or clear agyStopped.
		sendsBeforeA2A := len(host.got("session.send"))
		if _, err := nodeCall(t, n, "session.a2a.send", map[string]any{
			"session_id":       convRow,
			"text":             "a2a ping",
			"a2a_wake_on_send": true,
		}); rpcCode(err) != "a2a_session_busy" {
			t.Fatalf("expected a2a_session_busy, got %v", err)
		}
		if got := len(host.got("session.send")); got != sendsBeforeA2A {
			t.Fatalf("refused A2A send forwarded %d extra session.send call(s)", got-sendsBeforeA2A)
		}
		row, ok = agyCompanionStoppedRow(listSessions(t, n), convRow)
		if !ok || !isUserStoppedRow(row) {
			t.Fatalf("refused A2A send cleared agyStopped: %+v %+v", row, row.Capabilities)
		}

		// 3. A newer stop recorded while session.send is in flight must not be cleared by that send finishing.
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			if method == "session.send" {
				n.mu.Lock()
				n.agyStopped[agyTestID] = agyStop{row: agyTestRow, at: time.Now().Add(time.Second)}
				n.mu.Unlock()
			}
			return map[string]any{"ok": true}, nil
		})
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": convRow, "text": "in flight"}); err != nil {
			t.Fatalf("session.send: %v", err)
		}
		row, ok = agyCompanionStoppedRow(listSessions(t, n), convRow)
		if !ok || !isUserStoppedRow(row) {
			t.Fatalf("in-flight send cleared a newer agyStopped mark: %+v %+v", row, row.Capabilities)
		}
	})

	t.Run("session.queue.send_now clears agyStopped on success and keeps it on error", func(t *testing.T) {
		n, hub := runtimeNode(t)
		closedAt := time.Now()
		n.mu.Lock()
		n.agyStopped = map[string]agyStop{agyTestID: {row: agyTestRow, at: closedAt}}
		n.mu.Unlock()

		host := dialAgyCompanionForStoppedTest(t, n, convRow, closedAt.Add(-time.Minute))
		waitFor(t, "initial stopped row pushed to hub", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && isUserStoppedRow(row)
		})

		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			if method == "session.queue.send_now" {
				return nil, errors.New("nothing queued")
			}
			return map[string]any{"ok": true}, nil
		})
		if _, err := nodeCall(t, n, "session.queue.send_now", map[string]any{"session_id": convRow}); err == nil {
			t.Fatal("expected session.queue.send_now error")
		}
		if row, ok := agyCompanionStoppedRow(listSessions(t, n), convRow); !ok || !isUserStoppedRow(row) {
			t.Fatalf("failed send_now cleared agyStopped: %+v", row)
		}

		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		})
		if _, err := nodeCall(t, n, "session.queue.send_now", map[string]any{"session_id": convRow}); err != nil {
			t.Fatalf("session.queue.send_now: %v", err)
		}
		waitFor(t, "hub observes unmasked row after queue.send_now", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Status == proto.StatusDone && (row.Capabilities == nil || row.Capabilities.StopCause == "")
		})
	})

	t.Run("assistantSendRuntime and assistantCompactRuntime clear agyStopped on accepted prompt and keep it on error", func(t *testing.T) {
		n, hub := runtimeNode(t)
		assistantHub(hub)
		closedAt := time.Now()
		cwd := filepath.Join(n.home, ".persona", "assistant")
		n.state.put(convRow, sessionState{Launcher: agyAgent, Cwd: cwd})
		n.mu.Lock()
		n.agyStopped = map[string]agyStop{agyTestID: {row: agyTestRow, at: closedAt}}
		n.mu.Unlock()

		host := dialAgyCompanionForStoppedTest(t, n, convRow, closedAt.Add(-time.Minute))
		waitFor(t, "initial stopped row pushed to hub", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && isUserStoppedRow(row)
		})

		// Empty input (!hasInput) must not clear agyStopped.
		if _, err := n.assistantSendRuntime(context.Background(), n.runtimes.ownerOf(convRow), convRow, cwd, agyAgent, "", nil, "", false, false, false, nil, 0); err != nil {
			t.Fatalf("empty assistantSendRuntime: %v", err)
		}
		if row, ok := agyCompanionStoppedRow(listSessions(t, n), convRow); !ok || !isUserStoppedRow(row) {
			t.Fatalf("empty assistantSendRuntime cleared agyStopped: %+v", row)
		}

		// Real prompt clears agyStopped and unmasks StatusWorking immediately.
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		})
		if _, err := n.assistantSendRuntime(context.Background(), n.runtimes.ownerOf(convRow), convRow, cwd, agyAgent, "follow up", nil, "", false, false, false, nil, 0); err != nil {
			t.Fatalf("assistantSendRuntime: %v", err)
		}
		waitFor(t, "hub observes unmasked StatusWorking after assistantSendRuntime", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Status == proto.StatusWorking && (row.Capabilities == nil || row.Capabilities.StopCause == "")
		})

		// Re-mark stopped BEFORE pushing StatusDone so hub.lastList() observes StatusOffline (StopUser) before compact.
		n.mu.Lock()
		n.agyStopped[agyTestID] = agyStop{row: agyTestRow, at: time.Now()}
		n.mu.Unlock()
		if err := host.peer.Notify("runtime.sessions", map[string]any{
			"sessions": []map[string]any{{
				"session_id": convRow,
				"cwd":        cwd,
				"status":     proto.StatusDone,
				"title":      "Stopped conversation",
				"rev":        4,
				"updated_at": closedAt.Add(15 * time.Second).UTC().Format(time.RFC3339Nano),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "hub observes re-stopped row before compact", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && isUserStoppedRow(row)
		})

		// Failed compact must keep agyStopped.
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			if method == "session.send" {
				return nil, errors.New("compact rejected")
			}
			return map[string]any{"ok": true}, nil
		})
		if err := n.assistantCompactRuntime(context.Background(), convRow, false); err == nil {
			t.Fatal("expected assistantCompactRuntime error")
		}
		if row, ok := agyCompanionStoppedRow(listSessions(t, n), convRow); !ok || !isUserStoppedRow(row) {
			t.Fatalf("failed assistantCompactRuntime cleared agyStopped: %+v", row)
		}

		// Accepted compact clears agyStopped and republishes unmasked StatusDone with CompactionRunning.
		setRuntimeAnswer(host, func(method string, _ json.RawMessage) (any, error) {
			return map[string]any{"ok": true}, nil
		})
		if err := n.assistantCompactRuntime(context.Background(), convRow, false); err != nil {
			t.Fatalf("assistantCompactRuntime: %v", err)
		}
		waitFor(t, "hub observes unmasked row after assistantCompactRuntime", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Status == proto.StatusDone &&
				(row.Capabilities == nil || row.Capabilities.StopCause == "") &&
				row.Compaction != nil && row.Compaction.State == proto.CompactionRunning
		})
		if row, ok := agyCompanionStoppedRow(listSessions(t, n), convRow); !ok || isUserStoppedRow(row) {
			t.Fatalf("listSessions remained stopped after assistantCompactRuntime: %+v", row)
		}

		// Settle the background awaitRuntimeCompaction goroutine cleanly before t.Cleanup.
		if err := host.peer.Notify("runtime.sessions", map[string]any{
			"sessions": []map[string]any{{
				"session_id": convRow,
				"cwd":        cwd,
				"status":     proto.StatusDone,
				"title":      "Stopped conversation",
				"rev":        5,
				"updated_at": closedAt.Add(20 * time.Second).UTC().Format(time.RFC3339Nano),
			}},
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "compaction settled before cleanup", func() bool {
			row, ok := agyCompanionStoppedRow(hub.lastList(), convRow)
			return ok && row.Compaction != nil && row.Compaction.State == proto.CompactionDone
		})
	})
}
