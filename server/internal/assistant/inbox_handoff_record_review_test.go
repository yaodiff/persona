package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Review of the record-only hand-off (inbox_handoff.go recordHandoff, docs/assistant/inbox.md "Item actions"): what the
// clients' POST /api/v1/inbox/{id}/handoff with a session must do, beyond inbox_handoff_record_test.go.

// A recorded hand-off is what the item lists, through both the list and the item's own GET: the session, no op, and the
// chain entry (the list memo must have been forgotten, inbox_list_memo.go).
func TestReviewRecordHandoffInTheListAndTheItem(t *testing.T) {
	h, _, it := handoffHub(t)
	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "listed-conversation", "conversation", "报价后续", "20000000-0000-4000-8000-000000000001"))

	code, listed := inboxCall(t, h.routes(), "GET", "/api/v1/inbox?sources=all", "")
	if code != 200 {
		t.Fatalf("list: %d %v", code, listed)
	}
	found := false
	for _, raw := range listed["items"].([]any) {
		entry := raw.(map[string]any)
		if entry["id"] != it.ID {
			continue
		}
		found = true
		hs, _ := entry["handoffs"].([]any)
		if len(hs) != 1 || hs[0].(map[string]any)["session_id"] != "listed-conversation" || hs[0].(map[string]any)["op"] != nil {
			t.Fatalf("listed hand-offs: %v", entry["handoffs"])
		}
		chain, _ := entry["chain"].([]any)
		if len(chain) == 0 || chain[len(chain)-1].(map[string]any)["kind"] != ChainHandling {
			t.Fatalf("listed chain: %v", entry["chain"])
		}
	}
	if !found {
		t.Fatal("the item is not in the list")
	}

	code, one := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/"+it.ID, "")
	hs, _ := one["handoffs"].([]any)
	if code != 200 || len(hs) != 1 || hs[0].(map[string]any)["session_id"] != "listed-conversation" {
		t.Fatalf("the item: %d %v", code, one)
	}
}

// An item the user already filed keeps being filed: recording a hand-off on it records the step and nothing else.
func TestReviewRecordHandoffOnAFiledItem(t *testing.T) {
	h, _, it := handoffHub(t)
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatalf("done: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	doneAt, doneBy := it.DoneAt, it.DoneBy
	h.inbox.mu.Unlock()
	if doneAt == nil {
		t.Fatal("the fixture did not file the item")
	}
	out := mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", "filed-conversation", "conversation", "已结束的事", "20000000-0000-4000-8000-000000000002"))
	h.inbox.mu.Lock()
	after, by, handoffs, chain := it.DoneAt, it.DoneBy, len(it.Handoffs), len(it.Chain)
	h.inbox.mu.Unlock()
	if after == nil || !after.Equal(*doneAt) || by != doneBy {
		t.Fatalf("recording changed the filing: %v %q (was %v %q)", after, by, doneAt, doneBy)
	}
	if handoffs != 1 || chain == 0 {
		t.Fatalf("the record: %d hand-offs, %d chain entries", handoffs, chain)
	}
	if item, _ := out["item"].(map[string]any); item["status"] != nil || item["done_at"] == nil {
		t.Fatalf("listing a filed item after the record: %v", out["item"])
	}
}

// The item keeps its latest hand-offs, the record-only ones included, and each session of the same kind gets its own
// chain entry (the de-duplication is per session, not per kind).
func TestReviewRecordHandoffKeepsTheLatest(t *testing.T) {
	h, _, it := handoffHub(t)
	for i := range handoffsMax + 2 {
		session := "latest-" + string(rune('a'+i))
		mustRecordSessionHandoff(t, h, it.ID,
			recordedHandoffBody("server", session, "conversation", "第几次", "20000000-0000-4000-8000-00000000010"+string(rune('0'+i))))
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(it.Handoffs) != handoffsMax {
		t.Fatalf("%d hand-offs kept", len(it.Handoffs))
	}
	if last := it.Handoffs[len(it.Handoffs)-1].SessionID; last != "latest-"+string(rune('a'+handoffsMax+1)) {
		t.Fatalf("the latest hand-off is %q", last)
	}
	entries := 0
	for _, e := range it.Chain {
		if e.Kind == ChainHandling && e.Link != nil && strings.HasPrefix(e.Link.SessionID, "latest-") {
			entries++
		}
	}
	if entries != handoffsMax+2 {
		t.Fatalf("%d chain entries for %d sessions", entries, handoffsMax+2)
	}
}

// An Agent recorded this way may write back to the item as a conversation may (the apps record an Agent target with
// kind agent): its suggestion names it and the chain links it.
func TestReviewRecordHandoffAgentWritesBack(t *testing.T) {
	h, _, it := handoffHub(t)
	const agent = "recorded-agent-write-back"
	mustRecordSessionHandoff(t, h, it.ID,
		recordedHandoffBody("server", agent, "agent", "排查报价", "20000000-0000-4000-8000-000000000003"))
	if _, err := mainSuggest(h, agent, `{"item":"lark:f1:100","kind":"reply","text":"周五给你","recap":"问清了"}`); err != nil {
		t.Fatalf("the recorded Agent could not write back: %v", err)
	}
	h.inbox.mu.Lock()
	g := it.Suggestion
	e := it.Chain[len(it.Chain)-1]
	handling := it.Handling
	h.inbox.mu.Unlock()
	if g == nil || g.Conversation == nil || g.Conversation.MachineID != "server" || g.Conversation.SessionID != agent {
		t.Fatalf("the suggestion does not name the Agent: %+v", g)
	}
	if e.Kind != ChainHandling || e.Link == nil || e.Link.SessionID != agent {
		t.Fatalf("the write-back's chain entry: %+v", e)
	}
	if handling != nil {
		t.Fatalf("recording and writing back took a processing conversation: %+v", handling)
	}
}

// A session object with nothing in it is a refused record, as a missing one is: nothing recorded, nothing sent.
func TestReviewRecordHandoffEmptySessionObject(t *testing.T) {
	h, _, it := handoffHub(t)
	beforeActivity := len(recordedSessionActivity(h.f))
	code, out := callRecordedHandoff(t, h, it.ID, map[string]any{
		"session":   map[string]any{},
		"kind":      "agent",
		"client_id": "20000000-0000-4000-8000-000000000004",
	})
	if code != 400 {
		t.Fatalf("an empty session: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	handoffs, chain := len(it.Handoffs), len(it.Chain)
	h.inbox.mu.Unlock()
	if handoffs != 0 || chain != 0 {
		t.Fatalf("an empty session recorded %d hand-offs and %d chain entries", handoffs, chain)
	}
	if got := len(recordedSessionActivity(h.f)); got != beforeActivity {
		t.Fatalf("an empty session sent, started, or woke a session: %v", got)
	}
}

// The chain text the manager writes is translated again for a client asking in another language (i18n.InboxJSON at the
// response boundary): each new key round-trips, the title kept.
func TestReviewRecordHandoffTextRoundTrips(t *testing.T) {
	for _, key := range []string{"assistant-inbox_chain.handed_agent", "assistant-inbox_chain.handed_conversation",
		"assistant-inbox_chain.handed_agent_generic", "assistant-inbox_chain.handed_conversation_generic"} {
		args := i18n.Args{"title": "张三 × A+B"}
		for _, from := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			text := i18n.T(from, key, args)
			for _, to := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				want := i18n.T(to, key, args)
				if got := i18n.InboxText(to, text); got != want {
					t.Errorf("%s -> %s %s: %q, want %q", from, to, key, got, want)
				}
			}
		}
	}
}
