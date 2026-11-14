package assistant

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

const inboxNowContractPath = "../../../spec/fixtures/inbox/now.json"

type inboxNowContractCase struct {
	Name  string `json:"name"`
	Input struct {
		Item InboxItem `json:"item"`
	} `json:"input"`
	Expected struct {
		Now     string `json:"now"`
		Section string `json:"section"`
	} `json:"expected"`
}

func readInboxNowContract(t *testing.T) []inboxNowContractCase {
	t.Helper()
	raw, err := os.ReadFile(inboxNowContractPath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []inboxNowContractCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatalf("%s has no cases", inboxNowContractPath)
	}
	return fixture.Cases
}

// The manager and the apps share this table: the manager computes the key and section, the apps put the key in words.
func TestInboxNowContract(t *testing.T) {
	for _, tc := range readInboxNowContract(t) {
		t.Run(tc.Name, func(t *testing.T) {
			it := tc.Input.Item
			// The fixture's now is what an app receives, never an input to the manager's computation.
			it.Now, it.Status = "", ""
			if got := it.nowKey(); got != tc.Expected.Now {
				t.Fatalf("nowKey = %q, want %q", got, tc.Expected.Now)
			}
			if got := it.status(); got != tc.Expected.Section {
				t.Fatalf("status = %q, want %q", got, tc.Expected.Section)
			}
			listed := it.listed()
			if listed.Now != tc.Expected.Now {
				t.Fatalf("listed now = %q, want %q", listed.Now, tc.Expected.Now)
			}
			if listed.Status != tc.Expected.Section {
				t.Fatalf("listed status = %q, want %q", listed.Status, tc.Expected.Section)
			}
		})
	}
}

// Combinations the individual shared cases do not all cover: the first state in compose wins.
func TestInboxNowPrecedence(t *testing.T) {
	tests := []struct {
		name        string
		item        InboxItem
		wantNow     string
		wantSection string
	}{
		{
			name: "suggestion before failed",
			item: InboxItem{Tier: TierAction,
				Suggestion: &InboxSuggestion{Kind: SuggestReply},
				Handling:   &InboxHandling{State: HandlingFailed, Error: "timed out"}},
			wantNow: NowConfirmReply, wantSection: StatusAction,
		},
		{
			name: "folded before suggestion and handling",
			item: InboxItem{Tier: TierNoise,
				Suggestion: &InboxSuggestion{Kind: SuggestAgent},
				Handling:   &InboxHandling{State: HandlingLooking}},
			wantNow: NowFolded, wantSection: StatusNoise,
		},
		{
			name: "urgent looking keeps its words and changes section",
			item: InboxItem{Tier: TierAction, Urgent: true,
				Agent:    &InboxAgent{State: AgentRunning, SessionID: "s1"},
				Handling: &InboxHandling{State: HandlingLooking, SessionID: "s1"}},
			wantNow: NowLooking, wantSection: StatusAction,
		},
		{
			name: "follow up ask before check due",
			item: InboxItem{Tier: TierAction,
				Handling: &InboxHandling{State: HandlingDone},
				Follow:   &InboxFollow{Checks: inboxFollowUpAskChecks, Due: true}},
			wantNow: NowFollowUpAsk, wantSection: StatusAction,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.nowKey(); got != tc.wantNow {
				t.Fatalf("nowKey = %q, want %q", got, tc.wantNow)
			}
			if got := tc.item.status(); got != tc.wantSection {
				t.Fatalf("status = %q, want %q", got, tc.wantSection)
			}
		})
	}
}

// CheckAt is data for the apps, not a clock read here. Listed-only fields and urgency do not replace the item's key.
func TestInboxNowIgnoresClockAndListedFields(t *testing.T) {
	it := &InboxItem{Tier: TierAction, Handling: &InboxHandling{State: HandlingWaiting}}
	tests := []struct {
		name       string
		at         time.Time
		urgent     bool
		listedNow  string
		listedTier string
	}{
		{name: "past check time", at: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), listedNow: NowFailed, listedTier: StatusNoise},
		{name: "future check time and urgent", at: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), urgent: true, listedNow: NowDoneUser, listedTier: StatusFYI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at := tc.at
			it.StartedAt, it.UpdatedAt = at.Add(-time.Hour), at
			it.Follow = &InboxFollow{CheckAt: &at}
			it.Urgent, it.Now, it.Status = tc.urgent, tc.listedNow, tc.listedTier
			if got := it.nowKey(); got != NowWaiting {
				t.Fatalf("nowKey = %q, want %q", got, NowWaiting)
			}
		})
	}
}

func TestInboxNowLegacyDraft(t *testing.T) {
	it := &InboxItem{Tier: TierAction, Agent: &InboxAgent{State: AgentDrafted, Draft: "Friday works."}}
	if got := it.nowKey(); got != NowConfirmReply {
		t.Fatalf("nowKey = %q, want %q", got, NowConfirmReply)
	}
}

// A conclusion follows status(): it is not an action suggestion and does not show queued or processing work.
func TestInboxNowConclusionUsesLaterRules(t *testing.T) {
	tests := []struct {
		name        string
		item        InboxItem
		wantNow     string
		wantSection string
	}{
		{
			name: "handback conclusion is still a conclusion",
			item: InboxItem{Tier: TierAction,
				Suggestion: &InboxSuggestion{Kind: SuggestConclusion, Handback: true},
				Handling:   &InboxHandling{State: HandlingLooking}},
			wantNow: NowNeedsYou, wantSection: StatusAction,
		},
		{
			name: "conclusion on fyi does not surface failed handling",
			item: InboxItem{Tier: TierFYI,
				Suggestion: &InboxSuggestion{Kind: SuggestConclusion},
				Handling:   &InboxHandling{State: HandlingFailed}},
			wantNow: NowFYI, wantSection: StatusFYI,
		},
		{
			name: "follow up still applies",
			item: InboxItem{Tier: TierAction,
				Suggestion: &InboxSuggestion{Kind: SuggestConclusion},
				Handling:   &InboxHandling{State: HandlingLooking},
				Follow:     &InboxFollow{Checks: inboxFollowUpAskChecks, Due: true}},
			wantNow: NowFollowUpAsk, wantSection: StatusAction,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item.nowKey(); got != tc.wantNow {
				t.Fatalf("nowKey = %q, want %q", got, tc.wantNow)
			}
			if got := tc.item.status(); got != tc.wantSection {
				t.Fatalf("status = %q, want %q", got, tc.wantSection)
			}
		})
	}
}

func TestInboxNowUnknownDoneByUsesAppFallback(t *testing.T) {
	doneAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, doneBy := range []string{"", "from_a_newer_manager"} {
		it := &InboxItem{Tier: TierAction, DoneAt: &doneAt, DoneBy: doneBy}
		if got := it.nowKey(); got != "" {
			t.Fatalf("done_by %q: nowKey = %q, want an empty app-fallback key", doneBy, got)
		}
		if got := it.status(); got != "" {
			t.Fatalf("done_by %q: status = %q, want done", doneBy, got)
		}
	}
}
