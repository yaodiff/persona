package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestFrameGateFlushDrainsWithoutDanglingTimer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		inSend1 := make(chan struct{})
		release1 := make(chan struct{})
		inSend2 := make(chan struct{})
		release2 := make(chan struct{})

		var mu sync.Mutex
		var sent []string
		g := &frameGate{minGap: 50 * time.Millisecond, send: func(f host.Frame) {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			s := string(raw)
			mu.Lock()
			sent = append(sent, s)
			mu.Unlock()
			switch s {
			case "F1":
				close(inSend1)
				<-release1
			case "F2":
				close(inSend2)
				<-release2
			}
		}}

		g.put(host.Frame{Full: true, Data: b("F1")})
		<-inSend1
		g.put(host.Frame{Data: b("F2")})
		close(release1)
		synctest.Wait() // deliver("F1") finished within minGap and armed timer for "F2"

		flushed := make(chan struct{})
		go func() {
			g.flush()
			close(flushed)
		}()
		<-inSend2
		// A frame arrives while flush is delivering "F2".
		g.put(host.Frame{Data: b("F3")})
		close(release2)
		<-flushed

		mu.Lock()
		got := append([]string(nil), sent...)
		mu.Unlock()
		if want := []string{"F1", "F2", "F3"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("after flush returned: sent = %v, want %v (dangling pending frame/timer left behind)", got, want)
		}
		g.mu.Lock()
		hasTimer := g.timer != nil
		hasPending := g.pending != nil
		g.mu.Unlock()
		if hasTimer || hasPending {
			t.Fatalf("flush left dangling state: timer=%v pending=%v", hasTimer, hasPending)
		}
	})
}

func TestFrameGateCloseAndDiscardRejectLatePutsAndStaleTimers(t *testing.T) {
	const gap = 50 * time.Millisecond

	synctest.Test(t, func(t *testing.T) {
		b := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		var mu sync.Mutex
		var sent []string
		g := &frameGate{minGap: gap, send: func(f host.Frame) {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			mu.Lock()
			sent = append(sent, string(raw))
			mu.Unlock()
		}}

		g.put(host.Frame{Full: true, Data: b("A")})
		synctest.Wait()
		g.put(host.Frame{Data: b("B")}) // buffered behind timer
		g.mu.Lock()
		staleGen := g.timerGen
		g.mu.Unlock()

		// Manual flush drains B and invalidates staleGen.
		g.flush()
		// Buffer C behind a new timer (50ms).
		g.put(host.Frame{Data: b("C")})
		// A late callback from the invalidated timer generation must not flush C early.
		g.onTimer(staleGen)
		mu.Lock()
		gotBefore := append([]string(nil), sent...)
		mu.Unlock()
		if want := []string{"A", "B"}; fmt.Sprint(gotBefore) != fmt.Sprint(want) {
			t.Fatalf("stale timer flushed C early: sent = %v, want %v", gotBefore, want)
		}

		// Close drains C and permanently rejects later puts and timers.
		g.close()
		g.put(host.Frame{Data: b("LATE")})
		<-time.After(2 * gap)
		synctest.Wait()

		mu.Lock()
		gotAfter := append([]string(nil), sent...)
		mu.Unlock()
		if want := []string{"A", "B", "C"}; fmt.Sprint(gotAfter) != fmt.Sprint(want) {
			t.Fatalf("after close: sent = %v, want %v", gotAfter, want)
		}

		// Discard drops buffered frames without sending them and cancels pending timers.
		var discarded []string
		g2 := &frameGate{minGap: gap, send: func(f host.Frame) {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			mu.Lock()
			discarded = append(discarded, string(raw))
			mu.Unlock()
		}}
		g2.put(host.Frame{Full: true, Data: b("X")})
		synctest.Wait()
		g2.put(host.Frame{Data: b("DROP")})
		g2.discard()
		g2.put(host.Frame{Data: b("AFTER_DISCARD")})
		<-time.After(2 * gap)
		synctest.Wait()

		mu.Lock()
		gotDiscarded := append([]string(nil), discarded...)
		mu.Unlock()
		if want := []string{"X"}; fmt.Sprint(gotDiscarded) != fmt.Sprint(want) {
			t.Fatalf("after discard: sent = %v, want %v", gotDiscarded, want)
		}
	})
}

func TestFrameGateInFlightCloseAndDiscard(t *testing.T) {
	b := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	t.Run("close_while_busy_drains_pending_immediately", func(t *testing.T) {
		inSend1 := make(chan struct{})
		release1 := make(chan struct{})
		var mu sync.Mutex
		var sent []string
		g := &frameGate{minGap: time.Hour, send: func(f host.Frame) {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			s := string(raw)
			mu.Lock()
			sent = append(sent, s)
			mu.Unlock()
			if s == "F1" {
				close(inSend1)
				<-release1
			}
		}}

		g.put(host.Frame{Full: true, Data: b("F1")})
		<-inSend1
		g.put(host.Frame{Data: b("F2")})

		closed := make(chan struct{})
		go func() {
			g.close()
			close(closed)
		}()

		// Wait deterministically until close() has acquired g.mu and marked g.closed = true.
		for {
			g.mu.Lock()
			isClosed := g.closed
			g.mu.Unlock()
			if isClosed {
				break
			}
			runtime.Gosched()
		}
		select {
		case <-closed:
			t.Fatal("close() returned while F1 was still in flight")
		default:
		}

		// A concurrent put arriving after close() started must be ignored.
		g.put(host.Frame{Data: b("F3")})
		close(release1)
		<-closed

		mu.Lock()
		got := append([]string(nil), sent...)
		mu.Unlock()
		if want := []string{"F1", "F2"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("sent = %v, want %v", got, want)
		}
		g.mu.Lock()
		hasTimer, hasPending := g.timer != nil, g.pending != nil
		g.mu.Unlock()
		if hasTimer || hasPending {
			t.Fatalf("close while busy left dangling state: timer=%v pending=%v", hasTimer, hasPending)
		}
	})

	t.Run("discard_while_busy_drops_pending_without_timer", func(t *testing.T) {
		inSend1 := make(chan struct{})
		release1 := make(chan struct{})
		var mu sync.Mutex
		var sent []string
		g := &frameGate{minGap: time.Hour, send: func(f host.Frame) {
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			s := string(raw)
			mu.Lock()
			sent = append(sent, s)
			mu.Unlock()
			if s == "F1" {
				close(inSend1)
				<-release1
			}
		}}

		g.put(host.Frame{Full: true, Data: b("F1")})
		<-inSend1
		g.put(host.Frame{Data: b("F2")})

		discarded := make(chan struct{})
		go func() {
			g.discard()
			close(discarded)
		}()

		// Wait deterministically until discard() has marked g.closed and cleared g.pending.
		for {
			g.mu.Lock()
			doneMarking := g.closed && g.pending == nil
			g.mu.Unlock()
			if doneMarking {
				break
			}
			runtime.Gosched()
		}
		select {
		case <-discarded:
			t.Fatal("discard() returned while F1 was still in flight")
		default:
		}

		g.put(host.Frame{Data: b("F3")})
		close(release1)
		<-discarded

		mu.Lock()
		got := append([]string(nil), sent...)
		mu.Unlock()
		if want := []string{"F1"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("sent = %v, want %v", got, want)
		}
		g.mu.Lock()
		hasTimer, hasPending := g.timer != nil, g.pending != nil
		g.mu.Unlock()
		if hasTimer || hasPending {
			t.Fatalf("discard while busy left dangling state: timer=%v pending=%v", hasTimer, hasPending)
		}
	})
}

type fakeTermControl struct {
	done    chan struct{}
	once    sync.Once
	onClose func()
}

func (c *fakeTermControl) Input(string) error               { return nil }
func (c *fakeTermControl) Resize(int, int) error            { return nil }
func (c *fakeTermControl) Scroll(int, bool, int, int) error { return nil }
func (c *fakeTermControl) Click(int, int) error             { return nil }
func (c *fakeTermControl) Done() <-chan struct{}            { return c.done }
func (c *fakeTermControl) Err() error                       { return errors.New("closed") }
func (c *fakeTermControl) Close() {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
		close(c.done)
	})
}

type failingTermController struct {
	onControl func(fn func(host.Frame)) error
}

func (c *failingTermController) CanControl() bool { return true }
func (c *failingTermController) Control(_ context.Context, _ host.PaneRef, _, _ int, _ bool, fn func(host.Frame)) (host.Control, error) {
	return nil, c.onControl(fn)
}

func TestTerminalControlErrorDiscardsBufferedFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		n2hR, n2hW := io.Pipe()
		h2nR, h2nW := io.Pipe()
		defer n2hW.Close()
		defer h2nW.Close()

		n := &Node{
			peer:  rpc.New("n", n2hW),
			terms: map[string]*termStream{},
		}
		hub := rpc.New("h", h2nW)

		var mu sync.Mutex
		var frames []string
		hub.OnEvent = func(event string, params json.RawMessage) {
			if event != "terminal.frame" {
				return
			}
			var p struct {
				Data string `json:"data"`
			}
			if json.Unmarshal(params, &p) == nil {
				raw, _ := base64.StdEncoding.DecodeString(p.Data)
				mu.Lock()
				frames = append(frames, string(raw))
				mu.Unlock()
			}
		}
		hubDone := make(chan struct{})
		nodeDone := make(chan struct{})
		go func() { defer close(hubDone); _ = hub.Serve(ctx, n2hR) }()
		go func() { defer close(nodeDone); _ = n.peer.Serve(ctx, h2nR) }()
		defer func() {
			cancel()
			_ = n2hW.Close()
			_ = h2nW.Close()
			<-hubDone
			<-nodeDone
		}()

		b := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		wantErr := errors.New("control startup failed")
		ctlr := &failingTermController{
			onControl: func(fn func(host.Frame)) error {
				// Emit F1 (sent immediately) and F2 (buffered behind termMinGap timer), then fail.
				fn(host.Frame{Full: true, Width: 80, Height: 24, Data: b("F1")})
				synctest.Wait()
				fn(host.Frame{Width: 80, Height: 24, Data: b("F2")})
				return wantErr
			},
		}

		_, err := n.terminalControl(ctx, "s1", ctlr, host.PaneRef{ID: "w1:p1"}, termControl{Cols: 80, Rows: 24})
		if !errors.Is(err, wantErr) {
			t.Fatalf("terminalControl err = %v, want %v", err, wantErr)
		}

		// Advance well past termMinGap: gate.discard() must have dropped F2 and stopped its timer.
		<-time.After(5 * time.Second)
		synctest.Wait()

		mu.Lock()
		got := append([]string(nil), frames...)
		mu.Unlock()
		if want := []string{"F1"}; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("terminal.frame payloads after failed control start = %v, want %v", got, want)
		}
	})
}

func TestControlLoopNoFramesOrSendCallsAfterTerminalEndOnLeaseExpiry(t *testing.T) {
	// gap > lease-expiry tick (lease=50ms -> 6th 10ms tick at t=60ms) so lease expiry
	// unambiguously triggers controlLoop -> gate.close() while F2 is still held behind the timer.
	const gap = 200 * time.Millisecond
	const lease = 50 * time.Millisecond

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		n2hR, n2hW := io.Pipe()
		h2nR, h2nW := io.Pipe()
		defer n2hW.Close()
		defer h2nW.Close()

		n := &Node{
			peer:  rpc.New("n", n2hW),
			terms: map[string]*termStream{},
		}
		hub := rpc.New("h", h2nW)

		var mu sync.Mutex
		var events []string
		var framesBeforeEnd []string
		endSeen := make(chan struct{})
		var endOnce sync.Once
		var ended atomic.Bool
		var sendAfterEnd atomic.Int32
		hub.OnEvent = func(event string, params json.RawMessage) {
			mu.Lock()
			events = append(events, event)
			if event == "terminal.frame" && !ended.Load() {
				var p struct {
					Data string `json:"data"`
				}
				if json.Unmarshal(params, &p) == nil {
					raw, _ := base64.StdEncoding.DecodeString(p.Data)
					framesBeforeEnd = append(framesBeforeEnd, string(raw))
				}
			}
			mu.Unlock()
			if event == "terminal.end" {
				ended.Store(true)
				endOnce.Do(func() { close(endSeen) })
			}
		}
		hubDone := make(chan struct{})
		nodeDone := make(chan struct{})
		go func() { defer close(hubDone); _ = hub.Serve(ctx, n2hR) }()
		go func() { defer close(nodeDone); _ = n.peer.Serve(ctx, h2nR) }()
		defer func() {
			cancel()
			_ = n2hW.Close()
			_ = h2nW.Close()
			<-hubDone
			<-nodeDone
		}()

		b := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
		sctx, scancel := context.WithCancel(ctx)
		s := &termStream{cancel: scancel, lease: time.Now(), leaseFor: lease}

		inFlushSend := make(chan struct{})
		releaseFlushSend := make(chan struct{})
		var gate *frameGate
		ctrl := &fakeTermControl{
			done: make(chan struct{}),
			onClose: func() {
				// Simulate a final frame arriving while control is tearing down.
				gate.put(host.Frame{Data: b("ON_CLOSE")})
			},
		}
		s.ctl = ctrl
		n.terms["s1"] = s

		firstFlushFrame := true
		gate = &frameGate{minGap: gap, send: func(f host.Frame) {
			if ended.Load() {
				sendAfterEnd.Add(1)
			}
			if s.isLeaving() {
				return
			}
			raw, _ := base64.StdEncoding.DecodeString(f.Data)
			if string(raw) == "F2" && firstFlushFrame {
				firstFlushFrame = false
				close(inFlushSend)
				<-releaseFlushSend
			}
			_ = n.peer.Notify("terminal.frame", map[string]any{"session_id": "s1", "data": f.Data})
		}}

		// Prime F1 (sent immediately) and F2 (held behind the 200ms minGap timer).
		gate.put(host.Frame{Full: true, Data: b("F1")})
		synctest.Wait()
		gate.put(host.Frame{Data: b("F2")})

		loopDone := make(chan struct{})
		go func() {
			defer close(loopDone)
			n.controlLoop(sctx, "s1", s, gate)
		}()

		// Wait for lease expiry at t=60ms to trigger controlLoop -> gate.close() draining F2.
		<-inFlushSend
		gate.mu.Lock()
		closedDuringDrain := gate.closed
		gate.mu.Unlock()
		if !closedDuringDrain {
			t.Fatal("expected controlLoop -> gate.close() (not rate-limit timer) to drain F2")
		}
		// While controlLoop is draining F2 (before letGo runs), control emits F3; close() must drop it.
		gate.put(host.Frame{Data: b("F3")})
		close(releaseFlushSend)

		<-endSeen
		<-loopDone
		// Advance past minGap to catch any dangling timer firing after terminal.end.
		<-time.After(2 * gap)
		synctest.Wait()

		if n := sendAfterEnd.Load(); n != 0 {
			t.Fatalf("gate.send invoked %d time(s) after terminal.end", n)
		}
		mu.Lock()
		gotEvents := append([]string(nil), events...)
		gotFrames := append([]string(nil), framesBeforeEnd...)
		mu.Unlock()
		endIdx := -1
		for i, ev := range gotEvents {
			if ev == "terminal.end" {
				endIdx = i
				break
			}
		}
		if endIdx < 0 {
			t.Fatalf("missing terminal.end in %v", gotEvents)
		}
		for _, ev := range gotEvents[endIdx+1:] {
			if ev == "terminal.frame" {
				t.Fatalf("terminal.frame emitted after terminal.end: %v", gotEvents)
			}
		}
		if want := []string{"F1", "F2"}; fmt.Sprint(gotFrames) != fmt.Sprint(want) {
			t.Fatalf("terminal.frame payloads before terminal.end = %v, want %v (F3 and ON_CLOSE must be dropped)", gotFrames, want)
		}
	})
}
