// Package rpc is a tiny bidirectional JSON-lines RPC used between hub and node over SSH stdio.
//
// Each line is one of:
//
//	{"id":"h1","method":"m","params":{}}      request
//	{"id":"h1","result":{}} / {"id":"h1","error":{"message":"…"}}   response
//	{"event":"e","params":{}}                 one-way event
//	{"event":"rpc.cancel","params":{"id":"h1"}}   the caller no longer waits for request h1 (Peer.Cancels)
package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

type EventHandler func(event string, params json.RawMessage)

type message struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	// line is the whole line, written already (Marshaled params); nothing else is set.
	line []byte
}

type Error struct {
	Message string `json:"message"`
	// Code and Data are set by handlers that return an *Error themselves, for callers that act on the kind of
	// failure (a refused send) rather than show the message.
	Code string          `json:"code,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// WireError is its code and data, for the extension SDK (sdk/go/ext) to send as they are.
func (e *Error) WireError() (string, json.RawMessage) { return e.Code, e.Data }

var ErrClosed = errors.New("rpc: connection closed")

var errFrameBudget = errors.New("rpc: runtime frame budget exceeded")

// ErrDropped is what Call answers when the connection closed after the request went out: the other side may have done
// it. It is ErrClosed too (errors.Is), with the same words (a relay further on passes them as they are).
var ErrDropped error = droppedError{}

type droppedError struct{}

func (droppedError) Error() string        { return ErrClosed.Error() }
func (droppedError) Is(target error) bool { return target == ErrClosed }

// ErrNotWritten is matched (errors.Is) by what Call answers when the caller's ctx ended before the request could be
// written (the writer busy with a long write ahead): the other side never saw it. The error is still the ctx's
// (errors.Is DeadlineExceeded / Canceled), with its words.
var ErrNotWritten = errors.New("rpc: request not written")

type notWrittenError struct{ err error }

func (e notWrittenError) Error() string        { return e.err.Error() }
func (e notWrittenError) Unwrap() error        { return e.err }
func (e notWrittenError) Is(target error) bool { return target == ErrNotWritten }

// ErrWriteDelayed is matched (errors.Is) by what Call answers when its request reached the wire only after waiting
// behind another write, then its reply wait ended. The error is still the ctx's error. A ping delayed this way says
// the write channel was busy; it is not the same as an uninterrupted ping that nobody answered.
var ErrWriteDelayed = errors.New("rpc: request written after waiting for the writer")

type writeDelayedError struct {
	err       error
	remaining time.Duration
}

func (e writeDelayedError) Error() string        { return e.err.Error() }
func (e writeDelayedError) Unwrap() error        { return e.err }
func (e writeDelayedError) Is(target error) bool { return target == ErrWriteDelayed }

// WriteDelayRemaining reports how much of the caller's deadline budget remained when its delayed request finished
// writing. Negative means the write finished after the deadline. The boolean is false for other errors.
func WriteDelayRemaining(err error) (time.Duration, bool) {
	var e writeDelayedError
	if errors.As(err, &e) {
		return e.remaining, true
	}
	return 0, false
}

// Limits are optional transport budgets for untrusted runtime peers. Zero preserves
// existing process/node behavior; configure them before Serve or any concurrent call.
type Limits struct{ FrameBytes, ConcurrentRequests, PendingCalls int }

type Peer struct {
	Limits       Limits
	TrackReplies bool // enable AfterReply hooks for a resident's drain boundary
	prefix       string
	w            io.Writer
	// wsem is a one-slot semaphore around w, not a mutex: a write can block for good (SSH stdin to a frozen node,
	// a stalled relay WebSocket), and whoever waits behind it must still return on its ctx or on Close.
	wsem         chan struct{}
	nextID       atomic.Int64
	readSeq      atomic.Uint64
	lastRead     atomic.Int64 // unix nanoseconds; any positive raw read, including part of a JSON line
	lastActivity atomic.Int64 // last input that was not a deadline error response
	deadlineRead atomic.Uint64
	writeWaiters atomic.Int64
	mu           sync.Mutex
	pending      map[string]chan message
	methods      map[string]string // the method of each pending call, by id: what Close reports as cut off
	suspended    bool
	closed       chan struct{}
	once         sync.Once
	err          error

	OnRequest Handler
	OnEvent   EventHandler
	// Cancels: the methods whose calls tell the other side when their ctx ends first (cancelEvent), which cancels
	// that request's ctx. None by default: extensions and connectors speak this protocol too and need not know the
	// event, and most calls are meant to finish on the other side anyway. A peer that does not know the event ignores
	// it, as before: its handler runs on.
	Cancels map[string]bool

	// OnClose, when set, is told once why the peer closed and the methods of the calls still waiting for an answer
	// then (sorted; a method twice for two calls): they fail with ErrDropped or ErrClosed, which alone says nothing of
	// what was cut off. Called from Close, outside the peer's lock; set it before Serve or any call.
	OnClose func(err error, inflight []string)

	running map[string]context.CancelFunc // requests being handled, by id: what a cancelEvent ends
	// Only bounded peers couple accepted work to closure; legacy peers retain their cancellation semantics.
	runtimeCancel context.CancelFunc
}

// cancelEvent is sent for a request the caller no longer waits for: an approval the hub asks for on the phone stays
// pending otherwise, and allowing it later runs a tool call nobody is waiting on (a stale message sent, #2597).
const cancelEvent = "rpc.cancel"

// connKey carries the connection's context in a handler's ctx (ConnContext).
type connKey struct{}

// ConnContext is the context of the connection a handler's request came in on: it ends with the connection, not with
// the request. A handler's own ctx ends when the handler returns (and when the caller cancels it), so anything meant
// to outlive the request (a watch that keeps pushing after its reply) must hang off this one (#3139: a transcript
// watch that took the request's ctx ended with the reply, and no session's items were pushed any more). A ctx that
// did not come from Serve is its own connection.
func ConnContext(ctx context.Context) context.Context {
	if c, ok := ctx.Value(connKey{}).(context.Context); ok {
		return c
	}
	return ctx
}

// New creates a peer. prefix distinguishes request ids by side ("h" for hub, "n" for node).
func New(prefix string, w io.Writer) *Peer {
	return &Peer{prefix: prefix, w: w, pending: map[string]chan message{}, methods: map[string]string{}, running: map[string]context.CancelFunc{}, closed: make(chan struct{}), wsem: make(chan struct{}, 1)}
}

// PingActivity is when this connection last showed activity that can make a ping timeout ambiguous: any recent raw
// input, except that a deadline error response does not prove anything beyond the relay having answered. Partial JSON
// lines count. If ordinary traffic is already buffered behind a deadline in the same raw read, that read counts.
func (p *Peer) PingActivity() time.Time {
	if seq := p.readSeq.Load(); seq != 0 && seq == p.deadlineRead.Load() {
		ns := p.lastActivity.Load()
		if ns == 0 {
			return time.Time{}
		}
		return time.Unix(0, ns)
	}
	return p.lastReadTime()
}

func (p *Peer) lastReadTime() time.Time {
	ns := p.lastRead.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// WriteWaiters is how many messages are queued for the writer, not counting the write that currently owns it.
func (p *Peer) WriteWaiters() int64 { return p.writeWaiters.Load() }

type readActivity struct {
	peer *Peer
	r    io.Reader
}

func (r readActivity) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if n > 0 {
		r.peer.readSeq.Add(1)
		r.peer.lastRead.Store(time.Now().UnixNano())
	}
	return n, err
}

func deadlineMessage(m message) bool {
	return m.ID != "" && m.Error != nil && m.Error.Code == "" && m.Error.Message == context.DeadlineExceeded.Error()
}

// nonDeadlineTraffic reports traffic already buffered behind a deadline reply that cannot be another deadline reply.
// Complete later lines are decoded exactly. An unfinished line counts only once its prefix identifies an event,
// request or result; a partial error remains uncounted until its whole line proves that it is not another deadline.
func nonDeadlineTraffic(b []byte) bool {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		var m message
		if err := json.Unmarshal(dropCR(b[:i]), &m); err != nil || !deadlineMessage(m) {
			return true
		}
		b = b[i+1:]
	}
	tail := bytes.TrimSpace(b)
	if len(tail) == 0 {
		return false
	}
	var m message
	if err := json.Unmarshal(tail, &m); err == nil {
		return !deadlineMessage(m)
	}
	return bytes.Contains(tail, []byte(`"event"`)) || bytes.Contains(tail, []byte(`"method"`)) || bytes.Contains(tail, []byte(`"result"`))
}

// Serve reads messages until r fails; it returns the read error.
func (p *Peer) Serve(ctx context.Context, r io.Reader) error {
	if p.Limits.ConcurrentRequests > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		p.mu.Lock()
		p.runtimeCancel = cancel
		select {
		case <-p.closed:
			cancel()
		default:
		}
		p.mu.Unlock()
	}
	maximum := 64 * 1024 * 1024
	if p.Limits.FrameBytes > 0 {
		maximum = p.Limits.FrameBytes
	}
	sc := newLineReader(readActivity{peer: p, r: r}, maximum) // raw reads count partial lines as activity too
	for sc.Scan() {
		readAt := p.lastReadTime()
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			p.lastActivity.Store(readAt.UnixNano())
			continue
		}
		if deadlineMessage(m) {
			// Classify the rest of this raw read before waking the caller. Otherwise a deadline followed by an ordinary
			// message in one packet could still close a live connection; deadline-only read-ahead must not keep it alive.
			if nonDeadlineTraffic(sc.buffered()) {
				p.lastActivity.Store(readAt.UnixNano())
			}
			p.deadlineRead.Store(p.readSeq.Load())
		} else {
			p.lastActivity.Store(readAt.UnixNano())
		}
		switch {
		case m.Method != "":
			// Registered before the handler starts: a cancel right behind the request finds it.
			hctx, cancel := context.WithCancel(context.WithValue(ctx, connKey{}, ctx))
			p.mu.Lock()
			if p.Limits.ConcurrentRequests > 0 && (len(p.running) >= p.Limits.ConcurrentRequests || p.running[m.ID] != nil || m.ID == "") {
				p.mu.Unlock()
				cancel()
				err := errors.New("rpc: runtime request budget exceeded")
				p.Close(err)
				return err
			}
			p.running[m.ID] = cancel
			p.mu.Unlock()
			go p.handle(hctx, m)
		case m.Event == cancelEvent:
			var c struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(m.Params, &c)
			p.mu.Lock()
			cancel := p.running[c.ID]
			p.mu.Unlock()
			if cancel != nil {
				cancel()
			}
		case m.Event != "":
			if p.OnEvent != nil {
				p.OnEvent(m.Event, m.Params)
			}
		case m.ID != "":
			p.mu.Lock()
			ch := p.pending[m.ID]
			if ch != nil {
				ch <- m // publish the reply before WaitPending can observe its completion
			}
			delete(p.pending, m.ID)
			delete(p.methods, m.ID)
			p.mu.Unlock()
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	p.Close(err)
	return err
}

func (p *Peer) handle(ctx context.Context, m message) {
	if p.TrackReplies {
		hooks := &replyHooks{}
		ctx = context.WithValue(ctx, replyKey{}, hooks)
		defer hooks.run()
	}
	defer func() {
		p.mu.Lock()
		cancel := p.running[m.ID]
		delete(p.running, m.ID)
		p.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}()
	var res any
	var err error
	if p.OnRequest == nil {
		err = fmt.Errorf("no handler for %s", m.Method)
	} else {
		res, err = p.OnRequest(ctx, m.Method, m.Params)
	}
	out := message{ID: m.ID}
	var coded *Error
	if errors.As(err, &coded) {
		out.Error = coded
	} else if err != nil {
		out.Error = &Error{Message: err.Error()}
	} else {
		b, merr := marshal(res)
		if merr != nil {
			out.Error = &Error{Message: merr.Error()}
		} else {
			out.Result = b
		}
	}
	if err := p.write(out); errors.Is(err, errFrameBudget) {
		// The request must get an error, not a permanently unresolved promise.
		// If even the error cannot fit, disconnect so all pending calls fail.
		if err := p.write(message{ID: m.ID, Error: &Error{Message: errFrameBudget.Error()}}); err != nil {
			p.Close(err)
		}
	}
}

func (p *Peer) write(m message) error {
	return p.writeContext(context.Background(), m)
}

// encodeLine is marshal(m) and its newline, encoded into one buffer: the same bytes and errors without copying the
// line once more to append the newline.
func encodeLine(m message) ([]byte, error) {
	if m.line != nil {
		return m.line, nil
	}
	var b bytes.Buffer
	if err := plainEncoder(&b).Encode(m); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// marshal is json.Marshal(v) with < > & as they are. Escaped, they cost six bytes each, and a transcript's HTML or
// code went out at up to three times its size in every page and push (#2907); a later encoder keeps escapes it is
// given, so only the first hop can leave them out. Escapes already in a json.RawMessage stay as they are, and U+2028
// and U+2029 are still escaped, as encoding/json does without HTML escaping.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	if err := plainEncoder(&b).Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// plainEncoder is an encoder to w that leaves < > & as they are (marshal).
func plainEncoder(w io.Writer) *json.Encoder {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc
}

func (p *Peer) writeContext(ctx context.Context, m message) error {
	_, err := p.writeContextObserved(ctx, m)
	return err
}

type writeObservation struct {
	delayed   bool
	remaining time.Duration
}

// writeContextObserved also reports whether this message had to wait behind another writer before reaching the wire,
// and how much of the caller's deadline remained when the write finished.
func (p *Peer) writeContextObserved(ctx context.Context, m message) (writeObservation, error) {
	b, err := encodeLine(m)
	if err != nil {
		return writeObservation{}, err
	}
	if p.Limits.FrameBytes > 0 && len(b) > p.Limits.FrameBytes {
		return writeObservation{}, errFrameBudget
	}
	// Wait for the writer, but not past the caller or the connection: the write ahead may never finish. A write
	// already under way cannot be interrupted. Try the immediate acquisition first so delayed means another write held
	// the slot when this message arrived, rather than merely that a select chose one ready case over another.
	observation := writeObservation{}
	select {
	case p.wsem <- struct{}{}:
	default:
		observation.delayed = true
		p.writeWaiters.Add(1)
		select {
		case p.wsem <- struct{}{}:
			p.writeWaiters.Add(-1)
		case <-ctx.Done():
			p.writeWaiters.Add(-1)
			return observation, ctx.Err()
		case <-p.closed:
			p.writeWaiters.Add(-1)
			return observation, ErrClosed
		}
	}
	defer func() { <-p.wsem }()
	// Both may have become ready together and select picks at random: check again so a cancelled request never
	// reaches the wire.
	if err := ctx.Err(); err != nil {
		return observation, err
	}
	select {
	case <-p.closed:
		return observation, ErrClosed
	default:
	}
	_, err = p.w.Write(b)
	if deadline, ok := ctx.Deadline(); ok {
		observation.remaining = deadline.Sub(time.Now())
	}
	return observation, err
}

// Call sends a request and decodes the result into out (if non-nil). Params that are Appended are written straight
// into the request's line.
func (p *Peer) Call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := fmt.Sprintf("%s%d", p.prefix, p.nextID.Add(1))
	ch := make(chan message, 1)
	p.mu.Lock()
	if p.suspended {
		p.mu.Unlock()
		return ErrSuspended
	}
	if p.Limits.PendingCalls > 0 && len(p.pending) >= p.Limits.PendingCalls {
		p.mu.Unlock()
		return errors.New("rpc: runtime pending-call budget exceeded")
	}
	p.pending[id] = ch
	p.methods[id] = method
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.pending, id)
		delete(p.methods, id)
		p.mu.Unlock()
	}()
	var req message
	if a, ok := params.(Appended); ok {
		if size, ok := callSize(id, method, a); ok {
			if p.Limits.FrameBytes > 0 && size > p.Limits.FrameBytes { // before the line is made
				return errFrameBudget
			}
			req.line = callLine(id, method, a, size)
		} else {
			params = json.RawMessage(a.Append(make([]byte, 0, a.Size))) // the encoder writes the envelope
		}
	}
	if req.line == nil {
		raw, err := marshal(params)
		if err != nil {
			return err
		}
		req = message{ID: id, Method: method, Params: raw}
	}
	writeObservation, err := p.writeContextObserved(ctx, req)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil && errors.Is(err, cerr) {
			return notWrittenError{err} // waited for the writer (a long write ahead) and gave up: never on the wire
		}
		return err
	}
	var m message
	select {
	case m = <-ch:
	case <-ctx.Done():
		if p.Cancels[method] {
			params, _ := json.Marshal(map[string]string{"id": id})
			_ = p.write(message{Event: cancelEvent, Params: params})
		}
		if writeObservation.delayed {
			return writeDelayedError{err: ctx.Err(), remaining: writeObservation.remaining}
		}
		return ctx.Err()
	case <-p.closed:
		// A drain can close the transport after Serve published the reply but before this goroutine resumed.
		// That known result wins over an ambiguous connection drop.
		select {
		case m = <-ch:
		default:
			return ErrDropped
		}
	}
	if m.Error != nil {
		return m.Error
	}
	if out != nil && len(m.Result) > 0 {
		return json.Unmarshal(m.Result, out)
	}
	return nil
}

// Notify sends a one-way event.
func (p *Peer) Notify(event string, params any) error {
	return p.NotifyContext(context.Background(), event, params)
}

// NotifyContext bounds waiting for the writer. Once writing starts, the transport owner must close a stalled
// connection to interrupt it; cancellation alone cannot stop an arbitrary io.Writer.
func (p *Peer) NotifyContext(ctx context.Context, event string, params any) error {
	if raw, ok := params.(Marshaled); ok {
		if line, ok := marshaledLine(event, raw); ok {
			return p.writeContext(ctx, message{line: line})
		}
		params = json.RawMessage(raw)
	}
	// Bytes already encoded go into the envelope as they are: its encoder checks and compacts them as this one would,
	// with the same errors, so the line is the same without encoding them twice (a computer's session list each time
	// it changes). An empty one is what json.Marshal says it is (an error).
	if raw, ok := params.(json.RawMessage); ok && len(raw) > 0 {
		return p.writeContext(ctx, message{Event: event, Params: raw})
	}
	raw, err := marshal(params)
	if err != nil {
		return err
	}
	return p.writeContext(ctx, message{Event: event, Params: raw})
}

func (p *Peer) Close(err error) {
	p.once.Do(func() {
		var inflight []string
		p.mu.Lock() // taken before the waiters wake: they drop their entries as they return
		if p.OnClose != nil {
			for _, m := range p.methods {
				inflight = append(inflight, m)
			}
		}
		p.err = err
		close(p.closed)
		if p.runtimeCancel != nil {
			p.runtimeCancel()
		}
		p.mu.Unlock()
		if p.OnClose != nil {
			sort.Strings(inflight)
			p.OnClose(err, inflight)
		}
	})
}

func (p *Peer) Done() <-chan struct{} { return p.closed }

// Err is why the peer was closed (what Close was given); nil while it is open.
func (p *Peer) Err() error {
	select {
	case <-p.closed:
		return p.err
	default:
		return nil
	}
}
