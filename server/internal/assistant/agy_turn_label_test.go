package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyTerminalReadLabelsRequireNativeEvidence(t *testing.T) {
	for _, state := range []string{proto.StateWorking, proto.StateUnread} {
		row := proto.Session{Agent: "agy", PaneID: "w1:p1", State: state}
		if agentsListState(row) != proto.StateUnknown || agentsListLabel(row) != stateNames[proto.StateUnknown] {
			t.Fatalf("unverified engine state %s leaked into Agent tools", state)
		}
		if snapshotAgentGroup(row, time.Now()) != snapshotAgentOutside {
			t.Fatalf("unverified engine state %s leaked into the Assistant snapshot", state)
		}
		row.NativeTurnVerified = true
		if agentsListState(row) != state || agentsListLabel(row) != stateNames[state] {
			t.Fatalf("verified %s was lost", state)
		}
		row.NativeTurnVerified, row.PaneID = false, ""
		if agentsListState(row) != state || agentsListLabel(row) != stateNames[state] {
			t.Fatalf("headless AGY state %s was changed", state)
		}
	}
	for _, agent := range []string{"claude", "codex"} {
		row := proto.Session{Agent: agent, PaneID: "w1:p1", State: proto.StateWorking}
		if agentsListState(row) != proto.StateWorking {
			t.Fatalf("%s engine state was changed", agent)
		}
	}
}

func TestAgyTerminalToolsShareLatestReplyAndState(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	const reply = "Example reply."
	row := proto.Session{SessionID: "agy-w1-p1", Agent: "agy", PaneID: "w1:p1", State: proto.StateUnread,
		Status: proto.StatusDone, UpdatedAt: time.Now(), LastText: reply, Work: &proto.WorkState{Phase: proto.WorkDone}}
	f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: reply})
	for _, verified := range []bool{false, true} {
		row.NativeTurnVerified = verified
		f.setSessions("box", row)
		want := stateNames[proto.StateUnknown]
		if verified {
			want = stateNames[proto.StateUnread]
		}
		read, err := s.toolAgentRead(ctx, "box", row.SessionID, 1, "")
		if err != nil || !strings.Contains(read, "("+want+",") || !strings.Contains(read, reply) {
			t.Fatalf("agent_read did not show %q and the latest reply: %q, %v", want, read, err)
		}
		list := s.describeAgents(ctx, "active", "box", "")
		if !strings.Contains(list, "["+want+"]") || !strings.Contains(list, reply) {
			t.Fatalf("agents_list did not show %q and the latest reply: %q", want, list)
		}
		done := s.describeAgents(ctx, "done", "box", "")
		if strings.Contains(done, "session_id="+row.SessionID) != verified {
			t.Fatalf("agents_list done with verified=%t: %q", verified, done)
		}
	}
}
