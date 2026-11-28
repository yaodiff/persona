package archguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// A module of the assistant's kernel answers only the bus methods a domain lists as a module's (assistantwire's
// addModuleMethods, which seam.NewBusMethod reads), and BusHub.request asks the kernel after its own cases and before
// the extension manager and the devices. So no listed name may be a case of that central dispatch: a case before the
// kernel would answer it first and leave the module's method dead, and one after it would lose its calls to the
// module. This guard reads the dispatch's cases from the source, so it holds without running package assistant's
// tests (TestTheCentralSwitchLeavesTheKernelsBusMethods there sends each listed name through BusHub.request).

// busDispatch are the functions that answer the assistant's bus calls besides its kernel, by package, receiver and
// name: BusHub.request, with each function of its package it hands the method name to, and the Request of the
// extension manager and of the devices, which BusHub.request calls through its Options (assistant/main.go makes them
// with extmgr.New and devices.New).
var busDispatch = []struct{ dir, recv, name string }{
	{"internal/assistant", "BusHub", "request"},
	{"internal/extmgr", "Manager", "Request"},
	{"internal/devices", "Devices", "Request"},
}

// moduleMethodsDir is the package whose addModuleMethods calls list the modules' bus methods.
const moduleMethodsDir = "internal/assistantwire"

const moduleMethodsHint = "a module's bus method is a name nothing else answers: drop the central case, or take the " +
	"name off the module's list (assistantwire/module_methods.go; docs/server/architecture.md \"Validated and sealed\")"

// TestModuleBusMethodsAreNoCentralCase fails when a name a domain lists as a module's bus method is a case of the
// central dispatch, or lies under a prefix it routes, when a name is listed twice, and when the guard cannot read a
// case's name.
func TestModuleBusMethodsAreNoCentralCase(t *testing.T) {
	failures, err := checkModuleBusMethods(newSourceTree(nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range failures {
		t.Error(f)
	}
}

// The guard on planted sources: a case for a listed name in BusHub.request, a comparison in a function it hands the
// name to, a listed name the devices answer, a name listed twice and a case it cannot read each fail. It reads the
// cases of every part of the dispatch, the functions it is handed to included.
func TestModuleBusMethodsGuardFindsPlantedCases(t *testing.T) {
	cases, unread, err := newSourceTree(nil).dispatchCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(unread) > 0 {
		t.Errorf("cases the guard cannot read on the tree as it is: %v", unread)
	}
	for _, want := range []string{
		"http.request",              // BusHub.request's own switch
		"kb.owner",                  // kbRequest, which it hands the name to
		"assistant.extension_event", // extensionEventRequest's comparison
		"devices.call",              // its comparison after the kernel
		"extensions.accept",         // the extension manager's Request
		"devices.hold_start",        // the devices' Request
	} {
		if !slices.ContainsFunc(cases, func(c busCase) bool { return c.value == want && !c.prefix }) {
			t.Errorf("the guard does not read %s among the central dispatch's cases", want)
		}
	}

	read := func(path string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(serverDir, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	plant := func(path, old, new string) map[string]string {
		t.Helper()
		src := read(path)
		if !strings.Contains(src, old) {
			t.Fatalf("%s no longer has %q: plant the case elsewhere", path, old)
		}
		return map[string]string{path: strings.Replace(src, old, new, 1)}
	}
	const busFile, eventFile = "internal/assistant/bus.go", "internal/assistant/extension_event.go"
	const httpCase = "\tcase assistantwire.MethodHTTP:\n"
	const eventTest = "\tif method != assistantwire.MethodExtensionEvent {\n"
	const listed = "internal/assistantwire/planted_methods.go"
	for _, c := range []struct {
		name    string
		planted map[string]string
		want    string
	}{
		{"a case in BusHub.request", plant(busFile, httpCase, "\tcase assistantwire.MethodInboxItem:\n\t\treturn nil, nil\n"+httpCase),
			"inbox.item is listed as a module's bus method"},
		{"a comparison in a function it hands the name to",
			plant(eventFile, eventTest, "\tif method == \"inbox.forget\" {\n\t\treturn nil, true, nil\n\t}\n"+eventTest),
			"inbox.forget is listed as a module's bus method"},
		{"a listed name the devices answer",
			map[string]string{listed: "package assistantwire\n\nfunc init() { addModuleMethods(\"devices.hold_start\") }\n"},
			"devices.hold_start is listed as a module's bus method"},
		{"a name listed twice", map[string]string{listed: "package assistantwire\n\nfunc init() { addModuleMethods(MethodInboxItem) }\n"},
			"inbox.item is listed as a module's bus method twice"},
		{"a case the guard cannot read", plant(busFile, httpCase, "\tcase plantedName():\n\t\treturn nil, nil\n"+httpCase),
			"plantedName(): the guard cannot read this case's name"},
	} {
		failures, err := checkModuleBusMethods(newSourceTree(c.planted))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !slices.ContainsFunc(failures, func(f string) bool { return strings.Contains(f, c.want) }) {
			t.Errorf("%s: the guard reports %q, want one with %q", c.name, failures, c.want)
		}
	}
}

// checkModuleBusMethods is what the guard reports of the tree s: each listed module bus method that the central
// dispatch answers, by a case or a prefix it routes, each name listed twice, and each case whose name it cannot read.
func checkModuleBusMethods(s *sourceTree) ([]string, error) {
	listed, err := s.moduleMethods()
	if err != nil {
		return nil, err
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("%s lists no module bus method (addModuleMethods): point the guard at the list", moduleMethodsDir)
	}
	cases, unread, err := s.dispatchCases()
	if err != nil {
		return nil, err
	}
	var failures []string
	for _, u := range unread {
		failures = append(failures, u+": the guard cannot read this case's name, so it cannot tell it from a module's "+
			"bus method: make it a string literal or constant")
	}
	first := map[string]string{}
	for _, m := range listed {
		if at, twice := first[m.value]; twice {
			failures = append(failures, fmt.Sprintf("%s: %s is listed as a module's bus method twice (first at %s)",
				m.at, m.value, at))
			continue
		}
		first[m.value] = m.at
		for _, c := range cases {
			if c.value == m.value && !c.prefix || c.prefix && strings.HasPrefix(m.value, c.value) {
				failures = append(failures, fmt.Sprintf("%s: %s is listed as a module's bus method, and the central "+
					"dispatch answers it at %s (%s): %s", m.at, m.value, c.at, c.text, moduleMethodsHint))
			}
		}
	}
	return failures, nil
}

// A listedMethod is a name a domain lists as a module's bus method, and where.
type listedMethod struct{ value, at string }

// moduleMethods are the names moduleMethodsDir's addModuleMethods calls list, each a string literal or constant.
func (s *sourceTree) moduleMethods() ([]listedMethod, error) {
	p, err := s.pkg(moduleMethodsDir)
	if err != nil {
		return nil, err
	}
	var out []listedMethod
	var bad []string
	for _, f := range p.files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); !ok || id.Name != "addModuleMethods" {
				return true
			}
			for _, a := range call.Args {
				v, ok := s.value(p, f, a)
				if !ok {
					bad = append(bad, pos(s.fset, a)+": "+types.ExprString(a))
					continue
				}
				out = append(out, listedMethod{v, pos(s.fset, a)})
			}
			return true
		})
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("list each module bus method as a string constant, so the guard reads it: %s", strings.Join(bad, ", "))
	}
	return out, nil
}

// A busCase is a name the central dispatch answers, or a prefix it routes every name under, and where it is read.
type busCase struct {
	value  string
	prefix bool
	at     string
	text   string
}

// dispatchCases reads the cases of busDispatch's functions: the labels of a switch on the method name, the names it is
// compared with (== and !=) and the prefixes it is routed on (strings.HasPrefix and the like); and the same in each
// function of the package the name is handed to as an argument, under that function's own name for the parameter.
// unread are the cases whose name the guard cannot read: it compares names, so a case is a string literal or constant.
func (s *sourceTree) dispatchCases() (cases []busCase, unread []string, err error) {
	for _, d := range busDispatch {
		p, err := s.pkg(d.dir)
		if err != nil {
			return nil, nil, err
		}
		type funcRef struct {
			fn   *ast.FuncDecl
			file *ast.File
		}
		byName := map[string][]funcRef{}
		for _, f := range p.files {
			for _, decl := range f.Decls {
				if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
					byName[fn.Name.Name] = append(byName[fn.Name.Name], funcRef{fn, f})
				}
			}
		}
		i := slices.IndexFunc(byName[d.name], func(r funcRef) bool {
			return r.fn.Recv != nil && len(r.fn.Recv.List) == 1 && receiverType(r.fn.Recv.List[0].Type) == d.recv
		})
		if i < 0 {
			return nil, nil, fmt.Errorf("%s has no (*%s).%s: point busDispatch at where the bus calls are answered now",
				d.dir, d.recv, d.name)
		}
		seen := map[*ast.FuncDecl]map[string]bool{}
		var walk func(r funcRef, param string)
		walk = func(r funcRef, param string) {
			if seen[r.fn][param] {
				return
			}
			if seen[r.fn] == nil {
				seen[r.fn] = map[string]bool{}
			}
			seen[r.fn][param] = true
			isParam := func(e ast.Expr) bool { id, ok := e.(*ast.Ident); return ok && id.Name == param }
			add := func(e ast.Expr, prefix bool) {
				text := types.ExprString(e)
				v, ok := s.value(p, r.file, e)
				if !ok {
					unread = append(unread, pos(s.fset, e)+": "+text)
					return
				}
				cases = append(cases, busCase{v, prefix, pos(s.fset, e), text})
			}
			ast.Inspect(r.fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.SwitchStmt:
					if n.Tag != nil && isParam(n.Tag) {
						for _, c := range n.Body.List {
							for _, e := range c.(*ast.CaseClause).List {
								add(e, false)
							}
						}
					}
				case *ast.BinaryExpr:
					if n.Op == token.EQL || n.Op == token.NEQ {
						if isParam(n.X) {
							add(n.Y, false)
						} else if isParam(n.Y) {
							add(n.X, false)
						}
					}
				case *ast.CallExpr:
					if sel, ok := n.Fun.(*ast.SelectorExpr); ok && len(n.Args) == 2 && isParam(n.Args[0]) &&
						slices.Contains([]string{"HasPrefix", "CutPrefix", "TrimPrefix"}, sel.Sel.Name) {
						add(n.Args[1], true)
					}
					name := localCallee(r.file, n.Fun)
					for i, a := range n.Args {
						if name == "" || !isParam(a) {
							continue
						}
						for _, callee := range byName[name] {
							if q := paramName(callee.fn, i); q != "" && q != "_" {
								walk(callee, q)
							}
						}
					}
				}
				return true
			})
		}
		entry := byName[d.name][i]
		if paramName(entry.fn, 1) != "method" {
			return nil, nil, fmt.Errorf("(*%s).%s in %s no longer takes the method name second, as method: point the "+
				"guard at the parameter", d.recv, d.name, d.dir)
		}
		walk(entry, "method")
	}
	return cases, unread, nil
}

// localCallee is the name of the function or method fun calls when it may be one of the file's own package: a plain
// name, or a method on a local value (s.kbRequest); "" for a function of an imported package or a longer selector
// (s.kernel.Answer).
func localCallee(f *ast.File, fun ast.Expr) string {
	switch fun := fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if x, ok := fun.X.(*ast.Ident); ok && !importsName(f, x.Name) {
			return fun.Sel.Name
		}
	}
	return ""
}

// paramName is the name of fn's i-th parameter, "" when it has none or the parameter is unnamed.
func paramName(fn *ast.FuncDecl, i int) string {
	for _, field := range fn.Type.Params.List {
		if len(field.Names) == 0 {
			if i == 0 {
				return ""
			}
			i--
			continue
		}
		if i < len(field.Names) {
			return field.Names[i].Name
		}
		i -= len(field.Names)
	}
	return ""
}

// A sourceTree reads the server module's packages on demand: each package's production files, parsed, and its string
// constants by name. planted replaces a file's source, or adds a file, by its path under server/ (the guard's own
// tests); the other files are parsed once per test binary.
type sourceTree struct {
	planted map[string]string
	fset    *token.FileSet
	pkgs    map[string]*sourcePackage
}

type sourcePackage struct {
	files  []*ast.File
	consts map[string]string
}

func newSourceTree(planted map[string]string) *sourceTree {
	return &sourceTree{planted: planted, fset: sourceFset, pkgs: map[string]*sourcePackage{}}
}

// sourceFset and sourceFiles are the files sourceTree parsed from disk, shared by every tree.
var (
	sourceFset  = token.NewFileSet()
	sourceMu    sync.Mutex
	sourceFiles = map[string]*ast.File{}
)

// pkg is the package in dir, a path under server/.
func (s *sourceTree) pkg(dir string) (*sourcePackage, error) {
	if p, ok := s.pkgs[dir]; ok {
		return p, nil
	}
	paths, err := filepath.Glob(filepath.Join(serverDir, dir, "*.go"))
	if err != nil {
		return nil, err
	}
	var rels []string
	for _, path := range paths {
		if !strings.HasSuffix(path, "_test.go") {
			rels = append(rels, dir+"/"+filepath.Base(path))
		}
	}
	for rel := range s.planted {
		if filepath.Dir(rel) == dir && !slices.Contains(rels, rel) {
			rels = append(rels, rel)
		}
	}
	if len(rels) == 0 {
		return nil, fmt.Errorf("no Go files in %s", dir)
	}
	p := &sourcePackage{consts: map[string]string{}}
	for _, rel := range rels {
		f, err := s.parse(rel)
		if err != nil {
			return nil, err
		}
		p.files = append(p.files, f)
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.CONST {
				continue
			}
			for _, spec := range g.Specs {
				v := spec.(*ast.ValueSpec)
				for i, id := range v.Names {
					if i >= len(v.Values) {
						continue
					}
					if lit, ok := v.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if text, err := strconv.Unquote(lit.Value); err == nil {
							p.consts[id.Name] = text
						}
					}
				}
			}
		}
	}
	s.pkgs[dir] = p
	return p, nil
}

func (s *sourceTree) parse(rel string) (*ast.File, error) {
	if src, ok := s.planted[rel]; ok {
		return parser.ParseFile(s.fset, filepath.Join(serverDir, rel), src, parser.SkipObjectResolution)
	}
	sourceMu.Lock()
	defer sourceMu.Unlock()
	if f, ok := sourceFiles[rel]; ok {
		return f, nil
	}
	f, err := parser.ParseFile(s.fset, filepath.Join(serverDir, rel), nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	sourceFiles[rel] = f
	return f, nil
}

// value is e's string when e is a string literal, a string constant of own (f's package) or of a package of the
// module f imports, or a sum of those.
func (s *sourceTree) value(own *sourcePackage, f *ast.File, e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			v, err := strconv.Unquote(e.Value)
			return v, err == nil
		}
	case *ast.ParenExpr:
		return s.value(own, f, e.X)
	case *ast.Ident:
		v, ok := own.consts[e.Name]
		return v, ok
	case *ast.SelectorExpr:
		x, ok := e.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		dir, ok := importedDir(f, x.Name)
		if !ok {
			return "", false
		}
		p, err := s.pkg(dir)
		if err != nil {
			return "", false
		}
		v, ok := p.consts[e.Sel.Name]
		return v, ok
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			a, okA := s.value(own, f, e.X)
			b, okB := s.value(own, f, e.Y)
			return a + b, okA && okB
		}
	}
	return "", false
}

// importedDir is the directory under server/ of the module package f imports as name.
func importedDir(f *ast.File, name string) (string, bool) {
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || importLocalName(imp, path) != name {
			continue
		}
		return strings.CutPrefix(path, modulePath)
	}
	return "", false
}

// importsName says whether f imports a package, of the module or not, as name.
func importsName(f *ast.File, name string) bool {
	for _, imp := range f.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && importLocalName(imp, path) == name {
			return true
		}
	}
	return false
}

// importLocalName is the name a file refers to the package imp imports by: its own name, or the path's last element.
func importLocalName(imp *ast.ImportSpec, path string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	return path[strings.LastIndex(path, "/")+1:]
}
