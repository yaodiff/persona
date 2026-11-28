package node

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise one host snapshot directly in the adapter tests.
func (n *Node) traexAgents(ctx context.Context, panes, agents []host.Agent) []host.Agent {
	return n.traexSnapshotAgents(ctx, panes, agents, &traexHostEvidence{})
}

func freshTraexLogs(t *testing.T, n *Node, rows string) {
	t.Helper()
	traexHistoryRevisionSQL(t, filepath.Join(n.traexHome(), "logs_2.sqlite"),
		"CREATE TABLE logs(ts INTEGER, ts_nanos INTEGER, process_uuid TEXT, target TEXT, thread_id TEXT);"+rows)
}

func freshTraexRecord(pid int, thread, target string, at time.Time) string {
	return fmt.Sprintf("INSERT INTO logs VALUES(%d,%d,'pid:%d:aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee','%s','%s');",
		at.Unix(), at.Nanosecond(), pid, target, thread)
}

func TestTraexFreshForegroundEvidence(t *testing.T) {
	pid := os.Getpid()
	start, ok := traexLogStart(pid)
	if !ok {
		t.Fatal("missing kernel process start")
	}
	const title, target = "Inspect the export", "traex_tui::app"
	record := func(thread string, at time.Time) string { return freshTraexRecord(pid, thread, target, at) }
	good := record(threadMain, start.Add(time.Microsecond))
	for _, tc := range []struct {
		name, title, stateSQL, logs string
		want                        string
	}{
		{"same thread after start", title, "", good, threadMain},
		{"missing log", title, "", "", ""},
		{"title prefix", "Inspect the exp", "", good, ""},
		{"directory suffix", title + " | app", "", good, threadMain},
		{"unrelated directory", title + " | other", "", good, ""},
		{"separator inside name", "Inspect | export | app", "UPDATE threads SET name='Inspect | export' WHERE id='" + threadMain + "';", good, threadMain},
		{"ambiguous suffix", title + " | app", "UPDATE threads SET archived=0,name='Inspect the export | app' WHERE id='" + threadOld + "';", good, ""},
		{"legacy history", title, "UPDATE threads SET history_mode='legacy' WHERE id='" + threadMain + "';", good, threadMain},
		{"case differs", "inspect the export", "", good, ""},
		{"duplicate title", title, "UPDATE threads SET archived=0,name='Inspect the export' WHERE id='" + threadOld + "';", good, ""},
		{"two process tokens", title, "", good + strings.Replace(good, "aaaaaaaa-bbbb", "bbbbbbbb-aaaa", 1), ""},
		{"another thread", title, "", record(threadOld, start.Add(time.Microsecond)), ""},
		{"conflicting threads", title, "", good + record(threadOld, start.Add(2*time.Microsecond)), ""},
		{"prior process with same PID", title, "", record(threadMain, start.Add(-time.Nanosecond)), ""},
		{"at start", title, "", record(threadMain, start), ""},
		{"PID prefix collision", title, "", freshTraexRecord(pid*10+1, threadMain, target, time.Now()), ""},
		{"daemon log", title, "", freshTraexRecord(pid, threadMain, "traex_app_server::thread", time.Now()), ""},
		{"subagent source", "Review a tool", "", record(threadGuardian, time.Now()), ""},
		{"subagent thread source", title, "UPDATE threads SET thread_source='subagent' WHERE id='" + threadMain + "';", good, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _, hc, _ := hcNode(t)
			traexHistoryFixture(t, n, nil)
			traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT;"+tc.stateSQL)
			freshTraexLogs(t, n, tc.logs)
			withProcessArgvStub(t, func(int) ([]string, error) { return []string{"traecli"}, nil })
			term := hcTraexTerminal("fresh", "traex", "working", pid, []string{"traecli"}, "/work/app", "s1", "tab1", "A")
			term["title"] = tc.title
			hc.setTerminals(term)
			p := host.Agent{Agent: traexAgent, Cwd: "/work/app", PaneID: "herdr-core:fresh", Title: tc.title, RawTitle: tc.title, ForegroundPID: pid, ForegroundArgv: []string{"traecli"}, AgentStatus: proto.StatusWorking}
			agents := []host.Agent{p}
			added := n.traexAgents(context.Background(), []host.Agent{p}, agents)
			if got := agents[0].SessionID(); got != tc.want || len(added) != 0 {
				t.Fatalf("session = %q, want %q; extra rows = %d", got, tc.want, len(added))
			}
			if tc.want == "" {
				return
			}
			withProcessArgvStub(t, func(int) ([]string, error) { return []string{"traecli", "app-server"}, nil })
			if added := n.traexAgents(context.Background(), []host.Agent{p}, nil); len(added) != 0 {
				t.Fatal("a foreground daemon inherited the previous client's evidence")
			}
		})
	}
}

func TestTraexFreshHerdrSessionList(t *testing.T) {
	n, f, home := herdrNode(t)
	traexHistoryFixture(t, n, []historyTurn{{thread: threadMain, id: "turn1", status: "completed", ordinal: 0,
		items: [][2]string{userItem("u", "Inspect the export"), answerItem("a", "The export is ready.")}}})
	traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT; UPDATE threads SET source='cli' WHERE name='Review a tool'; UPDATE threads SET archived=0 WHERE id='"+threadOld+"';")
	pid := os.Getpid()
	start, ok := traexLogStart(pid)
	if !ok {
		t.Fatal("missing kernel process start")
	}
	freshTraexLogs(t, n, freshTraexRecord(pid, threadMain, "traex_tui::chatwidget", start.Add(time.Millisecond)))
	for _, id := range []string{threadMain, threadOld, threadGuardian} {
		writeTraexOwnerWithName(t, home, id, os.Getppid(), "traecli app-server", time.Now())
	}
	withProcessArgvStub(t, func(pid int) ([]string, error) {
		if pid == os.Getpid() {
			return []string{"traecli"}, nil
		}
		return []string{"traecli", "resume", threadOld}, nil
	})
	f.handle("pane.process_info", func(params map[string]any) any {
		pid := os.Getpid()
		if str(params["pane_id"]) == "w1:p2" {
			pid++
		}
		return map[string]any{"process_info": map[string]any{"foreground_processes": []any{map[string]any{"pid": pid, "name": "traecli"}}}}
	})
	fresh := agentOf("traecli", "", proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/work/app", "Inspect the export")
	resumed := agentOf("traecli", "", proto.StatusDone, "w1:p2", "w1:t2", "w1", "/work/app", "Inspect the export")
	f.reply("session.snapshot", snapshot(nil, nil, []any{fresh, resumed}, []any{fresh, resumed}))
	sessions := listSessions(t, n)
	if len(sessions) != 3 {
		t.Fatalf("want two terminal rows and one offline history, got %v", ids(sessions))
	}
	rows := byID(sessions)
	if a, b := rows[threadMain], rows[threadOld]; a.PaneID != "w1:p1" || a.Status != proto.StatusWorking || b.PaneID != "w1:p2" || b.Status != proto.StatusDone {
		t.Fatalf("fresh = %s/%s, resumed = %s/%s", a.PaneID, a.Status, b.PaneID, b.Status)
	}
	if daemon := rows[threadGuardian]; daemon.PaneID != "" || daemon.Status != proto.StatusOffline {
		t.Fatalf("daemon-only history acquired a terminal: %s/%s", daemon.PaneID, daemon.Status)
	}
	if id := n.traexLiveThread("w1:p1", ""); id != threadMain {
		t.Fatalf("fresh start confirmation = %q", id)
	}
	changed := agentOf("traecli", "", proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/work/app", "A different conversation")
	f.reply("session.snapshot", snapshot(nil, nil, []any{changed, resumed}, []any{changed, resumed}))
	if id := n.traexLiveThread("w1:p1", ""); id != "" {
		t.Fatalf("start reconfirmed %q from the previous title", id)
	}
	// A second snapshot reports the same terminal's completed turn. Exiting
	// the terminal later returns that same row to local history.
	finished := agentOf("traecli", "", proto.StatusDone, "w1:p1", "w1:t1", "w1", "/work/app", "Inspect the export")
	f.reply("session.snapshot", snapshot(nil, nil, []any{finished, resumed}, []any{finished, resumed}))
	completed := listSessions(t, n)
	final := byID(completed)[threadMain]
	if len(completed) != 3 || final.PaneID != "w1:p1" || final.Status != proto.StatusDone || final.LastText != "The export is ready." || final.UpdatedAt.IsZero() {
		t.Fatalf("completion: %d rows, %q/%s %q at %v", len(completed), final.PaneID, final.Status, final.LastText, final.UpdatedAt)
	}
	traexHistoryRevisionSQL(t, filepath.Join(n.traexHome(), "logs_2.sqlite"), "DELETE FROM logs;")
	if id := n.traexLiveThread("w1:p1", ""); id != "" {
		t.Fatalf("shared writer reconfirmed a stale fresh binding: %q", id)
	}
	f.reply("session.snapshot", snapshot(nil, nil, []any{resumed}, []any{resumed}))
	stopped := listSessions(t, n)
	offline := byID(stopped)[threadMain]
	if len(stopped) != 3 || offline.PaneID != "" || offline.Status != proto.StatusOffline || offline.LastText != final.LastText || !offline.UpdatedAt.Equal(final.UpdatedAt) {
		t.Fatalf("after exit: %d rows, %q/%s %q at %v", len(stopped), offline.PaneID, offline.Status, offline.LastText, offline.UpdatedAt)
	}
}

func TestTraexFreshClaimsAcrossHosts(t *testing.T) {
	n, _, _ := herdrNode(t)
	fresh := host.Agent{Agent: traexAgent, PaneID: "w1:p1", AgentSession: &host.AgentSession{Kind: "id", Value: threadMain, Source: traexForegroundSource}}
	other := host.Agent{Agent: traexAgent, PaneID: "herdr-core:t1", AgentSession: &host.AgentSession{Kind: "id", Value: threadMain}}
	for _, source := range []string{"herdr", traexForegroundSource, traexMultiWriterSource} {
		other.AgentSession.Source = source
		for _, agents := range [][]host.Agent{{fresh, other}, {other, fresh}} {
			n.setAgents(agents)
			want := other.PaneID
			switch source {
			case traexForegroundSource:
				want = ""
			case traexMultiWriterSource:
				want = fresh.PaneID
			}
			if got := n.agents[threadMain].PaneID; got != want {
				t.Fatalf("other source %q: pane = %q, want %q", source, got, want)
			}
		}
	}
}

func TestTraexFreshDetectsGenericSharedWriter(t *testing.T) {
	n, _, hc, home := hcNode(t)
	traexHistoryFixture(t, n, nil)
	traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT; UPDATE threads SET archived=0 WHERE id='"+threadOld+"';")
	pidA, pidB := os.Getpid(), os.Getppid()
	now := time.Now().Add(2 * time.Second)
	freshTraexLogs(t, n, freshTraexRecord(pidA, threadMain, "traex_tui::app", now)+freshTraexRecord(pidB, threadOld, "traex_tui::app", now))
	withProcessArgvStub(t, func(pid int) ([]string, error) { return []string{"traecli"}, nil })
	writer := exec.Command(os.Args[0], "-test.run=^TestTraexOwnerHelper$")
	writer.Env = append(os.Environ(), "PERSONA_TRAEX_OWNER_HELPER=1")
	if err := writer.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Process.Kill(); _ = writer.Wait() })
	writerPID := writer.Process.Pid
	seedTraexPane(writerPID, "herdr-core:t1")
	writeTraexOwnerWithName(t, home, threadMain, writerPID, "generic", now.Add(-time.Minute))
	writeTraexOwnerWithName(t, home, threadOld, writerPID, "generic", now)
	a := hcTraexTerminal("t1", "traex", "working", pidA, []string{"traecli"}, "/work/app", "s1", "tab1", "A")
	b := hcTraexTerminal("t2", "traex", "done", pidB, []string{"traecli"}, "/work/app", "s1", "tab2", "B")
	a["title"], b["title"] = "⠸ Inspect the export | /work/app", "Archived"
	hc.setTerminals(a, b)
	// Count reads of the evidence databases in a full two-terminal snapshot.
	// Titles travel through stdin, so the title query has no trailing SQL argument.
	queryLog := filepath.Join(t.TempDir(), "reads")
	if runtime.GOOS != "windows" {
		sqlite := sqlite3WithJSON(t)
		bin := t.TempDir()
		wrapper := `#!/bin/sh
case "$*" in
  *"GROUP BY process_uuid"*) echo logs >> "$TRAEX_QUERY_LOG" ;;
  *"-readonly"*"state_5.sqlite") echo state >> "$TRAEX_QUERY_LOG" ;;
esac
exec "$TRAEX_TEST_SQLITE" "$@"
`
		if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte(wrapper), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("TRAEX_TEST_SQLITE", sqlite)
		t.Setenv("TRAEX_QUERY_LOG", queryLog)
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	rows := byID(listSessions(t, n))
	if rows[threadMain].PaneID != "herdr-core:t1" || rows[threadOld].PaneID != "herdr-core:t2" {
		t.Fatalf("A in %q, B in %q", rows[threadMain].PaneID, rows[threadOld].PaneID)
	}
	if runtime.GOOS != "windows" {
		reads, err := os.ReadFile(queryLog)
		if err != nil || string(reads) != "state\nlogs\n" {
			t.Fatalf("snapshot evidence reads = %q (%v)", reads, err)
		}
	}
}

func TestTraexFreshRequiresReportedForeground(t *testing.T) {
	n, _, hc, _ := hcNode(t)
	traexHistoryFixture(t, n, nil)
	traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT;")
	pid := os.Getpid()
	freshTraexLogs(t, n, freshTraexRecord(pid, threadMain, "traex_tui::app", time.Now().Add(2*time.Second)))
	withProcessArgvStub(t, func(int) ([]string, error) { return []string{"traecli"}, nil })
	wrapper := hcTerminal("t1", "traex", "working", pid+1, "bash", "/work/app", "s1", "tab1", "A")
	wrapper["title"] = "Inspect the export"
	hc.setTerminals(wrapper)
	// Windows inventory can replace that wrapper with a descendant. It is a useful
	// discovery hint, but the terminal itself has never named this PID as foreground.
	p := host.Agent{Agent: traexAgent, PaneID: "herdr-core:t1", ForegroundPID: pid, ForegroundArgv: []string{"traecli"}, RawTitle: "Inspect the export", AgentStatus: proto.StatusWorking}
	if added := n.traexAgents(context.Background(), []host.Agent{p}, nil); len(added) != 0 {
		t.Fatal("a descendant of the foreground wrapper became the live terminal")
	}
}

func TestTraexFreshIdleSource(t *testing.T) {
	p := host.Agent{Agent: traexAgent, PaneID: "w1:p1", AgentStatus: proto.StatusIdle}
	if got := traexAppliedStatus(p, traexForegroundSource); got != "" {
		t.Fatalf("unconfirmed Idle bypassed screen detection: %q", got)
	}
	p.AgentSession = &host.AgentSession{Source: "herdr:traecli"}
	if got := traexAppliedStatus(p, traexForegroundSource); got != proto.StatusIdle {
		t.Fatalf("native Idle became %q", got)
	}
	p.AgentSession = &host.AgentSession{Source: resumeSource}
	if got := traexAppliedStatus(p, traexForegroundSource); got != "" {
		t.Fatalf("synthetic Idle bypassed screen detection: %q", got)
	}
}

func TestTraexFreshWriterAcrossRawSnapshots(t *testing.T) {
	for _, split := range []bool{false, true} {
		for _, multi := range []bool{false, true} {
			t.Run(fmt.Sprintf("split_%v_multi_%v", split, multi), func(t *testing.T) {
				n, f, home := herdrNode(t)
				hc := newFakeHC(t)
				if split {
					n.hosts.Set(n.hosts.Get(host.Herdr), hcHost(hc))
				}
				traexHistoryFixture(t, n, nil)
				traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT; UPDATE threads SET archived=0 WHERE id='"+threadOld+"';")
				pid, writer := os.Getpid(), os.Getppid()
				now := time.Now().Add(2 * time.Second)
				freshTraexLogs(t, n, freshTraexRecord(pid, threadOld, "traex_tui::app", now))
				withProcessArgvStub(t, func(got int) ([]string, error) {
					if got == writer && multi {
						return []string{"generic-daemon"}, nil
					}
					return []string{"traecli"}, nil
				})
				seedTraexPane(writer, "w1:p1")
				writeTraexOwnerWithName(t, home, threadOld, writer, "generic", now.Add(-time.Minute))
				if multi {
					writeTraexOwnerWithName(t, home, threadMain, writer, "generic", now)
				}
				a := agentOf("traecli", "", proto.StatusIdle, "w1:p1", "w1:t1", "w1", "/work/app", "Shell")
				b := agentOf("traecli", "", proto.StatusWorking, "w1:p2", "w1:t2", "w1", "/work/app", "Archived")
				panes := []any{a, b}
				freshPane := "w1:p2"
				if split {
					panes = []any{a}
					c := hcTraexTerminal("t2", "traex", "working", pid, []string{"traecli"}, "/work/app", "s1", "tab2", "B")
					c["title"] = "Archived"
					hc.setTerminals(c)
					freshPane = "herdr-core:t2"
				}
				f.reply("session.snapshot", snapshot(nil, nil, panes, panes))
				f.handle("pane.process_info", func(params map[string]any) any {
					fg := pid
					if str(params["pane_id"]) == "w1:p1" {
						fg = writer
						if multi {
							fg = 0
						}
					}
					return map[string]any{"process_info": map[string]any{"foreground_processes": []any{map[string]any{"pid": fg}}}}
				})
				rows := byID(listSessions(t, n))
				want := "w1:p1"
				if multi {
					want = freshPane
				}
				if rows[threadMain].PaneID != "" || rows[threadOld].PaneID != want {
					t.Fatalf("sibling in %q, matched thread in %q, want %q", rows[threadMain].PaneID, rows[threadOld].PaneID, want)
				}
			})
		}
	}
}

func TestTraexFreshIncompleteForegroundCheck(t *testing.T) {
	n, f, _ := herdrNode(t)
	traexHistoryFixture(t, n, nil)
	traexHistoryRevisionSQL(t, codexAppDBPath(n.traexHome()), "ALTER TABLE threads ADD COLUMN thread_source TEXT;")
	a, b := os.Getpid(), os.Getppid()
	now := time.Now().Add(2 * time.Second)
	freshTraexLogs(t, n, freshTraexRecord(a, threadMain, "traex_tui::app", now)+freshTraexRecord(b, threadMain, "traex_tui::app", now))
	withProcessArgvStub(t, func(int) ([]string, error) { return []string{"traecli"}, nil })
	secondChecks := 0
	f.handle("pane.process_info", func(params map[string]any) any {
		pid := a
		if str(params["pane_id"]) == "w1:p2" {
			pid = b
			secondChecks++
			if secondChecks > 1 {
				return herdrFail{"timeout", "foreground unavailable"}
			}
		}
		return map[string]any{"process_info": map[string]any{"foreground_processes": []any{map[string]any{"pid": pid}}}}
	})
	panes := []any{agentOf("traecli", "", proto.StatusWorking, "w1:p1", "w1:t1", "w1", "/work/app", "Inspect the export"), agentOf("traecli", "", proto.StatusWorking, "w1:p2", "w1:t2", "w1", "/work/app", "Inspect the export")}
	f.reply("session.snapshot", snapshot(nil, nil, panes, panes))
	if got := byID(listSessions(t, n))[threadMain].PaneID; got != "" {
		t.Fatalf("incomplete candidate verification selected %q", got)
	}
}
