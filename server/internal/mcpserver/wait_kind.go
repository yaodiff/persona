package mcpserver

import (
	"context"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// mcpWaitState is the latest enum for one pending call. It lives only in the call's context and is never persisted.
type mcpWaitState struct {
	kind atomic.Value // proto.MCPWaitKind
}

type mcpWaitContextKey struct{}

func withMCPWait(ctx context.Context) context.Context {
	state := &mcpWaitState{}
	state.set(proto.MCPWaitUnknown)
	return nodeclient.WithMCPWait(context.WithValue(ctx, mcpWaitContextKey{}, state), state.set)
}

func mcpWaitFrom(ctx context.Context) *mcpWaitState {
	state, _ := ctx.Value(mcpWaitContextKey{}).(*mcpWaitState)
	return state
}

func (s *mcpWaitState) set(kind proto.MCPWaitKind) {
	if s != nil {
		s.kind.Store(proto.NormalizeMCPWaitKind(kind))
	}
}

func (s *mcpWaitState) current() proto.MCPWaitKind {
	if s == nil {
		return proto.MCPWaitUnknown
	}
	kind, _ := s.kind.Load().(proto.MCPWaitKind)
	return proto.NormalizeMCPWaitKind(kind)
}

// mcpWaitMessage is the complete vocabulary a terminal client can show. These fixed English strings belong to the
// Agent's terminal client, not Persona UI, and therefore do not use Persona's localization resources.
func mcpWaitMessage(ctx context.Context) string {
	switch mcpWaitFrom(ctx).current() {
	case proto.MCPWaitApproval:
		return "Waiting for your approval"
	case proto.MCPWaitAnswer:
		return "Waiting for your answer"
	case proto.MCPWaitComputer:
		return "Waiting for the computer"
	default:
		return "Still waiting"
	}
}
