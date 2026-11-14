package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

// inboxWakeSlotsWireLegacyAgent is the public agent shape an old client decodes.
// It deliberately has none of the deferred-wake bookkeeping fields.
type inboxWakeSlotsWireLegacyAgent struct {
	State      string      `json:"state"`
	Task       string      `json:"task"`
	Reason     string      `json:"reason"`
	MachineID  string      `json:"machine_id"`
	SessionID  string      `json:"session_id"`
	Draft      string      `json:"draft"`
	Note       string      `json:"note"`
	Changes    []DocChange `json:"changes"`
	Error      string      `json:"error"`
	Of         string      `json:"of"`
	TodoID     string      `json:"todo_id"`
	StartedAt  *time.Time  `json:"started_at"`
	DraftedAt  *time.Time  `json:"drafted_at"`
	Attempts   int         `json:"attempts"`
	RetryAfter *time.Time  `json:"retry_after"`
}

func TestInboxWakeSlotsWireCompatibility(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	tests := []struct {
		name            string
		deferredAt      bool
		wakeSlot        bool
		deferredPending bool
	}{
		{name: "deferred_at", deferredAt: true},
		{name: "wake_slot", wakeSlot: true},
		{name: "pending_deferred", deferredPending: true},
		{name: "all_internal_fields", deferredAt: true, wakeSlot: true, deferredPending: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actualItem := inboxWakeSlotsWirePublicItem(now)
			if tc.deferredAt {
				deferredAt := now.Add(-2 * time.Minute)
				actualItem.Agent.DeferredAt = &deferredAt
			}
			actualItem.Agent.WakeSlot = tc.wakeSlot
			if tc.deferredPending {
				actualItem.Agent.Pending = []inboxNote{{Name: "new_messages", Text: "Held until admitted", Deferred: true}}
			}

			// The control is a separate item and a separate service, not another listed copy of the actual fixture.
			actualHub := inboxWakeSlotsWireHub(t, actualItem)
			controlHub := inboxWakeSlotsWireHub(t, inboxWakeSlotsWirePublicItem(now))
			for _, endpoint := range []struct {
				name string
				path string
				list bool
			}{
				{name: "list", path: "/api/v1/inbox?sources=all", list: true},
				{name: "detail", path: "/api/v1/inbox/" + actualItem.ID},
			} {
				t.Run(endpoint.name, func(t *testing.T) {
					actual := inboxWakeSlotsWireResponseItem(t, actualHub.routes(), endpoint.path, endpoint.list)
					control := inboxWakeSlotsWireResponseItem(t, controlHub.routes(), endpoint.path, endpoint.list)
					inboxWakeSlotsWireAssertInternalFieldsAbsent(t, actual)
					inboxWakeSlotsWireAssertSamePublicJSON(t, actual, control)
					inboxWakeSlotsWireAssertLegacyAgent(t, actual, now)
				})
			}
		})
	}
}

// inboxWakeSlotsWirePublicItem returns only fields that were already public before deferred wake slots.
func inboxWakeSlotsWirePublicItem(now time.Time) *InboxItem {
	startedAt := now.Add(-30 * time.Minute)
	draftedAt := now.Add(-20 * time.Minute)
	retryAfter := now.Add(5 * time.Minute)
	return &InboxItem{
		ID:          "wire-compat-item",
		Source:      "lark",
		Kind:        KindDM,
		Account:     "primary",
		AccountName: "Example account",
		Title:       "Quarterly plan",
		Sender:      "A colleague",
		Preview:     "Please review the plan",
		Count:       3,
		StartedAt:   now.Add(-time.Hour),
		UpdatedAt:   now.Add(-time.Minute),
		Tier:        TierAction,
		Summary:     "Review requested",
		Reason:      "A reply is needed",
		Last:        "message-7",
		Context:     "Colleague: please review the plan",
		Lark:        &InboxLark{FeedID: "feed-wire", StartPosition: "7"},
		Agent: &InboxAgent{
			State:      AgentDrafted,
			Task:       "Draft a reply",
			Reason:     "The sender asked a question",
			MachineID:  "server",
			SessionID:  "session-wire",
			Draft:      "I will review it today.",
			Note:       "The draft answers the question.",
			Changes:    []DocChange{{Ref: "row-wire", Field: "status", Value: "approved"}},
			Error:      "The previous attempt needs a retry",
			Of:         "message-7",
			TodoID:     "todo-wire",
			StartedAt:  &startedAt,
			DraftedAt:  &draftedAt,
			Attempts:   1,
			RetryAfter: &retryAfter,
		},
	}
}

func inboxWakeSlotsWireHub(t *testing.T, item *InboxItem) *inboxHub {
	t.Helper()
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatalf("prepare the inbox: %v", err)
	}
	waitNeedsYouIdle(t, h.Service)
	h.inbox.mu.Lock()
	h.inbox.items[item.ID] = item
	h.inbox.mu.Unlock()
	return h
}

func inboxWakeSlotsWireResponseItem(t *testing.T, handler http.Handler, path string, list bool) map[string]any {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, response.Code, response.Body.String())
	}
	if list {
		var envelope struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode the list response: %v", err)
		}
		if len(envelope.Items) != 1 {
			t.Fatalf("list contains %d items, want the one fixture item: %s", len(envelope.Items), response.Body.String())
		}
		return envelope.Items[0]
	}
	var item map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
		t.Fatalf("decode the detail response: %v", err)
	}
	return item
}

func inboxWakeSlotsWireAssertInternalFieldsAbsent(t *testing.T, item map[string]any) {
	t.Helper()
	agent, ok := item["agent"].(map[string]any)
	if !ok {
		t.Fatalf("public agent is missing or has the wrong shape: %#v", item["agent"])
	}
	for _, key := range []string{"deferred_at", "wake_slot", "pending"} {
		if value, exists := agent[key]; exists {
			t.Fatalf("internal agent.%s leaked into the response: %#v", key, value)
		}
	}
	// No pending array means no pending[].deferred member can reach an old client either.
}

func inboxWakeSlotsWireAssertSamePublicJSON(t *testing.T, actual, control map[string]any) {
	t.Helper()
	for key, want := range control {
		got, exists := actual[key]
		if !exists {
			t.Errorf("public field %q is missing: want %#v", key, want)
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("public field %q changed: got %#v, want %#v", key, got, want)
		}
	}
	for key, got := range actual {
		if _, exists := control[key]; !exists {
			t.Errorf("unexpected public field %q: %#v", key, got)
		}
	}
}

func inboxWakeSlotsWireAssertLegacyAgent(t *testing.T, item map[string]any, now time.Time) {
	t.Helper()
	raw, err := json.Marshal(item["agent"])
	if err != nil {
		t.Fatalf("encode the response agent for the legacy decode: %v", err)
	}
	var got inboxWakeSlotsWireLegacyAgent
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("old client cannot decode the public agent: %v", err)
	}
	startedAt := now.Add(-30 * time.Minute)
	draftedAt := now.Add(-20 * time.Minute)
	retryAfter := now.Add(5 * time.Minute)
	want := inboxWakeSlotsWireLegacyAgent{
		State:      AgentDrafted,
		Task:       "Draft a reply",
		Reason:     "The sender asked a question",
		MachineID:  "server",
		SessionID:  "session-wire",
		Draft:      "I will review it today.",
		Note:       "The draft answers the question.",
		Changes:    []DocChange{{Ref: "row-wire", Field: "status", Value: "approved"}},
		Error:      "The previous attempt needs a retry",
		Of:         "message-7",
		TodoID:     "todo-wire",
		StartedAt:  &startedAt,
		DraftedAt:  &draftedAt,
		Attempts:   1,
		RetryAfter: &retryAfter,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordinary public agent content changed:\ngot:  %#v\nwant: %#v", got, want)
	}
}
