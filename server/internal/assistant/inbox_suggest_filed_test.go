package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// A kind handed in after the user filed the item is refused by the tool itself (the model must not be told it was
// kept): nothing on the item or on a related one changes, no row is written, and the conversation hears the refusal.
// Only a recap still saves, and the item stays filed in the store.
func TestInboxSuggestKindAfterFiling(t *testing.T) {
	h, _, it := handledHub(t)
	// Hold the item's delivery off: tellSoon sends its pending notes (and clears the wake) on a goroutine once a request
	// has answered, which the test would race with.
	delivery, _ := h.inbox.telling.LoadOrStore(it.ID, &sync.Mutex{})
	delivery.(*sync.Mutex).Lock()
	t.Cleanup(delivery.(*sync.Mutex).Unlock)
	other := actionItem("lark:f2:1", time.Now())
	h.inbox.mu.Lock()
	h.inbox.items[other.ID] = other
	h.inbox.mu.Unlock()
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatalf("filing: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	chain, seq := len(it.Chain), it.SuggestSeq
	agent, handling := it.Agent, it.Handling
	agentState, handlingState := *it.Agent, *it.Handling
	before := string(itemState(it))
	h.inbox.mu.Unlock()
	saved := h.st.saved(it.ID)

	// A change the item would not offer and a related id: the refusal comes first, so neither leaves a trace.
	out, err := suggestTool(t, h, `{"kind":"reply","text":"late","recap":"late recap","related":["`+other.ID+`"],`+
		`"changes":[{"ref":"nope","field":"x","value":"y"}]}`)
	if err == nil || !strings.Contains(err.Error(), "already filed") {
		t.Fatalf("a late suggestion on a filed item: %q %v", out, err)
	}
	if out != "" {
		t.Fatalf("the tool said the suggestion was kept: %q", out)
	}
	h.inbox.mu.Lock()
	after := string(itemState(it))
	chain2, seq2 := len(it.Chain), it.SuggestSeq
	h.inbox.mu.Unlock()
	if after != before || chain2 != chain || seq2 != seq || other.Related != nil {
		t.Fatalf("a refused late suggestion left a trace: chain %d→%d seq %d→%d, related %+v\nbefore %s\nafter  %s",
			chain, chain2, seq, seq2, other.Related, before, after)
	}
	if !reflect.DeepEqual(*it.Agent, agentState) || !reflect.DeepEqual(*it.Handling, handlingState) {
		t.Fatalf("agent/handling moved: %+v %+v", it.Agent, it.Handling)
	}
	if h.st.saved(it.ID) != saved {
		t.Fatal("a refused late suggestion wrote a row")
	}

	// The recap alone still saves, and the item stays filed in the store too.
	if out, err := suggestTool(t, h, `{"recap":"after filing"}`); err != nil || out != "The recap is updated." {
		t.Fatalf("recap only on a filed item: %q %v", out, err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Recap != "after filing" || len(it.Chain) != chain || it.SuggestSeq != seq || it.Agent != agent || it.Handling != handling {
		t.Fatalf("recap only: recap %q chain %d seq %d agent %+v handling %+v", it.Recap, len(it.Chain), it.SuggestSeq, it.Agent, it.Handling)
	}
	if it.open() {
		t.Fatal("the recap-only call reopened the item")
	}
	if h.st.done[it.ID] == nil {
		t.Fatal("the recap saved the item open")
	}
}

// The old inbox_draft says the same refusal (not "not started for an inbox item"), while a conversation that is no
// item's still gets the old wording, and a failed save is still reported as a failed save rather than as filed.
func TestInboxDraftOnAFiledItem(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	h.inbox.file(it, time.Now(), "user")
	h.inbox.mu.Unlock()
	if _, err := h.runTool(context.Background(), "server", "s1", "inbox_draft", json.RawMessage(`{"text":"late"}`)); !errors.Is(err, errSuggestFiled) {
		t.Fatalf("inbox_draft on a filed item: %v", err)
	}
	if _, err := h.runTool(context.Background(), "server", "nobody", "inbox_draft", json.RawMessage(`{"text":"hi"}`)); err == nil ||
		err.Error() != "this conversation was not started for an inbox item" {
		t.Fatalf("a conversation no item's: %v", err)
	}
}

// The item's own conversation naming the item explicitly is refused the same way (it used to be accepted and
// silently dropped), and so is a call from a conversation the item was handed to.
func TestInboxSuggestFiledNamingTheItem(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	h.inbox.file(it, time.Now(), "user")
	h.inbox.mu.Unlock()
	if _, err := suggestTool(t, h, `{"item":"`+it.ID+`","kind":"reply","text":"late"}`); !errors.Is(err, errSuggestFiled) {
		t.Fatalf("own item named: %v", err)
	}
	h2, _, it2 := handoffHub(t)
	handOff(t, h2, it2)
	h2.inbox.mu.Lock()
	h2.inbox.file(it2, time.Now(), "user")
	h2.inbox.mu.Unlock()
	if _, err := mainSuggest(h2, "m1", `{"item":"`+it2.ID+`","kind":"reply","text":"late"}`); err == nil || !strings.Contains(err.Error(), "already filed") {
		t.Fatalf("handed conversation: %v", err)
	}
}

// An open item is unaffected by the filed guard: the same call that a filed item refuses is kept on it.
func TestInboxSuggestKindOnAnOpenItemStillKept(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"conclusion","text":"只是通知","outcome":"fyi"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Suggestion == nil || it.Suggestion.Kind != SuggestConclusion || it.Tier != TierFYI {
		t.Fatalf("open item: %+v", it.Suggestion)
	}
}
