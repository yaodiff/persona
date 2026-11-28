package domainrules

import (
	"go/ast"
	"go/token"
	"strings"
)

// kitAPI is where the kit's listed API needs a core rule left alone, keyed by package ("<dir>"), function
// ("<dir>.<Func>"), method or field ("<dir>.<Type>.<Name>"), with the rules each may break. Every other core rule
// runs over the kit as it runs over a core: the kit is the vocabulary cores are built from, so it takes raw input,
// callbacks and type parameters only where its documented API needs them (docs/server/architecture.md "Domain
// model", "The domain kit"). Nothing else in the kit is exempt; a new kit type or function is held to every rule
// until its own line here says why not.
var kitAPI = map[string][]string{
	// defect: a programmer error stops the operation, so Raise and Catch are the kit's one panic and one recover;
	// Raise takes the text of its message, Catch the site's report, and a Defect is what a crash prints.
	"internal/domainkit/defect.Raise":        {"core-primitive-signature", "core-effect", "core-constructor"},
	"internal/domainkit/defect.Catch":        {"core-interface-param", "core-effect", "core-constructor"},
	"internal/domainkit/defect.Defect.Error": {"core-accessor", "core-primitive-signature", "core-codec-method"},
	"internal/domainkit/defect.Defect.op":    {"core-primitive-field"},
	"internal/domainkit/defect.Defect.cause": {"core-primitive-field"},
	// outcome: generic over every core's failure family, so it declares none. Ok and Err have pointer receivers so
	// that Of{}.Ok(v) does not compile and producers set their named result. Err's any(f) == nil makes a nil failure
	// a defect. Its readers are combinators, neither constructors nor domain services, and take callbacks; Must's op
	// names the caller in the defect.
	"internal/domainkit/outcome":        {"core-fail-family"},
	"internal/domainkit/outcome.Of.Ok":  {"core-pointer-receiver"},
	"internal/domainkit/outcome.Of.Err": {"core-pointer-receiver", "core-any"},
	"internal/domainkit/outcome.Unpack": {"core-constructor"},
	"internal/domainkit/outcome.Fold":   {"core-interface-param", "core-constructor"},
	"internal/domainkit/outcome.Widen":  {"core-constructor"},
	"internal/domainkit/outcome.Then":   {"core-interface-param", "core-constructor"},
	"internal/domainkit/outcome.Map":    {"core-interface-param", "core-constructor"},
	"internal/domainkit/outcome.Must":   {"core-primitive-signature", "core-constructor"},
	// opt: absence for a value of any type, so a presence flag beside it, made by Some and None and read through
	// callbacks.
	"internal/domainkit/opt.Some":         {"core-constructor"},
	"internal/domainkit/opt.None":         {"core-constructor"},
	"internal/domainkit/opt.Fold":         {"core-interface-param", "core-constructor"},
	"internal/domainkit/opt.Option.Match": {"core-interface-param"},
	"internal/domainkit/opt.Option.some":  {"core-primitive-field"},
	// The raw-input functions, where the standard library's values enter the kit (ParseMinutes and ParseSeconds are
	// Parse functions, which the rules allow anyway), and Now.Stamp, which hands a core the instant the clock was
	// read at.
	"internal/domainkit/fail.NewCode":      {"core-primitive-signature"},
	"internal/domainkit/fail.NewPart":      {"core-primitive-signature"},
	"internal/domainkit/slot.NewSlot":      {"core-primitive-signature"},
	"internal/domainkit/instant.At":        {"core-constructor"},
	"internal/domainkit/instant.ReadClock": {"core-constructor"},
	"internal/domainkit/instant.Now.Stamp": {"core-accessor"},
}

// kitImports is what a kit package may import besides the computing standard library and the kit: instant's At and
// ReadClock take the time.Time an adapter or the application's clock has. Nothing else in the kit imports more.
var kitImports = map[string][]string{
	"internal/domainkit/instant": {"time"},
}

// coreChecks is one pass of the core rules over a package.
type coreChecks struct {
	findings
	p   *Pkg
	idx Index
	kit bool // the kit: every core rule, with what kitAPI lists left out

	ownMethods methodNames // this package's and the kit's method names, once outcomes needs them
}

// Core is the rules for p's own code, by its layer: the core rules for cores, shared kernels and the kit (with what
// kitAPI lists left out), the seam rules for application seams, and the package comment and the hook for
// every domain package. p must be parsed in full (Full).
func Core(p *Pkg, idx Index) []Finding {
	c := &coreChecks{p: p, idx: idx, kit: p.Layer == layerKit}
	switch p.Layer {
	case layerCore, layerShared, layerKit:
		c.docAndHook()
		c.coreRules()
	case layerAdapter:
		c.docAndHook()
	case layerSeam:
		c.hook()
		c.seamRules()
	}
	return c.list
}

// exempt says whether the kit's API lets name (a function, "<Type>.<Method>", "<Type>.<field>", or "" for the
// package) break rule.
func (c *coreChecks) exempt(name, rule string) bool {
	if !c.kit {
		return false
	}
	key := c.p.Rel
	if name != "" {
		key += "." + name
	}
	for _, r := range kitAPI[key] {
		if r == rule {
			return true
		}
	}
	return false
}

func (c *coreChecks) at(f *File, n ast.Node, id, what string) { c.add(f.pos(n), id, c.p.Rel, what) }

func (c *coreChecks) docAndHook() {
	documented := false
	for _, f := range c.p.Files {
		if !f.Test && strings.HasPrefix(f.Doc, "Package "+c.p.Name+" ") {
			documented = true
		}
	}
	if !documented {
		c.add(first(c.p), "core-doc", c.p.Rel, display(c.p)+" has no package comment")
	}
	c.hook()
}

// hook checks for rules_test.go calling domainrules.Check.
func (c *coreChecks) hook() {
	for _, f := range c.p.Files {
		if !strings.HasSuffix(f.Rel, "/rules_test.go") && f.Rel != "rules_test.go" {
			continue
		}
		name := ""
		for _, imp := range f.Imports {
			if imp.Path == c.idx.rules {
				name = c.idx.local(imp)
			}
		}
		called := false
		if f.ast != nil && name != "" {
			ast.Inspect(f.ast, func(n ast.Node) bool {
				if call, ok := n.(*ast.CallExpr); ok {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Check" {
						if id, ok := sel.X.(*ast.Ident); ok && id.Name == name {
							called = true
						}
					}
				}
				return !called
			})
		}
		if !called {
			c.add(f.pos(f.ast), "core-hook", c.p.Rel, display(c.p)+"'s rules_test.go does not call domainrules.Check")
		}
		return
	}
	c.add(first(c.p), "core-hook", c.p.Rel, display(c.p)+" has no rules_test.go")
}

// coreRules is the core-* rules for a core, a shared kernel or the kit.
func (c *coreChecks) coreRules() {
	d := c.p.decls
	for _, f := range c.p.Files {
		if f.Test {
			if !f.External {
				c.add(f.pos(f.ast.Name), "core-test-package", c.p.Rel, f.Rel+" is a white-box test")
			}
			continue
		}
		// A core's purity holds for the kit too, which every core imports: its listed API aside (kitAPI, kitImports).
		c.imports(f)
		c.effects(f)
		c.clockCalls(f)
		c.anyUses(f)
		c.outcomes(f)
		c.partNames(f)
	}
	for _, v := range d.values {
		switch {
		case v.tok == token.VAR:
			c.at(v.file, v.name, "core-package-var", v.name.Name+" is package-level state")
		case exported(v.name.Name):
			c.at(v.file, v.name, "core-exported-const", v.name.Name+" exports a primitive")
		}
	}
	shapes := map[string]*typeDecl{}
	for _, t := range d.order {
		c.typeRules(t, shapes)
	}
	for _, fd := range d.funcs {
		c.funcRules(fd)
	}
	for _, t := range d.order {
		for _, fd := range d.methods[t.name] {
			c.funcRules(fd)
		}
	}
	if !c.exempt("", "core-fail-family") {
		c.failFamily()
	}
}

// typeRules is the rules on one type declaration.
func (c *coreChecks) typeRules(t *typeDecl, shapes map[string]*typeDecl) {
	s, f := t.spec, t.file
	if s.TypeParams != nil && len(s.TypeParams.List) > 0 && !c.kitConstraints(s.TypeParams) {
		c.at(f, s.Name, "core-type-param", t.name+" declares a type parameter")
	}
	if t.strct != nil {
		c.structRules(t)
		if exported(t.name) {
			if name, ok := singleField(t.strct); ok {
				want := lowerCamel(t.name)
				alt := ""
				if strings.HasSuffix(t.name, "ID") && len(t.name) > 2 {
					alt = lowerCamel(strings.TrimSuffix(t.name, "ID")) // MachineID{machine}
				}
				if name != want && name != alt {
					c.at(f, s.Name, "core-field-name", t.name+"'s field is "+name+", not "+want)
				}
			}
			sh := shape(t.strct)
			if other, ok := shapes[sh]; ok {
				c.at(f, s.Name, "core-distinct-shape", other.name+" and "+t.name+" share "+sh)
			} else {
				shapes[sh] = t
			}
			for _, suffix := range []string{"Service", "Manager", "Repository", "Store", "Port", "Client", "Gateway", "Handler"} {
				if strings.HasSuffix(t.name, suffix) {
					c.at(f, s.Name, "core-service-name", t.name+" is a service object")
					break
				}
			}
		}
	}
	if !exported(t.name) {
		return
	}
	switch {
	case s.Assign != token.NoPos:
		c.at(f, s.Name, "core-alias", t.name+" is an alias")
	case t.strct == nil && t.iface == nil:
		c.at(f, s.Name, "core-bare-type", t.name+" is a named "+exprString(s.Type))
	}
	if (t.strct != nil || (t.iface != nil && t.sealed)) && !strings.Contains(t.doc, "The zero "+t.name+" is") {
		c.at(f, s.Name, "core-zero-doc", t.name+" does not say what its zero value is")
	}
	if t.iface != nil {
		for _, m := range t.iface.Methods.List {
			for _, n := range m.Names {
				if strings.HasSuffix(t.name, "Visitor") && !strings.HasPrefix(n.Name, "Visit") {
					c.at(f, n, "core-visitor-name", t.name+"."+n.Name+" does not start with Visit")
				}
				if ft, ok := m.Type.(*ast.FuncType); ok && exported(n.Name) {
					c.signature(f, n, t.name+"."+n.Name, "", ft, nil)
				}
			}
		}
	}
}

// kitConstraints says whether a kit type parameter list uses only the constraints the kit's API needs: any and
// fail.Fail.
func (c *coreChecks) kitConstraints(tp *ast.FieldList) bool {
	if !c.kit {
		return false
	}
	for _, f := range tp.List {
		switch exprString(f.Type) {
		case "any", "fail.Fail":
		case "Fail":
			if c.p.Rel != "internal/domainkit/fail" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// structRules is the field rules on one struct type.
func (c *coreChecks) structRules(t *typeDecl) {
	f := t.file
	failName := ""
	for _, imp := range f.Imports {
		if imp.Path == c.idx.module+"/internal/domainkit/fail" {
			failName = c.idx.local(imp)
		}
	}
	tagged := false
	many := fieldCount(t.strct) > 1
	for _, fl := range t.strct.Fields.List {
		if fl.Tag != nil {
			tagged = true
		}
		if len(fl.Names) == 0 {
			pkg, name := baseType(fl.Type)
			base := pkg != "" && pkg == failName && (name == "Invalid" || name == "Refusal" || name == "Absence" ||
				name == "Conflict" || name == "Invalids")
			if !base {
				c.at(f, fl, "core-exported-field", t.name+"."+name+" is embedded")
			}
			continue
		}
		for _, n := range fl.Names {
			if exported(n.Name) {
				c.at(f, n, "core-exported-field", t.name+"."+n.Name+" is exported")
			}
			primitive := many && n.Name != "_" && c.p.decls.primitive(f, c.idx, fl.Type)
			if primitive && !c.exempt(t.name+"."+n.Name, "core-primitive-field") {
				c.at(f, n, "core-primitive-field", t.name+"."+n.Name+" is "+exprString(fl.Type))
			}
		}
	}
	if tagged {
		c.at(f, t.spec.Name, "core-tag", t.name+" has struct tags")
	}
}

// funcRules is the rules on one function or method.
func (c *coreChecks) funcRules(fd *funcDecl) {
	decl, f, name := fd.decl, fd.file, fd.name()
	if decl.Type.TypeParams != nil && len(decl.Type.TypeParams.List) > 0 && !c.kitConstraints(decl.Type.TypeParams) {
		c.at(f, decl.Name, "core-type-param", name+" declares a type parameter")
	}
	isExported := exported(decl.Name.Name)
	if fd.recv != "" {
		if fd.ptr && !c.exempt(name, "core-pointer-receiver") {
			c.at(f, decl.Name, "core-pointer-receiver", name+" has a pointer receiver")
		}
		if codecMethod(decl.Name.Name) && !c.exempt(name, "core-codec-method") {
			c.at(f, decl.Name, "core-codec-method", name+" is a codec")
		}
		if decl.Name.Name == "Wire" && !c.wireIsField(fd) {
			c.at(f, decl.Name, "core-wire-method", fd.recv+".Wire is not the field")
		}
		if isExported && !c.exempt(name, "core-accessor") && !c.accessorOK(fd) {
			why := ""
			switch {
			case decl.Name.Name == "ID":
				why = ": an ID returns the package's own <X>ID type or a shared kernel's"
			case decl.Name.Name == "Build" && strings.HasSuffix(fd.recv, "Builder"):
				why = ": a builder's Build returns an outcome of its package's type"
			}
			c.at(f, decl.Name, "core-accessor", name+"() hands state out"+why)
		} else if isExported && !c.exempt(name, "core-accessor") {
			c.fieldReturn(fd)
		}
		c.receiverWrites(fd)
	} else if isExported {
		if !c.exempt(name, "core-constructor") {
			c.constructor(fd)
		}
		if !c.exempt(name, "core-accessor") {
			c.fieldReturn(fd)
		}
	}
	if decl.Recv == nil && decl.Name.Name == "init" {
		c.at(f, decl.Name, "core-effect", f.Rel+":"+itoa(f.pos(decl).Line)+" uses init")
	}
	if isExported && (strings.HasPrefix(decl.Name.Name, "Evolve") || strings.HasPrefix(decl.Name.Name, "Apply")) &&
		decl.Type.Params != nil && len(decl.Type.Params.List) > 0 {
		c.at(f, decl.Name, "core-public-evolve", name+" applies events")
	}
	c.interfaceParams(fd)
	if isExported {
		c.signature(f, decl.Name, name, fd.recv, decl.Type, decl)
	}
	if decl.Body != nil {
		c.mapRanges(fd)
	}
}

func codecMethod(name string) bool {
	switch name {
	case "String", "Error", "GoString", "Format", "MarshalJSON", "UnmarshalJSON", "MarshalText", "UnmarshalText",
		"MarshalBinary", "UnmarshalBinary", "AppendText", "Scan", "Value", "LogValue":
		return true
	}
	return false
}

// signature is the rules on an exported function's or method's parameters and results: their number, primitives
// and collections. decl is nil for an interface method.
func (c *coreChecks) signature(f *File, n ast.Node, name, recv string, ft *ast.FuncType, decl *ast.FuncDecl) {
	ps, rs := params(ft.Params), params(ft.Results)
	if len(ps) > 4 {
		c.at(f, n, "core-params", name+" takes "+itoa(len(ps))+" parameters")
	}
	top := decl != nil && decl.Recv == nil
	parse := top && wordPrefix(decl.Name.Name, "Parse")
	build := top && (wordPrefix(decl.Name.Name, "New") || wordPrefix(decl.Name.Name, "Restore"))
	wire := recv != "" && strings.HasSuffix(name, ".Wire")
	for _, p := range ps {
		if c.p.decls.primitive(f, c.idx, p) && !parse && !c.exempt(name, "core-primitive-signature") && !c.funcParam(f, p, decl) {
			c.at(f, n, "core-primitive-signature", name+" takes "+exprString(p))
		}
		if c.p.decls.collection(f, c.idx, p) && !build {
			c.at(f, n, "core-collection", name+" takes "+exprString(p))
		}
	}
	for _, r := range rs {
		if c.p.decls.primitive(f, c.idx, r) && exprString(r) != "bool" && !wire &&
			!c.exempt(name, "core-primitive-signature") {
			c.at(f, n, "core-primitive-signature", name+" returns "+exprString(r))
		}
		if c.p.decls.collection(f, c.idx, r) {
			c.at(f, n, "core-collection", name+" returns "+exprString(r))
		}
	}
}

// funcParam says whether p is a func-typed parameter, which core-interface-param reports instead.
func (c *coreChecks) funcParam(f *File, p ast.Expr, decl *ast.FuncDecl) bool {
	if decl == nil {
		return false
	}
	t := c.p.decls.named(p)
	if _, ok := t.(*ast.FuncType); ok {
		return true
	}
	if base := genericBase(t); base != nil {
		t = base
	}
	if sel, ok := t.(*ast.SelectorExpr); ok {
		st := stdType(sel, f, c.idx)
		return st == "iter.Seq" || st == "iter.Seq2"
	}
	return false
}

// unwrapParam is a parameter's type without pointers, slices, arrays, variadics and map values.
func unwrapParam(e ast.Expr) ast.Expr {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.ArrayType:
			e = x.Elt
		case *ast.Ellipsis:
			e = x.Elt
		case *ast.MapType:
			e = x.Value
		default:
			return e
		}
	}
}

// knownInterfaces are interfaces of the standard library a parameter may name, which no core declares.
var knownInterfaces = map[string]bool{
	"context.Context": true, "fmt.Stringer": true, "fmt.State": true, "io.Reader": true, "io.Writer": true,
	"io.ReadCloser": true, "io.WriteCloser": true, "io.ReadWriter": true, "io.Closer": true, "sort.Interface": true,
	"iter.Seq": true, "iter.Seq2": true, "http.Handler": true, "json.Marshaler": true, "json.Unmarshaler": true,
}

// interfaceParams is core-interface-param: no func-typed or interface-typed parameter but the package's own visitors
// and sealed interfaces, on every function and method.
func (c *coreChecks) interfaceParams(fd *funcDecl) {
	decl, f, name := fd.decl, fd.file, fd.name()
	if c.exempt(name, "core-interface-param") {
		return
	}
	tps := typeParams(decl)
	imports := c.idx.imports(f)
	for _, p := range params(decl.Type.Params) {
		t := c.p.decls.named(p) // type fetch[T any] func(...) is a func parameter as func(...) is
		bad := false
		switch x := t.(type) {
		case *ast.FuncType, *ast.InterfaceType:
			bad = true
		case *ast.Ident:
			if x.Name == "error" || x.Name == "any" {
				bad = true
			} else if td := c.p.decls.types[x.Name]; td != nil && td.iface != nil && !tps[x.Name] {
				bad = !strings.HasSuffix(x.Name, "Visitor") && !td.sealed
			}
		case *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
			pkg, tn := baseType(t)
			if knownInterfaces[pkg+"."+tn] {
				bad = true
				break
			}
			if imp, ok := imports[pkg]; ok {
				if dp := c.idx.pkg(imp.Path); dp != nil && dp.decls != nil {
					if td := dp.decls.types[tn]; td != nil && td.iface != nil {
						bad = true
					}
				}
			}
		}
		if bad {
			c.at(f, decl.Name, "core-interface-param", name+" takes "+exprString(p))
		}
	}
}

// accessorOK says whether an exported method's results are what tell, don't ask allows: nothing, a bool, the
// receiver's type or an outcome of it, or one of ID, Wire, Build on a builder, a stage's next stage, Accept.
func (c *coreChecks) accessorOK(fd *funcDecl) bool {
	decl, recv := fd.decl, fd.recv
	rs := params(decl.Type.Results)
	name := decl.Name.Name
	switch {
	case name == "Wire":
		return true
	case name == "Accept":
		return len(rs) == 0
	case len(rs) == 0:
		return true
	case len(rs) > 1:
		return false
	}
	r := rs[0]
	pkg, base := baseType(r)
	switch {
	case exprString(r) == "bool":
		return true
	case pkg == "" && base == recv:
		return true
	case name == "ID":
		return c.identity(fd.file, r)
	case name == "Build" && strings.HasSuffix(recv, "Builder"):
		return c.outcomeOfOwn(fd.file, r)
	case strings.HasSuffix(recv, "Step") && pkg == "" && c.p.decls.types[base] != nil &&
		(strings.HasSuffix(base, "Step") || strings.HasSuffix(base, "Builder")):
		return true
	}
	if c.isOutcome(fd.file, r) {
		if args := typeArgs(r); len(args) > 0 {
			if p, b := baseType(args[0]); p == "" && b == recv {
				return true
			}
		}
	}
	return false
}

// fieldReturn is core-accessor on an exported function or method whose whole body returns state as it is: a field of
// a parameter or of the receiver (`return n.title`), whatever the function is named. ID and Wire are the exceptions;
// a constructor or a domain service that only hands a field out is a getter all the same.
func (c *coreChecks) fieldReturn(fd *funcDecl) {
	decl := fd.decl
	if n := decl.Name.Name; n == "ID" || n == "Wire" {
		return
	}
	results := soleReturn(decl)
	if len(results) == 0 {
		return
	}
	inputs := map[string]bool{}
	if r := receiverIdent(decl); r != "" {
		inputs[r] = true
	}
	for _, fl := range decl.Type.Params.List {
		for _, n := range fl.Names {
			inputs[n.Name] = true
		}
	}
	for _, r := range results {
		if !fieldOf(r, inputs) {
			return
		}
	}
	c.at(fd.file, decl.Name, "core-accessor", fd.name()+"() hands state out, returning "+exprString(results[0]))
}

// soleReturn gives the returned expressions when the body consists of one return.
func soleReturn(decl *ast.FuncDecl) []ast.Expr {
	if decl.Body != nil && len(decl.Body.List) == 1 {
		if ret, ok := decl.Body.List[0].(*ast.ReturnStmt); ok {
			return ret.Results
		}
	}
	return nil
}

// fieldOf says whether e is a field reached from one of inputs (n.title, b.noteBuilder.title), parentheses and
// dereferences aside.
func fieldOf(e ast.Expr, inputs map[string]bool) bool {
	sel := false
	for {
		switch x := e.(type) {
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		case *ast.SelectorExpr:
			e, sel = x.X, true
		case *ast.Ident:
			return sel && inputs[x.Name]
		default:
			return false
		}
	}
}

// identity says whether ID's result is an identity: the package's own type named <X>ID, or a shared kernel's type.
func (c *coreChecks) identity(f *File, e ast.Expr) bool {
	pkg, base := baseType(e)
	if pkg == "" {
		return c.p.decls.types[base] != nil && strings.HasSuffix(base, "ID") && len(base) > 2
	}
	imp, ok := c.idx.imports(f)[pkg]
	return ok && c.idx.layer(imp.Path) == layerShared
}

// own says whether a type expression names a type this package declares.
func (c *coreChecks) own(e ast.Expr) bool {
	pkg, base := baseType(e)
	return pkg == "" && c.p.decls.types[base] != nil
}

// outcomeOfOwn says whether a type expression is outcome.Of of a type this package declares: what Build returns.
func (c *coreChecks) outcomeOfOwn(f *File, e ast.Expr) bool {
	if !c.isOutcome(f, e) {
		return false
	}
	args := typeArgs(e)
	return len(args) > 0 && c.own(args[0])
}

// isOutcome says whether a type expression is the kit's outcome.Of.
func (c *coreChecks) isOutcome(f *File, e ast.Expr) bool {
	pkg, base := baseType(e)
	return base == "Of" && pkg != "" && pkg == c.idx.kitName(f, "outcome") || base == "Of" && pkg == "" && c.p.Rel == "internal/domainkit/outcome"
}

// coreType says whether a type expression names a type of this package, a shared kernel or the kit.
func (c *coreChecks) coreType(f *File, e ast.Expr) bool {
	pkg, base := baseType(e)
	if pkg == "" {
		return c.p.decls.types[base] != nil
	}
	imp, ok := c.idx.imports(f)[pkg]
	if !ok {
		return false
	}
	l := c.idx.layer(imp.Path)
	return l == layerShared || l == layerKit
}

// wireIsField says whether a Wire method is on a single-field struct and returns that field.
func (c *coreChecks) wireIsField(fd *funcDecl) bool {
	t := c.p.decls.types[fd.recv]
	if t == nil || t.strct == nil {
		return false
	}
	field, ok := singleField(t.strct)
	recv := receiverIdent(fd.decl)
	if !ok || recv == "" || fd.decl.Body == nil || len(fd.decl.Body.List) != 1 {
		return false
	}
	ret, ok := fd.decl.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	sel, ok := ret.Results[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == recv
}

// constructor is core-constructor on an exported function.
func (c *coreChecks) constructor(fd *funcDecl) {
	decl, f := fd.decl, fd.file
	name := decl.Name.Name
	for _, banned := range []string{"Make", "Create", "Build", "Init", "From", "To", "Get", "Set", "Reconstruct", "Must"} {
		if wordPrefix(name, banned) {
			c.at(f, decl.Name, "core-constructor", name+" is not a constructor or domain service")
			return
		}
	}
	rs := params(decl.Type.Results)
	switch {
	case wordPrefix(name, "Parse") || wordPrefix(name, "New"):
		// What the name says it makes: a type of this package, or an outcome of one.
		if len(rs) == 1 && (c.own(rs[0]) || c.outcomeOfOwn(f, rs[0])) {
			return
		}
		c.at(f, decl.Name, "core-constructor", name+" is not a constructor or domain service: a Parse or New makes "+
			"a type of its own package")
		return
	case wordPrefix(name, "Restore"):
		if len(rs) == 1 && c.isOutcome(f, rs[0]) {
			return
		}
		c.at(f, decl.Name, "core-constructor", name+" is not a constructor or domain service: a Restore returns an outcome")
		return
	}
	if len(rs) == 1 {
		r := rs[0]
		if c.isOutcome(f, r) {
			if args := typeArgs(r); len(args) > 0 {
				r = args[0]
			}
		}
		pkg, base := baseType(r)
		// A closed-set constructor: named <Type><Value> and returning that type.
		if pkg == "" && c.p.decls.types[base] != nil && strings.HasPrefix(name, base) && len(name) > len(base) &&
			exported(name[len(base):]) {
			return
		}
		// A domain service: takes a core value and returns a core type.
		if c.coreType(f, r) {
			for _, p := range params(decl.Type.Params) {
				if c.coreType(f, unwrapParam(p)) {
					return
				}
			}
		}
	}
	c.at(f, decl.Name, "core-constructor", name+" is not a constructor or domain service")
}

// wordPrefix says whether name starts with the word prefix: Parse in ParseRef and Parse, not in Parser.
func wordPrefix(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	rest := name[len(prefix):]
	return rest == "" || rest[0] >= 'A' && rest[0] <= 'Z' || rest[0] >= '0' && rest[0] <= '9'
}

// imports is core-import on one file: the computing standard library, the kit a core may use and shared kernels; for
// the kit, the computing standard library, the other kit packages and what kitImports lists.
func (c *coreChecks) imports(f *File) {
	for _, imp := range f.Imports {
		path := imp.Path
		if computing[path] {
			continue
		}
		rel := c.idx.rel(path)
		l := c.idx.layer(path)
		switch {
		case c.kit && (l == layerKit || contains(kitImports[c.p.Rel], path)):
			continue
		case !c.kit && (l == layerShared || l == layerKit && kitForCores[rel]):
			continue
		}
		c.edge(imp.Pos, "core-import", c.p.Rel, display(c.p)+" imports "+rel, f.Rel+" imports "+rel)
	}
}

// effects is core-effect on one file: no go statement, channel, select, panic, recover, print or println, the kit's
// listed API aside.
func (c *coreChecks) effects(f *File) {
	for _, d := range f.ast.Decls {
		if !c.exempt(declName(d), "core-effect") {
			c.effectsIn(f, d)
		}
	}
}

// effectsIn is core-effect on one declaration.
func (c *coreChecks) effectsIn(f *File, d ast.Decl) {
	ast.Inspect(d, func(n ast.Node) bool {
		what := ""
		switch x := n.(type) {
		case *ast.GoStmt:
			what = "a go statement"
		case *ast.ChanType:
			what = "a channel"
		case *ast.SelectStmt:
			what = "select"
		case *ast.SendStmt:
			what = "a channel send"
		case *ast.UnaryExpr:
			if x.Op == token.ARROW {
				what = "a channel receive"
			}
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok {
				switch id.Name {
				case "panic", "recover", "print", "println":
					what = id.Name
				}
			}
		}
		if what != "" {
			pos := f.pos(n)
			c.add(pos, "core-effect", c.p.Rel, f.Rel+":"+itoa(pos.Line)+" uses "+what)
		}
		return true
	})
}

// clockFuncs are the time functions that read or wait on the clock.
var clockFuncs = map[string]bool{
	"Now": true, "Since": true, "Until": true, "Sleep": true, "After": true, "AfterFunc": true, "Tick": true,
	"NewTimer": true, "NewTicker": true, "LoadLocation": true,
}

// clockCalls is core-clock-call on one file: any reference to a clock function of time, or to math/rand, called or
// not.
func (c *coreChecks) clockCalls(f *File) {
	timeName := c.idx.stdName(f, "time")
	rands := map[string]bool{}
	for _, p := range []string{"math/rand", "math/rand/v2"} {
		if n := c.idx.stdName(f, p); n != "" {
			rands[n] = true
		}
	}
	if timeName == "" && len(rands) == 0 {
		return
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if id.Name == timeName && clockFuncs[sel.Sel.Name] || rands[id.Name] {
			c.at(f, sel, "core-clock-call", f.Rel+" calls "+id.Name+"."+sel.Sel.Name)
		}
		return true
	})
}

// anyUses is core-any on one file: no any and no interface{}, once per declaration. In the kit, a type parameter's
// constraint may be any (kitConstraints says which), and its listed API may use any where kitAPI says.
func (c *coreChecks) anyUses(f *File) {
	for _, d := range f.ast.Decls {
		if c.exempt(declName(d), "core-any") {
			continue
		}
		if found := c.firstAny(d); found != nil {
			c.at(f, found, "core-any", declName(d)+" uses any")
		}
	}
}

// firstAny is the first any or interface{} in d, nil when there is none; the kit's type parameter lists are left
// out, since kitConstraints checks them.
func (c *coreChecks) firstAny(d ast.Decl) ast.Node {
	constraints := map[*ast.FieldList]bool{}
	if c.kit {
		ast.Inspect(d, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncType:
				constraints[x.TypeParams] = x.TypeParams != nil
			case *ast.TypeSpec:
				constraints[x.TypeParams] = x.TypeParams != nil
			}
			return true
		})
	}
	var found ast.Node
	ast.Inspect(d, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FieldList:
			return !constraints[x] && found == nil
		case *ast.Ident:
			if x.Name == "any" {
				found = x
			}
		case *ast.InterfaceType:
			if len(x.Methods.List) == 0 {
				found = x
			}
		}
		return found == nil
	})
	return found
}

// declName is a top-level declaration's name for messages.
func declName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			recv, _ := receiverName(d.Recv.List[0].Type)
			return recv + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.TypeSpec:
				return s.Name.Name
			case *ast.ValueSpec:
				if len(s.Names) > 0 {
					return s.Names[0].Name
				}
			}
		}
	}
	return "a declaration"
}

// partNames is core-part-name on one file: a fail.NewPart literal holds no underscore.
func (c *coreChecks) partNames(f *File) {
	failName := c.idx.kitName(f, "fail")
	if failName == "" {
		return
	}
	ast.Inspect(f.ast, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewPart" {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Name == failName {
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && strings.Contains(lit.Value, "_") {
				c.at(f, lit, "core-part-name", "Part "+lit.Value+" looks like a wire field")
			}
		}
		return true
	})
}

// outcomes is core-outcome-return on one file: every return of an outcome sets it, no outcome is built by literal,
// and no call's result is discarded, whether with _ or by a call written as a statement (dropped).
func (c *coreChecks) outcomes(f *File) {
	outcomeName := c.idx.kitName(f, "outcome")
	imports, methods := c.idx.imports(f), c.methodResults(f)
	for _, d := range f.ast.Decls {
		fd, ok := d.(*ast.FuncDecl)
		name := declName(d)
		if ok && fd.Body != nil {
			c.returns(f, name, fd.Type, fd.Body, outcomeName)
		}
		ast.Inspect(d, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncLit:
				c.returns(f, name, x.Type, x.Body, outcomeName)
			case *ast.CompositeLit:
				if pkg, base := baseType(x.Type); base == "Of" && pkg != "" && pkg == outcomeName {
					c.at(f, x, "core-outcome-return", name+" builds an outcome by literal")
				}
			case *ast.AssignStmt:
				if len(x.Rhs) == 1 {
					if _, call := x.Rhs[0].(*ast.CallExpr); call {
						for _, l := range x.Lhs {
							if id, ok := l.(*ast.Ident); ok && id.Name == "_" {
								c.at(f, x, "core-outcome-return", name+" discards what a call returns, as it would an outcome or a failure")
								break
							}
						}
					}
				}
			case *ast.ExprStmt:
				if call, ok := ast.Unparen(x.X).(*ast.CallExpr); ok && c.dropped(call, imports, methods) {
					c.at(f, x, "core-outcome-return", name+" calls "+exprString(callee(call))+
						" as a statement and drops what it returns")
				}
			}
			return true
		})
	}
}

// callee is the function a call names, its type arguments removed: ParseTitle for ParseTitle[T](x).
func callee(call *ast.CallExpr) ast.Expr {
	fun := ast.Unparen(call.Fun)
	switch x := fun.(type) {
	case *ast.IndexExpr:
		return ast.Unparen(x.X)
	case *ast.IndexListExpr:
		return ast.Unparen(x.X)
	}
	return fun
}

// dropped says whether a call written as a statement throws away a result, as far as the declarations the rules
// parsed tell: it calls a function of this package, or of the kit or a core or shared kernel the file imports
// (imports, by the names the file uses), that has results; or a method by a name that this package, the kit and the
// cores the file imports declare only with results (methods). A call the declarations cannot resolve (the standard
// library's, a func value's) is left alone.
func (c *coreChecks) dropped(call *ast.CallExpr, imports map[string]Import, methods map[string]bool) bool {
	switch fun := callee(call).(type) {
	case *ast.Ident:
		return funcResults(c.p.decls, fun.Name)
	case *ast.SelectorExpr:
		if id, ok := ast.Unparen(fun.X).(*ast.Ident); ok {
			if imp, ok := imports[id.Name]; ok {
				p := c.idx.pkg(imp.Path)
				return p != nil && p.decls != nil && c.idx.domain(imp.Path) && funcResults(p.decls, fun.Sel.Name)
			}
		}
		return methods[fun.Sel.Name]
	}
	return false
}

// funcResults says whether d declares a top-level function by that name with results.
func funcResults(d *decls, name string) bool {
	for _, fd := range d.funcs {
		if fd.decl.Name.Name == name {
			return results(fd.decl.Type)
		}
	}
	return false
}

// results says whether a function type returns anything.
func results(ft *ast.FuncType) bool { return ft.Results != nil && len(ft.Results.List) > 0 }

// methodResults is, for f, the method names declared only with results (on a type or in an interface) across this
// package, the kit and the cores and shared kernels f imports: a call of one of them as a statement drops a result
// whatever its receiver, as far as those declarations tell. A name any of them also declares without results is
// left out, since the receiver's type would decide.
func (c *coreChecks) methodResults(f *File) map[string]bool {
	if c.ownMethods.with == nil {
		c.ownMethods = methodNames{with: map[string]bool{}, without: map[string]bool{}}
		c.ownMethods.add(c.p.decls)
		for _, p := range c.idx.pkgs {
			if p != c.p && c.idx.layer(p.Path) == layerKit {
				c.ownMethods.add(p.decls)
			}
		}
	}
	names := c.ownMethods.copy()
	for _, imp := range f.Imports {
		if p := c.idx.pkg(imp.Path); p != nil && p != c.p && core(c.idx.layer(imp.Path)) {
			names.add(p.decls)
		}
	}
	for n := range names.without {
		delete(names.with, n)
	}
	return names.with
}

// methodNames is method names by whether a declaration of each has results.
type methodNames struct{ with, without map[string]bool }

// copy is m with maps of its own, so that adding to it leaves m as it was.
func (m methodNames) copy() methodNames {
	out := methodNames{with: map[string]bool{}, without: map[string]bool{}}
	for n := range m.with {
		out.with[n] = true
	}
	for n := range m.without {
		out.without[n] = true
	}
	return out
}

// add records d's methods and interface methods; a package not parsed in full adds nothing.
func (m methodNames) add(d *decls) {
	if d == nil {
		return
	}
	mark := func(name string, ft *ast.FuncType) {
		if results(ft) {
			m.with[name] = true
		} else {
			m.without[name] = true
		}
	}
	for _, fds := range d.methods {
		for _, fd := range fds {
			mark(fd.decl.Name.Name, fd.decl.Type)
		}
	}
	for _, t := range d.order {
		if t.iface == nil {
			continue
		}
		for _, field := range t.iface.Methods.List {
			if ft, ok := field.Type.(*ast.FuncType); ok {
				for _, n := range field.Names {
					mark(n.Name, ft)
				}
			}
		}
	}
}

// returns checks the returns of one function body (not of the function literals inside it) whose results include
// an outcome.
func (c *coreChecks) returns(f *File, name string, ft *ast.FuncType, body *ast.BlockStmt, outcomeName string) {
	if outcomeName == "" || ft.Results == nil {
		return
	}
	slot := -1
	for i, r := range params(ft.Results) {
		if pkg, base := baseType(r); base == "Of" && pkg == outcomeName {
			slot = i
		}
	}
	if slot < 0 {
		return
	}
	n := len(params(ft.Results))
	var walk func(n ast.Node) bool
	walk = func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			switch {
			case len(x.Results) == 0:
				c.at(f, x, "core-outcome-return", name+" returns an unset outcome")
			case len(x.Results) == n:
				if _, ok := x.Results[slot].(*ast.CallExpr); !ok {
					c.at(f, x, "core-outcome-return", name+" returns an unset outcome")
				}
			}
		}
		return true
	}
	ast.Inspect(body, walk)
}

// failFamily is core-fail-family: a core whose code uses failures declares one sealed family, and every type that
// embeds a fail base implements its marker.
func (c *coreChecks) failFamily() {
	uses := false
	for _, f := range c.p.Files {
		if !f.Test && (c.idx.kitName(f, "fail") != "" || c.idx.kitName(f, "outcome") != "") {
			uses = true
		}
	}
	if !uses {
		return
	}
	d := c.p.decls
	family := d.types["Fail"]
	marker := ""
	if family != nil && family.iface != nil {
		failName := c.idx.kitName(family.file, "fail")
		embeds := false
		for _, m := range family.iface.Methods.List {
			if len(m.Names) == 0 && failName != "" && exprString(m.Type) == failName+".Fail" {
				embeds = true
			}
			for _, n := range m.Names {
				if !exported(n.Name) {
					marker = n.Name
				}
			}
		}
		if !embeds || marker == "" || len(family.iface.Methods.List) != 2 {
			marker = ""
		}
	}
	if marker == "" {
		c.add(first(c.p), "core-fail-family", c.p.Rel, display(c.p)+" has no sealed Fail family")
		return
	}
	for _, t := range d.order {
		if t.strct == nil {
			continue
		}
		failName := c.idx.kitName(t.file, "fail")
		for _, fl := range t.strct.Fields.List {
			if len(fl.Names) != 0 {
				continue
			}
			if pkg, _ := baseType(fl.Type); pkg == "" || pkg != failName {
				continue
			}
			has := false
			for _, m := range d.methods[t.name] {
				if m.decl.Name.Name == marker {
					has = true
				}
			}
			if !has {
				c.at(t.file, t.spec.Name, "core-fail-family",
					display(c.p)+" has no sealed Fail family: "+t.name+" embeds a fail base without "+marker+"()")
			}
		}
	}
}

// receiverWrites is core-receiver-write on one method: no write through a slice or map reached from the receiver,
// or from a local that copies it.
func (c *coreChecks) receiverWrites(fd *funcDecl) {
	recv := receiverIdent(fd.decl)
	if recv == "" || fd.decl.Body == nil {
		return
	}
	f := fd.file
	aliases := map[string]bool{recv: true}
	slicesName, sortName := c.idx.stdName(f, "slices"), c.idx.stdName(f, "sort")
	rooted := func(e ast.Expr) bool {
		for {
			switch x := e.(type) {
			case *ast.Ident:
				return aliases[x.Name]
			case *ast.SelectorExpr:
				e = x.X
			case *ast.IndexExpr:
				e = x.X
			case *ast.ParenExpr:
				e = x.X
			case *ast.StarExpr:
				e = x.X
			case *ast.SliceExpr:
				e = x.X
			default:
				return false
			}
		}
	}
	// indexed says whether an assigned expression writes into an element reached from the receiver.
	var indexed func(e ast.Expr) bool
	indexed = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.IndexExpr:
			return rooted(x.X) && !c.arrayField(fd, x.X) || indexed(x.X)
		case *ast.SelectorExpr:
			return indexed(x.X)
		case *ast.ParenExpr:
			return indexed(x.X)
		case *ast.StarExpr:
			return indexed(x.X)
		}
		return false
	}
	report := func(n ast.Node, e ast.Expr) {
		c.at(f, n, "core-receiver-write", fd.name()+" writes through "+exprString(e))
	}
	ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE && len(x.Lhs) == len(x.Rhs) {
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && rooted(x.Rhs[i]) {
						if _, call := x.Rhs[i].(*ast.CallExpr); !call {
							aliases[id.Name] = true
						}
					}
				}
			}
			for _, l := range x.Lhs {
				if indexed(l) {
					report(x, l)
				}
			}
		case *ast.ValueSpec:
			for i, n := range x.Names {
				if i < len(x.Values) && rooted(x.Values[i]) {
					aliases[n.Name] = true
				}
			}
		case *ast.IncDecStmt:
			if indexed(x.X) {
				report(x, x.X)
			}
		case *ast.CallExpr:
			if len(x.Args) == 0 {
				return true
			}
			target := x.Args[0]
			switch fn := x.Fun.(type) {
			case *ast.Ident:
				switch fn.Name {
				case "append", "clear", "copy", "delete":
					if rooted(target) && !c.arrayField(fd, target) {
						report(x, target)
					}
				}
			case *ast.SelectorExpr:
				id, ok := fn.X.(*ast.Ident)
				if !ok || !rooted(target) {
					return true
				}
				switch {
				case id.Name == slicesName && slicesName != "":
					switch fn.Sel.Name {
					case "Sort", "SortFunc", "SortStableFunc", "Reverse", "Delete", "DeleteFunc", "Insert", "Replace",
						"Compact", "CompactFunc":
						report(x, target)
					}
				case id.Name == sortName && sortName != "":
					switch fn.Sel.Name {
					case "Sort", "Stable", "Slice", "SliceStable", "Strings", "Ints", "Float64s":
						report(x, target)
					}
				}
			}
		}
		return true
	})
}

// arrayField says whether e is a receiver field declared as a fixed-size array, which a value receiver copies.
func (c *coreChecks) arrayField(fd *funcDecl, e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	t := c.p.decls.types[fd.recv]
	if t == nil || t.strct == nil {
		return false
	}
	for _, fl := range t.strct.Fields.List {
		for _, n := range fl.Names {
			if n.Name == sel.Sel.Name {
				at, ok := fl.Type.(*ast.ArrayType)
				return ok && at.Len != nil
			}
		}
	}
	return false
}

// mapRanges is core-map-range on one function: no range over a local, parameter or receiver field declared as a
// map, and maps.Keys, Values and All only inside slices.Sorted or SortedFunc.
func (c *coreChecks) mapRanges(fd *funcDecl) {
	f := fd.file
	maps := map[string]bool{}
	isMapType := func(e ast.Expr) bool { _, ok := e.(*ast.MapType); return ok }
	isMapValue := func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.CompositeLit:
			return isMapType(x.Type)
		case *ast.CallExpr:
			if id, ok := x.Fun.(*ast.Ident); ok && id.Name == "make" && len(x.Args) > 0 {
				return isMapType(x.Args[0])
			}
		}
		return false
	}
	if fd.decl.Type.Params != nil {
		for _, p := range fd.decl.Type.Params.List {
			if isMapType(p.Type) {
				for _, n := range p.Names {
					maps[n.Name] = true
				}
			}
		}
	}
	recv := receiverIdent(fd.decl)
	mapsName, slicesName := c.idx.stdName(f, "maps"), c.idx.stdName(f, "slices")
	var sorted []ast.Node // the arguments of slices.Sorted and SortedFunc calls
	ast.Inspect(fd.decl.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE && len(x.Lhs) == len(x.Rhs) {
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && isMapValue(x.Rhs[i]) {
						maps[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for i, nm := range x.Names {
				if isMapType(x.Type) || i < len(x.Values) && isMapValue(x.Values[i]) {
					maps[nm.Name] = true
				}
			}
		case *ast.RangeStmt:
			over := false
			switch r := x.X.(type) {
			case *ast.Ident:
				over = maps[r.Name]
			case *ast.CompositeLit:
				over = isMapType(r.Type)
			case *ast.SelectorExpr:
				if id, ok := r.X.(*ast.Ident); ok && id.Name == recv && recv != "" {
					over = c.mapField(fd, r.Sel.Name)
				}
			}
			if over {
				pos := f.pos(x)
				c.add(pos, "core-map-range", c.p.Rel, f.Rel+":"+itoa(pos.Line)+" ranges over a map")
			}
		case *ast.CallExpr:
			sel, ok := x.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if id.Name == slicesName && slicesName != "" && (sel.Sel.Name == "Sorted" || sel.Sel.Name == "SortedFunc") {
				for _, a := range x.Args {
					sorted = append(sorted, a)
				}
			}
			if id.Name == mapsName && mapsName != "" && (sel.Sel.Name == "Keys" || sel.Sel.Name == "Values" || sel.Sel.Name == "All") {
				inside := false
				for _, s := range sorted {
					if s == ast.Node(x) {
						inside = true
					}
				}
				if !inside {
					pos := f.pos(x)
					c.add(pos, "core-map-range", c.p.Rel, f.Rel+":"+itoa(pos.Line)+" ranges over a map")
				}
			}
		}
		return true
	})
}

// mapField says whether the receiver's field name is declared as a map.
func (c *coreChecks) mapField(fd *funcDecl, name string) bool {
	t := c.p.decls.types[fd.recv]
	if t == nil || t.strct == nil {
		return false
	}
	for _, fl := range t.strct.Fields.List {
		for _, n := range fl.Names {
			if n.Name == name {
				_, ok := fl.Type.(*ast.MapType)
				return ok
			}
		}
	}
	return false
}

// seamRules checks a seam's exported fields, variables, ports and direct field getters.
func (c *coreChecks) seamRules() {
	d := c.p.decls
	types := seamTypes{}
	for _, v := range d.values {
		if v.tok == token.VAR && exported(v.name.Name) {
			c.at(v.file, v.name, "seam-exported-field", v.name.Name+" is exported")
		}
	}
	for _, t := range d.order {
		types[t.name] = append(types[t.name], t)
		if t.strct == nil {
			c.seamPortType(t)
			continue
		}
		for _, fl := range t.strct.Fields.List {
			for _, n := range fl.Names {
				if exported(n.Name) {
					c.at(t.file, n, "seam-exported-field", t.name+"."+n.Name+" is exported")
				}
			}
			if len(fl.Names) == 0 {
				if _, name := baseType(fl.Type); exported(name) {
					c.at(t.file, fl, "seam-exported-field", t.name+"."+name+" is exported")
				}
			}
		}
	}
	check := func(fd *funcDecl) {
		if !exported(fd.decl.Name.Name) {
			return
		}
		for _, fl := range []*ast.FieldList{fd.decl.Type.Params, fd.decl.Type.Results} {
			for _, p := range params(fl) {
				if untyped(p) {
					c.at(fd.file, fd.decl.Name, "seam-any", fd.name()+" takes an untyped value")
					return
				}
			}
		}
	}
	for _, fd := range d.funcs {
		check(fd)
	}
	for _, methods := range d.methods {
		for _, fd := range methods {
			check(fd)
			if fd.decl.Name.IsExported() {
				c.seamGetter(types, fd)
			}
		}
	}
}

// seamGetter reports a direct return of a declared receiver field. A selector can also name a method.
func (c *coreChecks) seamGetter(types seamTypes, fd *funcDecl) {
	recv := receiverIdent(fd.decl)
	for _, result := range soleReturn(fd.decl) {
		sel, ok := ast.Unparen(result).(*ast.SelectorExpr)
		if !ok {
			continue
		}
		root := ast.Unparen(sel.X)
		switch ptr := root.(type) {
		case *ast.StarExpr:
			root = ast.Unparen(ptr.X)
		case *ast.UnaryExpr:
			if ptr.Op == token.AND {
				root = ast.Unparen(ptr.X)
			}
		}
		id, ok := root.(*ast.Ident)
		if !ok || id.Name != recv {
			continue
		}
		if types.hasField(fd.recv, fd.file, sel.Sel.Name, false, nil) {
			c.at(fd.file, fd.decl.Name, "core-accessor", fd.name()+"() hands state out, returning "+exprString(result))
			return
		}
	}
}

// seamTypes keeps all build variants of a name. A declaration in the use's own file applies to that use;
// elsewhere a field must be present in every possible declaration before a selector is known to read it.
type seamTypes map[string][]*typeDecl

func (types seamTypes) hasField(name string, file *File, field string, public bool, path []string) bool {
	candidates := types[name]
	if len(candidates) == 0 || contains(path, name) {
		return false
	}
	for _, t := range candidates {
		if t.file == file {
			candidates = []*typeDecl{t}
			break
		}
	}
	public = public || ast.IsExported(name)
	for _, t := range candidates {
		// An alias shares its target's methods. A distinct private type does not acquire its base's public API.
		if !public && !t.spec.Assign.IsValid() {
			return false
		}
		if t.strct == nil {
			pkg, base := baseType(t.spec.Type)
			if pkg != "" || !types.hasField(base, t.file, field, public, append(path, name)) {
				return false
			}
			continue
		}
		found := false
		for _, f := range t.strct.Fields.List {
			if contains(fieldNames(f), field) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// seamPortType checks the type a seam exports, leaving type parameter constraints outside the check.
func (c *coreChecks) seamPortType(t *typeDecl) {
	if !exported(t.name) {
		return
	}
	if t.iface == nil || len(t.iface.Methods.List) == 0 {
		if untyped(t.spec.Type) {
			c.at(t.file, t.spec.Name, "seam-any", t.name+" exposes an untyped value")
		}
		return
	}
	c.seamInterfaceMethods(t, t.iface)
}

func (c *coreChecks) seamInterfaceMethods(t *typeDecl, iface *ast.InterfaceType) {
	for _, method := range iface.Methods.List {
		if embedded, ok := ast.Unparen(method.Type).(*ast.InterfaceType); ok {
			c.seamInterfaceMethods(t, embedded)
			continue
		}
		for _, name := range method.Names {
			if exported(name.Name) && untyped(method.Type) {
				c.at(t.file, name, "seam-any", t.name+"."+name.Name+" exposes an untyped value")
			}
		}
	}
}

// untyped says whether a type expression holds any, interface{} or a map of them, outside a type parameter list.
func untyped(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.Field:
			// A parameter, result or struct field can be named any and still have a concrete type.
			found = found || untyped(x.Type)
			return false
		case *ast.SelectorExpr:
			return false // a qualified name cannot be the predeclared any
		case *ast.ArrayType:
			found = found || untyped(x.Elt)
			return false // the length is a value expression
		case *ast.Ident:
			if x.Name == "any" {
				found = true
			}
		case *ast.InterfaceType:
			if len(x.Methods.List) == 0 {
				found = true
			}
		}
		return !found
	})
	return found
}
