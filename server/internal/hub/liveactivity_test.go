package hub

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/relayproto"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The JSON is the Swift ContentState: field names and Unix-second times must not drift.
func TestFleetStateJSON(t *testing.T) {
	st := FleetState{Phase: "working", Working: 1, WorkingSince: 1761700000, Items: []FleetItem{{ID: "m/s", Title: "api", Kind: "working", Since: 1761700000}}}
	b, _ := json.Marshal(st)
	want := `{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"api","place":"","kind":"working","detail":"","since":1761700000}],"workingSince":1761700000,"details":false}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

// A background workflow's item (#2935, #3437), as FleetStateTests.decodesABackgroundItem reads it: background, the line,
// and the bar's counts. A plain working item carries neither (TestFleetStateJSON), so older apps see the same JSON.
func TestFleetBackgroundItemJSON(t *testing.T) {
	st := FleetState{Phase: "working", Working: 1, WorkingSince: 1761700000, Items: []FleetItem{{ID: "m/s", Title: "audit", Kind: "working",
		Detail: "parity-audit · Verify · 169/185", Background: true, Progress: &FleetProgress{Done: 169, Started: 185}, Since: 1761700000}}}
	b, _ := json.Marshal(st)
	want := `{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"audit","place":"","kind":"working","detail":"parity-audit · Verify · 169/185","background":true,"progress":{"done":169,"started":185},"since":1761700000}],"workingSince":1761700000,"details":false}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestCoalescer(t *testing.T) {
	var c coalescer
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Kind: "working"}}}
	if now, sched := c.offer(working); now || !sched {
		t.Fatalf("first change should wait: %v %v", now, sched)
	}
	if now, sched := c.offer(working); now || sched {
		t.Fatalf("already scheduled: %v %v", now, sched)
	}
	c.sentState(working)
	if now, sched := c.offer(working); now || sched {
		t.Fatalf("unchanged: %v %v", now, sched)
	}
	asks := working
	asks.Phase, asks.NeedsYou = "needsYou", 1
	asks.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix1"}}, asks.Items...)
	if now, _ := c.offer(asks); !now {
		t.Fatal("a new request goes out at once")
	}
	c.sentState(asks)
	// The same request with the worker gone: an ordinary change.
	less := asks
	less.Working, less.Items = 0, asks.Items[:1]
	if now, sched := c.offer(less); now || !sched {
		t.Fatalf("no new request: %v %v", now, sched)
	}
}

func TestLiveActivityPayload(t *testing.T) {
	now := time.Unix(1761700000, 0)
	st := FleetState{Phase: "needsYou", NeedsYou: 2, Working: 3}
	start := liveActivityPayload("start", st, now)["aps"].(map[string]any)
	if start["event"] != "start" || start["attributes-type"] != "PersonaActivityAttributes" || start["timestamp"] != now.Unix() {
		t.Fatalf("start %v", start)
	}
	if start["alert"].(map[string]any)["body"] != "2 个需要你 · 3 个在工作" {
		t.Fatalf("alert %v", start["alert"])
	}
	if start["stale-date"] != now.Add(fleetStaleIn).Unix() || start["relevance-score"] != 1.0 {
		t.Fatalf("stale / relevance %v", start)
	}
	update := liveActivityPayload("update", st, now)["aps"].(map[string]any)
	if _, ok := update["alert"]; ok {
		t.Fatal("updates do not alert: the interaction push already did")
	}
	if _, ok := update["category"]; ok {
		t.Fatal("no category on activity pushes")
	}
	end := liveActivityPayload("end", st, now)["aps"].(map[string]any)
	if end["dismissal-date"] != now.Unix() {
		t.Fatalf("end %v", end)
	}
}

// fakeAPNs records what the hub sends.
type fakeAPNs struct {
	mu   sync.Mutex
	got  []apnsCall
	gone map[string]bool // tokens answered with 410
}

type apnsCall struct {
	Token, Topic, Type, Priority, Event string
	State                               FleetState
	Body                                []byte
	Collapse                            string // apns-collapse-id
}

func (f *fakeAPNs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only pushes count: a node on the test machine probing local ports sends GET / here too (docs/lessons/testing.md).
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/3/device/") {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var body struct {
		APS struct {
			Event string     `json:"event"`
			State FleetState `json:"content-state"`
		} `json:"aps"`
	}
	_ = json.Unmarshal(b, &body)
	token := strings.TrimPrefix(r.URL.Path, "/3/device/")
	f.mu.Lock()
	f.got = append(f.got, apnsCall{token, r.Header.Get("apns-topic"), r.Header.Get("apns-push-type"), r.Header.Get("apns-priority"), body.APS.Event, body.APS.State, b, r.Header.Get("apns-collapse-id")})
	gone := f.gone[token]
	f.mu.Unlock()
	if gone {
		w.WriteHeader(410)
		_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
	}
}

func (f *fakeAPNs) calls(t *testing.T, n int) []apnsCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.got) >= n {
			out := append([]apnsCall{}, f.got...)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("want %d APNs calls, got %d: %+v", n, len(f.got), f.got)
	return nil
}

// fleetStartStore models durable state shared by independently reconstructed hubs.
// Real transaction/row-lock behavior is covered by db.TestLiveActivityStartBudget.
type fleetStartStore struct {
	Store
	mu      sync.Mutex
	rows    map[string]db.LiveActivity
	due     map[string]time.Time
	claimed map[string]time.Time
	fail    bool
}

func newFleetStartStore() *fleetStartStore {
	return &fleetStartStore{rows: map[string]db.LiveActivity{}, due: map[string]time.Time{}, claimed: map[string]time.Time{}}
}

// HoldLiveActivityStart is the store's: only a start claimed up to asOf, and changed only when the deadline moved.
func (s *fleetStartStore) HoldLiveActivityStart(_ context.Context, device string, asOf time.Time, hold time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return false, fmt.Errorf("injected hold persistence failure")
	}
	at, ok := s.claimed[device]
	if !ok || at.After(asOf) || s.due[device].Equal(at.Add(hold)) {
		return false, nil
	}
	s.due[device] = at.Add(hold)
	return true, nil
}

// A phone registering over its socket saves its tools: nothing these tests read.
func (s *fleetStartStore) ReplacePhoneTools(context.Context, db.PhoneToolRegistration) error {
	return nil
}

func (s *fleetStartStore) SaveLiveActivity(_ context.Context, v db.LiveActivity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[v.DeviceID] = v
	return nil
}

func (s *fleetStartStore) LiveActivities(context.Context) ([]db.LiveActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var rows []db.LiveActivity
	for _, r := range s.rows {
		rows = append(rows, r)
	}
	return rows, nil
}

func (s *fleetStartStore) ClaimLiveActivityStart(_ context.Context, device, token string, at time.Time, cooldown, maxAge time.Duration) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return time.Time{}, false, fmt.Errorf("injected claim persistence failure")
	}
	r := s.rows[device]
	if r.PushToStartToken != token || (r.PushToken != "" && at.Sub(r.StartedAt) < maxAge) {
		return time.Time{}, false, nil
	}
	if due := s.due[device]; at.Before(due) {
		return due, false, nil
	}
	s.claimed[device], s.due[device] = at, at.Add(cooldown)
	return s.due[device], true, nil
}

type fleetClaimHookStore struct {
	Store
	afterClaim func()
}

func (s *fleetClaimHookStore) ClaimLiveActivityStart(ctx context.Context, device, token string, at time.Time, cooldown, maxAge time.Duration) (time.Time, bool, error) {
	due, claimed, err := s.Store.ClaimLiveActivityStart(ctx, device, token, at, cooldown, maxAge)
	if claimed {
		s.afterClaim()
	}
	return due, claimed, err
}

func TestFleetStartRechecksAfterSlowClaim(t *testing.T) {
	for _, change := range []string{"empty", "update_token", "foreground", "disabled", "rotated_start"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				stop()
				h.live.update("phone", func(v *db.LiveActivity) { v.PushToken = "" })
				working := FleetState{Phase: "working", Working: 1}
				reportLive(t, h, working, FleetStats{})
				h.store = &fleetClaimHookStore{Store: h.store, afterClaim: func() {
					switch change {
					case "empty":
						reportLive(t, h, FleetState{}, FleetStats{})
					case "update_token":
						h.live.update("phone", func(v *db.LiveActivity) { v.PushToken, v.StartedAt = "fresh", time.Now() })
					case "foreground":
						h.mu.Lock()
						h.clients[&client{out: make(chan []byte, 64), phone: true, pushDevice: "phone"}] = struct{}{}
						h.mu.Unlock()
					case "disabled":
						_ = h.push.setPrefs("phone", NotifyPrefs{LiveActivity: false})
					case "rotated_start":
						h.live.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "rotated" })
					}
				}}
				due := h.live.flush(working, true, true)
				synctest.Wait()
				if got := sink.count("phone", "liveactivity", "start"); got != 0 {
					t.Fatalf("%s during claim still pushed: %d", change, got)
				}
				if !due.Equal(time.Now().Add(fleetRefresh)) {
					t.Fatalf("spent claim lost retry deadline: %s", due)
				}
			})
		})
	}
}

func testLive(t *testing.T) (*Hub, *fakeAPNs) {
	h := newTestHub(t)
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "devtok", Topic: "app.example"})
	h.store = newFleetStartStore()
	return h, f
}

func TestFleetStartAcrossFiveEpisodes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, sink, stop := localFleetLoop(t)
		stop() // manual flushes must not race the single-owner coalescer run loop
		h.live.update("phone", func(v *db.LiveActivity) { v.PushToken = "" })
		for i := range 5 {
			h.live.flush(FleetState{}, false, false)
			h.live.flush(FleetState{Phase: "working", Working: i + 1}, false, false)
			time.Sleep(time.Second)
			synctest.Wait()
		}
		if got := sink.count("phone", "liveactivity", "start"); got != 1 {
			t.Fatalf("after: five episodes produced %d starts, want 1", got)
		}
		// The phone answers that start (its app connected): the next waits for the rolling cooldown alone. Unanswered,
		// it would wait for fleetMaxAge (#7641, TestFleetUnansweredStartHoldsTheNext).
		h.live.phoneAnswered(context.Background(), "phone")
		// The timer reads the newest active state, not any of the five old ones.
		h.live = newLiveActivities(h)
		reportLive(t, h, FleetState{Phase: "working", Working: 9}, FleetStats{})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); h.live.run(ctx) }()
		defer func() { cancel(); <-done }()
		time.Sleep(fleetRefresh - 5*time.Second)
		synctest.Wait()
		if got := sink.count("phone", "liveactivity", "start"); got != 2 {
			t.Fatalf("at expiry: got %d starts, want 2 total", got)
		}
		sink.mu.Lock()
		var last FleetState
		for _, call := range sink.got {
			if call.device == "phone" && call.event == "start" {
				last = call.state
			}
		}
		sink.mu.Unlock()
		if last.Working != 9 {
			t.Fatalf("deferred start used old state: %+v", last)
		}
		time.Sleep(fleetRefresh)
		synctest.Wait()
		if got := sink.count("phone", "liveactivity", "start"); got != 2 {
			t.Fatalf("unchanged episode repeated: %d", got)
		}
		t.Log("after: five episodes yielded one start; expiry sent only the latest state once")
	})
}

func TestFleetDeferredStartCancellation(t *testing.T) {
	for _, change := range []string{"empty", "done", "foreground", "update_token", "disabled"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				defer stop()
				h.live.update("phone", func(v *db.LiveActivity) { v.PushToken = "" })
				reportLive(t, h, FleetState{Phase: "working", Working: 1}, FleetStats{})
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				// Answered: the next round's start is the one the cooldown defers, not one held for fleetMaxAge (#7641).
				h.live.phoneAnswered(context.Background(), "phone")
				reportLive(t, h, FleetState{Phase: "done"}, FleetStats{})
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				reportLive(t, h, FleetState{Phase: "working", Working: 2}, FleetStats{})
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				switch change {
				case "empty":
					reportLive(t, h, FleetState{}, FleetStats{})
				case "done":
					reportLive(t, h, FleetState{Phase: "done"}, FleetStats{})
				case "foreground":
					h.mu.Lock()
					h.clients[&client{out: make(chan []byte, 64), phone: true, pushDevice: "phone"}] = struct{}{}
					h.mu.Unlock()
				case "update_token":
					h.live.update("phone", func(v *db.LiveActivity) { v.PushToken, v.StartedAt = "fresh", time.Now() })
				case "disabled":
					putFleetPrefs(t, h, "phone", `{"live_activity":false}`)
				}
				time.Sleep(fleetRefresh)
				synctest.Wait()
				if got := sink.count("phone", "liveactivity", "start"); got != 1 {
					t.Fatalf("%s failed to cancel deferred start: %d", change, got)
				}
			})
		})
	}
}

func TestFleetStartRestartAndConcurrentInstances(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, sink, stop := localFleetLoop(t)
		stop()
		h.live.update("phone", func(v *db.LiveActivity) { v.PushToken = "" })
		working := FleetState{Phase: "working", Working: 1}
		// Two fresh instances load the same persisted token and claim concurrently.
		one, two := newLiveActivities(h), newLiveActivities(h)
		one.load(context.Background())
		two.load(context.Background())
		var wg sync.WaitGroup
		for _, la := range []*liveActivities{one, two} {
			wg.Go(func() { la.flush(working, true, true) })
		}
		wg.Wait()
		synctest.Wait()
		if got := sink.count("phone", "liveactivity", "start"); got != 1 {
			t.Fatalf("concurrent instances started %d times", got)
		}
		// Answered, so that what follows is the rolling cooldown: unanswered, a start holds the next for fleetMaxAge
		// across a restart too (#7641, TestFleetUnansweredStartHoldsTheNext).
		h.live.phoneAnswered(context.Background(), "phone")
		// Reconstruct all ephemeral state and run with no further fleet changes:
		// startup must recover the due time from Postgres and eventually flush.
		h.live = newLiveActivities(h)
		reportLive(t, h, FleetState{Phase: "working", Working: 7}, FleetStats{})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); h.live.run(ctx) }()
		defer func() { cancel(); <-done }()
		time.Sleep(fleetRefresh - time.Second)
		synctest.Wait()
		if got := sink.count("phone", "liveactivity", "start"); got != 1 {
			t.Fatalf("restart reset rolling budget: %d", got)
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if got := sink.count("phone", "liveactivity", "start"); got != 2 {
			t.Fatalf("restart lost deferred start at expiry: %d", got)
		}
	})
}

func TestFleetStartPersistenceFailsClosedWithoutLimitingAlertsOrUrgentUpdates(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(fmt.Sprintf("no_store=%v", unavailable), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoopWith(t, func(h *Hub) {
					h.store.(*fleetStartStore).fail = true
					if unavailable {
						h.store = nil
					}
				})
				defer stop()
				h.live.update("phone", func(v *db.LiveActivity) { v.PushToken = "" })
				reportLive(t, h, FleetState{Phase: "working", Working: 1}, FleetStats{})
				time.Sleep(fleetCoalesce + fleetMinGap)
				synctest.Wait()
				if got := sink.count("phone", "liveactivity", "start"); got != 0 {
					t.Fatalf("failed persistence authorized %d starts", got)
				}
				h.notifyInteraction("", proto.Interaction{ID: "ordinary", Kind: proto.IxPermission, Summary: "Approve the operation"}, "computer", false)
				synctest.Wait()
				if got := sink.count("", "alert", ""); got != 2 {
					t.Fatalf("start claim failure swallowed ordinary alerts: %d", got)
				}
				h.live.update("phone", func(v *db.LiveActivity) { v.PushToken, v.StartedAt = "fresh", time.Now() })
				reportLive(t, h, FleetState{Phase: "needsYou", NeedsYou: 1}, FleetStats{})
				synctest.Wait()
				if got := sink.count("phone", "liveactivity", "update"); got != 1 {
					t.Fatalf("urgent update was delayed by start budget: %d", got)
				}
				sink.mu.Lock()
				for _, call := range sink.got {
					if call.device == "phone" && call.event == "update" && call.priority != "10" {
						t.Errorf("urgent update lost high priority: %+v", call)
					}
				}
				sink.mu.Unlock()
			})
		})
	}
}

func TestLiveActivityFlush(t *testing.T) {
	h, f := testLive(t)
	la := h.live
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}

	// Only a push-to-start token: start once per episode.
	la.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "p2s" })
	la.flush(working, false, false)
	la.flush(working, false, false)
	c := f.calls(t, 1)
	if c[0].Token != "p2s" || c[0].Event != "start" || c[0].Type != "liveactivity" || c[0].Topic != "app.example.push-type.liveactivity" {
		t.Fatalf("start %+v", c[0])
	}
	if c[0].State.Details {
		t.Fatal("lock screen details are off by default")
	}

	// The app reports the started activity's token: updates go there, the device's settings decide the details.
	la.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", time.Now() })
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, LockDetails: true}); err != nil {
		t.Fatal(err)
	}
	la.flush(working, true, false)
	c = f.calls(t, 2)
	if c[1].Token != "acttok" || c[1].Event != "update" || c[1].Priority != "10" || !c[1].State.Details {
		t.Fatalf("update %+v", c[1])
	}

	// Empty for a while: end, and forget the activity token.
	la.flush(FleetState{}, false, false)
	la.mu.Lock()
	la.emptyAt = time.Now().Add(-2 * fleetEndAfter)
	la.mu.Unlock()
	la.tick()
	c = f.calls(t, 3)
	if c[2].Token != "acttok" || c[2].Event != "end" {
		t.Fatalf("end %+v", c[2])
	}
	if r := la.rows["phone"]; r.PushToken != "" || r.PushToStartToken != "p2s" {
		t.Fatalf("row after end %+v", r)
	}

	// A new episode starts only after the rolling cooldown; turning it off stops everything.
	la.now = func() time.Time { return time.Now().Add(fleetRefresh) }
	la.flush(working, false, false)
	f.calls(t, 4)
	_ = h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: false})
	la.flush(FleetState{}, false, false)
	la.flush(working, false, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 4)); n != 4 {
		t.Fatalf("activity off still pushed: %d", n)
	}
}

func TestLiveActivityDeadToken(t *testing.T) {
	h, f := testLive(t)
	f.gone["acttok"] = true
	h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", time.Now() })
	h.live.flush(FleetState{Phase: "working", Working: 1}, false, false)
	f.calls(t, 1)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.live.mu.Lock()
		tok := h.live.rows["phone"].PushToken
		h.live.mu.Unlock()
		if tok == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a 410 should forget the activity token")
}

func TestLiveActivityTokenAPI(t *testing.T) {
	h := newTestHub(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	post := func(body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/devices/phone/live-activity", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", err, resp.Status)
		}
		resp.Body.Close()
	}
	post(`{"push_to_start_token":"p2s"}`)
	post(`{"activity_id":"a1","push_token":"t1"}`)
	r := h.live.rows["phone"]
	if r.PushToStartToken != "p2s" || r.ActivityID != "a1" || r.PushToken != "t1" || r.StartedAt.IsZero() {
		t.Fatalf("%+v", r)
	}
	post(`{"activity_id":"a1","push_token":"t2"}`) // rotated
	if h.live.rows["phone"].PushToken != "t2" {
		t.Fatal("rotation")
	}
	post(`{"activity_id":"old","ended":true}`) // an older activity ending must not drop the current one
	if h.live.rows["phone"].PushToken != "t2" {
		t.Fatal("ended an unrelated activity")
	}
	post(`{"activity_id":"a1","ended":true}`)
	if r := h.live.rows["phone"]; r.PushToken != "" || r.PushToStartToken != "p2s" {
		t.Fatalf("after end %+v", r)
	}
}

// TestManualLiveActivityPush sends one real update through APNs (sandbox) with this machine's ~/.persona/apns.json, to
// check a running activity end to end (docs/operations/testing.md). Opt-in: PERSONA_LA_TOKEN=<activity push token>
// PERSONA_LA_TOPIC=<app bundle id>.
func TestManualLiveActivityPush(t *testing.T) {
	token, topic := os.Getenv("PERSONA_LA_TOKEN"), os.Getenv("PERSONA_LA_TOPIC")
	if token == "" || topic == "" {
		t.Skip("set PERSONA_LA_TOKEN and PERSONA_LA_TOPIC")
	}
	p := newPusher(dataDir(), nil, t.Logf)
	if p.direct == nil {
		t.Skip("no APNs key here")
	}
	now := time.Now()
	st := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 1, WorkingSince: float64(now.Add(-100 * time.Second).Unix()), Details: true,
		Items: []FleetItem{{ID: "m/s9", Title: "APNs 推送", Place: "测试", Kind: proto.IxPermission, Detail: "Bash: echo hi", Interaction: "ix9", Since: float64(now.Unix())}}}
	done := make(chan struct{})
	p.logf = func(f string, a ...any) { t.Logf(f, a...) }
	p.sendLiveActivity(Device{Name: "simulator", APNSEnv: "sandbox", Topic: topic}, token, liveActivityPayload("update", st, now), "10", func() { t.Error("token gone") })
	go func() { time.Sleep(3 * time.Second); close(done) }()
	<-done
}

// Turning lock screen details (or the activity) on / off changes what each device gets though the fleet did not:
// the next signal must push anyway.
func TestPrefsChangeResends(t *testing.T) {
	h := newTestHub(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/devices/phone/prefs", strings.NewReader(`{"lock_details":true}`))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp.Status)
	}
	resp.Body.Close()
	h.live.mu.Lock()
	resend := h.live.resend["phone"]
	h.live.mu.Unlock()
	if !resend {
		t.Fatal("a prefs change should force the next fleet push")
	}
	if p := h.push.prefs("phone"); !p.LockDetails || !p.LiveActivity {
		t.Fatalf("%+v", p)
	}
}

// fleetPushSink uses an in-memory relay transport: synctest can settle every push
// goroutine without real sockets, a database or wall-clock waits.
type fleetPushSink struct {
	mu  sync.Mutex
	got []fleetPushCall
	// answer, when set, answers a call instead of 200: a status (and body) the relay returns, or an error for no answer.
	answer func(fleetPushCall) (status int, body string, err error)
}

type fleetPushCall struct {
	device, kind, event, priority string
	token                         string // a Live Activity push's activity or push-to-start token
	state                         FleetState
}

func (s *fleetPushSink) RoundTrip(r *http.Request) (*http.Response, error) {
	var req relayproto.PushRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, err
	}
	_ = r.Body.Close()
	call := fleetPushCall{device: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer grant-"), kind: req.Type, priority: req.Priority,
		token: req.Token}
	call.event, _ = req.APS["event"].(string)
	if b, err := json.Marshal(req.APS["content-state"]); err == nil {
		_ = json.Unmarshal(b, &call.state)
	}
	s.mu.Lock()
	s.got = append(s.got, call)
	answer := s.answer
	s.mu.Unlock()
	status, body := http.StatusOK, `{"ok":true}`
	if answer != nil {
		var err error
		if status, body, err = answer(call); err != nil {
			return nil, err
		}
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func (s *fleetPushSink) count(device, kind, event string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, call := range s.got {
		if (device == "" || call.device == device) && call.kind == kind && call.event == event {
			n++
		}
	}
	return n
}

func localFleetLoop(t *testing.T) (*Hub, *fleetPushSink, func()) {
	t.Helper()
	return localFleetLoopWith(t, nil)
}

// localFleetLoopWith is localFleetLoop with prepare run on the hub after its devices are set up and before the loop
// starts: a test changes what the loop reads (h.store) here, not while it runs.
func localFleetLoopWith(t *testing.T, prepare func(h *Hub)) (*Hub, *fleetPushSink, func()) {
	t.Helper()
	h := newTestHub(t)
	h.store = newFleetStartStore()
	sink := &fleetPushSink{}
	h.push.relay = &http.Client{Transport: sink}
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"phone", "pad"} {
		if err := h.push.register(Device{DeviceID: id, Name: id, Kind: "iphone", APNSToken: id + "-device",
			Topic: "app.example", PushKey: base64.StdEncoding.EncodeToString(key.PublicKey().Bytes()),
			RelayURL: "https://relay.example", RelayGrant: "grant-" + id}); err != nil {
			t.Fatal(err)
		}
		h.live.update(id, func(v *db.LiveActivity) {
			v.ActivityID, v.PushToken, v.PushToStartToken, v.StartedAt = id+"-activity", id+"-update", id+"-start", time.Now()
		})
	}
	if prepare != nil {
		prepare(h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.live.run(ctx)
	}()
	synctest.Wait()
	return h, sink, func() { cancel(); <-done }
}

func putFleetPrefs(t *testing.T, h *Hub, device, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/devices/"+device+"/prefs", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("prefs: %d %s", w.Code, w.Body.String())
	}
	synctest.Wait()
}

func TestFleetPrefsPushOnlyChangedDevice(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"same", `{}`, 0},
		{"input", `{"input":false}`, 0},
		{"done", `{"done":"off"}`, 0},
		{"translation", `{"translate":{"enabled":true,"target":"ja","mail":true}}`, 0},
		{"details", `{"lock_details":true}`, 1},
		{"look", `{"look":{"design":"visor","light":1,"dark":2}}`, 1},
		{"island", `{"island":{"preset":"b"}}`, 1},
		{"disabled", `{"live_activity":false}`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				defer stop()
				reportLive(t, h, FleetState{Phase: "working", Working: 1}, FleetStats{TPM: 12000})
				synctest.Wait()
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				beforePhone, beforePad := sink.count("phone", "liveactivity", "update"), sink.count("pad", "liveactivity", "update")
				if beforePhone != 1 || beforePad != 1 {
					t.Fatalf("baseline updates: phone=%d pad=%d", beforePhone, beforePad)
				}
				putFleetPrefs(t, h, "phone", tc.body)
				phone := sink.count("phone", "liveactivity", "update") - beforePhone
				pad := sink.count("pad", "liveactivity", "update") - beforePad
				t.Logf("first prefs update: phone=%d other=%d", phone, pad)
				if phone != tc.want || pad != 0 {
					t.Errorf("first prefs update: phone=%d other=%d, want %d/0", phone, pad, tc.want)
				}
				beforePhone, beforePad = sink.count("phone", "liveactivity", "update"), sink.count("pad", "liveactivity", "update")
				putFleetPrefs(t, h, "phone", tc.body)
				phone = sink.count("phone", "liveactivity", "update") - beforePhone
				pad = sink.count("pad", "liveactivity", "update") - beforePad
				t.Logf("identical retry: phone=%d other=%d", phone, pad)
				if phone != 0 || pad != 0 {
					t.Errorf("identical prefs retry: phone=%d other=%d, want 0/0", phone, pad)
				}
			})
		})
	}
}

func TestFleetPushStartWaitsForLastPhoneToLeave(t *testing.T) {
	for _, clean := range []bool{true, false} {
		t.Run(fmt.Sprintf("clean=%v", clean), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				defer stop()
				h.live.update("phone", func(v *db.LiveActivity) { v.PushToken, v.ActivityID = "", "" })
				app := &client{out: make(chan []byte, 64), phone: true, pushDevice: "phone", clean: clean}
				other := &client{out: make(chan []byte, 64), phone: true, pushDevice: "phone", clean: clean}
				h.mu.Lock()
				h.clients[app], h.clients[other] = struct{}{}, struct{}{}
				h.mu.Unlock()
				working := FleetState{Phase: "working", Working: 1}
				reportLive(t, h, working, FleetStats{})
				synctest.Wait()
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				open := sink.count("phone", "liveactivity", "start")
				t.Logf("foreground starts=%d", open)
				if open != 0 {
					t.Errorf("push-start to foreground phone: %d, want 0", open)
				}
				h.dropClient(app)
				synctest.Wait()
				if n := sink.count("phone", "liveactivity", "start"); n != 0 {
					t.Errorf("push-start while another phone connection remains: %d", n)
				}
				h.dropClient(other)
				synctest.Wait()
				closed := sink.count("phone", "liveactivity", "start")
				t.Logf("last connection left: total starts=%d", closed)
				if closed != 1 {
					t.Errorf("active round lost its start opportunity: %d, want 1", closed)
				}
				h.live.changed()
				synctest.Wait()
				if n := sink.count("phone", "liveactivity", "start"); n != 1 {
					t.Errorf("same round started more than once: %d", n)
				}
				// Foreground suppression is only for the activity, never the ordinary alert.
				h.notifyInteraction("", proto.Interaction{ID: "ordinary", Kind: proto.IxPermission, Summary: "Approve the operation"}, "computer", false)
				synctest.Wait()
				if n := sink.count("", "alert", ""); n != 2 {
					t.Errorf("ordinary alert fanout=%d, want 2", n)
				}
			})
		})
	}
}

func TestFleetPrefsKeepsPendingFleetDelivery(t *testing.T) {
	for _, urgent := range []bool{false, true} {
		t.Run(fmt.Sprintf("urgent=%v", urgent), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				defer stop()
				one := FleetState{Phase: "working", Working: 1}
				reportLive(t, h, one, FleetStats{})
				synctest.Wait()
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				two := one
				two.Working = 2
				reportLive(t, h, two, FleetStats{})
				synctest.Wait()
				putFleetPrefs(t, h, "phone", `{"lock_details":true}`)
				if n := sink.count("pad", "liveactivity", "update"); n != 1 {
					t.Fatalf("settings advanced the other device's pending ordinary push: %d", n)
				}
				if urgent {
					two.Phase, two.NeedsYou = "needsYou", 1
					two.Items = []FleetItem{{ID: "m/a", Kind: proto.IxPermission, Interaction: "pending"}}
					reportLive(t, h, two, FleetStats{})
					synctest.Wait()
					sink.mu.Lock()
					defer sink.mu.Unlock()
					for _, device := range []string{"phone", "pad"} {
						var last fleetPushCall
						for _, call := range sink.got {
							if call.device == device && call.kind == "liveactivity" {
								last = call
							}
						}
						if last.priority != "10" || last.state.NeedsYou != 1 || last.state.Working != 2 {
							t.Errorf("settings swallowed %s's urgent latest state: %+v", device, last)
						}
					}
				} else {
					time.Sleep(fleetMinGap)
					synctest.Wait()
					if n := sink.count("pad", "liveactivity", "update"); n != 2 {
						t.Errorf("settings swallowed the other device's pending ordinary push: %d, want 2", n)
					}
				}
			})
		})
	}
}

func TestFleetPrefsPartialLookAndReenable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, sink, stop := localFleetLoop(t)
		defer stop()
		prefs := defaultPrefs()
		prefs.Look = &FleetLook{Design: "visor", Light: 1, Dark: 2}
		if err := h.push.setPrefs("phone", prefs); err != nil {
			t.Fatal(err)
		}
		reportLive(t, h, FleetState{Phase: "working", Working: 1}, FleetStats{})
		synctest.Wait()
		time.Sleep(fleetCoalesce)
		synctest.Wait()
		putFleetPrefs(t, h, "phone", `{"look":{"light":3}}`)
		if got := h.push.prefs("phone").Look; got == nil || got.Design != "visor" || got.Light != 3 || got.Dark != 2 {
			t.Fatalf("partial look lost its other fields: %+v", got)
		}
		if n := sink.count("phone", "liveactivity", "update"); n != 2 {
			t.Fatalf("partial look was not immediately refreshed: %d, want 2", n)
		}
		putFleetPrefs(t, h, "phone", `{"live_activity":false}`)
		putFleetPrefs(t, h, "phone", `{"live_activity":true}`)
		putFleetPrefs(t, h, "phone", `{"live_activity":true}`)
		if phone, pad := sink.count("phone", "liveactivity", "update"), sink.count("pad", "liveactivity", "update"); phone != 3 || pad != 1 {
			t.Errorf("re-enable needs one fresh target update, not a global resend: phone=%d pad=%d, want 3/1", phone, pad)
		}
	})
}

func TestFleetPushStartUsesStateAndTokenAtDeparture(t *testing.T) {
	for _, tc := range []string{"empty", "done", "reported_token", "expired_token"} {
		t.Run(tc, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h, sink, stop := localFleetLoop(t)
				defer stop()
				h.live.update("phone", func(v *db.LiveActivity) {
					v.ActivityID, v.PushToken = "", ""
					if tc == "expired_token" {
						v.ActivityID, v.PushToken, v.StartedAt = "expired", "expired-update", time.Now().Add(-fleetMaxAge)
					}
				})
				app := &client{out: make(chan []byte, 64), phone: true, pushDevice: "phone", clean: true}
				h.mu.Lock()
				h.clients[app] = struct{}{}
				h.mu.Unlock()
				reportLive(t, h, FleetState{Phase: "working", Working: 1}, FleetStats{})
				synctest.Wait()
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				switch tc {
				case "empty":
					reportLive(t, h, FleetState{}, FleetStats{})
				case "done":
					reportLive(t, h, FleetState{Phase: "done", Unread: 1}, FleetStats{})
				case "reported_token":
					h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "local", "local-update", time.Now() })
				}
				synctest.Wait()
				time.Sleep(fleetCoalesce)
				synctest.Wait()
				h.dropClient(app)
				synctest.Wait()
				want := 0
				if tc == "expired_token" {
					want = 1
				}
				if n := sink.count("phone", "liveactivity", "start"); n != want {
					t.Errorf("departure %s: starts=%d, want %d", tc, n, want)
				}
				if n := sink.count("phone", "liveactivity", "update"); n != 0 {
					t.Errorf("departure %s: unexpected updates=%d", tc, n)
				}
			})
		})
	}
}

func TestFleetMetricsKeepsNewestForegroundState(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, _, stop := localFleetLoop(t)
		defer stop()
		app := &client{out: make(chan []byte, 64), phone: true, pushDevice: "phone"}
		h.mu.Lock()
		h.clients[app] = struct{}{}
		h.mu.Unlock()
		one := FleetState{Phase: "working", Working: 1}
		reportLive(t, h, one, FleetStats{TPM: 100})
		synctest.Wait()
		time.Sleep(14 * time.Second)
		synctest.Wait()
		two := one
		two.Working = 2
		reportLive(t, h, two, FleetStats{TPM: 100})
		synctest.Wait()
		reportLive(t, h, two, FleetStats{TPM: 200})
		synctest.Wait()
		time.Sleep(time.Second) // Metrics tick, before the ordinary state's coalesced flush.
		synctest.Wait()
		var latest FleetState
		backwards, sawTwo := 0, false
		for len(app.out) > 0 {
			var event struct {
				Type  string     `json:"type"`
				Fleet FleetState `json:"fleet"`
			}
			if err := json.Unmarshal(<-app.out, &event); err != nil {
				t.Fatal(err)
			}
			if event.Type != "fleet" {
				continue
			}
			if sawTwo && event.Fleet.Working < 2 {
				backwards++
			}
			sawTwo = sawTwo || event.Fleet.Working == 2
			latest = event.Fleet
		}
		t.Logf("foreground backward transitions=%d latest working=%d", backwards, latest.Working)
		if backwards != 0 || latest.Working != 2 || latest.Stats == nil || latest.Stats.TPM != 200 {
			t.Fatalf("metrics replaced a newer foreground state: backwards=%d latest=%+v", backwards, latest)
		}
	})
}

func TestIslandSides(t *testing.T) {
	cases := []struct {
		p           IslandPrefs
		left, right string
	}{
		{IslandPrefs{}, SlotStatus, SlotStatus},
		{IslandPrefs{Preset: "a"}, SlotStatus, SlotStatus},
		{IslandPrefs{Preset: "b"}, SlotStatus, SlotTPM},
		{IslandPrefs{Preset: "c"}, SlotStatus, SlotTokens},
		{IslandPrefs{Preset: "custom", Left: SlotAssistant, Right: SlotLoad}, SlotAssistant, SlotLoad},
		{IslandPrefs{Preset: "custom", Left: "cost", Right: ""}, SlotStatus, SlotStatus},
	}
	for _, c := range cases {
		if l, r := c.p.sides(); l != c.left || r != c.right {
			t.Errorf("%+v: %s / %s, want %s / %s", c.p, l, r, c.left, c.right)
		}
	}
	if p := devicePrefs(json.RawMessage(`{"island":{"preset":"zz"}}`)); p.Island.Preset != "a" {
		t.Errorf("unknown preset kept: %+v", p.Island)
	}
}

// The device's persona rides in its pushes (the widget extension cannot read the app's settings); the JSON is what
// apple/Shared/FleetState.swift decodes. One that would bloat every push is dropped.
func TestFleetLookForDevice(t *testing.T) {
	look := &FleetLook{Design: "circuit", Light: 0xD4643F, Dark: 0xE8837C}
	d := forDevice(FleetState{Phase: "working", Working: 1}, NotifyPrefs{Look: look}, FleetStats{})
	b, _ := json.Marshal(d)
	if want := `"look":{"design":"circuit","light":13919295,"dark":15238012}`; !strings.Contains(string(b), want) {
		t.Fatalf("got %s, want %s in it", b, want)
	}
	if d := forDevice(FleetState{}, defaultPrefs(), FleetStats{}); d.Look != nil {
		t.Fatalf("a device that told nothing gets no look: %+v", d.Look)
	}
	p := devicePrefs(json.RawMessage(`{"look":{"design":"visor","light":1,"dark":2,"package":"mint/r1"}}`))
	if p.Look == nil || p.Look.Package != "mint/r1" {
		t.Fatalf("stored look: %+v", p.Look)
	}
	if p := devicePrefs(json.RawMessage(`{"look":{"design":"` + strings.Repeat("x", 40) + `"}}`)); p.Look != nil {
		t.Fatalf("an oversized look kept: %+v", p.Look)
	}
}

func TestFleetForDevice(t *testing.T) {
	stats := FleetStats{TPM: 12000, Tokens: 42000000, Requests: 220,
		Loads: []FleetLoad{{ID: "a", Name: "A", Percent: 30}, {ID: "b", Name: "B", Percent: 70}}}
	st := FleetState{Phase: "working", Working: 1, Assistant: true, Items: []FleetItem{{
		ID: "m/s", Kind: proto.IxPermission, Interaction: "ix", InteractionCreatedAt: "2026-11-25T01:02:03.123456789Z",
	}}}
	a := forDevice(st, defaultPrefs(), stats)
	if a.Stats != nil || a.Assistant || a.Left != SlotStatus || a.Right != SlotStatus {
		t.Fatalf("preset a: %+v", a)
	}
	if len(a.Items) != 1 || a.Items[0].Interaction != "" || a.Items[0].InteractionCreatedAt != "" {
		t.Fatalf("details-off item retains an actionable occurrence: %+v", a.Items)
	}
	b := forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "b"}, LockDetails: true}, stats)
	if b.Stats == nil || b.Stats.TPM != 12000 || b.Stats.Tokens != 0 || !b.Details {
		t.Fatalf("preset b: %+v %+v", b, b.Stats)
	}
	c := forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "custom", Left: SlotAssistant, Right: SlotLoad}}, stats)
	if !c.Assistant || c.Stats == nil || len(c.Stats.Loads) != 1 || c.Stats.Loads[0].ID != "b" {
		t.Fatalf("custom without a machine shows the busiest: %+v %+v", c, c.Stats)
	}
	c = forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotLoad, Machine: "a"}}, stats)
	if c.Stats.Loads[0].ID != "a" {
		t.Fatalf("chosen machine: %+v", c.Stats)
	}
}

func TestLiveActivityMetrics(t *testing.T) {
	h, f := testLive(t)
	la := h.live
	now := time.Now()
	// The island's numbers as the manager works them out from the nodes' usage (fleet.live).
	report := func(out int64) {
		st, _ := h.fleetLive()
		reportLive(t, h, st, FleetStats{TPM: out / 2, Requests: 1, Loads: []FleetLoad{{ID: "m", Name: "m", Percent: 25}}})
	}
	report(25000)
	la.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", now })
	_ = h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "b"}})
	working := FleetState{Phase: "working", Working: 1}
	la.flush(working, false, false)
	c := f.calls(t, 1)
	if c[0].State.Right != SlotTPM || c[0].State.Stats == nil || c[0].State.Stats.TPM == 0 {
		t.Fatalf("state carries the TPM: %+v", c[0].State)
	}
	// The assistant starting is not on this device's island: no push.
	la.flush(FleetState{Phase: "working", Working: 1, Assistant: true}, false, false)
	// Numbers alone: not before live_metrics_seconds, and not when the rounded value is the same.
	report(50000)
	la.metrics(working, now.Add(10*time.Second))
	la.metrics(working, now.Add(61*time.Second))
	c = f.calls(t, 2)
	if c[1].Priority != "5" || c[1].State.Stats.TPM <= c[0].State.Stats.TPM {
		t.Fatalf("metrics push %+v %+v", c[1], c[1].State.Stats)
	}
	la.metrics(working, now.Add(200*time.Second))
	time.Sleep(50 * time.Millisecond)
	f.calls(t, 2)
}

func TestCoalescerNeedsYouBothWays(t *testing.T) {
	var c coalescer
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Kind: "working"}}}
	asks := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 1,
		Items: []FleetItem{{ID: "m/b", Kind: proto.IxQuestion, Interaction: "ix1"}, {ID: "m/a", Kind: "working"}}}
	c.sentState(working)
	if now, _ := c.offer(asks); !now {
		t.Fatal("a request arriving goes out at once")
	}
	c.sentState(asks)
	// Answered (here or on the computer): the island must stop saying so at once, not after the gap.
	if now, _ := c.offer(working); !now {
		t.Fatal("a request answered goes out at once")
	}
	c.sentState(working)
	more := working
	more.Working = 2
	if now, sched := c.offer(more); now || !sched {
		t.Fatalf("an ordinary change waits: %v %v", now, sched)
	}
}

// A card with fields names no interaction on its row (the assistant's fleet_live.go): another such card of the same
// agent, after the first was answered, is still a new request and goes out at once.
func TestCoalescerNewCardWithFieldsOfTheSameAgent(t *testing.T) {
	var c coalescer
	first := FleetState{Phase: "needsYou", NeedsYou: 1, Items: []FleetItem{{ID: "m/p", Kind: proto.IxPermission, Since: 1761700000}}}
	c.sentState(first)
	second := first
	second.Items = []FleetItem{{ID: "m/p", Kind: proto.IxPermission, Since: 1761700042}}
	if now, sched := c.offer(second); !now {
		t.Fatalf("a new card goes out at once: now=%v schedule=%v", now, sched)
	}
}

// Past fleetMaxItems the items are the same three agents whether three or four need the user: a fourth arriving, or
// one past the three answered, changes only NeedsYou. The island's "N need you" must follow at once all the same
// (#4604), not after fleetMinGap at priority 5.
func TestCoalescerNeedsYouCountPastTheItems(t *testing.T) {
	var c coalescer
	three := FleetState{Phase: "needsYou", NeedsYou: 3, Items: []FleetItem{
		{ID: "m/a", Kind: proto.IxQuestion, Interaction: "ix1"},
		{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix2"},
		{ID: "m/c", Kind: proto.IxPlan, Interaction: "ix3"},
	}}
	c.sentState(three)
	four := three
	four.NeedsYou = 4
	if now, sched := c.offer(four); !now {
		t.Fatalf("a fourth request goes out at once: now=%v schedule=%v", now, sched)
	}
	c.sentState(four)
	if now, sched := c.offer(three); !now {
		t.Fatalf("the fourth answered goes out at once: now=%v schedule=%v", now, sched)
	}
	c.sentState(three)
	busier := three
	busier.Working = 2
	if now, sched := c.offer(busier); now || !sched {
		t.Fatalf("an ordinary change waits: %v %v", now, sched)
	}
}

// fleetClock gives h.live a clock the test moves, and the device "phone" a running activity.
func fleetClock(t *testing.T) (*Hub, *fakeAPNs, *time.Time) {
	h, f := testLive(t)
	clock := time.Unix(1_800_000_000, 0)
	h.live.now = func() time.Time { return clock }
	h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", clock })
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true}); err != nil {
		t.Fatal(err)
	}
	return h, f, &clock
}

func TestFleetMinGap(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	working := func(n int) FleetState {
		return FleetState{Phase: "working", Working: n, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	}
	t0 := *clock
	if next := la.flush(working(1), false, false); !next.IsZero() {
		t.Fatalf("first change waits for nothing: %v", next)
	}
	f.calls(t, 1)

	// An ordinary change 5 s later: its turn is fleetMinGap after the last push.
	*clock = t0.Add(5 * time.Second)
	if next := la.flush(working(2), false, false); !next.Equal(t0.Add(fleetMinGap)) {
		t.Fatalf("deferred until %v, want %v", next, t0.Add(fleetMinGap))
	}
	// A request arriving does not wait, and carries what changed meanwhile.
	*clock = t0.Add(8 * time.Second)
	asks := working(2)
	asks.Phase, asks.NeedsYou = "needsYou", 1
	asks.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix1"}}, asks.Items...)
	la.flush(asks, true, false)
	c := f.calls(t, 2)
	if c[1].Priority != "10" || c[1].State.NeedsYou != 1 || c[1].State.Working != 2 {
		t.Fatalf("request push %+v", c[1])
	}
	// Its turn come, the deferred device gets the state as it is then.
	*clock = t0.Add(8*time.Second + fleetMinGap)
	if next := la.flush(working(3), false, false); !next.IsZero() {
		t.Fatalf("due: %v", next)
	}
	c = f.calls(t, 3)
	if c[2].Priority != "5" || c[2].State.Working != 3 {
		t.Fatalf("ordinary push %+v", c[2])
	}
}

// TestFleetPacingBound runs the run loop's decisions (coalescer, flush and its next) over a fleet that changes every
// 3 s for 10 minutes, a request arriving and being answered now and then: every change reaches the device within
// fleetMinGap + fleetCoalesce (it, or a later state), a request arriving or answered at once, and far fewer pushes go
// out than there were changes.
func TestFleetPacingBound(t *testing.T) {
	h, _, clock := fleetClock(t)
	la := h.live
	start := *clock
	end := start.Add(10 * time.Minute)
	type change struct {
		at       time.Time
		critical bool
	}
	var changes []change
	var pushedAt []time.Time // each push, and the last change it carried
	var pushedUpTo []int
	var truth FleetState
	var due time.Time // the run loop's timer
	pushed := func() bool {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt["phone"].Equal(*clock)
	}
	flush := func(urgent bool) {
		if next := la.flush(truth, urgent, false); !next.IsZero() && (due.IsZero() || next.Before(due)) {
			due = next
		}
		if pushed() && (len(pushedAt) == 0 || !pushedAt[len(pushedAt)-1].Equal(*clock) || pushedUpTo[len(pushedUpTo)-1] < len(changes)-1) {
			pushedAt, pushedUpTo = append(pushedAt, *clock), append(pushedUpTo, len(changes)-1)
		}
	}
	for step := 0; clock.Before(end); step++ {
		truth = FleetState{Phase: "working", Working: 1 + step%4, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
		if step%40 >= 20 && step%40 < 27 { // a request for 21 s, then answered
			truth.Phase, truth.NeedsYou = "needsYou", 1
			truth.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxQuestion, Interaction: fmt.Sprintf("ix%d", step/40)}}, truth.Items...)
		}
		now, schedule := la.coal.offer(truth)
		changes = append(changes, change{*clock, now})
		switch {
		case now:
			flush(true)
		case schedule:
			if at := clock.Add(fleetCoalesce); due.IsZero() || at.Before(due) {
				due = at
			}
		}
		next := clock.Add(3 * time.Second) // time passes to the next change, the timer going off on the way
		for !due.IsZero() && !due.After(next) {
			*clock, due = due, time.Time{}
			flush(false)
		}
		*clock = next
	}
	bound := fleetMinGap + fleetCoalesce
	worst := time.Duration(0)
	for i, c := range changes {
		if c.at.After(end.Add(-bound)) {
			break // still within its bound when the run ended
		}
		k := sort.Search(len(pushedUpTo), func(k int) bool { return pushedUpTo[k] >= i })
		if k == len(pushedUpTo) {
			t.Fatalf("change %d at %v never reached the device", i, c.at.Sub(start))
		}
		lag := pushedAt[k].Sub(c.at)
		if c.critical && lag != 0 {
			t.Fatalf("a request change at %v waited %v", c.at.Sub(start), lag)
		}
		worst = max(worst, lag)
	}
	if worst > bound {
		t.Fatalf("a change reached the device after %v, bound %v", worst, bound)
	}
	if len(pushedAt) > len(changes)/4 {
		t.Fatalf("%d pushes for %d changes: the gap does not hold", len(pushedAt), len(changes))
	}
	t.Logf("%d changes in 10 min: %d pushes, the slowest change after %v (bound %v)", len(changes), len(pushedAt), worst, bound)
}

func TestFleetOpenPhone(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()

	// Open: the app draws the activity from the fleet event; no push, but the hub knows what it shows.
	la.broadcast(working)
	la.flush(working, true, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed to an open app: %d", n)
	}
	// Gone to the background (a clean close): it shows the last fleet, nothing to send.
	app.clean = true
	h.dropClient(app)
	la.flush(working, false, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed what the app already showed: %d", n)
	}
	la.mu.Lock()
	repair := la.repair
	la.mu.Unlock()
	if repair {
		t.Fatal("a clean close needs no repair")
	}

	// Open again, then the network drops (no clean close): what it shows is unknown, the state goes at once although
	// the last update was moments ago.
	app2 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app2] = struct{}{}
	h.mu.Unlock()
	more := working
	more.Working = 2
	la.broadcast(more)
	la.flush(more, false, false)
	*clock = clock.Add(time.Second)
	h.dropClient(app2)
	la.mu.Lock()
	repair = la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("a dropped connection should repair the activity")
	}
	if next := la.flush(more, false, false); !next.IsZero() {
		t.Fatalf("repair waited for the gap: %v", next)
	}
	if c := f.calls(t, 1); c[0].State.Working != 2 {
		t.Fatalf("repair push %+v", c[0])
	}

	// A clean close long after the last fleet event: the activity may have gone stale, push it.
	app3 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone", clean: true}
	h.mu.Lock()
	h.clients[app3] = struct{}{}
	h.mu.Unlock()
	*clock = clock.Add(fleetRefresh + time.Minute)
	h.dropClient(app3)
	la.mu.Lock()
	repair = la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("a stale activity should be refreshed when the app leaves")
	}
}

func TestSessionsSentOnce(t *testing.T) {
	h := newTestHub(t)
	app := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, role: roleFull}
	addTestClient(h, app)
	box, mini := &Machine{cfg: MachineConfig{ID: "box"}}, &Machine{cfg: MachineConfig{ID: "mini"}}
	a := []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "reading"}}
	b := []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "writing"}}
	h.broadcastSessions(box, a)
	h.broadcastSessions(box, a) // the same again (an interaction shown, a session seen): not sent
	h.broadcastSessions(mini, a)
	h.broadcastSessions(box, b)
	h.broadcastSessions(box, a) // back to the first: the apps have b, so it goes
	var got []string
	for len(app.out) > 0 {
		var m struct {
			Machine  string          `json:"machine_id"`
			Sessions []proto.Session `json:"sessions"`
		}
		_ = json.Unmarshal(<-app.out, &m)
		got = append(got, m.Machine+":"+m.Sessions[0].LastText)
	}
	if want := []string{"box:reading", "mini:reading", "box:writing", "box:reading"}; !slices.Equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

// TestFleetRefreshPerDevice: each device's activity is refreshed fleetRefresh after its own last push, so it never
// shows "waiting for the hub" while the hub is up (#4154). A change only another device shows (the assistant
// answering, on an island that does not show it) is no push for this one and must not put its refresh off.
func TestFleetRefreshPerDevice(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	h.push.register(Device{DeviceID: "pad", Name: "pad", APNSToken: "padtok", Topic: "app.example"})
	la.update("pad", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act2", "padact", *clock })
	if err := h.push.setPrefs("pad", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true,
		Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotAssistant}}); err != nil {
		t.Fatal(err)
	}
	sentAt := func(device string) time.Time {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt[device]
	}
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	t0 := *clock
	reportLive(t, h, working, FleetStats{})
	la.flush(working, false, false)
	f.calls(t, 2)

	// 6 minutes on, the assistant answers: only the pad's island shows it.
	*clock = t0.Add(6 * time.Minute)
	answering := working
	answering.Assistant = true
	reportLive(t, h, answering, FleetStats{})
	la.flush(answering, false, false)
	f.calls(t, 3)
	if !sentAt("phone").Equal(t0) || !sentAt("pad").Equal(*clock) {
		t.Fatalf("after the assistant: phone %v, pad %v", sentAt("phone").Sub(t0), sentAt("pad").Sub(t0))
	}

	// The run loop's tick every 15 s: the phone is refreshed fleetRefresh after its own push, the pad after its own.
	refreshed := map[string]time.Time{}
	for *clock = t0.Add(6*time.Minute + 15*time.Second); !clock.After(t0.Add(fleetStaleIn + 2*time.Minute)); *clock = clock.Add(15 * time.Second) {
		la.tick()
		for _, d := range []string{"phone", "pad"} {
			if _, ok := refreshed[d]; !ok && sentAt(d).Equal(*clock) {
				refreshed[d] = *clock
			}
		}
	}
	for d, last := range map[string]time.Time{"phone": t0, "pad": t0.Add(6 * time.Minute)} {
		at, ok := refreshed[d]
		if !ok {
			t.Fatalf("%s never refreshed (last push at %v)", d, last.Sub(t0))
		}
		if wait := at.Sub(last); wait < fleetRefresh || wait > fleetRefresh+15*time.Second {
			t.Fatalf("%s refreshed %v after its last push, want about %v (stale after %v)", d, wait, fleetRefresh, fleetStaleIn)
		}
	}
	time.Sleep(50 * time.Millisecond)
	c := f.calls(t, 5)
	if len(c) != 5 {
		t.Fatalf("want one refresh per device, got %d pushes: %+v", len(c), c)
	}
	for _, x := range c[3:] {
		// Each device gets its own version: the pad's island shows the assistant, the phone's does not.
		if x.Event != "update" || x.Priority != "5" || x.State.Working != 1 || x.State.Assistant != (x.Token == "padact") {
			t.Fatalf("refresh %+v", x)
		}
	}
}

// TestFleetAssistantHoldsActivity: only the assistant answering is something to show on an island that shows it
// ("Thinking"): the hub pushes it there and does not end that activity while it answers, nor until fleetEndAfter after it
// stopped. A device whose island does not show the assistant ends as with an empty fleet, and none is started for it
// (#4398).
func TestFleetAssistantHoldsActivity(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	h.push.register(Device{DeviceID: "pad", Name: "pad", APNSToken: "padtok", Topic: "app.example"})
	la.update("pad", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act2", "padact", *clock })
	showing := NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotAssistant}}
	if err := h.push.setPrefs("pad", showing); err != nil {
		t.Fatal(err)
	}
	// pushes counts the fake APNs calls to token with event, waiting up to 2 s for want of them (pushes go out on
	// their own goroutines). want 0 cannot be waited for: it gives a push in flight 50 ms to show up.
	pushes := func(token, event string, want int) (n int, last apnsCall) {
		count := func() (n int, last apnsCall) {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, c := range f.got {
				if c.Token == token && c.Event == event {
					n, last = n+1, c
				}
			}
			return n, last
		}
		if want == 0 {
			time.Sleep(50 * time.Millisecond)
			return count()
		}
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if n, last = count(); n >= want || time.Now().After(deadline) {
				return n, last
			}
		}
	}
	t0 := *clock
	la.flush(FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}, false, false)
	f.calls(t, 2)
	// A device that could have one push-started: whether only the assistant should start one is not decided yet.
	h.push.register(Device{DeviceID: "mini", Name: "mini", APNSToken: "minitok", Topic: "app.example"})
	la.update("mini", func(v *db.LiveActivity) { v.PushToStartToken = "minip2s" })
	if err := h.push.setPrefs("mini", showing); err != nil {
		t.Fatal(err)
	}

	// A minute on, the agent is done and seen, and the assistant answers: the pad shows it, the phone has nothing.
	*clock = t0.Add(time.Minute)
	answering := FleetState{Assistant: true}
	reportLive(t, h, answering, FleetStats{})
	la.flush(answering, false, false)
	if n, c := pushes("padact", "update", 2); n != 2 || !c.State.Assistant || c.State.Phase != "" {
		t.Fatalf("pad: %d updates, last %+v", n, c)
	}
	if n, _ := pushes("acttok", "update", 1); n != 1 {
		t.Fatalf("phone: %d updates, want only the first", n)
	}

	// It answers for a quarter of an hour: the phone's activity ends after fleetEndAfter, the pad's stays, refreshed.
	for *clock = clock.Add(15 * time.Second); clock.Before(t0.Add(time.Minute + fleetStaleIn)); *clock = clock.Add(15 * time.Second) {
		la.tick()
	}
	if n, _ := pushes("acttok", "end", 1); n != 1 {
		t.Fatalf("phone: %d ends, want 1", n)
	}
	if n, _ := pushes("padact", "end", 0); n != 0 || la.rows["pad"].PushToken != "padact" {
		t.Fatal("the pad's activity ended while the assistant answers")
	}
	if n, c := pushes("padact", "update", 3); n != 3 || !c.State.Assistant {
		t.Fatalf("pad: %d updates, want a refresh before it goes stale; last %+v", n, c)
	}
	if n, _ := pushes("minip2s", "start", 0); n != 0 {
		t.Fatal("only the assistant answering started an activity")
	}

	// It stops: the pad's activity ends fleetEndAfter later, as an emptied fleet's does.
	stopped := *clock
	reportLive(t, h, FleetState{}, FleetStats{})
	la.flush(FleetState{}, false, false)
	for ; clock.Before(stopped.Add(fleetEndAfter)); *clock = clock.Add(15 * time.Second) {
		la.tick()
	}
	if n, _ := pushes("padact", "end", 0); n != 0 {
		t.Fatal("the pad's activity ended at once when the assistant stopped")
	}
	la.tick()
	if n, _ := pushes("padact", "end", 1); n != 1 {
		t.Fatalf("pad: %d ends fleetEndAfter after the assistant stopped, want 1", n)
	}
}

// TestFleetClearsWhenTheLastRequestGoes: the last request answered on a computer, or gone, empties the fleet. The
// running activities lose the request and its buttons at once (priority 10, no items), not when they end a minute
// later; they still end only fleetEndAfter on, since an early end would leave the next request no activity to open
// (push-starts are rationed). An island showing the assistant answering keeps it, without the request.
func TestFleetClearsWhenTheLastRequestGoes(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	h.push.register(Device{DeviceID: "pad", Name: "pad", APNSToken: "padtok", Topic: "app.example"})
	la.update("pad", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act2", "padact", *clock })
	showing := NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotAssistant}}
	if err := h.push.setPrefs("pad", showing); err != nil {
		t.Fatal(err)
	}
	// A device with only a push-to-start token gets no activity started to show that nothing is left.
	h.push.register(Device{DeviceID: "mini", Name: "mini", APNSToken: "minitok", Topic: "app.example"})
	if err := h.push.setPrefs("mini", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true}); err != nil {
		t.Fatal(err)
	}
	// last is the latest of the n calls (waited for) that went to token with event, and how many did.
	last := func(n int, token, event string) (count int, c apnsCall) {
		for _, call := range f.calls(t, n) {
			if call.Token == token && call.Event == event {
				count, c = count+1, call
			}
		}
		return count, c
	}
	// offer and flush as the run loop does: the coalescer decides the urgency.
	deliver := func(st FleetState) {
		reportLive(t, h, st, FleetStats{})
		urgent, _ := la.coal.offer(st)
		la.flush(st, urgent, false)
	}

	t0 := *clock
	asks := FleetState{Phase: "needsYou", NeedsYou: 1, Assistant: true,
		Items: []FleetItem{{ID: "m/b", Title: "api", Kind: proto.IxPermission, Interaction: "ix1", Detail: "Bash: go test"}}}
	deliver(asks)
	f.calls(t, 2)
	la.update("mini", func(v *db.LiveActivity) { v.PushToStartToken = "minip2s" })

	// Answered on the computer 10 s later, with the assistant still answering: both running activities are cleared now.
	*clock = t0.Add(10 * time.Second)
	deliver(FleetState{Assistant: true})
	if n, c := last(4, "acttok", "update"); n != 2 || c.Priority != "10" || c.State.Phase != "" || c.State.NeedsYou != 0 || len(c.State.Items) != 0 || c.State.Assistant {
		t.Fatalf("phone: %d updates, want the request cleared at once; last %+v", n, c)
	}
	if n, c := last(4, "padact", "update"); n != 2 || c.Priority != "10" || len(c.State.Items) != 0 || !c.State.Assistant {
		t.Fatalf("pad: %d updates, want the request cleared and the assistant kept; last %+v", n, c)
	}
	la.mu.Lock()
	_, started := la.started["mini"]
	la.mu.Unlock()
	if started {
		t.Fatal("clearing started an activity")
	}

	// Nothing comes back: the phone's activity ends fleetEndAfter on, not earlier (ending forgets its token at once).
	for *clock = clock.Add(15 * time.Second); clock.Before(t0.Add(10*time.Second + fleetEndAfter)); *clock = clock.Add(15 * time.Second) {
		la.tick()
	}
	la.mu.Lock()
	phone, pad := la.rows["phone"].PushToken, la.rows["pad"].PushToken
	la.mu.Unlock()
	if phone != "acttok" || pad != "padact" {
		t.Fatalf("an activity ended before fleetEndAfter: phone %q, pad %q", phone, pad)
	}
	la.tick()
	if n, _ := last(5, "acttok", "end"); n != 1 {
		t.Fatalf("phone: %d ends fleetEndAfter after the request went, want 1", n)
	}
	// Nothing else in that minute; the pad's activity stays while the assistant answers.
	if n := len(f.calls(t, 5)); n != 5 || la.rows["pad"].PushToken != "padact" {
		t.Fatalf("%d pushes, want 5 (two updates each, the phone's end); pad token %q", n, la.rows["pad"].PushToken)
	}
}

// An ordinary change that empties the fleet (the last agent done and seen) is not urgent: no clearing push, the
// activity ends fleetEndAfter on as before.
func TestFleetEmptiedWithoutARequestPushesOnlyTheEnd(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	la.coal.offer(working)
	la.flush(working, false, false)
	f.calls(t, 1)
	*clock = clock.Add(10 * time.Second)
	if urgent, _ := la.coal.offer(FleetState{}); urgent {
		t.Fatal("an agent finishing is not urgent")
	}
	la.flush(FleetState{}, false, false)
	*clock = clock.Add(fleetEndAfter)
	la.tick()
	if c := f.calls(t, 2); len(c) != 2 || c[1].Event != "end" {
		t.Fatalf("want only the end after the first update, got %+v", c)
	}
}

// TestFleetBackgroundRefreshFromLastUpdate: an app that goes to the background may have left its activity's
// stale-date as an earlier update set it (it does not update for a fleet that changes nothing it shows, nor for a
// settings change), so the hub is only sure of fleetOpenFresh after the last fleet event: the refresh comes by then,
// not fleetRefresh after the event (#4294).
func TestFleetBackgroundRefreshFromLastUpdate(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()
	sentAt := func() time.Time {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt["phone"]
	}

	// 0:00 the app drew a fleet that, as far as the hub knows, changed nothing: its stale-date may be as near as 5:00
	// (an earlier update at -10:00, LiveActivityController.worthUpdating).
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	t0 := *clock
	la.broadcast(working)
	la.flush(working, false, false)

	// 3:00 gone to the background: what it shows is known, nothing to send now.
	*clock = t0.Add(3 * time.Minute)
	app.clean = true
	h.dropClient(app)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed what the app already showed: %d", n)
	}

	// The run loop's tick: the refresh comes before 5:00.
	var refreshed time.Time
	for *clock = clock.Add(15 * time.Second); !clock.After(t0.Add(fleetStaleIn)); *clock = clock.Add(15 * time.Second) {
		la.tick()
		if sentAt().Equal(*clock) {
			refreshed = *clock
			break
		}
	}
	if refreshed.IsZero() {
		t.Fatal("never refreshed")
	}
	if at := refreshed.Sub(t0); at >= 5*time.Minute {
		t.Fatalf("refreshed %v after the last fleet event, want before 5m0s (the stale-date it may have)", at)
	}
	if c := f.calls(t, 1); c[0].Event != "update" || c[0].State.Working != 1 {
		t.Fatalf("refresh %+v", c[0])
	}

	// Gone to the background fleetOpenFresh or more after the last fleet event: the state goes at once.
	app2 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone", clean: true}
	h.mu.Lock()
	h.clients[app2] = struct{}{}
	h.mu.Unlock()
	la.broadcast(working)
	*clock = clock.Add(fleetOpenFresh)
	h.dropClient(app2)
	la.mu.Lock()
	repair := la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("an activity that may be near its stale-date should be refreshed when the app leaves")
	}
}
