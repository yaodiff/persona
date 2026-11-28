package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestFinishedPushSourceAncestry(t *testing.T) {
	for _, tc := range []struct {
		name string
		sess proto.Session
		want int
	}{
		{"codex parent", proto.Session{Agent: "codex", ParentSessionID: "root"}, 0},
		{"codex nested orphan", proto.Session{SourceKind: "codex_app_server", ParentThreadID: "missing", RootSessionID: "root"}, 0},
		{"codex explicit flag", proto.Session{Agent: "codex", IsSubagent: true}, 0},
		{"codex root metadata", proto.Session{Agent: "codex", RootSessionID: "root"}, 0},
		{"claude runtime child", proto.Session{Agent: "claude", ParentSessionID: "root"}, 0},
		{"ordinary claude", proto.Session{Agent: "claude"}, 1},
		{"misleading title", proto.Session{Agent: "codex", Title: "Subagent worker", RootSessionID: "subject"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			work := tc.sess
			work.SessionID, work.Status, work.UpdatedAt = "subject", proto.StatusWorking, time.Now()
			ft.sessions("mac", work)
			done := work
			done.Status, done.LastText = proto.StatusDone, "Verified result"
			ft.sessions("mac", done)
			got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond)
			if len(got) != tc.want {
				t.Fatalf("completion pushes = %+v, want %d", got, tc.want)
			}
			kept := listed(t, ft.s, "mac", "subject")
			if kept.Status != proto.StatusDone || kept.LastText != done.LastText || kept.ParentSessionID != done.ParentSessionID {
				t.Fatalf("completion filtering changed session state/report: %+v", kept)
			}
			// Completion suppression is not a session mute: both necessary cards still notify.
			for _, kind := range []string{proto.IxPermission, proto.IxQuestion} {
				ft.s.announce(proto.Interaction{ID: kind, MachineID: "mac", SessionID: "subject", Kind: kind, OutwardWrite: kind == proto.IxPermission,
					ToolName: "Bash", Questions: []proto.Question{{Question: "Which option?"}}})
			}
			if got := ft.pushesOf(assistantwire.NotifyInput, 2, time.Second); len(got) != 2 {
				t.Fatalf("necessary cards suppressed: %+v", got)
			}
		})
	}
}

func TestFinishedPushWaitsForNativeChildren(t *testing.T) {
	for _, agent := range []string{"codex", "claude"} {
		t.Run(agent, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			root := proto.Session{SessionID: "root", Agent: agent, Status: proto.StatusWorking, UpdatedAt: time.Now(), Rev: 10}
			child := proto.Session{SessionID: "child", Agent: agent, ParentSessionID: "root", Status: proto.StatusWorking, UpdatedAt: time.Now()}
			publish := func() {
				if agent == "claude" {
					root.Subagents = []proto.Subagent{{ID: "child", Running: child.Status == proto.StatusWorking, LastText: child.LastText}}
					ft.sessions("mac", root)
				} else {
					ft.sessions("mac", root, child)
				}
			}
			publish()
			root.Status, root.LastText = proto.StatusDone, "Waiting for child report"
			publish()
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(got) != 0 {
				t.Fatalf("parent notified before child finished: %+v", got)
			}
			child.Status, child.LastText = proto.StatusDone, "Child result"
			root.Status, root.WokeByTask = proto.StatusWorking, true
			publish()
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(got) != 0 {
				t.Fatalf("child completion notified: %+v", got)
			}
			root.Status, root.LastText, root.Rev = proto.StatusDone, "All work verified", 20
			if agent == "claude" {
				ft.event("mac", "hook.event", map[string]any{"session_id": "root", "event": "Stop", "stop_sig": "final-result"})
			}
			publish()
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].SessionID != "root" || got[0].Body != root.LastText {
				t.Fatalf("overall completion = %+v", got)
			}
			// Reconnect/status replay of the same completion must not make a second alert.
			root.WokeByTask, root.Status = false, proto.StatusWorking
			publish()
			root.Status = proto.StatusDone
			publish()
			if got := ft.pushesOf(assistantwire.NotifyDone, 2, 200*time.Millisecond); len(got) != 1 {
				t.Fatalf("duplicate completion = %+v", got)
			}
			// A real later turn can produce identical text and must still notify.
			ft.event("mac", "hook.event", map[string]any{"session_id": "root", "event": "UserPromptSubmit"})
			root.Status, root.Rev = proto.StatusWorking, 30
			publish()
			root.Status, root.Rev = proto.StatusDone, 40
			publish()
			if got := ft.pushesOf(assistantwire.NotifyDone, 2, time.Second); len(got) != 2 {
				t.Fatalf("new turn suppressed: %+v", got)
			}
		})
	}
}

func TestCompletionChildrenUseScopedAncestry(t *testing.T) {
	ft := newFleetTestNoStore(t)
	root := proto.Session{SessionID: "home-a-root", SourceThreadID: "raw-root"}
	for _, tc := range []struct {
		name string
		list []proto.Session
		want bool
	}{
		{"nested", []proto.Session{{SessionID: "middle", ParentSessionID: root.SessionID}, {SessionID: "child", ParentSessionID: "middle", Status: proto.StatusWorking}}, true},
		{"missing intermediate", []proto.Session{{SessionID: "child", ParentSessionID: "missing", RootSessionID: root.SessionID, Status: proto.StatusWorking}}, true},
		{"child approval", []proto.Session{{SessionID: "child", ParentSessionID: root.SessionID, Status: proto.StatusBlocked}}, true},
		{"other home", []proto.Session{{SessionID: "child", ParentSessionID: "home-b-root", ParentThreadID: "raw-root", Status: proto.StatusWorking}}, false},
		{"cycle", []proto.Session{{SessionID: "a", ParentSessionID: "b", Status: proto.StatusWorking}, {SessionID: "b", ParentSessionID: "a"}}, false},
		{"finished", []proto.Session{{SessionID: "child", ParentSessionID: root.SessionID, Status: proto.StatusDone}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft.d.setSessions("mac", append([]proto.Session{root}, tc.list...))
			if got := ft.s.completionChildrenRunning("mac", root); got != tc.want {
				t.Fatalf("running children = %v, want %v", got, tc.want)
			}
		})
	}
}
