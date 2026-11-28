package assistant

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Delivery (#3306, docs/assistant/todos.md "Delivery"): a task's work is done when it is live and checked, not when a PR is open or
// merged. A task's agent is told so with its prompt (assistant/task_delivery) and ends its last reply with a line
// saying how far it got; the done judge reads that line and the replies and says the delivery too. A finished task
// stopped at a PR, merged but not deployed, or deployed but off by default stays open with where it is held and who
// can unhold it; the ones waiting for the user's nod are listed together in the snapshot, to approve at once.
// assistant.done_needs_delivery turns it on (off by default).

// Deliveries: how far a task's work went out.
const (
	deliveryLive   = "live"   // live and checked
	deliveryPR     = "pr"     // stopped at a PR
	deliveryMerged = "merged" // merged, not deployed (or not checked)
	deliveryOff    = "off"    // deployed, off by default
	deliveryNone   = "none"   // nothing to ship
)

// Who can unhold a delivery.
const (
	unblockUser  = "user"
	unblockAgent = "agent"
	unblockOther = "other"
	// unblockSupervisor: an owner hold an Agent (or the done judge) reported on a supervised task that its supervisor
	// has not reviewed yet (#13191). It is read, never stored: currentDelivery derives it from a recorded user hold.
	// Clients that do not know it treat it as not the owner's, as for agent and other.
	unblockSupervisor = "supervisor"
)

// TodoDelivery is where a finished task's work is held short of live (Todo.Delivery): State one of pr, merged, off
// (live and none are kept too, so a later judgment replaces an earlier hold), Gate what holds it in a line, Unblock
// who can unhold it (user, agent, other; the apps also get supervisor, currentDelivery).
type TodoDelivery struct {
	State   string    `json:"state"`
	Gate    string    `json:"gate,omitempty"`
	Unblock string    `json:"unblock,omitempty"`
	At      time.Time `json:"at"`
	// SupervisorTold: when the hub handed this unreviewed owner hold to the task's supervisor (deliveryHoldSignal),
	// kept with the hold so a restart does not tell it again. Stored only: currentDelivery leaves it off the wire.
	SupervisorTold time.Time `json:"supervisor_told,omitzero"`
}

// held: the work is finished as asked but not out yet.
func (d *TodoDelivery) held() bool {
	return d != nil && (d.State == deliveryPR || d.State == deliveryMerged || d.State == deliveryOff)
}

// deliveryClosable: a task delivered so far may be closed.
func deliveryClosable(state string) bool { return state == deliveryLive || state == deliveryNone }

// The line an agent ends its last reply with (assistant/task_delivery).
var (
	deliveryLine = lazyre.New(`交付\s*[:：]\s*(已上线并实测|停在\s*PR|已合未上线|默认关待启用|不涉及上线)`)
	gateField    = lazyre.New(`卡点\s*[:：]\s*([^；;\n]+)`)
	unblockField = lazyre.New(`谁来解\s*[:：]\s*([^；;\n]+)`)
	// Match an explicit external actor at the start of a wait, including when the judge selected user.
	externalDeliveryWait = lazyre.New(`(?i)^(?:(?:wait(?:ing)? (?:for|on)|awaiting)\s+|(?:等待|等|待)\s*)?(?:(?:the|an?)\s+)?` + // i18n-ignore: matches the original gate text
		`(?:deploy(?:ment)?(?: integration)?\s+agent\b|maintainers?\b|reviewers?\b|部署(?:集成)?\s*agent\b|维护者|评审人|メンテナー|レビュアー|デプロイ(?:統合)?エージェント)`) // i18n-ignore: matches the original actor name
	readyDeliveryGate = lazyre.New(`(?i)\b(?:ready for review|out of draft|mark(?:ing)? (?:(?:the )?PR )?ready)\b|` +
		`(?:转(?:为)?|标记(?:为)?)\s*ready\b|(?:取消|退出)草稿|ドラフト解除`) // i18n-ignore: matches the original gate text
)

var deliveryWords = map[string]string{"已上线并实测": deliveryLive, "停在PR": deliveryPR, "已合未上线": deliveryMerged,
	"默认关待启用": deliveryOff, "不涉及上线": deliveryNone}

// deliveryMark reads the delivery line off a reply: its state, gate and who unholds it; ok false when there is none.
// The last such line counts.
func deliveryMark(text string) (d TodoDelivery, ok bool) {
	for _, line := range strings.Split(text, "\n") {
		m := deliveryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		d = TodoDelivery{State: deliveryWords[strings.Join(strings.Fields(m[1]), "")]}
		if g := gateField.FindStringSubmatch(line); g != nil {
			d.Gate = noneWord(g[1])
		}
		var who string
		if u := unblockField.FindStringSubmatch(line); u != nil {
			who = u[1]
		}
		d.Unblock = unblockOf(who, d.Gate)
		ok = true
	}
	if !ok { // no delivery line: a closeout says the same of merging and deploying (agents_closeout.go)
		if c, artifacts, has := closeout.Parse(text); has {
			return closeoutDelivery(c, artifacts)
		}
	}
	return d, ok
}

func noneWord(s string) string {
	s = strings.Trim(strings.TrimSpace(s), "「」<>")
	if s == "无" || s == "没有" || strings.EqualFold(s, "none") {
		return ""
	}
	return clip(s, 40)
}

// unblockOf maps the stated actor and gate to user, agent or other.
func unblockOf(s, gate string) string {
	s = strings.ToLower(noneWord(s))
	switch {
	case externalDeliveryWait.MatchString(s) || externalDeliveryWait.MatchString(gate):
		return unblockOther
	case s == "":
		return ""
	case s == unblockUser || strings.HasPrefix(s, "用户") || s == "你" || strings.HasPrefix(s, "你来"):
		return unblockUser
	case s == unblockAgent || strings.HasPrefix(s, "agent") || strings.HasPrefix(s, "我"):
		return unblockAgent
	default:
		return unblockOther
	}
}

var defaultOffGate = lazyre.New(`(?i)默认关|默认关闭|\b(?:off[-._ ]by[-._ ]default|disabled[-._ ]by[-._ ]default|default[-._ ]off)\b|既定でオフ|デフォルトでオフ`)                                                                                                                                                                                                // i18n-ignore: matches reported feature-gate evidence
var negatedDefaultOffGate = lazyre.New(`(?i)\b(?:not|never|no[-._ ]longer|cannot|(?:is|are|was|were|do|does|did|wo|would|ca|could|should|must|need|sha|has|have|had|might|ought)n['’]?t)[-._ ](?:(?:be|been)[-._ ])?(?:off[-._ ]by[-._ ]default|disabled[-._ ]by[-._ ]default|default[-._ ]off)\b|(?:没有|并非|不是|不再)默认关|既定でオフではない|デフォルトでオフではない`) // i18n-ignore: matches negated feature-gate evidence

func hasDefaultOffGate(text string) bool {
	return defaultOffGate.MatchString(text) && !negatedDefaultOffGate.MatchString(text)
}

// judgedDelivery is the delivery of a finished stretch of work: what the judge said (state, gate, unblock as it
// answered), else the agents' own line, else nothing to ship; held at the PR when that says live but the replies name
// a PR and say nothing of a deploy.
func judgedDelivery(state, gate, unblock string, replies []taskDoneReply) TodoDelivery {
	var mark TodoDelivery
	marked, excludedPR := false, false
	stage := ""
	for _, r := range replies {
		if m, ok := deliveryMark(r.Text); ok && (!r.otherPR || m.State == deliveryNone) {
			mark, marked = m, true
		}
		if r.otherPR {
			excludedPR = true
			continue
		}
		st, _ := stageOf(r.Text)
		if buildNumbers.MatchString(r.Text) {
			st = "deploy" // a TestFlight build sent out is the app's release
		}
		if stageIndex(st) > stageIndex(stage) {
			stage = st
		}
	}
	d := TodoDelivery{State: strings.ToLower(strings.TrimSpace(state)), Gate: noneWord(gate), Unblock: unblockOf(unblock, strings.TrimSpace(gate))}
	if d.Unblock == unblockUser && d.Gate == "" && mark.Unblock == unblockOther {
		d.Gate, d.Unblock = mark.Gate, mark.Unblock
	}
	if d.State == deliveryOff {
		if marked {
			d = mark
		} else {
			d = TodoDelivery{State: deliveryMerged}
		}
	}
	judged := true
	switch d.State {
	case deliveryLive, deliveryPR, deliveryMerged, deliveryOff, deliveryNone:
	default:
		d, judged = mark, false // a state it made up, or none: the agent's own line
		if !marked && !excludedPR {
			d.State = deliveryNone
		}
	}
	// Said live (or not judged) while the replies name a PR and no deploy: held, unless the agent's own line says it
	// is out. A judge's "nothing to ship" stands (a task that only cites a PR).
	if (d.State == deliveryLive || !judged) && deliveryClosable(d.State) && !(marked && deliveryClosable(mark.State)) {
		switch stage {
		case "review":
			d = TodoDelivery{State: deliveryPR, Gate: d.Gate, Unblock: d.Unblock}
		case "merge":
			d = TodoDelivery{State: deliveryMerged, Gate: d.Gate, Unblock: d.Unblock}
		}
	}
	if deliveryClosable(d.State) {
		d.Gate, d.Unblock = "", ""
	}
	return d
}

// deliveryStage is the stage bar's step a delivery shows at least (reports.go).
func deliveryStage(state string) string {
	switch state {
	case deliveryPR:
		return "review"
	case deliveryMerged:
		return "merge"
	case deliveryOff, deliveryLive:
		return "deploy"
	}
	return ""
}

// setTaskDelivery keeps d as task id's delivery, with its stages (the same setting), and on the todo.
// False leaves the previous state intact so deterministic supervision cannot consume a failed write.
func (s *Service) setTaskDelivery(ctx context.Context, id string, d TodoDelivery) bool {
	return s.writeTaskDelivery(ctx, id, d, false)
}

func (s *Service) writeTaskDelivery(ctx context.Context, id string, d TodoDelivery, legacy bool) bool {
	var reviewedAt time.Time // the latest review: a hold told to the supervisor before it is told again (#13191)
	if t, ok := s.todoNow(id); ok {
		if r := triageLatestReview(t.Supervision); r != nil {
			reviewedAt = r.At
		}
	}
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		s.logf("task %s: delivery %s not kept: the stages could not be read", id, d.State)
		return false
	}
	cur := b.stages[id]
	if legacy && cur.hasTaskRuntime() {
		b.mu.Unlock()
		return false
	}
	// A later owner hold can ask for the same action again after a review cleared it.
	if dd := cur.Delivery; dd != nil && dd.State == d.State && dd.Gate == d.Gate && dd.Unblock == d.Unblock &&
		(d.Unblock != unblockUser || !d.At.After(dd.At)) {
		b.mu.Unlock()
		return true
	}
	if dd := cur.Delivery; dd != nil && dd.State == d.State && dd.Gate == d.Gate && dd.Unblock == d.Unblock &&
		!dd.SupervisorTold.IsZero() && reviewedAt.Before(dd.SupervisorTold) {
		// The same owner hold again with no review since its supervisor was told: not news for the supervisor.
		d.SupervisorTold = dd.SupervisorTold
	}
	cur.Delivery = &d
	if stage := deliveryStage(d.State); stageIndex(stage) > stageIndex(cur.Stage) {
		cur.Stage = stage
	}
	next := maps.Clone(b.stages)
	next[id] = cur
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, next); err != nil {
			s.logf("task %s: delivery: %v", id, err)
			b.mu.Unlock()
			return false
		}
	}
	b.stages = next
	b.mu.Unlock()
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Delivery, t.Stage = &d, cur.Stage
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return true
}

// Delivery gate tiers (assistant.delivery_gate): how far a PR/Issue-backed task must go before task_review accepted
// closes it. "merged" is the default; "live" matches legacy assistant.done_needs_delivery=true; "off" disables the
// gate (legacy assistant.done_needs_delivery=false).
const (
	deliveryGateOff    = "off"
	deliveryGateMerged = "merged"
	deliveryGateLive   = "live"
)

var deliveryGates = []string{deliveryGateOff, deliveryGateMerged, deliveryGateLive}

// deliveryGateOf resolves the effective delivery closeout tier from v, preserving backward compatibility with the
// legacy boolean done_needs_delivery setting (true -> live, explicit false -> off, both unset -> merged).
func deliveryGateOf(v Settings) string {
	if v.DeliveryGate != nil && slices.Contains(deliveryGates, *v.DeliveryGate) {
		return *v.DeliveryGate
	}
	if v.DoneNeedsDelivery != nil {
		if *v.DoneNeedsDelivery {
			return deliveryGateLive
		}
		return deliveryGateOff
	}
	return deliveryGateMerged
}

// deliveryGate is the effective delivery closeout gate (off, merged or live; merged by default).
func (s *Service) deliveryGate(ctx context.Context) string {
	return deliveryGateOf(s.settings(ctx))
}

// doneNeedsDelivery: a finished task closes only once live and checked (delivery_gate == live, or legacy
// assistant.done_needs_delivery == true; false by default).
func (s *Service) doneNeedsDelivery(ctx context.Context) bool {
	return s.deliveryGate(ctx) == deliveryGateLive
}

var deliveryLabels = map[string]string{deliveryPR: "停在 PR", deliveryMerged: "已合未上线", deliveryOff: "默认关待启用"}

var unblockLabels = map[string]string{unblockUser: "要用户点头", unblockAgent: "Agent 自己能接着做", unblockOther: "等别人 / CI / 环境",
	unblockSupervisor: "Agent 说要用户点头，督办还没判断，没给用户开卡"} // i18n-ignore: the snapshot's words

// currentDelivery reads the hold against the same facts for the apps and snapshot. The recorded delivery remains
// available to the deployment checks. Review, decision and forge writes already notify clients to read the todo again.
func currentDelivery(t Todo) *TodoDelivery {
	if !t.Delivery.held() {
		return t.Delivery
	}
	d := *t.Delivery
	structured := t.hasTaskRuntime()
	if p := prFacts(t, t.PRURL); !structured && d.State == deliveryPR && p != nil && !p.CheckedAt.IsZero() && !p.CheckedAt.Before(d.At) {
		switch {
		case p.State == "merged" && len(polledPRs(t)) == 1:
			d.State, d.Gate, d.Unblock = deliveryMerged, "", unblockAgent
		case p.State == "open" && !p.Draft && readyDeliveryGate.MatchString(d.Gate):
			d.Gate, d.Unblock = "", unblockOther
		}
	}
	if t.DoneAt != nil {
		d.Gate, d.Unblock = "", ""
		return &d
	}
	d.SupervisorTold = time.Time{}
	if structured {
		if w := t.WaitState; t.Phase == assistantwire.TaskPhaseWaiting && d.Gate == "" && w != nil &&
			w.WaitingOn == assistantwire.WaitExternal && w.Target.Kind == assistantwire.WaitTargetPR && sameReplyPR(w.Target.Ref, t.PRURL) {
			d.Gate, d.Unblock = clip(oneLine(w.Reason), 60), unblockOther
		}
	} else if d.Unblock != unblockSupervisor { // already read once (the snapshot reads its copies again)
		d.Unblock = unblockOf(d.Unblock, d.Gate)
	}
	v := t.Supervision
	r := triageLatestReview(v)
	reviewed := false // the supervisor's own needs_user makes it the owner's
	if r != nil {
		switch {
		case v.parking() != nil && slices.Contains(parkWaitingOn, r.WaitingOn):
			d.Gate, d.Unblock = r.Reason, unblockOther
		case r.Verdict == reviewNeedsUser && triageReviewState(t) == reviewNeedsUser:
			d.Gate, d.Unblock = cmp.Or(r.Reason, r.Note), unblockUser
			reviewed = true
		case !r.At.Before(d.At) && (acceptanceStands(t) ||
			v.State == reviewUserCheck && userCheckAsked(t) && (t.ReopenedAt == nil || !r.At.Before(*t.ReopenedAt))):
			// The current round's personal verification replaces the earlier delivery approval.
			d.Gate, d.Unblock = "", ""
		case d.Unblock == unblockUser && !r.At.Before(d.At) &&
			(r.Verdict == reviewNeedsUser || r.Verdict == reviewParked || r.Verdict == reviewRework):
			// Rework or an ended wait supersedes the earlier owner gate until a new judgment supplies one.
			d.Gate, d.Unblock = "", unblockAgent
		}
	}
	if d.Unblock == unblockUser && !reviewed && supervisedHold(t, r) {
		d.Unblock = unblockSupervisor
	}
	return &d
}

// supervisedHold: task t has a supervisor and its latest review r (nil for none) came before its recorded delivery
// hold. Only the supervisor's needs_user card or a real interaction reaches the owner's Needs you; a hold an Agent or
// the done judge reported goes to the supervisor first, which opens the card, parks or sends the work on (#13191).
// An unsupervised task keeps its owner hold (#10787).
func supervisedHold(t Todo, r *TaskReview) bool {
	return t.Supervision != nil && t.Supervision.Supervisor != "" && t.Delivery != nil &&
		(r == nil || r.At.Before(t.Delivery.At))
}

// deliverySummary is an open task's held delivery in a line for the snapshot ("" when it is not held).
func deliverySummary(t Todo) string {
	d := currentDelivery(t)
	if t.DoneAt != nil || !d.held() {
		return ""
	}
	out := deliveryLabels[d.State]
	if d.Gate != "" {
		out += "，卡在「" + d.Gate + "」"
	}
	if u := unblockLabels[d.Unblock]; u != "" {
		out += "，" + u
	}
	return out
}

// snapshotApprovals are the open tasks held for the user's nod, oldest first (scopeSnapshot shows a conversation the
// oldest snapshotApprovalLimit of its own).
func (s *Service) snapshotApprovals(ctx context.Context, web string) []prompts.SnapshotApproval {
	var list []Todo
	for _, t := range s.todosOf(ctx, "*") {
		t.Delivery = currentDelivery(t)
		if t.Origin != "" && t.DoneAt == nil && t.Delivery.waitsForOwner() {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Delivery.At.Before(list[j].Delivery.At) })
	var out []prompts.SnapshotApproval
	for _, t := range list {
		gate := t.Delivery.Gate
		if gate == "" {
			gate = "等用户批准"
		}
		out = append(out, prompts.SnapshotApproval{ID: t.ID, Title: clip(oneLine(t.Title), 60), Stage: deliveryLabels[t.Delivery.State],
			Gate: clip(oneLine(gate), 60),
			Link: todoLink(web, t.ID), Owner: todoOwner(t)})
	}
	return out
}

func (d *TodoDelivery) waitsForOwner() bool {
	return d.held() && d.Unblock == unblockUser
}
