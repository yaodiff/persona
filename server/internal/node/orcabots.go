package node

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// Orca's bots in the bridge (docs/integrations/orca.md "Bots found on a computer", #16891). Every orcaBotsPoll
// the bridge reads the computer's bots (bots.roster), each one's chats (bots.chats) and its items waiting on the owner
// (bots.items.list), all on Orca's local socket. Two things come of it:
//   - an item's open question becomes one question card on the session of the chat it was raised in (the bot's open
//     Home Chat when that chat is not listed or listed closed), deduplicated by the item and the question, taken down
//     when the item no longer waits on that question, and answered back to Orca as the owner
//     (bots.items.owner.comment); an answer Orca did not take goes on the phone again, as the prompts' do (ask);
//   - the sessions a bot runs on its own in its home folder (Look around, Tidy-up, and its earlier Home Chats) fold under
//     its open Home Chat as side sessions (parent_agent_id, linkOrcaBotRuns). The bot's other open chats stay rows of
//     their own.

// orcaBotsPoll is how often the bridge reads the bots: an item is answered by a person, so seconds do not matter.
var orcaBotsPoll = 20 * time.Second

// orcaBotHomes is what the bridge last learned of the computer's bots for the session list: each bot's home folder and
// the Persona session of its open Home Chat, and the Persona sessions of every bot's open chats, which never fold.
type orcaBotHomes struct {
	homes []orcaBotHome
	keep  map[string]bool
}

// orcaBotHome is one bot's home folder (cleaned) and its Home Chat's Persona session.
type orcaBotHome struct {
	folder  string
	session string
}

// same: h and o fold the same sessions.
func (h *orcaBotHomes) same(o *orcaBotHomes) bool {
	if h == nil || o == nil {
		return h == o
	}
	if !slices.Equal(h.homes, o.homes) || len(h.keep) != len(o.keep) {
		return false
	}
	for id := range h.keep {
		if !o.keep[id] {
			return false
		}
	}
	return true
}

// linkOrcaBotRuns folds the sessions a bot ran in its home folder (or below it) under that bot's open Home Chat as side
// sessions (parent_agent_id): each keeps its own state, unread and sends, and the manager folds it under the Home
// Chat's row. The Home Chat must be a row of list too, and a session that already names a parent, a subagent, an
// assistant conversation and one of a bot's open chats are left alone.
func linkOrcaBotRuns(list []proto.Session, homes *orcaBotHomes) {
	if homes == nil || len(homes.homes) == 0 || len(list) == 0 {
		return
	}
	listed := make(map[string]bool, len(list))
	for i := range list {
		listed[list[i].SessionID] = true
	}
	for i := range list {
		s := &list[i]
		if s.ParentAgentID() != "" || s.IsSubagent || s.Assistant || homes.keep[s.SessionID] {
			continue
		}
		for _, h := range homes.homes {
			if h.session == s.SessionID || !listed[h.session] {
				continue
			}
			if underFolder(s.Cwd, h.folder) || underFolder(s.WorkspaceCwd, h.folder) {
				s.SetParentAgentID(h.session)
				break
			}
		}
	}
}

// underFolder: dir is folder or under it.
func underFolder(dir, folder string) bool {
	if dir == "" || folder == "" {
		return false
	}
	dir = filepath.Clean(dir)
	return dir == folder || strings.HasPrefix(dir, folder+string(filepath.Separator))
}

// orcaItemAsk is one item question put on the phone.
type orcaItemAsk struct {
	bot     string // the bot's Orca id: its cards are only taken down after its items were read
	cancel  context.CancelFunc
	retryAt time.Time // the node could not take it, or Orca did not take its answer: offer it again then
	attempt int       // how many times it was put on the phone before; each time under its own card id
	done    bool      // answered, handed back or expired: not offered again while the item waits on this question
}

// orcaBotsState is the bridge's part for the bots.
type orcaBotsState struct {
	mu      sync.Mutex
	at      time.Time // when the bots were last read
	running bool      // a read is under way
	asks    map[string]*orcaItemAsk
	homes   *orcaBotHomes
	logged  bool // a failed read was logged (one line until a read works again)
}

// botsTick starts a read of the bots when orcaBotsPoll has passed since the last one and none runs: off the refresh
// goroutine, so a slow Orca never holds the 1.5 s session list. chats are the chats open in Orca (orcaOpenChats),
// orcaOf and closed the refresh's (never written once it has handed them over).
func (b *orcaBridge) botsTick(ctx context.Context, chats []orcaChat, orcaOf map[string]string, closed map[string]bool) {
	st := &b.bots
	st.mu.Lock()
	if st.running || time.Since(st.at) < orcaBotsPoll {
		st.mu.Unlock()
		return
	}
	st.running, st.at = true, time.Now()
	st.mu.Unlock()
	go func() {
		defer func() {
			st.mu.Lock()
			st.running = false
			st.mu.Unlock()
		}()
		b.readBots(ctx, chats, orcaOf, closed)
	}()
}

// readBots reads the bots once and brings the cards and the fold in step with them.
func (b *orcaBridge) readBots(ctx context.Context, chats []orcaChat, orcaOf map[string]string, closed map[string]bool) {
	bots, err := b.c.Bots(ctx)
	switch {
	case errors.Is(err, orcart.ErrNotRunning), orcart.IsCode(err, "method_not_found"), orcart.IsCode(err, "method_not_supported"):
		b.publishBotHomes(nil)
		b.settleItemCards(nil, nil)
		return
	case err != nil:
		b.noteBotsFailure(err)
		return // the cards and the fold stay as they are
	}
	b.bots.mu.Lock()
	b.bots.logged = false
	b.bots.mu.Unlock()
	personaOf := make(map[string]string, len(orcaOf)) // Orca session → its Persona session, chats the bridge lists
	for persona, orca := range orcaOf {
		personaOf[orca] = persona
	}
	folderOf := map[string]string{} // Orca session → the local worktree it runs in (a chat with no turn yet too)
	for _, c := range chats {
		if c.tree.Path != "" {
			folderOf[c.orca] = filepath.Clean(c.tree.Path)
		}
	}
	homes := &orcaBotHomes{keep: map[string]bool{}}
	live := map[string]bool{} // the cards wanted now
	read := map[string]bool{} // the bots whose items were read
	for i := range bots {
		bot := &bots[i]
		if open, err := b.c.BotChats(ctx, bot); err == nil {
			for _, o := range open {
				if p := personaOf[string(o)]; p != "" {
					homes.keep[p] = true
				}
			}
			home := string(bot.HomeSessionID)
			if p, folder := personaOf[home], folderOf[home]; p != "" && folder != "" {
				homes.homes = append(homes.homes, orcaBotHome{folder: folder, session: p})
			}
		}
		items, err := b.c.BotItems(ctx, bot.BotID, proto.OrcaItemNeedsYou)
		if err != nil {
			continue
		}
		read[string(bot.BotID)] = true
		for _, it := range items.Items {
			if it.Status != proto.OrcaItemNeedsYou || it.Question == nil || it.Question.EventID == "" {
				continue
			}
			persona := personaOf[string(it.ChatSessionID)]
			if home := personaOf[string(bot.HomeSessionID)]; home != "" && (persona == "" || closed[persona]) {
				persona = home // raised in a chat Persona does not list, or lists closed: the bot's open Home Chat
			}
			if persona == "" {
				continue // no home in Persona's list: Orca's own panel holds it
			}
			key := orcaItemKey(it)
			live[key] = true
			b.offerItem(ctx, key, string(bot.BotID), persona, it)
		}
	}
	slices.SortFunc(homes.homes, func(a, b orcaBotHome) int { return strings.Compare(a.folder, b.folder) })
	b.publishBotHomes(homes)
	b.settleItemCards(live, read)
}

// noteBotsFailure logs a failed read once until one works again.
func (b *orcaBridge) noteBotsFailure(err error) {
	b.bots.mu.Lock()
	defer b.bots.mu.Unlock()
	if !b.bots.logged {
		b.bots.logged = true
		b.logf("orca bridge: bots: %v", err)
	}
}

// publishBotHomes keeps homes for the session list and has the list pushed again when the fold changed.
func (b *orcaBridge) publishBotHomes(homes *orcaBotHomes) {
	if homes != nil && len(homes.homes) == 0 {
		homes = nil
	}
	b.bots.mu.Lock()
	changed := !b.bots.homes.same(homes)
	b.bots.homes = homes
	b.bots.mu.Unlock()
	if changed && b.onBotHomes != nil {
		b.onBotHomes(homes)
	}
}

// orcaItemKey names one question of one item: a new question on the same item is another card.
func orcaItemKey(it proto.OrcaBotItem) string {
	return string(it.ItemID) + "/" + string(it.Question.EventID)
}

// settleItemCards takes down the cards of questions no longer waiting: of a bot whose items were read (read; nil: Orca
// is gone, all of them) and not among live.
func (b *orcaBridge) settleItemCards(live, read map[string]bool) {
	b.bots.mu.Lock()
	defer b.bots.mu.Unlock()
	for key, a := range b.bots.asks {
		if live[key] || read != nil && !read[a.bot] {
			continue
		}
		if a.cancel != nil {
			a.cancel() // the goroutine takes the card off the phone
		}
		delete(b.bots.asks, key)
	}
}

// offerItem puts one item question on the phone unless it is there already, or was answered, handed back or expired.
// An answer Orca did not take (it refused it, or it is not known whether it took it) is never dropped: while the item
// still waits on the question, the next read puts it on the phone again as a new card.
func (b *orcaBridge) offerItem(ctx context.Context, key, bot, persona string, it proto.OrcaBotItem) {
	b.bots.mu.Lock()
	if b.bots.asks == nil {
		b.bots.asks = map[string]*orcaItemAsk{}
	}
	a := b.bots.asks[key]
	if a != nil && (a.done || a.retryAt.IsZero() || time.Now().Before(a.retryAt)) {
		b.bots.mu.Unlock()
		return
	}
	attempt := 0
	if a != nil {
		attempt = a.attempt
	}
	ix := orcaItemInteraction(persona, it)
	ix.ID = orcaItemCardID(key, attempt)
	askCtx, cancel := context.WithCancel(ctx)
	mine := &orcaItemAsk{bot: bot, cancel: cancel, attempt: attempt}
	b.bots.asks[key] = mine
	b.bots.mu.Unlock()
	go func() {
		defer cancel()
		var res proto.Resolution
		err := b.peer.Call(askCtx, "interaction.create", map[string]any{"interaction": ix}, &res)
		if askCtx.Err() != nil {
			if ctx.Err() == nil {
				_ = b.peer.Notify("interaction.cancel", map[string]any{"id": ix.ID})
			}
			return
		}
		if err != nil {
			b.retryItem(key, mine, attempt, time.Now().Add(orcaAskRetry))
			return
		}
		text := ""
		if !res.Dismiss { // handed back, or nobody answered in time: Orca's own panel answers it
			text = orcaItemAnswer(ix, it, res)
		}
		if text == "" {
			b.retryItem(key, mine, -1, time.Time{})
			return
		}
		// The card's id is the request's: an answer the manager hands again (the bridge asked again after a restart)
		// lands once in Orca.
		if err := b.c.AnswerBotItem(ctx, it.ItemID, it.Question.EventID, text, ix.ID); err != nil {
			if ctx.Err() == nil {
				b.logf("orca bridge: answering item %s: %v (offered again while it waits on the question)", it.ItemID, err)
			}
			b.retryItem(key, mine, attempt+1, time.Now())
			return
		}
		b.retryItem(key, mine, -1, time.Time{})
	}()
}

// retryItem has the item question's card a offered again at at, as its attempt-th time on the phone; attempt -1: it is
// done (answered, handed back or expired), not offered again while the item waits on the question.
func (b *orcaBridge) retryItem(key string, a *orcaItemAsk, attempt int, at time.Time) {
	b.bots.mu.Lock()
	defer b.bots.mu.Unlock()
	if b.bots.asks[key] != a {
		return // taken down meanwhile (another card may stand for the question now)
	}
	if attempt < 0 {
		a.done = true
		return
	}
	a.attempt, a.retryAt = attempt, at
}

// orcaItemCardID is the card id of an item question's attempt-th time on the phone, by orcaCardID's rule (the first
// one's is the question's alone, the same after a node restart; a later one's its own) under its own prefix.
func orcaItemCardID(key string, attempt int) string {
	return "orcaitem_" + strings.TrimPrefix(orcaCardID(key, attempt), "orca_")
}

// orcaItemInteraction is the question card of an item: one question per decision of the bot's ask, with its quick
// replies as options, the item's title and the ask's context (Markdown, the bot's own words) above the first; an ask
// without decisions is one question to answer in words.
func orcaItemInteraction(persona string, it proto.OrcaBotItem) *proto.Interaction {
	q := it.Question
	ix := &proto.Interaction{SessionID: persona, Kind: proto.IxQuestion, Format: "markdown", Summary: it.Title}
	lead := orcaItemLead(it)
	if len(q.Decisions) == 0 {
		ix.Questions = []proto.Question{{Question: joinParagraphs(lead, strings.TrimSpace(q.Text)),
			Options: []proto.QuestionOption{}}}
		if strings.TrimSpace(q.Text) == "" {
			ix.Questions[0].Question = joinParagraphs(lead, q.Gist)
		}
		return ix
	}
	for i, d := range q.Decisions {
		pq := proto.Question{Question: d.Question, MultiSelect: d.Multi, Options: []proto.QuestionOption{}}
		if i == 0 {
			pq.Question = joinParagraphs(lead, d.Question)
		}
		for _, r := range d.Replies {
			pq.Options = append(pq.Options, proto.QuestionOption{Label: r.Label, Description: r.Detail})
			if r.Recommended && pq.Recommend == "" {
				pq.Recommend = r.Label
			}
		}
		ix.Questions = append(ix.Questions, pq)
	}
	return ix
}

// orcaItemLead is what goes above an item's first question: its title in bold, then the ask's context (the question's
// text before its first decision; its gist when the decisions cannot be found in it).
func orcaItemLead(it proto.OrcaBotItem) string {
	q := it.Question
	title := ""
	if t := strings.TrimSpace(it.Title); t != "" {
		title = "**" + t + "**"
	}
	if len(q.Decisions) == 0 {
		return title
	}
	context := strings.TrimSpace(q.Gist)
	text := q.Text
	if i := strings.LastIndex(text, q.Decisions[0].Question); i > 0 {
		before := strings.TrimSpace(text[:i])
		before = strings.TrimSpace(strings.TrimSuffix(before, "1.")) // several decisions are numbered
		if before != "" {
			context = before
		}
	}
	return joinParagraphs(title, context)
}

// joinParagraphs joins the parts with words in them, a blank line between.
func joinParagraphs(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}

// orcaItemAnswer is what answering the card posts to the item, in Orca's own format: a single decision's answer as it
// came (a reply's label, several labels joined by ", ", or words of the owner's own); with several decisions, one line
// each, "<the question's first line> → <answer>", for the decisions answered. Typed words with no decision answered
// go as they are.
func orcaItemAnswer(ix *proto.Interaction, it proto.OrcaBotItem, res proto.Resolution) string {
	q := it.Question
	if len(q.Decisions) <= 1 {
		if len(ix.Questions) > 0 {
			if a := strings.TrimSpace(res.Answers[ix.Questions[0].Question]); a != "" {
				return a
			}
		}
		return strings.TrimSpace(res.Text)
	}
	var lines []string
	for i, d := range q.Decisions {
		if i >= len(ix.Questions) {
			break
		}
		if a := strings.TrimSpace(res.Answers[ix.Questions[i].Question]); a != "" {
			lines = append(lines, orcaQuestionLine(d.Question)+" → "+a)
		}
	}
	if len(lines) == 0 {
		return strings.TrimSpace(res.Text)
	}
	return strings.Join(lines, "\n")
}

var (
	orcaLineMarkRe   = lazyre.New(`^(#{1,6}|>|[-*+]|\d+[.)])\s+`)
	orcaLineLinkRe   = lazyre.New(`!?\[([^\]]*)\]\([^)]*\)`)
	orcaLineStrongRe = lazyre.New(`(\*\*|__)(.+?)(\*\*|__)`)
	orcaLineCodeRe   = lazyre.New("`([^`]*)`")
)

// orcaQuestionLine is a question's first line with words, as plain words: how Orca names a decision in an answer that
// answers several ("<line> → <answer>").
func orcaQuestionLine(question string) string {
	for _, line := range strings.Split(question, "\n") {
		line = strings.TrimSpace(line)
		line = orcaLineMarkRe.ReplaceAllString(line, "")
		line = orcaLineLinkRe.ReplaceAllString(line, "$1")
		line = orcaLineStrongRe.ReplaceAllString(line, "$2")
		line = orcaLineCodeRe.ReplaceAllString(line, "$1")
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// setOrcaBotHomes keeps what folds under the bots' Home Chats and has the session list pushed again.
func (rh *RuntimeHost) setOrcaBotHomes(homes *orcaBotHomes) {
	if old := rh.orcaBots.Swap(homes); old.same(homes) {
		return
	}
	if n := rh.current(); n != nil {
		n.notifySessionsSoon()
	}
}

// orcaBotHomes is what folds under the bots' Home Chats on this computer now; nil without an Orca bridge.
func (n *Node) orcaBotHomes() *orcaBotHomes {
	if n.runtimeHost == nil {
		return nil
	}
	return n.runtimeHost.orcaBots.Load()
}
