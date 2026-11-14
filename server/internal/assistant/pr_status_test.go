package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type statusForge struct {
	fakeForge
	status      TodoPRStatus
	statusErr   error
	statusCalls int
}

func (f *statusForge) PRStatus(context.Context, string, int) (TodoPRStatus, error) {
	f.statusCalls++
	return f.status, f.statusErr
}

func TestPRStatusReadsForgeBlockers(t *testing.T) {
	for _, tc := range []struct {
		name, pr, reviews, statuses, runs string
		want                              TodoPRStatus
	}{
		{
			name:    "ready with review, checks and conflicts",
			pr:      `{"state":"open","draft":false,"mergeable_state":"dirty","head":{"sha":"head"},"requested_reviewers":[{}]}`,
			reviews: `[]`, statuses: `{"state":"success","total_count":1}`,
			runs: `{"total_count":2,"check_runs":[{"status":"completed","conclusion":"failure"},{"status":"in_progress"}]}`,
			want: TodoPRStatus{State: "open", Review: "required", Checks: "failure", Conflicts: true},
		},
		{
			name:     "latest review per reviewer",
			pr:       `{"state":"open","head":{"sha":"head"}}`,
			reviews:  `[{"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED"},{"user":{"login":"reviewer"},"state":"APPROVED"}]`,
			statuses: `{"state":"pending","total_count":0}`,
			runs:     `{"total_count":1,"check_runs":[{"status":"completed","conclusion":"neutral"}]}`,
			want:     TodoPRStatus{State: "open", Review: "approved", Checks: "success"},
		},
		{
			name:     "draft with requested changes",
			pr:       `{"state":"open","draft":true,"head":{"sha":"head"}}`,
			reviews:  `[{"user":{"login":"reviewer"},"state":"CHANGES_REQUESTED"}]`,
			statuses: `{"state":"pending","total_count":1}`, runs: `{"check_runs":[]}`,
			want: TodoPRStatus{State: "open", Draft: true, Review: "changes_requested", Checks: "pending"},
		},
		{
			name: "merged needs no commit endpoints", pr: `{"state":"closed","merged":true}`,
			want: TodoPRStatus{State: "merged", Review: "pending", Checks: "none"},
		},
		{
			name: "closed without merge", pr: `{"state":"closed","merged":false}`,
			want: TodoPRStatus{State: "closed", Review: "pending", Checks: "none"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body string
				switch r.URL.RequestURI() {
				case "/repos/example/app/pulls/12":
					body = tc.pr
				case "/repos/example/app/pulls/12/reviews?per_page=100":
					body = tc.reviews
				case "/repos/example/app/commits/head/status":
					body = tc.statuses
				case "/repos/example/app/commits/head/check-runs?per_page=100&filter=latest":
					body = tc.runs
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.Error(w, "unexpected", http.StatusNotFound)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer srv.Close()
			got, err := (githubForge{base: srv.URL, client: srv.Client()}).PRStatus(context.Background(), "example/app", 12)
			if err != nil || got != tc.want {
				t.Fatalf("PR status = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestPRStatusPollReturnsTerminalPRToSupervision(t *testing.T) {
	for _, terminal := range []string{"merged", "closed"} {
		t.Run(terminal, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			ctx, now := context.Background(), time.Now()
			url := "https://github.com/example/app/pull/12"
			s.setTaskStage(ctx, task.ID, "review", url)
			if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
				t.Fatal(err)
			}
			f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success", Review: "required"}}
			s.deploy.forge = f
			s.sweepPRStatuses(ctx, now)
			got := todoNow(s, task.ID)
			if got.DoneAt != nil || got.Supervision.State != reviewParked || got.PRStatus == nil || got.PRStatus.State != "open" {
				t.Fatalf("ready PR lost its open parked task: %+v", got)
			}
			s.sweepPRStatuses(ctx, now.Add(time.Minute))
			if f.statusCalls != 1 {
				t.Fatalf("polled a fresh observation %d times", f.statusCalls)
			}
			f.status.State = terminal
			s.sweepPRStatuses(ctx, now.Add(deployRecheck))
			got = todoNow(s, task.ID)
			if got.DoneAt != nil || got.PRStatus.State != terminal || got.Supervision.State != reviewPending || got.Supervision.Parked != nil {
				t.Fatalf("terminal PR did not return to review, or auto-closed: %+v, %+v", got, got.Supervision)
			}
			if terminal == "merged" && got.Stage != "merge" {
				t.Fatalf("merged PR stage = %q", got.Stage)
			}
			if terminal == "closed" && got.Stage != "review" {
				t.Fatalf("closed PR advanced as if merged: %q", got.Stage)
			}
			b := &s.supervise
			b.mu.Lock()
			found := false
			for _, signal := range b.pending {
				found = found || signal.Todo == task.ID && signal.Kind == sigReview
			}
			b.mu.Unlock()
			if !found {
				t.Fatal("no supervisor wake was queued")
			}
			raw, _ := json.Marshal(got)
			if !strings.Contains(string(raw), `"pr_status"`) {
				t.Fatal("PR facts not published on Todo")
			}
		})
	}
}

func TestPRStatusFailurePreservesLastObservation(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	s.setTaskStage(ctx, task.ID, "review", "https://github.com/example/app/pull/12")
	f := &statusForge{status: TodoPRStatus{State: "open", Checks: "failure"}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	before := *todoNow(s, task.ID).PRStatus
	f.statusErr = errors.New("forge unavailable")
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got := todoNow(s, task.ID)
	if got.PRStatus == nil || *got.PRStatus != before || got.Supervision.State == reviewPending {
		t.Fatalf("failure replaced facts or triggered acceptance: %+v", got)
	}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck+time.Minute))
	if f.statusCalls != 2 {
		t.Fatalf("an outage bypassed the polling interval: %d calls", f.statusCalls)
	}
}

func TestPRStatusDoesNotWakeAnUnrelatedLaterPark(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	s.setTaskStage(ctx, task.ID, "deploy", "https://github.com/example/app/pull/12")
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	f := &statusForge{status: TodoPRStatus{State: "merged"}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	if got := todoNow(s, task.ID); got.Supervision.State != reviewParked || got.DoneAt != nil {
		t.Fatalf("an already merged task's unrelated park was disturbed: %+v", got)
	}
}

func TestPRStatusPassDoesNotNeedRunningHubCommit(t *testing.T) {
	s, _, task := newSupervisedService(t)
	s.setTaskStage(context.Background(), task.ID, "review", "https://github.com/example/app/pull/12")
	f := &statusForge{status: TodoPRStatus{State: "open", Checks: "success"}}
	s.deploy.forge = f
	s.deploy.hubBuild = func(context.Context) (string, string, error) {
		return "", "", errors.New("hub build unavailable")
	}
	s.sweepDeploys(time.Now())
	if got := todoNow(s, task.ID); got.PRStatus == nil || got.PRStatus.State != "open" || f.statusCalls != 1 {
		t.Fatalf("PR observations depend on the hub's repository/build: calls=%d, facts=%+v", f.statusCalls, got.PRStatus)
	}
}

func TestPRStatusIgnoresAReplacedPR(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	oldURL, newURL := "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	s.setTaskStage(ctx, task.ID, "review", oldURL)
	old := todoNow(s, task.ID)
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	s.setTaskStage(ctx, task.ID, "review", newURL)
	if err := s.applyPRStatus(ctx, old, TodoPRStatus{URL: oldURL, State: "merged", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, task.ID); got.PRStatus != nil || got.Supervision.State != reviewParked || got.PRURL != newURL {
		t.Fatalf("a stale response changed the replacement PR's task: %+v", got)
	}
}

// prsForge answers each PR from its own facts, by number.
type prsForge struct {
	fakeForge
	byNumber map[int]TodoPRStatus
	calls    map[int]int
}

func (f *prsForge) PRStatus(_ context.Context, _ string, n int) (TodoPRStatus, error) {
	if f.calls == nil {
		f.calls = map[int]int{}
	}
	f.calls[n]++
	return f.byNumber[n], nil
}

// Work spanning several PRs (#7935): the poll reads the earlier ones too, the task waits for merge while any of them
// is open, and its supervisor is woken only once the last open one is merged or closed.
func TestPRStatusFollowsEveryPROfTheTask(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	first, second, last := "https://github.com/example/app/pull/11", "https://github.com/example/app/pull/12", "https://github.com/example/app/pull/13"
	for _, u := range []string{first, second, last} {
		s.setTaskStage(ctx, task.ID, "review", u)
	}
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	f := &prsForge{byNumber: map[int]TodoPRStatus{
		11: {State: "merged"},
		12: {State: "open", Review: "required", Checks: "success"},
		13: {State: "merged"},
	}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	got := todoNow(s, task.ID)
	if f.calls[11] != 1 || f.calls[12] != 1 || f.calls[13] != 1 {
		t.Fatalf("not every PR was read: %v", f.calls)
	}
	if open := triageOpenPRs(got, now); len(open) != 1 || open[0].URL != second {
		t.Fatalf("open PRs = %+v (current %+v, others %+v)", open, got.PRStatus, got.OtherPRs)
	}
	if tr := triageTodo(listedTodo{Todo: got}, noSessions, now, time.Time{}, time.UTC, false); tr.Section != triagePR {
		t.Fatalf("a task whose earlier PR is open is not waiting for merge: %+v (review %q, status %q, open %d)", tr, triageReviewState(got), triageStatus(got, noSessions, now), len(triageOpenPRs(got, now)))
	}
	if got.Supervision.State != reviewParked {
		t.Fatalf("woken while a PR is still open: %+v", got.Supervision)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"other_prs"`) {
		t.Fatal("earlier PR facts not published on Todo")
	}
	// The last open one merges: woken now; a merged PR is not polled again.
	f.byNumber[12] = TodoPRStatus{State: "merged"}
	s.sweepPRStatuses(ctx, now.Add(deployRecheck))
	got = todoNow(s, task.ID)
	if got.Supervision.State != reviewPending {
		t.Fatalf("last open PR merged without a wake: %+v", got.Supervision)
	}
	if f.calls[11] != 1 {
		t.Fatalf("a merged earlier PR was polled again: %v", f.calls)
	}
}

// A task held at its PRs whose PRs are all found merged the first time they are read is woken once the last one is
// read, not by the first one while the others are still unknown (#7935).
func TestPRStatusWakesAPRHoldOnlyAfterEveryPRIsRead(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	for _, u := range []string{"https://github.com/example/app/pull/11", "https://github.com/example/app/pull/12"} {
		s.setTaskStage(ctx, task.ID, "review", u)
	}
	if _, err := reviewErr(s, task.ID, waitingPark()); err != nil {
		t.Fatal(err)
	}
	f := &prsForge{byNumber: map[int]TodoPRStatus{11: {State: "merged"}, 12: {State: "merged"}}}
	s.deploy.forge = f
	s.sweepPRStatuses(ctx, now)
	if got := todoNow(s, task.ID); got.Supervision.State != reviewPending {
		t.Fatalf("all PRs merged, not woken: %+v", got.Supervision)
	}
}
