package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"mvdan.cc/sh/v3/syntax"
)

// Where text naming lark-cli matters (#8119). Words only run as a command at a command's own position, which
// classifyLark reads; text anywhere else (a grep or perl pattern, a here-document written to a file, an echo) is data
// until a program executes it. That takes code a program is given (`python3 -c`, `awk`, caught by the program's own
// words), a program reading what is piped or redirected into it (a shell, an interpreter, xargs, ssh), or a file
// written now and run later in the same command. A script file's contents stay unread, as before (#4613).

// larkFlow is what classifyLark notes of one segment for larkTextRuns.
type larkFlow struct {
	text     string
	call     bool     // lark-cli's own call (or ssh's, read remotely): its words reach no other program
	filter   bool     // an output filter: it prints what it is given or told, and runs none of it
	program  bool     // anything else: a program that may run a file
	opaque   bool     // a program whose code the command does not show (a script, a shell reading its input)
	writes   bool     // it writes a file
	notes    []string // the notes it writes (larkNoteFile): run only by a program that names them (#8148)
	resolved bool     // this invocation came from a proven literal command-prefix assignment
}

// larkTextRuns: the command may run lark-cli where classifyLark cannot see it. That is a program with code the
// command does not show in a command that names lark-cli (as before #8119, minus inline code that runs nothing
// else), text naming lark-cli piped through filters into a program, or such text written to a file while a program
// runs as well. Text naming lark-cli that only filters see or print stays data. In a sequential list (one command
// after another, as written) only a program after the text was written can run the file, and a note (Markdown,
// plain text, a log, a table) only a program that names it (#8148).
func larkTextRuns(cmd string, flow []larkFlow, sequential bool) bool {
	names := larkCLIRe.MatchString(cmd)
	pipelineCalls := larkPipelineCallFlags(cmd, flow)
	tainted, mentioned, writes, program, notesRun, pos := false, false, false, false, false, 0
	var notes []string
	runsNote := func(f larkFlow) bool {
		return f.program && slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(f.text, filepath.Base(n)) })
	}
	for i, f := range flow {
		own := !f.call && larkCLIRe.MatchString(f.text)
		piped := true // unless only another separator stands before the segment
		if at := strings.Index(cmd[pos:], f.text); at >= 0 {
			gap := cmd[pos : pos+at]
			piped = pos > 0 && (strings.Trim(gap, " \t\n\\|&;") != "" || larkPipedFrom(gap))
			pos += at + len(f.text)
		}
		// Provenance relaxes only a program outside a pipeline that actually calls
		// lark-cli. Within such a pipeline, variable-indirect and literal spellings agree.
		if names && f.opaque && (!f.resolved || pipelineCalls[i]) || piped && tainted && !f.filter {
			return true
		}
		tainted = f.filter && (own || piped && tainted)
		program = program || f.program && (!sequential || mentioned && writes)
		notesRun = notesRun || mentioned && runsNote(f)
		mentioned = mentioned || own
		writes = writes || f.writes
		notes = append(notes, f.notes...)
	}
	if !sequential {
		notesRun = notesRun || mentioned && slices.ContainsFunc(flow, runsNote)
	}
	return mentioned && (writes && program || notesRun)
}

// larkPipelineCallFlags marks each segment whose own pipeline contains a classified
// lark-cli call. Semicolon-separated pipelines stay independent: a read in one must
// not change the provenance boundary of an opaque program in another.
func larkPipelineCallFlags(cmd string, flow []larkFlow) []bool {
	groups := make([]int, len(flow))
	calls := map[int]bool{}
	pos, group := 0, 0
	for i, f := range flow {
		connected := false
		if at := strings.Index(cmd[pos:], f.text); at >= 0 {
			gap := cmd[pos : pos+at]
			connected = i > 0 && larkPipedFrom(gap)
			pos += at + len(f.text)
		}
		if i > 0 && !connected {
			group++
		}
		groups[i] = group
		if f.call {
			calls[group] = true
		}
	}
	out := make([]bool, len(flow))
	for i, group := range groups {
		out[i] = calls[group]
	}
	return out
}

// larkPipedFrom: the command text before a segment ends in a pipe (`|`, `|&`), not `||`.
func larkPipedFrom(before string) bool {
	before = strings.TrimRight(before, " \t\n\\")
	before = strings.TrimSuffix(before, "&")
	return strings.HasSuffix(before, "|") && !strings.HasSuffix(before, "||")
}

// selfContainedCode: an interpreter given its whole program inline (python -c, node -e, ruby -e, perl -e, awk and
// sed scripts) that starts no program, evaluates no other code and loads none: the program is all in the command,
// so its own words show whether it names lark-cli. A script file, a module or code read from input is not.
func selfContainedCode(words []string) bool {
	if len(words) < 2 {
		return false
	}
	base := filepath.Base(words[0])
	code, ok := "", false
	switch {
	case base == "awk" || base == "gawk":
		code, ok = larkInlineScript(words[1:], "-F -v", "")
		ok = ok && !strings.ContainsAny(code, "|") && !strings.Contains(code, "system") && !strings.Contains(code, "getline")
		return ok
	case base == "sed":
		code, ok = larkInlineScript(words[1:], "", "-n -E -r")
		return ok && sedScriptReadsOnly(code)
	case larkInterpreter.MatchString(base):
		flag := "-e"
		if strings.HasPrefix(base, "python") {
			flag = "-c"
		}
		for i := 1; i < len(words); i++ {
			w := words[i]
			if w == flag || base == "node" && (w == "--eval" || w == "-p" || w == "--print") {
				if i+1 < len(words) {
					code, ok = words[i+1], true
					for _, a := range words[i+2:] {
						ok = ok && !strings.ContainsAny(a, "|<>&") // perl's <> runs "cmd|" given as a file
					}
				}
				break
			}
			if !strings.HasPrefix(w, "-") || w == "-" || w == "-m" || w == "-r" || w == "--require" || w == "--import" {
				return false // a program file, stdin, a module or a preload
			}
		}
	}
	if !ok {
		return false
	}
	return !larkCodeRuns.MatchString(code)
}

// larkCodeRuns: what in inline code starts a program, evaluates code or loads it from elsewhere: calls, not words in
// its data (a message type "system" is no system()), and builtins only undotted (re.compile, json.load are fine).
var larkCodeRuns = lazyre.New("(?i)(?:^|[^.\\w])(?:eval|exec|compile|__import__|import|require)\\s*\\(" +
	"|\\b(?:system|exec[a-z]+|popen[0-9]*|spawn[a-z]*|fork|getoutput|check_output)\\s*\\(" +
	"|subprocess|importlib|runpy|child_process|ctypes|getattr|__builtins__|builtins|__dict__|globals\\s*\\(|\\bpty\\.|`|\\bqx\\W|%x[{(\\[]|open\\s*\\(\\s*[\"']\\s*\\|")

// sedScriptReadsOnly ignores letters inside /regular-expression/ addresses and the pattern and replacement of an
// s command. Elsewhere e executes a command and w/W writes a file, including when they are s flags. Unknown or
// unterminated syntax stays opaque. This avoids mistaking "/## Messages/,/## Summary/p" for execution without
// allowing sed's actual evaluation and file-writing forms.
func sedScriptReadsOnly(code string) bool {
	section := func(start int, delimiter byte) (int, bool) {
		for i := start; i < len(code); i++ {
			if code[i] == '\\' {
				i++ // an escaped delimiter (or backslash) remains part of the section
				continue
			}
			if code[i] == delimiter {
				return i + 1, true
			}
		}
		return len(code), false
	}
	for i := 0; i < len(code); {
		switch code[i] {
		case 'e', 'w', 'W':
			return false
		case '/':
			next, ok := section(i+1, '/')
			if !ok {
				return false
			}
			i = next
		case 's':
			if i+1 >= len(code) || code[i+1] == '\\' || code[i+1] == '\n' || code[i+1] == '\r' || code[i+1] == ' ' || code[i+1] == '\t' {
				return false
			}
			delimiter := code[i+1]
			next, ok := section(i+2, delimiter)
			if !ok {
				return false
			}
			next, ok = section(next, delimiter)
			if !ok {
				return false
			}
			i = next // flags stay visible: s///e evaluates and s///w writes
		default:
			i++
		}
	}
	return true
}

// larkInlineScript is the script of `awk|sed [options] SCRIPT [files…]` (or -e SCRIPT): valued lists the options
// that take a value, flags those that do not; any other option makes it unknown.
func larkInlineScript(args []string, valued, flags string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-e" && i+1 < len(args):
			return args[i+1], true
		case wrapperHasOption(valued, a) && i+1 < len(args):
			i++
		case wrapperHasOption(flags, a):
		case strings.HasPrefix(a, "-"):
			return "", false
		default:
			return a, true
		}
	}
	return "", false
}

// larkWritesFileVars: a segment's redirections write a file (anything but /dev/null or another descriptor); the notes
// among them (larkNoteFile) are listed apart. vars, when not nil, are the literal values earlier assignments in the
// command gave (classifyLarkRelaxed): a note's directory may then come from one (larkNotePath, #15860).
func larkWritesFileVars(redirect string, vars map[string]string) (writes bool, notes []string) {
	for _, t := range strings.Fields(redirect) {
		op := strings.TrimLeft(t, "0123456789&")
		if !strings.HasPrefix(op, ">") || redirectFD.MatchString(t) {
			continue
		}
		target := strings.TrimLeft(op, ">|")
		if path, ok := larkNotePath(target, vars); ok {
			target = path
		}
		switch {
		case target == "/dev/null":
		case larkNoteFile.MatchString(target):
			notes = append(notes, target)
		default:
			writes = true
		}
	}
	return writes, notes
}

// larkNotePath is a redirect target whose directory comes from literal variables (`$d/note.md`, `"${d}/note.md"`),
// with them filled in. The file's own name has to be written out: a program that runs the note then names it
// (larkTextRuns), as it would a literal path. ok is false for anything else, a variable this command did not set to a
// literal value included; the target then stays a write.
func larkNotePath(target string, vars map[string]string) (string, bool) {
	slash := strings.LastIndex(target, "/")
	if len(vars) == 0 || slash < 0 || !strings.Contains(target[:slash], "$") ||
		strings.ContainsAny(strings.Trim(target[slash+1:], `"'`), "$`\\\"'{}") {
		return "", false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(": "+target), "")
	if err != nil || len(file.Stmts) != 1 {
		return "", false
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) != 2 || len(file.Stmts[0].Redirs) != 0 {
		return "", false
	}
	path, _, ok := literalLoopWord(call.Args[1], vars)
	return path, ok && larkNoteFile.MatchString(path)
}

// larkNoteFile: a literal path to a note (Markdown, plain text, a log, a table). No program runs one unless it is
// told to: by its name.
var larkNoteFile = lazyre.New(`^[\w./~+-]+\.(?:md|markdown|txt|log|csv|tsv)$`)

// larkExecutableAssignment: an assignment that can change which program a command runs or what it loads. A PATH of
// directories already trusted here (trustedPATH) does not; on another machine PATH names that machine's directories
// and changes nothing here. extraPATH are the login shell's directories (larkExtraPATH).
func larkExecutableAssignment(w string, remote bool, extraPATH larkExtraPATH) bool {
	k, val, _ := strings.Cut(w, "=")
	if !larkExecutableEnv[k] {
		return false
	}
	return k != "PATH" || !remote && !trustedPATH(val, extraPATH)
}

// larkSystemPath: where lark-cli and the programs it starts are installed in the usual setups.
var larkSystemPath = []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/usr/local/bin", "/opt/homebrew/bin"}

// trustedPATH: every directory of a PATH value is one this process's PATH, the login shell's (extraPATH) or the system
// already has, so a command resolves among programs the gate already runs by name (an absolute lark-cli path is taken
// as lark-cli too). The login shell's directories are where the assistant's own Bash finds an npm global install that
// this process does not have (larkLoginPATH). $HOME, ~ and $PATH are read as here; any other expansion is not trusted.
func trustedPATH(val string, extraPATH larkExtraPATH) bool {
	known := map[string]bool{}
	extra := append([]string(nil), larkSystemPath...)
	if extraPATH != nil {
		extra = append(extra, extraPATH()...)
	}
	for _, d := range append(filepath.SplitList(os.Getenv("PATH")), extra...) {
		if filepath.IsAbs(d) {
			known[filepath.Clean(d)] = true
		}
	}
	home, _ := os.UserHomeDir()
	for _, d := range strings.Split(val, ":") {
		if d == "$PATH" || d == "${PATH}" {
			continue
		}
		for _, h := range []string{"$HOME", "${HOME}", "~"} {
			if rest, ok := strings.CutPrefix(d, h); ok && home != "" && (rest == "" || rest[0] == '/') {
				d = home + rest
				break
			}
		}
		if strings.ContainsAny(d, "$`~") || !known[filepath.Clean(d)] {
			return false
		}
	}
	return true
}

// sshRemoteCommand is the command `ssh [options] host [command…]` runs on the other machine, "" for none (a login
// shell, or -N). ok is false for what the gate does not follow: an option ssh does not document here, a subsystem
// (-s), a config file (-F) or an -o that runs a command of its own (ProxyCommand runs here).
func sshRemoteCommand(args []string) (string, bool) {
	const flags, values = "46AaCfGgKkMNnqTtVvXxYy", "BbcDEeIiJLlmOoPpQRSWw"
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			if i+1 >= len(args) {
				return "", false
			}
			return strings.Join(args[i+2:], " "), true
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return strings.Join(args[i+1:], " "), true // a is the host; ssh joins the rest for the remote shell
		}
		for j := 1; j < len(a); j++ {
			switch {
			case a[j] == 'N':
				return "", true
			case strings.IndexByte(flags, a[j]) >= 0:
			case strings.IndexByte(values, a[j]) >= 0:
				value := a[j+1:]
				if value == "" {
					if i++; i >= len(args) {
						return "", false
					}
					value = args[i]
				}
				if a[j] == 'o' {
					key := strings.ToLower(strings.TrimSpace(value))
					if f := strings.FieldsFunc(key, func(r rune) bool { return r == '=' || r == ' ' || r == '\t' }); len(f) > 0 {
						key = f[0]
					}
					if strings.HasSuffix(key, "command") || key == "include" {
						return "", false // ProxyCommand, LocalCommand, RemoteCommand, KnownHostsCommand…
					}
				}
				j = len(a)
			default:
				return "", false
			}
		}
	}
	return "", false // no host
}

// perlSubstitution: `perl [-0…] [-p|-n] [-i…] [-l] -e 's/A/B/flags' [files…]` with one substitution that evaluates no
// code (no /e, no (?{…}), no @{[…]} or ${\…} in it) over plain file names: it rewrites text and runs nothing,
// whatever its pattern says.
func perlSubstitution(args []string) bool {
	code := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case !strings.HasPrefix(a, "-"):
			// -p and -n open files the two-argument way: "cmd|" runs cmd, "<…" / ">…" change the mode.
			if code == "" || strings.ContainsAny(a, "|<>&") || strings.TrimSpace(a) != a {
				return false // a program file, or a "file" that runs or redirects
			}
		case code == "" && perlSwitchesE.MatchString(a) && i+1 < len(args):
			i++
			code = args[i]
		case perlSwitches.MatchString(a):
		default:
			return false
		}
	}
	code = strings.TrimSuffix(strings.TrimSpace(code), ";")
	if len(code) < 4 || code[0] != 's' || strings.Contains(code, "(?{") || strings.Contains(code, "(??{") ||
		strings.Contains(code, "@{") || strings.Contains(code, "${") {
		return false
	}
	d := code[1]
	if !strings.ContainsRune("/|#!,:%+", rune(d)) {
		return false
	}
	i, parts := 2, 0
	for ; i < len(code) && parts < 2; i++ {
		switch code[i] {
		case '\\':
			i++
		case d:
			parts++
		}
	}
	return parts == 2 && strings.Trim(code[i:], "msixpodualngcr") == ""
}

var (
	perlSwitches  = lazyre.New(`^-(?:0[0-7]*|[pnl])*(?:i\S*)?$`)
	perlSwitchesE = lazyre.New(`^-(?:0[0-7]*|[pnl])*e$`)
)
