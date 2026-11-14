package db

import (
	"context"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/workspaceboard"
)

// Bulk edits and history ship independently. Their combined contract is one
// committed revision and one journal entry for a batch, including its Undo.
func TestWorkspaceBoardBulkActivityAndUndo(t *testing.T) {
	database, _ := openBoardActivity(t)
	ctx := context.Background()
	initial, err := database.WorkspaceBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := saveActivityCard(t, database, initial, "a", "First workspace")
	before = saveActivityCard(t, database, before, "b", "Second workspace")
	yes, no := true, false
	next, err := workspaceboard.Apply(before, workspaceboard.Mutation{Action: "batch_cards", Changes: []workspaceboard.CardChange{
		{ID: "a", ColumnID: "done", Pinned: &yes}, {ID: "b", ColumnID: "done", Archived: &yes},
	}})
	if err != nil {
		t.Fatal(err)
	}
	saved, err := database.SaveWorkspaceBoard(ctx, before.Revision, next)
	if err != nil {
		t.Fatal(err)
	}
	history, err := database.WorkspaceBoardActivity(ctx, 0, 20, "")
	if err != nil || len(history.Entries) != 3 || history.Entries[0].Revision != "3" || history.Entries[0].ChangeCount != 2 || history.Entries[0].CardCount != 2 {
		t.Fatalf("batch must have one grouped history entry: %+v %v", history, err)
	}
	detail, err := database.WorkspaceBoardActivityDetail(ctx, 3, 0, 30)
	if err != nil || detail.Total != 2 || detail.Changes[0].Before.ColumnID != "todo" || detail.Changes[0].After.ColumnID != "done" || !(*detail.Changes[1].After.Archived) {
		t.Fatalf("batch history lost metadata: %+v %v", detail, err)
	}
	undone, err := workspaceboard.Apply(saved, workspaceboard.Mutation{Action: "batch_cards", Changes: []workspaceboard.CardChange{
		{ID: "b", ColumnID: "todo", Archived: &no}, {ID: "a", ColumnID: "todo", BeforeID: "b", Pinned: &no},
	}})
	if err != nil {
		t.Fatal(err)
	}
	undone, err = database.SaveWorkspaceBoard(ctx, saved.Revision, undone)
	if err != nil {
		t.Fatal(err)
	}
	if undone.Revision != 4 || !reflect.DeepEqual(undone.Cards, before.Cards) {
		t.Fatalf("undo lost original board: %+v", undone)
	}
	filtered, err := database.WorkspaceBoardActivity(ctx, 0, 20, "a")
	if err != nil || len(filtered.Entries) != 3 || filtered.Entries[0].Revision != "4" || filtered.Entries[1].Revision != "3" {
		t.Fatalf("card history must include batch and undo exactly once: %+v %v", filtered, err)
	}
	undoDetail, err := database.WorkspaceBoardActivityDetail(ctx, 4, 0, 30)
	if err != nil || undoDetail.Total != 2 || undoDetail.Changes[0].Before.ColumnID != "done" || undoDetail.Changes[0].After.ColumnID != "todo" {
		t.Fatalf("undo must record its own changes: %+v %v", undoDetail, err)
	}
}
