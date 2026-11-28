package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/db"
)

// A second manager edits a task while a queue claim is still committing.
// Even when the edited slot is the claimed one, both changes must survive.
func TestRequestPrerequisitesPlanEditPreservesConcurrentClaim(t *testing.T) {
	ctx := t.Context()
	url := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _ := newQuietServiceOn(t, database)
	writer, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close(context.Background())

	for index, name := range []string{"same_task", "sibling"} {
		t.Run(name, func(t *testing.T) {
			tasks, err := json.Marshal([]PlanTask{{TodoID: "gated", Queue: &TaskQueue{Key: requestGateKey}}, {TodoID: "sibling"}})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err := database.CreateTaskPlan(ctx, db.TaskPlanRow{ID: name, Conversation: "conv1", Tasks: tasks, State: planStarted, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			tx, err := writer.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `UPDATE task_plans SET tasks = jsonb_set(tasks, '{0,queue,claimed}', 'true') WHERE id=$1`, name); err != nil {
				t.Fatal(err)
			}
			patched := make(chan error, 1)
			go func() {
				patched <- s.patchPlanTask(ctx, name, index, func(task *PlanTask) { task.Title = "Updated" })
			}()
			// The patch has reached the database. The old full-plan writer would now be
			// waiting to replace the row with a pre-claim snapshot.
			waitFor(t, "plan edit waiting for the uncommitted claim", func() bool {
				var blocked bool
				err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM pg_locks WHERE NOT granted AND transactionid = pg_current_xact_id()::text::xid)`).Scan(&blocked)
				if err != nil {
					t.Fatal(err)
				}
				return blocked
			})
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-patched; err != nil {
				t.Fatal(err)
			}
			result, err := s.loadPlan(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Tasks[0].Queue.Claimed || result.Tasks[index].Title != "Updated" {
				t.Fatalf("lost a concurrent change: %+v, queue %+v", result.Tasks, result.Tasks[0].Queue)
			}
		})
	}
}
