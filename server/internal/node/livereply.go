package node

import (
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Live replies of the Claude sessions Persona starts (#1309, docs/integrations/claude-integration.md "MessageDisplay").
//
// Their live replies plugin has Claude Code's MessageDisplay hook (hooksplugin.go, generated.go displayHooks; loaded while
// the machine's setting is on, setLiveReplies): as Claude shows a reply in the
// terminal it hands the hook each batch of newly completed lines, often seconds before the transcript has them (a
// reply that ends in a tool call is written only once the tool is done). The hook passes the batch to the node
// (`personad hook`, kind hook.display), which turns it into the session's session.delta: the live text the headless
// assistant streams too, the message so far, "" once there is none.
//
// What a batch is (measured on 2.1.319): message_id is a random id of the message on screen, the same for each of its
// batches and never the transcript's (msg_… or a line's uuid); index counts the message's batches from 0, one of them
// with final; delta holds only what is new since the last batch, whole lines but for the final one. Up to three hooks
// run at once, so batches arrive out of order; each API reply is a message of its own (the words before a tool call
// and those after it are two). The transcript can have a message before its first batch comes, and a Stop can come
// before its last. So the live text is never the source:
//
//   - The transcript's words replace it (unlanded): the words of the newest API message in the exchange under way that
//     begin like it, taken off its start. Two messages of a turn can begin alike; the one before lands first, so words
//     that begin like both are the one before's until a later message has them too (prev).
//   - It belongs to its turn: once the transcript shows that turn over after its first batch came (a new prompt or a
//     background task waking the agent, turn_duration, an interrupt, a model error that ended it: turnOver), it goes,
//     as it does a moment after a Stop, at the next prompt, when its claude is gone, or after liveQuietMost.
//   - A message whose last batch came and that the transcript has whole is done, and a batch of it that comes late is
//     dropped, like one of a message a newer one replaced or of a turn a Stop ended.
//
// Only a session the hub watches (someone has it open) is told: its transcript is read every 250 ms and pushed, and
// the live text is checked against it in the same step (pushItems), so the replacement reaches the apps right after the
// items it is replaced by. While nobody watches, the batches are still taken (a page opened mid-message gets the
// message so far), and the first read of a new watch checks them against the transcript before anything is told.

// displayInput is a MessageDisplay hook's input. prompt_id is the user line's promptId in the transcript (and what
// UserPromptSubmit and Stop carry); turn_id is new for every turn and found nowhere else.
type displayInput struct {
	HookEventName string `json:"hook_event_name"`
	SessionID     string `json:"session_id"`
	PromptID      string `json:"prompt_id"`
	TurnID        string `json:"turn_id"`
	MessageID     string `json:"message_id"`
	Index         int    `json:"index"`
	Final         bool   `json:"final"`
	Delta         string `json:"delta"`
	AgentID       string `json:"agent_id"`
}

// Bounds of the live text.
var (
	// liveCheckEvery: how often a session with live text on the apps looks whether it should go (liveCheck).
	liveCheckEvery = 2 * time.Second
	// liveStopGrace: how long after a Stop the message it ended may stay live, for the transcript to be read.
	liveStopGrace = 2 * time.Second
	// liveQuietMost: a message with no batch for this long is not coming further (a claude killed mid-message that
	// the node cannot tell from one running a long tool).
	liveQuietMost = 30 * time.Minute
)

const (
	liveTextMost  = 1 << 20 // bytes of one message kept; batches past it are dropped
	liveAheadMost = 256     // batches held waiting for a missing one
	liveRemember  = 8       // replaced messages and ended turns remembered per session
	liveSessions  = 256     // sessions tracked; the quietest go first
)

// liveReplies is each session's message on screen as its batches came, and what the hub was last told of it.
type liveReplies struct {
	mu sync.Mutex
	m  map[string]*liveReply
	// off: the machine's setting is off (node.configure claude_live_replies false), so a start does not load the live
	// replies plugin; the zero value is on, as for an older hub that never says. Batches of a session started before
	// are still taken: its terminal pays for them anyway.
	off atomic.Bool
}

type liveReply struct {
	// The message: its turn (prompt, turn) and id, its text from the batches in order (next is the index of the next
	// one, last the index of the final one, -1 until it came), the batches that came ahead of a missing one, when its
	// first batch came (since: what the transcript writes after it is of its turn or later) and when the last came.
	prompt, turn, msg string
	text              string
	next, last        int
	ahead             map[int]string
	since, at         time.Time
	// endedAt: a Stop ended the message's turn then (zero: it did not).
	endedAt time.Time
	// prev: the text of the message before it in the same turn (prevTurn), "" for none.
	prev, prevTurn string
	// gone: messages a newer one replaced or that ended; ended: turns a Stop ended. A batch of either is late and dropped.
	gone, ended []string
	shown       string      // what the hub has (session.delta), "" for nothing
	check       *time.Timer // liveCheck while shown
}

// final: all of the message's batches were taken.
func (r *liveReply) final() bool { return r.last >= 0 && r.next > r.last }

// endLocked ends the message on screen: a late batch of it is dropped. keep: the turn goes on, and its text is the
// message before the next one's (prev); otherwise the turn is over and nothing of it is kept.
func (r *liveReply) endLocked(keep bool) {
	if r.msg != "" {
		r.gone = remember(r.gone, r.msg)
		if keep && r.text != "" {
			r.prev, r.prevTurn = r.text, r.turn
		}
	}
	if !keep {
		r.prev, r.prevTurn = "", ""
	}
	r.prompt, r.turn, r.msg, r.text, r.next, r.last, r.ahead = "", "", "", "", 0, -1, nil
	r.since, r.endedAt = time.Time{}, time.Time{}
}

func (l *liveReplies) entry(id string, now time.Time) *liveReply {
	if l.m == nil {
		l.m = map[string]*liveReply{}
	}
	r := l.m[id]
	if r == nil {
		if len(l.m) >= liveSessions {
			var quiet string
			for k, v := range l.m {
				if v.shown == "" && (quiet == "" || v.at.Before(l.m[quiet].at)) {
					quiet = k
				}
			}
			if quiet != "" {
				delete(l.m, quiet)
			}
		}
		r = &liveReply{at: now, last: -1}
		l.m[id] = r
	}
	return r
}

// remember appends v to a bounded list of ids.
func remember(list []string, v string) []string {
	if v == "" || slices.Contains(list, v) {
		return list
	}
	list = append(list, v)
	if len(list) > liveRemember {
		list = list[len(list)-liveRemember:]
	}
	return list
}

// take adds a batch; it says whether the text changed (it grew, or a new message replaced one with words). A batch of
// a message that ended or of a turn a Stop ended, and one already taken, change nothing.
func (l *liveReplies) take(in displayInput, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.entry(in.SessionID, now)
	if slices.Contains(r.gone, in.MessageID) || in.TurnID != "" && slices.Contains(r.ended, in.TurnID) {
		return false
	}
	replaced := false
	if r.msg != in.MessageID {
		replaced = r.text != ""
		r.endLocked(true)
		r.prompt, r.turn, r.msg, r.since = in.PromptID, in.TurnID, in.MessageID, now
		if r.prevTurn != in.TurnID {
			r.prev, r.prevTurn = "", ""
		}
	}
	if _, seen := r.ahead[in.Index]; seen || in.Index < r.next || len(r.ahead) >= liveAheadMost {
		return replaced
	}
	if r.ahead == nil {
		r.ahead = map[int]string{}
	}
	r.ahead[in.Index] = in.Delta
	if in.Final {
		r.last = in.Index
	}
	r.at = now
	grew := false
	for {
		d, ok := r.ahead[r.next]
		if !ok {
			break
		}
		delete(r.ahead, r.next)
		r.next++
		if d != "" && len(r.text)+len(d) <= liveTextMost {
			r.text += d
			grew = true
		}
	}
	return grew || replaced
}

// liveView is what showLive weighs of a session's message.
type liveView struct {
	msg, text, prev string
	since           time.Time
	final           bool
}

// view is the session's message, the zero value when there is none.
func (l *liveReplies) view(id string) liveView {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && r.msg != "" {
		return liveView{msg: r.msg, text: r.text, prev: r.prev, since: r.since, final: r.final()}
	}
	return liveView{}
}

// has says whether the session has a message, live text on the apps, or a turn under way that had one.
func (l *liveReplies) has(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	return r != nil && (r.msg != "" || r.shown != "" || r.prev != "")
}

// show records want as what the hub has, and says whether that is news.
func (l *liveReplies) show(id, want string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r == nil {
		if want == "" {
			return false
		}
		r = l.entry(id, time.Now())
	}
	if r.shown == want {
		return false
	}
	r.shown = want
	return true
}

// drop ends the session's message msg ("" for whichever) as endLocked does.
func (l *liveReplies) drop(id, msg string, keep bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && (msg == "" || r.msg == msg) {
		r.endLocked(keep)
	}
}

// prompt is a UserPromptSubmit: the turn before is over, and so is a message of it (not one of this prompt's own,
// should its hook come late).
func (l *liveReplies) prompt(id, prompt string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && (r.msg == "" || prompt == "" || r.prompt != prompt) {
		r.endLocked(false)
	}
}

// stop is a Stop of the turn of prompt ("" for whichever): the message on screen gets no more batches, and goes once
// liveStopGrace has passed (liveCheck), by when the transcript has it.
func (l *liveReplies) stop(id, prompt string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r != nil && r.msg == "" {
		r.prev, r.prevTurn = "", "" // the turn is over: its messages are no next one's
	}
	if r == nil || r.msg == "" || prompt != "" && r.prompt != "" && r.prompt != prompt {
		return
	}
	r.ended = remember(r.ended, r.turn)
	if r.endedAt.IsZero() {
		r.endedAt = now
	}
}

// due says whether the session's message should go now: its turn ended liveStopGrace ago, it has been quiet for
// liveQuietMost, or its claude is gone.
func (l *liveReplies) due(id string, now time.Time, agentGone bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r == nil || r.msg == "" {
		return false
	}
	return agentGone || !r.endedAt.IsZero() && now.Sub(r.endedAt) >= liveStopGrace || now.Sub(r.at) >= liveQuietMost
}

// arm starts the session's liveCheck unless it runs; disarm says it ran.
func (l *liveReplies) arm(id string, f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && r.check == nil {
		r.check = time.AfterFunc(liveCheckEvery, f)
	}
}

func (l *liveReplies) disarm(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil {
		r.check = nil
	}
}

// forget drops what the session has once there is nothing on the apps, no message and no turn under way that had one.
func (l *liveReplies) forget(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && r.msg == "" && r.shown == "" && r.prev == "" && r.check == nil {
		delete(l.m, id)
	}
}

// displayHook takes a MessageDisplay hook's input (serveSock, kind hook.display; the hook was answered already). Only
// an Agent's main conversation: a subagent's batches are not its reply, and the headless assistant streams its own.
func (n *Node) displayHook(raw json.RawMessage) {
	var in displayInput
	if json.Unmarshal(raw, &in) != nil || in.HookEventName != "MessageDisplay" || !validID(in.SessionID) || in.AgentID != "" ||
		in.MessageID == "" || in.Index < 0 || n.isAssistantSession(in.SessionID) {
		return
	}
	mu := n.pushLock(in.SessionID)
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	if in.Delta != "" {
		n.ttft.firstToken(in.SessionID, now, in.PromptID)
	}
	if n.live.take(in, now) {
		n.showLive(in.SessionID)
	}
}

// liveTurnHook is what UserPromptSubmit and Stop, from Persona's own hooks, mean for the live text.
func (n *Node) liveTurnHook(in hookInput) {
	if in.AgentID != "" {
		return
	}
	switch in.HookEventName {
	case "UserPromptSubmit":
		n.ttft.arm(in.SessionID, time.Now(), in.PromptID)
	case "Stop":
		n.ttft.clearPending(in.SessionID)
	}
	if !n.live.has(in.SessionID) {
		return
	}
	switch in.HookEventName {
	case "UserPromptSubmit":
		mu := n.pushLock(in.SessionID)
		mu.Lock()
		defer mu.Unlock()
		n.live.prompt(in.SessionID, in.PromptID)
		n.showLive(in.SessionID)
	case "Stop":
		n.live.stop(in.SessionID, in.PromptID, time.Now())
		n.live.arm(in.SessionID, func() { n.liveCheck(in.SessionID) })
	}
}

// liveRecheck is showLive for a session that has a message, after its new transcript lines were pushed: the caller
// holds the session's pushLock.
func (n *Node) liveRecheck(id string) {
	if n.live.has(id) {
		n.showLive(id)
	}
}

// liveUnwatched clears what the hub keeps of a session nobody has open any more (it would hand it to the next page
// that opens it, after the transcript went on without the node reading it). The message itself stays: a page that
// opens the session while Claude still shows it gets it back, once the watch's first read checked it (showLive).
func (n *Node) liveUnwatched(id string) {
	if !n.live.has(id) {
		return
	}
	mu := n.pushLock(id)
	mu.Lock()
	defer mu.Unlock()
	n.showLive(id)
}

// showLive tells the hub the session's live text when it changed: what of its message the transcript does not have
// yet, nothing while nobody watches it. A message that is over goes first: its turn ended (liveReplies.due, turnOver),
// or the transcript has all of it. The caller holds the session's pushLock, which orders it after the items that
// replace it.
func (n *Node) showLive(id string) {
	want := ""
	if v := n.live.view(id); v.text != "" {
		f := n.watchedFile(id)
		switch {
		case n.live.due(id, time.Now(), n.liveAgentGone(id)):
			n.live.drop(id, v.msg, false)
		case f == nil: // nobody watches: nothing is told
		default:
			ex := exchange(f)
			if turnOver(ex, v.since) {
				n.live.drop(id, v.msg, false)
				break
			}
			rest, over := unlanded(v.text, messageWords(ex), v.prev)
			if over || rest == "" && v.final {
				n.live.drop(id, v.msg, true) // the transcript has all of it, or a later message
			} else {
				want = rest
			}
		}
	}
	if !n.live.show(id, want) {
		if want == "" {
			n.live.forget(id)
		}
		return
	}
	if n.peer != nil {
		_ = n.peer.Notify("session.delta", map[string]any{"session_id": id, "text": want})
	}
	if want != "" {
		n.live.arm(id, func() { n.liveCheck(id) })
	} else {
		n.live.forget(id)
	}
}

// liveCheck runs every liveCheckEvery while a session's live text is on the apps or a Stop waits to clear it.
func (n *Node) liveCheck(id string) {
	n.live.disarm(id)
	if n.peer != nil {
		select {
		case <-n.peer.Done(): // this connection is over, and the hub forgot its live texts with it
			return
		default:
		}
	}
	mu := n.pushLock(id)
	mu.Lock()
	defer mu.Unlock()
	n.showLive(id)
	if n.live.has(id) && !n.live.idle(id) {
		n.live.arm(id, func() { n.liveCheck(id) })
	}
}

// idle: nothing on the apps and no Stop to clear after: liveCheck can stop.
func (l *liveReplies) idle(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	return r == nil || r.shown == "" && r.endedAt.IsZero()
}

// liveAgentGone: the claude the session's hooks run under is gone (hookturn.go keeps which it is).
func (n *Node) liveAgentGone(id string) bool {
	t, ok := n.hooks.get(id)
	return ok && t.pid > 0 && !processAlive(t.pid, t.start)
}

// watchedFile is the session's transcript while the hub watches it, nil otherwise.
func (n *Node) watchedFile(id string) *transcript.File {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.watches[id]; !ok {
		return nil
	}
	return n.files[id]
}

// exchange is the transcript's items from the message that opened the exchange under way on (a prompt of the user's,
// or a background task waking the agent), as the node has read them; without that message when it is further back than
// a page.
func exchange(f *transcript.File) []proto.Item {
	items, _, _ := f.PageTurns(0, 1, 400)
	for i := len(items) - 1; i >= 0; i-- {
		if opensExchange(items[i]) {
			return items[i:]
		}
	}
	return items
}

func opensExchange(it proto.Item) bool {
	return it.Wake || it.Role == "user" && it.Kind == proto.KindText && !it.Queued
}

// turnOver: the transcript shows the turn a message belongs to over, written after its first batch came (since): the
// exchange opened after it, or a turn ended in it since (turn_duration, an interrupt: Esc fires no Stop, a model error
// that ended the turn: StopFailure, not Stop). What came before since is the turn before's (a new prompt's line can be
// read after its first batch).
func turnOver(ex []proto.Item, since time.Time) bool {
	for _, it := range ex {
		if it.TS.After(since) && (opensExchange(it) || endsTurn(it)) {
			return true
		}
	}
	return false
}

func endsTurn(it proto.Item) bool {
	switch {
	case it.TurnDuration != nil:
		return true
	case it.Role == "assistant" && it.Kind == proto.KindText:
		return assistantwire.CutOff(it.Text) // "API Error: …", Claude Code's answer when the model call failed
	case it.Role == "system" && it.Kind == proto.KindNotice:
		t := strings.TrimSpace(it.Text)
		return t == transcript.InterruptedNotice || strings.HasPrefix(t, transcript.APIErrorPrefix) && !strings.HasSuffix(t, transcript.APIRetrying)
	}
	return false
}

// landed is an agent's words as the transcript has them. size: a cut item's whole length in bytes (pages and pushes
// carry long text cut, text holding only its start), 0 for a whole one.
type landed struct {
	text string
	size int
}

// messageWords are the agent's words in an exchange, one list per API message, oldest first: the text items of each run
// of the agent's lines. A message's lines are written together; what comes between two (a tool's result, a message
// typed meanwhile, a compaction) is not the agent's.
func messageWords(ex []proto.Item) [][]landed {
	var out [][]landed
	open := false
	for _, it := range ex {
		switch {
		case it.Role != "assistant":
			open = false
		case it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "":
			if !open {
				out, open = append(out, nil), true
			}
			out[len(out)-1] = append(out[len(out)-1], landedItem(it))
		}
	}
	return out
}

// landedItem is an item's words, without the line a cut one ends in.
func landedItem(it proto.Item) landed {
	if it.FullSize == 0 {
		return landed{text: it.Text}
	}
	t := it.Text
	if i := strings.LastIndex(t, "\n…"); i >= 0 {
		t = t[:i]
	}
	return landed{text: t, size: it.FullSize}
}

const liveSpace = " \t\r\n"

// unlanded is what of a live message the transcript does not have yet ("" for nothing), and over: a later message's
// words came after its own, so it is done. The live text is the message's lines as Claude showed them, the transcript's
// items its text blocks, word for word the same (measured). Its words are the newest message's (words) that begin like
// it, unless prev (the turn's message before it) begins alike and has no earlier words of its own: that one lands
// first. What is left after them is as it came (as it came while nothing landed): the apps match it to the item that
// replaces it by its start (iOS ChatStore, Android ChatRealtimeState, web lib/live-text.ts).
func unlanded(text string, words [][]landed, prev string) (rest string, over bool) {
	mine := -1
	for i := len(words) - 1; i >= 0 && mine < 0; i-- {
		if begins(words[i], text) {
			mine = i
		}
	}
	if mine >= 0 && prev != "" && begins(words[mine], prev) &&
		!slices.ContainsFunc(words[:mine], func(w []landed) bool { return begins(w, prev) }) {
		mine = -1 // the message before begins alike and lands first: these are its words
	}
	switch {
	case mine < 0:
		return liveRest(text), false
	case mine < len(words)-1:
		return "", true
	}
	return trimLanded(text, words[mine]), false
}

// begins: a message's words begin like s, which is their start, or they are its.
func begins(words []landed, s string) bool {
	if len(words) == 0 {
		return false
	}
	t := strings.TrimSpace(words[0].text)
	body := strings.TrimLeft(s, liveSpace)
	if t == "" || strings.TrimSpace(body) == "" {
		return false
	}
	return strings.HasPrefix(t, strings.TrimRight(body, liveSpace)) || strings.HasPrefix(body, t) && (words[0].size > 0 || wordEnd(body, t))
}

// wordEnd: t, a start of body, ends where a word does: body goes on with a space or nothing, or t ends in punctuation
// ("OK" is not the start of "OK, the tests pass", "Done." is the first block of "Done.Next block").
func wordEnd(body, t string) bool {
	if len(body) == len(t) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(body[len(t):])
	last, _ := utf8.DecodeLastRuneInString(t)
	return unicode.IsSpace(next) || unicode.IsPunct(last)
}

// trimLanded takes a message's landed words off the start of its live text, block by block.
func trimLanded(text string, words []landed) string {
	s := text
	for _, w := range words {
		t := strings.TrimSpace(w.text)
		if t == "" {
			continue
		}
		body := strings.TrimLeft(s, liveSpace)
		if strings.HasPrefix(t, strings.TrimRight(body, liveSpace)) {
			return ""
		}
		if !strings.HasPrefix(body, t) {
			break
		}
		n := len(t)
		if w.size > 0 { // a cut item holds all of its block: size bytes, less the space it begins with
			n = w.size - (len(w.text) - len(strings.TrimLeft(w.text, liveSpace)))
			for n < len(body) && !utf8.RuneStart(body[n]) {
				n++
			}
			if n >= len(body) {
				return ""
			}
		} else if !wordEnd(body, t) {
			break
		}
		s = strings.TrimLeft(body[n:], liveSpace)
	}
	return liveRest(s)
}

// liveRest is s, or "" when nothing but space is left of it.
func liveRest(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return s
}
