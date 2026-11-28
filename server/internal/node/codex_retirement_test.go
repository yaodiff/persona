package node

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexRetirementRechecksOwnerInput(t *testing.T) {
	for _, tc := range []struct {
		name       string
		interrupts int
		keys       int
	}{
		{"before_interrupt", 0, 0},
		{"during_interrupt", 1, 0},
		{"before_second_key", 1, 1},
		{"before_followup_key", 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			withStop(t, 1, 600*time.Millisecond)
			n.codices = newCodexRegistry()
			n.codices.scan = func() []codexProcess { return nil }
			n.configureCodex(proto.CodexConfig{})

			cwd := mkdir(t, filepath.Join(home, "repo"))
			codexHome := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)
			now := time.Now().UTC()
			writeRollout(t, n.home, remoteRaw, cwd, now, now, activeRolloutEvent(now, "task_started"))
			recordInput := func() { n.typing.input(remoteRaw)() }

			rf, _ := connectRemoteFixture(t, n, codexHome, "discovered-09b03a5e", remoteRaw)
			rf.mu.Lock()
			rf.response = func(method string, _ map[string]any) (any, bool) {
				switch method {
				case "thread/read":
					return map[string]any{"thread": map[string]any{
						"id": remoteRaw, "cwd": cwd, "status": map[string]any{"type": "active"},
						"turns": []any{map[string]any{"id": "turn-1", "status": "inProgress"}},
					}}, true
				case "turn/interrupt":
					if tc.name == "during_interrupt" {
						recordInput()
					}
					return map[string]any{}, true
				}
				return nil, false
			}
			rf.mu.Unlock()

			n.bindPane("w1:p1", remoteRaw)
			f.reply("session.snapshot", snapshot(nil, nil, []any{
				agentOf("codex", remoteRaw, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
			}, nil))
			listSessions(t, n)
			f.reset()
			if tc.name == "before_interrupt" {
				recordInput()
			}

			var keys atomic.Int32
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_keys" && keys.Add(1) == int32(tc.keys) {
					recordInput()
				}
			})
			out, err := request(t, n, nodeproto.StopIfIdle, map[string]any{
				"session_id": remoteRaw, "retire": true, "stop_cause": "task_completed",
			})
			if got := rf.count("turn/interrupt"); got != tc.interrupts {
				t.Errorf("protocol interrupts = %d, want %d", got, tc.interrupts)
			}
			if got := len(f.trace("pane.send_keys")); got != tc.keys {
				t.Errorf("Ctrl+C sends = %d, want %d", got, tc.keys)
			}
			if err == nil {
				t.Fatalf("retirement did not refuse owner input: %v", out)
			}
			if code, _ := refusalOf(t, err); code != notRetirable {
				t.Fatalf("retirement refusal = %v, want %s", err, notRetirable)
			}
			if st, _ := n.state.get(remoteRaw); st.StopCause.AllowsSupervisorResume() {
				t.Fatal("refused retirement granted automatic recovery")
			}
			closesNothing(t, f)
		})
	}
}
