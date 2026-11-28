package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

// testReportDelay is the tests' reportDelay: short, set on each service (never the shared constant).
const testReportDelay = 50 * time.Millisecond

// reportService is a task service whose Feishu bot is paired, with reports on (or off) and the execution steps on (the
// card as it was before #3218), recording the posts.
func reportService(t *testing.T, on bool, stores ...Store) (*Service, *fakeHub, func() []string) {
	t.Helper()
	return reportServiceWith(t, FeishuFeatures{Main: true, Cards: true, Reports: on, ReportSteps: true}, stores...)
}

// reportServiceWith is reportService with the bot's switches as given.
func reportServiceWith(t *testing.T, feats FeishuFeatures, stores ...Store) (*Service, *fakeHub, func() []string) {
	t.Helper()
	var st Store
	if len(stores) > 0 {
		st = stores[0]
	}
	s, f := newTaskServiceOn(t, st)
	s.reports.mu.Lock()
	s.reports.delay = testReportDelay
	s.reports.mu.Unlock()
	var mu sync.Mutex
	var posts []string
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: feats}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		posts = append(posts, m.Card)
		mu.Unlock()
		return map[string]any{"message_id": "om_r"}, nil
	})
	return s, f, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), posts...)
	}
}

// A task's turn that ends with a merged PR is one card in the chat: the task, its stage, what was said, links to the
// task's page, the PR and the agent; never a bare id. The todo carries the stage and the PR for the apps.
func TestTaskDoneIsReportedInFeishu(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportService(t, true, repositorystore.New(todoTestDB(t)))
	todo := startedTask(t, s, f)
	artifactNodeProof(f, "https://github.com/o/r/pull/901", "contributor", "contributor",
		"fix/login", "fix/login", "abc", "abc")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了，https://github.com/o/r/pull/901 已合并", 0)
	waitFor(t, "the report", func() bool { return len(posts()) == 1 })
	card := posts()[0]
	for _, want := range []string{"修登录崩溃", "✅开发", "🔵**合入**", "⚪部署", "已合并",
		"https://hub.example.com/todos/" + todo.ID, "https://github.com/o/r/pull/901", "https://hub.example.com/s/box/s1"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if got := onlyTodo(s); got.Stage != "merge" || got.PRURL != "https://github.com/o/r/pull/901" {
		t.Fatalf("todo stage %q pr %q", got.Stage, got.PRURL)
	}
	time.Sleep(4 * testReportDelay)
	if n := len(posts()); n != 1 {
		t.Fatalf("%d posts", n)
	}
}

// A chart in the Agent's answer (persona-card, #3140) is drawn on the report card as its numbers, never cut down to
// the title alone: the owner reads this card in Lark (#4093).
func TestTaskReportDrawsAChartAsNumbers(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportService(t, true)
	startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "睡眠这一周\n\n```persona-card\n"+
		`{"type":"line","title":"Sleep this week","unit":"h","points":[{"label":"Mon","value":7.2},{"label":"Tue","value":6.5}]}`+
		"\n```\n\n睡得比上周多点。", 0)
	waitFor(t, "the report", func() bool { return len(posts()) == 1 })
	card := posts()[0]
	for _, want := range []string{"Sleep this week", "Mon", "7.2", "Tue", "6.5", "睡得比上周多点"} {
		if !strings.Contains(card, want) {
			t.Errorf("the chart is not drawn as numbers: the card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "persona-card") || strings.Contains(card, `\"type\"`) {
		t.Errorf("the chart block is left on the card as it was written:\n%s", card)
	}
}

// Changes within the delay are one card, the decision on top with its own answerable card; with reports off nothing
// is posted.
func TestTaskReportsMergeAndPutDecisionsFirst(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportService(t, true)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusBlocked, "", 1)
	ix := proto.Interaction{ID: "ix1", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}, {Label: "dev"}}}}}
	f.answerApp(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) { return map[string]any{"ok": true, "interaction": ix}, nil })
	s.taskReport(todo, reportFailed, "box", [2]string{}, "那台电脑离线", nil)
	s.taskReport(todo, reportNeeds, "box", [2]string{"box", "s1"}, noticeAsk(ix), &ix)
	waitFor(t, "the report and the question's card", func() bool { return len(posts()) == 2 })
	card := posts()[0]
	if !strings.Contains(card, "1 件事等你拍板") || !strings.Contains(card, "等你拍板") || !strings.Contains(card, "用哪个分支") {
		t.Fatalf("report:\n%s", card)
	}
	if !strings.Contains(posts()[1], "main") || !strings.Contains(posts()[1], `"a":"ix"`) {
		t.Fatalf("question card:\n%s", posts()[1])
	}

	s2, f2, posts2 := reportService(t, false)
	startedTask(t, s2, f2)
	agentTurn(s2, f2, proto.StatusWorking, "", 0)
	agentTurn(s2, f2, proto.StatusDone, "做完了", 0)
	time.Sleep(6 * testReportDelay)
	if got := posts2(); len(got) != 0 {
		t.Fatalf("reports off, posted %q", got)
	}
}

func TestStageOf(t *testing.T) {
	for text, want := range map[string]string{
		"改好了，还在跑测试":                                  "develop",
		"提了 https://github.com/o/r/pull/1":           "review",
		"https://github.com/o/r/pull/1 已合入":          "merge",
		"合入 https://github.com/o/r/pull/1 并已部署到 n37": "deploy",
		"已部署，实测通过，截图为证":                              "verify",
		// Not deployed yet, waiting for it, or asking: no "Please verify".
		"https://github.com/o/r/pull/1 已合并，还没部署到 hub，等你部署":       "merge",
		"Merged https://github.com/o/r/pull/2, not deployed yet": "merge",
		"已合入。未部署（这台连不上 hub）":                                     "develop",
		"部署了吗？还没有":                                               "develop",
		"等部署完再验":                                                 "develop",
		"Ready to be deployed once CI passes":                    "develop",
		"deploy.sh 跑完，已上线":                                       "deploy",
		"已部署到 hub，还没验证":                                          "deploy",
		"已部署到 hub。未部署的部分：网页":                                     "deploy",
	} {
		if got, _ := stageOf(text); got != want {
			t.Errorf("stageOf(%q) = %q, want %q", text, got, want)
		}
	}
	if got := stageBar("zh-Hans", "", false); strings.Contains(got, "✅") || strings.Contains(got, "🔵") {
		t.Errorf("no stage: %s", got)
	}
	if got := stageBar("zh-Hans", "", true); strings.Contains(got, "✅") || strings.Contains(got, "完成") {
		t.Errorf("no stage, done: %s", got) // nothing reached: the plain strip, never a Done of nothing
	}
	// Done after the steps taken (#11590): no current step, nothing claimed past Develop.
	if got, want := stageBar("en", "develop", true), "✅Plan → ✅Build → ✅**Done**"; got != want {
		t.Errorf("done at develop = %s, want %s", got, want)
	}
	if got := stageBar("en", "develop", false); !strings.Contains(got, "🔵**Build**") || !strings.Contains(got, "⚪Verified") {
		t.Errorf("open at develop = %s", got)
	}
	_ = context.Background
}

func TestStageOfSelectsOnlyAnUnambiguousLegacyPR(t *testing.T) {
	const delivery = "https://github.com/example/project/pull/12"
	const unrelated = "https://github.com/example/project/pull/13"
	for _, tc := range []struct {
		name, text, want string
	}{
		{"one distinct PR", delivery + "\n" + delivery + "#discussion_r123", delivery},
		{"multiple prose PRs", delivery + "\n" + unrelated, ""},
		{"explicit closeout", delivery + "\n<closeout status=\"done\">\nartifact: pr " + unrelated + "\nartifact: url " + delivery + "\n</closeout>", unrelated},
		{"closeout without a PR", unrelated + "\n" + researchNoPRCloseout, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := stageOf(tc.text); got != tc.want {
				t.Fatalf("stage PR = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdvanceTaskReplyPreservesDeliveryBinding(t *testing.T) {
	const delivery = "https://github.com/example/project/pull/12"
	const english = "https://github.com/example/project/pull/13"
	const chinese = "https://github.com/example/project/pull/14"
	const replacement = "https://github.com/example/project/pull/15"
	s, todo, _ := linkRoleTask(t)
	ctx := t.Context()
	store := s.store().(todoRepositoryImportStore)
	if _, _, err := store.ImportTodoRepositoryLinks(ctx, todo.ID, []string{delivery}, "explicit"); err != nil {
		t.Fatal(err)
	}
	// A repository link alone is enough to disable prose-based delivery extraction.
	s.advanceTaskReply(ctx, todo, "", "", "Checked for overlap with "+english)
	if got := todoNow(s, todo.ID).PRURL; got != "" {
		t.Fatalf("prose assigned a stage PR despite a repository binding: %q", got)
	}
	// A structured closeout can explicitly set the primary PR. The following reports use a stale
	// snapshot so the stored PR must win even if the caller has not seen it.
	a := todo.Agents[0]
	f := s.hub.(*fakeHub)
	deliveryText := "<closeout status=\"done\">\nartifact: pr " + delivery + "\n</closeout>"
	deliveryReply := artifactReportProof(f, todo, deliveryText, delivery)
	s.advanceTaskReplyAt(ctx, todo, a.MachineID, a.SessionID, deliveryText, deliveryReply.TS)
	for _, enabled := range []bool{true, false} {
		setSettings(t, s, func(v *Settings) { v.TodosV2 = &enabled })
		s.advanceTaskReply(ctx, todo, "", "", "Checked for overlap with "+english+"\n与 "+chinese+" 无重叠。") // i18n-ignore: task report fixture
		s.advanceTaskReply(ctx, todo, "", "", "Related work: "+delivery)
		s.advanceTaskReply(ctx, todo, "", "", "Checked no overlap with PR #16")
		if got := todoNow(s, todo.ID); got.PRURL != delivery || len(got.PRs) != 0 {
			t.Fatalf("todos_v2=%v: stored PR = %q, earlier PRs = %v", enabled, got.PRURL, got.PRs)
		}
	}
	replacementText := "Checked " + english + "\n<closeout status=\"done\">\nartifact: pr " + replacement + "\nartifact: url " + chinese + "\n</closeout>"
	replacementReply := artifactReportProof(f, todo, replacementText, replacement)
	s.advanceTaskReplyAt(ctx, todo, a.MachineID, a.SessionID, replacementText, replacementReply.TS)
	if got := todoNow(s, todo.ID).PRURL; got != replacement {
		t.Fatalf("explicit replacement PR = %q, want %q", got, replacement)
	}
	roles := linkRoles(t, s, todo.ID)
	for u, want := range map[string]string{
		delivery: repository.LinkRoleDelivery, replacement: repository.LinkRoleDelivery,
		english: repository.LinkRoleReference, chinese: repository.LinkRoleReference,
	} {
		if roles[u] != want {
			t.Errorf("role of %s = %q, want %q", u, roles[u], want)
		}
	}
}

func TestAdvanceTaskReplyLegacyDeliveryAfterReferences(t *testing.T) {
	const reference = "https://github.com/example/project/pull/12"
	const other = "https://github.com/example/project/pull/13"
	const delivery = "https://github.com/example/project/pull/14"
	s, todo, _ := linkRoleTask(t)
	ctx := t.Context()
	s.advanceTaskReply(ctx, todo, "", "", reference+" and "+other)
	s.advanceTaskReply(ctx, todo, "", "", reference)
	if got := todoNow(s, todo.ID).PRURL; got != "" {
		t.Fatalf("prose selected a saved reference as the PR: %q", got)
	}
	a := todo.Agents[0]
	openText := "Opened " + delivery
	openReply := artifactReportProof(s.hub.(*fakeHub), todo, openText, delivery)
	s.advanceTaskReplyAt(ctx, todo, a.MachineID, a.SessionID, openText, openReply.TS)
	if got := todoNow(s, todo.ID).PRURL; got != delivery {
		t.Fatalf("legacy single PR = %q, want %q", got, delivery)
	}
	roles := linkRoles(t, s, todo.ID)
	if roles[reference] != repository.LinkRoleReference || roles[other] != repository.LinkRoleReference || roles[delivery] != repository.LinkRoleDelivery {
		t.Fatalf("stored roles = %v, want only the single legacy PR as delivery", roles)
	}
}

// A decision whose question card the bot already posted (cards / all_cards) is not told again as waiting.
func TestTaskReportSkipsAskAlreadyInChat(t *testing.T) {
	s, f, posts := reportService(t, true)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusBlocked, "", 1)
	ix := proto.Interaction{ID: "ix2", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}}}}}
	keepChatCard(s, ix.ID, &botCard{}) // claimed: the bot is posting its card
	s.taskReport(todo, reportNeeds, "box", [2]string{"box", "s1"}, noticeAsk(ix), &ix)
	time.Sleep(6 * testReportDelay)
	if got := posts(); len(got) != 0 {
		t.Fatalf("posted %q", got)
	}
}

// Realistic replies: told as deployed or not (negated, deferred, asked). A false "deployed" sends a bogus "Please verify" card.
func TestSaidDeployed(t *testing.T) {
	for text, want := range map[string]bool{
		"已完成部署，请验收": true, "部署好了": true, "已经部署，不需要你再看": true, "已部署到 hub，不需要你再操作": true,
		"部署完成，链接 …": true, "上线了，可以去看看": true, "Deployed to staging; nothing pending.": true,
		"shipped and deployed, nothing pending": true, "It is live now": true,
		"not yet deployed": false, "hasn't been deployed yet": false, "it isn't fully deployed yet": false,
		"nothing has been deployed yet": false, "it has not been deployed": false, "先别部署到生产，等我确认": false,
		"无需部署到生产": false, "等你确认后再部署到生产": false, "待验证后再部署到生产": false, "还没部署到 hub，等我打声招呼": false,
		"尚未部署到 hub，先别验收": false, "准备部署到生产，等 CI": false, "等部署完再说": false, "部署了吗？": false,
		"Not deployed yet.": false, "we haven't deployed it yet": false, "never deployed": false,
		"awaiting deploy, will ping you": false, "It will be deployed after review": false,
		"还没合入。已部署到 hub": true, "PR 已合并，未部署（连不上 hub）": false,
	} {
		if got := said(deployWords, text); got != want {
			t.Errorf("said(%q) = %v, want %v", text, got, want)
		}
	}
}

// failingPosts replaces the FeishuPost answer: every card is recorded; report cards (the header emoji marks them)
// fail — the first attempt only, or every attempt with alwaysFail — anything else goes through.
func failingPosts(f *fakeHub, alwaysFail bool) (posts func() []string, reportAttempts func() int) {
	var mu sync.Mutex
	var got []string
	var attempts int
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		got = append(got, m.Card)
		isReport := strings.Contains(m.Card, `"content":"汇报"}`)
		if isReport {
			attempts++
		}
		fail := isReport && (alwaysFail || attempts == 1)
		mu.Unlock()
		if fail {
			return nil, errors.New("network jitter")
		}
		return map[string]any{"message_id": "om_r"}, nil
	})
	return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), got...)
		}, func() int {
			mu.Lock()
			defer mu.Unlock()
			return attempts
		}
}

func reportsState(s *Service) (n int, timer bool) {
	s.reports.mu.Lock()
	defer s.reports.mu.Unlock()
	return len(s.reports.pending), s.reports.timer != nil
}

// A transport failure keeps the whole batch (its terminal reports with it), schedules a retry; the second attempt
// posts the card and only then the needs decision card; success clears pending and the timer.
func TestTaskReportRetriesAfterTransportFailure(t *testing.T) {
	needsLarkLayer(t)
	s, f, _ := reportService(t, true)
	s.reports.mu.Lock()
	s.reports.retry = testReportDelay
	s.reports.mu.Unlock()
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusBlocked, "", 1)
	// A second todo, whose failure is reported together with the first one's decision.
	other := Todo{ID: "todo_b", Title: "修支付崩溃", Origin: "conv1"}
	s.todos.mu.Lock()
	s.todos.todos[other.ID] = &other
	s.todos.mu.Unlock()
	ix := proto.Interaction{ID: "ix-r1", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}}}}}
	f.answerApp(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) {
		return map[string]any{"ok": true, "interaction": ix}, nil
	})
	posts, attempts := failingPosts(f, false)
	s.taskReport(todo, reportNeeds, "box", [2]string{"box", "s1"}, noticeAsk(ix), &ix)
	s.taskReport(other, reportFailed, "box", [2]string{}, "另一台离线", nil)
	waitFor(t, "the first failed attempt", func() bool { return attempts() == 1 })
	waitFor(t, "the batch back in pending with a retry timer", func() bool {
		n, timer := reportsState(s)
		return n == 2 && timer
	})
	waitFor(t, "the retried report and the question card", func() bool { return len(posts()) == 3 })
	got := posts()
	for i := 0; i < 2; i++ {
		if !strings.Contains(got[i], "1 件事等你拍板") || !strings.Contains(got[i], "另一台离线") ||
			!strings.Contains(got[i], "修支付崩溃") {
			t.Fatalf("attempt %d report:\n%s", i+1, got[i])
		}
	}
	if !strings.Contains(got[2], `"a":"ix"`) || !strings.Contains(got[2], "main") {
		t.Fatalf("question card posted without the retried report:\n%s", got[2])
	}
	waitFor(t, "pending cleared after success", func() bool {
		n, timer := reportsState(s)
		return n == 0 && !timer
	})
	time.Sleep(4 * testReportDelay)
	if len(posts()) != 3 {
		t.Fatalf("%d posts after success", len(posts()))
	}
}

// While the bot is unpaired the batch stays dropped (a later card says how things are then): becoming ready does not
// deliver it, and no retry timer is left.
func TestTaskReportDroppedWhenBotNotReady(t *testing.T) {
	s, f, _ := reportService(t, true)
	todo := startedTask(t, s, f)
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: false}, nil
	})
	posts, _ := failingPosts(f, false)
	s.taskReport(todo, reportDone, "box", [2]string{}, "做完了", nil)
	time.Sleep(6 * testReportDelay)
	if got := posts(); len(got) != 0 {
		t.Fatalf("not ready, posted %q", got)
	}
	if n, timer := reportsState(s); n != 0 || timer {
		t.Fatalf("not ready: %d pending, timer=%v", n, timer)
	}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: FeishuFeatures{Main: true, Cards: true, Reports: true, ReportSteps: true}}, nil
	})
	time.Sleep(6 * testReportDelay)
	if got := posts(); len(got) != 0 {
		t.Fatalf("the dropped batch was sent after the bot got ready: %q", got)
	}
}

// After reportMaxRetries retries the batch is given up: the queue does not pin a dead transport forever.
func TestTaskReportGivesUpAfterMaxRetries(t *testing.T) {
	needsLarkLayer(t)
	s, f, _ := reportService(t, true)
	s.reports.mu.Lock()
	s.reports.retry = testReportDelay
	s.reports.mu.Unlock()
	todo := startedTask(t, s, f)
	posts, attempts := failingPosts(f, true)
	s.taskReport(todo, reportDone, "box", [2]string{}, "做完了", nil)
	want := reportMaxRetries + 1
	waitFor(t, "the first send and every retry to fail", func() bool { return attempts() == want })
	time.Sleep(8 * testReportDelay)
	if got := attempts(); got != want {
		t.Fatalf("%d report attempts, want %d then stop", got, want)
	}
	if n, timer := reportsState(s); n != 0 || timer {
		t.Fatalf("after giving up: %d pending, timer=%v", n, timer)
	}
	if len(posts()) != want {
		t.Fatalf("%d posts, want %d", len(posts()), want)
	}
}

// A newer change of the same todo arriving while a failed batch waits for retry replaces the failed item; the retry
// sends the newer state.
func TestTaskReportRetryKeepsNewerChange(t *testing.T) {
	needsLarkLayer(t)
	s, f, _ := reportService(t, true)
	s.reports.mu.Lock()
	s.reports.retry = time.Second
	s.reports.mu.Unlock()
	todo := startedTask(t, s, f)
	posts, attempts := failingPosts(f, false)
	s.taskReport(todo, reportFailed, "box", [2]string{}, "第一次失败原因", nil)
	waitFor(t, "the first failed attempt", func() bool { return attempts() == 1 })
	waitFor(t, "the batch back in pending", func() bool {
		n, timer := reportsState(s)
		return n == 1 && timer
	})
	s.taskReport(todo, reportDone, "box", [2]string{"box", "s1"}, "第二次进展", nil)
	waitFor(t, "the retried report", func() bool { return attempts() == 2 && len(posts()) == 2 })
	card := posts()[1]
	if !strings.Contains(card, "第二次进展") || !strings.Contains(card, "这一轮做完") {
		t.Fatalf("retried card lacks the newer done change:\n%s", card)
	}
	if strings.Contains(card, "第一次失败原因") || strings.Contains(card, "没开起来") {
		t.Fatalf("retried card still carries the superseded failure:\n%s", card)
	}
	waitFor(t, "pending cleared", func() bool {
		n, timer := reportsState(s)
		return n == 0 && !timer
	})
}

// A newer change of the same todo arriving while the first card is still being posted merges inside requeueReports:
// the superseded item is never posted, the retry carries the newer state. The first post is gated open only after the
// newer change landed.
func TestTaskReportMergesChangeArrivedDuringPost(t *testing.T) {
	needsLarkLayer(t)
	s, f, _ := reportService(t, true)
	s.reports.mu.Lock()
	s.reports.delay = time.Second // the newer change's normal-delay timer must not fire while the post is gated
	s.reports.retry = testReportDelay
	s.reports.mu.Unlock()
	todo := startedTask(t, s, f)
	var mu sync.Mutex
	var got []string
	var attempts int
	entered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		got = append(got, m.Card)
		firstReport := strings.Contains(m.Card, `"content":"汇报"}`)
		if firstReport {
			attempts++
		}
		first := firstReport && attempts == 1
		mu.Unlock()
		if first {
			once.Do(func() { close(entered) })
			<-gate
			return nil, errors.New("network jitter")
		}
		return map[string]any{"message_id": "om_r"}, nil
	})
	state := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return attempts, len(got)
	}
	cards := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
	s.taskReport(todo, reportFailed, "box", [2]string{}, "第一次失败原因", nil)
	<-entered // the first post is in flight, pending already cleared
	s.taskReport(todo, reportDone, "box", [2]string{}, "第二次进展", nil)
	close(gate)
	waitFor(t, "the retried report", func() bool {
		a, n := state()
		return a == 2 && n == 2
	})
	card := cards()[1]
	if !strings.Contains(card, "第二次进展") || !strings.Contains(card, "这一轮做完") {
		t.Fatalf("retried card lacks the newer done change:\n%s", card)
	}
	if strings.Contains(card, "第一次失败原因") || strings.Contains(card, "没开起来") {
		t.Fatalf("the superseded failure was posted:\n%s", card)
	}
	waitFor(t, "pending cleared", func() bool {
		n, timer := reportsState(s)
		return n == 0 && !timer
	})
}

// A failed read of the stages keeps the book unloaded: a stage or delivery set meanwhile is not written over every
// other task's, and the next look reads the stored stages again.
func TestStagesReadFailureNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	st := &downStore{memStore: &memStore{}}
	keep := map[string]todoStage{"a": {Stage: "review", PR: "https://git.example.com/team/repo/pull/1",
		Delivery: &TodoDelivery{State: deliveryPR}}}
	if err := st.PutSetting(ctx, reportStagesKey, keep); err != nil {
		t.Fatal(err)
	}
	s := newServiceOn(t, newFakeHub().withMachine(), st)

	st.failing(true)
	if got := s.todoStages(ctx); len(got) != 0 {
		t.Fatalf("stages while unreadable: %v", got)
	}
	st.failing(false)
	s.setTaskStage(ctx, "b", "review", "")
	s.setTaskDelivery(ctx, "c", TodoDelivery{State: deliveryMerged})

	var stored map[string]todoStage
	if _, err := st.Setting(ctx, reportStagesKey, &stored); err != nil {
		t.Fatal(err)
	}
	a := stored["a"]
	if a.Stage != "review" || a.PR != keep["a"].PR || a.Delivery == nil || a.Delivery.State != deliveryPR {
		t.Fatalf("task a after a failed read: %+v (stored %v)", a, stored)
	}
	if stored["b"].Stage != "review" || stored["c"].Delivery == nil || stored["c"].Stage != "merge" {
		t.Fatalf("the later changes were not kept: %v", stored)
	}

	// While the read keeps failing, nothing is written and nothing is cached.
	st.failing(true)
	s.reports.mu.Lock()
	s.reports.loaded, s.reports.stages = false, nil
	s.reports.mu.Unlock()
	s.setTaskStage(ctx, "d", "review", "")
	s.setTaskDelivery(ctx, "e", TodoDelivery{State: deliveryPR})
	st.failing(false)
	stored = nil
	if _, err := st.Setting(ctx, reportStagesKey, &stored); err != nil {
		t.Fatal(err)
	}
	if _, ok := stored["d"]; ok || len(stored) != 3 {
		t.Fatalf("written while unreadable: %v", stored)
	}
	if got := s.todoStages(ctx); len(got) != 3 {
		t.Fatalf("stages after the store is back: %v", got)
	}
}
