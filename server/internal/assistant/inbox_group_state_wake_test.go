package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	inboxGroupStateWakeWho  = "Example teammate"
	inboxGroupStateWakeNext = "Wait for the synthetic confirmation"
)

// inboxGroupStateWakeApplyReplied starts with an existing wait and a reply suggestion that the owner's source reply
// supersedes. It checks the common B-13 record, follow-up reset, suggestion cleanup and same-position idempotence.
func inboxGroupStateWakeApplyReplied(t *testing.T, h *inboxHub, it *InboxItem, initialState string) {
	t.Helper()
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "UTC" })
	h.inbox.mu.Lock()
	oldCheck := time.Now().UTC().Add(time.Hour)
	it.Follow = &InboxFollow{Who: inboxGroupStateWakeWho, Next: inboxGroupStateWakeNext, CheckAt: &oldCheck, Checks: 2, Due: true}
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Superseded synthetic reply",
		Note: "Old synthetic note", Changes: []DocChange{{Ref: "doc-1", Field: "status", Value: "old"}}, At: time.Now().UTC()}
	it.SuggestSeq = 1
	h.inbox.setHandling(it, initialState, "", time.Now())
	before := inboxGroupStateContentOf(it)
	h.inbox.mu.Unlock()

	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	entries := inboxGroupStateSelfReplies(it)
	if len(entries) != 1 || entries[0].Via != it.Source || entries[0].Link == nil ||
		entries[0].Link.Type != LinkSource || entries[0].Link.Position != it.Last {
		t.Fatalf("self_replied entries = %+v, want one source-linked entry at %s", entries, it.Last)
	}
	if !it.open() {
		t.Fatalf("a processing conversation's source reply filed the item: %+v", it)
	}
	f := followOf(h, it)
	if f == nil || f.Who != inboxGroupStateWakeWho || f.Next != inboxGroupStateWakeNext || f.Checks != 0 || f.Due {
		t.Fatalf("follow after source reply = %+v, want preserved who/next and reset checks", f)
	}
	wantCheckIn(t, h, it, inboxFollowDefault, "source reply's default follow")
	h.inbox.mu.Lock()
	suggestion := currentSuggestion(it)
	draft, note, changes := it.Agent.Draft, it.Agent.Note, len(it.Agent.Changes)
	pending := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if suggestion != nil || draft != "" || note != "" || changes != 0 {
		t.Fatalf("the source reply left its superseded suggestion: suggestion=%+v draft=%q note=%q changes=%d",
			suggestion, draft, note, changes)
	}
	assertInboxGroupStateContent(t, before, it)

	// Repeating the same source position is not another owner reply and does not queue another note.
	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	h.inbox.mu.Lock()
	pendingAfter := len(it.Agent.Pending)
	h.inbox.mu.Unlock()
	if len(inboxGroupStateSelfReplies(it)) != 1 || pendingAfter != pending {
		t.Fatalf("repeated source reply was not idempotent: chain=%+v pending %d -> %d", it.Chain, pending, pendingAfter)
	}
	assertInboxGroupStateContent(t, before, it)
}

// inboxGroupStateWakeOrdinaryReplied applies replied to an ordinary processing conversation. While its wake is
// undelivered the item may be looking; Back must remember that an empty turn returns to waiting.
func inboxGroupStateWakeOrdinaryReplied(t *testing.T, twinEnabled bool) (*inboxHub, *InboxItem) {
	t.Helper()
	h := inboxGroupStateTestHub(t, twinEnabled)
	it := inboxGroupStateTestItem(h, "event-wake-ordinary", true)
	inboxGroupStateWakeApplyReplied(t, h, it, HandlingDone)
	state, wake, back := handlingNow(h, it)
	if twinEnabled {
		if state != HandlingLooking && state != HandlingWaiting || !wake || back != HandlingWaiting {
			t.Fatalf("after source reply: handling %q, wake %v, back %q; want a wake that restores waiting", state, wake, back)
		}
		requireInboxGroupStatePending(t, it, prompts.NoteSelfReplied, "2026-11-13 06:30")
	} else {
		if state != HandlingWaiting || wake || back != "" {
			t.Fatalf("disabled twin after source reply: handling %q, wake %v, back %q; want waiting only", state, wake, back)
		}
		h.inbox.mu.Lock()
		pending := len(it.Agent.Pending)
		h.inbox.mu.Unlock()
		if pending != 0 {
			t.Fatalf("disabled twin queued %d notes, want none", pending)
		}
	}
	return h, it
}

func TestInboxGroupStateWakeRepliedRestoresWaiting(t *testing.T) {
	t.Run("recap only", func(t *testing.T) {
		h, it := inboxGroupStateWakeOrdinaryReplied(t, true)
		h.deliverPending(context.Background())
		if _, _, err := h.suggest(context.Background(), "session-1", suggestIn{Recap: "Synthetic recap without an outcome"}); err != nil {
			t.Fatal(err)
		}
		state, wake, _ := handlingNow(h, it)
		if state != HandlingWaiting || wake {
			t.Fatalf("recap-only turn: handling %q, wake %v; want waiting", state, wake)
		}
		wantCheckIn(t, h, it, inboxFollowDefault, "recap-only source-reply turn")
		followCheckNoFailure(t, h, it)
	})

	t.Run("conclusion done files", func(t *testing.T) {
		h, it := inboxGroupStateWakeOrdinaryReplied(t, true)
		h.deliverPending(context.Background())
		in := suggestIn{Kind: SuggestConclusion, Text: "Synthetic conclusion", Outcome: "done"}
		if _, _, err := h.suggest(context.Background(), "session-1", in); err != nil {
			t.Fatal(err)
		}
		h.inbox.mu.Lock()
		open, doneAt, doneBy := it.open(), it.DoneAt, it.DoneBy
		h.inbox.mu.Unlock()
		if open || doneAt == nil || doneBy != doneByManager {
			t.Fatalf("conclusion done: open=%v done_at=%v done_by=%q, want manager filing", open, doneAt, doneBy)
		}
	})

	for _, tc := range []struct {
		name           string
		timeoutMinutes int
		age            time.Duration
		status         string
	}{
		{name: "timeout", timeoutMinutes: 1, age: 2 * time.Minute, status: proto.StatusWorking},
		{name: "no draft", timeoutMinutes: 15, age: 2 * time.Minute, status: proto.StatusIdle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it := inboxGroupStateWakeOrdinaryReplied(t, true)
			ctx := context.Background()
			h.deliverPending(ctx)
			started := time.Now().Add(-tc.age)
			h.inbox.mu.Lock()
			it.Agent.StartedAt = &started
			h.inbox.mu.Unlock()
			h.f.setSessions("server", proto.Session{SessionID: "session-1", Status: tc.status})

			h.sweepInboxAgents(ctx, InboxAgentConfig{Enabled: true, TimeoutMinutes: tc.timeoutMinutes}, time.Now())

			state, wake, _ := handlingNow(h, it)
			if state != HandlingWaiting || wake {
				t.Fatalf("%s restoration: handling %q, wake %v; want waiting", tc.name, state, wake)
			}
			wantCheckIn(t, h, it, inboxFollowDefault, tc.name+" source-reply restoration")
			followCheckNoFailure(t, h, it)
		})
	}

	t.Run("twin disabled only waits", func(t *testing.T) {
		h, _ := inboxGroupStateWakeOrdinaryReplied(t, false)
		h.deliverPending(context.Background())
		if notes := notesOf(h.f, "session-1"); len(notes) != 0 {
			t.Fatalf("disabled twin delivered notes: %q", notes)
		}
	})
}

func TestInboxGroupStateWakeRepliedKeepsAgentAndReportsStop(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	it := inboxGroupStateTestItem(h, "event-wake-agent", true)
	h.inboxAgentStarted(ctx, "session-1", "server", "agent-session", "synthetic Agent task")
	inboxGroupStateWakeApplyReplied(t, h, it, HandlingAgent)

	state, wake, back := handlingNow(h, it)
	if state != HandlingAgent || !wake || back != HandlingAgent {
		t.Fatalf("source reply while an Agent works: handling %q, wake %v, back %q; want agent preserved", state, wake, back)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfReplied, "2026-11-13 06:30")

	// The existing stopped-Agent path still records the same stop and hands the matter back to the conversation.
	h.f.setSessions("server",
		proto.Session{SessionID: "session-1", Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: "agent-session", Status: proto.StatusIdle,
			LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "server", "agent-session")
	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("Agent stops after source reply = %d, want one", got)
	}
	stop := latestInboxAgentStop(t, it)
	if stop.Detail != "Synthetic Agent result" || stop.Link == nil || stop.Link.SessionID != "agent-session" {
		t.Fatalf("Agent stop entry = %+v", stop)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("handling after the Agent stop = %q, want looking", state)
	}
}

func TestInboxGroupStateWakeReadOnlyRecordsWaitingOrAgent(t *testing.T) {
	for _, state := range []string{HandlingWaiting, HandlingAgent} {
		t.Run(state, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-read-"+state, true)
			at := time.Now().UTC().Add(time.Hour)
			h.inbox.mu.Lock()
			it.Follow = &InboxFollow{Who: inboxGroupStateWakeWho, Next: inboxGroupStateWakeNext, CheckAt: &at, Checks: 2, Due: true}
			h.inbox.setHandling(it, state, "", time.Now())
			before := rawJSON(it)
			h.inbox.mu.Unlock()

			report := inboxGroupStateReport(it, true, "")
			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateChange(t, rest, changed, it)
			var want InboxItem
			if err := json.Unmarshal(before, &want); err != nil {
				t.Fatal(err)
			}
			want.Read = true
			if got := string(rawJSON(it)); got != string(rawJSON(&want)) {
				t.Fatalf("read changed more than Read:\nwant %s\n got %s", rawJSON(&want), got)
			}
			h.deliverPending(context.Background())
			if notes := notesOf(h.f, "session-1"); len(notes) != 0 {
				t.Fatalf("read in %q delivered notes: %q", state, notes)
			}
		})
	}
}

func TestInboxGroupStateWakeReadStillWakesOtherHandling(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	it := inboxGroupStateTestItem(h, "event-read-ordinary", true)
	beforeChain := len(it.Chain)
	report := inboxGroupStateReport(it, true, "")
	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	state, wake, back := handlingNow(h, it)
	if !it.Read || state != HandlingLooking || !wake || back != HandlingDone {
		t.Fatalf("ordinary read: read=%v handling=%q wake=%v back=%q", it.Read, state, wake, back)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfRead, "")
	if len(it.Chain) != beforeChain {
		t.Fatalf("ordinary read added a chain entry: %+v", it.Chain)
	}

	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	requireInboxGroupStatePending(t, it, prompts.NoteSelfRead, "")
}

// inboxGroupStateWakeConnector lets the direct-reply regression use the real POST /reply path for a grouped event.
type inboxGroupStateWakeConnector struct {
	got []connector.InboxReply
}

func (*inboxGroupStateWakeConnector) ID() string   { return inboxGroupStateSource }
func (*inboxGroupStateWakeConnector) Name() string { return "Example chat" }
func (*inboxGroupStateWakeConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}, Kinds: []string{KindGroup}}
}
func (*inboxGroupStateWakeConnector) Collect(context.Context) ([]connector.InboxThing, error) {
	return nil, nil
}
func (c *inboxGroupStateWakeConnector) Reply(_ context.Context, r connector.InboxReply) error {
	c.got = append(c.got, r)
	return nil
}

func TestInboxGroupStateWakeDirectReplyThenSourceReports(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	conn := &inboxGroupStateWakeConnector{}
	h.addConnector(conn, true)
	it := inboxGroupStateTestItem(h, "event-direct-reply", true)
	it.Connector = &InboxConnector{Thread: inboxGroupStateThread, CanReply: true}

	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"Synthetic direct reply"}`)
	if code != 200 || out["done_at"] != nil {
		t.Fatalf("direct reply: %d %v", code, out)
	}
	if len(conn.got) != 1 || conn.got[0].Text != "Synthetic direct reply" || conn.got[0].Last != it.Last {
		t.Fatalf("direct reply sends = %+v", conn.got)
	}
	toldConv(t, h, "Synthetic direct reply")
	h.deliverPending(ctx) // finish the non-waking NoteSent before observing the source reports
	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake || back != "" || it.Agent.SentOf != it.Last {
		t.Fatalf("after direct reply: handling=%q wake=%v back=%q sent_of=%q last=%q", state, wake, back, it.Agent.SentOf, it.Last)
	}
	h.inbox.mu.Lock()
	if len(it.Agent.Pending) != 0 {
		t.Fatalf("direct reply left %d pending notes after delivery", len(it.Agent.Pending))
	}
	before := rawJSON(it)
	h.inbox.mu.Unlock()
	baselineNotes := len(notesOf(h.f, "session-1"))

	// The source's replied is the message Persona just sent, not another reply by the owner.
	replied := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{replied})
	requireInboxGroupStateIgnored(t, rest, changed)
	if got := string(rawJSON(it)); got != string(before) {
		t.Fatalf("Persona's own source reply changed the waiting item:\nwant %s\n got %s", before, got)
	}

	// Its read marker at the same position changes Read only.
	read := inboxGroupStateReport(it, true, "")
	rest, changed = h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{read})
	requireInboxGroupStateChange(t, rest, changed, it)
	var want InboxItem
	if err := json.Unmarshal(before, &want); err != nil {
		t.Fatal(err)
	}
	want.Read = true
	if got := string(rawJSON(it)); got != string(rawJSON(&want)) {
		t.Fatalf("read after Persona's own reply changed more than Read:\nwant %s\n got %s", rawJSON(&want), got)
	}
	if len(inboxGroupStateSelfReplies(it)) != 0 {
		t.Fatalf("Persona's own reply recorded self_replied: %+v", it.Chain)
	}
	h.deliverPending(ctx)
	if got := len(notesOf(h.f, "session-1")); got != baselineNotes {
		t.Fatalf("source reports delivered new notes: %d -> %d", baselineNotes, got)
	}
}

func TestInboxGroupStateWakeRepliedRejectsStaleLast(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	it := inboxGroupStateTestItem(h, "event-stale-wake", true)
	at := time.Now().UTC().Add(time.Hour)
	h.inbox.mu.Lock()
	it.Follow = &InboxFollow{Who: inboxGroupStateWakeWho, Next: inboxGroupStateWakeNext, CheckAt: &at, Checks: 2, Due: true}
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Keep this synthetic suggestion", At: time.Now().UTC()}
	h.inbox.setHandling(it, HandlingDone, "", time.Now())
	before := rawJSON(it)
	h.inbox.mu.Unlock()
	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	report.Last = "m9"

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	if got := string(rawJSON(it)); got != string(before) {
		t.Fatalf("stale replied changed the item:\nwant %s\n got %s", before, got)
	}
}
