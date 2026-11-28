package projection

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// What the projection works out over a whole list: the rows' order within each section (rank), the counts, and the
// places (proposal 3.3, 3.4, 3.7; spec/fixtures/todo-counts/README.md).

// routeLinkedInteractions gives one to-do the existing card: the session's tied to-do when it lists it, else the first
// that lists its worker. A released worker is listed only for its linked park. Non-owners keep their park, not a second ask.
func routeLinkedInteractions(list []Facts) {
	type sessionKey struct{ machine, session string }
	listing := map[sessionKey][]string{}
	for _, f := range list {
		linked := parkEvidenceInteractionID(&f) != ""
		for _, a := range f.Agents {
			if a.DismissedAt != nil || a.StartError != "" || a.ReleasedAt != nil && !linked {
				continue
			}
			key := sessionKey{a.MachineID, a.SessionID}
			if !slices.Contains(listing[key], f.ID) {
				listing[key] = append(listing[key], f.ID)
			}
		}
	}
	for i := range list {
		id := parkEvidenceInteractionID(&list[i])
		if id == "" {
			continue
		}
		var key sessionKey
		found := false
		for _, c := range list[i].Interactions {
			if c.ID == id {
				key, found = sessionKey{c.MachineID, c.SessionID}, true
				break
			}
		}
		if !found || len(listing[key]) == 0 {
			continue
		}
		home := listing[key][0]
	tied:
		for _, f := range list {
			for _, s := range f.Sessions {
				if s.MachineID == key.machine && s.SessionID == key.session && s.Todo != nil && slices.Contains(listing[key], s.Todo.ID) {
					home = s.Todo.ID
					break tied
				}
			}
		}
		if home == list[i].ID {
			continue
		}
		sv := *list[i].Supervision
		parked := *sv.Parked
		evidence := *parked.Evidence
		evidence.InteractionID = ""
		parked.Evidence = &evidence
		sv.Parked = &parked
		list[i].Supervision = &sv
	}
}

// Views is the view of every to-do of list at now, in the list's order: each related to the rest of the list (its
// steps, a superseded failed start), then ranked within its section.
func Views(list []Facts, now time.Time) []assistantwire.TodoSummary {
	views, _ := ViewsAndWaits(list, now)
	return views
}

// ViewsAndWaits works out both the view (TodoSummary) and the strongly-typed waiting reason (TodoWait, nil unless the
// row is in Waiting) of every to-do in list in a single pass (#16168).
func ViewsAndWaits(list []Facts, now time.Time) ([]assistantwire.TodoSummary, []*assistantwire.TodoWait) {
	related := relate(list)
	routeLinkedInteractions(related)
	placed := make([]placement, len(related))
	for i := range related {
		placed[i] = place(&related[i], now)
	}
	rank(related, placed, now)
	views := make([]assistantwire.TodoSummary, len(placed))
	waits := make([]*assistantwire.TodoWait, len(placed))
	for i := range placed {
		views[i] = placed[i].TodoSummary
		waits[i] = cloneWait(placed[i].wait)
	}
	return views, waits
}

// rank sets each placement's rank: its place among the rows of the same section, 0 first, by its section's rule
// (proposal 3.4 "Within a section"); placed[i] is list[i]'s, list related (its steps attached). Every to-do of the list
// is ranked, a step its parent carries too, and ties go by id, so the order never depends on the list's.
//
//   - Needs you and Can wait: the soonest deadline first (the earlier of the decisions' deadlines on the row, a carried
//     step's included, and the to-do's due), then the oldest ask (since).
//   - Working: the latest activity first, its steps' included.
//   - Waiting: the soonest wake first (a park's end, a usage limit's reset and its grace, the hub's next wake of a run
//     that waits on another Agent or an outside event, a carried step's), rows with none last, then the longest
//     waiting.
//   - To do: by due (overdue, today, later), then priority (high, normal, low), then the newest.
//   - Just finished and Done: the newest first.
//   - Someday: the soonest back first, then the newest.
func rank(list []Facts, placed []placement, now time.Time) {
	bySection := map[string][]int{}
	for i := range placed {
		bySection[placed[i].Section] = append(bySection[placed[i].Section], i)
	}
	var blocked map[string]int
	if needsYouRows := bySection[assistantwire.TodoListSectionNeedsYou]; len(needsYouRows) > 1 && anyTodosV2(list, needsYouRows) {
		blocked = downstreamBlockedCounts(list, placed)
	}
	for section, rows := range bySection {
		useCriticalPath := section == assistantwire.TodoListSectionNeedsYou && blocked != nil
		keys := make(map[int]rankKey, len(rows))
		for _, i := range rows {
			k := rankKeyOf(&list[i], &placed[i], now)
			if useCriticalPath {
				k.criticalPath = true
				k.canWait = placed[i].CanWait
				k.downstreamBlocked = blocked[list[i].ID]
			}
			keys[i] = k
		}
		slices.SortFunc(rows, func(a, b int) int {
			return cmp.Or(compareRank(section, keys[a], keys[b]), strings.Compare(list[a].ID, list[b].ID))
		})
		for n, i := range rows {
			placed[i].Rank = n
		}
	}
}

func anyTodosV2(list []Facts, rows []int) bool {
	for _, i := range rows {
		if list[i].TodosV2 {
			return true
		}
	}
	return false
}

// downstreamBlockedCounts computes, for each open to-do in list, how many distinct open downstream
// to-dos transitively depend on it via after (dep -> dependent) or part_of (step -> parent) edges.
// Closed and Someday to-dos are excluded, and graph traversal is cycle-safe.
func downstreamBlockedCounts(list []Facts, placed []placement) map[string]int {
	open := make(map[string]bool, len(list))
	for i := range list {
		if list[i].ID != "" && isOpen(placed[i].TodoSummary) {
			open[list[i].ID] = true
		}
	}
	if len(open) == 0 {
		return nil
	}
	downstream := make(map[string][]string, len(open))
	for i := range list {
		f := &list[i]
		if !open[f.ID] {
			continue
		}
		for _, dep := range f.After {
			if dep != "" && dep != f.ID && open[dep] {
				downstream[dep] = append(downstream[dep], f.ID)
			}
		}
		if f.PartOf != "" && f.PartOf != f.ID && !outsideDecisionRow(*f) && open[f.PartOf] {
			downstream[f.ID] = append(downstream[f.ID], f.PartOf)
		}
	}
	counts := make(map[string]int, len(open))
	for start := range open {
		if len(downstream[start]) == 0 {
			continue
		}
		visited := map[string]bool{start: true}
		queue := append([]string(nil), downstream[start]...)
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			if visited[cur] {
				continue
			}
			visited[cur] = true
			queue = append(queue, downstream[cur]...)
		}
		counts[start] = len(visited) - 1
	}
	return counts
}

// rankKey is what a section's rule compares; a zero time is "none", which goes last where a time leads.
type rankKey struct {
	deadline, since, activity, wake, due, back, created time.Time
	priority                                            int
	criticalPath                                        bool
	canWait                                             bool
	downstreamBlocked                                   int
}

func rankKeyOf(f *Facts, p *placement, now time.Time) rankKey {
	k := rankKey{since: p.Since, wake: p.wake, back: p.back, created: f.CreatedAt, activity: lastActivity(f, now)}
	for i := range f.Steps {
		k.activity = latest(k.activity, lastActivity(&f.Steps[i], now))
	}
	if due, ok := dueEnd(f); ok {
		k.due = due
	}
	k.deadline = earliest(p.deadline, k.due)
	switch f.Priority {
	case priorityHigh:
		k.priority = -1
	case priorityLow:
		k.priority = 1
	}
	return k
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a && b:
		return -1
	default:
		return 1
	}
}

// soonest compares two times where the earlier leads and none goes last.
func soonest(a, b time.Time) int {
	switch {
	case a.IsZero() && b.IsZero():
		return 0
	case a.IsZero():
		return 1
	case b.IsZero():
		return -1
	}
	return a.Compare(b)
}

// compareRank orders two rows of section by its rule.
func compareRank(section string, a, b rankKey) int {
	switch section {
	case assistantwire.TodoListSectionNeedsYou:
		if a.criticalPath && b.criticalPath {
			if c := compareBool(a.canWait, b.canWait); c != 0 {
				return c
			}
			if c := cmp.Compare(b.downstreamBlocked, a.downstreamBlocked); c != 0 {
				return c
			}
		}
		return cmp.Or(soonest(a.deadline, b.deadline), soonest(a.since, b.since))
	case assistantwire.TodoListSectionCanWait:
		return cmp.Or(soonest(a.deadline, b.deadline), soonest(a.since, b.since))
	case assistantwire.TodoListSectionWorking:
		return b.activity.Compare(a.activity)
	case assistantwire.TodoListSectionWaiting:
		return cmp.Or(soonest(a.wake, b.wake), soonest(a.since, b.since))
	case assistantwire.TodoListSectionTodo:
		return cmp.Or(soonest(a.due, b.due), cmp.Compare(a.priority, b.priority), b.created.Compare(a.created))
	case assistantwire.TodoListSectionJustFinished, assistantwire.TodoListSectionDone:
		return b.since.Compare(a.since)
	case assistantwire.TodoListSectionSomeday:
		return cmp.Or(soonest(a.back, b.back), b.created.Compare(a.created))
	}
	return 0
}

// carried says, for each to-do of list, whether it is a step its parent carries: its part_of names a to-do of the
// list that is open. Such a step is no row of its own in the counts (its parent counts once, proposal 3.4 "Steps roll
// up"); a step whose parent is closed or not listed counts as a row.
func carried(list []Facts, views []assistantwire.TodoSummary) []bool {
	open := map[string]bool{}
	for i := range list {
		open[list[i].ID] = views[i].Phase != assistantwire.TodoPhaseDone
	}
	out := make([]bool, len(list))
	for i, f := range list {
		out[i] = f.PartOf != "" && f.PartOf != f.ID && open[f.PartOf] && !outsideDecisionRow(f)
	}
	return out
}

// isOpen: not closed and not put off (the counts' open).
func isOpen(v assistantwire.TodoSummary) bool {
	return v.Phase != assistantwire.TodoPhaseDone && v.Section != assistantwire.TodoListSectionSomeday
}

// Counts are the list's numbers (proposal 3.7, #10226): open, every to-do that is not closed, in any section but
// Someday; needs_you, the to-dos in Needs you, quiet tasks left out as the badge leaves them out; in_progress, the rows
// of Working and Waiting; and the rows of each section, every section named. A step its parent carries is counted
// with its parent only. views[i] is list[i]'s.
func Counts(list []Facts, views []assistantwire.TodoSummary) assistantwire.TodoCounts {
	c := assistantwire.TodoCounts{Sections: make(map[string]int, len(assistantwire.TodoListSections))}
	for _, s := range assistantwire.TodoListSections {
		c.Sections[s] = 0
	}
	for i, skip := range carried(list, views) {
		v := views[i]
		if skip {
			continue
		}
		c.Sections[v.Section]++
		if isOpen(v) {
			c.Open++
		}
		if v.Section == assistantwire.TodoListSectionNeedsYou && !list[i].Quiet {
			c.NeedsYou++
		}
		if v.Section == assistantwire.TodoListSectionWorking || v.Section == assistantwire.TodoListSectionWaiting {
			c.InProgress++
		}
	}
	return c
}

// Places are the To-dos sidebar's places (proposal 3.3): All To-dos with N, Today and Upcoming (0: they draw no count
// yet), then each project with an open to-do, by slug (bytes), with its part of N. titles are the knowledge base's
// project titles by slug; a project it does not name is titled by its slug. views[i] is list[i]'s.
func Places(list []Facts, views []assistantwire.TodoSummary, titles map[string]string) assistantwire.TodoPlaces {
	counts := Counts(list, views)
	out := assistantwire.TodoPlaces{Counts: counts, Places: []assistantwire.TodoPlace{
		{ID: assistantwire.TodoPlaceKindAll, Kind: assistantwire.TodoPlaceKindAll, NeedsYou: counts.NeedsYou},
		{ID: assistantwire.TodoPlaceKindToday, Kind: assistantwire.TodoPlaceKindToday},
		{ID: assistantwire.TodoPlaceKindUpcoming, Kind: assistantwire.TodoPlaceKindUpcoming},
	}}
	needsYou := map[string]int{}
	for i, skip := range carried(list, views) {
		f, v := &list[i], views[i]
		if skip || f.Project == "" || !isOpen(v) {
			continue
		}
		n := needsYou[f.Project]
		if v.Section == assistantwire.TodoListSectionNeedsYou && !f.Quiet {
			n++
		}
		needsYou[f.Project] = n
	}
	slugs := make([]string, 0, len(needsYou))
	for slug := range needsYou {
		slugs = append(slugs, slug)
	}
	slices.Sort(slugs)
	for _, slug := range slugs {
		title := titles[slug]
		if title == "" {
			title = slug
		}
		out.Places = append(out.Places, assistantwire.TodoPlace{ID: "project:" + slug, Kind: assistantwire.TodoPlaceKindProject,
			Title: title, Project: slug, NeedsYou: needsYou[slug]})
	}
	return out
}
