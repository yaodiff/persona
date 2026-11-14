package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// memOutbox is OutboxStore in memory.
type memOutbox struct {
	mu    sync.Mutex
	seq   int64
	items []db.OutboxItem
}

func (m *memOutbox) OutboxAdd(_ context.Context, it db.OutboxItem, keep int, staleBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slices.ContainsFunc(m.items, func(x db.OutboxItem) bool { return x.ID == it.ID }) {
		return nil
	}
	m.seq++
	it.Seq = m.seq
	if it.CreatedAt.IsZero() {
		it.CreatedAt = time.Now()
	}
	m.items = append(m.items, it)
	m.items = slices.DeleteFunc(m.items, func(x db.OutboxItem) bool { return x.Kind == outboxBroadcast && x.CreatedAt.Before(staleBefore) })
	n := 0
	for i := len(m.items) - 1; i >= 0; i-- {
		if m.items[i].Kind == it.Kind {
			if n++; n > keep {
				m.items = slices.Delete(m.items, i, i+1)
			}
		}
	}
	return nil
}

func (m *memOutbox) OutboxCount(context.Context) (int, error) { return m.len(), nil }

func (m *memOutbox) OutboxItems(_ context.Context, limit int) ([]db.OutboxItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.items[:min(limit, len(m.items))]), nil
}

func (m *memOutbox) OutboxDone(_ context.Context, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = slices.DeleteFunc(m.items, func(x db.OutboxItem) bool { return slices.Contains(ids, x.ID) })
	return nil
}

func (m *memOutbox) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.items)
}

// outboxHub is a hub on the bus that can be away (dials refused), and records what reaches it, in order.
type outboxHub struct {
	srv    *httptest.Server
	up     atomic.Bool
	legacy bool // answers manager.outbox unknown_method, like a hub from before it
	failN  atomic.Int32
	freeze bool // guarded by mu: notify.request gets no answer

	mu   sync.Mutex
	got  []string // "push <id>", "broadcast <type>", in the order taken
	conn *websocket.Conn
}

func newOutboxHub(t *testing.T) *outboxHub {
	h := &outboxHub{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.up.Load() {
			http.Error(w, "away", http.StatusServiceUnavailable)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		h.mu.Lock()
		h.conn = ws
		h.mu.Unlock()
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodHello, assistantwire.MethodSnapshot:
				return map[string]any{}, nil
			case assistantwire.MethodNotify:
				h.mu.Lock()
				frozen := h.freeze
				h.mu.Unlock()
				if frozen {
					time.Sleep(time.Second) // longer than the caller waits
				}
				var n NotifyRequest
				_ = json.Unmarshal(params, &n)
				h.take("push " + n.Notification.ID)
				return map[string]bool{"ok": true}, nil
			case assistantwire.MethodOutbox:
				if h.legacy {
					return nil, &rpc.Error{Code: bus.CodeUnknownMethod, Message: "unknown method " + method}
				}
				var o assistantwire.Outbox
				_ = json.Unmarshal(params, &o)
				res := assistantwire.OutboxResult{Done: []string{}}
				for _, it := range o.Items {
					h.take(itemString(it))
					res.Done = append(res.Done, it.ID)
				}
				if h.failN.Add(-1) >= 0 {
					return nil, &rpc.Error{Code: "busy", Message: "taken, but the answer is lost"}
				}
				return res, nil
			}
			return nil, &rpc.Error{Code: "not_found", Message: method}
		}
		p.OnEvent = func(event string, params json.RawMessage) {
			if event == assistantwire.EventBroadcast {
				var b Broadcast
				_ = json.Unmarshal(params, &b)
				h.take(itemString(assistantwire.OutboxItem{Message: b.Message}))
			}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func itemString(it assistantwire.OutboxItem) string {
	if it.Notification != nil {
		return "push " + it.Notification.ID
	}
	var m struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(it.Message, &m)
	return "broadcast " + m.Type
}

func (h *outboxHub) take(s string) {
	h.mu.Lock()
	h.got = append(h.got, s)
	h.mu.Unlock()
}

func (h *outboxHub) taken() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.got)
}

// drop cuts the connection and keeps dials refused: the hub is away.
func (h *outboxHub) drop() {
	h.up.Store(false)
	h.mu.Lock()
	c := h.conn
	h.mu.Unlock()
	if c != nil {
		_ = c.CloseNow()
	}
}

func (h *outboxHub) waitFor(t *testing.T, want []string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !slices.Equal(h.taken(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("the hub took %q, want %q", h.taken(), want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func startOutboxManager(t *testing.T, h *outboxHub, out OutboxStore, ext ...Extensions) *BusHub {
	t.Helper()
	o := BusOptions{Hub: h.srv.URL, Token: "jb_x", Logf: testLogf(t), Backoff: 20 * time.Millisecond, Outbox: out}
	if len(ext) > 0 {
		o.Extensions = ext[0]
	}
	b := NewBusHub(o)
	b.client.MaxBackoff = 100 * time.Millisecond
	svc := New(Options{Hub: b, Store: func() Store { return &memStore{} }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	return b
}

func push(id string) NotifyRequest {
	return NotifyRequest{Notification: Notification{ID: id, Kind: NotifyPush, Title: id}}
}

func live(kind string) Broadcast {
	raw, _ := json.Marshal(map[string]string{"type": kind})
	return Broadcast{Message: raw}
}

// What the manager makes while the hub is away is kept and reaches the hub once it is back, in the order made; then
// it goes live again.
func TestTheOutboxKeepsWhatTheHubMissed(t *testing.T) {
	h := newOutboxHub(t)
	out := &memOutbox{}
	b := startOutboxManager(t, h, out)
	ctx := context.Background()
	if _, err := b.NotifyRequest(ctx, push("ntf_1")); err != nil {
		t.Fatalf("a push while the hub is away: %v", err)
	}
	b.Broadcast(ctx, live("todos"))
	if _, err := b.NotifyRequest(ctx, push("ntf_2")); err != nil {
		t.Fatal(err)
	}
	if out.len() != 3 {
		t.Fatalf("kept %d", out.len())
	}
	h.up.Store(true)
	h.waitFor(t, []string{"push ntf_1", "broadcast todos", "push ntf_2"})
	deadline := time.Now().Add(5 * time.Second)
	for out.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if out.len() != 0 {
		t.Fatalf("still kept after the hub took them: %d", out.len())
	}
	if _, err := b.NotifyRequest(ctx, push("ntf_3")); err != nil {
		t.Fatal(err)
	}
	h.waitFor(t, []string{"push ntf_1", "broadcast todos", "push ntf_2", "push ntf_3"})

	// Away again: kept again, and handed over when it is back.
	h.drop()
	deadline = time.Now().Add(5 * time.Second)
	for b.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := b.NotifyRequest(ctx, push("ntf_4")); err != nil {
		t.Fatal(err)
	}
	h.up.Store(true)
	h.waitFor(t, []string{"push ntf_1", "broadcast todos", "push ntf_2", "push ntf_3", "push ntf_4"})
}

// A hub from before manager.outbox gets the items one by one, as notify.request and ws.broadcast.
func TestTheOutboxToAnOlderHub(t *testing.T) {
	h := newOutboxHub(t)
	h.legacy = true
	b := startOutboxManager(t, h, &memOutbox{})
	ctx := context.Background()
	if _, err := b.NotifyRequest(ctx, push("ntf_1")); err != nil {
		t.Fatal(err)
	}
	b.Broadcast(ctx, live("assistant_feed"))
	h.up.Store(true)
	h.waitFor(t, []string{"push ntf_1", "broadcast assistant_feed"})
}

// Items too old to be news are not handed over; ones whose answer was lost are sent again (the hub takes each once).
func TestTheOutboxDropsStaleItemsAndRetries(t *testing.T) {
	retry := outboxRetry
	outboxRetry = 50 * time.Millisecond
	t.Cleanup(func() { outboxRetry = retry })
	h := newOutboxHub(t)
	h.failN.Store(1)
	out := &memOutbox{}
	old, _ := json.Marshal(map[string]string{"type": "todos"})
	_ = out.OutboxAdd(context.Background(), db.OutboxItem{ID: "bc_old", Kind: outboxBroadcast, Body: old,
		CreatedAt: time.Now().Add(-2 * broadcastKeep)}, broadcastsKept, time.Time{})
	n, _ := json.Marshal(push("ntf_old").Notification)
	_ = out.OutboxAdd(context.Background(), db.OutboxItem{ID: "ntf_old", Kind: outboxPush, Body: n,
		CreatedAt: time.Now().Add(-2 * pushKeep)}, pushesKept, time.Time{})
	nf, _ := json.Marshal(push("ntf_1").Notification)
	_ = out.OutboxAdd(context.Background(), db.OutboxItem{ID: "ntf_1", Kind: outboxPush, Body: nf}, pushesKept, time.Time{})
	startOutboxManager(t, h, out) // a manager started again with what an earlier run kept
	h.up.Store(true)
	h.waitFor(t, []string{"push ntf_1", "push ntf_1"}) // sent again after the lost answer (the real hub delivers it once)
	deadline := time.Now().Add(5 * time.Second)
	for out.len() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if out.len() != 0 {
		t.Fatalf("still kept: %d", out.len())
	}
}

// slowReport is extensions whose report on a new connection takes until release: the outbox waits for it.
type slowReport struct {
	Extensions
	release chan struct{}
}

func (s slowReport) Hello(json.RawMessage) {}
func (s slowReport) BusUp()                { <-s.release }
func (s slowReport) Event(string, json.RawMessage) bool {
	return false
}
func (s slowReport) AppCall(context.Context, assistantwire.AppCall) (json.RawMessage, bool, error) {
	return nil, false, nil
}

// The outbox goes after the manager reported its state on the new connection (its cards, its extensions): a card's
// push the hub got before the card would be refused.
func TestTheOutboxWaitsForTheReport(t *testing.T) {
	h := newOutboxHub(t)
	rep := slowReport{release: make(chan struct{})}
	b := startOutboxManager(t, h, &memOutbox{}, rep)
	if _, err := b.NotifyRequest(context.Background(), push("ntf_1")); err != nil {
		t.Fatal(err)
	}
	h.up.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for !b.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if got := h.taken(); len(got) != 0 {
		t.Fatalf("handed over before the report: %q", got)
	}
	close(rep.release)
	h.waitFor(t, []string{"push ntf_1"})
}

// A push whose call did not get an answer (a hub that stopped answering) is kept, not lost.
func TestAPushThatTimedOutIsKept(t *testing.T) {
	h := newOutboxHub(t)
	out := &memOutbox{}
	b := startOutboxManager(t, h, out)
	h.up.Store(true)
	deadline := time.Now().Add(5 * time.Second)
	for !b.Connected() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.mu.Lock()
	h.freeze = true
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := b.NotifyRequest(ctx, push("ntf_slow")); err != nil {
		t.Fatalf("a push the hub did not answer: %v", err)
	}
	if out.len() != 1 {
		t.Fatalf("kept %d", out.len())
	}
}

// What the outbox keeps: not a coded answer of the hub about the item (it would get the same), but a connection the
// hub no longer takes calls on, a timeout or a dropped bus; and never a body that is not JSON.
func TestWhatTheOutboxKeeps(t *testing.T) {
	if !answered(&rpc.Error{Code: "no_device", Message: "x"}) {
		t.Error("a coded refusal kept")
	}
	if answered(&rpc.Error{Code: bus.CodeStaleConnection, Message: "x"}) {
		t.Error("a stale connection's refusal taken for the hub's answer")
	}
	if answered(context.DeadlineExceeded) || answered(bus.ErrOffline) || answered(rpc.ErrClosed) {
		t.Error("an unanswered send taken for an answer")
	}
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "jb_x", Logf: testLogf(t), Outbox: &memOutbox{}})
	if b.keep(context.Background(), "bc_bad", outboxBroadcast, []byte(`{"type":`)) {
		t.Error("a body that is not JSON kept")
	}
	if !b.keep(context.Background(), "bc_ok", outboxBroadcast, []byte(`{"type":"todos"}`)) {
		t.Error("a JSON body not kept")
	}
}
