package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const artifactPR = "https://github.com/example/project/pull/7"

func artifactCreationProof(f *fakeHub, todo Todo, urls ...string) {
	f.setItems(artifactCreationItems(todo, urls...)...)
}

func artifactTurnAt(todo Todo) time.Time {
	at := todo.Agents[0].LinkedAt
	for _, agent := range todo.Agents {
		if agent.LinkedAt.After(at) {
			at = agent.LinkedAt
		}
	}
	return at.Add(time.Second)
}

func artifactReportProof(f *fakeHub, todo Todo, text string, urls ...string) proto.Item {
	reply := proto.Item{Role: "assistant", Kind: proto.KindText, Text: text, TS: artifactTurnAt(todo)}
	f.setItems(append(artifactCreationItems(todo, urls...), reply)...)
	return reply
}

func artifactCreationItems(todo Todo, urls ...string) []proto.Item {
	items := []proto.Item{}
	at := artifactTurnAt(todo).Add(-time.Second)
	for i, url := range urls {
		items = append(items, proto.Item{ID: fmt.Sprint("created-", i), Role: "user", Kind: proto.KindToolResult,
			TS: at.Add(time.Millisecond), Outcome: &proto.ToolOutcome{
				Status: proto.OutcomeOK, Git: &proto.OutcomeGit{PR: &proto.OutcomePR{URL: url, Action: "created"}}}})
	}
	items = append(items, proto.Item{Kind: proto.KindNotice, TS: artifactTurnAt(todo)})
	return items
}

func artifactWork(todo Todo) db.SessionWork {
	a := todo.Agents[0]
	return db.SessionWork{MachineID: a.MachineID, SessionID: a.SessionID, TurnAt: artifactTurnAt(todo),
		Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, By: "rule",
			Artifacts: []proto.WorkArtifact{{Kind: "pr", Ref: artifactPR + "#issuecomment-1"},
				{Kind: "pr", Ref: "https://github.com/example/project/pull/8"}}}}
}

func savedArtifactURLs(t *testing.T, st todoRepositoryStore, id string) (repository.TaskSet, []string) {
	t.Helper()
	set, err := st.TodoRepositories(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	urls := []string{}
	for _, row := range set.Repositories {
		for _, link := range row.Links {
			urls = append(urls, link.CanonicalURL)
		}
	}
	slices.Sort(urls)
	return set, urls
}

func artifactInboxItem(s *Service, id, url string) *InboxItem {
	it := &InboxItem{ID: id, Source: "github", Kind: KindMention, Title: "Review update", Sender: "maintainer",
		Last: id, Count: 1, Preview: "Please check the reported failure.", UpdatedAt: time.Now(),
		Connector: &InboxConnector{Thread: "example/project#7", URL: url}}
	s.inbox.mu.Lock()
	s.inbox.items[id] = it
	s.inbox.mu.Unlock()
	return it
}

func TestTaskRepositoryArtifactsRouteFeedbackToExistingAgent(t *testing.T) {
	for _, entry := range []string{"closeout", "work artifacts", "scheduled triage artifacts"} {
		t.Run(entry, func(t *testing.T) {
			database := todoTestDB(t)
			st := repositorystore.New(database)
			s, f := newQuietServiceOn(t, st)
			s.superviseTimes.gather = time.Hour
			turnInboxSignalsOn(t, s)
			in := newFakeInbox()
			f.inbox, in.svc = in, s
			if err := s.inboxReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			todo := agentTask(t, s, f)
			artifactCreationProof(f, todo, artifactPR, "https://github.com/example/project/pull/8")
			before := len(sentToAgent(f))
			work := artifactWork(todo)
			switch entry {
			case "closeout":
				text := "Related work: https://github.com/example/project/pull/99\n" +
					"<closeout status=\"blocked\" merged=\"no\" deployed=\"no\">\n" +
					"artifact: pr " + artifactPR + "\nartifact: pr https://github.com/example/project/pull/8\n" +
					"waiting: Review\n</closeout>"
				reply := artifactReportProof(f, todo, text, artifactPR, "https://github.com/example/project/pull/8")
				s.taskStageItems(t.Context(), SessionItems{MachineID: work.MachineID, SessionID: work.SessionID,
					Items: []proto.Item{reply}})
			case "work artifacts":
				s.putWork(t.Context(), work)
			case "scheduled triage artifacts":
				if !s.putTriageWork(work, 0) {
					t.Fatal("triage did not commit its artifacts")
				}
			}
			set, urls := savedArtifactURLs(t, st, todo.ID)
			if !slices.Equal(urls, []string{artifactPR, "https://github.com/example/project/pull/8"}) {
				t.Fatalf("task-owned PRs not saved, or background PR leaked: %v", urls)
			}
			current := todoNow(s, todo.ID)
			if current.PRURL == artifactPR || strings.Contains(current.Title+"\n"+current.Notes, artifactPR) || current.DoneAt != nil {
				t.Fatalf("test needs a saved-only PR on an open task: %+v", current)
			}
			for _, comment := range []struct{ id, fragment string }{
				{"github:artifact-comment", "#issuecomment-20"},
				{"github:artifact-review", "#discussion_r21"},
			} {
				it := artifactInboxItem(s, comment.id, artifactPR+comment.fragment)
				s.handPass(t.Context())
				waitFor(t, "feedback on the existing task Agent", func() bool {
					return len(sentToAgent(f)) > before
				})
				s.inbox.mu.Lock()
				linked := it.Agent != nil && it.Agent.TodoID == todo.ID
				s.inbox.mu.Unlock()
				sent := sentToAgent(f)[before:]
				if !linked || len(sent) != 1 || !strings.Contains(sent[0], comment.fragment) {
					t.Fatalf("saved link did not route to its Agent: linked=%v, notes=%v", linked, sent)
				}
				before++
				s.handPass(t.Context())
				s.inbox.mu.Lock()
				stillLinked := it.Agent != nil && it.Agent.TodoID == todo.ID
				s.inbox.mu.Unlock()
				if !stillLinked || len(sentToAgent(f)) != before {
					t.Fatal("repository scope cleared the link or replay delivered twice")
				}
			}
			after, _ := savedArtifactURLs(t, st, todo.ID)
			rows, err := database.ListTodos(t.Context())
			if err != nil || after.Revision != set.Revision || len(rows) != 1 {
				t.Fatal("replay changed associations or created another task", after, rows, err)
			}
		})
	}
}

func TestTaskRepositoryArtifactsStayOnCurrentTask(t *testing.T) {
	for _, name := range []string{"unlinked session", "wrong machine", "older turn", "released", "dismissed", "closed", "retired"} {
		t.Run(name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, f := newQuietServiceOn(t, st)
			todo := agentTask(t, s, f)
			work := artifactWork(todo)
			now := time.Now()
			switch name {
			case "unlinked session":
				work.SessionID = "another-session"
			case "wrong machine":
				work.MachineID = "another-machine"
			case "older turn":
				work.TurnAt = todo.Agents[0].LinkedAt.Add(-time.Second)
			case "retired":
				work.Work.RetiredAt = &now
			default:
				s.todos.mu.Lock()
				row := s.todos.todos[todo.ID]
				switch name {
				case "released":
					row.Agents[0].ReleasedAt = &now
				case "dismissed":
					row.Agents[0].DismissedAt = &now
				case "closed":
					row.DoneAt = &now
				}
				s.todos.mu.Unlock()
			}
			s.putWork(t.Context(), work)
			if set, urls := savedArtifactURLs(t, st, todo.ID); set.Revision != 0 || len(urls) != 0 {
				t.Fatal("foreign or historical artifacts were imported", set)
			}
		})
	}
}

type artifactImportStore struct {
	*repositorystore.Store
	fail atomic.Bool
}

func (st *artifactImportStore) ImportTodoRepositoryLinks(ctx context.Context, id string, urls []string, source string) (repository.TaskSet, bool, error) {
	if st.fail.Load() {
		return repository.TaskSet{}, false, errors.New("association store temporarily unavailable")
	}
	return st.Store.ImportTodoRepositoryLinks(ctx, id, urls, source)
}

func TestTaskRepositoryArtifactsRetryAfterReloadWithoutResurrectingRemovedLinks(t *testing.T) {
	st := &artifactImportStore{Store: repositorystore.New(todoTestDB(t))}
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	artifactCreationProof(f, todo, artifactPR, "https://github.com/example/project/pull/8")
	st.fail.Store(true)
	s.putWork(t.Context(), artifactWork(todo))
	if _, urls := savedArtifactURLs(t, st, todo.ID); len(urls) != 0 {
		t.Fatal("failed import changed associations", urls)
	}
	st.fail.Store(false)
	next, nextHub := newQuietServiceOn(t, st)
	artifactCreationProof(nextHub, todo, artifactPR, "https://github.com/example/project/pull/8")
	if err := next.ensureTodosLoaded(t.Context()); err != nil || !next.loadWork(t.Context()) {
		t.Fatal("reload failed", err)
	}
	next.linkInboxTaskURLs(t.Context())
	set, urls := savedArtifactURLs(t, st, todo.ID)
	if !slices.Contains(urls, artifactPR) {
		t.Fatal("durable Agent artifacts were not recovered", set)
	}
	removed, err := st.SetTodoRepositories(t.Context(), todo.ID, repository.SetInput{
		ExpectedRevision: &set.Revision, ExpectedCatalogRevision: new(int64), Repositories: []repository.TaskInput{}})
	if err != nil {
		t.Fatal(err)
	}
	next.linkInboxTaskURLs(t.Context())
	set, urls = savedArtifactURLs(t, st, todo.ID)
	if set.Revision != removed.Revision || len(urls) != 0 {
		t.Fatal("artifact replay resurrected a removed link", set)
	}
}

func TestTaskRepositoryArtifactsValidateExactPRURLs(t *testing.T) {
	artifacts := []proto.WorkArtifact{
		{Kind: "pr", Ref: artifactPR + "#discussion_r1"},
		{Kind: "pr", Ref: "https://gitlab.example.com/team/api/-/merge_requests/9"},
		{Kind: "url", Ref: "https://github.com/example/project/pull/99"},
		{Kind: "pr", Ref: "https://github.com/example/project/issues/7"},
		{Kind: "pr", Ref: artifactPR + "?token=synthetic-secret"},
		{Kind: "pr", Ref: "https://user:synthetic-secret@" + "github.com/example/project/pull/10"},
		{Kind: "pr", Ref: "#11"},
	}
	if got := taskRepositoryArtifactLinks(artifacts, true); !slices.Equal(got, []string{artifactPR, "https://gitlab.example.com/team/api/-/merge_requests/9"}) {
		t.Fatal("non-PR, unsafe or unresolved artifacts accepted", got)
	}
	text := "Related change: " + artifactPR + "\n" + researchNoPRCloseout + relatedMemoryFooter
	if got := taskRepositoryReportLinks(text); len(got) != 0 {
		t.Fatal("a non-PR closeout imported incidental PRs", got)
	}
	titled := []proto.WorkArtifact{{Kind: "pr", Ref: "Prepared [Keep task context](" + artifactPR + ") for review."}}
	if got := taskRepositoryArtifactLinks(titled, true); !slices.Equal(got, []string{artifactPR}) {
		t.Fatal("a PR artifact's label hid its URL", got)
	}
	// Display clipping in closeout.Parse must not drop associations from the original record.
	text = "<closeout status=\"blocked\" merged=\"no\" deployed=\"no\">\n"
	for n := 1; n <= 7; n++ {
		text += fmt.Sprintf("artifact: pr https://github.com/example/project/pull/%d Change %d\n", n, n)
	}
	text += "waiting: Review\n</closeout>"
	if got := taskRepositoryReportLinks(text); len(got) != 7 {
		t.Fatal("closeout display limits clipped task PR associations", got)
	}
}

func TestInboxSavedRepositoryLinksRemainUnambiguousAndOpen(t *testing.T) {
	for _, name := range []string{"unique", "ambiguous", "closed", "cancelled", "different repository"} {
		t.Run(name, func(t *testing.T) {
			st := repositorystore.New(todoTestDB(t))
			s, _ := newQuietServiceOn(t, st)
			todo := supervisedTodo(t, s, "Review an artifact")
			if err := s.importTodoRepositoryLinks(t.Context(), todo.ID, []string{artifactPR}, "explicit"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			switch name {
			case "ambiguous":
				other := supervisedTodo(t, s, "Another artifact")
				if err := s.importTodoRepositoryLinks(t.Context(), other.ID, []string{artifactPR}, "explicit"); err != nil {
					t.Fatal(err)
				}
			case "closed":
				s.todos.mu.Lock()
				s.todos.todos[todo.ID].DoneAt = &now
				s.todos.mu.Unlock()
			case "cancelled":
				if err := s.failTodo(t.Context(), todo.ID, "Cancelled by the owner"); err != nil {
					t.Fatal(err)
				}
			}
			url := artifactPR + "#issuecomment-20"
			it := artifactInboxItem(s, "github:saved-association", url)
			if name == "different repository" {
				it.Connector.Thread = "other/project#7"
				it.Connector.URL = strings.Replace(url, "example/project", "other/project", 1)
			}
			s.linkInboxTaskURLs(t.Context())
			s.inbox.mu.Lock()
			linked := it.Agent != nil && it.Agent.TodoID != ""
			s.inbox.mu.Unlock()
			if linked != (name == "unique") {
				t.Fatal("incorrect deterministic association", name, it.Agent)
			}
		})
	}
}
