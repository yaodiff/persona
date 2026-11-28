package devices

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// recordingStateStore is a healthy, immediate nodeStateStore that records every PutNodeSession. It embeds
// memStore to satisfy the broad Store interface Options takes.
type recordingStateStore struct {
	*memStore

	mu    sync.Mutex
	calls []writtenCall
}

func newRecordingStateStore() *recordingStateStore {
	return &recordingStateStore{memStore: newMemStore()}
}

func (s *recordingStateStore) PutNodeSession(_ context.Context, _, session string, st db.NodeSession) error {
	s.mu.Lock()
	s.calls = append(s.calls, writtenCall{session: session, state: st})
	s.mu.Unlock()
	return nil
}

func (s *recordingStateStore) NodeSessions(context.Context, string) (map[string]db.NodeSession, error) {
	return map[string]db.NodeSession{}, nil
}

func (s *recordingStateStore) snapshot() []writtenCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]writtenCall(nil), s.calls...)
}

func (s *recordingStateStore) waitSession(session string, timeout time.Duration) (writtenCall, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, c := range s.snapshot() {
			if c.session == session {
				return c, true
			}
		}
		time.Sleep(2 * time.Millisecond) // sleep-ok: yield inside the recording store's deadline-bounded waitSession poll
	}
	return writtenCall{}, false
}

func (s *recordingStateStore) sessionSet() map[string]bool {
	out := map[string]bool{}
	for _, c := range s.snapshot() {
		out[c.session] = true
	}
	return out
}

// TestStatePutAfterWriterExitRunsBoundedFallback reproduces the shutdown/removal window finding from review: a
// state.put admitted strictly after the per-Machine writer exited used to be inserted into a pending map no
// goroutine ever reads and was silently never written, breaking the guarantee that the last put of a machine
// being let go is kept. Serializing the late put after waitDone makes the loss deterministic on the old code.
func TestStatePutAfterWriterExitRunsBoundedFallback(t *testing.T) {
	st := newRecordingStateStore()
	d := New(Options{Hub: &fakeHost{}, Store: st, Logf: func(string, ...any) {}})
	m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh"})
	m.storeWait = time.Second
	m.ctx, m.stop = context.WithCancel(context.Background())
	defer m.stop()

	// The first admission lazily starts the single writer.
	m.persistNodeState("seed", db.NodeSession{Cwd: "/seed"})
	if _, ok := st.waitSession("seed", 3*time.Second); !ok {
		t.Fatal("seed state.put never reached PutNodeSession")
	}

	// Let the Machine end exactly as removing/replacing it does, and wait until its writer has fully exited.
	m.stop()
	if m.stateWriter == nil || !m.stateWriter.waitDone(3*time.Second) {
		t.Fatal("node-state writer did not exit after the Machine context ended")
	}

	// The last state.put, admitted in the window strictly after the writer exited.
	last := db.NodeSession{Cwd: "/last", CreationID: "creation-last", Incarnation: "inc-last"}
	m.persistNodeState("last", last)

	call, ok := st.waitSession("last", time.Second)
	if !ok {
		t.Fatal("the last state.put was admitted after the writer exited and then silently never written: " +
			"no bounded fallback PutNodeSession happened (shutdown/removal window drop)")
	}
	if call.state.Cwd != "/last" || call.state.CreationID != "creation-last" || call.state.Incarnation != "inc-last" {
		t.Fatalf("fallback write = %+v, want the admitted last state with its creation receipt", call.state)
	}
}

// TestStatePutStopRaceNeverDropsAdmittedPut hammers the stop/enqueue coordination under -race: every admission of a
// distinct session must reach the Store through either the still-live ordered writer or the post-exit bounded
// fallback. The terminal seal makes that ownership exclusive and atomic.
func TestStatePutStopRaceNeverDropsAdmittedPut(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		st := newRecordingStateStore()
		d := New(Options{Hub: &fakeHost{}, Store: st, Logf: func(string, ...any) {}})
		m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh"})
		m.storeWait = 2 * time.Second
		m.ctx, m.stop = context.WithCancel(context.Background())

		const n = 24
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				m.persistNodeState(fmt.Sprintf("s%02d", i), db.NodeSession{Cwd: fmt.Sprintf("/%d", i)})
			}(i)
		}
		// Vary the cancel point across iterations so some admissions hit a live writer and some the exit window.
		if iter%3 == 0 {
			runtime.Gosched()
		} else {
			time.Sleep(time.Duration(iter%4) * time.Millisecond) // sleep-ok: spread the cancel point so admissions cover both the live writer and exit window
		}
		m.stop()
		wg.Wait()
		if m.stateWriter != nil && !m.stateWriter.waitDone(5*time.Second) {
			t.Fatalf("iter %d: writer did not stop", iter)
		}

		deadline := time.Now().Add(5 * time.Second)
		var got map[string]bool
		for time.Now().Before(deadline) {
			got = st.sessionSet()
			if len(got) == n {
				break
			}
			time.Sleep(2 * time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for all admitted writes to land
		}
		if len(got) != n {
			var missing []string
			for i := 0; i < n; i++ {
				sid := fmt.Sprintf("s%02d", i)
				if !got[sid] {
					missing = append(missing, sid)
				}
			}
			t.Fatalf("iter %d: %d/%d admitted sessions reached the Store, missing %v", iter, len(got), n, missing)
		}
	}
}
