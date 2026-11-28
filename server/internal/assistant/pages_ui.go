package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Dashboards (#15017): the pages an assistant conversation keeps up to date for the owner, drawn where extensions'
// pages are (extmgr.Pages, under the reserved extension id "-pages"): the index (ui "index": Pinned, All, Recently
// removed) and each kept page (ui "pg_…"). A page is a live card as its conversation wrote it, held to the page floor
// (wire.ParseAgentPage); the hub draws it with a frame of its own the model cannot change: the page's title as the
// View's (the apps' header), a footer saying when it was updated and where the owner's last tap is, and the owner's
// actions (persona.pin, persona.unpin, persona.rename, persona.remove, persona.restore:<id>, persona.delete:<id>,
// persona.retry:<tap>). A tap on the page's own buttons goes back to its conversation (pages_actions.go). Only the
// owner's devices with full access reach any of it (extmgr checks), only while Settings › Assistant has it on
// (`dashboards`), only on the built-in manager.

const (
	pagesIndex = "index"
	// pageAction starts the hub's own actions (wire.AgentPageActionPrefix): a page never has one of its own.
	pageAction = wire.AgentPageActionPrefix
)

var pageIDPattern = lazyre.New(`^pg_[0-9a-f]{16}$`)

// pageStore is the store's part the pages need (internal/db assistant_pages.go, assistant_page_actions.go).
type pageStore interface {
	AssistantPage(context.Context, string) (*db.AssistantPageRow, error)
	AssistantPages(context.Context, db.AssistantPageQuery) ([]db.AssistantPageRow, error)
	RecentlyRemovedAssistantPages(context.Context, time.Time) ([]db.AssistantPageRow, error)
	PinAssistantPage(context.Context, string, bool, time.Time) (*db.AssistantPageRow, bool, error)
	RenameAssistantPage(context.Context, string, string, string, time.Time) (*db.AssistantPageRow, bool, error)
	RemoveAssistantPage(context.Context, string, string, time.Time) (*db.AssistantPageRow, bool, error)
	RestoreAssistantPage(context.Context, string, time.Time) (*db.AssistantPageRow, bool, error)
	AssistantPageVersionSummaries(context.Context, string) ([]db.AssistantPageVersionRow, error)
	AssistantPageVersion(context.Context, string, int64) (*db.AssistantPageVersionRow, error)
	RestoreAssistantPageVersion(context.Context, string, int64, int64, string, time.Time) (*db.AssistantPageRow, error)
	DeleteAssistantPage(context.Context, string) (bool, error)
	BeginAssistantPageAction(context.Context, db.AssistantPageActionRow) (*db.AssistantPageActionRow, bool, error)
	AssistantPageAction(context.Context, string) (*db.AssistantPageActionRow, error)
	AssistantPageActions(context.Context, string, int) ([]db.AssistantPageActionRow, error)
	PendingAssistantPageActions(context.Context, int) ([]db.AssistantPageActionRow, error)
	ClaimAssistantPageDelivery(context.Context, string, time.Time) (*db.AssistantPageActionRow, error)
	SettleAssistantPageDelivery(context.Context, db.AssistantPageDelivery) (*db.AssistantPageActionRow, error)
	RetryAssistantPageAction(context.Context, string) (*db.AssistantPageActionRow, error)
	MarkAssistantPageTurnStarted(context.Context, string, string, time.Time) ([]db.AssistantPageActionRow, error)
	MarkAssistantPageTurnEnded(context.Context, string, string, time.Time) ([]db.AssistantPageActionRow, error)
	AssistantPageTurnTargets(context.Context) ([]db.AssistantPageTurnTarget, error)
	AssistantPageTapDays(context.Context, time.Time, string) ([]db.AssistantPageTapDay, error)
	ExpireAssistantDrafts(context.Context, time.Time) ([]db.AssistantPageRow, error)
	// the page tool's and the preview card's (pages_tool.go)
	CreateAssistantPage(context.Context, db.NewAssistantPage) (*db.AssistantPageRow, error)
	UpdateAssistantPage(context.Context, db.AssistantPageUpdate) (*db.AssistantPageRow, error)
	AssistantPageByOffer(context.Context, string) (*db.AssistantPageRow, error)
	KeepAssistantPage(context.Context, db.AssistantPageKeep) (*db.AssistantPageRow, bool, error)
	DeclineAssistantPage(context.Context, string, time.Time) (*db.AssistantPageRow, bool, error)
	RemoveAssistantPageWithReason(context.Context, string, string, string, time.Time) (*db.AssistantPageRow, bool, error)
	PurgeAssistantPages(context.Context, time.Time) (int64, error)
	TrimAssistantPageActions(context.Context, time.Time) (int64, error)
	// Dashboards phase 2: watch pages, readings, grants and routine binding (#13020)
	SetAssistantPageOverview(context.Context, string, bool, time.Time) (*db.AssistantPageRow, bool, error)
	SetAssistantPageAlert(context.Context, string, bool, time.Time) (*db.AssistantPageRow, bool, error)
	RecordAssistantPageAlertState(context.Context, string, bool, *time.Time, time.Time) (*db.AssistantPageRow, error)
	EvaluateAssistantPageAlert(context.Context, string, bool, time.Duration, time.Time) (*db.AssistantPageRow, bool, error)
	PauseAssistantPage(context.Context, string, bool, time.Time) (*db.AssistantPageRow, bool, error)
	BindAssistantPageRoutine(context.Context, string, string, time.Time) (*db.AssistantPageRow, bool, error)
	UnbindAssistantPageRoutine(context.Context, string, time.Time) (*db.AssistantPageRow, string, error)
	UnbindAssistantPageByRoutine(context.Context, string, time.Time) (*db.AssistantPageRow, error)
	AssistantPageByRoutine(context.Context, string) (*db.AssistantPageRow, error)
	MarkAssistantPageRefresh(context.Context, string, string, time.Time) (*db.AssistantPageRow, error)
	ClearAssistantPageRefresh(context.Context, string, time.Time) (*db.AssistantPageRow, bool, error)
	AddAssistantPageReading(context.Context, db.NewAssistantPageReading) (*db.AssistantPageRow, *db.AssistantPageReadingRow, error)
	AssistantPageReadings(context.Context, string, int) ([]db.AssistantPageReadingRow, error)
	AssistantPagesReadings(context.Context, []string, int) (map[string][]db.AssistantPageReadingRow, error)
	GrantAssistantPageData(context.Context, db.AssistantPageGrantRow) (*db.AssistantPageGrantRow, error)
	RevokeAssistantPageData(context.Context, string, string, string) (bool, error)
	HasAssistantPageDataGrant(context.Context, string, string, string) (bool, error)
	AssistantPageGrants(context.Context, string) ([]db.AssistantPageGrantRow, error)
	DeleteAssistantPageWithRoutine(context.Context, string) (bool, string, error)
	PurgeAssistantPagesWithRoutines(context.Context, time.Time) (int64, []string, error)
}

func (s *Service) pageStore() (pageStore, bool) {
	st, ok := s.store().(pageStore)
	return st, ok
}

// pagesManager: this manager is the one with pages, the built-in one (docs/assistant/managers.md): the hub relays the
// extensions' routes to the default manager, and only the built-in manager wires the hook (main.go).
func (s *Service) pagesManager() bool { return s.id == "" || s.id == assistantwire.AssistantID }

// withPages adds the pages' capability to the hello of manager id when it has pages: the built-in one, with a store
// that keeps them, the switch on or off (the apps show the switch only while the hub lists the feature).
func withPages(caps []string, id string, svc *Service) []string {
	if id != assistantwire.AssistantID {
		return caps
	}
	if svc != nil {
		if _, ok := svc.pageStore(); !ok {
			return caps
		}
	}
	return append(caps, assistantwire.CapabilityAssistantPages)
}

// dashboardsOn: the owner turned Dashboards on (Settings › Assistant, `dashboards`; off until then).
func (s *Service) dashboardsOn(ctx context.Context) bool {
	on := s.settings(ctx).Dashboards
	return on != nil && *on && s.pagesManager()
}

// pagesHook is the pages as extmgr draws them; svc is read when it is called (the extensions start before the
// assistant is made, main.go).
type pagesHook struct{ svc func() *Service }

func (h pagesHook) Places(ctx context.Context, l i18n.Lang, placement string, overview bool) []extmgr.UIPlace {
	s := h.svc()
	if s == nil {
		return nil
	}
	return s.pagePlacesFor(ctx, l, placement, overview)
}

func (h pagesHook) UI(ctx context.Context, l i18n.Lang, ui string, in wire.UIRequest, device string) (extension.ActionResult, error) {
	s := h.svc()
	if s == nil {
		return extension.ActionResult{}, pagesRefusal(http.StatusServiceUnavailable, "unavailable", "unavailable")
	}
	return s.pageUI(ctx, l, ui, in, device)
}

func pagesRefusal(status int, code, key string) error {
	return &extmgr.PagesError{Status: status, Code: code, Err: i18n.Err("assistant-pages." + key)}
}

// pagePlaces are the sidebar's entries: the index while a page is kept or one waits in Recently removed, then each
// pinned page; nothing while Dashboards are off. Never a badge (a page is no count of the app's).
func (s *Service) pagePlaces(ctx context.Context, l i18n.Lang) []extmgr.UIPlace {
	return s.pagePlacesFor(ctx, l, "", false)
}

// pagePlacesFor returns the -pages places for placement, including overview_card entries (`g_<hex>`) for kept watch
// pages with on_overview only when overview is true (`?pages_overview=1`) and placement is empty or `overview_card`.
func (s *Service) pagePlacesFor(ctx context.Context, l i18n.Lang, placement string, overview bool) []extmgr.UIPlace {
	st, ok := s.pageStore()
	if !ok || !s.dashboardsOn(ctx) {
		return nil
	}
	wantNav := placement == "" || extension.FullPage(placement)
	wantOverview := overview && (placement == "" || placement == "overview_card")
	if !wantNav && !wantOverview {
		return nil
	}
	kept, err := st.AssistantPages(ctx, db.AssistantPageQuery{Status: []string{db.AssistantPageActive}})
	if err != nil {
		s.logf("assistant pages: places: %v", err)
		return nil
	}
	var out []extmgr.UIPlace
	if wantNav {
		showIndex := len(kept) > 0
		if !showIndex {
			removed, err := st.RecentlyRemovedAssistantPages(ctx, time.Now())
			showIndex = err == nil && len(removed) > 0
		}
		if showIndex {
			out = append(out, pagePlace(pagesIndex, i18n.T(l, "assistant-pages.dashboards"), wire.AgentPageIconDefault, ""))
			pinned := 0
			for _, p := range kept {
				if p.Pinned && pinned < db.AssistantPagesPinnedMost {
					out = append(out, pagePlace(p.ID, p.Title, p.Icon, p.IconColor))
					pinned++
				}
			}
		}
	}
	if wantOverview {
		for _, p := range kept {
			if len(p.Watch) > 0 && p.OnOverview {
				out = append(out, pageOverviewPlace(p))
			}
		}
	}
	return out
}

func pagePlace(id, title, icon, color string) extmgr.UIPlace {
	return extmgr.UIPlace{Extension: extmgr.PagesExtension, ExtensionName: "Persona", ID: id, Title: title, Placement: "nav_page",
		Size: extension.SizeFull, Icon: icon, IconColor: color}
}

// pageState is what a drawn page keeps in View.state, which the apps send back with every request: the detail page
// it draws (p, "" the page itself), the page's version it was drawn from (v: a confirmed action on another one is
// confirmed again) and the owner's form. Rev identifies the snapshot displayed in version history.
type pageState struct {
	P    string `json:"p"`
	V    int64  `json:"v"`
	Form string `json:"form,omitempty"`
	Rev  int64  `json:"rev,omitempty"`
}

func readPageState(raw string) pageState {
	var st pageState
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
}

func (st pageState) encode() string {
	raw, _ := json.Marshal(st)
	return string(raw)
}

// pageRequest is one request to the pages.
type pageRequest struct {
	s        *Service
	st       pageStore
	l        i18n.Lang
	version  int
	platform string
	device   string
	in       wire.UIRequest
	state    pageState
	now      time.Time
	loc      *time.Location
}

// pageUI answers a request to draw ui, or an action on it.
func (s *Service) pageUI(ctx context.Context, l i18n.Lang, ui string, in wire.UIRequest, device string) (extension.ActionResult, error) {
	if !s.dashboardsOn(ctx) {
		return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "off")
	}
	st, ok := s.pageStore()
	if !ok {
		return extension.ActionResult{}, pagesRefusal(http.StatusServiceUnavailable, "unavailable", "unavailable")
	}
	version := 1
	platform := ""
	if in.Context != nil {
		version = in.Context.UIVersion
		platform = in.Context.Platform
	}
	now := time.Now()
	s.logPageRefreshes(ctx, now)
	r := &pageRequest{s: s, st: st, l: l.Or(s.lang(ctx)), version: min(max(version, 1), extension.UIVersion), platform: platform, device: device, in: in,
		state: readPageState(in.State), now: now, loc: s.location(ctx)}
	switch {
	case ui == pagesIndex:
		return r.index(ctx)
	case pageIDPattern.MatchString(ui):
		return r.page(ctx, ui)
	case overviewIDPattern.MatchString(ui):
		return r.overviewCard(ctx, ui)
	}
	return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "not_found")
}

// splitPageAction parts "persona.restore:<id>" into the action and what it names.
func splitPageAction(action string) (string, string) {
	if name, arg, ok := strings.Cut(action, ":"); ok && strings.HasPrefix(name, pageAction) {
		return name, arg
	}
	return action, ""
}

// ownAction says whether action is one of the hub's (any case: a page's own never starts with it).
func ownAction(action string) bool {
	return len(action) >= len(pageAction) && strings.EqualFold(action[:len(pageAction)], pageAction)
}

func (r *pageRequest) index(ctx context.Context) (extension.ActionResult, error) {
	action, arg := splitPageAction(r.in.Action)
	switch action {
	case "":
		return r.indexResult(ctx, nil, "")
	case "persona.open": // an app without pushes (before UI version 3) opens a row by its action
		return r.page(ctx, arg)
	case "persona.pin", "persona.unpin", "persona.remove":
		return r.ownerAction(ctx, pagesIndex, action, r.in.Values["page"])
	case "persona.restore", "persona.delete":
		return r.ownerAction(ctx, pagesIndex, action, arg)
	}
	return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
}

// indexResult draws the index, with a line to undo removing undo, and toast.
func (r *pageRequest) indexResult(ctx context.Context, undo *db.AssistantPageRow, toast string) (extension.ActionResult, error) {
	kept, err := r.st.AssistantPages(ctx, db.AssistantPageQuery{Status: []string{db.AssistantPageActive}})
	if err != nil {
		return extension.ActionResult{}, err
	}
	removed, err := r.st.RecentlyRemovedAssistantPages(ctx, r.now)
	if err != nil {
		return extension.ActionResult{}, err
	}
	var readingIDs []string
	for _, p := range kept {
		if len(p.Watch) > 0 || p.RoutineID != "" {
			readingIDs = append(readingIDs, p.ID)
		}
	}
	var readingsByPage map[string][]db.AssistantPageReadingRow
	if len(readingIDs) > 0 {
		readingsByPage, err = r.st.AssistantPagesReadings(ctx, readingIDs, db.AssistantPageReadingsKept)
		if err != nil {
			return extension.ActionResult{}, err
		}
	}
	v := drawPagesIndexWith(kept, removed, undo, readingsByPage, r.l, r.loc, r.now)
	if err := r.fit(&v); err != nil {
		return extension.ActionResult{}, err
	}
	return extension.ActionResult{View: &v, Toast: toast}, nil
}

func (r *pageRequest) fit(v *wire.View) error {
	return r.fitSlot(v, "nav_page")
}

func (r *pageRequest) fitSlot(v *wire.View, slotName string) error {
	slot, _ := extension.Slot(slotName)
	_, err := extension.Fit(v, slot, r.version, r.l)
	return err
}

func (r *pageRequest) page(ctx context.Context, id string) (extension.ActionResult, error) {
	p, err := r.st.AssistantPage(ctx, id)
	if errors.Is(err, db.ErrAssistantPageNotFound) || err == nil && (p.Status == db.AssistantPageDraft || p.Status == db.AssistantPageRemoved && !p.Listed()) {
		return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "not_found")
	}
	if err != nil {
		return extension.ActionResult{}, err
	}
	action, arg := splitPageAction(r.in.Action)
	if p.Status == db.AssistantPageRemoved {
		switch action {
		case "persona.restore", "persona.delete":
			return r.ownerAction(ctx, id, action, arg)
		}
		v := drawGonePage(p, r.l)
		if err := r.fit(&v); err != nil {
			return extension.ActionResult{}, err
		}
		return extension.ActionResult{View: &v}, nil
	}
	if r.state.Form == "history" || r.state.Form == "version" || action == "persona.history" {
		return r.historyAction(ctx, p, action, arg)
	}
	switch action {
	case "", "persona.open":
		return r.pageResult(ctx, p, r.state.P, "")
	case "persona.pin", "persona.unpin", "persona.remove", "persona.rename":
		return r.ownerAction(ctx, id, action, id)
	case "persona.restore", "persona.delete":
		return r.ownerAction(ctx, id, action, arg)
	case "persona.retry":
		return r.retry(ctx, p, arg)
	case "persona.refresh", "persona.pause", "persona.resume", "persona.alert", "persona.overview":
		return r.pageWatchAction(ctx, p, action)
	}
	if ownAction(action) {
		return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
	}
	return r.tap(ctx, p)
}

// pageResult draws page p (its detail page sub, or the owner's form the state asks for) with toast.
func (r *pageRequest) pageResult(ctx context.Context, p *db.AssistantPageRow, sub, toast string) (extension.ActionResult, error) {
	var v wire.View
	if r.state.Form == "rename" && r.in.Action == "" {
		v = drawRenameForm(p, r.l)
	} else {
		taps, err := r.st.AssistantPageActions(ctx, p.ID, 1)
		if err != nil {
			return extension.ActionResult{}, err
		}
		var last *db.AssistantPageActionRow
		if len(taps) > 0 {
			last = &taps[0]
		}
		extra := pageDrawExtra{platform: r.platform}
		if len(p.Watch) > 0 || p.RoutineID != "" {
			extra.readings, err = r.st.AssistantPageReadings(ctx, p.ID, db.AssistantPageReadingsKept)
			if err != nil {
				return extension.ActionResult{}, err
			}
		}
		if p.RoutineID != "" {
			if t, ok := r.s.taskView(ctx, p.RoutineID); ok {
				extra.routine = &t
			}
			extra.refresh = r.s.pageRefreshStatusFor(p, extra.readings, extra.routine, r.now)
		}
		v = drawPageWith(p, sub, last, extra, r.l, r.loc, r.now, r.s.webBase(ctx))
	}
	if err := r.fit(&v); err != nil {
		r.s.logf("assistant pages: %s does not fit UI version %d: %v", p.ID, r.version, err)
		v = drawBrokenPage(p, r.l)
		if err := r.fit(&v); err != nil {
			return extension.ActionResult{}, err
		}
	}
	return extension.ActionResult{View: &v, Toast: toast}, nil
}

// ownerAction is one of the owner's own actions on page id, asked from ui (the index or a page): pin, unpin, rename,
// remove (to Recently removed, with a line on the index to undo it), restore and delete (for good).
func (r *pageRequest) ownerAction(ctx context.Context, from, action, id string) (extension.ActionResult, error) {
	if !pageIDPattern.MatchString(id) {
		return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "not_found")
	}
	toast := func(key string) string { return i18n.T(r.l, "assistant-pages."+key) }
	again := func(t string) (extension.ActionResult, error) { // the page or the index as it is now
		if from == pagesIndex {
			return r.indexResult(ctx, nil, t)
		}
		p, err := r.st.AssistantPage(ctx, from)
		if err != nil {
			return r.indexResult(ctx, nil, t)
		}
		if p.Status != db.AssistantPageActive {
			v := drawGonePage(p, r.l)
			if err := r.fit(&v); err != nil {
				return extension.ActionResult{}, err
			}
			return extension.ActionResult{View: &v, Toast: t}, nil
		}
		r.state.Form = ""
		return r.pageResult(ctx, p, r.state.P, t)
	}
	var (
		p       *db.AssistantPageRow
		changed bool
		err     error
	)
	switch action {
	case "persona.pin", "persona.unpin":
		pin := action == "persona.pin"
		p, changed, err = r.st.PinAssistantPage(ctx, id, pin, r.now)
		if errors.Is(err, db.ErrAssistantPagePinLimit) {
			return again(toast("pin_limit"))
		}
		if err == nil && pin {
			if err := r.s.showPagesInSidebar(ctx, id); err != nil {
				r.s.logf("assistant pages: sidebar keys for %s: %v", id, err)
			}
		}
		if err == nil {
			r.s.pagesTold(ctx, id, changed)
			if pin {
				return again(toast("pinned_toast"))
			}
			return again(toast("unpinned_toast"))
		}
	case "persona.rename":
		title, bad := pageOwnerTitle(r.in.Values["title"])
		if bad != "" {
			return extension.ActionResult{Toast: toast(bad)}, nil // the form stays, with what was typed
		}
		p, changed, err = r.st.RenameAssistantPage(ctx, id, title, r.device, r.now)
		if errors.Is(err, db.ErrAssistantPageTitleTaken) {
			return extension.ActionResult{Toast: toast("title_taken")}, nil
		}
		if err == nil {
			r.s.pagesTold(ctx, id, changed)
			if changed && len(p.Watch) > 0 && p.OnOverview {
				r.s.pageChanged(overviewCardID(id), pageContent)
			}
			if r.version >= 3 {
				return extension.ActionResult{Pop: true, Toast: toast("renamed")}, nil
			}
			r.state = pageState{}
			return r.pageResult(ctx, p, "", toast("renamed"))
		}
	case "persona.remove":
		p, changed, err = r.st.RemoveAssistantPage(ctx, id, db.AssistantPageRemovedByOwner, r.now)
		if err == nil {
			if changed {
				r.s.cancelPageRefresh(id)
				_, _, _ = r.st.ClearAssistantPageRefresh(ctx, id, r.now)
				r.s.pageRoutineRemoved(ctx, id, p.RoutineID)
				if len(p.Watch) > 0 && p.OnOverview {
					r.s.pageChanged(overviewCardID(id), pageContent)
				}
			}
			r.s.pagesTold(ctx, id, changed)
			return r.indexResult(ctx, p, toast("removed_toast"))
		}
	case "persona.restore":
		p, changed, err = r.st.RestoreAssistantPage(ctx, id, r.now)
		switch {
		case errors.Is(err, db.ErrAssistantPageKeptLimit):
			return again(toast("kept_limit"))
		case errors.Is(err, db.ErrAssistantPageTitleTaken):
			return again(toast("title_taken"))
		case err == nil:
			if changed {
				r.s.pageRoutineRestored(ctx, id, p.RoutineID)
				if len(p.Watch) > 0 && p.OnOverview {
					r.s.pageChanged(overviewCardID(id), pageContent)
				}
			}
			r.s.pagesTold(ctx, id, changed)
			return again(toast("restored"))
		}
	case "persona.delete":
		var routine string
		changed, routine, err = r.st.DeleteAssistantPageWithRoutine(ctx, id)
		if err == nil {
			if changed {
				r.s.cancelPageRefresh(id)
				r.s.pageRoutineDeleted(ctx, id, routine)
				r.s.pageChanged(overviewCardID(id), pageContent)
			}
			r.s.pagesTold(ctx, id, changed)
			return r.indexResult(ctx, nil, toast("deleted"))
		}
	}
	switch {
	case errors.Is(err, db.ErrAssistantPageNotFound), errors.Is(err, db.ErrAssistantPageRemoved), errors.Is(err, db.ErrAssistantPageNotKept):
		return again(toast("not_found"))
	case errors.Is(err, db.ErrAssistantPageNotGone):
		return again(toast("bad_action"))
	}
	return extension.ActionResult{}, err
}

// pagesTold tells the apps a change of page id's place, when there was one.
func (s *Service) pagesTold(ctx context.Context, id string, changed bool) {
	if changed {
		s.pagesListChanged(ctx)
		s.pageChanged(id, pageContent)
	}
}

// pageOwnerTitle is the owner's new title for a page, or the word that says why it is not one: the characters a
// title may have, and none of the app's own places' names (the owner may use one inside a longer title).
func pageOwnerTitle(raw string) (string, string) {
	title, err := wire.CheckAgentPageTitle(raw)
	if err != nil {
		return "", "bad_title"
	}
	if _, err := pageTitleRules(true).Check(title); err != nil {
		return "", "title_reserved"
	}
	return title, ""
}

// reservedPlaces are the keys (assistant-pages.json reserved_*) of the names of the app's own places, as the three
// apps' sidebars name them (scripts/test/test_dashboards_reserved_names.py keeps them together).
var reservedPlaces = []string{"agents", "agents_2", "assistant", "computers", "dashboards", "health", "history", "inbox", "inbox_2",
	"knowledge", "machines", "main_chat", "main_conversation", "meetings", "messages", "needs_you", "overview", "persona",
	"scheduled_tasks", "settings", "starred", "starred_messages", "todos", "workflow_board"}

// reservedPageNames are the app's own places' names in every language the hub speaks.
func reservedPageNames() []string {
	var out []string
	for _, l := range i18n.Langs {
		for _, key := range reservedPlaces {
			if name := i18n.T(l, "assistant-pages.reserved_"+key); !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// pageTitleRules are a page title's rules: no name of the app's own places in it (an Agent's), or as it (the owner's),
// compared as kept titles are (db.AssistantPageTitleKey).
func pageTitleRules(owner bool) wire.AgentPageTitleRules {
	return wire.AgentPageTitleRules{Reserved: reservedPageNames(), Owner: owner, Fold: db.AssistantPageTitleKey}
}

// ---- drawing ----

// pageTime is when something happened as a page says it: today, yesterday or a day, in the owner's zone.
func pageTime(l i18n.Lang, loc *time.Location, now, at time.Time, key string) string {
	at, now = at.In(loc), now.In(loc)
	clock := fmt.Sprintf("%d:%02d", at.Hour(), at.Minute())
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	switch {
	case day(at) == day(now):
		return i18n.T(l, "assistant-pages."+key+"_today", i18n.Args{"time": clock})
	case day(at) == day(now.AddDate(0, 0, -1)):
		return i18n.T(l, "assistant-pages."+key+"_yesterday", i18n.Args{"time": clock})
	}
	return i18n.T(l, "assistant-pages."+key+"_on", i18n.Args{"date": pageDate(l, at)})
}

// pageDate is a day as the language writes it (Nov 20, 11月20日).
func pageDate(l i18n.Lang, at time.Time) string {
	month := i18n.T(l, fmt.Sprintf("assistant-pages.month_%d", int(at.Month())))
	return i18n.T(l, "assistant-pages.date", i18n.Args{"month": month, "day": at.Day()})
}

func pageWord(l i18n.Lang, key string) string { return i18n.T(l, "assistant-pages."+key) }

// drawPagesIndex is the index: the pinned pages, the others, and Recently removed, with the line to undo removing undo
// on top; with no page kept, an example of what to ask for.
func drawPagesIndex(kept, removed []db.AssistantPageRow, undo *db.AssistantPageRow, l i18n.Lang, loc *time.Location, now time.Time) wire.View {
	return drawPagesIndexWith(kept, removed, undo, nil, l, loc, now)
}

func drawPagesIndexWith(kept, removed []db.AssistantPageRow, undo *db.AssistantPageRow, readings map[string][]db.AssistantPageReadingRow, l i18n.Lang, loc *time.Location, now time.Time) wire.View {
	v := wire.View{Title: pageWord(l, "dashboards"), Sections: []wire.Section{}}
	if undo != nil {
		v.Sections = append(v.Sections, wire.Section{Key: "persona.undo", Items: []wire.Component{
			{Type: wire.ComponentText, Key: "persona.removed", Text: i18n.T(l, "assistant-pages.removed_line", i18n.Args{"title": undo.Title})},
			{Type: wire.ComponentButton, Key: "persona.undo", Title: pageWord(l, "undo"), Action: &wire.Action{ID: "persona.restore:" + undo.ID}},
		}})
	}
	var pinned, rest []wire.Component
	for _, p := range kept {
		row := keptPageRowWith(p, readings[p.ID], l, loc, now)
		if p.Pinned {
			pinned = append(pinned, row)
		} else {
			rest = append(rest, row)
		}
	}
	if len(pinned) > 0 {
		v.Sections = append(v.Sections, wire.Section{Key: "pinned", Title: pageWord(l, "pinned"), Items: pinned})
	}
	if len(rest) > 0 {
		v.Sections = append(v.Sections, wire.Section{Key: "all", Title: pageWord(l, "all"), Items: rest})
	}
	if len(kept) == 0 {
		v.Sections = append(v.Sections, wire.Section{Key: "empty", Items: []wire.Component{{Type: "empty", Key: "empty",
			Title: pageWord(l, "empty_title"), Detail: pageWord(l, "empty_detail"), Icon: wire.AgentPageIconDefault}}})
	}
	if len(removed) > 0 {
		items := make([]wire.Component, 0, len(removed))
		for _, p := range removed {
			subtitle := i18n.T(l, "assistant-pages.removed_on", i18n.Args{"date": pageDate(l, p.RemovedAt.In(loc))})
			if p.RemovedBy == db.AssistantPageRemovedByAgent && p.RemovedReason != "" { // page remove's reason (pages_tool.go)
				subtitle = i18n.T(l, "assistant-pages.removed_by_agent", i18n.Args{"date": pageDate(l, p.RemovedAt.In(loc)), "reason": p.RemovedReason})
			}
			items = append(items, wire.Component{Type: wire.ComponentRow, Key: "removed:" + p.ID, Title: p.Title, Icon: p.Icon,
				Subtitle: subtitle,
				Actions: []wire.RowAction{
					{Title: pageWord(l, "restore"), Icon: "refresh", Swipe: true, Action: wire.Action{ID: "persona.restore:" + p.ID}},
					{Title: pageWord(l, "delete"), Icon: "trash", Role: wire.ButtonRoleDestructive,
						Action: wire.Action{ID: "persona.delete:" + p.ID, Confirm: pageWord(l, "delete_confirm")}},
				}})
		}
		v.Sections = append(v.Sections, wire.Section{Key: "removed", Title: pageWord(l, "removed"), Items: items})
	}
	return v
}

func keptPageRow(p db.AssistantPageRow, l i18n.Lang, loc *time.Location, now time.Time) wire.Component {
	return keptPageRowWith(p, nil, l, loc, now)
}

func keptPageRowWith(p db.AssistantPageRow, readings []db.AssistantPageReadingRow, l i18n.Lang, loc *time.Location, now time.Time) wire.Component {
	pin := wire.RowAction{Title: pageWord(l, "pin"), Icon: "star", Swipe: true, Action: wire.Action{ID: "persona.pin", Values: map[string]string{"page": p.ID}}}
	if p.Pinned {
		pin = wire.RowAction{Title: pageWord(l, "unpin"), Icon: "star", Swipe: true, Action: wire.Action{ID: "persona.unpin", Values: map[string]string{"page": p.ID}}}
	}
	row := wire.Component{Type: wire.ComponentRow, Key: "page:" + p.ID, Title: p.Title, Subtitle: keptPageSubtitle(p, readings, l, loc, now),
		Icon: p.Icon, IconColor: p.IconColor, Action: &wire.Action{ID: "persona.open:" + p.ID, Push: &wire.Push{UI: p.ID, Title: p.Title}},
		Actions: []wire.RowAction{pin, {Title: pageWord(l, "remove"), Icon: "trash", Role: wire.ButtonRoleDestructive, Swipe: true,
			Action: wire.Action{ID: "persona.remove", Values: map[string]string{"page": p.ID}, Confirm: pageWord(l, "remove_confirm")}}}}
	if len(p.Watch) > 0 {
		if watch, err := wire.ParsePageWatch(p.Watch); err == nil {
			g := drawWatchGlance(watch, parseWatchReadings(watch, readings), l)
			row.Value = g.value
			row.Spark = g.spark
			if g.icon != "" {
				row.Icon = g.icon
				row.IconColor = g.iconColor
			}
		}
	}
	return row
}

// drawPage is kept page p as the apps draw it: its detail page sub ("" or one it does not have: the page itself), its
// actions made the extension pages' own, its title as the View's, and the hub's footer (last: the owner's latest tap).
func drawPage(p *db.AssistantPageRow, sub string, last *db.AssistantPageActionRow, l i18n.Lang, loc *time.Location, now time.Time, web string) wire.View {
	return drawPageWith(p, sub, last, pageDrawExtra{}, l, loc, now, web)
}

func drawPageWith(p *db.AssistantPageRow, sub string, last *db.AssistantPageActionRow, extra pageDrawExtra, l i18n.Lang, loc *time.Location, now time.Time, web string) wire.View {
	var watch *wire.PageWatch
	if len(p.Watch) > 0 {
		w, err := wire.ParsePageWatch(p.Watch)
		if err != nil {
			return drawBrokenPage(p, l)
		}
		watch = w
	}
	var v wire.View
	if watch != nil && emptyPageView(p.View) {
		sub = ""
	} else {
		root, err := wire.ParseAgentPage(p.View)
		if err != nil {
			return drawBrokenPage(p, l)
		}
		v = *root
		if page, ok := root.Pages[sub]; ok && sub != "" {
			v = page
		} else {
			sub = ""
		}
	}
	v.Pages, v.Fallback, v.RefreshAfter = nil, "", 0
	for _, a := range wire.Actions(&v) {
		pageActionForExtension(a, p.ID, p.Version)
	}
	v.Title = p.Title
	v.State = pageState{P: sub, V: p.Version}.encode()
	if watch != nil && sub == "" {
		sec := drawWatchSection(watch, parseWatchReadings(watch, extra.readings), l, loc, now, 0)
		v.Sections = append([]wire.Section{sec}, v.Sections...)
	}
	v.Sections = append(v.Sections, pageFooterWith(p, watch, last, extra, l, loc, now, web))
	return v
}

// pageActionForExtension makes a card's action one an extension page has: a reply is the action's id alone (its
// words stay on the hub), a detail page is pushed as this page with that state, and an open or a pop is answered by
// the hub when tapped (pageRequest.tap).
func pageActionForExtension(a *wire.Action, id string, version int64) {
	switch {
	case a.Reply != "":
		a.Reply = ""
	case a.Push != nil && a.Push.Page != "":
		a.Push = &wire.Push{UI: id, State: pageState{P: a.Push.Page, V: version}.encode()}
	case a.Pop:
		a.Pop = false
	case a.Open != "":
		a.Open = ""
	}
}

// pageFooter is the hub's own part of a page: when it was updated, where the owner's latest tap is, and the owner's
// actions.
func pageFooter(p *db.AssistantPageRow, last *db.AssistantPageActionRow, l i18n.Lang, loc *time.Location, now time.Time, web string) wire.Section {
	return pageFooterWith(p, nil, last, pageDrawExtra{}, l, loc, now, web)
}

func pageFooterWith(p *db.AssistantPageRow, watch *wire.PageWatch, last *db.AssistantPageActionRow, extra pageDrawExtra, l i18n.Lang, loc *time.Location, now time.Time, web string) wire.Section {
	items := []wire.Component{{Type: wire.ComponentText, Key: "persona.updated", Text: pageFooterTimeLine(p, extra, l, loc, now), Style: "caption"}}
	if p.Paused {
		items = append(items, wire.Component{Type: wire.ComponentText, Key: "persona.paused", Text: watchWord(l, "updates_paused"), Style: "caption"})
	}
	if refreshItems := pageRefreshStatusItems(p, extra.refresh, l); len(refreshItems) > 0 {
		items = append(items, refreshItems...)
	} else {
		items = append(items, pageTapStatus(p, last, l, now, web)...)
	}
	items = append(items, pageWatchFooterControls(p, watch, extra.platform, l)...)
	pin := wire.Component{Type: wire.ComponentButton, Key: "persona.pin", Title: pageWord(l, "pin"), Action: &wire.Action{ID: "persona.pin"}}
	if p.Pinned {
		pin = wire.Component{Type: wire.ComponentButton, Key: "persona.pin", Title: pageWord(l, "unpin"), Action: &wire.Action{ID: "persona.unpin"}}
	}
	items = append(items, pageHistoryButton(p, l), pin,
		wire.Component{Type: wire.ComponentButton, Key: "persona.rename", Title: pageWord(l, "rename"),
			Action: &wire.Action{ID: "persona.rename", Push: &wire.Push{UI: p.ID, State: pageState{V: p.Version, Form: "rename"}.encode(), Title: pageWord(l, "rename")}}},
		wire.Component{Type: wire.ComponentButton, Key: "persona.remove", Title: pageWord(l, "remove"), Role: wire.ButtonRoleDestructive,
			Action: &wire.Action{ID: "persona.remove", Confirm: pageWord(l, "remove_confirm")}})
	return wire.Section{Key: "persona.footer", Items: items}
}

// pageTapStatusFor is how long a tap's state stays on its page: a failed one until it is sent again (it needs the
// owner), the others a day.
const pageTapStatusFor = 24 * time.Hour

// pageTapStatus is the footer's line for the owner's latest tap: Received (the hub has it), Delivered to Persona (in
// its conversation; Sent to your main conversation instead when that one was gone), Persona is working (its turn
// started), and once that turn ended nothing (it updated the page) or Persona replied in the chat (it did not, with a
// link to the conversation); Not delivered with Retry when it failed.
func pageTapStatus(p *db.AssistantPageRow, a *db.AssistantPageActionRow, l i18n.Lang, now time.Time, web string) []wire.Component {
	if a == nil {
		return nil
	}
	line := func(key string) []wire.Component {
		return []wire.Component{{Type: wire.ComponentText, Key: "persona.status", Text: pageWord(l, key), Style: "caption"}}
	}
	switch {
	case a.Status == db.AssistantPageTapFailed:
		return append(line("not_delivered"), wire.Component{Type: wire.ComponentButton, Key: "persona.retry", Title: pageWord(l, "retry"),
			Action: &wire.Action{ID: "persona.retry:" + a.ID}})
	case now.Sub(a.At) > pageTapStatusFor:
		return nil
	case a.Status == db.AssistantPageTapPending:
		return line("received")
	case a.TurnEndedAt != nil:
		if a.TurnStartedAt != nil && p.ContentAt.After(*a.TurnStartedAt) {
			return nil // the turn answered on the page
		}
		if link := sessionLink(web, a.TargetMachine, a.TargetConversation, true); strings.HasPrefix(link, "https://") {
			if u, err := url.Parse(link); err == nil && u.Host != "" {
				return []wire.Component{{Type: wire.ComponentLink, Key: "persona.status", Title: pageWord(l, "replied"), URL: link}}
			}
		}
		return line("replied")
	case a.TurnStartedAt != nil:
		return line("working")
	case a.Redirected:
		return line("redirected")
	}
	return line("delivered")
}

// drawRenameForm is the owner's form to rename page p.
func drawRenameForm(p *db.AssistantPageRow, l i18n.Lang) wire.View {
	return wire.View{Title: pageWord(l, "rename"), State: pageState{V: p.Version, Form: "rename"}.encode(), Sections: []wire.Section{{Key: "persona.rename", Items: []wire.Component{
		{Type: wire.ComponentField, Key: "persona.title", Name: "title", Label: pageWord(l, "title_field"), Kind: "text", Value: p.Title},
		{Type: wire.ComponentButton, Key: "persona.save", Title: pageWord(l, "save"), Action: &wire.Action{ID: "persona.rename", Submit: true}},
	}}}}
}

// drawGonePage is a page in Recently removed, opened from a link or a sidebar not drawn again yet.
func drawGonePage(p *db.AssistantPageRow, l i18n.Lang) wire.View {
	return wire.View{Title: p.Title, Sections: []wire.Section{{Key: "persona.gone", Items: []wire.Component{{Type: "empty", Key: "persona.gone",
		Title: pageWord(l, "gone_title"), Detail: pageWord(l, "gone_detail"), Icon: p.Icon,
		Button: &wire.EmptyButton{Title: pageWord(l, "restore"), Action: wire.Action{ID: "persona.restore:" + p.ID}}}}}}}
}

// drawBrokenPage stands for a page whose contents this hub cannot draw (stored by another version, say).
func drawBrokenPage(p *db.AssistantPageRow, l i18n.Lang) wire.View {
	return wire.View{Title: p.Title, Sections: []wire.Section{{Key: "persona.broken", Items: []wire.Component{{Type: "empty", Key: "persona.broken",
		Title: pageWord(l, "broken_title"), Detail: pageWord(l, "broken_detail"), Icon: p.Icon}}}}}
}
