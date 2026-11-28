//go:build unix

package hubedge

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type writeErrConn struct {
	net.Conn
	writeErr error
	onWrite  func([]byte) error
}

func (c *writeErrConn) Write(p []byte) (int, error) {
	if c.onWrite != nil {
		if err := c.onWrite(p); err != nil {
			return 0, err
		}
	}
	if c.writeErr != nil {
		return 0, c.writeErr
	}
	return c.Conn.Write(p)
}

type closeNotifyConn struct {
	net.Conn
	once   sync.Once
	closed chan struct{}
}

func (c *closeNotifyConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// If the upstream answers before fromClient starts, fromClient must not flip keeping back on,
// retain post-response bytes, or swallow subsequent upstream write errors.
func TestRelayFastUpstreamResponseStopsKeepingAndPropagatesWriteErrors(t *testing.T) {
	client, edgeClient := tcpPair(t)
	edgeUp, slot := tcpPair(t)
	writeFailed := make(chan struct{})
	var failOnce sync.Once
	up := &writeErrConn{
		Conn:     edgeUp,
		writeErr: errors.New("upstream write failed"),
		onWrite: func([]byte) error {
			failOnce.Do(func() { close(writeFailed) })
			return nil
		},
	}

	r := &relay{client: edgeClient, up: up}
	toDone := make(chan struct{})
	go func() {
		defer close(toDone)
		r.toClient(func() net.Conn { return nil })
	}()

	// Deliver the upstream's first bytes and confirm the client receives them before fromClient starts.
	if _, err := slot.Write([]byte("ready")); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len("ready"))
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatalf("client read first bytes: %v", err)
	}

	fromDone := make(chan struct{})
	go func() {
		defer close(fromDone)
		r.fromClient()
	}()

	if _, err := client.Write([]byte("after-first-byte")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-writeFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream Write was never called")
	}
	select {
	case <-fromDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fromClient ignored upstream write error after first upstream byte")
	}
	select {
	case <-toDone:
	case <-time.After(2 * time.Second):
		t.Fatal("toClient did not exit after fromClient closed the relay")
	}
	r.mu.Lock()
	keptLen := len(r.kept)
	r.mu.Unlock()
	if keptLen != 0 {
		t.Fatalf("retained %d post-response bytes in replay buffer", keptLen)
	}
}

// Once client bytes exceed replayLimit before the upstream has replied, keeping must stop so
// upstream write failures close the relay promptly instead of being swallowed while toClient
// remains blocked reading from the silent upstream.
func TestRelayOverflowStopsKeepingAndPropagatesWriteErrors(t *testing.T) {
	client, edgeClient := tcpPair(t)
	edgeUp, slot := tcpPair(t)
	go func() { _, _ = io.Copy(io.Discard, slot) }()

	var seenBytes atomic.Int64
	writeFailed := make(chan struct{})
	var failOnce sync.Once
	up := &writeErrConn{
		Conn: edgeUp,
		onWrite: func(p []byte) error {
			if seenBytes.Add(int64(len(p))) > replayLimit {
				failOnce.Do(func() { close(writeFailed) })
				return errors.New("upstream write failed after overflow")
			}
			return nil
		},
	}

	r := &relay{client: edgeClient, up: up}
	fromDone := make(chan struct{})
	toDone := make(chan struct{})
	go func() {
		defer close(fromDone)
		r.fromClient()
	}()
	go func() {
		defer close(toDone)
		r.toClient(func() net.Conn { return nil })
	}()

	// Write past replayLimit asynchronously so a prompt relay close never flakes on EPIPE.
	big := bytes.Repeat([]byte("B"), replayLimit+4096)
	go func() { _, _ = client.Write(big) }()

	select {
	case <-writeFailed:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream Write never crossed replayLimit")
	}
	select {
	case <-fromDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fromClient swallowed upstream write error after replayLimit overflow")
	}
	select {
	case <-toDone:
	case <-time.After(2 * time.Second):
		t.Fatal("toClient did not exit after overflow write error closed the relay")
	}
}

// If client traffic overflows replayLimit while toClient is waiting for the replacement slot in next(),
// toClient must not switch onto the new slot with dropped/truncated prefix bytes.
func TestRelayDoesNotReplayAfterOverflowDuringSlotSwitch(t *testing.T) {
	client, edgeClient := tcpPair(t)
	edgeUp1, slot1 := tcpPair(t)
	edgeUp2, slot2 := tcpPair(t)

	var seenBytes atomic.Int64
	overflowed := make(chan struct{})
	var overOnce sync.Once
	up1 := &writeErrConn{
		Conn: edgeUp1,
		onWrite: func(p []byte) error {
			if seenBytes.Add(int64(len(p))) > replayLimit {
				overOnce.Do(func() { close(overflowed) })
			}
			return nil
		},
	}
	up2Closed := make(chan struct{})
	up2 := &closeNotifyConn{Conn: edgeUp2, closed: up2Closed}

	nextEntered := make(chan struct{})
	releaseNext := make(chan struct{})
	var releaseOnce sync.Once
	unblockNext := func() { releaseOnce.Do(func() { close(releaseNext) }) }
	t.Cleanup(unblockNext)

	r := &relay{client: edgeClient, up: up1}
	fromDone := make(chan struct{})
	toDone := make(chan struct{})
	go func() {
		defer close(fromDone)
		r.fromClient()
	}()
	go func() {
		defer close(toDone)
		r.toClient(func() net.Conn {
			close(nextEntered)
			<-releaseNext
			return up2
		})
	}()

	// Send a small replayable prefix, then half-close slot1's write side while draining its reads so
	// toClient enters next() without triggering TCP RST or setting r.closed during the client's overflow write.
	prefix := []byte("GET / HTTP/1.1\r\nHost: example\r\n")
	if _, err := client.Write(prefix); err != nil {
		t.Fatal(err)
	}
	gotPrefix := make([]byte, len(prefix))
	_ = slot1.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(slot1, gotPrefix); err != nil {
		t.Fatalf("slot1 read prefix: %v", err)
	}
	_ = slot1.SetReadDeadline(time.Time{})
	go func() { _, _ = io.Copy(io.Discard, slot1) }()
	if err := slot1.CloseWrite(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-nextEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("toClient did not ask for a replacement slot")
	}

	// Overflow replayLimit while next() is still waiting for the replacement slot.
	big := bytes.Repeat([]byte("A"), replayLimit+1024)
	if _, err := client.Write(big); err != nil {
		t.Fatal(err)
	}
	select {
	case <-overflowed:
	case <-time.After(5 * time.Second):
		t.Fatal("fromClient did not process overflowing bytes")
	}

	r.mu.Lock()
	overflow, closed := r.overflow, r.closed
	r.mu.Unlock()
	if !overflow || closed {
		t.Fatalf("expected overflow=true and closed=false before next() returns; got overflow=%v closed=%v", overflow, closed)
	}

	unblockNext()

	select {
	case <-up2Closed:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement upstream connection was not closed after replay overflow")
	}
	_ = slot2.SetReadDeadline(time.Now().Add(2 * time.Second))
	leaked, err := io.ReadAll(slot2)
	if err != nil {
		t.Fatalf("slot2 read: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("slot2 received %d truncated bytes after overflow", len(leaked))
	}
	select {
	case <-toDone:
	case <-time.After(2 * time.Second):
		t.Fatal("toClient did not exit")
	}
	select {
	case <-fromDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fromClient did not exit")
	}
}

// If a non-replayable request (POST) arrives after an idle slot closed and while next() waits,
// toClient must re-check replayability before writing to the replacement slot.
func TestRelayDoesNotReplayNonReplayableRequestArrivingDuringSwitch(t *testing.T) {
	client, edgeClient := tcpPair(t)
	edgeUp1, slot1 := tcpPair(t)
	edgeUp2, slot2 := tcpPair(t)

	postSeen := make(chan struct{})
	var postOnce sync.Once
	up1 := &writeErrConn{
		Conn: edgeUp1,
		onWrite: func([]byte) error {
			postOnce.Do(func() { close(postSeen) })
			return nil
		},
	}
	up2Closed := make(chan struct{})
	up2 := &closeNotifyConn{Conn: edgeUp2, closed: up2Closed}

	nextEntered := make(chan struct{})
	releaseNext := make(chan struct{})
	var releaseOnce sync.Once
	unblockNext := func() { releaseOnce.Do(func() { close(releaseNext) }) }
	t.Cleanup(unblockNext)

	r := &relay{client: edgeClient, up: up1}
	fromDone := make(chan struct{})
	toDone := make(chan struct{})
	go func() {
		defer close(fromDone)
		r.fromClient()
	}()
	go func() {
		defer close(toDone)
		r.toClient(func() net.Conn {
			close(nextEntered)
			<-releaseNext
			return up2
		})
	}()

	// Half-close the idle slot's write side while draining reads; replayable(nil) is true so toClient enters next().
	go func() { _, _ = io.Copy(io.Discard, slot1) }()
	if err := slot1.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-nextEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("toClient did not enter next() on idle slot EOF")
	}

	if _, err := client.Write([]byte("POST /api/v1/send HTTP/1.1\r\nHost: example\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-postSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("fromClient did not read the POST request")
	}

	unblockNext()

	select {
	case <-up2Closed:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement upstream connection was not closed for non-replayable POST")
	}
	_ = slot2.SetReadDeadline(time.Now().Add(2 * time.Second))
	leaked, err := io.ReadAll(slot2)
	if err != nil {
		t.Fatalf("slot2 read: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("slot2 received non-replayable bytes: %q", string(leaked))
	}
	select {
	case <-toDone:
	case <-time.After(2 * time.Second):
		t.Fatal("toClient did not exit")
	}
	select {
	case <-fromDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fromClient did not exit")
	}
}
