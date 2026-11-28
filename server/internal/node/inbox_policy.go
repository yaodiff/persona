package node

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	originInbox = "inbox"
	// inboxLarkRefusal is deliberately actionable: the model has a supported way to finish its turn without sending.
	inboxLarkRefusal = "Inbox conversations cannot make outbound Lark calls directly; use inbox_suggest so the owner can confirm the action."
)

// inboxPermissionMode accepts only the manager's dedicated AI-twin field. Missing, Plan and unknown values stay in
// default, preserving the MCP allowlist and rolling-upgrade safety. A live process keeps its existing mode; the latest
// setting applies when a stopped process resumes or a new conversation starts.
func inboxPermissionMode(configured, current string, keepCurrent bool) string {
	if keepCurrent {
		return current
	}
	if configured == proto.ModePlan || !slices.Contains(proto.PermissionModes, configured) {
		return proto.ModeDefault
	}
	return configured
}

// assistantOrigin is the manager-recorded origin for a model call. Session state covers Claude from before process
// start and every resumed conversation; the process record covers Codex while its proc-* key is becoming a thread id.
func (n *Node) assistantOrigin(id string) string {
	origin, _ := n.assistantOriginCwd(id)
	return origin
}

// assistantOriginCwd also returns the trusted directory the assistant process started in. A hook's current cwd may
// have changed since then and must not inherit directory-local assumptions made for the original assistant directory.
func (n *Node) assistantOriginCwd(id string) (string, string) {
	if n.state != nil {
		if st, ok := n.state.get(id); ok && st.Origin != "" {
			return st.Origin, st.Cwd
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	read := func(p *assistantProc) (string, string) {
		if p == nil {
			return "", ""
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.origin, p.cwd
	}
	if p := n.procs[id]; p != nil {
		return read(p)
	}
	if p := n.akeys[id]; p != nil {
		return read(p)
	}
	if p := n.spare; p != nil && p.id == id {
		return read(p)
	}
	for _, p := range n.akeys {
		if p != nil && (p.id == id || p.cx != nil && p.cx.key == id) {
			return read(p)
		}
	}
	return "", ""
}

// inboxReadRules lets an Inbox investigation read the owner's installed Skills and knowledge base. Claude Code checks
// both a requested symlink path and its resolved target, so a linked Skills root or direct child Skill needs both rules.
// Nested links keep the usual permission flow. A comma cannot be carried by the comma-joined --allowedTools value.
func (n *Node) inboxReadRules() []string {
	var skillRoots []string
	if n.home != "" {
		skillRoots = append(skillRoots, filepath.Join(n.home, ".claude", "skills"))
	}
	if n.dir != "" {
		skillRoots = append(skillRoots, filepath.Join(n.dir, "skills"))
	}
	roots := slices.Clone(skillRoots)
	if n.dir != "" {
		roots = append(roots, filepath.Join(n.dir, "data"))
	}
	var rules []string
	add := func(path string) {
		if path == "" || strings.Contains(path, ",") {
			return
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if path[0] != '/' {
			path = "/" + path // a Windows drive path
		}
		rule := "Read(/" + path + "/**)"
		if !slices.Contains(rules, rule) {
			rules = append(rules, rule)
		}
	}
	for _, root := range roots {
		if strings.Contains(root, ",") {
			continue
		}
		add(root)
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			add(resolved)
		}
	}
	// Installed Skills are commonly one symlink per Skill. Resolve just this bounded level rather than walking a Skill's
	// contents: its files may contain unrelated links, which retain the normal approval boundary.
	for _, root := range skillRoots {
		if strings.Contains(root, ",") {
			continue
		}
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink == 0 {
				continue
			}
			resolved, err := filepath.EvalSymlinks(filepath.Join(root, entry.Name()))
			if err != nil {
				continue
			}
			if info, err := os.Stat(resolved); err == nil && info.IsDir() {
				add(resolved)
			}
		}
	}
	return rules
}

// inboxSecretDenyRules keeps the bare Inbox Read allow away from credentials. The data names and closed directories
// are the same lists used by the path guard and agent sandbox; the home and agent paths are the path guard's lists too.
// Both written and resolved paths are needed because Claude checks the requested path and its symlink target. These
// are Read rules only: Bash and every outbound or mutation tool retain their ordinary permission flow.
func (n *Node) inboxSecretDenyRules() []string {
	var rules []string
	add := func(path string, directory bool) {
		if path == "" {
			return
		}
		for _, p := range inboxOriginalAndResolved(path) {
			rule := inboxReadDenyRule(p, directory)
			if rule != "" && !slices.Contains(rules, rule) {
				rules = append(rules, rule)
			}
		}
	}
	if n.dir != "" {
		for _, name := range identity.DataSecrets {
			add(filepath.Join(n.dir, name), false)     // exact rules also make the launch contract explicit
			add(filepath.Join(n.dir, name+"*"), false) // DataSecrets covers backups by their name prefix
		}
		// These primary keys also have exact rules, rather than relying on the suffix globs below.
		for _, name := range []string{"secret.key", "launch.key"} {
			add(filepath.Join(n.dir, name), false)
		}
		for _, glob := range identity.DataCredentialGlobs() {
			add(filepath.Join(n.dir, glob), false)
		}
		for _, dir := range append(slices.Clone(identity.DataSecretDirs), "managers", "run", "extensions/run") {
			add(filepath.Join(n.dir, dir), true)
		}
	}
	if n.home != "" {
		for _, dir := range secretDirs {
			add(filepath.Join(n.home, dir), true)
		}
		for _, name := range secretFiles {
			add(filepath.Join(n.home, name), false)
		}
	}
	claude, codex := n.knownAgentDirs()
	for _, dir := range claude {
		for _, name := range claudeSecretNames {
			add(filepath.Join(dir, name), false)
		}
	}
	for _, dir := range codex {
		for _, name := range codexSecretNames {
			add(filepath.Join(dir, name), false)
		}
	}
	return rules
}

// inboxOriginalAndResolved includes a missing path's physical form too: EvalSymlinks resolves its deepest existing
// parent and the missing suffix is then restored. This does not read the path's contents.
func inboxOriginalAndResolved(path string) []string {
	path = filepath.Clean(path)
	paths := []string{path}
	current, suffix := path, []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			if resolved != path {
				paths = append(paths, resolved)
			}
			return paths
		}
		parent := filepath.Dir(current)
		if parent == current {
			return paths
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func inboxReadDenyRule(path string, directory bool) string {
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path[0] != '/' {
		path = "/" + path // a Windows drive path
	}
	if directory {
		path += "/**"
	}
	return "Read(/" + path + ")"
}

// inboxLarkOutbound says whether a parsed command can make a Lark change or is too opaque to prove read-only. It runs
// before configured allows, bypass-safe bot send/reply, and phone confirmation: none of those is an Inbox confirmation.
func inboxLarkOutbound(cfg proto.AssistantLark, v larkVerdict, command string) bool {
	for _, c := range v.calls {
		if !larkCallRead(cfg.Gates, c) {
			return true
		}
	}
	if v.cliWrite || v.unknown {
		return true
	}
	if _, write := gateWrite(cfg.Gates, command); write {
		return true
	}
	// A command substitution or unparsed fragment associated with a gated call cannot be shown to be read-only.
	return (v.unsure || v.unparsed) && (len(v.calls) > 0 || larkCLIRe.MatchString(command))
}
