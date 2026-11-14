package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A dispatch policy is an optional organization rule for who may hand out work. It is off by default: with no
// stored policy, or one past its expiry, every conversation behaves as without the feature. The owner sets it with
// the assistant settings (GET/PUT /api/v1/assistant); an installed extension granted the assistant.dispatch_policy
// hub scope may also write one through GET/PUT /api/v1/assistant/dispatch-policy, and a policy written that way must
// expire within dispatchPolicyMaxExpiry so a stopped or uninstalled extension leaves nothing enforced.

// Conversation classes a policy rule can name.
const (
	dispatchClassMain                 = "main"
	dispatchClassLeader               = "leader"
	dispatchClassBackgroundSupervisor = "background_supervisor"
	dispatchClassScheduled            = "scheduled"
	dispatchClassOther                = "other"
	dispatchPolicyMaxRules            = 10
	dispatchPolicyMaxMessageRunes     = 300
	dispatchPolicyMaxExpiry           = time.Hour
)

// Actions a rule can deny.
const (
	dispatchActionTaskPlan          = "task_plan"
	dispatchActionAgentStart        = "agent_start"
	dispatchActionStartConversation = "start_conversation"
	dispatchActionSendToMain        = "send_to_main"
	dispatchActionSendToLeader      = "send_to_leader"
)

var (
	dispatchClasses = []string{
		dispatchClassMain, dispatchClassLeader, dispatchClassBackgroundSupervisor,
		dispatchClassScheduled, dispatchClassOther,
	}
	dispatchActions = []string{
		dispatchActionTaskPlan, dispatchActionAgentStart, dispatchActionStartConversation,
		dispatchActionSendToMain, dispatchActionSendToLeader,
	}
)

// DispatchRule denies the listed actions of one conversation class. Message, when set, is shown to the model after
// the generic refusal line, for example where findings should be written instead.
type DispatchRule struct {
	ConversationClass string   `json:"conversation_class"`
	Deny              []string `json:"deny"`
	Message           string   `json:"message,omitempty"`
}

// DispatchPolicy is the stored policy. ExpiresAt is optional for the owner's own setting; a policy an extension
// writes must carry one no more than dispatchPolicyMaxExpiry ahead. An expired policy is treated as absent.
type DispatchPolicy struct {
	Rules     []DispatchRule `json:"rules"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
}

// validate checks the shape without touching the store. allowNoExpiry is true for the owner's own settings write.
func (p *DispatchPolicy) validate(allowNoExpiry bool, now time.Time) error {
	if p == nil {
		return nil
	}
	if len(p.Rules) == 0 {
		return errors.New("dispatch_policy.rules must name at least one rule")
	}
	if len(p.Rules) > dispatchPolicyMaxRules {
		return fmt.Errorf("dispatch_policy.rules has %d entries; at most %d", len(p.Rules), dispatchPolicyMaxRules)
	}
	seen := map[string]bool{}
	for i, rule := range p.Rules {
		if !slices.Contains(dispatchClasses, rule.ConversationClass) {
			return fmt.Errorf("dispatch_policy.rules[%d].conversation_class must be one of %v", i, dispatchClasses)
		}
		if seen[rule.ConversationClass] {
			return fmt.Errorf("dispatch_policy.rules[%d] repeats conversation_class %q", i, rule.ConversationClass)
		}
		seen[rule.ConversationClass] = true
		if len(rule.Deny) == 0 {
			return fmt.Errorf("dispatch_policy.rules[%d].deny must name at least one action", i)
		}
		for _, action := range rule.Deny {
			if !slices.Contains(dispatchActions, action) {
				return fmt.Errorf("dispatch_policy.rules[%d].deny has an unknown action %q; allowed: %v", i, action, dispatchActions)
			}
		}
		if n := len([]rune(rule.Message)); n > dispatchPolicyMaxMessageRunes {
			return fmt.Errorf("dispatch_policy.rules[%d].message is %d runes; at most %d", i, n, dispatchPolicyMaxMessageRunes)
		}
	}
	if p.ExpiresAt == nil {
		if !allowNoExpiry {
			return errors.New("dispatch_policy.expires_at is required on this route")
		}
		return nil
	}
	if !p.ExpiresAt.After(now) {
		return errors.New("dispatch_policy.expires_at must be in the future")
	}
	// Only an extension's policy is bounded: it proves it is still installed by refreshing it. The owner's own
	// settings write may keep an expiry as far ahead as they choose.
	if !allowNoExpiry {
		if max := now.Add(dispatchPolicyMaxExpiry); p.ExpiresAt.After(max) {
			return fmt.Errorf("dispatch_policy.expires_at must be at most %s ahead", dispatchPolicyMaxExpiry)
		}
	}
	return nil
}

// activeDispatchPolicy returns the stored policy while it is in force. An absent or expired policy is no policy.
func (s *Service) activeDispatchPolicy(ctx context.Context) *DispatchPolicy {
	p := s.settings(ctx).DispatchPolicy
	if p == nil || len(p.Rules) == 0 {
		return nil
	}
	if p.ExpiresAt != nil && !p.ExpiresAt.After(time.Now().UTC()) {
		return nil
	}
	return p
}

// ruleFor returns the rule that names the conversation's class, or nil.
func (p *DispatchPolicy) ruleFor(class string) *DispatchRule {
	for i := range p.Rules {
		if p.Rules[i].ConversationClass == class {
			return &p.Rules[i]
		}
	}
	return nil
}

// dispatchConversationClass names the class of sid on machine. The checks run most specific first: a conversation a
// scheduled task runs in can itself be made the main or a leader conversation later, and the stored lists can still
// contain an old main id. A task's own conversation keeps its class even when the schedule cannot be read or its task
// was deleted mid-run, because the origin book records OriginTask when the run's conversation starts: the schedule
// book alone would fail open in those windows.
func (s *Service) dispatchConversationClass(ctx context.Context, machine, sid string) string {
	if sid == "" {
		return dispatchClassOther
	}
	cfg := s.settings(ctx)
	if cfg.Main != "" && cfg.Main == sid {
		return dispatchClassMain
	}
	if slices.Contains(cfg.Leaders, sid) {
		return dispatchClassLeader
	}
	if cfg.BackgroundSupervisor != "" && cfg.BackgroundSupervisor == sid {
		return dispatchClassBackgroundSupervisor
	}
	if s.devs != nil {
		machine = s.devs.Canonical(machine) // isTaskConversation compares machine ids as the task book keeps them
	}
	if s.isTaskConversation(ctx, machine, sid) {
		return dispatchClassScheduled
	}
	if s.originIsTaskOn(machine, sid) {
		return dispatchClassScheduled
	}
	return dispatchClassOther
}

// originIsTaskOn says the origin book records sid as a conversation a scheduled task started on machine. The book is
// written before the run's first turn (startConversation → originSet), so it also covers the window before the run
// session is adopted and a turn still running after its task was deleted.
func (s *Service) originIsTaskOn(machine, sid string) bool {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	o := b.m[sid]
	return o != nil && o.Origin == OriginTask && (o.MachineID == "" || o.MachineID == machine)
}

// dispatchToolGuard refuses a model's tool call that the active policy denies this conversation class. It only sees
// the model's entry point (Service.Tool): the hub's own runTool calls never reach it.
func (s *Service) dispatchToolGuard(ctx context.Context, machine, self, tool string) error {
	action := map[string]string{
		"task_plan":        dispatchActionTaskPlan,
		"agent_start":      dispatchActionAgentStart,
		"conversation_new": dispatchActionStartConversation,
	}[tool]
	if action == "" {
		return nil
	}
	policy := s.activeDispatchPolicy(ctx)
	if policy == nil {
		return nil
	}
	rule := policy.ruleFor(s.dispatchConversationClass(ctx, machine, self))
	if rule == nil || !slices.Contains(rule.Deny, action) {
		return nil
	}
	return s.dispatchRefusal(ctx, rule, "refused_dispatch", i18n.Args{"action": action})
}

// toolCallerMachine is the machine the model's tool call came from, stored on the context in Service.Tool. The
// conversation tools only receive the conversation id, so they take the machine from here. "" when a call did not
// come through Tool (the hub's own calls): those paths are not guarded by the policy anyway.
func toolCallerMachine(ctx context.Context) string {
	c, ok := ctx.Value(toolCallerKey{}).(toolCaller)
	if !ok {
		return ""
	}
	return c.machine
}

// dispatchNewConversationGuard refuses conversation_new from a class the policy limits.
func (s *Service) dispatchNewConversationGuard(ctx context.Context, self string) error {
	return s.dispatchToolGuard(ctx, toolCallerMachine(ctx), self, "conversation_new")
}

// dispatchScheduleGuard refuses cancel_followup / delete_routine from a class the policy limits. It does not depend
// on the exact denied actions: a limited conversation must not be able to change the schedule that gives it its
// class, regardless of which actions its rule names.
func (s *Service) dispatchScheduleGuard(ctx context.Context, machine, self string) error {
	policy := s.activeDispatchPolicy(ctx)
	if policy == nil {
		return nil
	}
	rule := policy.ruleFor(s.dispatchConversationClass(ctx, machine, self))
	if rule == nil {
		return nil
	}
	return s.dispatchRefusal(ctx, rule, "refused_schedule", nil)
}

// dispatchSendGuard refuses conversation_send to the main or a leader conversation when the policy limits the
// caller's class, keeping the owner-words exception for a relay to the main conversation.
func (s *Service) dispatchSendGuard(ctx context.Context, self, targetMachine, targetSession, text string) error {
	targetClass := s.dispatchConversationClass(ctx, targetMachine, targetSession)
	return s.dispatchRelayGuard(ctx, toolCallerMachine(ctx), self, targetClass, text)
}

// dispatchRelayGuard refuses a conversation_send to the main or a leader conversation when the policy denies the
// caller's class that action. A send to any other conversation is not limited here. The owner's own words still get
// through to the main conversation: either the current turn started with one of their messages, or the sent text
// starts with their words verbatim. When the transcript cannot be read to confirm that, the send stays refused.
func (s *Service) dispatchRelayGuard(ctx context.Context, machine, self, targetClass, text string) error {
	var action string
	switch targetClass {
	case dispatchClassMain:
		action = dispatchActionSendToMain
	case dispatchClassLeader:
		action = dispatchActionSendToLeader
	default:
		return nil
	}
	policy := s.activeDispatchPolicy(ctx)
	if policy == nil {
		return nil
	}
	rule := policy.ruleFor(s.dispatchConversationClass(ctx, machine, self))
	if rule == nil || !slices.Contains(rule.Deny, action) {
		return nil
	}
	if targetClass == dispatchClassMain {
		allowed, err := s.ownerWordsRelay(ctx, machine, self, text)
		if err != nil {
			return s.dispatchRefusal(ctx, rule, "refused_unreadable", nil)
		}
		if allowed {
			return nil
		}
	}
	key := "refused_send_leader"
	if targetClass == dispatchClassMain {
		key = "refused_send_main"
	}
	return s.dispatchRefusal(ctx, rule, key, nil)
}

// dispatchRefusal builds the localized refusal line and appends the rule's own note when it has one.
func (s *Service) dispatchRefusal(ctx context.Context, rule *DispatchRule, key string, args i18n.Args) error {
	line := i18n.T(s.lang(ctx), "assistant-dispatch_policy."+key, args)
	if rule != nil && strings.TrimSpace(rule.Message) != "" {
		line += " " + strings.TrimSpace(rule.Message)
	}
	return errors.New(line)
}

// ownerWordsRelay says whether text may go from self to the main conversation as the owner's own words.
//
// A message counts as the owner's when it is a user text item with a client message id and no source marking it as a
// scheduled run, a supervision message, a relay or a system notice. Two ways qualify: the latest such input that
// started the current turn is the owner's, or the sent text starts with one of the owner's recent messages
// verbatim. A node's transcript call that fails is an error: the caller fails closed rather than guessing.
func (s *Service) ownerWordsRelay(ctx context.Context, machine, self, text string) (bool, error) {
	if machine == "" || self == "" {
		return false, errors.New("the calling conversation is unknown")
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems,
		map[string]any{"session_id": self, "turns": briefOriginTurns, "lazy_images": true}, &res); err != nil {
		s.logf("dispatch policy: read %s on %s: %v", short(self), machine, err)
		return false, err
	}
	sent := strings.TrimSpace(text)
	var turnStarterOwner bool
	for i := len(res.Items) - 1; i >= 0; i-- {
		it := res.Items[i]
		if it.Role != "user" || it.Kind != proto.KindText {
			continue
		}
		ownerText := ownerMessageText(it)
		if ownerText == "" {
			continue // a scheduled run, a supervision message, a relay or a system notice
		}
		if sent != "" && strings.HasPrefix(sent, ownerText) {
			return true, nil // the sent text starts with the owner's own words
		}
		// The latest non-queued user input is what started the current turn; a message the owner interjected while
		// the turn was already running (Queued) does not change who started it, so it is not checked here.
		if !it.Queued && !turnStarterOwner {
			turnStarterOwner = latestTextInputIsTurnStarter(res.Items, i)
		}
	}
	return turnStarterOwner, nil
}

// latestTextInputIsTurnStarter says whether the user text item at index i (an owner item) is the input that started
// the current turn: the latest user text item which is neither queued nor a background task's notice.
func latestTextInputIsTurnStarter(items []proto.Item, ownerIndex int) bool {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role != "user" || it.Kind != proto.KindText || it.Queued {
			continue
		}
		if it.Task != nil && !it.Wake {
			continue // a background task's notice inside the turn, not an input that starts one
		}
		return i == ownerIndex
	}
	return false
}

// ownerMessageText returns the owner's own words from an item, or "" when the item is not theirs. The client message
// id is the part briefOrigin lacks: Persona's own notices have no source of their own and would otherwise be taken
// for the owner's words.
func ownerMessageText(it proto.Item) string {
	if it.ClientMsgID == "" || it.From != "" || it.Wake {
		return ""
	}
	if prompts.ItemSource(it) != nil {
		return ""
	}
	return strings.TrimSpace(stripContext(it.Text))
}
