package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func inboxWakeSlotsListedCompatFixture(withDeferredAt, withWakeSlot, withDeferredPending bool) *InboxItem {
	at := time.Date(2026, time.November, 14, 10, 30, 0, 123000000, time.UTC)
	retryAfter := at.Add(5 * time.Minute)
	item := &InboxItem{
		ID:        "listed-compat",
		Source:    "example",
		Kind:      KindDM,
		Title:     "A public title",
		Preview:   "A public preview",
		Count:     3,
		Tier:      TierAction,
		Last:      "position-1",
		UpdatedAt: at,
		Agent: &InboxAgent{
			State:      AgentRunning,
			Task:       "Review the update",
			Reason:     "A reply is needed",
			MachineID:  "server",
			SessionID:  "session-1",
			Draft:      "A public draft",
			Note:       "A public note",
			Changes:    []DocChange{{Ref: "row-listed", Field: "status", Value: "done"}},
			Error:      "A public agent error",
			Of:         "position-1",
			Attempts:   2,
			RetryAfter: &retryAfter,
		},
	}
	if withDeferredAt {
		item.Agent.DeferredAt = &at
	}
	item.Agent.WakeSlot = withWakeSlot
	if withDeferredPending {
		item.Agent.Pending = []inboxNote{{
			Name:     "agent_stopped",
			Text:     "The agent result",
			Machine:  "server",
			Session:  "session-1",
			Deferred: true,
		}}
	}
	return item
}

func TestInboxWakeSlotsListedCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name                string
		withDeferredAt      bool
		withWakeSlot        bool
		withDeferredPending bool
	}{
		{name: "deferred_at", withDeferredAt: true},
		{name: "wake_slot", withWakeSlot: true},
		{name: "pending_deferred", withDeferredPending: true},
		{name: "all", withDeferredAt: true, withWakeSlot: true, withDeferredPending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := inboxWakeSlotsListedCompatFixture(tc.withDeferredAt, tc.withWakeSlot, tc.withDeferredPending)
			if tc.withDeferredAt && item.Agent.DeferredAt == nil {
				t.Fatal("the fixture did not set deferred_at")
			}
			if tc.withWakeSlot && !item.Agent.WakeSlot {
				t.Fatal("the fixture did not set wake_slot")
			}
			if tc.withDeferredPending && (len(item.Agent.Pending) != 1 || !item.Agent.Pending[0].Deferred) {
				t.Fatalf("the fixture did not set a deferred pending note: %+v", item.Agent.Pending)
			}
			before, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			wantAgent := *item.Agent
			wantAgent.Pending = nil
			wantAgent.DeferredAt = nil
			wantAgent.WakeSlot = false

			listed := item.listed()
			afterListed, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(afterListed, before) {
				t.Fatalf("listed mutated the original item:\nbefore %s\n after %s", before, afterListed)
			}
			if listed.Agent == nil {
				t.Fatal("listed dropped the public Agent")
			}
			if listed.Agent.Pending != nil || listed.Agent.DeferredAt != nil || listed.Agent.WakeSlot {
				t.Fatalf("listed exposed deferred bookkeeping: %+v", listed.Agent)
			}
			wantChanges := []DocChange{{Ref: "row-listed", Field: "status", Value: "done"}}
			if !reflect.DeepEqual(listed.Agent.Changes, wantChanges) {
				t.Fatalf("listed changed public Agent.Changes: want %+v, got %+v", wantChanges, listed.Agent.Changes)
			}
			if got, want := listed.Agent.Error, "A public agent error"; got != want {
				t.Fatalf("listed changed public Agent.Error: want %q, got %q", want, got)
			}
			wantRetryAfter := time.Date(2026, time.November, 14, 10, 35, 0, 123000000, time.UTC)
			if listed.Agent.RetryAfter == nil || !listed.Agent.RetryAfter.Equal(wantRetryAfter) {
				t.Fatalf("listed changed public Agent.RetryAfter: want %v, got %v", wantRetryAfter, listed.Agent.RetryAfter)
			}
			if !reflect.DeepEqual(*listed.Agent, wantAgent) {
				t.Fatalf("listed changed public Agent fields:\nwant %+v\n got %+v", wantAgent, *listed.Agent)
			}
			if listed.ID != item.ID || listed.Title != item.Title || listed.Preview != item.Preview || listed.Count != item.Count || listed.Tier != item.Tier {
				t.Fatalf("listed changed ordinary item fields: %+v", listed)
			}

			encoded, err := json.Marshal(listed)
			if err != nil {
				t.Fatal(err)
			}
			afterEncoding, err := json.Marshal(item)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(afterEncoding, before) {
				t.Fatalf("encoding the listed item mutated the original:\nbefore %s\n after %s", before, afterEncoding)
			}
			var external struct {
				Agent map[string]json.RawMessage `json:"agent"`
			}
			if err := json.Unmarshal(encoded, &external); err != nil {
				t.Fatal(err)
			}
			if external.Agent == nil {
				t.Fatalf("the encoded item has no Agent: %s", encoded)
			}
			for _, key := range []string{"pending", "deferred_at", "wake_slot"} {
				if _, present := external.Agent[key]; present {
					t.Fatalf("the encoded Agent exposes %s: %s", key, encoded)
				}
			}
		})
	}
}

func TestInboxWakeSlotsListedCompatibilityPersistence(t *testing.T) {
	dsn := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.TruncateForTest(ctx); err != nil {
		t.Fatal(err)
	}

	service := newServiceOn(t, newFakeHub().withMachine(), database)
	if err := service.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	item := inboxWakeSlotsListedCompatFixture(true, true, true)
	deferredAt := *item.Agent.DeferredAt
	wantNote := item.Agent.Pending[0]
	service.inbox.mu.Lock()
	service.inbox.items[item.ID] = item
	service.inbox.mu.Unlock()

	listed := item.listed()
	if _, err := json.Marshal(listed); err != nil {
		t.Fatal(err)
	}
	if failed := service.saveInbox(ctx, []*InboxItem{item}); len(failed) != 0 {
		t.Fatalf("save after listed sanitization failed: %v", failed)
	}

	reloaded := newServiceOn(t, newFakeHub().withMachine(), database)
	if err := reloaded.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded.inbox.mu.Lock()
	got := reloaded.inbox.items[item.ID]
	reloaded.inbox.mu.Unlock()
	if got == nil || got.Agent == nil {
		t.Fatalf("reload lost the item or its Agent: %+v", got)
	}
	if got.Agent.DeferredAt == nil || !got.Agent.DeferredAt.Equal(deferredAt) {
		t.Fatalf("reload lost deferred_at: want %v, got %v", deferredAt, got.Agent.DeferredAt)
	}
	if !got.Agent.WakeSlot {
		t.Fatal("reload lost wake_slot")
	}
	if len(got.Agent.Pending) != 1 || got.Agent.Pending[0] != wantNote || !got.Agent.Pending[0].Deferred {
		t.Fatalf("reload lost the deferred pending note: want %+v, got %+v", wantNote, got.Agent.Pending)
	}
}
