package db

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Dashboards (#15017): pages an assistant conversation drafts and the owner keeps in the sidebar. A page starts as a
// draft with a preview card in the conversation; the owner's Keep on that card makes it active. The owner pins, renames,
// removes (to Recently removed, 30 days), restores and deletes pages; Agents write the contents. Every limit is checked
// in the transaction that would break it: page counts under one advisory lock, a page's writes and taps under its row
// lock, the writes of all pages together under the lock of their setting row. Lock order: the advisory lock, then page
// rows, then the setting row; whatever changes more than one page takes the advisory lock first.

const (
	AssistantPagesKeptMost             = 20
	AssistantPagesPinnedMost           = 3
	AssistantPageDraftsPerConversation = 3
	AssistantPageDraftsMost            = 10
	// AssistantPageDraftsPerDay is how many pages Agents draft in any 24 hours; declined and expired drafts count.
	AssistantPageDraftsPerDay  = 5
	AssistantPageVersionsKept  = 20
	AssistantPageWritesPerHour = 60
	// AssistantPagesWritesPerHour is the writes of all pages together in an hour.
	AssistantPagesWritesPerHour = 600
	AssistantPageTapsPerMinute  = 10
	AssistantPageTapDedupe      = 30 * time.Second
	AssistantPageDraftLife      = 7 * 24 * time.Hour
	// AssistantPageRemovedLife is how long a removed page can be restored, and how long any removed page is kept.
	AssistantPageRemovedLife  = 30 * 24 * time.Hour
	AssistantPagesRemovedMost = 100
	AssistantPageTapLife      = 90 * 24 * time.Hour
	AssistantPageTapsKept     = 200
	// AssistantPageReasonMost is how many characters an Agent's reason for removing a page has at most.
	AssistantPageReasonMost  = 200
	assistantPageViewBytes   = 64 << 10
	assistantPageTitleMost   = 40
	assistantPageWriteWindow = time.Hour

	// AssistantPagesRateKey is the setting holding the write window of all pages together.
	AssistantPagesRateKey = "assistant.pages_rate"
	assistantPagesLock    = "assistant_pages"
)

// Page states, owners, writers and why a page was removed.
const (
	AssistantPageDraft   = "draft"
	AssistantPageActive  = "active"
	AssistantPageRemoved = "removed"

	AssistantPageOwnerMain         = "main"
	AssistantPageOwnerConversation = "conversation"

	AssistantPageByAgent     = "agent"
	AssistantPageByRoutine   = "routine"
	AssistantPageByTodoAgent = "todo_agent"
	AssistantPageByOwner     = "owner"
	AssistantPageByRestore   = "restore"

	AssistantPageRemovedByOwner        = "owner"
	AssistantPageRemovedByAgent        = "agent"
	AssistantPageRemovedByConversation = "conversation_deleted"
	AssistantPageRemovedByDeclined     = "declined"
	AssistantPageRemovedByExpired      = "expired"
)

var (
	ErrAssistantPageNotFound = errors.New("assistant page not found")
	ErrAssistantPageVersion  = errors.New("assistant page version changed")
	ErrAssistantPageRemoved  = errors.New("assistant page was removed")
	ErrAssistantPageNotDraft = errors.New("assistant page is no longer a draft")
	ErrAssistantPageNotKept  = errors.New("assistant page is not kept")
	ErrAssistantPageKept     = errors.New("a kept page's title and icon are the owner's")
	ErrAssistantPagePaused   = errors.New("the owner paused this page's updates")
	ErrAssistantPageNotGone  = errors.New("only a removed page can be deleted")

	ErrAssistantPageKeptLimit          = fmt.Errorf("at most %d pages are kept", AssistantPagesKeptMost)
	ErrAssistantPagePinLimit           = fmt.Errorf("at most %d pages are pinned", AssistantPagesPinnedMost)
	ErrAssistantPageConversationDrafts = fmt.Errorf("a conversation has at most %d drafts", AssistantPageDraftsPerConversation)
	ErrAssistantPageDrafts             = fmt.Errorf("at most %d drafts wait for the owner", AssistantPageDraftsMost)
	ErrAssistantPageDailyDrafts        = fmt.Errorf("at most %d pages are drafted in 24 hours", AssistantPageDraftsPerDay)
	ErrAssistantPageRate               = fmt.Errorf("a page is written at most %d times an hour", AssistantPageWritesPerHour)
	ErrAssistantPagesRate              = fmt.Errorf("pages are written at most %d times an hour together", AssistantPagesWritesPerHour)
	ErrAssistantPageTapRate            = fmt.Errorf("a page takes at most %d taps a minute", AssistantPageTapsPerMinute)
	ErrAssistantPageTitleTaken         = errors.New("a kept page already has this title")
)

// AssistantPageTitleTakenError names the kept page that has the title already, so an Agent can update it instead.
type AssistantPageTitleTakenError struct{ ID string }

func (e *AssistantPageTitleTakenError) Error() string {
	return fmt.Sprintf("%v: %s", ErrAssistantPageTitleTaken, e.ID)
}

func (e *AssistantPageTitleTakenError) Is(target error) bool {
	return target == ErrAssistantPageTitleTaken
}

// AssistantPageTitleKey is how titles compare: NFKC, case folded, without the white space around them. It is also the
// fold for wire.AgentPageTitleRules.
func AssistantPageTitleKey(title string) string {
	return norm.NFKC.String(cases.Fold().String(norm.NFKC.String(strings.TrimSpace(title))))
}

var (
	assistantPageID       = lazyre.New(`^pg_[0-9a-f]{16}$`)
	assistantPageActionID = lazyre.New(`^pa_[0-9a-f]{16}$`)
)

// NewAssistantPageID is a new page id: pg_ and 16 lowercase hex digits.
func NewAssistantPageID() string { return "pg_" + randomHex16() }

// NewAssistantPageActionID is a new tap id: pa_ and 16 lowercase hex digits.
func NewAssistantPageActionID() string { return "pa_" + randomHex16() }

func randomHex16() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AssistantPageRow is a page. View is nil in lists (AssistantPages, RecentlyRemovedAssistantPages, ...), which never
// read the contents.
type AssistantPageRow struct {
	ID             string
	Status         string
	Title          string
	Icon           string
	IconColor      string
	OwnerKind      string
	OwnerMachine   string
	OwnerRef       string
	CreatedMachine string
	CreatedSession string
	OfferView      string
	Why            string
	View           json.RawMessage
	Version        int64
	Pinned         bool
	PinnedAt       *time.Time
	Paused         bool
	PausedBy       string
	KeptDevice     string
	KeptAt         *time.Time
	// WritesWindowStart and WritesWindowN are the page's write window: writes counted since its start.
	WritesWindowStart time.Time
	WritesWindowN     int
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ContentAt         time.Time
	RemovedAt         *time.Time
	RemovedBy         string
	// RemovedReason is why an Agent removed it (page remove), for the owner; empty otherwise.
	RemovedReason string
	// Watch is the watch definition JSON object (nil when the page has no watch).
	Watch json.RawMessage
	// Alert is the owner's alert switch and the manager's alert evaluation state.
	Alert AssistantPageAlert
	// OnOverview is whether the owner enabled this page's Overview card on the web.
	OnOverview bool
	// RoutineID is the scheduled routine bound to refresh this page ("" when none).
	RoutineID string
	// RefreshAskedAt and RefreshDevice record an owner's pending manual refresh request.
	RefreshAskedAt *time.Time
	RefreshDevice  string
}

// Listed is a removed page shown in Recently removed: one the owner had kept, removed by the owner, an Agent or with its
// conversation. Declined and expired drafts are never listed.
func (p *AssistantPageRow) Listed() bool {
	return p.Status == AssistantPageRemoved && p.KeptAt != nil && slices.Contains(listedRemovals, p.RemovedBy)
}

var listedRemovals = []string{AssistantPageRemovedByOwner, AssistantPageRemovedByAgent, AssistantPageRemovedByConversation}

// listedSQL selects the pages Recently removed lists.
const listedSQL = `status = 'removed' AND kept_at IS NOT NULL AND removed_by IN ('owner', 'agent', 'conversation_deleted')`

// AssistantPageVersionRow is one stored version of a page.
type AssistantPageVersionRow struct {
	PageID  string
	Version int64
	Title   string
	View    json.RawMessage
	By      AssistantPageWriter
	At      time.Time
}

// AssistantPageWriter is who wrote a version: Kind is agent, routine, todo_agent, owner or restore; Machine and Session
// the conversation's, Device the owner's device.
type AssistantPageWriter struct {
	Kind    string
	Machine string
	Session string
	Device  string
	Note    string
}

const assistantPageSummary = `id, status, title, icon, icon_color, owner_kind, owner_machine, owner_ref, created_machine,
	created_session, offer_view, why, version, pinned, pinned_at, paused, paused_by, kept_device, kept_at, writes_window_start,
	writes_window_n, created_at, updated_at, content_at, removed_at, removed_by, removed_reason, watch, alert, on_overview,
	routine_id, refresh_asked_at, refresh_device`

const assistantPageColumns = assistantPageSummary + `, view`

func scanAssistantPage(row pgx.Row, withView bool) (*AssistantPageRow, error) {
	var p AssistantPageRow
	var watchRaw, alertRaw []byte
	dest := []any{&p.ID, &p.Status, &p.Title, &p.Icon, &p.IconColor, &p.OwnerKind, &p.OwnerMachine, &p.OwnerRef,
		&p.CreatedMachine, &p.CreatedSession, &p.OfferView, &p.Why, &p.Version, &p.Pinned, &p.PinnedAt, &p.Paused,
		&p.PausedBy, &p.KeptDevice, &p.KeptAt, &p.WritesWindowStart, &p.WritesWindowN, &p.CreatedAt, &p.UpdatedAt, &p.ContentAt, &p.RemovedAt, &p.RemovedBy, &p.RemovedReason,
		&watchRaw, &alertRaw, &p.OnOverview, &p.RoutineID, &p.RefreshAskedAt, &p.RefreshDevice}
	if withView {
		dest = append(dest, &p.View)
	}
	err := row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAssistantPageNotFound
	}
	if err != nil {
		return nil, err
	}
	if len(watchRaw) > 0 {
		p.Watch = json.RawMessage(watchRaw)
	}
	if len(alertRaw) > 0 {
		if err := json.Unmarshal(alertRaw, &p.Alert); err != nil {
			return nil, fmt.Errorf("assistant page %s alert: %w", p.ID, err)
		}
		if p.Alert.PushedAt != nil {
			t := p.Alert.PushedAt.UTC()
			p.Alert.PushedAt = &t
		}
	}
	return &p, nil
}

func collectAssistantPages(rows pgx.Rows, err error) ([]AssistantPageRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AssistantPageRow{}
	for rows.Next() {
		p, err := scanAssistantPage(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// pagesTx runs fn in one transaction, under the pages' advisory lock when locked.
func (d *DB) pagesTx(ctx context.Context, locked bool, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if locked {
			if err := lockAssistantPages(ctx, tx); err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

func lockAssistantPages(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, assistantPagesLock)
	return err
}

// lockAssistantPage reads a page with its row locked.
func lockAssistantPage(ctx context.Context, tx pgx.Tx, id string) (*AssistantPageRow, error) {
	return scanAssistantPage(tx.QueryRow(ctx, `SELECT `+assistantPageSummary+` FROM assistant_pages WHERE id = $1 FOR UPDATE`, id), false)
}

func checkAssistantPageTitle(title string) error {
	if n := utf8.RuneCountInString(title); n < 1 || n > assistantPageTitleMost || strings.TrimSpace(title) != title {
		return fmt.Errorf("an assistant page title has 1 to %d characters without white space around them", assistantPageTitleMost)
	}
	return nil
}

func checkAssistantPageView(view json.RawMessage) error {
	if len(view) > assistantPageViewBytes {
		return fmt.Errorf("an assistant page view has at most %d bytes", assistantPageViewBytes)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(view, &object); err != nil || object == nil {
		return errors.New("an assistant page view must be a JSON object")
	}
	return nil
}

// titleTaken is the kept page other than self whose title compares equal to key.
func titleTaken(ctx context.Context, tx pgx.Tx, key, self string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id FROM assistant_pages WHERE status = 'active' AND title_key = $1 AND id <> $2`, key, self).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &AssistantPageTitleTakenError{ID: id}
}

func countPages(ctx context.Context, tx pgx.Tx, where string, args ...any) (int, error) {
	var n int
	err := tx.QueryRow(ctx, `SELECT count(*) FROM assistant_pages WHERE `+where, args...).Scan(&n)
	return n, err
}

// NewAssistantPage is a draft an Agent creates.
type NewAssistantPage struct {
	ID        string
	Title     string
	Icon      string
	IconColor string
	// OwnerKind is main (OwnerMachine and OwnerRef empty: the main conversation of the moment) or conversation (the
	// conversation's machine and public id).
	OwnerKind    string
	OwnerMachine string
	OwnerRef     string
	// CreatedMachine and CreatedSession are the drafting conversation's: its drafts are counted against it.
	CreatedMachine string
	CreatedSession string
	OfferView      string
	Why            string
	View           json.RawMessage
	// Watch is an optional watch definition JSON object (nil/empty means no watch). When Watch is set, View may be
	// empty and defaults to "{}".
	Watch json.RawMessage
	Note  string
	At    time.Time
}

// CreateAssistantPage stores a draft as version 1. It refuses when the owner keeps the most pages, when the drafting
// conversation or all conversations have the most drafts waiting, when Agents drafted the most pages in the 24 hours
// before At, when a kept page has the same title (an *AssistantPageTitleTakenError naming it), and when the pages were
// written the most times this hour.
func (d *DB) CreateAssistantPage(ctx context.Context, p NewAssistantPage) (*AssistantPageRow, error) {
	if !assistantPageID.MatchString(p.ID) || p.CreatedMachine == "" || p.CreatedSession == "" || p.At.IsZero() {
		return nil, errors.New("a new assistant page needs an id, the drafting conversation and a time")
	}
	switch {
	case p.OwnerKind == AssistantPageOwnerMain && p.OwnerMachine == "" && p.OwnerRef == "":
	case p.OwnerKind != AssistantPageOwnerMain && p.OwnerKind != "" && p.OwnerMachine != "" && p.OwnerRef != "":
	default:
		return nil, errors.New("an assistant page is the main conversation's, or another owner's with its machine and id")
	}
	if err := checkAssistantPageTitle(p.Title); err != nil {
		return nil, err
	}
	p.Watch = normalizeOptionalJSON(p.Watch)
	if len(p.Watch) > 0 {
		if err := checkAssistantPageWatch(p.Watch); err != nil {
			return nil, err
		}
		if len(p.View) == 0 {
			p.View = json.RawMessage(`{}`)
		}
	}
	if err := checkAssistantPageView(p.View); err != nil {
		return nil, err
	}
	if p.Icon == "" {
		p.Icon = "chart"
	}
	p.At = p.At.UTC().Truncate(time.Microsecond)
	var out *AssistantPageRow
	err := d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		limits := []struct {
			err   error
			most  int
			where string
			args  []any
		}{
			{ErrAssistantPageKeptLimit, AssistantPagesKeptMost, `status = 'active'`, nil},
			{ErrAssistantPageConversationDrafts, AssistantPageDraftsPerConversation,
				`status = 'draft' AND created_machine = $1 AND created_session = $2`, []any{p.CreatedMachine, p.CreatedSession}},
			{ErrAssistantPageDrafts, AssistantPageDraftsMost, `status = 'draft'`, nil},
			{ErrAssistantPageDailyDrafts, AssistantPageDraftsPerDay, `created_at > $1`, []any{p.At.Add(-24 * time.Hour)}},
		}
		for _, l := range limits {
			n, err := countPages(ctx, tx, l.where, l.args...)
			if err != nil {
				return err
			}
			if n >= l.most {
				return l.err
			}
		}
		key := AssistantPageTitleKey(p.Title)
		if err := titleTaken(ctx, tx, key, ""); err != nil {
			return err
		}
		if err := d.countPagesWrite(ctx, tx, p.At); err != nil {
			return err
		}
		row, err := scanAssistantPage(tx.QueryRow(ctx, `INSERT INTO assistant_pages (id, status, title, title_key, icon,
			icon_color, owner_kind, owner_machine, owner_ref, created_machine, created_session, offer_view, why, view, watch, version,
			writes_window_start, writes_window_n, created_at, updated_at, content_at)
			VALUES ($1, 'draft', $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, 1, $15, 1, $15, $15, $15)
			RETURNING `+assistantPageColumns, p.ID, p.Title, key, p.Icon, p.IconColor, p.OwnerKind, p.OwnerMachine,
			p.OwnerRef, p.CreatedMachine, p.CreatedSession, p.OfferView, p.Why, []byte(p.View), watchArg(p.Watch), p.At), true)
		if err != nil {
			return err
		}
		by := AssistantPageWriter{Kind: AssistantPageByAgent, Machine: p.CreatedMachine, Session: p.CreatedSession, Note: p.Note}
		if err := writeAssistantPageVersion(ctx, tx, row, by, p.At); err != nil {
			return err
		}
		out = row
		return nil
	})
	return out, err
}

// writeAssistantPageVersion stores p's version and keeps the latest AssistantPageVersionsKept.
func writeAssistantPageVersion(ctx context.Context, tx pgx.Tx, p *AssistantPageRow, by AssistantPageWriter, at time.Time) error {
	view := p.View
	if view == nil {
		if err := tx.QueryRow(ctx, `SELECT view FROM assistant_pages WHERE id = $1`, p.ID).Scan(&view); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO assistant_page_versions (page_id, version, title, view, by_kind, by_machine,
		by_session, by_device, note, at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, p.ID, p.Version, p.Title,
		[]byte(view), by.Kind, by.Machine, by.Session, by.Device, by.Note, at); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM assistant_page_versions WHERE page_id = $1 AND version <= $2`,
		p.ID, p.Version-AssistantPageVersionsKept)
	return err
}

// assistantPagesRate is the write window of all pages together (the setting AssistantPagesRateKey).
type assistantPagesRate struct {
	Start time.Time `json:"start"`
	N     int       `json:"n"`
}

// countPagesWrite counts one write of a page in the window of all pages together, under its setting row's lock.
func (d *DB) countPagesWrite(ctx context.Context, tx pgx.Tx, at time.Time) error {
	table := d.settingsTable()
	if _, err := tx.Exec(ctx, `INSERT INTO `+table+` (key, value, updated_at) VALUES ($1, '{}', now())
		ON CONFLICT (key) DO NOTHING`, AssistantPagesRateKey); err != nil {
		return err
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT value FROM `+table+` WHERE key = $1 FOR UPDATE`, AssistantPagesRateKey).Scan(&raw); err != nil {
		return err
	}
	var r assistantPagesRate
	if json.Unmarshal(raw, &r) != nil || r.Start.IsZero() || !at.Before(r.Start.Add(assistantPageWriteWindow)) {
		r = assistantPagesRate{Start: at}
	}
	if r.N >= AssistantPagesWritesPerHour {
		return ErrAssistantPagesRate
	}
	r.N++
	value, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE `+table+` SET value = $2, updated_at = now() WHERE key = $1`, AssistantPagesRateKey, value)
	return err
}

// AssistantPageUpdate replaces a page's contents (and optionally its watch).
type AssistantPageUpdate struct {
	ID       string
	Expected int64
	// View replaces the page's View; may be empty only when Watch or ClearWatch is set, in which case the existing
	// View is kept.
	View json.RawMessage
	// Watch replaces the page's watch definition when non-empty. Changing a kept page's watch display is refused
	// (ErrAssistantPageWatchDisplay); changing the watch's alert rule turns Alert.On off.
	Watch json.RawMessage
	// ClearWatch removes the watch on a draft page (refused on a kept watch page with ErrAssistantPageWatchDisplay).
	ClearWatch bool
	// Title and Icon change a draft's ("" keeps them); once kept they are the owner's.
	Title string
	Icon  string
	By    AssistantPageWriter
	At    time.Time
}

// UpdateAssistantPage writes the next version of a draft or kept page when Expected is still its version
// (ErrAssistantPageVersion otherwise), as a new version row, keeping the latest AssistantPageVersionsKept. It counts one
// write in the page's hour (ErrAssistantPageRate) and in all pages' (ErrAssistantPagesRate). A paused page takes no
// Agent's writes.
func (d *DB) UpdateAssistantPage(ctx context.Context, u AssistantPageUpdate) (*AssistantPageRow, error) {
	if u.ID == "" || u.Expected < 1 || u.Expected >= 9007199254740991 || u.At.IsZero() {
		return nil, errors.New("an assistant page update needs the page, a safe positive version and a time")
	}
	if !slices.Contains([]string{AssistantPageByAgent, AssistantPageByRoutine, AssistantPageByTodoAgent, AssistantPageByOwner, AssistantPageByRestore}, u.By.Kind) {
		return nil, fmt.Errorf("unknown assistant page writer %q", u.By.Kind)
	}
	u.Watch = normalizeOptionalJSON(u.Watch)
	if len(u.Watch) > 0 && u.ClearWatch {
		return nil, errors.New("an assistant page update cannot both set and clear watch")
	}
	if len(u.Watch) > 0 {
		if err := checkAssistantPageWatch(u.Watch); err != nil {
			return nil, err
		}
	}
	if len(u.View) > 0 || len(u.Watch) == 0 && !u.ClearWatch {
		if err := checkAssistantPageView(u.View); err != nil {
			return nil, err
		}
	}
	if u.Title != "" {
		if err := checkAssistantPageTitle(u.Title); err != nil {
			return nil, err
		}
	}
	u.At = u.At.UTC().Truncate(time.Microsecond)
	var out *AssistantPageRow
	err := d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		p, err := lockAssistantPage(ctx, tx, u.ID)
		if err != nil {
			return err
		}
		switch {
		case p.Status == AssistantPageRemoved:
			return ErrAssistantPageRemoved
		case p.Version != u.Expected:
			return ErrAssistantPageVersion
		case p.Paused && u.By.Kind != AssistantPageByOwner && u.By.Kind != AssistantPageByRestore:
			return ErrAssistantPagePaused
		case p.Status != AssistantPageDraft && (u.Title != "" && u.Title != p.Title || u.Icon != "" && u.Icon != p.Icon):
			return ErrAssistantPageKept
		}
		nextWatch := p.Watch
		var alertUpdate bool
		var alertParam any
		switch {
		case len(u.Watch) > 0:
			if p.Status != AssistantPageDraft {
				if oldDisp := watchDisplay(p.Watch); oldDisp != "" && watchDisplay(u.Watch) != oldDisp {
					return ErrAssistantPageWatchDisplay
				}
			}
			nextWatch = u.Watch
			if !watchAlertEqual(p.Watch, u.Watch) {
				nextAlert := AssistantPageAlert{On: false, Tripped: false, PushedAt: p.Alert.PushedAt}
				raw, err := marshalAlert(nextAlert)
				if err != nil {
					return err
				}
				alertUpdate, alertParam = true, raw
			}
		case u.ClearWatch:
			if p.Status != AssistantPageDraft && len(p.Watch) > 0 {
				return ErrAssistantPageWatchDisplay
			}
			nextWatch = nil
			alertUpdate, alertParam = true, nil
		}
		start, n := p.WritesWindowStart, p.WritesWindowN
		if !u.At.Before(start.Add(assistantPageWriteWindow)) {
			start, n = u.At, 0
		}
		if n >= AssistantPageWritesPerHour {
			return ErrAssistantPageRate
		}
		if err := d.countPagesWrite(ctx, tx, u.At); err != nil {
			return err
		}
		var viewParam any
		if len(u.View) > 0 {
			viewParam = []byte(u.View)
		}
		title, icon := cmp.Or(u.Title, p.Title), cmp.Or(u.Icon, p.Icon)
		row, err := scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET view = COALESCE($2, view), title = $3, title_key = $4,
			icon = $5, watch = $6, alert = CASE WHEN $7 THEN $8::jsonb ELSE alert END, version = version + 1,
			writes_window_start = $9, writes_window_n = $10, updated_at = $11, content_at = $11
			WHERE id = $1 RETURNING `+assistantPageColumns, u.ID, viewParam, title, AssistantPageTitleKey(title), icon,
			watchArg(nextWatch), alertUpdate, alertParam, start, n+1, u.At), true)
		if err != nil {
			return err
		}
		if err := writeAssistantPageVersion(ctx, tx, row, u.By, u.At); err != nil {
			return err
		}
		out = row
		return nil
	})
	return out, err
}

// AssistantPageKeep is the owner's Keep on a draft's preview card.
type AssistantPageKeep struct {
	OfferView string
	Device    string
	// Pin also pins the page, unless AssistantPagesPinnedMost are pinned: it is then kept without.
	Pin bool
	// AlertOn enables the page's alert switch in the same tap as Keep.
	AlertOn bool
	// OnOverview places the page on web Overview in the same tap as Keep.
	OnOverview bool
	At         time.Time
}

// KeepAssistantPage makes the draft offered on the card k.OfferView a kept page. It is idempotent by the card: a page
// kept already is given back with changed false. A declined or expired draft is ErrAssistantPageRemoved; the kept pages'
// limit and a kept page with the same title refuse it.
func (d *DB) KeepAssistantPage(ctx context.Context, k AssistantPageKeep) (p *AssistantPageRow, changed bool, err error) {
	if k.OfferView == "" || k.At.IsZero() {
		return nil, false, errors.New("keeping an assistant page needs its card and a time")
	}
	at := k.At.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM assistant_pages WHERE offer_view = $1`, k.OfferView).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
			return ErrAssistantPageNotFound
		} else if err != nil {
			return err
		}
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		switch old.Status {
		case AssistantPageActive:
			p = old
			return nil
		case AssistantPageRemoved:
			return ErrAssistantPageRemoved
		}
		if n, err := countPages(ctx, tx, `status = 'active'`); err != nil {
			return err
		} else if n >= AssistantPagesKeptMost {
			return ErrAssistantPageKeptLimit
		}
		if err := titleTaken(ctx, tx, AssistantPageTitleKey(old.Title), id); err != nil {
			return err
		}
		pin := false
		if k.Pin {
			n, err := countPages(ctx, tx, `status = 'active' AND pinned`)
			if err != nil {
				return err
			}
			pin = n < AssistantPagesPinnedMost
		}
		var alertUpdate bool
		var alertParam any
		if k.AlertOn {
			nextAlert := old.Alert
			nextAlert.On = true
			raw, err := marshalAlert(nextAlert)
			if err != nil {
				return err
			}
			alertUpdate, alertParam = true, raw
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET status = 'active', kept_device = $2,
			kept_at = $3, pinned = $4, pinned_at = CASE WHEN $4 THEN $3::timestamptz END, on_overview = $5,
			alert = CASE WHEN $6 THEN $7::jsonb ELSE alert END, updated_at = $3
			WHERE id = $1 RETURNING `+assistantPageSummary, id, k.Device, at, pin, k.OnOverview, alertUpdate, alertParam), false)
		changed = err == nil
		return err
	})
	return p, changed, mapTitleConflict(err)
}

// DeclineAssistantPage removes the draft offered on the card offerView ("not this one"). It stays, unlisted, until it
// is purged, so it keeps counting against the drafts of the day. Declining one declined already changes nothing; a kept
// page is ErrAssistantPageNotDraft.
func (d *DB) DeclineAssistantPage(ctx context.Context, offerView string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if offerView == "" || at.IsZero() {
		return nil, false, errors.New("declining an assistant page needs its card and a time")
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := scanAssistantPage(tx.QueryRow(ctx, `SELECT `+assistantPageSummary+` FROM assistant_pages WHERE offer_view = $1 FOR UPDATE`, offerView), false)
		if err != nil {
			return err
		}
		switch {
		case old.Status == AssistantPageRemoved && old.RemovedBy == AssistantPageRemovedByDeclined:
			p = old
			return nil
		case old.Status == AssistantPageRemoved:
			return ErrAssistantPageRemoved
		case old.Status != AssistantPageDraft:
			return ErrAssistantPageNotDraft
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET status = 'removed', removed_at = $2,
			removed_by = 'declined', updated_at = $2 WHERE id = $1 RETURNING `+assistantPageSummary, old.ID, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}

// PinAssistantPage pins or unpins a kept page; at most AssistantPagesPinnedMost are pinned.
func (d *DB) PinAssistantPage(ctx context.Context, id string, pin bool, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.Pinned == pin {
			p = old
			return nil
		}
		if pin {
			if n, err := countPages(ctx, tx, `status = 'active' AND pinned`); err != nil {
				return err
			} else if n >= AssistantPagesPinnedMost {
				return ErrAssistantPagePinLimit
			}
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET pinned = $2,
			pinned_at = CASE WHEN $2 THEN $3::timestamptz END, updated_at = $3 WHERE id = $1 RETURNING `+assistantPageSummary,
			id, pin, at), false)
		changed = err == nil
		return err
	})
	return p, changed, err
}

func (p *AssistantPageRow) kept() error {
	switch p.Status {
	case AssistantPageActive:
		return nil
	case AssistantPageRemoved:
		return ErrAssistantPageRemoved
	}
	return ErrAssistantPageNotKept
}

// RenameAssistantPage is the owner renaming a kept page: a new version with the new title, written by the owner from
// device. No other kept page may have the title. The same title changes nothing.
func (d *DB) RenameAssistantPage(ctx context.Context, id, title, device string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if err := checkAssistantPageTitle(title); err != nil {
		return nil, false, err
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := old.kept(); err != nil {
			return err
		}
		if old.Title == title {
			p = old
			return nil
		}
		key := AssistantPageTitleKey(title)
		if err := titleTaken(ctx, tx, key, id); err != nil {
			return err
		}
		row, err := scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET title = $2, title_key = $3,
			version = version + 1, updated_at = $4 WHERE id = $1 RETURNING `+assistantPageColumns, id, title, key, at), true)
		if err != nil {
			return err
		}
		if err := writeAssistantPageVersion(ctx, tx, row, AssistantPageWriter{Kind: AssistantPageByOwner, Device: device}, at); err != nil {
			return err
		}
		row.View = nil
		p, changed = row, true
		return nil
	})
	return p, changed, mapTitleConflict(err)
}

// RemoveAssistantPage moves a kept page to Recently removed: by is owner or agent. It is unpinned; restoring it within
// AssistantPageRemovedLife brings it back. Recently removed keeps the latest AssistantPagesRemovedMost: older ones are
// deleted. Removing a page removed already changes nothing; a draft is declined, not removed.
func (d *DB) RemoveAssistantPage(ctx context.Context, id, by string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	return d.RemoveAssistantPageWithReason(ctx, id, by, "", at)
}

// RemoveAssistantPageWithReason is RemoveAssistantPage with why it was removed (an Agent's page remove), at most
// AssistantPageReasonMost characters, kept until the page is restored.
func (d *DB) RemoveAssistantPageWithReason(ctx context.Context, id, by, reason string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	if by != AssistantPageRemovedByOwner && by != AssistantPageRemovedByAgent {
		return nil, false, fmt.Errorf("an assistant page is removed by the owner or an agent, not %q", by)
	}
	if utf8.RuneCountInString(reason) > AssistantPageReasonMost {
		return nil, false, fmt.Errorf("a removal's reason has at most %d characters", AssistantPageReasonMost)
	}
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		switch old.Status {
		case AssistantPageRemoved:
			p = old
			return nil
		case AssistantPageDraft:
			return ErrAssistantPageNotKept
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET status = 'removed', removed_at = $2,
			removed_by = $3, removed_reason = $4, pinned = false, pinned_at = NULL, updated_at = $2 WHERE id = $1
			RETURNING `+assistantPageSummary, id, at, by, reason), false)
		if err != nil {
			return err
		}
		changed = true
		return trimRecentlyRemoved(ctx, tx)
	})
	return p, changed, err
}

// trimRecentlyRemoved deletes the listed removed pages past the latest AssistantPagesRemovedMost.
func trimRecentlyRemoved(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `DELETE FROM assistant_pages WHERE id IN (SELECT id FROM assistant_pages WHERE `+listedSQL+`
		ORDER BY removed_at DESC, id DESC OFFSET $1)`, AssistantPagesRemovedMost)
	return err
}

// RestoreAssistantPage brings a page back from Recently removed, unpinned. A page removed with its conversation comes
// back as the main conversation's. The kept pages' limit and a kept page with the same title refuse it; a page restored
// already changes nothing; one not listed (a draft that was never kept, or one past AssistantPageRemovedLife) is
// ErrAssistantPageNotFound.
func (d *DB) RestoreAssistantPage(ctx context.Context, id string, at time.Time) (p *AssistantPageRow, changed bool, err error) {
	at = at.UTC().Truncate(time.Microsecond)
	err = d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Status == AssistantPageActive {
			p = old
			return nil
		}
		if !old.Listed() || !old.RemovedAt.After(at.Add(-AssistantPageRemovedLife)) {
			return ErrAssistantPageNotFound
		}
		if n, err := countPages(ctx, tx, `status = 'active'`); err != nil {
			return err
		} else if n >= AssistantPagesKeptMost {
			return ErrAssistantPageKeptLimit
		}
		if err := titleTaken(ctx, tx, AssistantPageTitleKey(old.Title), id); err != nil {
			return err
		}
		p, err = scanAssistantPage(tx.QueryRow(ctx, `UPDATE assistant_pages SET status = 'active', removed_at = NULL,
			removed_by = '', removed_reason = '', updated_at = $2,
			owner_kind = CASE WHEN removed_by = 'conversation_deleted' THEN 'main' ELSE owner_kind END,
			owner_machine = CASE WHEN removed_by = 'conversation_deleted' THEN '' ELSE owner_machine END,
			owner_ref = CASE WHEN removed_by = 'conversation_deleted' THEN '' ELSE owner_ref END
			WHERE id = $1 RETURNING `+assistantPageSummary, id, at), false)
		changed = err == nil
		return err
	})
	return p, changed, mapTitleConflict(err)
}

// DeleteAssistantPage deletes a removed page for good, with its versions, taps, readings and grants. Only a removed
// page can be deleted (ErrAssistantPageNotGone); a page gone already reports false.
func (d *DB) DeleteAssistantPage(ctx context.Context, id string) (bool, error) {
	deleted, _, err := d.DeleteAssistantPageWithRoutine(ctx, id)
	return deleted, err
}

// DeleteAssistantPageWithRoutine deletes a removed page for good (cascading its versions, taps, readings and grants)
// and returns whether it was deleted and the routine_id it had bound ("" if none) so the caller can delete the
// routine. Only a removed page can be deleted (ErrAssistantPageNotGone); a page gone already reports false, "".
func (d *DB) DeleteAssistantPageWithRoutine(ctx context.Context, id string) (deleted bool, routineID string, err error) {
	err = d.pagesTx(ctx, false, func(tx pgx.Tx) error {
		old, err := lockAssistantPage(ctx, tx, id)
		if errors.Is(err, ErrAssistantPageNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if old.Status != AssistantPageRemoved {
			return ErrAssistantPageNotGone
		}
		tag, err := tx.Exec(ctx, `DELETE FROM assistant_pages WHERE id = $1`, id)
		if err == nil && tag.RowsAffected() == 1 {
			deleted = true
			routineID = old.RoutineID
		}
		return err
	})
	return deleted, routineID, err
}

// ExpireAssistantDrafts removes the drafts older than AssistantPageDraftLife at now (removed_by expired: never listed in
// Recently removed) and gives them back, so their cards can say so.
func (d *DB) ExpireAssistantDrafts(ctx context.Context, now time.Time) ([]AssistantPageRow, error) {
	now = now.UTC().Truncate(time.Microsecond)
	var out []AssistantPageRow
	err := d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		var err error
		out, err = collectAssistantPages(tx.Query(ctx, `UPDATE assistant_pages SET status = 'removed', removed_at = $1,
			removed_by = 'expired', updated_at = $1 WHERE status = 'draft' AND created_at <= $2
			RETURNING `+assistantPageSummary, now, now.Add(-AssistantPageDraftLife)))
		return err
	})
	return out, err
}

// RemoveConversationAssistantPages is RemoveConversationAssistantPagesTx in a transaction of its own.
func (d *DB) RemoveConversationAssistantPages(ctx context.Context, machine, conversation string, at time.Time) ([]AssistantPageRow, error) {
	var out []AssistantPageRow
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		var err error
		out, err = RemoveConversationAssistantPagesTx(ctx, tx, machine, conversation, at)
		return err
	})
	return out, err
}

// RemoveConversationAssistantPagesTx is a deleted conversation's hand-off, in the transaction that deletes it: the
// pages it owned (owner_kind conversation, its machine and public id) and the drafts it was offering go to removed with
// removed_by conversation_deleted. Its kept pages are listed in Recently removed and come back as the main
// conversation's when restored; its drafts are never listed. It gives back the pages it removed.
func RemoveConversationAssistantPagesTx(ctx context.Context, tx pgx.Tx, machine, conversation string, at time.Time) ([]AssistantPageRow, error) {
	if machine == "" || conversation == "" || at.IsZero() {
		return nil, errors.New("a conversation's pages need its machine, its id and a time")
	}
	if err := lockAssistantPages(ctx, tx); err != nil {
		return nil, err
	}
	at = at.UTC().Truncate(time.Microsecond)
	out, err := collectAssistantPages(tx.Query(ctx, `UPDATE assistant_pages SET status = 'removed', removed_at = $3,
		removed_by = 'conversation_deleted', pinned = false, pinned_at = NULL, updated_at = $3
		WHERE status <> 'removed' AND (owner_kind = 'conversation' AND owner_machine = $1 AND owner_ref = $2
			OR status = 'draft' AND created_machine = $1 AND created_session = $2)
		RETURNING `+assistantPageSummary, machine, conversation, at))
	if err != nil || len(out) == 0 {
		return out, err
	}
	return out, trimRecentlyRemoved(ctx, tx)
}

// mapTitleConflict turns the unique index's refusal (two kept pages with one title, raced past titleTaken) into the
// error titleTaken gives.
func mapTitleConflict(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "assistant_pages_kept_title" {
		return ErrAssistantPageTitleTaken
	}
	return err
}

// AssistantPage is one page with its contents.
func (d *DB) AssistantPage(ctx context.Context, id string) (*AssistantPageRow, error) {
	return scanAssistantPage(d.pool.QueryRow(ctx, `SELECT `+assistantPageColumns+` FROM assistant_pages WHERE id = $1`, id), true)
}

// AssistantPageByOffer is the page offered on the preview card view, with its contents.
func (d *DB) AssistantPageByOffer(ctx context.Context, view string) (*AssistantPageRow, error) {
	if view == "" {
		return nil, ErrAssistantPageNotFound
	}
	return scanAssistantPage(d.pool.QueryRow(ctx, `SELECT `+assistantPageColumns+` FROM assistant_pages WHERE offer_view = $1`, view), true)
}

// AssistantPageQuery selects pages: by status (all when empty) and, when OwnerKind is set, by owner.
type AssistantPageQuery struct {
	Status       []string
	OwnerKind    string
	OwnerMachine string
	OwnerRef     string
}

// AssistantPages lists pages without their contents: pinned ones first in the order they were pinned, then the latest
// written first.
func (d *DB) AssistantPages(ctx context.Context, q AssistantPageQuery) ([]AssistantPageRow, error) {
	where, args := []string{"true"}, []any{}
	if len(q.Status) > 0 {
		args = append(args, q.Status)
		where = append(where, fmt.Sprintf("status = ANY($%d)", len(args)))
	}
	if q.OwnerKind != "" {
		args = append(args, q.OwnerKind, q.OwnerMachine, q.OwnerRef)
		where = append(where, fmt.Sprintf("owner_kind = $%d AND owner_machine = $%d AND owner_ref = $%d", len(args)-2, len(args)-1, len(args)))
	}
	return collectAssistantPages(d.pool.Query(ctx, `SELECT `+assistantPageSummary+` FROM assistant_pages WHERE `+
		strings.Join(where, " AND ")+` ORDER BY pinned DESC, pinned_at NULLS LAST, content_at DESC, id`, args...))
}

// RecentlyRemovedAssistantPages are the removed pages the owner can restore at now, the latest removed first.
func (d *DB) RecentlyRemovedAssistantPages(ctx context.Context, now time.Time) ([]AssistantPageRow, error) {
	return collectAssistantPages(d.pool.Query(ctx, `SELECT `+assistantPageSummary+` FROM assistant_pages WHERE `+listedSQL+`
		AND removed_at > $1 ORDER BY removed_at DESC, id DESC LIMIT $2`, now.Add(-AssistantPageRemovedLife), AssistantPagesRemovedMost))
}

// PurgeAssistantPages deletes the pages removed AssistantPageRemovedLife or longer before now (listed, declined and
// expired alike), with their versions, taps, readings and grants.
func (d *DB) PurgeAssistantPages(ctx context.Context, now time.Time) (int64, error) {
	n, _, err := d.PurgeAssistantPagesWithRoutines(ctx, now)
	return n, err
}

// PurgeAssistantPagesWithRoutines deletes the pages removed AssistantPageRemovedLife or longer before now (listed,
// declined and expired alike), cascading their versions, taps, readings and grants, and returns how many pages were
// deleted and the non-empty routine_ids they had bound so the caller can delete those routines.
func (d *DB) PurgeAssistantPagesWithRoutines(ctx context.Context, now time.Time) (int64, []string, error) {
	var n int64
	var routines []string
	err := d.pagesTx(ctx, true, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `DELETE FROM assistant_pages WHERE status = 'removed' AND removed_at <= $1 RETURNING routine_id`,
			now.Add(-AssistantPageRemovedLife))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rid string
			if err := rows.Scan(&rid); err != nil {
				return err
			}
			n++
			if rid != "" {
				routines = append(routines, rid)
			}
		}
		return rows.Err()
	})
	return n, routines, err
}
