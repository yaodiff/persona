package node

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

type inputDriver struct {
	*fakeRuntime
	binding string
}

func inputRuntime(t *testing.T, n *Node, name string, capable bool) *inputDriver {
	t.Helper()
	return &inputDriver{fakeRuntime: dialRuntime(t, n, map[string]any{"runtime": name, "launcher": name, "instance": name + "-instance", "caps": map[string]bool{"input": capable, "start": true}})}
}

// Obtain the proof from a real node -> runtime start, as the SDK does.
func startInputRuntime(t *testing.T, n *Node, name string) *inputDriver {
	t.Helper()
	rt := inputRuntime(t, n, name, true)
	rt.mu.Lock()
	rt.answer = func(m string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		if m != "session.start" || json.Unmarshal(raw, &p) != nil {
			return nil, errors.New("expected session.start")
		}
		return map[string]any{"session": proto.Session{SessionID: p.SessionID, Status: proto.StatusWorking}}, nil
	}
	rt.mu.Unlock()
	s, _, err := n.runtimeStartOn(t.Context(), n.runtimes.launcherRuntime(name), runtimeStartParams{Launcher: name, SessionID: "toy-session"})
	if err != nil || s.SessionID != "toy-session" {
		t.Fatalf("runtime start: %v", err)
	}
	calls := rt.got("session.start")
	var start struct {
		Binding string `json:"input_binding"`
	}
	if len(calls) != 1 || json.Unmarshal(calls[0], &start) != nil || start.Binding == "" {
		t.Fatal("node did not bind a protected input source")
	}
	rt.binding = start.Binding
	return rt
}

func pushInputWait(t *testing.T, rt *inputDriver, wait string) {
	t.Helper()
	row := map[string]any{"session_id": "toy-session", "status": "working", "input_binding": rt.binding}
	if wait != "" {
		row["waiting_inputs"] = []map[string]any{{
			"task": "task-1", "wait_id": wait, "command": "example setup", "prompt": "Continue?",
			"output": "Ready", "masked": false, "tool_name": "other", "id": "ci_chosen", "fields": []any{},
		}}
	}
	if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{row}}); err != nil {
		t.Fatal(err)
	}
}

func nextInputCard(t *testing.T, n *Node, previous string) proto.Interaction {
	t.Helper()
	var card proto.Interaction
	waitFor(t, "node-created runtime card", func() bool {
		cards := n.runtimes.inputCards()
		if len(cards) != 1 || cards[0].ID == previous {
			return false
		}
		card = cards[0]
		return true
	})
	return card
}

func answerRuntimeCard(t *testing.T, n *Node, card proto.Interaction, allow bool, line string) error {
	t.Helper()
	_, err := nodeCall(t, n, "codex.answer", map[string]any{
		"session_id": card.SessionID, "id": card.ID, "created_at": card.CreatedAt,
		"resolution": proto.Resolution{Allow: &allow, Values: map[string]string{"input": line}},
	})
	return err
}

func TestRuntimeCommandInputRoundTrip(t *testing.T) {
	n, hub := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "first")
	card := nextInputCard(t, n, "")
	if !proto.IsCommandInputCard(card) || card.ID == "ci_chosen" || card.Fields[0].Label != "Continue?" || card.Agent != "toy" {
		t.Fatalf("unexpected card: %+v", card)
	}
	var in proto.CommandInput
	want := proto.CommandInput{Task: "task-1", Command: "example setup", Prompt: "Continue?", Output: "Ready"}
	if json.Unmarshal(card.ToolInput, &in) != nil || in != want {
		t.Fatalf("wrong card input: %s", card.ToolInput)
	}
	n.syncCodexCards(context.Background())
	waitFor(t, "codex.interactions", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, event := range hub.events {
			if event == "codex.interactions" {
				return true
			}
		}
		return false
	})
	if err := answerRuntimeCard(t, n, card, true, "a\nb"); refusalCode(err) != codeCommandInputLine {
		t.Fatalf("line: %v", err)
	}
	if len(rt.got("session.input")) != 0 {
		t.Fatal("invalid line reached driver")
	}
	rt.mu.Lock()
	rt.answer = func(method string, _ json.RawMessage) (any, error) {
		if method != "session.input" {
			return nil, errors.New("unexpected method")
		}
		return map[string]bool{"typed": true}, nil
	}
	rt.mu.Unlock()
	if err := answerRuntimeCard(t, n, card, true, "  first answer  "); err != nil {
		t.Fatal(err)
	}
	calls := rt.got("session.input")
	var sent map[string]string
	wantCall := map[string]string{"task": "task-1", "session_id": "toy-session", "wait_id": "first", "line": "  first answer  "}
	if len(calls) != 1 || json.Unmarshal(calls[0], &sent) != nil || !maps.Equal(sent, wantCall) {
		t.Fatalf("wrong input call: %s", calls)
	}
	if err := answerRuntimeCard(t, n, card, true, "again"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("repeat: %v", err)
	}
	pushInputWait(t, rt, "second")
	second := nextInputCard(t, n, card.ID)
	if err := answerRuntimeCard(t, n, second, false, ""); err != nil {
		t.Fatal(err)
	}
	pushInputWait(t, rt, "second")
	if err := answerRuntimeCard(t, n, second, true, "after decline"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("decline: %v", err)
	}
	if len(rt.got("session.input")) != 1 || len(rt.got("session.send")) != 0 {
		t.Fatal("decline, replay or model send reached driver")
	}
}

func TestRuntimeCommandInputOwnershipAndWithdrawal(t *testing.T) {
	n, _ := runtimeNode(t)
	first := startInputRuntime(t, n, "first")
	pushInputWait(t, first, "one")
	old := nextInputCard(t, n, "")
	newer := inputRuntime(t, n, "next", false)
	pushInputWait(t, newer, "one")
	waitFor(t, "incapable owner hides old card", func() bool {
		h := n.runtimes.ownerOf(old.SessionID)
		return h != nil && h.id() == "next" && len(n.runtimes.inputCards()) == 0
	})
	if err := answerRuntimeCard(t, n, old, true, "test"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("owner changed: %v", err)
	}
	next := inputRuntime(t, n, "third", true)
	next.binding = "unverified"
	pushInputWait(t, next, "one")
	waitFor(t, "replacement listed", func() bool { h := n.runtimes.ownerOf(old.SessionID); return h != nil && h.id() == "third" })
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("unverified runtime was allowed to present a protected card")
	}
	if err := answerRuntimeCard(t, n, old, true, "test"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("stale response: %v", err)
	}
	_ = next.conn.Close()
	_ = newer.conn.Close()
	waitFor(t, "original owner restored", func() bool { h := n.runtimes.ownerOf(old.SessionID); return h != nil && h.id() == "first" })
	pushInputWait(t, first, "")
	waitFor(t, "withdrawal", func() bool { return len(n.runtimes.inputCards()) == 0 })
	if err := answerRuntimeCard(t, n, old, true, "test"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("withdrawal: %v", err)
	}
	for _, rt := range []*inputDriver{first, newer, next} {
		if len(rt.got("session.input")) != 0 {
			t.Fatal("stale answer reached runtime")
		}
	}
}

func TestRuntimeCommandInputRefusals(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "first")
	card := nextInputCard(t, n, "")
	const line = "test-private-line"
	for _, tc := range []struct {
		driverCode, want string
		result           any
	}{
		{codeCommandInputRefused, codeCommandInputRefused, nil},
		{codeCommandInputLine, codeCommandInputLine, nil},
		{codeCommandInputUnreachable, codeCommandInputUnreachable, nil},
		{line, codeCommandInputUnconfirmed, nil},
		{"", codeCommandInputUnconfirmed, map[string]bool{"ok": true}},
		{screen.PromptChanged, screen.PromptChanged, nil},
	} {
		rt.mu.Lock()
		rt.answer = func(string, json.RawMessage) (any, error) {
			if tc.driverCode == "" {
				return tc.result, nil
			}
			return nil, &rpc.Error{Code: tc.driverCode, Message: line, Data: json.RawMessage(`"test-private-line"`)}
		}
		rt.mu.Unlock()
		err := answerRuntimeCard(t, n, card, true, line)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != tc.want || strings.Contains(re.Message+string(re.Data), line) {
			t.Fatalf("unexpected refusal for %q: %v", tc.want, err)
		}
		var data struct {
			Delivered string `json:"delivered"`
		}
		if json.Unmarshal(re.Data, &data) != nil || (data.Delivered == "unknown") != (tc.want == codeCommandInputUnconfirmed) {
			t.Fatalf("delivery for %q: %s", tc.want, re.Data)
		}
	}
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("obsolete prompt still listed")
	}
}

// Each update includes a valid wait and an unrelated malformed one. Wait for the
// registry's revision so assertions also observe updates that produce no cards.
func pushInputRevision(t *testing.T, n *Node, rt *inputDriver, rev int, status, wait string) {
	t.Helper()
	row := map[string]any{"session_id": "toy-session", "input_binding": rt.binding, "rev": rev, "status": status, "waiting_inputs": []any{
		map[string]any{"task": "task-1", "wait_id": wait, "prompt": "Password:", "output": strings.Repeat(".", rev)},
		map[string]any{"task": 42, "prompt": nil},
	}}
	if err := rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{row}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "input snapshot applied", func() bool {
		s, _, ok := n.runtimes.rowOf("toy-session")
		return ok && s.Rev == int64(rev)
	})
}

func TestRuntimeCommandInputSnapshotAndReconnect(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputRevision(t, n, rt, 1, "working", "same")
	card := nextInputCard(t, n, "")
	var in proto.CommandInput
	if json.Unmarshal(card.ToolInput, &in) != nil || !in.Masked {
		t.Fatal("Password prompt was not masked by default")
	}
	h := n.runtimes.ownerOf(card.SessionID)
	h.peer.SuspendCalls()
	if err := answerRuntimeCard(t, n, card, true, "example"); refusalCode(err) != codeCommandInputUnreachable {
		t.Fatalf("undispatched answer: %v", err)
	}
	h.peer.ResumeCalls()
	pushInputRevision(t, n, rt, 2, "working", "same")
	updated := nextInputCard(t, n, card.ID)
	if json.Unmarshal(updated.ToolInput, &in) != nil || in.Output != ".." {
		t.Fatal("updated command output missing from refreshed card")
	}
	card = updated
	if err := answerRuntimeCard(t, n, card, false, ""); err != nil {
		t.Fatal(err)
	}
	pushInputRevision(t, n, rt, 3, "working", "same")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("refresh reopened a declined occurrence")
	}
	_ = rt.conn.Close()
	waitFor(t, "runtime disconnected", func() bool { return n.runtimes.ownerOf(card.SessionID) == nil })
	again := inputRuntime(t, n, "toy", true)
	again.binding = rt.binding // the same SDK process retained its node-issued proof
	pushInputRevision(t, n, again, 4, "working", "same")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("same runtime instance reopened its declined occurrence")
	}
	pushInputRevision(t, n, again, 5, "working", "next")
	next := nextInputCard(t, n, card.ID)
	pushInputRevision(t, n, again, 6, "interrupted", "next")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("interrupted runtime still listed a card")
	}
	if err := answerRuntimeCard(t, n, next, true, "example"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("interrupted: %v", err)
	}
	if len(rt.got("session.input"))+len(again.got("session.input")) != 0 {
		t.Fatal("a refused answer reached a driver")
	}
}

func TestRuntimeCommandInputClaimSurvivesRefresh(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputRevision(t, n, rt, 1, "working", "one")
	card := nextInputCard(t, n, "")
	entered, release := make(chan struct{}), make(chan struct{})
	rt.mu.Lock()
	rt.answer = func(m string, _ json.RawMessage) (any, error) {
		if m != "session.input" {
			return nil, errors.New("unexpected method")
		}
		select {
		case <-entered:
			return nil, errors.New("duplicate call")
		default:
			close(entered)
		}
		<-release
		return map[string]bool{"typed": true}, nil
	}
	rt.mu.Unlock()
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	defer finish()
	done := make(chan error, 1)
	go func() { done <- answerRuntimeCard(t, n, card, true, "example") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("driver did not receive the first call")
	}
	pushInputRevision(t, n, rt, 2, "working", "one")
	current := nextInputCard(t, n, "")
	if err := answerRuntimeCard(t, n, current, true, "second"); refusalCode(err) != codeCommandInputRefused {
		t.Fatalf("second call during refresh: %v", err)
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(n.runtimes.inputCards()) != 0 || len(rt.got("session.input")) != 1 {
		t.Fatal("wait did not close exactly once")
	}
}

func TestRuntimeCommandInputRelayedPeerNeedsStartProof(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "one")
	old := nextInputCard(t, n, "")
	_ = rt.conn.Close()
	waitFor(t, "original runtime disconnected", func() bool { return n.runtimes.ownerOf(old.SessionID) == nil })
	// A byte relay can speak the local runtime protocol and repeat the hello
	// and row. Neither the transport nor the reported instance is a grant.
	nodeSide, relaySide := net.Pipe()
	go n.runtimeHost.serveConn(t.Context(), nodeSide, "")
	relay := &inputDriver{fakeRuntime: runtimeOn(t, relaySide, map[string]any{
		"runtime": "toy", "instance": "toy-instance", "caps": map[string]bool{"input": true},
	})}
	pushInputRevision(t, n, relay, 3, "working", "one")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("unbound relay published a protected card")
	}
	if err := answerRuntimeCard(t, n, old, true, "example"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("old answer after relay reconnect: %v", err)
	}
	if len(relay.got("session.input")) != 0 {
		t.Fatal("unbound relay received input")
	}
}

func TestRuntimeCommandInputLostReplyAcrossReconnect(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "one")
	card := nextInputCard(t, n, "")
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	rt.mu.Lock()
	rt.answer = func(m string, _ json.RawMessage) (any, error) {
		if m != "session.input" {
			return nil, errors.New("unexpected method")
		}
		close(entered)
		<-release
		return map[string]bool{"typed": true}, nil
	}
	rt.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- answerRuntimeCard(t, n, card, true, "example") }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("driver did not get input")
	}
	_ = rt.conn.Close()
	if err := <-done; refusalCode(err) != codeCommandInputUnconfirmed {
		t.Fatalf("lost reply: %v", err)
	}
	again := inputRuntime(t, n, "toy", true)
	again.binding = rt.binding
	pushInputRevision(t, n, again, 2, "working", "one")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("new connection offered a second write after unknown delivery")
	}
	pushInputRevision(t, n, again, 3, "working", "next")
	nextInputCard(t, n, card.ID)
	if len(again.got("session.input")) != 0 {
		t.Fatal("reconnect redelivered input")
	}
}

func TestRuntimeCommandInputStartMustConfirmRequestedSession(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := inputRuntime(t, n, "toy", true)
	started, reply := make(chan string, 1), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(reply) }) }
	defer release()
	rt.mu.Lock()
	rt.answer = func(method string, params json.RawMessage) (any, error) {
		if method != "session.start" {
			return nil, errors.New("unexpected method")
		}
		var start struct {
			Binding string `json:"input_binding"`
		}
		if err := json.Unmarshal(params, &start); err != nil {
			return nil, err
		}
		started <- start.Binding
		<-reply
		return map[string]any{"session": proto.Session{SessionID: "provider-assigned", Status: proto.StatusWorking}}, nil
	}
	rt.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, _, err := n.runtimeStartOn(t.Context(), n.runtimes.launcherRuntime("toy"), runtimeStartParams{Launcher: "toy", SessionID: "toy-session"})
		done <- err
	}()
	select {
	case rt.binding = <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("missing start")
	}
	if rt.binding == "" {
		t.Fatal("missing start binding")
	}
	pushInputRevision(t, n, rt, 1, "working", "one")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("input card before start confirmation")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	} // ordinary starts can use the provider's ID
	pushInputRevision(t, n, rt, 2, "working", "one")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("a different returned session authorized the requested session")
	}
}

func TestRuntimeCommandInputUnknownThenRefusedRetry(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "one")
	card := nextInputCard(t, n, "")
	rt.mu.Lock()
	rt.answer = func(string, json.RawMessage) (any, error) { return nil, errors.New("no confirmation") }
	rt.mu.Unlock()
	if err := answerRuntimeCard(t, n, card, true, "first"); refusalCode(err) != codeCommandInputUnconfirmed {
		t.Fatalf("first delivery: %v", err)
	}
	if err := answerRuntimeCard(t, n, card, true, "invalid\nline"); refusalCode(err) != codeCommandInputLine {
		t.Fatalf("rejected retry: %v", err)
	}
	_ = rt.conn.Close()
	waitFor(t, "disconnected", func() bool { return n.runtimes.ownerOf(card.SessionID) == nil })
	again := inputRuntime(t, n, "toy", true)
	again.binding = rt.binding
	pushInputRevision(t, n, again, 1, "offline", "one")
	// While the session stays listed, the SDK omits its wait. Advance past the
	// old delivery-record lifetime before advertising the original wait again.
	if cards := n.runtimes.inputCardsAt(time.Now().Add(2 * agyInputMaxAge)); len(cards) != 0 {
		t.Fatal("offline session offered input")
	}
	pushInputRevision(t, n, again, 2, "working", "one")
	if len(n.runtimes.inputCards()) != 0 {
		t.Fatal("the original occurrence was offered again after a quiet reconnect")
	}
	if err := answerRuntimeCard(t, n, card, true, "again"); refusalCode(err) != screen.PromptChanged {
		t.Fatalf("old card after reconnect: %v", err)
	}
	if len(again.got("session.input")) != 0 {
		t.Fatal("the reconnect delivered an answer for the old occurrence")
	}
	pushInputRevision(t, n, again, 3, "working", "next")
	nextInputCard(t, n, card.ID)
}

func TestRuntimeCommandInputQuietReconnectPreservesBinding(t *testing.T) {
	n, _ := runtimeNode(t)
	rt := startInputRuntime(t, n, "toy")
	pushInputWait(t, rt, "before")
	card := nextInputCard(t, n, "")
	_ = rt.conn.Close()
	waitFor(t, "disconnected", func() bool { return n.runtimes.ownerOf(card.SessionID) == nil })
	again := inputRuntime(t, n, "toy", true)
	again.binding = rt.binding
	if err := again.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{map[string]any{"session_id": card.SessionID}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "quiet inventory", func() bool { return n.runtimes.ownerOf(card.SessionID) != nil })
	b := n.runtimes
	b.mu.Lock()
	b.inputBindings[card.SessionID].seen = time.Now().Add(-2 * agyInputMaxAge)
	b.mu.Unlock()
	if len(b.inputCards()) != 0 {
		t.Fatal("quiet reconnect produced a card")
	}
	pushInputWait(t, again, "after")
	nextInputCard(t, n, card.ID)
}
