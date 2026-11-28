package hub

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The app registers exactly these categories (apple/Persona/App/NotificationActions.swift); a different spelling would
// silently drop the buttons from every notification.
func TestInteractionPayloadCategory(t *testing.T) {
	want := map[string]string{
		proto.IxPermission: "INTERACTION_PERMISSION", proto.IxPlan: "INTERACTION_PLAN",
		proto.IxQuestion: "INTERACTION_QUESTION", proto.IxScreen: "INTERACTION_SCREEN",
	}
	for kind, cat := range want {
		occurrence := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
		p := interactionPayload(proto.Interaction{ID: "ix1", Kind: kind, MachineID: "m", SessionID: "s", SessionTitle: "t", CreatedAt: occurrence}, "box")
		aps := p["aps"].(map[string]any)
		if aps["category"] != cat {
			t.Errorf("%s: category %v, want %s", kind, aps["category"], cat)
		}
		// The buttons resolve this id; the tap opens machine/session.
		if p["interaction_id"] != "ix1" || p["machine_id"] != "m" || p["session_id"] != "s" {
			t.Errorf("%s: ids %v", kind, p)
		}
		if p["interaction_created_at"] != occurrence.Format(time.RFC3339Nano) {
			t.Errorf("%s: occurrence %v, want %s", kind, p["interaction_created_at"], occurrence.Format(time.RFC3339Nano))
		}
	}
}
