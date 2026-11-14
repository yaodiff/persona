package workspaceboard

import (
	"slices"
	"strconv"
	"time"
)

const (
	ActivityRetentionLimit = 500
	ActivityRetentionBytes = 16 << 20
)

// ActivityValue contains only historical presentation metadata. Bindings, paths,
// machine identities and conversation contents never enter the journal.
type ActivityValue struct {
	Title       string `json:"title"`
	ColumnID    string `json:"column_id,omitempty"`
	ColumnTitle string `json:"column_title,omitempty"`
	Pinned      *bool  `json:"pinned,omitempty"`
	Archived    *bool  `json:"archived,omitempty"`
}

type ActivityChange struct {
	Kind    string         `json:"kind"`
	ID      string         `json:"id,omitempty"`
	Title   string         `json:"title,omitempty"`
	Fields  []string       `json:"fields,omitempty"`
	Before  *ActivityValue `json:"before,omitempty"`
	After   *ActivityValue `json:"after,omitempty"`
	CardIDs []string       `json:"card_ids,omitempty"`
}

type ActivitySummary struct {
	Revision    string    `json:"revision"`
	At          time.Time `json:"at"`
	ChangeCount int       `json:"change_count"`
	CardCount   int       `json:"card_count"`
	Kinds       []string  `json:"kinds"`
}

type ActivityPage struct {
	Entries        []ActivitySummary `json:"entries"`
	NextCursor     string            `json:"next_cursor,omitempty"`
	LatestRevision string            `json:"latest_revision"`
	OldestRevision string            `json:"oldest_revision,omitempty"`
	RetentionLimit int               `json:"retention_limit"`
}

type ActivityDetail struct {
	Revision   string           `json:"revision"`
	At         time.Time        `json:"at"`
	Changes    []ActivityChange `json:"changes"`
	Total      int              `json:"total"`
	NextOffset *int             `json:"next_offset,omitempty"`
}

// ActivityChanges compares validated board documents, independent of the operation
// that produced them. A client cannot forge the history by describing its own edit.
func ActivityChanges(before, after State) []ActivityChange {
	changes := []ActivityChange{}
	oldColumns, newColumns := make(map[string]Column, len(before.Columns)), make(map[string]Column, len(after.Columns))
	oldCards, newCards := make(map[string]Card, len(before.Cards)), make(map[string]Card, len(after.Cards))
	for _, column := range before.Columns {
		oldColumns[column.ID] = column
	}
	for _, column := range after.Columns {
		newColumns[column.ID] = column
	}
	for _, card := range before.Cards {
		oldCards[card.ID] = card
	}
	for _, card := range after.Cards {
		newCards[card.ID] = card
	}
	value := func(card Card, columns map[string]Column) *ActivityValue {
		pinned, archived := card.Pinned, card.Archived
		return &ActivityValue{Title: card.Title, ColumnID: card.ColumnID, ColumnTitle: columns[card.ColumnID].Title, Pinned: &pinned, Archived: &archived}
	}
	for _, card := range before.Cards {
		if _, exists := newCards[card.ID]; !exists {
			changes = append(changes, ActivityChange{Kind: "card_removed", ID: card.ID, Title: card.Title, Before: value(card, oldColumns)})
		}
	}
	for _, card := range after.Cards {
		old, exists := oldCards[card.ID]
		if !exists {
			changes = append(changes, ActivityChange{Kind: "card_added", ID: card.ID, Title: card.Title, After: value(card, newColumns)})
			continue
		}
		fields := []string{}
		if old.Title != card.Title {
			fields = append(fields, "title")
		}
		if old.ColumnID != card.ColumnID {
			fields = append(fields, "column")
		}
		if old.Pinned != card.Pinned {
			fields = append(fields, "pinned")
		}
		if old.Archived != card.Archived {
			fields = append(fields, "archived")
		}
		if len(fields) > 0 {
			changes = append(changes, ActivityChange{Kind: "card_updated", ID: card.ID, Title: card.Title, Fields: fields, Before: value(old, oldColumns), After: value(card, newColumns)})
		}
	}
	for _, column := range before.Columns {
		if _, exists := newColumns[column.ID]; !exists {
			changes = append(changes, ActivityChange{Kind: "column_removed", ID: column.ID, Title: column.Title, Before: &ActivityValue{Title: column.Title}})
		}
	}
	for _, column := range after.Columns {
		old, exists := oldColumns[column.ID]
		if !exists {
			changes = append(changes, ActivityChange{Kind: "column_added", ID: column.ID, Title: column.Title, After: &ActivityValue{Title: column.Title}})
		} else if old.Title != column.Title {
			changes = append(changes, ActivityChange{Kind: "column_renamed", ID: column.ID, Title: column.Title, Before: &ActivityValue{Title: old.Title}, After: &ActivityValue{Title: column.Title}})
		}
	}
	// Compare only common columns: inserting or deleting a column shifts indices,
	// but leaves every surviving column in the same relative order.
	oldOrder, newOrder := []string{}, []string{}
	for _, column := range before.Columns {
		if _, exists := newColumns[column.ID]; exists {
			oldOrder = append(oldOrder, column.ID)
		}
	}
	for _, column := range after.Columns {
		if _, exists := oldColumns[column.ID]; exists {
			newOrder = append(newOrder, column.ID)
		}
	}
	if !slices.Equal(oldOrder, newOrder) {
		changes = append(changes, ActivityChange{Kind: "columns_reordered"})
	}
	for _, column := range after.Columns {
		if _, exists := oldColumns[column.ID]; !exists {
			continue
		}
		oldOrder, newOrder = []string{}, []string{}
		for _, card := range before.Cards {
			if next, exists := newCards[card.ID]; exists && card.ColumnID == column.ID && next.ColumnID == column.ID {
				oldOrder = append(oldOrder, card.ID)
			}
		}
		for _, card := range after.Cards {
			if old, exists := oldCards[card.ID]; exists && card.ColumnID == column.ID && old.ColumnID == column.ID {
				newOrder = append(newOrder, card.ID)
			}
		}
		if slices.Equal(oldOrder, newOrder) {
			continue
		}
		// A snapshot cannot tell which card was dragged in a swap. Describe the
		// changed span as one order edit, without guessing an actor. Added/removed
		// cards, cross-column moves and pin grouping cannot create an order edit.
		first, last := 0, len(newOrder)-1
		for oldOrder[first] == newOrder[first] {
			first++
		}
		for oldOrder[last] == newOrder[last] {
			last--
		}
		changes = append(changes, ActivityChange{Kind: "cards_reordered", ID: column.ID, Title: column.Title, CardIDs: append([]string{}, newOrder[first:last+1]...)})
	}
	return changes
}

// ActivityCardIDs is the distinct set a card-specific history query searches.
func ActivityCardIDs(changes []ActivityChange) []string {
	ids := map[string]bool{}
	for _, change := range changes {
		switch change.Kind {
		case "card_added", "card_removed", "card_updated":
			ids[change.ID] = true
		case "cards_reordered":
			for _, id := range change.CardIDs {
				ids[id] = true
			}
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

func ActivitySummaryFor(revision int64, at time.Time, changes []ActivityChange) ActivitySummary {
	kinds := []string{}
	for _, change := range changes {
		if !slices.Contains(kinds, change.Kind) {
			kinds = append(kinds, change.Kind)
		}
	}
	return ActivitySummary{Revision: strconv.FormatInt(revision, 10), At: at, ChangeCount: len(changes), CardCount: len(ActivityCardIDs(changes)), Kinds: kinds}
}
