package rpc

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// holdReadConn wraps a net.Conn so a test can hold Read blocked across a resume
// takeover (simulating an old transport whose Read unwinds after AcceptResume
// installs the next generation), while writes are dropped silently.
type holdReadConn struct {
	net.Conn
	hold        atomic.Bool
	readBlocked chan struct{}
	blockOnce   sync.Once
	releaseRead chan struct{}
	releaseOnce sync.Once
}

func newHoldReadConn(c net.Conn) *holdReadConn {
	return &holdReadConn{
		Conn:        c,
		readBlocked: make(chan struct{}),
		releaseRead: make(chan struct{}),
	}
}

func (c *holdReadConn) Read(b []byte) (int, error) {
	if c.hold.Load() {
		c.blockOnce.Do(func() { close(c.readBlocked) })
		<-c.releaseRead
		return 0, io.EOF
	}
	n, err := c.Conn.Read(b)
	if c.hold.Load() {
		c.blockOnce.Do(func() { close(c.readBlocked) })
		<-c.releaseRead
		return 0, io.EOF
	}
	return n, err
}

func (c *holdReadConn) Write(b []byte) (int, error) {
	if c.hold.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *holdReadConn) release() {
	c.releaseOnce.Do(func() { close(c.releaseRead) })
}

func (c *holdReadConn) Close() error {
	// Intentionally leave releaseRead open while hold is active so Read stays
	// unwinding until the test explicitly releases it.
	return c.Conn.Close()
}

// When an old transport's Read has waited longer than resumeStale and is still
// unwinding as AcceptResume installs the next generation, watchStale must not
// attribute that stale wait timestamp to the new generation and lose it.
func TestWatchStaleIgnoresUnwindingOldTransportAcrossResume(t *testing.T) {
	oldStale, oldCheck, oldAck := resumeStale, resumeCheck, allAckDelay
	resumeStale, resumeCheck, allAckDelay = 120*time.Millisecond, 15*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		resumeStale, resumeCheck, allAckDelay = oldStale, oldCheck, oldAck
	})

	rawA, rawB := net.Pipe()
	a := newHoldReadConn(rawA)
	defer a.release()
	b := rawB

	h, n := New("h", a), New("n", b)
	hLost := make(chan error, 8)
	nLost := make(chan error, 8)
	for _, side := range []struct {
		p    *Peer
		lost chan error
	}{{h, hLost}, {n, nLost}} {
		side.p.EnableLanes([]string{"ping"}, []string{"bulk"})
		side.p.EnableResume(5 * time.Second)
		lost := side.lost
		side.p.OnLost = func(err error) { lost <- err }
		side.p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return map[string]any{}, nil
		}
	}

	hServed := make(chan error, 1)
	nServed := make(chan error, 1)
	go func() { hServed <- h.Serve(context.Background(), a) }()
	go func() { nServed <- n.Serve(context.Background(), b) }()
	t.Cleanup(func() {
		a.release()
		h.Close(ErrClosed)
		n.Close(ErrClosed)
		_ = a.Close()
		_ = b.Close()
		<-hServed
		<-nServed
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.UpgradeLanes(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return n.Framed() && h.readsResumable() && n.readsResumable() })

	// Put transport gen 1 into silent hold so h's swapReader.Read blocks on a.Read
	// without receiving ACKs for the frame h writes next.
	a.hold.Store(true)
	_ = b.Close()
	select {
	case <-nLost:
	case <-time.After(3 * time.Second):
		t.Fatal("node side did not notice cut")
	}

	// Send a frame from h so lw.oldestKept() is non-zero; h's a.Read will block on releaseRead.
	if err := h.Notify("ev", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-a.readBlocked:
	case <-time.After(3 * time.Second):
		t.Fatal("old transport Read did not enter blocked state")
	}

	// Wait for watchStale to declare gen 1 silent (proving gen 1's wait timestamp > resumeStale).
	select {
	case err := <-hLost:
		if err == nil || !strings.Contains(err.Error(), errStale.Error()) {
			t.Fatalf("gen 1 lost err = %v, want %v", err, errStale)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watchStale did not detect silent gen 1")
	}

	// Resume on a new transport (a2, b2) while a.Read (gen 1) is STILL unwinding!
	a2, b2 := net.Pipe()
	t.Cleanup(func() {
		_ = a2.Close()
		_ = b2.Close()
	})
	accepted := make(chan error, 1)
	go func() {
		line, rest, err := ReadFirstLine(a2, resumeLineMax)
		if err != nil {
			accepted <- err
			return
		}
		req, ok := ParseResume(line)
		if !ok {
			accepted <- io.ErrUnexpectedEOF
			return
		}
		accepted <- h.AcceptResume(req, rest, a2)
	}()
	if err := n.Resume(ctx, b2, b2); err != nil {
		t.Fatalf("node Resume: %v", err)
	}
	if err := <-accepted; err != nil {
		t.Fatalf("hub AcceptResume: %v", err)
	}

	// While gen 1's a.Read is STILL blocked and h has an unacked frame (a2 has not been read
	// by h yet), watchStale must NOT attribute gen 1's old waiting timestamp to gen 2.
	select {
	case err := <-hLost:
		t.Fatalf("watchStale falsely lost newly resumed gen 2 while gen 1 Read was unwinding: %v", err)
	case <-time.After(resumeStale + 4*resumeCheck):
	}

	// Now let gen 1's Read finish unwinding; h switches to a2 and normal RPC calls succeed.
	a.release()
	var out map[string]any
	if err := h.Call(ctx, "ping", map[string]any{}, &out); err != nil {
		t.Fatalf("Call after resume failed: %v", err)
	}
	if h.Lost() {
		t.Fatal("hub peer unexpectedly lost after resume")
	}
}

// swapReader must track waiting timestamps using Go's monotonic clock (not UnixNano wall clock),
// fence reads by generation, and clear any older generation's wait stamp on install.
func TestSwapReaderWaitingTimestampPreservesMonotonicClockAndGeneration(t *testing.T) {
	p := New("h", io.Discard)
	p.EnableLanes(nil, nil)
	p.EnableResume(time.Second)

	r, w := io.Pipe()
	defer w.Close()
	sr := newSwapReader(p, r)

	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 16)
		_, _ = sr.Read(buf)
	}()

	var st time.Time
	eventually(t, func() bool {
		st = sr.waitingSince(0)
		return !st.IsZero()
	})
	if !strings.Contains(st.String(), " m=") {
		t.Fatalf("waitingSince timestamp %q lost monotonic clock reading", st.String())
	}
	if got := sr.waitingSince(1); !got.IsZero() {
		t.Fatalf("waitingSince for next generation = %v, want zero", got)
	}

	// Installing generation 1 immediately clears generation 0's wait stamp.
	r2, w2 := io.Pipe()
	defer r2.Close()
	defer w2.Close()
	sr.install(r2, 1)
	if got := sr.waitingSince(0); !got.IsZero() {
		t.Fatalf("waitingSince(0) after install(1) = %v, want zero", got)
	}
	if got := sr.waitingSince(1); !got.IsZero() {
		t.Fatalf("waitingSince(1) before gen 1 Read = %v, want zero", got)
	}

	_ = r.Close()
	<-done
}
