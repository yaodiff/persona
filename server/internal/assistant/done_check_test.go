package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

// agentStopped leaves task agent s1 on box stopped with reply as its last words, without telling the assistant its turn
// ended: a turn end the hub missed (it restarted in the middle of the turn).
func agentStopped(f *fakeHub, reply string) {
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: reply, UpdatedAt: time.Now()})
}

type doneCheckAnswer struct {
	Verdict    string `json:"verdict"`
	WouldClose bool   `json:"would_close"`
	Skip       string `json:"skip"`
	Applied    *bool  `json:"applied"`
	Stage      string `json:"stage"`
}

func doneCheck(t *testing.T, s *Service, id, body string) doneCheckAnswer {
	t.Helper()
	w := serveAs(s, "full", "POST", "/api/v1/todos/"+id+"/done-check", body)
	var out doneCheckAnswer
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("done-check %s: %d %s", body, w.Code, w.Body.String())
	}
	return out
}

// Re-check (#4867): without apply it only answers, as before; with apply it moves the stage from the agent's last reply
// (a turn end the hub missed) and writes the verdict. Even a legacy unsupervised task becomes delivered and supervised by
// the main conversation: a judge never closes it (#5732).
func TestDoneCheckApplyMarksAFinishedTaskDelivered(t *testing.T) {
	s, f := newTaskServiceOn(t, repositorystore.New(todoTestDB(t)))
	f.judge = `{"verdict":"done","reason":"修好了"}`
	todo := startedTask(t, s, f)
	if todo.Supervision != nil {
		t.Fatalf("supervised: %+v", todo.Supervision)
	}
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	reply := "修好了，PR https://github.com/o/r/pull/9 已合并"
	artifactReportProof(f, todo, reply, "https://github.com/o/r/pull/9")
	agentStopped(f, reply)

	got := doneCheck(t, s, todo.ID, "")
	if got.Verdict != verdictDone || got.WouldClose || got.Applied != nil {
		t.Fatalf("answer: %+v", got)
	}
	if now := todoNow(s, todo.ID); now.DoneAt != nil || now.Check != nil || stageIndex(now.Stage) >= stageIndex("review") {
		t.Fatalf("written without apply: done %v, check %+v, stage %q", now.DoneAt, now.Check, now.Stage)
	}

	got = doneCheck(t, s, todo.ID, `{"apply":true}`)
	if got.Verdict != verdictDone || got.WouldClose || got.Applied == nil || !*got.Applied || got.Stage != "merge" {
		t.Fatalf("applied: %+v", got)
	}
	now := todoNow(s, todo.ID)
	if now.DoneAt != nil || now.Check == nil || now.Check.Verdict != verdictDone || now.Stage != "merge" || now.PRURL != "https://github.com/o/r/pull/9" {
		t.Fatalf("after apply: done %v, check %+v, stage %q, pr %q", now.DoneAt, now.Check, now.Stage, now.PRURL)
	}
	if now.Supervision == nil || now.Supervision.Supervisor != "conv1" || now.Supervision.State != reviewPending {
		t.Fatalf("not delivered to the main reviewer: %+v", now.Supervision)
	}
	if again := doneCheck(t, s, todo.ID, `{"apply":true}`); again.Verdict != verdictDone || again.WouldClose || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("a delivered task closed on another check: %+v", again)
	}
}

// A supervised task the judge finds done is not closed by Re-check: it waits for its supervisor's review, which is told.
// A working agent's task, or a body that is not JSON, is not applied.
func TestDoneCheckApplySupervisedWaitsForReview(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if got := doneCheck(t, s, todo.ID, `{"apply":true}`); got.Applied == nil || *got.Applied {
		t.Fatalf("applied while its agent works: %+v", got)
	}
	agentStopped(f, "修好了，提了 PR")
	if w := serveAs(s, "full", "POST", "/api/v1/todos/"+todo.ID+"/done-check", `{"apply":`); w.Code != http.StatusBadRequest {
		t.Fatalf("a broken body: %d", w.Code)
	}
	got := doneCheck(t, s, todo.ID, `{"apply":true}`)
	if got.Applied == nil || !*got.Applied {
		t.Fatalf("not applied: %+v", got)
	}
	waitFor(t, "waiting for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if now := todoNow(s, todo.ID); now.DoneAt != nil {
		t.Fatalf("a supervised task closed by Re-check: %+v", now)
	}
}

// Re-check keeps an explicit owner action even when the rest of the judgment failed. The visible wait and its one push
// do not depend on the judge being configured or available (#5732).
func TestDoneCheckApplyKeepsOwnerActionWhenJudgeFails(t *testing.T) {
	t.Run("model failure", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		f.judge = ""
		agentStopped(f, "Spec 已完成，下一步由你发起实现。")
		w := serveAs(s, "full", "POST", "/api/v1/todos/"+todo.ID+"/done-check", `{"apply":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("a deterministic owner action was discarded with the judge error: %d %s", w.Code, w.Body.String())
		}
		var out doneCheckAnswer
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Verdict != verdictNeedsUser || out.Applied == nil || !*out.Applied {
			t.Fatalf("applied owner action = %+v, %v", out, err)
		}
		if cur := todoNow(s, todo.ID); cur.Supervision.Waits == nil {
			t.Fatalf("the judge failure discarded the owner wait: %+v", cur.Supervision)
		}
		ownerWaitHeldBySupervisor(t, s, f, todo.ID)
		doneCheck(t, s, todo.ID, `{"apply":true}`)
		ownerWaitHeldBySupervisor(t, s, f, todo.ID)
	})

	t.Run("model not configured", func(t *testing.T) {
		s, f, _ := modelService(t)
		setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
		f.known["conv1"] = true
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()
		todo, err := s.createTodo(ctx, "Prepare the release", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
		if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", todo.Title, "/code/app", ""); err != nil {
			t.Fatal(err)
		}
		agentStopped(f, "Spec 已完成，下一步由你发起实现。")
		w := serveAs(s, "full", "POST", "/api/v1/todos/"+todo.ID+"/done-check", `{"apply":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("an unconfigured judge discarded the deterministic owner action: %d %s", w.Code, w.Body.String())
		}
		if cur := todoNow(s, todo.ID); cur.Supervision.Waits == nil {
			t.Fatalf("an unconfigured judge discarded the owner wait: %+v", cur.Supervision)
		}
		ownerWaitHeldBySupervisor(t, s, f, todo.ID)
		doneCheck(t, s, todo.ID, `{"apply":true}`)
		ownerWaitHeldBySupervisor(t, s, f, todo.ID)
	})
}

func TestDoneCheckApplyIndeterminateJudgeFailureStaysBadGateway(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	f.judge = ""
	agentStopped(f, "The implementation is finished.")
	w := serveAs(s, "full", "POST", "/api/v1/todos/"+todo.ID+"/done-check", `{"apply":true}`)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("an indeterminate judge failure = %d %s, want 502", w.Code, w.Body.String())
	}
	if cur := todoNow(s, todo.ID); cur.Supervision.State == reviewNeedsUser || cur.Supervision.Waits != nil || len(f.pushed()) != 0 {
		t.Fatalf("an indeterminate failure invented an owner action: %+v, pushes=%+v", cur.Supervision, f.pushed())
	}
}
