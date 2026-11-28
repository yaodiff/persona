package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type outageTestDevices struct {
	fakeDevices
	hub          *fakeHub
	mu           sync.Mutex
	capacity     proto.CapacitySnapshot
	gatewayRetry func(source, provider string, now time.Time) time.Time
}

func (d *outageTestDevices) Has(string) bool { return false }

func (d *outageTestDevices) List() []devices.Summary {
	d.hub.mu.Lock()
	defer d.hub.mu.Unlock()
	seen := map[string]bool{}
	out := make([]devices.Summary, 0, len(d.hub.devices)+len(d.hub.sessions))
	for _, dev := range d.hub.devices {
		seen[dev.ID] = true
		out = append(out, devices.Summary{
			ID: dev.ID, Name: dev.Name, Home: dev.Home, Online: dev.Online, Reachable: dev.Reachable,
		})
	}
	for id := range d.hub.sessions {
		if !seen[id] {
			seen[id] = true
			out = append(out, devices.Summary{ID: id, Name: id, Home: "/home/u", Online: true, Reachable: true})
		}
	}
	return out
}

func (d *outageTestDevices) Summary(id string) (devices.Summary, bool) {
	for _, s := range d.List() {
		if s.ID == id {
			return s, true
		}
	}
	return devices.Summary{}, false
}

func (d *outageTestDevices) Shown(id string) (devices.Shown, bool) {
	if _, ok := d.Summary(id); ok {
		return devices.Shown{Listed: true}, true
	}
	return devices.Shown{}, false
}

func (d *outageTestDevices) Sessions(id string) ([]proto.Session, bool) {
	d.hub.mu.Lock()
	defer d.hub.mu.Unlock()
	list, ok := d.hub.sessions[id]
	if !ok {
		return nil, false
	}
	return slices.Clone(list), true
}

func (d *outageTestDevices) AppendSessions(dst []proto.Session, id string) ([]proto.Session, bool) {
	list, ok := d.Sessions(id)
	if !ok {
		return dst, false
	}
	return append(dst, list...), true
}

func (d *outageTestDevices) SessionsWhere(id string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	list, ok := d.Sessions(id)
	if !ok {
		return nil, false
	}
	var out []proto.Session
	for i := range list {
		if keep(&list[i]) {
			out = append(out, list[i])
		}
	}
	return out, true
}

func (d *outageTestDevices) Session(id, sid string) (proto.Session, bool) {
	list, ok := d.Sessions(id)
	if !ok {
		return proto.Session{}, false
	}
	for _, s := range list {
		if s.SessionID == sid {
			return s, true
		}
	}
	return proto.Session{}, false
}

func (d *outageTestDevices) CapacitySnapshot(context.Context) proto.CapacitySnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.capacity
}

func (d *outageTestDevices) setCapacity(snap proto.CapacitySnapshot) {
	d.mu.Lock()
	d.capacity = snap
	d.mu.Unlock()
}

func (d *outageTestDevices) GatewayRetryAt(source, provider string, now time.Time) time.Time {
	d.mu.Lock()
	fn := d.gatewayRetry
	d.mu.Unlock()
	if fn != nil {
		return fn(source, provider, now)
	}
	return time.Time{}
}

func newOutagePlanStore() Store {
	return planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
}

func newOutageSupervisedService(t *testing.T, st Store) (*Service, *fakeHub, *outageTestDevices, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	d := &outageTestDevices{hub: f}
	s.devs = d
	f.judge = `{"verdict":"done","reason":"PR merged"}`
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Fix login crash", "prompt": "fix login crash and add tests", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "repo is on box", "done_when": []string{"PR merged to main"},
		"expect_minutes": 30,
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "supervised task agent linked", func() bool {
		todos := s.todosOf(context.Background(), "*")
		return len(todos) == 1 && len(todos[0].Agents) > 0
	})
	todo := s.todosOf(context.Background(), "*")[0]
	f.setSessions("box", proto.Session{
		MachineID:     "box",
		SessionID:     todo.Agents[0].SessionID,
		Agent:         "claude",
		Status:        proto.StatusDone,
		State:         proto.StateUnread,
		TurnError:     "503 Service Unavailable: no eligible upstream account",
		TurnErrorCode: proto.TurnErrorUpstreamUnavailable,
		LastText:      "partial work before provider outage",
		UpdatedAt:     time.Now().Add(-time.Second),
	})
	return s, f, d, todo
}

func pendingOutageSignal(s *Service, todoID string) (superviseSignal, bool) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.pending[todoID+"|"+sigOutageRecovery]
	return g, ok
}

func TestGoldenOutageRecoveryLanguages(t *testing.T) {
	for _, tc := range []struct {
		locale, lang string
	}{
		{"en", "English"},
		{"zh-Hans", ""},
		{"ja", "Japanese"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			got := prompts.RenderOutageRecovery(prompts.OutageRecovery{
				Language:     tc.lang,
				Provider:     "claude",
				Suppressed:   3,
				Interrupted:  2,
				Unjudged:     1,
				LatestState:  outageStateLabel(tc.lang, "stopped_error"),
				LatestDetail: "503 Service Unavailable: no eligible upstream account",
			})
			golden(t, "outage_recovery."+tc.locale, got)
		})
	}
}

func TestOutageCoalescesInterruptedAndUnjudgedIntoOneRecoveryDigest(t *testing.T) {
	s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
	ctx := context.Background()
	f.hello.Locale = "en"

	// Outage 1: provider-wide saturation with typed future recovery metadata.
	recovery1 := time.Now().Add(10 * time.Minute)
	d.setCapacity(saturated("claude", recovery1, false))

	s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
	s.superviseJudged(ctx, todo, time.Now().Add(-2*time.Minute), taskDoneCheck{}, errors.New("503 Service Unavailable: upstream attempt budget exhausted"))
	s.taskNotice(ctx, todo, "box", "interrupted", "", "529 overloaded: no other eligible account")

	// Send pass during the active outage must NOT wake the supervisor.
	s.sendDigest(ctx)
	if got := len(f.notices()); got != 0 {
		t.Fatalf("expected 0 supervisor wakes during outage, got %d:\n%v", got, f.notices())
	}
	held, ok := pendingOutageSignal(s, todo.ID)
	if !ok || held.Outage == nil {
		t.Fatalf("expected pending sigOutageRecovery for %s", todo.ID)
	}
	if held.Outage.Suppressed != 3 || held.Outage.Interrupted != 2 || held.Outage.Unjudged != 1 {
		t.Fatalf("unexpected coalesced counts: %+v", held.Outage)
	}

	// Reach recovery point and deliver one coalesced recovery digest.
	s.supervise.mu.Lock()
	cur := s.supervise.pending[todo.ID+"|"+sigOutageRecovery]
	cur.NotBefore = time.Now().Add(-time.Millisecond)
	s.supervise.pending[todo.ID+"|"+sigOutageRecovery] = cur
	s.supervise.mu.Unlock()
	d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
		Provider: "claude", Accounts: 2, Available: 1,
	}}})

	s.sendDigest(ctx)
	waitFor(t, "one coalesced recovery digest", func() bool { return len(f.notices()) == 1 })
	msg := entryLine(f.notices()[0])
	for _, want := range []string{
		todo.ID,
		todo.Title,
		"signal=" + sigOutageRecovery,
		"coalesced 3 suppressed signal(s)",
		"2 interrupted",
		"1 unjudged",
		"Latest state:",
		"529 overloaded: no other eligible account",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("recovery digest missing %q:\n%s", want, msg)
		}
	}

	// Outage 2 (later, distinct outage): handled as a new event with a fresh suppressed count.
	recovery2 := time.Now().Add(10 * time.Minute)
	d.setCapacity(saturated("claude", recovery2, false))
	s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
	s.superviseJudged(ctx, todo, time.Now().Add(-time.Minute), taskDoneCheck{}, errors.New("503 Service Unavailable: no eligible upstream account"))
	s.sendDigest(ctx)
	if got := len(f.notices()); got != 1 {
		t.Fatalf("expected no wake during second outage, got %d notices", got)
	}
	held2, ok := pendingOutageSignal(s, todo.ID)
	if !ok || held2.Outage == nil || held2.Outage.Suppressed != 2 {
		t.Fatalf("expected fresh second outage hold with suppressed=2, got ok=%v %+v", ok, held2.Outage)
	}
	if held2.Key == held.Key {
		t.Fatalf("distinct outage reused previous key %q", held.Key)
	}

	// Positive early recovery for Outage 2 releases immediately without waiting for recovery2.
	d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
		Provider: "claude", Accounts: 2, Available: 2,
	}}})
	s.superviseWatch(ctx, time.Now())
	s.sendDigest(ctx)
	waitFor(t, "second distinct outage recovery digest", func() bool { return len(f.notices()) == 2 })
	msg2 := entryLine(f.notices()[1])
	if !strings.Contains(msg2, "signal="+sigOutageRecovery) || !strings.Contains(msg2, "coalesced 2 suppressed signal(s)") {
		t.Fatalf("second outage digest unexpected:\n%s", msg2)
	}
}

func TestOutageHoldPreservesOwnerDecisionAndUnrelatedFailures(t *testing.T) {
	s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
	ctx := context.Background()
	f.hello.Locale = "en"

	// Create a second task on codex to verify provider scoping.
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Codex task", "prompt": "run codex task", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "codex work", "done_when": []string{"done"},
	}}})
	if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
		t.Fatal(err)
	}
	var codexTodo Todo
	for _, item := range s.todosOf(ctx, "*") {
		if item.ID != todo.ID {
			codexTodo = item
		}
	}
	f.setSessions("box",
		proto.Session{
			MachineID: "box", SessionID: todo.Agents[0].SessionID, Agent: "claude", Status: proto.StatusDone,
			TurnError: "503 Service Unavailable: no eligible upstream account", TurnErrorCode: proto.TurnErrorUpstreamUnavailable,
			UpdatedAt: time.Now().Add(-time.Second),
		},
		proto.Session{
			MachineID: "box", SessionID: "s2", Agent: "codex", Status: proto.StatusDone,
			TurnError: "503 Service Unavailable", UpdatedAt: time.Now().Add(-time.Second),
		},
	)
	if len(codexTodo.Agents) > 0 {
		s.todos.mu.Lock()
		if cur := s.todos.todos[codexTodo.ID]; cur != nil && len(cur.Agents) > 0 {
			cur.Agents[0].SessionID = "s2"
			cur.Agents[0].Agent = "codex"
			codexTodo = *cur
		}
		s.todos.mu.Unlock()
	}

	// Only claude is in a provider-wide outage.
	d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), false))

	// Hold an outage-caused signal on todo (claude).
	s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
	if _, ok := pendingOutageSignal(s, todo.ID); !ok {
		t.Fatal("expected claude outage signal to be held")
	}

	// 1. Unrelated auth failure on claude must NOT be held.
	s.taskNotice(ctx, todo, "box", "interrupted", "", "401 Unauthorized: invalid api key")
	waitFor(t, "unrelated auth failure delivered immediately", func() bool { return len(f.notices()) >= 1 })
	if !strings.Contains(f.notices()[0], "401 Unauthorized") {
		t.Fatalf("expected immediate auth failure notice, got:\n%s", f.notices()[0])
	}
	// The held outage signal must still remain held (not leaked into the unrelated digest).
	if _, ok := pendingOutageSignal(s, todo.ID); !ok {
		t.Fatal("outage hold was prematurely released by unrelated failure")
	}

	// 2. Judge model not configured must NOT be held.
	_, notConfiguredErr := ai.New(ai.Config{}).Chat(ctx, ai.Request{})
	s.superviseJudged(ctx, todo, time.Now().Add(-3*time.Minute), taskDoneCheck{}, notConfiguredErr)
	// 3. Valid unsure verdict on a non-outage task must NOT be held.
	s.superviseJudged(ctx, codexTodo, time.Now().Add(-2*time.Minute), taskDoneCheck{Verdict: verdictUnsure, Reason: "ambiguous result"}, nil)
	// 4. Codex outage symptom while only claude is saturated must NOT be held.
	s.taskNotice(ctx, codexTodo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")

	waitFor(t, "unrelated signals delivered while the tasks are active", func() bool {
		joined := strings.Join(f.notices(), "\n")
		return strings.Contains(joined, "判定模型未配置") && strings.Contains(joined, "ambiguous result")
	})

	// 5. Explicit owner decision (verdictNeedsUser even with model error) must NOT be held.
	s.superviseJudged(ctx, codexTodo, time.Now().Add(-time.Minute), taskDoneCheck{
		Verdict: verdictNeedsUser,
		Reason:  "confirm production cutover",
	}, errors.New("503 Service Unavailable"))

	waitFor(t, "owner decision and unrelated failures delivered", func() bool {
		joined := strings.Join(f.notices(), "\n")
		return strings.Contains(joined, "判定模型未配置") &&
			strings.Contains(joined, "ambiguous result") &&
			strings.Contains(joined, "confirm production cutover") &&
			strings.Contains(joined, codexTodo.ID)
	})
	// Still must NOT have delivered the held claude outage recovery digest.
	for _, n := range f.notices() {
		if strings.Contains(n, "signal="+sigOutageRecovery) {
			t.Fatalf("outage recovery digest leaked before recovery:\n%s", n)
		}
	}
}

func TestOutageHoldDropsStaleOrClosedTask(t *testing.T) {
	t.Run("closed during hold", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), false))
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); !ok {
			t.Fatal("expected outage hold")
		}
		done := true
		if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &done}); err != nil {
			t.Fatal(err)
		}
		d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
			Provider: "claude", Accounts: 2, Available: 1,
		}}})
		s.sendDigest(ctx)
		if _, ok := pendingOutageSignal(s, todo.ID); ok {
			t.Fatal("closed task hold was not dropped")
		}
		if got := len(f.notices()); got != 0 {
			t.Fatalf("closed task received obsolete recovery notice:\n%v", f.notices())
		}
	})

	t.Run("resumed working during hold", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), false))
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); !ok {
			t.Fatal("expected outage hold")
		}
		// Agent resumes working while held.
		s.supervisedWorking(ctx, todo)
		if _, ok := pendingOutageSignal(s, todo.ID); ok {
			t.Fatal("supervisedWorking did not clear pending outage hold")
		}
		d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
			Provider: "claude", Accounts: 2, Available: 1,
		}}})
		s.sendDigest(ctx)
		if got := len(f.notices()); got != 0 {
			t.Fatalf("resumed task received obsolete recovery notice:\n%v", f.notices())
		}
	})

	t.Run("clean turn completed during hold", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), false))
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); !ok {
			t.Fatal("expected outage hold")
		}
		// Session finished a clean turn after the outage symptom without calling supervisedWorking.
		f.setSessions("box", proto.Session{
			MachineID: "box", SessionID: todo.Agents[0].SessionID, Agent: "claude", Status: proto.StatusDone,
			TurnError: "", LastText: "completed cleanly", UpdatedAt: time.Now().Add(time.Second),
		})
		d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
			Provider: "claude", Accounts: 2, Available: 1,
		}}})
		s.sendDigest(ctx)
		if got := len(f.notices()); got != 0 {
			t.Fatalf("recovered clean task received obsolete recovery notice:\n%v", f.notices())
		}
	})

	t.Run("parked during hold", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), false))
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); !ok {
			t.Fatal("expected outage hold")
		}
		if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
			v.State = reviewParked
			v.Reviews = append(v.Reviews, TaskReview{
				At:        time.Now().UTC(),
				Verdict:   reviewParked,
				Reason:    "waiting on external check",
				Until:     time.Now().Add(time.Hour).UTC(),
				WaitingOn: "external_event",
			})
			return true
		}); err != nil {
			t.Fatal(err)
		}
		d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
			Provider: "claude", Accounts: 2, Available: 1,
		}}})
		s.sendDigest(ctx)
		if got := len(f.notices()); got != 0 {
			t.Fatalf("parked task received obsolete recovery notice:\n%v", f.notices())
		}
	})
}

func TestOutageWithoutRecoveryMetadataFallsBackToBoundedPath(t *testing.T) {
	t.Run("saturated without EarliestRecovery", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
			Provider: "claude", Accounts: 2, Available: 0, EarliestRecovery: nil, Stale: false,
		}}})
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); ok {
			t.Fatal("signal was held indefinitely despite absent EarliestRecovery")
		}
		waitFor(t, "fallback interrupted delivery", func() bool { return len(f.notices()) == 1 })
		if !strings.Contains(f.notices()[0], "signal="+sigInterrupted) {
			t.Fatalf("expected normal interrupted fallback, got:\n%s", f.notices()[0])
		}
	})

	t.Run("stale saturation is not held", func(t *testing.T) {
		s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		d.setCapacity(saturated("claude", time.Now().Add(10*time.Minute), true))
		s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
		if _, ok := pendingOutageSignal(s, todo.ID); ok {
			t.Fatal("stale capacity snapshot must not hold signals")
		}
		waitFor(t, "stale fallback interrupted delivery", func() bool { return len(f.notices()) == 1 })
	})

	t.Run("ai PausedError with future Until holds and delivers at recovery", func(t *testing.T) {
		s, f, _, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		f.hello.Locale = "en"
		until := time.Now().Add(80 * time.Millisecond)
		pausedErr := &ai.PausedError{Pause: ai.Pause{Reason: ai.ReasonQuota, Until: until}}
		s.superviseJudged(ctx, todo, time.Now().Add(-time.Minute), taskDoneCheck{}, pausedErr)
		if _, ok := pendingOutageSignal(s, todo.ID); !ok {
			t.Fatal("expected ai.PausedError with future Until to hold sigUnjudged")
		}
		if got := len(f.notices()); got != 0 {
			t.Fatalf("expected 0 immediate notices while paused, got %d", got)
		}
		waitFor(t, "paused recovery digest delivered once Until passes", func() bool { return len(f.notices()) == 1 })
		msg := entryLine(f.notices()[0])
		if !strings.Contains(msg, "signal="+sigOutageRecovery) || !strings.Contains(msg, "coalesced 1 suppressed signal(s)") {
			t.Fatalf("unexpected paused recovery digest:\n%s", msg)
		}
	})

	t.Run("ai PausedError with past Until falls back immediately", func(t *testing.T) {
		s, f, _, todo := newOutageSupervisedService(t, newOutagePlanStore())
		ctx := context.Background()
		pausedErr := &ai.PausedError{Pause: ai.Pause{Reason: ai.ReasonQuota, Until: time.Now().Add(-time.Second)}}
		s.superviseJudged(ctx, todo, time.Now().Add(-time.Minute), taskDoneCheck{}, pausedErr)
		if _, ok := pendingOutageSignal(s, todo.ID); ok {
			t.Fatal("past PausedError.Until must not hold signal")
		}
		waitFor(t, "immediate unjudged fallback", func() bool { return len(f.notices()) == 1 })
		if !strings.Contains(f.notices()[0], "signal="+sigUnjudged) {
			t.Fatalf("expected sigUnjudged fallback, got:\n%s", f.notices()[0])
		}
	})
}

func TestOutageHoldUsesGatewayRetryAtRecoveryMetadata(t *testing.T) {
	s, f, d, todo := newOutageSupervisedService(t, newOutagePlanStore())
	ctx := context.Background()
	f.hello.Locale = "en"
	group := testPool
	group.Launchers = []string{"gw-claude"}
	group.QuotaPool = &GatewayQuotaPool{Source: "gw1", Provider: "claude"}
	withLauncherGroups(s, group)
	f.setSessions("box", proto.Session{
		MachineID:     "box",
		SessionID:     todo.Agents[0].SessionID,
		Agent:         "claude",
		Launcher:      "gw-claude",
		Status:        proto.StatusDone,
		State:         proto.StateUnread,
		TurnError:     "503 Service Unavailable: no eligible upstream account",
		TurnErrorCode: proto.TurnErrorUpstreamUnavailable,
		UpdatedAt:     time.Now().Add(-time.Second),
	})
	recovery := time.Now().Add(80 * time.Millisecond)
	d.mu.Lock()
	d.gatewayRetry = func(source, provider string, now time.Time) time.Time {
		if source == "gw1" && provider == "claude" && recovery.After(now) {
			return recovery
		}
		return time.Time{}
	}
	d.mu.Unlock()

	s.taskNotice(ctx, todo, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
	s.superviseJudged(ctx, todo, time.Now().Add(-time.Minute), taskDoneCheck{Verdict: verdictUnsure, Reason: "no reply", reasonKey: checkReasonNoReply}, nil)
	held, ok := pendingOutageSignal(s, todo.ID)
	if !ok || held.Outage == nil || held.Outage.Source != outageSourceGateway || held.Outage.Suppressed != 2 {
		t.Fatalf("expected gateway-backed outage hold with suppressed=2, got ok=%v %+v", ok, held.Outage)
	}
	waitFor(t, "gateway outage recovery digest", func() bool { return len(f.notices()) == 1 })
	msg := entryLine(f.notices()[0])
	if !strings.Contains(msg, "signal="+sigOutageRecovery) || !strings.Contains(msg, "coalesced 2 suppressed signal(s)") {
		t.Fatalf("unexpected gateway recovery digest:\n%s", msg)
	}
}

func TestOutageHoldPersistsAcrossRestartAndSeparatesPerTaskDigests(t *testing.T) {
	st := newOutagePlanStore()
	s1, f1, d1, todo1 := newOutageSupervisedService(t, st)
	ctx := context.Background()
	f1.hello.Locale = "en"

	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Second supervised task", "prompt": "fix second bug", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "second task", "done_when": []string{"done"},
	}}})
	if _, err := s1.toolTaskPlan(ctx, "conv1", raw); err != nil {
		t.Fatal(err)
	}
	var todo2 Todo
	for _, item := range s1.todosOf(ctx, "*") {
		if item.ID != todo1.ID {
			todo2 = item
		}
	}

	recovery := time.Now().Add(10 * time.Minute)
	d1.setCapacity(saturated("claude", recovery, false))

	s1.taskNotice(ctx, todo1, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")
	s1.superviseJudged(ctx, todo1, time.Now().Add(-time.Minute), taskDoneCheck{}, errors.New("503 Service Unavailable: no eligible upstream account"))
	s1.taskNotice(ctx, todo2, "box", "interrupted", "", "503 Service Unavailable: no eligible upstream account")

	// Restart manager on the same store before recovery, carrying over the persisted todos.
	s2, f2 := newQuietServiceOn(t, st)
	f2.hello.Locale = "en"
	d2 := &outageTestDevices{hub: f2}
	d2.setCapacity(proto.CapacitySnapshot{CheckedAt: time.Now(), Providers: []proto.ProviderCapacity{{
		Provider: "claude", Accounts: 2, Available: 1,
	}}})
	s2.devs = d2
	s1.todos.mu.RLock()
	s2.todos.mu.Lock()
	s2.todos.todos = map[string]*Todo{}
	for id, item := range s1.todos.todos {
		cp := *item
		s2.todos.todos[id] = &cp
	}
	s2.todos.loaded = true
	s2.todos.mu.Unlock()
	s1.todos.mu.RUnlock()
	f2.setSessions("box", proto.Session{
		MachineID: "box", SessionID: todo1.Agents[0].SessionID, Agent: "claude", Status: proto.StatusDone,
		TurnError: "503 Service Unavailable: no eligible upstream account", UpdatedAt: time.Now().Add(-time.Second),
	})
	if err := s2.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s2.restoreSupervisePending(ctx)
	s2.recheckOutageHolds(ctx, time.Now())
	s2.sendDigest(ctx)

	waitFor(t, "restarted coalesced per-task recovery digests", func() bool { return len(f2.notices()) == 1 })
	body := f2.notices()[0]
	if !strings.Contains(body, todo1.ID) || !strings.Contains(body, "coalesced 2 suppressed signal(s)") {
		t.Fatalf("missing coalesced task 1 entry after restart:\n%s", body)
	}
	if !strings.Contains(body, todo2.ID) || !strings.Contains(body, "coalesced 1 suppressed signal(s)") {
		t.Fatalf("missing coalesced task 2 entry after restart:\n%s", body)
	}
}

func TestOutageHoldDoesNotAlterAccountExhaustionContinuationLimits(t *testing.T) {
	sess := proto.Session{
		MachineID: "box",
		SessionID: "s1",
		Agent:     "claude",
		TurnError: "no other eligible account",
	}
	if accountExhaustionRetryAllowed(sess, 0, 0) {
		t.Fatal("default account_exhaustion_max_retries=0 must remain disallowed for coding Agents")
	}
	if !accountExhaustionRetryAllowed(sess, 0, 1) || accountExhaustionRetryAllowed(sess, 1, 1) {
		t.Fatal("explicit opt-in account_exhaustion_max_retries=1 limit must remain unchanged")
	}
}
