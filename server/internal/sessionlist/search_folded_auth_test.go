package sessionlist

import (
	"reflect"
	"slices"
	"testing"
)

// Folded side sessions (Fold.SearchAs) must pass the same authorization and isolation
// boundaries as their target parent row BEFORE SearchContext.Document is called.
func TestRuntimeSearchExcludesUnauthorizedFoldedSideSessions(t *testing.T) {
	ctx := searchContext()
	var decoded []string
	ctx.Document = func(item Item) (SearchDocument, error) {
		decoded = append(decoded, item.ID)
		return item.Full.(SearchDocument), nil
	}

	makeSide := func(id, machine, text, sessionID, paneSource string) Item {
		it := searchItem(id, machine, "Side "+id, ctx.Now)
		doc := it.Full.(SearchDocument)
		doc.AgentText = text
		it.Full = doc
		it.PaneSource = paneSource
		it.Fold = &Fold{
			Root:          "parent-session",
			RootMachine:   "allowed",
			SearchAs:      "parent",
			SearchSession: sessionID,
		}
		return it
	}

	parent := searchItem("parent", "allowed", "Parent row", ctx.Now)
	parent.PaneSource = "terminal"

	foreignMachineSide := makeSide("foreign-machine-side", "forbidden", "secret needle from forbidden machine", "side-forbidden-machine", "terminal")
	foreignManagerSide := makeSide("foreign-manager-side", "allowed", "secret needle from other manager", "side-other-manager", "terminal")
	foreignManagerSide.Fold.RootMachine = ""
	excludedOriginSide := makeSide("excluded-origin-side", "allowed", "secret needle from feishu side", "side-feishu", "feishu")
	excludedOriginSide.Fold.RootMachine = ""
	allowedSide := makeSide("allowed-side", "allowed", "authorized needle in allowed side", "side-ok", "terminal")
	allowedSide.Fold.RootMachine = ""

	var index Index
	index.ReplaceOwned("allowed-a", "mgr-a", []Item{parent, excludedOriginSide, allowedSide})
	index.ReplaceOwned("forbidden-a", "mgr-a", []Item{foreignMachineSide})
	index.ReplaceOwned("allowed-b", "mgr-b", []Item{foreignManagerSide})

	baseOpts := Options{
		SearchContext:     ctx,
		AllowedMachines:   []string{"allowed"},
		Manager:           "mgr-a",
		ExcludePaneSource: "feishu",
		Roots:             true,
	}

	secretOpts := baseOpts
	secretOpts.Search = "secret needle"
	page, err := index.Query(secretOpts)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Sessions) != 0 || len(page.SearchHits) != 0 {
		t.Fatalf("unauthorized folded side session leaked into search results: %+v", page)
	}
	for _, id := range []string{"foreign-machine-side", "foreign-manager-side", "excluded-origin-side"} {
		if slices.Contains(decoded, id) {
			t.Fatalf("excluded folded side session %q was decoded before authorization (decoded=%v)", id, decoded)
		}
	}

	// Authorized side session on the same allowed machine/manager/origin still matches.
	okOpts := baseOpts
	okOpts.Search = "authorized needle"
	page, err = index.Query(okOpts)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.SearchHits["parent"].HitSessionID != "side-ok" {
		t.Fatalf("authorized folded side session missing: %+v", page)
	}
}

// Options.AllowedMachines is an adapter-owned authorization boundary and must be enforced
// across non-search Query, Counts, CountMatchingState, and PlaceCounts as well as Search,
// without sharing cached unfiltered views across different machine allowlists.
func TestAllowedMachinesEnforcedAcrossNonSearchQueriesAndCache(t *testing.T) {
	var index Index
	now := searchContext().Now
	m1 := searchItem("m1-a", "m1", "Alpha", now)
	m1.State = "needs_you"
	m2 := searchItem("m2-b", "m2", "Beta", now)
	m2.State = "needs_you"
	index.Replace("m1", []Item{m1})
	index.Replace("m2", []Item{m2})

	// Prime the cache with an unrestricted query first.
	all, err := index.Query(Options{})
	if err != nil || all.Total != 2 {
		t.Fatalf("unrestricted Query = %+v, %v", all, err)
	}

	// Restricted to m1 only.
	restricted, err := index.Query(Options{AllowedMachines: []string{"m1"}, Summary: true})
	if err != nil {
		t.Fatal(err)
	}
	if restricted.Total != 1 || len(restricted.Sessions) != 1 || !reflect.DeepEqual(restricted.Sessions[0], map[string]string{"id": "m1-a"}) {
		t.Fatalf("AllowedMachines=[m1] Query leaked unauthorized machine: %+v", restricted)
	}

	counts, err := index.Counts(Options{AllowedMachines: []string{"m1"}})
	if err != nil || counts.Total != 1 || counts.Machine["m2"] != 0 {
		t.Fatalf("AllowedMachines=[m1] Counts leaked unauthorized machine: %+v %v", counts, err)
	}

	n, err := index.CountMatchingState(Options{AllowedMachines: []string{"m1"}}, "needs_you", []string{"m1-a", "m2-b"})
	if err != nil || n != 1 {
		t.Fatalf("AllowedMachines=[m1] CountMatchingState = %d, %v; want 1", n, err)
	}

	places, err := index.PlaceCounts(Options{AllowedMachines: []string{"m1"}})
	if err != nil || places.Total != 1 {
		t.Fatalf("AllowedMachines=[m1] PlaceCounts = %+v, %v; want Total=1", places, err)
	}

	// Empty non-nil allowlist denies every machine.
	emptyPage, err := index.Query(Options{AllowedMachines: []string{}})
	if err != nil || emptyPage.Total != 0 || len(emptyPage.Sessions) != 0 {
		t.Fatalf("AllowedMachines=[] Query leaked sessions: %+v %v", emptyPage, err)
	}
	emptyCounts, err := index.Counts(Options{AllowedMachines: []string{}})
	if err != nil || emptyCounts.Total != 0 {
		t.Fatalf("AllowedMachines=[] Counts leaked sessions: %+v %v", emptyCounts, err)
	}
}
