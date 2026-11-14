package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

func chainMergeItem(at time.Time) *InboxItem {
	return &InboxItem{ID: "merge", Source: "chat", Kind: KindGroup, Title: "Topic", Sender: "Alice", Count: 1, Last: "1",
		StartedAt: at, UpdatedAt: at, Connector: &InboxConnector{Thread: "thread", URL: "https://example.com/first"}}
}

func chainMergeMessages(from, to int) []InboxChainMessage {
	out := make([]InboxChainMessage, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, InboxChainMessage{Sender: "Alice", Text: fmt.Sprintf("message-%d", i)})
	}
	return out
}

// Consecutive batches share one entry: its first id and link stay, its count and time advance, and its messages are
// the newest ten across both batches.
func TestInboxChainMergesConsecutiveReceived(t *testing.T) {
	b := newInbox()
	b.lang = func() i18n.Lang { return i18n.En }
	at := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	it := chainMergeItem(at)
	it.Count, it.Last = 8, "8"
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 8))

	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 13, "13", at.Add(time.Minute)
	it.Connector.URL = "https://example.com/second"
	b.noteReceived(it, prev, chainMergeMessages(9, 13))

	if len(it.Chain) != 1 || it.ChainSeq != 1 {
		t.Fatalf("merged chain: seq %d, %+v", it.ChainSeq, it.Chain)
	}
	e := it.Chain[0]
	wantText := i18n.T(i18n.En, "assistant-inbox_chain.received_from", i18n.Args{"count": 13, "sender": "Alice"})
	if e.ID != "c1" || e.Count != 13 || !e.At.Equal(it.UpdatedAt) || e.Sender != "Alice" || e.Text != wantText || e.Via != "chat" {
		t.Fatalf("merged entry: %+v", e)
	}
	if e.Link == nil || e.Link.URL != "https://example.com/first" {
		t.Fatalf("the first batch's link stays: %+v", e.Link)
	}
	if len(e.Messages) != chainMessagesMax || e.Messages[0].Text != "message-4" || e.Messages[chainMessagesMax-1].Text != "message-13" {
		t.Fatalf("newest messages: %+v", e.Messages)
	}
}

// A run with several senders has no one sender in its text; a run with one keeps it (the same-sender case above).
func TestInboxChainMergedReceivedClearsDifferentSender(t *testing.T) {
	b := newInbox()
	b.lang = func() i18n.Lang { return i18n.En }
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 1))
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt, it.Sender = 2, "2", at.Add(time.Minute), "Bob"
	messages := chainMergeMessages(2, 2)
	messages[0].Sender = "Bob"
	b.noteReceived(it, prev, messages)

	e := it.Chain[0]
	wantText := i18n.T(i18n.En, "assistant-inbox_chain.received", i18n.Args{"count": 2})
	if len(it.Chain) != 1 || e.Sender != "" || e.Text != wantText {
		t.Fatalf("different senders: %+v", e)
	}
}

// Another step between two batches keeps their entries separate. Stored and synthesized entries name where they happened.
func TestInboxChainReceivedDoesNotMergeAcrossTriaged(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	it.Tier = TierAction
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 1))
	b.noteTriaged(it)
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 2, "2", at.Add(time.Minute)
	b.noteReceived(it, prev, chainMergeMessages(2, 2))

	if len(it.Chain) != 3 || it.Chain[0].Kind != ChainReceived || it.Chain[1].Kind != ChainTriaged || it.Chain[2].Kind != ChainReceived {
		t.Fatalf("separate batches: %+v", it.Chain)
	}
	if it.Chain[0].Via != "chat" || it.Chain[1].Via != ViaAssistant || it.Chain[2].Via != "chat" {
		t.Fatalf("via: %+v", it.Chain)
	}

	old := chainMergeItem(at)
	old.Count, old.Last, old.Tier = 2, "2", TierFYI
	synthesized := synthesizedChain(i18n.En, old)
	if len(synthesized) != 2 || synthesized[0].Via != "chat" || synthesized[1].Via != ViaAssistant {
		t.Fatalf("synthesized via: %+v", synthesized)
	}
}

// A merged mail run still opens its first mail, not the first mail of its latest batch.
func TestInboxChainMergedMailKeepsFirstUID(t *testing.T) {
	b := newInbox()
	b.lang = func() i18n.Lang { return i18n.En }
	at := time.Now().Add(-time.Hour)
	it := &InboxItem{ID: "mail", Source: "mail", Kind: KindMail, Sender: "alice@example.com", Count: 1, Last: "3",
		UpdatedAt: at, Mail: &InboxMail{AccountID: "account", UIDs: []uint32{3}}}
	b.noteReceived(it, inboxSeen{}, []InboxChainMessage{{Sender: "alice@example.com", Text: "first"}})
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt, it.Sender = 2, "5", at.Add(time.Minute), "bob@example.com"
	it.Mail.UIDs = []uint32{3, 5}
	b.noteReceived(it, prev, []InboxChainMessage{{Sender: "bob@example.com", Text: "second"}})

	if len(it.Chain) != 1 {
		t.Fatalf("mail chain: %+v", it.Chain)
	}
	e := it.Chain[0]
	wantText := i18n.T(i18n.En, "assistant-inbox_chain.received_mail", i18n.Args{"count": 2})
	if e.Count != 2 || e.Link == nil || e.Link.UID != 3 || e.Text != wantText || len(e.Messages) != 2 {
		t.Fatalf("merged mail: %+v", e)
	}
}

// Merging is an item change just like appending: a list whose bytes are already kept shows the merged entry at once.
func TestInboxChainMergedReceivedInvalidatesList(t *testing.T) {
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 1))
	h.inbox.mu.Unlock()

	listed := func() InboxItem {
		t.Helper()
		raws, _, ok := h.inboxListRaw(true)
		if !ok {
			t.Fatal("list encoding failed")
		}
		for _, raw := range raws {
			var got InboxItem
			if json.Unmarshal(raw, &got) == nil && got.ID == it.ID {
				return got
			}
		}
		t.Fatal("item missing from list")
		return InboxItem{}
	}
	if got := listed(); len(got.Chain) != 1 || got.Chain[0].Count != 1 {
		t.Fatalf("before merge: %+v", got.Chain)
	}

	h.inbox.mu.Lock()
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 2, "2", at.Add(time.Minute)
	h.inbox.noteReceived(it, prev, chainMergeMessages(2, 2))
	h.inbox.mu.Unlock()
	if got := listed(); len(got.Chain) != 1 || got.Chain[0].Count != 2 || len(got.Chain[0].Messages) != 2 {
		t.Fatalf("list kept the entry before the merge: %+v", got.Chain)
	}
}

// A merge at the chain's entry limit changes its last entry in place rather than adding another.
func TestInboxChainMergeDoesNotGrowFullChain(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	it.Count, it.Last, it.ChainSeq = 2, "2", chainMax
	it.Chain = make([]InboxChainEntry, chainMax)
	for i := 0; i < chainMax-1; i++ {
		it.Chain[i] = InboxChainEntry{ID: fmt.Sprintf("c%d", i+1), Kind: ChainSent, At: at.Add(-time.Hour)}
	}
	it.Chain[chainMax-1] = InboxChainEntry{ID: "c50", Kind: ChainReceived, Count: 1, Sender: "Alice", At: at.Add(-time.Minute),
		Link: &InboxChainLink{Type: LinkSource, URL: "https://example.com/first"}}
	b.noteReceived(it, inboxSeen{last: "1", count: 1}, chainMergeMessages(2, 2))

	if len(it.Chain) != chainMax || it.ChainSeq != chainMax {
		t.Fatalf("full chain grew: len %d, seq %d", len(it.Chain), it.ChainSeq)
	}
	if e := it.Chain[chainMax-1]; e.ID != "c50" || e.Count != 2 {
		t.Fatalf("last entry: %+v", e)
	}
}

// Supervisor signals use the item count, not a chain id that a merge keeps: every batch is counted once, including the
// first batch after the process-local cursor is restored from a stored item.
func TestInboxChainMergeSupervisorCounts(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	it.Kind = KindDM
	it.Agent = &InboxAgent{TodoID: "todo"}
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 1))
	if got, ok := inboxSignalOf(it, true); !ok || got.Inbox == nil || got.Inbox.Count != 1 || it.Agent.signaled != 1 {
		t.Fatalf("first signal: %+v, ok %v, watermark %d", got.Inbox, ok, it.Agent.signaled)
	}

	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 3, "3", at.Add(time.Minute)
	b.noteReceived(it, prev, chainMergeMessages(2, 3))
	if len(it.Chain) != 1 {
		t.Fatalf("batches did not merge: %+v", it.Chain)
	}
	if got, ok := inboxSignalOf(it, false); !ok || got.Inbox == nil || got.Inbox.Count != 2 || it.Agent.signaled != 3 {
		t.Fatalf("merged signal: %+v, ok %v, watermark %d", got.Inbox, ok, it.Agent.signaled)
	}

	_, it.Agent.signaled = receivedSince(it, 0, false) // as loading a stored item leaves it (inboxBook.stored)
	it.Agent.signaledOf = it.Last
	prev = seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 5, "5", at.Add(2*time.Minute)
	b.noteReceived(it, prev, chainMergeMessages(4, 5))
	if got, ok := inboxSignalOf(it, false); !ok || got.Inbox == nil || got.Inbox.Count != 2 || it.Agent.signaled != 5 {
		t.Fatalf("first signal after loading: %+v, ok %v, watermark %d", got.Inbox, ok, it.Agent.signaled)
	}
}

// A merged Feishu run opens the chat at its first batch's first message, not at the first message of its latest batch.
func TestInboxChainMergedLarkKeepsFirstPosition(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	it := &InboxItem{ID: "lark:1", Source: "lark", Kind: KindDM, Title: "Alice", Sender: "Alice", Count: 3, Last: "102",
		StartedAt: at, UpdatedAt: at, Lark: &InboxLark{FeedID: "f", StartPosition: "100"}}
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 3))
	if e := it.Chain[0]; e.Link == nil || e.Link.Position != "100" || e.Count != 3 {
		t.Fatalf("first batch: %+v", e)
	}
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 5, "104", at.Add(time.Minute)
	b.noteReceived(it, prev, chainMergeMessages(4, 5))
	if len(it.Chain) != 1 {
		t.Fatalf("the batches did not merge: %+v", it.Chain)
	}
	e := it.Chain[0]
	// The latest batch alone would start at 103; the run keeps where it began.
	if e.Count != 5 || e.Link == nil || e.Link.Position != "100" {
		t.Fatalf("merged entry: %+v, link %+v", e, e.Link)
	}
}

// A run whose senders differed has no sender; a later batch from the first sender does not put one back.
func TestInboxChainMergedReceivedSenderStaysCleared(t *testing.T) {
	b := newInbox()
	b.lang = func() i18n.Lang { return i18n.En }
	at := time.Now().Add(-time.Hour)
	it := chainMergeItem(at)
	b.noteReceived(it, inboxSeen{}, chainMergeMessages(1, 1))
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt, it.Sender = 2, "2", at.Add(time.Minute), "Bob"
	b.noteReceived(it, prev, []InboxChainMessage{{Sender: "Bob", Text: "b"}})
	prev = seenOf(it)
	it.Count, it.Last, it.UpdatedAt, it.Sender = 3, "3", at.Add(2*time.Minute), "Alice"
	b.noteReceived(it, prev, []InboxChainMessage{{Sender: "Alice", Text: "a"}})
	e := it.Chain[0]
	if len(it.Chain) != 1 || e.Sender != "" || e.Text != i18n.T(i18n.En, "assistant-inbox_chain.received", i18n.Args{"count": 3}) {
		t.Fatalf("sender came back: %+v", e)
	}
}

// Chains written before the merge keep their consecutive received entries as they are: only the newest entry takes what
// comes in now.
func TestInboxChainOldConsecutiveReceivedsStay(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	it := &InboxItem{ID: "old", Source: "chat", Kind: KindGroup, Sender: "Alice", Count: 3, Last: "3", StartedAt: at, UpdatedAt: at,
		ChainSeq: 2, Chain: []InboxChainEntry{
			{ID: "c1", Kind: ChainReceived, Count: 1, Sender: "Alice", At: at},
			{ID: "c2", Kind: ChainReceived, Count: 2, Sender: "Alice", At: at.Add(time.Minute)},
		}}
	prev := seenOf(it)
	it.Count, it.Last, it.UpdatedAt = 5, "5", at.Add(2*time.Minute)
	b.noteReceived(it, prev, chainMergeMessages(4, 5))
	if len(it.Chain) != 2 || it.Chain[0].Count != 1 || it.Chain[1].ID != "c2" || it.Chain[1].Count != 4 {
		t.Fatalf("old run rewritten: %+v", it.Chain)
	}
}

// An item from before chains gets its synthesized received entry, and a batch that comes in later adds to that entry
// rather than a second one.
func TestInboxChainMergesIntoSynthesizedReceived(t *testing.T) {
	b := newInbox()
	at := time.Now().Add(-time.Hour)
	old := &InboxItem{ID: "pre", Source: "chat", Kind: KindGroup, Title: "Topic", Sender: "Alice", Count: 2, Last: "2",
		StartedAt: at, UpdatedAt: at, Connector: &InboxConnector{Thread: "t"}}
	b.stored(old)
	if len(old.Chain) != 1 || old.Chain[0].Via != "chat" {
		t.Fatalf("backfill: %+v", old.Chain)
	}
	prev := seenOf(old)
	old.Count, old.Last, old.UpdatedAt = 4, "4", at.Add(time.Minute)
	b.noteReceived(old, prev, chainMergeMessages(3, 4))
	if len(old.Chain) != 1 || old.ChainSeq != 1 {
		t.Fatalf("backfilled entry not merged into: seq %d, %+v", old.ChainSeq, old.Chain)
	}
	if e := old.Chain[0]; e.ID != "c1" || e.Count != 4 || len(e.Messages) != 2 {
		t.Fatalf("merged backfill: %+v", e)
	}
}

// The supervisor's watermark is the item count, which mail sets to its unread count: when mails were read elsewhere
// between two batches the count can fall below it, and the next batch is then counted as one (the known deviation, at
// least one message, and the signal still goes out).
func TestInboxChainMergeSupervisorMailCountFalls(t *testing.T) {
	it := &InboxItem{ID: "mail:1", Source: "mail", Kind: KindMail, Count: 5, Last: "5"}
	if _, seq := receivedSince(it, 0, false); seq != 5 {
		t.Fatalf("watermark %d", seq)
	}
	it.Count, it.Last = 3, "6" // two mails read elsewhere, one new
	if n, seq := receivedSince(it, 5, false); n != 1 || seq != 3 {
		t.Fatalf("after the count fell: %d messages, watermark %d", n, seq)
	}
}
