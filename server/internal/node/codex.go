package node

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Codex support: herdr reports Codex panes as agent "codex"; their conversations are rollouts under
// ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl (parsed by transcript/codex.go). Compatible Codex
// variants use the same layout in their own data directories. herdr's Codex integration reports the session id when
// Codex starts; a Codex started before that integration has none. Codex puts
// the thread's name in the terminal title ("<name> | <directory>"). Until a herdr pane identifies its thread, it
// has its own waiting row (codex_waiting.go). Its working directory can contain rollouts from many earlier panes.
//
// Subagents write rollouts of their own next to the conversation, in the same directory: Codex's thread_spawn helpers
// and the compatible CLI's guardian, which reviews each tool call before it runs. A pane never shows one: they are skipped when
// guessing, and a subagent's id maps to the conversation it belongs to (rolloutMeta).

const codexWindow = 14 * 24 * time.Hour
const rolloutScanInterval = 5 * time.Second

type codexRollout struct {
	id    string
	path  string
	mtime time.Time
	rolloutMeta
}

// rolloutMeta is what a rollout's first line (session_meta) says about its thread.
type rolloutMeta struct {
	threadID string
	cwd      string
	sub      bool   // a subagent: thread_source "subagent", or a source of {"subagent": …}
	root     string // the conversation a subagent belongs to (session_id, else parent_thread_id); "" when not given
}

// rolloutCache is one rollout home's scan (Codex: ~/.codex, a compatible CLI: the CLI-specific data directory — the same layout).
type rolloutCache struct {
	mu       sync.Mutex // protects this home while its filesystem may be waiting for access
	at       time.Time
	rollouts []codexRollout
	segments []codexRollout // every file of rollouts', a continued thread's older segments too (rolloutSegments)
	older    []rolloutFile  // rollouts in day directories before the window and archived ones, listed once a minute
	olderAt  time.Time
	// misses are the ids rolloutPath globbed for and found nowhere: a thread with no rollout (an app-server thread that
	// never wrote one, an App chat whose rollout Codex deleted) was globbed across all of
	// history every second. A miss holds while the directories a new rollout appears in (missDirs) are unchanged; when
	// they changed, while no rollout name in them has the thread's id (recentNames). A rollout put into an older day
	// directory is seen when rollouts lists the older days again (a minute), which lets that thread's miss go.
	misses map[string]rolloutMiss
	// hits are the paths rolloutPath found for an id with that missLook's stamp. Their file and header are checked again
	// on every call; a recentNames look without the id or the next older-day relist keeps the hit from going stale. A
	// rollout Codex compressed to .jsonl.zst (#17768) is a hit, not a miss.
	hits map[string]rolloutHit
	// recentNames are the rollout names in missDirs when their missStamp was recentStamp ("\n" after each name): listed
	// again only when a directory changes.
	recentNames string
	recentStamp []int64
	relistedAt  time.Time              // when the look of the last relistMisses was taken
	metas       map[string]rolloutMeta // path → its session_meta (never changes once written); gone paths leave it once a minute
	titles      map[string]string
	former      map[string][]string // id → the names session_index.jsonl gave the thread before its latest (a rename)
	titlesAt    time.Time
}

var rolloutCaches = struct {
	sync.Mutex
	byHome map[string]*rolloutCache
}{byHome: map[string]*rolloutCache{}}

var rolloutIDRe = lazyre.New(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:_[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})*\.jsonl(?:\.zst)?$`)

// rolloutFile is a rollout's path and the thread id its name ends in.
type rolloutFile struct {
	path string
	id   string
}

// rolloutID is a rollout filename's thread UUID, before any _<segment UUID> suffix, or empty when absent:
// what rolloutIDRe captures, without a regular expression. rollouts reads it for every rollout on disk once a minute,
// where rolloutIDRe took most of each refresh's CPU time.
func rolloutID(path string) string {
	path = strings.TrimSuffix(path, ".zst")
	const ext, idLen = ".jsonl", 36
	if len(path) < idLen+len(ext) || !strings.HasSuffix(path, ext) {
		return ""
	}
	end := len(path) - len(ext)
	start := end - idLen
	if !rolloutUUID(path[start:end]) {
		return ""
	}
	for start >= idLen+1 && path[start-1] == '_' && rolloutUUID(path[start-idLen-1:start-1]) {
		end, start = start-1, start-idLen-1
	}
	return path[start:end]
}

func rolloutUUID(id string) bool {
	const idLen = 36
	if len(id) != idLen {
		return false
	}
	for i := range idLen {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}

func (n *Node) codexHome() string {
	if n.sessionScope != nil {
		return n.sessionScope.codex
	}
	return filepath.Join(n.home, ".codex")
}

func (n *Node) codexPath(id string) string {
	return n.codexPathIn(id, nil)
}
func (n *Node) codexPathIn(id string, pass *codexListPass) string {
	if s, ok := n.codexSource(id); ok {
		return rolloutPathIn(s.home, s.thread, pass)
	}
	return rolloutPathIn(n.codexHome(), id, pass)
}
func (n *Node) codexTitle(id string) string {
	title, _ := n.codexTitleIn(id, nil)
	return title
}
func (n *Node) codexTitleIn(id string, pass *codexListPass) (string, bool) {
	if s, ok := n.codexSource(id); ok {
		return threadTitleIn(s.home, s.thread, pass)
	}
	return threadTitleIn(n.codexHome(), id, pass)
}

func cacheFor(home string) *rolloutCache {
	c := rolloutCaches.byHome[home]
	if c == nil {
		c = &rolloutCache{metas: map[string]rolloutMeta{}}
		rolloutCaches.byHome[home] = c
	}
	return c
}

// rolloutCacheFor takes the shared map lock only to look up a home. Filesystem work uses that home's lock.
func rolloutCacheFor(home string) *rolloutCache {
	rolloutCaches.Lock()
	defer rolloutCaches.Unlock()
	return cacheFor(home)
}

// tryLockRolloutCache acquires home's cache mutex. In a list pass (pass != nil), acquisition is
// nonblocking: if another reader holds the mutex, home is marked unavailable for this pass instead
// of blocking the whole session list across homes.
func tryLockRolloutCache(home string, pass *codexListPass) (*rolloutCache, bool) {
	c := rolloutCacheFor(home)
	if pass == nil {
		c.mu.Lock()
		return c, true
	}
	if pass.homeUnavailable(home) {
		return nil, false
	}
	if !c.mu.TryLock() {
		pass.markHomeUnavailable(home)
		return nil, false
	}
	return c, true
}

// rollouts lists a home's rollouts written in the last two weeks, newest first; cached for 5s. A rollout lives in the
// directory of the day its thread started (<home>/sessions/YYYY/MM/DD) and grows there for as long as the thread is
// resumed: one started six weeks earlier and in use every day was not found at all (2026-10-31), so the older days
// are listed too, once a minute, and kept when written lately. Every older rollout is still looked at on each refresh,
// so a thread resumed after weeks shows within 5 seconds.
func rollouts(home string) []codexRollout {
	list, _ := rolloutsIn(home, nil)
	return list
}

func rolloutsIn(home string, pass *codexListPass) ([]codexRollout, bool) {
	c, ok := tryLockRolloutCache(home, pass)
	if !ok {
		return nil, false
	}
	defer c.mu.Unlock()
	now := time.Now()
	if now.Sub(c.at) < rolloutScanInterval {
		return c.rollouts, true
	}
	var files []rolloutFile
	recent := map[string]bool{}
	for d := 0; d <= int(codexWindow/(24*time.Hour)); d++ {
		day := now.AddDate(0, 0, -d)
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		recent[dir] = true
		paths, _ := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl*"))
		for _, p := range paths {
			if id := rolloutID(p); id != "" {
				files = append(files, rolloutFile{path: p, id: id})
			}
		}
	}
	if now.Sub(c.olderAt) > time.Minute {
		look := newMissLook(home) // before listing: a rollout written meanwhile moves a directory past this stamp
		all, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl*"))
		archived, _ := filepath.Glob(filepath.Join(home, "archived_sessions", "rollout-*.jsonl*"))
		all = append(all, archived...)
		c.older = c.older[:0]
		for _, p := range all {
			if id := rolloutID(p); id != "" && !recent[filepath.Dir(p)] {
				c.older = append(c.older, rolloutFile{path: p, id: id})
			}
		}
		c.olderAt = now
		c.relistMisses(all, look)
		c.pruneMetas(files)
	}
	firstOlder := len(files)
	files = append(files, c.older...)
	var out []codexRollout
	for i, f := range files {
		st, err := os.Stat(f.path)
		if err != nil || (i >= firstOlder && now.Sub(st.ModTime()) > codexWindow) {
			continue
		}
		meta, ok := c.metas[f.path]
		if !ok || meta.threadID == "" { // a first line still being written must not stay cached as missing
			var whole bool
			if meta, whole = readRolloutMeta(f.path); whole {
				c.metas[f.path] = meta
			}
		}
		id := firstNonEmpty(meta.threadID, f.id)
		if meta.threadID == "" && !strings.HasSuffix(f.path, f.id+".jsonl") {
			continue // an ambiguous continuation header is still being written
		}
		out = append(out, codexRollout{id: id, path: f.path, mtime: st.ModTime(), rolloutMeta: meta})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].mtime.Equal(out[j].mtime) {
			return out[i].path > out[j].path
		}
		return out[i].mtime.After(out[j].mtime)
	})
	// A continuation is another physical rollout for the same logical thread. As with resumed
	// transcript copies, follow its newest file; do not advertise each segment as a new session.
	c.segments = slices.Clone(out)
	seen := map[string]bool{}
	unique := out[:0]
	for _, r := range out {
		if !seen[r.id] {
			seen[r.id] = true
			unique = append(unique, r)
		}
	}
	out = unique
	c.rollouts, c.at = out, now
	return out, true
}

// rolloutSegments is every rollout file rollouts lists (written in the last two weeks), each segment of a continued
// thread too, newest first: the token usage meter reads them all (usage_adapters.go), the session list a thread's
// newest.
func rolloutSegments(home string) []codexRollout {
	rollouts(home)
	c := rolloutCacheFor(home)
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.segments)
}

// allRolloutPaths is every rollout file of a home on disk, whenever it was written, archived ones too: what a token
// usage backfill reaching further back than rollouts' two weeks reads.
func allRolloutPaths(home string) []string {
	paths, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl*"))
	archived, _ := filepath.Glob(filepath.Join(home, "archived_sessions", "rollout-*.jsonl*"))
	return slices.DeleteFunc(append(paths, archived...), func(path string) bool { return rolloutID(path) == "" })
}

// pruneMetas keeps the session_meta of the rollouts just listed (recent and older), so a deleted or moved rollout
// does not stay for the life of the node. Into a new map: deleting from a map never gives its memory back.
func (c *rolloutCache) pruneMetas(recent []rolloutFile) {
	metas := make(map[string]rolloutMeta, min(len(c.metas), len(recent)+len(c.older)))
	for _, list := range [][]rolloutFile{recent, c.older} {
		for _, f := range list {
			if m, ok := c.metas[f.path]; ok {
				metas[f.path] = m
			}
		}
	}
	c.metas = metas
}

func codexRolloutReader(f *os.File) (io.Reader, func(), error) {
	if !strings.HasSuffix(f.Name(), ".zst") {
		return f, func() {}, nil
	}
	decoder, err := zstd.NewReader(f)
	if err != nil {
		return nil, func() {}, err
	}
	return decoder, decoder.Close, nil
}

// readRolloutMeta reads a rollout's first line (session_meta, tens of KB of instructions); whole says that line was all
// written, and only then is what it says worth keeping. Codex 0.167 creates the file empty and writes the line 8–19 ms
// later: a meta read in between and kept left the thread without a directory for good, and a start in that directory
// never found its thread (#2351).
func readRolloutMeta(path string) (m rolloutMeta, whole bool) {
	f, err := os.Open(path)
	if err != nil {
		return rolloutMeta{}, false
	}
	defer f.Close()
	reader, closeReader, err := codexRolloutReader(f)
	if err != nil {
		return rolloutMeta{}, false
	}
	defer closeReader()
	r := bufio.NewReaderSize(reader, 256<<10)
	line, err := r.ReadBytes('\n')
	whole = err == nil
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			ID           string          `json:"id"`
			Cwd          string          `json:"cwd"`
			SessionID    string          `json:"session_id"`
			ParentThread string          `json:"parent_thread_id"`
			ThreadSource string          `json:"thread_source"`
			Source       json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" {
		return rolloutMeta{}, whole
	}
	p := l.Payload
	m = rolloutMeta{cwd: p.Cwd}
	if rolloutUUID(p.ID) {
		m.threadID = p.ID
	}
	var source map[string]json.RawMessage
	m.sub = p.ThreadSource == "subagent" || (json.Unmarshal(p.Source, &source) == nil && source["subagent"] != nil)
	if m.sub {
		for _, id := range []string{p.SessionID, p.ParentThread} {
			if id != "" && id != p.ID {
				m.root = id
				break
			}
		}
	}
	return m, whole
}

// metaOf is a rollout's session_meta, read once it is all written (rollouts older than the scan window are read on
// demand).
func metaOf(home, path string) rolloutMeta {
	m, _ := metaOfIn(home, path, nil)
	return m
}

func metaOfIn(home, path string, pass *codexListPass) (rolloutMeta, bool) {
	c, ok := tryLockRolloutCache(home, pass)
	if !ok {
		return rolloutMeta{}, false
	}
	m, cached := c.metas[path]
	c.mu.Unlock()
	if cached && m.threadID != "" {
		return m, true
	}
	m, whole := readRolloutMeta(path)
	if whole {
		if c, ok := tryLockRolloutCache(home, pass); ok {
			c.metas[path] = m
			c.mu.Unlock()
		}
	}
	return m, true
}

// rolloutThreadID uses the header as the authority; the filename is a fallback while a header is incomplete.
func rolloutThreadID(home, path string) string {
	return rolloutThreadIDIn(home, path, nil)
}

func rolloutThreadIDIn(home, path string, pass *codexListPass) string {
	m, ok := metaOfIn(home, path, pass)
	if !ok {
		return ""
	}
	if m.threadID != "" {
		return m.threadID
	}
	if id := rolloutID(path); id != "" && strings.HasSuffix(path, id+".jsonl") {
		return id
	}
	return ""
}

// mainThread maps a subagent's thread to the conversation it belongs to; any other thread maps to itself.
func mainThread(home, id string) string {
	thread, _ := mainThreadRollout(home, id)
	return thread
}

func mainThreadRollout(home, id string) (string, string) {
	p := rolloutPath(home, id)
	if p == "" {
		return id, ""
	}
	if m := metaOf(home, p); m.sub && m.root != "" {
		if root := rolloutPath(home, m.root); root != "" {
			return m.root, root
		}
	}
	return id, p
}

// rolloutGlob is what rolloutPath globs with (a test counts the globs).
var rolloutGlob = filepath.Glob

// rolloutPath finds a session's rollout under a home by id.
func rolloutPath(home, id string) string { return rolloutPathIn(home, id, nil) }

// rolloutPathIn is rolloutPath with the look at the directories a miss depends on (missLook) taken once per home in a
// list pass (codexListPass): the list asks for every thread with no rollout each second, most of them twice.
func rolloutPathIn(home, id string, pass *codexListPass) string {
	list, ok := rolloutsIn(home, pass)
	if !ok {
		return ""
	}
	for _, r := range list {
		if r.id == id && fileExists(r.path) {
			return r.path
		}
	}
	if !validID(id) {
		return ""
	}
	look := pass.missLook(home)
	if look == nil {
		return ""
	}
	c, ok := tryLockRolloutCache(home, pass)
	if !ok {
		return ""
	}
	hit, hitOK := c.hits[id]
	missed, ok := c.misses[id]
	if ok && look.at.Sub(missed.asked) > missAskedEvery {
		missed.asked = look.at
		c.misses[id] = missed
	}
	c.mu.Unlock()
	if hitOK {
		var matches []string
		if slices.Equal(dirStamps(hit.dirs), hit.dirStamp) {
			matches = rolloutMatchesIn(home, id, hit.paths, pass)
			if pass.homeUnavailable(home) {
				return ""
			}
		}
		if len(matches) > 0 {
			if slices.Equal(hit.stamp, look.stamp) {
				return newestFirst(matches)[0]
			}
			// Another thread's recent rollout does not invalidate this one. A name for this thread may be its move or
			// continuation: look across history again, as the original lookup did.
			recent := look.recentNames()
			if pass.homeUnavailable(home) {
				return ""
			}
			if !strings.Contains(recent, "-"+id) {
				look.keepHit(id, matches)
				return newestFirst(matches)[0]
			}
		}
	}
	if ok && slices.Equal(missed.stamp, look.stamp) {
		return ""
	}
	// A directory changed since the miss, but no rollout name in them has this id: no glob across all of history (each
	// new rollout made every thread with none glob it again, 528 of them for 14 s of CPU).
	if ok {
		recent := look.recentNames()
		if pass.homeUnavailable(home) {
			return ""
		}
		if !strings.Contains(recent, "-"+id) {
			look.keepMiss(id)
			return ""
		}
	}
	if pass.homeUnavailable(home) {
		return ""
	}
	matches, _ := rolloutGlob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+id+"*.jsonl*"))
	matches = slices.DeleteFunc(matches, func(path string) bool { return rolloutID(path) == "" || rolloutThreadIDIn(home, path, pass) != id })
	if pass.homeUnavailable(home) {
		return ""
	}
	if len(matches) == 0 {
		matches, _ = rolloutGlob(filepath.Join(home, "archived_sessions", "rollout-*-"+id+"*.jsonl*"))
		matches = slices.DeleteFunc(matches, func(path string) bool { return rolloutID(path) == "" || rolloutThreadIDIn(home, path, pass) != id })
		if pass.homeUnavailable(home) {
			return ""
		}
	}
	if len(matches) == 0 {
		// A rollout moved between the two globs (unarchived from archived_sessions into an older day) is in neither.
		// The move changed a directory the look stamped: no miss on this look, so the next one globs again.
		if slices.Equal(dirStamps(look.dirs), look.stamp) {
			look.keepMiss(id)
		}
		return ""
	}
	look.keepHit(id, matches)
	return newestFirst(matches)[0]
}

// rolloutMatches revalidates cached paths without holding rolloutCaches' lock: rolloutThreadID's metadata cache takes
// that lock itself. A vanished file or a header that no longer names id is not a hit, and forces a fresh glob.
func rolloutMatches(home, id string, paths []string) []string {
	return rolloutMatchesIn(home, id, paths, nil)
}

func rolloutMatchesIn(home, id string, paths []string, pass *codexListPass) []string {
	matches := make([]string, 0, len(paths))
	for _, path := range paths {
		if pass.homeUnavailable(home) {
			return nil
		}
		if fileExists(path) && rolloutThreadIDIn(home, path, pass) == id {
			matches = append(matches, path)
		}
	}
	return matches
}

// rolloutMiss is a thread rolloutPath found no rollout of: the missStamp it holds for, and when it was last asked about
// (to within missAskedEvery). One not asked about for missKeptFor is let go when the older days are listed, so the
// misses kept are those of threads still asked about.
type rolloutMiss struct {
	stamp []int64
	asked time.Time
}

// rolloutHit is a thread's matching paths at one missLook. Unlike a miss it is checked against the files on every call;
// several continuation segments stay together so newestFirst still picks among all of them. Their parent directories'
// stamps notice a new segment in the same old day directory immediately, without waiting for the minute listing.
type rolloutHit struct {
	paths    []string
	dirs     []string
	stamp    []int64
	dirStamp []int64
}

const (
	missAskedEvery = 10 * time.Second
	missKeptFor    = 2 * time.Minute
)

// missLook is one look at the directories a new rollout appears in (missDirs): their missStamp, when it was taken and,
// once asked for, the rollout names in them.
type missLook struct {
	home  string
	at    time.Time
	dirs  []string
	stamp []int64
	names *string
	pass  *codexListPass
}

func newMissLook(home string) *missLook { return newMissLookIn(home, nil) }

func newMissLookIn(home string, pass *codexListPass) *missLook {
	now := time.Now()
	dirs := missDirs(home, now)
	return &missLook{home: home, at: now, dirs: dirs, stamp: dirStamps(dirs), pass: pass}
}

// settled: no directory changed in the second before the look. One changed within it may change again within its
// mtime's granularity (a coarse clock tick) without its mtime moving, so nothing seen then is kept for later looks.
func (l *missLook) settled() bool { return slices.Max(l.stamp) < l.at.Add(-time.Second).UnixNano() }

// relistedSince: a listing of every directory of home (relistMisses) began after at.
func relistedSince(home string, at time.Time) bool {
	relisted, _ := relistedSinceIn(home, at, nil)
	return relisted
}

func relistedSinceIn(home string, at time.Time, pass *codexListPass) (bool, bool) {
	c, ok := tryLockRolloutCache(home, pass)
	if !ok {
		return false, false
	}
	defer c.mu.Unlock()
	return c.relistedAt.After(at), true
}

// keepHit records the paths found for id as of the look, under the same settled and relist safeguards as a miss. A hit
// and a miss for one id cannot coexist. The older-day listing lets a hit go when it names a rollout of the thread the
// hit does not have (a continuation in another old day directory), and the next lookup reads the current set once.
func (l *missLook) keepHit(id string, paths []string) {
	if !l.settled() {
		return
	}
	dirs := rolloutMatchDirs(paths)
	dirStamp := dirStamps(dirs)
	if slices.Max(dirStamp) >= l.at.Add(-time.Second).UnixNano() {
		return // a segment was added or moved in the old directory just now: the next look checks it again
	}
	c, ok := tryLockRolloutCache(l.home, l.pass)
	if !ok {
		return
	}
	defer c.mu.Unlock()
	if c.relistedAt.After(l.at) {
		return
	}
	if c.hits == nil {
		c.hits = map[string]rolloutHit{}
	}
	delete(c.misses, id)
	c.hits[id] = rolloutHit{paths: slices.Clone(paths), dirs: dirs, stamp: slices.Clone(l.stamp), dirStamp: dirStamp}
}

func rolloutMatchDirs(paths []string) []string {
	dirs := make([]string, 0, len(paths))
	for _, path := range paths {
		if dir := filepath.Dir(path); !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// keepMiss records that id has no rollout as of the look, unless it is not settled, or a listing of every directory
// began after it: that listing knows more, and may just have let this miss go for a rollout written meanwhile into an
// older day. The next look decides then.
func (l *missLook) keepMiss(id string) {
	if !l.settled() {
		return
	}
	c, ok := tryLockRolloutCache(l.home, l.pass)
	if !ok {
		return
	}
	defer c.mu.Unlock()
	if c.relistedAt.After(l.at) {
		return
	}
	if c.misses == nil {
		c.misses = map[string]rolloutMiss{}
	}
	delete(c.hits, id)
	asked := l.at
	if m, ok := c.misses[id]; ok && m.asked.After(asked) {
		asked = m.asked
	}
	c.misses[id] = rolloutMiss{stamp: l.stamp, asked: asked}
}

// recentNames are the rollout names in the look's directories, "\n" after each: listed once per look, and kept for
// later looks while the directories stay as they were when the look was settled. A name with "-<id>" in it may be the
// thread's rollout, which only rolloutPath's glob and the rollout's header tell; a name without it is not.
func (l *missLook) recentNames() string {
	if l.names != nil {
		return *l.names
	}
	c, locked := tryLockRolloutCache(l.home, l.pass)
	if !locked {
		return ""
	}
	names, ok := c.recentNames, c.recentStamp != nil && slices.Equal(c.recentStamp, l.stamp)
	c.mu.Unlock()
	if !ok {
		var paths []string
		for _, dir := range l.dirs {
			found, _ := rolloutGlob(filepath.Join(dir, "rollout-*.jsonl*"))
			paths = append(paths, found...)
		}
		names = rolloutNames(paths)
		// Kept only when no directory changed while they were listed: a rollout moved between two of them meanwhile
		// may be in neither list.
		if l.settled() && slices.Equal(dirStamps(l.dirs), l.stamp) {
			if c, locked := tryLockRolloutCache(l.home, l.pass); locked {
				c.recentNames, c.recentStamp = names, l.stamp
				c.mu.Unlock()
			}
		}
	}
	l.names = &names
	return names
}

func rolloutNames(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		b.WriteString(filepath.Base(p))
		b.WriteByte('\n')
	}
	return b.String()
}

// relistMisses checks the misses against every rollout on disk (all, listed after look was taken). A thread whose id
// is in no name has no rollout anywhere: its miss holds, from now on for look's stamp when look is settled. One whose
// id is in a name is let go, to be globbed (and the rollout's header read) at its next look, and so is one not asked
// about for missKeptFor. A hit is let go when this listing names a rollout of its thread it does not have (a
// continuation in another old day directory, a copy), and kept otherwise (relistHits). Into new maps: deleting from a
// map never gives its memory back.
func (c *rolloutCache) relistMisses(all []string, look *missLook) {
	c.relistedAt = look.at
	c.relistHits(all)
	if len(c.misses) == 0 {
		return
	}
	names := rolloutNames(all)
	settled := look.settled()
	kept := make(map[string]rolloutMiss, len(c.misses))
	for id, m := range c.misses {
		switch {
		case look.at.Sub(m.asked) > missKeptFor || strings.Contains(names, "-"+id):
		case settled:
			kept[id] = rolloutMiss{stamp: look.stamp, asked: m.asked}
		default:
			kept[id] = m
		}
	}
	c.misses = kept
}

// relistHits keeps the hits whose thread the listing (all) names in exactly the paths the hit has. Threads whose
// rollout Codex compressed (.jsonl.zst, #17768) are hits outside the window, hundreds of them on a long-used computer;
// letting every hit go each minute globbed each of them across all of history again (#17809), as the misses did before
// relistMisses kept them (528 threads, 14 s of CPU every minute).
func (c *rolloutCache) relistHits(all []string) {
	if len(c.hits) == 0 {
		return
	}
	listed := make(map[string][]string, len(c.hits))
	for _, p := range all {
		if id := rolloutID(p); id != "" {
			if _, ok := c.hits[id]; ok {
				listed[id] = append(listed[id], p)
			}
		}
	}
	kept := make(map[string]rolloutHit, len(c.hits))
	for id, h := range c.hits {
		paths := listed[id]
		if len(paths) == len(h.paths) && !slices.ContainsFunc(paths, func(p string) bool { return !slices.Contains(h.paths, p) }) {
			kept[id] = h
		}
	}
	c.hits = kept
}

// missDirs are the directories a new rollout appears in: the day directories of the window, where a thread's rollout
// is created, and archived_sessions, where an archived one moves.
func missDirs(home string, now time.Time) []string {
	days := int(codexWindow / (24 * time.Hour))
	out := make([]string, 0, days+2)
	for d := 0; d <= days; d++ {
		day := now.AddDate(0, 0, -d)
		out = append(out, filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02")))
	}
	return append(out, filepath.Join(home, "archived_sessions"))
}

// missStamp is when the directories a new rollout appears in (missDirs) were last changed (0: not there). A file
// created or moved into one changes its mtime.
func missStamp(home string) []int64 { return dirStamps(missDirs(home, time.Now())) }

func dirStamps(dirs []string) []int64 {
	out := make([]int64, len(dirs))
	for i, dir := range dirs {
		out[i] = dirStamp(dir)
	}
	return out
}

func dirStamp(dir string) int64 {
	st, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
}

// codexSession is the last fallback for other hosts: the newest unclaimed conversation in the pane's directory.
func (n *Node) codexSession(a host.Agent, taken map[string]host.Agent) string {
	if strings.LastIndex(a.Title, " | ") > 0 {
		return "" // a named thread could not be identified: never substitute another conversation from its directory
	}
	cwd := a.ForegroundCwd
	if cwd == "" {
		cwd = a.Cwd
	}
	home := n.codexPaneHome(a.PaneID)
	for _, r := range rollouts(home) {
		id := n.codexPublicID(home, r.id)
		if _, used := taken[id]; !used && !r.sub && r.cwd != "" && (r.cwd == cwd || r.cwd == a.Cwd) {
			return id
		}
	}
	return ""
}

// codexTitleName is the thread's name in a Codex pane's title ("<name> | <directory>"; either part may be cut short
// with "..."), and a test for a name in the index that it could be. ok is false when the title carries no name.
func codexTitleName(title string) (name string, match func(string) bool, ok bool) {
	i := strings.LastIndex(title, " | ")
	if i <= 0 {
		return "", nil, false
	}
	name = strings.TrimSpace(title[:i])
	// Codex adds this state prefix while a question is open; it is not part of the thread's name.
	name = strings.TrimPrefix(name, "[ . ] Action Required | ")
	name = strings.TrimPrefix(name, "[ ! ] Action Required | ")
	cut := strings.TrimSuffix(strings.TrimSuffix(name, "..."), "…")
	return name, func(t string) bool { return t == name || (cut != name && strings.HasPrefix(t, cut)) }, true
}

// codexByTitle finds the thread a Codex pane shows by the name in its title (codexTitleName). Of several threads with
// that name it keeps prefer (what the host or a start said). A herdr pane with an ambiguous title waits for stronger
// evidence. claimed prevents keeping a stale id when a uniquely matching thread is already taken.
func (n *Node) codexByTitle(home string, a host.Agent, prefer string, taken map[string]host.Agent) (bestID string, claimed bool) {
	_, match, ok := codexTitleName(a.Title)
	if !ok {
		return "", false
	}
	var best string
	var bestAt time.Time
	matches := 0
	for _, id := range threadsNamed(home, match) {
		p := rolloutPath(home, id)
		if p != "" && metaOf(home, p).sub {
			continue // a pane shows its conversation, never a subagent's thread
		}
		matches++
		public := n.codexPublicID(home, id)
		if _, used := taken[public]; used {
			claimed = true
			continue
		}
		if id == prefer || public == prefer {
			return id, claimed
		}
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && (best == "" || st.ModTime().After(bestAt)) {
			best, bestAt = id, st.ModTime()
		}
	}
	kind := host.ParsePane(a.PaneID).Kind
	if matches > 1 && (kind == host.Herdr || kind == host.HerdrCore) {
		return "", false // an ambiguous name neither identifies a thread nor contradicts a reported id
	}
	return best, claimed
}

// threadTitle is the name the agent gave the thread (<home>/session_index.jsonl, last entry wins); re-read on change.
func threadTitle(home, id string) string {
	title, _ := threadTitleIn(home, id, nil)
	return title
}

func threadTitleIn(home, id string, pass *codexListPass) (string, bool) {
	c, ok := tryLockRolloutCache(home, pass)
	if !ok {
		return "", false
	}
	defer c.mu.Unlock()
	return c.threadTitles(home)[id], true
}

// threadFormerlyNamed says whether session_index.jsonl gave thread id a name that passes match before its latest one:
// the thread was renamed, and a title that still shows the earlier name is that same thread's.
func threadFormerlyNamed(home, id string, match func(string) bool) bool {
	c := rolloutCacheFor(home)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.threadTitles(home) == nil {
		return false // no index (any more): what it said before is no evidence now
	}
	for _, t := range c.former[id] {
		if match(t) {
			return true
		}
	}
	return false
}

// threadsNamed lists the threads whose name passes match.
func threadsNamed(home string, match func(string) bool) []string {
	c := rolloutCacheFor(home)
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for id, t := range c.threadTitles(home) {
		if match(t) {
			out = append(out, id)
		}
	}
	return out
}

// threadTitles reads a home's session_index.jsonl when it changed; the caller holds the home cache lock.
func (c *rolloutCache) threadTitles(home string) map[string]string {
	path := filepath.Join(home, "session_index.jsonl")
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if c.titles == nil || st.ModTime().After(c.titlesAt) {
		titles, former := map[string]string{}, map[string][]string{}
		if f, err := os.Open(path); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				var e struct {
					ID   string `json:"id"`
					Name string `json:"thread_name"`
				}
				if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && strings.TrimSpace(e.Name) != "" {
					name := strings.TrimSpace(e.Name)
					if last, ok := titles[e.ID]; ok && last != name {
						former[e.ID] = append(former[e.ID], last)
					}
					titles[e.ID] = name
				}
			}
			f.Close()
		}
		c.titles, c.former, c.titlesAt = titles, former, st.ModTime()
	}
	return c.titles
}
