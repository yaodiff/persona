package assistant

import (
	"reflect"
	"testing"
	"time"
)

// The compatibility control must independently cover the ordinary public Agent
// fields whose survival it claims to prove. A same-listed() actual/control
// comparison cannot catch both sides losing a field that its legacy shape and
// fixtures omit.
func TestInboxWakeSlotsLegacyAgentCoverage(t *testing.T) {
	legacy := reflect.TypeOf(inboxWakeSlotsWireLegacyAgent{})
	for _, name := range []string{"Changes", "Error", "RetryAfter"} {
		if _, ok := legacy.FieldByName(name); !ok {
			t.Errorf("the independent legacy Agent omits existing public field %s", name)
		}
	}

	for _, tc := range []struct {
		name string
		item *InboxItem
	}{
		{name: "listed fixture", item: inboxWakeSlotsListedCompatFixture(true, true, true)},
		{name: "wire fixture", item: inboxWakeSlotsWirePublicItem(time.Now().UTC())},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.item.Agent.Changes) == 0 {
				t.Error("fixture leaves public Agent.Changes empty, so its survival is not checked")
			}
			if tc.item.Agent.Error == "" {
				t.Error("fixture leaves public Agent.Error empty, so its survival is not checked")
			}
			if tc.item.Agent.RetryAfter == nil {
				t.Error("fixture leaves public Agent.RetryAfter nil, so its survival is not checked")
			}
		})
	}
}
