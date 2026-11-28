package lark

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A /read's answer held for the owner (#14437, docs/extensions/bots.md "Reading a group with /read"). Unless the owner
// asked with the read_group_direct switch on (readDirectKey), a /read's answer does not go into the group: it goes to
// the owner's private chat with the bot as the very cards the group would get, the first saying which /read it answers
// (the owner's words and a link that opens the group), the last with two buttons, Post to group and Don't post. The
// group sees only the reaction meanwhile.
//
// Post to group sends those cards under the owner's message, as the answer went before: through the hub's /read source
// (extmgr/bots_read.go: that message's chat only, its reply budget, never a detour into the group when the message is
// gone) and under each piece's own key (the module's readAnswerKey, #11992). What went is kept piece by piece, so a
// second press, Lark's redelivery of a press or a press after a restart posts nothing twice; a press whose post did not
// reach Lark leaves the buttons for another try under the same keys. Don't post closes the card for good, and with no
// press nothing is ever posted: nothing goes on a timer. A post the hub or Lark refuses (the source expired, the message
// recalled or deleted, its replies used up) is said on the card, and nothing goes into the group as a message of its
// own.
//
// The held answers are kept in readHeldKey, apart from the chat's state (written on every message): each one's cards
// while it waits, and only how it ended once it is posted, declined or refused.

const (
	readHeldKey = "bots.feishu.read_held"
	// readHeldKeepFor is how long a held answer can be posted: about as long as Lark lets the bot edit the card with its
	// buttons. The hub keeps the /read source itself for 90 days (extmgr botAISourcesKeepFor), longer than this.
	readHeldKeepFor = 14 * 24 * time.Hour
	// readHeldKeep bounds the list, the oldest going first: the answers of that many /read at most.
	readHeldKeep = 30
	// readHeldPostFor is how long a press may take to post an answer's cards.
	readHeldPostFor = 2 * time.Minute
)

// How a held answer ended (readHeld.State): "" while it waits.
const (
	readHeldPosted   = "posted"
	readHeldDeclined = "declined"
	readHeldRefused  = "refused"
)

// readHeld is one held answer.
type readHeld struct {
	// ID is the key of its first piece in the group (readAnswerKey): the same for every retry of the turn's answer.
	ID        string          `json:"id"`
	ChatID    string          `json:"chat_id"`
	MessageID string          `json:"message_id"` // the owner's /read: the answer goes under it
	InThread  bool            `json:"in_thread,omitempty"`
	Ask       string          `json:"ask,omitempty"` // the owner's words, clipped (readAsk)
	Pieces    []readHeldPiece `json:"pieces"`
	// Card is the private message with the buttons: the last piece, or a card of their own (Alone) when that one did
	// not get out; "" before it went.
	Card  string    `json:"card,omitempty"`
	Alone bool      `json:"alone,omitempty"`
	State string    `json:"state,omitempty"`
	Why   string    `json:"why,omitempty"` // why the post was refused, as the card says it (readHeldRefused)
	At    time.Time `json:"at"`
}

// readHeldPiece is one card of a held answer.
type readHeldPiece struct {
	Card     string `json:"card,omitempty"` // its JSON as it goes into the group; dropped once the answer is settled
	Fallback string `json:"fallback,omitempty"`
	Key      string `json:"key"`            // its key in the group (readAnswerKey)
	Sent     string `json:"sent,omitempty"` // its message in the group, once there
}

// readHeldBook is the held answers in memory (turns.held); mu guards it and is held around the store's reads
// and writes of them.
type readHeldBook struct {
	mu      sync.Mutex
	loaded  bool
	list    []*readHeld
	posting map[string]bool // the answers a press is posting now
}

// errHeldStore: the held answer could not be kept, so nothing was posted; the answer is tried again later (the turn's
// answer returns it, turn.go).
var errHeldStore = errors.New("the /read answer could not be kept for the owner")

// readHeldLoadLocked reads the held answers once; b.mu is held.
func (s core) readHeldLoadLocked(ctx context.Context) error {
	b := &s.t.held
	if b.loaded {
		return nil
	}
	var list []*readHeld
	if _, err := s.h.Setting(ctx, readHeldKey, &list); err != nil {
		return err
	}
	b.list, b.loaded = list, true
	return nil
}

// readHeldSaveLocked writes the held answers, the old ones and those past readHeldKeep dropped first; b.mu is held.
func (s core) readHeldSaveLocked(ctx context.Context) error {
	b := &s.t.held
	now := s.t.book.clock()
	b.list = slices.DeleteFunc(b.list, func(h *readHeld) bool { return h == nil || now.Sub(h.At) > readHeldKeepFor })
	if len(b.list) > readHeldKeep {
		b.list = slices.Clone(b.list[len(b.list)-readHeldKeep:])
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.h.PutSetting(ctx, readHeldKey, b.list)
}

// find is held answer id, nil when there is none (b.mu is held).
func (b *readHeldBook) find(id string) *readHeld {
	for _, h := range b.list {
		if h != nil && h.ID == id {
			return h
		}
	}
	return nil
}

// holdRead holds a /read's answer for the owner (the turn's answer, turn.go): it goes to the owner's private chat as
// the cards the group would get (keys are their keys in the group), held there until the owner posts it or not. quiet: the run said nothing (its
// card says only how it ended), so the card goes to the owner with nothing to post. Nothing goes into the group.
// An error it returns that counts unreachable, or errHeldStore, means nothing went out: the answer is tried again, and
// a try after the cards went out posts nothing more.
func (s core) holdRead(ctx context.Context, r *channel.Run, cards []*botcard.Card, fallbacks, keys []string, quiet bool) error {
	if len(cards) == 0 || len(fallbacks) != len(cards) || len(keys) != len(cards) {
		return nil
	}
	l := s.botLang(ctx)
	h := &readHeld{ID: keys[0], ChatID: r.Ext[readChatKey], MessageID: r.ReplyTo, InThread: r.Ext[readThreadKey] != "",
		Ask: r.Ext[readAskKey], At: s.t.book.clock().UTC()}
	for i, c := range cards {
		h.Pieces = append(h.Pieces, readHeldPiece{Card: c.JSON(), Fallback: fallbacks[i], Key: keys[i]})
	}
	if quiet {
		// How it ended, for the owner only: there is no answer to post.
		c := readHeldPieceCard(l, h, 0)
		c.Link(i18n.T(l, "assistant-feishu.read_held_open"), readGroupLink(h.ChatID))
		mid, err := s.botPostTo(ctx, feishuTarget{}, "", c, fallbacks[0], readHeldPrivateKey(h.ID, "end"))
		if err == nil {
			s.rememberRun(mid, r)
		}
		return err
	}
	b := &s.t.held
	b.mu.Lock()
	if err := s.readHeldLoadLocked(ctx); err != nil {
		b.mu.Unlock()
		s.logf("bots: read the held /read answers: %v", err)
		return errHeldStore
	}
	if had := b.find(h.ID); had != nil {
		if had.Card != "" || had.State != "" {
			b.mu.Unlock()
			return nil // a retry of an answer whose cards went out already
		}
		h = had // a retry of one whose cards did not get out: the same cards, under the same keys
	} else {
		b.list = append(b.list, h)
		if err := s.readHeldSaveLocked(ctx); err != nil {
			b.list = slices.DeleteFunc(b.list, func(x *readHeld) bool { return x == h })
			b.mu.Unlock()
			s.logf("bots: keep a held /read answer: %v", err)
			return errHeldStore
		}
	}
	held := *h
	held.Pieces = slices.Clone(h.Pieces)
	b.mu.Unlock()

	var first, buttons string
	var missing []int
	last := len(held.Pieces) - 1
	for i, p := range held.Pieces {
		c := readHeldPieceCard(l, &held, i)
		if i == last {
			readHeldWaiting(l, &held, c)
		}
		mid, err := s.botPostTo(ctx, feishuTarget{}, "", c, p.Fallback, readHeldPrivateKey(p.Key, "card"))
		if err != nil {
			if i == 0 && s.unreachable(err) {
				return err // nothing went out: the answer is tried again, under the same keys
			}
			s.logf("bots: a held /read answer's card: %v", err)
			missing = append(missing, i+1)
			continue
		}
		s.rememberRun(mid, r)
		first = cmp.Or(first, mid)
		if i == last {
			buttons = mid
		}
	}
	if buttons == "" {
		// The card with the buttons did not get out: they come on a card of their own, so the answer can still be posted.
		c := readHeldFromCard(l, &held)
		readHeldWaiting(l, &held, c)
		mid, err := s.botPostTo(ctx, feishuTarget{}, "", c, "", readHeldPrivateKey(held.ID, "buttons"))
		if err != nil {
			s.logf("bots: a held /read answer's buttons: %v", err)
		} else {
			buttons, held.Alone = mid, true
			s.rememberRun(mid, r)
		}
	}
	if len(missing) > 0 {
		s.botNoteTo(ctx, feishuTarget{}, cmp.Or(first, buttons), missingPiecesNote(l, missing))
	}
	if buttons == "" {
		return nil // nothing to press: the owner was told what did not get out
	}
	b.mu.Lock()
	if cur := b.find(held.ID); cur != nil {
		cur.Card, cur.Alone = buttons, held.Alone
		if err := s.readHeldSaveLocked(ctx); err != nil {
			s.logf("bots: keep a held /read answer's card: %v", err)
		}
	}
	b.mu.Unlock()
	return nil
}

// readHeldNote tells the owner, in the private chat, what kept a /read from being done: a held /read says nothing in
// its group (chat and ask as on the held answer's card).
func (s core) readHeldNote(ctx context.Context, chat, ask, text string) {
	l := s.botLang(ctx)
	c := readHeldFromCard(l, &readHeld{ChatID: chat, Ask: ask}).Markdown(botcard.EscapeMarkdown(text))
	c.Link(i18n.T(l, "assistant-feishu.read_held_open"), readGroupLink(chat))
	if _, err := s.botPostTo(ctx, feishuTarget{}, "", c, text, ""); err != nil {
		s.logf("bots: /read: tell the owner: %v", err)
	}
}

// readWorking tells the owner, in the private chat, that a held /read is still being worked on, once it has worked
// read_working_after (#16808): it reads the group and investigates before it answers, which can take minutes, and the
// group's reaction is all the owner would see meanwhile. Once per run (runLive.readNoted), under a key of its own so a
// restart that forgets it does not say it twice within Lark's hour. A /read answered straight in the group says nothing
// here: the group's reaction is where its owner looks.
func (s core) readWorking(ctx context.Context, r *channel.Run, took time.Duration) {
	if took < s.timing("read_working_after") || s.runExt(r, readDirectKey) != "" {
		return
	}
	prog := s.live(r.ID)
	if prog == nil {
		return // a run not followed any more
	}
	prog.mu.Lock()
	noted := prog.readNoted
	prog.readNoted = true
	prog.mu.Unlock()
	if noted {
		return
	}
	l := s.botLang(ctx)
	h := &readHeld{ChatID: s.runExt(r, readChatKey), Ask: s.runExt(r, readAskKey)}
	text := i18n.T(l, "assistant-feishu.read_working")
	c := readHeldFromCard(l, h).Note(text)
	c.Link(i18n.T(l, "assistant-feishu.read_held_open"), readGroupLink(h.ChatID))
	if _, err := s.botPostTo(ctx, feishuTarget{}, "", c, text, readHeldPrivateKey(r.ReplyTo, "working")); err != nil {
		s.logf("bots: /read: tell the owner it is being worked on: %v", err)
	}
}

// readHeldAction answers a press of a held answer's buttons (the card action "rd"): do is "post" or "skip". Lark
// waits 3 seconds for the answer: a post that takes longer says so, and the card is changed when it is done.
func (s core) readHeldAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	l := s.botLang(ctx)
	id, do := a.Str("id"), a.Str("do")
	if do != "post" && do != "skip" {
		return toast("error", i18n.T(l, "assistant-feishu_cards.unknown_action"))
	}
	var readOff bool
	if do == "post" {
		f, ready := s.feishuFeatures(ctx)
		readOff = ready && !f.ReadGroup
	}
	b := &s.t.held
	b.mu.Lock()
	if err := s.readHeldLoadLocked(ctx); err != nil {
		b.mu.Unlock()
		s.logf("bots: read the held /read answers: %v", err)
		return toast("error", i18n.T(l, "http-status.unavailable"))
	}
	h := b.find(id)
	switch {
	case h == nil || s.t.book.clock().Sub(h.At) > readHeldKeepFor:
		b.mu.Unlock()
		return toast("info", i18n.T(l, "assistant-feishu.read_held_expired"))
	case h.State != "":
		text := readHeldEnded(l, h)
		b.mu.Unlock()
		return toast("info", text)
	case b.posting[id]:
		b.mu.Unlock()
		return toast("info", i18n.T(l, "assistant-feishu.read_held_posting"))
	case readOff:
		b.mu.Unlock()
		return toast("warning", i18n.T(l, "assistant-feishu.read_held_read_off"))
	}
	msg := cmp.Or(a.Context.MessageID, h.Card)
	if do == "skip" {
		before := *h
		before.Pieces = slices.Clone(h.Pieces)
		card := readHeldSettleLocked(l, h, readHeldDeclined, "")
		if err := s.readHeldSaveLocked(ctx); err != nil {
			*h = before // not recorded: the buttons work again
			b.mu.Unlock()
			s.logf("bots: decline a held /read answer: %v", err)
			return toast("error", i18n.T(l, "http-status.unavailable"))
		}
		b.mu.Unlock()
		return &botcard.CardResponse{Toast: &botcard.Toast{Type: "success", Content: i18n.T(l, "assistant-feishu.read_held_declined")}, Card: card.Raw()}
	}
	if b.posting == nil {
		b.posting = map[string]bool{}
	}
	b.posting[id] = true
	b.mu.Unlock()
	done := make(chan readHeldOutcome, 1)
	go func() {
		pctx, cancel := context.WithTimeout(context.Background(), readHeldPostFor)
		defer cancel()
		done <- s.postHeldRead(pctx, l, id)
	}()
	select {
	case o := <-done:
		return o.response()
	case <-time.After(s.timing("card_answer_wait")):
		go func() {
			o := <-done
			if msg == "" {
				return
			}
			uctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if o.card != nil {
				if err := s.botUpdateCard(uctx, msg, o.card); err != nil {
					s.logf("bots: a held /read answer's card: %v", err)
				}
			} else {
				s.botNote(uctx, msg, o.text) // the buttons stay: the note says to press again
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu.read_held_posting"))
	}
}

// readHeldOutcome is what a press to post came to: the toast, and the card to show instead (nil: it stays, buttons
// and all, for another press).
type readHeldOutcome struct {
	kind, text string
	card       *botcard.Card
}

func (o readHeldOutcome) response() *botcard.CardResponse {
	resp := toast(o.kind, o.text)
	if o.card != nil {
		resp.Card = o.card.Raw()
	}
	return resp
}

// postHeldRead posts held answer id's cards that are not in the group yet under the owner's /read, each under its own
// key, recording each as it goes. All in: the card says it was posted. A post Lark may not have got leaves the buttons
// for another press; one the hub or Lark refused settles the answer as not posted, and says why.
func (s core) postHeldRead(ctx context.Context, l i18n.Lang, id string) readHeldOutcome {
	b := &s.t.held
	b.mu.Lock()
	h := b.find(id)
	if h == nil {
		delete(b.posting, id)
		b.mu.Unlock()
		return readHeldOutcome{kind: "info", text: i18n.T(l, "assistant-feishu.read_held_expired")}
	}
	held := *h
	held.Pieces = slices.Clone(h.Pieces)
	b.mu.Unlock()
	to := feishuTarget{ChatID: held.ChatID, InThread: held.InThread, NoDetour: true, Read: true}
	var failed error
	for i, p := range held.Pieces {
		if p.Sent != "" {
			continue
		}
		mid, err := s.botPostContent(ctx, to, held.MessageID, p.Card, p.Fallback, p.Key)
		if err != nil {
			failed = err
			break
		}
		b.mu.Lock()
		if i < len(h.Pieces) {
			h.Pieces[i].Sent = cmp.Or(mid, "?") // gone out, whatever its id
		}
		if err := s.readHeldSaveLocked(ctx); err != nil {
			s.logf("bots: keep a held /read answer's post: %v", err)
		}
		b.mu.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.posting, id)
	if failed == nil {
		card := readHeldSettleLocked(l, h, readHeldPosted, "")
		if err := s.readHeldSaveLocked(ctx); err != nil {
			s.logf("bots: keep a held /read answer's post: %v", err)
		}
		return readHeldOutcome{kind: "success", text: i18n.T(l, "assistant-feishu.read_held_posted"), card: card}
	}
	s.logf("bots: post a held /read answer: %v", failed)
	why, final := s.readHeldWhy(l, failed)
	if !final {
		return readHeldOutcome{kind: "error", text: i18n.T(l, "assistant-feishu.read_held_try_again", i18n.Args{"why": why})}
	}
	card := readHeldSettleLocked(l, h, readHeldRefused, why)
	if err := s.readHeldSaveLocked(ctx); err != nil {
		s.logf("bots: keep a held /read answer's refusal: %v", err)
	}
	return readHeldOutcome{kind: "error", text: readHeldEnded(l, h), card: card}
}

// readHeldWhy is why a post did not go, in the owner's words, and whether pressing again can help (false: it may,
// later; Lark or the hub may not have got it).
func (s core) readHeldWhy(l i18n.Lang, err error) (why string, final bool) {
	var refused *rpc.Error
	switch {
	case errors.As(err, &refused) && refused.Code == assistantwire.CodeChatDenied:
		return i18n.T(l, "assistant-feishu.read_held_denied"), true
	case botcard.MessageGone(err):
		return i18n.T(l, "assistant-feishu.read_held_gone"), true
	case s.unreachable(err) || botcard.RateLimited(err):
		return i18n.T(l, "assistant-feishu.read_held_unreachable"), false
	case botcard.Refused(err):
		return clip(oneLine(err.Error()), 120), true
	}
	return clip(oneLine(err.Error()), 120), false
}

// readHeldSettleLocked ends held answer h (posted, declined or refused for why): the card it now shows in the
// private chat, the last piece with how it ended in place of the buttons. Its cards are no longer kept: nothing can
// post them now. b.mu is held.
func readHeldSettleLocked(l i18n.Lang, h *readHeld, state, why string) *botcard.Card {
	h.State, h.Why = state, why
	c := readHeldFromCard(l, h)
	if n := len(h.Pieces); n > 0 && h.Pieces[n-1].Card != "" && !h.Alone {
		c = readHeldPieceCard(l, h, n-1)
	}
	c.Note(readHeldEnded(l, h))
	c.Link(i18n.T(l, "assistant-feishu.read_held_open"), readGroupLink(h.ChatID))
	for i := range h.Pieces {
		h.Pieces[i].Card, h.Pieces[i].Fallback = "", ""
	}
	return c
}

// readHeldEnded says how held answer h ended.
func readHeldEnded(l i18n.Lang, h *readHeld) string {
	switch h.State {
	case readHeldPosted:
		return i18n.T(l, "assistant-feishu.read_held_posted")
	case readHeldDeclined:
		return i18n.T(l, "assistant-feishu.read_held_declined")
	}
	sent := 0
	for _, p := range h.Pieces {
		if p.Sent != "" {
			sent++
		}
	}
	if sent > 0 {
		return i18n.T(l, "assistant-feishu.read_held_partly", i18n.Args{"sent": sent, "total": len(h.Pieces), "why": h.Why})
	}
	return i18n.T(l, "assistant-feishu.read_held_not_posted", i18n.Args{"why": h.Why})
}

// readHeldPieceCard is piece i of held answer h as the private chat shows it: the card the group would get, the first
// with the line saying which /read it answers on top.
func readHeldPieceCard(l i18n.Lang, h *readHeld, i int) *botcard.Card {
	var c botcard.Card
	if err := json.Unmarshal([]byte(h.Pieces[i].Card), &c); err != nil || c.Schema == "" {
		return readHeldFromCard(l, h)
	}
	if i == 0 {
		from := botcard.NewCard("", "", "").Note(readHeldFrom(l, h)).Body.Elements
		c.Body.Elements = append(from, c.Body.Elements...)
	}
	return &c
}

// readHeldFromCard is a card of the held answer's own, with only the line saying which /read it answers.
func readHeldFromCard(l i18n.Lang, h *readHeld) *botcard.Card {
	from := readHeldFrom(l, h)
	return botcard.NewCard("", "", from).Note(from)
}

// readHeldFrom says which /read a card answers: the owner's words, when there were any.
func readHeldFrom(l i18n.Lang, h *readHeld) string {
	if h.Ask == "" {
		return i18n.T(l, "assistant-feishu.read_held_from_bare")
	}
	return i18n.T(l, "assistant-feishu.read_held_from", i18n.Args{"ask": h.Ask})
}

// readHeldWaiting ends the card of a held answer that waits: who sees it, the two buttons, and the way to the group.
func readHeldWaiting(l i18n.Lang, h *readHeld, c *botcard.Card) {
	c.Note(i18n.T(l, "assistant-feishu.read_held_waiting"))
	c.Buttons(
		botcard.Btn(i18n.T(l, "assistant-feishu.read_held_post"), botcard.Primary, map[string]any{"a": "rd", "id": h.ID, "do": "post"}),
		botcard.Btn(i18n.T(l, "assistant-feishu.read_held_skip"), botcard.Default, map[string]any{"a": "rd", "id": h.ID, "do": "skip"}))
	c.Link(i18n.T(l, "assistant-feishu.read_held_open"), readGroupLink(h.ChatID))
}

// readGroupLink opens the group in the Lark app (an applink, as SidebarURL's).
func readGroupLink(chat string) string {
	return "https://applink.feishu.cn/client/chat/open?openChatId=" + url.QueryEscape(chat)
}

// readHeldPrivateKey is the key a held answer's card goes into the private chat under (its group key and what the
// card is): a retry lands once, and never as the group's own card. Hashed to fit Lark's 50-character uuid.
func readHeldPrivateKey(key, what string) string {
	sum := sha256.Sum256([]byte("held:" + key + "\x00" + what))
	return "rh-" + hex.EncodeToString(sum[:12])
}
