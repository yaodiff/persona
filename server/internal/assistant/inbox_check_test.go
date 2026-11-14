package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// followCheckHub has one item waiting on the other side, with its next look already due and a processing conversation
// ready to wake. The caller chooses how many earlier checks found no news.
func followCheckHub(t *testing.T, checks int) (*inboxHub, *InboxItem, time.Time) {
	t.Helper()
	h, _, it := handledHub(t)
	now := time.Now().UTC()
	at := now.Add(-time.Minute)
	h.inbox.mu.Lock()
	it.Follow = &InboxFollow{Who: "张三", Next: "等他给报价", CheckAt: &at, Checks: checks}
	it.Suggestion = nil
	it.Agent.Pending = nil
	it.Agent.Wake = false
	it.Agent.Back = ""
	h.inbox.setHandling(it, HandlingWaiting, "", now)
	h.inbox.mu.Unlock()
	return h, it, now
}

func followCheckItemState(h *inboxHub, it *InboxItem) string {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return string(itemState(it))
}

func followCheckNoFailure(t *testing.T, h *inboxHub, it *InboxItem) {
	t.Helper()
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	for _, kind := range chainKinds(it) {
		if kind == ChainFailed {
			t.Fatalf("a check that returned to waiting recorded a failure: %v", chainKinds(it))
		}
	}
}

// A future look, one whose suggestion already waits for the user, a member merged into another row, and one with new
// content still to escalate are left exactly as they were. A merged member is not open even though its DoneAt may be
// empty; new content is a source-triggered wake, not an empty due check.
func TestInboxCheckSkipsItemsThatMustNotRun(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prepare func(*InboxItem, time.Time)
	}{
		{name: "not due", prepare: func(it *InboxItem, now time.Time) {
			at := now.Add(time.Hour)
			it.Follow.CheckAt = &at
		}},
		{name: "current suggestion", prepare: func(it *InboxItem, now time.Time) {
			it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "可以，周四给你", At: now}
		}},
		{name: "merged member", prepare: func(it *InboxItem, now time.Time) {
			it.MergedInto = "lark:f1:parent"
		}},
		{name: "new content not escalated yet", prepare: func(it *InboxItem, now time.Time) {
			it.Last = "101"
			it.Context = "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：报价好了"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it, now := followCheckHub(t, 0)
			h.inbox.mu.Lock()
			tc.prepare(it, now)
			h.inbox.mu.Unlock()
			before := followCheckItemState(h, it)

			h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now)

			if after := followCheckItemState(h, it); after != before {
				t.Fatalf("a skipped item changed:\nbefore %s\nafter  %s", before, after)
			}
			if got := len(h.f.sentToAgents()); got != 0 {
				t.Fatalf("a skipped item sent %d messages", got)
			}
		})
	}
}

// A due look takes a conversation slot: it counts the check, records the step, and wakes with who, what and the check
// number. Delivery, not the check itself, sends that note into the existing processing conversation.
func TestInboxCheckAdmitsDueFollowUpAndDeliversNote(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	ctx := context.Background()
	h.checkInboxFollowUps(ctx, InboxAgentConfig{Enabled: true, Concurrency: 1}, now)

	f := followOf(h, it)
	if f == nil || f.Checks != 1 || f.CheckAt != nil || f.Due {
		t.Fatalf("follow after the admitted check: %+v", f)
	}
	if state, wake, back := handlingNow(h, it); state != HandlingLooking || !wake || back != HandlingWaiting {
		t.Fatalf("handling %q, wake %v, back %q", state, wake, back)
	}
	last := lastChain(h, it)
	if last.Kind != ChainCheck || last.Via != ViaAssistant || strings.TrimSpace(last.Text) == "" || strings.Contains(last.Text, "{") {
		t.Fatalf("check step: %+v", last)
	}
	h.inbox.mu.Lock()
	pending := append([]inboxNote(nil), it.Agent.Pending...)
	h.inbox.mu.Unlock()
	if len(pending) != 1 || pending[0].Name != prompts.NoteCheck || pending[0].Who != "张三" ||
		pending[0].Next != "等他给报价" || pending[0].Checks != 1 {
		t.Fatalf("pending check note: %+v", pending)
	}
	if got := len(h.f.sentToAgents()); got != 0 {
		t.Fatalf("the check delivered its own note: %d sends", got)
	}

	h.deliverPending(ctx)

	sent := h.f.sentToAgents()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "s1: ") {
		t.Fatalf("the check went to the wrong conversations: %v", sent)
	}
	text := sent[0]
	for _, want := range []string{"张三", "等他给报价", "1"} {
		if !strings.Contains(text, want) {
			t.Errorf("the check note lacks %q:\n%s", want, text)
		}
	}
	if state, wake, back := handlingNow(h, it); state != HandlingLooking || wake || back != HandlingWaiting {
		t.Fatalf("after delivery: handling %q, wake %v, back %q", state, wake, back)
	}
	h.inbox.mu.Lock()
	left := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d notes left after delivery", left)
	}
}

// With the twin disabled, handPass still settles a due look before its disabled-twin return: the user gets check_due,
// not a silent wait and not a model call or a wake.
func TestInboxCheckTwinOffReturnsCheckDue(t *testing.T) {
	h, it, _ := followCheckHub(t, 0)
	h.inbox.mu.Lock()
	it.Tier = TierFYI
	h.inbox.mu.Unlock()
	model, calls := captureModel(t, `{"tier":"action"}`)
	h.in.setModel(model)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })

	h.handPass(context.Background())

	f := followOf(h, it)
	if f == nil || f.CheckAt != nil || !f.Due || f.Checks != 0 {
		t.Fatalf("follow with the twin off: %+v", f)
	}
	if state, wake, _ := handlingNow(h, it); state != HandlingDone || wake {
		t.Fatalf("handling %q, wake %v", state, wake)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if listed.Now != NowCheckDue || listed.Status != StatusAction || listed.Tier != TierAction {
		t.Fatalf("listed with the twin off: now %q, status %q, tier %q", listed.Now, listed.Status, listed.Tier)
	}
	last := lastChain(h, it)
	if last.Kind != ChainCheck || last.Via != ViaAssistant || strings.TrimSpace(last.Text) == "" || strings.Contains(last.Text, "{") {
		t.Fatalf("localized check step: %+v", last)
	}
	if pending != 0 || len(h.f.sentToAgents()) != 0 || len(calls()) != 0 {
		t.Fatalf("twin off: %d pending notes, %d sends, %d model calls", pending, len(h.f.sentToAgents()), len(calls()))
	}
}

// Three empty looks are enough: the fourth pass does not wake another conversation; it asks the user whether to keep
// following the matter.
func TestInboxCheckAfterThreeLooksAsksWithoutWake(t *testing.T) {
	h, it, now := followCheckHub(t, inboxFollowUpAskChecks)
	h.inbox.mu.Lock()
	it.Tier = TierFYI
	h.inbox.mu.Unlock()
	model, calls := captureModel(t, `{"tier":"action"}`)
	h.in.setModel(model)

	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now)

	f := followOf(h, it)
	if f == nil || f.Checks != inboxFollowUpAskChecks || f.CheckAt != nil || f.Due {
		t.Fatalf("follow after three checks: %+v", f)
	}
	if state, wake, _ := handlingNow(h, it); state != HandlingDone || wake {
		t.Fatalf("handling %q, wake %v", state, wake)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if listed.Now != NowFollowUpAsk || listed.Status != StatusAction || listed.Tier != TierAction {
		t.Fatalf("listed after three checks: now %q, status %q, tier %q", listed.Now, listed.Status, listed.Tier)
	}
	if last := lastChain(h, it); last.Kind != ChainCheck || last.Via != ViaAssistant || strings.TrimSpace(last.Text) == "" {
		t.Fatalf("last step after three checks: %+v", last)
	}
	if pending != 0 || len(h.f.sentToAgents()) != 0 || len(calls()) != 0 {
		t.Fatalf("after three checks: %d pending notes, %d sends, %d model calls", pending, len(h.f.sentToAgents()), len(calls()))
	}
}

// A looking conversation consumes the same slot startInboxAgents uses. A due look stays due while the only slot is
// occupied, and is admitted by a later pass after that conversation leaves looking.
func TestInboxCheckWaitsForConcurrencyCapacity(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	busy := actionItem("busy", now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "busy"}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1}
	before := followCheckItemState(h, it)

	h.checkInboxFollowUps(context.Background(), cfg, now)

	if after := followCheckItemState(h, it); after != before {
		t.Fatalf("the due check ran without capacity:\nbefore %s\nafter  %s", before, after)
	}

	later := now.Add(time.Minute)
	h.inbox.mu.Lock()
	h.inbox.setHandling(busy, HandlingWaiting, "", later)
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(context.Background(), cfg, later)

	f := followOf(h, it)
	if f == nil || f.Checks != 1 || f.CheckAt != nil {
		t.Fatalf("follow after capacity freed: %+v", f)
	}
	if state, wake, back := handlingNow(h, it); state != HandlingLooking || !wake || back != HandlingWaiting {
		t.Fatalf("after capacity freed: handling %q, wake %v, back %q", state, wake, back)
	}
}

// A check whose turn only updates the recap is not a failure: its count stays, and it waits again with a fresh default
// check time.
func TestInboxCheckRecapOnlyRestoresWaiting(t *testing.T) {
	h, it, now := followCheckHub(t, 0)
	ctx := context.Background()
	h.checkInboxFollowUps(ctx, InboxAgentConfig{Enabled: true}, now)
	h.deliverPending(ctx)

	if _, err := suggestTool(t, h, `{"recap":"又问了一次，还是等他给报价"}`); err != nil {
		t.Fatal(err)
	}

	f := followOf(h, it)
	if f == nil || f.Checks != 1 || f.Due {
		t.Fatalf("follow after a recap-only check: %+v", f)
	}
	wantCheckIn(t, h, it, inboxFollowDefault, "recap-only check")
	if state, wake, _ := handlingNow(h, it); state != HandlingWaiting || wake {
		t.Fatalf("handling %q, wake %v", state, wake)
	}
	followCheckNoFailure(t, h, it)
}

// Timeout and an idle turn without a draft have the same restoration as recap-only when this turn came from waiting.
func TestInboxCheckSweepRestoresWaiting(t *testing.T) {
	for _, tc := range []struct {
		name           string
		timeoutMinutes int
		age            time.Duration
		working        bool
	}{
		{name: "timeout", timeoutMinutes: 1, age: 2 * time.Minute, working: true},
		{name: "no draft", timeoutMinutes: 15, age: 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it, now := followCheckHub(t, 0)
			ctx := context.Background()
			cfg := InboxAgentConfig{Enabled: true, TimeoutMinutes: tc.timeoutMinutes}
			h.checkInboxFollowUps(ctx, cfg, now)
			h.deliverPending(ctx)
			started := time.Now().Add(-tc.age)
			h.inbox.mu.Lock()
			it.Agent.StartedAt = &started
			h.inbox.mu.Unlock()
			status := proto.StatusIdle
			if tc.working {
				status = proto.StatusWorking
			}
			h.f.setSessions("server", proto.Session{SessionID: "s1", Status: status})

			h.sweepInboxAgents(ctx, cfg, time.Now())

			f := followOf(h, it)
			if f == nil || f.Checks != 1 || f.Due {
				t.Fatalf("follow after %s: %+v", tc.name, f)
			}
			wantCheckIn(t, h, it, inboxFollowDefault, tc.name)
			if state, wake, _ := handlingNow(h, it); state != HandlingWaiting || wake {
				t.Fatalf("after %s: handling %q, wake %v", tc.name, state, wake)
			}
			followCheckNoFailure(t, h, it)
		})
	}
}

// New messages start the no-news count over. Once the processing conversation has set another wait, its next due look
// can run as check one rather than being suppressed by the old count or due mark.
func TestInboxCheckNewMessagesResetAndPermitAnother(t *testing.T) {
	h, it, now := followCheckHub(t, 2)
	ctx := context.Background()
	h.inbox.mu.Lock()
	it.Follow.Due = true
	it.Last, it.TriagedAt = "101", "101"
	it.Context = "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：报价还要等一下"
	it.UpdatedAt = now
	h.inbox.mu.Unlock()

	if got := h.inbox.escalate(now.Add(-time.Hour), now); len(got) != 1 {
		t.Fatalf("new messages escalated %d items, want 1", len(got))
	}
	if f := followOf(h, it); f == nil || f.Checks != 0 || f.Due {
		t.Fatalf("follow after new messages: %+v", f)
	}
	h.deliverPending(ctx)
	if _, err := suggestTool(t, h, `{"recap":"还要等报价","follow":{"who":"张三","next":"等他给报价","check_in":"1h"}}`); err != nil {
		t.Fatal(err)
	}
	later := time.Now().UTC()
	h.inbox.mu.Lock()
	at := later.Add(-time.Minute)
	it.Follow.CheckAt = &at
	h.inbox.mu.Unlock()

	h.checkInboxFollowUps(ctx, InboxAgentConfig{Enabled: true}, later)

	f := followOf(h, it)
	if f == nil || f.Checks != 1 || f.CheckAt != nil || f.Due {
		t.Fatalf("follow on the later due check: %+v", f)
	}
	if state, wake, back := handlingNow(h, it); state != HandlingLooking || !wake || back != HandlingWaiting {
		t.Fatalf("later check: handling %q, wake %v, back %q", state, wake, back)
	}
	h.inbox.mu.Lock()
	pending := append([]inboxNote(nil), it.Agent.Pending...)
	h.inbox.mu.Unlock()
	if len(pending) != 1 || pending[0].Name != prompts.NoteCheck || pending[0].Checks != 1 {
		t.Fatalf("later check note: %+v", pending)
	}
}

// Instruct and reject are the user taking the matter back, so both clear the old no-news count and due mark. Confirmed
// and direct replies have the same assertions in inbox_waiting_test.go.
func TestInboxCheckUserActionsResetLooks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		body       string
		suggestion bool
	}{
		{name: "instruct", body: `{"version":0,"action":"instruct","text":"先问问他预算"}`},
		{name: "reject", body: `{"version":1,"action":"reject"}`, suggestion: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it, now := followCheckHub(t, 2)
			h.inbox.mu.Lock()
			it.Follow.Due = true
			if tc.suggestion {
				it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "可以，周四给你", At: now}
				it.SuggestSeq = 1
			}
			h.inbox.mu.Unlock()

			if code, out := suggestionCall(t, h, it.ID, tc.body); code != 200 {
				t.Fatalf("%s: %d %v", tc.name, code, out)
			}
			if f := followOf(h, it); f == nil || f.Checks != 0 || f.Due {
				t.Fatalf("follow after %s: %+v", tc.name, f)
			}
		})
	}
}

// A stored wait whose processing conversation is gone cannot be checked by waking anything. It returns to the user as
// check_due without queuing a replacement conversation.
func TestInboxCheckWithoutSessionReturnsCheckDue(t *testing.T) {
	h, it, now := followCheckHub(t, 1)
	h.inbox.mu.Lock()
	it.Tier = TierFYI
	it.Agent.MachineID, it.Agent.SessionID = "", ""
	it.Handling.MachineID, it.Handling.SessionID = "", ""
	h.inbox.mu.Unlock()
	model, calls := captureModel(t, `{"tier":"action"}`)
	h.in.setModel(model)

	h.checkInboxFollowUps(context.Background(), InboxAgentConfig{Enabled: true}, now)

	f := followOf(h, it)
	if f == nil || f.Checks != 1 || f.CheckAt != nil || !f.Due {
		t.Fatalf("follow without a conversation: %+v", f)
	}
	h.inbox.mu.Lock()
	state, agentState, wake, pending := it.Handling.State, it.Agent.State, it.Agent.Wake, len(it.Agent.Pending)
	listed := it.listed()
	h.inbox.mu.Unlock()
	if state != HandlingDone || agentState == AgentQueued || wake || pending != 0 {
		t.Fatalf("without a conversation: handling %q, agent %q, wake %v, %d notes", state, agentState, wake, pending)
	}
	if listed.Now != NowCheckDue || listed.Status != StatusAction || listed.Tier != TierAction {
		t.Fatalf("listed without a conversation: now %q, status %q, tier %q", listed.Now, listed.Status, listed.Tier)
	}
	if last := lastChain(h, it); last.Kind != ChainCheck || last.Via != ViaAssistant ||
		strings.TrimSpace(last.Text) == "" || strings.Contains(last.Text, "{") {
		t.Fatalf("localized check step without a conversation: %+v", last)
	}
	if got := len(h.f.sentToAgents()); got != 0 || len(calls()) != 0 {
		t.Fatalf("without a conversation: %d sends, %d model calls", got, len(calls()))
	}
}
