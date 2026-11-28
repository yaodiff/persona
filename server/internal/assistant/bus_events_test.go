package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// goroutinesIn counts the goroutines whose stack (or whose starter) is in the function named.
func goroutinesIn(fn string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for _, g := range strings.Split(string(buf), "\n\n") {
		if strings.Contains(g, fn) {
			count++
		}
	}
	return count
}

const queueEventFn = "(*BusHub).queueEvent"

// eventsWaiting is how many of the hub's events wait in b's line; takeWaiting takes the oldest out.
func eventsWaiting(b *BusHub) int { return b.events.len() }

func takeWaiting(t *testing.T, b *BusHub) busEvent {
	t.Helper()
	ev, ok := b.events.take()
	if !ok {
		t.Fatal("no event waits")
	}
	b.pending.Add(-1)
	return ev
}

// numbered is params for an event about conversation server/c1 that also says it is the nth queued (the handlers do
// not read n).
func numbered(n int) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"machine_id": "server", "session_id": "c1", "n": n, "at": time.Unix(int64(n), 0).UTC()})
	return raw
}

func numberOf(t *testing.T, ev busEvent) int {
	t.Helper()
	var p struct {
		N *int `json:"n"`
	}
	if err := json.Unmarshal(ev.params, &p); err != nil || p.N == nil {
		t.Fatalf("%s %s: no number (%v)", ev.name, ev.params, err)
	}
	return *p.N
}

// heldBus is a bus whose events are taken by takeEvents as in Serve, with no connection: the first event taken is
// held (held is closed then) until release, and every event taken is noted in the order it was.
type heldBus struct {
	b       *BusHub
	held    chan struct{}
	release func()
	cancel  context.CancelFunc
	ended   chan struct{} // closed once takeEvents returned

	mu    sync.Mutex
	taken []busEvent
	seen  chan struct{} // one per event taken after the first
}

func newHeldBus(t *testing.T) *heldBus {
	t.Helper()
	return newHeldBusFor(t, newTestService(t, newFakeHub()))
}

// newHeldBusFor is newHeldBus whose events s handles.
func newHeldBusFor(t *testing.T, s *Service) *heldBus {
	t.Helper()
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	b.setHello(Hello{}) // the hub's answer is in: events are not held back for it (waitHello)
	gate := make(chan struct{})
	h := &heldBus{b: b, held: make(chan struct{}), ended: make(chan struct{}), seen: make(chan struct{}, 4*eventsMost)}
	var once sync.Once
	h.release = func() { once.Do(func() { close(gate) }) }
	first := true
	b.taking = func(ev busEvent) {
		h.mu.Lock()
		h.taken = append(h.taken, ev)
		h.mu.Unlock()
		if first {
			first = false
			close(h.held)
			<-gate
			return
		}
		h.seen <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	b.mu.Lock()
	b.svc, b.ctx = s, ctx
	b.mu.Unlock()
	go func() {
		defer close(h.ended)
		b.takeEvents(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		h.release()
		<-h.ended
	})
	return h
}

// hold queues one event and waits until the consumer holds it: from then on nothing is taken until release.
func (h *heldBus) hold(t *testing.T) {
	t.Helper()
	h.b.queueEvent("managers", json.RawMessage(`{}`))
	select {
	case <-h.held:
	case <-time.After(10 * time.Second):
		t.Fatal("the first event was not taken")
	}
}

// wait waits until n more events were taken after the held one.
func (h *heldBus) wait(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-h.seen:
		case <-deadline:
			t.Fatalf("%d of %d events taken", i, n)
		}
	}
}

// fill queues n events that nothing reads ("managers": the bus's roster).
func fill(b *BusHub, n int) {
	for i := 0; i < n; i++ {
		b.queueEvent("managers", json.RawMessage(`{}`))
	}
}

// stateEvents are what the hub says of one conversation's state, each of which must be taken itself: its turn
// started, the owner looked at it (the hub's word, and an app's).
var stateEvents = []string{assistantwire.EventTurnStarted, assistantwire.EventSessionSeen, assistantwire.EventSeen}

// A full line starts no goroutine per event: what must each be taken waits in the line itself (#15906; each used to
// start a goroutine that blocked on the channel, with nothing bounding how many).
func TestBusFullLineStartsNoGoroutines(t *testing.T) {
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	fill(b, eventsRoom)
	const more = 300
	for i := 0; i < more; i++ {
		b.queueEvent(stateEvents[i%len(stateEvents)], numbered(i))
	}
	if n := goroutinesIn(queueEventFn); n != 0 {
		t.Fatalf("%d goroutines started by queueEvent, want none", n)
	}
	if n := eventsWaiting(b); n != eventsRoom+more {
		t.Fatalf("%d events waiting, want %d", n, eventsRoom+more)
	}
	if n := b.pending.Load(); n != eventsRoom+more {
		t.Fatalf("%d events counted as not taken, want %d", n, eventsRoom+more)
	}
}

func TestBusInlineViewPageDropsOnlyUnderBacklog(t *testing.T) {
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	b.queueEvent(assistantwire.EventInlineViewPage, numbered(1))
	if got := takeWaiting(t, b); got.name != assistantwire.EventInlineViewPage {
		t.Fatalf("queued event = %s, want inline view page", got.name)
	}
	fill(b, eventsRoom)
	b.queueEvent(assistantwire.EventInlineViewPage, numbered(2))
	if got := eventsWaiting(b); got != eventsRoom {
		t.Fatalf("%d events waiting, want %d after dropping the inline view page", got, eventsRoom)
	}
}

// What the hub says of one conversation's state is handled in the order it came, however long the line was, and
// none of it is lost (#15906: past 1024 waiting, the events reached the line in the order the scheduler ran their
// goroutines).
func TestBusLongLineKeepsArrivalOrder(t *testing.T) {
	h := newHeldBus(t)
	h.hold(t)
	fill(h.b, eventsRoom) // the line is full: 1024 wait behind the one held
	const more = 300
	for i := 0; i < more; i++ {
		h.b.queueEvent(stateEvents[i%len(stateEvents)], numbered(i))
	}
	h.release()
	h.wait(t, eventsRoom+more)
	h.mu.Lock()
	taken := append([]busEvent(nil), h.taken...)
	h.mu.Unlock()
	var got []int
	for _, ev := range taken {
		if ev.name != "managers" {
			if want := stateEvents[len(got)%len(stateEvents)]; ev.name != want {
				t.Fatalf("event %d taken is %s, want %s", len(got), ev.name, want)
			}
			got = append(got, numberOf(t, ev))
		}
	}
	if len(got) != more {
		t.Fatalf("%d of the conversation's %d events taken", len(got), more)
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("the conversation's events were taken out of order: number %d came %dth (%v…)", n, i, got[:min(len(got), i+8)])
		}
	}
	h.b.Settle()
	if n := h.b.pending.Load(); n != 0 {
		t.Fatalf("%d events counted as not taken after all were", n)
	}
}

// While the line is long, an event of which only the newest matters (the mutes, a request to report everything)
// takes the place of the one of its key that waits, and is handled where it came, not where the older one waited;
// under eventsRoom every event waits itself. A machine's list and the hello again say the whole of something too,
// but their handlers go by what changed since the one before: each of those waits itself, however long the line.
func TestBusLongLineKeepsTheNewestWhole(t *testing.T) {
	list := func(machine string, n int) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"machine_id": machine, "sessions": []any{}, "n": n})
		return raw
	}
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	b.queueEvent(assistantwire.EventMutes, numbered(-2))
	b.queueEvent(assistantwire.EventMutes, numbered(-1))
	if n := eventsWaiting(b); n != 2 {
		t.Fatalf("a short line: %d events waiting, want both mutes", n)
	}
	fill(b, eventsRoom-2)
	// What comes now, in order: ten requests to report everything with a turn's start in the middle, mac's list
	// three times, the hello twice, the mutes five times.
	n := 0
	queue := func(event string, params func(int) json.RawMessage, times int) {
		for i := 0; i < times; i++ {
			b.queueEvent(event, params(n))
			n++
		}
	}
	queue(assistantwire.EventResync, numbered, 5)
	queue(assistantwire.EventTurnStarted, numbered, 1) // number 5
	queue(assistantwire.EventResync, numbered, 5)
	queue(assistantwire.EventSessionsChanged, func(n int) json.RawMessage { return list("mac", n) }, 3)
	queue(assistantwire.EventUpdated, numbered, 2)
	queue(assistantwire.EventMutes, numbered, 5)
	if n := goroutinesIn(queueEventFn); n != 0 {
		t.Fatalf("%d goroutines started by queueEvent, want none", n)
	}
	for i := 0; i < eventsRoom; i++ {
		takeWaiting(t, b)
	}
	want := []struct {
		name string
		n    int
	}{
		{assistantwire.EventTurnStarted, 5},
		{assistantwire.EventResync, 10}, // the newest, after the turn's start that came before it
		{assistantwire.EventSessionsChanged, 11},
		{assistantwire.EventSessionsChanged, 12},
		{assistantwire.EventSessionsChanged, 13},
		{assistantwire.EventUpdated, 14},
		{assistantwire.EventUpdated, 15},
		{assistantwire.EventMutes, 20},
	}
	if n := eventsWaiting(b); n != len(want) {
		t.Fatalf("%d events waiting past the first %d, want %d", n, eventsRoom, len(want))
	}
	for i, w := range want {
		ev := takeWaiting(t, b)
		if ev.name != w.name || numberOf(t, ev) != w.n {
			t.Fatalf("event %d: %s number %d, want %s number %d", i, ev.name, numberOf(t, ev), w.name, w.n)
		}
	}
	if n := b.pending.Load(); n != 0 {
		t.Fatalf("%d events counted as not taken after all were", n)
	}
}

// Every hello the hub sends again (manager.updated) is handled, however long the line: the computers one lists and
// the one before did not are the ones just confirmed, which get their keys (trustChanged). A computer the first
// hello takes off the list and the second confirms again got none while the second took the first's place in line:
// the list before and after was the same.
func TestBusLongLineHandlesEveryHello(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["mac"] = nil
	f.devices = []Device{{ID: "server", Online: true, Reachable: true}, {ID: "mac", Online: true, Reachable: true}}
	st := &trustStore{memStore: &memStore{}}
	h := newHeldBusFor(t, newServiceTrust(t, f, st, st))
	hello := func(machines ...string) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"assistant": map[string]any{"machine_id": "server", "machines": machines}})
		return raw
	}
	var kept Hello
	if err := json.Unmarshal(hello("server", "mac"), &kept); err != nil {
		t.Fatal(err)
	}
	h.b.setHello(kept) // the hello in effect lists both computers
	h.hold(t)
	fill(h.b, eventsRoom)
	h.b.queueEvent(assistantwire.EventUpdated, hello("server"))        // the mac is no longer confirmed
	h.b.queueEvent(assistantwire.EventUpdated, hello("server", "mac")) // and is again
	f.mu.Lock()
	clear(f.managed)
	f.mu.Unlock()
	h.release()
	h.wait(t, eventsRoom+2)
	h.b.Settle()
	if got := h.b.helloNow().Assistant.Machines; len(got) != 2 {
		t.Fatalf("the hello kept lists %v, want both computers", got)
	}
	// The keys go on a goroutine of their own (a node's answer can take seconds).
	waitFor(t, "the computer confirmed again gets the managed launchers", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, mac := f.managed["mac"]
		return mac
	})
}

// Every list of a machine's is handed over, in order, however long the line: what follows the lists goes by
// consecutive ones, so a list that shows a conversation working, followed by one that shows it stopped, is not a
// repeat of it (only the stopped one was handed over while it took the working one's place in line).
func TestBusLongLineHandsEveryListOver(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	var mu sync.Mutex
	var seen []string
	s.sessionsSeen = func(d DeviceSessions) {
		if d.MachineID != "mac" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range d.Sessions {
			seen = append(seen, c.SessionID+" "+c.Status)
		}
	}
	h := newHeldBusFor(t, s)
	list := func(status string) json.RawMessage {
		raw, err := json.Marshal(DeviceSessions{MachineID: "mac", Sessions: []proto.Session{{SessionID: "c1", Status: status}}})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	h.hold(t)
	fill(h.b, eventsRoom)
	h.b.queueEvent(assistantwire.EventSessionsChanged, list(proto.StatusWorking))
	h.b.queueEvent(assistantwire.EventTurnEnded, json.RawMessage(`{"machine_id":"mac","session_id":"c1"}`))
	h.b.queueEvent(assistantwire.EventSessionsChanged, list(proto.StatusDone))
	h.release()
	h.wait(t, eventsRoom+3)
	h.b.Settle()
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"c1 " + proto.StatusWorking, "c1 " + proto.StatusDone}; !slices.Equal(seen, want) {
		t.Fatalf("the lists handed over: %q, want %q", seen, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var order []string
	for _, ev := range h.taken[len(h.taken)-3:] {
		order = append(order, ev.name)
	}
	if want := []string{assistantwire.EventSessionsChanged, assistantwire.EventTurnEnded, assistantwire.EventSessionsChanged}; !slices.Equal(order, want) {
		t.Fatalf("taken in the order %q, want %q", order, want)
	}
}

// Once what takes the events has ended (Serve's ctx), nothing is left waiting on the line, and queueing still does
// not block (#15906: the goroutines that waited for room stayed blocked for the life of the process).
func TestBusNothingWaitsAfterTheConsumerEnds(t *testing.T) {
	h := newHeldBus(t)
	h.hold(t)
	fill(h.b, eventsRoom)
	for i := 0; i < 200; i++ {
		h.b.queueEvent(stateEvents[i%len(stateEvents)], numbered(i))
	}
	h.cancel()
	h.release()
	select {
	case <-h.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("takeEvents did not return when its ctx ended")
	}
	for i := 0; i < 2*eventsMost; i++ { // more than the line keeps: none of them waits for room
		h.b.queueEvent(stateEvents[i%len(stateEvents)], numbered(i))
	}
	if n := goroutinesIn(queueEventFn); n != 0 {
		t.Fatalf("%d goroutines still waiting in queueEvent, want none", n)
	}
	if n := eventsWaiting(h.b); n > eventsMost+1 {
		t.Fatalf("%d events waiting, want at most %d", n, eventsMost+1)
	}
}

// The line keeps eventsMost events and no more: the ones that came then are counted in a note that waits where
// they would have, and what comes once there is room again waits behind the note.
func TestBusFullLineNotesWhatItDidNotKeep(t *testing.T) {
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	for i := 0; i < eventsMost+50; i++ {
		b.queueEvent(stateEvents[i%len(stateEvents)], numbered(i))
	}
	if n := eventsWaiting(b); n != eventsMost+1 {
		t.Fatalf("%d events waiting, want %d and the note", n, eventsMost)
	}
	if n := b.pending.Load(); n != eventsMost+1 {
		t.Fatalf("%d events counted as not taken, want %d", n, eventsMost+1)
	}
	for i := 0; i < 10; i++ {
		if ev := takeWaiting(t, b); numberOf(t, ev) != i {
			t.Fatalf("event %d taken is number %d", i, numberOf(t, ev))
		}
	}
	b.queueEvent(assistantwire.EventTurnStarted, numbered(-1)) // room again
	for i := 10; i < eventsMost; i++ {
		if ev := takeWaiting(t, b); numberOf(t, ev) != i {
			t.Fatalf("event %d taken is number %d", i, numberOf(t, ev))
		}
	}
	if note := takeWaiting(t, b); note.lost != 50 || note.name != "" {
		t.Fatalf("after the %d kept: %+v, want the note of 50 not kept", eventsMost, note)
	}
	if ev := takeWaiting(t, b); numberOf(t, ev) != -1 {
		t.Fatalf("behind the note: number %d", numberOf(t, ev))
	}
	if n := eventsWaiting(b); n != 0 {
		t.Fatalf("%d events still waiting", n)
	}
}

// Events the full line did not keep are made up for the way a connection that was away is: once everything before
// them is taken, the connection to the hub is dropped and the next one says hello again.
func TestBusEventsNotKeptStartANewConnection(t *testing.T) {
	hellos := make(chan struct{}, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == assistantwire.MethodHello {
				hellos <- struct{}{}
			}
			return map[string]any{}, nil
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()
	hello := func(what string) {
		t.Helper()
		select {
		case <-hellos:
		case <-time.After(10 * time.Second):
			t.Fatalf("no hello: %s", what)
		}
	}

	var logMu sync.Mutex
	var logged []string
	b := NewBusHub(BusOptions{Hub: srv.URL, Token: "jb_x", Backoff: 10 * time.Millisecond, Logf: func(format string, args ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}})
	held, gate := make(chan struct{}), make(chan struct{})
	first := true
	b.taking = func(busEvent) {
		if first {
			first = false
			close(held)
			<-gate
		}
	}
	st := &memStore{}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()
	hello("the first connection")
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	b.TellLocal("managers", json.RawMessage(`{}`))
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the first event was not taken")
	}
	fill(b, eventsMost+3) // three more than the line keeps
	select {
	case <-hellos:
		t.Fatal("a new connection before the events in line were taken")
	default:
	}
	close(gate)
	hello("after the events that were not kept")
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	b.Settle()
	logMu.Lock()
	defer logMu.Unlock()
	if !strings.Contains(strings.Join(logged, "\n"), "3 event(s) of the hub's were not kept") {
		t.Fatalf("the log does not say what was not kept:\n%s", strings.Join(logged, "\n"))
	}
}

// What waits behind the note of events not kept is not taken before the new connection's hello answer is kept: one
// of the events lost may have been the hub's settings (manager.updated), and until the hello is kept again the ones
// it has are older than those the later events were sent under.
func TestBusEventsBehindALossWaitForTheNewHello(t *testing.T) {
	asked := make(chan struct{}, 8)
	second := make(chan struct{}) // closed: the hub answers the second connection's hello
	var hellos atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
			if method == assistantwire.MethodHello {
				n := hellos.Add(1)
				asked <- struct{}{}
				if n == 2 {
					select {
					case <-second:
					case <-ctx.Done():
					}
				}
			}
			return map[string]any{}, nil
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()
	hello := func(what string) {
		t.Helper()
		select {
		case <-asked:
		case <-time.After(10 * time.Second):
			t.Fatalf("no hello: %s", what)
		}
	}

	b := NewBusHub(BusOptions{Hub: srv.URL, Token: "jb_x", Backoff: 10 * time.Millisecond, Logf: testLogf(t)})
	behindParams := json.RawMessage(`{"behind":true}`)
	held, gate := make(chan struct{}), make(chan struct{})
	var answered atomic.Bool
	behind := make(chan bool, 1) // the event behind the note was taken: whether the second hello was answered by then
	first := true
	b.taking = func(ev busEvent) {
		switch {
		case first:
			first = false
			close(held)
			<-gate
		case ev.lost > 0:
			b.queueEvent("managers", behindParams) // the line is empty but for the note being taken: this waits behind it
		case string(ev.params) == string(behindParams):
			behind <- answered.Load()
		}
	}
	st := &memStore{}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()
	defer close(second)
	hello("the first connection")
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}

	b.TellLocal("managers", json.RawMessage(`{}`))
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the first event was not taken")
	}
	fill(b, eventsMost+3) // three more than the line keeps
	close(gate)
	hello("after the events that were not kept") // asked, not answered yet
	select {
	case <-behind:
		t.Fatal("the event behind the note was taken before the new connection's hello was answered")
	default:
	}
	answered.Store(true)
	second <- struct{}{}
	select {
	case after := <-behind:
		if !after {
			t.Fatal("the event behind the note was taken before the new connection's hello was answered")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the event behind the note was never taken")
	}
	if err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	b.Settle()
}

// With no hub to say hello again, the line goes on after helloWait with the settings it has, and waits once for a
// connection, not once for every note of events lost on it: a line that keeps filling is not held helloWait each time.
func TestBusEventsBehindALossGoOnWithoutAHub(t *testing.T) {
	was := helloWait
	t.Cleanup(func() { helloWait = was }) // after the consumer below has ended
	helloWait = 20 * time.Millisecond
	var logMu sync.Mutex
	var logged []string
	b := NewBusHub(BusOptions{Logf: func(format string, args ...any) {
		logMu.Lock()
		logged = append(logged, fmt.Sprintf(format, args...))
		logMu.Unlock()
	}})
	b.setHello(Hello{})
	holdParams := json.RawMessage(`{"hold":true}`)
	held, gate := make(chan struct{}), make(chan struct{})
	b.taking = func(ev busEvent) {
		if string(ev.params) == string(holdParams) {
			held <- struct{}{}
			<-gate
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.svc, b.ctx = newTestService(t, newFakeHub()), ctx
	b.mu.Unlock()
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		b.takeEvents(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-ended
	})
	for round := 1; round <= 2; round++ {
		b.queueEvent("managers", holdParams)
		select {
		case <-held:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: the event to hold was not taken", round)
		}
		fill(b, eventsMost+2) // two more than the line keeps
		gate <- struct{}{}
		b.Settle()
	}
	logMu.Lock()
	defer logMu.Unlock()
	all := strings.Join(logged, "\n")
	if n := strings.Count(all, "2 event(s) of the hub's were not kept"); n != 2 {
		t.Fatalf("%d notes of events not kept in the log, want 2:\n%s", n, all)
	}
	if n := strings.Count(all, "no new hello from the hub"); n != 1 {
		t.Fatalf("the line waited for a new hello %d times, want once for the connection:\n%s", n, all)
	}
}
