package node

import (
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
)

// An Agent the node starts runs in a pane, with its engine's environment and not the node's: herdr's server keeps the
// node's PERSONA_HOME only when the node started it (herdrServerKeep), and Orca's app runs with whatever environment
// it was started from. So what the node types into a pane says itself which data directory, and so which node, the
// Agent belongs to, and leaves out what only the assistant's processes are given: an engine started from the
// assistant's Bash tool would otherwise hand the assistant's environment to every Agent in it (#744). The files the
// node generates for its Agents (mcp.json, the hooks: generatedFiles) name the directory too, for a shell that takes
// neither.

// assistantOnlyEnv is what an Agent never takes over from its pane: what the node gives the assistant's processes alone
// (assistant_start.go, assistant_codex_spawn.go, assistant_mcp.go, assistant_guest.go, larkgate_env.go), and what a
// running Claude Code sets for every program it runs, marking it as that Claude's. The older spelling of each Persona
// variable goes too (assistantOnlyEnvNames). An Agent that wants CLAUDE_CODE_AUTO_COMPACT_WINDOW or ENABLE_TOOL_SEARCH
// gets them from its launcher or Claude Code's settings, not from the pane.
var assistantOnlyEnv = []string{
	modelSourceEnv, modelSourceBinEnv,
	"PERSONA_ASSISTANT", "PERSONA_ASSISTANT_BIN", "PERSONA_SESSION_ID", "PERSONA_SPACE", trackKindEnv, categoriesEnv, "PERSONA_GUEST",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "ENABLE_TOOL_SEARCH",
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION",
	"CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_EFFORT",
	"CLAUDE_PID", "CLAUDE_PLUGIN_DATA",
}

// startupOnlyEnv is what a start tab's shell is given only for its startup files (startShellEnv, startenv.go): the
// typed start line unsets it again, so the Agent, and the shell the tab keeps once it exits, have the environment
// the user's own terminals have; the Agent mark the tab also starts with (agentMarkEnv) stays. Nothing an Agent's own
// shells read: they are not interactive and load none of those frameworks.
var startupOnlyEnv = []string{"DISABLE_AUTO_UPDATE", "DISABLE_UPDATE_PROMPT", "ZSH_DISABLE_COMPFIX", "POWERLEVEL9K_DISABLE_CONFIGURATION_WIZARD"}

// assistantOnlyEnvNames is assistantOnlyEnv with each Persona variable's older spelling after it.
func assistantOnlyEnvNames() []string {
	var out []string
	for _, k := range assistantOnlyEnv {
		current, legacy := compat.EnvNames(k)
		out = append(out, current)
		if legacy != current {
			out = append(out, legacy)
		}
	}
	return out
}

// rootAssign is `PERSONA_HOME=<dir> `, quoted with q: in front of a command another process runs (a pane's shell,
// Claude Code running a hook), it names the data directory, and so the node, that the program belongs to.
func rootAssign(dir string, q func(string) string) string {
	return "PERSONA_HOME=" + q(dir) + " "
}

// agentMarkEnv marks a terminal Persona opened for an Agent, and the Agent's own processes (#10111,
// docs/integrations/engines.md "Agents run in a terminal"): a shell's startup files can tell such a terminal from one
// the user opened. herdr, the Herdr App and Orca start the shell with it (startShellEnv, host.EnvLauncher); agentCommand
// puts it in front of the Agent too, for engines whose terminals take no environment (Tern, an Orca without env).
const agentMarkEnv = "PERSONA_AGENT=1"

// agentCommand is cmd, a command line quoted with q that starts an Agent in a pane whose shell is shells (the programs
// in front of it, takesAssign): with this node's directory in front as PERSONA_HOME and the Agent mark (agentMarkEnv)
// where the shell takes `VAR=val cmd`, and, in the sh family, after `unset` of assistantOnlyEnvNames and
// startupOnlyEnv, joined with && so that a start whose shell refused the unset does not run. fish has no unset: there
// the directory and the mark alone. Another shell, or a node without a directory, gets cmd as it is.
func (n *Node) agentCommand(cmd string, q func(string) string, shells ...string) string {
	if n.dir == "" || !takesAssign(shells...) {
		return cmd
	}
	cmd = rootAssign(n.dir, q) + agentMarkEnv + " " + cmd
	for _, s := range shells {
		if strings.TrimPrefix(filepath.Base(s), "-") == "fish" {
			return cmd
		}
	}
	return "unset " + strings.Join(agentUnsetNames(), " ") + " && " + cmd
}

// agentUnsetNames are the variables agentCommand unsets in the sh family: the assistant's, then a start tab's startup ones.
func agentUnsetNames() []string {
	return append(assistantOnlyEnvNames(), startupOnlyEnv...)
}

// agentStartCommand is agentCommand for a command that may go into a start file. On Windows the outer pane is
// PowerShell, but Git Bash reads that file, so the command carries the same PERSONA_HOME and unset prefix as on
// macOS and Linux. herdr's separate resume_argv path keeps using agentCommand with the pane's own shell.
func (n *Node) agentStartCommand(cmd string, q func(string) string, shells ...string) string {
	return n.agentCommand(cmd, q, startCommandShells(shells)...)
}
