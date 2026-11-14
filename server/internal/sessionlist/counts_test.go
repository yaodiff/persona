package sessionlist

import (
	"fmt"
	"net/url"
	"testing"
)

func TestDeltaCountsDoNotBuildFullOrderedView(t *testing.T) {
	var index Index
	items := make([]Item, 8000)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("row-%04d", n), n)
	}
	index.Replace("demo", items)
	baseline := index.Revision()
	items[7999].Full = testPayload{ID: items[7999].ID, Title: "Changed without a timestamp change"}
	items[7999].State = "needs_you"
	index.Replace("demo", items)
	page := mustQuery(t, &index, Options{Since: baseline})
	if len(page.Sessions) != 1 || page.Total != 8000 {
		t.Fatalf("delta: %+v", page)
	}
	view := index.cache[cacheKey{}].result
	if view.ordered || len(view.events) != 0 {
		t.Fatal("delta unnecessarily built the whole ordered list")
	}
	counts, err := index.Counts(Options{})
	if err != nil || counts.State["working"] != 7999 || counts.State["needs_you"] != 1 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	// A later full page upgrades the counts-only cache without losing members.
	full := mustQuery(t, &index, Options{Limit: 50})
	if len(full.Sessions) != 50 || full.Total != 8000 || !index.cache[cacheKey{}].result.ordered {
		t.Fatalf("full after counts: %+v", full)
	}
	index.Replace("demo", items[:7999])
	counts, err = index.Counts(Options{})
	if err != nil || counts.Total != 7999 || counts.State["working"] != 7999 {
		t.Fatalf("removed counts: %+v %v", counts, err)
	}
	if _, exists := counts.State["needs_you"]; exists {
		t.Fatal("zero-count group retained")
	}
}

func TestFrozenCountsDetachedFromCurrentCounters(t *testing.T) {
	var index Index
	index.Replace("demo", []Item{testItem("a", 1), testItem("b", 2)})
	page := mustQuery(t, &index, Options{Limit: 1})
	index.RemoveSource("demo")
	frozen, err := index.Counts(Options{Cursor: page.NextCursor})
	if err != nil || frozen.Total != 2 || frozen.State["working"] != 2 {
		t.Fatalf("snapshot mutated: %+v %v", frozen, err)
	}
	current, _ := index.Counts(Options{})
	if current.Total != 0 || len(current.State) != 0 {
		t.Fatalf("current: %+v", current)
	}
}

// Rows waiting in the background (#6045) stay in the working count, for older apps, and are counted apart too; the
// count follows a row that starts or stops waiting, and filtered counts have it as well.
func TestBackgroundWaitingCount(t *testing.T) {
	var index Index
	items := make([]Item, 4)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("row-%d", n), n)
	}
	items[1].BackgroundWaiting, items[2].BackgroundWaiting = true, true
	index.Replace("demo", items)
	counts, err := index.Counts(Options{})
	if err != nil || counts.State["working"] != 4 || counts.BackgroundWaiting != 2 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	items[2].BackgroundWaiting = false
	items[2].Full = testPayload{ID: items[2].ID, Title: "Its build started"}
	index.Replace("demo", items)
	if counts, _ = index.Counts(Options{}); counts.BackgroundWaiting != 1 {
		t.Fatalf("after one started working: %+v", counts)
	}
	if counts, _ = index.Counts(Options{State: "working"}); counts.BackgroundWaiting != 1 || counts.Total != 4 {
		t.Fatalf("filtered: %+v", counts)
	}
}

// Sessions by to-do (#6662): todo=<id> lists the rows on that to-do, todo=none the rows on none; the counts have each
// to-do's rows and, under "", the rows on none. A row moved to another to-do moves with it, in a delta too, and the
// filter binds a cursor as the others do.
func TestTodoFilterAndCounts(t *testing.T) {
	var index Index
	items := make([]Item, 5)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("row-%d", n), n)
	}
	setTodo := func(row int, todo string) {
		items[row].Fold = &Fold{State: items[row].State, BackgroundWaiting: items[row].BackgroundWaiting, Todo: todo}
	}
	setTodo(0, "todo_a")
	setTodo(1, "todo_a")
	setTodo(2, "todo_b")
	index.Replace("demo", items)
	counts, err := index.Counts(Options{})
	if err != nil || counts.Todo["todo_a"] != 2 || counts.Todo["todo_b"] != 1 || counts.Todo[""] != 2 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	ids := func(p Page) []string {
		var out []string
		for _, s := range p.Sessions {
			out = append(out, s.(testPayload).ID)
		}
		return out
	}
	if got := ids(mustQuery(t, &index, Options{Todo: "todo_a"})); fmt.Sprint(got) != "[row-1 row-0]" {
		t.Fatalf("todo_a: %v", got)
	}
	if got := ids(mustQuery(t, &index, Options{Todo: TodoNone})); fmt.Sprint(got) != "[row-4 row-3]" {
		t.Fatalf("none: %v", got)
	}
	if counts, _ := index.Counts(Options{Todo: "todo_b"}); counts.Total != 1 || counts.Todo["todo_b"] != 1 {
		t.Fatalf("filtered counts: %+v", counts)
	}
	baseline := index.Revision()
	setTodo(3, "todo_b")
	items[3].Full = testPayload{ID: items[3].ID, Title: "Linked to b"}
	index.Replace("demo", items)
	if got := ids(mustQuery(t, &index, Options{Todo: "todo_b", Since: baseline})); fmt.Sprint(got) != "[row-3]" {
		t.Fatalf("delta into todo_b: %v", got)
	}
	if page := mustQuery(t, &index, Options{Todo: TodoNone, Since: baseline}); len(page.Remove) != 1 || page.Remove[0] != "row-3" || page.Total != 1 {
		t.Fatalf("delta out of none: %+v", page)
	}
	first := mustQuery(t, &index, Options{Todo: "todo_b", Limit: 1})
	if _, err := index.Query(Options{Todo: "todo_a", Cursor: first.NextCursor}); err == nil {
		t.Fatal("a cursor taken with another to-do")
	}
}

// The sidebar's quick filters on the paged list (#6662): waiting=1 the rows that only wait in the background, waiting=0
// the others, by their folded state with Roots; exclude_pane_source leaves one origin out, of the rows and the counts.
func TestWaitingAndExcludedOrigin(t *testing.T) {
	var index Index
	items := make([]Item, 4)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("row-%d", n), n)
	}
	items[1].BackgroundWaiting = true
	items[2].PaneSource, items[3].PaneSource = "feishu", "terminal"
	index.Replace("demo", items)
	total := func(o Options) int {
		t.Helper()
		page := mustQuery(t, &index, o)
		return page.Total
	}
	if total(Options{State: "working", Waiting: "1"}) != 1 || total(Options{State: "working", Waiting: "0"}) != 3 {
		t.Fatal("waiting split the working rows wrongly")
	}
	counts, err := index.Counts(Options{ExcludePaneSource: "feishu"})
	if err != nil || counts.Total != 3 || counts.PaneSource["feishu"] != 0 || counts.PaneSource["terminal"] != 1 {
		t.Fatalf("excluded origin: %+v %v", counts, err)
	}
	// a parent folding a working child is not waiting by its folded state
	items[1].Fold = &Fold{State: "working", BackgroundWaiting: false}
	items[1].Full = testPayload{ID: items[1].ID, Title: "Its child works"}
	index.Replace("demo", items)
	if total(Options{Roots: true, Waiting: "1"}) != 0 || total(Options{Waiting: "1"}) != 1 {
		t.Fatal("waiting did not follow the folded state with roots")
	}
	for _, v := range []url.Values{{"waiting": {"2"}}, {"exclude_pane_source": {""}}, {"exclude_pane_source": {"elsewhere"}}} {
		if _, err := Parse(v); err == nil {
			t.Fatalf("accepted %v", v)
		}
	}
}
