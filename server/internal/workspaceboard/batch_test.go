package workspaceboard

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBatchCardsOrderedMoveAndMetadata(t *testing.T) {
	original := fixture()
	before, _ := json.Marshal(original)
	next := apply(t, original, Mutation{Action: "batch_cards", Changes: []CardChange{
		{ID: "0", ColumnID: "done", Pinned: flag(true)},
		{ID: "1", ColumnID: "done", BeforeID: "0", Archived: flag(true)},
		{ID: "2", Pinned: flag(true)},
	}})
	if !reflect.DeepEqual(cardIDs(next), []string{"2", "3", "1", "0"}) {
		t.Fatalf("request order or anchor lost: %v", cardIDs(next))
	}
	if next.Revision != original.Revision || !next.Cards[0].Pinned || !next.Cards[2].Archived || next.Cards[2].ColumnID != "done" || !next.Cards[3].Pinned {
		t.Fatalf("batch metadata: %+v", next)
	}
	for _, card := range next.Cards {
		prior := original.Cards[int(card.ID[0]-'0')]
		prior.ColumnID, prior.Pinned, prior.Archived = card.ColumnID, card.Pinned, card.Archived
		if !reflect.DeepEqual(prior, card) {
			t.Fatalf("batch changed a binding: %+v", card)
		}
	}
	// Undo can restore order with right-to-left moves, anchoring to a card moved earlier.
	next = apply(t, next, Mutation{Action: "batch_cards", Changes: []CardChange{
		{ID: "1", ColumnID: "todo", Archived: flag(false)},
		{ID: "0", ColumnID: "todo", BeforeID: "1", Pinned: flag(false)},
		{ID: "2", Pinned: flag(false)},
	}})
	var todo []string
	for _, card := range next.Cards {
		if card.ColumnID == "todo" {
			todo = append(todo, card.ID)
		}
	}
	if !reflect.DeepEqual(todo, []string{"0", "1"}) || next.Cards[0].Pinned {
		t.Fatalf("undo did not restore order and flags: %+v", next)
	}
	next.Cards[0].Title = "Changed"
	next.Columns[0].Title = "Changed"
	after, _ := json.Marshal(original)
	if string(after) != string(before) {
		t.Fatal("batch result shares mutable slices with the original")
	}
}

func TestBatchCardsRejectsWholeOperation(t *testing.T) {
	cases := map[string]Mutation{
		"missing changes":            {Action: "batch_cards"},
		"empty changes":              {Action: "batch_cards", Changes: []CardChange{}},
		"too many":                   {Action: "batch_cards", Changes: make([]CardChange, MaxBatchCards+1)},
		"single action with changes": {Action: "update_card", ID: "0", Pinned: flag(true), Changes: []CardChange{}},
		"batch with single ID":       {Action: "batch_cards", ID: "0", Changes: []CardChange{{ID: "0", Pinned: flag(true)}}},
		"batch with title":           {Action: "batch_cards", Title: text("new"), Changes: []CardChange{{ID: "0", Pinned: flag(true)}}},
		"batch with binding":         {Action: "batch_cards", MachineID: "other", Changes: []CardChange{{ID: "0", Pinned: flag(true)}}},
		"duplicate card":             {Action: "batch_cards", Changes: []CardChange{{ID: "0", Pinned: flag(true)}, {ID: "0", Archived: flag(true)}}},
	}
	for name, change := range map[string]CardChange{
		"unknown":               {ID: "missing", Pinned: flag(true)},
		"empty ID":              {Pinned: flag(true)},
		"invalid ID":            {ID: "1\n", Pinned: flag(true)},
		"long ID":               {ID: strings.Repeat("x", MaxIDBytes+1), Pinned: flag(true)},
		"no edits":              {ID: "1"},
		"anchor without column": {ID: "1", BeforeID: "0", Pinned: flag(true)},
		"unknown column":        {ID: "1", ColumnID: "missing"},
		"unknown anchor":        {ID: "1", ColumnID: "todo", BeforeID: "missing"},
		"self anchor":           {ID: "1", ColumnID: "todo", BeforeID: "1"},
		"wrong column anchor":   {ID: "1", ColumnID: "todo", BeforeID: "2"},
	} {
		cases[name] = Mutation{Action: "batch_cards", Changes: []CardChange{
			{ID: "0", Pinned: flag(true), Archived: flag(true)}, change,
		}}
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			original := fixture()
			before, _ := json.Marshal(original)
			result, err := Apply(original, mutation)
			if !errors.Is(err, ErrInvalid) || !reflect.DeepEqual(result, State{}) {
				t.Fatalf("partial result or success: %+v, %v", result, err)
			}
			after, _ := json.Marshal(original)
			if string(before) != string(after) {
				t.Fatal("failed batch changed the caller's state")
			}
		})
	}
}

func TestBatchCardsCoversMaximumBoard(t *testing.T) {
	original := Default()
	changes := make([]CardChange, MaxBatchCards)
	for i := range MaxBatchCards {
		id := fmt.Sprint(i)
		original.Cards = append(original.Cards, Card{ID: id, MachineID: "computer", WorkspaceID: id, Title: "Workspace", Cwd: "/work/" + id, Source: "directory", ColumnID: "todo"})
		changes[i] = CardChange{ID: id, ColumnID: "done", Archived: flag(true)}
	}
	next := apply(t, original, Mutation{Action: "batch_cards", Changes: changes})
	if len(next.Cards) != MaxBatchCards {
		t.Fatalf("lost cards: %d", len(next.Cards))
	}
	for i, card := range next.Cards {
		if card.ID != fmt.Sprint(i) || !card.Archived || card.ColumnID != "done" || original.Cards[i].Archived {
			t.Fatalf("wrong card at %d: %+v", i, card)
		}
	}
}
