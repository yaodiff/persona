package devicewire

import (
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Node update RPCs sent by the device manager to a resident node.
const (
	MethodNodeUpdateBegin   = "node.update.begin"
	MethodNodeUpdateAbort   = "node.update.abort"
	MethodNodeUpdateChunk   = "node.update.chunk"
	MethodNodeUpdateCommit  = "node.update.commit"
	MethodNodeUpdateConfirm = "node.update.confirm"
)

var (
	NodeUpdateBeginMethod   = buswire.Method[proto.NodeUpdateBeginParams, proto.NodeUpdateBeginResult]{Name: MethodNodeUpdateBegin}
	NodeUpdateAbortMethod   = buswire.Method[proto.NodeUpdateAbortParams, buswire.None]{Name: MethodNodeUpdateAbort}
	NodeUpdateChunkMethod   = buswire.Method[proto.NodeUpdateChunkParams, proto.NodeUpdateChunkResult]{Name: MethodNodeUpdateChunk}
	NodeUpdateCommitMethod  = buswire.Method[proto.NodeUpdateCommitParams, proto.NodeUpdateCommitResult]{Name: MethodNodeUpdateCommit}
	NodeUpdateConfirmMethod = buswire.Method[proto.NodeUpdateConfirmParams, buswire.None]{Name: MethodNodeUpdateConfirm}
)
