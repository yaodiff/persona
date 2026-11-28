package assistant

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's taps on a page's own buttons (Dashboards, #15017). A tap is checked against the page as stored (only a
// declared action, only the fields it declares, 2000 characters a value, never a secret: viewReplyMessage, as for a
// card), written down with the message its conversation gets (the action's words, the values, and the
// <persona-action view="-pages/pg_…"> tag the apps already hide), and answered at once: Received. The hub's side ends
// there (receipt); the message goes in the background to the conversation that owns the page (delivery), as the owner
// speaking but never interrupting: no ownerInterjects, interject_background false, so it waits for the turn under way.
// A page of the main conversation goes to whichever conversation is the main one now; one whose conversation is gone
// goes to the main one, and its page says so. The footer follows it: Delivered to Persona, Persona is working (that
// conversation's turn started), then nothing (the turn updated the page) or Persona replied in the chat (execution).
// The tap's row times each step; the manager logs each day's taps, queue time and turn wait (AssistantPageTapDays).

// pagesRuntime is the pages' state in the manager: the events waiting to be told, the conversations whose turns are
// followed, the delivery loop's wake-up.
type pagesRuntime struct {
	events pageEvents

	mu sync.Mutex
	// awaiting are the conversations taps were delivered to whose turn has not ended, by canonical machine and
	// conversation, each with the machine as the tap names it; loaded once it was read from the store.
	awaiting map[[2]string]string
	loaded   bool
	wake     chan struct{}
	logged   string // the last day whose taps were logged

	// preempted counts deliveries that would have interrupted a turn: a tap's message never does (it must stay 0).
	preempted atomic.Int64
}

func (r *pagesRuntime) wakeChan() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	return r.wake
}

// kickPages wakes the delivery loop.
func (s *Service) kickPages() {
	select {
	case s.pages.wakeChan() <- struct{}{}:
	default:
	}
}

// tap is the owner tapping one of page p's own actions.
func (r *pageRequest) tap(ctx context.Context, p *db.AssistantPageRow) (extension.ActionResult, error) {
	root, err := wire.ParseAgentPage(p.View)
	if err != nil {
		return r.pageResult(ctx, p, "", pageWord(r.l, "changed"))
	}
	sub := r.state.P
	page := root
	if detail, ok := root.Pages[sub]; ok && sub != "" {
		page = &detail
	} else {
		sub = ""
	}
	var selected *wire.Action
	for _, a := range wire.Actions(page) {
		if a.ID == r.in.Action {
			selected = a
			break
		}
	}
	// An action the page no longer has, or one to confirm on another version than the owner saw: the page as it is now,
	// to look at and confirm again (never sent for them).
	if selected == nil || selected.Confirm != "" && r.state.V != p.Version {
		return r.pageResult(ctx, p, sub, pageWord(r.l, "changed"))
	}
	switch {
	case selected.Open != "":
		if u, err := url.Parse(selected.Open); err == nil && u.Scheme == "https" && u.Host != "" {
			return extension.ActionResult{OpenURL: selected.Open}, nil
		}
		return extension.ActionResult{Toast: pageWord(r.l, "open_link")}, nil
	case selected.Pop:
		if r.version >= 3 {
			return extension.ActionResult{Pop: true}, nil
		}
		return r.pageResult(ctx, p, "", "")
	case selected.Push != nil:
		return r.pageResult(ctx, p, selected.Push.Page, "")
	}
	text, _, err := viewReplyMessage(root, extmgrPagesView(p.ID), wire.ViewActionInput{Action: selected.ID, Page: sub, Values: r.in.Values,
		Confirmed: true, ExpectedVersion: p.Version})
	if err != nil {
		return extension.ActionResult{}, &extmgr.PagesError{Status: http.StatusBadRequest, Code: "bad_request", Err: err}
	}
	a, created, err := r.st.BeginAssistantPageAction(ctx, db.AssistantPageActionRow{ID: db.NewAssistantPageActionID(), PageID: p.ID,
		Action: selected.ID, SubPage: sub, Values: r.in.Values, PageVersion: p.Version, Text: text, MessageID: "page-tap-" + randomHex(16),
		Device: r.device, At: r.now})
	switch {
	case errors.Is(err, db.ErrAssistantPageTapRate):
		return r.pageResult(ctx, p, sub, pageWord(r.l, "tap_rate"))
	case errors.Is(err, db.ErrAssistantPageNotFound), errors.Is(err, db.ErrAssistantPageRemoved), errors.Is(err, db.ErrAssistantPageNotKept):
		return extension.ActionResult{}, pagesRefusal(http.StatusNotFound, "not_found", "not_found")
	case err != nil:
		return extension.ActionResult{}, err
	}
	if created {
		go r.s.deliverPageTap(context.WithoutCancel(ctx), a.ID)
	}
	return r.tapAnswer(ctx, p, sub, a)
}

// tapAnswer is the page with tap a's state in its footer, and Received.
func (r *pageRequest) tapAnswer(ctx context.Context, p *db.AssistantPageRow, sub string, a *db.AssistantPageActionRow) (extension.ActionResult, error) {
	v := drawPage(p, sub, a, r.l, r.loc, r.now, r.s.webBase(ctx))
	if err := r.fit(&v); err != nil {
		return extension.ActionResult{Toast: pageWord(r.l, "received")}, nil
	}
	return extension.ActionResult{View: &v, Toast: pageWord(r.l, "received")}, nil
}

// extmgrPagesView is the view a tap's tag names: the reserved extension and the page.
func extmgrPagesView(id string) string { return "-pages/" + id }

// retry sends tap id of page p again (the owner's Retry on a tap that was not delivered), with the values it had.
func (r *pageRequest) retry(ctx context.Context, p *db.AssistantPageRow, id string) (extension.ActionResult, error) {
	a, err := r.st.AssistantPageAction(ctx, id)
	if err != nil {
		return extension.ActionResult{}, err
	}
	if a == nil || a.PageID != p.ID {
		return r.pageResult(ctx, p, r.state.P, pageWord(r.l, "not_found"))
	}
	if a, err = r.st.RetryAssistantPageAction(ctx, id); err != nil {
		return extension.ActionResult{}, err
	}
	if a.Status == db.AssistantPageTapPending {
		go r.s.deliverPageTap(context.WithoutCancel(ctx), a.ID)
	}
	return r.tapAnswer(ctx, p, r.state.P, a)
}

// pageTapLease is how long a delivery holds a tap.
const pageTapLease = 10 * time.Minute

// deliverPageTap delivers tap id to the conversation that owns its page, unless another delivery holds it.
func (s *Service) deliverPageTap(parent context.Context, id string) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	st, ok := s.pageStore()
	if !ok {
		return
	}
	a, err := st.ClaimAssistantPageDelivery(ctx, id, time.Now().Add(pageTapLease))
	if err != nil || a == nil || a.LeaseUntil == nil {
		return
	}
	d := db.AssistantPageDelivery{ID: a.ID, Lease: *a.LeaseUntil, Status: db.AssistantPageTapPending}
	if p, err := st.AssistantPage(ctx, a.PageID); err != nil {
		d.Error = err.Error()
	} else {
		d = s.sendPageTap(ctx, p, a, d)
	}
	finish, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFinish()
	settled, err := st.SettleAssistantPageDelivery(finish, d)
	if err != nil {
		s.logf("assistant pages: settling tap %s: %v", a.ID, err)
		return
	}
	if settled.Status == db.AssistantPageTapSent {
		s.awaitPageTurn(settled.TargetMachine, settled.TargetConversation)
	}
	if settled.Status != db.AssistantPageTapPending {
		s.pageChanged(settled.PageID, pageStatus)
	}
}

// sendPageTap sends tap a of page p to its conversation (the main one for a page of the main conversation, or for
// one whose conversation is gone), as the owner's message waiting for the turn under way; d is its outcome.
func (s *Service) sendPageTap(ctx context.Context, p *db.AssistantPageRow, a *db.AssistantPageActionRow, d db.AssistantPageDelivery) db.AssistantPageDelivery {
	machine, conversation := p.OwnerMachine, p.OwnerRef
	toMain := p.OwnerKind != db.AssistantPageOwnerConversation
	if !toMain {
		_, found, err := s.pageConversation(ctx, machine, conversation)
		switch {
		case errors.Is(err, ErrNoDevice), err == nil && !found:
			toMain, d.Redirected = true, true // its conversation (or its computer) is gone: the main one takes it
		case err != nil:
			d.Error = i18n.T(s.lang(ctx), "assistant-view_actions.waiting_machine")
			return d
		}
	}
	for attempt := 0; ; attempt++ {
		if toMain {
			cfg := s.settings(ctx)
			machine, conversation = cfg.mainRouteMachine(), cfg.Main
			if conversation == "" || machine == "" {
				d.Status, d.Error = db.AssistantPageTapFailed, "no main conversation"
				return d
			}
		}
		params, err := s.pageTapParams(ctx, machine, conversation, a)
		if err == nil {
			if interject, _ := params["interject_background"].(bool); interject {
				s.pages.preempted.Add(1) // counted, so the daily line would show it: never (pageTapParams)
			}
			err = s.sendOnce(ctx, machine, params)
		}
		switch {
		case err == nil:
			d.Status, d.At = db.AssistantPageTapSent, time.Now()
			d.TargetMachine, d.TargetConversation = machine, conversation
			return d
		case errors.Is(err, apierr.ErrOffline) || errors.Is(err, ErrNoDevice):
			d.Error = err.Error()
			return d
		case !toMain && attempt == 0 && pageConversationGone(err, conversation):
			toMain, d.Redirected = true, true
			continue
		}
		d.Status, d.Error = db.AssistantPageTapFailed, err.Error()
		return d
	}
}

// pageTapParams are the session.send_once parameters of tap a's message to conversation: the owner's own (what it runs
// on, and they spoke), queued after the turn under way: interject_background is false whatever the owner's interject
// setting says, and the owner's preemption of a background round (ownerInterjects) is never asked.
func (s *Service) pageTapParams(ctx context.Context, machine, conversation string, a *db.AssistantPageActionRow) (map[string]any, error) {
	params := map[string]any{"session_id": conversation, "client_msg_id": a.MessageID, "text": a.Text, "resume_only": true}
	if err := s.sendModel(ctx, machine, conversation, params, s.lang(ctx)); err != nil {
		return nil, err
	}
	params["interject_background"] = false // sendModel set it when the owner moves work aside for their messages
	s.UserSpoke(ctx, SessionRef{MachineID: machine, SessionID: conversation})
	s.detachPendingApprovalsOnOwnerMessage(machine, conversation)
	return params, nil
}

// pageConversation is conversation id on machine: found false when the machine answered it has none, an error when it
// did not answer.
func (s *Service) pageConversation(ctx context.Context, machine, id string) (proto.Session, bool, error) {
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, machine, DeviceSessionGet, map[string]any{"session_id": id}, &res); err != nil {
		return proto.Session{}, false, err
	}
	return res.Session, res.OK && res.Session.Assistant, nil
}

// pageConversationGone: the send was refused before delivery because the conversation is gone (deleted, or its
// transcript cleaned up: node "conversation … is gone").
func pageConversationGone(err error, id string) bool {
	return errors.Is(err, db.ErrConversationDeleted) || strings.HasSuffix(err.Error(), "conversation "+id+" is gone")
}

// ---- the turns that answer taps ----

func (s *Service) canonicalMachine(machine string) string {
	if s.devs != nil {
		return s.devs.Canonical(machine)
	}
	return machine
}

// awaitPageTurn: a tap was delivered to conversation on machine; its turns are followed.
func (s *Service) awaitPageTurn(machine, conversation string) {
	r := &s.pages
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.awaiting == nil {
		r.awaiting = map[[2]string]string{}
	}
	r.awaiting[[2]string{s.canonicalMachine(machine), conversation}] = machine
}

// loadPageTurns reads which conversations' turns are followed (after a start).
func (s *Service) loadPageTurns(ctx context.Context, st pageStore) {
	targets, err := st.AssistantPageTurnTargets(ctx)
	if err != nil {
		return
	}
	r := &s.pages
	r.mu.Lock()
	defer r.mu.Unlock()
	r.awaiting = map[[2]string]string{}
	for _, t := range targets {
		r.awaiting[[2]string{s.canonicalMachine(t.Machine), t.Conversation}] = t.Machine
	}
	r.loaded = true
}

// pageTurnMachine is the machine taps to conversation name, when its turns are followed.
func (s *Service) pageTurnMachine(machine, conversation string) (string, bool) {
	r := &s.pages
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.awaiting[[2]string{s.canonicalMachine(machine), conversation}]
	return stored, ok
}

// pageTurnStarted: conversation started a turn; the taps delivered to it are being handled (its pages say so).
func (s *Service) pageTurnStarted(machine, conversation string, at time.Time) {
	s.markPageTurn(machine, conversation, at, true)
}

// pageTurnEnded: conversation's turn ended; the taps it took up are answered.
func (s *Service) pageTurnEnded(machine, conversation string, at time.Time) {
	s.markPageTurn(machine, conversation, at, false)
}

func (s *Service) markPageTurn(machine, conversation string, at time.Time, started bool) {
	stored, ok := s.pageTurnMachine(machine, conversation)
	if !ok {
		return
	}
	st, ok := s.pageStore()
	if !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		mark := st.MarkAssistantPageTurnEnded
		if started {
			mark = st.MarkAssistantPageTurnStarted
		}
		taps, err := mark(ctx, stored, conversation, at)
		if err != nil {
			s.logf("assistant pages: the turn of %s: %v", short(conversation), err)
			return
		}
		var pages []string
		for _, a := range taps {
			if !slices.Contains(pages, a.PageID) {
				pages = append(pages, a.PageID)
			}
		}
		for _, id := range pages {
			s.pageChanged(id, pageStatus)
		}
	}()
}

// ---- the loop ----

const (
	pageLoopEvery  = 20 * time.Second
	pageCleanEvery = time.Hour
)

// pagesLoop delivers the taps waiting (on a tick, or at once when woken), and keeps the pages' tables: drafts past
// their week removed, pages removed 30 days deleted, old taps trimmed, each day's taps logged. Not on the standby
// process of a blue-green switch.
func (s *Service) pagesLoop(ctx context.Context) {
	ticker := time.NewTicker(pageLoopEvery)
	defer ticker.Stop()
	var cleaned time.Time
	for {
		if st, ok := s.pageStore(); ok && !s.hello(ctx).Assistant.Standby {
			s.pages.mu.Lock()
			loaded := s.pages.loaded
			s.pages.mu.Unlock()
			if !loaded {
				s.loadPageTurns(ctx, st)
			}
			if pending, err := st.PendingAssistantPageActions(ctx, 20); err == nil {
				for _, a := range pending {
					s.deliverPageTap(ctx, a.ID)
				}
			}
			if time.Since(cleaned) >= pageCleanEvery {
				s.cleanPages(ctx, st, time.Now())
				cleaned = time.Now()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.pages.wakeChan():
		}
	}
}

// cleanPages is the pages' upkeep at now, and the log of the days before today not logged yet.
func (s *Service) cleanPages(ctx context.Context, st pageStore, now time.Time) {
	if expired, err := st.ExpireAssistantDrafts(ctx, now); err != nil {
		s.logf("assistant pages: expiring drafts: %v", err)
	} else if len(expired) > 0 {
		s.logf("assistant pages: %d drafts expired", len(expired))
		s.expirePageOffers(ctx, expired) // their cards say Expired (pages_tool.go)
	}
	if n, routines, err := st.PurgeAssistantPagesWithRoutines(ctx, now); err != nil {
		s.logf("assistant pages: purging: %v", err)
	} else {
		for _, routine := range routines {
			s.pageRoutineDeleted(ctx, "", routine) // their routines go with them
		}
		if n > 0 {
			s.pagesListChanged(ctx) // Recently removed lost them
		}
	}
	if _, err := st.TrimAssistantPageActions(ctx, now); err != nil {
		s.logf("assistant pages: trimming taps: %v", err)
	}
	s.loadPageTurns(ctx, st)
	s.logPageTaps(ctx, st, now)
}

// logPageTaps logs yesterday's taps (once a day): how many, the queue time (tap to delivered) and the turn wait
// (delivered to the turn that took them up), and the deliveries that would have interrupted a turn (always 0).
func (s *Service) logPageTaps(ctx context.Context, st pageStore, now time.Time) {
	zone := s.settings(ctx).Timezone // a name the database knows too; "Local" is Go's alone
	loc, err := time.LoadLocation(zone)
	if zone == "" || err != nil {
		zone, loc = "UTC", time.UTC
	}
	today := now.In(loc)
	yesterday := time.Date(today.Year(), today.Month(), today.Day()-1, 0, 0, 0, 0, loc)
	day := yesterday.Format(time.DateOnly)
	s.pages.mu.Lock()
	done := s.pages.logged == day
	s.pages.logged = day
	s.pages.mu.Unlock()
	if done {
		return
	}
	days, err := st.AssistantPageTapDays(ctx, yesterday, zone)
	if err != nil {
		s.logf("assistant pages: the taps of %s: %v", day, err)
		return
	}
	for _, d := range days {
		if d.Day.In(loc).Format(time.DateOnly) != day {
			continue
		}
		s.logf("%s", pageTapDayLine(day, d, s.pages.preempted.Load()))
	}
}

func pageTapDayLine(day string, d db.AssistantPageTapDay, preempted int64) string {
	return fmt.Sprintf("assistant pages: taps on %s: %d (delivered %d, redirected %d, failed %d); queue time p50 %v p95 %v max %v; "+
		"turn wait (%d started) p50 %v p95 %v max %v; preempted turns %d", day, d.Taps, d.Delivered, d.Redirected, d.Failed,
		d.QueueP50, d.QueueP95, d.QueueMax, d.Started, d.WaitP50, d.WaitP95, d.WaitMax, preempted)
}
