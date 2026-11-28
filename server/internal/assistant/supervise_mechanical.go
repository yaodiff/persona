package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Mechanical waits are screened before routing, including before starting a background supervisor.
// Restoring a scheduled wake's signal (rather than marking it told) keeps an expired or cancelled wake retryable.
func (s *Service) screenMechanicalSignals(ctx context.Context, list []superviseSignal, now, prev time.Time) []superviseSignal {
	var quiet []superviseSignal
	list = slices.DeleteFunc(list, func(g superviseSignal) bool {
		if !s.routineSignalSuppressed(g, now) {
			return false
		}
		quiet = append(quiet, g)
		return true
	})
	if len(quiet) > 0 {
		s.turnsTold(ctx, quiet)
		s.ackSupervisePending(quiet)
		s.logf("supervise: %d routine signal(s) suppressed by task state or recent owner input", len(quiet))
	}
	actionable := map[string]bool{}
	for _, g := range list {
		switch g.Kind {
		case sigIdle, sigStalled, sigOrphaned, sigAllStopped:
		default:
			actionable[g.Todo] = true
		}
	}
	var kept []superviseSignal
	for _, g := range list {
		if actionable[g.Todo] {
			// An actionable sibling keeps the to-do out of mechanical parking, but its idle still needs the read-only wake check.
			if g.Kind == sigIdle {
				if _, retry := s.mechanicalCheck(ctx, g, now, true); !retry.IsZero() {
					s.restoreScreened(g, now, prev)
					continue
				}
			}
			kept = append(kept, g)
			continue
		}
		handled, retry := s.mechanicalSignal(ctx, g, now)
		switch {
		case !retry.IsZero():
			s.restoreScreened(g, now, prev)
		case handled:
			s.turnsTold(ctx, []superviseSignal{g})
			s.logf("supervise: task %s %s handled without a model", g.Todo, g.Kind)
		default:
			if t, ok := s.todoNow(g.Todo); ok {
				if a := lastAutomatic(t.Supervision); a != nil && a.Count >= maxAutomaticParks {
					g.Detail += " Automatic-parking limit reached (three unchanged attempts)."
				}
			}
			kept = append(kept, g)
		}
	}
	if len(kept) == 0 {
		b := &s.supervise
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	return kept
}

// screenFinalWakeSignals rechecks only wake promises after routing and recipient batches have merged their final lists.
func (s *Service) screenFinalWakeSignals(ctx context.Context, list []superviseSignal, now, prev time.Time) []superviseSignal {
	kept, deferred := s.partitionFinalWakeSignals(ctx, list, now, "final")
	for _, g := range deferred {
		s.restoreScreened(g, now, prev)
	}
	return kept
}

// partitionFinalWakeSignals separates deferred idles without restoring them. Queue drain restores only after its
// rewritten durable queue no longer contains those idles; ordinary final delivery restores them immediately.
func (s *Service) partitionFinalWakeSignals(ctx context.Context, list []superviseSignal, now time.Time, boundary string) (kept, deferred []superviseSignal) {
	kept = list[:0]
	for _, g := range list {
		if g.Kind == sigIdle {
			if _, retry := s.mechanicalCheckAt(ctx, g, now, true, boundary); !retry.IsZero() {
				deferred = append(deferred, g)
				continue
			}
		}
		kept = append(kept, g)
	}
	return kept, deferred
}

// restoreScreened keeps a deferred signal out of the digest batch that screening just kept it out of.
func (s *Service) restoreScreened(g superviseSignal, now, prev time.Time) {
	g.batchTo, g.batchUntil = "", time.Time{}
	s.restoreDigest([]superviseSignal{g}, now, prev)
}

// futureNativeWake returns the earliest live native wakeup when every current agent has one and a stopped-state
// conflict would otherwise hide it. Queued input, offline and read-only agents always fail open. Clean stops keep the
// ordinary Work and delivery checks. The conflict is diagnostic: it names why this idle would have needed supervision.
func futureNativeWake(agents []proto.Session, now time.Time) (time.Time, string, bool) {
	var wake time.Time
	conflict := "none"
	suppress := false
	for _, c := range agents {
		if c.Status != proto.StatusDone || c.ReadOnlyReason != "" || len(c.Queued) > 0 ||
			c.Loop == nil || c.Loop.Kind != "wakeup" || !c.Loop.NextAt.After(now) {
			return time.Time{}, "none", false
		}
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 || c.TurnError != "" {
			suppress = true
			if conflict == "none" {
				conflict = nativeWakeConflict(c)
			}
		}
		if wake.IsZero() || c.Loop.NextAt.Before(wake) {
			wake = c.Loop.NextAt
		}
	}
	return wake, conflict, suppress
}

func nativeWakeConflict(c proto.Session) string {
	switch {
	case c.State == proto.StateNeedsYou || c.PendingInteractions > 0:
		return "needs_you"
	case c.TurnError != "":
		return "turn_error"
	case len(c.Queued) > 0:
		return "queued"
	default:
		return "none"
	}
}

// Unknown, actionable and conflicting evidence deliberately falls through to ordinary supervision.
func (s *Service) mechanicalSignal(ctx context.Context, g superviseSignal, now time.Time) (bool, time.Time) {
	return s.mechanicalCheckAt(ctx, g, now, false, "first")
}

// mechanicalCheck is the first-boundary form used by mixed actionable batches.
func (s *Service) mechanicalCheck(ctx context.Context, g superviseSignal, now time.Time, wakeOnly bool) (bool, time.Time) {
	return s.mechanicalCheckAt(ctx, g, now, wakeOnly, "first")
}

func (s *Service) logWakeWithheld(g superviseSignal, boundary string, wake time.Time, conflict string) {
	s.logf("supervise: idle withheld task=%s boundary=%s wake=%s conflict=%s", g.Todo, boundary, wake.Format(time.RFC3339Nano), conflict)
}

// mechanicalCheckAt is mechanicalSignal; wakeOnly stops before delivery reads, writes or automatic parking.
func (s *Service) mechanicalCheckAt(ctx context.Context, g superviseSignal, now time.Time, wakeOnly bool, boundary string) (bool, time.Time) {
	switch g.Kind {
	case sigIdle, sigStalled, sigOrphaned, sigAllStopped:
	default:
		return false, time.Time{}
	}
	if g.Kind == sigIdle && g.Handoff {
		return false, time.Time{}
	}
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil || t.Supervision == nil || g.Ask != "" {
		return false, time.Time{}
	}
	v := t.Supervision
	st := v.state(t)
	if st != "" && st != reviewRework || v.Waits != nil || v.AutoReview != nil && v.AutoReview.State == autoRunning {
		return false, time.Time{}
	}
	agents, reached := s.taskAgentsReached(ctx, t)
	if !reached {
		return false, time.Time{}
	}
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	for _, c := range agents {
		if g.Kind == sigIdle && c.Loop != nil && c.Loop.Kind == "wakeup" &&
			(c.Status == proto.StatusWorking || c.State == proto.StateWorking) {
			s.logWakeWithheld(g, boundary, c.Loop.NextAt, nativeWakeConflict(c))
			return false, now // the wakeup's turn is running; its end will judge the stop afresh
		}
	}
	if g.Kind == sigIdle {
		if wake, conflict, ok := futureNativeWake(agents, now); ok {
			s.logWakeWithheld(g, boundary, wake, conflict)
			return false, wake
		}
	}
	agents, wake, ok := s.mechanicalAgents(ctx, t, agents, now)
	if !ok {
		return false, time.Time{}
	}
	if wakeOnly {
		if g.Kind == sigIdle && !wake.IsZero() {
			s.logWakeWithheld(g, boundary, wake, "none")
		}
		return false, wake
	}
	if wake.IsZero() && len(agents) == 1 && s.parkCloseout(ctx, t, agents[0], now) {
		return true, time.Time{}
	}
	if st != "" {
		return false, time.Time{} // only evidenced closeouts extend the existing mechanical path to rework
	}
	if g.Kind == sigAllStopped {
		return false, wake // other all-stopped handling retains its existing group checks
	}
	handled, retry := s.mechanicalPR(ctx, t, agents, now, wake)
	if g.Kind == sigIdle && !retry.IsZero() {
		s.logWakeWithheld(g, boundary, retry, "none")
	}
	return handled, retry
}

// Callers establish reachability; both paths require the same safely stopped inventory.
func (s *Service) mechanicalAgents(ctx context.Context, t Todo, agents []proto.Session, now time.Time) ([]proto.Session, time.Time, bool) {
	v := t.Supervision
	st := v.state(t)
	if st != "" && st != reviewRework || v.Waits != nil || v.AutoReview != nil && v.AutoReview.State == autoRunning {
		return nil, time.Time{}, false
	}
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	if len(agents) == 0 {
		return nil, time.Time{}, false
	}
	for _, c := range agents {
		if c.Status != proto.StatusDone || c.State == proto.StateWorking || c.State == proto.StateNeedsYou ||
			c.PendingInteractions > 0 || len(c.Queued) > 0 || c.TurnError != "" || c.ReadOnlyReason != "" {
			return nil, time.Time{}, false
		}
	}
	if !s.loadWork(ctx) {
		return nil, time.Time{}, false
	}
	for _, c := range agents {
		if e, exists := s.workEntry([2]string{c.MachineID, c.SessionID}); exists && sameTurn(c, e.TurnAt) &&
			(e.Work.On == proto.WaitUser || e.Work.Phase == proto.WorkWorking ||
				e.Work.Closeout != nil && closeoutDisproved(*e.Work.Closeout)) {
			return nil, time.Time{}, false
		}
	}

	// A wake is only a promise while the switch is on, its durable row is current, and its time is in the future.
	var wake time.Time
	wakes := s.triageSwitches(ctx)
	for _, c := range agents {
		var next time.Time
		if c.Loop != nil && c.Loop.Kind == "wakeup" && c.Loop.NextAt.After(now) {
			next = c.Loop.NextAt
		} else if wakes.Enabled && wakes.Wake {
			e, exists := s.workEntry([2]string{c.MachineID, c.SessionID})
			if !exists || !sameTurn(c, e.TurnAt) || e.Work.Phase != proto.WorkBlocked ||
				(e.Work.On != proto.WaitExternal && e.Work.On != proto.WaitAgent) ||
				(e.Work.On == proto.WaitAgent && e.Work.Agent == nil) ||
				e.WakeAt == nil || !e.WakeAt.After(now) || e.InteractionID != "" || e.Work.RetiredAt != nil {
				wake = time.Time{}
				break
			}
			next = *e.WakeAt
		}
		if next.IsZero() {
			wake = time.Time{}
			break
		}
		if wake.IsZero() || next.Before(wake) {
			wake = next
		}
	}
	return agents, wake, true
}

func (s *Service) mechanicalPR(ctx context.Context, t Todo, agents []proto.Session, now time.Time, wake time.Time) (bool, time.Time) {
	if t.hasTaskRuntime() {
		return false, wake // PR and deployment observations already update the stage ledger
	}
	d := t.Delivery
	if d == nil || d.Unblock != unblockOther || d.Gate == "" || d.At.IsZero() ||
		(d.State != deliveryPR && d.State != deliveryMerged) || t.PRURL == "" || len(agents) != 1 {
		return false, wake
	}
	c := agents[0]
	if c.Cwd == "" || d.At.Before(c.UpdatedAt) {
		return false, wake
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var res struct {
		PR *struct {
			URL, State, Review, Mergeable string
			HeadOID                       string `json:"head_oid"`
			ChecksState                   string `json:"checks_state"`
			MergeState                    string `json:"merge_state"`
			Draft                         bool
			ChecksComplete                bool       `json:"checks_complete"`
			ReviewsComplete               bool       `json:"reviews_complete"`
			ThreadsComplete               bool       `json:"threads_complete"`
			LastReviewAt                  *time.Time `json:"last_review_at"`
			Threads                       []struct{}
		} `json:"pr"`
	}
	if err := s.call(qctx, c.MachineID, "git.pr", map[string]any{"cwd": c.Cwd}, &res); err != nil {
		return false, time.Time{}
	}
	p := res.PR
	if p == nil || p.URL != t.PRURL || p.HeadOID == "" || p.Draft || !p.ChecksComplete || !p.ReviewsComplete ||
		!p.ThreadsComplete || len(p.Threads) > 0 || p.Review == "CHANGES_REQUESTED" || p.Mergeable == "CONFLICTING" ||
		p.MergeState == "DIRTY" || p.State == "OPEN" && p.Mergeable != "MERGEABLE" ||
		p.ChecksState == "failing" || p.LastReviewAt != nil && p.LastReviewAt.After(d.At) {
		return false, time.Time{}
	}
	// Read again after the remote check: a new turn or another review must win over this old wait.
	cur, exists := s.todoNow(t.ID)
	latest, present := s.session(ctx, c.MachineID, c.SessionID)
	sameAgents := slices.EqualFunc(cur.Agents, t.Agents, func(a, b TodoAgent) bool {
		return a.MachineID == b.MachineID && a.SessionID == b.SessionID &&
			(a.ReleasedAt == nil) == (b.ReleasedAt == nil) && (a.DismissedAt == nil) == (b.DismissedAt == nil)
	})
	if !exists || cur.DoneAt != nil || cur.PRURL != t.PRURL || !sameAgents ||
		cur.Supervision == nil || !cur.Supervision.UpdatedAt.Equal(t.Supervision.UpdatedAt) ||
		!present || latest.Status != proto.StatusDone || !sameTurn(latest, c.UpdatedAt) ||
		latest.PendingInteractions > 0 || len(latest.Queued) > 0 || latest.TurnError != "" {
		return false, time.Time{}
	}
	switch p.State {
	case "OPEN":
		if !wake.IsZero() {
			return false, wake // parking would disable the agent's already scheduled follow-up
		}
		if d.State != deliveryPR {
			return false, time.Time{} // conflicting delivery evidence needs judgment
		}
		// A ready/open PR does not prove anyone requested a named reviewer. Leave the signal with the supervisor
		// until it can supply the actual contact receipt through task_review; do not synthesize evidence.
		return false, time.Time{}
	case "MERGED":
		if p.ChecksState != "passing" {
			return false, time.Time{}
		}
		if d.State == deliveryMerged {
			if !wake.IsZero() {
				return false, wake
			}
			// A delivery gate alone does not identify an observable external event and its wake condition.
			return false, time.Time{}
		}
		// Merge/CI proves a delivery stage, not deployment, live verification or acceptance. If no wake owns the
		// next step the supervisor still hears of it; otherwise the recorded follow-up proceeds without a model wake.
		if !s.writeTaskDelivery(ctx, t.ID, TodoDelivery{State: deliveryMerged, Gate: i18n.T(s.lang(ctx),
			"assistant-todos.supervision_needs_deployment"), Unblock: unblockAgent, At: now.UTC()}, true) {
			return false, time.Time{}
		}
		return false, wake
	}
	return false, time.Time{}
}
