package assistant

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	maxAutomaticParks    = 3
	closeoutBackfillMost = 4
	sigParkedSummary     = "parked_summary"
)

type automaticPark struct {
	Dependency string    `json:"dependency"`
	Turn       string    `json:"turn"`
	Count      int       `json:"count"`
	FirstAt    time.Time `json:"first_at"`
	SummaryDay string    `json:"summary_day,omitempty"`
	SummaryTo  string    `json:"summary_to,omitempty"`
}

var closeoutDate = lazyre.New(`\b\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}:\d{2}(?:Z|[+-]\d{2}:\d{2}))?\b`)
var closeoutSource = lazyre.New(`https?://[^\s<>"]+`)
var closeoutPRWait = lazyre.New(`(?i)\b(review|reviews|re-review|merge|merging)\b`)
var closeoutOwnPRWork = lazyre.New(`(?i)\b(?:my|our|own)\s+(?:(?:final|upstream)\s+)*(?:review|merge|merging)\b|\b(?:i|we)\s+(?:(?:will|must|need to|have to)\s+)?(?:review|merge|finish|complete|fix|test|verify)\b|\b(?:review|merge|merging)\s+by\s+(?:me|us)\b`)

// The optional park line uses task_review's existing evidence contract, not another waiting schema.
func closeoutPark(reply string, previous *TaskReview, now time.Time, loc *time.Location) (Parked, bool) {
	c, artifacts, ok := closeout.Parse(reply)
	if !ok && strings.Contains(reply, "<closeout") || closeoutDisproved(c) {
		return Parked{}, false
	}
	delivery, marked := deliveryMark(reply)
	if marked && (delivery.Unblock == unblockUser || delivery.Unblock == unblockAgent) {
		return Parked{}, false
	}
	// A turn can be done while its delivery still waits on somebody else. Read the current reply, not an old ledger gate.
	if c.Waiting == "" && marked && delivery.held() && delivery.Unblock == unblockOther {
		c.Waiting = delivery.Gate
	}
	if c.Waiting == "" {
		return Parked{}, false
	}
	// Own unfinished work stays actionable even beside an upstream PR or a recorded receipt.
	if closeoutOwnPRWork.MatchString(c.Waiting) ||
		containsAny(c.Waiting, []string{"等我", "待我", "由我", "我来", "自己复审", "自己评审", "自分で", "私が"}) { // i18n-ignore: literal Agent closeout inputs.
		return Parked{}, false
	}
	// i18n-ignore: literal Agent closeout inputs; an owner's decision must stay actionable.
	if containsAny(strings.ToLower(c.Waiting), []string{"owner decision", "user decision", "user approval", "用户决定", "用户批准", "用户拍板"}) {
		return Parked{}, false
	}
	var body string
	if start := strings.LastIndex(reply, "<closeout"); ok && start >= 0 {
		var complete bool
		body, _, complete = strings.Cut(reply[start:], "</closeout>")
		if !complete {
			return Parked{}, false
		}
	}
	var input struct {
		WaitingOn string        `json:"waiting_on"`
		Until     string        `json:"until"`
		Evidence  *ParkEvidence `json:"evidence"`
	}
	explicit := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*"))
		key, value, found := strings.Cut(line, ":")
		if !found || key != "park" {
			continue
		}
		if explicit || json.Unmarshal([]byte(value), &input) != nil {
			return Parked{}, false
		}
		explicit = true
	}
	if explicit && (input.Evidence == nil || !strings.Contains(strings.ToLower(c.Waiting),
		strings.ToLower(strings.TrimSpace(input.Evidence.Target)))) {
		return Parked{}, false
	}
	// The evidence derived below is for a wait that passed the owner, own-work and Agent/user-unblock checks above,
	// so its next step is external (admission requires next_actor). An explicit park line and a reused receipt
	// keep their own: an Agent states it, and an older receipt without it goes back to the supervisor.
	if !explicit {
		prText := c.Waiting
		for _, a := range artifacts {
			prText += "\n" + a.Ref
		}
		if !ok {
			prText = reply // Legacy delivery-only replies have no structured artifact fields.
		}
		prSource := closeoutPRSource(prText)
		if prSource != "" && prSource != closeoutPRSource(reply) {
			return Parked{}, false
		}
		// Reuse an inspected receipt only when this closeout explicitly names the same target and source.
		if previous != nil && previous.Evidence != nil && previous.Reply == nil && previous.Decided == "" &&
			closeoutBinds(c, artifacts, previous.Evidence) {
			input.WaitingOn, input.Evidence = previous.WaitingOn, previous.Evidence.clone()
			if input.Evidence.Kind == "date" {
				input.Until = previous.Until.Format(time.RFC3339)
			}
		} else if date := closeoutDate.FindString(c.Waiting); date != "" {
			at, err := time.Parse(time.RFC3339, date)
			if len(date) == len(time.DateOnly) {
				at, err = time.ParseInLocation(time.DateOnly, date, loc)
			}
			if err != nil {
				return Parked{}, false
			}
			input.WaitingOn, input.Until = parkDate, at.Format(time.RFC3339)
			input.Evidence = &ParkEvidence{Kind: "date", Target: c.Waiting, Wake: c.Waiting, NextActor: nextActorExternal}
		} else if source := prSource; source != "" && upstreamCloseout(c.Waiting) {
			// The PR is an external object, not proof that a reviewer was contacted.
			input.WaitingOn = parkExternalEvent
			input.Evidence = &ParkEvidence{Kind: "external_event", Target: c.Waiting, URL: source, Wake: c.Waiting,
				NextActor: nextActorExternal}
		} else if externalCloseout(c.Waiting) {
			source := strings.TrimRight(closeoutSource.FindString(c.Waiting), ".,;)")
			if source == "" {
				for _, a := range artifacts {
					if a.Kind != "build" && a.Kind != "url" {
						continue
					}
					u, err := url.Parse(a.Ref)
					if err != nil {
						continue
					}
					name := strings.Trim(strings.TrimSuffix(u.Path, "/"), "/")
					if i := strings.LastIndex(name, "/"); i >= 0 {
						name = name[i+1:]
					}
					if len(name) >= 3 && strings.Contains(c.Waiting, name) && parkSourceURL(a.Ref) {
						if source != "" && source != a.Ref {
							return Parked{}, false
						}
						source = a.Ref
					}
				}
			}
			if source == "" || parkReviewURL(source) {
				return Parked{}, false // review/contact evidence must not be invented from a PR link
			}
			input.WaitingOn = parkExternalEvent
			input.Evidence = &ParkEvidence{Kind: "external_event", Target: c.Waiting, URL: source, Wake: c.Waiting,
				NextActor: nextActorExternal}
		}
	}
	p, err := readPark(c.Waiting, input.Until, input.WaitingOn, input.Evidence, now)
	if err != nil {
		return Parked{}, false
	}
	return p, true
}

func upstreamCloseout(waiting string) bool {
	// i18n-ignore: literal Agent upstream-wait inputs, not a sent-contact assertion.
	intent := closeoutPRWait.MatchString(waiting) || containsAny(waiting, []string{"评审", "复审", "复评", "合并", "レビュー", "マージ"})
	// i18n-ignore: require an external actor; generic pending/waiting wording is not evidence of one.
	external := containsAny(strings.ToLower(waiting), []string{"upstream", "maintainer", "reviewer", "上游", "维护者", "评审人", "评审者", "メンテナー", "レビュアー", "レビュー担当"})
	return intent && external
}

func closeoutPRSource(reply string) string {
	var source string
	for _, link := range closeoutSource.FindAllString(reply, -1) {
		link = triageNormalizePRURL(strings.TrimRight(link, ".,;)]}`"))
		if !parkReviewURL(link) {
			continue
		}
		if source != "" && source != link {
			return "" // Different PRs in the same reply need a supervisor to disambiguate the wait.
		}
		source = link
	}
	return source
}

// Reconcile pre-start stops even when their idle receipt was already told. Read each ready task only once.
func (s *Service) backfillCloseout(ctx context.Context, t Todo, lists machineSessions, now time.Time) (Todo, bool) {
	if t.Supervision == nil || t.DoneAt != nil {
		return t, false
	}
	st := t.Supervision.state(t)
	if st != "" && st != reviewRework {
		return t, false
	}
	agents := s.taskAgentsIn(ctx, t, lists)
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil && lists[a.MachineID].err != nil {
			return t, false
		}
	}
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	if len(agents) != 1 || agents[0].Status != proto.StatusDone || agents[0].UpdatedAt.After(s.born) {
		return t, false
	}
	b := &s.supervise
	b.mu.Lock()
	scanned := b.closeoutScanned[t.ID]
	for _, g := range b.pending {
		if g.Todo != t.ID {
			continue
		}
		switch g.Kind {
		case sigIdle, sigStalled, sigOrphaned, sigAllStopped:
		default:
			b.mu.Unlock()
			return t, false
		}
	}
	b.mu.Unlock()
	if scanned {
		return t, false
	}
	agents, wake, ok := s.mechanicalAgents(ctx, t, agents, now)
	if !ok || !wake.IsZero() || len(agents) != 1 || agents[0].UpdatedAt.After(s.born) {
		return t, false
	}
	b.mu.Lock()
	if b.closeoutScanned[t.ID] {
		b.mu.Unlock()
		return t, false
	}
	if b.closeoutScanned == nil {
		b.closeoutScanned = map[string]bool{}
	}
	b.closeoutScanned[t.ID] = true
	b.mu.Unlock()
	// Reuse this sweep's reachable inventory; admission still rechecks current-turn identity before the write CAS.
	if s.parkCloseout(ctx, t, agents[0], now) {
		if e, ok := s.turnOf(ctx, t.ID); ok && e.Machine == agents[0].MachineID &&
			e.Session == agents[0].SessionID && e.Rev == agents[0].Rev && e.Signal != nil && e.Signal.Kind == sigIdle {
			s.turnsTold(ctx, []superviseSignal{{Todo: t.ID, Kind: sigIdle, Key: e.Signal.Key}})
		}
		b.mu.Lock()
		for key, g := range b.pending {
			if g.Todo == t.ID {
				switch g.Kind {
				case sigIdle, sigStalled, sigOrphaned, sigAllStopped:
					delete(b.pending, key)
				}
			}
		}
		b.mu.Unlock()
	}
	if current, ok := s.todoNow(t.ID); ok {
		return current, true
	}
	return t, true
}

// Missing transcript/facts and failed writes do not consume the one startup inspection.
func (s *Service) retryCloseoutBackfill(id string) {
	s.supervise.mu.Lock()
	delete(s.supervise.closeoutScanned, id)
	s.supervise.mu.Unlock()
}

func externalCloseout(waiting string) bool {
	text := strings.ToLower(waiting)
	// i18n-ignore: these are literal Agent closeout inputs, not user-facing strings.
	if containsAny(text, []string{"approval", "permission", "reply", "review", "owner", "批准", "拍板", "回复", "复审", "返事", "承認"}) {
		return false
	}
	// i18n-ignore: literal Agent closeout inputs.
	return containsAny(text, []string{"deploy", "release", "event", "check run", "部署", "发布", "事件", "デプロイ", "リリース"})
}

func closeoutBinds(c proto.Closeout, artifacts []proto.WorkArtifact, e *ParkEvidence) bool {
	source := e.URL
	if e.Contact != nil && source == "" {
		source = e.Contact.URL
	}
	bound := source != "" && (strings.Contains(c.Waiting, source) || slices.ContainsFunc(artifacts, func(a proto.WorkArtifact) bool {
		return a.Ref == source
	}))
	if !bound {
		return false
	}
	text := strings.ToLower(c.Waiting)
	if strings.Contains(text, strings.ToLower(e.Target)) {
		return true
	}
	switch e.Kind {
	case "external_event":
		if parkReviewURL(e.URL) {
			return upstreamCloseout(c.Waiting)
		}
		return externalCloseout(c.Waiting)
	case "merge_review":
		// i18n-ignore: literal Agent closeout inputs; the exact source already binds the recorded reviewer.
		return containsAny(text, []string{"review", "merge", "复审", "合并", "レビュー", "マージ"})
	case "person_reply":
		// i18n-ignore: literal Agent closeout inputs, paired with the exact sent-message source.
		return containsAny(text, []string{"reply", "respond", "回复", "返事"})
	}
	return false
}

func automaticDependency(p Parked) string {
	e := p.Evidence
	parts := []string{e.Kind, e.URL}
	if e.Contact != nil {
		parts = append(parts, strings.ToLower(e.Target), e.Contact.URL)
	}
	if e.Kind == "date" {
		parts = append(parts, p.Until.Format(time.RFC3339))
	}
	b, _ := json.Marshal(parts)
	return string(b)
}

func lastAutomatic(v *Supervision) *automaticPark {
	if v == nil || len(v.Reviews) == 0 {
		return nil
	}
	return v.Reviews[len(v.Reviews)-1].Automatic
}

func runtimeCloseoutPark(t Todo, now time.Time) (Parked, bool) {
	w := t.WaitState
	if t.Phase != assistantwire.TaskPhaseWaiting || w == nil {
		return Parked{}, false
	}
	e := &ParkEvidence{Target: w.Target.Ref, Wake: w.Reason, NextActor: nextActorExternal}
	var until string
	if !w.Until.IsZero() {
		until = w.Until.UTC().Format(time.RFC3339)
	}
	switch w.WaitingOn {
	case assistantwire.WaitDate:
		e.Kind = parkDate
	case assistantwire.WaitExternal:
		switch w.Target.Kind {
		case assistantwire.WaitTargetPR:
			for _, u := range polledPRs(t) {
				if sameReplyPR(u, w.Target.Ref) {
					e.URL = u
					break
				}
			}
			if e.URL == "" {
				return Parked{}, false
			}
		case assistantwire.WaitTargetURL, assistantwire.WaitTargetIssue:
			e.URL = w.Target.Ref
		default:
			return Parked{}, false
		}
		e.Kind = parkExternalEvent
	default:
		return Parked{}, false // an owner decision or an unresolved reference goes through supervision
	}
	p, err := readPark(w.Reason, until, e.Kind, e, now)
	return p, err == nil
}

// A third unchanged attempt goes to the supervisor. Replaying one stopped turn never spends another attempt.
func (s *Service) parkCloseout(ctx context.Context, t Todo, c proto.Session, now time.Time) bool {
	if cur, ok := s.todoNow(t.ID); ok {
		t = cur
	}
	if t.Supervision == nil || s.agentSupervised(t) || c.UpdatedAt.After(now) {
		return false
	}
	var p Parked
	var ok bool
	if t.hasTaskRuntime() {
		p, ok = runtimeCloseoutPark(t, now)
	} else {
		_, reply := s.triageRead(ctx, c.MachineID, c)
		if reply == "" {
			reply = c.LastText
		}
		if reply == "" {
			s.retryCloseoutBackfill(t.ID)
			return false
		}
		var previous *TaskReview
		if n := len(t.Supervision.Reviews); n > 0 {
			previous = &t.Supervision.Reviews[n-1]
		}
		p, ok = closeoutPark(reply, previous, now, s.location(ctx))
	}
	if !ok || verifyParkTarget(t.ID, p.Evidence) != nil {
		return false
	}
	dependency := automaticDependency(p)
	prWait := p.Evidence.Kind == "merge_review" ||
		p.Evidence.Kind == "external_event" && parkReviewURL(p.Evidence.URL)
	if prWait {
		if t.PRURL != p.Evidence.URL && !slices.Contains(t.PRs, p.Evidence.URL) {
			return false
		}
		fact := prFacts(t, p.Evidence.URL)
		if fact == nil || fact.CheckedAt.After(now) || now.Sub(fact.CheckedAt) > triagePRStatusFreshFor {
			s.retryCloseoutBackfill(t.ID)
			return false
		}
		if fact.State != "open" || fact.HeadOID == "" || fact.Draft || fact.Conflicts || fact.Review == "changes_requested" ||
			fact.Checks == "failure" || fact.Checks == "failing" {
			return false
		}
		dependency += "|" + fact.HeadOID + "|" + fact.Review + "|" + fact.Checks
	}
	turn := c.MachineID + "/" + c.SessionID + "@" + c.UpdatedAt.UTC().Format(time.RFC3339Nano)
	latest, present := s.session(ctx, c.MachineID, c.SessionID)
	if !present || !sameTurn(latest, c.UpdatedAt) || latest.Status != proto.StatusDone ||
		latest.State == proto.StateWorking || latest.State == proto.StateNeedsYou ||
		latest.PendingInteractions > 0 || len(latest.Queued) != 0 || latest.TurnError != "" || latest.ReadOnlyReason != "" {
		return false
	}
	parked := false
	_, _, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool {
		return s.investigationTransitionError(ctx, cur, cur.Supervision, p.Evidence) == nil
	}, func(v *Supervision) bool {
		cur, exists := s.todoNow(t.ID)
		if !exists || cur.DoneAt != nil || v.state(cur) != "" && v.state(cur) != reviewRework || v.Waits != nil ||
			!v.UpdatedAt.Equal(t.Supervision.UpdatedAt) ||
			!sameTaskRuntime(cur, t) ||
			cur.PRURL != t.PRURL || !slices.Equal(cur.PRs, t.PRs) ||
			!slices.EqualFunc(cur.Agents, t.Agents, func(a, b TodoAgent) bool {
				return a.MachineID == b.MachineID && a.SessionID == b.SessionID &&
					(a.ReleasedAt == nil) == (b.ReleasedAt == nil) && (a.DismissedAt == nil) == (b.DismissedAt == nil)
			}) {
			return false
		}
		if prWait {
			fact := prFacts(cur, p.Evidence.URL)
			if fact == nil || fact.State != "open" || fact.Draft || fact.Conflicts ||
				fact.CheckedAt.After(now) || now.Sub(fact.CheckedAt) > triagePRStatusFreshFor ||
				dependency != automaticDependency(p)+"|"+fact.HeadOID+"|"+fact.Review+"|"+fact.Checks {
				return false
			}
		}
		a := automaticPark{Dependency: dependency, Turn: turn, Count: 1, FirstAt: now.UTC()}
		if prev := lastAutomatic(v); prev != nil && prev.Dependency == dependency {
			a = *prev
			if prev.Turn != turn {
				a.Count = min(maxAutomaticParks, a.Count+1)
			}
			a.Turn = turn
			if a.Count >= maxAutomaticParks || prev.Turn == turn {
				changed := prev.Turn != turn
				*v.Reviews[len(v.Reviews)-1].Automatic = a
				return changed
			}
		}
		v.State, v.Waits = reviewParked, nil
		v.Reviews = append(v.Reviews, TaskReview{At: now.UTC(), Verdict: reviewParked, By: "closeout",
			Reason: p.Reason, Until: p.Until, WaitingOn: p.WaitingOn, Parks: a.Count, Evidence: p.Evidence, Automatic: &a})
		if len(v.Reviews) > 20 {
			v.Reviews = v.Reviews[len(v.Reviews)-20:]
		}
		parked = true
		return true
	})
	if err != nil {
		s.retryCloseoutBackfill(t.ID)
		s.logf("task %s: closeout parking failed: %v", t.ID, err)
		return false
	}
	if parked {
		s.logf("task %s: automatically parked from closeout (evidence=%s, until=%s)",
			t.ID, p.Evidence.Kind, p.Until.UTC().Format(time.RFC3339))
	}
	return parked
}

// automaticParkSummaryTo fixes the logical target routeTo would choose. Delivery still checks reachability; an
// optimistic dispatcher lookup here only covers the no-main fallback and keeps generation and revalidation consistent.
func (s *Service) automaticParkSummaryTo(ctx context.Context, cfg Settings, t Todo) string {
	if to := s.digestTo(ctx, cfg, superviseSignal{Todo: t.ID}); to != "" {
		return to
	}
	return s.dispatcherOf(t.ID, func(string) bool { return true })
}

// One morning batch, starting the day after admission; never one immediate model wake per new park.
func (s *Service) automaticParkSummary(ctx context.Context, t Todo, now time.Time) (superviseSignal, bool) {
	r := t.Supervision.parking()
	if t.DoneAt != nil || r == nil || r.Automatic == nil || !now.Before(r.Until) {
		return superviseSignal{}, false
	}
	a := r.Automatic
	day := now.Format(time.DateOnly)
	to := s.automaticParkSummaryTo(ctx, s.settings(ctx), t)
	if now.Hour() < 9 || !a.FirstAt.In(now.Location()).Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())) ||
		to == "" || a.SummaryDay == day && a.SummaryTo == to {
		return superviseSignal{}, false
	}
	return superviseSignal{Todo: t.ID, Kind: sigParkedSummary, Key: day + "|" + a.Dependency,
		To: to, At: now}, true
}

func (s *Service) automaticParksTold(ctx context.Context, list []superviseSignal) {
	for _, g := range list {
		if g.Kind != sigParkedSummary {
			continue
		}
		_, _, err := s.updateSupervision(ctx, g.Todo, func(v *Supervision) bool {
			r := v.parking()
			if r == nil || r.Automatic == nil || g.Key != g.At.Format(time.DateOnly)+"|"+r.Automatic.Dependency {
				return false
			}
			r.Automatic.SummaryDay, r.Automatic.SummaryTo = g.At.Format(time.DateOnly), g.To
			return true
		})
		if err != nil {
			s.logf("task %s: automatic parking summary receipt: %v", g.Todo, err)
		}
	}
}
