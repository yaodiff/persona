package kblark

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

type fakeReader struct {
	minutes  []Minute
	detail   map[string]Minute
	detailOK bool // false: MinuteDetail fails, as an unreadable one does
	docs     map[string]string
	search   map[string][]Doc
	searchOK bool
}

func (f *fakeReader) SearchMinutes(context.Context, time.Time, time.Time) ([]Minute, error) {
	return f.minutes, nil
}

func (f *fakeReader) MinuteDetail(_ context.Context, token string) (Minute, error) {
	if !f.detailOK {
		return Minute{}, errors.New("lark-cli: not logged in")
	}
	return f.detail[token], nil
}

func (f *fakeReader) InspectDoc(_ context.Context, url string) (string, error) {
	return f.docs[url], nil
}

func (f *fakeReader) SearchDocs(_ context.Context, query string) ([]Doc, error) {
	if !f.searchOK {
		return nil, errors.New("search failed")
	}
	return f.search[query], nil
}

func newEngine(t *testing.T) *kb.Engine {
	t.Helper()
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &kb.Engine{Store: st, Logf: func(string, ...any) {}}
}

func meeting(t *testing.T, e *kb.Engine, id string) *kb.Item {
	t.Helper()
	it, ok := e.Store.Item(kb.KindMeeting, id)
	if !ok {
		t.Fatalf("%s not imported", id)
	}
	return it
}

func TestRunImportsMinutesOnce(t *testing.T) {
	e := newEngine(t)
	at := time.Date(2026, 11, 17, 12, 1, 31, 0, time.UTC)
	r := &fakeReader{
		minutes:  []Minute{{Token: "t1", Title: "周会", URL: "https://x.larkoffice.com/minutes/t1", Start: at}},
		detail:   map[string]Minute{"t1": {Summary: "一句摘要", Todos: []string{"做甲", "做乙"}, Chapters: []string{"开场"}}},
		detailOK: true,
	}
	res, err := Run(context.Background(), e, r, Config{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Minutes != 1 || res.Changed != 1 {
		t.Fatalf("result %+v", res)
	}
	it := meeting(t, e, "lark-minutes-t1")
	if it.Title != "周会" || it.URL != "https://x.larkoffice.com/minutes/t1" || !it.At.Equal(at) ||
		it.Source != Source || it.Where != Where {
		t.Fatalf("item %+v", it)
	}
	for _, want := range []string{"摘要：一句摘要", "待办：做甲；做乙", "章节：开场"} {
		if !strings.Contains(it.Note, want) {
			t.Fatalf("note %q lacks %q", it.Note, want)
		}
	}
	list := e.List(kb.KindMeeting, "")
	if len(list) != 1 || list[0].Name != "周会" || list[0].OneLine != it.Note || list[0].Extra != it.URL {
		t.Fatalf("list %+v", list)
	}

	// A second run reads the same minute and writes nothing.
	res, err = Run(context.Background(), e, r, Config{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed != 0 {
		t.Fatalf("second run changed %d", res.Changed)
	}
	if n := len(e.Store.Items(kb.KindMeeting)); n != 1 {
		t.Fatalf("%d minutes after two runs", n)
	}
}

func TestRunKeepsAUsersNote(t *testing.T) {
	e := newEngine(t)
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	r := &fakeReader{
		minutes:  []Minute{{Token: "t1", Title: "旧标题", URL: "https://x.larkoffice.com/minutes/t1", Start: at}},
		detail:   map[string]Minute{"t1": {Summary: "来源摘要"}},
		detailOK: true,
	}
	if _, err := Run(context.Background(), e, r, Config{}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Apply(kb.KindMeeting, "lark-minutes-t1", kb.Update{Note: "用户改过的笔记"}); err != nil {
		t.Fatal(err)
	}

	r.minutes[0].Title = "新标题"
	if _, err := Run(context.Background(), e, r, Config{}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	got := meeting(t, e, "lark-minutes-t1")
	if got.Title != "新标题" {
		t.Fatalf("title %q", got.Title)
	}
	if got.Note != "用户改过的笔记" {
		t.Fatalf("note overwritten: %q", got.Note)
	}
}

func TestRunRetriesADetailThatFailed(t *testing.T) {
	e := newEngine(t)
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	r := &fakeReader{
		minutes: []Minute{{Token: "t1", Title: "周会", URL: "https://x.larkoffice.com/minutes/t1", Start: at}},
		detail:  map[string]Minute{"t1": {Summary: "补上的摘要"}},
	}
	if _, err := Run(context.Background(), e, r, Config{}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if it := meeting(t, e, "lark-minutes-t1"); it.Note != "" {
		t.Fatalf("note %q before the detail could be read", it.Note)
	}
	r.detailOK = true
	if _, err := Run(context.Background(), e, r, Config{}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if it := meeting(t, e, "lark-minutes-t1"); !strings.Contains(it.Note, "补上的摘要") {
		t.Fatalf("note %q", it.Note)
	}
}

func TestRunFillsDocumentTitles(t *testing.T) {
	e := newEngine(t)
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindDoc, Source: "larkx", ID: "d1", URL: "https://x.larkoffice.com/docx/d1"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindDoc, Source: "other", ID: "d2", URL: "https://example.com/d2"}); err != nil {
		t.Fatal(err)
	}
	r := &fakeReader{docs: map[string]string{"https://x.larkoffice.com/docx/d1": "文档标题"}}
	res, err := Run(context.Background(), e, r, Config{}, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Filled != 1 {
		t.Fatalf("filled %d", res.Filled)
	}
	if it, _ := e.Store.Item(kb.KindDoc, "d1"); it.Title != "文档标题" {
		t.Fatalf("title %q", it.Title)
	}
	if it, _ := e.Store.Item(kb.KindDoc, "d2"); it.Title != "" {
		t.Fatalf("a foreign link was titled: %q", it.Title)
	}
}

func TestRunDiscoversDocuments(t *testing.T) {
	e := newEngine(t)
	r := &fakeReader{
		searchOK: true,
		search: map[string][]Doc{"表情": {
			{Token: "d9", Title: "表情分析", URL: "https://x.larkoffice.com/docx/d9", At: time.Date(2026, 11, 20, 9, 52, 58, 0, time.UTC)},
		}},
	}
	cfg := Config{DocSearch: []string{"表情"}}
	res, err := Run(context.Background(), e, r, cfg, time.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Docs != 1 {
		t.Fatalf("docs %d", res.Docs)
	}
	it, ok := e.Store.Item(kb.KindDoc, "d9")
	if !ok || it.Title != "表情分析" || it.Source != Source {
		t.Fatalf("doc %+v", it)
	}
	if _, err := Run(context.Background(), e, r, cfg, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Store.Items(kb.KindDoc)); n != 1 {
		t.Fatalf("%d documents after two runs", n)
	}
}

func TestRunReportsAFailedSearch(t *testing.T) {
	e := newEngine(t)
	r := &searchFailReader{}
	if _, err := Run(context.Background(), e, r, Config{}, time.Now(), nil); err == nil {
		t.Fatal("no error")
	}
	if n := len(e.Store.Items(kb.KindMeeting)); n != 0 {
		t.Fatalf("%d minutes written after a failed search", n)
	}
}

type searchFailReader struct{}

func (searchFailReader) SearchMinutes(context.Context, time.Time, time.Time) ([]Minute, error) {
	return nil, errors.New("lark-cli is not signed in")
}
func (searchFailReader) MinuteDetail(context.Context, string) (Minute, error) { return Minute{}, nil }
func (searchFailReader) InspectDoc(context.Context, string) (string, error)   { return "", nil }
func (searchFailReader) SearchDocs(context.Context, string) ([]Doc, error)    { return nil, nil }

// A minute's note words its labels in the owner's language; zh-Hans (and no language: i18n.Default) is the text the
// import wrote before the labels were localized, byte for byte. What Lark sent is kept as it came.
func TestMinuteNoteLabelsInTheOwnersLanguage(t *testing.T) {
	m := Minute{Summary: "一句\n摘要", Todos: []string{"做甲", " ", "做乙", "做丙", "做丁"}, Chapters: []string{"开场", "讨论"}}
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{
		{i18n.ZhHans, "摘要：一句 摘要 待办：做甲；做乙；做丙 章节：开场；讨论"},
		{"", "摘要：一句 摘要 待办：做甲；做乙；做丙 章节：开场；讨论"},
		{i18n.En, "Summary: 一句 摘要 To-dos: 做甲; 做乙; 做丙 Chapters: 开场; 讨论"},
		{i18n.Ja, "要約：一句 摘要 ToDo：做甲；做乙；做丙 チャプター：开场；讨论"},
	} {
		if got := minuteNote(m, tc.lang); got != tc.want {
			t.Errorf("%q: note %q, want %q", tc.lang, got, tc.want)
		}
	}
}

// Run writes the note in Config.Lang.
func TestRunWritesTheNoteInConfigLang(t *testing.T) {
	e := newEngine(t)
	r := &fakeReader{
		minutes:  []Minute{{Token: "t1", Title: "Weekly", URL: "https://example.feishu.cn/minutes/t1"}},
		detail:   map[string]Minute{"t1": {Summary: "Plan", Todos: []string{"Ship"}}},
		detailOK: true,
	}
	if _, err := Run(context.Background(), e, r, Config{Lang: i18n.En}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if got := meeting(t, e, "lark-minutes-t1").Note; got != "Summary: Plan To-dos: Ship" {
		t.Fatalf("note %q", got)
	}
}
