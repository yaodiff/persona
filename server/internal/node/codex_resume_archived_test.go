package node

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestStoppedCodexNativeArchiveAndGuardedResume(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	n, f, cwd := codexHistoryNode(t)
	provenanceHerdr(n)
	withStop(t, 1, 50*time.Millisecond)
	n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, Model: "gpt-7",
		AppMode: "plan", Pane: "w1:p1", StartedAt: time.Now().Add(-time.Hour)})
	n.agents[startThread] = host.Agent{Agent: "codex", PaneID: "w1:p1", TabID: "w1:t1",
		WorkspaceID: "w1", AgentStatus: proto.StatusIdle}
	quitsOn(f, "w1:p1", startThread, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, startThread))
	if res, err := request(t, n, "session.stop", map[string]any{
		"session_id": startThread, "retire": true, "stop_cause": proto.StopTaskCompleted,
	}); err != nil || res["stopped"] != true {
		t.Fatalf("stop did not complete: %v %v", res, err)
	}
	if info, err := n.sessionInfo(t.Context(), startThread); err != nil || info["stop_cause"] != proto.StopTaskCompleted {
		t.Fatalf("post-stop provenance: %v %v", info, err)
	}
	commands := stubCodexArchiveCommand(t)
	if res, err := request(t, n, "session.archive", map[string]any{"session_id": startThread}); err != nil || res["native"] != nativeArchiveDone {
		t.Fatalf("native archive: %v %v", res, err)
	}
	// Its terminal has since gone too: only the archived rollout and saved node state remain.
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if _, listed := byID(listSessions(t, n))[startThread]; listed {
		t.Fatal("native archived conversation is still in the live inventory")
	}
	if info, err := n.sessionInfo(t.Context(), startThread); err != nil || info["stop_cause"] != proto.StopTaskCompleted {
		t.Fatalf("native archival lost authoritative stop evidence: %v %v", info, err)
	}
	f.reset()
	got := resumeFile(f, cwd)
	if _, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "resume_automatic": true, "prompt": "Continue the assigned task",
	}); err != nil {
		t.Fatal(err)
	}
	commands.mu.Lock()
	runs := commands.runs
	commands.mu.Unlock()
	if len(runs) != 2 || runs[0][2] != "archive" || runs[1][2] != "unarchive" ||
		runs[0][3] != startThread || runs[1][3] != startThread || archiveIn(t, n.codexHome(), startThread) {
		t.Fatalf("native stop/archive/resume handoff failed: %v", runs)
	}
	file := got()
	args := strings.Join(file.Argv, " ")
	if file.Prompt != "Continue the assigned task" || !strings.Contains(args, "resume "+startThread) ||
		!strings.Contains(args, "--sandbox read-only") || !strings.Contains(args, "--model gpt-7") {
		t.Fatalf("resume lost identity, prompt or launch constraints: %+v", file)
	}
}

func TestArchivedCodexResumeRestoresBeforeOpeningTerminal(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	for _, tc := range []struct {
		name      string
		automatic bool
		server    bool
		archived  bool
	}{
		{"supervisor command", true, false, true},
		{"owner command", false, false, true},
		{"supervisor app server", true, true, true},
		{"already restored", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, cwd := codexHistoryNode(t)
			provenanceHerdr(n)
			listSessions(t, n)
			home := canonicalCodexHome(n.codexHome(), n.home)
			n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, Model: "gpt-7",
				AppMode: "plan", StopCause: proto.StopTaskCompleted})
			command := stubCodexArchiveCommand(t)
			var server *remoteFixture
			if tc.server {
				server, _ = connectRemoteFixture(t, n, home, "resume-archive")
				server.mu.Lock()
				server.archive = func(method, thread string) {
					moveCodexRollout(t, home, thread, method == "thread/archive")
				}
				server.mu.Unlock()
			}
			if tc.archived {
				moveCodexRollout(t, home, startThread, true)
			}
			f.reset()
			got := resumeFile(f, cwd)
			f.after(func(c herdrCall) {
				if c.Method == "tab.create" && codexArchived(home, n.codexPath(startThread)) {
					t.Error("opened Codex while still archived: its confirmation would block the prompt")
				}
			})
			res, err := request(t, n, "session.start", map[string]any{
				"resume_session_id": startThread, "resume_automatic": tc.automatic, "prompt": "Continue the assigned task",
			})
			if err != nil {
				t.Fatal(err)
			}
			if s, _ := res["session"].(map[string]any); s == nil || s["session_id"] != startThread {
				t.Fatalf("resumed another conversation: %v", res)
			}
			if archiveIn(t, home, startThread) {
				t.Fatal("the resumed thread is still natively archived")
			}
			command.mu.Lock()
			runs := command.runs
			command.mu.Unlock()
			switch {
			case tc.server:
				if server.count("thread/unarchive") != 1 || len(runs) != 0 {
					t.Fatalf("did not use the existing app server: calls=%d commands=%v", server.count("thread/unarchive"), runs)
				}
			case tc.archived:
				if len(runs) != 1 || runs[0][0] != "cx" || runs[0][1] != home ||
					runs[0][2] != "unarchive" || runs[0][3] != startThread {
					t.Fatalf("wrong unarchive command: %v", runs)
				}
			default:
				if len(runs) != 0 {
					t.Fatalf("unarchived a thread already restored: %v", runs)
				}
			}
			file := got()
			args := strings.Join(file.Argv, " ")
			if file.Prompt != "Continue the assigned task" || !strings.Contains(args, "resume "+startThread) ||
				!strings.Contains(args, "--sandbox read-only") || !strings.Contains(args, "--model gpt-7") ||
				strings.Contains(args, "--dangerously-bypass") {
				t.Fatalf("resume lost its prompt, identity or original constraints: %+v", file)
			}
		})
	}
}

func TestArchivedCodexResumeRefusesFailedUnarchive(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	for _, tc := range []struct {
		name string
		fail bool
		out  string
	}{
		{"failed", true, "unarchive failed"},
		{"answered without restoring", false, ""},
		{"unsupported", true, "error: unrecognized subcommand 'unarchive'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, cwd := codexHistoryNode(t)
			provenanceHerdr(n)
			listSessions(t, n)
			n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, StopCause: proto.StopTaskCompleted})
			moveCodexRollout(t, n.codexHome(), startThread, true)
			command := stubCodexArchiveCommand(t)
			command.move, command.fail, command.out = false, tc.fail, tc.out
			f.reset()
			resumeFile(f, cwd)
			_, err := request(t, n, "session.start", map[string]any{
				"resume_session_id": startThread, "resume_automatic": true, "prompt": "Continue",
			})
			if code, _ := refusalOf(t, err); code != nativeArchiveFailed {
				t.Fatalf("did not fail closed on native unarchive: %v", err)
			}
			if !archiveIn(t, n.codexHome(), startThread) {
				t.Fatal("a failed unarchive changed the archived thread")
			}
			if calls := f.trace("tab.create", "workspace.create", "pane.send_input"); len(calls) != 0 {
				t.Fatalf("started a terminal despite failed unarchive: %v", calls)
			}
		})
	}
}

func TestArchivedCodexResumeChecksStopCauseBeforeUnarchive(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	for _, cause := range []proto.StopCause{"", proto.StopUnknown, proto.StopUser, "future"} {
		t.Run(string(cause), func(t *testing.T) {
			n, f, cwd := codexHistoryNode(t)
			provenanceHerdr(n)
			listSessions(t, n)
			n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, StopCause: cause})
			moveCodexRollout(t, n.codexHome(), startThread, true)
			command := stubCodexArchiveCommand(t)
			f.reset()
			_, err := request(t, n, "session.start", map[string]any{
				"resume_session_id": startThread, "resume_automatic": true, "prompt": "Continue",
			})
			if code, _ := refusalOf(t, err); code != "resume_not_allowed" {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if len(command.runs) != 0 || !archiveIn(t, n.codexHome(), startThread) {
				t.Fatalf("a refused resume touched the native archive: %v", command.runs)
			}
			if len(f.trace("tab.create", "pane.send_input")) != 0 {
				t.Fatal("a refused resume opened a terminal")
			}
		})
	}
}

func TestArchivedCodexResumeChecksHoldersBeforeUnarchive(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	n, f, cwd := codexHistoryNode(t)
	provenanceHerdr(n)
	listSessions(t, n)
	n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, StopCause: proto.StopTaskCompleted})
	moveCodexRollout(t, n.codexHome(), startThread, true)
	command := stubCodexArchiveCommand(t)
	n.codices.mu.Lock()
	n.codices.checked = func() ([]codexProcess, bool) { return []codexProcess{{pid: os.Getpid()}}, true }
	n.codices.mu.Unlock()
	held, err := os.Open(n.codexPath(startThread))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	f.reset()
	_, err = request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "resume_automatic": true, "prompt": "Continue",
	})
	if code, _ := refusalOf(t, err); code != codexResumeRefused {
		t.Fatalf("resumed a thread held open: %v", err)
	}
	if len(command.runs) != 0 || !archiveIn(t, n.codexHome(), startThread) {
		t.Fatal("unarchived a thread held by another process")
	}
	if len(f.trace("tab.create", "pane.send_input")) != 0 {
		t.Fatal("started a second writer")
	}
}
