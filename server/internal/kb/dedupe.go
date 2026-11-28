package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Project dedupe (docs/assistant/knowledge.md "Project deduplication"). The same work is often discussed in several groups under different
// names, and a chat's compression only sees project names: each group grew its own project (2026-11-01: two names
// for the same work with no word in common, 10 people in common). Pairs of live projects that share people, chats
// or name words go to the model with both profiles; the ones it calls the same are merged. Each pair's verdict is
// kept (projects/.dedupe.json) so it is asked once, and again only after either side changed a lot.

const (
	// dedupeRecheck: a pair judged different is asked again after this long (both have grown since).
	dedupeRecheck = 14 * 24 * time.Hour
	// dedupeMinShared is the fewest people in common for a pair to be a candidate on people alone.
	dedupeMinShared = 3
)

type dedupeVerdict struct {
	Same   bool      `json:"same"`
	Split  bool      `json:"split,omitempty"` // the user unmerged the pair (unmergeProject): never asked about again
	Reason string    `json:"reason,omitempty"`
	At     time.Time `json:"at"`
}

// settled: the pair is not asked about now.
func (v dedupeVerdict) settled() bool {
	return v.Same || v.Split || time.Since(v.At) < dedupeRecheck
}

func (s *Store) dedupePath() string { return filepath.Join(s.Dir, KindProject, ".dedupe.json") }

func (s *Store) dedupeVerdicts() map[string]dedupeVerdict {
	s.dedupeMu.Lock()
	defer s.dedupeMu.Unlock()
	return s.dedupeVerdictsLocked()
}

func (s *Store) dedupeVerdictsLocked() map[string]dedupeVerdict {
	out := map[string]dedupeVerdict{}
	if raw, err := os.ReadFile(s.dedupePath()); err == nil {
		_ = json.Unmarshal(raw, &out)
	}
	return out
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

// dedupeCandidate is a pair worth asking about, with how strongly it looks like one project.
type dedupeCandidate struct {
	a, b   *Project
	shared int
	score  float64
}

// dedupeCandidates ranks pairs of live, not ignored projects by what they have in common: people (relative to the
// smaller side), chats, documents, and words of their names and aliases.
func dedupeCandidates(projects []*Project) []dedupeCandidate {
	// A project with a parent is locked (protected); one with projects under it is the user's organizing too, and the
	// pair would likely look alike: neither is asked about (project_tree.go).
	parents := map[string]bool{}
	for _, p := range projects {
		if p.Live() && p.Parent != "" {
			parents[p.Parent] = true
		}
	}
	var live []*Project
	for _, p := range projects {
		if p.Live() && p.Status != "ignored" && !protectedProject(p) && !parents[p.Slug] {
			live = append(live, p)
		}
	}
	var out []dedupeCandidate
	for i := 0; i < len(live); i++ {
		for j := i + 1; j < len(live); j++ {
			a, b := live[i], live[j]
			shared := overlap(a.People, b.People)
			chats := overlap(a.Chats, b.Chats)
			items := overlap(a.Items, b.Items)
			words := overlap(nameWords(a), nameWords(b))
			smaller := min(len(a.People), len(b.People))
			// Everyone is in a few big groups: people alone count only when it is a good part of the smaller side.
			byPeople := shared >= dedupeMinShared && smaller > 0 && float64(shared)/float64(smaller) >= 0.3
			if !byPeople && chats == 0 && items == 0 && words < 2 {
				continue
			}
			score := float64(chats)*3 + float64(items) + float64(words)*2
			if smaller > 0 {
				score += 5 * float64(shared) / float64(smaller)
			}
			out = append(out, dedupeCandidate{a: a, b: b, shared: shared, score: score})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return pairKey(out[i].a.Slug, out[i].b.Slug) < pairKey(out[j].a.Slug, out[j].b.Slug)
	})
	return out
}

func overlap(a, b []string) int {
	set := map[string]bool{}
	for _, x := range a {
		set[x] = true
	}
	n := 0
	seen := map[string]bool{}
	for _, x := range b {
		if set[x] && !seen[x] {
			seen[x] = true
			n++
		}
	}
	return n
}

// nameWords are the words of a project's name and aliases, lowercased, two characters or more: "Acme codex plan" →
// acme, codex, plan. Common words every project has say nothing and are left out.
func nameWords(p *Project) []string {
	var out []string
	for _, n := range append([]string{p.Name}, p.Aliases...) {
		for _, w := range strings.Split(Slug(n), "-") {
			if len([]rune(w)) >= 2 && !genericWord[w] {
				out = addUnique(out, w)
			}
		}
	}
	return out
}

var genericWord = map[string]bool{"项目": true, "方案": true, "需求": true, "支持": true, "优化": true, "改版": true, "上线": true,
	"迭代": true, "联调": true, "接入": true, "agent": true, "ai": true, "the": true, "and": true}

// DedupeProjects asks about at most max candidate pairs not judged yet (or judged different a while ago) and merges
// the ones the model calls the same. Returns how many were merged.
func (e *Engine) DedupeProjects(ctx context.Context, max int) int {
	if e.Model == nil {
		return 0
	}
	verdicts := e.Store.dedupeVerdicts()
	asked, merged := 0, 0
	gone := map[string]bool{} // merged away this run: their other pairs are stale
	projects := e.Store.Projects()
	for _, p := range projects {
		p.People = e.externalPeople(p.People)
	}
	for _, c := range dedupeCandidates(projects) {
		if asked >= max || ctx.Err() != nil {
			break
		}
		if gone[c.a.Slug] || gone[c.b.Slug] {
			continue
		}
		// One side named in the shown form of the other (「X（a、b）」 and X) is a copied listing, not a second project
		// to judge: the model kept the copy and merged the real one into it (#5824).
		if isShownFormOf(c.a.Name, c.b.Name) || isShownFormOf(c.b.Name, c.a.Name) {
			continue
		}
		if e.shareProjectSource(c.a, c.b) {
			continue
		}
		key := pairKey(c.a.Slug, c.b.Slug)
		if v, ok := verdicts[key]; ok && v.settled() {
			continue
		}
		asked++
		same, keepA, reason, err := e.judgeDuplicate(ctx, c)
		if err != nil {
			if ai.IsPaused(err) { // not judged: asked again once the connection is back
				break
			}
			e.logf("dedupe %s: %v", key, err)
			continue
		}
		keep, drop := c.a, c.b
		if !keepA {
			keep, drop = c.b, c.a
		}
		didMerge, err := e.commitDedupe(key, dedupeVerdict{Same: same, Reason: reason, At: time.Now().UTC()}, keep.Slug, drop.Slug)
		if err != nil {
			e.logf("dedupe %s: %v", key, err)
			continue
		}
		if !didMerge {
			continue
		}
		e.logf("dedupe: merged %s into %s (%s)", drop.Slug, keep.Slug, reason)
		gone[drop.Slug] = true
		merged++
	}
	return merged
}

func (e *Engine) judgeDuplicate(ctx context.Context, c dedupeCandidate) (same, keepA bool, reason string, err error) {
	owner := e.knowledgeOwner()
	side := func(key string, p *Project) prompts.KBDedupeProject {
		var chats []string
		for i, id := range p.Chats {
			if i >= 6 {
				break
			}
			if ch, ok := e.Store.Chat(id); ok && ch.Name != "" {
				chats = append(chats, ch.Name)
			}
		}
		return prompts.KBDedupeProject{Language: owner.Language, Key: key, Name: p.Name, Aliases: p.Aliases, OneLine: p.OneLine, Goal: p.Goal,
			Summary: clip(e.Store.Summary(KindProject, p.Slug), 1500), Chats: chats, People: len(p.People)}
	}
	in := prompts.RenderKBDedupeInput(prompts.KBDedupeInput{Language: owner.Language, A: side("A", c.a), B: side("B", c.b), Shared: c.shared})
	var d struct {
		Same   bool   `json:"same"`
		Sure   bool   `json:"sure"`
		Keep   string `json:"keep"`
		Reason string `json:"reason"`
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := e.modelFor("kb.dedupe").JSONMax(cctx, prompts.RenderKBDedupe(owner), in, 600, &d); err != nil {
		return false, false, "", err
	}
	reason = oneLine(d.Reason)
	if d.Same && (!d.Sure || hedged(reason)) {
		// Merging is hard to undo: a "same" the model is not sure of is kept as different (asked again later).
		return false, false, "存疑，不合并：" + reason, nil
	}
	return d.Same, !strings.EqualFold(strings.TrimSpace(d.Keep), "B"), reason, nil
}

// hedgeWords in a "same" verdict's reason mean the model doubts it (merges whose reasons read "whether the direction is the same is doubtful",
// #2546).
var hedgeWords = []string{"存疑", "不确定", "拿不准", "不一定", "待确认", "或许", "也许", "似乎", "不太像", "可能不是",
	"uncertain", "unsure", "unclear", "doubt", "not sure", "not certain", "cannot tell", "can't tell", "maybe", "perhaps", "might", "may be", "may not", "possibly", "probably", "seems",
	"not the same", "not same", "not identical", "different project", "different work", "distinct projects",
	"不確か", "不明", "確信でき", "判断でき", "疑問", "疑わし", "かもしれ", "おそらく", "可能性"}

func hedged(reason string) bool {
	reason = strings.ReplaceAll(strings.ToLower(reason), "’", "'")
	for _, w := range hedgeWords {
		if strings.Contains(reason, w) {
			return true
		}
	}
	return false
}

// MergeProjects folds drop into keep: drop's name and aliases become keep's aliases; people, chats, documents,
// code directories, keywords and Moments posts are joined; the timelines are interleaved; the chats, people and items
// that named drop name keep. drop stays on disk as merged (merged_into), hidden from lists, with what keep gained
// (merge), so unmergeProject can undo it. keep is marked dirty: its profile is rewritten from both.
func (e *Engine) MergeProjects(keepSlug, dropSlug, why string) error {
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	return e.mergeProjects(keepSlug, dropSlug, why, false)
}

func protectedProject(p *Project) bool {
	return p.referenceOnly() || len(p.SourceURLs) > 0 || p.Pinned != "" || p.Locked || len(p.LockedFields) > 0
}

func (e *Engine) mergeProjects(keepSlug, dropSlug, why string, manual bool) error {
	if keepSlug == dropSlug {
		return errors.New("a project cannot be merged into itself")
	}
	keep, ok := e.Store.Project(keepSlug)
	if !ok || !keep.Live() {
		return fmt.Errorf("%s: %w", keepSlug, ErrNotFound)
	}
	drop, ok := e.Store.Project(dropSlug)
	if !ok || !drop.Live() {
		return fmt.Errorf("%s: %w", dropSlug, ErrNotFound)
	}
	if keep.referenceOnly() || drop.referenceOnly() {
		return fmt.Errorf("%w: cannot merge projects", ErrReferenceOnly)
	}
	if !manual {
		if protectedProject(keep) || protectedProject(drop) {
			return errors.New("automatic merge would change a protected project")
		}
		for _, p := range e.Store.Projects() {
			if p.Live() && (p.Parent == keep.Slug || p.Parent == drop.Slug) {
				return errors.New("automatic merge would change the user's project tree")
			}
		}
		if e.shareProjectSource(keep, drop) {
			return errors.New("automatic merge would fold separate views of a shared source")
		}
		for _, id := range drop.Chats {
			if c, ok := e.Store.Chat(id); ok && taggingFieldLocked(&c.Tagging, "projects") {
				return errors.New("automatic merge would change protected chat relationships")
			}
		}
		for _, id := range drop.People {
			if p, ok := e.Store.Person(id); ok && taggingFieldLocked(&p.Tagging, "projects") {
				return errors.New("automatic merge would change protected person relationships")
			}
		}
		for _, ref := range drop.Items {
			kind, id, ok := strings.Cut(ref, "/")
			if !ok {
				continue
			}
			if it, ok := e.Store.Item(kind, id); ok && taggingFieldLocked(&it.Tagging, "projects") {
				return errors.New("automatic merge would change protected item relationships")
			}
		}
	}
	// What keep gains is recorded on drop (ProjectMerge) so an unmerge takes back just that: the lists only grow
	// at their ends here.
	rec := &ProjectMerge{At: time.Now().UTC(), Status: drop.Status}
	nAliases, nPeople, nChats, nItems, nDirs, nKeywords := len(keep.Aliases), len(keep.People), len(keep.Chats), len(keep.Items), len(keep.Dirs), len(keep.Keywords)
	keptMoments := keep.Moments
	// drop's name always becomes an alias (its mentions should find keep) unless a third project goes by it; its
	// aliases join within the bound and without names of other projects.
	taken := e.takenProjectNames(keep.Slug, drop.Slug)
	if a := strings.TrimSpace(drop.Name); a != "" && normName(a) != normName(keep.Name) && !taken[normName(a)] && !hasAlias(keep, normName(a)) {
		keep.Aliases = append(keep.Aliases, a)
	}
	addProjectAliases(keep, drop.Aliases, taken)
	// Not what the user took off keep; and what they took off drop stays off keep, unless keep has it (rec.Excluded).
	keep.People = addUnique(keep.People, taggingKept(&keep.Tagging, "people", drop.People)...)
	keep.Chats = addUnique(keep.Chats, taggingKept(&keep.Tagging, "chats", drop.Chats)...)
	keep.Items = addUnique(keep.Items, taggingKept(&keep.Tagging, "items", drop.Items)...)
	for _, list := range []string{"people", "chats", "items"} {
		for _, id := range drop.Excluded[list] {
			if !slices.Contains(*keep.list(list), id) && !taggingExcludes(&keep.Tagging, list, id) {
				rec.exclude(list, id)
			}
		}
	}
	keep.Dirs = addUnique(keep.Dirs, drop.Dirs...)
	keep.Keywords = addUnique(keep.Keywords, drop.Keywords...)
	keep.Moments = mergeMoments(keep.Moments, drop.Moments)
	rec.Aliases = slices.Clone(keep.Aliases[nAliases:])
	rec.People, rec.Chats, rec.Items = slices.Clone(keep.People[nPeople:]), slices.Clone(keep.Chats[nChats:]), slices.Clone(keep.Items[nItems:])
	rec.Dirs, rec.Keywords = slices.Clone(keep.Dirs[nDirs:]), slices.Clone(keep.Keywords[nKeywords:])
	had, has := momentIDs(keptMoments), momentIDs(keep.Moments)
	rec.Moments = removeStrings(has, had)
	rec.Evicted = slices.DeleteFunc(slices.Clone(keptMoments), func(m ProjectMoment) bool { return slices.Contains(has, m.ID) })
	if drop.Kind == "long" {
		keep.Kind = "long"
	}
	if keep.Goal == "" {
		keep.Goal = drop.Goal
	}
	if keep.MyRole == "" {
		keep.MyRole = drop.MyRole
	}
	if !keep.Locked {
		if rank(drop.Importance) < rank(keep.Importance) {
			keep.Importance = drop.Importance
		}
		keep.Relevance = max(keep.Relevance, drop.Relevance)
		for _, t := range drop.Tags {
			if len(keep.Tags) < 6 {
				keep.Tags = addUnique(keep.Tags, t)
			}
		}
	}
	if keep.Status == "done" && drop.Status == "active" {
		keep.Status = "active"
	}
	if drop.Created.Before(keep.Created) {
		keep.Created = drop.Created
	}
	var mergedSummary string
	if manual {
		// The target keeps its identity and scalar priorities. The merged source remains readable with its original
		// protected metadata, while both sets of confirmed facts remain visible on the target.
		if joined := joinPinned(keep.Pinned, drop.Pinned); joined != keep.Pinned {
			was := keep.Pinned
			rec.Pinned, keep.Pinned = &was, joined
		}
		was := MergeLocks{Fields: slices.Clone(keep.LockedFields), Locked: keep.Locked}
		if locks := joinLocks(was, drop); !locks.same(was) {
			rec.Locks = &was
			keep.LockedFields, keep.Locked = locks.Fields, locks.Locked
		}
		if e.Store.Summary(KindProject, keep.Slug) == "" {
			mergedSummary = e.Store.Summary(KindProject, drop.Slug)
		}
	}
	if err := e.mergeLogs(keep.Slug, drop.Slug); err != nil {
		return err
	}
	_ = e.Store.AppendLog(keep.Slug, time.Now(), "Persona", fmt.Sprintf("合并了项目「%s」（%s）", drop.Name, why))
	keep.Dirty = true
	keep.Updated = time.Now().UTC()
	if err := e.Store.putEntity(KindProject, keep.Slug, keep, &keep.Tagging, manual); err != nil {
		return err
	}
	if len(rec.Excluded) > 0 { // tagging only: an automatic merge's write keeps keep's own exclusions as they were
		err := e.Store.editTagging(KindProject, keep.Slug, func(t *Tagging) bool {
			for list, ids := range rec.Excluded {
				taggingExclude(t, list, ids...)
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	if mergedSummary != "" {
		if err := e.Store.putSummary(KindProject, keep.Slug, mergedSummary, true); err != nil {
			return err
		}
	}
	// Who named drop names keep.
	swap := func(ref string, list []string) ([]string, bool) {
		to := keep.Slug
		if list, member := refList(ref); taggingExcludes(&keep.Tagging, list, member) {
			to = "" // the user took it off keep: it stops naming drop and does not name keep
		}
		changed := false
		var out []string
		for _, s := range list {
			if s == drop.Slug {
				s, changed = to, true
			}
			out = addUnique(out, s)
		}
		if changed && to != "" && !slices.Contains(list, keep.Slug) {
			rec.Moved = append(rec.Moved, ref)
		}
		return out, changed
	}
	if err := e.relinkRefs(drop, manual, swap); err != nil {
		return err
	}
	if err := e.excludeBack(keep.Slug, rec.Excluded, true); err != nil {
		return err
	}
	// The projects under drop go under keep (beside keep, when keep is one of them or below one).
	moves, err := e.moveChildren(drop.Slug, keep.Slug, drop.Parent)
	rec.Children = moves
	if err != nil {
		return err
	}
	drop.Status, drop.MergedInto, drop.Merge, drop.Dirty = "merged", keep.Slug, rec, false
	drop.Updated = time.Now().UTC()
	if err := e.Store.putEntity(KindProject, drop.Slug, drop, &drop.Tagging, manual); err != nil {
		return err
	}
	e.index(KindProject, keep.Slug, keep.Name, keep.Tagging, keep.OneLine, keep.Updated)
	return nil
}

// projectRefs are p's chats, people and items as "<kind>/<id>".
func projectRefs(p *Project) []string {
	var refs []string
	for _, id := range p.Chats {
		refs = append(refs, KindChat+"/"+id)
	}
	for _, id := range p.People {
		refs = append(refs, KindPerson+"/"+id)
	}
	return append(refs, p.Items...)
}

// refList is the list of a project's that names ref ("<kind>/<id>"), and the id it names it by there.
func refList(ref string) (list, member string) {
	kind, id, _ := strings.Cut(ref, "/")
	switch kind {
	case KindChat:
		return "chats", id
	case KindPerson:
		return "people", id
	}
	return "items", ref
}

// excludeBack records (on) or lifts the reverse side of exclusions a merge gave the project slug: each chat, person
// and item in links stops (or may again) name it (exclude.go). Tagging only, so an automatic merge records it too.
func (e *Engine) excludeBack(slug string, links map[string][]string, on bool) error {
	for _, list := range []string{"people", "chats", "items"} {
		for _, member := range links[list] {
			kind, id := list, member
			if list == "items" {
				kind, id, _ = strings.Cut(member, "/")
			}
			err := e.Store.editTagging(kind, id, func(t *Tagging) bool {
				had := taggingExcludes(t, "projects", slug)
				if on {
					taggingExclude(t, "projects", slug)
				} else {
					taggingInclude(t, "projects", slug)
				}
				return had != on
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// relinkRefs gives each of p's chats, people and items the projects f makes of its own, and writes the ones f changes.
func (e *Engine) relinkRefs(p *Project, manual bool, f func(ref string, projects []string) ([]string, bool)) error {
	for _, ref := range projectRefs(p) {
		kind, id, _ := strings.Cut(ref, "/")
		var v any
		var t *Tagging
		var list *[]string
		switch kind {
		case KindChat:
			// Under chatMu (editChatAs): an ingest's pending count and last message time written meanwhile stay.
			if _, _, err := e.Store.editChatAs(id, manual, func(c *Chat) {
				if l, changed := f(ref, c.Projects); changed {
					c.Projects = l
				}
			}); err != nil {
				return err
			}
			continue
		case KindRecording:
			// Under recMu, like every other read-compare-write of a recording: an upload or a trim meanwhile stays.
			e.Store.recMu.Lock()
			var err error
			if r, ok := e.Store.Recording(id); ok {
				if l, changed := f(ref, r.Projects); changed {
					r.Projects = l
					err = e.Store.putEntity(KindRecording, r.ID, r, &r.Tagging, manual)
				}
			}
			e.Store.recMu.Unlock()
			if err != nil {
				return err
			}
			continue
		case KindPerson:
			// Read again under personMu: chats or handles the pipeline links meanwhile stay.
			if _, _, err := e.Store.updatePerson(id, manual, nil, func(per *Person) bool {
				l, changed := f(ref, per.Projects)
				if changed {
					per.Projects = l
				}
				return changed
			}); err != nil {
				return err
			}
			continue
		default:
			it, ok := e.Store.Item(kind, id)
			if !ok {
				continue
			}
			kind, id, v, t, list = it.Kind, it.ID, it, &it.Tagging, &it.Projects
		}
		if l, changed := f(ref, *list); changed {
			*list = l
			if err := e.Store.putEntity(kind, id, v, t, manual); err != nil {
				return err
			}
		}
	}
	return nil
}

// joinLocks is keep's locks with drop's added (a manual merge keeps both).
func joinLocks(keep MergeLocks, drop *Project) MergeLocks {
	return MergeLocks{Fields: addUnique(slices.Clone(keep.Fields), drop.LockedFields...), Locked: keep.Locked || drop.Locked}
}

// same: l and o lock the same, the fields in any order (an update sorts a project's locked fields).
func (l MergeLocks) same(o MergeLocks) bool {
	return l.Locked == o.Locked && slices.Equal(slices.Sorted(slices.Values(l.Fields)), slices.Sorted(slices.Values(o.Fields)))
}

// momentIDs are the ids of posts, in their order.
func momentIDs(posts []ProjectMoment) []string {
	ids := make([]string, 0, len(posts))
	for _, m := range posts {
		ids = append(ids, m.ID)
	}
	return ids
}

// joinPinned is keep's pinned facts with drop's added (a manual merge keeps both).
func joinPinned(keep, drop string) string {
	switch {
	case keep == "":
		return drop
	case drop != "" && drop != keep:
		return keep + "\n\n" + drop
	}
	return keep
}

// rank orders importance: P0 is 0, unset 4.
func rank(p string) int {
	if importanceRe.MatchString(p) {
		return int(p[1] - '0')
	}
	return 4
}

// mergeLogs rewrites keep's timeline with drop's lines in it, by time (the lines start with "- 2006-01-02 15:04").
func (e *Engine) mergeLogs(keepSlug, dropSlug string) error {
	lines := e.Store.Log(keepSlug, math.MaxInt) // both in the archive's zone
	other := e.Store.Log(dropSlug, math.MaxInt)
	if len(other) == 0 {
		return nil
	}
	seen := map[string]bool{}
	for _, l := range lines {
		seen[l] = true
	}
	for _, l := range other {
		if !seen[l] {
			lines = append(lines, l)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool { return logTime(lines[i]) < logTime(lines[j]) })
	return e.Store.WriteLog(keepSlug, lines)
}

func logTime(l string) string {
	if r := []rune(l); len(r) >= 18 {
		return string(r[2:18])
	}
	return ""
}

func mergeMoments(a, b []ProjectMoment) []ProjectMoment {
	seen := map[string]bool{}
	var out []ProjectMoment
	for _, m := range append(append([]ProjectMoment{}, a...), b...) {
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out[:min(len(out), maxProjectMoments)]
}
