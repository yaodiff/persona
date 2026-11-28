package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// sourceFinalReply reads the current task's finished answer. A shared main conversation is only
// read by the client message ID of this request. Its latest turn may belong to a different chat.
func (s *Service) sourceFinalReply(ctx context.Context, t Todo) (text string, wait time.Duration) {
	if len(t.Agents) == 0 {
		return s.sourceMainReply(ctx, t)
	}
	start := t.CreatedAt
	if t.ReopenedAt != nil {
		start = *t.ReopenedAt
	}
	agents := slices.Clone(t.Agents)
	slices.SortStableFunc(agents, func(a, b TodoAgent) int { return b.LinkedAt.Compare(a.LinkedAt) })
	for _, a := range agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		items, ok := s.lastTurnItems(ctx, a.MachineID, a.SessionID)
		if !ok || len(items) == 0 {
			wait = sourceReportWait
			continue
		}
		_, side := taskTurnSource(t, items)
		if side != "" || !sourceTurnInRound(items, start, *t.DoneAt) {
			continue
		}
		reply, pending := sourceReplyItems(items)
		if pending {
			wait = sourceReportWait
		}
		if len(reply) == 0 || reply[len(reply)-1].TS.Before(a.LinkedAt) {
			continue
		}
		if text := readSourceReply(ctx, busTranscript{s: s, machine: a.MachineID, session: a.SessionID}, reply); text != "" {
			return text, 0
		}
		wait = sourceReportWait
	}
	return "", wait
}

// A linked session can have history before the task, and can keep working after it closes.
func sourceTurnInRound(items []proto.Item, start, done time.Time) bool {
	var prompt time.Time
	for _, it := range items {
		if (it.Role != "user" || it.Kind != proto.KindText) && !it.Wake {
			continue
		}
		if !prompt.IsZero() || it.TS.IsZero() || it.TS.Before(start) || it.TS.After(done) {
			return false
		}
		prompt = it.TS
	}
	return !prompt.IsZero()
}

func (s *Service) sourceMainReply(ctx context.Context, t Todo) (text string, wait time.Duration) {
	handoff := t.Source.Handoff
	if handoff == "" {
		handoff = t.Source.Anchor // existing private-chat handoffs already kept this id
	}
	machine := s.settings(ctx).MachineID
	if handoff == "" || t.Origin == "" || machine == "" || t.ReopenedAt != nil {
		return "", 0
	}
	heldThrough := -1
	if run, _ := s.channelRunByID("fb_" + handoff); run != nil {
		s.chans.mu.Lock()
		if run.Held {
			heldThrough = run.Base
		}
		s.chans.mu.Unlock()
	}
	// A read that cannot tell (a failed call, the prompt not found, the full text unreadable) keeps
	// the longest wait known for this todo. Its background work may still be running, and the short
	// wait counted from the close would already have passed and sent the fallback in its place.
	unknown := func() time.Duration {
		if heldThrough >= 0 {
			s.noteSourceWait(t.ID, channel.HeldMost)
		}
		return s.sourceWaitFloor(t.ID, sourceReportWait)
	}
	// Asked before the read, so the items read after it hold the turn the node said was over.
	idle := s.sourceSessionIdle(ctx, machine, t.Origin)
	// A second turn can start by the time the completion is recorded. Only the handoff's own
	// exchange is eligible, even when a newer answer is present.
	items, at, err := s.sourceMainPage(ctx, machine, t.Origin, channel.ClientMsgID(channel.Feishu, handoff))
	if err != nil {
		return "", unknown()
	}
	if at < 0 {
		// The node can publish the prompt after the todo closes. The channel run can
		// also be registered after that close or already have been folded into another run.
		return "", unknown()
	}

	reply, wait := sourceMainReplyItems(items[at+1:], heldThrough, idle)
	if len(reply) == 0 {
		if wait >= channel.HeldMost {
			s.noteSourceWait(t.ID, wait)
		}
		return "", wait
	}
	if text := readSourceReply(ctx, busTranscript{s: s, machine: machine, session: t.Origin}, reply); text != "" {
		return text, 0
	}
	return "", unknown()
}

// sourceSessionIdle: the node lists conversation id as done with its turn and not waiting on the
// user. A headless conversation (claude -p, how the assistant runs the main one) writes no
// turn_duration line, so this is how its turn's end shows. A conversation the node cannot list,
// or lists with no status, is not taken as idle.
func (s *Service) sourceSessionIdle(ctx context.Context, machine, id string) bool {
	c, ok := s.session(ctx, machine, id)
	if !ok || c.Starting || c.Status == proto.StatusWorking || c.State == proto.StateWorking || c.State == proto.StateNeedsYou {
		return false
	}
	return c.Status == proto.StatusIdle || c.Status == proto.StatusDone
}

// sourceMainTurns are the windows, in prompts, read for the handoff's prompt: the near one
// first, then a wider one. While background work runs, other requests to the shared
// conversation can push the prompt out of the near one.
var sourceMainTurns = []int{5, 30}

// sourceMainPage reads the main conversation back to the handoff's prompt; at is the prompt's
// index in items, -1 when no window holds it.
func (s *Service) sourceMainPage(ctx context.Context, machine, session, msgID string) (items []proto.Item, at int, err error) {
	for _, turns := range sourceMainTurns {
		var page struct{ Items []proto.Item }
		if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": session, "turns": turns}, &page); err != nil {
			return nil, -1, err
		}
		at := slices.IndexFunc(page.Items, func(it proto.Item) bool {
			return it.Role == "user" && it.Kind == proto.KindText && it.ClientMsgID == msgID
		})
		if at >= 0 {
			return page.Items, at, nil
		}
	}
	return nil, -1, nil
}

// noteSourceWait remembers that the answer of todo id was found waiting for background work
// (up to wait after the close). sourceWaitFloor gives a read that cannot tell at least that long.
// Memory only: after a restart a held channel run still says so.
func (s *Service) noteSourceWait(id string, wait time.Duration) {
	b := &s.todos
	b.reportMu.Lock()
	defer b.reportMu.Unlock()
	if wait <= b.reportWaits[id] {
		return
	}
	if b.reportWaits == nil {
		b.reportWaits = map[string]time.Duration{}
	}
	b.reportWaits[id] = wait
}

func (s *Service) sourceWaitFloor(id string, wait time.Duration) time.Duration {
	b := &s.todos
	b.reportMu.Lock()
	defer b.reportMu.Unlock()
	return max(wait, b.reportWaits[id])
}

// forgetSourceWait drops what noteSourceWait kept once the report stops waiting.
func (s *Service) forgetSourceWait(id string) {
	b := &s.todos
	b.reportMu.Lock()
	delete(b.reportWaits, id)
	b.reportMu.Unlock()
}

// sourceMainReplyItems reads the handoff's answer from items, the main conversation after the
// handoff's prompt. A wake whose tool call belongs to the handoff or one of its continuations
// carries the handoff on. An unrelated wake cannot supply the answer.
//
// A different request (sourceOtherRequest) ends the handoff: a later wake for its background
// work answers in a conversation that has moved on. The reply is then the last words one of the
// handoff's own exchanges ended with before that request, even one that was still waiting on
// background work, and nothing more is awaited.
//
// Until then, background work counts across the handoff's exchanges, as runBackground counts a
// run's: a continuation for one of several tasks is still an interim answer. A Monitor reports
// events, not an end, so it is not awaited (startsBackground). Tool results still identify
// background work after its run is gone.
//
// A continuation that a matching wake started waits up to channel.HeldMost until its turn ends,
// even when the wake ended the last background work and no channel run is held: the short wait
// counts from the close and has usually passed by then. A continuation that ends without words
// falls back to the short wait.
//
// A turn ends at its turn_duration line. The assistant runs the main conversation headless
// (claude -p), and Claude Code writes no such line there, so the handoff's exchange also ends
// where what follows shows it did: a wake, or another request that was not queued mid-turn, is
// only taken once the turn before it is over. The exchange at the end of items ends when idle
// says the node lists the conversation as done (sourceSessionIdle), and only once it holds its
// words: the node can say a turn is over a moment before its last words are in the transcript.
func sourceMainReplyItems(items []proto.Item, heldThrough int, idle bool) ([]proto.Item, time.Duration) {
	owned, start := true, 0
	wait := sourceReportWait
	var calls, background, reply, last []proto.Item
	// end closes the handoff's exchange items[start:stop], a turn that ended; seq is its last line.
	// tail: the end is only the conversation's idle state, which counts once the turn has words.
	end := func(stop, seq int, tail bool) {
		turn := items[start:stop]
		words := sourceTurnWords(turn)
		if tail && len(words) == 0 && channel.Stopped(turn) == "" {
			return
		}
		owned = false
		if len(words) != 0 {
			last = words
		}
		if len(background) != 0 || heldThrough >= 0 && (start == 0 || seq <= heldThrough) {
			return
		}
		reply = words
		if len(reply) != 0 || channel.Stopped(turn) != "" {
			wait = 0
		} else {
			wait = sourceReportWait
		}
	}
	// before is the line the exchange ending at items[i] last wrote.
	before := func(i int) int {
		if i > start {
			return items[i-1].Seq
		}
		return items[i].Seq
	}
	for i, it := range items {
		if it.Task != nil && taskEnded(it.Task.Status) {
			background = slices.DeleteFunc(background, func(launch proto.Item) bool {
				return launchedIn([]proto.Item{launch}, it.Task)
			})
		}
		if it.Wake {
			if owned {
				end(i, before(i), false)
			}
			if launchedIn(calls, it.Task) {
				owned, start = true, i+1
				reply, wait = nil, channel.HeldMost
			}
			continue
		}
		if sourceOtherRequest(it) {
			if owned && !it.Queued {
				end(i, before(i), false)
			}
			return last, 0
		}
		if !owned {
			continue
		}
		switch it.Kind {
		case proto.KindToolUse:
			calls = append(calls, it)
		case proto.KindToolResult:
			if !it.IsError && transcript.LaunchedBackgroundID(it.Text) != "" {
				calls = append(calls, it)
				if startsBackground(it.Text) {
					background = append(background, it)
				}
			}
		case turnDurationKind:
			end(i+1, it.Seq, false)
		}
	}
	if owned && idle && len(items) > start {
		end(len(items), items[len(items)-1].Seq, true)
	}
	if len(reply) != 0 {
		return reply, 0
	}
	if len(background) != 0 || heldThrough >= 0 {
		return nil, channel.HeldMost
	}
	if owned && channel.Stopped(items[start:]) != "" {
		return nil, 0
	}
	return nil, wait
}

// sourceOtherRequest: a message put into the shared conversation after the handoff's prompt,
// typed, queued mid-turn or injected by Persona. Tool results, and the pictures a tool returns,
// are user lines in a transcript too, and a wake is a background task's notice.
func sourceOtherRequest(it proto.Item) bool {
	if it.Role != "user" || it.Wake {
		return false
	}
	return it.Kind == proto.KindText || it.Kind == proto.KindImage && it.ToolUseID == ""
}

// Only assistant text after the last tool call or result is part of the final answer.
func sourceReplyItems(items []proto.Item) ([]proto.Item, bool) {
	if channel.Stopped(items) != "" {
		return nil, false
	}
	if _, ok := turnEndOf(items); !ok {
		return nil, true
	}
	return sourceTurnWords(items), false
}

// sourceTurnWords is the final answer of a turn known to have ended: its assistant text after the
// last tool call or result; none for a stopped turn.
func sourceTurnWords(items []proto.Item) []proto.Item {
	if channel.Stopped(items) != "" {
		return nil
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Kind == proto.KindToolUse || it.Kind == proto.KindToolResult || it.Role == "user" || it.Wake {
			items = items[i+1:]
			break
		}
	}
	return channel.Answer(items)
}

func readSourceReply(ctx context.Context, src channel.ItemSource, reply []proto.Item) string {
	var parts []string
	for _, it := range reply {
		text := it.Text
		if it.FullSize > 0 {
			full, err := src.Full(ctx, it.ID)
			if err != nil || full.ID != it.ID || full.Role != "assistant" || full.Kind != proto.KindText || full.FullSize > 0 || full.Text == "" {
				return "" // do not pass off a truncated item as the full answer
			}
			text = full.Text
		}
		if text = strings.TrimSpace(text); text != "" && !channel.Placeholder(text) {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}
