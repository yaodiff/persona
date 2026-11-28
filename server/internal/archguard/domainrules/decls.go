package domainrules

import (
	"go/ast"
	"go/token"
	"strings"
)

// decls is what a fully parsed package declares in its non-test files.
type decls struct {
	types   map[string]*typeDecl
	order   []*typeDecl
	funcs   []*funcDecl            // top-level functions, init included
	methods map[string][]*funcDecl // by the receiver's type name
	values  []*valueDecl           // package-level var and const names
}

// A typeDecl is one type declaration.
type typeDecl struct {
	name   string
	spec   *ast.TypeSpec
	doc    string
	file   *File
	strct  *ast.StructType    // when it is a struct
	iface  *ast.InterfaceType // when it is an interface
	sealed bool               // an interface with an unexported method: only its package implements it
}

// A funcDecl is one function or method.
type funcDecl struct {
	decl *ast.FuncDecl
	file *File
	recv string // the receiver's type name, "" for a function
	ptr  bool   // a pointer receiver
}

// name is the function as messages name it: Type.Method or Func.
func (f *funcDecl) name() string {
	if f.recv != "" {
		return f.recv + "." + f.decl.Name.Name
	}
	return f.decl.Name.Name
}

// A valueDecl is one package-level var or const.
type valueDecl struct {
	name  *ast.Ident
	tok   token.Token
	file  *File
	value ast.Expr
}

// index collects p's declarations from its parsed non-test files.
func index(p *Pkg) *decls {
	d := &decls{types: map[string]*typeDecl{}, methods: map[string][]*funcDecl{}}
	for _, f := range p.Files {
		if f.Test || f.ast == nil {
			continue
		}
		for _, decl := range f.ast.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				fd := &funcDecl{decl: decl, file: f}
				if decl.Recv != nil && len(decl.Recv.List) == 1 {
					fd.recv, fd.ptr = receiverName(decl.Recv.List[0].Type)
					d.methods[fd.recv] = append(d.methods[fd.recv], fd)
				} else {
					d.funcs = append(d.funcs, fd)
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						td := newTypeDecl(f, decl, spec)
						d.types[td.name] = td
						d.order = append(d.order, td)
					case *ast.ValueSpec:
						for i, n := range spec.Names {
							v := &valueDecl{name: n, tok: decl.Tok, file: f}
							if i < len(spec.Values) {
								v.value = spec.Values[i]
							}
							d.values = append(d.values, v)
						}
					}
				}
			}
		}
	}
	return d
}

// newTypeDecl is the declaration spec makes in f, inside decl.
func newTypeDecl(f *File, decl *ast.GenDecl, spec *ast.TypeSpec) *typeDecl {
	td := &typeDecl{name: spec.Name.Name, spec: spec, file: f}
	switch {
	case spec.Doc != nil:
		td.doc = spec.Doc.Text()
	case decl.Doc != nil && len(decl.Specs) == 1:
		td.doc = decl.Doc.Text()
	}
	switch t := spec.Type.(type) {
	case *ast.StructType:
		td.strct = t
	case *ast.InterfaceType:
		td.iface = t
		for _, m := range t.Methods.List {
			for _, n := range m.Names {
				if !exported(n.Name) {
					td.sealed = true
				}
			}
		}
	}
	return td
}

// typeNamed is the declaration of the type name in the non-test files of the package in dir, its generated files
// included, nil when they declare none. It parses, once each, only the files whose text holds the name as a word,
// since a file that declares a type names it: reading a contract's or a library's type costs the files that mention
// it, not the package. The files it parses count in the budget (Parsed).
func (t *tree) typeNamed(dir, name string) (*typeDecl, error) {
	key := dir + " " + name
	if td, ok := t.named[key]; ok {
		return td, nil
	}
	var files []*File
	if p := t.dirs[dir]; p != nil {
		files = p.Files
	}
	files = append(files[:len(files):len(files)], t.generated[dir]...)
	var found *typeDecl
	for _, f := range files {
		if f.Test || found != nil {
			continue
		}
		if f.ast == nil {
			src, err := t.read(f.Rel)
			if err != nil {
				return nil, err
			}
			if !holdsWord(string(src), name) {
				continue
			}
			if err := f.parse(); err != nil {
				return nil, err
			}
		}
		for _, decl := range f.ast.Decls {
			if g, ok := decl.(*ast.GenDecl); ok && g.Tok == token.TYPE {
				for _, spec := range g.Specs {
					if ts := spec.(*ast.TypeSpec); ts.Name.Name == name {
						found = newTypeDecl(f, g, ts)
					}
				}
			}
		}
	}
	t.named[key] = found
	return found, nil
}

// holdsWord says whether text holds word with no letter, digit or underscore on either side.
func holdsWord(text, word string) bool {
	ident := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
	}
	for from := 0; ; {
		i := strings.Index(text[from:], word)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(word)
		if (i == 0 || !ident(text[i-1])) && (end == len(text) || !ident(text[end])) {
			return true
		}
		from = i + 1
	}
}

// receiverName is a receiver's type name and whether it is a pointer: T for T, *T, T[K] and *T[K, V].
func receiverName(e ast.Expr) (string, bool) {
	ptr := false
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			ptr, e = true, x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name, ptr
		default:
			return exprString(e), ptr
		}
	}
}

// receiverIdent is a method's receiver name, "" when it has none or it is _.
func receiverIdent(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) != 1 || len(fd.Recv.List[0].Names) != 1 {
		return ""
	}
	if n := fd.Recv.List[0].Names[0].Name; n != "_" {
		return n
	}
	return ""
}

// typeParams is the names of a function's and its receiver's type parameters.
func typeParams(fd *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	if fd.Type.TypeParams != nil {
		for _, f := range fd.Type.TypeParams.List {
			for _, n := range f.Names {
				out[n.Name] = true
			}
		}
	}
	if fd.Recv != nil && len(fd.Recv.List) == 1 {
		var walk func(e ast.Expr)
		walk = func(e ast.Expr) {
			switch x := e.(type) {
			case *ast.StarExpr:
				walk(x.X)
			case *ast.ParenExpr:
				walk(x.X)
			case *ast.IndexExpr:
				if id, ok := x.Index.(*ast.Ident); ok {
					out[id.Name] = true
				}
			case *ast.IndexListExpr:
				for _, i := range x.Indices {
					if id, ok := i.(*ast.Ident); ok {
						out[id.Name] = true
					}
				}
			}
		}
		walk(fd.Recv.List[0].Type)
	}
	return out
}

// fieldCount is how many named fields a struct has, blank ones left out, an embedded field counting as one.
func fieldCount(st *ast.StructType) int {
	n := 0
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			n++
		}
		for _, name := range f.Names {
			if name.Name != "_" {
				n++
			}
		}
	}
	return n
}

// singleField is a single-field struct's field name, ignoring blank fields, and whether it has exactly one.
func singleField(st *ast.StructType) (string, bool) {
	if fieldCount(st) != 1 {
		return "", false
	}
	for _, f := range st.Fields.List {
		for _, name := range f.Names {
			if name.Name != "_" {
				return name.Name, true
			}
		}
		if len(f.Names) == 0 {
			return exprString(f.Type), true
		}
	}
	return "", false
}

// shape is a struct's field list as text: names and types, in order.
func shape(st *ast.StructType) string {
	var parts []string
	for _, f := range st.Fields.List {
		names := make([]string, 0, len(f.Names))
		for _, n := range f.Names {
			names = append(names, n.Name)
		}
		parts = append(parts, strings.TrimSpace(strings.Join(names, ", ")+" "+exprString(f.Type)))
	}
	return "{" + strings.Join(parts, "; ") + "}"
}

// basic are the predeclared types a primitive is made of.
var basic = map[string]bool{
	"string": true, "bool": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true, "byte": true, "rune": true,
	"error": true, "any": true,
}

// stdPrimitives are named types of the computing standard library that hold raw primitives (strings, numbers, bytes,
// runes or functions), so a core cannot hide raw input behind them in a field or signature.
var stdPrimitives = map[string]bool{
	"bytes.Buffer":        true,
	"bytes.Reader":        true,
	"iter.Seq":            true,
	"iter.Seq2":           true,
	"sort.Float64Slice":   true,
	"sort.IntSlice":       true,
	"sort.StringSlice":    true,
	"strconv.NumError":    true,
	"strings.Builder":     true,
	"strings.Reader":      true,
	"strings.Replacer":    true,
	"unicode.CaseRange":   true,
	"unicode.Range16":     true,
	"unicode.Range32":     true,
	"unicode.RangeTable":  true,
	"unicode.SpecialCase": true,
}

// stdCollections are named types of the computing standard library that are or hold slices of raw data, so a core
// cannot pass or return a collection behind them.
var stdCollections = map[string]bool{
	"bytes.Buffer":        true,
	"bytes.Reader":        true,
	"sort.Float64Slice":   true,
	"sort.IntSlice":       true,
	"sort.StringSlice":    true,
	"unicode.RangeTable":  true,
	"unicode.SpecialCase": true,
}

// stdType is "<import path>.<Sel>" when sel's package qualifier is an import of f, "" otherwise.
func stdType(sel *ast.SelectorExpr, f *File, idx Index) string {
	id, ok := sel.X.(*ast.Ident)
	if !ok || f == nil {
		return ""
	}
	if imp, ok := idx.imports(f)[id.Name]; ok {
		return imp.Path + "." + sel.Sel.Name
	}
	return ""
}

// primitive says whether a type expression is a primitive: a predeclared basic type, error or any, an empty
// interface, a func or chan type, a standard-library named type that wraps raw values (sort.StringSlice,
// strings.Builder, bytes.Buffer, …), or a slice, array or pointer of one, a map with one as key or value, or a
// generic type with one among its type arguments. A kit type is no wrapper that makes raw input domain input:
// opt.Option[string] and outcome.Of[[]int, Fail] are primitives as string and []int are. A type the package declares
// counts as what it is declared as, exported or not: an alias as its target, a named basic, func, chan or
// standard-library primitive type (type label string, type lines sort.StringSlice) as that type, and a struct as no
// primitive, so a name of its own hides no primitive.
func (d *decls) primitive(f *File, idx Index, e ast.Expr) bool {
	return d.primitiveIn(f, idx, e, map[string]bool{})
}

func (d *decls) primitiveIn(f *File, idx Index, e ast.Expr, seen map[string]bool) bool {
	switch x := e.(type) {
	case *ast.Ident:
		if t, tf := d.declared(x, seen); t != nil {
			return d.primitiveIn(tf, idx, t, seen)
		}
		return basic[x.Name]
	case *ast.SelectorExpr:
		return stdPrimitives[stdType(x, f, idx)]
	case *ast.ParenExpr:
		return d.primitiveIn(f, idx, x.X, seen)
	case *ast.IndexExpr, *ast.IndexListExpr:
		for _, a := range typeArgs(x) {
			if d.primitiveIn(f, idx, a, seen) {
				return true
			}
		}
		if base := genericBase(x); base != nil {
			return d.primitiveIn(f, idx, base, seen)
		}
		return false
	case *ast.StarExpr:
		return d.primitiveIn(f, idx, x.X, seen)
	case *ast.ArrayType:
		return d.primitiveIn(f, idx, x.Elt, seen)
	case *ast.Ellipsis:
		return d.primitiveIn(f, idx, x.Elt, seen)
	case *ast.MapType:
		return d.primitiveIn(f, idx, x.Key, seen) || d.primitiveIn(f, idx, x.Value, seen)
	case *ast.FuncType, *ast.ChanType:
		return true
	case *ast.InterfaceType:
		return len(x.Methods.List) == 0
	}
	return false
}

// collection says whether a type expression is a slice, array or map (or a variadic parameter), a standard-library
// named slice type (sort.StringSlice, bytes.Buffer, …), or a generic type with one among its type arguments, all the
// way down (opt.Option[[]Agent], outcome.Of[map[Key]Agent, Fail]). A type the package declares counts as what it is
// declared as, exported or not (type rows []row is a slice).
func (d *decls) collection(f *File, idx Index, e ast.Expr) bool {
	return d.collectionIn(f, idx, e, map[string]bool{})
}

func (d *decls) collectionIn(f *File, idx Index, e ast.Expr, seen map[string]bool) bool {
	switch x := e.(type) {
	case *ast.Ident:
		if t, tf := d.declared(x, seen); t != nil {
			return d.collectionIn(tf, idx, t, seen)
		}
	case *ast.SelectorExpr:
		return stdCollections[stdType(x, f, idx)]
	case *ast.ParenExpr:
		return d.collectionIn(f, idx, x.X, seen)
	case *ast.ArrayType, *ast.MapType, *ast.Ellipsis:
		return true
	case *ast.IndexExpr, *ast.IndexListExpr:
		for _, a := range typeArgs(x) {
			if d.collectionIn(f, idx, a, seen) {
				return true
			}
		}
		if base := genericBase(x); base != nil {
			return d.collectionIn(f, idx, base, seen)
		}
	}
	return false
}

// declared is what a type name the package declares is declared as (an alias's target included) and the file that
// declares it, once per walk (seen), so a type that names itself ends; nil for a name the package does not declare.
func (d *decls) declared(id *ast.Ident, seen map[string]bool) (ast.Expr, *File) {
	t := d.types[id.Name]
	if t == nil || seen[id.Name] {
		return nil, nil
	}
	seen[id.Name] = true
	return t.spec.Type, t.file
}

// named is what a parameter's type is once the package's own aliases and named non-struct, non-interface types are
// read as what they are declared as, pointers, slices, arrays, variadics and map values removed (unwrapParam) at
// every step: func(...) for type fetch func(...) and for fetch[T] of a generic one, the type itself otherwise.
func (d *decls) named(e ast.Expr) ast.Expr {
	t := unwrapParam(e)
	seen := map[string]bool{}
	for {
		base := t
		if g := genericBase(t); g != nil {
			base = g
		}
		id, ok := base.(*ast.Ident)
		if !ok {
			return t
		}
		td := d.types[id.Name]
		if td == nil || td.strct != nil || td.iface != nil || seen[id.Name] {
			return t
		}
		seen[id.Name] = true
		t = unwrapParam(td.spec.Type)
	}
}

// genericBase is the generic type an instantiation names (list for list[T]).
func genericBase(e ast.Expr) ast.Expr {
	switch x := e.(type) {
	case *ast.IndexExpr:
		return x.X
	case *ast.IndexListExpr:
		return x.X
	}
	return nil
}

// baseType is the type a type expression names, generics and pointers removed: Of for outcome.Of[T, F] with its
// package's name ("outcome"), State for *State.
func baseType(e ast.Expr) (pkg, name string) {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return "", x.Name
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok {
				return id.Name, x.Sel.Name
			}
			return "", ""
		default:
			return "", ""
		}
	}
}

// typeArgs is the type arguments of an instantiated type expression (outcome.Of[T, F] -> T, F).
func typeArgs(e ast.Expr) []ast.Expr {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return typeArgs(x.X)
	case *ast.IndexExpr:
		return []ast.Expr{x.Index}
	case *ast.IndexListExpr:
		return x.Indices
	}
	return nil
}

// params is a field list's entries one per name (an unnamed parameter counting once), with its type.
func params(fl *ast.FieldList) []ast.Expr {
	if fl == nil {
		return nil
	}
	var out []ast.Expr
	for _, f := range fl.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, f.Type)
		}
	}
	return out
}
