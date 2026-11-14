package kb

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/text/unicode/norm"
)

// Searching for the app (docs/assistant/knowledge.md "Search"): results are entries, not lines. Every line containing all the
// words is found (archived messages newest day first), grouped by the entry it belongs to, the entries ranked
// (name matches, then importance, relevance, how much matched, recency), a few lines kept for each. Opening one, Matches
// gives every place in that entry with a few messages around each, and Day the whole day a message is in.

// Found is one line of an entry that matched.
type Found struct {
	Path string `json:"path"` // relative to the data dir
	Line string `json:"line"`
	// N is the line's number in its file (1-based): messages are found again by it on the day page.
	N int `json:"n"`
}

// Result is one entry found by a search: the entry as ranked lists show it and its first lines.
type Result struct {
	Entry Entry   `json:"entry"`
	Count int     `json:"count"` // lines matched in all, at most foundCap
	Hits  []Found `json:"hits"`
	// InName: the name itself matched (those come first).
	InName bool `json:"in_name,omitempty"`
}

const (
	hitsPerResult = 3
	foundCap      = 99
)

// fold is the form text and queries are compared in: lowercased, then composed (NFC), so "café" typed precomposed
// finds the decomposed "cafe\u0301" macOS often writes, and the other way round. Already composed text (nearly all of
// the archive) passes norm's quick check without being copied.
func fold(s string) string { return norm.NFC.String(strings.ToLower(s)) }

// words is the query folded and split; a file without all of them is skipped whole.
func words(query string) []string { return strings.Fields(fold(query)) }

// hasAll is the whole-file check: most files have none of the words, and one pass over the file is much cheaper than
// a match per line (the archive is ~400k lines; everything is read, the oldest days too).
func hasAll(lowered string, ws []string) bool {
	for _, w := range ws {
		if !strings.Contains(lowered, w) {
			return false
		}
	}
	return true
}

// matcher returns whether a line has every word of the query (case- and normalization-insensitive, fold); nil for an
// empty query.
func matcher(query string) func(string) bool {
	words := words(query)
	if len(words) == 0 {
		return nil
	}
	return func(l string) bool {
		l = fold(l)
		for _, w := range words {
			if !strings.Contains(l, w) {
				return false
			}
		}
		return true
	}
}

// searchable reports whether a file of the data dir (path relative to it) holds text a search may return. Bookkeeping
// is not: a person's hashed identity hints (handles.json, handles.go), a connector's cursor (cursors/<source>.json),
// history imports' progress (history/, history.go), the selection's bookkeeping (select/, a chat's retention.json),
// the Moments verdicts of a project (moments-judged.json, moments_link.go) and recording undo snapshots mean nothing
// to a model, and the hints are kept out of every tool and prompt. Nor are a topic's points.json and a source's
// distill mark (.topics.json, topics.go). A meeting's live log (live.json, live.go) is never
// searched: other people's words said while Persona attended, and the questions asked of it. Nor is photos.json
// (recording_photos.go: digests, deletions; photos.md has the photos' words), and the photos themselves are in
// attachments/, which the walk never enters (nor a hub-mode meeting's audio, attachments/audio/). Nor is
// hub_segments.json (hub_segments.go): transcript.md, rendered from the transcript of record, is the searched text.
// Nor is live ingestion's bookkeeping in a chat (ingest.go): the receipts
// (.receipts.json: the source, every message's id and time, one long line), the intent of a write in progress
// (.ingest.json) and the zone the days are in (messages/.zone, zone.go). Profile files are read before any day, so a
// query of digits, a date or the source's name matched every chat's receipts and filled the limit (#5991). Nor is the
// search index itself (.search/, search_index.go).
func searchable(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case parts[0] == "cursors" || parts[0] == "history" || parts[0] == selectDir || parts[0] == trashDirName || parts[0] == SearchIndexDir:
		return false
	case len(parts) == 3 && parts[0] == KindChat && (parts[2] == retentionFile || parts[2] == ingestReceipts || parts[2] == ingestJournal):
		return false
	case len(parts) == 4 && parts[0] == KindChat && parts[2] == "messages" && parts[3] == zoneFile:
		return false
	case len(parts) == 3 && parts[0] == KindRecording && (parts[2] == recordingJournal || parts[2] == "minutes.json"):
		return false // minutes.md has the minutes' text
	case len(parts) == 3 && parts[0] == KindRecording && (parts[2] == recapFile || parts[2] == recapNotesFile):
		return false // recap.md has the recap's text; the notes are the model's, unchecked (day_recap.go)
	case len(parts) >= 3 && parts[0] == KindRecording && strings.HasPrefix(parts[2], liveFile):
		return false // live.json, and its temporary file while it is replaced
	case len(parts) >= 3 && parts[0] == KindRecording && strings.HasPrefix(parts[2], meetingFile):
		return false // meeting.json (meeting.go): the manager's bookkeeping of the room and the mark
	case len(parts) >= 3 && parts[0] == KindRecording && strings.HasPrefix(parts[2], photosFile):
		return false // photos.json (recording_photos.go): digests and deletions; photos.md has the photos' words
	case len(parts) >= 3 && parts[0] == KindRecording && strings.HasPrefix(parts[2], hubSegmentsFile):
		return false // hub_segments.json and its temporary file: transcript.md has the transcript of record
	case len(parts) == 3 && parts[0] == KindPerson && parts[2] == "handles.json":
		return false
	case len(parts) == 3 && parts[0] == KindPerson && parts[2] == profilesFile:
		return false // summary.md shows the profiles, each line labelled with its part (profile.go)
	case len(parts) == 3 && parts[0] == KindProject && parts[2] == "moments-judged.json":
		return false
	case len(parts) == 3 && parts[0] == KindTopic && parts[2] == topicPointsFile:
		return false // summary.md has the points' text; the candidates waiting in it are the model's, unchecked
	case len(parts) == 3 && parts[2] == topicMarkFile:
		return false
	}
	return true
}

// entryOf maps a path relative to the data dir to the entry it belongs to: chats/<id>/…, docs/<id>.json.
func entryOf(rel string) (kind, id string, ok bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	switch parts[0] {
	case KindChat, KindPerson, KindProject, KindRecording, KindTopic:
		if len(parts) < 3 {
			return "", "", false
		}
		return parts[0], parts[1], true
	case KindDoc, KindMeeting, KindEvent:
		if id, ok := strings.CutSuffix(parts[1], ".json"); ok && len(parts) == 2 {
			return parts[0], id, true
		}
	}
	return "", "", false
}

// shown reports whether Matches reads the file at rel (relative to the data dir) for the entry kind/id it belongs to:
// Find only counts lines from those, so every result it gives can be located once opened. Other files under an entry
// (a project's moments-judged.json, a person's handles.json) are bookkeeping, not something to show.
func shown(kind, rel string) bool {
	// rest is what follows <kind>/<id>/ (entryOf has checked there is one).
	_, rest, _ := strings.Cut(filepath.ToSlash(rel), "/")
	_, rest, _ = strings.Cut(rest, "/")
	switch kind {
	case KindChat, KindPerson, KindProject:
		if rest == "summary.md" || rest == "log.md" || rest == "meta.json" {
			return true
		}
		// Store.Days: the .md files directly in a chat's messages/.
		day, ok := strings.CutPrefix(rest, "messages/")
		return kind == KindChat && ok && strings.HasSuffix(day, ".md") && !strings.Contains(day, "/")
	case KindRecording:
		return rest == "summary.md" || rest == "minutes.md" || rest == recapMarkdown || rest == "transcript.md" || rest == photosText || rest == "meta.json"
	case KindTopic:
		return rest == "summary.md" || rest == "meta.json"
	case KindDoc, KindMeeting, KindEvent:
		return true // entryOf already keeps only <kind>/<id>.json
	}
	return false
}

// Find searches every entry and returns at most limit, ranked; none when limit <= 0.
func (e *Engine) Find(query string, limit int) []Result {
	match := matcher(query)
	if match == nil || limit <= 0 {
		return nil
	}
	recording := e.Store.recordingSearchView()
	ws := words(query)
	// A file whose text folds to itself (nearly every one) has lines that do too, so they are matched as written
	// instead of folded one by one: lowercasing works rune by rune, and NFC neither composes nor reorders across a
	// line break or a space (no canonical decomposition holds one), so trimming a line does not change how it folds.
	// The two spaces NFC does change (U+2000, U+2001) cannot be in such a file: they would have changed its fold.
	matchFolded := func(t string) bool { return hasAll(t, ws) }
	// read is what one file gives: its first lines that match and how many do.
	read := func(c *findText, rel string) (found []Found, count int) {
		if !hasAll(c.lower, ws) {
			return nil, 0
		}
		m := match
		if c.same {
			m = matchFolded
		}
		return fileFound(c.raw, rel, m)
	}
	// Every file is looked at, in parallel: one stat each, which is all most of them cost. The search index
	// (search_index.go) rules out the files that cannot hold every word, as long as they are as it read them; the
	// others are read, through findCache (the app searches as the user types, so a file unchanged since the last
	// search is not read again). Without an index, or for the files it does not have, that is every file.
	all := e.files().all
	filter := e.searchFilter(ws)
	gen := e.find.begin()
	const (
		findSkip = iota // not a file of an entry, gone, or ruled out by the index
		findRead        // to read
		findDone        // read already: a recording's uploaded files, which are not read from disk (searchText)
	)
	state := make([]uint8, len(all))
	stats := make([]fileStat, len(all))
	type fileHits struct {
		found []Found // what fileFound keeps
		count int
	}
	var early sync.Map // by index: the fileHits of the findDone files
	workers := max(1, min(8, runtime.NumCPU()))
	// unread: a file that is there was not read, so its cached text was not seen by this search.
	var unread atomic.Bool
	each(len(all), workers, func(i int) {
		f := all[i]
		if f.kind == "" { // a file Matches does not read (shown)
			return
		}
		if recordingViewFile(f.rel) {
			c, st, ok := e.searchText(f.path, gen, recording)
			stats[i], state[i] = st, findDone
			if ok {
				var fh fileHits
				if fh.found, fh.count = read(c, f.rel); fh.count > 0 {
					early.Store(i, fh)
				}
			}
			return
		}
		st := statFile(f.path)
		if !st.ok {
			return
		}
		stats[i] = fileStat{path: f.path, mod: st.mod, size: st.size}
		if filter != nil && filter.skip(f.rel, st) {
			unread.Store(true)
			return
		}
		state[i] = findRead
	})
	// The entries the lists show depend only on the JSON files (meta.json, docs/<id>.json): kept while none changed.
	var metas []fileStat
	for i, f := range all {
		if f.kind != "" && strings.HasSuffix(f.path, ".json") {
			metas = append(metas, stats[i])
		}
	}
	known := e.find.entries(metas, func() map[string]Entry {
		known := map[string]Entry{}
		for _, k := range []string{KindProject, KindChat, KindPerson, KindDoc, KindMeeting, KindEvent, KindTopic} {
			for _, en := range e.List(k, "") {
				known[k+"/"+en.ID] = en
			}
		}
		for id, files := range recording {
			var r Recording
			if raw := files["meta.json"]; raw != nil && json.Unmarshal(raw, &r) == nil {
				known[KindRecording+"/"+id] = recordingEntry(&r)
			}
		}
		return known
	})
	// The files to read, by entry and in file order, so the lines kept for an entry are its profile's first, then
	// the newest days'. Files of an entry the lists do not show (a doc without a title yet) are not read: there
	// would be nothing to open.
	type entryFiles struct {
		key   string
		res   *Result
		files []int // indices in all
		next  int   // files[:next] are read
		batch int
	}
	type entryKey struct{ kind, id string }
	groups := map[entryKey]*entryFiles{}
	var order []*entryFiles
	for i, f := range all {
		if state[i] == findSkip {
			continue
		}
		g := groups[entryKey{f.kind, f.id}]
		if g == nil {
			g = &entryFiles{key: f.kind + "/" + f.id, batch: 16}
			groups[entryKey{f.kind, f.id}] = g
			if _, ok := known[g.key]; ok {
				order = append(order, g)
			}
		}
		g.files = append(g.files, i)
	}
	by := map[string]*Result{}
	// add counts one file's lines for its entry, and reports whether the entry is settled: at foundCap lines with
	// every line it shows, nothing a later file has changes it.
	add := func(g *entryFiles, fh fileHits) bool {
		if fh.count == 0 {
			return false
		}
		if g.res == nil {
			g.res = &Result{Entry: known[g.key]}
			by[g.key] = g.res
		}
		r := g.res
		r.Count = min(foundCap, r.Count+fh.count)
		for _, f := range fh.found {
			if len(r.Hits) < hitsPerResult && !hasLine(r.Hits, f.Line) {
				r.Hits = append(r.Hits, f)
			}
		}
		return r.Count >= foundCap && len(r.Hits) == hitsPerResult
	}
	// Read in rounds: of every entry not settled yet its next files, a few at first and more each round, so an
	// entry with a line on every day costs its newest days, and one with none is read through in a few rounds.
	type task struct {
		g *entryFiles
		i int
	}
	var tasks []task
	var hits []fileHits
	for len(order) > 0 {
		tasks = tasks[:0]
		for _, g := range order {
			for _, i := range g.files[g.next:min(len(g.files), g.next+g.batch)] {
				tasks = append(tasks, task{g, i})
			}
		}
		hits = append(hits[:0], make([]fileHits, len(tasks))...)
		each(len(tasks), workers, func(n int) {
			i := tasks[n].i
			if state[i] == findDone {
				if fh, ok := early.Load(i); ok {
					hits[n] = fh.(fileHits)
				}
				return
			}
			if c, ok := e.find.textAt(all[i].path, gen, stats[i]); ok {
				hits[n].found, hits[n].count = read(c, all[i].rel)
			}
		})
		n := 0
		left := order[:0]
		for _, g := range order {
			end := min(len(g.files), g.next+g.batch)
			settled := false
			for ; g.next < end; g.next, n = g.next+1, n+1 {
				settled = settled || add(g, hits[n])
			}
			if g.batch = min(4*g.batch, 4096); !settled && g.next < len(g.files) {
				left = append(left, g)
			} else if g.next < len(g.files) {
				unread.Store(true)
			}
		}
		order = left
	}
	if unread.Load() {
		// The files not read (ruled out by the index, or of an entry settled before them) are still as the cache
		// has them when their stat is the cached one: prune leaves their texts, and drops those of files gone.
		e.find.seen(gen, func(yield func(fileStat) bool) {
			for i := range all {
				if stats[i].path != "" && !yield(stats[i]) {
					return
				}
			}
		})
	}
	e.find.prune(gen)
	// Names: an entry whose name matches is found even when no file line does.
	for key, en := range known {
		if match(en.Name) {
			if by[key] == nil {
				by[key] = &Result{Entry: en}
			}
			by[key].InName = true
		}
	}
	out := make([]Result, 0, len(by))
	for _, r := range by {
		r.Hits = sortHits(r.Hits)
		out = append(out, *r)
	}
	imp := func(s string) int {
		if s == "" {
			return 4
		}
		return int(s[1] - '0')
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.InName != b.InName {
			return a.InName
		}
		if imp(a.Entry.Importance) != imp(b.Entry.Importance) {
			return imp(a.Entry.Importance) < imp(b.Entry.Importance)
		}
		if a.Entry.Relevance != b.Entry.Relevance {
			return a.Entry.Relevance > b.Entry.Relevance
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.Entry.Updated.Equal(b.Entry.Updated) {
			return a.Entry.Updated.After(b.Entry.Updated)
		}
		return a.Entry.Kind+a.Entry.ID < b.Entry.Kind+b.Entry.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// files lists the profile files (summaries, metadata, timelines) and the message days, newest day first. What it
// returns is shared with findCache: callers only read it.
func (e *Engine) files() *dirWalk {
	c := &e.find
	c.walkMu.Lock()
	defer c.walkMu.Unlock()
	return e.walkLocked()
}

// walkLocked is files with walkMu held. Unchanged directories cost a stat each: their listings are kept with every
// child's paths and kind, and the lists are gathered into scratch slices, copied only when something changed.
func (e *Engine) walkLocked() *dirWalk {
	c := &e.find
	c.walkGen++
	changed := false
	files, days := c.scratch[0][:0], c.scratch[1][:0]
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		ents, fresh := c.listDir(dir, rel)
		changed = changed || fresh
		for i := range ents {
			switch d := &ents[i]; {
			case d.dir:
				// Attachments hold no text; history/, select/ and the search index are bookkeeping, not searched (see
				// searchable).
				if d.name != "attachments" && d.rel != "history" && d.rel != selectDir && d.rel != SearchIndexDir {
					walk(d.path, d.rel)
				}
			case d.day:
				days = append(days, d)
			case d.file:
				files = append(files, d)
			}
		}
	}
	walk(e.Store.Dir, "")
	c.scratch = [2][]*dirEnt{files, days}
	for d, l := range c.dirs {
		if l.gen != c.walkGen {
			delete(c.dirs, d)
			changed = true
		}
	}
	if !changed && c.walked {
		return c.walk
	}
	w := &dirWalk{files: slices.Clone(files), walkDays: slices.Clone(days), days: slices.Clone(days)}
	sort.SliceStable(w.days, func(i, j int) bool { return w.days[i].name > w.days[j].name })
	w.all = append(slices.Clip(w.files), w.days...)
	c.walk, c.walked = w, !changed
	return w
}

// searchOrder is the order Search reads the data dir's files in: the profile files as walked, the message days
// newest first and the Moments posts (.md) newest first.
type searchOrder struct {
	files, days, moments []*dirEnt
}

// searchFiles returns the data dir's files in Search's order, worked out once per change of the walk. The days are
// in Search's own order (sort.Slice of the walk's, which can put same-named days of different chats the other way
// round from Find's). Shared: callers only read it.
func (e *Engine) searchFiles() *searchOrder {
	c := &e.find
	c.walkMu.Lock()
	defer c.walkMu.Unlock()
	w := e.walkLocked()
	if w.search != nil {
		return w.search
	}
	o := &searchOrder{}
	for i, l := range [][]*dirEnt{w.files, w.walkDays} {
		for _, d := range l {
			switch {
			case d.moment: // read after everything else, whatever else it would be
				if strings.HasSuffix(d.name, ".md") {
					o.moments = append(o.moments, d)
				}
			case i == 0:
				o.files = append(o.files, d)
			default:
				o.days = append(o.days, d)
			}
		}
	}
	sort.Slice(o.days, func(i, j int) bool { return o.days[i].name > o.days[j].name })
	sort.Slice(o.moments, func(i, j int) bool { return o.moments[i].path > o.moments[j].path }) // newer post ids first
	w.search = o
	return o
}

// sortHits puts what says most first: the profile, the timeline, messages, then metadata (a tag, a member's name).
func sortHits(h []Found) []Found {
	rank := func(f Found) int {
		switch {
		case strings.HasSuffix(f.Path, "/summary.md"), strings.HasSuffix(f.Path, "/minutes.md"), strings.HasSuffix(f.Path, "/"+recapMarkdown):
			return 0
		case strings.HasSuffix(f.Path, "/log.md"):
			return 1
		case strings.Contains(f.Path, "/messages/"), strings.HasSuffix(f.Path, "/transcript.md"), strings.HasSuffix(f.Path, "/"+photosText):
			return 2
		}
		return 3
	}
	sort.SliceStable(h, func(i, j int) bool { return rank(h[i]) < rank(h[j]) })
	return h
}

func hasLine(h []Found, line string) bool {
	for _, f := range h {
		if f.Line == line {
			return true
		}
	}
	return false
}

// fileFound is what Find keeps of one file (rel, relative to the data dir): its first hitsPerResult distinct lines
// that match, clipped as results show them and numbered as in the file, and how many lines match, at most foundCap.
// No other line of the file can be shown: an entry already holding h lines takes at most hitsPerResult-h more, and
// at most h of the file's distinct lines repeat those.
func fileFound(raw, rel string, match func(string) bool) (found []Found, count int) {
	path := filepath.ToSlash(rel)
	for n, rest, more := 1, raw, true; more; n++ {
		var l string
		l, rest, more = strings.Cut(rest, "\n")
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "# ") || !match(t) {
			continue
		}
		count++
		if len(found) < hitsPerResult {
			if line := clip(t, 300); !hasLine(found, line) {
				found = append(found, Found{Path: path, Line: line, N: n})
			}
		}
		if count >= foundCap && len(found) == hitsPerResult {
			break
		}
	}
	return found, min(count, foundCap)
}
