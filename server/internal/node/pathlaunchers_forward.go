package node

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// forwardedClaudeWrapper reads a thin exec wrapper and one helper with fixed arguments.
// It adds only default-endpoint launchers with a known config directory, leaving the
// existing endpoint inventory alone. The helper and its setup commands are never run.
func forwardedClaudeWrapper(path, head, home string) (wrapper, bool) {
	if !strings.Contains(head, "exec") || !strings.Contains(head, "@") {
		return wrapper{}, false
	}
	stmts := launcherShellStatements(head)
	if len(stmts) != 1 || !filepath.IsAbs(path) {
		return wrapper{}, false
	}
	call := launcherSimpleCall(stmts[0])
	if call == nil || len(call.Args) < 3 || call.Args[0].Lit() != "exec" || launcherQuotedParam(call.Args[len(call.Args)-1]) != "@" {
		return wrapper{}, false
	}
	shell := launcherScriptState{vars: map[string]string{"0": path, "HOME": home}}
	target, ok := shell.word(call.Args[1], false, false)
	if !ok || !filepath.IsAbs(target) || filepath.Base(target) == "claude" {
		return wrapper{}, false
	}
	for _, word := range call.Args[2 : len(call.Args)-1] {
		arg, ok := shell.word(word, false, false)
		if !ok {
			return wrapper{}, false
		}
		shell.args = append(shell.args, arg)
	}
	st, err := os.Stat(target)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 || st.Size() > scriptMaxSize {
		return wrapper{}, false
	}
	head, err = scriptHead(target)
	if err != nil {
		return wrapper{}, false
	}
	shell.vars["0"] = target
	return shell.readHelper(launcherShellStatements(head))
}

type launcherScriptState struct {
	vars map[string]string
	args []string
}

// launcherShellStatements uses the shell grammar to distinguish executable code
// from quotes, comments, here-documents and nested commands.
func launcherShellStatements(head string) []*syntax.Stmt {
	first, _, _ := strings.Cut(head, "\n")
	interpreter, ok := strings.CutPrefix(first, "#!")
	if !ok {
		return nil
	}
	fields := strings.Fields(interpreter)
	if len(fields) == 2 && filepath.Base(fields[0]) == "env" {
		fields = fields[1:]
	}
	if len(fields) != 1 {
		return nil
	}
	switch filepath.Base(fields[0]) {
	case "sh", "bash", "dash":
	default:
		return nil
	}
	tree, err := syntax.NewParser().Parse(strings.NewReader(head), "")
	if err != nil {
		return nil
	}
	return tree.Stmts
}

func launcherSimpleCall(stmt *syntax.Stmt) *syntax.CallExpr {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(stmt.Redirs) != 0 || stmt.Background || stmt.Coprocess || stmt.Negated {
		return nil
	}
	return call
}

func launcherQuotedParam(word *syntax.Word) string {
	if len(word.Parts) == 1 {
		if quoted, ok := word.Parts[0].(*syntax.DblQuoted); ok && !quoted.Dollar && len(quoted.Parts) == 1 {
			if p, ok := quoted.Parts[0].(*syntax.ParamExp); ok && launcherParam(p) {
				return p.Param.Value
			}
		}
	}
	return ""
}

func launcherParam(p *syntax.ParamExp) bool {
	return !p.Excl && !p.Length && !p.Width && p.Index == nil && p.Slice == nil && p.Repl == nil && p.Names == 0 && p.Exp == nil
}

// readHelper tracks straight-line assignments through the final exec. Setup
// commands and abort guards may do work, but cannot change the tracked variables
// on a path that reaches Claude. Unsupported shell control flow leaves it unknown.
func (s *launcherScriptState) readHelper(stmts []*syntax.Stmt) (wrapper, bool) {
	exported := map[string]bool{}
	for _, stmt := range stmts {
		if stmt.Background || stmt.Coprocess || stmt.Negated || len(stmt.Redirs) != 0 {
			break
		}
		switch c := stmt.Cmd.(type) {
		case *syntax.DeclClause:
			if c.Variant.Value == "export" && s.assign(c.Args, exported) {
				continue
			}
		case *syntax.CallExpr:
			if len(c.Args) == 0 {
				if s.assign(c.Assigns, nil) {
					continue
				}
				break
			}
			if len(c.Assigns) != 0 {
				break
			}
			switch c.Args[0].Lit() {
			case "exec":
				return s.claude(c.Args[1:], exported)
			case "unset":
				for _, arg := range c.Args[1:] {
					name := arg.Lit()
					if !launcherVariable(name) {
						return wrapper{}, false
					}
					delete(s.vars, name)
					delete(exported, name)
				}
				continue
			case "shift":
				if s.shift(c.Args[1:]) {
					continue
				}
			case "set":
				switch len(c.Args) {
				case 2:
					if flag := c.Args[1].Lit(); strings.HasPrefix(flag, "-") && strings.Trim(flag[1:], "eu") == "" && len(flag) > 1 {
						continue
					}
				case 3:
					if c.Args[1].Lit() == "-euo" && c.Args[2].Lit() == "pipefail" {
						continue
					}
				}
			default:
				if launcherSetup(stmt) {
					continue
				}
			}
		case *syntax.BinaryCmd:
			if launcherGuard(c) {
				continue
			}
		}
		break
	}
	return wrapper{}, false
}

// These variables can change how subsequent words split or commands resolve.
func launcherVariable(name string) bool {
	return syntax.ValidName(name) && name != "IFS" && name != "PATH"
}

func (s *launcherScriptState) assign(assigns []*syntax.Assign, export map[string]bool) bool {
	values := s
	if export != nil && len(assigns) > 1 {
		// The shell expands an export's arguments before applying any of them.
		before := *s
		before.vars = maps.Clone(s.vars)
		values = &before
	}
	for _, a := range assigns {
		if a.Name == nil || !launcherVariable(a.Name.Value) || a.Append || a.Array != nil || a.Index != nil || !launcherReadOnlyWord(a.Value) {
			return false
		}
		name := a.Name.Value
		if export != nil {
			export[name] = true
		}
		if !a.Naked {
			if value, ok := values.word(a.Value, true, false); ok {
				s.vars[name] = value
			} else {
				delete(s.vars, name)
			}
		}
	}
	return true
}

func (s *launcherScriptState) shift(words []*syntax.Word) bool {
	count := 1
	if len(words) == 1 {
		n, err := strconv.Atoi(words[0].Lit())
		if err != nil || n < 0 {
			return false
		}
		count = n
	} else if len(words) != 0 {
		return false
	}
	if count > len(s.args) {
		return false
	}
	s.args = s.args[count:]
	return true
}

func (s *launcherScriptState) claude(words []*syntax.Word, exported map[string]bool) (wrapper, bool) {
	if len(s.args) != 0 || len(words) < 2 || launcherQuotedParam(words[len(words)-1]) != "@" {
		return wrapper{}, false
	}
	program, ok := s.word(words[0], false, false)
	if !ok || filepath.Base(program) != "claude" {
		return wrapper{}, false
	}
	configDir := s.vars["CLAUDE_CONFIG_DIR"]
	if !exported["CLAUDE_CONFIG_DIR"] || !filepath.IsAbs(configDir) {
		return wrapper{}, false
	}
	if base, known := s.vars["ANTHROPIC_BASE_URL"]; exported["ANTHROPIC_BASE_URL"] && (!known || base != "") {
		return wrapper{}, false
	}
	w := wrapper{env: map[string]string{"CLAUDE_CONFIG_DIR": configDir}}
	for _, word := range words[1 : len(words)-1] {
		v, ok := s.word(word, false, false)
		if !ok || isScriptArgs(v) {
			return wrapper{}, false
		}
		w.args = append(w.args, v)
	}
	if !fixedClaudeArgs(w.args) {
		return wrapper{}, false
	}
	if settings := settingsArg(w.args); filepath.IsAbs(settings) {
		w.settings = settings
	}
	return w, true
}

// launcherGuard accepts a test followed by setup or an error path that exits.
// Any assignment or exec reached conditionally would make inference ambiguous.
func launcherGuard(c *syntax.BinaryCmd) bool {
	if c.Op != syntax.OrStmt && c.Op != syntax.AndStmt || !launcherTest(c.X) {
		return false
	}
	if launcherSetup(c.Y) {
		return true
	}
	if c.Op != syntax.OrStmt {
		return false
	}
	if launcherExit(c.Y) {
		return true
	}
	block, ok := c.Y.Cmd.(*syntax.Block)
	if !ok || len(c.Y.Redirs) != 0 || c.Y.Negated || c.Y.Background || c.Y.Coprocess || len(block.Stmts) == 0 {
		return false
	}
	last := len(block.Stmts) - 1
	for _, stmt := range block.Stmts[:last] {
		if !launcherSetup(stmt) {
			return false
		}
	}
	return launcherExit(block.Stmts[last])
}

func launcherTest(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return false
	}
	name := call.Args[0].Lit()
	return (name == "[" || name == "test") && launcherSetup(stmt)
}

func launcherExit(stmt *syntax.Stmt) bool {
	call := launcherSimpleCall(stmt)
	if call == nil || len(call.Args) != 2 || call.Args[0].Lit() != "exit" {
		return false
	}
	_, err := strconv.Atoi(call.Args[1].Lit())
	return err == nil
}

func launcherSetup(stmt *syntax.Stmt) bool {
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 || len(call.Args) == 0 || stmt.Background || stmt.Coprocess {
		return false
	}
	switch call.Args[0].Lit() {
	case "[", "test", ":", "true", "echo", "mkdir", "ln", "python", "python3", "touch":
	default:
		return false
	}
	for _, arg := range call.Args[1:] {
		if !launcherReadOnlyWord(arg) {
			return false
		}
	}
	for _, redir := range stmt.Redirs {
		if redir.N != nil && strings.HasPrefix(redir.N.Value, "{") || !launcherReadOnlyWord(redir.Word) || !launcherReadOnlyWord(redir.Hdoc) {
			return false
		}
	}
	return true
}

// Unknown values can be ignored for unrelated variables, provided expansion
// does not itself assign to variables in the current shell.
func launcherReadOnlyWord(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	safe := true
	syntax.Walk(word, func(node syntax.Node) bool {
		switch p := node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			return false // separate process
		case *syntax.ArithmExp:
			safe = false
		case *syntax.ParamExp:
			safe = safe && launcherParam(p)
		}
		return safe
	})
	return safe
}

// word resolves literal text and simple known parameters. It rejects words
// that could split, glob, or need real shell evaluation.
func (s *launcherScriptState) word(word *syntax.Word, assignment, quoted bool) (string, bool) {
	if word == nil {
		return "", true
	}
	var out strings.Builder
	for i, part := range word.Parts {
		var value string
		var ok bool
		switch p := part.(type) {
		case *syntax.Lit:
			value, ok = p.Value, !strings.Contains(p.Value, `\`)
			if !quoted {
				ok = ok && !strings.ContainsAny(value, "{}") && (assignment || !strings.ContainsAny(value, "*?["))
				if i == 0 && strings.HasPrefix(value, "~") {
					home := s.vars["HOME"]
					if home == "" || value != "~" && !strings.HasPrefix(value, "~/") {
						return "", false
					}
					value = home + value[1:]
				}
			}
		case *syntax.SglQuoted:
			value, ok = p.Value, !p.Dollar
		case *syntax.DblQuoted:
			if !p.Dollar {
				value, ok = s.word(&syntax.Word{Parts: p.Parts}, assignment, true)
			}
		case *syntax.ParamExp:
			if launcherParam(p) {
				key := p.Param.Value
				value, ok = s.vars[key]
				if pos, err := strconv.Atoi(key); err == nil && pos > 0 && pos <= len(s.args) {
					value, ok = s.args[pos-1], true
				}
				ok = ok && (quoted || assignment || value != "" && !strings.ContainsAny(value, " \t\n*?["))
			}
		case *syntax.CmdSubst:
			if quoted || assignment {
				value, ok = s.scriptDir(p)
			}
		}
		if !ok || out.Len()+len(value) > scriptHeadSize {
			return "", false
		}
		out.WriteString(value)
	}
	return out.String(), true
}

// scriptDir recognizes dirname "$0" and dirname "$(readlink -f "$0")"
// structurally. Their paths come from the file being read.
func (s *launcherScriptState) scriptDir(sub *syntax.CmdSubst) (string, bool) {
	var text bytes.Buffer
	if err := syntax.NewPrinter().Print(&text, sub); err != nil {
		return "", false
	}
	path := s.vars["0"]
	switch text.String() {
	case `$(dirname "$0")`:
		return filepath.Dir(path), true
	case `$(dirname "$(readlink -f "$0")")`:
		real, err := filepath.EvalSymlinks(path)
		return filepath.Dir(real), err == nil
	}
	return "", false
}
