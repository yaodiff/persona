package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- the turn, as it looks in Feishu ----

// feishuSink is the Feishu bot as the pipeline's sink: a progress card replying to the message after a few seconds,
// rewritten every few, the answer written into it (or cards of their own when long), and the message's reaction going
// from OnIt to DONE. With cot_answer on, a turn whose agent message is allowed writes its answer into that message and
// sends no card, falling back to these cards when the message cannot carry it (#16847).
type feishuSink struct{ s core }

// progressIdle: the card's elapsed time moves every few seconds, new steps or not: a long turn must not look stuck.
func (f feishuSink) progressIdle() time.Duration { return f.s.timing("progress_idle") }

// nativeWorker is the run's worker, nil when it has none. It takes only the run's own lock.
func (f feishuSink) nativeWorker(r *channel.Run) *cotWorker {
	owner := f.s.live(r.ID)
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	return owner.native
}

// cotWorked: r's agent message is a worker's. Topics always; a private run with dm_cot on, or one that already
// has a worker (the route does not change mid-turn: the legacy path has a different cursor and would append twice).
func (f feishuSink) cotWorked(ctx context.Context, r *channel.Run, scope int) bool {
	switch scope {
	case cotScopeTopic:
		return true
	case cotScopeDM:
		if f.cotAllowed(ctx, scope) {
			return true
		}
		return f.nativeWorker(r) != nil
	}
	return false
}

// holdsCard is unified mode holding the progress card back: none has been posted, the agent message is not off,
// and it is open already or the turn is still inside the grace. w is nil before the first snapshot. Past the grace a
// turn with no process gets its card.
func (f feishuSink) holdsCard(r *channel.Run, w *cotWorker, took time.Duration) bool {
	if r.Ext["card"] != "" {
		return false
	}
	if w != nil {
		open, off := w.state()
		if off {
			return false
		}
		if open {
			return true
		}
	}
	return took < f.s.timing("cot_unified_grace")
}

// When Feishu says the card is updated too often, updates stop for 5 s, then 10, 20, at most 30; the answer is not
// held back by the pause, except that its own last write into the card waits the first window out once
// (answer_card_wait, timings.go).
const (
	cardBackoffFirst = 5 * time.Second
	cardBackoffMost  = 30 * time.Second
)

func (f feishuSink) progress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	s := f.s
	ctx = s.sendAs(ctx, "progress", r.MachineID, r.SessionID)
	if s.runExt(r, readChatKey) != "" {
		// A /read shows no progress in its group: the reaction says it is being worked on (read.go). A held one that
		// takes a while tells the owner once, privately (readWorking).
		s.readWorking(ctx, r, took)
		return
	}
	scope := s.cotScope(r)
	switch {
	case f.cotWorked(ctx, r, scope):
		// Queued as this returns, after the card: the worker's persistence takes the channels' lock, which a
		// progress tick holds across this call, so queuing first would keep that tick, and the next one's card,
		// waiting on the native API. The queue itself only replaces a snapshot.
		defer func() { f.cotQueue(r, nil) }()
		if f.cotUnified(ctx, scope) && f.holdsCard(r, f.nativeWorker(r), took) {
			return // unified mode: no progress card while the agent message carries the turn
		}
	case f.cotProgressNow(ctx, r):
		return
	}
	c := progressCard(s.botLang(ctx), r.Label, took, steps, live, s.h.RunProgress(r))
	prog := s.live(r.ID)
	if prog == nil {
		prog = &runLive{} // a run not followed any more: nothing to remember
	}
	sum := sha256.Sum256([]byte(c.JSON()))
	if card := s.runExt(r, "card"); card != "" {
		now := s.chansNow()
		if k := s.kitOf(r, prog); k != nil && f.kitGone(ctx, k, card) {
			s.forgetProgressCard(r, prog)
			s.logf("bots: progress card gone (withdrawn or deleted); the next tick posts a new one")
			return
		}
		if sum == prog.sentCard {
			return // nothing new
		}
		if f.kitProgress(ctx, r, prog, c) { // a card entity: written into, its words streamed (stream.go)
			prog.sentCard = sum
			return
		}
		if now.Before(prog.quietUntil) {
			return // Feishu asked us to slow down: a later tick sends the latest
		}
		err := s.botUpdateCard(ctx, card, c)
		switch {
		case err == nil:
			prog.sentCard, prog.backoff = sum, 0
		case botcard.RateLimited(err):
			prog.backoff = min(max(2*prog.backoff, cardBackoffFirst), cardBackoffMost)
			prog.quietUntil = now.Add(prog.backoff)
			s.logf("bots: progress update: %v; quiet for %s", err, prog.backoff)
		case botcard.MessageGone(err):
			// Withdrawn or deleted: the next tick posts a new card, and the answer does not go into this one.
			s.forgetProgressCard(r, prog)
			s.logf("bots: progress card gone: %v", err)
		default:
			s.logf("bots: progress update: %v", err)
		}
		return
	}
	to := runTo(s.runExts(r, "chat", "thread"))
	mid, err, kit := f.kitPost(ctx, r, prog, c, to)
	if !kit {
		// A card sent as a message has no entity, even one an earlier card of the run had (forgetProgressCard).
		s.setRunExt(r, "card_id", "")
		prog.kit = nil
		mid, err = s.botPostTo(ctx, to, r.ReplyTo, c, "", "")
	}
	if err != nil {
		s.logf("bots: progress card: %v", err)
		return
	}
	prog.sentCard, prog.backoff, prog.quietUntil = sum, 0, time.Time{}
	s.setRunExt(r, "card", mid)
	if to.ChatID == "" { // a topic's messages never reach the private chat's routing
		s.rememberRun(mid, r)
	}
}

func (f feishuSink) folded(ctx context.Context, o *channel.Run, owner *runLive) {
	var native *cotWorker
	if owner != nil {
		owner.mu.Lock()
		native = owner.native
		owner.mu.Unlock()
	}
	if native != nil {
		native.abort()
	}
	if o.Ext["card"] != "" {
		f.s.kitEnd(ctx, nil, o.Ext)
		_ = f.s.botUpdateCard(ctx, o.Ext["card"], botcard.NewCard("", "", "").Note(i18n.T(f.s.botLang(ctx), "assistant-feishu.folded")))
	}
	if cot := o.Ext["cot"]; cot != "" && native == nil {
		_ = f.s.cotCall(ctx, assistantwire.FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": o.Ext["cot_msg"], "reason": "done"})
	}
}

// recalled takes a recalled message's marks off when the run it was folded into goes on without it: its reaction,
// and its own progress card (which said it is answered with the next message) says it was recalled.
func (f feishuSink) recalled(ctx context.Context, m channel.Folded) {
	s := f.s
	if rx := m.Ext["reaction"]; rx != "" && m.ReplyTo != "" {
		s.botUnreact(ctx, m.ReplyTo, rx)
	}
	if card := m.Ext["card"]; card != "" {
		s.kitEnd(ctx, nil, m.Ext)
		l := s.botLang(ctx)
		c := botcard.NewCard("", "", "").Note(i18n.T(l, "assistant-feishu.recalled_folded"))
		endHeader(l, c, channel.EndRecalled)
		if err := s.botUpdateCard(ctx, card, c); err != nil {
			s.logf("bots: a recalled message's card: %v", err)
		}
	}
}

// answer writes the answer into the progress card (more cards when long), and marks the messages DONE. When nothing
// of it got out because the hub could not be reached it says so (tried again later); once a piece is out, the rest
// is best effort (a card sent twice would show twice). The card's last line says how the run ended; a quiet end
// still turns the progress card into that line — or, when that card was refused or the run gave it back on a held
// wait, posts it as a card of its own under a key (so a retry lands once) — so the chat never stays on "Working" and
// is never left silent.
func (f feishuSink) answer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) (answerErr error) {
	s := f.s
	ctx = s.sendAs(ctx, "answer", r.MachineID, r.SessionID)
	l := s.botLang(ctx)
	all := f.s.runSteps(r.ID)
	foot := endFoot(l, r.Label, took, len(all), final, end)
	if note != "" {
		foot += "\n" + note
	}
	// The process is in the agent message above the answer, or else folded into the answer's first card. In unified
	// mode the answer itself goes into the agent message, and no card is sent, when the worker confirms it.
	var steps []string
	native := false
	var worker *cotWorker
	var repair *cotFallback
	scope := s.cotScope(r)
	read := readRun(r.Ext)
	card := r.Ext["card"]
	fin := &cotFinal{end: end}
	worked := f.cotWorked(ctx, r, scope)
	if worked && end == channel.EndDone && !quiet && !read && card == "" && f.cotUnified(ctx, scope) {
		if plain := answerCards(l, s.linkNamer(ctx), answer, false, nil, foot); len(plain) == 1 && plain[0].fallback != "" {
			fin.answer = plain[0].fallback + "\n\n" + foot
		}
	}
	if worked {
		native, worker = f.cotQueue(r, fin)
	} else {
		native = f.cotFinishNow(ctx, r)
	}
	if fin.answer != "" && worker != nil {
		out, ok := worker.result(ctx, s.timing("cot_answer_wait"))
		if ok && out.answered {
			f.markDone(ctx, r, final, end)
			return nil
		}
		// The outcome is known: nothing to repair later, so the worker skips its repair wait.
		native, worker = ok && out.native, nil
	}
	if worker != nil {
		defer func() {
			if answerErr == nil {
				worker.answerFallback(repair)
			}
		}()
	}
	if !native {
		steps = append([]string{}, all...)
	}
	// A /read's answer may be read by the whole group, posted there directly or by the owner: the process (commands,
	// paths on the computer) and the web link to the conversation stay out of it (read.go).
	if read {
		steps = nil
	}
	if card != "" {
		// Its last write is as a message: the card stops streaming first (the run's lock is held, stream.go).
		var k *cardKit
		if prog := s.live(r.ID); prog != nil {
			k = prog.kit
		}
		s.kitEnd(ctx, k, r.Ext)
	}
	// With answer_new_card the answer is a card of its own at the bottom of the chat (Lark notifies on a new message,
	// not on an edit) and the progress card folds into the process (#5396); a quiet ending stays in the progress card.
	newCard := false
	if f, ready := s.feishuFeatures(ctx); ready && f.AnswerNewCard && card != "" && !quiet {
		newCard = true
	}
	answerSteps := steps
	if newCard {
		answerSteps = nil // they go into the folded progress card
	}
	cards := answerCards(l, s.linkNamer(ctx), answer, quiet, answerSteps, foot)
	var fallbackCards []botAnswerCard
	if worker != nil && native && !read {
		fallbackCards = answerCards(l, s.linkNamer(ctx), answer, quiet, all, foot)
	}
	if quiet {
		c := botcard.NewCard("", "", foot)
		processPanel(l, c, steps)
		cards = []botAnswerCard{{card: c.Note(foot)}}
		if worker != nil && native && !read {
			fallback := botcard.NewCard("", "", foot)
			processPanel(l, fallback, all)
			fallbackCards = []botAnswerCard{{card: fallback.Note(foot)}}
		}
	}
	// Unless the owner asked with the switch to answer in the group directly, a /read's answer goes to the owner's
	// private chat, and into the group only when the owner posts it (read_held.go, #14437): the group
	// sees nothing of it meanwhile but the reaction. r is a copy of the run (channels_answer.go): its Ext is read
	// without the lock.
	if read && r.Ext[readDirectKey] == "" {
		if err := f.holdRead(ctx, r, cards, quiet, end); err != nil {
			return err
		}
		f.markDone(ctx, r, final, end)
		return nil
	}
	to := runTo(r.Ext)
	var missing []int // the pieces that did not get out (1-based): said at the end, not sent again
	defer func() {
		if len(missing) > 0 {
			s.botNoteTo(ctx, to, r.ReplyTo, missingPiecesNote(l, missing))
		}
	}()
	for i, ac := range cards {
		c, piece := ac.card, ac.fallback
		var fallback *botcard.Card
		if i == 0 && len(fallbackCards) > 0 {
			fallback = fallbackCards[0].card
		}
		if i == len(cards)-1 && r.SessionID != "" && !read {
			// The conversation's web link goes under the last piece only, as on pushed and mirrored turns, in the row of
			// the answer's own links.
			c.AppendToLinks(s.openButton(ctx, r.MachineID, r.SessionID))
			if fallback != nil {
				fallback.AppendToLinks(s.openButton(ctx, r.MachineID, r.SessionID))
			}
		}
		if i == 0 {
			endHeader(l, c, end)
			if fallback != nil {
				endHeader(l, fallback, end)
			}
		}
		if quiet && i == 0 {
			quietMessage := card
			// The ending has to land somewhere: in the progress card the run still has, or — when that card was refused
			// (recalled, no longer a card, still limited after the wait) or a held run gave its card back with its
			// message — as a card of its own, which says how the run ended, done or not (#4228). Recalled messages and
			// unrelated held waits get no new card; an unrelated turn's already-open progress card is closed without
			// carrying its text.
			var err error
			post := end != channel.EndRecalled && end != channel.EndUnrelated
			if card != "" {
				if err = s.botUpdateAnswerCard(ctx, card, c); err == nil {
					post = false
				} else if post {
					s.logf("bots: the progress card's end: %v; sending it", err)
				}
			}
			if post {
				var mid string
				mid, err = s.botPostTo(ctx, to, r.ReplyTo, c, foot, endKey(r.ID))
				if err == nil {
					quietMessage = mid
				}
				if err == nil && to.ChatID == "" {
					s.rememberRun(mid, r)
				}
			}
			if err != nil {
				if s.unreachable(err) {
					return err // nothing went out: the ending is tried again later, under the same key
				}
				s.logf("bots: the run's quiet ending: %v", err)
			}
			if err == nil && fallback != nil {
				repair = &cotFallback{message: quietMessage, card: fallback}
			}
			break
		}
		var mid string
		var err error
		answerPostedNew := false
		if i == 0 && newCard {
			// Under a key of the progress card's: a retry after an unreachable failure lands once.
			if mid, err = s.botPostTo(ctx, to, r.ReplyTo, c, piece, answerKey(r.ID, card)); err == nil {
				answerPostedNew = true
				if worker != nil && native && !read {
					repair = &cotFallback{message: card, card: s.foldedProgressCard(l, all)}
				}
				if ferr := s.botUpdateAnswerCard(ctx, card, s.foldedProgressCard(l, steps)); ferr != nil {
					s.logf("bots: fold the progress card: %v", ferr)
				}
			} else if s.unreachable(err) {
				return err // nothing went out: the whole answer is tried again later, under the same key
			} else {
				s.logf("bots: answer as a new card: %v; writing it into the progress card", err)
				mid = ""
			}
		}
		if i == 0 && card != "" && mid == "" {
			if err = s.botUpdateAnswerCard(ctx, card, c); err == nil {
				mid = card
			} else {
				s.logf("bots: answer into the progress card: %v; sending it", err)
			}
		}
		if mid == "" {
			key := ""
			if read {
				// A /read has no progress card to write into or to key a new card by: each piece goes out under a key of
				// its own turn, so a retry after an unreachable failure lands once in the group (#11987).
				key = readAnswerKey(r.ID, r.WaitFrom(), i)
			}
			mid, err = s.botPostTo(ctx, to, r.ReplyTo, c, piece, key)
		}
		if err != nil {
			if i == 0 && s.unreachable(err) {
				return err // nothing went out: the whole answer is tried again later
			}
			s.logf("bots: answer: %v", err)
			missing = append(missing, i+1)
			continue
		}
		if i == 0 && fallback != nil && !answerPostedNew {
			repair = &cotFallback{message: mid, card: fallback}
		}
		if to.ChatID == "" {
			s.rememberRun(mid, r)
		}
	}
	f.markDone(ctx, r, final, end)
	return nil
}

// holdRead holds a /read's answer for the owner (read_held.go): the cards the group would get, the first with how the
// run ended, each under the key it goes into the group by (readAnswerKey). As in the group, a recalled request or an
// unrelated held wait says nothing. Only a failure to reach the hub or to keep the answer is returned: the answer is
// tried again.
func (f feishuSink) holdRead(ctx context.Context, r *channel.Run, cards []botAnswerCard, quiet bool, end channel.End) error {
	s := f.s
	if len(cards) == 0 || quiet && (end == channel.EndRecalled || end == channel.EndUnrelated) {
		return nil
	}
	endHeader(s.botLang(ctx), cards[0].card, end)
	list, fallbacks, keys := make([]*botcard.Card, len(cards)), make([]string, len(cards)), make([]string, len(cards))
	for i, ac := range cards {
		list[i], fallbacks[i], keys[i] = ac.card, ac.fallback, readAnswerKey(r.ID, r.WaitFrom(), i)
	}
	if err := s.holdRead(ctx, r, list, fallbacks, keys, quiet); err != nil {
		if s.unreachable(err) || errors.Is(err, errHeldStore) {
			return err
		}
		s.logf("bots: hold a /read's answer: %v", err)
	}
	return nil
}

// markDone takes the reaction off the run's messages and marks them DONE once the run is over (final).
func (f feishuSink) markDone(ctx context.Context, r *channel.Run, final bool, end channel.End) {
	s := f.s
	if !final {
		return
	}
	for _, m := range append(r.Folded, channel.Folded{ReplyTo: r.ReplyTo, Ext: r.Ext}) {
		if m.ReplyTo == "" {
			continue
		}
		if rx := m.Ext["reaction"]; rx != "" {
			s.botUnreact(ctx, m.ReplyTo, rx)
		}
		if end == channel.EndRecalled {
			continue // the message is gone: nothing to mark done
		}
		if _, err := s.botReact(ctx, m.ReplyTo, "DONE"); err != nil {
			s.logf("bots: react: %v", err)
		}
	}
}

// answerKey is the key a run's answer goes out under as a new card (answer_new_card): one per progress card, as a
// held run answers each turn under a card of its own. Hashed to fit Feishu's 50-character uuid, as endKey is.
func answerKey(id, card string) string {
	sum := sha256.Sum256([]byte("answer:" + id + "\x00" + card))
	return "ans-" + hex.EncodeToString(sum[:12])
}

// readAnswerKey is the key piece i of a /read's answer goes out under: the same for every retry of the turn (a retry
// keeps the run and its wait's start; its footer's elapsed time changes, so the hub's key from the card's words would
// not), another for each piece and for a held run's later turn. Hashed to fit Feishu's 50-character uuid.
func readAnswerKey(id string, turn time.Time, i int) string {
	sum := sha256.Sum256([]byte("read:" + id + "\x00" + strconv.FormatInt(turn.UnixNano(), 10) + "\x00" + strconv.Itoa(i)))
	return "rd-" + hex.EncodeToString(sum[:12])
}

// foldedProgressCard is what a progress card becomes once its answer went out as a new card: the process, folded,
// and a line pointing to the answer below it.
func (s core) foldedProgressCard(l i18n.Lang, steps []string) *botcard.Card {
	below := i18n.T(l, "assistant-feishu.answer_below")
	c := botcard.NewCard("", "", below)
	processPanel(l, c, steps)
	return c.Note("↓ " + below)
}

// endKey is the key a run's quiet ending goes out under when it is posted as a card of its own: a retry after an
// unreachable failure lands once. Feishu caps a uuid at 50 characters, so it is hashed like the nudge's.
func endKey(id string) string {
	sum := sha256.Sum256([]byte("end:" + id))
	return "end-" + hex.EncodeToString(sum[:12])
}

// botUpdateAnswerCard writes the answer into the progress card. Feishu refusing the update for being too often is
// waited out once: the run is over, so no later update writes that card again, and the owner would keep a card that
// says it is still working. Any other refusal, and a second one, is left to the caller (a card of the answer's own).
func (s core) botUpdateAnswerCard(ctx context.Context, messageID string, card *botcard.Card) error {
	err := s.botUpdateCard(ctx, messageID, card)
	if err == nil || !botcard.RateLimited(err) {
		return err
	}
	wait := s.timing("answer_card_wait")
	s.logf("bots: the progress card's answer: %v; waiting %s", err, wait)
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.botUpdateCard(ctx, messageID, card)
}

// endFoot is an answer's last line: how it ended, the label and time it took, and how many tools it called (one
// answered first while background work goes on says no end).
func endFoot(l i18n.Lang, label string, took time.Duration, tools int, final bool, end channel.End) string {
	foot := i18n.T(l, "assistant-feishu.took", i18n.Args{"label": label, "took": channel.Duration(l, took)})
	if final {
		key := "assistant-feishu.end_done"
		if end != channel.EndDone {
			key = "assistant-feishu.end_" + string(end)
		}
		foot = i18n.T(l, key) + " · " + foot
	}
	if tools > 0 {
		foot += " · " + i18n.T(l, "assistant-feishu.tools_called", i18n.Args{"count": tools})
	}
	return foot
}

// endHeader gives a card of a run that did not end normally a header saying so, unless the answer has its own.
func endHeader(l i18n.Lang, c *botcard.Card, end channel.End) {
	if end == channel.EndDone || c.Header != nil {
		return
	}
	color := botcard.Grey
	if end == channel.EndError || end == channel.EndOffline {
		color = botcard.Orange
	}
	c.Header = botcard.NewCard(i18n.T(l, "assistant-feishu.end_"+string(end)), color, "").Header
}
