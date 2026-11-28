package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// setQuietForTest makes quietModeOn answer on without the store; nil goes back to reading it.
func (s *Service) setQuietForTest(on *bool) {
	s.pushQuiet.mu.Lock()
	s.pushQuiet.force = on
	s.pushQuiet.mu.Unlock()
}

func pushQuietFor(t *testing.T, s *Service, on bool) {
	t.Helper()
	s.setQuietForTest(&on)
	t.Cleanup(func() { s.setQuietForTest(nil) })
}

// cardPushes are the pushes of card id with the level each carried.
func (e *routeEnv) cardPushes(id string) (levels []string) {
	for _, p := range e.f.pushed() {
		if p.Interaction != nil && p.Interaction.ID == id {
			levels = append(levels, p.Level)
		}
	}
	return levels
}

// heldNotices are the P1 notices the assistant gave the hub for task todo's held cards.
func (e *routeEnv) heldNotices(todo string) []Notification {
	var out []Notification
	for _, p := range e.f.pushed() {
		if p.Interaction == nil && p.RelatedKey == "ask:"+todo {
			out = append(out, p)
		}
	}
	return out
}

// feishuCards turns the Lark bot on with every agent card wanted, and counts the cards it is given to post.
func (e *routeEnv) feishuCards() func() int {
	var mu sync.Mutex
	n := 0
	e.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: FeishuFeatures{Main: true, Cards: true, AllCards: true}}, nil
	})
	e.f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return map[string]any{"message_id": "om_x"}, nil
	})
	e.f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	return func() int { mu.Lock(); defer mu.Unlock(); return n }
}

func (e *routeEnv) announced(id string) bool {
	e.s.chans.mu.Lock()
	defer e.s.chans.mu.Unlock()
	return e.s.chans.announced[id]
}

// With the switch on, a tool permission card of a supervised task stays off the owner's phone: it climbs leader → main
// conversation, then goes back to the leader with internal reminders and no owner notification.
// It stays with the leader, which is reminded again at each hold. Each step is a subtest of its own,
// so a failure names the step.
func TestQuietPermissionCardStaysWithItsSupervisor(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold := askHoldFor
	askHoldFor = 2 * time.Minute
	t.Cleanup(func() { askHoldFor = oldHold })
	pushQuietFor(t, e.s, true)
	step := func(name string, f func(t *testing.T)) bool {
		return t.Run(name, func(st *testing.T) {
			parent := e.t
			e.t = st
			defer func() { e.t = parent }()
			f(st)
		})
	}
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")

	if !step("up_to_main", func(t *testing.T) {
		e.s.askTick(time.Now().Add(2 * askHoldFor)) // leader → main conversation, as before
		m := e.noticeWith("conv1", "interaction_id=ix1")
		if !strings.Contains(m, "signal=ask") || !strings.Contains(m, "low_distraction=true") {
			t.Fatalf("the main conversation is not told the card stays with the supervisors:\n%s", m)
		}
	}) {
		return
	}
	if !step("back_to_leader", func(t *testing.T) {
		e.s.askTick(time.Now().Add(4 * askHoldFor)) // past the last rung: back to the leader, not the owner
		r := e.noticeWith("lead1", "再交给你处理")
		if !strings.Contains(r, "interaction_id=ix1") {
			t.Fatalf("the leader's reminder:\n%s", r)
		}
	}) {
		return
	}
	if !step("internal_reminder", func(t *testing.T) {
		waitFor(t, "the internal reminder event", func() bool { return len(workEventsOf(e.s, "ask_quiet_reminded")) > 0 })
		if len(e.heldNotices(e.todo.ID)) != 0 {
			t.Fatal("internal reminder was lost or constructed an owner notification")
		}
	}) {
		return
	}
	if !step("reminded_again", func(t *testing.T) {
		e.s.askTick(time.Now().Add(6 * askHoldFor)) // its hold with the leader runs out again: it stays there
		e.s.askTick(time.Now().Add(8 * askHoldFor)) // and again, past the old 15-minute deadline
		e.settle()
	}) {
		return
	}
	step("main_not_given_it_again", func(t *testing.T) {
		if got := e.f.noticesTo("conv1"); len(got) != 1 {
			t.Fatalf("the main conversation was given the card again after the reminder: %d messages:\n%s", len(got), strings.Join(got, "\n---\n"))
		}
	})
	step("no_card_push", func(t *testing.T) {
		if got := e.cardPushes("ix1"); len(got) != 0 {
			t.Fatalf("the owner was pushed the card: %v", got)
		}
	})
	step("no_owner_held_notice", func(t *testing.T) {
		if len(e.heldNotices(e.todo.ID)) != 0 {
			t.Fatalf("unexpected owner held notice: %+v", e.heldNotices(e.todo.ID))
		}
	})
	step("still_delegating", func(t *testing.T) {
		if !e.lastShown("ix1").Delegating {
			t.Fatal("the card is not shown as the supervisors'")
		}
	})
	step("not_announced", func(t *testing.T) {
		if e.announced("ix1") {
			t.Fatal("the card went to the channels")
		}
	})
	step("with_the_leader", func(t *testing.T) {
		if todo, ok := e.s.routedTo("ix1", "lead1", false); !ok || todo != e.todo.ID {
			t.Fatal("the card is not with the task's leader")
		}
	})
	// The supervisor may still escalate what is the owner's alone.
	step("escalated", func(t *testing.T) {
		if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "要推远端",
			"why_owner": "irreversible"}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the escalated push", func() bool { return len(e.cardPushes("ix1")) == 1 })
		if got := e.cardPushes("ix1"); got[0] != assistantwire.LevelP0 {
			t.Fatalf("escalated irreversible card pushed at %v", got)
		}
	})
}

// An owner-only card parked with its leader has no deadline that hands it to the owner: it stays there at 15 minutes
// and long after, even with its supervisor busy, without an owner notification.
func TestQuietOwnerOnlyCardHasNoDeadline(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold := askHoldFor
	askHoldFor = 2 * time.Minute
	t.Cleanup(func() { askHoldFor = oldHold })
	pushQuietFor(t, e.s, true)
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	start := time.Now()

	e.s.askTick(start.Add(2 * askHoldFor)) // leader → main conversation
	e.s.askTick(start.Add(4 * askHoldFor)) // main conversation → back to the leader, parked
	e.s.askTick(start.Add(6 * askHoldFor)) // the hold expires again: the leader is reminded again
	waitFor(t, "the internal reminder", func() bool { return len(workEventsOf(e.s, "ask_quiet_reminded")) > 0 })
	e.supervisorState(proto.StatusIdle, proto.StatusWorking)
	e.s.askTick(start.Add(15 * time.Minute))
	e.supervisorState(proto.StatusIdle, proto.StatusIdle)
	e.s.askTick(start.Add(time.Hour))
	e.s.askTick(start.Add(2 * time.Hour))
	e.settle()
	if got := e.cardPushes("ix1"); len(got) != 0 {
		t.Fatalf("the owner was pushed on a timeout: %v", got)
	}
	if todo, ok := e.s.routedTo("ix1", "lead1", false); !ok || todo != e.todo.ID {
		t.Fatal("the card is not parked with its leader")
	}
	if !e.lastShown("ix1").Delegating {
		t.Fatal("the card is no longer shown as the supervisor's")
	}
	if got := len(e.heldNotices(e.todo.ID)); got != 0 {
		t.Fatalf("the held notice was sent %d times", got)
	}
}

// Ordinary questions have no deadline either: low-distraction mode keeps one with its supervisor after the same
// elapsed time, with internal reminders and no owner push.
func TestQuietQuestionHasNoAbsoluteDeadline(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold := askHoldFor
	askHoldFor = 2 * time.Minute
	t.Cleanup(func() { askHoldFor = oldHold })
	pushQuietFor(t, e.s, true)
	e.raise(askQuestion("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	start := time.Now()

	e.s.askTick(start.Add(2 * askHoldFor))
	e.s.askTick(start.Add(4 * askHoldFor))
	e.s.askTick(start.Add(6 * askHoldFor))
	e.s.askTick(start.Add(30 * time.Minute))
	e.settle()
	if got := e.cardPushes("ix1"); len(got) != 0 {
		t.Fatalf("the ordinary question reached the owner on a timeout: %v", got)
	}
	if got := len(e.heldNotices(e.todo.ID)); got != 0 {
		t.Fatalf("the question constructed %d owner notifications, want none", got)
	}
	if !e.lastShown("ix1").Delegating {
		t.Fatal("the ordinary question left its supervisors")
	}
	if todo, ok := e.s.routedTo("ix1", "lead1", false); !ok || todo != e.todo.ID {
		t.Fatal("the ordinary question is not parked with its leader")
	}
}

// A supervisor that needs the owner hands an owner-only card up a rung at once, but the last rung needing the owner
// does not hand it to them: it stays with the main conversation, and no held notice goes out for it.
func TestQuietOwnerOnlyNeedsYouReleaseStopsAtTheLastRung(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	pushQuietFor(t, e.s, true)
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	start := time.Now()
	e.supervisorState(proto.StatusIdle, proto.StatusBlocked)
	e.s.askTick(start)
	e.noticeWith("conv1", "interaction_id=ix1")
	if got := e.cardPushes("ix1"); len(got) != 0 {
		t.Fatalf("the owner was pushed before the main conversation needed them: %v", got)
	}

	e.supervisorState(proto.StatusBlocked, proto.StatusIdle)
	e.s.askTick(start.Add(superviseTick))
	e.settle()
	if got := e.cardPushes("ix1"); len(got) != 0 {
		t.Fatalf("the owner was pushed because the main conversation needs them: %v", got)
	}
	if _, ok := e.s.routedTo("ix1", "conv1", false); !ok {
		t.Fatal("the card left the main conversation")
	}
	if got := len(e.heldNotices(e.todo.ID)); got != 0 {
		t.Fatalf("a needs_you release became %d held notices", got)
	}
}

// A parked card stays with its supervisor when the switch goes off: no timeout hands it to the owner either way.
func TestParkedCardStaysWhenTheSwitchIsOff(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold := askHoldFor
	askHoldFor = time.Minute
	t.Cleanup(func() { askHoldFor = oldHold })
	on := true
	e.s.setQuietForTest(&on)
	t.Cleanup(func() { e.s.setQuietForTest(nil) })
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	for i := 2; i <= 8; i += 2 {
		e.s.askTick(time.Now().Add(time.Duration(i) * askHoldFor))
	}
	e.settle()
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("pushed %d times while the switch was on", n)
	}
	off := false
	e.s.setQuietForTest(&off)
	e.s.askTick(time.Now().Add(12 * askHoldFor))
	e.s.askTick(time.Now().Add(14 * askHoldFor))
	e.settle()
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("pushed %d times once the switch was off", n)
	}
	if _, ok := e.s.routedTo("ix1", "lead1", false); !ok {
		t.Fatal("the card left its leader")
	}
}

// With the switch off a card nobody handles climbs leader → main conversation as before, then goes back to the
// leader, not to the owner; and nothing goes to the owner's held list either (a P1 would push with the switch off).
func TestQuietOffPermissionCardGoesBackToItsSupervisor(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	oldHold := askHoldFor
	askHoldFor = 2 * time.Minute
	t.Cleanup(func() { askHoldFor = oldHold })
	pushQuietFor(t, e.s, false)
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	e.s.askTick(time.Now().Add(2 * askHoldFor))
	m := e.noticeWith("conv1", "interaction_id=ix1")
	if !strings.Contains(m, "signal=ask") {
		t.Fatalf("the main conversation's message changed with the switch off:\n%s", m)
	}
	e.s.askTick(time.Now().Add(4 * askHoldFor))
	e.noticeWith("lead1", "再交给你处理")
	e.settle()
	if n := e.pushesOf("ix1"); n != 0 {
		t.Fatalf("the owner was pushed %d times on a timeout", n)
	}
	if len(e.heldNotices(e.todo.ID)) != 0 {
		t.Fatal("a held notice with the switch off")
	}
}

// ask_escalate pushes by why_owner: irreversible, visible_to_others, only_owner_knows or p0 push at P0; priority and
// acceptance are P1, which with the switch on go to the apps and the hub's held list only (no Feishu card, no
// channel), and with it off ring as before.
func TestEscalateLevelByWhyOwner(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name     string
		quiet    bool
		args     map[string]any
		level    string
		announce bool
		says     string
	}{
		{"quiet priority", true, map[string]any{"why_owner": "priority"}, assistantwire.LevelP1, false, "without a push"},
		{"quiet acceptance", true, map[string]any{"why_owner": "acceptance"}, assistantwire.LevelP1, false, "without a push"},
		{"quiet none", true, map[string]any{}, assistantwire.LevelP1, false, "without a push"},
		{"quiet irreversible", true, map[string]any{"why_owner": "irreversible"}, assistantwire.LevelP0, true, "asked now"},
		{"quiet visible", true, map[string]any{"why_owner": "visible_to_others"}, assistantwire.LevelP0, true, "asked now"},
		{"quiet only owner", true, map[string]any{"why_owner": "only_owner_knows"}, assistantwire.LevelP0, true, "asked now"},
		{"quiet p0", true, map[string]any{"why_owner": "priority", "p0": true}, assistantwire.LevelP0, true, "asked now"},
		{"off priority", false, map[string]any{"why_owner": "priority"}, assistantwire.LevelP1, true, "asked now"},
		{"off none", false, map[string]any{}, assistantwire.LevelP1, true, "asked now"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRouteEnv(t, nil, true)
			pushQuietFor(t, e.s, tc.quiet)
			cards := e.feishuCards()
			e.raise(askPermission("ix1", "worker-1"))
			e.noticeWith("lead1", "interaction_id=ix1")
			args := map[string]any{"todo_id": e.todo.ID, "reason": "要推到远端分支"}
			for k, v := range tc.args {
				args[k] = v
			}
			res, err := runJSON(t, e.s, "lead1", "ask_escalate", args)
			if err != nil || !strings.Contains(res, tc.says) {
				t.Fatalf("escalate: %s %v", res, err)
			}
			waitFor(t, "the push", func() bool { return len(e.cardPushes("ix1")) == 1 })
			e.settle()
			if got := e.cardPushes("ix1"); len(got) != 1 || got[0] != tc.level {
				t.Fatalf("pushes at %v, want one at %s", got, tc.level)
			}
			if tc.announce {
				waitFor(t, "the Feishu card", func() bool { return cards() == 1 })
			} else {
				waitFor(t, "the held card's announcement", func() bool { return e.announced("ix1") })
				e.settle()
				if n := cards(); n != 0 {
					t.Fatalf("a held escalation posted %d Feishu cards", n)
				}
				if !e.announced("ix1") {
					t.Fatal("a held card is not marked announced: the channels' catch-up would post it")
				}
			}
			if shown := e.lastShown("ix1"); shown.Delegating || shown.Delegate == nil {
				t.Fatalf("not on the owner's Needs you list: %+v", shown)
			}
		})
	}
}

func TestEscalateRejectsAnUnknownWhyOwner(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "x",
		"why_owner": "urgent"}); err == nil || !strings.Contains(err.Error(), "why_owner") {
		t.Fatalf("err %v", err)
	}
}

func TestEscalateLevel(t *testing.T) {
	for why, want := range map[string]string{"irreversible": "P0", "visible_to_others": "P0", "only_owner_knows": "P0",
		"priority": "P1", "acceptance": "P1", "": "P1"} {
		if got := escalateLevel(why, false); got != want {
			t.Errorf("%q: %s, want %s", why, got, want)
		}
		if got := escalateLevel(why, true); got != "P0" {
			t.Errorf("%q with p0: %s", why, got)
		}
	}
}

// The supervision message says what the switch changes: never to the user on a timeout, why_owner on escalations.
func TestSuperviseMessageUnderTheSwitch(t *testing.T) {
	on := prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Asked: true, AskMinutes: 5, PushGrading: true,
		Entries: []prompts.SuperviseEntry{{Kind: sigAsk, ID: "todo_1", Title: "修登录崩溃",
			Asks: []prompts.SuperviseAsk{{ID: "ix1", Kind: "permission", Session: "w1", Text: "Bash", Reminded: true}}}}})
	for _, want := range []string{"signal=ask", "Question routing: timeout_minutes=5", "low_distraction=true", "session_id=w1"} {
		if !strings.Contains(on, want) {
			t.Errorf("lacks %q:\n%s", want, on)
		}
	}
	off := prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Asked: true, AskMinutes: 5})
	if strings.Contains(off, "low_distraction=true") || !strings.Contains(off, "low_distraction=false") {
		t.Fatalf("the message changed with the switch off:\n%s", off)
	}
}

// With the switch on a supervised task's conclusion (task_review accepted) posts no card: the briefing and the board
// keep it.
func TestQuietConclusionPostsNoCard(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	pushQuietFor(t, s, true)
	waitsForReview(t, s, f, todo.ID, "查了，对方接口不对外开放，直连做不了。")
	waitFor(t, "the review state", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	before := len(posts())
	if before != 0 {
		t.Fatalf("progress cards with the switch on:\n%s", strings.Join(posts(), "\n---\n"))
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "cannot", "reason": "对方接口不对外开放",
		"link": "https://docs.example.com/api"})
	waitFor(t, "the accepted conclusion", func() bool { a := askOf(s, todo.ID); return a != nil && a.Conclusion && a.NoCard })
	if after := posts(); len(after) != 0 {
		t.Fatalf("cards with the switch on:\n%s", strings.Join(after, "\n---\n"))
	}
	if a := askOf(s, todo.ID); a == nil || !a.Conclusion || !a.NoCard || a.MessageID != "" {
		t.Fatalf("ask %+v", a)
	}
	for _, p := range f.pushed() {
		t.Fatalf("pushed %+v", p)
	}
}

// With the switch on a "Please verify" ask posts no card and keeps waiting on the board and in the briefing.
func TestQuietPleaseVerifyPostsNoCard(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	pushQuietFor(t, s, true)
	s.ownerVerifiesAll = true
	f.judge = `{"changed":"设置里多了开关","link":"https://hub.example.com/settings","how":"打开设置看开关"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the ask", func() bool { a := askOf(s, todo.ID); return a != nil && !a.AskedAt.IsZero() })
	waitFor(t, "the cardless ask", func() bool { a := askOf(s, todo.ID); return a != nil && a.NoCard && a.MessageID == "" })
	if n := countAcceptCards(posts()); n != 0 {
		t.Fatalf("%d 请验收 cards", n)
	}
	if a := askOf(s, todo.ID); !a.NoCard || a.MessageID != "" {
		t.Fatalf("ask %+v", a)
	}
	ctx := context.Background()
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 {
		t.Fatalf("%d waiting in the briefing, want 1", total)
	}
}

// A held escalation puts the card on the owner's Needs you list: the supervisor cannot escalate the same card again
// (it is the user's already), and the tool reply tells them the escape hatches — escalate with p0 or the right
// why_owner from the start, or notify_user with push_level P0 when waiting now would do real harm.
func TestHeldEscalationIsNotReEscalatable(t *testing.T) {
	e := newRouteEnv(t, nil, true)
	pushQuietFor(t, e.s, true)
	e.raise(askPermission("ix1", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix1")
	res, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "x",
		"why_owner": "priority"})
	if err != nil || !strings.Contains(res, "without a push") {
		t.Fatalf("first escalation: %s %v", res, err)
	}
	// notifyInteractionAt sends the held card's push on a goroutine of its own: count from there.
	waitFor(t, "the held card's push", func() bool { return len(e.cardPushes("ix1")) == 1 })
	for _, args := range []map[string]any{
		{"todo_id": e.todo.ID, "reason": "now urgent", "p0": true},
		{"todo_id": e.todo.ID, "reason": "now urgent", "why_owner": "irreversible"},
	} {
		if _, err := runJSON(t, e.s, "lead1", "ask_escalate", args); err == nil ||
			!strings.Contains(err.Error(), "no card waiting that the user has not been told of") {
			t.Fatalf("re-escalation %v: %v", args, err)
		}
	}
	// The rejected re-escalations above add no second push; settle the service before the final count.
	e.settle()
	// The held escalation's P1 push is handed to the hub on deliver()'s own goroutine, so wait for it to land
	// before reading the pushes.
	waitFor(t, "the held escalation's P1 push", func() bool { return len(e.cardPushes("ix1")) == 1 })
	if got := e.cardPushes("ix1"); len(got) != 1 || got[0] != assistantwire.LevelP1 {
		t.Fatalf("the rejected re-escalation pushed: %v", got)
	}
	if shown := e.lastShown("ix1"); shown.PushLevel != assistantwire.LevelP1 {
		t.Fatalf("held escalation interaction push_level = %q, want %q", shown.PushLevel, assistantwire.LevelP1)
	}
	e.s.fleet.mu.Lock()
	storedLevel := e.s.fleet.interactions["ix1"].ix.PushLevel
	e.s.fleet.mu.Unlock()
	if storedLevel != assistantwire.LevelP1 {
		t.Fatalf("stored pending interaction push_level = %q, want %q", storedLevel, assistantwire.LevelP1)
	}

	// An urgent P0 escalation stamps P0 on the Interaction as well.
	e.raise(askPermission("ix2", "worker-1"))
	e.noticeWith("lead1", "interaction_id=ix2")
	if _, err := runJSON(t, e.s, "lead1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "urgent", "p0": true}); err != nil {
		t.Fatalf("P0 escalation: %v", err)
	}
	waitFor(t, "P0 interaction shown", func() bool {
		ix, ok := e.shown("ix2")
		return ok && !ix.Delegating
	})
	if shown := e.lastShown("ix2"); shown.PushLevel != assistantwire.LevelP0 {
		t.Fatalf("P0 escalation interaction push_level = %q, want %q", shown.PushLevel, assistantwire.LevelP0)
	}
}

// With the switch off the same turn still posts its progress card (as before).
func TestQuietOffTaskReportStillPosts(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportService(t, true)
	pushQuietFor(t, s, false)
	startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了，https://github.com/o/r/pull/901 已合并", 0)
	waitFor(t, "the report", func() bool { return len(posts()) == 1 })
}

// With the switch on, a task's progress report posts nothing.
func TestQuietTaskReportPostsNothing(t *testing.T) {
	s, f, posts := reportService(t, true)
	pushQuietFor(t, s, true)
	startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了，https://github.com/o/r/pull/901 已合并", 0)
	waitReportQueue(t, s)
	if n := len(posts()); n != 0 {
		t.Fatalf("%d progress cards with the switch on", n)
	}
}

// Leader reports carry a level for the extensions only with the switch on: progress, reached and stuck P1, a decision P0.
func TestLeaderReportLevel(t *testing.T) {
	// (A plan waiting for Start is a decision report with Plan set: leaderReportsSync gives it P1, see
	// TestLeaderPlanWaitingIsADecision.)
	s, _ := newTaskService(t)
	ctx := context.Background()
	pushQuietFor(t, s, false)
	for _, k := range []string{assistantwire.LeaderProgress, assistantwire.LeaderReached, assistantwire.LeaderDecision} {
		if got := s.leaderReportLevel(ctx, k); got != "" {
			t.Errorf("off, %s: %q", k, got)
		}
	}
	pushQuietFor(t, s, true)
	for k, want := range map[string]string{assistantwire.LeaderProgress: "P1", assistantwire.LeaderReached: "P1",
		assistantwire.LeaderStuck: "P1", assistantwire.LeaderDecision: "P0"} {
		if got := s.leaderReportLevel(ctx, k); got != want {
			t.Errorf("on, %s: %q, want %s", k, got, want)
		}
	}
}

// A real owner-step plan keeps the existing P1 priority. Missing ordinary dispatch authority alone
// no longer produces a phone notification; it is covered by TestNonOwnerPlanWaitsDoNotPush.
func TestOwnerPlanWaitingForStartIsNotP0(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	f.hello.Assistant.MachineID = "box"
	setSettings(t, s, func(v *Settings) { v.Main, v.MachineID = "main1", "box" })
	if _, err := s.toolTaskPlan(ctx, "side1", planArgsWithStep(t, ownerStepMoney)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the push", func() bool { return len(f.pushed()) == 1 })
	if p := f.pushed()[0]; p.Level == assistantwire.LevelP0 || p.Kind != assistantwire.NotifyPush {
		t.Fatalf("plan push %+v", p)
	}
}

// The switch is the hub's hello: QuietPush on is on, an older hub that sends nothing is off.
func TestQuietModeFollowsTheHubsHello(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	if s.quietModeOn(ctx) {
		t.Fatal("on without the hub saying so")
	}
	f.mu.Lock()
	f.hello.QuietPush = true
	f.mu.Unlock()
	if !s.quietModeOn(ctx) {
		t.Fatal("off with the hub's switch on")
	}
}

// A reply reminder is keyed by the question it waits on: the same stop of the same agent is one key, a new question
// (a later stop) another, so the hub never takes an agent's second question for a repeat of its first.
func TestReplyReminderKeyedByQuestion(t *testing.T) {
	at := time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)
	a := replyRelatedKey(proto.Session{SessionID: "s1", UpdatedAt: at})
	if a != replyRelatedKey(proto.Session{SessionID: "s1", UpdatedAt: at}) {
		t.Fatal("one question, two keys")
	}
	if a == replyRelatedKey(proto.Session{SessionID: "s1", UpdatedAt: at.Add(time.Minute)}) {
		t.Fatal("two questions of one agent share a key")
	}
	if a == replyRelatedKey(proto.Session{SessionID: "s2", UpdatedAt: at}) {
		t.Fatal("two agents share a key")
	}
}
