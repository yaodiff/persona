package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPersonStatusUpdate(t *testing.T) {
	for _, status := range []string{"ignored", "self"} {
		t.Run(status, func(t *testing.T) {
			e := editEngine(t)
			if err := e.Store.PutPerson(&Person{ID: "person", Name: "Contact"}); err != nil {
				t.Fatal(err)
			}
			stale, _ := e.Store.Person("person")
			editApply(t, e, KindPerson, "person", Update{Status: status})
			if err := e.Store.PutPerson(stale); err != nil {
				t.Fatal(err)
			}
			reopened, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			p, ok := reopened.Person("person")
			if !ok || p.Status != status || !taggingFieldLocked(&p.Tagging, "status") {
				t.Fatalf("status was not persisted and protected: %+v", p)
			}
			editApply(t, e, KindPerson, "person", Update{Note: "Confirmed note"})
			p, _ = e.Store.Person("person")
			if p.Status != status {
				t.Fatalf("omitted status changed it: %+v", p)
			}
			editApply(t, e, KindPerson, "person", Update{Status: "active"})
			p, _ = e.Store.Person("person")
			if p.Status != "active" {
				t.Fatalf("status was not restored: %+v", p)
			}
		})
	}
}

func TestPersonStatusList(t *testing.T) {
	e := editEngine(t)
	for _, status := range []string{"", "active", "ignored", "self"} {
		if err := e.Store.PutPerson(&Person{ID: "person-" + status, Status: status, Tagging: Tagging{Tags: []string{"work"}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tag := range []string{"", "work"} {
		list := e.List(KindPerson, tag)
		if len(list) != 3 {
			t.Fatalf("tag %q: listed %d people, want 3", tag, len(list))
		}
		for _, entry := range list {
			if entry.ID == "person-ignored" {
				t.Fatalf("tag %q: ignored person is listed", tag)
			}
		}
	}
	detail, err := e.Get(KindPerson, "person-ignored", 0)
	var read Person
	if err != nil || json.Unmarshal(detail.Meta, &read) != nil || read.Status != "ignored" {
		t.Fatalf("ignored person cannot be read explicitly: %+v, %v", detail, err)
	}
	editApply(t, e, KindPerson, "person-ignored", Update{Status: "active"})
	if list := e.List(KindPerson, ""); len(list) != 4 {
		t.Fatalf("restored person is not listed: %+v", list)
	}
}

func TestPersonStatusRejectsUnknownWithoutWriting(t *testing.T) {
	for _, status := range []string{"paused", "done", "merged", "unknown"} {
		for _, extension := range []string{"", "curator"} {
			t.Run(status+"/"+extension, func(t *testing.T) {
				e := editEngine(t)
				if err := e.Store.PutPerson(&Person{ID: "person"}); err != nil {
					t.Fatal(err)
				}
				if err := e.Store.PutProject(&Project{Slug: "project"}); err != nil {
					t.Fatal(err)
				}
				before, _ := e.Get(KindPerson, "person", 0)
				u := Update{Status: status, Note: "Must not be written", Projects: []string{"project"}}
				var err error
				if extension == "" {
					_, err = e.Apply(KindPerson, "person", u)
				} else {
					_, err = e.ApplyExtension(KindPerson, "person", u, ExtensionWrite{Extension: extension})
				}
				if err == nil {
					t.Fatal("unknown status was accepted")
				}
				after, _ := e.Get(KindPerson, "person", 0)
				project, _ := e.Store.Project("project")
				if !bytes.Equal(before.Meta, after.Meta) || before.Summary != after.Summary || len(project.People) != 0 || len(project.LockedFields) != 0 {
					t.Fatalf("invalid update changed the entity or reverse links: %+v %+v", after, project)
				}
			})
		}
	}
}

func TestPersonStatusAutomaticCuration(t *testing.T) {
	for _, status := range []string{"", "active", "ignored", "self"} {
		t.Run(status, func(t *testing.T) {
			e := editEngine(t)
			pid := PersonID("example", "sender")
			if err := e.Store.PutPerson(&Person{ID: pid, Name: "Contact", Status: status, Note: "Original note", Chats: []string{"old-chat"}, Projects: []string{"old-project"}}); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.putSummary(KindPerson, pid, "Original summary", true); err != nil {
				t.Fatal(err)
			}
			c := &Chat{ID: "chat", Name: "Chat"}
			now := time.Now().UTC()
			e.notePerson(c, "example", SourceMessage{SenderID: "sender", SenderName: "Contact", At: now})
			if slices.Contains(c.People, pid) != (status != "ignored") {
				t.Fatalf("chat association for %q: %v", status, c.People)
			}
			// Old chat membership must not let an ignored account back into a project.
			c.People = []string{pid}
			slug := e.evolveProject("Project", "short", nil, "", c, true, "Progress", now)
			project, ok := e.Store.Project(slug)
			if !ok || slices.Contains(project.People, pid) != (status != "ignored" && status != "self") {
				t.Fatalf("project association for %q: %+v", status, project)
			}
			e.notePersonNote(c, "Contact", "New note")
			e.WritePersonSummary(pid)
			p, _ := e.Store.Person(pid)
			if !slices.Contains(p.Chats, "old-chat") || !slices.Contains(p.Projects, "old-project") {
				t.Fatalf("historical associations lost: %+v", p)
			}
			if slices.Contains(p.Projects, slug) != (status != "ignored" && status != "self") {
				t.Fatalf("reverse project association for %q: %+v", status, p)
			}
			if status == "ignored" {
				if p.Note != "Original note" || strings.TrimSpace(e.Store.Summary(KindPerson, pid)) != "Original summary" {
					t.Fatalf("ignored account was summarized: %+v", p)
				}
				// Persistence also rejects a summary prepared before the status changed.
				if err := e.Store.PutSummary(KindPerson, pid, "Stale automatic summary"); err != nil {
					t.Fatal(err)
				}
				if strings.TrimSpace(e.Store.Summary(KindPerson, pid)) != "Original summary" {
					t.Fatal("automatic summary bypassed the persisted status")
				}
			} else if p.Note != "New note" || !strings.Contains(e.Store.Summary(KindPerson, pid), "New note") {
				t.Fatalf("active account behavior changed: %+v", p)
			}
			listed := len(e.entries(nil, nil, []string{pid})) > 0
			if listed != (status != "ignored" && status != "self") {
				t.Fatal("external roster includes an ignored or self account")
			}
		})
	}
}

func TestPersonStatusDedupeRoster(t *testing.T) {
	for _, status := range []string{"ignored", "self"} {
		t.Run(status, func(t *testing.T) {
			m := &scriptModel{replies: []string{`{"same":false,"reason":"Different work"}`}}
			e := testEngine(t, m)
			people := []string{"account-a", "account-b", "account-c"}
			for _, id := range people {
				if err := e.Store.PutPerson(&Person{ID: id, Status: status}); err != nil {
					t.Fatal(err)
				}
			}
			for _, slug := range []string{"alpha", "beta"} {
				if err := e.Store.PutProject(&Project{Slug: slug, Name: slug, Status: "active", People: people}); err != nil {
					t.Fatal(err)
				}
			}
			if n := e.DedupeProjects(context.Background(), 1); n != 0 || m.calls != 0 {
				t.Fatalf("non-collaborators drove deduplication: merged=%d calls=%d", n, m.calls)
			}
			for _, slug := range []string{"alpha", "beta"} {
				p, _ := e.Store.Project(slug)
				if !slices.Equal(p.People, people) {
					t.Fatal("roster view deleted historical membership")
				}
			}
		})
	}
}

func TestPersonStatusExtensionProvenance(t *testing.T) {
	e := editEngine(t)
	if err := e.Store.PutPerson(&Person{ID: "person"}); err != nil {
		t.Fatal(err)
	}
	ev := Evidence{Chat: "chat", Day: "2026-01-02", N: 1}
	for _, status := range []string{"ignored", "self", "active"} {
		before, _ := e.Store.Person("person")
		res := extApply(t, e, "curator", KindPerson, "person", Update{Status: status}, ev)
		want := json.RawMessage("null")
		if before.Status != "" {
			want, _ = json.Marshal(before.Status)
		}
		if !bytes.Equal(res.Before["status"], want) {
			t.Fatalf("before status = %s, want %s", res.Before["status"], want)
		}
		if err := e.Store.PutPerson(before); err != nil {
			t.Fatal(err)
		}
		p, _ := e.Store.Person("person")
		provenance := p.Provenance["status"]
		if p.Status != status || provenance.By != "ext:curator" || provenance.At.IsZero() || len(provenance.Evidence) != 1 || provenance.Evidence[0] != ev {
			t.Fatalf("status or provenance lost: %+v", p)
		}
	}
	_, err := e.ApplyExtension(KindPerson, "person", Update{Status: "ignored"}, ExtensionWrite{Extension: "other"})
	if owners := conflictOwners(t, err); owners["status"] != "ext:curator" {
		t.Fatalf("status holder not protected: %v", owners)
	}
	editApply(t, e, KindPerson, "person", Update{Status: "self"})
	p, _ := e.Store.Person("person")
	if _, ok := p.Provenance["status"]; ok {
		t.Fatalf("user did not take over status: %+v", p)
	}
	_, err = e.ApplyExtension(KindPerson, "person", Update{Status: "active"}, ExtensionWrite{Extension: "curator"})
	if owners := conflictOwners(t, err); owners["status"] != UserWriter {
		t.Fatalf("user status not protected: %v", owners)
	}
}
