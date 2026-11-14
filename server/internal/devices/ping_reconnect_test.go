package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A connection whose node stops answering (a half-open link) is closed after pingMissLimit unanswered pings in a
// row, so the machine reconnects instead of holding every call until its own wait runs out (#7510).
func TestMeasureNodesClosesAConnectionThatStopsAnswering(t *testing.T) {
	oldWait := pingWait
	pingWait = 50 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	d.addEntry(m)
	peer := rpc.New("h", io.Discard) // writes go nowhere: no answer ever comes
	m.mu.Lock()
	m.peer, m.online, m.active, m.transport = peer, true, &nodePath{key: "m1"}, "ssh"
	m.mu.Unlock()
	for i := 1; i < pingMissLimit; i++ {
		d.measureNodes(context.Background())
		select {
		case <-peer.Done():
			t.Fatalf("closed after %d unanswered ping(s), want %d", i, pingMissLimit)
		default:
		}
	}
	d.measureNodes(context.Background())
	select {
	case <-peer.Done():
	default:
		t.Fatalf("still open after %d unanswered pings", pingMissLimit)
	}
	if err := peer.Err(); err == nil || err.Error() != "no answer to 2 pings in a row (a half-open link?)" {
		t.Fatalf("closed with %v", err)
	}
}

// A large inbound frame can hold the node's pong behind it. Recent chunks count even when none arrive during the
// ping itself; once the traffic stops, two silent written pings still close the connection.
func TestPingTimeoutsDoNotCloseDuringLargeTransfer(t *testing.T) {
	oldWait, oldWindow := pingWait, pingActivityWindow
	pingWait, pingActivityWindow = 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { pingWait, pingActivityWindow = oldWait, oldWindow })
	r, w := io.Pipe()
	writes := &signalWriter{ch: make(chan string, 16)}
	peer := rpc.New("h", writes)
	serveDone := make(chan error, 1)
	go func() { serveDone <- peer.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		peer.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	d, _ := pingMachine(t, peer)
	chunks := []string{
		`{"event":"file.chunk","params":"`,
		`first part of a large file; `,
		`the pong is still queued behind this frame; `,
		`more bytes before another ping`,
	}
	for i := 0; i < pingMissLimit+1; i++ {
		writePingActivity(t, w, peer, chunks[i])
		d.measureNodes(context.Background())
		writes.wantPing(t)
		assertPeerOpen(t, peer)
	}

	// No more bytes arrive. Once the recent-activity window expires, this same peer is indistinguishable from a
	// half-open link and must retain the original two-miss behavior.
	<-time.After(pingActivityWindow + pingWait)
	for i := 1; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		writes.wantPing(t)
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("silent connection stayed open after two more written ping timeouts")
	}
}

// A relayed deadline is itself an inbound response. It must not refresh activity and prevent every later relayed
// deadline from closing the peer.
func TestMeasureNodesClosesAfterRelayedTimeoutsWithoutOtherTraffic(t *testing.T) {
	hubConn, nodeConn := net.Pipe()
	hub, node := rpc.New("h", hubConn), rpc.New("n", nodeConn)
	node.OnRequest = func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, &rpc.Error{Message: context.DeadlineExceeded.Error()}
	}
	hubDone, nodeDone := make(chan error, 1), make(chan error, 1)
	go func() { hubDone <- hub.Serve(context.Background(), hubConn) }()
	go func() { nodeDone <- node.Serve(context.Background(), nodeConn) }()
	t.Cleanup(func() {
		_ = hubConn.Close()
		_ = nodeConn.Close()
		hub.Close(io.EOF)
		node.Close(io.EOF)
		for name, done := range map[string]<-chan error{"hub": hubDone, "node": nodeDone} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Errorf("%s Serve did not stop", name)
			}
		}
	})
	d, _ := pingMachine(t, hub)
	for i := 1; i <= pingMissLimit; i++ {
		d.measureNodes(context.Background())
		if i < pingMissLimit {
			assertPeerOpen(t, hub)
		}
	}
	select {
	case <-hub.Done():
	default:
		t.Fatal("relayed timeout responses kept the connection alive without other traffic")
	}
}

// A writer that consumed most of the probe left too little time for a pong, so those rounds do not count as misses.
func TestPingMostlyDelayedByWriterDoesNotCountAsMiss(t *testing.T) {
	oldWait := pingWait
	pingWait = 100 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &delayWriter{uploads: make(chan chan struct{}, 16), pings: make(chan string, 16)}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	for i := 0; i < pingMissLimit+1; i++ {
		runDelayedPing(t, d, peer, w, 90*time.Millisecond)
		assertPeerOpen(t, peer)
	}
}

// A brief queue is different: the ping was written with almost its whole response budget left, and a silent peer
// must still close after two misses. An unbounded ErrWriteDelayed exemption would keep it open forever.
func TestBrieflyDelayedPingStillClosesSilentPeer(t *testing.T) {
	oldWait := pingWait
	pingWait = 100 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &delayWriter{uploads: make(chan chan struct{}, 16), pings: make(chan string, 16)}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	for i := 1; i <= pingMissLimit; i++ {
		runDelayedPing(t, d, peer, w, 0)
		if i < pingMissLimit {
			assertPeerOpen(t, peer)
		}
	}
	select {
	case <-peer.Done():
	default:
		t.Fatal("silent peer stayed open after two briefly delayed but written pings")
	}
}

// The pre-existing not-written path is exercised through measureNodes too: only the upload reaches the writer, and
// repeated pings expiring behind it never close the connection.
func TestMeasureNodesDoesNotCloseWhenPingWaitsForLargeWrite(t *testing.T) {
	oldWait := pingWait
	pingWait = 50 * time.Millisecond
	t.Cleanup(func() { pingWait = oldWait })
	w := &stuckWriter{in: make(chan struct{}), release: make(chan struct{})}
	peer := rpc.New("h", w)
	d, _ := pingMachine(t, peer)
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- peer.Call(context.Background(), "file.upload.chunk", nil, nil) }()
	t.Cleanup(func() {
		close(w.release)
		peer.Close(io.EOF)
		select {
		case <-uploadDone:
		case <-time.After(time.Second):
			t.Error("upload did not stop")
		}
	})
	select {
	case <-w.in:
	case <-time.After(time.Second):
		t.Fatal("upload did not take the writer")
	}
	for i := 0; i < pingMissLimit+1; i++ {
		d.measureNodes(context.Background())
		assertPeerOpen(t, peer)
	}
	if got := w.writeCount(); got != 1 {
		t.Fatalf("writer calls = %d, want only the upload (no ping reached the wire)", got)
	}
}

func TestMeasureNodesSkipsAnOnlineMachineWithoutPeer(t *testing.T) {
	peer := rpc.New("h", io.Discard)
	d, m := pingMachine(t, peer)
	m.mu.Lock()
	m.peer = nil // a path switch can detach the peer while leaving the machine warm/online
	m.mu.Unlock()
	d.measureNodes(context.Background()) // must not panic or manufacture a miss on a replacement connection
}

func pingMachine(t *testing.T, peer *rpc.Peer) (*Devices, *Machine) {
	t.Helper()
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	d.addEntry(m)
	m.mu.Lock()
	m.peer, m.online, m.active, m.transport = peer, true, &nodePath{key: "m1"}, "ssh"
	m.mu.Unlock()
	return d, m
}

func assertPeerOpen(t *testing.T, peer *rpc.Peer) {
	t.Helper()
	select {
	case <-peer.Done():
		t.Fatalf("connection closed: %v", peer.Err())
	default:
	}
}

// delayWriter holds each upload until its own release channel closes; pings then complete immediately.
type delayWriter struct {
	uploads chan chan struct{}
	pings   chan string
}

func (w *delayWriter) Write(b []byte) (int, error) {
	line := string(b)
	if strings.Contains(line, `"method":"file.upload.chunk"`) {
		release := make(chan struct{})
		w.uploads <- release
		<-release
	} else if strings.Contains(line, `"method":"ping"`) {
		w.pings <- line
	}
	return len(b), nil
}

func runDelayedPing(t *testing.T, d *Devices, peer *rpc.Peer, w *delayWriter, delay time.Duration) {
	t.Helper()
	uploadDone := make(chan error, 1)
	go func() { uploadDone <- peer.Call(context.Background(), "file.upload.chunk", nil, nil) }()
	select {
	case release := <-w.uploads:
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		t.Cleanup(func() {
			unblock()
			peer.Close(io.EOF)
			select {
			case <-uploadDone:
			case <-time.After(time.Second):
				t.Error("upload did not stop")
			}
		})
		measureDone := make(chan struct{})
		go func() {
			d.measureNodes(context.Background())
			close(measureDone)
		}()
		deadline := time.Now().Add(time.Second)
		for peer.WriteWaiters() == 0 {
			if time.Now().After(deadline) {
				unblock()
				t.Fatal("ping never queued behind the upload")
			}
			<-time.After(time.Millisecond)
		}
		if delay > 0 {
			<-time.After(delay)
		}
		unblock()
		select {
		case <-measureDone:
		case <-time.After(2 * time.Second):
			t.Fatal("measureNodes did not return")
		}
	case <-time.After(time.Second):
		t.Fatal("upload did not take the writer")
	}
	select {
	case <-w.pings:
	case <-time.After(time.Second):
		t.Fatal("delayed ping was not written")
	}
}

// signalWriter completes every write immediately and exposes each complete RPC line to the test.
type signalWriter struct{ ch chan string }

func (w *signalWriter) Write(b []byte) (int, error) {
	w.ch <- string(b)
	return len(b), nil
}

func (w *signalWriter) wantPing(t *testing.T) {
	t.Helper()
	select {
	case got := <-w.ch:
		if !strings.Contains(got, `"method":"ping"`) {
			t.Fatalf("write = %q, want a ping", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ping was not written")
	}
}

// writePingActivity puts another partial-frame chunk on the wire and waits until the raw reader observed it.
func writePingActivity(t *testing.T, w io.Writer, peer *rpc.Peer, chunk string) {
	t.Helper()
	before := peer.PingActivity()
	writeDone := make(chan error, 1)
	go func() {
		_, err := io.WriteString(w, chunk)
		writeDone <- err
	}()
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write activity: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("activity write did not reach the reader")
	}
	deadline := time.Now().Add(time.Second)
	for !peer.PingActivity().After(before) {
		if time.Now().After(deadline) {
			t.Fatal("reader did not record the activity")
		}
		<-time.After(time.Millisecond)
	}
}

// Only pings in a row on the same connection count: an answer (an error the node answered with too) clears the
// count, a connection already gone counts neither way, and a new connection starts over.
func TestPingMissesCountInARowOnOneConnection(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "m1", Kind: "ssh", SSHHost: "m1"})
	peer := rpc.New("h", io.Discard)
	m.mu.Lock()
	m.peer = peer
	m.mu.Unlock()
	open := func() bool {
		select {
		case <-peer.Done():
			return false
		default:
			return true
		}
	}
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, nil)
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, &rpc.Error{Code: "busy", Message: "busy"})
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.pingAnswered(peer, rpc.ErrDropped) // gone: neither a miss nor an answer
	m.pingAnswered(peer, notWritten(t))  // never on the wire (a long write ahead): neither
	m.mu.Lock()
	m.reloadUntil = time.Now().Add(time.Minute) // a resident node handing over: calls wait for the new connection
	m.mu.Unlock()
	m.pingAnswered(peer, context.DeadlineExceeded)
	m.mu.Lock()
	m.reloadUntil = time.Time{}
	m.mu.Unlock()
	if !open() {
		t.Fatal("closed although no two misses were in a row")
	}
	other := rpc.New("h", io.Discard)
	m.pingAnswered(other, context.DeadlineExceeded) // another connection's miss starts its own count
	m.pingAnswered(peer, context.DeadlineExceeded)
	if !open() {
		t.Fatal("misses on two connections were added up")
	}
	m.pingAnswered(peer, &rpc.Error{Message: "context deadline exceeded"}) // a relay's words for its wait running out
	if open() {
		t.Fatal("two misses in a row did not close the connection")
	}

	// A connection the machine no longer uses is not closed (it is the run loop's to end).
	m.mu.Lock()
	m.peer = rpc.New("h", io.Discard)
	m.mu.Unlock()
	m.pingAnswered(other, context.DeadlineExceeded)
	select {
	case <-other.Done():
		t.Fatal("closed a connection the machine no longer uses")
	default:
	}
}

// notWritten is what a ping that waited for the writer and gave up answers.
func notWritten(t *testing.T) error {
	t.Helper()
	w := &stuckWriter{in: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(w.release) })
	p := rpc.New("h", w)
	go func() { _ = p.Call(context.Background(), "file.upload", nil, nil) }()
	<-w.in // the upload holds the writer, its write stuck
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := p.Call(ctx, "ping", nil, nil)
	if !errors.Is(err, rpc.ErrNotWritten) {
		t.Fatalf("ping behind a stuck write: %v", err)
	}
	return err
}

// stuckWriter's first write says so on in and blocks until release.
type stuckWriter struct {
	in, release chan struct{}
	once        sync.Once
	mu          sync.Mutex
	calls       int
}

func (w *stuckWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	w.once.Do(func() { close(w.in) })
	<-w.release
	return len(b), nil
}

func (w *stuckWriter) writeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}
