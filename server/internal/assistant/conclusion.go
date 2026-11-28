package assistant

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Conclusion cards (#1226, docs/extensions/bots.md "Conclusions"): the owner hands out many tasks and wants one thing of
// each, its conclusion. When the main conversation's review of a supervised task gives a result (task_review accepted, or
// needs_user), the task gets one card: a verdict (done / cannot be done / needs your decision), one sentence of why and
// one link (the PR, the page, the document or the agent's conversation). A decision carries the option the supervisor
// recommends, and "OK" takes it. The conclusion is kept as the task's ask in the acceptance book, the same as a deployed
// task's "Please verify" (acceptance.go), so the briefing, the board and the web app list it the same way, with the same
// verdict line; a deployed task keeps its "Please verify" card, which says the verdict on top of its three lines.

// The verdicts of a conclusion (acceptAsk.Outcome).
const (
	outcomeDone   = "done"
	outcomeCannot = "cannot"
	outcomeDecide = "decide"
)

// decideAsk says a is a conclusion that waits for the owner's decision.
func decideAsk(a *acceptAsk) bool { return a.Conclusion && a.Outcome == outcomeDecide }

// verdictText is an ask's verdict line: the same words on its Lark card, the briefing's card and `brief` item, the board
// and the snapshot. A verification ask (!a.Conclusion) reflects the to-do's effective delivery state.
func verdictText(l i18n.Lang, a *acceptAsk, delivery *TodoDelivery) string {
	label := i18n.T(l, "assistant-reports.verdict_"+cmp.Or(a.Outcome, outcomeDone))
	reason := a.Reason
	if !a.Conclusion {
		reason = verifyReason(l, delivery)
	}
	if reason == "" {
		return label
	}
	return i18n.T(l, "assistant-reports.verdict_line", i18n.Args{"verdict": label, "reason": reason})
}

// verifyReason picks the verification ask's reason from the to-do's effective delivery state: only explicit live says
// deployed; held states name their gate; none or missing live evidence stays neutral.
func verifyReason(l i18n.Lang, delivery *TodoDelivery) string {
	state := ""
	if delivery != nil {
		state = delivery.State
	}
	switch state {
	case deliveryLive:
		return i18n.T(l, "assistant-reports.verdict_deployed")
	case deliveryPR:
		return i18n.T(l, "assistant-reports.verdict_verify_pr")
	case deliveryMerged:
		return i18n.T(l, "assistant-reports.verdict_verify_merged")
	case deliveryOff:
		if hasDefaultOffGate(delivery.Gate) {
			return i18n.T(l, "assistant-reports.verdict_verify_off")
		}
		return i18n.T(l, "assistant-reports.verdict_verify")
	default:
		return i18n.T(l, "assistant-reports.verdict_verify")
	}
}

// recommendText is a decision's recommended option as a line ("" when it is not a decision or recommends nothing).
func recommendText(l i18n.Lang, a *acceptAsk) string {
	if !decideAsk(a) || a.Recommend == "" {
		return ""
	}
	return i18n.T(l, "assistant-reports.verdict_recommend", i18n.Args{"option": a.Recommend})
}

// askSummary is the plain line a card of ask a falls back to, and what the chat list shows of it.
func askSummary(l i18n.Lang, a *acceptAsk) string {
	if a.Conclusion {
		return verdictText(l, a, nil)
	}
	return a.Changed
}

// checkOptionTaken identifies a check's recommendation or an explicitly recorded option. Older nonempty Chosen
// text cannot establish an option selection, even if it spells a label.
func checkOptionTaken(a *acceptAsk) bool {
	return a.Check != "" && (a.Chosen == "" || a.OptionTaken)
}

// acceptedNote is what the bot answers under a card the owner passed.
func acceptedNote(l i18n.Lang, a *acceptAsk) string {
	switch {
	case a.OutsideID != "" && !a.WithdrawnAt.IsZero():
		return i18n.T(l, "assistant-reports.outside_withdrawn")
	case a.OutsideID != "" && a.ByDefault:
		return i18n.T(l, "assistant-reports.outside_answered_default")
	case a.OutsideID != "":
		return i18n.T(l, "assistant-reports.outside_answered")
	case decideAsk(a) && a.Superseded:
		if a.SupersededReason != "" {
			return i18n.T(l, "assistant-reports.decision_superseded_reason", i18n.Args{"reason": a.SupersededReason})
		}
		return i18n.T(l, "assistant-reports.decision_superseded")
	case decideAsk(a) && a.AgentReply != nil && a.AgentReply.Handled:
		return i18n.T(l, "assistant-reports.decision_handled_in_agent")
	case decideAsk(a) && a.ByDefault:
		return i18n.T(l, "assistant-reports.verdict_taken_default")
	case decideAsk(a) && a.TextDelivery == decisionTextAgent && a.Chosen != "":
		return i18n.T(l, "assistant-reports.decision_chosen_noted", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && a.TextDelivery == decisionTextRecorded && a.Chosen != "":
		return i18n.T(l, "assistant-reports.decision_chosen_for_supervisor", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && a.TextDelivery != "" && a.Chosen != "": // unknown future outcome: do not invent delivery
		return i18n.T(l, "assistant-reports.decision_chosen_recorded", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && checkOptionTaken(a) && a.Chosen != "": // a check's option went to the supervisor
		return i18n.T(l, "assistant-reports.decision_chosen_supervisor", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && checkOptionTaken(a) && a.Response == "": // its OK: to the supervisor, not the Agent
		return i18n.T(l, "assistant-reports.verdict_taken_supervisor")
	case decideAsk(a) && !a.OptionTaken && a.Chosen != "": // older text has no stored selection or delivery evidence
		return i18n.T(l, "assistant-reports.decision_chosen_recorded", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && a.Chosen != "":
		return i18n.T(l, "assistant-reports.decision_chosen_noted", i18n.Args{"option": a.Chosen})
	case decideAsk(a) && a.Response != "":
		return i18n.T(l, "assistant-reports.decision_response_noted", i18n.Args{"response": i18n.T(l, "assistant-reports.decision_response_"+a.Response)})
	case decideAsk(a):
		return i18n.T(l, "assistant-reports.verdict_taken")
	case a.BySupervisor:
		return i18n.T(l, "assistant-reports.accepted_by_supervisor")
	case a.Conclusion:
		return i18n.T(l, "assistant-reports.verdict_acked")
	}
	return i18n.T(l, "assistant-reports.accepted")
}

// conclusionCard is a conclusion's card: the verdict line, the recommended option of a decision, the link, and the button
// ("Got it", or for a decision "Take the recommendation"); once passed, what came of it instead of the button.
func (s *Service) conclusionCard(ctx context.Context, t Todo, a acceptAsk) *botcard.Card {
	l := s.botLang(ctx)
	title := s.feishuDirection(ctx, t, s.feishuDirections(ctx)) + i18n.T(l, "assistant-reports.verdict_title", i18n.Args{"task": clip(oneLine(t.Title), 30)})
	color := botcard.Green
	switch {
	case !a.AcceptedAt.IsZero(): // passed: over
		color = botcard.Grey
	case a.Outcome == outcomeCannot:
		color = botcard.Red
	case a.Outcome == outcomeDecide:
		color = botcard.Orange
	}
	delivery := currentDelivery(t)
	c := botcard.NewCard(title, color, verdictText(l, &a, delivery)).Tag(i18n.T(l, "assistant-reports.tag_conclusion"), botcard.Neutral)
	// The goal, where it is now (with how far the work went out) and the evidence below (#3218).
	now := botcard.EscapeMarkdown(verdictText(l, &a, delivery))
	if live := liveText(l, s.todoStages(ctx)[t.ID]); live != "" && a.Outcome != outcomeDecide {
		now += " · " + botcard.EscapeMarkdown(live)
	}
	c.Markdown(i18n.T(l, "assistant-reports.goal_head", i18n.Args{"goal": botcard.EscapeMarkdown(clip(oneLine(t.Title), 80)), "now": now}))
	if r := recommendText(l, &a); r != "" {
		c.Markdown(botcard.EscapeMarkdown(r))
	}
	// The criteria and how each ended stay on the task's page, not on the card (#10380): the card leads with the result,
	// and names only what was not met, so a shortfall is never hidden behind "Done".
	if a.Outcome != outcomeDecide {
		if line := notMetLine(l, s.resultOfTodo(t.ID)); line != "" {
			c.Markdown(line)
		}
	}
	if d := a.deadlineText(l, s.location(ctx), time.Now(), t.Agents); d != "" && decideAsk(&a) && a.AcceptedAt.IsZero() && a.AgentReply == nil {
		c.Markdown(botcard.EscapeMarkdown(d))
	}
	// Self-contained (#3804): where it came from and stands, what each option leads to, what waiting costs.
	for _, line := range decisionLines(l, s.location(ctx), &a) {
		c.Markdown(botcard.EscapeMarkdown(line))
	}
	if a.Link != "" {
		c.Markdown(i18n.T(l, "assistant-reports.verdict_link", i18n.Args{"link": "[" + botcard.EscapeMarkdown(clip(a.Link, 80)) + "](" + a.Link + ")"}))
	}
	if !a.AcceptedAt.IsZero() {
		return c.Note(acceptedNote(l, &a))
	}
	// One row: the primary answer, the first other option of a decision, and a "more" menu with the rest — the other
	// options, the fixed answers for a decision they cannot answer as it is (decision.go), the link.
	var bs []botcard.Button
	var menu []botcard.MenuItem
	hint := "assistant-reports.verdict_reply_hint"
	switch {
	case decideAsk(&a) && a.Recommend != "":
		bs = append(bs, botcard.Btn(i18n.T(l, "assistant-reports.verdict_take"), botcard.Primary, map[string]any{"a": "accept", "id": t.ID}))
		hint = "assistant-reports.verdict_decide_hint"
	case decideAsk(&a):
		hint = "assistant-reports.verdict_decide_hint_none"
	default:
		bs = append(bs, botcard.Btn(i18n.T(l, "assistant-reports.verdict_ack"), botcard.Primary, map[string]any{"a": "accept", "id": t.ID}))
	}
	if decideAsk(&a) {
		for i, o := range a.Options {
			if i == 0 {
				continue
			}
			if len(bs) < 2 {
				bs = append(bs, botcard.Btn(clip(o.Label, 20), botcard.Default, map[string]any{"a": "accept", "id": t.ID, "opt": o.Label}))
				continue
			}
			menu = append(menu, botcard.MenuItem{Label: clip(o.Label, 20), Value: map[string]any{"opt": o.Label}})
		}
		for _, r := range ownerResponses {
			menu = append(menu, botcard.MenuItem{Label: i18n.T(l, "assistant-reports.decision_response_"+r), Value: map[string]any{"resp": r}})
		}
	}
	if a.Link != "" {
		if len(menu) == 0 {
			bs = append(bs, botcard.LinkBtn(i18n.T(l, "assistant-reports.accept_open"), a.Link))
		} else {
			menu = append(menu, botcard.MenuItem{Label: i18n.T(l, "assistant-reports.accept_open"), URL: a.Link})
		}
	}
	c.Buttons(append(bs, botcard.Overflow(map[string]any{"a": "accept", "id": t.ID}, menu...))...)
	return c.Note(i18n.T(l, hint))
}

// liveText says how far a task's work went out, from its delivery (delivery.go) or else its stage; "" when not known.
func liveText(l i18n.Lang, st todoStage) string {
	if d := st.Delivery; d != nil {
		switch d.State {
		case deliveryLive:
			return i18n.T(l, "assistant-reports.live_live")
		case deliveryNone:
			return i18n.T(l, "assistant-reports.live_none")
		case deliveryPR, deliveryMerged, deliveryOff:
			return i18n.T(l, "assistant-reports.live_held_"+d.State)
		}
	}
	switch st.Stage {
	case "verify":
		return i18n.T(l, "assistant-reports.live_live")
	case "deploy":
		return i18n.T(l, "assistant-reports.live_unchecked")
	case "review":
		return i18n.T(l, "assistant-reports.live_held_pr")
	case "merge":
		return i18n.T(l, "assistant-reports.live_held_merged")
	}
	return ""
}

// conclusionInput is what a review says of a task's result, for its conclusion card.
type conclusionInput struct {
	Outcome   string // outcomeDone, outcomeCannot or outcomeDecide
	Reason    string // one sentence of why (the review's note when it gave none)
	Link      string // the link it gave ("" or not a web address: one is found, see conclusionLink)
	Recommend string // a decision's recommended option
	Note      string
	Checks    []ReviewCheck
	// Deadline and Reversible: a decision's deadline and whether it can be undone (#3127, decision_deadline.go).
	Deadline   time.Time
	Reversible bool
	// Decision: a decision's why_owner, options and cost of waiting (#3804, decision.go); the zero value for the rest.
	Decision reviewDecision
	Reviewer string // the supervisor raising this decision, for its pending manual-resume refusal
	// ForBriefing: the decision can wait and is held for the next briefing: no card now (#4930, decision_wait.go).
	ForBriefing bool
	// RaisedBy: the leader conversation or background supervisor that raised it, kept while it is held (acceptAsk).
	RaisedBy string
	// Check: the recurring schedule whose progress check raised the decision (acceptAsk.Check).
	Check string
	// Verified: the supervisor checked the evidence (supervisorVerified): a done or cannot conclusion tells the owner and
	// is not left for them to acknowledge (#3240).
	Verified bool
}

// askConclusion keeps supervised task t's conclusion as its ask and posts its card when the bot can; true when the card
// went out (false: none is due, the bot could not take it yet and retryAcceptance tries it again, or no card was made).
// A later conclusion takes the place of the one before, whose cards are dead from then on (Earlier). A task whose deployed
// work asks to be verified this round gets no second card for a done one: that "Please verify" card says the verdict on
// top (a decision still asks, as its own card).
func (s *Service) askConclusion(ctx context.Context, t Todo, in conclusionInput) bool {
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return false
	}
	var earlier []string
	if old := b.asks[t.ID]; old != nil {
		if in.Outcome != outcomeDecide && (old.AskedAt.IsZero() || !old.Conclusion && !acceptAskStale(old, t)) {
			// A "Please verify" card is going out, or is up for this round: it is this conclusion. A decision is not: it
			// stands in for a "Please verify" still being written, which then gives way (postAsk, #3804).
			b.mu.Unlock()
			return false
		}
		earlier = lastOf(slices.Concat(old.Earlier, old.Messages), acceptEarlierMost)
	}
	b.asks[t.ID] = &acceptAsk{Earlier: earlier} // claimed, as askAcceptance does
	b.mu.Unlock()
	l := s.botLang(ctx)
	note, _, _ := strings.Cut(strings.TrimSpace(in.Note), "\n")
	reasonLimit := 120
	if in.Outcome == outcomeDecide {
		reasonLimit = decisionReasonMaxRunes
	}
	a := acceptAsk{Conclusion: true, Outcome: in.Outcome, Reason: clip(oneLine(cmp.Or(strings.TrimSpace(in.Reason), note)), reasonLimit),
		Recommend: clip(oneLine(in.Recommend), 120), Link: s.conclusionLink(ctx, t, in), AskedAt: time.Now().UTC(), Earlier: earlier}
	if in.Outcome == outcomeDecide && a.Recommend != "" {
		a.Deadline, a.Reversible = in.Deadline, in.Reversible
	}
	if in.Outcome == outcomeDecide {
		if in.Check == "" && in.Reviewer != "" {
			a.ResumeTerminal = s.todos.takeResumeTerminal(t.ID, in.Reviewer, a.AskedAt)
		}
		a.WhyOwner, a.Options, a.CostOfWaiting = in.Decision.WhyOwner, in.Decision.Options, in.Decision.CostOfWaiting
		a.From, a.Stands = s.decisionBackground(ctx, t) // the hub's, not the model's: where it came from, where it stands
		a.ForBriefing, a.Check = in.ForBriefing, in.Check
		if a.ForBriefing {
			a.RaisedBy = in.RaisedBy
		}
	}
	if a.Reason == "" && in.Outcome == outcomeDone {
		a.Reason = i18n.T(l, "assistant-reports.verdict_done_default")
	}
	if in.Verified && in.Outcome != outcomeDecide {
		a.AcceptedAt, a.BySupervisor = a.AskedAt, true
	}
	if ag := lastAgent(t); ag.SessionID != "" {
		a.MachineID, a.SessionID = ag.MachineID, ag.SessionID
	}
	return s.postAsk(ctx, t, a)
}

// lastAgent is the agent that worked on t last (none: zero).
func lastAgent(t Todo) TodoAgent {
	if len(t.Agents) == 0 {
		return TodoAgent{}
	}
	return t.Agents[len(t.Agents)-1]
}

// validWebURL returns raw trimmed when it is a well-formed http(s) URL with a non-empty host; "" otherwise.
func validWebURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" || urlRe.FindString(u) != u {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Host == "" {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return u
	default:
		return ""
	}
}

// todoRepositoryDeliveryPR returns the first canonical delivery PR/MR URL saved in task id's repository bindings.
// It reads saved bindings without mutating state and skips reference-role links.
func (s *Service) todoRepositoryDeliveryPR(ctx context.Context, id string) string {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return ""
	}
	set, err := st.TodoRepositories(ctx, id)
	if err != nil {
		return ""
	}
	for _, row := range set.Repositories {
		for _, l := range row.Links {
			if l.Kind != "pull_request" {
				continue
			}
			role := l.Role
			if role == "" {
				role = repository.LinkRoleDelivery
			}
			if role != repository.LinkRoleDelivery {
				continue
			}
			if u := validWebURL(l.CanonicalURL); u != "" {
				return u
			}
		}
	}
	return ""
}

// conclusionLink is the one link of a conclusion: the one the review gave, when it is a web address; else the task's
// stage PR (or PRURL); else a canonical saved repository delivery PR/MR link; else the first web address in the
// review's note and evidence; else the agent's conversation; else the task's page.
func (s *Service) conclusionLink(ctx context.Context, t Todo, in conclusionInput) string {
	if u := validWebURL(in.Link); u != "" {
		return u
	}
	if pr := validWebURL(cmp.Or(s.todoStages(ctx)[t.ID].PR, t.PRURL)); pr != "" {
		return pr
	}
	if pr := s.todoRepositoryDeliveryPR(ctx, t.ID); pr != "" {
		return pr
	}
	texts := []string{in.Note}
	for _, c := range in.Checks {
		texts = append(texts, c.Evidence)
	}
	if u := validWebURL(strings.TrimRight(urlRe.FindString(strings.Join(texts, "\n")), ".,;:!?")); u != "" {
		return u
	}
	web := s.webBase(ctx)
	if ag := lastAgent(t); web != "" && ag.SessionID != "" {
		return sessionLink(web, ag.MachineID, ag.SessionID, false)
	}
	return todoLink(web, t.ID)
}

// dropDecision ends task id's decision still waiting, once its review moved on (rework, cancelled): it is no longer
// listed, its button takes nothing, and a reply to its card is answered "already handled".
func (s *Service) dropDecision(ctx context.Context, id string) {
	s.supersedeDecision(ctx, id, "")
}

// supersedeDecision clears task id's open needs_user decision ask when supervision transitions the review via
// task_review, recording the natural-language reason (never fabricating an owner selection), notifying clients so it
// leaves "Needs you", and redrawing any Lark cards to remove buttons and display the supersession note (#13112).
func (s *Service) supersedeDecision(ctx context.Context, id, reason string) {
	reason = clip(oneLine(strings.TrimSpace(reason)), 300)
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	a := b.asks[id]
	if a == nil || !decideAsk(a) || !a.AcceptedAt.IsZero() {
		b.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	a.AcceptedAt = now
	if !a.Deadline.IsZero() && a.DueAt.IsZero() {
		a.DueAt = now
	}
	a.Superseded = true
	a.SupersededReason = reason
	s.saveAcceptLocked(ctx)
	cp := *a
	b.mu.Unlock()
	s.askChanged(ctx)
	s.todoPushOver(id) // a later decision of the task rings again
	if len(cp.Messages) > 0 {
		s.updateAskCards(ctx, id, cp)
	}
}

// askCopy is task id's ask as it is now (false: none, or a claim still going out).
func (s *Service) askCopy(id string) (acceptAsk, bool) {
	a, _, ok := s.askCopyReadable(id)
	return a, ok
}

// askCopyReadable is the three-state form of askCopy. readable is false when the acceptance book could not be read
// (a transient settings-store failure), which is not evidence that no owner card exists: callers that would spend a
// once-per-round marker on that conclusion must hold off and retry rather than treat an unreadable ledger as card-less.
// ok is false both for an unreadable book and for a readable one with no ask; the distinction is readable.
func (s *Service) askCopyReadable(id string) (ask acceptAsk, readable, ok bool) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAcceptLocked(context.Background()) {
		return acceptAsk{}, false, false
	}
	a := b.asks[id]
	if a == nil || a.AskedAt.IsZero() {
		return acceptAsk{}, true, false
	}
	return *a, true, true
}

// takeRecommendation: the owner took decision a's recommended option for task id (or, a.ByDefault, nobody answered by its
// deadline and the hub took it, #3127): it is kept in the todo's decisions and sent to the task's agent, whose next stop
// comes back to the supervisor (userDecided). With no Agent to receive an explicit owner choice, the choice is recorded
// and handed to the supervisor instead. Any other delivery error takes nothing and leaves the round retryable. A
// failed automatic default keeps the old rollback and owner-reminder behavior. A decision a recurring check raised
// (a.Check) is about the supervisor's approach, not the Agent's work: it is recorded and goes to the supervisor with
// what the option leads to, and the Agent hears only what the supervisor then tells it (checkDecided, #11587).
func (s *Service) takeRecommendation(ctx context.Context, id string, a acceptAsk) (acceptResult, error) {
	t, ok := s.todoNow(id)
	if !ok {
		return acceptResult{}, db.ErrTodoNotFound
	}
	l := s.botLang(ctx)
	msg := i18n.T(l, "assistant-reports.verdict_take_to_agent", i18n.Args{"option": a.Recommend})
	row := db.TodoDecisionRow{Question: cmp.Or(a.Reason, i18n.T(l, "assistant-reports.verdict_decide")), Choice: decisionText(a.Recommend),
		Source: db.DecisionManual}
	if a.ByDefault {
		msg = i18n.T(l, "assistant-reports.verdict_default_to_agent", i18n.Args{"option": a.Recommend})
		// The hub's call at the deadline, not the owner's: an agent's source, with the reason saying so.
		row.Reason, row.Source = defaultReason(l, s.location(ctx), a.Deadline), db.DecisionAgent
	}
	var sendErr error
	if a.Check == "" && a.OutsideID == "" {
		if _, err := s.relayToTask(ctx, t.Origin, id, msg, func(cur Todo) bool {
			return cur.DoneAt == nil && !acceptAskStale(&a, cur)
		}); err != nil {
			s.logf("task %s: conclusion to agent: %v", id, err)
			if a.ByDefault || !decisionWaitsForSupervisor(err) {
				return acceptResult{}, err // a retryable failure or an automatic default takes nothing
			}
			sendErr = err
		}
	}
	if st := s.todoDB(); st != nil {
		at := decisionNow()
		row.ID, row.TodoID, row.CreatedAt = "dec_"+randomHex(8), id, at
		if ok, err := s.insertDecision(ctx, st, row); err != nil {
			s.logf("task %s: conclusion decision: %v", id, err)
		} else if ok {
			if !a.ByDefault {
				s.ownerDecided(id, row.ID, a.WhyOwner)
			}
			s.decisionsChanged(ctx, id, at)
		}
	}
	switch {
	case a.OutsideID != "":
		// An outside caller polls the answer; there is no Agent or supervisor to relay to.
	case a.Check != "":
		s.checkDecided(ctx, t, &a, a.Recommend, false)
	case sendErr != nil:
		return s.decisionNotDelivered(ctx, id, a.AskedAt, a.Recommend, sendErr), nil
	default:
		s.userDecided(ctx, id)
	}
	return acceptSucceeded(), nil
}

// userDecided: the owner answered supervised task id's decision, and the answer went to its agent or its supervisor for
// delivery. The task no longer waits for them, so its agent's next stop comes back to the supervisor as any stop does
// (superviseJudged), whether it finished or not, and the supervision messages are about it again.
func (s *Service) userDecided(ctx context.Context, id string) {
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if v.State != reviewNeedsUser {
			return false
		}
		v.State = ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	}); err != nil {
		s.logf("task %s: supervision: %v", id, err)
	}
}
