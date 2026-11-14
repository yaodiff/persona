package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

const (
	inboxGroupStateSource = "example-chat"
	inboxGroupStateThread = "chat:room"
	inboxGroupStateLast   = "m10"
)

var inboxGroupStateAt = time.Date(2026, 11, 13, 6, 30, 0, 0, time.UTC)

// inboxGroupStateContent is the event content a state report must never replace.
type inboxGroupStateContent struct {
	Title   string
	Summary string
	Reason  string
	Tier    string
	Count   int
	Meta    string
	Rules   string
	Preview string
	Sender  string
}

func inboxGroupStateContentOf(it *InboxItem) inboxGroupStateContent {
	return inboxGroupStateContent{Title: it.Title, Summary: it.Summary, Reason: it.Reason, Tier: it.Tier,
		Count: it.Count, Meta: string(it.Meta), Rules: it.Rules, Preview: it.Preview, Sender: it.Sender}
}

func assertInboxGroupStateContent(t *testing.T, before inboxGroupStateContent, it *InboxItem) {
	t.Helper()
	after := inboxGroupStateContentOf(it)
	for _, check := range []struct{ name, before, after string }{
		{"title", before.Title, after.Title},
		{"summary", before.Summary, after.Summary},
		{"reason", before.Reason, after.Reason},
		{"tier", before.Tier, after.Tier},
		{"count", strconv.Itoa(before.Count), strconv.Itoa(after.Count)},
		{"meta", before.Meta, after.Meta},
		{"rules", before.Rules, after.Rules},
		{"preview", before.Preview, after.Preview},
		{"sender", before.Sender, after.Sender},
	} {
		if check.before != check.after {
			t.Errorf("%s changed from %q to %q", check.name, check.before, check.after)
		}
	}
}

func inboxGroupStateTestHub(t *testing.T, twinEnabled bool) *inboxHub {
	t.Helper()
	h := newInboxHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = twinEnabled })
	return h
}

func inboxGroupStateTestItem(h *inboxHub, id string, withConversation bool) *InboxItem {
	now := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	it := &InboxItem{
		ID: id, Source: inboxGroupStateSource, Kind: KindGroup,
		Title: "Original title", Sender: "Original sender", Preview: "Original preview",
		Count: 7, StartedAt: now.Add(-time.Hour), UpdatedAt: now,
		Tier: TierAction, Summary: "Original summary", Reason: "Original reason",
		Last: inboxGroupStateLast, TriagedAt: inboxGroupStateLast, TriagedBy: "model",
		Context: "Original context", Rules: "Original source rules",
		Meta:     json.RawMessage(`{"messages":["m10"],"owner":"original"}`),
		Group:    &InboxGroup{Connector: inboxGroupStateSource, Thread: inboxGroupStateThread},
		Chain:    []InboxChainEntry{{ID: "c1", At: now, Kind: ChainReceived, Text: "Original received step"}},
		ChainSeq: 1,
	}
	if withConversation {
		it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "session-1", Of: inboxGroupStateLast}
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone,
			MachineID: "server", SessionID: "session-1", Since: &now}
	}
	h.inbox.mu.Lock()
	h.inbox.items[id] = it
	h.inbox.mu.Unlock()
	return it
}

// inboxGroupStateReport deliberately carries replacement content: only read, done, at and last may be read.
func inboxGroupStateReport(it *InboxItem, read bool, done string) connector.InboxThing {
	thread := ""
	if it.Group != nil {
		thread = it.Group.Thread
	}
	return connector.InboxThing{
		Thread: thread, Kind: KindGroup, ID: it.ID,
		Group: &connector.ThingGroup{Batch: "", New: []string{}},
		Read:  read, Done: done, At: inboxGroupStateAt, Last: it.Last,
		Title: "Replacement title", Sender: "Replacement sender", Preview: "Replacement preview",
		Context: "Replacement context", Tier: connector.TierNoise, Why: "Replacement reason",
		Rules: "Replacement source rules", Meta: json.RawMessage(`{"replacement":true}`),
		Extra: &connector.ThingExtra{Count: 99},
	}
}

func inboxGroupStateTestSource() assistantwire.InboxSource {
	return assistantwire.InboxSource{ID: inboxGroupStateSource, Name: "Example chat", Group: true}
}

func inboxGroupStateSelfReplies(it *InboxItem) []InboxChainEntry {
	var out []InboxChainEntry
	for _, entry := range it.Chain {
		if entry.Kind == ChainSelfReplied {
			out = append(out, entry)
		}
	}
	return out
}

func requireInboxGroupStateChange(t *testing.T, rest []connector.InboxThing, changed []*InboxItem, it *InboxItem) {
	t.Helper()
	if len(rest) != 0 {
		t.Fatalf("state reports left as ordinary things: %+v", rest)
	}
	if len(changed) != 1 || changed[0] != it {
		t.Fatalf("changed = %+v, want only %s", changed, it.ID)
	}
}

func requireInboxGroupStateIgnored(t *testing.T, rest []connector.InboxThing, changed []*InboxItem) {
	t.Helper()
	if len(rest) != 0 {
		t.Fatalf("state reports left as ordinary things: %+v", rest)
	}
	if len(changed) != 0 {
		t.Fatalf("ignored state changed items: %+v", changed)
	}
}

func requireInboxGroupStatePending(t *testing.T, it *InboxItem, name, text string) {
	t.Helper()
	if it.Agent == nil || len(it.Agent.Pending) != 1 {
		t.Fatalf("pending notes = %+v, want one %s", it.Agent, name)
	}
	note := it.Agent.Pending[0]
	if note.Name != name || note.Text != text || !it.Agent.Wake {
		t.Fatalf("pending note = %+v, wake = %v; want %s with text %q and wake", note, it.Agent.Wake, name, text)
	}
}

// A reply with a processing conversation stays open, records its source position, and wakes that conversation once.
// Read is also true here to pin replied's precedence over read-only handling.
func TestInboxGroupStateHandledRepliedNotifiesOnce(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	h.f.hello.Owner = "示例用户"
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	it := inboxGroupStateTestItem(h, "event-replied", true)
	before := inboxGroupStateContentOf(it)
	report := inboxGroupStateReport(it, true, connector.DoneReplied)

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if !it.open() || it.DoneAt != nil || it.DoneBy != "" {
		t.Fatalf("a handled reply archived the item: %+v", it)
	}
	entries := inboxGroupStateSelfReplies(it)
	if len(entries) != 1 {
		t.Fatalf("self_replied entries = %+v, want one", entries)
	}
	entry := entries[0]
	if entry.Via != it.Source || entry.Link == nil || entry.Link.Type != LinkSource || entry.Link.Position != inboxGroupStateLast {
		t.Fatalf("self_replied = %+v, want via %s and a source link at %s", entry, it.Source, inboxGroupStateLast)
	}
	if it.Handling == nil || it.Handling.State != HandlingLooking {
		t.Fatalf("handling = %+v, want looking", it.Handling)
	}
	localTime := "2026-11-13 14:30"
	requireInboxGroupStatePending(t, it, prompts.NoteSelfReplied, localTime)

	// A repeated report neither appends another position marker nor queues another note.
	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	if len(inboxGroupStateSelfReplies(it)) != 1 {
		t.Fatalf("repeated report duplicated the chain: %+v", it.Chain)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfReplied, localTime)

	h.deliverPending(context.Background())
	notes := notesOf(h.f, "session-1")
	if len(notes) != 1 {
		t.Fatalf("delivered notes = %q, want one", notes)
	}
	want := prompts.RenderInboxHandlingNote(prompts.NoteSelfReplied,
		prompts.InboxHandlingNote{Owner: "示例用户", Text: localTime})
	if notes[0] != want || !strings.Contains(notes[0], localTime) {
		t.Fatalf("delivered note = %q, want %q with the source time in the user's zone", notes[0], want)
	}

	// Delivery does not make a repeated source report new again.
	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	h.deliverPending(context.Background())
	if again := notesOf(h.f, "session-1"); len(again) != 1 {
		t.Fatalf("repeated report delivered another note: %q", again)
	}
	assertInboxGroupStateContent(t, before, it)
}

// A confirmed reply sent by Persona is not the owner answering elsewhere: the event keeps waiting for the other side.
func TestInboxGroupStateOwnConfirmedReplyKeepsWaiting(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	it := inboxGroupStateTestItem(h, "event-own-send", true)
	it.Handling.State = HandlingWaiting
	it.Agent.SentOf = it.Last
	report := inboxGroupStateReport(it, true, connector.DoneReplied)
	before := string(rawJSON(it))

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	if got := string(rawJSON(it)); got != before {
		t.Fatalf("Persona's own confirmed reply changed the item:\nbefore: %s\nafter:  %s", before, got)
	}
	if !it.open() || it.Handling.State != HandlingWaiting || it.Agent.Wake || len(it.Agent.Pending) != 0 ||
		len(inboxGroupStateSelfReplies(it)) != 0 {
		t.Fatalf("Persona's own confirmed reply did not keep waiting: item=%+v agent=%+v", it, it.Agent)
	}
}

// A read with a processing conversation sets Read and asks it to look, without adding a chain entry.
func TestInboxGroupStateHandledReadNotifiesOnce(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	h.f.hello.Owner = "示例用户"
	it := inboxGroupStateTestItem(h, "event-read", true)
	before := inboxGroupStateContentOf(it)
	beforeChain := len(it.Chain)
	report := inboxGroupStateReport(it, true, "")

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if !it.Read || !it.open() || it.DoneAt != nil || it.DoneBy != "" {
		t.Fatalf("read result = %+v, want read and open", it)
	}
	if len(it.Chain) != beforeChain {
		t.Fatalf("read added a chain entry: %+v", it.Chain)
	}
	if it.Handling == nil || it.Handling.State != HandlingLooking {
		t.Fatalf("handling = %+v, want looking", it.Handling)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfRead, "")

	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	if len(it.Chain) != beforeChain {
		t.Fatalf("repeated read added a chain entry: %+v", it.Chain)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfRead, "")

	h.deliverPending(context.Background())
	notes := notesOf(h.f, "session-1")
	want := prompts.RenderInboxHandlingNote(prompts.NoteSelfRead, prompts.InboxHandlingNote{Owner: "示例用户"})
	if len(notes) != 1 || notes[0] != want {
		t.Fatalf("delivered notes = %q, want %q", notes, want)
	}
	rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateIgnored(t, rest, changed)
	h.deliverPending(context.Background())
	if again := notesOf(h.f, "session-1"); len(again) != 1 {
		t.Fatalf("repeated read delivered another note: %q", again)
	}
	assertInboxGroupStateContent(t, before, it)
}

// New messages clear Read and advance Last. The old read marker must not mark the new version read or wake its chat.
func TestInboxGroupStateRejectsStaleLast(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
		done string
	}{
		{name: "replied", read: true, done: connector.DoneReplied},
		{name: "read cleared by a new message", read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-stale", true)
			it.Last = "m11"
			it.Read = false // what a new appended message leaves
			before := inboxGroupStateContentOf(it)
			beforeChain := len(it.Chain)
			report := inboxGroupStateReport(it, tc.read, tc.done)
			report.Last = inboxGroupStateLast // the state was observed at m10, before m11 arrived

			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateIgnored(t, rest, changed)
			if it.Read || len(it.Chain) != beforeChain || len(inboxGroupStateSelfReplies(it)) != 0 {
				t.Fatalf("stale state changed the item: %+v", it)
			}
			if it.Agent.Wake || len(it.Agent.Pending) != 0 || it.Handling.State != HandlingDone {
				t.Fatalf("stale state woke or moved the conversation: agent=%+v handling=%+v", it.Agent, it.Handling)
			}
			assertInboxGroupStateContent(t, before, it)
		})
	}
}

// Without an established processing conversation, a reply records itself and files the event as replied.
func TestInboxGroupStateWithoutConversationReplied(t *testing.T) {
	for _, tc := range []struct {
		name  string
		agent *InboxAgent
	}{
		{name: "no agent"},
		{name: "no session yet", agent: &InboxAgent{State: AgentQueued, MachineID: "server"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-replied", false)
			it.Agent = tc.agent
			before := inboxGroupStateContentOf(it)
			report := inboxGroupStateReport(it, true, connector.DoneReplied) // replied wins over read-only

			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateChange(t, rest, changed, it)
			if it.open() || it.DoneAt == nil || it.DoneBy != connector.DoneReplied {
				t.Fatalf("reply filing = %+v, want done_by replied", it)
			}
			entries := inboxGroupStateSelfReplies(it)
			if len(entries) != 1 || entries[0].Via != it.Source || entries[0].Link == nil ||
				entries[0].Link.Type != LinkSource || entries[0].Link.Position != inboxGroupStateLast {
				t.Fatalf("self_replied entries = %+v", entries)
			}
			if it.Agent != nil && (it.Agent.Wake || len(it.Agent.Pending) != 0) {
				t.Fatalf("an item without a processing conversation was woken: %+v", it.Agent)
			}

			rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateIgnored(t, rest, changed)
			if len(inboxGroupStateSelfReplies(it)) != 1 {
				t.Fatalf("repeated reply duplicated the chain: %+v", it.Chain)
			}
			assertInboxGroupStateContent(t, before, it)
		})
	}
}

// Read-only filing follows the item's tier: FYI and noise close, while an action stays open but read.
func TestInboxGroupStateWithoutConversationReadByTier(t *testing.T) {
	for _, tc := range []struct {
		tier     string
		done     bool
		openTier string
	}{
		{tier: TierFYI, done: true},
		{tier: TierNoise, done: true},
		{tier: TierAction, openTier: TierAction},
	} {
		t.Run(tc.tier, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-read", false)
			it.Tier = tc.tier
			before := inboxGroupStateContentOf(it)
			beforeChain := len(it.Chain)
			report := inboxGroupStateReport(it, true, "")

			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateChange(t, rest, changed, it)
			if !it.Read {
				t.Fatal("read-only report did not set Read")
			}
			if tc.done {
				if it.open() || it.DoneAt == nil || it.DoneBy != connector.DoneRead {
					t.Fatalf("read filing = %+v, want done_by read", it)
				}
			} else if !it.open() || it.DoneAt != nil || it.DoneBy != "" || it.Tier != tc.openTier {
				t.Fatalf("action read = %+v, want open and unchanged tier", it)
			}
			if !tc.done && len(it.Chain) != beforeChain {
				t.Fatalf("an action read added a chain entry: %+v", it.Chain)
			}
			if len(inboxGroupStateSelfReplies(it)) != 0 {
				t.Fatalf("read-only report recorded a reply: %+v", it.Chain)
			}

			rest, changed = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateIgnored(t, rest, changed)
			assertInboxGroupStateContent(t, before, it)
		})
	}
}

// Invalid targets and reports with no recognized state are consumed but change nothing.
func TestInboxGroupStateRejectsInvalidTargets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*InboxItem, *connector.InboxThing)
	}{
		{name: "already done", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			doneAt := inboxGroupStateAt
			it.DoneAt, it.DoneBy = &doneAt, "user"
		}},
		{name: "another connector", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Connector = "another-source"
		}},
		{name: "another conversation", mutate: func(_ *InboxItem, report *connector.InboxThing) {
			report.Thread = "chat:another"
		}},
		{name: "conversation prefix followed by a letter", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Thread = inboxGroupStateThread + "mate"
		}},
		{name: "conversation prefix followed by a digit", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Thread = inboxGroupStateThread + "2"
		}},
		{name: "conversation prefix followed by underscore", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Thread = inboxGroupStateThread + "_topic"
		}},
		{name: "conversation prefix followed by hyphen", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Thread = inboxGroupStateThread + "-topic"
		}},
		{name: "parent event is not a child of the report", mutate: func(it *InboxItem, _ *connector.InboxThing) {
			it.Group.Thread = "chat"
		}},
		{name: "not a grouped event", mutate: func(it *InboxItem, report *connector.InboxThing) {
			it.Group = nil
			report.Thread = inboxGroupStateThread
		}},
		{name: "unknown item id", mutate: func(_ *InboxItem, report *connector.InboxThing) {
			report.ID = "event-missing"
		}},
		{name: "neither read nor replied", mutate: func(_ *InboxItem, report *connector.InboxThing) {
			report.Read, report.Done = false, ""
		}},
		{name: "unsupported done value", mutate: func(_ *InboxItem, report *connector.InboxThing) {
			report.Read, report.Done = false, connector.DoneRead
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, true)
			it := inboxGroupStateTestItem(h, "event-target", true)
			report := inboxGroupStateReport(it, true, "")
			tc.mutate(it, &report)
			before := inboxGroupStateContentOf(it)
			beforeJSON := string(rawJSON(it))
			beforeChain := len(it.Chain)
			wasOpen, doneBy := it.open(), it.DoneBy

			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateIgnored(t, rest, changed)
			if it.Read || it.open() != wasOpen || it.DoneBy != doneBy || len(it.Chain) != beforeChain {
				t.Fatalf("invalid target changed the item: %+v", it)
			}
			if it.Agent.Wake || len(it.Agent.Pending) != 0 || it.Handling.State != HandlingDone {
				t.Fatalf("invalid target woke or moved the conversation: agent=%+v handling=%+v", it.Agent, it.Handling)
			}
			if got := string(rawJSON(it)); got != beforeJSON {
				t.Fatalf("invalid target mutated the item:\nbefore: %s\nafter:  %s", beforeJSON, got)
			}
			h.inbox.mu.Lock()
			itemCount := len(h.inbox.items)
			h.inbox.mu.Unlock()
			if itemCount != 1 {
				t.Fatalf("invalid target changed the item set to %d items", itemCount)
			}
			assertInboxGroupStateContent(t, before, it)
		})
	}
}

// sameConversation accepts a nested topic, while the boundary rejection cases above reject mere id prefixes.
func TestInboxGroupStateAcceptsNestedConversation(t *testing.T) {
	h := inboxGroupStateTestHub(t, false)
	it := inboxGroupStateTestItem(h, "event-topic", false)
	report := inboxGroupStateReport(it, true, "")
	it.Group.Thread = inboxGroupStateThread + "/topic:1"
	before := inboxGroupStateContentOf(it)

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if !it.Read || !it.open() {
		t.Fatalf("nested conversation read = %+v, want read and open", it)
	}
	assertInboxGroupStateContent(t, before, it)
}

// Source rules on a state report are required by some connectors but are not new triage input for the item.
func TestInboxGroupStateProcessesReportWithRules(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "UTC" })
	it := inboxGroupStateTestItem(h, "event-rules", true)
	before := inboxGroupStateContentOf(it)
	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	report.Rules = "User rules attached by the source; do not replace the item's rules"

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if len(inboxGroupStateSelfReplies(it)) != 1 {
		t.Fatalf("a report with rules was not processed: %+v", it.Chain)
	}
	if it.Rules != "Original source rules" {
		t.Fatalf("item rules changed to %q", it.Rules)
	}
	requireInboxGroupStatePending(t, it, prompts.NoteSelfReplied, "2026-11-13 06:30")
	assertInboxGroupStateContent(t, before, it)
}

// With the twin off, a reply records and waits without a wake, and a read only sets its flag; neither forces Looking.
func TestInboxGroupStateTwinDisabledOnlyRecords(t *testing.T) {
	for _, tc := range []struct {
		name string
		read bool
		done string
	}{
		{name: "replied", done: connector.DoneReplied},
		{name: "read", read: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := inboxGroupStateTestHub(t, false)
			it := inboxGroupStateTestItem(h, "event-disabled", true)
			it.Handling.State = HandlingWaiting
			before := inboxGroupStateContentOf(it)
			beforeChain := len(it.Chain)
			report := inboxGroupStateReport(it, tc.read, tc.done)

			rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
			requireInboxGroupStateChange(t, rest, changed, it)
			if tc.done == connector.DoneReplied {
				if len(inboxGroupStateSelfReplies(it)) != 1 {
					t.Fatalf("disabled twin did not record the reply: %+v", it.Chain)
				}
			} else if !it.Read || len(it.Chain) != beforeChain {
				t.Fatalf("disabled twin read = %+v", it)
			}
			if !it.open() || it.Agent.Wake || len(it.Agent.Pending) != 0 || it.Handling.State != HandlingWaiting {
				t.Fatalf("disabled twin dispatched work or forced looking: agent=%+v handling=%+v", it.Agent, it.Handling)
			}
			h.deliverPending(context.Background())
			if notes := notesOf(h.f, "session-1"); len(notes) != 0 {
				t.Fatalf("disabled twin delivered notes: %q", notes)
			}
			assertInboxGroupStateContent(t, before, it)
		})
	}
}

// State separation keeps ordinary things and real batches in their original order for the existing connector paths.
func TestInboxGroupStateLeavesOrdinaryThings(t *testing.T) {
	h := inboxGroupStateTestHub(t, false)
	it := inboxGroupStateTestItem(h, "event-read", false)
	before := inboxGroupStateContentOf(it)
	report := inboxGroupStateReport(it, true, "")
	ordinary := connector.InboxThing{ID: "ordinary", Thread: "thread:ordinary"}
	batch := connector.InboxThing{Thread: inboxGroupStateThread,
		Group: &connector.ThingGroup{Batch: "batch-11", New: []string{"m11"}}, Last: "m11"}

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(),
		[]connector.InboxThing{ordinary, report, batch})
	if len(changed) != 1 || changed[0] != it {
		t.Fatalf("changed = %+v, want the read item", changed)
	}
	if len(rest) != 2 || rest[0].ID != ordinary.ID || rest[1].Group == nil || rest[1].Group.Batch != batch.Group.Batch {
		t.Fatalf("ordinary things were not preserved: %+v", rest)
	}
	if !it.Read {
		t.Fatal("the state report among ordinary things was not applied")
	}
	assertInboxGroupStateContent(t, before, it)
}

// Through collectSource, a real batch still reaches relateBatches while a state report updates its existing event.
func TestInboxGroupStateOrdinaryBatchStillGroups(t *testing.T) {
	batch := groupBatch()
	g := &groupLark{batch: batch, placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	before := inboxGroupStateContentOf(it)
	report := inboxGroupStateReport(it, true, "")
	src := sourceOf(g)
	src.Things = []connector.InboxThing{report, *batch}

	changed := h.collectSource(context.Background(), src)
	if len(changed) != 1 || changed[0] != it {
		t.Fatalf("collectSource changed = %+v, want the state report's item", changed)
	}
	h.waitGrouping()
	appends, _, fallbacks := g.counts()
	if appends != 1 || fallbacks != 1 {
		t.Fatalf("ordinary batch did not reach grouping: appends=%d fallbacks=%d", appends, fallbacks)
	}
	h.inbox.mu.Lock()
	fresh := h.inbox.items["lark:F1:1060"]
	read := it.Read
	h.inbox.mu.Unlock()
	if fresh == nil || fresh.Count != 2 {
		t.Fatalf("ordinary batch produced no grouped event: %+v", fresh)
	}
	if !read {
		t.Fatal("the state report was swallowed with the ordinary batch")
	}
	assertInboxGroupStateContent(t, before, it)
}

// A full collection round carries a state change through changed, roundWrite and the normal item save.
func TestInboxGroupStateRoundSavesChange(t *testing.T) {
	h := inboxGroupStateTestHub(t, false)
	it := inboxGroupStateTestItem(h, "event-save", false)
	before := inboxGroupStateContentOf(it)
	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	src := inboxGroupStateTestSource()
	src.Things = []connector.InboxThing{report}
	round := assistantwire.InboxRound{Epoch: "state-save", Seq: 1, Round: roundPoll,
		Names: map[string]string{src.ID: src.Name}, Sources: []assistantwire.InboxSource{src}}

	if err := h.InboxCollected(context.Background(), round); err != nil {
		t.Fatalf("InboxCollected: %v", err)
	}
	h.st.imu.Lock()
	raw := append([]byte(nil), h.st.rows[it.ID]...)
	writes := h.st.ids[it.ID]
	h.st.imu.Unlock()
	if len(raw) == 0 || writes != 1 {
		t.Fatalf("saved bytes = %d, writes = %d; want one saved state change", len(raw), writes)
	}
	var saved InboxItem
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("saved item: %v", err)
	}
	if saved.DoneAt == nil || saved.DoneBy != connector.DoneReplied {
		t.Fatalf("saved item = %+v, want done_by replied", &saved)
	}
	entries := inboxGroupStateSelfReplies(&saved)
	if len(entries) != 1 || entries[0].Via != saved.Source || entries[0].Link == nil || entries[0].Link.Position != inboxGroupStateLast {
		t.Fatalf("saved self_replied entries = %+v", entries)
	}
	assertInboxGroupStateContent(t, before, &saved)
}

func TestGoldenInboxGroupStateNotes(t *testing.T) {
	golden(t, "inbox_group_state.self_replied", prompts.RenderInboxHandlingNote(prompts.NoteSelfReplied,
		prompts.InboxHandlingNote{Owner: "示例用户", Text: "2026-11-13 14:30"}))
	golden(t, "inbox_group_state.self_read", prompts.RenderInboxHandlingNote(prompts.NoteSelfRead,
		prompts.InboxHandlingNote{Owner: "示例用户"}))
}
