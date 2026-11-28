package node

import (
	"bytes"
	"go/scanner"
	"go/token"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"mvdan.cc/sh/v3/syntax"
)

// normalizeLarkCommand removes inert here-documents, reads programs handed over in here-documents and one-command
// groups as written inline (larkgate_inline.go) and resolves immediately preceding literal command assignments
// solely for classification. It never executes the input.
func normalizeLarkCommand(cmd, cwd string, gates []proto.CommandGate) string {
	return normalizeLarkCommandRelaxed(cmd, cwd, gates, nil, false)
}

// resolved, when non-nil, records the normalized segment positions whose executable came from a proven assignment.
// The classifier uses that provenance only for the neighboring-opaque-program check; the command itself is still
// parsed and classified normally. Positions, rather than word content, keep an identical later literal segment distinct.
// relax (a bypass conversation, classifyLarkRelaxed) also lets a here-document's file be named through a literal
// variable set earlier in the list (`d=~/notes; cat > $d/note.md <<'EOF'`) and sit in a directory that does not exist
// yet (made by mkdir in the same list), #15860.
func normalizeLarkCommandRelaxed(cmd, cwd string, gates []proto.CommandGate, resolved map[int]bool, relax bool) string {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return cmd
	}
	inlined := larkInlineSyntax(tree, gates)
	if inlined {
		var out bytes.Buffer
		if syntax.NewPrinter().Print(&out, tree) != nil {
			return cmd
		}
		cmd = out.String()
		if tree, err = syntax.NewParser().Parse(strings.NewReader(cmd), ""); err != nil {
			return cmd
		}
	}
	// Only the leading prefix of simple commands, pipelines, and && / || chains takes part in assignment
	// propagation. An unsupported suffix (loops, subshells, blocks, declarations) stops later resolutions without
	// invalidating earlier ones; flat && / || lists still get heredoc handling below.
	stmts, flat := larkFlatStmts(tree.Stmts)
	prefixStmts := larkNormPrefix(tree.Stmts)
	if !flat && len(prefixStmts) == 0 {
		return cmd
	}
	if !larkNormAssignsSafe(prefixStmts, cmd) {
		return cmd
	}
	state := larkNormState{
		vars:     map[string]string{},
		varsDir:  map[string]string{},
		textVars: map[string]string{},
	}
	if filepath.IsAbs(cwd) {
		state.currentDir = filepath.Clean(cwd)
	}
	changed := false
	segPosReliable := true
	segmentPosition := 0
	for _, stmt := range prefixStmts {
		onTrue, onFalse := walkNormStmt(stmt, state, cmd, gates, resolved, &segmentPosition, &segPosReliable, &changed)
		if b, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && (b.Op == syntax.AndStmt || b.Op == syntax.OrStmt) {
			state = intersectNormState(onTrue, onFalse)
		} else {
			state = onTrue
		}
	}
	// An opaque command stops later resolutions; expansions already made in earlier commands remain valid because
	// those commands run first. Keeping them also handles a literal prefix in the final command of a sequence.
	if !flat {
		stmts = nil // here-documents are judged only in lists of simple commands
	}

	// A heredoc fed only to a file writer is data, except when a later command
	// could execute it or its unquoted body contains an executable substitution.
	hasHeredoc, safe, goTest := false, true, false
	var bodies []*syntax.Word
	dir := cwd // a literal cd moves where later relative targets are
	// vars (relax): literal values of variables assigned by a statement of their own at the top of the list, in order.
	// One assigned anywhere else (after && or ||) or to anything but a literal is unknown from then on.
	vars := map[string]string{}
	top := map[*syntax.Stmt]bool{}
	for _, stmt := range tree.Stmts {
		top[stmt] = true
	}
	// path is a target or argument word's value: a literal word as before or, relaxed, one with variables from vars.
	path := func(word *syntax.Word, value string) string {
		if value == "" && relax {
			if resolved, _, ok := literalLoopWord(word, vars); ok {
				return resolved
			}
		}
		return value
	}
	for _, stmt := range stmts {
		call := stmt.Cmd.(*syntax.CallExpr)
		if len(call.Args) == 0 {
			if len(stmt.Redirs) != 0 {
				safe = false
			}
			for _, a := range call.Assigns {
				if a.Name == nil {
					continue
				}
				value, ok := "", false
				if relax && top[stmt] && !a.Append && a.Index == nil && a.Array == nil && a.Value != nil {
					value, ok = larkLiteralAssignValue(a.Value, cmd)
				}
				if ok {
					vars[a.Name.Value] = value
				} else {
					delete(vars, a.Name.Value)
				}
			}
			continue
		}
		name, dynamic := executionWord(call.Args[0])
		base := filepath.Base(name)
		if name == "cd" && !dynamic && len(call.Args) == 2 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 {
			to, expanded := executionWord(call.Args[1])
			if to = larkHomePath(to); !filepath.IsAbs(to) && filepath.IsAbs(dir) {
				to = filepath.Join(dir, to)
			}
			if info, err := os.Stat(to); expanded || err != nil || !info.IsDir() || !filepath.IsAbs(to) {
				safe = false
			}
			dir = to
			continue
		}
		// Do not infer safety from an interpreter denylist: build tools and
		// arbitrary programs can execute the file as well. Only these ordinary
		// file/inspection commands and the specified test invocation are inert.
		allowed := map[string]bool{"cat": true, "tee": true, "echo": true, "pwd": true, "ls": true, "head": true, "wc": true}[base] ||
			relax && base == "mkdir" // makes the directories a note is then written in
		if base == "go" && len(call.Args) > 1 && call.Args[1].Lit() == "test" {
			allowed = true
			goTest = true
		}
		if !allowed || name != base || dynamic || dynamicCommandName(name) || len(call.Assigns) != 0 {
			safe = false
		}
		for _, arg := range call.Args[1:] {
			value, expanded := executionWord(arg)
			if expanded && relax {
				if resolved, _, ok := literalLoopWord(arg, vars); ok {
					value, expanded = resolved, false
				}
			}
			option, _, _ := strings.Cut(strings.TrimLeft(value, "-"), "=")
			if expanded || strings.HasPrefix(value, "-") && (option == "exec" || option == "toolexec") {
				safe = false
			}
		}
		for _, redir := range stmt.Redirs {
			if redir.Hdoc == nil {
				if (redir.Op != syntax.RdrOut && redir.Op != syntax.AppOut) || redir.Word == nil || !safeHeredocTarget(path(redir.Word, redir.Word.Lit()), dir, relax) {
					safe = false
				}
				continue
			}
			hasHeredoc = true
			bodies = append(bodies, redir.Hdoc)
			if base != "cat" && base != "tee" {
				safe = false
				continue
			}
			if base == "cat" {
				output := false
				for _, other := range stmt.Redirs {
					if (other.Op == syntax.RdrOut || other.Op == syntax.AppOut) && other.Word != nil && safeHeredocTarget(path(other.Word, other.Word.Lit()), dir, relax) {
						output = true
					}
				}
				if !output || len(call.Args) != 1 {
					safe = false
				}
			} else {
				files := 0
				for _, arg := range call.Args[1:] {
					value := path(arg, arg.Lit())
					if value == "-a" || value == "--append" {
						continue
					}
					if value == "" || strings.HasPrefix(value, "-") || !safeHeredocTarget(value, dir, relax) {
						safe = false
					}
					files++
				}
				if files == 0 {
					safe = false
				}
			}
			syntax.Walk(redir.Hdoc, func(n syntax.Node) bool {
				switch n.(type) {
				case *syntax.CmdSubst, *syntax.ProcSubst:
					safe = false
				}
				return true
			})
		}
	}
	if goTest {
		for _, body := range bodies {
			if !inertGoDocument(body) {
				safe = false
			}
		}
	}
	if hasHeredoc && safe {
		for _, stmt := range stmts {
			redirs := stmt.Redirs[:0]
			for _, r := range stmt.Redirs {
				if r.Hdoc == nil {
					redirs = append(redirs, r)
				}
			}
			stmt.Redirs = redirs
		}
		changed = true
	}
	if !changed {
		if len(resolved) > 0 && (!segPosReliable || !larkResolvedPrefixMatches(prefixStmts, segmentPosition)) {
			clear(resolved)
		}
		return cmd // with the inline rewrites, if any
	}
	var out bytes.Buffer
	if err := syntax.NewPrinter().Print(&out, tree); err != nil {
		if resolved != nil {
			clear(resolved)
		}
		return cmd
	}
	result := out.String()
	if len(resolved) > 0 && (!segPosReliable || !larkResolvedPrefixMatches(prefixStmts, segmentPosition)) {
		clear(resolved)
	}
	return result
}

// Special files, symlinks and virtual filesystems can forward the heredoc to
// a running process. An unknown target is not proof of an ordinary file.
// relax (a bypass conversation) also takes a file in directories that do not exist yet: the write then makes or fails
// on plain directories under the nearest one that exists, which is checked as the parent would be (#15860).
func safeHeredocTarget(name, cwd string, relax bool) bool {
	if name = larkHomePath(name); !safeLiteralCommand(name) {
		return false
	}
	if !filepath.IsAbs(name) {
		if !filepath.IsAbs(cwd) {
			return false
		}
		name = filepath.Join(cwd, name)
	}
	name = filepath.Clean(name)
	parent, err := filepath.EvalSymlinks(filepath.Dir(name))
	if err != nil {
		if !relax {
			return false
		}
		if parent, err = larkMissingParent(filepath.Dir(name)); err != nil {
			return false
		}
	}
	for _, path := range []string{name, filepath.Join(parent, filepath.Base(name))} {
		for _, prefix := range []string{"/dev", "/proc", "/sys"} {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return false
			}
		}
	}
	info, err := os.Lstat(name)
	if os.IsNotExist(err) {
		return true
	}
	return err == nil && info.Mode().IsRegular()
}

// larkMissingParent is where a directory that does not exist would be made: the nearest existing ancestor, with its
// symlinks resolved, joined with the missing rest. That ancestor has to be a directory.
func larkMissingParent(dir string) (string, error) {
	for missing := ""; ; {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			info, err := os.Stat(real)
			if err != nil {
				return "", err
			}
			if !info.IsDir() {
				return "", os.ErrInvalid
			}
			return filepath.Join(real, missing), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		up := filepath.Dir(dir)
		if up == dir {
			return "", err
		}
		missing = filepath.Join(filepath.Base(dir), missing)
		dir = up
	}
}

// go test can execute generated tests and package initializers. Only a document
// containing comments and an optional package clause is inert for this purpose.
func inertGoDocument(body *syntax.Word) bool {
	value, dynamic := executionWord(body)
	if dynamic {
		return false
	}
	content := []byte(value)
	file := token.NewFileSet().AddFile("", -1, len(content))
	var scan scanner.Scanner
	valid := true
	scan.Init(file, content, func(token.Position, string) { valid = false }, scanner.ScanComments)
	packageName := false
	seenPackage := false
	for {
		_, tok, _ := scan.Scan()
		switch tok {
		case token.EOF:
			return valid && !packageName
		case token.COMMENT, token.SEMICOLON:
		case token.PACKAGE:
			if seenPackage || packageName {
				return false
			}
			seenPackage, packageName = true, true
		case token.IDENT:
			if !packageName {
				return false
			}
			packageName = false
		default:
			return false
		}
	}
}

// larkLiteralDirectory tracks only an explicit literal cd whose destination is a known existing directory. An empty
// result disables relative-prefix expansion until another absolute cd; bare cd, options, previous-directory syntax and
// other users' home directories stay unknown.
func larkLiteralDirectory(args []*syntax.Word, currentDir string) string {
	if len(args) != 2 {
		return ""
	}
	to, _ := executionWord(args[1])
	to = larkHomePath(to)
	if !filepath.IsAbs(to) && to != "." && to != ".." && !strings.HasPrefix(to, "./") && !strings.HasPrefix(to, "../") {
		return "" // CDPATH can choose a directory outside currentDir
	}
	if !filepath.IsAbs(to) && currentDir != "" {
		to = filepath.Join(currentDir, to)
	}
	if !filepath.IsAbs(to) {
		return ""
	}
	to = filepath.Clean(to)
	info, err := os.Stat(to)
	if err != nil || !info.IsDir() {
		return ""
	}
	return to
}

// larkNoParentReference rejects every parent component before a relative executable is joined to a tracked directory.
// Without it, symlinked directories could make filepath.Clean differ from the kernel's component-by-component lookup.
func larkNoParentReference(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// larkRelativeGate keeps a newly resolved relative prefix from turning a configured gate command or script into an
// ordinary program merely because the invocation contains a slash. Basenames cover exact commands and script suffixes
// after the prefix is joined to its tracked directory, and deliberately remain conservative across directories.
func larkRelativeGate(gates []proto.CommandGate, name string) bool {
	base := filepath.Base(name)
	for _, gate := range gates {
		if gate.Command != "" && filepath.Base(gate.Command) == base ||
			gate.Script != "" && filepath.Base(gate.Script) == base {
			return true
		}
	}
	return false
}

// These commands cannot execute another program, write a file through an option, or change shell state. Their output
// redirections are checked separately. Keep this list narrower than unchanged: wrappers, interpreters, xargs, tee and
// even general-purpose tools such as find, sed or awk do not belong here.
var larkIndependentReadCommands = map[string]bool{
	"echo": true, ":": true, "true": true, "false": true, "pwd": true,
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true,
	"cut": true, "tr": true, "wc": true, "jq": true,
}

func larkIndependentRead(args []*syntax.Word, words []string, executable int) bool {
	if executable != 0 || !larkIndependentReadCommands[words[executable]] {
		return false
	}
	for i, arg := range args {
		if i != executable && !larkReadOnlyWord(arg) {
			return false
		}
	}
	return true
}

// larkReadOnlyWord permits literal glob/brace syntax and side-effect-free scalar parameters. Actual brace expansion only
// adds read arguments. Command/process substitution, arithmetic, arrays and parameter operators remain opaque. Parsing
// uses the bash dialect, while execution may use zsh: once a word contains a parameter or literal '$', only pure ASCII
// $NAME/${NAME} parameters and the literal set [A-Za-z0-9/._,@%-] are accepted. This conservatively rejects zsh flags,
// subscripts, modifiers and glob qualifiers rather than trying to blacklist each spelling.
func larkReadOnlyWord(word *syntax.Word) bool {
	safe, restricted, literalsSafe := true, false, true
	syntax.Walk(word, func(node syntax.Node) bool {
		if !safe {
			return false
		}
		switch n := node.(type) {
		case nil, *syntax.Word, *syntax.DblQuoted:
			return true
		case *syntax.Lit:
			restricted = restricted || strings.Contains(n.Value, "$")
			literalsSafe = literalsSafe && larkReadOnlyLiteral(n.Value)
			return true
		case *syntax.SglQuoted:
			return false
		case *syntax.ParamExp:
			restricted = true
			safe = larkReadOnlyParameter(n)
			return false
		default:
			safe = false
			return false
		}
	})
	return safe && (!restricted || literalsSafe)
}

func larkReadOnlyParameter(p *syntax.ParamExp) bool {
	if !scalarParameter(p) {
		return false
	}
	name := p.Param.Value
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func larkReadOnlyLiteral(value string) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case '/', '.', '_', ',', '@', '%', '-':
			continue
		}
		return false
	}
	return true
}

// larkBindingRedirects keeps assignments across descriptor duplication and literal /dev/null input/output. Real file
// output, here-documents, inherited input descriptors and dynamic targets retain the old invalidation behavior.
func larkBindingRedirects(redirs []*syntax.Redirect) bool {
	for _, redirect := range redirs {
		if redirect == nil || redirect.Word == nil {
			return false
		}
		// executionWord marks every expansion with '$', so a dynamic target cannot equal an accepted literal below.
		target, _ := executionWord(redirect.Word)
		if redirect.N != nil {
			if redirect.N.Value == "" {
				return false
			}
			for _, r := range redirect.N.Value {
				if r < '0' || r > '9' {
					return false // {NAME} assigns the opened descriptor number to NAME
				}
			}
		}
		switch redirect.Op {
		case syntax.DplOut:
			if target != "1" && target != "2" {
				return false
			}
			if redirect.N != nil && redirect.N.Value != "1" && redirect.N.Value != "2" {
				return false
			}
		case syntax.RdrIn, syntax.RdrOut, syntax.AppOut:
			if target != "/dev/null" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// literalCommandPrefix is the unquoted field splitting of a statically assigned command prefix. Quotes and shell
// metacharacters inside a variable are data at runtime, not syntax to reinterpret; rejecting them here keeps that
// distinction and prevents a value from smuggling in another command, glob or expansion.
func literalCommandPrefix(value string) ([]string, bool) {
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return nil, false
	}
	for _, field := range fields {
		if !safeLiteralCommand(field) {
			return nil, false
		}
	}
	return fields, true
}

func safeLiteralCommand(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._+-", c) {
			continue
		}
		return false
	}
	return true
}

// larkHomePath is a path with a leading ~ (the shell's own home) expanded.
func larkHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}

type larkNormState struct {
	vars       map[string]string
	varsDir    map[string]string
	textVars   map[string]string
	currentDir string
	// moved: a literal cd ran before. It changes nothing an absolute path names, but a relative one, or a bare name with
	// "." or an empty entry in PATH, is looked up from the new directory: only absolute values resolve after it (#8389).
	moved  bool
	unsafe bool
}

func (s larkNormState) clone() larkNormState {
	return larkNormState{
		vars:       maps.Clone(s.vars),
		varsDir:    maps.Clone(s.varsDir),
		textVars:   maps.Clone(s.textVars),
		currentDir: s.currentDir,
		moved:      s.moved,
		unsafe:     s.unsafe,
	}
}

// intersectNormState keeps only facts guaranteed to hold along both execution paths a and b.
func intersectNormState(a, b larkNormState) larkNormState {
	out := larkNormState{
		vars:     map[string]string{},
		varsDir:  map[string]string{},
		textVars: map[string]string{},
		moved:    a.moved || b.moved,
		unsafe:   a.unsafe || b.unsafe,
	}
	if a.currentDir != "" && a.currentDir == b.currentDir {
		out.currentDir = a.currentDir
	} else if a.currentDir != b.currentDir {
		out.moved = true
	}
	for k, v := range a.vars {
		if bv, ok := b.vars[k]; ok && bv == v {
			out.vars[k] = v
			if ad, ok := a.varsDir[k]; ok && ad != "" && b.varsDir[k] == ad {
				out.varsDir[k] = ad
			}
		}
	}
	for k, v := range a.textVars {
		if bv, ok := b.textVars[k]; ok && bv == v {
			out.textVars[k] = v
		}
	}
	return out
}

// larkNormPrefix returns the leading prefix of statements made solely of simple commands, pipelines, and && / || chains.
func larkNormPrefix(in []*syntax.Stmt) []*syntax.Stmt {
	var out []*syntax.Stmt
	for _, s := range in {
		if !larkNormStmtOK(s, false) {
			break
		}
		out = append(out, s)
	}
	return out
}

func larkNormStmtOK(s *syntax.Stmt, inPipe bool) bool {
	if s.Background || s.Negated || s.Coprocess {
		return false
	}
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		return true
	case *syntax.BinaryCmd:
		if len(s.Redirs) != 0 {
			return false
		}
		if c.Op == syntax.Pipe || c.Op == syntax.PipeAll {
			return larkNormStmtOK(c.X, true) && larkNormStmtOK(c.Y, true)
		}
		if !inPipe && (c.Op == syntax.AndStmt || c.Op == syntax.OrStmt) {
			return larkNormStmtOK(c.X, false) && larkNormStmtOK(c.Y, false)
		}
	}
	return false
}

// larkLiteralAssignValue extracts a static assignment value preserving POSIX double-quote backslash
// semantics and rejecting ANSI-C ($'...') and localized ($"...") quotes as well as all expansions (including $?).
func larkLiteralAssignValue(word *syntax.Word, cmd string) (string, bool) {
	if home, ok := larkHomeWord(word, cmd); ok {
		return home, true
	}
	if _, dynamic := executionWord(word); dynamic {
		return "", false
	}
	value, _, ok := literalLoopWord(word, nil)
	return value, ok
}

// larkSingleQuotedParts encodes value as WordParts so syntax.Printer escapes embedded single quotes,
// preventing single-quote escape when printing rewritten script arguments.
func larkSingleQuotedParts(value string) []syntax.WordPart {
	chunks := strings.Split(value, "'")
	parts := make([]syntax.WordPart, 0, len(chunks)*2-1)
	for i, chunk := range chunks {
		if i > 0 {
			parts = append(parts, &syntax.Lit{Value: `\'`})
		}
		if chunk != "" || len(chunks) == 1 {
			parts = append(parts, &syntax.SglQuoted{Value: chunk})
		}
	}
	return parts
}

// larkSimpleRedirectTargets reports whether every redirect target is a plain unquoted word that shellcmd.Split
// reads identically to the shell AST without splitting or truncating on embedded separators or quotes.
func larkSimpleRedirectTargets(redirs []*syntax.Redirect) bool {
	for _, r := range redirs {
		if r == nil || r.Word == nil {
			continue
		}
		for _, part := range r.Word.Parts {
			lit, ok := part.(*syntax.Lit)
			if !ok || strings.ContainsAny(lit.Value, " \t\n|;&<>()'\"\\#") {
				return false
			}
		}
	}
	return true
}

// larkResolvedPrefixMatches verifies a 1-to-1 bijection between AST CallExpr positions in prefixStmts
// and shellcmd.Split segments, preventing segment-count shifts (including cross-statement +k/-k cancellation)
// from assigning resolved-executable provenance to a different segment.
func larkResolvedPrefixMatches(prefixStmts []*syntax.Stmt, want int) bool {
	var buf bytes.Buffer
	if syntax.NewPrinter().Print(&buf, &syntax.File{Stmts: prefixStmts}) != nil {
		return false
	}
	segs, ok := shellcmd.Split(buf.String())
	if !ok || len(segs) != want {
		return false
	}
	matched := true
	var check func(*syntax.Stmt)
	check = func(s *syntax.Stmt) {
		if !matched {
			return
		}
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			var one bytes.Buffer
			if syntax.NewPrinter().Print(&one, s) != nil {
				matched = false
				return
			}
			oneSegs, oneOK := shellcmd.Split(one.String())
			if !oneOK || len(oneSegs) != 1 {
				matched = false
			}
		case *syntax.BinaryCmd:
			check(c.X)
			check(c.Y)
		}
	}
	for _, s := range prefixStmts {
		check(s)
		if !matched {
			return false
		}
	}
	return true
}

// larkScriptWordSafe rejects zsh-only parameter syntax that the bash AST represents as literal parts:
// any unescaped "$" in a Lit (e.g. $~X, $=X, $+X, $^X, $#, $é) and any Lit starting with ":" or "["
// immediately following a ParamExp (zsh parameter modifiers and subscripts).
func larkScriptWordSafe(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	var checkParts func([]syntax.WordPart, bool) bool
	checkParts = func(parts []syntax.WordPart, quoted bool) bool {
		prevParam := false
		for _, part := range parts {
			switch p := part.(type) {
			case *syntax.Lit:
				if prevParam && (strings.HasPrefix(p.Value, ":") || strings.HasPrefix(p.Value, "[")) {
					return false
				}
				if larkHasUnescapedDollar(p.Value, quoted) {
					return false
				}
				prevParam = false
			case *syntax.DblQuoted:
				if p.Dollar || !checkParts(p.Parts, true) {
					return false
				}
				prevParam = false
			case *syntax.SglQuoted:
				if p.Dollar {
					return false
				}
				prevParam = false
			case *syntax.ParamExp:
				prevParam = true
			default:
				return false
			}
		}
		return true
	}
	return checkParts(word.Parts, false)
}

func larkHasUnescapedDollar(s string, quoted bool) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			if !quoted || strings.ContainsRune("\"\\$`\n", rune(s[i+1])) {
				i++
				continue
			}
		}
		if s[i] == '$' {
			return true
		}
	}
	return false
}

// larkNormAssignsSafe rejects any prefix containing dynamic, indexed, append, array, IFS, or executable-environment
// assignments so those mutations cannot invalidate earlier variable inferences.
func larkNormAssignsSafe(stmts []*syntax.Stmt, cmd string) bool {
	safe := true
	var check func(*syntax.Stmt)
	check = func(s *syntax.Stmt) {
		if !safe {
			return
		}
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			for _, a := range c.Assigns {
				if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil {
					safe = false
					return
				}
				if a.Name.Value == "IFS" || larkExecutableEnv[a.Name.Value] {
					safe = false // executable lookup and field splitting invalidate earlier inferences
					return
				}
				if _, ok := larkLiteralAssignValue(a.Value, cmd); !ok {
					safe = false // expansions or special quote forms can alter shell state or decoded bytes
					return
				}
			}
		case *syntax.BinaryCmd:
			check(c.X)
			check(c.Y)
		}
	}
	for _, s := range stmts {
		check(s)
		if !safe {
			return false
		}
	}
	return true
}

func larkPipeUnit(s *syntax.Stmt) []*syntax.Stmt {
	var unit []*syntax.Stmt
	var collect func(*syntax.Stmt)
	collect = func(s *syntax.Stmt) {
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			unit = append(unit, s)
		case *syntax.BinaryCmd:
			collect(c.X)
			collect(c.Y)
		}
	}
	collect(s)
	return unit
}

// literalExecutableParam extracts the variable name when word is a plain scalar parameter expansion ($V, ${V})
// or a double-quoted single scalar expansion ("$V", "${V}"). Double-quoted expansions suppress word splitting, so
// callers must require a single safe token when quoted is true.
func literalExecutableParam(word *syntax.Word) (name string, quoted, ok bool) {
	if word == nil || len(word.Parts) != 1 {
		return "", false, false
	}
	var p *syntax.ParamExp
	switch part := word.Parts[0].(type) {
	case *syntax.ParamExp:
		p = part
	case *syntax.DblQuoted:
		if part.Dollar || len(part.Parts) != 1 {
			return "", false, false
		}
		p, ok = part.Parts[0].(*syntax.ParamExp)
		if !ok {
			return "", false, false
		}
		quoted = true
	default:
		return "", false, false
	}
	if !scalarParameter(p) {
		return "", false, false
	}
	return p.Param.Value, quoted, true
}

// walkNormStmt propagates proven bindings through && and || using two exit states:
// onTrue holds when stmt succeeds (the only entry to the right side of &&),
// onFalse holds when stmt fails (the only entry to the right side of ||).
func walkNormStmt(stmt *syntax.Stmt, state larkNormState, cmd string, gates []proto.CommandGate, resolved map[int]bool, segPos *int, segPosReliable *bool, changed *bool) (onTrue, onFalse larkNormState) {
	if b, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		switch b.Op {
		case syntax.AndStmt:
			xTrue, xFalse := walkNormStmt(b.X, state, cmd, gates, resolved, segPos, segPosReliable, changed)
			yTrue, yFalse := walkNormStmt(b.Y, xTrue, cmd, gates, resolved, segPos, segPosReliable, changed)
			return yTrue, intersectNormState(xFalse, yFalse)
		case syntax.OrStmt:
			xTrue, xFalse := walkNormStmt(b.X, state, cmd, gates, resolved, segPos, segPosReliable, changed)
			yTrue, yFalse := walkNormStmt(b.Y, xFalse, cmd, gates, resolved, segPos, segPosReliable, changed)
			return intersectNormState(xTrue, yTrue), yFalse
		}
	}
	before := state.clone()
	after, wasCD := applyNormUnit(larkPipeUnit(stmt), state, cmd, gates, resolved, segPos, segPosReliable, changed)
	if wasCD {
		// If cd failed (entering the right arm of ||), do not assume the target directory was entered.
		before.moved = true
		before.currentDir = ""
		before.unsafe = after.unsafe
		return after, before
	}
	return after, after.clone()
}

func applyNormUnit(unit []*syntax.Stmt, state larkNormState, cmd string, gates []proto.CommandGate, resolved map[int]bool, segPos *int, segPosReliable *bool, changed *bool) (larkNormState, bool) {
	wasCD := false
	for _, stmt := range unit {
		position := *segPos
		*segPos++
		if !larkSimpleRedirectTargets(stmt.Redirs) {
			*segPosReliable = false
		}
		if state.unsafe {
			continue
		}
		call := stmt.Cmd.(*syntax.CallExpr)
		if len(call.Args) == 0 {
			if len(unit) > 1 || !larkBindingRedirects(stmt.Redirs) {
				state.unsafe = true // an assignment in a pipe or with non-binding redirects sets nothing safe for later commands
				continue
			}
			for _, a := range call.Assigns {
				if a.Name == nil {
					continue
				}
				delete(state.vars, a.Name.Value)
				delete(state.varsDir, a.Name.Value)
				delete(state.textVars, a.Name.Value)
				if a.Append || a.Index != nil || a.Array != nil || a.Value == nil {
					continue
				}
				if value, ok := larkLiteralAssignValue(a.Value, cmd); ok {
					state.textVars[a.Name.Value] = value
					if _, ok := literalCommandPrefix(value); ok {
						state.vars[a.Name.Value] = value
						state.varsDir[a.Name.Value] = state.currentDir
					}
				}
			}
			continue
		}
		words := make([]string, len(call.Args))
		for i, word := range call.Args {
			words[i], _ = executionWord(word)
		}
		suffix, ok := unwrapCommand(words)
		if !ok || len(suffix) == 0 {
			state.unsafe = true
			continue // unknown wrapper form or wrapper without target command must not propagate bindings
		}
		resolvedAbsolute := false
		index := len(words) - len(suffix)
		base := filepath.Base(suffix[0])
		scriptArguments := commandShells[strings.TrimRight(base, "0123456789.")] || base == "ssh"
		type scriptReplacement struct {
			index int
			value string
		}
		var scriptReplacements []scriptReplacement
		var replacedArgs map[int]bool
		if scriptArguments {
			for i := index + 1; i < len(call.Args); i++ {
				arg := call.Args[i]
				if !larkScriptWordSafe(arg) {
					continue
				}
				value, replaced, literal := literalLoopWord(arg, state.vars)
				if !literal || !replaced {
					// Text bindings also cover literal script fragments. Unquoted field splitting rules still
					// reject a multiword value; a quoted expansion stays one word.
					value, replaced, literal = literalLoopWord(arg, state.textVars)
				}
				if !literal || !replaced {
					continue
				}
				if replacedArgs == nil {
					replacedArgs = map[int]bool{}
				}
				scriptReplacements = append(scriptReplacements, scriptReplacement{index: i, value: value})
				replacedArgs[i] = true
			}
		}
		dynamicArgs := false
		argsReadOnly := true
		for i, arg := range call.Args {
			if i == index || replacedArgs[i] {
				continue
			}
			_, dynamic := executionWord(arg)
			if dynamic || wordExpandsToMany(arg) {
				dynamicArgs = true
				if !larkReadOnlyWord(arg) {
					argsReadOnly = false
					break
				}
			}
		}
		if dynamicArgs && !argsReadOnly {
			state.unsafe = true
			continue // do not mutate or resolve when another argument can mutate shell state
		}
		// Commit script argument replacements only after all sibling arguments passed side-effect validation.
		for _, r := range scriptReplacements {
			call.Args[r.index].Parts = larkSingleQuotedParts(r.value)
			words[r.index], *changed = r.value, true
		}
		if paramName, quoted, ok := literalExecutableParam(call.Args[index]); ok {
			if value, ok := state.vars[paramName]; ok {
				var prefix []string
				var literal bool
				if quoted {
					if safeLiteralCommand(value) {
						prefix, literal = []string{value}, true
					}
				} else {
					prefix, literal = literalCommandPrefix(value)
				}
				// Reject leading "-" so a variable cannot inject a wrapper option (e.g. `env "$V" python3 ...`)
				// and inherit resolved-executable provenance for the command that follows it.
				if literal && strings.HasPrefix(prefix[0], "-") {
					literal = false
				}
				resolvable := literal && (!state.moved || filepath.IsAbs(prefix[0]))
				// System binary directories have special filter semantics downstream; keep relative prefixes opaque there.
				if literal && state.moved && !filepath.IsAbs(prefix[0]) && strings.HasPrefix(prefix[0], "./") &&
					larkNoParentReference(prefix[0]) && state.currentDir != "" && state.varsDir[paramName] == state.currentDir &&
					filepath.Dir(filepath.Join(state.currentDir, prefix[0])) != "/bin" && filepath.Dir(filepath.Join(state.currentDir, prefix[0])) != "/usr/bin" &&
					!isLarkCLI(prefix[0]) && !larkRelativeGate(gates, prefix[0]) {
					prefix[0] = filepath.Join(state.currentDir, prefix[0])
					resolvable = true
				}
				if resolvable {
					expanded := make([]*syntax.Word, 0, len(call.Args)+len(prefix)-1)
					expanded = append(expanded, call.Args[:index]...)
					for _, field := range prefix {
						expanded = append(expanded, &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: field}}})
					}
					expanded = append(expanded, call.Args[index+1:]...)
					call.Args = expanded
					if resolved != nil && *segPosReliable {
						resolved[position] = true
					}
					resolvedAbsolute = filepath.IsAbs(prefix[0]) && (index == 0 || filepath.Base(words[0]) == "timeout")
					*changed = true
				}
			}
		}
		if dynamicArgs && !larkIndependentRead(call.Args, words, index) {
			state.unsafe = true
			continue // do not propagate bindings past a non-independent dynamic invocation
		}
		// Only commands that cannot change the parent shell's state can take part. In a pipe that is its last
		// command (zsh runs it in the shell itself); the others run apart.
		name := words[0]
		unchanged := resolvedAbsolute || stmt != unit[len(unit)-1] || map[string]bool{"echo": true, ":": true, "true": true, "false": true, "ls": true, "git": true, "go": true, "pwd": true, "date": true, "sleep": true, "cat": true, "tee": true, "head": true, "tail": true, "cut": true, "sort": true, "uniq": true, "tr": true, "wc": true, "grep": true, "jq": true, "timeout": true, "env": true, "nice": true, "nohup": true, "setsid": true, "stdbuf": true, "xargs": true, "sudo": true, "ssh": true}[name]
		if name == "cd" && len(unit) == 1 && len(call.Args) <= 2 && len(call.Assigns) == 0 && len(stmt.Redirs) == 0 {
			literal := true
			for _, arg := range call.Args {
				_, dynamic := executionWord(arg)
				literal = literal && !dynamic && !wordExpandsToMany(arg)
			}
			unchanged, state.moved = literal, state.moved || literal
			if literal {
				state.currentDir = larkLiteralDirectory(call.Args, state.currentDir)
				wasCD = true
			}
		}
		if !unchanged {
			state.unsafe = true
			continue
		}
		if len(call.Assigns) != 0 || !larkBindingRedirects(stmt.Redirs) {
			clear(state.vars)
			clear(state.varsDir)
			clear(state.textVars)
		}
	}
	return state, wasCD
}

// larkFlatStmts lists the simple commands of a `;`, `&&` and `||` list, in order; false for anything else (pipes,
// loops, subshells, background jobs).
func larkFlatStmts(in []*syntax.Stmt) ([]*syntax.Stmt, bool) {
	var out []*syntax.Stmt
	var add func(*syntax.Stmt) bool
	add = func(s *syntax.Stmt) bool {
		if s.Background || s.Negated || s.Coprocess {
			return false
		}
		switch c := s.Cmd.(type) {
		case *syntax.CallExpr:
			out = append(out, s)
			return true
		case *syntax.BinaryCmd:
			return (c.Op == syntax.AndStmt || c.Op == syntax.OrStmt) && len(s.Redirs) == 0 && add(c.X) && add(c.Y)
		}
		return false
	}
	for _, s := range in {
		if !add(s) {
			return nil, false
		}
	}
	return out, true
}
