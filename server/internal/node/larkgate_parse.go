package node

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/textfmt"
	"mvdan.cc/sh/v3/syntax"
)

var (
	larkFilters = map[string]bool{"jq": true, "head": true, "tail": true, "grep": true, "egrep": true, "wc": true, "sort": true, "uniq": true, "cut": true, "tr": true, "column": true,
		// and commands that only print
		"echo": true, "printf": true, "true": true, "date": true, "seq": true, "which": true, "type": true, "cd": true}
	// curl flags a gated command passes on that cannot leak the token or reach another host, with whether they take a value.
	gateCurlFlags = map[string]bool{"--url-query": true, "-G": false, "--get": false, "--data-urlencode": true, "-d": true, "--data": true,
		"--data-raw": true, "-o": true, "--output": true, "-i": false, "--include": false, "-m": true, "--max-time": true, "-s": false,
		"-S": false, "-f": false, "--compressed": false, "-H": true, "--header": true, "--upload": true, "-X": true, "--request": true}
)

// classifyLark reads a Bash command against the command gates (curl / wget aimed at one's API is refused); cwd
// resolves @file bodies and query inputs; risk supplies CLI metadata when configured. Known read commands also have a
// static fallback. A PATH assignment may name only directories on this process's PATH and the system ones; larkCheck
// uses classifyLarkWith, which also knows the login shell's (larkExtraPATH).
func classifyLark(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk) larkVerdict {
	return classifyLarkWith(cmd, gates, cwd, risk, nil)
}

// larkExtraPATH supplies directories a PATH assignment may name besides this process's PATH and the system ones: the
// login shell's, the PATH the assistant's Bash resolves programs in (larkLoginPATH). nil: none. It is called only for
// a command that assigns PATH.
type larkExtraPATH func() []string

// classifyLarkWith is classifyLark for a caller with a login shell in view.
func classifyLarkWith(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, extraPATH larkExtraPATH) larkVerdict {
	return classifyLarkRelaxed(cmd, gates, cwd, risk, extraPATH, false)
}

// classifyLarkRelaxed is classifyLarkWith for a conversation whose permission mode lets classified calls through
// (relax: an assistant conversation in bypassPermissions, larkCheck). It also reads, as the usual reading cannot, a
// loop over a file's words that only runs lark-cli reads (larkFileReadLoops) and a note written through a literal
// path variable (normalizeLarkCommandRelaxed, larkWritesFileVars), #15860. Every other mode keeps the usual reading.
func classifyLarkRelaxed(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, extraPATH larkExtraPATH, relax bool) larkVerdict {
	v := classifyLarkDepth(cmd, gates, cwd, risk, extraPATH, 0, relax)
	// A relative @file body or query input is read from cwd at check time. Whatever else the command runs may run
	// the call elsewhere (cd in any spelling, env -C, sudo -D, a subshell) or change the file first (a redirect, an
	// earlier command), so its contents are known only for a lone call (larkLoneGateCall, #8404).
	if slices.ContainsFunc(v.calls, func(c larkCall) bool { return c.relFile }) && !larkLoneGateCall(cmd, gates) {
		v.other, v.unknown = true, true
	}
	return v
}

// larkLoneGateCall: the command is one gated call with literal words and no assignment or redirect, at most under
// `timeout <duration>`, piped only into output filters. Nothing in it runs before the call or changes its directory.
func larkLoneGateCall(cmd string, gates []proto.CommandGate) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil || len(tree.Stmts) != 1 {
		return false
	}
	var calls []*syntax.CallExpr
	var walk func(*syntax.Stmt) bool
	walk = func(st *syntax.Stmt) bool {
		if st.Background || st.Coprocess || st.Negated || len(st.Redirs) != 0 {
			return false
		}
		switch c := st.Cmd.(type) {
		case *syntax.CallExpr:
			calls = append(calls, c)
			return true
		case *syntax.BinaryCmd:
			return c.Op == syntax.Pipe && walk(c.X) && walk(c.Y)
		}
		return false
	}
	if !walk(tree.Stmts[0]) {
		return false
	}
	for i, c := range calls {
		if len(c.Assigns) != 0 || len(c.Args) == 0 {
			return false
		}
		words := make([]string, len(c.Args))
		for j, arg := range c.Args {
			var dynamic bool
			if words[j], dynamic = executionWord(arg); dynamic {
				return false
			}
		}
		if i > 0 {
			if !isLarkFilter(words) || words[0] == "cd" {
				return false
			}
			continue
		}
		if words[0] == "timeout" && len(words) > 2 && timeoutArg.MatchString(words[1]) {
			words = words[2:]
		}
		if gateOf(gates, words[0]) < 0 {
			return false
		}
	}
	return true
}

// larkReadFormattingPipeline proves the narrow command an Inbox default conversation may allow (#11991):
// one literal configured GET/HEAD under an optional timeout, then only the fixed output filters below. The AST must
// be one pipeline, so an appended command, substitution, assignment or file redirect is not formatting. Interpreters
// are not filters: even apparently inert Python may evaluate a string through a standard-library API. The caller
// separately requires one classified, rule-allowed read; this helper never executes it.
func larkReadFormattingPipeline(cmd string, gates []proto.CommandGate) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil || len(tree.Stmts) != 1 {
		return false
	}
	var statements []*syntax.Stmt
	var walk func(*syntax.Stmt) bool
	walk = func(st *syntax.Stmt) bool {
		if st.Background || st.Coprocess || st.Negated {
			return false
		}
		switch c := st.Cmd.(type) {
		case *syntax.CallExpr:
			statements = append(statements, st)
			return true
		case *syntax.BinaryCmd:
			return (c.Op == syntax.Pipe || c.Op == syntax.PipeAll) && walk(c.X) && walk(c.Y)
		}
		return false
	}
	if !walk(tree.Stmts[0]) || len(statements) < 2 {
		return false
	}
	for i, st := range statements {
		call := st.Cmd.(*syntax.CallExpr)
		if len(call.Assigns) != 0 || len(call.Args) == 0 {
			return false
		}
		for _, redirect := range st.Redirs {
			// Only stdout/stderr duplication such as 2>&1 is inert. An inherited descriptor may name a file or socket,
			// so neither an arbitrary target (>&3) nor an inherited input (<&3) belongs in this exception.
			if redirect.Op != syntax.DplOut || redirect.Word == nil {
				return false
			}
			fd, dynamic := executionWord(redirect.Word)
			if dynamic || fd != "1" && fd != "2" {
				return false
			}
			if redirect.N != nil && redirect.N.Value != "1" && redirect.N.Value != "2" {
				return false
			}
		}
		words := make([]string, len(call.Args))
		for j, arg := range call.Args {
			var dynamic bool
			if words[j], dynamic = executionWord(arg); dynamic {
				return false
			}
		}
		if i > 0 {
			// Do not inherit larkFilters: sort/uniq have output paths, and this exception never includes interpreters or
			// general-purpose print commands. Only these fixed stdin/stdout filters qualify.
			switch words[0] {
			case "jq", "head", "tail", "grep", "cut", "tr", "wc", "column":
				continue
			default:
				return false
			}
		}
		if words[0] == "timeout" {
			if len(words) <= 2 || !timeoutArg.MatchString(words[1]) {
				return false
			}
			words = words[2:]
		}
		if len(words) != 3 || gateOf(gates, words[0]) < 0 || words[1] != "GET" && words[1] != "HEAD" ||
			!strings.HasPrefix(words[2], "/") {
			return false
		}
	}
	return true
}

func classifyLarkDepth(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, extraPATH larkExtraPATH, depth int, relax bool) larkVerdict {
	return classifyLarkAt(cmd, gates, cwd, risk, extraPATH, depth, false, relax)
}

// classifyLarkAt is classifyLarkDepth for a command run here or, remote, on another machine through ssh: there PATH
// names that machine's directories, which say nothing about the programs here.
func classifyLarkAt(cmd string, gates []proto.CommandGate, cwd string, risk larkRisk, extraPATH larkExtraPATH, depth int, remote, relax bool) larkVerdict {
	var v larkVerdict
	if depth > 16 {
		v.other, v.unparsed, v.unknown = true, true, true
		return v
	}
	v.unknown = shellCommandUnknown(cmd, false, depth)
	merge := func(child larkVerdict) {
		v.calls = append(v.calls, child.calls...)
		if child.deny != "" {
			v.deny = child.deny
		}
		v.unknown = v.unknown || child.unknown || child.unparsed && larkCLIRe.MatchString(cmd)
		v.dynamic = v.dynamic || child.dynamic
		v.cliWrite = v.cliWrite || child.cliWrite
		v.unread = append(v.unread, child.unread...)
	}
	for _, g := range gates {
		for _, name := range g.Secret {
			if name != "" && strings.Contains(cmd, name) {
				v.deny = "不要读取、打印或直接使用 " + name + "：" + g.Title + "一律用 " + g.Command + "，它自己带凭据。"
				return v
			}
		}
	}
	if strings.Contains(cmd, "/proc/") && strings.Contains(cmd, "environ") {
		v.deny = "不要读取进程的环境变量（/proc/*/environ）：里面有凭据。"
		return v
	}
	if larkProfileEnv.MatchString(larkProfileRead.ReplaceAllString(cmd, "")) && larkCLIRe.MatchString(cmd) {
		v.deny = "lark-cli 的配置档由管理者定（manager.json 的 lark.cli.profile），不要自己设或清 LARKSUITE_CLI_PROFILE。"
		return v
	}
	if strings.Contains(cmd, ".lark-cli/") || strings.HasSuffix(cmd, ".lark-cli") {
		v.deny = "不要读或改 lark-cli 的配置和凭据（~/.lark-cli）：飞书用 lark-cli 命令，它自己带登录。"
		return v
	}
	if commands, ok := literalLarkLoopCommands(cmd, gates); ok {
		v.unknown, v.other = false, true
		for _, command := range commands {
			child := classifyLarkDepth(command, gates, cwd, risk, extraPATH, depth+1, relax)
			merge(child)
			v.unsure = v.unsure || child.unsure
			v.unparsed = v.unparsed || child.unparsed
			// Loop expansion proves reads only. Writes (including GET deletion
			// routes) keep the whole-command approval and proxy restrictions.
			for _, call := range child.calls {
				v.unknown = v.unknown || !larkCallRead(gates, call) || !larkBypassSafe(call)
			}
		}
		return v
	}
	resolvedPrefixes := map[int]bool{}
	cmd = normalizeLarkCommandRelaxed(cmd, cwd, gates, resolvedPrefixes, relax)
	v.unknown = shellCommandUnknown(cmd, false, depth)
	segs, ok := shellcmd.Split(cmd)
	if !ok {
		// The line splitter cannot represent every compound statement. Walk the shell AST instead, preserving simple
		// statements and whole pipelines, and classify each one with the same rules. Unsupported compound syntax keeps
		// the original opaque verdict; no command is executed by this fallback.
		if tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), ""); err == nil {
			compound := true
			var walk func(*syntax.Stmt) bool
			walk = func(stmt *syntax.Stmt) bool {
				if len(tree.Stmts) == 1 && stmt == tree.Stmts[0] {
					if _, simple := stmt.Cmd.(*syntax.CallExpr); simple {
						return false // the splitter, not a compound statement, is what failed
					}
				}
				switch c := stmt.Cmd.(type) {
				case *syntax.CallExpr:
					for _, redirect := range stmt.Redirs {
						if redirect.Pos().Offset() < c.End().Offset() {
							return false // a redirect interleaved with arguments can change which words are the call
						}
					}
				case *syntax.BinaryCmd:
					if c.Op != syntax.Pipe && c.Op != syntax.PipeAll {
						return walk(c.X) && walk(c.Y)
					}
				case *syntax.Block:
					if len(stmt.Redirs) != 0 {
						return false
					}
					for _, inner := range c.Stmts {
						if !walk(inner) {
							return false
						}
					}
					return true
				case *syntax.Subshell:
					if len(stmt.Redirs) != 0 {
						return false
					}
					for _, inner := range c.Stmts {
						if !walk(inner) {
							return false
						}
					}
					return true
				default:
					return false
				}
				var text strings.Builder
				if syntax.NewPrinter().Print(&text, stmt) != nil || strings.TrimSpace(text.String()) == strings.TrimSpace(cmd) {
					return false // never recursively classify the same unsplittable text
				}
				child := classifyLarkAt(text.String(), gates, cwd, risk, extraPATH, depth+1, remote, relax)
				merge(child)
				v.other = true
				v.unsure = v.unsure || child.unsure
				v.unparsed = v.unparsed || child.unparsed
				return true
			}
			for _, stmt := range tree.Stmts {
				compound = walk(stmt) && compound
			}
			if compound {
				v.other = true
				// Splitting statements also loses larkTextRuns's cross-statement data flow (a heredoc written now and
				// executed later). Keep every compound fallback that names the CLI opaque; the per-call results above
				// are sufficient only when no such indirect execution text is present.
				v.unknown = v.unknown || larkCLIRe.MatchString(cmd)
				return v
			}
		}
		v.other, v.unparsed, v.unknown = true, true, v.unknown || gateNamed(gates, cmd) || larkCLIRe.MatchString(cmd) || shellCommandUnknown(cmd, true, depth)
		return v
	}
	vars := map[string]string{}
	varsStable := larkVariableScopeStable(cmd, gates)
	if !varsStable {
		// Outside straight-line code only a variable constant over the whole command expands (#16491).
		vars = larkLiteralConstants(cmd, gates)
	}
	executableEnv := false
	// Text naming lark-cli is a call only where a program executes it (larkgate_text.go): flow notes each
	// segment for larkTextRuns.
	flow := make([]larkFlow, 0, len(segs))
	// sequential: a `;` / `&&` / `||` list of simple commands, run one after another in the order written.
	sequential := false
	if tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), ""); err == nil {
		_, sequential = larkFlatStmts(tree.Stmts)
	}
	// fileLoops: every loop over a command substitution in the command iterates over a file's words and only reads
	// Lark (larkFileReadLoops, #15860); only a bypass conversation reads it so (classifyLarkRelaxed).
	fileLoops := relax && larkFileReadLoops(cmd, risk)
	// noteVars: the literal values a note's path may name (larkWritesFileVars); nil keeps every variable path a write.
	var noteVars map[string]string
	if relax && varsStable {
		noteVars = vars
	}
	for position, s := range segs {
		writes, notes := larkWritesFileVars(s.Redirect, noteVars)
		flow = append(flow, larkFlow{text: s.Text, writes: writes, notes: notes})
		seg := &flow[len(flow)-1]
		seg.resolved = resolvedPrefixes[position]
		assignmentWords := s.Words
		for len(assignmentWords) > 0 && (assignmentWords[0] == "do" || assignmentWords[0] == "then" || assignmentWords[0] == "else") {
			assignmentWords = assignmentWords[1:]
		}
		resultAssignment := len(assignmentWords) == 1 && isAssignment(assignmentWords[0])
		nestedUnsure := false
		// fileReads: every substitution that is no filter only reads files (larkFileReadSubstitution).
		fileReads := true
		for _, nested := range s.Nested {
			if inner, ok := larkArithmetic(nested); ok {
				// $((…)) is arithmetic, not a subshell: it runs only its own $(…). It stays unsure (a variable's
				// value is evaluated as an expression, which may hold a substitution of its own).
				nestedUnsure, fileReads = true, false
				for _, c := range inner {
					child := classifyLarkDepth(c, gates, cwd, risk, extraPATH, depth+1, relax)
					if len(child.calls) > 0 || child.unknown || child.unparsed {
						v.other = true
					}
					merge(child)
				}
				continue
			}
			child := classifyLarkDepth(nested, gates, cwd, risk, extraPATH, depth+1, relax)
			if len(child.calls) > 0 || child.unknown || child.unparsed {
				v.other = true
			}
			formatting := larkFilterSubstitution(nested) || resultAssignment && larkAssignmentFormattingSubstitution(nested)
			readResult := resultAssignment && larkClassifiedReadSubstitution(gates, child)
			nestedUnsure = nestedUnsure || !formatting && !readResult || child.unknown || child.unparsed || child.unsure
			fileReads = fileReads && (larkFilterSubstitution(nested) || larkFileReadSubstitution(nested)) &&
				len(child.calls) == 0 && !child.unknown && !child.unparsed && !child.unsure
			merge(child)
		}
		// Output filters in a substitution do not make the enclosing command
		// opaque. Gated calls and unresolved execution inside it still do. A lark-cli command's argument may also come
		// from a file (`$(cat ids.txt)`, `$(< ids.txt)`, #9101), as from $(head …): a write with it stays unknown by
		// the dynamic-argument rule below. In any other command's argument (curl's) a file read stays unsure: it could
		// carry the file elsewhere.
		cliFileRead := s.ArgSubst && nestedUnsure && fileReads && larkSegmentCLI(s.Words)
		// The header of a loop over a file's words, when every such loop only reads Lark (fileLoops): its body is
		// classified segment by segment below, as any loop's.
		loopFileRead := fileLoops && len(s.Words) > 0 && s.Words[0] == "for"
		if s.ArgSubst && nestedUnsure && !cliFileRead && !loopFileRead {
			v.unsure, v.other = true, true
		}
		if v.deny != "" {
			return v
		}
		words := s.Words
		// Loop bodies may prefix the invocation with both a shell keyword and environment assignments.
		for len(words) > 0 && (words[0] == "do" || words[0] == "then" || words[0] == "else") {
			words = words[1:]
			v.other = true
		}
		// Leading assignments: alone they set a variable for later words; before ol only the account / timeout.
		envWords := words
		if len(envWords) > 0 && envWords[0] == "export" {
			envWords = envWords[1:]
		}
		for _, w := range envWords {
			if !isAssignment(w) {
				break
			}
			executableEnv = executableEnv || larkExecutableAssignment(w, remote, extraPATH)
		}
		var env []string
		for len(words) > 0 && isAssignment(words[0]) {
			env = append(env, words[0])
			words = words[1:]
		}
		if len(words) == 0 {
			if varsStable {
				for _, a := range env {
					k, val, _ := strings.Cut(a, "=")
					if strings.ContainsAny(val, "$`") {
						delete(vars, k) // known only when it runs: stays a variable
						continue
					}
					vars[k] = val
				}
			}
			continue
		}
		for _, word := range words {
			if isAssignment(word) {
				executableEnv = executableEnv || larkExecutableAssignment(word, remote, extraPATH)
			}
		}
		// zsh's `=cmd` expansion names the command it runs (`=sh` is `sh`, `=env` is `env`, `=ex` is the gate):
		// unwrapCommand, shellScriptIndex, gateOf and isLarkCLI must all read the resolved name, not its `=` spelling,
		// or a shell, wrapper or gate behind it stays ordinary and its carried write reaches the gate without a card.
		words = append([]string{strings.TrimPrefix(words[0], "=")}, words[1:]...)
		var parsed bool
		words, parsed = unwrapCommand(words)
		if !parsed {
			v.other = true
			v.unknown = v.unknown || gateNamed(gates, s.Text) || gateNamedFields(gates, words) || larkCLIRe.MatchString(s.Text)
			for _, word := range words {
				v.unknown = v.unknown || strings.ContainsAny(word, "$`*?[]{}")
			}
			continue
		}
		if len(words) == 0 {
			continue
		}
		name := words[0]
		gate := -1
		if (name == "bash" || name == "sh") && len(words) > 1 {
			if i := gateOf(gates, words[1]); i >= 0 {
				gate, words = i, words[1:]
			}
		}
		if gate < 0 {
			gate = gateOf(gates, name)
		}
		switch {
		case shellScriptIndex(words) >= 0:
			v.other = true
			merge(classifyLarkDepth(words[shellScriptIndex(words)], gates, cwd, risk, extraPATH, depth+1, relax))
		case name == "eval":
			v.other = true
			merge(classifyLarkDepth(strings.Join(words[1:], " "), gates, cwd, risk, extraPATH, depth+1, relax))
		case codeBuiltinRuns(name, words) && (gateNamed(gates, s.Text) || gateNamedFields(gates, words)):
			v.other, v.unknown = true, true
		case name == "trap" && (gateNamed(gates, strings.Join(words[1:], " ")) || gateNamedFields(gates, words) || larkCLIRe.MatchString(strings.Join(words[1:], " "))):
			// The action runs later, but it is code this command carries: its gated call must be confirmed now.
			// gateNamedFields also sees a gate name packed inside a wrapper's quoted action (`sh -c "ex POST …"`),
			// which gateNamed stops at the wrapper word.
			v.other, v.unknown = true, true
		case isLarkCLI(name):
			// As for gated calls (parseGateCall): literal assignments earlier in the command resolve; what stays a
			// variable may be a flag value of a read, never a command word, an api route or anything of a write.
			args := make([]string, len(words)-1)
			for i, a := range words[1:] {
				args[i] = expandKnownVars(a, vars)
			}
			dynamic := strings.ContainsAny(strings.Join(args, " "), "$`")
			c, deny, ok := parseLarkCLI(args, risk)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other, v.cliWrite, v.unknown = true, true, true
				continue
			}
			if !c.cliRead() {
				v.cliWrite = true
			}
			// An unquoted variable can add words; that matters only to a read that --dry-run or --help made one
			// (a later --dry-run=false sends): a command's own risk and an api method are set by its literal words.
			flagRead := flagOn(args, "--dry-run") || flagOn(args, "-h", "--help")
			if dynamic && (!c.cliRead() || strings.Contains(c.Path, "$") || strings.Contains(strings.Join(args, " "), "`") || flagRead && splitGateArguments(s.Text)) {
				v.other, v.unknown = true, true
			}
			v.calls = append(v.calls, c)
			seg.call = true // its words go to lark-cli alone, its output is data
			continue
		case gate >= 0:
			g := gates[gate]
			for _, a := range env {
				if k, _, _ := strings.Cut(a, "="); !slices.Contains(g.AllowedEnv, k) {
					v.other = true
				}
			}
			if s.ArgSubst || splitGateArgumentsForGate(s.Text) {
				v.other, v.unknown = true, true
				v.unread = append(v.unread, gate)
				continue
			}
			c, deny, ok := parseGateCall(g, words[1:], vars, env, cwd)
			if deny != "" {
				v.deny = deny
				return v
			}
			if !ok {
				v.other, v.unknown = true, true
				v.unread = append(v.unread, gate)
				continue
			}
			c.Gate = gate
			v.calls = append(v.calls, c)
		case name == "curl" || name == "wget" || name == "http" || name == "xh":
			if g, ok := gateAimed(gates, s.Text); ok {
				v.deny = "不要直接 curl " + g.Title + " 的接口：用 " + g.Command + "，写操作要先在手机上确认。"
				return v
			}
			v.other = true
		case filepath.Base(name) == "ssh":
			v.other = true
			remote, ok := sshRemoteCommand(words[1:])
			if !ok || remote == "" {
				seg.opaque = true // a login shell there runs whatever comes in
				v.unknown = v.unknown || larkCLIRe.MatchString(s.Text)
				continue
			}
			// lark-cli there acts as the user too: only what the gate can read as reads stays out of the way.
			child := classifyLarkAt(remote, gates, "", risk, extraPATH, depth+1, true, relax)
			for _, c := range child.calls {
				child.unknown = child.unknown || !c.CLI // a gated command there is not this machine's gate
			}
			merge(child)
			v.unsure = v.unsure || child.unsure
			v.unknown = v.unknown || child.unparsed && larkCLIRe.MatchString(remote)
			seg.call = true // the remote command, read above, is what its words do
		case filepath.Base(name) == "perl" && perlSubstitution(words[1:]):
			v.other = true // it edits text and runs none of it, whatever its pattern names
		case name == "for" || name == "done" || name == "fi":
			v.other = true // a loop's words are its variable's values: run only where a command uses $var (dynamicCommandName)
		case isLarkFilter(words) && len(env) == 0:
			seg.filter = true
		case name == "tee":
			v.other, seg.filter, seg.writes = true, true, true
		default:
			v.other, seg.program = true, true
			// Inline Python that can start nothing holds lark-cli's name as data (#8148); it may write it to a file.
			inert := inertPythonCode(words)
			dynamicName := dynamicCommandName(name)
			if larkCLIRe.MatchString(s.Text) && !inert || dynamicName {
				v.unknown = true // an interpreter, wrapper or script may invoke the CLI
			}
			v.dynamic = v.dynamic || dynamicName
			if inert && larkCLIRe.MatchString(s.Text) {
				if files, ok := pythonWrites(words); ok {
					seg.notes = append(seg.notes, files...)
				} else {
					seg.writes = true
				}
			}
			// A program whose code the command does not show may run lark-cli; inline code that runs nothing
			// else is read by its own words above.
			seg.opaque = (indirectLarkCommand(name) || sortExecutesProgram(words)) && !selfContainedCode(words) && !inert
			sequential = sequential && name != "trap" // its code runs later
		}
	}
	if larkTextRuns(cmd, flow, sequential) {
		v.other, v.unknown = true, true
	}
	if executableEnv && (len(v.calls) > 0 || larkCLIRe.MatchString(cmd)) {
		v.other, v.unknown = true, true
	}
	if len(v.calls) == 0 {
		v.other = true
	}
	return v
}

// codeBuiltinRuns reports a zsh builtin that executes code supplied as an argument. The code is not
// parsed here: when it names a gate, the whole command must be confirmed rather than treated as ordinary.
func codeBuiltinRuns(name string, words []string) bool {
	if name != "emulate" {
		return false
	}
	for i := 1; i < len(words); i++ {
		// A `--` only ends emulate's own options: the shell name and its -c still follow it (zsh's
		// `emulate -- zsh -c CODE` runs CODE), so keep scanning rather than treating it as a stop.
		if words[i] == "--" {
			continue
		}
		if words[i] == "-c" || strings.HasPrefix(words[i], "-") && !strings.HasPrefix(words[i], "--") &&
			strings.Contains(words[i][1:], "c") {
			return i+1 < len(words)
		}
	}
	return false
}

// larkFilterSubstitution reports whether a command substitution only formats
// or prints data. Other programs keep the enclosing gated call conservative.
// larkClassifiedReadSubstitution is a command substitution made solely of calls already proven to be reads. Its
// output can be stored or formatted as data; the enclosing executable and any eval/shell use are checked separately.
func larkClassifiedReadSubstitution(gates []proto.CommandGate, v larkVerdict) bool {
	return len(v.calls) > 0 && !v.other && !v.unknown && !v.unparsed && !v.unsure &&
		!slices.ContainsFunc(v.calls, func(c larkCall) bool { return !larkCallRead(gates, c) || !larkBypassSafe(c) })
}

func larkFilterSubstitution(cmd string) bool {
	return larkSubstitutionFilter(cmd, false)
}

// larkAssignmentFormattingSubstitution also permits self-contained inline formatters. It is used only when a result
// is saved in a variable; command arguments keep the stricter filter rule above.
func larkAssignmentFormattingSubstitution(cmd string) bool {
	return larkSubstitutionFilter(cmd, true)
}

func larkSubstitutionFilter(cmd string, inlineCode bool) bool {
	segs, ok := shellcmd.Split(cmd)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		if s.Subst || len(s.Words) == 0 {
			return false
		}
		words, ok := unwrapCommand(s.Words)
		if !ok || len(words) == 0 || len(words) != len(s.Words) || !isLarkFilter(words) && (!inlineCode || !selfContainedCode(words)) {
			return false
		}
	}
	return true
}

// larkFileReadSubstitution: a command substitution that only reads files, `cat <file>…` with plain file words or
// `< file` (#9101). Kept only in a lark-cli read's argument (larkSegmentCLI), as $(head …) is anywhere.
func larkFileReadSubstitution(cmd string) bool {
	segs, ok := shellcmd.Split(cmd)
	if !ok || len(segs) != 1 || segs[0].Subst {
		return false
	}
	s := segs[0]
	if len(s.Words) == 0 {
		return larkReadsFileOnly(s.Redirect)
	}
	if s.Redirect != "" || s.Words[0] != "cat" || len(s.Words) < 2 {
		return false
	}
	for _, w := range s.Words[1:] {
		if strings.HasPrefix(w, "-") || strings.ContainsAny(w, "$`") {
			return false
		}
	}
	return true
}

// larkReadsFileOnly: redirect is one input redirection from a file ("<ids.txt"), nothing written, no descriptor moved.
func larkReadsFileOnly(redirect string) bool {
	file, ok := strings.CutPrefix(redirect, "<")
	return ok && file != "" && !strings.ContainsAny(file, "<>&| ")
}

// larkSegmentCLI: a segment's command, past loop keywords, assignments and wrappers (timeout N…), is lark-cli.
func larkSegmentCLI(words []string) bool {
	for len(words) > 0 && (words[0] == "do" || words[0] == "then" || words[0] == "else" || isAssignment(words[0])) {
		words = words[1:]
	}
	words, ok := unwrapCommand(words)
	return ok && len(words) > 0 && isLarkCLI(words[0])
}

func isAssignment(w string) bool {
	k, _, ok := strings.Cut(w, "=")
	if !ok || k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		if c == '_' || 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || i > 0 && '0' <= c && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// indirectLarkCommand identifies opaque execution in a command already naming the CLI.
// Without that context, interpreters and script files stay outside this gate.
func indirectLarkCommand(name string) bool {
	if name == "[" || name == "[[" {
		return false
	}
	if strings.ContainsAny(name, "$`*?[]{}~") {
		return true
	}
	base := filepath.Base(name)
	if larkInterpreter.MatchString(base) {
		return true
	}
	switch base {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "python", "python2", "python3", "node", "nodejs", "perl", "ruby", "lua", "php", "eval", "xargs", "source", ".", "env", "exec", "command", "sudo", "timeout", "builtin", "busybox", "time", "!", "nohup", "nice", "setsid", "ssh", "awk", "gawk", "sed", "find":
		return true
	}
	if strings.Contains(name, "/") {
		if dir := filepath.Dir(name); dir == "/bin" || dir == "/usr/bin" {
			if larkFilters[base] {
				return false
			}
			switch base {
			case "rm", "git", "tee", "ls", "cat", "curl", "wget":
				return false
			}
		}
		return true
	}
	return strings.HasSuffix(name, ".sh") || strings.HasSuffix(name, ".py") || strings.HasSuffix(name, ".js")
}

// These variables can replace the executable or load code before a nominally read-only CLI call.
var larkExecutableEnv = map[string]bool{
	"PATH": true, "NODE_OPTIONS": true, "NODE_PATH": true, "BASH_ENV": true, "ENV": true, "ZDOTDIR": true,
	"LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "DYLD_INSERT_LIBRARIES": true,
	"PYTHONPATH": true, "PYTHONSTARTUP": true,
}

var larkInterpreter = lazyre.New(`^(?:python|node|perl|ruby|lua|php)(?:[0-9]+(?:\.[0-9]+)*)?$`)

var redirectFD = lazyre.New(`^[0-9]*[<>]&[0-9]+$`)

// safeRedirect is used only by readsOnlyMemory. It accepts /tmp and /dev/null;
// readsOnlyMemory rejects /tmp separately. Lark redirect targets are not checked here.
func safeRedirect(r string) bool {
	for _, t := range strings.Fields(r) {
		if strings.ContainsAny(t, "$`*?[]{}~\"'\\") {
			return false
		}
		if strings.HasSuffix(t, ">") || strings.HasSuffix(t, "<") {
			return false
		}
		if redirectFD.MatchString(t) {
			continue
		}
		t = strings.TrimLeft(t, "0123456789&>")
		t = filepath.Clean(t)
		if t == "/dev/null" || strings.HasPrefix(t, "/tmp/") {
			continue
		}
		return false
	}
	return true
}

// gateOf is the gate whose command or script name is, -1 for none.
func gateOf(gates []proto.CommandGate, name string) int {
	// zsh's `=cmd` runs the resolved command (`=ex …` is the gate); classify the word by what it names.
	name = strings.TrimPrefix(name, "=")
	for i, g := range gates {
		if g.Command != "" && name == g.Command || g.Script != "" && (name == g.Script || strings.HasSuffix(name, "/"+g.Script)) {
			return i
		}
	}
	return -1
}

// gateAimed: text reaches a gate's API (its base's host, or its path prefix).
func gateAimed(gates []proto.CommandGate, text string) (proto.CommandGate, bool) {
	for _, g := range gates {
		if h := larkHost(g.Env[g.BaseEnv]); h != "" && strings.Contains(text, h) || g.PathPrefix != "" && strings.Contains(text, g.PathPrefix) {
			return g, true
		}
	}
	return proto.CommandGate{}, false
}

// gateNamed reports literal gate command words in a command the shell
// splitter could not read. The AST preserves names next to redirections;
// the fallback is deliberately conservative about quoted text.
func gateNamed(gates []proto.CommandGate, cmd string) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err == nil {
		found := false
		syntax.Walk(tree, func(node syntax.Node) bool {
			call, ok := node.(*syntax.CallExpr)
			if !ok || len(call.Args) == 0 || found {
				return !found
			}
			words := make([]string, len(call.Args))
			for i, word := range call.Args {
				words[i], _ = executionWord(word)
			}
			suffix, ok := unwrapCommand(words)
			if ok && len(suffix) > 0 {
				found = gateOf(gates, suffix[0]) >= 0
				if !found && (suffix[0] == "bash" || suffix[0] == "sh") && len(suffix) > 1 {
					found = gateOf(gates, suffix[1]) >= 0
				}
			}
			return !found
		})
		return found
	}
	cmd = strings.NewReplacer("<", " ", ">", " ").Replace(cmd)
	for _, word := range strings.Fields(cmd) {
		if gateOf(gates, strings.Trim(word, "'\"`;&|()")) >= 0 {
			return true
		}
	}
	return false
}

// gateNamedFields keeps a gate name visible when an unsupported wrapper leaves its
// command packed inside one quoted or option=value word (for example env -S).
// This fallback only confirms; it never reinterprets or executes those words.
func gateNamedFields(gates []proto.CommandGate, words []string) bool {
	for _, word := range words {
		fields := strings.FieldsFunc(word, func(r rune) bool {
			return r == ' ' || r == '\t' || r == '\n' || strings.ContainsRune("='\"`;&|()", r)
		})
		for _, field := range fields {
			if gateOf(gates, field) >= 0 {
				return true
			}
		}
	}
	return false
}

// larkVariableScopeStable reports whether straight-line assignments can be
// used to expand a later route. Only known read-only commands can appear
// between those assignments and the gated call.
func larkVariableScopeStable(cmd string, gates []proto.CommandGate) bool {
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return false
	}
	stable := true
	syntax.Walk(tree, func(node syntax.Node) bool {
		if !stable {
			return false
		}
		switch n := node.(type) {
		case *syntax.IfClause, *syntax.ForClause, *syntax.WhileClause, *syntax.CaseClause, *syntax.FuncDecl, *syntax.DeclClause:
			stable = false
			return false
		case *syntax.Stmt:
			if n.Background || n.Negated || n.Coprocess {
				stable = false
				return false
			}
		case *syntax.BinaryCmd:
			// A pipeline's parts and what follows && or || may run in a subshell or not at all: an assignment there
			// may never reach later commands. Commands there are checked as anywhere else.
			syntax.Walk(n, func(inner syntax.Node) bool {
				if call, ok := inner.(*syntax.CallExpr); ok && len(call.Assigns) > 0 {
					stable = false
				}
				return stable
			})
			return stable
		case *syntax.Assign:
			if n.Append || n.Index != nil || n.Array != nil || n.Value == nil {
				stable = false
				return false
			}
		case *syntax.ArithmExp:
			stable = false
			return false
		case *syntax.ParamExp:
			if n.Exp != nil {
				stable = false
				return false
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			words := make([]string, len(n.Args))
			for i, word := range n.Args {
				words[i], _ = executionWord(word)
			}
			suffix, ok := unwrapCommand(words)
			if !ok || len(suffix) == 0 {
				stable = false
				return false
			}
			name := suffix[0]
			allowed := gateOf(gates, name) >= 0 || isLarkCLI(name)
			if !allowed && (name == "bash" || name == "sh") && len(suffix) > 1 {
				allowed = gateOf(gates, suffix[1]) >= 0
			}
			if !allowed {
				allowed = map[string]bool{"cat": true, "tee": true, "ls": true, "git": true, "go": true, "pwd": true, "sleep": true, "false": true, ":": true}[name] ||
					isLarkFilter(suffix) && name != "printf" && name != "cd"
			}
			if !allowed {
				stable = false
				return false
			}
		}
		return true
	})
	return stable
}

var (
	larkConstantValue = lazyre.New(`^[A-Za-z0-9_-]+$`)
	larkConstantName  = lazyre.New(`^(?:[a-z][A-Za-z0-9_]*|[A-Z])$`)
	// zsh's lowercase special parameters and hook arrays: the shell sets or reads them itself (=~ fills match, cd runs
	// chpwd_functions). bash's are all uppercase or start with "_", which larkConstantName already rules out.
	larkShellLowercase = map[string]bool{"argv": true, "status": true, "pipestatus": true, "path": true, "cdpath": true,
		"fpath": true, "fignore": true, "mailpath": true, "manpath": true, "module_path": true, "psvar": true, "watch": true,
		"match": true, "mbegin": true, "mend": true, "reply": true, "signals": true, "histchars": true, "prompt": true,
		"funcstack": true, "funcfiletrace": true, "funcsourcetrace": true, "functrace": true, "options": true,
		"parameters": true, "commands": true, "aliases": true, "galiases": true, "saliases": true, "functions": true,
		"builtins": true, "reswords": true, "history": true, "historywords": true, "jobdirs": true, "jobstates": true,
		"jobtexts": true, "dirstack": true, "modules": true, "nameddirs": true, "userdirs": true, "usergroups": true,
		"errnos": true, "sysparams": true, "termcap": true, "terminfo": true, "mapfile": true, "epochtime": true,
		"langinfo": true, "keymaps": true, "widgets": true, "patchars": true, "words": true, "compstate": true}
)

// larkPlainTest: test / [ arguments that are only string and file tests, as in [[ ]]; zsh evaluates the operands of
// the others (-t, -v, -eq, …) as arithmetic or a subscript. An expansion is only an operand, right after a literal
// operator, never in an operator's place.
func larkPlainTest(args []*syntax.Word) bool {
	unary := []string{"-n", "-z", "-e", "-f", "-d", "-s", "-r", "-w", "-x"}
	binary := []string{"=", "==", "!="}
	words := make([]string, len(args))
	dynamic := make([]bool, len(args))
	for i, w := range args {
		words[i], dynamic[i] = executionWord(w)
	}
	for i, w := range words {
		switch {
		case dynamic[i]:
			prevOp := i > 0 && !dynamic[i-1] && (slices.Contains(unary, words[i-1]) || slices.Contains(binary, words[i-1]))
			nextOp := i+1 < len(words) && !dynamic[i+1] && slices.Contains(binary, words[i+1])
			if !prevOp && !nextOp {
				return false
			}
		case strings.HasPrefix(w, "-") && !slices.Contains(unary, w) && w != "-a" && w != "-o":
			return false
		}
	}
	return true
}

// larkConstantSafeCommands: commands that cannot set a variable of the shell running them: programs that run in a
// process of their own, and builtins that only print or test. Not printf / print (-v), read, set, or any modifier.
var larkConstantSafeCommands = map[string]bool{"cd": true, "echo": true, "true": true, "false": true, ":": true,
	"test": true, "[": true, "pwd": true, "cat": true, "tee": true, "ls": true, "head": true, "tail": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "wc": true, "sort": true, "uniq": true, "cut": true,
	"tr": true, "column": true, "jq": true, "awk": true, "sed": true, "paste": true, "seq": true, "date": true,
	"sleep": true, "rm": true, "mkdir": true, "rmdir": true, "cp": true, "mv": true, "touch": true, "basename": true,
	"dirname": true, "diff": true, "comm": true, "nl": true, "fold": true, "base64": true}

// larkShellUppercase: bash's and zsh's uppercase parameters that the shell itself rewrites while a command runs (cd,
// =~, read, getopts, every command or second). A literal binding of one says nothing about its later value.
var larkShellUppercase = map[string]bool{"PWD": true, "OLDPWD": true, "FUNCNAME": true, "REPLY": true, "OPTARG": true,
	"OPTIND": true, "LINENO": true, "RANDOM": true, "SRANDOM": true, "SECONDS": true, "EPOCHSECONDS": true,
	"EPOCHREALTIME": true, "HISTCMD": true, "PIPESTATUS": true, "MATCH": true, "MBEGIN": true, "MEND": true,
	"COPROC": true, "MAPFILE": true, "DIRSTACK": true, "GROUPS": true, "PPID": true, "UID": true, "EUID": true,
	"SHLVL": true, "COLUMNS": true, "LINES": true, "TTYIDLE": true, "ERRNO": true}

// larkShellManaged: a name bash or zsh sets or acts on by itself.
func larkShellManaged(name string) bool {
	return name == "_" || name == "IFS" || strings.HasPrefix(name, "BASH") || larkShellUppercase[name] ||
		larkShellLowercase[name] || strings.HasSuffix(name, "_functions") || strings.HasPrefix(name, "zsh_") ||
		strings.HasPrefix(name, "zle_") || strings.HasPrefix(name, "comp")
}

// larkConstantNameOK: a lowercase name or one capital letter that the shell never sets itself.
func larkConstantNameOK(name string) bool {
	return larkConstantName.MatchString(name) && !larkShellManaged(name)
}

// larkLiteralConstants returns the variables provably constant over the whole command, for routes in loops and lists
// that larkVariableScopeStable rejects (#16491). A constant is assigned exactly once, as `NAME=value` on its own, by a
// top-level statement that always runs: a `;` / newline statement, or an `&&` operand after only `true`, `:` or, in the
// first statement, `cd /tmp`. Its value is a literal of
// [A-Za-z0-9_-], so it cannot change which route a call reaches; its name is one the shell never sets itself
// (larkConstantNameOK); it is not used before that statement, and no other assignment of it appears. Every command in
// it must be one that cannot set a variable (larkConstantSafeCommands), every assignment in it must be to such a name,
// and nothing in it may evaluate arithmetic, a subscript or a non-string test; otherwise there are no constants at all.
func larkLiteralConstants(cmd string, gates []proto.CommandGate) map[string]string {
	out := map[string]string{}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return out
	}
	type constant struct {
		value string
		at    syntax.Pos
	}
	candidates := map[string]constant{}
	assigned := map[string]int{} // every assignment of each name, anywhere
	used := map[string][]syntax.Pos{}
	opaque := false
	alwaysRuns := func(st *syntax.Stmt, first bool) bool {
		call, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || st.Negated || st.Background || st.Coprocess || len(st.Redirs) > 0 || len(call.Assigns) > 0 {
			return false
		}
		words := make([]string, len(call.Args))
		for i, arg := range call.Args {
			var dynamic bool
			if words[i], dynamic = executionWord(arg); dynamic {
				return false
			}
		}
		switch {
		case len(words) == 1 && (words[0] == "true" || words[0] == ":"):
			return true
		case len(words) == 2 && words[0] == "cd" && words[1] == "/tmp" && first:
			// Only /tmp, as the command's first statement: every user may enter it, and nothing before it runs.
			info, err := os.Stat("/tmp")
			return err == nil && info.IsDir()
		}
		return false
	}
	candidate := func(st *syntax.Stmt) {
		call, ok := st.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) != 0 || len(call.Assigns) != 1 || len(st.Redirs) > 0 || st.Background || st.Coprocess || st.Negated {
			return
		}
		a := call.Assigns[0]
		if a.Append || a.Index != nil || a.Array != nil || a.Naked || a.Value == nil || a.Name == nil {
			return
		}
		value, dynamic := executionWord(a.Value)
		if dynamic || !larkConstantValue.MatchString(value) || !larkConstantNameOK(a.Name.Value) {
			return
		}
		candidates[a.Name.Value] = constant{value: value, at: st.Pos()}
	}
	for si, st := range tree.Stmts {
		// A top-level `a && b && NAME=v` is a left-nested chain; walk it in order.
		var chain []*syntax.Stmt
		for cur := st; ; {
			b, ok := cur.Cmd.(*syntax.BinaryCmd)
			if !ok || b.Op != syntax.AndStmt || cur.Negated || cur.Background || cur.Coprocess || len(cur.Redirs) > 0 {
				chain = append([]*syntax.Stmt{cur}, chain...)
				break
			}
			chain = append([]*syntax.Stmt{b.Y}, chain...)
			cur = b.X
		}
		for i, part := range chain {
			if !slices.ContainsFunc(chain[:i], func(prev *syntax.Stmt) bool { return !alwaysRuns(prev, si == 0) }) {
				candidate(part)
			}
		}
	}
	syntax.Walk(tree, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Assign:
			opaque = opaque || n.Index != nil // a[expr]=v evaluates expr as arithmetic
			if n.Name != nil {
				assigned[n.Name.Value]++
				// Only ordinary names: IFS changes how constants split, PATH what runs, and an assignment to one of zsh's
				// integer specials (HISTSIZE=e) evaluates its value as arithmetic.
				opaque = opaque || !larkConstantNameOK(n.Name.Value) || larkExecutableEnv[n.Name.Value]
			}
		case *syntax.WordIter:
			if n.Name != nil {
				assigned[n.Name.Value] += 2 // a loop variable is never a constant
			}
		case *syntax.ParamExp:
			if n.Index != nil || n.Slice != nil {
				opaque = true // a subscript or offset is arithmetic
			}
			if n.Param != nil {
				if n.Exp != nil || n.Excl || n.Names != 0 {
					assigned[n.Param.Value] += 2 // ${NAME:=x}, ${!NAME}: assigns or reads indirectly
				}
				used[n.Param.Value] = append(used[n.Param.Value], n.Pos())
			}
		case *syntax.ArithmCmd, *syntax.ArithmExp:
			// Arithmetic can assign (`$((N=1))`), also through a variable whose value is an expression.
			opaque = true
		case *syntax.UnaryTest:
			// Only string and file tests: zsh evaluates other operands (-t, -v, …) as arithmetic or a subscript.
			switch n.Op {
			case syntax.TsExists, syntax.TsRegFile, syntax.TsDirect, syntax.TsRead, syntax.TsWrite, syntax.TsExec,
				syntax.TsNoEmpty, syntax.TsEmpStr, syntax.TsNempStr, syntax.TsNot:
			default:
				opaque = true
			}
		case *syntax.BinaryTest:
			switch n.Op {
			case syntax.TsMatchShort, syntax.TsMatch, syntax.TsNoMatch, syntax.AndTest, syntax.OrTest:
			default:
				opaque = true // -eq and the like evaluate arithmetic; =~ fills match variables
			}
		case *syntax.ExtGlob:
			opaque = true // zsh glob qualifiers can run code
		case *syntax.Lit:
			// An unquoted or double-quoted [ is a glob or, after $name in zsh, a subscript evaluated as arithmetic; a $
			// left in a literal is an expansion the parser did not read ($~name turns on zsh's glob qualifiers).
			if strings.Contains(n.Value, "[") && n.Value != "[" || strings.Contains(n.Value, "$") {
				opaque = true
			}
		case *syntax.FuncDecl, *syntax.DeclClause, *syntax.LetClause, *syntax.CoprocClause:
			opaque = true
		case *syntax.Redirect:
			if n.N != nil && strings.Trim(n.N.Value, "0123456789") != "" {
				opaque = true // {name}>file assigns name
			}
		case *syntax.CallExpr:
			if len(n.Args) == 0 {
				return true
			}
			// Only a program in a process of its own cannot set this shell's variables. bash and zsh have many builtins
			// and precommand modifiers that can (print -v, zstyle -s, set -A, emulate -c, noglob eval, repeat, …):
			// every command must be a gated one, lark-cli, or a name known to be one of those programs or an inert
			// builtin; anything else leaves no constants.
			name, dynamic := executionWord(n.Args[0])
			if dynamic || gateOf(gates, name) < 0 && !isLarkCLI(name) && !larkConstantSafeCommands[name] {
				opaque = true
			}
			if name == "test" || name == "[" {
				opaque = opaque || !larkPlainTest(n.Args[1:])
			}
		}
		return true
	})
	if opaque {
		return map[string]string{}
	}
	for name, c := range candidates {
		if assigned[name] != 1 || slices.ContainsFunc(used[name], func(p syntax.Pos) bool { return p.Offset() < c.at.Offset() }) {
			continue
		}
		out[name] = c.value
	}
	return out
}

// scalarParameter is a plain quoted scalar expansion, not "$@", an array
// expansion, or a parameter operation whose result needs further parsing.
func scalarParameter(p *syntax.ParamExp) bool {
	return p != nil && p.Param != nil && p.Param.Value != "@" && p.Param.Value != "*" &&
		!p.Excl && !p.Length && !p.Width && p.Index == nil && p.Slice == nil && p.Repl == nil && p.Exp == nil && p.Names == 0
}

// braceExpansion recognizes braces across the literal and quoted parts of one
// word. Quoted content is a placeholder because it does not itself expand.
func braceExpansion(word *syntax.Word) bool {
	var text strings.Builder
	for _, part := range word.Parts {
		if lit, ok := part.(*syntax.Lit); ok {
			text.WriteString(lit.Value)
		} else {
			text.WriteByte('_')
		}
	}
	value := text.String()
	return strings.Contains(value, "{") && strings.Contains(value, "}") && strings.Contains(value, ",")
}

// wordExpandsToMany reports shell syntax that can produce extra arguments.
// Double quotes protect scalar variables, but not "$@" or array expansions.
func wordExpandsToMany(word *syntax.Word) bool {
	if braceExpansion(word) {
		return true
	}
	found := false
	syntax.Walk(word, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.SglQuoted:
			return false
		case *syntax.DblQuoted:
			syntax.Walk(n, func(inner syntax.Node) bool {
				if p, ok := inner.(*syntax.ParamExp); ok && !scalarParameter(p) {
					found = true
					return false
				}
				return true
			})
			return false
		case *syntax.ParamExp, *syntax.BraceExp, *syntax.ArithmExp, *syntax.CmdSubst, *syntax.ProcSubst, *syntax.ExtGlob:
			found = true
			return false
		case *syntax.Lit:
			if strings.ContainsAny(n.Value, "*?[") {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// splitGateArguments reports an expansion that can produce extra arguments.
// Redirect targets are excluded.
func splitGateArguments(cmd string) bool {
	return splitGateArgumentsMode(cmd, false)
}

// splitGateArgumentsForGate permits only the narrowly proven unquoted read-query case below. lark-cli's flags keep
// the stricter splitGateArguments check.
func splitGateArgumentsForGate(cmd string) bool {
	return splitGateArgumentsMode(cmd, true)
}

func splitGateArgumentsMode(cmd string, unmatchedReadQuery bool) bool {
	cmd = strings.TrimSpace(cmd)
	for {
		stripped := false
		for _, keyword := range []string{"do", "then", "else"} {
			if len(cmd) > len(keyword) && cmd[:len(keyword)] == keyword && strings.ContainsRune(" \t\n", rune(cmd[len(keyword)])) {
				cmd = strings.TrimSpace(cmd[len(keyword):])
				stripped = true
			}
		}
		if !stripped {
			break
		}
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		return true
	}
	found := false
	syntax.Walk(tree, func(node syntax.Node) bool {
		if found {
			return false
		}
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		for i, arg := range call.Args {
			if unmatchedReadQuery && gateUnmatchedReadQuery(call.Args, i) {
				continue
			}
			found = wordExpandsToMany(arg)
			if found {
				break
			}
		}
		return false
	})
	return found
}

// gateUnmatchedReadQuery proves that an unquoted '?' in the final argument cannot produce a different request.
// The method is a literal GET/HEAD, the route is absolute, no other glob or expansion occurs, the pathname glob has
// no match, and the whole word still passes the deletion-action boundary with its query retained as path text. With
// no match, an ordinary shell leaves the bytes alone; nullglob removes only the final route, leaving no request, and
// failglob runs nothing. A match, a later argument or a non-read method stays unknown.
func gateUnmatchedReadQuery(args []*syntax.Word, index int) bool {
	if index < 2 || index != len(args)-1 {
		return false
	}
	method, methodDynamic := executionWord(args[index-1])
	if methodDynamic || method != "GET" && method != "HEAD" {
		return false
	}
	value, dynamic := executionWord(args[index])
	if dynamic || !strings.HasPrefix(value, "/") || !strings.Contains(value, "?") || strings.ContainsAny(value, "*[") {
		return false
	}
	_, query, found := strings.Cut(value, "?")
	if !found || !strings.Contains(query, "=") || !larkBypassSafe(larkCall{Method: method, Path: strings.ToLower(value)}) {
		return false
	}
	matches, err := filepath.Glob(value)
	return err == nil && len(matches) == 0
}

// expandKnownVars replaces $NAME and ${NAME} with their values in vars. A bare $NAME followed by `:` or `[` stays: zsh
// applies a modifier ($F:s/x/y/) or a subscript there, so the value is not what the shell would use.
func expandKnownVars(s string, vars map[string]string) string {
	if !strings.Contains(s, "$") {
		return s
	}
	var out strings.Builder
	last := 0
	for _, m := range shellVarRef.FindAllStringIndex(s, -1) {
		ref := s[m[0]:m[1]]
		val, ok := vars[strings.Trim(ref, "${}")]
		if bare := !strings.HasPrefix(ref, "${"); bare && m[1] < len(s) && (s[m[1]] == ':' || s[m[1]] == '[') {
			ok = false
		}
		if strings.HasPrefix(ref, "${") && !strings.HasSuffix(ref, "}") {
			ok = false // ${NAME followed by an operator, not a plain reference
		}
		out.WriteString(s[last:m[0]])
		if ok {
			out.WriteString(val)
		} else {
			out.WriteString(ref)
		}
		last = m[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

// parseGateCall reads `METHOD PATH [BODY] [curl flags]` after a gated command.
func parseGateCall(g proto.CommandGate, args []string, vars map[string]string, env []string, cwd string) (larkCall, string, bool) {
	expand := func(s string) (string, bool) {
		if !strings.Contains(s, "$") {
			return s, true
		}
		out := expandKnownVars(s, vars)
		return out, !strings.Contains(out, "$")
	}
	// A literal method is part of the call's identity: even a previously
	// assigned variable could change before a loop invokes it. A literal GET
	// can keep variables in its query, but an unresolved route could hide a
	// deletion action or a configured deny rule.
	if len(args) < 2 || !httpMethod.MatchString(strings.ToUpper(args[0])) {
		return larkCall{}, "", false // a subcommand of its own (ol events …): not a call the rules know
	}
	method := strings.ToUpper(args[0])
	path, resolved := expand(args[1])
	path, _, _ = strings.Cut(path, "#") // curl never sends a URL fragment, including one after a raw path query.
	route, _, _ := strings.Cut(path, "?")
	if strings.Contains(route, "$") || strings.Contains(path, "$(") || strings.Contains(path, "`") || !resolved && method != "GET" {
		return larkCall{}, "", false
	}
	if _, err := validateGateProxyPath(strings.TrimPrefix(route, "/")); err != nil {
		return larkCall{}, "", false
	}
	c := larkCall{Method: method}
	if g.PathPrefix != "" {
		path = strings.TrimPrefix(path, g.PathPrefix)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	c.Path, c.Query, _ = strings.Cut(path, "?")
	queryFlagSeen, queryFragment, get, hasData := false, false, false, false
	accountHeader := gateAgentAccountHeader(g)
	accountHeaderValue := ""
	rest := args[2:]
	if len(rest) > 0 && (strings.HasPrefix(rest[0], "{") || strings.HasPrefix(rest[0], "[")) {
		c.Body, rest, hasData = rest[0], rest[1:], true
	} else if len(rest) > 0 && strings.HasPrefix(rest[0], "@") {
		c.Body, c.fileBody, c.relFile, rest = readBodyFile(rest[0][1:], cwd), true, !filepath.IsAbs(rest[0][1:]), rest[1:]
		hasData = true
	}
	for i := 0; i < len(rest); i++ {
		f := rest[i]
		switch f {
		case "-v", "--verbose", "--trace", "--trace-ascii", "--trace-time", "-K", "--config", "--libcurl", "-w", "--write-out", "-D", "--dump-header":
			return larkCall{}, "不要给 " + g.Command + " 加会打印请求头（含凭据）或改写请求的 curl 参数（" + f + "）。", false
		}
		name, val, inline := strings.Cut(f, "=")
		if strings.HasPrefix(f, "-X") && len(f) > 2 { // -XPOST
			name, val, inline = "-X", f[2:], true
		}
		takes, known := gateCurlFlags[name]
		if !known {
			return larkCall{}, "", false // a positional word would be another URL curl sends the token to
		}
		if takes && !inline {
			if i+1 >= len(rest) {
				return larkCall{}, "", false
			}
			i++
			val = rest[i]
		}
		switch name {
		case "-X", "--request":
			c.Method = strings.ToUpper(val) // curl takes the last -X
		case "-G", "--get":
			get = true
		case "-H", "--header":
			headerName, headerValue, _ := strings.Cut(val, ":")
			headerName = strings.ToLower(strings.TrimSpace(headerName))
			if !slices.Contains(g.Headers, headerName) {
				return larkCall{}, "", false
			}
			if headerName == accountHeader {
				accountHeaderValue = strings.TrimSpace(headerValue)
			}
		case "--upload":
			c.Body = "上传文件 " + val
		case "--url-query":
			if gateCurlQueryExpansionUnknown(val) {
				c.queryUnknown = true
			}
			query, relative := "", false
			if strings.HasPrefix(val, "+") {
				query = val[1:] // curl's leading plus says the rest is already encoded.
			} else if name, value, found := strings.Cut(val, "="); found {
				// A name is already literal: curl escapes only the value. A leading equals instead marks unnamed content,
				// which it encodes without emitting that equals sign.
				if name != "" {
					query = name + "="
				}
				query += gateCurlQueryEscape(value)
			} else if name, p, file := strings.Cut(val, "@"); file {
				// With no equals, name@file is curl's file form; @file is the same form without a name.
				if p == "-" {
					query = val
					c.queryUnknown = true
				} else if p == "" {
					return larkCall{}, "读不到 --url-query 文件 ：empty path", false
				} else if p == os.DevNull {
					// Curl reads os.DevNull as an empty file; skip opening the device node.
				} else {
					relative = !filepath.IsAbs(p)
					if relative {
						p = filepath.Join(cwd, p)
					}
					p = filepath.Clean(p)
					b, err := readSafeRegularBodyFile(p)
					if err != nil {
						return larkCall{}, "读不到 --url-query 文件 " + p + "：" + err.Error(), false
					}
					if len(b) > 0 { // curl adds nothing for an empty file, even when it has a name.
						query = gateCurlQueryEscape(string(b))
						if name != "" {
							query = name + "=" + query
						}
					}
				}
			} else {
				query = gateCurlQueryEscape(val)
			}
			// A literal name or leading-plus content may introduce the final URL's fragment. Unlike a fragment already in
			// the path (which curl parses before applying query flags), everything appended after this marker belongs to
			// that fragment. Escaped values and file contents contain %23 instead and never set this state.
			query, _, hasFragment := strings.Cut(query, "#")
			c.relFile = c.relFile || relative
			if queryFragment {
				continue
			}
			if queryFlagSeen {
				// Every later query flag adds a separator, even when the preceding argument was empty or the URL query
				// already ended in one. Empty arguments and files therefore remain visible in the raw query identity.
				c.Query += "&"
			} else if c.Query != "" && !strings.HasSuffix(c.Query, "&") {
				// The first query flag appends to a nonempty URL query, but does not duplicate its trailing separator.
				c.Query += "&"
			}
			c.Query += query
			queryFlagSeen = true
			queryFragment = hasFragment
		case "-d", "--data", "--data-raw", "--data-urlencode":
			hasData = true
			if (name == "-d" || name == "--data") && strings.HasPrefix(val, "@") ||
				name == "--data-urlencode" && hasCurlDataFile(val) {
				c.fileBody = true
			}
			if c.Body == "" {
				c.Body = val
			}
		}
	}
	if get && hasData {
		// Curl moves data into the URL query for -G, including GET/HEAD and even with -X POST, and its combination with
		// --url-query is not the order in which the flags appear. Mark every method unknown: rule-allowed calls can still
		// go, while larkCheck refuses to create a confirmation grant for a different request.
		c.queryUnknown = true
		c.dataInQuery = true
	}
	if _, agent := gateAgentToken(g); agent {
		account := ""
		if name := gateAgentAccountEnv(g); name != "" {
			account = g.Env[name]
			if value, ok := vars[name]; ok {
				account = value
			}
			for _, assignment := range env {
				if key, value, ok := strings.Cut(assignment, "="); ok && key == name {
					account = value
				}
			}
		}
		if account == "" {
			account = gateAgentQueryAccount(c.Query)
		}
		if accountHeaderValue != "" {
			account = accountHeaderValue
		}
		c.Account = normalizeAgentAccount(account)
	}
	return c, "", true
}

// gateCurlQueryExpansionUnknown reports every shell expansion in a raw --url-query argument. Quote information is gone
// by this point, so even a previously assigned $NAME is unsafe to expand: single quotes would have kept it literal.
// Literal dollars that cannot start an expansion remain data; +a=%24value is the explicit preencoded literal alternative.
func gateCurlQueryExpansionUnknown(raw string) bool {
	if strings.Contains(raw, "`") || shellVarRef.MatchString(raw) {
		return true
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '$' || i+1 == len(raw) {
			continue
		}
		next := raw[i+1]
		if next == '{' || next == '(' || next == '[' || next >= '0' && next <= '9' ||
			next >= 'A' && next <= 'Z' || next >= 'a' && next <= 'z' || next == '_' || strings.ContainsRune("*@#?$!-", rune(next)) {
			return true // an unsupported, positional, special, arithmetic or command substitution
		}
		// Every other dollar is literal data (for example the $' in an already quoted reserved-byte argument).
	}
	return false
}

// gateCurlQueryEscape is curl --url-query's form escaping without its leading-plus or @file handling: unreserved bytes
// stay as they are, a space becomes plus, and every other byte becomes a lowercase percent escape. Escaping bytes rather
// than URL-parsing the argument also keeps existing percent signs, ampersands and equals signs from acquiring structure.
func gateCurlQueryEscape(s string) string {
	const hex = "0123456789abcdef"
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.~", rune(c)) {
			out.WriteByte(c)
		} else if c == ' ' {
			out.WriteByte('+')
		} else {
			out.WriteByte('%')
			out.WriteByte(hex[c>>4])
			out.WriteByte(hex[c&15])
		}
	}
	return out.String()
}

const maxBodyFileBytes = 1 << 20

// hasCurlDataFile reports whether a curl --data-urlencode value uses @file or name@file form.
func hasCurlDataFile(val string) bool {
	before, _, ok := strings.Cut(val, "@")
	return ok && !strings.Contains(before, "=")
}

// isLarkFilter reports whether words invokes a pure output-only filter from larkFilters, rejecting
// flags that write files, execute programs, or mutate system state (#8413).
func isLarkFilter(words []string) bool {
	if len(words) == 0 || !larkFilters[words[0]] {
		return false
	}
	return isSafeFilterInvocation(words[0], words[1:])
}

func isSafeFilterInvocation(base string, args []string) bool {
	switch base {
	case "sort":
		writesFile, runsProgram := inspectSortArgs(args)
		return !writesFile && !runsProgram
	case "date":
		return safeDateArgs(args)
	case "uniq":
		return safeUniqArgs(args)
	default:
		return true
	}
}

func sortExecutesProgram(words []string) bool {
	if len(words) == 0 || filepath.Base(words[0]) != "sort" {
		return false
	}
	_, runsProgram := inspectSortArgs(words[1:])
	return runsProgram
}

func isLongFlagPrefix(flag, full string, minPrefix int) bool {
	name, _, _ := strings.Cut(flag, "=")
	return len(name) >= minPrefix && strings.HasPrefix(full, name)
}

func inspectSortArgs(args []string) (writesFile, runsProgram bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if strings.HasPrefix(arg, "--") {
			if isLongFlagPrefix(arg, "--output", 3) {
				writesFile = true
				if !strings.Contains(arg, "=") {
					i++
				}
				continue
			}
			if isLongFlagPrefix(arg, "--compress-program", 4) {
				runsProgram = true
				if !strings.Contains(arg, "=") {
					i++
				}
				continue
			}
			if !strings.Contains(arg, "=") {
				switch arg {
				case "--key", "--field-separator", "--temporary-directory", "--buffer-size",
					"--batch-size", "--parallel", "--random-source", "--sort", "--files0-from":
					i++
				}
			}
			continue
		}
		if strings.HasPrefix(arg, "-") && len(arg) > 1 {
			for j := 1; j < len(arg); j++ {
				switch arg[j] {
				case 'o':
					writesFile = true
					if j+1 == len(arg) {
						i++
					}
					j = len(arg)
				case 'k', 't', 'T', 'S':
					if j+1 == len(arg) {
						i++
					}
					j = len(arg)
				}
			}
		}
	}
	return writesFile, runsProgram
}

func safeDateArgs(args []string) bool {
	afterEnd := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !afterEnd && arg == "--" {
			afterEnd = true
			continue
		}
		if !afterEnd && strings.HasPrefix(arg, "--") {
			if isLongFlagPrefix(arg, "--set", 3) {
				return false
			}
			if !strings.Contains(arg, "=") {
				switch arg {
				case "--date", "--file", "--reference", "--rfc-3339":
					i++
				}
			}
			continue
		}
		if !afterEnd && strings.HasPrefix(arg, "-") && len(arg) > 1 {
			for j := 1; j < len(arg); j++ {
				switch arg[j] {
				case 's':
					return false
				case 'd', 'f', 'r':
					if j+1 == len(arg) {
						i++
					}
					j = len(arg)
				case 'I':
					// GNU date -I accepts an optional attached argument (such as -Iseconds);
					// it never consumes the next word, and characters in that value are not flags.
					j = len(arg)
				}
			}
			continue
		}
		// GNU and BSD date treat any positional operand not starting with '+' as a clock-setting timestamp.
		if !strings.HasPrefix(arg, "+") {
			return false
		}
	}
	return true
}

// safeUniqArgs rejects uniq invocations with a second positional operand, which uniq writes to as its output file.
func safeUniqArgs(args []string) bool {
	operands := 0
	afterEnd := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !afterEnd && arg == "--" {
			afterEnd = true
			continue
		}
		if !afterEnd && strings.HasPrefix(arg, "--") {
			if !strings.Contains(arg, "=") {
				switch arg {
				case "--skip-fields", "--skip-chars", "--check-chars":
					i++
				}
			}
			continue
		}
		if !afterEnd && strings.HasPrefix(arg, "-") && len(arg) > 1 {
			for j := 1; j < len(arg); j++ {
				switch arg[j] {
				case 'f', 's', 'w':
					if j+1 == len(arg) {
						i++
					}
					j = len(arg)
				}
			}
			continue
		}
		operands++
		if operands > 1 {
			return false
		}
	}
	return true
}

func isVirtualFSPath(p string) bool {
	clean := filepath.ToSlash(filepath.Clean(p))
	for _, root := range []string{"/proc", "/dev", "/sys"} {
		if clean == root || strings.HasPrefix(clean, root+"/") {
			return true
		}
	}
	return false
}

func readSafeRegularBodyFile(p string) ([]byte, error) {
	if isVirtualFSPath(p) {
		return nil, errors.New("virtual filesystem path")
	}
	base := filepath.Base(p)
	if base == "" || base == "." || base == ".." || base == string(filepath.Separator) {
		return nil, errors.New("not a regular file")
	}
	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	if isVirtualFSPath(resolvedDir) || isVirtualFSPath(filepath.Join(resolvedDir, base)) {
		return nil, errors.New("virtual filesystem path")
	}
	root, err := os.OpenRoot(resolvedDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Confirm the pinned root inode still matches a canonical non-virtual directory after open.
	afterDir, err := filepath.EvalSymlinks(resolvedDir)
	if err != nil || isVirtualFSPath(afterDir) {
		return nil, errors.New("virtual filesystem path")
	}
	afterInfo, err := os.Lstat(afterDir)
	if err != nil {
		return nil, err
	}
	rootInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(afterInfo, rootInfo) {
		return nil, errors.New("parent directory changed during open")
	}
	st, err := root.Lstat(base)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 || !st.Mode().IsRegular() || dataless(st) {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > maxBodyFileBytes {
		return nil, errors.New("file too large")
	}
	f, err := openKnowledgeFile(root, base)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fst, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fst.Mode().IsRegular() || !os.SameFile(st, fst) {
		return nil, errors.New("not a regular file")
	}
	if fst.Size() > maxBodyFileBytes {
		return nil, errors.New("file too large")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBodyFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > maxBodyFileBytes {
		return nil, errors.New("file too large")
	}
	return b, nil
}

func readBodyFile(p, cwd string) string {
	if p == "" || p == "-" {
		return "（读不到 " + p + "）"
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	b, err := readSafeRegularBodyFile(p)
	if err != nil {
		return "（读不到 " + p + "）"
	}
	return textfmt.Clip(string(b), 4000)
}

var httpMethod = lazyre.New(`^(GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS)$`)

var (
	// timeoutArg is the duration of a leading `timeout 20 …` (also read by readsOnlyMemory).
	timeoutArg = lazyre.New(`^[0-9.]+[smh]?$`)
	// shellVarRef is a $NAME or ${NAME} reference parseGateCall expands.
	shellVarRef = lazyre.New(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)
	// commandNameRe is what safeCommandName lets onto PATH.
	commandNameRe = lazyre.New(`^[a-z][a-z0-9_-]{0,31}$`)
)

func larkHost(base string) string {
	h := base
	if i := strings.Index(h, "://"); i >= 0 {
		h = h[i+3:]
	}
	h, _, _ = strings.Cut(h, "/")
	return h
}
