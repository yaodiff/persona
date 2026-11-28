package node

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Persona does not close an agent's terminal when it stops it (#4754, docs/integrations/engines.md "Agents run in a
// terminal"): session.stop and session.retire quit the agent and keep its pane. These are the only functions that close
// a pane or a tab (n.close, or a host's Close with a pane, workspace and tab), each for what the user asked, for a tab
// whose start failed before any agent ran in it, or for a finished Agent's terminal after its grace period. A new one fails here: stop the agent instead (quitAgent), or add it with
// the reason.
var paneClosers = map[string]string{
	"handleMethod":          "session.close: the user's Close, or a session with no terminal (hub supervise_leader.go)",
	"closePane":             "panes.close: the user closes a pane or tab",
	"retire":                "session.retire with close_terminal: the user's Archive and close, or the owner's close-on-end setting (#10355)",
	"close":                 "n.close itself",
	"closeWithCause":        "n.close with the stop cause it records: the user's, or close-on-end's automatic one (#10355)",
	"closeWithCauseChecked": "the same Close with the automatic caller's activity check before interrupting or closing",
	"closeExited":           "session.retire with after_exit: the owner's close-on-end setting, once the Agent quit (#10355)",
	"closeIdleTerminal":     "the owner's close-on-end setting after a guarded task stop, with a final activity check (#6819, #10355)",
	"closeExitedChecked":    "an already exited Agent's marked shell, with the automatic caller's final activity check",
	"closeAfterExit":        "session.close after its Agent exits: the user's Close of its marked shell (#9648)",
	"closeStoppedOrca":      "close of the original idle Orca shell after verification (#17041)",
	"closeOrcaTaskTerminal": "the owner's opted-in Orca task terminal after durable acceptance (#17041)",
	"closeTaskTerminal":     "session.close_task_terminal: the owner's opted-in task tab after durable acceptance (#10201)",
	"closeCodexStart":       "a Codex start abandoned before Codex ran",
	"closeNewTab":           "a start whose command never ran",
	"failedTerminal":        "a Herdr App terminal whose launch failed",
	"closeFinished":         "panes.close_finished: the user's Close finished terminals (stopped.go)",
	"closeFinishedPane":     "a finished Agent's terminal after its grace period, rechecked under its recovery lock (finished_close.go, #10111)",
	"closeStalledTab":       "the user's retry of a stalled Orca start: its kept tab, with no agent in it, whose one launch is used (#9534)",
	"closeWorktreeShells": "the empty first shell of an Agent's worktree, no Agent in it, once that Agent's own tab is open " +
		"there (personatab.go)",
}

func TestOnlyUserActionsClosePanes(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var found []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			nodeMethod, keptClaimMethod := false, false
			if fn.Recv != nil {
				if ptr, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
					typ, _ := ptr.X.(*ast.Ident)
					nodeMethod = typ != nil && typ.Name == "Node"
					keptClaimMethod = typ != nil && typ.Name == "keptClaim"
				}
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				c, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := c.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, _ := sel.X.(*ast.Ident)
				closes := (sel.Sel.Name == "close" || sel.Sel.Name == "closeAfterExit" || sel.Sel.Name == "closeWithCause" || sel.Sel.Name == "closeWithCauseChecked" ||
					sel.Sel.Name == "closeExited" || sel.Sel.Name == "closeIdleTerminal" ||
					sel.Sel.Name == "closeExitedChecked") && recv != nil && recv.Name == "n" ||
					(sel.Sel.Name == "PaneClose" || sel.Sel.Name == "PaneCloseIf") && nodeMethod ||
					sel.Sel.Name == "CloseVerified" ||
					sel.Sel.Name == "guardedClose" && (nodeMethod || keptClaimMethod) ||
					sel.Sel.Name == "Close" && len(c.Args) == 4 // host.Organizer.Close(ctx, pane, workspace, tab)
				if closes && !slices.Contains(found, fn.Name.Name) {
					found = append(found, fn.Name.Name)
				}
				return true
			})
		}
	}
	for _, fn := range found {
		if _, ok := paneClosers[fn]; !ok {
			t.Errorf("%s closes a pane or tab: Persona only stops agents on its own (quitAgent); see "+
				"docs/integrations/engines.md \"Agents run in a terminal\"", fn)
		}
	}
	for fn := range paneClosers {
		if !slices.Contains(found, fn) {
			t.Errorf("%s no longer closes a pane: drop it from paneClosers", fn)
		}
	}
}
