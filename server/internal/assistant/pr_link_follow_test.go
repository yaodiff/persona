package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

// newLinkedDeliveryTask is a supervised task whose stage names no PR: its delivery PRs are only repository links.
func newLinkedDeliveryTask(t *testing.T, links ...repository.TaskLink) (*Service, *fakeHub, *todoReadyRepoStore, Todo) {
	t.Helper()
	st := &todoReadyRepoStore{memStore: &memStore{}}
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	task, err := s.createTodo(ctx, "Fix the approval reminder", "Ship the reminder fix.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"The fix is merged and running."}, 0)
	st.setRepo(task.ID, repository.TaskSet{TodoID: task.ID, Repositories: []repository.TaskRepository{{Links: links}}})
	return s, f, st, todoNow(s, task.ID)
}

// A delivery PR that is only a repository link (the stage named a branch, not the PR) is polled like a stage PR; a
// reference link is not (#14049).
func TestLinkedDeliveryPRsArePolled(t *testing.T) {
	const delivery, unset, ref = "https://github.com/example/app/pull/21", "https://github.com/example/app/pull/22", "https://github.com/example/app/pull/99"
	s, _, _, task := newLinkedDeliveryTask(t,
		repository.TaskLink{Kind: "pull_request", CanonicalURL: delivery, Role: repository.LinkRoleDelivery},
		repository.TaskLink{Kind: "pull_request", CanonicalURL: unset},
		repository.TaskLink{Kind: "pull_request", CanonicalURL: ref, Role: repository.LinkRoleReference},
		repository.TaskLink{Kind: "issue", CanonicalURL: "https://github.com/example/app/issues/20"})
	if task.PRURL != "" || len(task.PRs) != 0 {
		t.Fatalf("premise: the stage names a PR: %q %v", task.PRURL, task.PRs)
	}
	forge := &prsForge{byNumber: map[int]TodoPRStatus{21: {State: "open", HeadOID: "a"}, 22: {State: "open", HeadOID: "b"}, 99: {State: "open"}}}
	s.deploy.forge = forge
	s.sweepPRStatuses(context.Background(), time.Now())
	if forge.calls[21] != 1 || forge.calls[22] != 1 || forge.calls[99] != 0 {
		t.Fatalf("polled = %v; want the two delivery links once each and never the reference", forge.calls)
	}
	got := todoNow(s, task.ID)
	if !slices.Contains(got.PRs, delivery) || !slices.Contains(got.PRs, unset) || slices.Contains(got.PRs, ref) {
		t.Fatalf("followed PRs = %v", got.PRs)
	}
	if f, ok := todoObservedPRState(got, delivery); !ok || f.State != "open" {
		t.Fatalf("no facts kept for the linked PR: %+v %v", f, ok)
	}
}

// A task parked on something else (here: the design team) whose delivery PRs all finish while it waits, one merged
// and the last closed, wakes its supervisor once; the park stays, and the message names what it waits for (#14049).
func TestParkedTaskIsWokenWhenItsDeliveryPRsFinish(t *testing.T) {
	const first, second = "https://github.com/example/app/pull/21", "https://github.com/example/app/pull/22"
	s, f, _, task := newLinkedDeliveryTask(t,
		repository.TaskLink{Kind: "pull_request", CanonicalURL: first, Role: repository.LinkRoleDelivery},
		repository.TaskLink{Kind: "pull_request", CanonicalURL: second, Role: repository.LinkRoleDelivery})
	ctx, now := context.Background(), time.Now()
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	forge := &prsForge{byNumber: map[int]TodoPRStatus{21: {State: "open", HeadOID: "a"}, 22: {State: "open", HeadOID: "b"}}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	forge.byNumber[21] = TodoPRStatus{State: "merged", HeadOID: "a"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, first); ok {
		t.Fatal("woken while the other delivery PR was still open")
	}
	forge.byNumber[22] = TodoPRStatus{State: "closed", HeadOID: "b"}
	s.sweepPRStatuses(ctx, now.Add(2*deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, first); !ok {
		t.Fatalf("not woken once every delivery PR finished: %v", pendingKinds(s, task.ID))
	}
	if got := todoNow(s, task.ID).Supervision.State; got != reviewParked {
		t.Fatalf("the unrelated park was ended: %q", got)
	}
	sendPendingPRConflicts(s)
	notices := f.notices()
	if len(notices) != 1 {
		t.Fatalf("%d notices, want 1: %q", len(notices), notices)
	}
	for _, want := range []string{"signal=" + sigPRMerged, first, "The design team has to pick the icon set first"} {
		if !strings.Contains(notices[0], want) {
			t.Errorf("the wake lacks %q:\n%s", want, notices[0])
		}
	}
	// Once only: later sweeps, and a restart that forgets what was sent, do not wake it again.
	forgetSignals(t, s)
	for i := 3; i < 6; i++ {
		s.sweepPRStatuses(ctx, now.Add(time.Duration(i)*deployRecheck))
	}
	if _, ok := pendingPRMerged(s, task.ID, first); ok {
		t.Fatal("woken a second time")
	}
}

// A PR closed unmerged that ends a park names the task's other linked PRs, so its supervisor can tell a continuation
// (a maintainer's follow-up PR) from abandoned work (#14049).
func TestClosedPRWakeNamesLinkedSuccessors(t *testing.T) {
	st := &todoReadyRepoStore{memStore: &memStore{}}
	s, _, task := newPRConflictTask(t, st)
	const next = "https://github.com/example/app/pull/31"
	st.setRepo(task.ID, repository.TaskSet{TodoID: task.ID, Repositories: []repository.TaskRepository{{Links: []repository.TaskLink{
		{Kind: "pull_request", CanonicalURL: task.PRURL, Role: repository.LinkRoleDelivery},
		{Kind: "pull_request", CanonicalURL: next, Role: repository.LinkRoleReference},
	}}}})
	ctx, now := context.Background(), time.Now()
	if _, err := reviewErr(s, task.ID, mergePark(task.PRURL)); err != nil {
		t.Fatal(err)
	}
	forge := &prsForge{byNumber: map[int]TodoPRStatus{12: {State: "open", HeadOID: "h"}}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	forge.byNumber[12] = TodoPRStatus{State: "closed", HeadOID: "h"}
	s.sweepPRStatuses(ctx, now.Add(2*deployRecheck)) // the first read is dated at the park, after now
	b := &s.supervise
	b.mu.Lock()
	g, ok := b.pending[(superviseSignal{Todo: task.ID, Kind: sigReview}).id()]
	b.mu.Unlock()
	if !ok {
		t.Fatalf("the closed PR did not wake the park: %v", pendingKinds(s, task.ID))
	}
	if !strings.Contains(g.Detail, "PR closed: "+task.PRURL) || !strings.Contains(g.Detail, "may continue it: "+next) {
		t.Fatalf("detail = %q", g.Detail)
	}
}

// erringPRsForge fails the reads of the PR numbers in fail, like a rate-limited or unreachable host.
type erringPRsForge struct {
	prsForge
	fail map[int]bool
}

func (f *erringPRsForge) PRStatus(ctx context.Context, repo string, n int) (TodoPRStatus, error) {
	if f.fail[n] {
		return TodoPRStatus{}, errors.New("rate limited")
	}
	return f.prsForge.PRStatus(ctx, repo, n)
}

// The last delivery PR first read closed (its earlier reads failed) completes the delivery after another merged: the
// wake for the merged one goes out although the closed PR was never seen open (#14049 review).
func TestLastPRFirstReadClosedWakesForTheMergedOne(t *testing.T) {
	const first, second = "https://github.com/example/app/pull/21", "https://github.com/example/app/pull/22"
	s, _, _, task := newLinkedDeliveryTask(t,
		repository.TaskLink{Kind: "pull_request", CanonicalURL: first, Role: repository.LinkRoleDelivery},
		repository.TaskLink{Kind: "pull_request", CanonicalURL: second, Role: repository.LinkRoleDelivery})
	ctx, now := context.Background(), time.Now()
	forge := &erringPRsForge{prsForge: prsForge{byNumber: map[int]TodoPRStatus{21: {State: "open", HeadOID: "a"}}}, fail: map[int]bool{22: true}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	forge.byNumber[21] = TodoPRStatus{State: "merged", HeadOID: "a"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, first); ok {
		t.Fatal("woken while the other delivery PR was unread")
	}
	forge.fail[22] = false
	forge.byNumber[22] = TodoPRStatus{State: "closed", HeadOID: "b"}
	s.sweepPRStatuses(ctx, now.Add(2*deployRecheck))
	if forge.calls[22] == 0 {
		t.Fatal("premise: the second PR was never read")
	}
	if _, ok := pendingPRMerged(s, task.ID, first); !ok {
		t.Fatalf("no wake once the last PR was first read closed: %v (state %q)", pendingKinds(s, task.ID), todoNow(s, task.ID).Supervision.State)
	}
}

// A task parked on a handoff waits on another task, not on its delivery PR: it hears that the PR merged like any other
// park, and stays parked (#14049 review).
func TestHandoffParkHearsItsDeliveryPRMerged(t *testing.T) {
	const pr = "https://github.com/example/app/pull/21"
	s, _, _, task := newLinkedDeliveryTask(t, repository.TaskLink{Kind: "pull_request", CanonicalURL: pr, Role: repository.LinkRoleDelivery})
	ctx, now := context.Background(), time.Now()
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		r := &v.Reviews[len(v.Reviews)-1]
		r.WaitingOn, r.Evidence = parkTodoHandoff, nil
		r.Handoff = &taskHandoff{ID: "handoff-1", Machine: "box", Session: "s2"}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	forge := &prsForge{byNumber: map[int]TodoPRStatus{21: {State: "open", HeadOID: "a"}}}
	s.deploy.forge = forge
	s.sweepPRStatuses(ctx, now)
	forge.byNumber[21] = TodoPRStatus{State: "merged", HeadOID: "a"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	if _, ok := pendingPRMerged(s, task.ID, pr); !ok {
		t.Fatalf("a handoff park did not hear its delivery PR merged: %v", pendingKinds(s, task.ID))
	}
	if p := todoNow(s, task.ID).Supervision.parking(); p == nil || p.Handoff == nil {
		t.Fatal("the handoff park was ended")
	}
}

// A parked task whose only delivery PR is a repository link that is already merged when first read (CaC033's review).
func TestParkedLinkedPRFirstReadMerged(t *testing.T) {
	const first = "https://github.com/example/app/pull/21"
	s, _, _, task := newLinkedDeliveryTask(t,
		repository.TaskLink{Kind: "pull_request", CanonicalURL: first, Role: repository.LinkRoleDelivery})
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{21: {State: "merged", HeadOID: "a"}}}
	s.sweepPRStatuses(context.Background(), time.Now())
	if _, ok := pendingPRMerged(s, task.ID, first); !ok {
		t.Fatalf("no wake for a linked PR first read merged while parked: %v", pendingKinds(s, task.ID))
	}
}

// Links are read at most once per deployRecheck: a link added a minute later is followed only after it (CaC033's review).
func TestLinksReadOncePerRecheck(t *testing.T) {
	const a, b = "https://github.com/example/app/pull/21", "https://github.com/example/app/pull/22"
	s, _, st, task := newLinkedDeliveryTask(t,
		repository.TaskLink{Kind: "pull_request", CanonicalURL: a, Role: repository.LinkRoleDelivery})
	s.deploy.forge = &prsForge{byNumber: map[int]TodoPRStatus{21: {State: "open", HeadOID: "a"}, 22: {State: "open", HeadOID: "b"}}}
	now := time.Now()
	s.sweepPRStatuses(context.Background(), now)
	st.setRepo(task.ID, repository.TaskSet{TodoID: task.ID, Repositories: []repository.TaskRepository{{Links: []repository.TaskLink{
		{Kind: "pull_request", CanonicalURL: a, Role: repository.LinkRoleDelivery},
		{Kind: "pull_request", CanonicalURL: b, Role: repository.LinkRoleDelivery}}}}})
	s.sweepPRStatuses(context.Background(), now.Add(time.Minute))
	if got := todoNow(s, task.ID).PRs; slices.Contains(got, b) {
		t.Fatalf("links read again within deployRecheck: %v", got)
	}
	s.sweepPRStatuses(context.Background(), now.Add(deployRecheck+time.Minute))
	if got := todoNow(s, task.ID).PRs; !slices.Contains(got, b) {
		t.Fatalf("new link not followed after deployRecheck: %v", got)
	}
}
