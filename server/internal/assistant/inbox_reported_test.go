package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

func reportedJSON(t *testing.T, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func reportedSuggest(t *testing.T, h *inboxHub, args map[string]any) (string, error) {
	t.Helper()
	return suggestTool(t, h, reportedJSON(t, args))
}

func reportedChain(h *inboxHub, it *InboxItem) []InboxChainEntry {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return append([]InboxChainEntry(nil), it.Chain...)
}

func reportedLast(t *testing.T, h *inboxHub, it *InboxItem) InboxChainEntry {
	t.Helper()
	chain := reportedChain(h, it)
	if len(chain) == 0 {
		t.Fatal("the item has no chain entries")
	}
	return chain[len(chain)-1]
}

func reportedWrites(h *inboxHub, id string) int {
	h.st.imu.Lock()
	defer h.st.imu.Unlock()
	return h.st.ids[id]
}

func reportedSnapshot(h *inboxHub, it *InboxItem) (string, int) {
	h.inbox.mu.Lock()
	before := string(itemState(it))
	h.inbox.mu.Unlock()
	return before, reportedWrites(h, it.ID)
}

func reportedAssertUnchanged(t *testing.T, h *inboxHub, it *InboxItem, before string, writes int) {
	t.Helper()
	h.inbox.mu.Lock()
	after := string(itemState(it))
	h.inbox.mu.Unlock()
	if after != before {
		t.Fatalf("a refused reported step changed the item:\nbefore: %s\nafter:  %s", before, after)
	}
	if got := reportedWrites(h, it.ID); got != writes {
		t.Fatalf("a refused reported step wrote a row: writes %d → %d", writes, got)
	}
}

// A reported step is enough on its own; omitting by records the assistant.
func TestInboxReportedOnlyDefaultsToAssistant(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.lang = func() i18n.Lang { return i18n.En }
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Requester confirmed the schedule"},
	}); err != nil {
		t.Fatal(err)
	}
	e := reportedLast(t, h, it)
	if e.Kind != ChainReported || e.Via != ViaAssistant || e.Detail != "Requester confirmed the schedule" ||
		e.Text != "Assistant says: Requester confirmed the schedule" {
		t.Fatalf("reported step: %+v", e)
	}
	h.inbox.mu.Lock()
	open, suggestion := it.open(), it.Suggestion
	h.inbox.mu.Unlock()
	if !open || suggestion != nil {
		t.Fatalf("a reported-only call changed the item's work: open %v, suggestion %+v", open, suggestion)
	}
}

// Both by values share the same exact prefix in each language; by controls only via.
func TestInboxReportedLocalizedText(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		by   string
		want string
	}{
		{"en assistant", i18n.En, ViaAssistant, "Assistant says: Requester confirmed"},
		{"en agent", i18n.En, ViaAgent, "Assistant says: Requester confirmed"},
		{"zh-Hans assistant", i18n.ZhHans, ViaAssistant, "助手说：Requester confirmed"},
		{"zh-Hans agent", i18n.ZhHans, ViaAgent, "助手说：Requester confirmed"},
		{"ja assistant", i18n.Ja, ViaAssistant, "アシスタントによると：Requester confirmed"},
		{"ja agent", i18n.Ja, ViaAgent, "アシスタントによると：Requester confirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.lang = func() i18n.Lang { return tc.lang }
			if _, err := reportedSuggest(t, h, map[string]any{
				"reported": map[string]any{"text": "Requester confirmed", "by": tc.by},
			}); err != nil {
				t.Fatal(err)
			}
			e := reportedLast(t, h, it)
			if e.Kind != ChainReported || e.Via != tc.by || e.Detail != "Requester confirmed" || e.Text != tc.want {
				t.Fatalf("reported step: %+v, want text %q and via %q", e, tc.want, tc.by)
			}
		})
	}
}

// Reported text is trimmed and flattened before it is clipped to 120 runes with the usual ellipsis.
func TestInboxReportedNormalizesAndClipsText(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		want    string
		clipped bool
	}{
		{"one line", " \n First line\r\n\t second   line \t ", "First line second line", false},
		{"120 runes", strings.Repeat("界", 140), strings.Repeat("界", 120) + "…", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.lang = func() i18n.Lang { return i18n.En }
			if _, err := reportedSuggest(t, h, map[string]any{
				"reported": map[string]any{"text": tc.text},
			}); err != nil {
				t.Fatal(err)
			}
			e := reportedLast(t, h, it)
			if e.Kind != ChainReported || e.Detail != tc.want || e.Text != "Assistant says: "+tc.want {
				t.Fatalf("reported step: %+v, want detail %q", e, tc.want)
			}
			if tc.clipped && (len([]rune(e.Detail)) != 121 || !strings.HasSuffix(e.Detail, "…")) {
				t.Fatalf("clipped detail has %d runes and no final ellipsis: %q", len([]rune(e.Detail)), e.Detail)
			}
		})
	}
}

// A reported step can travel with a recap and the item's assistant-written name and summary.
func TestInboxReportedWithRecapAndNaming(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"recap":    "The schedule is confirmed",
		"title":    "Schedule confirmed",
		"summary":  "The requester confirmed Friday",
		"reported": map[string]any{"text": "Checked with the requester", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	e := reportedLast(t, h, it)
	if e.Kind != ChainReported || e.Via != ViaAgent || e.Detail != "Checked with the requester" {
		t.Fatalf("reported step: %+v", e)
	}
	h.inbox.mu.Lock()
	recap, title, summary, titleBy := it.Recap, it.Title, it.Summary, it.TitleBy
	h.inbox.mu.Unlock()
	if recap != "The schedule is confirmed" || title != "Schedule confirmed" ||
		summary != "The requester confirmed Friday" || titleBy != TitleByAssistant {
		t.Fatalf("recap %q, title %q, summary %q, title_by %q", recap, title, summary, titleBy)
	}
}

// A kind and a reported step are both kept by the same call.
func TestInboxReportedWithKind(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"kind":     SuggestReply,
		"text":     "Friday works for me",
		"reported": map[string]any{"text": "Checked the calendar", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	chain := reportedChain(h, it)
	reported, handling := 0, 0
	for _, e := range chain {
		switch e.Kind {
		case ChainReported:
			reported++
			if e.Detail != "Checked the calendar" || e.Via != ViaAgent {
				t.Fatalf("reported step: %+v", e)
			}
		case ChainHandling:
			handling++
		}
	}
	if reported != 1 || handling == 0 {
		t.Fatalf("chain after kind and reported: %v", chainKinds(it))
	}
	h.inbox.mu.Lock()
	suggestion := it.Suggestion
	h.inbox.mu.Unlock()
	if suggestion == nil || suggestion.Kind != SuggestReply || suggestion.Text != "Friday works for me" {
		t.Fatalf("suggestion: %+v", suggestion)
	}
}

// Follow remains separate from kind, and can accompany a reported step.
func TestInboxReportedWithFollow(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"follow":   map[string]any{"who": "Requester", "next": "Wait for the final date", "check_in": "4h"},
		"reported": map[string]any{"text": "Asked for the final date"},
	}); err != nil {
		t.Fatal(err)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "Requester" || f.Next != "Wait for the final date" {
		t.Fatalf("follow: %+v", f)
	}
	reported, waiting := 0, 0
	for _, e := range reportedChain(h, it) {
		switch e.Kind {
		case ChainReported:
			reported++
			if e.Detail != "Asked for the final date" || e.Via != ViaAssistant {
				t.Fatalf("reported step: %+v", e)
			}
		case ChainWaiting:
			waiting++
		}
	}
	if reported != 1 || waiting != 1 {
		t.Fatalf("chain after follow and reported: %v", chainKinds(it))
	}
}

// A conclusion that files the item records the reported step before the conclusion and done entries.
func TestInboxReportedBeforeConclusionDone(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"kind":     SuggestConclusion,
		"text":     "Nothing more is needed",
		"outcome":  "done",
		"reported": map[string]any{"text": "Checked the final state", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	chain := reportedChain(h, it)
	if len(chain) < 3 {
		t.Fatalf("chain is too short: %+v", chain)
	}
	reported, concluded, done := chain[len(chain)-3], chain[len(chain)-2], chain[len(chain)-1]
	if reported.Kind != ChainReported || reported.Via != ViaAgent || reported.Detail != "Checked the final state" ||
		concluded.Kind != ChainConcluded || done.Kind != ChainDone {
		t.Fatalf("chain suffix: reported %+v, concluded %+v, done %+v", reported, concluded, done)
	}
	h.inbox.mu.Lock()
	open, suggestion := it.open(), it.Suggestion
	h.inbox.mu.Unlock()
	if open || suggestion != nil {
		t.Fatalf("done conclusion: open %v, suggestion %+v", open, suggestion)
	}
}

// Every call carrying reported is refused on a filed item without a mutation or a save. A kind alone keeps its old error.
func TestInboxReportedFiledRefusal(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	h.inbox.file(it, time.Now().UTC(), "user")
	h.inbox.mu.Unlock()
	before, writes := reportedSnapshot(h, it)

	for _, tc := range []struct {
		name            string
		args            map[string]any
		existingRefusal bool
	}{
		{"reported only", map[string]any{
			"reported": map[string]any{"text": "A late step"},
		}, false},
		{"recap and reported", map[string]any{
			"recap":    "Must not replace the recap",
			"reported": map[string]any{"text": "A late step"},
		}, false},
		{"kind and reported", map[string]any{
			"kind":     SuggestReply,
			"text":     "A late reply",
			"reported": map[string]any{"text": "A late step"},
		}, true},
		{"follow and reported", map[string]any{
			"follow":   map[string]any{"next": "Wait for a reply"},
			"reported": map[string]any{"text": "A late step"},
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := reportedSuggest(t, h, tc.args)
			if tc.existingRefusal {
				// Adding reported does not change the established kind/follow-on-filed refusal (#6683).
				if out != "" || !errors.Is(err, errSuggestFiled) {
					t.Fatalf("existing filed refusal: output %q, error %v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "already filed") ||
				!strings.Contains(err.Error(), "reported") || !strings.Contains(err.Error(), "not recorded") {
				t.Fatalf("filed refusal: output %q, error %v", out, err)
			}
			if out != "" {
				t.Fatalf("the tool said the refused step was kept: %q", out)
			}
			reportedAssertUnchanged(t, h, it, before, writes)
		})
	}

	out, err := reportedSuggest(t, h, map[string]any{"kind": SuggestReply, "text": "A late reply"})
	if out != "" || !errors.Is(err, errSuggestFiled) {
		t.Fatalf("kind on a filed item: output %q, error %v", out, err)
	}
	reportedAssertUnchanged(t, h, it, before, writes)
}

// Only the most recent reported text deduplicates, even when another kind of chain entry intervenes.
func TestInboxReportedDeduplicatesOnlyLatestText(t *testing.T) {
	h, _, it := handledHub(t)
	first := "Requester confirmed the schedule"
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "  Requester confirmed\nthe schedule  ", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reportedSuggest(t, h, map[string]any{
		"kind": SuggestReply,
		"text": "Friday works for me",
	}); err != nil {
		t.Fatal(err)
	}
	before := len(reportedChain(h, it))
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "\tRequester confirmed   the schedule\n", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	if after := len(reportedChain(h, it)); after != before {
		t.Fatalf("an identical normalized report added a chain entry: %d → %d", before, after)
	}
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Asked for a different date", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": first, "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, e := range reportedChain(h, it) {
		if e.Kind == ChainReported {
			details = append(details, e.Detail)
		}
	}
	want := []string{first, "Asked for a different date", first}
	if len(details) != len(want) {
		t.Fatalf("reported details %q, want %q", details, want)
	}
	for i := range want {
		if details[i] != want[i] {
			t.Fatalf("reported details %q, want %q", details, want)
		}
	}
}

// A conversation the item was handed to records a reported step by naming the item.
func TestInboxReportedFromHandoff(t *testing.T) {
	h, _, it := handoffHub(t)
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", reportedJSON(t, map[string]any{
		"item":     it.ID,
		"reported": map[string]any{"text": "Checked the request", "by": ViaAgent},
	})); err != nil {
		t.Fatal(err)
	}
	e := reportedLast(t, h, it)
	if e.Kind != ChainReported || e.Via != ViaAgent || e.Detail != "Checked the request" {
		t.Fatalf("reported step: %+v", e)
	}
	h.inbox.mu.Lock()
	open, suggestion := it.open(), it.Suggestion
	h.inbox.mu.Unlock()
	if !open || suggestion != nil {
		t.Fatalf("handed reported-only call: open %v, suggestion %+v", open, suggestion)
	}
}

// The listed item and its GET JSON expose the reported kind and its via value.
func TestInboxReportedListedJSON(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := reportedSuggest(t, h, map[string]any{
		"reported": map[string]any{"text": "Requester confirmed", "by": ViaAgent},
	}); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	h.inbox.mu.Unlock()
	if len(listed.Chain) == 0 {
		t.Fatal("the listed item has no chain")
	}
	e := listed.Chain[len(listed.Chain)-1]
	if e.Kind != ChainReported || e.Via != ViaAgent {
		t.Fatalf("listed chain entry: %+v", e)
	}

	code, out := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/"+it.ID, "")
	if code != 200 {
		t.Fatalf("GET the item: %d %v", code, out)
	}
	chain, ok := out["chain"].([]any)
	if !ok || len(chain) == 0 {
		t.Fatalf("GET chain: %v", out["chain"])
	}
	last, ok := chain[len(chain)-1].(map[string]any)
	if !ok || last["kind"] != ChainReported || last["via"] != ViaAgent || last["detail"] != "Requester confirmed" {
		t.Fatalf("GET chain entry: %v", chain[len(chain)-1])
	}
}

// Blank text and an unknown by are refused before any accompanying change is made or saved.
func TestInboxReportedRejectsBlankTextAndInvalidBy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reported map[string]any
	}{
		{"blank text", map[string]any{"text": " \n\t "}},
		{"invalid by", map[string]any{"text": "Checked the request", "by": "owner"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			before, writes := reportedSnapshot(h, it)
			out, err := reportedSuggest(t, h, map[string]any{
				"kind":     SuggestReply,
				"text":     "A reply that must not be kept",
				"recap":    "A recap that must not be kept",
				"reported": tc.reported,
			})
			if err == nil || !strings.Contains(err.Error(), "reported") {
				t.Fatalf("invalid reported step: output %q, error %v", out, err)
			}
			if out != "" {
				t.Fatalf("the tool said an invalid call was kept: %q", out)
			}
			reportedAssertUnchanged(t, h, it, before, writes)
		})
	}
}
