package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// launcherCheckDir starts the name of the temp directory a check runs in; Claude Code names the projects directory it
// keeps for it after it, which is how the session list tells a check's leftovers from the user's sessions.
const launcherCheckDir = "persona-check-"

// launcherCheckTimeout bounds Settings › Models' "Test": no answer by then is a failure (a variable for the tests).
// A request through a shared gateway queues behind the account's other traffic; 150 seconds outlasts the queue of a
// health round burst (2026-11-13: 90 seconds did not).
var launcherCheckTimeout = 150 * time.Second

// launcherCheck answers Settings › Models' "Test": runs the launcher once, headless, with a one-word prompt in an empty
// directory, and says whether an answer came back, how long it took and which model answered. It is a real request
// (a few tokens).
func (n *Node) launcherCheck(ctx context.Context, name string, models ...string) proto.LauncherCheck {
	model := ""
	if len(models) > 0 {
		model = models[0]
	}
	return n.launcherCheckWith(ctx, name, launcherCheckOpts{model: model})
}

// launcherCheckWith is launcherCheck with what launchers.check asked for: with assistantTurn, a claude launcher's
// check runs as the assistant runs a turn of conversation (launchercheck_assistant.go).
func (n *Node) launcherCheckWith(ctx context.Context, name string, o launcherCheckOpts) proto.LauncherCheck {
	if proto.IsExternalLauncher(name) {
		return proto.LauncherCheck{Agent: "claude", Error: externalRefusal(ctx, "adapter_probe_unsupported").Error()}
	}
	model := o.model
	if model != "" && !proto.ValidModelID(model) {
		return proto.LauncherCheck{Error: "invalid model"}
	}
	if name != "claude" && !shellcmd.NameRe.MatchString(name) {
		return proto.LauncherCheck{Error: fmt.Sprintf("invalid launcher %q", name)}
	}
	if isDesktopLauncher(name) { // nothing of its own to run: the app's session runs on the app's settings
		return proto.LauncherCheck{Agent: "claude", Error: desktopCheckMsg}
	}
	if isCodexAppLauncher(name) {
		return proto.LauncherCheck{Agent: "codex", Error: codexAppCheckMsg}
	}
	agent := n.launcherAgent(ctx, name)
	if argv, ok := n.codexArgv(name); ok && n.codexOnSharedDaemon(argv) {
		// codex exec runs in its own process and passes while a start, which joins the shared daemon, cannot.
		if err := codexDaemonSocketGone(n.codexHome()); err != nil {
			return proto.LauncherCheck{Agent: "codex", Error: err.Error()}
		}
	}
	dir, err := os.MkdirTemp("", launcherCheckDir)
	if err != nil {
		return proto.LauncherCheck{Error: err.Error()}
	}
	defer os.RemoveAll(dir)
	prompt := prompts.RenderLauncherCheck()
	if agent == "claude" {
		// What Claude Code keeps of the run, the shell's or a runtime's (Paseo's probe agent runs it in dir too).
		defer n.removeCheckProject(dir)
	}
	// A runtime's launcher, as a start in ctx's host runs it: where that host's engine runs only its own launchers, its
	// provider of the name, whatever launcher of the shell's or of Persona's has it too (#8140).
	h := n.runtimeOfIn(ctx, name)
	if h == nil && !n.usesAgyPane(ctx, name, "") {
		if m, ok := n.managedLauncher(name); ok && m.AgentType() == agyAgent {
			h = n.runtimes.launcherRuntime(agyAgent)
		}
	}
	if h != nil {
		if result, handled := n.runtimeLauncherCheck(ctx, h, name, dir, prompt, model); handled {
			return result
		}
	}
	if agent == "claude" {
		// The runtime may own authentication independently. This onboarding guard applies to the shell probe only.
		if ready, known := n.claudeOnboardingReady(ctx); known && !ready {
			return proto.LauncherCheck{Agent: agent, Error: claudeOnboardingNotReadyMsg}
		}
	}
	// The flag that keeps the run off the disk goes between before and after: a transcript per check, a hundred a day on
	// each computer, filled the newest offline sessions the list keeps and pushed the user's own Agents out (#2959).
	before, noSave, after := checkArgs(agent, prompt)
	if agent == "claude" {
		model = n.claudeModelArg(name, model) // what a start of it passes (#10365)
	}
	if model != "" {
		after += " --model " + shellQuote(model)
	}
	turn := assistantCheck{}
	if o.assistantTurn && agent == "claude" {
		if turn, err = n.assistantCheckFor(ctx, name, o, dir); err != nil {
			return proto.LauncherCheck{Agent: agent, Error: err.Error()}
		}
		defer turn.cleanup()
		after += turn.args
	}
	if agent == "claude" {
		// No hooks: the check is no session of the user's, and Persona's own hooks can hold it past the timeout (#3412;
		// the model list does the same, launcher_models.go). claudeSettings merges this with what the launcher needs from
		// --settings: a managed launcher's env (else the user's global settings env, another gateway, would win), an
		// alias's own --settings (Claude Code reads only the last one).
		ours := filepath.Join(dir, "check-settings.json")
		if err := os.WriteFile(ours, []byte(`{"disableAllHooks":true}`), 0o600); err != nil {
			return proto.LauncherCheck{Error: err.Error()}
		}
		settings, err := turn.withSettings(n.claudeSettings(ctx, name, ours, dir, turn.profile()...), dir, n.home)
		if err != nil {
			return proto.LauncherCheck{Agent: agent, Error: err.Error()}
		}
		settings, err = n.sourceClaudeSettings(ctx, name, dir, settings, "", modelhttp.Source{Role: "background"})
		if err != nil {
			return proto.LauncherCheck{Agent: agent, Error: err.Error()}
		}
		after += scriptSettingsFlag(settings)
	}
	ctx, cancel := context.WithTimeout(ctx, launcherCheckTimeout)
	defer cancel()
	script := n.launcherInDirScript
	if turn.on {
		script = turn.inDirScript(n)
	}
	run := func(flag string) (string, error) {
		line := script(name, dir, before+flag+after)
		if agent == "codex" && os.Getenv(modelhttp.SourceHostsEnv) != "" {
			line = codexShellWith(line, "")
		}
		cmd := loginShell(ctx, line)
		cmd.Env = turn.env
		if agent == "codex" {
			cmd.Env = append(cmd.Environ(), modelSourceEnvironment(modelhttp.Source{Role: "background"}, n.bin)...)
		} // nil (the node's own) but for an assistant turn
		killGroupOnCancel(cmd)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	start := time.Now()
	flag := ""
	if noSave != "" {
		flag = " " + noSave
	}
	text, err := run(flag)
	if err != nil && ctx.Err() == nil && flag != "" && strings.Contains(text, noSave) {
		// A CLI too old for the flag, or a compatible one without it, refuses it before doing anything: run without.
		start = time.Now()
		text, err = run("")
	}
	res := proto.LauncherCheck{Agent: agent, Millis: int(time.Since(start).Milliseconds()), AssistantTurn: turn.on}
	if ctx.Err() != nil {
		// What it printed before the time ran out usually names the cause (an upstream 401 or 402, a proxy that
		// refuses, a host that does not resolve); with nothing printed, how the command ended (#5951).
		res.Error = fmt.Sprintf("%d 秒内没有回答", int(launcherCheckTimeout.Seconds()))
		if tail := checkOutput(text); tail != "" {
			res.Error += "；最后的输出：" + tail
		} else if err != nil {
			res.Error += "；" + clipText(redactCheck(err.Error()), 200)
		}
		return res
	}
	if agent == "claude" {
		if r, ok := claudeResult(text); ok {
			res.Model, res.Answer = r.model, clipText(textfmt.StripTerminalEscapes(r.result), 200)
			if turn.on && r.subtype == "error_max_turns" {
				// The model answered with a tool call, which the one turn allowed did not run: the request went through.
				r.isError, res.Answer = false, ""
			}
			if r.isError {
				res.Error = clipText(redactCheck(textfmt.LauncherErrorSummary(r.result, 400)), 400)
				// A result that is only its kind (error_during_execution…) says nothing of why; what claude printed
				// besides its result line often does.
				if r.bare {
					if tail := checkOutput(withoutResultLines(text)); tail != "" {
						res.Error += "；" + tail
					}
				}
			} else {
				res.OK = true
			}
			if r.millis > 0 {
				res.Millis = r.millis
			}
			return res
		}
	} else if err == nil && text != "" {
		lines := strings.Split(textfmt.StripTerminalEscapes(text), "\n")
		res.OK, res.Answer = true, clipText(lines[len(lines)-1], 200)
		return res
	}
	res.Error = checkOutput(text)
	if res.Error == "" && err != nil {
		res.Error = clipText(redactCheck(err.Error()), 400)
	}
	return res
}

// launcherAgent is the CLI launcher name runs: a managed one's type, a runtime's word for it, what a probe saw, else
// claude.
// checkArgs is the one-turn, non-interactive run a check gives agent's CLI: the arguments before and after the flag that
// keeps the run off the disk, and that flag ("" for none). Codex's (exec … --skip-git-repo-check) go only to Codex and
// its variants: pi took them before (#8194), and every check of a pi launcher failed with "Unknown option
// '--skip-git-repo-check'", which the health check reads as a CLI too old. pi has -p and --no-session; another agent
// (Antigravity, Oh My Pi, one the node does not know) gets -p alone.
func checkArgs(agent, prompt string) (before, noSave, after string) {
	switch agent {
	case "claude":
		return ` -p ` + shellQuote(prompt) + ` --output-format json --max-turns 1`, "--no-session-persistence", ""
	case "codex", traexAgent: // one non-interactive turn
		return ` exec`, "--ephemeral", ` --skip-git-repo-check ` + shellQuote(prompt)
	case piAgent:
		return ` -p ` + shellQuote(prompt), "--no-session", ""
	}
	return ` -p ` + shellQuote(prompt), "", ""
}

func (n *Node) launcherAgent(ctx context.Context, name string) string {
	if m, managed := n.managedLauncher(name); managed {
		return m.AgentType()
	}
	if name != "claude" {
		if res, ok := n.runtimeLauncherModels(name); ok && res.Agent != "" {
			return res.Agent
		} else if d := n.launcherProbeCached(ctx, name); d.Agent != "" {
			return d.Agent
		}
	}
	return "claude"
}

// checkOutput is what a check printed, as its error says it: terminal escapes and the shell's startup noise gone, keys,
// URLs and IPs redacted (the escapes around an address are stripped first, so one a prompt wrapped still matches), and
// the result summarized to its error line.
func checkOutput(text string) string {
	return textfmt.LauncherErrorSummary(redactCheck(textfmt.StripTerminalEscapes(text)), 400)
}

var (
	checkURL = lazyre.New(`(?i)\b[a-z][a-z0-9+.-]*://[^\s"'<>]+`)
	checkIP  = lazyre.New(`\b\d{1,3}(\.\d{1,3}){3}(:\d+)?\b`)
)

// redactCheck keeps keys, gateway URLs and addresses out of a check's error (it is shown and may be logged).
func redactCheck(s string) string {
	return checkIP.ReplaceAllString(checkURL.ReplaceAllString(redactSecrets(s), "<url>"), "<ip>")
}

// withoutResultLines is what claude printed besides its JSON result.
func withoutResultLines(text string) string {
	var keep []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.Contains(l, `"is_error"`) {
			keep = append(keep, l)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

type claudeOut struct {
	result  string
	subtype string
	isError bool
	bare    bool // an error whose result was empty: it says only its subtype
	model   string
	millis  int
	usage   aiwire.Usage
}

// claudeResult reads `claude -p --output-format json`'s answer: the last line that holds a JSON object, from its first
// "{" that starts one (an interactive zsh may print banners first, and a shell integration's escape codes on the same
// line, #3412).
func claudeResult(out string) (claudeOut, bool) {
	// Escape sequences the shell integration leaves around the line (or on it) must not reach the JSON parser.
	lines := strings.Split(textfmt.StripTerminalEscapes(out), "\n")
	for l := len(lines) - 1; l >= 0; l-- {
		line := lines[l]
		for i := strings.IndexByte(line, '{'); i >= 0; {
			if r, ok := claudeResultJSON(line[i:]); ok {
				return r, true
			}
			next := strings.IndexByte(line[i+1:], '{')
			if next < 0 {
				break
			}
			i += 1 + next
		}
	}
	return claudeOut{}, false
}

func claudeResultJSON(body string) (claudeOut, bool) {
	var r struct {
		Result     string                     `json:"result"`
		IsError    bool                       `json:"is_error"`
		Subtype    string                     `json:"subtype"`
		DurationMS int                        `json:"duration_ms"`
		ModelUsage map[string]json.RawMessage `json:"modelUsage"`
		Usage      json.RawMessage            `json:"usage"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(body)), &r) != nil || (r.Result == "" && r.Subtype == "" && r.ModelUsage == nil) {
		return claudeOut{}, false
	}
	c := claudeOut{result: r.Result, subtype: r.Subtype, isError: r.IsError || (r.Subtype != "" && r.Subtype != "success"), millis: r.DurationMS}
	if c.isError && c.result == "" {
		c.result, c.bare = r.Subtype, true
	}
	// Missing or malformed usage must not turn a valid answer into an error.
	var usage aiwire.Usage
	if len(r.Usage) > 0 && json.Unmarshal(r.Usage, &usage) == nil {
		c.usage = usage
	}
	for m := range r.ModelUsage {
		if c.model == "" || len(m) > len(c.model) { // the main model; the small one is usually a prefix-free other name
			c.model = m
		}
	}
	return c, true
}

func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// removeCheckProject removes the projects directory Claude Code keeps for the check's temp directory dir: it makes one
// (holding an empty memory directory) even under --no-session-persistence, and a claude without the flag writes the
// transcript there. The name is the encoded path of dir (as assistantProject encodes), so only that one goes; on macOS
// Claude Code encodes the resolved path while MkdirTemp returns the symlinked one (/var → /private/var), so both names go.
func (n *Node) removeCheckProject(dir string) {
	names := []string{nonAlnum.ReplaceAllString(dir, "-")}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
		names = append(names, nonAlnum.ReplaceAllString(real, "-"))
	}
	for _, p := range n.projectDirs() {
		for _, name := range names {
			_ = os.RemoveAll(filepath.Join(p, name))
		}
	}
}

// isLauncherCheckProject: base is the name of the projects directory Claude Code keeps for a launcher check's temp
// directory ("<temp parent>/persona-check-<digits>", or an older node's prefix; the temp parent differs by machine).
// A fast first cut on the name only: the user may have a directory of their own with a similar name, so the caller
// checks where the run started before dropping the transcript (isLauncherCheckRun).
func isLauncherCheckProject(base string) bool {
	for _, prefix := range []string{launcherCheckDir, compat.LauncherCheckDir} {
		i := strings.LastIndex(base, "-"+prefix)
		if i < 0 {
			continue
		}
		digits := base[i+1+len(prefix):]
		if digits != "" && strings.Trim(digits, "0123456789") == "" {
			return true
		}
	}
	return false
}

// isLauncherCheckDir: dir is the temp directory a launcher check runs in ("<temp parent>/persona-check-<digits>", or
// an older node's prefix), or "" (unknown).
func isLauncherCheckDir(dir string) bool {
	for _, prefix := range []string{launcherCheckDir, compat.LauncherCheckDir} {
		i := strings.LastIndex(dir, "/"+prefix)
		if i < 0 {
			continue
		}
		digits := dir[i+1+len(prefix):]
		if digits != "" && strings.Trim(digits, "0123456789") == "" {
			return true
		}
	}
	return false
}
