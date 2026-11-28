package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// hubEventRecorder captures JSON-lines runtime.hub notifications written to an rpc.Peer
// and optionally blocks the first notification matching blockOn to deterministically
// expose attach/detach interleavings.
type hubEventRecorder struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	states []bool

	blockArmed *atomic.Bool
	blockOn    bool
	blockOnce  *sync.Once
	inBlock    chan struct{}
	release    chan struct{}
}

func (w *hubEventRecorder) Write(p []byte) (int, error) {
	var msg struct {
		Event  string `json:"event"`
		Params struct {
			Connected bool `json:"connected"`
		} `json:"params"`
	}
	matched := false
	if json.Unmarshal(bytes.TrimSpace(p), &msg) == nil && msg.Event == "runtime.hub" {
		if w.blockOnce != nil && w.blockArmed != nil && w.blockArmed.Load() && msg.Params.Connected == w.blockOn {
			matched = true
		}
	}
	if matched {
		triggered := false
		w.blockOnce.Do(func() {
			triggered = true
			close(w.inBlock)
		})
		if triggered {
			<-w.release
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if json.Unmarshal(bytes.TrimSpace(p), &msg) == nil && msg.Event == "runtime.hub" {
		w.states = append(w.states, msg.Params.Connected)
	}
	return w.buf.Write(p)
}

func (w *hubEventRecorder) lastState() (bool, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.states) == 0 {
		return false, false
	}
	return w.states[len(w.states)-1], true
}

func (w *hubEventRecorder) snapshot() []bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]bool(nil), w.states...)
}

func TestRuntimeHostStaleDetachDoesNotInvertHubStateAndPreservesOfflineEdge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rh := NewRuntimeHost("test", "")
		rh.logger = log.New(io.Discard, "", 0)

		var blockOnce sync.Once
		inBlock := make(chan struct{})
		release := make(chan struct{})
		var armed atomic.Bool

		const count = 8
		recs := make([]*hubEventRecorder, count)
		for i := range count {
			rec := &hubEventRecorder{
				blockArmed: &armed,
				blockOn:    false,
				blockOnce:  &blockOnce,
				inBlock:    inBlock,
				release:    release,
			}
			recs[i] = rec
			h := rh.reg.add(rpc.New("r", rec))
			h.mu.Lock()
			h.ready = true
			h.hello.Runtime = fmt.Sprintf("rt-%d", i)
			h.mu.Unlock()
		}

		a := &Node{}
		b := &Node{}
		rh.attach(a)
		synctest.Wait()

		armed.Store(true)
		rh.detach(a)

		// Wait until the blocked runtime is mid-write of connected:false.
		<-inBlock

		// Re-attach b while detach(a)'s per-conn notifier is still mid-write.
		rh.attach(b)
		synctest.Wait()

		close(release)
		synctest.Wait()

		if rh.current() != b {
			t.Fatalf("current = %p, want b (%p)", rh.current(), b)
		}
		// Every runtime must end on connected:true AND observe the false->true edge so
		// edge-triggered SDKs re-report assistant PIDs cleared on hub transition.
		want := []bool{true, false, true}
		for i, rec := range recs {
			if got := rec.snapshot(); !slices.Equal(got, want) {
				t.Fatalf("rt-%d hub history = %v, want %v", i, got, want)
			}
		}
	})
}

func TestRuntimeHostStaleAttachDoesNotInvertHubState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rh := NewRuntimeHost("test", "")
		rh.logger = log.New(io.Discard, "", 0)

		var blockOnce sync.Once
		inBlock := make(chan struct{})
		release := make(chan struct{})
		var armed atomic.Bool
		armed.Store(true)

		const count = 8
		recs := make([]*hubEventRecorder, count)
		for i := range count {
			rec := &hubEventRecorder{
				blockArmed: &armed,
				blockOn:    true,
				blockOnce:  &blockOnce,
				inBlock:    inBlock,
				release:    release,
			}
			recs[i] = rec
			h := rh.reg.add(rpc.New("r", rec))
			h.mu.Lock()
			h.ready = true
			h.hello.Runtime = fmt.Sprintf("rt-%d", i)
			h.mu.Unlock()
		}

		a := &Node{}
		rh.attach(a)

		// Wait until one runtime is mid-write of connected:true.
		<-inBlock

		rh.detach(a)
		synctest.Wait()
		close(release)
		synctest.Wait()

		if rh.current() != nil {
			t.Fatalf("current = %p, want nil after detach", rh.current())
		}
		for i, rec := range recs {
			last, ok := rec.lastState()
			if !ok || last {
				t.Fatalf("rt-%d final hub state = (%v, ok=%v), history = %v; want final connected=false after detach",
					i, last, ok, rec.snapshot())
			}
		}
	})
}

func TestRuntimeHostWedgedRuntimeDoesNotStarveHealthyRuntimes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rh := NewRuntimeHost("test", "")
		rh.logger = log.New(io.Discard, "", 0)

		var wedgeOnce sync.Once
		wedgedInWrite := make(chan struct{})
		releaseWedge := make(chan struct{})
		var armed atomic.Bool
		armed.Store(true)

		wedgedRec := &hubEventRecorder{
			blockArmed: &armed,
			blockOn:    true,
			blockOnce:  &wedgeOnce,
			inBlock:    wedgedInWrite,
			release:    releaseWedge,
		}
		healthyRec := &hubEventRecorder{}

		addReady := func(name string, p *rpc.Peer) *runtimeConn {
			h := rh.reg.add(p)
			h.mu.Lock()
			h.ready = true
			h.hello.Runtime = name
			h.mu.Unlock()
			return h
		}
		wedgedPeer := rpc.New("r", wedgedRec)
		addReady("rt-wedged", wedgedPeer)
		addReady("rt-healthy", rpc.New("r", healthyRec))
		// Nil-peer guard regression check: must not panic during notifyHub.
		addReady("rt-nilpeer", nil)

		a := &Node{}
		b := &Node{}
		rh.attach(a)
		<-wedgedInWrite
		synctest.Wait()

		if got := healthyRec.snapshot(); !slices.Equal(got, []bool{true}) {
			t.Fatalf("healthy runtime starved by wedged peer on attach(a): got %v, want [true]", got)
		}

		rh.detach(a)
		synctest.Wait()
		if got := healthyRec.snapshot(); !slices.Equal(got, []bool{true, false}) {
			t.Fatalf("healthy runtime starved by wedged peer on detach(a): got %v, want [true false]", got)
		}

		rh.attach(b)
		synctest.Wait()
		if got := healthyRec.snapshot(); !slices.Equal(got, []bool{true, false, true}) {
			t.Fatalf("healthy runtime starved by wedged peer on attach(b): got %v, want [true false true]", got)
		}

		// Closing the wedged peer and releasing its writer exits cleanly without leaking goroutines.
		wedgedPeer.Close(io.EOF)
		close(releaseWedge)
		synctest.Wait()
	})
}

type hookRWC struct {
	io.Reader
	w       io.Writer
	closeFn func() error
	onWrite func([]byte)
}

func (c *hookRWC) Write(p []byte) (int, error) {
	if c.onWrite != nil {
		c.onWrite(p)
	}
	return c.w.Write(p)
}

func (c *hookRWC) Close() error { return c.closeFn() }

func TestRuntimeHostHelloReplyPrecedesConcurrentHubEventsOnWire(t *testing.T) {
	for _, tc := range []struct {
		name          string
		startAttached bool
		wantHelloHub  bool
		wantEventHub  bool
	}{
		{"detach_during_hello_reply", true, true, false},
		{"attach_during_hello_reply", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				rh := NewRuntimeHost("test", "")
				rh.logger = log.New(io.Discard, "", 0)

				a := &Node{}
				b := &Node{}
				if tc.startAttached {
					rh.attach(a)
					synctest.Wait()
				}

				c2sR, c2sW := io.Pipe()
				s2cR, s2cW := io.Pipe()

				var triggerOnce sync.Once
				srvConn := &hookRWC{
					Reader:  c2sR,
					w:       s2cW,
					closeFn: func() error { _ = c2sR.Close(); return s2cW.Close() },
					onWrite: func(line []byte) {
						// Trigger the hub transition right as the runtime.hello reply is being written
						// (after helloHub was snapshotted, before rpc.AfterReply runs).
						var msg struct {
							ID     string          `json:"id"`
							Result json.RawMessage `json:"result"`
						}
						if json.Unmarshal(bytes.TrimSpace(line), &msg) == nil && msg.ID != "" && len(msg.Result) > 0 {
							triggerOnce.Do(func() {
								if tc.startAttached {
									rh.detach(a)
								} else {
									rh.attach(b)
								}
							})
						}
					},
				}

				serveDone := make(chan struct{})
				go func() {
					defer close(serveDone)
					rh.serveConn(ctx, srvConn, "")
				}()

				type wireMsg struct {
					ID     string `json:"id"`
					Event  string `json:"event"`
					Result struct {
						Hub bool `json:"hub"`
					} `json:"result"`
					Params struct {
						Connected bool `json:"connected"`
					} `json:"params"`
				}
				wireCh := make(chan wireMsg, 8)
				readDone := make(chan struct{})
				go func() {
					defer close(readDone)
					sc := bufio.NewScanner(s2cR)
					for sc.Scan() {
						var m wireMsg
						if json.Unmarshal(sc.Bytes(), &m) == nil {
							wireCh <- m
						}
					}
				}()

				_, err := io.WriteString(c2sW, `{"id":"c1","method":"runtime.hello","params":{"runtime":"rt-wire","version":"1"}}`+"\n")
				if err != nil {
					t.Fatal(err)
				}

				first := <-wireCh
				second := <-wireCh

				if first.ID != "c1" || first.Event != "" {
					t.Fatalf("first wire message must be runtime.hello reply, got %+v", first)
				}
				if first.Result.Hub != tc.wantHelloHub {
					t.Fatalf("hello reply hub = %v, want %v", first.Result.Hub, tc.wantHelloHub)
				}
				if second.Event != "runtime.hub" || second.Params.Connected != tc.wantEventHub {
					t.Fatalf("second wire message = %+v, want runtime.hub connected=%v", second, tc.wantEventHub)
				}

				cancel()
				_ = c2sW.Close()
				_ = s2cR.Close()
				<-serveDone
				<-readDone
				synctest.Wait()
			})
		})
	}
}

func TestRuntimeHostConcurrentAttachHostOffAndWatchesInvariant(t *testing.T) {
	rh := NewRuntimeHost("test", "")
	rh.logger = log.New(io.Discard, "", 0)

	const count = 4
	recs := make([]*hubEventRecorder, count)
	conns := make([]*runtimeConn, count)
	peers := make([]*rpc.Peer, count)
	for i := range count {
		rec := &hubEventRecorder{}
		recs[i] = rec
		peer := rpc.New("r", rec)
		peers[i] = peer
		h := rh.reg.add(peer)
		conns[i] = h
		h.mu.Lock()
		h.ready = true
		h.hello.Runtime = fmt.Sprintf("rt-%d", i)
		h.mu.Unlock()
	}
	defer func() {
		for i := range count {
			rh.reg.remove(conns[i])
			peers[i].Close(io.EOF)
		}
		for _, h := range conns {
			for {
				h.mu.Lock()
				busy := h.hubNotifying
				h.mu.Unlock()
				if !busy {
					break
				}
				runtime.Gosched()
			}
		}
	}()

	var stop atomic.Bool
	var mismatch atomic.Bool
	var observerWG sync.WaitGroup
	observerWG.Add(1)
	go func() {
		defer observerWG.Done()
		for !stop.Load() {
			// Under rh.mu -> rh.reg.mu, hostOff must always match the currently attached node.
			rh.mu.Lock()
			cur := rh.node
			rh.reg.mu.Lock()
			off := rh.reg.hostOff
			rh.reg.mu.Unlock()
			rh.mu.Unlock()
			if cur != nil && (off == nil || off(proto.EnginePaseo) != cur.engineHostOff(proto.EnginePaseo)) {
				mismatch.Store(true)
			}
		}
	}()

	var wg sync.WaitGroup
	for range 25 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			paseoNode := &Node{}
			paseoNode.eng.name = proto.EnginePaseo
			rh.attach(paseoNode)
			rh.detach(paseoNode)
		}()
		go func() {
			defer wg.Done()
			herdrNode := &Node{}
			herdrNode.eng.name = proto.EngineHerdr
			rh.attach(herdrNode)
			rh.detach(herdrNode)
		}()
	}
	wg.Wait()
	stop.Store(true)
	observerWG.Wait()

	if mismatch.Load() {
		t.Fatal("observed rh.reg.hostOff disagreeing with rh.current() during concurrent attach")
	}

	// Verify a stale detach(a) racing right after attach(b) never wipes b's watch or flips hostOff.
	a := &Node{}
	a.eng.name = proto.EngineHerdr
	b := &Node{}
	b.eng.name = proto.EnginePaseo
	rh.attach(a)
	rh.attach(b)
	rh.reg.mu.Lock()
	rh.reg.watching["session-b"] = 11
	rh.reg.mu.Unlock()
	rh.detach(a)

	rh.reg.mu.Lock()
	gotSeq := rh.reg.watching["session-b"]
	off := rh.reg.hostOff
	rh.reg.mu.Unlock()
	if gotSeq != 11 {
		t.Fatalf("stale detach(a) wiped b's watch: watching[session-b] = %d, want 11", gotSeq)
	}
	if off == nil || off(proto.EnginePaseo) {
		t.Fatalf("hostOff does not match attached Paseo node: off(paseo)=%v", off != nil && off(proto.EnginePaseo))
	}
}

func TestRuntimeHostListenFailureAndHubHoldCoalescing(t *testing.T) {
	t.Run("listen_failure_leaves_runtimeHost_nil", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		n := &Node{
			runtimeSock: filepath.Join(blocker, "runtimes.sock"),
			logger:      log.New(io.Discard, "", 0),
		}
		rh, err := n.listenRuntimeSocket(context.Background())
		if err == nil {
			t.Fatalf("expected listenRuntimeSocket error for non-directory socket parent, got rh=%v", rh)
		}
		if n.runtimeHost != nil {
			t.Fatalf("n.runtimeHost = %p after Listen failure, want nil", n.runtimeHost)
		}
	})

	t.Run("false_true_false_coalescing_and_holder_refcount", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			rh := NewRuntimeHost("test", "")
			rh.logger = log.New(io.Discard, "", 0)

			rec := &hubEventRecorder{}
			peer := rpc.New("r", rec)
			defer peer.Close(io.EOF)
			h := rh.reg.add(peer)
			defer rh.reg.remove(h)
			h.mu.Lock()
			h.ready = true
			h.hello.Runtime = "rt-hold"
			h.hubStateInit = true
			h.hubLastConnected = false
			h.hubLastEpoch = 0
			// Simulate two overlapping runtime.hello calls holding notifications.
			h.hubHolders = 2
			h.mu.Unlock()

			a := &Node{}
			rh.attach(a)
			rh.detach(a)
			// Releasing the first holder must keep notifications gated while holder #2 is active.
			rh.releaseHubAfterHello(h)
			synctest.Wait()
			if got := rec.snapshot(); len(got) != 0 {
				t.Fatalf("emitted runtime.hub while second hello holder active: %v", got)
			}

			// Releasing the last holder after false->true->false must NOT emit a redundant connected:false.
			rh.releaseHubAfterHello(h)
			synctest.Wait()
			if got := rec.snapshot(); len(got) != 0 {
				t.Fatalf("emitted redundant runtime.hub on false->true->false coalescing: %v", got)
			}

			// A subsequent overlapping hello that fails registerRuntime must still release and drain queued updates.
			h.mu.Lock()
			h.hubHolders++
			h.mu.Unlock()
			b := &Node{}
			rh.attach(b)
			synctest.Wait()
			if got := rec.snapshot(); len(got) != 0 {
				t.Fatalf("emitted runtime.hub while hello holder active: %v", got)
			}
			rh.releaseHubAfterHello(h)
			synctest.Wait()
			if got := rec.snapshot(); !slices.Equal(got, []bool{true}) {
				t.Fatalf("after releasing failed duplicate hello, got %v, want [true]", got)
			}
		})
	})
}
