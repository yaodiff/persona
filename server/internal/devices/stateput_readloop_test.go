package devices

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// gateStateStore blocks every PutNodeSession until release is closed. It proves a test really runs while the store
// call is still in flight.
type gateStateStore struct {
	*memStore

	entered chan string // session id of every entered call, buffered by the test
	release chan struct{}

	mu       sync.Mutex
	order    []string
	returned int
}

func newGateStateStore() *gateStateStore {
	return &gateStateStore{memStore: newMemStore(), entered: make(chan string, 64), release: make(chan struct{})}
}

func (s *gateStateStore) PutNodeSession(ctx context.Context, _, session string, _ db.NodeSession) error {
	select {
	case s.entered <- session:
	default:
	}
	<-s.release
	s.mu.Lock()
	s.order = append(s.order, session)
	s.returned++
	s.mu.Unlock()
	return nil
}

func (s *gateStateStore) NodeSessions(context.Context, string) (map[string]db.NodeSession, error) {
	return map[string]db.NodeSession{}, nil
}

func (s *gateStateStore) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.order...)
}

// A stalled Store call behind state.put must not hold the node connection's single rpc read loop: a reply (or the
// next request) arriving after the event has to be dispatched while PutNodeSession is still blocked. The old
// synchronous state.put fails this deterministically.
func TestStatePutStoreStallDoesNotDelayFollowingReply(t *testing.T) {
	st := newGateStateStore()
	d := New(Options{Hub: &fakeHost{}, Store: st, Logf: func(string, ...any) {}})
	m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh", SSHHost: "box.invalid"})
	m.storeWait = time.Minute // the gate, not the budget, bounds the call in this test
	m.ctx, m.stop = context.WithCancel(context.Background())
	defer m.stop()

	hubConn, nodeConn := net.Pipe()
	hubPeer := rpc.New("hub", hubConn)
	nodePeer := rpc.New("node", nodeConn)
	// The same synchronous event admission conn.go installs (eventRetire around m.onEvent).
	hubPeer.OnEvent = func(event string, params json.RawMessage) {
		m.eventRetire.Lock()
		defer m.eventRetire.Unlock()
		m.onEvent(event, params)
	}
	requestSeen := make(chan struct{})
	releaseReply := make(chan struct{})
	var seenOnce sync.Once
	nodePeer.OnRequest = func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		seenOnce.Do(func() { close(requestSeen) })
		<-releaseReply
		return map[string]any{"ok": true}, nil
	}
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	go hubPeer.Serve(serveCtx, hubConn)
	go nodePeer.Serve(serveCtx, nodeConn)

	releaseStoreOnce := sync.Once{}
	releaseStore := func() { releaseStoreOnce.Do(func() { close(st.release) }) }
	defer releaseStore()
	releaseReplyOnce := sync.Once{}
	defer releaseReplyOnce.Do(func() { close(releaseReply) })

	// A call from the manager side whose reply the node withholds until the test releases it.
	callErr := make(chan error, 1)
	go func() { callErr <- hubPeer.Call(serveCtx, "test.wait", nil, nil) }()
	select {
	case <-requestSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("node peer never received the call")
	}

	// state.put is written first on the wire; on the old code its synchronous PutNodeSession blocks the read loop.
	raw := json.RawMessage(`{"session_id":"s1","state":{"launcher":"claude","cwd":"/w"}}`)
	if err := nodePeer.Notify("state.put", raw); err != nil {
		t.Fatalf("notify state.put: %v", err)
	}
	select {
	case sid := <-st.entered:
		if sid != "s1" {
			t.Fatalf("PutNodeSession session = %q, want s1", sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("state.put never reached PutNodeSession")
	}

	// Let the node answer the earlier call. Its reply is the next line after state.put on the same connection.
	releaseReplyOnce.Do(func() { close(releaseReply) })
	select {
	case err := <-callErr:
		if err != nil {
			t.Fatalf("the following reply was not delivered: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("state.put blocks the single rpc read loop on a stalled Store: the following reply was not dispatched while PutNodeSession was still in flight")
	}

	// While the Store call is still blocked, the event callback must already have released eventRetire: Store I/O
	// never runs under it.
	retireGot := make(chan struct{})
	go func() {
		m.eventRetire.Lock()
		m.eventRetire.Unlock()
		close(retireGot)
	}()
	select {
	case <-retireGot:
	case <-time.After(time.Second):
		t.Fatal("eventRetire stays held while the Store call blocks: Store I/O runs inside event admission")
	}

	// Proof the reply overtook an still-blocked Store call (otherwise the timing assertion proves nothing).
	select {
	case sid := <-st.entered:
		t.Fatalf("unexpected second PutNodeSession for %q before the gate released", sid)
	default:
	}
	st.mu.Lock()
	gotReturns := st.returned
	st.mu.Unlock()
	if gotReturns != 0 {
		t.Fatalf("PutNodeSession returned %d time(s) before release, want 0", gotReturns)
	}

	releaseStore()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		gotReturns = st.returned
		st.mu.Unlock()
		if gotReturns == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the blocked store call to return
	}
	if gotReturns != 1 {
		t.Fatalf("PutNodeSession returns = %d after release, want 1", gotReturns)
	}

	// Stopping the Machine ends the writer goroutine after its bounded drain.
	m.stop()
	if w := m.stateWriter; w != nil && !w.waitDone(5*time.Second) {
		t.Fatal("node-state writer did not stop after the Machine context ended")
	}
}

// Through the real state.put event path, a second session must not start a concurrent PutNodeSession while the
// first is still in flight: exactly one writer goroutine per Machine. Fanning one goroutine out per event fails
// this.
func TestStatePutDifferentSessionsStayBehindOneWriter(t *testing.T) {
	st := newGateStateStore()
	d := New(Options{Hub: &fakeHost{}, Store: st, Logf: func(string, ...any) {}})
	m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh"})
	m.storeWait = time.Minute
	m.ctx, m.stop = context.WithCancel(context.Background())

	releaseOnce := sync.Once{}
	release := func() { releaseOnce.Do(func() { close(st.release) }) }
	defer release()

	put := func(sid, cwd string) {
		raw, err := json.Marshal(map[string]any{
			"session_id": sid,
			"state":      map[string]any{"launcher": "claude", "cwd": cwd},
		})
		if err != nil {
			t.Fatal(err)
		}
		m.onEvent("state.put", raw)
	}

	put("a", "/a")
	select {
	case sid := <-st.entered:
		if sid != "a" {
			t.Fatalf("first write = %q, want a", sid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session a never reached PutNodeSession")
	}

	put("b", "/b")
	put("c", "/c")
	select {
	case sid := <-st.entered:
		t.Fatalf("session %q reached PutNodeSession while session a was still in flight: writes fan out per event instead of one ordered writer", sid)
	case <-time.After(200 * time.Millisecond):
	}

	release()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(st.callOrder()) == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond) // sleep-ok: yield inside the deadline-bounded poll for the three completed writes
	}
	if got := st.callOrder(); len(got) != 3 || got[0] != "a" {
		t.Fatalf("completed write order = %v, want a first and three writes", got)
	}
	m.stop()
	if w := m.stateWriter; w != nil && !w.waitDone(5*time.Second) {
		t.Fatal("node-state writer did not stop")
	}
}
