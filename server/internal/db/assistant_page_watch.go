package db

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Watch pages (Dashboards phase 2, #13020): overview placement, owner alert switch and alert evaluation state,
// owner pause/resume, one-to-one routine binding, and manual refresh requests on assistant_pages.

const (
	// AssistantPageAlertCooldown is the minimum interval between alert pushes for a page.
	AssistantPageAlertCooldown = 12 * time.Hour

	AssistantPagePausedByOwner   = "owner"
	AssistantPagePausedByRestore = "restore"

	assistantPageWatchBytes = 16 << 10
)

var (
	ErrAssistantPageHasRoutine   = errors.New("assistant page already has a routine")
	ErrAssistantPageRoutineTaken = errors.New("routine is already bound to another assistant page")
	ErrAssistantPageWatchDisplay = errors.New("a kept page's watch display cannot change")
)

// AssistantPageRoutineBoundError names the routine a page already has, so an Agent can edit it instead of binding
// a second one.
type AssistantPageRoutineBoundError struct{ RoutineID string }

func (e *AssistantPageRoutineBoundError) Error() string {
	return fmt.Sprintf("%v: %s", ErrAssistantPageHasRoutine, e.RoutineID)
}

func (e *AssistantPageRoutineBoundError) Is(target error) bool {
	return target == ErrAssistantPageHasRoutine
}

// AssistantPageAlert is a watch page's alert switch (owner-controlled) and evaluation state (manager-controlled),
// stored in assistant_pages.alert as {"on", "tripped", "pushed_at"}.
type AssistantPageAlert struct {
	On       bool       `json:"on"`
	Tripped  bool       `json:"tripped"`
	PushedAt *time.Time `json:"pushed_at,omitempty"`
}

func normalizeOptionalJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return trimmed
}

func watchArg(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}

func checkAssistantPageWatch(raw json.RawMessage) error {
	if len(raw) > assistantPageWatchBytes {
		return fmt.Errorf("an assistant page watch has at most %d bytes", assistantPageWatchBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("an assistant page watch must be a JSON object")
	}
	return nil
}

func watchDisplay(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var w struct {
		Display string `json:"display"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return ""
	}
	return w.Display
}

// watchAlertEqual reports whether the "alert" rule inside two watch JSON objects is semantically equal.
func watchAlertEqual(a, b json.RawMessage) bool {
	var wa, wb struct {
		Alert any `json:"alert"`
	}
	if len(a) > 0 {
		_ = json.Unmarshal(a, &wa)
	}
	if len(b) > 0 {
		_ = json.Unmarshal(b, &wb)
	}
	return reflect.DeepEqual(wa.Alert, wb.Alert)
}

func marshalAlert(a AssistantPageAlert) ([]byte, error) {
	if a.PushedAt != nil {
		t := a.PushedAt.UTC().Truncate(time.Microsecond)
		a.PushedAt = &t
	}
	return json.Marshal(a)
}

func mapRoutineConflict(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "assistant_pages_routine" {
		return ErrAssistantPageRoutineTaken
	}
	return err
}

// SetAssistantPageOverview sets whether kept page id appears on web Overview. No-op when already equal.
func (d *DB) SetAssistantPageOverview(ctx context.Context, id string, on bool, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if id == "" || at.IsZero() {
		return nil, false, errors.New("setting overview placement needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.OnOverview == on {
			p = old
			return nil
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET on_overview = $2, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, id, on, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}

// SetAssistantPageAlert toggles the owner's alert switch (Alert.On) on a kept page while preserving Tripped and
// PushedAt. Turning the switch on while Tripped is true preserves Tripped so the page does not push until it recovers
// and trips again.
func (d *DB) SetAssistantPageAlert(ctx context.Context, id string, on bool, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if id == "" || at.IsZero() {
		return nil, false, errors.New("setting a page alert switch needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.Alert.On == on {
			p = old
			return nil
		}
		next := old.Alert
		next.On = on
		raw, err := marshalAlert(next)
		if err != nil {
			return err
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET alert = $2, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, id, raw, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}

// RecordAssistantPageAlertState atomically updates Alert.Tripped (and Alert.PushedAt when pushedAt is non-nil; nil
// preserves the previous PushedAt) under the page's row lock without changing the owner's Alert.On switch.
func (d *DB) RecordAssistantPageAlertState(ctx context.Context, id string, tripped bool, pushedAt *time.Time, at time.Time) (*AssistantPageRow, error) {
	if id == "" || at.IsZero() {
		return nil, errors.New("recording page alert state needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	var out *AssistantPageRow
	err := d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		next := old.Alert
		next.Tripped = tripped
		if pushedAt != nil {
			t := pushedAt.UTC().Truncate(time.Microsecond)
			next.PushedAt = &t
		}
		raw, err := marshalAlert(next)
		if err != nil {
			return err
		}
		out, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET alert = $2, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, id, raw, at), false)
		return err
	})
	return out, err
}

// EvaluateAssistantPageAlert atomically records tripped under the page's row lock and decides whether to send a
// page_alert push: Alert.On must be true, tripped must be true, the previous Alert.Tripped must be false, and
// Alert.PushedAt must be nil or at least cooldown (defaults to AssistantPageAlertCooldown when <= 0) before at.
// When shouldPush is true, Alert.PushedAt is recorded as at in the same transaction. Alert.On is never modified.
func (d *DB) EvaluateAssistantPageAlert(ctx context.Context, id string, tripped bool, cooldown time.Duration, at time.Time) (p *AssistantPageRow, shouldPush bool, err error) {
	if id == "" || at.IsZero() {
		return nil, false, errors.New("evaluating a page alert needs a page id and a time")
	}
	if cooldown <= 0 {
		cooldown = AssistantPageAlertCooldown
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		next := old.Alert
		next.Tripped = tripped
		if old.Alert.On && tripped && !old.Alert.Tripped && (old.Alert.PushedAt == nil || !at.Before(old.Alert.PushedAt.Add(cooldown))) {
			t := at
			next.PushedAt = &t
			shouldPush = true
		}
		raw, err := marshalAlert(next)
		if err != nil {
			return err
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET alert = $2, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, id, raw, at), false)
		return err
	})
	return p, shouldPush, err
}

// PauseAssistantPage pauses (paused_by 'owner') or resumes (paused false, paused_by ”) a kept page for the owner.
func (d *DB) PauseAssistantPage(ctx context.Context, id string, paused bool, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if id == "" || at.IsZero() {
		return nil, false, errors.New("pausing or resuming a page needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		pausedBy := ""
		if paused {
			pausedBy = AssistantPagePausedByOwner
		}
		if old.Paused == paused && old.PausedBy == pausedBy {
			p = old
			return nil
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET paused = $2, paused_by = $3, updated_at = $4
			WHERE id = $1 RETURNING `+assistantPageSummary, id, paused, pausedBy, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}

// BindAssistantPageRoutine binds routineID to a draft or kept page (one routine per page, and one page per routine).
// Re-binding the same routineID is idempotent (changed false). Binding a second routine to a page that already has one
// returns *AssistantPageRoutineBoundError (wrapping ErrAssistantPageHasRoutine); binding a routine already owned by
// another page returns ErrAssistantPageRoutineTaken. Removed pages are refused with ErrAssistantPageRemoved.
func (d *DB) BindAssistantPageRoutine(ctx context.Context, pageID, routineID string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if pageID == "" || routineID == "" || at.IsZero() {
		return nil, false, errors.New("binding a routine needs a page id, a routine id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, pageID)
		if err != nil {
			return err
		}
		if old.Status == AssistantPageRemoved {
			return ErrAssistantPageRemoved
		}
		if old.RoutineID == routineID {
			p = old
			return nil
		}
		if old.RoutineID != "" {
			return &AssistantPageRoutineBoundError{RoutineID: old.RoutineID}
		}
		var other string
		if err := tx.QueryRow(ctx, `SELECT id FROM assistant_pages WHERE routine_id = $1 AND id <> $2`, routineID, pageID).Scan(&other); err == nil {
			return ErrAssistantPageRoutineTaken
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET routine_id = $2, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, pageID, routineID, at), false)
		changed = err == nil
		return err
	})
	return p, changed, mapRoutineConflict(err)
}

// UnbindAssistantPageRoutine clears pageID's routine_id and revokes that routine's data grant in the same transaction.
// Returns the updated page row and the former routine_id ("" if none was bound).
func (d *DB) UnbindAssistantPageRoutine(ctx context.Context, pageID string, at time.Time) (p *AssistantPageRow, routineID string, err error) {
	if pageID == "" || at.IsZero() {
		return nil, "", errors.New("unbinding a routine needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, pageID)
		if err != nil {
			return err
		}
		if old.RoutineID == "" {
			p = old
			return nil
		}
		routineID = old.RoutineID
		if _, err := tx.Exec(ctx, `DELETE FROM assistant_page_grants WHERE page_id = $1 AND grantee_kind = $2 AND grantee_ref = $3`,
			pageID, AssistantPageGrantRoutine, routineID); err != nil {
			return err
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET routine_id = '', updated_at = $2
			WHERE id = $1 RETURNING `+assistantPageSummary, pageID, at), false)
		return err
	})
	return p, routineID, err
}

// UnbindAssistantPageByRoutine clears the binding and revokes the data grant for the page bound to routineID, if any.
// Used when a routine is deleted from Scheduled tasks. Returns (nil, nil) when no page is bound to routineID.
func (d *DB) UnbindAssistantPageByRoutine(ctx context.Context, routineID string, at time.Time) (*AssistantPageRow, error) {
	if routineID == "" || at.IsZero() {
		return nil, errors.New("unbinding by routine needs a routine id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	var out *AssistantPageRow
	err := d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := scanAssistantPage(tx.QueryRow(ctx, `SELECT `+assistantPageSummary+` FROM assistant_pages WHERE routine_id = $1 FOR UPDATE`, routineID), false)
		if errors.Is(err, ErrAssistantPageNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM assistant_page_grants WHERE page_id = $1 AND grantee_kind = $2 AND grantee_ref = $3`,
			old.ID, AssistantPageGrantRoutine, routineID); err != nil {
			return err
		}
		out, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET routine_id = '', updated_at = $2
			WHERE id = $1 RETURNING `+assistantPageSummary, old.ID, at), false)
		return err
	})
	return out, err
}

// AssistantPageByRoutine returns the page bound to routineID, with its contents (ErrAssistantPageNotFound if none).
func (d *DB) AssistantPageByRoutine(ctx context.Context, routineID string) (*AssistantPageRow, error) {
	if routineID == "" {
		return nil, ErrAssistantPageNotFound
	}
	return scanAssistantPage(d.pool.QueryRow(ctx, `SELECT `+assistantPageColumns+` FROM assistant_pages WHERE routine_id = $1`, routineID), true)
}

// MarkAssistantPageRefresh records that the owner requested a manual refresh of kept page id from device at at.
func (d *DB) MarkAssistantPageRefresh(ctx context.Context, id, device string, at time.Time) (*AssistantPageRow, error) {
	if id == "" || at.IsZero() {
		return nil, errors.New("marking a page refresh needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	var out *AssistantPageRow
	err := d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.Paused {
			return ErrAssistantPagePaused
		}
		out, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET refresh_asked_at = $2, refresh_device = $3,
			updated_at = $2 WHERE id = $1 RETURNING `+assistantPageSummary, id, at, device), false)
		return err
	})
	return out, err
}

// ClearAssistantPageRefresh clears any pending manual refresh request on page id. Returns changed false when none was set.
func (d *DB) ClearAssistantPageRefresh(ctx context.Context, id string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if id == "" || at.IsZero() {
		return nil, false, errors.New("clearing a page refresh needs a page id and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.RefreshAskedAt == nil && old.RefreshDevice == "" {
			p = old
			return nil
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET refresh_asked_at = NULL, refresh_device = '',
			updated_at = $2 WHERE id = $1 RETURNING `+assistantPageSummary, id, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}
