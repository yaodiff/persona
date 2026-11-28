package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// The card ledger: rule 4 of the decision-rights design (2026-11-23, "who decides what"). The owner decides only six
// kinds of things; every card or push the manager sends to the owner's phone is recorded with which of the six it is
// (or none), its source and its owner, counted per day of the owner's time zone. Once a day, after the morning
// briefing's time, the main conversation (it supervises everything) is told the day before's count: how many, how many
// of each kind, and how many were none of the six, by source. Every push that is none of the six is a bug. The report
// goes to the main conversation without waking it (relay_later.go) and is never pushed to the owner. The ledger only
// records: which cards are pushed is decided where they are made.
//
// The six kinds (plus two non-decision buckets):
//
//	1 goals and direction: whether to do it, what it should become, what counts as good, how directions trade off
//	2 what only the owner knows: their preferences, the facts of their life and of the people in it
//	3 outward as the owner: messages, comments and mail others see, the owner's identity at work
//	4 money: a new paid account or subscription, more seats
//	5 irreversible and never said before: deleting their data, stopping what they use (a deploy with a backup that
//	  rolls itself back is reversible: not this)
//	6 only the owner in person: a login, a second factor, a password or a form, the experience check
//	7 owner-subscribed alerts and reminders: pushes the owner explicitly opted into (neither one of the six decisions
//	  nor a platform defect)
//	0 none of the six: a bug each time
//
// A card's kind is fixed in code by its source (cardCategories), never by a model: the source is a constant named
// where the card is made (withCard on the push's ctx, deliverCard, decisionCardSource, or inputCardSource for a card).

const (
	cardCatNone         = 0 // none of the six: every such push is a bug
	cardCatDirection    = 1
	cardCatOwnerKnows   = 2
	cardCatOutward      = 3
	cardCatMoney        = 4
	cardCatIrreversible = 5
	cardCatInPerson     = 6
	cardCatSubscribed   = 7 // owner-subscribed alerts and reminders: neither a decision nor a platform defect
	cardCatCount        = 8
)

// The sources: one constant per code point that pushes to the owner. Each has its kind in cardCategories, and the
// test reads this block to hold every constant to having one.
const (
	cardSecretForm         = "secret_form"          // the manager's own secret or form card (secret_run, secret_store, secret_handoff)
	cardAgentSecretForm    = "agent_secret_form"    // an Agent's card asking for a password, fields or an MCP form (elicitation)
	cardConfirmOutbound    = "confirm_outbound"     // health.go confirmOutbound: a conversation hands sensitive words to another Agent
	cardOutboundWrite      = "outbound_write"       // a connector's write the gate left to the owner (mail_send, a chat message, a comment)
	cardLeaderNeedsUser    = "leader_needs_user"    // a task waited on the owner (needs_user); before decision rights it was pushed straight to them, kept for the history it counts
	cardDecision           = "decision"             // todo_ask.go pushDecision: legacy/fallback decision source kept for historical rows
	cardTodoRemind         = "todo_remind"          // todo_plan.go: a reminder the owner set
	cardHealthWater        = "health_water"         // health_water.go: the water reminders the owner switched on
	cardPageAlert          = "page_alert"           // pages_alert.go: a watch page's alert rule newly tripped
	cardMinutesReady       = "minutes_ready"        // kb_minutes.go: a recording's minutes
	cardDayRecap           = "day_recap"            // kb_day.go: the day's recap
	cardBriefFallback      = "brief_fallback"       // briefing_fallback.go: the briefing itself, when its run failed
	cardAgentDone          = "agent_done"           // fleet_push.go: a turn finished (the devices' "done" pushes setting)
	cardAskEscalated       = "ask_escalated"        // ask_route.go: legacy/fallback ask_escalate source kept for historical rows and untagged escalations
	cardPlanWaiting        = "plan_waiting"         // tasks_plan.go: a plan waits for Start
	cardPlanMoney          = "plan_money"           // tasks_plan.go: a plan waits for Start: a task spends money
	cardPlanOutward        = "plan_outward"         // tasks_plan.go: a plan waits for Start: a task speaks outward as the owner
	cardPlanIrreversible   = "plan_irreversible"    // tasks_plan.go: a plan waits for Start: an irreversible step not agreed
	cardAgentPlan          = "agent_plan"           // an Agent's plan approval card
	cardTaskFailed         = "task_failed"          // supervise_events.go notifyTaskFailed
	cardAgentPermission    = "agent_permission"     // an Agent's tool approval
	cardAgentQuestion      = "agent_question"       // an Agent's question or screen dialog
	cardAgentReply         = "agent_reply"          // agents_reply.go: an Agent still waits for an answer
	cardRelayRefusal       = "relay_refusal"        // relay_refusal.go
	cardAskHeld            = "ask_held"             // ask_route.go quietRemind
	cardAskUser            = "ask_user"             // a conversation's ask_user question
	cardManagerQuestion    = "manager_question"     // another question card of the manager's own tools
	cardToolPermission     = "tool_permission"      // an approval card of the manager's own tools (ask)
	cardSettingsChange     = "settings_change"      // persona_settings (settings_tool.go, extmgr self_settings.go)
	cardRepositoryAdd      = "repository_add"       // repository_add_tool.go
	cardSubscriptionSave   = "subscription_save"    // extmgr subscription_tools.go
	cardDeviceControl      = "device_control"       // extmgr: iPhone, computer or browser control asked on the phone
	cardExtensionApproval  = "extension_approval"   // extmgr: any other approval of an extension in this process
	cardNotifyUser         = "notify_user"          // tools.go: notify_user
	cardUrgentCall         = "urgent_call"          // urgent_call_tool.go
	cardDecisionDeadline   = "decision_deadline"    // decision_deadline.go: an ask_user decision past its deadline
	cardScheduleFailed     = "schedule_failed"      // schedule.go: a scheduled run failed
	cardScheduleSkipped    = "schedule_skipped"     // schedule.go tellWaitingSkip
	cardScheduleStreak     = "schedule_streak"      // schedule_streak.go
	cardScheduleLimitRetry = "schedule_limit_retry" // schedule_limit_retry.go
	cardMemoryMerge        = "memory_merge"         // memory_merge.go
	cardCaptureSorted      = "capture_sorted"       // capture_sort.go
	cardKBHistory          = "kb_history"           // kbhistory_service.go
	cardUntagged           = "untagged"             // a push no source was named for

	// Per-why_owner decision and ask_escalate sources.
	cardDecisionDirection        = "decision_direction"         // todo_ask.go pushDecision: why_owner priority or acceptance
	cardDecisionOwnerKnows       = "decision_owner_knows"       // todo_ask.go pushDecision: why_owner only_owner_knows
	cardDecisionOutward          = "decision_outward"           // todo_ask.go pushDecision: why_owner visible_to_others
	cardDecisionIrreversible     = "decision_irreversible"      // todo_ask.go pushDecision: why_owner irreversible
	cardAskEscalatedDirection    = "ask_escalated_direction"    // ask_route.go: ask_escalate with why_owner priority or acceptance
	cardAskEscalatedOwnerKnows   = "ask_escalated_owner_knows"  // ask_route.go: ask_escalate with why_owner only_owner_knows
	cardAskEscalatedOutward      = "ask_escalated_outward"      // ask_route.go: ask_escalate with why_owner visible_to_others
	cardAskEscalatedIrreversible = "ask_escalated_irreversible" // ask_route.go: ask_escalate with why_owner irreversible
)

// cardCategories is each source's kind: 1-6 for owner decisions, 7 for owner-subscribed alerts/reminders,
// or 0 for none of the six. A source missing here is 0 too.
var cardCategories = map[string]int{
	// 6, only the owner in person: a password, fields or a form are typed by them.
	cardSecretForm:      cardCatInPerson,
	cardAgentSecretForm: cardCatInPerson,

	// 2, what only the owner knows.
	cardDecisionOwnerKnows:     cardCatOwnerKnows,
	cardAskEscalatedOwnerKnows: cardCatOwnerKnows,

	// 3, outward as the owner: a connector's write (mail, a chat message, a comment) goes out under their name.
	cardOutboundWrite:       cardCatOutward,
	cardPlanOutward:         cardCatOutward, // a planned task says it speaks outward as the owner
	cardDecisionOutward:     cardCatOutward,
	cardAskEscalatedOutward: cardCatOutward,

	// 4, money; 5, irreversible and never said.
	cardPlanMoney:                cardCatMoney,
	cardPlanIrreversible:         cardCatIrreversible,
	cardDecisionIrreversible:     cardCatIrreversible,
	cardAskEscalatedIrreversible: cardCatIrreversible,

	// 1, goals and direction: goal, priority and acceptance decisions, plus legacy history sources.
	cardLeaderNeedsUser:       cardCatDirection,
	cardDecision:              cardCatDirection,
	cardDecisionDirection:     cardCatDirection,
	cardAskEscalated:          cardCatDirection,
	cardAskEscalatedDirection: cardCatDirection,

	// 7, owner-subscribed alerts and reminders: neither one of the six decisions nor a platform defect.
	cardTodoRemind:    cardCatSubscribed, // a reminder the owner set
	cardHealthWater:   cardCatSubscribed, // a reminder the owner switched on
	cardPageAlert:     cardCatSubscribed, // a watch page alert the owner switched on
	cardMinutesReady:  cardCatSubscribed, // the minutes of a recording the owner made
	cardDayRecap:      cardCatSubscribed, // the recap the owner subscribed to
	cardBriefFallback: cardCatSubscribed, // the briefing the owner subscribed to, when its run failed
	cardAgentDone:     cardCatSubscribed, // the owner's own "done" pushes setting

	// 0, none of the six: a bug each time it reaches the owner.
	cardConfirmOutbound:    cardCatNone, // work handed to another Agent: nobody outside sees it, the owner's identity is not used
	cardPlanWaiting:        cardCatNone, // a plan's start: the supervisor's (the owner asked for the work)
	cardAgentPlan:          cardCatNone, // an Agent's plan: its supervisor approves it
	cardTaskFailed:         cardCatNone, // a failed start: the supervisor retries or reports it
	cardAgentPermission:    cardCatNone, // an Agent's tool approval: its supervisor's
	cardAgentQuestion:      cardCatNone, // an Agent's question: its supervisor answers
	cardAgentReply:         cardCatNone, // an Agent waiting for an answer: its supervisor's
	cardRelayRefusal:       cardCatNone, // a relay refused: between conversations
	cardAskHeld:            cardCatNone, // a held card's reminder: the supervisor did not handle it
	cardAskUser:            cardCatNone, // a conversation's own question: not by kind; one of the six goes through a decision
	cardManagerQuestion:    cardCatNone, // the manager's own question card
	cardToolPermission:     cardCatNone, // the manager's own tool approval
	cardSettingsChange:     cardCatNone, // the main conversation's own settings change (a merge or deploy grant registered for the owner)
	cardRepositoryAdd:      cardCatNone, // registering a repository: the main conversation's own bookkeeping
	cardSubscriptionSave:   cardCatNone, // saving a subscription the owner asked for: asking again is not theirs
	cardDeviceControl:      cardCatNone, // the agent drives the owner's device: execution, not one of the six
	cardExtensionApproval:  cardCatNone, // another extension approval: not by kind
	cardNotifyUser:         cardCatNone, // the conversation chose to push: results go in the briefing
	cardUrgentCall:         cardCatNone, // an urgent call: the conversation's call, not by kind
	cardDecisionDeadline:   cardCatNone, // an ask_user decision past its deadline: as ask_user
	cardScheduleFailed:     cardCatNone, // a scheduled run failed: the main conversation's to fix
	cardScheduleSkipped:    cardCatNone, // a scheduled run skipped: the main conversation's
	cardScheduleStreak:     cardCatNone, // scheduled runs failing in a row: the main conversation's
	cardScheduleLimitRetry: cardCatNone, // a run waiting out a usage limit: the main conversation's
	cardMemoryMerge:        cardCatNone, // the memory merged: housekeeping
	cardCaptureSorted:      cardCatNone, // a capture filed: housekeeping, the briefing says it
	cardKBHistory:          cardCatNone, // the knowledge base's history import: housekeeping
	cardUntagged:           cardCatNone, // nobody named the source: a bug until it is named
}

// cardCategoryOf is source's kind, 0 for none of the six or a source the table does not know.
func cardCategoryOf(source string) int { return cardCategories[source] }

// decisionCardSource maps a decision's why_owner to its fixed ledger source constant.
// Empty or unknown why_owner falls back to legacy cardDecision (kind 1).
func decisionCardSource(whyOwner string) string {
	switch strings.TrimSpace(whyOwner) {
	case whyPriority, whyAcceptance:
		return cardDecisionDirection
	case whyOnlyOwnerKnow:
		return cardDecisionOwnerKnows
	case whyVisible:
		return cardDecisionOutward
	case whyIrreversible:
		return cardDecisionIrreversible
	default:
		return cardDecision
	}
}

// escalatedCardSource maps an ask_escalate why_owner to its fixed ledger source constant.
// Empty or unknown why_owner falls back to legacy cardAskEscalated (kind 1).
func escalatedCardSource(whyOwner string) string {
	switch strings.TrimSpace(whyOwner) {
	case whyPriority, whyAcceptance:
		return cardAskEscalatedDirection
	case whyOnlyOwnerKnow:
		return cardAskEscalatedOwnerKnows
	case whyVisible:
		return cardAskEscalatedOutward
	case whyIrreversible:
		return cardAskEscalatedIrreversible
	default:
		return cardAskEscalated
	}
}

type cardSourceKey struct{}

// withCard is ctx for a push (push, pushResult) or a card (ask) made at source.
func withCard(ctx context.Context, source string) context.Context {
	return context.WithValue(ctx, cardSourceKey{}, source)
}

// cardSourceOf is the source withCard put on ctx, "" for none.
func cardSourceOf(ctx context.Context) string {
	s, _ := ctx.Value(cardSourceKey{}).(string)
	return s
}

// ownCardSource is the source of a card the manager asks itself (ask) when its caller named none.
func ownCardSource(ix proto.Interaction) string {
	switch {
	case secretOrForm(ix):
		return cardSecretForm
	case ix.ToolName == "ask_user":
		if src := questionOwnerSource(ix); src != "" {
			return src
		}
		return cardAskUser
	case hubCardSource(ix) != "":
		return hubCardSource(ix)
	case ix.Kind == proto.IxPermission:
		return cardToolPermission
	}
	return cardManagerQuestion
}

// secretOrForm: ix asks for a password, for fields or is an MCP server's form (projection cardKind's secret and form).
func secretOrForm(ix proto.Interaction) bool {
	return ix.Secret || len(ix.Fields) > 0 || ix.Kind == proto.IxElicitation
}

// outboundVerbs are the write verbs (connector.WriteVerb) of a connector's tool that put words out under the owner's
// name: such a write left to the owner is kind 3.
var outboundVerbs = []string{"send", "reply", "forward", "comment", "publish", "share", "invite", "react"}

// hubCardSource is the source of a permission card one of the manager's own tools or extensions made outside ask
// (askHub, waitPreparedCard: SessionTitle "Persona"), by its tool; "" for a card that is not one.
func hubCardSource(ix proto.Interaction) string {
	switch ix.ToolName {
	case "persona_settings":
		return cardSettingsChange
	case "repository_add":
		return cardRepositoryAdd
	case "subscription_save":
		return cardSubscriptionSave
	}
	if ix.Kind != proto.IxPermission || ix.SessionTitle != "Persona" {
		return ""
	}
	for _, p := range []string{"iphone_", "computer_", "browser_"} {
		if strings.HasPrefix(ix.ToolName, p) {
			return cardDeviceControl
		}
	}
	if ix.OutwardWrite || slices.Contains(outboundVerbs, connector.WriteVerb(ix.ToolName)) {
		return cardOutboundWrite
	}
	return cardExtensionApproval
}

// inputCardSource is the source of card ix's push (notifyInteractionAt): the manager's own card by what ask was told
// or by its tool, an Agent's by its kind. escalated: ask_route rang it up its ladder.
func (s *Service) inputCardSource(ix proto.Interaction, escalated bool) string {
	return s.inputCardSourceWithWhy(ix, escalated, "")
}

// inputCardSourceWithWhy is inputCardSource with an escalation's why_owner when known.
func (s *Service) inputCardSourceWithWhy(ix proto.Interaction, escalated bool, whyOwner string) string {
	if v, ok := s.cardAsks.Load(ix.ID); ok {
		return v.(string)
	}
	if ix.ToolName == "ask_user" { // ask_user with wait false (ask_later.go) does not go through ask
		if src := questionOwnerSource(ix); src != "" {
			return src
		}
		return cardAskUser
	}
	if src := hubCardSource(ix); src != "" { // before the form check: persona_settings may carry fields
		return src
	}
	switch {
	case secretOrForm(ix):
		return cardAgentSecretForm
	case ix.Kind == proto.IxPermission && ix.OutwardWrite:
		return cardOutboundWrite
	case escalated:
		return escalatedCardSource(whyOwner)
	case ix.Kind == proto.IxPermission:
		return cardAgentPermission
	case ix.Kind == proto.IxPlan:
		return cardAgentPlan
	}
	return cardAgentQuestion
}

// cardLedgerKey keeps the counts (Store.Setting), cardLedgerDays days of them; cardLedgerToldKey the last day told to
// the main conversation.
const (
	cardLedgerKey     = "assistant.card_ledger"
	cardLedgerToldKey = "assistant.card_ledger_told"
	cardLedgerDays    = 8
	heldNotifyTTL     = 5 * time.Minute
)

// cardCount is how many cards one source pushed (N) or had held by push grading (Held) for one owner in a day.
type cardCount struct {
	Source   string `json:"source"`
	Category int    `json:"category"`
	Owner    string `json:"owner,omitempty"`
	N        int    `json:"n"`
	Held     int    `json:"held,omitempty"`
}

// heldNotify remembers a held NotifyRequest result keyed by notification ID for callers (such as recordingHost)
// whose Notify and NoteCard calls cross a host seam returning only error.
type heldNotify struct {
	reason string
	at     time.Time
}

// cardLedger is the counts by the owner's local day (2006-01-02). Without a database they are kept in memory only and
// start again with the manager.
type cardLedger struct {
	mu      sync.Mutex
	loaded  bool
	days    map[string][]cardCount
	told    string // the last day told to the main conversation (reportCardLedger)
	heldIDs map[string]heldNotify
}

// noteCard records push n going to the owner: its source (source, else the one on ctx, else untagged), the kind the
// table gives it and its owner, the session it opens ("" for none). It takes no lock of the caller's, so it can be
// called wherever a push is sent.
func (s *Service) noteCard(ctx context.Context, source string, n Notification) {
	s.noteCardOwner(ctx, source, n.SessionID)
}

// noteCardResult records the outcome of a successful NotifyRequest (err == nil): pushed to the owner's phone when
// !res.Held (including legacy hubs that omit Held), or held by push grading when res.Held.
func (s *Service) noteCardResult(ctx context.Context, source string, n Notification, res NotifyResult) {
	s.noteCardOwnerResult(ctx, source, n.SessionID, res)
}

// noteCardOwnerResult is noteCardResult with an explicit owner.
func (s *Service) noteCardOwnerResult(ctx context.Context, source, owner string, res NotifyResult) {
	if res.Held {
		s.noteCardHeld(ctx, source, owner, res.Reason)
		return
	}
	s.noteCardOwner(ctx, source, owner)
}

// supervisingOwner is the owner of an Agent's push n (deliverCard): the conversation supervising the task of the
// session it opens, else that session. It reads the to-dos: not for a caller that may hold their lock.
func (s *Service) supervisingOwner(ctx context.Context, n Notification) string {
	if n.DeviceID != "" && n.SessionID != "" {
		if t, ok := s.todoOfSession(ctx, n.DeviceID, n.SessionID); ok && t.Supervision != nil && t.Supervision.Supervisor != "" {
			return t.Supervision.Supervisor
		}
	}
	return n.SessionID
}

// noteCardOwner is noteCard with the owner given.
func (s *Service) noteCardOwner(ctx context.Context, source, owner string) {
	source = cmp.Or(source, cardSourceOf(ctx), cardUntagged)
	cat := cardCategoryOf(source)
	if s.logf != nil {
		s.logf("assistant: card to the owner: kind %d, source %s, owner %q", cat, source, owner)
	}
	s.countCard(ctx, time.Now().In(s.location(ctx)), cardCount{Source: source, Category: cat, Owner: owner, N: 1})
}

// noteCardHeld records a push held by hub push grading (not pushed to the owner's phone).
func (s *Service) noteCardHeld(ctx context.Context, source, owner, reason string) {
	source = cmp.Or(source, cardSourceOf(ctx), cardUntagged)
	cat := cardCategoryOf(source)
	if s.logf != nil {
		s.logf("assistant: card held by push grading: kind %d, source %s, owner %q, reason %q", cat, source, owner, reason)
	}
	s.countCard(ctx, time.Now().In(s.location(ctx)), cardCount{Source: source, Category: cat, Owner: owner, Held: 1})
}

// rememberNotifyHeld caches whether notification id was held by the hub so a follow-up NoteCard call can record
// it accurately without changing module host interfaces.
func (s *Service) rememberNotifyHeld(id string, res NotifyResult) {
	if id == "" {
		return
	}
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	if !res.Held {
		delete(b.heldIDs, id)
		return
	}
	now := time.Now()
	if b.heldIDs == nil {
		b.heldIDs = map[string]heldNotify{}
	}
	for k, v := range b.heldIDs {
		if now.Sub(v.at) > heldNotifyTTL {
			delete(b.heldIDs, k)
		}
	}
	b.heldIDs[id] = heldNotify{reason: res.Reason, at: now}
}

// consumeNotifyHeld pops the held state previously recorded by rememberNotifyHeld for notification id.
func (s *Service) consumeNotifyHeld(id string) NotifyResult {
	if id == "" {
		return NotifyResult{}
	}
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.heldIDs[id]
	if !ok {
		return NotifyResult{}
	}
	delete(b.heldIDs, id)
	return NotifyResult{Held: true, Reason: h.reason}
}

// countCard adds c to the day of at, drops the days past cardLedgerDays and saves.
func (s *Service) countCard(ctx context.Context, at time.Time, c cardCount) {
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.cardsLoadedLocked(ctx) {
		return
	}
	day := at.Format(time.DateOnly)
	rows := b.days[day]
	if i := slices.IndexFunc(rows, func(r cardCount) bool { return r.Source == c.Source && r.Owner == c.Owner }); i >= 0 {
		rows[i].N += c.N
		rows[i].Held += c.Held
		rows[i].Category = c.Category // the table may have changed since
	} else {
		rows = append(rows, c)
	}
	b.days[day] = rows
	oldest := at.AddDate(0, 0, -cardLedgerDays).Format(time.DateOnly)
	for d := range b.days {
		if d < oldest {
			delete(b.days, d)
		}
	}
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(ctx, cardLedgerKey, b.days); err != nil && s.logf != nil {
			s.logf("assistant: save the card ledger: %v", err)
		}
	}
}

// cardsLoadedLocked reads the counts once; b.mu is held. False while they cannot be read (nothing is saved over them).
func (s *Service) cardsLoadedLocked(ctx context.Context) bool {
	b := &s.cards
	if b.loaded {
		return true
	}
	m := map[string][]cardCount{}
	var told string
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := st.Setting(ctx, cardLedgerKey, &m); err != nil {
			if s.logf != nil {
				s.logf("assistant: load the card ledger: %v", err)
			}
			return false
		}
		if _, err := st.Setting(ctx, cardLedgerToldKey, &told); err != nil {
			if s.logf != nil {
				s.logf("assistant: load the card ledger's last report: %v", err)
			}
			return false
		}
	}
	if m == nil {
		m = map[string][]cardCount{}
	}
	b.days, b.told, b.loaded = m, told, true
	return true
}

// cardStore is the database the counts are kept in, nil for none.
func (s *Service) cardStore() Store {
	return s.store()
}

// cardsOn is a copy of the counts of day (2006-01-02).
func (s *Service) cardsOn(ctx context.Context, day string) []cardCount {
	b := &s.cards
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.cardsLoadedLocked(ctx) {
		return nil
	}
	return slices.Clone(b.days[day])
}

// cardTally is one day's count: pushed to the phone in all, of each kind (1-6 decisions, 7 owner-subscribed alerts
// and reminders, 0 none of the six), per-source breakdowns for every kind, bugs (kind 0), and pushes held by hub
// push grading (excluded from Total, ByKind and Bugs).
type cardTally struct {
	Total        int
	ByKind       [cardCatCount]int
	BySource     [cardCatCount]map[string]int
	Bugs         map[string]int
	Held         int
	HeldBySource map[string]int
}

// tallyCards counts rows, each by the kind the table gives its source now (a row saved before a change of the table
// is counted as the table says today).
func tallyCards(rows []cardCount) cardTally {
	t := cardTally{
		Bugs:         map[string]int{},
		HeldBySource: map[string]int{},
	}
	for _, r := range rows {
		cat := cardCategoryOf(r.Source)
		if cat < 0 || cat >= cardCatCount {
			cat = cardCatNone
		}
		if r.N > 0 {
			t.Total += r.N
			t.ByKind[cat] += r.N
			if t.BySource[cat] == nil {
				t.BySource[cat] = map[string]int{}
			}
			t.BySource[cat][r.Source] += r.N
			if cat == cardCatNone {
				t.Bugs[r.Source] += r.N
			}
		}
		if r.Held > 0 {
			t.Held += r.Held
			t.HeldBySource[r.Source] += r.Held
		}
	}
	return t
}

// cardKindNames are the categories as the report names them.
var cardKindNames = [cardCatCount]string{
	"none of the six",
	"goals and direction",
	"only the owner knows",
	"outward as the owner",
	"money",
	"irreversible, never said",
	"only the owner in person",
	"owner-subscribed alerts and reminders",
}

// formatCardSources formats source counts descending by count, then ascending by source name.
func formatCardSources(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	sources := make([]string, 0, len(counts))
	for src := range counts {
		sources = append(sources, src)
	}
	sort.Slice(sources, func(i, j int) bool { // the most first, then by name
		if counts[sources[i]] != counts[sources[j]] {
			return counts[sources[i]] > counts[sources[j]]
		}
		return sources[i] < sources[j]
	})
	parts := make([]string, len(sources))
	for i, src := range sources {
		parts[i] = fmt.Sprintf("%s×%d", src, counts[src])
	}
	return strings.Join(parts, ", ")
}

// cardLedgerReport is the main conversation's message on day's pushes to the owner; "" when none was pushed or held.
func cardLedgerReport(day string, rows []cardCount) string {
	t := tallyCards(rows)
	if t.Total == 0 && t.Held == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Card ledger for %s (decision rights: the owner decides only six kinds of things). Pushed to the owner's phone: %d.\n", day, t.Total)
	for k := 1; k <= 6; k++ {
		fmt.Fprintf(&b, "%d %s: %d", k, cardKindNames[k], t.ByKind[k])
		if detail := formatCardSources(t.BySource[k]); detail != "" {
			fmt.Fprintf(&b, " — %s", detail)
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Subscribed alerts and reminders (not decisions, not bugs): %d", t.ByKind[cardCatSubscribed])
	if detail := formatCardSources(t.BySource[cardCatSubscribed]); detail != "" {
		fmt.Fprintf(&b, " — %s", detail)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "None of the six (each one a bug): %d", t.ByKind[cardCatNone])
	if detail := formatCardSources(t.Bugs); detail != "" {
		fmt.Fprintf(&b, " — %s", detail)
	}
	b.WriteString(".\n")
	if t.Held > 0 {
		fmt.Fprintf(&b, "Held by push grading (not pushed to phone): %d", t.Held)
		if detail := formatCardSources(t.HeldBySource); detail != "" {
			fmt.Fprintf(&b, " — %s", detail)
		}
		b.WriteByte('\n')
	}
	b.WriteString("For you, not the owner: settle what is none of the six with the supervisors or yourself, and do not pass this on to the owner.")
	if len(t.Bugs) > 0 {
		b.WriteString(" Each source above is a platform defect: hand each one, unless a task for it is open already, to the platform's supervisor as a task to fix where the card is made.")
	}
	return b.String()
}

// cardLedgerFrom is where the report comes from (relayMsg.From and Tool), cardLedgerTitle its label above the main
// conversation's composer.
const (
	cardLedgerFrom  = "card_ledger"
	cardLedgerTitle = "Card ledger"
)

// reportCardLedger tells the main conversation, once a day from the morning briefing's time in the owner's zone, the
// day before's count (cardLedgerReport). It is kept for the conversation without waking it (keepRelayLater): it goes
// in with the next message the manager delivers there, starts no turn, and is never pushed to the owner. A day with
// nothing pushed is told nothing. acceptLoop calls it on its tick.
func (s *Service) reportCardLedger(ctx context.Context, now time.Time) {
	loc := s.location(ctx)
	local := now.In(loc)
	hh, mm := s.briefingClock(ctx)
	if local.Hour()*60+local.Minute() < hh*60+mm {
		return
	}
	day := local.AddDate(0, 0, -1).Format(time.DateOnly)
	main := s.settings(ctx).Main
	b := &s.cards
	b.mu.Lock()
	if !s.cardsLoadedLocked(ctx) || b.told >= day {
		b.mu.Unlock()
		return
	}
	rows := slices.Clone(b.days[day])
	b.mu.Unlock()
	text := cardLedgerReport(day, rows)
	if u := unattendedReport(s.unattendedHandedOut()); u != "" {
		text = strings.TrimSpace(text + "\n\n" + u)
	}
	if text != "" {
		if main == "" {
			return // nobody to tell yet: tried again on the next tick
		}
		if _, ok := s.keepRelayLater(main, relayMsg{From: cardLedgerFrom, FromTitle: cardLedgerTitle, Tool: cardLedgerFrom, Text: text, At: now.UTC(), Internal: true}); !ok {
			return // the relay state could not be saved: tried again on the next tick
		}
		s.publish(ctx) // shown waiting above the main conversation's composer
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.told = day
	if st := s.cardStore(); st != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(ctx, cardLedgerToldKey, day); err != nil && s.logf != nil {
			s.logf("assistant: save the card ledger's last report: %v", err)
		}
	}
}
