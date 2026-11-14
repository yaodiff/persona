package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/transcript"
)

// startingSession reports whether a session without a transcript is one that exists but has not written its first
// line yet (a headless conversation, or an agent that is live or that Persona started), rather than an unknown id.
func (n *Node) startingSession(id string) bool {
	if n.isAssistantSession(id) {
		return true
	}
	if _, live := n.agent(id); live {
		return true
	}
	if _, known := n.codexSource(id); known {
		return true
	}
	_, known := n.state.get(id)
	return known
}

// transcriptPath finds <config dir>/projects/*/<id>.jsonl. One session can have a file in two config directories
// (resumed under another profile or copied to another project: the new writes go to the new copy, the old one stays):
// the newest is the one. A known path, reported by a hook or found here, is looked for again after half a minute, and
// stays a candidate: a hook may name a file outside the directories searched.
func (n *Node) transcriptPath(id string) string { return n.transcriptPathIn(id, nil) }

// transcriptPathIn is transcriptPath with a Codex rollout's stat taken from a list pass (codexListPass; nil: stat'ed
// here): the list stats each rollout already, and this ran again for every row every second.
func (n *Node) transcriptPathIn(id string, pass *codexListPass) string {
	p := n.findTranscriptPath(id, pass)
	if !n.scopedTranscriptAllows(p) {
		return ""
	}
	return p
}

func (n *Node) findTranscriptPath(id string, pass *codexListPass) string {
	// A runtime's session whose conversation is a Claude transcript (runtimetranscript.go): that one's file.
	if t := n.runtimes.transcriptOf(id); t != "" {
		id = t
	}
	if n.codexBlocked(id) {
		return ""
	}
	if src, ok := n.codexSource(id); ok && !n.ownedAssistantSource(id) {
		if p := n.codexAppHistoryPath(src, pass); p != "" {
			return p
		}
		if src.path != "" {
			if _, err := pass.stat(src.path); err == nil {
				return src.path
			}
		}
		if p := rolloutPath(src.home, src.thread); p != "" {
			return p
		}
		return n.codexAppHistoryPath(src, pass) // the App's chat with no rollout: its history as written (codex_apphistory.go)
	}
	if strings.HasPrefix(id, "c0de-") {
		return n.oldCodexTranscript(id)
	}
	n.mu.Lock()
	p := n.paths[id]
	at, fresh := n.pathsAt[id]
	n.mu.Unlock()
	known := p != "" && fileExists(p)
	if known && fresh && time.Since(at) < pathRecheck {
		return p
	}
	if !validID(id) {
		if known {
			return p
		}
		return ""
	}
	// Looking again is for a Claude transcript's newer copy in another projects directory. A cached non-Claude path
	// with no live rollout agent is returned directly. A live rollout instead rechecks its continuation segments.
	kind := n.rolloutAgent(id)
	if known && !isClaudeTranscript(p, id) && kind == "" {
		n.setPath(id, p)
		return p
	}
	// A live Codex or compatible-CLI agent's conversation is its rollout: the projects directories have nothing of it.
	if kind != "" {
		found := ""
		if kind == traexAgent {
			found = n.traexPath(id)
		}
		if found == "" {
			found = n.codexPath(id)
		}
		if found == "" && kind != traexAgent {
			found = n.traexPath(id)
		}
		if found != "" {
			n.setPath(id, found)
		}
		return found
	}
	matches := n.globProjects("*", id+".jsonl")
	if known && !slices.Contains(matches, p) {
		matches = append(matches, p)
	}
	matches = newestFirst(matches)
	if len(matches) == 0 {
		if p := n.codexPath(id); p != "" {
			matches = []string{p}
		} else if p := n.traexPath(id); p != "" {
			matches = []string{p}
		} else {
			return ""
		}
	}
	n.setPath(id, matches[0])
	return matches[0]
}

// pathRecheck is how long a found transcript path is trusted before transcriptPath looks for a newer copy.
const pathRecheck = 30 * time.Second

// isClaudeTranscript says whether path is a Claude transcript of session id (<projects>/<project>/<id>.jsonl) rather
// than a Codex or compatible-CLI rollout (rollout-<time>-<id>.jsonl) or another agent's history.
func isClaudeTranscript(path, id string) bool {
	return filepath.Base(path) == id+".jsonl"
}

// rolloutAgent is the kind of a live agent whose conversation is a rollout (Codex, the compatible CLI), "" for any
// other: such an agent never has a Claude transcript to look for.
func (n *Node) rolloutAgent(id string) string {
	n.mu.Lock()
	a, ok := n.agents[id]
	n.mu.Unlock()
	if ok && (a.Agent == "codex" || a.Agent == traexAgent) {
		return a.Agent
	}
	return ""
}

// setPath remembers session id's transcript path; transcriptPath looks for a newer copy after half a minute.
func (n *Node) setPath(id, p string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.paths[id] = p
	if n.pathsAt == nil {
		n.pathsAt = map[string]time.Time{}
	}
	n.pathsAt[id] = time.Now()
}

// newestFirst orders the copies of a transcript by modification time, newest first.
func newestFirst(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	mod := map[string]time.Time{}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil {
			mod[p] = st.ModTime()
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return mod[paths[i]].After(mod[paths[j]]) })
	return paths
}

func validID(id string) bool { return nodeclient.ValidSessionID(id) }

func (n *Node) file(id string) (*transcript.File, error) {
	n.refreshCodexAppHistory(id) // a Codex App chat with no rollout: its history written out of the App's database (codex_apphistory.go)
	n.mu.Lock()
	f := n.files[id]
	if f != nil {
		n.used(id)
	}
	n.mu.Unlock()
	if f != nil {
		n.followCopy(id, f)
		return f, nil
	}
	if _, _, sub := splitSubagent(id); sub {
		var err error
		if f, err = n.subagentFile(id); err != nil {
			return nil, err
		}
	} else if parent, agent, ok := strings.Cut(id, subagentSep); ok && n.runtimes.transcriptOf(parent) != "" {
		// A subagent of a runtime's session whose conversation is a Claude transcript: that transcript's subagent.
		var err error
		if f, err = n.subagentFile(n.runtimes.transcriptOf(parent) + subagentSep + agent); err != nil {
			return nil, err
		}
	} else {
		p := n.transcriptPath(id)
		if p == "" {
			return nil, fmt.Errorf("transcript for session %s not found", id)
		}
		f = transcript.NewFile(p)
	}
	n.mu.Lock()
	if existing := n.files[id]; existing != nil {
		f = existing
	} else {
		n.files[id] = f
	}
	n.used(id)
	n.evictFiles()
	n.mu.Unlock()
	return f, nil
}

// warmTranscripts parses the live sessions' transcripts once, one at a time, soon after the node starts. Right
// after a (re)deploy the phone pulls every busy session, and parsing a transcript of a few hundred MB on that first
// request took up to 30 s (the hub gave up with a 502).
func (n *Node) warmTranscripts(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(5 * time.Second): // the first herdr snapshot is in
	}
	n.mu.Lock()
	ids := make([]string, 0, len(n.agents))
	for id := range n.agents {
		ids = append(ids, id)
	}
	n.mu.Unlock()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if f, err := n.file(id); err == nil {
			n.refresh(id, f)
		}
	}
}

// followCopy points an open transcript at the file transcriptPath names now, when that is another: moved since it was
// opened (a Codex thread archived or restored), or a newer copy took over (resumed in another project or profile, or
// copied when its working directory moved). The session list's rev and updated_at read that file, so the items must
// too (#1423). Relocate to another inode starts over. moved says it pointed the transcript elsewhere.
func (n *Node) followCopy(id string, f *transcript.File) (moved bool) {
	if p := n.transcriptPath(id); p != "" && p != f.Where() {
		f.Relocate(p)
		return true
	}
	return false
}

// maxFiles caps the transcripts kept parsed in memory: every session ever opened used to stay, items and all.
const maxFiles = 16

// used marks a parsed transcript as just asked for. Callers hold n.mu.
func (n *Node) used(id string) {
	if n.fileUse == nil {
		n.fileUse = map[string]uint64{}
	}
	n.useTick++
	n.fileUse[id] = n.useTick
}

// evictFiles drops the least recently used parsed transcripts of offline sessions nobody watches, down to maxFiles.
// Live sessions stay (warmTranscripts parsed them so the phone never waits for a cold parse). Reopening one parses
// the file again; its history stays the same (same file, same first line). Callers hold n.mu.
func (n *Node) evictFiles() {
	for len(n.files) > maxFiles {
		oldest, at := "", uint64(0)
		for id := range n.files {
			if _, watched := n.watches[id]; watched {
				continue
			}
			if _, live := n.agents[id]; live {
				continue
			}
			if t := n.fileUse[id]; oldest == "" || t < at {
				oldest, at = id, t
			}
		}
		if oldest == "" {
			return // all watched
		}
		delete(n.files, oldest)
		delete(n.fileUse, oldest)
	}
}
