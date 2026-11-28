package assistant

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const replyInputPrefix = "assistant.reply_input/"

// replyInput records the most recent vouched app input of a conversation. The node's
// echo already ties that ID to the transcript line and survives a hub switch.
type replyInput struct {
	ID        string    `json:"id"`
	At        time.Time `json:"at"`
	Attempted bool      `json:"attempted,omitempty"`
	Revision  int64     `json:"revision"`
}

type replyInputStore interface {
	Setting(context.Context, string, any) (bool, error)
	CompareSetting(context.Context, string, int64, any) (bool, error)
}

func (s *Service) rememberReplyInput(ctx context.Context, machine, sid, id string, received time.Time) {
	st, ok := s.store().(replyInputStore)
	if !ok || id == "" {
		return
	}
	key := replyInputPrefix + machine + "/" + sid
	for {
		var current replyInput
		if _, err := st.Setting(ctx, key, &current); err != nil {
			s.logf("assistant %s: could not read the direct input for reply recovery", short(sid))
			return
		}
		if current.ID == id || !received.After(current.At) {
			return
		}
		next := replyInput{ID: id, At: received.UTC(), Revision: current.Revision + 1}
		saved, err := st.CompareSetting(ctx, key, current.Revision, next)
		if err != nil {
			s.logf("assistant %s: could not record the direct input for reply recovery", short(sid))
			return
		}
		if saved {
			return
		}
	}
}

// claimMissingReply arbitrates overlapping completions, newer owner input, and a
// replacement manager. For a first message (no app ID), retain the startup proof.
func (s *Service) claimMissingReply(ctx context.Context, machine, sid string, owner proto.Item) bool {
	st, durable := s.store().(replyInputStore)
	if !durable || owner.ClientMsgID == "" {
		return s.humanSent.claimReply(sid, owner)
	}
	key := replyInputPrefix + machine + "/" + sid
	var input replyInput
	if found, err := st.Setting(ctx, key, &input); err != nil {
		return false
	} else if !found { // an input sent before the durable record was added
		return s.humanSent.claimReply(sid, owner)
	}
	if input.ID != owner.ClientMsgID || input.Attempted || time.Since(input.At) > humanSendsKeep {
		return false
	}
	revision := input.Revision
	input.Revision++
	input.Attempted = true
	saved, err := st.CompareSetting(ctx, key, revision, input)
	return err == nil && saved
}

// replyInputCurrent also guards notices, since a new app message may have been
// admitted while the follow-up was in flight and have no transcript line yet.
func (s *Service) replyInputCurrent(ctx context.Context, machine, sid string, owner proto.Item) bool {
	st, durable := s.store().(replyInputStore)
	if !durable {
		return true
	}
	var input replyInput
	found, err := st.Setting(ctx, replyInputPrefix+machine+"/"+sid, &input)
	return err == nil && (!found || (input.ID == owner.ClientMsgID && input.Attempted))
}

// recheckMissingReplies covers a manager that comes up after a node stopped
// reporting a live owner turn. Only recent recorded inputs are candidates.
func (s *Service) recheckMissingReplies(ctx context.Context, machine string, list []proto.Session) {
	st, ok := s.store().(settingsPrefixStore)
	if !ok {
		return
	}
	prefix := replyInputPrefix + machine + "/"
	records, err := st.SettingsPrefix(ctx, prefix)
	if err != nil || len(records) == 0 {
		return
	}
	for _, row := range list {
		raw := records[prefix+row.SessionID]
		var input replyInput
		if len(raw) == 0 || json.Unmarshal(raw, &input) != nil || time.Since(input.At) > ownerTurnMax {
			continue
		}
		s.afterSettle(ctx, s.recheckMissingReply, machine, row.SessionID)
	}
}

func (s *Service) recheckMissingReply(ctx context.Context, machine, sid string) {
	sess, ok := s.decoratedSession(machine, sid)
	if !ok || !sess.Assistant || sess.Starting || sess.Archived || sess.IsDeleted() {
		return
	}
	sess.TurnError, sess.TurnErrorKind = latestTurnError(sess)
	s.recoverMissingReply(ctx, machine, sess)
}

func missingReplyStopped(sess proto.Session) bool {
	return sess.TurnErrorKind == proto.TurnErrorStopped || sess.Capabilities != nil &&
		(sess.Capabilities.StopCause == proto.StopUser || sess.Capabilities.StopCause == proto.StopExit)
}
