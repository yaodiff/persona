package kb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// gramRunes is what the random texts of these tests are made of: both cases, digits, punctuation, every kind of
// white space a query is split at, CJK, kana, Hangul, full-width forms, an emoji, a combining accent (a text with it
// folds to another one) and letters whose lower case is another length.
var gramRunes = []rune("abcdeXYZ019.,-_/#+: \n\t 　的一是周报进度同步ひらカタ한글ＡＢａ１🙂é́ÉßİΣς")

func randomText(rng *rand.Rand, n int) string {
	var sb strings.Builder
	for range n {
		if rng.IntN(5) == 0 {
			sb.WriteByte(' ')
		}
		sb.WriteRune(gramRunes[rng.IntN(len(gramRunes))])
	}
	return sb.String()
}

// randomQuery is a query for text: mostly a run of the text as written or as folded (white space in it makes it
// several words), sometimes runes of its own (rarely in the text).
func randomQuery(rng *rand.Rand, text string) string {
	from := []rune(text)
	switch rng.IntN(4) {
	case 0:
		return randomText(rng, 1+rng.IntN(4))
	case 1:
		from = []rune(fold(text))
	}
	if len(from) == 0 {
		return "a"
	}
	start := rng.IntN(len(from))
	return string(from[start:min(len(from), start+1+rng.IntN(6))])
}

// A file that holds every word of a query is never one the keys rule out: what the index may skip is decided by
// this alone.
func TestGramsNeverRuleOutAFileThatHoldsTheWords(t *testing.T) {
	rng := rand.New(rand.NewPCG(1273, 1))
	held, ruledOut := 0, 0
	for range 4000 {
		text := randomText(rng, 1+rng.IntN(120))
		folded := fold(text)
		keys := fileGrams(folded)
		for range 25 {
			q := randomQuery(rng, text)
			ws := words(q)
			if len(ws) == 0 {
				continue
			}
			clauses, ok := queryClauses(ws)
			if !ok {
				t.Fatalf("no clauses for %q", q)
			}
			may := keysMay(keys, clauses)
			switch has := hasAll(folded, ws); {
			case has && !may:
				t.Fatalf("the keys of %q rule out %q, which it holds", text, q)
			case has:
				held++
			case !may:
				ruledOut++
			}
		}
	}
	// Both happen, or the test says nothing: files that hold the words, and files the keys rule out.
	if held < 1000 || ruledOut < 1000 {
		t.Fatalf("%d queries held, %d ruled out: the texts and queries no longer cover both", held, ruledOut)
	}
}

func FuzzGramsNeverRuleOutAFileThatHoldsTheWords(f *testing.F) {
	f.Add("今天的进度同步一下 build ok", "进度 BUILD")
	f.Add("café ＡＢ", "café")
	f.Add("a b", "a")
	f.Fuzz(func(t *testing.T, text, query string) {
		folded := fold(text)
		ws := words(query)
		clauses, ok := queryClauses(ws)
		if !ok {
			return
		}
		if hasAll(folded, ws) && !keysMay(fileGrams(folded), clauses) {
			t.Fatalf("the keys of %q rule out %q, which it holds", text, query)
		}
	})
}

// What the keys say of the queries the design has to answer one by one: a single CJK character, two letters, a
// word that ends or starts in the other script, punctuation, case, composed and decomposed text.
func TestGramsOfShortAndMixedQueries(t *testing.T) {
	text := "- 09:07 同事1: Quarterly 周报 draft is ready, C++ v1.2 (café) ＡＢ #42\n- 09:14 同事2: 进度ok 🙂"
	keys := fileGrams(fold(text))
	for _, c := range []struct {
		query string
		may   bool
	}{
		{"周", true}, {"报", true}, {"周报", true}, {"报周", false}, {"月", false},
		{"q", true}, {"qu", true}, {"QUARTERLY", true}, {"uart", true}, {"quarterlies", false},
		{"dr", true}, {"rd", false}, {"x", false},
		{"报 dr", true}, {"周报 draft", true}, {"周报 drift", false},
		{"事1", true}, {"1:", true}, {"度ok", true}, {"度o", true}, {"ok", true}, {"度k", false},
		{"c++", true}, {"++", true}, {"+", true}, {"c+-", false}, {"v1.2", true}, {"1.3", false}, {"(", true},
		{"café", true}, {"CAFÉ", true}, {"cafe", false},
		{"ＡＢ", true}, {"ａｂ", true}, {"ab", false},
		{"🙂", true}, {"🙃", false}, {"#42", true}, {"#4", true}, {"42", true}, {"#43", false},
	} {
		ws := words(c.query)
		clauses, ok := queryClauses(ws)
		if !ok {
			t.Fatalf("%q: no clauses", c.query)
		}
		if got := keysMay(keys, clauses); got != c.may {
			t.Errorf("%q: may = %v, want %v", c.query, got, c.may)
		}
		if has := hasAll(fold(text), ws); has && !c.may {
			t.Errorf("%q: the table is wrong, the text holds it", c.query)
		}
	}
}

// testDocs are n files with random texts, as an update reads them.
func testDocs(rng *rand.Rand, prefix string, n int) ([]builtDoc, map[string]string) {
	docs := make([]builtDoc, n)
	texts := map[string]string{}
	for i := range docs {
		name := fmt.Sprintf("%s/c%02d/messages/2026-%02d-%02d.md", prefix, i%7, 1+i%12, 1+i%28)
		if _, twice := texts[name]; twice {
			name += fmt.Sprintf(".%d", i)
		}
		text := "build " + randomText(rng, 5+rng.IntN(60))
		if i%3 == 0 {
			text += " weekly"
		}
		texts[name] = text
		docs[i] = builtDoc{segDoc: segDoc{name: name, mod: int64(1_700_000_000_000_000_000 + i), size: int64(len(text))}, keys: fileGrams(fold(text))}
	}
	return docs, texts
}

func testQueries(rng *rand.Rand, texts map[string]string) []string {
	out := []string{"build", "weekly", "build weekly", "的", "a", "ab", "周报", "zzzz", "#", "é", "ｂ"}
	names := make([]string, 0, len(texts))
	for name := range texts {
		names = append(names, name)
	}
	sort.Strings(names)
	for range 60 {
		out = append(out, randomQuery(rng, texts[names[rng.IntN(len(names))]]))
	}
	return out
}

// checkSegment: for every query, the segment names exactly the files whose own keys do not rule the query out.
func checkSegment(t *testing.T, seg *indexSeg, texts map[string]string, queries []string) {
	t.Helper()
	for _, q := range queries {
		clauses, ok := queryClauses(words(q))
		if !ok {
			continue
		}
		cand, err := seg.candidates(clauses)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		for id, d := range seg.docs {
			text, ok := texts[d.name]
			if !ok {
				t.Fatalf("the segment has %s, which is not one of its files", d.name)
			}
			if want := keysMay(fileGrams(fold(text)), clauses); cand.has(uint32(id)) != want {
				t.Fatalf("%q: %s is a candidate = %v, its keys say %v", q, d.name, !want, want)
			}
		}
	}
}

func TestSegmentAnswersAsItsFilesKeysDo(t *testing.T) {
	rng := rand.New(rand.NewPCG(1273, 2))
	for _, n := range []int{1, 2, 7, 8, 9, 63, 64, 65, 300} {
		docs, texts := testDocs(rng, "chats", n)
		path := filepath.Join(t.TempDir(), "seg-000001.idx")
		if err := writeSeg(path, docs); err != nil {
			t.Fatal(err)
		}
		seg, err := openSeg(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(seg.docs) != n || !sort.SliceIsSorted(seg.docs, func(i, j int) bool { return seg.docs[i].name < seg.docs[j].name }) {
			t.Fatalf("%d files: the segment lists %d, sorted by name: %v", n, len(seg.docs), seg.docs)
		}
		for _, d := range docs {
			id, ok := slices.BinarySearchFunc(seg.docs, d.name, func(s segDoc, name string) int { return strings.Compare(s.name, name) })
			if !ok || seg.docs[id] != d.segDoc {
				t.Fatalf("%s: read back as %+v, written as %+v", d.name, seg.docs[id], d.segDoc)
			}
		}
		checkSegment(t, seg, texts, testQueries(rng, texts))
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the segment's mode is %v (%v), want 0600", fi.Mode().Perm(), err)
		}
	}
}

// Every kind of list is written and read: all the files, a bitmap, and gaps.
func TestSegmentListKinds(t *testing.T) {
	const n = 100
	docs := make([]builtDoc, n)
	for i := range docs {
		keys := []uint64{10} // every file
		if i%2 == 0 {
			keys = append(keys, 20) // half of them: a bitmap
		}
		if i == 3 || i == 97 {
			keys = append(keys, 30) // two: gaps
		}
		docs[i] = builtDoc{segDoc: segDoc{name: fmt.Sprintf("docs/d%03d.json", i), mod: int64(i), size: 1}, keys: keys}
	}
	path := filepath.Join(t.TempDir(), "seg-000001.idx")
	if err := writeSeg(path, docs); err != nil {
		t.Fatal(err)
	}
	seg, err := openSeg(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	keys := allSegKeys(t, seg)
	if len(keys) != 3 {
		t.Fatalf("the segment has %d keys, want 3", len(keys))
	}
	for i, want := range []struct {
		kind  byte
		count int
	}{{listEvery, n}, {listBitmap, n / 2}, {listGaps, 2}} {
		k := keys[i]
		if got := raw[seg.lists+int64(k.start)]; got != want.kind {
			t.Errorf("key %d: list kind %d, want %d", k.key, got, want.kind)
		}
		cand, err := seg.candidates([]gramClause{{all: []uint64{k.key}}})
		if err != nil || cand.count() != want.count {
			t.Errorf("key %d: %d files (%v), want %d", k.key, cand.count(), err, want.count)
		}
		ids, err := seg.ids(nil, raw[seg.lists+int64(k.start):seg.lists+int64(k.end)], k.crc)
		if err != nil || len(ids) != want.count || !slices.IsSorted(ids) {
			t.Errorf("key %d: ids %v (%v), want %d ascending", k.key, ids, err, want.count)
		}
	}
	// A range of keys is the files of any of them; a key the segment does not have is no file.
	for _, c := range []struct {
		clause gramClause
		count  int
	}{
		{gramClause{lo: 15, hi: 31}, n/2 + 2}, {gramClause{lo: 21, hi: 31}, 2}, {gramClause{lo: 31, hi: 99}, 0},
		{gramClause{all: []uint64{20, 30}}, 0}, {gramClause{all: []uint64{10, 30}}, 2}, {gramClause{all: []uint64{25}}, 0},
	} {
		cand, err := seg.candidates([]gramClause{c.clause})
		if err != nil || cand.count() != c.count {
			t.Errorf("%+v: %d files (%v), want %d", c.clause, cand.count(), err, c.count)
		}
	}
}

// A merge keeps the files it is told to keep, with the keys they had.
func TestMergeSegmentsKeepsTheChosenFiles(t *testing.T) {
	rng := rand.New(rand.NewPCG(1273, 3))
	dir := t.TempDir()
	texts := map[string]string{}
	var inputs []mergeInput
	kept := 0
	for i, n := range []int{40, 1, 130} {
		docs, own := testDocs(rng, fmt.Sprintf("chats/s%d", i), n)
		path := filepath.Join(dir, fmt.Sprintf("seg-%06d.idx", i))
		if err := writeSeg(path, docs); err != nil {
			t.Fatal(err)
		}
		seg, err := openSeg(path)
		if err != nil {
			t.Fatal(err)
		}
		in := mergeInput{seg: seg}
		if i == 2 {
			in.keep = func(id uint32) bool { return id%3 != 0 }
		}
		for id, d := range seg.docs {
			if in.keep == nil || in.keep(uint32(id)) {
				texts[d.name] = own[d.name]
				kept++
			}
		}
		inputs = append(inputs, in)
	}
	path := filepath.Join(dir, "seg-000009.idx")
	n, err := mergeSegs(context.Background(), path, inputs)
	if err != nil || n != kept {
		t.Fatalf("merged %d files (%v), want %d", n, err, kept)
	}
	seg, err := openSeg(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg.docs) != kept {
		t.Fatalf("the merged segment lists %d files, want %d", len(seg.docs), kept)
	}
	checkSegment(t, seg, texts, testQueries(rng, texts))
	// The same file kept from two segments is refused: one of the two entries would be the wrong one.
	twice := filepath.Join(dir, "seg-000010.idx")
	if _, err := mergeSegs(context.Background(), twice, []mergeInput{{seg: inputs[0].seg}, {seg: inputs[0].seg}}); err == nil {
		t.Fatal("a file kept from two segments was merged")
	}
	if _, err := os.Stat(twice); !os.IsNotExist(err) {
		t.Fatalf("the refused merge left its file: %v", err)
	}
}

// A damaged segment is never believed: whatever byte is wrong or missing, it does not open, or the list a search
// reads does not check out, or the answer is still the right one (the byte was in a list the search did not read).
// The damaged copies are read from memory (readSeg and candidatesIn, which openSeg and candidates read a file
// with): one for every byte of the segment.
func TestDamagedSegmentIsNeverBelieved(t *testing.T) {
	rng := rand.New(rand.NewPCG(1273, 4))
	docs, texts := testDocs(rng, "chats", 24)
	dir := t.TempDir()
	good := filepath.Join(dir, "seg-000001.idx")
	if err := writeSeg(good, docs); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	queries := testQueries(rng, texts)[:30]
	// Which files hold each query, worked out once: every damaged copy is checked against it.
	holds := map[string]map[string]bool{}
	for _, q := range queries {
		holds[q] = map[string]bool{}
		for name, text := range texts {
			holds[q][name] = hasAll(fold(text), words(q))
		}
	}
	refused, caught := 0, 0
	check := func(what string, bad []byte) {
		t.Helper()
		seg, err := readSeg(bytes.NewReader(bad), int64(len(bad)))
		if err != nil {
			if !damaged(err) {
				t.Fatalf("%s: refused with %v, which does not say the segment is damaged", what, err)
			}
			refused++
			return
		}
		for _, q := range queries {
			clauses, ok := queryClauses(words(q))
			if !ok {
				continue
			}
			cand, err := seg.candidatesIn(bytes.NewReader(bad), clauses)
			if err != nil {
				if !damaged(err) {
					t.Fatalf("%s: %q failed with %v, which does not say the segment is damaged", what, q, err)
				}
				caught++
				continue
			}
			for id, d := range seg.docs {
				if _, ok := texts[d.name]; !ok {
					t.Fatalf("%s: opened with a file it never had, %q", what, d.name)
				}
				if holds[q][d.name] && !cand.has(uint32(id)) {
					t.Fatalf("%s: %q rules out %s, which holds it", what, q, d.name)
				}
			}
		}
	}
	check("whole", raw)
	if refused != 0 || caught != 0 {
		t.Fatalf("the whole segment: refused %d times, %d lists caught", refused, caught)
	}
	for i := range raw {
		bad := bytes.Clone(raw)
		bad[i] ^= 1 << (i % 8)
		check(fmt.Sprintf("byte %d flipped", i), bad)
	}
	for n := 0; n < len(raw); n += 1 + n/16 {
		check(fmt.Sprintf("cut to %d bytes", n), raw[:n])
	}
	check("a byte added", append(bytes.Clone(raw), 0))
	if refused == 0 || caught == 0 {
		t.Fatalf("%d damaged segments refused, %d lists caught: the test no longer reaches both", refused, caught)
	}
	// And through the file, as the index reads it: a damaged footer does not open, and a list damaged after the
	// segment was opened is caught by the search that reads it.
	bad := bytes.Clone(raw)
	bad[len(bad)-9] ^= 0x40
	path := filepath.Join(dir, "seg-000002.idx")
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openSeg(path); !errors.Is(err, errSegment) {
		t.Fatalf("a segment with a damaged footer opened: %v", err)
	}
	seg, err := openSeg(good)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range allSegKeys(t, seg) {
		raw[seg.lists+int64(k.start)] ^= 0x80
	}
	if err := os.WriteFile(good, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	clauses, _ := queryClauses(words("build"))
	if _, err := seg.candidates(clauses); !errors.Is(err, errSegment) {
		t.Fatalf("a search read a damaged list of the file: %v", err)
	}
}

// The log is read up to its last whole entry: an append cut short, or bytes gone wrong after it was written, lose
// the entries from there on (their files are read again) and never give an entry that was not written. A log cut
// before its first entry is an empty one; one that starts with anything else is not a log. The damaged copies are
// read from memory (readIndexLogFrom, which readIndexLog reads a file with).
func TestIndexLogStopsAtTheFirstBadEntry(t *testing.T) {
	rng := rand.New(rand.NewPCG(1273, 5))
	docs, _ := testDocs(rng, "chats", 9)
	raw := []byte(indexLogMagic)
	ends := []int{len(raw)}
	for i := range docs {
		raw = encodeLogEntry(raw, &docs[i])
		ends = append(ends, len(raw))
	}
	whole := func(n int) int { // the entries wholly within the first n bytes
		return sort.SearchInts(ends, n+1) - 1
	}
	read := func(data []byte) (map[string]*builtDoc, int64, int, error) {
		return readIndexLogFrom(bytes.NewReader(data), int64(len(data)))
	}
	check := func(what string, data []byte, entries int, read func([]byte) (map[string]*builtDoc, int64, int, error)) {
		t.Helper()
		got, valid, _, err := read(data)
		if entries < 0 {
			if err == nil || !damaged(err) {
				t.Fatalf("%s: read %d entries (%v), want it refused", what, len(got), err)
			}
			return
		}
		wantValid := int64(ends[entries])
		if len(data) < len(indexLogMagic) {
			wantValid = 0 // nothing whole, not even its first bytes: written from the start
		}
		if err != nil || len(got) != entries || valid != wantValid {
			t.Fatalf("%s: %d entries, %d valid bytes (%v); want %d and %d", what, len(got), valid, err, entries, wantValid)
		}
		for i := range entries {
			if d := got[docs[i].name]; d == nil || !reflect.DeepEqual(*d, docs[i]) {
				t.Fatalf("%s: entry %d read back as %+v, written as %+v", what, i, d, docs[i])
			}
		}
	}
	check("whole", raw, len(docs), read)
	for n := range len(raw) {
		check(fmt.Sprintf("cut to %d bytes", n), raw[:n], max(0, whole(n)), read)
	}
	check("not a log", []byte("PKB"+"x"), -1, read)
	for i := range raw {
		bad := bytes.Clone(raw)
		bad[i] ^= 0x10
		check(fmt.Sprintf("byte %d flipped", i), bad, whole(i), read)
	}
	// Through the file, as the index reads it: whole, cut inside its last entry, and not there.
	path := filepath.Join(t.TempDir(), "log-000001.log")
	fromFile := func(data []byte) (map[string]*builtDoc, int64, int, error) {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return readIndexLog(path)
	}
	check("the file, whole", raw, len(docs), fromFile)
	check("the file, cut in its last entry", raw[:len(raw)-3], len(docs)-1, fromFile)
	check("the file, not a log", []byte("PKB"+"x"), -1, fromFile)
	if got, valid, _, err := readIndexLog(filepath.Join(t.TempDir(), "none.log")); err != nil || len(got) != 0 || valid != 0 {
		t.Fatalf("no log file: %d entries, %d bytes, %v", len(got), valid, err)
	}
	// An entry whose length says more than the file holds is where the log ends, and nothing is set aside for the
	// bytes it claims: here 200 MB, within what an entry may be.
	long := binary.LittleEndian.AppendUint32(bytes.Clone(raw[:ends[1]]), 200<<20)
	long = append(long, 1, 2, 3, 4, 'x', 'y', 'z')
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	check("an entry longer than the file", long, 1, read)
	runtime.ReadMemStats(&after)
	if got := after.TotalAlloc - before.TotalAlloc; got > 4<<20 {
		t.Fatalf("reading a log of %d bytes whose last entry says 200 MB set %d bytes aside", len(long), got)
	}
}

// indexArchive is a small archive with every kind of file a search reads, all old enough to be indexed: chats with
// days, a person, a project, a doc, a recording (whose meta.json and transcript.md searches do not read from disk)
// and a Moments post.
func indexArchive(t *testing.T) *Engine {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for c := range 4 {
		ch := &Chat{ID: fmt.Sprintf("lark-c%d", c), Name: fmt.Sprintf("群聊 %d", c), Kind: "group"}
		if err := st.PutChat(ch); err != nil {
			t.Fatal(err)
		}
		if err := st.PutSummary(KindChat, ch.ID, "# "+ch.Name+"\n\n讨论发布计划 release plan\n"); err != nil {
			t.Fatal(err)
		}
		for d := range 6 {
			day := start.AddDate(0, 0, d).Format("2006-01-02")
			text := fmt.Sprintf("# %s\n- 09:00 同事%d: 今天的进度同步一下 build ok #%d\n", day, c, d)
			if c == 1 && d == 2 {
				text += "- 09:30 同事1: Quarterly 周报 draft is ready\n"
			}
			matchesWrite(t, st.path(KindChat, ch.ID, "messages", day+".md"), text)
		}
	}
	if err := st.PutPerson(&Person{ID: "lark-u1", Name: "同事 1", Note: "负责后端 backend"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "p1", Name: "项目 1", Kind: "short", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "设计文档 design", Note: "周报 template", At: start}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := e.PutRecording(recID, recUpload(1, now.Add(-time.Hour), true, "weekly sync", "周报 review"), now); err != nil {
		t.Fatal(err)
	}
	matchesWrite(t, filepath.Join(st.Dir, "moments", "posts", "m1.md"), "a post about the 周报\n")
	matchesWrite(t, filepath.Join(st.Dir, "moments", "posts", "m1.json"), `{"id":"m1"}`+"\n")
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	return e
}

// dayArchive is the smallest archive an update and a search have something to do in: the chat lark-c2 with its
// first days of March 2026, a line each, all old enough to be indexed. For the tests that are about one file or
// about the index's own files, where every other file of indexArchive would only be opened, again and again.
func dayArchive(t *testing.T, days int) *Engine {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutChat(&Chat{ID: "lark-c2", Name: "群聊 2", Kind: "group"}); err != nil {
		t.Fatal(err)
	}
	for d := 1; d <= days; d++ {
		day := fmt.Sprintf("2026-03-%02d", d)
		matchesWrite(t, st.path(KindChat, "lark-c2", "messages", day+".md"), fmt.Sprintf("# %s\n- 09:00 同事2: 今天的进度同步一下 build ok #%d\n", day, d))
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st}
}

// writeOld writes a file of the archive and sets its mtime back by age (an hour and more is old enough to index).
func writeOld(t *testing.T, e *Engine, rel, text string, age time.Duration) {
	t.Helper()
	p := filepath.Join(e.Store.Dir, filepath.FromSlash(rel))
	matchesWrite(t, p, text)
	at := time.Now().Add(-age)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func updateIndex(t *testing.T, e *Engine) SearchIndexStats {
	t.Helper()
	stats, err := e.UpdateSearchIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// indexSizesFor sets the sizes an update works in for one test.
func indexSizesFor(t *testing.T, change func()) {
	t.Helper()
	was := indexSizes
	t.Cleanup(func() { indexSizes = was })
	change()
}

// smallSegments makes every update write segments of a few files, so a small archive goes through batches, more
// segments than are kept, and the merge.
func smallSegments(t *testing.T) {
	t.Helper()
	indexSizesFor(t, func() {
		indexSizes.logFiles, indexSizes.batchFiles, indexSizes.maxSegs, indexSizes.readChunk = 3, 5, 3, 4
	})
}

// allSegKeys are a segment's keys, first to last.
func allSegKeys(t *testing.T, seg *indexSeg) []segKey {
	t.Helper()
	f, err := os.Open(seg.path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	keys, err := seg.readKeys(f, 0, len(seg.first), nil)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// segKeys are the keys of every file of a segment, read back from its lists.
func segKeys(t *testing.T, seg *indexSeg) [][]uint64 {
	t.Helper()
	cur, err := seg.cursor()
	if err != nil {
		t.Fatal(err)
	}
	defer cur.close()
	out := make([][]uint64, len(seg.docs))
	var ids []uint32
	for {
		key, ok, err := cur.key()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			return out
		}
		if ids, err = cur.take(ids[:0]); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			out[id] = append(out[id], key)
		}
	}
}

// indexHolds is what the index on disk holds, read by a new store: every entry by path (the one searches use).
func indexHolds(t *testing.T, dir string) map[string]builtDoc {
	t.Helper()
	snap, _, _, _, err := readIndex(filepath.Join(dir, SearchIndexDir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]builtDoc{}
	keys := make([][][]uint64, len(snap.segs))
	for i, seg := range snap.segs {
		keys[i] = segKeys(t, seg)
	}
	for name, r := range snap.where {
		out[name] = builtDoc{segDoc: snap.segs[r.seg].docs[r.id], keys: keys[r.seg][r.id]}
	}
	for name, d := range snap.log {
		out[name] = *d
	}
	return out
}

// indexNames are all the paths the index's files name, the entries searches no longer use too: what is on disk of
// each file of the archive.
func indexNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	snap, _, _, _, err := readIndex(filepath.Join(dir, SearchIndexDir))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, seg := range snap.segs {
		for _, d := range seg.docs {
			out[d.name] = true
		}
	}
	for name := range snap.log {
		out[name] = true
	}
	return out
}

// checkIndexCurrent: the index holds every file a search reads from disk, as it is now, with the keys of its text,
// and nothing else.
func checkIndexCurrent(t *testing.T, e *Engine) {
	t.Helper()
	holds := indexHolds(t, e.Store.Dir)
	want := 0
	for _, d := range e.indexFiles() {
		want++
		raw, err := os.ReadFile(d.path)
		if err != nil {
			t.Fatal(err)
		}
		st := statFile(d.path)
		got, ok := holds[d.rel]
		if !ok {
			t.Fatalf("%s is not in the index", d.rel)
		}
		if got.mod != st.mod || got.size != st.size {
			t.Fatalf("%s: the index has it as (%d, %d), it is (%d, %d)", d.rel, got.mod, got.size, st.mod, st.size)
		}
		if keys := fileGrams(fold(string(raw))); !slices.Equal(got.keys, keys) {
			t.Fatalf("%s: the index has %d keys, its text gives %d", d.rel, len(got.keys), len(keys))
		}
	}
	if want == 0 || len(holds) != want {
		names := make([]string, 0, len(holds))
		for name := range holds {
			names = append(names, name)
		}
		sort.Strings(names)
		t.Fatalf("the index holds %d files, searches read %d: %v", len(holds), want, names)
	}
}

// checkIndexFiles: the index's directory and files are the owner's alone, and only the files the manifest names
// are there.
func checkIndexFiles(t *testing.T, e *Engine) {
	t.Helper()
	dir := e.Store.searchDir()
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("%s: mode %v (%v), want 0700", SearchIndexDir, fi.Mode().Perm(), err)
	}
	_, man, _, _, err := readIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	named := map[string]bool{indexManifest: true}
	for _, s := range man.Segments {
		named[s] = true
	}
	if man.Log != "" {
		named[man.Log] = true
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ents {
		fi, err := d.Info()
		if err != nil {
			t.Fatal(err)
		}
		if !named[d.Name()] {
			t.Errorf("%s/%s is not named by the manifest", SearchIndexDir, d.Name())
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s/%s: mode %v, want 0600", SearchIndexDir, d.Name(), fi.Mode().Perm())
		}
	}
}

func TestUpdateSearchIndexReadsEveryFileSearched(t *testing.T) {
	for _, mode := range []string{"log", "segments"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "segments" {
				smallSegments(t)
			}
			e := indexArchive(t)
			if e.Store.SearchIndexReady() {
				t.Fatal("ready before any update")
			}
			stats := updateIndex(t, e)
			files := len(e.indexFiles())
			if stats.Files != files || stats.Read != files || stats.Waiting != 0 {
				t.Fatalf("first update: %+v, want all %d files read", stats, files)
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
			if !e.Store.SearchIndexReady() {
				t.Fatal("not ready after an update that finished")
			}
			holds := indexHolds(t, e.Store.Dir)
			for _, not := range []string{"recordings/" + recID + "/meta.json", "recordings/" + recID + "/transcript.md", "moments/posts/m1.json"} {
				if _, ok := holds[not]; ok {
					t.Errorf("%s is in the index; no search reads it from disk", not)
				}
			}
			if _, ok := holds["moments/posts/m1.md"]; !ok {
				t.Error("the Moments post is not in the index")
			}
			// Nothing changed: nothing is read, and a store that opens the index finds it ready.
			if stats := updateIndex(t, e); stats.Read != 0 || stats.Files != files {
				t.Fatalf("second update: %+v, want nothing read", stats)
			}
			again, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if !again.SearchIndexReady() {
				t.Fatal("a new store does not find the index ready")
			}
			if stats := updateIndex(t, &Engine{Store: again}); stats.Read != 0 {
				t.Fatalf("a new store's update: %+v, want nothing read", stats)
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
		})
	}
}

// Files changed, added, renamed and deleted between updates: the next update reads the ones that are new to it and
// drops the others.
func TestUpdateSearchIndexFollowsTheFiles(t *testing.T) {
	for _, mode := range []string{"log", "segments"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "segments" {
				smallSegments(t)
			}
			e := indexArchive(t)
			updateIndex(t, e)
			day := func(c int, d string) string { return fmt.Sprintf("chats/lark-c%d/messages/%s.md", c, d) }
			// Changed: longer; another text of the same length; added; a chat renamed; a day deleted.
			writeOld(t, e, day(0, "2026-03-01"), "# 2026-03-01\n- 09:00 同事0: 改了 changed the plan\n", 50*time.Minute)
			same := "# 2026-03-02\n- 09:00 同事0: 今天的进度同步一下 buiId ok #1\n"
			if raw, err := os.ReadFile(filepath.Join(e.Store.Dir, day(0, "2026-03-02"))); err != nil || len(raw) != len(same) {
				t.Fatalf("the same-size text is %d bytes, the day %d (%v)", len(same), len(raw), err)
			}
			writeOld(t, e, day(0, "2026-03-02"), same, 50*time.Minute)
			writeOld(t, e, day(0, "2026-04-01"), "# 2026-04-01\n- 09:00 同事0: 新的一天 brand new\n", 50*time.Minute)
			if err := os.Rename(e.Store.path(KindChat, "lark-c2"), e.Store.path(KindChat, "lark-c9")); err != nil {
				t.Fatal(err)
			}
			gone := day(3, "2026-03-04")
			if err := os.Remove(filepath.Join(e.Store.Dir, gone)); err != nil {
				t.Fatal(err)
			}
			e.Store.SearchIndexPurge()
			stats := updateIndex(t, e)
			// Read: the two changed days, the new one, and the renamed chat's meta.json, summary.md and six days.
			if stats.Read != 3+8 {
				t.Fatalf("update after the changes: %+v, want 11 files read", stats)
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
			// Deleted on purpose: nothing of the deleted and renamed files is left on disk, in any segment or the log.
			for name := range indexNames(t, e.Store.Dir) {
				if name == gone || strings.HasPrefix(name, "chats/lark-c2/") {
					t.Errorf("the index still names %s", name)
				}
			}
			if stats := updateIndex(t, e); stats.Read != 0 || stats.Merged {
				t.Fatalf("update with nothing changed: %+v", stats)
			}
		})
	}
}

// indexEverHolds reports whether any entry on disk, in a segment or the log, the replaced ones too, says the file
// at name has key.
func indexEverHolds(t *testing.T, dir, name string, key uint64) bool {
	t.Helper()
	snap, man, _, _, err := readIndex(filepath.Join(dir, SearchIndexDir))
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range snap.segs {
		keys := segKeys(t, seg)
		for id, d := range seg.docs {
			if d.name == name && slices.Contains(keys[id], key) {
				return true
			}
		}
	}
	if man.Log == "" {
		return false
	}
	raw, err := os.ReadFile(filepath.Join(dir, SearchIndexDir, man.Log))
	if err != nil {
		t.Fatal(err)
	}
	for raw = raw[len(indexLogMagic):]; len(raw) >= 8; {
		n := int(uint32(raw[0]) | uint32(raw[1])<<8 | uint32(raw[2])<<16 | uint32(raw[3])<<24)
		if n == 0 || 8+n > len(raw) {
			break
		}
		if d, ok := decodeLogEntry(raw[8 : 8+n]); ok && d.name == name && slices.Contains(d.keys, key) {
			return true
		}
		raw = raw[8+n:]
	}
	return false
}

// Text deleted on purpose leaves the index at the next update: the entries of deleted files, and what the index
// read of a file before lines were taken out of it, in a segment or still in the log.
func TestSearchIndexPurgeDropsWhatTheFilesNoLongerSay(t *testing.T) {
	for _, mode := range []string{"log", "segments"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "segments" {
				smallSegments(t)
			}
			e := indexArchive(t)
			day := "chats/lark-c0/messages/2026-03-03.md"
			gone := "chats/lark-c2/messages/2026-03-03.md"
			secret := wordClause("zqsecret").all[0]
			writeOld(t, e, day, "# 2026-03-03\n- 09:00 同事0: the zqsecret plan\n- 09:01 同事0: build ok\n", 50*time.Minute)
			writeOld(t, e, gone, "# 2026-03-03\n- 09:00 同事2: zqsecret too\n", 50*time.Minute)
			updateIndex(t, e)
			// The line is taken out of one day (as retention does), the other day deleted; without the purge the
			// index still has what it read of both.
			writeOld(t, e, day, "# 2026-03-03\n- 09:01 同事0: build ok\n", 40*time.Minute)
			if err := os.Remove(filepath.Join(e.Store.Dir, gone)); err != nil {
				t.Fatal(err)
			}
			if stats := updateIndex(t, e); stats.Merged {
				t.Fatalf("update without the purge: %+v, want no merge", stats)
			}
			if !indexEverHolds(t, e.Store.Dir, day, secret) || !indexEverHolds(t, e.Store.Dir, gone, secret) {
				t.Fatal("the old entries are gone already: the test no longer shows the purge dropping them")
			}
			if got := e.Search("zqsecret", true, 10); len(got) != 0 {
				t.Fatalf("the deleted text is found: %+v", got)
			}
			e.Store.SearchIndexPurge()
			if stats := updateIndex(t, e); !stats.Merged {
				t.Fatalf("update after the purge: %+v, want a merge", stats)
			}
			if indexEverHolds(t, e.Store.Dir, day, secret) || indexEverHolds(t, e.Store.Dir, gone, secret) {
				t.Fatal("the index still has the deleted text's key")
			}
			if indexNames(t, e.Store.Dir)[gone] {
				t.Fatal("the index still names the deleted file")
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
			// With nothing to drop a purge costs nothing.
			e.Store.SearchIndexPurge()
			if stats := updateIndex(t, e); stats.Merged || stats.Read != 0 {
				t.Fatalf("a purge with nothing to drop: %+v", stats)
			}
		})
	}
}

// The engine's deletions ask for the purge themselves: a forgotten source, retention, a deleted recording.
func TestDeletionsPurgeTheSearchIndex(t *testing.T) {
	e := indexArchive(t)
	updateIndex(t, e)
	purge := &e.Store.search.purge
	if purge.Load() {
		t.Fatal("a purge is asked for before anything was deleted")
	}
	if ok, err := e.DeleteRecording(recID, time.Now()); err != nil || !ok {
		t.Fatalf("DeleteRecording: %v, %v", ok, err)
	}
	if !purge.Swap(false) {
		t.Error("deleting a recording did not ask for the purge")
	}
	if _, err := e.ForgetSource("lark"); err != nil {
		t.Fatal(err)
	}
	if !purge.Swap(false) {
		t.Error("forgetting a source did not ask for the purge")
	}
	// Retention: days older than a year go.
	writeOld(t, e, "chats/lark-c0/messages/2020-01-01.md", "# 2020-01-01\n- 09:00 同事0: long ago\n", time.Hour)
	if n := e.Prune(time.Now()); n == 0 {
		t.Fatal("retention removed nothing: the test no longer reaches the purge")
	}
	if !purge.Swap(false) {
		t.Error("retention did not ask for the purge")
	}
	if n := e.Prune(time.Now()); n != 0 || purge.Load() {
		t.Errorf("retention with nothing to remove: removed %d, purge %v", n, purge.Load())
	}
}

// Entries of files that went away without the index being told are dropped within indexPurgeEvery of the last
// merge, and entries of files changed since within indexMergeEvery.
func TestUpdateSearchIndexDropsOldEntriesInTime(t *testing.T) {
	e := indexArchive(t)
	updateIndex(t, e)
	gone := "chats/lark-c3/messages/2026-03-04.md"
	if err := os.Remove(filepath.Join(e.Store.Dir, gone)); err != nil {
		t.Fatal(err)
	}
	changed := "chats/lark-c0/messages/2026-03-01.md"
	writeOld(t, e, changed, "# 2026-03-01\n- 09:00 同事0: 改了\n", 50*time.Minute)
	if stats := updateIndex(t, e); stats.Merged || stats.Read != 1 {
		t.Fatalf("update right after: %+v, want one file read and no merge", stats)
	}
	if !indexNames(t, e.Store.Dir)[gone] {
		t.Fatal("the deleted file's entry is gone already: the test no longer shows the merge dropping it")
	}
	// merged sets back when the index was last merged, as a new process finds it.
	merged := func(ago time.Duration) *Engine {
		t.Helper()
		return indexMergedAt(t, e, time.Now().Add(-ago))
	}
	e = merged(indexPurgeEvery + time.Minute)
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("an hour after the last merge: %+v, want a merge", stats)
	}
	if indexNames(t, e.Store.Dir)[gone] {
		t.Fatal("the deleted file's entry is still on disk after the merge")
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
	// A changed file's old entry (in a segment, the new one in the log) waits for the daily merge.
	writeOld(t, e, changed, "# 2026-03-01\n- 09:00 同事0: 又改了 again\n", 40*time.Minute)
	if stats := updateIndex(t, e); stats.Merged {
		t.Fatalf("a changed file alone: %+v, want no merge", stats)
	}
	e = merged(indexPurgeEvery + time.Minute)
	if stats := updateIndex(t, e); stats.Merged {
		t.Fatalf("a changed file an hour after the merge: %+v, want no merge yet", stats)
	}
	e = merged(indexMergeEvery + time.Minute)
	if stats := updateIndex(t, e); !stats.Merged || stats.Dropped != 1 {
		t.Fatalf("a changed file a day after the merge: %+v, want a merge dropping one entry", stats)
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
}

// indexMergedAt writes at into e's manifest as the time the index was last merged, and gives the engine of a new
// store: the index as a new process finds it.
func indexMergedAt(t *testing.T, e *Engine, at time.Time) *Engine {
	t.Helper()
	ix := &e.Store.search
	was := ix.state.Load()
	man := was.man
	man.Merged = at
	u := &indexUpdate{e: e, ix: ix, dir: e.Store.searchDir(), man: man, logOld: was.logOld}
	if err := u.publish(was.snap, was.logLen); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st}
}

// A manifest whose last merge is yet to come (the clock was set back since, or one bit of its time is not the one
// written: a year of 3026) does not put off the merges that are due by time until then: the entry of a file that
// is gone is dropped at the next update, and the manifest has the time of that merge.
func TestUpdateSearchIndexWhoseLastMergeIsYetToCome(t *testing.T) {
	e := dayArchive(t, 4)
	updateIndex(t, e)
	gone := "chats/lark-c2/messages/2026-03-04.md"
	if err := os.Remove(filepath.Join(e.Store.Dir, gone)); err != nil {
		t.Fatal(err)
	}
	if stats := updateIndex(t, e); stats.Merged || !indexNames(t, e.Store.Dir)[gone] {
		t.Fatalf("update right after: %+v, want no merge yet and the entry still on disk", stats)
	}
	e = indexMergedAt(t, e, time.Now().AddDate(1000, 0, 0))
	if stats := updateIndex(t, e); !stats.Merged || stats.Dropped != 1 {
		t.Fatalf("update with a last merge a thousand years ahead: %+v, want a merge dropping one entry", stats)
	}
	if indexNames(t, e.Store.Dir)[gone] {
		t.Fatal("the deleted file's entry is still on disk")
	}
	if _, man, _, _, err := readIndex(e.Store.searchDir()); err != nil || man.Merged.After(time.Now()) {
		t.Fatalf("the manifest after the merge says it was merged at %v (%v)", man.Merged, err)
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
}

// A file changed in the last seconds, or too large, is left for searches to read; it is indexed once it is old
// enough.
func TestUpdateSearchIndexLeavesFreshAndHugeFiles(t *testing.T) {
	e := indexArchive(t)
	updateIndex(t, e)
	fresh := "chats/lark-c0/messages/2026-04-02.md"
	writeOld(t, e, fresh, "# 2026-04-02\n- 09:00 同事0: 刚写的 just now\n", 0)
	huge := "chats/lark-c0/messages/2026-04-03.md"
	writeOld(t, e, huge, "# 2026-04-03\n"+strings.Repeat("- 09:00 同事0: 很长 long\n", indexMaxFile/20), time.Hour)
	stats := updateIndex(t, e)
	if stats.Read != 0 || stats.Waiting != 2 {
		t.Fatalf("update: %+v, want two files waiting and none read", stats)
	}
	holds := indexHolds(t, e.Store.Dir)
	if _, ok := holds[fresh]; ok {
		t.Error("the file written just now is in the index")
	}
	if _, ok := holds[huge]; ok {
		t.Error("the file over indexMaxFile is in the index")
	}
	if !e.Store.SearchIndexReady() {
		t.Error("the index is not ready with only files it leaves out")
	}
	at := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(e.Store.Dir, fresh), at, at); err != nil {
		t.Fatal(err)
	}
	if stats := updateIndex(t, e); stats.Read != 1 || stats.Waiting != 1 {
		t.Fatalf("update once the file is old: %+v, want it read", stats)
	}
}

// stopAfter is a context that ends once its Err has been asked n times.
type stopAfter struct {
	context.Context
	left atomic.Int32
}

func (c *stopAfter) Err() error {
	if c.left.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// An update stopped half way keeps what it read: the index is not ready, says nothing wrong, and the next update
// reads only the rest.
func TestUpdateSearchIndexGoesOnAfterAStop(t *testing.T) {
	for _, mode := range []string{"log", "segments"} {
		t.Run(mode, func(t *testing.T) {
			smallSegments(t)
			if mode == "log" {
				indexSizesFor(t, func() { indexSizes.logFiles = 4096 })
			}
			e := indexArchive(t)
			files := len(e.indexFiles())
			ctx := &stopAfter{Context: context.Background()}
			ctx.left.Store(3)
			stats, err := e.UpdateSearchIndex(ctx)
			if !errors.Is(err, context.Canceled) || stats.Read == 0 || stats.Read >= files {
				t.Fatalf("stopped update: %+v, %v; want some of %d files read", stats, err, files)
			}
			if e.Store.SearchIndexReady() {
				t.Fatal("ready after an update that was stopped")
			}
			first := stats.Read
			if len(indexHolds(t, e.Store.Dir)) != first {
				t.Fatalf("the index holds %d files, the stopped update read %d", len(indexHolds(t, e.Store.Dir)), first)
			}
			// A new process goes on from what is on disk.
			st, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e = &Engine{Store: st}
			if st.SearchIndexReady() {
				t.Fatal("a new store finds the half-built index ready")
			}
			if stats := updateIndex(t, e); stats.Read != files-first {
				t.Fatalf("next update: %+v, want the other %d files read", stats, files-first)
			}
			if !st.SearchIndexReady() {
				t.Fatal("not ready after the update that finished")
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
		})
	}
}

// An index that does not check out is started again from the files: a manifest that is not one, one written with
// other tables or another format, a segment damaged or missing, a log that is not one.
func TestUpdateSearchIndexStartsAgainWhenDamaged(t *testing.T) {
	flip := func(t *testing.T, p string, at func(n int) int) {
		t.Helper()
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		raw[at(len(raw))] ^= 0x40
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest := func(t *testing.T, e *Engine) (string, indexManifestFile) {
		t.Helper()
		_, man, _, _, err := readIndex(e.Store.searchDir())
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(e.Store.searchDir(), indexManifest), man
	}
	for name, damage := range map[string]func(t *testing.T, e *Engine){
		"manifest is not JSON": func(t *testing.T, e *Engine) {
			p, _ := manifest(t, e)
			matchesWrite(t, p, "{")
		},
		"manifest of another type": func(t *testing.T, e *Engine) {
			p, _ := manifest(t, e)
			matchesWrite(t, p, `{"format":"one"}`)
		},
		"other tables": func(t *testing.T, e *Engine) {
			p, _ := manifest(t, e)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			matchesWrite(t, p, strings.Replace(string(raw), indexTables, "0.0.0/0.0.0", 1))
		},
		"another format": func(t *testing.T, e *Engine) {
			p, _ := manifest(t, e)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			now := fmt.Sprintf(`"format":%d`, indexFormat)
			if !strings.Contains(string(raw), now) {
				t.Fatalf("the manifest does not say %s: %s", now, raw)
			}
			matchesWrite(t, p, strings.Replace(string(raw), now, fmt.Sprintf(`"format":%d`, indexFormat-1), 1))
		},
		"segment's footer damaged": func(t *testing.T, e *Engine) {
			_, man := manifest(t, e)
			flip(t, filepath.Join(e.Store.searchDir(), man.Segments[0]), func(n int) int { return n - 9 })
		},
		"segment cut short": func(t *testing.T, e *Engine) {
			_, man := manifest(t, e)
			p := filepath.Join(e.Store.searchDir(), man.Segments[0])
			if err := os.Truncate(p, 100); err != nil {
				t.Fatal(err)
			}
		},
		"segment missing": func(t *testing.T, e *Engine) {
			_, man := manifest(t, e)
			if err := os.Remove(filepath.Join(e.Store.searchDir(), man.Segments[0])); err != nil {
				t.Fatal(err)
			}
		},
		"log is not a log": func(t *testing.T, e *Engine) {
			_, man := manifest(t, e)
			flip(t, filepath.Join(e.Store.searchDir(), man.Log), func(int) int { return 2 })
		},
		"manifest names a file elsewhere": func(t *testing.T, e *Engine) {
			p, man := manifest(t, e)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			matchesWrite(t, p, strings.Replace(string(raw), man.Segments[0], "../"+man.Segments[0], 1))
		},
		// Whole JSON of the right types, and a time the decoder refuses: neither a syntax error nor a type error.
		"manifest's time is not one": func(t *testing.T, e *Engine) {
			p, _ := manifest(t, e)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, ok := fields["merged"].(string); !ok {
				t.Fatalf("the manifest has no merged time: %s", raw)
			}
			fields["merged"] = "the day before"
			if raw, err = json.Marshal(fields); err != nil {
				t.Fatal(err)
			}
			matchesWrite(t, p, string(raw))
		},
		// A name the index gives no file. Read as the log, the directory fails as reading a directory does, at every
		// try: not an error that says the index is wrong.
		"manifest names the directory as its log": func(t *testing.T, e *Engine) {
			p, man := manifest(t, e)
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			matchesWrite(t, p, strings.Replace(string(raw), man.Log, ".", 1))
		},
	} {
		t.Run(name, func(t *testing.T) {
			// A segment and a log: most files in the first, a day changed afterwards in the second.
			indexSizesFor(t, func() { indexSizes.logFiles = 8 })
			e := indexArchive(t)
			updateIndex(t, e)
			writeOld(t, e, "chats/lark-c0/messages/2026-03-01.md", "# 2026-03-01\n- 09:00 同事0: 改了\n", 50*time.Minute)
			updateIndex(t, e)
			if _, man := manifest(t, e); len(man.Segments) != 1 || man.Log == "" {
				t.Fatalf("the index is %+v, want one segment and a log", man)
			}
			damage(t, e)
			st, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e = &Engine{Store: st}
			if st.SearchIndexReady() {
				t.Fatal("the damaged index is ready")
			}
			if f := e.searchFilter(words("build")); f != nil {
				t.Fatal("the damaged index answers a search")
			}
			files := len(e.indexFiles())
			stats := updateIndex(t, e)
			if !stats.Rebuilt || stats.Read != files {
				t.Fatalf("update: %+v, want the index started again and all %d files read", stats, files)
			}
			if !st.SearchIndexReady() || e.searchFilter(words("build")) == nil {
				t.Fatal("the index built again is not used")
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
		})
	}
}

// The index removed while the process that writes it runs (rm -r .search is how a person starts it again): searches
// give what the files say meanwhile, and the next update builds it again from every file. The same when only its
// log is removed: the update that would append to it finds it gone, and the one after starts again.
func TestUpdateSearchIndexStartsAgainWhenRemoved(t *testing.T) {
	for _, mode := range []string{"log", "segments"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "segments" {
				smallSegments(t)
			}
			e := indexArchive(t)
			updateIndex(t, e)
			want := findReference(e, "周报", 1<<20)
			if len(want) == 0 {
				t.Fatal("the word is not found")
			}
			if err := os.RemoveAll(e.Store.searchDir()); err != nil {
				t.Fatal(err)
			}
			if got := e.Find("周报", 1<<20); !reflect.DeepEqual(got, want) {
				t.Fatalf("Find with the index removed:\n got %+v\nwant %+v", got, want)
			}
			if got := e.Find("nowhere", 80); len(got) != 0 {
				t.Fatalf("Find of an absent word with the index removed: %+v", got)
			}
			files := len(e.indexFiles())
			if stats := updateIndex(t, e); stats.Read != files {
				t.Fatalf("update after the index was removed: %+v, want all %d files read", stats, files)
			}
			if !e.Store.SearchIndexReady() {
				t.Fatal("the index built again is not ready")
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
		})
	}
	t.Run("only the log", func(t *testing.T) {
		e := indexArchive(t)
		updateIndex(t, e)
		_, man, _, _, err := readIndex(e.Store.searchDir())
		if err != nil || man.Log == "" || len(man.Segments) != 0 {
			t.Fatalf("the index is %+v (%v), want only a log", man, err)
		}
		if err := os.Remove(filepath.Join(e.Store.searchDir(), man.Log)); err != nil {
			t.Fatal(err)
		}
		writeOld(t, e, "chats/lark-c0/messages/2026-03-01.md", "# 2026-03-01\n- 09:00 同事0: 改了\n", 50*time.Minute)
		if _, err := e.UpdateSearchIndex(context.Background()); err == nil || !damaged(err) {
			t.Fatalf("appending to a log that is gone: %v, want it found damaged", err)
		}
		if got, want := e.Find("改了", 80), findReference(e, "改了", 80); len(want) != 1 || !reflect.DeepEqual(got, want) {
			t.Fatalf("Find after the log was removed:\n got %+v\nwant %+v", got, want)
		}
		files := len(e.indexFiles())
		if stats := updateIndex(t, e); !stats.Rebuilt || stats.Read != files {
			t.Fatalf("the update after: %+v, want the index started again and all %d files read", stats, files)
		}
		checkIndexCurrent(t, e)
		checkIndexFiles(t, e)
	})
}

// A list that fails its check when a search reads it: the search goes without the index, which is then built again.
func TestSearchFilterRefusesADamagedList(t *testing.T) {
	smallSegments(t)
	e := indexArchive(t)
	updateIndex(t, e)
	_, man, _, _, err := readIndex(e.Store.searchDir())
	if err != nil || len(man.Segments) != 1 {
		t.Fatalf("the index is %+v (%v), want one segment", man, err)
	}
	p := filepath.Join(e.Store.searchDir(), man.Segments[0])
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	// Every list damaged (their first bytes), the files and keys left whole: the segment opens.
	seg, err := openSeg(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range allSegKeys(t, seg) {
		raw[seg.lists+int64(k.start)] ^= 0x80
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	e = &Engine{Store: st, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}
	if !st.SearchIndexReady() {
		t.Fatal("the segment did not open: the test no longer reaches a list's check")
	}
	if f := e.searchFilter(words("build")); f != nil {
		t.Fatal("a damaged list was believed")
	}
	if st.SearchIndexReady() || e.searchFilter(words("周报")) != nil {
		t.Fatal("the index is still used after a list failed its check")
	}
	if len(logged) != 1 {
		t.Fatalf("logged %q, want the failed check said once", logged)
	}
	for _, l := range logged {
		if strings.Contains(l, e.Store.Dir) || strings.Contains(l, "build") {
			t.Errorf("the log line %q names a path or the query", l)
		}
	}
	if stats := updateIndex(t, e); !stats.Rebuilt {
		t.Fatalf("update: %+v, want the index started again", stats)
	}
	checkIndexCurrent(t, e)
	if e.searchFilter(words("build")) == nil {
		t.Fatal("the index built again is not used")
	}
}

// Damage no search has read is found by the merge that reads every list: that update fails, and the next one
// starts the index again.
func TestUpdateSearchIndexFindsDamageWhenItMerges(t *testing.T) {
	smallSegments(t)
	e := indexArchive(t)
	updateIndex(t, e)
	_, man, _, _, err := readIndex(e.Store.searchDir())
	if err != nil || len(man.Segments) != 1 {
		t.Fatalf("the index is %+v (%v), want one segment", man, err)
	}
	p := filepath.Join(e.Store.searchDir(), man.Segments[0])
	seg, err := openSeg(p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	keys := allSegKeys(t, seg)
	raw[seg.lists+int64(keys[len(keys)/2].start)] ^= 0x80
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// A deleted file, dropped at once: a merge.
	if err := os.Remove(filepath.Join(e.Store.Dir, "chats/lark-c3/messages/2026-03-04.md")); err != nil {
		t.Fatal(err)
	}
	e.Store.SearchIndexPurge()
	if stats, err := e.UpdateSearchIndex(context.Background()); err == nil || !damaged(err) {
		t.Fatalf("update over the damaged segment: %+v, %v; want it refused as damaged", stats, err)
	}
	if e.Store.SearchIndexReady() || e.searchFilter(words("build")) != nil {
		t.Fatal("the index is still used after its merge failed a check")
	}
	if stats := updateIndex(t, e); !stats.Rebuilt {
		t.Fatalf("next update: %+v, want the index started again", stats)
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
}

// A read-only store follows the index another process writes: it reads the manifest again once it has changed, and
// a segment merged away under it sends it back to the manifest instead of to the scan for good.
func TestReadOnlyStoreFollowsTheIndex(t *testing.T) {
	was := indexFollowEvery
	t.Cleanup(func() { indexFollowEvery = was })
	indexFollowEvery = 0
	smallSegments(t)
	e := indexArchive(t)
	updateIndex(t, e)
	ro, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Engine{Store: ro}
	day := "chats/lark-c0/messages/2026-04-09.md"
	holds := func() bool {
		f := reader.searchFilter(words("zqfollow"))
		if f == nil {
			t.Fatal("the read-only store has no index to ask")
		}
		st := statFile(filepath.Join(e.Store.Dir, day))
		// The day is one the index read as it is now, and it holds the word: not ruled out, and not unknown.
		_, inLog := f.snap.log[day]
		_, inSeg := f.snap.where[day]
		return st.ok && (inLog || inSeg) && !f.skip(day, st)
	}
	if reader.searchFilter(words("build")) == nil {
		t.Fatal("the read-only store does not use the index")
	}
	writeOld(t, e, day, "# 2026-04-09\n- 09:00 同事0: zqfollow\n", 50*time.Minute)
	if err := os.Remove(filepath.Join(e.Store.Dir, "chats/lark-c3/messages/2026-03-04.md")); err != nil {
		t.Fatal(err)
	}
	e.Store.SearchIndexPurge()
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("update: %+v, want a merge", stats)
	}
	if !holds() {
		t.Fatal("the read-only store does not have the day the writer indexed")
	}
	// Without looking at the manifest: the segments it knew are gone, which it finds when it reads one.
	indexFollowEvery = time.Hour
	ro.search.looked.Store(time.Now().UnixNano())
	writeOld(t, e, day, "# 2026-04-09\n- 09:00 同事0: zqfollow again\n", 40*time.Minute)
	if err := os.Remove(filepath.Join(e.Store.Dir, "chats/lark-c3/messages/2026-03-05.md")); err != nil {
		t.Fatal(err)
	}
	e.Store.SearchIndexPurge()
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("second update: %+v, want a merge", stats)
	}
	if !holds() {
		t.Fatal("the read-only store did not read the index again after its segment was merged away")
	}
	if got, want := reader.Search("zqfollow", true, 10), searchByWalk(reader, "zqfollow", true, 10); !reflect.DeepEqual(got, want) || len(got) != 1 {
		t.Fatalf("Search of the read-only store:\n got %+v\nwant %+v", got, want)
	}
}

// An append to the log cut short (the process died) loses only that append: what was written before is used, and
// the next update reads the rest again and leaves a whole log.
func TestUpdateSearchIndexAfterATornLog(t *testing.T) {
	for _, c := range []struct {
		name string
		cut  func(logLen int64) int64
		lost func(files int) int
	}{
		{"the last entry", func(n int64) int64 { return n - 5 }, func(int) int { return 1 }},
		// The log is not synced: a crash right after it was started can leave it with nothing, or part of its head.
		{"everything", func(int64) int64 { return 0 }, func(files int) int { return files }},
		{"all but part of its head", func(int64) int64 { return 3 }, func(files int) int { return files }},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := indexArchive(t)
			updateIndex(t, e)
			_, man, logLen, _, err := readIndex(e.Store.searchDir())
			if err != nil || man.Log == "" || len(man.Segments) != 0 {
				t.Fatalf("the index is %+v (%v), want a log alone", man, err)
			}
			files := len(e.indexFiles())
			p := filepath.Join(e.Store.searchDir(), man.Log)
			if err := os.Truncate(p, c.cut(logLen)); err != nil {
				t.Fatal(err)
			}
			st, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e = &Engine{Store: st}
			lost := c.lost(files)
			if got := len(indexHolds(t, st.Dir)); got != files-lost {
				t.Fatalf("the torn log gives %d files, want %d", got, files-lost)
			}
			if stats := updateIndex(t, e); stats.Read != lost || stats.Rebuilt {
				t.Fatalf("update: %+v, want the %d lost file(s) read and nothing started again", stats, lost)
			}
			checkIndexCurrent(t, e)
			if _, _, valid, _, err := readIndex(st.searchDir()); err != nil {
				t.Fatal(err)
			} else if fi, err := os.Stat(p); err != nil || fi.Size() != valid {
				t.Fatalf("the log is %d bytes, %d of them whole entries (%v)", fi.Size(), valid, err)
			}
		})
	}
}

// A read-only store uses the index that is there and never writes one.
func TestSearchIndexOfAReadOnlyStore(t *testing.T) {
	e := indexArchive(t)
	ro, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Engine{Store: ro}).UpdateSearchIndex(context.Background()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("update of a read-only store: %v, want ErrReadOnly", err)
	}
	if ro.SearchIndexReady() || (&Engine{Store: ro}).searchFilter(words("build")) != nil {
		t.Fatal("a read-only store without an index has one")
	}
	if _, err := os.Stat(e.Store.searchDir()); !os.IsNotExist(err) {
		t.Fatalf("a read-only store made %s: %v", SearchIndexDir, err)
	}
	updateIndex(t, e)
	if ro, err = OpenReadOnly(e.Store.Dir); err != nil {
		t.Fatal(err)
	}
	if !ro.SearchIndexReady() || (&Engine{Store: ro}).searchFilter(words("build")) == nil {
		t.Fatal("a read-only store does not use the index that is there")
	}
}

// The index built again while another process still holds the one before: a reader never maps the new index's lists
// through the files table it read from the old one. The scenario of #1273's review: one segment; a read-only store
// loads it; a day is renamed (its siblings' numbers in the segment shift, no length changes); the index is removed
// and built again; the reader, which has not looked at the manifest since, finds what the files say.
func TestSearchIndexBuiltAgainUnderAReader(t *testing.T) {
	was := indexFollowEvery
	t.Cleanup(func() { indexFollowEvery = was })
	indexFollowEvery = time.Hour
	indexSizesFor(t, func() { indexSizes.logFiles = 8 })
	e := indexArchive(t)
	day := func(d int) string { return fmt.Sprintf("chats/lark-c1/messages/2026-03-%02d.md", d) }
	for d := 1; d <= 6; d++ {
		writeOld(t, e, day(d), fmt.Sprintf("# 2026-03-%02d\n- 09:00 同事1: zqday%d only here\n", d, d), 50*time.Minute)
	}
	updateIndex(t, e)
	_, man, _, _, err := readIndex(e.Store.searchDir())
	if err != nil || len(man.Segments) != 1 || man.Log != "" {
		t.Fatalf("the index is %+v (%v), want one segment", man, err)
	}
	first := man.Segments[0]
	size := statFile(filepath.Join(e.Store.searchDir(), first)).size
	ro, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Engine{Store: ro}
	check := func(step string) {
		t.Helper()
		for d := 1; d <= 7; d++ {
			q := fmt.Sprintf("zqday%d", d)
			want := findReference(&Engine{Store: ro}, q, 80)
			if got := reader.Find(q, 80); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Fatalf("%s: Find(%q) of the reader:\n got %+v\nwant %+v", step, q, got, want)
			}
		}
	}
	check("the first index")
	if reader.searchFilter(words("zqday4")) == nil {
		t.Fatal("the reader does not use the index")
	}
	// The day moves to the end of its chat: the three days after it take the numbers before theirs.
	if err := os.Rename(filepath.Join(e.Store.Dir, day(3)), filepath.Join(e.Store.Dir, day(7))); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.Store.searchDir()); err != nil {
		t.Fatal(err)
	}
	if stats := updateIndex(t, e); stats.Read != stats.Files {
		t.Fatalf("the update after the index was removed: %+v, want every file read", stats)
	}
	_, man, _, _, err = readIndex(e.Store.searchDir())
	if err != nil || len(man.Segments) != 1 {
		t.Fatalf("the index built again is %+v (%v), want one segment", man, err)
	}
	t.Logf("first index: %s, %d bytes; built again: %s, %d bytes", first, size, man.Segments[0], statFile(filepath.Join(e.Store.searchDir(), man.Segments[0])).size)
	check("the index built again")
	if ro.search.broken.Load() || reader.searchFilter(words("zqday4")) == nil {
		t.Fatal("the reader gave the index up instead of reading the one built again")
	}
}

// Searches while the index is updated, and removed under both (rm -r .search is always safe): every search gives
// what the files say, and the next update that is left alone leaves a whole index. The package's -race run checks
// here that a search's reading of the index and an update's writing of it share nothing unguarded.
//
// The directory is removed by turns, and after each removal a search is run that began after it: that one, or one
// of those running beside it, finds the index it was using gone and reads what is there instead (or the update
// comes to its failure first and marks the index, and searches go without one). The turns:
//
//   - an update has finished: updates and searches go on as they come;
//   - an update with a whole index and nothing to do has looked at the files and read none (the index's hook): it
//     stands there until the directory is gone, then goes on from what it holds, and the next update does not
//     start until the search has run;
//   - an update that builds the index again and has published a part of it has opened a file: it stands there
//     until the directory is gone and the search has run, then finds that it cannot write;
//   - the same when it has read a file;
//   - it has read a file, and goes on while the directory is emptied: it writes into it or fails, as it happens.
//
// The race detector cannot see two steps that the test itself puts in order. So the turns in which an update waits
// for the search check what searches give, and the first two are the detector's: after the look, the update's
// reading of what it holds and the search's reading of the index again wait for nothing of each other (an update
// that read the store's state there instead of its own is reported), and after a finished update nothing waits at
// all. Nothing here waits for a time to pass.
func TestSearchesWhileTheIndexIsUpdatedAndRemoved(t *testing.T) {
	smallSegments(t)
	e := indexArchive(t)
	ix, dir := &e.Store.search, e.Store.searchDir()
	// Every wait below ends with the test's deadline at the latest.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	wait := func(c <-chan struct{}) bool {
		select {
		case <-c:
			return true
		case <-ctx.Done():
			return false
		}
	}
	give := func(c chan<- struct{}) bool {
		select {
		case c <- struct{}{}:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// How long an update stands at its moment.
	const (
		goesOn        = iota // not at all
		untilGone            // until the directory is gone; no other update starts until the search has run
		untilSearched        // until the directory is gone and the search has run
	)
	type turn struct {
		moment string // of an update; none: right after one has finished
		stands int
	}
	turns := []turn{{}, {hookLooked, untilGone}, {hookOpened, untilSearched}, {hookRead, untilSearched}, {hookRead, goesOn}}
	const cycles = 6
	var due atomic.Pointer[turn]   // the turn that the next update to come to its moment takes
	reached := make(chan struct{}) // an update is at the moment that was due
	release := make(chan struct{}) // and may go on
	var parked atomic.Bool         // the update under way is the last one before resume
	resume := make(chan struct{})  // the next update may start
	// The update's goroutine calls it, and so do those that read its files: what it decides by is atomic. A moment
	// is taken when searches have a part of the index to lose there.
	ix.hook = func(moment, _ string) {
		d := due.Load()
		if d == nil || d.moment != moment {
			return
		}
		if snap := ix.current(); snap == nil || len(snap.segs) == 0 || !due.CompareAndSwap(d, nil) {
			return
		}
		if give(reached) && d.stands != goesOn {
			wait(release)
		}
	}
	queries := []string{"周报", "build", "nowhere", "draft", "进度 ok", "的", "同事1", "backend"}
	wantFind, wantSearch := map[string][]Result{}, map[string][]Hit{}
	for _, q := range queries {
		wantFind[q], wantSearch[q] = findReference(e, q, 1<<20), searchByWalk(e, q, true, 1<<20)
	}
	// What a search of q gives that the files do not say; nothing when it is right.
	search := func(q string) string {
		if got := e.Find(q, 1<<20); !reflect.DeepEqual(got, wantFind[q]) && (len(got) > 0 || len(wantFind[q]) > 0) {
			return fmt.Sprintf("Find(%q):\n got %+v\nwant %+v", q, got, wantFind[q])
		}
		if got := e.Search(q, true, 1<<20); !reflect.DeepEqual(got, wantSearch[q]) && (len(got) > 0 || len(wantSearch[q]) > 0) {
			return fmt.Sprintf("Search(%q):\n got %+v\nwant %+v", q, got, wantSearch[q])
		}
		return ""
	}
	const searchers = 4
	var stop atomic.Bool
	var searches, asked, updates atomic.Int64
	wrong := make(chan string, searchers)
	finished := make(chan struct{}) // an update has finished and the store has an index: told to whoever waits for it
	var wg sync.WaitGroup
	halt := func() {
		stop.Store(true)
		cancel()
		wg.Wait()
	}
	defer halt()
	for g := range searchers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := g; !stop.Load(); i++ {
				q := queries[i%len(queries)]
				if e.searchFilter(words(q)) != nil {
					asked.Add(1)
				}
				if msg := search(q); msg != "" {
					wrong <- msg
					cancel()
					return
				}
				searches.Add(1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			// An update whose files are removed under it fails; the one after starts again.
			if _, err := e.UpdateSearchIndex(context.Background()); err == nil {
				updates.Add(1)
				if e.Store.SearchIndexReady() {
					select {
					case finished <- struct{}{}:
					default:
					}
				}
			}
			if parked.CompareAndSwap(true, false) {
				wait(resume)
			}
		}
	}()
	removed := make([]int, len(turns)) // how often each turn removed an index that was there
	rounds := 0
	for ; rounds < cycles*len(turns) && ctx.Err() == nil; rounds++ {
		n := rounds % len(turns)
		stands := turns[n].stands
		if turns[n].moment == "" {
			if !wait(finished) {
				break
			}
		} else if due.Store(&turns[n]); !wait(reached) {
			break
		}
		was, there := ix.current(), statFile(filepath.Join(dir, indexManifest)).ok
		// The update may write a file while the directory is emptied: what was there is gone, the directory stays.
		if err := os.RemoveAll(dir); err != nil && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.EEXIST) {
			t.Fatal(err)
		}
		if stands == untilGone {
			parked.Store(true)
			if !give(release) {
				break
			}
		}
		if msg := search(queries[rounds%len(queries)]); msg != "" {
			t.Fatalf("round %d, after the directory was removed: %s", rounds, msg)
		}
		if there && was != nil && len(was.segs) > 0 {
			// Gone for the store too: a search read the index again, or the update failed first and marked it
			// (searches then go without one until the next update reads what is there).
			ix.mu.Lock()
			still := ix.current() == was && !ix.broken.Load()
			ix.mu.Unlock()
			if still {
				t.Errorf("round %d: searches still use the index that was removed: the test no longer has one find it gone", rounds)
			}
			removed[n]++
		}
		switch stands {
		case untilGone:
			give(resume)
		case untilSearched:
			give(release)
		}
	}
	late := rounds < cycles*len(turns) && errors.Is(ctx.Err(), context.DeadlineExceeded)
	due.Store(nil)
	halt()
	select {
	case msg := <-wrong:
		t.Fatal(msg)
	default:
	}
	if late {
		t.Fatalf("the test's deadline came in round %d of %d (turn %d): the update that was waited for did not come", rounds, cycles*len(turns), rounds%len(turns))
	}
	if searches.Load() == 0 || asked.Load() == 0 || updates.Load() == 0 {
		t.Fatalf("%d searches (%d with an index to ask), %d updates finished: the test no longer runs them together", searches.Load(), asked.Load(), updates.Load())
	}
	for n, tn := range turns {
		if removed[n] == 0 {
			t.Errorf("in %d rounds no index was removed in turn %d (%q): the test no longer comes to that moment", rounds, n, tn.moment)
		}
	}
	// Left alone, the index is whole again after at most two updates (the first may find what the removal left).
	if _, err := e.UpdateSearchIndex(context.Background()); err != nil {
		updateIndex(t, e)
	}
	updateIndex(t, e)
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
	for _, q := range queries {
		if got := e.Find(q, 1<<20); !reflect.DeepEqual(got, wantFind[q]) && (len(got) > 0 || len(wantFind[q]) > 0) {
			t.Fatalf("Find(%q) afterwards:\n got %+v\nwant %+v", q, got, wantFind[q])
		}
	}
}

// The writer never gives a segment the name of another. Should a file be another segment all the same (put there by
// hand, or written by a version that counted its files from one again), whoever holds the first one's files part
// reads the index again instead of reading the second one's lists through it. The second here is the first one's
// archive with a day renamed: the same keys, lengths and size, the files numbered otherwise, every checksum good.
func TestSegmentUnderAnotherOnesNameIsNotReadThroughItsFiles(t *testing.T) {
	was := indexFollowEvery
	t.Cleanup(func() { indexFollowEvery = was })
	indexFollowEvery = time.Hour
	indexSizesFor(t, func() { indexSizes.logFiles = 8 })
	e := indexArchive(t)
	day := func(d int) string { return fmt.Sprintf("chats/lark-c1/messages/2026-03-%02d.md", d) }
	for d := 1; d <= 6; d++ {
		writeOld(t, e, day(d), fmt.Sprintf("# 2026-03-%02d\n- 09:00 同事1: zqday%d only here\n", d, d), 50*time.Minute)
	}
	one := func() (string, []byte) { // the index's one segment: its path and bytes
		t.Helper()
		_, man, _, _, err := readIndex(e.Store.searchDir())
		if err != nil || len(man.Segments) != 1 || man.Log != "" {
			t.Fatalf("the index is %+v (%v), want one segment", man, err)
		}
		p := filepath.Join(e.Store.searchDir(), man.Segments[0])
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return p, raw
	}
	updateIndex(t, e)
	first, firstRaw := one()
	// Who holds the first index: a read-only store that has searched with it, and a search under way.
	ro, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	reader := &Engine{Store: ro}
	check := func(step string, eng *Engine) {
		t.Helper()
		for d := 1; d <= 7; d++ {
			q := fmt.Sprintf("zqday%d", d)
			want := findReference(&Engine{Store: eng.Store}, q, 80)
			if got := eng.Find(q, 80); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Fatalf("%s: Find(%q):\n got %+v\nwant %+v", step, q, got, want)
			}
		}
	}
	check("the first index", reader)
	held := e.Store.search.snapshot(e.Store.searchDir(), false)
	if held == nil || len(held.segs) != 1 {
		t.Fatalf("the writer's snapshot: %+v", held)
	}
	if err := os.Rename(filepath.Join(e.Store.Dir, day(3)), filepath.Join(e.Store.Dir, day(7))); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.Store.searchDir()); err != nil {
		t.Fatal(err)
	}
	updateIndex(t, e)
	second, secondRaw := one()
	if second == first || len(secondRaw) != len(firstRaw) || bytes.Equal(secondRaw, firstRaw) {
		t.Fatalf("the second segment is %s, %d bytes; the first %s, %d bytes: want another name, the same size, other bytes", filepath.Base(second), len(secondRaw), filepath.Base(first), len(firstRaw))
	}
	// The second segment under the first one's name, as the manifest then says.
	manifest := filepath.Join(e.Store.searchDir(), indexManifest)
	man, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(second, first); err != nil {
		t.Fatal(err)
	}
	matchesWrite(t, manifest, strings.Replace(string(man), filepath.Base(second), filepath.Base(first), 1))

	clauses, _ := queryClauses(words("zqday4"))
	if _, err := held.segs[0].candidates(clauses); !errors.Is(err, errSegmentOther) {
		t.Fatalf("a search under way read the other segment: %v, want errSegmentOther", err)
	}
	if _, err := held.segs[0].cursor(); !errors.Is(err, errSegmentOther) {
		t.Fatalf("a merge opened the other segment: %v, want errSegmentOther", err)
	}
	check("the second segment under the first one's name", reader)
	if ro.search.broken.Load() || reader.searchFilter(words("zqday4")) == nil {
		t.Fatal("the reader gave the index up instead of reading it again")
	}

	// The same under the process that writes: its files part is the second one's, the file becomes the first again
	// with the manifest untouched. A merge that opens it refuses; the next update, which looks at its segments
	// before it goes on from them, reads the index again (the first segment says nothing wrong of the files that are
	// still as it read them) and does its work with that.
	st, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	w := &Engine{Store: st}
	if stats := updateIndex(t, w); stats.Read != 0 || stats.Rebuilt {
		t.Fatalf("the update of a new process: %+v, want nothing to do", stats)
	}
	manStat := statFile(manifest)
	if err := os.WriteFile(first, firstRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if statFile(manifest) != manStat {
		t.Fatal("the manifest changed")
	}
	wsnap := st.search.current()
	if _, err := mergeSegs(context.Background(), filepath.Join(t.TempDir(), "merged.idx"), []mergeInput{{seg: wsnap.segs[0]}}); !errors.Is(err, errSegmentOther) || !damaged(err) {
		t.Fatalf("a merge over the other segment: %v, want errSegmentOther", err)
	}
	if err := os.Remove(filepath.Join(e.Store.Dir, "chats/lark-c3/messages/2026-03-04.md")); err != nil {
		t.Fatal(err)
	}
	w.Store.SearchIndexPurge()
	// Read again: the first segment does not have the renamed day, which is read; the purge merges.
	if stats := updateIndex(t, w); stats.Rebuilt || !stats.Merged || stats.Read != 1 {
		t.Fatalf("the update over the other segment: %+v, want the index read again, the renamed day read, a merge", stats)
	}
	if st.search.current() == wsnap {
		t.Fatal("the update went on from the files part it held")
	}
	checkIndexCurrent(t, w)
	checkIndexFiles(t, w)
	check("after the update over the other segment", w)

	// And a search of the writing process that finds another segment under the name before any merge does: it reads
	// the index again and goes on with it. The day goes back to its first name and the index is built once more by
	// another store; that segment is then put under the name of the one w2 holds.
	st2, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	w2 := &Engine{Store: st2}
	check("a new store", w2)
	third, thirdRaw := one()
	if err := os.Rename(filepath.Join(e.Store.Dir, day(7)), filepath.Join(e.Store.Dir, day(3))); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(e.Store.searchDir()); err != nil {
		t.Fatal(err)
	}
	st3, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	updateIndex(t, &Engine{Store: st3})
	fourth, fourthRaw := one()
	if len(fourthRaw) != len(thirdRaw) || bytes.Equal(fourthRaw, thirdRaw) {
		t.Fatalf("the segments are %d and %d bytes, want the same size and other bytes", len(thirdRaw), len(fourthRaw))
	}
	if man, err = os.ReadFile(manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fourth, third); err != nil {
		t.Fatal(err)
	}
	matchesWrite(t, manifest, strings.Replace(string(man), filepath.Base(fourth), filepath.Base(third), 1))
	check("the fourth segment under the third one's name", w2)
	if st2.search.broken.Load() || w2.searchFilter(words("zqday4")) == nil {
		t.Fatal("the writing process gave the index up instead of reading it again")
	}
}

// A photo deleted from a recording takes its line out of photos.md; what the index read of that line goes at the
// next update, as any text deleted on purpose (the file is still there, changed: without the purge its entry would
// stay until the next merge).
func TestPhotoDeleteDropsItsWordsFromTheSearchIndex(t *testing.T) {
	e := goldenEngine(t, nil)
	photoMeeting(t, e, recID, time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone))
	addPhoto(t, e, recID, photoUpload(1, 1, "第一张", ""), false)
	addPhoto(t, e, recID, photoUpload(2, 2, "zqphoto 第二张", ""), false)
	if err := ageFiles(e.Store.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	updateIndex(t, e)
	md := KindRecording + "/" + recID + "/" + photosText
	key := wordClause("zqphoto").all[0]
	if !indexEverHolds(t, e.Store.Dir, md, key) {
		t.Fatal("the photo's word is not in the index: the test shows nothing")
	}
	if e.Store.search.purge.Load() {
		t.Fatal("a purge is asked for before anything was deleted")
	}
	if ok, err := e.PhotoDelete(recID, 2); !ok || err != nil {
		t.Fatalf("PhotoDelete: %v, %v", ok, err)
	}
	if !e.Store.search.purge.Load() {
		t.Fatal("deleting a photo did not ask for the purge")
	}
	if got := e.Search("zqphoto", false, 10); len(got) != 0 {
		t.Fatalf("the deleted photo's word is found: %+v", got)
	}
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("the update after: %+v, want a merge", stats)
	}
	if indexEverHolds(t, e.Store.Dir, md, key) {
		t.Fatal("the index still has the deleted photo's word")
	}
}

// A topic goes when the sources of all its points are forgotten, which is the one place a topic is removed: the
// forgetting asks for the purge, so the topic's text leaves the index with the chats'.
func TestForgottenTopicLeavesTheSearchIndex(t *testing.T) {
	e := indexArchive(t)
	st := e.Store
	if err := st.PutChat(&Chat{ID: "lark-zq", Name: "zq 群", Kind: "group", Source: "lark"}); err != nil {
		t.Fatal(err)
	}
	matchesWrite(t, st.path(KindChat, "lark-zq", "messages", "2026-03-01.md"), "# 2026-03-01\n- 09:00 同事1: 每周发布 zqpoint\n")
	topic := &Topic{Slug: "zqtopic", Name: "zqtopic 发布节奏"}
	points := topicFile{Points: []TopicPoint{{ID: "p1", Text: "每周发布 zqpoint", Sources: []TopicSource{{Kind: KindChat, ID: "lark-zq"}}}}}
	if err := st.writeTopic(topic, points, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	updateIndex(t, e)
	var name string
	for n := range indexNames(t, st.Dir) {
		if strings.HasPrefix(n, KindTopic+"/zqtopic/") {
			name = n
		}
	}
	if name == "" {
		t.Fatal("the topic is not in the index: the test shows nothing")
	}
	got, err := e.ForgetSource("lark")
	if err != nil || !slices.Contains(got.Items[KindTopic], "zqtopic") {
		t.Fatalf("ForgetSource: %+v, %v; want the topic removed", got, err)
	}
	if !st.search.purge.Load() {
		t.Fatal("forgetting the topic's sources did not ask for the purge")
	}
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("the update after: %+v, want a merge", stats)
	}
	for n := range indexNames(t, st.Dir) {
		if strings.HasPrefix(n, KindTopic+"/") || strings.HasPrefix(n, KindChat+"/lark-zq/") {
			t.Errorf("the index still names %s", n)
		}
	}
	checkIndexCurrent(t, e)
}

// An update asked to stop while it looks at the files, or while it merges, stops there, not only between the
// batches it reads: nothing more is published, no file of its own is left, the purge it was asked for is still
// asked for, and the next update does the work.
func TestUpdateSearchIndexStopsInTheLookAndTheMerge(t *testing.T) {
	smallSegments(t)
	indexSizesFor(t, func() { indexSizes.lookAsks, indexSizes.mergeAsks = 1, 1 })
	e := indexArchive(t)
	manifest := filepath.Join(e.Store.searchDir(), indexManifest)
	// In the look: the context has ended before the first file is looked at.
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	if stats, err := e.UpdateSearchIndex(ended); !errors.Is(err, context.Canceled) || stats.Files != 0 || stats.Read != 0 {
		t.Fatalf("an update whose context has ended: %+v, %v; want it stopped before it looked at a file", stats, err)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("the stopped update published an index: %v", err)
	}
	updateIndex(t, e)
	checkIndexCurrent(t, e)
	before, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	// In the merge: a day deleted on purpose is a merge at the next update; its context ends at the merge's third
	// key (the look asks nothing of it here, the update once before it merges).
	gone := "chats/lark-c3/messages/2026-03-04.md"
	if err := os.Remove(filepath.Join(e.Store.Dir, gone)); err != nil {
		t.Fatal(err)
	}
	e.Store.SearchIndexPurge()
	indexSizesFor(t, func() { indexSizes.lookAsks = 1 << 20 })
	ctx := &stopAfter{Context: context.Background()}
	ctx.left.Store(3)
	stats, err := e.UpdateSearchIndex(ctx)
	if !errors.Is(err, context.Canceled) || stats.Merged {
		t.Fatalf("an update stopped in its merge: %+v, %v; want it stopped, nothing merged", stats, err)
	}
	if ctx.left.Load() > -1 {
		t.Fatalf("the merge asked its context %d times fewer than the test counts on", ctx.left.Load()+1)
	}
	if after, err := os.ReadFile(manifest); err != nil || !bytes.Equal(after, before) {
		t.Fatalf("the stopped merge changed the manifest (%v):\n%s\n%s", err, before, after)
	}
	checkIndexFiles(t, e) // the segment it was writing is gone
	if !e.Store.search.purge.Load() {
		t.Fatal("the purge is no longer asked for after the merge that was stopped")
	}
	if !indexNames(t, e.Store.Dir)[gone] {
		t.Fatal("the deleted day's entry is gone already: the test no longer shows the next update dropping it")
	}
	if got, want := e.Find("build", 80), findReference(e, "build", 80); !reflect.DeepEqual(got, want) {
		t.Fatalf("Find after the stopped merge:\n got %+v\nwant %+v", got, want)
	}
	if stats := updateIndex(t, e); !stats.Merged {
		t.Fatalf("the update after: %+v, want the merge", stats)
	}
	if indexNames(t, e.Store.Dir)[gone] || e.Store.search.purge.Load() {
		t.Fatal("the deleted day's entry is still in the index after the merge")
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
}

// An index started again after a failed check starts from nothing: what the store counted of the one before (the
// entries of its log that later ones replaced, which bring the next merge forward) is not counted in the new one.
func TestSearchIndexStartedAgainCountsNothingOfTheOneBefore(t *testing.T) {
	e := indexArchive(t)
	updateIndex(t, e)
	day := "chats/lark-c0/messages/2026-03-01.md"
	for i := range 3 {
		writeOld(t, e, day, fmt.Sprintf("# 2026-03-01\n- 09:00 同事0: 改了 %d\n", i), time.Duration(50-i)*time.Minute)
		if stats := updateIndex(t, e); stats.Read != 1 || stats.Merged {
			t.Fatalf("update %d: %+v, want the day read into the log", i, stats)
		}
	}
	ix := &e.Store.search
	if st := ix.state.Load(); st.logOld != 3 || st.logLen == 0 {
		t.Fatalf("the log has %d replaced entries in %d bytes, want 3", st.logOld, st.logLen)
	}
	ix.broken.Store(true) // as a search's failed check leaves it
	if stats := updateIndex(t, e); !stats.Rebuilt || stats.Merged || stats.Read != stats.Files {
		t.Fatalf("the update after the failed check: %+v, want the index started again", stats)
	}
	if st := ix.state.Load(); st.logOld != 0 || st.logLen == 0 || st.man.Seq != 1 {
		t.Fatalf("the index started again: %d replaced entries, %d bytes of log, %d files named; want 0, some, 1", st.logOld, st.logLen, st.man.Seq)
	}
	checkIndexCurrent(t, e)
	checkIndexFiles(t, e)
}

// swappedDay is a day of the archive that a test replaces under an update: p holds first as the update's look sees
// it (was); other is text of the same size. Its steps are called from the update's goroutines, so they report with
// Error.
type swappedDay struct {
	t            *testing.T
	p, aside     string
	first, other string
	was          fileTime
}

func (f *swappedDay) do(err error) {
	if err != nil {
		f.t.Error(err)
	}
}

// moveAway moves the file with the first text aside (its mtime stays) and puts the other text in its place, as
// another file with another mtime.
func (f *swappedDay) moveAway() {
	f.do(os.Rename(f.p, f.aside))
	f.do(os.WriteFile(f.p, []byte(f.other), 0o600))
	at := time.Unix(0, f.was.mod).Add(5 * time.Minute)
	f.do(os.Chtimes(f.p, at, at))
}

// writeOther writes the other text into the file that is there (the same file: whoever has it open reads it).
func (f *swappedDay) writeOther() { f.do(os.WriteFile(f.p, []byte(f.other), 0o600)) }

// copyAside keeps a copy of the first text aside, with its mtime: what a backup holds.
func (f *swappedDay) copyAside() {
	f.do(os.WriteFile(f.aside, []byte(f.first), 0o600))
	f.do(os.Chtimes(f.aside, time.Unix(0, f.was.mod), time.Unix(0, f.was.mod)))
}

// moveBack moves the file aside into place: the first text under its own mtime, as a rename or a tool that
// restores by writing beside and renaming leaves it.
func (f *swappedDay) moveBack() { f.do(os.Rename(f.aside, f.p)) }

// writeBack writes the first text into the file that is there and gives it its mtime back, as a tool that restores
// in place does.
func (f *swappedDay) writeBack() {
	f.do(os.WriteFile(f.p, []byte(f.first), 0o600))
	f.do(os.Chtimes(f.p, time.Unix(0, f.was.mod), time.Unix(0, f.was.mod)))
}

// restore leaves the first text in place under its own mtime, however far the case got.
func (f *swappedDay) restore() {
	f.t.Helper()
	if _, err := os.Stat(f.aside); err == nil {
		f.moveBack()
	} else if raw, err := os.ReadFile(f.p); err != nil || string(raw) != f.first {
		f.writeBack()
	}
	if raw, err := os.ReadFile(f.p); err != nil || string(raw) != f.first || statFile(f.p) != f.was {
		f.t.Fatalf("the first text is not back as it was: %q (%v), %+v, want %+v", raw, err, statFile(f.p), f.was)
	}
}

// An update looks at every file first and reads later, so the file it reads can be another than the one it looked
// at. What it keeps must still be true: the bytes that had this mtime and size have these keys. Here a day is
// replaced by text of the same size once the update has looked, and the first text is back under its own mtime
// afterwards: moved back, or put back by a sync or backup tool, as another file or into the one that is there. Were
// the other text's keys kept under the first one's mtime and size, the day would be ruled out for the first one's
// words from then on (#1273's second review). In every case a search gives what reading the files gives: at once,
// after two more updates, and in a new process; and the update keeps the day only when what it read is the first
// text.
func TestUpdateSearchIndexKeepsOnlyTheFileItLookedAt(t *testing.T) {
	const day = "chats/lark-c2/messages/2026-03-03.md"
	const first = "# 2026-03-03\n- 09:00 同事2: zqfirst only here\n"
	const other = "# 2026-03-03\n- 09:00 同事2: zqother only here\n"
	if len(first) != len(other) {
		t.Fatal("the two texts are not of one size")
	}
	cases := []struct {
		name string
		// at is what happens to the day at a moment of the update.
		at func(f *swappedDay, moment string)
		// reach: the moments the update must be at for the day, or the case replaces nothing.
		reach []string
		// kept: what the update read is the first text, which had the mtime and size it looked at: it may keep it.
		kept bool
		// segments: also with an update that writes segments (the read is the same; what takes the files is not).
		segments bool
	}{
		{
			// #1273's second review: nothing tells the read that the file is not the one looked at, but the file.
			name: "replaced before it is opened, the first moved back afterwards",
			at: func(f *swappedDay, moment string) {
				if moment == hookLooked {
					f.moveAway()
				}
			},
			reach:    []string{hookLooked},
			segments: true,
		},
		{
			// What only the look at the open file before it is read tells: the other text is read, and by the time
			// the file is asked again it is the first one's, written back into it with its mtime.
			name: "replaced before it is opened, the first written back in place once it is read",
			at: func(f *swappedDay, moment string) {
				switch moment {
				case hookLooked:
					f.moveAway()
				case hookRead:
					f.do(os.Remove(f.aside))
					f.writeBack()
				}
			},
			reach: []string{hookLooked},
		},
		{
			// What only the look at the open file after it is read tells, and only asked of the file, not of its
			// path: the other text was written into it, and the path is a copy of the first one's by then.
			name: "written in place once it is opened, a copy of the first moved in once it is read",
			at: func(f *swappedDay, moment string) {
				switch moment {
				case hookOpened:
					f.copyAside()
					f.writeOther()
				case hookRead:
					f.moveBack()
				}
			},
			reach: []string{hookOpened, hookRead},
		},
		{
			// The file read is the one opened: moved away, it is still the first text, and that is kept, under the
			// mtime and size that were its own.
			name: "moved away once it is opened, moved back afterwards",
			at: func(f *swappedDay, moment string) {
				if moment == hookOpened {
					f.moveAway()
				}
			},
			reach: []string{hookOpened, hookRead},
			kept:  true,
		},
	}
	check := func(t *testing.T, e *Engine, step string) {
		t.Helper()
		for _, q := range []string{"zqfirst", "zqother"} {
			if got, want := e.Find(q, 80), findReference(e, q, 80); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Errorf("%s: Find(%q):\n got %+v\nwant %+v", step, q, got, want)
			}
			if got, want := e.Search(q, true, 10), searchByWalk(e, q, true, 10); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Errorf("%s: Search(%q):\n got %+v\nwant %+v", step, q, got, want)
			}
			if got, want := e.Matches(KindChat, "lark-c2", q, 10), matchesReadingEveryFile(e, KindChat, "lark-c2", q, 10); !reflect.DeepEqual(got, want) && (len(got) > 0 || len(want) > 0) {
				t.Errorf("%s: Matches(%q):\n got %+v\nwant %+v", step, q, got, want)
			}
		}
		if got := findReference(e, "zqfirst", 80); len(got) != 1 {
			t.Fatalf("%s: reading the files finds the first text's word in %d entries, want 1", step, len(got))
		}
	}
	for _, c := range cases {
		for _, mode := range []string{"log", "segments"} {
			if mode == "segments" && !c.segments {
				continue
			}
			t.Run(c.name+"/"+mode, func(t *testing.T) {
				if mode == "segments" {
					smallSegments(t)
				}
				e := dayArchive(t, 4)
				writeOld(t, e, day, first, 50*time.Minute)
				p := filepath.Join(e.Store.Dir, filepath.FromSlash(day))
				f := &swappedDay{t: t, p: p, aside: filepath.Join(t.TempDir(), "aside.md"), first: first, other: other, was: statFile(p)}
				files := len(e.indexFiles())
				var mu sync.Mutex
				reached := map[string]bool{}
				e.Store.search.hook = func(moment, name string) {
					if name != "" && name != day {
						return
					}
					mu.Lock()
					defer mu.Unlock()
					reached[moment] = true
					c.at(f, moment)
				}
				stats := updateIndex(t, e)
				e.Store.search.hook = nil
				for _, m := range c.reach {
					if !reached[m] {
						t.Fatalf("the update was never at %q for the day: the case replaces nothing", m)
					}
				}
				held, ok := indexHolds(t, e.Store.Dir)[day]
				f.restore()
				check(t, e, "after the update")
				if ok && held.mod == f.was.mod && held.size == f.was.size && !slices.Equal(held.keys, fileGrams(fold(first))) {
					t.Errorf("the index has the day by the first text's mtime and size, with keys that are not its text's")
				}
				if want := files - 1; !c.kept && (ok || stats.Read != want || stats.Waiting != 1) {
					t.Errorf("the update kept what it read of a file that was not the one it looked at (in the index: %v): %+v, want %d read and 1 waiting", ok, stats, want)
				}
				if c.kept && (!ok || stats.Read != files || stats.Waiting != 0) {
					t.Errorf("the update did not keep the file it looked at and read (in the index: %v): %+v, want %d read", ok, stats, files)
				}
				updateIndex(t, e)
				updateIndex(t, e)
				check(t, e, "after two more updates")
				again, err := NewStore(e.Store.Dir)
				if err != nil {
					t.Fatal(err)
				}
				e = &Engine{Store: again}
				check(t, e, "a new store")
				checkIndexCurrent(t, e)
			})
		}
	}
}

// damageEveryList damages the first byte of every list of the segment at p and leaves its files and keys whole:
// the segment opens, and no list of it passes its check.
func damageEveryList(t *testing.T, p string) {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	seg, err := openSeg(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range allSegKeys(t, seg) {
		raw[seg.lists+int64(k.start)] ^= 0x80
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A search that finds the index damaged says so of the index it read, never of another: by the time it says it,
// the index may have been built again and read by someone else. Said of a read-only store that holds nothing at
// that moment (another search's look at the manifest has just dropped what it held), "broken" was for good: the
// look compared the manifest with a state that was not there, and nothing else read the index again (#1273's second
// review). Said of the writer's new index, it cost a second rebuild.
func TestSearchMarksOnlyTheIndexItReadAsDamaged(t *testing.T) {
	was := indexFollowEvery
	t.Cleanup(func() { indexFollowEvery = was })
	// one segment with every list damaged, and the store that wrote it
	damagedIndex := func(t *testing.T) *Engine {
		t.Helper()
		smallSegments(t)
		e := dayArchive(t, 3)
		updateIndex(t, e)
		_, man, _, _, err := readIndex(e.Store.searchDir())
		if err != nil || len(man.Segments) != 1 {
			t.Fatalf("the index is %+v (%v), want one segment", man, err)
		}
		return e
	}
	// rebuild is the writer starting the index again, as after rm -r .search; the new manifest is not the old one
	// to whoever compares their mtimes, however fast it was written.
	rebuild := func(t *testing.T, e *Engine) {
		t.Helper()
		manifest := filepath.Join(e.Store.searchDir(), indexManifest)
		before := statFile(manifest)
		if err := os.RemoveAll(e.Store.searchDir()); err != nil {
			t.Error(err)
		}
		if stats, err := e.UpdateSearchIndex(context.Background()); err != nil || stats.Read != stats.Files {
			t.Errorf("building the index again: %+v, %v", stats, err)
		}
		if statFile(manifest) == before {
			t.Error("the manifest written again has the mtime and size of the one before: the test would not tell them apart")
		}
	}
	t.Run("a read-only store whose other search has just dropped what it held", func(t *testing.T) {
		indexFollowEvery = time.Hour
		e := damagedIndex(t)
		ro, err := OpenReadOnly(e.Store.Dir)
		if err != nil {
			t.Fatal(err)
		}
		reader := &Engine{Store: ro}
		if reader.searchFilter(words("build")) == nil {
			t.Fatal("the read-only store does not use the index")
		}
		_, man, _, _, _ := readIndex(e.Store.searchDir())
		damageEveryList(t, filepath.Join(e.Store.searchDir(), man.Segments[0]))
		var found, dropped atomic.Bool
		looked, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
		ro.search.hook = func(moment, _ string) {
			switch {
			case moment == hookDamaged && found.CompareAndSwap(false, true):
				// The first search has found the damage and not said so yet. The writer builds the index again, and
				// a second search's look sees the new manifest and drops the state: it stops there.
				rebuild(t, e)
				indexFollowEvery = 0
				go func() {
					defer close(done)
					reader.searchFilter(words("build"))
				}()
				select {
				case <-looked:
				case <-time.After(10 * time.Second):
					t.Error("the second search never looked at the manifest")
				}
			case moment == hookFollowed && dropped.CompareAndSwap(false, true):
				close(looked)
				<-release
			}
		}
		first := reader.searchFilter(words("build"))
		close(release)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the second search never ended")
		}
		if !found.Load() || !dropped.Load() {
			t.Fatalf("the first search found damage: %v, the second dropped the state: %v; the test no longer drives the two", found.Load(), dropped.Load())
		}
		if first == nil || ro.search.broken.Load() {
			t.Errorf("the search that found the old index damaged said so of another: it got an index: %v, broken: %v", first != nil, ro.search.broken.Load())
		}
		// Whatever the two did, the index on disk is whole, and the store uses it: now and at every later look.
		for i := range 3 {
			if !ro.SearchIndexReady() || reader.searchFilter(words("build")) == nil {
				t.Fatalf("look %d after the two searches: the read-only store has no index (state held: %v, broken: %v), and the one on disk is whole",
					i+1, ro.search.state.Load() != nil, ro.search.broken.Load())
			}
		}
		if got, want := reader.Find("进度", 80), findReference(reader, "进度", 80); len(want) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("Find of the read-only store:\n got %+v\nwant %+v", got, want)
		}
	})
	// The state that was for good, however a store came to it: nothing held and marked broken. Nothing leaves a
	// store so any more; were one left so, its next look at the manifest reads the index.
	t.Run("a read-only store that holds nothing and is marked broken", func(t *testing.T) {
		indexFollowEvery = 0
		e := dayArchive(t, 3)
		updateIndex(t, e)
		ro, err := OpenReadOnly(e.Store.Dir)
		if err != nil {
			t.Fatal(err)
		}
		reader := &Engine{Store: ro}
		ro.search.mu.Lock()
		ro.search.state.Store(nil)
		ro.search.broken.Store(true)
		ro.search.mu.Unlock()
		if !ro.SearchIndexReady() || reader.searchFilter(words("build")) == nil {
			t.Fatal("the read-only store has no index at its next look, and the one on disk is whole")
		}
	})
	// An update's failed check is of the index the update works with. Here the index is removed under it and a
	// search reads what is there instead (nothing): the update fails for want of its files, and marks nothing.
	t.Run("the writer, whose index was removed and read again while its update ran", func(t *testing.T) {
		e := damagedIndex(t)
		if err := os.Remove(filepath.Join(e.Store.Dir, "chats/lark-c2/messages/2026-03-02.md")); err != nil {
			t.Fatal(err)
		}
		e.Store.SearchIndexPurge() // the update merges, which reads every segment
		var removed atomic.Bool
		e.Store.search.hook = func(moment, _ string) {
			if moment == hookLooked && removed.CompareAndSwap(false, true) {
				if err := os.RemoveAll(e.Store.searchDir()); err != nil {
					t.Error(err)
				}
				if e.searchFilter(words("build")) != nil {
					t.Error("a search has an index to ask after it was removed")
				}
			}
		}
		stats, err := e.UpdateSearchIndex(context.Background())
		e.Store.search.hook = nil
		if !removed.Load() || err == nil || !damaged(err) {
			t.Fatalf("the update over the removed index: %+v, %v; want it to fail for want of its files", stats, err)
		}
		if e.Store.search.broken.Load() {
			t.Error("the update marked the index a search read since (none) as having failed a check")
		}
		files := len(e.indexFiles())
		if stats := updateIndex(t, e); stats.Rebuilt || stats.Read != files {
			t.Fatalf("the update after: %+v, want all %d files read into a new index, with nothing to start again", stats, files)
		}
		checkIndexCurrent(t, e)
		checkIndexFiles(t, e)
	})
	t.Run("the writer, whose update has built the index again", func(t *testing.T) {
		e := damagedIndex(t)
		_, man, _, _, _ := readIndex(e.Store.searchDir())
		damageEveryList(t, filepath.Join(e.Store.searchDir(), man.Segments[0]))
		// A new store, as a process started over the damaged index: its searches read the segment's lists.
		st, err := NewStore(e.Store.Dir)
		if err != nil {
			t.Fatal(err)
		}
		e = &Engine{Store: st}
		var found atomic.Bool
		st.search.hook = func(moment, _ string) {
			if moment == hookDamaged && found.CompareAndSwap(false, true) {
				rebuild(t, e)
			}
		}
		first := e.searchFilter(words("build"))
		if !found.Load() {
			t.Fatal("the search found no damage: the test no longer drives it")
		}
		if first == nil || st.search.broken.Load() || !st.SearchIndexReady() {
			t.Fatalf("the search that found the old index damaged said so of the one built since: it got an index: %v, broken: %v", first != nil, st.search.broken.Load())
		}
		if stats := updateIndex(t, e); stats.Rebuilt || stats.Read != 0 {
			t.Fatalf("the update after: %+v, want nothing to do: the index was whole", stats)
		}
		checkIndexCurrent(t, e)
	})
}

// manifestKind says what a manifest's bytes are to the decoder, next to the manifest man they were made from: what
// kind of error, or which fields say something else (and whether that is nothing).
func manifestKind(man indexManifestFile, raw []byte) string {
	var got indexManifestFile
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Sprintf("does not decode (%T)", err)
	}
	var other []string
	say := func(field string, same, zero bool) {
		switch {
		case same:
		case zero:
			other = append(other, field+" (none)")
		default:
			other = append(other, field)
		}
	}
	say("format", got.Format == man.Format, got.Format == 0)
	say("tables", got.Tables == man.Tables, got.Tables == "")
	say("seq", got.Seq == man.Seq, got.Seq == 0)
	say("segments", slices.Equal(got.Segments, man.Segments), len(got.Segments) == 0)
	say("log", got.Log == man.Log, got.Log == "")
	say("merged", got.Merged.Equal(man.Merged), got.Merged.IsZero())
	say("complete", got.Complete == man.Complete, !got.Complete)
	if len(other) == 0 {
		return "decodes, and says the same"
	}
	return "decodes, with another " + strings.Join(other, ", ")
}

// Whatever one flipped bit makes of the manifest, it is a manifest or it is damage, and the index is whole after
// the next update, or after the one that follows an update that found the damage: the index as it was, or one
// built again from the files. Never an update that fails twice in a row: that store would have no index until
// someone removed .search, and say so once (#1273's second review: a time that does not parse was neither a syntax
// error nor a type error, so not damage).
//
// Every flip is decoded; an update is run over the first two flips of each kind (manifestKind), and over every
// flip with PERSONA_KB_DIFF_DEPTH=2.
func TestUpdateSearchIndexAfterAnyFlipOfItsManifest(t *testing.T) {
	indexSizesFor(t, func() { indexSizes.logFiles = 2 })
	e := dayArchive(t, 3)
	st := e.Store
	updateIndex(t, e)
	writeOld(t, e, "chats/lark-c2/messages/2026-03-01.md", "# 2026-03-01\n- 09:00 同事2: 改了\n", 50*time.Minute)
	updateIndex(t, e)
	dir := st.searchDir()
	_, man, _, _, err := readIndex(dir)
	if err != nil || len(man.Segments) != 1 || man.Log == "" || man.Merged.IsZero() || !man.Complete {
		t.Fatalf("the index is %+v (%v), want one segment, a log, a time and complete", man, err)
	}
	checkIndexCurrent(t, e)
	// The index as it is, to put back before every flip.
	whole := map[string][]byte{}
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ents {
		if whole[d.Name()], err = os.ReadFile(filepath.Join(dir, d.Name())); err != nil {
			t.Fatal(err)
		}
	}
	raw := whole[indexManifest]
	put := func(manifest []byte) {
		t.Helper()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, data := range whole {
			if name == indexManifest {
				data = manifest
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	every := diffDepth() >= 2
	kinds := map[string]int{}
	flips, ran, notDamage := 0, 0, 0
	for i := range raw {
		for _, mask := range []byte{0x01, 0x10, 0x80} {
			flipped := bytes.Clone(raw)
			flipped[i] ^= mask
			flips++
			kind := manifestKind(man, flipped)
			if _, err := decodeManifest(flipped); err != nil && !damaged(err) {
				// No update builds the index again over an error that is not damage: it comes back at every try.
				if notDamage++; notDamage <= 3 {
					t.Errorf("byte %d ^ %#02x (%s): reading the manifest fails with %q, which is not taken as damage", i, mask, kind, err)
				}
			}
			if kinds[kind]++; kinds[kind] > 2 && !every {
				continue
			}
			ran++
			put(flipped)
			st, err := NewStore(st.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e := &Engine{Store: st}
			if _, err := e.UpdateSearchIndex(context.Background()); err != nil {
				if _, again := e.UpdateSearchIndex(context.Background()); again != nil {
					t.Fatalf("byte %d ^ %#02x (%s): two updates in a row failed: %v; then: %v", i, mask, kind, err, again)
				}
			}
			if !st.SearchIndexReady() {
				t.Fatalf("byte %d ^ %#02x (%s): no index after the update", i, mask, kind)
			}
			checkIndexCurrent(t, e)
			checkIndexFiles(t, e)
		}
	}
	if notDamage > 3 {
		t.Errorf("and %d more flips of the %d", notDamage-3, flips)
	}
	names := make([]string, 0, len(kinds))
	for kind, n := range kinds {
		names = append(names, fmt.Sprintf("%d %s", n, kind))
	}
	sort.Strings(names)
	t.Logf("%d flips of a manifest of %d bytes decoded, updates run over %d of them; by kind:\n  %s", flips, len(raw), ran, strings.Join(names, "\n  "))
}

// The names the index gives its files, without what is new in them at every write, are the kinds a log of its
// failures tells files apart by (SearchIndexFileKind): two files of a kind are one, and no two kinds are.
func TestSearchIndexFileKindOfTheNamesTheIndexGives(t *testing.T) {
	dir := t.TempDir()
	u := &indexUpdate{dir: dir}
	tmp := func() string {
		f, err := os.CreateTemp(dir, indexManifest+".tmp-*") // as writeIndexFile names it
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		return f.Name()
	}
	kinds := map[string]string{}
	for want, names := range map[string][2]string{
		"seg-*.idx":           {u.name("seg", ".idx"), u.name("seg", ".idx")},
		"seg-*.idx.tmp":       {u.name("seg", ".idx") + ".tmp", u.name("seg", ".idx") + ".tmp"}, // segWriter's keys part
		"log-*.log":           {u.name("log", ".log"), u.name("log", ".log")},
		"manifest.json.tmp-*": {tmp(), tmp()},
		indexManifest:         {indexManifest, indexManifest},
		SearchIndexDir:        {SearchIndexDir, SearchIndexDir},
	} {
		if names[0] == names[1] && strings.Contains(want, "*") {
			t.Fatalf("%s: the two names are one: %s", want, names[0])
		}
		for _, name := range names {
			if !indexOwned(filepath.Base(name)) && want != SearchIndexDir {
				t.Errorf("%s is not a name of the index's files", name)
			}
			if got := SearchIndexFileKind(filepath.Join(dir, name)); got != want {
				t.Errorf("SearchIndexFileKind(%q) = %q, want %q", name, got, want)
			}
		}
		if other, ok := kinds[want]; ok {
			t.Errorf("%s and %s are one kind", other, names[0])
		}
		kinds[want] = names[0]
	}
}
