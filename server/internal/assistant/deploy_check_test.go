package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeForge is GitHub as a deploy check sees it: one PR (merged or not, its merge commit) and which hub commits
// contain it.
type fakeForge struct {
	mu            sync.Mutex
	merged        bool
	sha           string
	contains      map[string]bool // by hub commit; a commit not in it is not in the repository
	err           error
	issueOnly     bool
	issueErr      error
	issueState    string            // default issue state ("open" when empty)
	issueStates   map[string]string // optional per-issue states keyed by "owner/repo#number"
	issueStateErr error
	// ownedPRs maps "owner/repo#number" to whether the viewer opened it; a missing key means ownership is unknown
	// (known=false), so the gate keeps its conservative delivery default.
	ownedPRs     map[string]bool
	calls        int
	compareCalls int
}

// PROwnedByViewer backs prOwnershipForge in tests; a missing key reads as unknown, never foreign.
func (f *fakeForge) PROwnedByViewer(_ context.Context, repo string, number int) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, known := f.ownedPRs[repo+"#"+strconv.Itoa(number)]
	return owned, known, nil
}

func (f *fakeForge) IssueState(_ context.Context, repo string, number int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issueStateErr != nil {
		return "", f.issueStateErr
	}
	if f.issueStates != nil {
		if st, ok := f.issueStates[repo+"#"+strconv.Itoa(number)]; ok {
			return st, nil
		}
	}
	if f.issueState != "" {
		return f.issueState, nil
	}
	return "open", nil
}

func (f *fakeForge) IsPR(context.Context, string, int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.issueOnly, f.issueErr
}

func (f *fakeForge) PRMerge(_ context.Context, repo string, number int) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return false, "", f.err
	}
	return f.merged, f.sha, nil
}

func (f *fakeForge) Contains(_ context.Context, repo, base, head string) (bool, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compareCalls++
	if base != f.sha {
		return false, false, errors.New("unexpected base")
	}
	in, found := f.contains[head]
	return in, found, nil
}

// runningHub is the hub's commit as the deploy check reads it, changed by a deploy.
type runningHub struct {
	mu     sync.Mutex
	commit string
}

func (h *runningHub) build(context.Context) (string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return "v0.3.0-12-g" + h.commit[:7] + "-202611101200", h.commit + "-dirty", nil
}

func (h *runningHub) deploy(commit string) {
	h.mu.Lock()
	h.commit = commit
	h.mu.Unlock()
}

const (
	oldHub   = "aaaaaaa1111111111111111111111111111111aa"
	newHub   = "ccccccc3333333333333333333333333333333cc"
	mergeSHA = "bbbbbbb2222222222222222222222222222222bb"
)

// deployService is acceptService (owner-asked tasks are only the big ones) with the hub running oldHub and PR 9 of o/r
// merged as mergeSHA, in newHub and not in oldHub.
func deployService(t *testing.T) (*Service, *fakeHub, func() []string, *runningHub, *fakeForge) {
	t.Helper()
	s, f, posts := acceptService(t, repositorystore.New(todoTestDB(t)))
	artifactNodeProof(f, "https://github.com/o/r/pull/9", "contributor", "contributor", "fix/work", "fix/work", "abc", "abc")
	s.ownerVerifiesAll = false
	hub := &runningHub{commit: oldHub}
	forge := &fakeForge{merged: true, sha: mergeSHA, contains: map[string]bool{oldHub: false, newHub: true}}
	s.deploy.hubBuild, s.deploy.forge = hub.build, forge
	s.deploy.listPaths = []string{} // no deployed-commits file unless a test writes one (deployed_commits_test.go)
	return s, f, posts, hub, forge
}

func verifyCards(posts []string) (n int) {
	for _, p := range posts {
		if strings.Contains(p, "请验收") {
			n++
		}
	}
	return n
}

// The hub's commit: the build's (without -dirty), else the -g<hash> of its version; nothing when neither says.
func TestHubCommitOf(t *testing.T) {
	for _, tc := range []struct{ version, commit, want string }{
		{"v0.3.0-12-gabc1234-202611101200", "abc1234def5678abc1234def5678abc1234def56-dirty", "abc1234def5678abc1234def5678abc1234def56"},
		{"v0.3.0-12-gabc1234-202611101200", "", "abc1234"},
		{"v0.3.0-12-gabc1234-dirty-202611101200", "", "abc1234"},
		{"4420a9d0c-202611100810", "", "4420a9d0c"}, // deploy.sh with no tag: git describe --always
		{"4420a9d0c-dirty-202611100810", "", "4420a9d0c"},
		{"v0.3.0-202611100810", "", ""}, // a tag alone names no commit
		{"cafe-202611100810", "", ""},   // too short to be a commit

		{"dev", "", ""},
		{"1.2.3", "not-a-commit", ""},
	} {
		if got := hubCommitOf(tc.version, tc.commit); got != tc.want {
			t.Errorf("hubCommitOf(%q, %q) = %q, want %q", tc.version, tc.commit, got, tc.want)
		}
	}
}

// Merged but not in the running hub: the turn that says "deployed" leaves the task at merge, its delivery "merged, not
// deployed" saying why, with no "Please verify". Once the hub runs a commit that contains the merge, the task is
// deployed and — not a big feature — accepted by the supervising assistant, still with no card to the owner.
func TestMergedNotDeployedIsHeldUntilTheHubHasIt(t *testing.T) {
	s, f, posts, hub, forge := deployService(t)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "the held delivery", func() bool { d := todoNow(s, todo.ID).Delivery; return d != nil })
	got := todoNow(s, todo.ID)
	if got.Delivery.State != deliveryMerged || !strings.Contains(got.Delivery.Gate, "等部署") || !strings.Contains(got.Delivery.Gate, "aaaaaaa1") {
		t.Fatalf("delivery %+v", got.Delivery)
	}
	if got.Stage != "merge" {
		t.Fatalf("stage %q: merged and not deployed is not deployed", got.Stage)
	}
	if line := deliverySummary(got); !strings.HasPrefix(line, "已合未上线") {
		t.Fatalf("summary %q", line)
	}
	waitReportQueue(t, s)
	if a := askOf(s, todo.ID); a != nil {
		t.Fatalf("asked to verify work not deployed: %+v", a)
	}
	if n := verifyCards(posts()); n != 0 {
		t.Fatalf("%d 请验收 cards", n)
	}

	// The same hub: nothing changes, and GitHub is not asked again.
	calls := forge.calls
	s.sweepDeploys(time.Now())
	if forge.calls != calls || todoNow(s, todo.ID).Delivery.State != deliveryMerged {
		t.Fatalf("same hub: %d calls, %+v", forge.calls-calls, todoNow(s, todo.ID).Delivery)
	}

	hub.deploy(newHub)
	s.sweepDeploys(time.Now())
	got = todoNow(s, todo.ID)
	if got.Delivery == nil || got.Delivery.State != deliveryLive {
		t.Fatalf("deployed, delivery %+v", got.Delivery)
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.ByAssistant || a.AcceptedAt.IsZero() || !a.NoCard {
		t.Fatalf("ask %+v", a)
	}
	if got.Stage != "verify" {
		t.Fatalf("stage %q", got.Stage)
	}
	waitReportQueue(t, s)
	if n := verifyCards(posts()); n != 0 {
		t.Fatalf("%d 请验收 cards to the owner for a small task", n)
	}
	s.reports.mu.Lock()
	watch := s.reports.stages[todo.ID].Deploy
	s.reports.mu.Unlock()
	if watch != nil {
		t.Fatalf("still watched: %+v", watch)
	}
}

func TestDeployWatchKeepsConfirmedMergeAcrossBuildsDuringForgeOutage(t *testing.T) {
	s, _, _, hub, forge := deployService(t)
	ctx := t.Context()
	now := time.Now()
	const pr = "https://github.com/o/r/pull/9"
	putTodo(s, Todo{ID: "todo_merge", Title: "Confirm delivery", Origin: "conv1", CreatedAt: now})
	s.setTaskStage(ctx, "todo_merge", "review", pr)
	s.sweepDeploys(now)
	if got := todoNow(s, "todo_merge"); got.Delivery == nil || got.Delivery.State != deliveryMerged {
		t.Fatalf("initial confirmed merge: %+v", got.Delivery)
	}

	forge.err = errors.New("the forge is offline")
	for i, sha := range []string{newHub, strings.Repeat("e", 40)} {
		hub.deploy(sha)
		writeDeployed(t, s, sha, oldHub+" unrelated change (#5)")
		s.sweepDeploys(now.Add(time.Duration(i+1) * time.Minute))
		got := todoNow(s, "todo_merge")
		if got.Delivery == nil || got.Delivery.State != deliveryMerged || !strings.Contains(got.Delivery.Gate, "还没有这个 PR") {
			t.Fatalf("build %s lost its upstream merge evidence: %+v", sha, got.Delivery)
		}
	}
	got := todoNow(s, "todo_merge")
	if stage, live := s.gateDeployStage(ctx, got, "deploy", pr, "", "", ""); stage != "merge" || live {
		t.Fatalf("repeat deployment claim during outage: stage %s, live %v", stage, live)
	}
	if d := s.gateDelivery(ctx, got, TodoDelivery{State: deliveryLive}, "https://github.com/o/r/pull/10", true); d.State != deliveryPR {
		t.Fatalf("another PR inherited merge evidence: %+v", d)
	}
}

// Deployed and in the running hub at once: deployed, accepted by the assistant (no card); a big feature (a high
// priority to-do) asks the owner with the card instead.
func TestDeployedInTheRunningHub(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts, hub, _ := deployService(t)
	hub.deploy(newHub)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "accepted by the assistant", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && !a.AcceptedAt.IsZero() && todoNow(s, todo.ID).Stage == "verify"
	})
	if a := askOf(s, todo.ID); !a.ByAssistant || a.MessageID != "" {
		t.Fatalf("ask %+v", a)
	}
	if got := todoNow(s, todo.ID); got.Stage != "verify" || got.Delivery != nil && got.Delivery.held() {
		t.Fatalf("todo %q %+v", got.Stage, got.Delivery)
	}
	if n := verifyCards(posts()); n != 0 {
		t.Fatalf("%d 请验收 cards", n)
	}

	s2, f2, posts2, hub2, _ := deployService(t)
	hub2.deploy(newHub)
	big := startedTask(t, s2, f2)
	s2.todos.mu.Lock()
	s2.todos.todos[big.ID].Priority = "high"
	s2.todos.mu.Unlock()
	agentTurn(s2, f2, proto.StatusWorking, "", 0)
	agentTurn(s2, f2, proto.StatusDone, deployedReply, 0)
	waitAcceptCard(t, s2, big.ID, posts2)
	if a := askOf(s2, big.ID); a.ByAssistant || !a.AcceptedAt.IsZero() {
		t.Fatalf("a big feature's ask %+v", a)
	}
	if got := todoNow(s2, big.ID); got.Stage != "deploy" {
		t.Fatalf("stage %q", got.Stage)
	}
}

// A check that cannot tell (GitHub unreachable) never says deployed; a PR in a repository the hub is not built from
// keeps the agent's word (unverified).
func TestDeployCheckFailingIsNotDeployed(t *testing.T) {
	s, f, posts, _, forge := deployService(t)
	forge.err = errors.New("dial tcp: i/o timeout")
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "the held delivery", func() bool { return todoNow(s, todo.ID).Delivery != nil })
	got := todoNow(s, todo.ID)
	if got.Delivery.State != deliveryPR || got.Stage != "review" || !strings.Contains(got.Delivery.Gate, "等确认部署") {
		t.Fatalf("todo %q %+v", got.Stage, got.Delivery)
	}
	waitReportQueue(t, s)
	if askOf(s, todo.ID) != nil || verifyCards(posts()) != 0 {
		t.Fatal("asked to verify work the check could not confirm")
	}

	s2, f2, _, _, forge2 := deployService(t)
	forge2.contains = map[string]bool{} // the hub's commit is not in o/r: not the hub's work
	other := startedTask(t, s2, f2)
	agentTurn(s2, f2, proto.StatusWorking, "", 0)
	agentTurn(s2, f2, proto.StatusDone, deployedReply, 0)
	waitFor(t, "the ask", func() bool { a := askOf(s2, other.ID); return a != nil && !a.AskedAt.IsZero() })
	if got := todoNow(s2, other.ID); got.Delivery != nil && got.Delivery.held() {
		t.Fatalf("unverified work held: %+v", got.Delivery)
	}
}

// A delivery the judge calls live is held the same way (assistant.done_needs_delivery): merged, not deployed.
func TestJudgedLiveIsGated(t *testing.T) {
	s, _, _, hub, _ := deployService(t)
	ctx := context.Background()
	todo := Todo{ID: "todo_g", PRURL: "https://github.com/o/r/pull/9"}
	if d := s.gateDelivery(ctx, todo, TodoDelivery{State: deliveryLive}, "", false); d.State != deliveryMerged || d.Unblock != unblockOther {
		t.Fatalf("not deployed: %+v", d)
	}
	hub.deploy(newHub)
	if d := s.gateDelivery(ctx, todo, TodoDelivery{State: deliveryLive}, "", false); d.State != deliveryLive {
		t.Fatalf("deployed: %+v", d)
	}
	if d := s.gateDelivery(ctx, Todo{ID: "todo_x"}, TodoDelivery{State: deliveryLive}, "", false); d.State != deliveryLive {
		t.Fatalf("no PR: %+v", d)
	}
}

// The bus hub reads the running hub's version and commit from its health.
func TestBusHubReadsTheHubBuild(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/health" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"version":"v1-gabc1234-202611101200","commit":"abc1234def"}`))
	}))
	defer srv.Close()
	v, c, err := NewBusHub(BusOptions{Hub: srv.URL}).HubBuild(context.Background())
	if err != nil || v != "v1-gabc1234-202611101200" || c != "abc1234def" {
		t.Fatalf("%q %q %v", v, c, err)
	}
}

// GitHub's answers as the forge reads them: a merged PR's merge commit; compare's status (ahead / identical: the hub
// has it, behind / diverged: not); 404 a commit the repository does not have. Through the connector, its 404 error is
// the same "not found".
func TestGitHubForge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/pulls/9":
			_, _ = w.Write([]byte(`{"merged":true,"merge_commit_sha":"` + mergeSHA + `"}`))
		case "/repos/o/r/compare/" + mergeSHA + "..." + newHub:
			_, _ = w.Write([]byte(`{"status":"ahead"}`))
		case "/repos/o/r/compare/" + mergeSHA + "..." + oldHub:
			_, _ = w.Write([]byte(`{"status":"behind"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	g := githubForge{base: srv.URL, client: srv.Client()}
	if merged, sha, err := g.PRMerge(ctx, "o/r", 9); !merged || sha != mergeSHA || err != nil {
		t.Fatalf("PR: %v %q %v", merged, sha, err)
	}
	for head, want := range map[string][2]bool{newHub: {true, true}, oldHub: {false, true}, "ddddddd": {false, false}} {
		in, found, err := g.Contains(ctx, "o/r", mergeSHA, head)
		if err != nil || in != want[0] || found != want[1] {
			t.Errorf("%s: %v %v %v, want %v", head, in, found, err, want)
		}
	}
	if _, _, err := g.PRMerge(ctx, "o/r", 10); !errors.Is(err, errForgeNotFound) {
		t.Fatalf("missing PR: %v", err)
	}
	via := githubForge{base: "http://127.0.0.1:1", client: srv.Client(), via: func(_ context.Context, path string, out any) (bool, error) {
		return true, errors.New("GitHub GET " + path + ": 404 Not Found")
	}}
	if _, found, err := via.Contains(ctx, "o/r", mergeSHA, "ddddddd"); found || err != nil {
		t.Fatalf("connector 404: %v %v", found, err)
	}
}

// An open to-do that names a merged PR (stage review, no delivery: done_needs_delivery off) is not a PR to merge: the
// sweep finds it merged and not in the running hub, "merged, not deployed"; once the hub has it, deployed (Just
// finished). An open PR is left as it is and not asked about again before deployRecheck.
func TestMergedPRIsNotAPRToMerge(t *testing.T) {
	s, _, posts, hub, _ := deployService(t)
	ctx := context.Background()
	pr := "https://github.com/o/r/pull/9"
	putTodo(s, Todo{ID: "todo_m", Title: "主对话任务栏显示全部进度", Origin: "conv1", CreatedAt: time.Now().Add(-time.Hour)})
	s.setTaskStage(ctx, "todo_m", "review", pr)
	if got := todoNow(s, "todo_m"); got.PRURL != pr || got.Delivery != nil {
		t.Fatalf("setup: %+v", got)
	}
	s.sweepDeploys(time.Now())
	got := todoNow(s, "todo_m")
	if got.Delivery == nil || got.Delivery.State != deliveryMerged || got.Stage != "merge" {
		t.Fatalf("merged, not deployed: %q %+v", got.Stage, got.Delivery)
	}
	hub.deploy(newHub)
	s.sweepDeploys(time.Now())
	got = todoNow(s, "todo_m")
	if got.Delivery == nil || got.Delivery.State != deliveryLive || got.Stage != "deploy" {
		t.Fatalf("deployed: %q %+v", got.Stage, got.Delivery)
	}
	if askOf(s, "todo_m") != nil || verifyCards(posts()) != 0 {
		t.Fatal("asked to verify a to-do no turn said deployed")
	}

	// An open PR: nothing changes, and the next minute's sweep does not ask GitHub again.
	s2, _, _, _, forge2 := deployService(t)
	forge2.merged = false
	putTodo(s2, Todo{ID: "todo_o", Title: "开着的 PR", Origin: "conv1", CreatedAt: time.Now().Add(-time.Hour)})
	s2.setTaskStage(ctx, "todo_o", "review", pr)
	s2.sweepDeploys(time.Now())
	calls := forge2.calls
	s2.sweepDeploys(time.Now().Add(time.Minute))
	if got := todoNow(s2, "todo_o"); got.Delivery != nil || got.Stage != "review" || forge2.calls != calls {
		t.Fatalf("open PR: %q %+v, %d more calls", got.Stage, got.Delivery, forge2.calls-calls)
	}
}
