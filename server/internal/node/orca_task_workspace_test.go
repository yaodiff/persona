package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

func TestOrcaTaskWorkspaceGroupsThreeAgentsAndKeepsUserTerminal(t *testing.T) {
	w := orcaWorktreeNode(t)
	h := w.n.hosts.Get(host.Orca).(*orcaHost)
	user := orcarttest.Terminal("term_user", "tab-user", "leaf-user", w.src, w.repo, "dev", "Shell", "")
	w.o.SetTerminals(user)
	dest := filepath.Join(w.dir, "worker")
	res, err := request(t, w.n, "worktree.create", map[string]any{
		"cwd": w.repo, "branch": "task/worker", "path": dest, "for_agent": true, "open_workspace": false,
	})
	if err != nil || res["path"] != dest {
		t.Fatalf("worker checkout: %v, %v", res, err)
	}

	procs := &fakeProcs{}
	h.scan = procs.scan
	ready := screenFixture(t, "claude-2.1.312-empty-162.txt")
	w.o.Handle("terminal.send", func(p map[string]any) any {
		handle, line := str(p["terminal"]), str(p["text"])
		procs.set(orcaFakePane(handle), orcaProc{pid: 4242, agent: "claude"})
		w.o.SetScreen(handle, ready)
		return map[string]any{"send": map[string]any{"handle": handle, "accepted": true, "bytesWritten": len(line)}}
	})
	files := map[string]string{}
	dirs := map[string]string{}
	receipts := map[string]string{}
	const task = "todo-group"
	const transcriptLine = `{"type":"custom-title","customTitle":"Task result"}`
	for i, member := range []struct{ id, name, cwd string }{
		{sidA, "Leader", w.repo}, {sidB, "Writable worker", dest}, {sidC, "Read-only worker", w.repo},
	} {
		params := map[string]any{"task_id": task, "session_id": member.id, "tab_name": member.name,
			"cwd": member.cwd, "workspace_label": "Group"}
		if i > 0 {
			params["workspace_id"] = w.src
		}
		result, err := request(t, w.n, "session.start", params)
		if err != nil {
			t.Fatalf("start %s: %v", member.name, err)
		}
		placement, _ := result["task_workspace"].(map[string]any)
		st, _ := w.n.state.get(member.id)
		if placement["workspace_id"] != w.src || placement["task_id"] != task || st.OrcaTerminal == nil || st.OrcaTerminal.TaskID != task {
			t.Fatalf("%s placement = %v, ownership = %+v", member.name, placement, st.OrcaTerminal)
		}
		calls := w.o.Params("terminal.send")
		if !strings.HasPrefix(str(calls[len(calls)-1]["text"]), "cd -- "+shellQuote(member.cwd)+" && ") {
			t.Fatalf("%s did not start in %s", member.name, member.cwd)
		}
		files[member.id] = writeTranscript(t, w.home, member.cwd, member.id, time.Now(), transcriptLine)
		dirs[st.OrcaTerminal.Handle] = member.cwd
		receipts[st.OrcaTerminal.Handle] = st.OrcaTerminal.Incarnation
		procs.set(host.ParsePane(st.Pane).ID)
		w.o.SetProcess(st.OrcaTerminal.Handle, orcarttest.ShellAtRest(42))
	}
	// Publish a sidebar layout from the fake runtime's actual terminal.create results.
	var inventory struct {
		Terminals []map[string]any `json:"terminals"`
	}
	if err := h.c.Call(t.Context(), orcart.ListTimeout, "terminal.list", map[string]any{"includeVisualLayouts": false}, &inventory); err != nil {
		t.Fatal(err)
	}
	w.o.SetTerminals(inventory.Terminals...)
	w.o.SetRevision(w.src, 1)
	layout, err := h.Snapshot(t.Context())
	if err != nil || len(layout.Spaces) != 1 || len(layout.Slots) != 4 {
		t.Fatalf("Orca sidebar before acceptance: %+v, %v", layout, err)
	}
	for _, slot := range layout.Slots {
		if slot.SpaceID != w.src {
			t.Fatalf("tab outside the leader's workspace: %+v", slot)
		}
	}
	if err := w.n.refreshHosts(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	h.inspectCloseShell = func(_ context.Context, o orcaTerminalOwner, _ int) (orcaShell, bool) {
		cwd, ok := dirs[o.Handle]
		return orcaShell{pid: 42, start: "shell", cwd: cwd}, ok
	}
	if _, err := request(t, w.n, "workspace.close", map[string]any{"host": "orca", "task_id": "other", "workspace_id": w.src}); err == nil {
		t.Fatal("foreign task accepted")
	}
	closeParams := map[string]any{"host": "orca", "task_id": task, "workspace_id": w.src}
	if result, err := request(t, w.n, "workspace.close", closeParams); err != nil || result["closed"] != true {
		t.Fatalf("close: %v, %v", result, err)
	}
	for _, call := range w.o.Params("terminal.close") {
		handle := str(call["terminal"])
		inc, ok := receipts[handle]
		if !ok || inc != call["expectedIncarnationId"] {
			t.Fatalf("closed a foreign or replaced terminal: %v", call)
		}
		delete(receipts, handle)
	}
	if len(receipts) != 0 {
		t.Fatalf("task terminals left open: %v", receipts)
	}
	// The socket fake records close requests. Publish the resulting inventory for the next pass.
	w.o.SetTerminals(user)
	if result, err := request(t, w.n, "workspace.close", closeParams); err != nil || result["closed"] != false || w.o.Count("terminal.close") != 3 {
		t.Fatalf("repeat close: %v, %v", result, err)
	}
	for id, path := range files {
		if data, err := os.ReadFile(path); err != nil || string(data) != transcriptLine+"\n" {
			t.Errorf("transcript for %s changed: %q, %v", id, data, err)
		}
	}
	if branchOf(t, dest) != "task/worker" || branchOf(t, w.repo) != "dev" || len(w.o.Invalid()) != 0 {
		t.Fatalf("task checkouts or Orca request schema: %v", w.o.Invalid())
	}
}
