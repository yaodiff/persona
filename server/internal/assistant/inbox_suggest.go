package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- the suggestion tool (inbox_suggest; inbox_draft is its older name for a reply) ----

// suggestReported is one externally visible step the processing conversation says it or an Agent took. The hub records
// the statement without treating it as execution the program observed.
type suggestReported struct {
	Text string `json:"text"`
	By   string `json:"by"`
}

type suggestIn struct {
	Kind     string           `json:"kind"`
	Text     string           `json:"text"`
	Note     string           `json:"note"`
	Handback bool             `json:"handback"`
	Outcome  string           `json:"outcome"`
	Recap    string           `json:"recap"`
	Title    string           `json:"title"`
	Summary  string           `json:"summary"`
	Machine  string           `json:"machine"`
	Cwd      string           `json:"cwd"`
	Due      string           `json:"due"`
	Related  []string         `json:"related"`
	Changes  []DocChange      `json:"changes"`
	Follow   *suggestFollow   `json:"follow"`
	Reported *suggestReported `json:"reported"`
	// Item names the event: needed from a conversation it was handed to (Hand to AI, inbox_handoff.go).
	Item string `json:"item"`
}

var errNotProcessing = i18n.ErrEnglish("inbox-errors.not_processing")

// errSuggestion is a suggestion the tool refuses, said so the model can hand in a better one.
type errSuggestion struct{ msg string }

func (e errSuggestion) Error() string { return e.msg }

var errSuggestFiled = errSuggestion{"this inbox item is already filed, so the suggestion was not submitted and the user cannot see it; end this turn"}
var errReportedFiled = errSuggestion{"this inbox item is already filed, so the reported step was not recorded; end this turn"}

// check holds a suggestion to the rules before anything is kept.
func (in *suggestIn) check() error {
	in.Kind, in.Text, in.Outcome = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Text), strings.TrimSpace(in.Outcome)
	if in.Kind != "" && in.Follow != nil {
		return errSuggestion{"give either kind (reply, agent or conclusion) or follow, not both: follow says this turn only waits on the other side"}
	}
	if in.Reported != nil {
		in.Reported.Text = clip(oneLine(strings.TrimSpace(in.Reported.Text)), 120)
		in.Reported.By = strings.TrimSpace(in.Reported.By)
		if in.Reported.Text == "" {
			return errSuggestion{"reported.text is required: say one thing that was actually done"}
		}
		switch in.Reported.By {
		case "":
			in.Reported.By = ViaAssistant
		case ViaAgent, ViaAssistant:
		default:
			return errSuggestion{fmt.Sprintf("reported.by %q: one of agent, assistant", in.Reported.By)}
		}
	}
	switch in.Kind {
	case "":
		if strings.TrimSpace(in.Recap) == "" && in.Reported == nil {
			return errSuggestion{"give kind (reply, agent, conclusion or todo) with text, at least recap, or a reported step"}
		}
		return nil
	case SuggestReply, SuggestAgent, SuggestConclusion, SuggestTodo:
	default:
		return errSuggestion{fmt.Sprintf("kind %q: one of reply, agent, conclusion, todo", in.Kind)}
	}
	if in.Text == "" {
		return errSuggestion{"text is required"}
	}
	if in.Kind == SuggestReply && !in.Handback {
		// The user sends it with one tap: a blank left to fill in (<expected completion time>) would go out as it is.
		if p := draftPlaceholder(in.Text); p != "" {
			return errSuggestion{fmt.Sprintf("the reply leaves %s to fill in, and it goes out as it is: leave out what you could not find (say so in note) and submit again", p)}
		}
	}
	in.Due = strings.TrimSpace(in.Due)
	if in.Kind == SuggestTodo {
		if in.Item != "" {
			return errSuggestion{"a to-do is suggested by the conversation that processes the item, not one it was handed to"}
		}
		if err := checkSuggestedDue(in.Due); err != nil {
			return err
		}
	} else if in.Due != "" {
		return errSuggestion{"due goes with a to-do only"}
	}
	if in.Kind != SuggestReply && len(in.Changes) > 0 {
		return errSuggestion{"changes go with a reply only"}
	}
	if in.Kind == SuggestConclusion {
		switch in.Outcome {
		case "":
			in.Outcome = TierFYI
		case TierFYI, "done":
		default:
			return errSuggestion{fmt.Sprintf("outcome %q: fyi or done", in.Outcome)}
		}
	}
	return nil
}

// suggest keeps the suggestion of the conversation sessionID on its event: the item's id, or a change the item does
// not offer (nothing kept then); errNotProcessing when no event's conversation runs there.
func (s *Service) suggest(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, error) {
	id, refused, _, _, err := s.suggestFrom(ctx, sessionID, in)
	return id, refused, err
}

// suggestFrom is suggest, also naming the conversation the event was handed to when the suggestion came from one
// (in.Item; nil for the event's processing conversation). titleIgnored says a source-named item kept its own title.
func (s *Service) suggestFrom(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, *SessionRef, bool, error) {
	if err := in.check(); err != nil {
		return "", nil, nil, false, err
	}
	var follow *InboxFollow
	var loc *time.Location
	if in.Follow != nil {
		var err error
		follow, err = in.Follow.follow(time.Now())
		if err != nil {
			return "", nil, nil, false, err
		}
		loc = s.location(ctx)
	}
	if s.inboxReady(ctx) != nil {
		return "", nil, nil, false, errInboxUnavailable
	}
	b := &s.inbox
	b.mu.Lock()
	var it *InboxItem
	var from *SessionRef
	if item := strings.TrimSpace(in.Item); item != "" {
		var err error
		if it, from, err = b.handedItem(item, sessionID); err != nil {
			b.mu.Unlock()
			return "", nil, nil, false, err
		}
	} else {
		it = b.itemOfAgent(sessionID)
		// A conversation that answers before its start returned (an agent runtime already running starts a turn at
		// once): its item learns the conversation's id a moment later.
		for wait := time.Now().Add(inboxDraftWait); it == nil && b.agentStarting() && time.Now().Before(wait); {
			b.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			b.mu.Lock()
			it = b.itemOfAgent(sessionID)
		}
		if it == nil {
			b.mu.Unlock()
			return "", nil, nil, false, errNotProcessing
		}
	}
	if from == nil && (in.Kind != "" || in.Follow != nil) && !it.open() {
		b.mu.Unlock()
		return "", nil, nil, false, errSuggestFiled
	}
	if in.Reported != nil && !it.open() {
		b.mu.Unlock()
		return "", nil, nil, false, errReportedFiled
	}
	for _, ch := range in.Changes {
		if it.Doc == nil || !docOffers(*it.Doc, ch) {
			b.mu.Unlock()
			return it.ID, &ch, nil, false, nil
		}
	}
	now := time.Now().UTC()
	w := beginWrite(it)
	if r := in.Reported; r != nil {
		duplicate := false
		for i := len(it.Chain) - 1; i >= 0; i-- {
			if it.Chain[i].Kind != ChainReported {
				continue
			}
			duplicate = it.Chain[i].Detail == r.Text
			break
		}
		if !duplicate {
			b.chainAdd(it, InboxChainEntry{Kind: ChainReported, At: now,
				Text:   i18n.T(b.userLang(), "assistant-inbox_chain.reported", i18n.Args{"text": r.Text}),
				Detail: r.Text, Via: r.By})
		}
	}
	if r := strings.TrimSpace(in.Recap); r != "" {
		it.Recap = clip(r, recapMax)
	}
	titleIgnored := false
	if title := strings.TrimSpace(in.Title); title != "" {
		if suggestWritesTitle(it) {
			it.Title, it.TitleBy = clip(oneLine(title), 40), TitleByAssistant
		} else {
			titleIgnored = true
		}
	}
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		it.Summary, it.TitleBy = clip(oneLine(summary), 80), TitleByAssistant
	}
	for _, id := range in.Related {
		relate(it, b.items[strings.TrimSpace(id)])
	}
	var card *MessageCard
	var handed *handoffCard
	stop := false
	if from == nil && in.Kind == "" && in.Follow == nil && it.open() && it.Handling != nil && it.Handling.State == HandlingLooking {
		// No kind or follow: this turn offers nothing new to confirm, so the event goes back to where it was.
		back := it.Agent.Back
		if it.Suggestion != nil || back == "" || back == HandlingLooking || back == HandlingFailed {
			back = HandlingDone
		}
		it.Agent.Wake = false
		if back == HandlingWaiting {
			agentStopped := len(it.Chain) > 0 && it.Chain[len(it.Chain)-1].Kind == ChainAgentStopped
			b.resumeInboxWaiting(it, now)
			if agentStopped {
				// The stop turn's recap consumed its restoration point; later turns must not restore it again.
				it.Agent.Back = ""
			}
		} else {
			b.setHandling(it, back, "", now)
		}
	}
	if in.Kind != "" && it.open() {
		if legacySuggestion(it) != nil {
			it.SuggestSeq = max(it.SuggestSeq, 1)
		}
		it.SuggestSeq++
		g := &InboxSuggestion{Version: it.SuggestSeq, Kind: in.Kind, Text: in.Text, Note: clip(strings.TrimSpace(in.Note), 600),
			Handback: in.Handback, Due: in.Due, Changes: in.Changes, At: now, Cwd: strings.TrimSpace(in.Cwd), Conversation: from}
		if in.Kind == SuggestReply && !in.Handback {
			g.MailTarget = mailTarget(*it)
		}
		if in.Kind == SuggestAgent {
			if m, ok := s.machineByName(ctx, strings.TrimSpace(in.Machine)); ok {
				g.MachineID = m.ID
			}
		}
		it.Suggestion = g
		var link *InboxChainLink
		if from == nil {
			it.Agent.Wake, it.Agent.Back = false, ""
			b.setHandling(it, HandlingDone, "", now)
			link = &InboxChainLink{Type: LinkSession, MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
		} else {
			clearDraft(it)
			link = &InboxChainLink{Type: LinkSession, MachineID: from.MachineID, SessionID: from.SessionID}
		}
		l := b.userLang()
		switch {
		case in.Kind == SuggestConclusion:
			b.chainAdd(it, InboxChainEntry{Kind: ChainConcluded, At: now, Text: i18n.T(l, "assistant-inbox_chain.concluded"), Detail: in.Text})
			if in.Outcome == "done" {
				it.Suggestion = nil
				b.file(it, now, doneByManager)
				if from != nil {
					stop = b.closeHandling(it, "read", now) // a processing conversation still on it stops
				}
			} else {
				it.Tier = TierFYI
			}
		default:
			key := "suggested_reply"
			if in.Handback {
				key = "suggested_handback"
			} else if in.Kind == SuggestAgent {
				key = "suggested_agent"
			} else if in.Kind == SuggestTodo {
				key = "suggested_todo"
			}
			b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, At: now, Text: i18n.T(l, "assistant-inbox_chain."+key), Detail: clip(in.Text, 600), Link: link})
		}
		if in.Kind == SuggestReply && from == nil {
			card = messageCardOf(it)
		}
		handed = handoffCardOf(it, g, in.Outcome)
	}
	if in.Follow != nil && it.open() {
		if g := legacySuggestion(it); g != nil {
			it.SuggestSeq = max(it.SuggestSeq, 1)
			it.Suggestion = g
		}
		keepAgent := from == nil && it.Handling != nil && it.Handling.State == HandlingAgent
		b.waitForOtherSide(it, follow, ViaAssistant, now, loc, true)
		if keepAgent {
			// The follow is this turn's outcome, but the current Agent still owns the matter until it stops.
			it.Agent.Back = HandlingWaiting
			b.setHandling(it, HandlingAgent, "", now)
		}
	}
	id := it.ID
	b.settle(w)
	b.mu.Unlock()
	// An undone suggestion gets no card, and the conversation hears it failed: the inbox must not promise what a
	// restart would lose. Undone, the event is back where the turn found it, so handing it in again works. One that
	// something else moved meanwhile stays (that move's save writes the row) and goes on as kept.
	if s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion"); w.undone {
		return id, nil, from, titleIgnored, errInboxNotPersisted
	}
	if card != nil {
		s.messageCard(ctx, id, *card)
	}
	if handed != nil {
		s.postHandoffCard(ctx, *from, id, *handed)
	}
	if stop {
		s.tellSoon(ctx, it)
	}
	return id, nil, from, titleIgnored, nil
}

// suggestWritesTitle is whether the processing conversation may name a chat event. Grouped events keep their
// connector block too, so Group / Lark identify the chat; connector threads and source-titled items keep their title.
func suggestWritesTitle(it *InboxItem) bool {
	return (it.Group != nil || it.Lark != nil) && it.Mail == nil && it.Doc == nil && it.Moment == nil
}

// inboxDraft keeps the reply drafted in conversation sessionID (inbox_draft, the older name): suggest with kind reply.
func (s *Service) inboxDraft(ctx context.Context, sessionID, text, note string, proposed []DocChange) (string, *DocChange, error) {
	id, refused, err := s.suggest(ctx, sessionID, suggestIn{Kind: SuggestReply, Text: text, Note: note, Changes: proposed})
	if err != nil && !errors.Is(err, errInboxNotPersisted) && !errors.Is(err, errSuggestFiled) {
		return "", nil, nil
	}
	return id, refused, err
}

func (s *Service) toolInboxSuggest(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var in suggestIn
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	_, refused, from, titleIgnored, err := s.suggestFrom(ctx, self, in)
	if err != nil {
		return "", err
	}
	if refused != nil {
		return "", fmt.Errorf("change %+v is not one the item lists (ref, field and one of its values)", *refused)
	}
	notice := ""
	if titleIgnored {
		notice = " The title was left unchanged because this item keeps its source's own title; the title argument was ignored."
	}
	recorded := ""
	if in.Reported != nil {
		recorded = "The reported step is recorded. "
	}
	if in.Follow != nil {
		message := "The recap and follow-up are updated; the item is waiting on the other side."
		if in.Reported != nil && strings.TrimSpace(in.Recap) == "" {
			message = "The follow-up is updated; the item is waiting on the other side."
		}
		return recorded + message + notice, nil
	}
	if in.Kind == "" {
		if in.Reported != nil && strings.TrimSpace(in.Recap) == "" {
			return strings.TrimSpace(recorded) + notice, nil
		}
		return recorded + "The recap is updated." + notice, nil
	}
	if from != nil {
		return recorded + "The suggestion is in the user's inbox and shown in this chat as a card (no need to repeat it in full); nothing goes out until they confirm. What they do with it is told here; later messages on the item are not." + notice, nil
	}
	return recorded + "The suggestion is in the user's inbox; nothing goes out until they confirm. End this turn: what comes next (their answer, new messages) is sent here." + notice, nil
}

// legacySuggestion is an AI twin draft stored before suggestions, as a reply suggestion (version 1); nil for none.
func legacySuggestion(it *InboxItem) *InboxSuggestion {
	a := it.Agent
	if it.Suggestion != nil || it.Handling != nil || a == nil || a.State != AgentDrafted || strings.TrimSpace(a.Draft) == "" {
		return nil
	}
	g := &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: a.Draft, Note: a.Note, Changes: a.Changes}
	g.At = it.UpdatedAt.UTC()
	if a.DraftedAt != nil {
		g.At = a.DraftedAt.UTC()
	}
	return g
}

// currentSuggestion is the item's suggestion as listed (an old draft's too).
func currentSuggestion(it *InboxItem) *InboxSuggestion {
	if it.Suggestion != nil {
		return it.Suggestion
	}
	return legacySuggestion(it)
}
