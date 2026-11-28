package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// GET /api/v1/todos and GET /api/v1/todos/{id} emit the server-computed strongly-typed TodoWait on waiting and
// parked rows and omit it on non-waiting rows (#16168).
func TestTodosRoutesEmitTypedWait(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Second)
		sentAt := now.Add(-20 * time.Minute)
		until := now.Add(24 * time.Hour)
		wakeAt := now.Add(30 * time.Minute)

		plain := e.create("Water the plants").ID
		parkedID := e.create("Confirm venue booking").ID
		agentWaitID := e.create("Connect login UI").ID

		_, _, err := e.s.updateSupervision(ctx, parkedID, func(v *Supervision) bool {
			v.Supervisor = "conv_main"
			v.State = reviewParked
			v.UpdatedAt = now.Add(-15 * time.Minute)
			v.Reviews = []TaskReview{{
				At:        now.Add(-15 * time.Minute),
				Verdict:   reviewParked,
				Reason:    "Waiting for Alex to confirm the room quote",
				Until:     until,
				WaitingOn: parkSomeoneElse,
				Parks:     1,
				Evidence: &ParkEvidence{
					Kind:      "person_reply",
					Target:    "Alex",
					Wake:      "Alex replies in the email thread",
					NextActor: nextActorExternal,
					Contact: &ParkContact{
						Channel: "email",
						Sender:  "Persona",
						SentAt:  sentAt,
						URL:     "https://mail.example.com/threads/venue-42",
						Request: "Asked whether the conference room is open on Nov 25",
						Status:  "sent",
					},
				},
			}}
			return true
		})
		if err != nil {
			t.Fatal(err)
		}

		e.sessions("server", proto.Session{
			SessionID:  "s_wait",
			Title:      "Login UI",
			Status:     proto.StatusDone,
			State:      proto.StateIdle,
			StateSince: now.Add(-10 * time.Minute),
			UpdatedAt:  now.Add(-10 * time.Minute),
			Work: &proto.WorkState{
				Phase:  proto.WorkBlocked,
				On:     proto.WaitAgent,
				Wait:   "Waiting for backend auth PR to merge",
				Then:   "Rebase and run login tests",
				WakeAt: &wakeAt,
				Agent:  &proto.WorkAgent{MachineID: "server", SessionID: "s_auth", Label: "Auth API Worker"},
			},
		})
		e.must("PUT", "/api/v1/todos/"+agentWaitID+"/agents/server/s_wait", "")

		body := e.must("GET", "/api/v1/todos", "").Body.Bytes()
		var listResp struct {
			Todos []listedTodo `json:"todos"`
		}
		if err := json.Unmarshal(body, &listResp); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		byID := map[string]listedTodo{}
		for _, row := range listResp.Todos {
			byID[row.ID] = row
		}

		if byID[plain].Wait != nil {
			t.Fatalf("plain todo unexpectedly has wait: %+v", byID[plain].Wait)
		}
		pw := byID[parkedID].Wait
		if pw == nil {
			t.Fatalf("parked todo missing wait: %s", body)
		}
		if pw.Kind != assistantwire.TodoWaitKindPersonReply || pw.WaitingOn != "Alex" ||
			pw.Reason != "Waiting for Alex to confirm the room quote" ||
			pw.WakeCondition != "Alex replies in the email thread" ||
			!pw.Until.Equal(until) || !pw.Since.Equal(sentAt) ||
			pw.Contact == nil || pw.Contact.Channel != "email" {
			t.Fatalf("unexpected parked wait: %+v", pw)
		}

		aw := byID[agentWaitID].Wait
		if aw == nil {
			t.Fatalf("agent-wait todo missing wait: %s", body)
		}
		if aw.Kind != assistantwire.TodoWaitKindAgent || aw.WaitingOn != "Auth API Worker" ||
			aw.Reason != "Waiting for backend auth PR to merge" ||
			aw.WakeCondition != "Rebase and run login tests" ||
			!aw.WakeAt.Equal(wakeAt) {
			t.Fatalf("unexpected agent wait: %+v", aw)
		}

		// GET /api/v1/todos/{id} must return the exact same wait object as the list.
		detailBody := e.must("GET", "/api/v1/todos/"+parkedID, "").Body.Bytes()
		var detailResp struct {
			Todo listedTodo `json:"todo"`
		}
		if err := json.Unmarshal(detailBody, &detailResp); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		if !reflect.DeepEqual(detailResp.Todo.Wait, pw) {
			t.Fatalf("detail wait %+v != list wait %+v", detailResp.Todo.Wait, pw)
		}
	})
}

func TestActiveBlockerWaitEmitsTypedTodoWait(t *testing.T) {
	list := []listedTodo{
		{
			Todo: Todo{ID: "t1", Title: "Deploy release"},
			ActiveBlockers: []todoActiveBlockerView{
				{ID: "b1", Kind: todoBlockerKindExternalPR, TargetRef: "https://github.com/example/app/pull/77", State: "active"},
			},
		},
		{
			Todo: Todo{ID: "t2", Title: "Publish docs"},
			ActiveBlockers: []todoActiveBlockerView{
				{ID: "b2", Kind: "external_target", TargetRef: "Upstream CDN rollout", State: "active"},
				{ID: "b3", Kind: "external_target", TargetRef: "DNS propagation", State: "active"},
			},
		},
	}
	facts := make([]projection.Facts, len(list))
	for i, row := range list {
		facts[i] = projectionFacts(row, nil, nil)
		facts[i].TodosV2 = true
	}
	views, waits := projection.ViewsAndWaits(facts, time.Now())

	if waits[0] == nil || waits[0].Kind != assistantwire.TodoWaitKindPrMerge ||
		waits[0].WaitingOn != "example/app#77" || waits[0].URL != "https://github.com/example/app/pull/77" {
		t.Fatalf("unexpected PR blocker wait: %+v", waits[0])
	}
	if waits[1] == nil || waits[1].Kind != assistantwire.TodoWaitKindExternalEvent ||
		waits[1].WaitingOn != "Upstream CDN rollout" {
		t.Fatalf("unexpected external blocker wait: %+v", waits[1])
	}
	if line := views[1].Line; line == nil || line.Key != assistantwire.TodoLineKeyParked ||
		line.Args[assistantwire.TodoLineArgReason] != "Upstream CDN rollout" || line.Args["remaining"] != "1" {
		t.Fatalf("external blocker did not send a target and separate count: %+v", line)
	}
}
