package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The page tool (Dashboards, #15017; docs/assistant/dashboards.md): how a conversation drafts, updates, reads, lists
// and removes the pages the owner comes back to. One tool with an action, so the tool list grows by one entry
// (prompts/mcp/assistant/page.md; the prompt size ratchet).
//
// Who writes: the main conversation (its pages are owner_kind main: whichever conversation is the main one writes them,
// and a tap goes to it) and ordinary assistant conversations, direction leaders included (owner_kind conversation:
// its machine and public id, logical_authority.go). Who never does: a conversation that writes to other people (the
// Inbox's, Lark /agent's, an extension's: outward_reader.go), a meeting guest, the background supervisor and its rounds
// (roundToolNames does not list page), a scheduled routine's own conversation, a conversation on a work computer, a
// space's (the Health space's included), and any conversation of a manager other than the built-in one. Who calls is
// only ever the MCP route's machine and session, never an argument. The tool is listed only to a conversation that may
// write (guardTools), and each call checks again.
//
// A draft never shows by itself: create puts a preview card in the calling conversation (the page read-only, then Keep
// in Dashboards, Keep and pin to the sidebar, Not now), and the owner's Keep on it, in the card's own request
// (applyPageOffer), makes it a kept page in the sidebar. Neither a draft nor an update pushes or counts in the card
// ledger: the owner looks when they want to.

const pageToolName = "page"

// The preview card's choices (its actions' ids): the hub's own, as on a page (wire.AgentPageActionPrefix).
const (
	pageOfferKeep    = "persona.keep"
	pageOfferKeepPin = "persona.keep_pin"
	pageOfferNotNow  = "persona.not_now"
	// pageOfferSection is the preview's last section, its choices; replaced by a line once the draft is gone.
	pageOfferSection = "persona.offer"
	// pagePreviewSections is how many of the page's own sections the preview shows.
	pagePreviewSections = 3
	// pageRecentTaps is how many of the owner's latest taps page get gives.
	pageRecentTaps = 10
	pageNoteMost   = 120
	pageWhyMost    = 200
)

func pageOfferAction(action string) bool {
	return action == pageOfferKeep || action == pageOfferKeepPin || action == pageOfferNotNow
}

// pageCaller is a conversation that may write pages: its machine (canonical) and public id, and whether it is the
// main conversation (it writes the main conversation's pages, and reads every page).
type pageCaller struct {
	machine, session string
	main             bool
}

// pageRefused is why a conversation does not get the page tool, in a sentence the model can act on.
type pageRefused struct{ why string }

func (e *pageRefused) Error() string { return "page: " + e.why }

func refusePage(why string) error { return &pageRefused{why: why} }

// pageCallerOf is the conversation session on machine as the page tool's caller, or why it is not one. listing skips
// the check that asks its computer (the node asking for its tools says it is an assistant conversation).
func (s *Service) pageCallerOf(ctx context.Context, machine, session string, listing bool) (pageCaller, error) {
	if !s.pagesManager() {
		return pageCaller{}, refusePage("dashboards are the built-in assistant's; conversations of this manager do not make them.")
	}
	if _, ok := s.pageStore(); !ok {
		return pageCaller{}, refusePage("dashboards need the assistant's database, which this process does not have.")
	}
	if !s.dashboardsOn(ctx) {
		return pageCaller{}, refusePage("Dashboards are off. Only the owner turns them on, in Settings › Assistant; answer in the chat instead.")
	}
	if machine == "" || session == "" {
		return pageCaller{}, refusePage("only an assistant conversation makes dashboards.")
	}
	if s.isWorkMachine(machine) {
		return pageCaller{}, refusePage("a conversation on a work computer does not make dashboards.")
	}
	if _, guest, err := s.guestOf(ctx, machine, session); err != nil || guest {
		return pageCaller{}, refusePage("a meeting guest does not make dashboards.")
	}
	if s.outwardReader(ctx, machine, session) {
		return pageCaller{}, refusePage("a conversation that writes to other people (the Inbox's, Lark /agent's, an extension's) does not make dashboards; the owner can ask for one in the main conversation.")
	}
	if s.isBackground(ctx, s.supervisionHoldKey(ctx, session)) {
		return pageCaller{}, refusePage("the background supervisor does not make dashboards.")
	}
	if routine, err := s.routineConversation(ctx, machine, session); err != nil || routine {
		return pageCaller{}, refusePage("a scheduled routine's run does not make dashboards; the conversation the owner talks in does.")
	}
	switch space := s.agentCallerSpace(ctx, machine, session); {
	case space == SpaceHealth:
		return pageCaller{}, refusePage("the Health space does not make dashboards.")
	case space != "":
		return pageCaller{}, refusePage("a space's conversations do not make dashboards yet; the main conversation does.")
	}
	if !listing {
		if c, ok := s.session(ctx, machine, session); !ok || !c.Assistant {
			return pageCaller{}, refusePage("only an assistant conversation makes dashboards.")
		}
	}
	cfg := s.settings(ctx)
	main := cfg.Main != "" && cfg.Main == session && s.sameMachine(machine, cfg.mainRouteMachine())
	return pageCaller{machine: s.canonicalMachine(machine), session: session, main: main}, nil
}

// routineConversation: session on machine is a scheduled task's own conversation (Task.RunSessionID, non-source
// ScheduleRun, or OriginTask), or the schedule cannot be read.
func (s *Service) routineConversation(ctx context.Context, machine, session string) (bool, error) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range sc.tasks {
		if t.RunSessionID == session && (t.RunMachineID == "" || s.sameMachine(t.RunMachineID, machine)) {
			sc.mu.Unlock()
			return true, nil
		}
		if t.SessionID == "" && t.LastSessionID == session && (t.RunMachineID == "" || s.sameMachine(t.RunMachineID, machine)) {
			sc.mu.Unlock()
			return true, nil
		}
	}
	for i := len(sc.runs) - 1; i >= 0; i-- {
		r := sc.runs[i]
		if r.Conversation != "source" && r.SessionID == session && (r.MachineID == "" || s.sameMachine(r.MachineID, machine)) {
			sc.mu.Unlock()
			return true, nil
		}
	}
	sc.mu.Unlock()
	if _, isTask := s.originTaskRef(ctx, machine, session); isTask {
		return true, nil
	}
	return false, nil
}

// pageToolListed narrows tools for conversation session on machine:
//   - a page-bound routine run gets no notify_user/inbox_post and gets page narrowed to get/patch of its own page
//     while its grant and current-run checks pass;
//   - any other conversation gets page only if it may write dashboards.
func (s *Service) pageToolListed(ctx context.Context, machine, session string, tools []proto.Tool) []proto.Tool {
	if task, ok, err := s.pageRoutineTaskFor(ctx, machine, session); err == nil && ok {
		_, allowedErr := s.pageRoutineCallerOf(ctx, machine, session, task, "", "", true)
		out := make([]proto.Tool, 0, len(tools))
		for _, t := range tools {
			switch t.Name {
			case "notify_user", "inbox_post":
				continue
			case pageToolName:
				if allowedErr == nil {
					out = append(out, pageRoutineTool(task.PageID))
				}
			default:
				out = append(out, t)
			}
		}
		return out
	}
	if !slices.ContainsFunc(tools, func(t proto.Tool) bool { return t.Name == pageToolName }) {
		return tools
	}
	if _, err := s.pageCallerOf(ctx, machine, session, true); err == nil {
		return tools
	}
	return slices.DeleteFunc(slices.Clone(tools), func(t proto.Tool) bool { return t.Name == pageToolName })
}

// writes: the caller may write page p (its owner is the main conversation and the caller is it now, or the caller is
// the conversation that owns it).
func (s *Service) pageWrites(c pageCaller, p *db.AssistantPageRow) bool {
	switch p.OwnerKind {
	case db.AssistantPageOwnerMain:
		return c.main
	case db.AssistantPageOwnerConversation:
		return p.OwnerRef == c.session && s.sameMachine(p.OwnerMachine, c.machine)
	}
	return false
}

// pageToolInput is the page tool's arguments (prompts/mcp/assistant/page.md).
type pageToolInput struct {
	Action    string          `json:"action"`
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Icon      string          `json:"icon"`
	Why       string          `json:"why"`
	View      json.RawMessage `json:"view"`
	FromView  json.RawMessage `json:"from_view"`
	Watch     json.RawMessage `json:"watch"`
	Reading   json.RawMessage `json:"reading"`
	Items     json.RawMessage `json:"items"`
	AlertText string          `json:"alert_text"`
	Version   int64           `json:"version"`
	Note      string          `json:"note"`
	Reason    string          `json:"reason"`
}

// toolPage runs the page tool for conversation self on machine.
func (s *Service) toolPage(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var in pageToolInput
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if len(in.FromView) != 0 && in.Action != "create" {
		return "", errors.New("from_view: only page create accepts a source card")
	}
	if task, ok, err := s.pageRoutineTaskFor(ctx, machine, self); err != nil {
		return "", err
	} else if ok {
		rc, err := s.pageRoutineCallerOf(ctx, machine, self, task, in.Action, in.ID, false)
		if err != nil {
			return "", err
		}
		st, _ := s.pageStore()
		switch in.Action {
		case "get":
			return s.pageGetResponse(ctx, st, rc.page, false)
		case "patch":
			return s.pagePatch(ctx, st, rc.page, db.AssistantPageWriter{
				Kind:    db.AssistantPageByRoutine,
				Machine: rc.machine,
				Session: rc.session,
			}, in)
		}
		return "", fmt.Errorf("forbidden: a dashboard routine may only call page get and page patch on its own page (%s)", task.PageID)
	}
	c, err := s.pageCallerOf(ctx, machine, self, false)
	if err != nil {
		return "", err
	}
	st, _ := s.pageStore()
	switch in.Action {
	case "create":
		return s.pageCreate(ctx, st, machine, c, in)
	case "update":
		return s.pageUpdate(ctx, st, c, in)
	case "patch":
		return s.pagePatchByWriter(ctx, st, c, in)
	case "get":
		return s.pageGet(ctx, st, c, in)
	case "list":
		return s.pageList(ctx, st, c)
	case "remove":
		return s.pageRemove(ctx, st, c, in)
	}
	return "", errors.New("action: create, update, patch, get, list or remove")
}

// pageLine checks a one-line text argument of at most most characters.
func pageLine(field, text string, most int) (string, error) {
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > most || strings.ContainsFunc(text, unicode.IsControl) {
		return "", fmt.Errorf("%s: one line of at most %d characters", field, most)
	}
	return text, nil
}

// parsePageView is a page's view as the tool takes it: the page floor (wire.ParseAgentPage), as it is stored.
func parsePageView(raw json.RawMessage) (*wire.View, json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil, errors.New("view is required: the whole page as a View")
	}
	v, err := wire.ParseAgentPage(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid view: %w", err)
	}
	stored, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return v, stored, nil
}

// pageWriteError is a store's refusal of a write as the tool says it: a word the model can act on first.
func pageWriteError(err error) error {
	switch {
	case errors.Is(err, db.ErrAssistantPageNotFound):
		return errors.New("not_found: no such page; page list gives the ids")
	case errors.Is(err, db.ErrAssistantPageVersion):
		return errors.New("version_conflict: the page changed; read it with page get first, then update from its version")
	case errors.Is(err, db.ErrAssistantPagePaused):
		return errors.New("paused_by_owner: the owner paused this page's updates; ask the owner in the chat before changing it")
	case errors.Is(err, db.ErrAssistantPageRate), errors.Is(err, db.ErrAssistantPagesRate):
		return fmt.Errorf("rate_limited: %v; update it later, with what changed meanwhile", err)
	case errors.Is(err, db.ErrAssistantPageRemoved):
		return errors.New("removed: the page was removed or not kept; it is no longer written")
	case errors.Is(err, db.ErrAssistantPageNotKept):
		return errors.New("not_kept: this page is still a draft; readings and patches start only after the owner keeps it")
	case errors.Is(err, db.ErrAssistantPageKept):
		return errors.New("title and icon: once kept they are the owner's; update the contents only")
	case errors.Is(err, db.ErrAssistantPageWatchDisplay):
		return errors.New("watch_display_fixed: a kept page's watch display cannot change; draft a new page instead")
	}
	return err
}

func (s *Service) pageCreate(ctx context.Context, st pageStore, callMachine string, c pageCaller, in pageToolInput) (string, error) {
	title, err := wire.CheckAgentPageTitle(in.Title)
	if err == nil {
		title, err = pageTitleRules(false).Check(title)
	}
	if err != nil {
		return "", err
	}
	icon := cmp.Or(strings.TrimSpace(in.Icon), wire.AgentPageIconDefault)
	if err := wire.CheckAgentPageIcon(icon); err != nil {
		return "", err
	}
	why, err := pageLine("why", in.Why, pageWhyMost)
	if err != nil {
		return "", err
	}
	note, err := pageLine("note", in.Note, pageNoteMost)
	if err != nil {
		return "", err
	}
	var watch *wire.PageWatch
	var storedWatch json.RawMessage
	if hasNonNullJSON(in.Watch) {
		watch, err = wire.ParsePageWatch(in.Watch)
		if err != nil {
			return "", fmt.Errorf("invalid watch: %w", err)
		}
		storedWatch, err = json.Marshal(watch)
		if err != nil {
			return "", err
		}
	}
	var v *wire.View
	var stored json.RawMessage
	views, err := s.viewsStore()
	if err != nil {
		return "", err
	}
	switch {
	case len(in.FromView) != 0:
		if len(in.View) != 0 {
			return "", errors.New("from_view: choose either a source card or an explicit view")
		}
		v, stored, err = s.pageFromCard(ctx, st, views, c, in.FromView)
	case hasNonNullJSON(in.View) || watch == nil:
		v, stored, err = parsePageView(in.View)
	default:
		stored = json.RawMessage(`{}`)
	}
	if err != nil {
		return "", err
	}
	l := s.lang(ctx)
	preview := pagePreviewWatch(v, watch, nil, title, why, l)
	card := "view_" + randomHex(8)
	draft := db.NewAssistantPage{ID: db.NewAssistantPageID(), Title: title, Icon: icon, OwnerKind: db.AssistantPageOwnerMain,
		CreatedMachine: c.machine, CreatedSession: c.session, OfferView: card, Why: why, View: stored, Watch: storedWatch, Note: note, At: time.Now()}
	if !c.main {
		draft.OwnerKind, draft.OwnerMachine, draft.OwnerRef = db.AssistantPageOwnerConversation, c.machine, c.session
	}
	p, err := st.CreateAssistantPage(ctx, draft)
	var taken *db.AssistantPageTitleTakenError
	switch {
	case errors.As(err, &taken):
		return viewToolJSON(map[string]any{"error": "exists", "id": taken.ID})
	case errors.Is(err, db.ErrAssistantPagesRate):
		return "", pageWriteError(err)
	case err != nil:
		return "", err
	}
	if _, err := s.showView(ctx, views, callMachine, c.session, card, &preview); err != nil {
		// the draft has no card to keep it from: it goes, as one the owner did not want
		if _, _, derr := st.DeclineAssistantPage(context.WithoutCancel(ctx), card, time.Now()); derr != nil {
			s.logf("assistant pages: dropping draft %s without its card: %v", p.ID, derr)
		}
		return "", err
	}
	return viewToolJSON(map[string]any{"id": p.ID, "version": p.Version, "status": p.Status})
}

func (s *Service) pageUpdate(ctx context.Context, st pageStore, c pageCaller, in pageToolInput) (string, error) {
	p, err := s.writablePage(ctx, st, c, in.ID)
	if err != nil {
		return "", err
	}
	note, err := pageLine("note", in.Note, pageNoteMost)
	if err != nil {
		return "", err
	}
	hasView := hasNonNullJSON(in.View)
	hasWatch := hasNonNullJSON(in.Watch)
	clearWatch := isExplicitNullJSON(in.Watch)

	var storedWatch json.RawMessage
	if hasWatch {
		w, err := wire.ParsePageWatch(in.Watch)
		if err != nil {
			return "", fmt.Errorf("invalid watch: %w", err)
		}
		storedWatch, err = json.Marshal(w)
		if err != nil {
			return "", err
		}
	}
	var v *wire.View
	var storedView json.RawMessage
	switch {
	case hasView:
		v, storedView, err = parsePageView(in.View)
		if err != nil {
			return "", err
		}
	case clearWatch:
		if p.Status == db.AssistantPageDraft && (!hasNonNullJSON(p.View) || bytes.Equal(bytes.TrimSpace(p.View), []byte("{}"))) {
			return "", errors.New("view is required when clearing watch")
		}
	case !hasWatch:
		return "", errors.New("view is required: the whole page as a View (or watch)")
	}
	u := db.AssistantPageUpdate{ID: p.ID, Expected: cmp.Or(in.Version, p.Version), View: storedView, Watch: storedWatch, ClearWatch: clearWatch,
		By: db.AssistantPageWriter{Kind: db.AssistantPageByAgent, Machine: c.machine, Session: c.session, Note: note}, At: time.Now()}
	if in.Title != "" || in.Icon != "" {
		if p.Status != db.AssistantPageDraft {
			return "", pageWriteError(db.ErrAssistantPageKept)
		}
		if in.Title != "" {
			if u.Title, err = wire.CheckAgentPageTitle(in.Title); err == nil {
				u.Title, err = pageTitleRules(false).Check(u.Title)
			}
			if err != nil {
				return "", err
			}
		}
		if in.Icon != "" {
			if err := wire.CheckAgentPageIcon(in.Icon); err != nil {
				return "", err
			}
			u.Icon = in.Icon
		}
	}
	row, err := st.UpdateAssistantPage(ctx, u)
	if err != nil {
		return "", pageWriteError(err)
	}
	if row.Status == db.AssistantPageDraft {
		s.redrawPageOffer(ctx, row, v)
	} else {
		s.pageWritten(row.ID)
	}
	return viewToolJSON(map[string]any{"version": row.Version})
}

// writablePage is page id when the caller may write it.
func (s *Service) writablePage(ctx context.Context, st pageStore, c pageCaller, id string) (*db.AssistantPageRow, error) {
	if !pageIDPattern.MatchString(id) {
		return nil, errors.New("id: the pg_ id page create or page list gave")
	}
	p, err := st.AssistantPage(ctx, id)
	if err != nil {
		return nil, pageWriteError(err)
	}
	if !s.pageWrites(c, p) {
		return nil, errors.New("forbidden: another conversation keeps this page; ask it with conversation_send")
	}
	if p.Status == db.AssistantPageRemoved {
		return nil, pageWriteError(db.ErrAssistantPageRemoved)
	}
	return p, nil
}

// pageTap is one of the owner's taps as page get gives it.
type pageTap struct {
	Action string            `json:"action"`
	Values map[string]string `json:"values"`
	At     time.Time         `json:"at"`
	Status string            `json:"status"`
}

// pageReadingEntry is one stored watch reading as page get gives it.
type pageReadingEntry struct {
	Reading   json.RawMessage `json:"reading"`
	AlertText string          `json:"alert_text,omitempty"`
	Note      string          `json:"note,omitempty"`
	ByKind    string          `json:"by_kind"`
	At        time.Time       `json:"at"`
}

func (s *Service) pageGet(ctx context.Context, st pageStore, c pageCaller, in pageToolInput) (string, error) {
	if !pageIDPattern.MatchString(in.ID) {
		return "", errors.New("id: the pg_ id page create or page list gave")
	}
	p, err := st.AssistantPage(ctx, in.ID)
	if err != nil {
		return "", pageWriteError(err)
	}
	writes := s.pageWrites(c, p)
	// the main conversation reads every kept page, to answer what the owner keeps; a draft is its conversation's
	if !writes && !(c.main && p.Status == db.AssistantPageActive) {
		return "", errors.New("forbidden: another conversation keeps this page")
	}
	return s.pageGetResponse(ctx, st, p, !writes)
}

func (s *Service) pageGetResponse(ctx context.Context, st pageStore, p *db.AssistantPageRow, readOnly bool) (string, error) {
	taps, err := st.AssistantPageActions(ctx, p.ID, pageRecentTaps)
	if err != nil {
		return "", err
	}
	recent := make([]pageTap, 0, len(taps))
	for _, a := range taps {
		values := a.Values
		if values == nil {
			values = map[string]string{}
		}
		recent = append(recent, pageTap{Action: a.Action, Values: values, At: a.At, Status: a.Status})
	}
	out := map[string]any{"id": p.ID, "title": p.Title, "status": p.Status, "pinned": p.Pinned, "paused": p.Paused,
		"version": p.Version, "updated_at": p.ContentAt, "view": p.View, "recent_actions": recent}
	if p.RoutineID != "" {
		out["routine_id"] = p.RoutineID
	}
	if hasNonNullJSON(p.Watch) {
		out["watch"] = p.Watch
		rows, err := st.AssistantPageReadings(ctx, p.ID, 0)
		if err != nil {
			return "", err
		}
		readings := make([]pageReadingEntry, 0, len(rows))
		for _, r := range rows {
			readings = append(readings, pageReadingEntry{
				Reading:   r.Reading,
				AlertText: r.AlertText,
				Note:      r.Note,
				ByKind:    r.ByKind,
				At:        r.At,
			})
		}
		out["readings"] = readings
	}
	if readOnly {
		out["read_only"] = true
	}
	return viewToolJSON(out)
}

// pageEntry is one page as page list gives it.
type pageEntry struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Pinned bool   `json:"pinned"`
	Paused bool   `json:"paused"`
}

func (s *Service) pageList(ctx context.Context, st pageStore, c pageCaller) (string, error) {
	all, err := st.AssistantPages(ctx, db.AssistantPageQuery{Status: []string{db.AssistantPageActive, db.AssistantPageDraft}})
	if err != nil {
		return "", err
	}
	mine, others := []pageEntry{}, []pageEntry{}
	for i := range all {
		p := &all[i]
		e := pageEntry{ID: p.ID, Title: p.Title, Status: p.Status, Pinned: p.Pinned, Paused: p.Paused}
		switch {
		case s.pageWrites(c, p):
			mine = append(mine, e)
		case c.main && p.Status == db.AssistantPageActive:
			others = append(others, e)
		}
	}
	out := map[string]any{"pages": mine}
	if c.main {
		out["read_only"] = others // kept by other conversations: the owner may ask what they keep
	}
	return viewToolJSON(out)
}

func (s *Service) pageRemove(ctx context.Context, st pageStore, c pageCaller, in pageToolInput) (string, error) {
	p, err := s.writablePage(ctx, st, c, in.ID)
	if err != nil {
		return "", err
	}
	reason, err := pageLine("reason", in.Reason, db.AssistantPageReasonMost)
	if err == nil && reason == "" {
		err = errors.New("reason: say in a line why, for the owner")
	}
	if err != nil {
		return "", err
	}
	if p.Status == db.AssistantPageDraft {
		// a draft is withdrawn: its card says so, its held routine is deleted, and it never shows
		row, _, err := st.DeclineAssistantPage(ctx, p.OfferView, time.Now())
		if err != nil {
			return "", pageWriteError(err)
		}
		s.closePageOffer(ctx, row, "offer_withdrawn")
		s.pageRoutineDeleted(ctx, row.ID, cmp.Or(row.RoutineID, p.RoutineID))
		return viewToolJSON(map[string]any{"status": row.Status})
	}
	row, changed, err := st.RemoveAssistantPageWithReason(ctx, p.ID, db.AssistantPageRemovedByAgent, reason, time.Now())
	if err != nil {
		return "", pageWriteError(err)
	}
	if changed {
		s.pageRoutineRemoved(ctx, row.ID, row.RoutineID)
	}
	s.pagesTold(ctx, row.ID, changed)
	return viewToolJSON(map[string]any{"status": row.Status})
}

// ---- the preview card ----

// pagePreview is the card a draft is offered on: the page read-only (its first sections, without buttons, forms or
// anything else to tap), then why it exists and the owner's three choices.
func pagePreview(page *wire.View, title, why string, l i18n.Lang) wire.View {
	return pagePreviewWatch(page, nil, nil, title, why, l)
}

// pagePreviewWatch builds a draft's preview card, adding the watch summary (Checks, How often, Shows) and switches
// (Alert me when …, Show on Overview) above the three submit choices when watch is non-nil.
func pagePreviewWatch(page *wire.View, watch *wire.PageWatch, task *Task, title, why string, l i18n.Lang) wire.View {
	v := wire.View{Title: title, Fallback: pagePreviewFallbackWatch(page, watch, title, l)}
	if page != nil {
		for _, sec := range page.Sections {
			if len(v.Sections) == pagePreviewSections {
				break
			}
			if sec, ok := readOnlySection(sec); ok {
				v.Sections = append(v.Sections, sec)
			}
		}
	}
	if watch != nil {
		v.Sections = append(v.Sections, pageWatchPreviewSection(watch, task, l))
	}
	v.Sections = append(v.Sections, pageOfferChoicesWatch(watch, why, l))
	if _, err := wire.ParseAssistantView(pageJSON(v)); err != nil {
		// a page whose read-only part a card does not take (it never should): its words, then watch + choices
		var secs []wire.Section
		if page != nil && page.Fallback != "" {
			secs = append(secs, wire.Section{Key: "persona.preview", Items: []wire.Component{{Type: wire.ComponentText, Key: "persona.preview",
				Text: clip(page.Fallback, 2000)}}})
		}
		if watch != nil {
			secs = append(secs, pageWatchPreviewSection(watch, task, l))
		}
		secs = append(secs, pageOfferChoicesWatch(watch, why, l))
		v.Sections = secs
	}
	return v
}

func pageJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// pagePreviewFallbackWatch is the card's words without its components: the draft and the choices, then the page's or watch's.
func pagePreviewFallbackWatch(page *wire.View, watch *wire.PageWatch, title string, l i18n.Lang) string {
	tail := ""
	switch {
	case page != nil && page.Fallback != "":
		tail = page.Fallback
	case watch != nil:
		tail = watch.Question
	}
	head := i18n.T(l, "assistant-pages.offer_fallback", i18n.Args{"title": title})
	if tail == "" {
		return clip(head, 2000)
	}
	return clip(head+"\n"+tail, 2000)
}

// pageOfferChoices is the preview's last section: why the page exists, and Keep in Dashboards, Keep and pin to the
// sidebar, Not now. Each is a reply: the conversation is told what the owner chose, as for any card.
func pageOfferChoices(why string, l i18n.Lang) wire.Section {
	return pageOfferChoicesWatch(nil, why, l)
}

func pageOfferChoicesWatch(watch *wire.PageWatch, why string, l i18n.Lang) wire.Section {
	sec := wire.Section{Key: pageOfferSection, Title: pageWord(l, "offer_title")}
	if why != "" {
		sec.Items = append(sec.Items, wire.Component{Type: wire.ComponentText, Key: "persona.why", Text: why, Style: wire.TextStyleSecondary})
	}
	if watch != nil {
		if watch.Alert != nil {
			sec.Items = append(sec.Items, wire.Component{
				Type:  wire.ComponentField,
				Key:   "persona.offer.alert",
				Name:  pageOfferFieldAlert,
				Label: pageWatchAlertLabel(watch, l),
				Kind:  wire.FieldKindToggle,
				Value: "false",
			})
		}
		sec.Items = append(sec.Items, wire.Component{
			Type:  wire.ComponentField,
			Key:   "persona.offer.overview",
			Name:  pageOfferFieldOverview,
			Label: pageRoutineWord(l, "preview_overview"),
			Kind:  wire.FieldKindToggle,
			Value: "false",
		})
	}
	for _, c := range []struct{ id, word string }{{pageOfferKeep, "keep"}, {pageOfferKeepPin, "keep_pin"}, {pageOfferNotNow, "not_now"}} {
		word := pageWord(l, c.word)
		sec.Items = append(sec.Items, wire.Component{Type: wire.ComponentButton, Key: c.id, Title: word, Action: &wire.Action{ID: c.id, Reply: word, Submit: watch != nil}})
	}
	return sec
}

// readOnlyRefused are the components a preview leaves out: they are there to be tapped or typed into.
var readOnlyRefused = []string{wire.ComponentButton, wire.ComponentField, wire.ComponentSegmented, "composer", "comment_thread", "breadcrumb"}

// readOnlySection is sec without anything to tap: the components that are controls go, the others lose their actions.
// ok is false when nothing is left.
func readOnlySection(sec wire.Section) (wire.Section, bool) {
	out := sec
	out.More, out.Link, out.Items = nil, nil, nil
	for _, e := range sec.Items {
		if slices.Contains(readOnlyRefused, e.Type) {
			continue
		}
		e.Action, e.Button, e.Actions, e.Buttons = nil, nil, nil, nil
		e.Expand, e.Load, e.Comment, e.Reply, e.Context, e.MarkViewed, e.Draft, e.Navigate = nil, nil, nil, nil, nil, nil, nil, nil
		e.Crumbs, e.Threads = nil, nil
		e.Tiles = slices.Clone(e.Tiles)
		for i := range e.Tiles {
			e.Tiles[i].Action, e.Tiles[i].Actions = nil, nil
		}
		e.Nodes = slices.Clone(e.Nodes)
		for i := range e.Nodes {
			e.Nodes[i].Action = nil
		}
		out.Items = append(out.Items, e)
	}
	return out, len(out.Items) > 0
}

// showView puts v in conversation's feed as a new live card id, made on machine (view_show's path).
func (s *Service) showView(ctx context.Context, st viewStore, machine, conversation, id string, v *wire.View) (wire.AssistantView, error) {
	view, err := json.Marshal(v)
	if err != nil {
		return wire.AssistantView{}, err
	}
	at := time.Now().UTC()
	row := db.AssistantViewRow{ID: id, Conversation: conversation, MachineID: machine, View: view, Version: 1, CreatedAt: at, UpdatedAt: at}
	out, err := storedView(row)
	if err != nil {
		return wire.AssistantView{}, err
	}
	body, err := json.Marshal(map[string]any{"view": out})
	if err != nil {
		return wire.AssistantView{}, err
	}
	feed := db.FeedRow{ID: "feed_" + randomHex(8), Conversation: conversation, At: at, Kind: feedView, Ref: row.ID, Body: body}
	if err := st.CreateAssistantView(ctx, row, feed); err != nil {
		return wire.AssistantView{}, err
	}
	s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": conversation, "item": FeedItem{ID: feed.ID, Conversation: conversation, At: at, Kind: feedView, Ref: row.ID, Body: body}})
	s.viewChanged(out)
	return out, nil
}

// rewriteView replaces live card id of conversation with v, from whatever version it has now.
func (s *Service) rewriteView(ctx context.Context, conversation, id string, edit func(*wire.View) bool) error {
	st, err := s.viewsStore()
	if err != nil {
		return err
	}
	row, err := st.AssistantView(ctx, conversation, id)
	if err != nil {
		return err
	}
	var v wire.View
	if err := json.Unmarshal(row.View, &v); err != nil {
		return err
	}
	if !edit(&v) {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	updated, err := st.UpdateAssistantView(ctx, conversation, id, row.Version, raw, time.Now().UTC())
	if err != nil {
		return err
	}
	out, err := storedView(*updated)
	if err != nil {
		return err
	}
	s.viewChanged(out)
	return nil
}

// redrawPageOffer draws draft p's preview card again from its new contents v (nil parses p.View), watch, and routine.
func (s *Service) redrawPageOffer(ctx context.Context, p *db.AssistantPageRow, v *wire.View) {
	if p == nil || p.OfferView == "" {
		return
	}
	if v == nil && hasNonNullJSON(p.View) && !bytes.Equal(bytes.TrimSpace(p.View), []byte("{}")) {
		v, _ = wire.ParseAgentPage(p.View)
	}
	var watch *wire.PageWatch
	if hasNonNullJSON(p.Watch) {
		watch, _ = wire.ParsePageWatch(p.Watch)
	}
	var task *Task
	if p.RoutineID != "" {
		if t, ok := s.pageRoutineTask(ctx, p.RoutineID); ok {
			task = &t
		}
	}
	preview := pagePreviewWatch(v, watch, task, p.Title, p.Why, s.lang(ctx))
	if err := s.rewriteView(ctx, p.CreatedSession, p.OfferView, func(card *wire.View) bool {
		*card = preview
		return true
	}); err != nil {
		s.logf("assistant pages: the preview of %s: %v", p.ID, err)
	}
}

// closePageOffer replaces the choices of the card draft p was offered on with a line saying why it is no longer
// offered (key: offer_expired, offer_withdrawn).
func (s *Service) closePageOffer(ctx context.Context, p *db.AssistantPageRow, key string) {
	if p == nil || p.OfferView == "" {
		return
	}
	line := pageWord(s.lang(ctx), key)
	if err := s.rewriteView(ctx, p.CreatedSession, p.OfferView, func(card *wire.View) bool {
		i := slices.IndexFunc(card.Sections, func(sec wire.Section) bool { return sec.Key == pageOfferSection })
		if i < 0 {
			return false
		}
		card.Sections[i] = wire.Section{Key: pageOfferSection, Items: []wire.Component{{Type: wire.ComponentText, Key: "persona.closed", Text: line, Style: wire.TextStyleSecondary}}}
		card.Fallback = clip(line+"\n"+card.Fallback, 2000)
		return true
	}); err != nil && !errors.Is(err, db.ErrAssistantViewNotFound) {
		s.logf("assistant pages: the card of %s: %v", p.ID, err)
	}
}

// ---- Keep ----

// applyPageOffer is the owner's choice on a draft's preview card, applied in the request that makes it (before the
// card's answer is stored and told to the conversation in the background): Keep makes the draft a kept page (pinned
// too for Keep and pin, while fewer than three are), so it is in the sidebar at once, even while the conversation's
// computer is offline and the card's answer waits for it; Not now declines it. It is idempotent by the card. It
// answers the request itself (and says so) when the choice cannot be made: the card is then not answered, so the owner
// can choose again. An action on a card that offers no page is not its business.
func (s *Service) applyPageOffer(w http.ResponseWriter, r *http.Request, st viewActionStore, a db.AssistantViewActionRow) bool {
	if a.Kind == viewActionKindHTML || !pageOfferAction(a.Action) {
		return false
	}
	ps, ok := s.pageStore()
	if !ok {
		return false
	}
	ctx := r.Context()
	p, err := ps.AssistantPageByOffer(ctx, a.Key)
	if errors.Is(err, db.ErrAssistantPageNotFound) {
		return false
	}
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return true
	}
	refuse := func(code, key string) bool {
		apierr.WriteCode(w, http.StatusConflict, code, i18n.Err("assistant-pages."+key))
		return true
	}
	now := time.Now()
	if a.Action == pageOfferNotNow {
		declined, _, err := ps.DeclineAssistantPage(ctx, a.Key, now)
		switch {
		case errors.Is(err, db.ErrAssistantPageNotDraft):
			return refuse("page_kept", "already_kept")
		case errors.Is(err, db.ErrAssistantPageRemoved):
			viewChangedNow(w, r, st, a.Conversation, a.Key) // expired: the card says so
			return true
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return true
		}
		if declined != nil {
			s.pageRoutineDeleted(ctx, declined.ID, cmp.Or(declined.RoutineID, p.RoutineID))
		}
		return false
	}
	if !s.dashboardsOn(ctx) {
		return refuse("dashboards_off", "off")
	}
	var values map[string]string
	if len(a.Values) > 0 {
		_ = json.Unmarshal(a.Values, &values)
	}
	alertOn := values[pageOfferFieldAlert] == "true"
	onOverview := values[pageOfferFieldOverview] == "true"
	device := r.Header.Get(extwire.HeaderDevice)
	kept, changed, err := ps.KeepAssistantPage(ctx, db.AssistantPageKeep{
		OfferView:  a.Key,
		Device:     device,
		Pin:        a.Action == pageOfferKeepPin,
		AlertOn:    alertOn,
		OnOverview: onOverview,
		At:         now,
	})
	switch {
	case errors.Is(err, db.ErrAssistantPageRemoved):
		viewChangedNow(w, r, st, a.Conversation, a.Key)
		return true
	case errors.Is(err, db.ErrAssistantPageKeptLimit):
		return refuse("page_limit", "kept_limit")
	case errors.Is(err, db.ErrAssistantPageTitleTaken):
		return refuse("page_title_taken", "title_taken")
	case err != nil:
		apierr.Write(w, http.StatusServiceUnavailable, err)
		return true
	}
	if changed {
		if kept.RoutineID != "" {
			if _, err := ps.GrantAssistantPageData(ctx, db.AssistantPageGrantRow{
				PageID:      kept.ID,
				GranteeKind: db.AssistantPageGrantRoutine,
				GranteeRef:  kept.RoutineID,
				Rights:      db.AssistantPageRightData,
				GrantedBy:   cmp.Or(device, "owner"),
				CreatedAt:   now,
			}); err != nil {
				s.logf("assistant pages: grant routine %s on keep of %s: %v", kept.RoutineID, kept.ID, err)
			}
			s.armPageRoutine(ctx, kept.ID, kept.RoutineID, now)
		}
		s.pagesListChanged(ctx)
		pinned := ""
		if kept.Pinned {
			pinned = kept.ID
		}
		if err := s.showPagesInSidebar(ctx, pinned); err != nil {
			s.logf("assistant pages: sidebar keys for %s: %v", p.ID, err)
		}
	}
	return false
}

// pageOffer: live card id offers a draft (its choices are the hub's, and the draft's contents are page update's).
func (s *Service) pageOffer(ctx context.Context, id string) bool {
	ps, ok := s.pageStore()
	if !ok {
		return false
	}
	_, err := ps.AssistantPageByOffer(ctx, id)
	return err == nil
}

// conversationKeptPages are the ids of the pages conversation id on machine keeps, which go to Recently removed when
// it is deleted.
func (s *Service) conversationKeptPages(ctx context.Context, machine, id string) []string {
	st, ok := s.pageStore()
	if !ok || !s.pagesManager() {
		return nil
	}
	pages, err := st.AssistantPages(ctx, db.AssistantPageQuery{Status: []string{db.AssistantPageActive},
		OwnerKind: db.AssistantPageOwnerConversation, OwnerMachine: machine, OwnerRef: id})
	if err != nil {
		s.logf("assistant pages: the pages of %s: %v", short(id), err)
		return nil
	}
	ids := make([]string, 0, len(pages))
	for _, p := range pages {
		ids = append(ids, p.ID)
	}
	return ids
}

// expirePageOffers says on the cards of drafts that expired that they did, and deletes their held routines.
func (s *Service) expirePageOffers(ctx context.Context, expired []db.AssistantPageRow) {
	for i := range expired {
		s.closePageOffer(ctx, &expired[i], "offer_expired")
		if expired[i].RoutineID != "" {
			s.pageRoutineDeleted(ctx, expired[i].ID, expired[i].RoutineID)
		}
	}
}
