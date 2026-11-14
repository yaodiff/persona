package assistant

// Review tests for the grouping connector's read/replied state reports (inbox_group_state.go, docs/assistant/inbox.md
// "Following up", "Read and replied state from a grouping connector").

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// A "replied" report that is Persona's own confirmed reply, not the user's: the source cannot tell them apart, so the
// manager must (settleThing's ownSend guard). The event keeps waiting on the other side, with no self_replied step and
// no note to the conversation.
func TestInboxGroupStateOwnSendIsNotTheUsersReply(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	it := inboxGroupStateTestItem(h, "event-own-send", true)
	// What a confirmed reply leaves: waiting on the other side, SentOf = the item's Last.
	it.Handling.State = HandlingWaiting
	it.Agent.SentOf = it.Last
	beforeChain := len(it.Chain)
	report := inboxGroupStateReport(it, false, connector.DoneReplied)

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	if len(rest) != 0 {
		t.Fatalf("state reports left as ordinary things: %+v", rest)
	}
	if len(changed) != 0 {
		t.Fatalf("Persona's own reply was taken as the user's: %+v", changed)
	}
	if !it.open() || it.Handling.State != HandlingWaiting {
		t.Fatalf("handling = %+v, want still waiting", it.Handling)
	}
	if len(it.Chain) != beforeChain || len(inboxGroupStateSelfReplies(it)) != 0 {
		t.Fatalf("a self_replied step was recorded for Persona's own reply: %+v", it.Chain)
	}
	if it.Agent.Wake || len(it.Agent.Pending) != 0 {
		t.Fatalf("the conversation was woken for Persona's own reply: %+v", it.Agent)
	}
}

// The user's reply after Persona's own (a new incoming message cleared SentOf) is theirs and is recorded.
func TestInboxGroupStateSelfRepliedAfterOwnSend(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	it := inboxGroupStateTestItem(h, "event-after-own-send", true)
	it.Handling.State = HandlingWaiting
	it.Agent.SentOf = "m9" // the previous, incoming position Persona answered
	report := inboxGroupStateReport(it, false, connector.DoneReplied)

	rest, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if len(inboxGroupStateSelfReplies(it)) != 1 || it.Handling.State != HandlingLooking {
		t.Fatalf("the user's reply was not recorded: %+v / %+v", it.Chain, it.Handling)
	}
}

// A row that cannot be written gives the item back what it held, and the next round (the connector hands the state
// again) applies it once.
func TestInboxGroupStateSaveFailureIsUndoneAndRetried(t *testing.T) {
	h := inboxGroupStateTestHub(t, false)
	it := inboxGroupStateTestItem(h, "event-save-fails", false)
	beforeChain := len(it.Chain)
	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	src := inboxGroupStateTestSource()
	src.Things = []connector.InboxThing{report}
	round := assistantwire.InboxRound{Epoch: "state-save-fails", Seq: 1, Round: roundPoll,
		Names: map[string]string{src.ID: src.Name}, Sources: []assistantwire.InboxSource{src}}

	h.st.writeFails(true)
	if err := h.InboxCollected(context.Background(), round); err != nil {
		t.Fatalf("InboxCollected: %v", err)
	}
	h.inbox.mu.Lock()
	open, doneBy, chain := it.open(), it.DoneBy, len(it.Chain)
	h.inbox.mu.Unlock()
	if !open || doneBy != "" || chain != beforeChain {
		t.Fatalf("a failed save was left in memory: open=%v done_by=%q chain=%d/%d", open, doneBy, chain, beforeChain)
	}

	h.st.writeFails(false)
	round.Seq = 2
	if err := h.InboxCollected(context.Background(), round); err != nil {
		t.Fatalf("InboxCollected: %v", err)
	}
	h.inbox.mu.Lock()
	open, doneBy = it.open(), it.DoneBy
	replies := len(inboxGroupStateSelfReplies(it))
	h.inbox.mu.Unlock()
	if open || doneBy != connector.DoneReplied || replies != 1 {
		t.Fatalf("the retried state was not applied once: open=%v done_by=%q replies=%d", open, doneBy, replies)
	}
	if !h.st.saved(it.ID) {
		t.Fatal("the retried state was not saved")
	}
}

// A reply from a notification (read false, replied true), then the chat is opened: the read report at the same position
// must not queue a second note that says the owner did not reply here. The conversation gets one story.
func TestInboxGroupStateReadAfterRepliedDoesNotRepeatTheNote(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	h.f.hello.Owner = "示例用户"
	it := inboxGroupStateTestItem(h, "event-replied-then-read", true)
	replied := inboxGroupStateReport(it, false, connector.DoneReplied)
	read := inboxGroupStateReport(it, true, "")
	if _, changed := h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{replied}); len(changed) != 1 {
		t.Fatalf("the reply was not applied: %+v", changed)
	}
	handling := it.Handling.State
	_, _ = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{read})
	h.deliverPending(context.Background())
	notes := notesOf(h.f, "session-1")
	if len(notes) != 1 || strings.Contains(notes[0], "看过这件事的最新消息") {
		t.Fatalf("the conversation got both stories: %q", notes)
	}
	if it.Handling.State != handling || it.Agent.Wake {
		t.Fatalf("the read moved the event again: handling=%+v agent=%+v", it.Handling, it.Agent)
	}
	if len(inboxGroupStateSelfReplies(it)) != 1 {
		t.Fatalf("self_replied entries = %+v", it.Chain)
	}
}

// After Persona sends a confirmed reply the chat is no longer unread, so the connector reports the event read at the
// same position (its own rule). That is not the owner reading in the source: Read is set, but the event keeps waiting
// on the other side, with no self_read note and no wake.
func TestInboxGroupStateOwnSendReadIsNotTheOwnersRead(t *testing.T) {
	h := inboxGroupStateTestHub(t, true)
	h.f.hello.Owner = "示例用户"
	it := inboxGroupStateTestItem(h, "event-own-send-read", true)
	it.Handling.State = HandlingWaiting
	it.Agent.SentOf = it.Last
	report := inboxGroupStateReport(it, true, "")

	_, _ = h.applyGroupStates(context.Background(), inboxGroupStateTestSource(), []connector.InboxThing{report})
	h.deliverPending(context.Background())
	if !it.Read {
		t.Fatalf("the read of Persona's own reply was not recorded: %+v", it)
	}
	if it.Handling.State != HandlingWaiting || it.Agent.Wake || len(it.Agent.Pending) != 0 {
		t.Fatalf("the waiting event was woken and moved: handling=%+v agent=%+v", it.Handling, it.Agent)
	}
	if notes := notesOf(h.f, "session-1"); len(notes) != 0 {
		t.Fatalf("the conversation was told the owner read it: %q", notes)
	}
}
