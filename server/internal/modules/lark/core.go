package lark

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu turns (turn.go, cot.go, cot_worker.go, stream.go): how a run of the Feishu bot shows in the chat, its
// progress card, its agent message and its answer. The channels' pipeline (the runs, their state and their lock) is
// the core's; it hands each run to the layer (Layer.Turn*), and the module keeps only its own state of each live run
// (runLive, through Host.RunState) and of every run (turns).

// turns is the module's state of the Feishu turns, one per layer.
type turns struct {
	h  Host
	mu sync.Mutex
	// kitDown: CardKit would not make a progress card (stream.go); until then they are sent as messages.
	kitDown time.Time
	// workers are the agent messages' workers, the chat's messages being handled (message.go) and the agents and
	// start forms a card's press starts (card_actions.go), still running (Layer.Wait).
	workers sync.WaitGroup
	// posts are the to-do topic lines waiting to be said (thread_posts.go).
	posts threadPoster
	// book is the chat's state (chat_state.go).
	book feishuBook
	// handoff is the proxy replies the module has taken (handoff.go), with its own lock.
	handoff *handoffBook
	// guest is the group guest conversations the module runs (guest.go), with its own lock.
	guest *guestBook
	// detached are the cards closed when their node went away (asks.go).
	detached detachedCards
	// held are the /read answers held for the owner (read_held.go).
	held readHeldBook
	// board, wb and comp are the standing board (board.go), the pinned workbench (workbench_pin.go) and the
	// companion bots' notices (companions.go), each with its own lock.
	board standingBoard
	wb    workbenchState
	comp  companionState
}

// wait waits for the workers, or ctx.
func (t *turns) wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { t.workers.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newTurns(h Host) *turns {
	t := &turns{h: h}
	t.book.now = h.ChatNow
	return t
}

// sink is the Feishu bot as the channels' sink.
func (t *turns) sink() feishuSink { return feishuSink{s: core{h: t.h, t: t}} }

// core is the assistant as the turn code calls it: the host's methods under the names the code has always used.
type core struct {
	h Host
	t *turns
}

func (s core) logf(format string, args ...any)       { s.h.Logf(format, args...) }
func (s core) runCtx() context.Context               { return s.h.RunCtx() }
func (s core) botLang(ctx context.Context) i18n.Lang { return s.h.Lang(ctx) }
func (s core) chansNow() time.Time                   { return s.h.Now() }

func (s core) sendAs(ctx context.Context, kind, machine, session string) context.Context {
	return s.h.SendAs(ctx, kind, machine, session)
}

func (s core) appCall(ctx context.Context, method string, params, out any) error {
	return s.h.Call(ctx, method, params, out)
}

func (s core) botPostTo(ctx context.Context, to feishuTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return s.h.Post(ctx, to, replyTo, card, fallback, key)
}

func (s core) botPostContent(ctx context.Context, to feishuTarget, replyTo, content, fallback, key string) (string, error) {
	return s.h.PostContent(ctx, to, replyTo, content, fallback, key)
}

func (s core) botNoteTo(ctx context.Context, to feishuTarget, messageID, text string) {
	s.h.Note(ctx, to, messageID, text)
}

func (s core) botUpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return s.h.UpdateCard(ctx, messageID, card)
}

// botReplyTo writes text under messageID at to: key makes the same line, sent again, land once; full says the text is
// a task's whole answer (the hub's full_answer).
func (s core) botReplyTo(ctx context.Context, to feishuTarget, messageID, text, key string, full bool) error {
	return s.appCall(ctx, FeishuText, targetParams(to, map[string]any{"reply_to": messageID, "text": text, "uuid": key, "full_answer": full}), nil)
}

func (s core) botReact(ctx context.Context, messageID, emoji string) (string, error) {
	var res struct {
		ReactionID string `json:"reaction_id"`
	}
	err := s.appCall(ctx, FeishuReact, map[string]any{"message_id": messageID, "emoji": emoji}, &res)
	return res.ReactionID, err
}

func (s core) botUnreact(ctx context.Context, messageID, reactionID string) {
	_ = s.appCall(ctx, FeishuUnreact, map[string]any{"message_id": messageID, "reaction_id": reactionID}, nil)
}

func (s core) botMessages(ctx context.Context, messageID string) ([]botcard.Item, error) {
	var res struct {
		Items []botcard.Item `json:"items"`
	}
	err := s.appCall(ctx, FeishuMessages, map[string]any{"message_id": messageID}, &res)
	return res.Items, err
}

func (s core) runExt(r *channel.Run, key string) string { return s.h.RunExt(r, key) }

func (s core) runExts(r *channel.Run, keys ...string) map[string]string {
	return s.h.RunExts(r, keys...)
}

func (s core) setRunExt(r *channel.Run, key, value string) { s.h.SetRunExt(r, key, value) }

func (s core) runSteps(id string) []string { return s.h.RunSteps(id) }

func (s core) unreachable(err error) bool { return s.h.Unreachable(err) }

func (s core) botStepParts(l i18n.Lang, it proto.Item) (string, string) { return s.h.StepParts(l, it) }

func (s core) linkNamer(ctx context.Context) func(*url.URL) string { return s.h.LinkNamer(ctx) }

func (s core) openButton(ctx context.Context, machine, session string) botcard.Button {
	return s.h.OpenButton(ctx, machine, session)
}

// rememberRun binds a message the bot posted in the private chat to run r's conversation: replying to it talks there.
func (s core) rememberRun(messageID string, r *channel.Run) {
	s.rememberBotThread(messageID, botTargetOf(r), r.Label)
}

// botAnswerCard is one card of an answer and the text it goes as when refused.
type botAnswerCard struct {
	card     *botcard.Card
	fallback string
}

// feishuTarget is where a post goes (the outbound port's target).
type feishuTarget = assistantwire.ChannelTarget

// readDirectKey is set ("1") on a /read's run asked with the read_group_direct switch on: its answer goes straight
// into the group. Without it the answer is held for the owner (read_held.go, #14437).
const readDirectKey = "read_direct"

// cloneExt is a copy of a run's channel values.
func cloneExt(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// runLive is the module's state of one live run (Host.RunState), beside the core's: the progress card as last sent
// and its entity, the agent message's cursor and its worker. sentCard, backoff, quietUntil, kit and, for a private
// run, cot are used under the run's progress lock (the core holds it around TurnProgress and TurnAnswer); mu guards
// native and cot where a topic run's worker shares them.
type runLive struct {
	mu     sync.Mutex
	cot    cotProgress
	native *cotWorker
	// sentCard is the progress card last sent (its hash), not sent again; backoff and quietUntil hold updates back
	// after Feishu's rate limit.
	sentCard   [32]byte
	backoff    time.Duration
	quietUntil time.Time
	// kit is the progress card as a CardKit entity, streamed into (stream.go); nil before it is sent, or for a card
	// sent as a message.
	kit *cardKit
	// readNoted: a held /read's owner was told it is being worked on (readWorking), once per run.
	readNoted bool
}

// Abort stops the run's agent-message worker (the core dropped the run). Never called under the channels' lock.
func (p *runLive) Abort() {
	p.mu.Lock()
	w := p.native
	p.mu.Unlock()
	if w != nil {
		w.abort()
	}
}

// NextTurn forgets the agent message of a held run's turn: the next turn gets its own (called under the channels'
// lock).
func (p *runLive) NextTurn() {
	p.mu.Lock()
	p.cot, p.native = cotProgress{}, nil
	p.mu.Unlock()
}

// live is run id's state, nil when the run is not live or no longer among the channels' runs.
func (s core) live(id string) *runLive {
	st, _, ok := s.h.RunState(id, true, func() any { return &runLive{} })
	if !ok {
		return nil
	}
	p, _ := st.(*runLive) // nil for a state the module did not make
	return p
}
