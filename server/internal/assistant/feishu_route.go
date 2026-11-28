package assistant

import (
	"context"
	"maps"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu chat's access to the core's runs and to-dos. Target resolution shared with plugin channels lives in
// target_resolve.go.

// startBotRun sends text (with its pictures) for the owner's message messageID into the target's conversation, or
// starts one, and starts the run that answers it. ext are the run's channel values besides the reaction.
func (s *Service) startBotRun(ctx context.Context, rt resolvedTarget, t botTarget, messageID, text string, images []proto.Image, probe, reaction string, ext map[string]string) error {
	run := &channel.Run{ID: "fb_" + messageID, Channel: channel.Feishu, ReplyTo: messageID, Started: time.Now().UTC(), Label: rt.label,
		Probe: probe, Ext: ext}
	if run.Ext == nil {
		run.Ext = map[string]string{}
	}
	if reaction != "" {
		run.Ext["reaction"] = reaction
	}
	if run.Ext[assistantwire.FeishuTopicMayOpenTodo] != "" {
		c := &s.chans
		c.mu.Lock()
		if c.topicStarting == nil {
			c.topicStarting = make(map[string]chan struct{})
		}
		done := make(chan struct{})
		c.topicStarting[messageID] = done
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.topicStarting, messageID)
			close(done)
			c.mu.Unlock()
		}()
	}
	if rt.isNew {
		sess, err := s.Start(ctx, StartParams{Text: text, Images: images, Main: rt.main, Space: t.Space, Origin: OriginChannel, OriginRef: channel.Feishu})
		if err != nil {
			return err
		}
		run.MachineID, run.SessionID, run.TargetKind, run.Probe = sess.MachineID, sess.SessionID, targetPersona, ""
		if t.Kind == targetPersona {
			bound := botTarget{Kind: targetPersona, MachineID: sess.MachineID, SessionID: sess.SessionID}
			if run.Ext["chat"] != "" && run.Ext["thread"] != "" {
				s.larkModule().RememberTopicSession(ctx, run.Ext["chat"], run.Ext["thread"], bound, "")
			} else { // /new in private DM: the chat goes on in it
				s.larkModule().SetTarget(bound)
			}
		}
	} else {
		sid := rt.session.SessionID
		run.MachineID, run.SessionID, run.TargetKind = rt.machine, sid, t.Kind
		if run.TargetKind == targetMain {
			run.TargetKind = targetPersona
		}
		run.Base = s.lastSeq(ctx, rt.machine, sid)
		id := channel.ClientMsgID(channel.Feishu, messageID)
		run.MsgIDs = []string{id}
		p := map[string]any{"session_id": sid, "text": text, "client_msg_id": id}
		if len(images) > 0 {
			p["images"] = images
		}
		if !t.Agent() {
			s.UserSpoke(ctx, SessionRef{MachineID: rt.machine, SessionID: sid})
			var err error
			if p, err = s.SendParams(ctx, SendParams{MachineID: rt.machine, SessionID: sid, Params: p}); err != nil {
				return err
			}
		}
		if err := s.sendOnce(ctx, rt.machine, p); err != nil {
			return err
		}
	}
	s.startChannelRun(run)
	return nil
}

// decidingTopicRun looks up a message accepted for main-decides routing in this conversation.
// Folded messages keep their own eligibility and original reply anchor.
func (s *Service) decidingTopicRun(ctx context.Context, machine, session, message string) (channel.Run, bool) {
	c := &s.chans
	c.mu.Lock()
	pending := c.topicStarting[message]
	c.mu.Unlock()
	if pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
			return channel.Run{}, false
		}
	}
	if !s.isMain(ctx, session) {
		return channel.Run{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(ctx)
	for _, r := range c.st.Runs {
		if r.Channel != channel.Feishu || r.MachineID != machine || r.SessionID != session || r.Recalled {
			continue
		}
		if r.ReplyTo == message && r.Ext[assistantwire.FeishuTopicMayOpenTodo] != "" {
			return channel.Run{ReplyTo: message, SessionID: session, Ext: maps.Clone(r.Ext)}, true
		}
		for _, earlier := range r.Folded {
			if earlier.ReplyTo == message && earlier.Ext[assistantwire.FeishuTopicMayOpenTodo] != "" {
				return channel.Run{ReplyTo: message, SessionID: session, Ext: maps.Clone(earlier.Ext)}, true
			}
		}
	}
	return channel.Run{}, false
}

// todoOfThread is the todo th is bound to (open or done), from memory (the todos are loaded).
func (s *Service) todoOfThread(th TodoThread) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Thread != nil && sameThread(*t.Thread, th) {
			return copyTodoLocked(t), true
		}
	}
	return Todo{}, false
}

// ThreadRouteCandidates reads the best available agent and opening conversation of an open to-do.
func (h larkHost) ThreadRouteCandidates(ctx context.Context, todoID string) []botTarget {
	s := h.s
	t, ok := s.todoNow(todoID)
	if !ok {
		t = Todo{ID: todoID}
	}
	var candidates []botTarget
	var running []proto.Session
	for _, a := range s.taskAgents(ctx, t) {
		if a.Status != proto.StatusOffline && a.Status != proto.StatusInterrupted {
			running = append(running, a)
		}
	}
	if len(running) > 0 {
		c := pickTaskAgent(running)
		candidates = append(candidates, botTarget{Kind: targetAgent, MachineID: c.MachineID, SessionID: c.SessionID})
	}
	if t.Origin != "" {
		candidates = append(candidates, botTarget{Kind: targetPersona, SessionID: t.Origin})
	} else if a, ok := s.askTodoOf(ctx, t.ID); ok {
		candidates = append(candidates, botTarget{Kind: targetPersona, SessionID: a.SessionID})
	}
	return candidates
}
