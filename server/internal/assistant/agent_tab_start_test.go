package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskStartNamesTabAfterSlowModel(t *testing.T) {
	s, f := newQuietService(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	f.tabModelEntered, f.tabModelGate, f.tabReply = entered, gate, "Async Login\n(second line is ignored)"
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f.startReply = func(params map[string]any, sess *proto.Session) *proto.StartInstead {
		requested, _ := params["tab_name"].(string)
		sess.SpaceKind, sess.SlotID, sess.SlotName = "herdr", "w1:t1", requested
		return nil
	}
	task := PlanTask{Title: "Inspect the failure", Prompt: "Inspect the failure", Machine: "box", Cwd: "/code/app",
		Readonly: true, Permission: "plan"}
	type startResult struct {
		mid, sid string
		err      error
	}
	done := make(chan startResult, 1)
	go func() {
		mid, sid, err := s.startTaskAgent(context.Background(), &task, false)
		done <- startResult{mid, sid, err}
	}()
	var started startResult
	select {
	case started = <-done:
	case <-time.After(2 * time.Second):
		release()
		<-done
		t.Fatal("the task start waited for the naming model")
	}
	if started.err != nil || started.mid != "box" || started.sid == "" {
		release()
		t.Fatalf("start = %q/%q, %v", started.mid, started.sid, started.err)
	}
	f.mu.Lock()
	startParams := f.starts[0]
	f.mu.Unlock()
	if got := startParams["tab_name"]; got != "inspect-the-failure" {
		t.Fatalf("local placeholder = %q, want inspect-the-failure", got)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("the returned session was not named in the background")
	}
	release()
	waitAgentTabNaming(t, s)
	f.mu.Lock()
	renames := append([]map[string]any(nil), f.sends...)
	f.mu.Unlock()
	if len(renames) != 1 {
		t.Fatalf("renames = %v", renames)
	}
	params := renames[0]["rename"].(map[string]any)
	if params["session_id"] != started.sid || params["tab_id"] != "w1:t1" ||
		params["expected_tab_name"] != "inspect-the-failure" || params["tab_name"] != "async-login" {
		t.Fatalf("guarded rename = %v", params)
	}
	if got := s.agentDisplayName("box", proto.Session{SessionID: started.sid}); got != "async-login" {
		t.Fatalf("display title after the guarded rename = %q", got)
	}
}

func TestLateTabNameDoesNotOverwriteManualRename(t *testing.T) {
	s, f := newQuietService(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	f.tabModelEntered, f.tabModelGate, f.tabReply = entered, gate, "Async Login"
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	current := "inspect-the-failure"
	guarded, unguarded := 0, 0
	f.answerNode(assistantwire.DeviceSessionRenameTabIfCurrent, func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		guarded++
		if current != params["expected_tab_name"] {
			return nil, errors.New("the tab name changed")
		}
		current = params["tab_name"].(string)
		return map[string]any{"ok": true}, nil
	})
	f.answerNode(DeviceSessionRename, func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		unguarded++
		current = params["tab_name"].(string)
		return map[string]any{"ok": true}, nil
	})
	s.nameAgentTabLater(assistantwire.AgentTabNameStart{MachineID: "box", SessionID: "s1", TabID: "w1:t1",
		ExpectedTabName: "inspect-the-failure", NameFrom: "Inspect the failure"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("the naming model was not called")
	}
	f.mu.Lock()
	current = "owner-chosen-title"
	f.mu.Unlock()
	release()
	waitAgentTabNaming(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	if current != "owner-chosen-title" || guarded != 1 || unguarded != 0 {
		t.Fatalf("late name overwrote the owner's title: current = %q, guarded = %d, unguarded = %d", current, guarded, unguarded)
	}
}

func TestLateTabNameWaitsForStartedTab(t *testing.T) {
	s, f := newQuietService(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	f.tabModelEntered, f.tabModelGate, f.tabReply = entered, gate, "Async Login"
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	f.sessions["box"] = nil
	gets := 0
	f.answerNode(DeviceSessionGet, func(_ string, _ json.RawMessage) (any, error) {
		gets++
		if gets == 1 {
			return map[string]any{"ok": false}, nil
		}
		return map[string]any{"ok": true, "session": proto.Session{SessionID: "s1", SpaceKind: "herdr", SlotID: "w1:t1", SlotName: "inspect-the-failure"}}, nil
	})
	s.nameAgentTabLater(assistantwire.AgentTabNameStart{MachineID: "box", SessionID: "s1",
		ExpectedTabName: "inspect-the-failure", NameFrom: "Inspect the failure"})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("the naming model was not called")
	}
	release()
	waitAgentTabNaming(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	if gets < 2 || len(f.sends) != 1 {
		t.Fatalf("session reads = %d, renames = %v", gets, f.sends)
	}
	params := f.sends[0]["rename"].(map[string]any)
	if params["tab_id"] != "w1:t1" || params["expected_tab_name"] != "inspect-the-failure" || params["tab_name"] != "async-login" {
		t.Fatalf("guarded rename after the tab appeared = %v", params)
	}
}

// An Orca start is named the same way (#17043): the placeholder Orca shows is the expected name, the tab found once
// the node lists it, and the rename goes through the node's guarded call.
func TestOrcaStartTabNamedLater(t *testing.T) {
	s, f := newQuietService(t)
	entered, gate := make(chan struct{}), make(chan struct{})
	f.tabModelEntered, f.tabModelGate, f.tabReply = entered, gate, "Async Login"
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	listed := proto.Session{SessionID: "s1", SpaceKind: "orca", SlotID: "tab1", TabID: "tab1", SlotName: "inspect-the-failure"}
	start := agentTabNameStart("box", proto.Session{SessionID: "s1", SpaceKind: "orca"}, "inspect-the-failure", "Inspect the failure")
	if start.ExpectedTabName != "inspect-the-failure" || start.TabID != "" {
		t.Fatalf("start = %+v", start)
	}
	if got := agentTabNameStart("box", listed, "requested", "x"); got.ExpectedTabName != "inspect-the-failure" || got.TabID != "tab1" {
		t.Fatalf("an Orca tab's own label is the expected name: %+v", got)
	}
	f.sessions["box"] = nil
	gets := 0
	f.answerNode(DeviceSessionGet, func(_ string, _ json.RawMessage) (any, error) {
		gets++
		if gets == 1 {
			return map[string]any{"ok": false}, nil
		}
		return map[string]any{"ok": true, "session": listed}, nil
	})
	s.nameAgentTabLater(start)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		release()
		t.Fatal("the naming model was not called")
	}
	release()
	waitAgentTabNaming(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 1 {
		t.Fatalf("renames = %v", f.sends)
	}
	params := f.sends[0]["rename"].(map[string]any)
	if params["tab_id"] != "tab1" || params["expected_tab_name"] != "inspect-the-failure" || params["tab_name"] != "async-login" {
		t.Fatalf("guarded rename of the Orca tab = %v", params)
	}
}

func waitAgentTabNaming(t *testing.T, s *Service) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		s.agentNames.runs.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background tab naming did not finish")
	}
}
