package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// ---- POST /api/v1/inbox/{id}/suggestion ----

// Actions on a suggestion.
const (
	suggestConfirm  = "confirm"
	suggestReject   = "reject"
	suggestInstruct = "instruct"
)

type suggestionReq struct {
	Version int     `json:"version"`
	Action  string  `json:"action"`
	Text    *string `json:"text"`
	// Due is a to-do suggestion's due time as the user left it (a day or a moment, "" none); absent keeps the suggestion's.
	Due      *string `json:"due"`
	ClientID string  `json:"client_id"`
}

var (
	errSuggestionStale = i18n.ErrEnglish("inbox-errors.suggestion_stale")
	errAnswerOnly      = i18n.ErrEnglish("inbox-errors.answer_only")
)

func (s *Service) inboxSuggestionRoutes() {
	s.handle("POST /api/v1/inbox/{id}/suggestion", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in suggestionReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		switch in.Action {
		case suggestConfirm, suggestReject:
		case suggestInstruct:
			if in.Text == nil || strings.TrimSpace(*in.Text) == "" {
				apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.text_required"))
				return
			}
		default:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.suggestion_action"))
			return
		}
		it, err := s.actOnSuggestion(r.Context(), r.PathValue("id"), in, fullRole(r))
		l := i18n.OfWriter(w)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errSuggestionStale):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "suggestion_stale", "error": i18n.T(l, "assistant-inbox.suggestion_stale"), "item": it})
		case errors.Is(err, errInboxItemDone):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "inbox_item_done", "error": i18n.T(l, "assistant-inbox.item_done"), "retryable": false, "delivered": "none"})
		case errors.Is(err, errAnswerOnly):
			apierr.Write(w, http.StatusForbidden, err)
		case errors.Is(err, errMailReplyTargetChanged):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "mail_reply_target_changed", "error": i18n.T(l, "assistant-inbox.mail_target_changed"), "retryable": false, "delivered": "none"})
		case replyInDoubt(err):
			body := apierr.LocalizedBody(http.StatusBadGateway, assistantwire.CodeAssistantInDoubt, err, i18n.OfWriter(w))
			body["delivered"], body["retryable"] = "unknown", false
			apierr.WriteBody(w, http.StatusBadGateway, body)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, it)
		}
	})
}

// actOnSuggestion confirms, rejects or answers the item's current suggestion, at the version the user saw. An
// instruction at version 0 is also accepted when no suggestion is current. full is false for an answer-only device
// (the watch): it may only confirm a reply or a conclusion, as text.
func (s *Service) actOnSuggestion(ctx context.Context, id string, in suggestionReq, full bool) (InboxItem, error) {
	enabled := s.inboxConfig(ctx).Agent.Enabled
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return InboxItem{}, errNoInboxItem
	}
	g := currentSuggestion(it)
	switch {
	case !it.open():
		b.mu.Unlock()
		return InboxItem{}, errInboxItemDone
	case g == nil && (in.Version != 0 || in.Action != suggestInstruct):
		out := it.listed()
		b.mu.Unlock()
		return out, errSuggestionStale
	case g != nil && g.Version != in.Version:
		out := it.listed()
		b.mu.Unlock()
		return out, errSuggestionStale
	}
	text := ""
	if g != nil {
		text = g.Text
	}
	if in.Text != nil && strings.TrimSpace(*in.Text) != "" {
		text = strings.TrimSpace(*in.Text)
	}
	if !full && (g == nil || in.Action != suggestConfirm || g.Kind == SuggestAgent || g.Kind == SuggestTodo || g.Handback || len(g.Changes) > 0 || assistantwire.AsksAgent(text)) {
		b.mu.Unlock()
		return InboxItem{}, errAnswerOnly
	}
	if g != nil && g.Conversation != nil {
		resetInboxFollowChecks(it)
		return s.actOnHandedSuggestion(ctx, it, g, in, text) // it lets b.mu go
	}
	w := beginWrite(it)
	resetInboxFollowChecks(it)
	if it.Agent == nil { // an old draft's item, or a first instruction: its conversation's bookkeeping starts here
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	now := time.Now()
	l := b.userLang()
	edited := g != nil && text != g.Text
	// The suggestion is taken now, under the lock: a second confirmation of it (another device, a retry) is stale.
	it.Suggestion = nil
	if it.Agent.State == AgentDrafted {
		it.Agent.State = AgentSkipped
	}
	execute := g != nil && in.Action == suggestConfirm && !g.Handback
	switch {
	case in.Action == suggestReject:
		b.chainAdd(it, rejectedEntry(l, g.Text, now))
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteRejected, Text: g.Text}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case in.Action == suggestInstruct:
		words := strings.TrimSpace(*in.Text)
		b.chainAdd(it, instructedEntry(l, words, now))
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: words}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case !execute: // handback: the conversation does it
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
		b.flushInboxWakes(it, now)
		b.tell(it, inboxNote{Name: prompts.NoteHandback, Text: text, Edited: edited}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
	case g.Kind == SuggestConclusion:
		b.chainAdd(it, confirmedEntry(l, text, false, now))
		b.setHandling(it, HandlingDone, "", now)
		b.file(it, now, "user")
	default:
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
	}
	if !enabled && !execute {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now)
		b.settle(w)
	}
	last := it.Last
	b.mu.Unlock()
	if !execute || g.Kind == SuggestConclusion {
		if !enabled && !execute {
			if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion response"); failed[it.ID] {
				return InboxItem{}, errInboxNotPersisted
			}
		} else {
			s.saveInbox(ctx, []*InboxItem{it})
		}
		if enabled && it.Agent != nil && len(it.Agent.Pending) > 0 {
			s.tellSoon(ctx, it)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		return out, nil
	}
	if g.Kind == SuggestAgent {
		return s.confirmAgent(ctx, it, g, text, edited)
	}
	if g.Kind == SuggestTodo {
		due := g.Due
		if in.Due != nil {
			due = strings.TrimSpace(*in.Due)
		}
		return s.confirmTodo(ctx, it, text, due)
	}
	return s.confirmReply(ctx, it, g, text, edited, last, in.ClientID)
}

func confirmedEntry(l i18n.Lang, text string, edited bool, now time.Time) InboxChainEntry {
	key := "assistant-inbox_chain.confirmed"
	if edited {
		key = "assistant-inbox_chain.confirmed_edited"
	}
	return InboxChainEntry{Kind: ChainConfirmed, At: now.UTC(), Text: i18n.T(l, key), Detail: text, Edited: edited}
}

func rejectedEntry(l i18n.Lang, text string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainRejected, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.rejected"), Detail: clip(text, 600)}
}

func instructedEntry(l i18n.Lang, words string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainInstructed, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.instructed",
		i18n.Args{"text": clip(oneLine(words), 40)}), Detail: words}
}

// confirmReply sends the confirmed reply as shown (or as the user edited it) through the same once-only path as
// /reply, but keeps the event open: it waits for the other side (handling waiting), and the conversation is told.
// A send that fails leaves the event Needs you without a suggestion.
func (s *Service) confirmReply(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool, last, clientID string) (InboxItem, error) {
	if !validClientID(clientID) { // the same suggestion confirmed again is the same send
		clientID = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
	}
	var err error
	if target := g.MailTarget; target != nil {
		_, err = s.replyInboxCheckedDo(ctx, it.ID, text, clientID, false, false, &target.UID, &target.Version, g.Changes...)
	} else if it.Mail != nil {
		// A pre-upgrade mail suggestion cannot prove which message its text answered. Never guess after a read sync.
		err = errMailReplyTargetChanged
	} else {
		// Non-mail suggestions keep their legacy path; new mail suggestions always name a target.
		_, err = s.replyInboxDo(ctx, it.ID, text, clientID, false, false, g.Changes...)
	}
	b := &s.inbox
	now := time.Now()
	loc := s.location(ctx)
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_send"), now)
	} else {
		it.Agent.SentOf = last
		b.waitForOtherSide(it, defaultInboxFollow(now), ViaUser, now, loc, false)
		b.tell(it, inboxNote{Name: prompts.NoteSent, Text: text, Edited: edited}, false, now)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}

// confirmAgent starts the Agent with the confirmed instruction, as the event's conversation would (agent_start from
// it: the chain links the sub-session; when it stops, its last words or failure wake the conversation for the next suggestion).
func (s *Service) confirmAgent(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	self := it.Agent.SessionID
	b.mu.Unlock()
	machine := g.MachineID
	if machine == "" {
		machine = s.settings(ctx).MachineID
	}
	cwd := g.Cwd
	if cwd == "" {
		cwd = "~"
	}
	args, _ := json.Marshal(map[string]string{"machine": machine, "cwd": cwd, "prompt": text})
	res, err := s.runTool(ctx, s.settings(ctx).MachineID, self, "agent_start", args)
	enabled := s.inboxConfig(ctx).Agent.Enabled
	now := time.Now()
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_agent"), now)
	} else {
		session := ""
		for _, e := range it.Chain {
			if e.Kind == ChainAgent && e.Link != nil {
				session = e.Link.SessionID
			}
		}
		if self == "" { // no conversation recorded the start: the chain still links it
			b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
				i18n.Args{"title": clip(oneLine(text), 40)})})
		}
		b.setHandling(it, HandlingAgent, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteAgentStarted, Text: text, Edited: edited, Machine: machine, Session: session}, false, now)
		_ = res
	}
	if !enabled {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}
