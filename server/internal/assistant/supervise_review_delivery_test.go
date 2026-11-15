package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// deliveryReviewService is a supervised task with a GitHub PR, the hub running oldHub and the forge as given, with
// done_needs_delivery on unless off.
func deliveryReviewService(t *testing.T, forge *fakeForge, hub *runningHub, off bool) (*Service, Todo) {
	t.Helper()
	s, _, todo := newSupervisedService(t)
	s.deploy.forge = forge
	if hub != nil {
		s.deploy.hubBuild = hub.build
	} else { // a hub that does not say its commit: nothing to check the deploy against
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	}
	s.deploy.listPaths = []string{}
	if !off {
		deliveryOn(t, s)
	}
	s.setTaskStage(context.Background(), todo.ID, "review", "https://github.com/o/r/pull/9")
	return s, todo
}

func acceptReview(s *Service, todo string, extra map[string]any) (string, error) {
	args := map[string]any{"todo_id": todo, "verdict": "accepted", "note": "做完了",
		"checks": []ReviewCheck{
			{Criterion: "PR 合并到 master", Met: true, Evidence: "draft PR"},
			{Criterion: "浅色 / 深色截图贴进 PR", Met: true, Evidence: "both screenshots verified"},
		}}
	for k, v := range extra {
		args[k] = v
	}
	raw, _ := json.Marshal(args)
	return s.runTool(context.Background(), "server", "conv1", "task_review", raw)
}

// With done_needs_delivery on, accepted closes a task only once its PR is live (#6428): an unmerged PR, a merge the
// running hub does not contain yet and a check that fails all refuse it, and the to-do stays open; a deployed PR, the
// switch off, a merged PR where the hub cannot be checked against, and outcome cannot are accepted.
func TestAcceptedNeedsTheTasksPRLive(t *testing.T) {
	for _, tc := range []struct {
		name   string
		forge  *fakeForge
		hub    string // "" : the hub does not say its commit
		off    bool
		extra  map[string]any
		refuse string
	}{
		{name: "unmerged", forge: &fakeForge{}, hub: oldHub, refuse: "not merged"},
		{name: "unmerged, unverifiable hub", forge: &fakeForge{}, refuse: "not merged"},
		{name: "merged, not deployed", forge: &fakeForge{merged: true, sha: mergeSHA, contains: map[string]bool{oldHub: false}}, hub: oldHub, refuse: "does not contain it yet"},
		{name: "forge error", forge: &fakeForge{err: errors.New("403")}, hub: oldHub, refuse: "could not be checked"},
		{name: "forge error, unverifiable hub", forge: &fakeForge{err: errors.New("403")}, refuse: "could not be checked"},
		{name: "the review's link names the unmerged PR", forge: &fakeForge{}, hub: oldHub, extra: map[string]any{"link": "https://github.com/o/r/pull/9"}, refuse: "not merged"},
		{name: "deployed", forge: &fakeForge{merged: true, sha: mergeSHA, contains: map[string]bool{oldHub: true}}, hub: oldHub},
		{name: "merged, unverifiable hub", forge: &fakeForge{merged: true, sha: mergeSHA}},
		{name: "switch off", forge: &fakeForge{}, hub: oldHub, off: true},
		{name: "outcome cannot", forge: &fakeForge{}, hub: oldHub, extra: map[string]any{"outcome": "cannot", "reason": "做不了"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hub *runningHub
			if tc.hub != "" {
				hub = &runningHub{commit: tc.hub}
			}
			s, todo := deliveryReviewService(t, tc.forge, hub, tc.off)
			res, err := acceptReview(s, todo.ID, tc.extra)
			got := todoNow(s, todo.ID)
			if tc.refuse == "" {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if got.DoneAt == nil {
					t.Fatalf("not closed: %s", res)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refuse) || !strings.Contains(err.Error(), "external_event") {
				t.Fatalf("accepted (%v): %s", err, res)
			}
			if got.DoneAt != nil {
				t.Fatalf("closed at %v", got.DoneAt)
			}
			if v := got.Supervision; v != nil && v.State == reviewAccepted {
				t.Fatalf("the ledger says accepted: %+v", v)
			}
		})
	}
}

func TestAcceptedRemovesConfirmedNonPR(t *testing.T) {
	for _, hub := range []string{"", oldHub} {
		for _, kind := range []string{"issue", "missing", "cached outage"} {
			t.Run(kind+"/"+hub, func(t *testing.T) {
				var running *runningHub
				if hub != "" {
					running = &runningHub{commit: hub}
				}
				s, todo := deliveryReviewService(t, &fakeForge{}, running, false)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/repos/o/r/issues/9":
						if kind == "missing" {
							http.NotFound(w, r)
						} else {
							_, _ = w.Write([]byte(`{"number":9}`))
						}
					case "/repos/o/r/pulls":
						_, _ = w.Write([]byte(`[]`))
					default:
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
				ctx := context.Background()
				s.watchDeploy(ctx, todo.ID, deployWatch{PR: "https://github.com/o/r/pull/9", Since: time.Now()})
				s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged})
				if kind == "cached outage" {
					s.deploy.githubDown = time.Now().Add(time.Hour)
				}
				if res, err := acceptReview(s, todo.ID, map[string]any{"link": "https://github.com/o/r/issues/9"}); err != nil {
					t.Fatalf("confirmed non-PR still blocked acceptance: %v (%s)", err, res)
				}
				got := todoNow(s, todo.ID)
				if got.DoneAt == nil || got.PRURL != "" || len(got.PRs) != 0 || got.Supervision.State != reviewAccepted {
					t.Fatalf("not accepted without the false PR: %+v", got)
				}
				stages := s.todoStages(ctx)
				if st := stages[todo.ID]; st.PR != "" || len(st.PRs) != 0 || st.Deploy != nil {
					t.Fatalf("false PR survived in stage storage: %+v", st)
				}
			})
		}
	}
}

func TestAcceptedDoesNotDiscardUnconfirmedPR(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		repo   int
	}{
		{"private repository 404", 404, "", 404},
		{"PR access denied despite visible repository", 404, "", 403},
		{"PR access response not authoritative", 404, `{}`, 200},
		{"PR metadata still exists", 200, `{"pull_request":{}}`, 0},
		{"authentication required", 401, "", 0},
		{"forbidden", 403, "", 0},
		{"invalid request", 422, "", 0},
		{"rate limited", 429, "", 0},
		{"upstream failure", 503, "", 0},
		{"invalid JSON", 200, `{`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo := deliveryReviewService(t, &fakeForge{}, &runningHub{commit: oldHub}, false)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				status, body := 404, ""
				if r.URL.Path == "/repos/o/r/issues/9" {
					status, body = tc.status, tc.body
				} else if r.URL.Path == "/repos/o/r/pulls" {
					status, body = tc.repo, tc.body
				} else if r.URL.Path == "/repos/o/r" {
					status, body = http.StatusOK, `{"full_name":"o/r"}`
				}
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
			if _, err := acceptReview(s, todo.ID, nil); err == nil {
				t.Fatal("accepted an unconfirmed PR")
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt != nil || got.PRURL != "https://github.com/o/r/pull/9" {
				t.Fatalf("unconfirmed PR was discarded: %+v", got)
			}
		})
	}
}

func TestAcceptedNonPRCannotHideEarlierRealPR(t *testing.T) {
	s, todo := deliveryReviewService(t, &fakeForge{}, &runningHub{commit: oldHub}, false)
	ctx := context.Background()
	s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/10")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/issues/10":
			_, _ = w.Write([]byte(`{"number":10}`))
		case "/repos/o/r/pulls/9":
			_, _ = w.Write([]byte(`{"merged":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	s.deploy.forge = githubForge{base: server.URL, client: server.Client()}
	if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("earlier unmerged PR was not checked: %v", err)
	}
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.PRURL != "https://github.com/o/r/pull/9" || slices.Contains(got.PRs, "https://github.com/o/r/pull/10") {
		t.Fatalf("incorrect PR history after recovery: %+v", got)
	}
	if !s.deploy.githubDown.IsZero() {
		t.Fatal("a non-PR 404 put unrelated PR verification on cooldown")
	}
}

func TestForgetTaskPRKeepsStateOnWriteFailure(t *testing.T) {
	st := &memStore{}
	s, _ := newTaskServiceOn(t, st)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Research findings", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	pr := "https://github.com/o/r/pull/9"
	s.setTaskStage(ctx, todo.ID, "review", pr)
	st.mu.Lock()
	st.failPut = errors.New("database unavailable")
	st.mu.Unlock()
	if err := s.forgetTaskPR(ctx, todo.ID, pr, "https://github.com/o/r/pull/8"); err == nil {
		t.Fatal("non-PR removal succeeded without persisting it")
	}
	st.mu.Lock()
	st.failPut = nil
	st.mu.Unlock()
	if got := todoNow(s, todo.ID); got.PRURL != pr {
		t.Fatalf("in-memory task changed on failed write: %+v", got)
	}
	if got := s.todoStages(ctx)[todo.ID]; got.PR != pr {
		t.Fatalf("stage book changed on failed write: %+v", got)
	}
}

func TestForgetTaskPRPersistsRemoval(t *testing.T) {
	st := &memStore{}
	s, _ := newTaskServiceOn(t, st)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Research findings", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	pr := "https://github.com/o/r/pull/9"
	real := "https://github.com/o/r/pull/8"
	s.setTaskStage(ctx, todo.ID, "review", pr)
	s.setTaskStage(ctx, todo.ID, "review", real)
	s.setTaskStage(ctx, todo.ID, "review", pr)
	s.watchDeploy(ctx, todo.ID, deployWatch{PR: pr, Since: time.Now()})
	if err := s.forgetTaskPR(ctx, todo.ID, pr, real); err != nil {
		t.Fatal(err)
	}
	restarted, _ := newTaskServiceOn(t, st)
	got := restarted.todoStages(ctx)[todo.ID]
	if got.PR != real || slices.Contains(got.PRs, pr) || got.Deploy != nil {
		t.Fatalf("non-PR removal did not survive restart: %+v", got)
	}
	if current := todoNow(s, todo.ID); current.PRURL != real || slices.Contains(current.PRs, pr) {
		t.Fatalf("task disagrees with persisted stages: %+v", current)
	}
}
