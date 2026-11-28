package sessionlist

import (
	"reflect"
	"testing"
)

func TestOriginOnlyChangeInvalidatesViews(t *testing.T) {
	var i Index
	items := []Item{testItem("a", 1), testItem("b", 2)}
	items[0].PaneSource, items[1].PaneSource = "terminal", "desktop"
	i.Replace("owner", items)
	first := mustQuery(t, &i, Options{PaneSource: "terminal"})
	_, _ = i.Counts(Options{PaneSource: "desktop"})
	// Deliberately keep both payloads, Source and UpdatedAt unchanged. The index
	// must detect the immutable adapter attribute independently of JSON changes.
	items[0].PaneSource, items[1].PaneSource = "desktop", "terminal"
	i.Replace("owner", items)
	delta := mustQuery(t, &i, Options{PaneSource: "terminal", Since: first.Revision})
	if delta.Revision == first.Revision || delta.Total != 1 || len(delta.Sessions) != 1 || !reflect.DeepEqual(delta.Remove, []string{"a"}) {
		t.Fatalf("delta: %+v", delta)
	}
	c, err := i.Counts(Options{PaneSource: "desktop"})
	if err != nil || c.Total != 1 || c.PaneSource["desktop"] != 1 || c.Revision != delta.Revision {
		t.Fatalf("counts: %+v %v", c, err)
	}
	c.PaneSource["desktop"] = 100
	c, _ = i.Counts(Options{PaneSource: "desktop"})
	if c.PaneSource["desktop"] != 1 {
		t.Fatal("counts exposed a mutable cached map")
	}
	all, _ := i.Counts(Options{})
	if !reflect.DeepEqual(all.PaneSource, map[string]int{"terminal": 1, "desktop": 1}) {
		t.Fatalf("unfiltered counters: %v", all.PaneSource)
	}
	i.RemoveSource("owner")
	all, _ = i.Counts(Options{})
	if all.Total != 0 || len(all.PaneSource) != 0 {
		t.Fatalf("removed counters: %+v", all)
	}
}

func TestAgentsTabFilterCountsOnlyCurrentTabMembership(t *testing.T) {
	var i Index
	live, ended := testItem("live", 1), testItem("ended", 2)
	live.PaneSource, ended.PaneSource = "terminal", "terminal"
	live.State, ended.State = "idle", "offline"
	i.Replace("owner", []Item{live, ended})
	filter := Options{AgentsOnly: true, PaneSource: "terminal"}
	page := mustQuery(t, &i, filter)
	counts, err := i.Counts(Options{AgentsOnly: true})
	if err != nil || counts.Total != 2 || counts.PaneSource["terminal"] != 1 || page.Total != 1 || len(page.Sessions) != 1 {
		t.Fatalf("agents counts: %+v (%v), tab page: %+v", counts, err, page)
	}
	if all := mustQuery(t, &i, Options{PaneSource: "terminal"}); all.Total != 2 {
		t.Fatalf("general session query lost the ended session's origin: %+v", all)
	}

	live.State = "offline"
	i.Replace("owner", []Item{live, ended})
	filter.Since = page.Revision
	delta := mustQuery(t, &i, filter)
	counts, err = i.Counts(Options{AgentsOnly: true})
	if err != nil || counts.PaneSource["terminal"] != 0 || delta.Total != 0 || !reflect.DeepEqual(delta.Remove, []string{"live"}) {
		t.Fatalf("agents counts: %+v (%v), tab delta: %+v", counts, err, delta)
	}
}
