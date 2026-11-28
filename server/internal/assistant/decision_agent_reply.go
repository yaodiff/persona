package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// ownerAuthoredDecisionText is the hub-side backstop against non-owner inputs settling a
// pending decision. The node's promptSource already whitelists genuine user input on the
// hook path (which carries no text), but an older node still reports a side conversation as
// "user", and the hub's own user_spoke path can carry text with provenance. Anything wrapped
// as a side conversation, carrying a message-source/relay/injection envelope, or otherwise
// attributed to an automated source is not the owner answering the ask. A user-pasted block
// protected by ProtectUserMessage has no inferred source and still counts as the owner's
// words: UserAuthoredContext is checked first, because its author=user wrapper leaves raw
// markers (including the side-conversation template) intact.
func ownerAuthoredDecisionText(text string) bool {
	if text == "" {
		return true // hook events carry no prompt text; prompt_source is their gate
	}
	// A protected paste is the owner's words even when it quotes a machine template: the
	// other classifiers (ParseInjected, ItemSource, ProtectUserMessage itself) honor the same
	// exception, so the raw-text side-conversation regex must not override it.
	if prompts.UserAuthoredContext(text) {
		return true
	}
	if prompts.IsSideConversation(text) {
		return false
	}
	if src := prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text}); src != nil && src.Kind != "" {
		return false
	}
	// ItemSource only maps injected text that carries a MessageSource kind. Plain scheduled
	// follow-ups, inbox/lost-tasks/ask_later notices and the <persona-wake>/<persona-inbox>
	// forms have none, so reject every remaining Persona-written injection explicitly.
	if prompts.ParseInjected(text) != nil {
		return false
	}
	return prompts.ParseRelaySource(text) == nil
}

// hookPromptClockSkew tolerates clock differences between the node's transcript timestamps and
// the hub's hook time when deciding whether a visible prompt belongs to the hook's own turn.
const hookPromptClockSkew = 5 * time.Second

// hookPromptAllowsDecision is the hub-side backstop for the hook path, whose event carries no
// prompt text. The node classifies prompt_source precisely once it knows side conversations;
// this catches an older node that still reports one as "user". It blocks only on concrete
// provenance evidence: if the node cannot be asked, the turn has not synced yet, or no user
// prompt is visible, the node's classification stands, so a genuine owner reply still settles.
//
// UserPromptSubmit fires before the prompt is appended to the transcript, and a spooled replay
// can arrive several turns later, so turns:1 may still show the previous turn. A prompt older
// than the hook's own time (minus clock skew) therefore cannot be the answer being submitted:
// it means the current turn has not synced, and the node's classification is trusted rather
// than letting a prior side conversation block the owner's real reply.
func (s *Service) hookPromptAllowsDecision(ctx context.Context, r SessionRef, at time.Time) bool {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.call(cctx, r.MachineID, DeviceSessionItems, map[string]any{"session_id": r.SessionID, "turns": 1, "lazy_images": true}, &res); err != nil {
		return true
	}
	_, items := splitLastTurn(res.Items)
	threshold := at.Add(-hookPromptClockSkew)
	for _, it := range items {
		if it.Role != "user" || it.Kind != proto.KindText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		// Belongs to an older turn (or the current prompt has not synced yet): the transcript
		// cannot answer for this hook, so fail open on the node's prompt_source classification.
		if it.TS.Before(threshold) {
			return true
		}
		if prompts.UserAuthoredContext(it.Text) {
			return true
		}
		if src := prompts.ItemSource(it); src != nil && src.Kind != "" {
			return false
		}
		if prompts.IsSideConversation(it.Text) || prompts.ParseInjected(it.Text) != nil || prompts.ParseRelaySource(it.Text) != nil {
			return false
		}
		return true
	}
	return true
}

type decisionAgentReply struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	ToldAt    time.Time `json:"told_at,omitzero"`
	Text      string    `json:"text,omitempty"`
	Handled   bool      `json:"handled,omitempty"`
	Closeout  string    `json:"closeout,omitempty"`
	// FromSupervisor marks a reply the owner wrote in a supervisor / direction conversation's
	// reverse-lookup path, rather than in the task's worker Agent chat. The restart sweep uses it
	// to rebuild the supervisor-specific routing (an explicit leader destination); records
	// written before this field existed stay false and keep the legacy no-To route.
	FromSupervisor bool `json:"from_supervisor,omitempty"`
}

// priorDecisionReply anchors the scan to the Agent's actual question, within this task's current round.
// The reviewer can quote it when the card's reason paraphrases it.
func (s *Service) priorDecisionReply(ctx context.Context, t Todo, question string) *decisionAgentReply {
	ag := lastAgent(t)
	if ag.SessionID == "" || ag.ReleasedAt != nil || ag.DismissedAt != nil || t.AgentAborted(ag.MachineID, ag.SessionID) {
		return nil
	}
	since := later(t.CreatedAt, t.ReopenedAt)
	since = later(since, &ag.LinkedAt)
	if r := triageLatestReview(t.Supervision); r != nil {
		since = later(since, &r.At)
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.call(ctx, ag.MachineID, DeviceSessionItems, map[string]any{"session_id": ag.SessionID, "turns": 10, "lazy_images": true}, &res); err != nil {
		return nil
	}
	question = strings.Join(strings.Fields(question), " ")
	if question == "" {
		return nil
	}
	for i := len(res.Items) - 1; i >= 0; i-- {
		it := res.Items[i]
		if it.Role != "assistant" || it.Kind != proto.KindText || it.Narration || !it.TS.After(since) ||
			!strings.Contains(strings.Join(strings.Fields(it.Text), " "), question) {
			continue
		}
		if reply := decisionTranscriptReply(res.Items[i+1:], it.TS); reply != nil {
			reply.MachineID, reply.SessionID, reply.Handled = ag.MachineID, ag.SessionID, true
			return reply
		}
		return nil
	}
	return nil
}

// decisionTranscriptReply retains the latest human text from a strictly newer transcript entry.
func decisionTranscriptReply(items []proto.Item, since time.Time) *decisionAgentReply {
	inputs := transcript.UserInputPositions(items)
	for i := len(inputs) - 1; i >= 0; i-- {
		id, _, _ := strings.Cut(inputs[i].ID, "#")
		var text []string
		var at time.Time
		for _, it := range items {
			itemID, _, _ := strings.Cut(it.ID, "#")
			if id == "" || itemID != id || it.Role != "user" || it.ToolUseID != "" || !it.TS.After(since) {
				continue
			}
			if it.FullSize != 0 || it.Kind == proto.KindImage {
				return nil // part of the latest input is unavailable as text
			}
			if it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "" {
				text = append(text, it.Text)
				at = it.TS.UTC()
			}
		}
		if len(text) > 0 {
			return &decisionAgentReply{At: at, Text: strings.Join(text, "\n")}
		}
	}
	return nil
}

// decisionReplyTurnEnded handles only a human reply and the structured closeout of that same turn.
// It hands verification back to the supervisor without closing the task.
func (s *Service) decisionReplyTurnEnded(ctx context.Context, t Todo, last proto.Session, items []proto.Item, answer string) bool {
	block := deliveryCloseout(answer)
	c, _, ok := closeout.Parse(block)
	if !ok || c.Status != proto.CloseoutDone {
		return false
	}
	id := t.ID
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	t, ok = s.todoNow(id)
	if !ok || t.DoneAt != nil || s.activeTodoOf(last.MachineID, last.SessionID) != id {
		return false
	}
	a, ok := s.askCopy(id)
	if !ok || acceptAskStale(&a, t) {
		return false
	}
	reply := decisionTranscriptReply(items, a.AskedAt)
	if reply == nil || exactDecisionOption(reply.Text, a.Options) != "" {
		return false
	}
	if r := a.AgentReply; r != nil && r.Handled {
		return r.MachineID == last.MachineID && r.SessionID == last.SessionID && r.At.Equal(reply.At)
	}
	if !decisionOpen(&a, t) || acceptAskEnded(&a, t) || !a.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision) {
		return false
	}
	if r := triageLatestReview(t.Supervision); r == nil || r.At.After(a.AskedAt) {
		return false // the latest review has changed and has not published its ask yet
	}
	reply.MachineID, reply.SessionID = last.MachineID, last.SessionID
	reply.Handled, reply.Closeout = true, clip(block, 3000)
	a.AgentReply, a.AcceptedAt, a.DueAt = reply, time.Now().UTC(), reply.At
	b := &s.accept
	b.mu.Lock()
	prev := b.asks[id]
	if prev == nil || !prev.AskedAt.Equal(a.AskedAt) || !prev.AcceptedAt.IsZero() || !prev.RecheckAt.IsZero() {
		b.mu.Unlock()
		return false
	}
	b.asks[id] = &a
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		b.asks[id] = prev
		b.mu.Unlock()
		s.logf("task %s: save decision handled in Agent chat: %v", id, err)
		return false
	}
	b.mu.Unlock()
	s.handBackAgentDecision(ctx, id, a)
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		s.updateAskCards(ctx, id, a)
	}()
	return true
}

func (s *Service) handBackAgentDecision(ctx context.Context, id string, a acceptAsk) {
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		r := triageLatestReview(v)
		if v.State != reviewNeedsUser || r == nil || r.At.After(a.AskedAt) || r.Verdict != reviewNeedsUser {
			return false
		}
		v.State = reviewPending
		return true
	}); err != nil {
		s.logf("task %s: hand handled decision back to supervisor: %v", id, err)
	}
	s.askChanged(ctx)
	s.signalAgentDecisionReply(id, a)
}

// keepPriorDecisionReply records a resolved round without posting an owner ask, under the caller's action lock.
func (s *Service) keepPriorDecisionReply(ctx context.Context, t Todo, decision reviewDecision, reply *decisionAgentReply, asked time.Time) (string, error) {
	id := t.ID
	cur, ok := s.todoNow(id)
	r := triageLatestReview(cur.Supervision)
	if !ok || cur.DoneAt != nil || r == nil || !r.At.Equal(asked) || acceptAskStale(&acceptAsk{AskedAt: asked}, cur) {
		return "", fmt.Errorf("the task's review changed while checking the Agent conversation")
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return "", errAcceptStore
	}
	prev := b.asks[id]
	a := acceptAsk{Conclusion: true, Outcome: outcomeDecide, Reason: decision.Reason, WhyOwner: decision.WhyOwner,
		Options: decision.Options, Recommend: decision.Recommend, AskedAt: asked, AcceptedAt: asked, NoCard: true,
		AgentReply: reply}
	if prev != nil {
		a.Earlier = lastOf(append(append([]string(nil), prev.Earlier...), prev.Messages...), acceptEarlierMost)
	}
	b.asks[id] = &a
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		b.asks[id] = prev
		b.mu.Unlock()
		return "", err
	}
	b.mu.Unlock()
	// A letter typed before the card cannot refer to the card's displayed order.
	label := matchDecisionOption(reply.Text, decision.Options, false)
	if label != "" {
		if err := s.recordToldDecision(ctx, id, &a, label, reply.MachineID, reply.SessionID, reply.Text); err != nil {
			s.logf("task %s: record prior Agent-chat choice: %v", id, err)
			label = "" // the supervisor still receives the owner's words and can act on the evidence
		}
	}
	s.handBackAgentDecision(ctx, id, a)
	return label, nil
}

// agentDecisionSpoke settles only an exact owner choice; anything else pauses for the supervisor.
// The same action lock as the deadline's final check fences a default already collected by a sweep.
func (s *Service) agentDecisionSpoke(ctx context.Context, r SessionRef, at time.Time, text string) {
	if !ownerAuthoredDecisionText(text) {
		// A side conversation, relay or other non-owner input must never settle or withdraw a
		// pending decision card, even if an older node reported it with prompt_source=user.
		return
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("decision: agent reply: load tasks: %v", err)
		return
	}
	id := s.activeTodoOf(r.MachineID, r.SessionID)
	if id == "" {
		return
	}
	t, ok := s.todoNow(id)
	state := t.Supervision.state(t)
	if !ok || t.DoneAt != nil || state != reviewUserCheck && state != reviewNeedsUser {
		return // input can only answer a round that is already waiting on the owner
	}
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	t, ok = s.todoNow(id)
	if ok && t.Supervision.state(t) == reviewUserCheck {
		a, err := s.acceptAskLocked(id)
		if err != nil || a == nil || !at.After(a.AskedAt) || acceptAskStale(a, t) {
			return
		}
		// The owner is asking the same Agent for more, not passing the verification card.
		if err := s.reopenTodo(ctx, id); err != nil {
			s.logf("task %s: personal verification feedback: %v", id, err)
		}
		return
	}
	if !ok || t.DoneAt != nil || t.Supervision == nil || t.Supervision.State != reviewNeedsUser {
		return
	}
	// Hook events carry no text. A node older than the prompt-source fix still reports a side
	// conversation as prompt_source=user, so read the turn's prompt and block on its provenance
	// before touching the ask. The read happens only for a genuinely pending decision. The hook
	// time bounds the read: an older transcript turn must not judge the current submission.
	if text == "" && !s.hookPromptAllowsDecision(ctx, r, at) {
		return
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	a := b.asks[id]
	if a == nil || !decideAsk(a) || a.AskedAt.IsZero() || !a.AcceptedAt.IsZero() ||
		!at.After(a.AskedAt) || acceptAskStale(a, t) || acceptAskEnded(a, t) || !a.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision) {
		b.mu.Unlock()
		return
	}
	label := exactDecisionOption(text, a.Options)
	if a.AgentReply != nil && label == "" {
		b.mu.Unlock()
		return
	}
	var err error
	if a.AgentReply == nil {
		a.AgentReply = &decisionAgentReply{MachineID: r.MachineID, SessionID: r.SessionID, At: at.UTC()}
		a.DueAt = at.UTC()
		// Even a failed write must not let this process take the default. The sweep retries persistence.
		err = s.saveAcceptErrLocked(ctx)
	}
	cp := *a
	b.mu.Unlock()
	if err != nil {
		s.logf("task %s: decision: persist agent reply: %v", id, err)
	}
	if label != "" && err == nil {
		taken, err := s.decisionToldLocked(ctx, id, label, r.MachineID, r.SessionID, text)
		if err == nil && taken {
			return
		}
		if err != nil {
			s.logf("task %s: decision: record exact agent reply: %v", id, err)
		}
	}
	s.askChanged(ctx)
	s.signalAgentDecisionReply(id, cp)
	// Card updates can involve the network; never wait for them on a user's send or a node hook.
	go func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		s.updateAskCards(ctx, id, cp)
	}()
}

// Letters refer to displayed order; labels match in full, ignoring only whitespace and punctuation.
// A letter and a normalized label pointing at different options are ambiguous, not a choice.
func exactDecisionOption(text string, options []DecisionOption) string {
	return matchDecisionOption(text, options, true)
}

func matchDecisionOption(text string, options []DecisionOption, allowLetters bool) string {
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || unicode.IsPunct(r) {
				return -1
			}
			return r
		}, s)
	}
	answer := normalize(text)
	if answer == "" {
		return ""
	}
	chosen := -1
	if allowLetters && len(answer) == 1 {
		c := answer[0]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		if c >= 'A' && int(c-'A') < len(options) {
			chosen = int(c - 'A')
		}
	}
	for i, option := range options {
		if normalize(option.Label) == answer {
			if chosen >= 0 && chosen != i {
				return ""
			}
			chosen = i
		}
	}
	if chosen < 0 {
		return ""
	}
	return options[chosen].Label
}

func agentDecisionReplyKey(a acceptAsk) string {
	key := "agent-reply@" + a.AskedAt.UTC().Format(time.RFC3339Nano)
	if a.AgentReply != nil && a.AgentReply.Handled {
		key += "/handled"
	}
	return key
}

func (s *Service) signalAgentDecisionReply(id string, a acceptAsk) {
	r := a.AgentReply
	if r == nil || !r.ToldAt.IsZero() || !r.Handled && !a.AcceptedAt.IsZero() {
		return
	}
	s.signal(superviseSignal{Todo: id, Kind: sigDecisionReply, Key: agentDecisionReplyKey(a),
		At: r.At, Machine: r.MachineID, Session: r.SessionID})
}

// signalSupervisorDecisionReply hands an owner reply written in a supervisor / direction
// conversation back to that very conversation. It is distinct from signalAgentDecisionReply,
// which keeps the shared routing (an Agent-chat reply and the restart sweep rely on digestTo's
// needs_user escalation to the main conversation and digestTarget's runtime-policy handling).
// A leader distinct from Main is named explicitly so the reverse-lookup hand-back reaches the
// direction that raised the card; a main-conversation card keeps the legacy no-To route.
func (s *Service) signalSupervisorDecisionReply(ctx context.Context, id string, a acceptAsk) {
	r := a.AgentReply
	if r == nil || !r.ToldAt.IsZero() || !r.Handled && !a.AcceptedAt.IsZero() {
		return
	}
	g := superviseSignal{Todo: id, Kind: sigDecisionReply, Key: agentDecisionReplyKey(a),
		At: r.At, Machine: r.MachineID, Session: r.SessionID}
	if t, ok := s.todoNow(id); ok && t.Supervision != nil {
		lead := t.Supervision.Supervisor
		if lead != "" && lead != s.settings(ctx).Main {
			g.To = lead
		}
	}
	s.signal(g)
}

// Recover the supervisor handoff from the persisted round after a manager restart.
func (s *Service) sweepAgentDecisionReplies() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	pending := map[string]acceptAsk{}
	for id, a := range b.asks {
		if a.AgentReply != nil && a.AgentReply.ToldAt.IsZero() && (a.AgentReply.Handled || a.AcceptedAt.IsZero()) {
			pending[id] = *a
		}
	}
	if len(pending) > 0 {
		if err := s.saveAcceptErrLocked(ctx); err != nil {
			s.logf("decision: persist pending agent replies: %v", err)
		}
	}
	b.mu.Unlock()
	for id, a := range pending {
		if a.AgentReply.Handled {
			s.handBackAgentDecision(ctx, id, a)
		} else if a.AgentReply.FromSupervisor {
			// A reply captured through the supervisor reverse-lookup must route back to the
			// direction that owns the card; the persisted flag survives the restart that erased
			// the in-memory signal's explicit destination.
			s.signalSupervisorDecisionReply(ctx, id, a)
		} else {
			s.signalAgentDecisionReply(id, a)
		}
	}
}

func (s *Service) agentDecisionRepliesTold(ctx context.Context, signals []superviseSignal) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, g := range signals {
		if g.Kind != sigDecisionReply {
			continue
		}
		a := b.asks[g.Todo]
		if a == nil || a.AgentReply == nil || agentDecisionReplyKey(*a) != g.Key || !a.AgentReply.ToldAt.IsZero() {
			continue
		}
		cp := *a.AgentReply
		cp.ToldAt = time.Now().UTC()
		prev := a.AgentReply
		a.AgentReply = &cp
		if err := s.saveAcceptErrLocked(ctx); err != nil {
			a.AgentReply = prev
			s.logf("task %s: decision: save supervisor handoff: %v", g.Todo, err)
		}
	}
}

func (s *Service) agentDecisionPrompt(machine, session string, p map[string]any) {
	if p["event"] != "UserPromptSubmit" || p["prompt_source"] != promptSourceUser {
		return
	}
	at := time.Now()
	if raw, ok := p["spooled_at"].(string); ok {
		var err error
		if at, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			return
		}
	}
	// acceptTryAs holds this todo's action lock across session.send. Waiting for it here would deadlock the read loop
	// with that send, its reverse calls and every reply. Queue the decision work instead: the send completes first, then
	// the hook acquires the lock in event order.
	s.inOrder(func() {
		s.agentDecisionSpoke(context.Background(), SessionRef{MachineID: machine, SessionID: session}, at, "")
	})
}
