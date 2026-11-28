package assistant

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func buildTodoBoardProjection(items []listedTodo, directions map[string]string) *todoBoardProjection {
	items = slices.Clone(items)
	(&Service{}).populateTodoEffectiveDirections(items, directions)
	facts := make([]projection.Facts, len(items))
	views := make([]assistantwire.TodoSummary, len(items))
	for i, item := range items {
		facts[i] = projection.Facts{ID: item.ID, PartOf: item.PartOf, Quiet: item.Quiet}
		if item.View != nil {
			views[i] = *item.View
		}
	}
	return buildTodoBoardProjectionWithOptions(items, directions, true, projection.Counts(facts, views))
}

func requireBoardStatusGroup(t *testing.T, board *todoBoardProjection, key string) todoBoardStatusGroup {
	t.Helper()
	for _, group := range board.StatusGroups {
		if group.Key == key {
			return group
		}
	}
	t.Fatalf("board is missing %s", key)
	return todoBoardStatusGroup{}
}

func TestTodosV2BoardProjectionEmpty(t *testing.T) {
	got := buildTodoBoardProjection(nil, nil)
	if got == nil {
		t.Fatal("buildTodoBoardProjection(nil, nil) returned nil")
	}
	if len(got.StatusGroups) != len(boardSectionOrder) {
		t.Fatalf("status_groups len = %d, want %d", len(got.StatusGroups), len(boardSectionOrder))
	}
	for i, wantKey := range boardSectionOrder {
		g := got.StatusGroups[i]
		if g.Key != wantKey {
			t.Fatalf("status_groups[%d].key = %q, want %q", i, g.Key, wantKey)
		}
		if g.TotalCount != 0 || g.VisibleCount != 0 || len(g.Entries) != 0 || g.Entries == nil {
			t.Fatalf("status_groups[%d] = %+v, want empty non-nil group", i, g)
		}
	}
	if got.DirectionGroups == nil || len(got.DirectionGroups) != 0 {
		t.Fatalf("direction_groups = %#v, want empty non-nil slice", got.DirectionGroups)
	}
	if got.SidebarTree == nil || len(got.SidebarTree) != 0 {
		t.Fatalf("sidebar_tree = %#v, want empty non-nil slice", got.SidebarTree)
	}
	if got.Counts != (todoBoardCounts{}) {
		t.Fatalf("counts = %+v, want zero counts", got.Counts)
	}
}

func TestTodosV2BoardProjectionSeparatesStatusFromAttention(t *testing.T) {
	at := time.Now()
	items := []listedTodo{
		{Todo: Todo{ID: "parent"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou}},
		{Todo: Todo{ID: "step", PartOf: "parent"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou}},
		{Todo: Todo{ID: "todo_od_independent", PartOf: "parent"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou}},
		{Todo: Todo{ID: "quiet", Quiet: true}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou}},
		{Todo: Todo{ID: "deferred"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionCanWait, CanWait: true}},
		{Todo: Todo{ID: "plain", PartOf: "cancelled"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseTodo, Section: assistantwire.TodoListSectionTodo}},
		{Todo: Todo{ID: "someday"}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseSomeday, Section: assistantwire.TodoListSectionSomeday}},
		{Todo: Todo{ID: "cancelled", DoneAt: &at}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionDone, Reason: assistantwire.TodoReasonCanceled}},
		{Todo: Todo{ID: "fresh-cancellation", DoneAt: &at, CancelledAt: &at}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionJustFinished, Reason: assistantwire.TodoReasonClosedUnseen}},
		{Todo: Todo{ID: "done", DoneAt: &at}, View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionDone}},
	}
	board := buildTodoBoardProjection(items, nil)
	wantCounts := todoBoardCounts{NeedsYou: 2, CanWait: 1, Todo: 1, Someday: 1, Done: 1, Cancelled: 2, OpenTotal: 5}
	if got := board.Counts; got != wantCounts {
		t.Errorf("board.counts = %+v, want %+v", got, wantCounts)
	}
	sections := map[string]string{}
	for _, group := range board.StatusGroups {
		for _, entry := range group.Entries {
			sections[entry.TodoID] = group.Key
		}
	}
	for id, want := range map[string]string{
		"step": "needs_you", "deferred": "can_wait", "plain": "todo",
		"cancelled": "cancelled", "fresh-cancellation": "cancelled", "done": "done",
	} {
		if got := sections[id]; got != want {
			t.Errorf("section of %s = %q, want %q", id, got, want)
		}
	}
}

func TestTodosV2BoardProjectionStatusGroupsAndGoalFolding(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	doneAt := base.Add(-1 * time.Hour)
	someday := base.Add(48 * time.Hour)

	items := []listedTodo{
		// Top-level goal in progress with folded + surfaced children and a grandchild.
		{
			Todo: Todo{
				ID:        "goal_1",
				Title:     "Ship v2 board",
				CreatedAt: base.Add(-10 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWorking,
				Section: assistantwire.TodoListSectionWorking,
				Rank:    0,
			},
		},
		// Normal working child -> folded under goal_1.
		{
			Todo: Todo{
				ID:        "child_work",
				Title:     "Implement backend",
				PartOf:    "goal_1",
				CreatedAt: base.Add(-9 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWorking,
				Section: assistantwire.TodoListSectionWorking,
				Rank:    1,
			},
		},
		// Grandchild of goal_1 (via child_work) -> folded under top-level goal_1.
		{
			Todo: Todo{
				ID:        "grandchild_wait",
				Title:     "Wait for review",
				PartOf:    "child_work",
				CreatedAt: base.Add(-8 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWaiting,
				Section: assistantwire.TodoListSectionWaiting,
				Rank:    0,
			},
		},
		// Done child -> folded under goal_1.
		{
			Todo: Todo{
				ID:        "child_done",
				Title:     "Write spec",
				PartOf:    "goal_1",
				DoneAt:    &doneAt,
				CreatedAt: base.Add(-11 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseDone,
				Section: assistantwire.TodoListSectionDone,
				Rank:    0,
			},
		},
		// Blocked child needing user decision -> surfaces into needs_you AND recorded in goal_1.surfaced_child_ids.
		{
			Todo: Todo{
				ID:        "child_blocked",
				Title:     "Approve schema",
				PartOf:    "goal_1",
				CreatedAt: base.Add(-7 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			Ask:      &todoAsk{Outcome: outcomeDecide, Question: "Proceed?", AskedAt: base.Add(-5 * time.Minute)},
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseNeedsYou,
				Section: assistantwire.TodoListSectionNeedsYou,
				Rank:    1,
			},
		},
		// Standalone urgent item (rank 0 in needs_you -> must precede child_blocked with rank 1).
		{
			Todo: Todo{
				ID:        "urgent_top",
				Title:     "Fix prod outage",
				CreatedAt: base.Add(-6 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseNeedsYou,
				Section: assistantwire.TodoListSectionNeedsYou,
				Rank:    0,
			},
		},
		// Supervised pending_review (<24h, view section is working under todos_v2 -> board section pending_review).
		{
			Todo: Todo{
				ID:          "review_1",
				Title:       "Review PR",
				CreatedAt:   base.Add(-5 * time.Minute),
				Supervision: &Supervision{State: reviewPending, Supervisor: "lead1"},
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWorking,
				Section: assistantwire.TodoListSectionWorking,
				Rank:    2,
			},
		},
		// Someday item.
		{
			Todo: Todo{
				ID:           "someday_1",
				Title:        "Explore idea",
				SomedayUntil: &someday,
				CreatedAt:    base.Add(-4 * time.Minute),
			},
			ItemType: db.ItemTypePersonal,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseSomeday,
				Section: assistantwire.TodoListSectionSomeday,
				Rank:    0,
			},
		},
		// Done parent with an open child: open child must NOT be hidden under done parent.
		{
			Todo: Todo{
				ID:        "done_parent",
				Title:     "Old goal",
				DoneAt:    &doneAt,
				CreatedAt: base.Add(-20 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseDone,
				Section: assistantwire.TodoListSectionDone,
				Rank:    1,
			},
		},
		{
			Todo: Todo{
				ID:        "open_child_of_done",
				Title:     "Follow-up cleanup",
				PartOf:    "done_parent",
				CreatedAt: base.Add(-3 * time.Minute),
			},
			ItemType: db.ItemTypePersonal,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseTodo,
				Section: assistantwire.TodoListSectionTodo,
				Rank:    0,
			},
		},
		// Cycle in part_of: both items must remain top-level (never dropped).
		{
			Todo: Todo{
				ID:        "cycle_a",
				Title:     "Cycle A",
				PartOf:    "cycle_b",
				CreatedAt: base.Add(-2 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWaiting,
				Section: assistantwire.TodoListSectionWaiting,
				Rank:    1,
			},
		},
		{
			Todo: Todo{
				ID:        "cycle_b",
				Title:     "Cycle B",
				PartOf:    "cycle_a",
				CreatedAt: base.Add(-1 * time.Minute),
			},
			ItemType: db.ItemTypeTask,
			View: &assistantwire.TodoSummary{
				Phase:   assistantwire.TodoPhaseWaiting,
				Section: assistantwire.TodoListSectionWaiting,
				Rank:    2,
			},
		},
	}

	got := buildTodoBoardProjection(items, nil)

	// Reversed input must produce identical output (determinism).
	rev := slices.Clone(items)
	slices.Reverse(rev)
	gotRev := buildTodoBoardProjection(rev, nil)
	if !reflect.DeepEqual(got, gotRev) {
		t.Fatalf("board projection not deterministic on reversed input:\ngot: %+v\nrev: %+v", got, gotRev)
	}

	byKey := map[string]todoBoardStatusGroup{}
	totalAcrossGroups := 0
	for i, g := range got.StatusGroups {
		if g.Key != boardSectionOrder[i] {
			t.Fatalf("status_groups[%d].key = %q, want %q", i, g.Key, boardSectionOrder[i])
		}
		byKey[g.Key] = g
		totalAcrossGroups += g.TotalCount
	}
	if totalAcrossGroups != len(items) {
		t.Fatalf("sum(status_groups.total_count) = %d, want %d", totalAcrossGroups, len(items))
	}

	// needs_you: urgent_top (rank 0), then surfaced child_blocked (rank 1).
	ny := byKey[boardSectionNeedsYou]
	if ny.TotalCount != 2 || ny.VisibleCount != 2 {
		t.Fatalf("needs_you counts = (%d, %d), want (2, 2)", ny.TotalCount, ny.VisibleCount)
	}
	if len(ny.Entries) != 2 || ny.Entries[0].TodoID != "urgent_top" || ny.Entries[1].TodoID != "child_blocked" {
		t.Fatalf("needs_you entries = %+v, want [urgent_top, child_blocked]", ny.Entries)
	}

	// in_progress: raw total is 2 (goal_1 + child_work), visible is 1 (goal_1 with 3 folded + 1 surfaced).
	ip := byKey[boardSectionInProgress]
	if ip.TotalCount != 2 || ip.VisibleCount != 1 || len(ip.Entries) != 1 {
		t.Fatalf("in_progress = %+v, want total=2 visible=1", ip)
	}
	goalEntry := ip.Entries[0]
	if goalEntry.TodoID != "goal_1" {
		t.Fatalf("goalEntry.TodoID = %q, want goal_1", goalEntry.TodoID)
	}
	wantFolded := []string{"child_work", "grandchild_wait", "child_done"}
	if !reflect.DeepEqual(goalEntry.FoldedChildIDs, wantFolded) || goalEntry.FoldedCount != 3 {
		t.Fatalf("goal_1 folded = (%v, %d), want (%v, 3)", goalEntry.FoldedChildIDs, goalEntry.FoldedCount, wantFolded)
	}
	if !reflect.DeepEqual(goalEntry.SurfacedChildIDs, []string{"child_blocked"}) {
		t.Fatalf("goal_1 surfaced = %v, want [child_blocked]", goalEntry.SurfacedChildIDs)
	}

	// pending_review: review_1.
	pr := byKey[boardSectionPendingReview]
	if pr.TotalCount != 1 || pr.VisibleCount != 1 || pr.Entries[0].TodoID != "review_1" {
		t.Fatalf("pending_review = %+v, want [review_1]", pr)
	}

	// Waiting has one folded grandchild and two visible rows. The ordinary follow-up is a To-do.
	wt := byKey[boardSectionWaiting]
	if wt.TotalCount != 3 || wt.VisibleCount != 2 {
		t.Fatalf("waiting = %+v, want total=3 visible=2", wt)
	}
	gotWaitIDs := []string{wt.Entries[0].TodoID, wt.Entries[1].TodoID}
	wantWaitIDs := []string{"cycle_a", "cycle_b"}
	if !reflect.DeepEqual(gotWaitIDs, wantWaitIDs) {
		t.Fatalf("waiting visible IDs = %v, want %v", gotWaitIDs, wantWaitIDs)
	}
	if plain := byKey[boardSectionTodo]; plain.TotalCount != 1 || plain.VisibleCount != 1 || plain.Entries[0].TodoID != "open_child_of_done" {
		t.Fatalf("todo = %+v, want open_child_of_done", plain)
	}

	// someday: someday_1.
	sd := byKey[boardSectionSomeday]
	if sd.TotalCount != 1 || sd.VisibleCount != 1 || sd.Entries[0].TodoID != "someday_1" {
		t.Fatalf("someday = %+v, want [someday_1]", sd)
	}

	// done: raw total is 2 (child_done folded, done_parent visible).
	dn := byKey[boardSectionDone]
	if dn.TotalCount != 2 || dn.VisibleCount != 1 || dn.Entries[0].TodoID != "done_parent" {
		t.Fatalf("done = %+v, want total=2 visible=1 [done_parent]", dn)
	}

	// Summary counts carry direct steps with their parents independently of status group visibility.
	wantCounts := todoBoardCounts{
		NeedsYou:      1,
		InProgress:    1,
		PendingReview: 1,
		Todo:          1,
		Someday:       1,
		Done:          1,
		OpenTotal:     4,
	}
	if got.Counts != wantCounts {
		t.Fatalf("counts = %+v, want %+v", got.Counts, wantCounts)
	}
}

func TestTodosV2BoardProjectionDirectionGroupsAndOrdering(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	doneAt := base.Add(-1 * time.Hour)
	directions := map[string]string{
		"dir_alpha": "Alpha",
		"dir_beta":  "Beta",
		"dir_gamma": "Gamma",
		"dir_delta": "Delta",
	}

	items := []listedTodo{
		// Explicit direction_id overrides origin.
		{
			Todo: Todo{
				ID:          "t_override",
				DirectionID: "dir_beta",
				Origin:      "dir_alpha",
				CreatedAt:   base,
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou, Rank: 0},
		},
		// Falls back to origin because origin is a known direction.
		{
			Todo: Todo{
				ID:        "t_origin_fallback",
				Origin:    "dir_alpha",
				CreatedAt: base.Add(-1 * time.Minute),
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWorking, Section: assistantwire.TodoListSectionWorking, Rank: 0},
		},
		// dir_gamma has 2 waiting items (higher total than dir_delta).
		{
			Todo: Todo{
				ID:          "t_gamma_1",
				DirectionID: "dir_gamma",
				CreatedAt:   base.Add(-2 * time.Minute),
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWaiting, Section: assistantwire.TodoListSectionWaiting, Rank: 0},
		},
		{
			Todo: Todo{
				ID:          "t_gamma_2",
				DirectionID: "dir_gamma",
				DoneAt:      &doneAt,
				CreatedAt:   base.Add(-3 * time.Minute),
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionDone, Rank: 0},
		},
		// dir_delta has 1 waiting item.
		{
			Todo: Todo{
				ID:          "t_delta_1",
				DirectionID: "dir_delta",
				CreatedAt:   base.Add(-4 * time.Minute),
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWaiting, Section: assistantwire.TodoListSectionWaiting, Rank: 1},
		},
		// Main conversation origin (not in directions map) -> ungrouped (""), always last even with needs_you!
		{
			Todo: Todo{
				ID:        "t_ungrouped",
				Origin:    "conv_main",
				CreatedAt: base.Add(-5 * time.Minute),
			},
			View: &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou, Rank: 1},
		},
	}

	got := buildTodoBoardProjection(items, directions)

	// Expected group order:
	// 1. dir_beta (has needs_you)
	// 2. dir_alpha (has in_progress)
	// 3. dir_gamma (total=2)
	// 4. dir_delta (total=1)
	// 5. "" (ungrouped, always last)
	wantOrder := []string{"dir_beta", "dir_alpha", "dir_gamma", "dir_delta", ""}
	if len(got.DirectionGroups) != len(wantOrder) {
		t.Fatalf("direction_groups len = %d, want %d: %+v", len(got.DirectionGroups), len(wantOrder), got.DirectionGroups)
	}
	for i, wantDir := range wantOrder {
		g := got.DirectionGroups[i]
		if g.DirectionID != wantDir {
			t.Fatalf("direction_groups[%d].direction_id = %q, want %q", i, g.DirectionID, wantDir)
		}
	}
	if got.DirectionGroups[0].Label != "Beta" || got.DirectionGroups[0].Counts.NeedsYou != 1 || got.DirectionGroups[0].Counts.Total != 1 {
		t.Fatalf("dir_beta group = %+v", got.DirectionGroups[0])
	}
	if got.DirectionGroups[1].Label != "Alpha" || got.DirectionGroups[1].Counts.InProgress != 1 {
		t.Fatalf("dir_alpha group = %+v", got.DirectionGroups[1])
	}
	if got.DirectionGroups[2].Label != "Gamma" || got.DirectionGroups[2].Counts.Waiting != 1 || got.DirectionGroups[2].Counts.Done != 1 || got.DirectionGroups[2].Counts.Total != 2 {
		t.Fatalf("dir_gamma group = %+v", got.DirectionGroups[2])
	}
	if !reflect.DeepEqual(got.DirectionGroups[2].TodoIDs, []string{"t_gamma_1", "t_gamma_2"}) {
		t.Fatalf("dir_gamma todo_ids = %v, want [t_gamma_1, t_gamma_2]", got.DirectionGroups[2].TodoIDs)
	}
	if got.DirectionGroups[4].Label != "" || got.DirectionGroups[4].Counts.NeedsYou != 1 || !reflect.DeepEqual(got.DirectionGroups[4].TodoIDs, []string{"t_ungrouped"}) {
		t.Fatalf("ungrouped = %+v", got.DirectionGroups[4])
	}
}

func TestTodosV2BoardProjectionSidebarTree(t *testing.T) {
	base := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	doneAt := base.Add(-1 * time.Hour)
	someday := base.Add(24 * time.Hour)

	items := []listedTodo{
		{
			Todo:     Todo{ID: "wait_1", DirectionID: "dir_1", CreatedAt: base},
			ItemType: db.ItemTypePersonal,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWaiting, Section: assistantwire.TodoListSectionWaiting, Rank: 0},
		},
		{
			Todo:     Todo{ID: "review_1", DirectionID: "dir_1", CreatedAt: base, Supervision: &Supervision{State: reviewPending}},
			ItemType: db.ItemTypeTask,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWorking, Section: assistantwire.TodoListSectionWorking, Rank: 1},
		},
		{
			Todo:     Todo{ID: "goal_ip", DirectionID: "dir_1", CreatedAt: base},
			ItemType: db.ItemTypeTask,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWorking, Section: assistantwire.TodoListSectionWorking, Rank: 0},
		},
		{
			Todo:     Todo{ID: "goal_child_ip", PartOf: "goal_ip", CreatedAt: base},
			ItemType: db.ItemTypeTask,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseWorking, Section: assistantwire.TodoListSectionWorking, Rank: 2},
		},
		{
			Todo:     Todo{ID: "goal_child_ny", PartOf: "goal_ip", CreatedAt: base},
			ItemType: db.ItemTypeTask,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseNeedsYou, Section: assistantwire.TodoListSectionNeedsYou, Rank: 0},
		},
		{
			Todo:     Todo{ID: "someday_1", SomedayUntil: &someday, CreatedAt: base},
			ItemType: db.ItemTypePersonal,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseSomeday, Section: assistantwire.TodoListSectionSomeday, Rank: 0},
		},
		{
			Todo:     Todo{ID: "done_1", DoneAt: &doneAt, CreatedAt: base},
			ItemType: db.ItemTypePersonal,
			View:     &assistantwire.TodoSummary{Phase: assistantwire.TodoPhaseDone, Section: assistantwire.TodoListSectionDone, Rank: 0},
		},
	}

	got := buildTodoBoardProjection(items, map[string]string{"dir_1": "Core"})

	// Sidebar tree includes only open visible nodes (needs_you > in_progress > pending_review > waiting).
	// someday_1 and done_1 are excluded; goal_child_ip is folded under goal_ip; goal_child_ny is surfaced.
	wantIDs := []string{"goal_child_ny", "goal_ip", "review_1", "wait_1"}
	gotIDs := make([]string, len(got.SidebarTree))
	for i, n := range got.SidebarTree {
		gotIDs[i] = n.TodoID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("sidebar_tree order = %v, want %v", gotIDs, wantIDs)
	}

	goalNode := got.SidebarTree[1]
	if goalNode.Section != boardSectionInProgress ||
		goalNode.ItemType != db.ItemTypeTask ||
		goalNode.DirectionID != "dir_1" ||
		goalNode.FoldedCount != 1 ||
		!reflect.DeepEqual(goalNode.ChildIDs, []string{"goal_child_ip"}) ||
		!reflect.DeepEqual(goalNode.SurfacedChildIDs, []string{"goal_child_ny"}) ||
		goalNode.NeedsYouCount != 1 ||
		goalNode.ActiveCount != 2 {
		t.Fatalf("goal_ip sidebar node = %+v", goalNode)
	}
}

func TestTodosV2BoardProjectionHTTPGatingAndRollback(t *testing.T) {
	s, _, _ := briefService(t, "Board projection HTTP test")

	// Empty list: todos_v2=false omits board; todos_v2=true emits empty board.
	emptyOff := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if emptyOff.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos (empty, off): %d %s", emptyOff.Code, emptyOff.Body.String())
	}
	var emptyOffMap map[string]json.RawMessage
	if err := json.Unmarshal(emptyOff.Body.Bytes(), &emptyOffMap); err != nil {
		t.Fatalf("decode empty off: %v", err)
	}
	if _, has := emptyOffMap["board"]; has {
		t.Fatalf("todos_v2=false must omit board on empty list, got: %s", emptyOffMap["board"])
	}

	// Create a goal with a folded child and a surfaced needs_you child.
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Goal Alpha"}); bad {
		t.Fatal(out)
	}
	goal, ok := todoByTitle(s, "Goal Alpha")
	if !ok {
		t.Fatal("missing Goal Alpha")
	}
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Child Quiet", "part_of": goal.ID}); bad {
		t.Fatal(out)
	}
	childQuiet, ok := todoByTitle(s, "Child Quiet")
	if !ok {
		t.Fatal("missing Child Quiet")
	}
	if out, bad := tool(t, s, "todo_create", map[string]any{"title": "Child Blocked", "part_of": goal.ID}); bad {
		t.Fatal(out)
	}
	childBlocked, ok := todoByTitle(s, "Child Blocked")
	if !ok {
		t.Fatal("missing Child Blocked")
	}

	// Mark goal with explicit direction_id and childBlocked with a failed start so it surfaces to needs_you.
	s.todos.mu.Lock()
	s.todos.todos[goal.ID].DirectionID = "dir_alpha"
	s.todos.todos[childBlocked.ID].StartError = "agent failed to launch"
	s.todos.mu.Unlock()

	// 1. Baseline (todos_v2=false): board must be omitted.
	baselineResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if baselineResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos baseline: %d %s", baselineResp.Code, baselineResp.Body.String())
	}
	baselineBytes := append([]byte(nil), baselineResp.Body.Bytes()...)
	var baselineTop map[string]json.RawMessage
	if err := json.Unmarshal(baselineBytes, &baselineTop); err != nil {
		t.Fatalf("decode baseline: %v", err)
	}
	if _, has := baselineTop["board"]; has {
		t.Fatalf("baseline todos_v2=false must omit board, got: %s", baselineTop["board"])
	}

	// 2. Enable todos_v2=true: board must be emitted with exact folding, surfacing, direction group, and sidebar tree.
	setTodosV2ForTest(t, s, true, false)
	onResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if onResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos on: %d %s", onResp.Code, onResp.Body.String())
	}
	var onPayload struct {
		Board  *todoBoardProjection     `json:"board"`
		Counts assistantwire.TodoCounts `json:"counts"`
	}
	if err := json.Unmarshal(onResp.Body.Bytes(), &onPayload); err != nil {
		t.Fatalf("decode on payload: %v", err)
	}
	if onPayload.Board == nil {
		t.Fatal("todos_v2=true expected non-nil board in GET /api/v1/todos")
	}
	b := onPayload.Board
	// Both the goal and its blocking child surface in Needs you. The list and board totals count their goal once.
	if b.Counts.OpenTotal != 1 || b.Counts.NeedsYou != 1 ||
		b.Counts.OpenTotal != onPayload.Counts.Open || b.Counts.NeedsYou != onPayload.Counts.NeedsYou {
		t.Fatalf("board.counts = %+v, list counts = %+v, want open=1 needs_you=1", b.Counts, onPayload.Counts)
	}
	nyGroup := b.StatusGroups[0]
	if nyGroup.Key != boardSectionNeedsYou || nyGroup.TotalCount != 2 || nyGroup.VisibleCount != 2 {
		t.Fatalf("needs_you group = %+v, want total=2 visible=2", nyGroup)
	}
	var goalEntry *todoBoardEntry
	var sawSurfacedChild bool
	for i := range nyGroup.Entries {
		if nyGroup.Entries[i].TodoID == goal.ID {
			goalEntry = &nyGroup.Entries[i]
		}
		if nyGroup.Entries[i].TodoID == childBlocked.ID {
			sawSurfacedChild = true
		}
	}
	if !sawSurfacedChild || goalEntry == nil {
		t.Fatalf("needs_you entries = %+v, want both %s and %s", nyGroup.Entries, goal.ID, childBlocked.ID)
	}
	if !reflect.DeepEqual(goalEntry.FoldedChildIDs, []string{childQuiet.ID}) ||
		!reflect.DeepEqual(goalEntry.SurfacedChildIDs, []string{childBlocked.ID}) ||
		goalEntry.FoldedCount != 1 {
		t.Fatalf("goal entry = %+v", goalEntry)
	}
	todoGroup := requireBoardStatusGroup(t, b, boardSectionTodo)
	if todoGroup.TotalCount != 1 || todoGroup.VisibleCount != 0 {
		t.Fatalf("todo group = %+v, want total=1 visible=0", todoGroup)
	}
	if len(b.DirectionGroups) != 1 || b.DirectionGroups[0].DirectionID != "dir_alpha" || b.DirectionGroups[0].Counts.Total != 3 {
		t.Fatalf("direction_groups = %+v, want [dir_alpha(total=3)]", b.DirectionGroups)
	}
	if len(b.SidebarTree) != 2 {
		t.Fatalf("sidebar_tree = %+v, want 2 visible open nodes", b.SidebarTree)
	}

	// 3. Rollback todos_v2=false: response must omit board and match baseline byte-for-byte.
	setTodosV2ForTest(t, s, false, true)
	rollbackResp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if rollbackResp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos rollback: %d %s", rollbackResp.Code, rollbackResp.Body.String())
	}
	if !bytes.Equal(rollbackResp.Body.Bytes(), baselineBytes) {
		t.Fatalf("todos_v2 rollback GET /api/v1/todos bytes diverged from baseline:\ngot:  %s\nwant: %s", rollbackResp.Body.String(), string(baselineBytes))
	}
}

func TestTodosV2RepoGatePromotesBoundOpenPRToWaitingNotDraftOrRef(t *testing.T) {
	base := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	list := []listedTodo{
		{
			Todo: Todo{ID: "open_pr_repo", Title: "Bound open PR", CreatedAt: base},
			RepoGate: &repoGate{
				State:        repoGateBlockedConflict,
				TargetCount:  1,
				BlockingRepo: "org/app",
				BlockingPR:   "https://github.com/org/app/pull/12",
				Reason:       "merge_conflicts",
			},
		},
		{
			Todo: Todo{ID: "draft_pr_repo", Title: "Bound draft PR", CreatedAt: base},
			RepoGate: &repoGate{
				State:        repoGateAwaitingReview,
				TargetCount:  1,
				BlockingRepo: "org/app",
				BlockingPR:   "https://github.com/org/app/pull/13",
				Reason:       "draft_pr",
			},
		},
		{
			Todo: Todo{ID: "ref_only_repo", Title: "Reference repo", CreatedAt: base},
			RepoGate: &repoGate{
				State:          repoGateReferenceOnly,
				ReferenceCount: 1,
				Reason:         "reference_only",
			},
		},
	}
	facts := make([]projection.Facts, len(list))
	for i, row := range list {
		facts[i] = projectionFacts(row, nil, nil)
		facts[i].TodosV2 = true
	}
	views, waits := projection.ViewsAndWaits(facts, base)

	if views[0].Section != assistantwire.TodoListSectionWaiting || views[0].Reason != assistantwire.TodoReasonPrOpen {
		t.Fatalf("open_pr_repo view = %+v, want waiting/pr_open", views[0])
	}
	if waits[0] == nil || waits[0].Kind != assistantwire.TodoWaitKindPrMerge ||
		!reflect.DeepEqual(waits[0].PrBlockers, []assistantwire.TodoWaitPRBlocker{assistantwire.TodoWaitPRBlockerConflicts}) {
		t.Fatalf("open_pr_repo wait = %+v, want pr_merge with conflicts", waits[0])
	}
	if views[1].Section != assistantwire.TodoListSectionTodo || waits[1] != nil {
		t.Fatalf("draft_pr_repo unexpectedly promoted: view=%+v wait=%+v", views[1], waits[1])
	}
	if views[2].Section != assistantwire.TodoListSectionTodo || waits[2] != nil {
		t.Fatalf("ref_only_repo unexpectedly promoted: view=%+v wait=%+v", views[2], waits[2])
	}

	// Fallback classifyBoardSection (View == nil) orders open-PR repo_gate items ahead of unstarted items.
	secOpen, subOpen := classifyBoardSection(list[0])
	secDraft, subDraft := classifyBoardSection(list[1])
	if secOpen != boardSectionWaiting || subOpen != 0 {
		t.Fatalf("classifyBoardSection(open_pr_repo) = (%q, %d), want (%q, 0)", secOpen, subOpen, boardSectionWaiting)
	}
	if secDraft != boardSectionTodo || subDraft != 0 {
		t.Fatalf("classifyBoardSection(draft_pr_repo) = (%q, %d), want (%q, 0)", secDraft, subDraft, boardSectionTodo)
	}
}
