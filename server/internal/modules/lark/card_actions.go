package lark

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// --- card buttons ---

// FeishuAction is feishu.card_action: the owner pressed one of the bot's cards (the hub checked it is the owner);
// the answer is the toast and the card to show instead. Feishu waits 3 seconds for it.
func (s core) FeishuAction(ctx context.Context, p FeishuAction) (*botcard.CardResponse, error) {
	var a botcard.CardAction
	if err := json.Unmarshal(p.Event, &a); err != nil {
		return nil, badRequest(err)
	}
	resp := s.botCardAction(ctx, &a, p.Action)
	if resp != nil && resp.Card != nil {
		if c, ok := resp.Card.Data.(*botcard.Card); ok {
			s.workbenchStamp(ctx, c) // the card drawn in its place keeps its way back to the workbench
		}
	}
	return resp, nil
}

// botCardAction answers a press; action is the owner's action for the card it presses (what answers it).
func (s core) botCardAction(ctx context.Context, a *botcard.CardAction, action string) *botcard.CardResponse {
	l := s.botLang(ctx)
	switch a.Str("a") {
	case "nop": // a link of a "more" menu was opened: nothing to do
		return nil
	case "ix":
		return s.answerFromCard(ctx, a, action)
	case "rd": // a /read's answer held for the owner: post it into the group, or not (read_held.go)
		return s.readHeldAction(ctx, a)
	case "accept":
		// The hub checked the owner pressed it; marking a task verified approves nothing. The decision is the core's
		// (its acceptance ledger and decisions), and so is what the toast says.
		return toast(s.h.AcceptAction(ctx, a))
	case "later_done": // the Handled button of a question that can wait, on a briefing card (later.go)
		ok, err := s.h.LaterHandled(ctx, a.Str("id"))
		if err != nil { // the queue (or a question's to-do) can't be read or written: the question stays, the button works again
			return toast("error", i18n.T(l, "http-status.unavailable"))
		}
		if ok {
			return toast("success", i18n.T(l, "assistant-briefing.later_handled"))
		}
		return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
	case "switch":
		f, _ := s.feishuFeatures(ctx)
		kind, m, sess, title := a.Str("k"), a.Str("m"), a.Str("s"), a.Str("t")
		t := botTarget{Kind: kind, MachineID: m, SessionID: sess}
		if kind == targetMain {
			t = botTarget{Kind: targetMain}
		}
		if err := s.botAllowed(l, f, t); err != nil {
			return toast("error", err.Error())
		}
		s.setBotCurrent(t)
		// The callback only carries the pressed card's own message id (no thread root, no chat beyond its id): bind
		// the card itself. A quote reply to it has it as the parent, and a topic started on it has it as the root,
		// so botTargetFor routes both to this target.
		s.rememberBotThread(a.Context.MessageID, t, title)
		return toast("success", i18n.T(l, "assistant-feishu_cards.now_to_reply", i18n.Args{"title": clip(title, 30)}))
	case "start_form":
		if f, _ := s.feishuFeatures(ctx); !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
		// A double tap and the hub's resend of a call it gave up on arrive as presses of the same list card: only the
		// first opens a form, the rest get the same toast (as the /start button below does for its own presses).
		mid := a.Context.MessageID
		var at time.Time
		if mid != "" {
			var pressed bool
			if pressed, at = s.pressOnce(mid, "form", s.timing("form_window")); pressed {
				return toast("info", i18n.T(l, "assistant-feishu_cards.start_form_opening"))
			}
		}
		// Feishu gives the callback 3 s and the post goes over the bus to the feishu app, so the /start form is
		// posted from a goroutine (as botStartAgent posts its card), quoting the pressed list card.
		s.t.workers.Add(1) // the layer's background work (Layer.Wait)
		go func() {
			defer s.t.workers.Done()
			cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := s.botPost(cctx, mid, s.startAgentCard(cctx, ""), ""); err != nil {
				s.logf("bots: start form: %v", err)
				if mid != "" {
					// Nothing opened, so a press again must go through instead of being answered with the same toast.
					s.releasePress(mid, "form", at)
				}
				// A post that failed by running out of time used up cctx, so the note gets a budget of its own.
				nctx, ncancel := context.WithTimeout(context.Background(), 10*time.Second)
				s.botNote(nctx, mid, i18n.T(s.botLang(nctx), "assistant-feishu_cards.start_form_failed", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
				ncancel()
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.start_form_opening"))
	case "start":
		if f, _ := s.feishuFeatures(ctx); !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
		machine, cwd, prompt, mode := a.Form("machine"), strings.TrimSpace(a.Form("cwd")), strings.TrimSpace(a.Form("prompt")), a.Form("mode")
		if machine == "" || cwd == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.pick_machine_dir"))
		}
		// A double tap, Feishu's 3 s redelivery, and the hub's own resend all arrive as presses of the same card
		// message: within the window only the first starts an agent, the rest get the same toast.
		mid := a.Context.MessageID
		var at time.Time
		if mid != "" {
			var pressed bool
			if pressed, at = s.startCardPressed(mid); pressed {
				return toast("info", i18n.T(l, "assistant-feishu_cards.starting"))
			}
		}
		s.t.workers.Add(1) // the layer's background work (Layer.Wait)
		go func() {
			defer s.t.workers.Done()
			if err := s.botStartAgent(machine, cwd, prompt, mode); err != nil && mid != "" {
				// Nothing started, so a retry press of the same card must go through. at makes this a compare-and-swap:
				// a failure arriving after the window only frees the slot if a later press has not re-stamped it.
				s.forgetStartCard(mid, at)
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.starting"))
	case "nav": // a workbench button (workbench.go)
		return s.workbenchNav(ctx, a)
	case "ib": // an Inbox button of the workbench's /inbox card
		return s.workbenchInboxAction(ctx, a)
	case "stop":
		// A progress card may outlive its run when its final edit was refused.
		f, _ := s.feishuFeatures(ctx)
		t := botTarget{Kind: a.Str("k"), MachineID: a.Str("m"), SessionID: a.Str("s")}
		return toast(s.stopProgressCard(ctx, f, t, a.Context.MessageID))
	}
	return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
}

// cardAnswerWait is how long a press waits for its answer to go through before it says "Processing…": the hub has
// Feishu's 3 seconds, and the press came over the bus (the timing "card_answer_wait").
const cardAnswerWait = 1500 * time.Millisecond

// answerFromCard turns a press on an interaction card into the same answer the app would give, and resolves it.
func (s core) answerFromCard(ctx context.Context, a *botcard.CardAction, action string) *botcard.CardResponse {
	l := s.botLang(ctx)
	id := a.Str("id")
	var expected *time.Time
	if raw := strings.TrimSpace(a.Str("created_at")); raw != "" {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
		}
		expected = &at
	}
	var got struct {
		Interaction proto.Interaction `json:"interaction"`
		OK          bool              `json:"ok"`
		Status      string            `json:"status"`
	}
	if err := s.appCallTo(ctx, assistantwire.AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); err != nil {
		return toast("error", clip(oneLine(err.Error()), 80))
	}
	if !got.OK && !finalStatus(got.Status) {
		// Not up now, but not known to be over either (in the history, not restored yet; or the history could not
		// say): the card stays as it is.
		return toast("info", s.h.EndedText(l, got.Status, "assistant-feishu_cards.already_handled"))
	}
	if !got.OK {
		// Over for good (answered elsewhere, maybe while the assistant was away): this card is closed as such, its
		// buttons gone. A same-ID replacement already in the ledger is not the occurrence pressed here.
		ix := proto.Interaction{ID: id}
		if expected != nil {
			ix.CreatedAt = *expected
		}
		card, had, same := s.takeBotCardOccurrence(id, ix)
		if !same {
			return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
		}
		if had {
			ix = card.Ix
		}
		ended := s.h.EndedText(l, got.Status, "assistant-channels.ended")
		return &botcard.CardResponse{Toast: &botcard.Toast{Type: "info", Content: s.h.EndedText(l, got.Status, "assistant-feishu_cards.already_handled")},
			Card: s.answeredCard(ctx, ix, ended).Raw()}
	}
	ix := got.Interaction
	if expected != nil && !ix.CreatedAt.Equal(*expected) {
		// The live card is a same-ID replacement, not the operation pressed here.
		// Reject before consuming an owner action or mutating either bot card.
		return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
	}
	type outcome struct {
		ok  bool
		err error
	}
	if a.Str("clarify") == "1" {
		question := a.Form("question")
		if !ix.CanClarify || ix.Private {
			return toast("error", i18n.T(l, "assistant-feishu_cards.unknown_action"))
		}
		if err := s.h.ValidateClarifyQuestion(question); err != nil {
			return toast("warning", err.Error())
		}
		// The callback token identifies this form delivery. Replaying it uses the
		// same owner-message ID, while a later deliberate press gets a fresh one.
		// Older callbacks without a token still have their one-use owner action;
		// never collapse every tokenless question on this card into one delivery.
		delivery := strings.TrimSpace(a.Token)
		if delivery == "" {
			delivery = strings.TrimSpace(action)
		}
		if delivery == "" {
			delivery = randomHex(8)
		}
		clientMsgID := channel.ClientMsgID(channel.Feishu, "approval-"+delivery)
		done := make(chan outcome, 1)
		go func() {
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			ok, err := s.h.ClarifyInteractionAt(rctx, id, question, clientMsgID, action, a.Context.ChatID, a.Context.MessageID, expected)
			done <- outcome{ok, err}
		}()
		select {
		case o := <-done:
			if o.err != nil {
				return toast("error", clip(oneLine(o.err.Error()), 120))
			}
			if !o.ok {
				return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
			}
			return toast("success", i18n.T(l, "assistant-feishu_cards.question_sent_pending"))
		case <-time.After(s.timing("card_answer_wait")):
			msg := a.Context.MessageID
			go func() {
				o := <-done
				if msg == "" {
					return
				}
				nctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				if o.err != nil {
					s.botNote(nctx, msg, i18n.T(l, "assistant-feishu_cards.press_not_sent", i18n.Args{"error": clip(oneLine(o.err.Error()), 200)}))
				} else if o.ok {
					s.botNote(nctx, msg, i18n.T(l, "assistant-feishu_cards.question_sent_pending"))
				}
			}()
			return toast("info", i18n.T(l, "assistant-feishu_cards.working_on_it"))
		}
	}
	yes, no := true, false
	var r proto.Resolution
	switch {
	case a.Str("dismiss") == "1":
		r.Dismiss = true
	case ix.Kind == proto.IxQuestion && a.Str("form") == "1":
		r.Answers = map[string]string{}
		for i, q := range ix.Questions {
			ans := strings.TrimSpace(a.Form("t" + strconv.Itoa(i)))
			if ans == "" {
				ans = a.Form("q" + strconv.Itoa(i))
			}
			if ans != "" {
				r.Answers[q.Question] = ans
			}
		}
		if len(r.Answers) < len(ix.Questions) {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.answer_each"))
		}
	case ix.Kind == proto.IxQuestion:
		i, _ := strconv.Atoi(a.Str("q"))
		if i < 0 || i >= len(ix.Questions) {
			return toast("error", i18n.T(l, "assistant-feishu_cards.question_mismatch"))
		}
		ans := a.Str("v")
		if a.Str("free") == "1" {
			ans = strings.TrimSpace(a.Form("text"))
		}
		if ans == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.write_answer"))
		}
		r.Answers = map[string]string{ix.Questions[i].Question: ans}
	case ix.Kind == proto.IxPlan && a.Str("approve") == "1":
		r.Approve, r.Mode = &yes, a.Str("mode")
	case ix.Kind == proto.IxPlan:
		r.Approve, r.Feedback = &no, strings.TrimSpace(a.Form("feedback"))
	case ix.Kind == proto.IxPermission:
		if len(ix.Fields) > 0 {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.fill_in_app"))
		}
		if a.Str("allow") == "1" {
			r.Allow, r.Always = &yes, a.Str("always") == "1"
		} else {
			r.Allow = &no
		}
	case ix.Kind == proto.IxScreen && a.Str("opt") != "":
		r.Option = a.Str("opt")
	case ix.Kind == proto.IxScreen && a.Str("typed") == "1":
		text := a.Form("text")
		if text == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.write_text_first"))
		}
		r.Keys, r.Text = []string{"enter"}, text
	case ix.Kind == proto.IxScreen && a.Str("keys") != "":
		r.Keys = []string{a.Str("keys")}
	default:
		return toast("error", i18n.T(l, "assistant-feishu_cards.unknown_action"))
	}
	// This card is closed by the answer below, not again when the interaction ends.
	// The occurrence check makes the optimistic removal a compare-and-swap: a
	// replacement announced after the lookup must stay answerable.
	card, had, same := s.takeBotCardOccurrence(id, ix)
	if !same {
		return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
	}
	restore := func(ctx context.Context) bool {
		if had {
			return s.botRestoreCard(ctx, id, card)
		}
		return false
	}
	how := s.h.ResolutionText(l, ix, r)
	// Screen and Codex cards act on the terminal and can take a few seconds; Feishu waits 3 s for this answer.
	done := make(chan outcome, 1)
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ok, err := s.h.ResolveInteractionAt(rctx, id, r, action, expected)
		done <- outcome{ok, err}
	}()
	select {
	case o := <-done:
		switch {
		case o.err != nil:
			restore(ctx)
			return toast("error", clip(oneLine(o.err.Error()), 80))
		case !o.ok:
			return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
		}
		return &botcard.CardResponse{Toast: &botcard.Toast{Type: "success", Content: how}, Card: s.answeredCard(ctx, ix, how).Raw()}
	case <-time.After(s.timing("card_answer_wait")):
		msg := a.Context.MessageID
		go func() {
			o := <-done
			if o.err != nil {
				s.logf("bots: answer %s: %v", id, o.err)
			}
			if o.err == nil && o.ok {
				if msg != "" {
					cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					defer cancel()
					s.botCardDone(cctx, msg, ix, how)
				}
				return
			}
			// Not answered: the card waits again (botRestoreCard leaves out one that ended meanwhile), and the chat is
			// told what became of the press — its toast said "Working on it…" long ago, and nothing else would say
			// that the press failed or lost to another surface (#4123).
			rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			back := restore(rctx)
			cancel()
			if msg == "" {
				return
			}
			// The restore may have used up rctx: the note gets a budget of its own.
			nctx, ncancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer ncancel()
			switch {
			case o.err != nil:
				s.botNote(nctx, msg, i18n.T(l, "assistant-feishu_cards.press_not_sent", i18n.Args{"error": clip(oneLine(o.err.Error()), 200)}))
			case !back:
				// Answered elsewhere while this press was out, and its own ended update found no card to close it:
				// close it here as handled, as the typed answer does.
				s.botCardDone(nctx, msg, ix, i18n.T(l, "assistant-feishu_cards.handled"))
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.working_on_it"))
	}
}

// differentBotCardOccurrence distinguishes two occurrence-bearing cards under
// one runtime ID. A zero timestamp is the legacy ID-only form and retains its
// old behavior; newly written ledger entries always carry the timestamp.
func differentBotCardOccurrence(a, b proto.Interaction) bool {
	return !a.CreatedAt.IsZero() && !b.CreatedAt.IsZero() &&
		(!a.CreatedAt.Equal(b.CreatedAt) || a.MachineID != b.MachineID || a.SessionID != b.SessionID)
}

// takeBotCardOccurrence removes id's ledger entry only if it still represents
// expected. same is false when a same-ID replacement arrived after the lookup.
func (s core) takeBotCardOccurrence(id string, expected proto.Interaction) (card botCard, had, same bool) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	card, had = b.cards[id]
	if had && differentBotCardOccurrence(card.Ix, expected) {
		return card, true, false
	}
	s.keepBotCardLocked(id, nil)
	return card, had, true
}

// botRestoreCard puts back the card of interaction id, closed optimistically for an answer that did not go through,
// unless the hub says it is no longer pending: it ended while the answer was out, and its interaction.ended found no
// card to close, so putting it back would leave a card waiting that nothing answers (#5012). When that cannot be
// asked either, the card is put back, as before. A different occurrence, live or already in the ledger, is never
// overwritten. It reports whether the card waits again.
func (s core) botRestoreCard(ctx context.Context, id string, card botCard) bool {
	var got struct {
		OK          bool              `json:"ok"`
		Interaction proto.Interaction `json:"interaction"`
	}
	if err := s.appCallTo(ctx, assistantwire.AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); err == nil {
		if !got.OK || differentBotCardOccurrence(card.Ix, got.Interaction) {
			return false
		}
	}
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if current, ok := b.cards[id]; ok && differentBotCardOccurrence(card.Ix, current.Ix) {
		return false
	}
	s.keepBotCardLocked(id, &card)
	return true
}

// botStartCardWindow is how long a repeat press of the same /start card is ignored: a double tap, Feishu's 3 s
// redelivery, and the hub's ~2 s resend all fall inside it. It is deliberately shorter than botStartAgent's
// 2-minute timeout: if the first start is still running when the window ends, the owner pressing again is a
// deliberate manual retry and may start a second agent. The transport retries above arrive at ~2-3 s, while the
// slot is held for the whole outstanding call, so they can never reach that second start (the timing
// "start_card_window").
const botStartCardWindow = 60 * time.Second

// botFormWindow is how long a repeat press of the same card's "Start new agent" button is ignored: a double tap and
// the hub's ~2-3 s resend fall inside it. Much shorter than botStartCardWindow: two agents started from the same form
// are a duplicate, while asking the form open again a few seconds later is the owner wanting another one (the timing
// "form_window").
const botFormWindow = 10 * time.Second

// botPressMost is how long a press is remembered at most: the longest window, after which the sweep drops it so the map
// cannot grow without bound. A press kind with a longer window than these two must be added here, or its live slots get
// swept away.
func (s core) botPressMost() time.Duration {
	return max(s.timing("start_card_window"), s.timing("form_window"))
}

// pressOnce reports whether message mid's button kind was already pressed within window, and records this press
// either way. A true answer means the caller must not do the thing again. The returned time is the timestamp this
// press stored (or the existing one for a duplicate), for releasePress's compare-and-swap.
func (s core) pressOnce(mid, kind string, window time.Duration) (bool, time.Time) {
	b := s.fbot()
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	key := mid + kind
	if at, ok := b.pressAt[key]; ok && now.Sub(at) < window {
		return true, at
	}
	if b.pressAt == nil {
		b.pressAt = map[string]time.Time{}
	}
	for k, at := range b.pressAt { // forget presses past the longest window so the map cannot grow without bound
		if now.Sub(at) >= s.botPressMost() {
			delete(b.pressAt, k)
		}
	}
	b.pressAt[key] = now
	return false, now
}

// releasePress releases message mid's button kind slot after what the press was for failed, so pressing again retries
// it instead of being answered with a bare "Starting" / "Opening the start form". It only deletes the slot this press
// stamped (at): if the window passed and a later manual press re-stamped it while the first one was still hung, that
// newer slot must survive the first press's late failure.
func (s core) releasePress(mid, kind string, at time.Time) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	key := mid + kind
	if cur, ok := b.pressAt[key]; ok && cur.Equal(at) {
		delete(b.pressAt, key)
	}
}

// startCardPressed is pressOnce for the /start card's Start button (botStartCardWindow).
func (s core) startCardPressed(mid string) (bool, time.Time) {
	return s.pressOnce(mid, "start", s.timing("start_card_window"))
}

// forgetStartCard is releasePress for the /start card's Start button.
func (s core) forgetStartCard(mid string, at time.Time) {
	s.releasePress(mid, "start", at)
}

// botStartAgent starts a coding agent from the /start card and points the chat at it; mode is the form's permission
// mode (the workbench's field), "" the launcher's own. A non-nil error was already told to the owner as a note; it
// means no agent was started.
func (s core) botStartAgent(machine, cwd, prompt, mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l := s.botLang(ctx)
	d, ok := s.h.Device(ctx, machine)
	if !ok {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.no_such_machine"))
		return assistantwire.ErrNoDevice
	}
	cwd = proto.ExpandHome(cwd, d.Home)
	params := map[string]any{"cwd": cwd, "prompt": prompt, "tab_name": s.h.TabName(ctx, prompt)} // agents_start.go
	if slices.Contains(proto.PermissionModes, mode) {
		params["permission_mode"] = mode
	}
	if err := s.h.ApplyModel(ctx, "agent.new", params); err != nil {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.cannot_start", i18n.Args{"error": err.Error()}))
		return err
	}
	var res struct{ Session proto.Session }
	var err error
	if res.Session, err = s.h.StartAgent(ctx, d.ID, params); err != nil {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.cannot_start", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		return err
	}
	t := botTarget{Kind: targetAgent, MachineID: d.ID, SessionID: res.Session.SessionID}
	preferTab := s.h.UseHerdrTabName(ctx)
	label, secondary := s.h.SessionNamesAs(res.Session, preferTab)
	if label == "" || label == "." {
		label = cwd
	}
	label += " · " + d.Name
	s.setBotCurrent(t)
	c := botcard.NewCard(i18n.T(l, "assistant-feishu_cards.started"), botcard.Blue, i18n.T(l, "assistant-feishu_cards.started")).Subtitle(dotJoin(label, secondary)).
		Markdown(i18n.T(l, "assistant-feishu_cards.started_body", i18n.Args{"machine": botcard.EscapeMarkdown(d.Name), "dir": botcard.Code(cwd)}))
	if prompt != "" {
		c.Note(i18n.T(l, "assistant-feishu_cards.started_working"))
	}
	mid, err := s.botPost(ctx, "", c.AppendToLinks(s.openButton(ctx, d.ID, res.Session.SessionID)), "")
	if err != nil {
		s.logf("bots: started note: %v", err)
	}
	s.rememberBotThread(mid, t, label)
	if prompt != "" && mid != "" {
		s.h.StartChannelRun(&channel.Run{ID: "fb_" + mid, Channel: channel.Feishu, ReplyTo: mid, MachineID: t.MachineID, SessionID: t.SessionID,
			TargetKind: t.Kind, Label: label, Started: time.Now().UTC()})
	}
	return nil
}
