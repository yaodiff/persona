package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const reviewTestModel = "gpt-6.2-sol"

var reviewTestTime = time.Date(2099, 10, 15, 12, 0, 0, 0, time.UTC)
var reviewSource = SessionRef{MachineID: "box", SessionID: "source"}
var reviewTarget = SessionRef{MachineID: "box", SessionID: "review"}

func reviewConfig() projectReviewConfig {
	return projectReviewConfig{
		Enabled: true, Target: reviewTarget, Model: reviewTestModel,
		Projects: []string{"alpha"}, AppInputs: true,
	}
}

func reviewTestService(t *testing.T, store *db.DB) (*Service, *fakeHub) {
	t.Helper()
	s, f := newTaskServiceOn(t, store)
	s.projectReview = &projectReview{projectReviewConfig: reviewConfig(), db: store, wake: make(chan struct{}, 1)}
	return s, f
}

func TestProjectReviewManagerRestart(t *testing.T) {
	ctx := context.Background()
	if os.Getenv("PERSONA_TEST_PROJECT_REVIEW_CAPTURE") == "1" {
		store, err := db.Open(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		s, _ := reviewTestService(t, store)
		todo := reviewNewTask(t, s, "alpha", "todo_alpha", reviewSource)
		a := proto.Session{MachineID: "box", SessionID: "source", UpdatedAt: reviewTestTime, Status: proto.StatusWorking}
		if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{SessionRef: reviewSource, MessageID: "owner-1", At: reviewTestTime}); err != nil {
			t.Fatal(err)
		}
		if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{SessionRef: reviewTarget, MessageID: "loop"}); err != nil {
			t.Fatal(err)
		}
		turn := []proto.Item{{ID: "finished", Kind: "turn_duration", TS: reviewTestTime, TurnDuration: &proto.TurnDuration{TurnID: "turn-1"}}}
		end, named := turnEndOf(turn)
		s.takeTurn(ctx, todo, a, turn, end, named, reviewTestTime.Add(-time.Minute), turnDone)
		s.takeTurn(ctx, todo, a, turn, end, named, reviewTestTime.Add(-time.Minute), turnDone)

		a.UpdatedAt = reviewTestTime.Add(time.Minute)
		a.Status = proto.StatusBlocked
		s.projectReviewSession(ctx, a, proto.StatusWorking)
		a.UpdatedAt = a.UpdatedAt.Add(time.Second)
		s.projectReviewSession(ctx, a, proto.StatusBlocked)

		return
	}

	store := todoTestDB(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(exe, "-test.run=^TestProjectReviewManagerRestart$")
	command.Env = append(os.Environ(), "PERSONA_TEST_PROJECT_REVIEW_CAPTURE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("capture process failed: %v\n%s", err, output)
	}
	s, f := reviewTestService(t, store)
	a := proto.Session{MachineID: "box", SessionID: "source", UpdatedAt: reviewTestTime.Add(time.Minute + 2*time.Second),
		Status: proto.StatusWorking, State: proto.StateWorking}
	s.projectReviewSession(ctx, a, "") // first live row after the producer process was restarted
	todo, ok, err := s.todo(ctx, "todo_alpha")
	if err != nil || !ok {
		t.Fatalf("new dispatch did not survive restart: %v %v", ok, err)
	}
	todo.Supervision = &Supervision{ExpectMinutes: 1}
	agents := []proto.Session{a}
	signals := watchSignals(todo, agents, a.UpdatedAt.Add(24*time.Hour))
	if len(signals) != 1 || signals[0].Kind != sigStalled {
		t.Fatalf("no watchdog evidence: %+v", signals)
	}
	s.projectReviewStall(ctx, signals[0], agents)
	s.projectReviewStall(ctx, signals[0], agents)
	target, _ := json.Marshal(reviewTarget)
	ready := proto.Session{SessionID: "review", Status: proto.StatusIdle, Agent: "codex", Model: reviewTestModel}
	f.sessions["box"] = []proto.Session{ready}
	advance := func(now time.Time) {
		t.Helper()
		if _, err := s.projectReviewStep(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"owner_input", "execution_completed", "waiting_for_owner", "stalled"}
	for i, kind := range want {
		e, ok, err := store.NextProjectReview(ctx, string(target))
		if err != nil || !ok || !strings.Contains(string(e.Event), `"kind": "`+kind+`"`) {
			t.Fatalf("#%d: want %s, got %+v %v", i, kind, e, err)
		}
		now := e.RetryAt.Add(time.Second)
		if i == 0 {
			f.sessions["box"][0].Model = "wrong-model"
			advance(now)
			retry, _, _ := store.NextProjectReview(ctx, string(target))
			if retry.ID != e.ID || retry.Attempts != 1 || len(f.sentOnce) != 0 {
				t.Fatalf("model mismatch advanced queue: %+v, sends=%d", retry, len(f.sentOnce))
			}
			f.sessions["box"][0].Model = reviewTestModel
			now = retry.RetryAt.Add(time.Second)
			f.failSendOnce = map[string]error{"box/review": errors.New("node disconnected")}
			advance(now)
			retry, _, _ = store.NextProjectReview(ctx, string(target))
			if retry.ID != e.ID || retry.Attempts != 2 || len(f.sentOnce) != 0 {
				t.Fatalf("failed delivery advanced queue: %+v", retry)
			}
			f.failSendOnce = nil
			now = retry.RetryAt.Add(time.Second)
		}
		advance(now)
		sends := len(f.sentOnce)
		if sends != 2*i+1 {
			t.Fatalf("send count=%d, want %d", sends, 2*i+1)
		}
		// The first event's first send went uncertain: the hub refuses that ID again, so the retry has its own.
		first := e.ID
		if i == 0 {
			first = e.ID + ":2"
		}
		sent := f.sentOnce[sends-1]
		if sent["session_id"] != "review" || sent["client_msg_id"] != first || !strings.Contains(sent["text"].(string), "alpha") {
			t.Fatalf("invalid delivery: %+v", sent)
		}
		seq := len(f.items) + 1
		f.items = append(f.items, proto.Item{Seq: seq, Role: "user", Kind: proto.KindText, ClientMsgID: first},
			proto.Item{Seq: seq + 1, Role: "assistant", Kind: proto.KindText, Text: "wrong receipt", Model: reviewTestModel},
			proto.Item{Seq: seq + 2, Kind: "turn_duration", ID: "review-finished"})
		f.itemsPage = 2
		retry, _, _ := store.NextProjectReview(ctx, string(target))
		advance(retry.RetryAt.Add(time.Second))
		head, ok, _ := store.NextProjectReview(ctx, string(target))
		if !ok || head.ID != e.ID {
			t.Fatal("transport success or arbitrary output settled delivery")
		}
		// The turn closed without a receipt: the event is sent again after it, under a new ID.
		again := f.sentOnce[len(f.sentOnce)-1]["client_msg_id"]
		if len(f.sentOnce) != sends+1 || again == first || head.HistoryAfter == nil || *head.HistoryAfter != seq+2 {
			t.Fatalf("closed turn without a receipt was not sent again after it: sends=%d id=%v after=%v", len(f.sentOnce), again, head.HistoryAfter)
		}
		seq = len(f.items) + 1
		f.items = append(f.items, proto.Item{Seq: seq, Role: "user", Kind: proto.KindText, ClientMsgID: again.(string)},
			proto.Item{Seq: seq + 1, Role: "assistant", Kind: proto.KindText, Text: "PROJECT_REVIEW_RECEIPT " + e.ID + " no_change", Model: reviewTestModel},
			proto.Item{Seq: seq + 2, Kind: "turn_duration", ID: "review-finished-again"},
			proto.Item{Seq: seq + 3, Role: "user", Kind: proto.KindText, ClientMsgID: "later"},
			proto.Item{Seq: seq + 4, Role: "assistant", Kind: proto.KindText, Text: "another turn", Model: "other-model"})
		// The sender and receipt reader are different Service instances sharing only the durable database and node.
		s = newServiceOn(t, f, store)
		s.projectReview = &projectReview{projectReviewConfig: reviewConfig(), db: store, wake: make(chan struct{}, 1)}
		f.sessions["box"][0].Model, f.sessions["box"][0].Status = "other-model", proto.StatusWorking
		retry, _, _ = store.NextProjectReview(ctx, string(target))
		advance(retry.RetryAt.Add(time.Second))
		f.sessions["box"][0] = ready
	}
	if _, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("late delivery after replay: %v %v", ok, err)
	}
	if tsk, ok := s.todoNow(todo.ID); !ok || tsk.DoneAt != nil {
		t.Fatalf("delivery changed acceptance: %+v", tsk)
	}
	t.Log("capture subprocess exited; dynamic task state restored; four receipts matched across pages after service restart and a newer turn; transport success alone held the queue")
}

func reviewNewTask(t *testing.T, s *Service, project, id string, agent SessionRef) Todo {
	t.Helper()
	ctx := context.Background()
	task, err := s.createTaskTodoAs(ctx, id, id, "", "main", "plan-test", project, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, task.ID, agent.MachineID, agent.SessionID, "", "", "codex"); err != nil {
		t.Fatal(err)
	}
	task, _ = s.todoNow(task.ID)
	return task
}

func TestProjectReviewDynamicScope(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	s, f := reviewTestService(t, store)
	target, _ := json.Marshal(reviewTarget)
	f.sessions["box"] = []proto.Session{{SessionID: reviewTarget.SessionID, Status: proto.StatusIdle, Agent: "codex", Model: reviewTestModel}}

	alpha := reviewNewTask(t, s, "alpha", "todo_dynamic", reviewSource)
	beta := SessionRef{MachineID: "box", SessionID: "beta"}
	reviewNewTask(t, s, "beta", "todo_beta", beta)
	for i, ref := range []SessionRef{reviewTarget, {MachineID: "box", SessionID: "main"}, beta} {
		if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{
			SessionRef: ref, MessageID: assistantwire.ProjectReviewMessageID(fmt.Sprintf("msg-%d", i)), At: reviewTestTime,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{
		SessionRef: reviewSource, MessageID: "multi-project-input", At: reviewTestTime, Unscoped: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{
		SessionRef: reviewSource, MessageID: "captured-before-assignment", At: alpha.Agents[0].LinkedAt.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	row := proto.Session{MachineID: reviewSource.MachineID, SessionID: reviewSource.SessionID}
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "old-turn", At: reviewTestTime}, turnDone)
	// The author withdraws alpha after it was queued. No previously authorized refs may leave the manager.
	s.projectReview.Projects = nil
	for i := 0; i < 5; i++ {
		next, ok, err := store.NextProjectReview(ctx, string(target))
		if err != nil || !ok {
			t.Fatalf("review-only event #%d: %v %v", i, ok, err)
		}
		if i < 4 && !strings.Contains(string(next.Event), `"project": ""`) {
			t.Fatalf("unscoped input received a project at capture: %s", next.Event)
		}
		if _, err := s.projectReviewStep(ctx, next.RetryAt.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		msg := f.sentOnce[i]["text"].(string)
		if strings.Contains(msg, "todo_") || strings.Contains(msg, "old-turn") || strings.Contains(msg, "msg-") ||
			strings.Contains(msg, "multi-project-input") || strings.Contains(msg, "captured-before-assignment") ||
			!strings.Contains(msg, next.ID+" triage_only") {
			t.Fatalf("unauthorized facts in review-only prompt: %s", msg)
		}
		if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
			t.Fatal(err)
		}
	}

	s.projectReview.Projects = []string{"alpha"}
	if err := s.releaseStoppedAgents(ctx, alpha.ID, alpha.Agents); err != nil {
		t.Fatal(err)
	}
	replacement := SessionRef{MachineID: "box", SessionID: "replacement"}
	if err := s.linkTodoAgent(ctx, alpha.ID, replacement.MachineID, replacement.SessionID, "", "", "codex"); err != nil {
		t.Fatal(err)
	}
	alpha, _ = s.todoNow(alpha.ID)
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "released-agent", At: reviewTestTime}, turnDone)
	row.SessionID = replacement.SessionID
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "replacement-done", At: reviewTestTime}, turnDone)
	next, ok, err := store.NextProjectReview(ctx, string(target))
	if err != nil || !ok || !strings.Contains(string(next.Event), "replacement-done") || !strings.Contains(string(next.Event), alpha.ID) {
		t.Fatalf("replacement's event did not retain project attribution: %+v %v", next, err)
	}
	if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{
		SessionRef: replacement, MessageID: "continued-task", At: reviewTestTime,
	}); err != nil {
		t.Fatal(err)
	}
	next, ok, err = store.NextProjectReview(ctx, string(target))
	if err != nil || !ok || !strings.Contains(string(next.Event), "continued-task") || !strings.Contains(string(next.Event), alpha.ID) {
		t.Fatalf("continuation on replacement did not follow the task ledger: %+v %v", next, err)
	}
	if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("unexpected extra event: %v %v", ok, err)
	}
	looseAgent := SessionRef{MachineID: "box", SessionID: "unassigned"}
	loose := reviewNewTask(t, s, "", "todo_unassigned", looseAgent)
	looseRow := proto.Session{MachineID: looseAgent.MachineID, SessionID: looseAgent.SessionID}
	end := turnClose{Key: "unassigned-done", At: reviewTestTime}
	s.projectReviewTurn(ctx, loose, looseRow, end, turnDone)
	if _, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("unassigned task reported without opt-in: %v %v", ok, err)
	}
	s.projectReview.ReviewUnassigned = true
	s.projectReviewTurn(ctx, loose, looseRow, end, turnDone)
	next, ok, err = store.NextProjectReview(ctx, string(target))
	if err != nil || !ok || !strings.Contains(string(next.Event), "unassigned-done") {
		t.Fatalf("opted-in unassigned task did not produce triage: %s %v %v", next.Event, ok, err)
	}
	if _, err := s.projectReviewStep(ctx, next.RetryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	msg := f.sentOnce[len(f.sentOnce)-1]["text"].(string)
	if strings.Contains(msg, loose.ID) || strings.Contains(msg, end.Key) || !strings.Contains(msg, next.ID+" triage_only") {
		t.Fatalf("unassigned task leaked to triage: %s", msg)
	}
	if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
		t.Fatal(err)
	}
	t.Log("main, unrelated project, revoked binding and opted-in unassigned task arrived as opaque triage; continuation and replacement inherited their authorized task")
}

func TestProjectReviewRejectsInvalidConfig(t *testing.T) {
	c := reviewConfig()
	c.Projects = []string{"alpha project"}
	if err := c.validate(); err == nil {
		t.Fatal("invalid project reference accepted")
	}
	c.Enabled = false
	if err := c.validate(); err != nil {
		t.Fatalf("disabled configuration: %v", err)
	}
}

func TestProjectReviewPairedFeishuOwnerInput(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	store := todoTestDB(t)
	s, f := reviewTestService(t, store)
	s.projectReview.FeishuOwnerInputs = true
	status := FeishuStatus{Enabled: true, Ready: true, Owner: "ou_paired", Chats: []string{"oc_bound"}}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return status, nil })
	s.larkModule().RememberThread("bot-review", botTarget{Kind: targetAgent, MachineID: reviewTarget.MachineID, SessionID: reviewTarget.SessionID}, "Review")
	for _, tc := range []struct{ id, sender, senderType, chat, parent, from string }{
		{"om_owner", "ou_paired", "user", "oc_bound", "", ""},
		{"om_owner", "ou_paired", "user", "oc_bound", "", ""},
		{"om_other", "ou_guest", "user", "oc_bound", "", ""},
		{"om_unbound", "ou_paired", "user", "oc_other", "", ""},
		{"om_bot", "ou_paired", "app", "oc_bound", "", ""},
		{"om_elsewhere", "ou_paired", "user", "oc_bound", "", FeishuFromElsewhere},
		{"om_self", "ou_paired", "user", "oc_bound", "bot-review", ""},
	} {
		raw := json.RawMessage(fmt.Sprintf(`{"sender":{"sender_type":%q,"sender_id":{"open_id":%q}},"message":{"message_id":%q,"chat_id":%q,"parent_id":%q,"chat_type":"p2p","message_type":"text","content":"private original"}}`,
			tc.senderType, tc.sender, tc.id, tc.chat, tc.parent))
		if err := s.FeishuMessage(ctx, FeishuMessage{Event: raw, From: tc.from}); err != nil {
			t.Fatal(err)
		}
		// The chat handles each message as it always does (the duplicate is a real redelivery, taken once).
		wait, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.larkModule().Wait(wait)
		cancel()
		if err != nil {
			t.Fatalf("the chat did not handle %s: %v", tc.id, err)
		}
	}
	target, _ := json.Marshal(reviewTarget)
	event, ok, err := store.NextProjectReview(ctx, string(target))
	if err != nil || !ok || !strings.Contains(string(event.Event), `"reference": "feishu:om_owner"`) ||
		!strings.Contains(string(event.Event), `"project": ""`) || strings.Contains(string(event.Event), "private original") {
		t.Fatalf("want an opaque owner event: %s %v %v", event.Event, ok, err)
	}
	if err := store.SettleProjectReview(ctx, event.ID, "review_receipt_no_change"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("non-owner, unbound, self, or duplicate Feishu input reached the queue: %v %v", ok, err)
	}
	t.Log("paired owner handover arrived once; other sender, bot, unbound chat, elsewhere, and review target excluded")
}

// The hub queues an input with its own clock, and a new Agent's first prompt before the to-do link.
// Neither a later task update nor that ordering may take an authorized input out of its project.
func TestProjectReviewOwnerInputAssignmentEpoch(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	s, _ := reviewTestService(t, store)
	target, _ := json.Marshal(reviewTarget)
	alpha := reviewNewTask(t, s, "alpha", "todo_epoch", reviewSource)
	linked := alpha.Agents[0].LinkedAt
	notes := "a later edit"
	if _, err := s.editTodo(ctx, alpha.ID, db.TodoEdit{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	if updated, _ := s.todoNow(alpha.ID); !linked.Before(updated.UpdatedAt) {
		t.Fatalf("fixture: the edit did not move UpdatedAt past the link: %v %v", linked, updated.UpdatedAt)
	}
	cases := []struct {
		id     string
		at     time.Time
		scoped bool
	}{
		{"first-agent:" + reviewSource.SessionID, linked.Add(-time.Second), true},
		{"after-link-before-edit", linked, true},
		{"before-link", linked.Add(-time.Second), false},
		{"first-agent:another-session", linked.Add(-time.Second), false},
		{"no-time", time.Time{}, false},
		// The send API takes any non-empty client_msg_id: these are reviewed under a hash of the id.
		{"draft #3 + more", linked, true},
		{strings.Repeat("x", 300), linked, true},
	}
	for _, c := range cases {
		if err := s.projectReviewOwnerInput(ctx, assistantwire.ProjectReviewOwnerInput{SessionRef: reviewSource, MessageID: assistantwire.ProjectReviewMessageID(c.id), At: c.at}); err != nil {
			t.Fatal(err)
		}
		next, ok, err := store.NextProjectReview(ctx, string(target))
		ref := projectReviewOwnerRef(c.id)
		if err != nil || !ok || !strings.Contains(string(next.Event), `"reference": "`+ref+`"`) {
			t.Fatalf("%s: not queued: %s %v %v", c.id, next.Event, ok, err)
		}
		if ref != c.id && (!strings.HasPrefix(ref, "app-message:") || strings.Contains(string(next.Event), c.id)) {
			t.Errorf("%.40s: the raw id reached the event: %s", c.id, next.Event)
		}
		if got := strings.Contains(string(next.Event), alpha.ID); got != c.scoped {
			t.Errorf("%s: scoped=%v, want %v: %s", c.id, got, c.scoped, next.Event)
		}
		if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectReviewOutcomeTriageOnlyIsReviewOnly(t *testing.T) {
	cases := []struct {
		reply          string
		triage, scoped bool
		want           string
	}{
		{"done\nPROJECT_REVIEW_RECEIPT id-1 triage_only", true, false, "triage_only"},
		{"PROJECT_REVIEW_RECEIPT id-1 triage_only", false, true, ""},
		{"PROJECT_REVIEW_RECEIPT id-1 facts_recorded", false, true, "facts_recorded"},
		{"PROJECT_REVIEW_RECEIPT id-1 facts_recorded", true, false, ""},
		{"PROJECT_REVIEW_RECEIPT id-2 no_change", false, true, ""},
		// downgraded: the scoped attempt may have landed before the triage resend
		{"PROJECT_REVIEW_RECEIPT id-1 no_change", true, true, "no_change"},
		{"PROJECT_REVIEW_RECEIPT id-1 triage_only", true, true, "triage_only"},
	}
	for _, c := range cases {
		if got := projectReviewOutcome(c.reply, "id-1", c.triage, c.scoped); got != c.want {
			t.Errorf("%q triage=%v scoped=%v: got %q, want %q", c.reply, c.triage, c.scoped, got, c.want)
		}
	}
}

// A scoped attempt is reserved and its send goes unconfirmed; the task loses its project before the retry. The retry
// sends only the triage prompt, and the triage_only receipt it asks for must settle the delivery, or the target's
// queue waits behind it forever. The event is an interrupted Codex turn, whose end is a positional item id.
func TestProjectReviewDowngradeAfterReservation(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	s, f := reviewTestService(t, store)
	target, _ := json.Marshal(reviewTarget)
	f.sessions["box"] = []proto.Session{{SessionID: reviewTarget.SessionID, Status: proto.StatusIdle, Agent: "codex", Model: reviewTestModel}}
	alpha := reviewNewTask(t, s, "alpha", "todo_downgrade", reviewSource)
	row := proto.Session{MachineID: reviewSource.MachineID, SessionID: reviewSource.SessionID}
	if !proto.PositionalItemID("line12#0") {
		t.Fatal("fixture: not a positional item id")
	}
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "line12#0", At: reviewTestTime}, turnInterrupted)
	e, ok, err := store.NextProjectReview(ctx, string(target))
	if err != nil || !ok || !strings.Contains(string(e.Event), `"reference": "line12#0"`) || !strings.Contains(string(e.Event), "execution_interrupted") {
		t.Fatalf("interrupted Codex turn not queued: %s %v %v", e.Event, ok, err)
	}
	f.failSendOnce = map[string]error{"box/review": errors.New("node disconnected")}
	if _, err := s.projectReviewStep(ctx, e.RetryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	e, _, _ = store.NextProjectReview(ctx, string(target))
	if e.HistoryAfter == nil || e.ReviewOnly || e.Downgraded || len(f.sentOnce) != 0 {
		t.Fatalf("fixture: want a reserved scoped attempt, got %+v sends=%d", e, len(f.sentOnce))
	}
	f.failSendOnce = nil
	s.projectReview.Projects = nil // authorization revoked between attempts
	if _, err := s.projectReviewStep(ctx, e.RetryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.sentOnce) != 1 {
		t.Fatalf("retry not sent: %d", len(f.sentOnce))
	}
	if msg := f.sentOnce[0]["text"].(string); strings.Contains(msg, alpha.ID) || !strings.Contains(msg, e.ID+" triage_only") {
		t.Fatalf("retry after revocation was not the triage prompt: %s", msg)
	}
	e, _, _ = store.NextProjectReview(ctx, string(target))
	if !e.ReviewOnly || !e.Downgraded {
		t.Fatalf("downgrade not saved before the send: %+v", e)
	}
	f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: e.ID},
		proto.Item{Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "PROJECT_REVIEW_RECEIPT " + e.ID + " triage_only", Model: reviewTestModel},
		proto.Item{Seq: 3, Kind: "turn_duration", ID: "review-finished"})
	if _, err := s.projectReviewStep(ctx, e.RetryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if head, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("triage receipt did not settle the downgraded delivery: %+v %v %v", head, ok, err)
	}
}

// Turn ends and session rows carry the node's clock. A first turn can end, or a row become blocked, before the hub
// writes the link, and a node clock can run behind the hub's: neither may lose the event.
func TestProjectReviewNodeTimesBeforeTheLink(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	s, _ := reviewTestService(t, store)
	target, _ := json.Marshal(reviewTarget)
	alpha := reviewNewTask(t, s, "alpha", "todo_node_clock", reviewSource)
	early := alpha.Agents[0].LinkedAt.Add(-30 * time.Second)
	row := proto.Session{MachineID: reviewSource.MachineID, SessionID: reviewSource.SessionID, UpdatedAt: early}
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "fast-first-turn", At: early}, turnDone)
	row.Status = proto.StatusBlocked
	s.projectReviewSession(ctx, row, proto.StatusWorking)
	// Its own task: a blocking episode that is already open absorbs another blocking event.
	quiet := SessionRef{MachineID: "box", SessionID: "quiet"}
	still := reviewNewTask(t, s, "alpha", "todo_node_clock_stall", quiet)
	idle := proto.Session{MachineID: quiet.MachineID, SessionID: quiet.SessionID, UpdatedAt: still.Agents[0].LinkedAt.Add(-time.Minute),
		Status: proto.StatusWorking}
	stall := superviseSignal{Todo: still.ID, Kind: sigStalled, Key: "stalled-since-early", Machine: quiet.MachineID, Session: quiet.SessionID}
	s.projectReviewStall(ctx, stall, []proto.Session{idle})
	for _, want := range [][2]string{{alpha.ID, "fast-first-turn"}, {alpha.ID, "waiting_for_owner"}, {still.ID, "stalled-since-early"}} {
		next, ok, err := store.NextProjectReview(ctx, string(target))
		if err != nil || !ok || !strings.Contains(string(next.Event), `"`+want[0]+`"`) || !strings.Contains(string(next.Event), want[1]) {
			t.Fatalf("%s: not queued for %s: %s %v %v", want[1], want[0], next.Event, ok, err)
		}
		if err := store.SettleProjectReview(ctx, next.ID, "send_unconfirmed"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.releaseStoppedAgents(ctx, alpha.ID, alpha.Agents); err != nil {
		t.Fatal(err)
	}
	alpha, _ = s.todoNow(alpha.ID)
	s.projectReviewTurn(ctx, alpha, row, turnClose{Key: "after-release", At: reviewTestTime}, turnDone)
	if next, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("a released agent's turn was queued: %s %v %v", next.Event, ok, err)
	}
}

// A failed settlement waits like any other error: a zero wait would re-read the same delivery in a tight loop for
// as long as the database refuses the write.
func TestProjectReviewSettleFailureBacksOff(t *testing.T) {
	ctx := context.Background()
	store := todoTestDB(t)
	s, _ := reviewTestService(t, store)
	target, _ := json.Marshal(reviewTarget)
	if err := s.projectReview.record(ctx, projectReviewBinding{SessionRef: reviewTarget}, "owner_input", "own-words", "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	next, ok, err := store.NextProjectReview(ctx, string(target))
	if err != nil || !ok {
		t.Fatalf("fixture: nothing queued: %v %v", ok, err)
	}
	restore := rejectExpiredAnswerWrite(t, store, "project_review_deliveries", "NEW.settled_at IS NOT NULL")
	wait, err := s.projectReviewStep(ctx, next.RetryAt.Add(time.Second))
	if err == nil || wait <= 0 {
		t.Fatalf("failed settlement: wait=%v err=%v, want a backoff and the error", wait, err)
	}
	restore()
	if wait, err := s.projectReviewStep(ctx, next.RetryAt.Add(time.Second)); err != nil || wait != 0 {
		t.Fatalf("settled self-source: wait=%v err=%v", wait, err)
	}
	if _, ok, err := store.NextProjectReview(ctx, string(target)); err != nil || ok {
		t.Fatalf("self-source event not settled: %v %v", ok, err)
	}
}

// reviewOnceGuard answers session.send_once like the hub's once-only guard (hub sendOnce and sendOutcome): a send
// whose outcome the hub never learned leaves its client message ID unknown, and that ID is refused from then on
// without reaching the node; a delivered ID is a no-op.
type reviewOnceGuard struct {
	state map[string]string
	sent  []string // IDs the node took
	calls int
	lose  int // the next sends time out after the node may have had them
}

func (g *reviewOnceGuard) answer(_ string, params json.RawMessage) (any, error) {
	var a struct {
		ID string `json:"client_msg_id"`
	}
	_ = json.Unmarshal(params, &a)
	g.calls++
	switch g.state[a.ID] {
	case "delivered":
		return map[string]any{}, nil
	case "unknown":
		return nil, errors.New("hub-assistant_channels.send_in_doubt")
	}
	if g.lose > 0 {
		g.lose--
		g.state[a.ID] = "unknown"
		return nil, context.DeadlineExceeded
	}
	g.state[a.ID] = "delivered"
	g.sent = append(g.sent, a.ID)
	return map[string]any{}, nil
}

func reviewGuarded(t *testing.T) (*Service, *fakeHub, *db.DB, *reviewOnceGuard) {
	t.Helper()
	store := todoTestDB(t)
	s, f := reviewTestService(t, store)
	f.sessions["box"] = []proto.Session{{SessionID: reviewTarget.SessionID, Status: proto.StatusIdle, Agent: "codex", Model: reviewTestModel}}
	g := &reviewOnceGuard{state: map[string]string{}}
	f.answerNode(DeviceSessionSendOnce, g.answer)
	return s, f, store, g
}

// reviewQueue records an unscoped event and returns the target's head (the oldest unsettled event).
func reviewQueue(t *testing.T, s *Service, store *db.DB, key string) db.ProjectReviewDelivery {
	t.Helper()
	ctx := context.Background()
	if err := s.projectReview.record(ctx, projectReviewBinding{SessionRef: reviewSource}, "owner_input", key, "", time.Time{}); err != nil {
		t.Fatal(err)
	}
	return reviewHead(t, store)
}

func reviewHead(t *testing.T, store *db.DB) db.ProjectReviewDelivery {
	t.Helper()
	target, _ := json.Marshal(reviewTarget)
	e, _, err := store.NextProjectReview(context.Background(), string(target))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// reviewStepHead runs one step at the head's retry time.
func reviewStepHead(t *testing.T, s *Service, store *db.DB) {
	t.Helper()
	if _, err := s.projectReviewStep(context.Background(), reviewHead(t, store).RetryAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func reviewOutcome(t *testing.T, store *db.DB, id string) (outcome string, settled bool) {
	t.Helper()
	if err := store.Transaction(context.Background(), func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT outcome, settled_at IS NOT NULL FROM project_review_deliveries WHERE id=$1`,
			id).Scan(&outcome, &settled)
	}); err != nil {
		t.Fatal(err)
	}
	return outcome, settled
}

// reviewTurn appends a review turn started by client message ID id that ends with reply (a receipt or not).
func reviewTurn(f *fakeHub, id, reply string) {
	seq := len(f.items) + 1
	f.items = append(f.items, proto.Item{Seq: seq, Role: "user", Kind: proto.KindText, ClientMsgID: id},
		proto.Item{Seq: seq + 1, Role: "assistant", Kind: proto.KindText, Text: reply, Model: reviewTestModel},
		proto.Item{Seq: seq + 2, Kind: "turn_duration", ID: fmt.Sprintf("turn-%d", seq)})
}

// A send that timed out leaves its ID unknown at the hub, which then refuses that ID without asking the node. The
// retry must use a new one, or the head of the queue never goes out and every later event waits behind it. A send
// the hub took is awaited, not sent again.
func TestProjectReviewUncertainSendGetsANewID(t *testing.T) {
	s, f, store, g := reviewGuarded(t)
	e := reviewQueue(t, s, store, "owner-uncertain")
	g.lose = 1
	reviewStepHead(t, s, store)
	if len(g.sent) != 0 || g.state[e.ID] != "unknown" {
		t.Fatalf("fixture: want an uncertain first send, got %+v", g)
	}
	reviewStepHead(t, s, store)
	if want := []string{e.ID + ":2"}; !slices.Equal(g.sent, want) {
		t.Fatalf("retry after an uncertain send: node took %v, want %v (head %+v)", g.sent, want, reviewHead(t, store))
	}
	calls := g.calls
	reviewStepHead(t, s, store) // the echo is not in the transcript yet
	if g.calls != calls {
		t.Fatalf("a send the hub confirmed was sent again before its echo: %d calls, want %d", g.calls, calls)
	}
	// The first send may still land after all, in the same turn as the second: the turn's receipt counts for both.
	f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: e.ID},
		proto.Item{Seq: 2, Role: "user", Kind: proto.KindText, ClientMsgID: e.ID + ":2"},
		proto.Item{Seq: 3, Role: "assistant", Kind: proto.KindText, Text: "PROJECT_REVIEW_RECEIPT " + e.ID + " triage_only", Model: reviewTestModel},
		proto.Item{Seq: 4, Kind: "turn_duration", ID: "both-landed"})
	reviewStepHead(t, s, store)
	if outcome, settled := reviewOutcome(t, store, e.ID); !settled || outcome != "review_receipt_triage_only" {
		t.Fatalf("receipt of the late first send: outcome=%q settled=%v", outcome, settled)
	}
	if g.calls != calls {
		t.Fatalf("sent again although the receipt was there: %d calls, want %d", g.calls, calls)
	}
}

// A review turn that closes without a valid receipt (stopped, an unrelated message, a reply without the receipt
// line) is not waited on forever: the event is sent again after that turn, under a new ID, a bounded number of times.
// A turn still running is waited on. A confirmed send whose echo is still missing projectReviewEchoWait later, with the
// target idle, is sent again too. Each kind of failure settles the event after projectReviewMaxSends sends, so the
// next event goes out.
func TestProjectReviewSendsAreBounded(t *testing.T) {
	t.Run("closed turns without a receipt", func(t *testing.T) {
		s, f, store, g := reviewGuarded(t)
		e := reviewQueue(t, s, store, "owner-closed")
		reviewStepHead(t, s, store)
		seq := len(f.items) + 1
		f.items = append(f.items, proto.Item{Seq: seq, Role: "user", Kind: proto.KindText, ClientMsgID: e.ID},
			proto.Item{Seq: seq + 1, Role: "assistant", Kind: proto.KindText, Text: "working on it", Model: reviewTestModel})
		reviewStepHead(t, s, store)
		if len(g.sent) != 1 {
			t.Fatalf("a running review turn was sent again: %v", g.sent)
		}
		f.items = append(f.items, proto.Item{Seq: seq + 2, Kind: proto.KindNotice, Text: userStoppedNotice})
		reviewStepHead(t, s, store)
		if len(g.sent) != 2 || g.sent[1] != e.ID+":2" {
			t.Fatalf("a stopped review turn was not sent again under a new ID: %v", g.sent)
		}
		if head := reviewHead(t, store); head.ID != e.ID || head.HistoryAfter == nil || *head.HistoryAfter != seq+2 {
			t.Fatalf("the next receipt is not looked for after the closed turn: %+v", head)
		}
		for n := 2; n < projectReviewMaxSends; n++ {
			reviewTurn(f, projectReviewSendID(e.ID, n), "no receipt line")
			reviewStepHead(t, s, store)
		}
		if len(g.sent) != projectReviewMaxSends {
			t.Fatalf("sends=%d, want %d: %v", len(g.sent), projectReviewMaxSends, g.sent)
		}
		reviewQueue(t, s, store, "owner-after-closed")
		reviewTurn(f, projectReviewSendID(e.ID, projectReviewMaxSends), "PROJECT_REVIEW_RECEIPT someone-else triage_only")
		reviewStepHead(t, s, store)
		if outcome, settled := reviewOutcome(t, store, e.ID); !settled || outcome != "review_receipt_missing" {
			t.Fatalf("after %d closed turns: outcome=%q settled=%v", projectReviewMaxSends, outcome, settled)
		}
		next := reviewHead(t, store)
		reviewStepHead(t, s, store)
		if next.ID == e.ID || g.sent[len(g.sent)-1] != next.ID {
			t.Fatalf("the next event did not go out: %v", g.sent)
		}
	})
	t.Run("confirmed sends that never echo", func(t *testing.T) {
		s, f, store, g := reviewGuarded(t)
		e := reviewQueue(t, s, store, "owner-dropped")
		reviewStepHead(t, s, store)
		if head := reviewHead(t, store); len(g.sent) != 1 || !head.SendConfirmed || head.SendConfirmedAt.IsZero() {
			t.Fatalf("fixture: want one confirmed send, got %v (head %+v)", g.sent, head)
		}
		// stepAfterWait runs one step once it is due and the confirmed send's echo has been missing for
		// projectReviewEchoWait.
		stepAfterWait := func() {
			t.Helper()
			head := reviewHead(t, store)
			at := head.SendConfirmedAt.Add(projectReviewEchoWait)
			if head.RetryAt.After(at) {
				at = head.RetryAt
			}
			if _, err := s.projectReviewStep(context.Background(), at.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		}
		reviewStepHead(t, s, store)
		if len(g.sent) != 1 {
			t.Fatalf("a confirmed send was repeated before its echo had time to show up: %v", g.sent)
		}
		f.sessions["box"][0].Status = proto.StatusWorking // the echo may be queued behind the running turn
		stepAfterWait()
		if len(g.sent) != 1 {
			t.Fatalf("a confirmed send was repeated while the target was busy: %v", g.sent)
		}
		f.sessions["box"][0].Status = proto.StatusIdle
		for n := 2; n <= projectReviewMaxSends; n++ {
			stepAfterWait()
			if len(g.sent) != n || g.sent[n-1] != projectReviewSendID(e.ID, n) {
				t.Fatalf("a confirmed send whose echo never came was not sent again under a new ID: %v", g.sent)
			}
		}
		reviewQueue(t, s, store, "owner-after-dropped")
		f.sessions["box"][0].Status = proto.StatusWorking
		stepAfterWait()
		if _, settled := reviewOutcome(t, store, e.ID); settled {
			t.Fatal("the last confirmed send was given up while the target was busy and its echo could still be queued")
		}
		f.sessions["box"][0].Status = proto.StatusIdle
		stepAfterWait()
		if outcome, settled := reviewOutcome(t, store, e.ID); !settled || outcome != "send_echo_missing" {
			t.Fatalf("after %d confirmed sends without an echo: outcome=%q settled=%v", projectReviewMaxSends, outcome, settled)
		}
		next := reviewHead(t, store)
		reviewStepHead(t, s, store)
		if next.ID == e.ID || g.sent[len(g.sent)-1] != next.ID {
			t.Fatalf("the next event did not go out: %v", g.sent)
		}
	})
	t.Run("uncertain sends", func(t *testing.T) {
		s, _, store, g := reviewGuarded(t)
		e := reviewQueue(t, s, store, "owner-lost")
		g.lose = projectReviewMaxSends
		for range projectReviewMaxSends {
			reviewStepHead(t, s, store)
		}
		reviewQueue(t, s, store, "owner-after-lost")
		if head := reviewHead(t, store); head.ID != e.ID || head.Sends != projectReviewMaxSends {
			t.Fatalf("fixture: %+v", head)
		}
		reviewStepHead(t, s, store)
		if outcome, settled := reviewOutcome(t, store, e.ID); !settled || outcome != "send_unconfirmed" {
			t.Fatalf("after %d uncertain sends: outcome=%q settled=%v", projectReviewMaxSends, outcome, settled)
		}
		next := reviewHead(t, store)
		reviewStepHead(t, s, store)
		if next.ID == e.ID || len(g.sent) != 1 || g.sent[0] != next.ID {
			t.Fatalf("the next event did not go out: %v", g.sent)
		}
	})
}
