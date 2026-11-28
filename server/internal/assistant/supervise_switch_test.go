package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The switch for automatic supervision messages (supervise_switch.go): saved per assistant, on unless turned off, and
// when off nothing is gathered, scheduled, queued or sent, what waits is withdrawn, and none of it comes back when the
// switch is turned on again. The task ledger, the owner's own review and the approval cards do not depend on it.

func putAutoSupervision(t *testing.T, s *Service, on bool) map[string]any {
	t.Helper()
	body := `{"auto_supervision_messages":false}`
	if on {
		body = `{"auto_supervision_messages":true}`
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT %s: %d %s", body, rec.Code, rec.Body.String())
	}
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func getAssistantView(t *testing.T, s *Service) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %v %s", rec.Code, err, rec.Body.String())
	}
	return view
}

// setAutoSupervisionQuietly saves the switch without the settings-changed hook: what a timer, a drain or a restart finds
// when the switch was turned off a moment before they run.
func setAutoSupervisionQuietly(t *testing.T, s *Service, on bool) {
	t.Helper()
	mode := supervisionModeOff
	if on {
		mode = supervisionModeAll
	}
	setSupervisionModeQuietly(t, s, mode)
}

// setSupervisionModeQuietly saves the mode (and keeps the older switch in step) without the settings-changed hook.
func setSupervisionModeQuietly(t *testing.T, s *Service, mode string) {
	t.Helper()
	on := mode != supervisionModeOff
	if _, err := s.updateSettings(context.Background(), func(v *Settings) error {
		v.AutoSupervisionMode, v.AutoSupervisionMessages = mode, &on
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// restartedService is a new manager on the database st that keeps the saved settings (a restart does not rewrite them
// the way the other fixtures do) and finds the main conversation where it was.
func restartedService(t *testing.T, st Store) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	s := newServiceOn(t, f, st)
	shortSupervise(t, s)
	f.hello.Assistant.MachineID = "server"
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	return s, f
}

// queuedSupervise counts the supervision messages in conversation to's saved relay queue.
func queuedSupervise(s *Service, to string) (n int, withdrawn int) {
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	for _, m := range s.relays.st.Queue[to] {
		if m.Tool == superviseTool {
			n++
			if m.Withdrawn {
				withdrawn++
			}
		}
	}
	return n, withdrawn
}

func gatheredSignals(s *Service) (memory, durable int, timer bool) {
	b := &s.supervise
	b.mu.Lock()
	memory, timer = len(b.pending), b.timer != nil
	b.mu.Unlock()
	s.relays.mu.Lock()
	durable = len(s.relays.st.SupervisePending)
	s.relays.mu.Unlock()
	return
}

func TestAutoSupervisionSwitchDefaultsOnAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	if v := getAssistantView(t, s); v["auto_supervision_messages"] != true {
		t.Fatalf("default view = %v, want true (on)", v["auto_supervision_messages"])
	}
	if !s.autoSupervisionOn(ctx) {
		t.Fatal("unset must be on, as every assistant behaved before the switch")
	}
	if v := putAutoSupervision(t, s, false); v["auto_supervision_messages"] != false {
		t.Fatalf("PUT answer = %v, want the saved false", v["auto_supervision_messages"])
	}
	if s.autoSupervisionOn(ctx) || getAssistantView(t, s)["auto_supervision_messages"] != false {
		t.Fatal("not off right after the write")
	}
	// Another PUT that does not name it leaves it alone.
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(`{"supervision_batch_minutes":5}`)))
	if rec.Code != http.StatusOK || getAssistantView(t, s)["auto_supervision_messages"] != false {
		t.Fatalf("an unrelated write changed it: %d %s", rec.Code, rec.Body.String())
	}

	again, _ := restartedService(t, st) // a restart: the same database, a new manager
	if again.autoSupervisionOn(ctx) || getAssistantView(t, again)["auto_supervision_messages"] != false {
		t.Fatal("the switch did not survive a restart")
	}
	if v := putAutoSupervision(t, again, true); v["auto_supervision_messages"] != true || !again.autoSupervisionOn(ctx) {
		t.Fatalf("turning it back on: %v", v["auto_supervision_messages"])
	}
	third, _ := restartedService(t, st)
	if !third.autoSupervisionOn(ctx) || getAssistantView(t, third)["auto_supervision_messages"] != true {
		t.Fatal("on did not survive a restart")
	}
}

func TestAutoSupervisionOffGathersAndSchedulesNothing(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	putAutoSupervision(t, s, false)
	before := len(f.notices())
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "off-1", Minutes: 50})
	s.queueSignal(superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "off-2"})
	s.restoreDigest([]superviseSignal{{Todo: todo.ID, Kind: sigInterrupted, Key: "off-3"}}, time.Now(), time.Time{})
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("gathered while off: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	time.Sleep(4 * shortSuperviseTimings.every) // sleep-ok: a negative check, nothing must go out over a window several digest ticks long; there is no event to wait for
	if got := len(f.notices()) - before; got != 0 {
		t.Fatalf("%d supervision message(s) went out while off", got)
	}
	// The ledger is still kept: the task and its supervision are there.
	if now := todoNow(s, todo.ID); now.Supervision == nil || now.DoneAt != nil {
		t.Fatalf("the task ledger changed: %+v", now)
	}
}

// Turned back on, only what happens afterwards is told: what was gathered or queued while on, and withdrawn when it was
// turned off, is not replayed.
func TestAutoSupervisionOnAgainTakesOnlyNewEvents(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour} // gathered, not yet sent
	old := superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "before-off", Detail: "cut off"}
	s.signal(old)
	if mem, durable, _ := gatheredSignals(s); mem != 1 || durable != 1 {
		t.Fatalf("not gathered before the switch was turned off: memory=%d durable=%d", mem, durable)
	}
	putAutoSupervision(t, s, false)
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("not withdrawn: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	s.superviseTimes = shortSuperviseTimings
	putAutoSupervision(t, s, true)
	time.Sleep(4 * shortSuperviseTimings.every) // sleep-ok: a negative check over several digest ticks; no event to wait for
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("a withdrawn signal was replayed after the switch was turned on: %q", n)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "after-on", Detail: "cut off again"})
	waitFor(t, "the new event's message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; strings.Contains(n, "before-off") || !strings.Contains(n, "cut off again") {
		t.Fatalf("message: %s", n)
	}
}

// Hub queueing: a digest waiting behind a working conversation is taken off the queue and its outbox entry with it; the
// conversation going idle later sends nothing, owner is not pushed and nothing is retried.
func TestAutoSupervisionOffWithdrawsQueuedDigest(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Queued example")
	s.updateSupervisePending(func(p map[string]superviseSignal) {
		g := superviseSignal{Todo: tasks[0].ID, Kind: sigStalled, Key: "pending", Minutes: 50}
		p[g.id()] = g
	})
	if n, _ := queuedSupervise(s, "conv1"); n != 1 {
		t.Fatalf("fixture: %d queued", n)
	}
	putAutoSupervision(t, s, false)
	if n, _ := queuedSupervise(s, "conv1"); n != 0 || s.queuedFor("conv1") != 0 {
		t.Fatalf("still queued: %d, queuedFor=%d", n, s.queuedFor("conv1"))
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("gathered after the withdrawal: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	s.supervise.mu.Lock()
	indexed := len(s.supervise.queued)
	s.supervise.mu.Unlock()
	if indexed != 0 {
		t.Fatalf("the in-memory queue index kept %d conversation(s)", indexed)
	}
	pushes := len(f.pushes)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || len(f.pushes) != pushes {
		t.Fatalf("a withdrawn digest was sent or pushed: sends=%q pushes=%d", f.notices(), len(f.pushes)-pushes)
	}
	// Another message in the same queue is not a supervision message and stays.
	if s.queueRelay("conv1", relayMsg{From: "conv2", FromTitle: "Other", Tool: "conversation_send", Text: "hello", At: time.Now().UTC()}) != 1 {
		t.Fatal("an ordinary relay no longer queues")
	}
	putAutoSupervision(t, s, false) // withdrawing again leaves it
	if s.queuedFor("conv1") != 1 {
		t.Fatalf("an ordinary relay was withdrawn: %d", s.queuedFor("conv1"))
	}
}

// A timer that was already running when the switch went off sends nothing, and what it held is withdrawn.
func TestAutoSupervisionOffStopsARunningDigestTimer(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "timer", Detail: "cut off"})
	if mem, _, timer := gatheredSignals(s); mem != 1 || !timer {
		t.Fatalf("fixture: memory=%d timer=%v", mem, timer)
	}
	setAutoSupervisionQuietly(t, s, false)      // no hook: the timer fires into the off switch
	time.Sleep(4 * shortSuperviseTimings.every) // sleep-ok: a negative check over several digest ticks; no event to wait for
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("the timer sent %q", n)
	}
	s.supervise.mu.Lock()
	last := s.supervise.last
	s.supervise.mu.Unlock()
	if !last.IsZero() {
		t.Fatalf("a digest pass was counted while off: last message at %v", last)
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("after the timer: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
}

// The digest that waits for its turn: it was started while the switch was on (a timer fired, another digest was still being
// delivered) and the switch went off before it took anything out of the gather. It takes nothing and sends nothing.
func TestAutoSupervisionOffRechecksBeforeTakingTheGather(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "waiting", Detail: "cut off", At: time.Now().Add(-time.Hour)}
	s.signal(g)
	s.supervise.dedupMu.Lock() // another delivery holds the line
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.sendDigest(context.Background())
	}()
	time.Sleep(100 * time.Millisecond) // sleep-ok: let the goroutine reach the held line; the lock exposes no signal
	setAutoSupervisionQuietly(t, s, false)
	s.supervise.dedupMu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the digest did not end")
	}
	s.supervise.mu.Lock()
	last, sent := s.supervise.last, len(s.supervise.sent)
	s.supervise.mu.Unlock()
	if !last.IsZero() || sent != 0 || len(f.notices()) != 0 {
		t.Fatalf("it took the gather: last=%v sent=%d notices=%q", last, sent, f.notices())
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("not withdrawn: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
}

// The send race: the digest was put together while the switch was on, and it went off before the send. The message is not
// sent, not queued, not retried and not pushed; its signals are not restored, and nothing is counted as told.
func TestAutoSupervisionOffRaceBeforeSendIsSuppressed(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "race", Detail: "cut off"}
	s.updateSupervisePending(func(p map[string]superviseSignal) { p[g.id()] = g })
	setAutoSupervisionQuietly(t, s, false)
	pushes := len(f.pushes)
	if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("a suppressed digest reported as delivered")
	}
	if len(f.notices()) != 0 || len(f.pushes) != pushes {
		t.Fatalf("sent or pushed: %q, %d pushes", f.notices(), len(f.pushes)-pushes)
	}
	if n, _ := queuedSupervise(s, "conv1"); n != 0 {
		t.Fatalf("queued: %d", n)
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("restored for a retry: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	// The last look in deliverRelay itself refuses, with the outcome that is none of sent, refused or failed.
	err := s.deliverRelay(ctx, "server", "conv1", relayMsg{From: superviseTool, Tool: superviseTool, Text: "x", At: time.Now().UTC()})
	if !relaySuppressed(err) {
		t.Fatalf("deliverRelay = %v, want the suppressed outcome", err)
	}
	if relayFailureOf(err) == relayFailedRefused || refusedForGood(err) {
		t.Fatal("suppressed must not read as refused for good")
	}
	if len(f.notices()) != 0 {
		t.Fatalf("deliverRelay sent %q", f.notices())
	}
	// An ordinary relay is not touched by the switch.
	if err := s.deliverRelay(ctx, "server", "conv1", relayMsg{From: "conv2", FromTitle: "Other", Tool: "conversation_send", Text: "hi", At: time.Now().UTC()}); err != nil {
		t.Fatalf("an ordinary relay: %v", err)
	}
}

// The queue race: the conversation is working, so the digest would queue; the switch went off first. Nothing is admitted to
// the queue, neither as a new message nor by replacing the one already waiting, and the failure to queue is not retried.
func TestAutoSupervisionOffRaceBeforeQueueingIsSuppressed(t *testing.T) {
	ctx := context.Background()
	s, f, tasks := queuedDigestFixture(t, "Queue race")
	var before relayMsg
	s.relays.mu.Lock()
	before = s.relays.st.Queue["conv1"][0]
	s.relays.mu.Unlock()
	setAutoSupervisionQuietly(t, s, false)
	g := superviseSignal{Todo: tasks[0].ID, Kind: sigStalled, Key: "late", Minutes: 50}
	if s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("a suppressed digest reported as queued")
	}
	s.relays.mu.Lock()
	queue := append([]relayMsg(nil), s.relays.st.Queue["conv1"]...)
	s.relays.mu.Unlock()
	if len(queue) != 1 || queue[0].Text != before.Text || queue[0].Signals != before.Signals {
		t.Fatalf("the queue changed while off: %d message(s)", len(queue))
	}
	if _, err := s.queueRelayDurable("conv1", relayMsg{From: superviseTool, Tool: superviseTool, Text: "x", At: time.Now().UTC()}); !relaySuppressed(err) {
		t.Fatalf("queueRelayDurable = %v, want the suppressed outcome", err)
	}
	if mem, _, timer := gatheredSignals(s); mem != 0 || timer {
		t.Fatalf("a retry was scheduled: memory=%d timer=%v", mem, timer)
	}
	if len(f.notices()) != 0 {
		t.Fatalf("sent %q", f.notices())
	}
}

// The drain race and queue recovery: a supervision message that was queued while on and is found by a drain (a turn
// that ended, a node that reconnected, a hub that restarted) after the switch went off leaves unsent, once.
func TestAutoSupervisionOffDrainSuppressesQueuedMessage(t *testing.T) {
	s, f, _ := queuedDigestFixture(t, "Drain example")
	setAutoSupervisionQuietly(t, s, false) // no withdrawal ran
	pushes := len(f.pushes)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || len(f.pushes) != pushes {
		t.Fatalf("sent or pushed: %q, %d", f.notices(), len(f.pushes)-pushes)
	}
	if n, _ := queuedSupervise(s, "conv1"); n != 0 || s.queuedFor("conv1") != 0 {
		t.Fatalf("still queued: %d", n)
	}
	s.relays.mu.Lock()
	stuck, failed := len(s.relays.stuck), len(s.relays.failedAt)
	s.relays.mu.Unlock()
	if stuck != 0 || failed != 0 {
		t.Fatalf("suppressed counted as a failed try: stuck=%d failedAt=%d", stuck, failed)
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("its signals were replayed: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
}

// A send in flight is not cut off, but its head is marked withdrawn with nothing left to replay: the drain that sent it
// removes it, and neither retries it nor puts its signals back.
func TestAutoSupervisionOffMarksTheInFlightHeadWithdrawn(t *testing.T) {
	s, f, _ := queuedDigestFixture(t, "In flight")
	s.relays.mu.Lock()
	s.relays.draining["conv1"] = true // a drain's send is on its way
	s.relays.mu.Unlock()
	putAutoSupervision(t, s, false)
	n, withdrawn := queuedSupervise(s, "conv1")
	if n != 1 || withdrawn != 1 {
		t.Fatalf("the in-flight head: %d queued, %d withdrawn", n, withdrawn)
	}
	s.relays.mu.Lock()
	head := s.relays.st.Queue["conv1"][0]
	delete(s.relays.draining, "conv1")
	s.relays.mu.Unlock()
	if got, structured := decodeDigestSignals(head.Signals); !structured || len(got) != 0 {
		t.Fatalf("withdrawn head signals = %q, want an empty structured list", head.Signals)
	}
	pushes := len(f.pushes)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || len(f.pushes) != pushes || s.queuedFor("conv1") != 0 {
		t.Fatalf("the withdrawn head: sends=%q pushes=%d queued=%d", f.notices(), len(f.pushes)-pushes, s.queuedFor("conv1"))
	}
	if mem, durable, _ := gatheredSignals(s); mem != 0 || durable != 0 {
		t.Fatalf("a withdrawn head replayed its signals: memory=%d durable=%d", mem, durable)
	}
}

// Hub restart: a queue the manager saved before the switch was turned off (its withdrawal did not reach the store) is
// not sent by the manager that starts afterwards, and the watch's tick empties it; a withdrawal that did reach the store
// leaves nothing to find.
func TestAutoSupervisionOffRestartDoesNotResend(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	task, err := s.createTodo(ctx, "Restart example", "Verify", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"Verified"}, 0)
	g := superviseSignal{Todo: task.ID, Kind: sigInterrupted, Key: "restart", Detail: "cut off"}
	s.updateSupervisePending(func(p map[string]superviseSignal) { p[g.id()] = g })
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("not queued")
	}
	setAutoSupervisionQuietly(t, s, false) // saved off, the queue still on disk

	again, hub := restartedService(t, st)
	again.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	if again.autoSupervisionOn(ctx) {
		t.Fatal("the restarted manager lost the switch")
	}
	hub.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	again.relays.mu.Lock()
	loaded := again.relayLocked(ctx)
	queuedOnDisk := len(again.relays.st.Queue["conv1"])
	again.relays.mu.Unlock()
	if !loaded || queuedOnDisk != 1 {
		t.Fatalf("fixture: the saved queue holds %d message(s) (loaded=%v)", queuedOnDisk, loaded)
	}
	drainQueuedDigest(t, again, hub)
	if len(hub.notices()) != 0 || again.queuedFor("conv1") != 0 {
		t.Fatalf("a restarted manager sent a queued supervision message while off: %q, queued=%d", hub.notices(), again.queuedFor("conv1"))
	}
	var saved relayState
	if _, err := st.Setting(ctx, relayKey, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Queue["conv1"]) != 0 || len(saved.SupervisePending) != 0 {
		t.Fatalf("the saved queue still holds %d message(s), %d gathered signal(s)", len(saved.Queue["conv1"]), len(saved.SupervisePending))
	}

	// The watch's tick applies a switch that was left off, with a queue the drain has not reached.
	third, hub3 := newQuietServiceOn(t, &memStore{})
	third.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	hub3.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	t3, _ := third.createTodo(ctx, "Tick example", "Verify", "conv1", "")
	third.superviseTodo(ctx, t3.ID, "conv1", []string{"Verified"}, 0)
	g3 := superviseSignal{Todo: t3.ID, Kind: sigInterrupted, Key: "tick", Detail: "cut off"}
	third.updateSupervisePending(func(p map[string]superviseSignal) { p[g3.id()] = g3 })
	third.deliverDigest(ctx, third.settings(ctx), "conv1", []superviseSignal{g3}, "", time.Now(), time.Time{})
	setAutoSupervisionQuietly(t, third, false)
	third.superviseWatch(ctx, time.Now())
	if n, _ := queuedSupervise(third, "conv1"); n != 0 {
		t.Fatalf("the watch tick left %d queued", n)
	}
}

// A task leader's Agent is told by session.send: the same switch, with the same outcome, and the retry loop that could
// keep a failed message for a day does not start.
func TestAutoSupervisionOffLeaderMessageIsSuppressed(t *testing.T) {
	s, f, _ := newSupervisedService(t)
	lead := TodoAgent{MachineID: "box", SessionID: "leader1"}
	setAutoSupervisionQuietly(t, s, false)
	calls := f.callCount()
	if err := s.sendToLeader(context.Background(), lead, "text", nil, nil, nil, nil); !relaySuppressed(err) {
		t.Fatalf("sendToLeader = %v, want suppressed", err)
	}
	if err := s.sendToLeaderNow(context.Background(), lead, "text", nil); !relaySuppressed(err) {
		t.Fatalf("sendToLeaderNow = %v, want suppressed", err)
	}
	s.escalateLeaderMessage(lead, nil, nil, nil, "text", false, nil) // returns at once: no goroutine keeps trying
	if got := f.callCount() - calls; got != 0 {
		t.Fatalf("%d node call(s) for a suppressed leader message", got)
	}
}

// The node's waiting input: turning the switch off asks the nodes to cancel the supervision input of conversations
// that have a queue; a node that comes back later is asked as soon as it lists them, and an ordinary session list asks
// for nothing while the switch is on.
func TestAutoSupervisionOffCancelsNodeQueuedInput(t *testing.T) {
	s, f, _ := newSupervisedService(t)
	cancels := func() []string {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []string
		for _, c := range f.calls {
			if c.Method == deviceAssistantCancelSupervision {
				var a map[string]any
				_ = json.Unmarshal(c.Params, &a)
				out = append(out, c.Device+"/"+a["session_id"].(string))
			}
		}
		return out
	}
	queued := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking, Queued: []string{"督办消息"}}
	idle := proto.Session{SessionID: "conv9", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	agent := proto.Session{SessionID: "s1", MachineID: "server", Status: proto.StatusWorking, Queued: []string{"typed ahead"}}
	f.setSessions("server", queued, idle, agent)
	s.drainRelays("server", []proto.Session{queued, idle, agent}) // on: nothing asked
	time.Sleep(50 * time.Millisecond)                             // sleep-ok: a negative check, no cancel may be asked; there is no event to wait for
	if got := cancels(); len(got) != 0 {
		t.Fatalf("asked to cancel while on: %v", got)
	}
	putAutoSupervision(t, s, false)
	waitFor(t, "the cancel for the conversation with a queue", func() bool { return len(cancels()) == 1 })
	if got := cancels(); got[0] != "server/conv1" {
		t.Fatalf("cancel went to %v, want only the assistant conversation with a waiting queue", got)
	}
	// A reconnect: another conversation's queue appears in the list the manager gets.
	late := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusWorking, Queued: []string{"督办消息"}}
	f.setSessions("server", queued, late)
	s.drainRelays("server", []proto.Session{queued, late})
	waitFor(t, "the cancel after the reconnect", func() bool { return len(cancels()) == 2 })
	if got := cancels(); got[1] != "server/conv2" {
		t.Fatalf("cancels = %v, want conv2 asked after the reconnect (conv1 is within its own gap)", got)
	}
	// An older node that does not know the method answers with an error: nothing breaks.
	s.supervise.off.mu.Lock()
	s.supervise.off.cancel = nil
	s.supervise.off.mu.Unlock()
	s.cancelNodeSupervisionOn(context.Background(), "server", []proto.Session{queued})
	// Turned back on, nothing is asked.
	putAutoSupervision(t, s, true)
	n := len(cancels())
	s.drainRelays("server", []proto.Session{late})
	time.Sleep(50 * time.Millisecond) // sleep-ok: a negative check, no cancel may be asked; there is no event to wait for
	if len(cancels()) != n {
		t.Fatal("asked to cancel while on again")
	}
}

// What the switch must not touch: the task ledger keeps following the agent, the owner's own review (task_review) works
// and closes the task, and approval cards are raised and answered.
func TestAutoSupervisionOffKeepsLedgerReviewAndApprovals(t *testing.T) {
	ctx := context.Background()
	s, f, todo := newSupervisedService(t)
	putAutoSupervision(t, s, false)
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/901 已合并", 0)
	waitFor(t, "the ledger moving to review", func() bool {
		v := todoNow(s, todo.ID).Supervision
		return v != nil && v.State == reviewPending
	})
	time.Sleep(3 * shortSuperviseTimings.every) // sleep-ok: a negative check over several digest ticks; no event to wait for
	if n := f.notices(); len(n) != 0 {
		t.Fatalf("a supervision message went out while off: %q", n)
	}
	// The owner's active query and acceptance: the review tool answers and closes the task.
	if out, err := s.toolTodoList(ctx, json.RawMessage(`{}`)); err != nil || !strings.Contains(out, todo.ID) {
		t.Fatalf("listing the tasks: %v %q", err, out)
	}
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "checked by hand",
		"checks": completeReviewChecks(todoNow(s, todo.ID))})
	if res, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("the owner's acceptance while off: %q %v", res, err)
	}
	if done := todoNow(s, todo.ID); done.DoneAt == nil {
		t.Fatalf("not closed: %+v", done.Supervision)
	}
	// An approval card is raised and answered as always, on an assistant that has the switch off too.
	approver := newTestService(t, newFakeHub().withMachine())
	putAutoSupervision(t, approver, false)
	go func() {
		q, ok := awaitCard(approver)
		if !ok {
			return
		}
		allowed := true
		answer, _ := json.Marshal(proto.Resolution{Allow: &allowed})
		if _, err := approver.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
			t.Errorf("approve: %v", err)
		}
	}()
	res := approver.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(`{"action":"set","key":"assistant.supervision_batch_minutes","value":45}`), MachineID: "server", SessionID: "conv1"})
	if res.IsError || !strings.Contains(res.Content[0].Text, "45") {
		t.Fatalf("an approval card while off: %+v", res)
	}
}

// The background supervisor's migration: a supervision message queued for a background conversation that is gone is not
// moved or told again while the switch is off.
func TestAutoSupervisionOffBackgroundMigrationTellsNothing(t *testing.T) {
	ctx := context.Background()
	s, f, todo := newSupervisedService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	g := superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "bg", Detail: "cut off"}
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.setQueuedLocked("bg1", []superviseSignal{g})
	s.supervise.mu.Unlock()
	setAutoSupervisionQuietly(t, s, false)
	s.backgroundStranded(ctx)
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("the migration told what was lost: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	if len(f.notices()) != 0 {
		t.Fatalf("sent %q", f.notices())
	}
}

// persona_settings reads and changes the switch like the other assistant switches: a set shows a phone card with
// before and after, nothing changes until it is approved, and the approved change applies at once (what waited is
// withdrawn), the same as the PUT the web settings page uses.
func TestAutoSupervisionPersonaSettingsToolSwitchesAtOnce(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	task, err := s.createTodo(ctx, "Switch example", "Verify", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"Verified"}, 0)
	g := superviseSignal{Todo: task.ID, Kind: sigInterrupted, Key: "queued", Detail: "cut off"}
	s.updateSupervisePending(func(p map[string]superviseSignal) { p[g.id()] = g })
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("not queued")
	}
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	decide := func(allow bool, wantCard string) {
		go func() {
			q, ok := awaitCard(s)
			if !ok {
				return
			}
			if wantCard != "" && q.Summary != wantCard {
				t.Errorf("approval card: %q want %q", q.Summary, wantCard)
			}
			answer, _ := json.Marshal(proto.Resolution{Allow: &allow})
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
				t.Errorf("answer: %v", err)
			}
		}()
	}
	if got, isErr := tool(`{"action":"get","key":"assistant.auto_supervision_messages"}`); isErr || got != "true" {
		t.Fatalf("default read: %q error=%v", got, isErr)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":"off"}`); !isErr {
		t.Fatalf("a value that is not true or false was accepted: %q", out)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":true}`); isErr || len(s.pendingQuestions()) != 0 {
		t.Fatalf("the value in effect should be a no-op: %q error=%v", out, isErr)
	}

	// Declined: nothing changes and what waits stays.
	decide(false, "")
	if out, _ := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":false}`); !strings.Contains(out, "declined") {
		t.Fatalf("a declined change did not say so: %q", out)
	}
	if !s.autoSupervisionOn(ctx) {
		t.Fatal("a declined card turned the switch off")
	}
	if n, _ := queuedSupervise(s, "conv1"); n != 1 {
		t.Fatalf("a declined card withdrew the queue: %d", n)
	}

	// Approved: it reads back off, the queue is withdrawn, and the web view agrees.
	decide(true, i18n.T(i18n.Default, "assistant-settings_tool.change_auto_supervision_messages", i18n.Args{"before": i18n.T(i18n.Default, "assistant-settings_tool.on"), "after": i18n.T(i18n.Default, "assistant-settings_tool.off")}))
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":false}`); isErr {
		t.Fatalf("approved change: %q", out)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.auto_supervision_messages"}`); got != "false" || s.autoSupervisionOn(ctx) {
		t.Fatalf("read back after the change: %q", got)
	}
	if getAssistantView(t, s)["auto_supervision_messages"] != false {
		t.Fatal("the assistant's settings view disagrees with persona_settings")
	}
	if n, _ := queuedSupervise(s, "conv1"); n != 0 {
		t.Fatalf("what waited was not withdrawn: %d", n)
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("gathered after the change: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	pushes := len(f.pushes)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || len(f.pushes) != pushes {
		t.Fatalf("a supervision message went out with the switch off: sends=%q", f.notices())
	}

	// Back on through the tool: it reads on again and replays nothing.
	decide(true, "")
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":true}`); isErr {
		t.Fatalf("turning it on: %q", out)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.auto_supervision_messages"}`); got != "true" {
		t.Fatalf("read back after turning on: %q", got)
	}
	if n, _ := queuedSupervise(s, "conv1"); n != 0 {
		t.Fatalf("turning it on replayed %d withdrawn message(s)", n)
	}
	if settingTitles["assistant.auto_supervision_messages"] != "title_auto_supervision_messages" {
		t.Fatal("no approval-card title for assistant.auto_supervision_messages")
	}
}

// ---- the three modes: all, outcomes, off (#16417) ----

// putAssistant writes body to PUT /api/v1/assistant and answers the status and the view.
func putAssistant(t *testing.T, s *Service, body string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
	var view map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	return rec.Code, view
}

func putSupervisionMode(t *testing.T, s *Service, mode string) map[string]any {
	t.Helper()
	code, view := putAssistant(t, s, `{"auto_supervision_mode":"`+mode+`"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT mode %s: %d %v", mode, code, view)
	}
	return view
}

func savedSupervision(t *testing.T, s *Service) (string, *bool) {
	t.Helper()
	v := s.settings(context.Background())
	return v.AutoSupervisionMode, v.AutoSupervisionMessages
}

func TestSupervisionModeOfFallsBackToTheSwitch(t *testing.T) {
	on, off := true, false
	for _, c := range []struct {
		name string
		v    Settings
		want string
	}{
		{"unset", Settings{}, supervisionModeAll},
		{"switch on", Settings{AutoSupervisionMessages: &on}, supervisionModeAll},
		{"switch off", Settings{AutoSupervisionMessages: &off}, supervisionModeOff},
		{"mode wins", Settings{AutoSupervisionMode: supervisionModeOutcomes, AutoSupervisionMessages: &off}, supervisionModeOutcomes},
		{"mode off", Settings{AutoSupervisionMode: supervisionModeOff}, supervisionModeOff},
		{"unknown mode falls back", Settings{AutoSupervisionMode: "later", AutoSupervisionMessages: &off}, supervisionModeOff},
	} {
		if got := supervisionModeOf(c.v); got != c.want {
			t.Errorf("%s: mode = %q, want %q", c.name, got, c.want)
		}
		if got := autoSupervisionOf(c.v); got != (c.want != supervisionModeOff) {
			t.Errorf("%s: switch = %v", c.name, got)
		}
	}
}

// Every signal kind, the way outcomes judges it: one-shot handoffs, stops, failures, explicit reports and decisions go,
// the routine checks while a task runs do not. all sends every one and off none.
func TestSupervisionSignalAllowedByMode(t *testing.T) {
	outcomes := []superviseSignal{
		{Kind: sigReview}, {Kind: sigHandoffResult}, {Kind: sigParentCloseout}, {Kind: sigPRMerged}, {Kind: sigIdle},
		{Kind: sigUnjudged}, {Kind: sigFailed}, {Kind: sigInterrupted}, {Kind: sigTimedOut}, {Kind: sigAllStopped},
		{Kind: sigOrphaned}, {Kind: sigRecovered}, {Kind: sigNeedsYou}, {Kind: sigAsk}, {Kind: sigDecide},
		{Kind: sigEscalated}, {Kind: sigDecisionReply}, {Kind: sigUndecidable}, {Kind: sigDeliveryHold},
		{Kind: sigOwnerCannot}, {Kind: sigCardlessWait}, {Kind: sigNeedsUserExternalInfo}, {Kind: sigParkDecided},
		{Kind: sigParkedDue}, {Kind: sigRecheck}, {Kind: sigOwnerPresent}, {Kind: sigOutageRecovery}, {Kind: sigTodoReady},
		{Kind: sigBlockersUnblocked}, {Kind: sigMergeTrainTurn}, {Kind: sigWriteConflict},
		{Kind: sigReport, Report: upReportDone}, {Kind: sigReport, Report: upReportBlocked},
		{Kind: sigReport, Report: upReportQuestion}, {Kind: sigCheck, Report: checkDecidedReport},
	}
	routine := []superviseSignal{
		{Kind: sigStalled}, {Kind: sigNoProgress}, {Kind: sigLeaderStuck}, {Kind: sigEstimateDue}, {Kind: sigOverdue},
		{Kind: sigReviewNudge}, {Kind: sigStarted}, {Kind: sigParkedSummary}, {Kind: sigPRConflict}, {Kind: sigDraftPR},
		{Kind: sigPREvent}, {Kind: sigInbox}, {Kind: sigReport, Report: upReportProgress}, {Kind: sigReport},
		{Kind: sigCheck, Report: checkNoProgressReport}, {Kind: sigCheck, Report: checkErrorReport},
	}
	for _, g := range outcomes {
		if !supervisionSignalAllowed(supervisionModeOutcomes, g) {
			t.Errorf("outcomes dropped %s/%s", g.Kind, g.Report)
		}
	}
	for _, g := range routine {
		if supervisionSignalAllowed(supervisionModeOutcomes, g) {
			t.Errorf("outcomes sent the routine %s/%s", g.Kind, g.Report)
		}
	}
	for _, g := range append(outcomes, routine...) {
		if !supervisionSignalAllowed(supervisionModeAll, g) || supervisionSignalAllowed(supervisionModeOff, g) {
			t.Errorf("%s/%s: all must send it and off must not", g.Kind, g.Report)
		}
	}
	// A message is judged by what it carries: one outcome among routine checks is enough; legacy text is sent unless off.
	mixed := []superviseSignal{{Kind: sigStalled}, {Kind: sigReview}}
	if !supervisionSignalsAllowed(supervisionModeOutcomes, mixed) || supervisionSignalsAllowed(supervisionModeOutcomes, routine) {
		t.Fatal("outcomes judged a message by the wrong member")
	}
	routineMsg := relayMsg{Tool: superviseTool, Text: "x", Signals: encodeDigestSignals(routine)}
	legacy := relayMsg{Tool: superviseTool, Text: "queued before signals"}
	other := relayMsg{Tool: "conversation_send", Text: "hello"}
	if supervisionRelayAllowed(supervisionModeOutcomes, routineMsg) || !supervisionRelayAllowed(supervisionModeOutcomes, legacy) ||
		supervisionRelayAllowed(supervisionModeOff, legacy) || !supervisionRelayAllowed(supervisionModeOff, other) {
		t.Fatal("relay judgement")
	}
}

// The API: the mode is read and written with the older switch kept in step both ways; an unknown mode is refused; a
// saved older switch with no mode still decides; and the mode survives a restart.
func TestAutoSupervisionModeAPIKeepsTheSwitchCompatible(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	if v := getAssistantView(t, s); v["auto_supervision_mode"] != "all" || v["auto_supervision_messages"] != true {
		t.Fatalf("default view: mode=%v messages=%v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
	if v := putSupervisionMode(t, s, "outcomes"); v["auto_supervision_mode"] != "outcomes" || v["auto_supervision_messages"] != true {
		t.Fatalf("outcomes view: %v %v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
	if mode, on := savedSupervision(t, s); mode != "outcomes" || on == nil || !*on {
		t.Fatalf("saved outcomes: mode=%q switch=%v", mode, on)
	}
	if code, _ := putAssistant(t, s, `{"auto_supervision_mode":"sometimes"}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown mode: %d, want 400", code)
	}
	if v := getAssistantView(t, s); v["auto_supervision_mode"] != "outcomes" {
		t.Fatalf("a refused write changed it: %v", v["auto_supervision_mode"])
	}
	// The mode wins over the switch in the same body.
	if _, v := putAssistant(t, s, `{"auto_supervision_mode":"outcomes","auto_supervision_messages":false}`); v["auto_supervision_mode"] != "outcomes" || v["auto_supervision_messages"] != true {
		t.Fatalf("both in one body: %v %v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
	again, _ := restartedService(t, st)
	if again.supervisionMode(ctx) != supervisionModeOutcomes || getAssistantView(t, again)["auto_supervision_mode"] != "outcomes" {
		t.Fatal("outcomes did not survive a restart")
	}
	if v := putSupervisionMode(t, again, "off"); v["auto_supervision_mode"] != "off" || v["auto_supervision_messages"] != false {
		t.Fatalf("off view: %v %v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
	if mode, on := savedSupervision(t, again); mode != "off" || on == nil || *on {
		t.Fatalf("saved off: mode=%q switch=%v", mode, on)
	}
	// An older client that knows only the switch: true is all, false off.
	if v := putAutoSupervision(t, again, true); v["auto_supervision_mode"] != "all" {
		t.Fatalf("switch on: mode %v", v["auto_supervision_mode"])
	}
	putSupervisionMode(t, again, "outcomes")
	if v := putAutoSupervision(t, again, false); v["auto_supervision_mode"] != "off" || again.autoSupervisionOn(ctx) {
		t.Fatalf("switch off from outcomes: mode %v", v["auto_supervision_mode"])
	}
	// What an older hub saved (the switch only) still decides.
	old, _ := newQuietServiceOn(t, &memStore{})
	if _, err := old.updateSettings(ctx, func(v *Settings) error { off := false; v.AutoSupervisionMessages = &off; return nil }); err != nil {
		t.Fatal(err)
	}
	if v := getAssistantView(t, old); v["auto_supervision_mode"] != "off" || v["auto_supervision_messages"] != false {
		t.Fatalf("an older saved switch: %v %v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
}

// outcomes gathers and sends a task's stop, and gathers nothing of the routine checks while it runs.
func TestAutoSupervisionOutcomesSendsOutcomesOnly(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	putSupervisionMode(t, s, "outcomes")
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "o-stalled", Minutes: 50})
	s.queueSignal(superviseSignal{Todo: todo.ID, Kind: sigNoProgress, Key: "o-noprogress"})
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReport, Report: upReportProgress, Key: "o-progress"})
	s.restoreDigest([]superviseSignal{{Todo: todo.ID, Kind: sigEstimateDue, Key: "o-estimate"}}, time.Now(), time.Time{})
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("outcomes gathered a routine check: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "o-interrupted", Detail: "cut off in outcomes"})
	waitFor(t, "the stop's message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "cut off in outcomes") {
		t.Fatalf("message: %s", n)
	}
	time.Sleep(3 * shortSuperviseTimings.every) // sleep-ok: a negative check over several digest ticks; no event to wait for
	if n := f.notices(); len(n) != 1 {
		t.Fatalf("outcomes sent %d message(s), want only the stop: %q", len(n), n)
	}
}

// queueRoutineOnly puts a supervision message made only of routine checks in conversation to's saved queue.
func queueRoutineOnly(t *testing.T, s *Service, to string, g superviseSignal) {
	t.Helper()
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		t.Fatal("the relay queue cannot be read")
	}
	if s.relays.st.Queue == nil {
		s.relays.st.Queue = map[string][]relayMsg{}
	}
	s.relays.st.Queue[to] = append(s.relays.st.Queue[to], relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool,
		Text: "routine only", Signals: encodeDigestSignals([]superviseSignal{g}), At: time.Now().UTC()})
	if err := s.saveRelayLocked(); err != nil {
		t.Fatal(err)
	}
}

// all → outcomes withdraws only what outcomes drops: the routine signals gathered in memory and in the durable outbox,
// and a queued message made only of them; a queued outcome wake-up stays and is sent, the node is not asked to cancel
// anything, and switching back to all replays nothing.
func TestAutoSupervisionAllToOutcomesWithdrawsOnlyRoutine(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Outcome example") // conv1 waits with an interrupted + unjudged digest
	todo := tasks[0]
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "w-stalled", Minutes: 50})
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "w-review"})
	s.updateSupervisePending(func(p map[string]superviseSignal) {
		g := superviseSignal{Todo: todo.ID, Kind: sigEstimateDue, Key: "w-estimate"}
		p[g.id()] = g
	})
	routine := superviseSignal{Todo: todo.ID, Kind: sigNoProgress, Key: "w-noprogress"}
	queueRoutineOnly(t, s, "conv2", routine)
	s.supervise.mu.Lock()
	s.supervise.init()
	s.supervise.setQueuedLocked("conv2", []superviseSignal{routine})
	s.supervise.mu.Unlock()
	if mem, durable, _ := gatheredSignals(s); mem != 2 || durable != 3 {
		t.Fatalf("fixture: memory=%d durable=%d", mem, durable)
	}
	calls := f.callCount()

	putSupervisionMode(t, s, "outcomes")
	s.supervise.mu.Lock()
	_, stalled := s.supervise.pending[todo.ID+"|"+sigStalled]
	_, review := s.supervise.pending[todo.ID+"|"+sigReview]
	_, indexed := s.supervise.queued["conv2"]
	s.supervise.mu.Unlock()
	if stalled || !review {
		t.Fatalf("memory after outcomes: stalled=%v review=%v", stalled, review)
	}
	s.relays.mu.Lock()
	durable := maps.Clone(s.relays.st.SupervisePending)
	conv2 := len(s.relays.st.Queue["conv2"])
	s.relays.mu.Unlock()
	for id, g := range durable {
		if !supervisionSignalAllowed(supervisionModeOutcomes, g) {
			t.Errorf("the durable outbox kept the routine %s", id)
		}
	}
	if _, ok := durable[todo.ID+"|"+sigReview]; !ok {
		t.Fatal("the durable outbox lost the review")
	}
	if conv2 != 0 || indexed {
		t.Fatalf("the routine-only message stayed: queued=%d indexed=%v", conv2, indexed)
	}
	if n, withdrawn := queuedSupervise(s, "conv1"); n != 1 || withdrawn != 0 {
		t.Fatalf("the queued outcome wake-up: %d queued, %d withdrawn", n, withdrawn)
	}
	for _, c := range f.calls[calls:] {
		if c.Method == deviceAssistantCancelSupervision {
			t.Fatal("outcomes asked a node to cancel its waiting input")
		}
	}
	drainQueuedDigest(t, s, f)
	waitFor(t, "the queued outcome wake-up", func() bool { return len(f.notices()) >= 1 })
	if n := f.notices()[0]; !strings.Contains(n, "Outcome example") {
		t.Fatalf("the queued outcome message: %s", n)
	}

	// Back to all: what outcomes withdrew does not come back.
	putSupervisionMode(t, s, "all")
	s.supervise.mu.Lock()
	_, back := s.supervise.pending[todo.ID+"|"+sigStalled]
	s.supervise.mu.Unlock()
	s.relays.mu.Lock()
	_, backDurable := s.relays.st.SupervisePending[todo.ID+"|"+sigEstimateDue]
	s.relays.mu.Unlock()
	if back || backDurable {
		t.Fatalf("switching back to all replayed a withdrawn signal: memory=%v durable=%v", back, backDurable)
	}
}

// A queued message that carries an outcome among routine checks stays whole; one in flight made only of routine checks
// keeps its head, withdrawn with nothing left to replay.
func TestAutoSupervisionOutcomesKeepsMixedAndMarksInFlightRoutine(t *testing.T) {
	s, _, tasks := queuedDigestFixture(t, "Mixed example")
	todo := tasks[0]
	s.relays.mu.Lock()
	head := s.relays.st.Queue["conv1"][0]
	signals, _ := decodeDigestSignals(head.Signals)
	head.Signals = encodeDigestSignals(append(signals, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "m-stalled"}))
	s.relays.st.Queue["conv1"][0] = head
	s.relays.draining["conv3"] = true
	s.relays.mu.Unlock()
	queueRoutineOnly(t, s, "conv3", superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "m-inflight"})
	putSupervisionMode(t, s, "outcomes")
	s.relays.mu.Lock()
	mixed := s.relays.st.Queue["conv1"]
	inflight := s.relays.st.Queue["conv3"]
	delete(s.relays.draining, "conv3")
	s.relays.mu.Unlock()
	if len(mixed) != 1 || mixed[0].Withdrawn || mixed[0].Signals != head.Signals {
		t.Fatalf("the mixed message changed: %+v", mixed)
	}
	if len(inflight) != 1 || !inflight[0].Withdrawn {
		t.Fatalf("the in-flight routine head: %+v", inflight)
	}
	if got, structured := decodeDigestSignals(inflight[0].Signals); !structured || len(got) != 0 {
		t.Fatalf("the in-flight routine head would replay %q", inflight[0].Signals)
	}
}

// outcomes → off withdraws everything, outcomes included, and asks the nodes to cancel, exactly as off always did.
func TestAutoSupervisionOutcomesToOffWithdrawsEverything(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Off after outcomes")
	putSupervisionMode(t, s, "outcomes")
	s.signal(superviseSignal{Todo: tasks[0].ID, Kind: sigReview, Key: "x-review"})
	if mem, _, _ := gatheredSignals(s); mem != 1 {
		t.Fatalf("fixture: memory=%d", mem)
	}
	queued := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking, Queued: []string{"督办消息"}}
	f.setSessions("server", queued)
	putSupervisionMode(t, s, "off")
	if n, _ := queuedSupervise(s, "conv1"); n != 0 {
		t.Fatalf("off left %d queued", n)
	}
	if mem, durable, timer := gatheredSignals(s); mem != 0 || durable != 0 || timer {
		t.Fatalf("off left: memory=%d durable=%d timer=%v", mem, durable, timer)
	}
	waitFor(t, "the node cancel", func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.calls {
			if c.Method == deviceAssistantCancelSupervision {
				return true
			}
		}
		return false
	})
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 {
		t.Fatalf("sent while off: %q", f.notices())
	}
}

// The delivery boundaries under outcomes: a routine-only supervision message is neither queued nor sent, an outcome or
// a legacy text message is, and an ordinary relay is never touched.
func TestAutoSupervisionOutcomesRelayBoundaries(t *testing.T) {
	ctx := context.Background()
	s, f, todo := newSupervisedService(t)
	setSupervisionModeQuietly(t, s, supervisionModeOutcomes)
	routine := relayMsg{From: superviseTool, Tool: superviseTool, Text: "routine", At: time.Now().UTC(),
		Signals: encodeDigestSignals([]superviseSignal{{Todo: todo.ID, Kind: sigStalled, Key: "b-stalled"}})}
	if _, err := s.queueRelayDurable("conv1", routine); !relaySuppressed(err) {
		t.Fatalf("queueRelayDurable(routine) = %v, want suppressed", err)
	}
	if err := s.deliverRelay(ctx, "server", "conv1", routine); !relaySuppressed(err) {
		t.Fatalf("deliverRelay(routine) = %v, want suppressed", err)
	}
	if len(f.notices()) != 0 {
		t.Fatalf("a routine message went out: %q", f.notices())
	}
	outcome := relayMsg{From: superviseTool, Tool: superviseTool, Text: "outcome wake-up", At: time.Now().UTC(),
		Signals: encodeDigestSignals([]superviseSignal{{Todo: todo.ID, Kind: sigFailed, Key: "b-failed"}})}
	if err := s.deliverRelay(ctx, "server", "conv1", outcome); relaySuppressed(err) {
		t.Fatalf("deliverRelay(outcome) suppressed")
	}
	legacy := relayMsg{From: superviseTool, Tool: superviseTool, Text: "legacy text", At: time.Now().UTC()}
	if err := s.deliverRelay(ctx, "server", "conv1", legacy); relaySuppressed(err) {
		t.Fatalf("deliverRelay(legacy) suppressed")
	}
	if n := f.notices(); len(n) != 2 {
		t.Fatalf("outcome and legacy messages: %q", n)
	}
}

// A task leader's Agent under outcomes: a message of routine checks only is not sent and starts no retry loop; one that
// carries an outcome goes to the leader's session.
func TestAutoSupervisionOutcomesLeaderRouting(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	lead := TodoAgent{MachineID: "box", SessionID: "leader1"}
	setSupervisionModeQuietly(t, s, supervisionModeOutcomes)
	routine := []superviseSignal{{Todo: todo.ID, Kind: sigNoProgress, Key: "l-noprogress"}}
	calls := f.callCount()
	if err := s.sendToLeader(context.Background(), lead, "text", routine, routine, nil, nil); !relaySuppressed(err) {
		t.Fatalf("sendToLeader(routine) = %v, want suppressed", err)
	}
	if err := s.sendToLeaderNow(context.Background(), lead, "text", routine); !relaySuppressed(err) {
		t.Fatalf("sendToLeaderNow(routine) = %v, want suppressed", err)
	}
	s.escalateLeaderMessage(lead, routine, routine, nil, "text", false, nil) // returns at once
	if got := f.callCount() - calls; got != 0 {
		t.Fatalf("%d node call(s) for a routine leader message", got)
	}
	outcome := []superviseSignal{{Todo: todo.ID, Kind: sigReview, Key: "l-review"}}
	if err := s.sendToLeaderNow(context.Background(), lead, "text", outcome); relaySuppressed(err) {
		t.Fatal("sendToLeaderNow(outcome) suppressed")
	}
	if f.callCount() == calls {
		t.Fatal("an outcome leader message was not sent")
	}
}

// persona_settings: assistant.auto_supervision_mode reads the mode, refuses an unknown one, changes on an approved
// phone card that names both modes, and agrees with the web view; the older switch's key still turns it off.
func TestAutoSupervisionModePersonaSettingsTool(t *testing.T) {
	ctx := context.Background()
	s, _ := newQuietServiceOn(t, &memStore{})
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	approve := func(wantCard string) {
		go func() {
			q, ok := awaitCard(s)
			if !ok {
				return
			}
			if wantCard != "" && q.Summary != wantCard {
				t.Errorf("approval card: %q want %q", q.Summary, wantCard)
			}
			allow := true
			answer, _ := json.Marshal(proto.Resolution{Allow: &allow})
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
				t.Errorf("answer: %v", err)
			}
		}()
	}
	if got, isErr := tool(`{"action":"get","key":"assistant.auto_supervision_mode"}`); isErr || got != "all" {
		t.Fatalf("default read: %q error=%v", got, isErr)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_mode","value":"sometimes"}`); !isErr {
		t.Fatalf("an unknown mode was accepted: %q", out)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_mode","value":"all"}`); isErr || len(s.pendingQuestions()) != 0 {
		t.Fatalf("the mode in effect should be a no-op: %q error=%v", out, isErr)
	}
	name := func(m string) string { return i18n.T(i18n.Default, "assistant-settings_tool.auto_supervision_mode_"+m) }
	approve(i18n.T(i18n.Default, "assistant-settings_tool.change_auto_supervision_mode", i18n.Args{"before": name("all"), "after": name("outcomes")}))
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_mode","value":"outcomes"}`); isErr {
		t.Fatalf("approved change: %q", out)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.auto_supervision_mode"}`); got != "outcomes" {
		t.Fatalf("read back: %q", got)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.auto_supervision_messages"}`); got != "true" {
		t.Fatalf("the older key under outcomes: %q", got)
	}
	if v := getAssistantView(t, s); v["auto_supervision_mode"] != "outcomes" || v["auto_supervision_messages"] != true {
		t.Fatalf("the web view disagrees: %v %v", v["auto_supervision_mode"], v["auto_supervision_messages"])
	}
	if mode, on := savedSupervision(t, s); mode != "outcomes" || on == nil || !*on {
		t.Fatalf("saved: mode=%q switch=%v", mode, on)
	}
	// The older key still works: true is in effect under outcomes (a no-op), false turns everything off.
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":true}`); isErr || len(s.pendingQuestions()) != 0 {
		t.Fatalf("true under outcomes should be a no-op: %q error=%v", out, isErr)
	}
	approve("")
	if out, isErr := tool(`{"action":"set","key":"assistant.auto_supervision_messages","value":false}`); isErr {
		t.Fatalf("the older key: %q", out)
	}
	if got, _ := tool(`{"action":"get","key":"assistant.auto_supervision_mode"}`); got != "off" {
		t.Fatalf("the older key did not turn the mode off: %q", got)
	}
	if settingTitles["assistant.auto_supervision_mode"] != "title_auto_supervision_mode" {
		t.Fatal("no approval-card title for assistant.auto_supervision_mode")
	}
}
