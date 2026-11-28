package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func topologyPostgres(t *testing.T) (*todoEnv, *repositorystore.Store) {
	t.Helper()
	st := repositorystore.New(todoTestDB(t))
	e := newTodoEnv(t, st)
	setBlockersTodosV2Flag(t, e.s, true, false)
	return e, st
}

func rejectTopologyWrite(t *testing.T, st *repositorystore.Store, table, name, check string) func() {
	t.Helper()
	exec := func(sql string) {
		if err := st.Transaction(context.Background(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql)
			return err
		}); err != nil {
			t.Error(err)
		}
	}
	exec("ALTER TABLE " + table + " ADD CONSTRAINT " + name + " CHECK (" + check + ")")
	drop := func() { exec("ALTER TABLE " + table + " DROP CONSTRAINT IF EXISTS " + name) }
	t.Cleanup(drop)
	return drop
}

func requireTopologyConstraint(t *testing.T, err error, name string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.ConstraintName != name {
		t.Fatalf("expected constraint %s to refuse the transaction, got %v", name, err)
	}
}

func TestTodoTopologyPostgresSplitRollbackAndRetry(t *testing.T) {
	e, st := topologyPostgres(t)
	ctx := t.Context()
	source := e.create("Release preparation")
	const pr = "https://github.com/example/sample/pull/10"
	if _, _, err := st.ImportTodoRepositoryLinks(ctx, source.ID, []string{pr}, "explicit"); err != nil {
		t.Fatal(err)
	}
	blocker, err := e.s.AddTodoBlocker(ctx, source.ID, todoBlockerKindExternalPR, pr, "{}")
	if err != nil || blocker == nil {
		t.Fatalf("create blocker: row=%+v err=%v", blocker, err)
	}
	in := todoSplitRequest{Children: []todoSplitChildInput{
		{Title: "Prepare changes", CopyRepoLinks: true},
		{Title: "Verify changes", AfterIndices: []int{0}, MoveBlockerIDs: []string{blocker.ID}},
	}}

	// The final blocker cancellation fails after creating the children and writing links and blockers.
	allowWrite := rejectTopologyWrite(t, st, "todo_blockers", "split_test_active", "state <> 'cancelled'")
	_, err = e.s.splitTodo(ctx, source.ID, in)
	requireTopologyConstraint(t, err, "split_test_active")
	if rows, err := st.ListTodos(ctx); err != nil || len(rows) != 1 || rows[0].Notes != source.Notes {
		t.Fatalf("failed split wrote to PostgreSQL: rows=%+v err=%v", rows, err)
	}
	e.s.todos.mu.RLock()
	liveCount := len(e.s.todos.todos)
	e.s.todos.mu.RUnlock()
	if live, ok := e.s.todoNow(source.ID); !ok || liveCount != 1 || live.Notes != source.Notes {
		t.Fatalf("failed split published to memory: count=%d source=%+v", liveCount, live)
	}
	if rows, err := st.ListActiveTodoBlockers(ctx); err != nil || len(rows) != 1 || rows[0].ID != blocker.ID {
		t.Fatalf("failed split changed blockers: rows=%+v err=%v", rows, err)
	}

	allowWrite()
	first, err := e.s.splitTodo(ctx, source.ID, in)
	if err != nil || len(first.Children) != 2 || first.MovedBlockers != 1 {
		t.Fatalf("split retry: %+v, %v", first, err)
	}
	reloaded := newServiceOn(t, e.f, st)
	reloaded.id = e.s.id
	retry, err := reloaded.splitTodo(ctx, source.ID, in)
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatalf("split after restart created a different result: first=%+v retry=%+v err=%v", first, retry, err)
	}
	rows, err := st.ListTodos(ctx)
	if err != nil || len(rows) != 3 {
		t.Fatalf("unexpected child count after retry: rows=%+v err=%v", rows, err)
	}
	parent, err := st.GetTodo(ctx, source.ID)
	if err != nil || strings.Count(parent.Notes, "[split]") != 1 {
		t.Fatalf("split audit was not applied once: parent=%+v err=%v", parent, err)
	}
	links, err := st.TodoRepositories(ctx, first.Children[0].ID)
	if err != nil || len(links.Repositories) != 1 || len(links.Repositories[0].Links) != 1 ||
		links.Repositories[0].Links[0].Role != repository.LinkRoleReference {
		t.Fatalf("split did not copy a reference link: %+v, %v", links, err)
	}
	active, err := st.ListActiveTodoBlockers(ctx)
	if err != nil || len(active) != 2 {
		t.Fatalf("split did not commit both child blockers: %+v, %v", active, err)
	}
}

func TestTodoTopologyPostgresMergeRollbackAndRetry(t *testing.T) {
	e, st := topologyPostgres(t)
	ctx := t.Context()
	source := e.create("Original plan")
	target := e.create("Revised plan")
	child := e.create("First step")
	e.must("PATCH", "/api/v1/todos/"+child.ID, fmt.Sprintf(`{"part_of":%q}`, source.ID))
	const pr = "https://github.com/example/sample/pull/20"
	if _, _, err := st.ImportTodoRepositoryLinks(ctx, source.ID, []string{pr}, "explicit"); err != nil {
		t.Fatal(err)
	}
	blocker, err := e.s.AddTodoBlocker(ctx, source.ID, todoBlockerKindExternalPR, pr, "{}")
	if err != nil || blocker == nil {
		t.Fatalf("create blocker: row=%+v err=%v", blocker, err)
	}
	if inserted, err := st.InsertTodoDecision(ctx, db.TodoDecisionRow{
		ID: "dec_release", TodoID: source.ID, Question: "When to release?", Choice: "Next week",
		Source: db.DecisionAgent, CreatedAt: time.Now().UTC(),
	}); err != nil || !inserted {
		t.Fatalf("create decision: inserted=%t err=%v", inserted, err)
	}

	check := func(wantParent string, merged bool) {
		t.Helper()
		var durableNote string
		for _, expected := range []struct {
			todo   Todo
			parent string
		}{{source, ""}, {target, ""}, {child, wantParent}} {
			wantClosed := merged && expected.todo.ID == source.ID
			durable, err := st.GetTodo(ctx, expected.todo.ID)
			if err != nil || (durable.DoneAt != nil) != wantClosed || durable.PartOf != expected.parent {
				t.Fatalf("unexpected stored row: row=%+v err=%v", durable, err)
			}
			live, ok := e.s.todoNow(expected.todo.ID)
			if !ok || (live.DoneAt != nil) != wantClosed || live.PartOf != expected.parent || live.Notes != durable.Notes {
				t.Fatalf("stored and live topology differ for %s: live=%+v durable=%+v", expected.todo.ID, live, durable)
			}
			if expected.todo.ID == target.ID {
				durableNote = durable.Notes
			}
		}
		wantCopies := 0
		wantOwner := source.ID
		if merged {
			wantCopies, wantOwner = 1, target.ID
		}
		if strings.Count(durableNote, "[merged from ") != wantCopies {
			t.Fatalf("unexpected provenance notes: %q", durableNote)
		}
		if rows, err := st.ListTodoDecisions(ctx, target.ID, false); err != nil || len(rows) != wantCopies {
			t.Fatalf("unexpected target decisions: %+v, %v", rows, err)
		}
		if links, err := st.TodoRepositories(ctx, target.ID); err != nil || len(links.Repositories) != wantCopies {
			t.Fatalf("unexpected target links: %+v, %v", links, err)
		}
		active, err := st.ListActiveTodoBlockers(ctx)
		if err != nil || len(active) != 1 || active[0].TodoID != wantOwner {
			t.Fatalf("unexpected active blockers: %+v, %v", active, err)
		}
	}

	// A concurrent parent edit invalidates the pre-merge graph before the first write.
	in := todoMergeRequest{SourceID: source.ID}
	if _, err := st.EditTodo(ctx, target.ID, db.TodoEdit{PartOf: &source.ID}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err = e.s.mergeTodos(ctx, target.ID, in)
	var conflict topologyConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("merge used a stale graph, err=%v", err)
	}
	root := ""
	if _, err := st.EditTodo(ctx, target.ID, db.TodoEdit{PartOf: &root}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check(source.ID, false)

	// Refuse the source close after all target writes. PostgreSQL must roll back the whole graph.
	allowWrite := rejectTopologyWrite(t, st, "todos", "merge_test_open", "done_by <> 'cancelled'")
	_, err = e.s.mergeTodos(ctx, target.ID, in)
	requireTopologyConstraint(t, err, "merge_test_open")
	check(source.ID, false)

	allowWrite()
	first, err := e.s.mergeTodos(ctx, target.ID, in)
	if err != nil || !slices.Equal(first.ReparentedChildren, []string{child.ID}) || first.TransferredLinks != 1 || first.TransferredBlockers != 1 {
		t.Fatalf("merge retry failed: %+v, %v", first, err)
	}
	reloaded := newServiceOn(t, e.f, st)
	reloaded.id = e.s.id
	if _, err := reloaded.mergeTodos(ctx, target.ID, in); err != nil {
		t.Fatalf("merge was not safe to replay after restart: %v", err)
	}
	check(target.ID, true)
}

func TestTodoTopologyPostgresReorganizeRollback(t *testing.T) {
	e, st := topologyPostgres(t)
	ctx := t.Context()
	const leader = "lead0001"
	setSettings(t, e.s, func(v *Settings) { v.Leaders = []string{leader} })
	e.sessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: leader, MachineID: "server", Assistant: true})
	first, err := e.s.createTodo(ctx, "Ready to move", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.superviseTodo(ctx, first.ID, "conv1", nil, 0); err != nil {
		t.Fatal(err)
	}
	second := e.create("Refuse this move")
	parent := e.create("Destination")
	allowWrite := rejectTopologyWrite(t, st, "todos", "reorganize_test_direction", "title <> 'Refuse this move' OR direction_id = ''")
	dir := leader
	in := todoReorganizeRequest{IDs: []string{first.ID, second.ID}, PartOf: &parent.ID, DirectionID: &dir, TransferSupervision: true}
	checkLeader := func(want string) {
		t.Helper()
		rows, err := st.ListTodoSupervision(ctx)
		if err != nil || len(rows) != 1 || rows[0].TodoID != first.ID || rows[0].Supervisor != want {
			t.Fatalf("stored supervision: %+v, %v", rows, err)
		}
		cur, ok := e.s.todoNow(first.ID)
		if !ok || cur.Supervision == nil || cur.Supervision.Supervisor != want {
			t.Fatalf("live supervision: %+v", cur.Supervision)
		}
	}
	_, err = e.s.reorganizeTodos(ctx, in)
	requireTopologyConstraint(t, err, "reorganize_test_direction")
	checkLeader("conv1")
	for _, id := range in.IDs {
		live, ok := e.s.todoNow(id)
		if !ok || live.PartOf != "" || live.DirectionID != "" {
			t.Fatalf("failed reorganize published to memory: %+v", live)
		}
		row, err := st.GetTodo(ctx, id)
		if err != nil || row.PartOf != "" || row.DirectionID != "" {
			t.Fatalf("failed reorganize wrote to Postgres: row=%+v err=%v", row, err)
		}
	}
	allowWrite()
	w := reorganizeRequest(e.s, "full", fmt.Sprintf(`{"ids":[%q,%q],"part_of":%q,"direction_id":%q,"transfer_supervision":true}`, first.ID, second.ID, parent.ID, dir))
	if w.Code != http.StatusOK {
		t.Fatalf("reorganize retry: %d %s", w.Code, w.Body.String())
	}
	for _, id := range in.IDs {
		row, err := st.GetTodo(ctx, id)
		if err != nil || row.PartOf != parent.ID || row.DirectionID != dir {
			t.Fatalf("reorganize did not persist: row=%+v err=%v", row, err)
		}
	}
	checkLeader(leader)
}
