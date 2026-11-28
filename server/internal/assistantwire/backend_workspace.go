package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/repositorywire"
)

// BackendWorkspaceRPCTimeout is the Manager's budget for a task.workspace RPC.
const BackendWorkspaceRPCTimeout = 5 * time.Minute

// BackendWorkspacePrepareTimeout covers serial mirror ensures, the direct prepare RPC if any
// mirror fails, and extra time for planning, status writes and the HTTP reply.
func BackendWorkspacePrepareTimeout(repositories int) time.Duration {
	return time.Duration(repositories)*repositorywire.MirrorEnsureTimeout + BackendWorkspaceRPCTimeout + time.Minute
}
