package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestTaskReviewDoesNotWaitForOwnerActivityPersistence(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		t.Run(verdict, func(t *testing.T) {
			ctx := context.Background()
			st := &reviewActivityStore{DB: todoTestDB(t)}
			s, _, todo := newSupervisedServiceOn(t, st)
			before := todoNow(s, todo.ID)
			beforeRows, err := st.ListTodoSupervision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			feedBefore := feedKinds(s, todo.Origin)
			atGuard, releaseGuard := make(chan struct{}), make(chan struct{})
			atSave, releaseSave := make(chan struct{}), make(chan struct{})
			allowGuard := sync.OnceFunc(func() { close(releaseGuard) })
			allowSave := sync.OnceFunc(func() { close(releaseSave) })
			st.beforeCommit = func() {
				close(atGuard)
				<-releaseGuard
			}
			st.beforeActivitySave = func() {
				close(atSave)
				<-releaseSave
			}
			raw, _ := json.Marshal(map[string]any{
				"todo_id": todo.ID, "verdict": verdict, "checks": completeReviewChecks(todo),
			})
			reviewDone, ownerDone := make(chan struct{}), make(chan struct{})
			var reviewErr, ownerErr error
			ownerStarted := false
			t.Cleanup(func() {
				allowGuard()
				allowSave()
				select {
				case <-reviewDone:
				case <-time.After(5 * time.Second):
					t.Error("review did not finish after releasing the fixture")
				}
				if ownerStarted {
					select {
					case <-ownerDone:
					case <-time.After(5 * time.Second):
						t.Error("owner event did not finish after releasing the fixture")
					}
				}
			})
			go func() {
				_, reviewErr = s.toolTaskReview(ctx, "conv1", raw)
				close(reviewDone)
			}()
			select {
			case <-atGuard:
			case <-time.After(5 * time.Second):
				t.Fatal("the review did not reach its database commit guard")
			}
			agent := todo.Agents[0]
			event, _ := json.Marshal(assistantwire.UserSpokeParams{
				SessionRef: SessionRef{MachineID: agent.MachineID, SessionID: agent.SessionID},
				Text:       "Please keep this conversation open.",
			})
			ownerStarted = true
			go func() {
				ownerErr = (&BusHub{svc: s}).event(ctx, busEvent{name: assistantwire.EventUserSpoke, params: event})
				close(ownerDone)
			}()
			select {
			case <-atSave:
			case <-time.After(5 * time.Second):
				t.Fatal("the real owner event did not reach activity persistence")
			}
			// Persistence can wait for a connection held by ReviewTodo. Its guard must only read the published activity.
			allowGuard()
			select {
			case <-reviewDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the review waited for owner activity persistence")
			}
			if reviewErr == nil || !strings.Contains(reviewErr.Error(), "cannot be closed") {
				t.Fatalf("the concurrent owner event did not refuse the review: %v", reviewErr)
			}
			cur := todoNow(s, todo.ID)
			if cur.DoneAt != nil || !reflect.DeepEqual(cur.Supervision, before.Supervision) {
				t.Fatalf("the refused review committed: %+v", cur)
			}
			afterRows, err := st.ListTodoSupervision(ctx)
			if err != nil || !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatalf("the refused review persisted ledger changes: %+v %v", afterRows, err)
			}
			if feedKinds(s, todo.Origin) != feedBefore || s.resultOfTodo(cur.ID) != nil {
				t.Fatal("the refused review published review effects")
			}
			stored, err := st.GetTodo(ctx, todo.ID)
			if err != nil || stored.DoneAt != nil {
				t.Fatalf("the refused review persisted a closure: %+v %v", stored, err)
			}
			allowSave()
			select {
			case <-ownerDone:
			case <-time.After(5 * time.Second):
				t.Fatal("the owner event did not finish")
			}
			if ownerErr != nil {
				t.Fatal(ownerErr)
			}
			var times map[string]time.Time
			if _, err := st.Setting(ctx, taskActivityKey, &times); err != nil ||
				times[runtimeListID(agent.MachineID, agent.SessionID)].IsZero() {
				t.Fatalf("the owner activity was not eventually persisted: %v", err)
			}
		})
	}
}
