package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Side conversation supervision handover (#8043, #13657, docs/assistant/unified-entry.md "Supervision").
//
// Live side conversations supervise the tasks they dispatch. When a side conversation is archived or gone, its open
// tasks move to the main conversation's follow-up immediately, once, with a notice explaining why. When a live side
// conversation leaves a review waiting for sideReviewAfter while it is not in a turn, the review is handed to main too.
// In both cases, origin stays on the task so main chat keeps the origin attribution and link, and the supervisor swap
// takes the task's review lock so exactly one conversation can review a task at a time.

// sideReviewAfter is how long a task may wait for a side conversation's review before the review is handed to the
// main conversation's follow-up.
var sideReviewAfter = 30 * time.Minute

// handOverUnreviewed, from the supervision loop's watch (superviseWatch): moves open tasks of archived or gone side
// conversations to main chat immediately, and hands over tasks that waited for a live side conversation's review past
// sideReviewAfter.
func (s *Service) handOverUnreviewed(ctx context.Context, now time.Time) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.MachineID == "" {
		return
	}
	statuses := map[string]string{} // each side conversation's gone/archived status is checked once
	away := map[string]bool{}       // each live side conversation is asked after once
	for _, t := range s.todosOf(ctx, "*") {
		v := t.Supervision
		if v == nil || t.closedForGood() {
			continue
		}
		side := v.Supervisor
		if !s.sideSupervisor(ctx, cfg, side) {
			continue
		}
		status, checked := statuses[side]
		if !checked {
			status = s.sideConversationStatus(ctx, cfg, side)
			statuses[side] = status
		}
		if status != "" {
			s.handOverSideTask(ctx, t, side, cfg.Main, status)
			continue
		}
		if t.DoneAt != nil || v.state(t) != reviewPending || now.Sub(v.UpdatedAt) < sideReviewAfter {
			continue
		}
		if _, asked := away[side]; !asked {
			away[side] = s.conversationAway(ctx, cfg, side)
		}
		if away[side] {
			s.handOverReview(ctx, t, side, cfg.Main, now)
		}
	}
}

// handOverArchivedSide immediately hands open tasks supervised by a newly archived side conversation to main chat.
func (s *Service) handOverArchivedSide(ctx context.Context, id string) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || !s.sideSupervisor(ctx, cfg, id) {
		return
	}
	for _, t := range s.todosOf(ctx, "*") {
		if t.closedForGood() || t.Supervision == nil || t.Supervision.Supervisor != id {
			continue
		}
		s.handOverSideTask(ctx, t, id, cfg.Main, "archived")
	}
}

// sideSupervisor: supervisor is a side conversation that supervises what it handed out: not the main
// conversation, a leader conversation, the background supervisor or a task leader's agent.
func (s *Service) sideSupervisor(ctx context.Context, cfg Settings, supervisor string) bool {
	return supervisor != "" && supervisor != cfg.Main && !s.isLeader(ctx, supervisor) && !s.isBackground(ctx, supervisor) &&
		!s.isLeaderRef(supervisor)
}

// sideConversationStatus returns "archived" when id is archived, "gone" when the machine confirms id is missing or no
// longer an assistant conversation, or "" when id is live or cannot be determined right now.
func (s *Service) sideConversationStatus(ctx context.Context, cfg Settings, id string) string {
	if s.isArchived(id) {
		return "archived"
	}
	if cfg.MachineID == "" {
		return ""
	}
	if c, ok := s.session(ctx, cfg.MachineID, id); ok {
		if !c.Assistant {
			return "gone"
		}
		return ""
	}
	if d, ok := s.device(ctx, cfg.MachineID); !ok || !d.Online || !d.Reachable {
		return ""
	}
	list, err := s.sessions(ctx, cfg.MachineID)
	if err != nil {
		return ""
	}
	for _, c := range list {
		if c.SessionID == id {
			if !c.Assistant {
				return "gone"
			}
			return ""
		}
	}
	if _, deleted, err := s.deletedConversation(ctx, cfg.MachineID, id); err == nil && deleted {
		return "gone"
	}
	if sideRuntimeInventoryReady(id, list) {
		return "gone"
	}
	return ""
}

// conversationAway: conversation id of the assistant's machine is not in a turn, as far as the machine can say: it is
// listed and neither working nor starting (a turn may be the review itself, and what is queued behind a turn goes in
// when it ends), it is no assistant conversation any more, or it is gone from the list. A machine that cannot say now
// says nothing: neither does a list with no assistant conversation at all, which is what the manager has from its
// start until the node has connected and listed (backgroundThere).
func (s *Service) conversationAway(ctx context.Context, cfg Settings, id string) bool {
	inTurn := func(c proto.Session) bool { return c.Assistant && (c.Status == proto.StatusWorking || c.Starting) }
	if c, ok := s.session(ctx, cfg.MachineID, id); ok {
		return !inTurn(c)
	}
	if d, ok := s.device(ctx, cfg.MachineID); !ok || !d.Online || !d.Reachable {
		return false
	}
	list, err := s.sessions(ctx, cfg.MachineID)
	if err != nil {
		return false
	}
	for _, c := range list {
		if c.SessionID == id {
			return !inTurn(c)
		}
	}
	if _, deleted, err := s.deletedConversation(ctx, cfg.MachineID, id); err == nil && deleted {
		return true
	}
	return sideRuntimeInventoryReady(id, list)
}

// sideRuntimeInventoryReady reports whether list is complete enough to prove that absent side conversation id is gone.
// File-backed Claude conversations are listed independently from the Antigravity runtime, so seeing only Claude
// assistant sessions cannot prove an agy-* side conversation is gone while zero agy assistant sessions are listed.
func sideRuntimeInventoryReady(id string, list []proto.Session) bool {
	if strings.HasPrefix(id, "agy-") {
		for _, c := range list {
			if c.Assistant && (c.Agent == "agy" || strings.HasPrefix(c.SessionID, "agy-")) {
				return true
			}
		}
		return false
	}
	for _, c := range list {
		if c.Assistant {
			return true
		}
	}
	return false
}

// handOverSideTask atomically moves an open task supervised by a gone or archived side conversation to main chat's
// follow-up, transfers any pending signals/asks, posts a one-time notice explaining why, and re-signals review if needed.
func (s *Service) handOverSideTask(ctx context.Context, t Todo, side, to, reason string) {
	s.reviewDismissals.actions.acquire(t.ID)
	v, changed, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool {
		return !cur.closedForGood()
	}, func(v *Supervision) bool {
		if v.Supervisor != side {
			return false
		}
		v.Supervisor = to
		v.SupervisorSource = "explicit"
		return true
	})
	s.reviewDismissals.actions.release(t.ID)
	if err != nil {
		s.logf("task %s: hand over from %s conversation %s to main: %v", t.ID, reason, short(side), err)
		return
	}
	if !changed {
		return
	}
	s.logf("task %s: side conversation %s is %s; the main conversation's follow-up takes it over", t.ID, short(side), reason)
	s.taskCard(ctx, to, t.ID, wakeActive)
	if cur, ok := s.todoNow(t.ID); ok && cur.Supervision != nil {
		s.supervise.routingMu.Lock()
		terr := s.transferPendingAsks(ctx, cur)
		s.supervise.routingMu.Unlock()
		if terr != nil {
			s.logf("task %s: transfer pending signals from %s to %s: %v", t.ID, short(side), short(to), terr)
		}
	}
	s.sideHandoverNotice(ctx, t, side, to, reason)
	if cur, ok := s.todoNow(t.ID); ok && cur.DoneAt == nil && v.state(cur) == reviewPending {
		detail := "派出它的对话已不存在，转给你跟进验收" // i18n-ignore: the supervision message is the assistant's prompt
		if reason == "archived" {
			detail = "派出它的对话已归档，转给你跟进验收" // i18n-ignore: the supervision message is the assistant's prompt
		}
		s.signalHandedOverReview(t.ID, to, reviewKey(&v), detail)
	}
}

// sideHandoverNotice writes a one-time notice into main chat's feed and pushes the owner explaining why the task moved.
func (s *Service) sideHandoverNotice(ctx context.Context, t Todo, side, to, reason string) {
	cfg := s.settings(ctx)
	l := s.lang(ctx)
	convName := s.originDisplayName(ctx, cfg, side)
	key := "assistant-tasks.side_handover_gone"
	if reason == "archived" {
		key = "assistant-tasks.side_handover_archived"
	}
	title := i18n.T(l, "assistant-tasks.side_handover_title", i18n.Args{"title": t.Title})
	body := i18n.T(l, key, i18n.Args{"title": t.Title, "origin": convName})
	feedID := "side_handover_" + t.ID + "_" + side + "_" + to
	fresh, err := s.appendFeedOnce(ctx, feedID, to, feedNotice, t.ID, noticeBody{Text: body})
	if err != nil {
		s.logf("task %s: handover notice feed: %v", t.ID, err)
		return
	}
	if !fresh {
		return
	}
	s.deliver(Notification{
		ID:        "ntf_side_handover_" + t.ID + "_" + side,
		Kind:      assistantwire.NotifyPush,
		Level:     assistantwire.LevelP1,
		DeviceID:  cfg.MachineID,
		SessionID: to,
		Title:     title,
		Body:      body,
	})
}

// handOverReview makes conversation to (the main one) the supervisor of task t, which waited for side conversation
// side's review, and signals the review again addressed to recipient to, so to gets its own deduplication fingerprint
// while the previous supervisor's receipt stays intact and, the ledger no longer naming a side conversation, this
// handover runs only once. A task reviewed, sent back to work or moved meanwhile is left alone.
func (s *Service) handOverReview(ctx context.Context, t Todo, side, to string, now time.Time) {
	var waited time.Duration
	s.reviewDismissals.actions.acquire(t.ID)
	v, changed, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool {
		return cur.DoneAt == nil
	}, func(v *Supervision) bool {
		if v.Supervisor != side || v.state(t) != reviewPending {
			return false
		}
		waited = now.Sub(v.UpdatedAt)
		v.Supervisor = to
		return true
	})
	s.reviewDismissals.actions.release(t.ID)
	if err != nil {
		s.logf("task %s: hand its review over to the main conversation: %v", t.ID, err) // tried again at the next pass
		return
	}
	if !changed {
		return
	}
	s.logf("task %s: conversation %s left its review waiting for %s; the main conversation's follow-up takes it over",
		t.ID, short(side), waited.Round(time.Minute))
	detail := fmt.Sprintf("派出它的对话过了 %d 分钟没验收，转给你验收", int(waited.Minutes())) // i18n-ignore: the supervision message is the assistant's prompt
	s.signalHandedOverReview(t.ID, to, reviewKey(&v), detail)
}

// signalHandedOverReview emits a handed-over sigReview scoped to recipient to.
// Setting Recipient preserves the previous supervisor's deduplication receipt while giving to its own fingerprint and
// ensuring digestEntry verifies that to still supervises the task; clearing any matching early sent entry lets a
// same-key handover (such as an external-PR wake or same-microsecond update) pass admission.
func (s *Service) signalHandedOverReview(todoID, to, key, detail string) {
	g := superviseSignal{Todo: todoID, Kind: sigReview, Key: key, Recipient: to, Detail: detail}
	s.supervise.mu.Lock()
	s.supervise.forgetToldLocked(g)
	s.supervise.mu.Unlock()
	s.signal(g)
}
