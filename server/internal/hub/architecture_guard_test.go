package hub

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
)

// The hub is the communication center only: clients <-> manager, hub <-> hub, connections, pairing / auth, admin,
// push delivery and relays. Business (the assistant, inbox, todos, usage, boards, extensions, devices' data) is the
// manager's (internal/assistant, internal/devices, internal/extmgr). These tests pin the hub's surface so new business
// lands in the manager instead (docs/server/architecture.md "The hub is only a communication center").

const guardDoc = "see docs/server/architecture.md「The hub is only a communication center」"

const modulePath = "github.com/nwverse/persona/server/"

// hubSources parses the hub's non-test files.
func hubSources(t *testing.T) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*ast.File{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = f
	}
	return out
}

// readList reads a testdata list: one entry per line, `#` starts a comment, blank lines skipped.
func readList(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// The routes the hub answers itself are exactly testdata/hub_routes_allowed.txt. A route counts when this package
// registers it with a string literal (or a concatenation of them), or by ranging a list of string literals. Ranging a
// wire table the hub only relays does not: those patterns are the manager's. Ranging any other name fails the guard,
// so a route cannot hide behind an identifier.
func TestHubAnswersOnlyItsOwnRoutes(t *testing.T) {
	relayed := relayedHubRoutes()
	own := map[string]string{} // pattern -> file
	for name, f := range hubSources(t) {
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
			case *ast.BasicLit, *ast.BinaryExpr: // the same patterns route_scopes_test.go reads
				if p, ok := patternOf(call.Args[0]); ok && !slices.Contains(relayed, p) {
					own[p] = name
				}
			}
			return true
		})
		if err := noteRangedRoutes(name, f, relayed, own); err != nil {
			t.Error(err)
		}
	}
	allowed := readList(t, "hub_routes_allowed.txt")
	for _, p := range slices.Sorted(func(yield func(string) bool) {
		for p := range own {
			if !yield(p) {
				return
			}
		}
	}) {
		if !slices.Contains(allowed, p) {
			t.Errorf("the hub answers %q itself (%s), which is not in testdata/hub_routes_allowed.txt. "+
				"Business routes belong to the manager: implement it in internal/assistant, internal/devices or internal/extmgr "+
				"and let the hub relay it by listing it in assistantwire.Routes / devicewire.Routes / extwire.Routes. "+
				"Only if it is truly the hub's own (connections, pairing / auth, admin, push delivery, relays) add the line to "+
				"the allowlist; %s", p, own[p], guardDoc)
		}
	}
	for _, p := range allowed {
		if _, ok := own[p]; !ok {
			t.Errorf("testdata/hub_routes_allowed.txt lists %q, which the hub no longer answers itself: delete the line", p)
		}
	}
}

// relayedHubRoutes are the manager's routes the hub only forwards. A loop over one of these tables is not the hub
// answering the route itself. extwire.KeptRoutes is not here: nothing ranges it to register a route.
func relayedHubRoutes() []string {
	return slices.Concat(
		assistantwire.Routes, assistantwire.InboxRoutes, assistantwire.SocketRoutes,
		devicewire.Routes, devicewire.ShareRoutes, devicewire.MetricsRoutes,
		devicewire.TransferRoutes, devicewire.UsageRoutes, devicewire.AdminUsageRoutes,
		extwire.Routes)
}

// noteRangedRoutes records routes registered by ranging a list of string literals (minus relayed). A loop that
// registers through any other name must name one of the relayed wire tables; mergedLists is the one in-package map,
// and only while every key is already a relayed route.
func noteRangedRoutes(file string, f *ast.File, relayed []string, own map[string]string) error {
	var errs []error
	ast.Inspect(f, func(n ast.Node) bool {
		rng, ok := n.(*ast.RangeStmt)
		if !ok || !bodyRegistersRangeIdent(rng.Body, rangeNames(rng)) {
			return true
		}
		switch x := rng.X.(type) {
		case *ast.CompositeLit:
			lits, ok := compositeStringLits(x)
			if !ok {
				errs = append(errs, fmt.Errorf("%s registers a route by ranging over a composite literal the guard cannot read as string patterns. Use a list of string literals, or range a relayed wire table; %s", file, guardDoc))
				return true
			}
			for _, p := range lits {
				if !slices.Contains(relayed, p) {
					own[p] = file
				}
			}
		case *ast.Ident:
			if x.Name == "mergedLists" {
				if err := mergedListsCovered(relayed); err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", file, err))
				}
				return true
			}
			errs = append(errs, errors.New(rangeHint(file, x.Name)))
		case *ast.SelectorExpr:
			if !relayedRangeSelector(x) {
				errs = append(errs, errors.New(rangeHint(file, selectorString(x))))
			}
		default:
			errs = append(errs, errors.New(rangeHint(file, fmt.Sprintf("%T", rng.X))))
		}
		return true
	})
	return errors.Join(errs...)
}

func rangeHint(file, what string) string {
	return fmt.Sprintf("%s registers a route by ranging over %s, which is not a relayed wire table "+
		"(assistantwire.Routes / InboxRoutes / SocketRoutes, devicewire.Routes / ShareRoutes / MetricsRoutes / "+
		"TransferRoutes / UsageRoutes / AdminUsageRoutes, extwire.Routes). Range one of those, or register the route "+
		"with a string literal; %s", file, what, guardDoc)
}

func rangeNames(rng *ast.RangeStmt) map[string]bool {
	names := map[string]bool{}
	for _, e := range []ast.Expr{rng.Key, rng.Value} {
		id, ok := e.(*ast.Ident)
		if ok && id.Name != "_" && id.Name != "" {
			names[id.Name] = true
		}
	}
	return names
}

func bodyRegistersRangeIdent(body ast.Stmt, names map[string]bool) bool {
	if body == nil || len(names) == 0 {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
			return true
		}
		id, ok := call.Args[0].(*ast.Ident)
		if ok && names[id.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

func compositeStringLits(lit *ast.CompositeLit) ([]string, bool) {
	if lit == nil || len(lit.Elts) == 0 {
		return nil, false
	}
	out := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		basic, ok := elt.(*ast.BasicLit)
		if !ok || basic.Kind != token.STRING {
			return nil, false
		}
		s, err := strconv.Unquote(basic.Value)
		if err != nil {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// relayedRangeSelector reports whether e is one of the wire tables a registration loop may range. KeptRoutes is
// absent on purpose: it is never ranged to register a route.
func relayedRangeSelector(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	switch pkg.Name + "." + sel.Sel.Name {
	case "assistantwire.Routes", "assistantwire.InboxRoutes", "assistantwire.SocketRoutes",
		"devicewire.Routes", "devicewire.ShareRoutes", "devicewire.MetricsRoutes",
		"devicewire.TransferRoutes", "devicewire.UsageRoutes", "devicewire.AdminUsageRoutes",
		"extwire.Routes":
		return true
	default:
		return false
	}
}

func selectorString(e *ast.SelectorExpr) string {
	if id, ok := e.X.(*ast.Ident); ok {
		return id.Name + "." + e.Sel.Name
	}
	return e.Sel.Name
}

// mergedListsCovered checks the loop `for pattern, key := range mergedLists`. The key is registered only when it is
// already a relayed route, either as written or with the method prefix removed (extwire.Routes has no method).
func mergedListsCovered(relayed []string) error {
	var missing []string
	for pattern := range mergedLists {
		if slices.Contains(relayed, pattern) {
			continue
		}
		if path, ok := cutMethod(pattern); ok && slices.Contains(relayed, path) {
			continue
		}
		missing = append(missing, pattern)
	}
	if len(missing) == 0 {
		return nil
	}
	slices.Sort(missing)
	return fmt.Errorf("mergedLists registers %s, which is not in a relayed wire table. Add it to assistantwire / devicewire / extwire before ranging it; %s", strings.Join(missing, ", "), guardDoc)
}

func cutMethod(pattern string) (string, bool) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || !strings.HasPrefix(path, "/") {
		return "", false
	}
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return path, true
	default:
		return "", false
	}
}

func parseHubSnippet(t *testing.T, src string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "snippet.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestRangeOverAnUnknownIdentifierFailsTheGuard(t *testing.T) {
	f := parseHubSnippet(t, `package hub
func register(mux interface{ HandleFunc(string, any) }) {
	for _, pattern := range notAWireTable {
		mux.HandleFunc(pattern, nil)
	}
}`)
	err := noteRangedRoutes("snippet.go", f, nil, map[string]string{})
	if err == nil || !strings.Contains(err.Error(), "notAWireTable") || !strings.Contains(err.Error(), "relayed wire table") {
		t.Fatalf("got %v, want a hint naming notAWireTable", err)
	}
}

func TestMergedListsRangeDoesNotTripTheGuard(t *testing.T) {
	relayed := relayedHubRoutes()
	if err := mergedListsCovered(relayed); err != nil {
		t.Fatal(err)
	}
	f := parseHubSnippet(t, `package hub
func register(mux interface{ HandleFunc(string, any) }) {
	for pattern, key := range mergedLists {
		mux.HandleFunc(pattern, key)
	}
}`)
	own := map[string]string{}
	if err := noteRangedRoutes("snippet.go", f, relayed, own); err != nil {
		t.Fatal(err)
	}
	if len(own) != 0 {
		t.Fatalf("mergedLists routes are relayed, got %#v", own)
	}
}

func TestLiteralListRangeCountsAsTheHubsOwnRoute(t *testing.T) {
	f := parseHubSnippet(t, `package hub
func register(mux interface{ HandleFunc(string, any) }) {
	for _, pattern := range []string{"/api/v1/admin/plugins", "/kept"} {
		mux.HandleFunc(pattern, nil)
	}
	for _, pattern := range devicewire.UsageRoutes {
		mux.HandleFunc(pattern, nil)
	}
}`)
	own := map[string]string{}
	if err := noteRangedRoutes("snippet.go", f, []string{"/kept"}, own); err != nil {
		t.Fatal(err)
	}
	if _, ok := own["/api/v1/admin/plugins"]; !ok || len(own) != 1 {
		t.Fatalf("want only the non-relayed literal, got %#v", own)
	}
}

// The manager's packages and the connectors' implementations never end up in the hub's binary through the hub.
func TestHubDoesNotImportTheManager(t *testing.T) {
	forbidden := []string{ // anywhere in the hub's dependencies
		"internal/agents", "internal/ai", "internal/assistant", "internal/assistantskills", "internal/connector",
		"internal/devices", "internal/extmgr", "internal/extension/host", "internal/feishubot", "internal/kb",
		"internal/lark", "internal/mail", "internal/mcpserver", "internal/node", "internal/office",
		"plugins",
		"internal/tunnel", // tailcat: the manager dials the computers, with its own key (files_guard_test.go)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	out, err := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		rel, ok := strings.CutPrefix(dep, modulePath)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rel == f || strings.HasPrefix(rel, f+"/") {
				t.Errorf("the hub depends on %s, which is the manager's: move the code that needs it into the manager "+
					"and reach it through the contract packages (assistantwire, devicewire, extwire; the types they carry "+
					"are in kbwire, connectorwire, aiwire); %s", dep, guardDoc)
			}
		}
	}
}

// The hub calls no model: it hands model settings on as aiwire.Config, imports nothing of internal/ai, and talks to
// no model API itself. Triage, titles, summaries and judging are the manager's.
func TestHubCallsNoModel(t *testing.T) {
	modelHosts := []string{"api.anthropic.com", "api.openai.com", "generativelanguage.googleapis.com", "/chat/completions"}
	for name, f := range hubSources(t) {
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == modulePath+"internal/ai" {
				t.Errorf("%s imports internal/ai: the hub calls no model, the manager does (internal/assistant); a model "+
					"setting the hub hands on is an aiwire.Config; %s", name, guardDoc)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if n, ok := n.(*ast.BasicLit); ok && n.Kind == token.STRING {
				for _, h := range modelHosts {
					if strings.Contains(n.Value, h) {
						t.Errorf("%s names the model API %q: the hub calls no model, the manager does; %s", name, h, guardDoc)
					}
				}
			}
			return true
		})
	}
}

// The hub's Store is exactly testdata/hub_store_methods.txt: new business tables are the manager's (its own store).
func TestHubStoreStaysSmall(t *testing.T) {
	typ := reflect.TypeFor[Store]()
	var have []string
	for i := range typ.NumMethod() {
		have = append(have, typ.Method(i).Name)
	}
	allowed := readList(t, "hub_store_methods.txt")
	for _, m := range have {
		if !slices.Contains(allowed, m) {
			t.Errorf("hub.Store gained %s, which is not in testdata/hub_store_methods.txt. Business data is the manager's: "+
				"put the table behind the manager's store (internal/assistant, internal/devices, internal/extmgr). Only state of the "+
				"hub's own (connections, devices' tokens, pairing, push delivery) goes in the list; %s", m, guardDoc)
		}
	}
	for _, m := range allowed {
		if !slices.Contains(have, m) {
			t.Errorf("testdata/hub_store_methods.txt lists %s, which hub.Store no longer has: delete the line", m)
		}
	}
}
