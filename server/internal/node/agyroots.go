package node

import (
	"cmp"
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// Antigravity data directories besides the default (#17097). A launcher can run each agy account in its own data
// directory: it sets HOME (or ANTIGRAVITY_CLI_CONFIG_DIR) per account before it starts agy, so a conversation lives
// under <that HOME>/.gemini/antigravity-cli/brain/<id>, not under the node's agyDataRoot. herdr still names the
// conversation (agent_session), so the node only has to read it where it is: agyRootFor checks the default first,
// which is the whole story on a computer whose agy uses one directory, then the directories the agy processes in the
// node's panes use. Those are read from the processes' environment, only for panes whose conversation was not found,
// at most once every agyRootsRescan, and kept in memory; every read stays inside the conversation's directory
// (agyBrain), and only the user's own processes are read, as for the language server (agyinput.go).

// agyRootsRescan is how often the panes' processes may be read again for a conversation found nowhere.
const agyRootsRescan = 30 * time.Second

// agyRootsMaxProcs bounds the processes read under one pane.
const agyRootsMaxProcs = 64

// agyRootsScanTimeout bounds one scan.
const agyRootsScanTimeout = 5 * time.Second

// agyRootsState is what the node knows of the other data directories.
type agyRootsState struct {
	mu      sync.Mutex
	extra   []string
	scanned time.Time
}

// agyRootOfPID is the agy data directory pid's environment names ("" when it names none); tests stand in for it.
var agyRootOfPID = readAgyRootOfPID

// agyRootsNow is the scan's clock; tests stand in for it.
var agyRootsNow = time.Now

// agyPaneRoots are the data directories the processes under a pane use; tests stand in for it.
var agyPaneRoots = (*Node).readAgyPaneRoots

var (
	agyConfigDirRe = envVarRe("ANTIGRAVITY_CLI_CONFIG_DIR")
	agyHomeRe      = envVarRe("HOME")
)

// readAgyRootOfPID reads ANTIGRAVITY_CLI_CONFIG_DIR, else HOME/.gemini/antigravity-cli, from pid's environment as it
// was started (/proc/<pid>/environ on Linux, ps -E on macOS: procEnvBytes).
func readAgyRootOfPID(pid int) string {
	return agyRootOfEnv(procEnvBytes(pid))
}

// agyRootOfEnv is the agy data directory an environment listing names, "" when it names none.
func agyRootOfEnv(env []byte) string {
	if len(env) == 0 {
		return ""
	}
	if m := agyConfigDirRe.FindSubmatch(env); m != nil {
		return string(m[1])
	}
	if m := agyHomeRe.FindSubmatch(env); m != nil {
		return filepath.Join(string(m[1]), ".gemini", "antigravity-cli")
	}
	return ""
}

// agyHasConversation: root holds the conversation's directory (a real directory, as agyBrain requires).
func agyHasConversation(root, conversation string) bool {
	if root == "" {
		return false
	}
	st, err := os.Lstat(filepath.Join(root, "brain", conversation))
	return err == nil && st.IsDir()
}

// agyRootFor is the agy data directory that holds conversation: the default when it does, or when nothing else does.
func (n *Node) agyRootFor(conversation string) string {
	def := n.agyDataRoot()
	if !validID(conversation) || agyHasConversation(def, conversation) {
		return def
	}
	if root := n.agyKnownRootWith(conversation); root != "" {
		return root
	}
	if n.agyScanRoots() {
		if root := n.agyKnownRootWith(conversation); root != "" {
			return root
		}
	}
	return def
}

// agyRootKnownFor is agyRootFor without a scan, for a caller that may hold n.mu (a session row's metadata): the
// default, or a directory an earlier scan found.
func (n *Node) agyRootKnownFor(conversation string) string {
	def := n.agyDataRoot()
	if !validID(conversation) || agyHasConversation(def, conversation) {
		return def
	}
	return cmp.Or(n.agyKnownRootWith(conversation), def)
}

// agyKnownRootWith is the first other directory already known that holds conversation.
func (n *Node) agyKnownRootWith(conversation string) string {
	n.agyRoots.mu.Lock()
	roots := slices.Clone(n.agyRoots.extra)
	n.agyRoots.mu.Unlock()
	for _, root := range roots {
		if agyHasConversation(root, conversation) {
			return root
		}
	}
	return ""
}

// agyScanRoots reads the data directories of the agy panes whose conversation no known directory holds, unless a scan
// ran within agyRootsRescan; it says whether it found a new one. The caller must not hold n.mu.
func (n *Node) agyScanRoots() bool {
	now := agyRootsNow()
	n.agyRoots.mu.Lock()
	if !n.agyRoots.scanned.IsZero() && now.Sub(n.agyRoots.scanned) < agyRootsRescan {
		n.agyRoots.mu.Unlock()
		return false
	}
	n.agyRoots.scanned = now
	known := slices.Clone(n.agyRoots.extra)
	n.agyRoots.mu.Unlock()

	def := n.agyDataRoot()
	type paneConversation struct{ pane, conversation string }
	var reported []paneConversation
	n.mu.Lock()
	for _, agents := range n.hostAgents {
		for _, a := range agents {
			if conversation := n.agyReportedLocked(a); a.PaneID != "" && validID(conversation) {
				reported = append(reported, paneConversation{a.PaneID, conversation})
			}
		}
	}
	n.mu.Unlock()
	var panes []string
	for _, r := range reported {
		if slices.Contains(panes, r.pane) || agyHasConversation(def, r.conversation) ||
			slices.ContainsFunc(known, func(root string) bool { return agyHasConversation(root, r.conversation) }) {
			continue
		}
		panes = append(panes, r.pane)
	}
	if len(panes) == 0 {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), agyRootsScanTimeout)
	defer cancel()
	var found []string
	for _, pane := range panes {
		if ctx.Err() != nil {
			break
		}
		for _, root := range agyPaneRoots(n, ctx, pane) {
			root = filepath.Clean(root)
			if !filepath.IsAbs(root) || root == filepath.Clean(def) || slices.Contains(known, root) || slices.Contains(found, root) {
				continue
			}
			if st, err := os.Stat(filepath.Join(root, "brain")); err != nil || !st.IsDir() {
				continue
			}
			found = append(found, root)
		}
	}
	if len(found) == 0 {
		return false
	}
	n.agyRoots.mu.Lock()
	for _, root := range found {
		if !slices.Contains(n.agyRoots.extra, root) {
			n.agyRoots.extra = append(n.agyRoots.extra, root)
		}
	}
	n.agyRoots.mu.Unlock()
	return true
}

// readAgyPaneRoots are the data directories named by the processes under pane (its engine's process and their
// descendants, as agyLanguageServer walks them).
func (n *Node) readAgyPaneRoots(ctx context.Context, pane string) []string {
	roots, err := n.paneProcesses(ctx, pane)
	if err != nil || len(roots) == 0 {
		return nil
	}
	procs, err := listProcs(ctx)
	if err != nil {
		return nil
	}
	children := procChildren(procs)
	seen := map[int]bool{}
	queue := slices.Clone(roots)
	var out []string
	for len(queue) > 0 && len(seen) < agyRootsMaxProcs && ctx.Err() == nil {
		pid := queue[0]
		queue = queue[1:]
		if seen[pid] || pid <= 0 {
			continue
		}
		seen[pid] = true
		queue = append(queue, children[pid]...)
		if root := agyRootOfPID(pid); root != "" && !slices.Contains(out, root) {
			out = append(out, root)
		}
	}
	return out
}
