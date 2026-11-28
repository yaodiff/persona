package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// deliveryReviewService is a supervised task with a GitHub PR, the hub running oldHub and the forge as given, with
// delivery_gate=live (legacy done_needs_delivery=true) unless off (delivery_gate=off).
func deliveryReviewService(t *testing.T, forge *fakeForge, hub *runningHub, off bool) (*Service, Todo) {
	t.Helper()
	return deliveryReviewServiceOn(t, forge, hub, off, nil)
}

func deliveryReviewServiceOn(t *testing.T, forge *fakeForge, hub *runningHub, off bool, st Store) (*Service, Todo) {
	t.Helper()
	s, _, todo := newSupervisedServiceOn(t, st)
	s.deploy.forge = forge
	if hub != nil {
		s.deploy.hubBuild = hub.build
	} else { // a hub that does not say its commit: nothing to check the deploy against
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	}
	s.deploy.listPaths = []string{}
	if off {
		setDeliveryGate(t, s, deliveryGateOff)
	} else {
		deliveryOn(t, s)
	}
	s.setTaskStage(context.Background(), todo.ID, "review", "https://github.com/o/r/pull/9")
	return s, todo
}

func setDeliveryGate(t *testing.T, s *Service, gate string) {
	t.Helper()
	if _, err := s.updateSettings(context.Background(), func(v *Settings) error {
		v.DeliveryGate = &gate
		v.DoneNeedsDelivery = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryGateContractPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, completion, global, title, refusal string
		merged, live                             bool
	}{
		{name: "draft skips live", completion: "draft", global: deliveryGateLive},
		{name: "ready skips live", completion: "ready", global: deliveryGateLive},
		{name: "landed requires merge even with global off", completion: "landed", global: deliveryGateOff, title: "Do not stop at PR", refusal: "not merged"},
		{name: "verified requires a live build", completion: "verified", global: deliveryGateOff, merged: true, refusal: "not live"},
		{name: "verified accepts a live build", completion: "verified", global: deliveryGateOff, merged: true, live: true},
		{name: "inherit uses the global gate without guessing", completion: "inherit", global: deliveryGateMerged, title: "Stop at the draft PR", refusal: "not merged"},
		{name: "legacy PR-only scope still works", global: deliveryGateMerged, title: "Stop at the draft PR"},
		{name: "legacy scope after a negated clause", global: deliveryGateMerged, title: "Do not deploy, stop at the draft PR"},
		{name: "legacy negation does not opt out", global: deliveryGateMerged, title: "Do not stop at PR", refusal: "not merged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, _ := newTaskService(t)
			title := tc.title
			if title == "" {
				title = "Deliver the artifact"
			}
			todo := startPlanTask(t, s, planTaskArgs(false, title, title, "The artifact is reviewed"))
			s.deploy.forge = &fakeForge{merged: tc.merged, sha: mergeSHA, contains: map[string]bool{oldHub: tc.live}}
			s.deploy.hubBuild = (&runningHub{commit: oldHub}).build
			s.deploy.listPaths = []string{}
			s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/9")
			setDeliveryGate(t, s, tc.global)
			if tc.completion != "" {
				if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
					"todo_id": todo.ID, "change": "Set the delivery milestone", "why": "Updated task scope",
					"completion_mode": tc.completion,
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := s.acceptedDeliveryError(ctx, todoNow(s, todo.ID), "", "")
			if tc.refusal == "" && err != nil {
				t.Fatalf("delivery was refused: %v", err)
			}
			if tc.refusal != "" && (err == nil || !strings.Contains(err.Error(), tc.refusal)) {
				t.Fatalf("delivery error = %v, want %q", err, tc.refusal)
			}
		})
	}
}

type scopeChangeForge struct {
	*fakeForge
	change func()
}

func (f scopeChangeForge) PRMerge(ctx context.Context, repo string, number int) (bool, string, error) {
	f.change()
	return f.fakeForge.PRMerge(ctx, repo, number)
}

func TestDeliveryGateRetriesWhenCompletionModeChangesDuringVerification(t *testing.T) {
	ctx := t.Context()
	forge := &fakeForge{merged: true, sha: mergeSHA}
	s, todo := deliveryReviewService(t, forge, nil, false)
	setMode := func(mode string) {
		if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
			"todo_id": todo.ID, "change": "Update the delivery milestone", "why": "The scope changed",
			"completion_mode": mode,
		}); err != nil {
			t.Fatal(err)
		}
	}
	setMode("landed")
	s.deploy.forge = scopeChangeForge{forge, func() { setMode("verified") }}
	if _, err := s.acceptedDeliveryError(ctx, todoNow(s, todo.ID), "", ""); err == nil || !strings.Contains(err.Error(), "changed its completion mode") {
		t.Fatalf("review used stale delivery checks: %v", err)
	}
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
		{name: "forge error", forge: &fakeForge{err: errors.New("403")}, hub: oldHub, refuse: "交付状态暂时无法验证"},
		{name: "forge error, unverifiable hub", forge: &fakeForge{err: errors.New("403")}, refuse: "交付状态暂时无法验证"},
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
			if err == nil || !strings.Contains(err.Error(), tc.refuse) || (tc.forge.err == nil && !strings.Contains(err.Error(), "external_event")) {
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

func TestAcceptedUnavailableDoesNotCallWorkUnfinished(t *testing.T) {
	s, todo := deliveryReviewService(t, &fakeForge{err: errors.New("verification unavailable")}, &runningHub{commit: oldHub}, false)
	_, err := acceptReview(s, todo.ID, nil)
	if err == nil || !strings.Contains(err.Error(), "交付状态暂时无法验证") {
		t.Fatalf("missing unavailable-verification guidance: %v", err)
	}
	for _, old := range []string{"is not live", "agent_send", "external_event", "outcome cannot"} {
		if strings.Contains(err.Error(), old) {
			t.Fatalf("unfinished-work guidance %q remains: %v", old, err)
		}
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil {
		t.Fatalf("unavailable verification closed the task: %+v", got)
	}
	t.Logf("unavailable verification refusal: %v", err)
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
	s, todo := deliveryReviewServiceOn(t, &fakeForge{}, &runningHub{commit: oldHub}, false, repositorystore.New(todoTestDB(t)))
	ctx := context.Background()
	s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/10")
	s.hub.(*fakeHub).setItems(append(artifactCreationItems(todo, "https://github.com/o/r/pull/9"), proto.Item{Role: "assistant", Kind: proto.KindText, TS: artifactTurnAt(todo),
		Text: "<closeout status=\"not_done\" merged=\"no\" deployed=\"no\">\n" +
			"artifact: pr https://github.com/o/r/pull/9\n</closeout>"})...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/issues/10":
			_, _ = w.Write([]byte(`{"number":10}`))
		case "/repos/o/r/issues/9":
			_, _ = w.Write([]byte(`{"pull_request":{}}`))
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

// Default delivery_gate=merged blocks unmerged PRs (guiding park) and allows merged PRs even before hub deployment.
func TestDefaultMergedDeliveryGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		forge  *fakeForge
		hub    string
		extra  map[string]any
		refuse string
	}{
		{name: "unmerged PR refused with park guidance", forge: &fakeForge{}, hub: oldHub, refuse: "not merged"},
		{name: "merged PR accepted even when running hub does not contain it yet",
			forge: &fakeForge{merged: true, sha: mergeSHA, contains: map[string]bool{oldHub: false}}, hub: oldHub},
		{name: "forge error handled gracefully", forge: &fakeForge{err: errors.New("429 rate limited")}, hub: oldHub, refuse: "交付状态暂时无法验证"},
		{name: "outcome cannot bypasses gate", forge: &fakeForge{}, hub: oldHub, extra: map[string]any{"outcome": "cannot", "reason": "做不了"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, todo := newSupervisedService(t)
			s.deploy.forge = tc.forge
			s.deploy.hubBuild = (&runningHub{commit: tc.hub}).build
			s.deploy.listPaths = []string{}
			if got := s.deliveryGate(context.Background()); got != deliveryGateMerged {
				t.Fatalf("default delivery_gate = %q, want %q", got, deliveryGateMerged)
			}
			s.setTaskStage(context.Background(), todo.ID, "review", "https://github.com/o/r/pull/9")
			res, err := acceptReview(s, todo.ID, tc.extra)
			got := todoNow(s, todo.ID)
			if tc.refuse == "" {
				if err != nil {
					t.Fatalf("refused under default merged gate: %v", err)
				}
				if got.DoneAt == nil {
					t.Fatalf("not closed: %s", res)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refuse) {
				t.Fatalf("err = %v, want substring %q (res=%s)", err, tc.refuse, res)
			}
			if tc.forge.err == nil && (!strings.Contains(err.Error(), "external_event") || !strings.Contains(err.Error(), "someone_else") || !strings.Contains(err.Error(), "pr_event=terminal")) {
				t.Fatalf("missing park guidance: %v", err)
			}
			if got.DoneAt != nil {
				t.Fatalf("closed prematurely: %v", got.DoneAt)
			}
		})
	}
}

// memRepoStore adds in-memory todoRepositoryStore and task_plan/feed support to memStore so repository-linked
// delivery gate tests run without requiring PostgreSQL.
type memRepoStore struct {
	*memStore
	sets         map[string]repository.TaskSet
	plans        map[string]db.TaskPlanRow
	todoReposErr error
}

func newMemRepoStore() *memRepoStore {
	return &memRepoStore{memStore: &memStore{}, sets: map[string]repository.TaskSet{}, plans: map[string]db.TaskPlanRow{}}
}

func (m *memRepoStore) CreateTaskPlan(_ context.Context, row db.TaskPlanRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plans[row.ID] = row
	return nil
}

func (m *memRepoStore) TaskPlan(_ context.Context, id string) (*db.TaskPlanRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.plans[id]
	if !ok {
		return nil, nil
	}
	cp := row
	return &cp, nil
}

func (m *memRepoStore) TaskPlans(_ context.Context, conv string) ([]db.TaskPlanRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.TaskPlanRow
	for _, row := range m.plans {
		if conv == "" || row.Conversation == conv {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memRepoStore) TaskPlansByID(_ context.Context, ids []string) ([]db.TaskPlanRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []db.TaskPlanRow
	for _, id := range ids {
		if row, ok := m.plans[id]; ok {
			out = append(out, row)
		}
	}
	return out, nil
}

func (m *memRepoStore) UpdateTaskPlan(_ context.Context, id, from, to string, body json.RawMessage, at time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.plans[id]
	if !ok || (from != "" && row.State != from) {
		return false, nil
	}
	row.State, row.Tasks, row.UpdatedAt = to, body, at
	m.plans[id] = row
	return true, nil
}

func (m *memRepoStore) AppendFeed(context.Context, db.FeedRow) error { return nil }
func (m *memRepoStore) Feed(context.Context, string, time.Time, int) ([]db.FeedRow, error) {
	return nil, nil
}
func (m *memRepoStore) FeedItem(context.Context, string) (*db.FeedRow, error) { return nil, nil }
func (m *memRepoStore) MarkFeedSent(context.Context, string, string, time.Time, json.RawMessage) ([]db.FeedRow, error) {
	return nil, nil
}
func (m *memRepoStore) FeedByRef(context.Context, string, []string, int) ([]db.FeedRow, error) {
	return nil, nil
}

func (m *memRepoStore) TodoRepositories(_ context.Context, id string) (repository.TaskSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.todoReposErr != nil {
		return repository.TaskSet{}, m.todoReposErr
	}
	if set, ok := m.sets[id]; ok {
		return set, nil
	}
	return repository.TaskSet{SchemaVersion: 1, TodoID: id, Repositories: []repository.TaskRepository{}}, nil
}

func (m *memRepoStore) CreateTodoWithRepositories(context.Context, db.TodoRow, repository.CreateBindings) error {
	return nil
}

func (m *memRepoStore) SetTodoRepositories(_ context.Context, id string, in repository.SetInput) (repository.TaskSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.sets[id]
	cur.SchemaVersion, cur.TodoID, cur.Revision = 1, id, cur.Revision+1
	cur.Repositories = make([]repository.TaskRepository, 0, len(in.Repositories))
	for _, r := range in.Repositories {
		row := repository.TaskRepository{RepoID: r.RepoID}
		for _, l := range r.Links {
			if l.Resolved != nil {
				row.Links = append(row.Links, l.Resolved.Link)
			} else if _, link, err := repository.ParseTaskLinkInput(l); err == nil {
				row.Links = append(row.Links, link)
			}
		}
		cur.Repositories = append(cur.Repositories, row)
	}
	m.sets[id] = cur
	return cur, nil
}

func (m *memRepoStore) SetTodoRepositoryLinks(_ context.Context, id, repoID string, in repository.LinksInput) (repository.TaskSet, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur := m.sets[id]
	cur.SchemaVersion, cur.TodoID, cur.Revision = 1, id, cur.Revision+1
	var links []repository.TaskLink
	for _, l := range in.Links {
		if l.Resolved != nil {
			links = append(links, l.Resolved.Link)
		} else if _, link, err := repository.ParseTaskLinkInput(l); err == nil {
			links = append(links, link)
		}
	}
	for i := range cur.Repositories {
		if cur.Repositories[i].RepoID == repoID {
			cur.Repositories[i].Links = links
			m.sets[id] = cur
			return cur, nil
		}
	}
	cur.Repositories = append(cur.Repositories, repository.TaskRepository{RepoID: repoID, Links: links})
	m.sets[id] = cur
	return cur, nil
}

// Under merged (default) and live, linked repository issues must be CLOSED and repository PR links must be MERGED;
// under off, neither blocks.
func TestDeliveryGateChecksLinkedRepositoryIssuesAndPRs(t *testing.T) {
	linkTodo := func(t *testing.T, st *memRepoStore, todoID string, urls ...string) {
		t.Helper()
		var links []repository.LinkInput
		for _, u := range urls {
			links = append(links, repository.LinkInput{URL: u})
		}
		if _, err := st.SetTodoRepositoryLinks(context.Background(), todoID, "repo_o_r", repository.LinksInput{Links: links}); err != nil {
			t.Fatalf("link todo repositories: %v", err)
		}
	}

	t.Run("landed requires linked issue closure even with global off", func(t *testing.T) {
		st := newMemRepoStore()
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Fix bug #42", "prompt": "Fix issue #42",
			"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
			"completion_mode": "landed",
			"done_when":       []string{"PR merged and issue #42 closed"},
		}}})
		if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
		todo := onlyTodo(s)
		linkTodo(t, st, todo.ID, "https://github.com/o/r/issues/42", "https://github.com/o/r/pull/9")
		forge := &fakeForge{merged: true, sha: mergeSHA, issueStates: map[string]string{"o/r#42": "open"}}
		s.deploy.forge = forge
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}
		setDeliveryGate(t, s, deliveryGateOff)

		checks := []ReviewCheck{{Criterion: "PR merged and issue #42 closed", Met: true, Evidence: "PR #9 merged"}}
		_, err := acceptReview(s, todo.ID, map[string]any{"checks": checks})
		if err == nil || !strings.Contains(err.Error(), "https://github.com/o/r/issues/42") || !strings.Contains(err.Error(), "still open") || !strings.Contains(err.Error(), "external_event") {
			t.Fatalf("open linked issue did not block acceptance: %v", err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt != nil {
			t.Fatal("task closed while linked issue remained open")
		}

		// Close the issue -> now acceptance succeeds.
		forge.mu.Lock()
		forge.issueStates["o/r#42"] = "closed"
		forge.mu.Unlock()
		if _, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err != nil {
			t.Fatalf("closed issue still blocked acceptance: %v", err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt == nil {
			t.Fatal("task did not close after PR merged and issue closed")
		}
	})

	t.Run("unmerged repository pull_request link blocks even without t.PRURL", func(t *testing.T) {
		st := newMemRepoStore()
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Ship fix", "prompt": "Ship fix",
			"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
			"done_when": []string{"Fix merged"},
		}}})
		if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
		todo := onlyTodo(s)
		linkTodo(t, st, todo.ID, "https://github.com/o/r/pull/15")
		s.deploy.forge = &fakeForge{merged: false}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}

		checks := []ReviewCheck{{Criterion: "Fix merged", Met: true, Evidence: "opened PR #15"}}
		if _, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err == nil || !strings.Contains(err.Error(), "not merged") {
			t.Fatalf("unmerged repository PR link did not block: %v", err)
		}
		setDeliveryGate(t, s, deliveryGateOff)
		if _, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err != nil {
			t.Fatalf("delivery_gate=off still blocked: %v", err)
		}
	})

	t.Run("read-only task and explicit PR-only scope are not blocked under merged, but PR-only does not override live", func(t *testing.T) {
		st := newMemRepoStore()
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		// 1. Read-only task with linked open issue -> allowed under default merged.
		rawRO, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Investigate issue #42", "prompt": "Investigate issue #42",
			"machine": "box", "cwd": "/code/app", "readonly": true, "user_requested": true, "why": "repo",
			"goal": "Root cause of #42",
		}}})
		if _, err := s.toolTaskPlan(ctx, "conv1", rawRO); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "ro agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
		roTodo := onlyTodo(s)
		linkTodo(t, st, roTodo.ID, "https://github.com/o/r/issues/42")
		s.deploy.forge = &fakeForge{merged: false, issueStates: map[string]string{"o/r#42": "open"}}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}
		// The owner asked for this investigation, so its result goes to the owner (user_check, #15566); the delivery
		// gate still checks the review.
		if _, err := acceptReview(s, roTodo.ID, map[string]any{"checks": []ReviewCheck{}, "user_check": true}); err != nil {
			t.Fatalf("read-only task was blocked under default merged: %v", err)
		}

		// 2. Explicit PR-only writable task -> allowed under merged, still blocked under live.
		s2, _ := newQuietServiceOn(t, newMemRepoStore())
		rawPROnly, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Open draft PR", "prompt": "Prepare a draft PR",
			"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
			"done_when": []string{"仅提交草稿 PR 并附测试日志"},
		}}})
		if _, err := s2.toolTaskPlan(ctx, "conv1", rawPROnly); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "pr-only agent", func() bool { return len(onlyTodo(s2).Agents) == 1 })
		prOnlyTodo := onlyTodo(s2)
		s2.setTaskStage(ctx, prOnlyTodo.ID, "review", "https://github.com/o/r/pull/21")
		s2.deploy.forge = &fakeForge{merged: false}
		s2.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s2.deploy.listPaths = []string{}

		checks := []ReviewCheck{{Criterion: "仅提交草稿 PR 并附测试日志", Met: true, Evidence: "draft PR #21"}}
		// Under live, PR-only criteria must NOT override live (#6428).
		setDeliveryGate(t, s2, deliveryGateLive)
		if _, err := acceptReview(s2, prOnlyTodo.ID, map[string]any{"checks": checks}); err == nil || !strings.Contains(err.Error(), "not merged") {
			t.Fatalf("PR-only task bypassed live gate: %v", err)
		}
		// Under merged, explicit PR-only scope is allowed.
		setDeliveryGate(t, s2, deliveryGateMerged)
		if _, err := acceptReview(s2, prOnlyTodo.ID, map[string]any{"checks": checks}); err != nil {
			t.Fatalf("explicit PR-only task was blocked under merged gate: %v", err)
		}
	})

	t.Run("unconfigured GitHub linked issue is non-blocking under merged but fails closed under live", func(t *testing.T) {
		st := newMemRepoStore()
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Fix bug #42", "prompt": "Fix issue #42",
			"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
			"done_when": []string{"Issue #42 closed"},
		}}})
		if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
		todo := onlyTodo(s)
		linkTodo(t, st, todo.ID, "https://github.com/o/r/issues/42")
		s.deploy.forge = &fakeForge{merged: true, sha: mergeSHA, issueStateErr: fmt.Errorf("%w: no gh auth", errGitHubUnconfigured)}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}

		checks := []ReviewCheck{{Criterion: "Issue #42 closed", Met: true, Evidence: "verified"}}
		// Under live, unconfigured GitHub issue check must fail closed with delivery_verification_unavailable.
		setDeliveryGate(t, s, deliveryGateLive)
		if res, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err == nil ||
			(!strings.Contains(err.Error(), "暂时无法验证") && !strings.Contains(err.Error(), "temporarily cannot be verified")) {
			t.Fatalf("live gate did not fail closed on unconfigured GitHub issue: res=%q err=%v", res, err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt != nil {
			t.Fatal("task closed under live gate despite unconfigured GitHub issue check")
		}

		// Under merged, unconfigured GitHub issue check degrades to a non-blocking reply note.
		setDeliveryGate(t, s, deliveryGateMerged)
		res, err := acceptReview(s, todo.ID, map[string]any{"checks": checks})
		if err != nil {
			t.Fatalf("merged gate blocked unconfigured GitHub issue: %v", err)
		}
		if !strings.Contains(res, "github.com") {
			t.Fatalf("merged gate reply missing host-not-connected note: %q", res)
		}
		if got := todoNow(s, todo.ID); got.DoneAt == nil {
			t.Fatal("task did not close under merged gate with unconfigured GitHub note")
		}
	})

	t.Run("repository link store failure fails closed as temporary verification error", func(t *testing.T) {
		st := newMemRepoStore()
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Fix bug #42", "prompt": "Fix issue #42",
			"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
			"done_when": []string{"Fix merged"},
		}}})
		if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
		todo := onlyTodo(s)
		s.deploy.forge = &fakeForge{merged: true, sha: mergeSHA}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}

		st.mu.Lock()
		st.todoReposErr = errors.New("db connection reset")
		st.mu.Unlock()

		checks := []ReviewCheck{{Criterion: "Fix merged", Met: true, Evidence: "merged"}}
		res, err := acceptReview(s, todo.ID, map[string]any{"checks": checks})
		if err == nil || !strings.Contains(err.Error(), "db connection reset") ||
			(!strings.Contains(err.Error(), "暂时无法验证") && !strings.Contains(err.Error(), "temporarily cannot be verified")) {
			t.Fatalf("repository store failure did not fail closed: res=%q err=%v", res, err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt != nil {
			t.Fatal("task closed despite repository link store failure")
		}

		// Once store recovers, acceptance succeeds.
		st.mu.Lock()
		st.todoReposErr = nil
		st.mu.Unlock()
		if _, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err != nil {
			t.Fatalf("acceptance failed after repository store recovery: %v", err)
		}
		if got := todoNow(s, todo.ID); got.DoneAt == nil {
			t.Fatal("task did not close after repository store recovery")
		}
	})
}

func TestReferenceIssueDoesNotBlockAcceptance(t *testing.T) {
	st := newMemRepoStore()
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "Fix bug #42", "prompt": "Fix issue #42",
		"machine": "box", "cwd": "/code/app", "readonly": false, "user_requested": true, "why": "repo",
		"done_when": []string{"Issue #42 closed"},
	}}})
	if _, err := s.toolTaskPlan(ctx, "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	issueURL := "https://github.com/o/r/issues/42"
	if _, err := st.SetTodoRepositoryLinks(ctx, todo.ID, "repo_o_r", repository.LinksInput{Links: []repository.LinkInput{
		{URL: issueURL, Role: repository.LinkRoleReference},
	}}); err != nil {
		t.Fatalf("link reference issue: %v", err)
	}
	if got := linkRoles(t, s, todo.ID)[issueURL]; got != repository.LinkRoleReference {
		t.Fatalf("issue stored as %q, want reference", got)
	}
	s.deploy.forge = &fakeForge{issueStates: map[string]string{"o/r#42": "open"}}
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}

	checks := []ReviewCheck{{Criterion: "Issue #42 closed", Met: true, Evidence: "reference only"}}
	if _, err := acceptReview(s, todo.ID, map[string]any{"checks": checks}); err != nil {
		t.Fatalf("open reference issue blocked acceptance: %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatal("task stayed open although its only linked issue was a reference")
	}
}

// Three-tier delivery_gate setting works via HTTP PUT/GET and persona_settings while staying backward-compatible
// with legacy done_needs_delivery boolean configs.
func TestDeliveryGateSettingsAndBackwardCompat(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())

	// Default: delivery_gate=merged, done_needs_delivery=false.
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateMerged || v.DoneNeedsDelivery {
		t.Fatalf("default view = %q / %v, want merged / false", v.DeliveryGate, v.DoneNeedsDelivery)
	}

	// Legacy boolean true -> live; explicit false -> off.
	on, off := true, false
	if _, err := s.updateSettings(ctx, func(v *Settings) error { v.DoneNeedsDelivery = &on; v.DeliveryGate = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateLive || !v.DoneNeedsDelivery {
		t.Fatalf("legacy true view = %q / %v, want live / true", v.DeliveryGate, v.DoneNeedsDelivery)
	}
	if _, err := s.updateSettings(ctx, func(v *Settings) error { v.DoneNeedsDelivery = &off; v.DeliveryGate = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateOff || v.DoneNeedsDelivery {
		t.Fatalf("legacy false view = %q / %v, want off / false", v.DeliveryGate, v.DoneNeedsDelivery)
	}

	// persona_settings get/set for assistant.delivery_gate.
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	if out, _ := tool(`{"action":"get","key":"assistant.delivery_gate"}`); out != deliveryGateOff {
		t.Fatalf("get delivery_gate = %q, want off", out)
	}
	if out, isErr := tool(`{"action":"set","key":"assistant.delivery_gate","value":"invalid"}`); !isErr || !strings.Contains(out, "off, merged or live") {
		t.Fatalf("invalid tier not refused: %v %s", isErr, out)
	}
	wantCard := i18n.T(i18n.Default, "assistant-settings_tool.change_delivery_gate", i18n.Args{
		"before": i18n.T(i18n.Default, "assistant-settings_tool.delivery_gate_off"),
		"after":  i18n.T(i18n.Default, "assistant-settings_tool.delivery_gate_merged"),
	})
	go func() {
		q, ok := awaitCard(s)
		if !ok {
			t.Error("no approval card")
			return
		}
		if q.Summary != wantCard {
			t.Errorf("card %q, want %q", q.Summary, wantCard)
		}
		allow := true
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		_, _ = s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw})
	}()
	if out, isErr := tool(`{"action":"set","key":"assistant.delivery_gate","value":"merged"}`); isErr || !strings.Contains(out, "assistant.delivery_gate is merged") {
		t.Fatalf("set delivery_gate merged failed: %v %s", isErr, out)
	}
	if out, _ := tool(`{"action":"get","key":"assistant.delivery_gate"}`); out != deliveryGateMerged {
		t.Fatalf("after set, get = %q, want merged", out)
	}

	// Regression: passing JSON null to legacy assistant.done_needs_delivery must be rejected, create no approval card,
	// and keep the default merged gate unchanged.
	if out, isErr := tool(`{"action":"set","key":"assistant.done_needs_delivery","value":null}`); !isErr || !strings.Contains(out, "value must be true or false") {
		t.Fatalf("legacy done_needs_delivery=null not refused: %v %q", isErr, out)
	}
	if len(s.pendingQuestions()) != 0 {
		t.Fatalf("legacy done_needs_delivery=null created approval card(s): %d", len(s.pendingQuestions()))
	}
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateMerged || v.DoneNeedsDelivery {
		t.Fatalf("after legacy null, view = %q / %v, want merged / false", v.DeliveryGate, v.DoneNeedsDelivery)
	}

	// Regression: setting legacy assistant.done_needs_delivery=false from default merged must disable the gate (off),
	// not return "unchanged", and clearing DeliveryGate persists the off tier.
	approveNext := func(wantSummary string) {
		go func() {
			q, ok := awaitCard(s)
			if !ok {
				t.Error("no approval card")
				return
			}
			if wantSummary != "" && q.Summary != wantSummary {
				t.Errorf("card %q, want %q", q.Summary, wantSummary)
			}
			allow := true
			raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
			_, _ = s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw})
		}()
	}
	wantMergedToOff := i18n.T(i18n.Default, "assistant-settings_tool.change_delivery_gate", i18n.Args{
		"before": i18n.T(i18n.Default, "assistant-settings_tool.delivery_gate_merged"),
		"after":  i18n.T(i18n.Default, "assistant-settings_tool.delivery_gate_off"),
	})
	approveNext(wantMergedToOff)
	if out, isErr := tool(`{"action":"set","key":"assistant.done_needs_delivery","value":false}`); isErr || !strings.Contains(out, "assistant.done_needs_delivery is false") {
		t.Fatalf("legacy done_needs_delivery=false from merged failed: %v %s", isErr, out)
	}
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateOff || v.DoneNeedsDelivery {
		t.Fatalf("after legacy false, view = %q / %v, want off / false", v.DeliveryGate, v.DoneNeedsDelivery)
	}
	// Repeating false when already off reports unchanged without asking again.
	if out, isErr := tool(`{"action":"set","key":"assistant.done_needs_delivery","value":false}`); isErr || !strings.Contains(out, "没有变化") {
		t.Fatalf("repeat legacy false = %v %q, want unchanged", isErr, out)
	}
	// Legacy true transitions off -> live.
	approveNext("")
	if out, isErr := tool(`{"action":"set","key":"assistant.done_needs_delivery","value":true}`); isErr || !strings.Contains(out, "assistant.done_needs_delivery is true") {
		t.Fatalf("legacy done_needs_delivery=true failed: %v %s", isErr, out)
	}
	if v := s.view(ctx, false); v.DeliveryGate != deliveryGateLive || !v.DoneNeedsDelivery {
		t.Fatalf("after legacy true, view = %q / %v, want live / true", v.DeliveryGate, v.DoneNeedsDelivery)
	}

	// Repository provider non-GitHub issue check does not block when host is unknown.
	if _, ok := repository.RemoteHost("https://github.com/o/r/issues/1"); !ok {
		t.Fatal("expected valid host")
	}
}

// End-to-end closeout loop: default merged gate refuses early acceptance on an open PR, supervisor parks with
// external_event + pr_event=terminal, sweepPRStatuses wakes on merge, and final acceptance succeeds.
func TestDefaultMergedGateParkAndWakeCloseoutLoop(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	holdDigest(s)
	ctx := context.Background()
	prURL := "https://github.com/o/r/pull/9"
	s.setTaskStage(ctx, todo.ID, "review", prURL)
	forge := &statusForge{status: TodoPRStatus{State: "open"}}
	s.deploy.forge = forge
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}

	// 1. Premature acceptance is refused under default delivery_gate=merged.
	if _, err := acceptReview(s, todo.ID, nil); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("open PR was accepted prematurely: %v", err)
	}

	// 2. Supervisor parks waiting on PR terminal event.
	parkArgs, _ := json.Marshal(map[string]any{
		"todo_id":    todo.ID,
		"verdict":    "parked",
		"waiting_on": "external_event",
		"reason":     "Waiting for PR merge: " + prURL,
		"evidence": map[string]any{
			"kind":       "external_event",
			"next_actor": "external",
			"target":     "PR #9",
			"url":        prURL,
			"pr_event":   "terminal",
			"wake":       "PR #9 is merged or closed",
		},
	})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", parkArgs); err != nil {
		t.Fatalf("park failed: %v", err)
	}
	if got := todoNow(s, todo.ID); got.Supervision == nil || got.Supervision.State != reviewParked {
		t.Fatalf("task not parked: %+v", got.Supervision)
	}

	// 3. PR merges -> sweepPRStatuses wakes supervision to reviewPending.
	forge.status = TodoPRStatus{State: "merged"}
	forge.merged = true
	forge.sha = mergeSHA
	s.sweepPRStatuses(ctx, time.Now().Add(time.Minute))
	if got := todoNow(s, todo.ID); got.Supervision.State != reviewPending {
		t.Fatalf("parked PR did not wake on merge: state=%q", got.Supervision.State)
	}

	// 4. Supervisor runs final acceptance -> now succeeds and closes the todo.
	if _, err := acceptReview(s, todo.ID, nil); err != nil {
		t.Fatalf("acceptance after merge failed: %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatal("todo did not close after merge")
	}
}
