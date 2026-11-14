// Package workspaceboard implements the board without database or session side effects.
package workspaceboard

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxColumns    = 32
	MaxCards      = 1000
	MaxBatchCards = MaxCards
	MaxTitleRunes = 200
	MaxIDBytes    = 1024
	MaxPathBytes  = 8192
)

var ErrInvalid = errors.New("invalid workspace board")

type State struct {
	Revision int64    `json:"revision"`
	Columns  []Column `json:"columns"`
	Cards    []Card   `json:"cards"`
}
type Column struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type Card struct {
	ID          string `json:"id"`
	MachineID   string `json:"machine_id"`
	WorkspaceID string `json:"workspace_id"`
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	Source      string `json:"source"`
	SourceID    string `json:"source_id,omitempty"`
	Branch      string `json:"branch,omitempty"`
	ColumnID    string `json:"column_id"`
	Pinned      bool   `json:"pinned"`
	Archived    bool   `json:"archived"`
}

// CardChange is a reversible edit to one existing card. A column also requests a
// move, appended unless BeforeID names a card in that column at this step.
// It deliberately cannot change bindings, create cards or run node operations.
type CardChange struct {
	ID       string `json:"id"`
	ColumnID string `json:"column_id,omitempty"`
	BeforeID string `json:"before_id,omitempty"`
	Pinned   *bool  `json:"pinned,omitempty"`
	Archived *bool  `json:"archived,omitempty"`
}

type Mutation struct {
	Revision    int64        `json:"revision"`
	Action      string       `json:"action"`
	ID          string       `json:"id,omitempty"`
	ColumnID    string       `json:"column_id,omitempty"`
	BeforeID    string       `json:"before_id,omitempty"`
	Title       *string      `json:"title,omitempty"`
	Pinned      *bool        `json:"pinned,omitempty"`
	Archived    *bool        `json:"archived,omitempty"`
	MachineID   string       `json:"machine_id,omitempty"`
	WorkspaceID string       `json:"workspace_id,omitempty"`
	Cwd         string       `json:"cwd,omitempty"`
	MoveTo      string       `json:"move_to,omitempty"`
	Changes     []CardChange `json:"changes,omitempty"`
	// Discovery metadata is set by the hub after validating the workspace on its node.
	Source   string `json:"-"`
	SourceID string `json:"-"`
	Branch   string `json:"-"`
}

func Default() State {
	return State{Columns: []Column{{"todo", "待办"}, {"doing", "进行中"}, {"review", "待审查"}, {"done", "已完成"}}, Cards: []Card{}}
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }
func validID(id string) bool {
	return id != "" && len(id) <= MaxIDBytes && strings.TrimSpace(id) == id && !strings.ContainsFunc(id, unicode.IsControl) && utf8.ValidString(id)
}
func validTitle(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= MaxTitleRunes && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// Validate rejects corrupt references and duplicate bindings, including archived cards.
// Cwd is a node-canonical absolute path on its remote machine.
func Validate(s State) error {
	if s.Revision < 0 {
		return invalid("negative revision")
	}
	if len(s.Columns) < 1 || len(s.Columns) > MaxColumns {
		return invalid("column count out of bounds")
	}
	if len(s.Cards) > MaxCards {
		return invalid("too many cards")
	}
	columns := make(map[string]bool, len(s.Columns))
	for _, c := range s.Columns {
		if !validID(c.ID) || !validTitle(c.Title) || columns[c.ID] {
			return invalid("invalid or duplicate column")
		}
		columns[c.ID] = true
	}
	ids := make(map[string]bool, len(s.Cards))
	type binding struct{ machine, cwd string }
	bindings := make(map[binding]bool, len(s.Cards))
	for _, c := range s.Cards {
		if !validID(c.ID) || ids[c.ID] {
			return invalid("invalid or duplicate card id")
		}
		if !validID(c.MachineID) || !validID(c.WorkspaceID) || !validTitle(c.Title) || !validID(c.Source) {
			return invalid("invalid card metadata")
		}
		if (c.SourceID != "" && !validID(c.SourceID)) || len(c.Branch) > MaxIDBytes || !utf8.ValidString(c.Branch) || strings.ContainsFunc(c.Branch, unicode.IsControl) {
			return invalid("invalid source metadata")
		}
		canonical, err := CanonicalCwd(c.Cwd)
		if err != nil {
			return err
		}
		if !columns[c.ColumnID] {
			return invalid("unknown card column")
		}
		key := binding{c.MachineID, canonical}
		if bindings[key] {
			return invalid("workspace already bound; restore its existing card")
		}
		bindings[key], ids[c.ID] = true, true
	}
	return nil
}

// Apply returns independent slices even for no-op moves; it never changes the revision.
// Optimistic concurrency and the revision increment belong to the database.
func Apply(current State, m Mutation) (State, error) {
	if m.Action != "batch_cards" && m.Changes != nil {
		return State{}, invalid("changes require batch_cards")
	}
	if err := Validate(current); err != nil {
		return State{}, err
	}
	next := State{Revision: current.Revision, Columns: append([]Column{}, current.Columns...), Cards: append([]Card{}, current.Cards...)}
	cardIndex := func(id string) int {
		for i, c := range next.Cards {
			if c.ID == id {
				return i
			}
		}
		return -1
	}
	colIndex := func(id string) int {
		for i, c := range next.Columns {
			if c.ID == id {
				return i
			}
		}
		return -1
	}
	title := func() string {
		if m.Title == nil {
			return ""
		}
		return strings.TrimSpace(*m.Title)
	}
	switch m.Action {
	case "batch_cards":
		if m.ID != "" || m.ColumnID != "" || m.BeforeID != "" || m.Title != nil || m.Pinned != nil || m.Archived != nil || m.MachineID != "" || m.WorkspaceID != "" || m.Cwd != "" || m.MoveTo != "" || m.Source != "" || m.SourceID != "" || m.Branch != "" {
			return State{}, invalid("batch_cards accepts only revision, action and changes")
		}
		if err := applyCardChanges(&next, m.Changes); err != nil {
			return State{}, err
		}
	case "add_card":
		canonical, err := CanonicalCwd(m.Cwd)
		if err != nil {
			return State{}, err
		}
		c := Card{ID: m.ID, MachineID: m.MachineID, WorkspaceID: m.WorkspaceID, Title: title(), Cwd: canonical, Source: m.Source, SourceID: m.SourceID, Branch: m.Branch, ColumnID: m.ColumnID}
		if m.Pinned != nil {
			c.Pinned = *m.Pinned
		}
		if m.Archived != nil {
			c.Archived = *m.Archived
		}
		next.Cards = append(next.Cards, c)
	case "update_card":
		i := cardIndex(m.ID)
		if i < 0 {
			return State{}, invalid("unknown card")
		}
		if m.Title != nil {
			next.Cards[i].Title = title()
		}
		if m.Pinned != nil {
			next.Cards[i].Pinned = *m.Pinned
		}
		if m.Archived != nil {
			next.Cards[i].Archived = *m.Archived
		}
	case "move_card":
		i := cardIndex(m.ID)
		if i < 0 || colIndex(m.ColumnID) < 0 {
			return State{}, invalid("unknown card or target column")
		}
		if m.BeforeID != "" {
			before := cardIndex(m.BeforeID)
			if before < 0 || next.Cards[before].ColumnID != m.ColumnID {
				return State{}, invalid("before card must be in the target column")
			}
		}
		if m.BeforeID == m.ID {
			break
		}
		c := next.Cards[i]
		c.ColumnID = m.ColumnID
		next.Cards = append(next.Cards[:i], next.Cards[i+1:]...)
		at := len(next.Cards)
		if m.BeforeID != "" {
			at = cardIndex(m.BeforeID)
		}
		next.Cards = append(next.Cards, Card{})
		copy(next.Cards[at+1:], next.Cards[at:])
		next.Cards[at] = c
	case "add_column":
		next.Columns = append(next.Columns, Column{ID: m.ID, Title: title()})
	case "update_column":
		i := colIndex(m.ID)
		if i < 0 || m.Title == nil {
			return State{}, invalid("unknown column or missing title")
		}
		next.Columns[i].Title = title()
	case "move_column":
		i := colIndex(m.ID)
		if i < 0 || (m.BeforeID != "" && colIndex(m.BeforeID) < 0) {
			return State{}, invalid("unknown column")
		}
		if m.BeforeID == m.ID {
			break
		}
		c := next.Columns[i]
		next.Columns = append(next.Columns[:i], next.Columns[i+1:]...)
		at := len(next.Columns)
		if m.BeforeID != "" {
			at = colIndex(m.BeforeID)
		}
		next.Columns = append(next.Columns, Column{})
		copy(next.Columns[at+1:], next.Columns[at:])
		next.Columns[at] = c
	case "delete_column":
		i := colIndex(m.ID)
		if i < 0 {
			return State{}, invalid("unknown column")
		}
		if m.MoveTo != "" && (m.MoveTo == m.ID || colIndex(m.MoveTo) < 0) {
			return State{}, invalid("invalid replacement column")
		}
		for j := range next.Cards {
			if next.Cards[j].ColumnID == m.ID {
				if m.MoveTo == "" {
					return State{}, invalid("nonempty column requires a replacement")
				}
				next.Cards[j].ColumnID = m.MoveTo
			}
		}
		next.Columns = append(next.Columns[:i], next.Columns[i+1:]...)
	default:
		return State{}, invalid("unknown action")
	}
	if err := Validate(next); err != nil {
		return State{}, err
	}
	return next, nil
}

// applyCardChanges owns next's independent slices. A failure discards every edit,
// including edits before an invalid later anchor. Revision changes and persistence
// happen once outside Apply, never once per card.
func applyCardChanges(next *State, changes []CardChange) error {
	if len(changes) == 0 || len(changes) > MaxBatchCards {
		return invalid("batch card count out of bounds")
	}
	known := make(map[string]bool, len(next.Cards))
	for _, card := range next.Cards {
		known[card.ID] = true
	}
	columns := make(map[string]bool, len(next.Columns))
	for _, column := range next.Columns {
		columns[column.ID] = true
	}
	seen := make(map[string]bool, len(changes))
	for _, change := range changes {
		if !validID(change.ID) || !known[change.ID] || seen[change.ID] {
			return invalid("invalid, unknown or duplicate batch card")
		}
		seen[change.ID] = true
		if change.ColumnID == "" && change.Pinned == nil && change.Archived == nil {
			return invalid("batch card has no changes")
		}
		if change.ColumnID != "" && (!validID(change.ColumnID) || !columns[change.ColumnID]) {
			return invalid("unknown batch target column")
		}
		if change.BeforeID != "" && (change.ColumnID == "" || !validID(change.BeforeID) || !known[change.BeforeID] || change.BeforeID == change.ID) {
			return invalid("invalid batch position")
		}
	}
	index := func(id string) int {
		for i, card := range next.Cards {
			if card.ID == id {
				return i
			}
		}
		return -1
	}
	for _, change := range changes {
		i := index(change.ID)
		card := next.Cards[i]
		if change.Pinned != nil {
			card.Pinned = *change.Pinned
		}
		if change.Archived != nil {
			card.Archived = *change.Archived
		}
		if change.ColumnID == "" {
			next.Cards[i] = card
			continue
		}
		if change.BeforeID != "" && next.Cards[index(change.BeforeID)].ColumnID != change.ColumnID {
			return invalid("before card must be in the target column")
		}
		card.ColumnID = change.ColumnID
		next.Cards = append(next.Cards[:i], next.Cards[i+1:]...)
		at := len(next.Cards)
		if change.BeforeID != "" {
			at = index(change.BeforeID)
		}
		next.Cards = append(next.Cards, Card{})
		copy(next.Cards[at+1:], next.Cards[at:])
		next.Cards[at] = card
	}
	return nil
}

// CanonicalCwd normalizes remote path syntax without consulting the hub filesystem.
// The node must resolve symlinks and verify the directory before binding it.
func CanonicalCwd(cwd string) (string, error) {
	if len(cwd) > MaxPathBytes || strings.ContainsRune(cwd, 0) || !utf8.ValidString(cwd) {
		return "", invalid("invalid workspace path")
	}
	if len(cwd) >= 2 && ((cwd[0] >= 'a' && cwd[0] <= 'z') || (cwd[0] >= 'A' && cwd[0] <= 'Z')) && cwd[1] == ':' {
		if len(cwd) < 3 || (cwd[2] != '/' && cwd[2] != '\\') {
			return "", invalid("workspace path must be absolute")
		}
		return strings.ToLower(cwd[:2]) + path.Clean(strings.ReplaceAll(cwd[2:], "\\", "/")), nil
	}
	if strings.HasPrefix(cwd, "\\\\") || strings.HasPrefix(cwd, "//") {
		parts := strings.Split(strings.ReplaceAll(cwd[2:], "\\", "/"), "/")
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
			return "", invalid("UNC workspace path needs a server and share")
		}
		root := "//" + strings.ToLower(parts[0]) + "/" + strings.ToLower(parts[1])
		rest := path.Clean("/" + strings.Join(parts[2:], "/"))
		if rest == "/" {
			return root, nil
		}
		return root + rest, nil
	}
	if !path.IsAbs(cwd) {
		return "", invalid("workspace path must be absolute")
	}
	return path.Clean(cwd), nil
}
