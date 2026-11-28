package domainrules

import (
	"go/ast"
	"go/token"
	"strings"
)

// outsideChecks is one pass of the rules on code that uses a core, the kit or an adapter.
type outsideChecks struct {
	findings
	p   *Pkg
	idx Index
	err error // the first file that could not be read while resolving a contract's, db's or prompts' type
}

// Outside is the rules on p's parsed files as users of cores, the kit and adapters (out-*), p's own rules as an
// adapter (adapter-*), and its imports as a part that takes no core (path-no-core). Files Full or the prefilter did
// not parse are left alone: they use no domain package. An adapter's rules read the declarations of the contract,
// db and prompts types it names, parsing the files that declare them (typeNamed).
func Outside(p *Pkg, idx Index) ([]Finding, error) {
	c := &outsideChecks{p: p, idx: idx}
	c.pathNoCore()
	if p.Layer == layerAdapter {
		c.adapterImports()
		if p.decls != nil {
			c.adapterDecls()
		}
	}
	for _, f := range p.Files {
		if f.ast != nil {
			c.file(f)
		}
	}
	return c.list, c.err
}

// ref is a domain package a file imports: the name it uses, the import and the package.
type ref struct {
	name string
	imp  Import
	pkg  *Pkg
}

// refs is the domain packages (cores, shared kernels, kit) a file imports, by name. A file's own package is left
// out, except for an external test file (package <name>_test), which is outside it.
func (c *outsideChecks) refs(f *File) map[string]ref {
	out := map[string]ref{}
	for name, imp := range c.idx.imports(f) {
		if p := c.idx.pkg(imp.Path); p != nil && c.foreign(f, p) && c.idx.domain(imp.Path) {
			out[name] = ref{name, imp, p}
		}
	}
	return out
}

// foreign says whether p is outside the code of f: another package, or f's own when f is an external test.
func (c *outsideChecks) foreign(f *File, p *Pkg) bool { return p != c.p || f.External }

// kitDir says whether the file is in a kit package's directory (its own tests included).
func (c *outsideChecks) kitDir() bool { return c.p.Layer == layerKit }

func (c *outsideChecks) file(f *File) {
	layer := c.idx.layer(c.p.Path)
	refs := c.refs(f)
	adapter, testkit := layer == layerAdapter, layer == layerTestkit
	inCore := core(layer) || layer == layerKit
	// Dot imports and aliases hide literals and conversions.
	for _, imp := range f.Imports {
		if imp.Name == "." && c.idx.domain(imp.Path) && c.foreign(f, c.idx.pkg(imp.Path)) {
			c.add(imp.Pos, "out-dot-import", c.idx.rel(imp.Path), f.Rel+" dot-imports "+c.idx.rel(imp.Path))
		}
	}
	key := func(r ref) string { return r.pkg.Rel }
	// The first domain package the file imports, which file-wide findings count against.
	firstKey := ""
	for _, imp := range f.Imports {
		if c.idx.domain(imp.Path) && c.foreign(f, c.idx.pkg(imp.Path)) {
			firstKey = c.idx.rel(imp.Path)
			break
		}
	}
	if firstKey != "" {
		for _, imp := range f.Imports {
			if imp.Path == "unsafe" {
				c.add(imp.Pos, "out-unsafe", firstKey, f.Rel+" uses unsafe")
			}
		}
	}
	reflectName := c.idx.stdName(f, "reflect")
	imports := c.idx.imports(f)
	adapters := map[string]bool{}
	for name, imp := range imports {
		if c.idx.layer(imp.Path) == layerAdapter && c.idx.pkg(imp.Path) != c.p {
			adapters[name] = true
		}
	}
	var stack []ast.Node
	ast.Inspect(f.ast, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		parent := ast.Node(nil)
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch x := n.(type) {
		case *ast.TypeSpec:
			if x.Assign != 0 {
				if r, t, ok := c.domainType(refs, x.Type); ok {
					c.at(f, x, "out-alias", key(r), f.Rel+" aliases "+r.name+"."+t)
				}
			}
		case *ast.CompositeLit:
			if r, t, ok := c.domainType(refs, x.Type); ok {
				c.at(f, x, "out-literal", key(r), f.Rel+" builds "+r.name+"."+t+" by literal")
			} else if elem := elementType(x.Type); elem != nil {
				// []core.T{{...}}: the elided element literals are literals of core.T.
				if r, t, ok := c.domainType(refs, elem); ok {
					for _, e := range x.Elts {
						if kv, ok := e.(*ast.KeyValueExpr); ok {
							e = kv.Value
						}
						if lit, ok := e.(*ast.CompositeLit); ok && lit.Type == nil {
							c.at(f, lit, "out-literal", key(r), f.Rel+" builds "+r.name+"."+t+" by literal")
						}
					}
				}
			}
		case *ast.CallExpr:
			if len(x.Args) == 1 && x.Ellipsis == 0 {
				if r, t, ok := c.domainType(refs, x.Fun); ok {
					c.at(f, x, "out-conversion", key(r), f.Rel+" converts to "+r.name+"."+t)
				}
			}
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Log" && !adapter {
				if id, ok := sel.X.(*ast.Ident); ok && adapters[id.Name] && !logArgument(parent, x) {
					c.at(f, x, "out-log-use", c.p.Rel, f.Rel+" keeps "+id.Name+".Log(...)")
				}
			}
		case *ast.SelectorExpr:
			id, isIdent := x.X.(*ast.Ident)
			_, isImport := imports[identName(id)]
			if x.Sel.Name == "Wire" && !(isIdent && isImport) && !adapter && !testkit && !(c.kitDir() && f.Test) {
				c.at(f, x, "out-wire-ref", c.p.Rel, f.Rel+" reads "+exprString(x.X)+".Wire")
			}
			if !isIdent {
				break
			}
			if id.Name == reflectName && reflectName != "" && x.Sel.Name != "DeepEqual" && firstKey != "" {
				c.at(f, x, "out-unsafe", firstKey, f.Rel+" uses reflect."+x.Sel.Name)
			}
			r, ok := refs[id.Name]
			if !ok {
				break
			}
			rel := r.pkg.Rel
			switch {
			case core(c.idx.layer(r.imp.Path)) && (wordPrefix(x.Sel.Name, "Parse") || wordPrefix(x.Sel.Name, "Restore")) &&
				!adapter && !testkit && !f.Test && c.declares(r.pkg, x.Sel.Name):
				c.at(f, x, "out-parse-call", rel, f.Rel+" calls "+id.Name+"."+x.Sel.Name)
			case rel == "internal/domainkit/instant" && x.Sel.Name == "ReadClock" &&
				!under(c.p.Rel, "internal/domainkit/clock") && !(c.kitDir() && f.Test):
				c.at(f, x, "out-clock-read", rel, f.Rel+" reads the clock")
			case rel == "internal/domainkit/slot" && x.Sel.Name == "NewSlot" && !adapter && !testkit && !(c.kitDir() && f.Test):
				c.at(f, x, "out-slot-mint", rel, f.Rel+" mints a Slot")
			case rel == "internal/domainkit/fail" && (x.Sel.Name == "NewCode" || x.Sel.Name == "NewPart") &&
				!inCore && !testkit:
				c.at(f, x, "out-code-mint", rel, f.Rel+" makes a failure code")
			}
		case *ast.FuncDecl:
			c.visitorImpl(f, x, refs, adapter || testkit || inCore && !f.Test)
		}
		return true
	})
}

// identName is an identifier's name, "" for none.
func identName(id *ast.Ident) string {
	if id == nil {
		return ""
	}
	return id.Name
}

// declares says whether p declares a top-level function name.
func (c *outsideChecks) declares(p *Pkg, name string) bool {
	if p.decls == nil {
		return true
	}
	for _, fd := range p.decls.funcs {
		if fd.decl.Name.Name == name {
			return true
		}
	}
	return false
}

// visitorImpl is out-visitor-impl on one method: a Visit* method whose parameters name a type of an imported core
// or the kit implements a visitor, which only adapters, testkit and a core's own tests do. The finding counts against
// the first core the parameters name (the kit when they name no core). A parameterless Visit* method names no type,
// so in a package a file of which imports a core or the kit it counts against the imported core or kit whose
// interface declares that method with matching signature (a core before the kit, then by path). A Visit* method of a
// generic type names only its type parameters, and an instance of the type (peek[fleetcount.Why]), made in any file
// of the package, implements any core's visitor, so in a package a file of which imports a core or the kit it counts
// against a core or kit package the package imports: first one that declares an interface method of its name, then a
// core, then by path; in a core's own test, only another core it imports. In a core's own test a method is judged by
// the visitor it implements, not by the packages its parameters name: one of the core's own visitors passes, whatever
// kit or shared kernel types it takes (VisitHoldUntil(until instant.Instant), VisitAsker(asker agentref.Ref)), and
// another domain package's visitor counts against that package, even when it takes only the kit's types.
func (c *outsideChecks) visitorImpl(f *File, fd *ast.FuncDecl, refs map[string]ref, allowed bool) {
	if allowed || fd.Recv == nil || !strings.HasPrefix(fd.Name.Name, "Visit") {
		return
	}
	if f.Test && core(c.p.Layer) {
		if c.implements(c.p, f, fd) {
			return // the core's own black-box tests visiting it
		}
		if r, ok := c.implemented(f, fd, refs); ok {
			recv, _ := receiverName(fd.Recv.List[0].Type)
			c.at(f, fd.Name, "out-visitor-impl", r.pkg.Rel, recv+"."+fd.Name.Name+" implements "+r.name+"'s visitor")
			return
		}
	}
	var named []ref
	for _, p := range params(fd.Type.Params) {
		pkg, _ := baseType(unwrapParam(p))
		if r, ok := refs[pkg]; ok {
			named = append(named, r)
		}
	}
	generic := ""
	if len(named) == 0 && genericReceiver(fd) {
		var first *ref
		rank := func(r ref) [2]bool { return [2]bool{declaresMethod(r.pkg, fd.Name.Name), core(r.pkg.Layer)} }
		for _, r := range c.pkgRefs() {
			if f.Test && core(c.p.Layer) && (r.pkg == c.p || !core(r.pkg.Layer)) {
				continue // a core's own tests visit it, with the kit's types
			}
			if first == nil || better(rank(r), rank(*first)) || rank(r) == rank(*first) && r.pkg.Rel < first.pkg.Rel {
				first = &r
			}
		}
		if first == nil {
			return
		}
		named = append(named, *first)
		if !declaresMethod(first.pkg, fd.Name.Name) {
			generic = " is a generic type's Visit method in a package that imports " + first.name
		}
	}
	if len(named) == 0 && len(params(fd.Type.Params)) == 0 {
		if r, ok := c.implemented(f, fd, c.pkgRefs()); ok {
			named = append(named, r)
		}
	}
	if len(named) == 0 {
		return
	}
	pick := named[0]
	for _, r := range named {
		if f.Test && r.pkg == c.p {
			return // the core's own black-box tests visiting it
		}
		if core(r.pkg.Layer) && !core(pick.pkg.Layer) {
			pick = r
		}
	}
	recv, _ := receiverName(fd.Recv.List[0].Type)
	what := " implements " + pick.name + "'s visitor"
	if generic != "" {
		what = generic
	}
	c.at(f, fd.Name, "out-visitor-impl", pick.pkg.Rel, recv+"."+fd.Name.Name+what)
}

// implemented is the domain package other than the file's own whose visitor fd implements (implements), when one of
// those the file imports declares one: a core before the kit, then by path.
func (c *outsideChecks) implemented(f *File, fd *ast.FuncDecl, refs map[string]ref) (ref, bool) {
	var first *ref
	for _, r := range refs {
		if r.pkg == c.p || !c.implements(r.pkg, f, fd) {
			continue
		}
		if first == nil || core(r.pkg.Layer) && !core(first.pkg.Layer) ||
			core(r.pkg.Layer) == core(first.pkg.Layer) && r.pkg.Rel < first.pkg.Rel {
			first = &r
		}
	}
	if first == nil {
		return ref{}, false
	}
	return *first, true
}

// implements says whether fd, a method declared in f, implements a method of an interface p declares: the same name,
// and the same parameter and result types once each type is spelled by the import path of the package that declares
// it, which reads the same from p's own files and from a test that imports p.
func (c *outsideChecks) implements(p *Pkg, f *File, fd *ast.FuncDecl) bool {
	if p.decls == nil {
		return false
	}
	mine := c.idx.imports(f)
	params, results := typeKeys(fd.Type.Params, mine, nil), typeKeys(fd.Type.Results, mine, nil)
	for _, t := range p.decls.order {
		if t.iface == nil {
			continue
		}
		theirs := c.idx.imports(t.file)
		for _, m := range t.iface.Methods.List {
			ft, ok := m.Type.(*ast.FuncType)
			if !ok {
				continue // an embedded interface: its own declaration is read where it is declared
			}
			for _, n := range m.Names {
				if n.Name == fd.Name.Name && sameKeys(typeKeys(ft.Params, theirs, p), params) &&
					sameKeys(typeKeys(ft.Results, theirs, p), results) {
					return true
				}
			}
		}
	}
	return false
}

// sameKeys says whether a and b spell the same types in the same order.
func sameKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// typeKeys is the type of each parameter in fl, spelled by typeKey.
func typeKeys(fl *ast.FieldList, imports map[string]Import, own *Pkg) []string {
	var out []string
	for _, e := range params(fl) {
		out = append(out, typeKey(e, imports, own))
	}
	return out
}

// typeKey is a type with each package it names spelled by import path: a name a file imports under is its import's
// path, and a bare name own declares (when own is the package whose file e is in) is own's path. Two spellings of one
// type in different files read the same; a bare name own does not declare (a predeclared type, a type parameter, a
// test's own type) stays as written.
func typeKey(e ast.Expr, imports map[string]Import, own *Pkg) string {
	switch x := e.(type) {
	case *ast.Ident:
		if own != nil && own.decls.types[x.Name] != nil {
			return own.Path + "." + x.Name
		}
		return x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			if imp, ok := imports[id.Name]; ok {
				return imp.Path + "." + x.Sel.Name
			}
		}
	case *ast.ParenExpr:
		return typeKey(x.X, imports, own)
	case *ast.StarExpr:
		return "*" + typeKey(x.X, imports, own)
	case *ast.ArrayType:
		return "[" + exprString(x.Len) + "]" + typeKey(x.Elt, imports, own)
	case *ast.Ellipsis:
		return "..." + typeKey(x.Elt, imports, own)
	case *ast.MapType:
		return "map[" + typeKey(x.Key, imports, own) + "]" + typeKey(x.Value, imports, own)
	case *ast.IndexExpr:
		return typeKey(x.X, imports, own) + "[" + typeKey(x.Index, imports, own) + "]"
	case *ast.IndexListExpr:
		args := make([]string, len(x.Indices))
		for i, a := range x.Indices {
			args[i] = typeKey(a, imports, own)
		}
		return typeKey(x.X, imports, own) + "[" + strings.Join(args, ", ") + "]"
	}
	return exprString(e)
}

// declaresMethod says whether p declares an interface with a method of that name: a visitor a generic type's Visit
// method of the name can implement.
func declaresMethod(p *Pkg, name string) bool {
	if p.decls == nil {
		return false
	}
	for _, t := range p.decls.order {
		if t.iface == nil {
			continue
		}
		for _, m := range t.iface.Methods.List {
			for _, n := range m.Names {
				if n.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// better says whether rank a comes before rank b: true before false, the first entry first.
func better(a, b [2]bool) bool {
	if a[0] != b[0] {
		return a[0]
	}
	return a[1] && !b[1]
}

// pkgRefs is the domain packages any file of the package imports (refs of each), by path.
func (c *outsideChecks) pkgRefs() map[string]ref {
	out := map[string]ref{}
	for _, f := range c.p.Files {
		for _, r := range c.refs(f) {
			out[r.imp.Path] = r
		}
	}
	return out
}

// genericReceiver says whether a method's receiver is a generic type's (peek[T], *pair[K, V]).
func genericReceiver(fd *ast.FuncDecl) bool {
	e := fd.Recv.List[0].Type
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr, *ast.IndexListExpr:
			return true
		default:
			return false
		}
	}
}

// domainType is the domain package and type a type expression names (core.T, core.T[X], (core.T)), when it names a
// type a core, shared kernel or the kit declares.
func (c *outsideChecks) domainType(refs map[string]ref, e ast.Expr) (ref, string, bool) {
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
			continue
		case *ast.IndexExpr:
			e = x.X
			continue
		case *ast.IndexListExpr:
			e = x.X
			continue
		case *ast.SelectorExpr:
			id, ok := x.X.(*ast.Ident)
			if !ok {
				return ref{}, "", false
			}
			r, ok := refs[id.Name]
			if !ok || r.pkg.decls == nil || r.pkg.decls.types[x.Sel.Name] == nil {
				return ref{}, "", false
			}
			return r, x.Sel.Name, true
		}
		return ref{}, "", false
	}
}

// elementType is a slice, array or map literal type's element type, nil for another type.
func elementType(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.ArrayType:
		return x.Elt
	case *ast.MapType:
		return x.Value
	}
	return nil
}

// logArgument says whether call is an argument of parent, a call to a logging function.
func logArgument(parent ast.Node, call *ast.CallExpr) bool {
	p, ok := parent.(*ast.CallExpr)
	if !ok {
		return false
	}
	isArg := false
	for _, a := range p.Args {
		if a == ast.Expr(call) {
			isArg = true
		}
	}
	if !isArg {
		return false
	}
	name := ""
	switch fn := p.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	}
	for _, s := range []string{"logf", "Logf", "Printf", "Info", "Warn", "Error", "Debug"} {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func (c *outsideChecks) at(f *File, n ast.Node, id, key, what string) { c.add(f.pos(n), id, key, what) }

// pathNoCore is path-no-core: the hub, hubedge, db, prompts and cmd/persona-hub import no core, kit or adapter.
func (c *outsideChecks) pathNoCore() {
	inPart := false
	for _, part := range noCore {
		if under(c.p.Rel, part) {
			inPart = true
		}
	}
	if !inPart {
		return
	}
	for _, f := range c.p.Files {
		for _, imp := range f.Imports {
			l := c.idx.layer(imp.Path)
			if !c.idx.domain(imp.Path) && l != layerAdapter {
				continue
			}
			from := display(c.p)
			if f.Test {
				from += " (test)"
			}
			rel := c.idx.rel(imp.Path)
			c.edge(imp.Pos, "path-no-core", rel, from+" imports "+rel, from+" imports "+rel)
		}
	}
}

// adapterImports is adapter-no-application and adapter-import on an adapter's non-test imports.
func (c *outsideChecks) adapterImports() {
	for _, f := range c.p.Files {
		if f.Test {
			continue
		}
		for _, imp := range f.Imports {
			dep := c.idx.pkg(imp.Path)
			if dep == nil || dep == c.p {
				continue
			}
			switch l := c.idx.layer(imp.Path); l {
			case layerApp, layerSeam, undeclared:
				c.add(imp.Pos, "adapter-no-application", c.p.Rel, "adapter "+display(c.p)+" imports application "+dep.Rel)
			case layerAdapter:
				// Allowed only for a shared kernel's adapter: one under a core shared package.
				owner := c.idx.pkg(dep.Path[:strings.LastIndex(dep.Path, "/")])
				if owner == nil || owner.Layer != layerShared {
					c.add(imp.Pos, "adapter-import", c.p.Rel, "adapter "+display(c.p)+" imports "+dep.Rel)
				}
			}
		}
	}
}

// adapterDecls is adapter-name and adapter-exported-result on an adapter's declarations: what its exported functions
// and methods hand out in their results and through their parameters, and its exported structs' exported fields.
func (c *outsideChecks) adapterDecls() {
	d := c.p.decls
	coreTypes := map[string]string{} // exported type name -> the core declaring it
	for _, f := range c.p.Files {
		if f.Test {
			continue
		}
		for _, r := range c.refs(f) {
			if r.pkg.decls == nil || !core(r.pkg.Layer) {
				continue
			}
			for name := range r.pkg.decls.types {
				if exported(name) {
					coreTypes[name] = r.name
				}
			}
		}
	}
	for _, t := range d.order {
		if core, ok := coreTypes[t.name]; ok && exported(t.name) {
			c.at(t.file, t.spec.Name, "adapter-name", c.p.Rel, c.p.Name+"."+t.name+" shadows "+core+"."+t.name)
		}
		c.exportedFields(t)
		c.exportedType(t)
	}
	for _, v := range d.values {
		switch {
		case !exported(v.name.Name):
		case v.tok == token.VAR:
			c.at(v.file, v.name, "adapter-exported-result", c.p.Rel, c.p.Name+"."+v.name.Name+" is an exported variable")
		case v.tok == token.CONST:
			c.at(v.file, v.name, "adapter-exported-result", c.p.Rel, c.p.Name+"."+v.name.Name+" is an exported constant")
		}
	}
	check := func(fd *funcDecl) {
		if !exported(fd.decl.Name.Name) || fd.recv != "" && !exported(fd.recv) {
			return
		}
		say := func(what string) { c.at(fd.file, fd.decl.Name, "adapter-exported-result", c.p.Rel, what) }
		for _, r := range params(fd.decl.Type.Results) {
			if ok, why := c.adapterResult(fd, r); !ok {
				say(fd.name() + " returns " + exprString(r) + why)
			}
		}
		tps := typeParams(fd.decl)
		for _, p := range params(fd.decl.Type.Params) {
			// One finding per parameter, the first way out it gives: an interface's methods may give many.
			for _, out := range c.handedBack(fd.file, p, tps) {
				through := exprString(p)
				switch {
				case strings.HasPrefix(out.via, "()"):
					through += out.via // what a func type returns: "assistantwire.Opener().DocTitle"
				case out.via != "":
					through += " (" + out.via + ")"
				}
				ok, why := false, ""
				if out.e != nil {
					ok, why = c.handsOutOnly(out)
				}
				switch {
				case ok:
					continue
				case out.e == nil:
					say(fd.name() + " can hand out any value through " + through)
				case out.e == p:
					say(fd.name() + " can hand back what it writes into " + through + ", a type of another package" + why)
				default:
					say(fd.name() + " hands out " + exprString(out.e) + " through " + through + why)
				}
				break
			}
		}
	}
	for _, fd := range d.funcs {
		check(fd)
	}
	for _, t := range d.order {
		for _, fd := range d.methods[t.name] {
			check(fd)
		}
	}
}

// exportedFields is adapter-exported-result on an adapter's exported struct other than its Record: an exported field
// hands what it holds to whoever has the struct, as a getter would.
func (c *outsideChecks) exportedFields(t *typeDecl) {
	if !exported(t.name) || t.name == "Record" || t.strct == nil {
		return
	}
	for _, fl := range t.strct.Fields.List {
		if len(fl.Names) == 0 {
			if _, name := baseType(fl.Type); exported(name) {
				c.at(t.file, fl, "adapter-exported-result", c.p.Rel, c.p.Name+"."+t.name+"."+name+" is an exported field")
			}
		}
		for _, n := range fl.Names {
			if exported(n.Name) {
				c.at(t.file, n, "adapter-exported-result", c.p.Rel, c.p.Name+"."+t.name+"."+n.Name+" is an exported field")
			}
		}
	}
}

// exportedType is adapter-exported-result on an adapter's exported type that is neither a struct nor an interface: an
// alias, or a named slice, map, array, func or chan type, hands application code a raw shape under the adapter's
// name. Its named basic types are app-named-primitive's (Ratchets).
func (c *outsideChecks) exportedType(t *typeDecl) {
	if !exported(t.name) {
		return
	}
	what := ""
	switch t.spec.Type.(type) {
	case *ast.ArrayType, *ast.MapType, *ast.FuncType, *ast.ChanType:
		what = " is a named " + exprString(t.spec.Type)
	}
	if t.spec.Assign != token.NoPos {
		what = " is an exported alias"
	}
	if what != "" {
		c.at(t.file, t.spec.Name, "adapter-exported-result", c.p.Rel, c.p.Name+"."+t.name+what)
	}
}

// sinks are the interfaces an adapter writes an answer to, by import path and name: what goes into one is not read by
// the rule and is a review's to check. They and another module's contracts are the only types of another package an
// adapter takes that the rule does not read as a way back to its caller (docs/server/architecture.md "Data objects and
// adapters" lists what it does not read).
var sinks = map[string]bool{"io.Writer": true, "net/http.ResponseWriter": true}

// A typeIn is a type expression and the file it is written in, whose imports and own package name what it names. via
// is the method or field of a contract's, db's or prompts' type a value of it goes through, for messages.
type typeIn struct {
	e   ast.Expr
	f   *File
	via string
}

// dataPkg says whether the package at path holds plain data an adapter may hand out: a contract (this module's or
// another's, as contracts.txt lists them), db or prompts.
func (c *outsideChecks) dataPkg(path string) bool {
	if c.idx.layer(path) == layerContract {
		return true
	}
	rel := c.idx.rel(path)
	return under(rel, "internal/db") || under(rel, "internal/prompts")
}

// declared is the declaration of the type a type expression in f names in a package of the tree, with its import path
// and name, and whether the rules can read that package (Index.typeNamed). An unqualified name is f's own package's;
// a predeclared one is none.
func (c *outsideChecks) declared(t typeIn) (td *typeDecl, path, name string, read bool) {
	pkg, name := baseType(t.e)
	switch {
	case name == "":
		return nil, "", "", true
	case pkg == "":
		if basic[name] {
			return nil, "", name, true
		}
		path = t.f.pkg.Path
	default:
		imp, ok := c.idx.imports(t.f)[pkg]
		if !ok {
			return nil, "", name, true
		}
		path = imp.Path
	}
	td, read, err := c.idx.typeNamed(path, name)
	if err != nil && c.err == nil {
		c.err = err
	}
	return td, path, name, read
}

// handedBack is what an adapter can hand its caller through a parameter of type p of f: what a pointer, slice, map
// or channel it can send on holds, written into it (a map's keys included); what a callback or a method of an
// interface it declares takes, and what the adapter can write into through what they return. A type the
// adapter declares counts as the type it is declared as. A contract's, db's or prompts' type is read from its
// declaration (calls): its data is what an adapter may hand out anyway, so what goes back is what its methods and
// funcs take. Another package's type is itself what goes back: a named map or slice (http.Header, url.Values), a
// struct with a map or pointer inside, or an interface whose methods take what the adapter passes them all reach the
// caller, and the rule does not read their declarations. So it passes only as a type an adapter may hand out (a core,
// shared kernel or kit type) or as one of the sinks. An entry with no type stands for any value at all: a parameter of
// type any, interface{} or a type parameter (tps) can carry a pointer to anything, which the adapter can assert and
// write through. A value of a basic type, error or a struct the adapter declares hands nothing back.
func (c *outsideChecks) handedBack(f *File, p ast.Expr, tps map[string]bool) []typeIn {
	seen := map[string]bool{}
	in := func(e ast.Expr) typeIn { return typeIn{e: e, f: f} }
	var back func(p ast.Expr) []typeIn
	called := func(ft *ast.FuncType) []typeIn {
		var out []typeIn
		for _, a := range params(ft.Params) {
			out = append(out, in(a))
		}
		for _, r := range params(ft.Results) {
			out = append(out, back(r)...) // what the caller's code returns, the adapter can write into
		}
		return out
	}
	back = func(p ast.Expr) []typeIn {
		switch x := p.(type) {
		case *ast.ParenExpr:
			return back(x.X)
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
			pkg, name := baseType(x)
			if pkg == "" {
				if t := c.p.decls.types[name]; t != nil && t.strct == nil && !seen[name] {
					seen[name] = true
					return back(t.spec.Type) // a generic type the adapter declares
				}
				return nil
			}
			imp, ok := c.idx.imports(f)[pkg]
			if ok && sinks[imp.Path+"."+name] {
				return nil
			}
			if ok && c.dataPkg(imp.Path) {
				return c.calls(in(x), map[string]bool{})
			}
			return []typeIn{in(x)}
		case *ast.StarExpr:
			return []typeIn{in(x.X)}
		case *ast.ArrayType:
			if x.Len == nil {
				return []typeIn{in(x.Elt)}
			}
			return back(x.Elt) // an array is copied, but not what its pointers reach
		case *ast.Ellipsis:
			return []typeIn{in(x.Elt)}
		case *ast.MapType:
			return []typeIn{in(x.Key), in(x.Value)}
		case *ast.ChanType:
			if x.Dir != ast.RECV {
				return []typeIn{in(x.Value)}
			}
		case *ast.FuncType:
			return called(x)
		case *ast.InterfaceType:
			if len(x.Methods.List) == 0 {
				return []typeIn{{}}
			}
			var out []typeIn
			for _, m := range x.Methods.List {
				if ft, ok := m.Type.(*ast.FuncType); ok {
					out = append(out, called(ft)...)
				} else {
					out = append(out, back(m.Type)...) // an embedded interface: any, the adapter's own or another package's
				}
			}
			return out
		case *ast.Ident:
			if x.Name == "any" || tps[x.Name] {
				return []typeIn{{}}
			}
			if t := c.p.decls.types[x.Name]; t != nil && t.strct == nil && !seen[x.Name] {
				seen[x.Name] = true
				return back(t.spec.Type)
			}
		}
		return nil
	}
	return back(p)
}

// calls is what an adapter can hand its caller's code by calling what a value of a contract's, db's or prompts' type
// t holds, read from the type's declaration: the parameters of an interface's exported methods (an embedded
// interface's included) and of a func type, and what can be called on what they return, through the type's exported
// and embedded fields, the elements of its pointers, slices, maps and channels, and the types of the same packages it
// holds. Its data, written into, is plain data like any the adapter returns. An interface the rule cannot read,
// embedded or one of knownInterfaces, is itself what goes back. Another module's contract, which the rule does not
// read, gives nothing.
func (c *outsideChecks) calls(t typeIn, seen map[string]bool) []typeIn {
	in := func(e ast.Expr) typeIn { return typeIn{e: e, f: t.f, via: t.via} }
	// called is what a method or func of the type takes, and what the adapter can call on what it returns: the
	// caller's code wrote both (connectorwire.Items' ItemSource returns a kbwire.ItemSource, whose DocTitle takes text).
	called := func(ft *ast.FuncType, via string) []typeIn {
		var out []typeIn
		for _, a := range params(ft.Params) {
			out = append(out, typeIn{e: a, f: t.f, via: via})
		}
		for _, r := range params(ft.Results) {
			out = append(out, c.calls(typeIn{e: r, f: t.f, via: via + "()"}, seen)...)
		}
		return out
	}
	switch x := t.e.(type) {
	case *ast.ParenExpr:
		return c.calls(in(x.X), seen)
	case *ast.StarExpr:
		return c.calls(in(x.X), seen)
	case *ast.ArrayType:
		return c.calls(in(x.Elt), seen)
	case *ast.Ellipsis:
		return c.calls(in(x.Elt), seen)
	case *ast.MapType:
		return append(c.calls(in(x.Key), seen), c.calls(in(x.Value), seen)...)
	case *ast.ChanType:
		return c.calls(in(x.Value), seen)
	case *ast.FuncType:
		return called(x, t.via)
	case *ast.InterfaceType:
		var out []typeIn
		for _, m := range x.Methods.List {
			if ft, ok := m.Type.(*ast.FuncType); ok {
				if !exported(m.Names[0].Name) {
					continue // only the type's own package can call it
				}
				via := m.Names[0].Name
				if t.via != "" {
					via = t.via + "." + via // a field's or a result's method
				}
				out = append(out, called(ft, via)...)
			} else {
				out = append(out, c.callsOf(in(m.Type), seen, true)...)
			}
		}
		return out
	case *ast.StructType:
		var out []typeIn
		for _, fl := range x.Fields.List {
			for _, n := range fieldNames(fl) {
				if exported(n) {
					out = append(out, c.calls(typeIn{e: fl.Type, f: t.f, via: n}, seen)...)
				}
			}
		}
		return out
	}
	return c.callsOf(t, seen, false)
}

// callsOf is calls on a named type: its declaration's when the rules read the package it is in and it is a
// contract's, db's or prompts' type; itself when it is an interface the rule cannot read (embedded in an interface,
// or one of knownInterfaces); nothing otherwise.
func (c *outsideChecks) callsOf(t typeIn, seen map[string]bool, embedded bool) []typeIn {
	td, path, name, read := c.declared(t)
	if name == "" || basic[name] {
		return nil
	}
	switch {
	case !read && c.dataPkg(path):
		return nil // another module's contract: not read
	case td == nil && c.dataPkg(path):
		return []typeIn{t} // a type the tree does not declare: itself, which handsOutOnly refuses
	case td != nil && c.dataPkg(path):
		if seen[path+"."+name] {
			return nil
		}
		seen[path+"."+name] = true
		return c.calls(typeIn{e: td.spec.Type, f: td.file, via: t.via}, seen)
	case embedded || c.knownInterface(t):
		return []typeIn{t}
	}
	return nil
}

// knownInterface says whether a type expression names one of knownInterfaces, the standard library's interfaces.
func (c *outsideChecks) knownInterface(t typeIn) bool {
	pkg, name := baseType(t.e)
	return pkg != "" && knownInterfaces[pkg+"."+name]
}

// fieldNames is a struct field's names, an embedded field's being its type's.
func fieldNames(fl *ast.Field) []string {
	if len(fl.Names) == 0 {
		_, name := baseType(fl.Type)
		return []string{name}
	}
	var out []string
	for _, n := range fl.Names {
		out = append(out, n.Name)
	}
	return out
}

// adapterResult says whether an adapter may return a type (handsOutOnly), or Log's string, which goes straight into a
// log call; when it may not, why says which method or func of a contract's type refuses it, if one does.
func (c *outsideChecks) adapterResult(fd *funcDecl, r ast.Expr) (ok bool, why string) {
	if exprString(r) == "string" {
		return fd.recv == "" && fd.decl.Name.Name == "Log" && len(params(fd.decl.Type.Results)) == 1, ""
	}
	return c.handsOutOnly(typeIn{e: r, f: fd.file})
}

// handsOutOnly says whether every type a value of type t holds (handedParts) is one an adapter may hand out: an error,
// or a type handedOut allows. A map hands out its keys as well as its values. When one is not, why names the method
// or func of a contract's, db's or prompts' type that refuses it, if one does.
func (c *outsideChecks) handsOutOnly(t typeIn) (bool, string) {
	for _, part := range handedParts(t.e) {
		if exprString(part) == "error" {
			continue
		}
		if ok, why := c.handedOutType(typeIn{e: part, f: t.f}, map[string]bool{}); !ok {
			return false, why
		}
	}
	return true, ""
}

// handedParts is the types a value of type t gives whoever holds it: t itself, or what a pointer, slice, array, map
// (its keys and its values) or channel holds, all the way down.
func handedParts(t ast.Expr) []ast.Expr {
	switch x := t.(type) {
	case *ast.ParenExpr:
		return handedParts(x.X)
	case *ast.StarExpr:
		return handedParts(x.X)
	case *ast.ArrayType:
		return handedParts(x.Elt)
	case *ast.Ellipsis:
		return handedParts(x.Elt)
	case *ast.MapType:
		return append(handedParts(x.Key), handedParts(x.Value)...)
	case *ast.ChanType:
		return handedParts(x.Value)
	}
	return []ast.Expr{t}
}

// handedOut says whether an adapter may hand a type out, as a kit type's argument or to what a contract's type
// calls: every part of it (handedParts) is one handedOutType allows, an error not included.
func (c *outsideChecks) handedOut(t typeIn, seen map[string]bool) (bool, string) {
	for _, part := range handedParts(t.e) {
		if ok, why := c.handedOutType(typeIn{e: part, f: t.f}, seen); !ok {
			return false, why
		}
	}
	return true, ""
}

// handedOutType is whether an adapter may hand out one type that holds no other (handedParts): its own Record, a
// type of a core or shared kernel, a kit type whose every type argument is one too, all the way down (so
// outcome.Of[agentref.Ref, agentref.Fail] and opt.Option[[]fleetcount.Agent] pass, and outcome.Of[string, fail.Fail],
// opt.Option[[]string] and opt.Option[map[string]fleetcount.Agent] are raw values one layer out), or a contract's,
// db's or prompts' type that dataSafe allows. In a file of a contract, db or prompts, an unqualified name is that
// package's type.
func (c *outsideChecks) handedOutType(t typeIn, seen map[string]bool) (bool, string) {
	if t.f.pkg == c.p && exprString(t.e) == "Record" {
		return true, ""
	}
	pkg, name := baseType(t.e)
	if name == "" || pkg == "" && (t.f.pkg == c.p || basic[name]) {
		return false, ""
	}
	path := t.f.pkg.Path
	if pkg != "" {
		imp, ok := c.idx.imports(t.f)[pkg]
		if !ok {
			return false, ""
		}
		path = imp.Path
	}
	switch c.idx.layer(path) {
	case layerKit:
		for _, a := range typeArgs(t.e) {
			if ok, why := c.handedOut(typeIn{e: a, f: t.f}, seen); !ok {
				return false, why
			}
		}
		return true, ""
	case layerCore, layerShared:
		return true, ""
	}
	if c.dataPkg(path) {
		return c.dataSafe(typeIn{e: t.e, f: t.f}, seen)
	}
	return false, ""
}

// dataSafe says whether an adapter may hand out a contract's, db's or prompts' type, read from its declaration: what
// a value of it can be called with, an interface's methods and a func type, through its exported and embedded fields
// and the types of the same packages it holds (callsSafe), takes and returns only what an adapter may hand out. Its
// data is plain data; its methods, a type's own code, are not the adapter's. Another module's contract, which the
// rule does not read, passes; a type the tree does not declare does not. why names the method or func that refuses
// it.
func (c *outsideChecks) dataSafe(t typeIn, seen map[string]bool) (bool, string) {
	td, path, name, read := c.declared(t)
	switch {
	case !read:
		return true, ""
	case td == nil:
		return false, ", which the server's tree does not declare"
	case seen[path+"."+name]:
		return true, ""
	}
	seen[path+"."+name] = true
	return c.callsSafe(typeIn{e: td.spec.Type, f: td.file}, "", seen)
}

// callsSafe is dataSafe on a type expression in a contract's, db's or prompts' file; label names it in messages (the
// field or method it is, "" for the type itself).
func (c *outsideChecks) callsSafe(t typeIn, label string, seen map[string]bool) (bool, string) {
	in := func(e ast.Expr) typeIn { return typeIn{e: e, f: t.f} }
	switch x := t.e.(type) {
	case *ast.ParenExpr:
		return c.callsSafe(in(x.X), label, seen)
	case *ast.StarExpr:
		return c.callsSafe(in(x.X), label, seen)
	case *ast.ArrayType:
		return c.callsSafe(in(x.Elt), label, seen)
	case *ast.Ellipsis:
		return c.callsSafe(in(x.Elt), label, seen)
	case *ast.MapType:
		if ok, why := c.callsSafe(in(x.Key), label, seen); !ok {
			return false, why
		}
		return c.callsSafe(in(x.Value), label, seen)
	case *ast.ChanType:
		return c.callsSafe(in(x.Value), label, seen)
	case *ast.FuncType:
		return c.takesAndReturns(x, t.f, label, seen)
	case *ast.InterfaceType:
		for _, m := range x.Methods.List {
			if ft, ok := m.Type.(*ast.FuncType); ok {
				if !exported(m.Names[0].Name) {
					continue // only the type's own package can call it
				}
				if ok, why := c.takesAndReturns(ft, t.f, m.Names[0].Name, seen); !ok {
					return false, why
				}
			} else if ok, why := c.namedSafe(in(m.Type), seen, true); !ok {
				return false, why
			}
		}
		return true, ""
	case *ast.StructType:
		for _, fl := range x.Fields.List {
			for _, n := range fieldNames(fl) {
				if exported(n) {
					if ok, why := c.callsSafe(in(fl.Type), n, seen); !ok {
						return false, why
					}
				}
			}
		}
		return true, ""
	}
	return c.namedSafe(t, seen, false)
}

// namedSafe is callsSafe on a named type: dataSafe for a contract's, db's or prompts' type, refused for an interface
// the rule cannot read (embedded in an interface, or one of knownInterfaces), and passed for other types, which are
// data.
func (c *outsideChecks) namedSafe(t typeIn, seen map[string]bool, embedded bool) (bool, string) {
	pkg, name := baseType(t.e)
	switch {
	case name == "" || pkg == "" && basic[name]:
		return true, ""
	case pkg == "":
		return c.dataSafe(t, seen)
	}
	if imp, ok := c.idx.imports(t.f)[pkg]; ok && c.dataPkg(imp.Path) {
		return c.dataSafe(t, seen)
	}
	if embedded || c.knownInterface(t) {
		return false, ", which holds " + exprString(t.e) + ", an interface the rule does not read"
	}
	return true, ""
}

// takesAndReturns says whether a method or func of a contract's, db's or prompts' type, which the caller can call on
// what the adapter hands it, takes and returns only what an adapter may hand out (handedOut, an error included).
func (c *outsideChecks) takesAndReturns(ft *ast.FuncType, f *File, name string, seen map[string]bool) (bool, string) {
	for _, side := range []struct {
		verb string
		list *ast.FieldList
	}{{"takes", ft.Params}, {"returns", ft.Results}} {
		for _, p := range params(side.list) {
			for _, part := range handedParts(p) {
				if exprString(part) == "error" {
					continue
				}
				if ok, why := c.handedOutType(typeIn{e: part, f: f}, seen); !ok {
					if why == "" {
						why = ", whose " + name + " " + side.verb + " " + exprString(p)
						if name == "" {
							why = ", which " + side.verb + " " + exprString(p)
						}
					}
					return false, why
				}
			}
		}
	}
	return true, ""
}
