package extmgr

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
)

// The unified inbox (docs/assistant/inbox.md) is the assistant's (internal/assistant's inbox*.go; "the manager" in the
// comments below): the items and their state, triage, events, mutes, the AI double, translations and the
// /api/v1/inbox… routes, which the hub relays to the manager process. The sources are this code's, the extension
// manager in that same process: it reads them when a round is due — its poll, a connector's changed, a pull to refresh
// the assistant asks for (inbox.collect) — and hands what it read to the assistant in-process (inbox.collected through
// callAssistant); it answers what the inbox asks of the sources and of its other parts (app.call inbox.*:
// inboxAppCall, reached through the bus connection's localAppCall, never the hub), and tells the extensions and,
// through the hub's broadcast, the apps when items changed (inbox.changed, taken here by AssistantSaid). It keeps no
// inbox state of its own: while the assistant can't take a round (there is none yet, or it refused or failed the
// hand-over) the sources are still read on schedule and what they had is kept for it (merged, bounded, in memory:
// heldThingsMost, heldMailMost), so what leaves a source's window meanwhile (mail's newest 40, iMessage's newest 30) is
// not lost; what was kept goes with the first round it takes again, and a full round still owed at the next wake-up.

const (
	inboxPoll     = 2 * time.Minute // fallback when no Feishu event arrives
	inboxMailPoll = 3 * time.Minute // IMAP is slow; Gmail goes through a proxy
	// inboxRoundWait bounds handing a round over (the manager reads the new mail's text before it answers).
	inboxRoundWait = 5 * time.Minute
	// inboxMailParallel is how many mail accounts a full round reads at once: a slow server (up to the mail
	// package's 20 s per operation) no longer holds up the others, and the round (which holds the inbox lock the
	// Feishu chat rounds wait on) takes about the slowest account's time instead of their sum.
	inboxMailParallel = 4
)

// inboxDebounce is how long a kick waits so that a burst of Feishu events collects once (tests shorten it).
var inboxDebounce = 3 * time.Second

// InboxConfig tunes the inbox (manager.json "inbox").
type InboxConfig struct {
	// Rules is extra guidance for triage, in the user's words ("anything from my manager needs me", "alerts from
	// group X are noise"…), appended to the prompt.
	Rules string `json:"rules,omitempty"`
	// TriageAI (model 1) sorts and summarizes new items; JudgeAI (model 2) links a Needs you item to
	// one of the user's tasks.
	// Empty fields fall back to manager.json's "ai"; without them both use "ai".
	TriageAI *ai.Config `json:"triage_ai,omitempty"`
	JudgeAI  *ai.Config `json:"judge_ai,omitempty"`
	// Agent (model 3) works on the Needs you items (and those a policy hands it) and drafts the reply (the manager's
	// inbox_agent.go).
	Agent InboxAgentConfig `json:"agent,omitempty"`
	// ReplyPolicy optionally restricts which classified items may become owner reply drafts. It retains both the legacy
	// pointer fields and the upstream compiled categories; the assistant deliberately interprets only one schema.
	ReplyPolicy *assistantwire.InboxReplyPolicyConfig `json:"reply_policy,omitempty"`
	// Visibility keeps explicitly named chats, or all direct chats when enabled, out of AI handling and reads.
	Visibility *assistantwire.InboxVisibilityConfig `json:"visibility,omitempty"`
}

// InboxAgentConfig is manager.json's inbox.agent, handed to the manager as it is.
type InboxAgentConfig = assistantwire.InboxAgentConfig

// InboxReplyPolicyConfig is manager.json's inbox.reply_policy, handed to the manager as it is.
type InboxReplyPolicyConfig = assistantwire.InboxReplyPolicyConfig

// inboxSources is this code's part of the inbox: the wake-ups, one collection at a time, and what the manager
// reported.
type inboxSources struct {
	once    sync.Once
	kick    chan struct{}
	wide    atomic.Bool   // a kick asked for more than the Feishu chats (kickInbox)
	owed    atomic.Bool   // the manager missed a round (not taken): a full one is owed, run once it takes rounds again
	running chan struct{} // one collection at a time (held while the manager applies it; lock, unlock)
	told    atomic.Bool   // "the manager is not connected" was logged (until a round goes through again)
	// held is what was read and not handed over (the manager was away, or did not take it), merged, written under
	// running: it goes with the next round. Set means the last round was not taken (inboxLoop reads that without
	// running). epoch and seq stamp the rounds (InboxRound.Epoch, Seq).
	held  atomic.Pointer[assistantwire.InboxRound]
	epoch string
	seq   atomic.Uint64

	tellOnce sync.Once
	changed  chan assistantwire.InboxChanged // inbox.changed waiting for tellInboxChanged

	// The events plugins wrote lately, by append id (groupAppendOnce).
	appendMu    sync.Mutex
	appended    map[string]connector.InboxThing
	appendLocks sync.Map // append id → *sync.Mutex, while it is written
}

func (b *inboxSources) init() {
	b.once.Do(func() {
		b.kick = make(chan struct{}, 1)
		b.running = make(chan struct{}, 1)
		b.epoch = strconv.FormatInt(time.Now().UnixNano(), 36)
	})
}

// inboxRound is what one collection reads.
type inboxRound int

const (
	// roundChats follows a Feishu realtime event: the Feishu chats only. Document and Moments (company feed) notices
	// and the other connectors wait for the next poll; reading them on every event was most of the hub's Lark gateway
	// calls in a busy group (#674).
	roundChats inboxRound = iota
	roundPoll             // every inboxPoll and on a connector's kick: every source but the slow ones
	roundFull             // every inboxMailPoll, when the manager connects and on a refresh: mail too
)

func (r inboxRound) wire() string {
	switch r {
	case roundChats:
		return assistantwire.RoundChats
	case roundFull:
		return assistantwire.RoundFull
	}
	return assistantwire.RoundPoll
}

// kickInbox asks for a collection of every source soon (a connector's realtime event, its settings changed).
func (h *Manager) kickInbox() {
	h.inbox.init()
	h.inbox.wide.Store(true)
	h.inbox.signal()
}

// kickedRound is the round a kick asks for: the Feishu chats, unless something else asked for every source too.
func (b *inboxSources) kickedRound() inboxRound {
	if b.wide.Swap(false) {
		return roundPoll
	}
	return roundChats
}

// lock waits for the collection before to end, or for ctx: a caller that stops waiting (a refresh the manager gave up
// on) does not stay queued behind a round that is with the manager.
func (b *inboxSources) lock(ctx context.Context) error {
	select {
	case b.running <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *inboxSources) unlock() { <-b.running }

func (b *inboxSources) signal() {
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// inboxLoop is the inbox's wake-ups: every inboxPoll, a kick (debounced), mail every inboxMailPoll. The first round is
// a full one when the manager connects (inboxManagerConnected).
func (h *Manager) inboxLoop(ctx context.Context) {
	h.inbox.init()
	lastMail := time.Now()
	t := time.NewTicker(inboxPoll)
	defer t.Stop()
	for {
		round := roundPoll
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.inbox.kick:
			select { // let the burst finish
			case <-ctx.Done():
				return
			case <-time.After(inboxDebounce):
			}
			round = h.inbox.kickedRound()
		}
		if h.isMember() { // the center keeps the group's inbox; this one's stays as it was (mesh_group.go)
			continue
		}
		// A round the manager missed is made up with a full one once it takes rounds again (nothing is held), not while
		// it refuses them or the hand-over fails (its store does not answer…): a kick or a poll reads what it reads and
		// mail keeps its own pace then.
		if time.Since(lastMail) >= inboxMailPoll || h.inbox.owed.Load() && h.inbox.held.Load() == nil {
			lastMail = time.Now()
			round = roundFull
		}
		_ = h.collectInbox(ctx, round)
	}
}

// inboxManagerConnected: Run has started this code (the assistant runs beside it in this process, started on its own):
// a full round goes to the assistant now, the first since the process started, and GitHub's muted repositories kept in
// its options move to its mutes.
func (h *Manager) inboxManagerConnected() {
	h.inbox.init()
	h.inbox.owed.Store(true)
	h.inbox.wide.Store(true)
	h.inbox.signal()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h.githubMutes(ctx)
}

// collectInbox reads the sources of one round and hands them, with what was kept from before, to the manager
// (inbox.collected), which applies them before it answers. Not connected, or not taken, what was read is kept for the
// next round, and a full round is owed once one is taken again (inboxLoop).
func (h *Manager) collectInbox(ctx context.Context, round inboxRound) error {
	h.inbox.init()
	if err := h.inbox.lock(ctx); err != nil {
		return err
	}
	defer h.inbox.unlock()
	r := h.readInbox(ctx, round)
	if held := h.inbox.held.Load(); held != nil {
		r = mergeRounds(*held, r)
	}
	if !h.assistantUp() {
		h.inbox.held.Store(&r)
		h.inbox.owed.Store(true)
		if !h.inbox.told.Swap(true) {
			h.logf("inbox: the manager is not connected: what the sources have is kept for it")
		}
		return errAssistantOffline
	}
	r.Epoch, r.Seq = h.inbox.epoch, h.inbox.seq.Add(1)
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), inboxRoundWait)
	defer cancel()
	if _, err := buswire.Call(cctx, assistantCaller(h.callAssistant), assistantwire.InboxCollectedMethod, r); err != nil {
		h.inbox.held.Store(&r)
		h.inbox.owed.Store(true) // read again, whole, when it is back
		h.logf("inbox: hand the round to the manager: %v", err)
		return err
	}
	h.inbox.held.Store(nil)
	h.inbox.told.Store(false)
	if r.Round == assistantwire.RoundFull { // what was kept went with it
		h.inbox.owed.Store(false)
	}
	return nil
}

// What is kept for the manager while it is away, at most: things of one source, messages of one mailbox (the
// newest).
const (
	heldThingsMost = 2000
	heldMailMost   = 500
)

// mergeRounds is old with the newer round n over it: what n read replaces what old had of the same thing (a thread,
// an event, a batch, a mail), what only old has stays (it left the source's window meanwhile) within heldThingsMost
// and heldMailMost; a source n read as off, or one this code no longer has, keeps nothing. The mail accounts that
// could not be read are the newest mail round's.
func mergeRounds(old, n assistantwire.InboxRound) assistantwire.InboxRound {
	out := n
	if old.Round == assistantwire.RoundFull || n.Round == assistantwire.RoundFull {
		out.Round = assistantwire.RoundFull
	} else if old.Round == assistantwire.RoundPoll {
		out.Round = assistantwire.RoundPoll
	}
	out.Sources = slices.Clone(n.Sources)
	at := map[string]int{}
	for i, s := range out.Sources {
		at[s.ID] = i
	}
	for _, o := range old.Sources {
		i, ok := at[o.ID]
		switch {
		case !ok:
			if _, have := n.Names[o.ID]; have {
				out.Sources = append(out.Sources, o) // not read this time: what it had
			}
		case out.Sources[i].ReadError != "":
			o.ReadError = out.Sources[i].ReadError // not read this time: what it had, and that it failed
			out.Sources[i] = o
		case !out.Sources[i].Off:
			out.Sources[i].Things = mergeHeld(o.Things, out.Sources[i].Things, heldThingsMost, func(t connector.InboxThing) string {
				key := t.Thread + "\x00" + t.ID
				if t.Group != nil {
					key += "\x00" + t.Group.Batch
				}
				return key
			})
		}
	}
	switch {
	case n.Mail == nil:
		out.Mail = old.Mail
	case old.Mail != nil:
		m := &assistantwire.InboxMailRead{Accounts: slices.Clone(n.Mail.Accounts), Failed: n.Mail.Failed}
		got := map[string]int{}
		for i, a := range m.Accounts {
			got[a.ID] = i
		}
		for _, a := range old.Mail.Accounts {
			i, ok := got[a.ID]
			if !ok {
				m.Accounts = append(m.Accounts, a) // not read this time
				continue
			}
			cur := &m.Accounts[i]
			if cur.InboxUnavailable && !a.InboxUnavailable {
				cur.InboxUnavailable, cur.UIDValidity, cur.Messages = false, a.UIDValidity, a.Messages
			} else {
				cur.UIDValidity, cur.Messages = mergeMailPage(a.UIDValidity, cur.UIDValidity, a.Messages, cur.Messages)
			}
			cur.Folders = slices.Clone(cur.Folders)
			at := map[string]int{}
			for j, f := range cur.Folders {
				at[f.Name] = j
			}
			for _, f := range a.Folders {
				if j, ok := at[f.Name]; ok {
					dst := &cur.Folders[j]
					dst.UIDValidity, dst.Messages = mergeMailPage(f.UIDValidity, dst.UIDValidity, f.Messages, dst.Messages)
				} else {
					cur.Folders = append(cur.Folders, f)
				}
			}
		}
		out.Mail = m
	}
	return out
}

// The same UID may be a different mail after renumbering. Do not replay a held page of the old validity.
func mergeMailPage(oldValidity, validity uint32, old, n []assistantwire.InboxMailSummary) (uint32, []assistantwire.InboxMailSummary) {
	if validity == 0 {
		validity = oldValidity
	}
	if oldValidity != 0 && oldValidity != validity {
		return validity, n
	}
	return validity, mergeHeld(old, n, heldMailMost, func(m assistantwire.InboxMailSummary) string {
		return strconv.FormatUint(uint64(m.UID), 10)
	})
}

// mergeHeld is what only old has, then n, the newest most of them.
func mergeHeld[T any](old, n []T, most int, key func(T) string) []T {
	have := map[string]bool{}
	for _, v := range n {
		have[key(v)] = true
	}
	out := make([]T, 0, len(old)+len(n))
	for _, v := range old {
		if !have[key(v)] {
			out = append(out, v)
		}
	}
	out = append(out, n...)
	if len(out) > most {
		out = out[len(out)-most:]
	}
	return out
}
