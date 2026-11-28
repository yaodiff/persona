package node

import (
	"path/filepath"
	"strconv"
	"strings"
)

// The login shell is bash on many Linux machines (no zsh at all, or a login shell left at bash): launcherScript then
// prints `alias -p` (bash has no zsh's (z) and (Q) to split the words itself, and bash 3.2 on macOS no BASH_ALIASES),
// marked by bashAliasMark, and bashAliases turns it into the records zsh prints, so the same parsers read both.
const bashAliasMark = "\x02"

// launcherShell is the shell the launchers are listed and started in (loginShellProgram) and the rc file that defines
// them there, for the group titles in Settings › Model providers: zsh reads ~/.zshrc; bash run interactively (`bash -ic`,
// also Git Bash on Windows) reads ~/.bashrc, and as a login shell on macOS (`bash -lic`, shellFlag) ~/.bash_profile.
func launcherShell() (shell, rcFile string) {
	if strings.TrimSuffix(filepath.Base(loginShellProgram()), ".exe") == "zsh" {
		return "zsh", "~/.zshrc"
	}
	return "bash", bashRCFile(shellFlag("bash"))
}

// bashRCFile is the file bash started with flag reads the launchers from.
func bashRCFile(flag string) string {
	if strings.Contains(flag, "l") {
		return "~/.bash_profile"
	}
	return "~/.bashrc"
}

// bashAliases is `alias -p` (`alias name='value'`, one a line) as zsh's records: name US word US … RS, the words of
// the value split and unquoted the way the shell would, separators (; && || | &) words of their own.
func bashAliases(out string) string {
	var b strings.Builder
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "alias ")
		if !ok {
			continue
		}
		rest = strings.TrimPrefix(rest, "-- ")
		name, value, ok := strings.Cut(rest, "=")
		if !ok || name == "" {
			continue
		}
		raw := shellWords(value)
		if len(raw) != 1 {
			continue
		}
		b.WriteString(name)
		b.WriteByte('\x1f')
		for _, w := range shellWords(raw[0]) {
			b.WriteString(w)
			b.WriteByte('\x1f')
		}
		b.WriteByte('\x1e')
	}
	return b.String()
}

// shellWords splits s into words as a POSIX shell would before expanding anything: blanks separate, quotes and
// backslashes are removed, and an unquoted ; & | && || is a word of its own. Nothing is expanded or run.
func shellWords(s string) []string {
	var words []string
	var cur strings.Builder
	in := false
	flush := func() {
		if in {
			words = append(words, cur.String())
			cur.Reset()
			in = false
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '\'':
			in = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				cur.WriteString(s[i+1:])
				i = len(s)
				continue
			}
			cur.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			in = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`", s[i+1]) >= 0 {
					i++
				}
				cur.WriteByte(s[i])
			}
		case c == '\\':
			in = true
			if i+1 < len(s) {
				i++
				cur.WriteByte(s[i])
			}
		case c == ';' || c == '&' || c == '|':
			flush()
			op := string(c)
			if c != ';' && i+1 < len(s) && s[i+1] == c {
				op += string(c)
				i++
			}
			words = append(words, op)
		default:
			in = true
			cur.WriteByte(c)
		}
	}
	flush()
	return words
}

// passwdShell is the base name of the login shell of uid in an /etc/passwd file; "" when it has no entry.
func passwdShell(passwd string, uid int) string {
	id := strconv.Itoa(uid)
	for _, line := range strings.Split(passwd, "\n") {
		f := strings.Split(strings.TrimSpace(line), ":")
		if len(f) == 7 && f[2] == id && f[6] != "" {
			return filepath.Base(f[6])
		}
	}
	return ""
}
