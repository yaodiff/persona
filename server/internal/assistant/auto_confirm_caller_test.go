package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

type ownerCaptureExt struct {
	*guestExt
	ownerSeen []bool
}

func (e *ownerCaptureExt) CallTool(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	e.ownerSeen = append(e.ownerSeen, connector.OwnerCallerOf(ctx))
	return e.guestExt.CallTool(ctx, machine, session, name, args, assistant)
}

func TestMCPCallSetsOwnerCallerOnlyForOwnerAssistantConversations(t *testing.T) {
	_, s, base := guestService(t)
	ext := &ownerCaptureExt{guestExt: base}
	s.SetExtensions(ext)

	// 1. Authenticated owner assistant conversation on personal machine -> OwnerCallerOf == true.
	res := s.mcpCall(context.Background(), "server", "owner-sess", "lark_agent_reply", json.RawMessage(`{}`), true)
	if res.IsError {
		t.Fatalf("owner assistant call failed: %+v", res)
	}
	if len(ext.ownerSeen) != 1 || !ext.ownerSeen[0] {
		t.Fatalf("owner assistant call must carry OwnerCallerOf=true, got %v", ext.ownerSeen)
	}

	// 2. Guest conversation on personal machine -> OwnerCallerOf == false.
	if err := s.guestSet(context.Background(), "server", "guest-sess", []string{"lark_agent_reply"}); err != nil {
		t.Fatal(err)
	}
	res = s.mcpCall(context.Background(), "server", "guest-sess", "lark_agent_reply", json.RawMessage(`{}`), true)
	if res.IsError {
		t.Fatalf("guest call failed: %+v", res)
	}
	if len(ext.ownerSeen) != 2 || ext.ownerSeen[1] {
		t.Fatalf("guest conversation must NOT carry OwnerCallerOf=true, got %v", ext.ownerSeen)
	}

	// 3. Coding Agent call (assistant == false) -> OwnerCallerOf == false.
	res = s.mcpCall(context.Background(), "server", "agent-sess", "lark_agent_reply", json.RawMessage(`{}`), false)
	if res.IsError {
		t.Fatalf("coding agent call failed: %+v", res)
	}
	if len(ext.ownerSeen) != 3 || ext.ownerSeen[2] {
		t.Fatalf("coding agent call must NOT carry OwnerCallerOf=true, got %v", ext.ownerSeen)
	}

	// 4. Meeting room conversation -> OwnerCallerOf == false.
	if err := s.guestSetRoom(context.Background(), "server", "room-sess", "rec-1"); err != nil {
		t.Fatal(err)
	}
	res = s.mcpCall(context.Background(), "server", "room-sess", "lark_agent_reply", json.RawMessage(`{}`), true)
	if res.IsError {
		t.Fatalf("room call failed: %+v", res)
	}
	if len(ext.ownerSeen) != 4 || ext.ownerSeen[3] {
		t.Fatalf("meeting room call must NOT carry OwnerCallerOf=true, got %v", ext.ownerSeen)
	}
}
