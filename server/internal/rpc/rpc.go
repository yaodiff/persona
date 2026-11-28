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

type writeOpKind string

const (
	writeOpRequest writeOpKind = "request"
	writeOpEvent   writeOpKind = "event"
	writeOpReply   writeOpKind = "reply"
	writeOpMessage writeOpKind = "message"
)

type message struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Event  string          `json:"event,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	// line is the whole line, written already (Marshaled params); nothing else is set.
	line []byte
	// opKind and opName label the operation for write-budget diagnostics without changing the wire JSON.
	opKind writeOpKind
	opName string
}

func (m message) writeOp() (kind writeOpKind, op string) {
	if m.opKind != "" || m.opName != "" {
		return m.opKind, m.opName
	}
	switch {
	case m.Method != "":
		return writeOpRequest, m.Method
	case m.Event != "":
		return writeOpEvent, m.Event
	case m.ID != "":
		return writeOpReply, m.ID
	default:
		return writeOpMessage, ""
	}
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

// ErrMessageTooLarge is returned when an outgoing RPC line exceeds Peer.WriteLimit.
var ErrMessageTooLarge = errors.New("rpc: message exceeds write limit")

type writeLimitError struct {
	kind        writeOpKind
	op          string
	size, limit int
}

func (e writeLimitError) Error() string {
	if e.op != "" {
		return fmt.Sprintf("rpc: outgoing %s %s is %d bytes (limit %d)", e.kind, e.op, e.size, e.limit)
	}
	return fmt.Sprintf("rpc: outgoing %s is %d bytes (limit %d)", e.kind, e.size, e.limit)
}

func (writeLimitError) Unwrap() error { return ErrMessageTooLarge }

// ErrDropped is what Call answers when the connection closed after the request went out: the other side may have done
// it. It is ErrClosed too (errors.Is), with the same words (a relay further on passes them as they are).
var ErrDropped error = droppedError{}

type droppedError struct{}

func (droppedError) Error() string        { return ErrClosed.Error() }
func (droppedError) Is(target error) bool { return target == ErrClosed }

// ErrNotSent marks an error returned before the request was written: the caller stopped waiting, or the connection
// closed, while the call still waited to write. The other side never saw the request. NotSent wraps the cause, which
// errors.Is still finds (context.Canceled, ErrClosed) and whose words it keeps, so a caller that cannot tell this
// apart reads it as before.
var ErrNotSent = errors.New("rpc: request not sent")

// NotSent marks err (non-nil) as returned before the request went out (ErrNotSent).
func NotSent(err error) error {
	if err == nil || errors.Is(err, ErrNotSent) {
		return err
	}
	return notSentError{err}
}

type notSentError struct{ err error }

func (e notSentError) Error() string   { return e.err.Error() }
func (e notSentError) Unwrap() []error { return []error{e.err, ErrNotSent} }

// ErrNotWritten is matched (errors.Is) by what Call answers when the caller's ctx ended before the request could be
// written (the writer busy with a long write ahead): the other side never saw it. The error is still the ctx's
// (errors.Is DeadlineExceeded / Canceled), with its words.
var ErrNotWritten = errors.New("rpc: request not written")

type notWrittenError struct{ err error }

func (e notWrittenError) Error() string        { return e.err.Error() }
func (e notWrittenError) Unwrap() error        { return e.err }
func (e notWrittenError) Is(target error) bool { return target == ErrNotWritten }

// ErrWriteDelayed matches a timed-out call that waited behind another write. For a framed ping it also covers
// observed acknowledgements draining earlier events or answers during that ping's response budget.
var ErrWriteDelayed = errors.New("rpc: request written after waiting for the writer")

type writeDelayedError struct {
	err       error
	remaining time.Duration
}

func (e writeDelayedError) Error() string        { return e.err.Error() }
func (e writeDelayedError) Unwrap() error        { return e.err }
func (e writeDelayedError) Is(target error) bool { return target == ErrWriteDelayed }

// WriteDelayRemaining reports the budget left after a delayed write, including observed transport backlog ahead
// of a framed ping. A negative value means the write finished after its deadline.
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
	ReadLimit    int  // optional inbound message budget, in bytes; Limits.FrameBytes takes precedence
	WriteLimit   int  // optional outbound message budget, in bytes; Limits.FrameBytes takes precedence
	TrackReplies bool // enable AfterReply hooks for a resident's drain boundary
	prefix       string
	w            io.Writer
	// wsem is a one-slot semaphore around w, not a mutex: a write can block for good (SSH stdin to a frozen node,
	// a stalled relay WebSocket), and whoever waits behind it must still return on its ctx or on Close.
	wsem         chan struct{}
	nextID       atomic.Int64
	pingActivity atomic.Int64 // unix nanoseconds; the latest input that can explain a late pong (PingActivity)
	lastWrite    atomic.Int64 // unix nanoseconds; the latest write that went through (LastWrite)
	writeWaiters atomic.Int64
	mu           sync.Mutex
	pending      map[string]chan message
	methods      map[string]string // the method of each pending call, by id: what Close reports as cut off
	suspended    bool
	closed       chan struct{}
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

	// OnWriteRejected, when set, is told when an outgoing request, reply or event is rejected for exceeding the write
	// budget: kind ("request", "reply", "event"), operation name (method or event), encoded byte size (newline
	// included), and budget limit. Message contents are never passed. Set it before Serve or any call.
	OnWriteRejected func(kind, op string, size, limit int)

	// LineFilter, when set, rewrites each line this side writes, newline included, just before it goes out (the node
	// hides the values handed to Agent starts, node/secretenv.go); it must keep the line one line. Once lanes are on,
	// the frame writer calls it while other goroutines write. LineWritten, when set, is told of each line as it was
	// encoded (before LineFilter) once all of it is written. Set both before Serve or any call.
	LineFilter  func([]byte) []byte
	LineWritten func([]byte)
	// OnLanes, when set, is told once this side's output is framed (lanes.go), whenever that is: an answer to
	// UpgradeLanes after its caller stopped waiting still switches. Set it before Serve.
	OnLanes func()

	// Lanes (lanes.go): the table EnableLanes set (nil: this peer never frames), the frame writer once either direction
	// is framed (mu), set as out once this side's output is, this side's rpc.lanes call until its answer is read (mu),
	// whether this side answered one (mu), and closed once this side's output is framed.
	lanes laneTable
	// creditReplies: methods whose answers this side writes with credit (CreditReplies).
	creditReplies map[string]bool
	lw            *laneWriter
	out           atomic.Pointer[laneWriter]
	lanesCall     string
	lanesAsked    bool
	lanesUp       chan struct{}

	// resume (resume.go): nil unless EnableResume. OnLost, when set, is told (in a goroutine of its own) that the
	// transport was lost and the peer waits to be resumed: the caller closes it, which ends a write or read still stuck
	// on it, and finds a new one. Set it before Serve.
	resume *resumeState
	OnLost func(err error)

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

// PingActivity is when this connection last received input that can explain a ping answered late (#8771): a reply to a
// call from this side, on time or late, as the other side got that call; or the middle of a frame still arriving (a
// read that continues a line begun before it and does not finish it), which a pong can be queued behind on the other
// side's single writer. Only a frame already bigger than pingFrameMiddleBytes can be such a queue: small lines are
// split across reads by the transport itself (streaming decompression, lost segments), so their pieces never count.
// A frame that has arrived whole counts no more: a pong behind it comes next, and counts as a reply. Requests and
// events the other side starts itself do not count: they keep coming when nothing sent from here gets through (a node
// with running Agents sends them, its session list of hundreds of KiB among them, every few seconds over a link whose
// other direction has stalled). Neither does a deadline error reply, which only says a relay answered its own timeout,
// nor a read that begins or finishes a line: a busy peer's small lines are cut at arbitrary read boundaries too. A
// line that cannot be decoded still counts, as nothing shows it is not a reply.
func (p *Peer) PingActivity() time.Time {
	ns := p.pingActivity.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// LastWrite is when this peer last wrote to its transport and the write went through: a line, or framed, a batch of
// frames. Zero before the first. A writer held up for good (a peer that stopped reading) shows as no progress.
func (p *Peer) LastWrite() time.Time {
	ns := p.lastWrite.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// WriteWaiters is how many messages are queued for the writer, not counting the write that currently owns it.
func (p *Peer) WriteWaiters() int64 { return p.writeWaiters.Load() }

// readActivity notes when Serve's latest raw read came in: when the lines taken from it, or the middle of a frame it
// carried, arrived.
type readActivity struct {
	r  io.Reader
	at int64 // unix nanoseconds
}

func (r *readActivity) Read(b []byte) (int, error) {
	n, err := r.r.Read(b)
	if n > 0 {
		r.at = time.Now().UnixNano()
	}
	return n, err
}

func deadlineMessage(m message) bool {
	return m.ID != "" && m.Error != nil && m.Error.Code == "" && m.Error.Message == context.DeadlineExceeded.Error()
}

// replyMessage: m answers a call (an id with a result or an error, and no method or event).
func replyMessage(m message) bool {
	return m.ID != "" && m.Method == "" && m.Event == "" && (m.Result != nil || m.Error != nil)
}

// bufferedActivity reports ping activity already read behind a deadline reply, so that it counts before the deadline
// wakes its caller: a complete later line that is a reply but not another deadline, or that cannot be decoded; or an
// unfinished line whose envelope names a result but no method or event (a line names those before params or a result).
// A partial error remains uncounted until its whole line proves that it is not another deadline. Requests and events do
// not count, whole or begun: a line begun in this read is not the middle of a frame (lineReader.middle).
func bufferedActivity(b []byte) bool {
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		var m message
		if err := json.Unmarshal(dropCR(b[:i]), &m); err != nil || replyMessage(m) && !deadlineMessage(m) {
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
		return replyMessage(m) && !deadlineMessage(m)
	}
	// A tail cut off inside params can contain a nested "result" key that belongs to a request or event, so only an
	// envelope prefix with the reply's own id and result (and no method or event) counts. A piece that starts inside a
	// params object carries no envelope id and stays unclassified rather than keeping a dead link alive.
	head := tail
	if i := bytes.Index(tail, []byte(`"params"`)); i >= 0 {
		head = tail[:i]
	}
	return bytes.Contains(head, []byte(`"id"`)) && bytes.Contains(head, []byte(`"result"`)) &&
		!bytes.Contains(head, []byte(`"method"`)) && !bytes.Contains(head, []byte(`"event"`))
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
	maximum := p.ReadLimit
	if p.Limits.FrameBytes > 0 {
		maximum = p.Limits.FrameBytes
	}
	if maximum <= 0 {
		maximum = 64 << 20
	}
	act := &readActivity{r: r}
	lr := newLineReader(act, maximum)
	// lastReply is when the latest reply, or line that could be one (it did not decode), arrived. The middle of a frame
	// counts only while that frame is still arriving (lineReader.middle); when the line is whole, activity goes back to
	// the last reply, so a large request or event the other side started cannot keep clearing misses once it is
	// through: a pong behind it would then be the next line and arrive as a reply. Framed (lanes.go), a line's frames
	// in between count the same way (frameReader.middle).
	var lastReply, middleAt int64
	lr.middle = func() { middleAt = act.at; p.pingActivity.Store(act.at) }
	var sc lineSource = lr
	framed := false
	for sc.Scan() {
		readAt := act.at
		var m message
		// A line in a buffer of its own keeps its params or result where they are (ownedline.go); any other is in the
		// reader's buffer, which the next line overwrites, so they are copied out of it.
		if !sc.Owned() || !unmarshalOwned(sc.Bytes(), &m) {
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				lastReply = readAt
				p.pingActivity.Store(lastReply)
				continue
			}
		}
		// A reply counts before any synchronous callback can block, and with no caller left too: one that comes after its
		// ping gave up still shows that the ping got there. A deadline alone does not count: only the rest of its raw
		// read, classified now, can prove later activity. The same read-ahead applies behind an ordinary request/event.
		if p.lanes != nil && m.Event == lanesMethod {
			// The other side's last line: frames follow, and what was read behind it is not lines.
			if !framed && p.answeredLanes() {
				sc, framed = p.framedInput(lr, act, maximum), true
			}
			continue
		}
		if replyMessage(m) && !deadlineMessage(m) || sc.aheadActivity() {
			lastReply = readAt
		}
		activity := lastReply
		if middleAt > activity && sc.holding() {
			// Framed, lines finish while a long one goes on arriving beside them: the activity its middle gave stands
			// until it is whole, as it does with lines, where nothing finishes in between.
			activity = middleAt
		}
		p.pingActivity.Store(activity)
		if p.lanes != nil && m.Method == lanesMethod && m.ID != "" { // the other side asks for frames
			go p.answerLanes(m)
			continue
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
			lanesAnswer := m.ID == p.lanesCall
			if lanesAnswer {
				p.lanesCall = ""
			}
			p.mu.Unlock()
			// The answer to this side's rpc.lanes is the other side's last line, whether or not its caller still
			// waits: frames follow it, and this side's own output follows suit.
			if lanesAnswer && !framed && lanesAccepted(m) {
				p.resumeGranted(resumeTokenOf(m))
				sc, framed = p.framedInput(lr, act, maximum), true
				go p.switchOutput(message{Event: lanesMethod, Params: lanesMarker})
			}
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
	out := message{ID: m.ID, opKind: writeOpReply, opName: m.Method}
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
	r := p.replyRoute(m.Method)
	var werr error
	if limit := p.writeLimit(); limit > 0 && out.Error == nil {
		if size, ok := replyResultSize(m.ID, len(out.Result)); ok && size > limit {
			werr = p.writeTooLarge(writeOpReply, m.Method, size, limit)
		}
	}
	if werr == nil {
		werr = p.write(out, r)
	}
	if isWriteBudgetErr(werr) {
		// The request must get an error, not a permanently unresolved promise.
		// If even the error cannot fit, disconnect so all pending calls fail.
		if err := p.write(message{ID: m.ID, Error: &Error{Message: werr.Error()}, opKind: writeOpReply, opName: m.Method}, r); err != nil {
			p.Close(err)
		}
	}
}

func (p *Peer) writeLimit() int {
	if p.Limits.FrameBytes > 0 {
		return p.Limits.FrameBytes
	}
	return p.WriteLimit
}

func (p *Peer) writeTooLarge(kind writeOpKind, op string, size, limit int) error {
	if p.OnWriteRejected != nil {
		p.OnWriteRejected(string(kind), op, size, limit)
	}
	if p.Limits.FrameBytes > 0 {
		return errFrameBudget
	}
	return writeLimitError{kind: kind, op: op, size: size, limit: limit}
}

func isWriteBudgetErr(err error) bool {
	return errors.Is(err, errFrameBudget) || errors.Is(err, ErrMessageTooLarge)
}

func (p *Peer) write(m message, r route) error {
	return p.writeContext(context.Background(), m, r)
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

func (p *Peer) writeContext(ctx context.Context, m message, r route) error {
	_, err := p.writeContextObserved(ctx, m, r)
	return err
}

type writeObservation struct {
	delayed      bool
	remaining    time.Duration
	writer       *laneWriter // a control frame queued behind previously written, unacknowledged events or answers
	acked, ahead uint64      // acknowledged at enqueue, and the last uncredited frame before this control frame
}

// A later cumulative acknowledgement through the traffic ahead of this ping proves the peer was draining its path.
// A silent peer, a duplicate acknowledgement or one covering the ping itself cannot extend its response budget.
func (o *writeObservation) observePingDrain(deadline time.Time) {
	if o.writer == nil {
		return
	}
	o.writer.mu.Lock()
	through, at := o.writer.ackedThrough, o.writer.ackAdvanced
	o.writer.mu.Unlock()
	if through > o.acked && through <= o.ahead && !at.After(deadline) {
		o.delayed = true
		o.remaining = min(o.remaining, max(deadline.Sub(at), 0))
	}
}

// writeContextObserved also reports whether this message had to wait behind another writer before reaching the wire,
// and how much of the caller's deadline remained when the write finished. r is where it goes once the output is framed
// (lanes.go).
func (p *Peer) writeContextObserved(ctx context.Context, m message, r route) (writeObservation, error) {
	b, err := encodeLine(m)
	if err != nil {
		return writeObservation{}, err
	}
	if limit := p.writeLimit(); limit > 0 && len(b) > limit {
		kind, op := m.writeOp()
		return writeObservation{}, p.writeTooLarge(kind, op, len(b), limit)
	}
	if lw := p.out.Load(); lw != nil {
		return p.writeFramed(ctx, lw, b, r)
	}
	// Wait for the writer, but not past the caller or the connection: the write ahead may never finish. A write
	// already under way cannot be interrupted. Try the immediate acquisition first so delayed means another write held
	// the slot when this message arrived, rather than merely that a select chose one ready case over another. Until the
	// write begins nothing went out (NotSent).
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
			return observation, NotSent(ctx.Err())
		case <-p.closed:
			p.writeWaiters.Add(-1)
			return observation, NotSent(ErrClosed)
		}
	}
	if lw := p.out.Load(); lw != nil { // framed while this line waited: it goes as frames
		<-p.wsem
		framedObservation, err := p.writeFramed(ctx, lw, b, r)
		framedObservation.delayed = framedObservation.delayed || observation.delayed
		return framedObservation, err
	}
	defer func() { <-p.wsem }()
	// Both may have become ready together and select picks at random: check again so a cancelled request never
	// reaches the wire.
	if err := ctx.Err(); err != nil {
		return observation, NotSent(err)
	}
	select {
	case <-p.closed:
		return observation, NotSent(ErrClosed)
	default:
	}
	err = p.writeLine(b)
	if deadline, ok := ctx.Deadline(); ok {
		observation.remaining = deadline.Sub(time.Now())
	}
	return observation, err
}

// writeLine writes b, a whole line, as a line (the caller holds wsem): through LineFilter, all of it or an error, then
// LineWritten.
func (p *Peer) writeLine(b []byte) error {
	line := b
	if p.LineFilter != nil {
		line = p.LineFilter(b)
	}
	n, err := p.w.Write(line)
	if err == nil && n != len(line) {
		err = io.ErrShortWrite // a writer that breaks io.Writer's rule must not pass for one that wrote the line
	}
	if err == nil {
		p.lastWrite.Store(time.Now().UnixNano())
	}
	if err == nil && p.LineWritten != nil {
		p.LineWritten(b)
	}
	return err
}

// Call sends a request and decodes the result into out (if non-nil). Params that are Appended are written straight
// into the request's line.
func (p *Peer) Call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return NotSent(err)
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
	limit := p.writeLimit()
	if a, ok := params.(Appended); ok {
		if size, ok := callSize(id, method, a); ok {
			if limit > 0 && size > limit { // before the line is made
				return p.writeTooLarge(writeOpRequest, method, size, limit)
			}
			req = message{line: callLine(id, method, a, size), opKind: writeOpRequest, opName: method}
		} else {
			params = json.RawMessage(a.Append(make([]byte, 0, a.Size))) // the encoder writes the envelope
		}
	}
	if req.line == nil {
		if raw, ok := params.(Marshaled); ok && len(raw) > 0 {
			if size, ok := callRawSize(id, method, len(raw)); ok && limit > 0 && size > limit {
				return p.writeTooLarge(writeOpRequest, method, size, limit)
			}
		}
		raw, err := marshal(params)
		if err != nil {
			return err
		}
		if size, ok := callRawSize(id, method, len(raw)); ok && limit > 0 && size > limit {
			return p.writeTooLarge(writeOpRequest, method, size, limit)
		}
		req = message{ID: id, Method: method, Params: raw}
	}
	writeObservation, err := p.writeContextObserved(ctx, req, p.lanes.request(method))
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
			_ = p.write(message{Event: cancelEvent, Params: params}, controlRoute)
		}
		if deadline, ok := ctx.Deadline(); ok && method == "ping" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			writeObservation.observePingDrain(deadline)
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
	return p.notifyRoute(ctx, event, params, p.lanes.eventRoute(event))
}

func (p *Peer) notifyRoute(ctx context.Context, event string, params any, r route) error {
	limit := p.writeLimit()
	if raw, ok := params.(Marshaled); ok {
		if size, ok := marshaledLineSize(event, raw); ok {
			if limit > 0 && size > limit {
				return p.writeTooLarge(writeOpEvent, event, size, limit)
			}
			return p.writeContext(ctx, message{line: marshaledLineWithSize(event, raw, size), opKind: writeOpEvent, opName: event}, r)
		}
		params = json.RawMessage(raw)
	}
	// Bytes already encoded go into the envelope as they are: its encoder checks and compacts them as this one would,
	// with the same errors, so the line is the same without encoding them twice (a computer's session list each time
	// it changes). An empty one is what json.Marshal says it is (an error).
	if raw, ok := params.(json.RawMessage); ok && len(raw) > 0 {
		return p.writeContext(ctx, message{Event: event, Params: raw}, r)
	}
	raw, err := marshal(params)
	if err != nil {
		return err
	}
	if size, ok := marshaledLineSize(event, Marshaled(raw)); ok && limit > 0 && size > limit {
		return p.writeTooLarge(writeOpEvent, event, size, limit)
	}
	return p.writeContext(ctx, message{Event: event, Params: raw}, r)
}

func (p *Peer) Close(err error) {
	p.closeIf(err, nil)
}

// closeIf atomically checks cond (when non-nil) and closes p while holding p.mu, so conditional closes such as a
// resume-window timer cannot race with Resume or AcceptResume installing a new transport.
func (p *Peer) closeIf(err error, cond func() bool) bool {
	var inflight []string
	var onClose func(error, []string)
	p.mu.Lock() // taken before the waiters wake: they drop their entries as they return
	select {
	case <-p.closed:
		p.mu.Unlock()
		return false
	default:
	}
	if cond != nil && !cond() {
		p.mu.Unlock()
		return false
	}
	if p.resume != nil {
		p.resume.timerGen++
		if p.resume.timer != nil {
			p.resume.timer.Stop()
			p.resume.timer = nil
		}
	}
	if p.OnClose != nil {
		onClose = p.OnClose
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
	if onClose != nil {
		sort.Strings(inflight)
		onClose(err, inflight)
	}
	return true
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
