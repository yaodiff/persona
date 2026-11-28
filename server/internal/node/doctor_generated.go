package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// checkGenerated compares each generated file with what this binary would write: current, stale (differs, or part
// of a plugin is missing) or missing.
func checkGenerated(opts DoctorOptions) []DoctorCheck {
	files, err := generatedFiles(opts.Bin, opts.Dir)
	if err != nil {
		return []DoctorCheck{{Name: "generated-files", Status: DoctorFail, Detail: err.Error(), Items: []string{}}}
	}
	var out []DoctorCheck
	for _, g := range generatedGroups {
		var issues []string
		total := 0
		for _, f := range files {
			if f.Group != g {
				continue
			}
			total++
			got, err := os.ReadFile(filepath.Join(opts.Dir, f.Rel))
			switch {
			case err != nil:
				issues = append(issues, "missing "+filepath.ToSlash(f.Rel))
			case !bytes.Equal(got, f.Content):
				issues = append(issues, "differs "+filepath.ToSlash(f.Rel))
			}
		}
		allMissing := len(issues) == total && allPrefix(issues, "missing")
		out = append(out, generatedCheck(g, issues, allMissing, true, opts.Lang))
	}
	issues := assistantskills.PluginIssues(filepath.Join(opts.Dir, agentPluginGroup))
	allMissing := len(issues) == 1 && issues[0] == "missing"
	return append(out, generatedCheck(agentPluginGroup, issues, allMissing, false, opts.Lang))
}

// generatedCheck turns what differs in one generated file or directory into its check. critical: the sessions
// lose their hooks or MCP without it (missing fails), otherwise only the skills (missing warns).
func generatedCheck(name string, issues []string, allMissing, critical bool, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: name, Items: issues}
	if c.Items == nil {
		c.Items = []string{}
	}
	switch {
	case len(issues) == 0:
		c.State, c.Status, c.Detail = "current", DoctorOK, i18n.T(lang, "node-doctor.generated_current")
	case allMissing:
		c.State, c.Status, c.Detail = "missing", DoctorWarn, i18n.T(lang, "node-doctor.generated_missing")
		if critical {
			c.Status = DoctorFail
			c.Hint = i18n.T(lang, "node-doctor.generated_missing_critical_hint")
		} else {
			c.Hint = i18n.T(lang, "node-doctor.generated_missing_plugin_hint")
		}
	default:
		c.State, c.Status = "stale", DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.generated_stale", i18n.Args{"count": len(issues), "issue": shorten(issues[0], 60)})
		c.Hint = i18n.T(lang, "node-doctor.generated_stale_hint")
	}
	return c
}

func allPrefix(items []string, prefix string) bool {
	return !slices.ContainsFunc(items, func(s string) bool { return !strings.HasPrefix(s, prefix) })
}

// configCommands are the programs a settings file or hooks plugin (top-level "hooks") runs: a shell-form hook's first
// word, an exec-form one's (with "args") command as it is.
func configCommands(b []byte) []string {
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
			} `json:"hooks"`
		} `json:"hooks"`
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	var out []string
	for _, matchers := range f.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				w := h.Command
				if h.Args == nil {
					w = shellFirstWord(h.Command)
				}
				if w != "" {
					out = append(out, w)
				}
			}
		}
	}
	for _, s := range f.MCPServers {
		if s.Command != "" {
			out = append(out, s.Command)
		}
	}
	return out
}

// shellFirstWord is the program a command line written with shellQuote runs: its first word past the VAR=value ones
// in front (rootAssign). A word is bare, or single-quoted with each embedded quote or backslash closed,
// backslash-escaped and reopened. Other tools' hooks in the user's settings.json (hookProgram) may double-quote a
// word instead: read the same way, nothing expanded inside. shellQuote never writes a bare ", so its lines read as before.
func shellFirstWord(cmd string) string {
	for rest := strings.TrimSpace(cmd); rest != ""; {
		var w strings.Builder
		i := 0
		for i < len(rest) && rest[i] != ' ' && rest[i] != '\t' {
			switch q := rest[i]; {
			case q == '\'' || q == '"':
				j := strings.IndexByte(rest[i+1:], q)
				if j < 0 {
					return "" // not a command line shellQuote writes
				}
				w.WriteString(rest[i+1 : i+1+j])
				i += j + 2
			case rest[i] == '\\' && i+1 < len(rest):
				w.WriteByte(rest[i+1])
				i += 2
			default:
				w.WriteByte(rest[i])
				i++
			}
		}
		if !assignmentRe.MatchString(rest[:i]) {
			return w.String()
		}
		rest = strings.TrimLeft(rest[i:], " \t")
	}
	return ""
}

// assignmentRe is a shell word that sets a variable for the command after it.
var assignmentRe = lazyre.New(`^[A-Za-z_][A-Za-z0-9_]*=`)

// checkCommandPaths: the program each generated file runs exists and can be run. A file moved or removed under a
// running node (a redeploy to another path) leaves hooks that fail on every call.
func checkCommandPaths(dir string) DoctorCheck {
	return checkCommandPathsIn(dir, i18n.Default)
}

func checkCommandPathsIn(dir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "command-paths", Items: []string{}}
	files := []string{"claude-settings.json", "assistant-settings.json", "mcp.json"}
	for _, kind := range []string{"claude", "assistant"} {
		files = append(files, filepath.Join(kind+"-hooks", "hooks", "hooks.json"))
	}
	files = append(files, filepath.Join(liveRepliesDir, "hooks", "hooks.json"))
	seen := map[string]bool{}
	var bad []string
	read := 0
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			continue
		}
		read++
		for _, p := range configCommands(b) {
			if seen[p] {
				continue
			}
			seen[p] = true
			if err := runnable(p); err != nil {
				bad = append(bad, fmt.Sprintf("%s: %v", p, err))
			}
		}
	}
	switch {
	case len(bad) > 0:
		slices.Sort(bad)
		c.Status, c.Items = DoctorFail, bad
		c.Detail = i18n.T(lang, "node-doctor.command_paths_bad", i18n.Args{"count": len(bad), "issue": shorten(bad[0], 80)})
		c.Hint = i18n.T(lang, "node-doctor.command_paths_bad_hint")
	case read == 0:
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.command_paths_none")
		c.Hint = i18n.T(lang, "node-doctor.command_paths_none_hint")
	default:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.command_paths_ok", i18n.Args{"count": len(seen)})
	}
	return c
}

func runnable(path string) error {
	if !filepath.IsAbs(path) { // a bare name: found on PATH like claude would
		if _, err := exec.LookPath(path); err != nil {
			return errors.New("not on PATH")
		}
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return errors.New("not found")
	}
	if st.IsDir() || (runtime.GOOS != "windows" && st.Mode()&0o111 == 0) {
		return errors.New("not executable")
	}
	return nil
}
