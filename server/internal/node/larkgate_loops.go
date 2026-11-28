package node

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"mvdan.cc/sh/v3/syntax"
)

// literalLarkLoopCommands binds each literal for-list value for classification
// only. It never executes shell code. Validate the whole tree before returning
// any commands: a later mutation or an opaque outer loop invalidates all bindings.
func literalLarkLoopCommands(cmd string, gates []proto.CommandGate) ([]string, bool) {
	if !strings.Contains(cmd, "for") {
		return nil, false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return nil, false
	}
	vars := map[string]string{}
	var commands []string
	resolved := false
	loops, steps, size := 0, 0, 0
	var visit func(*syntax.Stmt, bool) bool
	visit = func(stmt *syntax.Stmt, top bool) bool {
		steps++
		// Bound Cartesian products of nested lists and duplicated command text.
		if steps > 256 || stmt.Background || stmt.Coprocess || stmt.Negated {
			return false
		}
		if _, simple := stmt.Cmd.(*syntax.CallExpr); !simple && len(stmt.Redirs) > 0 {
			return false
		}
		switch n := stmt.Cmd.(type) {
		case *syntax.ForClause:
			list, ok := n.Loop.(*syntax.WordIter)
			if !ok || n.Select || !list.InPos.IsValid() || len(list.Items) == 0 || list.Name == nil {
				return false
			}
			name := list.Name.Value
			if _, shadow := vars[name]; shadow || larkShellManaged(name) || larkExecutableEnv[name] {
				return false // the shell rewrites a managed name (cd, =~, RANDOM) instead of retaining the binding
			}
			values := make([]string, len(list.Items))
			for i, item := range list.Items {
				value, _, ok := literalLoopWord(item, nil)
				if !ok || strings.ContainsAny(value, "$`*?[]{}") || wordExpandsToMany(item) {
					return false
				}
				values[i] = value
			}
			loops++
			for _, value := range values {
				vars[name] = value
				for _, body := range n.Do {
					if !visit(body, false) {
						return false
					}
				}
			}
			delete(vars, name) // later uses need their own proven binding
			return true
		case *syntax.BinaryCmd:
			if n.Op == syntax.AndStmt || n.Op == syntax.OrStmt {
				// The right arm may be skipped. An assignment there must not replace a binding used by a later loop:
				// the old value (or no value) remains reachable, so the expansion stays conservative.
				return visit(n.X, top) && visit(n.Y, false)
			}
			return visit(n.X, false) && visit(n.Y, false) // a pipe's assignments do not reach the parent shell
		case *syntax.Block:
			for _, body := range n.Stmts {
				if !visit(body, false) {
					return false
				}
			}
			return true
		case *syntax.IfClause:
			for clause := n; clause != nil; clause = clause.Else {
				for _, body := range append(append([]*syntax.Stmt{}, clause.Cond...), clause.Then...) {
					if !visit(body, false) {
						return false
					}
				}
			}
			return true
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				if !top || len(stmt.Redirs) != 0 || !bindLiteralLoopAssignments(n.Assigns, vars, cmd) {
					return false
				}
				return true
			}
			if !literalLoopEnvAssignments(n.Assigns) {
				return false
			}
			words := make([]string, len(n.Args))
			bound := make([]bool, len(n.Args))
			for i, word := range n.Args {
				var ok bool
				words[i], bound[i], ok = literalLoopWord(word, vars)
				if !ok || isAssignment(words[i]) {
					return false
				}
			}
			// Locate the executable before resolving it: a variable must never
			// become a wrapper, shell, CLI or gate, even if its arguments look safe.
			original := make([]string, len(n.Args))
			for i, word := range n.Args {
				original[i], _ = executionWord(word)
			}
			rest, ok := unwrapCommand(original)
			if !ok || len(rest) == 0 {
				return false
			}
			index := len(original) - len(rest)
			name := words[index]
			base := filepath.Base(name)
			gated := gateOf(gates, name) >= 0 || isLarkCLI(name)
			if bound[index] {
				_, wrapper := commandWrappers[base]
				if !safeLiteralCommand(strings.TrimPrefix(name, "~/")) || gated || gateOf(gates, base) >= 0 || wrapper ||
					commandShells[strings.TrimRight(base, "0123456789.")] || indirectLarkCommand(base) {
					return false
				}
			}
			// A variable method remains unknown, matching the existing gate policy.
			if gated && index+1 < len(bound) && bound[index+1] {
				return false
			}
			if gateOf(gates, name) >= 0 && (index+1 >= len(words) || !strings.EqualFold(words[index+1], "GET") && !strings.EqualFold(words[index+1], "HEAD")) {
				return false // preserve the existing approval/denial path for explicit writes
			}
			resolved = resolved || bound[index]
			if gated {
				for _, changed := range bound[index+1:] {
					resolved = resolved || changed
				}
			}
			// These commands cannot mutate a binding in the parent shell. Unknown
			// builtins/functions and all assignment/declaration forms fall back.
			ordinary := map[string]bool{"[": true, ":": true, "false": true, "cd": true, "ls": true, "git": true, "go": true, "pwd": true, "sleep": true, "cat": true, "tee": true, "python": true, "python3": true, "sha256sum": true}[name] ||
				isLarkFilter(words[index:]) && name != "printf" && name != "cd"
			external := (filepath.IsAbs(name) || strings.HasPrefix(name, "~/")) && !commandShells[base] && !indirectLarkCommand(base) && isSafeFilterInvocation(base, words[index+1:])
			if !gated && !ordinary && !external {
				return false
			}
			type replacement struct {
				start, end int
				text       string
			}
			var replacements []replacement
			valid := true
			envValues := map[*syntax.Word]bool{}
			for _, a := range n.Assigns {
				envValues[a.Value] = true
			}
			syntax.Walk(stmt, func(node syntax.Node) bool {
				if r, ok := node.(*syntax.Redirect); ok && r.Hdoc != nil {
					// A literal heredoc is preserved byte for byte. Expansion in an unquoted body makes the whole
					// binding attempt opaque; a quoted body is data and needs no variable replacement.
					_, dynamic := executionWord(r.Hdoc)
					valid = valid && !dynamic
					return false
				}
				word, ok := node.(*syntax.Word)
				if !ok {
					return valid
				}
				if envValues[word] {
					return false // invocation-local environment values stay for the nested classifier
				}
				value, changed, ok := literalLoopWord(word, vars)
				if !ok {
					valid = false
				} else if changed {
					quoted, err := syntax.Quote(value, syntax.LangBash)
					valid = valid && err == nil
					replacements = append(replacements, replacement{int(word.Pos().Offset()), int(word.End().Offset()), quoted})
				}
				return false
			})
			if !valid {
				return false
			}
			sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
			var out strings.Builder
			at := int(stmt.Pos().Offset())
			for _, r := range replacements {
				out.WriteString(cmd[at:r.start])
				out.WriteString(r.text)
				at = r.end
			}
			out.WriteString(cmd[at:stmt.End().Offset()])
			size += out.Len()
			if size > 256*1024 {
				return false
			}
			commands = append(commands, out.String())
			return true
		default:
			return false
		}
	}
	for _, stmt := range tree.Stmts {
		if !visit(stmt, true) {
			return nil, false
		}
	}
	return commands, loops > 0 && resolved
}

// bindLiteralLoopAssignments records only top-level scalar assignments whose value is one literal executable word.
// They have the same lifetime as a literal for-list binding. Anything dynamic or multiword stays for the normal gate.
func bindLiteralLoopAssignments(assigns []*syntax.Assign, vars map[string]string, cmd string) bool {
	if len(assigns) == 0 {
		return false
	}
	for _, a := range assigns {
		if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil || larkShellManaged(a.Name.Value) || larkExecutableEnv[a.Name.Value] {
			return false
		}
		value, dynamic := executionWord(a.Value)
		if home, ok := larkHomeWord(a.Value, cmd); ok {
			value, dynamic = home, false
		}
		if dynamic || !safeLiteralCommand(value) {
			return false
		}
		vars[a.Name.Value] = value
	}
	return true
}

// literalLoopEnvAssignments permits environment prefixes on one invocation without treating them as persistent
// bindings. Their values are classified normally, including any nested command; executable-loading variables and
// IFS remain opaque because they can change what the expanded words mean.
func literalLoopEnvAssignments(assigns []*syntax.Assign) bool {
	for _, a := range assigns {
		if a.Name == nil || a.Append || a.Index != nil || a.Array != nil || a.Value == nil || larkShellManaged(a.Name.Value) || larkExecutableEnv[a.Name.Value] {
			return false
		}
	}
	return true
}

// literalLoopWord removes shell quotes and substitutes only plain scalar loop
// bindings. An unquoted binding must remain one nonempty, non-globbing word.
func literalLoopWord(word *syntax.Word, vars map[string]string) (string, bool, bool) {
	var out strings.Builder
	changed := false
	var parts func([]syntax.WordPart, bool) bool
	parts = func(items []syntax.WordPart, quoted bool) bool {
		for _, item := range items {
			switch p := item.(type) {
			case *syntax.Lit:
				for i := 0; i < len(p.Value); i++ {
					c := p.Value[i]
					if c == '\\' && i+1 < len(p.Value) && (!quoted || strings.ContainsRune("\"\\$`\n", rune(p.Value[i+1]))) {
						i++
						c = p.Value[i]
						if c == '\n' {
							continue
						}
					}
					out.WriteByte(c)
				}
			case *syntax.SglQuoted:
				if p.Dollar {
					return false
				}
				out.WriteString(p.Value)
			case *syntax.DblQuoted:
				if p.Dollar || !parts(p.Parts, true) {
					return false
				}
			case *syntax.ParamExp:
				if !scalarParameter(p) {
					return false
				}
				if p.Param.Value == "?" {
					// `echo "$d exit=$?"` (#11193): the last exit status is always a number, set by the shell, never by
					// the command, so it cannot name a program or a lark-cli word. Kept as text: a word holding it is
					// not a literal the classifier can read as a command, a method or a path.
					out.WriteString("$?")
					continue
				}
				value, ok := vars[p.Param.Value]
				if !ok || !quoted && !safeLiteralCommand(strings.TrimPrefix(value, "~/")) {
					return false
				}
				out.WriteString(value)
				changed = true
			default:
				return false
			}
		}
		return true
	}
	ok := parts(word.Parts, false)
	// A neighboring glob or brace must not become quoted by replacement.
	if changed && braceExpansion(word) {
		ok = false
	}
	for _, part := range word.Parts {
		if lit, isLit := part.(*syntax.Lit); changed && isLit && strings.ContainsAny(lit.Value, "*?[") {
			ok = false
		}
	}
	return out.String(), changed, ok
}

// larkFileReadLoops: the command has a `for` loop over a command substitution, and every such loop is one the gate
// can read as reads (#15860). Its list is the words of files the command names (`$(cat file…)`, `$(< file)`, either
// piped only through output filters), and its body only runs known lark-cli reads, piped only into output filters and
// redirected only to files (larkLoopReadStmt). The loop variable, like any expansion there, can only be the whole
// double-quoted value of a flag (larkLoopCLIArgs). Anything else (another program, a nested loop, eval, a
// substitution in the body, a variable in a command word, flag name or --as) leaves the loop opaque, as before.
// Nothing here runs a command.
func larkFileReadLoops(cmd string, risk larkRisk) bool {
	if !strings.Contains(cmd, "for") {
		return false
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	loops, ok := 0, true
	syntax.Walk(tree, func(node syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := node.(type) {
		case *syntax.CallExpr:
			// A quoted `"for"` is a command the classifier's segments cannot tell from a loop's header.
			if len(n.Args) > 0 {
				if name, _ := executionWord(n.Args[0]); larkShellKeywords[name] {
					ok = false
				}
			}
		case *syntax.ForClause:
			list, words := n.Loop.(*syntax.WordIter)
			if !words || !larkWordsExpand(list.Items) {
				return true // a literal list: literalLarkLoopCommands or the usual reading
			}
			loops++
			ok = larkFileReadLoop(n, list, risk)
			return false // its body is read whole above
		}
		return true
	})
	return ok && loops > 0
}

// larkShellKeywords open or close a compound command: a segment starting with one is part of it.
var larkShellKeywords = map[string]bool{"for": true, "in": true, "do": true, "done": true, "while": true, "until": true,
	"if": true, "then": true, "elif": true, "else": true, "fi": true, "case": true, "esac": true, "select": true, "function": true}

// larkWordsExpand: a word runs something or does arithmetic as it expands.
func larkWordsExpand(words []*syntax.Word) bool {
	found := false
	for _, w := range words {
		syntax.Walk(w, func(node syntax.Node) bool {
			switch node.(type) {
			case *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ArithmExp:
				found = true
			}
			return !found
		})
	}
	return found
}

// larkFileReadLoop is one loop larkFileReadLoops accepts.
func larkFileReadLoop(n *syntax.ForClause, list *syntax.WordIter, risk larkRisk) bool {
	if n.Select || list.Name == nil || len(list.Items) != 1 || len(n.Do) == 0 {
		return false
	}
	if name := list.Name.Value; name == "IFS" || name == larkProfileEnvName || larkExecutableEnv[name] {
		return false
	}
	item := list.Items[0]
	if len(item.Parts) != 1 {
		return false
	}
	sub, ok := item.Parts[0].(*syntax.CmdSubst)
	if !ok || len(sub.Stmts) != 1 || !larkFileWordsSource(sub.Stmts[0]) {
		return false
	}
	for _, stmt := range n.Do {
		if !larkLoopReadStmt(stmt, risk) {
			return false
		}
	}
	return true
}

// larkPipeline is a statement's pipeline, in order; nil for anything else (`|&`, a list, a negation, background).
func larkPipeline(stmt *syntax.Stmt) []*syntax.Stmt {
	if stmt.Background || stmt.Negated || stmt.Coprocess {
		return nil
	}
	if b, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		if b.Op != syntax.Pipe || len(stmt.Redirs) != 0 {
			return nil
		}
		x, y := larkPipeline(b.X), larkPipeline(b.Y)
		if x == nil || y == nil {
			return nil
		}
		return append(x, y...)
	}
	return []*syntax.Stmt{stmt}
}

// larkFileWordsSource: a loop's list reads files only: `cat file…` or `< file` (larkFileReadSubstitution, #9101),
// piped only through output filters with literal words.
func larkFileWordsSource(stmt *syntax.Stmt) bool {
	pipe := larkPipeline(stmt)
	if len(pipe) == 0 {
		return false
	}
	var text strings.Builder
	if syntax.NewPrinter().Print(&text, pipe[0]) != nil || !larkFileReadSubstitution(text.String()) {
		return false
	}
	for _, s := range pipe[1:] {
		if _, ok := larkLiteralFilter(s); !ok {
			return false
		}
	}
	return true
}

// larkLiteralFilter: a statement is one output filter (larkFilters but cd) with literal words and no assignment or
// redirection; its words.
func larkLiteralFilter(s *syntax.Stmt) ([]string, bool) {
	call, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 || len(s.Redirs) != 0 {
		return nil, false
	}
	words := make([]string, len(call.Args))
	for i, arg := range call.Args {
		value, dynamic := executionWord(arg)
		if dynamic {
			return nil, false
		}
		words[i] = value
	}
	return words, larkFilters[words[0]] && words[0] != "cd"
}

// larkLoopReadStmt is one statement of a file-read loop's body: a known lark-cli read (larkLoopCLIRead), optionally
// under `timeout`, piped only into output filters (larkLoopFilter); or an output filter alone. Every part writes only
// to files named from the current directory down or to another descriptor (larkLoopRedirects).
func larkLoopReadStmt(stmt *syntax.Stmt, risk larkRisk) bool {
	pipe := larkPipeline(stmt)
	if len(pipe) == 0 {
		return false
	}
	for i, s := range pipe {
		call, ok := s.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 || len(call.Assigns) != 0 || !larkLoopRedirects(s.Redirs) {
			return false
		}
		for _, arg := range call.Args {
			if larkWordsExpand([]*syntax.Word{arg}) || !larkLoopParameters(arg) {
				return false
			}
		}
		if !(i == 0 && larkLoopCLIRead(call, risk) || larkLoopFilter(call, i > 0)) {
			return false
		}
	}
	return true
}

// larkLoopParameters: every expansion in a word is a plain scalar variable ($t, ${t}).
func larkLoopParameters(word *syntax.Word) bool {
	ok := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch p := node.(type) {
		case *syntax.ParamExp:
			ok = ok && scalarParameter(p)
		case *syntax.ExtGlob, *syntax.BraceExp:
			ok = false
		}
		return ok
	})
	return ok
}

// larkLoopRedirects: output goes to /dev/null, another descriptor or a file: never one a variable starts the path of
// (only the literal text before the first expansion says where it is), one in /dev, /proc or /sys (where bash's
// /dev/tcp sends what it is given), or one past "..". Nothing is read from a file.
func larkLoopRedirects(redirs []*syntax.Redirect) bool {
	for _, r := range redirs {
		if r.Hdoc != nil || r.Word == nil || r.N != nil && r.N.Value != "1" && r.N.Value != "2" {
			return false
		}
		value, dynamic := executionWord(r.Word)
		switch r.Op {
		case syntax.DplOut:
			if dynamic || value != "1" && value != "2" {
				return false
			}
		case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut:
			if value == "/dev/null" && !dynamic {
				continue
			}
			prefix, _, _ := strings.Cut(value, "$") // executionWord writes each expansion as "$"
			if prefix == "" || strings.HasPrefix(prefix, "-") || strings.Contains(value, "..") {
				return false
			}
			if root := strings.TrimPrefix(prefix, "~"); strings.HasPrefix(root, "/") {
				top, _, named := strings.Cut(root[1:], "/")
				if !named && dynamic || top == "" || top == "dev" || top == "proc" || top == "sys" {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// larkLoopFilter: an output filter (larkFilters but cd, which moves later relative paths) or sed with a script that
// only prints (sedScriptReadsOnly), reading its input when piped. A variable may only stand in echo's and printf's
// double-quoted words, which they print; every other filter's words are literal.
func larkLoopFilter(call *syntax.CallExpr, piped bool) bool {
	words := make([]string, len(call.Args))
	dynamic := false
	for i, arg := range call.Args {
		var d bool
		words[i], d = executionWord(arg)
		if d && i == 0 {
			return false
		}
		if d {
			for _, part := range arg.Parts {
				if _, quoted := part.(*syntax.DblQuoted); !quoted {
					return false
				}
			}
			dynamic = true
		}
	}
	switch name := words[0]; {
	case name == "echo" || name == "printf":
		return true
	case dynamic:
		return false
	case name == "sed":
		// `sed [-n|-E|-r…] SCRIPT` or `… -e SCRIPT` and nothing more: another word and sed reads (or, -i, edits) a file.
		rest := words[1:]
		for len(rest) > 0 && (rest[0] == "-n" || rest[0] == "-E" || rest[0] == "-r") {
			rest = rest[1:]
		}
		if len(rest) == 2 && rest[0] == "-e" {
			rest = rest[1:]
		}
		return piped && len(rest) == 1 && !strings.HasPrefix(rest[0], "-") && sedScriptReadsOnly(rest[0])
	default:
		return larkFilters[name] && name != "cd"
	}
}

// larkLoopSensitiveFlags never take a loop's word: who the call acts as, its method, its profile, where its output
// goes and the program that formats it.
var larkLoopSensitiveFlags = map[string]bool{"--as": true, "--method": true, "--request": true, "--profile": true,
	"--output": true, "--jq": true, "--format": true, "--dry-run": true, "--help": true}

// larkLoopCLIRead: a lark-cli call, alone or under `timeout [options] DURATION`, that only reads (cliRead, larkRead,
// larkBypassSafe; not made one by --dry-run or --help) with every expansion as the whole double-quoted value of a
// flag (larkLoopCLIArgs).
func larkLoopCLIRead(call *syntax.CallExpr, risk larkRisk) bool {
	words := make([]string, len(call.Args))
	for i, arg := range call.Args {
		words[i], _ = executionWord(arg)
	}
	at := 0
	if words[0] == "timeout" {
		for at = 1; at < len(words) && !isLarkCLI(words[at]); at++ {
			if !strings.HasPrefix(words[at], "-") && !larkDuration.MatchString(words[at]) {
				return false
			}
		}
	}
	if at >= len(words) || !isLarkCLI(words[at]) || !safeLiteralCommand(words[at]) {
		return false
	}
	for _, arg := range call.Args[:at+1] {
		if _, dynamic := executionWord(arg); dynamic {
			return false
		}
	}
	args := call.Args[at+1:]
	if !larkLoopCLIArgs(args) {
		return false
	}
	c, deny, ok := parseLarkCLI(words[at+1:], risk)
	return ok && deny == "" && c.cliRead() && larkRead(c) && larkBypassSafe(c) &&
		!flagOn(words[at+1:], "--dry-run", "-h", "--help")
}

// larkDuration is timeout's duration.
var larkDuration = lazyre.New(`^[0-9]+(\.[0-9]+)?[smhd]?$`)

// larkLoopCLIArgs: lark-cli's words, read as pflag reads them, put an expansion only in a flag's value: the whole
// double-quoted word after a long flag that is neither sensitive (larkLoopSensitiveFlags) nor known to take no value
// (larkCLIBoolFlags), or the double-quoted part of `--flag="…"`. Never in a leading option, a command word, a flag's
// name or past `--`.
func larkLoopCLIArgs(args []*syntax.Word) bool {
	values := make([]string, len(args))
	dynamic := make([]bool, len(args))
	for i, arg := range args {
		values[i], dynamic[i] = executionWord(arg)
	}
	i := 0
	// Leading options parseLarkCLI takes before the command words, with their values.
	for i < len(args) && strings.HasPrefix(values[i], "-") {
		name, _, inline := strings.Cut(values[i], "=")
		if !strings.Contains(" --as --format -q --jq -o --output ", " "+name+" ") {
			break
		}
		if dynamic[i] {
			return false
		}
		i++
		if !inline {
			if i == len(args) || dynamic[i] {
				return false
			}
			i++
		}
	}
	// Command words.
	for ; i < len(args) && !strings.HasPrefix(values[i], "-"); i++ {
		if dynamic[i] {
			return false
		}
	}
	valueOf := "" // the flag the next word is the value of
	for ; i < len(args); i++ {
		if valueOf != "" {
			if dynamic[i] && !(larkLoopValueFlag(valueOf) && larkDoubleQuoted(args[i].Parts)) {
				return false
			}
			valueOf = ""
			continue
		}
		if dynamic[i] {
			lit, ok := args[i].Parts[0].(*syntax.Lit)
			if !ok || len(args[i].Parts) < 2 {
				return false
			}
			name, rest, inline := strings.Cut(lit.Value, "=")
			if !inline || rest != "" || !larkLoopValueFlag(name) || !larkDoubleQuoted(args[i].Parts[1:]) {
				return false
			}
			continue
		}
		if values[i] == "--" {
			return !slices.Contains(dynamic[i:], true)
		}
		if !strings.HasPrefix(values[i], "-") {
			continue // a literal argument
		}
		name, _, inline := strings.Cut(values[i], "=")
		if !inline && !larkCLIBoolFlags[name] {
			valueOf = name
		}
	}
	return true
}

// larkLoopValueFlag: a long flag a loop's word may be the value of.
func larkLoopValueFlag(name string) bool {
	return strings.HasPrefix(name, "--") && len(name) > 2 && !larkLoopSensitiveFlags[name] && safeLiteralCommand(name)
}

// larkDoubleQuoted: parts are double-quoted text, so an expansion in them stays one word and is never a glob.
func larkDoubleQuoted(parts []syntax.WordPart) bool {
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if _, ok := part.(*syntax.DblQuoted); !ok {
			return false
		}
	}
	return true
}
