package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// sideReviewFixture installs a pre-#5732 ledger: side1 handed out the task and still owns its pending review, with
// the old supervision message already delivered there. New task_plan work is supervised by conv1; this fixture keeps
// the legacy handover path testable without restoring that old routing. Both conversations are idle.
func sideReviewFixture(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietService(t)
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	f.known["side1"] = true
	listSide(f, proto.StatusIdle)
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app", "readonly": true,
		"why": "这个仓库最近在 box 上", "done_when": []string{"PR 合并到 main"}, "expect_minutes": 30,
	}}})
	if _, err := s.toolTaskPlan(context.Background(), "side1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	return s, f, deliverPendingReview(t, s, f, onlyTodo(s), "side1")
}

// deliverPendingReview starts the fixture with a review already delivered to its supervisor.
func deliverPendingReview(t *testing.T, s *Service, f *fakeHub, todo Todo, supervisor string) Todo {
	t.Helper()
	v, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		v.Supervisor, v.State = supervisor, reviewPending
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigReview, Key: reviewKey(&v), To: supervisor})
	waitFor(t, "the initial review notice", func() bool { return reviewsTold(f, supervisor, todo.ID) == 1 })
	if v := todoNow(s, todo.ID).Supervision; v == nil || v.Supervisor != supervisor || v.State != reviewPending {
		t.Fatalf("ledger = %+v, want %s waiting for review", v, supervisor)
	}
	return todoNow(s, todo.ID)
}

// listSide has the assistant's machine list the main conversation conv1, idle, and the side conversation side1 in
// status.
func listSide(f *fakeHub, status string) {
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: status})
}

// reviewsTold counts the supervision messages sent into conversation conv that name task id.
func reviewsTold(f *fakeHub, conv, id string) int {
	n := 0
	for _, text := range f.noticesTo(conv) {
		if strings.Contains(text, "📋 督办") && strings.Contains(text, id) {
			n++
		}
	}
	return n
}

// A review a side conversation leaves waiting is handed to the main conversation's follow-up (#8043). Within the
// bound, and while the side conversation is in a turn (it may be the review), the task stays its own. Past the bound
// with the conversation stopped, the ledger names the main conversation, the origin stays, and the main conversation is
// told once, with why; later passes tell nobody again. The main conversation's review closes the task and the side
// conversation gets its end notice, as for any task the main conversation supervises for another conversation.
func TestReviewLeftWaitingByASideConversationGoesToTheMainConversation(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	supervisor := func() string { return todoNow(s, todo.ID).Supervision.Supervisor }

	s.superviseWatch(ctx, time.Now().Add(10*time.Minute))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q after 10 minutes: the side conversation still has time to review", got)
	}
	listSide(f, proto.StatusWorking)
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the side conversation is in a turn", got)
	}

	listSide(f, proto.StatusIdle)
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	got := todoNow(s, todo.ID)
	if v := got.Supervision; v.Supervisor != "conv1" || v.State != reviewPending || got.Origin != "side1" || got.DoneAt != nil {
		t.Fatalf("not handed over as it was: origin %q, done %v, ledger %+v", got.Origin, got.DoneAt, v)
	}
	waitFor(t, "the review told to the main conversation", func() bool { return reviewsTold(f, "conv1", todo.ID) == 1 })
	var told string
	for _, text := range f.noticesTo("conv1") {
		if strings.Contains(text, todo.ID) {
			told = text
		}
	}
	if !strings.Contains(told, "待逐项复核，不代表完成") || !strings.Contains(told, "转给你验收") {
		t.Fatalf("the main conversation is not told that the review is handed to it, and why:\n%s", told)
	}

	// Once: the next passes find a task of the main conversation, and its review was told.
	s.superviseWatch(ctx, time.Now().Add(90*time.Minute))
	s.superviseWatch(ctx, time.Now().Add(2*time.Hour))
	s.sendDigest(ctx)
	if n, m := reviewsTold(f, "conv1", todo.ID), reviewsTold(f, "side1", todo.ID); n != 1 || m != 1 {
		t.Fatalf("told the main conversation %d time(s) and the side conversation %d: want once each", n, m)
	}

	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "PR 已合并",
		"checks": []map[string]any{{"criterion": "PR 合并到 main", "met": true, "evidence": "pull/7"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task closed", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
	waitFor(t, "the side conversation's end notice", func() bool { return len(endNotices(f, "side1")) == 1 })
}

// Even with the legacy background supervisor configured, a handed-over review follows #5732's current routing:
// the ledger and the supervision message both stay with the main conversation, and no background stream is started.
func TestReviewLeftWaitingByASideConversationStaysWithMainWhenBackgroundIsConfigured(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	s.noBackground = false
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID = "bg1", "server"
	})
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "bg1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})

	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "conv1" {
		t.Fatalf("the ledger names %q, want the main conversation", got)
	}
	waitFor(t, "the review told to the main conversation", func() bool { return reviewsTold(f, "conv1", todo.ID) == 1 })
	if n := reviewsTold(f, "bg1", todo.ID); n != 0 {
		t.Fatalf("the legacy background conversation got %d new supervision message(s)", n)
	}
	if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "PR 已合并",
		"checks": []map[string]any{{"criterion": "PR 合并到 main", "met": true, "evidence": "pull/7"}}}); err != nil {
		t.Fatalf("the main conversation cannot review the task handed to it: %v", err)
	}
	waitFor(t, "the task closed", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}

// A side conversation that is gone from its machine's list no longer reviews anything: its review is handed over too.
// While the machine cannot say (it is away, or has listed no assistant conversation yet, as after a manager's start)
// nothing moves.
func TestReviewOfASideConversationThatIsGoneIsHandedOver(t *testing.T) {
	s, f, todo := sideReviewFixture(t)
	ctx := context.Background()
	supervisor := func() string { return todoNow(s, todo.ID).Supervision.Supervisor }

	f.setSessions("server") // nothing listed yet
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the machine has listed no assistant conversation", got)
	}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.mu.Lock()
	f.down = map[string]bool{"server": true}
	f.mu.Unlock()
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "side1" {
		t.Fatalf("handed to %q while the assistant's machine is away", got)
	}
	f.mu.Lock()
	f.down = nil
	f.mu.Unlock()

	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := supervisor(); got != "conv1" {
		t.Fatalf("the review of a conversation that is gone stays with %q", got)
	}
	waitFor(t, "the review told to the main conversation", func() bool { return reviewsTold(f, "conv1", todo.ID) == 1 })
}

// A direction keeps reviews for Agents it explicitly adopted (#5732). Even after
// the cardless settle time, the reminder goes to that lead and the ledger stays its own.
func TestReviewLeftWaitingByALeaderStaysWithIt(t *testing.T) {
	c := boxAgent("manual01")
	s, f := leadTakeEnv(t, c)
	if _, err := runJSON(t, s, "lead0001", "agent_supervise", map[string]any{"machine": "box", "session_id": c.SessionID}); err != nil {
		t.Fatal(err)
	}
	todo := deliverPendingReview(t, s, f, onlyTodo(s), "lead0001")
	s.superviseWatch(t.Context(), time.Now().Add(time.Hour))
	s.sendDigest(t.Context())
	waitFor(t, "the cardless reminder to the lead", func() bool { return reviewsTold(f, "lead0001", todo.ID) == 2 })
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "lead0001" {
		t.Fatalf("the review moved to %q", got)
	}
	if n := reviewsTold(f, "conv1", todo.ID); n != 0 {
		t.Fatalf("main got %d supervision messages for the lead's review", n)
	}
}

func flushSuperviseDigest(ctx context.Context, s *Service) {
	setSuperviseTimingsForTest(s, superviseTimings{gather: 1, every: 1, urgent: 1})
	s.sendDigest(ctx)
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
}

// reviewSignalsTold counts sigReview supervision notices sent to conv for task id, ignoring non-review wakes
// such as cardless-wait reminders.
func reviewSignalsTold(f *fakeHub, conv, id string) int {
	n := 0
	for _, text := range f.noticesTo(conv) {
		if strings.Contains(text, "📋 督办") && strings.Contains(text, id) && strings.Contains(text, "待逐项复核，不代表完成") {
			n++
		}
	}
	return n
}

func handoverNoticeDelivered(f *fakeHub, conv, id string) bool {
	for _, text := range f.noticesTo(conv) {
		if strings.Contains(text, "📋 督办") && strings.Contains(text, id) &&
			strings.Contains(text, "待逐项复核，不代表完成") && strings.Contains(text, "转给你") {
			return true
		}
	}
	return false
}

// sideReviewProductionFixture sets up a side-supervised task matching the real production delivery shape:
// the worker agent has a non-empty last reply (so e.Last drives the deduplication hash) and the initial sigReview
// is delivered to side1 without an explicit To or Recipient field.
func sideReviewProductionFixture(t *testing.T) (*Service, *fakeHub, Todo, superviseSignal) {
	t.Helper()
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
	ctx := context.Background()
	f.known["side1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{
			SessionID:    "side1",
			MachineID:    "server",
			Assistant:    true,
			Status:       proto.StatusIdle,
			SourceKind:   proto.SourceRuntime,
			Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
		},
	)
	todo, err := s.createTodo(ctx, "Fix login crash", "Fix iPad landscape login crash", "side1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo.ID, "side1", []string{"PR merged to main"}, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "worker1", "Fix login crash", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{
		SessionID: "worker1",
		MachineID: "box",
		Cwd:       "/code/app",
		Agent:     "claude",
		Status:    proto.StatusDone,
		State:     proto.StateUnread,
		LastText:  "Fixed the login crash and added unit tests.",
		UpdatedAt: time.Now(),
	})
	v, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: reviewKey(&v), Detail: "All checks passed."}
	s.signal(initial)
	flushSuperviseDigest(ctx, s)
	if n := reviewsTold(f, "side1", todo.ID); n != 1 {
		t.Fatalf("side1 received %d initial review notice(s), want 1", n)
	}
	if n := reviewsTold(f, "conv1", todo.ID); n != 0 {
		t.Fatalf("conv1 received %d initial review notice(s), want 0", n)
	}
	return s, f, todoNow(s, todo.ID), initial
}

func TestSideHandoverReviewDeliversToMainAfterSideDedup(t *testing.T) {
	for _, mode := range []string{"archived", "gone", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			s, f, todo, initial := sideReviewProductionFixture(t)
			ctx := context.Background()

			initialEntry, ok := s.digestEntry(ctx, initial, s.webBase(ctx))
			if !ok || initialEntry.Last == "" {
				t.Fatalf("initial digestEntry = %+v, ok=%v; want non-empty Last", initialEntry, ok)
			}
			sideDedupKey, _ := digestFingerprint(initialEntry, initial)
			s.supervise.dedupMu.Lock()
			_, hadSideReceipt := s.supervise.dedup[sideDedupKey]
			s.supervise.dedupMu.Unlock()
			if !hadSideReceipt {
				t.Fatalf("initial side1 delivery did not record deduplication fingerprint %q", sideDedupKey)
			}

			switch mode {
			case "archived":
				if _, err := s.setArchived(ctx, "server", "side1", true, titleByUser); err != nil {
					t.Fatal(err)
				}
				// A watch pass before the digest flushes must not strip Recipient from the pending handover signal.
				s.superviseWatch(ctx, time.Now())
			case "gone":
				f.setSessions("server",
					proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				)
				s.superviseWatch(ctx, time.Now())
			case "timeout":
				s.superviseWatch(ctx, time.Now().Add(time.Hour))
			}

			cur := todoNow(s, todo.ID)
			if cur.Supervision == nil || cur.Supervision.Supervisor != "conv1" {
				t.Fatalf("after %s handover, supervisor = %+v, want conv1", mode, cur.Supervision)
			}

			s.supervise.mu.Lock()
			pendingReview, hasPending := s.supervise.pending[todo.ID+"|"+sigReview]
			s.supervise.mu.Unlock()
			if !hasPending || pendingReview.Recipient != "conv1" {
				t.Fatalf("after %s handover, pending sigReview = (%+v, ok=%v), want Recipient=conv1", mode, pendingReview, hasPending)
			}

			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 || !handoverNoticeDelivered(f, "conv1", todo.ID) {
				t.Fatalf("after %s handover, main conversation received %d sigReview notice(s) (handover=%v), want 1:\n%s",
					mode, n, handoverNoticeDelivered(f, "conv1", todo.ID), strings.Join(f.noticesTo("conv1"), "\n---\n"))
			}

			// The previous supervisor's deduplication receipt stays intact, and a stale signal addressed to side1 is rejected.
			s.supervise.dedupMu.Lock()
			_, sideReceiptKept := s.supervise.dedup[sideDedupKey]
			s.supervise.dedupMu.Unlock()
			if !sideReceiptKept {
				t.Fatalf("handover deleted previous supervisor receipt %q", sideDedupKey)
			}
			if _, valid := s.digestEntry(ctx, superviseSignal{Todo: todo.ID, Kind: sigReview, Recipient: "side1"}, s.webBase(ctx)); valid {
				t.Fatal("stale sigReview addressed to side1 was still admitted after handover")
			}

			// Duplicate delivery to the same recipient (conv1) is still deduplicated.
			s.superviseWatch(ctx, time.Now().Add(2*time.Hour))
			flushSuperviseDigest(ctx, s)
			dup := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "review@duplicate-same-recipient", Recipient: "conv1", Detail: "duplicate"}
			s.signal(dup)
			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
				t.Fatalf("duplicate delivery sent %d sigReview notice(s) to conv1, want 1", n)
			}
			dupEntry, ok := s.digestEntry(ctx, dup, s.webBase(ctx))
			if !ok {
				t.Fatal("digestEntry rejected current supervisor conv1")
			}
			if out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{dupEntry}, []superviseSignal{dup}, time.Now()); err != nil || len(out) != 0 {
				t.Fatalf("reserveSupervision did not deduplicate repeat sigReview for conv1: len=%d err=%v", len(out), err)
			}
		})
	}
}

func TestSideHandoverExternalPRSameKeyDeliversOnceToMain(t *testing.T) {
	for _, mode := range []string{"archived", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			st := &memStore{}
			s, f := newQuietServiceOn(t, st)
			setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
			ctx := context.Background()
			f.known["side1"] = true
			f.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				proto.Session{
					SessionID:    "side1",
					MachineID:    "server",
					Assistant:    true,
					Status:       proto.StatusIdle,
					SourceKind:   proto.SourceRuntime,
					Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
				},
			)
			todo, err := s.createTodo(ctx, "Wait on upstream PR", "Wait for PR #12", "side1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.superviseTodo(ctx, todo.ID, "side1", []string{"PR merged"}, 30, "plan"); err != nil {
				t.Fatal(err)
			}
			args := externalPRParkArgs(externalPRSource)
			args["todo_id"] = todo.ID
			if _, err := runJSON(t, s, "side1", "task_review", args); err != nil {
				t.Fatal(err)
			}
			s.deploy.forge = &externalPRForge{terminal: "merged"}
			now := time.Now()
			s.sweepPRStatuses(ctx, now)
			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "side1", todo.ID); n != 1 {
				t.Fatalf("side1 received %d external-PR review notice(s), want 1", n)
			}
			origKey := reviewKey(todoNow(s, todo.ID).Supervision)
			if !strings.HasPrefix(origKey, externalPRReviewPrefix) {
				t.Fatalf("reviewKey = %q, want prefix %q", origKey, externalPRReviewPrefix)
			}

			switch mode {
			case "archived":
				if _, err := s.setArchived(ctx, "server", "side1", true, titleByUser); err != nil {
					t.Fatal(err)
				}
			case "timeout":
				s.superviseWatch(ctx, now.Add(time.Hour))
			}

			if gotKey := reviewKey(todoNow(s, todo.ID).Supervision); gotKey != origKey {
				t.Fatalf("external-PR reviewKey changed across handover: got %q, want %q", gotKey, origKey)
			}
			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
				t.Fatalf("after %s external-PR handover, main received %d sigReview notice(s), want 1", mode, n)
			}

			// Repeat delivery to conv1 with the same key must stay deduplicated.
			s.superviseWatch(ctx, now.Add(2*time.Hour))
			flushSuperviseDigest(ctx, s)
			dup := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: origKey, Recipient: "conv1"}
			dupEntry, ok := s.digestEntry(ctx, dup, s.webBase(ctx))
			if !ok {
				t.Fatal("digestEntry rejected conv1 external-PR review")
			}
			if out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{dupEntry}, []superviseSignal{dup}, now.Add(2*time.Hour)); err != nil || len(out) != 0 {
				t.Fatalf("repeat external-PR sigReview for conv1 was not deduplicated: len=%d err=%v", len(out), err)
			}
			if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
				t.Fatalf("repeat watches sent %d external-PR sigReview notice(s) to conv1, want 1", n)
			}
		})
	}
}

func TestRehomeWorkerReviewDeliversAfterLeaderDedup(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
	leaderTodo := startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	workerAgent := worker.Agents[0]
	f.setSessions(workerAgent.MachineID,
		proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()},
		proto.Session{
			SessionID: workerAgent.SessionID,
			MachineID: workerAgent.MachineID,
			Cwd:       "/code/app",
			Agent:     "claude",
			Status:    proto.StatusDone,
			State:     proto.StateUnread,
			LastText:  "Completed worker task and verified tests.",
			UpdatedAt: time.Now(),
		},
	)
	v, _, err := s.updateSupervision(ctx, worker.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	s.signal(superviseSignal{Todo: worker.ID, Kind: sigReview, Key: reviewKey(&v), Detail: "Worker done."})
	flushSuperviseDigest(ctx, s)
	if len(deliveredTo(f, "s1")) == 0 {
		t.Fatal("task leader did not receive initial worker sigReview")
	}

	if ok, err := s.autoDoneTodo(ctx, leaderTodo.ID, "leader finished", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("close leader task: ok=%v err=%v", ok, err)
	}
	s.rehomeLeaderlessWorkers(ctx)
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", worker.ID); n != 1 {
		t.Fatalf("after rehoming leaderless worker, conv1 received %d sigReview notice(s), want 1", n)
	}

	dup := superviseSignal{Todo: worker.ID, Kind: sigReview, Key: "review@duplicate-rehome", Recipient: "conv1"}
	s.signal(dup)
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", worker.ID); n != 1 {
		t.Fatalf("duplicate rehomed review sent %d sigReview notice(s) to conv1, want 1", n)
	}
}

func TestSideHandoverSameMicrosecondTurnReceiptDeliversToMain(t *testing.T) {
	s, f, todo, initial := sideReviewProductionFixture(t)
	ctx := context.Background()
	origAt := todo.Supervision.UpdatedAt
	origKey := initial.Key

	// Record a told turn-ledger receipt for the exact same reviewKey, matching a same-microsecond handover after
	// superviseJudged already delivered to side1.
	toldAt := origAt
	s.tasks.turns.mu.Lock()
	s.tasks.turns.loaded = true
	if s.tasks.turns.doc.Turns == nil {
		s.tasks.turns.doc.Turns = map[string]taskTurn{}
	}
	s.tasks.turns.doc.Turns[todo.ID] = taskTurn{
		Machine: "box",
		Session: "worker1",
		Key:     "turn-1",
		State:   turnDone,
		Signal:  &turnSignal{Kind: sigReview, Key: origKey, Told: &toldAt},
	}
	s.tasks.turns.mu.Unlock()

	s.supervise.mu.Lock()
	sentKey := s.supervise.sent[initial.id()]
	s.supervise.mu.Unlock()
	if sentKey != origKey {
		t.Fatalf("supervise.sent[%q] = %q, want %q", initial.id(), sentKey, origKey)
	}

	// Hand supervision over to conv1 at the exact same timestamp so reviewKey(&v) equals origKey.
	v, changed, err := s.updateSupervisionGuardedAt(ctx, todo.ID, origAt, func(cur Todo) bool {
		return cur.DoneAt == nil
	}, func(v *Supervision) bool {
		v.Supervisor = "conv1"
		return true
	})
	if err != nil || !changed {
		t.Fatalf("updateSupervisionGuardedAt: changed=%v err=%v", changed, err)
	}
	if reviewKey(&v) != origKey {
		t.Fatalf("same-microsecond reviewKey = %q, want %q", reviewKey(&v), origKey)
	}

	s.signalHandedOverReview(todo.ID, "conv1", reviewKey(&v), "派出它的对话已归档，转给你跟进验收")
	// A watch pass before the digest flushes must keep Recipient=conv1 on both the pending signal and the turn ledger.
	s.superviseWatch(ctx, origAt)
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
		t.Fatalf("same-microsecond handover delivered %d sigReview notice(s) to conv1, want 1", n)
	}

	// Repeat admission for conv1 and watchSignals with empty Recipient must stay deduplicated.
	s.signalHandedOverReview(todo.ID, "conv1", origKey, "duplicate")
	s.superviseWatch(ctx, origAt)
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
		t.Fatalf("repeat same-microsecond handover delivered %d sigReview notice(s) to conv1, want 1", n)
	}
}

func TestSideHandoverExternalPRSecondaryTransferToLeadDeliversOnce(t *testing.T) {
	for _, stage := range []string{"pending_in_outbox", "queued_untried_on_main"} {
		t.Run(stage, func(t *testing.T) {
			st := &memStore{}
			s, f := newQuietServiceOn(t, st)
			setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
			ctx := context.Background()
			f.known["side1"] = true
			f.known["lead1"] = true
			setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
			f.setSessions("server",
				proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				proto.Session{
					SessionID:    "side1",
					MachineID:    "server",
					Assistant:    true,
					Status:       proto.StatusIdle,
					SourceKind:   proto.SourceRuntime,
					Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
				},
			)
			todo, err := s.createTodo(ctx, "Wait on upstream PR", "Wait for PR #12", "side1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.superviseTodo(ctx, todo.ID, "side1", []string{"PR merged"}, 30, "plan"); err != nil {
				t.Fatal(err)
			}
			args := externalPRParkArgs(externalPRSource)
			args["todo_id"] = todo.ID
			if _, err := runJSON(t, s, "side1", "task_review", args); err != nil {
				t.Fatal(err)
			}
			s.deploy.forge = &externalPRForge{terminal: "merged"}
			now := time.Now()
			s.sweepPRStatuses(ctx, now)
			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "side1", todo.ID); n != 1 {
				t.Fatalf("side1 received %d external-PR review notice(s), want 1", n)
			}
			origKey := reviewKey(todoNow(s, todo.ID).Supervision)

			// First handover: side1 archived -> main (conv1), holding the review before conv1 receives it.
			if _, err := s.setArchived(ctx, "server", "side1", true, titleByUser); err != nil {
				t.Fatal(err)
			}
			if stage == "queued_untried_on_main" {
				// Simulate conv1 busy so the handed-over review is admitted untried into conv1's relay queue
				// and populates supervise.sent[todo|review] == origKey.
				f.setSessions("server",
					proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking},
					proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
				)
				flushSuperviseDigest(ctx, s)
				if n := reviewSignalsTold(f, "conv1", todo.ID); n != 0 {
					t.Fatalf("busy conv1 unexpectedly received %d review notice(s)", n)
				}
			}

			// Second handover (explicit Move to / agent_transfer): conv1 -> direction lead1.
			s.supervise.routingMu.Lock()
			_, blockedBy, terr := s.transferTaskLocked(ctx, todoNow(s, todo.ID), "lead1", "conv1")
			s.supervise.routingMu.Unlock()
			if terr != nil || blockedBy != "" {
				t.Fatalf("transferTaskLocked(conv1 -> lead1): blockedBy=%q err=%v", blockedBy, terr)
			}

			// A same-key watch pass before flushing must not resurrect stale Recipient=conv1.
			s.superviseWatch(ctx, now.Add(time.Minute))
			s.supervise.mu.Lock()
			pendingReview, hasPending := s.supervise.pending[todo.ID+"|"+sigReview]
			s.supervise.mu.Unlock()
			if !hasPending || pendingReview.Key != origKey || pendingReview.To != "lead1" || pendingReview.Recipient != "lead1" {
				t.Fatalf("after secondary transfer, pending sigReview = (%+v, ok=%v), want Key=%q To=lead1 Recipient=lead1", pendingReview, hasPending, origKey)
			}

			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "lead1", todo.ID); n != 1 {
				t.Fatalf("lead1 received %d external-PR review notice(s) after secondary transfer, want 1", n)
			}
			if n := reviewSignalsTold(f, "conv1", todo.ID); n != 0 {
				t.Fatalf("conv1 received %d review notice(s) after transfer to lead1, want 0", n)
			}

			// Subsequent watches must stay deduplicated for lead1.
			s.superviseWatch(ctx, now.Add(2*time.Minute))
			flushSuperviseDigest(ctx, s)
			if n := reviewSignalsTold(f, "lead1", todo.ID); n != 1 {
				t.Fatalf("repeat watch delivered %d review notice(s) to lead1, want 1", n)
			}
		})
	}

	// Unit check: mergeSignals must not inherit an old Recipient when a same-key signal targets a different To.
	merged := mergeSignals(
		[]superviseSignal{{Todo: "todo_1", Kind: sigReview, Key: "external-pr@1", To: "conv1", Recipient: "conv1"}},
		[]superviseSignal{{Todo: "todo_1", Kind: sigReview, Key: "external-pr@1", To: "lead1"}},
	)
	if len(merged) != 1 || merged[0].To != "lead1" || merged[0].Recipient != "" {
		t.Fatalf("mergeSignals kept stale Recipient across target change: %+v", merged)
	}
}

func TestExternalPRDedupUpgradeCompatibilityPreservesLegacyReceipt(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})
	ctx := context.Background()
	f.known["lead1"] = true
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	todo, err := s.createTodo(ctx, "Wait on upstream PR", "Wait for PR #12", "lead1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo.ID, "lead1", []string{"PR merged"}, 30, "plan"); err != nil {
		t.Fatal(err)
	}
	args := externalPRParkArgs(externalPRSource)
	args["todo_id"] = todo.ID
	if _, err := runJSON(t, s, "lead1", "task_review", args); err != nil {
		t.Fatal(err)
	}
	s.deploy.forge = &externalPRForge{terminal: "merged"}
	now := time.Now()
	s.sweepPRStatuses(ctx, now)
	origKey := reviewKey(todoNow(s, todo.ID).Supervision)
	if !strings.HasPrefix(origKey, externalPRReviewPrefix) {
		t.Fatalf("reviewKey = %q, want prefix %q", origKey, externalPRReviewPrefix)
	}

	// Simulate a pre-upgrade persistent receipt stored under the legacy 3-element key [todo, "review", "external_pr"]
	// (no recipient/To element), with an empty in-memory cache as right after an upgrade restart.
	legacyKeyBytes, _ := json.Marshal([]string{todo.ID, sigReview, "external_pr"})
	sum := sha256.Sum256([]byte(origKey))
	legacyHistory := map[string]superviseFingerprint{
		string(legacyKeyBytes): {
			Hash: hex.EncodeToString(sum[:]),
			At:   now.Add(-time.Minute).UTC(),
			Key:  "",
			Todo: todo.ID,
		},
	}
	if err := st.PutSetting(ctx, superviseDedupKey, legacyHistory); err != nil {
		t.Fatal(err)
	}
	// Clear pending/sent/in-memory dedup to mimic a post-upgrade restart.
	s.supervise.mu.Lock()
	s.supervise.pending = map[string]superviseSignal{}
	s.supervise.sent = map[string]string{}
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Lock()
	s.supervise.dedup = nil
	s.supervise.dedupMu.Unlock()
	s.ackSupervisePending([]superviseSignal{{Todo: todo.ID, Kind: sigReview, Key: origKey}})

	// Ordinary post-upgrade watch for the same assigned supervisor (sets To=lead1, Recipient="") must hit the legacy
	// receipt and NOT re-wake lead1.
	s.superviseWatch(ctx, now.Add(time.Minute))
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "lead1", todo.ID); n != 0 {
		t.Fatalf("post-upgrade watch re-delivered %d external-PR review notice(s) to lead1, want 0", n)
	}

	// An explicit handover to a new supervisor (conv1, Recipient=conv1) must still deliver once.
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Supervisor = "conv1"
		v.SupervisorSource = "explicit"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.signalHandedOverReview(todo.ID, "conv1", origKey, "handed over to main")
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
		t.Fatalf("explicit handover to conv1 delivered %d external-PR review notice(s), want 1", n)
	}

	// After a restart where only conv1's recipient-scoped receipt exists, an ordinary watch for conv1 (To=conv1,
	// Recipient="") must still be deduplicated.
	delete(legacyHistory, string(legacyKeyBytes))
	convKeyBytes, _ := json.Marshal([]string{todo.ID, sigReview, "external_pr", "conv1"})
	legacyHistory[string(convKeyBytes)] = superviseFingerprint{
		Hash: hex.EncodeToString(sum[:]),
		At:   now.UTC(),
		Key:  "",
		Todo: todo.ID,
	}
	if err := st.PutSetting(ctx, superviseDedupKey, legacyHistory); err != nil {
		t.Fatal(err)
	}
	s.supervise.mu.Lock()
	s.supervise.pending = map[string]superviseSignal{}
	s.supervise.sent = map[string]string{}
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Lock()
	s.supervise.dedup = nil
	s.supervise.dedupMu.Unlock()
	s.superviseWatch(ctx, now.Add(2*time.Minute))
	flushSuperviseDigest(ctx, s)
	if n := reviewSignalsTold(f, "conv1", todo.ID); n != 1 {
		t.Fatalf("post-restart watch after handover delivered %d total review notice(s) to conv1, want 1", n)
	}
}

func TestAgySideConversationNotHandedOverWhileAgyRuntimeUnlisted(t *testing.T) {
	ctx := context.Background()
	s, f := newQuietService(t)
	setSuperviseTimingsForTest(s, superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour})

	const (
		agySide  = "agy-11111111-1111-1111-1111-111111111111"
		agyOther = "agy-99999999-9999-9999-9999-999999999999"
		agyDel   = "agy-22222222-2222-2222-2222-222222222222"
	)
	f.known[agySide] = true
	f.known[agyDel] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{
			SessionID:    agySide,
			MachineID:    "server",
			Agent:        "agy",
			Assistant:    true,
			Status:       proto.StatusIdle,
			SourceKind:   proto.SourceRuntime,
			Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
		},
		proto.Session{
			SessionID:    agyDel,
			MachineID:    "server",
			Agent:        "agy",
			Assistant:    true,
			Status:       proto.StatusIdle,
			SourceKind:   proto.SourceRuntime,
			Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
		},
	)

	todo1, err := s.createTodo(ctx, "Agy side task", "Verify agy runtime restart", agySide, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo1.ID, agySide, []string{"Verified"}, 30, "plan"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, todo1.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}

	todo2, err := s.createTodo(ctx, "Deleted agy side task", "Verify explicit deletion handover", agyDel, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo2.ID, agyDel, []string{"Verified"}, 30, "plan"); err != nil {
		t.Fatal(err)
	}

	// Simulate hub restart or missing runtimepkg where Claude assistant sessions are listed,
	// while zero agy assistant sessions are reported.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	if got := s.sideConversationStatus(ctx, s.settings(ctx), agySide); got != "" {
		t.Fatalf("sideConversationStatus(%q) while agy runtime unlisted = %q, want empty", agySide, got)
	}
	if s.conversationAway(ctx, s.settings(ctx), agySide) {
		t.Fatalf("conversationAway(%q) while agy runtime unlisted = true, want false", agySide)
	}
	// Even past sideReviewAfter, an unlisted agy runtime must not trigger gone or away handover.
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	if got := todoNow(s, todo1.ID).Supervision.Supervisor; got != agySide {
		t.Fatalf("todo1 supervisor moved to %q while agy runtime was unlisted, want %q", got, agySide)
	}

	// Once agy reconnects, agySide still owns todo1 and can review it.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{
			SessionID:    agySide,
			MachineID:    "server",
			Agent:        "agy",
			Assistant:    true,
			Status:       proto.StatusIdle,
			SourceKind:   proto.SourceRuntime,
			Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
		},
	)
	if !s.canReviewLedger(ctx, agySide, todoNow(s, todo1.ID), todoNow(s, todo1.ID).Supervision) {
		t.Fatal("agySide lost review permission after agy runtime reconnected")
	}

	// When the agy runtime IS listing other agy assistant sessions and agySide is absent, it hands over as gone.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{
			SessionID:    agyOther,
			MachineID:    "server",
			Agent:        "agy",
			Assistant:    true,
			Status:       proto.StatusIdle,
			SourceKind:   proto.SourceRuntime,
			Capabilities: &proto.SessionCapabilities{SupervisionPolicy: true},
		},
	)
	s.superviseWatch(ctx, time.Now())
	if got := todoNow(s, todo1.ID).Supervision.Supervisor; got != "conv1" {
		t.Fatalf("todo1 supervisor = %q after agy runtime listed other sessions without agySide, want conv1", got)
	}

	// Explicit durable deletion hands over even when zero agy assistant sessions remain listed.
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	s.refreshDurableConversationDeletion(ctx, "server", agyDel, deletedConversation{At: time.Now().UTC()})
	s.superviseWatch(ctx, time.Now())
	if got := todoNow(s, todo2.ID).Supervision.Supervisor; got != "conv1" {
		t.Fatalf("todo2 supervisor = %q after explicit durable deletion, want conv1", got)
	}
}
