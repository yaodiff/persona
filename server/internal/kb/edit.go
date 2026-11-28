package kb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/nwverse/persona/server/internal/kbwire"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Change describes a persisted entity change, without exposing its contents.
// What is metadata, summary, messages, or log.
type Change struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	What string `json:"what"`
}

// ObserveChanges connects store writes, including connector and pipeline writes, to the engine's observer.
// Install observers before starting concurrent work. Callbacks run after the persistence lock is released.
func (e *Engine) ObserveChanges() {
	if e.Store == nil || e.Changed == nil {
		return
	}
	e.Store.observeMu.Lock()
	e.Store.changed = e.Changed
	e.Store.observeMu.Unlock()
}

func (s *Store) notify(kind, id, what string) {
	s.observeMu.RLock()
	f := s.changed
	s.observeMu.RUnlock()
	if f != nil {
		f(Change{Kind: kind, ID: id, What: what})
	}
}

func entityMap(v any) map[string]json.RawMessage {
	raw, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func (s *Store) entityPath(kind, id string) string {
	if kind == KindDoc || kind == KindMeeting || kind == KindEvent {
		return filepath.Join(s.Dir, kind, SafeID(id)+".json")
	}
	return s.path(kind, id, "meta.json")
}

func fieldKey(field string) string {
	if field == "project_kind" {
		return "kind"
	}
	return field
}

func taggingFieldLocked(t *Tagging, field string) bool { return kbwire.TaggingFieldLocked(t, field) }

// putEntity rereads protected fields while holding the metadata lock. A stale pipeline object cannot
// restore locked links or overwrite a hand edit made while a model or connector was running.
func (s *Store) putEntity(kind, id string, v any, t *Tagging, manual bool) (err error) {
	if s.readOnly {
		return ErrReadOnly
	}
	if kind == KindRecording {
		s.recFilesMu.Lock()
		if err := s.recoverRecordingLocked(id); err != nil {
			s.recFilesMu.Unlock()
			return err
		}
	}
	s.metaMu.Lock()
	if kind == KindChat {
		s.mu.Lock()
	}
	changed := false
	defer func() {
		if kind == KindChat {
			s.mu.Unlock()
		}
		s.metaMu.Unlock()
		if kind == KindRecording {
			s.recFilesMu.Unlock()
		}
		if err == nil && changed {
			s.notify(kind, id, "metadata")
		}
	}()
	if kind == KindChat {
		if err := s.pendingIngestLocked(id); err != nil {
			return err
		}
	}
	path := s.entityPath(kind, id)
	current := map[string]json.RawMessage{}
	exists, err := readJSON(path, &current)
	if err != nil {
		return err
	}
	if !exists && t.Persisted {
		return nil
	} // forgotten during an external call
	incoming := protectedEntity(v, t, current, manual)
	if kind == KindProject && exists {
		// Only a digest commit or a summary write can change the field records.
		// Other project writers may have read their copy while a digest was running.
		keys := []string{"field_coverage", "one_line_needs_summary_review", "one_line"}
		if !manual {
			keys = append(keys, "project_mode", "source_urls")
			if string(current["project_mode"]) == `"reference-only"` {
				keys = append(keys, "goal", "my_role", "note", "keywords", "status", "kind")
			}
		}
		for _, key := range keys {
			if value, ok := current[key]; ok {
				incoming[key] = value
			} else {
				delete(incoming, key)
			}
		}
	}
	before, _ := json.Marshal(current)
	after, _ := json.Marshal(incoming)
	changed = !exists || !bytes.Equal(before, after)
	if changed {
		err = writeJSON(path, incoming)
	}
	if err == nil {
		switch entity := v.(type) {
		case *Chat:
			*entity = Chat{}
		case *Person:
			*entity = Person{}
		case *Project:
			*entity = Project{}
		case *Item:
			*entity = Item{}
		case *Recording:
			*entity = Recording{}
		case *Topic:
			*entity = Topic{}
		}
		_ = json.Unmarshal(after, v)
		t.Persisted = true
	}
	return err
}

// editChat applies a hand edit to chat id under chatMu, like the pipeline's writers: it finishes an interrupted ingest,
// reads the chat again and lets edit change only the user's fields, so the messages, pending count, cursor and last
// message time an ingest, a history import or compression wrote meanwhile stay. It returns the chat as written (false:
// no such chat). edit must not take chatMu or ingestMu; write linked entities after editChat returns.
func (s *Store) editChat(id string, edit func(c *Chat)) (*Chat, bool, error) {
	return s.editChatAs(id, true, edit)
}

// editChatAs is editChat for a write that may not be the user's own (manual false keeps locked fields).
func (s *Store) editChatAs(id string, manual bool, edit func(c *Chat)) (*Chat, bool, error) {
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	if err := s.recoverIngest(id); err != nil {
		return nil, false, err
	}
	c, ok := s.Chat(id)
	if !ok {
		return nil, false, nil
	}
	edit(c)
	if err := s.putEntity(KindChat, c.ID, c, &c.Tagging, manual); err != nil {
		return nil, true, err
	}
	return c, true, nil
}

// protectedEntity merges fields held by a person or extension into a pipeline write.
func protectedEntity(v any, t *Tagging, current map[string]json.RawMessage, manual bool) map[string]json.RawMessage {
	incoming := entityMap(v)
	// Keep an explicit clear in metadata; omitting it makes detail clients fall back to cached tags.
	if t.Tags != nil && len(t.Tags) == 0 {
		incoming["tags"] = json.RawMessage(`[]`)
	}
	if len(current) > 0 {
		var protected Tagging
		raw, _ := json.Marshal(current)
		_ = json.Unmarshal(raw, &protected)
		if !manual {
			for _, field := range append([]string{"pinned", "locked_fields", "locked", "provenance", "excluded", "held_off_projects"}, protected.LockedFields...) {
				key := fieldKey(field)
				if value, ok := current[key]; ok {
					incoming[key] = value
				} else {
					delete(incoming, key)
				}
			}
			if protected.Locked {
				for _, key := range []string{"importance", "relevance", "tags"} {
					if value, ok := current[key]; ok {
						incoming[key] = value
					} else {
						delete(incoming, key)
					}
				}
			}
			// The links the user removed stay out; the rest of each list is the write's (exclude.go).
			for list, ids := range protected.Excluded {
				var have []string
				if raw, ok := incoming[list]; !ok || json.Unmarshal(raw, &have) != nil {
					continue
				}
				if kept := removeStrings(have, ids); len(kept) == 0 {
					delete(incoming, list)
				} else if len(kept) != len(have) {
					incoming[list], _ = json.Marshal(kept)
				}
			}
		}
	}
	return incoming
}

func (s *Store) putSummary(kind, id, text string, manual bool, writer ...string) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if kind == KindRecording {
		s.recFilesMu.Lock()
		if err := s.recoverRecordingLocked(id); err != nil {
			s.recFilesMu.Unlock()
			return err
		}
	}
	unlock := func() {
		s.metaMu.Unlock()
		if kind == KindRecording {
			s.recFilesMu.Unlock()
		}
	}
	s.metaMu.Lock()
	var t struct {
		Tagging
		Status string `json:"status"`
		Mode   string `json:"project_mode"`
	}
	exists, err := readJSON(s.entityPath(kind, id), &t)
	if err != nil {
		unlock()
		return err
	}
	if kind == KindProject && t.Mode == referenceOnly {
		unlock()
		return fmt.Errorf("%w: cannot save summary", ErrReferenceOnly)
	}
	if !manual && (!exists || taggingFieldLocked(&t.Tagging, "summary") || (kind == KindPerson && t.Status == "ignored")) {
		unlock()
		return nil
	}
	path := s.path(kind, id, "summary.md")
	next := []byte(strings.TrimSpace(text) + "\n")
	old, _ := os.ReadFile(path)
	changed := !bytes.Equal(old, next)
	if changed {
		err = writeFile(path, next)
	}
	metaChanged := false
	if err == nil && changed && kind == KindProject {
		if p, ok := s.Project(id); ok {
			if len(writer) > 0 {
				p.recordProjectField("summary", string(next), writer[0], "kb.update", nil)
			} else {
				delete(p.FieldCoverage, "summary")
			}
			if p.OneLine != "" && (len(writer) > 0 || len(old) > 0) {
				p.OneLineNeedsReview = true
				if len(writer) > 0 && !taggingFieldLocked(&p.Tagging, "one_line") {
					p.Dirty = true
				}
			}
			err = writeJSON(s.entityPath(kind, id), p)
			metaChanged = err == nil
		}
	}
	unlock()
	if err == nil && changed {
		s.notify(kind, id, "summary")
	}
	if metaChanged {
		s.notify(kind, id, "metadata")
	}
	return err
}

func removeStrings(list, remove []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(v string) bool { return slices.Contains(remove, v) })
}

func protectUpdate(u Update, t *Tagging, le linkEdit) {
	fields := []string{}
	for key, set := range map[string]bool{
		"name": u.Name != "", "project_kind": u.Kind != "", "status": u.Status != "", "goal": u.Goal != "", "my_role": u.MyRole != "", "note": u.Note != "", "summary": u.Summary != "",
		"importance": u.Importance != "", "relevance": u.Relevance != nil, "tags": u.Tags != nil, "dirs": len(u.Dirs) > 0,
		"aliases": len(u.Aliases)+len(u.RemoveAliases) > 0, "chats": len(u.Chats)+len(u.RemoveChats) > 0, "people": len(u.People)+len(u.RemovePeople) > 0, "projects": len(u.Projects)+len(u.RemoveProjects) > 0,
	} {
		if set {
			fields = append(fields, key)
		}
	}
	for _, field := range fields {
		if le.locks(u, field) {
			t.LockedFields = addUnique(t.LockedFields, field)
		}
	}
	slices.Sort(t.LockedFields)
	if u.Pinned != nil {
		t.Pinned = *u.Pinned
	}
}

// unlink removes the reverse side of explicit removals, and of the user's, keeps the links out of automatic writes
// there too (exclude.go); it never deletes an entity. It also lifts the reverse side of what the user allowed again.
func (e *Engine) unlink(kind, id string, u Update, le linkEdit) error {
	lock := func(t *Tagging, field string) {
		if le.lockLists {
			t.LockedFields = addUnique(t.LockedFields, field)
		}
	}
	allowed := func(list, other string) bool { return slices.Contains(le.allow[list], other) }
	person := func(pid string, edit func(p *Person)) error {
		p, _, err := e.Store.updatePerson(pid, true, nil, func(p *Person) bool {
			edit(p)
			return true
		})
		if err == nil && p != nil {
			e.WritePersonSummary(p.ID)
		}
		return err
	}
	chat := func(cid string, edit func(c *Chat)) error {
		_, _, err := e.Store.editChat(cid, edit)
		return err
	}
	if kind == KindProject {
		for _, pid := range u.RemovePeople {
			if err := person(pid, func(p *Person) {
				p.Projects = removeStrings(p.Projects, []string{id})
				lock(&p.Tagging, "projects")
				le.removed(&p.Tagging, "projects", id)
			}); err != nil {
				return err
			}
		}
		for _, cid := range u.RemoveChats {
			if err := chat(cid, func(c *Chat) {
				c.Projects = removeStrings(c.Projects, []string{id})
				lock(&c.Tagging, "projects")
				le.removed(&c.Tagging, "projects", id)
			}); err != nil {
				return err
			}
		}
		for _, pid := range le.allow["people"] {
			if err := person(pid, func(p *Person) { taggingInclude(&p.Tagging, "projects", id) }); err != nil {
				return err
			}
		}
		for _, cid := range le.allow["chats"] {
			if err := chat(cid, func(c *Chat) {
				taggingInclude(&c.Tagging, "projects", id)
				c.HeldOffProjects = false
			}); err != nil {
				return err
			}
		}
	}
	if kind == KindChat {
		for _, pid := range u.RemovePeople {
			if err := person(pid, func(p *Person) {
				p.Chats = removeStrings(p.Chats, []string{id})
				lock(&p.Tagging, "chats")
				le.removed(&p.Tagging, "chats", id)
			}); err != nil {
				return err
			}
		}
		for _, pid := range le.allow["people"] {
			if err := person(pid, func(p *Person) { taggingInclude(&p.Tagging, "chats", id) }); err != nil {
				return err
			}
		}
	}
	if kind == KindPerson {
		for _, cid := range u.RemoveChats {
			if err := chat(cid, func(c *Chat) {
				c.People = removeStrings(c.People, []string{id})
				lock(&c.Tagging, "people")
				le.removed(&c.Tagging, "people", id)
			}); err != nil {
				return err
			}
		}
		for _, cid := range le.allow["chats"] {
			if err := chat(cid, func(c *Chat) { taggingInclude(&c.Tagging, "people", id) }); err != nil {
				return err
			}
		}
	}
	for _, slug := range append(slices.Clone(u.RemoveProjects), le.allow["projects"]...) {
		p, ok := e.Store.Project(slug)
		if !ok {
			continue
		}
		var list, member string
		switch kind {
		case KindChat:
			list, member = "chats", id
		case KindPerson:
			list, member = "people", id
		case KindDoc, KindMeeting, KindEvent, KindRecording:
			list, member = "items", kind+"/"+SafeID(id)
		default:
			continue
		}
		if allowed("projects", slug) && !slices.Contains(u.RemoveProjects, slug) {
			taggingInclude(&p.Tagging, list, member)
		} else {
			*p.list(list) = removeStrings(*p.list(list), []string{member})
			lock(&p.Tagging, list)
			le.removed(&p.Tagging, list, member)
		}
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, true); err != nil {
			return err
		}
	}
	return nil
}

// list is the project's list of links by its name ("people", "chats", "items").
func (p *Project) list(name string) *[]string {
	switch name {
	case "people":
		return &p.People
	case "chats":
		return &p.Chats
	}
	return &p.Items
}

func (e *Engine) pinnedPeople(ids []string) []prompts.KBPinnedPerson {
	var out []prompts.KBPinnedPerson
	for _, id := range ids {
		if p, ok := e.Store.Person(id); ok && p.Pinned != "" {
			out = append(out, prompts.KBPinnedPerson{Name: p.Name, Pinned: p.Pinned})
		}
	}
	return out
}
