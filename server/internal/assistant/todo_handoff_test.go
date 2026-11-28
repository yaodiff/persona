package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const handoffCheck = "Verify the release candidate"

func handoffTestTodos(t *testing.T, store Store) (*Service, *fakeHub, Todo, Todo, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, store)
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	var runs []proto.Session
	add := func(title, sid string) Todo {
		task, err := s.createTodo(ctx, title, "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, task.ID, "conv1", []string{handoffCheck}, 120)
		if err = s.linkTodoAgent(ctx, task.ID, "box", sid, title, "/code/app", "codex"); err != nil {
			t.Fatal(err)
		}
		runs = append(runs, proto.Session{MachineID: "box", SessionID: sid, Status: proto.StatusIdle, State: proto.StateIdle,
			Agent: "codex", UpdatedAt: time.Now()})
		return todoNow(s, task.ID)
	}
	a, b, receiving := add("Candidate A", "origin-a"), add("Candidate B", "origin-b"), add("Release verification", "receiver")
	f.setSessions("box", runs...)
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{
		DeviceSessionSend: func(_ string, raw json.RawMessage) (any, error) {
			var params map[string]any
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, err
			}
			f.agentSent = append(f.agentSent, params)
			return map[string]bool{"ok": true, "queued": true}, nil
		},
	}
	f.mu.Unlock()
	return s, f, a, b, receiving
}

func sendHandoffTest(t *testing.T, s *Service, f *fakeHub, self, origin, receiving, head string) (map[string]string, error) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{
		"machine": "box", "session_id": "receiver", "text": "Please run the release check.",
		"handoff": map[string]string{
			"todo_id": origin, "receiving_todo_id": receiving, "head": head, "check": handoffCheck, "until": "2h",
		},
	})
	_, err := s.runTool(context.Background(), "server", self, "agent_send", args)
	if err != nil {
		return nil, err
	}
	var receipt map[string]string
	for _, line := range strings.Split(strings.Join(sentToAgent(f), "\n"), "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var fields map[string]string
		if json.Unmarshal([]byte(line), &fields) == nil && fields["todo_id"] == origin {
			// The latest request for this origin is the one its Agent just received.
			receipt = fields
		}
	}
	return receipt, nil
}

func reportHandoffTest(s *Service, session, kind string, receipt map[string]string) (string, error) {
	args, _ := json.Marshal(map[string]any{"kind": kind, "text": "The release check ran.",
		"evidence": []string{"check-release: 4 passed"}, "handoff_receipt": receipt})
	return s.reportToSupervisor(context.Background(), "box", session, args, time.Now())
}

func TestTaskHandoffInboxStaysWithOrigin(t *testing.T) {
	for _, mode := range []string{"omitted", "explicit", "already linked to origin", "already linked to receiver", "unassigned receiver"} {
		t.Run(mode, func(t *testing.T) {
			d := todoTestDB(t)
			s, f, origin, _, receiving := handoffTestTodos(t, d)
			it := taskInboxItem(t, s, "example:release-check", "server", "conv1")
			session := "receiver"
			if mode == "unassigned receiver" {
				session = "unassigned"
				f.setSessions("box",
					proto.Session{MachineID: "box", SessionID: "origin-a", Status: proto.StatusIdle, Agent: "codex"},
					proto.Session{MachineID: "box", SessionID: session, Status: proto.StatusIdle, Agent: "codex"})
			}
			if mode == "already linked to origin" || mode == "already linked to receiver" {
				id := origin.ID
				if mode == "already linked to receiver" {
					id = receiving.ID
				}
				source := PlanTask{InboxItem: it.ID}
				if err := s.planInboxSource(t.Context(), "server", "conv1", &source); err != nil {
					t.Fatal(err)
				}
				if err := s.linkExistingTaskInbox(t.Context(), "conv1", source, id, id, nil); err != nil {
					t.Fatal(err)
				}
			}
			args := map[string]any{"machine": "box", "session_id": session, "text": "Please run the release check.",
				"handoff": map[string]string{"todo_id": origin.ID, "receiving_todo_id": receiving.ID, "head": strings.Repeat("a", 40),
					"check": handoffCheck, "until": "2h"}}
			if mode != "omitted" {
				args["inbox_item"] = it.ID
			}
			got, err := existingInboxCall(t, s, "agent_send", args)
			if mode == "unassigned receiver" || mode == "already linked to receiver" {
				if err == nil || len(sentToAgent(f)) != 0 {
					t.Fatalf("invalid handoff sent a message: %s, %v", got, err)
				}
				return
			}
			if err != nil || len(sentToAgent(f)) != 1 {
				t.Fatalf("handoff did not send exactly once: %s, %v", got, err)
			}
			next, _ := newQuietServiceOn(t, d)
			if err := next.inboxReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			if mode == "omitted" {
				next.inbox.mu.Lock()
				saved := next.inbox.items[it.ID]
				unlinked := saved != nil && saved.Agent == nil
				next.inbox.mu.Unlock()
				if !unlinked {
					t.Fatal("omitting inbox_item changed the saved Inbox assignment")
				}
			} else {
				// The explicit backlink belongs to the origin. The chain points to the receiver.
				origin.Agents = receiving.Agents
				assertExistingInboxLink(t, next, it.ID, origin)
			}
			if current, ok := s.openTodoOfAgent("box", session); !ok || current.ID != receiving.ID {
				t.Fatal("handoff changed the receiver's owning task", current.ID)
			}
		})
	}
}

func TestConfirmedTaskHandoffsWakeOnlyMatchingOrigin(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(ctx, newMigratedDatabase(t, db.AllSets), db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, f, a, b, receiving := handoffTestTodos(t, d)
	head := strings.Repeat("a", 40)
	ra, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, head)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := sendHandoffTest(t, s, f, "conv1", b.ID, receiving.ID, head)
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(61 * time.Minute)
	s.superviseWatch(ctx, later)
	for _, id := range []string{a.ID, b.ID} {
		current := todoNow(s, id)
		view := projection.View(projectionFacts(listedTodo{Todo: current}, nil, time.UTC), later)
		if p := current.Supervision.Parked; p == nil || !strings.Contains(p.Reason, receiving.Title) ||
			!strings.Contains(p.Reason, handoffCheck) || !p.Until.After(later) || view.Section != assistantwire.TodoListSectionWaiting {
			t.Fatalf("%s after 61 minutes: park=%+v view=%+v", current.Title, p, view)
		}
		if sigs := pendingKinds(s, id); len(sigs) != 0 {
			t.Fatalf("%s stopped while waiting: %v", current.Title, sigs)
		}
		t.Logf("%s after 61 minutes: %s, %s", current.Title, view.Section, current.Supervision.Parked.Reason)
	}
	if ra["id"] == "" || rb["id"] == "" || ra["id"] == rb["id"] {
		t.Fatalf("the two sends need different receipt IDs: %v, %v", ra, rb)
	}
	for _, bad := range []struct {
		key, value, session string
	}{
		{"head", strings.Repeat("b", 40), "receiver"},
		{"check", "Different check", "receiver"},
		{"id", "unknown", "receiver"},
		{"todo_id", b.ID, "receiver"},
		{"head", head, "origin-b"},
	} {
		copyReceipt := make(map[string]string, len(ra))
		for key, value := range ra {
			copyReceipt[key] = value
		}
		copyReceipt[bad.key] = bad.value
		if got, err := reportHandoffTest(s, bad.session, "done", copyReceipt); err == nil {
			t.Fatalf("unmatched receipt accepted: %s (%s)", bad.key, got)
		}
	}
	if s0, s1 := todoNow(s, a.ID).Supervision.State, todoNow(s, b.ID).Supervision.State; s0 != reviewParked || s1 != reviewParked {
		t.Fatalf("a rejected receipt moved a wait: A=%s B=%s", s0, s1)
	}
	if _, err := reportHandoffTest(s, "receiver", "done", ra); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.digestEntry(ctx, superviseSignal{Todo: a.ID, Kind: sigIdle}, ""); ok {
		t.Fatal("a stale stopped signal survived a matching handoff result")
	}
	settleService(t, s)
	if s0, s1, sc := todoNow(s, a.ID).Supervision.State, todoNow(s, b.ID).Supervision.State, todoNow(s, receiving.ID).Supervision.State; s0 != reviewPending || s1 != reviewParked || sc == reviewPending {
		t.Fatalf("one matching receipt: A=%s B=%s receiver=%s", s0, s1, sc)
	}
	messageCount := 0
	for _, notice := range f.notices() {
		if strings.Contains(notice, "Handoff result received") {
			messageCount++
			if !strings.Contains(notice, a.ID) || strings.Contains(notice, b.ID) || !strings.Contains(notice, "4 passed") {
				t.Fatalf("supervision did not carry only A's evidence:\n%s", notice)
			}
		}
	}
	if messageCount != 1 {
		t.Fatalf("got %d result messages, want one", messageCount)
	}
	// Reload the actual ledger into a new Service on the same Postgres fixture.
	f.mu.Lock()
	f.settings = nil
	f.mu.Unlock()
	restarted := newServiceOn(t, f, d)
	shortSupervise(t, restarted)
	before := len(f.notices())
	got, err := reportHandoffTest(restarted, "receiver", "done", ra)
	if err != nil || !strings.Contains(got, "already recorded") {
		t.Fatalf("duplicate after restart: %q, %v", got, err)
	}
	restarted.superviseWatch(ctx, time.Now())
	if sigs := pendingKinds(restarted, a.ID); len(sigs) != 0 || len(f.notices()) != before {
		t.Fatalf("the result woke review again after restart: %v", sigs)
	}
	if st := todoNow(restarted, b.ID).Supervision.State; st != reviewParked {
		t.Fatalf("waiting sibling after restart = %s", st)
	}
	t.Log("one matching receipt: A=pending_review B=parked; duplicate after Postgres reload: 0 new wakes")
}

func TestTaskHandoffRequiresDeliveryConfirmation(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
	}{
		{"no acknowledgement", nil},
		{"unknown delivery", context.DeadlineExceeded},
		{"node refused", errors.New("Agent cannot take the message")},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, a, _, receiving := handoffTestTodos(t, nil)
			f.mu.Lock()
			f.nodes[DeviceSessionSend] = func(_ string, _ json.RawMessage) (any, error) {
				return map[string]bool{"queued": true}, c.err
			}
			f.mu.Unlock()
			if _, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, strings.Repeat("a", 40)); err == nil {
				t.Fatal("unconfirmed send should fail")
			}
			got := todoNow(s, a.ID).Supervision
			if got.State != a.Supervision.State || !got.UpdatedAt.Equal(a.Supervision.UpdatedAt) {
				t.Fatalf("unconfirmed send changed origin: %+v", got)
			}
		})
	}
}

func TestTaskHandoffEscalatesMissedResults(t *testing.T) {
	for _, tc := range []struct{ action, message string }{
		{"expiry", "Handoff deadline passed without a matching result"},
		{"failure", "Handoff failed"},
		{"cancel", "Receiving to-do cancelled or removed"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			s, f, a, _, receiving := handoffTestTodos(t, nil)
			r, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, strings.Repeat("a", 40))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch tc.action {
			case "failure":
				if _, err = reportHandoffTest(s, "receiver", "blocked", r); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				if _, err = reviewErr(s, receiving.ID, map[string]any{"verdict": "cancelled", "note": "Release cancelled"}); err != nil {
					t.Fatal(err)
				}
				s.superviseWatch(ctx, time.Now())
			case "expiry":
				s.superviseWatch(ctx, time.Now().Add(3*time.Hour))
			}
			settleService(t, s)
			if cur := todoNow(s, a.ID); cur.Supervision.State != reviewPending || cur.DoneAt != nil {
				t.Fatalf("expected an open to-do returned to review: %+v", cur.Supervision)
			}
			if !strings.Contains(strings.Join(f.notices(), "\n"), tc.message) {
				t.Fatalf("expected %q in supervision: %v", tc.message, f.notices())
			}
		})
	}
}

func TestTaskHandoffRespectsNewWorkAndOwnerDecision(t *testing.T) {
	s, f, a, _, receiving := handoffTestTodos(t, nil)
	head := strings.Repeat("a", 40)
	if _, err := sendHandoffTest(t, s, f, "side-chat", a.ID, receiving.ID, head); err == nil || len(sentToAgent(f)) != 0 {
		t.Fatal("an unrelated conversation may not send a handoff for the originating to-do")
	}
	f.mu.Lock()
	f.beforeSend = func() { s.taskWorking(context.Background(), "box", "origin-a", startRow{}) }
	f.mu.Unlock()
	if _, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, head); err == nil || todoNow(s, a.ID).Supervision.State == reviewParked {
		t.Fatal("a node acknowledgement parked a work round that had already changed")
	}
	first, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, head)
	if err != nil {
		t.Fatal(err)
	}
	s.taskWorking(context.Background(), "box", "origin-a", startRow{})
	second, err := sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reportHandoffTest(s, "receiver", "done", first); err == nil {
		t.Fatal("the prior work round's receipt was accepted")
	}
	_, _, err = s.updateSupervision(context.Background(), a.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reportHandoffTest(s, "receiver", "done", second); err == nil {
		t.Fatal("a receipt took over an owner decision")
	}
	if _, err = sendHandoffTest(t, s, f, "conv1", a.ID, receiving.ID, head); err == nil {
		t.Fatal("a new wait took over an owner decision")
	}
}
