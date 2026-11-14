package assistant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Cases around a reported step that the first pass leaves open: what a refused change leaves behind, how `by` is
// read, which chain entries the duplicate check compares against, a reported-only turn's handling state, and the
// prefix a reader in another language sees.

// A call whose document change the item does not offer keeps nothing at all, the reported step included: the refusal
// comes before any write.
func TestInboxReportedWithRefusedChangeKeepsNothing(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Doc = &InboxDoc{Rows: []DocRow{{Ref: "r1", Status: []DocStatus{{Field: "状态", Options: []DocOption{{Name: "完成"}}}}}}}
	before := len(it.Chain)
	h.inbox.mu.Unlock()
	_, refused, _, _, _ := h.suggestFrom(t.Context(), "s1", suggestIn{
		Kind:     SuggestReply,
		Text:     "改好了",
		Changes:  []DocChange{{Ref: "r1", Field: "状态", Value: "不存在的值"}},
		Reported: &suggestReported{Text: "改了表格的状态"},
	})
	if refused == nil {
		t.Fatal("a change the item does not offer was taken")
	}
	h.inbox.mu.Lock()
	after, kinds := len(it.Chain), chainKinds(it)
	h.inbox.mu.Unlock()
	if after != before {
		t.Fatalf("a refused change added chain entries: %v", kinds)
	}
	for _, k := range kinds {
		if k == ChainReported {
			t.Fatalf("a refused change recorded the reported step: %v", kinds)
		}
	}
}

// `by` is read past its whitespace, and an empty one stays the assistant.
func TestInboxReportedTrimsBy(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Checked the request", "by": "  agent  "},
	}); err != nil {
		t.Fatal(err)
	}
	if e := reportedLast(t, h, it); e.Kind != ChainReported || e.Via != ViaAgent {
		t.Fatalf("reported step: %+v", e)
	}
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Confirmed the date", "by": "   "},
	}); err != nil {
		t.Fatal(err)
	}
	if e := reportedLast(t, h, it); e.Via != ViaAssistant {
		t.Fatalf("blank by: %+v", e)
	}
}

// The duplicate check reads the statement, not who reported it: the same words from the other side are one step.
func TestInboxReportedDeduplicatesAcrossBy(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Sent the corrected quote", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	before := len(reportedChain(h, it))
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Sent the corrected quote", "by": ViaAssistant},
	}); err != nil {
		t.Fatal(err)
	}
	if after := len(reportedChain(h, it)); after != before {
		t.Fatalf("the same statement from the other side was recorded twice: %d → %d", before, after)
	}
}

// A reported-only turn puts a waiting item back where it was, as a recap-only turn does, and adds no suggestion.
func TestInboxReportedOnlyKeepsWaiting(t *testing.T) {
	h, _, it := handledHub(t)
	now := time.Now().UTC()
	at := now.Add(4 * time.Hour)
	h.inbox.mu.Lock()
	h.inbox.waitForOtherSide(it, &InboxFollow{Who: "张三", CheckAt: &at}, ViaAssistant, now, time.UTC, false)
	// The look the turn came from: the conversation is looking, and its place before was waiting.
	it.Handling.State = HandlingLooking
	it.Agent.Back, it.Agent.Wake = HandlingWaiting, true
	h.inbox.mu.Unlock()
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Called the supplier", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	if e := reportedLast(t, h, it); e.Kind != ChainReported || e.Detail != "Called the supplier" {
		t.Fatalf("reported step: %+v", e)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake || back != HandlingWaiting {
		t.Fatalf("handling %q, wake %v, back %q, want it back to waiting as a recap-only turn leaves it", state, wake, back)
	}
	h.inbox.mu.Lock()
	follow, suggestion := it.Follow, it.Suggestion
	h.inbox.mu.Unlock()
	if follow == nil || follow.CheckAt == nil || suggestion != nil {
		t.Fatalf("follow %+v, suggestion %+v", follow, suggestion)
	}
}

// A reader in another language sees the prefix in their own language and the statement untouched, both in the entry's
// text and in the detail the apps draw on their own.
func TestInboxReportedPrefixLocalizedAtTheBoundary(t *testing.T) {
	it := InboxItem{ID: "reported-lang", Source: "mail", Kind: KindMail, Tier: TierAction,
		Chain: []InboxChainEntry{{ID: "c1", Kind: ChainReported, At: time.Now().UTC(),
			Text:   i18n.T(i18n.En, "assistant-inbox_chain.reported", i18n.Args{"text": "Sent the corrected quote"}),
			Detail: "Sent the corrected quote", Via: ViaAgent}}}
	raw, err := json.Marshal(it.listed())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.ZhHans, "助手说：Sent the corrected quote"},
		{i18n.Ja, "アシスタントによると：Sent the corrected quote"},
	} {
		var got struct {
			Chain []InboxChainEntry `json:"chain"`
		}
		if err := json.Unmarshal(i18n.InboxJSON(tc.lang, raw), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Chain) != 1 {
			t.Fatalf("%s: chain %+v", tc.lang, got.Chain)
		}
		if got.Chain[0].Text != tc.want {
			t.Errorf("%s: text %q, want %q", tc.lang, got.Chain[0].Text, tc.want)
		}
		if got.Chain[0].Detail != "Sent the corrected quote" || got.Chain[0].Via != ViaAgent {
			t.Errorf("%s: detail %q, via %q", tc.lang, got.Chain[0].Detail, got.Chain[0].Via)
		}
	}
}
