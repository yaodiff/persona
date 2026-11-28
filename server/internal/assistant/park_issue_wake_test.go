package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

const parkedIssue = "https://github.com/example/app/issues/40"

// A park whose evidence is a GitHub issue is woken when the issue closes, once, and not while it is open (#14050).
func TestParkOnAnIssueIsWokenWhenItCloses(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	now := time.Now()
	evidence := testParkEvidence("external_event", "the maintainers", parkedIssue, "The maintainers close the issue")
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "parked", "reason": "Waiting for the maintainers to close the issue",
		"waiting_on": parkExternalEvent, "until": "7d", "evidence": evidence}); err != nil {
		t.Fatal(err)
	}
	forge := &fakeForge{issueState: "open"}
	s.deploy.forge = forge
	s.sweepPRStatusesBounded(now)
	cur := todoNow(s, todo.ID)
	if cur.Supervision.State != reviewParked || pendingReviewSignal(s, todo.ID) {
		t.Fatalf("an open issue woke the park: state=%q signals=%v", cur.Supervision.State, pendingKinds(s, todo.ID))
	}
	if o := cur.Supervision.parking().ExternalIssue; o == nil || o.URL != parkedIssue || o.State != "open" {
		t.Fatalf("the issue's read was not kept on the park: %+v", o)
	}
	// Not read again within deployRecheck.
	forge.issueState = "closed"
	s.sweepPRStatusesBounded(now.Add(time.Minute))
	if todoNow(s, todo.ID).Supervision.State != reviewParked {
		t.Fatal("read again before deployRecheck")
	}
	s.sweepPRStatusesBounded(now.Add(deployRecheck))
	if got := todoNow(s, todo.ID).Supervision.State; got != reviewPending {
		t.Fatalf("state = %q after the issue closed, want pending_review", got)
	}
	b := &s.supervise
	b.mu.Lock()
	g, ok := b.pending[(superviseSignal{Todo: todo.ID, Kind: sigReview}).id()]
	b.mu.Unlock()
	if !ok || !strings.Contains(g.Detail, "issue this park waits on is closed: "+parkedIssue) {
		t.Fatalf("review signal = %+v (%v)", g, ok)
	}
}

// An open linked issue refuses acceptance with the ways out named; set to reference, it no longer blocks (#14050).
func TestOpenIssueRefusalNamesTheWaysOut(t *testing.T) {
	s, todo, _ := linkRoleTask(t)
	ctx := context.Background()
	if _, err := s.importReportedLinks(ctx, todo.ID, []string{parkedIssue}, "issue", nil, nil); err != nil {
		t.Fatal(err)
	}
	_, err := acceptReview(s, todo.ID, nil)
	if err == nil {
		t.Fatal("an open delivery issue was accepted")
	}
	for _, want := range []string{"is still open", "ask its maintainer to close it", "role to reference", "todo_repository_links_set"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
	setTestTodoLinkRole(t, s, todo.ID, parkedIssue, repository.LinkRoleReference)
	if got := linkRoles(t, s, todo.ID)[parkedIssue]; got != repository.LinkRoleReference {
		t.Fatalf("issue role = %q", got)
	}
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("a reference issue blocked acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("the task stayed open")
	}
}

// Only an evidence URL that is itself a GitHub issue enrolls the park: an issue quoted inside another URL, a longer
// path or a number too large to read does not (#14050 review).
func TestParkIssueURLIsTheEvidenceItself(t *testing.T) {
	for raw, want := range map[string]string{
		"https://github.com/o/r/issues/40":                                 "https://github.com/o/r/issues/40",
		"https://github.com/o/r/issues/40/":                                "https://github.com/o/r/issues/40",
		"https://github.com/o/r/issues/40#issuecomment-1":                  "https://github.com/o/r/issues/40",
		"https://www.github.com/o/r/issues/40?x=1":                         "https://github.com/o/r/issues/40",
		"https://example.com/wait?source=https://github.com/o/r/issues/40": "",
		"https://github.com/o/r/issues/40/events":                          "",
		"https://github.com/o/r/pull/40":                                   "",
		"https://github.com/o/r/issues/99999999999999999999999":            "",
		"see https://github.com/o/r/issues/40":                             "",
	} {
		r := &TaskReview{WaitingOn: parkExternalEvent, Evidence: &ParkEvidence{URL: raw}}
		if got := parkIssueURL(r); got != want {
			t.Errorf("parkIssueURL(%q) = %q, want %q", raw, got, want)
		}
	}
}
