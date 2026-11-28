package assistant

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// testSpaceKindOwner is the extension the hub says contributes the space kind (extmgr/track_kinds.go).
const testSpaceKindOwner = "space-conversations"

// testTrackKinds is the declarative contribution the old track tests exercise.
// Production has no built-in domain-space kind; it comes from the extension.
type testTrackKinds struct{}

func (testTrackKinds) DomainTrackKindsJSON(context.Context) (json.RawMessage, error) {
	return json.Marshal([]struct {
		ID        string                        `json:"id"`
		Title     string                        `json:"title"`
		Config    assistantwire.TrackKindConfig `json:"config"`
		Extension string                        `json:"extension"`
	}{{ID: trackKindSpace, Title: "空间", Extension: testSpaceKindOwner, Config: assistantwire.TrackKindConfig{
		Schema: assistantwire.TrackKindSchema, Template: trackTemplateSpace, UserInstructions: "space_md", Context: "track",
		DefaultCategories: []string{}, TagFocus: []string{}, Rhythms: []assistantwire.TrackRhythm{},
		Attention:   assistantwire.TrackAttention{PushCap: assistantwire.LevelP0, DeepDefault: "quick", EnterMode: "none"},
		Supervision: "none", Usage: []assistantwire.TrackUsageRule{}, Assembly: "track", CaptureTarget: true,
		Channels: []string{"topics", "single"}, Sidebar: assistantwire.TrackSidebar{Group: "spaces"},
	}}})
}

func installTestTrackKinds(s *Service) { s.trackKindsExt = testTrackKinds{} }
