package assistant

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The reviewer's checks of nowKey against the shared table (spec/fixtures/inbox/now.json, docs/assistant/inbox.md
// "Following up"): every case, the list's copy, combinations of several states, and that the key is from the item
// alone (no clock).

const nowFixturePath = "../../../spec/fixtures/inbox/now.json"

type nowFixtureCase struct {
	Name  string `json:"name"`
	Input struct {
		Item json.RawMessage `json:"item"`
	} `json:"input"`
	Expected struct {
		Now     string `json:"now"`
		Section string `json:"section"`
	} `json:"expected"`
}

func readNowFixture(t *testing.T) []nowFixtureCase {
	t.Helper()
	raw, err := os.ReadFile(nowFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []nowFixtureCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f.Cases
}

func nowItemOf(t *testing.T, raw json.RawMessage) *InboxItem {
	t.Helper()
	it := &InboxItem{}
	if err := json.Unmarshal(raw, it); err != nil {
		t.Fatal(err)
	}
	return it
}

// Every case in the shared table: the key, the section, and the list's copy of the item.
func TestNowKeySharedCases(t *testing.T) {
	cases := readNowFixture(t)
	if len(cases) != 24 {
		t.Fatalf("the shared table has %d cases, want 24", len(cases))
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			it := nowItemOf(t, c.Input.Item)
			if got := it.nowKey(); got != c.Expected.Now {
				t.Fatalf("nowKey = %q, want %q", got, c.Expected.Now)
			}
			if got := it.status(); got != c.Expected.Section {
				t.Fatalf("status = %q, want %q", got, c.Expected.Section)
			}
			if got := it.listed().Now; got != c.Expected.Now {
				t.Fatalf("listed now = %q, want %q", got, c.Expected.Now)
			}
		})
	}
}

// nowItem is an open item with nothing prepared, for building combinations.
func nowItem() *InboxItem {
	at := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	return &InboxItem{ID: "i", Source: "chat", Kind: KindGroup, Title: "T", Tier: TierAction,
		StartedAt: at, UpdatedAt: at, Last: "1"}
}

// States that hold together: the earlier one in the table's order wins.
func TestNowKeyCombinations(t *testing.T) {
	past := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC) // long before any test run
	replied := &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "ok", At: past}
	agentAsk := &InboxSuggestion{Version: 1, Kind: SuggestAgent, Text: "do it", At: past}
	handback := &InboxSuggestion{Version: 1, Kind: SuggestAgent, Text: "do it", Handback: true, At: past}
	conclusion := &InboxSuggestion{Version: 1, Kind: SuggestConclusion, Text: "seen", At: past}
	doneAt := past.Add(time.Hour)

	tests := []struct {
		name string
		item func() *InboxItem
		want string
	}{
		{"suggestion over a failed handling", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingFailed, Error: "boom"}
			it.Suggestion = replied
			return it
		}, NowConfirmReply},
		{"agent suggestion handed back", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
			it.Suggestion = handback
			return it
		}, NowDecide},
		{"folded keeps folded over a suggestion", func() *InboxItem {
			it := nowItem()
			it.Tier = TierNoise
			it.Suggestion = replied
			return it
		}, NowFolded},
		{"folded keeps folded over an agent looking", func() *InboxItem {
			it := nowItem()
			it.Tier = TierNoise
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking}
			return it
		}, NowFolded},
		{"done keeps done over everything", func() *InboxItem {
			it := nowItem()
			it.DoneAt, it.DoneBy = &doneAt, "user"
			it.Tier = TierNoise
			it.Suggestion = replied
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking}
			return it
		}, NowDoneUser},
		{"a conclusion suggestion is not one of the suggestion keys", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
			it.Suggestion = conclusion
			return it
		}, NowNeedsYou},
		{"a conclusion suggestion on an fyi item", func() *InboxItem {
			it := nowItem()
			it.Tier = TierFYI
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
			it.Suggestion = conclusion
			return it
		}, NowFYI},
		{"an agent suggestion on an fyi item waits for the user", func() *InboxItem {
			it := nowItem()
			it.Tier = TierFYI
			it.Suggestion = agentAsk
			return it
		}, NowConfirmAgent},
		{"queued without a session", func() *InboxItem {
			it := nowItem()
			it.Agent = &InboxAgent{State: AgentQueued}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking}
			return it
		}, NowQueued},
		{"queued with a session is the processing state", func() *InboxItem {
			it := nowItem()
			it.Agent = &InboxAgent{State: AgentQueued, SessionID: "s1"}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking}
			return it
		}, NowLooking},
		{"a running agent record is the processing state", func() *InboxItem {
			it := nowItem()
			it.Agent = &InboxAgent{State: AgentRunning, SessionID: "s1"}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking}
			return it
		}, NowLooking},
		{"three checks while it waits is still waiting", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
			it.Follow = &InboxFollow{Who: "Alex", Checks: 3, Due: true}
			return it
		}, NowWaiting},
		{"three checks, nothing prepared", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
			it.Follow = &InboxFollow{Checks: 3, Due: true}
			return it
		}, NowFollowUpAsk},
		{"two checks and due", func() *InboxItem {
			it := nowItem()
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
			it.Follow = &InboxFollow{Checks: 2, Due: true}
			return it
		}, NowCheckDue},
		{"nothing prepared, no tier", func() *InboxItem {
			it := nowItem()
			it.Tier = ""
			return it
		}, NowNeedsYou},
		{"urgent while waiting stays waiting", func() *InboxItem {
			it := nowItem()
			it.Urgent = true
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
			it.Follow = &InboxFollow{Who: "Alex"}
			return it
		}, NowWaiting},
		{"an old draft is a reply suggestion", func() *InboxItem {
			it := nowItem()
			it.Agent = &InboxAgent{State: AgentDrafted, Draft: "Friday works.", MachineID: "m1", SessionID: "s1"}
			return it
		}, NowConfirmReply},
		{"muted: the newest done entry says muted", func() *InboxItem {
			it := nowItem()
			it.DoneAt, it.DoneBy = &doneAt, "read"
			it.Chain = []InboxChainEntry{
				{ID: "c1", At: past, Kind: ChainDone, By: DoneByMuted},
				{ID: "c2", At: doneAt, Kind: ChainReceived, Text: "later news"},
			}
			return it
		}, NowDoneMuted},
		{"an older muted done entry under a newer one is read", func() *InboxItem {
			it := nowItem()
			it.DoneAt, it.DoneBy = &doneAt, "read"
			it.Chain = []InboxChainEntry{
				{ID: "c1", At: past, Kind: ChainDone, By: DoneByMuted},
				{ID: "c2", At: doneAt, Kind: ChainDone, By: "read"},
			}
			return it
		}, NowDoneRead},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.item().nowKey(); got != tc.want {
				t.Fatalf("nowKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// The key is from the item alone: a check time long past, due false and fewer than three checks is not a due look, and
// the same item gives the same key whenever it is asked.
func TestNowKeyNeedsNoClock(t *testing.T) {
	it := nowItem()
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone}
	it.Follow = &InboxFollow{Who: "Alex", Next: "ask", CheckAt: ptrTime(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)), Checks: 2}
	first := it.nowKey()
	if first != NowNeedsYou {
		t.Fatalf("a past check_at without due gave %q, want %q", first, NowNeedsYou)
	}
	if second := it.nowKey(); second != first {
		t.Fatalf("nowKey changed from %q to %q", first, second)
	}
	// Asking does not change the item.
	before, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	_ = it.nowKey()
	_ = it.listed()
	after, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("nowKey changed the item: %s / %s", before, after)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// A muted filing is an earlier filing once the item was undone and filed again: a reply and a superseded filing
// write no done chain entry, so the newest done entry is still the mute's and must not name this filing.
func TestNowKeyStaleMutedEntryDoesNotNameALaterFiling(t *testing.T) {
	past := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	doneAt := past.Add(2 * time.Hour)
	muted := InboxChainEntry{ID: "c1", At: past, Kind: ChainDone, Text: "Muted, done", By: DoneByMuted}
	for _, doneBy := range []string{"replied", "superseded"} {
		t.Run(doneBy, func(t *testing.T) {
			it := nowItem()
			it.DoneAt, it.DoneBy = &doneAt, doneBy
			it.Chain = []InboxChainEntry{muted, {ID: "c2", At: doneAt, Kind: ChainSelfReplied, Text: "replied at the source"}}
			want := map[string]string{"replied": NowDoneReplied, "superseded": NowDoneSuperseded}[doneBy]
			if got := it.nowKey(); got != want {
				t.Fatalf("done_by %q: nowKey = %q, want %q", doneBy, got, want)
			}
		})
	}
}
