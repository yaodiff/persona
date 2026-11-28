package all

import (
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/botport"
)

// The user sync's OAuth client and readers are provided exactly when the build tags keep the Lark module; without it
// the manager gets nil and answers that the sync is unavailable. Run with -tags persona_without_lark or
// persona_core_only as well (scripts/ci/server.sh).
func TestLarkUserAuthFollowsTheBuildTags(t *testing.T) {
	if got, want := botport.NewUserAuth("", "", nil) != nil, slices.Contains(wantModules, "lark"); got != want {
		t.Fatalf("lark user auth provided: %v, want %v for this build's tags", got, want)
	}
}

func TestLarkSyncerFollowsTheBuildTags(t *testing.T) {
	if got, want := botport.NewSyncer(nil, nil) != nil, slices.Contains(wantModules, "lark"); got != want {
		t.Fatalf("lark syncer provided: %v, want %v for this build's tags", got, want)
	}
}

func TestLarkSyncServiceFollowsTheBuildTags(t *testing.T) {
	if got, want := botport.NewSyncService(nil) != nil, slices.Contains(wantModules, "lark"); got != want {
		t.Fatalf("lark sync service provided: %v, want %v for this build's tags", got, want)
	}
}
