package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// firstReplySendID is an internal ledger key. The first input can be vouched for by the
// hub's start request even though the node intentionally assigns it no client message ID.
const firstReplySendID = assistantwire.FirstMessageIDPrefix + "answer-again"

// recoverMissingReply looks at the latest settled exchange in an Assistant chat. Session.LastText can
// still hold an earlier answer when the current turn has only tools. Use the transcript and its
// input provenance instead. Missing or ambiguous evidence costs us a recovery, never a replay.
func (s *Service) recoverMissingReply(ctx context.Context, machine string, sess proto.Session) {
	sid := sess.SessionID
	if sid == "" || missingReplyStopped(sess) ||
		len(sess.Queued) > 0 || backgroundRunning(sess) || s.queuedFor(sid) > 0 {
		return
	}
	switch sess.Status {
	case proto.StatusDone, proto.StatusIdle, proto.StatusOffline, proto.StatusInterrupted:
	default:
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var page struct {
		Items   []proto.Item `json:"items"`
		HasMore *bool        `json:"has_more"`
	}
	if err := s.historySource(ctx, machine, DeviceSessionItems, map[string]any{
		"session_id": sid, "turns": 1, "lazy_images": true,
	}, &page); err != nil {
		return
	}
	owner, reply, retried, ok := missingReplyExchange(page.Items)
	if !ok || (owner.ClientMsgID == "" && (page.HasMore == nil || *page.HasMore)) || !missingFinalReply(reply) {
		return
	}
	if !retried && sess.TurnError != "" {
		return
	}
	if s.hasPending(ctx, machine, sid) {
		return
	}

	id := fmt.Sprintf("assistant-reply-%x", sha256.Sum256([]byte(machine+"\x00"+sid+"\x00"+owner.ID)))
	lang := s.lang(ctx)
	if !retried {
		// A second completion event can arrive before the follow-up reaches the transcript.
		if !s.claimMissingReply(ctx, machine, sid, owner) {
			return
		}
		params, err := s.sendParams(ctx, SendParams{MachineID: machine, SessionID: sid, Params: map[string]any{
			"session_id": sid,
			"text":       prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: lang.Name()}),
		}})
		if err == nil {
			if !s.replyInputCurrent(ctx, machine, sid, owner) {
				return
			}
			params["client_msg_id"] = id
			params["answer_again_after"] = page.Items[len(page.Items)-1].ID
			err = s.call(ctx, machine, assistantwire.DeviceAssistantAnswerAgain, params, nil)
		}
		if err == nil {
			s.logf("assistant %s: asked for a missing reply again", short(sid))
			return
		}
		var refused *rpc.Error
		if errors.As(err, &refused) && refused.Code == assistantwire.CodeAnswerAgainChanged {
			return // a newer message, Stop or work in progress won the race
		}
		s.logf("assistant %s: could not ask for the missing reply again", short(sid))
	}
	// Keep the notice if the client reloads. Its ID also covers an uncertain send
	// that arrived and later ended without an answer.
	text := i18n.T(lang, "injected.answer_again_failed")
	writeCtx, writeCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer writeCancel()
	if !s.replyInputCurrent(writeCtx, machine, sid, owner) {
		return
	}
	if _, err := s.appendFeedOnce(writeCtx, id, sid, feedNotice, "", noticeBody{Text: text}); err != nil {
		s.logf("assistant %s: could not record the missing reply notice", short(sid))
	}
}

// missingReplyExchange returns the owner input and the output after it (or after Persona's follow-up).
// The node pages from the last user prompt, including subsequent automated inputs and wake boundaries.
func missingReplyExchange(items []proto.Item) (owner proto.Item, reply []proto.Item, retried, ok bool) {
	if len(items) == 0 || len(items) >= handoffTurnMost {
		return owner, nil, false, false
	}
	owner = items[0]
	if msg, user := extensionMessage(owner); !user || msg.Role != "user" || owner.ID == "" || owner.Queued || owner.Wake {
		return owner, nil, false, false
	}
	reply = items[1:]
	for i, it := range items[1:] {
		if it.Wake {
			return owner, nil, false, false
		}
		if it.Role != "user" || it.ToolUseID != "" || (it.Kind != proto.KindText && it.Kind != proto.KindImage) || it.Seq == owner.Seq {
			continue
		}
		// Only the generated follow-up may be part of this exchange. An ordinary second input,
		// even if queued in the same turn, makes its attribution ambiguous.
		if retried || it.Queued || it.ClientMsgID != "" || it.From != "" || prompts.ItemSource(it) != nil {
			return owner, nil, false, false
		}
		if in := prompts.ParseInjected(it.Text); in == nil || in.State != "answer_again" {
			return owner, nil, false, false
		}
		reply, retried = items[i+2:], true
	}
	return owner, reply, retried, true
}

func missingFinalReply(items []proto.Item) bool {
	if channel.Stopped(items) != "" || transcript.HasUserVisibleAction(items) {
		return false
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role != "assistant" || it.Kind != proto.KindText || it.Narration {
			continue
		}
		if it.FullSize > 0 {
			return false // a clipped answer cannot be recognized as a whole-reply placeholder
		}
		if strings.TrimSpace(it.Text) != "" {
			return channel.Placeholder(it.Text)
		}
	}
	return true
}
