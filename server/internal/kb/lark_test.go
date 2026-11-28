package kb

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestPutCollectedItemWritesAndIndexes(t *testing.T) {
	e := testEngine(t, nil)
	type row struct{ kind, id, name, one string }
	var rows []row
	e.Index = func(kind, id, name string, _ Tagging, one string, _ time.Time) {
		rows = append(rows, row{kind, id, name, one})
	}
	at := time.Date(2026, 11, 17, 12, 1, 31, 0, time.UTC)
	it := &Item{Kind: KindMeeting, Source: "larkcli", ID: "lark-minutes-t1", Title: "周会", URL: "https://x.larkoffice.com/minutes/t1", At: at, Where: "lark-cli", Note: "摘要：一句话"}
	changed, err := e.PutCollectedItem(it)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, ok := e.Store.Item(KindMeeting, "lark-minutes-t1")
	if !ok || got.Title != "周会" || got.URL == "" || !got.At.Equal(at) || got.Source != "larkcli" || got.Where != "lark-cli" {
		t.Fatalf("item %+v", got)
	}
	if got.Seen.IsZero() {
		t.Fatal("a new item is marked seen")
	}
	if !slices.Contains(got.LockedFields, "note") {
		t.Fatalf("note not locked: %v", got.LockedFields)
	}
	if len(rows) != 1 || rows[0].kind != KindMeeting || rows[0].id != "lark-minutes-t1" || rows[0].one != "摘要：一句话" {
		t.Fatalf("index rows %+v", rows)
	}
	// kb_search reads the files, so the item is found at once (the search index follows in its lane).
	if hits := e.Search("周会", false, 10); len(hits) == 0 {
		t.Fatal("search does not find the collected minute")
	}
	// Nothing changed: no write, no index row.
	changed, err = e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "lark-minutes-t1", Title: "周会", URL: it.URL, At: at})
	if err != nil || changed {
		t.Fatalf("unchanged: changed=%v err=%v", changed, err)
	}
	if len(rows) != 1 {
		t.Fatalf("unchanged wrote an index row: %+v", rows)
	}
}

// A re-import updates what the source owns and keeps the note, tags and projects already there.
func TestPutCollectedItemKeepsWhatTheSourceDoesNotSend(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if _, err := e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "m1", Title: "旧标题", At: at, Note: "摘要：旧"}); err != nil {
		t.Fatal(err)
	}
	old, _ := e.Store.Item(KindMeeting, "m1")
	old.Tags = []string{"项目甲"}
	old.Projects = []string{"ss-a"}
	old.Tagging.TaggedAt = time.Now()
	if err := e.Store.PutItem(old); err != nil {
		t.Fatal(err)
	}
	changed, err := e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "m1", Title: "新标题", At: at, Note: "摘要：新"})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := e.Store.Item(KindMeeting, "m1")
	if got.Title != "新标题" {
		t.Fatalf("title %q", got.Title)
	}
	if got.Note != "摘要：旧" {
		t.Fatalf("note overwritten: %q", got.Note)
	}
	if !slices.Equal(got.Tags, []string{"项目甲"}) || !slices.Equal(got.Projects, []string{"ss-a"}) {
		t.Fatalf("tags/projects lost: %v %v", got.Tags, got.Projects)
	}
	if !got.TaggedAt.IsZero() {
		t.Fatal("a changed item is tagged again")
	}
}

// A note that was never there is filled in later (the detail was unreadable the first time).
func TestPutCollectedItemFillsAMissingNote(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if _, err := e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "m1", Title: "周会", At: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "m1", Title: "周会", At: at, Note: "摘要：补上"}); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.Item(KindMeeting, "m1")
	if got.Note != "摘要：补上" {
		t.Fatalf("note %q", got.Note)
	}
}

// An id another source filed stays that source's.
func TestPutCollectedItemLeavesAnotherSourcesItem(t *testing.T) {
	e := testEngine(t, nil)
	if err := e.Store.PutItem(&Item{Kind: KindDoc, Source: "larkx", ID: "d1", Title: "别人的"}); err != nil {
		t.Fatal(err)
	}
	changed, err := e.PutCollectedItem(&Item{Kind: KindDoc, Source: "larkcli", ID: "d1", Title: "我的"})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	got, _ := e.Store.Item(KindDoc, "d1")
	if got.Title != "别人的" || got.Source != "larkx" {
		t.Fatalf("item %+v", got)
	}
}

// A discovered document the selection leaves out is not written.
func TestPutCollectedItemFollowsTheDocumentSelection(t *testing.T) {
	s := sel(func(s *Selection) { setDocRule(s, sigViewed, false, nil) })
	e := docSelEngine(t, s)
	changed, err := e.PutCollectedItem(&Item{Kind: KindDoc, Source: "larkcli", ID: "d1", Title: "文档", URL: "https://x.larkoffice.com/docx/d1", Signals: []string{sigViewed}})
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if _, ok := e.Store.Item(KindDoc, "d1"); ok {
		t.Fatal("a document the selection leaves out was written")
	}
}

// Item tagging rewrites an unlocked note; a collected note is kept as written.
func TestTagItemsKeepsACollectedNote(t *testing.T) {
	e := testEngine(t, &scriptModel{replies: []string{`{"items":[{"id":"meetings/m1","importance":"P2","relevance":50,"tags":["周会"],"note":"模型的一句话"}]}`}})
	at := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	if _, err := e.PutCollectedItem(&Item{Kind: KindMeeting, Source: "larkcli", ID: "m1", Title: "周会", At: at, Note: "摘要：来源的一句话"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.TagItems(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.Item(KindMeeting, "m1")
	if got.Note != "摘要：来源的一句话" {
		t.Fatalf("note overwritten by tagging: %q", got.Note)
	}
	if got.Importance != "P2" || !slices.Equal(got.Tags, []string{"周会"}) || got.TaggedAt.IsZero() {
		t.Fatalf("tagging did not apply: %+v", got.Tagging)
	}
}

func TestFillDocTitles(t *testing.T) {
	e := testEngine(t, nil)
	for _, it := range []*Item{
		{Kind: KindDoc, Source: "larkx", ID: "empty", URL: "https://x.larkoffice.com/docx/empty"},
		{Kind: KindDoc, Source: "larkx", ID: "titled", Title: "已有", URL: "https://x.larkoffice.com/docx/titled"},
		{Kind: KindDoc, Source: "other", ID: "foreign", URL: "https://example.com/d"},
		{Kind: KindDoc, Source: "larkx", ID: "kept", URL: "https://x.larkoffice.com/docx/kept"},
	} {
		if err := e.Store.PutItem(it); err != nil {
			t.Fatal(err)
		}
	}
	asked := map[string]bool{}
	n := e.FillDocTitles(context.Background(), 10, func(_ context.Context, url string) (string, error) {
		asked[url] = true
		return "标题 " + url, nil
	}, func(it *Item) bool { return it.Source != "other" })
	if n != 2 {
		t.Fatalf("filled %d", n)
	}
	if asked["https://example.com/d"] {
		t.Fatal("a foreign link was asked about")
	}
	if got, _ := e.Store.Item(KindDoc, "empty"); got.Title == "" {
		t.Fatal("empty title not filled")
	}
	if got, _ := e.Store.Item(KindDoc, "titled"); got.Title != "已有" {
		t.Fatalf("titled document changed: %q", got.Title)
	}
}
