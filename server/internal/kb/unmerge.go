package kb

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// UserUpdate is an Update from the user's own paths (the assistant's kb_update, HTTP PUT). Unmerge is not part of
// kbwire.Update, so the extension protocol (kb.update) has no way to send it.
type UserUpdate struct {
	Update
	// ExpectedPinned guards a recording correction; omission keeps the original unconditional update.
	ExpectedPinned *string `json:"expected_pinned,omitempty"`
	// ProjectMode and SourceURLs are the owner's project settings. An empty SourceURLs list clears the pointers.
	ProjectMode *string  `json:"project_mode,omitempty"`
	SourceURLs  []string `json:"source_urls,omitempty"`
	// Parent (projects): the project to sit under, by slug or name; "" moves it to the top level. Milestones replaces
	// its milestone flags; Milestone adds, changes or removes one (project_tree.go). Each locks what it sets.
	Parent     *string        `json:"parent,omitempty"`
	Milestones *[]Milestone   `json:"milestones,omitempty"`
	Milestone  *MilestoneEdit `json:"milestone,omitempty"`
	// Unmerge (projects): undo the merge that folded this project into another (Unmerge); nothing else applies.
	Unmerge bool `json:"unmerge,omitempty"`
	// LockLists: a list a removal takes a link out of is locked whole too, on both entries, so automatic organizing
	// changes nothing in it (locked_fields); false keeps out only the removed links (exclude.go). Unset: as lock says,
	// what a removal did before exclusions, so a client that does not send it gets what it asked for.
	LockLists *bool `json:"lock_lists,omitempty"`
	// Allow*: links the user removed that automatic organizing may make again; nothing is linked by them.
	AllowPeople   []string `json:"allow_people,omitempty"`
	AllowChats    []string `json:"allow_chats,omitempty"`
	AllowProjects []string `json:"allow_projects,omitempty"`
}

// ApplyUser is Apply, or Unmerge when u asks for it.
func (e *Engine) ApplyUser(kind, id string, u UserUpdate) (string, error) {
	if u.ExpectedPinned != nil && (kind != KindRecording || u.Pinned == nil) {
		return "", fmt.Errorf("expected_pinned requires a recording pinned update")
	}

	if !u.Unmerge {
		return e.applyUser(kind, id, u)
	}
	if kind != KindProject {
		return "", errors.New("unmerge applies to projects")
	}
	if u.ProjectMode != nil || u.SourceURLs != nil {
		return "", fmt.Errorf("%w: set project_mode or source_urls separately from unmerge", ErrInvalidProjectSource)
	}
	if u.hasTreeEdit() {
		return "", &TreeError{Code: CodeParentWithMerge}
	}
	return e.Unmerge(id)
}

// Unmerge undoes, as the user, the merge that folded the project id names (its slug, or its name) into another
// (unmergeProject). Returns its slug.
func (e *Engine) Unmerge(id string) (string, error) {
	if e.Store.ReadOnly() {
		return "", ErrReadOnly
	}
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	p := e.mergedProject(id)
	if p == nil {
		return "", fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	return p.Slug, e.unmergeProject(p)
}

// mergedProject is the project id names for an unmerge: the one with that slug, else the most recently merged one
// with that name, else the live one FindProject gives (which unmergeProject refuses as not merged).
func (e *Engine) mergedProject(id string) *Project {
	if p, ok := e.Store.Project(id); ok {
		return p
	}
	n := normName(id)
	var merged *Project
	for _, p := range e.Store.Projects() {
		if !p.Live() && n != "" && (normName(p.Name) == n || p.Slug == Slug(id)) {
			merged = preferProject(merged, p)
		}
	}
	if merged != nil {
		return merged
	}
	return e.FindProject(id)
}

// unmergeProject undoes the merge that folded drop into another project (MergeProjects, merge_into): drop lists again
// with its status from before, and its chats, people and items name it again.
//
// A recorded merge (drop.Merge) is undone exactly: the aliases, people, chats, items, code directories, keywords and
// Moments posts it gave the kept project leave it, the posts of its own it pushed past the cap come back, the pinned
// facts and locks a manual merge joined go back to the kept project's own, and the chats, people and items it moved
// from drop name drop alone again. A merge made before the record leaves no way to tell which of drop's people, chats and items the kept
// project had already, and it usually had many (dedupe pairs projects by the people and chats they share): they stay
// on the kept project too (remove_people / remove_chats take them off), and only drop's name and aliases leave it,
// since a live project's alias must not be another live project's name or alias.
//
// Either way drop's lines leave the kept project's timeline (drop's own still has them), both projects are marked
// dirty so their profiles (goal, kind, status, tags) are rewritten from what each now has, and dedupe never merges
// the pair again. What another project still merged into the kept one brought it too stays: that merge still holds.
func (e *Engine) unmergeProject(drop *Project) error {
	if drop.Live() {
		return fmt.Errorf("%s is not merged", drop.Slug)
	}
	keep, found := e.Store.Project(drop.MergedInto)
	if drop.referenceOnly() || found && keep.referenceOnly() {
		return fmt.Errorf("%w: cannot unmerge projects", ErrReferenceOnly)
	}
	if found && !keep.Live() {
		return fmt.Errorf("%s went into %s, which was merged into %s since: unmerge %s first", drop.Slug, keep.Slug, keep.MergedInto, keep.Slug)
	}
	rec, now := drop.Merge, time.Now().UTC()
	// held: what drop's merge brought keep that a project merged into keep after it has too (handOver).
	var held ProjectMerge
	// siblings: the other projects still merged into keep; records: those with a merge record, written once keep's
	// lists are worked out; moved: the references drop's merge moved that name drop alone again.
	var siblings, moved []string
	var records []*Project
	if found {
		// The split goes first: if a write below fails, the merge stays and can be unmerged again, and dedupe does
		// not ask about the pair meanwhile.
		if err := e.Store.putDedupeVerdict(pairKey(keep.Slug, drop.Slug), dedupeVerdict{Split: true, Reason: "拆开了", At: now}); err != nil {
			return err
		}
		for _, p := range e.Store.Projects() {
			if p.Live() || p.MergedInto != keep.Slug || p.Slug == drop.Slug {
				continue
			}
			siblings = append(siblings, p.Slug)
			if rec != nil && p.Merge != nil {
				records = append(records, p)
				if !p.Merge.At.Before(rec.At) {
					handOver(rec, p, &held)
				}
			}
		}
		if rec != nil {
			moved = removeStrings(rec.Moved, held.Moved)
			keep.Aliases = removeStrings(keep.Aliases, rec.Aliases)
			keep.People = removeStrings(keep.People, removeStrings(rec.People, held.People))
			keep.Chats = removeStrings(keep.Chats, removeStrings(rec.Chats, held.Chats))
			keep.Items = removeStrings(keep.Items, removeStrings(rec.Items, held.Items))
			keep.Dirs = removeStrings(keep.Dirs, removeStrings(rec.Dirs, held.Dirs))
			keep.Keywords = removeStrings(keep.Keywords, removeStrings(rec.Keywords, held.Keywords))
			gone := removeStrings(rec.Moments, held.Moments)
			leaving := func(m ProjectMoment) bool { return slices.Contains(gone, m.ID) }
			keep.Moments = slices.DeleteFunc(slices.Clone(keep.Moments), leaving)
			// The posts merges pushed past the cap, drop's and those of the merges that still hold, come back as
			// far as the room allows, except drop's own that leave; the rest wait on those merges' records for an
			// unmerge to make room.
			pool := slices.Clone(rec.Evicted)
			for _, s := range records {
				pool = append(pool, s.Merge.Evicted...)
			}
			pool = slices.DeleteFunc(pool, leaving)
			keep.Moments = mergeMoments(keep.Moments, pool)
			kept := momentIDs(keep.Moments)
			rest := slices.DeleteFunc(pool, func(m ProjectMoment) bool { return slices.Contains(kept, m.ID) })
			for _, s := range records {
				s.Merge.Evicted = mergeMoments(nil, rest)
			}
			// Pinned facts and locks changed since the merge are the user's newer word and stay.
			if rec.Pinned != nil && keep.Pinned == joinPinned(*rec.Pinned, drop.Pinned) {
				keep.Pinned = *rec.Pinned
			}
			if l := rec.Locks; l != nil {
				if joinLocks(*l, drop).same(MergeLocks{Fields: keep.LockedFields, Locked: keep.Locked}) {
					keep.LockedFields, keep.Locked = l.Fields, l.Locked
				}
			}
			for list, ids := range rec.Excluded {
				taggingInclude(&keep.Tagging, list, ids...)
			}
		} else {
			names := map[string]bool{normName(drop.Name): true}
			for _, a := range drop.Aliases {
				names[normName(a)] = true
			}
			keep.Aliases = slices.DeleteFunc(slices.Clone(keep.Aliases), func(a string) bool { return names[normName(a)] })
		}
		for _, s := range records {
			if err := e.Store.putEntity(KindProject, s.Slug, s, &s.Tagging, true); err != nil {
				return err
			}
		}
		if err := e.unmergeLogs(keep.Slug, drop.Slug, siblings); err != nil {
			return err
		}
		keep.Dirty, keep.Updated = true, now
		if err := e.Store.putEntity(KindProject, keep.Slug, keep, &keep.Tagging, true); err != nil {
			return err
		}
		if rec != nil {
			if err := e.excludeBack(keep.Slug, rec.Excluded, false); err != nil {
				return err
			}
		}
		e.index(KindProject, keep.Slug, keep.Name, keep.Tagging, keep.OneLine, keep.Updated)
	}
	// drop's chats, people and items name it again; the ones the merge moved stop naming the kept project.
	err := e.relinkRefs(drop, true, func(ref string, list []string) ([]string, bool) {
		out := addUnique(slices.Clone(list), drop.Slug)
		if slices.Contains(moved, ref) {
			out = removeStrings(out, []string{keep.Slug})
		}
		return out, !slices.Equal(out, list)
	})
	if err != nil {
		return err
	}
	drop.Status = "active"
	if rec != nil && rec.Status != "" && rec.Status != "merged" {
		drop.Status = rec.Status
	}
	drop.MergedInto, drop.Merge, drop.Dirty, drop.Updated = "", nil, true, now
	if err := e.Store.putEntity(KindProject, drop.Slug, drop, &drop.Tagging, true); err != nil {
		return err
	}
	e.index(KindProject, drop.Slug, drop.Name, drop.Tagging, drop.OneLine, drop.Updated)
	if rec != nil { // the projects the merge moved off drop come back under it
		if err := e.moveBack(rec.Children, drop.Slug); err != nil {
			return err
		}
	}
	if found { // last, so a retry after a failed write does not note the unmerge twice
		_ = e.Store.AppendLog(keep.Slug, now, "Persona", fmt.Sprintf("拆开了项目「%s」", drop.Name))
	}
	return nil
}

// handOver handles sib, a project merged into keep after drop, when drop's merge (rec) is undone: what rec brought
// keep that sib has too stays on keep, since sib's merge still holds, and the chats, people and items among it that rec
// moved go on naming keep. Both go into held, and onto sib's record as if sib's merge had brought them, which it would
// have without drop's, so unmerging sib later takes them off keep. A merge before drop's is not asked: keep had what
// it brought when drop's came, so what rec lists is what the user took off keep in between. The caller writes sib.
func handOver(rec *ProjectMerge, sib *Project, held *ProjectMerge) {
	has := ProjectMerge{People: sib.People, Chats: sib.Chats, Items: sib.Items, Dirs: sib.Dirs, Keywords: sib.Keywords,
		Moments: momentIDs(sib.Moments), Moved: projectRefs(sib)} // Moved: sib's references, to match rec.Moved against
	from, in, into, to := rec.joined(), has.joined(), held.joined(), sib.Merge.joined()
	for i := range from {
		both := slices.DeleteFunc(slices.Clone(*from[i]), func(v string) bool { return !slices.Contains(*in[i], v) })
		*into[i] = addUnique(*into[i], both...)
		*to[i] = addUnique(*to[i], both...)
	}
}

// joined are the lists of a merge record that name what the merge joined into the kept project, in a fixed order.
func (m *ProjectMerge) joined() []*[]string {
	return []*[]string{&m.People, &m.Chats, &m.Items, &m.Dirs, &m.Keywords, &m.Moments, &m.Moved}
}

// unmergeLogs takes drop's lines out of keep's timeline, where mergeLogs put them. A line both had before the merge
// (same minute, source and text) goes too; drop's timeline keeps it. A line one of others (projects still merged into
// keep) has too stays.
func (e *Engine) unmergeLogs(keepSlug, dropSlug string, others []string) error {
	theirs := map[string]bool{}
	for _, l := range e.Store.Log(dropSlug, math.MaxInt) {
		theirs[l] = true
	}
	for _, slug := range others {
		for _, l := range e.Store.Log(slug, math.MaxInt) {
			delete(theirs, l)
		}
	}
	if len(theirs) == 0 {
		return nil
	}
	lines := e.Store.Log(keepSlug, math.MaxInt)
	kept := slices.DeleteFunc(slices.Clone(lines), func(l string) bool { return theirs[l] })
	if len(kept) == len(lines) {
		return nil
	}
	return e.Store.WriteLog(keepSlug, kept)
}
