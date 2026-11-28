package node

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

type orcaCloseFixture struct {
	n     *Node
	h     *orcaHost
	o     *orcarttest.Fake
	procs *fakeProcs
	cwd   string
	owner orcaTerminalOwner
}

func newOrcaCloseFixture(t *testing.T) orcaCloseFixture {
	t.Helper()
	n, _, o, procs, home := orcaNode(t)
	h := n.hosts.Get(host.Orca).(*orcaHost)
	cwd := mkdir(t, home+"/project")
	o.SetWorktrees(orcarttest.Worktree("wt", cwd, "refs/heads/main"))
	o.SetTerminals(orcarttest.Terminal("term_user", "tab_user", "leaf_user", "wt", cwd, "", "Shell", ""))
	mark := &taskTerminalStart{token: taskTerminalTestToken}
	ctx := context.WithValue(t.Context(), taskTerminalStartKey{}, mark)
	created, err := n.newTab(ctx, h, cwd, "", "Agent", sidA)
	if err != nil {
		t.Fatal(err)
	}
	pane := created.Pane.String()
	n.putAgentState(ctx, h, sidA, sessionState{Pane: pane, Cwd: cwd, Launcher: "claude"})
	n.bindPane(pane, sidA)
	st, _ := n.state.get(sidA)
	if st.OrcaTerminal == nil || mark.pane != pane {
		t.Fatalf("new Orca tab has no ownership record: %+v", st.OrcaTerminal)
	}
	owner := *st.OrcaTerminal
	if owner.RootPID <= 0 || owner.RootStart == "" || owner.TaskToken != mark.token {
		t.Fatal("record does not match the created shell or the task start")
	}
	o.SetScreen(owner.Handle, "$ ")
	o.SetAgentStatus(owner.Handle, false, "")
	o.SetProcess(owner.Handle, orcarttest.ShellAtRest(42))
	f := orcaCloseFixture{n: n, h: h, o: o, procs: procs, cwd: cwd, owner: owner}
	f.idleShell()
	return f
}

func (f orcaCloseFixture) idleShell() {
	f.h.inspectCloseShell = func(_ context.Context, owner orcaTerminalOwner, _ int) (orcaShell, bool) {
		return orcaShell{pid: 42, start: "original-shell", cwd: f.cwd},
			owner == f.owner
	}
}

func (f orcaCloseFixture) closeCalls(t *testing.T, want int) {
	t.Helper()
	calls := f.o.Params("terminal.close")
	if len(calls) != want {
		t.Fatalf("terminal.close calls = %v, want %d", calls, want)
	}
	for _, call := range calls {
		if call["terminal"] != f.owner.Handle || call["expectedIncarnationId"] != f.owner.Incarnation {
			t.Fatalf("close was sent to an unexpected terminal: %+v", call)
		}
	}
}

func TestOrcaCloseAfterExitVerifiesIdleOriginal(t *testing.T) {
	for _, scenario := range []string{"closed", "user terminal", "old record", "missing shell mark", "old runtime", "unverifiable", "shell with jobs", "missing shell identity", "different shell", "reincarnated", "moved", "rebound during check", "process changed", "directory changed", "inventory failed", "truncated", "unknown completeness", "already gone", "close failed", "no close receipt"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOrcaCloseFixture(t)
			st, _ := f.n.state.get(sidA)
			var before func() error
			switch scenario {
			case "user terminal":
				st.Pane, st.OrcaTerminal = "orca:tab_user:leaf_user", nil
				f.n.state.put(sidA, st)
			case "old record":
				f.n.state.update(sidA, func(s *sessionState) bool { s.OrcaTerminal = nil; return true })
				st.OrcaTerminal = nil
			case "missing shell mark":
				f.h.inspectCloseShell = func(context.Context, orcaTerminalOwner, int) (orcaShell, bool) { return orcaShell{}, false }
			case "old runtime":
				f.o.Handle("terminal.inspectProcess", func(map[string]any) any { return orcarttest.Fail{Code: "method_not_found"} })
			case "unverifiable":
				f.o.SetProcess(f.owner.Handle, orcarttest.Unverifiable("process_table_unreadable"))
			case "shell with jobs":
				f.o.SetProcess(f.owner.Handle, orcarttest.ShellWithJobs(42))
			case "missing shell identity":
				f.o.SetProcess(f.owner.Handle, orcarttest.ShellAtRest(0))
			case "different shell":
				f.o.SetProcess(f.owner.Handle, orcarttest.ShellAtRest(43))
			case "reincarnated":
				f.o.Update(f.owner.Handle, map[string]any{"incarnationId": "other"})
			case "moved":
				f.o.Move(f.owner.Handle, "tab_elsewhere", "leaf_elsewhere")
			case "rebound during check":
				before = func() error { f.n.bindPane(f.owner.Pane, sidB); return nil }
			case "process changed":
				before = func() error {
					f.h.inspectCloseShell = func(context.Context, orcaTerminalOwner, int) (orcaShell, bool) {
						return orcaShell{pid: 42, start: "reused-pid", cwd: f.cwd}, true
					}
					return nil
				}
			case "directory changed":
				f.h.inspectCloseShell = func(context.Context, orcaTerminalOwner, int) (orcaShell, bool) {
					return orcaShell{pid: 42, start: "original-shell", cwd: f.cwd + "/elsewhere"}, true
				}
			case "inventory failed":
				f.o.Handle("terminal.list", func(map[string]any) any { return orcarttest.Fail{Code: "unavailable"} })
			case "truncated":
				f.o.Handle("terminal.list", func(map[string]any) any { return map[string]any{"terminals": []any{}, "truncated": true} })
			case "unknown completeness":
				f.o.Handle("terminal.list", func(map[string]any) any { return map[string]any{"terminals": []any{}} })
			case "already gone":
				f.o.SetTerminals()
			case "close failed":
				f.o.Handle("terminal.close", func(map[string]any) any { return orcarttest.Fail{Code: "terminal_handle_stale"} })
			case "no close receipt":
				f.o.Handle("terminal.close", func(map[string]any) any {
					return map[string]any{"close": map[string]any{"handle": f.owner.Handle, "ptyKilled": false}}
				})
			}
			f.o.Reset()
			err := f.n.closeAfterExit(t.Context(), sidA, st, before)
			wantSuccess := scenario == "closed" || scenario == "already gone"
			if (err == nil) != wantSuccess {
				t.Fatalf("close error = %v, want success %v", err, wantSuccess)
			}
			if scenario == "closed" || scenario == "close failed" || scenario == "no close receipt" {
				f.closeCalls(t, 1)
			} else {
				f.closeCalls(t, 0)
			}
		})
	}
}

func TestOrcaRekeyedFinishedTerminalKeepsItsOwner(t *testing.T) {
	for _, mode := range []string{"manual", "grace", "after exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newOrcaCloseFixture(t)
			if key := f.o.Orphan(f.owner.Handle); key == "" || key == host.ParsePane(f.owner.Pane).ID {
				t.Fatal("fixture did not re-key its terminal")
			}
			listed, err := f.n.listPanes(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var marked int
			for _, pane := range listed.Hosts[0].Panes {
				if pane.Pane == f.owner.Pane && pane.AgentSession == sidA {
					marked++
				} else if pane.AgentSession != "" {
					t.Fatalf("unexpectedly labelled terminal: %+v", pane)
				}
			}
			if marked != 1 {
				t.Fatalf("expected one re-keyed terminal at the original pane: %+v", listed.Hosts[0].Panes)
			}
			switch mode {
			case "manual":
				preview, err := f.n.closeFinished(t.Context(), f.owner.Pane, true)
				if err != nil || len(preview["closed"].([]finishedTerminal)) != 1 {
					t.Fatalf("close preview = %v, %v", preview, err)
				}
				f.closeCalls(t, 0)
				closed, err := f.n.closeFinished(t.Context(), f.owner.Pane, false)
				if err != nil || len(closed["closed"].([]finishedTerminal)) != 1 {
					t.Fatalf("manual close = %v, %v", closed, err)
				}
			case "grace":
				got := rounds(t, f.n, time.Now(), 0, testGrace)
				if len(got[0]) != 0 || !slices.Equal(got[1], []string{f.owner.Pane}) {
					t.Fatalf("grace close = %v", got)
				}
			case "after exit":
				closed, err := f.n.closeExited(t.Context(), sidA)
				if !closed || err != nil {
					t.Fatalf("close after exit = %v, %v", closed, err)
				}
			}
			f.closeCalls(t, 1)
		})
	}
}

func TestOrcaRekeyedTerminalNeedsLiveProof(t *testing.T) {
	for _, scenario := range []string{"no creation record", "busy shell", "changed incarnation", "changed worktree", "old key reused"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOrcaCloseFixture(t)
			f.o.Orphan(f.owner.Handle)
			key := host.ParsePane(f.owner.Pane).ID
			switch scenario {
			case "no creation record":
				f.h.mu.Lock()
				delete(f.h.made, key)
				f.h.mu.Unlock()
			case "busy shell":
				f.o.SetProcess(f.owner.Handle, orcarttest.ShellWithJobs(42))
			case "changed incarnation":
				f.o.Update(f.owner.Handle, map[string]any{"incarnationId": "replacement"})
			case "changed worktree":
				f.o.Update(f.owner.Handle, map[string]any{"worktreeId": "other"})
			case "old key reused":
				tab, leaf, _ := strings.Cut(key, ":")
				f.o.Move("term_user", tab, leaf)
			}
			st, _ := f.n.state.get(sidA)
			if err := f.n.closeAfterExit(t.Context(), sidA, st, nil); err == nil {
				t.Fatal("an unverifiable re-key was not refused")
			}
			f.closeCalls(t, 0)
		})
	}
}

func TestOrcaCloseDoesNotUseCachedLiveAgentAfterExit(t *testing.T) {
	f := newOrcaCloseFixture(t)
	f.n.mu.Lock()
	f.n.agents[sidA] = host.Agent{PaneID: f.owner.Pane, Agent: "claude", AgentStatus: proto.StatusIdle}
	f.n.mu.Unlock()
	if _, err := request(t, f.n, "session.close", map[string]any{"session_id": sidA}); err != nil {
		t.Fatal(err)
	}
	f.closeCalls(t, 1)
	if got := f.o.Trace("terminal.send"); len(got) > 0 {
		t.Fatalf("Close interrupted the stopped shell: %v", got)
	}
}

func TestOrcaCloseWhenAgentEnds(t *testing.T) {
	f := newOrcaCloseFixture(t)
	refused := errors.New("fixture input arrived")
	checks := 0
	closed, err := f.n.closeExitedChecked(t.Context(), sidA, func() error {
		checks++
		if checks > 1 {
			return refused
		}
		return nil
	})
	if closed || !errors.Is(err, refused) {
		t.Fatalf("automatic close after input: closed=%v, error=%v", closed, err)
	}
	f.closeCalls(t, 0)
	closed, err = f.n.closeExited(t.Context(), sidA)
	if !closed || err != nil {
		t.Fatalf("automatic close: closed=%v, error=%v", closed, err)
	}
	f.closeCalls(t, 1)
}

func TestOrcaFinishedTerminalSurvivesNodeRestart(t *testing.T) {
	f := newOrcaCloseFixture(t)
	st, _ := f.n.state.get(sidA)
	// Exercise both ends of the state.put/state.load JSON contract; the new
	// node and Orca host have no memory of having created the terminal.
	var manager db.NodeSession
	if err := json.Unmarshal(mustJSON(st), &manager); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(mustJSON(manager), &st); err != nil {
		t.Fatal(err)
	}
	if st.OrcaTerminal == nil || *st.OrcaTerminal != f.owner {
		t.Fatalf("ownership did not survive state round trip: %+v", st.OrcaTerminal)
	}
	n, _ := testNode(t)
	f.n, f.h = n, newOrcaHost(&orcart.Client{MetaPath: f.o.MetaPath})
	f.h.scan = f.procs.scan
	f.idleShell()
	n.hosts = host.NewRegistry(f.h)
	n.state.merge(map[string]sessionState{sidA: st})
	n.panes = n.state.panes()
	listed, err := n.listPanes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	panes := listed.Hosts[0].Panes
	if len(panes) != 2 || panes[0].AgentSession != "" || panes[1].AgentSession != sidA {
		t.Fatalf("finished terminal labels = %+v", panes)
	}
	t0 := time.Now()
	f.o.SetProcess(f.owner.Handle, orcarttest.ShellWithJobs(42))
	if got := rounds(t, n, t0, -2*testGrace, -testGrace); len(got[0])+len(got[1]) != 0 {
		t.Fatalf("restored ownership bypassed the runtime's busy-shell guard: %v", got)
	}
	f.o.SetProcess(f.owner.Handle, orcarttest.ShellAtRest(42))
	first := rounds(t, n, t0, 0, testGrace/2)
	f.o.SetScreen(f.owner.Handle, "changed\n$ ")
	later := rounds(t, n, t0, testGrace, 2*testGrace)
	if len(first[0])+len(first[1])+len(later[0]) != 0 || !slices.Equal(later[1], []string{f.owner.Pane}) {
		t.Fatalf("grace close attempts = %v %v", first, later)
	}
	f.closeCalls(t, 1)
}

func (f orcaCloseFixture) task(t *testing.T, pid int) devicewire.TaskTerminal {
	t.Helper()
	l := host.ParsePane(f.owner.Pane).ID
	term, err := f.h.terminal(t.Context(), l, true)
	if err != nil {
		t.Fatal(err)
	}
	user := orcarttest.Terminal("term_user", "tab_user", "leaf_user", "wt", f.cwd, "", "Shell", "")
	agent := orcarttest.Terminal(f.owner.Handle, term.TabID, term.LeafID, "wt", f.cwd, "", "Agent", "claude")
	agent["incarnationId"] = term.IncarnationID
	f.o.SetTerminals(user, agent)
	f.o.SetWorktrees(orcarttest.Worktree("wt", f.cwd, "", orcarttest.Agent(l, "claude", "idle")))
	f.o.SetAgentStatus(term.Handle, true, "idle")
	f.o.SetProcess(term.Handle, orcarttest.Running("claude", 42))
	f.o.SetScreen(term.Handle, "Fixture finished.\n\n❯ ")
	f.procs.set(l, orcaProc{pid: pid, agent: "claude", cwd: f.cwd})
	writeClaudePID(t, f.n.home, pid, sidA)
	writeTranscript(t, f.n.home, f.cwd, sidA, time.Now().Add(-time.Hour),
		`{"type":"user","message":{"role":"user","content":"Finish fixture"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Fixture finished."}]},"uuid":"a1"}`,
		`{"type":"system","subtype":"turn_duration","durationMs":1000}`)
	if err := f.n.refreshHosts(t.Context()); err != nil {
		t.Fatal(err)
	}
	mark := taskTerminalStart{token: f.owner.TaskToken, pane: f.owner.Pane}
	target := f.n.taskTerminalReceipt(t.Context(), &proto.Session{SessionID: sidA, PaneID: f.owner.Pane,
		TabID: term.TabID, WorkspaceID: term.WorktreeID, Agent: "claude"}, mark)
	if target == nil || target.PID != pid {
		t.Fatalf("start receipt = %+v, want process %d", target, pid)
	}
	return *target
}

func TestOrcaTaskTerminalCloseAfterAcceptance(t *testing.T) {
	for _, scenario := range []string{"closed", "no authorization", "orphaned during check", "focus changes", "split", "last tab", "process replaced", "live work", "inspection changes", "draft", "viewer", "wrong session", "write fails"} {
		t.Run(scenario, func(t *testing.T) {
			f := newOrcaCloseFixture(t)
			target := f.task(t, idleTaskTerminalPID(t))
			allow := func() bool { return true }
			want := devicewire.TaskTerminalSkipped
			switch scenario {
			case "closed":
				want = devicewire.TaskTerminalClosed
			case "no authorization":
				allow = func() bool { return false }
			case "orphaned during check":
				allow = func() bool {
					f.o.Orphan(f.owner.Handle)
					return true
				}
			case "focus changes", "split", "last tab":
				allow = func() bool {
					term := orcarttest.Terminal(f.owner.Handle, target.TabID, "leaf_2", "wt", f.cwd, "", "Agent", "claude")
					term["incarnationId"] = f.owner.Incarnation
					sibling := orcarttest.Terminal("term_user", "tab_user", "leaf_user", "wt", f.cwd, "", "Shell", "")
					switch scenario {
					case "focus changes":
						f.o.SetTerminals(term, sibling)
					case "last tab":
						f.o.SetTerminals(term)
					case "split":
						sibling["tabId"] = target.TabID
						other := orcarttest.Terminal("term_other", "tab_other", "leaf_other", "wt", f.cwd, "", "Shell", "")
						f.o.SetTerminals(other, term, sibling)
					}
					return true
				}
			case "process replaced":
				target.Process = "other process"
			case "live work":
				f.h.inspectCloseShell = func(context.Context, orcaTerminalOwner, int) (orcaShell, bool) { return orcaShell{}, false }
			case "inspection changes":
				allow = func() bool {
					f.o.SetProcess(f.owner.Handle, orcarttest.Running("vi", 42))
					return true
				}
			case "draft":
				f.o.SetBoxScreen(f.owner.Handle, "❯", "unfinished draft")
			case "viewer":
				f.n.mu.Lock()
				f.n.terms = map[string]*termStream{panePrefix + target.PaneID: {}}
				f.n.mu.Unlock()
			case "wrong session":
				writeClaudePID(t, f.n.home, target.PID, sidB)
			case "write fails":
				f.o.Handle("terminal.close", func(map[string]any) any { return orcarttest.Fail{Code: "terminal_handle_stale"} })
			}
			if got := f.n.closeTaskTerminal(t.Context(), target, allow); got != want {
				t.Fatalf("close-on-accept = %s, want %s; calls %v", got, want, f.o.Trace())
			}
			if scenario == "closed" || scenario == "write fails" {
				f.closeCalls(t, 1)
			} else {
				f.closeCalls(t, 0)
			}
		})
	}
}

func TestOrcaCloseProcessTreeRefusesOtherWork(t *testing.T) {
	procs := map[int]proc{1: {tty: "pts1", cmd: "zsh"}, 2: {ppid: 1, tty: "pts1", cmd: "claude"}}
	if !orcaCloseProcessTree(procs, 1, 2) || orcaCloseProcessTree(procs, 1, 0) {
		t.Fatal("only the original shell and foreground task process may remain for task acceptance")
	}
	procs[3] = proc{ppid: 2, cmd: "background worker"}
	if orcaCloseProcessTree(procs, 1, 2) {
		t.Fatal("accepted a background child with no controlling terminal")
	}
	delete(procs, 2)
	delete(procs, 3)
	procs[4] = proc{tty: "pts1", cmd: "background job"}
	if orcaCloseProcessTree(procs, 1, 0) {
		t.Fatal("accepted an unrelated process sharing the terminal")
	}
}
