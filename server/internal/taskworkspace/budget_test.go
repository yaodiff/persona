package taskworkspace_test

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

// The backend workspace budgets come from the node contract (nodeproto) so the hub can link them without
// repositorywire; each place that reads them keeps the values it had (#17786, #17787).
func TestBackendWorkspaceBudgetsKeepTheirValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		got, want time.Duration
	}{
		{"taskworkspace.GitTimeout", taskworkspace.GitTimeout, 2 * time.Minute},
		{"taskworkspace.ColdFetchTimeout", taskworkspace.ColdFetchTimeout, 10 * time.Minute},
		{"taskworkspace.RPCTimeout", taskworkspace.RPCTimeout, 5 * time.Minute},
		{"taskworkspace.PrepareTimeout(1)", taskworkspace.PrepareTimeout(1), 17 * time.Minute},
		{"repositorywire.MirrorColdFetchTimeout", repositorywire.MirrorColdFetchTimeout, 10 * time.Minute},
		{"repositorywire.MirrorEnsureTimeout", repositorywire.MirrorEnsureTimeout, 11 * time.Minute},
		{"assistantwire.BackendWorkspaceRPCTimeout", assistantwire.BackendWorkspaceRPCTimeout, 5 * time.Minute},
		{"assistantwire.BackendWorkspacePrepareTimeout(2)", assistantwire.BackendWorkspacePrepareTimeout(2), 2*11*time.Minute + 29*time.Minute + time.Minute},
	} {
		if c.got != c.want {
			t.Errorf("%s = %s, want %s", c.name, c.got, c.want)
		}
	}
	if repositorywire.MaxTaskRepositories != 20 {
		t.Errorf("repositorywire.MaxTaskRepositories = %d, want 20", repositorywire.MaxTaskRepositories)
	}
}
