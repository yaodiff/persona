package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// noTranscript is file's error for a session id this machine has no transcript for: an unknown id (a stale link, a
// client that sent the wrong field) or a file that is gone. Its text is the one the node has always given.
type noTranscript struct{ id string }

func (e noTranscript) Error() string { return fmt.Sprintf("transcript for session %s not found", e.id) }

// readErr is the error a read of session id's history (session.items, session.item, session.image,
// session.visualization) answers: no transcript is codeNotFound, which the hub serves as 404 not_found (apierr.Call),
// not a 502 internal error (#9551), with why in data.reason (readReason), which the hub words (#11388). The message
// stays the one the node has always given. Sends, rewinds and the other callers of file keep the plain error: the
// manager takes a not_found send as "the session is gone" and withdraws its card (assistant sessionNotFound), which a
// missing file alone must not do.
func (n *Node) readErr(id string, err error) error {
	var nt noTranscript
	if !errors.As(err, &nt) {
		return err
	}
	re := &rpc.Error{Code: codeNotFound, Message: nt.Error()}
	if reason := n.readReason(id, nt.id); reason != "" {
		re.Data, _ = json.Marshal(map[string]string{"reason": reason})
	}
	return re
}

// readReason is why the node has no transcript of session id to read (missing: the session whose transcript it looked
// for): proto.NotFoundNoTranscript for a session it knows (knownSession), proto.NotFoundUnknownSession for one it has
// no record of. A subagent ("<session>@<agent>") goes by its session. None until the node's records have come from the
// hub on this connection (recordsLoaded: a session missing from them may still be Persona's), nor for a session an
// Agent runtime runs: registered ownership, a transcript the runtime names, or a runtime assistant conversation. The
// runtime-shaped id test is only a fallback for an unrecorded id: the node's own record can use that shape too (#11470).
func (n *Node) readReason(id, missing string) string {
	if parent, _, ok := splitSubagent(id); ok && parent == missing {
		id = parent
	}
	session, _, _ := strings.Cut(id, subagentSep)
	_, _, runtimeOwned := n.runtimes.belongs(session)
	if id != missing || runtimeOwned || n.runtimeAssistant(session) || n.runtimes.transcriptOf(id) != "" {
		return ""
	}
	if n.knownSession(id) || n.startingSession(id) {
		return proto.NotFoundNoTranscript
	}
	if runtimeSessionID(session) || !n.recordsLoaded() {
		return ""
	}
	return proto.NotFoundUnknownSession
}

// knownSession reports whether the node knows session id, which may have no transcript: a headless conversation, an
// agent that is live, a Codex thread it registered, or a session Persona started (its record, kept 60 days).
func (n *Node) knownSession(id string) bool {
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

// startingSession reports whether a session without a transcript may not have written its first line yet, so a read
// of its history answers an empty page rather than not_found: it runs (a live agent, an assistant process, an external
// start under way, a thread a Codex app-server has loaded: a new one has no rollout before its first message), waits
// for its first message (a Codex waiting row too: its record names its pane until a thread takes it, also before a
// host refresh lists it again after a reconnect), has a start in progress or one that failed within the time a failed
// start stays listed (startFailedFor), or was started less than the start bound (startWait) ago: by its record, or by
// a start that began since (startsBegun: a synchronous one, which a Resume always is, keeps the record's first time).
// A read of any other session without one is not_found (readErr): what it had to read is gone, or it never got a
// message (#11388, #11450).
func (n *Node) startingSession(id string) bool {
	n.mu.Lock()
	_, proc := n.procs[id]
	n.mu.Unlock()
	if proc || n.externalStarts.has(id) || n.codexLoaded(id) {
		return true
	}
	if _, live := n.agent(id); live {
		return true
	}
	if n.starts.get(id) != nil || n.startsBegun.within(id, n.startWait()) {
		return true
	}
	st, ok := n.state.get(id)
	return ok && (st.AwaitingFirstMessage || isCodexWaitID(id) && st.Pane != "" ||
		!st.StartedAt.IsZero() && time.Since(st.StartedAt) < n.startWait())
}

// startsBegun is when the latest start of each session that startLog does not hold began: a synchronous start, and a
// conversation started again once healClaudeRefusalTranscript removed its only, refused, turn with its file. Until its
// process is listed it has no transcript, and the record's start time can be long past (startingSession).
type startsBegun struct {
	mu sync.Mutex
	m  map[string]time.Time
}

// begin notes a start of id now; the func it returns takes the note back (a start that failed) unless another start
// of id began since. Notes older than keep go.
func (b *startsBegun) begin(id string, keep time.Duration) (drop func()) {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.m == nil {
		b.m = map[string]time.Time{}
	}
	for k, at := range b.m {
		if now.Sub(at) > keep {
			delete(b.m, k)
		}
	}
	b.m[id] = now
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.m[id].Equal(now) {
			delete(b.m, id)
		}
	}
}

// within: a start of id began less than d ago.
func (b *startsBegun) within(id string, d time.Duration) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	at, ok := b.m[id]
	return ok && time.Since(at) < d
}

// codexLoaded: a ready Codex app-server of this machine has session id's thread loaded (codexOwner), with or without
// a rollout yet.
func (n *Node) codexLoaded(id string) bool {
	if n.codices == nil {
		return false
	}
	_, _, err := n.codexOwner(id)
	return err == nil
}

// recordsLoaded: this connection has the hub's copy of the node's records (stateReady): not while it loads, nor after
// a load that failed and is tried again (reloadState) with none of them. A node outside Serve (a test, a command) has
// them once it is not loading.
func (n *Node) recordsLoaded() bool {
	if n.state.isLoading() {
		return false
	}
	if n.stateReady == nil {
		return true
	}
	select {
	case <-n.stateReady:
		return true
	default:
		return false
	}
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

// ownTranscriptPath is transcriptPath of session id's own file: never the transcript a runtime names for a session it
// lists under that id (a fork's line names Claude sessions by their own ids, runtimealiases.go).
func (n *Node) ownTranscriptPath(id string) string {
	p := n.findOwnTranscriptPath(id, nil)
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
	return n.findOwnTranscriptPath(id, pass)
}

func (n *Node) findOwnTranscriptPath(id string, pass *codexListPass) string {
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
		if p := rolloutPathIn(src.home, src.thread, pass); p != "" {
			return p
		}
		return n.codexAppHistoryPath(src, pass) // the App's chat with no rollout: its history as written (codex_apphistory.go)
	}
	if strings.HasPrefix(id, "c0de-") {
		return n.oldCodexTranscript(id)
	}
	// A migrated compatible-CLI conversation may retain its old rollout. Its paginated history is authoritative,
	// even when a reader cached the rollout before migration.
	n.mu.Lock()
	p := n.paths[id]
	at, fresh := n.pathsAt[id]
	n.mu.Unlock()
	known := p != "" && fileExists(p)
	freshHit := known && fresh && time.Since(at) < pathRecheck
	// Answer a fresh cache hit before the TRAE probe when the path is already known to be another agent's transcript:
	// a Claude <id>.jsonl, or a list-pass row sessionListIn knows is not TRAE (Antigravity and others cache differently
	// named files). Every Claude/Agy row otherwise asked TRAE's databases for its own id on every list pass. TRAE and
	// Codex rollout/app-history names are not <id>.jsonl and an untagged caller stays untrusted, so real TRAE
	// conversations still reach the probe below.
	if freshHit && n.cachedPathSkipsTraex(id, p, pass) {
		return p
	}
	if _, ok := n.traexThreadIn(id, pass); ok {
		if p := n.traexPath(id); p != "" {
			n.setPath(id, p)
			return p
		}
	}
	if freshHit {
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
			found = n.codexPathIn(id, pass)
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
		if p := n.codexPathIn(id, pass); p != "" {
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

// cachedPathSkipsTraex reports whether a fresh cached path p for id is already known not to be a TRAE conversation the
// TRAE probe could improve: a Claude project transcript (<id>.jsonl), or a list pass whose row for id is tagged as a
// different agent. Callers without such a pass only skip the unambiguously Claude-named path, so this never changes
// lookup for a real TRAE/Codex rollout name.
func (n *Node) cachedPathSkipsTraex(id, p string, pass *codexListPass) bool {
	return isClaudeTranscript(p, id) || pass.knownNonTraexAgent(id)
}

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
	return n.transcriptFile(id)
}

func (n *Node) exportFile(id string) (*transcript.File, error) {
	unsupported := func() error {
		return refusal(proto.CodeNodeOutdated, "The transcript is a database history projection.", nil)
	}
	if _, _, projected := n.codexAppHistoryChat(id); projected {
		return nil, unsupported()
	}
	f, err := n.transcriptFile(id)
	if err != nil {
		return nil, err
	}
	// Refuse a cached projection even when its database can no longer name
	// the session. The readable JSONL would still be stale.
	if dir := n.codexAppHistoryDir(); dir != "" && filepath.Dir(f.Where()) == filepath.Clean(dir) {
		return nil, unsupported()
	}
	return f, nil
}

func (n *Node) transcriptFile(id string) (*transcript.File, error) {
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
			return nil, noTranscript{id}
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
const maxFiles = 48

// used marks a parsed transcript as just asked for. Callers hold n.mu.
func (n *Node) used(id string) {
	if n.fileUse == nil {
		n.fileUse = map[string]uint64{}
	}
	n.useTick++
	n.fileUse[id] = n.useTick
}

// evictFiles drops the least recently used parsed transcripts of offline sessions nobody watches, down to maxFiles.
// Live sessions (terminal agents and running headless assistant processes) stay so the phone never waits for a cold
// parse. Reopening one parses the file again; its history stays the same (same file, same first line). Callers hold n.mu.
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
			if p := n.procs[id]; p != nil && p.alive() {
				continue
			}
			if t := n.fileUse[id]; oldest == "" || t < at {
				oldest, at = id, t
			}
		}
		if oldest == "" {
			return // all watched or live
		}
		delete(n.files, oldest)
		delete(n.fileUse, oldest)
	}
}
