package node

import (
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Claude's desktop app keeps an index of its Claude Code sessions, one JSON per session:
// ~/Library/Application Support/Claude/claude-code-sessions/<…>/<…>/local_<id>.json (Claude-3p for the build on a
// third-party provider). Each names the transcript the session runs (cliSessionId), whether the session is archived
// (isArchived), and the transcripts it ran before (priorCliSessionIds: it went on in a new one). Its sidebar lists each
// unarchived session once (a side session nested under its parent), however long it has been quiet. The offline list
// follows it (#3304): it lists the transcript each unarchived session runs whatever its age, on a count of its own (with
// the entry's title and directory where the transcript has none yet), and leaves out the transcripts of archived
// sessions, and a session's earlier transcripts once the one it runs now is listed. Only these files are read, never the
// app's config file (it holds credentials). The saved-environment mirror supplies native project names and folders.
//
// One session can have several entries: the app keeps copies of its local_<id>.json under its other account and
// organization folders (measured 2026-11-23: 5 of 6 sessions had 4 or 5), each with the cliSessionId and isArchived
// it had when it was written. The index reads each session once (desktopSessions, #14394), from the copy that names
// the transcript it runs now; every other transcript its copies name (a stale cliSessionId, priorCliSessionIds, and
// lastAssistantCliSessionId when it differs) is one it ran before. The list folds those into the row of the
// transcript the session runs now, live or at rest, and names them in that row's aliases (foldDesktopEarlier); a
// message to one goes on in the session (desktopWentOn).
//
// Deleting a session in the app removes its entry and leaves its transcript. A transcript the desktop app ran (its
// entrypoint says so) that no entry names, now or as an earlier transcript, is such a session's and is left out too
// (claudeDesktopIndex.gone, #8314). Only that: a transcript another client ran is never judged by the index; an index
// with no readable entry (not there, another layout) leaves out nothing; an entry that cannot be read keeps every id it
// mentions; and a transcript written in the last desktopEntryGrace stays, as the app may not have written its entry yet.
// But Delete also writes deleted_<uuid> beside the entries, for the session's own id and for each transcript it ran
// (measured 2026-11-15: each holds the time of the delete in ms; 51 of 57 deletes wrote more than one): a transcript a marker
// names is gone at once. Some deletes leave only the session's own marker after removing its sole entry. A bounded,
// process-local cache of entries actually read here retains their current and earlier transcript associations for that
// case. It does not infer associations for a local-only marker first seen after a restart, and never broadens the age
// fallback to every old transcript merely because some deletion marker exists (#8673).
//
// A scheduled task's runs are sessions of their own in the index, each naming the task (scheduledTaskId); the app's
// sidebar lists them under the task in its own section, apart from the projects. The list draws a task's runs the
// same way, under its newest run, as it draws a side session (linkSideSessions, #8314).
//
// A session another one started (a side chat, a /btw question, a session spawned for a task) names that one in
// spawnedFrom.sessionId (its local_<id>); a fork made with the app's Fork names the session it came from in
// forkedFromSessionId alone (#9376; a session split off with start_session writes both, naming the same session). The
// app's sidebar nests a session under spawnedFrom.sessionId, else forkedFromSessionId, and the list draws it the same
// way (#5935): its row names the parent's current transcript in parent_agent_id while both are listed
// (linkSideSessions), and stays a session of its own. An entry with neither id, or whose parent is archived or not in
// the index, links nothing; so does one the user moved to the top level (lineageDetached, which keeps the old ids,
// #8522).

// claudeDesktopDirs are the desktop apps' data directories under home (a variable: tests use their own).
// Windows can redirect AppData, but another node's home must never read this user's desktop index.
var claudeDesktopDirs = func(home string) []string {
	local, roaming := "", ""
	if runtime.GOOS == "windows" {
		if own, err := os.UserHomeDir(); err == nil && strings.EqualFold(filepath.Clean(home), filepath.Clean(own)) {
			local, roaming = os.Getenv("LOCALAPPDATA"), os.Getenv("APPDATA")
		}
	}
	return claudeDesktopDataDirs(home, runtime.GOOS, local, roaming)
}

func claudeDesktopDataDirs(home, platform, local, roaming string) []string {
	if home == "" || !filepath.IsAbs(home) {
		return nil
	}
	if platform == "darwin" {
		base := filepath.Join(home, "Library", "Application Support")
		return []string{filepath.Join(base, "Claude"), filepath.Join(base, "Claude-3p")}
	}
	if platform != "windows" {
		return nil
	}
	bases := []string{filepath.Join(home, "AppData", "Local"), filepath.Join(home, "AppData", "Roaming")}
	for _, base := range []string{local, roaming} {
		if filepath.IsAbs(base) {
			bases = append(bases, base)
		}
	}
	// Store installations may virtualize either AppData directory under their package's LocalCache.
	for _, base := range []string{filepath.Join(home, "AppData", "Local"), local} {
		if !filepath.IsAbs(base) {
			continue
		}
		packages, _ := filepath.Glob(filepath.Join(base, "Packages", "Claude_*", "LocalCache"))
		for _, p := range packages {
			bases = append(bases, filepath.Join(p, "Local"), filepath.Join(p, "Roaming"))
		}
	}
	var dirs []string
	seen := map[string]bool{}
	for _, base := range bases {
		for _, name := range []string{"Claude", "Claude-3p", "Claude-Data"} {
			p := filepath.Clean(filepath.Join(base, name))
			key := strings.ToLower(p)
			if !seen[key] {
				seen[key] = true
				dirs = append(dirs, p)
			}
		}
	}
	return dirs
}

// desktopDataDirs are the desktop apps' data directories under home on goos: macOS's Application Support, Windows'
// local app data (measured 2026-11-15: the Store build, Claude 3.6556, keeps its index in %LOCALAPPDATA%\Claude-3p, not
// in its package's folder). Another system's layout is not known: none.
func desktopDataDirs(goos, home string) []string {
	var base string
	switch goos {
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	case "windows":
		base = filepath.Join(home, "AppData", "Local")
	default:
		return nil
	}
	return []string{filepath.Join(base, "Claude"), filepath.Join(base, "Claude-3p")}
}

// claudeDesktopOpens: the desktop app here opens a session at its link in the background (/usr/bin/open -g), so a
// message to a session nothing runs and a new session can go to the app (claudedesktop_open.go, claudedesktop_new.go).
// macOS opens the link in the background; Windows uses its registered URI handler. A variable: tests model either app
// on any system, including a platform whose links cannot be opened.
var claudeDesktopOpens = runtime.GOOS == "darwin" || runtime.GOOS == "windows"

type claudeDesktopEntry struct {
	transcript  string   // cliSessionId
	archived    bool     // isArchived
	prior       []string // priorCliSessionIds, oldest first, then a lastAssistantCliSessionId of another transcript
	title, cwd  string   // as the sidebar shows it, and where it runs
	originCwd   string   // the original project directory, before the app creates a worktree
	local       string   // the app's own id of the session (sessionId, local_<uuid>): its link opens it (claudedesktop_open.go)
	environment string   // the saved local environment's id, when the app records an explicit association
	parent      string   // the local_<uuid> this one came from (spawnedFrom.sessionId, else forkedFromSessionId), or ""
	task        string   // the scheduled task this session is a run of (scheduledTaskId), or ""
	created     int64    // when the app made the session (createdAt, ms; 0 unknown): the newest run of a task heads it
	activity    int64    // when the session was last active (lastActivityAt, ms; 0 unknown): the newest entry of a session describes it

	// focused and turnEdge (ms, 0 unknown): when the app last brought the session into view (lastFocusedAt), and when a
	// turn of it last began or ended (lastTurnEdgeAt). claudedesktop_unread.go reads them.
	focused, turnEdge int64
}

type claudeDesktopFile struct {
	mtime time.Time
	size  int64
	entry claudeDesktopEntry // transcript "": unreadable
	// mentions are the ids an unreadable entry holds as far as it is read (mentionedIDs): none of them is gone.
	mentions []string
}

type claudeDesktopAssociation struct {
	transcripts []string
	markerSeen  bool // the last scans saw deleted_<local uuid>; its disappearance retires this association
	used        uint64
}

var claudeDesktopFiles = struct {
	sync.Mutex
	byPath map[string]claudeDesktopFile
	// associations retains what a readable local_<uuid>.json named after the entry is removed, keyed by the tombstone's
	// path beside it. It is process-local: only an entry actually observed by this process can make a local-only
	// deleted_<uuid> name its transcripts. The bound and markerSeen keep stale associations from growing forever.
	associations     map[string]*claudeDesktopAssociation
	associationClock uint64
}{byPath: map[string]claudeDesktopFile{}, associations: map[string]*claudeDesktopAssociation{}}

const (
	maxDesktopAssociations           = 1024
	maxDesktopAssociationTranscripts = 128
)

// claudeDesktopIndex is what the desktop apps' session index says about transcripts.
type claudeDesktopIndex struct {
	visible      map[string]bool                     // a session's current transcript → some entry naming it is unarchived
	earlier      map[string][]claudeDesktopNow       // a transcript none runs now → the sessions that ran it before
	about        map[string]claudeDesktopAbout       // an unarchived session's current transcript → its title and directory
	environments map[string]claudeDesktopEnvironment // profile/account-qualified saved local environments
	// parents: an unarchived session's current transcript → the current transcript of the unarchived session that
	// started it or that it was forked from (spawnedFrom, forkedFromSessionId), for linkSideSessions.
	parents map[string]string
	// origins: an unarchived session's current transcript → the folder it was started in (originCwd), for
	// placeClaudeDesktop.
	origins map[string]string
	seen    map[string]claudeDesktopSeen // an unarchived session's current transcript → its two times (claudedesktop_unread.go)
	// lastShown (ms) is the latest time the app brought any unarchived session into view: the session it shows now
	// was brought into view then (claudeDesktopIndex.shown).
	lastShown int64
	// named: every transcript some entry names (current or earlier, archived or not) or an unreadable entry mentions;
	// entries counts the readable entries. gone reads both.
	named   map[string]bool
	entries int
	// deleted: the ids the app's deleted_<uuid> markers name (deletedMarkers): a transcript among them is gone at once.
	deleted map[string]bool
	// deletedFromEntry: transcripts a deleted local session's previously readable entry named. A local-only tombstone
	// hides exactly these, never every unnamed old transcript.
	deletedFromEntry map[string]bool
	// before: an unarchived session's current transcript → the transcripts it ran before that none runs now, newest
	// first (at most maxDesktopAssociationTranscripts): its row's aliases (foldDesktopEarlier, #14394).
	before map[string][]string
}

type claudeDesktopAbout struct{ title, cwd, originCwd, local, environment string }

// claudeDesktopNow is a session that went on from an earlier transcript: the one it runs now, and whether it is archived.
type claudeDesktopNow struct {
	transcript string
	archived   bool
}

// readClaudeDesktopIndex reads the index under home; a file is parsed again only when it changed.
func readClaudeDesktopIndex(home string) claudeDesktopIndex {
	var paths []string
	deleted, markerPaths := deletedMarkers(home)
	x := claudeDesktopIndex{visible: map[string]bool{}, earlier: map[string][]claudeDesktopNow{}, about: map[string]claudeDesktopAbout{},
		environments: map[string]claudeDesktopEnvironment{}, parents: map[string]string{}, origins: map[string]string{}, seen: map[string]claudeDesktopSeen{}, named: map[string]bool{},
		deleted: deleted, deletedFromEntry: map[string]bool{}, before: map[string][]string{}}
	dirs := claudeDesktopDirs(home)
	for _, dir := range dirs {
		found, _ := filepath.Glob(filepath.Join(dir, "claude-code-sessions", "*", "*", "local_*.json"))
		paths = append(paths, found...)
		if environments, err := readClaudeDesktopEnvironments(dir); err == nil {
			for _, environment := range environments {
				x.environments[claudeDesktopEnvironmentID(dir, environment.AccountID, environment.ID)] = environment
			}
		}
	}
	claudeDesktopFiles.Lock()
	defer claudeDesktopFiles.Unlock()
	if claudeDesktopFiles.associations == nil {
		claudeDesktopFiles.associations = map[string]*claudeDesktopAssociation{}
	}
	currentEntries := map[string]bool{}
	currentMarkers := make(map[string]bool, len(markerPaths))
	for _, p := range markerPaths {
		currentMarkers[p] = true
	}
	current := make(map[string]bool, len(paths))
	byLocal := map[string]claudeDesktopEntry{} // the sessions by the app's own id, to find a side session's parent
	var started []claudeDesktopEntry           // the unarchived sessions another session started or forked
	runs := map[string][]claudeDesktopEntry{}  // a scheduled task → its unarchived runs no session started
	var read []desktopEntryFile                // the readable entries, copies of one session included
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		current[p] = true
		f, ok := claudeDesktopFiles.byPath[p]
		if !ok || !f.mtime.Equal(st.ModTime()) || f.size != st.Size() {
			f = claudeDesktopFile{mtime: st.ModTime(), size: st.Size()}
			if st.Size() <= 4<<20 {
				f.entry = readClaudeDesktopEntry(p)
			}
			if f.entry.transcript == "" {
				f.mentions = mentionedIDs(p)
			}
			claudeDesktopFiles.byPath[p] = f
		}
		for _, id := range f.mentions {
			x.named[id] = true
		}
		e := f.entry
		if e.transcript == "" {
			continue
		}
		x.entries++
		x.named[e.transcript] = true
		for _, old := range e.prior {
			x.named[old] = true
		}
		read = append(read, desktopEntryFile{entry: e, path: p, mtime: f.mtime})
		if e.local != "" {
			key := filepath.Join(filepath.Dir(p), "deleted_"+strings.TrimPrefix(e.local, "local_"))
			currentEntries[key] = true
			a := claudeDesktopFiles.associations[key]
			if a == nil {
				a = &claudeDesktopAssociation{}
				claudeDesktopFiles.associations[key] = a
			}
			a.transcripts = desktopAssociationTranscripts(e)
			if !currentMarkers[key] {
				a.markerSeen = false
			}
			claudeDesktopFiles.associationClock++
			a.used = claudeDesktopFiles.associationClock
		}
	}
	sessions := desktopSessions(read)
	for _, s := range sessions {
		e, p := s.head.entry, s.head.path
		x.visible[e.transcript] = x.visible[e.transcript] || !e.archived
		if !e.archived {
			environment := ""
			if e.environment != "" {
				accountDir := filepath.Dir(filepath.Dir(p))
				profileDir := filepath.Dir(filepath.Dir(accountDir))
				environment = claudeDesktopEnvironmentID(profileDir, filepath.Base(accountDir), e.environment)
			}
			x.about[e.transcript] = claudeDesktopAbout{title: e.title, cwd: e.cwd, originCwd: e.originCwd, local: e.local, environment: environment}
			if e.originCwd != "" {
				x.origins[e.transcript] = e.originCwd
			}
			x.seen[e.transcript] = claudeDesktopSeen{focused: e.focused, turnEdge: e.turnEdge}
			x.lastShown = max(x.lastShown, e.focused)
			if e.parent != "" {
				started = append(started, e)
			} else if e.task != "" {
				runs[e.task] = append(runs[e.task], e)
			}
		}
		if e.local != "" {
			byLocal[e.local] = e
		}
		for _, old := range s.earlier {
			x.earlier[old] = append(x.earlier[old], claudeDesktopNow{e.transcript, e.archived})
		}
	}
	// A transcript a session runs now is that session's, whatever another one ran before (hidden reads it so too).
	for old := range x.earlier {
		if _, now := x.visible[old]; now {
			delete(x.earlier, old)
		}
	}
	for _, s := range sessions {
		if e := s.head.entry; !e.archived {
			for _, old := range s.earlier {
				if _, ranBefore := x.earlier[old]; ranBefore && len(x.before[e.transcript]) < maxDesktopAssociationTranscripts &&
					!slices.Contains(x.before[e.transcript], old) {
					x.before[e.transcript] = append(x.before[e.transcript], old)
				}
			}
		}
	}
	for _, p := range markerPaths {
		a := claudeDesktopFiles.associations[p]
		if a == nil {
			continue
		}
		for _, id := range a.transcripts {
			x.deletedFromEntry[id] = true
		}
		a.markerSeen = true
		claudeDesktopFiles.associationClock++
		a.used = claudeDesktopFiles.associationClock
	}
	for key, a := range claudeDesktopFiles.associations {
		if !desktopPathInAny(key, dirs) {
			continue // another home's scan owns this association's marker-disappearance cleanup
		}
		if !currentEntries[key] && a.markerSeen && !currentMarkers[key] {
			delete(claudeDesktopFiles.associations, key)
		}
	}
	for len(claudeDesktopFiles.associations) > maxDesktopAssociations {
		var oldestKey string
		var oldest uint64
		for key, a := range claudeDesktopFiles.associations {
			if oldestKey == "" || a.used < oldest {
				oldestKey, oldest = key, a.used
			}
		}
		delete(claudeDesktopFiles.associations, oldestKey)
	}
	for _, e := range started {
		if p, ok := byLocal[e.parent]; ok && !p.archived && p.transcript != e.transcript {
			x.parents[e.transcript] = p.transcript
		}
	}
	for _, rs := range runs { // a task's runs under its newest (the latest createdAt; the larger transcript id on a tie)
		head := rs[0]
		for _, e := range rs[1:] {
			if e.created > head.created || e.created == head.created && e.transcript > head.transcript {
				head = e
			}
		}
		for _, e := range rs {
			if e.transcript != head.transcript {
				x.parents[e.transcript] = head.transcript
			}
		}
	}
	for p := range claudeDesktopFiles.byPath {
		if !current[p] {
			delete(claudeDesktopFiles.byPath, p)
		}
	}
	return x
}

// desktopEntryFile is a readable entry of the index, with its file and when that was last written.
type desktopEntryFile struct {
	entry claudeDesktopEntry
	path  string
	mtime time.Time
}

// desktopSession is one session of the app: the copy of its entry that names the transcript it runs now (head), and
// the other transcripts its copies name, newest first, never the head's.
type desktopSession struct {
	head    desktopEntryFile
	earlier []string
}

// desktopSessions groups the readable entries (in path order) by the session they are, the app's own id (sessionId; an
// entry without one is a session of its own), in the order the sessions first come. A session's copies under the app's
// other account and organization folders keep the cliSessionId and isArchived they had when written (#14394): a copy
// whose transcript a copy names as an earlier one is from before the session went on. The head is the newest file of
// the copies whose transcripts no copy names so (of all of them, in a loop no app writes; the larger transcript id on a
// tie). Its transcript and archived flag are the session's; the earlier transcripts are the head's (priorCliSessionIds
// and lastAssistantCliSessionId, latest first), then each other copy's transcript and earlier ones, newer copies first.
func desktopSessions(read []desktopEntryFile) []desktopSession {
	groups := map[string][]desktopEntryFile{}
	var order []string
	for _, r := range read {
		key := cmp.Or(r.entry.local, r.path)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], r)
	}
	out := make([]desktopSession, 0, len(order))
	for _, key := range order {
		copies := groups[key]
		ranBefore := map[string]bool{}
		for _, c := range copies {
			for _, old := range c.entry.prior {
				ranBefore[old] = true
			}
		}
		// Newest first: the copy active last (lastActivityAt) describes the session, then the later-written file (#15262).
		slices.SortStableFunc(copies, func(a, b desktopEntryFile) int {
			return cmp.Or(cmp.Compare(b.entry.activity, a.entry.activity), b.mtime.Compare(a.mtime),
				strings.Compare(b.entry.transcript, a.entry.transcript))
		})
		head := 0
		for i, c := range copies {
			if !ranBefore[c.entry.transcript] {
				head = i
				break
			}
		}
		s := desktopSession{head: copies[head]}
		seen := map[string]bool{s.head.entry.transcript: true}
		add := func(id string) {
			if validID(id) && !seen[id] {
				seen[id] = true
				s.earlier = append(s.earlier, id)
			}
		}
		addPrior := func(e claudeDesktopEntry) {
			for j := len(e.prior) - 1; j >= 0; j-- {
				add(e.prior[j])
			}
		}
		addPrior(s.head.entry)
		for i, c := range copies {
			if i != head {
				add(c.entry.transcript)
				addPrior(c.entry)
			}
		}
		out = append(out, s)
	}
	return out
}

// desktopAssociationTranscripts bounds what one readable entry retains: its current transcript first, then each
// distinct valid earlier transcript. The entry itself is already capped at 4 MiB; these caps bound the retained copy.
func desktopAssociationTranscripts(e claudeDesktopEntry) []string {
	out := make([]string, 0, 1+min(len(e.prior), maxDesktopAssociationTranscripts-1))
	seen := map[string]bool{e.transcript: true}
	out = append(out, e.transcript)
	for _, id := range e.prior {
		if len(out) == maxDesktopAssociationTranscripts {
			break
		}
		if validID(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func desktopPathInAny(path string, dirs []string) bool {
	for _, dir := range dirs {
		if dir = filepath.Clean(dir); path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// deletedMarkers are the ids the app's deleted_<uuid> markers name under home: a deleted session's own (local_<uuid>
// without its prefix) and the transcripts it ran. The paths retain which index directory each marker belongs to so a
// local session id can be matched only to an entry observed beside it.
func deletedMarkers(home string) (map[string]bool, []string) {
	out := map[string]bool{}
	var paths []string
	for _, dir := range claudeDesktopDirs(home) {
		found, _ := filepath.Glob(filepath.Join(dir, "claude-code-sessions", "*", "*", "deleted_*"))
		for _, p := range found {
			if id := strings.TrimPrefix(filepath.Base(p), "deleted_"); validID(id) {
				out[id] = true
				paths = append(paths, p)
			}
		}
	}
	return out, paths
}

func readClaudeDesktopEntry(path string) claudeDesktopEntry {
	b, err := os.ReadFile(path)
	if err != nil {
		return claudeDesktopEntry{}
	}
	var raw struct {
		Transcript  string          `json:"cliSessionId"`
		Archived    bool            `json:"isArchived"`
		Prior       []string        `json:"priorCliSessionIds"`
		Title       string          `json:"title"`
		Cwd         string          `json:"cwd"`
		OriginCwd   string          `json:"originCwd"`
		Local       string          `json:"sessionId"`
		Environment json.RawMessage `json:"startedFromEnvironmentId"`
		// Read on their own: a spawnedFrom or forkedFromSessionId of another shape leaves the entry readable, with no
		// parent from it.
		SpawnedFrom json.RawMessage `json:"spawnedFrom"`
		ForkedFrom  json.RawMessage `json:"forkedFromSessionId"`
		Focused     float64         `json:"lastFocusedAt"`
		TurnEdge    float64         `json:"lastTurnEdgeAt"`
		Task        string          `json:"scheduledTaskId"`
		Created     float64         `json:"createdAt"`
		Activity    float64         `json:"lastActivityAt"`
		Detached    bool            `json:"lineageDetached"` // moved to the sidebar's top level: the old lineage stays, unnested
		// The transcript of the session's last answer: until a session that went on in a new transcript answers there,
		// the one it went on from (#14394). Read on its own, as spawnedFrom.
		LastAnswer json.RawMessage `json:"lastAssistantCliSessionId"`
	}
	if json.Unmarshal(b, &raw) != nil || !validID(raw.Transcript) {
		return claudeDesktopEntry{}
	}
	var last string
	if json.Unmarshal(raw.LastAnswer, &last) == nil && validID(last) && last != raw.Transcript && !slices.Contains(raw.Prior, last) {
		raw.Prior = append(raw.Prior, last)
	}
	// The session this one came from, as the app's sidebar finds it: spawnedFrom.sessionId, else forkedFromSessionId
	// (a fork made with the app's Fork writes only that); none once the user moved it to the top level.
	var spawned struct {
		Local string `json:"sessionId"`
	}
	var forked string
	if len(raw.SpawnedFrom) > 0 && json.Unmarshal(raw.SpawnedFrom, &spawned) != nil {
		spawned.Local = ""
	}
	if len(raw.ForkedFrom) > 0 && json.Unmarshal(raw.ForkedFrom, &forked) != nil {
		forked = ""
	}
	parent := cmp.Or(spawned.Local, forked)
	if raw.Detached {
		parent = ""
	}
	cwd, origin := raw.Cwd, raw.OriginCwd
	if !filepath.IsAbs(cwd) {
		cwd = ""
	}
	var environment string
	if json.Unmarshal(raw.Environment, &environment) != nil || len(environment) > 128 {
		environment = ""
	}
	if !filepath.IsAbs(origin) {
		origin = ""
	}
	return claudeDesktopEntry{transcript: raw.Transcript, archived: raw.Archived, prior: raw.Prior, title: raw.Title, cwd: cwd, originCwd: origin, environment: environment,
		local: localID(raw.Local), parent: localID(parent), focused: int64(raw.Focused), turnEdge: int64(raw.TurnEdge),
		task: raw.Task, created: int64(raw.Created), activity: int64(raw.Activity)}
}

// mentionedIDs are the session ids in the first 64 KB of an entry that cannot be read (cut short while the app wrote
// it, too big, another shape): what it names is not known, so a transcript it may name is never taken for gone.
func mentionedIDs(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	return desktopIDRe.FindAllString(string(b), -1)
}

var desktopIDRe = lazyre.New(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// desktopEntryGrace is how long a transcript the desktop app ran may go without an entry naming it before it is taken
// for a deleted session's: the app writes a new session's entry as it starts the session.
const desktopEntryGrace = 2 * time.Minute

// gone: transcript id, which the desktop app ran, belongs to a session deleted in the app: no entry names id and nothing
// unreadable mentions it, and either a deleted_<id> marker says so, the local session's retained entry association says
// so, or the index has readable entries and the transcript was last written (written) longer than desktopEntryGrace ago.
// A local-only marker without an observed association never widens the age fallback. The caller asks only about a
// transcript whose entrypoint is the desktop app's.
func (x claudeDesktopIndex) gone(id string, written time.Time) bool {
	return !x.named[id] && (x.deleted[id] || x.deletedFromEntry[id] || x.entries > 0 && time.Since(written) > desktopEntryGrace)
}

// localID is id when it is the app's own form of a session id (local_<uuid>), else "": it goes into a link.
func localID(id string) string {
	if !strings.HasPrefix(id, "local_") || !validID(strings.TrimPrefix(id, "local_")) {
		return ""
	}
	return id
}

// listed: transcript id is the one an unarchived session of the desktop app runs: the sidebar lists it, so the offline
// list does whatever its age (up to maxDesktopOffline, the newest first), on a count of its own (#3304).
func (x claudeDesktopIndex) listed(id string) bool { return x.visible[id] }

// maxDesktopOffline bounds the desktop app's top-level sessions the offline list shows (the newest, whatever their age);
// they are counted on their own, not toward the 80 of the others. It only guards against a runaway index: the list
// follows the sidebar, which keeps every unarchived session. It was 100, and a sidebar outgrows that (one Mac had 208
// top-level sessions): the ones quiet for a week or more were cut, newest transcript first, while the sidebar still
// showed them (#15522). Folded sessions have their own bound below (#15279).
const maxDesktopOffline = 1000

// maxDesktopFolded bounds the desktop app's sessions the offline list shows under another row (the index gives them a
// parent, claudeDesktopIndex.parents): the newest, counted apart from maxDesktopOffline. A daily scheduled task leaves
// one a day, and they took the places of the top-level sessions (#15279).
const maxDesktopFolded = 300

// hidden: the offline list leaves transcript id out as the desktop app's sidebar does. shown are the transcripts the
// list has shown so far (newest first, so a session's current one normally comes before its earlier ones; the list
// folds the rest, foldDesktopEarlier).
func (x claudeDesktopIndex) hidden(id string, shown map[string]bool) bool {
	if visible, named := x.visible[id]; named {
		return !visible // a session's current transcript: hidden while every session running it is archived
	}
	return wentOn(x.earlier[id], shown)
}

// wentOn: a transcript that sessions ran before (claudeDesktopIndex.earlier) is theirs no more: an unarchived one of
// them is listed through the transcript it runs now (in listed), or only archived ones ran it. While an unarchived
// session's current transcript is not listed, its earlier one stands in for it: at worst a session shows twice, never
// not at all.
func wentOn(sessions []claudeDesktopNow, listed map[string]bool) bool {
	if len(sessions) == 0 {
		return false
	}
	open := false
	for _, s := range sessions {
		if !s.archived {
			open = true
			if listed[s.transcript] {
				return true // its session is listed through the transcript it runs now
			}
		}
	}
	return !open // only archived sessions ran it
}

// desktopFolds is what the list folds of the desktop app's sessions, read with the offline scan (offlineFolds):
// claudeDesktopIndex.earlier and .before.
type desktopFolds struct {
	earlier map[string][]claudeDesktopNow
	before  map[string][]string
}

// foldDesktopEarlier lists each session of the desktop app once, as its sidebar does (#14394): the row of a transcript
// one of its sessions went on from goes, live or at rest, once an unarchived session that ran it is listed through the
// transcript it runs now; at rest also when only archived sessions ran it (wentOn, as the scan hides it), while a
// claude of the app still running it is not judged by the archive. The scan's newest-first rule (hidden) misses one
// written after the current transcript, and the live list had no rule. The row of a session's current transcript names
// the transcripts it ran before that no row lists in its aliases (on the row's own copy), so the manager shows no
// archive record under an old id. Left as they are: the rows keep (the main conversation, Persona's branches, as the
// scan keeps them), an assistant conversation, a row in a pane or of another source (a terminal's claude that took up
// an earlier transcript), one Persona is starting, and one that waits on the user, whose question or prompt stays
// reachable. The rows kept stay in order.
func foldDesktopEarlier(list []proto.Session, f desktopFolds, keep map[string]bool) []proto.Session {
	if len(f.earlier) == 0 {
		return list // no session went on from a transcript: nothing goes, and no row has one to name
	}
	listed := make(map[string]bool, len(list))
	for i := range list {
		listed[list[i].SessionID] = true
	}
	gone := map[string]bool{}
	for i := range list {
		s := &list[i]
		sessions := f.earlier[s.SessionID]
		if len(sessions) == 0 || keep[s.SessionID] || s.Assistant || s.PaneID != "" || s.Starting || s.Status == proto.StatusBlocked {
			continue
		}
		switch resting := s.Status == proto.StatusOffline || s.Status == proto.StatusInterrupted; {
		case resting && (s.SourceKind == "" || s.SourceKind == proto.SourceClaudeDesktop):
		case !resting && s.SourceKind == proto.SourceClaudeDesktop:
			// A claude of the app still running it: only into a row its unarchived session is listed through, as the
			// offline rules never judge a running session by the archive.
			sessions = slices.DeleteFunc(slices.Clone(sessions), func(c claudeDesktopNow) bool { return c.archived })
		default:
			continue
		}
		if wentOn(sessions, listed) {
			gone[s.SessionID] = true
		}
	}
	j := 0
	for i := range list {
		s := &list[i]
		if gone[s.SessionID] {
			continue
		}
		if ids := f.before[s.SessionID]; len(ids) > 0 {
			aliases := slices.Clone(s.Aliases())
			for _, id := range ids {
				if (!listed[id] || gone[id]) && !slices.Contains(aliases, id) {
					aliases = append(aliases, id)
				}
			}
			s.SetAliases(aliases)
		}
		if j != i {
			list[j] = *s
		}
		j++
	}
	clear(list[j:])
	return list[:j]
}

// linkSideSessions draws each of the desktop app's side sessions under the session that started it (parent_agent_id,
// #5935): parents maps a side session's transcript to its parent's (claudeDesktopIndex.parents). The parent must be a
// row of list too; a session whose parents lead back to it (a loop the app should never write) stays a root. Each
// link is set on the row's own copy of its hierarchy (SetParentAgentID), never on one another copy shares.
func linkSideSessions(list []proto.Session, parents map[string]string) {
	if len(parents) == 0 {
		return
	}
	listed := make(map[string]bool, len(list))
	for i := range list {
		listed[list[i].SessionID] = true
	}
	for i := range list {
		s := &list[i]
		if p := parents[s.SessionID]; p != "" && listed[p] && !loopsBack(s.SessionID, parents) {
			s.SetParentAgentID(p)
		}
	}
}

// placeClaudeDesktop puts each of the desktop app's sessions in its project, as the app's sidebar groups them and as
// the Codex App's chats are placed (decorateCodexSession). Without a space a session was in no project in every
// client's list by project, a session in a worktree the app made (<repo>/.claude/worktrees/<name>) included (#8521).
// The project is the repository the session works in, a linked worktree's main checkout (repo_root, fillGitPlace);
// until that is read, and for a folder that is no repository, the folder the app started the session in (origins, the
// index's originCwd). The home folder, or one above it, is no project (as the apps' lists by app have it). The space is
// the one the Codex App's chats of the same repository are in (projectSpace), so a repository is one project on a
// computer whichever app works in it. Nothing here runs git: repo_root comes from the list's own cache of Git places.
// The app's sessions are the ones a claude of the app runs (claude_desktop) and the transcripts it ran that nothing
// runs now (the desktop surface, no source, no pane); a session some host placed keeps its place.
func placeClaudeDesktop(list []proto.Session, origins map[string]string, home string) {
	homes := []string{} // the home folder as written and by its physical path: a root may name it either way
	if home != "" {
		home = filepath.Clean(home)
		homes = append(homes, home)
		if real := physicalDirs.lookup(home).root; real != "" && real != home {
			homes = append(homes, real)
		}
	}
	for i := range list {
		s := &list[i]
		resting := s.SourceKind == "" && s.AgentSurface == transcript.SurfaceDesktop && s.PaneID == "" && s.Agent == "claude"
		if s.SourceKind != proto.SourceClaudeDesktop && !resting || s.SpaceID != "" || s.Assistant {
			continue
		}
		root := cmp.Or(s.RepoRoot, origins[s.SessionID])
		if !filepath.IsAbs(root) {
			continue
		}
		root = filepath.Clean(root)
		if slices.ContainsFunc(homes, func(h string) bool { return pathWithin(h, root) }) {
			continue
		}
		s.SpaceKind, s.SpaceID, s.SpaceName = proto.SourceClaudeDesktop, projectSpace+root, filepath.Base(root)
	}
}

// loopsBack: following parents up from id comes back to id.
func loopsBack(id string, parents map[string]string) bool {
	seen := map[string]bool{}
	for p := parents[id]; p != ""; p = parents[p] {
		if p == id {
			return true
		}
		if seen[p] {
			return false // a loop above id, which does not contain it
		}
		seen[p] = true
	}
	return false
}
