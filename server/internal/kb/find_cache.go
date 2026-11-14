package kb

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// findCacheBytes bounds what findCache keeps of the files' text (raw and lowercased); files beyond it are read every
// time, as without the cache.
const findCacheBytes = 64 << 20

// findSettle: a file changed this recently is not kept. A write within the same mtime tick and of the same size
// would otherwise go unseen; once the mtime is this old, any later write gives a newer one.
const findSettle = 2 * time.Second

// fileStat is what tells a file changed.
type fileStat struct {
	path    string
	content [32]byte // accepted recording bytes can differ from physical files under a journal
	mod     int64    // UnixNano
	size    int64
}

// findReadHook, when set by a test, runs between a file's stat and its read.
var findReadHook func(path string)

func settling(st fileStat) bool { return time.Since(time.Unix(0, st.mod)) < findSettle }

type findText struct {
	stat  fileStat
	raw   string
	lower string // raw folded (fold): the same string unless raw has upper case or decomposed text
	same  bool   // lower == raw, so Find matches the lines as written (see matchFolded)
	gen   uint64 // the last search that saw the file (prune drops the ones no search saw since)
}

// findCache is what Find keeps between searches: each file's text, and the entries of the lists while no JSON file
// changed. Everything is checked against a fresh stat on every search, so results are those of reading every file.
type findCache struct {
	mu    sync.Mutex
	gen   uint64
	texts map[string]*findText
	bytes int
	// metas: the stats of the JSON files known was built from (sorted by path as files lists them).
	metas []fileStat
	known map[string]Entry

	// The walk (files): each directory's listing while its mtime holds (adding, removing or renaming an entry
	// changes it), and the sorted result while no listing changed.
	walkMu  sync.Mutex
	walkGen uint64
	dirs    map[string]*dirListing
	walked  bool
	walk    *dirWalk
	scratch [2][]*dirEnt // the files and days a walk gathers, reused by the next one

	// reads counts the files text read from disk (tests tell by it what a search did not have to read).
	reads atomic.Int64
}

// dirWalk is what one walk of the data dir found (files). Shared and read-only, but for search, which the first
// Search after the walk fills in with walkMu held (searchFiles).
type dirWalk struct {
	files    []*dirEnt // profile files, as walked
	days     []*dirEnt // message days, newest first (stable: same-named days as walked)
	all      []*dirEnt // files then days: what Find looks at
	walkDays []*dirEnt // the days as walked
	search   *searchOrder
}

type dirListing struct {
	stat fileStat
	ents []dirEnt
	gen  uint64
}

// dirEnt is a directory's child with what the searches would otherwise work out for every file on every search.
type dirEnt struct {
	name string
	dir  bool
	path string // the directory's path joined with name
	rel  string // relative to the data dir, with slashes
	// kind and id: the entry of a file Find reads (entryOf, shown); "" for any other file.
	kind, id string
	// day: under a messages/ directory. moment: under moments/posts/ (Search reads the .md ones last). file: any
	// other file a search may read: searchable, .md or .json and not a recording's segments.json (transcript.md has
	// the same text), or a Moments .md.
	day, file, moment bool
}

func newDirEnt(dir, rel, name string, isDir bool) dirEnt {
	d := dirEnt{dir: isDir, path: filepath.Join(dir, name), rel: name}
	if rel != "" {
		d.rel = rel + "/" + name
	}
	d.name = d.rel[len(d.rel)-len(name):] // one string kept, not two
	if isDir {
		return d
	}
	// A file under messages/ is a day unless it is bookkeeping (searchable): a chat's messages/.zone names its zone.
	d.day, d.moment = under(d.rel, "/messages/") && searchable(d.rel), under(d.rel, "/moments/posts/")
	md := strings.HasSuffix(name, ".md")
	d.file = !d.day && (d.moment && md || name != "segments.json" && searchable(d.rel) && (md || strings.HasSuffix(name, ".json")))
	if kind, id, ok := entryOf(d.rel); ok && shown(kind, d.rel) {
		d.kind, d.id = kind, id
	}
	return d
}

// under reports whether rel (with slashes) is in a directory named as sub ("/messages/") at any depth.
func under(rel, sub string) bool {
	return strings.HasPrefix(rel, sub[1:]) || strings.Contains(rel, sub)
}

// listDir returns a directory's entries sorted by name, and whether they were read now rather than kept; rel is the
// directory's path relative to the data dir ("" for itself). Called with walkMu held.
func (c *findCache) listDir(dir, rel string) ([]dirEnt, bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, true
	}
	st := fileStat{path: dir, mod: fi.ModTime().UnixNano(), size: fi.Size()}
	if l := c.dirs[dir]; l != nil && l.stat == st {
		l.gen = c.walkGen
		return l.ents, false
	}
	des, err := os.ReadDir(dir)
	ents := make([]dirEnt, len(des))
	for i, d := range des {
		ents[i] = newDirEnt(dir, rel, d.Name(), d.IsDir())
	}
	delete(c.dirs, dir)
	if err == nil && !settling(st) {
		if c.dirs == nil {
			c.dirs = map[string]*dirListing{}
		}
		c.dirs[dir] = &dirListing{stat: st, ents: ents, gen: c.walkGen}
	}
	return ents, true
}

func (c *findCache) begin() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	return c.gen
}

// Recording text bypasses the disk cache: a journal may mask a newer physical
// file without changing that file's stat. Other archive files keep their cache.
func (e *Engine) searchText(p string, gen uint64, recording map[string]map[string][]byte) (*findText, fileStat, bool) {
	rel, err := filepath.Rel(e.Store.Dir, p)
	if err == nil {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) == 3 && parts[0] == KindRecording && (parts[2] == "meta.json" || parts[2] == "transcript.md") {
			raw := recording[parts[1]][parts[2]]
			if raw == nil {
				return nil, fileStat{}, false
			}
			st := fileStat{path: p, size: int64(len(raw)), content: sha256.Sum256(raw)}
			text := string(raw)
			t := &findText{stat: st, raw: text, lower: fold(text)}
			t.same = t.lower == t.raw
			return t, st, true
		}
	}
	return e.find.text(p, gen)
}

// text returns a file's text and its stat, from the cache while the file is unchanged.
func (c *findCache) text(p string, gen uint64) (*findText, fileStat, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fileStat{}, false
	}
	st := fileStat{path: p, mod: fi.ModTime().UnixNano(), size: fi.Size()}
	c.mu.Lock()
	t := c.texts[p]
	if t != nil && t.stat == st {
		t.gen = gen
		c.mu.Unlock()
		return t, st, true
	}
	c.mu.Unlock()
	c.reads.Add(1)
	// The text is read through one handle between two stats of it, so it is kept only under the stat of the file
	// it came from: a stat by path and a read by path could be two files.
	raw, read, kept, err := readStable(p)
	if err != nil {
		return nil, st, false
	}
	st = read
	s := string(raw)
	t = &findText{stat: st, raw: s, lower: fold(s), gen: gen}
	t.same = t.lower == t.raw
	if !kept || settling(st) {
		return t, st, true // changing while read, or just now: used once, not kept
	}
	n := t.cost()
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.texts[p]; old != nil {
		c.bytes -= old.cost()
		delete(c.texts, p)
	}
	if c.bytes+n <= findCacheBytes {
		if c.texts == nil {
			c.texts = map[string]*findText{}
		}
		c.texts[p] = t
		c.bytes += n
	}
	return t, st, true
}

// readStable reads a file through one handle and returns its text with the handle's stat, and whether the text is
// that stat's: the handle's stat is the same after the read and the length read is its size.
func readStable(p string) ([]byte, fileStat, bool, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, fileStat{}, false, err
	}
	defer f.Close()
	stat := func() (fileStat, error) {
		fi, err := f.Stat()
		if err != nil {
			return fileStat{}, err
		}
		return fileStat{path: p, mod: fi.ModTime().UnixNano(), size: fi.Size()}, nil
	}
	st, err := stat()
	if err != nil {
		return nil, fileStat{}, false, err
	}
	if findReadHook != nil {
		findReadHook(p)
	}
	var b bytes.Buffer
	b.Grow(int(st.size) + bytes.MinRead) // the size and room to see the end without growing, as os.ReadFile
	if _, err := b.ReadFrom(f); err != nil {
		return nil, st, false, err
	}
	after, err := stat()
	return b.Bytes(), st, err == nil && after == st && int64(b.Len()) == st.size, nil
}

// textAt is text for a file whose stat was just read (st): the cached text when it is of that stat; otherwise the
// file is read as it is now.
func (c *findCache) textAt(p string, gen uint64, st fileStat) (*findText, bool) {
	c.mu.Lock()
	if t := c.texts[p]; t != nil && t.stat == st {
		t.gen = gen
		c.mu.Unlock()
		return t, true
	}
	c.mu.Unlock()
	t, _, ok := c.text(p, gen)
	return t, ok
}

// seen marks the cached texts of files the search gen did not read but found unchanged (their stats now are files'):
// prune keeps them.
func (c *findCache) seen(gen uint64, files func(yield func(fileStat) bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.texts) == 0 {
		return
	}
	files(func(st fileStat) bool {
		if t := c.texts[st.path]; t != nil && t.stat == st {
			t.gen = gen
		}
		return true
	})
}

// cost is the bytes a cached text holds (folding returns the same string when nothing changes; a decomposed or
// mixed-case file keeps a second, folded copy).
func (t *findText) cost() int {
	if t.lower == t.raw {
		return len(t.raw)
	}
	return len(t.raw) + len(t.lower)
}

// prune drops the files the search gen and those after it did not see (deleted, or no longer searched).
func (c *findCache) prune(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, t := range c.texts {
		if t.gen < gen {
			c.bytes -= t.cost()
			delete(c.texts, p)
		}
	}
}

// entries returns the lists' entries, built again by list unless metas (the JSON files' stats now) are those they
// were built from. The map is shared: callers only read it.
func (c *findCache) entries(metas []fileStat, list func() map[string]Entry) map[string]Entry {
	c.mu.Lock()
	if c.known != nil && slices.Equal(c.metas, metas) {
		known := c.known
		c.mu.Unlock()
		return known
	}
	c.mu.Unlock()
	known := list()
	for _, m := range metas {
		if settling(m) {
			return known // a file just written: build again next time
		}
	}
	c.mu.Lock()
	c.metas, c.known = metas, known
	c.mu.Unlock()
	return known
}
