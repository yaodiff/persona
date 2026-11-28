package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The kind reads evidence first, then what the work is asked to deliver, never a word that only names what is read,
// tried or shipped (#11590).
func TestTaskStageKind(t *testing.T) {
	for _, tc := range []struct {
		name     string
		readonly bool
		goal     string
		criteria []string
		changed  bool
		want     string
	}{
		// What a code task asks for
		{"fix in the goal", false, "Fix the login crash", []string{"The crash no longer happens"}, false, "code"},
		{"Chinese fix with tests", false, "", []string{"修复登录错误，全部测试通过"}, false, "code"},
		{"a title that says fix", false, "修登录崩溃", nil, false, "code"},
		{"research, then implement", false, "", []string{"Research the bug and implement a fix"}, false, "code"},
		{"fix, then verify", false, "", []string{"Fix the crash, then verify it on a device"}, false, "code"},
		{"merge request delivery", false, "", []string{"MR merged and deployed"}, false, "code"},
		{"merged into main", false, "", []string{"合入 main 并部署上线"}, false, "code"},
		{"open a PR", false, "", []string{"相关检查通过，开 PR 关联该 issue（只开 PR，不合并）"}, false, "code"},
		{"CI", false, "", []string{"远端 CI 全过"}, false, "code"},
		{"Japanese implementation", false, "", []string{"新しい API を実装する"}, false, "code"},
		{"a change seen outranks read-only", true, "Research report", nil, true, "code"},

		// What a non-code task asks for
		{"nothing asked", false, "", nil, false, "general"},
		{"read-only", true, "Fix the login crash", []string{"PR merged"}, false, "general"},
		{"research", false, "", []string{"Publish the research report"}, false, "general"},
		{"Chinese research", false, "", []string{"调研并给出方案"}, false, "general"},
		{"no kind of work named", false, "", []string{"Everything works"}, false, "general"},
		{"doc review", false, "", []string{"Doc review complete"}, false, "general"},
		{"shipping words alone", false, "", []string{"合并、部署、上线"}, false, "general"},
		{"release alone", false, "", []string{"Release shipped"}, false, "general"},
		{"deploy to verify", false, "", []string{"部署到测试环境并验证"}, false, "general"},
		{"test with real data", false, "", []string{"Test the rule with real data"}, false, "general"},
		{"cite the code", false, "", []string{"结论附代码出处"}, false, "general"},
		{"cite the code, English", false, "", []string{"Cite the code for each finding"}, false, "general"},
		{"verify the fix", false, "", []string{"Verify the fix works on staging"}, false, "general"},
		{"list what needs fixing", false, "", []string{"列出需要修复的地方"}, false, "general"},
		{"don't change the code", false, "", []string{"Do not change the code; report what you find"}, false, "general"},
		{"不改代码", false, "", []string{"不改代码，只出报告"}, false, "general"},
		{"a person named Mr", false, "Call Mr Lee about the boiler", nil, false, "general"},
		{"edit a document", false, "修改文档里的日期", nil, false, "general"},
		{"Japanese research", false, "", []string{"仕様を調査して報告する"}, false, "general"},
		{"whether", false, "", []string{"Decide whether to fix it"}, false, "general"},
		// A verification whose third criterion says what to do if the check turns something up: the conditional
		// sentence and its "otherwise leave the code alone" ask for nothing (the reported case, in other words).
		{"verification with a conditional fix", false, "用真实数据验证规则的判定结果", []string{
			"权限生效，能读到真实数据",
			"在测试环境用真实数据验证判定结果，结论写进文档",
			"若验证中发现判定口径或代码问题：修好、补单测、远端 CI 全过并推送，重新部署测试环境再验证；没问题则不改代码",
		}, false, "general"},
		{"English conditional", false, "Check the new rule against real data", []string{
			"If the check finds a bug, fix it, add unit tests and redeploy. Otherwise leave the code alone.",
		}, false, "general"},
		{"when-clause", false, "", []string{"When the check fails, open a PR with the fix"}, false, "general"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskStageKind(tc.readonly, tc.goal, tc.criteria, tc.changed); got != tc.want {
				t.Fatalf("kind = %s, want %s", got, tc.want)
			}
		})
	}
}

// planTaskArgs is a one-task plan on box with its goal and criteria.
func planTaskArgs(readonly bool, title, goal string, doneWhen ...string) json.RawMessage {
	permission := "bypassPermissions"
	if readonly {
		permission = "plan"
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": title, "prompt": title, "machine": "box", "cwd": "/code/app", "readonly": readonly, "why": "the repository is on box",
		"permission": permission, "goal": goal, "done_when": doneWhen, "expect_minutes": 30,
	}}})
	return raw
}

// startPlanTask plans args in conv1 and starts it as the card's Start does (a writable task waits for it).
func startPlanTask(t *testing.T, s *Service, args json.RawMessage) Todo {
	t.Helper()
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv1", args)
	if err != nil {
		t.Fatal(err)
	}
	if onlyTodo(s).ID == "" {
		if _, err := s.startPlan(ctx, planID(t, res), nil, false); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	return onlyTodo(s)
}

// Without delivery evidence, new execute tasks start general regardless of the words in their criteria.
func TestTaskStageKindAtStart(t *testing.T) {
	s, _ := newTaskService(t)
	todo := startPlanTask(t, s, planTaskArgs(false, "Check the rule with real data", "Check the new rule against real data",
		"The rule's results are checked against real data",
		"If the check finds a bug, fix it, add unit tests and redeploy; otherwise leave the code alone"))
	if got := todoNow(s, todo.ID).StageKind; got != "general" {
		t.Fatalf("verification kind = %s", got)
	}
	s2, _ := newTaskService(t)
	unbound := startPlanTask(t, s2, planTaskArgs(false, "Login crash", "Fix the login crash", "PR merged with a regression test"))
	if brief, ok := s2.briefOfTodo(unbound.ID); !ok || brief.WorkModeNow != assistantwire.WorkModeExecute || unbound.StageKind != "general" {
		t.Fatalf("unbound task = %s, brief = %+v", unbound.StageKind, brief)
	}
}

func TestTaskStageKindUsesRepositoryAndCurrentMode(t *testing.T) {
	ctx := t.Context()
	st := newMemRepoStore()
	s, _ := newQuietServiceOn(t, st)
	todo, err := s.newTodo(ctx, "Prepare the artifact", "", "conv1", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetTodoRepositories(ctx, todo.ID, repository.SetInput{Repositories: []repository.TaskInput{{RepoID: "repo_o_r"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.putTaskBrief(ctx, briefRecord{
		taskContract: taskContract{WorkMode: assistantwire.WorkModeExecute},
		Goal:         todo.Title,
		DoneWhen:     []string{"The artifact is ready"},
	}, todo.ID); err != nil {
		t.Fatal(err)
	}
	s.restage(ctx, todo.ID, nil)
	if got := todoNow(s, todo.ID).StageKind; got != "code" {
		t.Fatalf("execute with a repository = %q, want code", got)
	}
	for _, mode := range []assistantwire.WorkMode{assistantwire.WorkModeResearch, assistantwire.WorkModeCoordinate, assistantwire.WorkModeExecute} {
		if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
			"todo_id": todo.ID, "change": "Update the work mode", "why": "The required work changed",
			"work_mode": mode, "done_when": []string{"Fix and merge the PR"},
		}); err != nil {
			t.Fatal(err)
		}
		want := "general"
		if mode == assistantwire.WorkModeExecute {
			want = "code"
		}
		if got := todoNow(s, todo.ID).StageKind; got != want {
			t.Fatalf("%s with a repository = %q, want %s", mode, got, want)
		}
	}
}

// The read-only start is kept: a later recompute (new criteria through agent_supervise, a task without a kind) does not
// make the task code by words; a change seen does.
func TestTaskStageKindKeepsReadonly(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	todo := startPlanTask(t, s, planTaskArgs(true, "Research the crash", "Find out why the login page crashes"))
	if got := todoNow(s, todo.ID); got.StageKind != "general" {
		t.Fatalf("read-only kind = %s", got.StageKind)
	}
	if !s.todoStages(ctx)[todo.ID].Readonly {
		t.Fatalf("stage record = %+v, want readonly kept", s.todoStages(ctx)[todo.ID])
	}
	if brief, ok := s.briefOfTodo(todo.ID); !ok || brief.WorkModeNow != assistantwire.WorkModeResearch {
		t.Fatalf("read-only brief = %+v, want research", brief)
	}
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.DoneWhen = []string{"Fix the crash and open a PR"}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.restage(ctx, todo.ID, nil)
	if got := todoNow(s, todo.ID).StageKind; got != "general" {
		t.Fatalf("read-only task after new criteria = %s", got)
	}
	// A closeout naming a commit is a change seen, read-only start or not.
	reply := "Pushed the fix.\n<closeout status=\"done\" merged=\"no\" deployed=\"n/a\">\nartifact: commit 1a2b3c4\n</closeout>"
	s.advanceTaskReply(ctx, todoNow(s, todo.ID), "box", "s1", reply)
	if got := todoNow(s, todo.ID).StageKind; got != "code" {
		t.Fatalf("after a commit artifact = %s", got)
	}
	// It stays code when the criteria are read again.
	s.restage(ctx, todo.ID, nil)
	if got := s.todoStages(ctx)[todo.ID]; got.Kind != "code" || !got.Change {
		t.Fatalf("after restage = %+v", got)
	}
}

func TestTaskStageChangeSeen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply string
		want  bool
	}{
		{"commit artifact", "<closeout status=\"done\">\nartifact: commit 1a2b3c4\n</closeout>", true},
		{"branch artifact", "<closeout status=\"done\">\nartifact: branch fix/login\n</closeout>", true},
		{"doc artifact", "<closeout status=\"done\">\nartifact: doc https://example.com/report\n</closeout>", false},
		{"held at a PR", "交付：停在 PR；卡点：等评审；谁来解：维护者", true},
		{"merged, not live", "交付：已合未上线；卡点：等部署；谁来解：用户", true},
		{"nothing to ship", "交付：不涉及上线；卡点：无；谁来解：无", false},
		{"plain progress", "Still reading the code.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, marked := deliveryMark(tc.reply)
			if got := changeSeen(tc.reply, d, marked); got != tc.want {
				t.Fatalf("changeSeen = %v, want %v", got, tc.want)
			}
		})
	}
}

// Closed with nothing left to deploy, the strip ends with Done; deployed, held, cancelled, failed or general work does not.
func TestTaskStageDone(t *testing.T) {
	done := time.Now()
	pr := "https://github.com/o/r/pull/7"
	for _, tc := range []struct {
		name string
		todo Todo
		want bool
	}{
		{"accepted at develop", Todo{DoneAt: &done, DoneBy: "review", Stage: "develop", StageKind: "code"}, true},
		{"closed by the judge at develop", Todo{DoneAt: &done, DoneBy: "auto", Stage: "develop", StageKind: "code"}, true},
		{"kind from an older hub", Todo{DoneAt: &done, Stage: "develop"}, true},
		{"nothing to ship after a merge", Todo{DoneAt: &done, Stage: "merge", StageKind: "code", PRURL: pr,
			Delivery: &TodoDelivery{State: deliveryNone}}, true},
		{"still open", Todo{Stage: "develop", StageKind: "code"}, false},
		{"general", Todo{DoneAt: &done, Stage: "develop", StageKind: "general"}, false},
		{"no stage", Todo{DoneAt: &done, StageKind: "code"}, false},
		{"cancelled", Todo{DoneAt: &done, DoneBy: "cancelled", Stage: "develop", StageKind: "code"}, false},
		{"cancelled, with its time", Todo{DoneAt: &done, CancelledAt: &done, Stage: "develop", StageKind: "code"}, false},
		{"failed start", Todo{DoneAt: &done, StartError: "offline", Stage: "develop", StageKind: "code"}, false},
		{"deployed", Todo{DoneAt: &done, Stage: "deploy", StageKind: "code", PRURL: pr}, false},
		{"verified", Todo{DoneAt: &done, Stage: "verify", StageKind: "code", PRURL: pr}, false},
		{"held at merge", Todo{DoneAt: &done, Stage: "merge", StageKind: "code", PRURL: pr,
			Delivery: &TodoDelivery{State: deliveryMerged}}, false},
		{"merged, delivery not said", Todo{DoneAt: &done, Stage: "merge", StageKind: "code", PRURL: pr}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := stageDone(tc.todo); got != tc.want {
				t.Fatalf("stageDone = %v, want %v", got, tc.want)
			}
		})
	}
}

// The apps get stage_done on a listed to-do, worked out from its state; it is never stored.
func TestTaskStageDoneOnTheWire(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, _ := newTaskServiceOn(t, st)
	todo, err := s.createTodo(ctx, "Upgrade the CLI", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.setTaskStageKind(ctx, todo.ID, "develop", "", "code", false)
	wire, _ := json.Marshal(formatTodo(todoNow(s, todo.ID)))
	if strings.Contains(string(wire), "stage_done") {
		t.Fatalf("open to-do wire = %s", wire)
	}
	closeTodo(t, s, todo.ID)
	wire, _ = json.Marshal(formatTodo(todoNow(s, todo.ID)))
	if !strings.Contains(string(wire), `"stage_done":true`) || !strings.Contains(string(wire), `"stage":"develop"`) {
		t.Fatalf("closed to-do wire = %s", wire)
	}
	var stored map[string]json.RawMessage
	if _, err := st.Setting(ctx, reportStagesKey, &stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored[todo.ID]), "done") {
		t.Fatalf("stored stage = %s", stored[todo.ID])
	}
}
