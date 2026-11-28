package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestReportedProviderResolutionKeepsEachCallersKind(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "in flight"}[pending], func(t *testing.T) {
			database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			s, provider, _ := acmeService(t, database)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			if pending {
				s.repoExt = slowAcme{provider, release}
			}
			todo, err := s.createPlainTodo(t.Context(), "Repository work", "", db.TodoEdit{})
			if err != nil {
				t.Fatal(err)
			}
			record := func(counts chan int) reportedLinkSave {
				return func(_ context.Context, links []repository.LinkInput) ([]acceptedReportLink, error) {
					counts <- len(links)
					return nil, nil
				}
			}
			first, second := make(chan int, 4), make(chan int, 4)
			const issue = "https://code.acme.test/team/app/issues/17"
			if _, err := s.importReportedLinks(t.Context(), todo.ID, []string{issue}, "", record(first), nil); err != nil {
				t.Fatal(err)
			}
			if !pending {
				s.reportedLinks.runs.Wait()
			}
			if _, err := s.importReportedLinks(t.Context(), todo.ID, []string{issue}, "pull_request", record(second), nil); err != nil {
				t.Fatal(err)
			}
			close(release)
			s.reportedLinks.runs.Wait()
			total := func(counts chan int) (n int) {
				for len(counts) > 0 {
					n += <-counts
				}
				return n
			}
			if a, b := total(first), total(second); a != 1 || b != 0 {
				t.Fatalf("shared resolution lost the caller's kind filter: any=%d PR-only=%d want=1,0", a, b)
			}
			if got := provider.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
				t.Fatal("resolution was not shared", got)
			}
		})
	}
}

func repositoryReportTask(t *testing.T, s *Service, id string) Todo {
	t.Helper()
	todo := todoNow(s, id)
	if len(todo.Agents) == 0 {
		s.hub.(*fakeHub).setSessions("box", proto.Session{MachineID: "box", SessionID: "report-" + id,
			Agent: "claude", Status: proto.StatusIdle})
		if err := s.ensureTodosLoaded(t.Context()); err != nil {
			t.Fatal(err)
		}
		s.todos.writing.Lock()
		err := s.writeTodoAgentLink(t.Context(), id, "box", "report-"+id, "Repository work", "/code/app", "claude", "", "manual", "", false)
		s.todos.writing.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		todo = todoNow(s, id)
	}
	return todo
}

func TestRepositoryProviderOwnershipIsRecheckedAfterResolution(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "cached", true: "in flight"}[pending], func(t *testing.T) {
			database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			s, provider, _ := acmeService(t, database)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			if pending {
				s.repoExt = slowAcme{provider, release}
			}
			todo, err := s.createPlainTodo(t.Context(), "Repository work", "", db.TodoEdit{})
			if err != nil {
				t.Fatal(err)
			}
			const review = "https://code.acme.test/team/app/reviews/17"
			ownedRepositoryReport(t, s, todo.ID, "Deployment only mentions "+review)
			if !pending {
				s.reportedLinks.runs.Wait()
			}
			if _, links := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID); len(links) != 0 {
				t.Fatal("provider resolution claimed the reference-only PR", links)
			}
			ownedRepositoryReport(t, s, todo.ID, "Created "+review, review)
			close(release)
			s.reportedLinks.runs.Wait()
			if _, links := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID); !slices.Equal(links, []string{review}) {
				t.Fatal("resolution retry cache hid fresh ownership evidence", links)
			}
			if got := provider.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
				t.Fatal("resolution was not shared between the two reports", got)
			}
		})
	}
}

func TestRepositoryProviderPendingReportsKeepLatestOwnership(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "reference replaces proof", true: "fresh proof"}[owned], func(t *testing.T) {
			database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			s, provider, _ := acmeService(t, database)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			s.repoExt = slowAcme{provider, release}
			todo, err := s.createPlainTodo(t.Context(), "Repository work", "", db.TodoEdit{})
			if err != nil {
				t.Fatal(err)
			}
			const review = "https://code.acme.test/team/app/reviews/17"
			ownedRepositoryReport(t, s, todo.ID, "Created "+review, review)
			s.reportedLinks.mu.Lock()
			asking := s.reportedLinks.asking[todo.ID+" "+review]
			s.reportedLinks.mu.Unlock()
			if !asking {
				t.Fatal("the first proven report did not start an asynchronous resolution")
			}
			if owned {
				ownedRepositoryReport(t, s, todo.ID, "Fresh creation "+review, review)
			} else {
				ownedRepositoryReport(t, s, todo.ID, "Deployment only mentions "+review)
			}
			close(release)
			s.reportedLinks.runs.Wait()
			if _, links := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID); slices.Contains(links, review) != owned {
				t.Fatalf("pending resolution inherited stale authorization: links=%v wantLinked=%v", links, owned)
			}
			if got := provider.asked(); !slices.Equal(got, []string{"parse", "link", "describe"}) {
				t.Fatal("the pending report repeated provider resolution", got)
			}
			if !owned && todoNow(s, todo.ID).PRURL != "" {
				t.Fatal("a rejected pending report entered the legacy PR route")
			}
		})
	}
}

type aliasedReportProvider struct{ *acmeProvider }

func (p aliasedReportProvider) RepositoryLink(ctx context.Context, repo repository.Identity, in repository.LinkInput) (repository.TaskLink, error) {
	in.URL = strings.Replace(in.URL, "/changes/", "/reviews/", 1)
	return p.acmeProvider.RepositoryLink(ctx, repo, in)
}

type slowAliasedReportProvider struct {
	aliasedReportProvider
	release chan struct{}
}

func (p slowAliasedReportProvider) RepositoryParse(ctx context.Context, provider, remote string) (repository.Parsed, error) {
	return (slowAcme{p.acmeProvider, p.release}).RepositoryParse(ctx, provider, remote)
}

func TestRepositoryProviderCanonicalAddressKeepsOriginalOwnership(t *testing.T) {
	for _, entry := range []string{"report", "artifacts"} {
		t.Run(entry, func(t *testing.T) {
			database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			s, provider, _ := acmeService(t, database)
			s.repoExt = aliasedReportProvider{provider}
			todo, err := s.createPlainTodo(t.Context(), "Repository work", "", db.TodoEdit{})
			if err != nil {
				t.Fatal(err)
			}
			todo = repositoryReportTask(t, s, todo.ID)
			const raw = "https://code.acme.test/team/app/changes/17"
			const canonical = "https://code.acme.test/team/app/reviews/17"
			text := "Created " + raw
			reply := artifactReportProof(s.hub.(*fakeHub), todo, text, raw)
			if entry == "report" {
				a := todo.Agents[0]
				s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, reply.TS)
			} else {
				work := artifactWork(todo)
				work.TurnAt = reply.TS
				work.Work.Artifacts = []proto.WorkArtifact{{Kind: "pr", Ref: raw}}
				if err := s.importTaskRepositoryArtifacts(t.Context(), work); err != nil {
					t.Fatal(err)
				}
			}
			s.reportedLinks.runs.Wait()
			if _, links := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID); !slices.Equal(links, []string{canonical}) {
				t.Fatalf("provider canonicalization discarded the original creation proof: %v", links)
			}
		})
	}
}

func TestRepositoryProviderCompletionAppliesTaskProgress(t *testing.T) {
	for _, mode := range []string{"first report", "latest pending report", "stale report"} {
		t.Run(mode, func(t *testing.T) {
			database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			s, provider, _ := acmeService(t, database)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			s.repoExt = slowAliasedReportProvider{aliasedReportProvider{provider}, release}
			todo, err := s.createPlainTodo(t.Context(), "Repository work", "", db.TodoEdit{})
			if err != nil {
				t.Fatal(err)
			}
			todo = repositoryReportTask(t, s, todo.ID)
			f := s.hub.(*fakeHub)
			a := todo.Agents[0]
			const raw = "https://code.acme.test/team/app/changes/17"
			const canonical = "https://code.acme.test/team/app/reviews/17"
			text := "Opened " + raw
			reply := artifactReportProof(f, todo, text, raw)
			s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, reply.TS)
			if cur := todoNow(s, todo.ID); cur.PRURL != "" {
				t.Fatalf("PR set before provider answered: %q", cur.PRURL)
			}
			if mode != "first report" {
				at := reply.TS.Add(time.Second)
				text = "Merged " + raw
				next := proto.Item{Role: "assistant", Kind: proto.KindText, Text: text, TS: at}
				ask := proto.Item{Role: "user", Kind: proto.KindText, Text: "Check the review", TS: at.Add(-500 * time.Millisecond)}
				proof := proto.Item{Role: "user", Kind: proto.KindToolResult, TS: at.Add(-time.Millisecond),
					Outcome: &proto.ToolOutcome{Status: proto.OutcomeOK, Git: &proto.OutcomeGit{PR: &proto.OutcomePR{Action: "created", URL: raw}}}}
				if mode == "latest pending report" {
					f.setItems(ask, proof, next)
					s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, at)
				} else {
					f.setItems(ask, next)
				}
			}
			close(release)
			s.reportedLinks.runs.Wait()
			wantPR, wantStage := canonical, "review"
			if mode == "latest pending report" {
				wantStage = "merge"
			} else if mode == "stale report" {
				wantPR, wantStage = "", ""
			}
			_, links := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID)
			if cur := todoNow(s, todo.ID); cur.PRURL != wantPR || cur.Stage != wantStage ||
				slices.Contains(links, canonical) != (wantPR != "") {
				t.Fatalf("deferred report progress: PR=%q stage=%q links=%v; want PR=%q stage=%q",
					cur.PRURL, cur.Stage, links, wantPR, wantStage)
			}
		})
	}
}

// Provider routing fixtures still need a real task link and matching native creation evidence.
func ownedRepositoryReport(t *testing.T, s *Service, id, text string, created ...string) bool {
	t.Helper()
	todo := repositoryReportTask(t, s, id)
	reply := artifactReportProof(s.hub.(*fakeHub), todo, text, created...)
	a := todo.Agents[0]
	return s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, reply.TS)
}

func TestRepositoryProviderReportsRequireTurnOwnership(t *testing.T) {
	for _, entry := range []string{"report", "artifacts"} {
		for _, evidence := range []string{"created in turn", "reference only", "earlier turn", "another open task"} {
			t.Run(entry+"/"+evidence, func(t *testing.T) {
				database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				s, provider, _ := acmeService(t, database)
				todo, err := s.createPlainTodo(t.Context(), "Deployment evidence", "", db.TodoEdit{})
				if err != nil {
					t.Fatal(err)
				}
				todo = repositoryReportTask(t, s, todo.ID)
				f := s.hub.(*fakeHub)
				const review = "https://code.acme.test/team/app/reviews/17"
				text := "Deployment report mentions " + review
				reply := artifactReportProof(f, todo, text, review)
				switch evidence {
				case "reference only":
					f.setItems(reply)
				case "earlier turn":
					old := artifactCreationItems(todo, review)
					reply.TS = reply.TS.Add(2 * time.Second)
					old = append(old, proto.Item{Role: "user", Kind: proto.KindText, Text: "Only check deployment", TS: reply.TS.Add(-time.Second)}, reply)
					f.setItems(old...)
				case "another open task":
					other, err := s.createPlainTodo(t.Context(), "Original implementation", "", db.TodoEdit{})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add",
						[]byte(`{"id":"`+other.ID+`","urls":["`+review+`"]}`)); err != nil {
						t.Fatal(err)
					}
				}
				a := todo.Agents[0]
				if entry == "report" {
					s.advanceTaskReplyAt(t.Context(), todo, a.MachineID, a.SessionID, text, reply.TS)
				} else {
					work := artifactWork(todo)
					work.TurnAt = reply.TS
					work.Work.Artifacts = []proto.WorkArtifact{{Kind: "pr", Ref: review}}
					if err := s.importTaskRepositoryArtifacts(t.Context(), work); err != nil {
						t.Fatal(err)
					}
				}
				s.reportedLinks.runs.Wait()
				if evidence == "reference only" || evidence == "earlier turn" {
					if got := provider.asked(); len(got) != 0 {
						t.Fatal("an unproven report reached the provider", got)
					}
				}
				_, urls := savedArtifactURLs(t, s.store().(todoRepositoryStore), todo.ID)
				if want := evidence == "created in turn"; slices.Contains(urls, review) != want {
					t.Fatalf("provider resolution replaced ownership proof: links=%v wantLinked=%v", urls, want)
				}
				if evidence != "created in turn" && todoNow(s, todo.ID).PRURL != "" {
					t.Fatal("rejected provider PR entered the legacy route")
				}
			})
		}
	}
}
