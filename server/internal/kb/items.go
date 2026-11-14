package kb

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// ItemSource lists documents, meetings and calendar events (Feishu through the Lark extension).
type ItemSource interface {
	Items(ctx context.Context) ([]kbwire.Item, error)
	// DocTitle looks up a document's title by its link ("" when it cannot).
	DocTitle(ctx context.Context, url string) string
}

// CollectItems stores new meetings and events and fills in titles of documents seen in chats. With a selection
// (Engine.Selection), a document is stored only when it selects it (Selection.DocSelected): one it does not select is
// not written, and one already stored is left as it is (the selection stops writes; only Prune deletes, by days).
func (e *Engine) CollectItems(ctx context.Context, src ItemSource, sources ...string) error {
	if e.Store.ReadOnly() {
		return ErrReadOnly
	}
	source := ""
	if len(sources) > 0 {
		source = sources[0]
	}
	items, err := src.Items(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sel, ret := e.selection(), e.Retention()
	for _, w := range items {
		it := ItemFromWire(w)
		it.Source = source
		if it.Kind == KindDoc && sel != nil && !sel.DocSelected(ret, DocFacts{Source: source, ID: it.ID, Signals: it.Signals}) {
			continue
		}
		old, ok := e.Store.Item(it.Kind, it.ID)
		if ok {
			// An ID alone cannot identify an owner: legacy or colliding items stay
			// with their original source unless a stored chat proves ownership.
			owner := e.itemSource(old)
			if owner != source {
				continue
			}
			if old.Source == it.Source && old.Title == it.Title && old.At.Equal(it.At) {
				if !slices.Equal(kbwire.CanonicalSignals(old.Signals), it.Signals) { // canonical: a reordering is no change
					// Only why it concerns the owner moved: keep them as the source says now, without tagging it again.
					old.Signals = it.Signals
					if err := e.Store.PutItem(old); err != nil {
						return err
					}
				}
				continue
			}
			carryOver(&it, old)
		} else {
			it.Seen = now
			if it.Kind == KindDoc && sel != nil {
				// Past its days already (an old document a source still lists): not written, as Prune would delete it.
				if days, ok := sel.DocDays(DocFacts{Source: source, ID: it.ID, Signals: it.Signals}); ok && docExpired(&it, now, days) {
					continue
				}
			}
		}
		if err := e.Store.PutItem(&it); err != nil {
			return err
		}
	}
	n := 0
	for _, it := range e.Store.Items(KindDoc) {
		if it.Title != "" || n >= 10 || ctx.Err() != nil || e.itemSource(it) != source {
			continue
		}
		if x := sel.Item(source, ItemDoc, it.ID); x != nil && !x.Read { // the user keeps it from being read
			continue
		}
		it.Source = source
		n++
		if t := src.DocTitle(ctx, it.URL); t != "" {
			it.Title = t
		} else {
			it.Title = it.URL
		}
		_ = e.Store.PutItem(it)
	}
	return nil
}

// itemSource recovers legacy attribution only from an existing source chat.
func (e *Engine) itemSource(it *Item) string {
	if it.Source != "" {
		return it.Source
	}
	if c, found := e.Store.Chat(it.Where); found {
		if c.Connector != "" {
			return c.Connector
		}
		return c.Source
	}
	return ""
}

type itemTags struct {
	Items []struct {
		ID         string     `json:"id"`
		Importance string     `json:"importance"`
		Relevance  modelScore `json:"relevance"`
		Tags       []string   `json:"tags"`
		Project    string     `json:"project"`
		Note       string     `json:"note"`
	} `json:"items"`
}

// tagItemsBatch is how many items one tagging call reads.
const tagItemsBatch = 40

// TagItems tags documents, meetings and events that are new or changed, tagItemsBatch per call and at most max calls
// per run; more reports that untagged ones are left for the next round: the cap was reached, the connection is paused or
// the run ended (not when no model is set up: the next collection asks again). The items of a batch whose call failed
// wait (retryWait, per item) and the next batch goes on, so a batch the model keeps failing on costs a call now and then
// rather than every run, and does not hold back the rest; an item changed since (what the model reads of it) is tried
// at once. A paused connection or no model returns its error at once without counting.
func (e *Engine) TagItems(ctx context.Context, max int) (more bool, err error) {
	if e.Model == nil {
		return false, nil
	}
	now := time.Now()
	var todo []*Item
	due := map[string]bool{}
	for _, k := range []string{KindEvent, KindMeeting, KindDoc} {
		for _, it := range e.Store.Items(k) {
			if it.TaggedAt.IsZero() && it.Title != "" && !it.Locked {
				key := it.Kind + "/" + it.ID
				due[key] = true
				if e.retries().items.waits(key, itemAbout(it), now) {
					continue
				}
				todo = append(todo, it)
			}
		}
	}
	e.retries().items.keep(due) // tagged, locked or gone in the meantime
	for calls := 0; len(todo) > 0 && ctx.Err() == nil; calls++ {
		if calls >= max {
			return true, nil
		}
		batch := todo[:min(tagItemsBatch, len(todo))]
		todo = todo[len(batch):]
		byID := map[string]*Item{}
		in := prompts.KBItemsInput{Projects: e.knownProjects(), Tags: e.TopTags(40)}
		for _, it := range batch {
			key := it.Kind + "/" + it.ID
			byID[key] = it
			var at time.Time
			if !it.At.IsZero() {
				at = it.At.In(e.Store.Zone())
			}
			where := it.Where
			if c, ok := e.Store.Chat(it.Where); ok {
				where = c.Name
			}
			in.Items = append(in.Items, prompts.KBItem{Pinned: it.Pinned, Key: key, Kind: it.Kind, Title: it.Title, At: at, Where: where})
		}
		var out itemTags
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := e.modelFor("kb.items").JSONMax(cctx, prompts.RenderKBItems(prompts.KBOwner{Owner: e.ownerName()}), prompts.RenderKBItemsInput(in), 6000, &out)
		cancel()
		if err != nil {
			if deferred(ctx, err) {
				return !ai.IsNotConfigured(err), err
			}
			var f projectRetry
			for _, it := range batch {
				f = e.retries().items.fail(it.Kind+"/"+it.ID, itemAbout(it))
			}
			e.logf("tag items: %v (%d item(s), failed %d time(s) in a row, next try in %s)", err, len(batch), f.failures, retryWait(f.failures))
			continue
		}
		for _, it := range batch {
			e.retries().items.clear(it.Kind + "/" + it.ID)
		}
		e.Store.applyMu.Lock()
		now := time.Now().UTC()
		for _, t := range out.Items {
			it := byID[t.ID]
			if it == nil {
				continue
			}
			if _, exists := e.Store.Item(it.Kind, it.ID); !exists {
				continue
			}
			applyTags(&it.Tagging, t.Importance, int(t.Relevance), t.Tags, now)
			it.Note = oneLine(t.Note)
			if p := e.FindProject(t.Project); p != nil {
				it.Projects = addUnique(it.Projects, p.Slug)
				key := it.Kind + "/" + SafeID(it.ID)
				n := len(p.Items)
				if p.Items = addUnique(p.Items, key); len(p.Items) != n {
					_ = e.Store.PutProject(p)
				}
			}
			_ = e.Store.PutItem(it)
			e.index(it.Kind, it.ID, it.Title, it.Tagging, it.Note, it.At)
		}
		e.Store.applyMu.Unlock()
		// Ones the model skipped: mark tagged anyway so they do not come back every run.
		for _, it := range batch {
			if it.TaggedAt.IsZero() {
				it.TaggedAt = now
				_ = e.Store.PutItem(it)
			}
		}
	}
	return len(todo) > 0, nil // the run ended with some not tried
}

// itemAbout is what the model reads of an item to tag it: an item changed since a failure is tried at once.
func itemAbout(it *Item) string {
	return it.Title + "\x00" + it.At.UTC().Format(time.RFC3339) + "\x00" + it.Where + "\x00" + it.Pinned
}

// --- reading ---

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	Entry = kbwire.Entry
	Hit   = kbwire.Hit
)

// Search finds lines containing every word of the query (case-insensitive) in summaries, metadata, timelines, the
// Markdown of the ExtraDirs and (when messages is set) the message archive; newest message days first. At most limit
// hits. A line skip says yes to is passed over (not a hit, and not counted against limit).
func (e *Engine) Search(query string, messages bool, limit int, skip ...func(line string) bool) []Hit {
	s := e.newSearcher(query, limit, true)
	if s == nil {
		return nil
	}
	s.skip = skip
	// The data dir's files come from Find's walk (searchFiles), kept while no directory changed: profile files, the
	// ExtraDirs, the message days newest first, then the Moments archive (moments.go), thousands of posts read after
	// everything else so they never crowd it out.
	o := e.searchFiles()
	s.scanAll(o.files)
	for _, dir := range e.ExtraDirs {
		if s.full() {
			break
		}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // .git and the like
			}
			if !d.IsDir() && strings.HasSuffix(p, ".md") {
				s.scan(p)
			}
			return nil
		})
	}
	if messages {
		s.scanAll(o.days)
	}
	s.scanAll(o.moments)
	return s.hits
}

// SearchKind is Search within one kind of entry (projects, chats, people, docs, meetings, events, recordings): the
// files under that kind's directory, by entry, those whose name has every word of the query first, then as List ranks
// them (the walk alone goes by id, and a recording's id is random, so "last week's" one could come after older ones);
// with messages, the chats' message days after them, newest first. Never the ExtraDirs or the Moments posts. Nil for
// any other kind. skip as for Search.
func (e *Engine) SearchKind(query, kind string, messages bool, limit int, skip ...func(line string) bool) []Hit {
	switch kind {
	case KindProject, KindChat, KindPerson, KindDoc, KindMeeting, KindEvent, KindRecording, KindTopic:
	default:
		return nil
	}
	s := e.newSearcher(query, limit, kind == KindRecording)
	if s == nil {
		return nil
	}
	s.skip = skip
	list := e.List(kind, "")
	if kind == KindRecording { // kb_list's order for recordings, newest first (List ranks them by the model's importance)
		start := map[string]time.Time{}
		for _, r := range e.Store.Recordings() {
			start[SafeID(r.ID)] = r.StartedAt
		}
		sort.SliceStable(list, func(i, j int) bool { return start[SafeID(list[i].ID)].After(start[SafeID(list[j].ID)]) })
	}
	rank := make(map[string]int, len(list))
	for i, en := range list {
		r := i
		if !s.match(en.Name) {
			r += len(list)
		}
		rank[SafeID(en.ID)] = r
	}
	o := e.searchFiles()
	type ranked struct {
		d    *dirEnt
		rank int
	}
	var files []ranked
	for _, d := range o.files {
		if !strings.HasPrefix(d.rel, kind+"/") {
			continue
		}
		r := 2 * len(list) // a file of no listed entry (a doc without a title yet): last
		if _, id, ok := entryOf(d.rel); ok {
			if i, ok := rank[id]; ok {
				r = i
			}
		}
		files = append(files, ranked{d, r})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].rank < files[j].rank })
	inOrder := make([]*dirEnt, len(files))
	for i, f := range files {
		inOrder[i] = f.d
	}
	s.scanAll(inOrder)
	if messages && kind == KindChat {
		// Every day is a chat's: o.days are the files under a messages/ directory, which only chats have.
		s.scanAll(o.days, func(d *dirEnt) bool { return strings.HasPrefix(d.rel, kind+"/") })
	}
	return s.hits
}

// searcher gathers the hits of Search and SearchKind: the lines with every word of the query, at most limit.
type searcher struct {
	e         *Engine
	match     func(string) bool
	ws        []string
	gen       uint64
	recording map[string]map[string][]byte
	limit     int
	hits      []Hit
	skip      []func(line string) bool // lines never to give (Search's skip)
	// filter is the search index asked this query (search_index.go); nil without one, and every file is read.
	filter *searchFilter
}

// newSearcher starts a search; nil when there is nothing to find. Files come through Find's cache (findCache.text): a
// file unchanged since an earlier search is one stat, not a read and a fold, and one without every word is skipped
// whole. A search stops at limit, so it leaves the pruning of files no longer there to Find, which sees them all; the
// cache is bounded either way (findCacheBytes). recordings: the search may read recordings' files, whose accepted
// text is read once for it (recordingSearchView).
func (e *Engine) newSearcher(query string, limit int, recordings bool) *searcher {
	match := matcher(query)
	if match == nil || limit <= 0 {
		return nil
	}
	s := &searcher{e: e, match: match, ws: words(query), limit: limit}
	if recordings {
		s.recording = e.Store.recordingSearchView()
	}
	s.filter = e.searchFilter(s.ws)
	s.gen = e.find.begin()
	return s
}

// scanAll scans the files of the data dir in l, in order, until the limit; only those keep says yes to, when it is
// given. With a search index the files it rules out are passed over without being read: a stretch of files at a
// time is looked at (a stat each, in parallel), a short one first and longer ones as the search goes on without
// filling the limit, then the files left of it are scanned in order. A query no file has costs the stats alone.
func (s *searcher) scanAll(l []*dirEnt, keep ...func(*dirEnt) bool) {
	want := func(d *dirEnt) bool { return len(keep) == 0 || keep[0](d) }
	if s.filter == nil {
		for _, d := range l {
			if s.full() {
				return
			}
			if want(d) {
				s.scan(d.path)
			}
		}
		return
	}
	workers := max(1, min(8, runtime.NumCPU()))
	var out []bool
	for n := 64; len(l) > 0 && !s.full(); n = min(2*n, 8192) {
		part := l[:min(n, len(l))]
		l = l[len(part):]
		out = append(out[:0], make([]bool, len(part))...)
		each(len(part), workers, func(i int) {
			d := part[i]
			if !want(d) {
				out[i] = true
				return
			}
			if recordingViewFile(d.rel) {
				return // not read from disk (searchText): the index does not have it
			}
			// A file that is gone is left to scan, which finds it so.
			st := statFile(d.path)
			out[i] = st.ok && s.filter.skip(d.rel, st)
		})
		for i, d := range part {
			if s.full() {
				return
			}
			if !out[i] {
				s.scan(d.path)
			}
		}
	}
}

func (s *searcher) full() bool { return len(s.hits) >= s.limit }

// scan adds the lines of file p that match, until the limit.
func (s *searcher) scan(p string) {
	if s.full() {
		return
	}
	t, _, ok := s.e.searchText(p, s.gen, s.recording)
	if !ok || !hasAll(t.lower, s.ws) {
		return
	}
	rel, err := filepath.Rel(s.e.Store.Dir, p)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = p
	}
	for _, l := range strings.Split(t.raw, "\n") {
		if s.full() {
			return
		}
		if s.match(l) && !s.skipped(l) {
			s.hits = append(s.hits, Hit{Path: rel, Line: clip(strings.TrimSpace(l), 300)})
		}
	}
}

func (s *searcher) skipped(l string) bool {
	for _, f := range s.skip {
		if f(l) {
			return true
		}
	}
	return false
}

// clip cuts s to its first n runes plus an ellipsis (textfmt.Clip).
var clip = textfmt.Clip

// Rank orders by importance, then relevance, then recency.
func Rank(list []Entry) {
	imp := func(s string) int {
		if s == "" {
			return 4
		}
		return int(s[1] - '0')
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if imp(a.Importance) != imp(b.Importance) {
			return imp(a.Importance) < imp(b.Importance)
		}
		if a.Relevance != b.Relevance {
			return a.Relevance > b.Relevance
		}
		return a.Updated.After(b.Updated)
	})
}

// List returns the entities of a kind as ranked entries; tag filters to those carrying it.
func (e *Engine) List(kind, tag string) []Entry {
	var out []Entry
	has := func(t []string) bool {
		if tag == "" {
			return true
		}
		for _, x := range t {
			if x == tag {
				return true
			}
		}
		return false
	}
	switch kind {
	case KindProject:
		for _, p := range e.Store.Projects() {
			if has(p.Tags) && p.Live() {
				out = append(out, Entry{Kind: kind, ID: p.Slug, Name: p.Name, OneLine: p.OneLine, Importance: p.Importance,
					Relevance: p.Relevance, Tags: p.Tags, Updated: p.Updated, Extra: p.Kind + " · " + p.Status})
			}
		}
	case KindChat:
		for _, c := range e.Store.Chats() {
			if has(c.Tags) {
				out = append(out, Entry{Kind: kind, ID: c.ID, Name: c.Name, OneLine: c.OneLine, Importance: c.Importance,
					Relevance: c.Relevance, Tags: c.Tags, Updated: c.LastAt, Extra: c.Kind, Source: c.Source})
			}
		}
	case KindPerson:
		for _, p := range e.Store.People() {
			if p.Status != "ignored" && has(p.Tags) {
				out = append(out, Entry{Kind: kind, ID: p.ID, Name: p.Name, OneLine: p.Note, Importance: p.Importance,
					Relevance: p.Relevance, Tags: p.Tags, Updated: p.LastSeen})
			}
		}
	case KindDoc, KindMeeting, KindEvent:
		for _, it := range e.Store.Items(kind) {
			if has(it.Tags) && it.Title != "" {
				out = append(out, Entry{Kind: kind, ID: it.ID, Name: it.Title, OneLine: it.Note, Importance: it.Importance,
					Relevance: it.Relevance, Tags: it.Tags, Updated: it.At, Extra: it.URL})
			}
		}
	case KindRecording:
		for _, r := range e.Store.Recordings() {
			if has(r.Tags) {
				out = append(out, recordingEntry(r))
			}
		}
	case KindTopic:
		return e.listTopics(has) // the biggest first: topics carry no importance of their own
	}
	Rank(out)
	return out
}

// recordingEntry is a recording as lists show it: Extra is the device it was made on.
func recordingEntry(r *Recording) Entry {
	return Entry{Kind: KindRecording, ID: r.ID, Name: r.Name, OneLine: r.OneLine, Importance: r.Importance,
		Relevance: r.Relevance, Tags: r.Tags, Updated: r.StartedAt, Extra: r.Device}
}

// Prompt is a short Markdown digest of the most important projects and chats, for the assistant's live snapshot.
func (e *Engine) Prompt(projects, chats int) string {
	var d prompts.KBDigest
	for _, p := range e.List(KindProject, "") {
		if len(d.Projects) >= projects || strings.HasSuffix(p.Extra, "done") || strings.HasSuffix(p.Extra, "ignored") {
			continue
		}
		d.Projects = append(d.Projects, digestEntry(p))
	}
	for _, c := range e.List(KindChat, "") {
		if len(d.Chats) >= chats || c.Importance == "P3" {
			continue
		}
		d.Chats = append(d.Chats, digestEntry(c))
	}
	return prompts.RenderKBDigest(d)
}

func digestEntry(x Entry) prompts.KBDigestEntry {
	return prompts.KBDigestEntry{ID: x.ID, Name: x.Name, Importance: x.Importance, OneLine: x.OneLine, Relevance: x.Relevance}
}

// carryOver gives an update of a stored item what the source does not send (tags, projects, note, first seen); the
// item is tagged again, as it changed. The Tagging it copies carries the mark that it was read from disk, so a write
// after the item was deleted meanwhile (a prune, a forget) does not bring it back (putEntity skips a persisted item
// that is no longer there).
func carryOver(it, old *Item) {
	it.Tagging, it.Projects, it.Note, it.Seen = old.Tagging, old.Projects, old.Note, old.Seen
	it.Tagging.TaggedAt = time.Time{}
}
