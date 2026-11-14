package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

// needsYouTold are the counts the inbox told the hub (inbox.needs_you), in order.
func needsYouTold(t *testing.T, f *fakeHub) []int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int
	for _, e := range f.told {
		if e.event != assistantwire.EventInboxNeedsYou {
			continue
		}
		var p assistantwire.InboxNeedsYou
		if err := json.Unmarshal(e.params, &p); err != nil {
			t.Fatalf("%s: %v", e.params, err)
		}
		out = append(out, p.Count)
	}
	return out
}

// waitNeedsYou waits until the inbox told the hub want counts (it tells on its own goroutine), and returns them.
func waitNeedsYou(t *testing.T, f *fakeHub, want int) []int {
	t.Helper()
	var got []int
	waitFor(t, "the inbox count to reach the hub", func() bool {
		got = needsYouTold(t, f)
		return len(got) >= want
	})
	return got
}

// waitNeedsYouIdle waits for the one teller to finish, including a queued change.
func waitNeedsYouIdle(t *testing.T, s *Service) {
	t.Helper()
	waitFor(t, "the inbox count teller to finish", func() bool {
		s.inbox.needsYouMu.Lock()
		defer s.inbox.needsYouMu.Unlock()
		return !s.inbox.needsYou.telling
	})
}

// setHub makes h s's hub; tests swap the hub with it, never with s.hub = h. The "Needs you" teller reads s.hub on a
// goroutine of its own, and a test's service may still run the one its first inbox read started (or any later
// change's): the teller reads the hub only under needsYouMu, and setHub swaps it under that lock. It does not wait for
// the teller: in a synctest bubble a wait's sleep moves the clock, and the bubble's timers would fire while the hub
// changes.
func setHub(s *Service, h Hub) {
	s.inbox.needsYouMu.Lock()
	defer s.inbox.needsYouMu.Unlock()
	s.hub = h
}

// The hub is told how many open items are in "Needs you" (the roster's inbox_needs_you): the items the apps list
// under that section, the connectors' and the ones not triaged yet among them, and none of the others. It is told
// when what is stored is first read, after every change, and again when asked (a handshake); never before the read.
func TestInboxNeedsYouToldToTheHub(t *testing.T) {
	f := newFakeHub().withMachine()
	f.inbox = newFakeInbox()
	st := newInboxMemStore()
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
	s.inbox.init()
	waitNeedsYouIdle(t, s)
	if got := needsYouTold(t, f); len(got) != 0 {
		t.Fatalf("told %v before what is stored was read", got)
	}
	s.tellInboxNeedsYou()
	if got := needsYouTold(t, f); len(got) != 0 {
		t.Fatalf("told %v before what is stored was read", got)
	}
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := waitNeedsYou(t, f, 1); !slices.Equal(got, []int{0}) {
		t.Fatalf("an empty inbox read: told %v, want [0]", got)
	}

	done := time.Now()
	item := func(id string, change func(*InboxItem)) *InboxItem {
		it := mailItem(id, "shop@example.com", "Shop")
		change(it)
		return it
	}
	items := []*InboxItem{
		item("untriaged", func(*InboxItem) {}),                                                                         // counts: not triaged yet is "Needs you"
		item("action", func(it *InboxItem) { it.Tier = TierAction }),                                                   // counts
		item("connector", func(it *InboxItem) { it.Source, it.Mail, it.Connector = "github", nil, &InboxConnector{} }), // counts
		item("failed", func(it *InboxItem) { it.Tier, it.Handling = TierFYI, &InboxHandling{State: HandlingFailed} }),  // counts
		item("fyi", func(it *InboxItem) { it.Tier = TierFYI }),
		item("noise", func(it *InboxItem) { it.Tier = TierNoise }),
		item("working", func(it *InboxItem) { it.Tier, it.Handling = TierAction, &InboxHandling{State: HandlingWaiting} }),
		item("done", func(it *InboxItem) { it.Tier, it.DoneAt = TierAction, &done }),
	}
	s.inbox.mu.Lock()
	want := 0
	for _, it := range items {
		s.inbox.items[it.ID] = it
		if listed := it.listed(); listed.Status == StatusAction {
			want++ // what the apps count: open items the list puts under "Needs you"
		}
	}
	s.inbox.mu.Unlock()
	if want != 4 {
		t.Fatalf("the list has %d items under Needs you, want 4", want)
	}
	s.saveInbox(context.Background(), items)
	if got := waitNeedsYou(t, f, 2); !slices.Equal(got, []int{0, want}) {
		t.Fatalf("after a change: told %v, want [0 %d]", got, want)
	}

	if _, err := s.setInboxDone(context.Background(), "action", true, false); err != nil {
		t.Fatal(err)
	}
	if got := waitNeedsYou(t, f, 3); got[len(got)-1] != want-1 {
		t.Fatalf("after one was done: told %v, want %d last", got, want-1)
	}
	before := len(needsYouTold(t, f))
	s.tellInboxNeedsYou() // a handshake: the same number again, for the hub's new connection
	if got := waitNeedsYou(t, f, before+1); len(got) != before+1 || got[len(got)-1] != want-1 {
		t.Fatalf("told again: %v", got)
	}
}

// Telling never waits for the hub, and the last count told is of the inbox as it is: changes that come while one count
// is on its way are counted and told once it went, by the one teller, so they reach the hub in order.
func TestInboxNeedsYouDoesNotWaitForTheHub(t *testing.T) {
	f := newFakeHub().withMachine()
	f.inbox = newFakeInbox()
	st := newInboxMemStore()
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
	if err := s.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitNeedsYou(t, f, 1)
	// The hub does not take what it is told: fakeHub.Tell waits for f.mu.
	f.mu.Lock()
	told := make(chan struct{})
	go func() {
		defer close(told)
		for i := range 5 {
			s.inbox.mu.Lock()
			it := mailItem("m"+string(rune('0'+i)), "shop@example.com", "Shop")
			s.inbox.items[it.ID] = it
			s.inbox.mu.Unlock()
			s.tellInboxNeedsYou()
		}
	}()
	select {
	case <-told:
	case <-time.After(5 * time.Second):
		f.mu.Unlock()
		t.Fatal("telling waited for the hub")
	}
	f.mu.Unlock()
	var got []int
	waitFor(t, "the last inbox count to reach the hub", func() bool {
		got = needsYouTold(t, f)
		return len(got) > 0 && got[len(got)-1] == 5
	})
	if got[len(got)-1] != 5 {
		t.Fatalf("the last count told is %v, want 5", got)
	}
	if !slices.IsSorted(got) {
		t.Fatalf("counts out of order: %v", got)
	}
	if len(got) > 3 { // the first read's, the one on its way, and one for everything that came meanwhile
		t.Fatalf("told %d times for changes that came while one count was on its way: %v", len(got), got)
	}
}

// Every connection is told the count once it is up (the hub keeps it of the connection only): the first, which reads
// what is stored, and one after the hub dropped the first, though the inbox did not change meanwhile.
func TestInboxNeedsYouToldAtEachHandshake(t *testing.T) {
	counts := make(chan int, 16)
	conns := make(chan *websocket.Conn, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conns <- ws
		conn := bus.NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			switch method {
			case assistantwire.MethodHello, assistantwire.MethodSnapshot:
				return map[string]any{}, nil
			}
			return nil, &rpc.Error{Code: "not_found", Message: method}
		}
		p.OnEvent = func(event string, params json.RawMessage) {
			var c assistantwire.InboxNeedsYou
			if event == assistantwire.EventInboxNeedsYou && json.Unmarshal(params, &c) == nil {
				counts <- c.Count
			}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer srv.Close()

	b := NewBusHub(BusOptions{Hub: srv.URL, Token: "jb_x", Logf: testLogf(t)})
	b.client.Backoff = 10 * time.Millisecond
	st := newInboxMemStore()
	raw, _ := json.Marshal(mailItem("m1", "shop@example.com", "Shop"))
	if err := st.PutInboxItem(context.Background(), "m1", raw, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{Hub: b, Store: func() Store { return st }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- b.Serve(ctx, svc) }()
	defer func() {
		cancel()
		<-served
	}()
	told := func(what string) {
		t.Helper()
		select {
		case n := <-counts:
			if n != 1 {
				t.Fatalf("%s: told %d, want the one stored item", what, n)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: no count reached the hub", what)
		}
	}
	told("the first connection")
	first := <-conns
	for len(counts) > 0 { // the same count again from the first read is the first connection's too
		<-counts
	}
	_ = first.Close(websocket.StatusGoingAway, "restart")
	select {
	case <-conns:
	case <-time.After(10 * time.Second):
		t.Fatal("the manager did not connect again")
	}
	told("the connection after a drop")
}
