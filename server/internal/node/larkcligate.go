package node

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// lark-cli (Feishu's official open-platform CLI, docs/plans/plan-lark-cli.md) in the assistant's Bash passes the same gate
// as ol (larkgate.go). lark-cli tells which commands write: every command's --help prints "Risk: read", "Risk: write"
// or "Risk: high-risk-write". Reads run at once; writes, and commands whose risk cannot be read, are confirmed on the
// phone with the whole command on the card. A command lark-cli does not have runs too: it changes nothing, and lark-cli
// itself tells the assistant so. Logging in and out, switching apps or profiles, and updating lark-cli are the user's
// to do: refused.

// larkRisk asks lark-cli for a command's risk level ("" when it cannot say, larkCLIMissing when lark-cli has no such
// command).
type larkRisk func(words []string) string

// larkCLIMissing is the risk of a command lark-cli says it does not have (larkRiskOf): run, it only fails with
// "unknown subcommand", so the gate treats it as a read.
const larkCLIMissing = "missing"

// larkCLIReads says a lark-cli risk level changes nothing on Feishu: a read, or a command lark-cli does not have.
func larkCLIReads(risk string) bool { return risk == "read" || risk == larkCLIMissing }

var (
	// lark-cli's own commands that only print: help, method schemas, the skills it carries, who is logged in.
	// Stable read commands also work when the manager has not configured CLI risk discovery, or the lookup gets no
	// answer (a wrapper on the login shell's PATH that cannot find the real lark-cli from the node's, #8389). Only an
	// unknown risk falls back here: what lark-cli reports wins. Each is "Risk: read" in lark-cli 1.0.54.
	larkCLIKnownReads = map[string]bool{"docs/+fetch": true, "contact/+search-user": true, "im/+chat-messages-list": true, "im/+messages-search": true,
		"im/+messages-mget": true, "im/+threads-messages-list": true, "im/+chat-search": true}
	// larkCLIReadVerbs name what a command does when lark-cli cannot be asked (#11193): a shortcut (`calendar +agenda`,
	// `task +get-my-tasks`, `im +chat-messages-list`) or a generated method (`calendar events list`) named by one of them
	// only reads. A shortcut may carry the verb as its whole name or after its last "-"; only get and list also lead a
	// name. Any other name, a send / reply / create / update / delete included, stays a write.
	larkCLIReadVerbs      = map[string]bool{"fetch": true, "agenda": true, "search": true, "list": true, "get": true, "mget": true}
	larkCLILeadReadVerbs  = map[string]bool{"get": true, "list": true}
	larkCLIMethodReadVerb = map[string]bool{"list": true, "get": true, "search": true, "mget": true, "batch_get": true}
	// larkCLILocalDownloads save what a message carries (an image, a file) under the current directory and change
	// nothing on Lark, though lark-cli rates them "Risk: write" (lark-cli 1.0.91): /read downloads a group's images
	// and files with them (#11803). lark-cli overwrites an existing file and takes a dotfile name, and without --output
	// the name is the sender's (Content-Disposition), so one is a read only when --output names a plain file in
	// lark-im-resources/ (larkCLIDownloadOutput, the directory --download-resources uses), and only for an ordinary
	// write rating: a high-risk one is not.
	larkCLILocalDownloads = map[string]bool{"im/+messages-resources-download": true}
	larkCLIDownloadOutput = lazyre.New(`^lark-im-resources/[A-Za-z0-9_][A-Za-z0-9._-]{0,199}$`)
	larkCLIRead           = map[string]bool{"schema": true, "skills": true, "whoami": true, "doctor": true, "help": true, "completion": true}
	larkCLIAuthRead       = map[string]bool{"status": true, "check": true, "scopes": true, "list": true}
	larkCLIEventRead      = map[string]bool{"list": true, "schema": true, "status": true}
	larkRiskRe            = lazyre.New(`(?m)^\s*Risk:\s*([a-z-]+)`)
	// larkCLIRe: a lark-cli call anywhere in a command the gate cannot parse.
	larkCLIRe = lazyre.New("(^|[\\s;&|(\"'`])(\\S*/)?lark-cli(\\s|[\"']|$)")
	// larkCLINamed: lark-cli as a word of its own anywhere in a command, a variable's value (`L=lark-cli; $L …`)
	// included; what lets a whole-command card say it acts on Lark (wholeCommandTitle).
	larkCLINamed = lazyre.New(`(^|[^A-Za-z0-9_.-])lark-cli([^A-Za-z0-9_.-]|$)`)
)

// isLarkCLI sees through zsh's `=cmd` expansion (`=lark-cli …` runs the resolved path): the word still names lark-cli.
func isLarkCLI(name string) bool {
	name = strings.TrimPrefix(name, "=")
	return name == "lark-cli" || filepath.Base(name) == "lark-cli"
}

// larkProfileEnvName is the variable lark-cli takes its profile from when no --profile is given (lark-cli 1.0.98): the
// node sets it to lark.cli.profile in the assistant's environment (assistantLarkEnvNamed, #9123).
const larkProfileEnvName = "LARKSUITE_CLI_PROFILE"

// larkProfileEnv: a command names lark-cli's profile variable (to set it, unset it or pass it on), which like
// --profile is the manager's to choose.
var larkProfileEnv = lazyre.New(`(^|[^A-Za-z0-9_])LARKSUITE_CLI_PROFILE([^A-Za-z0-9_]|$)`)

// larkProfileRead: reading the variable, which cannot change it, so a diagnostic passes: `$LARKSUITE_CLI_PROFILE`,
// `${LARKSUITE_CLI_PROFILE}` and `printenv LARKSUITE_CLI_PROFILE`. An expansion with an operator
// (`${LARKSUITE_CLI_PROFILE:=x}`) or any other mention still counts as naming it.
var larkProfileRead = lazyre.New(`\$LARKSUITE_CLI_PROFILE|\$\{LARKSUITE_CLI_PROFILE\}|(^|[\s;&|(])printenv\s+LARKSUITE_CLI_PROFILE`)

// parseLarkCLI reads `lark-cli <words…> [flags]`: the words name the command (`im +messages-send`, `api POST
// /open-apis/…`), the flags are its input. ok is false when the gate cannot tell what the command is.
func parseLarkCLI(args []string, risk larkRisk) (c larkCall, deny string, ok bool) {
	c = larkCall{Gate: -1, CLI: true, Method: "CLI", Args: args}
	var words, flags []string
	for _, f := range args {
		if f == "--profile" || strings.HasPrefix(f, "--profile=") {
			return c, "lark-cli 的配置档由管理者定（manager.json 的 lark.cli.profile），不要自己传 --profile。", false
		}
	}
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		name, _, inline := strings.Cut(args[i], "=")
		if !strings.Contains(" --as --format -q --jq -o --output ", " "+name+" ") {
			break
		}
		flags = append(flags, args[i])
		i++
		if !inline {
			if i == len(args) {
				return c, "", false
			}
			flags = append(flags, args[i])
			i++
		}
	}
	for ; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, args[i:]...)
			break
		}
		words = append(words, a)
	}
	if strings.Contains(strings.Join(words, " "), "$") {
		return c, "", false // a variable names the command: its risk is unknown
	}
	c.Path = "/" + strings.Join(words, "/")
	switch {
	case len(words) == 0:
		if flagOn(flags, "-h", "--help", "-v", "--version") {
			c.Risk = "read"
			return c, "", true
		}
		return c, "", false
	case flagOn(flags, "-h", "--help"):
		c.Risk = "read"
	case words[0] == "auth":
		if len(words) > 1 && larkCLIAuthRead[words[1]] {
			c.Risk = "read"
			return c, "", true
		}
		return c, "lark-cli 的登录、退出和授权由他自己在 hub 所在的机器上做（lark-cli auth login），不要替他运行。", false
	case words[0] == "config" || words[0] == "profile" || words[0] == "update":
		return c, "不要改 lark-cli 的配置、配置档或版本：这些由他自己做。", false
	case flagOn(flags, "--dry-run"):
		c.Risk = "read" // prints the request, runs nothing
	case larkCLIRead[words[0]]:
		c.Risk = "read"
	case words[0] == "event" && len(words) > 1 && larkCLIEventRead[words[1]]:
		c.Risk = "read"
	case words[0] == "api":
		if len(words) < 3 {
			return c, "", false
		}
		c.Method = strings.ToUpper(words[1])
		endpoint, err := url.ParseRequestURI(words[2])
		if err != nil || !httpMethod.MatchString(c.Method) || !strings.HasPrefix(words[2], "/") || endpoint.Host != "" {
			return c, "", false
		}
		c.Path = path.Clean(endpoint.Path)
		c.Query, c.Body = endpoint.RawQuery, flagValue(flags, "--data")
		if params := flagValue(flags, "--params"); params != "" {
			c.Query = params
		}
	default:
		if risk != nil {
			c.Risk = risk(words)
		}
		if c.Risk == "" && (larkCLIKnownReads[strings.Join(words, "/")] || larkCLINamedRead(words)) {
			c.Risk = "read"
		}
		if (c.Risk == "" || c.Risk == "write") && larkCLILocalDownloads[strings.Join(words, "/")] && larkCLISafeDownload(flags) {
			c.Risk = "read"
		}
	}
	return c, "", true
}

// larkCLINamedRead says a lark-cli command's name marks it a read (larkCLIReadVerbs), for when lark-cli gives no risk
// level: `<service> +<shortcut>` or `<service> <resource…> <method>`. Only the fallback: what lark-cli reports wins.
func larkCLINamedRead(words []string) bool {
	if len(words) < 2 {
		return false
	}
	last := words[len(words)-1]
	if name, shortcut := strings.CutPrefix(last, "+"); shortcut {
		if len(words) != 2 || name == "" {
			return false
		}
		parts := strings.Split(name, "-")
		return larkCLIReadVerbs[parts[len(parts)-1]] || len(parts) > 1 && larkCLILeadReadVerbs[parts[0]]
	}
	for _, w := range words {
		if strings.HasPrefix(w, "+") {
			return false
		}
	}
	return len(words) >= 3 && larkCLIMethodReadVerb[last]
}

// larkCLIDownloadFlags are the flags of `im +messages-resources-download` (lark-cli 1.0.91 --help), true for those
// that take a value. The command has no -o: lark-cli rejects it today, but a later version may add one, so a word the
// list does not name keeps the download a write.
var larkCLIDownloadFlags = map[string]bool{
	"--as": true, "--file-key": true, "--format": true, "-q": true, "--jq": true, "--message-id": true, "--output": true,
	"--type": true, "--dry-run": false, "--json": false,
}

// larkCLISafeDownload: the download can only write a plain file in lark-im-resources/, nowhere else and no dotfile.
// It has to be proved from the words alone, read in order: every one is a flag the command is known to have (no -o in
// any spelling, no shorthand cluster, no "--", no stray argument), there is an --output, and every --output (not
// only the last, which pflag keeps) names such a file.
func larkCLISafeDownload(flags []string) bool {
	outputs := 0
	for i := 0; i < len(flags); i++ {
		key, val, inline := strings.Cut(flags[i], "=")
		takesValue, known := larkCLIDownloadFlags[key]
		if !known {
			return false
		}
		if !takesValue {
			continue
		}
		if !inline {
			if i+1 == len(flags) {
				return false
			}
			i++
			val = flags[i]
		}
		if key == "--output" {
			if val == "" || strings.Contains(val, "..") || !larkCLIDownloadOutput.MatchString(val) {
				return false
			}
			outputs++
		}
	}
	return outputs > 0
}

// cliRead: a lark-cli call that only reads (its own risk level, or a GET through `lark-cli api`).
func (c larkCall) cliRead() bool {
	if c.Method == "CLI" {
		return larkCLIReads(c.Risk)
	}
	return c.Method == "GET" || c.Method == "HEAD"
}

// flagOn says a switch (--help, --dry-run…) is on. lark-cli's flags are pflag's: "--dry-run=false" turns it off,
// and a flag written without "=" may take the next word as its value ("--text --dry-run" sends the text
// "--dry-run"), so a bare switch counts only when the word before it is not a flag that may be waiting for one.
func flagOn(flags []string, names ...string) bool {
	on := false
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if f == "--" {
			break
		}
		if !strings.HasPrefix(f, "-") {
			continue
		}
		key, val, inline := strings.Cut(f, "=")
		matched := false
		for _, name := range names {
			if key == name {
				matched = true
				break
			}
		}
		if matched {
			on = true
			if inline {
				value, err := strconv.ParseBool(val)
				on = err == nil && value
			}
			continue
		}
		if !inline && !larkCLIBoolFlags[key] {
			if i+1 < len(flags) && strings.HasPrefix(flags[i+1], "-") {
				return false
			}
			i++ // a value flag consumes even a dash-prefixed argument
		}
	}
	return on
}

var larkCLIBoolFlags = map[string]bool{
	"--help": true, "-h": true, "--version": true, "-v": true, "--dry-run": true,
	"--reply-in-thread": true,
}

// flagValue follows pflag's last-value semantics, including --name=value. A value flag consumes
// its next argument even when that argument starts with a dash; -- ends flag parsing.
func flagValue(flags []string, name string) string {
	value := ""
	for i := 0; i < len(flags); i++ {
		f := flags[i]
		if f == "--" {
			break
		}
		if !strings.HasPrefix(f, "-") {
			continue
		}
		key, val, inline := strings.Cut(f, "=")
		if !inline {
			if larkCLIBoolFlags[key] {
				continue
			}
			// An unknown flag may be boolean or consume the next flag as text. Neither
			// interpretation may retain an earlier bot identity as a bypass exemption.
			if key != name && (i+1 >= len(flags) || strings.HasPrefix(flags[i+1], "-")) {
				return ""
			}
			if i+1 >= len(flags) {
				if key == name {
					value = ""
				}
				break
			}
			i++
			val = flags[i]
		}
		if key == name {
			value = val
		}
	}
	return value
}

// larkCLIRisk asks lark-cli for the risk level of each command it is given, once per command.
func (n *Node) larkCLIRisk(cfg proto.AssistantLark) larkRisk {
	if cfg.CLI == nil {
		cfg.CLI = &proto.LarkCLI{}
	}
	return func(words []string) string {
		key := cfg.CLI.Profile + "\x00" + strings.Join(words, " ")
		n.mu.Lock()
		r, ok := n.larkRisks[key]
		n.mu.Unlock()
		if ok {
			return r
		}
		bin := n.larkCLIBin(cfg.CLI)
		if bin == "" {
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, append(append(larkCLIProfile(cfg.CLI), words...), "--help")...)
		cmd.Env = larkCLIEnv(bin)
		out, err := cmd.CombinedOutput()
		r = larkRiskOf(out, words)
		if r == "" || (r == larkCLIMissing && err != nil) {
			return "" // not cached: lark-cli may be updating, or missing for now; only a clean run says "no such command"
		}
		n.mu.Lock()
		if n.larkRisks == nil {
			n.larkRisks = map[string]string{}
		}
		n.larkRisks[key] = r
		n.mu.Unlock()
		return r
	}
}

// larkRiskOf reads lark-cli's `<words…> --help`: the level its Risk: line gives; larkCLIMissing when the help is the
// group's above the command instead, which is what lark-cli prints for a command it does not have (lark-cli 1.0.98:
// `lark-cli im messages get --help` prints `im messages`, whose methods do not include get); "" otherwise. It is the
// group's when every Usage line names exactly lark-cli and the words without the last, the help lists the group's
// Available Commands, and the last word is not one of them. A command that exists but has no Risk: line stays "".
func larkRiskOf(out []byte, words []string) string {
	if m := larkRiskRe.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	if len(words) == 0 {
		return ""
	}
	parent := strings.Join(append([]string{"lark-cli"}, words[:len(words)-1]...), " ")
	usage := larkHelpSection(out, "Usage:")
	if len(usage) == 0 {
		return ""
	}
	for _, line := range usage {
		// cobra's two forms: the group's own use line and its `<path> [command]` line.
		if strings.TrimSuffix(strings.TrimSuffix(strings.Join(strings.Fields(line), " "), " [command]"), " [flags]") != parent {
			return ""
		}
	}
	commands := larkHelpSection(out, "Available Commands:")
	if len(commands) == 0 {
		return ""
	}
	for _, line := range commands {
		if strings.Fields(line)[0] == words[len(words)-1] {
			return ""
		}
	}
	return larkCLIMissing
}

// larkHelpSection is the indented lines under a heading of cobra's help (`Usage:`, `Available Commands:`), up to the
// first blank or unindented line; nil without the heading.
func larkHelpSection(out []byte, heading string) []string {
	var lines []string
	in := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimRight(line, " \r")
		switch {
		case !in:
			in = line == heading
		case line == "" || !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
			return lines
		default:
			lines = append(lines, line)
		}
	}
	return lines
}

// larkCLIBin is lark-cli's absolute path: lark.cli.bin as given, else looked up in a login shell (an npm install
// usually lives in a version manager's directory that only the user's shell puts on PATH); "" when it is not there.
func (n *Node) larkCLIBin(c *proto.LarkCLI) string {
	bin := c.Bin
	if bin == "" {
		bin = "lark-cli"
	}
	if filepath.IsAbs(bin) {
		return bin
	}
	n.mu.Lock()
	p, ok := n.larkCLIPaths[bin]
	n.mu.Unlock()
	if ok {
		return p
	}
	if p, _ = exec.LookPath(bin); p == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		out := larkShellOutput(ctx, "command -v "+shellQuote(bin))
		cancel()
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); filepath.IsAbs(last) {
			p = last
		}
	}
	if p != "" {
		n.mu.Lock()
		if n.larkCLIPaths == nil {
			n.larkCLIPaths = map[string]string{}
		}
		n.larkCLIPaths[bin] = p
		n.mu.Unlock()
	}
	return p
}

// larkShellOutput is what a login shell printed for script, for the gate's asks (where lark-cli is, the shell's PATH),
// which run inside a tool call's hook. Output waits for stdout to close, not for the shell to exit, and a background
// job the user's shell startup leaves running (an update check, a daemon started from ~/.zshrc) inherits stdout: without
// WaitDelay that job would decide how long the call waits, past ctx's timeout. What the shell printed is kept either way.
func larkShellOutput(ctx context.Context, script string) []byte {
	cmd := loginShell(ctx, script)
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output()
	return out
}

// larkLoginPATHRetry: how long a failed ask of the login shell's PATH stands before another try.
const larkLoginPATHRetry = 5 * time.Minute

// larkLoginPATHMark starts the line the login shell prints its PATH on, apart from what the user's shell startup
// prints.
const larkLoginPATHMark = "__PERSONA_LOGIN_PATH__"

// larkLoginPATH is the login shell's PATH directories: the ones the assistant's Bash resolves programs in. Claude Code
// runs each command through the user's shell snapshot (larkgate_env.go), which restores that PATH, and an npm global
// lark-cli and the node it starts live in a version manager's directory that this process's own PATH need not have
// (#12483). Asked at most once per larkLoginPATHRetry; nil when the shell cannot say.
func (n *Node) larkLoginPATH() []string {
	n.mu.Lock()
	dirs, at := n.larkPATH, n.larkPATHAt
	n.mu.Unlock()
	if dirs != nil || time.Since(at) < larkLoginPATHRetry {
		return dirs
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	out := larkShellOutput(ctx, `printf '\n`+larkLoginPATHMark+`%s\n' "$PATH"`)
	cancel()
	dirs = parseLoginPATH(out)
	n.mu.Lock()
	n.larkPATHAt = time.Now()
	if dirs != nil {
		n.larkPATH = dirs
	}
	n.mu.Unlock()
	return dirs
}

// parseLoginPATH reads the last marked line of a login shell's output: its PATH directories. Anything else the shell
// printed (a profile's greeting) counts for nothing, and only absolute directories are kept. nil when the line is not
// there, so the ask is tried again later.
func parseLoginPATH(out []byte) []string {
	path, seen := "", false
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), larkLoginPATHMark); ok {
			path, seen = v, true
		}
	}
	if !seen {
		return nil
	}
	dirs := []string{}
	for _, d := range strings.Split(path, ":") {
		if d != "" && filepath.IsAbs(d) {
			dirs = append(dirs, d)
		}
	}
	return dirs
}

func larkCLIProfile(c *proto.LarkCLI) []string {
	if c.Profile == "" {
		return nil
	}
	return []string{"--profile", c.Profile}
}

// larkCLIEnv: lark-cli's own directory first on PATH, for an npm shim that runs `env node`.
func larkCLIEnv(bin string) []string {
	return append(os.Environ(), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// writeLarkCLIWrapper puts `lark-cli` on the assistant's PATH (dir): the configured binary with the configured
// profile. Without lark-cli in the config, or when it cannot be found, there is none.
func (n *Node) writeLarkCLIWrapper(dir string, c *proto.LarkCLI) {
	path := filepath.Join(dir, "lark-cli")
	bin := ""
	if c != nil {
		bin = n.larkCLIBin(c)
	}
	if bin == "" || bin == path {
		_ = os.Remove(path)
		return
	}
	args := ""
	for _, a := range larkCLIProfile(c) {
		args += " " + shellQuote(a)
	}
	wrapper := "#!/bin/sh\n# lark-cli as manager.json lark.cli says (node/larkcligate.go), on the assistant's PATH.\nPATH=" +
		shellQuote(filepath.Dir(bin)) + ":\"$PATH\" exec " + shellQuote(bin) + args + " \"$@\"\n"
	_ = os.WriteFile(path, []byte(wrapper), 0o755)
}
