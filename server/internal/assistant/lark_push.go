package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Core decides which turns and reminder text can leave. The Lark push port lays out and delivers the copies.
func (s *Service) feishuPush(machine, session, title, body string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = s.feishuPushKeyed(ctx, machine, session, title, body, "")
}

func (s *Service) feishuPushKeyed(ctx context.Context, machine, session, title, body, key string) error {
	return s.feishuPushTold(ctx, machine, session, title, body, key, nil)
}

func (s *Service) feishuPushTold(ctx context.Context, machine, session, title, body, key string, r *copyReport) error {
	if s.larkAbsent() {
		r.skipped(channel.Feishu)
		return nil
	}
	started := false
	to := assistantwire.SessionRef{MachineID: machine, SessionID: session}
	receipt := assistantwire.LarkPushCopy{
		Started: func() {
			started = true
			r.going(pushCopy{channel: channel.Feishu})
		},
		Delivered: func() {
			s.markPushed(channel.Feishu, machine, session)
			r.went(channel.Feishu, nil)
		},
	}
	err := s.larkModule().PushKeyed(ctx, to, title, body, key, receipt)
	if started {
		r.went(channel.Feishu, errNoCopy) // ignored if the first card already landed
	} else {
		r.going() // an on but unready bot has no copy in the report
	}
	return err
}

// larkReminder binds one claimed occurrence to Core's privacy policy and validity check. Once it
// uses a generic notice, the same notice is kept for the remaining attempts.
func (s *Service) larkReminder(ctx context.Context, to assistantwire.SessionRef, todo Todo, title, body string, private, known bool, space string) assistantwire.LarkReminder {
	var noticeTitle, noticeBody string
	if !private && !known {
		noticeTitle, noticeBody = spaceNotice(s.botLang(ctx))
	}
	private = private || !known
	reminder := assistantwire.LarkReminder{
		Todo: todo.ID,
		Copy: func(ctx context.Context) (string, string, bool) {
			if !private && todo.Origin != "" {
				space, known = s.selfSpace(ctx, todo.Origin)
				private = !known || s.trackPolicy(ctx, space).Redact
			}
			if !private {
				return title, body, false
			}
			if noticeTitle == "" {
				noticeTitle, noticeBody = s.redactedNotice(ctx, s.botLang(ctx), space)
			}
			return noticeTitle, noticeBody, true
		},
		Delivered: func() { s.markPushed(channel.Feishu, to.MachineID, to.SessionID) },
	}
	if todo.RemindAt != nil {
		at := *todo.RemindAt
		reminder.Schedule = assistantwire.LarkReminderAt(at)
		reminder.Current = func() bool {
			cur, ok := s.todoNow(todo.ID)
			return ok && cur.DoneAt == nil && cur.RemindAt != nil && cur.RemindAt.Equal(at)
		}
	}
	return reminder
}

// feishuMirror selects a turn the Lark module may mirror. Core owns the conversation and privacy rules,
// the live push ledger, the transcript and the claim that keeps a briefing to one card set.
func (s *Service) feishuMirror(ctx context.Context, machine, session string) {
	if s.justMeNow(machine, session) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	layer := s.larkModule()
	f, ready := layer.Features(ctx)
	if !ready {
		return
	}
	main := s.isMainOn(ctx, machine, session)
	task, delivery := s.taskDeliveryForConversation(ctx, machine, session)
	if task == "" {
		task = s.taskOfConversation(ctx, machine, session)
	}
	hasThreadDelivery := isFeishuThreadDelivery(delivery)
	if !(f.Sync || f.Pushes || hasThreadDelivery) {
		return
	}
	if !(main && f.Sync || task != "" && (f.Pushes || hasThreadDelivery)) || f.Pushes && !hasThreadDelivery && s.pushedLately(channel.Feishu, machine, session) {
		return
	}
	sess, ok := s.session(ctx, machine, session)
	if !ok || sess.WokeByTask && !main || s.trackPolicy(ctx, spaceOf(sess)).Redact || s.transientRetryTurn(ctx, sess) {
		return
	}
	asked, answer, turn, ok := s.channelLastTurnOf(ctx, machine, session)
	if !ok || strings.Contains(asked, "title=\"飞书机器人\"") { // i18n-ignore: matches the stored prompt context tag
		return
	}
	lang := s.botLang(ctx)
	label := i18n.T(lang, "assistant-feishu_cards.main_chat")
	if !main {
		label = i18n.T(lang, "assistant-feishu_cards.scheduled_task", i18n.Args{"task": task})
		cut := sess.TurnError != "" || cutOff(answer)
		if s.briefFailure(session, asked, cut) {
			return // the scheduled-run watcher sends the one-per-occurrence facts fallback
		}
		run, claimed := s.briefClaim(session, asked, turn, cut)
		if claimed {
			return
		}
		if run != nil {
			s.briefMirror(ctx, machine, session, label, run, answer, turn, delivery)
			return
		}
	}
	to := s.larkMirror(machine, session, label, main, answer, turn, delivery)
	layer.Mirror(ctx, to, lang, assistantwire.LarkMirrorTurn{
		Asked: asked, Question: clip(oneLine(stripContext(asked)), 80), Answer: answer, Turn: turn,
	})
}

// larkMirror carries the latest-turn and push-ledger checks into a retry without exposing either ledger to the module.
func (s *Service) larkMirror(machine, session, label string, main bool, answer, turn string, delivery *TaskDelivery) assistantwire.LarkMirror {
	m := assistantwire.LarkMirror{
		SessionRef: assistantwire.SessionRef{MachineID: machine, SessionID: session},
		Main:       main,
		Label:      label,
		StillDue: func(ctx context.Context, pushes bool) bool {
			if pushes && !isFeishuThreadDelivery(delivery) && s.pushedLately(channel.Feishu, machine, session) {
				return false
			}
			_, current, last, ok := s.channelLastTurnOf(ctx, machine, session)
			return ok && current == answer && last == turn
		},
	}
	if isFeishuThreadDelivery(delivery) {
		m.ThreadChatID = delivery.ChatID
		m.ThreadID = delivery.ThreadID
	}
	return m
}

func (s *Service) feishuAgentDone(machine string, sess proto.Session, head, text, place string, redact bool) {
	session := sess.SessionID
	to := assistantwire.SessionRef{MachineID: machine, SessionID: session}
	s.larkModule().AgentDone(to, func(ctx context.Context) (assistantwire.LarkAgentDone, bool) {
		// The main chat and scheduled tasks already have their own mirrors. A redacted turn stays off Feishu.
		if sess.Assistant || s.mainSessionRep(machine, session) || s.taskSessionRep(machine, session) || redact {
			return assistantwire.LarkAgentDone{}, false
		}
		preferTab := s.useHerdrTabName(ctx)
		label, secondary := feishuSessionNames(sess, preferTab)
		if tab := sess.DisplayTabName(preferTab); tab != "" {
			head = strings.Replace(head, pushTitle(sess), clip(oneLine(tab), 70), 1)
		}
		return assistantwire.LarkAgentDone{
			Head: s.agentDoneHead(ctx, machine, session, head), Text: text, Place: place,
			Secondary: secondary, ThreadTitle: label + " · " + s.machineName(machine), AwaitsReply: sess.AwaitsReply,
		}, true
	})
}

// agentDoneHead includes the task's direction and title and keeps the status mark at the front.
func (s *Service) agentDoneHead(ctx context.Context, machine, session, head string) string {
	t, ok := s.taskOf(ctx, s.canonical(machine), session)
	if !ok {
		return head
	}
	mark := ""
	for _, m := range []string{"⚠️", "⏳"} {
		if rest, found := strings.CutPrefix(head, m); found {
			mark, head = m+" ", strings.TrimSpace(rest)
			break
		}
	}
	label := s.feishuDirection(ctx, t, s.feishuDirections(ctx)) + clip(oneLine(t.Title), 70)
	if head != t.Title {
		label += " · " + head
	}
	return mark + label
}
