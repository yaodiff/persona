package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// briefDirections reads the same task, review, queue, session and interaction ledgers as supervision. It does not ask
// a model to infer why work stopped. A task with no dispatch attempt is counted only; a queue is an attempt even before
// its first agent exists. Tasks whose supervisor is not a current direction (notably the background supervisor) belong
// to main, so leader-dispatched and main-dispatched work are both covered.
func (s *Service) briefDirections(ctx context.Context, now time.Time, l i18n.Lang, web string) ([]prompts.BriefDirection, []prompts.BriefDisabledRoutine) {
	cfg := s.settings(ctx)
	waits, ok := s.briefingWaits(cfg)
	if !ok { // the briefing module is off: no wait rows to work out
		return nil, nil
	}
	ids := make([]string, 0, len(cfg.Leaders)+1)
	if cfg.Main != "" {
		ids = append(ids, cfg.Main)
	}
	for _, id := range cfg.Leaders {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, s.briefDisabledRoutines(ctx, now, l, nil)
	}
	names := s.leaderNames(ctx, ids)
	if cfg.Main != "" {
		names[cfg.Main] = i18n.T(l, "assistant-feishu_cards.main_chat")
	}
	loc := s.location(ctx)
	dirs := make([]prompts.BriefDirection, len(ids))
	byID := make(map[string]int, len(ids))
	for i, id := range ids {
		dirs[i] = prompts.BriefDirection{ID: id, Name: names[id], Owner: id == cfg.Main, Link: sessionLink(web, cfg.MachineID, id, true)}
		byID[id] = i
	}
	directionOf := func(t Todo) (int, bool) {
		id := t.Origin
		if t.Supervision != nil && t.Supervision.Supervisor != "" {
			id = t.Supervision.Supervisor
		}
		if i, ok := byID[id]; ok {
			return i, true
		}
		i, ok := byID[cfg.Main]
		return i, ok && (t.Supervision != nil || t.Origin != "")
	}

	queued := map[string]*TaskQueue{}
	for _, p := range s.queueCandidatePlans(ctx) {
		if p.State != planStarted {
			continue
		}
		for _, pt := range p.Tasks {
			if pt.Queue != nil && pt.TodoID != "" {
				q := *pt.Queue
				queued[pt.TodoID] = &q
			}
		}
	}

	// Cards are mapped only through a real open task/session link. A card in a leader or main chat without such a link
	// stays at direction level; nothing guesses a task from its title. Private cards never contribute title or reason.
	cards := map[string][]proto.Interaction{}
	var loose []proto.Interaction
	for _, ix := range s.pendingInteractions() {
		if s.interactionEnded(ix.ID) || now.Sub(ix.CreatedAt) < waits.CardAfter() {
			continue
		}
		if t, ok := s.todoOfSession(ctx, ix.MachineID, ix.SessionID); ok {
			cards[t.ID] = append(cards[t.ID], ix)
		} else if _, ok := byID[ix.SessionID]; ok {
			loose = append(loose, ix)
		}
	}

	todos := s.todosOf(ctx, "*")
	lists := machineSessions{}
	reachable := map[string]bool{}
	for _, d := range s.devices(ctx) {
		reachable[d.ID] = d.Reachable
	}
	orphanLists, orphanFailed := map[string]map[string]proto.Session{}, map[string]bool{}
	for _, t := range todos {
		if t.DoneAt != nil {
			continue
		}
		i, ok := directionOf(t)
		if !ok {
			continue
		}
		d := &dirs[i]
		d.Open++
		queue := queued[t.ID]
		if len(t.Agents) == 0 && queue == nil {
			d.Undispatched++
			continue
		}
		agents := s.taskAgentsIn(ctx, t, lists)
		w, stopped := s.briefTaskWait(ctx, t, agents, cards[t.ID], queue, now, waits, reachable, orphanLists, orphanFailed)
		if !stopped {
			d.Active++
			continue
		}
		w.Link = todoLink(web, t.ID)
		if w.Kind == "private" && w.InteractionID != "" {
			// A task-linked ordinary card still opens its to-do. A Private card keeps only its conversation link, never a
			// URL that would reveal the task identity it deliberately omits.
			ix := cards[t.ID][0]
			for _, candidate := range cards[t.ID] {
				if candidate.ID == w.InteractionID {
					ix = candidate
					break
				}
			}
			w.Link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		}
		w.Line = briefWaitLine(l, w, now, loc)
		d.Waits = append(d.Waits, w)
	}
	for _, ix := range loose {
		i := byID[ix.SessionID]
		w := briefCardWait(ix, l)
		w.Link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		w.Line = briefWaitLine(l, w, now, loc)
		dirs[i].Waits = append(dirs[i].Waits, w)
	}
	for i := range dirs {
		waits.FoldDirection(&dirs[i])
	}
	return dirs, s.briefDisabledRoutines(ctx, now, l, names)
}

func (s *Service) briefTaskWait(ctx context.Context, t Todo, agents []proto.Session, cards []proto.Interaction, queue *TaskQueue,
	now time.Time, waits modapi.WaitRules, reachable map[string]bool, lists map[string]map[string]proto.Session, failed map[string]bool) (prompts.BriefWait, bool) {
	base := prompts.BriefWait{TodoID: t.ID, Title: clip(oneLine(t.Title), 80)}
	v := t.Supervision
	if v != nil {
		switch v.state(t) {
		case reviewNeedsUser:
			base.Kind, base.WaitingOn, base.Since = "needs_user", "owner", v.UpdatedAt
			if len(v.Reviews) > 0 {
				r := v.Reviews[len(v.Reviews)-1]
				base.Reason = clip(oneLine(r.Reason), 120)
				if base.Reason == "" {
					base.Reason = clip(oneLine(r.Note), 120)
				}
			}
			return base, true
		case reviewPending:
			base.Kind, base.WaitingOn, base.Since = "review", "supervisor", v.UpdatedAt
			if v.PendingSince != nil {
				base.Since = *v.PendingSince
			}
			return base, true
		case reviewParked:
			if p := v.parking(); p != nil {
				base.Kind, base.WaitingOn, base.Since, base.Until, base.Reason = "parked", p.WaitingOn, p.At, p.Until, clip(oneLine(p.Reason), 120)
				return base, true
			}
		}
	}
	if len(cards) > 0 {
		slices.SortFunc(cards, func(a, b proto.Interaction) int { return a.CreatedAt.Compare(b.CreatedAt) })
		w := briefCardWait(cards[0], s.botLang(ctx))
		if w.Kind != "private" {
			w.TodoID, w.Title = base.TodoID, base.Title
		}
		return w, true
	}
	if queue != nil {
		until := queue.NextAt
		if queue.Key == workspaceQueueKey {
			until = time.Time{}
		}
		base.Kind, base.WaitingOn, base.Since, base.Until, base.Reason = "capacity", "capacity", queue.QueuedAt, until, clip(oneLine(queue.Reason), 120)
		return base, true
	}
	if len(t.Agents) > 0 {
		if oa, known := s.orphanAgents(ctx, t, reachable, lists, failed); known {
			if o, orphan := taskOrphan(t, oa, now); orphan {
				since := now
				if old, ok := s.orphanOf(t.ID); ok && !old.Since.IsZero() {
					since = old.Since
				} else {
					s.orphans.mu.Lock()
					if at := s.orphans.seen[t.ID]; !at.IsZero() {
						since = at
					}
					s.orphans.mu.Unlock()
				}
				if o.Reason != orphanOffline {
					if since.Equal(now) {
						// A closed/error session already has a real last-activity timestamp. On first discovery there
						// is no earlier orphan mark, so do not rewrite a long-finished run as "stopped just now".
						var last time.Time
						for _, c := range agents {
							at := c.UpdatedAt
							if c.ActivityAt != nil && c.ActivityAt.After(at) {
								at = *c.ActivityAt
							}
							if at.After(last) {
								last = at
							}
						}
						if !last.IsZero() && last.Before(since) {
							since = last
						}
					}
					base.Kind, base.WaitingOn, base.Since, base.Reason = "orphaned", "technical", since, o.Reason
					return base, true
				}
				if now.Sub(since) >= waits.OrphanAfter() {
					base.Kind, base.WaitingOn, base.Since, base.Reason = "orphaned", "technical", since, o.Reason
					return base, true
				}
			}
		}
	}
	if brief, ok := s.briefOfTodo(t.ID); ok && s.briefTip(brief.ID) == t.ID && !brief.DueAt.IsZero() && !now.Before(brief.DueAt) {
		base.Kind, base.WaitingOn, base.Since = "overdue", "supervisor", brief.DueAt
		return base, true
	}
	expect := 0
	if v != nil {
		expect = v.ExpectMinutes
	}
	var latest proto.Session
	var moved time.Time
	for _, c := range agents {
		at := c.UpdatedAt
		if c.ActivityAt != nil && c.ActivityAt.After(at) {
			at = *c.ActivityAt
		}
		if at.After(moved) {
			latest, moved = c, at
		}
	}
	// The task is as live as its most recently active agent. An old stopped session must not make a task with another
	// active agent look stalled; the threshold (working vs. silent) comes from that latest session's state.
	if !moved.IsZero() {
		after := waits.SilentAfter(expect)
		if latest.State == proto.StateWorking {
			after = waits.StalledAfter(expect)
		}
		if now.Sub(moved) >= after {
			base.Kind, base.WaitingOn, base.Since = "stalled", "technical", moved
			base.Reason = clip(oneLine(latest.Activity), 120)
			return base, true
		}
	}
	// Every agent was released or dismissed: this was dispatched, but nobody reopened it. The ordinary orphan pass
	// sees no active agent, so keep the historical stop visible instead of counting it as active work.
	if len(agents) == 0 && len(t.Agents) > 0 {
		var stoppedAt time.Time
		for _, a := range t.Agents {
			at := a.LinkedAt
			if a.ReleasedAt != nil {
				at = *a.ReleasedAt
			} else if a.DismissedAt != nil {
				at = *a.DismissedAt
			}
			if at.After(stoppedAt) {
				stoppedAt = at
			}
		}
		if !stoppedAt.IsZero() && now.Sub(stoppedAt) >= waits.SilentAfter(expect) {
			base.Kind, base.WaitingOn, base.Since, base.Reason = "stalled", "technical", stoppedAt, orphanClosed
			return base, true
		}
	}
	return prompts.BriefWait{}, false
}

func briefCardWait(ix proto.Interaction, l i18n.Lang) prompts.BriefWait {
	w := prompts.BriefWait{InteractionID: ix.ID, Since: ix.CreatedAt, WaitingOn: "owner"}
	if ix.Private {
		w.Kind = "private"
		w.Title = i18n.T(l, "assistant-briefing.private_card")
		return w
	}
	w.Kind = "question"
	if ix.Kind == proto.IxPermission {
		w.Kind = "permission"
	}
	w.Title = clip(oneLine(ix.TaskTitle), 80)
	if w.Title == "" {
		w.Title = i18n.T(l, "assistant-feishu_cards.board_from_persona")
	}
	w.Reason = boardSummary(l, ix)
	return w
}

func briefWaitLine(l i18n.Lang, w prompts.BriefWait, now time.Time, loc *time.Location) string {
	line := i18n.T(l, "assistant-briefing.wait_line", i18n.Args{"title": w.Title,
		"kind": i18n.T(l, "assistant-briefing.wait_kind_"+w.Kind), "wait": boardWait(l, boardWaited(now, w.Since))})
	if !w.Until.IsZero() {
		line = i18n.T(l, "assistant-briefing.wait_until", i18n.Args{"line": line, "time": w.Until.In(loc).Format("2006-01-02 15:04")})
	}
	if w.Reason != "" && w.Kind != "private" {
		line += " — " + w.Reason
	}
	return line
}

// briefDisabledRoutines includes disabled Facts/Report routines (the daily reports) and interval/cron patrols. Legacy
// disabled tasks have no DisabledAt: their line says so, never substituting LastRun. Ordinary one-off follow-ups and
// unrelated daily reminders stay out.
func (s *Service) briefDisabledRoutines(ctx context.Context, now time.Time, l i18n.Lang, names map[string]string) []prompts.BriefDisabledRoutine {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return nil
	}
	defer sc.mu.Unlock()
	cfg := s.settings(ctx)
	web := s.webBase(ctx)
	var out []prompts.BriefDisabledRoutine
	for _, t := range sc.tasks {
		if t.Enabled || t.Repeat == repeatOnce {
			continue
		}
		kind := ""
		switch {
		case t.Facts || t.Report:
			kind = "report"
		case t.Repeat == repeatInterval || t.Repeat == repeatCron:
			kind = "patrol"
		default:
			continue
		}
		direction := t.SessionID
		if direction == "" {
			direction = cfg.Main
		}
		name := names[direction]
		if name == "" {
			name = direction
		}
		r := prompts.BriefDisabledRoutine{ID: t.ID, Title: t.labelIn(l), Direction: name, Kind: kind,
			Link: sessionLink(web, t.RunMachineID, t.RunSessionID, true)}
		if !t.DisabledAt.IsZero() {
			at := t.DisabledAt
			r.DisabledAt = &at
			r.Line = i18n.T(l, "assistant-briefing.disabled_line", i18n.Args{"title": r.Title, "direction": r.Direction,
				"wait": boardWait(l, boardWaited(now, at))})
		} else {
			r.Line = i18n.T(l, "assistant-briefing.disabled_unknown", i18n.Args{"title": r.Title, "direction": r.Direction})
		}
		out = append(out, r)
	}
	slices.SortFunc(out, func(a, b prompts.BriefDisabledRoutine) int {
		if c := strings.Compare(a.Direction, b.Direction); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
