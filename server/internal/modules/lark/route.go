package lark

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// feishuTopic is the chat and thread a message was said in.
type feishuTopic struct {
	Chat, Thread string
	UnboundMode  assistantwire.TopicTodoMode
	Pairing      string
}

// target is where what is sent for that topic goes: its chat, as a reply into the thread. The zero topic (nil) is the
// private chat, where nothing changes.
func (t *feishuTopic) target() feishuTarget {
	if t == nil {
		return feishuTarget{}
	}
	return feishuTarget{ChatID: t.Chat, InThread: true}
}

// runTo is where a run's messages go, from its channel values (Ext): under the owner's "/read" in its group
// (the lark module's read.go), the topic it was asked in, else the private chat.
func runTo(ext map[string]string) feishuTarget {
	if readRun(ext) {
		return feishuTarget{ChatID: ext[readChatKey], InThread: ext[readThreadKey] != "", NoDetour: true, Read: true}
	}
	if ext["chat"] == "" || ext["thread"] == "" {
		return feishuTarget{}
	}
	return (&feishuTopic{Chat: ext["chat"], Thread: ext["thread"]}).target()
}

// threadRoutes tries the to-do's active agent, its opening or asking conversation, then the main conversation.
// It skips targets that are disabled or unavailable. If no route is available, it returns the last failure.
func (s core) threadRoutes(ctx context.Context, todoID string, f FeishuFeatures) ([]botTarget, error) {
	tried := append(s.h.ThreadRouteCandidates(ctx, todoID), botTarget{Kind: targetMain})
	l := s.botLang(ctx)
	var out []botTarget
	var last error
	for _, c := range tried {
		if err := s.botAllowed(l, f, c); err != nil {
			last = err
			continue
		}
		if c.Kind == targetPersona && !s.h.SessionExists(ctx, "", c.SessionID) {
			continue // A to-do outlives the conversation that opened it.
		}
		if _, err := s.h.ResolveTarget(ctx, c); err != nil {
			last = err
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 && last == nil {
		last = errors.New(i18n.T(l, "assistant-feishu.no_assistant_machine"))
	}
	return out, last
}

const (
	readChatKey   = assistantwire.FeishuReadChatKey
	readThreadKey = "read_thread"
)

// readRun says a run answers a /read: its answer goes under the message in that group, quietly.
func readRun(ext map[string]string) bool { return ext[readChatKey] != "" }

// openTarget is who takes a to-do opened from a topic or from elsewhere: the main conversation, which origin is the id
// of. fail says why not, in the bot's language, "" when it can.
func (s core) openTarget(ctx context.Context, st FeishuStatus) (target botTarget, origin, fail string) {
	l := s.botLang(ctx)
	failed := func(err error) string {
		return i18n.T(l, "assistant-feishu.thread_open_failed", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	}
	target = botTarget{Kind: targetMain}
	if err := s.botAllowed(l, st.Features, target); err != nil {
		return target, "", failed(err)
	}
	rt, err := s.h.ResolveTarget(ctx, target)
	if err != nil {
		return target, "", failed(err)
	}
	origin, _ = s.h.MainChat(ctx)
	if exists := s.h.SessionExists(ctx, rt.Machine, origin); origin == "" || rt.IsNew || !exists {
		return target, "", i18n.T(l, "assistant-feishu.thread_open_no_main")
	}
	return target, origin, ""
}
