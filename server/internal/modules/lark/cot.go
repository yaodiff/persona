package lark

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A run's agent message (feishubot's message_cot) opens at its first thinking or tool event. A topic run does so with
// topic_cot on, a private-chat run with dm_cot on (#16847); a /read run never gets one. With cot_answer on, a run whose
// agent message is allowed gets its answer inside that message and no progress or answer card, falling back to cards
// when the message cannot carry it. Otherwise the progress card stays beside the agent message, and the answer goes
// into the card. When creation or an append fails, the card's folded panel takes over.
//
// A private run that already had an agent message before these switches keeps appending to it on the legacy path and
// finishes it, with the answer sent as a card of its own.
//
// The run's Ext keeps "cot" and "cot_msg" (the agent message's ids, shared by its events as the run id) and
// "cot_para" (the last paragraph, which the next tool calls hang under and the process ends with); "cot_off" marks a
// run whose agent message could not be used, and "cot_answer" marks one whose answer was confirmed inside it.

// cotResultMost is how much of a tool's output the agent message shows.
const cotResultMost = 1500

// The scopes a run's agent message would live in (core.cotScope says which is r's).
const (
	cotScopeNone  = iota // a /read: never an agent message
	cotScopeDM           // the owner's private chat: dm_cot
	cotScopeTopic        // a topic of an opted-in group: topic_cot
)

// cot_complete's reasons.
const (
	cotReasonDone  = "done"
	cotReasonError = "error"
)

// cotFinal is a turn's terminal snapshot for the worker.
type cotFinal struct {
	end    channel.End
	answer string // unified mode: the text that goes into the agent message; "" keeps the answer out of it
}

// cotOutcome is what the worker's terminal work came to.
type cotOutcome struct {
	native   bool // the process is in the agent message
	answered bool // and so is the answer, confirmed
}

// cotScope: where r's agent message would live. A /read never has one.
func (s core) cotScope(r *channel.Run) int {
	if s.runExt(r, readChatKey) != "" {
		return cotScopeNone
	}
	if s.runExt(r, "chat") != "" {
		return cotScopeTopic
	}
	return cotScopeDM
}

// cotProgress is the native cursor: under chanLive.mu for legacy private runs,
// owned exclusively by cotWorker for topic and private turns.
type cotProgress struct {
	// persist fences asynchronous topic writes against the run generation that owns them.
	persist func(*channel.Run, string, string)

	sent int // items of chanLive.items turned into events
	// words are what came since the conversation's last tool call, from its first words on: the words are the process
	// only once a call follows (until then they may be the answer), and thinking after them waits with them, so the
	// process keeps the conversation's order.
	words []proto.Item
	calls map[string]bool // tool calls shown and not settled yet (their output goes under them); Ext "cot_calls"
	// queue is what was made of the items already taken and has not reached Feishu yet (sent 50 at a time, the rest
	// next time): an append is never made twice.
	queue []botcard.CotEvent
	// The agent message as this run has it (the answer works on a copy of the run, and may be tried again).
	cot, msg, para string
	done, off      bool
	answered       bool // the answer's text was confirmed inside the agent message (Ext "cot_answer")
}

// cotChunk is how many events one append carries (the open platform's limit).
const cotChunk = 50

// load fills p from the run's Ext (a run the assistant took up again after a restart).
func (s core) loadCot(p *cotProgress, r *channel.Run) {
	if p.cot == "" {
		p.cot, p.msg, p.para = s.runExt(r, "cot"), s.runExt(r, "cot_msg"), s.runExt(r, "cot_para")
		p.done = p.done || s.runExt(r, "cot_done") != ""
		p.answered = p.answered || s.runExt(r, "cot_answer") != ""
		if p.calls == nil && p.cot != "" {
			for _, id := range strings.Split(s.runExt(r, "cot_calls"), ",") {
				if id != "" {
					if p.calls == nil {
						p.calls = map[string]bool{}
					}
					p.calls[id] = true
				}
			}
		}
	}
	p.off = p.off || s.runExt(r, "cot_off") != ""
}

// setCotExt keeps one of the agent message's values on the run itself: the answer works on a copy of it, and a value
// only the copy had would be gone when the answer is tried again after a restart.
func (s core) setCotExt(r *channel.Run, p *cotProgress, key, value string) {
	if p.persist != nil {
		p.persist(r, key, value)
		return
	}
	s.h.SetLiveRunExt(r, key, value)
}

// progressAfter: the progress card is there within a second or two of the message, before the turn is seen working.
func (f feishuSink) progressAfter(*channel.Run) time.Duration {
	return f.s.timing("progress_after")
}

// cotCall is a call of the hub's Feishu transport for agent messages.
func (s core) cotCall(ctx context.Context, method string, params map[string]any) error {
	return s.appCall(ctx, method, params, nil)
}

// cotAllowed reads the scope's switch. It gates only creation: an open agent message is finished regardless.
func (f feishuSink) cotAllowed(ctx context.Context, scope int) bool {
	features, ready := f.s.feishuFeatures(ctx)
	if !ready {
		return false
	}
	switch scope {
	case cotScopeTopic:
		return features.TopicCot
	case cotScopeDM:
		return features.DMCot
	}
	return false
}

// cotUnified: cot_answer is on and scope's own switch is.
func (f feishuSink) cotUnified(ctx context.Context, scope int) bool {
	features, ready := f.s.feishuFeatures(ctx)
	return ready && features.CotAnswer && f.cotAllowed(ctx, scope)
}

// cotEndReason is cot_complete's reason for a run that ended as end: a turn that did not finish normally is closed as
// done (stopped, recalled, unrelated) or as an error (failed, timed out, gone, offline), never as a finished run.
func cotEndReason(end channel.End) string {
	switch end {
	case channel.EndError, channel.EndGaveUp, channel.EndGone, channel.EndOffline:
		return cotReasonError
	}
	return cotReasonDone
}

// owned: the cursor is a worker's (it persists through the worker), not the legacy path's.
func (p *cotProgress) owned() bool { return p.persist != nil }

// cotStart creates r's agent message in its origin chat and topic. False leaves the progress card in charge; a
// refusal also marks the run so later updates keep the folded-panel fallback.
func (f feishuSink) cotStart(ctx context.Context, r *channel.Run, p *cotProgress) bool {
	s := f.s
	params := map[string]any{"reply_to": r.ReplyTo}
	to := runTo(s.runExts(r, "chat", "thread"))
	if to.ChatID != "" {
		params["chat_id"], params["in_thread"] = to.ChatID, true
	}
	var res struct {
		CotID     string `json:"cot_id"`
		MessageID string `json:"message_id"`
	}
	err := s.appCall(ctx, assistantwire.FeishuCotCreate, params, &res)
	if err == nil && (res.CotID == "" || res.MessageID == "") {
		err = errors.New("feishu: no cot_id / message_id in the answer")
	}
	if err != nil {
		if s.cotNotSent(err) || s.unreachable(err) && !s.cotMaybeSent(err) {
			return false // definitely not sent: tried again on the next look
		}
		s.logf("bots: agent message unavailable (%v); the process goes into the progress card's folded panel", err)
		s.setCotExt(r, p, "cot_off", "1")
		p.off = true
		return false
	}
	p.cot, p.msg = res.CotID, res.MessageID
	s.setCotExt(r, p, "cot", res.CotID)
	s.setCotExt(r, p, "cot_msg", res.MessageID)
	if to.ChatID == "" {
		s.rememberRun(res.MessageID, r)
	}
	return true
}

// hasProcess: a turn has something to show besides its answer (thinking, a tool call).
func hasProcess(items []proto.Item) bool {
	for _, it := range items {
		if it.Kind == proto.KindThinking || it.Kind == proto.KindToolUse {
			return true
		}
	}
	return false
}

// cotEvents turns the new items of a run's process into events (words held back until a tool call follows them: the
// words a turn ends with are its answer, which goes out on its own). para is the last paragraph so far.
// finish: the turn ended, and the thinking held with its last words goes in (the words are its answer).
func (s core) cotEvents(l i18n.Lang, p *cotProgress, items []proto.Item, para string, start func(first string) []botcard.CotEvent, finish bool) ([]botcard.CotEvent, string) {
	var out []botcard.CotEvent
	paragraph := func(id, text string) {
		if para == "" {
			out = append(out, start(id)...)
		}
		out = append(out, botcard.CotParagraph(id, text)...)
		para = id
	}
	// p changes only once the events are taken (an append that did not reach Feishu is made again from the same items).
	words := slices.Clone(p.words)
	calls := maps.Clone(p.calls)
	if calls == nil {
		calls = map[string]bool{}
	}
	defer func() { p.words, p.calls = words, calls }()
	for _, it := range items {
		switch it.Kind {
		case proto.KindThinking:
			if len(words) > 0 {
				words = append(words, it) // after words that may be the answer: shown with them, in order
				continue
			}
			paragraph("p-"+it.ID, strings.TrimSpace(it.Text))
		case proto.KindText:
			words = append(words, it)
		case proto.KindToolUse:
			for _, w := range words {
				paragraph("p-"+w.ID, strings.TrimSpace(w.Text))
			}
			words = nil
			if para == "" { // a turn that starts with a tool call: a paragraph for its calls to hang under
				paragraph("p-"+it.ID, i18n.T(l, "assistant-feishu.cot_tools_paragraph"))
			}
			id := it.ToolUseID
			if id == "" {
				id = it.ID
			}
			verb, detail := s.botStepParts(l, it)
			title := verb
			if detail != "" {
				title += " · " + detail
			}
			out = append(out, botcard.CotToolCall(id, para, it.ToolName, title, cotIcon(it.ToolName))...)
			calls[id] = true
		case proto.KindToolResult:
			if !calls[it.ToolUseID] {
				continue // its call was before this run saw the conversation
			}
			delete(calls, it.ToolUseID)
			text := strings.TrimSpace(it.Text)
			if text == "" {
				out = append(out, botcard.CotToolResult(it.ToolUseID, i18n.T(l, "assistant-feishu.cot_tool_done"), false))
				continue
			}
			if r := []rune(text); len(r) > cotResultMost {
				text = string(r[:cotResultMost]) + "\n…"
			}
			out = append(out, botcard.CotToolResult(it.ToolUseID, text, true))
		}
	}
	if finish {
		for _, w := range words {
			if w.Kind == proto.KindThinking {
				paragraph("p-"+w.ID, strings.TrimSpace(w.Text))
			}
		}
		words = nil
	}
	return out, para
}

// cotIcon is the agent message's icon for a tool.
func cotIcon(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "bash") || strings.Contains(n, "shell") || strings.Contains(n, "command"):
		return "bash"
	case strings.Contains(n, "write") || strings.Contains(n, "edit") || strings.Contains(n, "patch"):
		return "write"
	case strings.Contains(n, "read") || strings.Contains(n, "notebook"):
		return "read"
	case strings.Contains(n, "grep") || strings.Contains(n, "glob") || strings.Contains(n, "search") || strings.Contains(n, "fetch"):
		return "search"
	case strings.Contains(n, "task") || strings.Contains(n, "todo") || strings.Contains(n, "agent"):
		return "task"
	}
	return "default"
}

// cotPush adds r's new process to its agent message (finish: and ends it). False when the message can't be used (the
// run is marked: its card takes over).
func (f feishuSink) cotPush(ctx context.Context, r *channel.Run, p *runLive, items []proto.Item, fin *cotFinal) bool {
	finish := fin != nil
	s := f.s
	l := s.botLang(ctx)
	cot, msg := p.cot.cot, p.cot.msg
	start := func(first string) []botcard.CotEvent { return botcard.CotStart(r.SessionID, cot, first) }
	if p.cot.sent > len(items) {
		p.cot.sent = len(items)
	}
	next := p.cot
	events, para := s.cotEvents(l, &next, items[p.cot.sent:], p.cot.para, start, finish)
	events = append(slices.Clone(p.cot.queue), events...)
	if finish {
		for _, id := range slices.Sorted(maps.Keys(next.calls)) { // calls whose output never came: settled, not left spinning
			events = append(events, botcard.CotToolResult(id, i18n.T(l, "assistant-feishu.cot_tool_done"), false))
		}
		next.calls = nil
		if para == "" { // nothing of the process: an empty one would not open
			para = "p-" + r.ID + "-done"
			events = append(events, start(para)...)
			events = append(events, botcard.CotParagraph(para, i18n.T(l, "assistant-feishu.cot_done"))...)
		}
		switch {
		case fin.end != channel.EndDone:
			events = append(events, botcard.CotReasoningEnd(para))
		case fin.answer != "":
			events = append(events, botcard.CotFinishWithAnswer(r.SessionID, cot, para, "a-"+r.ID, fin.answer)...)
		default:
			events = append(events, botcard.CotFinish(r.SessionID, cot, para)...)
		}
	}
	// The items are taken now; what of them does not reach Feishu waits in the queue.
	p.cot.words, p.cot.sent = next.words, len(items)
	clean := true
	if !maps.Equal(next.calls, p.cot.calls) && !finish { // at the end: kept until the end got through
		p.cot.calls = next.calls
		s.setCotExt(r, &p.cot, "cot_calls", strings.Join(slices.Sorted(maps.Keys(next.calls)), ","))
	}
	if para != p.cot.para {
		p.cot.para = para
		s.setCotExt(r, &p.cot, "cot_para", para)
	}
	for len(events) > 0 {
		if ctx.Err() != nil {
			p.cot.queue = events
			return true
		}
		chunk := events[:min(cotChunk, len(events))]
		if err := s.cotCall(ctx, assistantwire.FeishuCotAppend, map[string]any{"cot_id": cot, "message_id": msg, "events": chunk}); err != nil {
			if s.cotNotSent(err) {
				p.cot.queue = events
				return true
			}
			if s.cotMaybeSent(err) {
				// The chunk may have reached Feishu (no reply came back): not sent again, which could show it twice;
				// the end is made sure of with cot_complete (cotFinishNow).
				clean = false
				s.logf("bots: agent message %s: an append got no reply (%v); going on after it", cot, err)
				events = events[len(chunk):]
				p.cot.queue = events
				if ctx.Err() != nil || len(events) == 0 && finish {
					return true // the rest on the next look (or, at the end, cot_complete)
				}
				continue
			}
			p.cot.queue = events
			if s.h.Offline(err) || errors.Is(err, rpc.ErrClosed) || !cotRefused(err) && cotPassing(err) {
				return true // not applied: tried again on the next look, from this chunk on
			}
			s.logf("bots: agent message: %v; the progress card takes over", err)
			s.setCotExt(r, &p.cot, "cot_off", "1")
			p.cot.off, p.cot.queue = true, nil
			_ = s.cotCall(ctx, assistantwire.FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": msg, "reason": cotReasonError})
			return false
		}
		events = events[len(chunk):]
	}
	p.cot.queue = nil
	// A turn that did not finish normally leaves cot_done to cotFinishNowItems, which sets it once cot_complete got
	// through. The answer counts as delivered only when every append through RUN_FINISHED was confirmed.
	p.cot.done = finish && fin.end == channel.EndDone
	if finish && fin.answer != "" && fin.end == channel.EndDone && clean {
		p.cot.answered = true
		s.setCotExt(r, &p.cot, "cot_answer", "1")
	}
	if finish {
		p.cot.calls = nil
		s.setCotExt(r, &p.cot, "cot_calls", "")
	}
	if p.cot.done {
		s.setCotExt(r, &p.cot, "cot_done", "1")
	}
	return true
}

// cotProgressNow shows r's process in its agent message. True means a legacy private run needs no card; a topic run
// returns false after its native update so the progress card still appears beside it.
func (f feishuSink) cotProgressNow(ctx context.Context, r *channel.Run) bool {
	p := f.s.live(r.ID)
	if p == nil {
		return false
	}
	items, _ := f.s.h.RunItems(r.ID)
	return f.cotProgressNowItems(ctx, r, p, items)
}

func (f feishuSink) cotProgressNowItems(ctx context.Context, r *channel.Run, p *runLive, items []proto.Item) bool {
	if ctx.Err() != nil {
		return false
	}
	f.s.loadCot(&p.cot, r)
	if p.cot.off {
		return false
	}
	if p.cot.cot == "" {
		// Creation is the worker's: a legacy private run only continues the agent message it already has.
		if !p.cot.owned() || !hasProcess(items) || !f.cotAllowed(ctx, f.s.cotScope(r)) {
			return false
		}
		if !f.cotStart(ctx, r, &p.cot) {
			return false
		}
	}
	if ctx.Err() != nil {
		return false
	}
	if !f.cotPush(ctx, r, p, items, nil) {
		return false
	}
	return !p.cot.owned() // a legacy private run needs no card; a worker's caller decides about the card itself
}

// cotFinishNow ends r's agent message as its answer goes out, creating it first when a quick topic turn ended before
// a progress look. False when there is none and the card needs its folded process panel.
func (f feishuSink) cotFinishNow(ctx context.Context, r *channel.Run) bool {
	p := f.s.live(r.ID)
	if p == nil {
		return false
	}
	items, _ := f.s.h.RunItems(r.ID)
	return f.cotFinishNowItems(ctx, r, p, items, cotFinal{end: channel.EndDone})
}

func (f feishuSink) cotFinishNowItems(ctx context.Context, r *channel.Run, p *runLive, items []proto.Item, fin cotFinal) bool {
	if ctx.Err() != nil {
		f.s.loadCot(&p.cot, r)
		if p.cot.cot != "" && !p.cot.done && !p.cot.off {
			if err := f.cotEnd(ctx, p.cot.cot, p.cot.msg, cotEndReason(fin.end)); err != nil {
				f.s.logf("bots: agent message %s: not ended: %v", p.cot.cot, err)
			} else {
				p.cot.done = true
				f.s.setCotExt(r, &p.cot, "cot_done", "1")
			}
			p.cot.queue = nil
		}
		return false
	}
	f.s.loadCot(&p.cot, r)
	if p.cot.done {
		return true // an answer tried again: its agent message has ended
	}
	if p.cot.off {
		return false
	}
	if p.cot.cot == "" {
		if !p.cot.owned() || !hasProcess(items) || !f.cotAllowed(ctx, f.s.cotScope(r)) || !f.cotStart(ctx, r, &p.cot) {
			return false
		}
	}
	if !f.cotPush(ctx, r, p, items, &fin) {
		return false
	}
	if !p.cot.done {
		// The end did not get through for now (a rate limit, a busy server) and the run is over after this answer:
		// ended without the rest, not left spinning.
		s := f.s
		if err := f.cotEnd(ctx, p.cot.cot, p.cot.msg, cotEndReason(fin.end)); err != nil {
			s.logf("bots: agent message %s: not ended: %v", p.cot.cot, err)
			p.cot.queue = nil
			return true // the answer still goes out; a later look of this run cannot end it either (the run is over)
		}
		p.cot.queue, p.cot.done = nil, true
		s.setCotExt(r, &p.cot, "cot_done", "1")
	}
	return true
}

// cotEnd ends an agent message with cot_complete, tried a few times on its own deadline (the answer's may be what
// ran out).
func (f feishuSink) cotEnd(ctx context.Context, cot, msg, reason string) error {
	var err error
	for _, wait := range f.s.timingList("cot_end_tries") {
		time.Sleep(wait)
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		err = f.s.cotCall(cctx, assistantwire.FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": msg, "reason": reason})
		cancel()
		if err == nil || !(f.s.cotNotSent(err) || f.s.h.Offline(err) || errors.Is(err, rpc.ErrClosed) || f.s.cotMaybeSent(err) || cotPassing(err)) {
			return err
		}
	}
	return err
}

// feishuCode is the error code Feishu answered a call with (0 when the call did not get that far).
func feishuCode(err error) int {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != assistantwire.CodeFeishuAPI {
		return 0
	}
	var d struct {
		Code int `json:"code"`
	}
	_ = json.Unmarshal(re.Data, &d)
	return d.Code
}

// cotRefused: Feishu will not give this bot agent messages (the app lacks the permission).
func cotRefused(err error) bool {
	switch feishuCode(err) {
	case 99991672, 99991679: // the app's / the user's scope is missing
		return true
	}
	return false
}

// cotPassing: an append that failed for now and may go through when tried again (rate limits, a busy server); any
// other error ends the agent message.
func cotPassing(err error) bool {
	switch feishuCode(err) {
	case feishuTransport, 99991400, 99991500, 99991661, 99991662, 99991663, 230020:
		return true
	}
	return false
}

// cotMaybeSent: the call went out and no reply came back (a timeout, the connection dropped after it was sent, the
// hub got no reply from Feishu): Feishu may have applied it.
func (s core) cotNotSent(err error) bool {
	return errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten) || s.h.Offline(err) || errors.Is(err, rpc.ErrClosed) && !errors.Is(err, rpc.ErrDropped)
}

func (s core) cotMaybeSent(err error) bool {
	if s.cotNotSent(err) {
		return false
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, rpc.ErrDropped) || feishuCode(err) == feishuTransport
}

// feishuTransport is the code the hub gives a Feishu call that got no answer from Feishu (a timeout, a dropped
// connection, a reply that is not Feishu's JSON: a gateway's 502).
const feishuTransport = -1
