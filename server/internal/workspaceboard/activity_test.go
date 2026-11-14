package workspaceboard

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func cloneActivityState(s State) State {
	s.Columns = slices.Clone(s.Columns)
	s.Cards = slices.Clone(s.Cards)
	return s
}

func TestActivityChangesHistoricalMetadata(t *testing.T) {
	before := fixture()
	after := cloneActivityState(before)
	after.Cards[0].Title = "New title"
	after.Cards[0].ColumnID = "done"
	after.Cards[0].Pinned, after.Cards[0].Archived = true, true
	after.Columns[3].Title = "Finished"
	changes := ActivityChanges(before, after)
	if len(changes) != 2 || changes[0].Kind != "card_updated" || changes[1].Kind != "column_renamed" {
		t.Fatalf("changes: %+v", changes)
	}
	card := changes[0]
	if !slices.Equal(card.Fields, []string{"title", "column", "pinned", "archived"}) || card.Before.Title != "Project 0" || card.Before.ColumnTitle != "待办" || card.After.ColumnTitle != "Finished" || *card.Before.Pinned || !*card.After.Archived {
		t.Fatalf("historical metadata: %+v", card)
	}
	// The presentation must survive later renames without depending on live state.
	after.Cards[0].Title, after.Columns[3].Title = "Later name", "Later column"
	if card.After.Title != "New title" || card.After.ColumnTitle != "Finished" {
		t.Fatal("journal aliases mutable state")
	}
	raw, _ := json.Marshal(changes)
	for _, forbidden := range []string{"machine_id", "workspace_id", "source", "cwd", "branch", "/projects/", "machine-a"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("history leaked %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(string(raw), `"pinned":false`) || !strings.Contains(string(raw), `"archived":false`) {
		t.Fatalf("false metadata disappeared: %s", raw)
	}
}

func TestActivityChangesDoNotMistakeShiftsForReorders(t *testing.T) {
	before := fixture()
	for name, edit := range map[string]func(*State){
		"card removed":         func(s *State) { s.Cards = s.Cards[1:] },
		"card inserted":        func(s *State) { c := s.Cards[0]; c.ID = "new"; s.Cards = append([]Card{c}, s.Cards...) },
		"moved across columns": func(s *State) { s.Cards[0].ColumnID = "doing" },
		"pin grouping":         func(s *State) { s.Cards[1].Pinned = true },
		"column removed":       func(s *State) { s.Columns = s.Columns[:3]; s.Cards[3].ColumnID = "doing" },
		"column inserted":      func(s *State) { s.Columns = append([]Column{{ID: "new", Title: "New"}}, s.Columns...) },
		"column renamed":       func(s *State) { s.Columns[0].Title = "Renamed" },
	} {
		t.Run(name, func(t *testing.T) {
			after := cloneActivityState(before)
			edit(&after)
			changes := ActivityChanges(before, after)
			if len(changes) == 0 {
				t.Fatal("missed the real edit")
			}
			for _, change := range changes {
				if change.Kind == "cards_reordered" || change.Kind == "columns_reordered" {
					t.Fatalf("shift is not reorder: %+v", changes)
				}
			}
		})
	}
}

func TestActivityChangesGroupAmbiguousReorders(t *testing.T) {
	before := fixture()
	for i := range before.Cards {
		before.Cards[i].ColumnID = "todo"
	}
	after := cloneActivityState(before)
	after.Cards[1], after.Cards[2] = after.Cards[2], after.Cards[1]
	changes := ActivityChanges(before, after)
	if len(changes) != 1 || changes[0].Kind != "cards_reordered" || changes[0].ID != "todo" || !slices.Equal(changes[0].CardIDs, []string{"2", "1"}) {
		t.Fatalf("swap must be one column edit, not guessed dragged cards: %+v", changes)
	}
	if changes[0].Before != nil || changes[0].After != nil {
		t.Fatal("order must not forge one card's before/after value")
	}
	after.Columns[1], after.Columns[2] = after.Columns[2], after.Columns[1]
	changes = ActivityChanges(before, after)
	if len(changes) != 2 || changes[0].Kind != "columns_reordered" || changes[1].Kind != "cards_reordered" {
		t.Fatalf("column order lost: %+v", changes)
	}
	if !slices.Equal(ActivityCardIDs(changes), []string{"1", "2"}) {
		t.Fatal("column IDs contaminated card filter")
	}
}

func TestActivityAddsRemovesAndNoopRevision(t *testing.T) {
	before := fixture()
	after := cloneActivityState(before)
	after.Cards[0].ID, after.Cards[0].Title = "new", "New workspace"
	after.Columns[2] = Column{ID: "new-column", Title: "New column"}
	changes := ActivityChanges(before, after)
	kinds := []string{}
	for _, change := range changes {
		kinds = append(kinds, change.Kind)
		switch change.Kind {
		case "card_removed", "column_removed":
			if change.Before == nil || change.After != nil {
				t.Fatal("removal must have only before")
			}
		case "card_added", "column_added":
			if change.Before != nil || change.After == nil {
				t.Fatal("addition must have only after")
			}
		}
	}
	if !slices.Equal(kinds, []string{"card_removed", "card_added", "column_removed", "column_added"}) {
		t.Fatal(kinds)
	}
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	summary := ActivitySummaryFor(9007199254740993, at, changes)
	if summary.Revision != "9007199254740993" || summary.CardCount != 2 || summary.ChangeCount != 4 || !slices.Equal(summary.Kinds, kinds) {
		t.Fatalf("summary: %+v", summary)
	}
	noop := ActivityChanges(before, cloneActivityState(before))
	if !reflect.DeepEqual(noop, []ActivityChange{}) {
		t.Fatalf("no-op created changes: %+v", noop)
	}
	if ActivitySummaryFor(8, at, noop).ChangeCount != 0 {
		t.Fatal("no-op revision fabricated an edit")
	}
}
