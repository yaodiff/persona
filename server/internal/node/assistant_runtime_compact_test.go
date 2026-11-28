package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const runtimeCompactTestID = "agy-11111111-2222-3333-4444-555555555555"

func runtimeCompactFixture(t *testing.T) (*Node, *fakeRuntime, func(string, int64, string)) {
	t.Helper()
	n, hub := runtimeNode(t)
	hub.mu.Lock()
	hub.answer = func(method string, _ json.RawMessage) (any, error, bool) {
		return map[string]string{"text": "- Example snapshot"}, nil, method == "assistant.context"
	}
	hub.mu.Unlock()
	hello := assistantRuntimeHello()
	hello["runtime"], hello["name"], hello["launcher"] = "agy", "Antigravity", "agy"
	host := dialRuntime(t, n, hello)
	cwd := filepath.Join(n.home, ".persona", "assistant")
	push := func(status string, rev int64, turnError string) {
		t.Helper()
		row := proto.Session{SessionID: runtimeCompactTestID, Cwd: cwd, Status: status, Rev: rev, TurnError: turnError}
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{row}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "runtime row updated", func() bool {
			got, _, ok := n.runtimes.rowOf(runtimeCompactTestID)
			return ok && got.Status == status && got.Rev == rev && got.TurnError == turnError
		})
	}
	push(proto.StatusIdle, 1, "")
	return n, host, push
}

func TestAssistantRuntimeCompaction(t *testing.T) {
	for _, test := range []struct {
		name, turnError, state string
		sawWorking             bool
	}{
		{"done", "", proto.CompactionDone, true},
		{"fast", "", proto.CompactionDone, false},
		{"failed", "Compaction failed", proto.CompactionFailed, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, host, push := runtimeCompactFixture(t)
			id := runtimeCompactTestID
			n.mu.Lock()
			n.ctxSent = map[string]map[string]string{id: {"snapshot": "previous context"}}
			n.mu.Unlock()
			params := map[string]any{"session_id": id}
			if _, err := nodeCall(t, n, "assistant.compact", params); err != nil {
				t.Fatal(err)
			}
			sends := host.got("session.send")
			want := map[string]any{"session_id": id, "text": "/compact", "assistant": true}
			var got map[string]any
			if len(sends) != 1 || json.Unmarshal(sends[0], &got) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("runtime sends: %s", sends)
			}
			if began := listed(t, n)[id].Compaction; began == nil || began.State != proto.CompactionRunning || began.StartedAt.IsZero() {
				t.Fatalf("running compaction: %+v", began)
			}
			if _, err := nodeCall(t, n, "assistant.compact", params); refusalCode(err) != "compacting" {
				t.Fatalf("duplicate compaction: %v", err)
			}
			n.sweepCompactions()
			if test.sawWorking {
				push(proto.StatusWorking, 2, "")
			}
			push(proto.StatusDone, 2, test.turnError)
			waitFor(t, "compaction settled", func() bool {
				got := listed(t, n)[id].Compaction
				return got != nil && got.State == test.state && got.FinishedAt != nil && got.Error == test.turnError
			})
			n.mu.Lock()
			_, sent := n.ctxSent[id]
			n.mu.Unlock()
			if sent {
				t.Fatal("the next message would omit the context snapshot")
			}
		})
	}
}

func TestAssistantRuntimeCompactionRefusals(t *testing.T) {
	n, host, push := runtimeCompactFixture(t)
	id := runtimeCompactTestID
	if err := n.assistantCompact(t.Context(), id, true); refusalCode(err) != "not_running" {
		t.Fatalf("only running: %v", err)
	}
	push(proto.StatusWorking, 2, "")
	if err := n.assistantCompact(t.Context(), id, false); refusalCode(err) != "busy" {
		t.Fatalf("busy: %v", err)
	}
	push(proto.StatusIdle, 3, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := n.assistantCompact(ctx, id, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if sends := host.got("session.send"); len(sends) != 0 {
		t.Fatalf("refused compaction sent input: %s", sends)
	}
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: "not_found", Message: "Conversation not found"}
	}
	host.mu.Unlock()
	if err := n.assistantCompact(t.Context(), id, false); refusalCode(err) != "not_found" {
		t.Fatalf("runtime refusal: %v", err)
	}
	if got := listed(t, n)[id].Compaction; got == nil || got.State != proto.CompactionFailed || got.FinishedAt == nil {
		t.Fatalf("failed compaction: %+v", got)
	}
}

func TestAssistantRuntimeCompactionSerializesSends(t *testing.T) {
	n, host, push := runtimeCompactFixture(t)
	id := runtimeCompactTestID
	sendStarted, releaseSend := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseSend) })
	defer release()
	host.mu.Lock()
	host.answer = func(method string, raw json.RawMessage) (any, error) {
		if method == "session.send" && strings.Contains(string(raw), `"text":"First message"`) {
			close(sendStarted)
			<-releaseSend
		}
		return nil, nil
	}
	host.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	send := func(text string) error {
		_, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": id, "text": text})
		return err
	}
	sendResult, compactResult := make(chan error, 1), make(chan error, 1)
	go func() { sendResult <- send("First message") }()
	select {
	case <-sendStarted:
	case err := <-sendResult:
		t.Fatalf("send ended before reaching the runtime: %v", err)
	case <-ctx.Done():
		t.Fatal("send did not reach the runtime")
	}
	go func() { compactResult <- n.assistantCompact(ctx, id, false) }()
	select {
	case err := <-compactResult:
		t.Fatalf("compaction overtook an unacknowledged send: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	if err := <-sendResult; err != nil {
		t.Fatal(err)
	}
	if err := <-compactResult; refusalCode(err) != "busy" {
		t.Fatalf("compaction after send, before a runtime list update: %v", err)
	}
	if got := host.got("session.send"); len(got) != 1 || !strings.Contains(string(got[0]), "Example snapshot") {
		t.Fatalf("first runtime send: %s", got)
	}
	push(proto.StatusDone, 2, "")
	if err := n.assistantCompact(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := send("While compacting"); refusalCode(err) != "compacting" {
		t.Fatalf("send during compaction: %v", err)
	}
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": id}); err != nil {
		t.Fatalf("interrupt during compaction: %v", err)
	}
	if got := host.got("session.interrupt"); len(got) != 1 {
		t.Fatalf("runtime interrupts: %s", got)
	}
	push(proto.StatusDone, 3, "")
	waitFor(t, "compaction settled", func() bool {
		got := listed(t, n)[id].Compaction
		return got != nil && got.State == proto.CompactionDone
	})
	if err := send("After compaction"); err != nil {
		t.Fatal(err)
	}
	if got := host.got("session.send"); len(got) != 3 || !strings.Contains(string(got[2]), "Example snapshot") {
		t.Fatalf("the next turn must resend the snapshot: %s", got)
	}
}

func TestAssistantRuntimeCompactionQueued(t *testing.T) {
	n, host, push := runtimeCompactFixture(t)
	id := runtimeCompactTestID
	_, commit := n.turnContext(id, contextAnswer{Text: "- Example snapshot"})
	commit()
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) {
		return map[string]bool{"queued": true}, nil
	}
	host.mu.Unlock()
	err := n.assistantCompact(t.Context(), id, false)
	if refusalCode(err) != "compaction_queued" {
		t.Fatalf("queued compaction: %v", err)
	}
	push(proto.StatusDone, 2, "")
	if got := listed(t, n)[id].Compaction; got == nil || got.State != proto.CompactionFailed || got.Error != err.Error() {
		t.Fatalf("the queued command was not tracked as failed: %+v", got)
	}
	if block, _ := n.turnContext(id, contextAnswer{Text: "- Example snapshot"}); block == "" {
		t.Fatal("the queued command might run, so the snapshot must be sent again")
	}
}

func TestAssistantRuntimeCompactionClaimsUnlistedSession(t *testing.T) {
	n, _ := runtimeNode(t)
	hello := assistantRuntimeHello()
	hello["runtime"], hello["name"], hello["launcher"] = "agy", "Antigravity", "agy"
	host := dialRuntime(t, n, hello)
	id := runtimeCompactTestID
	n.state.put(id, sessionState{Cwd: filepath.Join(n.home, ".persona", "assistant"), Launcher: "agy"})
	if err := n.assistantCompact(t.Context(), id, true); refusalCode(err) != "not_running" {
		t.Fatalf("only running: %v", err)
	}
	if len(host.got("session.items")) != 0 || len(host.got("session.send")) != 0 {
		t.Fatal("only-running compaction called the runtime")
	}
	if err := n.assistantCompact(t.Context(), id, false); err != nil {
		t.Fatal(err)
	}
	if len(host.got("session.items")) != 1 || len(host.got("session.send")) != 1 {
		t.Fatal("compaction did not claim and send to the unlisted session")
	}
}

func TestAssistantRuntimeCompactionCancelsPendingSend(t *testing.T) {
	n, host, _ := runtimeCompactFixture(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) {
		close(started)
		<-release
		return nil, nil
	}
	host.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- n.assistantCompact(ctx, runtimeCompactTestID, false) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("runtime did not receive the command")
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled send: %v", err)
	}
	if got := listed(t, n)[runtimeCompactTestID].Compaction; got == nil || got.State != proto.CompactionFailed || got.FinishedAt == nil {
		t.Fatalf("canceled compaction: %+v", got)
	}
}

func TestAssistantRuntimeHandoff(t *testing.T) {
	n, host, _ := runtimeCompactFixture(t)
	host.mu.Lock()
	host.answer = func(string, json.RawMessage) (any, error) {
		return map[string]any{"items": []proto.Item{
			{Seq: 1, Kind: proto.KindText, Role: "user", Text: "<persona-context kind=\"snapshot\">Snapshot</persona-context>\n\nQuestion"},
			{Seq: 2, Kind: proto.KindToolResult, Role: "assistant", Text: "Tool output"},
			{Seq: 3, Kind: proto.KindText, Role: "assistant", Text: "Answer"},
			{Seq: 4, Kind: proto.KindText, Role: "user", Text: "<persona-context>Internal context</persona-context>"},
		}}, nil
	}
	host.mu.Unlock()
	raw, err := nodeCall(t, n, "assistant.handoff", map[string]any{"session_id": runtimeCompactTestID})
	if err != nil {
		t.Fatal(err)
	}
	var got Handoff
	want := Handoff{Recent: []HandoffTurn{{Role: "user", Text: "Question"}, {Role: "assistant", Text: "Answer"}}}
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("handoff %s: %v", raw, err)
	}
	pages := host.got("session.items")
	var page struct {
		SessionID string `json:"session_id"`
		Limit     int    `json:"limit"`
	}
	if len(pages) != 1 || json.Unmarshal(pages[0], &page) != nil || page.SessionID != runtimeCompactTestID || page.Limit != 400 {
		t.Fatalf("runtime pages: %s", pages)
	}
}

func TestAssistantRuntimeCompactionSettledByRuntimeEventDespiteUnknownStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventErr  string
		wantState string
	}{
		{name: "done", eventErr: "", wantState: proto.CompactionDone},
		{name: "failed", eventErr: "upstream compaction failed", wantState: proto.CompactionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, hub := runtimeNode(t)
			hub.mu.Lock()
			hub.answer = func(method string, _ json.RawMessage) (any, error, bool) {
				return map[string]string{"text": "- Example snapshot"}, nil, method == "assistant.context"
			}
			hub.mu.Unlock()
			hello := assistantRuntimeHello()
			hello["runtime"], hello["name"], hello["launcher"] = "agy", "Antigravity", "agy"
			host := dialRuntime(t, n, hello)
			otherHello := assistantRuntimeHello()
			otherHello["runtime"], otherHello["name"], otherHello["launcher"] = "other", "Other", "other"
			other := dialRuntime(t, n, otherHello)

			id := runtimeCompactTestID
			cwd := filepath.Join(n.home, ".persona", "assistant")
			push := func(status string, rev int64) {
				t.Helper()
				row := proto.Session{SessionID: id, Cwd: cwd, Status: status, Rev: rev}
				if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{row}}); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "runtime row updated", func() bool {
					got, _, ok := n.runtimes.rowOf(id)
					return ok && got.Status == status && got.Rev == rev
				})
			}
			push(proto.StatusIdle, 1)

			if _, err := nodeCall(t, n, "assistant.compact", map[string]any{"session_id": id}); err != nil {
				t.Fatal(err)
			}
			// Simulate conversation_summaries.db lagging behind (summary_behind -> StatusUnknown, unchanged Rev).
			push(proto.StatusUnknown, 1)
			if !n.runtimes.compacting(id) {
				t.Fatal("compaction should remain running while only StatusUnknown arrives")
			}

			// An unrelated runtime connection must not be able to settle this compaction.
			if err := other.peer.Notify("session.compaction.settled", map[string]any{"session_id": id, "error": "spoofed"}); err != nil {
				t.Fatal(err)
			}
			// Synchronize with other connection by pushing an unrelated row on it, then verify compaction is still running.
			if err := other.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{{SessionID: "other-1", Status: proto.StatusIdle}}}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "other runtime list processed", func() bool {
				_, _, ok := n.runtimes.rowOf("other-1")
				return ok
			})
			if !n.runtimes.compacting(id) {
				t.Fatal("unrelated runtime settled active compaction")
			}

			payload := map[string]any{"session_id": id}
			if tc.eventErr != "" {
				payload["error"] = tc.eventErr
			}
			if err := host.peer.Notify("session.compaction.settled", payload); err != nil {
				t.Fatal(err)
			}

			waitFor(t, "compaction settled via runtime event", func() bool {
				got := listed(t, n)[id].Compaction
				return got != nil && got.State == tc.wantState && got.FinishedAt != nil && got.Error == tc.eventErr
			})
			waitFor(t, "hub received terminal compaction state via sessions.changed", func() bool {
				for _, row := range hub.lastList() {
					if row.SessionID == id && row.Compaction != nil && row.Compaction.State == tc.wantState && row.Compaction.Error == tc.eventErr {
						return true
					}
				}
				return false
			})

			// Subsequent lagging runtime.sessions rows must not overwrite the settled compaction state.
			push(proto.StatusUnknown, 1)
			got := listed(t, n)[id].Compaction
			if got == nil || got.State != tc.wantState || got.Error != tc.eventErr {
				t.Fatalf("compaction state changed after trailing StatusUnknown: %+v", got)
			}

			// Sending a follow-up message must no longer be refused with compacting.
			if _, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": id, "text": "After settled"}); err != nil {
				t.Fatalf("assistant.send after settlement: %v", err)
			}
		})
	}
}

func TestSilentRuntimeCompactionSettledByEventDespiteUnknownStatus(t *testing.T) {
	n, host, push := runtimeCompactFixture(t)
	id := runtimeCompactTestID
	_, rtHost, ok := n.runtimes.rowOf(id)
	if !ok {
		t.Fatal("missing runtime owner")
	}
	unlock := n.typing.lock(id)
	compaction, err := n.startRuntimeCompactionLocked(t.Context(), rtHost, id, true, true, false)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	push(proto.StatusUnknown, 1, "")
	if err := host.peer.Notify("session.compaction.settled", map[string]any{"session_id": id}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-compaction.done:
	case <-time.After(5 * time.Second):
		t.Fatal("silent compaction did not unblock on session.compaction.settled")
	}
	if failure := compaction.failureMessage(); failure != "" {
		t.Fatalf("unexpected failure message: %q", failure)
	}
	if n.runtimes.compacting(id) {
		t.Fatal("compaction still reported running after settlement")
	}
}
