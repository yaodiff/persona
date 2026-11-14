package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// pair builds two Peers wired back to back through io.Pipe, the way hub and node talk over SSH stdio.
// Each Serve runs in its own goroutine and is stopped by the returned cleanup.
func pair(t *testing.T) (*Peer, *Peer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a2bR, a2bW := io.Pipe()
	b2aR, b2aW := io.Pipe()
	hub := New("h", a2bW)
	node := New("n", b2aW)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hub.Serve(ctx, b2aR) }()
	go func() { defer wg.Done(); _ = node.Serve(ctx, a2bR) }()
	t.Cleanup(func() {
		cancel()
		_ = a2bW.Close()
		_ = b2aW.Close()
		wg.Wait()
	})
	return hub, node
}

func jsonArg(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("params %s: %v", raw, err)
	}
	return m
}

func TestCallBothDirections(t *testing.T) {
	hub, node := pair(t)
	node.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "node.ping":
			return map[string]any{"pong": jsonArg(t, params)["n"]}, nil
		case "node.add":
			p := jsonArg(t, params)
			return map[string]any{"sum": p["a"].(float64) + p["b"].(float64)}, nil
		}
		return nil, errors.New("unknown method " + method)
	}
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "hub.hello" {
			return nil, errors.New("unknown method")
		}
		return map[string]any{"who": jsonArg(t, params)["who"]}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var pong struct {
		Pong string `json:"pong"`
	}
	if err := hub.Call(ctx, "node.ping", map[string]any{"n": "hi"}, &pong); err != nil {
		t.Fatalf("hub→node call: %v", err)
	}
	if pong.Pong != "hi" {
		t.Fatalf("pong = %q", pong.Pong)
	}

	var sum struct {
		Sum float64 `json:"sum"`
	}
	if err := hub.Call(ctx, "node.add", map[string]any{"a": 2, "b": 3}, &sum); err != nil {
		t.Fatalf("node.add: %v", err)
	}
	if sum.Sum != 5 {
		t.Fatalf("sum = %v", sum.Sum)
	}

	// The reverse direction shares the same two peers.
	var hello struct {
		Who string `json:"who"`
	}
	if err := node.Call(ctx, "hub.hello", map[string]any{"who": "node"}, &hello); err != nil {
		t.Fatalf("node→hub call: %v", err)
	}
	if hello.Who != "node" {
		t.Fatalf("who = %q", hello.Who)
	}
}

func TestCallPropagatesErrorMessage(t *testing.T) {
	hub, node := pair(t)
	node.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("herdr is not running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := hub.Call(ctx, "session.start", nil, nil)
	if err == nil || err.Error() != "herdr is not running" {
		t.Fatalf("want the remote message, got %v", err)
	}
}

// A call's params and its reply cross the line with < > & as they are, not six bytes each (#2907): a message or a
// tool input of HTML or code would go out at up to three times its size.
func TestCallKeepsHTMLCharacters(t *testing.T) {
	const html = `<b>&amp;</b>`
	hub, node := pair(t)
	var got json.RawMessage
	node.OnRequest = func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		got = append(json.RawMessage(nil), params...)
		return map[string]string{"html": html}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var reply json.RawMessage
	if err := hub.Call(ctx, "session.send", map[string]string{"html": html}, &reply); err != nil {
		t.Fatal(err)
	}
	for hop, b := range map[string]json.RawMessage{"params": got, "reply": reply} {
		if want := `{"html":"` + html + `"}`; string(b) != want {
			t.Errorf("%s: %s, want %s", hop, b, want)
		}
	}
}

func TestNoHandlerIsAnError(t *testing.T) {
	hub, node := pair(t)
	node.OnRequest = nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hub.Call(ctx, "node.info", nil, nil); err == nil {
		t.Fatal("want an error when the far side has no handler")
	}
}

func TestNotifyDeliversEvents(t *testing.T) {
	hub, node := pair(t)
	got := make(chan map[string]any, 1)
	node.OnEvent = func(event string, params json.RawMessage) {
		if event != "session.screen" {
			return
		}
		got <- jsonArg(t, params)
	}
	if err := hub.Notify("session.screen", map[string]any{"session_id": "s1", "status": "working"}); err != nil {
		t.Fatalf("notify: %v", err)
	}
	select {
	case p := <-got:
		if p["session_id"] != "s1" || p["status"] != "working" {
			t.Fatalf("unexpected params %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event was not delivered")
	}
}

func TestNotifyWithoutHandlerDoesNotBlock(t *testing.T) {
	hub, node := pair(t)
	node.OnEvent = nil
	done := make(chan error, 1)
	go func() { done <- hub.Notify("sessions.changed", map[string]any{}) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("notify: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("notify blocked")
	}
}

func TestCallAfterPeerClosedReturnsErrClosed(t *testing.T) {
	hub := New("h", io.Discard)
	hub.Close(io.EOF)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := hub.Call(ctx, "x", nil, nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
	select {
	case <-hub.Done():
	default:
		t.Fatal("Done() should be closed")
	}
}

// A call refused before its request went out is ErrClosed; one whose connection closed while it was out is
// ErrDropped (the other side may have done it), which is ErrClosed too.
func TestCallDroppedWhileOutIsErrDropped(t *testing.T) {
	closed := New("h", io.Discard)
	closed.Close(io.EOF)
	if err := closed.Call(context.Background(), "x", nil, nil); errors.Is(err, ErrDropped) {
		t.Fatalf("not sent, yet dropped: %v", err)
	}
	out := New("h", io.Discard)
	go func() {
		time.Sleep(50 * time.Millisecond)
		out.Close(io.EOF)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := out.Call(ctx, "x", nil, nil)
	if !errors.Is(err, ErrDropped) || !errors.Is(err, ErrClosed) || err.Error() != ErrClosed.Error() {
		t.Fatalf("want ErrDropped (as ErrClosed), got %v", err)
	}
}

func TestServeReturnsOnEOF(t *testing.T) {
	r, w := io.Pipe()
	p := New("h", io.Discard)
	errc := make(chan error, 1)
	go func() { errc <- p.Serve(context.Background(), r) }()
	_ = w.Close()
	select {
	case err := <-errc:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("want io.EOF, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
}

func TestPingActivityTracksAnUnfinishedFrame(t *testing.T) {
	const partial = `{"event":"file.chunk","params":"still being written`
	p := New("h", io.Discard)
	if got := p.PingActivity(); !got.IsZero() {
		t.Fatalf("PingActivity before Serve = %v, want zero", got)
	}
	if err := p.Serve(context.Background(), strings.NewReader(partial)); !errors.Is(err, io.EOF) {
		t.Fatalf("Serve = %v, want EOF", err)
	}
	if got := p.PingActivity(); got.IsZero() {
		t.Fatal("PingActivity stayed zero although a partial frame was read")
	}
}

// Even an error response whose caller has already gone away is not new evidence of a working node. Other traffic
// before it remains activity; another late deadline does not move that timestamp forward.
func TestPingActivityIgnoresLateDeadlineResponses(t *testing.T) {
	r, w := io.Pipe()
	p := New("h", io.Discard)
	events := make(chan string, 1)
	p.OnEvent = func(event string, _ json.RawMessage) { events <- event }
	serveDone := make(chan error, 1)
	go func() { serveDone <- p.Serve(context.Background(), r) }()
	t.Cleanup(func() {
		_ = w.Close()
		_ = r.Close()
		p.Close(io.EOF)
		select {
		case <-serveDone:
		case <-time.After(time.Second):
			t.Error("Serve did not stop")
		}
	})
	write := func(line string) {
		t.Helper()
		done := make(chan error, 1)
		go func() {
			_, err := io.WriteString(w, line+"\n")
			done <- err
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("write: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("write did not reach Serve")
		}
	}
	waitDeadline := func(previous uint64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for p.deadlineRead.Load() <= previous {
			if time.Now().After(deadline) {
				t.Fatal("deadline response was not processed")
			}
			<-time.After(time.Millisecond)
		}
	}
	write(`{"id":"h1","error":{"message":"context deadline exceeded"}}`)
	waitDeadline(0)
	if got := p.PingActivity(); !got.IsZero() {
		t.Fatalf("late deadline created activity %v", got)
	}
	write(`{"event":"work"}`)
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("event was not processed")
	}
	active := p.PingActivity()
	if active.IsZero() {
		t.Fatal("ordinary event did not count as activity")
	}
	previous := p.deadlineRead.Load()
	write(`{"id":"h2","error":{"message":"context deadline exceeded"}}`)
	waitDeadline(previous)
	if got := p.PingActivity(); !got.Equal(active) {
		t.Fatalf("another late deadline moved activity from %v to %v", active, got)
	}
}

func TestPingActivityClassifiesReadAheadBeforeReply(t *testing.T) {
	const deadline = `{"id":"h1","error":{"message":"context deadline exceeded"}}`
	for _, tc := range []struct {
		name   string
		tail   string
		active bool
	}{
		{name: "complete ordinary event", tail: `{"event":"file.chunk","params":{"data":"x"}}` + "\n", active: true},
		{name: "partial ordinary event", tail: `{"event":"file.chunk","params":"still arriving`, active: true},
		{name: "complete second deadline", tail: `{"id":"h2","error":{"message":"context deadline exceeded"}}` + "\n", active: false},
		{name: "partial second deadline", tail: `{"id":"h2","error":{"message":"context dead`, active: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			p := New("h", io.Discard)
			reply := make(chan message, 1)
			p.pending["h1"], p.methods["h1"] = reply, "ping"
			serveDone := make(chan error, 1)
			go func() { serveDone <- p.Serve(context.Background(), r) }()
			t.Cleanup(func() {
				_ = w.Close()
				_ = r.Close()
				p.Close(io.EOF)
				select {
				case <-serveDone:
				case <-time.After(time.Second):
					t.Error("Serve did not stop")
				}
			})
			writeDone := make(chan error, 1)
			go func() {
				_, err := io.WriteString(w, deadline+"\n"+tc.tail)
				writeDone <- err
			}()
			select {
			case <-reply:
			case <-time.After(time.Second):
				t.Fatal("deadline reply was not delivered")
			}
			if got := p.PingActivity(); got.IsZero() == tc.active {
				t.Fatalf("PingActivity = %v, active = %v", got, tc.active)
			}
			select {
			case err := <-writeDone:
				if err != nil {
					t.Fatalf("write: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("combined write did not finish")
			}
		})
	}
}

func TestCallContextCancelled(t *testing.T) {
	// The far side never answers, so the caller has to fall back to its context.
	p := New("h", io.Discard)
	p.OnRequest = nil
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := p.Call(ctx, "x", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the context error, got %v", err)
	}
}

func TestPeerPrefixDistinguishesIDs(t *testing.T) {
	hub, node := pair(t)
	seen := make(chan string, 2)
	node.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		seen <- method
		return struct{}{}, nil
	}
	hub.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		seen <- method
		return struct{}{}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hub.Call(ctx, "a", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := node.Call(ctx, "b", nil, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for range 2 {
		got[<-seen] = true
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("missing calls: %+v", got)
	}
}

func TestUnknownJSONLinesAreSkipped(t *testing.T) {
	r, w := io.Pipe()
	p := New("h", io.Discard)
	events := make(chan string, 4)
	p.OnEvent = func(event string, _ json.RawMessage) { events <- event }
	errc := make(chan error, 1)
	go func() { errc <- p.Serve(context.Background(), r) }()
	for _, line := range []string{"garbage", `{}`, `{"event":"one"}`, `{"event":"two"}`} {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	<-errc
	close(events)
	var got []string
	for e := range events {
		got = append(got, e)
	}
	if len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Fatalf("events = %+v", got)
	}
}

func TestCodedErrorReachesCaller(t *testing.T) {
	a2b, b2a := io.Pipe()
	c2d, d2c := io.Pipe()
	a := New("a", b2a)
	b := New("b", d2c)
	b.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return nil, &Error{Code: "input_draft", Message: "draft there", Data: json.RawMessage(`{"draft":"hi"}`)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = a.Serve(ctx, c2d) }()
	go func() { _ = b.Serve(ctx, a2b) }()
	err := a.Call(ctx, "x", nil, nil)
	var re *Error
	if !errors.As(err, &re) || re.Code != "input_draft" || string(re.Data) != `{"draft":"hi"}` || re.Message != "draft there" {
		t.Fatalf("got %#v", err)
	}
}

// A caller that stopped waiting before dispatch must not start work on the remote machine.
func TestCanceledCallDoesNotSend(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline"}[expired], func(t *testing.T) {
			var wire bytes.Buffer
			p := New("h", &wire)
			ctx, cancel := context.WithCancel(context.Background())
			if expired {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else {
				cancel()
			}
			defer cancel()
			wantErr := ctx.Err()
			if err := p.Call(ctx, "session.start", map[string]string{"prompt": "hello"}, nil); !errors.Is(err, wantErr) {
				t.Fatalf("call = %v, want %v", err, wantErr)
			}
			if wire.Len() != 0 {
				t.Fatalf("canceled request reached wire: %s", wire.String())
			}
		})
	}
}

type callMarshalFunc func() ([]byte, error)

func (f callMarshalFunc) MarshalJSON() ([]byte, error) { return f() }

func TestCallCanceledDuringMarshalDoesNotSend(t *testing.T) {
	var wire bytes.Buffer
	p := New("h", &wire)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	params := callMarshalFunc(func() ([]byte, error) {
		cancel()
		return []byte(`{}`), nil
	})
	if err := p.Call(ctx, "session.start", params, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("call = %v, want canceled", err)
	}
	if wire.Len() != 0 {
		t.Fatalf("canceled request reached wire: %s", wire.String())
	}
}

// stuckWriter blocks every Write until release is closed, like SSH stdin to a frozen node; then it records the
// bytes. started gets a signal per Write that has begun.
type stuckWriter struct {
	release chan struct{}
	started chan struct{}
	mu      sync.Mutex
	wire    bytes.Buffer
}

func newStuckWriter() *stuckWriter {
	return &stuckWriter{release: make(chan struct{}), started: make(chan struct{}, 16)}
}

func (w *stuckWriter) Write(b []byte) (int, error) {
	w.started <- struct{}{}
	<-w.release
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wire.Write(b)
}

func (w *stuckWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.wire.String()
}

// blockWriter has p send a Notify that sticks in Write, so the writer is busy for whatever comes next.
func blockWriter(t *testing.T, p *Peer, w *stuckWriter) {
	t.Helper()
	go func() { _ = p.Notify("stuck", nil) }()
	select {
	case <-w.started:
	case <-time.After(time.Second):
		t.Fatal("first write did not start")
	}
}

func TestCallCanceledBeforeWriterAvailableDoesNotSend(t *testing.T) {
	w := newStuckWriter()
	p := New("h", w)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marshaled := make(chan struct{})
	params := callMarshalFunc(func() ([]byte, error) {
		close(marshaled)
		return []byte(`{}`), nil
	})
	// Another outbound message owns the writer until after this call loses its caller.
	blockWriter(t, p, w)
	done := make(chan error, 1)
	go func() { done <- p.Call(ctx, "session.start", params, nil) }()
	select {
	case <-marshaled:
	case <-time.After(time.Second):
		close(w.release)
		t.Fatal("request did not reach serialization")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call = %v, want canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled call did not return while the writer was busy")
	}
	close(w.release)
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(w.String(), "session.start") {
		t.Fatalf("queued canceled request reached wire: %s", w.String())
	}
}

// A write stuck on the wire must not hold later calls past their own deadline (#2994).
func TestCallDeadlineWhileWriterBlocked(t *testing.T) {
	w := newStuckWriter()
	p := New("h", w)
	blockWriter(t, p, w)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- p.Call(ctx, "session.start", nil, nil) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call = %v, want deadline exceeded", err)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("call took %v, want about its 200ms deadline", took)
		}
	case <-time.After(2 * time.Second):
		close(w.release)
		t.Fatal("call still blocked behind a stuck write 2s after its 200ms deadline")
	}
	close(w.release)
	time.Sleep(50 * time.Millisecond)
	if strings.Contains(w.String(), "session.start") {
		t.Fatalf("timed-out request reached wire after the writer freed up: %s", w.String())
	}
}

// Close wakes calls and notifications waiting for a stuck writer with ErrClosed (#2994).
func TestCloseWakesWritersWaitingBehindStuckWrite(t *testing.T) {
	w := newStuckWriter()
	p := New("h", w)
	defer close(w.release)
	blockWriter(t, p, w)
	callDone := make(chan error, 1)
	notifyDone := make(chan error, 1)
	go func() { callDone <- p.Call(context.Background(), "session.start", nil, nil) }()
	go func() { notifyDone <- p.Notify("later", nil) }()
	time.Sleep(50 * time.Millisecond)
	p.Close(nil)
	for name, ch := range map[string]chan error{"call": callDone, "notify": notifyDone} {
		select {
		case err := <-ch:
			if !errors.Is(err, ErrClosed) {
				t.Fatalf("%s = %v, want ErrClosed", name, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s still blocked behind a stuck write after Close", name)
		}
	}
}

// A call the caller gives up on ends on the other side too, for the methods in Cancels (#2597); the others run on
// there, as they always did.
func TestCancelReachesTheHandler(t *testing.T) {
	hub, node := pair(t)
	node.Cancels = map[string]bool{"wait": true}
	ended := make(chan string, 2)
	hub.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		select {
		case <-ctx.Done():
			ended <- method + " cancelled"
		case <-time.After(300 * time.Millisecond):
			ended <- method + " ran on"
		}
		return nil, nil
	}
	for _, method := range []string{"wait", "other"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		if err := node.Call(ctx, method, nil, nil); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("%s: %v", method, err)
		}
		cancel()
		select {
		case got := <-ended:
			want := map[string]string{"wait": "wait cancelled", "other": "other ran on"}[method]
			if got != want {
				t.Fatalf("got %q, want %q", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: the handler never ended", method)
		}
	}
}

// A handler's ctx ends with its reply; ConnContext is the connection's, which a watch that keeps pushing after the
// reply hangs off (#3139).
func TestConnContextOutlivesTheRequest(t *testing.T) {
	hub, node := pair(t)
	type seen struct{ req, conn context.Context }
	got := make(chan seen, 1)
	hub.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		got <- seen{ctx, ConnContext(ctx)}
		return nil, nil
	}
	if err := node.Call(context.Background(), "watch", nil, nil); err != nil {
		t.Fatal(err)
	}
	s := <-got
	select {
	case <-s.req.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the request's ctx did not end with its reply")
	}
	if s.conn.Err() != nil {
		t.Fatalf("the connection's ctx ended with the request: %v", s.conn.Err())
	}
	if ConnContext(context.Background()) == nil {
		t.Fatal("a ctx that did not come from Serve is its own connection")
	}
}
