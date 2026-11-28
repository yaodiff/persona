package assistant

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// sigOwnerPresent wakes a supervisor when the user becomes present (an app connects online or the
// user speaks in any conversation) while a task or supervisor conversation is waiting on an
// owner-only action, so the supervisor can issue a fresh card on the spot.
const sigOwnerPresent = "owner_present"

type ownerActionWait struct {
	MatterKey  string
	TodoID     string
	Supervisor string
	MachineID  string
	SessionID  string
	ToolName   string
	Summary    string
	ExpiredAt  time.Time
}

type ownerActionWaits struct {
	mu       sync.Mutex
	cards    map[string]ownerActionWait // matterKey -> expired owner-only card waiting for re-issue
	notified map[string]time.Time       // matterKey -> when the supervisor was nudged (dedup)
}

func (w *ownerActionWaits) initLocked() {
	if w.cards == nil {
		w.cards = map[string]ownerActionWait{}
	}
	if w.notified == nil {
		w.notified = map[string]time.Time{}
	}
}

// claimNotify atomically records that matterKey has been notified. It returns false if matterKey
// was already notified, guaranteeing the same waiting matter is never nudged twice.
func (w *ownerActionWaits) claimNotify(matterKey string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.initLocked()
	if _, already := w.notified[matterKey]; already {
		return false
	}
	w.notified[matterKey] = now
	return true
}

func (w *ownerActionWaits) unclaimNotify(matterKey string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.notified, matterKey)
}

func ownerCardMatterKey(todoID, sessionID, toolName string) string {
	if todoID != "" {
		return "todo:" + todoID + ":card:" + toolName
	}
	return "conv:" + sessionID + ":card:" + toolName
}

// noteOwnerActionCardExpired records that an owner-only card expired unanswered so that when the
// user returns, the supervisor can be woken once to re-issue the card.
func (s *Service) noteOwnerActionCardExpired(ix proto.Interaction) {
	if ix.SessionID == "" && ix.MachineID == "" {
		return
	}
	todoID := ""
	supervisor := ix.SessionID
	if todo, ok := s.taskOf(context.Background(), ix.MachineID, ix.SessionID); ok && todo.DoneAt == nil && todo.Supervision != nil {
		todoID = todo.ID
		if sup := cmp.Or(todo.Supervision.Supervisor, todo.Origin); sup != "" {
			supervisor = sup
		}
	}
	if supervisor == "" {
		return
	}
	key := ownerCardMatterKey(todoID, ix.SessionID, ix.ToolName)
	w := ownerActionWait{
		MatterKey:  key,
		TodoID:     todoID,
		Supervisor: supervisor,
		MachineID:  ix.MachineID,
		SessionID:  ix.SessionID,
		ToolName:   ix.ToolName,
		Summary:    clip(oneLine(ix.Summary), 160),
		ExpiredAt:  time.Now().UTC(),
	}
	s.ownerWaits.mu.Lock()
	s.ownerWaits.initLocked()
	s.ownerWaits.cards[key] = w
	s.ownerWaits.mu.Unlock()
}

// clearOwnerActionCardWait clears any recorded wait for an owner-only card once the user answers
// a card for the same session/task and tool.
func (s *Service) clearOwnerActionCardWait(ix proto.Interaction) {
	todoID := ""
	if todo, ok := s.taskOf(context.Background(), ix.MachineID, ix.SessionID); ok {
		todoID = todo.ID
	}
	s.ownerWaits.mu.Lock()
	defer s.ownerWaits.mu.Unlock()
	if todoID != "" {
		k := ownerCardMatterKey(todoID, ix.SessionID, ix.ToolName)
		delete(s.ownerWaits.cards, k)
		delete(s.ownerWaits.notified, k)
	}
	if ix.SessionID != "" {
		k := ownerCardMatterKey("", ix.SessionID, ix.ToolName)
		delete(s.ownerWaits.cards, k)
		delete(s.ownerWaits.notified, k)
	}
}

// hasPendingOwnerOnlyCard reports whether there is already an active (unclaimed) owner-only timed
// card pending for todo or sessionID, in which case the user already has a card to act on.
func (s *Service) hasPendingOwnerOnlyCard(todo *Todo, sessionID string) bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, p := range s.fleet.interactions {
		if p.claimed || !ownerOnlyTimedCard(p.ix) {
			continue
		}
		if sessionID != "" && p.ix.SessionID == sessionID {
			return true
		}
		if todo != nil {
			if p.ix.SessionID == todo.Origin {
				return true
			}
			if todo.Supervision != nil && todo.Supervision.Supervisor != "" && p.ix.SessionID == todo.Supervision.Supervisor {
				return true
			}
			for _, a := range todo.Agents {
				if a.active() && a.MachineID == p.ix.MachineID && a.SessionID == p.ix.SessionID {
					return true
				}
			}
		}
	}
	return false
}

// ownerAppeared is called when the hub observes that the user is present (an app came online or
// the user spoke in any conversation). excludeConv is the conversation the user just spoke in (empty
// for app_online): we do not inject a redundant supervision nudge into the very conversation the
// user is actively talking to. It wakes waiting supervisors once per waiting matter so they can
// issue a card on the spot.
func (s *Service) ownerAppeared(ctx context.Context, source, excludeConv string) {
	if ctx.Err() != nil {
		return
	}
	now := time.Now().UTC()

	// 1. Expired owner-only cards waiting to be re-issued.
	s.ownerWaits.mu.Lock()
	cardWaits := make([]ownerActionWait, 0, len(s.ownerWaits.cards))
	for _, w := range s.ownerWaits.cards {
		if _, already := s.ownerWaits.notified[w.MatterKey]; !already {
			cardWaits = append(cardWaits, w)
		}
	}
	s.ownerWaits.mu.Unlock()

	for _, w := range cardWaits {
		if excludeConv != "" && w.Supervisor == excludeConv {
			continue
		}
		var todoPtr *Todo
		if w.TodoID != "" {
			t, ok := s.todoNow(w.TodoID)
			if !ok || t.DoneAt != nil {
				s.ownerWaits.mu.Lock()
				delete(s.ownerWaits.cards, w.MatterKey)
				s.ownerWaits.mu.Unlock()
				continue
			}
			todoPtr = &t
		}
		if s.hasPendingOwnerOnlyCard(todoPtr, w.SessionID) {
			continue
		}
		if !s.ownerWaits.claimNotify(w.MatterKey, now) {
			continue
		}
		detail := ownerPresentCardDetail(w)
		if w.TodoID != "" && todoPtr != nil && todoPtr.Supervision != nil {
			s.signal(superviseSignal{
				Todo:   w.TodoID,
				Kind:   sigOwnerPresent,
				Key:    w.MatterKey,
				Detail: detail,
				At:     now,
			})
			continue
		}
		if !s.nudgeSupervisorOwnerPresent(ctx, w, now) {
			s.ownerWaits.unclaimNotify(w.MatterKey)
		}
	}

	// 2. Open supervised tasks waiting on an owner-only action.
	todos := s.todosWhere(ctx, "*", func(t *Todo) bool {
		return t.DoneAt == nil && t.Supervision != nil
	})

	for _, t := range todos {
		sup := cmp.Or(t.Supervision.Supervisor, t.Origin)
		if excludeConv != "" && sup == excludeConv {
			continue
		}
		key, detail, ok := ownerPresentTodoWait(t)
		if !ok || recentOwnerInput(t, now) {
			continue
		}
		if s.hasPendingOwnerOnlyCard(&t, t.Supervision.Supervisor) {
			continue
		}
		if !s.ownerWaits.claimNotify(key, now) {
			continue
		}
		s.signal(superviseSignal{
			Todo:   t.ID,
			Kind:   sigOwnerPresent,
			Key:    key,
			Detail: detail,
			At:     now,
		})
	}
}

func ownerPresentCardDetail(w ownerActionWait) string {
	tool := cmp.Or(w.ToolName, "secret")
	if w.Summary != "" {
		return fmt.Sprintf("之前的 %s 卡片已过期（%s），请当场重新发卡", tool, w.Summary) // i18n-ignore: supervisor prompt
	}
	return fmt.Sprintf("之前的 %s 卡片已过期，请当场重新发卡", tool) // i18n-ignore: supervisor prompt
}

// ownerPresentTodoWait reports whether supervised task t is currently waiting on an owner-only
// action, returning its deduplication key and a short detail line.
func ownerPresentTodoWait(t Todo) (key, detail string, ok bool) {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return "", "", false
	}
	if v.Waits != nil {
		key = "todo:" + t.ID + ":waits:" + v.Waits.Work.UTC().Format(time.RFC3339Nano)
		detail = clip(oneLine(v.Waits.Reason), 160)
		return key, detail, true
	}
	if v.state(t) == reviewNeedsUser && len(v.Reviews) > 0 {
		r := v.Reviews[len(v.Reviews)-1]
		if r.Verdict == reviewNeedsUser && r.WhyOwner == whyOnlyOwnerKnow {
			key = "todo:" + t.ID + ":only_owner:" + r.At.UTC().Format(time.RFC3339Nano)
			detail = clip(oneLine(cmp.Or(r.Reason, r.Note)), 160)
			return key, detail, true
		}
	}
	return "", "", false
}

// ownerPresentStillHolds revalidates a sigOwnerPresent signal at digest delivery time.
func (s *Service) ownerPresentStillHolds(t Todo, g superviseSignal) bool {
	if t.DoneAt != nil || t.Supervision == nil {
		return false
	}
	if s.hasPendingOwnerOnlyCard(&t, t.Supervision.Supervisor) {
		return false
	}
	if strings.Contains(g.Key, ":card:") {
		s.ownerWaits.mu.Lock()
		_, still := s.ownerWaits.cards[g.Key]
		s.ownerWaits.mu.Unlock()
		return still
	}
	key, _, ok := ownerPresentTodoWait(t)
	return ok && key == g.Key
}

// nudgeSupervisorOwnerPresent sends a direct (or queued) supervision message to a standalone
// supervisor conversation whose owner-only card expired without an associated supervised todo.
func (s *Service) nudgeSupervisorOwnerPresent(ctx context.Context, w ownerActionWait, now time.Time) bool {
	here := s.settings(ctx)
	machine := cmp.Or(w.MachineID, here.MachineID)
	to := w.Supervisor
	if machine == "" || to == "" {
		return false
	}
	if !s.autoSupervisionOn(ctx) {
		return false
	}
	text := fmt.Sprintf(
		"📋 督办：用户已经在线或刚发来消息\n（这是 Persona 的督办消息，不是用户发的。）\n<persona-supervision level=\"triage\" />\n你之前发起的「%s」操作卡片在用户未查看时已过期，用户现在已出现，请立即当场重新发卡让用户处理。", // i18n-ignore: supervisor prompt
		cmp.Or(w.Summary, w.ToolName, "需要用户本人操作"),
	)
	msg := relayMsg{
		From:      superviseTool,
		FromTitle: "督办", // i18n-ignore: supervisor prompt
		Tool:      superviseTool,
		Text:      text,
		Urgent:    true,
		At:        now.UTC(),
	}
	c, ok := s.session(ctx, machine, to)
	if ok && (c.Starting || c.Status == proto.StatusWorking || s.relayWaits(to, msg)) {
		n, err := s.queueRelayDurable(to, msg)
		if err != nil || n == 0 {
			return false
		}
		s.publish(ctx)
		if c.Status != proto.StatusWorking && !c.Starting {
			s.drainRelays(machine, []proto.Session{c})
		}
		return true
	}
	if err := s.tryDirectRelay(ctx, machine, to, &msg); err != nil {
		if relaySuppressed(err) {
			return false
		}
		n, qerr := s.queueRelayDurable(to, msg)
		if qerr != nil || n == 0 {
			return false
		}
		s.publish(ctx)
		return true
	}
	return true
}
