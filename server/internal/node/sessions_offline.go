package node

import (
	"cmp"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const offlineWindow = 7 * 24 * time.Hour

var offlineCache struct {
	sync.Mutex
	at   time.Time
	home string // whose scan it is: another node's (tests run many in one process) scans its own
	list []proto.Session
	// parents: the desktop app's side sessions and the sessions that started them, read with the scan
	// (claudeDesktopIndex.parents, linkSideSessions); replaced whole, never changed in place.
	parents map[string]string
	// origins: the folders the desktop app's sessions were started in (claudeDesktopIndex.origins, placeClaudeDesktop),
	// read and replaced the same way.
	origins map[string]string
	// folds: the transcripts the desktop app's sessions went on from (foldDesktopEarlier, #14394), read and replaced
	// the same way.
	folds desktopFolds
}

// appendOffline appends to dst the recent transcripts without a live agent, straight into the caller's list (sessionList's);
// the directory scan is cached for 20s.
func (n *Node) appendOffline(dst []proto.Session, live map[string]bool) []proto.Session {
	offlineCache.Lock()
	defer offlineCache.Unlock()
	if time.Since(offlineCache.at) > 20*time.Second || offlineCache.home != n.offlineCacheKey() {
		offlineCache.list = n.scanOffline()
		desktop := readClaudeDesktopIndex(n.home) // its files were just read: unchanged ones are not parsed again
		offlineCache.parents, offlineCache.origins = desktop.parents, desktop.origins
		offlineCache.folds = desktopFolds{earlier: desktop.earlier, before: desktop.before}
		offlineCache.at, offlineCache.home = time.Now(), n.offlineCacheKey()
	}
	for i := range offlineCache.list {
		if !live[offlineCache.list[i].SessionID] {
			dst = append(dst, offlineCache.list[i])
		}
	}
	return dst
}

// setKept replaces the sessions that are always listed; the next list has them.
func (n *Node) setKept(ids []string) {
	m := map[string]bool{}
	for _, id := range ids {
		if validID(id) {
			m[id] = true
		}
	}
	n.mu.Lock()
	n.kept = m
	n.mu.Unlock()
	forgetOfflineScan()
}

// offlineDesktop is what the last offline scan of this node's home read of the desktop app's index: the side sessions'
// parents and the sessions' start folders (offlineCache.parents, .origins); nil when another home's scan is cached.
func (n *Node) offlineDesktop() (parents, origins map[string]string) {
	offlineCache.Lock()
	defer offlineCache.Unlock()
	if offlineCache.home != n.offlineCacheKey() {
		return nil, nil
	}
	return offlineCache.parents, offlineCache.origins
}

// offlineFolds is what the last offline scan of this node's home read of the transcripts the desktop app's sessions
// went on from (offlineCache.folds); none when another home's scan is cached.
func (n *Node) offlineFolds() desktopFolds {
	offlineCache.Lock()
	defer offlineCache.Unlock()
	if offlineCache.home != n.offlineCacheKey() {
		return desktopFolds{}
	}
	return offlineCache.folds
}

// alwaysListed are the sessions the offline scan lists whatever the desktop app's index says of them: the kept ones
// (the main conversation) and "Branch to New Chat"'s copies (fork.go).
func (n *Node) alwaysListed() map[string]bool {
	out := map[string]bool{}
	for id := range n.keptSessions() {
		out[id] = true
	}
	n.mu.Lock()
	for id := range n.forked {
		out[id] = true
	}
	n.mu.Unlock()
	return out
}

// forgetOfflineScan drops the cached scan of offline transcripts: the next list scans again.
func forgetOfflineScan() {
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
}

func (n *Node) keptSessions() map[string]bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.kept
}

func (n *Node) scanOffline() []proto.Session {
	var out []proto.Session
	files := n.globProjects("*", "*.jsonl")
	n.maybePruneGoneMetas(files)
	// Which count a transcript is listed under: the assistant's conversations are counted on their own (a script running
	// `claude -p` hundreds of times a day filled the 80 newest with its sessions, and the "Chat" tab lost every conversation
	// quieter than a couple of hours, #2105); kept ones are not counted.
	const (
		others = iota
		assistants
		keptOnes
		desktopOnes // the desktop app's unarchived sessions, whatever their age (claudedesktop.go)
		// desktopFolded: the ones of them a client draws under another row, not as a row (a side session or a fork under
		// the session it came from, a scheduled task's earlier runs under its newest): counted apart, so they do not
		// take the places of the sessions the sidebar lists at the top level (#15279).
		desktopFolded
	)
	type cand struct {
		path  string
		st    os.FileInfo
		count int
	}
	var cands []cand
	keep := n.keptSessions()
	n.mu.Lock()
	forked := maps.Clone(n.forked) // "Branch to New Chat"'s copies (fork.go)
	n.mu.Unlock()
	own := n.assistantProject()
	persona := n.personaProjectDirs()
	unlisted := n.runtimes.unlistedTranscripts() // sessions of a runtime's own that it no longer lists (runtimeunlisted.go)
	// The desktop app's sessions as its sidebar lists them (claudedesktop.go).
	desktop := readClaudeDesktopIndex(n.home)
	desktopProjects := map[string]string{}
	for _, p := range files {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !validID(id) || n.isRoundFork(id) {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if keep[id] { // the main conversation: listed however long it has been quiet and however busy the machine
			cands = append(cands, cand{p, st, keptOnes})
			continue
		}
		// In the desktop app's sidebar: listed however long it has been quiet, and however little it says yet.
		inDesktop := desktop.listed(id)
		if !inDesktop && (time.Since(st.ModTime()) > offlineWindow || (st.Size() < 2048 && !forked[id])) {
			continue
		}
		if isAssistantProject(filepath.Base(filepath.Dir(p)), own) {
			cands = append(cands, cand{p, st, assistants})
			continue
		}
		// Another platform's runs (agentfilters.go) go before the newest are counted: hundreds a day would crowd out
		// the user's own. So do a program's one-shot runs (`claude -p`, claude_oneshot.go), unless the desktop app's
		// sidebar lists the transcript: a session New Agent starts there is written by `claude -p` and taken over by the
		// app (claudedesktop_new.go), and says only sdk-cli until the app ends a turn in it (#15522).
		if !forked[id] && n.agentFiltered("claude", p) {
			continue
		}
		if !forked[id] && !unlisted[id] && !inDesktop && n.oneShotRun(id, filepath.Base(filepath.Dir(p)), p, st, persona) {
			continue
		}
		if inDesktop { // counted on their own: they neither take the others' 80 nor are pushed out by them
			count := desktopOnes
			if desktop.parents[id] != "" {
				count = desktopFolded
			}
			cands = append(cands, cand{p, st, count})
			continue
		}
		cands = append(cands, cand{p, st, others})
	}
	// Newest first, and only what is listed counts: a session the filters below leave out (a blocked or out-of-scope
	// directory, a probe that never said anything, an older copy) took one of the 80 and pushed a listed one out.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	limit := [...]int{others: 80, assistants: maxAssistantOffline, keptOnes: len(cands), desktopOnes: maxDesktopOffline,
		desktopFolded: maxDesktopFolded}
	var counted [len(limit)]int
	listed := map[string]bool{}
	shown := map[string]bool{} // what the desktop app's rules look at: rows are left out before they are counted
	for _, c := range cands {
		id := strings.TrimSuffix(filepath.Base(c.path), ".jsonl")
		if listed[id] {
			continue // an older copy in another config directory (newest first): it used to win, and list the session twice
		}
		listed[id] = true
		if !keep[id] && !forked[id] && desktop.hidden(id, shown) {
			continue // an archived session of the desktop app, or a transcript its session went on from
		}
		if counted[c.count] >= limit[c.count] {
			continue
		}
		m := n.meta(c.path, c.st)
		if isLauncherCheckRun(c.path, m) {
			continue // a launcher health check's run, a hundred a day on each computer: no session of the user's (#2959)
		}
		if m.surface == transcript.SurfaceDesktop && !keep[id] && !forked[id] && desktop.gone(id, c.st.ModTime()) {
			continue // a session deleted in the desktop app: its entry is gone, its transcript stays (claudedesktop.go, #8314)
		}
		// Where the session is: an assistant conversation's start directory, before the filters look at it. A desktop
		// app's session whose transcript says nothing yet (it just began, or went on in a new transcript) has the
		// directory and the title its entry gives.
		title, cwd := m.title, m.cwd
		if about, ok := desktop.about[id]; ok {
			title, cwd = cmp.Or(title, about.title), cmp.Or(cwd, about.cwd)
		}
		cwd = n.assistantHome(id, cwd)
		if c.count == assistants && !identity.IsAssistantDir(cwd) && !identity.InSpace(cwd) {
			base := filepath.Base(filepath.Dir(c.path))
			if dir := n.assistantDirOfProject(base); dir != "" {
				cwd = dir
			} else if !isMeetingRoomProject(base) {
				if strings.HasSuffix(base, "-health") {
					cwd = identity.HealthDir(n.home)
				} else if strings.HasSuffix(base, "-assistant") {
					cwd = identity.AssistantDir(n.home)
				}
			}
		}
		if !n.sessionScopeAllows(cwd) {
			continue
		}
		// A blocked Agent folder never takes the assistant's own conversations or the sessions the hub keeps out, at
		// rest as in the live list (sessions.go); a blocked Claude config folder still does here.
		if n.restBlocked(id, cwd, keep) {
			continue
		}
		// Sessions that never produced output (hook probes): small files read whole with no title and no words. A bigger
		// one only had its tail read, and a tail of tool calls under one huge line says nothing; the main conversation is
		// listed whatever its tail holds. Dropping it left the app and the web with no session for the main chat, and its composer
		// disabled (#1163: its last turn stopped mid tool call, under a 377 KB line).
		if m.lastText == "" && m.lastTextAt.IsZero() && m.title == "" && !keep[id] && !forked[id] && c.count != desktopOnes && c.count != desktopFolded && c.st.Size() <= quickTail {
			continue
		}
		n.setPath(id, c.path)
		s := proto.Session{
			SessionID: id, Title: title, Cwd: cwd, Status: proto.StatusOffline, Model: n.shownModel(id, m.model),
			Agent: "claude", AgentSurface: m.surface,
			LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, c.st), Rev: c.st.Size(),
		}
		s.Activity, s.ActivityAt = m.activityOf()
		n.setSessionContext(&s, m)
		m.turnErr.set(&s) // Preserve the full gateway error and its reset after the terminal Agent exits.
		s.ModelFallback = m.modelFallback
		n.setModelRestore(&s, c.path, m)
		n.terminalModesRow(&s, m)
		untitled(&s, m)
		n.decorateClaudeDesktop(&s, desktop, desktopProjects)
		out = append(out, s)
		shown[id] = true
		counted[c.count]++
	}
	return out
}

// codexAssistantsAtRest lists the assistant's Codex conversations whose process has been let go (assistantIdleTimeout).
// scanOffline reads only Claude's projects and codexSessions keeps the assistant's out of Agents, so such a conversation
// had no row at all, and every client turned its composer off, with nothing left to send the message that resumes it (#2126).
func (n *Node) codexAssistantsAtRest(seen map[string]bool) []proto.Session {
	return n.codexAssistantsAtRestIn(seen, nil)
}

func (n *Node) codexAssistantsAtRestIn(seen map[string]bool, pass *codexListPass) []proto.Session {
	if n.state == nil || n.codices == nil {
		return nil
	}
	n.state.mu.Lock()
	cwds := map[string]string{}
	for id, st := range n.state.Sessions {
		if !seen[id] && validID(id) && identity.IsAssistantDir(st.Cwd) {
			cwds[id] = st.Cwd
		}
	}
	n.state.mu.Unlock()
	keep := n.keptSessions()
	type cand struct {
		id, path string
		st       os.FileInfo
	}
	var cands []cand
	for id := range cwds {
		path := n.codexPathIn(id, pass)
		if path == "" {
			continue
		}
		st, err := os.Stat(path)
		if err != nil || !keep[id] && time.Since(st.ModTime()) > offlineWindow {
			continue
		}
		cands = append(cands, cand{id, path, st})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	var out []proto.Session
	quiet := 0
	for _, c := range cands {
		if !keep[c.id] {
			if quiet++; quiet > maxAssistantOffline {
				continue
			}
		}
		m := n.meta(c.path, c.st)
		s := proto.Session{
			SessionID: c.id, Title: m.title, Cwd: cwds[c.id], Status: proto.StatusOffline, Model: m.model, Agent: "codex",
			LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, c.st), Rev: c.st.Size(),
		}
		s.Activity, s.ActivityAt = m.activityOf()
		setContext(&s, m)
		s.ModelFallback = m.modelFallback
		s.PermissionMode, s.Effort = n.currentModes(c.id, m)
		untitled(&s, m)
		n.decorate(&s)
		out = append(out, s)
	}
	return out
}

var nonAlnum = lazyre.New(`[^A-Za-z0-9]`)

// maxAssistantOffline is how many of the assistant directory's quiet transcripts the list keeps, apart from the 80
// newest of every other directory.
const maxAssistantOffline = 100

// assistantProject is the projects directory name Claude Code keeps for this node's assistant directory.
func (n *Node) assistantProject() string {
	return nonAlnum.ReplaceAllString(identity.AssistantDir(n.home), "-")
}

// assistantDirOfProject is the directory of this node's assistant (or its health space) whose projects directory
// Claude Code names base, "" for any other. Only this node's own names map back: the projects directory's name is
// lossy ("/", ".", "-" and "_" all become "-"), so the former state directory's or a symlinked home's cannot be told
// apart from another directory's (assistant-bin, another manager's assistant-m-<hex>).
func (n *Node) assistantDirOfProject(base string) string {
	switch own := n.assistantProject(); base {
	case own:
		return identity.AssistantDir(n.home)
	case strings.TrimSuffix(own, "-assistant") + "-health":
		return identity.HealthDir(n.home)
	}
	return ""
}

// assistantHome is the directory session id is listed in at rest: the directory its node record says it started in,
// when that is an assistant or space directory (the unresolved path its running process and resumes use), else cwd
// (what its transcript says: where an assistant conversation started, an agent's latest directory, #2368). A record
// of a scoped node's unlisted built-in directory (unlistedAssistantDir) is passed over for the transcript's.
func (n *Node) assistantHome(id, cwd string) string {
	if n.state == nil {
		return cwd
	}
	if st, ok := n.state.get(id); ok && (identity.IsAssistantDir(st.Cwd) || identity.InSpace(st.Cwd)) && !n.unlistedAssistantDir(st.Cwd) {
		return st.Cwd
	}
	return cwd
}

// isAssistantProject: base is the name of the projects directory Claude Code keeps for the assistant's directory
// (own: this node's, assistantProject). The assistant may have run under ~/.persona or the former state directory
// (compat.StateDir), through a symlinked home, so the name is matched by its end as well (#1423: a copy under the
// former name is the assistant's too).
// The health space's directory (docs/assistant/health.md) sits next to it and counts as the assistant's too.
func isAssistantProject(base, own string) bool {
	if base == own || base == strings.TrimSuffix(own, "-assistant")+"-health" {
		return true
	}
	if isMeetingRoomProject(base) { // a meeting's room (assistant_guest.go): an assistant conversation, whatever runs it now
		return true
	}
	for _, suffix := range assistantProjectSuffixes() {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// isMeetingRoomProject: base is the projects folder of a meeting room's directory (guest-meeting/<recording id> in the
// node's state directory, either spelling).
func isMeetingRoomProject(base string) bool {
	former := "-" + nonAlnum.ReplaceAllString(compat.StateDir, "-")
	for _, mark := range []string{"--persona-guest-meeting-", former + "-guest-meeting-"} {
		if i := strings.LastIndex(base, mark); i >= 0 && uuidRe.MatchString(base[i+len(mark):]) {
			return true
		}
	}
	return false
}

// assistantProjectSuffixes are the ends isAssistantProject matches, built on first use (not at start: every short
// personad command would pay for the regexp) instead of on every call, once per Codex thread and transcript per list.
var assistantProjectSuffixes = sync.OnceValue(func() []string {
	former := "-" + nonAlnum.ReplaceAllString(compat.StateDir, "-") // "." becomes "-" as in the project's name
	return []string{"--persona-assistant", former + "-assistant", "--persona-health", former + "-health"}
})

// isLauncherCheckRun: the transcript at path is a launcher health check's: its directory — a temp directory the check
// made and removed when it ended — no longer exists, and the run started there (the head's start directory, which
// Claude Code names even after `cd`s; the check never runs a Bash that could move). A directory of the user's own with
// a similar name exists, so the name alone is not enough (#2959). The head is the meta the listing has already read.
func isLauncherCheckRun(path string, m quickMeta) bool {
	if !isLauncherCheckProject(filepath.Base(filepath.Dir(path))) {
		return false
	}
	if st, err := os.Stat(m.start); err == nil && st.IsDir() {
		return false // the directory exists: not one of the check's
	}
	// Before a big first read the head is still running: it holds the check's prompt or nothing, never a start
	// directory; decide on what is known, and let the next pass (the read finishes) be exact.
	return m.start == "" || isLauncherCheckDir(m.start)
}
