package hub

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

// routePatterns are every route the apps reach through the hub: the hub's own as registered, the manager's it relays
// as they are (assistantwire, devicewire, extwire), and the routes the manager's extensions code registers behind
// extwire.Routes (internal/extmgr: an extension's token is judged by their patterns there, extensions.authorize).
func routePatterns(t *testing.T) []string {
	seen := map[string]bool{}
	// larksync's routes are mounted on extmgr's API mux (extmgr/bots.go larkSyncRoutes) since they moved to the module (#16357).
	for _, dir := range []string{".", filepath.Join("..", "extmgr"), filepath.Join("..", "modules", "lark", "larksync")} {
		fset := token.NewFileSet()
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(fset, name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) != 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
					return true
				}
				switch call.Args[0].(type) {
				case *ast.BasicLit, *ast.BinaryExpr: // a variable is not a route's (the compatibility paths, a loop's)
					if p, ok := patternOf(call.Args[0]); ok {
						seen[p] = true
					}
				}
				return true
			})
		}
	}
	for _, p := range slices.Concat(assistantwire.Routes, assistantwire.InboxRoutes, assistantwire.SocketRoutes, devicewire.Routes, devicewire.ShareRoutes, devicewire.MetricsRoutes, devicewire.UsageRoutes, devicewire.AdminUsageRoutes, extwire.Routes) {
		seen[p] = true // the manager's, relayed
	}
	return slices.Sorted(func(yield func(string) bool) {
		for p := range seen {
			if !yield(p) {
				return
			}
		}
	})
}

func patternOf(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil && (strings.HasPrefix(s, "/") || strings.Contains(s, " /") || strings.HasSuffix(s, " "))
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		a, ok1 := patternOf(e.X)
		b, ok2 := patternOf(e.Y)
		return a + b, ok1 && ok2
	case *ast.Ident:
		return "{" + e.Name + "}", true
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return "{" + x.Name + "." + e.Sel.Name + "}", true
		}
	}
	return "", false
}

// Every route and the scope an extension's token needs for it (extwire.RouteScopes, the manager's to check).
func TestRouteScopes(t *testing.T) {
	patterns := routePatterns(t)
	t.Run("input_resolution_requires_full_credentials", func(t *testing.T) {
		const pattern = "POST /api/v1/assistant/conversations/{id}/inputs/resolve"
		if !slices.Contains(patterns, pattern) {
			t.Fatalf("missing input resolution route: %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Fatalf("an extension token must not authorize input settlement or replay: %s has scope %s", pattern, scope)
		}
	})
	for p := range extwire.RouteScopes {
		if !slices.Contains(patterns, p) {
			t.Errorf("RouteScopes names %q, which neither the hub nor the manager registers", p)
		}
	}
	for p, s := range extwire.RouteScopes {
		if s == extension.ScopeCallPrefix {
			continue
		}
		if _, ok := extension.ScopeTitle(s); !ok {
			t.Errorf("RouteScopes: %q needs %q, which is not a scope", p, s)
		}
	}
	var b strings.Builder
	b.WriteString("# Every route of the hub (as registered) and the scope an extension's token needs for it; - is full credentials only.\n")
	b.WriteString("# Kept by TestRouteScopes (hub/route_scopes_test.go); PERSONA_UPDATE_ROUTE_SCOPES=1 rewrites it.\n")
	for _, p := range patterns {
		s := extwire.RouteScopes[p]
		if s == "" {
			s = "-"
		} else if s == extension.ScopeCallPrefix {
			s = extension.ScopeCallPrefix + "{id}"
		}
		fmt.Fprintf(&b, "%-20s %s\n", s, p)
	}
	file := filepath.Join("testdata", "route_scopes.txt")
	if os.Getenv("PERSONA_UPDATE_ROUTE_SCOPES") == "1" {
		_ = os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(file, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != b.String() {
		t.Fatalf("the hub's routes changed: decide whether an extension's token opens the new ones (extwire.RouteScopes), " +
			"then PERSONA_UPDATE_ROUTE_SCOPES=1 go test ./internal/hub -run TestRouteScopes and check the diff of testdata/route_scopes.txt")
	}
}

func TestCodexTakeoverRequiresFullCredentials(t *testing.T) {
	patterns := routePatterns(t)
	for _, method := range []string{"GET", "POST"} {
		pattern := method + " /api/v1/machines/{mid}/codex/{sid}/takeover"
		if !slices.Contains(patterns, pattern) {
			t.Fatalf("missing takeover route: %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Fatalf("an extension token must not authorize takeover: %s has scope %s", pattern, scope)
		}
	}
}

func TestBackendWorkspacesRequireFullCredentials(t *testing.T) {
	patterns := routePatterns(t)
	for _, pattern := range []string{
		"GET /api/v1/todos/{id}/backend-workspace",
		"GET /api/v1/todos/{id}/backend-workspace/operations/{operation_id}",
		"POST /api/v1/todos/{id}/backend-workspace/prepare",
		"POST /api/v1/todos/{id}/backend-workspace/cancel",
		"POST /api/v1/todos/{id}/backend-workspace/cleanup",
	} {
		if !slices.Contains(patterns, pattern) {
			t.Errorf("missing backend workspace route %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Errorf("backend workspace route %s grants extension scope %s", pattern, scope)
		}
	}
}

func TestRepoMirrorsRequireFullCredentials(t *testing.T) {
	patterns := routePatterns(t)
	for _, pattern := range []string{
		"GET /api/v1/assistant/repositories/mirrors",
		"GET /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
		"PUT /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
		"POST /api/v1/assistant/repositories/{id}/mirrors/{machine_id}/sync",
		"DELETE /api/v1/assistant/repositories/{id}/mirrors/{machine_id}",
	} {
		if !slices.Contains(patterns, pattern) {
			t.Errorf("missing repo mirror route %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Errorf("repo mirror route %s grants extension scope %s", pattern, scope)
		}
	}
}

func TestTodoHandoffRequiresFullCredentials(t *testing.T) {
	patterns := routePatterns(t)
	for _, pattern := range []string{
		"GET /api/v1/todos/{id}/handoffs",
		"POST /api/v1/todos/{id}/handoff",
	} {
		if !slices.Contains(patterns, pattern) {
			t.Errorf("missing todo handoff route %s", pattern)
		}
		if scope := extwire.RouteScopes[pattern]; scope != "" {
			t.Errorf("todo handoff route %s grants extension scope %s", pattern, scope)
		}
	}
}
