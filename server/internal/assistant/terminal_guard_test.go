package assistant

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// An agent runs on top of a terminal, and Persona never closes an agent's terminal on its own (#4754,
// docs/integrations/engines.md "Agents run in a terminal"): its own passes stop an agent with session.stop, which keeps
// the pane. The node methods that close a terminal are asked only where the user asked for it, or for what decision 3
// of #4754 keeps closing. A new caller of one of them fails here: stop the agent instead, or add it below with the
// user action it serves.
var terminalClosers = map[string][]string{
	"session.close": {
		"forceArchiveConfirmation",   // the user's force archive of an Agent blocked on a confirmation
		"cleanAutoReview",            // a finished review's reviewer, an internal helper (#4754 decision 3)
		"stopContinuedAgent",         // explicit continuation of a non-terminal runtime session (SourceRuntime with !Terminal and Close, or no_terminal when non-terminal and not disallowed)
		"retireArchived",             // the user's "Archive and close" for a runtime whose declared close needs no terminal
		"logicalSweep",               // only recorded assistant segment identities, never ordinary coding Agents
		"advanceLogicalConversation", // retired assistant source, after the target accepted its first input
		"closeMain",                  // the user's Clear context (#5095): the main chat's own headless process, never a pane (session.stop refuses assistant conversations)
	},
	"session.retire": {
		"retireArchived",     // the user's "Archive and close" (close_terminal)
		"closeEndedTerminal", // the owner's opt-in close_ended_terminals setting, when an Agent's session ends (#10355)
	},
	"session.retire_guarded": {
		"retireArchived", // guarded idle archive-and-close with activity-version verification
	},
	"panes.close": {},
	"tabs.close":  {},
	"session.close_task_terminal": {
		"finishTaskTerminal", // the owner opted in at dispatch and task_review, or the owner's Pass, accepted durable completion (#10201, #16948)
		"tracksNodeWrite",    // classification only: compares method names and never calls a node
	},
}

func TestOnlyUserActionsCloseAgentTerminals(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	consts := map[string]string{
		"deviceSessionRetire":        "session.retire",
		"deviceSessionRetireGuarded": "session.retire_guarded",
		"deviceSessionStop":          "session.stop",
		"MethodCloseTaskTerminal":    "session.close_task_terminal",
	}
	fset := token.NewFileSet()
	found := map[string][]string{}
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
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				var method string
				switch v := n.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						method, _ = strconv.Unquote(v.Value)
					}
				case *ast.Ident:
					method = consts[v.Name]
				}
				if _, watched := terminalClosers[method]; watched && !slices.Contains(found[method], fn.Name.Name) {
					found[method] = append(found[method], fn.Name.Name)
				}
				return true
			})
		}
	}
	for method, allowed := range terminalClosers {
		for _, fn := range found[method] {
			if !slices.Contains(allowed, fn) {
				t.Errorf("%s asks a node for %s, which closes an agent's terminal: Persona only stops agents on its own "+
					"(session.stop); see docs/integrations/engines.md \"Agents run in a terminal\"", fn, method)
			}
		}
		for _, fn := range allowed {
			if !slices.Contains(found[method], fn) {
				t.Errorf("%s no longer asks for %s: drop it from terminalClosers", fn, method)
			}
		}
	}
}
