package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestOutboxAndCallRingBusInvalidParams(t *testing.T) {
	for _, tc := range []struct{ method, params, message string }{
		{assistantwire.MethodOutbox, `[]`, "manager.outbox: json: cannot unmarshal array into Go value of type assistantwire.Outbox"},
		{assistantwire.MethodCallRing, `[]`, "call.ring: json: cannot unmarshal array into Go value of type assistantwire.CallRingRequest"},
		{assistantwire.MethodCallRing, `{"call":{"id":"call-1"}}`, "call.ring: id and title are required"},
	} {
		out, err := busMethods[tc.method](nil, t.Context(), nil, json.RawMessage(tc.params))
		var re *rpc.Error
		if out != nil || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams || re.Message != tc.message {
			t.Errorf("%s %s = %v, %v, want invalid_params with %q", tc.method, tc.params, out, err, tc.message)
		}
	}
}

// What a manager kept while the hub was away is delivered in order, each item once: sent again (the manager did not
// hear the answer), or once live and again from the outbox, it does not push twice.
func TestTheOutboxIsDeliveredOnce(t *testing.T) {
	c := &client{device: "test-device", out: make(chan []byte, 16)}
	h := newTestHub(t)
	h.clients, h.push = map[*client]struct{}{c: {}}, newPusher(t.TempDir(), nil, t.Logf)
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: false, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}
	call := func(method string, p any) json.RawMessage {
		t.Helper()
		raw, _ := json.Marshal(p)
		out, err := busMethods[method](h, context.Background(), nil, raw)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		b, _ := json.Marshal(out)
		return b
	}
	n := func(id string) *assistantwire.Notification {
		return &assistantwire.Notification{ID: id, Kind: assistantwire.NotifyPush, Title: id}
	}
	todos, _ := json.Marshal(map[string]string{"type": "todos"})
	items := assistantwire.Outbox{Items: []assistantwire.OutboxItem{
		{ID: "ntf_a", At: time.Now(), Notification: n("ntf_a")},
		{ID: "bc_b", At: time.Now(), Message: todos},
		{ID: "ntf_c", At: time.Now(), Notification: n("ntf_c")},
	}}
	if got := string(call(assistantwire.MethodOutbox, items)); got != `{"done":["ntf_a","bc_b","ntf_c"]}` {
		t.Fatalf("done %s", got)
	}
	var seen []string
	for len(seen) < 3 {
		select {
		case raw := <-c.out:
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			switch {
			case m["type"] == "notify":
				seen = append(seen, m["title"].(string))
			case m["type"] == "todos":
				seen = append(seen, "todos")
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("delivered %v", seen)
		}
	}
	if seen[0] != "ntf_a" || seen[1] != "todos" || seen[2] != "ntf_c" {
		t.Fatalf("order %v", seen)
	}
	// Again (the answer was lost), and one of them live: nothing more reaches the apps.
	call(assistantwire.MethodOutbox, items)
	call(assistantwire.MethodNotify, assistantwire.NotifyRequest{Notification: *n("ntf_a")})
	select {
	case raw := <-c.out:
		t.Fatalf("delivered twice: %s", raw)
	case <-time.After(300 * time.Millisecond):
	}
	// A new push live still goes.
	call(assistantwire.MethodNotify, assistantwire.NotifyRequest{Notification: *n("ntf_d")})
	select {
	case raw := <-c.out:
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil || m["title"] != "ntf_d" {
			t.Fatalf("live push: %s", raw)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a new push did not go")
	}
}

// handedOver forgets the oldest past its bound, and anything after its time.
func TestHandedOverIsBounded(t *testing.T) {
	var s seenItems
	now := time.Now()
	if !s.first("a", now) || s.first("a", now) {
		t.Fatal("a twice")
	}
	if !s.first("a", now.Add(handedOverFor+time.Second)) {
		t.Fatal("a not forgotten after its time")
	}
	for i := range handedOverMost + 10 {
		s.first(string(rune('A'+i%26))+time.Duration(i).String(), now)
	}
	if s.order.Len() > handedOverMost || len(s.entries) > handedOverMost || s.order.Len() != len(s.entries) {
		t.Fatalf("kept %d / %d", s.order.Len(), len(s.entries))
	}
}

// A card's push that came before its card is refused and not recorded as handed over (the manager forgets it all the
// same: it is answered done): the same push sent again once the card is known goes.
func TestARefusedOutboxPushIsNotRecorded(t *testing.T) {
	c := &client{device: "test-device", out: make(chan []byte, 16)}
	h := newTestHub(t)
	h.clients, h.push = map[*client]struct{}{c: {}}, newPusher(t.TempDir(), nil, t.Logf)
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: true, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}
	ix := proto.Interaction{ID: "ix-1", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, Summary: "请选择"}
	items := assistantwire.Outbox{Items: []assistantwire.OutboxItem{{ID: "ntf_ix", At: time.Now(),
		Notification: &assistantwire.Notification{ID: "ntf_ix", Kind: assistantwire.NotifyInput, Interaction: &ix}}}}
	raw, _ := json.Marshal(items)
	if _, err := busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-c.out:
		t.Fatalf("a card's push before its card: %s", got)
	case <-time.After(200 * time.Millisecond):
	}
	reportInteraction(t, h, ix)
	for drained := false; !drained; { // what the card itself sent the apps
		select {
		case <-c.out:
		case <-time.After(200 * time.Millisecond):
			drained = true
		}
	}
	if _, err := busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-c.out:
		var m map[string]any
		if json.Unmarshal(got, &m) != nil || m["interaction_id"] != "ix-1" {
			t.Fatalf("the card's push: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the push did not go once the card was reported")
	}
}

// What one hub process delivered, the next one does not deliver again (handed_over in its database).
func TestHandedOverOutlivesTheHubProcess(t *testing.T) {
	_, _, store := newBusHub(t)
	id := "ntf_restart_" + randomHex(6) // the records stay in the database for two hours: a test run again needs its own
	deliveries := func() int {
		c := &client{device: "test-device", out: make(chan []byte, 16)}
		h := newTestHub(t)
		h.store = store
		h.clients, h.push = map[*client]struct{}{c: {}}, newPusher(t.TempDir(), nil, t.Logf)
		h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
		<-c.out
		_ = h.push.setPrefs(c.device, NotifyPrefs{Input: false, Done: DoneOff})
		raw, _ := json.Marshal(assistantwire.NotifyRequest{Notification: assistantwire.Notification{ID: id,
			Kind: assistantwire.NotifyPush, Title: "once"}})
		if _, err := busMethods[assistantwire.MethodNotify](h, context.Background(), nil, raw); err != nil {
			t.Fatal(err)
		}
		n := 0
		for {
			select {
			case <-c.out:
				n++
			case <-time.After(300 * time.Millisecond):
				return n
			}
		}
	}
	if n := deliveries(); n != 1 {
		t.Fatalf("the first hub delivered %d", n)
	}
	if n := deliveries(); n != 0 {
		t.Fatalf("the next hub delivered it again: %d", n)
	}
}

// flakyHandOver is a hand-over store that does not answer while fail is set.
type flakyHandOver struct {
	Store
	fail bool
	seen map[string]bool
}

func (f *flakyHandOver) HandOverFirst(_ context.Context, manager, id string, _ time.Duration) (bool, error) {
	if f.fail {
		return false, errors.New("database hiccup")
	}
	k := manager + "/" + id
	first := !f.seen[k]
	f.seen[k] = true
	return first, nil
}

func (f *flakyHandOver) HandOverForget(_ context.Context, manager, id string) error {
	delete(f.seen, manager+"/"+id)
	return nil
}

// An item handled while the store did not answer is not delivered again once it does.
func TestHandedOverWhileTheStoreWasDown(t *testing.T) {
	h := newTestHub(t)
	st := &flakyHandOver{fail: true, seen: map[string]bool{}}
	h.store = st
	first, rel, paused := h.beginHandOver(context.Background(), "assistant", "ntf_x")
	if !first || paused {
		t.Fatalf("the first time: first=%v paused=%v", first, paused)
	}
	if rel != nil {
		rel()
	}
	st.fail = false
	first, rel, paused = h.beginHandOver(context.Background(), "assistant", "ntf_x")
	if first || paused || rel != nil {
		t.Fatalf("delivered again once the store answered: first=%v paused=%v", first, paused)
	}
	first, rel, paused = h.beginHandOver(context.Background(), "assistant", "ntf_y")
	if !first || paused {
		t.Fatalf("a new one: first=%v paused=%v", first, paused)
	}
	if rel != nil {
		rel()
	}
}

// gatedHandOver is a hand-over store whose answers wait for a gate (a slow database).
type gatedHandOver struct {
	Store
	gate chan struct{}
	mu   sync.Mutex
	seen map[string]bool
}

func (g *gatedHandOver) HandOverFirst(_ context.Context, manager, id string, _ time.Duration) (bool, error) {
	<-g.gate
	g.mu.Lock()
	defer g.mu.Unlock()
	k := manager + "/" + id
	first := !g.seen[k]
	g.seen[k] = true
	return first, nil
}

func (g *gatedHandOver) HandOverForget(context.Context, string, string) error { return nil }

// Two sends of one id at once (a live push still being handled when it comes again from the outbox): exactly one is
// delivered, also with a slow store.
func TestTwoSendsOfOneIDAtOnce(t *testing.T) {
	h := newTestHub(t)
	st := &gatedHandOver{gate: make(chan struct{}), seen: map[string]bool{}}
	h.store = st
	results := make(chan bool, 2)
	for range 2 {
		go func() {
			first, rel, _ := h.beginHandOver(context.Background(), "assistant", "ntf_z")
			if rel != nil {
				rel()
			}
			results <- first
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(st.gate)
	a, b := <-results, <-results
	if a == b {
		t.Fatalf("both %v: want exactly one delivery", a)
	}
}

// hookHandOver records HandOverFirst / HandOverForget calls and supports hooks for quiesce interleavings.
type hookHandOver struct {
	Store
	mu       sync.Mutex
	seen     map[string]bool
	firsts   int
	forgets  int
	onFirst  func(manager, id string)
	onForget func(manager, id string)
}

func (s *hookHandOver) HandOverFirst(_ context.Context, manager, id string, _ time.Duration) (bool, error) {
	if s.onFirst != nil {
		s.onFirst(manager, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.firsts++
	if s.seen == nil {
		s.seen = map[string]bool{}
	}
	k := manager + "/" + id
	first := !s.seen[k]
	s.seen[k] = true
	return first, nil
}

func (s *hookHandOver) HandOverForget(_ context.Context, manager, id string) error {
	if s.onForget != nil {
		s.onForget(manager, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgets++
	delete(s.seen, manager+"/"+id)
	return nil
}

// While writes are paused, outbox items are neither delivered nor marked Done (and in-memory seenItems is untouched),
// so a post-restart replay still runs the durable HandOverFirst duplicate check after resume.
func TestPausedOutboxSkipsDeliveryAndPreservesDurableDeduplication(t *testing.T) {
	c := &client{device: "test-device", out: make(chan []byte, 16)}
	h := newTestHub(t)
	st := &hookHandOver{seen: map[string]bool{"assistant/ntf_dup": true}}
	h.store = st
	h.clients, h.push = map[*client]struct{}{c: {}}, newPusher(t.TempDir(), nil, t.Logf)
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: false, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}

	if err := h.quiesce.pause(context.Background()); err != nil {
		t.Fatal(err)
	}
	n := func(id string) *assistantwire.Notification {
		return &assistantwire.Notification{ID: id, Kind: assistantwire.NotifyPush, Title: id}
	}
	raw, _ := json.Marshal(assistantwire.Outbox{Items: []assistantwire.OutboxItem{
		{ID: "ntf_dup", At: time.Now(), Notification: n("ntf_dup")},
		{ID: "ntf_new", At: time.Now(), Notification: n("ntf_new")},
	}})
	out, err := busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw)
	if err != nil {
		t.Fatalf("outbox while paused: %v", err)
	}
	res := out.(assistantwire.OutboxResult)
	if len(res.Done) != 0 {
		t.Fatalf("paused outbox marked items done: %v", res.Done)
	}
	// Live notify while paused is refused rather than silently acknowledged or delivered.
	liveRaw, _ := json.Marshal(assistantwire.NotifyRequest{Notification: *n("ntf_live")})
	if _, err := busMethods[assistantwire.MethodNotify](h, context.Background(), nil, liveRaw); err == nil {
		t.Fatal("live notify succeeded while paused")
	} else {
		var re *rpc.Error
		if !errors.As(err, &re) {
			t.Fatalf("live notify error = %v, want *rpc.Error", err)
		}
	}
	if st.firsts != 0 {
		t.Fatalf("HandOverFirst called %d times while paused", st.firsts)
	}
	select {
	case got := <-c.out:
		t.Fatalf("delivered while paused: %s", got)
	case <-time.After(150 * time.Millisecond):
	}

	h.quiesce.resume()

	// After resume, ntf_dup is suppressed by the durable store and ntf_new is delivered once.
	out, err = busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw)
	if err != nil {
		t.Fatalf("outbox after resume: %v", err)
	}
	res = out.(assistantwire.OutboxResult)
	if len(res.Done) != 2 || res.Done[0] != "ntf_dup" || res.Done[1] != "ntf_new" {
		t.Fatalf("done after resume = %v", res.Done)
	}
	select {
	case got := <-c.out:
		var m map[string]any
		if json.Unmarshal(got, &m) != nil || m["title"] != "ntf_new" {
			t.Fatalf("unexpected delivery after resume: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ntf_new was not delivered after resume")
	}
	select {
	case got := <-c.out:
		t.Fatalf("duplicate delivered after resume: %s", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// A pause starting after HandOverFirst commits and before delivery fails waits for the handover turn to finish
// HandOverForget, so a retry after resume is not stranded against a stale durable claim.
func TestPauseBetweenHandOverClaimAndFailedDeliveryDoesNotStrandItem(t *testing.T) {
	c := &client{device: "test-device", out: make(chan []byte, 16)}
	h := newTestHub(t)
	h.clients, h.push = map[*client]struct{}{c: {}}, newPusher(t.TempDir(), nil, t.Logf)
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: true, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}

	pauseDone := make(chan error, 1)
	var pauseOnce sync.Once
	st := &hookHandOver{
		seen: map[string]bool{},
		onFirst: func(_, _ string) {
			pauseOnce.Do(func() {
				go func() { pauseDone <- h.quiesce.pause(context.Background()) }()
				waitFor(t, "quiesce paused during handover claim", func() bool {
					h.quiesce.mu.Lock()
					defer h.quiesce.mu.Unlock()
					return h.quiesce.paused
				})
				// pause must still be waiting on the held handover turn.
				select {
				case err := <-pauseDone:
					t.Fatalf("pause completed before delivery and HandOverForget settled: %v", err)
				case <-time.After(20 * time.Millisecond):
				}
			})
		},
	}
	h.store = st

	ix := proto.Interaction{ID: "ix-pause", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, Summary: "choose"}
	raw, _ := json.Marshal(assistantwire.Outbox{Items: []assistantwire.OutboxItem{{
		ID:           "ntf_ix_pause",
		At:           time.Now(),
		Notification: &assistantwire.Notification{ID: "ntf_ix_pause", Kind: assistantwire.NotifyInput, Interaction: &ix},
	}}})
	// Delivery fails because the interaction card has not been reported yet.
	if _, err := busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw); err != nil {
		t.Fatalf("outbox failed delivery: %v", err)
	}
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatalf("pause returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pause did not finish after handover settled")
	}
	st.mu.Lock()
	forgets := st.forgets
	stillClaimed := st.seen["assistant/ntf_ix_pause"]
	st.mu.Unlock()
	if forgets != 1 || stillClaimed {
		t.Fatalf("durable forget did not run under the held turn: forgets=%d stillClaimed=%v", forgets, stillClaimed)
	}

	h.quiesce.resume()
	reportInteraction(t, h, ix)
	for drained := false; !drained; {
		select {
		case <-c.out:
		case <-time.After(150 * time.Millisecond):
			drained = true
		}
	}

	// Retry after resume must succeed and deliver the push.
	if _, err := busMethods[assistantwire.MethodOutbox](h, context.Background(), nil, raw); err != nil {
		t.Fatalf("outbox retry after resume: %v", err)
	}
	select {
	case got := <-c.out:
		var m map[string]any
		if json.Unmarshal(got, &m) != nil || m["interaction_id"] != "ix-pause" {
			t.Fatalf("unexpected retry push: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retried push was stranded after pause between claim and failed delivery")
	}
}

func TestSeenItemsForgetAndReinsertDoesNotEvictFreshKey(t *testing.T) {
	var s seenItems
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if !s.first("anchor", now) {
		t.Fatal("anchor should be new")
	}
	// Item fails once and is forgotten, then retries and succeeds shortly after.
	// If forget leaves a stale slot in order, capacity eviction of the stale slot
	// deletes the freshly accepted key from the map.
	if !s.first("flaky", now) {
		t.Fatal("flaky initial attempt should be new")
	}
	s.forget("flaky")
	later := now.Add(time.Minute)
	if !s.first("flaky", later) {
		t.Fatal("flaky retry after forget should be new")
	}

	// Fill capacity and push eviction past anchor and the stale forgotten slot.
	for i := range handedOverMost - 1 {
		key := "filler-" + time.Duration(i).String()
		if !s.first(key, later) {
			t.Fatalf("%s should be new", key)
		}
	}
	// Freshly delivered "flaky" must still be deduplicated.
	if s.first("flaky", later) {
		t.Fatal("freshly accepted key was evicted by its stale forgotten order entry")
	}
	if s.order.Len() != len(s.entries) || len(s.entries) > handedOverMost {
		t.Fatalf("invariant broken: order=%d entries=%d", s.order.Len(), len(s.entries))
	}
}

func TestSeenItemsRepeatedForgetsDoNotStarveLiveEntries(t *testing.T) {
	var s seenItems
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if !s.first("anchor", now) || !s.first("live", now) {
		t.Fatal("initial keys should be new")
	}
	for range handedOverMost {
		if !s.first("retrying", now) {
			t.Fatal("forgotten retrying key should be accepted again")
		}
		s.forget("retrying")
	}
	if s.first("live", now) {
		t.Fatal("repeated forgets evicted an unrelated live entry")
	}
	if s.order.Len() != len(s.entries) || len(s.entries) != 2 {
		t.Fatalf("invariant broken: order=%d entries=%d, want 2", s.order.Len(), len(s.entries))
	}
}

func TestSeenItemsDuplicateLookupAtCapacityDoesNotEvict(t *testing.T) {
	var s seenItems
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	for i := range handedOverMost {
		key := "k-" + time.Duration(i).String()
		if !s.first(key, now) {
			t.Fatalf("%s should be new", key)
		}
	}
	// Checking an already-seen key at capacity must not evict the oldest live entry.
	newest := "k-" + time.Duration(handedOverMost-1).String()
	if s.first(newest, now) {
		t.Fatal("newest should already be seen")
	}
	if s.first("k-0s", now) {
		t.Fatal("duplicate lookup at capacity evicted oldest live entry")
	}
	if s.order.Len() != len(s.entries) || len(s.entries) != handedOverMost {
		t.Fatalf("invariant broken: order=%d entries=%d, want %d", s.order.Len(), len(s.entries), handedOverMost)
	}
}

func TestSeenItemsForgetAndReinsertDoesNotStallExpiration(t *testing.T) {
	var s seenItems
	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if !s.first("anchor", t0) {
		t.Fatal("anchor should be new")
	}
	if !s.first("reused", t0.Add(time.Minute)) {
		t.Fatal("reused first try should be new")
	}
	s.forget("reused")
	for i := range 100 {
		key := "old-" + time.Duration(i).String()
		if !s.first(key, t0.Add(2*time.Minute)) {
			t.Fatalf("%s should be new", key)
		}
	}
	// Re-insert "reused" near the end of the original TTL window.
	tReused := t0.Add(handedOverFor - 10*time.Minute)
	if !s.first("reused", tReused) {
		t.Fatal("reused retry should be new")
	}
	// After anchor and all 100 old-* keys have expired, "reused" is still fresh.
	// Inserting handedOverMost-1 fresh keys brings live entries to exactly handedOverMost.
	tLate := t0.Add(handedOverFor + 5*time.Minute)
	for i := range handedOverMost - 1 {
		key := "fresh-" + time.Duration(i).String()
		if !s.first(key, tLate) {
			t.Fatalf("%s should be new", key)
		}
	}
	if s.first("reused", tLate) {
		t.Fatal("freshly reinserted key was evicted because expired entries stalled behind its ghost")
	}
	if s.first("fresh-0s", tLate) {
		t.Fatal("fresh entry was evicted because expired entries stalled behind a forgotten ghost")
	}
	if s.order.Len() != len(s.entries) || len(s.entries) != handedOverMost {
		t.Fatalf("invariant broken: order=%d entries=%d, want %d", s.order.Len(), len(s.entries), handedOverMost)
	}
}

func TestSeenItemsZeroValueForgetAndNonMonotonicExpiry(t *testing.T) {
	var s seenItems
	// Zero-value forget must be a safe no-op.
	s.forget("missing")

	t0 := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	// Front has a later timestamp than a subsequent entry (e.g., clock stepped backward).
	if !s.first("front-newer", t0.Add(10*time.Minute)) {
		t.Fatal("front-newer should be new")
	}
	if !s.first("behind-older", t0) {
		t.Fatal("behind-older should be new")
	}
	// At t0 + handedOverFor + 1m, front-newer is still unexpired, while behind-older has expired.
	tCheck := t0.Add(handedOverFor + time.Minute)
	if !s.first("behind-older", tCheck) {
		t.Fatal("expired key behind an unexpired front should be accepted again")
	}
	if s.first("behind-older", tCheck) {
		t.Fatal("re-accepted key should now be deduplicated")
	}
	if s.order.Len() != len(s.entries) || len(s.entries) != 2 {
		t.Fatalf("invariant broken: order=%d entries=%d, want 2", s.order.Len(), len(s.entries))
	}
}
