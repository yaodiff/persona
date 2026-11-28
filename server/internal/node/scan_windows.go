package node

import (
	"slices"
	"strings"
	"time"
)

// agentImages are the programs an agent process runs as on Windows: Claude Code's native binary, Codex's, or either's
// script under node / bun (agentOfCmd decides from the command line).
var agentImages = map[string]bool{"claude": true, "codex": true, "node": true, "bun": true}

// scanOrcaProcsOS is scanOrcaProcs from Windows' process table. Windows has no terminal foreground group and no
// stopped state, so an agent process with the pane's ORCA_PANE_KEY counts unless an agent process of the same pane is
// among its ancestors (a claude started by a claude is not what the terminal shows). Claude first, then by start.
func scanOrcaProcsOS() (map[string][]orcaProc, error) {
	procs, parents, err := listWinProcs(func(name string) bool { return agentImages[name] },
		orcaPaneEnv, orcaLegacyPaneEnv, herdrPaneEnv, herdrCorePaneEnv)
	if err != nil {
		return nil, err
	}
	type cand struct {
		orcaProc
		pane string
	}
	byPID := map[int]cand{}
	for _, p := range procs {
		agent := agentOfCmd(p.argv)
		if agent == "" {
			continue
		}
		pane := orcaPaneOf(envListing(p.env))
		if pane == "" {
			continue
		}
		byPID[p.pid] = cand{orcaProc{pid: p.pid, agent: agent, cwd: p.cwd, start: p.start.Truncate(time.Second)}, pane}
	}
	out := map[string][]orcaProc{}
	for pid, c := range byPID {
		if !nestedAgent(pid, c.pane, c.start.UnixNano(), parents, func(pid int) (string, int64, bool) {
			a, ok := byPID[pid]
			return a.pane, a.start.UnixNano(), ok
		}) {
			out[c.pane] = append(out[c.pane], c.orcaProc)
		}
	}
	for _, ps := range out {
		slices.SortFunc(ps, func(a, b orcaProc) int {
			if (a.agent == "claude") != (b.agent == "claude") {
				if a.agent == "claude" {
					return -1
				}
				return 1
			}
			if c := a.start.Compare(b.start); c != 0 {
				return c
			}
			return a.pid - b.pid
		})
	}
	return out, nil
}

// nestedAgent: an agent process of the same pane, started before pid, is among pid's ancestors (parents maps a pid to
// its parent's). The start check keeps a reused pid from making a stranger an ancestor.
func nestedAgent(pid int, pane string, start int64, parents map[int]int,
	agentOf func(pid int) (pane string, start int64, ok bool)) bool {
	seen := map[int]bool{pid: true}
	for p := parents[pid]; p != 0 && !seen[p]; p = parents[p] {
		seen[p] = true
		if apane, astart, ok := agentOf(p); ok && apane == pane && astart <= start {
			return true
		}
	}
	return false
}

// envListing writes picked variables as an environment listing (NAME=value, NUL-separated) for the parsers shared
// with /proc and ps.
func envListing(env map[string]string) string {
	var b strings.Builder
	for k, v := range env {
		b.WriteString(k + "=" + v + "\x00")
	}
	return b.String()
}

// scanClaudeProcessesOS is scanClaudeProcesses from Windows' process table. The values are taken whole (a config
// directory under a home with a space in it is common here), not through the listing parsers.
func scanClaudeProcessesOS() map[int]procEnv {
	out := map[int]procEnv{}
	procs, _, err := listWinProcs(func(name string) bool { return agentImages[name] },
		"CLAUDE_CONFIG_DIR", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
		"CLAUDE_CODE_DISABLE_FAST_MODE", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_VERTEX")
	if err != nil {
		return out
	}
	for _, p := range procs {
		if !isClaudeCmd(p.argv) {
			continue
		}
		pe := procEnv{
			ConfigDir: p.env["CLAUDE_CONFIG_DIR"], BaseURL: p.env["ANTHROPIC_BASE_URL"], Model: p.env["ANTHROPIC_MODEL"],
			FastUnavailable: claudeEnvFlag(p.env["CLAUDE_CODE_DISABLE_FAST_MODE"]) || claudeEnvFlag(p.env["CLAUDE_CODE_USE_BEDROCK"]) || claudeEnvFlag(p.env["CLAUDE_CODE_USE_FOUNDRY"]),
			Vertex:          claudeEnvFlag(p.env["CLAUDE_CODE_USE_VERTEX"]),
		}
		if pe != (procEnv{}) {
			out[p.pid] = pe
		}
	}
	return out
}
