package node

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexRewindPreservesNativePaneOperations(t *testing.T) {
	for _, route := range []string{codexRouteTerminal, codexRouteAppServer} {
		t.Run(route, func(t *testing.T) {
			var n *Node
			if route == codexRouteAppServer {
				n, _, _, _ = daemonPane(t)
				n.joinCodexPanes(context.Background())
				waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
			} else {
				n = remoteTestNode(t)
			}
			n.mu.Lock()
			n.agents[remoteRaw] = host.Agent{Agent: "codex", PaneID: "w1:p1", Cwd: n.home, AgentStatus: proto.StatusWorking}
			n.mu.Unlock()
			rows := n.sessionList(context.Background(), false)
			row := byID(decodeSessions(t, rows))[remoteRaw]
			if row.Status != proto.StatusWorking || row.CodexRoute != route {
				t.Fatalf("native row status %q route %q", row.Status, row.CodexRoute)
			}
			caps := row.Capabilities
			if caps == nil || !caps.Read || !caps.Send || !caps.Interrupt || !caps.Approve || !caps.Terminal || caps.Attach {
				t.Fatalf("native operations after rewind decoration: %+v", caps)
			}
			if caps.Rewind == nil || caps.RewindCode == nil || *caps.RewindCode {
				t.Fatalf("rewind metadata: %+v", caps)
			}
		})
	}
}

func TestCodexRewindPreservesExplicitOperationRestrictions(t *testing.T) {
	n := remoteTestNode(t)
	for _, source := range []string{"", "codex_history", "codex_app_server", "runtime", "identity_conflict"} {
		t.Run("source="+source, func(t *testing.T) {
			caps := &proto.SessionCapabilities{Read: true, Terminal: source == ""}
			row := proto.Session{SessionID: remoteRaw, SourceKind: source, Capabilities: caps}
			n.codexRewindRow(remoteRaw, &row)
			if row.Capabilities != caps || caps.Send || caps.Interrupt || caps.Approve || caps.Attach || caps.Terminal != (source == "") {
				t.Fatalf("explicit restrictions after rewind decoration: %+v", row.Capabilities)
			}
		})
	}
}
