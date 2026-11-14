package kb

import (
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A synthetic archive a little over the size the search was written for (~33 MB of text in ~37k files; the real one
// is ~25 MB, docs/assistant/knowledge.md): benchChats chats with a year of day files each, plus people, projects and docs.
// Built once per test binary; every benchmark reads it.
const (
	benchChats  = 100
	benchDays   = 365
	benchLines  = 12 // lines per day file (~60 bytes each, Chinese and ASCII mixed)
	benchPeople = 300
	benchDocs   = 200
	// benchProjects is how many projects it has.
	benchProjects = 20
)

var (
	benchOnce sync.Once
	benchDir  string
	benchErr  error
)

func benchStore(tb testing.TB) *Store {
	tb.Helper()
	benchOnce.Do(func() {
		benchDir, benchErr = os.MkdirTemp("", "kb-find-bench-")
		if benchErr != nil {
			return
		}
		benchErr = fillBenchStore(benchDir)
	})
	if benchErr != nil {
		tb.Fatal(benchErr)
	}
	st, err := NewStore(benchDir)
	if err != nil {
		tb.Fatal(err)
	}
	return st
}

// benchIndexed is the bench archive with its search index built, as the manager's index lane keeps it: what the
// benchmarks measure. (The tests that read the bench archive take it without one, benchStore.)
func benchIndexed(tb testing.TB) *Store {
	tb.Helper()
	st := benchStore(tb)
	benchIndexOnce.Do(func() { _, benchIndexErr = (&Engine{Store: st}).UpdateSearchIndex(context.Background()) })
	if benchIndexErr != nil {
		tb.Fatal(benchIndexErr)
	}
	return st
}

var (
	benchIndexOnce sync.Once
	benchIndexErr  error
)

func fillBenchStore(dir string) error {
	return fillArchive(dir, archiveShape{chats: benchChats, days: benchDays, stride: 1, lines: benchLines,
		start: time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)})
}

// archiveShape is a synthetic archive's size: chats with days day files each, one every stride days from start, of
// lines lines; the people, projects and docs are the same in every shape. varied: every message says something else
// (variedText) instead of the one repeated line.
type archiveShape struct {
	chats, days, stride, lines int
	start                      time.Time
	varied                     bool
	// How many people, docs and projects; zero: the bench archive's (benchPeople, benchDocs, benchProjects).
	people, docs, projects int
}

// variedText writes message texts of about the repeated line's length out of skewed vocabularies: a few characters
// and words on most lines, a long tail of rare ones, as in real text. The repeated line gives a search index a few
// thousand distinct keys whatever the archive's size; this gives it the many a real archive does. Every text has
// "build", as the repeated line has. The same texts every time (a fixed seed).
type variedText struct {
	rng  *rand.Rand
	han  *rand.Zipf
	word *rand.Zipf
}

func newVariedText() *variedText {
	rng := rand.New(rand.NewPCG(1273, 4))
	return &variedText{rng: rng, han: rand.NewZipf(rng, 1.1, 4, 3499), word: rand.NewZipf(rng, 1.1, 4, 19999)}
}

func (v *variedText) write(sb *strings.Builder) {
	for range 8 + v.rng.IntN(8) {
		sb.WriteRune(rune(0x4E00 + v.han.Uint64()*5))
	}
	sb.WriteString(" build")
	for range 2 {
		sb.WriteByte(' ')
		// A word is its rank written in syllables, so the common ones are short.
		for n := v.word.Uint64() + 100; n > 0; n /= 100 {
			sb.WriteByte("bcdfghjklmnpqrstvwxz"[n%100/5])
			sb.WriteByte("aeiou"[n%5])
		}
	}
}

// fillArchive writes a synthetic archive of the shape into dir: Chinese and ASCII mixed, about 1% of the days with
// the rare word, every file's mtime an hour back.
func fillArchive(dir string, shape archiveShape) error {
	st, err := NewStore(dir)
	if err != nil {
		return err
	}
	start := shape.start
	var varied *variedText
	if shape.varied {
		varied = newVariedText()
	}
	for c := range shape.chats {
		ch := &Chat{ID: fmt.Sprintf("lark-c%03d", c), Name: fmt.Sprintf("群聊 %03d", c), Kind: "group",
			Tagging: Tagging{Importance: fmt.Sprintf("P%d", c%4), Relevance: c % 100}, LastAt: start.AddDate(0, 0, shape.days*shape.stride)}
		if err := st.PutChat(ch); err != nil {
			return err
		}
		if err := os.WriteFile(st.path(KindChat, ch.ID, "summary.md"), []byte("# "+ch.Name+"\n\n讨论发布计划和日常事务。\n"), 0o600); err != nil {
			return err
		}
		msgs := st.path(KindChat, ch.ID, "messages")
		if err := os.MkdirAll(msgs, 0o700); err != nil {
			return err
		}
		var sb strings.Builder
		for d := range shape.days {
			day := start.AddDate(0, 0, d*shape.stride).Format("2006-01-02")
			sb.Reset()
			sb.WriteString("# " + day + "\n")
			for l := range shape.lines {
				fmt.Fprintf(&sb, "- %02d:%02d 同事%d: ", 9+l/6, (l*7)%60, l%5)
				switch {
				case (c*shape.days+d)%97 == 0 && l == 3: // about 1% of the days mention the rare word
					sb.WriteString("Quarterly 周报 draft is ready")
				case varied != nil:
					varied.write(&sb)
				default:
					sb.WriteString("今天的进度同步一下 build ok, next step review")
				}
				fmt.Fprintf(&sb, " #%d\n", d*shape.lines+l)
			}
			if err := os.WriteFile(filepath.Join(msgs, day+".md"), []byte(sb.String()), 0o600); err != nil {
				return err
			}
		}
	}
	for p := range cmp.Or(shape.people, benchPeople) {
		if err := st.PutPerson(&Person{ID: fmt.Sprintf("lark-u%03d", p), Name: fmt.Sprintf("同事 %03d", p), Note: "负责后端"}); err != nil {
			return err
		}
	}
	for i := range cmp.Or(shape.docs, benchDocs) {
		if err := st.PutItem(&Item{Kind: KindDoc, ID: fmt.Sprintf("d%03d", i), Title: fmt.Sprintf("设计文档 %03d", i), At: start}); err != nil {
			return err
		}
	}
	for i := range cmp.Or(shape.projects, benchProjects) {
		if err := st.PutProject(&Project{Slug: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("项目 %02d", i), Kind: "short", Status: "active"}); err != nil {
			return err
		}
	}
	return ageFiles(dir, time.Hour)
}

// ageFiles sets the mtime of every file and directory under dir back by d: a real archive's files are mostly old,
// and Find does not keep a file or listing changed in the last seconds.
func ageFiles(dir string, d time.Duration) error {
	at := time.Now().Add(-d)
	return filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
}

func TestMain(m *testing.M) {
	code := m.Run()
	if benchDir != "" {
		_ = os.RemoveAll(benchDir)
	}
	os.Exit(code)
}

// BenchmarkFind is search-as-you-type: the same engine answers one query after another (the app sends one per
// keystroke), the archive unchanged in between. typing is the keystrokes of a rare word; the others repeat one query:
// a rare word (about 1% of the days: the index leaves only those to read), a word on every message line (every chat
// far past foundCap: its newest days are read) and a word no file has (the index rules every file out).
func BenchmarkFind(b *testing.B) {
	e := &Engine{Store: benchIndexed(b)}
	if res := e.Find("周报", 80); len(res) == 0 {
		b.Fatal("the rare word is not found")
	}
	for _, c := range []struct {
		name    string
		queries []string
	}{
		{"typing", []string{"周", "周报", "周报 draft"}},
		{"rare", []string{"周报 draft"}},
		{"every", []string{"build"}},
		{"none", []string{"nowhere"}},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; b.Loop(); i++ {
				e.Find(c.queries[i%len(c.queries)], 80)
			}
		})
	}
}

// BenchmarkFindCold is the first search of a new engine (nothing kept from an earlier one; the index is the
// store's, read once).
func BenchmarkFindCold(b *testing.B) {
	st := benchIndexed(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		(&Engine{Store: st}).Find("周报", 80)
	}
}

// BenchmarkSearchIndex is the index lane's work (search_index_update.go): the first index of the archive (every file
// read) and a run that finds nothing changed (the listing kept, every file stat-ed).
func BenchmarkSearchIndex(b *testing.B) {
	st := benchIndexed(b)
	ctx := context.Background()
	b.Run("first", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			if err := os.RemoveAll(st.searchDir()); err != nil {
				b.Fatal(err)
			}
			fresh, err := NewStore(st.Dir)
			if err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			if stats, err := (&Engine{Store: fresh}).UpdateSearchIndex(ctx); err != nil || stats.Read == 0 {
				b.Fatal(stats, err)
			}
		}
	})
	// The store of the other benchmarks may hold the index removed above: a new one reads what is there now.
	fresh, err := NewStore(st.Dir)
	if err != nil {
		b.Fatal(err)
	}
	e := &Engine{Store: fresh}
	if _, err := e.UpdateSearchIndex(ctx); err != nil {
		b.Fatal(err)
	}
	b.Run("unchanged", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if stats, err := e.UpdateSearchIndex(ctx); err != nil || stats.Read != 0 {
				b.Fatal(stats, err)
			}
		}
	})
}

// BenchmarkSearch is kb_search over the same archive, messages included, on a warm engine: a rare word (the days that
// have it are spread over the year: the index leaves only those to read, until the limit is reached), a common one
// (the limit is reached in the first days) and a word no file has (the index rules every file out).
func BenchmarkSearch(b *testing.B) {
	e := &Engine{Store: benchIndexed(b)}
	if hits := e.Search("周报 draft", true, 40); len(hits) == 0 {
		b.Fatal("the rare word is not found")
	}
	e.Search("nowhere", true, 40)
	for _, q := range []string{"周报 draft", "build", "nowhere"} {
		b.Run(q, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				e.Search(q, true, 40)
			}
		})
	}
}

// BenchmarkMatches is opening a chat found by a search (GET /api/v1/kb/chats/<id>?q=, limit 40) in a chat with a year
// of day files: a rare word (4 of the 365 days have it, so every day is looked at) and a common one (on every line: the
// first days fill the limit). Warm is right after a Find, whose cache holds the files it read; cold is a new engine
// each time.
func BenchmarkMatches(b *testing.B) {
	st := benchIndexed(b)
	const chat = "lark-c000"
	warm := &Engine{Store: st}
	warm.Find("周报", 80)
	for _, q := range []struct{ name, query string }{{"rare", "周报 draft"}, {"common", "review"}} {
		if m := warm.Matches(KindChat, chat, q.query, 40); len(m) == 0 {
			b.Fatalf("%q is not found", q.query)
		}
		b.Run(q.name+"/warm", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				warm.Matches(KindChat, chat, q.query, 40)
			}
		})
		b.Run(q.name+"/cold", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				(&Engine{Store: st}).Matches(KindChat, chat, q.query, 40)
			}
		})
	}
}
