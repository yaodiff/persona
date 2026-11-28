package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// newBackgroundService is newSupervisedService with the background supervisor on, as it is by default: the main
// conversation conv1 handed out one task (agent s1 on box). The conversations the fake starts get ids from s101 on, so
// the background supervisor's is not the agent's.
func newBackgroundService(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	return newBackgroundServiceOn(t, nil)
}

func newBackgroundServiceOn(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedServiceOn(t, st)
	s.noBackground = false
	f.mu.Lock()
	f.made = 100
	f.mu.Unlock()
	return s, f, todo
}

// tellLegacyBackground delivers a real pre-#5732 signal to an existing background conversation, granting its closeout
// permission for that task. Tests of legacy model, decision and review behavior use this rather than bypassing the gate.
func tellLegacyBackground(t *testing.T, s *Service, f *fakeHub, todoID, background string) {
	t.Helper()
	s.signal(superviseSignal{Todo: todoID, Kind: sigStalled, Key: "legacy-notice", To: background, Minutes: 50, At: time.Now()})
	waitFor(t, "the legacy background notification", func() bool {
		return strings.Contains(strings.Join(f.noticesTo(background), "\n"), todoID)
	})
	// Visibility precedes deliverDigest's grant defer. Use the production read barrier before inspecting or restarting.
	if !s.legacyBackgroundNotice(context.Background(), background, todoID) {
		t.Fatal("the visible legacy notice did not publish its closeout grant")
	}
}

// conversationStarts are the assistant.send calls that started a conversation (no session id).
func conversationStarts(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, a := range f.sends {
		if id, _ := a["session_id"].(string); id == "" {
			out = append(out, a)
		}
	}
	return out
}

// New task signals stay with main even when the legacy background setting is on: no background conversation is
// started. Main reviews the task and its ledger and origin stay unchanged (#5732).
func TestMainSupervisesWithoutStartingBackground(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	if got := s.settings(ctx).BackgroundSupervisor; got != "" {
		t.Fatalf("the background supervisor %q was made before anything was for it", got)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	waitFor(t, "main's supervision message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	n := f.noticesTo("conv1")[0]
	for _, want := range []string{"📋 督办", "待逐项复核，不代表完成", "pull/7"} {
		if !strings.Contains(n, want) {
			t.Errorf("main's message lacks %q:\n%s", want, n)
		}
	}
	if got := s.settings(ctx).BackgroundSupervisor; got != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("a background supervisor %q was started for a new task", got)
	}

	// Nothing is written on the task for a background supervisor.
	if cur := todoNow(s, todo.ID); cur.Origin != "conv1" || cur.Supervision.Supervisor != "conv1" {
		t.Fatalf("the task moved: origin %q, supervisor %q", cur.Origin, cur.Supervision.Supervisor)
	}

	review := func(self, verdict, note string) (string, error) {
		args := map[string]any{"todo_id": todo.ID, "verdict": verdict, "note": note}
		if verdict == reviewAccepted {
			args["checks"] = completeReviewChecks(todoNow(s, todo.ID))
		}
		return runJSON(t, s, self, "task_review", args)
	}
	if _, err := review("conv2", "rework", "x"); err == nil {
		t.Fatal("another side conversation reviewed the task")
	}
	if res, err := review("conv1", "rework", "补测试"); err != nil || !strings.Contains(res, "Rework round 1") {
		t.Fatalf("main's first rework: %s %v", res, err)
	}
	if sent := sentToAgent(f); len(sent) != 1 || !strings.Contains(sent[0], "补测试") {
		t.Fatalf("sent to the agent: %q", sent)
	}
	// The main conversation may still review it, as it may a leader's task.
	if res, err := review("conv1", "rework", "再补深色截图"); err != nil || !strings.Contains(res, "Rework round 2") {
		t.Fatalf("the main conversation's rework: %s %v", res, err)
	}
	res, err := review("conv1", "accepted", "已合并，测试补了")
	if err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("main's accept: %s %v", res, err)
	}
	done := todoNow(s, todo.ID)
	if done.DoneAt == nil || done.Supervision.State != reviewAccepted || done.Supervision.Supervisor != "conv1" || done.Origin != "conv1" {
		t.Fatalf("accepted: %+v %+v", done, done.Supervision)
	}
	// Main's feed has both reworks and the acceptance; no background turn was involved.
	items, _, err := s.feed(ctx, "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Each review is main's own.
	var accepted bool
	by := map[string]string{} // each review's author, by its note
	for _, it := range items {
		var body reviewFeedBody
		if it.Kind != feedReview || it.Ref != todo.ID || json.Unmarshal(it.Body, &body) != nil {
			continue
		}
		by[body.Note] = body.By
		accepted = accepted || body.Verdict == reviewAccepted
	}
	if !accepted {
		t.Fatalf("the main conversation's feed has no accepted review: %+v", items)
	}
	if by["补测试"] != "" || by["已合并，测试补了"] != "" || by["再补深色截图"] != "" {
		t.Fatalf("main wrote each review: %+v", by)
	}
	if tasks := s.snapshotTasks(ctx, ""); len(tasks) != 1 || tasks[0].ID != todo.ID || !strings.Contains(tasks[0].Health, "已验收") {
		t.Fatalf("the snapshot's tasks: %+v", tasks)
	}
	settleService(t, s)
	if got := f.noticesTo("conv1"); len(got) != 1 {
		t.Fatalf("the acceptance took another turn of main: %q", got)
	}
}

// A legacy background conversation may finish only a task whose old notification actually reached it. It cannot accept
// a new main-supervised task merely because it is still configured (#5732).
func TestBackgroundReviewIsLimitedToLegacyNotices(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	review := func() (string, error) {
		return runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "legacy closeout", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	}
	if _, err := review(); err == nil {
		t.Fatal("the legacy background supervisor accepted a new task it was never notified about")
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Fatalf("the unauthorized review closed the new task: %+v", got)
	}

	tellLegacyBackground(t, s, f, todo.ID, "bg1")
	if len(conversationStarts(f)) != 0 {
		t.Fatal("delivering an old notice started a replacement background supervisor")
	}
	if res, err := review(); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("the old background supervisor could not finish its notified task: %s %v", res, err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.Supervision.State != reviewAccepted {
		t.Fatalf("the legacy task did not close: %+v", got)
	}
	settleService(t, s)
}

// A no-main Origin fallback is delivery only, never evidence of an explicit legacy background route. Both a visible
// delivery and a structured queue restored after restart must preserve that distinction (#5732).
func TestNoMainBackgroundOriginNoticeDoesNotGrantCloseout(t *testing.T) {
	for _, tc := range []struct {
		name        string
		queued      bool
		restoreMain bool
	}{
		{name: "direct"},
		{name: "queued", queued: true},
		{name: "queued_restore_main", queued: true, restoreMain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f := newQuietServiceOn(t, st)
			secretary(f)
			f.known["bg1"] = true
			status := proto.StatusIdle
			if tc.queued {
				status = proto.StatusWorking
			}
			f.setSessions("server", proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: status})
			setSettings(t, s, func(v *Settings) {
				v.Main = ""
				v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
			})
			if _, err := runJSON(t, s, "bg1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
				"task": map[string]any{"title": "拉周报数据", "done_when": []string{"表格链接贴回来"}}}); err != nil {
				t.Fatal(err)
			}
			todo := onlyTodo(s)
			if todo.Origin != "bg1" || todo.Supervision == nil || todo.Supervision.Supervisor != "" {
				t.Fatalf("the no-main dispatch changed provenance: %+v %+v", todo, todo.Supervision)
			}
			if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
				v.State = reviewPending
				return true
			}); err != nil {
				t.Fatal(err)
			}
			s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "no-main-origin-review", At: time.Now()})
			if tc.queued {
				waitFor(t, "the no-main Origin queue", func() bool { return s.queuedFor("bg1") == 1 })
			} else {
				waitFor(t, "the no-main Origin notification", func() bool {
					return strings.Contains(strings.Join(f.noticesTo("bg1"), "\n"), todo.ID)
				})
			}
			if tc.queued {
				s.relays.mu.Lock()
				raw := s.relays.st.Queue["bg1"][0].Signals
				s.relays.mu.Unlock()
				queued, structured := decodeDigestSignals(raw)
				if !structured || len(queued) != 1 || !queued[0].OriginOnly {
					t.Fatalf("the no-main route was not persisted with its queued signal: %+v", queued)
				}
			}
			if tc.restoreMain {
				setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
				if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
					v.Supervisor = "conv1"
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.queued {
				simulateSupervisionRestart(t, s)
			}

			current := todoNow(s, todo.ID)
			if s.backgroundSupervises(context.Background(), "bg1", current) {
				t.Error("the no-main Origin fallback became background acceptance authority")
			}
			res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "origin is not acceptance", "checks": completeReviewChecks(current)})
			if err == nil {
				t.Errorf("the Origin fallback accepted its own task: %s", res)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil {
				t.Errorf("the unauthorized Origin review closed the task: %+v", got)
			}
			settleService(t, s)
		})
	}
}

// A signal that waited through main's restore and removal is still a no-main Origin delivery. The Supervisor left by
// rehoming is current ledger state, not evidence that the background notice came from an explicit legacy route.
func TestNoMainPendingReviewAfterRehomeDoesNotGrantCloseout(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f := newQuietServiceOn(t, st)
	secretary(f)
	f.known["bg1"] = true
	f.setSessions("server", proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) {
		v.Main = ""
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
	})
	if _, err := runJSON(t, s, "bg1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
		"task": map[string]any{"title": "拉周报数据", "done_when": []string{"表格链接贴回来"}}}); err != nil {
		t.Fatal(err)
	}
	todo := onlyTodo(s)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "pending-through-rehome", At: time.Now()})
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	setSuperviseTimingsForTest(s, superviseTimings{gather: 1, every: 1, urgent: time.Hour})
	s.sendDigest(ctx)

	current := todoNow(s, todo.ID)
	if s.backgroundSupervises(ctx, "bg1", current) {
		t.Error("a pending no-main signal acquired acceptance authority through rehome")
	}
	res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "origin is not acceptance", "checks": completeReviewChecks(current)})
	if err == nil {
		t.Errorf("the rehomed pending Origin review accepted its own task: %s", res)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Errorf("the unauthorized rehomed Origin review closed the task: %+v", got)
	}
	settleService(t, s)
}

// A queue written before OriginOnly existed carries the same no-main fallback (#10126). Restart recovery must not use
// the restored main and rehomed ledger to reinterpret that old Origin delivery as a legacy closeout grant.
func TestPreUpgradeNoMainQueueDoesNotGainGrantAfterRehome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		handover bool
	}{
		{name: "supervisor_rehome"},
		{name: "origin_handover", handover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f := newQuietServiceOn(t, st)
			secretary(f)
			f.known["bg1"] = true
			f.setSessions("server", proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
			setSettings(t, s, func(v *Settings) {
				v.Main = ""
				v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
			})
			if _, err := runJSON(t, s, "bg1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
				"task": map[string]any{"title": "拉周报数据", "done_when": []string{"表格链接贴回来"}}}); err != nil {
				t.Fatal(err)
			}
			todo := onlyTodo(s)
			ctx := context.Background()
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
				t.Fatal(err)
			}
			g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "pre-origin-only-queue", At: time.Now()}
			if n := s.queueRelay("bg1", relayMsg{From: superviseTool, Tool: superviseTool, Text: "old no-main Origin review", Signals: encodeDigestSignals([]superviseSignal{g}), At: g.At}); n != 1 {
				t.Fatalf("queue the pre-upgrade Origin notice: %d", n)
			}
			setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			if tc.handover {
				setSettings(t, s, func(v *Settings) { v.Main = "bg1" })
				if moved := s.handOverMainTasks(ctx, "bg1", "conv1"); moved != 1 {
					t.Fatalf("the production main handover moved %d tasks", moved)
				}
				setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			} else if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
				t.Fatal(err)
			}
			simulateSupervisionRestart(t, s)

			current := todoNow(s, todo.ID)
			if tc.handover && (current.Origin != "conv1" || current.Supervision.Supervisor != "conv1") {
				t.Fatalf("the production handover did not move Origin and Supervisor: %+v", current)
			}
			if s.backgroundSupervises(ctx, "bg1", current) {
				t.Error("a pre-upgrade no-main queue became acceptance authority after rehome")
			}
			res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "origin is not acceptance", "checks": completeReviewChecks(current)})
			if err == nil {
				t.Errorf("the pre-upgrade Origin queue accepted its own task: %s", res)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil {
				t.Errorf("the unauthorized pre-upgrade Origin review closed the task: %+v", got)
			}
			settleService(t, s)
		})
	}
}

// An ask remembers its original rung in To. If that rung disappears with main, routeTo falls back to the task's
// dispatcher; the original target must not turn that no-main delivery into a legacy acceptance grant.
func TestNoMainAskFallbackDoesNotGrantCloseout(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	s := e.s
	ctx := context.Background()
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
	e.f.known["bg1"] = true
	convs := []proto.Session{
		{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
	}
	e.sessions("server", convs...)
	e.f.setSessions("server", convs...)
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	todo := e.todo
	todo.Origin = "bg1"
	putTodo(s, todo)
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.Supervisor = ""; return true }); err != nil {
		t.Fatal(err)
	}
	e.raise(askQuestion("ix_no_main_origin", "worker-1"))
	waitFor(t, "the ask routed to main", func() bool {
		ix, ok := e.shown("ix_no_main_origin")
		return ok && ix.Delegating
	})
	var routed superviseSignal
	waitFor(t, "the routed ask signal", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		for _, g := range s.supervise.pending {
			if g.Kind == sigAsk {
				routed = g
				return true
			}
		}
		return false
	})
	if routed.To != "conv1" {
		t.Fatalf("the ask did not retain its original main rung: %+v", routed)
	}

	setSettings(t, s, func(v *Settings) { v.Main = "" })
	convs = []proto.Session{{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle}}
	e.sessions("server", convs...)
	e.f.setSessions("server", convs...)
	setSuperviseTimingsForTest(s, superviseTimings{gather: 1, every: 1, urgent: time.Hour})
	s.sendDigest(ctx) // conv1 is gone: the actual delivery falls back to Origin=bg1 while To stays conv1
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}

	current := todoNow(s, todo.ID)
	if s.backgroundSupervises(ctx, "bg1", current) {
		t.Error("the no-main ask fallback became background acceptance authority")
	}
	res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "origin is not acceptance", "checks": completeReviewChecks(current)})
	if err == nil {
		t.Errorf("the ask Origin fallback accepted its own task: %s", res)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Errorf("the unauthorized ask-fallback review closed the task: %+v", got)
	}
	e.settle()
}

// A failed start can have no supervision ledger yet. Its no-main failure digest is still delivery-only: creating a
// ledger later must not turn that old notice into the dispatcher's acceptance authority.
func TestNoMainFailedStartNoticeDoesNotGrantCloseout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		queued  bool
		restart bool
	}{
		{name: "direct"},
		{name: "queued_restore_main", queued: true, restart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f := newQuietServiceOn(t, st)
			secretary(f)
			f.known["bg1"] = true
			status := proto.StatusIdle
			if tc.queued {
				status = proto.StatusWorking
			}
			f.setSessions("server", proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: status})
			setSettings(t, s, func(v *Settings) {
				v.Main = ""
				v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
			})
			setSuperviseTimingsForTest(s, superviseTimings{gather: 1, every: 1, urgent: time.Hour})
			todo := Todo{ID: "todo_no_main_failed", Title: "A start that failed", Origin: "bg1"}
			putTodo(s, todo)
			ctx := context.Background()
			s.taskStartFailed(ctx, todo, "box", nil, "runtime unavailable")
			s.sendDigest(ctx)
			if tc.queued {
				if got := s.queuedFor("bg1"); got != 1 {
					t.Fatalf("the failed-start notice did not queue: %d", got)
				}
				s.relays.mu.Lock()
				raw := s.relays.st.Queue["bg1"][0].Signals
				s.relays.mu.Unlock()
				queued, structured := decodeDigestSignals(raw)
				if !structured || len(queued) != 1 || !queued[0].OriginOnly {
					t.Errorf("the no-main failed-start route was not persisted: %+v", queued)
				}
			}
			setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.Supervisor, v.State = "conv1", reviewPending
				v.DoneWhen = []string{"the start is recovered"}
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if tc.restart {
				simulateSupervisionRestart(t, s)
			}

			current := todoNow(s, todo.ID)
			if s.backgroundSupervises(ctx, "bg1", current) {
				t.Error("the no-main failed-start notice became background acceptance authority")
			}
			res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "origin is not acceptance", "checks": completeReviewChecks(current)})
			if err == nil {
				t.Errorf("the failed-start Origin fallback accepted its own task: %s", res)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil {
				t.Errorf("the unauthorized failed-start review closed the task: %+v", got)
			}
			settleService(t, s)
		})
	}
}

// visibleBackgroundSend makes the legacy notice visible before its send returns, exposing the closeout grant's
// publication boundary without sleeping for a scheduler accident.
type visibleBackgroundSend struct {
	Hub
	started, release chan struct{}
	once             sync.Once
}

func (h *visibleBackgroundSend) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	res, err := h.Hub.DeviceCall(ctx, p)
	if err != nil || p.Method != DeviceAssistantSend {
		return res, err
	}
	var v struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(p.Params, &v); err != nil {
		return nil, err
	}
	if v.SessionID == "bg1" {
		h.once.Do(func() { close(h.started) })
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return res, nil
}

func TestLegacyBackgroundReviewWaitsForVisibleNoticeGrant(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	h := &visibleBackgroundSend{Hub: f, started: make(chan struct{}), release: make(chan struct{})}
	setHub(s, h)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(h.release) }) }
	defer unblock()
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "legacy-notice", To: "bg1", Minutes: 50, At: time.Now()})
	select {
	case <-h.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the legacy notice did not become visible")
	}
	args := map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "legacy closeout", "checks": completeReviewChecks(todoNow(s, todo.ID))}
	result := make(chan error, 1)
	go func() {
		_, err := runJSON(t, s, "bg1", "task_review", args)
		result <- err
	}()
	var err error
	completed := false
	select {
	case err = <-result:
		completed = true
	case <-time.After(50 * time.Millisecond):
	}
	if completed {
		t.Fatalf("the visible notice's review completed before its send returned: %v", err)
	}
	unblock()
	select {
	case err = <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("the legacy review did not finish after the notice send returned")
	}
	if err != nil {
		t.Fatalf("the visible legacy notice could not be accepted: %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.Supervision.State != reviewAccepted {
		t.Fatalf("the legacy task did not close: %+v", got)
	}
	settleService(t, s)
}

func TestRehomeKeepsLegacyBackgroundCloseout(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	// The old ledger itself names background. Rehoming unrelated side tasks must not take this old closeout away.
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		v.Supervisor = "bg1"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.backfillPlanSupervisors(context.Background())
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "bg1" {
		t.Fatalf("rehome took the legacy background task to %q", got)
	}
	res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "legacy closeout", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("the rehomed legacy background task cannot close: %s %v", res, err)
	}
}

func TestLegacyBackgroundGrantReadsFormerNULKey(t *testing.T) {
	grants := map[string]bool{"bg1\x00todo_legacy": true}
	if !legacyBackgroundGrant(grants, "bg1", "todo_legacy") {
		t.Fatal("the former in-memory NUL key no longer authorizes its legacy closeout")
	}
	if legacyBackgroundGrant(grants, "bg1", "todo_other") || legacyBackgroundGrant(grants, "other", "todo_legacy") {
		t.Fatal("legacy key compatibility leaked a grant to another task or conversation")
	}
}

func TestLegacyBackgroundNoticeSurvivesRestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newBackgroundServiceOn(t, st)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	tellLegacyBackground(t, s, f, todo.ID, "bg1")
	s.relays.mu.Lock()
	persisted := legacyBackgroundGrant(s.relays.st.LegacyBackground, "bg1", todo.ID)
	s.relays.mu.Unlock()
	if !persisted {
		t.Fatal("the delivered legacy notice was not persisted")
	}
	var stored relayState
	if ok, err := s.store().Setting(context.Background(), relayKey, &stored); err != nil || !ok || !legacyBackgroundGrant(stored.LegacyBackground, "bg1", todo.ID) {
		t.Fatalf("the legacy grant was not stored: ok=%v err=%v grants=%+v", ok, err, stored.LegacyBackground)
	}

	s.supervise.mu.Lock()
	s.supervise.legacyBackground = map[string]bool{}
	s.supervise.mu.Unlock()
	s.relays.mu.Lock()
	s.relays.loaded = false
	s.relays.mu.Unlock()
	res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "after restart", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("a persisted legacy notice did not authorize closeout after restart: %s %v", res, err)
	}
}

func TestLegacyBackgroundQueuedNoticeSurvivesRestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newBackgroundServiceOn(t, st)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	// A pre-upgrade queue entry has no structured signals and no separately persisted grant. Only its entry subject is
	// an assignment; an unrelated task linked from the evidence must not acquire closeout permission after a restart.
	legacyText := "1. 任务「整理材料」（" + todo.ID + "）：待逐项复核，不代表完成；证据」（todo_unrelated）：说明"
	if n := s.queueRelay("bg1", relayMsg{Tool: superviseTool, Text: legacyText, At: time.Now()}); n != 1 {
		t.Fatalf("queue the legacy notice: %d", n)
	}
	structured := []superviseSignal{{Todo: "todo_structured", To: "bg1"}}
	if n := s.queueRelay("bg1", relayMsg{Tool: superviseTool, Text: "structured notice", Signals: encodeDigestSignals(structured), At: time.Now()}); n != 2 {
		t.Fatalf("queue the structured notice: %d", n)
	}

	s.supervise.mu.Lock()
	s.supervise.legacyBackground = map[string]bool{}
	s.supervise.mu.Unlock()
	s.relays.mu.Lock()
	s.relays.loaded = false
	s.relays.mu.Unlock()
	res, err := runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "queued before upgrade", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("a queued pre-upgrade notice did not authorize closeout: %s %v", res, err)
	}
	for _, id := range []string{todo.ID, "todo_structured"} {
		if !s.legacyBackgroundNotice(context.Background(), "bg1", id) {
			t.Errorf("the actual notice subject %s lost its grant after restart", id)
		}
	}
	if s.legacyBackgroundNotice(context.Background(), "bg1", "todo_unrelated") {
		t.Fatal("an unrelated todo ID in the evidence became a closeout grant")
	}
}

func TestBackgroundCannotReviewLeaderWorkerWithoutLegacyNotice(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	w := planWorker(t, s, f, nil)
	f.known["bg1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	review := func() (string, error) {
		return runJSON(t, s, "bg1", "task_review", map[string]any{"todo_id": w.ID, "verdict": "accepted", "note": "legacy leader closeout", "checks": completeReviewChecks(todoNow(s, w.ID))})
	}
	if _, err := review(); err == nil {
		t.Fatal("the background leader fallback bypassed the legacy notice grant")
	}
	if got := todoNow(s, w.ID); got.DoneAt != nil {
		t.Fatalf("the unauthorized leader review closed the worker: %+v", got)
	}
	s.rememberLegacyBackground("bg1", []superviseSignal{{Todo: w.ID}})
	if res, err := review(); err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("the notified leader worker could not be closed out: %s %v", res, err)
	}
}

func TestExistingBackgroundStaysBuiltIn(t *testing.T) {
	s, _, _ := newBackgroundService(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	cfg := s.settings(ctx)
	if slices.Contains(cfg.Leaders, "bg1") || s.isLeader(ctx, "bg1") || s.supervises(ctx, "bg1") {
		t.Fatal("the background supervisor is a leader")
	}
	if !slices.Contains(keptSessions(cfg, nil, nil, nil, nil, nil), SessionRef{MachineID: "server", SessionID: "bg1"}) {
		t.Fatal("the background supervisor is not kept listed")
	}
	if _, err := s.setArchived(ctx, "server", "bg1", true, titleByUser); !errors.Is(err, errArchiveBackground) {
		t.Fatalf("archiving the background supervisor: %v", err)
	}
	raw, _ := json.Marshal([]string{"bg1"})
	if _, err := s.setLeaders(ctx, raw); err == nil || !strings.Contains(err.Error(), "background supervisor") {
		t.Fatalf("the background supervisor as a leader: %v", err)
	}
	if _, err := s.makeDirection(ctx, "bg1", "", "", ""); err == nil || !strings.Contains(err.Error(), "background supervisor") {
		t.Fatalf("the background supervisor as a direction: %v", err)
	}
}

// Main is not told that a background supervisor follows its tasks, because none is created for a new signal (#5732).
func TestNoBackgroundSupervisorIsAnnounced(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	const said = "由后台督办对话"
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "main's supervision", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if got := s.settings(ctx).BackgroundSupervisor; got != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("background supervisor %q was created", got)
	}
	first := s.Context(ctx, ContextParams{SessionID: "conv1"})
	if strings.Contains(first.Text, said) || len(first.Receipts) != 0 {
		t.Fatalf("a background supervisor was announced:\n%s", first.Text)
	}
}

// The compatibility setting must describe the route ownSupervisor really selects: main for new work, with only
// previously assigned notices left to an already configured background conversation (#5732).
func TestMainSupervisesOwnTasksDescriptionMatchesCompatibilityRoute(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	cfg := s.settings(context.Background())
	off := false
	cfg.MainSupervisesOwnTasks = &off
	cfg.BackgroundSupervisor = "legacy-background"
	if got := s.ownSupervisor(cfg); got != cfg.Main {
		t.Fatalf("ownSupervisor() = %q, want main %q", got, cfg.Main)
	}
	about := ""
	for _, setting := range s.ownSettings() {
		if setting.Key == "assistant.main_supervises_own_tasks" {
			about = setting.About
		}
	}
	for _, stale := range []string{"one built-in Persona conversation made on first use", "follows up every supervised task"} {
		if strings.Contains(about, stale) {
			t.Fatalf("setting still promises a route that does not exist: %q", about)
		}
	}
	for _, want := range []string{"main conversation supervises every new task", "previously assigned supervision notices", "no background conversation is created"} {
		if !strings.Contains(about, want) {
			t.Fatalf("setting description lacks %q: %q", want, about)
		}
	}

	// The compatibility description must not erase the old closeout path: a real previously assigned notice still drains.
	f.known["legacy-background"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "legacy-background", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) {
		v.MainSupervisesOwnTasks = &off
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "legacy-background", "server"
	})
	tellLegacyBackground(t, s, f, todo.ID, "legacy-background")
	res, err := runJSON(t, s, "legacy-background", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "finish the old notice", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if err != nil || !strings.Contains(res, "accepted and closed") {
		t.Fatalf("legacy background drain: %s %v", res, err)
	}
	if len(conversationStarts(f)) != 0 {
		t.Fatal("draining an old notice created a new background conversation")
	}
}

// main_supervises_own_tasks remains a readable setting, but it no longer creates a second route: both values leave
// new signals with main and start no background conversation (#5732).
func TestMainSupervisesOwnTasksSettingLeavesRouteWithMain(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	if v := s.view(ctx, false); v.MainSupervisesOwnTasks {
		t.Fatal("the setting's compatibility default changed")
	}
	w, v := permissionSettingsRequest(t, s, "PUT", "/api/v1/assistant", "", "full", `{"main_supervises_own_tasks":true,"background_supervisor":"conv2"}`)
	if w.Code != 200 || !v.MainSupervisesOwnTasks || v.BackgroundSupervisor != "" {
		t.Fatalf("PUT /api/v1/assistant: %d %s", w.Code, w.Body.String())
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "the main conversation's message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if n := f.noticesTo("conv1")[0]; strings.Contains(n, "后台督办") || !strings.Contains(n, "待逐项复核，不代表完成") {
		t.Fatalf("the main conversation's message:\n%s", n)
	}
	if got := s.settings(ctx).BackgroundSupervisor; got != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("a background supervisor %q was made with the setting on", got)
	}

	off := false
	setSettings(t, s, func(v *Settings) { v.MainSupervisesOwnTasks = &off })
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补测试"}); err != nil {
		t.Fatal(err)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "补了测试", 0)
	waitFor(t, "main's second message", func() bool { return len(f.noticesTo("conv1")) == 2 })
	if got := s.settings(ctx).BackgroundSupervisor; got != "" || len(conversationStarts(f)) != 0 {
		t.Fatalf("a background supervisor %q was made with the setting off", got)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.Supervisor != "conv1" {
		t.Fatalf("the task changed with the setting: %+v %+v", cur, cur.Supervision)
	}
}

// A background supervisor that cannot start drops nothing: its tasks' signals go to the main conversation, and the
// start is not tried again at once.
func TestBackgroundSupervisorThatCannotStartFallsBackToMain(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	f.mu.Lock()
	f.onAssistantSend = func(f *fakeHub, a map[string]any) {
		f.failAssistant = nil
		if id, _ := a["session_id"].(string); id == "" {
			f.failAssistant = errors.New("claude is not signed in")
		}
	}
	f.mu.Unlock()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "the main conversation's message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	if n := f.noticesTo("conv1")[0]; !strings.Contains(n, "待逐项复核，不代表完成") || strings.Contains(n, "你是后台督办对话") {
		t.Fatalf("the main conversation's message:\n%s", n)
	}
	if got := s.settings(ctx).BackgroundSupervisor; got != "" {
		t.Fatalf("a background supervisor %q is kept though it did not start", got)
	}
	if id, _ := s.ensureBackground(ctx); id != "" {
		t.Fatalf("the start was tried again at once: %q", id)
	}
}

// A background supervisor its machine no longer lists is not replaced. Its queued signals are told again to main.
func TestBackgroundSupervisorGoneMovesItsQueueToMain(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = "gone1", "server", &old
	})
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 50, At: time.Now()}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.setQueuedLocked("gone1", []superviseSignal{g})
	b.sent[g.id()] = g.Key
	b.mu.Unlock()
	if s.queueRelay("gone1", relayMsg{From: superviseTool, Tool: superviseTool, Text: "📋 督办", At: time.Now().UTC()}) == 0 {
		t.Fatal("could not queue for the old background supervisor")
	}

	s.backgroundStranded(ctx)
	if s.queuedFor("gone1") != 0 {
		t.Fatal("the old background supervisor's queue was kept")
	}
	waitFor(t, "the signal told again to main", func() bool {
		for _, n := range f.noticesTo("conv1") {
			if strings.Contains(n, "50 分钟没有进展") {
				return true
			}
		}
		return false
	})
	if len(conversationStarts(f)) != 0 {
		t.Fatal("a replacement background supervisor was started")
	}
	if got := f.noticesTo("gone1"); len(got) != 0 {
		t.Fatalf("something was sent to the conversation that is gone: %q", got)
	}
}

// digestTo: every new task signal goes to main. Explicit leaders keep their specialized route, and legacy cards naming
// a background supervisor move to main when it was switched off or moved (#5732).
func TestDigestToWithBackgroundSupervisor(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) {
		v.Leaders = []string{"lead1"}
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
	})
	led, err := s.createTodo(ctx, "主管的任务", "", "lead1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, led.ID, "lead1", []string{"做完"}, 30)
	// A side conversation supervises what it hands out itself (#5002): the background supervisor takes the tasks whose
	// ledger names the main conversation, not these.
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	sided, err := s.createTodo(ctx, "别的对话的任务", "", "side1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, sided.ID, "side1", []string{"做完"}, 30)
	waits, err := s.createTodo(ctx, "别的对话等用户的任务", "", "side1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, waits.ID, "side1", []string{"做完"}, 30)
	if _, _, err := s.updateSupervision(ctx, waits.ID, func(v *Supervision) bool { v.State = reviewNeedsUser; return true }); err != nil {
		t.Fatal(err)
	}
	for conv, want := range map[string]string{"side1": "side1", "bg1": "conv1", "gone": "conv1"} {
		// Live side conversations own what they plan (#13657); the background supervisor and gone conversations fall back to main.
		if got, ok := s.defaultPlanSupervisor(ctx, conv, ""); !ok || got != want {
			t.Errorf("a task %s plans is supervised by %q %v, want %q", conv, got, ok, want)
		}
	}
	cfg := s.settings(ctx)
	for _, c := range []struct {
		name string
		g    superviseSignal
		want string
	}{
		{"a review", superviseSignal{Todo: todo.ID, Kind: sigReview}, "conv1"},
		{"a report", superviseSignal{Todo: todo.ID, Kind: sigReport}, "conv1"},
		{"a stall", superviseSignal{Todo: todo.ID, Kind: sigStalled}, "conv1"},
		{"a stop short", superviseSignal{Todo: todo.ID, Kind: sigIdle}, "conv1"},
		{"a stop nobody judged", superviseSignal{Todo: todo.ID, Kind: sigUnjudged}, "conv1"},
		{"a waiting agent", superviseSignal{Todo: todo.ID, Kind: sigNeedsYou}, "conv1"},
		{"an interrupted turn", superviseSignal{Todo: todo.ID, Kind: sigInterrupted}, "conv1"},
		{"a task nobody works on", superviseSignal{Todo: todo.ID, Kind: sigOrphaned}, "conv1"},
		{"a task past its clock", superviseSignal{Todo: todo.ID, Kind: sigOverdue}, "conv1"},
		{"a parking that is due", superviseSignal{Todo: todo.ID, Kind: sigParkedDue}, "conv1"},
		{"an inbox message", superviseSignal{Todo: todo.ID, Kind: sigInbox}, "conv1"},
		{"a decision to recheck", superviseSignal{Todo: todo.ID, Kind: sigRecheck}, "conv1"},
		{"a decision nobody can answer", superviseSignal{Todo: todo.ID, Kind: sigUndecidable}, "conv1"},
		{"a decision the owner cannot answer", superviseSignal{Todo: todo.ID, Kind: sigOwnerCannot}, "conv1"},
		{"a failed start", superviseSignal{Todo: todo.ID, Kind: sigFailed}, "conv1"},
		{"a retried start", superviseSignal{Todo: todo.ID, Kind: sigStarted}, "conv1"},
		{"an escalation", superviseSignal{Todo: todo.ID, Kind: sigEscalated}, "conv1"},
		{"a leader stuck", superviseSignal{Todo: led.ID, Kind: sigLeaderStuck}, "conv1"},
		{"a leader's review", superviseSignal{Todo: led.ID, Kind: sigReview}, "lead1"},
		{"a side conversation's review", superviseSignal{Todo: sided.ID, Kind: sigReview}, "conv1"},
		{"a side conversation's task waiting for the user", superviseSignal{Todo: waits.ID, Kind: sigIdle}, "conv1"},
		{"a card's rung", superviseSignal{Todo: led.ID, Kind: sigAsk, To: "lead1"}, "lead1"},
		{"a card raised to the main conversation", superviseSignal{Todo: led.ID, Kind: sigAsk, To: "conv1"}, "conv1"},
		{"a task leader's decision for the main conversation", superviseSignal{Todo: todo.ID, Kind: sigDecide, To: "conv1"}, "conv1"},
		{"a task leader's decision for a leader", superviseSignal{Todo: led.ID, Kind: sigDecide, To: "lead1"}, "lead1"},
		{"a task that is gone", superviseSignal{Todo: "none", Kind: sigReview}, "conv1"},
	} {
		if got := s.digestTo(ctx, cfg, c.g); got != c.want {
			t.Errorf("%s goes to %q, want %q", c.name, got, c.want)
		}
	}
	// Off, or on another machine than the assistant's: the main conversation, also for what named the background supervisor.
	on := true
	off := cfg
	off.MainSupervisesOwnTasks = &on
	moved := cfg
	moved.BackgroundSupervisorMachineID = "laptop"
	for name, c := range map[string]Settings{"off": off, "moved": moved} {
		if got := s.digestTo(ctx, c, superviseSignal{Todo: todo.ID, Kind: sigReview}); got != "conv1" {
			t.Errorf("%s: a review goes to %q", name, got)
		}
		if got := s.digestTo(ctx, c, superviseSignal{Todo: todo.ID, Kind: sigAsk, To: "bg1"}); got != "conv1" {
			t.Errorf("%s: a card held by the background supervisor goes to %q", name, got)
		}
	}
}

// Cards of a main-supervised task use main's ask ladder even when a legacy background conversation is registered.
// Main may escalate one; an unanswered card stays with main after the hold (#5732), never pushed to the user.
func TestAsksOfMainSupervisedTasksUseMainLadder(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	ctx := context.Background()
	e.s.noBackground = false
	e.f.known["bg1"] = true
	convs := []proto.Session{{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle}}
	e.sessions("server", convs...)
	e.f.setSessions("server", convs...)
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	if _, _, err := e.s.updateSupervision(ctx, e.todo.ID, func(v *Supervision) bool { v.Supervisor = "conv1"; return true }); err != nil {
		t.Fatal(err)
	}
	if ladder, _, _, ok := e.s.askLadder(ctx, proto.Interaction{MachineID: "mac", SessionID: "worker-1", Kind: proto.IxQuestion}); !ok || !slices.Equal(ladder, []string{"conv1"}) {
		t.Fatalf("ladder = %v %v", ladder, ok)
	}
	if _, _, _, ok := e.s.askLadder(ctx, proto.Interaction{MachineID: "server", SessionID: "bg1", Kind: proto.IxQuestion}); ok {
		t.Fatal("the background supervisor's own cards are routed")
	}

	e.raise(askPermission("ix1", "worker-1"))
	n := e.noticeWith("conv1", "interaction_id=ix1")
	if !strings.Contains(n, "先交给你处理") {
		t.Fatalf("main's card message:\n%s", n)
	}
	e.settle()
	if got := e.pushesOf("ix1"); got != 0 {
		t.Fatalf("the user was pushed %d times", got)
	}
	if _, ok := e.s.routedTo("ix1", "conv1", true); !ok {
		t.Fatal("main may not answer its card")
	}
	if _, ok := e.s.routedTo("ix1", "lead1", false); ok {
		t.Fatal("a leader may answer a card of a task that is not its own")
	}
	res, err := runJSON(t, e.s, "conv1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "要推到远端分支，需要你同意", "recommend": "允许"})
	if err != nil || !strings.Contains(res, "1 card(s)") {
		t.Fatalf("escalate: %s %v", res, err)
	}
	waitFor(t, "the push", func() bool { return e.pushesOf("ix1") == 1 })

	// A card nobody handles stays with main after the hold, reminded, never pushed to the user.
	e.raise(askPermission("ix2", "worker-2"))
	e.noticeWith("conv1", "interaction_id=ix2")
	e.s.askTick(time.Now().Add(2 * askHoldFor))
	e.noticeWith("conv1", "再交给你处理")
	e.settle()
	if got := e.pushesOf("ix2"); got != 0 {
		t.Fatalf("the unhandled card was pushed %d times", got)
	}

	on := true
	setSettings(t, e.s, func(v *Settings) { v.MainSupervisesOwnTasks = &on })
	if ladder, _, _, ok := e.s.askLadder(ctx, proto.Interaction{MachineID: "mac", SessionID: "worker-1", Kind: proto.IxQuestion}); !ok || !slices.Equal(ladder, []string{"conv1"}) {
		t.Fatalf("with the setting on the ladder = %v %v", ladder, ok)
	}
}

// Without leader conversations askLadder still never routes to the background supervisor: open supervised-task
// non-six-class cards route to main (ownSupervisor) via active sigAsk wake-up rather than bg1 or keepRelayLater,
// while the background supervisor's own cards stay unrouted (#5732, #16291).
func TestBackgroundSupervisorAloneDoesNotRouteAsks(t *testing.T) {
	e := newRouteEnv(t, nil, false)
	ctx := context.Background()
	e.s.noBackground = false
	e.f.known["bg1"] = true
	convs := []proto.Session{
		{SessionID: "conv1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "lead1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
		{SessionID: "bg1", MachineID: "server", Cwd: assistantHome, Assistant: true, Status: proto.StatusIdle},
	}
	e.sessions("server", convs...)
	e.f.setSessions("server", convs...)
	setSettings(t, e.s, func(v *Settings) { v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server" })
	if ladder, _, _, ok := e.s.askLadder(ctx, proto.Interaction{MachineID: "mac", SessionID: "worker-1", Kind: proto.IxQuestion}); !ok || !slices.Equal(ladder, []string{"conv1"}) {
		t.Fatalf("ladder = %v %v, want [conv1]", ladder, ok)
	}
	if _, _, _, ok := e.s.askLadder(ctx, proto.Interaction{MachineID: "server", SessionID: "bg1", Kind: proto.IxQuestion}); ok {
		t.Fatal("the background supervisor's own cards are routed")
	}
	e.raise(askPermission("ix1", "worker-1"))
	n := e.noticeWith("conv1", "interaction_id=ix1")
	if !strings.Contains(n, "signal=ask") {
		t.Fatalf("main's notice lacks signal=ask:\n%s", n)
	}
	e.settle()
	if !e.lastShown("ix1").Delegating || e.pushesOf("ix1") != 0 {
		t.Fatal("non-owner permission pushed to the owner instead of routing")
	}
	if len(e.f.noticesTo("bg1")) != 0 {
		t.Fatalf("background supervisor was handed asks: %q", e.f.noticesTo("bg1"))
	}
	for _, to := range []string{"conv1", "bg1"} {
		for _, m := range laterFor(e.s, to) {
			if m.From == cardAgentPermission && strings.Contains(m.Text, "interaction_id=ix1") {
				t.Fatalf("supervised permission fell into %s keepRelayLater queue", to)
			}
		}
	}
}

// The background supervisor's model purpose follows the main chat's until it is given its own.
func TestBackgroundSupervisorModelPurpose(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	p, err := s.modelPurposeDef(ctx, purposeSupervise)
	if err != nil {
		t.Fatal(err)
	}
	if p.Parent != "assistant" || p.CategoryID != "assistant" || p.launch() != "headless" || modelRecordID(p) != purposeSupervise {
		t.Fatalf("purpose = %+v, record %q", p, modelRecordID(p))
	}
	main, err := s.resolveModel(ctx, "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.resolveModel(ctx, purposeSupervise); err != nil || got != main {
		t.Fatalf("with nothing saved it runs %+v, the main chat %+v (%v)", got, main, err)
	}
	if got := purposeOf(t, s, purposeSupervise); len(got.Fields) == 0 || got.Effective != purposeOf(t, s, "assistant").Effective {
		t.Fatalf("the purpose as the apps read it: %+v", got)
	}
	if r := modelRequest(s, "PATCH", purposeSupervise, `{"revision":0,"model":"small-model"}`); r.Code != 200 {
		t.Fatalf("giving it its own model: %d %s", r.Code, r.Body.String())
	}
	if got, err := s.resolveModel(ctx, purposeSupervise); err != nil || got.Model != "small-model" || got.Provider != main.Provider {
		t.Fatalf("its own model: %+v %v", got, err)
	}
	if again, _ := s.resolveModel(ctx, "assistant"); again != main {
		t.Fatalf("the main chat's model moved with it: %+v", again)
	}
	// The catalog lists it once, after the other built-in uses but the two meeting purposes (#5806, #7980), as the one
	// read by its own id: an app that still asks for it apart finds it listed and draws one row.
	var catalog struct {
		Purposes []ModelPurpose `json:"purposes"`
	}
	if r := modelRequest(s, "GET", "", ""); r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &catalog) != nil {
		t.Fatalf("catalog: %d %s", r.Code, r.Body.String())
	}
	listed := slices.DeleteFunc(slices.Clone(catalog.Purposes), func(p ModelPurpose) bool { return p.ID != purposeSupervise })
	if len(listed) != 1 || catalog.Purposes[len(catalog.Purposes)-4].ID != purposeSupervise || catalog.Purposes[len(catalog.Purposes)-3].ID != purposeMeetingRoom {
		t.Fatalf("the catalog's rows for it: %+v", listed)
	}
	if own := purposeOf(t, s, purposeSupervise); !reflect.DeepEqual(listed[0], own) || own.Description == "" || own.Launch != "headless" {
		t.Fatalf("listed %+v, by its id %+v", listed[0], own)
	}
}

// A Persona-managed provider only the background supervisor runs on is not deleted underneath it: 409 with the
// "still used" error naming the use, in the request's language, and nothing changed (#5806).
func TestProviderOfBackgroundSupervisorIsNotDeleted(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "supervise-only"}}, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "unused"}})
	if uses, err := s.providerModelUses(ctx, "launcher.supervise-only"); err != nil || len(uses) != 0 {
		t.Fatalf("before it is chosen: %v %v", uses, err)
	}
	if r := modelRequest(s, "PATCH", purposeSupervise, `{"revision":0,"provider":"launcher.supervise-only","model":"small-model"}`); r.Code != 200 {
		t.Fatalf("giving it its own provider: %d %s", r.Code, r.Body.String())
	}
	if main, err := s.resolveModel(ctx, "assistant"); err != nil || main.Provider == "launcher.supervise-only" {
		t.Fatalf("the main chat runs on it too: %+v %v", main, err)
	}
	before, err := s.readStoredLaunchers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		name := purposeName(l, purposeSupervise)
		res, err := serveHTTP(ctx, s, assistantwire.HTTPRequest{
			Method: "DELETE", Path: "/api/v1/launchers/supervise-only", Header: map[string][]string{"Accept-Language": {string(l)}},
		}, s.lang)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(res.Body, &body)
		want := i18n.T(l, "assistant-models_launchers.err_in_use", i18n.Args{"uses": name})
		if res.Status != 409 || body.Error != want || name == "" || strings.HasPrefix(name, "assistant-models.") {
			t.Fatalf("%s delete: %d %s; want 409 %q", l, res.Status, res.Body, want)
		}
	}
	if after, err := s.readStoredLaunchers(ctx); err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("the refused deletion changed the providers: %+v %v", after, err)
	}
	// One nothing runs on still goes, and so does this one once the background supervisor follows the main chat again.
	if r := request(s, "DELETE", "/api/v1/launchers/unused", ""); r.Code != 200 {
		t.Fatalf("an unused provider: %d %s", r.Code, r.Body.String())
	}
	rev := purposeOf(t, s, purposeSupervise).Revision
	if r := modelRequest(s, "PATCH", purposeSupervise, fmt.Sprintf(`{"revision":%d,"provider":null,"model":null}`, rev)); r.Code != 200 {
		t.Fatalf("following the main chat again: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/launchers/supervise-only", ""); r.Code != 200 {
		t.Fatalf("once nothing runs on it: %d %s", r.Code, r.Body.String())
	}
}

// While it follows the main chat, the main chat's provider is the background supervisor's too: the refused deletion
// names both, and so does what an import, or hiding the alias, reads before it takes the alias out (#5806). With a
// provider of its own it is named alone.
func TestProviderUsesNameTheBackgroundSupervisor(t *testing.T) {
	s, _, _ := modelService(t)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "shared"}}, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "supervise-only"}})
	l := s.requestCtxLang(context.Background())
	main, own, room, mainOnly := purposeName(l, "assistant"), purposeName(l, purposeSupervise), purposeName(l, purposeMeetingRoom), purposeName(l, purposeMain)
	uses := func(name string) []string {
		t.Helper()
		r := request(s, "GET", "/api/v1/launchers/"+name+"/uses", "")
		var body struct {
			Uses []string `json:"uses"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &body); r.Code != 200 || err != nil {
			t.Fatalf("uses of %s: %d %s %v", name, r.Code, r.Body.String(), err)
		}
		return body.Uses
	}
	rev := purposeOf(t, s, "assistant").Revision
	if r := modelRequest(s, "PATCH", "assistant", fmt.Sprintf(`{"revision":%d,"provider":"launcher.shared","model":"big-model"}`, rev)); r.Code != 200 {
		t.Fatalf("the main chat's provider: %d %s", r.Code, r.Body.String())
	}
	if p := purposeOf(t, s, purposeSupervise); p.Override.Provider != "" || p.Effective.Provider != "launcher.shared" {
		t.Fatalf("it does not follow the main chat: %+v", p)
	}
	// The In-meeting conversation follows the main chat as well (#7980).
	if got := uses("shared"); !reflect.DeepEqual(got, []string{main, own, room, mainOnly}) || main == own {
		t.Fatalf("uses while it follows the main chat: %v; want %q, %q, %q and %q", got, main, own, room, mainOnly)
	}
	r := request(s, "DELETE", "/api/v1/launchers/shared", "")
	sep := i18n.T(l, "assistant-models_launchers.list_sep")
	want := i18n.T(l, "assistant-models_launchers.err_in_use", i18n.Args{"uses": main + sep + own + sep + room + sep + mainOnly})
	var refused struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &refused)
	if r.Code != 409 || refused.Error != want {
		t.Fatalf("deleting the provider both run on: %d %s; want 409 %q", r.Code, r.Body.String(), want)
	}
	if got := uses("supervise-only"); len(got) != 0 {
		t.Fatalf("uses before it is chosen: %v", got)
	}
	if r := modelRequest(s, "PATCH", purposeSupervise, `{"revision":0,"provider":"launcher.supervise-only","model":"small-model"}`); r.Code != 200 {
		t.Fatalf("giving it its own provider: %d %s", r.Code, r.Body.String())
	}
	if got := uses("supervise-only"); !reflect.DeepEqual(got, []string{own}) {
		t.Fatalf("uses of its own provider: %v; want %q", got, own)
	}
	if got := uses("shared"); !reflect.DeepEqual(got, []string{main, room, mainOnly}) {
		t.Fatalf("uses of the main chat's provider once it has its own: %v; want %q, %q and %q", got, main, room, mainOnly)
	}
}

// The legacy background supervisor's prompts cover only tasks assigned before #5732. New work is always supervised by
// the main conversation, regardless of the old setting.
func TestGoldenBackgroundSupervisor(t *testing.T) {
	golden(t, "context.background", prompts.RenderAssistantContext(prompts.AssistantContext{Background: true, Owner: "Example user", Snapshot: "（快照）"}))
	golden(t, "context.main_background_started", prompts.RenderAssistantContext(prompts.AssistantContext{Main: true, Owner: "Example user",
		BackgroundStarted: "a1b2c3d4-0000-4000-8000-000000000009", Snapshot: "（快照）"}))
	golden(t, "supervise.background", prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Asked: true, AskMinutes: 5,
		Entries: sortEntries([]prompts.SuperviseEntry{
			{ID: "t1", Title: "修登录崩溃", Kind: sigReview, Machine: "Mac", Session: "a1b2c3d4", DoneWhen: []string{"PR 合并到 main"}, Last: "做完了，PR 已合并"},
			{ID: "t2", Title: "补测试", Kind: sigAsk, Asks: []prompts.SuperviseAsk{{ID: "ix1", Kind: proto.IxQuestion, Machine: "Mac", Session: "b2c3d4e5", Text: "用哪个分支？"}}},
		})}))
	golden(t, "supervise.background_escalated", prompts.RenderSupervise(prompts.Supervise{Escalated: true, BackgroundEscalated: true, Entries: []prompts.SuperviseEntry{
		{ID: "t1", Title: "修登录崩溃", Kind: sigEscalated, Background: true, Detail: "要不要顺手改 iPhone 横屏", Recommend: "先只修 iPad"}}}))

	bare := claudeMDHub(Device{ID: "server", Name: "服务器", Kind: "local"})
	s := newTestService(t, bare)
	s.kb.dir = "<hub>/data"
	off := s.claudeMD(context.Background(), "server", false)
	s.noBackground = false
	on := s.claudeMD(context.Background(), "server", false)
	golden(t, "assistant_claude_md.background", on)
	for _, common := range []string{off, on} {
		if !strings.Contains(common, "主管派的任务归该主管") {
			t.Fatal("shared CLAUDE.md lost the direction ownership rule")
		}
		if strings.Contains(common, "cost_of_waiting") || strings.Contains(common, "visibility: \"silent\"") {
			t.Fatal("shared CLAUDE.md still contains detailed supervision handling")
		}
	}
	if on != off {
		t.Fatal("the legacy background supervisor setting changed the main supervision instructions")
	}
	yes := true
	setSettings(t, s, func(v *Settings) { v.MainSupervisesOwnTasks = &yes })
	if got := s.claudeMD(context.Background(), "server", false); got != off {
		t.Fatal("with the setting on the CLAUDE.md is not what it was before the background supervisor")
	}
}

// An empty session list before the assistant's machine has connected says nothing about an existing background
// conversation. A connected list without it means gone; an unreachable machine says nothing either.
func TestBackgroundSupervisorPresenceIsReadFromItsMachine(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = "bg1", "server", &old
	})
	f.setSessions("server")
	if !s.backgroundThere(ctx, s.settings(ctx), "bg1") {
		t.Fatal("gone by a list its machine has not sent yet")
	}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if s.backgroundThere(ctx, s.settings(ctx), "bg1") {
		t.Fatal("still there though its machine lists without it")
	}
	f.mu.Lock()
	f.devices = []Device{{ID: "server", Name: "server"}, {ID: "box", Name: "box", Online: true, Reachable: true}}
	f.mu.Unlock()
	if !s.backgroundThere(ctx, s.settings(ctx), "bg1") {
		t.Fatal("gone by the list of a machine that is not connected")
	}
	if len(conversationStarts(f)) != 0 {
		t.Fatal("a replacement background supervisor was started")
	}
}

// A supervision message queued for a background supervisor before the manager restarted has no signals in memory to
// tell again, and its deduplication entries would keep them from going out: when that background supervisor is gone,
// the message itself moves to the main conversation instead of being dropped.
func TestBackgroundSupervisorGoneAfterRestartKeepsItsQueuedMessage(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = "gone1", "server", &old
	})
	if s.queueRelay("gone1", relayMsg{From: superviseTool, Tool: superviseTool, Text: "📋 督办：重启前排队的", At: time.Now().UTC()}) == 0 {
		t.Fatal("could not queue for the old background supervisor")
	}
	s.backgroundStranded(ctx)
	waitFor(t, "the message in the main conversation", func() bool {
		return slices.ContainsFunc(f.noticesTo("conv1"), func(n string) bool { return strings.Contains(n, "重启前排队的") })
	})
	if s.queuedFor("gone1") != 0 {
		t.Fatal("the old background supervisor's queue was kept")
	}
	if got := f.noticesTo("gone1"); len(got) != 0 {
		t.Fatalf("something was sent to the conversation that is gone: %q", got)
	}
}

// With no main destination, retirement cannot move anything. Keep the durable queue untouched so a later main
// configuration can reconcile it instead of silently deleting every kind of relay (#5732).
func TestBackgroundSupervisorGoneWithoutMainKeepsQueueUntilDestinationReturns(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	old := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = "gone1", "server", &old
		v.Main = ""
	})
	if s.queueRelay("gone1", relayMsg{From: "conv2", Tool: "conversation_send", Text: "keep until main returns", At: time.Now().UTC()}) == 0 {
		t.Fatal("could not queue for the old background supervisor")
	}
	s.backgroundStranded(ctx)
	if got := s.queuedFor("gone1"); got != 1 {
		t.Fatalf("a missing main destination deleted the durable queue: %d", got)
	}
	if got := s.queuedFor("conv1"); got != 0 {
		t.Fatalf("an empty destination invented a main queue: %d", got)
	}
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	s.backgroundStranded(ctx)
	waitFor(t, "the retained message after main returns", func() bool {
		return slices.ContainsFunc(f.noticesTo("conv1"), func(n string) bool { return strings.Contains(n, "keep until main returns") })
	})
	if s.queuedFor("gone1") != 0 {
		t.Fatal("the retained queue did not move once main returned")
	}
}

// The owner comes first in main's supervision queue: after they write there, later messages wait by rank until they
// are quiet; a decision goes before an older ordinary message.
func TestOwnerPresenceAndMainSupervisionQueue(t *testing.T) {
	presenceTimes(t, 400*time.Millisecond, time.Hour)
	ownerTurnTimes(t, 400*time.Millisecond, time.Hour)
	s, f, _ := newBackgroundService(t)
	stopHolds(t, s)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "main's supervision message", func() bool { return len(f.noticesTo("conv1")) == 1 })

	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	other := relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "hello", At: time.Now().UTC()}
	urgent := relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool, Text: "Synthetic supervision.", Urgent: true, At: time.Now().UTC()}
	s.relayOrQueue(ctx, "conv1", other)
	s.relayOrQueue(ctx, "conv1", urgent)
	if n := s.queuedFor("conv1"); n != 2 || f.sentTo("conv1") != 1 {
		t.Fatalf("while the owner writes there: %d queued, %d sent", n, f.sentTo("conv1"))
	}
	// Quiet again: one goes in per turn, and the one that waits on a decision goes before the one that came first.
	waitFor(t, "the first held message once the owner is quiet", func() bool { return f.sentTo("conv1") == 2 && s.queuedFor("conv1") == 1 })
	b := &s.relays
	b.mu.Lock()
	left := slices.Clone(b.st.Queue["conv1"])
	b.mu.Unlock()
	if len(left) != 1 || left[0].Tool != "conversation_send" {
		t.Fatalf("the message that waits on a decision did not go first; still queued: %+v", left)
	}
}

// Main's supervision rounds remain supervision rounds in the availability metrics, including a round seen starting
// before its source was reported.
func TestMainSupervisionRoundsAreSupervisionRounds(t *testing.T) {
	s, f, _ := newBackgroundService(t)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	waitFor(t, "main's supervision message", func() bool { return len(f.noticesTo("conv1")) == 1 })
	ref := SessionRef{MachineID: "server", SessionID: "conv1"}
	s.TurnStarted(ctx, ref)
	s.TurnEnded(ctx, ref)
	var rounds []map[string]any
	waitFor(t, "main's supervision round", func() bool {
		rounds = nil
		for _, e := range testEvents(s) {
			if e.Kind == roundEvent && e.SessionID == "conv1" {
				rounds = append(rounds, e.Detail)
			}
		}
		return len(rounds) == 1
	})
	if rounds[0]["source"] != proto.SourceSupervision || rounds[0]["main"] != true {
		t.Fatalf("main's round: %+v", rounds[0])
	}

	s.avail.mu.Lock()
	s.avail.rounds[[2]string{"server", "bg-late"}] = availRound{source: roundOther, started: time.Now()}
	s.avail.mu.Unlock()
	s.availStartedBy("server", "bg-late", proto.SourceSupervision, time.Now())
	s.avail.mu.Lock()
	got := s.avail.rounds[[2]string{"server", "bg-late"}].source
	s.avail.mu.Unlock()
	if got != proto.SourceSupervision {
		t.Fatalf("the round already under way is %q", got)
	}
}
