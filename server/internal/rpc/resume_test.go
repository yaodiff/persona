package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/netshape"
)

// resumeLink is a manager peer h and a node peer n, framed and resumable over a shaped link that a test cuts (both
// ends closed, what was on its way lost) and resumes over a new one.
type resumeLink struct {
	t            *testing.T
	link         netshape.Config
	h, n         *Peer
	hLost, nLost chan error
	mu           sync.Mutex
	a, b         net.Conn
	served       chan error
}

// newResumeLink makes the pair; setup, when set, configures both peers (handlers) before they serve. hResume and
// nResume enable resuming on each side with window (0: not).
func newResumeLink(t *testing.T, link netshape.Config, window time.Duration, hResume, nResume bool, setup func(h, n *Peer)) *resumeLink {
	t.Helper()
	l := &resumeLink{t: t, link: link, hLost: make(chan error, 8), nLost: make(chan error, 8), served: make(chan error, 2)}
	l.a, l.b = silentPipe(link)
	l.h, l.n = New("h", l.a), New("n", l.b)
	for _, side := range []struct {
		p      *Peer
		resume bool
		lost   chan error
	}{{l.h, hResume, l.hLost}, {l.n, nResume, l.nLost}} {
		side.p.EnableLanes([]string{"ping"}, []string{"bulk", "bulk.event"})
		if side.resume {
			side.p.EnableResume(window)
		}
		lost := side.lost
		side.p.OnLost = func(err error) { lost <- err }
		side.p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	}
	if setup != nil {
		setup(l.h, l.n)
	}
	go func() { l.served <- l.h.Serve(context.Background(), l.a) }()
	go func() { l.served <- l.n.Serve(context.Background(), l.b) }()
	t.Cleanup(func() {
		l.h.Close(ErrClosed)
		l.n.Close(ErrClosed)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.a.Close()
		l.b.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.h.UpgradeLanes(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return l.n.Framed() })
	if hResume && nResume { // both inputs framed too: a cut before the node read the marker line ends the connection
		eventually(t, func() bool { return l.h.readsResumable() && l.n.readsResumable() })
	}
	return l
}

// readsResumable: this side's input is framed and read through a transport a resume can replace.
func (p *Peer) readsResumable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resume != nil && p.resume.sr != nil
}

// silentConn is a transport that can go silent without a word (silence): what it is given goes nowhere, nothing comes,
// and neither end is told, as when a computer's address changes.
type silentConn struct {
	net.Conn
	silent atomic.Bool
	gone   chan struct{}
	once   sync.Once
}

func silentPipe(link netshape.Config) (net.Conn, net.Conn) {
	a, b := netshape.Pipe(link, link)
	return &silentConn{Conn: a, gone: make(chan struct{})}, &silentConn{Conn: b, gone: make(chan struct{})}
}

func (c *silentConn) Read(b []byte) (int, error) {
	n, err := 0, error(nil)
	if !c.silent.Load() {
		n, err = c.Conn.Read(b)
	}
	if c.silent.Load() {
		<-c.gone
		return 0, net.ErrClosed
	}
	return n, err
}

func (c *silentConn) Write(b []byte) (int, error) {
	if c.silent.Load() {
		return len(b), nil
	}
	return c.Conn.Write(b)
}

func (c *silentConn) Close() error {
	c.once.Do(func() { close(c.gone) })
	return c.Conn.Close()
}

// silence makes the transport go silent both ways.
func (l *resumeLink) silence() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.a.(*silentConn).silent.Store(true)
	l.b.(*silentConn).silent.Store(true)
}

// cut closes both ends of the transport: what was on its way either way is lost.
func (l *resumeLink) cut() {
	l.mu.Lock()
	a, b := l.a, l.b
	l.mu.Unlock()
	a.Close()
	b.Close()
}

// waitLost waits until both sides were told the transport was lost.
func (l *resumeLink) waitLost() {
	l.t.Helper()
	for _, ch := range []chan error{l.hLost, l.nLost} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			l.t.Fatal("a side was not told its transport was lost")
		}
	}
}

// resume connects the two again over a new link: h asks with its token, n answers as a resident node does, from the
// first line of the new transport.
func (l *resumeLink) resume() error {
	a, b := silentPipe(l.link)
	accepted := make(chan error, 1)
	go func() {
		line, rest, err := ReadFirstLine(b, resumeLineMax)
		if err != nil {
			accepted <- err
			return
		}
		req, ok := ParseResume(line)
		if !ok {
			accepted <- fmt.Errorf("not an rpc.resume: %q", line)
			return
		}
		accepted <- l.n.AcceptResume(req, rest, b)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := l.h.Resume(ctx, a, a)
	if aerr := <-accepted; err == nil {
		err = aerr
	}
	if err != nil {
		a.Close()
		b.Close()
		return err
	}
	l.mu.Lock()
	l.a, l.b = a, b
	l.mu.Unlock()
	return nil
}

// A cut in the middle of traffic both ways: calls in flight answer once the connection is resumed, each once, and the
// node's events arrive each once and in order, the ones lost on the way with the old transport written again.
func TestResumeKeepsCallsAndEventsExactlyOnce(t *testing.T) {
	const events = 1500
	release := make(chan struct{})
	var mu sync.Mutex
	var got []int
	var dup, gap int
	l := newResumeLink(t, netshape.Config{Rate: 20_000_000, Delay: 10 * time.Millisecond, Queue: 256 << 10}, 10*time.Second, true, true, func(h, n *Peer) {
		n.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			if method == "slow" {
				<-release
			}
			return json.RawMessage(params), nil
		}
		h.OnEvent = func(event string, params json.RawMessage) {
			var e struct {
				I int `json:"i"`
			}
			_ = json.Unmarshal(params, &e)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case len(got) > 0 && e.I <= got[len(got)-1]:
				dup++
			case e.I != len(got)+1:
				gap++
			}
			got = append(got, e.I)
		}
	})
	sent := make(chan error, 1)
	go func() { // events of every size, some longer than a frame
		for i := 1; i <= events; i++ {
			pad := strings.Repeat("x", (i%5)*9000)
			if err := l.n.Notify("bulk.event", map[string]any{"i": i, "pad": pad}); err != nil {
				sent <- err
				return
			}
		}
		sent <- nil
	}()
	slow := make(chan error, 8)
	for i := range 8 {
		go func() {
			var out map[string]int
			err := l.h.Call(context.Background(), "slow", map[string]int{"k": i}, &out)
			if err == nil && out["k"] != i {
				err = fmt.Errorf("answer %v to call %d", out, i)
			}
			slow <- err
		}()
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) > events/4 })
	l.cut()
	l.waitLost()
	if !l.h.Lost() || !l.n.Lost() {
		t.Fatal("a side not waiting to be resumed after the cut")
	}
	if err := l.resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	close(release)
	for range 8 {
		select {
		case err := <-slow:
			if err != nil {
				t.Fatalf("a call in flight across the cut: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("a call in flight never answered")
		}
	}
	if err := <-sent; err != nil {
		t.Fatalf("events: %v", err)
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= events })
	mu.Lock()
	defer mu.Unlock()
	if len(got) != events || dup > 0 || gap > 0 {
		t.Fatalf("%d events for %d sent, %d twice, %d out of order", len(got), events, dup, gap)
	}
	if l.h.LaneStats().Resent == 0 && l.n.LaneStats().Resent == 0 {
		t.Fatal("nothing written again: the cut lost nothing on its way")
	}
}

// A second cut while the first resume is writing again what was lost: the next resume takes it from where that one got.
func TestResumeAgainDuringAResume(t *testing.T) {
	const events = 2000
	var mu sync.Mutex
	var got []int
	l := newResumeLink(t, netshape.Config{Rate: 4_000_000, Delay: 20 * time.Millisecond, Queue: 256 << 10}, 10*time.Second, true, true, func(h, n *Peer) {
		h.OnEvent = func(event string, params json.RawMessage) {
			var e struct {
				I int `json:"i"`
			}
			_ = json.Unmarshal(params, &e)
			mu.Lock()
			got = append(got, e.I)
			mu.Unlock()
		}
	})
	go func() {
		for i := 1; i <= events; i++ {
			if l.n.Notify("bulk.event", map[string]any{"i": i, "pad": strings.Repeat("y", 4000)}) != nil {
				return
			}
		}
	}()
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) > 200 })
	l.cut()
	l.waitLost()
	if err := l.resume(); err != nil {
		t.Fatalf("first resume: %v", err)
	}
	l.cut() // at once: what the first resume wrote again is on its way, partly
	l.waitLost()
	if err := l.resume(); err != nil {
		t.Fatalf("second resume: %v", err)
	}
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= events })
	mu.Lock()
	defer mu.Unlock()
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("event %d is %d (%d events)", i+1, v, len(got))
		}
	}
	if len(got) != events {
		t.Fatalf("%d events for %d", len(got), events)
	}
}

// Not resumed within the window: the peer closes as a connection always did, its calls dropped.
func TestResumeWindowEnds(t *testing.T) {
	arrived := make(chan struct{}, 1)
	l := newResumeLink(t, netshape.Config{}, 200*time.Millisecond, true, true, func(h, n *Peer) {
		n.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			select {
			case arrived <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}
	})
	call := make(chan error, 1)
	go func() { call <- l.h.Call(context.Background(), "wait", nil, nil) }()
	// In flight: the node has the request. Pending alone is not enough: a call is pending before its line is written,
	// and one cut before then was never sent (ErrClosed, not ErrDropped).
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the node")
	}
	l.cut()
	l.waitLost()
	select {
	case err := <-call:
		if !errors.Is(err, ErrDropped) {
			t.Fatalf("the call: %v, want dropped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the call still waits after the window")
	}
	if !strings.Contains(fmt.Sprint(l.h.Err()), "not resumed within") {
		t.Fatalf("closed with %v", l.h.Err())
	}
	if err := l.resume(); err == nil {
		t.Fatal("resumed after the window")
	}
}

// A peer that never enabled resuming answers rpc.lanes as before: nothing is kept, and a cut ends the connection.
func TestResumeNeedsBothSides(t *testing.T) {
	for _, c := range []struct{ h, n bool }{{true, false}, {false, true}} {
		l := newResumeLink(t, netshape.Config{}, time.Second, c.h, c.n, nil)
		if l.h.Resumable() || l.n.Resumable() {
			t.Fatalf("resumable with h %t, n %t", c.h, c.n)
		}
		l.cut()
		for range 2 {
			select {
			case <-l.served:
			case <-time.After(5 * time.Second):
				t.Fatalf("h %t, n %t: a side still serves after the cut", c.h, c.n)
			}
		}
		select {
		case err := <-l.hLost:
			t.Fatalf("h told lost (%v) on a connection that cannot resume", err)
		default:
		}
	}
}

// A token is the connection's, once: one from before the last resume, a made-up one, or one given to another
// process (a peer with nothing to resume) is refused, and nothing is written on the new transport.
func TestResumeTokenOnceAndOwn(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, nil)
	l.h.mu.Lock()
	first := l.h.resume.token
	l.h.mu.Unlock()
	l.cut()
	l.waitLost()
	if err := l.resume(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.h.Call(ctx, "echo", nil, nil); err != nil { // the new transport carried something: the answer came
		t.Fatal(err)
	}
	other := New("n", io.Discard)
	other.EnableLanes(nil, nil)
	other.EnableResume(time.Second)
	for name, c := range map[string]struct {
		p     *Peer
		token string
	}{
		"used":    {l.n, first},
		"made up": {l.n, newResumeToken()},
		"other":   {other, first},
	} {
		var w bytes.Buffer
		if err := c.p.AcceptResume(ResumeRequest{ID: "h99", Token: c.token}, strings.NewReader(""), &w); !errors.Is(err, ErrResumeRefused) {
			t.Errorf("%s token: %v", name, err)
		}
		if w.Len() > 0 {
			t.Errorf("%s token: wrote %q", name, w.String())
		}
	}
	if l.n.Lost() {
		t.Fatal("a refused token suspended the connection")
	}
	var out map[string]any
	if err := l.h.Call(ctx, "echo", nil, &out); err != nil {
		t.Fatalf("after the refusals: %v", err)
	}
}

// The node side may not have seen its transport fail when the manager comes back on a new one (a half-open link):
// the resume takes the connection over all the same, and the old transport is let go.
func TestResumeBeforeTheOtherSideNoticed(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, nil)
	l.mu.Lock()
	oldA, oldB := l.a, l.b
	l.mu.Unlock()
	oldA.Close() // the manager's end only: the node's still looks open
	select {
	case <-l.hLost:
	case <-time.After(5 * time.Second):
		t.Fatal("h not told lost")
	}
	if err := l.resume(); err != nil {
		t.Fatal(err)
	}
	oldB.Close() // as the resident node does once a resume took over
	var out map[string]any
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.h.Call(ctx, "echo", nil, &out); err != nil {
		t.Fatalf("after resuming: %v", err)
	}
	if l.n.Lost() || l.h.Lost() {
		t.Fatal("still suspended after resuming")
	}
}

// What a writer keeps for a resume is bounded: past resumeKeepMax nothing is kept through the frame that went over (a
// gap), and the connection is resumable again once the other side acknowledges past it.
func TestResumeKeepIsBounded(t *testing.T) {
	old := resumeKeepMax
	resumeKeepMax = 100 << 10
	t.Cleanup(func() { resumeKeepMax = old })
	lw := newLaneWriter(io.Discard, make(chan struct{}), func(error) {}, nil)
	lw.keep = true
	now := time.Now()
	frame := make([]byte, 30<<10)
	for seq := uint64(1); seq <= 3; seq++ {
		lw.seq = seq
		lw.keepLocked(seq, frame, now)
	}
	if !lw.resumable() || !lw.canResume(0) {
		t.Fatal("90 KiB kept: not resumable")
	}
	lw.seq = 4
	lw.keepLocked(4, frame, now) // 120 KiB: over
	if lw.keptBytes != 0 || lw.resumable() || lw.canResume(2) {
		t.Fatalf("over the bound: %d bytes kept, resumable %t", lw.keptBytes, lw.resumable())
	}
	lw.seq = 5
	lw.keepLocked(5, frame, now)
	if err := lw.acked(4, 0); err != nil {
		t.Fatal(err)
	}
	if !lw.resumable() || !lw.canResume(4) || lw.canResume(3) {
		t.Fatal("the gap acknowledged: not resumable from it")
	}
	lw.seq = 6
	lw.keepLocked(6, frame, now.Add(resumeKeepAge+time.Second)) // the oldest kept too old: the other side is not reading
	if lw.resumable() {
		t.Fatal("frames kept unacknowledged past resumeKeepAge: still resumable")
	}
}

// pendingMethods is what a test waits on: the calls waiting for an answer.
func (p *Peer) pendingMethods() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, m := range p.methods {
		out = append(out, m)
	}
	return out
}

// A transport that goes silent without ending (an address changed) is lost once nothing was read on it for
// resumeStale while frames wait to be acknowledged: each side's reads would wait forever otherwise, and the calls
// behind them. A quiet one with nothing to acknowledge is left alone.
func TestResumeLosesASilentTransport(t *testing.T) {
	oldStale, oldCheck := resumeStale, resumeCheck
	resumeStale, resumeCheck = 300*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { resumeStale, resumeCheck = oldStale, oldCheck })
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, nil)
	<-time.After(3 * resumeStale) // quiet: nothing waits to be acknowledged
	if l.h.Lost() || l.n.Lost() {
		t.Fatal("a quiet transport was lost")
	}
	l.silence()
	call := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		call <- l.h.Call(ctx, "echo", nil, nil)
	}()
	select {
	case <-l.hLost:
	case <-time.After(5 * time.Second):
		t.Fatal("the silent transport was never lost")
	}
	l.mu.Lock()
	oldA, oldB := l.a, l.b
	l.mu.Unlock()
	oldA.Close() // as the manager does when told
	if err := l.resume(); err != nil {
		t.Fatal(err)
	}
	oldB.Close() // as the resident node does once a resume took its connection over
	if err := <-call; err != nil {
		t.Fatalf("the call made while the transport was silent: %v", err)
	}
}

// After a resume a frame may come again that was read over the old transport after this side told how far it read
// (the other side took the connection back before this one saw the old transport fail): it is dropped, and the line
// it belongs to comes once.
func TestResumeDropsFramesReadTwice(t *testing.T) {
	line := func(i int) []byte { return []byte(fmt.Sprintf(`{"event":"e","params":{"i":%d}}`, i)) }
	var lines [][]byte
	for i := 1; i <= 7; i++ {
		lines = append(lines, line(i))
	}
	first, _ := frames(0, LaneEvents, framePayloadMax, lines[:5]...) // 1..5 over the old transport
	again, _ := frames(2, LaneEvents, framePayloadMax, lines[2:]...) // 3..7 written again after a resume
	fr := newFrameReader(&resumedReader{parts: [][]byte{first, again}}, 64<<20)
	var got []string
	for fr.Scan() {
		got = append(got, string(fr.Bytes()))
	}
	if err := fr.Err(); err != nil {
		t.Fatalf("after %d lines: %v", len(got), err)
	}
	if len(got) != 7 {
		t.Fatalf("%d lines, want 7: %q", len(got), got)
	}
	for i, g := range got {
		if g != string(lines[i]) {
			t.Fatalf("line %d is %s", i+1, g)
		}
	}
}

// resumedReader hands out its parts one after the other, errResumed between two as a replaced transport does.
type resumedReader struct {
	parts [][]byte
	gap   bool
}

func (r *resumedReader) Read(b []byte) (int, error) {
	if len(r.parts) == 0 {
		return 0, io.EOF
	}
	if r.gap {
		r.gap = false
		return 0, errResumed
	}
	n := copy(b, r.parts[0])
	if r.parts[0] = r.parts[0][n:]; len(r.parts[0]) == 0 {
		r.parts = r.parts[1:]
		r.gap = len(r.parts) > 0
	}
	return n, nil
}

// A resume writes again only what the other side did not read: the frames after the last one it read.
func TestResumeWritesAgainFromWhatWasRead(t *testing.T) {
	lw := newLaneWriter(io.Discard, make(chan struct{}), func(error) {}, nil)
	lw.keep = true
	now := time.Now()
	for seq := uint64(1); seq <= 5; seq++ {
		lw.seq = seq
		f, _ := frames(seq-1, LaneEvents, framePayloadMax, []byte(fmt.Sprintf(`{"event":"e%d"}`, seq)))
		lw.keepLocked(seq, f, now)
	}
	lw.resume(io.Discard, 1, 3)
	lw.mu.Lock()
	batch := lw.resendLocked(nil)
	lw.mu.Unlock()
	heads, _ := heads(t, batch)
	var seqs []uint64
	for _, h := range heads {
		if h.kind == frameData {
			seqs = append(seqs, h.seq)
		}
	}
	if fmt.Sprint(seqs) != "[4 5]" {
		t.Fatalf("written again: %v, want [4 5]", seqs)
	}
}

// What a side keeps for a resume goes once the other side read it: every frame read is acknowledged, those of lanes
// without credit too, soon after (allAckDelay) when nothing else is written the other way.
func TestResumeKeptGoesOnceRead(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, nil)
	for i := range 50 {
		if err := l.n.Notify("ev", map[string]int{"i": i}); err != nil { // the events lane: no credit, no other acknowledgement
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return l.n.LaneStats().Kept == 0 })
}

// The transport replaced while this side's read loop was busy with a line, not reading (its OnEvent, say): the next
// read still starts afresh, the part of a frame left from the old transport dropped and frames read twice let go, so
// every event comes once and in order (#10829's review).
func TestResumeWhileTheReaderIsBusy(t *testing.T) {
	const events = 400
	hold := make(chan struct{})
	var mu sync.Mutex
	var got []int
	l := newResumeLink(t, netshape.Config{Rate: 4_000_000, Delay: 5 * time.Millisecond, Queue: 256 << 10}, 10*time.Second, true, true, func(h, n *Peer) {
		h.OnEvent = func(event string, params json.RawMessage) {
			var e struct {
				I int `json:"i"`
			}
			_ = json.Unmarshal(params, &e)
			if e.I == 50 {
				<-hold // busy with this line while the transport changes
			}
			mu.Lock()
			got = append(got, e.I)
			mu.Unlock()
		}
	})
	go func() {
		for i := 1; i <= events; i++ {
			if l.n.Notify("ev", map[string]any{"i": i, "pad": strings.Repeat("z", 3000)}) != nil {
				return
			}
		}
	}()
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 49 })
	<-time.After(50 * time.Millisecond) // more of the node's frames in h's buffer, one in part
	l.cut()
	if !l.h.Lose(errors.New("pings unanswered")) { // as the manager does: its reader is not reading to see the cut
		t.Fatal("not resumable")
	}
	select {
	case <-l.nLost:
	case <-time.After(5 * time.Second):
		t.Fatal("n not told lost")
	}
	if err := l.resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	close(hold)
	eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(got) >= events })
	mu.Lock()
	defer mu.Unlock()
	for i, v := range got {
		if v != i+1 {
			t.Fatalf("event %d is %d", i+1, v)
		}
	}
	if err := l.h.Err(); err != nil {
		t.Fatalf("closed: %v", err)
	}
}

// A connection taken over before its side saw the old transport fail, whose answer then cannot be written (the new
// transport failed too), waits out its window like any lost connection, instead of for ever (#10829's review).
func TestResumeAnswerFailsAndTheWindowRuns(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, 300*time.Millisecond, true, true, nil)
	l.n.mu.Lock()
	token := l.n.resume.token
	l.n.mu.Unlock()
	if err := l.n.AcceptResume(ResumeRequest{ID: "h9", Token: token}, strings.NewReader(""), failingWriter{}); err == nil || errors.Is(err, ErrResumeRefused) {
		t.Fatalf("an answer that could not be written: %v", err)
	}
	if !l.n.Lost() {
		t.Fatal("not lost after a failed take over")
	}
	select {
	case <-l.n.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("still open after its window")
	}
}

// The answer to a resume can be lost on its way (the new transport dropped right after it was written): the other side
// asks again with the token it has, which is still taken until the new transport carries something (#10829's review).
func TestResumeAnswerLostAsksAgain(t *testing.T) {
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, nil)
	l.h.mu.Lock()
	first := l.h.resume.token
	l.h.mu.Unlock()
	l.cut()
	l.waitLost()
	a, b := silentPipe(netshape.Config{})
	go func() { _, _ = io.Copy(io.Discard, a) }() // the answer goes nowhere
	accepted := make(chan error, 1)
	go func() {
		line, rest, err := ReadFirstLine(b, resumeLineMax)
		if err != nil {
			accepted <- err
			return
		}
		req, _ := ParseResume(line)
		accepted <- l.n.AcceptResume(req, rest, b)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	never, _ := io.Pipe()
	if err := l.h.Resume(ctx, never, a); err == nil {
		t.Fatal("a resume whose answer never came succeeded")
	}
	cancel()
	if err := <-accepted; err != nil {
		t.Fatalf("the node's side: %v", err)
	}
	a.Close() // the manager lets the transport go
	b.Close()
	if err := l.resume(); err != nil {
		t.Fatalf("asked again with the same token: %v", err)
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer ccancel()
	if err := l.h.Call(cctx, "echo", nil, nil); err != nil {
		t.Fatalf("after resuming: %v", err)
	}
	var w bytes.Buffer // the new transport carried a call: the token asked with twice is used now
	if err := l.n.AcceptResume(ResumeRequest{ID: "h98", Token: first}, strings.NewReader(""), &w); !errors.Is(err, ErrResumeRefused) {
		t.Fatalf("the used token once the new transport carried a call: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the new transport failed") }

// A read loop busy with a line does not read its transport, nor the acknowledgements of what its side wrote: that
// is no sign of a dead transport, which only a read waiting for bytes can show (#10829's review). This holds the busy
// side's own check only. The node answers once the hub's read loop is free again: an answer written meanwhile would
// wait for an acknowledgement the busy loop does not send, and the node would take the transport for dead and close
// it, as it should (TestResumeHeldReaderLearnsAtOnce). allAckDelay is shortened with resumeStale, as production has
// it well under: the hub's acknowledgement of the event it holds reaches the node before the node's check (#11031).
func TestResumeBusyReaderIsNotSilence(t *testing.T) {
	oldStale, oldCheck, oldAck := resumeStale, resumeCheck, allAckDelay
	resumeStale, resumeCheck, allAckDelay = 200*time.Millisecond, 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { resumeStale, resumeCheck, allAckDelay = oldStale, oldCheck, oldAck })
	hold, busy, asked := make(chan struct{}), make(chan struct{}), make(chan struct{})
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, func(h, n *Peer) {
		h.OnEvent = func(string, json.RawMessage) {
			close(busy)
			<-hold
		}
		n.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			close(asked)
			<-hold
			return map[string]any{}, nil
		}
	})
	if err := l.n.Notify("ev", map[string]int{"i": 1}); err != nil {
		t.Fatal(err)
	}
	<-busy // the hub's read loop is held from here on
	call := make(chan error, 1)
	go func() { call <- l.h.Call(context.Background(), "echo", nil, nil) }() // kept until an acknowledgement is read
	<-asked
	<-time.After(5 * resumeStale)
	if l.h.lw.oldestKept().IsZero() {
		t.Fatal("the call was acknowledged: the hub's read loop was not busy")
	}
	if l.h.Lost() || l.n.Lost() {
		t.Fatalf("a transport lost while its read loop was busy with a line: hub %t, node %t", l.h.Lost(), l.n.Lost())
	}
	close(hold)
	if err := <-call; err != nil {
		t.Fatal(err)
	}
}

// A read loop held up past the silence check (a slow handler on the manager's side, #10829's review): the other side
// takes the transport for dead and closes it, so the held side learns as soon as it reads again, and resumes at once,
// well within the window, rather than after its pings went unanswered. Nothing is lost or doubled.
func TestResumeHeldReaderLearnsAtOnce(t *testing.T) {
	oldStale, oldCheck := resumeStale, resumeCheck
	resumeStale, resumeCheck = 200*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { resumeStale, resumeCheck = oldStale, oldCheck })
	hold := make(chan struct{})
	var mu sync.Mutex
	var got []int
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, func(h, n *Peer) {
		h.OnEvent = func(_ string, params json.RawMessage) {
			var e struct{ I int }
			_ = json.Unmarshal(params, &e)
			if e.I == 0 {
				<-hold
			}
			mu.Lock()
			got = append(got, e.I)
			mu.Unlock()
		}
	})
	const events = 200
	go func() {
		for i := range events {
			_ = l.n.Notify("ev", map[string]int{"i": i})
		}
	}()
	select {
	case <-l.nLost: // its frames unacknowledged, nothing read
	case <-time.After(5 * time.Second):
		t.Fatal("the node's side never took the transport for dead")
	}
	close(hold)
	select {
	case <-l.hLost:
	case <-time.After(2 * time.Second):
		t.Fatal("the held side was not told: it would have waited for its pings, past the other side's window")
	}
	if err := l.resume(); err != nil {
		t.Fatalf("resume: %v", err)
	}
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == events
	})
	mu.Lock()
	defer mu.Unlock()
	for i, v := range got {
		if v != i {
			t.Fatalf("event %d came as %d: %v", i, v, got)
		}
	}
}

// A read loop slow over its lines (a handler blocking it for a while on each, the manager's state.put) is no silent
// transport to the other side, however long the lines take in all: each frame is acknowledged as it is taken, not only
// once the reader wants more bytes (#10829's review).
func TestResumeSlowReadLoopIsNotSilence(t *testing.T) {
	oldStale, oldCheck := resumeStale, resumeCheck
	resumeStale, resumeCheck = 1500*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { resumeStale, resumeCheck = oldStale, oldCheck })
	const events = 10
	var handled atomic.Int32
	l := newResumeLink(t, netshape.Config{}, 5*time.Second, true, true, func(h, n *Peer) {
		h.OnEvent = func(string, json.RawMessage) {
			time.Sleep(400 * time.Millisecond) // sleep-ok: a handler holding the read loop, well under resumeStale
			handled.Add(1)
		}
	})
	for i := range events {
		if err := l.n.Notify("ev", map[string]int{"i": i}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for handled.Load() < events && time.Now().Before(deadline) {
		select {
		case err := <-l.nLost:
			t.Fatalf("lost after %d of %d slow lines (%v): %v", handled.Load(), events, events*400*time.Millisecond, err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	if handled.Load() < events {
		t.Fatal("the lines were not all handled")
	}
	if l.n.Lost() || l.h.Lost() {
		t.Fatal("a side took the transport for silent")
	}
}

type failAfterWriteConn struct {
	net.Conn
	failNext atomic.Bool
}

func (c *failAfterWriteConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && c.failNext.CompareAndSwap(true, false) {
		return n, errors.New("write error after peer read")
	}
	return n, err
}

// A line whose final write failed on a resumable transport enters lw.held until the resumed transport
// finishes resending it. If the peer already read that frame before the cut (peerRead >= seq) and this
// side has nothing else to send, Resume must still release lw.held rather than deadlocking the caller.
func TestResumeReleasesHeldLinesWhenPeerAlreadyReadThem(t *testing.T) {
	aRaw, b := net.Pipe()
	a := &failAfterWriteConn{Conn: aRaw}
	h, n := New("h", a), New("n", b)
	h.EnableLanes([]string{"ping"}, []string{"bulk"})
	n.EnableLanes([]string{"ping"}, []string{"bulk"})
	h.EnableResume(5 * time.Second)
	n.EnableResume(5 * time.Second)

	hLost := make(chan error, 4)
	h.OnLost = func(err error) { hLost <- err }
	gotEvent := make(chan struct{})
	var eventOnce sync.Once
	n.OnEvent = func(event string, _ json.RawMessage) {
		if event == "ev" {
			eventOnce.Do(func() { close(gotEvent) })
		}
	}

	go func() { _ = h.Serve(context.Background(), a) }()
	go func() { _ = n.Serve(context.Background(), b) }()
	t.Cleanup(func() {
		h.Close(ErrClosed)
		n.Close(ErrClosed)
		_ = a.Close()
		_ = b.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.UpgradeLanes(ctx); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return n.Framed() && h.readsResumable() && n.readsResumable() })

	// Arm a write failure after the peer has received the frame bytes.
	a.failNext.Store(true)
	notifyDone := make(chan error, 1)
	go func() {
		notifyDone <- h.Notify("ev", map[string]int{"i": 1})
	}()

	select {
	case <-gotEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("peer never received the event frame")
	}
	select {
	case <-hLost:
	case <-time.After(5 * time.Second):
		t.Fatal("sender never marked the transport lost")
	}

	// Resume over a fresh transport; n already read frame 1 so h has zero frames to resend and no acks due.
	a2, b2 := net.Pipe()
	t.Cleanup(func() { _ = a2.Close(); _ = b2.Close() })
	acceptErr := make(chan error, 1)
	go func() {
		line, rest, err := ReadFirstLine(b2, resumeLineMax)
		if err != nil {
			acceptErr <- err
			return
		}
		req, ok := ParseResume(line)
		if !ok {
			acceptErr <- fmt.Errorf("not a resume line: %q", line)
			return
		}
		acceptErr <- n.AcceptResume(req, rest, b2)
	}()
	if err := h.Resume(ctx, a2, a2); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := <-acceptErr; err != nil {
		t.Fatalf("AcceptResume: %v", err)
	}

	select {
	case err := <-notifyDone:
		if err != nil {
			t.Fatalf("Notify after resume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Notify deadlocked in lw.held after resume when peer had already read its frame")
	}
}

type scriptWriter struct {
	fn func([]byte) (int, error)
}

func (w scriptWriter) Write(p []byte) (int, error) { return w.fn(p) }

// If an incoming acknowledgement empties lw.kept while lw.resend is still advancing through a multi-frame
// resend, resendLocked finishes with an empty batch and lw.resend == -1 and must release lw.held.
func TestLaneWriterReleasesHeldLinesWhenAckClearsResend(t *testing.T) {
	closed := make(chan struct{})
	defer close(closed)

	var writes atomic.Int32
	lostCh := make(chan struct{}, 1)
	w1 := scriptWriter{fn: func(p []byte) (int, error) {
		if writes.Add(1) == 2 {
			return 0, errors.New("second frame failed")
		}
		return len(p), nil
	}}

	var lw *laneWriter
	lw = newLaneWriter(w1, closed, func(err error) { t.Errorf("unexpected fail: %v", err) }, nil)
	lw.keep = true
	lw.lose = func(_ error, gen uint64) bool {
		lw.loseGen(gen)
		select {
		case lostCh <- struct{}{}:
		default:
		}
		return true
	}
	lw.start()

	// Two-frame line: frame 1 succeeds, frame 2 fails and moves the line into lw.held.
	payload := append(bytes.Repeat([]byte("x"), framePayloadMax+100), '\n')
	line := &outLine{b: payload, lane: LaneEvents, done: make(chan error, 1)}
	if _, err := lw.enqueue(line); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lostCh:
	case <-time.After(5 * time.Second):
		t.Fatal("writer never lost transport on second frame")
	}

	// Resume from peerRead=0 so resend starts at frame 1; while frame 1 is writing, ack both frames.
	wroteFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	unblockFirst := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	t.Cleanup(unblockFirst)
	var firstOnce sync.Once
	w2 := scriptWriter{fn: func(p []byte) (int, error) {
		firstOnce.Do(func() {
			close(wroteFirst)
			<-releaseFirst
		})
		return len(p), nil
	}}
	lw.resume(w2, 1, 0)

	select {
	case <-wroteFirst:
	case <-time.After(5 * time.Second):
		t.Fatal("resend never started writing frame 1")
	}
	if err := lw.acked(2, 0); err != nil {
		t.Fatalf("acked: %v", err)
	}
	unblockFirst()

	select {
	case err := <-line.done:
		if err != nil {
			t.Fatalf("line done: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("line deadlocked in lw.held after ack cleared remaining resend frames")
	}
}
