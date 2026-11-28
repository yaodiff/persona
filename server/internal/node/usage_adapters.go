package node

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/tokenuse"
)

// Where each agent keeps its token usage and how to read it (docs/server/machines.md "Token usage"): an adapter per
// agent says which files hold it, which session a file belongs to (its launcher is the session's) and parses its lines
// (tokenuse). The usage meter and the backfill read every adapter's files the same way: what a file gained since the
// last scan, from about the window's start in a big one, a request counted once across files.

// usageAdapter is one agent's records of token usage.
type usageAdapter interface {
	// agent is the agent the records are of (proto.Session.Agent's names).
	agent() string
	// paths are the files that may hold usage written since since; usageFiles keeps those changed since then.
	paths(n *Node, since time.Time) []string
	// session is the session (or thread) the file at path belongs to.
	session(path string) string
	// parser reads one file's lines.
	parser() tokenuse.Parser
	// startDir is the directory the session of the file at path started in, as its records say ("" when they do
	// not): the first one they name (usage_dirs.go).
	startDir(path string) string
}

// usageAdapters are the agents whose usage this node meters from files, in the order their files are read (a request
// in two agents' files is counted in the first one's). opencode keeps its in a database: usageQueries.
func (n *Node) usageAdapters() []usageAdapter {
	return []usageAdapter{claudeUsage{}, codexUsage{tokenuse.Codex, (*Node).meteredCodexHomes},
		codexUsage{tokenuse.Traex, func(n *Node) []string { return []string{n.traexHome()} }},
		piUsageFiles{tokenuse.Pi, piAgentDir}, piUsageFiles{tokenuse.Omp, ompAgentDir}}
}

// usageAgents are the agents this node meters, by file and by query.
func (n *Node) usageAgents() []string {
	var out []string
	for _, ad := range n.usageAdapters() {
		out = append(out, ad.agent())
	}
	for _, q := range n.usageQueries() {
		out = append(out, q.agent())
	}
	return out
}

// claudeUsage: Claude Code's transcripts in every config directory, their subagents' and their workflow runs' agents'
// (projectTranscripts).
type claudeUsage struct{}

func (claudeUsage) agent() string                       { return tokenuse.Claude }
func (claudeUsage) paths(n *Node, _ time.Time) []string { return n.projectTranscripts() }
func (claudeUsage) parser() tokenuse.Parser             { return tokenuse.ClaudeParser() }
func (claudeUsage) session(path string) string          { return claudeUsageSession(path) }

// startDir: the session's own transcript's first directory for a subagent's or a workflow agent's file too (theirs
// is where the session was when it started them); the file's own when that transcript is gone.
func (claudeUsage) startDir(path string) string {
	session := claudeUsageSession(path)
	for dir := filepath.Dir(path); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if filepath.Base(dir) == session {
			if cwd := headCwd(filepath.Join(filepath.Dir(dir), session+".jsonl")); cwd != "" {
				return cwd
			}
			break
		}
	}
	return headCwd(path)
}

// claudeUsageSession is a Claude Code transcript's session: its name (<id>.jsonl), the session folder over a
// subagent's (<id>/subagents/agent-….jsonl) or a workflow run's agent's (<id>/subagents/workflows/<run>/agent-….jsonl).
func claudeUsageSession(path string) string {
	if dir := filepath.Dir(path); filepath.Base(dir) == "subagents" {
		return filepath.Base(filepath.Dir(dir))
	}
	if runs := filepath.Dir(filepath.Dir(path)); filepath.Base(runs) == "workflows" && filepath.Base(filepath.Dir(runs)) == "subagents" {
		return filepath.Base(filepath.Dir(filepath.Dir(runs)))
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// codexUsage: the rollouts of Codex (every Codex home: the default and the registered ones) or of a CLI that writes
// Codex's (traex), every file: each segment of a continued thread (rolloutSegments; the session list follows a
// thread's newest), and for a backfill reaching further back than the session list's two weeks every rollout on disk.
type codexUsage struct {
	name  string
	homes func(*Node) []string
}

func (c codexUsage) agent() string           { return c.name }
func (c codexUsage) parser() tokenuse.Parser { return tokenuse.CodexParser(c.name) }

// startDir: the rollout's session_meta's cwd.
func (codexUsage) startDir(path string) string { return headCwd(path) }

func (c codexUsage) paths(n *Node, since time.Time) []string {
	var out []string
	seen, homes := map[string]bool{}, map[string]bool{}
	for _, home := range c.homes(n) {
		key := home
		if real, err := filepath.EvalSymlinks(home); err == nil {
			key = real // a registered home that is another's under a link: read once
		}
		if homes[key] {
			continue
		}
		homes[key] = true
		var paths []string
		if time.Since(since) > codexWindow-time.Hour {
			paths = allRolloutPaths(home)
		} else {
			for _, r := range rolloutSegments(home) {
				paths = append(paths, r.path)
			}
		}
		for _, p := range paths {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// meteredCodexHomes are the Codex homes whose usage is metered: every one (usageCodexHomes), or a session scope's own.
func (n *Node) meteredCodexHomes() []string {
	if n.sessionScope != nil {
		return []string{n.codexHome()}
	}
	return n.usageCodexHomes()
}

// session is a rollout's thread: the id its name ends with, before a continued segment's own (rolloutID).
func (codexUsage) session(path string) string {
	if id := rolloutID(path); id != "" {
		return id
	}
	return strings.TrimSuffix(filepath.Base(path), ".jsonl")
}

// piUsageFiles: the sessions of pi or of omp (the same format) under the agent's directory,
// <dir>/sessions/<cwd>/<time>_<id>.jsonl. A node bound to a session scope reads none.
type piUsageFiles struct {
	name string
	dir  func(home string) string
}

func (p piUsageFiles) agent() string           { return p.name }
func (p piUsageFiles) parser() tokenuse.Parser { return tokenuse.PiParser(p.name) }

// startDir: the session header's cwd.
func (piUsageFiles) startDir(path string) string { return headCwd(path) }

func (p piUsageFiles) paths(n *Node, _ time.Time) []string {
	if n.sessionScope != nil {
		return nil
	}
	paths, _ := filepath.Glob(filepath.Join(p.dir(n.home), "sessions", "*", "*.jsonl"))
	return paths
}

// session is a session file's id: its name after the last "_".
func (piUsageFiles) session(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if i := strings.LastIndexByte(name, '_'); i >= 0 {
		return name[i+1:]
	}
	return name
}

// piAgentDir is pi's agent directory: PI_CODING_AGENT_DIR when it is set, else <home>/.pi/agent (omp's is
// ompAgentDir; with the variable set both are it, and a request in it counts once).
func piAgentDir(home string) string {
	if d := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR")); d != "" {
		return d
	}
	return filepath.Join(home, ".pi", "agent")
}
