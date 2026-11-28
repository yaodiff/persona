package kb

import (
	"slices"
	"strings"
)

// Links the user removed by hand (docs/assistant/knowledge.md "Removed links", #10333). A removal the user makes (Apply)
// records the link on both entries' Excluded, under the list it left and the id that list named it by. Automatic writes
// (putEntity without manual, which the pipeline, connectors and merges use) leave an excluded id out of that list while
// the rest of the list keeps changing, and an extension's update cannot make the link again (ApplyExtension). Linking it
// again by hand, or allowing it again (UserUpdate.Allow*), lifts it. A whole-list lock (locked_fields) is a separate,
// explicit choice (UserUpdate.LockLists) and keeps its meaning, the locks written before exclusions included.

// taggingExclude records ids as links the user removed from list.
func taggingExclude(t *Tagging, list string, ids ...string) {
	for _, id := range ids {
		if id == "" || slices.Contains(t.Excluded[list], id) {
			continue
		}
		if t.Excluded == nil {
			t.Excluded = map[string][]string{}
		}
		t.Excluded[list] = append(t.Excluded[list], id)
	}
}

// taggingInclude lifts the exclusion of ids from list.
func taggingInclude(t *Tagging, list string, ids ...string) {
	if !slices.ContainsFunc(ids, func(id string) bool { return slices.Contains(t.Excluded[list], id) }) {
		return
	}
	t.Excluded[list] = removeStrings(t.Excluded[list], ids)
	if len(t.Excluded[list]) == 0 {
		delete(t.Excluded, list)
	}
	if len(t.Excluded) == 0 {
		t.Excluded = nil
	}
}

// taggingExcludes: the user removed id from list.
func taggingExcludes(t *Tagging, list, id string) bool { return slices.Contains(t.Excluded[list], id) }

// taggingKept is ids without those the user removed from list.
func taggingKept(t *Tagging, list string, ids []string) []string {
	if len(t.Excluded[list]) == 0 {
		return ids
	}
	return removeStrings(ids, t.Excluded[list])
}

// linkEdit is how an update changes links: as the user, whose removals are kept out of automatic writes and whose links
// made by hand lift that, or as an extension, which changes links as before exclusions.
type linkEdit struct {
	user bool
	// lockLists: a list the update only removes from is locked whole, on both entries (locked_fields).
	lockLists bool
	// allow: the ids each list may have again ("people", "chats", "projects"), nothing linked by it.
	allow map[string][]string
}

// userEdit is how an update the user sends changes links: lock_lists says whether a removal locks the lists, as lock
// does when it is unset (what a removal did before exclusions, for clients that do not send it).
func userEdit(u UserUpdate) linkEdit {
	lock := u.Lock == nil || *u.Lock
	if u.LockLists != nil {
		lock = *u.LockLists
	}
	allow := map[string][]string{}
	for list, ids := range map[string][]string{"people": u.AllowPeople, "chats": u.AllowChats, "projects": u.AllowProjects} {
		if len(ids) > 0 {
			allow[list] = ids
		}
	}
	return linkEdit{user: true, lockLists: lock, allow: allow}
}

// extensionEdit is how an extension's update changes links: a removal locks as lock says, and nothing is excluded.
func extensionEdit(u Update) linkEdit { return linkEdit{lockLists: u.Lock == nil || *u.Lock} }

// locks: whether an update locks field (protectUpdate): a link list it only removes from as lockLists says, the rest as
// lock says.
func (le linkEdit) locks(u Update, field string) bool {
	removing := map[string]bool{
		"people":   len(u.RemovePeople) > 0 && len(u.People) == 0,
		"chats":    len(u.RemoveChats) > 0 && len(u.Chats) == 0,
		"projects": len(u.RemoveProjects) > 0 && len(u.Projects) == 0,
	}
	if removing[field] {
		return le.lockLists
	}
	return u.Lock == nil || *u.Lock
}

// removed records, for the user, that target's list no longer has ids (the user's removal); an extension's removal
// records nothing.
func (le linkEdit) removed(t *Tagging, list string, ids ...string) {
	if le.user {
		taggingExclude(t, list, ids...)
	}
}

// linked lifts, for the user, the exclusion of ids in target's list (the user linked them by hand).
func (le linkEdit) linked(t *Tagging, list string, ids ...string) {
	if le.user {
		taggingInclude(t, list, ids...)
	}
}

// allowed lifts, on the entry an update names, the exclusions the user allowed again in lists.
func (le linkEdit) allowed(t *Tagging, lists ...string) {
	for _, list := range lists {
		taggingInclude(t, list, le.allow[list]...)
	}
}

// existing are the ids of kind that name an entry (projects: by their slugs), so a removal of a name that is not an id
// records nothing.
func (e *Engine) existing(kind string, ids []string) []string {
	var out []string
	for _, id := range ids {
		switch kind {
		case KindProject:
			if p, ok := e.Store.Project(id); ok {
				out = append(out, p.Slug)
			}
		case KindChat:
			if _, ok := e.Store.Chat(id); ok {
				out = append(out, id)
			}
		case KindPerson:
			if _, ok := e.Store.Person(id); ok {
				out = append(out, id)
			}
		}
	}
	return out
}

// allowFields are the allow_* fields each kind takes: its own link lists.
var allowFields = map[string][]string{
	KindProject:   {"allow_chats", "allow_people"},
	KindChat:      {"allow_people", "allow_projects"},
	KindPerson:    {"allow_chats", "allow_projects"},
	KindDoc:       {"allow_projects"},
	KindMeeting:   {"allow_projects"},
	KindEvent:     {"allow_projects"},
	KindRecording: {"allow_projects"},
}

// UserUnsupported is Unsupported with the fields only the user sends: an allow_* list the kind has no such list for.
func UserUnsupported(kind string, u UserUpdate) []IgnoredField {
	out := Unsupported(kind, u.Update)
	if _, known := kindFields[kind]; !known {
		return out
	}
	if kind != KindProject {
		for _, f := range []struct {
			name string
			set  bool
		}{{"project_mode", u.ProjectMode != nil}, {"source_urls", u.SourceURLs != nil}, {"parent", u.Parent != nil},
			{"milestones", u.Milestones != nil}, {"milestone", u.Milestone != nil}} {
			if f.set {
				out = append(out, IgnoredField{Field: f.name, Reason: kind + " entries do not use " + f.name})
			}
		}
	}
	own := allowFields[kind]
	for name, ids := range map[string][]string{"allow_people": u.AllowPeople, "allow_chats": u.AllowChats, "allow_projects": u.AllowProjects} {
		if len(ids) > 0 && !slices.Contains(own, name) {
			out = append(out, IgnoredField{Field: name, Reason: kind + " entries do not use " + name})
		}
	}
	slices.SortFunc(out, func(a, b IgnoredField) int { return strings.Compare(a.Field, b.Field) })
	return out
}

// stripAllow is u without the allow_* lists kind does not take.
func stripAllow(kind string, u UserUpdate) UserUpdate {
	own := allowFields[kind]
	if !slices.Contains(own, "allow_people") {
		u.AllowPeople = nil
	}
	if !slices.Contains(own, "allow_chats") {
		u.AllowChats = nil
	}
	if !slices.Contains(own, "allow_projects") {
		u.AllowProjects = nil
	}
	return u
}

// withoutRemoved takes out of an extension's update the links the user removed (either entry excludes the other), so
// it cannot make them again, and says which (Ignored).
func (e *Engine) withoutRemoved(kind, id string, u Update) (Update, []IgnoredField) {
	var ignored []IgnoredField
	drop := func(field string, ids []string, removed func(other string) bool) []string {
		var out []string
		for _, other := range ids {
			if removed(other) {
				ignored = append(ignored, IgnoredField{Field: field, Reason: "the user removed the link to " + other + ": it stays out until they link it again"})
				continue
			}
			out = append(out, other)
		}
		return out
	}
	self, _ := e.Store.tagging(kind, id)
	if self == nil {
		self = &Tagging{}
	}
	switch kind {
	case KindProject:
		u.People = drop("people", u.People, func(pid string) bool {
			t, _ := e.Store.tagging(KindPerson, pid)
			return taggingExcludes(self, "people", pid) || t != nil && taggingExcludes(t, "projects", id)
		})
		u.Chats = drop("chats", u.Chats, func(cid string) bool {
			t, _ := e.Store.tagging(KindChat, cid)
			return taggingExcludes(self, "chats", cid) || t != nil && taggingExcludes(t, "projects", id)
		})
	case KindChat, KindPerson, KindDoc, KindMeeting, KindEvent, KindRecording:
		list, member := kind, id // a chat in a project's chats, a person in its people
		if kind != KindChat && kind != KindPerson {
			list, member = "items", kind+"/"+SafeID(id)
		}
		u.Projects = drop("projects", u.Projects, func(slug string) bool {
			p, ok := e.Store.Project(slug)
			if !ok {
				return false
			}
			return taggingExcludes(self, "projects", p.Slug) || taggingExcludes(&p.Tagging, list, member)
		})
	}
	return u, ignored
}
