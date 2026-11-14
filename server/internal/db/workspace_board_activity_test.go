package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

func openBoardActivity(t *testing.T) (*DB, string) {
	t.Helper()
	url := migratedDatabase(t, AllSets)
	d, err := Open(context.Background(), url, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d, url
}
func saveActivityCard(t *testing.T, d *DB, before workspaceboard.State, id, title string) workspaceboard.State {
	t.Helper()
	next := before
	next.Cards = slices.Clone(before.Cards)
	found := false
	for i := range next.Cards {
		if next.Cards[i].ID == id {
			next.Cards[i].Title, found = title, true
		}
	}
	if !found {
		next.Cards = append(next.Cards, workspaceboard.Card{ID: id, Title: title, ColumnID: "todo", MachineID: "computer", WorkspaceID: id, Cwd: "/work/" + id, Source: "directory"})
	}
	saved, err := d.SaveWorkspaceBoard(context.Background(), before.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestWorkspaceBoardActivityAtomicRollback(t *testing.T) {
	d, _ := openBoardActivity(t)
	ctx := context.Background()
	initial, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `CREATE FUNCTION reject_board_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'journal unavailable'; END $$;
CREATE TRIGGER reject_board_activity BEFORE INSERT ON workspace_board_activity FOR EACH ROW EXECUTE FUNCTION reject_board_activity()`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SaveWorkspaceBoard(ctx, 0, initial); err == nil {
		t.Fatal("journal failure must abort the board save")
	}
	after, err := d.WorkspaceBoard(ctx)
	if err != nil || !reflect.DeepEqual(initial, after) {
		t.Fatalf("document committed without journal: %+v %v", after, err)
	}
	page, err := d.WorkspaceBoardActivity(ctx, 0, 20, "")
	if err != nil || len(page.Entries) != 0 || page.LatestRevision != "0" || page.OldestRevision != "" {
		t.Fatalf("history after rollback: %+v %v", page, err)
	}
	if _, err := d.pool.Exec(ctx, `DROP TRIGGER reject_board_activity ON workspace_board_activity`); err != nil {
		t.Fatal(err)
	}
	// Even a no-op committed revision has exactly one empty event.
	saved, err := d.SaveWorkspaceBoard(ctx, 0, initial)
	if err != nil || saved.Revision != 1 {
		t.Fatalf("retry: %+v %v", saved, err)
	}
	detail, err := d.WorkspaceBoardActivityDetail(ctx, 1, 0, 30)
	if err != nil || detail.Total != 0 || len(detail.Changes) != 0 || detail.At.IsZero() {
		t.Fatalf("empty revision: %+v %v", detail, err)
	}
	if _, err := d.SaveWorkspaceBoard(ctx, 0, initial); !errors.Is(err, ErrWorkspaceBoardConflict) {
		t.Fatalf("stale: %v", err)
	}
	page, _ = d.WorkspaceBoardActivity(ctx, 0, 20, "")
	if len(page.Entries) != 1 {
		t.Fatalf("stale save created a duplicate: %+v", page)
	}
}

func TestWorkspaceBoardActivityConcurrentCAS(t *testing.T) {
	d, _ := openBoardActivity(t)
	ctx := context.Background()
	initial, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	const writers = 16
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() { <-start; _, err := d.SaveWorkspaceBoard(ctx, 0, initial); results <- err })
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrWorkspaceBoardConflict) {
			t.Fatal(err)
		}
	}
	page, err := d.WorkspaceBoardActivity(ctx, 0, 20, "")
	if err != nil || successes != 1 || len(page.Entries) != 1 || page.LatestRevision != "1" {
		t.Fatalf("CAS journal: successes=%d page=%+v error=%v", successes, page, err)
	}
}

func TestWorkspaceBoardActivityPagingFilterDetailAndReopen(t *testing.T) {
	d, url := openBoardActivity(t)
	ctx := context.Background()
	state, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state = saveActivityCard(t, d, state, "a", "First name")
	state = saveActivityCard(t, d, state, "b", "Second workspace")
	state = saveActivityCard(t, d, state, "a", "Renamed")
	page, err := d.WorkspaceBoardActivity(ctx, 0, 2, "")
	if err != nil || page.NextCursor != "2" || page.LatestRevision != "3" || page.OldestRevision != "1" || len(page.Entries) != 2 || page.Entries[0].Revision != "3" || page.RetentionLimit != 500 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	state = saveActivityCard(t, d, state, "a", "Newer than the page")
	next, err := d.WorkspaceBoardActivity(ctx, 2, 2, "")
	if err != nil || next.NextCursor != "" || len(next.Entries) != 1 || next.Entries[0].Revision != "1" {
		t.Fatalf("new commits changed cursor coverage: %+v %v", next, err)
	}
	filtered, err := d.WorkspaceBoardActivity(ctx, 0, 20, "b")
	if err != nil || len(filtered.Entries) != 1 || filtered.Entries[0].Revision != "2" {
		t.Fatalf("card filter: %+v %v", filtered, err)
	}
	detail, err := d.WorkspaceBoardActivityDetail(ctx, 3, 0, 1)
	if err != nil || detail.Total != 1 || detail.Changes[0].Before.Title != "First name" || detail.Changes[0].After.Title != "Renamed" {
		t.Fatalf("historical rename: %+v %v", detail, err)
	}
	state.Cards[0].Pinned, state.Cards[1].Archived = true, true
	state, err = d.SaveWorkspaceBoard(ctx, state.Revision, state)
	if err != nil {
		t.Fatal(err)
	}
	detail, err = d.WorkspaceBoardActivityDetail(ctx, state.Revision, 0, 1)
	if err != nil || detail.Total != 2 || len(detail.Changes) != 1 || detail.NextOffset == nil || *detail.NextOffset != 1 {
		t.Fatalf("bounded details: %+v %v", detail, err)
	}
	tail, err := d.WorkspaceBoardActivityDetail(ctx, state.Revision, 1, 1)
	if err != nil || tail.NextOffset != nil || tail.Changes[0].ID == detail.Changes[0].ID {
		t.Fatalf("detail tail: %+v %v", tail, err)
	}
	if _, err := d.WorkspaceBoardActivityDetail(ctx, 999, 0, 1); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("missing detail: %v", err)
	}
	reopened, err := Open(ctx, url, AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, err := reopened.WorkspaceBoardActivityDetail(ctx, 3, 0, 30)
	if err != nil || persisted.Changes[0].Before.Title != "First name" {
		t.Fatalf("restart lost metadata: %+v %v", persisted, err)
	}
}

func TestWorkspaceBoardActivityRetentionBounds(t *testing.T) {
	d, _ := openBoardActivity(t)
	ctx := context.Background()
	state, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Seed old entries to exercise the boundary without hundreds of unrelated transactions.
	if _, err := d.pool.Exec(ctx, `INSERT INTO workspace_board_activity(revision,change_count,card_count,kinds,card_ids,changes,encoded_bytes)
 SELECT n,0,0,'{}','{}','[]',2 FROM generate_series(1,500) n;
 UPDATE workspace_board SET revision=500 WHERE id=true`); err != nil {
		t.Fatal(err)
	}
	state.Revision = 500
	state, err = d.SaveWorkspaceBoard(ctx, 500, state)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.WorkspaceBoardActivity(ctx, 0, 20, "")
	if err != nil || page.OldestRevision != "2" || page.LatestRevision != "501" {
		t.Fatalf("row retention: %+v %v", page, err)
	}
	if _, err := d.WorkspaceBoardActivityDetail(ctx, 1, 0, 20); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old detail survived: %v", err)
	}
	// Five equal old records use a 20 MiB encoded budget; the newest four fit.
	if _, err := d.pool.Exec(ctx, `DELETE FROM workspace_board_activity;
 INSERT INTO workspace_board_activity(revision,change_count,card_count,kinds,card_ids,changes,encoded_bytes)
 SELECT n,0,0,'{}','{}','[]',4194304 FROM generate_series(497,501) n`); err != nil {
		t.Fatal(err)
	}
	state, err = d.SaveWorkspaceBoard(ctx, state.Revision, state)
	if err != nil {
		t.Fatal(err)
	}
	page, err = d.WorkspaceBoardActivity(ctx, 0, 20, "")
	if err != nil || page.OldestRevision != "499" || len(page.Entries) != 4 {
		t.Fatalf("byte retention: %+v %v", page, err)
	}
	var total int64
	if err := d.pool.QueryRow(ctx, `SELECT sum(encoded_bytes) FROM workspace_board_activity`).Scan(&total); err != nil || total > workspaceboard.ActivityRetentionBytes {
		t.Fatalf("bytes=%d error=%v", total, err)
	}
}

func TestWorkspaceBoardActivityReorderFiltersAndRevisionPrecision(t *testing.T) {
	d, _ := openBoardActivity(t)
	ctx := context.Background()
	state, err := d.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c", "d"} {
		state = saveActivityCard(t, d, state, id, id)
	}
	const high int64 = 9007199254740993
	if _, err := d.pool.Exec(ctx, `UPDATE workspace_board SET revision=$1 WHERE id=true`, high); err != nil {
		t.Fatal(err)
	}
	state.Revision = high
	state.Cards[1], state.Cards[2] = state.Cards[2], state.Cards[1]
	state, err = d.SaveWorkspaceBoard(ctx, high, state)
	if err != nil {
		t.Fatal(err)
	}
	page, err := d.WorkspaceBoardActivity(ctx, 0, 20, "b")
	if err != nil || page.Entries[0].Revision != strconv.FormatInt(high+1, 10) || page.Entries[0].CardCount != 2 {
		t.Fatalf("precision or reorder filter: %+v %v", page, err)
	}
	detail, err := d.WorkspaceBoardActivityDetail(ctx, high+1, 0, 30)
	if err != nil || len(detail.Changes) != 1 || detail.Changes[0].Kind != "cards_reordered" || !slices.Equal(detail.Changes[0].CardIDs, []string{"c", "b"}) {
		t.Fatalf("reorder detail: %+v %v", detail, err)
	}
	raw, _ := json.Marshal(detail)
	if strings.Contains(string(raw), "/work/") || strings.Contains(string(raw), "machine_id") {
		t.Fatalf("binding metadata in history: %s", raw)
	}
	for _, query := range []struct {
		before int64
		limit  int
		card   string
	}{{-1, 20, ""}, {0, 0, ""}, {0, 51, ""}, {0, 20, "bad\n"}} {
		if _, err := d.WorkspaceBoardActivity(ctx, query.before, query.limit, query.card); !errors.Is(err, workspaceboard.ErrInvalid) {
			t.Fatalf("accepted invalid query %+v: %v", query, err)
		}
	}
	for _, args := range [][3]int64{{0, 0, 20}, {1, -1, 20}, {1, 0, 101}} {
		if _, err := d.WorkspaceBoardActivityDetail(ctx, args[0], int(args[1]), int(args[2])); !errors.Is(err, workspaceboard.ErrInvalid) {
			t.Fatal(fmt.Sprint(args, err))
		}
	}
}
