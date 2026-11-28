package kb

import (
	"errors"
	"slices"
	"testing"
	"time"
)

// treeFixture: four projects, all at the top level.
func treeFixture(t *testing.T) *Engine {
	t.Helper()
	e := testEngine(t, nil)
	for _, name := range []string{"garden", "seeds", "watering", "docs"} {
		if _, err := e.Apply(KindProject, name, Update{Name: name}); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func setParent(t *testing.T, e *Engine, child, parent string) {
	t.Helper()
	if _, err := e.ApplyUser(KindProject, child, UserUpdate{Parent: &parent}); err != nil {
		t.Fatalf("parent of %s = %q: %v", child, parent, err)
	}
}

func parentOf(t *testing.T, e *Engine, slug string) string {
	t.Helper()
	p, ok := e.Store.Project(slug)
	if !ok {
		t.Fatalf("%s is gone", slug)
	}
	return p.Parent
}

func treeCode(err error) string {
	var te *TreeError
	if errors.As(err, &te) {
		return te.Code
	}
	return ""
}

func TestSetParentLocksAndClears(t *testing.T) {
	e := treeFixture(t)
	setParent(t, e, "seeds", "garden")
	p, _ := e.Store.Project("seeds")
	if p.Parent != "garden" || !slices.Contains(p.LockedFields, "parent") {
		t.Fatalf("parent %q, locks %v", p.Parent, p.LockedFields)
	}
	// By name, deeper than the suggested depth: allowed (no depth limit).
	setParent(t, e, "watering", "seeds")
	setParent(t, e, "docs", "watering")
	tree, _ := e.ProjectTreeOf("docs")
	if tree.Depth != 4 || tree.Parent == nil || tree.Parent.ID != "watering" || len(tree.Ancestors) != 3 {
		t.Fatalf("tree = %+v", tree)
	}
	g, _ := e.ProjectTreeOf("garden")
	if len(g.Children) != 1 || g.Children[0].ID != "seeds" || g.Children[0].Children != 1 {
		t.Fatalf("garden children = %+v", g.Children)
	}
	setParent(t, e, "seeds", "")
	p, _ = e.Store.Project("seeds")
	if p.Parent != "" || !slices.Contains(p.LockedFields, "parent") {
		t.Fatalf("cleared: parent %q, locks %v", p.Parent, p.LockedFields)
	}
}

func TestSetParentRefusesSelfCycleAndMissing(t *testing.T) {
	e := treeFixture(t)
	setParent(t, e, "seeds", "garden")
	setParent(t, e, "watering", "seeds")
	if _, err := e.Apply(KindProject, "gone", Update{Name: "gone", Status: "ignored"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ApplyUser(KindProject, "docs", UserUpdate{Update: Update{MergeInto: "garden"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ child, parent, code string }{
		{"garden", "garden", CodeParentSelf},
		{"garden", "watering", CodeParentCycle},
		{"garden", "seeds", CodeParentCycle},
		{"seeds", "nowhere", CodeParentNotFound},
		{"seeds", "gone", CodeParentNotFound},
	} {
		p := c.parent
		_, err := e.ApplyUser(KindProject, c.child, UserUpdate{Parent: &p})
		if treeCode(err) != c.code {
			t.Errorf("%s under %s: %v, want %s", c.child, c.parent, err, c.code)
		}
	}
	// A merged parent by slug is refused; nothing changed meanwhile.
	docs := "docs"
	if _, err := e.ApplyUser(KindProject, "seeds", UserUpdate{Parent: &docs}); err == nil {
		t.Error("a merged project was accepted as a parent by slug")
	}
	if parentOf(t, e, "garden") != "" || parentOf(t, e, "seeds") != "garden" {
		t.Fatal("a refused change was written")
	}
	g := "garden"
	if _, err := e.ApplyUser(KindProject, "seeds", UserUpdate{Parent: &g, Update: Update{MergeInto: "watering"}}); treeCode(err) != CodeParentWithMerge {
		t.Errorf("with merge_into: %v", err)
	}
	if _, err := e.ApplyUser(KindProject, "seeds", UserUpdate{Parent: &g, Unmerge: true}); treeCode(err) != CodeParentWithMerge {
		t.Errorf("with unmerge: %v", err)
	}
}

// A parent set by the user stays through automatic writes: a pipeline write of a stale copy, and dedupe.
func TestParentSurvivesAutomaticWritesAndDedupe(t *testing.T) {
	e := treeFixture(t)
	stale, _ := e.Store.Project("seeds")
	setParent(t, e, "seeds", "garden")
	stale.OneLine = "rewritten by the model"
	if err := e.Store.PutProject(stale); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(t, e, "seeds"); got != "garden" {
		t.Fatalf("an automatic write changed the parent to %q", got)
	}
	// Cleared by the user, the parent stays cleared too.
	stale, _ = e.Store.Project("seeds")
	setParent(t, e, "seeds", "")
	if err := e.Store.PutProject(stale); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(t, e, "seeds"); got != "" {
		t.Fatalf("an automatic write brought back the parent %q", got)
	}
	setParent(t, e, "seeds", "garden")
	setParent(t, e, "watering", "garden")
	if err := e.mergeProjects("garden", "seeds", "auto", false); err == nil {
		t.Error("automatic merge of a parent and its child")
	}
	if err := e.mergeProjects("watering", "seeds", "auto", false); err == nil {
		t.Error("automatic merge of two siblings")
	}
	all := e.Store.Projects()
	for _, c := range dedupeCandidates(all) {
		for _, p := range []*Project{c.a, c.b} {
			if p.Slug == "garden" || p.Slug == "seeds" || p.Slug == "watering" {
				t.Errorf("dedupe asks about %s", p.Slug)
			}
		}
	}
}

func TestMilestonesEditAndLock(t *testing.T) {
	e := treeFixture(t)
	title, target := "Beta out", "2026-12-15"
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Milestone: &MilestoneEdit{Title: &title, Target: &target}}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project("garden")
	if len(p.Milestones) != 1 || p.Milestones[0].State != MilestoneOpen || p.Milestones[0].ID == "" || !slices.Contains(p.LockedFields, "milestones") {
		t.Fatalf("milestones %+v, locks %v", p.Milestones, p.LockedFields)
	}
	id := p.Milestones[0].ID
	undo := e.TreeUndo("garden", UserUpdate{Milestone: &MilestoneEdit{ID: id, Remove: true}})
	state := MilestoneAccepted
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Milestone: &MilestoneEdit{ID: id, State: &state}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Store.Project("garden"); p.Milestones[0].State != MilestoneAccepted {
		t.Fatalf("state %q", p.Milestones[0].State)
	}
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Milestone: &MilestoneEdit{ID: id, Remove: true}}); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Store.Project("garden"); len(p.Milestones) != 0 {
		t.Fatalf("not removed: %+v", p.Milestones)
	}
	// The undo taken before the edits puts back the list as it was then.
	if undo == nil || undo.Milestones == nil {
		t.Fatal("no undo")
	}
	if _, err := e.ApplyUser(KindProject, "garden", *undo); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Store.Project("garden"); len(p.Milestones) != 1 || p.Milestones[0].ID != id || p.Milestones[0].State != MilestoneOpen {
		t.Fatalf("undo gave %+v", p.Milestones)
	}
	stale := p
	stale.Milestones = nil
	if err := e.Store.PutProject(stale); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Store.Project("garden"); len(p.Milestones) != 1 {
		t.Fatal("an automatic write dropped the milestones")
	}
	bad := "next week"
	empty := ""
	for name, u := range map[string]UserUpdate{
		"bad date":   {Milestone: &MilestoneEdit{Title: &title, Target: &bad}},
		"no title":   {Milestone: &MilestoneEdit{Title: &empty}},
		"bad state":  {Milestone: &MilestoneEdit{Title: &title, State: &bad}},
		"unknown id": {Milestone: &MilestoneEdit{ID: "nope", Remove: true}},
	} {
		if _, err := e.ApplyUser(KindProject, "garden", u); treeCode(err) == "" {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestMergeMovesChildrenAndUnmergeBringsThemBack(t *testing.T) {
	e := treeFixture(t)
	setParent(t, e, "seeds", "garden")
	setParent(t, e, "watering", "garden")
	// docs (top level) takes garden in: garden's children go under docs.
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Update: Update{MergeInto: "docs"}}); err != nil {
		t.Fatal(err)
	}
	if parentOf(t, e, "seeds") != "docs" || parentOf(t, e, "watering") != "docs" {
		t.Fatalf("children went to %q, %q", parentOf(t, e, "seeds"), parentOf(t, e, "watering"))
	}
	// The user moves one elsewhere meanwhile: the unmerge leaves that one.
	setParent(t, e, "watering", "")
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Unmerge: true}); err != nil {
		t.Fatal(err)
	}
	if parentOf(t, e, "seeds") != "garden" || parentOf(t, e, "watering") != "" {
		t.Fatalf("after unmerge: %q, %q", parentOf(t, e, "seeds"), parentOf(t, e, "watering"))
	}
	// Merging a parent into its own child: the child takes the parent's place.
	setParent(t, e, "garden", "docs")
	if _, err := e.ApplyUser(KindProject, "garden", UserUpdate{Update: Update{MergeInto: "seeds"}}); err != nil {
		t.Fatal(err)
	}
	if got := parentOf(t, e, "seeds"); got != "docs" {
		t.Fatalf("the kept child is under %q", got)
	}
	if parents := e.ProjectParents(); parents["seeds"] != "docs" {
		t.Fatalf("parents = %v", parents)
	}
}

func TestDeleteParentLiftsChildrenAndRestoreBringsThemBack(t *testing.T) {
	e := treeFixture(t)
	setParent(t, e, "garden", "docs")
	setParent(t, e, "seeds", "garden")
	setParent(t, e, "watering", "garden")
	entry, err := e.DeleteProject("garden", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Children) != 2 || parentOf(t, e, "seeds") != "docs" || parentOf(t, e, "watering") != "docs" {
		t.Fatalf("children %+v; seeds under %q", entry.Children, parentOf(t, e, "seeds"))
	}
	setParent(t, e, "watering", "")
	if _, err := e.RestoreProject(entry.ID); err != nil {
		t.Fatal(err)
	}
	if parentOf(t, e, "seeds") != "garden" || parentOf(t, e, "watering") != "" {
		t.Fatalf("after restore: %q, %q", parentOf(t, e, "seeds"), parentOf(t, e, "watering"))
	}
}

func TestProjectParentsReadsThroughMergedAndIgnored(t *testing.T) {
	e := treeFixture(t)
	all := []*Project{
		{Slug: "a", Status: "active", Parent: "m"},
		{Slug: "m", Status: "merged", MergedInto: "k"},
		{Slug: "k", Status: "active"},
		{Slug: "b", Status: "active", Parent: "i"},
		{Slug: "i", Status: "ignored"},
		{Slug: "c", Status: "active", Parent: "missing"},
		{Slug: "x", Status: "active", Parent: "y"},
		{Slug: "y", Status: "active", Parent: "x"},
	}
	got := e.projectParents(all)
	if got["a"] != "k" || got["b"] != "" || got["c"] != "" {
		t.Fatalf("parents = %v", got)
	}
	if _, cyc := got["x"]; cyc {
		t.Fatalf("a cycle stays: %v", got)
	}
	if got["y"] != "x" {
		t.Fatalf("the rest of a cycle: %v", got)
	}
}

func TestTreeFieldsOnlyForProjects(t *testing.T) {
	e := treeFixture(t)
	_ = e.Store.PutChat(&Chat{ID: "chat"})
	g := "garden"
	ignored := UserUnsupported(KindChat, UserUpdate{Parent: &g})
	if len(ignored) != 1 || ignored[0].Field != "parent" {
		t.Fatalf("ignored = %v", ignored)
	}
	if _, err := e.ApplyUser(KindChat, "chat", UserUpdate{Parent: &g}); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	re := &Engine{Store: ro, Logf: func(string, ...any) {}}
	if _, err := re.ApplyUser(KindProject, "seeds", UserUpdate{Parent: &g}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only: %v", err)
	}
}

// A parent with no locks of its own still is the user's organizing: dedupe neither asks about it nor merges it.
func TestDedupeSkipsAParentWithoutLocks(t *testing.T) {
	e := testEngine(t, nil)
	people := []string{"lark-u1", "lark-u2", "lark-u3"}
	for _, slug := range []string{"garden", "garden-app", "seeds"} {
		if err := e.Store.PutProject(&Project{Slug: slug, Name: slug, Status: "active", People: people}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(dedupeCandidates(e.Store.Projects()))
	setParent(t, e, "seeds", "garden")
	for _, c := range dedupeCandidates(e.Store.Projects()) {
		if c.a.Slug == "garden" || c.b.Slug == "garden" || c.a.Slug == "seeds" || c.b.Slug == "seeds" {
			t.Fatalf("dedupe asks about %s and %s", c.a.Slug, c.b.Slug)
		}
	}
	if before < 2 {
		t.Fatalf("the fixture gives %d candidates before the tree", before)
	}
	if err := e.MergeProjects("garden-app", "garden", "auto"); err == nil {
		t.Fatal("dedupe merged a parent away")
	}
	if p, _ := e.Store.Project("garden"); !p.Live() {
		t.Fatal("the parent was merged")
	}
}
