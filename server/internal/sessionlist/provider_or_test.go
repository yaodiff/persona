package sessionlist

import (
	"net/url"
	"reflect"
	"testing"
)

func providerORParse(t *testing.T, values url.Values) Options {
	t.Helper()
	o, err := ParseProviderSets(values)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func providerORClone(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, list := range values {
		out[key] = append([]string(nil), list...)
	}
	return out
}

func providerORItem(id, provider, account, state string, seconds int) Item {
	item := testItem(id, seconds)
	item.Machine, item.Project, item.Source = "", "", "demo"
	item.Provider, item.Group, item.State = provider, account, state
	return item
}

func providerORTestRows() []Item {
	return []Item{
		providerORItem("demo:a", "provider-a", "account-a", "idle", 6),
		providerORItem("demo:b", "provider-b", "account-a", "unread", 5),
		providerORItem("demo:c", "provider-c", "account-b", "idle", 4),
		providerORItem("demo:d", "Provider-A", "account-a", "idle", 3),
		providerORItem("demo:e", "provider-a", "account-b", "unread", 2),
		providerORItem("demo:f", "provider-b", "account-b", "idle", 1),
	}
}

func TestProviderORMembershipCountsAndNormalization(t *testing.T) {
	var index Index
	index.Replace("demo", providerORTestRows())
	values := url.Values{
		"summary":  {"1"},
		"provider": {"provider-c", "provider-a", "Provider-A", "provider-a"},
	}
	if Enabled(url.Values{"provider": {"provider-a", "provider-c"}}) {
		t.Fatal("provider values alone opted into paging instead of retaining the legacy route")
	}
	page := mustQuery(t, &index, providerORParse(t, values))
	if got, want := sessionIDs(page), []string{"demo:a", "demo:c", "demo:d", "demo:e"}; !reflect.DeepEqual(got, want) || page.Total != len(want) {
		t.Fatalf("case-sensitive OR membership = %v, total %d, want %v", got, page.Total, want)
	}
	counts, err := index.Counts(providerORParse(t, values))
	if err != nil || counts.Total != 4 || counts.Revision != page.Revision ||
		!reflect.DeepEqual(counts.Machine, map[string]int{"": 4}) ||
		!reflect.DeepEqual(counts.Project, map[string]int{"": 4}) ||
		!reflect.DeepEqual(counts.Group, map[string]int{"account-a": 2, "account-b": 2}) ||
		!reflect.DeepEqual(counts.State, map[string]int{"idle": 3, "unread": 1}) ||
		!reflect.DeepEqual(counts.Todo, map[string]int{"": 4}) {
		t.Fatalf("full-set OR counts = %+v, %v", counts, err)
	}

	reordered := providerORClone(values)
	reordered["provider"] = []string{"Provider-A", "provider-a", "provider-c", "Provider-A"}
	again := mustQuery(t, &index, providerORParse(t, reordered))
	if !reflect.DeepEqual(sessionIDs(again), sessionIDs(page)) || again.Total != page.Total {
		t.Fatalf("reordered/duplicate spellings changed membership: %+v, want %+v", again, page)
	}

	and := providerORClone(values)
	and.Set("group", "account-a")
	and.Set("state", "idle")
	narrowed := mustQuery(t, &index, providerORParse(t, and))
	if got, want := sessionIDs(narrowed), []string{"demo:a", "demo:d"}; !reflect.DeepEqual(got, want) || narrowed.Total != 2 {
		t.Fatalf("provider OR did not AND with other filters: %v, total %d, want %v", got, narrowed.Total, want)
	}
	andCounts, err := index.Counts(providerORParse(t, and))
	if err != nil || andCounts.Total != narrowed.Total || !reflect.DeepEqual(andCounts.Group, map[string]int{"account-a": 2}) {
		t.Fatalf("AND counts = %+v, %v", andCounts, err)
	}

	// The old scalar remains exact and case-sensitive. A single empty scalar is
	// still the legacy unfiltered spelling, not an empty-provider membership set.
	for _, test := range []struct {
		provider string
		want     []string
	}{{"provider-a", []string{"demo:a", "demo:e"}}, {"Provider-A", []string{"demo:d"}}} {
		scalar := url.Values{"summary": {"1"}, "provider": {test.provider}}
		got := mustQuery(t, &index, providerORParse(t, scalar))
		if !reflect.DeepEqual(sessionIDs(got), test.want) || got.Total != len(test.want) {
			t.Errorf("scalar provider %q = %v, total %d, want %v", test.provider, sessionIDs(got), got.Total, test.want)
		}
	}
	legacy := url.Values{"provider": {""}}
	if Enabled(legacy) {
		t.Fatal("provider alone opted into paging")
	}
	got := mustQuery(t, &index, providerORParse(t, legacy))
	if got.Total != 6 || len(got.Sessions) != 6 {
		t.Fatalf("legacy single-empty provider was not unfiltered: %+v", got)
	}
}

func TestProviderORRejectsInvalidValueSets(t *testing.T) {
	tooMany := make([]string, 65)
	for n := range tooMany {
		tooMany[n] = "provider-a" // the limit applies before duplicate canonicalization
	}
	for name, values := range map[string]url.Values{
		"zero-length value list":   {"summary": {"1"}, "provider": nil},
		"empty member in an OR":    {"summary": {"1"}, "provider": {"provider-a", ""}},
		"more than 64 as supplied": {"summary": {"1"}, "provider": tooMany},
	} {
		t.Run(name, func(t *testing.T) {
			if !Enabled(values) {
				t.Error("the invalid provider set fell back to the legacy route")
			}
			_, err := ParseProviderSets(values)
			requireCode(t, err, "invalid_query")
		})
	}

	// Sixty-four supplied values are valid even when duplicates canonicalize to
	// one member. A single empty scalar remains the legacy unfiltered spelling.
	sixtyFour := make([]string, 64)
	for n := range sixtyFour {
		sixtyFour[n] = "provider-a"
	}
	providerORParse(t, url.Values{"summary": {"1"}, "provider": sixtyFour})
	legacy, err := ParseProviderSets(url.Values{"summary": {"1"}, "provider": {""}})
	if err != nil || legacy.Provider != "" {
		t.Fatalf("legacy single-empty provider: %+v, %v", legacy, err)
	}
}

func TestProviderORCursorBindsCanonicalSet(t *testing.T) {
	var index Index
	index.Replace("demo", providerORTestRows())
	firstValues := url.Values{
		"summary":  {"1"},
		"limit":    {"2"},
		"provider": {"provider-c", "provider-a", "Provider-A", "provider-a"},
	}
	first := mustQuery(t, &index, providerORParse(t, firstValues))
	if first.NextCursor == "" {
		t.Fatalf("first OR page has no continuation: %+v", first)
	}

	t.Run("same set reordered and duplicated", func(t *testing.T) {
		nextValues := providerORClone(firstValues)
		nextValues["provider"] = []string{"Provider-A", "provider-c", "provider-a", "Provider-A"}
		nextValues.Set("cursor", first.NextCursor)
		next := mustQuery(t, &index, providerORParse(t, nextValues))
		if got, want := sessionIDs(next), []string{"demo:d", "demo:e"}; !reflect.DeepEqual(got, want) || next.Total != 4 || next.Revision != first.Revision || next.HasMore {
			t.Fatalf("canonical continuation = %+v, want IDs %v", next, want)
		}
		counts, err := index.Counts(providerORParse(t, nextValues))
		if err != nil || counts.Total != 4 || counts.Revision != first.Revision {
			t.Fatalf("canonical frozen counts = %+v, %v", counts, err)
		}
	})

	changes := map[string][]string{
		"add a provider":     {"provider-a", "provider-c", "Provider-A", "provider-b"},
		"remove providers":   {"Provider-A"},
		"replace a provider": {"provider-a", "provider-c", "provider-b"},
		"omit the set":       nil,
	}
	for name, providers := range changes {
		t.Run(name, func(t *testing.T) {
			changed := providerORClone(firstValues)
			changed.Set("cursor", first.NextCursor)
			if providers == nil {
				changed.Del("provider")
			} else {
				changed["provider"] = providers
			}
			o := providerORParse(t, changed)
			_, err := index.Query(o)
			requireCode(t, err, "invalid_query")
			_, err = index.Counts(o)
			requireCode(t, err, "invalid_query")
		})
	}
}

func TestProviderORProviderEditsDriveDeltasAndCounts(t *testing.T) {
	var index Index
	rows := []Item{
		providerORItem("demo:stay", "provider-a", "account", "idle", 2),
		providerORItem("demo:enter", "provider-c", "account", "idle", 4),
		providerORItem("demo:leave-one", "provider-a", "account", "idle", 3),
		providerORItem("demo:leave-two", "provider-b", "account", "idle", 5),
		providerORItem("demo:outside", "provider-c", "account", "idle", 1),
	}
	index.Replace("demo", rows)
	values := url.Values{"summary": {"1"}, "provider": {"provider-b", "provider-a"}}
	baseline := mustQuery(t, &index, providerORParse(t, values))
	before, err := index.Counts(providerORParse(t, values))
	if err != nil {
		t.Fatal(err)
	}

	// Only providers change: timestamps and payloads deliberately stay fixed.
	rows[1].Provider = "provider-b"
	rows[2].Provider = "provider-c"
	rows[3].Provider = "provider-c"
	index.Replace("demo", rows)
	deltaValues := providerORClone(values)
	deltaValues.Set("since", baseline.Revision)
	delta := mustQuery(t, &index, providerORParse(t, deltaValues))
	if got, want := sessionIDs(delta), []string{"demo:enter"}; !reflect.DeepEqual(got, want) ||
		!reflect.DeepEqual(delta.Remove, []string{"demo:leave-two", "demo:leave-one"}) || !reflect.DeepEqual(delta.Added, []string{"demo:enter"}) || delta.Total != 2 {
		t.Fatalf("provider-edit delta = %+v, want enter %v and leave two rows", delta, want)
	}
	after, err := index.Counts(providerORParse(t, values))
	if err != nil {
		t.Fatal(err)
	}
	if before.Total != 3 || after.Total != 2 || after.Total-before.Total != -1 || after.Revision != delta.Revision {
		t.Fatalf("provider-edit count delta: before %+v, after %+v, delta revision %s", before, after, delta.Revision)
	}
}
