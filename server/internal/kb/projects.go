package kb

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

var slugRe = lazyre.New(`[^\p{L}\p{N}]+`)

// Slug is the directory name of a project name ("Persona 2.0 Launch" → "persona-2-0-launch"; letters of any script are kept).
func Slug(name string) string {
	s := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func normName(s string) string { return strings.ReplaceAll(Slug(s), "-", "") }

// FindProject matches a name against project names and aliases; a merged project's name leads to the one it went into.
// When several live projects match, a name (or slug) beats an alias, and among equals the most recently updated wins
// (then the smaller slug), so the answer does not depend on directory order.
//
// A name in the form known_projects.md shows a project in (「名字（别名、…）：一句话」, which models copy whole) that
// matches nothing is looked up again without its one line and trailing bracket groups (#5824).
func (e *Engine) FindProject(name string) *Project {
	return e.ProjectLookup().Find(name)
}

// ProjectLookup is the projects read once, for many lookups in a row: Store.Projects reads and parses every project's
// meta.json, and a caller resolving one name per to-do read them all once per to-do (#15539). It does not see writes
// made after it was taken.
type ProjectLookup struct {
	projects []lookupProject
	byID     map[string]*Project
}

type lookupProject struct {
	p       *Project
	name    string
	aliases []string
}

// ProjectLookup reads the projects now.
func (e *Engine) ProjectLookup() *ProjectLookup {
	l := &ProjectLookup{byID: map[string]*Project{}}
	for _, id := range e.Store.IDs(KindProject) {
		p, ok := e.Store.Project(id)
		if !ok {
			continue
		}
		l.byID[SafeID(id)] = p
		lp := lookupProject{p: p, name: normName(p.Name)}
		for _, a := range p.Aliases {
			lp.aliases = append(lp.aliases, normName(a))
		}
		l.projects = append(l.projects, lp)
	}
	return l
}

// Project is Store.Project as of when the lookup was taken.
func (l *ProjectLookup) Project(slug string) (*Project, bool) {
	p, ok := l.byID[SafeID(slug)]
	return p, ok
}

// Find is FindProject as of when the lookup was taken.
func (l *ProjectLookup) Find(name string) *Project {
	if p := l.find(name); p != nil {
		return p
	}
	for _, base := range displayBases(name) {
		if tooGenericAlias(normName(base)) { // 「无（某某群）」 is not the project named 无
			break
		}
		if p := l.find(base); p != nil {
			return p
		}
	}
	return nil
}

// trailingGroupRe is one bracket group at the end of a name: 「（…）」 or 「(...)」.
var trailingGroupRe = lazyre.New(`\s*[（(][^（()）]*[)）]\s*$`)

// displayBases are the shorter names a project name in the shown form may stand for, longest first: without the
// one line after 「：」, then without one trailing bracket group at a time. 2026-11-06 a knowledge base got projects
// named 「X（a、b）」 and 「X（a、b）（c、d…）」; dedupe then merged the real X into them.
func displayBases(name string) []string {
	var out []string
	s := strings.TrimSpace(name)
	if i := strings.Index(s, "："); i > 0 {
		s = strings.TrimSpace(s[:i])
		out = append(out, s)
	}
	for {
		t := strings.TrimSpace(trailingGroupRe.ReplaceAllString(s, ""))
		if t == s || t == "" {
			return out
		}
		s = t
		out = append(out, s)
	}
}

// shownName is the name a new project gets from a model's: without the one line and without trailing bracket groups
// that list aliases (「、」 inside, or more than one group). A single group without 「、」 can be part of a real name
// (「分身（内置版）」) and stays.
func shownName(name string) string {
	s := strings.TrimSpace(name)
	if i := strings.Index(s, "："); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	for {
		loc := trailingGroupRe.FindStringIndex(s)
		if loc == nil {
			return s
		}
		group, rest := s[loc[0]:], strings.TrimSpace(s[:loc[0]])
		if rest == "" || tooGenericAlias(normName(rest)) || (!strings.Contains(group, "、") && !trailingGroupRe.MatchString(rest)) {
			return s
		}
		s = rest
	}
}

// isShownFormOf says whether name is base in the shown form: base followed by bracket groups or a one line.
func isShownFormOf(name, base string) bool {
	n := normName(base)
	for _, b := range displayBases(name) {
		if normName(b) == n {
			return true
		}
	}
	return false
}

func (l *ProjectLookup) find(name string) *Project {
	n := normName(name)
	if n == "" {
		return nil
	}
	slug := Slug(name)
	var byName, byAlias, merged *Project
	for _, lp := range l.projects {
		p := lp.p
		exact := lp.name == n || p.Slug == slug
		if !exact && !slices.Contains(lp.aliases, n) {
			continue
		}
		switch {
		case !p.Live():
			merged = preferProject(merged, p)
		case exact:
			byName = preferProject(byName, p)
		default:
			byAlias = preferProject(byAlias, p)
		}
	}
	if byName != nil {
		return byName
	}
	if byAlias != nil {
		return byAlias
	}
	if merged != nil && merged.MergedInto != "" {
		if p, ok := l.Project(merged.MergedInto); ok && p.Live() {
			return p
		}
	}
	return nil
}

func hasAlias(p *Project, n string) bool {
	for _, a := range p.Aliases {
		if normName(a) == n {
			return true
		}
	}
	return false
}

// preferProject is the one of two matches to keep: the more recently updated, then the smaller slug.
func preferProject(cur, p *Project) *Project {
	if cur == nil || p.Updated.After(cur.Updated) || (p.Updated.Equal(cur.Updated) && p.Slug < cur.Slug) {
		return p
	}
	return cur
}

// maxProjectAliases bounds the aliases a project collects on its own (compression, dedupe): past it new ones are
// dropped. Aliases added by hand (kb_update) are not bounded. Before the bound a week of compression left active
// projects with 90-145 aliases each, among them the names of other active projects (#2546).
const maxProjectAliases = 12

// takenProjectNames are the normalized names, slugs and aliases of the live projects other than skip: an alias that
// is one of them belongs to another project and would make mentions of it land here.
func (e *Engine) takenProjectNames(skip ...string) map[string]bool {
	taken := map[string]bool{}
	for _, p := range e.Store.Projects() {
		if !p.Live() || slices.Contains(skip, p.Slug) {
			continue
		}
		for _, n := range append([]string{p.Name, p.Slug}, p.Aliases...) {
			if n = normName(n); n != "" {
				taken[n] = true
			}
		}
	}
	return taken
}

// tooGenericAlias: too short to tell projects apart (a one-character name, a two-letter abbreviation) or a word
// every project has.
func tooGenericAlias(n string) bool {
	r := []rune(n)
	if len(r) < 2 || genericWord[n] {
		return true
	}
	return len(r) <= 2 && len(n) == len(r) // ASCII only: "ss", "ai"
}

// addProjectAliases adds the aliases worth keeping to p: not p's own name, not too generic, not taken by another
// live project, and while p has fewer than maxProjectAliases.
func addProjectAliases(p *Project, aliases []string, taken map[string]bool) {
	own := normName(p.Name)
	for _, a := range aliases {
		a = strings.TrimSpace(a)
		n := normName(a)
		if n == "" || n == own || tooGenericAlias(n) || taken[n] || hasAlias(p, n) {
			continue
		}
		if len(p.Aliases) >= maxProjectAliases {
			return
		}
		p.Aliases = append(p.Aliases, a)
	}
}

// PruneProjectAliases takes off each live project the aliases the rules above would not let it collect, for the
// knowledge bases that collected them before the rules: another live project's name or slug (a name beats an alias
// in FindProject, so the alias never finds this project and only tells the models the two are one), its own name, a
// repeat, one too generic, and those past the first maxProjectAliases. The names of projects merged into it, directly
// or through another merged project, stay and do not count: their mentions should find it. An alias two live projects
// share stays on both (whose it is cannot be told), and so do aliases set by hand (locked). Returns how many went.
func (e *Engine) PruneProjectAliases() int {
	projects := e.Store.Projects()
	bySlug := map[string]*Project{}
	for _, p := range projects {
		bySlug[p.Slug] = p
	}
	owners := map[string][]string{}  // a live project's normalized name or slug → the projects going by it
	mergedIn := map[[2]string]bool{} // {live slug, normalized name of a project merged into it}
	for _, p := range projects {
		if p.Live() {
			for _, n := range []string{normName(p.Name), normName(p.Slug)} {
				if n != "" && !slices.Contains(owners[n], p.Slug) {
					owners[n] = append(owners[n], p.Slug)
				}
			}
			continue
		}
		into := bySlug[p.MergedInto]
		for range len(projects) { // a chain ends at a live project; the bound only guards a cycle
			if into == nil || into.Live() {
				break
			}
			into = bySlug[into.MergedInto]
		}
		if into != nil && into.Live() {
			mergedIn[[2]string{into.Slug, normName(p.Name)}] = true
		}
	}
	removed := 0
	for _, p := range projects {
		if !p.Live() || p.referenceOnly() || len(p.Aliases) == 0 || taggingFieldLocked(&p.Tagging, "aliases") {
			continue
		}
		own, seen, counted := normName(p.Name), map[string]bool{}, 0
		var kept []string
		for _, a := range p.Aliases {
			n := normName(a)
			other := slices.ContainsFunc(owners[n], func(s string) bool { return s != p.Slug })
			if n == "" || n == own || seen[n] || other {
				continue
			}
			seen[n] = true
			if !mergedIn[[2]string{p.Slug, n}] {
				if tooGenericAlias(n) || counted >= maxProjectAliases {
					continue
				}
				counted++
			}
			kept = append(kept, a)
		}
		if len(kept) == len(p.Aliases) {
			continue
		}
		n := len(p.Aliases) - len(kept)
		p.Aliases = kept
		// Not PutProject: tidying its aliases is not news, so Updated (recency in lists and in FindProject) stays.
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false); err != nil {
			e.logf("prune aliases of %s: %v", p.Slug, err)
			continue
		}
		removed += n
	}
	return removed
}

// knownProjects are the projects the models may name: not done or ignored.
func (e *Engine) knownProjects() []prompts.KnownProject {
	var out []prompts.KnownProject
	for _, p := range e.Store.Projects() {
		if p.Status == "done" || p.Status == "ignored" || !p.Live() {
			continue
		}
		// With its one line: names alone made the model start a second project for the same work under the name
		// of the group it was reading (2026-11-01, two work groups about the same project).
		known := prompts.KnownProject{Name: p.Name, Aliases: p.Aliases}
		if !p.referenceOnly() {
			known.OneLine, known.Pinned = p.OneLine, p.Pinned
		}
		out = append(out, known)
	}
	return out
}

// evolveProject merges a mention into its project (creating it), links the chat and its people when link says so, and
// logs the update.
func (e *Engine) evolveProject(name, kind string, aliases []string, myRole string, c *Chat, link bool, update string, now time.Time) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	p := e.FindProject(name)
	if p == nil {
		for _, a := range aliases {
			if p = e.FindProject(a); p != nil {
				break
			}
		}
	}
	if p != nil && p.Status == "ignored" {
		return ""
	}
	if p == nil {
		name = shownName(name) // not 「X（a、b）」 as a new project's name (#5824)
		slug := Slug(name)
		if slug == "" {
			return ""
		}
		p = &Project{Slug: slug, Name: name, Kind: "short", Status: "active", Created: now}
	}
	if p.referenceOnly() {
		if strings.TrimSpace(update) != "" || myRole != "" {
			e.logf("project %s: %v: skipped compression detail", p.Slug, ErrReferenceOnly)
		}
		if link && !slices.Contains(p.Chats, c.ID) {
			p.Chats = append(p.Chats, c.ID)
			if err := e.Store.PutProject(p); err != nil {
				e.logf("project %s: %v", p.Slug, err)
			}
		}
		return p.Slug
	}
	if kind == "short" || kind == "long" {
		if p.Kind == "" || p.Kind == "short" {
			p.Kind = kind // long wins: a mission does not become a feature
		}
	}
	addProjectAliases(p, aliases, e.takenProjectNames(p.Slug))
	if myRole != "" {
		p.MyRole = oneLine(myRole)
	}
	var people []string
	if link {
		p.Chats = addUnique(p.Chats, c.ID)
		people = e.externalPeople(c.People)
		p.People = addUnique(p.People, people...)
	}
	if strings.TrimSpace(update) != "" {
		if err := e.Store.AppendLog(p.Slug, now, c.Name, update); err != nil {
			e.logf("project %s: %v", p.Slug, err)
		} else {
			p.Dirty = true
			if p.Status == "done" {
				p.Status = "active"
			}
		}
	}
	_ = e.Store.PutProject(p)
	for _, pid := range people {
		_, _, _ = e.Store.updatePerson(pid, false, nil, func(per *Person) bool {
			if per.Status == "ignored" || per.Status == "self" {
				return false
			}
			n := len(per.Projects)
			per.Projects = addUnique(per.Projects, p.Slug)
			return len(per.Projects) != n
		})
	}
	return p.Slug
}

// heldOffProjects reports whether the user holds chat c to no project (Chat.HeldOffProjects, its projects still
// locked): compression then links it to none of the projects it mentions.
func heldOffProjects(c *Chat) bool {
	return c.HeldOffProjects && taggingFieldLocked(&c.Tagging, "projects")
}
