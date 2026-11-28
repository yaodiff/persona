package assistant

import (
	"encoding/json"
	"path"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// readonlyTaskSafeBash reports whether a Bash permission request from an active read-only task is a
// strictly read-only inspection command (for example git show/diff/log, gh pr view/diff, rg/grep/cat
// within the task workspace). It fails closed on any unparseable syntax, command substitution,
// output redirection, inline environment override, mutating subcommand, dangerous flag, or credential path.
func readonlyTaskSafeBash(raw json.RawMessage, cwd string) bool {
	if len(raw) == 0 {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return false
	}
	cmd := strings.TrimSpace(in.Command)
	if cmd == "" {
		return false
	}
	segs, ok := shellcmd.Split(cmd)
	if !ok || len(segs) == 0 {
		return false
	}
	for _, seg := range segs {
		if len(seg.Words) == 0 || seg.Subst || seg.ArgSubst || len(seg.Nested) > 0 {
			return false
		}
		if hasUnsafeReadonlyShellExpansion(seg.Text) {
			return false
		}
		if !safeReadonlyRedirect(seg.Redirect) {
			return false
		}
		// Reject inline VAR=val overrides (GIT_PAGER, GIT_EXTERNAL_DIFF, etc.) and wrappers (env/exec/command).
		if shellcmd.CommandIndex(seg.Words, map[string]string{}) != 0 {
			return false
		}
		if !safeReadonlySegment(seg.Words, cwd) {
			return false
		}
	}
	return true
}

func safeReadonlyRedirect(redirect string) bool {
	if strings.TrimSpace(redirect) == "" {
		return true
	}
	for _, tok := range strings.Fields(redirect) {
		if tok != "2>&1" && tok != "2>/dev/null" {
			return false
		}
	}
	return true
}

// hasUnsafeReadonlyShellExpansion reports whether text contains shell expansions or line
// continuations that Bash evaluates before executing parsed words: unquoted/double-quoted
// parameter expansion ($VAR, ${VAR}, $'...'), line continuations (\<newline>), unquoted
// brace expansion ({...}), or unquoted pathname globs (*, ?, [).
func hasUnsafeReadonlyShellExpansion(text string) bool {
	r := []rune(text)
	const (
		quoteNone = iota
		quoteSingle
		quoteDouble
	)
	mode := quoteNone
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch mode {
		case quoteSingle:
			if c == '\'' {
				mode = quoteNone
			}
		case quoteDouble:
			switch c {
			case '\\':
				if i+1 < len(r) {
					if r[i+1] == '\n' || r[i+1] == '\r' {
						return true
					}
					if strings.ContainsRune("\"\\$`", r[i+1]) {
						i++
					}
				}
			case '"':
				mode = quoteNone
			case '$':
				return true
			}
		default:
			switch c {
			case '\\':
				if i+1 < len(r) {
					if r[i+1] == '\n' || r[i+1] == '\r' {
						return true
					}
					i++
				}
			case '\'':
				mode = quoteSingle
			case '"':
				mode = quoteDouble
			case '$', '{', '}', '*', '?', '[':
				return true
			}
		}
	}
	return false
}

// matchesLongFlagPrefix reports whether flag (without any =value suffix) is an unambiguous
// prefix of blocked with at least minLen characters (matching getopt_long / git parse_options).
func matchesLongFlagPrefix(flag, blocked string, minLen int) bool {
	return len(flag) >= minLen && strings.HasPrefix(blocked, flag)
}

func safeReadonlySegment(words []string, cwd string) bool {
	switch words[0] {
	case "git":
		return safeReadonlyGit(words[1:], cwd)
	case "gh":
		return safeReadonlyGH(words[1:])
	case "rg":
		return safeReadonlySearch(words[1:], cwd, true)
	case "grep":
		return safeReadonlySearch(words[1:], cwd, false)
	case "cat":
		return safeReadonlyFileTool(words[1:], cwd, false, false, false, nil)
	case "head":
		return safeReadonlyFileTool(words[1:], cwd, false, false, false, map[string]bool{
			"-n": true, "-c": true, "--lines": true, "--bytes": true,
		})
	case "tail":
		return safeReadonlyFileTool(words[1:], cwd, true, false, false, map[string]bool{
			"-n": true, "-c": true, "--lines": true, "--bytes": true,
		})
	case "ls":
		// Do not include --color (optional =WHEN only, never consumes the next token) or -w / -I
		// (take no argument on BSD/macOS ls). Reject -L/-H/--dereference symlink following.
		return safeReadonlyFileTool(words[1:], cwd, false, false, true, map[string]bool{
			"--ignore": true, "--time-style": true, "--sort": true, "--format": true, "--width": true,
		})
	case "wc":
		return safeReadonlyFileTool(words[1:], cwd, false, true, false, nil)
	case "pwd":
		for _, a := range words[1:] {
			if a != "-L" && a != "-P" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func safeReadonlyGit(args []string, cwd string) bool {
	if readonlyGuardedDir(cwd) {
		return false
	}
	curCwd := cwd
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--no-pager" {
			i++
			continue
		}
		if a == "-C" {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
				return false
			}
			next := args[i+1]
			if filepath.IsAbs(next) {
				if !safeReadonlyPath(next, cwd) {
					return false
				}
				curCwd = filepath.Clean(next)
			} else {
				if !safeReadonlyPath(next, curCwd) {
					return false
				}
				if curCwd != "" {
					curCwd = filepath.Join(curCwd, filepath.FromSlash(normalizeReadonlySlash(next)))
				} else {
					curCwd = next
				}
			}
			if readonlyGuardedDir(curCwd) {
				return false
			}
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			// Reject -c, --config-env, --exec-path, --git-dir, --work-tree, --paginate, etc.
			return false
		}
		break
	}
	if i >= len(args) {
		return false
	}
	sub := args[i]
	switch sub {
	case "show", "diff", "log", "status", "rev-parse", "merge-base", "ls-files", "ls-tree", "cat-file", "grep", "blame", "describe", "shortlog":
	default:
		return false
	}
	rest := args[i+1:]
	afterDashDash := false
	grepHasPattern := false
	grepLongValueFlags := map[string]bool{
		"--after-context": true, "--before-context": true, "--context": true,
		"--max-count": true, "--max-depth": true, "--threads": true,
	}
	for j := 0; j < len(rest); j++ {
		a := rest[j]
		if !afterDashDash {
			if a == "--" {
				afterDashDash = true
				continue
			}
			if sub == "grep" && a == "-e" {
				if j+1 >= len(rest) {
					return false
				}
				grepHasPattern = true
				j++
				continue
			}
			if strings.HasPrefix(a, "--") {
				flag, _, hasVal := strings.Cut(a, "=")
				if matchesLongFlagPrefix(flag, "--ext-diff", len("--ext")) ||
					matchesLongFlagPrefix(flag, "--textconv", len("--textc")) ||
					matchesLongFlagPrefix(flag, "--no-index", len("--no-i")) ||
					matchesLongFlagPrefix(flag, "--output", len("--out")) ||
					matchesLongFlagPrefix(flag, "--exec", len("--exe")) ||
					matchesLongFlagPrefix(flag, "--paginate", len("--pag")) ||
					matchesLongFlagPrefix(flag, "--batch-command", len("--batch-co")) ||
					matchesLongFlagPrefix(flag, "--write-commit-graph", len("--write-c")) {
					return false
				}
				switch sub {
				case "cat-file":
					if matchesLongFlagPrefix(flag, "--textconv", len("--t")) ||
						matchesLongFlagPrefix(flag, "--filters", len("--fi")) {
						return false
					}
				case "blame":
					if matchesLongFlagPrefix(flag, "--contents", len("--con")) ||
						matchesLongFlagPrefix(flag, "--ignore-revs-file", len("--ignore-r")) {
						return false
					}
				case "ls-files":
					if matchesLongFlagPrefix(flag, "--exclude-from", len("--exclude-f")) ||
						matchesLongFlagPrefix(flag, "--exclude-per-directory", len("--exclude-p")) {
						return false
					}
				case "rev-parse":
					if matchesLongFlagPrefix(flag, "--resolve-git-dir", len("--resolve-g")) {
						return false
					}
				case "grep":
					if matchesLongFlagPrefix(flag, "--file", len("--fil")) ||
						matchesLongFlagPrefix(flag, "--open-files-in-pager", len("--op")) ||
						matchesLongFlagPrefix(flag, "--untracked", len("--u")) ||
						matchesLongFlagPrefix(flag, "--no-exclude-standard", len("--no-ex")) {
						return false
					}
					if grepLongValueFlags[flag] && !hasVal {
						if j+1 >= len(rest) {
							return false
						}
						j++
					}
				}
				continue
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				if strings.HasPrefix(a, "-o") || strings.HasPrefix(a, "-x") {
					return false
				}
				switch sub {
				case "diff", "show", "log":
					bundle := a[1:]
					for k := 0; k < len(bundle); k++ {
						ch := bundle[k]
						if ch == 'o' || ch == 'O' || ch == 'x' {
							return false
						}
						if strings.ContainsRune("nUSGLMCBl", rune(ch)) {
							break
						}
					}
				case "ls-files":
					bundle := a[1:]
					for k := 0; k < len(bundle); k++ {
						ch := bundle[k]
						if ch == 'o' || ch == 'x' || ch == 'X' {
							return false
						}
					}
				case "blame":
					bundle := a[1:]
					for k := 0; k < len(bundle); k++ {
						ch := bundle[k]
						if ch == 'S' || ch == 'o' || ch == 'x' {
							return false
						}
						if ch == 'L' || ch == 'C' || ch == 'M' {
							break
						}
					}
				case "grep":
					bundle := a[1:]
					for k := 0; k < len(bundle); k++ {
						ch := bundle[k]
						if ch == 'f' || ch == 'O' || ch == 'o' || ch == 'x' {
							return false
						}
						if ch == 'e' {
							grepHasPattern = true
							if k == len(bundle)-1 {
								if j+1 >= len(rest) {
									return false
								}
								j++
							}
							break
						}
						if ch == 'A' || ch == 'B' || ch == 'C' || ch == 'm' {
							if k == len(bundle)-1 {
								if j+1 >= len(rest) {
									return false
								}
								j++
							}
							break
						}
					}
				}
				continue
			}
			if sub == "grep" && !grepHasPattern {
				grepHasPattern = true
				continue
			}
		}
		if !safeReadonlyGitArg(a, curCwd) {
			return false
		}
	}
	return true
}

func gitArgEmbeddedPaths(arg string) []string {
	var out []string
	add := func(p string) {
		if p != "" {
			out = append(out, p)
		}
	}
	if strings.HasPrefix(arg, ":") {
		s := arg[1:]
		if strings.HasPrefix(s, "(") {
			if end := strings.IndexByte(s, ')'); end >= 0 {
				add(s[end+1:])
			}
		} else {
			// Short pathspec magic (:!, :^, :/) or stage syntax (:0:path).
			trimmed := strings.TrimLeft(s, "!^/")
			add(trimmed)
		}
	}
	for i := 0; i < len(arg); i++ {
		if arg[i] != ':' {
			continue
		}
		after := arg[i+1:]
		if after == "" {
			continue
		}
		if strings.HasPrefix(after, "(") {
			if end := strings.IndexByte(after, ')'); end >= 0 {
				add(after[end+1:])
				continue
			}
		}
		if i == 0 && strings.HasPrefix(after, "/") {
			add(strings.TrimLeft(after, "/"))
			continue
		}
		add(after)
	}
	return out
}

// safeReadonlyGitArg validates a non-flag git argument (revision, pathspec, or diff operand)
// so implicit --no-index paths (such as `git diff /dev/null /etc/passwd` or `.env`) and
// tilde/traversal/credential paths cannot escape the task workspace, while allowing quoted
// tracked-only git pathspecs (such as `git diff -- '*.go'`).
func safeReadonlyGitArg(arg, cwd string) bool {
	if arg == "" || strings.ContainsRune(arg, '\x00') || strings.HasPrefix(arg, "~") || strings.ContainsRune(arg, '$') {
		return false
	}
	if strings.ContainsRune(arg, '\\') || isUnsafeCrossOSOrUNCPath(arg) || hasWindowsDrivePrefix(arg) || readonlyGuardedTarget(arg, cwd) {
		return false
	}
	for _, seg := range strings.Split(normalizeReadonlySlash(arg), "/") {
		if seg == ".." || readonlyCredentialSegment(seg) {
			return false
		}
	}
	for _, sub := range gitArgEmbeddedPaths(arg) {
		if strings.HasPrefix(sub, "~") || strings.ContainsRune(sub, '\\') || isUnsafeCrossOSOrUNCPath(sub) || hasWindowsDrivePrefix(sub) || readonlyGuardedTarget(sub, cwd) {
			return false
		}
		for _, seg := range strings.Split(normalizeReadonlySlash(sub), "/") {
			if seg == ".." || readonlyCredentialSegment(seg) {
				return false
			}
		}
	}
	if filepath.IsAbs(arg) {
		if cwd == "" || !filepath.IsAbs(cwd) {
			return false
		}
		cleanCwd := filepath.Clean(cwd)
		cleanArg := filepath.Clean(arg)
		rel, err := filepath.Rel(cleanCwd, cleanArg)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return false
		}
		for _, seg := range strings.Split(normalizeReadonlySlash(rel), "/") {
			if seg == ".." || readonlyCredentialSegment(seg) {
				return false
			}
		}
	}
	return true
}

func ghShortValueFlags(cmd, sub string) string {
	switch cmd + " " + sub {
	case "pr view", "pr status", "issue view", "issue status":
		return "Rqt"
	case "pr diff":
		return "Re"
	case "pr list":
		return "RqtAaBHlLSs"
	case "pr checks":
		return "Rqti"
	case "issue list":
		return "RqtAalLSsm"
	case "repo view":
		return "qtb"
	case "run view":
		return "Rqtaj"
	case "run list":
		return "RqtbceLsu"
	default:
		return ""
	}
}

func safeReadonlyGH(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[0] {
	case "pr":
		switch args[1] {
		case "view", "diff", "list", "status", "checks":
		default:
			return false
		}
	case "issue":
		switch args[1] {
		case "view", "list", "status":
		default:
			return false
		}
	case "repo":
		if args[1] != "view" {
			return false
		}
	case "run":
		switch args[1] {
		case "view", "list":
		default:
			return false
		}
	default:
		return false
	}
	valFlags := ghShortValueFlags(args[0], args[1])
	for j := 2; j < len(args); j++ {
		a := args[j]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "--") {
			flag, _, _ := strings.Cut(a, "=")
			if flag == "--web" || flag == "--output" || flag == "--watch" {
				return false
			}
			continue
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			short, _, _ := strings.Cut(a[1:], "=")
			for k := 0; k < len(short); k++ {
				ch := short[k]
				if ch == 'w' || ch == 'o' {
					return false
				}
				// Stop scanning only when a short flag actually takes a value for this subcommand
				// so boolean flags (e.g. `gh run view -e`) cannot skip a following --web/-w/--watch.
				if strings.ContainsRune(valFlags, rune(ch)) {
					if k == len(short)-1 && !strings.ContainsRune(a, '=') {
						if j+1 >= len(args) {
							return false
						}
						j++
					}
					break
				}
			}
		}
	}
	return true
}

func safeReadonlySearch(args []string, cwd string, isRG bool) bool {
	if readonlyGuardedDir(cwd) {
		return false
	}
	valueFlags := map[string]bool{
		"-e": true, "--regexp": true,
		"-A": true, "--after-context": true,
		"-B": true, "--before-context": true,
		"-C": true,
		"-m": true, "--max-count": true,
		"--label": true, "--binary-files": true,
		"-d": true, "--directories": true,
	}
	shortValueFlags := map[byte]bool{
		'A': true, 'B': true, 'C': true, 'm': true, 'd': true,
	}
	if isRG {
		for _, f := range []string{
			"-g", "--glob", "--iglob", "-t", "--type", "-T", "--type-not",
			"--max-depth", "--max-columns", "--max-filesize", "--sort", "--sortr",
			"-j", "--threads", "--engine", "-E", "--encoding", "--color", "--context",
		} {
			valueFlags[f] = true
		}
		for _, ch := range []byte{'g', 't', 'T', 'E', 'r', 'M', 'j'} {
			shortValueFlags[ch] = true
		}
	} else {
		for _, f := range []string{"--include", "--exclude", "--exclude-dir"} {
			valueFlags[f] = true
		}
		shortValueFlags['D'] = true
	}
	hasPattern := false
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endOfFlags {
			if a == "--" {
				endOfFlags = true
				continue
			}
			if strings.HasPrefix(a, "--") {
				flag, _, hasVal := strings.Cut(a, "=")
				if isRG && flag == "--files" {
					if hasVal {
						return false
					}
					hasPattern = true
					continue
				}
				if matchesLongFlagPrefix(flag, "--file", len("--fil")) ||
					matchesLongFlagPrefix(flag, "--pre", len("--pre")) ||
					matchesLongFlagPrefix(flag, "--pre-glob", len("--pre-g")) ||
					matchesLongFlagPrefix(flag, "--search-zip", len("--search-z")) ||
					matchesLongFlagPrefix(flag, "--ignore-file", len("--ignore-f")) ||
					matchesLongFlagPrefix(flag, "--ignore-file-case-insensitive", len("--ignore-file-c")) ||
					matchesLongFlagPrefix(flag, "--exclude-from", len("--exclude-f")) ||
					matchesLongFlagPrefix(flag, "--hostname-bin", len("--hostname-b")) {
					return false
				}
				if isRG && (matchesLongFlagPrefix(flag, "--follow", len("--fol")) ||
					matchesLongFlagPrefix(flag, "--hidden", len("--hid")) ||
					matchesLongFlagPrefix(flag, "--no-ignore", len("--no-ig")) ||
					strings.HasPrefix(flag, "--no-ignore-") ||
					matchesLongFlagPrefix(flag, "--unrestricted", len("--unr"))) {
					return false
				}
				if !isRG && (matchesLongFlagPrefix(flag, "--file", len("--fi")) ||
					matchesLongFlagPrefix(flag, "--dereference-recursive", len("--der"))) {
					return false
				}
				if flag == "--regexp" {
					hasPattern = true
					if !hasVal {
						if i+1 >= len(args) {
							return false
						}
						i++
					}
					continue
				}
				if valueFlags[flag] && !hasVal {
					if i+1 >= len(args) {
						return false
					}
					i++
				}
				continue
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				if a == "-e" {
					if i+1 >= len(args) {
						return false
					}
					hasPattern = true
					i++
					continue
				}
				if valueFlags[a] {
					if i+1 >= len(args) {
						return false
					}
					i++
					continue
				}
				// Short flag bundle (e.g. -ni, -nE); stop at the first value-taking short flag
				// so attached flag values (e.g. -Tmake, -g'*.feature') are not misparsed as flags.
				bundle := a[1:]
				for j := 0; j < len(bundle); j++ {
					ch := bundle[j]
					if ch == 'f' || (isRG && (ch == 'z' || ch == 'L' || ch == 'u' || ch == '.')) || (!isRG && ch == 'R') {
						return false
					}
					if ch == 'e' {
						hasPattern = true
						if j == len(bundle)-1 {
							if i+1 >= len(args) {
								return false
							}
							i++
						}
						break
					}
					if shortValueFlags[ch] {
						if j == len(bundle)-1 {
							if i+1 >= len(args) {
								return false
							}
							i++
						}
						break
					}
				}
				continue
			}
		}
		if !hasPattern {
			hasPattern = true
			continue
		}
		if !safeReadonlyPath(a, cwd) {
			return false
		}
	}
	return hasPattern
}

func safeReadonlyFileTool(args []string, cwd string, noFollow, rejectFiles0, noDeref bool, valueFlags map[string]bool) bool {
	if readonlyGuardedDir(cwd) {
		return false
	}
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !endOfFlags {
			if a == "--" {
				endOfFlags = true
				continue
			}
			if strings.HasPrefix(a, "--") {
				flag, _, hasVal := strings.Cut(a, "=")
				if noFollow && (matchesLongFlagPrefix(flag, "--follow", len("--f")) || matchesLongFlagPrefix(flag, "--pid", len("--p"))) {
					return false
				}
				if rejectFiles0 && matchesLongFlagPrefix(flag, "--files0-from", len("--f")) {
					return false
				}
				if noDeref && (strings.HasPrefix(flag, "--dereference") || matchesLongFlagPrefix(flag, "--dereference", len("--de"))) {
					return false
				}
				if valueFlags[flag] && !hasVal {
					if i+1 >= len(args) {
						return false
					}
					i++
				}
				continue
			}
			if strings.HasPrefix(a, "-") && len(a) > 1 {
				if noFollow && strings.ContainsAny(a[1:], "fF") {
					return false
				}
				if noDeref && strings.ContainsAny(a[1:], "LH") {
					return false
				}
				if valueFlags[a] {
					if i+1 >= len(args) {
						return false
					}
					i++
				}
				continue
			}
		}
		if !safeReadonlyPath(a, cwd) {
			return false
		}
	}
	return true
}

func normalizeReadonlySlash(p string) string {
	return strings.ReplaceAll(filepath.ToSlash(p), "\\", "/")
}

func hasWindowsDrivePrefix(p string) bool {
	return len(p) >= 2 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':'
}

func isUnsafeCrossOSOrUNCPath(arg string) bool {
	if strings.HasPrefix(arg, `\`) {
		return true
	}
	norm := normalizeReadonlySlash(arg)
	if strings.HasPrefix(norm, "//") {
		return true
	}
	if strings.HasPrefix(norm, "/") && !filepath.IsAbs(arg) {
		return true
	}
	return false
}

func readonlyGuardedTarget(arg, cwd string) bool {
	normArg := normalizeReadonlySlash(arg)
	if readonlyGuardedDir(normArg) {
		return true
	}
	if cwd != "" && !filepath.IsAbs(arg) && !strings.HasPrefix(normArg, "/") {
		if readonlyGuardedDir(normalizeReadonlySlash(cwd) + "/" + normArg) {
			return true
		}
	}
	return false
}

func safeReadonlyPath(arg, cwd string) bool {
	if arg == "" || strings.ContainsRune(arg, '\x00') {
		return false
	}
	if strings.HasPrefix(arg, "~") || strings.ContainsAny(arg, "*?[]{}$") || hasWindowsDrivePrefix(arg) || isUnsafeCrossOSOrUNCPath(arg) {
		return false
	}
	if readonlyGuardedDir(cwd) || readonlyGuardedTarget(arg, cwd) {
		return false
	}
	for _, seg := range strings.Split(normalizeReadonlySlash(arg), "/") {
		if seg == ".." || readonlyCredentialSegment(seg) {
			return false
		}
	}
	if filepath.IsAbs(arg) {
		if cwd == "" || !filepath.IsAbs(cwd) {
			return false
		}
		cleanCwd := filepath.Clean(cwd)
		cleanArg := filepath.Clean(arg)
		rel, err := filepath.Rel(cleanCwd, cleanArg)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return false
		}
		for _, seg := range strings.Split(normalizeReadonlySlash(rel), "/") {
			if seg == ".." || readonlyCredentialSegment(seg) {
				return false
			}
		}
	}
	return true
}

func readonlyGuardedDir(dir string) bool {
	if dir == "" {
		return false
	}
	slash := strings.ToLower(path.Clean(normalizeReadonlySlash(dir)))
	if slash == ".config/gh" || strings.HasPrefix(slash, ".config/gh/") ||
		strings.HasSuffix(slash, "/.config/gh") || strings.Contains(slash, "/.config/gh/") ||
		slash == ".config/gcloud" || strings.HasPrefix(slash, ".config/gcloud/") ||
		strings.HasSuffix(slash, "/.config/gcloud") || strings.Contains(slash, "/.config/gcloud/") {
		return true
	}
	for _, seg := range strings.Split(slash, "/") {
		if readonlyCredentialSegment(seg) {
			return true
		}
	}
	return false
}

func readonlyCredentialSegment(seg string) bool {
	n := strings.ToLower(seg)
	switch {
	case n == "", n == ".":
		return false
	case n == ".env", strings.HasPrefix(n, ".env."), strings.HasSuffix(n, ".env"),
		strings.HasSuffix(n, ".cred"), strings.HasSuffix(n, ".creds"),
		strings.HasSuffix(n, ".pem"), strings.HasSuffix(n, ".key"),
		strings.HasSuffix(n, ".p12"), strings.HasSuffix(n, ".pfx"),
		strings.HasSuffix(n, ".keychain"), strings.HasSuffix(n, ".keychain-db"),
		strings.HasPrefix(n, "id_rsa"), strings.HasPrefix(n, "id_ed25519"), strings.HasPrefix(n, "id_ecdsa"),
		n == "hub.json", n == "credentials.json", n == "auth.json", n == "secrets.json", n == "admin.cred",
		n == ".netrc", n == ".npmrc", n == ".pypirc", n == ".git-credentials", n == "hosts.yml",
		n == ".ssh", n == ".aws", n == ".gnupg", n == ".docker", n == ".kube",
		n == ".persona", n == compat.StateDir, n == ".claude", n == ".codex", n == ".claude.json",
		strings.Contains(n, "launcher-settings"):
		return true
	}
	return false
}
