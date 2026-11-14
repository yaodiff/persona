package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Right after a message starts a session, claude has not written its transcript yet: the phone's backlog request
// must get an empty chat, not "transcript not found" (which flashed as an error on the chat page).
func TestItemsOfStartingSession(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, paths: map[string]string{}, files: map[string]*transcript.File{}, procs: map[string]*assistantProc{},
		agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}}
	const started = "11111111-2222-3333-4444-555555555555"
	n.state.put(started, sessionState{Cwd: home + "/.persona/assistant"})
	res, err := n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"`+started+`","turns":2}`))
	if err != nil {
		t.Fatalf("a starting session should not fail: %v", err)
	}
	if b, _ := json.Marshal(res); !strings.Contains(string(b), `"items":[]`) {
		t.Fatalf("want an empty page, got %s", b)
	}
	_, err = n.handle(context.Background(), "session.items", json.RawMessage(`{"session_id":"99999999-2222-3333-4444-555555555555"}`))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("an unknown session should still fail, got %v", err)
	}
}

// A start that fails at once (here: no terminal host) can finish before startAsync answers. The answer is the session
// as it was when the start began, never one the start goroutine is updating (go test -race caught the read of the
// record racing finish).
func TestStartAsyncAnswersBeforeAFastFailure(t *testing.T) {
	n, _ := testNode(t)
	n.modes = map[string]sessionModes{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("11111111-2222-3333-4444-%012d", i)
		s, err := n.startAsync(id, "", "", "", "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if s.SessionID != id || !s.Starting || s.Status != proto.StatusIdle || s.StartError != "" {
			t.Fatalf("answered %+v, want the starting session", s)
		}
		// Wait on the record's channel, not through the log: taking the log's lock here would order this read before
		// finish for the race detector and hide the race this test is for. Only this goroutine writes the map.
		r := n.starts.m[id]
		<-r.done
		if r.err == nil {
			t.Fatalf("start %s should fail without a terminal host", id)
		}
	}
}

func TestStartAsyncAllowsExpiredFailedID(t *testing.T) {
	n, _ := testNode(t)
	n.modes = map[string]sessionModes{}
	const id = "11111111-2222-3333-4444-555555555555"
	if _, err := n.startAsync(id, "", "", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	first := n.starts.get(id)
	if first == nil {
		t.Fatal("the first start has no record")
	}
	<-first.done
	if first.err == nil {
		t.Fatal("the first start should fail without a terminal host")
	}
	if _, err := n.startAsync(id, "", "", "", "", "", "", ""); err == nil || !strings.Contains(err.Error(), "already starting") {
		t.Fatalf("an unexpired failed ID must still refuse a duplicate: %v", err)
	}

	n.starts.mu.Lock()
	first.ended = time.Now().Add(-startFailedFor - time.Second)
	n.starts.mu.Unlock()
	if got := n.starts.listed(nil); len(got) != 0 {
		t.Fatalf("the expired failure should no longer be listed: %+v", got)
	}
	s, err := n.startAsync(id, "", "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("the expired failed ID must be retryable: %v", err)
	}
	if s == nil || !s.Starting {
		t.Fatalf("the retry must answer as a new starting session: %+v", s)
	}
	second := n.starts.get(id)
	if second == nil || second == first {
		t.Fatal("the retry must have a distinct start record")
	}
	<-second.done
	if second.err == nil {
		t.Fatal("the retry's worker should run and fail again without a terminal host")
	}
}

// A fixture may only check that an async start typed its command. Its unfinished starts must still stop before the
// node and fake terminal are torn down, not wake three minutes later in another test.
func TestAsyncStartsStopWithTheirNode(t *testing.T) {
	var pending []*startRec
	var stopped *Node
	t.Run("node", func(t *testing.T) {
		n, f, home := herdrNode(t)
		stopped = n
		startHerdr(f, "w1:p2", proto.StatusUnknown)
		for i := 0; i < 2; i++ {
			s, err := n.startAsync("", home, "claude", "", "w1", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			r := n.starts.get(s.SessionID)
			waitCalls(t, f, "pane.send_input", i+1)
			select {
			case <-r.done:
				t.Fatal("the fake shell should leave this start pending")
			default:
			}
			pending = append(pending, r)
		}
	})
	for _, r := range pending {
		select {
		case <-r.done:
			if !errors.Is(r.err, context.Canceled) {
				t.Fatalf("start ended with %v, want fixture cancellation", r.err)
			}
		default:
			t.Fatal("async start outlived its node's fixture")
		}
	}
	if _, err := stopped.startAsync("", t.TempDir(), "claude", "", "w1", "", "", ""); err == nil || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("a closed node accepted another start: %v", err)
	}
}
