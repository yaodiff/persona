package kb

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Project parents and milestones (#12927, docs/assistant/knowledge.md "Project parents and milestones"): a project may
// sit under another project (its parent, a slug in meta.json), and carry milestone flags. Only the user sets either
// (the app, the assistant's kb_update at the user's word); setting one locks it (locked_fields "parent",
// "milestones"), so automatic organizing and dedupe never change it. Depth is not limited: the hub refuses only a
// project as its own parent, a cycle, and a parent that is missing, merged or not a project (ignored).

// Milestone states: still open, accepted, not accepted.
const (
	MilestoneOpen        = "open"
	MilestoneAccepted    = "accepted"
	MilestoneNotAccepted = "not_accepted"
)

const (
	maxMilestones      = 30
	maxMilestoneTitle  = 200
	SuggestedTreeDepth = 3 // deeper than this the apps show a soft hint; the hub does not refuse it
)

// Milestone is a flag on a project: a title, an optional target date (YYYY-MM-DD) and its acceptance state.
type Milestone struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Target string `json:"target,omitempty"`
	State  string `json:"state"`
}

// MilestoneEdit adds (no id), changes (id with the fields to change) or removes (id, remove) one milestone.
type MilestoneEdit struct {
	ID     string  `json:"id,omitempty"`
	Title  *string `json:"title,omitempty"`
	Target *string `json:"target,omitempty"`
	State  *string `json:"state,omitempty"`
	Remove bool    `json:"remove,omitempty"`
}

// TreeError is a refused parent or milestone change; Code is the API's error code (and its message key).
type TreeError struct {
	Code   string
	Detail string
}

func (e *TreeError) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// Tree error codes.
const (
	CodeParentSelf        = "parent_self"
	CodeParentCycle       = "parent_cycle"
	CodeParentNotFound    = "parent_not_found"
	CodeParentWithMerge   = "parent_with_merge"
	CodeMilestoneInvalid  = "milestone_invalid"
	CodeMilestoneNotFound = "milestone_not_found"
)

// ProjectRef names a project for a tree: its parent, its children.
type ProjectRef struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Status     string    `json:"status,omitempty"`
	OneLine    string    `json:"one_line,omitempty"`
	Updated    time.Time `json:"updated,omitzero"`
	Children   int       `json:"children,omitempty"`
	Milestones int       `json:"milestones,omitempty"`
}

// ProjectTree is a project's place in the tree as its page shows it.
type ProjectTree struct {
	// Parent: the live parent it shows under (merged parents followed to the project kept); nil at the top level.
	Parent *ProjectRef `json:"parent,omitempty"`
	// Ancestors: the way down from the top, the parent last; empty at the top level.
	Ancestors []ProjectRef `json:"ancestors"`
	// Children: the live projects directly under it, by name.
	Children   []ProjectRef `json:"children"`
	Milestones []Milestone  `json:"milestones"`
	// ParentLocked: the user set (or cleared) the parent, so automatic organizing leaves it.
	ParentLocked bool `json:"parent_locked,omitempty"`
	// Depth is 1 at the top level.
	Depth int `json:"depth"`
}

// hasTreeEdit: u sets a parent or changes milestones.
func (u UserUpdate) hasTreeEdit() bool {
	return u.Parent != nil || u.Milestones != nil || u.Milestone != nil
}

// treeEdit is a validated tree change: the parent to write (nil: leave it) and the milestones (nil: leave them).
type treeEdit struct {
	parent     *string
	milestones *[]Milestone
}

// projectTreeUpdate validates u's parent and milestones for the project id names (slug, else name; "" when the call
// creates it). The caller holds applyMu.
func (e *Engine) projectTreeUpdate(id string, u UserUpdate) (treeEdit, error) {
	var out treeEdit
	if !u.hasTreeEdit() {
		return out, nil
	}
	if u.MergeInto != "" || u.Unmerge {
		return out, &TreeError{Code: CodeParentWithMerge}
	}
	if u.Milestones != nil && u.Milestone != nil {
		return out, &TreeError{Code: CodeMilestoneInvalid, Detail: "send milestones or milestone, not both"}
	}
	self := e.resolve(KindProject, id)
	if self == "" {
		self = Slug(cmp.Or(u.Name, id))
	}
	if u.Parent != nil {
		want := strings.TrimSpace(*u.Parent)
		parent := ""
		if want != "" {
			p, ok := e.Store.Project(want)
			if !ok {
				p = e.FindProject(want)
			}
			if p == nil || !p.Live() || p.Status == "ignored" {
				return out, &TreeError{Code: CodeParentNotFound, Detail: want}
			}
			if p.Slug == self {
				return out, &TreeError{Code: CodeParentSelf}
			}
			parents := e.projectParents(e.Store.Projects())
			for at, seen := p.Slug, map[string]bool{}; at != "" && !seen[at]; at = parents[at] {
				seen[at] = true
				if at == self {
					return out, &TreeError{Code: CodeParentCycle, Detail: p.Name}
				}
			}
			parent = p.Slug
		}
		out.parent = &parent
	}
	if u.Milestones != nil || u.Milestone != nil {
		var have []Milestone
		if p, ok := e.Store.Project(self); ok {
			have = p.Milestones
		}
		next, err := editMilestones(have, u.Milestones, u.Milestone, time.Now())
		if err != nil {
			return out, err
		}
		out.milestones = &next
	}
	return out, nil
}

// editMilestones is have after a whole new list (replace) or one edit, each checked.
func editMilestones(have []Milestone, replace *[]Milestone, one *MilestoneEdit, now time.Time) ([]Milestone, error) {
	newID := func(taken []Milestone, n int) string {
		for i := n; ; i++ {
			id := fmt.Sprintf("m%x", now.UnixNano()/1000+int64(i))
			if !slices.ContainsFunc(taken, func(m Milestone) bool { return m.ID == id }) {
				return id
			}
		}
	}
	var out []Milestone
	if replace != nil {
		for i, m := range *replace {
			if m.ID == "" || slices.ContainsFunc(out, func(x Milestone) bool { return x.ID == m.ID }) {
				m.ID = newID(append(slices.Clone(out), *replace...), i)
			}
			out = append(out, m)
		}
	} else {
		out = slices.Clone(have)
		at := -1
		if one.ID != "" {
			at = slices.IndexFunc(out, func(m Milestone) bool { return m.ID == one.ID })
			if at < 0 {
				return nil, &TreeError{Code: CodeMilestoneNotFound, Detail: one.ID}
			}
		}
		switch {
		case one.Remove && at >= 0:
			out = slices.Delete(out, at, at+1)
		case one.Remove:
			return nil, &TreeError{Code: CodeMilestoneInvalid, Detail: "remove needs the milestone's id"}
		case at < 0:
			m := Milestone{ID: newID(out, 0), State: MilestoneOpen}
			if one.Title != nil {
				m.Title = *one.Title
			}
			if one.Target != nil {
				m.Target = *one.Target
			}
			if one.State != nil {
				m.State = *one.State
			}
			out = append(out, m)
		default:
			if one.Title != nil {
				out[at].Title = *one.Title
			}
			if one.Target != nil {
				out[at].Target = *one.Target
			}
			if one.State != nil {
				out[at].State = *one.State
			}
		}
	}
	if len(out) > maxMilestones {
		return nil, &TreeError{Code: CodeMilestoneInvalid, Detail: fmt.Sprintf("at most %d milestones", maxMilestones)}
	}
	for i := range out {
		m := &out[i]
		m.Title, m.Target = oneLine(m.Title), strings.TrimSpace(m.Target)
		if m.State == "" {
			m.State = MilestoneOpen
		}
		switch {
		case m.Title == "" || utf8.RuneCountInString(m.Title) > maxMilestoneTitle:
			return nil, &TreeError{Code: CodeMilestoneInvalid, Detail: fmt.Sprintf("a title of 1 to %d characters", maxMilestoneTitle)}
		case m.State != MilestoneOpen && m.State != MilestoneAccepted && m.State != MilestoneNotAccepted:
			return nil, &TreeError{Code: CodeMilestoneInvalid, Detail: "state is open, accepted or not_accepted"}
		case m.Target != "":
			if _, err := time.Parse(time.DateOnly, m.Target); err != nil {
				return nil, &TreeError{Code: CodeMilestoneInvalid, Detail: "target is a date, YYYY-MM-DD"}
			}
		}
	}
	return out, nil
}

// writeTree writes a validated tree edit onto project slug as the user: the fields change and lock.
func (e *Engine) writeTree(slug string, t treeEdit) error {
	if t.parent == nil && t.milestones == nil {
		return nil
	}
	return e.Store.editProjectTree(slug, func(f *treeFields) bool {
		if t.parent != nil {
			f.Parent = *t.parent
			f.LockedFields = addUnique(f.LockedFields, "parent")
		}
		if t.milestones != nil {
			f.Milestones = *t.milestones
			f.LockedFields = addUnique(f.LockedFields, "milestones")
		}
		slices.Sort(f.LockedFields)
		return true
	})
}

// treeFields are the parts of a project's meta.json a tree edit changes.
type treeFields struct {
	Parent       string      `json:"parent,omitempty"`
	Milestones   []Milestone `json:"milestones,omitempty"`
	LockedFields []string    `json:"locked_fields,omitempty"`
}

// editProjectTree changes a project's parent, milestones and locks in place under metaMu: the rest of meta.json stays
// as it is on disk. A missing project is ErrNotFound.
func (s *Store) editProjectTree(slug string, edit func(*treeFields) bool) (err error) {
	if s.readOnly {
		return ErrReadOnly
	}
	s.metaMu.Lock()
	changed := false
	defer func() {
		s.metaMu.Unlock()
		if err == nil && changed {
			s.notify(KindProject, slug, "metadata")
		}
	}()
	path := s.entityPath(KindProject, slug)
	current := map[string]json.RawMessage{}
	exists, err := readJSON(path, &current)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%s: %w", slug, ErrNotFound)
	}
	var f treeFields
	raw, _ := json.Marshal(current)
	_ = json.Unmarshal(raw, &f)
	if !edit(&f) {
		return nil
	}
	next := map[string]json.RawMessage{}
	for k, v := range current {
		next[k] = v
	}
	set := func(key string, v any, empty bool) {
		if empty {
			delete(next, key)
			return
		}
		b, _ := json.Marshal(v)
		next[key] = b
	}
	set("parent", f.Parent, f.Parent == "")
	set("milestones", f.Milestones, len(f.Milestones) == 0)
	set("locked_fields", f.LockedFields, len(f.LockedFields) == 0)
	before, _ := json.Marshal(current)
	after, _ := json.Marshal(next)
	changed = !bytes.Equal(before, after)
	if changed {
		err = writeJSON(path, next)
	}
	return err
}

// setParentAs moves project slug under parent as organizing does on the user's behalf (a merge, a delete, their
// undo): the parent changes, its lock stays as it was.
func (e *Engine) setParentAs(slug, parent string) error {
	return e.Store.editProjectTree(slug, func(f *treeFields) bool {
		if f.Parent == parent {
			return false
		}
		f.Parent = parent
		return true
	})
}

// projectParents maps every live project to the live parent it shows under: a merged parent is followed to the
// project it went into; a missing, ignored or deleted parent, or one that would close a cycle, leaves it at the top
// (absent). The data is not changed: a parent that comes back (restored, unmerged) counts again.
func (e *Engine) projectParents(all []*Project) map[string]string {
	by := map[string]*Project{}
	for _, p := range all {
		by[p.Slug] = p
	}
	live := func(slug string) string {
		for seen := map[string]bool{}; slug != "" && !seen[slug]; {
			seen[slug] = true
			p, ok := by[slug]
			switch {
			case !ok:
				return ""
			case p.Status == "merged":
				slug = p.MergedInto
			case p.Status == "ignored":
				return ""
			default:
				return slug
			}
		}
		return ""
	}
	out := map[string]string{}
	for _, p := range all {
		if !p.Live() || p.Parent == "" {
			continue
		}
		if to := live(p.Parent); to != "" && to != p.Slug {
			out[p.Slug] = to
		}
	}
	// A cycle (hand-edited files): its members go to the top level, the smallest slug first, until none is left.
	for _, p := range all {
		seen := map[string]bool{}
		for at := p.Slug; at != ""; at = out[at] {
			if seen[at] {
				var ring []string
				for x := at; ; x = out[x] {
					ring = append(ring, x)
					if out[x] == at {
						break
					}
				}
				slices.Sort(ring)
				delete(out, ring[0])
				break
			}
			seen[at] = true
		}
	}
	return out
}

// ProjectParents maps each live project with a parent to the parent it shows under (projectParents).
func (e *Engine) ProjectParents() map[string]string {
	return e.projectParents(e.Store.Projects())
}

// projectChildren are, by parent, the slugs of the live projects directly under it, by slug.
func projectChildren(parents map[string]string) map[string][]string {
	out := map[string][]string{}
	for child, parent := range parents {
		out[parent] = append(out[parent], child)
	}
	for _, kids := range out {
		slices.Sort(kids)
	}
	return out
}

// ProjectTreeOf is project slug's place in the tree: its parent and the way down to it, its children, its milestones.
func (e *Engine) ProjectTreeOf(slug string) (ProjectTree, bool) {
	all := e.Store.Projects()
	by := map[string]*Project{}
	for _, p := range all {
		by[p.Slug] = p
	}
	p, ok := by[slug]
	if !ok {
		return ProjectTree{}, false
	}
	parents := e.projectParents(all)
	kids := projectChildren(parents)
	ref := func(q *Project) ProjectRef {
		return ProjectRef{ID: q.Slug, Name: q.Name, Status: q.Status, OneLine: q.OneLine, Updated: q.Updated,
			Children: len(kids[q.Slug]), Milestones: len(q.Milestones)}
	}
	t := ProjectTree{Ancestors: []ProjectRef{}, Children: []ProjectRef{}, Milestones: p.Milestones, Depth: 1,
		ParentLocked: slices.Contains(p.LockedFields, "parent")}
	if t.Milestones == nil {
		t.Milestones = []Milestone{}
	}
	for at, seen := parents[slug], map[string]bool{slug: true}; at != "" && !seen[at]; at = parents[at] {
		seen[at] = true
		t.Ancestors = append([]ProjectRef{ref(by[at])}, t.Ancestors...)
	}
	t.Depth += len(t.Ancestors)
	if n := len(t.Ancestors); n > 0 {
		parent := t.Ancestors[n-1]
		t.Parent = &parent
	}
	for _, k := range kids[slug] {
		t.Children = append(t.Children, ref(by[k]))
	}
	slices.SortFunc(t.Children, func(a, b ProjectRef) int { return strings.Compare(normName(a.Name), normName(b.Name)) })
	return t, true
}

// TreeUndo is the update that puts back what u's tree edit changes on project id: its parent, its milestones as they
// are now. nil when u has no tree edit or the project does not exist yet.
func (e *Engine) TreeUndo(id string, u UserUpdate) *UserUpdate {
	if !u.hasTreeEdit() {
		return nil
	}
	slug := e.resolve(KindProject, id)
	p, ok := e.Store.Project(slug)
	if slug == "" || !ok {
		return nil
	}
	var undo UserUpdate
	if u.Parent != nil {
		parent := p.Parent
		undo.Parent = &parent
	}
	if u.Milestones != nil || u.Milestone != nil {
		list := slices.Clone(p.Milestones)
		if list == nil {
			list = []Milestone{}
		}
		undo.Milestones = &list
	}
	return &undo
}

// moveChildren hands the children of from to to as a merge or delete does, except a child to would sit under itself
// (to is that child, or below it), which goes to fallback instead. It answers who moved where, for the undo.
func (e *Engine) moveChildren(from, to, fallback string) ([]TreeMove, error) {
	all := e.Store.Projects()
	parents := map[string]string{}
	for _, p := range all {
		if p.Live() && p.Parent != "" {
			parents[p.Slug] = p.Parent
		}
	}
	var moves []TreeMove
	for _, p := range all {
		if !p.Live() || p.Parent != from {
			continue
		}
		dest := to
		for at, seen := to, map[string]bool{}; at != "" && !seen[at]; at = parents[at] {
			seen[at] = true
			if at == p.Slug {
				dest = fallback
				break
			}
		}
		if dest == p.Slug {
			dest = ""
		}
		if err := e.setParentAs(p.Slug, dest); err != nil {
			return moves, err
		}
		parents[p.Slug] = dest
		moves = append(moves, TreeMove{Slug: p.Slug, To: dest})
	}
	return moves, nil
}

// TreeMove is a child a merge or delete moved: which one, and the parent it got ("" the top level).
type TreeMove struct {
	Slug string `json:"slug"`
	To   string `json:"to,omitempty"`
}

// moveBack undoes moves: each child still where the move put it goes back under parent.
func (e *Engine) moveBack(moves []TreeMove, parent string) error {
	for _, m := range moves {
		p, ok := e.Store.Project(m.Slug)
		if !ok || !p.Live() || p.Parent != m.To {
			continue // gone, or moved again since: the later move stands
		}
		if err := e.setParentAs(m.Slug, parent); err != nil {
			return err
		}
	}
	return nil
}

// ProjectTreeIndex is, in one read, every live project's parent as it shows (projectParents) and milestones, for lists.
func (e *Engine) ProjectTreeIndex() (parents map[string]string, milestones map[string][]Milestone) {
	all := e.Store.Projects()
	milestones = map[string][]Milestone{}
	for _, p := range all {
		if p.Live() && len(p.Milestones) > 0 {
			milestones[p.Slug] = p.Milestones
		}
	}
	return e.projectParents(all), milestones
}
