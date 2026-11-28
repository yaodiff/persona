package node

import (
	"net"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestSessionStopExternalRuntimeSessions(t *testing.T) {
	newRuntimeNode := func(t *testing.T, rtID, agent, name string, listed bool, makeSessions func(home string) []proto.Session) (*Node, *runtimeConn, string) {
		t.Helper()
		n, _, home := herdrNode(t)
		compSide, nodeSide := net.Pipe()
		t.Cleanup(func() { _ = compSide.Close(); _ = nodeSide.Close() })
		nodePeer := rpc.New("node", nodeSide)
		n.runtimes = newRuntimeRegistry()
		conn := n.runtimes.add(nodePeer)
		conn.ready = true
		conn.sessionsListed = listed
		conn.hello = runtimeHello{Runtime: rtID, Agent: agent, Name: name}
		if makeSessions != nil {
			if sessions := makeSessions(home); len(sessions) > 0 {
				n.runtimes.setSessions(conn, sessions)
			}
		}
		return n, conn, home
	}

	t.Run("live_external_runtime_returns_no_terminal_on_stop_and_not_retirable_on_retire", func(t *testing.T) {
		for _, tc := range []struct {
			rtID  string
			agent string
			name  string
			sid   string
		}{
			{opencodeRuntimeID, opencodeAgent, "OpenCode", "opencode-ses_live_01"},
			{"hermes", "hermes", "Hermes", "hermes-worker-01"},
			{"pi", "pi", "Pi", "pi-session-01"},
			{"paseo", "paseo", "Paseo", "paseo-agent-01"},
		} {
			t.Run(tc.rtID, func(t *testing.T) {
				n, _, _ := newRuntimeNode(t, tc.rtID, tc.agent, tc.name, true, func(home string) []proto.Session {
					return []proto.Session{{SessionID: tc.sid, Agent: tc.agent, Cwd: home, Status: proto.StatusWorking}}
				})
				_, err := request(t, n, "session.stop", map[string]any{"session_id": tc.sid})
				if code, _ := refusalOf(t, err); code != noTerminal {
					t.Fatalf("session.stop got code=%q err=%v, want %q", code, err, noTerminal)
				}
				_, err = request(t, n, "session.stop", map[string]any{"session_id": tc.sid, "retire": true})
				if code, _ := refusalOf(t, err); code != notRetirable {
					t.Fatalf("session.stop(retire=true) got code=%q err=%v, want %q", code, err, notRetirable)
				}
				_, err = request(t, n, "session.retire", map[string]any{"session_id": tc.sid})
				if code, _ := refusalOf(t, err); code != notRetirable {
					t.Fatalf("session.retire got code=%q err=%v, want %q", code, err, notRetirable)
				}
			})
		}
	})

	t.Run("live_external_runtime_assistant_returns_not_retirable", func(t *testing.T) {
		const sid = "opencode-ses_assistant_01"
		n, _, _ := newRuntimeNode(t, opencodeRuntimeID, opencodeAgent, "OpenCode", true, func(home string) []proto.Session {
			return []proto.Session{{SessionID: sid, Agent: opencodeAgent, Cwd: identity.AssistantDir(home), Status: proto.StatusWorking}}
		})
		_, err := request(t, n, "session.stop", map[string]any{"session_id": sid})
		if code, _ := refusalOf(t, err); code != notRetirable {
			t.Fatalf("assistant runtime session.stop got code=%q err=%v, want %q", code, err, notRetirable)
		}
	})

	t.Run("disconnected_known_runtime_session_stays_no_terminal", func(t *testing.T) {
		const sid = "hermes-away-01"
		n, conn, _ := newRuntimeNode(t, "hermes", "hermes", "Hermes", true, func(home string) []proto.Session {
			return []proto.Session{{SessionID: sid, Agent: "hermes", Cwd: home, Status: proto.StatusWorking}}
		})
		n.runtimes.remove(conn)
		_, err := request(t, n, "session.stop", map[string]any{"session_id": sid})
		if code, _ := refusalOf(t, err); code != noTerminal {
			t.Fatalf("disconnected runtime session.stop got code=%q err=%v, want %q", code, err, noTerminal)
		}
		_, err = request(t, n, "session.stop", map[string]any{"session_id": sid, "retire": true})
		if code, _ := refusalOf(t, err); code != notRetirable {
			t.Fatalf("disconnected runtime retire got code=%q err=%v, want %q", code, err, notRetirable)
		}
	})

	t.Run("pending_unlisted_runtime_fails_closed_with_runtime_away", func(t *testing.T) {
		for _, tc := range []struct {
			rtID  string
			agent string
			name  string
			sid   string
		}{
			{opencodeRuntimeID, opencodeAgent, "OpenCode", "opencode-ses_pending"},
			{"hermes", "hermes", "Hermes", "hermes-pending-01"},
		} {
			t.Run(tc.rtID, func(t *testing.T) {
				n, _, _ := newRuntimeNode(t, tc.rtID, tc.agent, tc.name, false, nil)
				_, err := request(t, n, "session.stop", map[string]any{"session_id": tc.sid})
				if code, _ := refusalOf(t, err); code != "runtime_away" {
					t.Fatalf("got code=%q err=%v, want runtime_away", code, err)
				}
			})
		}
	})

	t.Run("auto_companion_pending_until_listed_then_absent_reports_not_running", func(t *testing.T) {
		const sid = "opencode-ses_after_reboot"
		n, _, _ := herdrNode(t)
		n.autoCompanions = true
		n.runtimes = newRuntimeRegistry()

		// Before opencode companion connects, fail closed with runtime_away.
		_, err := request(t, n, "session.stop", map[string]any{"session_id": sid})
		if code, _ := refusalOf(t, err); code != "runtime_away" {
			t.Fatalf("unconnected autoCompanion got code=%q err=%v, want runtime_away", code, err)
		}

		// Once opencode connects and pushes its initial list without sid, stop reports not running.
		compSide, nodeSide := net.Pipe()
		t.Cleanup(func() { _ = compSide.Close(); _ = nodeSide.Close() })
		conn := n.runtimes.add(rpc.New("node", nodeSide))
		conn.ready = true
		conn.hello = runtimeHello{Runtime: opencodeRuntimeID, Agent: opencodeAgent, Name: "OpenCode"}
		n.runtimes.setSessions(conn, nil)

		res, err := request(t, n, "session.stop", map[string]any{"session_id": sid})
		if err != nil || res["stopped"] != false {
			t.Fatalf("absent opencode session after initial list = %v, %v; want stopped=false, err=nil", res, err)
		}
	})

	t.Run("malformed_session_id_still_rejected", func(t *testing.T) {
		n, _, _ := newRuntimeNode(t, opencodeRuntimeID, opencodeAgent, "OpenCode", true, nil)
		for _, bad := range []string{"../evil", "bad id"} {
			_, err := request(t, n, "session.stop", map[string]any{"session_id": bad})
			if err == nil || !strings.Contains(err.Error(), "invalid session id") {
				t.Fatalf("session_id=%q: expected invalid session id, got: %v", bad, err)
			}
		}
	})
}
