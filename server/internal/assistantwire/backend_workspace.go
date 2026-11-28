package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// BackendWorkspaceRPCTimeout is the Manager's budget for an ordinary task.workspace RPC.
const BackendWorkspaceRPCTimeout = nodeproto.TaskWorkspaceRPCSeconds * time.Second

// BackendWorkspacePrepareTimeout covers serial mirror ensures followed by a full direct
// prepare RPC if all mirrors fail, with time for planning, status writes and the reply.
func BackendWorkspacePrepareTimeout(repositories int) time.Duration {
	mirrorTimeout := time.Duration(repositories) * nodeproto.RepoMirrorEnsureSeconds * time.Second
	return mirrorTimeout + time.Duration(nodeproto.TaskWorkspacePrepareSeconds(repositories))*time.Second + time.Minute
}
