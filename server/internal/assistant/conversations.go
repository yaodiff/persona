package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant manages its own conversations (docs/assistant/assistant.md "Managing its own conversations", the user's ask on 2026-11-01):
// list, read, rename, archive, start one and message another. Archiving only hides a conversation (the app's "Archived"
// lists it, nothing is deleted); the set is the assistant's (settings assistant.archived), so Claude and Codex
// conversations are archived alike. Messages between conversations are queued by the assistant while the receiver
// runs and carry where they come from; a chain of them is cut after a few hops and a few per pair in ten minutes, so
// two conversations can't keep each other going. A hop is a request: its answer adds none (hopLocked, #7391).
//
// The main conversation is the owner's dispatch center (#10099): a request from it is never refused for its hop, it
// goes at the last hop (the receiver may answer, not go further), and what it sends to a leader conversation counts
// only per pair, not against its per-sender cap. A scheduled run that wakes a conversation starts a new chain, at most
// once per conversation per relayWindow (scheduledWake). conversation_new has its own budget, and a start that fails
// gives its place back.

const (
	archivedKey = "assistant.archived"
	relayKey    = "assistant.relay"

	relayMaxHops    = 3                // hops from the user's own message; an answer adds none
	relayPairMax    = 20               // messages from one conversation to another …
	relayFromMax    = 30               // … and from one conversation to any (a cost cap: past it they queue, #10400) …
	relayWindow     = 10 * time.Minute // … in this long
	relayHopMemory  = time.Hour        // a hop count older than this no longer counts (the user has moved on)
	relayNewMax     = 3                // conversations one conversation opens in relayWindow …
	relayNewMainMax = 10               // … and the main conversation, the owner's dispatch center
	// Loops are counted as round trips (#10400): two conversations that turn back to each other more than
	// relayRoundsMax times in relayRoundWindow are cut, until the owner speaks in one of them.
	relayRoundsMax   = 6
	relayRoundWindow = time.Hour
	relayQueueMax    = 5     // messages from one conversation to another waiting for the rate; past it a send is refused
	relayNewKey      = "new" // the (from, relayNewKey) pair counts conversation_new
)

// relayRetryGap is how long a conversation that did not take a message (relayBook.failedAt) waits for the next try.
var relayRetryGap = 30 * time.Second

// relayPlaceholderGoneRetries bounds how many "is gone" answers a queued relay tolerates while the receiver is still
// projected as an offline LogicalPlaceholder (for example right after a restart before an older node reports its
// runtime sessions). Once exhausted, the message leaves the queue and its sender is notified.
var relayPlaceholderGoneRetries = 3

type convArchived struct {
	MachineID string    `json:"machine_id"`
	At        time.Time `json:"at"`
	By        string    `json:"by"` // user | assistant
}

type archiveBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*convArchived // by session id
}

// archivedLocked reads the set once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) archivedLocked(ctx context.Context) bool {
	b := &s.archived
	if b.loaded {
		return true
	}
	m := map[string]*convArchived{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, archivedKey, &m); err != nil {
			s.logf("load archived conversations: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*convArchived{}
	}
	b.m, b.loaded = m, true
	return true
}

// archivedSet is the archived conversations' machines by id; false while the set could not be read.
func (s *Service) archivedSet() (map[string]string, bool) {
	b := &s.archived
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.archivedLocked(context.Background()) {
		return nil, false
	}
	out := make(map[string]string, len(b.m))
	for id, a := range b.m {
		out[id] = a.MachineID
	}
	return out, true
}

func (s *Service) isArchived(id string) bool {
	set, _ := s.archivedSet()
	_, ok := set[id]
	return ok
}

var errArchiveMain = i18n.Err("assistant-conversations.archive_main")

// errArchiveBackground: the background supervisor is built in and stays listed (supervise_background.go).
var errArchiveBackground = i18n.Err("assistant-conversations.archive_background_supervisor")

// setArchived archives (on) or brings back a conversation; by is who did it. It reports whether anything changed.
func (s *Service) setArchived(ctx context.Context, machine, id string, on bool, by string) (bool, error) {
	if err := s.conversationAvailable(ctx, machine, id); err != nil {
		return false, err
	}
	if on && s.settings(ctx).Main == id {
		return false, errArchiveMain
	}
	if on && s.isBackground(ctx, id) {
		return false, errArchiveBackground
	}
	st := s.store()
	if st == nil {
		return false, errors.New("no database")
	}
	b := &s.archived
	b.mu.Lock()
	if !s.archivedLocked(ctx) {
		b.mu.Unlock()
		return false, errors.New("archived conversations could not be read")
	}
	_, was := b.m[id]
	if was == on {
		b.mu.Unlock()
		return false, nil
	}
	if on {
		b.m[id] = &convArchived{MachineID: machine, At: time.Now().UTC(), By: by}
	} else {
		delete(b.m, id)
	}
	err := st.PutSetting(ctx, archivedKey, b.m)
	if err != nil { // keep memory as the database has it
		if on {
			delete(b.m, id)
		} else {
			b.m[id] = &convArchived{MachineID: machine, At: time.Now().UTC(), By: by}
		}
	}
	b.mu.Unlock()
	if err != nil {
		return false, err
	}
	if on {
		_ = s.call(ctx, machine, "assistant.cache_clear", map[string]any{"session_id": id}, nil)
		s.handOverArchivedSide(ctx, id)
	}
	s.publish(ctx) // the apps get the list with the flag now
	return true, nil
}

// unarchiveOnSend brings an archived conversation back when something is sent to it (the user from the app,
// another conversation): what happens in it should be in the list.
func (s *Service) unarchiveOnSend(machine, id string) bool {
	if !s.isArchived(id) {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	changed, err := s.setArchived(ctx, machine, id, false, titleByUser)
	if err != nil {
		s.logf("unarchive %s: %v", short(id), err)
	}
	return changed
}

// archiveRoutes: POST /api/v1/assistant/conversations/{id}/archive and …/unarchive.
func (s *Service) archiveRoutes() {
	for _, op := range []struct {
		path string
		on   bool
	}{{"archive", true}, {"unarchive", false}} {
		s.handle("POST /api/v1/assistant/conversations/{id}/"+op.path, func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if !validSessionID(id) {
				apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
				return
			}
			machine := s.settings(r.Context()).MachineID
			if _, err := s.setArchived(r.Context(), machine, id, op.on, titleByUser); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, errArchiveMain) || errors.Is(err, errArchiveBackground) {
					status = http.StatusBadRequest
				}
				apierr.Write(w, status, err)
				return
			}
			apierr.WriteJSON(w, map[string]any{"ok": true, "archived": op.on})
		})
	}
}

var sessionIDRe = lazyre.New(`^[A-Za-z0-9._-]{1,128}$`)
var legacySuperviseEntryIDRe = lazyre.New(`(?m)^\d+\.\s+任务「[^\n]*?」（(todo_[A-Za-z0-9]+)(，链接 [^\n]+)?）：`) // i18n-ignore: pre-signals rendered entry header, not display copy

func validSessionID(id string) bool { return sessionIDRe.MatchString(id) }

// --- messages between conversations ---

type relayMsg struct {
	// OnceID preserves a relay's delivery identity when its queued text is refreshed
	// (supervision digest refresh or a consumed detached continuation).
	OnceID string `json:"once_id,omitempty"`
	// Signals is the structured JSON content of a queued supervision message. It stays comparable with the relay and lets
	// /stop remove one task and regather the others after restart; older messages without it use their embedded task ids.
	Signals   string    `json:"signals,omitempty"`
	From      string    `json:"from"`
	FromTitle string    `json:"from_title"`
	Tool      string    `json:"tool"`
	Text      string    `json:"text"`
	Hop       int       `json:"hop"`
	At        time.Time `json:"at"`
	// Withdrawn marks a supervision message whose remaining text must not be sent or retried. A send already in flight
	// still finishes, but its failed queue head and every later restart drain drop it instead (#5732).
	Withdrawn bool `json:"withdrawn,omitempty"`
	// DoubtedAt is when a supervision send whose answer was lost began. It stays with the queued head across restart
	// until that head is removed, so a late-visible original line can still acknowledge its remaining signals (#5732).
	DoubtedAt time.Time `json:"doubted_at,omitzero"`
	// State is a task notice's state (quiet.go: done | needs | interrupted | failed) or an extension event's severity
	// (extension_event.go: info | warning | critical); "" for other relays.
	State string `json:"state,omitempty"`
	// Key and Title are an extension event's key and title (extension_event.go; From is the extension's id and FromTitle
	// its name); "" for other relays.
	Key   string `json:"key,omitempty"`
	Title string `json:"title,omitempty"`
	// Card identifies the card behind a quiet task's needs notice or a standalone permission relay.
	Card string `json:"card,omitempty"`
	// Internal marks a message meant only for the assistant, such as a card-ledger report or a card routed away
	// from the owner. queuedRelays leaves it out of the queue shown above the composer; delivery is unchanged. A
	// version that does not know it drops it on its next save: the relay then shows above the composer until it is
	// consumed, as an Urgent message ranks as any other.
	Internal bool `json:"internal,omitempty"`
	// Urgent: a supervision message with a signal that waits on a decision (relay_presence.go). A version that does
	// not know it drops it on its next save: the message then ranks and waits as any other supervision message.
	Urgent bool `json:"urgent,omitempty"`
	// Interject: this supervision message contains a validated decision_reply for a pending round; the node may bring
	// it into a running assistant turn and move foreground work to the background. It is never persisted in the relay queue.
	Interject bool `json:"interject,omitempty"`
	// QueuedAt and Depth are when it was first queued and how long the queue was with it then (the queue wait,
	// availability.go): kept by a message that takes its place (queuedAs).
	QueuedAt time.Time `json:"queued_at,omitzero"`
	Depth    int       `json:"depth,omitempty"`
	// DecisionAt is when it began to wait on a decision, where that is later than QueuedAt: it took the place of a
	// queued message that waited on none (replacing, relay_presence.go).
	DecisionAt time.Time `json:"decision_at,omitzero"`
	// Owner: what the owner themselves just did, their answer on the card of a question asked without waiting
	// (ask_later.go). It is not held for their presence (holdLocked): it is what they are waiting to see taken up. A
	// version that does not know it drops it on its next save: the answer is then held as any late answer is.
	Owner bool `json:"owner,omitempty"`
	// Private renews Just me when the message is actually dispatched, including
	// a result that waited in the durable queue or across a manager restart.
	Private bool `json:"private,omitempty"`
	// Continuation marks a detached approval's one-time retry instruction. Its
	// grant is checked again at the final drain, not only when it was queued.
	Continuation bool `json:"continuation,omitempty"`
	// ContinuationDigest binds that instruction to the exact journal occurrence,
	// not to whichever grant later reuses the interaction ID.
	ContinuationDigest string `json:"continuation_digest,omitempty"`
	// Answer: it answers a request the receiver sent (hopLocked, #7391), so it starts no exchange of its own: what
	// the receiver sends next costs a hop.
	Answer bool `json:"answer,omitempty"`
	// Due: past the relay rate it waits for the window (relayAdmit, #10400) and goes in no sooner than this, once.
	Due time.Time `json:"due,omitzero"`
	// TriedAt is the start of the earliest try that may have gone in. Save it before sending from the queue.
	// Looks, LookAfter and ConfirmFails keep the transcript check budget, schedule and confirmation-failure cycles
	// across manager restarts.
	TriedAt      time.Time `json:"tried_at,omitzero"`
	Looks        int       `json:"looks,omitempty"`
	LookAfter    time.Time `json:"look_after,omitzero"`
	ConfirmFails int       `json:"confirm_fails,omitempty"`
	// Quote: the owner's message the sender carried (conversation_send / conversation_new quote), as the hub verified
	// it (relay_quote.go, #3822). A version that does not know it drops it on its next save: the message then goes
	// without the owner's words, as before.
	Quote *relayRequest `json:"quote,omitempty"`
	// Chat: what a chat_reply tells (chat_reply.go, #10777), so a later batch of the same chat merges into it. A version
	// that does not know it drops it on its next save: the message then goes as it is, and the next batch queues its own.
	Chat *chatReply `json:"chat,omitempty"`
	// Handover: the hand-over of the owner's words this conversation_send is, and Answers: the one it answers
	// (relay_handover.go, #11699). A version that does not know them drops them on its next save: the message then ranks
	// as before, its hand-over stays queued until it expires, and the one it answers stays delivered.
	Handover string `json:"handover,omitempty"`
	Answers  string `json:"answers,omitempty"`
	// DeferredWake marks a message in relayState.Later that was meant to wake its receiver and was held back by the
	// per-sender wake cap (conversation_fanout.go, #14352). Explicit wake: false messages keep this false and are never
	// woken on their own. A version that does not know it drops it on its next save: the message stays quiet until the
	// receiver's next message, as before.
	DeferredWake bool `json:"deferred_wake,omitempty"`
}

type relayHop struct {
	Hop int       `json:"hop"`
	At  time.Time `json:"at"`
	// From: the conversation whose request this one works on and has not answered yet (#7391); "" when its work is the
	// user's, an answer, or a request it answered already. A version that does not know it drops it on its next
	// save: the answer then costs a hop, as every message did.
	From string `json:"from,omitempty"`
}

// relayState is what is saved: the queued messages and how far each conversation's current work is from the user.
type relayState struct {
	Queue map[string][]relayMsg `json:"queue,omitempty"` // by receiver
	Hops  map[string]relayHop   `json:"hops,omitempty"`  // by conversation: the hop of the message it is working on
	// SupervisePending: gathered supervision signals not yet delivered or admitted to Queue. It closes the restart
	// window for states (notably needs_user) that the watch cannot reconstruct (#5732).
	SupervisePending map[string]superviseSignal `json:"supervise_pending,omitempty"`
	// LegacyBackground: legacyBackgroundKey(conversation, todo) whose pre-#5732 supervision notice really reached that
	// conversation. Reads also accept the former NUL-delimited in-memory key; only the Postgres-safe form is written.
	// It survives a manager restart, so an old background can finish exactly those tasks and no new main-supervised ones.
	LegacyBackground map[string]bool `json:"legacy_background,omitempty"`
	// Spawned: the conversations conversation_new opened, by id, until they report back or their parent is told
	// (spawned.go, #4669).
	Spawned map[string]spawnedConv `json:"spawned,omitempty"`
	// Spoke: when the owner last sent each conversation a message, kept for ownerPresentFor (relay_presence.go).
	Spoke map[string]time.Time `json:"spoke,omitempty"`
	// Later: by receiver, what conversation_send with wake: false kept for it, to go in with its next delivered message
	// (relay_later.go, #11614, #12867). A version that does not know it drops it on its next save: those acknowledgements
	// are then not read.
	Later map[string][]relayMsg `json:"later,omitempty"`
	// LaterSends freezes quiet-message context for retries under one delivery ID: an assistant.send once_id or a scoped
	// owner client_msg_id (relay_later.go).
	LaterSends map[string]relayLaterSend `json:"later_sends,omitempty"`
	// Handed: by direction's lead conversation, when a hand-over of the owner's quoted words last went into it
	// (relay_lead_hold.go, #11699): until the owner next writes there (userSpoke), what it plans that writes waits for
	// their Start. A version that does not know it drops it on its next save: those plans then start as before.
	Handed map[string]time.Time `json:"handed,omitempty"`
	// Fanout: by sender and target, when sender last woke target by conversation_send within fanoutWindow
	// (conversation_fanout.go, #14352). Kept across restarts so a restart during a burst preserves the cap.
	Fanout map[string]map[string]time.Time `json:"fanout,omitempty"`
}

type relayBook struct {
	mu       sync.Mutex
	loaded   bool
	st       relayState
	sent     map[[2]string][]time.Time // (from, to) -> when; in memory: a restart only resets the rate
	draining map[string]bool
	doubted  map[string]time.Time   // by relayMsgID: when a try that ended in doubt began, its line not found then (relay_once.go)
	holds    map[string]*time.Timer // by conversation: drains its queue again when the hold for the owner ends
	stuck    map[string]relayMsg    // by conversation: the message that could not go in at the last drain (pickLocked)
	// failedAt: by conversation, when a delivery into it last failed because it did not take the message (it did not
	// start, its computer was away): that message, when nothing else may go, waits relayRetryGap for its next try. A
	// role-change refusal instead holds every message (roleChange below): passing over the last failed message must not
	// let another one retry that refusal on every session list (#10194, #13918). In memory: a restart tries at once.
	failedAt map[string]time.Time
	// msgFailedAt: by conversation and relayMsgID, when each queued message last failed with a retryable error. Keeps
	// multi-message queues from ping-ponging past relayRetryGap once every eligible message has tried and failed (#17592),
	// while still letting a never-failed message try once without waiting on an earlier failed head (#10194). In memory.
	msgFailedAt map[string]map[string]time.Time
	// roleChange: by conversation, a role-change busy refusal waits for the old process's work to end. The value says a
	// session list has shown that work since the refusal. While shown, no message goes; the first later idle list releases
	// at once. If no list ever shows it, failedAt remains the bounded fallback (#13918). In memory, like failedAt.
	roleChange map[string]bool
	// placeholderGone: by relayMsgID, consecutive "is gone" failures observed while the receiver was projected as a
	// LogicalPlaceholder (physical segment not yet listed by its node). In memory; cleared when the head leaves.
	placeholderGone map[string]int
	// placeholderWait: by conversation, a retryable failure occurred while the receiver was a LogicalPlaceholder; when
	// its real physical row appears in a later session list, failedAt is cleared so queued work delivers immediately.
	placeholderWait map[string]bool
	// ownerTurns: the conversations whose round the owner is on, in memory only (#6394). While one is set, every
	// queued message waits, whatever its rank: the owner's own message always goes before the queue. A restart falls
	// back on the persisted Spoke presence window (relay_presence.go).
	ownerTurns map[string]ownerTurn
	// replacing: the background supervisors being replaced (supervise_round.go backgroundOutgrown), in memory: nothing
	// starts going into one, and what would go in straight queues, until what waits for it moved to its replacement or
	// the replacement did not start.
	replacing map[string]bool
	// woke: by conversation, when a scheduled run last started it a new chain (scheduledWake), in memory like sent.
	woke map[string]time.Time
	// quoted: by target and quoted owner message, when a send carrying it went without counting (#10400): only the
	// first such send to a target is free. roundsFrom: by conversation, when the owner last spoke in it; round trips
	// before that are not counted. In memory like sent.
	quoted     map[string]time.Time
	roundsFrom map[string]time.Time
	// ownerLaterInFlight tracks active owner sends carrying relayState.Later[sid] so deferred fanout wakes wait
	// before deciding whether a wake is still needed (conversation_fanout.go, #14381). It never performs settlement
	// (settlement is always durable via LaterSends[owner:<client_msg_id>]).
	ownerLaterInFlight map[string]map[string]time.Time
	// ownerLaterDegraded tracks (session, client_msg_id) owner sends that degraded without quiet relays during a
	// relay state read outage so storage recovery before session.assistant_sent cannot drop an older reservation
	// unread (relay_later.go, #12867).
	ownerLaterDegraded map[[2]string]time.Time
}

// ownerTurn is one conversation's owner-turn gate (#6394).
type ownerTurn struct {
	at      time.Time // when the owner last spoke during it; a later message refreshes the safety limits
	working bool      // a session list has shown the conversation working since the owner spoke
}

func (s *Service) relayLocked(ctx context.Context) bool {
	b := &s.relays
	if b.loaded {
		return true
	}
	rs := relayState{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, relayKey, &rs); err != nil {
			s.logf("load conversation relay: %v", err)
			return false
		}
	}
	if rs.Queue == nil {
		rs.Queue = map[string][]relayMsg{}
	}
	if rs.Hops == nil {
		rs.Hops = map[string]relayHop{}
	}
	if rs.Spawned == nil {
		rs.Spawned = map[string]spawnedConv{}
	}
	if rs.LegacyBackground == nil {
		rs.LegacyBackground = map[string]bool{}
	}
	if rs.SupervisePending == nil {
		rs.SupervisePending = map[string]superviseSignal{}
	}
	for to, q := range rs.Queue {
		for i := range q {
			if q[i].Continuation && q[i].OnceID == "" {
				// Keep the ID an older manager may already have sent. New
				// continuations are pinned before delivery in deliverApprovalResult.
				q[i].OnceID = legacyRelayMsgID(to, q[i])
			}
		}
		rs.Queue[to] = q
	}
	// Upgrade an old queue in place: structured signals are exact; a pre-signals message still carries its todo ids.
	here := s.settings(ctx)
	for conversation, queue := range rs.Queue {
		for _, msg := range queue {
			if msg.Tool != superviseTool {
				continue
			}
			if signals, structured := decodeDigestSignals(msg.Signals); structured {
				for _, g := range signals {
					// Structured recovery requires explicit per-delivery evidence, not a guess from mutable
					// task fields or a lineage-shared brief. Ambiguous old queues stay available to main.
					if s.restoredLegacyBackgroundNotice(here, conversation, g) {
						rs.LegacyBackground[legacyBackgroundKey(conversation, g.Todo)] = true
					}
				}
			} else {
				// A pre-signals digest names each assignment in its numbered entry header. IDs in evidence or links
				// are not notices for those tasks and must not become closeout permission after a restart.
				for _, match := range legacySuperviseEntryIDRe.FindAllStringSubmatch(msg.Text, -1) {
					rs.LegacyBackground[legacyBackgroundKey(conversation, match[1])] = true
				}
			}
		}
	}
	b.st, b.loaded = rs, true
	if len(rs.Fanout) > 0 {
		s.sendFanout.merge(rs.Fanout, time.Now())
	}
	// Reloading after an uncertain answer commit must not forget another message's in-flight drain or owner turn.
	if b.draining == nil {
		b.sent, b.draining, b.replacing, b.holds = map[[2]string][]time.Time{}, map[string]bool{}, map[string]bool{}, map[string]*time.Timer{}
		b.doubted = map[string]time.Time{}
		b.woke = map[string]time.Time{}
		b.quoted, b.roundsFrom = map[string]time.Time{}, map[string]time.Time{}
		b.stuck = map[string]relayMsg{}
		b.failedAt = map[string]time.Time{}
		b.msgFailedAt = map[string]map[string]time.Time{}
		b.roleChange = map[string]bool{}
		b.placeholderGone = map[string]int{}
		b.placeholderWait = map[string]bool{}
		b.ownerTurns = map[string]ownerTurn{}
	}
	return true
}

func (s *Service) saveRelayLocked() error {
	// An uncertain answer commit may have added a durable relay while this cache was invalidated.
	if !s.relays.loaded {
		return errors.New("the relay queue is not loaded")
	}
	st := s.store()
	if st == nil {
		return nil
	}
	for id, hp := range s.relays.st.Hops {
		if time.Since(hp.At) > relayHopMemory {
			delete(s.relays.st.Hops, id)
		}
	}
	for id, at := range s.relays.st.Spoke {
		// One with messages still queued is drainHeldRelays' to forget, after it handed them on.
		if time.Since(at) >= ownerPresentFor && len(s.relays.st.Queue[id]) == 0 {
			delete(s.relays.st.Spoke, id)
		}
	}
	for id, c := range s.relays.st.Spawned {
		if time.Since(c.At) > spawnedKeep {
			delete(s.relays.st.Spawned, id)
		}
	}
	now := time.Now()
	s.pruneRelayLaterLocked(now)
	s.relays.st.Fanout = s.sendFanout.snapshot(now)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := st.PutSetting(ctx, relayKey, s.relays.st)
	if err != nil {
		s.logf("save conversation relay: %v", err)
	}
	return err
}

// hopLocked is the hop a message from → to would be, and whether it is an answer: the first message back to the
// conversation whose request from works on keeps the hop from works at (#7391). Every other message is one hop
// further from the user: a new request, one to a third conversation, a second one back, and the one after an answer.
// An answer is owed only for a request, and a request costs a hop, so a chain counts its exchanges, not its
// messages: a conversation can go back and forth with another a few times, and two that keep writing to each other
// are cut after 2 × relayMaxHops messages, however slowly they write. b.mu is held, the state loaded.
func (b *relayBook) hopLocked(from, to string, now time.Time) (int, bool) {
	hp, ok := b.st.Hops[from]
	if !ok || now.Sub(hp.At) > relayHopMemory { // the user's own work (hop 0), or work they have moved on from
		return 1, false
	}
	if hp.From != "" && hp.From == to {
		return hp.Hop, true
	}
	return hp.Hop + 1, false
}

// userSpoke: the user sent a message to the conversation, which works for them again (hop 0); what the system has
// for it waits while they talk (relay_presence.go).
func (s *Service) userSpoke(id string) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	b.ownerSpokeLocked(id, time.Now())
	delete(b.st.Hops, id)
	delete(b.st.Handed, id)       // the owner is in it again: what they ask for there starts as they ask (relay_lead_hold.go)
	b.roundsFrom[id] = time.Now() // the owner is in it again: its round trips count from here (#10400)
	s.saveRelayLocked()
}

// relayRoute is who is on each end of a message, as the settings name them (#10099): the main conversation and the
// leader conversations. The zero route is an ordinary message between two other conversations.
type relayRoute struct {
	fromMain bool     // from is the main conversation
	leaders  []string // the leader conversations, counted per pair only when the main conversation writes to them
	// quote: the owner's message the hub verified this send carries (conversation/message id), written within
	// relayHopMemory; "" for none. The first send of it to a target is the owner's own request: no rate, no round
	// trip, hop 1 (#10400).
	quote string
	// queue: past the rate the message waits for the window instead of being refused (relayAdmit's due).
	queue bool
}

// relayAdmission is what relayAdmit decided for a message.
type relayAdmission struct {
	hop    int
	answer bool
	due    time.Time // past the rate: when the window has room for it (zero: it may go now)
	free   bool      // it carries the owner's words and counted nothing
	count  int       // the count that put it past the rate, what it counts, and its limit
	what   string
	limit  int
}

// relaySentKeep is how long a sent message is remembered: the rates' window and the round trips'.
func relaySentKeep() time.Duration { return max(relayWindow, relayRoundWindow) }

// relayRouteFor reads the route of a message from self from the settings.
func (s *Service) relayRouteFor(ctx context.Context, self string) relayRoute {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.Main != self {
		return relayRoute{}
	}
	return relayRoute{fromMain: true, leaders: slices.Clone(cfg.Leaders)}
}

// relayAllowed checks the hop and rate limits for a message from → to and counts it when allowed. It returns the
// message's hop and whether it is an answer (hopLocked); an answer that goes uses up from's one free answer.
func (s *Service) relayAllowed(from, to string, now time.Time) (int, bool, error) {
	return s.relayAllowedOn(relayRoute{}, from, to, now)
}

// relayAllowedOn is relayAllowed on a route (#10099). The main conversation is never refused for its hop: its request
// goes at relayMaxHops, so the receiver may answer it but not go further. Its messages to a leader conversation count
// against the per-pair cap only. to == relayNewKey is a conversation_new, with its own per-sender budget.
func (s *Service) relayAllowedOn(r relayRoute, from, to string, now time.Time) (int, bool, error) {
	a, err := s.relayAdmit(r, from, to, now)
	return a.hop, a.answer, err
}

// relayAdmit checks a message from → to and counts it when it may go (#10400):
//   - the owner's verified words (r.quote) go free, once per quoted message and target;
//   - two conversations that turn back to each other more than relayRoundsMax times in relayRoundWindow are cut;
//   - past the per-pair or per-sender rate, a message on a queue route gets the time the window has room for it
//     (due, its place counted then), at most relayQueueMax waiting per pair; on any other route it is refused.
func (s *Service) relayAdmit(r relayRoute, from, to string, now time.Time) (relayAdmission, error) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return relayAdmission{}, errors.New("the relay state could not be read")
	}
	return s.relayAdmitLocked(r, from, to, now, true)
}

func (s *Service) relayAdmitLocked(r relayRoute, from, to string, now time.Time, saveAnswer bool) (relayAdmission, error) {
	b := &s.relays
	for k, ts := range b.sent {
		kept := ts[:0]
		for _, t := range ts {
			if now.Sub(t) < relaySentKeep() {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(b.sent, k)
		} else {
			b.sent[k] = kept
		}
	}
	if r.quote != "" && to != relayNewKey {
		for k, at := range b.quoted {
			if now.Sub(at) > relayHopMemory {
				delete(b.quoted, k)
			}
		}
		key := to + "|" + r.quote
		if _, used := b.quoted[key]; !used {
			b.quoted[key] = now
			return relayAdmission{hop: 1, free: true}, nil
		}
	}
	hop, answer := b.hopLocked(from, to, now)
	if hop > relayMaxHops {
		if !r.fromMain {
			return relayAdmission{}, fmt.Errorf("not sent: this would be hop %d from the user's own message (at most %d). Tell the user what you have instead", hop, relayMaxHops)
		}
		hop = relayMaxHops
	}
	if to != relayNewKey {
		if n := b.turnsLocked(from, to, now); n > 2*relayRoundsMax {
			return relayAdmission{}, fmt.Errorf("not sent: this conversation and that one have gone back and forth more than %d times in the last %d minutes (no word from the user in between). Stop the exchange and tell the user where it stands", relayRoundsMax, int(relayRoundWindow.Minutes()))
		}
	}
	var pairTs, fromTs []time.Time
	for k, ts := range b.sent {
		if k[0] != from {
			continue
		}
		for _, t := range ts {
			if now.Sub(t) >= relayWindow {
				continue // a place counted for later (due) is in the window too
			}
			if !r.fromMain || !slices.Contains(r.leaders, k[1]) {
				fromTs = append(fromTs, t)
			}
			if k[1] == to {
				pairTs = append(pairTs, t)
			}
		}
	}
	a := relayAdmission{hop: hop, answer: answer}
	if to == relayNewKey {
		limit := relayNewMax
		if r.fromMain {
			limit = relayNewMainMax
		}
		if len(pairTs) >= limit {
			return relayAdmission{}, fmt.Errorf("not sent: %d conversations started from this one in the last %d minutes already (at most %d)", len(pairTs), int(relayWindow.Minutes()), limit)
		}
	} else {
		over := func(ts []time.Time, limit int) time.Time { // when the window has room again; zero: it has now
			if len(ts) < limit {
				return time.Time{}
			}
			slices.SortFunc(ts, time.Time.Compare)
			return ts[len(ts)-limit].Add(relayWindow)
		}
		due := over(pairTs, relayPairMax)
		a.count, a.limit = len(pairTs), relayPairMax
		what := "messages to that conversation"
		if !(r.fromMain && slices.Contains(r.leaders, to)) {
			if d := over(fromTs, relayFromMax); d.After(due) {
				due = d
				a.count, a.limit = len(fromTs), relayFromMax
				what = "messages to other conversations"
			}
		}
		if !due.IsZero() && due.After(now) {
			if !r.queue {
				return relayAdmission{}, fmt.Errorf("not sent: %d %s in the last %d minutes already (at most %d)", a.count, what, int(relayWindow.Minutes()), a.limit)
			}
			waiting := 0
			for _, m := range b.st.Queue[to] {
				if m.From == from && m.Due.After(now) {
					waiting++
				}
			}
			if waiting >= relayQueueMax {
				return relayAdmission{}, fmt.Errorf("not sent: %d messages to that conversation already wait for the rate (at most %d). Wait for them to go in, or put what you have together in one message", waiting, relayQueueMax)
			}
			a.due, a.what = due, what
		}
	}
	at := now
	if !a.due.IsZero() {
		at = a.due // its place in the window is when it goes in
	}
	b.sent[[2]string{from, to}] = append(b.sent[[2]string{from, to}], at)
	if answer {
		hp := b.st.Hops[from]
		hp.From = "" // answered: another message back is a new request
		b.st.Hops[from] = hp
		if saveAnswer {
			s.saveRelayLocked()
		}
	}
	return a, nil
}

// turnsLocked is how many times messages between from and to changed direction in relayRoundWindow, since the owner
// last spoke in either, counting the message from → to about to go (a round trip is two turns). Messages that carried
// the owner's words are not counted (relayAdmit does not keep them). b.mu is held.
func (b *relayBook) turnsLocked(from, to string, now time.Time) int {
	since := now.Add(-relayRoundWindow)
	for _, id := range []string{from, to} {
		if at, ok := b.roundsFrom[id]; ok && at.After(since) {
			since = at
		}
	}
	type sent struct {
		at   time.Time
		back bool // to → from
	}
	var all []sent
	for _, t := range b.sent[[2]string{from, to}] {
		if t.After(since) {
			all = append(all, sent{t, false})
		}
	}
	for _, t := range b.sent[[2]string{to, from}] {
		if t.After(since) {
			all = append(all, sent{t, true})
		}
	}
	slices.SortStableFunc(all, func(x, y sent) int { return x.at.Compare(y.at) })
	all = append(all, sent{now, false})
	turns := 0
	for i := 1; i < len(all); i++ {
		if all[i].back != all[i-1].back {
			turns++
		}
	}
	return turns
}

// relayRefund gives back the place a message from → to counted at took when it did not go (a conversation_new that
// did not start, #10099).
func (s *Service) relayRefund(from, to string, at time.Time) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	k := [2]string{from, to}
	if i := slices.Index(b.sent[k], at); i >= 0 {
		b.sent[k] = slices.Delete(b.sent[k], i, i+1)
	}
}

// scheduledWake: a scheduled run (a follow-up or routine) went into conversation id, which starts a new chain, as a
// routine run in a fresh conversation does (#10099). Only once per conversation per relayWindow: a conversation
// cut off by the hops can't schedule itself a minute ahead again and again to keep a loop going. The per-pair and
// per-sender rates hold either way.
func (s *Service) scheduledWake(id string, now time.Time) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return
	}
	if _, ok := b.st.Hops[id]; !ok {
		return // on the user's own chain already
	}
	if at, ok := b.woke[id]; ok && now.Sub(at) < relayWindow {
		return
	}
	b.woke[id] = now
	delete(b.st.Hops, id)
	s.saveRelayLocked()
}

// relayText is the message as the receiver gets it (prompts/assistant/relay.md).
func relayText(msg relayMsg) string {
	return prompts.RenderRelay(prompts.Relay{FromID: msg.From, FromTitle: msg.FromTitle, Tool: msg.Tool, Hop: msg.Hop,
		MaxHops: relayMaxHops, Text: msg.Text, Quote: relayQuoteOf(msg.Quote)})
}

// queueRelay keeps a message for a conversation until it may go in: its turn ended and the owner is not talking in it
// (relay_presence.go). The queue is kept in the order the messages came; drainRelays picks by rank. A turn held by an
// ask_user waiting for the owner lets go of it after a while (ask_release.go).
func (s *Service) queueRelay(to string, msg relayMsg) int {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return 0
	}
	if msg.Tool == superviseTool && msg.DoubtedAt.IsZero() {
		msg.DoubtedAt = b.doubted[relayMsgID(to, msg)] // a direct send became doubtful before it was queued
	}
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(b.st.Queue[to])+1
	previousQueue, hadQueue := b.st.Queue[to]
	previousPending := maps.Clone(b.st.SupervisePending)
	b.st.Queue[to] = append(b.st.Queue[to], msg)
	if msg.Tool == superviseTool {
		if signals, structured := decodeDigestSignals(msg.Signals); structured {
			// Queue admission and outbox acknowledgment share this one durable write (#5732).
			ackSupervisePendingLocked(b.st.SupervisePending, signals)
			if len(b.st.SupervisePending) == 0 {
				b.st.SupervisePending = nil
			}
		}
	}
	err := s.saveRelayLocked()
	if msg.Tool == superviseTool && err != nil {
		// A supervision queue is not admitted until its queue and outbox acknowledgment are durable together. Restore
		// both memory forms so deliverDigest releases its reservation and leaves the turn untold for recovery (#5732).
		if hadQueue {
			b.st.Queue[to] = previousQueue
		} else {
			delete(b.st.Queue, to)
		}
		b.st.SupervisePending = previousPending
		b.mu.Unlock()
		return 0
	}
	n := len(b.st.Queue[to])
	b.mu.Unlock()
	s.handoverSet(msg.Handover, handoverQueued, "", nil) // a hand-over of the owner's words waits (relay_handover.go)
	go s.releaseHeldAsks(to)
	return n
}

// queueRelayDurable is the supervision delivery boundary. Unlike best-effort conversation relays, a supervision
// signal is accepted only after its queued message and outbox acknowledgment are saved together. A failed write rolls
// both in-memory forms back so the durable failed-start outbox and ordinary pending scheduler retry the same generation.
func (s *Service) queueRelayDurable(to string, msg relayMsg) (int, error) {
	if !supervisionRelayAllowed(s.supervisionMode(context.Background()), msg) {
		return 0, errSupervisionSuppressed // nothing the mode drops waits in a queue (supervise_switch.go)
	}
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return 0, errors.New("the relay queue cannot be read")
	}
	if msg.Tool == superviseTool && msg.DoubtedAt.IsZero() {
		msg.DoubtedAt = b.doubted[relayMsgID(to, msg)] // a direct send became doubtful before it was queued
	}
	previous := b.st.Queue
	previousPending := maps.Clone(b.st.SupervisePending)
	b.st.Queue = maps.Clone(previous)
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(previous[to])+1
	b.st.Queue[to] = append(slices.Clone(previous[to]), msg)
	if msg.Tool == superviseTool {
		if signals, structured := decodeDigestSignals(msg.Signals); structured {
			// Queue admission and outbox acknowledgment share this one durable write (#5732).
			ackSupervisePendingLocked(b.st.SupervisePending, signals)
			if len(b.st.SupervisePending) == 0 {
				b.st.SupervisePending = nil
			}
		}
	}
	if err := s.saveRelayLocked(); err != nil {
		b.st.Queue = previous
		b.st.SupervisePending = previousPending
		b.mu.Unlock()
		return 0, err
	}
	n := len(b.st.Queue[to])
	b.mu.Unlock()
	go s.releaseHeldAsks(to)
	return n, nil
}

// queuedRelays are the texts waiting for a conversation (shown above its composer, like typed-ahead input); false
// while they could not be read.
func (s *Service) queuedRelays() (map[string][]string, bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return nil, false
	}
	if len(b.st.Queue) == 0 && len(b.st.Later) == 0 {
		return nil, true
	}
	out := map[string][]string{}
	for id, q := range b.st.Later { // kept without waking it (relay_later.go): they go in with its next message
		for _, msg := range q {
			if msg.Internal {
				continue
			}
			if msg.DeferredWake {
				out[id] = append(out[id], "来自「"+msg.FromTitle+"」（稍后唤醒）："+msg.Text) // i18n-ignore: the queue's label, as the others'
				continue
			}
			out[id] = append(out[id], "来自「"+msg.FromTitle+"」（不唤醒）："+msg.Text) // i18n-ignore: the queue's label, as the others'
		}
	}
	for id, q := range b.st.Queue {
		for _, msg := range q {
			if msg.Internal {
				continue
			}
			if msg.standalonePermission() || msg.Tool == noticeTool || msg.Tool == askAnswerTool || msg.Tool == spawnNoticeTool || msg.Tool == taskEndTool || msg.Tool == prFollowupTool || msg.Tool == relayRefusedTool { // a quiet task's notice (quiet.go), a question's answer (ask_todo.go): its title is enough
				out[id] = append(out[id], msg.FromTitle+"的通知")
				continue
			}
			if msg.Tool == superviseTool { // the tasks to follow up (supervise_digest.go)
				out[id] = append(out[id], "督办消息")
				continue
			}
			if msg.Tool == chatReplyTool { // a chat's new messages (chat_reply.go): their words stay out of the label
				out[id] = append(out[id], msg.FromTitle+"的新消息") // i18n-ignore: the queue's label, as the task notices'
				continue
			}
			if msg.Tool == extensionEventTool { // an extension's event (extension_event.go): who and what
				out[id] = append(out[id], msg.FromTitle+": "+msg.Title)
				continue
			}
			out[id] = append(out[id], "来自「"+msg.FromTitle+"」："+msg.Text)
		}
	}
	return out, true
}

// relayWaitingFor reports whether a conversation has any relay waiting, including one kept only for the assistant.
// Conversation deletion uses this rather than queuedRelays, whose labels leave internal relays out of the client UI.
func (s *Service) relayWaitingFor(id string) (bool, bool) {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false, false
	}
	return len(b.st.Queue[id]) > 0 || len(b.st.Later[id]) > 0, true
}

func (msg relayMsg) source() *proto.MessageSource {
	switch msg.Tool {
	case "conversation_send", "conversation_new":
		return &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: msg.From, Title: msg.FromTitle}
	case superviseTool:
		return &proto.MessageSource{Kind: proto.SourceSupervision}
	case prFollowupTool, taskEndTool, relayRefusedTool, spawnNoticeTool, chatReplyTool, cardAgentPermission:
		return &proto.MessageSource{Kind: proto.SourceNotice, Title: "Persona"}
	case extensionEventTool:
		return &proto.MessageSource{Kind: proto.SourceExtension, Title: msg.FromTitle}
	default:
		return nil
	}
}

// deliverRelay sends one message into an assistant conversation and notes the hop it is working on. A quiet task's
// notice (quiet.go) is its own text, and the work it starts is not a hop of any chain.
func (s *Service) deliverRelay(ctx context.Context, machine, to string, msg relayMsg) error {
	res, round, err := s.sendRelay(ctx, machine, to, msg)
	if err != nil {
		return err
	}
	if round {
		s.superviseRoundAnswered(to, res)
	}
	s.relayDelivered(ctx, to, msg)
	return nil
}

// sendRelay performs the assistant.send RPC for a relayed message without marking supervision delivery complete.
// Supervision callers confirm the user item in the destination transcript before completing delivery (#12567).
func (s *Service) sendRelay(ctx context.Context, machine, to string, msg relayMsg) (superviseRoundReply, bool, error) {
	if !supervisionRelayAllowed(s.supervisionMode(ctx), msg) {
		return superviseRoundReply{}, false, errSupervisionSuppressed // the last look before the send: a timer or a drain that was already running
	}
	if msg.Tool == superviseTool && s.runtimeWithoutSupervisionPolicy(ctx, machine, to) {
		return superviseRoundReply{}, false, s.supervisionRuntimeRefusal(ctx, machine)
	}
	text := relayDeliveryText(msg)
	source, onceID := msg.source(), relayMsgID(to, msg)
	if block := s.agyRelayBlock(ctx, machine, to, msg); block != "" {
		ctx = context.WithValue(ctx, agyRelayContextKey{}, agyRelayContext{machine: machine, session: to, onceID: onceID, block: block})
	}
	if strings.TrimSpace(text) != "" && relayWords(text) == "" {
		text = prompts.RelayMessageText(text, source, onceID)
	}
	hold := s.shouldHoldSupervisionPushes(ctx, to, msg)
	params := map[string]any{"session_id": to, "text": text, "message_source": source, "resume_only": true, holdPushesParam: hold,
		"once_id": onceID} // every try of it under one id: the node sends it once (relay_once.go)
	if msg.Interject {
		params["interject_background"] = true
	}
	pick, kept, err := s.readModelPick(ctx, machine, to)
	if err != nil {
		return superviseRoundReply{}, false, err // unknown is not permission to use a process that still has an older ordinary connection
	}
	if !kept && proto.IsExternalLauncher(pick.Launcher) {
		return superviseRoundReply{}, false, i18n.Err("external-adapter.scope_required") // a kept choice on another machine is not a fallback on this one
	}
	if kept {
		if err := s.relayOnPick(ctx, machine, to, params, pick); err != nil {
			return superviseRoundReply{}, false, err
		}
	} else if s.isBackground(ctx, to) {
		s.backgroundModel(ctx, params) // its own model purpose, on every message as the main chat's is (supervise_background.go)
	} else if s.conversationModelPurpose(ctx, machine, to) == purposeMain {
		s.relayMainModel(ctx, machine, to, params)
	}
	if msg.standalonePermission() {
		cfg := s.settings(ctx)
		ix, pending := s.pendingStandalonePermission(to, msg, time.Now())
		if !pending || s.canonical(cfg.MachineID) != s.canonical(machine) || s.directoryHiddenSession(ix.MachineID, ix.SessionID) {
			return superviseRoundReply{}, false, errStandalonePermissionStale
		}
	}
	s.availInput(machine, to, relayKind(msg), time.Now())
	if msg.Tool == superviseTool {
		if hold {
			// Register only after admission, but before dispatch: a fast MCP call may precede assistant.send's answer.
			err = s.holdPushesFor(ctx, to, msg)
		} else {
			err = s.noteSideSupervisionPush(ctx, to, msg)
		}
		if err != nil {
			return superviseRoundReply{}, false, beforeNodeWrite(DeviceAssistantSend, err)
		}
	}
	// A supervision message rendered as a round starts from the first turn (supervise_round.go): what the text says
	// and the switch now decide, also for one queued before a restart or a change of the switch, and whatever model runs it.
	round := s.isBackground(ctx, to) && msg.Tool == superviseTool && !backgroundKeepsContext(s.settings(ctx)) && prompts.IsSuperviseRound(msg.Text)
	if round {
		params[superviseRoundParam] = true
	}
	if msg.Private {
		// Just me covers the original turn, not a later terminal-result turn.
		// Renew it at this actual-dispatch boundary so queued and restarted
		// deliveries retain the same protection as immediate ones.
		s.justMeSent(machine, to)
	}
	// A hand-over of the owner's words into a lead holds its writable plans from before the send (relay_lead_hold.go).
	handed := s.holdForHandover(to, msg)
	// Background, scheduled, supervision, and extension-event turns do not originate from a user's device;
	// clear any previous human turn's origin device so it never leaks into this turn's connector calls.
	s.clearConversationOriginDevice(to)
	var res superviseRoundReply
	if err := s.call(ctx, machine, DeviceAssistantSend, params, &res); err != nil {
		if msg.Tool == superviseTool && !relayInDoubt(err) { // in doubt, drainRelays lets go once it knows it did not go in
			s.releasePushes(ctx, to)
		}
		handed(err)
		return superviseRoundReply{}, round, err
	}
	if s.isBackground(ctx, to) && res.SessionID != "" && res.SessionID != to {
		s.registerBackgroundRoundFork(ctx, machine, res.SessionID, to)
	}
	return res, round, nil
}

// relayDeliveryText is what a relayed message says in the conversation: a notice, a supervision message and an answer
// are their own text; a message from another conversation says where it comes from.
func relayDeliveryText(msg relayMsg) string {
	if msg.Tool == noticeTool || msg.Tool == superviseTool || msg.Tool == askAnswerTool || msg.Tool == spawnNoticeTool || msg.Tool == taskEndTool || msg.Tool == prFollowupTool ||
		msg.Tool == chatReplyTool || msg.Tool == relayRefusedTool || msg.Tool == extensionEventTool {
		return msg.Text
	}
	return relayText(msg)
}

// relayDelivered notes a message that went into conversation to: the hop it is working on, or the report turn a
// notice starts.
func (s *Service) relayDelivered(ctx context.Context, to string, msg relayMsg) {
	if msg.standalonePermission() {
		if ix, ok := s.pendingStandalonePermission(to, msg, time.Now()); ok {
			s.fleet.mu.Lock()
			if p := s.fleet.interactions[ix.ID]; p != nil && p.route != nil && p.route.routedAt.Equal(msg.At) {
				p.route.delivered = true
			}
			s.fleet.mu.Unlock()
			s.workEvent(ix.MachineID, ix.SessionID, "ask_internal_delivered", map[string]any{"id": ix.ID, "to": to})
		}
	}
	if msg.Tool == superviseTool {
		s.backgroundRoundDelivered(ctx, to, msg, time.Now())
		s.askRelayDelivered(to, msg)
		if !s.shouldHoldSupervisionPushes(ctx, to, msg) {
			s.noteReportTurn(to)
		}
		return
	}
	if msg.Tool == taskEndTool || msg.Tool == prFollowupTool || msg.Tool == relayRefusedTool {
		return // a record for the conversation that planned the task (or a closed task's PR follow-up): no hop of any chain, no push of its own
	}
	if msg.Tool == chatReplyTool {
		s.chatReplyDelivered(to, msg) // others' words: no hop of any chain; the next one about that chat waits its gap
		return
	}
	if msg.Tool == noticeTool || msg.Tool == askAnswerTool {
		s.noteReportTurn(to) // the answer to a task's notice (or to the owner's late call) is pushed however quick it was (#3438)
		return
	}
	if msg.Tool == extensionEventTool { // not a hop of any chain; the answer to a warning or a critical one is pushed, an info one's goes by the device's setting
		if msg.State != assistantwire.EventSeverityInfo {
			s.noteReportTurn(to)
		}
		return
	}
	s.handoverWentIn(to, msg) // the hand-over of the owner's words it is, or answers (relay_handover.go)
	s.noteHandedOver(to, msg) // and a lead it went into waits for the owner before writable work (relay_lead_hold.go)
	now := time.Now().UTC()
	b := &s.relays
	b.mu.Lock()
	if s.relayLocked(ctx) {
		hp := relayHop{Hop: msg.Hop, At: now, From: msg.From} // a request: its first message back is the answer (hopLocked)
		if msg.Answer {
			// The answer to a question of its own: a request it works on is still to be answered.
			hp.From = ""
			if old, ok := b.st.Hops[to]; ok && now.Sub(old.At) <= relayHopMemory {
				hp.From = old.From
			}
		}
		b.st.Hops[to] = hp
		s.saveRelayLocked()
	}
	b.mu.Unlock()
}

// sameRelayHeadContent compares the message generation, ignoring only the attempt progress the draining writer saves
// before and during its own send. Text, signals, withdrawal and every other content field still have to match exactly.
func sameRelayHeadContent(a, b relayMsg) bool {
	a.TriedAt, b.TriedAt = time.Time{}, time.Time{}
	a.Looks, b.Looks = 0, 0
	a.LookAfter, b.LookAfter = time.Time{}, time.Time{}
	a.DoubtedAt, b.DoubtedAt = time.Time{}, time.Time{}
	a.ConfirmFails, b.ConfirmFails = 0, 0
	return a == b
}

// relayDeliveryCoversHead reports a narrowly narrowed in-flight supervision head: the original send is still live, the
// current head was withdrawn by a confirmation, and every remaining signal was already in that original send. A new or
// changed signal is never covered. The original delivered text proves all of them, so the narrowed head may leave too.
func relayDeliveryCoversHead(id string, current, sent relayMsg) bool {
	if sent.Tool != superviseTool || sent.Withdrawn || !current.Withdrawn || relayMsgID(id, current) != relayMsgID(id, sent) {
		return false
	}
	remaining, currentStructured := decodeDigestSignals(current.Signals)
	delivered, sentStructured := decodeDigestSignals(sent.Signals)
	if !currentStructured || !sentStructured {
		return false
	}
	for _, g := range remaining {
		if !slices.ContainsFunc(delivered, func(old superviseSignal) bool { return old.id() == g.id() && old.Key == g.Key }) {
			return false
		}
	}
	return true
}

// takeRelayHeadLocked removes msg when it is still the queue head and returns the withdrawn signals to replay.
// relays.mu is held; the outbox update and queue removal are saved together by the caller.
func (s *Service) takeRelayHeadLocked(id string, msg relayMsg, replayOK bool) (replay []superviseSignal) {
	b := &s.relays
	q := b.st.Queue[id]
	if len(q) == 0 {
		return nil
	}
	covered := relayDeliveryCoversHead(id, q[0], msg)
	if !sameRelayHeadContent(q[0], msg) && !covered {
		return nil // another writer changed the head: this copy neither removed it nor owns its replay
	}
	if covered {
		// A confirmation re-durable-ized the remaining signals in case the in-flight original never landed. It did land,
		// so its original structured payload acknowledges those retained stretches with the head removal (#5732).
		if signals, structured := decodeDigestSignals(msg.Signals); structured {
			ackSupervisePendingLocked(b.st.SupervisePending, signals)
			if len(b.st.SupervisePending) == 0 {
				b.st.SupervisePending = nil
			}
		}
	}
	if replayOK && msg.Withdrawn {
		if signals, structured := decodeDigestSignals(msg.Signals); structured {
			// A withdrawn head never delivered this copy. Restore only its remaining signals, atomically with removing the
			// head; a newer durable stretch of the same id still wins (#5732).
			if b.st.SupervisePending == nil {
				b.st.SupervisePending = map[string]superviseSignal{}
			}
			for _, g := range signals {
				cur, exists := b.st.SupervisePending[g.id()]
				if !exists {
					cur = g
					b.st.SupervisePending[g.id()] = cur
				}
				replay = append(replay, cur)
			}
		}
	}
	b.st.Queue[id] = q[1:]
	msgID := relayMsgID(id, msg)
	delete(b.doubted, msgID) // durable and in-memory delivery evidence leave only with the actual head
	delete(b.placeholderGone, msgID)
	b.clearMsgFailedLocked(id, msgID)
	if len(b.st.Queue[id]) == 0 {
		delete(b.st.Queue, id)
		delete(b.msgFailedAt, id)
	}
	return replay
}

// drainRelays delivers the next queued message to every conversation of the machine that is not running (called
// whenever its list changes: a turn that just ended, or the hub starting with messages still queued). Next is the
// best-ranked message the owner's presence does not hold (relay_presence.go); it is moved to the head of the queue,
// where the message going in is. A conversation whose messages are all held is drained again when the first hold
// ends.
func (s *Service) drainRelays(machine string, list []proto.Session) {
	if !s.autoSupervisionOn(context.Background()) {
		// A node that reconnects may still hold supervision input in its conversations' queues from before the switch was
		// turned off (supervise_switch.go); the switch applies to them as soon as they are listed again.
		go s.cancelNodeSupervision(context.Background(), machine, slices.Clone(list))
	}
	leads := s.queueLeads(context.Background())
	placeholders := make([]bool, len(list))
	for i := range list {
		if list[i].LogicalPlaceholder {
			placeholders[i] = true
		} else if list[i].Assistant && list[i].Cwd == "" && list[i].Space == "" {
			placeholders[i] = s.isLogicalOfflineRow(list[i])
		}
	}
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) || len(b.st.Queue) == 0 {
		b.mu.Unlock()
		return
	}
	type dueRelay struct {
		id          string
		background  bool // the list used for this try showed background work
		placeholder bool // the receiver's active physical segment was not yet in the node's session list
	}
	var due []dueRelay
	held := map[string]time.Duration{}
	now := time.Now()
	for i, c := range list {
		id := c.SessionID
		if len(b.st.Queue[id]) == 0 || b.draining[id] || b.replacing[id] {
			continue
		}
		placeholder := placeholders[i]
		if !placeholder && b.placeholderWait[id] {
			delete(b.placeholderWait, id)
			delete(b.failedAt, id)
			delete(b.msgFailedAt, id)
			delete(b.stuck, id)
		}
		background := backgroundRunning(c)
		sawWork, waiting := b.roleChange[id]
		if waiting && (c.Status == proto.StatusWorking || c.Starting || background) {
			// A role-change refusal is an event gate, not a poll: the first list with all of the old process's work over
			// releases it. This also covers background tasks, which do not make the node's foreground Status working.
			b.roleChange[id] = true
			continue
		}
		if c.Status == proto.StatusWorking || c.Starting {
			continue
		}
		if waiting && sawWork {
			delete(b.roleChange, id)
			delete(b.failedAt, id) // work completion is the retry event; do not add another gap after it
			delete(b.msgFailedAt, id)
			waiting = false
		}
		i, wait := b.pickLocked(id, leads[id], now)
		if i < 0 {
			held[id] = wait
			continue
		}
		last, stuck := b.stuck[id]
		if waiting || (stuck && b.st.Queue[id][i] == last) {
			// A role-change refusal belongs to the conversation: pickLocked passing over b.stuck must not let two
			// messages alternate around the gap (#13918). Other failures keep their per-message fairness (#10194).
			if gap := relayRetryGap - now.Sub(b.failedAt[id]); gap > 0 {
				held[id] = gap
				continue
			}
		}
		b.frontLocked(id, i)
		b.draining[id] = true
		due = append(due, dueRelay{id: id, background: background, placeholder: placeholder})
	}
	b.mu.Unlock()
	for id, wait := range held {
		s.drainAfter(machine, id, wait)
	}
	for _, next := range due {
		go func(id string, background, placeholder bool) {
			// A transfer must finish before a supervision drain selects its routing state.
			s.supervise.routingMu.RLock()
			defer s.supervise.routingMu.RUnlock()
			b.mu.Lock()
			q := b.st.Queue[id]
			if len(q) == 0 {
				delete(b.draining, id)
				b.mu.Unlock()
				return
			}
			msg := q[0]
			original := msg
			b.mu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			// A needs notice may have been answered on the phone after it queued while the agent's node still
			// reported blocked (#4332): ask the task once more right before it would interrupt. A node that cannot
			// be reached is not an answer, so its notice stays queued for a later drain.
			stale, keep := msg.Withdrawn || s.chatReplyStale(ctx, msg), false
			if !stale && msg.standalonePermission() {
				// A restored card gets a fresh route. An old wake must leave before its
				// transcript lookup can delay the current wake behind it.
				_, pending := s.pendingStandalonePermission(id, msg, time.Now())
				stale = !pending
			}
			if !stale && msg.Tool == noticeTool && msg.State == "needs" {
				// The loading read covers a hub restart whose first drain runs before the todos were read in:
				// an unreadable todo is unknown, so the notice is delivered as without the recheck.
				if t, ok, _ := s.todo(ctx, msg.From); ok && t.Quiet {
					if waits, known := s.taskNeedsRecheck(ctx, t); !known {
						keep = true
					} else if !waits {
						stale = true
					}
				}
			}
			var err error
			var retry time.Duration
			landed := false
			previousLooks := msg.Looks
			if !stale && !keep && (!msg.TriedAt.IsZero() || !msg.DoubtedAt.IsZero()) {
				if msg.TriedAt.IsZero() {
					msg.TriedAt = msg.DoubtedAt
				}
				landed = s.lookForRelay(machine, id, &msg)
				if !landed && msg.Looks < relayLookLimit {
					keep, retry = true, relayLookGap
				}
			}
			suppressed := false       // it was not sent because automatic supervision messages are off (supervise_switch.go)
			confirmExhausted := false // an unconfirmed supervision head hit supervisionConfirmRetryLimit and is retired
			if !stale && !keep && !landed && msg.Tool == superviseTool {
				var checked relayMsg
				var triedMismatch bool
				checked, stale, keep, triedMismatch = s.recheckQueuedDigestDetailed(ctx, id, original)
				if !stale && !keep {
					original, msg = checked, checked
				} else if triedMismatch {
					if previousLooks < relayLookLimit {
						// First pass reaching relayLookLimit: keep the exact uncertain payload and schedule a follow-up look.
						retry = relayLookGap
					} else {
						msg.ConfirmFails++
						if msg.ConfirmFails >= supervisionConfirmRetryLimit {
							confirmExhausted = true
							stale, keep, retry = true, false, 0
						} else {
							retry = relayLookGap
						}
					}
				}
			}
			if !stale && !keep && !landed && msg.Tool == prFollowupTool {
				// Pre-upgrade queued follow-ups also become quiet. A delivery
				// already in doubt was checked above under its original identity.
				quiet := msg
				quiet.OnceID = relayMsgID(id, original)
				_, stale = s.keepRelayLater(id, quiet)
				keep = !stale
				if keep {
					retry = relayRetryGap
				}
			}
			if !stale && !keep && !landed {
				msg, retry, err = s.tryQueuedApprovalRelay(ctx, machine, id, &original)
				keep = retry > 0
				if relaySuppressed(err) { // restored after a restart or a reconnect, or left by a withdrawal that did not reach it
					stale, suppressed, keep, retry, err = true, true, false, 0, nil // leaves unsent: nothing replayed, nobody pushed
				} else if errors.Is(err, errSupervisionConfirmExhausted) {
					confirmExhausted = true
					stale, keep, retry, err = true, false, 0, nil
				} else if errors.Is(err, errStandalonePermissionStale) {
					stale, err = true, nil
				}
			}
			cancel()
			// Withdrawing the text does not erase an earlier send's outcome. If its in-doubt line has since landed, the
			// original mixed digest already carried every remaining signal, so none of them may be replayed (#5732).
			landed = landed || msg.Withdrawn && s.relayLanded(machine, id, msg)
			// What the failure says (relayFailureOf): refused for good, it leaves the queue once unless its kind keeps it
			// (relayRefused), and then is what follows; gone, it leaves; any other, it is tried again after a gap.
			var failure relayFailure
			refused, stays := false, false
			var then func(context.Context)
			if confirmExhausted {
				s.logf("relay to %s: supervision send unconfirmed after %d confirmation cycle(s); retiring stuck queue head and alerting owner", short(id), msg.ConfirmFails)
				then = func(ctx context.Context) { s.reportSupervisionUnconfirmed(ctx, machine, id) }
			}
			if err != nil && !keep {
				failure = relayFailureOf(err)
				if failure == relayFailedRefused {
					refused = true
					stays, then = s.relayRefused(machine, id, msg, err)
				}
			}
			deferGone := false
			if err != nil && !keep && failure == relayFailedGone && placeholder {
				b.mu.Lock()
				if b.placeholderGone == nil {
					b.placeholderGone = map[string]int{}
				}
				msgID := relayMsgID(id, msg)
				b.placeholderGone[msgID]++
				deferGone = b.placeholderGone[msgID] < relayPlaceholderGoneRetries
				b.mu.Unlock()
			}
			leaves := stale || refused && !stays || !keep && (err == nil || (failure == relayFailedGone && !deferGone))
			if leaves && err != nil && failure == relayFailedGone {
				// Dropped: a hand-over it carried will not be tried again, marked before the queue without it is saved.
				s.handoverEnded(msg.Handover, handoverGone)
				switch {
				case msg.Tool == "conversation_send" || msg.Tool == "conversation_new":
					then = func(ctx context.Context) { s.tellRelayRefused(ctx, machine, id, msg, handoverGone) }
				case msg.Tool == askAnswerTool && !strings.HasPrefix(msg.From, approvalResultFrom):
					// Preserve an owner's card answer when a side conversation is gone by moving it to the main conversation,
					// matching askTarget and relayRefused. When the main conversation itself is gone (or unset), drop it.
					here := s.settings(context.Background())
					if here.Main != "" && here.Main != id && s.moveRelay(here.Main, msg) {
						then = func(ctx context.Context) {
							s.publish(ctx)
							if c, ok := s.session(ctx, here.MachineID, here.Main); ok && c.Status != proto.StatusWorking && !c.Starting {
								s.drainRelays(here.MachineID, []proto.Session{c})
							}
						}
					}
				}
			}
			b.mu.Lock()
			delete(b.draining, id)
			// An inline answer may have committed while this send was in flight. Apply its result to the loaded queue.
			if !b.loaded {
				reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				loaded := s.relayLocked(reloadCtx)
				cancel()
				if !loaded {
					s.drainAfterLocked(machine, id, relayRetryGap)
					b.mu.Unlock()
					return
				}
			}
			delete(b.stuck, id)
			delete(b.failedAt, id)
			delete(b.roleChange, id)
			delete(b.placeholderWait, id)
			if (err == nil && !stale && !keep) || landed {
				delete(b.msgFailedAt, id)
			}
			if err != nil && !keep && !leaves && failure != relayFailedInDoubt {
				now := time.Now()
				b.failedAt[id] = now
				b.recordMsgFailedLocked(id, msg, now)
				if placeholder {
					if b.placeholderWait == nil {
						b.placeholderWait = map[string]bool{}
					}
					b.placeholderWait[id] = true
					if deferGone && retry == 0 {
						retry = relayRetryGap
					}
				}
				if roleChangeBusy(err) {
					// If this try's own list already showed background work, the next all-idle list is its completion
					// event. Otherwise one active list must be seen first; absent that, relayRetryGap is the fallback.
					b.roleChange[id] = background
				}
			}
			taken := false
			var replay []superviseSignal
			if leaves {
				queuedBefore := len(b.st.Queue[id])
				replay = s.takeRelayHeadLocked(id, msg, !landed && !refused && !suppressed && !confirmExhausted)
				taken = len(b.st.Queue[id]) < queuedBefore
				if taken {
					s.saveRelayLocked()
				} else if len(b.st.Queue[id]) > 0 {
					// Another writer replaced the head while this send was outside the lock. The current head still needs
					// its own delivery decision; this copy neither acknowledges it nor records a delivered receipt.
					s.drainAfterLocked(machine, id, 0)
				}
			} else {
				if q := b.st.Queue[id]; len(q) > 0 && q[0] == original && q[0] != msg {
					q[0] = msg
					s.saveRelayLocked()
				}
				b.stuck[id] = msg // the next drain takes another one first (pickLocked)
			}
			if retry > 0 {
				s.drainAfterLocked(machine, id, retry)
			} else if err != nil && !keep && !leaves && failure != relayFailedInDoubt && msg.Tool == extensionEventTool && relayRetryGap > 0 {
				s.drainAfterLocked(machine, id, relayRetryGap)
			}
			b.mu.Unlock()
			if len(replay) > 0 {
				s.restoreDigest(replay, time.Time{}, time.Time{})
			}
			if taken && !stale && err == nil {
				s.availQueueDelivered(machine, id, msg, time.Now())
			}
			if taken {
				s.publish(context.Background()) // it no longer shows waiting
			}
			if confirmExhausted && taken {
				s.schedulePendingSupervision() // unblock supervision signals that waited behind the retired head
			}
			if suppressed {
				s.logf("relay to %s: supervision message suppressed, automatic supervision messages are off", short(id))
			}
			if then != nil {
				then(context.Background())
			}
			if stale || refused && !stays {
				if list, err := s.sessions(context.Background(), machine); err == nil {
					s.drainRelays(machine, list) // a message queued behind the stale or refused one is due as well
				}
			} else if !keep && err != nil && !refused { // relayRefused logged a refusal
				s.logf("relay to %s: %v", short(id), err)
			}
		}(next.id, next.background, next.placeholder)
	}
}

// --- the tools (definitions in prompts/mcp/assistant) ---

// assistantConversation finds an assistant conversation by full id or prefix on the assistant's machine.
func (s *Service) assistantConversation(ctx context.Context, id string) (proto.Session, error) {
	return s.spaceConversation(ctx, id, nil)
}

// spaceConversation is assistantConversation among the conversations of one space only (in == nil: any): a
// conversation's tools reach only the conversations of its own space (docs/assistant/health.md "How directories and memory are isolated").
func (s *Service) spaceConversation(ctx context.Context, id string, in *string) (proto.Session, error) {
	return s.spaceConversationOnMachine(ctx, s.settings(ctx).MachineID, id, in)
}

func (s *Service) spaceConversationOnMachine(ctx context.Context, machine, id string, in *string) (proto.Session, error) {
	list, err := s.sessions(ctx, machine)
	if errors.Is(err, ErrNoDevice) {
		return proto.Session{}, errors.New("the assistant's machine is not configured")
	}
	if err != nil {
		return proto.Session{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return proto.Session{}, errors.New("id is required")
	}
	var found []proto.Session
	for _, c := range list {
		if !c.Assistant || in != nil && spaceOf(c) != *in {
			continue
		}
		if c.SessionID == id {
			return c, nil
		}
		if len(id) >= 8 && strings.HasPrefix(c.SessionID, id) {
			found = append(found, c)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return proto.Session{}, fmt.Errorf("no conversation %q (conversations_list has the ids)", id)
	}
	return proto.Session{}, fmt.Errorf("%q is the start of %d conversations: give more of the id", id, len(found))
}

// conversations are the assistant's conversations of space ("" its own) on its machine (archived ones too when
// asked) and the main one.
func (s *Service) conversations(ctx context.Context, withArchived bool, space string) (list []proto.Session, main string) {
	here := s.settings(ctx)
	all, _ := s.sessions(ctx, here.MachineID) // none on no machine
	for _, c := range all {
		if c.Assistant && !c.IsDeleted() && spaceOf(c) == space && (withArchived || !c.Archived) {
			list = append(list, c)
		}
	}
	return list, here.Main
}

// conversationName is how a conversation is called in tool results and relayed messages.
func conversationName(c proto.Session, main string) string {
	if c.SessionID == main {
		return "主对话"
	}
	// Not named yet: the node's title is then the directory's name ("assistant") or Claude Code's default.
	if t := strings.TrimSpace(c.Title); t != "" && t != "Claude Code" && t != path.Base(c.Cwd) {
		return t
	}
	return "新对话"
}

func (s *Service) toolConversationsList(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		Query           string `json:"query"`
		Tag             string `json:"tag"`
		IncludeArchived bool   `json:"include_archived"`
		Limit           int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &a)
	if a.Limit <= 0 {
		a.Limit = 30
	}
	a.Limit = min(a.Limit, 100)
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return "", err
	}
	list, main := s.conversations(ctx, a.IncludeArchived, space)
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	q := strings.ToLower(strings.TrimSpace(a.Query))
	tag := strings.TrimSpace(a.Tag)
	loc := s.location(ctx)
	var b strings.Builder
	n := 0
	for _, c := range list {
		name := conversationName(c, main)
		if q != "" && !strings.Contains(strings.ToLower(name+"\n"+c.LastText), q) {
			continue
		}
		if tag != "" && !hasTag(c, tag) {
			continue
		}
		if n >= a.Limit {
			n++
			continue
		}
		n++
		var marks []string
		if c.SessionID == main {
			marks = append(marks, "main")
		}
		if c.SessionID == self {
			marks = append(marks, "this conversation")
		}
		if c.Status == proto.StatusWorking || c.Starting {
			marks = append(marks, "running")
		}
		if c.Archived {
			marks = append(marks, "archived")
		}
		if c.Agent == "codex" {
			marks = append(marks, "codex")
		}
		fmt.Fprintf(&b, "- %s 「%s」", c.SessionID, name)
		if len(marks) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(marks, ", "))
		}
		if c.Tags != nil && len(*c.Tags) > 0 {
			fmt.Fprintf(&b, " · tags: %s", strings.Join(*c.Tags, ", "))
		}
		if !c.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " · last active %s (%s)", c.UpdatedAt.In(loc).Format("01-02 15:04"), ago(c.UpdatedAt))
		}
		if t := oneLine(c.LastText); t != "" {
			fmt.Fprintf(&b, "\n  last: %s", clip(t, 160))
		}
		b.WriteString("\n")
	}
	if n == 0 {
		var filters []string
		if q != "" {
			filters = append(filters, fmt.Sprintf("query %q", a.Query))
		}
		if tag != "" {
			filters = append(filters, fmt.Sprintf("tag %q", tag))
		}
		if len(filters) > 0 {
			return "No conversation matches " + strings.Join(filters, " and ") + ".", nil
		}
		return "No conversations.", nil
	}
	head := fmt.Sprintf("%d conversation(s), newest activity first", n)
	if !a.IncludeArchived {
		head += " (archived ones left out)"
	}
	if tag != "" {
		head += fmt.Sprintf(" (tag %q)", tag)
	}
	if n > a.Limit {
		head += fmt.Sprintf("; the first %d", a.Limit)
	}
	return head + ":\n" + b.String(), nil
}

func (s *Service) toolConversationRead(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		Turns int    `json:"turns"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if a.Turns <= 0 {
		a.Turns = 2
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, c.MachineID, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "turns": min(a.Turns, 10)}, &res); err != nil {
		return "", err
	}
	head := fmt.Sprintf("「%s」 %s (%s)", conversationName(c, s.settings(ctx).Main), c.SessionID, stateNames[c.State])
	return transcriptText(head, sessionLink(s.webBase(ctx), c.MachineID, c.SessionID, true), res.Items), nil
}

func (s *Service) toolConversationRename(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Topic     string `json:"topic"`
		UserAsked bool   `json:"user_asked"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if c.SessionID == s.settings(ctx).Main {
		return "", errors.New("the main conversation is always shown as 主对话: it has no title to change")
	}
	if s.isLeader(ctx, c.SessionID) {
		if d := s.directionOf(c.SessionID); d != nil && d.NameBy == directionByUser && !a.UserAsked {
			return "", errors.New("the user named this direction themselves: rename it only when they ask you to, and then pass user_asked")
		}
		name := strings.TrimSpace(a.Name)
		if name == "" {
			name = strings.TrimSpace(a.Topic) // compatible with older callers that supplied kind/topic
		}
		if name == "" {
			return "", errors.New("name is required for a direction: use its project-supervisor name without a date or kind prefix")
		}
		raw, _ := json.Marshal(map[string]string{"id": c.SessionID, "name": name})
		return s.toolDirectionName(ctx, self, raw) // retains the existing main-only direction-name permission
	}
	if !slices.Contains(titleKinds, a.Kind) {
		return "", fmt.Errorf("kind must be one of %s", strings.Join(titleKinds, " / "))
	}
	topic := titleTopic(a.Topic)
	if topic == "" {
		return "", errors.New("topic is empty")
	}
	by, started := s.titleOf(c.SessionID)
	if by == titleByUser && !a.UserAsked {
		return "", fmt.Errorf("the user named this conversation 「%s」 themselves: rename it only when they ask you to, and then pass user_asked", c.Title)
	}
	if started.IsZero() { // started before titles were kept: today's day is the best guess
		started = time.Now()
	}
	title := titleDay(started, s.location(ctx)) + " | " + a.Kind + " | " + topic
	who := titleByAssistant
	if a.UserAsked {
		who = titleByUser // the user's choice, said in words: it stays like one typed in the app
	}
	if err := s.renameConversation(ctx, c, title, who); err != nil {
		return "", err
	}
	if c.SessionID == self {
		return fmt.Sprintf("Renamed this conversation to 「%s」.", title), nil
	}
	return fmt.Sprintf("Renamed %s to 「%s」.", c.SessionID, title), nil
}

func (s *Service) toolConversationArchive(ctx context.Context, self string, args json.RawMessage, on bool) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(args, &a)
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		if !on { // an archived conversation may be long out of the node's list: the id alone will do
			if id := strings.TrimSpace(a.ID); s.isArchived(id) {
				_, err := s.setArchived(ctx, s.settings(ctx).MachineID, id, false, titleByAssistant)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Unarchived %s.", id), nil
			}
		}
		return "", err
	}
	name := conversationName(c, s.settings(ctx).Main)
	changed, err := s.setArchived(ctx, c.MachineID, c.SessionID, on, titleByAssistant)
	if err != nil {
		return "", err
	}
	switch {
	case !changed && on:
		return fmt.Sprintf("「%s」 was archived already.", name), nil
	case !changed:
		return fmt.Sprintf("「%s」 is not archived.", name), nil
	case !on:
		return fmt.Sprintf("Unarchived 「%s」: it is back in the list.", name), nil
	case c.SessionID == self:
		return "Archived this conversation: it is out of the list (under 已归档). This turn finishes as usual; a new message from the user brings it back.", nil
	}
	return fmt.Sprintf("Archived 「%s」 (under 已归档 in the app; nothing is deleted).", name), nil
}

func (s *Service) toolConversationNew(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		Text  string   `json:"text"`
		Tags  []string `json:"tags"`
		Quote string   `json:"quote"`
		Tier  string   `json:"tier"`
		Pin   bool     `json:"pin"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("text is empty")
	}
	// Validate the tags before anything is started: an over-long or over-count call must not leave a conversation
	// behind it.
	tags, err := normalizeConversationTags(a.Tags)
	if err != nil {
		return "", err
	}
	if err := s.dispatchNewConversationGuard(ctx, self); err != nil {
		return "", err
	}
	from, err := s.relayFrom(ctx, self)
	if err != nil {
		return "", err
	}
	quote, err := s.resolveQuote(ctx, self, a.Quote) // before relayAllowed: a refused quote uses up no rate
	if err != nil {
		return "", err
	}
	var preset ModelTier
	var tierChoice tierPick
	a.Tier = strings.TrimSpace(a.Tier)
	if a.Tier != "" {
		// the conversation starts on the main chat's computer: the tier's candidate is picked for that one
		tierChoice, err = s.pickTierCandidate(ctx, a.Tier, tierFacts{Machine: s.settings(ctx).MachineID})
		if err != nil {
			return "", err
		}
		preset = tierChoice.Tier
	}
	counted := time.Now()
	hop, _, err := s.relayAllowedOn(s.relayRouteFor(ctx, self), self, relayNewKey, counted) // an opening message is a request, never an answer
	if err != nil {
		return "", err
	}
	started := false
	defer func() {
		if !started {
			s.relayRefund(self, relayNewKey, counted) // nothing started: it costs no place (#10099)
		}
	}()
	msg := relayMsg{From: self, FromTitle: from, Tool: "conversation_new", Text: a.Text, Hop: hop, At: time.Now().UTC(), Quote: quote}
	if quote != nil {
		s.keepQuote(ctx, self, *quote) // before it starts: its first turn may hand out work at once
	}
	sctx, cancel := context.WithTimeout(ctx, 100*time.Second)
	defer cancel()
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return "", err
	}
	c, err := s.startSignedIn(sctx, StartParams{Text: prompts.MessageSourceText(relayText(msg), msg.source()), Space: space, Origin: OriginSpawned, OriginRef: self,
		Launcher: preset.Launcher, Model: preset.Model, Effort: preset.Effort, RequireLauncher: a.Tier != ""})
	if err != nil {
		return "", err
	}
	started = true
	b := &s.relays
	b.mu.Lock()
	pinKept := false
	if s.relayLocked(ctx) {
		b.st.Hops[c.SessionID] = relayHop{Hop: hop, At: time.Now().UTC(), From: self} // its report back is the answer
		// Followed until it reports back: a turn that fails or ends without a word tells self (spawned.go, #4669).
		spawn := spawnedConv{Parent: self, At: time.Now().UTC()}
		if a.Pin {
			spawn.PinMachine = c.MachineID
		}
		b.st.Spawned[c.SessionID] = spawn
		pinKept = s.saveRelayLocked() == nil
	}
	b.mu.Unlock()
	s.titleNew(c.MachineID, c.SessionID, time.Now())
	note := quoteNote(quote)
	lang := s.lang(ctx)
	if a.Tier != "" {
		run := ModelTier{Launcher: c.Launcher, Model: c.Model, Effort: c.Effort}
		note += " " + i18n.T(lang, "assistant-conversations.started_on", i18n.Args{"run": run.runs()})
		row := modelRoutingOf(tierChoice, c.MachineID)
		row.SessionID = c.SessionID
		s.recordModelRouting(ctx, row)
		pick := modelPick{MachineID: c.MachineID, Launcher: preset.Launcher, Model: preset.Model, Effort: preset.Effort, At: time.Now().UTC()}
		if err := s.setModelPick(sctx, c.SessionID, &pick); err != nil {
			note += " " + i18n.T(lang, "assistant-conversations.pick_save_failed", i18n.Args{"error": err.Error()})
		}
	}
	if a.Pin && pinKept {
		note += " " + i18n.T(lang, "assistant-conversations.pin_requested")
		// A short first turn can finish before the start call returns its session ID.
		if child, ok := s.session(ctx, c.MachineID, c.SessionID); ok && child.Status == proto.StatusDone {
			s.spawnPinAfterTurn(ctx, c.MachineID, c.SessionID)
		}
	} else if a.Pin {
		note += " " + i18n.T(lang, "assistant-conversations.pin_save_failed")
	}
	if len(tags) > 0 {
		if _, err := s.setConversationTags(sctx, c.MachineID, c.SessionID, tags, titleByAssistant); err != nil {
			note += fmt.Sprintf(" Its tags could not be saved: %v", err)
		} else {
			note += " It is born with tags: " + strings.Join(tags, ", ") + "."
		}
	}
	return fmt.Sprintf("Started conversation %s; it is working on the message now and is named after its first turn. It shows up in the app's sidebar.%s", c.SessionID, note), nil
}

func (s *Service) toolConversationSend(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		ID    string `json:"id"`
		Text  string `json:"text"`
		Quote string `json:"quote"`
		Wake  *bool  `json:"wake"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid conversation_send arguments: %w", err)
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("text is empty")
	}
	later := a.Wake != nil && !*a.Wake
	if later && strings.TrimSpace(a.Quote) != "" {
		return "", errors.New("a quote hands on the owner's request, which must wake its receiver: send it without wake: false")
	}
	c, err := s.ownSpaceConversation(ctx, self, a.ID)
	if err != nil {
		return "", err
	}
	if c.SessionID == self {
		return "", errors.New("that is this conversation: you can't send a message to yourself")
	}
	if c.LogicalPlaceholder && !s.isZeroTurnPlaceholderConversation(ctx, c.MachineID, c.SessionID) {
		// Its active physical segment is gone. Keep the durable conversation readable, but never manufacture a new
		// execution segment or queue a message for an identity that no longer exists.
		return "", logicalRefusal(ctx, "unavailable")
	}
	// The dispatch policy runs after the target is known and before relayAllowed, whose rate-limit bookkeeping a
	// refused send must not consume.
	if err := s.dispatchSendGuard(ctx, self, c.MachineID, c.SessionID, a.Text); err != nil {
		return "", err
	}
	from, err := s.relayFrom(ctx, self)
	if err != nil {
		return "", err
	}
	if later {
		// Explicit wake: false: kept for the receiver, never woken on its own (relay_later.go, #11614).
		n, ok := s.keepRelayLater(c.SessionID, relayMsg{From: self, FromTitle: from, Tool: "conversation_send", Text: a.Text, At: time.Now().UTC()})
		if !ok {
			return "", errors.New("not kept: the relay state could not be read or saved; try again shortly")
		}
		s.spawnReported(self, c.SessionID) // a quiet report still answers the conversation that opened this one
		s.publish(ctx)                     // the receiver shows it waiting above its composer
		return fmt.Sprintf("Kept for %q without waking it (%d kept there): it goes in with the next message that conversation gets for anything else. No reply will come from it; no need to send it again.",
			conversationName(c, s.settings(ctx).Main), n), nil
	}
	fanoutChecked, fanoutAccepted := false, false
	var fanoutPrev time.Time
	var fanoutHadPrev bool
	fanoutAt := time.Now()
	if strings.TrimSpace(a.Quote) == "" {
		admitted, prev, hadPrev := s.admitFanout(ctx, self, c.SessionID, fanoutAt)
		if !admitted {
			// Held by the per-sender wake cap: keep in Later with a durable deferred wake (conversation_fanout.go, #14352).
			n, ok := s.keepRelayLater(c.SessionID, relayMsg{From: self, FromTitle: from, Tool: "conversation_send", Text: a.Text, At: fanoutAt.UTC(), DeferredWake: true})
			if !ok {
				return "", errors.New("not kept: the relay state could not be read or saved; try again shortly")
			}
			due := s.scheduleFanoutWake(ctx, self, c.SessionID, c.MachineID, fanoutAt)
			s.spawnReported(self, c.SessionID)
			s.publish(ctx)
			return fanoutKeptAnswer(conversationName(c, s.settings(ctx).Main), n, due.Sub(fanoutAt)), nil
		}
		fanoutChecked, fanoutPrev, fanoutHadPrev = true, prev, hadPrev
		defer func() {
			if fanoutChecked && !fanoutAccepted {
				s.sendFanout.restore(self, c.SessionID, fanoutPrev, fanoutHadPrev, fanoutAt)
			}
		}()
	}
	quote, err := s.resolveQuote(ctx, self, a.Quote) // before relayAllowed: a refused quote uses up no rate
	if err != nil {
		return "", err
	}
	route := s.relayRouteFor(ctx, self)
	route.queue = true
	now := time.Now()
	if quote != nil && quote.MessageID != "" && now.Sub(quote.At) <= relayHopMemory {
		route.quote = quote.Conversation + "/" + quote.MessageID // the owner's own request: free once per target (#10400)
	}
	adm, err := s.relayAdmit(route, self, c.SessionID, now)
	if err != nil {
		return "", err
	}
	hop, answer := adm.hop, adm.answer
	msg := relayMsg{From: self, FromTitle: from, Tool: "conversation_send", Text: a.Text, Hop: hop, Answer: answer, At: time.Now().UTC(), Quote: quote}
	if !adm.due.IsZero() {
		msg.Due = adm.due.UTC()
	}
	if quote != nil {
		s.keepQuote(ctx, self, *quote) // before it goes in: the receiver may hand out work in the turn it starts
	}
	name := conversationName(c, s.settings(ctx).Main)
	// The hand-over of the owner's words this is, and the one it answers (relay_handover.go, #11699).
	var forget, release func()
	msg.Handover, forget = s.handoverFor(ctx, self, c.SessionID, name, quote)
	defer forget()
	msg.Answers, release = s.handoverToAnswer(self, c.SessionID)
	defer release()
	back := quoteNote(quote)
	if s.unarchiveOnSend(c.MachineID, c.SessionID) {
		back = " It was archived; it is back in the list."
	}
	if !msg.Due.IsZero() {
		n := s.queueRelay(c.SessionID, msg)
		if n == 0 {
			return "", errors.New("not sent: the relay queue could not be read; try again shortly")
		}
		fanoutAccepted = true
		s.publish(ctx)                                 // the receiver shows it waiting above its composer
		s.drainRelays(c.MachineID, []proto.Session{c}) // a timer takes it in when its time comes
		at := msg.Due.In(s.location(ctx))
		return fmt.Sprintf("Queued for %q: %d %s in the last %d minutes already (at most %d). It goes in at about %s (in %d min), once, after what waits before it (%d waiting); no need to send it again.%s",
			name, adm.count, adm.what, int(relayWindow.Minutes()), adm.limit, at.Format("15:04"), int(math.Ceil(time.Until(msg.Due).Minutes())), n, back), nil
	}
	if running := c.Status == proto.StatusWorking || c.Starting; running || s.relayWaits(c.SessionID, msg) {
		n := s.queueRelay(c.SessionID, msg)
		if n > 0 {
			fanoutAccepted = true
			s.spawnReported(self, c.SessionID) // a conversation conversation_new opened reports back (spawned.go)
		}
		s.publish(ctx) // the receiver shows it waiting above its composer
		if running {
			return fmt.Sprintf("%q is running: the message is queued (%d waiting) and goes in when its current turn ends.%s", name, n, back), nil
		}
		s.drainRelays(c.MachineID, []proto.Session{c}) // what may go goes; the rest when the hold for the user ends
		return fmt.Sprintf("%q has messages waiting, or the user is talking in it: the message is queued (%d waiting) and goes in after them.%s", name, n, back), nil
	}
	if err := s.tryDirectRelay(ctx, c.MachineID, c.SessionID, &msg); err != nil {
		if f := relayFailureOf(err); f != relayFailedInDoubt && f != relayFailedNotNow {
			// Back to the sender, nothing queued: a refusal for good, a conversation that is gone, a setup the owner
			// fixes, the manager's own refusal (its model or connection gone, a pick that cannot be read), and, until
			// #11393's decision 1 says otherwise, a conversation at work or away (relayFailedBusy). Fail closed.
			return "", err
		}
		// It could not take the message now (it did not start, its computer is away, the answer did not come): kept
		// in its queue and tried again, under the same once_id, until it goes in (#10194). Never dropped.
		b := &s.relays
		b.mu.Lock()
		if b.failedAt != nil && !relayInDoubt(err) {
			now := time.Now()
			b.failedAt[c.SessionID] = now
			b.recordMsgFailedLocked(c.SessionID, msg, now)
		}
		b.mu.Unlock()
		n := s.queueRelay(c.SessionID, msg)
		if n == 0 {
			return "", err
		}
		fanoutAccepted = true
		s.spawnReported(self, c.SessionID)
		s.publish(ctx)
		s.drainAfter(c.MachineID, c.SessionID, relayRetryGap)
		return fmt.Sprintf("%q could not take the message now (%v): it is queued (%d waiting) and goes in once it can.%s", name, err, n, back), nil
	}
	fanoutAccepted = true
	s.spawnReported(self, c.SessionID)
	return fmt.Sprintf("Sent to 「%s」 (%s).%s", name, c.SessionID, back), nil
}

// relayFrom is the sending conversation's name (its message's hop is relayAllowed's).
func (s *Service) relayFrom(ctx context.Context, self string) (string, error) {
	if self == "" {
		return "", errors.New("only a Persona conversation can do this")
	}
	name := "另一段对话"
	if c, err := s.assistantConversation(ctx, self); err == nil {
		name = conversationName(c, s.settings(ctx).Main)
	}
	return name, nil
}

func (s *Service) queuedFor(id string) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return 0
	}
	return len(b.st.Queue[id])
}

// selfSpace is the space of conversation self ("" the assistant's own, and for a caller that is no conversation), and
// whether it could be told (the assistant's machine lists it or attests its launch directory).
func (s *Service) selfSpace(ctx context.Context, self string) (string, bool) {
	if self == "" {
		return "", true
	}
	machine := s.settings(ctx).MachineID
	c, ok := s.callerSpaceSession(ctx, machine, self)
	if !ok {
		cwd := s.callerCwd(ctx, machine, self)
		return runSpace(proto.Session{Cwd: cwd}), cwd != ""
	}
	return spaceOf(c), true
}

// knownSpace is selfSpace for the tools that reach other conversations. One that can't be told is taken for the
// assistant's own: those tools then reach the assistant's conversations only, never a space's, so a space's
// conversations stay out of reach either way (the worst case is a space's conversation seeing the assistant's list).
func (s *Service) knownSpace(ctx context.Context, self string) (string, error) {
	space, _ := s.selfSpace(ctx, self)
	return space, nil
}

// ownSpaceConversation finds conversation id among those of self's space: the conversations of different spaces (the
// assistant's own included) don't read, rename, archive or message each other.
func (s *Service) ownSpaceConversation(ctx context.Context, self, id string) (proto.Session, error) {
	return s.ownSpaceConversationOnMachine(ctx, s.settings(ctx).MachineID, self, id)
}

func (s *Service) ownSpaceConversationOnMachine(ctx context.Context, machine, self, id string) (proto.Session, error) {
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return proto.Session{}, err
	}
	if row, ok, err := s.deletedConversation(ctx, machine, id); err != nil {
		return proto.Session{}, err
	} else if ok && row.Space == space {
		return proto.Session{}, s.conversationAvailable(ctx, row.Machine, id)
	}
	return s.spaceConversationOnMachine(ctx, machine, id, &space)
}
