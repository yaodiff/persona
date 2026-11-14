package kb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A deleted project is soft-deleted (#4412): its directory moves to the data dir's .trash/<id>/project (and the projects
// merged into it to .trash/<id>/merged/<slug>), and the chats, people and items that named it stop naming it.
// trash.json next to them records which ones, so a restore puts the projects and exactly those links back.

// ErrConflict: a restore whose slug a newer project took meanwhile.
var ErrConflict = errors.New("conflict")

const trashDirName = ".trash"

// TrashEntry is one deleted project, as kb-trash lists it.
type TrashEntry struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	DeletedAt time.Time `json:"deleted_at"`
	// Unlinked are the "<kind>/<id>" entities that named the project when it was deleted.
	Unlinked []string `json:"unlinked,omitempty"`
	// Merged are the projects merged into it, which went with it.
	Merged []string `json:"merged,omitempty"`
}

func (s *Store) trashPath(id string, parts ...string) string {
	return filepath.Join(append([]string{s.Dir, trashDirName, SafeID(id)}, parts...)...)
}

// projectLive: a project's meta.json is there (a directory holding only a timeline a racing append made is not one).
func (s *Store) projectLive(slug string) bool {
	_, err := os.Stat(s.path(KindProject, slug, "meta.json"))
	return err == nil
}

// DeleteProject moves project slug to the trash and unlinks it. Merged and unknown projects are ErrNotFound.
func (e *Engine) DeleteProject(slug string, now time.Time) (TrashEntry, error) {
	if e.Store.ReadOnly() {
		return TrashEntry{}, ErrReadOnly
	}
	st := e.Store
	defer st.SearchIndexPurge() // a deleted project's text leaves the search index at its next update
	st.applyMu.Lock()
	defer st.applyMu.Unlock()
	p, ok := st.Project(slug)
	if !ok || !p.Live() {
		return TrashEntry{}, fmt.Errorf("%s: %w", slug, ErrNotFound)
	}
	entry := TrashEntry{Kind: KindProject, Slug: p.Slug, Name: p.Name, DeletedAt: now.UTC()}
	for _, other := range st.Projects() {
		if other.MergedInto == p.Slug {
			entry.Merged = append(entry.Merged, other.Slug)
		}
	}
	if err := os.MkdirAll(filepath.Join(st.Dir, trashDirName), 0o755); err != nil {
		return TrashEntry{}, err
	}
	// The id never carries the slug, so a long one can't be cut short by SafeID; Mkdir makes it unique.
	for n := now.UnixNano(); ; n++ {
		entry.ID = strconv.FormatInt(n, 10)
		err := os.Mkdir(st.trashPath(entry.ID), 0o755)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return TrashEntry{}, err
		}
	}
	// Under metaMu, which a pipeline write holds while it checks that the project is still there: once moved, none
	// can write it back.
	st.metaMu.Lock()
	var moved [][2]string // from, to: put back if a later move fails, so nothing is left half in the trash
	move := func(from, to string) error {
		err := os.Rename(from, to)
		if err == nil {
			moved = append(moved, [2]string{from, to})
		}
		return err
	}
	err := move(st.path(KindProject, p.Slug), st.trashPath(entry.ID, "project"))
	if err == nil && len(entry.Merged) > 0 {
		err = os.Mkdir(st.trashPath(entry.ID, "merged"), 0o755)
		for _, m := range entry.Merged {
			if err == nil {
				err = move(st.path(KindProject, m), st.trashPath(entry.ID, "merged", SafeID(m)))
			}
		}
	}
	if err != nil {
		for i := len(moved) - 1; i >= 0; i-- {
			_ = os.Rename(moved[i][1], moved[i][0])
		}
		_ = os.RemoveAll(st.trashPath(entry.ID))
	}
	st.metaMu.Unlock()
	if err != nil {
		e.logf("kb: deleting project %s: %v", p.Slug, err)
		return TrashEntry{}, err
	}
	err = e.relinkRefs(p, true, func(ref string, list []string) ([]string, bool) {
		if !slices.Contains(list, p.Slug) {
			return list, false
		}
		entry.Unlinked = append(entry.Unlinked, ref)
		return removeStrings(list, []string{p.Slug}), true
	})
	if werr := writeJSON(st.trashPath(entry.ID, "trash.json"), entry); err == nil {
		err = werr
	}
	if err != nil {
		return TrashEntry{}, err
	}
	e.unindex(KindProject, append([]string{p.Slug}, entry.Merged...))
	st.notify(KindProject, p.Slug, "metadata")
	return entry, nil
}

// Trash lists deleted projects, newest first.
func (e *Engine) Trash() []TrashEntry {
	dirs, _ := os.ReadDir(filepath.Join(e.Store.Dir, trashDirName))
	out := []TrashEntry{}
	for _, d := range dirs {
		var t TrashEntry
		if ok, err := readJSON(e.Store.trashPath(d.Name(), "trash.json"), &t); ok && err == nil && t.ID == d.Name() {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DeletedAt.After(out[j].DeletedAt) })
	return out
}

// trashedProjects are the projects in each trash entry (the deleted one and those merged into it), by entry id.
func (s *Store) trashedProjects() map[string][]*Project {
	out := map[string][]*Project{}
	dirs, _ := os.ReadDir(filepath.Join(s.Dir, trashDirName))
	for _, d := range dirs {
		paths := []string{s.trashPath(d.Name(), "project", "meta.json")}
		merged, _ := os.ReadDir(s.trashPath(d.Name(), "merged"))
		for _, m := range merged {
			paths = append(paths, s.trashPath(d.Name(), "merged", m.Name(), "meta.json"))
		}
		for _, path := range paths {
			var p Project
			if ok, err := readJSON(path, &p); ok && err == nil {
				out[d.Name()] = append(out[d.Name()], &p)
			}
		}
	}
	return out
}

// RestoreProject puts a deleted project, those merged into it and its links back. ErrNotFound: no such trash entry;
// ErrConflict: a project with one of their slugs exists again.
func (e *Engine) RestoreProject(id string) (TrashEntry, error) {
	if e.Store.ReadOnly() {
		return TrashEntry{}, ErrReadOnly
	}
	st := e.Store
	st.applyMu.Lock()
	defer st.applyMu.Unlock()
	var entry TrashEntry
	if ok, err := readJSON(st.trashPath(id, "trash.json"), &entry); err != nil {
		return TrashEntry{}, err
	} else if !ok || entry.ID != id || entry.Kind != KindProject {
		return TrashEntry{}, fmt.Errorf("%s: %w", id, ErrNotFound)
	}
	st.metaMu.Lock()
	for _, slug := range append([]string{entry.Slug}, entry.Merged...) {
		if st.projectLive(slug) {
			st.metaMu.Unlock()
			return TrashEntry{}, fmt.Errorf("%s: %w", slug, ErrConflict)
		}
	}
	// A timeline line appended while the project was being deleted can leave projects/<slug>/log.md alone; it goes
	// back onto the restored timeline. mu (after metaMu) holds AppendLog off until the timeline is back in place.
	st.mu.Lock()
	stray, _ := os.ReadFile(st.path(KindProject, entry.Slug, "log.md"))
	err := os.MkdirAll(filepath.Join(st.Dir, KindProject), 0o755)
	if err == nil {
		err = os.RemoveAll(st.path(KindProject, entry.Slug))
	}
	if err == nil {
		err = os.Rename(st.trashPath(id, "project"), st.path(KindProject, entry.Slug))
	}
	for _, m := range entry.Merged {
		if err == nil {
			_ = os.RemoveAll(st.path(KindProject, m))
			err = os.Rename(st.trashPath(id, "merged", SafeID(m)), st.path(KindProject, m))
		}
	}
	if err == nil && len(stray) > 0 {
		err = appendLogFile(st.path(KindProject, entry.Slug, "log.md"), stray)
	}
	st.mu.Unlock()
	st.metaMu.Unlock()
	if err != nil {
		e.logf("kb: restoring project %s: %v", entry.Slug, err)
		return TrashEntry{}, err
	}
	p, ok := st.Project(entry.Slug)
	if !ok {
		return TrashEntry{}, fmt.Errorf("%s: %w", entry.Slug, ErrNotFound)
	}
	// Only the recorded links come back, onto the entities that still exist.
	err = e.relinkRefs(p, true, func(ref string, list []string) ([]string, bool) {
		if !slices.Contains(entry.Unlinked, ref) || slices.Contains(list, p.Slug) {
			return list, false
		}
		return append(list, p.Slug), true
	})
	if err != nil {
		return TrashEntry{}, err
	}
	if err := os.RemoveAll(st.trashPath(id)); err != nil {
		return TrashEntry{}, err
	}
	e.index(KindProject, p.Slug, p.Name, p.Tagging, p.OneLine, p.Updated)
	st.notify(KindProject, p.Slug, "metadata")
	return entry, nil
}

func appendLogFile(path string, raw []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// forgetTrash removes for good the trash entries that hold anything a forgotten source brought in (its chats, people,
// items or posts, or timeline lines it wrote): a restore must not bring them back.
func (e *Engine) forgetTrash(chats, people, posts []string, items map[string]bool, names map[string]bool) error {
	for id, projects := range e.Store.trashedProjects() {
		hit := false
		for _, p := range projects {
			hit = hit || slices.ContainsFunc(p.Chats, func(c string) bool { return slices.Contains(chats, c) }) ||
				slices.ContainsFunc(p.People, func(c string) bool { return slices.Contains(people, c) }) ||
				slices.ContainsFunc(p.Items, func(k string) bool { return items[k] }) ||
				slices.ContainsFunc(p.Moments, func(m ProjectMoment) bool { return slices.Contains(posts, m.ID) })
		}
		if !hit {
			raw, _ := os.ReadFile(e.Store.trashPath(id, "project", "log.md"))
			for _, l := range strings.Split(string(raw), "\n") {
				if rest, ok := strings.CutPrefix(l, "- "); ok {
					if parts := strings.SplitN(rest, " · ", 3); len(parts) == 3 && names[parts[1]] {
						hit = true
						break
					}
				}
			}
		}
		if hit {
			if err := os.RemoveAll(e.Store.trashPath(id)); err != nil {
				return err
			}
		}
	}
	return nil
}
