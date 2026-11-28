package transcript

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"maps"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// File incrementally parses a transcript file. Safe for concurrent use.
type File struct {
	Path string

	mu      sync.Mutex
	offset  int64
	lines   int
	partial []byte
	items   []proto.Item
	meta    Meta
	// History identity (Span.History): the file's inode, a hash of its first line, and how many times it was found
	// rewritten while this process read it.
	ino       uint64
	firstHash uint32
	gen       int
	readMtime time.Time // modification time corresponding to the bytes read
	// tree: the transcript's branches (branch.go): lines on an abandoned one are not kept as items.
	tree *branchTree
	// Raw offsets for cut items and positional sources. Full reads the original bytes even if a same-size rewrite
	// left the cached page looking current.
	refs map[int]lineRef
	// A big file opened cold is parsed from a window at its end (window.go): lines 1..base are not parsed yet, win
	// says where they are, and meta covers only the parsed lines (partialMeta) until Meta works it out for the file.
	base        int
	win         *window
	partialMeta bool
	coldInputs  *coldInputStructure
}

type lineRef struct {
	off int64
	n   int
}

// Span says where a batch of items sits in the transcript, so a client can tell whether it continues what it has.
// History names this version of the file: it changes when the file is replaced or rewritten (seqs start over), and
// stays the same across node restarts. From is the line the batch continues from (it holds lines From+1..To), To the
// last line it accounts for — lines without items (titles, modes, attachments) count too, so To is often past the last
// item's seq.
type Span struct {
	History  string
	From, To int
}

func NewFile(path string) *File { return &File{Path: path} }

// Where is the path the file is read from now.
func (f *File) Where() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Path
}

// Relocate points the file at where its transcript is now: Codex moves a rollout between sessions/ and
// archived_sessions/ when a thread is archived or restored. What was read stays; reading goes on from there (a moved
// file keeps its inode, so Refresh does not start over).
func (f *File) Relocate(path string) {
	f.mu.Lock()
	f.Path = path
	f.mu.Unlock()
}

// NewSubagentFile reads a subagent's transcript (<session>/subagents/agent-<id>.jsonl, every line a sidechain).
func NewSubagentFile(path string) *File { return &File{Path: path, meta: Meta{Sidechain: true}} }

func (f *File) history() string {
	if f.lines == 0 {
		return "" // nothing read yet: no identity to compare
	}
	version := ".items2" // Re-read Codex pages whose user replies omitted the quoted question.
	if !f.meta.Codex {
		version = ".items6" // Reload Claude folds whose latest command description was omitted.
	}
	return fmt.Sprintf("%x.%08x.%d", f.ino, f.firstHash, f.gen) + version + f.branchToken()
}

// Refresh reads newly appended complete lines and returns the new items.
func (f *File) Refresh() ([]proto.Item, error) {
	fresh, _, err := f.RefreshSpan()
	return fresh, err
}

// RefreshSpan is Refresh that also says which lines it read (From == To: none).
func (f *File) RefreshSpan() ([]proto.Item, Span, error) {
	return f.refreshSpan(false)
}

// RefreshExportSpan opens a readable local file even for a cached view. A
// same-size edit with a different modification time invalidates the cache.
func (f *File) RefreshExportSpan() ([]proto.Item, Span, error) {
	return f.refreshSpan(true)
}

func (f *File) refreshSpan(strict bool) ([]proto.Item, Span, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fresh, span, err := f.refresh(strict)
	if err != nil || f.tree == nil || len(f.tree.revoked) == 0 {
		return fresh, span, err
	}
	// Claude went on from a turn the node took for rewound (RewoundAt): it was not. Its items are gone from those kept,
	// so the file is read again from the start without it, under a new token (the clients start over, as for a file
	// rewritten).
	manual := map[string]bool{}
	for u := range f.tree.manual {
		manual[u] = true
	}
	for _, u := range f.tree.revoked {
		delete(manual, u)
	}
	f.offset, f.lines, f.partial, f.items, f.refs, f.meta = 0, 0, nil, nil, nil, Meta{Sidechain: f.meta.Sidechain}
	f.base, f.win, f.partialMeta, f.firstHash = 0, nil, false, 0
	f.coldInputs = nil
	f.tree = &branchTree{manual: manual, marked: f.tree.marked}
	f.gen++
	return f.refresh(strict)
}

// freshTree is a new tree for the file read again from somewhere: the roots the node set stay (none after a file
// replaced, whose tree is nil), with the lines that were under them then (branchTree.marked).
func (f *File) freshTree() *branchTree {
	if f.tree == nil {
		return &branchTree{}
	}
	t := f.tree.kept()
	return &t
}

// Changed reports whether RefreshSpan may have something to do: the file's size or inode is not what was read, it
// cannot be stat'ed (RefreshSpan then says why), or a turn taken for rewound has to be read again (RefreshSpan). A
// watch asks on every tick, so it never waits for the file's lock (a read or a page under way): it says true then.
func (f *File) Changed() bool {
	if !f.mu.TryLock() {
		return true
	}
	path, offset, ino := f.Path, f.offset, f.ino
	revoked := f.tree != nil && len(f.tree.revoked) > 0
	f.mu.Unlock()
	if revoked {
		return true
	}
	st, err := statTranscript(path)
	return err != nil || !unchanged(st, offset, ino)
}

// unchanged: a file stat'ed as st is the one read up to offset (same inode), with nothing after it.
func unchanged(st os.FileInfo, offset int64, ino uint64) bool {
	return st.Size() == offset && inode(st) == ino
}

func (f *File) refresh(strict bool) ([]proto.Item, Span, error) {
	// A file with nothing new is the common case of a watched session at rest (a refresh four times a second): a stat
	// says so without parsing. Windows also reads the file index through a metadata handle. A failed stat goes on
	// to the open, which reports it, unless an export requires a readable regular file immediately.
	if st, err := statTranscript(f.Path); strict {
		if err != nil {
			return nil, Span{}, err
		}
		if !st.Mode().IsRegular() {
			return nil, Span{}, errors.New("transcript source is not a regular file")
		}
	} else if err == nil && unchanged(st, f.offset, f.ino) {
		return nil, Span{f.history(), f.lines, f.lines}, nil
	}
	fh, err := openTranscript(f.Path)
	if err != nil {
		return nil, Span{}, err
	}
	defer fh.Close()
	st, err := statOpenedTranscript(fh)
	if err != nil {
		return nil, Span{}, err
	}
	if strict && !st.Mode().IsRegular() {
		return nil, Span{}, errors.New("transcript source is not a regular file")
	}
	ino := inode(st)
	edited := strict && st.Size() == f.offset && !f.readMtime.IsZero() && !st.ModTime().Equal(f.readMtime)
	if edited || st.Size() < f.offset || (f.ino != 0 && ino != 0 && ino != f.ino) { // rewritten or replaced: start over
		f.offset, f.lines, f.partial, f.items, f.refs, f.meta, f.tree = 0, 0, nil, nil, nil, Meta{Sidechain: f.meta.Sidechain}, nil
		f.base, f.win, f.partialMeta = 0, nil, false
		f.coldInputs = nil
		f.firstHash = 0
		f.readMtime = time.Time{}
		f.gen++
	}
	f.ino = ino
	from := f.lines
	if st.Size() == f.offset {
		f.readMtime = st.ModTime()
		return nil, Span{f.history(), from, from}, nil
	}
	if f.offset == 0 && st.Size() > 2*windowBytes {
		// A big file read cold: its end only (the batch continues from the lines not parsed).
		fresh, ok, err := f.openWindow(fh, st, strict)
		if err != nil {
			return nil, Span{}, err
		}
		if ok {
			f.readMtime = st.ModTime()
			return fresh, Span{f.history(), f.base, f.lines}, nil
		}
	}
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return nil, Span{}, err
	}
	// Read straight after the partial line into a buffer sized from the stat (bytes.MinRead spare so the EOF read does
	// not grow it): one copy of the new bytes instead of io.ReadAll's doublings plus an append. A file still growing
	// is read to EOF all the same.
	buf := bytes.NewBuffer(make([]byte, 0, len(f.partial)+int(st.Size()-f.offset)+bytes.MinRead))
	buf.Write(f.partial)
	if _, err := buf.ReadFrom(fh); err != nil {
		return nil, Span{}, err
	}
	data := buf.Bytes()
	f.offset += int64(len(data) - len(f.partial))
	f.seeNotifications(data)
	pos := f.offset - int64(len(data)) // where data starts in the file
	var fresh []proto.Item
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line, at := data[:i], pos
		data, pos = data[i+1:], pos+int64(i)+1
		f.lines++
		if f.lines == 1 {
			f.firstHash = crc32.ChecksumIEEE(line)
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		items := ParseLine(f.lines, line, &f.meta)
		if f.tree == nil {
			f.tree = &branchTree{}
		}
		if trackLine(f.tree, &f.meta, items, &f.items, &fresh) { // on a branch that was gone back over (branch.go)
			continue
		}
		// New items go out with their images (subscribers that did not ask for lazy images get them whole); the
		// stored copy leaves them out.
		for _, it := range items {
			sent, _ := light(it, true)
			kept, cut := light(it, false)
			fresh = append(fresh, sent)
			f.items = append(f.items, kept)
			if cut || it.SourceFingerprint != "" {
				f.rememberLine(f.lines, lineRef{at, i})
			}
		}
	}
	f.partial = append([]byte(nil), data...)
	if st.Size() != f.offset { // a writer appended while the read was in progress
		if latest, err := fh.Stat(); err == nil && latest.Size() == f.offset {
			st = latest
		}
	}
	if st.Size() == f.offset {
		f.readMtime = st.ModTime()
	}
	return fresh, Span{f.history(), from, f.lines}, nil
}

// before returns the index of the first item with seq >= beforeSeq (0 = len, the end). Callers hold f.mu.
func (f *File) before(beforeSeq int) int {
	if beforeSeq <= 0 {
		return len(f.items)
	}
	return sort.Search(len(f.items), func(i int) bool { return f.items[i].Seq >= beforeSeq })
}

// pageSpan is the span of items[start:end] counted back from beforeSeq. Callers hold f.mu.
func (f *File) pageSpan(start, beforeSeq int) Span {
	sp := Span{History: f.history(), To: f.lines}
	if beforeSeq > 0 {
		sp.To = beforeSeq - 1
	}
	if (start > 0 || f.base > 0) && start < len(f.items) {
		sp.From = f.items[start].Seq - 1
	}
	return sp
}

// Page returns items with seq < beforeSeq (0 = from the end), newest `limit`, in ascending order.
func (f *File) Page(beforeSeq, limit int) ([]proto.Item, bool, Span) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureItems(beforeSeq, limit)
	end := f.before(beforeSeq)
	start := max(end-limit, 0)
	// Never split a transcript line across pages.
	for start > 0 && f.items[start].Seq == f.items[start-1].Seq {
		start--
	}
	out := append([]proto.Item(nil), f.items[start:end]...)
	return out, start > 0 || f.base > 0, f.pageSpan(start, beforeSeq)
}

// PageTurns returns the items before beforeSeq (0 = the end) going back far enough to hold `turns` whole exchanges:
// it starts at a user question, not an injected supervision/wake prompt. maxItems caps one enormous turn.
func (f *File) PageTurns(beforeSeq, turns, maxItems int) ([]proto.Item, bool, Span) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureTurns(beforeSeq, turns, maxItems)
	end := f.before(beforeSeq)
	start, seen := end, 0
	for start > 0 && seen < turns && end-start < maxItems {
		start--
		if pagePrompt(f.items[start]) {
			seen++
		}
	}
	for start > 0 && f.items[start].Seq == f.items[start-1].Seq { // never split a transcript line across pages
		start--
	}
	out := append([]proto.Item(nil), f.items[start:end]...)
	return out, start > 0 || f.base > 0, f.pageSpan(start, beforeSeq)
}

// rememberLine keeps a raw source location while the caller holds f.mu.
func (f *File) rememberLine(seq int, ref lineRef) {
	if f.refs == nil {
		f.refs = map[int]lineRef{}
	}
	f.refs[seq] = ref
}

// ErrSourceChanged means a positional item is absent or its record no longer matches the cached item.
var ErrSourceChanged = errors.New("original transcript item no longer present")

// Full returns an item whole: all of its text and its image data (pages and pushes carry a light copy). The line it
// came from is read and parsed again; if that no longer gives the item (the file changed under it), it says so
// rather than pass the cut copy off as whole.
func (f *File) Full(id string) (proto.Item, error) {
	f.mu.Lock()
	it, found := f.item(id)
	var err error
	if !found { // an item in lines not parsed yet: parse back to it (window.go)
		it, found, err = f.findOlder(id)
	}
	path := f.Path
	ref, reread := f.refs[it.Seq]
	scratch := Meta{Sidechain: f.meta.Sidechain, Codex: f.meta.Codex,
		agentCalls: maps.Clone(f.meta.agentCalls), taskCalls: maps.Clone(f.meta.taskCalls)}
	f.mu.Unlock()
	if !found {
		if proto.PositionalItemID(id) {
			if err != nil {
				return it, err
			}
			return it, ErrSourceChanged
		}
		return it, fmt.Errorf("no item %s", id)
	}
	if !reread {
		if it.SourceFingerprint != "" {
			return it, ErrSourceChanged
		}
		return it, nil
	}
	parsed, err := f.reparse(path, it.Seq, ref, &scratch)
	if it.SourceFingerprint != "" && err != nil && !errors.Is(err, io.EOF) {
		return it, err
	}
	full, ok := parsed[id]
	if it.SourceFingerprint != "" && (!ok || full.SourceFingerprint != it.SourceFingerprint) {
		return it, ErrSourceChanged
	}
	if !ok {
		return it, fmt.Errorf("item %s: line %d of the transcript changed, reload the chat", id, it.Seq)
	}
	it.Text, it.FullSize, it.Image = full.Text, 0, full.Image
	it.QuestionReplies, it.QuestionRepliesCut = full.QuestionReplies, false
	return it, nil
}

// item is the parsed item with this id. Callers hold f.mu.
func (f *File) item(id string) (proto.Item, bool) {
	for i := len(f.items) - 1; i >= 0; i-- {
		if f.items[i].ID == id {
			return f.items[i], true
		}
	}
	return proto.Item{}, false
}

// WithImages puts the image data back into items (for callers that did not ask for lazy images).
func (f *File) WithImages(items []proto.Item) []proto.Item {
	var out []proto.Item
	parsed := map[int]map[string]proto.Item{}
	for i, it := range items {
		if it.Image == nil || !it.Image.Lazy {
			continue
		}
		f.mu.Lock()
		path := f.Path
		ref, cut := f.refs[it.Seq]
		scratch := Meta{Sidechain: f.meta.Sidechain, Codex: f.meta.Codex}
		f.mu.Unlock()
		if !cut {
			continue
		}
		if parsed[it.Seq] == nil {
			parsed[it.Seq], _ = f.reparse(path, it.Seq, ref, &scratch)
		}
		if full, ok := parsed[it.Seq][it.ID]; ok && full.Image != nil {
			if out == nil {
				out = append([]proto.Item(nil), items...)
			}
			out[i].Image = full.Image
		}
	}
	if out == nil {
		return items
	}
	return out
}

// reparse reads line seq again from path and returns its items by id.
func (f *File) reparse(path string, seq int, ref lineRef, scratch *Meta) (map[string]proto.Item, error) {
	fh, err := openTranscript(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	line := make([]byte, ref.n)
	if _, err := fh.ReadAt(line, ref.off); err != nil {
		return nil, err
	}
	out := map[string]proto.Item{}
	for _, it := range ParseLine(seq, line, scratch) {
		out[it.ID] = it
	}
	return out, nil
}

// After returns items with seq > afterSeq, oldest first: at least limit of them unless fewer exist, and never part of
// a line (a Codex tool call is its call and its result on one line; a client resuming from the last seq it got would
// never see the rest). more says items are left after the last one returned. The span runs from afterSeq to the last
// line read when nothing is left, else to the last item's line.
func (f *File) After(afterSeq, limit int) (items []proto.Item, more bool, span Span) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensureAfter(afterSeq)
	span = Span{History: f.history(), From: afterSeq, To: max(f.lines, afterSeq)}
	i := 0
	for i < len(f.items) && f.items[i].Seq <= afterSeq {
		i++
	}
	end := min(i+limit, len(f.items))
	for end > i && end < len(f.items) && f.items[end].Seq == f.items[end-1].Seq {
		end++
	}
	if end == i {
		return nil, false, span
	}
	more = end < len(f.items)
	if more {
		span.To = f.items[end-1].Seq
	}
	return append([]proto.Item(nil), f.items[i:end]...), more, span
}

// Meta is the session-level information of the whole file (for a file opened by its end, worked out on first call).
func (f *File) Meta() Meta { return f.fullMeta() }

func (f *File) LastSeq() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lines
}

// End identifies the last complete line read from the file and the history its sequence counts in.
func (f *File) End() Span {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Span{History: f.history(), To: f.lines}
}

// LoadedItems returns the items already read from the current history without opening older pages.
func (f *File) LoadedItems() ([]proto.Item, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Item(nil), f.items...), f.history()
}

// TurnClosedAfter reports whether a non-hidden turn close occurred after seq in the lines f has parsed.
func (f *File) TurnClosedAfter(seq int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.meta.TurnClosed && f.meta.turnEndSeq > seq && (f.tree == nil || !f.tree.hidden[f.meta.turnEndLine])
}

// CoversClosedTurnAfter reports whether the checked end still covers every line after seq in history and the latest
// turn ended after the send. An open turn or a closing record on a discarded branch cannot establish that.
func (f *File) CoversClosedTurnAfter(end Span, history string, seq int, sentAt time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.meta.TurnClosed || f.meta.turnEndSeq <= seq || !f.meta.turnEndedAt.After(sentAt) ||
		f.tree != nil && f.tree.hidden[f.meta.turnEndLine] {
		return false
	}
	if history == "" || history != end.History || end.History != f.history() || end.To != f.lines || f.base > seq || seq > f.lines || len(f.partial) != 0 {
		return false
	}
	fh, err := openTranscript(f.Path)
	if err != nil {
		return false
	}
	defer fh.Close()
	// A rewrite that grew the same inode can look like an append to the incremental parser.
	first, err := bufio.NewReaderSize(fh, 64<<10).ReadSlice('\n')
	if err != nil || crc32.ChecksumIEEE(first[:len(first)-1]) != f.firstHash {
		return false
	}
	st, err := statTranscript(f.Path)
	return err == nil && unchanged(st, f.offset, f.ino)
}
