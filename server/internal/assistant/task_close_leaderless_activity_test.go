package assistant

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLeaderlessCancellationLateOwnerActivityCommitsNothing(t *testing.T) {
	for _, boundary := range []string{"ledger", "closure"} {
		t.Run(boundary, func(t *testing.T) {
			ctx := context.Background()
			st := todoTestDB(t)
			s, f := newQuietServiceOn(t, st)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			leader := startTaskLeader(t, s, f)
			worker := planWorker(t, s, f, nil)
			setSettings(t, s, func(v *Settings) { v.Main = "" })
			if _, _, err := s.updateSupervision(ctx, leader.ID, func(v *Supervision) bool {
				v.Supervisor = ""
				return true
			}); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.autoDoneTodo(ctx, leader.ID, "Finished", time.Now().Add(-time.Minute)); err != nil || !ok {
				t.Fatalf("closing the leader: %v %v", ok, err)
			}
			before := todoNow(s, worker.ID)
			beforeRows, err := st.ListTodoSupervision(ctx)
			if err != nil {
				t.Fatal(err)
			}
			lock, method := &s.supervise.writeMu, "updateSupervisionWithClose"
			if boundary == "closure" {
				lock, method = &s.todos.writing, "updateSupervisionWithClose"
			}
			lock.Lock()
			unlock := sync.OnceFunc(lock.Unlock)
			done := make(chan struct{})
			var cur Todo
			var cancelled bool
			t.Cleanup(func() {
				unlock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("rehome did not finish after releasing its write boundary")
				}
			})
			go func() {
				cur, cancelled = s.rehomeWorker(ctx, worker)
				close(done)
			}()
			waitFor(t, "leaderless cancellation at its "+boundary+" boundary", func() bool {
				for _, stack := range goroutineStacks() {
					if strings.Contains(stack, "[sync.Mutex.Lock]") &&
						strings.Contains(stack, "(*Service)."+method) &&
						strings.Contains(stack, "(*Service).rehomeWorker") {
						return true
					}
				}
				return false
			})
			agent := worker.Agents[0]
			setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
			unlock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("leaderless cancellation did not finish")
			}
			after := todoNow(s, worker.ID)
			if cancelled || cur.DoneAt != nil || after.DoneAt != nil ||
				!reflect.DeepEqual(after.Supervision, before.Supervision) {
				t.Fatalf("a refused cancellation changed its ledger or reported success: cancelled=%v before=%+v after=%+v",
					cancelled, before.Supervision, after.Supervision)
			}
			afterRows, err := st.ListTodoSupervision(ctx)
			if err != nil || !reflect.DeepEqual(afterRows, beforeRows) {
				t.Fatalf("a refused cancellation persisted a ledger change: %+v %v", afterRows, err)
			}
			setTaskViewer(t, s, agent.MachineID, agent.SessionID, false)
			old := time.Now().Add(-time.Hour)
			s.taskActivity.mu.Lock()
			s.taskActivity.times[runtimeListID(agent.MachineID, agent.SessionID)] = old
			s.taskActivity.mu.Unlock()
			s.fleet.mu.Lock()
			s.fleet.seen[[2]string{agent.MachineID, agent.SessionID}] = old
			s.fleet.mu.Unlock()
			if cur, cancelled := s.rehomeWorker(ctx, after); !cancelled || cur.DoneAt == nil || !cur.Cancelled() {
				t.Fatalf("the cancellation could not retry after inactivity: %+v %v", cur, cancelled)
			}
		})
	}
}
