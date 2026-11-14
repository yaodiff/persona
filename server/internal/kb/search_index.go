package kb

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// The search index (#1273, docs/assistant/knowledge.md "Search index") lets Find, Search and Matches pass over a
// file without reading it. It is kept in the data dir itself, in .search/: the files stay the source of truth, the
// index is only what was worked out from them, and it goes when the data dir goes.
//
// What it holds: for each file it has read, the path, mtime and size the file had then, and the file's keys
// (search_index_grams.go). What it is asked: "can this file, as it is now, hold every word of the query?". It may
// only answer no for a file whose path, mtime and size are the ones it read, so a file changed, added, renamed or
// not read yet is never passed over: the search reads it as it always did. An index that is missing, half built,
// behind or damaged therefore changes how long a search takes and nothing else.
//
// On disk:
//
//	.search/manifest.json       which files below make up the index, and what they were built with
//	.search/seg-<n>-<id>.idx    segments (search_index_segment.go), immutable; a file's newest entry is the one used
//	.search/log-<n>-<id>.log    the files read since the last segment was written, appended to; they come first
//
// <id> is drawn at random for every file, so a name is never used twice, whatever happens to the index in between
// (it is removed, built again, put back from a copy): a file found under a name is the one that was written there.
//
// One process writes it, the one that holds the data dir (UpdateSearchIndex, search_index_update.go); a read-only
// store uses what is there, and looks every indexFollowEvery whether the writer has published another.
const (
	// SearchIndexDir is the index's directory in the data dir. Worked out from the files, so a copy of the data dir
	// can leave it out (the history import's backups do).
	SearchIndexDir   = ".search"
	indexManifest    = "manifest.json"
	indexFormat      = 2
	indexLogMagic    = "PKBLOG1\n"
	indexLogMaxEntry = 256 << 20
)

// indexFollowEvery is how often a read-only store looks at the manifest for another process's update (a variable
// for the tests).
var indexFollowEvery = 30 * time.Second

// indexTables names the tables fold was built with: a key is taken from fold's output, so an index written with
// other case or normalization tables is not asked (it is built again).
var indexTables = unicode.Version + "/" + norm.Version

type indexManifestFile struct {
	Format int    `json:"format"`
	Tables string `json:"tables"`
	// Seq is the last number given to a file of the index.
	Seq uint64 `json:"seq"`
	// Segments, oldest first, and the log (may not exist yet).
	Segments []string `json:"segments"`
	Log      string   `json:"log"`
	// Merged is when the segments were last written as one: entries of files gone or changed since are in it at
	// most that long (UpdateSearchIndex).
	Merged time.Time `json:"merged"`
	// Complete: an update has finished since the index was started, so it holds the archive but for what changed
	// since (SearchIndexReady).
	Complete bool `json:"complete"`
}

// errIndexVersion: the index was written with another format or other folding tables.
var errIndexVersion = errors.New("search index: written by another version")

// errManifest: the manifest was read, and what was read is not a manifest of this index: it does not decode,
// whatever the decoder found wrong with it, or it names a file the index never gives a name like that.
var errManifest = errors.New("search index manifest is damaged")

// damaged reports whether err says the index itself is wrong (so it is built again), not that reading it failed
// this time (so it is tried again).
func damaged(err error) bool {
	return errors.Is(err, errSegment) || errors.Is(err, errSegmentOther) || errors.Is(err, errIndexVersion) ||
		errors.Is(err, errManifest) || errors.Is(err, fs.ErrNotExist)
}

// docRef is where a file's entry is in a snapshot's segments, with what tells the file changed since.
type docRef struct {
	seg  int32
	id   uint32
	mod  int64
	size int64
}

// searchSnap is the index as one search sees it. Never changed once published.
type searchSnap struct {
	segs  []*indexSeg
	where map[string]docRef    // by path: the file's entry in the newest segment that has one
	log   map[string]*builtDoc // by path: the files in the log; they come before where
}

func (s *searchSnap) empty() bool { return s == nil || len(s.where) == 0 && len(s.log) == 0 }

// indexState is what a store knows of the index on disk: the snapshot searches use, and what the next update goes
// on from. Never changed once stored: loading and publishing store another.
type indexState struct {
	snap   *searchSnap
	man    indexManifestFile
	logLen int64 // the log's valid bytes
	// logOld counts the log's entries that a later one replaced: still on disk until the log becomes a segment.
	logOld int
	// manStat is the manifest's mtime and size when it was read, or written by this process.
	manStat fileTime
}

// searchIndex is a store's index.
type searchIndex struct {
	// update: one writer at a time (UpdateSearchIndex), for the whole of its run. A run takes the state it starts
	// from and keeps its own manifest and log length from there on (indexUpdate), so a search that reads the index
	// from disk again while it runs (reload: the index was removed under both) changes nothing the run reads.
	update sync.Mutex
	// mu guards loading and publishing: state is stored with it held. Held briefly.
	mu sync.Mutex
	// state: nil until the index is read from disk.
	state atomic.Pointer[indexState]
	// looked: when a read-only store last compared the manifest with the one it read (UnixNano).
	looked atomic.Int64
	// broken: a check of the index in state failed. It is not asked again until the writer rebuilt it. Set with mu
	// held, and only of the state that failed the check (loadLocked, spoil).
	broken atomic.Bool
	// ready: the manifest's Complete (SearchIndexReady).
	ready atomic.Bool
	// purge: files were deleted on purpose (a source forgotten, retention): the next update drops their entries
	// at once instead of within the hour.
	purge atomic.Bool
	// hook is nil but in a test, which sets it before the store is used: it is called at the moments named below
	// (name: the file of the archive the moment is about, if one), for the test to do there what another process or
	// goroutine can do at such a moment and no test could otherwise time.
	hook func(at, name string)
}

// The moments a test's hook is called at (searchIndex.hook).
const (
	hookLooked  = "update: looked"  // an update has looked at every file and has read none yet
	hookOpened  = "update: opened"  // it has opened name, found it as the look did, and has not read it yet
	hookRead    = "update: read"    // it has read name and has not yet asked again what it read
	hookDamaged = "search: damaged" // a search found a part of the index damaged and has not yet said so
	// A read-only store's look found another manifest than the one it read, dropped what it held, and has not
	// read the index again yet.
	hookFollowed = "search: followed"
)

func (ix *searchIndex) at(moment, name string) {
	if ix.hook != nil {
		ix.hook(moment, name)
	}
}

func (s *Store) searchDir() string { return filepath.Join(s.Dir, SearchIndexDir) }

// current is the snapshot in use; nil when the index has not been read.
func (ix *searchIndex) current() *searchSnap {
	if st := ix.state.Load(); st != nil {
		return st.snap
	}
	return nil
}

// snapshot is the index to search with, read from disk the first time; nil when there is none to use. follow: the
// store does not write the index (it is read-only), so the one on disk is read again when its manifest changed.
func (ix *searchIndex) snapshot(dir string, follow bool) *searchSnap {
	if follow {
		now, last := time.Now().UnixNano(), ix.looked.Load()
		if now-last >= int64(indexFollowEvery) && ix.looked.CompareAndSwap(last, now) {
			ix.follow(dir)
		}
	}
	if ix.broken.Load() {
		return nil
	}
	if st := ix.state.Load(); st != nil {
		return st.snap
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	st := ix.state.Load()
	if st == nil {
		var err error
		if st, err = ix.loadLocked(dir); err != nil {
			return nil
		}
	}
	return st.snap
}

// follow is a read-only store's look at the manifest. When it is not the one the store read, what the store holds
// is dropped together with what was found wrong with it, and the next search reads the index that is there now.
// A store that holds nothing has nothing to compare: it is left to read the index, whatever it was marked as (a
// store with no index and "broken" set would otherwise never read one again: nothing else clears the mark of a
// read-only store).
func (ix *searchIndex) follow(dir string) {
	st := ix.state.Load()
	if st != nil && statFile(filepath.Join(dir, indexManifest)) == st.manStat {
		return
	}
	ix.mu.Lock()
	if ix.state.Load() == st {
		ix.state.Store(nil)
		ix.broken.Store(false)
	}
	ix.mu.Unlock()
	if st != nil {
		ix.at(hookFollowed, "")
	}
}

// spoil marks the index as having failed a check, if snap, the snapshot that failed it, is still the one in use,
// and reports whether it is. The check says nothing of an index read or published since: marking that one would
// send searches past a whole index, cost the writer a second rebuild, and leave a read-only store that has just
// dropped its state (follow) marked with nothing to compare a new manifest with.
func (ix *searchIndex) spoil(snap *searchSnap) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.current() != snap {
		return false
	}
	ix.broken.Store(true)
	return true
}

// reload reads the index from disk again when snap is still the one in use: a file of it is gone or is another
// file now, which is the writer's merge or rebuild when the manifest names other files, and damage when it still
// names that one.
func (ix *searchIndex) reload(dir string, snap *searchSnap) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.current() == snap {
		_, _ = ix.loadLocked(dir)
	}
}

// loadLocked reads the index from dir and stores it. One that does not check out is stored as an empty index with
// broken set, for the writer to start again; when reading failed for another reason nothing is stored, and the next
// caller tries again. ix.mu is held.
func (ix *searchIndex) loadLocked(dir string) (*indexState, error) {
	st := &indexState{snap: &searchSnap{}, manStat: statFile(filepath.Join(dir, indexManifest))}
	snap, man, logLen, logOld, err := readIndex(dir)
	switch {
	case err == nil:
		st.snap, st.man, st.logLen, st.logOld = snap, man, logLen, logOld
		ix.ready.Store(man.Complete)
	case damaged(err):
		ix.broken.Store(true)
	default:
		return nil, err
	}
	ix.state.Store(st)
	return st, nil
}

// readIndex reads the index at dir: an empty one when there is no manifest. logLen and logOld are the log's whole
// entries in bytes, and how many of them a later entry replaced.
func readIndex(dir string) (snap *searchSnap, man indexManifestFile, logLen int64, logOld int, err error) {
	raw, err := os.ReadFile(filepath.Join(dir, indexManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return &searchSnap{}, man, 0, 0, nil
	}
	if err != nil {
		return nil, man, 0, 0, err
	}
	if man, err = decodeManifest(raw); err != nil {
		return nil, man, 0, 0, err
	}
	snap = &searchSnap{}
	for _, name := range man.Segments {
		seg, err := openSeg(filepath.Join(dir, name))
		if err != nil {
			return nil, man, 0, 0, err
		}
		snap.segs = append(snap.segs, seg)
	}
	snap.where = whereOf(snap.segs)
	if man.Log != "" {
		if snap.log, logLen, logOld, err = readIndexLog(filepath.Join(dir, man.Log)); err != nil {
			return nil, man, 0, 0, err
		}
	}
	return snap, man, logLen, logOld, nil
}

// decodeManifest is the manifest whose bytes are raw. Bytes that are not one are damage (errManifest), whatever
// the decoder says of them: a value of another type, a time that is not one, anything a later decoder minds. Were
// one of these an error like "the disk could not be read", every update would fail on it the same way and the
// index would never be built again. The files it names have the names the index gives its files (indexUpdate.name)
// and no others: nothing else in the directory, or outside it, is read as a segment or a log.
func decodeManifest(raw []byte) (man indexManifestFile, err error) {
	if err := json.Unmarshal(raw, &man); err != nil {
		return man, fmt.Errorf("%w: %v", errManifest, err)
	}
	if man.Format != indexFormat || man.Tables != indexTables {
		return man, errIndexVersion
	}
	for _, name := range man.Segments {
		if !indexNamed(name, "seg-", ".idx") {
			return man, fmt.Errorf("%w: it names %q as a segment", errManifest, name)
		}
	}
	if man.Log != "" && !indexNamed(man.Log, "log-", ".log") {
		return man, fmt.Errorf("%w: it names %q as its log", errManifest, man.Log)
	}
	return man, nil
}

// indexNamed reports whether name is one the index gives a file of that kind: in its directory, kind first and ext
// last.
func indexNamed(name, kind, ext string) bool {
	return name == filepath.Base(name) && strings.HasPrefix(name, kind) && strings.HasSuffix(name, ext)
}

// whereOf maps every path of segs to its entry in the newest segment that has one.
func whereOf(segs []*indexSeg) map[string]docRef {
	n := 0
	for _, s := range segs {
		n += len(s.docs)
	}
	where := make(map[string]docRef, n)
	for i, s := range segs {
		for id, d := range s.docs {
			where[d.name] = docRef{seg: int32(i), id: uint32(id), mod: d.mod, size: d.size}
		}
	}
	return where
}

// readIndexLog reads the log at path: its files (the last entry of each), how many of its bytes are whole entries,
// and how many entries a later one replaced. What follows the whole entries (an append cut short) is not read; the
// writer cuts it off. No file is an empty log.
func readIndexLog(path string) (docs map[string]*builtDoc, valid int64, old int, err error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, 0, nil
	}
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	return readIndexLogFrom(f, fi.Size())
}

// readIndexLogFrom reads a log of size bytes from f, as readIndexLog says.
func readIndexLogFrom(f io.Reader, size int64) (docs map[string]*builtDoc, valid int64, old int, err error) {
	r := bufio.NewReaderSize(io.LimitReader(f, size), int(min(size, 1<<20)))
	head := make([]byte, len(indexLogMagic))
	switch n, err := io.ReadFull(r, head); {
	case err == nil && string(head) == indexLogMagic:
	case (err == io.EOF || err == io.ErrUnexpectedEOF) && string(head[:n]) == indexLogMagic[:n]:
		// Its first append was cut short (the log is not synced: a crash loses the newest entries, whose files are
		// read again): a log with nothing in it yet.
		return map[string]*builtDoc{}, 0, 0, nil
	case err != nil && err != io.EOF && err != io.ErrUnexpectedEOF:
		return nil, 0, 0, err
	default:
		return nil, 0, 0, errSegment
	}
	docs = map[string]*builtDoc{}
	valid = int64(len(indexLogMagic))
	var frame [8]byte
	for {
		if _, err := io.ReadFull(r, frame[:]); err != nil {
			break
		}
		n := binary.LittleEndian.Uint32(frame[:])
		// A length the file cannot hold is not an entry's: nothing is set aside for it.
		if n == 0 || n > indexLogMaxEntry || int64(n) > size-valid-int64(len(frame)) {
			break
		}
		body := make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil || crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(frame[4:]) {
			break
		}
		d, ok := decodeLogEntry(body)
		if !ok {
			break
		}
		if docs[d.name] != nil {
			old++
		}
		docs[d.name] = d
		valid += 8 + int64(n)
	}
	return docs, valid, old, nil
}

func encodeLogEntry(dst []byte, d *builtDoc) []byte {
	start := len(dst)
	dst = append(dst, 0, 0, 0, 0, 0, 0, 0, 0)
	dst = binary.AppendUvarint(dst, uint64(len(d.name)))
	dst = append(dst, d.name...)
	dst = binary.LittleEndian.AppendUint64(dst, uint64(d.mod))
	dst = binary.AppendUvarint(dst, uint64(d.size))
	dst = binary.AppendUvarint(dst, uint64(len(d.keys)))
	prev := uint64(0)
	for _, k := range d.keys {
		dst = binary.AppendUvarint(dst, k-prev)
		prev = k
	}
	body := dst[start+8:]
	binary.LittleEndian.PutUint32(dst[start:], uint32(len(body)))
	binary.LittleEndian.PutUint32(dst[start+4:], crc32.Checksum(body, crcTable))
	return dst
}

func decodeLogEntry(b []byte) (*builtDoc, bool) {
	n, b, ok := takeUvarint(b)
	if !ok || n > uint64(len(b)) {
		return nil, false
	}
	d := &builtDoc{}
	d.name, b = string(b[:n]), b[n:]
	if len(b) < 8 {
		return nil, false
	}
	d.mod = int64(binary.LittleEndian.Uint64(b))
	size, b, ok := takeUvarint(b[8:])
	if !ok {
		return nil, false
	}
	d.size = int64(size)
	count, b, ok := takeUvarint(b)
	if !ok || count > uint64(len(b)) {
		return nil, false
	}
	d.keys = make([]uint64, count)
	key := uint64(0)
	for i := range d.keys {
		var gap uint64
		if gap, b, ok = takeUvarint(b); !ok || (i > 0 && gap == 0) {
			return nil, false
		}
		key += gap
		d.keys[i] = key
	}
	return d, len(b) == 0
}

// searchFilter is the index asked one query: skip says which files a search need not read.
type searchFilter struct {
	snap    *searchSnap
	clauses []gramClause
	cand    []bitset // per segment: the files that can hold every word
}

// skip reports whether the file at name (relative to the data dir, with slashes), whose stat is st now, cannot hold
// every word of the query. False for a file the index did not read as it is now.
func (f *searchFilter) skip(name string, st fileTime) bool {
	if d := f.snap.log[name]; d != nil {
		return d.mod == st.mod && d.size == st.size && !keysMay(d.keys, f.clauses)
	}
	r, ok := f.snap.where[name]
	return ok && r.mod == st.mod && r.size == st.size && !f.cand[r.seg].has(r.id)
}

// keysMay reports whether a file with these keys (sorted) can hold every clause's word.
func keysMay(keys []uint64, clauses []gramClause) bool {
	for _, c := range clauses {
		if len(c.all) == 0 {
			i := sort.Search(len(keys), func(i int) bool { return keys[i] >= c.lo })
			if i == len(keys) || keys[i] >= c.hi {
				return false
			}
			continue
		}
		for _, k := range c.all {
			if _, ok := slices.BinarySearch(keys, k); !ok {
				return false
			}
		}
	}
	return true
}

// filter asks the index the query of words ws (folded, as words returns them). Nil when there is nothing to ask:
// no index, or one that failed a check just now (broken is set and the caller searches without it). follow as for
// snapshot.
func (ix *searchIndex) filter(dir string, ws []string, follow bool) *searchFilter {
	clauses, ok := queryClauses(ws)
	if !ok {
		return nil
	}
	for range 3 { // again when the segments were replaced while they were read
		snap := ix.snapshot(dir, follow)
		if snap.empty() {
			return nil
		}
		f := &searchFilter{snap: snap, clauses: clauses, cand: make([]bitset, len(snap.segs))}
		var err error
		for i, s := range snap.segs {
			if f.cand[i], err = s.candidates(clauses); err != nil {
				break
			}
		}
		if err == nil {
			return f
		}
		if ix.current() != snap {
			continue
		}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errSegmentOther) {
			// The segment is not there, or the file there is another one: the index was merged or built again
			// since snap was read. Nothing was taken from that file; the index as it is now is asked instead.
			ix.reload(dir, snap)
			continue
		}
		if damaged(err) {
			ix.at(hookDamaged, "")
			if !ix.spoil(snap) {
				continue // another index is in use by now: that one is asked
			}
		}
		return nil
	}
	return nil
}

// searchFilter is the index asked the query of ws for this engine's store; nil without one. A damaged index is
// said once, then left alone until the next update builds it again.
func (e *Engine) searchFilter(ws []string) *searchFilter {
	ix := &e.Store.search
	was := ix.broken.Load()
	f := ix.filter(e.Store.searchDir(), ws, e.Store.readOnly)
	if !was && ix.broken.Load() {
		e.logf("search index: a check failed; searching without it until it is built again")
	}
	return f
}

// SearchIndexReady reports whether the archive has a search index: an update has finished since it was started (or
// started again after a check failed), so searches read only what changed since and what can hold their words.
func (s *Store) SearchIndexReady() bool {
	ix := &s.search
	ix.snapshot(s.searchDir(), s.readOnly)
	return ix.ready.Load() && !ix.broken.Load()
}

// SearchIndexWanted reports whether an update has more to do than follow the files as they change: the index has
// not been through every file yet, a check of it failed, or text was deleted on purpose (SearchIndexPurge).
func (s *Store) SearchIndexWanted() bool { return s.search.purge.Load() || !s.SearchIndexReady() }

// SearchIndexPurge has the next update drop at once every entry that is not of a file as it is now: called after
// text was deleted on purpose (a source forgotten, retention, a recording or project deleted), so the index does
// not keep what the knowledge base no longer has.
func (s *Store) SearchIndexPurge() { s.search.purge.Store(true) }

// writeIndexFile replaces path with data through a temporary file, synced before it is renamed. A segment is synced
// before the manifest that names it is written (segWriter.finish), so the manifest's bytes and those of every
// segment it names are written out before it takes the manifest's place. The directory itself is not synced: after
// a power loss the manifest may be the one before (whose files the sweep may have removed already), or may name a
// segment whose directory entry was lost. Both are a manifest naming a file that is not there, which whoever reads
// it finds out (readIndex) and the next update answers by building the index again. The log is not synced at all:
// it is read up to its last whole entry.
func writeIndexFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	err = f.Chmod(0o600)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// indexOwned reports whether name is a file the index wrote (so one it may delete).
func indexOwned(name string) bool {
	return name == indexManifest || strings.HasPrefix(name, indexManifest+".tmp-") ||
		strings.HasPrefix(name, "seg-") && (strings.HasSuffix(name, ".idx") || strings.HasSuffix(name, ".tmp")) ||
		strings.HasPrefix(name, "log-") && strings.HasSuffix(name, ".log")
}

// SearchIndexFileKind is the name of the file of the index at path, without its directory and without what is new
// in the name every time a file is written: a segment's or a log's number and id (indexUpdate.name), a temporary
// file's ending (writeIndexFile). Whoever logs an update's failures tells by it whether the file one failed at is
// the kind of file the last one failed at. Any other name is returned as it is.
func SearchIndexFileKind(path string) string {
	name := filepath.Base(path)
	if base, _, ok := strings.Cut(name, ".tmp-"); ok {
		return base + ".tmp-*"
	}
	for _, kind := range []string{"seg-", "log-"} {
		if rest, ok := strings.CutPrefix(name, kind); ok {
			if i := strings.IndexByte(rest, '.'); i >= 0 {
				return kind + "*" + rest[i:]
			}
			return kind + "*"
		}
	}
	return name
}

// cloneWhere copies where for a new snapshot that adds seg (at index i) to it.
func cloneWhere(where map[string]docRef, i int, seg *indexSeg) map[string]docRef {
	out := maps.Clone(where)
	if out == nil {
		out = make(map[string]docRef, len(seg.docs))
	}
	for id, d := range seg.docs {
		out[d.name] = docRef{seg: int32(i), id: uint32(id), mod: d.mod, size: d.size}
	}
	return out
}
