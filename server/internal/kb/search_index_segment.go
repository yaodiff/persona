package kb

import (
	"bufio"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"math/bits"
	"os"
	"slices"
	"sort"
	"strings"
)

// A segment is one immutable file of the search index: some files of the data dir (path, mtime, size, each with a
// number), and for every key the numbers of the files that have it. Layout, in the order written:
//
//	"PKBSEG3\n"
//	lists      one per key, in key order: a kind byte, then nothing (every file), a bitmap, or the numbers as
//	           varint gaps
//	files      count; per file the bytes shared with the name before, the rest of its name, mtime, size
//	keys       blocks of segBlockKeys keys: per key its gap to the key before, its list's length and CRC; then
//	           the block's CRC
//	blocks     count; per block its first key (as a gap), its length and the length of its keys' lists
//	footer     where the four parts are, the segment's id, how many files, the CRCs of files and blocks, its
//	           own CRC
//
// An open segment keeps the files and the blocks part in memory; a search reads the blocks of the keys it asks
// for, then their lists. Every part is checked before it is believed: files and blocks when the segment opens, a
// block of keys and a list when they are read. A segment that fails a check is not used (searchIndex.broken), so a
// damaged one costs time, never a result.
//
// A list is numbers into the files part, so it says something only next to the files part it was written with. A
// segment is therefore never changed once written, its name is never given to another (indexUpdate.name), and its
// footer carries an id drawn at random when it was written: whoever holds a segment's files part in memory checks,
// each time it opens the file to read keys and lists, that the footer there is still the one it read (open). A file
// that is another segment under the same name is not read through this one's files.
const (
	segMagic      = "PKBSEG3\n"
	segFooterSize = 8*8 + 8 + 4*5
	segFooterTag  = 0x33474553 // "SEG3"
	segBlockKeys  = 64

	listEvery  = 0 // every file of the segment
	listBitmap = 1
	listGaps   = 2
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

var errSegment = errors.New("search index segment is damaged")

// errSegmentOther: the file at a segment's path is not the segment that was opened there (another one was written
// under its name, or its footer changed). Whoever holds it reads the index from disk again.
var errSegmentOther = errors.New("search index segment was replaced")

// segDoc is a file as a segment knows it. name is its path relative to the data dir, with slashes.
type segDoc struct {
	name string
	mod  int64 // UnixNano
	size int64
}

// indexSeg is an open segment: its files and where its blocks of keys are in memory, the keys and their lists read
// from the file when asked for.
type indexSeg struct {
	path string
	size int64
	// foot is the file's footer as it was read: what tells this segment from any other (open).
	foot [segFooterSize]byte
	docs []segDoc
	// Block i holds the keys from first[i] up to first[i+1]; it is at keyOff[i]..keyOff[i+1] of the keys part and
	// its lists at listOff[i]..listOff[i+1] of the lists part.
	first   []uint64
	keyOff  []uint64
	listOff []uint64
	lists   int64 // where the lists part starts in the file
	keys    int64 // where the keys part starts
}

// segKey is one key of a segment: where its list is in the lists part, and the list's CRC.
type segKey struct {
	key        uint64
	start, end uint64
	crc        uint32
}

// segWriter writes a segment: add for each key in ascending order, then finish. The keys part is written to a file
// of its own beside the segment and copied in at the end, so writing holds one block and one list in memory.
type segWriter struct {
	f, kf *os.File
	w, kw *bufio.Writer
	docs  []segDoc
	id    [8]byte
	n     uint64 // bytes of lists written
	nkeys uint64 // bytes of the keys part written

	block      []byte // the block being filled: its keys so far
	inBlock    int
	blockFirst uint64
	blockLists uint64 // bytes of its keys' lists
	last       uint64 // the last key added
	any        bool

	blocks    []byte // the blocks part
	nblocks   uint64
	prevFirst uint64

	buf []byte
	err error
}

// newSegWriter starts a segment at path (created private, failing when it exists) for docs, which add's numbers
// index.
func newSegWriter(path string, docs []segDoc) (*segWriter, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	kf, err := os.OpenFile(path+".tmp", os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	w := &segWriter{f: f, kf: kf, w: bufio.NewWriterSize(f, 1<<18), kw: bufio.NewWriterSize(kf, 1<<16), docs: docs}
	_, _ = rand.Read(w.id[:]) // never fails
	_, w.err = w.w.WriteString(segMagic)
	return w, nil
}

// add writes key's list: ids are the numbers of the files that have it, ascending and without repeats.
func (w *segWriter) add(key uint64, ids []uint32) {
	if w.err != nil || len(ids) == 0 {
		return
	}
	if w.any && key <= w.last {
		w.err = fmt.Errorf("search index: keys out of order")
		return
	}
	buf := w.buf[:0]
	bitmap := (len(w.docs) + 7) / 8
	switch {
	case len(ids) == len(w.docs):
		buf = append(buf, listEvery)
	case len(ids) >= bitmap: // a gap is at least a byte: the bitmap is no larger
		buf = append(buf, listBitmap)
		buf = slices.Grow(buf, bitmap)[:1+bitmap]
		clear(buf[1:])
		for _, id := range ids {
			buf[1+id/8] |= 1 << (id % 8)
		}
	default:
		buf = append(buf, listGaps)
		prev := uint32(0)
		for i, id := range ids {
			gap := id - prev
			if i > 0 && gap == 0 {
				w.err = fmt.Errorf("search index: a file twice in a list")
				return
			}
			buf = binary.AppendUvarint(buf, uint64(gap))
			prev = id
		}
	}
	w.buf = buf
	if _, err := w.w.Write(buf); err != nil {
		w.err = err
		return
	}
	w.n += uint64(len(buf))
	if w.inBlock == 0 {
		w.blockFirst = key
		w.block = binary.AppendUvarint(w.block[:0], key)
	} else {
		w.block = binary.AppendUvarint(w.block, key-w.last)
	}
	w.block = binary.AppendUvarint(w.block, uint64(len(buf)))
	w.block = binary.LittleEndian.AppendUint32(w.block, crc32.Checksum(buf, crcTable))
	w.blockLists += uint64(len(buf))
	w.last, w.any = key, true
	if w.inBlock++; w.inBlock == segBlockKeys {
		w.endBlock()
	}
}

// endBlock writes the block being filled, if it has a key.
func (w *segWriter) endBlock() {
	if w.inBlock == 0 || w.err != nil {
		return
	}
	w.block = binary.LittleEndian.AppendUint32(w.block, crc32.Checksum(w.block, crcTable))
	if _, err := w.kw.Write(w.block); err != nil {
		w.err = err
		return
	}
	w.blocks = binary.AppendUvarint(w.blocks, w.blockFirst-w.prevFirst)
	w.blocks = binary.AppendUvarint(w.blocks, uint64(len(w.block)))
	w.blocks = binary.AppendUvarint(w.blocks, w.blockLists)
	w.nkeys += uint64(len(w.block))
	w.prevFirst, w.nblocks = w.blockFirst, w.nblocks+1
	w.inBlock, w.blockLists = 0, 0
}

// finish writes the files, the keys, the blocks and the footer, and makes the segment durable. On an error the
// segment is removed.
func (w *segWriter) finish() (err error) {
	defer func() {
		if cerr := w.f.Close(); err == nil {
			err = cerr
		}
		w.kf.Close()
		_ = os.Remove(w.kf.Name())
		if err != nil {
			_ = os.Remove(w.f.Name())
		}
	}()
	w.endBlock()
	if w.err != nil {
		return w.err
	}
	docs := binary.AppendUvarint(nil, uint64(len(w.docs)))
	prev := ""
	for _, d := range w.docs {
		shared := 0
		for shared < len(prev) && shared < len(d.name) && prev[shared] == d.name[shared] {
			shared++
		}
		docs = binary.AppendUvarint(docs, uint64(shared))
		docs = binary.AppendUvarint(docs, uint64(len(d.name)-shared))
		docs = append(docs, d.name[shared:]...)
		docs = binary.LittleEndian.AppendUint64(docs, uint64(d.mod))
		docs = binary.AppendUvarint(docs, uint64(d.size))
		prev = d.name
	}
	if _, err := w.w.Write(docs); err != nil {
		return err
	}
	if err := w.kw.Flush(); err != nil {
		return err
	}
	if _, err := w.kf.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if n, err := io.Copy(w.w, w.kf); err != nil || uint64(n) != w.nkeys {
		return cmp.Or(err, io.ErrUnexpectedEOF)
	}
	blocks := append(binary.AppendUvarint(nil, w.nblocks), w.blocks...)
	foot := make([]byte, 0, segFooterSize)
	off := uint64(len(segMagic))
	for _, n := range []uint64{w.n, uint64(len(docs)), w.nkeys, uint64(len(blocks))} {
		foot = binary.LittleEndian.AppendUint64(foot, off)
		foot = binary.LittleEndian.AppendUint64(foot, n)
		off += n
	}
	foot = append(foot, w.id[:]...)
	foot = binary.LittleEndian.AppendUint32(foot, uint32(len(w.docs)))
	foot = binary.LittleEndian.AppendUint32(foot, crc32.Checksum(docs, crcTable))
	foot = binary.LittleEndian.AppendUint32(foot, crc32.Checksum(blocks, crcTable))
	foot = binary.LittleEndian.AppendUint32(foot, segFooterTag)
	foot = binary.LittleEndian.AppendUint32(foot, crc32.Checksum(foot, crcTable))
	for _, part := range [][]byte{blocks, foot} {
		if _, err := w.w.Write(part); err != nil {
			return err
		}
	}
	if err := w.w.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

// openSeg reads the segment at path: its files and blocks part. Any part that does not check out is errSegment.
func openSeg(path string) (*indexSeg, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s, err := readSeg(f, fi.Size())
	if err != nil {
		return nil, err
	}
	s.path = path
	return s, nil
}

// readSeg reads a segment of size bytes from f, all of it through f: the footer, the files and the blocks are one
// file's.
func readSeg(f io.ReaderAt, size int64) (*indexSeg, error) {
	if size < int64(len(segMagic))+segFooterSize {
		return nil, errSegment
	}
	head := make([]byte, len(segMagic))
	if _, err := f.ReadAt(head, 0); err != nil || string(head) != segMagic {
		return nil, errSegment
	}
	s := &indexSeg{size: size}
	foot := s.foot[:]
	if _, err := f.ReadAt(foot, size-segFooterSize); err != nil {
		return nil, errSegment
	}
	if crc32.Checksum(foot[:segFooterSize-4], crcTable) != binary.LittleEndian.Uint32(foot[segFooterSize-4:]) ||
		binary.LittleEndian.Uint32(foot[segFooterSize-8:]) != segFooterTag {
		return nil, errSegment
	}
	// The four parts follow one another from the magic to the footer.
	var offs, lens [4]uint64
	next := uint64(len(segMagic))
	for i := range offs {
		offs[i], lens[i] = binary.LittleEndian.Uint64(foot[16*i:]), binary.LittleEndian.Uint64(foot[16*i+8:])
		if offs[i] != next || lens[i] > uint64(size) {
			return nil, errSegment
		}
		next += lens[i]
	}
	if next != uint64(size-segFooterSize) {
		return nil, errSegment
	}
	read := func(part int, crc uint32) ([]byte, error) {
		buf := make([]byte, lens[part])
		if _, err := f.ReadAt(buf, int64(offs[part])); err != nil || crc32.Checksum(buf, crcTable) != crc {
			return nil, errSegment
		}
		return buf, nil
	}
	docs, err := read(1, binary.LittleEndian.Uint32(foot[76:]))
	if err != nil {
		return nil, err
	}
	blocks, err := read(3, binary.LittleEndian.Uint32(foot[80:]))
	if err != nil {
		return nil, err
	}
	s.lists, s.keys = int64(offs[0]), int64(offs[2])
	if s.docs, err = readSegDocs(docs, binary.LittleEndian.Uint32(foot[72:])); err != nil {
		return nil, err
	}
	if err := s.readBlocks(blocks, lens[2], lens[0]); err != nil {
		return nil, err
	}
	return s, nil
}

// readSegDocs decodes the files part. The names share one string, so a segment's names cost their bytes once.
func readSegDocs(b []byte, want uint32) ([]segDoc, error) {
	n, b, ok := takeUvarint(b)
	if !ok || n != uint64(want) || n > uint64(len(b)) {
		return nil, errSegment
	}
	type span struct{ start, end int }
	spans := make([]span, 0, n)
	docs := make([]segDoc, n)
	var names strings.Builder
	prev := span{}
	for i := range docs {
		var shared, rest uint64
		if shared, b, ok = takeUvarint(b); !ok {
			return nil, errSegment
		}
		if rest, b, ok = takeUvarint(b); !ok || rest > uint64(len(b)) || shared > uint64(prev.end-prev.start) {
			return nil, errSegment
		}
		cur := span{start: names.Len()}
		// The shared bytes are written again: a name is then one run of the string, whatever came before it.
		names.WriteString(names.String()[prev.start : prev.start+int(shared)])
		names.Write(b[:rest])
		b = b[rest:]
		cur.end = names.Len()
		if len(b) < 8 {
			return nil, errSegment
		}
		docs[i].mod = int64(binary.LittleEndian.Uint64(b))
		var size uint64
		if size, b, ok = takeUvarint(b[8:]); !ok {
			return nil, errSegment
		}
		docs[i].size = int64(size)
		spans = append(spans, cur)
		prev = cur
	}
	if len(b) != 0 {
		return nil, errSegment
	}
	all := names.String()
	for i, sp := range spans {
		docs[i].name = all[sp.start:sp.end]
	}
	return docs, nil
}

// readBlocks decodes the blocks part: the blocks fill the keys part (keysLen bytes) and their lists the lists part.
func (s *indexSeg) readBlocks(b []byte, keysLen, listsLen uint64) error {
	n, b, ok := takeUvarint(b)
	if !ok || n > uint64(len(b)) {
		return errSegment
	}
	s.first, s.keyOff, s.listOff = make([]uint64, n), make([]uint64, n+1), make([]uint64, n+1)
	first, keys, lists := uint64(0), uint64(0), uint64(0)
	for i := range s.first {
		var gap, size, list uint64
		if gap, b, ok = takeUvarint(b); !ok || (i > 0 && gap == 0) {
			return errSegment
		}
		if size, b, ok = takeUvarint(b); !ok || size < 4 || size > keysLen-keys {
			return errSegment
		}
		if list, b, ok = takeUvarint(b); !ok || list == 0 || list > listsLen-lists {
			return errSegment
		}
		first, keys, lists = first+gap, keys+size, lists+list
		s.first[i], s.keyOff[i+1], s.listOff[i+1] = first, keys, lists
	}
	if len(b) != 0 || keys != keysLen || lists != listsLen {
		return errSegment
	}
	return nil
}

func takeUvarint(b []byte) (uint64, []byte, bool) {
	v, n := binary.Uvarint(b)
	if n <= 0 {
		return 0, b, false
	}
	return v, b[n:], true
}

// blockOf is the block that holds key if the segment has it: the last one whose first key is not after it; -1 when
// every key of the segment is after it.
func (s *indexSeg) blockOf(key uint64) int {
	return sort.Search(len(s.first), func(i int) bool { return s.first[i] > key }) - 1
}

// readKeys appends the keys of blocks [i, j) to dst, read from f (the segment's file) in one read and checked.
func (s *indexSeg) readKeys(f io.ReaderAt, i, j int, dst []segKey) ([]segKey, error) {
	if i >= j {
		return dst, nil
	}
	base := s.keyOff[i]
	buf := make([]byte, s.keyOff[j]-base)
	if _, err := f.ReadAt(buf, s.keys+int64(base)); err != nil {
		return dst, errSegment
	}
	for b := i; b < j; b++ {
		block := buf[s.keyOff[b]-base : s.keyOff[b+1]-base]
		body := block[:len(block)-4]
		if crc32.Checksum(body, crcTable) != binary.LittleEndian.Uint32(block[len(body):]) {
			return dst, errSegment
		}
		key, pos := uint64(0), s.listOff[b]
		for n := 0; len(body) > 0; n++ {
			gap, rest, ok := takeUvarint(body)
			if !ok || (n > 0 && gap == 0) {
				return dst, errSegment
			}
			size, rest, ok := takeUvarint(rest)
			if !ok || size == 0 || size > s.listOff[b+1]-pos || len(rest) < 4 {
				return dst, errSegment
			}
			if key += gap; n == 0 && key != s.first[b] {
				return dst, errSegment
			}
			dst = append(dst, segKey{key: key, start: pos, end: pos + size, crc: binary.LittleEndian.Uint32(rest)})
			pos += size
			body = rest[4:]
		}
		if pos != s.listOff[b+1] {
			return dst, errSegment
		}
	}
	return dst, nil
}

// readList reads k's list from f (the segment's file).
func (s *indexSeg) readList(f io.ReaderAt, k segKey, buf []byte) ([]byte, error) {
	buf = slices.Grow(buf[:0], int(k.end-k.start))[:k.end-k.start]
	if _, err := f.ReadAt(buf, s.lists+int64(k.start)); err != nil {
		return buf, errSegment
	}
	return buf, nil
}

// bitset is a set of a segment's file numbers.
type bitset []uint64

func newBitset(n int) bitset { return make(bitset, (n+63)/64) }

func (b bitset) set(i uint32)      { b[i/64] |= 1 << (i % 64) }
func (b bitset) has(i uint32) bool { return b[i/64]&(1<<(i%64)) != 0 }

func (b bitset) fill(n int) {
	for i := range b {
		b[i] = ^uint64(0)
	}
	if rem := n % 64; rem != 0 && len(b) > 0 {
		b[len(b)-1] = 1<<rem - 1
	}
}

func (b bitset) count() int {
	n := 0
	for _, w := range b {
		n += bits.OnesCount64(w)
	}
	return n
}

// orList adds the files of an encoded list (checked against crc) to dst.
func (s *indexSeg) orList(dst bitset, list []byte, crc uint32) error {
	if len(list) == 0 || crc32.Checksum(list, crcTable) != crc {
		return errSegment
	}
	switch list[0] {
	case listEvery:
		if len(list) != 1 {
			return errSegment
		}
		dst.fill(len(s.docs))
	case listBitmap:
		raw := list[1:]
		if len(raw) != (len(s.docs)+7)/8 {
			return errSegment
		}
		for i := 0; i+8 <= len(raw); i += 8 {
			dst[i/8] |= binary.LittleEndian.Uint64(raw[i:])
		}
		for i := len(raw) &^ 7; i < len(raw); i++ {
			dst[i/8] |= uint64(raw[i]) << (8 * (i % 8))
		}
	case listGaps:
		id, raw := uint64(0), list[1:]
		for len(raw) > 0 {
			gap, n := binary.Uvarint(raw)
			if n <= 0 {
				return errSegment
			}
			if id += gap; id >= uint64(len(s.docs)) {
				return errSegment
			}
			dst.set(uint32(id))
			raw = raw[n:]
		}
	default:
		return errSegment
	}
	return nil
}

// ids appends the file numbers of an encoded list (checked against crc) to dst, ascending.
func (s *indexSeg) ids(dst []uint32, list []byte, crc uint32) ([]uint32, error) {
	if len(list) == 0 || crc32.Checksum(list, crcTable) != crc {
		return dst, errSegment
	}
	switch list[0] {
	case listEvery:
		for i := range s.docs {
			dst = append(dst, uint32(i))
		}
	case listBitmap:
		raw := list[1:]
		if len(raw) != (len(s.docs)+7)/8 {
			return dst, errSegment
		}
		for i, b := range raw {
			for ; b != 0; b &= b - 1 {
				dst = append(dst, uint32(i*8+bits.TrailingZeros8(b)))
			}
		}
	case listGaps:
		id, raw := uint64(0), list[1:]
		for len(raw) > 0 {
			gap, n := binary.Uvarint(raw)
			if n <= 0 {
				return dst, errSegment
			}
			if id += gap; id >= uint64(len(s.docs)) {
				return dst, errSegment
			}
			dst = append(dst, uint32(id))
			raw = raw[n:]
		}
	default:
		return dst, errSegment
	}
	return dst, nil
}

// open opens the segment's file to read keys and lists from, after checking that it is still the file s was read
// from: its footer, id and checksums, is the one s holds. The file is then read through this handle alone, and a
// segment's file is never written again, so what is read belongs to s.docs. Another file at the path (a segment
// written under the same name, which the index's own writer never does) is errSegmentOther, never an answer.
func (s *indexSeg) open() (*os.File, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	var foot [segFooterSize]byte
	if _, err := f.ReadAt(foot[:], s.size-segFooterSize); err != nil || foot != s.foot {
		f.Close()
		return nil, errSegmentOther
	}
	return f, nil
}

// candidates is the set of the segment's files that can hold every clause's word.
func (s *indexSeg) candidates(clauses []gramClause) (bitset, error) {
	f, err := s.open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.candidatesIn(f, clauses)
}

// candidatesIn is candidates read from f, the segment's file.
func (s *indexSeg) candidatesIn(f io.ReaderAt, clauses []gramClause) (bitset, error) {
	var err error
	none := newBitset(len(s.docs))
	// The keys every file must have, each looked up once; a key the segment does not have settles it.
	var want []uint64
	for _, c := range clauses {
		want = append(want, c.all...)
	}
	slices.Sort(want)
	want = slices.Compact(want)
	var block, exact []segKey
	at := -1
	for _, key := range want {
		b := s.blockOf(key)
		if b < 0 {
			return none, nil
		}
		if b != at {
			if block, err = s.readKeys(f, b, b+1, block[:0]); err != nil {
				return nil, err
			}
			at = b
		}
		i, ok := slices.BinarySearchFunc(block, key, func(k segKey, key uint64) int { return cmp.Compare(k.key, key) })
		if !ok {
			return none, nil
		}
		exact = append(exact, block[i])
	}
	out := newBitset(len(s.docs))
	out.fill(len(s.docs))
	tmp := newBitset(len(s.docs))
	and := func() bool {
		any := uint64(0)
		for i := range out {
			out[i] &= tmp[i]
			any |= out[i]
		}
		return any != 0
	}
	// The shortest lists first: the rarest keys, after which there is often nothing left to look for.
	slices.SortFunc(exact, func(a, b segKey) int { return cmp.Compare(a.end-a.start, b.end-b.start) })
	var list []byte
	for _, k := range exact {
		if list, err = s.readList(f, k, list); err != nil {
			return nil, err
		}
		clear(tmp)
		if err := s.orList(tmp, list, k.crc); err != nil {
			return nil, err
		}
		if !and() {
			return out, nil
		}
	}
	// A word too short for a key of its own: the files with any key in its range.
	for _, c := range clauses {
		if len(c.all) > 0 {
			continue
		}
		i, j := max(0, s.blockOf(c.lo)), s.blockOf(c.hi-1)+1
		if block, err = s.readKeys(f, i, j, block[:0]); err != nil {
			return nil, err
		}
		lo, _ := slices.BinarySearchFunc(block, c.lo, func(k segKey, key uint64) int { return cmp.Compare(k.key, key) })
		hi, _ := slices.BinarySearchFunc(block, c.hi, func(k segKey, key uint64) int { return cmp.Compare(k.key, key) })
		in := block[lo:hi]
		if len(in) == 0 {
			return none, nil
		}
		// Their lists follow one another: one read.
		if list, err = s.readList(f, segKey{start: in[0].start, end: in[len(in)-1].end}, list); err != nil {
			return nil, err
		}
		clear(tmp)
		for _, k := range in {
			if err := s.orList(tmp, list[k.start-in[0].start:k.end-in[0].start], k.crc); err != nil {
				return nil, err
			}
		}
		if !and() {
			return out, nil
		}
	}
	return out, nil
}

// segCursor reads a segment's keys and lists from first to last, a few blocks at a time.
type segCursor struct {
	seg   *indexSeg
	f     *os.File
	lists *bufio.Reader
	keys  []segKey
	pos   int
	next  int // the next block to read
	list  []byte
}

func (s *indexSeg) cursor() (*segCursor, error) {
	f, err := s.open()
	if err != nil {
		return nil, err
	}
	size := int64(s.listOff[len(s.listOff)-1])
	return &segCursor{seg: s, f: f, lists: bufio.NewReaderSize(io.NewSectionReader(f, s.lists, size), 1<<16)}, nil
}

func (c *segCursor) close() { c.f.Close() }

// key is the key the cursor is at; false after the last.
func (c *segCursor) key() (uint64, bool, error) {
	if c.pos == len(c.keys) {
		if c.next == len(c.seg.first) {
			return 0, false, nil
		}
		to := min(len(c.seg.first), c.next+16)
		var err error
		if c.keys, err = c.seg.readKeys(c.f, c.next, to, c.keys[:0]); err != nil {
			return 0, false, err
		}
		c.pos, c.next = 0, to
	}
	return c.keys[c.pos].key, true, nil
}

// take appends the files of the key the cursor is at to dst (their numbers, ascending) and moves to the next key.
func (c *segCursor) take(dst []uint32) ([]uint32, error) {
	k := c.keys[c.pos]
	c.list = slices.Grow(c.list[:0], int(k.end-k.start))[:k.end-k.start]
	if _, err := io.ReadFull(c.lists, c.list); err != nil {
		return dst, errSegment
	}
	c.pos++
	return c.seg.ids(dst, c.list, k.crc)
}

// builtDoc is a file read for the index: what tells it changed, and its keys (sorted, no repeats).
type builtDoc struct {
	segDoc
	keys []uint64
}

// writeSeg writes the segment of docs at path. The files are numbered by name.
func writeSeg(path string, docs []builtDoc) error {
	slices.SortFunc(docs, func(a, b builtDoc) int { return strings.Compare(a.name, b.name) })
	names := make([]segDoc, len(docs))
	total := 0
	for i, d := range docs {
		names[i] = d.segDoc
		total += len(d.keys)
	}
	type pair struct {
		key uint64
		id  uint32
	}
	pairs := make([]pair, 0, total)
	for i, d := range docs {
		for _, k := range d.keys {
			pairs = append(pairs, pair{k, uint32(i)})
		}
	}
	slices.SortFunc(pairs, func(a, b pair) int {
		if a.key != b.key {
			if a.key < b.key {
				return -1
			}
			return 1
		}
		return int(a.id) - int(b.id)
	})
	w, err := newSegWriter(path, names)
	if err != nil {
		return err
	}
	var ids []uint32
	for i := 0; i < len(pairs); {
		j := i
		ids = ids[:0]
		for ; j < len(pairs) && pairs[j].key == pairs[i].key; j++ {
			ids = append(ids, pairs[j].id)
		}
		w.add(pairs[i].key, ids)
		i = j
	}
	return w.finish()
}

// mergeInput is a segment going into a merge and which of its files are kept.
type mergeInput struct {
	seg  *indexSeg
	keep func(id uint32) bool
}

// mergeSegs writes at path the segment of the kept files of every input, and returns how many there are. No file
// may be kept from two inputs. The keys and lists are read and written one after another, so a merge holds one
// key's files in memory, not the segments. When ctx ends it stops, leaving no file.
func mergeSegs(ctx context.Context, path string, inputs []mergeInput) (int, error) {
	type source struct {
		cur   *segCursor
		remap []uint32 // a file's number in the merged segment; dropped: not kept
	}
	const dropped = ^uint32(0)
	type kept struct {
		doc segDoc
		src int
		id  uint32
	}
	var all []kept
	srcs := make([]*source, len(inputs))
	for i, in := range inputs {
		srcs[i] = &source{remap: make([]uint32, len(in.seg.docs))}
		for id, d := range in.seg.docs {
			srcs[i].remap[id] = dropped
			if in.keep == nil || in.keep(uint32(id)) {
				all = append(all, kept{d, i, uint32(id)})
			}
		}
	}
	slices.SortFunc(all, func(a, b kept) int { return strings.Compare(a.doc.name, b.doc.name) })
	docs := make([]segDoc, len(all))
	for n, k := range all {
		if n > 0 && docs[n-1].name == k.doc.name {
			return 0, fmt.Errorf("search index: a file kept from two segments")
		}
		docs[n] = k.doc
		srcs[k.src].remap[k.id] = uint32(n)
	}
	defer func() {
		for _, s := range srcs {
			if s.cur != nil {
				s.cur.close()
			}
		}
	}()
	for i, in := range inputs {
		cur, err := in.seg.cursor()
		if err != nil {
			return 0, err
		}
		srcs[i].cur = cur
	}
	w, err := newSegWriter(path, docs)
	if err != nil {
		return 0, err
	}
	var ids, out []uint32
	for n := 1; w.err == nil; n++ {
		if n%max(1, indexSizes.mergeAsks) == 0 && ctx.Err() != nil {
			w.err = ctx.Err()
			break
		}
		key, any := uint64(0), false
		for _, s := range srcs {
			k, ok, err := s.cur.key()
			if err != nil {
				w.err = err
				break
			}
			if ok && (!any || k < key) {
				key, any = k, true
			}
		}
		if !any || w.err != nil {
			break
		}
		out = out[:0]
		for _, s := range srcs {
			if k, ok, _ := s.cur.key(); !ok || k != key {
				continue
			}
			if ids, err = s.cur.take(ids[:0]); err != nil {
				w.err = err
				break
			}
			for _, id := range ids {
				if n := s.remap[id]; n != dropped {
					out = append(out, n)
				}
			}
		}
		slices.Sort(out)
		w.add(key, out)
	}
	return len(docs), w.finish()
}
