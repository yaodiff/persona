package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

const triageConcurrency = 4

// triageInbox sorts open items whose content changed since they were last looked at into "Needs you" / "FYI" / "Noise"
// with a one-line summary (the small-task model, inbox.triage). It runs in the background; one pass at a time.
func (s *Service) triageInbox(ctx context.Context) {
	b := &s.inbox
	if !b.triaging.TryLock() {
		return
	}
	b.mu.Lock()
	var todo []*InboxItem
	for _, it := range b.items {
		if it.open() && it.TriagedAt != it.Last && it.Context != "" {
			if it.handled() { // what joins an event being handled follows it: its conversation decides (inbox_handle.go)
				it.TriagedAt = it.Last
				continue
			}
			todo = append(todo, it)
		}
	}
	b.mu.Unlock()
	if len(todo) == 0 {
		b.triaging.Unlock()
		s.fileFoldersSoon(ctx)
		s.handInbox(ctx)
		return
	}
	// The pass outlives the caller: a pull to refresh answers before it is done.
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer b.triaging.Unlock()
		sem := make(chan struct{}, triageConcurrency)
		var wg sync.WaitGroup
		for _, it := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(it *InboxItem) {
				defer wg.Done()
				defer func() { <-sem }()
				if w := s.triage(ctx, it); w != nil {
					// Undone, the item is untriaged again: the next pass asks the model again.
					s.saveWrites(ctx, []*inboxWrite{w}, "its triage")
				}
			}(it)
		}
		wg.Wait()
		s.fileFoldersSoon(ctx) // where the items go (inbox_folders.go), in its own background pass
		s.handInbox(ctx)
	}()
}

// triage asks the model about an item in the list whose content changed; when the model fails the item gets its
// fallback tier (and no summary). When its connection is held the item stays untriaged. An item without a fallback
// (a connector's thing its rules leave to the model) keeps what it shows until a pass gets an answer. It returns the
// change, settled (inbox_rollback.go); nil when the item was left as it was.
func (s *Service) triage(ctx context.Context, it *InboxItem) *inboxWrite {
	last, v := s.askTriage(ctx, it, 90*time.Second)
	if ctx.Err() != nil {
		return nil
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	if it.Last != last { // new messages arrived meanwhile: the next pass sees them
		return nil
	}
	w := beginWrite(it)
	settled := func() *inboxWrite { b.settle(w); return w }
	fallback := b.ruleTier(it)
	switch {
	case v.state == triagePaused:
		// The model's connection is held (quota used up, busy): not triaged, so a pass after it is back asks again.
		// Meanwhile an untriaged item sits in its rule tier rather than in "Needs you".
		if it.Tier == "" && fallback != "" {
			it.Tier, it.Reason = fallback, b.byRules()
			b.applyPolicyTier(it)
			return settled()
		}
		return nil
	case v.state == "":
		it.Tier, it.Summary, it.Reason = v.tier, v.summary, v.reason
		it.Urgent = v.urgent && v.tier == TierAction
	case fallback == "":
		return nil
	default:
		if v.err != nil {
			s.logf("inbox: triage %s: %v", it.ID, v.err)
		}
		it.Tier, it.Reason = fallback, b.byRules()
	}
	b.applyPolicyTier(it) // the user's policy has the last word on the tier (inbox_policies.go)
	b.liftMerged(it)      // a merged row stays at its rows' highest tier (inbox_merge.go, #7230)
	it.TriagedAt = last
	b.noteTriaged(it)
	// Triage can settle an item read elsewhere while it was still untriaged (not one filed while the model thought).
	if it.Read && it.Tier != TierAction && it.open() {
		b.file(it, time.Now(), "read")
	}
	return settled()
}

// Why the model gave no tier (a triage verdict's state, GET /api/v1/inbox triage.state), the most telling first.
const (
	triageUnconfigured = "unconfigured" // no model is set up for inbox.triage
	triagePaused       = "paused"       // its connection is held: quota used up, or busy
	triageFailed       = "error"        // it failed or answered without a tier
	triageTimeout      = "timeout"      // too slow
	triageBehind       = "behind"       // not asked: the round had more new items than it triages (admitInbox)
)

var triageStates = []string{triageUnconfigured, triagePaused, triageFailed, triageTimeout, triageBehind}

// triageVerdict is the model's answer about one item, or (state) why there is none.
type triageVerdict struct {
	tier, summary, reason string
	urgent                bool   // someone waits, a deadline is near (action only)
	state                 string // "" when answered
	why                   string // the state for people: "model out of quota"
	err                   error
}

// askTriage asks the model about one item, under the item's own rules when its source sent some (they replace
// inbox.rules; the default criteria stay underneath). last is the Last it asked about.
func (s *Service) askTriage(ctx context.Context, it *InboxItem, timeout time.Duration) (last string, v triageVerdict) {
	b := &s.inbox
	b.mu.Lock()
	last, input, rules := it.Last, b.triageInput(it), strings.TrimSpace(it.Rules)
	b.mu.Unlock()
	return last, s.triageModel(ctx, input, rules, timeout)
}

// triageModel asks the triage model about one item's prompt input under rules (its source's; "" the Inbox's).
func (s *Service) triageModel(ctx context.Context, input, rules string, timeout time.Duration) (v triageVerdict) {
	if rules == "" {
		rules = s.inboxConfig(ctx).Rules
	}
	hello := s.hello(ctx)
	extContext, _ := s.inboxExtensionContext(ctx)
	system := prompts.RenderInboxTriage(prompts.InboxTriage{Owner: hello.Owner, Rules: rules, Language: answerLanguage(hello.Locale),
		Context: extContext})
	var out struct {
		Tier    string `json:"tier"`
		Summary string `json:"summary"`
		Reason  string `json:"reason"`
		Urgent  bool   `json:"urgent"`
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	err := s.inboxModelJSON(cctx, purposeTriage, system, input, 0, &out)
	slow := errors.Is(cctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
	cancel()
	paused, quota := modelPaused(err)
	l := s.lang(ctx)
	switch {
	case paused:
		v.state, v.why = triagePaused, i18n.T(l, "assistant-inbox_triage.busy")
		if quota {
			v.why = i18n.T(l, "assistant-inbox_triage.no_quota")
		}
	case out.Tier == TierAction || out.Tier == TierFYI || out.Tier == TierNoise:
		v.tier, v.summary, v.reason = out.Tier, clip(oneLine(out.Summary), 80), clip(oneLine(out.Reason), 30)
		v.urgent = out.Urgent && out.Tier == TierAction
	case modelUnconfigured(err):
		v.state, v.why = triageUnconfigured, i18n.T(l, "assistant-inbox_triage.unconfigured")
	case slow:
		v.state, v.why = triageTimeout, i18n.T(l, "assistant-inbox_triage.slow")
	default:
		v.state, v.why = triageFailed, i18n.T(l, "assistant-inbox_triage.failed")
	}
	v.err = err
	return v
}

// ruleTier is the fallback tier, without a model: people talking to me need me, groups are FYI, bots and lists are
// noise; a connector's item has the tier its rules gave, else noise; a plugin's connector's none ("": only the model
// sorts it, inbox_admit.go), nor one's whose source is not known yet to be built in (the manager just started).
func (b *inboxBook) ruleTier(it *InboxItem) string {
	if it.Connector != nil {
		switch {
		case it.Connector.RuleTier != "":
			return it.Connector.RuleTier
		case b.fromExtension(it.Source) || !b.builtinSource(it.Source):
			return ""
		}
		return TierNoise
	}
	switch it.Kind {
	case KindMention:
		// An @all without an @me is an announcement: worth knowing, not waiting on me.
		if it.Lark != nil && it.Lark.AtAll && !it.Lark.AtMe {
			return TierFYI
		}
		return TierAction
	case KindDM, KindDoc:
		return TierAction
	case KindMoment: // an @ asks for me; a comment or a reply is mostly chatter
		if it.Moment != nil && strings.HasPrefix(it.Moment.Notice, "atIn") {
			return TierAction
		}
		return TierFYI
	case KindGroup:
		return TierFYI
	case KindMail:
		if it.Mail != nil && it.Mail.Direct {
			return TierFYI
		}
	}
	return TierNoise
}

// triageInput is the item as the models read it (prompts/inbox/item.md).
func (b *inboxBook) triageInput(it *InboxItem) string {
	return prompts.RenderInboxItem(b.promptItem(it))
}

func (b *inboxBook) promptItem(it *InboxItem) prompts.InboxItem {
	p := prompts.InboxItem{Kind: it.Kind, MailDirect: it.Mail != nil && it.Mail.Direct, Context: it.Context}
	if it.Connector != nil {
		p.Source = it.Source
		p.Described = b.kindWords(it.Source, it.Kind)
	}
	return p
}

// draftModelWait is how long a draft may take (the app waits on it).
const draftModelWait = 2 * time.Minute

// draftReply writes a reply to an inbox item in the user's voice (optionally following their instruction).
func (s *Service) draftReply(ctx context.Context, id, instruction string) (string, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	var item prompts.InboxItem
	if ok {
		item = b.promptItem(it)
	}
	b.mu.Unlock()
	if !ok {
		return "", errNoInboxItem
	}
	hello := s.hello(ctx)
	extContext, neverQuote := s.inboxExtensionContext(ctx)
	// About me as the user last edited it in the app, else the config file's (notes), as the main chat has it.
	d := prompts.InboxDraft{Owner: hello.Owner, Notes: s.notes(ctx), Language: answerLanguage(hello.Locale), Context: extContext}
	input := prompts.RenderInboxDraftInput(prompts.InboxDraftInput{Item: item, Instruction: strings.TrimSpace(instruction)})
	cctx, cancel := context.WithTimeout(ctx, draftModelWait)
	defer cancel()
	// A draft that repeats a sentence an extension marked never to quote (what it keeps about people: evaluations,
	// guesses) is written again once, told so; if that one repeats one too, there is no draft (extension_context.go).
	for {
		text, err := s.inboxModelComplete(cctx, purposeDraft, prompts.RenderInboxDraft(d), input, 1024)
		if err != nil {
			return "", err
		}
		text = strings.TrimSpace(text)
		hit := quotesAny(text, neverQuote)
		if hit == "" {
			return text, nil
		}
		if d.Rewrite {
			s.logf("inbox: draft for %s withheld: the rewrite still repeats a sentence an extension marked never to quote", id)
			return "", errDraftWithheld
		}
		s.logf("inbox: draft for %s repeats a sentence an extension marked never to quote; writing it again", id)
		d.Rewrite = true
	}
}

// inboxReply serializes the replies to one inbox item and remembers the last one that went out.
type inboxReply struct {
	mu       sync.Mutex
	clientID string
	// keptOpen: the last reply went out but the item stayed open, for what came into it while the reply was out.
	keptOpen bool
	// unsaved: the last reply filed the item but its row could not be written; a repeat of it writes it again.
	unsaved bool
}

// repeated reports whether a reply with key went out last and still stands, so this one is a repeat (pressed on
// phone and watch, a timed-out request retried): the item filed as replied, or kept open by that reply. Without the
// second, a retry after new messages came in would go out again (#1828). Under b.mu, with r.mu held.
func (r *inboxReply) repeated(live *InboxItem, key string) bool {
	return r.clientID == key && (live.DoneBy == "replied" || r.keptOpen && live.open())
}

// inboxReplyReq is one reply: the item as it is in the inbox (live, under b.mu) and a copy taken for the reply, the
// text, the documents' status moves, and the item's replies (held for the whole reply).
type inboxReplyReq struct {
	id   string
	live *InboxItem
	item InboxItem
	text string
	// clientID: the app's id for the send (POST …/reply client_id); empty for one derived from the item and the text.
	clientID string
	changes  []DocChange
	once     *inboxReply
	// sendID names the send for the hub, which sends it once however often it is asked (InboxSent): a retry after
	// the answer was lost (the link dropping, the manager restarting) does not go out twice (replySendID).
	sendID string
}

// replySendID names one reply: the app's id for the send when it gave one, else the item, the text and the status
// moves (the same reply asked again is the same send). Not the message it answers: a retry after new messages came
// into the item (it stayed open, replyInboxFrom) would go out again (#1828), as the derived Feishu clientId is.
func replySendID(id, text, clientID string, changes []DocChange) string {
	if validClientID(clientID) {
		return namedClientID("send\n" + id + "\n" + strings.ToLower(clientID))
	}
	moves, _ := json.Marshal(changes)
	return namedClientID("send\n" + id + "\n" + text + "\n" + string(moves))
}

// replyInDoubt: the reply may have gone out though no answer said so: the link to the hub dropped while it was out,
// the time ran out, or the hub says a send under its id was cut off before (assistantwire.CodeAssistantInDoubt).
func replyInDoubt(err error) bool {
	var re *rpc.Error
	return errors.Is(err, rpc.ErrDropped) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &re) && re.Code == assistantwire.CodeAssistantInDoubt
}

// replyInboxFrom sends text as the user's reply to the item's source — in the Feishu chat, a document's comments, a
// Moments (company feed) post (the plugin that took Feishu over), a reply to the newest mail of the thread, a GitHub
// comment… (the hub sends it) — and files the item as done, unless something new came into it while the reply was
// out. clientID is the app's own id for the send (client_id): a retry of the same request is sent once; empty, one is
// derived from the item and the text. fromCard: it is "Send" on a message card (sendFromCard), which does not reply to an
// item filed since (errInboxItemDone). Checked with the copy for the reply, so an item filed after sendFromCard
// looked is not answered and keeps its mark (#4871).
func (s *Service) replyInboxFrom(ctx context.Context, id, text, clientID string, fromCard bool, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDo(ctx, id, text, clientID, fromCard, true, changes...)
}

// replyInboxDo is replyInboxFrom; file false keeps the item open (a confirmed suggestion's reply: the event waits for
// the other side), a repeat of the reply still being answered as sent.
func (s *Service) replyInboxDo(ctx context.Context, id, text, clientID string, fromCard, file bool, changes ...DocChange) (InboxItem, error) {
	return s.replyInboxDoMode(ctx, id, text, clientID, fromCard, file, false, changes...)
}

// replyInboxDoMode adds the direct /reply route's waiting mode without changing the shared send-once path used by
// handed-off suggestions. followDirect is true only for POST /api/v1/inbox/{id}/reply.
func (s *Service) replyInboxDoMode(ctx context.Context, id, text, clientID string, fromCard, file, followDirect bool, changes ...DocChange) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	if ok && fromCard && !it.open() {
		b.mu.Unlock()
		return InboxItem{}, errInboxItemDone
	}
	var c InboxItem
	if ok {
		c = *it
		if it.Lark != nil { // collection rewrites these fields while the reply is out
			l := *it.Lark
			c.Lark = &l
		}
		if it.Mail != nil {
			m := *it.Mail
			m.UIDs = append([]uint32(nil), it.Mail.UIDs...)
			c.Mail = &m
		}
		if it.Doc != nil {
			d := *it.Doc
			c.Doc = &d
		}
		if it.Moment != nil {
			m := *it.Moment
			c.Moment = &m
		}
		if it.Connector != nil {
			k := *it.Connector
			c.Connector = &k
		}
	}
	b.mu.Unlock()
	if !ok {
		return InboxItem{}, errNoInboxItem
	}
	// Phone and watch can both press send, and a timed-out request gets retried: one reply of an item at a time,
	// held until the item is filed (the check for "already sent" relies on it).
	r, _ := b.replies.LoadOrStore(id, &inboxReply{})
	once := r.(*inboxReply)
	once.mu.Lock()
	defer once.mu.Unlock()
	if fromCard {
		// Another reply of the item may have been out (the phone's, a repeat): filed while this one waited for it, the
		// item is not answered again from a card either (#5117).
		b.mu.Lock()
		open := it.open()
		b.mu.Unlock()
		if !open {
			return InboxItem{}, errInboxItemDone
		}
	}
	req := inboxReplyReq{id: id, live: it, item: c, text: text, clientID: clientID, changes: changes, once: once,
		sendID: replySendID(id, text, clientID, changes)}
	var already bool
	var err error
	switch {
	case c.Connector == nil && migratedThread(&c) != "": // a plugin took Feishu over (inbox_migrate.go)
		already, err = s.replyMigrated(ctx, req)
	case len(changes) > 0:
		return InboxItem{}, i18n.ErrEnglish("inbox-errors.document_only")
	case c.Connector != nil:
		already, err = s.replyConnectorItem(ctx, req)
	case c.Mail != nil:
		already, err = s.replyMail(ctx, req)
	default:
		return InboxItem{}, errCannotReply
	}
	if err != nil {
		return InboxItem{}, err
	}
	now := time.Now()
	if already {
		if once.unsaved { // the reply's item was not written: a repeat writes it (#4627)
			s.saveReplied(ctx, it, once)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		s.markCardsSent(ctx, id, now, text, true)
		return out, nil
	}
	var loc *time.Location
	if followDirect {
		loc = s.location(ctx)
	}
	b.mu.Lock()
	// The reply answers what the item held when it was sent. A move that came in meanwhile (collection runs while the
	// reply is out) is not answered: the item stays open with it, so it is not filed unseen (#1828). An item filed
	// meanwhile (read elsewhere) is filed as replied.
	kept := !file && it.open() || it.open() && it.Last != c.Last
	// A direct reply with a processing conversation waits for the other side instead of filing the matter. Old-App
	// card sends keep filing. If new messages arrived while the reply was out, the existing kept-open path is unchanged.
	if followDirect && !fromCard && file {
		resetInboxFollowChecks(it)
	}
	wait := followDirect && !fromCard && file && !kept && it.open() && it.Agent != nil && it.Agent.SessionID != ""
	kept = kept || wait
	once.keptOpen = kept
	if c.Mail != nil {
		once.clientID = text
	}
	b.noteSent(it, now)
	tell := false
	if wait {
		it.Suggestion = nil // the user's own reply supersedes a suggestion still waiting for confirmation
		it.Agent.SentOf = c.Last
		b.waitForOtherSide(it, defaultInboxFollow(now), ViaUser, now, loc, true)
		b.tell(it, inboxNote{Name: prompts.NoteSent, Text: text}, false, now)
		tell = true
	} else if !kept {
		it.done(now, "replied")
		tell = b.closeHandling(it, "replied", now) // its conversation stops
	}
	out := it.listed()
	b.mu.Unlock()
	if !kept || wait {
		s.saveReplied(ctx, it, once)
	} else if !file {
		s.saveInbox(ctx, []*InboxItem{it})
	}
	if tell {
		s.tellSoon(ctx, it)
	}
	// The item's cards in the assistant's conversations show it sent, wherever it was sent from.
	s.markCardsSent(ctx, id, now, text, true)
	return out, nil
}

// saveReplied writes the item after its reply, filed or waiting. The reply went out, so a failed write keeps the item's
// resulting state in memory (rolling it back would let a repeat send the reply again) and is remembered on the item's
// replies: a repeat writes it then, instead of the item returning to its pre-reply state after a restart. With once.mu held.
func (s *Service) saveReplied(ctx context.Context, it *InboxItem, once *inboxReply) {
	failed := s.saveInbox(ctx, []*InboxItem{it})
	once.unsaved = failed[it.ID]
	if once.unsaved {
		s.logf("inbox: %s was replied to but its resulting state could not be saved", it.ID)
	}
}

// replyMail answers the newest mail of the thread from the account that received it (the hub sends it).
func (s *Service) replyMail(ctx context.Context, r inboxReplyReq) (bool, error) {
	m := r.item.Mail
	if m == nil || len(m.UIDs) == 0 {
		return false, errCannotReply
	}
	b := &s.inbox
	b.mu.Lock()
	repeated := r.once.repeated(r.live, r.text)
	b.mu.Unlock()
	if repeated {
		return true, nil
	}
	var sent assistantwire.InboxSent
	err := s.appCall(ctx, AppInbox, inboxReplyMail, map[string]any{"account_id": m.AccountID, "uid": m.UIDs[len(m.UIDs)-1],
		"mailbox": m.Mailbox, "text": r.text, "send_id": r.sendID}, &sent)
	s.inboxSent(sent, r.sendID)
	// A hub "already" can be the first answer this manager got after an in-doubt send: this manager still writes the
	// resulting item state. Only the local repeat check above may skip that write.
	return false, err
}
