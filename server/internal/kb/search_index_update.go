package kb

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Keeping the search index current (search_index.go). An update looks at every file a search reads from disk, reads
// the ones the index does not have as they are now, and drops what it holds of files that are gone or changed. It
// never has to finish for searches to be right: whatever it has not read yet, they read.
const (
	// indexSettle: a file changed this recently waits for a later update. Like findSettle, it also makes an entry
	// safe to trust: once the mtime read is this old, any later write gives a newer one.
	indexSettle = 10 * time.Second
	// indexMaxFile: a larger file is not read into the index (searches read it, as before): working out a file's
	// keys takes several times its size in memory.
	indexMaxFile = 4 << 20
	// An entry that is not of a file as it is now is of no use to a search, and is dropped by writing the index
	// again as one segment: at the next update after SearchIndexPurge (text deleted on purpose), within
	// indexPurgeEvery of the last such merge when its file is gone, within indexMergeEvery when its file changed
	// (nearly always a day that got new lines, whose old entry says nothing the new one does not).
	indexPurgeEvery = 10 * time.Minute
	indexMergeEvery = 24 * time.Hour
)

// indexSizes are the sizes an update works in (a variable so tests reach every path with a small archive).
var indexSizes = struct {
	// The log takes the files of ordinary updates; past logFiles files or logBytes bytes it becomes a segment.
	logFiles int
	logBytes int64
	// More files than the log takes are written straight to segments, each of at most batchKeys keys or batchFiles
	// files, so a first build holds one batch in memory, not the archive.
	batchKeys, batchFiles int
	// More than maxSegs segments are written again as one.
	maxSegs int
	// Files are read readChunk at a time; an update asked to stop does between two.
	readChunk int
	// The long loops that read no file ask the context whether to stop as they go: the look at the files every
	// lookAsks files, a merge every mergeAsks keys.
	lookAsks, mergeAsks int
}{logFiles: 4096, logBytes: 16 << 20, batchKeys: 2 << 20, batchFiles: 1 << 16, maxSegs: 8, readChunk: 512, lookAsks: 1024, mergeAsks: 4096}

// SearchIndexStats is what one update did.
type SearchIndexStats struct {
	Files   int  // files searches read from disk
	Read    int  // read into the index by this update
	Waiting int  // not in the index: changed too recently, too large, or another file by the time it was read
	Dropped int  // entries of files gone or changed, taken out
	Merged  bool // the segments were written again as one
	Rebuilt bool // the index on disk did not check out and was started again
	Took    time.Duration
}

// indexedFile reports whether the index holds d: every file a search reads from disk. Not a recording's uploaded
// files (searchText reads those from the committed snapshot, never the disk) nor a Moments post's JSON (never
// searched).
func indexedFile(d *dirEnt) bool {
	if d.moment && !strings.HasSuffix(d.name, ".md") {
		return false
	}
	return !recordingViewFile(d.rel)
}

// recordingViewFile reports whether rel (relative to the data dir, with slashes) is a recording's meta.json or
// transcript.md.
func recordingViewFile(rel string) bool {
	rest, ok := strings.CutPrefix(rel, KindRecording+"/")
	if !ok {
		return false
	}
	_, name, ok := strings.Cut(rest, "/")
	return ok && (name == "meta.json" || name == "transcript.md")
}

// fileTime is what the index knows a file by, next to its path: its mtime and size. ok: the file is there.
type fileTime struct {
	mod, size int64
	ok        bool
}

func statFile(p string) fileTime {
	fi, err := os.Stat(p)
	if err != nil {
		return fileTime{}
	}
	return fileTime{mod: fi.ModTime().UnixNano(), size: fi.Size(), ok: true}
}

// openTime is statFile of an open file: of that file, whatever its path names by now.
func openTime(f *os.File) fileTime {
	fi, err := f.Stat()
	if err != nil {
		return fileTime{}
	}
	return fileTime{mod: fi.ModTime().UnixNano(), size: fi.Size(), ok: true}
}

// each calls f(i) for i in [0, n) on up to workers goroutines.
func each(n, workers int, f func(i int)) {
	if workers <= 1 || n < 2 {
		for i := range n {
			f(i)
		}
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(workers, n) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				f(i)
			}
		}()
	}
	wg.Wait()
}

// UpdateSearchIndex brings the search index up to date with the files: it reads those changed or added since it
// last read them and takes out what it holds of files that are gone. The first run on an existing archive reads
// everything, in batches written as it goes; searches meanwhile read the files it has not reached. When ctx ends
// it stops: while it looks at the files and while it merges within a moment, while it reads them after the batch in
// hand. The next run goes on from what was published.
func (e *Engine) UpdateSearchIndex(ctx context.Context) (SearchIndexStats, error) {
	if e.Store.ReadOnly() {
		return SearchIndexStats{}, ErrReadOnly
	}
	ix := &e.Store.search
	ix.update.Lock()
	defer ix.update.Unlock()
	u := &indexUpdate{e: e, ix: ix, dir: e.Store.searchDir(), start: time.Now(), workers: max(1, min(4, runtime.GOMAXPROCS(0)/2))}
	err := u.run(ctx)
	u.stats.Took = time.Since(u.start)
	if err != nil && damaged(err) && u.snap != nil {
		// A part of the index did not check out while it was merged: the next update starts it again, if it is
		// still the index in use (a search that found it gone has read the one on disk meanwhile: spoil).
		ix.spoil(u.snap)
	}
	return u.stats, err
}

type indexUpdate struct {
	e       *Engine
	ix      *searchIndex
	dir     string
	start   time.Time
	workers int
	stats   SearchIndexStats

	// The index as this update has it: what it started from (open) and published since. Its own, so nothing a
	// search does to the store's state while it runs (searchIndex.reload) is read here.
	snap   *searchSnap
	man    indexManifestFile
	logLen int64 // the log's valid bytes
	logOld int   // the log's entries that a later one replaced
	// What the look at the files found: which entries of each segment belong to a file that is still there (seen),
	// and still as the entry says (alive); the same for the log.
	seen, alive       []bitset
	logSeen, logAlive map[string]bool
}

func (u *indexUpdate) run(ctx context.Context) error {
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return err
	}
	if err := u.open(); err != nil {
		return err
	}
	purge := u.ix.purge.Swap(false)
	done := false
	defer func() {
		if purge && !done {
			u.ix.purge.Store(true)
		}
	}()
	ents := u.e.indexFiles()
	todo, stats, err := u.look(ctx, ents)
	if err != nil {
		return err
	}
	u.ix.at(hookLooked, "")
	if err := u.read(ctx, ents, todo, stats); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := u.maintain(ctx, purge); err != nil {
		return err
	}
	done = true
	if !u.man.Complete {
		u.man.Complete = true
		if err := u.publish(u.snap, u.logLen); err != nil {
			return err
		}
	}
	u.sweep()
	return nil
}

// open is the index as it is on disk; one that does not check out (or failed a search's check) is removed, and the
// update starts from nothing.
func (u *indexUpdate) open() error {
	ix := u.ix
	manifest := filepath.Join(u.dir, indexManifest)
	ix.mu.Lock()
	st := ix.state.Load()
	if st != nil && !u.unchanged(st) {
		// Not the index this process left: it was removed or put back from outside (a person, a restore). What is
		// there now is read, and checked as at a start.
		st = nil
		ix.broken.Store(false)
	}
	var err error
	if st == nil {
		st, err = ix.loadLocked(u.dir)
	}
	if err == nil && ix.broken.Load() {
		// The manifest first: without it there is no index, whatever else is still there (sweep removes that).
		if err = os.Remove(manifest); err == nil || os.IsNotExist(err) {
			err = nil
			st = &indexState{snap: &searchSnap{}}
			ix.state.Store(st)
			ix.ready.Store(false)
			ix.broken.Store(false)
			u.stats.Rebuilt = true
		}
	}
	ix.mu.Unlock()
	if err != nil {
		return err
	}
	u.snap, u.man, u.logLen, u.logOld = st.snap, st.man, st.logLen, st.logOld
	if u.man.Merged.IsZero() {
		u.man.Merged = time.Now().UTC() // a new index: nothing in it is older than now
	}
	// What an update that died left behind, or a damaged index: gone before a file is written.
	u.sweep()
	return nil
}

// unchanged reports whether the index on disk is still the one st was read from or published as: the manifest by
// its mtime and size, every segment by its footer (indexSeg.open). An update goes on from the files parts it holds
// in memory (which entries are still good, what a merge keeps), so they must be those of the segments on disk.
func (u *indexUpdate) unchanged(st *indexState) bool {
	if statFile(filepath.Join(u.dir, indexManifest)) != st.manStat {
		return false
	}
	for _, s := range st.snap.segs {
		f, err := s.open()
		if err != nil {
			return false
		}
		f.Close()
	}
	return true
}

// indexFiles are the files the index holds, in the order searches read them: profile files, then message days
// newest first.
func (e *Engine) indexFiles() []*dirEnt {
	w := e.files()
	out := make([]*dirEnt, 0, len(w.files)+len(w.days))
	for _, l := range [][]*dirEnt{w.files, w.days} {
		for _, d := range l {
			if indexedFile(d) {
				out = append(out, d)
			}
		}
	}
	return out
}

// look stats every file and tells which the index must read (their indices in ents), noting which entries are
// still good. It stops when ctx ends.
func (u *indexUpdate) look(ctx context.Context, ents []*dirEnt) (todo []int, stats []fileTime, err error) {
	stats = make([]fileTime, len(ents))
	asks := max(1, indexSizes.lookAsks)
	var stopped atomic.Bool
	each(len(ents), u.workers, func(i int) {
		if stopped.Load() {
			return
		}
		if i%asks == asks-1 && ctx.Err() != nil {
			stopped.Store(true)
			return
		}
		stats[i] = statFile(ents[i].path)
	})
	if stopped.Load() {
		return nil, nil, cmp.Or(ctx.Err(), context.Canceled)
	}
	snap := u.snap
	u.seen, u.alive = make([]bitset, len(snap.segs)), make([]bitset, len(snap.segs))
	for i, s := range snap.segs {
		u.seen[i], u.alive[i] = newBitset(len(s.docs)), newBitset(len(s.docs))
	}
	u.logSeen, u.logAlive = map[string]bool{}, map[string]bool{}
	now := time.Now()
	for i, d := range ents {
		st := stats[i]
		if !st.ok {
			continue // gone since the walk
		}
		u.stats.Files++
		r, inSeg := snap.where[d.rel]
		if inSeg {
			u.seen[r.seg].set(r.id)
		}
		if ld := snap.log[d.rel]; ld != nil {
			u.logSeen[d.rel] = true
			if ld.mod == st.mod && ld.size == st.size {
				u.logAlive[d.rel] = true
				continue
			}
		} else if inSeg && r.mod == st.mod && r.size == st.size {
			u.alive[r.seg].set(r.id)
			continue
		}
		if st.size > indexMaxFile || now.Sub(time.Unix(0, st.mod)) < indexSettle {
			u.stats.Waiting++
			continue
		}
		todo = append(todo, i)
	}
	return todo, stats, nil
}

// read reads the files of todo into the index: a few go to the log, more than the log takes to segments.
func (u *indexUpdate) read(ctx context.Context, ents []*dirEnt, todo []int, stats []fileTime) error {
	if len(todo) == 0 {
		return nil
	}
	bulk := len(todo)+len(u.snap.log) > indexSizes.logFiles
	var batch []builtDoc
	keys := 0
	if bulk {
		// The log's files go into the first segment: an entry left in the log would come before the newer one a
		// segment gets.
		for name, d := range u.snap.log {
			if u.logAlive[name] {
				batch = append(batch, *d)
				keys += len(d.keys)
			}
		}
	}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		err := u.addSegment(batch, bulk)
		batch, keys = nil, 0
		return err
	}
	chunk := indexSizes.readChunk
	docs := make([]*builtDoc, chunk)
	other := make([]bool, chunk) // there, and not (or not for the whole read) the file the look saw
	for start := 0; start < len(todo) && ctx.Err() == nil; start += chunk {
		part := todo[start:min(len(todo), start+chunk)]
		each(len(part), u.workers, func(i int) {
			d, st := ents[part[i]], stats[part[i]]
			docs[i], other[i] = nil, false
			raw, gone, ok := u.readLooked(d, st)
			if !ok {
				other[i] = !gone
				return // changing, another file, or gone: the search reads it, and the next update looks again
			}
			docs[i] = &builtDoc{segDoc: segDoc{name: d.rel, mod: st.mod, size: st.size}, keys: fileGrams(fold(string(raw)))}
		})
		for i, d := range docs[:len(part)] {
			if d == nil {
				if other[i] {
					u.stats.Waiting++
				}
				continue
			}
			batch = append(batch, *d)
			keys += len(d.keys)
			u.stats.Read++
			if bulk && (keys >= indexSizes.batchKeys || len(batch) >= indexSizes.batchFiles) {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	}
	if bulk {
		return flush()
	}
	return u.appendLog(batch)
}

// readLooked reads the file at d.path if it is the file the look saw there as st, and for as long as it is read:
// its bytes and ok, or gone when no file is there any more.
//
// The look and the read are two moments (in a first build the look at every file lies between them), and an entry
// says that the bytes which had this mtime and size have these keys. So the mtime and size have to be those of the
// very bytes read: the file is opened, asked through the handle, read through the handle and asked through it
// again, and both answers have to be the look's. Asking the path again would not do: by then it can name the file
// the look saw once more (moved back, put back by a sync or backup tool) while the bytes read were those of the
// file that was there in between, and the entry would rule the file out for words it holds, for good. What this
// cannot see is the index's first limit (docs/assistant/knowledge.md, "Search index (#1273)"): the same file
// written to the same size and its mtime set back to what it was, here within the one read.
func (u *indexUpdate) readLooked(d *dirEnt, st fileTime) (raw []byte, gone, ok bool) {
	f, err := os.Open(d.path)
	if err != nil {
		return nil, errors.Is(err, fs.ErrNotExist), false
	}
	defer f.Close()
	if openTime(f) != st {
		return nil, false, false
	}
	u.ix.at(hookOpened, d.rel)
	// One byte more than its size: a file that grew since does not end there.
	raw = make([]byte, st.size+1)
	n, err := io.ReadFull(f, raw)
	if int64(n) != st.size || err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, false, false
	}
	u.ix.at(hookRead, d.rel)
	if openTime(f) != st {
		return nil, false, false
	}
	return raw[:n], false, true
}

// name gives a new file of the index its name, one no file of the index had before: the count, for whoever reads
// the directory, and 64 bits drawn at random. The count alone would not do: it starts again whenever the index does
// (built again after a failed check, .search removed), and a segment written under the name of an earlier one is
// read by whoever still holds the earlier one's files part as if it were that one.
func (u *indexUpdate) name(kind, ext string) string {
	u.man.Seq++
	var id [8]byte
	_, _ = rand.Read(id[:]) // never fails
	return fmt.Sprintf("%s-%06d-%x%s", kind, u.man.Seq, id, ext)
}

// addSegment writes docs as a new segment and publishes it. resetLog: the log's files are in docs, so the index
// goes on with an empty one.
func (u *indexUpdate) addSegment(docs []builtDoc, resetLog bool) error {
	name := u.name("seg", ".idx")
	path := filepath.Join(u.dir, name)
	if err := writeSeg(path, docs); err != nil {
		return err
	}
	seg, err := openSeg(path)
	if err != nil {
		return err
	}
	next := &searchSnap{segs: append(append([]*indexSeg{}, u.snap.segs...), seg), log: u.snap.log}
	next.where = cloneWhere(u.snap.where, len(u.snap.segs), seg)
	u.man.Segments = append(append([]string{}, u.man.Segments...), name)
	logLen := u.logLen
	if resetLog {
		next.log, u.man.Log, logLen, u.logOld = nil, "", 0, 0
		u.logSeen, u.logAlive = map[string]bool{}, map[string]bool{}
	}
	// Every entry of the new segment is as its file was a moment ago.
	u.seen, u.alive = append(u.seen, nil), append(u.alive, nil)
	return u.publish(next, logLen)
}

// flushLog turns the log into a segment (what it holds of files gone or changed since is dropped) and starts an
// empty one.
func (u *indexUpdate) flushLog() error {
	if docs := u.liveLog(); len(docs) > 0 {
		return u.addSegment(docs, true)
	}
	if u.man.Log == "" && len(u.snap.log) == 0 {
		return nil
	}
	u.man.Log, u.logOld = "", 0
	u.logSeen, u.logAlive = map[string]bool{}, map[string]bool{}
	return u.publish(&searchSnap{segs: u.snap.segs, where: u.snap.where}, 0)
}

// appendLog adds docs to the log and publishes them.
func (u *indexUpdate) appendLog(docs []builtDoc) error {
	if len(docs) == 0 {
		return nil
	}
	if u.man.Log == "" {
		u.man.Log = u.name("log", ".log")
	}
	path := filepath.Join(u.dir, u.man.Log)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	logLen := u.logLen
	var buf []byte
	if logLen == 0 {
		buf = append(buf, indexLogMagic...)
	}
	for i := range docs {
		buf = encodeLogEntry(buf, &docs[i])
	}
	fi, err := f.Stat()
	if err == nil && fi.Size() < logLen {
		// The entries read from it are no longer there (the file was removed or cut): not a log to append to.
		err = fmt.Errorf("%w: the log is shorter than it was read", errSegment)
	}
	// What follows the whole entries (an append cut short by a crash) is cut off first.
	if err == nil {
		err = f.Truncate(logLen)
	}
	if err == nil {
		_, err = f.WriteAt(buf, logLen)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	next := &searchSnap{segs: u.snap.segs, where: u.snap.where, log: make(map[string]*builtDoc, len(u.snap.log)+len(docs))}
	for name, d := range u.snap.log {
		next.log[name] = d
	}
	for i := range docs {
		if next.log[docs[i].name] != nil {
			u.logOld++
		}
		next.log[docs[i].name] = &docs[i]
		u.logSeen[docs[i].name], u.logAlive[docs[i].name] = true, true
	}
	return u.publish(next, logLen+int64(len(buf)))
}

// publish writes the manifest and gives searches the new snapshot.
func (u *indexUpdate) publish(next *searchSnap, logLen int64) error {
	u.man.Format, u.man.Tables = indexFormat, indexTables
	raw, err := json.Marshal(u.man)
	if err != nil {
		return err
	}
	manifest := filepath.Join(u.dir, indexManifest)
	if err := writeIndexFile(manifest, raw); err != nil {
		return err
	}
	u.snap, u.logLen = next, logLen
	u.ix.mu.Lock()
	u.ix.state.Store(&indexState{snap: next, man: u.man, logLen: logLen, logOld: u.logOld, manStat: statFile(manifest)})
	u.ix.ready.Store(u.man.Complete)
	u.ix.mu.Unlock()
	return nil
}

// maintain turns a full log into a segment, and writes the index again as one segment when there are too many or
// it holds entries to drop (indexPurgeEvery): at once when purge, of files gone within indexPurgeEvery, of files
// changed within indexMergeEvery or once they are a quarter of the index. A merge stops when ctx ends, and the next
// update does it.
func (u *indexUpdate) maintain(ctx context.Context, purge bool) error {
	snap := u.snap
	total, alive, gone := len(snap.log), len(u.logAlive), 0
	for i, s := range snap.segs {
		total += len(s.docs)
		if u.alive[i] == nil {
			alive += len(s.docs) // written by this update
		} else {
			alive += u.alive[i].count()
		}
	}
	for name, r := range snap.where {
		if u.seen[r.seg] != nil && !u.seen[r.seg].has(r.id) && snap.log[name] == nil {
			gone++
		}
	}
	for name := range snap.log {
		if !u.logSeen[name] {
			gone++
		}
	}
	// stale: the entries no search uses, those the log still has of a file it read again too.
	stale := total - alive + u.logOld
	since := time.Since(u.man.Merged)
	if since < 0 {
		// A last merge that is yet to come: the clock was set back, or the manifest's time is not the one written.
		// Taken as long ago: waiting for that time would keep what is to be dropped for as long as it is away.
		since = indexMergeEvery
	}
	merge := len(snap.segs) > indexSizes.maxSegs ||
		stale > 0 && purge ||
		gone > 0 && since >= indexPurgeEvery ||
		stale > 0 && since >= indexMergeEvery ||
		stale*4 > total+u.logOld
	if !merge {
		if len(snap.log) >= indexSizes.logFiles || u.logLen >= indexSizes.logBytes {
			return u.flushLog()
		}
		return nil
	}
	if err := u.flushLog(); err != nil {
		return err
	}
	snap = u.snap
	inputs := make([]mergeInput, len(snap.segs))
	for i, s := range snap.segs {
		inputs[i].seg = s
		if alive := u.alive[i]; alive != nil {
			inputs[i].keep = alive.has
		}
	}
	name := u.name("seg", ".idx")
	path := filepath.Join(u.dir, name)
	n, err := mergeSegs(ctx, path, inputs)
	if err != nil {
		return err
	}
	next := &searchSnap{}
	u.man.Segments = nil
	if n == 0 {
		_ = os.Remove(path)
	} else {
		seg, err := openSeg(path)
		if err != nil {
			return err
		}
		next.segs = []*indexSeg{seg}
		next.where = whereOf(next.segs)
		u.man.Segments = []string{name}
	}
	u.man.Merged = time.Now().UTC()
	u.seen, u.alive = make([]bitset, len(next.segs)), make([]bitset, len(next.segs))
	u.stats.Dropped, u.stats.Merged = stale, true
	return u.publish(next, 0)
}

// liveLog is the log's files that are still as their entries say.
func (u *indexUpdate) liveLog() []builtDoc {
	docs := make([]builtDoc, 0, len(u.snap.log))
	for name, d := range u.snap.log {
		if u.logAlive[name] {
			docs = append(docs, *d)
		}
	}
	return docs
}

// sweep removes the index's files the manifest no longer names. One that cannot be removed (open elsewhere, on a
// system that minds) goes at a later update; its name is not given to another file meanwhile (name).
func (u *indexUpdate) sweep() {
	named := map[string]bool{indexManifest: true, u.man.Log: true}
	for _, s := range u.man.Segments {
		named[s] = true
	}
	ents, err := os.ReadDir(u.dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		if name := d.Name(); !d.IsDir() && !named[name] && indexOwned(name) {
			_ = os.Remove(filepath.Join(u.dir, name))
		}
	}
}
