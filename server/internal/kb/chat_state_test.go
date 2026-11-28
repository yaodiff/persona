package kb

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// prChat ingests a pull request's chat with one message, linked to project slug.
func prChat(t *testing.T, e *Engine, id, slug string, at time.Time) *Chat {
	t.Helper()
	sc := SourceChat{ID: id, Name: "repo#" + id, Kind: "pr", State: "open"}
	if _, err := e.Ingest(context.Background(), named("github"), sc, []SourceMessage{{ID: "body", SenderID: "alice", SenderName: "alice", Text: "please review", At: at}}); err != nil {
		t.Fatal(err)
	}
	c, ok := e.Store.Chat(ChatID("github", id))
	if !ok {
		t.Fatal("no chat")
	}
	if slug != "" {
		c.Projects = append(c.Projects, slug)
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
		p, ok := e.Store.Project(slug)
		if !ok {
			p = &Project{Slug: slug, Name: slug, Kind: "short", Status: "active"}
		}
		p.Chats = append(p.Chats, c.ID)
		if err := e.Store.PutProject(p); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// A merge with no new message is recorded on the chat, and its projects are rewritten with it (#9427).
func TestIngestRecordsStateWithoutLines(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	c := prChat(t, e, "repo-1", "fix-a", at)
	if c.State != "open" || !c.StateAt.IsZero() && c.StateAt.Before(at) {
		t.Fatalf("first batch: state %q at %v", c.State, c.StateAt)
	}
	p, _ := e.Store.Project("fix-a")
	p.Dirty = false
	_ = e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false)

	merged := at.Add(time.Hour)
	sc := SourceChat{ID: "repo-1", Name: "repo#repo-1", Kind: "pr", State: "merged", StateAt: merged}
	if n, err := e.Ingest(context.Background(), named("github"), sc, nil); n != 0 || err != nil {
		t.Fatalf("state-only batch: %d, %v", n, err)
	}
	c, _ = e.Store.Chat(c.ID)
	if c.State != "merged" || !c.StateAt.Equal(merged) || !c.Ended() || c.Pending != 1 {
		t.Fatalf("after the merge: %+v", c)
	}
	if p, _ := e.Store.Project("fix-a"); !p.Dirty {
		t.Fatal("the project is not marked for a rewrite")
	}

	// The same state again changes nothing; a source that reports none leaves it.
	before, _ := e.Store.Chat(c.ID)
	for _, again := range []SourceChat{sc, {ID: "repo-1", Kind: "pr"}} {
		if _, err := e.Ingest(context.Background(), named("github"), again, nil); err != nil {
			t.Fatal(err)
		}
	}
	if after, _ := e.Store.Chat(c.ID); after.State != before.State || !after.StateAt.Equal(before.StateAt) {
		t.Fatalf("changed by a repeat: %+v", after)
	}

	// A state for a chat the knowledge base never stored creates none.
	if _, err := e.Ingest(context.Background(), named("github"), SourceChat{ID: "repo-2", Kind: "pr", State: "closed"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Chat(ChatID("github", "repo-2")); ok {
		t.Fatal("a chat made from a state alone")
	}
}

// A batch with lines records its state too, and a reopen dirties the project as a merge does.
func TestIngestRecordsStateWithLines(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	c := prChat(t, e, "repo-3", "fix-b", at)
	closed := at.Add(10 * time.Minute)
	sc := SourceChat{ID: "repo-3", Kind: "pr", State: "closed", StateAt: closed}
	line := SourceMessage{ID: "state-closed-1", SenderName: "GitHub", Type: "SYSTEM", Text: "（状态）已关闭", At: closed}
	if n, err := e.Ingest(context.Background(), named("github"), sc, []SourceMessage{line}); n != 1 || err != nil {
		t.Fatalf("closed: %d, %v", n, err)
	}
	if c, _ = e.Store.Chat(c.ID); c.State != "closed" || c.Pending != 2 {
		t.Fatalf("closed chat %+v", c)
	}
	if p, _ := e.Store.Project("fix-b"); !p.Dirty {
		t.Fatal("not dirty after the close")
	}
}

func TestIngestSourceURLDoesNotDirtyProjects(t *testing.T) {
	for _, withMessages := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata only", true: "with messages"}[withMessages], func(t *testing.T) {
			e := testEngine(t, nil)
			sc := SourceChat{ID: "sample-garden-4", Kind: "pr", State: "open", StateAt: projectEvidenceTime}
			c := &Chat{ID: ChatID("github", sc.ID), Source: "github", SourceID: sc.ID, Kind: sc.Kind,
				State: sc.State, StateAt: sc.StateAt, Projects: []string{"garden"}}
			if err := e.Store.PutChat(c); err != nil {
				t.Fatal(err)
			}
			if err := e.Store.PutProject(&Project{Slug: "garden", Status: "active", Chats: []string{c.ID}}); err != nil {
				t.Fatal(err)
			}
			for i, url := range []string{"https://github.com/sample/garden/pull/4", "https://github.com/sample/garden-next/pull/4"} {
				sc.SourceURL = url
				var messages []SourceMessage
				if withMessages {
					messages = []SourceMessage{{ID: url, Text: "A review comment", At: e.textCutoff().AddDate(0, 0, i+1)}}
				}
				if n, err := e.Ingest(context.Background(), named("github"), sc, messages); err != nil || n != len(messages) {
					t.Fatalf("ingesting source URL %q: %d, %v", url, n, err)
				}
				got, _ := e.Store.Chat(c.ID)
				if got == nil || got.SourceURL != url || got.State != sc.State || !got.StateAt.Equal(sc.StateAt) {
					t.Fatalf("expected persisted URL and unchanged state: %+v", got)
				}
				p, _ := e.Store.Project("garden")
				if p == nil || p.Dirty {
					t.Fatalf("recording source URL %q dirtied the project: %+v", url, p)
				}
			}
		})
	}
}

// FinishProjects finishes a project only on the sources' facts and only when every condition holds.
func TestFinishProjects(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-5 * 24 * time.Hour)
	end := func(e *Engine, c *Chat, state string, at time.Time) {
		t.Helper()
		if _, err := e.Ingest(context.Background(), named("github"), SourceChat{ID: c.SourceID, Kind: "pr", State: state, StateAt: at}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		setup  func(e *Engine)
		busy   bool
		finish bool
	}{
		{"merged and quiet", func(e *Engine) { end(e, prChat(t, e, "a1", "p", old), "merged", old) }, false, true},
		{"merged and closed, paused", func(e *Engine) {
			end(e, prChat(t, e, "a1", "p", old), "merged", old)
			end(e, prChat(t, e, "a2", "p", old), "closed", old)
			p, _ := e.Store.Project("p")
			p.Status = "paused"
			_ = e.Store.PutProject(p)
		}, false, true},
		{"one chat still open", func(e *Engine) {
			end(e, prChat(t, e, "a1", "p", old), "merged", old)
			prChat(t, e, "a2", "p", old)
		}, false, false},
		{"merged two days ago", func(e *Engine) { end(e, prChat(t, e, "a1", "p", old), "merged", now.Add(-48*time.Hour)) }, false, false},
		{"a timeline entry yesterday", func(e *Engine) {
			end(e, prChat(t, e, "a1", "p", old), "merged", old)
			_ = e.Store.AppendLog("p", now.Add(-24*time.Hour), "chat", "follow-up")
		}, false, false},
		{"long-running", func(e *Engine) {
			end(e, prChat(t, e, "a1", "p", old), "merged", old)
			p, _ := e.Store.Project("p")
			p.Kind = "long"
			_ = e.Store.PutProject(p)
		}, false, false},
		{"status locked", func(e *Engine) {
			end(e, prChat(t, e, "a1", "p", old), "merged", old)
			p, _ := e.Store.Project("p")
			p.LockedFields = []string{"status"}
			_ = e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, true)
		}, false, false},
		{"an unfinished to-do", func(e *Engine) { end(e, prChat(t, e, "a1", "p", old), "merged", old) }, true, false},
		{"no chats", func(e *Engine) {
			_ = e.Store.PutProject(&Project{Slug: "p", Name: "p", Kind: "short", Status: "active"})
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := testEngine(t, nil)
			tc.setup(e)
			// Its own writes above made the project look busy just now: date them back.
			p, _ := e.Store.Project("p")
			p.Updated = old
			_ = e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false)
			got := e.FinishProjects(now, func(slug string) bool { return tc.busy && slug == "p" })
			p, _ = e.Store.Project("p")
			if (len(got) == 1) != tc.finish || (p.Status == "done") != tc.finish {
				t.Fatalf("finished %v, status %q; want finished %v", got, p.Status, tc.finish)
			}
		})
	}
}

// A rewrite does not take a finished project back to active: only news does.
func TestRewriteKeepsAFinishedProjectDone(t *testing.T) {
	m := &captureModel{reply: `{"summary":"s","one_line":"等评审","status":"active"}`}
	e := testEngine(t, m)
	_ = e.Store.PutProject(&Project{Slug: "p", Name: "p", Kind: "short", Status: "done", Dirty: true})
	if n := e.RefreshProjects(context.Background(), 1); n != 1 {
		t.Fatalf("rewrote %d", n)
	}
	if p, _ := e.Store.Project("p"); p.Status != "done" || p.OneLine != "等评审" {
		t.Fatalf("after the rewrite: %q %q", p.Status, p.OneLine)
	}
	// News revives it, as before.
	c := prChat(t, e, "a1", "", time.Now())
	if slug := e.evolveProject("p", "short", nil, "", c, true, "reopened", time.Now()); slug != "p" {
		t.Fatal(slug)
	}
	if p, _ := e.Store.Project("p"); p.Status != "active" {
		t.Fatalf("not revived: %q", p.Status)
	}
}

// The snapshot's digest leaves out chats that ended at their source and projects whose chats all did (#9427).
func TestPromptLeavesOutEndedChatsAndProjects(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Now().Add(-time.Hour).UTC()
	prChat(t, e, "c-ended", "over", at)
	prChat(t, e, "c-live", "going", at)
	prChat(t, e, "c-other", "going", at)
	for _, id := range []string{"c-ended", "c-other"} {
		if _, err := e.Ingest(context.Background(), named("github"), SourceChat{ID: id, Kind: "pr", State: "merged", StateAt: at}, nil); err != nil {
			t.Fatal(err)
		}
	}
	_ = e.Store.PutProject(&Project{Slug: "finished", Name: "finished", Kind: "short", Status: "done"})
	var projects, chats []string
	d := e.Prompt(6, 8)
	for _, x := range e.List(KindProject, "") {
		if strings.Contains(d, x.Name) {
			projects = append(projects, x.ID)
		}
	}
	for _, x := range e.List(KindChat, "") {
		if strings.Contains(d, x.Name) {
			chats = append(chats, x.ID)
		}
	}
	slices.Sort(projects)
	if !slices.Equal(projects, []string{"going"}) || !slices.Equal(chats, []string{ChatID("github", "c-live")}) {
		t.Fatalf("projects %v chats %v in\n%s", projects, chats, d)
	}
}

// A project whose summary the user locked keeps that summary and its lock through a merge (#9427, from the issue's
// note): the event dirties the project, the rewrite may update the one line and the status, and FinishProjects may
// finish it, but the locked text is never replaced.
func TestMergeLeavesALockedSummary(t *testing.T) {
	m := &captureModel{reply: `{"summary":"PR 已合并，事情做完了","one_line":"PR 已合并","status":"active"}`}
	e := testEngine(t, m)
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	c := prChat(t, e, "lock-1", "locked", at)
	const mine = "# locked\n\n我自己写的结论：等评审\n"
	if err := e.Store.putSummary(KindProject, "locked", mine, true); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project("locked")
	p.LockedFields, p.Dirty = []string{"summary"}, false
	if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, true); err != nil {
		t.Fatal(err)
	}

	if _, err := e.Ingest(context.Background(), named("github"), SourceChat{ID: c.SourceID, Kind: "pr", State: "merged", StateAt: at}, nil); err != nil {
		t.Fatal(err)
	}
	if p, _ = e.Store.Project("locked"); !p.Dirty {
		t.Fatal("the merge did not mark the project for a rewrite")
	}
	if n := e.RefreshProjects(context.Background(), 1); n != 1 {
		t.Fatalf("rewrote %d", n)
	}
	check := func(when string) *Project {
		t.Helper()
		p, _ := e.Store.Project("locked")
		if got := e.Store.Summary(KindProject, "locked"); got != mine {
			t.Fatalf("%s: the locked summary changed to %q", when, got)
		}
		if !slices.Equal(p.LockedFields, []string{"summary"}) {
			t.Fatalf("%s: locks %v", when, p.LockedFields)
		}
		return p
	}
	if p := check("after the rewrite"); p.OneLine != "PR 已合并" {
		t.Fatalf("one line %q", p.OneLine)
	}

	if got := e.FinishProjects(time.Now().Add(4*24*time.Hour), nil); !slices.Equal(got, []string{"locked"}) {
		t.Fatalf("finished %v", got)
	}
	if p := check("after finishing"); p.Status != "done" {
		t.Fatalf("status %q", p.Status)
	}
}
