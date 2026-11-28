package tunnel

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tailscale/tailcat"
)

type fakeTunnelClient struct {
	mu            sync.Mutex
	started       bool
	backendClosed bool
	closeCalls    int
	inFlight      int
	closeWhileAct bool
	onDial        func(ctx context.Context, port uint16) (net.Conn, error)
	onPath        func(ctx context.Context) (string, error)
	onClose       func()
}

func (f *fakeTunnelClient) DialTCPPort(ctx context.Context, port uint16) (net.Conn, error) {
	f.mu.Lock()
	f.inFlight++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.onDial != nil {
		return f.onDial(ctx, port)
	}
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	a, b := net.Pipe()
	_ = b.Close()
	return a, nil
}

func (f *fakeTunnelClient) path(ctx context.Context) (string, error) {
	f.mu.Lock()
	f.inFlight++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()
	if f.onPath != nil {
		return f.onPath(ctx)
	}
	f.mu.Lock()
	f.started = true
	f.mu.Unlock()
	return "direct", nil
}

// Close mirrors tailcat.Client.Close: if the lazy backend has not started yet, Close is a no-op;
// once started, Close closes the backend. Also records if Close was ever invoked concurrently with
// an in-flight DialTCPPort/path call.
func (f *fakeTunnelClient) Close() error {
	f.mu.Lock()
	f.closeCalls++
	if f.inFlight > 0 {
		f.closeWhileAct = true
	}
	if f.started {
		f.backendClosed = true
	}
	onClose := f.onClose
	f.mu.Unlock()
	if onClose != nil {
		onClose()
	}
	return nil
}

type closeTrackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeTrackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

func TestDialerClosePreventsNewClients(t *testing.T) {
	var created atomic.Int32
	d := &Dialer{
		newClient: func(tailcat.Addr) tunnelClient {
			created.Add(1)
			return &fakeTunnelClient{}
		},
	}
	d.Close()
	d.Close() // idempotent
	d.Reset("node-a")

	if _, err := d.Dial(context.Background(), "node-a", NodePort); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial after Close = %v, want net.ErrClosed", err)
	}
	if _, err := d.Path(context.Background(), "node-a"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Path after Close = %v, want net.ErrClosed", err)
	}
	if got := created.Load(); got != 0 {
		t.Fatalf("Dial/Path after Close created %d client(s), want 0", got)
	}
}

// If Dialer.Close runs after Dial acquired a client entry but before tailcat.Client's lazy backend
// started (making the initial Close() a no-op), Dial must close any returned connection, return
// net.ErrClosed, and close the late-started backend on release so nothing leaks.
func TestDialerCloseWhileDialStartingClosesLateStartedBackend(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var retConn *closeTrackingConn
	var fake *fakeTunnelClient

	d := &Dialer{
		newClient: func(tailcat.Addr) tunnelClient {
			fake = &fakeTunnelClient{
				onDial: func(ctx context.Context, _ uint16) (net.Conn, error) {
					close(entered)
					<-release
					// Simulate lazy startup completing right after Close()'s initial no-op Close().
					fake.mu.Lock()
					fake.started = true
					fake.mu.Unlock()
					a, b := net.Pipe()
					_ = b.Close()
					retConn = &closeTrackingConn{Conn: a}
					return retConn, nil
				},
			}
			return fake
		},
	}

	dialErr := make(chan error, 1)
	go func() {
		_, err := d.Dial(context.Background(), "node-a", NodePort)
		dialErr <- err
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Dial did not reach client")
	}

	// Close runs while Dial is active and fake.started == false: Close must defer e.c.Close()
	// until the last active caller exits (closeCalls == 0 while active).
	d.Close()
	fake.mu.Lock()
	if fake.backendClosed || fake.closeCalls != 0 {
		fake.mu.Unlock()
		t.Fatalf("unexpected active-close state: backendClosed=%v closeCalls=%d, want false/0", fake.backendClosed, fake.closeCalls)
	}
	fake.mu.Unlock()

	close(release)
	select {
	case err := <-dialErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Dial err = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Dial did not return")
	}

	if retConn == nil || !retConn.closed.Load() {
		t.Fatal("returned connection from closed Dialer was not closed")
	}
	fake.mu.Lock()
	closed, calls, whileAct := fake.backendClosed, fake.closeCalls, fake.closeWhileAct
	fake.mu.Unlock()
	if !closed || calls != 1 || whileAct {
		t.Fatalf("backend close mismatch: backendClosed=%v closeCalls=%d closeWhileAct=%v, want true/1/false", closed, calls, whileAct)
	}
}

// Multiple concurrent in-flight callers on the same entry: Close must call e.c.Close()
// exactly once, and only after the final caller returns (never concurrently with any caller).
func TestDialerCloseCallsClientCloseOnceAfterAllConcurrentCallersExit(t *testing.T) {
	entered := make(chan struct{}, 2)
	rel1 := make(chan struct{})
	rel2 := make(chan struct{})
	var fake *fakeTunnelClient

	d := &Dialer{
		newClient: func(tailcat.Addr) tunnelClient {
			fake = &fakeTunnelClient{
				onDial: func(ctx context.Context, port uint16) (net.Conn, error) {
					entered <- struct{}{}
					if port == NodePort {
						<-rel1
					} else {
						<-rel2
					}
					fake.mu.Lock()
					fake.started = true
					fake.mu.Unlock()
					return nil, ctx.Err()
				},
			}
			return fake
		},
	}

	done1 := make(chan error, 1)
	done2 := make(chan error, 1)
	go func() { _, err := d.Dial(context.Background(), "node-a", NodePort); done1 <- err }()
	go func() { _, err := d.Dial(context.Background(), "node-a", UploadPort); done2 <- err }()

	for range 2 {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			close(rel1)
			close(rel2)
			t.Fatal("concurrent dials did not enter")
		}
	}

	d.Close()

	// Release first caller: second caller is still active, so Close() must not have run yet.
	close(rel1)
	select {
	case err := <-done1:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("caller 1 err = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		close(rel2)
		t.Fatal("caller 1 did not return")
	}

	fake.mu.Lock()
	midCalls := fake.closeCalls
	fake.mu.Unlock()
	if midCalls != 0 {
		close(rel2)
		t.Fatalf("e.c.Close called %d time(s) while second caller still active, want 0", midCalls)
	}

	// Release second caller: now e.c.Close() runs exactly once.
	close(rel2)
	select {
	case err := <-done2:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("caller 2 err = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("caller 2 did not return")
	}

	fake.mu.Lock()
	closed, calls, whileAct := fake.backendClosed, fake.closeCalls, fake.closeWhileAct
	fake.mu.Unlock()
	if !closed || calls != 1 || whileAct {
		t.Fatalf("final close state: backendClosed=%v closeCalls=%d closeWhileAct=%v, want true/1/false", closed, calls, whileAct)
	}
}

// Dialer.Close must cancel every entry in a first pass before calling Close() on idle entries,
// so a slow Close() on one peer cannot delay cancellation of active calls on another peer.
func TestDialerCloseCancelsAllBeforeClosingIdle(t *testing.T) {
	idleCloseEntered := make(chan struct{})
	releaseIdleClose := make(chan struct{})
	activeEntered := make(chan struct{})
	var closeOnce sync.Once
	t.Cleanup(func() { closeOnce.Do(func() { close(releaseIdleClose) }) })

	d := &Dialer{
		newClient: func(addr tailcat.Addr) tunnelClient {
			if addr == "idle-node" {
				return &fakeTunnelClient{
					onClose: func() {
						close(idleCloseEntered)
						<-releaseIdleClose
					},
				}
			}
			return &fakeTunnelClient{
				onDial: func(ctx context.Context, _ uint16) (net.Conn, error) {
					close(activeEntered)
					<-ctx.Done()
					return nil, ctx.Err()
				},
			}
		},
	}

	// Establish idle-node (active returns to 0 after Dial completes).
	c, err := d.Dial(context.Background(), "idle-node", NodePort)
	if err != nil {
		t.Fatalf("seed idle-node: %v", err)
	}
	_ = c.Close()

	// Start an active call on active-node.
	activeDone := make(chan error, 1)
	go func() {
		_, err := d.Dial(context.Background(), "active-node", NodePort)
		activeDone <- err
	}()
	select {
	case <-activeEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("active-node Dial did not enter")
	}

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		d.Close()
	}()

	// Even while idle-node.Close() is blocked, active-node must already be canceled and return.
	select {
	case <-idleCloseEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("idle-node Close did not start")
	}
	select {
	case err := <-activeDone:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("active-node err = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("active-node was blocked behind idle-node.Close()")
	}

	closeOnce.Do(func() { close(releaseIdleClose) })
	select {
	case <-closeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Dialer.Close did not finish after releasing idle-node")
	}
}

// If an entry is already canceled before invoking e.c, Dial/Path must return net.ErrClosed
// without calling into e.c.
func TestDialerSkipsClientWhenEntryCanceledBeforeCall(t *testing.T) {
	var called atomic.Int32
	fake := &fakeTunnelClient{
		onDial: func(context.Context, uint16) (net.Conn, error) {
			called.Add(1)
			return nil, nil
		},
		onPath: func(context.Context) (string, error) {
			called.Add(1)
			return "direct", nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	d := &Dialer{
		clients: map[tailcat.Addr]*dialEntry{
			"node-a": {c: fake, ctx: ctx, cancel: cancel},
		},
	}
	if _, err := d.Dial(context.Background(), "node-a", NodePort); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Dial on pre-canceled entry = %v, want net.ErrClosed", err)
	}
	if _, err := d.Path(context.Background(), "node-a"); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Path on pre-canceled entry = %v, want net.ErrClosed", err)
	}
	if got := called.Load(); got != 0 {
		t.Fatalf("client called %d time(s) on pre-canceled entry, want 0", got)
	}
}

func TestDialerResetCancelsInFlightAndAllowsFreshClient(t *testing.T) {
	entered := make(chan struct{})
	var count atomic.Int32
	var first *fakeTunnelClient

	d := &Dialer{
		newClient: func(tailcat.Addr) tunnelClient {
			n := count.Add(1)
			fc := &fakeTunnelClient{}
			if n == 1 {
				first = fc
				fc.onDial = func(ctx context.Context, _ uint16) (net.Conn, error) {
					fc.mu.Lock()
					fc.started = true
					fc.mu.Unlock()
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
			}
			return fc
		},
	}
	defer d.Close()

	dialErr := make(chan error, 1)
	go func() {
		_, err := d.Dial(context.Background(), "node-a", NodePort)
		dialErr <- err
	}()

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first Dial did not enter")
	}

	d.Reset("node-a")
	select {
	case err := <-dialErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("in-flight Dial after Reset = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Reset did not cancel in-flight Dial")
	}

	first.mu.Lock()
	firstClosed := first.backendClosed
	first.mu.Unlock()
	if !firstClosed {
		t.Fatal("Reset did not close old client")
	}

	c, err := d.Dial(context.Background(), "node-a", NodePort)
	if err != nil {
		t.Fatalf("fresh Dial after Reset failed: %v", err)
	}
	_ = c.Close()
	if got := count.Load(); got != 2 {
		t.Fatalf("client count = %d, want 2", got)
	}
}

func TestDialerPathCancelsOnClose(t *testing.T) {
	entered := make(chan struct{})
	d := &Dialer{
		newClient: func(tailcat.Addr) tunnelClient {
			return &fakeTunnelClient{
				onPath: func(ctx context.Context) (string, error) {
					close(entered)
					<-ctx.Done()
					return "", ctx.Err()
				},
			}
		},
	}

	pathErr := make(chan error, 1)
	go func() {
		_, err := d.Path(context.Background(), "node-a")
		pathErr <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Path did not start")
	}
	d.Close()
	select {
	case err := <-pathErr:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Path after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Path did not return after Close")
	}
}

func TestInstallRejectsOversizedHeaderLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveUpload(ln, fakeBinary(t, t.TempDir(), "v1"), nil, 2*time.Second)

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))

	oversized := bytes.Repeat([]byte("a"), maxHeaderBytes+4096)
	_, _ = c.Write(oversized)
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read refusal: %v", err)
	}
	if !strings.Contains(line, errLineTooLong.Error()) {
		t.Fatalf("reply = %q, want %q", line, errLineTooLong.Error())
	}
}

func TestUploadRejectsOversizedReplyLine(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.Write(bytes.Repeat([]byte("x"), maxHeaderBytes+4096))
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = Upload(ctx, c, fakeBinary(t, t.TempDir(), "v2"))
	if err == nil || !strings.Contains(err.Error(), errLineTooLong.Error()) {
		t.Fatalf("Upload err = %v, want %q", err, errLineTooLong.Error())
	}
}

// An oversized reply arriving while send() is still writing a large binary must abort send()
// via SetWriteDeadline and surface errLineTooLong rather than masking it behind send's write error.
func TestUploadOversizedReplyAbortsInFlightSend(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	go func() {
		// Write exact maxHeaderBytes without a newline while never draining a's writes (so send blocks).
		_, _ = b.Write(bytes.Repeat([]byte("z"), maxHeaderBytes))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Upload(ctx, a, bigBinary(t, 256<<10))
	if !errors.Is(err, errLineTooLong) {
		t.Fatalf("Upload err = %v, want %v", err, errLineTooLong)
	}
}

func TestReadLineLimitedBoundaries(t *testing.T) {
	t.Run("multi_chunk_preserves_trailing_bytes", func(t *testing.T) {
		payload := append(bytes.Repeat([]byte("a"), 9000), '\n')
		trailing := []byte("GZIP-PAYLOAD-FOLLOWS")
		r := bufio.NewReaderSize(bytes.NewReader(append(payload, trailing...)), 4096)

		got, err := readLineLimited(r, maxHeaderBytes)
		if err != nil {
			t.Fatalf("readLineLimited: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("line len = %d, want %d", len(got), len(payload))
		}
		rest, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(rest, trailing) {
			t.Fatalf("trailing = %q (err=%v), want %q", rest, err, trailing)
		}
	})

	t.Run("exact_limit_with_newline_accepted", func(t *testing.T) {
		line := append(bytes.Repeat([]byte("b"), maxHeaderBytes-1), '\n')
		trailing := []byte("NEXT")
		r := bufio.NewReaderSize(bytes.NewReader(append(line, trailing...)), 4096)

		got, err := readLineLimited(r, maxHeaderBytes)
		if err != nil {
			t.Fatalf("readLineLimited exact limit: %v", err)
		}
		if len(got) != maxHeaderBytes {
			t.Fatalf("len = %d, want %d", len(got), maxHeaderBytes)
		}
		rest, err := io.ReadAll(r)
		if err != nil || !bytes.Equal(rest, trailing) {
			t.Fatalf("trailing = %q (err=%v), want %q", rest, err, trailing)
		}
	})

	t.Run("exact_limit_without_newline_fails_immediately_without_blocking", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()

		go func() {
			// Write exactly maxHeaderBytes bytes with no '\n' and keep the pipe open.
			_, _ = b.Write(bytes.Repeat([]byte("c"), maxHeaderBytes))
		}()

		errCh := make(chan error, 1)
		go func() {
			_, err := readLineLimited(bufio.NewReaderSize(a, 4096), maxHeaderBytes)
			errCh <- err
		}()

		select {
		case err := <-errCh:
			if !errors.Is(err, errLineTooLong) {
				t.Fatalf("err = %v, want %v", err, errLineTooLong)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("readLineLimited blocked waiting for byte maxHeaderBytes+1")
		}
	})

	t.Run("eof_mid_line", func(t *testing.T) {
		r := bufio.NewReaderSize(strings.NewReader("partial-without-newline"), 4096)
		if _, err := readLineLimited(r, maxHeaderBytes); !errors.Is(err, io.EOF) {
			t.Fatalf("err = %v, want io.EOF", err)
		}
	})
}

type writeFailTrackingConn struct {
	net.Conn
	failWrites  bool
	activeReads atomic.Int32
	readEntered chan struct{}
	enterOnce   sync.Once
}

func (c *writeFailTrackingConn) Read(p []byte) (int, error) {
	c.activeReads.Add(1)
	defer c.activeReads.Add(-1)
	c.enterOnce.Do(func() {
		if c.readEntered != nil {
			close(c.readEntered)
		}
	})
	return c.Conn.Read(p)
}

func (c *writeFailTrackingConn) Write(p []byte) (int, error) {
	if c.failWrites {
		return 0, errors.New("injected write failure")
	}
	return c.Conn.Write(p)
}

func TestUploadAnswerWaitTimeoutAndCancelJoinReaderGoroutine(t *testing.T) {
	bin := fakeBinary(t, t.TempDir(), "v2")

	t.Run("answer_wait_timeout", func(t *testing.T) {
		old := answerWait
		answerWait = 40 * time.Millisecond
		defer func() { answerWait = old }()

		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		tc := &writeFailTrackingConn{Conn: a, failWrites: true}

		err := Upload(context.Background(), tc, bin)
		if err == nil || !strings.Contains(err.Error(), "injected write failure") {
			t.Fatalf("Upload err = %v, want injected write failure", err)
		}
		if got := tc.activeReads.Load(); got != 0 {
			t.Fatalf("answer reader goroutine still active after Upload returned (activeReads=%d)", got)
		}
	})

	t.Run("ctx_canceled_during_answer_wait", func(t *testing.T) {
		// Repeat 10 rounds so any select race between <-answers and <-ctx.Done() would surface reliably.
		for range 10 {
			a, b := net.Pipe()
			readEntered := make(chan struct{})
			tc := &writeFailTrackingConn{Conn: a, failWrites: true, readEntered: readEntered}

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				done <- Upload(ctx, tc, bin)
			}()

			select {
			case <-readEntered:
			case <-time.After(3 * time.Second):
				cancel()
				_ = a.Close()
				_ = b.Close()
				t.Fatal("answer reader did not start")
			}
			start := time.Now()
			cancel()

			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					_ = a.Close()
					_ = b.Close()
					t.Fatalf("Upload err = %v, want context.Canceled", err)
				}
				if elapsed := time.Since(start); elapsed > 2*time.Second {
					_ = a.Close()
					_ = b.Close()
					t.Fatalf("Upload took %v after ctx cancel (waited for answerWait)", elapsed)
				}
			case <-time.After(3 * time.Second):
				_ = a.Close()
				_ = b.Close()
				t.Fatal("Upload did not return promptly after ctx cancel")
			}
			if got := tc.activeReads.Load(); got != 0 {
				_ = a.Close()
				_ = b.Close()
				t.Fatalf("answer reader goroutine still active after canceled Upload (activeReads=%d)", got)
			}
			_ = a.Close()
			_ = b.Close()
		}
	})

	t.Run("ctx_canceled_after_send_waiting_for_reply", func(t *testing.T) {
		a, b := net.Pipe()
		defer a.Close()
		defer b.Close()
		bodyDrained := make(chan struct{})
		go func() {
			r := bufio.NewReader(b)
			if _, err := r.ReadBytes('\n'); err != nil {
				return
			}
			// Read the exact gzip stream so Upload's send() finishes cleanly, then stay silent.
			zr, err := gzip.NewReader(r)
			if err != nil {
				return
			}
			zr.Multistream(false)
			_, _ = io.Copy(io.Discard, zr)
			_ = zr.Close()
			close(bodyDrained)
			_, _ = io.Copy(io.Discard, r)
		}()

		tc := &writeFailTrackingConn{Conn: a}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- Upload(ctx, tc, bin)
		}()

		select {
		case <-bodyDrained:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("peer did not drain upload body")
		}
		cancel()

		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Upload err = %v, want context.Canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Upload did not return after ctx cancel")
		}
		if got := tc.activeReads.Load(); got != 0 {
			t.Fatalf("answer reader goroutine still active after Upload returned (activeReads=%d)", got)
		}
	})
}
