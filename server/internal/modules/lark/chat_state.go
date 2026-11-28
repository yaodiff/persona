package lark

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu bot's chat (docs/extensions/bots.md): the owner talks to Persona in the bot's private chat — the main conversation,
// another one picked with /new and /list, or an agent picked with /agents — and the answer comes back as cards;
// what is asked shows as cards there too. The hub keeps the bot's transport (its credentials, the long connection,
// pairing, the open platform's REST): it hands the owner's messages and presses here (feishu.message,
// feishu.card_action), and posts what is given it (app.call feishu.*). The bot's switches (bots.feishu features) are
// the hub's, kept with its credentials; the chat's state (bots.feishu.state) is the assistant's.

// botStateKey is the chat's state: where messages go, the threads bound to targets, the messages already taken.
const botStateKey = "bots.feishu.state"

// botTarget is where a message goes: the main conversation (whichever it is when the message is sent), another
// Persona conversation, or a coding agent (assistantwire.ChatTarget, which the core names too).
type botTarget = assistantwire.ChatTarget

const (
	targetMain    = assistantwire.ChatMain
	targetPersona = assistantwire.ChatPersona
	targetAgent   = assistantwire.ChatAgent
)

// botThread is a message the bot posted for a target (an agent picked with /agents): replies to it, and messages in its
// thread, go to that target.
type botThread struct {
	Target botTarget `json:"target"`
	Title  string    `json:"title,omitempty"`
	At     time.Time `json:"at"`
}

// botState is the chat's state (botStateKey).
type botState struct {
	Current botTarget             `json:"current"`
	Threads map[string]*botThread `json:"threads,omitempty"`
	// Runs are an older hub's turns being answered: moved to the channels' runs (takeLegacyBotRuns).
	Runs []*botRun `json:"runs,omitempty"`
	Seen []string  `json:"seen,omitempty"`
	// Cards are the cards asking what is asked that are still open in the chat, by interaction id: only where each
	// is and what it answers (never what it says), so a card answered elsewhere after a restart is still closed.
	Cards map[string]*botCardRef `json:"cards,omitempty"`
	// Pairing is the pairing the state belongs to (the owner and when they paired, feishu.status; "" not paired; nil:
	// not recorded yet, a state from before). Another one, or none, starts the chat afresh (feishuReconcile).
	Pairing *string `json:"pairing,omitempty"`
	// Topics are the latest message the owner said in each topic (chat/thread -> message), what a reply into the thread
	// goes by (thread_posts.go).
	Topics map[string]*topicAnchor `json:"topics,omitempty"`
	// TopicSessions maps "<chat_id>/<thread_id>" to the conversation bound to that topic (space-bound topic groups
	// and scheduled-task thread deliveries).
	TopicSessions map[string]*botThread `json:"topic_sessions,omitempty"`
	// Board is the standing "waiting for you" card: its message and when it was sent (the core's feishu_board.go). Only
	// the bot's `board` switch uses it.
	Board *assistantwire.ChatBoardRef `json:"board,omitempty"`
	// Companions are the other Lark bots an extension registered for the workbench (the core's feishu_companions.go).
	Companions []assistantwire.CompanionBot `json:"companions,omitempty"`
	// NudgeOffAt is when the nudge sweep first saw nudges off (nudge_after 0), cleared when it sees them on again: the
	// first sweep with them on lets the cards already due go (nudge.go). Kept here so a restart while they are
	// off does not nudge the backlog.
	NudgeOffAt time.Time `json:"nudge_off_at,omitzero"`
}

// The bot's messages a reply is routed by: each turn binds several (its progress card or agent message, each answer
// card), and pushes and synced turns more, so a count of 100 forgot a card of a few days ago. They are kept by age,
// with a cap so the state, written on every message, stays small (about 110 KB at 500).
const (
	botSeenKeep       = 300
	botThreadsKeep    = 500
	botThreadsKeepFor = 30 * 24 * time.Hour
	botCardsKeep      = 100
	botCardsDays      = 7
)

// nudgeProgress is one kind of nudge's durable delivery state (assistantwire.NudgeProgress).
type nudgeProgress = assistantwire.NudgeProgress

// botCardRef is a card kept in the chat's state: its message and the interaction it asks (assistantwire.ChatCardRef).
type botCardRef = assistantwire.ChatCardRef

// feishuBook is the chat's state in memory, the cards asking what is asked, and the bot's status a moment.
type feishuBook struct {
	mu     sync.Mutex
	loaded bool
	st     botState
	cards  map[string]botCard // interaction id -> the card asking it
	// ended: interaction id -> a question card that ended without being answered in this chat (the core's
	// feishuInteractionDone), kept for endedAskFor so an option typed for it late is not sent on as a new message
	// (botAnswerQuestion).
	ended    map[string]endedCard
	status   *FeishuStatus
	statusAt time.Time
	now      func() time.Time     // the chat's clock (Host.ChatNow)
	pressAt  map[string]time.Time // recent one-shot card presses (the /start card and the start form), for one press one thing (card_actions.go)
	// line is closed when the last message taken is through (FeishuMessage): the chat's messages are handled one at
	// a time, in the order they came.
	line chan struct{}
	// recalled are the messages the owner recalled lately (FeishuRecalled): one not in its conversation yet is not sent.
	recalled map[string]time.Time
	// hinted are the topics told lately that they are tied to no open to-do (threadHint), by chat and thread.
	hinted map[string]time.Time
}

// feishuRecalledKeep is how long a recall is remembered: a message waits at most feishuTurnWait for the one before
// it and a few minutes for its downloads.
const feishuRecalledKeep = 10 * time.Minute

// FeishuRecalled is feishu.recalled: the owner recalled a message in the bot's chat. One not in its conversation yet
// is not sent; one being answered stops its turn (recallChannelRun).
func (s core) FeishuRecalled(p FeishuRecalled) {
	if p.MessageID == "" {
		return
	}
	s.noteRecalled(p.MessageID)
	go s.h.RecallRun(p.MessageID)
}

// noteRecalled remembers that the owner recalled message id, for feishuRecalledKeep.
func (s core) noteRecalled(id string) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	for id, at := range b.recalled {
		if now.Sub(at) > feishuRecalledKeep {
			delete(b.recalled, id)
		}
	}
	if b.recalled == nil {
		b.recalled = map[string]time.Time{}
	}
	b.recalled[id] = now
}

// botRecalled says whether the owner recalled message id.
func (s core) botRecalled(id string) bool {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.recalled[id]
	return ok
}

// feishuTurnWait is how long a message waits for the one before it to go into its conversation (the timing
// "turn_wait"). Most get there in a
// second or two; one stuck on a slow download or a new conversation that is slow to start must not hold up the chat
// for the 3 minutes it may take, so after this the next message goes on without it.
const feishuTurnWait = 20 * time.Second

// feishuTurn is one message's place in the chat's line: it waits for prev, and done is closed when it is through.
type feishuTurn struct {
	prev, done chan struct{}
	wait       time.Duration
	since      time.Time        // when it came
	now        func() time.Time // the chat's clock (Host.ChatNow)
	once       sync.Once
}

// waitTurn waits for the message before it until t.wait after it came (the hub's toAssistantSince): one that waited
// behind others already does not wait the whole time again, and one past it goes on at once.
func (t *feishuTurn) waitTurn() {
	if t.prev == nil {
		return
	}
	left := t.wait - t.now().Sub(t.since)
	if left <= 0 {
		return
	}
	timer := time.NewTimer(left)
	defer timer.Stop()
	select {
	case <-t.prev:
	case <-timer.C:
	}
}

// pass lets the next message go once this one's turn has come: right away when it waited already, else after the
// message before it (a message not handled still keeps its place, so the ones after it do not overtake that one).
func (t *feishuTurn) pass() {
	t.once.Do(func() {
		go func() {
			t.waitTurn()
			close(t.done)
		}()
	})
}

// botCard is a card asking an interaction, open in the chat (assistantwire.ChatCard).
type botCard = assistantwire.ChatCard

func (b *feishuBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// loadBotLocked reads the chat's state once; b.mu is held.
func (s core) loadBotLocked(ctx context.Context) {
	b := s.fbot()
	if b.loaded {
		return
	}
	if _, err := s.h.Setting(ctx, botStateKey, &b.st); err != nil {
		s.logf("bots: load state: %v", err)
		return // the next look tries again
	}
	b.loaded = true
	if b.st.Threads == nil {
		b.st.Threads = map[string]*botThread{}
	}
	if b.st.TopicSessions == nil {
		b.st.TopicSessions = map[string]*botThread{}
	}
	pruneBotThreads(b.st.Threads, b.clock())       // a long quiet chat: the stale ones go before the first route
	pruneBotThreads(b.st.TopicSessions, b.clock()) // keep topic-session bindings bounded the same way
	if b.st.Current.Kind == "" {
		b.st.Current = botTarget{Kind: targetMain}
	}
	s.trimBotCardsLocked()
	for id, ref := range b.st.Cards { // cards posted before a restart
		if _, ok := b.cards[id]; !ok && ref != nil && ref.MessageID != "" {
			if b.cards == nil {
				b.cards = map[string]botCard{}
			}
			b.cards[id] = botCard{MessageID: ref.MessageID, Thread: ref.Thread, ChatID: ref.ChatID, At: ref.At, NudgedAt: ref.NudgedAt, SkippedAt: ref.SkippedAt, Tries: ref.Tries, TriedAt: ref.TriedAt,
				DeadlineNudge: ref.DeadlineNudge,
				Ix: proto.Interaction{ID: id, Kind: ref.Kind, MachineID: ref.MachineID, SessionID: ref.SessionID, CreatedAt: ref.CreatedAt, ExpiresAt: ref.ExpiresAt,
					ApprovalBatchID: ref.ApprovalBatchID, ApprovalItemID: ref.ApprovalItemID, OwnerProposal: ref.OwnerProposal, Resumable: ref.Resumable, NoExpiry: ref.NoExpiry}}
		}
	}
}

// trimBotCardsLocked keeps the cards' records small: none older than botCardsDays (long over, or Feishu no longer
// lets them be changed), at most botCardsKeep, the newest; b.mu is held.
func (s core) trimBotCardsLocked() {
	b := s.fbot()
	for id, ref := range b.st.Cards {
		if ref == nil || !(ref.OwnerProposal && ref.NoExpiry) && b.clock().Sub(ref.At) > botCardsDays*24*time.Hour {
			delete(b.st.Cards, id)
		}
	}
	for len(b.st.Cards) > botCardsKeep {
		oldest := ""
		for id, ref := range b.st.Cards {
			if ref != nil && ref.OwnerProposal && ref.NoExpiry {
				continue // an unanswered reconstructable proposal has no age or count eviction
			}
			if oldest == "" || ref.At.Before(b.st.Cards[oldest].At) {
				oldest = id
			}
		}
		if oldest == "" {
			break
		}
		delete(b.st.Cards, oldest)
	}
}

// keepBotCardLocked records (or, card empty, forgets) the card asking interaction id, in memory and in the chat's
// state; b.mu is held.
func (s core) keepBotCardLocked(id string, card *botCard) {
	b := s.fbot()
	s.loadBotLocked(context.Background())
	if card == nil {
		delete(b.cards, id)
		if _, ok := b.st.Cards[id]; ok {
			delete(b.st.Cards, id)
			s.saveBotStateLocked()
		}
		return
	}
	if b.cards == nil {
		b.cards = map[string]botCard{}
	}
	b.cards[id] = *card
	if card.MessageID == "" {
		return // claimed, not posted yet
	}
	if b.st.Cards == nil {
		b.st.Cards = map[string]*botCardRef{}
	}
	at := card.At
	if at.IsZero() {
		at = b.clock()
	}
	b.st.Cards[id] = &botCardRef{MessageID: card.MessageID, Kind: card.Ix.Kind, MachineID: card.Ix.MachineID, SessionID: card.Ix.SessionID, CreatedAt: card.Ix.CreatedAt.UTC(),
		Thread: card.Thread, ChatID: card.ChatID, At: at.UTC(), ExpiresAt: card.Ix.ExpiresAt.UTC(), NudgedAt: card.NudgedAt.UTC(),
		ApprovalBatchID: card.Ix.ApprovalBatchID, ApprovalItemID: card.Ix.ApprovalItemID, OwnerProposal: card.Ix.OwnerProposal,
		Resumable: card.Ix.Resumable, NoExpiry: card.Ix.NoExpiry,
		SkippedAt: card.SkippedAt.UTC(), Tries: card.Tries, TriedAt: card.TriedAt.UTC(), DeadlineNudge: card.DeadlineNudge}
	s.saveBotStateLocked()
}

// saveBotStateLocked writes the chat's state, once it was read: what is in memory after a failed read would be
// written over the stored target, threads and pairing.
func (s core) saveBotStateLocked() {
	b := s.fbot()
	if !b.loaded {
		return
	}
	if len(b.st.Seen) > botSeenKeep {
		b.st.Seen = b.st.Seen[len(b.st.Seen)-botSeenKeep:]
	}
	pruneBotThreads(b.st.Threads, b.clock())
	pruneBotThreads(b.st.TopicSessions, b.clock())
	pruneTopicAnchors(b.st.Topics, b.clock())
	s.trimBotCardsLocked()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.h.PutSetting(ctx, botStateKey, b.st); err != nil {
		s.logf("bots: save state: %v", err)
	}
}

// pruneBotThreads drops the threads older than botThreadsKeepFor, then the oldest beyond botThreadsKeep. A thread
// without a time (a state written by hand or by a test) is not judged by age; it counts as the oldest for the cap.
func pruneBotThreads(threads map[string]*botThread, now time.Time) {
	for id, t := range threads {
		if t == nil || !t.At.IsZero() && now.Sub(t.At) > botThreadsKeepFor {
			delete(threads, id)
		}
	}
	if len(threads) <= botThreadsKeep {
		return
	}
	ids := make([]string, 0, len(threads))
	for id := range threads {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int { return threads[a].At.Compare(threads[b].At) })
	for _, id := range ids[:len(ids)-botThreadsKeep] {
		delete(threads, id)
	}
}

// feishuStatus is the bot as the hub has it now (feishu.status): on and paired, its switches.
func (s core) feishuStatus(ctx context.Context) (FeishuStatus, error) {
	var st FeishuStatus
	if err := s.appCall(ctx, assistantwire.FeishuStatusGet, map[string]any{}, &st); err != nil {
		return FeishuStatus{}, err
	}
	b := s.fbot()
	b.mu.Lock()
	b.status, b.statusAt = &st, time.Now()
	b.mu.Unlock()
	s.feishuReconcile(st)
	return st, nil
}

// feishuReconcile starts the chat afresh when the bot was paired with someone else, paired again or unpaired since
// the state was written (the hub's feishu.owner may have come while the assistant was away): back to the main
// conversation, no threads, no cards, nothing waiting. A state from before pairings were recorded takes the one now.
func (s core) feishuReconcile(st FeishuStatus) {
	key := pairingKey(st)
	b := s.fbot()
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	if !b.loaded || b.st.Pairing != nil && *b.st.Pairing == key {
		b.mu.Unlock()
		return
	}
	fresh := b.st.Pairing != nil // nil: recorded for the first time, the state stays
	if fresh {
		b.st = botState{Current: botTarget{Kind: targetMain}, Threads: map[string]*botThread{}, TopicSessions: map[string]*botThread{}, Seen: b.st.Seen}
		b.cards, b.ended = nil, nil
	}
	b.st.Pairing = &key
	s.saveBotStateLocked()
	b.mu.Unlock()
	if fresh {
		s.logf("bots: the bot's pairing changed: the chat starts afresh")
		s.h.ForgetTurns()
	}
}

func pairingKey(st FeishuStatus) string {
	if st.Owner == "" {
		return ""
	}
	return st.Owner + "@" + strconv.FormatInt(st.PairedAt.Unix(), 10)
}

// feishuStatusKept is feishuStatus kept a moment: for what looks every second (a run's sink, the cards' links).
func (s core) feishuStatusKept(ctx context.Context) (FeishuStatus, error) {
	b := s.fbot()
	b.mu.Lock()
	if b.status != nil && time.Since(b.statusAt) < 3*time.Second {
		st := *b.status
		b.mu.Unlock()
		return st, nil
	}
	b.mu.Unlock()
	return s.feishuStatus(ctx)
}

func (s core) feishuForgetStatus() {
	s.fbot().mu.Lock()
	s.fbot().status = nil
	s.fbot().mu.Unlock()
}

func (s core) feishuFeatures(ctx context.Context) (FeishuFeatures, bool) {
	st, err := s.feishuStatus(ctx)
	ready := err == nil && st.Ready
	s.workbenchOn(st.Features, ready) // what the posts' workbench button follows (workbench_pin.go)
	return st.Features, ready
}

// ---- the chat's state ----

// claimBotMessage: the first time this message is taken (Feishu delivers again what it thinks was missed); an error
// while the chat's state can't be read.
func (s core) claimBotMessage(id string) (bool, error) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	if !b.loaded {
		return false, errBotUnread
	}
	if id == "" || slices.Contains(b.st.Seen, id) {
		return false, nil
	}
	b.st.Seen = append(b.st.Seen, id)
	s.saveBotStateLocked()
	return true, nil
}

var errBotUnread = errors.New("the chat's state could not be read")

func (s core) setBotCurrent(t botTarget) {
	b := s.fbot()
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	b.st.Current = t
	s.saveBotStateLocked()
	b.mu.Unlock()
}

// botTargetFor is where a message goes: the target of the bot message it replies to (or of its thread), else the
// chat's current one.
func (s core) botTargetFor(root, parent string) botTarget {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	for _, id := range []string{parent, root} {
		if t := b.st.Threads[id]; id != "" && t != nil {
			return t.Target
		}
	}
	return b.st.Current
}

// rememberBotThread binds a message the bot posted to a target: replying to it talks to that target.
func (s core) rememberBotThread(messageID string, t botTarget, title string) {
	if messageID == "" || t.Kind == "" {
		return
	}
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	if !b.loaded {
		return // its threads could not be read: nothing to add to
	}
	b.st.Threads[messageID] = &botThread{Target: t, Title: title, At: b.clock().UTC()}
	s.saveBotStateLocked()
}

// feishuReset starts the chat afresh (unpaired from the channels' page): back to the main conversation, no threads,
// no cards, nothing waiting; the pairing it belongs to is read again.
func (s core) feishuReset() {
	b := s.fbot()
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	pairing := b.st.Pairing
	b.st = botState{Current: botTarget{Kind: targetMain}, Threads: map[string]*botThread{}, TopicSessions: map[string]*botThread{}, Seen: b.st.Seen, Pairing: pairing}
	b.cards, b.ended = nil, nil
	b.status = nil
	s.saveBotStateLocked()
	b.mu.Unlock()
}

// feishuUnpaired: the owner went: the chat's state and what it waited on go.
func (s core) feishuUnpaired() {
	s.feishuReset()
	s.h.ForgetTurns()
}

// FeishuOwner is feishu.owner: the bot was paired (it says hello, with what it can do) or unpaired. The pairing now
// is read from the hub (feishuReconcile starts the chat afresh), the same as on any later look when this event was
// missed.
func (s core) FeishuOwner(_ context.Context, p FeishuOwner) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		st, err := s.feishuStatus(ctx)
		if err != nil {
			s.feishuUnpaired() // the hub could not be asked: start afresh anyway
		}
		if !p.Paired || p.Greeted {
			return
		}
		l := s.botLang(ctx)
		card := botcard.NewCard(i18n.T(l, "assistant-feishu.paired"), botcard.Green, i18n.T(l, "assistant-feishu.paired")).Markdown(i18n.T(l, "assistant-feishu.paired_body") + botHelp(l, st.Features))
		if _, err := s.botPost(ctx, "", card, ""); err != nil {
			s.logf("bots: paired note: %v", err)
		}
	}()
}

// takeLegacyBotRuns are the runs an older hub kept in the chat's state, taken out of it (once).
func (s core) takeLegacyBotRuns(ctx context.Context) []*channel.Run {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	old := b.st.Runs
	if len(old) == 0 {
		return nil
	}
	b.st.Runs = nil
	s.saveBotStateLocked()
	var out []*channel.Run
	for _, r := range old {
		if r != nil && r.Target.SessionID != "" {
			out = append(out, r.channelRun())
		}
	}
	return out
}

// botRun is a run as bots.feishu.state kept it before the channels' pipeline: read once and moved over.
type botRun struct {
	ID        string      `json:"id"`
	MessageID string      `json:"message_id"`
	Target    botTarget   `json:"target"`
	Label     string      `json:"label"`
	Base      int         `json:"base"`
	StartedAt time.Time   `json:"started_at"`
	Card      string      `json:"card,omitempty"`
	Reaction  string      `json:"reaction,omitempty"`
	Seen      bool        `json:"seen,omitempty"`
	Held      bool        `json:"held,omitempty"`
	Probe     string      `json:"probe,omitempty"`
	Folded    []botFolded `json:"folded,omitempty"`
}

type botFolded struct {
	MessageID string `json:"message_id"`
	Reaction  string `json:"reaction,omitempty"`
}

// channelRun is a legacy run as the pipeline's.
func (r *botRun) channelRun() *channel.Run {
	out := &channel.Run{ID: r.ID, Channel: channel.Feishu, ReplyTo: r.MessageID, MachineID: r.Target.MachineID, SessionID: r.Target.SessionID,
		TargetKind: r.Target.Kind, Label: r.Label, Base: r.Base, Probe: r.Probe, Started: r.StartedAt, Seen: r.Seen, Held: r.Held,
		Ext: map[string]string{}}
	if r.MessageID != "" && !r.Held {
		out.MsgIDs = []string{channel.ClientMsgID(channel.Feishu, r.MessageID)}
	}
	if r.Card != "" {
		out.Ext["card"] = r.Card
	}
	if r.Reaction != "" {
		out.Ext["reaction"] = r.Reaction
	}
	for _, f := range r.Folded {
		fo := channel.Folded{ReplyTo: f.MessageID}
		if f.Reaction != "" {
			fo.Ext = map[string]string{"reaction": f.Reaction}
		}
		out.Folded = append(out.Folded, fo)
	}
	return out
}

// ---- what the hub shows of the chat ----

// FeishuView is feishu.view: where the chat's messages go now, and the agents bound to its threads.
func (s core) FeishuView(ctx context.Context) FeishuView {
	b := s.fbot()
	b.mu.Lock()
	s.loadBotLocked(ctx)
	cur := b.st.Current
	// One row per agent, not per thread: every card the bot posts about a conversation is bound to it, so an agent
	// talked to for a while has dozens of them, and the list would fill with copies of it (the clients key those rows
	// by session id). The newest thread names the agent, and the agents come newest first.
	type boundAgent struct {
		at time.Time
		a  FeishuAgent
	}
	seen := map[[2]string]int{} // machine and session → where its row is
	var rows []boundAgent
	for _, t := range b.st.Threads {
		if !t.Target.Agent() {
			continue
		}
		key := [2]string{t.Target.MachineID, t.Target.SessionID}
		if i, ok := seen[key]; ok {
			if t.At.After(rows[i].at) {
				rows[i].at, rows[i].a.Title = t.At, t.Title
			}
			continue
		}
		seen[key] = len(rows)
		rows = append(rows, boundAgent{at: t.At, a: FeishuAgent{Title: t.Title, MachineID: t.Target.MachineID, SessionID: t.Target.SessionID}})
	}
	// Newest first, and on the same time (state written by hand or by an older version can carry no time at all) by
	// session then machine, so two loads never order the rows differently.
	slices.SortStableFunc(rows, func(x, y boundAgent) int {
		if c := y.at.Compare(x.at); c != 0 {
			return c
		}
		if c := strings.Compare(x.a.SessionID, y.a.SessionID); c != 0 {
			return c
		}
		return strings.Compare(x.a.MachineID, y.a.MachineID)
	})
	var v FeishuView
	for _, r := range rows {
		v.Agents = append(v.Agents, r.a)
	}
	b.mu.Unlock()
	v.Target = s.h.TargetLabel(ctx, cur)
	return v
}

func (s core) feishuCardsWaiting() int {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.cards)
}

// topicAnchorsKeep and topicAnchorsKeepFor bound the topic messages remembered to reply to.
const (
	topicAnchorsKeep    = 200
	topicAnchorsKeepFor = 30 * 24 * time.Hour
)

// topicAnchor is a message of the topic the bot can reply into it with: the latest one the owner said there.
// Feishu replies into a thread by a message in it; the todo only keeps the thread's id.
type topicAnchor struct {
	MessageID string    `json:"message_id"`
	At        time.Time `json:"at"`
}

// rememberTopicMessage notes message id as the latest of the topic, kept with the chat's state (a restart does not
// lose where to reply).
func (s core) rememberTopicMessage(ctx context.Context, chatID, threadID, id string) {
	if chatID == "" || threadID == "" || id == "" {
		return
	}
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if !b.loaded {
		return
	}
	if b.st.Topics == nil {
		b.st.Topics = map[string]*topicAnchor{}
	}
	b.st.Topics[chatID+"/"+threadID] = &topicAnchor{MessageID: id, At: b.clock()}
	s.saveBotStateLocked()
}

// topicMessage is the message to reply to in the topic, "" when none is known.
func (s core) topicMessage(ctx context.Context, chatID, threadID string) string {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if a := b.st.Topics[chatID+"/"+threadID]; a != nil {
		return a.MessageID
	}
	return ""
}

// pruneTopicAnchors drops the anchors older than topicAnchorsKeepFor, then the oldest beyond topicAnchorsKeep.
func pruneTopicAnchors(m map[string]*topicAnchor, now time.Time) {
	for k, a := range m {
		if a == nil || now.Sub(a.At) > topicAnchorsKeepFor {
			delete(m, k)
		}
	}
	for len(m) > topicAnchorsKeep {
		oldest := ""
		for k, a := range m {
			if oldest == "" || a.At.Before(m[oldest].At) {
				oldest = k
			}
		}
		delete(m, oldest)
	}
}

// forgetTopicMessage drops the anchor of the topic when it is still the one given (a newer message may have come).
func (s core) forgetTopicMessage(ctx context.Context, chatID, threadID, id string) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if a := b.st.Topics[chatID+"/"+threadID]; a != nil && a.MessageID == id {
		delete(b.st.Topics, chatID+"/"+threadID)
		s.saveBotStateLocked()
	}
}

// rememberTopicSession binds a group chat topic to a conversation target so follow-up replies in that topic route back to it.
func (s core) rememberTopicSession(ctx context.Context, chatID, threadID string, t botTarget, title string) {
	if chatID == "" || threadID == "" || t.Kind == "" {
		return
	}
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if !b.loaded {
		return
	}
	if b.st.TopicSessions == nil {
		b.st.TopicSessions = map[string]*botThread{}
	}
	b.st.TopicSessions[chatID+"/"+threadID] = &botThread{Target: t, Title: title, At: b.clock().UTC()}
	s.saveBotStateLocked()
}

// topicSession returns the conversation target bound to a group chat topic, if any.
func (s core) topicSession(ctx context.Context, chatID, threadID string) (botTarget, bool) {
	if chatID == "" || threadID == "" {
		return botTarget{}, false
	}
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if t := b.st.TopicSessions[chatID+"/"+threadID]; t != nil && t.Target.Kind != "" {
		return t.Target, true
	}
	return botTarget{}, false
}

// chatBook is the chat's records as the core reads and writes them (assistantwire.ChatBook), under b.mu.
type chatBook struct{ s core }

func (c chatBook) Loaded() bool   { return c.s.fbot().loaded }
func (c chatBook) Now() time.Time { return c.s.fbot().clock() }

func (c chatBook) Card(id string) (botCard, bool) {
	card, ok := c.s.fbot().cards[id]
	return card, ok
}

func (c chatBook) KeepCard(id string, card *botCard) { c.s.keepBotCardLocked(id, card) }

func (c chatBook) Cards() map[string]botCard { return maps.Clone(c.s.fbot().cards) }

func (c chatBook) CardRef(id string) (botCardRef, bool) {
	ref := c.s.fbot().st.Cards[id]
	if ref == nil {
		return botCardRef{}, false
	}
	return *ref, true
}

func (c chatBook) ProposalFor(parent string) string {
	id := ""
	for candidate, ref := range c.s.fbot().st.Cards {
		if ref == nil || !ref.OwnerProposal || ref.MessageID != parent {
			continue
		}
		if id != "" {
			return "" // ambiguous persisted mapping: never guess which operation was approved
		}
		id = candidate
	}
	return id
}

func (c chatBook) NoteEnded(card botCard) {
	if typedAnswerable(card) {
		c.s.fbot().noteEndedLocked(card)
	}
}

func (c chatBook) Bound(machine, session string, d time.Duration) bool {
	b := c.s.fbot()
	for _, t := range b.st.Threads {
		if t.Target.MachineID == machine && t.Target.SessionID == session && b.clock().Sub(t.At) < d {
			return true
		}
	}
	return false
}

func (c chatBook) Board() *assistantwire.ChatBoardRef {
	if ref := c.s.fbot().st.Board; ref != nil {
		cp := *ref
		return &cp
	}
	return nil
}

func (c chatBook) SetBoard(ref *assistantwire.ChatBoardRef) {
	b := c.s.fbot()
	if !b.loaded {
		return
	}
	b.st.Board = ref
	c.s.saveBotStateLocked()
}

func (c chatBook) Pairing() *string {
	if p := c.s.fbot().st.Pairing; p != nil {
		cp := *p
		return &cp
	}
	return nil
}

func (c chatBook) Companions() []assistantwire.CompanionBot {
	return append([]assistantwire.CompanionBot(nil), c.s.fbot().st.Companions...)
}

func (c chatBook) SetCompanions(bots []assistantwire.CompanionBot) {
	b := c.s.fbot()
	if !b.loaded {
		return
	}
	b.st.Companions = bots
	c.s.saveBotStateLocked()
}
