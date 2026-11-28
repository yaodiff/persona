package sessionlist

import (
	"maps"
	"reflect"
	"sort"
	"strings"
)

// Query returns a full list or, with Since, collapsed changes since a retained
// revision. Continuations are frozen: both their order and revision remain
// unchanged while new batches arrive, even if the original journal is evicted.
func (i *Index) Query(options Options) (Page, error) {
	o, err := validate(options)
	if err != nil {
		return Page{}, err
	}
	var decoded map[*record]decodedDocument
	if o.Search != "" && o.Cursor == "" {
		decoded = i.decodeSearchDocuments(o) // before the lock: see decodeSearchDocuments
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.initialize()
	i.pruneSnapshots()
	var r *result
	var id string
	var offset int
	if o.Cursor != "" {
		s, snapshotID, position, err := i.cursorSnapshot(o)
		if err != nil {
			return Page{}, err
		}
		r, id, offset = s.result, snapshotID, position
	} else {
		if o.Since != "" {
			if _, err := i.sinceRevision(o.Since); err != nil {
				return Page{}, err
			}
		}
		if o.Search != "" {
			r, err = i.searchResult(o, decoded)
			if err != nil {
				return Page{}, err
			}
		} else {
			r = i.queryResult(o)
		}
		if len(r.events) > o.Limit {
			id = randomToken()
			if len(i.snapshots) == maxSnapshots {
				i.dropOldestSnapshot()
			}
			i.snapshots[id] = &snapshot{binding: o.binding(), result: r, expires: i.now().Add(snapshotTTL)}
			i.snapshotOrder = append(i.snapshotOrder, id)
		}
	}
	end := min(offset+o.Limit, len(r.events))
	p := Page{Sessions: make([]any, 0, end-offset), Remove: make([]string, 0), Added: make([]string, 0), Revision: r.counts.Revision, Total: r.counts.Total, HasMore: end < len(r.events)}
	if o.Search != "" {
		p.SearchHits = make(map[string]SearchHit, end-offset)
	}
	for _, e := range r.events[offset:end] {
		if e.value == nil {
			p.Remove = append(p.Remove, e.id)
			continue
		}
		value := e.value.item.Full
		if o.Summary {
			value = e.value.item.Summary
		}
		p.Sessions = append(p.Sessions, clone(value))
		if e.added {
			p.Added = append(p.Added, e.id)
		}
		if o.Search != "" {
			ranked := r.searchHits[e.id]
			hit := ranked.SearchHit
			if ranked.snippet != nil {
				hit.Evidence = searchSnippet(*ranked.snippet, r.searchTerms) // only the page's hits: see rankedHit
			}
			p.SearchHits[e.id] = hit
		}
	}
	if p.HasMore {
		p.NextCursor = i.cursorToken(id, end)
	}
	return p, nil
}

// Counts uses the same filtered view as Query. Without Cursor it reports current
// membership, validating Since if supplied; with Cursor it reports that frozen
// view's membership and revision rather than a newer revision.
func (i *Index) Counts(options Options) (Counts, error) {
	if options.Search != "" {
		return Counts{}, failure("invalid_query", "search does not support counts")
	}
	o, err := validate(options)
	if err != nil {
		return Counts{}, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.initialize()
	i.pruneSnapshots()
	if o.Cursor != "" {
		s, _, _, err := i.cursorSnapshot(o)
		if err != nil {
			return Counts{}, err
		}
		return copyCounts(s.result.counts), nil
	}
	if o.Since != "" {
		if _, err := i.sinceRevision(o.Since); err != nil {
			return Counts{}, err
		}
	}
	return copyCounts(i.countsResult(o.filters()).counts), nil
}

// CountMatchingState counts distinct items named by ids matching options whose state (under Roots) equals state.
func (i *Index) CountMatchingState(options Options, state string, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if options.Search != "" {
		return 0, failure("invalid_query", "search does not support counts")
	}
	o, err := validate(options)
	if err != nil {
		return 0, err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.initialize()
	i.pruneSnapshots()
	f := o.filters()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	if o.Cursor != "" {
		s, _, _, err := i.cursorSnapshot(o)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, e := range s.result.events {
			if e.value != nil && want[e.value.item.ID] && f.matches(e.value) && stateOf(e.value.item, f.Roots, f.CrossMachineHierarchy) == state {
				n++
			}
		}
		return n, nil
	}
	n := 0
	for id := range want {
		if r := i.items[id]; r != nil && f.matches(r) && stateOf(r.item, f.Roots, f.CrossMachineHierarchy) == state {
			n++
		}
	}
	return n, nil
}

func copyCounts(c Counts) Counts {
	c.Machine = maps.Clone(c.Machine)
	c.ActiveAgentsByMachine = maps.Clone(c.ActiveAgentsByMachine)
	c.Project = maps.Clone(c.Project)
	c.Group = maps.Clone(c.Group)
	c.State = maps.Clone(c.State)
	c.PaneSource = maps.Clone(c.PaneSource)
	c.Todo = maps.Clone(c.Todo)
	return c
}

func (f filters) matches(r *record) bool {
	if r == nil {
		return false
	}
	v := r.item
	todo := v.todo()
	paneSource := f.paneSource(v)
	return (f.ID != "" || f.IncludeHidden || !v.HiddenByDirectory) && (f.ID == "" || f.ID == v.ID) && (!f.NoPrivate || !v.Private) &&
		(!f.AgentsOnly || (!v.Assistant && !v.Private)) &&
		(!f.Roots || !v.hiddenByRoots(f.CrossMachineHierarchy)) &&
		(f.Under == "" || v.matchesUnder(f.UnderMachine, f.Under)) &&
		(f.State == "" || f.State == stateOf(v, f.Roots, f.CrossMachineHierarchy)) &&
		(f.Machine == "" || f.Machine == v.Machine) &&
		(f.AllowedMachines == "" || inAllowedMachines(f.AllowedMachines, v.Machine)) &&
		(f.Project == "" || f.Project == v.Project) &&
		(f.Places == "" || inPlaces(f.Places, v.Machine, v.Project)) &&
		(f.Spaces == "" || inPlaces(f.Spaces, v.Machine, v.Group)) &&
		(f.Group == "" || f.Group == v.Group) &&
		(f.Source == "" || f.Source == v.Source) &&
		(f.PaneSource == "" || f.PaneSource == paneSource) &&
		(f.ExcludePaneSource == "" || f.ExcludePaneSource != paneSource) &&
		(f.Waiting == "" || (f.Waiting == "1") == waitingOf(v, f.Roots, f.CrossMachineHierarchy)) &&
		(f.JustFinished == "" || (f.JustFinished == "1") == justFinishedOf(v, f.Roots, f.CrossMachineHierarchy)) &&
		(f.Manager == "" || f.Manager == r.owner) &&
		(f.Provider == "" || f.Provider == v.Provider) &&
		(f.Providers == "" || inProviders(f.Providers, v.Provider)) &&
		(f.Todo == "" || f.Todo == todo || f.Todo == TodoNone && todo == "") &&
		(f.Q == "" || strings.Contains(r.search, f.Q))
}

func sortEvents(events []event) {
	sort.Slice(events, func(a, b int) bool {
		if events[a].updated.Equal(events[b].updated) {
			return events[a].id < events[b].id
		}
		return events[a].updated.After(events[b].updated)
	})
}

func (i *Index) cached(key cacheKey) *result {
	entry := i.cache[key]
	if entry == nil {
		return nil
	}
	i.cacheClock++
	entry.used = i.cacheClock
	return entry.result
}

func (i *Index) remember(key cacheKey, r *result) *result {
	if i.cache[key] == nil && len(i.cache) == maxCache {
		var oldestKey cacheKey
		oldest := ^uint64(0)
		for k, entry := range i.cache {
			if entry.used < oldest {
				oldestKey, oldest = k, entry.used
			}
		}
		delete(i.cache, oldestKey)
	}
	i.cacheClock++
	i.cache[key] = &cacheEntry{result: r, used: i.cacheClock}
	return r
}

func emptyCounts(revision string, roots bool) Counts {
	counts := Counts{Machine: make(map[string]int), Project: make(map[string]int), Group: make(map[string]int), State: make(map[string]int), PaneSource: make(map[string]int), Todo: make(map[string]int), Revision: revision}
	if roots {
		counts.ActiveAgentsByMachine = make(map[string]int)
	}
	return counts
}

// stateOf is the state v is filtered and counted by: its folded state for Options.Roots (Fold.State, or
// Fold.LegacyState for legacy roots=1 queries without cross_machine_hierarchy=1 so cross-machine children that
// stay listed separately are not double-counted on their parent).
func stateOf(v Item, roots, crossMachine bool) string {
	if roots && v.Fold != nil {
		if !crossMachine && v.Fold.HasLegacyFold && v.Fold.LegacyState != "" {
			return v.Fold.LegacyState
		}
		if v.Fold.State != "" {
			return v.Fold.State
		}
	}
	return v.State
}

// waitingOf is whether v only waits in the background, as it is filtered and counted: by its folded state for
// Options.Roots (Fold.BackgroundWaiting, or Fold.LegacyBackgroundWaiting for legacy roots=1), its own when it folds none.
func waitingOf(v Item, roots, crossMachine bool) bool {
	if !v.BackgroundWaiting || !roots || v.Fold == nil {
		return v.BackgroundWaiting
	}
	if !crossMachine && v.Fold.HasLegacyFold {
		return v.Fold.LegacyBackgroundWaiting
	}
	return v.Fold.BackgroundWaiting
}

// justFinishedOf is whether v is Just finished, as it is filtered and counted: only while it shows idle (by its folded
// state for Options.Roots: a parent whose children work is not).
func justFinishedOf(v Item, roots, crossMachine bool) bool {
	return v.JustFinished && stateOf(v, roots, crossMachine) == "idle"
}

// The Agents tab filter uses current membership. A saved reference on an offline
// session still describes its origin in general session queries.
func (f filters) paneSource(v Item) string {
	if f.AgentsOnly && v.PaneSource == "terminal" && stateOf(v, f.Roots, f.CrossMachineHierarchy) == "offline" {
		return ""
	}
	return v.PaneSource
}

func addCount(c *Counts, v Item, f filters, delta int) {
	c.Total += delta
	state := stateOf(v, f.Roots, f.CrossMachineHierarchy)
	if f.Roots && !v.Assistant && state != "offline" {
		c.ActiveAgentsByMachine[v.Machine] += delta
		if c.ActiveAgentsByMachine[v.Machine] == 0 {
			delete(c.ActiveAgentsByMachine, v.Machine)
		}
	}
	if source := f.paneSource(v); source != "" {
		c.PaneSource[source] += delta
		if c.PaneSource[source] == 0 {
			delete(c.PaneSource, source)
		}
	}
	if waitingOf(v, f.Roots, f.CrossMachineHierarchy) {
		c.BackgroundWaiting += delta
	}
	if justFinishedOf(v, f.Roots, f.CrossMachineHierarchy) {
		c.JustFinished += delta
	}
	for _, pair := range []struct {
		m   map[string]int
		key string
	}{
		{c.Machine, v.Machine}, {c.Project, v.Project}, {c.Group, v.Group}, {c.State, state}, {c.Todo, v.todo()},
	} {
		pair.m[pair.key] += delta
		if pair.m[pair.key] == 0 {
			delete(pair.m, pair.key)
		}
	}
}

// exactResult deliberately bypasses ordered views and their caches. A miss,
// changed revision or new filter still touches only the selected map entry.
func (i *Index) exactResult(f filters) *result {
	r := &result{counts: emptyCounts(i.revisionToken(), f.Roots), ordered: true}
	if item := i.items[f.ID]; f.matches(item) {
		addCount(&r.counts, item.item, f, 1)
		r.events = []event{{id: f.ID, updated: item.item.UpdatedAt, value: item}}
	}
	return r
}

// countsResult never builds or sorts an ordered list. Unfiltered counts are
// maintained on updates, so a one-row delta does not scan 8,000 unrelated rows
// merely to report Total. Filtered counts are scanned only on a cache miss.
func (i *Index) countsResult(f filters) *result {
	if f.ID != "" {
		return i.exactResult(f)
	}
	key := cacheKey{filters: f}
	if r := i.cached(key); r != nil {
		return r
	}
	var counts Counts
	if f == (filters{}) {
		counts = copyCounts(i.counts)
	} else {
		counts = emptyCounts(i.revisionToken(), f.Roots)
		for _, item := range i.items {
			if f.matches(item) {
				addCount(&counts, item.item, f, 1)
			}
		}
	}
	return i.remember(key, &result{counts: counts})
}

func (i *Index) fullResult(f filters) *result {
	if f.ID != "" {
		return i.exactResult(f)
	}
	key := cacheKey{filters: f}
	if r := i.cached(key); r != nil && r.ordered {
		return r
	}
	r := &result{counts: i.countsResult(f).counts, ordered: true}
	for id, item := range i.items {
		if !f.matches(item) {
			continue
		}
		r.events = append(r.events, event{id: id, updated: item.item.UpdatedAt, value: item})
	}
	sortEvents(r.events)
	return i.remember(key, r)
}

func (i *Index) queryResult(o Options) *result {
	if o.Since == "" {
		return i.fullResult(o.filters())
	}
	key := cacheKey{filters: o.filters(), Since: o.Since}
	if r := i.cached(key); r != nil {
		return r
	}
	// Query already validated this baseline. Cursor continuations never touch
	// the journal, so retaining a frozen delta does not retain journal history.
	_, since, _ := parseRevision(o.Since)
	collapsed := make(map[string]change)
	for _, entry := range i.journal {
		if entry.revision <= since {
			continue
		}
		for id, c := range entry.changes {
			if previous, exists := collapsed[id]; exists {
				c.before = previous.before
			}
			collapsed[id] = c
		}
	}
	r := &result{counts: i.countsResult(o.filters()).counts}
	f := o.filters()
	for id, c := range collapsed {
		before, after := f.matches(c.before), f.matches(c.after)
		if after {
			if before && reflect.DeepEqual(c.before.item, c.after.item) {
				continue
			}
			r.events = append(r.events, event{id: id, updated: c.after.item.UpdatedAt, value: c.after, added: !before})
		} else if before {
			updated := c.before.item.UpdatedAt
			if c.after != nil {
				updated = c.after.item.UpdatedAt
			}
			r.events = append(r.events, event{id: id, updated: updated})
		}
	}
	sortEvents(r.events)
	return i.remember(key, r)
}
