package archguard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/archguard/domainrules"
)

// The type-aware domain rules (docs/server/architecture.md "Domain model", "The rules and their checks"): what the
// syntax-only rules of domainrules cannot see, because it takes a value's type to see it. TestDomainTyped runs them
// over the cores that predate no rule (every core but the legacy ones the baseline holds) and every package that
// reaches one through its production imports, directly or through others (an adapter hands a core's value to code
// that never imports the core), type-checked from source against the gc export data of what they import
// (go list -export -deps; go/types and go/importer, the standard library only).

// typedRule is what a typed finding says besides what was found, as domainrules' rules do.
type typedRule struct{ why, fix, good string }

var typedRules = map[string]typedRule{
	"typed-visitor-impl": {
		"a visitor is how a core's data leaves it, and only an adapter turns that data into anything else",
		"move the type into the core's adapter and call behaviour from the application",
		"server/internal/assistant/fleetcount/fleetcountadapt/render.go",
	},
	"typed-discard": {
		"an outcome or a failure dropped is a failure nobody handles",
		"unpack it (outcome.Unpack) and act on the failure, or log it through the adapter's Log",
		"server/internal/assistant/fleet_live.go (fill)",
	},
	"typed-codec": {
		"fmt, log, slog, json, templates, and custom loggers or any-typed sinks read a value's unexported fields through reflection, a getter by another name",
		"hand the value to its adapter, which writes its wire form or its Log line",
		"server/internal/agentref/agentrefadapt/agentrefadapt.go (Log)",
	},
	"typed-map-range": {
		"a map's order changes from run to run, so a core that ranges over one is not a function of its arguments",
		"range over slices.Sorted(maps.Keys(m)), or keep the entries in a slice",
		"server/internal/assistant/fleetcount/tally.go",
	},
	"typed-fail-compare": {
		"a failure's code cannot be compared, so == between Fail values panics at run time",
		"visit the failure (fail.FailVisitor), or compare it with nil only",
		"server/internal/domainkit/domaintest/domaintest.go (Describe)",
	},
}

func typedMessage(id, what string) string {
	r := typedRules[id]
	return "RULE " + id + " " + what + ". WHY " + r.why + ". FIX " + r.fix + ". GOOD " + r.good +
		`. see docs/server/architecture.md "Domain model"`
}

const domainKit = serverModule + "/internal/domainkit"

// typedUnit is one package to check: its import path, layer and files (the ones the build takes).
type typedUnit struct {
	path, layer string
	files       []string
}

// typedFinding is one breach: its rule and position (for the planted cases) and its message.
type typedFinding struct {
	rule string
	pos  token.Position
	msg  string
}

func TestDomainTyped(t *testing.T) {
	start := time.Now()
	pkgs, err := domainrules.Load(serverDir, serverModule)
	if err != nil {
		t.Fatal(err)
	}
	strict := map[string]bool{} // import paths of the cores the rules hold in full
	for _, p := range pkgs {
		if (p.Layer == "core" || p.Layer == "core shared") && !domainrules.LegacyCore(p.Rel) {
			strict[p.Path] = true
		}
	}
	layers := map[string]string{}
	byPath := map[string]*domainrules.Pkg{}
	for _, p := range pkgs {
		layers[p.Path] = p.Layer
		byPath[p.Path] = p
	}
	reach := reachesStrict(byPath, strict)
	var targets []string
	for _, p := range pkgs {
		if (strict[p.Path] || reach(p.Path)) && p.Name != "" {
			targets = append(targets, p.Path)
		}
	}
	listed, err := goListExport(serverDir, targets)
	if err != nil {
		t.Fatal(err)
	}
	var units []typedUnit
	for _, path := range targets {
		l := listed[path]
		var files []string
		for _, f := range l.GoFiles {
			files = append(files, filepath.Join(l.Dir, f))
		}
		units = append(units, typedUnit{path: path, layer: layers[path], files: files})
	}
	findings, checked, err := typedCheck(units, strict, layers, listed)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("domain typed: %d packages (%d strict cores), %d files type-checked in %s", len(units), len(strict), checked,
		time.Since(start).Round(time.Millisecond))
	if len(units) == 0 || checked == 0 {
		t.Fatalf("the typed domain rules inspected nothing: %d cores, %d packages", len(strict), len(units))
	}
	for _, f := range findings {
		t.Error(f.pos.String() + ": " + f.msg)
	}
}

// Every typed rule fails on its planted case and passes its compliant twin (testdata/domaintyped: an application
// package and a core, one that reaches a core only through its adapter and a helper that reaches none; each case is
// marked with a "planted: <rule>" comment on its line, and a twin names none).
func TestDomainTypedPlanted(t *testing.T) {
	planted := filepath.Join("testdata", "domaintyped")
	units := []typedUnit{ // the ones another imports first
		{path: "planted/helper", layer: "library"},
		{path: "planted/app", layer: "application"},
		{path: "planted/core", layer: "core"},
		{path: "planted/viaadapt", layer: "application"},
	}
	want := map[string]bool{}
	for i := range units {
		dir := filepath.Join(planted, filepath.Base(units[i].path))
		names, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(names) == 0 {
			t.Fatalf("no planted files in %s: %v", dir, err)
		}
		for _, name := range names {
			units[i].files = append(units[i].files, name)
			b, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			for n, line := range strings.Split(string(b), "\n") {
				if _, rule, ok := strings.Cut(line, "// planted: "); ok {
					want[rule+" "+filepath.Base(name)+":"+fmt.Sprint(n+1)] = true
				}
			}
		}
	}
	strict := map[string]bool{serverModule + "/internal/assistant/fleetcount": true, serverModule + "/internal/agentref": true,
		"planted/core": true}
	layers := map[string]string{serverModule + "/internal/assistant/fleetcount/fleetcountadapt": "adapter",
		serverModule + "/internal/agentref/agentrefadapt": "adapter"}
	for _, u := range units {
		layers[u.path] = u.layer
	}
	listed, err := goListExport(serverDir, []string{serverModule + "/internal/assistant/fleetcount/fleetcountadapt",
		serverModule + "/internal/agentref/agentrefadapt", "context", "fmt", "maps", "slices", "encoding/json", "log/slog"})
	if err != nil {
		t.Fatal(err)
	}
	findings, _, err := typedCheck(units, strict, layers, listed)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, f := range findings {
		got[f.rule+" "+filepath.Base(f.pos.Filename)+":"+fmt.Sprint(f.pos.Line)] = true
		if !strings.Contains(f.msg, ". WHY ") || !strings.Contains(f.msg, ". GOOD ") {
			t.Errorf("message without WHY/GOOD: %s", f.msg)
		}
	}
	for k := range want {
		if !got[k] {
			t.Errorf("planted %s: not found", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("found %s, which no planted comment names", k)
		}
	}
	for id := range typedRules {
		planted := false
		for k := range want {
			planted = planted || strings.HasPrefix(k, id+" ")
		}
		if !planted {
			t.Errorf("rule %s has no planted case", id)
		}
		if _, err := os.Stat(filepath.Join(serverDir, "..", strings.Fields(typedRules[id].good)[0])); err != nil {
			t.Errorf("rule %s's GOOD path: %v", id, err)
		}
	}
}

// reachesStrict says of a package of the tree whether its production imports reach a strict core, directly or through
// other packages of the tree.
func reachesStrict(byPath map[string]*domainrules.Pkg, strict map[string]bool) func(path string) bool {
	memo := map[string]bool{}
	var reach func(path string) bool
	reach = func(path string) bool {
		if r, ok := memo[path]; ok {
			return r
		}
		memo[path] = false // production imports cannot cycle; this only ends the walk if they did
		r := false
		if p := byPath[path]; p != nil {
			for _, f := range p.Files {
				for _, imp := range f.Imports {
					r = r || !f.Test && (strict[imp.Path] || reach(imp.Path))
				}
			}
		}
		memo[path] = r
		return r
	}
	return reach
}

// The typed rules check a package that reaches a strict core only through another package's production code, and
// leave out one that reaches it only from its tests.
func TestDomainTypedReach(t *testing.T) {
	const module = "planted"
	pkgs, err := domainrules.LoadFiles(module, map[string]string{
		"core/core.go":          "// Package core.\n//\n//persona:layer core\npackage core\n",
		"coreadapt/adapt.go":    "// Package coreadapt.\n//\n//persona:layer adapter\npackage coreadapt\n\nimport _ \"planted/core\"\n",
		"app/app.go":            "// Package app.\npackage app\n\nimport _ \"planted/coreadapt\"\n",
		"tested/tested.go":      "// Package tested.\npackage tested\n",
		"tested/tested_test.go": "package tested\n\nimport _ \"planted/core\"\n",
		"alone/alone.go":        "// Package alone.\npackage alone\n\nimport _ \"strings\"\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]*domainrules.Pkg{}
	for _, p := range pkgs {
		byPath[p.Path] = p
	}
	reach := reachesStrict(byPath, map[string]bool{module + "/core": true})
	for path, want := range map[string]bool{"coreadapt": true, "app": true, "tested": false, "alone": false} {
		if byPath[module+"/"+path] == nil {
			t.Fatalf("%s was not loaded", path)
		}
		if got := reach(module + "/" + path); got != want {
			t.Errorf("%s reaches the core: %v, want %v", path, got, want)
		}
	}
}

// listedPkg is what go list says of a package.
type listedPkg struct {
	ImportPath, Dir, Export string
	GoFiles, Deps           []string
}

// goListExport is go list -export -deps over pkgs, from dir: every package they reach, with its export data file.
func goListExport(dir string, pkgs []string) (map[string]listedPkg, error) {
	cmd := exec.Command("go", append([]string{"list", "-export", "-deps", "-json=ImportPath,Dir,Export,GoFiles,Deps"}, pkgs...)...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -export: %v: %s", err, stderr.String())
	}
	listed := map[string]listedPkg{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		listed[p.ImportPath] = p
	}
	return listed, nil
}

// typedCheck type-checks each unit from source, against the export data listed of what it imports, and runs the
// rules over it; layers is the layer each package of the tree declares. It returns the findings and the number of
// files checked.
func typedCheck(units []typedUnit, strict map[string]bool, layers map[string]string,
	listed map[string]listedPkg) ([]typedFinding, int, error) {
	fset := token.NewFileSet()
	imp := unitImporter{listed: listed, checked: map[string]*types.Package{},
		gc: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
			p, ok := listed[path]
			if !ok || p.Export == "" {
				return nil, fmt.Errorf("no export data for %s", path)
			}
			return os.Open(p.Export)
		})}
	var findings []typedFinding
	files := 0
	for _, u := range units {
		var parsed []*ast.File
		for _, name := range u.files {
			src, err := os.ReadFile(name)
			if err != nil {
				return nil, 0, err
			}
			f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution|parser.ParseComments)
			if err != nil {
				return nil, 0, err
			}
			parsed = append(parsed, f)
		}
		info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{},
			Uses: map[*ast.Ident]types.Object{}}
		conf := types.Config{Importer: imp}
		pkg, err := conf.Check(u.path, fset, parsed, info)
		if err != nil {
			return nil, 0, fmt.Errorf("type-checking %s: %v", u.path, err)
		}
		files += len(parsed)
		imp.checked[u.path] = pkg
		c := typedChecks{fset: fset, unit: u, pkg: pkg, info: info, strict: strict, layers: layers}
		c.fail = c.failInterface()
		if c.cores, err = reachedCores(pkg, strict, listed, imp); err != nil {
			return nil, 0, fmt.Errorf("type-checking %s: %v", u.path, err)
		}
		for _, f := range parsed {
			c.file(f)
		}
		c.visitors(parsed)
		findings = append(findings, c.findings...)
	}
	slices.SortFunc(findings, func(a, b typedFinding) int {
		return strings.Compare(a.pos.String()+a.rule, b.pos.String()+b.rule)
	})
	return findings, files, nil
}

// unitImporter imports a package from its gc export data, or, when go list knows no such package (a planted one), as
// typedCheck type-checked it from source before: the planted units import one another that way.
type unitImporter struct {
	gc      types.Importer
	listed  map[string]listedPkg
	checked map[string]*types.Package
}

func (u unitImporter) Import(path string) (*types.Package, error) {
	if _, known := u.listed[path]; !known && u.checked[path] != nil {
		return u.checked[path], nil
	}
	return u.gc.Import(path)
}

// typedChecks is the rules over one type-checked unit.
type typedChecks struct {
	fset     *token.FileSet
	unit     typedUnit
	pkg      *types.Package
	info     *types.Info
	strict   map[string]bool
	layers   map[string]string
	fail     *types.Interface // the kit's fail.Fail, nil when the unit reaches none
	cores    []*types.Package // the strict cores the unit reaches, directly or through what it imports
	findings []typedFinding
}

func (c *typedChecks) add(pos token.Pos, rule, what string) {
	c.findings = append(c.findings, typedFinding{rule: rule, pos: c.fset.Position(pos), msg: typedMessage(rule, what)})
}

// core says whether the unit is a core, which the syntax rules hold to discarding nothing already.
func (c *typedChecks) core() bool { return c.unit.layer == "core" || c.unit.layer == "core shared" }

// visits says whether a layer may declare a core's visitor: adapters, testkit and cores.
func visits(layer string) bool {
	return layer == "adapter" || layer == "testkit" || layer == "core" || layer == "core shared"
}

func (c *typedChecks) file(f *ast.File) {
	var fnResults []*types.Tuple
	var walk func(ast.Node)
	walk = func(node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				var res *types.Tuple
				if obj, ok := c.info.Defs[x.Name].(*types.Func); ok {
					if sig, ok := obj.Type().(*types.Signature); ok {
						res = sig.Results()
					}
				}
				if x.Body != nil {
					fnResults = append(fnResults, res)
					walk(x.Body)
					fnResults = fnResults[:len(fnResults)-1]
				}
				return false
			case *ast.FuncLit:
				var res *types.Tuple
				if sig, ok := c.info.Types[x.Type].Type.(*types.Signature); ok {
					res = sig.Results()
				}
				if x.Body != nil {
					fnResults = append(fnResults, res)
					walk(x.Body)
					fnResults = fnResults[:len(fnResults)-1]
				}
				return false
			case *ast.ExprStmt:
				if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok && !c.core() {
					if t := c.info.Types[call].Type; t != nil && c.mustUse(t) {
						c.add(call.Pos(), "typed-discard", types.ExprString(call.Fun)+" discards "+c.typeName(t))
					}
				}
			case *ast.AssignStmt:
				if !c.core() {
					c.blanks(x)
				}
				c.assignAny(x)
			case *ast.ValueSpec:
				c.valueSpecAny(x)
			case *ast.CompositeLit:
				c.compositeLitAny(x)
			case *ast.SendStmt:
				c.sendAny(x)
			case *ast.ReturnStmt:
				if len(fnResults) > 0 {
					c.returnAny(x, fnResults[len(fnResults)-1])
				}
			case *ast.CallExpr:
				c.codec(x)
			case *ast.RangeStmt:
				if c.core() {
					if t := c.info.Types[x.X].Type; t != nil {
						if _, ok := t.Underlying().(*types.Map); ok {
							c.add(x.Pos(), "typed-map-range", c.unit.path+" ranges over "+types.ExprString(x.X)+", a map")
						}
					}
				}
			case *ast.BinaryExpr:
				if x.Op == token.EQL || x.Op == token.NEQ {
					l, r := c.info.Types[x.X], c.info.Types[x.Y]
					if !l.IsNil() && !r.IsNil() && c.isFail(l.Type) && c.isFail(r.Type) {
						c.add(x.Pos(), "typed-fail-compare", types.ExprString(x)+" compares two failures")
					}
				}
			}
			return true
		})
	}
	walk(f)
}

// blanks is typed-discard on an assignment's blank identifiers: an outcome or a failure assigned to _ is dropped.
func (c *typedChecks) blanks(a *ast.AssignStmt) {
	var results []types.Type
	if len(a.Rhs) == 1 && len(a.Lhs) > 1 {
		if tuple, ok := c.info.Types[a.Rhs[0]].Type.(*types.Tuple); ok {
			for i := range tuple.Len() {
				results = append(results, tuple.At(i).Type())
			}
		}
	} else {
		for _, r := range a.Rhs {
			results = append(results, c.info.Types[r].Type)
		}
	}
	for i, l := range a.Lhs {
		if id, ok := l.(*ast.Ident); ok && id.Name == "_" && i < len(results) && results[i] != nil && c.mustUse(results[i]) {
			c.add(l.Pos(), "typed-discard", "an assignment to _ discards "+c.typeName(results[i]))
		}
	}
}

// codecs are the functions and methods that read a value's fields through reflection, by package and name ("" for
// any name in the package).
var codecs = map[string][]string{
	"fmt":           {""},
	"log":           {""},
	"log/slog":      {""},
	"encoding/json": {"Marshal", "MarshalIndent", "Encode"},
	"text/template": {"Execute", "ExecuteTemplate"},
	"html/template": {"Execute", "ExecuteTemplate"},
}

// isAny reports whether t's underlying type is the empty interface (any / interface{}), excluding type parameters.
func isAny(t types.Type) bool {
	if t == nil {
		return false
	}
	if _, tp := types.Unalias(t).(*types.TypeParam); tp {
		return false
	}
	iface, ok := t.Underlying().(*types.Interface)
	return ok && iface.Empty()
}

// exprValues yields the (position, expression text, type) of each value in exprs, unpacking a single multi-return call
// into its tuple elements when expected > 1.
func (c *typedChecks) exprValues(exprs []ast.Expr, expected int) []struct {
	pos  token.Pos
	text string
	typ  types.Type
} {
	var out []struct {
		pos  token.Pos
		text string
		typ  types.Type
	}
	if len(exprs) == 1 && expected > 1 {
		if tuple, ok := c.info.Types[exprs[0]].Type.(*types.Tuple); ok {
			pos, text := exprs[0].Pos(), types.ExprString(exprs[0])
			for i := range tuple.Len() {
				out = append(out, struct {
					pos  token.Pos
					text string
					typ  types.Type
				}{pos: pos, text: text, typ: tuple.At(i).Type()})
			}
			return out
		}
	}
	for _, e := range exprs {
		out = append(out, struct {
			pos  token.Pos
			text string
			typ  types.Type
		}{pos: e.Pos(), text: types.ExprString(e), typ: c.info.Types[e].Type})
	}
	return out
}

// codec is typed-codec on one call or type conversion: an argument that is, or holds, a strict core's value passed to
// a reflection codec, passed to an any / ...any parameter (such as a custom logger logf func(string, ...any)), or
// converted directly to any.
func (c *typedChecks) codec(call *ast.CallExpr) {
	funTV := c.info.Types[call.Fun]
	if funTV.IsType() {
		if len(call.Args) == 1 && isAny(funTV.Type) {
			arg := call.Args[0]
			if t := c.info.Types[arg].Type; t != nil {
				if core := c.holdsCore(t, map[types.Type]bool{}); core != "" {
					c.add(arg.Pos(), "typed-codec", types.ExprString(arg)+" holds "+core+", converted to "+c.typeName(funTV.Type))
				}
			}
		}
		return
	}
	var obj types.Object
	switch fn := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		obj = c.info.Uses[fn]
	case *ast.SelectorExpr:
		obj = c.info.Uses[fn.Sel]
	}
	if obj != nil && obj.Pkg() != nil {
		if names, ok := codecs[obj.Pkg().Path()]; ok && (slices.Contains(names, "") || slices.Contains(names, obj.Name())) {
			for _, v := range c.exprValues(call.Args, 2) {
				if v.typ != nil {
					if core := c.holdsCore(v.typ, map[types.Type]bool{}); core != "" {
						c.add(v.pos, "typed-codec", v.text+" holds "+core+", passed to "+obj.Pkg().Path()+"."+obj.Name())
					}
				}
			}
			return
		}
	}
	if funTV.Type == nil {
		return
	}
	sig, ok := funTV.Type.Underlying().(*types.Signature)
	if !ok {
		return
	}
	params := sig.Params()
	n := params.Len()
	if n == 0 {
		return
	}
	ellipsis := call.Ellipsis.IsValid()
	isContextWithValue := obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "WithValue"
	for i, v := range c.exprValues(call.Args, n) {
		if v.typ == nil {
			continue
		}
		if isContextWithValue && i == 2 {
			// context.WithValue stores an opaque request-scoped value and never reflects on its fields
			// (its String method prints "<not Stringer>" for non-Stringer values).
			continue
		}
		var pt types.Type
		if sig.Variadic() && i >= n-1 {
			if ellipsis && i == n-1 {
				pt = params.At(n - 1).Type()
			} else if sl, ok := params.At(n - 1).Type().(*types.Slice); ok {
				pt = sl.Elem()
			}
		} else if i < n {
			pt = params.At(i).Type()
		}
		if !isAny(pt) {
			continue
		}
		if core := c.holdsCore(v.typ, map[types.Type]bool{}); core != "" {
			c.add(v.pos, "typed-codec", v.text+" holds "+core+", passed to "+types.ExprString(call.Fun)+" as "+c.typeName(pt))
		}
	}
}

func (c *typedChecks) checkAnyValue(pos token.Pos, text string, target, val types.Type, where string) {
	if !isAny(target) || val == nil {
		return
	}
	if core := c.holdsCore(val, map[types.Type]bool{}); core != "" {
		c.add(pos, "typed-codec", text+" holds "+core+", "+where+" "+c.typeName(target))
	}
}

func (c *typedChecks) assignAny(a *ast.AssignStmt) {
	if a.Tok != token.ASSIGN {
		return
	}
	vals := c.exprValues(a.Rhs, len(a.Lhs))
	for i, lhs := range a.Lhs {
		if id, ok := lhs.(*ast.Ident); ok && id.Name == "_" {
			continue
		}
		if i < len(vals) {
			c.checkAnyValue(vals[i].pos, vals[i].text, c.info.Types[lhs].Type, vals[i].typ, "assigned to")
		}
	}
}

func (c *typedChecks) valueSpecAny(s *ast.ValueSpec) {
	if s.Type == nil || len(s.Values) == 0 {
		return
	}
	target := c.info.Types[s.Type].Type
	if !isAny(target) {
		return
	}
	for _, v := range c.exprValues(s.Values, len(s.Names)) {
		c.checkAnyValue(v.pos, v.text, target, v.typ, "assigned to")
	}
}

func (c *typedChecks) compositeLitAny(lit *ast.CompositeLit) {
	lt := c.info.Types[lit].Type
	if lt == nil {
		return
	}
	switch u := lt.Underlying().(type) {
	case *types.Slice:
		if !isAny(u.Elem()) {
			return
		}
		for _, elt := range lit.Elts {
			val := elt
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				val = kv.Value
			}
			c.checkAnyValue(val.Pos(), types.ExprString(val), u.Elem(), c.info.Types[val].Type, "placed in")
		}
	case *types.Array:
		if !isAny(u.Elem()) {
			return
		}
		for _, elt := range lit.Elts {
			val := elt
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				val = kv.Value
			}
			c.checkAnyValue(val.Pos(), types.ExprString(val), u.Elem(), c.info.Types[val].Type, "placed in")
		}
	case *types.Map:
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			c.checkAnyValue(kv.Key.Pos(), types.ExprString(kv.Key), u.Key(), c.info.Types[kv.Key].Type, "used as map key of")
			c.checkAnyValue(kv.Value.Pos(), types.ExprString(kv.Value), u.Elem(), c.info.Types[kv.Value].Type, "placed in")
		}
	case *types.Struct:
		for i, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if id, ok := kv.Key.(*ast.Ident); ok {
					if obj := c.info.Uses[id]; obj != nil {
						c.checkAnyValue(kv.Value.Pos(), types.ExprString(kv.Value), obj.Type(), c.info.Types[kv.Value].Type, "placed in field of")
					}
				}
			} else if i < u.NumFields() {
				c.checkAnyValue(elt.Pos(), types.ExprString(elt), u.Field(i).Type(), c.info.Types[elt].Type, "placed in field of")
			}
		}
	}
}

func (c *typedChecks) sendAny(s *ast.SendStmt) {
	ct := c.info.Types[s.Chan].Type
	if ct == nil {
		return
	}
	if ch, ok := ct.Underlying().(*types.Chan); ok {
		c.checkAnyValue(s.Value.Pos(), types.ExprString(s.Value), ch.Elem(), c.info.Types[s.Value].Type, "sent to channel of")
	}
}

func (c *typedChecks) returnAny(r *ast.ReturnStmt, results *types.Tuple) {
	if results == nil || results.Len() == 0 || len(r.Results) == 0 {
		return
	}
	vals := c.exprValues(r.Results, results.Len())
	for i := range results.Len() {
		if i < len(vals) {
			c.checkAnyValue(vals[i].pos, vals[i].text, results.At(i).Type(), vals[i].typ, "returned as")
		}
	}
}

// holdsCore is the strict core type a value of type t is or holds (through pointers, slices, arrays, maps and struct
// fields), "" for none.
func (c *typedChecks) holdsCore(t types.Type, seen map[types.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && c.strict[n.Obj().Pkg().Path()] {
		return n.Obj().Pkg().Name() + "." + n.Obj().Name()
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return c.holdsCore(u.Elem(), seen)
	case *types.Slice:
		return c.holdsCore(u.Elem(), seen)
	case *types.Array:
		return c.holdsCore(u.Elem(), seen)
	case *types.Map:
		if k := c.holdsCore(u.Key(), seen); k != "" {
			return k
		}
		return c.holdsCore(u.Elem(), seen)
	case *types.Struct:
		for i := range u.NumFields() {
			if k := c.holdsCore(u.Field(i).Type(), seen); k != "" {
				return k
			}
		}
	}
	return ""
}

// mustUse says whether a value of type t may not be dropped: an outcome.Of, or a failure.
func (c *typedChecks) mustUse(t types.Type) bool {
	if n, ok := t.(*types.Named); ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == domainKit+"/outcome" &&
		n.Obj().Name() == "Of" {
		return true
	}
	return c.isFail(t)
}

// isFail says whether t is a failure: fail.Fail, a family that embeds it, or a type that implements it.
func (c *typedChecks) isFail(t types.Type) bool {
	if c.fail == nil || t == nil {
		return false
	}
	if _, basic := t.(*types.Basic); basic {
		return false
	}
	return types.Implements(t, c.fail)
}

// failInterface is the kit's fail.Fail as the unit sees it, nil when the unit reaches no failure at all.
func (c *typedChecks) failInterface() *types.Interface {
	var found *types.Interface
	var walk func(p *types.Package, seen map[*types.Package]bool)
	walk = func(p *types.Package, seen map[*types.Package]bool) {
		if found != nil || seen[p] {
			return
		}
		seen[p] = true
		if p.Path() == domainKit+"/fail" {
			if obj := p.Scope().Lookup("Fail"); obj != nil {
				found, _ = obj.Type().Underlying().(*types.Interface)
			}
			return
		}
		for _, i := range p.Imports() {
			walk(i, seen)
		}
	}
	walk(c.pkg, map[*types.Package]bool{})
	return found
}

// typeName is t as a message names it.
func (c *typedChecks) typeName(t types.Type) string {
	return types.TypeString(t, func(p *types.Package) string { return p.Name() })
}

// reachedCores is every strict core pkg reaches: the ones it imports and the ones what it imports builds on (go list's
// Deps), as the importer that type-checked pkg loads them, so that their types are the ones pkg's code names.
func reachedCores(pkg *types.Package, strict map[string]bool, listed map[string]listedPkg,
	imp types.Importer) ([]*types.Package, error) {
	paths := map[string]bool{}
	for _, p := range pkg.Imports() {
		for _, path := range append([]string{p.Path()}, listed[p.Path()].Deps...) {
			paths[path] = paths[path] || strict[path]
		}
	}
	var cores []*types.Package
	for _, path := range slices.Sorted(maps.Keys(paths)) {
		if !paths[path] {
			continue
		}
		core, err := imp.Import(path)
		if err != nil {
			return nil, err
		}
		cores = append(cores, core)
	}
	return cores, nil
}

// visitors is typed-visitor-impl over the unit: a type that implements the *Visitor interface of a strict core the
// unit reaches (imported or not: an adapter can hand it the core's value), outside adapters, testkit and cores. It reads
// the types the unit declares, and the type of every expression in it, which also finds a visitor another package
// declares (a helper that reaches no core itself) and a generic one that implements the interface only once its type
// arguments are the core's.
func (c *typedChecks) visitors(files []*ast.File) {
	if visits(c.unit.layer) {
		return
	}
	var ifaces []*types.TypeName
	for _, p := range c.cores {
		for _, name := range p.Scope().Names() {
			obj, ok := p.Scope().Lookup(name).(*types.TypeName)
			if !ok || !obj.Exported() || !strings.HasSuffix(name, "Visitor") {
				continue
			}
			if iface, ok := obj.Type().Underlying().(*types.Interface); ok && iface.NumMethods() > 0 {
				ifaces = append(ifaces, obj)
			}
		}
	}
	if len(ifaces) == 0 {
		return
	}
	reported := map[*types.TypeName]bool{}
	implements := func(pos token.Pos, t types.Type, origin *types.TypeName) {
		for _, v := range ifaces {
			iface := v.Type().Underlying().(*types.Interface)
			if types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface) {
				reported[origin] = true
				c.add(pos, "typed-visitor-impl", c.typeName(t)+" implements "+v.Pkg().Name()+"."+v.Name()+" outside an adapter")
			}
		}
	}
	for id, obj := range c.info.Defs {
		tn, ok := obj.(*types.TypeName)
		if !ok || id.Name == "_" {
			continue
		}
		if named, ok := tn.Type().(*types.Named); ok && named.TypeParams().Len() > 0 {
			continue // a generic type is read where it is instantiated, below
		}
		implements(id.Pos(), tn.Type(), tn)
	}
	seen := map[*types.Named]bool{}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			e, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			t := c.info.Types[e].Type
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			named, ok := t.(*types.Named)
			if !ok || seen[named] {
				return true
			}
			seen[named] = true
			origin := named.Origin().Obj()
			if reported[origin] || origin.Pkg() == nil || origin.Pkg() == c.pkg && named.TypeArgs().Len() == 0 ||
				c.strict[origin.Pkg().Path()] || visits(c.layers[origin.Pkg().Path()]) {
				return true // read with its declaration above, or declared where a visitor may be
			}
			implements(e.Pos(), named, origin)
			return true
		})
	}
}
