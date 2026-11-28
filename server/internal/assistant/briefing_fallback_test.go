package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// briefFallbackPosts is a fake Feishu transport. failNext makes the first card posts fail transiently; no request
// leaves the process. It also records the idempotency UUID that a retry must preserve.
type briefFallbackPosts struct {
	mu       sync.Mutex
	posts    []map[string]any
	failNext int
}

func briefFallbackCapture(f *fakeHub) *briefFallbackPosts {
	c := &briefFallbackPosts{}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_test", PairedAt: time.Unix(100, 0), PublicURL: testWeb,
			Features: FeishuFeatures{Main: true, Pushes: true, Reports: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.posts = append(c.posts, m)
		if c.failNext > 0 {
			c.failNext--
			return nil, errors.New("temporary Feishu transport failure")
		}
		return map[string]any{"message_id": fmt.Sprintf("om_fallback_%d", len(c.posts))}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	return c
}

func (c *briefFallbackPosts) list() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]map[string]any, len(c.posts))
	for i, p := range c.posts {
		out[i] = map[string]any{}
		for k, v := range p {
			out[i][k] = v
		}
	}
	return out
}

func (c *briefFallbackPosts) cards() []string {
	var out []string
	for _, p := range c.list() {
		card, _ := p["card"].(string)
		out = append(out, card)
	}
	return out
}

func briefFallbackFacts() *briefFacts {
	now := time.Now().UTC()
	return &briefFacts{At: now, Lang: string(i18n.ZhHans), Web: testWeb, Loc: time.UTC, Changed: map[string]string{}, Asks: map[string]modapi.BriefAsk{},
		Directions: []prompts.BriefDirection{{ID: "main", Name: "Main", Owner: true, Open: 1, Link: testWeb + "/persona/main",
			Waits: []prompts.BriefWait{{TodoID: "todo-stopped", Title: "Stopped on quota", Kind: "stalled", Since: now.Add(-time.Hour),
				WaitingOn: "technical", Link: testWeb + "/todos/todo-stopped", Line: "Stopped on quota · stopped for 1 hour"}}}}}
}

func attachBriefFallback(s *Service, session, label string, fallbackOnly bool) {
	s.briefSetSessionRun(session, &briefRun{Facts: briefFallbackFacts(), Label: label, FallbackOnly: fallbackOnly})
}

func initBriefFallbackState(s *Service) {
	s.fleet.mu.Lock()
	s.fleet.interactions = map[string]*pending{}
	s.fleet.mu.Unlock()
	s.todos.mu.Lock()
	s.todos.loaded, s.todos.todos, s.todos.activeBySession = true, map[string]*Todo{}, map[[2]string]string{}
	s.todos.mu.Unlock()
	s.accept.mu.Lock()
	s.accept.loaded, s.accept.asks = true, map[string]*acceptAsk{}
	s.accept.mu.Unlock()
	s.fleetUp.Store(true)
}

// One routine at one scheduled due has one receipt, independent of the run id and of the bounded display history. A
// restarted hub reads the same receipt. Missing facts and a failed receipt write consume nothing, so a later attempt can send.
func TestBriefFallbackDeduplicatesOccurrenceAcrossRetryAndRestart(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	s := newServiceOn(t, f, st)
	initBriefFallbackState(s)
	due := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	run := ScheduleRun{ID: "run-first", RefID: "morning", SessionID: "s-morning", Due: due}
	task := Task{ID: "morning", Title: "Morning briefing", Facts: true}
	machine := Device{ID: "server", Name: "Server"}

	attachBriefFallback(s, run.SessionID, task.Title, false)
	s.briefFallback(ctx, run, task, machine)
	waitFor(t, "the first fallback notification", func() bool { return len(f.pushed()) == 1 })
	header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
	first := f.pushed()[0]
	if !strings.HasPrefix(first.Body, header+"\n") || !strings.Contains(first.Body, "Stopped on quota") ||
		first.ID != "ntf_brief_fallback_"+briefFallbackID(run) {
		t.Fatalf("first fallback = %+v", first)
	}

	run.ID = "run-automatic-retry"
	attachBriefFallback(s, run.SessionID, task.Title, false)
	s.briefFallback(ctx, run, task, machine)
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("automatic retry sent %d notifications, want one", got)
	}

	// Trim the ordinary display history well past maxRuns. The occurrence receipt is a separate setting.
	sc := s.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = make([]ScheduleRun, maxRuns+10)
	for i := range sc.runs {
		sc.runs[i] = ScheduleRun{ID: fmt.Sprintf("old-%d", i), RefID: "other", Status: runOK, Due: due, of: due}
	}
	sc.mu.Unlock()
	s.recordRun(ScheduleRun{RefID: "other", Status: runOK, Due: due, of: due})

	restarted := newServiceOn(t, f, st)
	initBriefFallbackState(restarted)
	attachBriefFallback(restarted, run.SessionID, task.Title, false)
	restarted.briefFallback(ctx, run, task, machine)
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("restart and run-history trimming resent the occurrence: %d notifications", got)
	}

	nextDue := run
	nextDue.Due = due.Add(24 * time.Hour)
	attachBriefFallback(restarted, nextDue.SessionID, task.Title, false)
	restarted.briefFallback(ctx, nextDue, task, machine)
	waitFor(t, "the next scheduled occurrence", func() bool { return len(f.pushed()) == 2 })
	otherRoutine := run
	otherRoutine.RefID = "evening"
	otherTask := Task{ID: "evening", Title: "Evening review", Facts: true}
	attachBriefFallback(restarted, otherRoutine.SessionID, otherTask.Title, false)
	restarted.briefFallback(ctx, otherRoutine, otherTask, machine)
	waitFor(t, "another routine at the same due", func() bool { return len(f.pushed()) == 3 })

	missing := run
	missing.RefID, missing.SessionID = "missing-facts", "s-missing"
	missingTask := Task{ID: missing.RefID, Title: "Missing facts", Facts: true}
	restarted.briefFallback(ctx, missing, missingTask, machine) // no briefRun attached: no receipt is consumed
	attachBriefFallback(restarted, missing.SessionID, missingTask.Title, false)
	restarted.briefFallback(ctx, missing, missingTask, machine)
	waitFor(t, "a retry after facts became available", func() bool { return len(f.pushed()) == 4 })

	storeFailure := run
	storeFailure.RefID, storeFailure.SessionID = "store-failure", "s-store-failure"
	storeTask := Task{ID: storeFailure.RefID, Title: "Store failure", Facts: true}
	attachBriefFallback(restarted, storeFailure.SessionID, storeTask.Title, false)
	st.mu.Lock()
	st.failPut = errors.New("database unavailable")
	st.mu.Unlock()
	restarted.briefFallback(ctx, storeFailure, storeTask, machine)
	if got := len(f.pushed()); got != 4 {
		t.Fatalf("an unremembered fallback went out: %d notifications", got)
	}
	st.mu.Lock()
	st.failPut = nil
	st.mu.Unlock()
	restarted.briefFallback(ctx, storeFailure, storeTask, machine)
	waitFor(t, "the retry after the receipt write recovered", func() bool { return len(f.pushed()) == 5 })

	ordinary := run
	ordinary.RefID, ordinary.SessionID = "ordinary-patrol", "s-ordinary"
	attachBriefFallback(restarted, ordinary.SessionID, "Ordinary patrol", false)
	restarted.briefFallback(ctx, ordinary, Task{ID: ordinary.RefID, Title: "Ordinary patrol"}, machine)
	if got := len(f.pushed()); got != 5 {
		t.Fatalf("an ordinary patrol was rerouted through the briefing fallback: %d", got)
	}
}

func TestBriefNotStartedFallbackDeduplicatesRetryAndRestart(t *testing.T) {
	needsLarkLayer(t)
	needsBriefingModule(t)
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	capture := briefFallbackCapture(f)
	f.failAssistant = errors.New("mock launcher unavailable")
	s := newServiceOn(t, f, st)
	initBriefFallbackState(s)
	setSettings(t, s, func(v *Settings) { v.Main = "main" })
	stoppedAt := time.Now().Add(-3 * time.Hour)
	s.todos.mu.Lock()
	s.todos.todos["todo-stopped"] = &Todo{ID: "todo-stopped", Origin: "main", Title: "Stopped on quota",
		Agents: []TodoAgent{{MachineID: "server", SessionID: "old-agent", LinkedAt: stoppedAt.Add(-time.Hour), ReleasedAt: &stoppedAt}}}
	s.todos.mu.Unlock()
	s.runRetry = 50 * time.Millisecond
	due := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	task := Task{ID: "morning-not-started", Title: "Morning briefing", Prompt: "Give me the briefing", Repeat: repeatInterval,
		EveryMinutes: 30, At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server", CreatedBy: "assistant", Facts: true}
	setTasks(s, task)
	if _, err := s.runTask(task, due, time.Now(), false, false); err == nil {
		t.Fatal("the mock conversation-start failure unexpectedly succeeded")
	}
	waitFor(t, "the not-started fallback", func() bool { return len(f.pushed()) == 1 })
	header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
	first := f.pushed()[0]
	if !strings.HasPrefix(first.Body, header+"\n") || !strings.Contains(first.Body, "Stopped on quota") || strings.Contains(first.Body, "mock launcher unavailable") {
		t.Fatalf("not-started fallback = %+v", first)
	}
	if cards := capture.cards(); len(cards) != 1 || !strings.Contains(cards[0], "Stopped on quota") {
		t.Fatalf("not-started Feishu cards = %q", cards)
	}
	waitFor(t, "the automatic not-started retry", func() bool {
		sc := s.loadSchedule(ctx)
		sc.mu.Lock()
		defer sc.mu.Unlock()
		n := 0
		for _, r := range sc.runs {
			if r.RefID == task.ID && r.Status == runFailed {
				n++
			}
		}
		return n >= 2
	})
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("the automatic retry sent %d fallback notifications, want one", got)
	}
	if got := len(capture.cards()); got != 1 {
		t.Fatalf("the automatic retry sent %d Feishu cards, want one", got)
	}

	restarted := newServiceOn(t, f, st)
	initBriefFallbackState(restarted)
	if _, err := restarted.runTask(task, due, time.Now(), false, true); err == nil {
		t.Fatal("the restarted mock conversation-start failure unexpectedly succeeded")
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("a restarted hub resent the occurrence: %d notifications", got)
	}
	sc := restarted.loadSchedule(ctx)
	sc.mu.Lock()
	defer sc.mu.Unlock()
	n := 0
	for _, r := range sc.runs {
		if r.RefID == task.ID && r.Status == runFailed {
			n++
			if !r.Due.Equal(due) {
				t.Errorf("retry changed scheduled due to %s, want %s", r.Due, due)
			}
		}
	}
	if n != 3 {
		t.Fatalf("failed runs = %d, want first + automatic retry + restarted retry", n)
	}
}

// Report keeps its old successful behavior: north-star model input, no Facts feed/card/push. The full snapshot is attached
// only so a model failure can still deliver deterministic facts.
func TestBriefReportFactsAreFallbackOnly(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newStorelessService(t, f)
	initBriefFallbackState(s)
	task := Task{ID: "evening-report", Title: "Evening report", Repeat: repeatDaily, Report: true}
	if model := s.briefReportStart(ctx, task, time.Now().UTC()); model != "" {
		t.Fatalf("Report without a north-star store got different model input: %q", model)
	}
	s.briefSent(ScheduleRun{RefID: task.ID, SessionID: "s-report", Due: time.Now()})
	run := s.briefOf("s-report")
	if run == nil || !run.FallbackOnly || run.Facts == nil {
		t.Fatalf("Report has no retained fallback facts: %+v", run)
	}
	if got, claimed := s.briefClaim("s-report", "⏰ Evening report", "report-turn", false); got != nil || claimed {
		t.Fatalf("a successful Report grew a Facts card: %+v (claimed %v)", got, claimed)
	}
	if got := s.briefPush("s-report", i18n.ZhHans, true); got != "" {
		t.Fatalf("a successful Report grew a Facts push: %q", got)
	}
	if got := feedKinds(s, "s-report"); got != "" {
		t.Fatalf("a successful Report grew a normal brief feed: %s", got)
	}

	scheduled := ScheduleRun{RefID: task.ID, SessionID: "s-report", Due: time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)}
	s.briefFallback(ctx, scheduled, task, Device{ID: "server", Name: "Server"})
	waitFor(t, "the Report fallback", func() bool { return len(f.pushed()) == 1 })
	if body := f.pushed()[0].Body; !strings.HasPrefix(body, i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")+"\n") {
		t.Fatalf("Report fallback body = %q", body)
	}
}

// A transient Feishu failure retries the same card with the same UUID while the failed scheduled turn is still last.
// The fallback itself has no assistant answer, so its retry guard follows that failed turn rather than a synthetic one.
func TestBriefFallbackFeishuRetryUsesStableCardAndFailedTurn(t *testing.T) {
	needsLarkLayer(t)
	needsBriefingModule(t)
	f := newFakeHub().withMachine()
	capture := briefFallbackCapture(f)
	capture.failNext = 1
	s := newStorelessService(t, f)
	s.setLarkTiming("mirror_retry", quickMirrorRetry)
	initBriefFallbackState(s)
	f.setItems(proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ Morning briefing"},
		proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "API Error: 529"})
	run := ScheduleRun{RefID: "morning", SessionID: "s-morning", Due: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	attachBriefFallback(s, run.SessionID, "Morning briefing", false)
	s.briefFallback(context.Background(), run, Task{ID: "morning", Title: "Morning briefing", Facts: true}, Device{ID: "server", Name: "Server"})
	waitFor(t, "the Feishu retry", func() bool { return len(capture.list()) == 2 })
	posts := capture.list()
	if posts[0]["uuid"] == "" || posts[0]["uuid"] != posts[1]["uuid"] {
		t.Fatalf("retry changed its idempotency UUID: %v / %v", posts[0]["uuid"], posts[1]["uuid"])
	}
	cards := capture.cards()
	header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
	if !strings.Contains(cards[0], header) || !strings.Contains(cards[1], "Stopped on quota") {
		t.Fatalf("retry did not deliver the fixed facts: %q", cards)
	}
}

// briefRetryRun drives one first failed turn and one automatic retry on a fake node. With succeedRetry, the retry has a
// normal model answer; otherwise it fails too. The fake Feishu and notification captures prove what the owner would get.
func briefRetryRun(t *testing.T, succeedRetry bool) (*Service, *fakeHub, *briefFallbackPosts, proto.Session, []ScheduleRun) {
	t.Helper()
	needsBriefingModule(t)
	f := newFakeHub().withMachine()
	capture := briefFallbackCapture(f)
	s := newServiceOn(t, f, &memStore{})
	initBriefFallbackState(s)
	s.runRetry, s.turnPoll = 100*time.Millisecond, 20*time.Millisecond
	setTasks(s, Task{ID: "morning", Title: "Morning briefing", Prompt: "Give me the briefing", Repeat: repeatInterval,
		EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true, MachineID: "server", CreatedBy: "assistant", Facts: true})
	f.setItems(proto.Item{ID: "u1", Role: "user", Kind: proto.KindText, Text: "⏰ Morning briefing"},
		proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "API Error: 529 quota unavailable"})
	f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	turnSent := make(chan int, 2)
	watching := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
	sends := 0
	f.onAssistantSend = func(f *fakeHub, _ map[string]any) {
		for i := range f.sessions["server"] {
			if f.sessions["server"][i].SessionID == "s1" {
				f.sessions["server"][i].Status, f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
				f.sessions["server"][i].TurnError = ""
			}
		}
		sends++
		turnSent <- sends
	}
	if f.nodes == nil {
		f.nodes = map[string]nodeAnswer{}
	}
	f.nodes[DeviceSessionGet] = func(machine string, params json.RawMessage) (any, error) {
		var asked struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &asked)
		for _, sess := range f.sessions[machine] {
			if sess.SessionID != asked.SessionID {
				continue
			}
			if sess.SessionID == "s1" && sess.State == proto.StateWorking && sends >= 1 && sends <= len(watching) {
				select {
				case <-watching[sends-1]:
				default:
					close(watching[sends-1])
				}
			}
			return map[string]any{"session": sess, "ok": true}, nil
		}
		return map[string]any{"ok": false}, nil
	}
	sess, err := runNow(t, s, "morning")
	if err != nil {
		t.Fatal(err)
	}
	if sess.SessionID != "s1" {
		t.Fatalf("scheduled session = %+v", sess)
	}
	go func() {
		for step := 1; step <= 2; step++ {
			select {
			case <-turnSent:
			case <-time.After(5 * time.Second):
				return
			}
			select {
			case <-watching[step-1]:
			case <-time.After(5 * time.Second):
				return
			}
			if step == 2 && succeedRetry {
				f.setItems(proto.Item{ID: "u2", Role: "user", Kind: proto.KindText, Text: "⏰ Morning briefing"},
					proto.Item{ID: "a2", Role: "assistant", Kind: proto.KindText, Text: briefSummary})
			}
			f.mu.Lock()
			for i := range f.sessions["server"] {
				if f.sessions["server"][i].SessionID == "s1" {
					f.sessions["server"][i].Status, f.sessions["server"][i].State = proto.StatusIdle, proto.StateIdle
					if step == 1 || !succeedRetry {
						f.sessions["server"][i].TurnError = "API Error: 401 invalid API key"
					}
				}
			}
			f.mu.Unlock()
		}
	}()

	waitFor(t, "the first model-failure fallback", func() bool { return len(capture.list()) == 1 && len(f.pushed()) == 1 })
	waitFor(t, "the automatic retry and its terminal watcher", func() bool {
		if f.sent() < 2 {
			return false
		}
		s.sched.mu.Lock()
		watchDone := len(s.sched.stateDrafts) == 0
		s.sched.mu.Unlock()
		if !watchDone {
			return false
		}
		sc := s.loadSchedule(context.Background())
		sc.mu.Lock()
		defer sc.mu.Unlock()
		for _, r := range sc.runs {
			if r.RefID == "morning" && r.Retried {
				if succeedRetry {
					return r.Status == runOK && s.briefOf("s1") != nil && s.briefOf("s1").Facts.At.After(time.Now().Add(-time.Minute))
				}
				return r.Status == runFailed
			}
		}
		return false
	})
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	runs := append([]ScheduleRun(nil), sc.runs...)
	sc.mu.Unlock()
	return s, f, capture, sess, runs
}

func TestBriefingFailureFallbackIsNotRepeatedByAutomaticRetry(t *testing.T) {
	needsLarkLayer(t)
	_, f, capture, _, runs := briefRetryRun(t, false)
	if got := len(capture.list()); got != 1 {
		t.Fatalf("failed first turn plus failed retry posted %d fallbacks, want one", got)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("failed first turn plus failed retry pushed %d notifications, want one: %+v", got, f.pushed())
	}
	var first, retry *ScheduleRun
	for i := range runs {
		if runs[i].RefID != "morning" {
			continue
		}
		if runs[i].Retried {
			retry = &runs[i]
		} else {
			first = &runs[i]
		}
	}
	if first == nil || retry == nil || first.Status != runFailed || retry.Status != runFailed || !retry.Due.Equal(first.Due) {
		t.Fatalf("retry did not preserve the scheduled occurrence: first %+v, retry %+v", first, retry)
	}
	card := capture.cards()[0]
	header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
	if !strings.Contains(card, header) || strings.Contains(card, "API Error: 401") {
		t.Fatalf("fallback exposed the raw model error or lacked its fixed header: %s", card)
	}
	if title := f.pushed()[0].Title; strings.Contains(title, "失败") {
		t.Fatalf("the ordinary failed-task push was not suppressed: %+v", f.pushed()[0])
	}
}

func TestBriefingRetrySuccessSendsNormalCardWithoutRetractingFallback(t *testing.T) {
	needsLarkLayer(t)
	s, f, capture, sess, runs := briefRetryRun(t, true)
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("the one fallback notification was duplicated or retracted: %+v", f.pushed())
	}
	s.feishuMirror(context.Background(), sess.MachineID, sess.SessionID)
	cards := capture.cards()
	if len(cards) != 2 {
		t.Fatalf("fallback plus successful retry posted %d cards, want two: %q", len(cards), cards)
	}
	header := i18n.T(i18n.ZhHans, "assistant-briefing.fallback_header")
	if !strings.Contains(cards[0], header) || strings.Contains(cards[1], header) || !strings.Contains(cards[1], briefSummary) {
		t.Fatalf("successful retry did not send the ordinary card: %q", cards)
	}
	var first, retry *ScheduleRun
	for i := range runs {
		if runs[i].RefID != "morning" {
			continue
		}
		if runs[i].Retried {
			retry = &runs[i]
		} else {
			first = &runs[i]
		}
	}
	if first == nil || retry == nil || first.Status != runFailed || retry.Status != runOK || !retry.Due.Equal(first.Due) {
		t.Fatalf("successful retry lost its occurrence identity: first %+v, retry %+v", first, retry)
	}
}
