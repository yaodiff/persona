package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

// BackendWorkspaceRPCTimeout is the Manager's budget for an ordinary task.workspace RPC.
const BackendWorkspaceRPCTimeout = taskworkspace.RPCTimeout

// BackendWorkspacePrepareTimeout covers serial mirror ensures followed by a full direct
// prepare RPC if all mirrors fail, with time for planning, status writes and the reply.
func BackendWorkspacePrepareTimeout(repositories int) time.Duration {
	mirrorTimeout := time.Duration(repositories) * repositorywire.MirrorEnsureTimeout
	return mirrorTimeout + taskworkspace.PrepareTimeout(repositories) + time.Minute
}
