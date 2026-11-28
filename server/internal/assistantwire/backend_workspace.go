package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/repositorywire"
)

// BackendWorkspaceRPCTimeout is the Manager's budget for an ordinary task.workspace RPC.
const BackendWorkspaceRPCTimeout = repositorywire.TaskWorkspaceRPCTimeout

// BackendWorkspacePrepareTimeout covers serial mirror ensures followed by a full direct
// prepare RPC if all mirrors fail, with time for planning, status writes and the reply.
func BackendWorkspacePrepareTimeout(repositories int) time.Duration {
	mirrorTimeout := time.Duration(repositories) * repositorywire.MirrorEnsureTimeout
	return mirrorTimeout + repositorywire.TaskWorkspacePrepareTimeout(repositories) + time.Minute
}
