package lark

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// threadHintEvery limits reminders for closed to-dos and unbound topics in always_todo groups.
const threadHintEvery = 10 * time.Minute

// threadHintsKeep bounds the topics remembered for it.
const threadHintsKeep = 200

// threadMessage routes the owner's message by its topic and the group's to-do policy.
func (s core) threadMessage(ctx context.Context, ev botcard.MessageEvent, st FeishuStatus, content botcard.Content) {
	msg := ev.Message
	// The hub checks the sender before handoff. A topic may reach an Agent, so check again here.
	if ev.Sender.SenderType != "user" || st.Owner == "" || ev.Sender.SenderID.OpenID != st.Owner {
		s.logf("bots: a group message not from the owner was dropped")
		return
	}
	if msg.ChatID == "" || msg.ThreadID == "" {
		s.logf("bots: a message outside a topic of a group chat was left alone")
		return // nowhere to answer in, and not a topic of a to-do
	}
	// Where the bot's own lines into this topic reply to (a todo's posts, the lark module's thread_posts.go): any message of it will do.
	s.rememberTopicMessage(ctx, msg.ChatID, msg.ThreadID, msg.MessageID)
	l := s.botLang(ctx)
	topic := &feishuTopic{Chat: msg.ChatID, Thread: msg.ThreadID}
	to := topic.target()
	if err := s.h.EnsureTodosLoaded(ctx); err != nil {
		s.logf("bots: topic message: todos not loaded: %v", err)
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		return
	}
	t, bound := s.h.TodoOfThread(msg.ChatID, msg.ThreadID)
	mode := st.TopicTodoModes[msg.ChatID].Effective()
	if !bound {
		s.unboundTopicMessage(ctx, ev, st, content, topic, mode)
		return
	}
	if !content.Forward && s.h.NoReplyCommand(openingCommandWords(ev, content)) {
		// "Don't report back": a command of its own, also for a todo that is done and not yet reported, and never passed on
		// as words (the core's todo_source.go).
		s.h.ThreadNoReply(ctx, msg.MessageID, t.ID, to)
		return
	}
	if t.Done {
		if _, asked := topicTodoCommand(openingCommandWords(ev, content)); asked || s.threadHintDue(msg.ChatID+"/"+msg.ThreadID) {
			s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.thread_done"))
		}
		return
	}
	text := strings.TrimSpace(content.Text)
	if content.Forward || text == "" && len(content.Media) == 0 {
		// Merged forwards are not opened here (others' words), and a message with nothing in it has nothing to pass on.
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.cannot_read"))
		return
	}
	// Who gets it is settled before anything is written; with no one, the words are still recorded and the user is told so.
	routes, routeErr := s.threadRoutes(ctx, t.ID, st.Features)
	recorded := false
	if text != "" {
		// Noted as the to-do's decision (the core's ledger; a failure there is only logged: the words still go to the
		// agent).
		switch s.h.TopicDecision(ctx, t.ID, i18n.T(l, "assistant-feishu.thread_question"), text, msg.MessageID) {
		case assistantwire.DecisionTodoGone: // deleted since it was looked up
			s.unboundTopicMessage(ctx, ev, st, content, topic, mode)
			return
		case assistantwire.DecisionSeen:
			return // this message was handed over before: once is all
		case assistantwire.DecisionNoted:
			recorded = true
		}
	}
	notDelivered := func(err error) {
		key := "assistant-feishu.not_sent"
		if recorded {
			key = "assistant-feishu.thread_recorded_not_sent"
		}
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, key, i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
	if text != "" {
		label := msg.ThreadID
		if strings.TrimSpace(t.ThreadTitle) != "" {
			label = t.ThreadTitle
		}
		s.h.TopicWake(ctx, t.ID, "feishu thread "+label, text, "feishu_thread:"+msg.ChatID+"/"+msg.ThreadID+"@"+msg.MessageID)
	}
	if len(routes) == 0 {
		s.logf("bots: topic message of todo %s: no one to give it to: %v", t.ID, routeErr)
		notDelivered(routeErr)
		return
	}
	content.Text = prompts.RenderTodoThreadReply(prompts.TodoThreadReply{Language: s.h.AnswerLanguage(s.hello(ctx).Locale), Title: contextText(clip(oneLine(t.Title), 80)), Text: text, Recorded: recorded})
	// A to-do that came from a question nobody answered (the core's ask_todo.go): the reply is the owner's call, and goes to the
	// conversation that asked (not to an Agent) as the answer; the to-do then closes.
	asked := false
	if convs := slices.DeleteFunc(slices.Clone(routes), func(r botTarget) bool { return r.Kind == targetAgent }); text != "" && len(convs) > 0 {
		if answer, ok := s.h.AskTodoTopicAnswer(ctx, t.ID, text); ok {
			content.Text, routes, asked = answer, convs, true
		}
	}
	var err error
	for _, target := range routes { // one that cannot take it (offline, gone, refusing) leaves it to the next
		if err = s.botSendIn(ctx, &ev, content, target, topic); err == nil {
			if asked {
				s.h.SettleAskClaim(ctx, t.ID, true)
				s.h.CloseAskTodo(ctx, t.ID)
			}
			return
		}
		s.logf("bots: topic message of todo %s to %s: %v", t.ID, target.Kind, err)
	}
	if asked {
		s.h.SettleAskClaim(ctx, t.ID, false)
	}
	notDelivered(err)
}

func (s core) unboundTopicMessage(ctx context.Context, ev botcard.MessageEvent, st FeishuStatus, content botcard.Content, topic *feishuTopic, mode assistantwire.TopicTodoMode) {
	l := s.botLang(ctx)
	note := func(text string) { s.botNoteTo(ctx, topic.target(), ev.Message.MessageID, text) }
	// Explicit /todo, /ai, or "create a to-do" requests always open a to-do, even in a bound session topic.
	if text, explicit := topicOpening(ev, content, assistantwire.TopicAnswerOnly); explicit {
		s.openTopicTodo(ctx, ev, st, content, text, topic)
		return
	}
	// A topic already bound to a conversation (space-bound topic group or scheduled-task thread delivery)
	// routes follow-up replies back to that conversation without creating a to-do or disturbing main chat.
	if boundTarget, ok := s.topicSession(ctx, topic.Chat, topic.Thread); ok {
		if content.Forward {
			note(i18n.T(l, "assistant-feishu.cannot_read"))
			return
		}
		if err := s.botAllowed(l, st.Features, boundTarget); err != nil {
			note(err.Error())
			return
		}
		if err := s.botSendIn(ctx, &ev, content, boundTarget, topic); err != nil {
			s.logf("bots: answer in bound topic session: %v", err)
			note(i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		}
		return
	}
	if text, open := topicOpening(ev, content, mode); open {
		s.openTopicTodo(ctx, ev, st, content, text, topic)
		return
	}
	if mode == assistantwire.TopicAlwaysTodo {
		if s.threadHintDue(topic.Chat + "/" + topic.Thread) {
			note(i18n.T(l, "assistant-feishu.thread_unbound"))
		}
		return
	}
	if content.Forward {
		note(i18n.T(l, "assistant-feishu.cannot_read"))
		return
	}
	// A topic group bound to a domain space starts an isolated conversation in that space per topic,
	// inheriting the space's directory, SPACE.md and context without disturbing the main conversation.
	if space := strings.TrimSpace(st.TopicSpaces[topic.Chat]); space != "" {
		target := botTarget{Kind: targetPersona, Space: space}
		if err := s.botAllowed(l, st.Features, target); err != nil {
			note(err.Error())
			return
		}
		if err := s.botSendIn(ctx, &ev, content, target, topic); err != nil {
			s.logf("bots: answer in space topic: %v", err)
			note(i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		}
		return
	}
	target := botTarget{Kind: targetMain}
	if err := s.botAllowed(l, st.Features, target); err != nil {
		note(err.Error())
		return
	}
	topic.UnboundMode = mode
	topic.Pairing = pairingKey(st)
	if err := s.botSendIn(ctx, &ev, content, target, topic); err != nil {
		s.logf("bots: answer in unbound topic: %v", err)
		note(i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
}

// threadHintDue says whether the topic key is to be told it has no open to-do now: once in threadHintEvery.
func (s core) threadHintDue(key string) bool {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if at, ok := b.hinted[key]; ok && now.Sub(at) < threadHintEvery {
		return false
	}
	if b.hinted == nil {
		b.hinted = map[string]time.Time{}
	}
	for k, at := range b.hinted {
		if now.Sub(at) >= threadHintEvery {
			delete(b.hinted, k)
		}
	}
	if len(b.hinted) >= threadHintsKeep { // a flood of topics: the hints stop rather than the table grow
		return false
	}
	b.hinted[key] = now
	return true
}
