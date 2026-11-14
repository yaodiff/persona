package assistant

import (
	"context"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
)

// A fallback plan may assign new messages to a handled candidate. The manager accepts the assignment and reopens
// that event instead of opening another row.
func TestInboxGroupStateFallbackReopensHandledEvent(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:900"}, {Message: "m1061", Event: "lark:F1:900"}}}}
	h, _ := groupHub(t, g, nil)
	item := h.inbox.items["lark:F1:900"]
	item.Count = 1
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != item.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if !item.open() || item.DoneBy != "" || item.Count != 3 || item.Last != "1061" {
		t.Fatalf("reopened event %+v", item)
	}
	kinds := chainKinds(item)
	if len(kinds) < 2 || kinds[0] != ChainReceived || kinds[1] != ChainReopened {
		t.Fatalf("chain %v", kinds)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the assignment opened another event: %v", h.inbox.items)
	}
}

func TestInboxGroupStateCapabilityAdvertised(t *testing.T) {
	if !slices.Contains(extension.HubCapabilities(), extension.HubInboxGroupState) {
		t.Fatalf("initialize capabilities %v", extension.HubCapabilities())
	}
}
