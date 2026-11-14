package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The reviewer's checks of due follow-up looks (docs/assistant/inbox.md "Following up", DECISIONS B-10): what a pass
// does with a due look beside the queued work it shares its slots with, that a user-triggered wake stays uncapped, that
// a look that already ran is not counted twice, and the template the conversation reads.

// The due-look note, with and without who and a next step: the conversation reads all three parts.
func TestInboxCheckTemplateGolden(t *testing.T) {
	golden(t, "inbox_handling.check", renderNote(inboxNote{Name: prompts.NoteCheck, Who: "张三", Next: "等他给报价", Checks: 2}, "小明"))
	golden(t, "inbox_handling.check_plain", renderNote(inboxNote{Name: prompts.NoteCheck, Checks: 1}, "小明"))
}

// A due look and a queued start share one pass's slots: the look is admitted first and takes the only one, so the
// queued event waits for a later pass instead of starting beside it.
func TestInboxCheckTakesTheSlotBeforeQueuedStarts(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	queued := actionItem("queued", now)
	queued.Agent = &InboxAgent{State: AgentQueued, Task: "查 1013 是否合入", Of: queued.Last}
	h.inbox.mu.Lock()
	h.inbox.items[queued.ID] = queued
	h.inbox.mu.Unlock()

	h.handPass(context.Background())

	if f := followOf(h, it); f == nil || f.Checks != 1 || f.CheckAt != nil {
		t.Fatalf("the due look did not run in the pass: %+v", f)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("the due look is %q, want looking", state)
	}
	if queued.Agent.State != AgentQueued || queued.Agent.SessionID != "" || h.f.made != 0 {
		t.Fatalf("a queued event started beside the due look: agent %+v, %d conversations started", queued.Agent, h.f.made)
	}
	if got := len(h.f.sentToAgents()); got != 1 || !strings.HasPrefix(h.f.sentToAgents()[0], "s1: ") {
		t.Fatalf("the due look's note: %d sends %v", got, h.f.sentToAgents())
	}

	// As long as the woken conversation is looking, the queued event still waits.
	h.handPass(context.Background())
	if queued.Agent.State != AgentQueued || h.f.made != 0 {
		t.Fatalf("the queued event started while the woken conversation is looking: %+v, %d starts", queued.Agent, h.f.made)
	}
}

// A wake the user or the other side causes is not one of the capped admissions: new messages still reach a
// conversation that is already running, with a due look waiting for capacity beside it.
func TestInboxCheckDoesNotCapSourceWakes(t *testing.T) {
	h, due, now := followCheckHub(t, 0)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	busy := actionItem("busy", now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "busy", Of: busy.Last}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingWaiting, "", now)
	h.inbox.mu.Unlock()

	// New messages for that event wake its conversation while the only slot is taken by the other one. A handled
	// event takes its next content: the converse never triages the new messages first.
	h.inbox.mu.Lock()
	busy.Last, busy.Context, busy.UpdatedAt = "13", "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：报价好了", now
	h.inbox.mu.Unlock()
	if got := h.inbox.escalate(now.Add(-time.Hour), now); len(got) != 1 {
		t.Fatalf("new messages escalated %d events, want 1", len(got))
	}
	h.deliverPending(context.Background())

	if got := len(h.f.sentToAgents()); got != 1 || !strings.HasPrefix(h.f.sentToAgents()[0], "busy: ") {
		t.Fatalf("a source wake was capped: %d sends %v", got, h.f.sentToAgents())
	}
	if f := followOf(h, due); f == nil || f.Checks != 0 || f.CheckAt == nil {
		t.Fatalf("the due look ran without a free slot: %+v", f)
	}

	// The same pass then admits the due look: capacity is the woken conversation's, not the wake's.
	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true, Concurrency: 1}, now)
	if f := followOf(h, due); f == nil || f.Checks != 0 {
		t.Fatalf("the due look ran beside the woken conversation: %+v", f)
	}
	h.inbox.mu.Lock()
	h.inbox.setHandling(busy, HandlingWaiting, "", now.Add(time.Minute))
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true, Concurrency: 1}, now.Add(time.Minute))
	if f := followOf(h, due); f == nil || f.Checks != 1 {
		t.Fatalf("the due look was not admitted after the slot freed: %+v", f)
	}
}

// Looking again does not change the item: a check_at exactly now is due, one a second later is not, and an item with
// no follow at all is never due.
func TestInboxCheckBoundaryAndNoFollow(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	h.inbox.mu.Lock()
	at := now
	it.Follow.CheckAt = &at
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now)
	if f := followOf(h, it); f == nil || f.Checks != 1 {
		t.Fatalf("check_at exactly now is not due: %+v", f)
	}

	h2, it2, now2 := followCheckHub(t, 0)
	h2.inbox.mu.Lock()
	at2 := now2.Add(time.Second)
	it2.Follow.CheckAt = &at2
	h2.inbox.mu.Unlock()
	h2.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now2)
	if f := followOf(h2, it2); f == nil || f.Checks != 0 || f.CheckAt == nil {
		t.Fatalf("a look a second away ran: %+v", f)
	}

	h3, it3, now3 := followCheckHub(t, 0)
	h3.inbox.mu.Lock()
	it3.Follow = nil
	h3.inbox.mu.Unlock()
	before := followCheckItemState(h3, it3)
	h3.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now3)
	if after := followCheckItemState(h3, it3); after != before {
		t.Fatalf("an item without a follow changed:\nbefore %s\nafter  %s", before, after)
	}
}

// A look that already ran is not counted again: the next pass finds no check time and leaves the item alone, with one
// note waiting rather than two.
func TestInboxCheckRunsOncePerDueTime(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	ctx := context.Background()
	cfg := InboxAgentConfig{Enabled: true}
	h.checkInboxFollowUps(ctx, cfg, now)
	h.checkInboxFollowUps(ctx, cfg, now.Add(time.Second))

	if f := followOf(h, it); f == nil || f.Checks != 1 {
		t.Fatalf("checks after two passes over one due time: %+v", f)
	}
	h.inbox.mu.Lock()
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if pending != 1 {
		t.Fatalf("%d check notes queued for one due time", pending)
	}
}

// The restoration to waiting is only for a turn woken from waiting: a look that fails otherwise still fails, so an
// ordinary timeout keeps its failed state and its chain entry.
func TestInboxCheckSweepStillFailsATurnNotWokenFromWaiting(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	h.inbox.mu.Lock()
	it.Agent.Back = ""
	started := now.Add(-5 * time.Minute)
	it.Agent.StartedAt = &started
	h.inbox.setHandling(it, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking})

	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true, TimeoutMinutes: 1}, now)

	if state, _, _ := handlingNow(h, it); state != HandlingFailed {
		t.Fatalf("a turn that did not come from waiting is %q, want failed", state)
	}
	if last := lastChain(h, it); last.Kind != ChainFailed {
		t.Fatalf("last chain entry: %+v", last)
	}
}

// With the twin off and three looks already made, the matter goes back to the user without a wake: it keeps the
// finished count, loses its check time, and says it is due (the shared table's follow_up_ask outranks check_due, so
// the user reads "Checked 3 times with no news. Keep following?").
func TestInboxCheckTwinOffAfterThreeLooksKeepsTheCount(t *testing.T) {
	h, it, now := followCheckHub(t, inboxFollowUpAskChecks)
	h.inbox.mu.Lock()
	it.Tier = TierFYI
	h.inbox.mu.Unlock()
	model, calls := captureModel(t, `{"tier":"action"}`)
	h.in.setModel(model)

	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{}, now)

	f := followOf(h, it)
	if f == nil || !f.Due || f.CheckAt != nil || f.Checks != inboxFollowUpAskChecks {
		t.Fatalf("follow with the twin off after three looks: %+v", f)
	}
	h.inbox.mu.Lock()
	state, listed := it.Handling.State, it.listed()
	h.inbox.mu.Unlock()
	if state != HandlingDone || listed.Status != StatusAction {
		t.Fatalf("handling %q, status %q", state, listed.Status)
	}
	if listed.Now != NowFollowUpAsk && listed.Now != NowCheckDue {
		t.Fatalf("now = %q, want follow_up_ask or check_due", listed.Now)
	}
	h.inbox.mu.Lock()
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if pending != 0 || len(h.f.sentToAgents()) != 0 || len(calls()) != 0 {
		t.Fatalf("with the twin off: %d notes, %d sends, %d model calls", pending, len(h.f.sentToAgents()), len(calls()))
	}
}

// The count is the gate: three looked-again turns that each end by waiting again, and the fourth pass asks the user
// instead of waking a conversation. This catches an off-by-one in the comparison (>= 3, not > 3).
func TestInboxCheckThreeRoundsThenAsks(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	ctx := context.Background()
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1}
	for round := 1; round <= inboxFollowUpAskChecks; round++ {
		at := now.Add(time.Duration(round) * time.Minute)
		h.inbox.mu.Lock()
		it.Follow.CheckAt = &at
		h.inbox.mu.Unlock()
		h.checkInboxFollowUps(ctx, cfg, at)
		f := followOf(h, it)
		if f == nil || f.Checks != round {
			t.Fatalf("round %d: follow %+v", round, f)
		}
		if state, wake, _ := handlingNow(h, it); state != HandlingLooking || !wake {
			t.Fatalf("round %d: handling %q, wake %v", round, state, wake)
		}
		h.deliverPending(ctx)
		if _, err := suggestTool(t, h, `{"recap":"还是在等他报价"}`); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
			t.Fatalf("round %d: a recap-only turn left the item %q", round, state)
		}
	}
	at := now.Add(10 * time.Minute)
	h.inbox.mu.Lock()
	it.Follow.CheckAt = &at
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(ctx, cfg, at)

	if f := followOf(h, it); f == nil || f.Checks != inboxFollowUpAskChecks || f.CheckAt != nil || f.Due {
		t.Fatalf("after three looks: %+v", f)
	}
	if state, wake, _ := handlingNow(h, it); state != HandlingDone || wake {
		t.Fatalf("after three looks: handling %q, wake %v", state, wake)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if listed.Now != NowFollowUpAsk {
		t.Fatalf("now after three looks = %q, want %q", listed.Now, NowFollowUpAsk)
	}
	if pending != 0 {
		t.Fatalf("the ask queued %d notes", pending)
	}
}

// Two matters are due with one slot: exactly one is admitted per pass, the other stays due untouched and is taken by a
// later pass. The seat the admitted turn holds in between keeps the second one out.
func TestInboxCheckOneSlotAcrossTwoMatters(t *testing.T) {
	h, first, now := followCheckHub(t, 0)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	second := actionItem("second", now)
	second.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s2", Of: second.Last}
	at := now.Add(-time.Minute)
	h.inbox.mu.Lock()
	h.inbox.items[second.ID] = second
	second.Follow = &InboxFollow{Who: "李四", Next: "等他回预算", CheckAt: &at}
	h.inbox.noteHandling(second, "server", "s2")
	h.inbox.setHandling(second, HandlingWaiting, "", now)
	// Give the first matter the older check time, so its order in the pass is deterministic.
	older := now.Add(-time.Hour)
	first.Follow.CheckAt = &older
	h.inbox.mu.Unlock()

	ctx := context.Background()
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1}
	h.checkInboxFollowUps(ctx, cfg, now)

	f1, f2 := followOf(h, first), followOf(h, second)
	if f1 == nil || f2 == nil {
		t.Fatalf("follows: %+v / %+v", f1, f2)
	}
	if f1.Checks != 1 || f2.Checks != 0 {
		t.Fatalf("one slot admitted checks %d and %d, want 1 and 0", f1.Checks, f2.Checks)
	}
	if f2.CheckAt == nil || !f2.CheckAt.Equal(at) {
		t.Fatalf("the matter that waited lost its due time: %+v", f2)
	}
	if state, _, _ := handlingNow(h, second); state != HandlingWaiting {
		t.Fatalf("the matter that waited is %q", state)
	}

	// Even with its own conversation now waiting again, the first matter does not free the slot: its woken turn is
	// looking. Once that turn waits again, the second matter is admitted.
	h.inbox.mu.Lock()
	h.inbox.setHandling(first, HandlingWaiting, "", now)
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(ctx, cfg, now.Add(time.Second))
	if f2b := followOf(h, second); f2b.Checks != 1 || f2b.CheckAt != nil {
		t.Fatalf("the second matter after the slot freed: %+v", f2b)
	}
}

// The chain steps a due look writes are the manager's words in the user's language, never a key or a placeholder, and
// they say the next step when there is one.
func TestInboxCheckChainInTheUsersLanguage(t *testing.T) {
	wants := map[i18n.Lang]map[string]string{
		i18n.ZhHans: {"due": "到了再看的时间：等他给报价", "due_plain": "到了再看的时间", "started": "到点回看：等他给报价", "ask": "看过 3 次还没动静，还要跟吗？"},
		i18n.En:     {"due": "Time to look again: 等他给报价", "due_plain": "Time to look again", "started": "Checked again on schedule: 等他给报价", "ask": "Checked 3 times with no news. Keep following?"},
		i18n.Ja:     {"due": "再確認の時間です：等他给报价", "due_plain": "再確認の時間です", "started": "予定どおり再確認：等他给报价", "ask": "3回確認しても動きがありません。続けますか？"},
	}
	for lang, want := range wants {
		t.Run(string(lang), func(t *testing.T) {
			// Twin off: check_due with and without a next step.
			for _, tc := range []struct{ name, next string }{{"due", "等他给报价"}, {"due_plain", ""}} {
				h, it, now := followCheckHub(t, 0)
				h.inbox.lang = func() i18n.Lang { return lang }
				h.inbox.mu.Lock()
				it.Follow.Next = tc.next
				h.inbox.mu.Unlock()
				h.checkInboxFollowUps(context.Background(), InboxAgentConfig{}, now)
				last := lastChain(h, it)
				if last.Kind != ChainCheck || last.Text != want[tc.name] {
					t.Fatalf("%s: %+v, want %q", tc.name, last, want[tc.name])
				}
			}
			// Twin on: the admitted look, then the ask after three.
			h, it, now := followCheckHub(t, 0)
			h.inbox.lang = func() i18n.Lang { return lang }
			h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now)
			if last := lastChain(h, it); last.Text != want["started"] || last.Via != ViaAssistant {
				t.Fatalf("started: %+v, want %q", last, want["started"])
			}
			h2, it2, now2 := followCheckHub(t, inboxFollowUpAskChecks)
			h2.inbox.lang = func() i18n.Lang { return lang }
			h2.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now2)
			if last := lastChain(h2, it2); last.Text != want["ask"] {
				t.Fatalf("ask: %+v, want %q", last, want["ask"])
			}
		})
	}
}

// The regression for the pass order: an item whose latest content has not reached its conversation is news, not an
// empty look, even when its check time is long past. One pass leaves the check unrecorded, the count untouched, and
// the conversation gets the single new-messages note rather than a due-look note beside it.
func TestInboxCheckNewContentIsNotADueCheck(t *testing.T) {
	h, it, now := followCheckHub(t, 1)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	h.inbox.mu.Lock()
	it.Last, it.Context, it.UpdatedAt = "101", "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：报价好了", now
	h.inbox.mu.Unlock()

	h.handPass(context.Background())

	h.inbox.mu.Lock()
	checks := it.Follow.Checks
	kinds := chainKinds(it)
	pending := append([]inboxNote(nil), it.Agent.Pending...)
	state := it.Handling.State
	servedOf := it.Agent.Of
	h.inbox.mu.Unlock()
	if checks != 0 {
		t.Fatalf("the count after new content: %d, want 0 (escalate starts it over)", checks)
	}
	if servedOf != "101" {
		t.Fatalf("the conversation's content is %q, want the new 101", servedOf)
	}
	for _, k := range kinds {
		if k == ChainCheck {
			t.Fatalf("a due check was recorded beside new content: %v", kinds)
		}
	}
	if state != HandlingLooking {
		t.Fatalf("after new content: handling %q", state)
	}
	h.f.mu.Lock()
	var notes []string
	for _, m := range h.f.agentSent {
		if m["session_id"] == "s1" {
			notes = append(notes, m["text"].(string))
		}
	}
	h.f.mu.Unlock()
	all := strings.Join(notes, "\n")
	if len(notes) != 1 || strings.Contains(all, "到点回看") || strings.Contains(all, "到了再看") {
		t.Fatalf("the conversation got %d notes, want one new-messages note without a due one:\n%s", len(notes), all)
	}
	if !strings.Contains(all, "报价好了") {
		t.Fatalf("the new-messages note does not carry the news:\n%s", all)
	}
	_ = pending
}

// Content that arrives while the twin is off stays waiting rather than being released as check_due: enabling the twin
// again still delivers it. Without new content, the same due item goes to the user.
func TestInboxCheckTwinOffKeepsWaitingForNewContent(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	h.inbox.mu.Lock()
	it.Last, it.Context, it.UpdatedAt = "101", "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：报价好了", now
	at := now.Add(-time.Minute)
	it.Follow.CheckAt = &at
	h.inbox.mu.Unlock()

	h.handPass(context.Background())

	h.inbox.mu.Lock()
	state := it.Handling.State
	due, checks := it.Follow.Due, it.Follow.Checks
	checkAt := it.Follow.CheckAt
	kinds := chainKinds(it)
	h.inbox.mu.Unlock()
	if state != HandlingWaiting || due || checks != 0 || checkAt == nil {
		t.Fatalf("new content with the twin off: handling %q, due %v, checks %d, check_at %v", state, due, checks, checkAt)
	}
	for _, k := range kinds {
		if k == ChainCheck {
			t.Fatalf("a due check was recorded with the twin off over new content: %v", kinds)
		}
	}
	if got := len(h.f.sentToAgents()); got != 0 {
		t.Fatalf("the twin is off but %d notes went out", got)
	}
}

// Without new content the twin-off rule stands: the item is due and needs the user, with no check counted.
func TestInboxCheckTwinOffWithoutNewContentIsDue(t *testing.T) {
	h, it, now := followCheckHub(t, 2)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	h.inbox.mu.Lock()
	it.Tier = TierFYI
	h.inbox.mu.Unlock()

	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{}, now)

	h.inbox.mu.Lock()
	state, listed, checks := it.Handling.State, it.listed(), it.Follow.Checks
	h.inbox.mu.Unlock()
	if state != HandlingDone || !it.Follow.Due || it.Follow.CheckAt != nil || checks != 2 {
		t.Fatalf("twin off with no news: handling %q, follow %+v", state, it.Follow)
	}
	if listed.Now != NowCheckDue || listed.Status != StatusAction {
		t.Fatalf("listed: now %q, status %q", listed.Now, listed.Status)
	}
}
