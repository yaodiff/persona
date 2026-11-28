package assistant

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Which conversation a chat message resolves to (moved out of feishu_route.go, #13404).

// botAllowed says, in l, why the chat may not send to t now (nil: it may).
func botAllowed(l i18n.Lang, f FeishuFeatures, t botTarget) error {
	switch {
	case t.Agent() && !f.Agents:
		return errors.New(i18n.T(l, "assistant-feishu.agents_off"))
	case !t.Agent() && !f.Main:
		if f.Agents {
			return errors.New(i18n.T(l, "assistant-feishu.main_off_agents_on"))
		}
		return errors.New(i18n.T(l, "assistant-feishu.chat_off"))
	}
	return nil
}

// resolvedTarget is a target as it is now: the machine and conversation, and what to call it.
type resolvedTarget struct {
	machine     string
	machineName string
	session     proto.Session
	label       string
	secondary   string
	main        bool
	isNew       bool // no conversation yet: the message starts one
}

func (s *Service) resolveBotTarget(ctx context.Context, t botTarget) (resolvedTarget, error) {
	l := s.botLang(ctx)
	switch t.Kind {
	case targetAgent:
		d, sess, err := s.findAgent(ctx, t.MachineID, t.SessionID)
		if err != nil {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.agent_not_found", i18n.Args{"id": short(t.SessionID)}))
		}
		if sess.SourceKind != "" && (sess.Capabilities == nil || !sess.Capabilities.Send) {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.agent_read_only", i18n.Args{"reason": sess.ReadOnlyReason}))
		}
		if stoppedAgent(sess) { // read-only (#4754)
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.agent_stopped"))
		}
		label, secondary := feishuSessionNames(sess, s.useHerdrTabName(ctx))
		return resolvedTarget{machine: d.ID, machineName: d.Name, session: sess, label: label + " · " + d.Name, secondary: secondary}, nil
	default:
		cfg := s.settings(ctx)
		d, ok := s.device(ctx, cfg.MachineID)
		if !ok {
			return resolvedTarget{}, errors.New(i18n.T(l, "assistant-feishu.no_assistant_machine"))
		}
		sid := t.SessionID
		if t.Kind == targetMain {
			sid = cfg.Main
		}
		r := resolvedTarget{machine: d.ID, machineName: d.Name, main: sid != "" && sid == cfg.Main}
		if sid == "" {
			if t.Kind == targetPersona && t.Space != "" && !s.taskSpaceAllowed(ctx, t.Space) {
				return resolvedTarget{}, errors.New(i18n.T(l, "assistant-schedule.unknown_space"))
			}
			r.isNew, r.main, r.label = true, t.Kind == targetMain, i18n.T(l, "assistant-feishu.new_chat")
			if r.main {
				r.label = i18n.T(l, "assistant-feishu.main_chat")
			} else if t.Space != "" {
				r.label = t.Space
			}
			return r, nil
		}
		sess, _ := s.session(ctx, d.ID, sid)
		sess.SessionID, sess.MachineID = sid, d.ID
		r.session = sess
		r.label = conversationName(sess, cfg.Main)
		return r, nil
	}
}

func (s *Service) targetLabel(ctx context.Context, t botTarget) string {
	r, err := s.resolveBotTarget(ctx, t)
	if err != nil {
		return i18n.T(s.botLang(ctx), "assistant-feishu.not_found")
	}
	if t.Kind == targetPersona && r.isNew {
		return i18n.T(s.botLang(ctx), "assistant-feishu.new_chat_next")
	}
	return r.label
}
