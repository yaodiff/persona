package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ownDeliveryPR = "https://github.com/o/r/pull/21"
	foreignPR     = "https://github.com/o/r/pull/6206"
)

// linkRoleTask is a supervised, merge-criterion task on a real Postgres store with the merged delivery gate.
func linkRoleTask(t *testing.T) (*Service, Todo, *fakeForge) {
	t.Helper()
	st := repositorystore.New(todoTestDB(t))
	s, _, todo := newSupervisedServiceOn(t, st)
	setDeliveryGate(t, s, deliveryGateMerged)
	forge := &fakeForge{} // nothing is merged or owned by default
	s.deploy.forge = forge
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}
	return s, todo, forge
}

func linkRoles(t *testing.T, s *Service, todoID string) map[string]string {
	t.Helper()
	set, err := s.savedTodoRepositories(context.Background(), todoID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			role := l.Role
			if role == "" {
				role = repository.LinkRoleDelivery
			}
			got[l.CanonicalURL] = role
		}
	}
	return got
}

func setTestTodoLinkRole(t *testing.T, s *Service, todoID, u, role string) {
	t.Helper()
	if _, err := s.store().(*repositorystore.Store).SetTodoRepositoryLinkRole(t.Context(), todoID, u, role); err != nil {
		t.Fatal(err)
	}
}

// A reference-role PR is display-only: an open, unmerged PR the task merely cites must not block acceptance.
// Removing the role filtering makes this test fail with the not-merged delivery error.
func TestReferenceRolePRDoesNotBlockAcceptance(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{foreignPR}, "pull_request", nil, map[string]string{foreignPR: repository.LinkRoleReference}, nil); err != nil {
		t.Fatal(err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleReference {
		t.Fatalf("foreign PR stored as %q, want reference", got)
	}
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("a reference PR blocked acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("task stayed open although its only linked PR was a reference")
	}
}

func TestReferenceReportPRDoesNotBlockThroughStage(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	reply := "Related work: " + foreignPR
	ctx := context.Background()
	s.advanceTaskReply(ctx, todo, "box", todo.Agents[0].SessionID, reply)
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleReference {
		t.Fatalf("related-work PR role = %q, want reference", got)
	}
	if got := todoNow(s, todo.ID).PRURL; got != "" {
		t.Fatalf("a reference PR became the delivery-stage PR: %q", got)
	}
	// Older tasks can still carry a reference in the legacy delivery-stage slot.
	s.setTaskStage(ctx, todo.ID, "review", foreignPR)
	if got := todoNow(s, todo.ID).PRURL; got != foreignPR {
		t.Fatalf("legacy stage PR = %q, want %q", got, foreignPR)
	}
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("a reference PR tracked by the stage blocked acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("task stayed open although its only reported PR was a reference")
	}
}

// A delivery-role PR keeps the old gate: unmerged delivery still refuses acceptance.
func TestDeliveryRolePRStillBlocksAcceptance(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{ownDeliveryPR}, "pull_request", nil, map[string]string{ownDeliveryPR: repository.LinkRoleDelivery}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := acceptReview(s, todo.ID, nil); err == nil ||
		!strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("unmerged delivery PR was accepted: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("task closed with an unmerged delivery PR")
	}
}

func TestBoundDeliveryRoleSurvivesDifferentForgeAuthor(t *testing.T) {
	s, todo, forge := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{foreignPR}, "pull_request", nil,
		map[string]string{foreignPR: repository.LinkRoleDelivery}, nil); err != nil {
		t.Fatal(err)
	}
	s.setTaskStage(ctx, todo.ID, "review", foreignPR)
	if got := todoNow(s, todo.ID).PRURL; got != foreignPR {
		t.Fatalf("stage PR = %q, want foreign PR %q", got, foreignPR)
	}
	forge.ownedPRs = map[string]bool{"o/r#6206": false} // known, opened by another author
	if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("bound delivery PR should block acceptance before merge: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("bound PR has role %q, want delivery", got)
	}
}

// A saved delivery binding stays authoritative when the stage happens to name the same PR (#17792).
func TestBoundDeliveryEqualToStagePR(t *testing.T) {
	for _, tc := range []struct {
		name, closeout, wantError string
		issueOnly                 bool
	}{
		{"unmerged", "<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\nartifact: pr " + ownDeliveryPR + "\n</closeout>", "not merged yet", false},
		{"no-PR closeout", researchNoPRCloseout, "not merged yet", false},
		{"forge confirms a non-PR", "", "HTTP 404", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo, forge := linkRoleTask(t)
			ctx := t.Context()
			if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{ownDeliveryPR}, "pull_request", nil,
				map[string]string{ownDeliveryPR: repository.LinkRoleDelivery}, nil); err != nil {
				t.Fatal(err)
			}
			s.setTaskStage(ctx, todo.ID, "review", ownDeliveryPR)
			if tc.issueOnly {
				forge.mu.Lock()
				forge.err = errors.New("HTTP 404")
				forge.issueOnly = true
				forge.mu.Unlock()
			} else {
				s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: tc.closeout})
			}
			if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("the bound PR did not block acceptance with %q: %v", tc.wantError, err)
			}
			if got := todoNow(s, todo.ID); got.DoneAt != nil || got.PRURL != ownDeliveryPR {
				t.Fatalf("acceptance changed the task's delivery: done=%v PR=%q", got.DoneAt, got.PRURL)
			}
			if got := linkRoles(t, s, todo.ID)[ownDeliveryPR]; got != repository.LinkRoleDelivery {
				t.Fatalf("bound PR has role %q, want delivery", got)
			}
		})
	}
}

func TestDeliveryBindingAfterStageMirror(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := t.Context()
	s.setTaskStage(ctx, todo.ID, "review", ownDeliveryPR)
	args := fmt.Appendf(nil, `{"id":%q,"urls":[%q]}`, todo.ID, ownDeliveryPR)
	if _, err := s.toolTodoRepositoryLinksAdd(ctx, "", "", "todo_repository_links_add", args); err != nil {
		t.Fatal(err)
	}
	s.setTaskStage(ctx, todo.ID, "merge", ownDeliveryPR)
	s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: researchNoPRCloseout})
	if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("binding a mirrored PR did not hold acceptance: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[ownDeliveryPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("later stage report lost the bound PR: role=%q", got)
	}
}

func TestStageMirrorDeliveryFallback(t *testing.T) {
	const relatedIssue = "https://github.com/o/r/issues/4"
	for _, name := range []string{"recorded mirror", "historical link"} {
		t.Run(name, func(t *testing.T) {
			s, todo, _ := linkRoleTask(t)
			ctx := t.Context()
			s.setTaskStage(ctx, todo.ID, "review", ownDeliveryPR)
			st := s.store().(*repositorystore.Store)
			if name == "historical link" {
				var revision int64
				if err := st.QueryRow(ctx, `UPDATE todo_repository_sets
					SET repositories = repositories #- '{0,links,0,provenance}'
					WHERE todo_id = $1 RETURNING revision`, todo.ID).Scan(&revision); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := st.ImportTodoRepositoryLinkDetails(ctx, todo.ID, []repository.LinkInput{
				{URL: relatedIssue, Role: repository.LinkRoleReference},
			}, "explicit"); err != nil {
				t.Fatal(err)
			}
			s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: researchNoPRCloseout})
			if _, err := acceptReview(s, todo.ID, nil); err != nil {
				t.Fatalf("a stage mirror blocked non-PR closeout: %v", err)
			}
			if got := todoNow(s, todo.ID); got.DoneAt == nil || got.PRURL != "" {
				t.Fatalf("non-PR closeout left stage delivery: done=%v PR=%q", got.DoneAt, got.PRURL)
			}
			if roles := linkRoles(t, s, todo.ID); len(roles) != 1 || roles[relatedIssue] != repository.LinkRoleReference {
				t.Fatalf("non-PR closeout should leave only the reference issue: %v", roles)
			}
		})
	}
}

func TestForgetReportedPRLinksKeepsBindingInStageHistory(t *testing.T) {
	st := newMemRepoStore()
	const id = "delivery-history"
	st.sets[id] = repository.TaskSet{TodoID: id, Revision: 1, Repositories: []repository.TaskRepository{{
		RepoID: "repo_o_r",
		Links: []repository.TaskLink{
			{CanonicalURL: foreignPR, Kind: "pull_request", Number: "6206", Provenance: repository.LinkProvenanceUnbound},
			{CanonicalURL: ownDeliveryPR, Kind: "pull_request", Number: "21", Provenance: repository.LinkProvenanceExplicit},
		},
	}}}
	s, _ := newQuietServiceOn(t, st)
	// A binding can be saved after acceptance started checking a stage mirror.
	if err := s.forgetReportedPRLinks(t.Context(), id, todoStage{PR: foreignPR, PRs: []string{ownDeliveryPR}}); err == nil {
		t.Fatal("non-PR cleanup deleted a delivery binding in stage history")
	}
	if roles := linkRoles(t, s, id); len(roles) != 2 || roles[ownDeliveryPR] != repository.LinkRoleDelivery {
		t.Fatalf("non-PR cleanup changed the saved links: %v", roles)
	}
}

type multiRepoDeliveryForge struct{ *fakeForge }

func (f multiRepoDeliveryForge) PRMerge(ctx context.Context, repo string, number int) (bool, string, error) {
	if repo == "o/r" && number == 21 {
		return true, mergeSHA, nil
	}
	return f.fakeForge.PRMerge(ctx, repo, number)
}

func TestBoundMultiRepoDeliveryPrecedesSinglePRRecovery(t *testing.T) {
	s, todo, forge := linkRoleTask(t)
	ctx := t.Context()
	const secondPR = "https://github.com/o/client/pull/32"
	links := []string{ownDeliveryPR, secondPR, foreignPR}
	roles := map[string]string{
		ownDeliveryPR: repository.LinkRoleDelivery, secondPR: repository.LinkRoleDelivery, foreignPR: repository.LinkRoleReference,
	}
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, links, "pull_request", nil, roles, nil); err != nil {
		t.Fatal(err)
	}
	// The legacy stage still tracks a reference and the report describes two different deliveries.
	s.setTaskStage(ctx, todo.ID, "review", foreignPR)
	s.hub.(*fakeHub).setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "<closeout status=\"done\" merged=\"yes\" deployed=\"n/a\">\n" +
		"artifact: pr " + ownDeliveryPR + "\nartifact: pr " + secondPR + "\n</closeout>"})
	s.deploy.forge = multiRepoDeliveryForge{forge}
	forge.ownedPRs = map[string]bool{"o/r#21": false, "o/client#32": false}
	if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), secondPR) {
		t.Fatalf("unmerged PR in the second repository should block acceptance: %v", err)
	}
	forge.mu.Lock()
	forge.merged = true
	forge.mu.Unlock()
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("both bound delivery PRs merged: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("task stayed open after both delivery PRs merged")
	}
	for u, role := range roles {
		if got := linkRoles(t, s, todo.ID)[u]; got != role {
			t.Errorf("role for %s = %q, want %q", u, got, role)
		}
	}
}

// When ownership cannot be established, the conservative delivery default stays and the unmerged PR still holds the
// task: a forge outage must never silently drop a delivery requirement.
func TestUnknownOwnershipKeepsDeliveryGate(t *testing.T) {
	s, todo, forge := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{foreignPR}, "pull_request", nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	forge.ownedPRs = map[string]bool{} // no key: unknown ownership
	if _, err := acceptReview(s, todo.ID, nil); err == nil ||
		!strings.Contains(err.Error(), "not merged yet") {
		t.Fatalf("unknown-ownership PR lost the delivery gate: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("unknown-ownership PR role changed to %q", got)
	}
}

// The role is editable through the stored set: flipping reference back to delivery makes the gate hold again.
func TestLinkRoleEditRoundTrip(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinksWithRoles(ctx, todo.ID, []string{foreignPR}, "pull_request", nil, map[string]string{foreignPR: repository.LinkRoleReference}, nil); err != nil {
		t.Fatal(err)
	}
	setTestTodoLinkRole(t, s, todo.ID, foreignPR, repository.LinkRoleDelivery)
	if got := linkRoles(t, s, todo.ID)[foreignPR]; got != repository.LinkRoleDelivery {
		t.Fatalf("role edit did not persist: %q", got)
	}
}

// The real report path: an Agent reply whose prose frames a PR as related work stores it as reference at import,
// while the same reply's own delivery PR keeps the delivery role. Without the prose classifier both land as delivery.
func TestAdvanceTaskReplyClassifiesReferenceProse(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	artifactNodeProof(f, ownDeliveryPR, "contributor", "contributor", "fix/parser", "fix/parser", "abc", "abc")
	reply := "Done.\nOur delivery PR is " + ownDeliveryPR + ".\n" +
		"Related work: " + foreignPR + " tracks the upstream dependency.\n"
	s.advanceTaskReply(context.Background(), todo, "box", todo.Agents[0].SessionID, reply)
	got := linkRoles(t, s, todo.ID)
	if got[foreignPR] != repository.LinkRoleReference {
		t.Fatalf("related-work PR role = %q, want reference", got[foreignPR])
	}
	if got[ownDeliveryPR] != repository.LinkRoleDelivery {
		t.Fatalf("delivery PR role = %q, want delivery", got[ownDeliveryPR])
	}
	if got := todoNow(s, todo.ID).PRURL; got != ownDeliveryPR {
		t.Fatalf("legacy delivery PR = %q, want %q", got, ownDeliveryPR)
	}
}
