package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrTodoNotFound      = errors.New("todo not found")
	ErrTodoCompleted     = errors.New("todo is already completed")
	ErrTodoImageNotFound = errors.New("todo image not found")
)

// TodoImage is one picture attached to a to-do. Data is present when it is being created or explicitly read as
// content; ListTodos fills only the metadata and Size, so listing to-dos never reads every image's bytes.
type TodoImage struct {
	TodoID string `json:"-"`
	ID     string `json:"id"`
	Mime   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Name   string `json:"name,omitempty"`
	Size   int    `json:"size,omitempty"`
	Data   []byte `json:"-"`
}

type TodoRow struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	DoneAt    *time.Time `json:"done_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Origin is the assistant conversation that opened it as a task, PlanID the plan it came from (#930); empty
	// for a todo made by hand.
	Origin string `json:"origin"`
	PlanID string `json:"plan_id"`
	// StartError: the task's agent did not start, and why; such a todo is done (#1077). Empty otherwise.
	StartError string `json:"start_error"`
	// DoneBy is who marked it done: "" the user (or a failed start), DoneByAuto the hub's judge (#1092),
	// DoneByReview an accepted review, or DoneByCancelled a cancellation, with DoneReason.
	// ReopenedAt: when the user last reopened it after it was done.
	DoneBy     string     `json:"done_by"`
	DoneReason string     `json:"done_reason"`
	ReopenedAt *time.Time `json:"reopened_at"`
	// Quiet: a task from the main conversation while assistant.main_tasks_quiet was on (#1731): no pushes, Live
	// Activity or task badges; the main conversation hears about it instead. Set once, when it is made.
	Quiet bool `json:"quiet"`
	// DueAt is the moment it is due, DueDate the day (midnight UTC: a calendar day, read in the user's time zone);
	// at most one is set (#1683). RemindAt is when the phone is pushed about it, RemindedAt when that was done;
	// Priority is "" (normal), PriorityHigh or PriorityLow.
	DueAt      *time.Time `json:"due_at"`
	DueDate    *time.Time `json:"due_date"`
	RemindAt   *time.Time `json:"remind_at"`
	RemindedAt *time.Time `json:"reminded_at"`
	Priority   string     `json:"priority"`
	// Project is the slug of the knowledge-base project the todo belongs to (#2581), "" for none.
	Project string `json:"project"`
	// Kind is "" for a to-do or a task, or KindConcern (#3561). Set when it is made, never changed.
	Kind string `json:"kind"`
	// SeenAt is when the owner last looked at it in Just finished (#5707): Got it, or opening it; nil when never, or
	// after an Undo. Only SeeTodo / AutoSeeTodo writes it.
	SeenAt *time.Time `json:"seen_at"`
	// AutoSeenUndone records that the owner explicitly called Undo (DELETE /api/v1/todos/{id}/seen) so background
	// supervision / re-acceptance never silently re-applies auto-seen (#14639).
	AutoSeenUndone bool `json:"auto_seen_undone,omitempty"`
	// InboxItem is the id of the Inbox row it was made from (#6480), "" for any other to-do. Set when it is made.
	InboxItem string `json:"inbox_item"`
	// PartOf is the to-do whose request this one is a piece of, "" for none; After the pieces of that request it waits
	// for, never nil (docs/assistant/todos.md "Requests in pieces"). The manager checks them before they are written.
	PartOf               string   `json:"part_of"`
	After                []string `json:"after"`
	RequireAcceptedAfter bool     `json:"require_accepted_after"`
	// RestoredAt is when the owner last brought it back from Inactive (#10226): activity for its status only, never an
	// edit (updated_at stays). Nil when never. Only RestoreTodo writes it.
	RestoredAt *time.Time `json:"restored_at"`
	// SomedayUntil is when a to-do the owner put off to Someday comes back (#10226), SomedayNoDate for Someday with no
	// date; nil while it is not put off. SetTodoSomeday writes it; reopening (TodoEdit.Apply) and RetryTodo clear it.
	SomedayUntil *time.Time `json:"someday_until"`
	// Responsible is an external responsible party that is not a Persona-started agent (#12712): a long-running
	// external session or a person, with optional machine_id / session_id hints; nil while none is set.
	Responsible *TodoResponsible `json:"responsible,omitempty"`
	// Audience distinguishes human-facing deliverables ("human") from autonomous engineering tasks ("autonomous"),
	// or "" for unset/legacy to-dos (#14619).
	Audience string `json:"audience,omitempty"`
	// DirectionID is the optional organizational direction this to-do belongs to (#14660), decoupled from runtime
	// supervision (supervision.supervisor) and origin. Empty for ungrouped/legacy to-dos.
	DirectionID string `json:"direction_id,omitempty"`
	// ItemType is the optional explicit 2-type classification ("personal" or "task"), or "" (SQL NULL) when unset (#15758).
	ItemType string `json:"item_type,omitempty"`
	// Images are the to-do's pictures, in the order the client supplied them. CreateTodo stores Data atomically with
	// the row; ListTodos reconstructs only their metadata.
	Images []TodoImage `json:"images,omitempty"`
}

// TodoResponsible is an external responsible party for a to-do (#12712, docs/assistant/todos.md): either a
// long-running external session ("session") or a person ("person"). Name is a stable label; MachineID and SessionID
// are optional hints.
type TodoResponsible struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

const (
	ResponsibleSession = "session"
	ResponsiblePerson  = "person"
)

func responsibleParam(r *TodoResponsible) (any, error) {
	if r == nil {
		return nil, nil
	}
	return json.Marshal(r)
}

// SomedayNoDate is the someday_until of a to-do put off to Someday with no date to come back on: a far date, so that
// "put off until" is the one rule for both (docs/assistant/todos.md "Restores and Someday").
var SomedayNoDate = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// KindConcern is a todo for something the owner raised in the main conversation and wants an answer to (#3561,
// docs/assistant/todos.md "Concerns"): it closes only through ReportConcern, once the main conversation told the owner
// the outcome; no other close (by hand, the hub's judge, a review, a failed start) applies to it.
const KindConcern = "concern"

// DoneByReported marks a concern closed by the report of its outcome to the owner (ReportConcern), with the report as
// DoneReason.
const DoneByReported = "reported"

// ErrConcernUnreported: a concern can only be closed by reporting its outcome to the owner (ReportConcern).
var ErrConcernUnreported = errors.New("a concern closes only once its outcome was reported to the owner")

// A todo's priorities besides normal ("").
const (
	PriorityHigh = "high"
	PriorityLow  = "low"
)

// A todo's audience besides unset ("") (#14619).
const (
	AudienceHuman      = "human"
	AudienceAutonomous = "autonomous"
)

// ErrInvalidTodoAudience is returned when a to-do's audience is not "", AudienceHuman, or AudienceAutonomous.
var ErrInvalidTodoAudience = errors.New(`audience must be "", "human", or "autonomous"`)

// ValidateTodoAudience reports whether audience is "", AudienceHuman, or AudienceAutonomous.
func ValidateTodoAudience(audience string) error {
	switch audience {
	case "", AudienceHuman, AudienceAutonomous:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTodoAudience, audience)
	}
}

// A todo's explicit item type besides unset ("") (#15758).
const (
	ItemTypePersonal = "personal"
	ItemTypeTask     = "task"
)

// ErrInvalidTodoItemType is returned when a to-do's item_type is not "", ItemTypePersonal, or ItemTypeTask.
var ErrInvalidTodoItemType = errors.New(`item_type must be "", "personal", or "task"`)

// ValidateTodoItemType reports whether itemType is "", ItemTypePersonal, or ItemTypeTask.
func ValidateTodoItemType(itemType string) error {
	switch itemType {
	case "", ItemTypePersonal, ItemTypeTask:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTodoItemType, itemType)
	}
}

func nullableItemType(itemType string) any {
	if itemType == "" {
		return nil
	}
	return itemType
}

// DoneByAuto marks a todo the hub closed because its task's work was judged finished (docs/assistant/todos.md "Automatic completion").
const DoneByAuto = "auto"

// DoneByReview marks a todo its supervisor (the main conversation's assistant) accepted after reviewing the work, with
// DoneReason (#3124).
const DoneByReview = "review"

// DoneByCancelled marks a cancelled task; further agent work cannot reopen it (#1851).
const DoneByCancelled = "cancelled"

type TodoSpaceRow struct {
	TodoID    string    `json:"todo_id"`
	MachineID string    `json:"machine_id"`
	Space     string    `json:"space"`
	LinkedAt  time.Time `json:"linked_at"`
}

type TodoAgentRow struct {
	TodoID      string     `json:"todo_id"`
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id"`
	LinkedAt    time.Time  `json:"linked_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	Title       string     `json:"title"`
	Cwd         string     `json:"cwd"`
	Agent       string     `json:"agent"`
	StartError  string     `json:"start_error"`
	Source      string     `json:"source"`
	Via         string     `json:"via"`
	DismissedAt *time.Time `json:"dismissed_at"`
}

const todoColumns = `id, title, notes, done_at, created_at, updated_at, origin, plan_id, start_error, done_by, done_reason, reopened_at, quiet,
	due_at, due_date, remind_at, reminded_at, priority, project, kind, seen_at, auto_seen_undone, inbox_item, part_of, after_ids, restored_at, someday_until, require_accepted_after, responsible, audience, direction_id, COALESCE(item_type, '')`

// todoDest is where a row of todoColumns is scanned to.
func todoDest(r *TodoRow) []any {
	return []any{&r.ID, &r.Title, &r.Notes, &r.DoneAt, &r.CreatedAt, &r.UpdatedAt, &r.Origin, &r.PlanID, &r.StartError, &r.DoneBy,
		&r.DoneReason, &r.ReopenedAt, &r.Quiet, &r.DueAt, &r.DueDate, &r.RemindAt, &r.RemindedAt, &r.Priority, &r.Project, &r.Kind,
		&r.SeenAt, &r.AutoSeenUndone, &r.InboxItem, &r.PartOf, &r.After, &r.RestoredAt, &r.SomedayUntil, &r.RequireAcceptedAfter, &r.Responsible, &r.Audience, &r.DirectionID, &r.ItemType}
}

// ListTodos returns all todos ordered by created_at DESC, with image metadata but without image bytes.
func (d *DB) ListTodos(ctx context.Context) ([]TodoRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+todoColumns+` FROM todos ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	var out []TodoRow
	byID := map[string]int{}
	for rows.Next() {
		var r TodoRow
		if err := rows.Scan(todoDest(&r)...); err != nil {
			rows.Close()
			return nil, err
		}
		byID[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	images, err := d.pool.Query(ctx, `SELECT todo_id, id, mime, width, height, name, octet_length(data)
		FROM todo_images ORDER BY todo_id, position`)
	if err != nil {
		return nil, err
	}
	defer images.Close()
	for images.Next() {
		var img TodoImage
		if err := images.Scan(&img.TodoID, &img.ID, &img.Mime, &img.Width, &img.Height, &img.Name, &img.Size); err != nil {
			return nil, err
		}
		if i, ok := byID[img.TodoID]; ok {
			out[i].Images = append(out[i].Images, img)
		}
	}
	return out, images.Err()
}

// ListAllTodoSpaces returns all space links.
func (d *DB) ListAllTodoSpaces(ctx context.Context) ([]TodoSpaceRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, machine_id, space, linked_at FROM todo_spaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoSpaceRow
	for rows.Next() {
		var r TodoSpaceRow
		if err := rows.Scan(&r.TodoID, &r.MachineID, &r.Space, &r.LinkedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllTodoAgents returns all agent links.
func (d *DB) ListAllTodoAgents(ctx context.Context) ([]TodoAgentRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at FROM todo_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoAgentRow
	for rows.Next() {
		var r TodoAgentRow
		if err := rows.Scan(&r.TodoID, &r.MachineID, &r.SessionID, &r.LinkedAt, &r.ReleasedAt, &r.Title, &r.Cwd, &r.Agent, &r.StartError, &r.Source, &r.Via, &r.DismissedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTodo returns one todo by id, or ErrTodoNotFound.
func (d *DB) GetTodo(ctx context.Context, id string) (*TodoRow, error) {
	return getTodo(ctx, d.pool, id, false)
}

// LockTodoTx returns a to-do and holds its row lock through the caller's transaction.
func LockTodoTx(ctx context.Context, tx pgx.Tx, id string) (*TodoRow, error) {
	return getTodo(ctx, tx, id, true)
}

func getTodo(ctx context.Context, q queryRower, id string, lock bool) (*TodoRow, error) {
	query := `SELECT ` + todoColumns + ` FROM todos WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	var r TodoRow
	err := q.QueryRow(ctx, query, id).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// TodoImageContent reads one to-do image's exact stored bytes. It is deliberately separate from ListTodos.
func (d *DB) TodoImageContent(ctx context.Context, todoID, imageID string) (TodoImage, error) {
	var img TodoImage
	err := d.pool.QueryRow(ctx, `SELECT todo_id, id, mime, width, height, name, data FROM todo_images
		WHERE todo_id = $1 AND id = $2`, todoID, imageID).
		Scan(&img.TodoID, &img.ID, &img.Mime, &img.Width, &img.Height, &img.Name, &img.Data)
	if errors.Is(err, pgx.ErrNoRows) {
		return TodoImage{}, ErrTodoImageNotFound
	}
	if err != nil {
		return TodoImage{}, err
	}
	img.Size = len(img.Data)
	return img, nil
}

// TodoImageContents reads all of a to-do's image bytes in their display order for an Agent handoff.
func (d *DB) TodoImageContents(ctx context.Context, todoID string) ([]TodoImage, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, id, mime, width, height, name, data FROM todo_images
		WHERE todo_id = $1 ORDER BY position`, todoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoImage
	for rows.Next() {
		var img TodoImage
		if err := rows.Scan(&img.TodoID, &img.ID, &img.Mime, &img.Width, &img.Height, &img.Name, &img.Data); err != nil {
			return nil, err
		}
		img.Size = len(img.Data)
		out = append(out, img)
	}
	return out, rows.Err()
}

// CreateTodo inserts a new todo and all of its images atomically.
func (d *DB) CreateTodo(ctx context.Context, t TodoRow) error {
	return d.Transaction(ctx, func(tx pgx.Tx) error { return CreateTodoTx(ctx, tx, t) })
}

// CreateTodoTx inserts a new todo and its images in a caller's transaction.
func CreateTodoTx(ctx context.Context, tx pgx.Tx, t TodoRow) error {
	return createTodo(ctx, tx, t)
}

func createTodo(ctx context.Context, executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, t TodoRow) error {
	if err := ValidateTodoAudience(t.Audience); err != nil {
		return err
	}
	if err := ValidateTodoItemType(t.ItemType); err != nil {
		return err
	}
	resp, err := responsibleParam(t.Responsible)
	if err != nil {
		return err
	}
	_, err = executor.Exec(ctx, `INSERT INTO todos (id, title, notes, done_at, created_at, updated_at, origin, plan_id, start_error, done_by,
		done_reason, reopened_at, quiet, due_at, due_date, remind_at, reminded_at, priority, project, kind, inbox_item, part_of, after_ids, require_accepted_after, responsible, audience, direction_id, item_type)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28)`,
		t.ID, t.Title, t.Notes, t.DoneAt, t.CreatedAt, t.UpdatedAt, t.Origin, t.PlanID, t.StartError, t.DoneBy, t.DoneReason, t.ReopenedAt, t.Quiet,
		t.DueAt, t.DueDate, t.RemindAt, t.RemindedAt, t.Priority, t.Project, t.Kind, t.InboxItem, t.PartOf, nonNilIDs(t.After), t.RequireAcceptedAfter, resp, t.Audience, strings.TrimSpace(t.DirectionID), nullableItemType(t.ItemType))
	if err != nil {
		return err
	}
	for i, img := range t.Images {
		if _, err := executor.Exec(ctx, `INSERT INTO todo_images (todo_id, id, position, mime, width, height, name, data)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, t.ID, img.ID, i, img.Mime, img.Width, img.Height, img.Name, img.Data); err != nil {
			return err
		}
	}
	return nil
}

// UpdateTodo updates only the fields that are non-nil.
func (d *DB) UpdateTodo(ctx context.Context, id string, title *string, notes *string, done *bool, updatedAt time.Time) (*TodoRow, error) {
	return d.EditTodo(ctx, id, TodoEdit{Title: title, Notes: notes, Done: done}, updatedAt)
}

// TodoEdit is a change to a todo by the user or the assistant on their behalf (#1683): the fields that are non-nil, and
// the due date and the reminder when SetDue / SetRemind (nil clears them). Project is a project's slug, "" for none
// (#2581); the caller has checked it names one.
type TodoEdit struct {
	Title, Notes *string
	InboxItem    *string
	Done         *bool
	// CloseGuard reads the caller's current in-memory close protections while the edit can still be rolled back.
	// It must not access the database.
	CloseGuard func() error
	// RestartWork starts a new work round when Done is false even if the task was already open.
	RestartWork bool
	SetDue      bool
	DueAt       *time.Time
	DueDate     *time.Time
	SetRemind   bool
	RemindAt    *time.Time
	Priority    *string
	Project     *string
	// Origin: the conversation (or agent) the task belongs to now; a task whose supervisor was handed to another
	// conversation moves with it (assistant/task_leader.go rehome).
	Origin *string
	// PartOf and After: the request it is a piece of ("" none) and the pieces it waits for (empty none); the caller has
	// checked them (assistant/todo_pieces.go).
	PartOf               *string
	After                *[]string
	RequireAcceptedAfter *bool
	// SetResponsible updates Responsible (nil clears it); the caller has validated it (assistant/todo_responsible.go).
	SetResponsible bool
	Responsible    *TodoResponsible
	// Audience updates Audience ("" clears it); validated against "", AudienceHuman, AudienceAutonomous (#14619).
	Audience *string
	// DirectionID updates organizational direction membership ("" clears it, trimmed of surrounding whitespace)
	// without touching origin, supervision, or todo_agents (#14660).
	DirectionID *string
	// ItemType updates explicit item_type ("" clears it to SQL NULL); validated against "", ItemTypePersonal, ItemTypeTask (#15758).
	ItemType *string
}

// Apply makes the edit to cur at the moment at: done stamps done_at (once), not done reopens it (reopened_at, no
// longer failed); done on a failed start, which the hub closed itself and which waits on the owner, is the owner closing
// it: no longer failed, done now by them (#3713). A reminder at another moment is to be pushed again. The store and the
// store-less memory both change a todo with it.
func (e TodoEdit) Apply(cur *TodoRow, at time.Time) {
	if e.InboxItem != nil {
		cur.InboxItem = *e.InboxItem
	}
	if e.Title != nil {
		cur.Title = *e.Title
	}
	if e.Notes != nil {
		cur.Notes = *e.Notes
	}
	if e.Done != nil {
		if *e.Done {
			if cur.DoneAt == nil || cur.StartError != "" {
				cur.DoneAt = &at
				cur.StartError = ""
				cur.DoneBy, cur.DoneReason = "", ""
			}
		} else {
			if cur.DoneAt != nil || e.RestartWork {
				cur.ReopenedAt = &at // the same stretch of work must not close it again
				cur.SeenAt, cur.AutoSeenUndone = nil, false
			}
			cur.DoneAt = nil
			cur.StartError = "" // reopened: no longer failed
			cur.DoneBy, cur.DoneReason = "", ""
			cur.SomedayUntil = nil // reopened: the owner is on it again, not putting it off (#10226)
		}
	}
	if e.SetDue {
		cur.DueAt, cur.DueDate = e.DueAt, e.DueDate
	}
	if e.SetRemind && !sameMoment(cur.RemindAt, e.RemindAt) {
		cur.RemindAt, cur.RemindedAt = e.RemindAt, nil
	}
	if e.Priority != nil {
		cur.Priority = *e.Priority
	}
	if e.Project != nil {
		cur.Project = *e.Project
	}
	if e.Origin != nil {
		cur.Origin = *e.Origin
	}
	if e.PartOf != nil {
		cur.PartOf = *e.PartOf
	}
	if e.After != nil {
		cur.After = nonNilIDs(*e.After)
	}
	if e.RequireAcceptedAfter != nil {
		cur.RequireAcceptedAfter = *e.RequireAcceptedAfter
	}
	if e.SetResponsible {
		if e.Responsible != nil {
			r := *e.Responsible
			cur.Responsible = &r
		} else {
			cur.Responsible = nil
		}
	}
	if e.Audience != nil {
		cur.Audience = *e.Audience
	}
	if e.DirectionID != nil {
		cur.DirectionID = strings.TrimSpace(*e.DirectionID)
	}
	if e.ItemType != nil {
		cur.ItemType = strings.TrimSpace(*e.ItemType)
	}
	cur.UpdatedAt = at
}

// nonNilIDs is ids as the after_ids column takes them: pgx sends a nil slice as NULL, which the column refuses.
func nonNilIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func sameMoment(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// EditTodo makes edit e to todo id at updatedAt (TodoEdit.Apply) and returns the row as it is now; ErrTodoNotFound when
// it is not there.
func (d *DB) EditTodo(ctx context.Context, id string, e TodoEdit, updatedAt time.Time) (*TodoRow, error) {
	var row *TodoRow
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		var err error
		row, err = EditTodoTx(ctx, tx, id, e, updatedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// EditTodoTx makes the ordinary to-do edit in a caller's transaction.
func EditTodoTx(ctx context.Context, tx pgx.Tx, id string, e TodoEdit, updatedAt time.Time) (*TodoRow, error) {
	if e.Audience != nil {
		if err := ValidateTodoAudience(*e.Audience); err != nil {
			return nil, err
		}
	}
	if e.ItemType != nil {
		if err := ValidateTodoItemType(strings.TrimSpace(*e.ItemType)); err != nil {
			return nil, err
		}
	}
	cur, err := LockTodoTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if cur.Kind == KindConcern && cur.DoneAt == nil && e.Done != nil && *e.Done {
		return nil, ErrConcernUnreported // only ReportConcern closes it
	}
	reopening := e.Done != nil && !*e.Done && (cur.DoneAt != nil || e.RestartWork)
	e.Apply(cur, updatedAt)
	resp, err := responsibleParam(cur.Responsible)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE todos SET title = $2, notes = $3, done_at = $4, updated_at = $5, start_error = $6,
		done_by = $7, done_reason = $8, reopened_at = $9, due_at = $10, due_date = $11, remind_at = $12, reminded_at = $13,
		priority = $14, project = $15, origin = $16, part_of = $17, after_ids = $18, inbox_item = $19,
		someday_until = $20, require_accepted_after = $21, responsible = $22, audience = $23, direction_id = $24, item_type = $25,
		seen_at = $26, auto_seen_undone = $27 WHERE id = $1`,
		cur.ID, cur.Title, cur.Notes, cur.DoneAt, cur.UpdatedAt, cur.StartError, cur.DoneBy, cur.DoneReason, cur.ReopenedAt,
		cur.DueAt, cur.DueDate, cur.RemindAt, cur.RemindedAt, cur.Priority, cur.Project, cur.Origin, cur.PartOf, nonNilIDs(cur.After), cur.InboxItem,
		cur.SomedayUntil, cur.RequireAcceptedAfter, resp, cur.Audience, cur.DirectionID, nullableItemType(cur.ItemType), cur.SeenAt, cur.AutoSeenUndone)
	if err != nil {
		return nil, err
	}
	if reopening {
		// The old Pass is stale as soon as reopened_at advances. Reset its ledger in the same commit.
		if _, err := tx.Exec(ctx, `UPDATE todo_supervision SET state = '', pending_since = NULL, updated_at = $2
			WHERE todo_id = $1 AND state = 'user_check' AND updated_at <= $2`, id, updatedAt); err != nil {
			return nil, err
		}
	}
	if cur.DoneAt != nil && e.Done != nil && *e.Done && e.CloseGuard != nil {
		if err := e.CloseGuard(); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// SeeTodo sets when the owner looked at todo id (#5707, docs/assistant/todo-status-sop.md "Seen"); nil clears it (Undo)
// and persists auto_seen_undone = true so auto-seen respects Undo in that completion round (#14639).
// Manual PUT /seen (at != nil) leaves auto_seen_undone untouched.
// It is not an edit: updated_at stays, so being seen never counts as activity.
func (d *DB) SeeTodo(ctx context.Context, id string, at *time.Time) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos
		SET seen_at = $2,
			auto_seen_undone = CASE WHEN $2::timestamptz IS NULL THEN true ELSE auto_seen_undone END
		WHERE id = $1 RETURNING `+todoColumns, id, at).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// AutoSeeTodo marks the specified completion round seen unless the owner undid it.
// A reopen, a different completion, or Undo makes the conditional write a no-op.
func (d *DB) AutoSeeTodo(ctx context.Context, id string, doneAt time.Time, reopenedAt *time.Time, at time.Time) (*TodoRow, bool, error) {
	var row TodoRow
	var updated bool
	err := d.Transaction(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE todos SET seen_at = $2
			WHERE id = $1 AND done_at IS NOT NULL AND NOT auto_seen_undone AND done_at = $3
				AND reopened_at IS NOT DISTINCT FROM $4 RETURNING `+todoColumns, id, at, doneAt, reopenedAt).
			Scan(todoDest(&row)...)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = getTodo(ctx, tx, id, false)
			return err
		}
		updated = err == nil
		return err
	})
	if err != nil || !updated {
		return nil, false, err
	}
	return &row, true, nil
}

// RestoreTodo records that the owner brought todo id back from Inactive at at (#10226). Like SeeTodo it is not an edit:
// updated_at stays; restored_at is activity for the to-do's status only.
func (d *DB) RestoreTodo(ctx context.Context, id string, at time.Time) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET restored_at = $2 WHERE id = $1 RETURNING `+todoColumns, id, at).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// SetTodoSomeday puts todo id off to Someday until until (SomedayNoDate: no date), or with nil takes it out of Someday
// (#10226). Only an open to-do is put off: ErrTodoCompleted when it is closed, though not for a failed start, which
// waits on the owner to retry or close it. Taking it out applies to any. Not an edit: updated_at stays.
func (d *DB) SetTodoSomeday(ctx context.Context, id string, until *time.Time) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET someday_until = $2
		WHERE id = $1 AND ($2::timestamptz IS NULL OR done_at IS NULL OR start_error <> '') RETURNING `+todoColumns, id, until).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := d.GetTodo(ctx, id); gerr != nil {
			return nil, gerr
		}
		return nil, ErrTodoCompleted
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ClaimTodoReminder marks todo id reminded at now, but only while its reminder is still the one at remindAt, not yet
// pushed and the todo not done: true when this call claimed it (the caller pushes it), false when it is gone, done,
// moved or already claimed. Claimed before the push, so a push is never made twice (a lost one is not retried).
func (d *DB) ClaimTodoReminder(ctx context.Context, id string, remindAt, now time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE todos SET reminded_at = $3
		WHERE id = $1 AND remind_at = $2 AND reminded_at IS NULL AND done_at IS NULL`, id, remindAt, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RetitleTodo replaces a todo's title with to only while it is still from: an automatic title (#2775) never writes
// over one the user gave meanwhile. False (and no error) when the title had changed or the todo is gone.
func (d *DB) RetitleTodo(ctx context.Context, id, from, to string, at time.Time) (*TodoRow, bool, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET title = $3, updated_at = $4 WHERE id = $1 AND title = $2
		RETURNING `+todoColumns, id, from, to, at).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// FailTodo ends a todo whose agent did not start: done (unless it already was) with why (#1077). A concern is left as
// it is and returned unchanged: only the report of its outcome closes it.
func (d *DB) FailTodo(ctx context.Context, id, startError string, at time.Time) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = COALESCE(done_at, $2), start_error = $3, updated_at = $2
		WHERE id = $1 AND kind <> 'concern'
		RETURNING `+todoColumns, id, at, startError).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		if cur, gerr := d.GetTodo(ctx, id); gerr == nil {
			return cur, nil // a concern: an agent of it not starting does not close it
		}
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// RetryTodo reopens a todo whose agent did not start, to start it again (#1396): not done, no error, reopened now, and
// out of Someday (the owner is on it again, #10226).
// False (and no error) when it is not failed any more: a retry that came first already reopened it.
func (d *DB) RetryTodo(ctx context.Context, id string, at time.Time) (*TodoRow, bool, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = NULL, start_error = '', done_by = '', done_reason = '',
		reopened_at = $2, updated_at = $2, someday_until = NULL, seen_at = NULL, auto_seen_undone = false WHERE id = $1 AND done_at IS NOT NULL AND start_error <> ''
		RETURNING `+todoColumns, id, at).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := d.GetTodo(ctx, id); gerr != nil {
			return nil, false, gerr
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// AutoDoneTodo marks a task done by the hub (DoneByAuto, with why) when it is still open and the user has not
// reopened it since workSince, the start of the work that was judged; false (and no error) when either changed, or it
// is a concern (never closed on its own). A final guard can roll back activity arriving during the write.
func (d *DB) AutoDoneTodo(ctx context.Context, id, reason string, at, workSince time.Time, guard func() error) (*TodoRow, bool, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	var r TodoRow
	err = tx.QueryRow(ctx, `UPDATE todos SET done_at = $2, done_by = $3, done_reason = $4, updated_at = $2
		WHERE id = $1 AND done_at IS NULL AND (reopened_at IS NULL OR reopened_at < $5) AND kind <> 'concern'
		RETURNING `+todoColumns, id, at, DoneByAuto, reason, workSince).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todos WHERE id = $1)`, id).Scan(&exists); err != nil {
			return nil, false, err
		}
		if !exists {
			return nil, false, ErrTodoNotFound
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if guard != nil {
		if err := guard(); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// DeleteTodo deletes a todo by id. Cascading deletes remove its todo_agents.
func (d *DB) DeleteTodo(ctx context.Context, id string) error {
	var deleted bool
	err := d.pool.QueryRow(ctx, `WITH removed AS (DELETE FROM todos WHERE id = $1 RETURNING id, require_accepted_after),
		remembered AS (INSERT INTO deleted_request_gates (id) SELECT id FROM removed WHERE require_accepted_after ON CONFLICT DO NOTHING)
		SELECT EXISTS (SELECT 1 FROM removed)`, id).Scan(&deleted)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrTodoNotFound
	}
	return nil
}

func (d *DB) DeletedRequestGates(ctx context.Context) (map[string]bool, error) {
	rows, err := d.pool.Query(ctx, `SELECT id FROM deleted_request_gates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gates := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		gates[id] = true
	}
	return gates, rows.Err()
}

// KeptLinks are the links through a space that its unlink, move or void keeps rather than deletes (#10941: those whose
// directory is in the health space, since they tell the to-do's history as the space's): those to Sessions of the
// space's computer, taken off the space, released and dismissed at At as UnlinkTodoAgent leaves a link, in the same
// transaction as the space's write.
type KeptLinks struct {
	Sessions []string
	At       time.Time
}

// keepTodoAgentsTx keeps todoID's links through via (machineID's space) that keep names, before the links through it
// are deleted.
func keepTodoAgentsTx(ctx context.Context, tx pgx.Tx, todoID, machineID, via string, keep KeptLinks) error {
	if len(keep.Sessions) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE todo_agents SET source = 'manual', via = '',
			released_at = COALESCE(released_at, $5), dismissed_at = COALESCE(dismissed_at, $5)
		WHERE todo_id = $1 AND machine_id = $2 AND source = 'space' AND via = $3 AND session_id = ANY($4)`,
		todoID, machineID, via, keep.Sessions, keep.At.UTC())
	return err
}

// LinkTodoSpace links a project space to a todo.
// If the space is already linked to another todo, that link is removed and its space-sourced agents are removed, but
// for those keep names (KeptLinks).
// Returns the old todo ID if one was unlinked.
func (d *DB) LinkTodoSpace(ctx context.Context, todoID, machineID, space string, linkedAt time.Time, keep KeptLinks) (string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var oldTodoID string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var targetDoneAt *time.Time
		err := tx.QueryRow(ctx, `SELECT done_at FROM todos WHERE id = $1 FOR UPDATE`, todoID).Scan(&targetDoneAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTodoNotFound
		}
		if err != nil {
			return err
		}
		if targetDoneAt != nil {
			return ErrTodoCompleted
		}

		// Check if space was linked to another todo
		var curTodoID string
		err = tx.QueryRow(ctx, `SELECT todo_id FROM todo_spaces WHERE machine_id = $1 AND space = $2 FOR UPDATE`, machineID, space).Scan(&curTodoID)
		if err == nil {
			if curTodoID != todoID {
				oldTodoID = curTodoID
				if _, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE machine_id = $1 AND space = $2`, machineID, space); err != nil {
					return err
				}
				viaKey := machineID + "/" + space
				if err := keepTodoAgentsTx(ctx, tx, oldTodoID, machineID, viaKey, keep); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE todo_id = $1 AND source = 'space' AND via = $2`, oldTodoID, viaKey); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oldTodoID, linkedAt); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Insert or update space link
		_, err = tx.Exec(ctx, `INSERT INTO todo_spaces (todo_id, machine_id, space, linked_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (machine_id, space) DO UPDATE SET
				todo_id = $1,
				linked_at = $4`,
			todoID, machineID, space, linkedAt)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if err != nil {
		return "", err
	}
	return oldTodoID, nil
}

// UnlinkTodoSpace removes a space link and physically deletes source=space agents linked via this space, but for those
// keep names (KeptLinks).
func (d *DB) UnlinkTodoSpace(ctx context.Context, todoID, machineID, space string, keep KeptLinks) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todos WHERE id = $1)`, todoID).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return ErrTodoNotFound
		}

		tag, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE todo_id = $1 AND machine_id = $2 AND space = $3`, todoID, machineID, space)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTodoNotFound
		}

		viaKey := machineID + "/" + space
		if err := keepTodoAgentsTx(ctx, tx, todoID, machineID, viaKey, keep); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE todo_id = $1 AND source = 'space' AND via = $2`, todoID, viaKey); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = now() WHERE id = $1`, todoID)
		return err
	})
}

// RenameTodoSpace migrates todo_spaces and todo_agents via when a space is renamed via hub.
// If newSpace is already linked to another todo (unique constraint conflict), the old space's
// link is voided (deleted) rather than migrating, returning (voided: true, err: nil); its holder's links keep names
// stay (KeptLinks).
func (d *DB) RenameTodoSpace(ctx context.Context, machineID, oldSpace, newSpace string, keep KeptLinks) (bool, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var voided bool
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// Check if newSpace already exists in todo_spaces for this machine.
		var existingTodoID string
		err := tx.QueryRow(ctx, `SELECT todo_id FROM todo_spaces WHERE machine_id = $1 AND space = $2 FOR UPDATE`, machineID, newSpace).Scan(&existingTodoID)
		if err == nil {
			// Conflict: newSpace is already linked to existingTodoID.
			// Void old link to avoid violating unique constraint.
			voided = true
			oldVia := machineID + "/" + oldSpace
			var holder string
			err := tx.QueryRow(ctx, `SELECT todo_id FROM todo_spaces WHERE machine_id = $1 AND space = $2 FOR UPDATE`, machineID, oldSpace).Scan(&holder)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if holder != "" {
				if err := keepTodoAgentsTx(ctx, tx, holder, machineID, oldVia, keep); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE machine_id = $1 AND space = $2`, machineID, oldSpace); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE source = 'space' AND via = $1`, oldVia); err != nil {
				return err
			}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if _, err := tx.Exec(ctx, `UPDATE todo_spaces SET space = $3 WHERE machine_id = $1 AND space = $2`, machineID, oldSpace, newSpace); err != nil {
			return err
		}
		oldVia := machineID + "/" + oldSpace
		newVia := machineID + "/" + newSpace
		if _, err := tx.Exec(ctx, `UPDATE todo_agents SET via = $2 WHERE source = 'space' AND via = $1`, oldVia, newVia); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return voided, nil
}

// lockTodoAgentMembership serializes one session's membership, then locks every affected parent in ID order.
func lockTodoAgentMembership(ctx context.Context, tx pgx.Tx, todoID, machineID, sessionID string) (*time.Time, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		jsonb_build_array('todo-agent', $1::text, $2::text)::text, 0))`, machineID, sessionID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id, done_at FROM todos
		WHERE id = $1 OR id IN (SELECT todo_id FROM todo_agents
			WHERE machine_id = $2 AND session_id = $3 AND released_at IS NULL)
		ORDER BY id FOR UPDATE`, todoID, machineID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := false
	var targetDoneAt *time.Time
	for rows.Next() {
		var id string
		var doneAt *time.Time
		if err := rows.Scan(&id, &doneAt); err != nil {
			return nil, err
		}
		if id == todoID {
			found, targetDoneAt = true, doneAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTodoNotFound
	}
	return targetDoneAt, nil
}

// AutoLinkTodoAgent links an agent automatically if not already present.
// Unlike LinkTodoAgentWithSource, it uses INSERT ... ON CONFLICT DO NOTHING so that
// any existing row (such as a dismissed link from an immediate unlink) is NOT overwritten.
// Returns (inserted bool, releasedTodoIDs []string, err error).
func (d *DB) AutoLinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	return d.autoLinkTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt, false, false)
}

// AutoLinkUnlinkedTodoAgent is AutoLinkTodoAgent for an agent that worked for no todo when the link was decided (the
// assistant's automatic links): it links nothing when the agent works for another open todo by the time it is
// written, or was linked to any todo after linkedAt, so a link by hand committed in between is never moved (#2704).
// A done todo it was on before took part only: that link is released, as a link by hand releases it (#2904).
func (d *DB) AutoLinkUnlinkedTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	return d.autoLinkTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt, true, false)
}

// AutoLinkUnlinkedTodoAgentOfDoneTask is the delayed creation-id variant for a card closed after an unknown start.
// It permits that done target while retaining every automatic-link guard: the membership lock, linkedAt decision time,
// no release of a newer manual membership, and INSERT ... ON CONFLICT DO NOTHING on the target.
func (d *DB) AutoLinkUnlinkedTodoAgentOfDoneTask(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	return d.autoLinkTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt, true, true)
}

func (d *DB) autoLinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time, onlyUnlinked, allowDone bool) (bool, []string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return false, nil, err
	}
	defer conn.Release()

	var inserted bool
	var releasedTodoIDs []string
	alreadyLinked := errors.New("todo agent link already exists")
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		targetDoneAt, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID)
		if err != nil {
			return err
		}
		if targetDoneAt != nil && !allowDone {
			return ErrTodoCompleted
		}

		// An existing or dismissed target must not release another active membership.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todo_agents
			WHERE todo_id = $1 AND machine_id = $2 AND session_id = $3)`, todoID, machineID, sessionID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}

		// Find other active links for this agent and release them. held: one on an open todo, or made after linkedAt.
		rows, err := tx.Query(ctx, `SELECT a.todo_id, t.done_at IS NULL OR a.linked_at > $4 FROM todo_agents a JOIN todos t ON t.id = a.todo_id
			WHERE a.machine_id = $1 AND a.session_id = $2 AND a.released_at IS NULL AND a.todo_id != $3`,
			machineID, sessionID, todoID, linkedAt)
		if err != nil {
			return err
		}
		heldElsewhere := false
		for rows.Next() {
			var oid string
			var held bool
			if err := rows.Scan(&oid, &held); err != nil {
				rows.Close()
				return err
			}
			releasedTodoIDs = append(releasedTodoIDs, oid)
			heldElsewhere = heldElsewhere || held
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if onlyUnlinked && heldElsewhere {
			releasedTodoIDs = nil // it works for another todo now (under the membership lock): leave it there
			return nil
		}

		if len(releasedTodoIDs) > 0 {
			_, err = tx.Exec(ctx, `UPDATE todo_agents SET released_at = $3 WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $4`,
				machineID, sessionID, linkedAt, todoID)
			if err != nil {
				return err
			}
			for _, oid := range releasedTodoIDs {
				if _, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oid, linkedAt); err != nil {
					return err
				}
			}
		}

		// Release the old active row before inserting: the partial unique index is immediate.
		tag, err := tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at)
			VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10, NULL)
			ON CONFLICT (todo_id, machine_id, session_id) DO NOTHING`,
			todoID, machineID, sessionID, linkedAt, title, cwd, agent, startError, source, via)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return alreadyLinked // Roll back any releases if an unexpected target appeared.
		}
		inserted = true
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if errors.Is(err, alreadyLinked) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return inserted, releasedTodoIDs, nil
}

// LinkTodoAgent links an agent manually to a todo.
func (d *DB) LinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError string, linkedAt time.Time) ([]string, error) {
	return d.LinkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, "manual", "", linkedAt)
}

// LinkTodoAgentWithSource links an agent with a specific source ("manual", "space", "spawned") and via.
func (d *DB) LinkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error) {
	return d.linkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, false, linkedAt)
}

// LinkTodoAgentOfDoneTask links an agent to a todo that is already done (see linkTodoAgentWithSource's allowDone):
// the agent of a task the user finished while it was still starting. Linking by hand stays refused (#2954).
func (d *DB) LinkTodoAgentOfDoneTask(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error) {
	return d.linkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, true, linkedAt)
}

// linkTodoAgentWithSource links an agent to a todo, refusing a todo that is done (ErrTodoCompleted) unless allowDone:
// a done todo keeps the agents started for it (tasks_close.go retires them), it just gets no new ones by hand.
func (d *DB) linkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, allowDone bool, linkedAt time.Time) ([]string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	var releasedTodoIDs []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		targetDoneAt, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID)
		if err != nil {
			return err
		}
		if targetDoneAt != nil && !allowDone {
			return ErrTodoCompleted
		}

		// Find other active links for this agent and release them.
		rows, err := tx.Query(ctx, `SELECT todo_id FROM todo_agents WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $3`,
			machineID, sessionID, todoID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid string
			if err := rows.Scan(&oid); err != nil {
				rows.Close()
				return err
			}
			releasedTodoIDs = append(releasedTodoIDs, oid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		if len(releasedTodoIDs) > 0 {
			_, err = tx.Exec(ctx, `UPDATE todo_agents SET released_at = $3 WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $4`,
				machineID, sessionID, linkedAt, todoID)
			if err != nil {
				return err
			}
			for _, oid := range releasedTodoIDs {
				if _, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oid, linkedAt); err != nil {
					return err
				}
			}
		}

		// Upsert link on target todo. Clears dismissed_at and sets source/via.
		_, err = tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at)
			VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10, NULL)
			ON CONFLICT (todo_id, machine_id, session_id) DO UPDATE SET
				linked_at = $4,
				released_at = NULL,
				dismissed_at = NULL,
				source = $9,
				via = $10,
				title = CASE WHEN $5 != '' THEN $5 ELSE todo_agents.title END,
				cwd = CASE WHEN $6 != '' THEN $6 ELSE todo_agents.cwd END,
				agent = CASE WHEN $7 != '' THEN $7 ELSE todo_agents.agent END,
				start_error = $8`,
			todoID, machineID, sessionID, linkedAt, title, cwd, agent, startError, source, via)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return releasedTodoIDs, nil
}

// ReleaseTodoAgent lets go of an agent still linked to a todo (released_at only: it stays in the todo's history, unlike
// UnlinkTodoAgent's dismissal). A link already let go is left as it is. A new agent taking over the todo after this one
// stopped does it (#6427).
func (d *DB) ReleaseTodoAgent(ctx context.Context, todoID, machineID, sessionID string, at time.Time) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE todo_agents SET released_at = $4
			WHERE todo_id = $1 AND machine_id = $2 AND session_id = $3 AND released_at IS NULL`,
			todoID, machineID, sessionID, at.UTC())
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, at.UTC())
		return err
	})
}

// UnlinkTodoAgent marks the agent as released and dismissed on a todo (soft unlink, #762).
func (d *DB) UnlinkTodoAgent(ctx context.Context, todoID, machineID, sessionID string) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID); err != nil {
			return err
		}

		now := time.Now().UTC()
		_, err := tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, dismissed_at)
			VALUES ($1, $2, $3, $4, $4, $4)
			ON CONFLICT (todo_id, machine_id, session_id) DO UPDATE SET
				released_at = $4,
				dismissed_at = $4`,
			todoID, machineID, sessionID, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, now)
		return err
	})
}

// UpdateTodoAgentSnapshot updates the title and start_error snapshot of an active agent link.
func (d *DB) UpdateTodoAgentSnapshot(ctx context.Context, machineID, sessionID, title, startError string) error {
	_, err := d.pool.Exec(ctx, `UPDATE todo_agents SET
		title = CASE WHEN $3 != '' THEN $3 ELSE title END,
		start_error = $4
		WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL`,
		machineID, sessionID, title, startError)
	return err
}
