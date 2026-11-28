package hub

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A to-do's pushes to the owner are keyed by the to-do and their reason (push_todo_dedup.go): one to-do rang four
// times in an hour while its key carried the round.

// todoDecision is the assistant's decision push of to-do id, round asked, keyed by the to-do and reason.
func todoDecision(id string, asked time.Time, reason string) assistantwire.Notification {
	return assistantwire.Notification{ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyDecide, Subtitle: "Persona",
		Title: "结论：修登录崩溃", Body: "🤔 要你拍板：先灰度还是直接全量", Level: assistantwire.LevelP0,
		RelatedKey: assistantwire.TodoPushKey(id, reason),
		Todo:       &assistantwire.NotifyTodo{ID: id, AskedAt: asked, Recommend: "先灰度 10%"}}
}

// todoNeedsUser is the assistant's needs-user push of to-do id.
func todoNeedsUser(id string) assistantwire.Notification {
	return gradingNotification(assistantwire.LevelP0, assistantwire.TodoPushKey(id, assistantwire.TodoPushNeedsUser), "任务需要你")
}

// Four pushes of one to-do about one reason, each round asked anew, ring once; another reason of the same to-do rings
// once too, and so does the same reason of another to-do. A push held as a repeat says so.
func TestTodoPushRingsOncePerTodoAndReason(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for i := range 4 {
		if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Duration(i)*15*time.Minute), assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 1 {
		t.Fatalf("%d pushes for one to-do's decision asked four times, want 1", *attempts)
	}
	for i := range 4 {
		out, err := h.deliverPushFrom(assistantwire.AssistantID, todoNeedsUser("todo_1"))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && (out == nil || out.held != interceptReasonDedup || out.devices != 0) {
			t.Fatalf("repeat needs-user push %d = %+v, want held as %s", i, out, interceptReasonDedup)
		}
	}
	if *attempts != 2 {
		t.Fatalf("%d pushes after the needs-user ones, want 2 (one more reason)", *attempts)
	}
	if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecideOverdue)); err != nil {
		t.Fatal(err)
	}
	if err := h.deliverPush(todoDecision("todo_2", asked, assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	if *attempts != 4 {
		t.Fatalf("%d pushes, want 4: the overdue reason and another to-do ring once each", *attempts)
	}
	// A day later the same reason rings again (a daily reminder), once.
	moveGradingClock(h, h.push.now().Add(gradingDedupWindow))
	for range 2 {
		if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 5 {
		t.Fatalf("%d pushes, want 5 after the window", *attempts)
	}
	f.calls(t, 1)
}

// A push whose key carries the round (an older manager's, todo:<id>@<round>) is not held by this rule: each round
// rings as it always did.
func TestLegacyTodoKeysRingEveryRound(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	for i := range 3 {
		n := todoDecision("todo_1", asked.Add(time.Duration(i)*time.Minute), assistantwire.TodoPushDecide)
		n.RelatedKey = ""
		if err := h.deliverPush(n); err != nil {
			t.Fatal(err)
		}
	}
	if *attempts != 3 {
		t.Fatalf("%d pushes, want 3", *attempts)
	}
	f.calls(t, 1)
}

// Once the manager says the to-do no longer waits on the owner (EventFleetResolved naming its ask), the hub recalls
// the delivered push from withdraw-capable devices with todo-ask:<id>, and the next push about the to-do rings.
// The decision push carries todo_id and never interaction_id (older apps ignore todo_id and never sweep it as stale).
func TestTodoPushOverRingsAgain(t *testing.T) {
	h, f, open := withdrawHub(t, featureWithdraw)
	asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	first := f.wait(t, 1)[0]
	p := open(first)
	if p["interaction_id"] != nil {
		t.Fatalf("decision push payload %v carries an interaction_id", p)
	}
	if p["todo_id"] != "todo_1" {
		t.Fatalf("decision push payload %v missing todo_id=todo_1", p)
	}
	if p["card_id"] != assistantwire.TodoAskCardID("todo_1") {
		t.Fatalf("decision push payload %v missing card_id=%s", p, assistantwire.TodoAskCardID("todo_1"))
	}
	if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Minute), assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	reportResolved(t, h, assistantwire.TodoAskCardID("todo_1"), "")
	withdrawn := f.wait(t, 2)[1]
	if withdrawn.PushType != "background" {
		t.Fatalf("expected background withdrawal push for todo-ask:todo_1, got %+v", withdrawn)
	}
	if ids, _ := open(withdrawn)["resolved_interaction_ids"].([]any); len(ids) != 1 || ids[0] != assistantwire.TodoAskCardID("todo_1") {
		t.Fatalf("withdrawal payload %v, want resolved_interaction_ids=[%s]", open(withdrawn), assistantwire.TodoAskCardID("todo_1"))
	}
	if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Hour), assistantwire.TodoPushDecide)); err != nil {
		t.Fatal(err)
	}
	got := f.wait(t, 3)
	if got[2].PushType != "alert" {
		t.Fatalf("after the to-do was over: %+v, want the next decision's alert (the repeat before it was held)", got[2])
	}
}

// A to-do needs-user push carries todo_id (never interaction_id) and is recalled on EventFleetResolved(todo-ask:<id>)
// for withdraw-capable devices; an ordinary NotifyPush carries neither, and an older app without featureWithdraw gets
// the alert and no withdrawal push.
func TestTodoNeedsUserPushCarriesTodoIDAndWithdraws(t *testing.T) {
	t.Run("withdraw-capable device", func(t *testing.T) {
		h, f, open := withdrawHub(t, featureWithdraw)
		if _, err := h.deliverPushFrom(assistantwire.AssistantID, todoNeedsUser("todo_1")); err != nil {
			t.Fatal(err)
		}
		alert := f.wait(t, 1)[0]
		p := open(alert)
		if p["interaction_id"] != nil {
			t.Fatalf("needs-user push payload %v carries an interaction_id", p)
		}
		if p["todo_id"] != "todo_1" || p["card_id"] != assistantwire.TodoAskCardID("todo_1") {
			t.Fatalf("needs-user push payload %v missing todo_id/card_id for todo_1", p)
		}
		// An ordinary notice with a non-TodoPushKey related key carries none of todo_id, card_id, or interaction_id.
		plain := gradingNotification(assistantwire.LevelP0, "todo:todo_2", "普通提醒")
		if _, err := h.deliverPushFrom(assistantwire.AssistantID, plain); err != nil {
			t.Fatal(err)
		}
		plainPayload := open(f.wait(t, 2)[1])
		if plainPayload["todo_id"] != nil || plainPayload["card_id"] != nil || plainPayload["interaction_id"] != nil {
			t.Fatalf("plain notice payload %v should not carry todo_id, card_id, or interaction_id", plainPayload)
		}
		reportResolved(t, h, assistantwire.TodoAskCardID("todo_1"), "")
		bg := f.wait(t, 3)[2]
		if bg.PushType != "background" {
			t.Fatalf("expected background withdrawal, got %+v", bg)
		}
		if ids, _ := open(bg)["resolved_interaction_ids"].([]any); len(ids) != 1 || ids[0] != assistantwire.TodoAskCardID("todo_1") {
			t.Fatalf("withdrawal payload %v", open(bg))
		}
	})
	t.Run("older app without withdraw feature", func(t *testing.T) {
		h, f, open := withdrawHub(t)
		if _, err := h.deliverPushFrom(assistantwire.AssistantID, todoNeedsUser("todo_1")); err != nil {
			t.Fatal(err)
		}
		p := open(f.wait(t, 1)[0])
		if p["todo_id"] != "todo_1" || p["interaction_id"] != nil {
			t.Fatalf("payload %v, want todo_id=todo_1 without interaction_id", p)
		}
		reportResolved(t, h, assistantwire.TodoAskCardID("todo_1"), "")
		h.push.withdrawals.mu.Lock()
		q := h.push.withdrawals.queue["phone"]
		h.push.withdrawals.mu.Unlock()
		if q != nil && len(q.cards) > 0 {
			t.Fatalf("older app queued withdrawal ids: %v", q.cards)
		}
		h.push.flushWithdraw("phone")
		h.push.sends.Wait()
		if bg := backgroundPushes(f); len(bg) != 0 {
			t.Fatalf("older app got %d background pushes", len(bg))
		}
	})
}

// A to-do decision or needs-user push stays eligible for withdrawal past the 30-minute card TTL, and a fresh round
// pushed before the previous round's withdrawal flushes cancels that stale withdrawal so it cannot wipe the new alert.
func TestTodoPushWithdrawTTLAndNewRoundCancelsQueued(t *testing.T) {
	t.Run("remembered past 30-minute card TTL", func(t *testing.T) {
		p := newPusher(t.TempDir(), nil, t.Logf)
		now := time.Now()
		p.now = func() time.Time { return now }
		todoCard := assistantwire.TodoAskCardID("todo_1")
		p.ownerCardPushedTo(todoCard, "phone")
		p.cardPushedTo("ix_old", "phone")
		now = now.Add(2 * time.Hour)
		p.cardPushedTo("ix_fresh", "ipad") // triggers prune
		p.withdrawals.gather = time.Hour
		p.withdraw("ix_old")
		p.withdraw(todoCard)
		p.withdrawals.mu.Lock()
		defer p.withdrawals.mu.Unlock()
		if q := p.withdrawals.queue["phone"]; q == nil || len(q.cards) != 1 || q.cards[0].ID != todoCard {
			t.Fatalf("phone queue after 2h = %+v, want [%s]", q, todoCard)
		}
		for _, q := range p.withdrawals.queue {
			if q.timer != nil {
				q.timer.Stop()
			}
		}
	})
	t.Run("new round cancels queued withdrawal", func(t *testing.T) {
		h, f, _ := withdrawHub(t, featureWithdraw)
		h.push.withdrawals.gather = time.Hour // deterministic: keep withdrawal queued until explicitly flushed
		t.Cleanup(func() {
			h.push.withdrawals.mu.Lock()
			for _, q := range h.push.withdrawals.queue {
				if q.timer != nil {
					q.timer.Stop()
				}
			}
			h.push.withdrawals.mu.Unlock()
		})
		asked := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
		if err := h.deliverPush(todoDecision("todo_1", asked, assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
		f.wait(t, 1)
		todoCard := assistantwire.TodoAskCardID("todo_1")
		// Supersede round 1: withdrawal is queued for phone.
		reportResolved(t, h, todoCard, "")
		h.push.withdrawals.mu.Lock()
		queuedBefore := len(h.push.withdrawals.queue["phone"].cards)
		h.push.withdrawals.mu.Unlock()
		if queuedBefore != 1 {
			t.Fatalf("expected 1 queued withdrawal before round 2, got %d", queuedBefore)
		}
		// Immediately push round 2 before the gather timer fires: cancels the stale queued withdrawal.
		if err := h.deliverPush(todoDecision("todo_1", asked.Add(time.Minute), assistantwire.TodoPushDecide)); err != nil {
			t.Fatal(err)
		}
		f.wait(t, 2)
		h.push.withdrawals.mu.Lock()
		queuedAfter := len(h.push.withdrawals.queue["phone"].cards)
		if q := h.push.withdrawals.queue["phone"]; q != nil && q.timer != nil {
			q.timer.Stop()
		}
		h.push.withdrawals.mu.Unlock()
		if queuedAfter != 0 {
			t.Fatalf("expected round 2 to cancel queued withdrawal, still have %d", queuedAfter)
		}
		h.push.flushWithdraw("phone")
		h.push.sends.Wait()
		if bg := backgroundPushes(f); len(bg) != 0 {
			t.Fatalf("delayed withdrawal went out after round 2 was pushed: %+v", bg)
		}
	})
}

// The to-do a related key names, in every form a client follows: the legacy round, the reason, the bare to-do.
func TestRelatedTodoKeysStillNameTheirTodo(t *testing.T) {
	for _, key := range []string{"todo:todo_1", "todo:todo_1@2026-11-14T10:00:00Z", "todo:todo_1:needs_user", " todo:todo_1:decide "} {
		if got := relatedTodoID(key, false); got != "todo_1" {
			t.Errorf("relatedTodoID(%q) = %q", key, got)
		}
		if got := relatedDestination(key); got != "todo:todo_1" {
			t.Errorf("relatedDestination(%q) = %q", key, got)
		}
	}
	for _, key := range []string{"todo:", "todo::needs_user", "ask:todo_1", "reply:s1@1"} {
		if got := relatedTodoID(key, false); got != "" {
			t.Errorf("relatedTodoID(%q) = %q, want none", key, got)
		}
	}
}

// Structured concern:<id>:<state> keys deduplicate within 12h even for P0 pushes, release on state change or
// EventFleetResolved, and apply identically when NotifyInput carries a structured todo: or concern: key.
func TestConcernStateDedupAcrossP0(t *testing.T) {
	h, f := gradingHub(t)
	attempts := gradingAttempts(h)

	concernPush := func(id, state string) assistantwire.Notification {
		return gradingNotification(assistantwire.LevelP0, "concern:"+id+":"+state, "Concern "+id+" "+state)
	}

	// Same concern and state pushed twice within 12h (even P0): rings once, second is held as ln_dedup_12h.
	out1, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked"))
	if err != nil || heldOf(out1) != "" || *attempts != 1 {
		t.Fatalf("first concern P0: out=%+v err=%v attempts=%d", out1, err, *attempts)
	}
	out2, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked"))
	if err != nil || out2 == nil || out2.held != interceptReasonDedup || *attempts != 1 {
		t.Fatalf("repeat concern P0: out=%+v err=%v attempts=%d, want held %s", out2, err, *attempts, interceptReasonDedup)
	}

	// Different concern ID is independent and rings.
	outOther, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c2", "blocked"))
	if err != nil || heldOf(outOther) != "" || *attempts != 2 {
		t.Fatalf("other concern P0: out=%+v err=%v attempts=%d", outOther, err, *attempts)
	}

	// State change on c1 rings and releases the previous state so transitioning back rings again.
	outReady, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "ready"))
	if err != nil || heldOf(outReady) != "" || *attempts != 3 {
		t.Fatalf("state-changed concern P0: out=%+v err=%v attempts=%d", outReady, err, *attempts)
	}
	outReadyDup, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "ready"))
	if err != nil || outReadyDup == nil || outReadyDup.held != interceptReasonDedup || *attempts != 3 {
		t.Fatalf("repeat new-state concern P0: out=%+v err=%v attempts=%d", outReadyDup, err, *attempts)
	}
	outBackToBlocked, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked"))
	if err != nil || heldOf(outBackToBlocked) != "" || *attempts != 4 {
		t.Fatalf("transition back to prior state: out=%+v err=%v attempts=%d", outBackToBlocked, err, *attempts)
	}

	// EventFleetResolved (both todo-ask:<id> and concern:<id>) releases the concern dedup lock.
	reportResolved(t, h, assistantwire.TodoAskCardID("c1"), "")
	outAfterTodoResolve, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked"))
	if err != nil || heldOf(outAfterTodoResolve) != "" || *attempts != 5 {
		t.Fatalf("after todo-ask resolve: out=%+v err=%v attempts=%d", outAfterTodoResolve, err, *attempts)
	}
	reportResolved(t, h, "concern:c1", "")
	outAfterConcernResolve, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked"))
	if err != nil || heldOf(outAfterConcernResolve) != "" || *attempts != 6 {
		t.Fatalf("after concern resolve: out=%+v err=%v attempts=%d", outAfterConcernResolve, err, *attempts)
	}

	// NotifyInput with structured concern: and todo: keys also claims dedup across P0 cards and releases on card resolve.
	addCard := func(id string) proto.Interaction {
		ix := proto.Interaction{ID: id, MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "approve", Manager: assistantwire.AssistantID}
		h.reported.mu.Lock()
		if h.reported.interactions == nil {
			h.reported.interactions = map[string]proto.Interaction{}
		}
		h.reported.interactions[ix.ID] = ix
		h.reported.mu.Unlock()
		return ix
	}
	ix1 := addCard("ix_concern_1")
	ix2 := addCard("ix_concern_2")
	outIx1, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID: "ntf_ix_c1", Kind: assistantwire.NotifyInput, Interaction: &ix1, Level: assistantwire.LevelP0, RelatedKey: "concern:c3:waiting",
	})
	if err != nil || outIx1 != nil || *attempts != 7 {
		t.Fatalf("first concern NotifyInput: out=%+v err=%v attempts=%d", outIx1, err, *attempts)
	}
	outIx2, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID: "ntf_ix_c2", Kind: assistantwire.NotifyInput, Interaction: &ix2, Level: assistantwire.LevelP0, RelatedKey: "concern:c3:waiting",
	})
	if err != nil || outIx2 == nil || outIx2.held != interceptReasonDedup || *attempts != 7 {
		t.Fatalf("duplicate concern NotifyInput: out=%+v err=%v attempts=%d, want held %s", outIx2, err, *attempts, interceptReasonDedup)
	}
	reportResolved(t, h, ix1.ID, "")
	outIx2After, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID: "ntf_ix_c2_after", Kind: assistantwire.NotifyInput, Interaction: &ix2, Level: assistantwire.LevelP0, RelatedKey: "concern:c3:waiting",
	})
	if err != nil || outIx2After != nil || *attempts != 8 {
		t.Fatalf("concern NotifyInput after card resolve: out=%+v err=%v attempts=%d", outIx2After, err, *attempts)
	}

	ixT1 := addCard("ix_todo_1")
	ixT2 := addCard("ix_todo_2")
	todoKey := assistantwire.TodoPushKey("t9", assistantwire.TodoPushNeedsUser)
	if out, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID: "ntf_ix_t1", Kind: assistantwire.NotifyInput, Interaction: &ixT1, Level: assistantwire.LevelP0, RelatedKey: todoKey,
	}); err != nil || out != nil || *attempts != 9 {
		t.Fatalf("first todo NotifyInput: out=%+v err=%v attempts=%d", out, err, *attempts)
	}
	if out, err := h.deliverPushFrom(assistantwire.AssistantID, assistantwire.Notification{
		ID: "ntf_ix_t2", Kind: assistantwire.NotifyInput, Interaction: &ixT2, Level: assistantwire.LevelP0, RelatedKey: todoKey,
	}); err != nil || out == nil || out.held != interceptReasonDedup || *attempts != 9 {
		t.Fatalf("duplicate todo NotifyInput: out=%+v err=%v attempts=%d", out, err, *attempts)
	}

	// After the 12h dedup window expires, the same concern state rings again once.
	moveGradingClock(h, h.push.now().Add(gradingDedupWindow))
	if out, err := h.deliverPushFrom(assistantwire.AssistantID, concernPush("c1", "blocked")); err != nil || heldOf(out) != "" || *attempts != 10 {
		t.Fatalf("after 12h window: out=%+v err=%v attempts=%d", out, err, *attempts)
	}

	// Malformed concern keys are not treated as structured dedup keys.
	for _, bad := range []string{"concern:", "concern:c1", "concern:c1:", "concern::blocked", "concern:c1@round"} {
		if _, _, _, ok := structuredPushKeyParts(bad); ok {
			t.Errorf("structuredPushKeyParts(%q) = true, want false", bad)
		}
	}
	f.calls(t, 10)
}
