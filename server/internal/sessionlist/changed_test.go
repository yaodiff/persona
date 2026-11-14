package sessionlist

import (
	"slices"
	"testing"
)

func TestChangedListsTheIDsBetweenTwoRevisions(t *testing.T) {
	var i Index
	start := i.Revision()
	i.Replace("m1", []Item{{ID: "a"}, {ID: "b"}})
	first := i.Revision()
	i.Replace("m1", []Item{{ID: "a"}, {ID: "b", State: "working"}})
	i.Replace("m2", []Item{{ID: "c"}})
	second := i.Revision()
	i.Replace("m1", []Item{{ID: "b", State: "working"}}) // a removed
	third := i.Revision()

	for _, c := range []struct {
		from, to string
		want     []string
	}{
		{start, first, []string{"a", "b"}},
		{first, second, []string{"b", "c"}},
		{second, third, []string{"a"}},
		{first, third, []string{"a", "b", "c"}},
		{third, third, []string{}},
	} {
		got, ok := i.Changed(c.from, c.to, 64)
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("Changed(%s, %s) = %v, %v; want %v", c.from, c.to, got, ok, c.want)
		}
	}
	if got, ok := i.Changed(first, third, 2); ok {
		t.Errorf("3 ids with max 2: %v, want not ok", got)
	}
	if _, ok := i.Changed(third, first, 64); ok {
		t.Error("to before from must not be ok")
	}
	var other Index
	if _, ok := i.Changed(other.Revision(), third, 64); ok {
		t.Error("another index's revision must not be ok")
	}
	if _, ok := i.Changed("nonsense", third, 64); ok {
		t.Error("a malformed revision must not be ok")
	}
}

func TestChangedIsNotOKOnceTheJournalDroppedFrom(t *testing.T) {
	var i Index
	from := i.Revision()
	for n := range maxJournalRevisions + 1 {
		i.Replace("m", []Item{{ID: "a", State: string(rune('a' + n%26))}, {ID: "b", Project: string(rune('a' + n%2))}})
	}
	if got, ok := i.Changed(from, i.Revision(), 64); ok {
		t.Fatalf("evicted baseline: %v, want not ok", got)
	}
}
