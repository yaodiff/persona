package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskStageMergeRequestLinks(t *testing.T) {
	for _, url := range []string{
		"https://gitlab.example.com/group/repo/merge_requests/42",
		"https://example.com/group/subgroup/repo/-/merge_requests/17",
		"https://example.com:8443/group/repo/-/merge_requests/8",
	} {
		for _, tc := range []struct{ reply, stage string }{
			{"Opened [MR](" + url + ").", "review"},
			{"Merged " + url, "merge"},
			{url + " is not merged yet", "review"},
			{"Not merged: " + url, "review"},
		} {
			stage, pr := stageOf(tc.reply)
			if stage != tc.stage || pr != url {
				t.Errorf("stageOf(%q) = %s %s; want %s %s", tc.reply, stage, pr, tc.stage, url)
			}
		}
	}
	for _, reply := range []string{"MR !42 merged", "MR !42 awaiting review", "https://example.com/group/repo/-/merge_requests/not-a-number"} {
		stage, pr := stageOf(reply)
		if stage != "develop" || pr != "" {
			t.Errorf("unlinked MR %q = %s %s", reply, stage, pr)
		}
	}
}

func TestTaskStageMidTaskReply(t *testing.T) {
	s, f := newTaskServiceOn(t, repositorystore.New(todoTestDB(t)))
	todo := startedTask(t, s, f)
	ctx := context.Background()
	if got := todoNow(s, todo.ID); got.StageKind != "general" {
		t.Fatalf("read-only task kind = %s", got.StageKind)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	url := "https://gitlab.example.com/group/repo/-/merge_requests/42"
	reply := artifactReportProof(f, todo, "Opened "+url, url)
	// No TurnEnded: a progress reply from the list must update a running task.
	s.SessionsChanged(ctx, DeviceSessions{MachineID: "box", Sessions: []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: reply.Text, UpdatedAt: reply.TS}}})
	if got := todoNow(s, todo.ID); got.Stage != "review" || got.PRURL != url || got.StageKind != "code" || got.DoneAt != nil {
		t.Fatalf("mid-task review = %+v", got)
	}
	s.SessionItems(ctx, SessionItems{MachineID: "box", SessionID: "s1", Items: []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: "MR merged"}}})
	if got := todoNow(s, todo.ID); got.Stage != "merge" || got.PRURL != url {
		t.Fatalf("mid-task merge = %+v", got)
	}
	s.SessionItems(ctx, SessionItems{MachineID: "box", SessionID: "s1", Items: []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: "Already deployed and verified"},
		{Role: "assistant", Kind: proto.KindToolResult, Text: "Already deployed and verified"},
		{Role: "assistant", Kind: proto.KindText, Text: "Still developing"},
	}})
	if got := todoNow(s, todo.ID); got.Stage != "merge" || got.DoneAt != nil {
		t.Fatalf("non-answer or earlier stage changed progress: %+v", got)
	}
}

func TestTaskStageClippedProgressReply(t *testing.T) {
	s, f := newTaskServiceOn(t, repositorystore.New(todoTestDB(t)))
	todo := startedTask(t, s, f)
	ctx := context.Background()
	url := "https://example.com/group/repo/-/merge_requests/73"
	f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: strings.Repeat("Checked the change. ", 20) + "Merged " + url})
	f.mu.Lock()
	reads := f.itemReads
	f.mu.Unlock()
	d := DeviceSessions{MachineID: "box", Sessions: []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "Merged " + url + "…"}}}
	s.taskStageSessions(ctx, d)
	s.taskStageSessions(ctx, d)
	f.mu.Lock()
	gotReads := f.itemReads
	f.mu.Unlock()
	if gotReads != reads {
		t.Fatalf("clipped reply fetched a transcript: %d -> %d", reads, gotReads)
	}
	if got := todoNow(s, todo.ID); stageIndex(got.Stage) > stageIndex("develop") || got.PRURL != "" {
		t.Fatalf("clipped list reply advanced progress: %+v", got)
	}
	text := "Merged " + url
	s.taskStageItems(ctx, SessionItems{MachineID: "box", SessionID: "s1", Items: []proto.Item{{Role: "assistant", Kind: proto.KindText, Text: text, FullSize: len(text) + 100}}})
	if got := todoNow(s, todo.ID); stageIndex(got.Stage) > stageIndex("develop") || got.PRURL != "" {
		t.Fatalf("clipped item advanced progress: %+v", got)
	}
	reply := artifactReportProof(f, todo, text, url)
	s.taskStageItems(ctx, SessionItems{MachineID: "box", SessionID: "s1", Items: []proto.Item{reply}})
	if got := todoNow(s, todo.ID); got.Stage != "merge" || got.PRURL != url {
		t.Fatalf("complete item missed its MR: %+v", got)
	}
}

func TestTaskStageDeliveryPriority(t *testing.T) {
	url := "https://example.com/g/r/merge_requests/3"
	for _, tc := range []struct{ mark, stage string }{
		{"停在 PR", "review"}, {"已合未上线", "merge"}, {"默认关待启用", "deploy"}, {"已上线并实测", "deploy"}, {"不涉及上线", "develop"},
	} {
		reply := "Merged " + url + ", deployed and verified\n交付：" + tc.mark
		if got, pr := stageOf(reply); got != tc.stage || pr != url {
			t.Errorf("mark %s = %s %s, want %s", tc.mark, got, pr, tc.stage)
		}
	}
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	// A later sentence can omit the known link without reducing a deployed result to merge.
	s.setTaskStage(ctx, todo.ID, "merge", url)
	s.advanceTaskReply(ctx, todoNow(s, todo.ID), "box", "s1", "Merged and deployed")
	if got := todoNow(s, todo.ID); got.Stage != "deploy" {
		t.Fatalf("link-free deployed reply = %s", got.Stage)
	}
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryLive})
	s.taskStageSessions(ctx, DeviceSessions{MachineID: "box", Sessions: []proto.Session{{SessionID: "s1", LastText: "交付：已合未上线"}}})
	if got := todoNow(s, todo.ID); got.Stage != "deploy" {
		t.Fatalf("old delivery moved deployed progress back: %s", got.Stage)
	}
	s.setTaskStage(ctx, todo.ID, "verify", url)
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR})
	if got := todoNow(s, todo.ID); got.Stage != "verify" {
		t.Fatalf("delivery moved verified progress back: %s", got.Stage)
	}
	s.setTaskStageKind(ctx, todo.ID, "verify", "", "", true)
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged})
	if got := todoNow(s, todo.ID); got.Stage != "verify" {
		t.Fatalf("explicit acceptance lost: %s", got.Stage)
	}
	s.setTaskStageKind(ctx, todo.ID, "unverified", "", "", false)
	if got := s.todoStages(ctx)[todo.ID]; got.Stage != "merge" || got.Verified {
		t.Fatalf("explicit review did not remove verification: %+v", got)
	}
}

func TestTaskStageDeliveryGate(t *testing.T) {
	for _, tc := range []struct {
		name, delivery, hub, reply, want string
		deployed                         bool
	}{
		{"confirmed deploy", deliveryMerged, newHub, "Merged and deployed", "deploy", true},
		{"confirmed verification", deliveryMerged, newHub, "Deployed and verified", "verify", true},
		{"held deploy", deliveryPR, oldHub, "Deployed and verified", "merge", false},
		{"delivery caps merge guess", deliveryPR, oldHub, "Merged", "review", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _, hub, _ := deployService(t)
			hub.deploy(tc.hub)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.setTaskStage(ctx, todo.ID, "review", "https://github.com/o/r/pull/9")
			s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: tc.delivery})
			if got := s.advanceTaskReply(ctx, todoNow(s, todo.ID), "box", "s1", tc.reply); got != tc.deployed {
				t.Fatalf("deployed = %v, want %v", got, tc.deployed)
			}
			if got := todoNow(s, todo.ID).Stage; got != tc.want {
				t.Fatalf("stage = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestTaskStagePlanFollowsAgent(t *testing.T) {
	for _, remaining := range []bool{false, true} {
		t.Run(map[bool]string{false: "only agent", true: "another pending plan"}[remaining], func(t *testing.T) {
			s, f := newTaskService(t)
			old := startedTask(t, s, f)
			ctx := context.Background()
			next, err := s.createTodo(ctx, "Next task", "", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			s.setTaskStage(ctx, old.ID, "develop", "")
			s.setTaskStage(ctx, next.ID, "develop", "")
			if remaining {
				if err := s.linkTodoAgent(ctx, old.ID, "box", "s2", "", "", ""); err != nil {
					t.Fatal(err)
				}
			}
			ix := proto.Interaction{ID: "moving_plan", MachineID: "box", SessionID: "s1", Kind: proto.IxPlan}
			s.fleet.mu.Lock()
			s.fleet.interactions = map[string]*pending{ix.ID: {ix: ix}}
			if remaining {
				s.fleet.interactions["remaining_plan"] = &pending{ix: proto.Interaction{ID: "remaining_plan", MachineID: "box", SessionID: "s2", Kind: proto.IxPlan}}
			}
			s.fleet.mu.Unlock()
			s.taskPlanApproval()
			if got := todoNow(s, old.ID).Stage; got != "plan" {
				t.Fatalf("original stage = %s", got)
			}
			if err := s.linkTodoAgent(ctx, next.ID, "box", "s1", "", "", ""); err != nil {
				t.Fatal(err)
			}
			wantOld := "develop"
			if remaining {
				wantOld = "plan"
			}
			if got := todoNow(s, old.ID).Stage; got != wantOld {
				t.Fatalf("old task stage after relink = %s, want %s", got, wantOld)
			}
			if got := todoNow(s, next.ID).Stage; got != "plan" {
				t.Fatalf("new task stage after relink = %s", got)
			}
			s.finishInteraction(ix.ID)
			if got := todoNow(s, next.ID).Stage; got != "develop" {
				t.Fatalf("new task stage after approval = %s", got)
			}
			if got := todoNow(s, old.ID).Stage; got != wantOld {
				t.Fatalf("old task stage after approval = %s, want %s", got, wantOld)
			}
		})
	}
}

func TestTaskStagePendingPlan(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	// A permission or question is not evidence of a plan waiting for approval.
	ix := proto.Interaction{ID: "plan_stage", MachineID: "box", SessionID: "s1", Kind: proto.IxPermission}
	s.fleet.mu.Lock()
	s.fleet.interactions = map[string]*pending{ix.ID: {ix: ix}}
	s.fleet.mu.Unlock()
	s.taskPlanApproval()
	if todoNow(s, todo.ID).Stage != "develop" {
		t.Fatal("permission guessed as plan")
	}
	ix.Kind = proto.IxPlan
	s.fleet.mu.Lock()
	s.fleet.interactions[ix.ID].ix = ix
	s.fleet.mu.Unlock()
	s.InteractionRaised(ctx, InteractionRaised{Interaction: ix})
	if got := todoNow(s, todo.ID); got.Stage != "plan" {
		t.Fatalf("pending plan stage = %s", got.Stage)
	}
	if stored := s.todoStages(ctx)[todo.ID].Stage; stored != "develop" {
		t.Fatalf("pending plan rolled back the stored stage: %s", stored)
	}
	// Starting another turn is not proof that the plan was approved.
	s.taskWorking(ctx, "box", "s1", startRow{})
	if todoNow(s, todo.ID).Stage != "plan" {
		t.Fatal("working status cleared pending plan")
	}
	s.finishInteraction(ix.ID)
	if todoNow(s, todo.ID).Stage != "develop" {
		t.Fatal("resolved plan still current")
	}
}

func TestTaskStageReviewVerdict(t *testing.T) {
	for _, tc := range []struct {
		name      string
		userCheck bool
		outcome   string
		want      string
	}{
		{"accepted without verification", false, "done", "merge"},
		{"owner-check", true, "done", "merge"},
		{"cannot", false, "cannot", "merge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged})
			_, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
				v.Supervisor = "conv1"
				v.State = reviewPending
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "Review complete", "user_check": tc.userCheck, "outcome": tc.outcome})
			if _, err := s.toolTaskReview(ctx, "conv1", raw); err != nil {
				t.Fatal(err)
			}
			if got := s.todoStages(ctx)[todo.ID]; got.Stage != tc.want || got.Verified {
				t.Fatalf("stage = %+v, want %s without verification", got, tc.want)
			}
		})
	}
}

func TestTaskStageMetadataRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, _ := newTaskServiceOn(t, st)
	todo, err := s.createTodo(ctx, "Research report", "Inspect the results", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.setTaskStageKind(ctx, todo.ID, "develop", "", "general", false)
	var stored map[string]todoStage
	if _, err := st.Setting(ctx, reportStagesKey, &stored); err != nil {
		t.Fatal(err)
	}
	if stored[todo.ID].Kind != "general" {
		t.Fatalf("stored kind = %+v", stored)
	}
	wire, err := json.Marshal(formatTodo(todoNow(s, todo.ID)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"stage_kind":"general"`) {
		t.Fatalf("wire = %s", wire)
	}
	// Loading by a new service preserves the setting without a schema migration.
	s2, _ := newTaskServiceOn(t, st)
	if got := s2.todoStages(ctx)[todo.ID].Kind; got != "general" {
		t.Fatalf("reloaded kind = %s", got)
	}
	if s.briefingOn() {
		b := s.briefBodyOf(&briefFacts{Progress: []briefProgress{{ID: todo.ID, Stage: "develop", StageKind: "general"}}}, "progress")
		if b.Progress[0].StageKind != "general" {
			t.Fatal("briefing omitted kind")
		}
	}
}
