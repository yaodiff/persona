package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the Feishu chat's records the core and the lark module both name (internal/larkhost, #13404) ----
//
// The chat's state (the settings row bots.feishu.state) is the lark module's. These are the parts of it the core still
// reads or writes while the standing board, the workbench pin and the companion bots are the core's, and what the module asks
// the core about a chat's target. Field names and JSON tags are the state's own: the stored bytes do not change.

// ChatTarget is where a message of the chat goes: the main conversation (whichever it is when the message is sent),
// another Persona conversation, or a coding agent.
type ChatTarget struct {
	Kind      string `json:"kind"` // ChatMain | ChatPersona | ChatAgent
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// TodoID is set only on a to-do reminder's cards: a reply still goes to the conversation, with that to-do as data.
	TodoID string `json:"todo_id,omitempty"`
	// Space is the domain space slug when starting a new conversation for a space-bound topic.
	Space string `json:"space,omitempty"`
}

// The kinds of a ChatTarget.
const (
	ChatMain    = "main"
	ChatPersona = "persona"
	ChatAgent   = "agent"
)

// Agent says whether t is a coding agent.
func (t ChatTarget) Agent() bool { return t.Kind == ChatAgent }

// ChatResolved is a target as it is now: the machine and conversation, and what to call it. IsNew: no conversation
// yet, the message starts one.
type ChatResolved struct {
	Machine     string
	MachineName string
	Session     proto.Session
	Label       string
	Secondary   string
	Main        bool
	IsNew       bool
}

// NudgeProgress is one kind of nudge's durable delivery state. The ordinary nudge keeps its original flat fields
// (ChatCardRef) for wire compatibility; the deadline nudge has its own record so either one cannot suppress the other.
type NudgeProgress struct {
	NudgedAt  time.Time `json:"nudged_at,omitzero"`
	SkippedAt time.Time `json:"skipped_at,omitzero"`
	Tries     int       `json:"tries,omitempty"`
	TriedAt   time.Time `json:"tried_at,omitzero"`
}

// Settled: the nudge went out, or was given up.
func (p NudgeProgress) Settled() bool { return !p.NudgedAt.IsZero() || !p.SkippedAt.IsZero() }

// ChatCard is a card asking an interaction, open in the chat.
type ChatCard struct {
	MessageID string
	Thread    string            // the topic the card was posted into (the run anchor it replies to); "" in the root chat
	ChatID    string            // the group chat the card is in (a topic's card); "" in the owner's DM
	Ix        proto.Interaction // what it asks (to close it when it ended unseen)
	At        time.Time         // when it was posted: the latest waiting card is the one a typed answer answers
	NudgedAt  time.Time         // when it was nudged under, zero before (the lark module's nudge.go)
	SkippedAt time.Time         // when its nudge was given up (ChatCardRef.SkippedAt)
	Tries     int               // nudge sends tried
	TriedAt   time.Time         // when the first nudge send was tried
	// DeadlineNudge is independent of the ordinary nudge above, including retries and the one-shot marker.
	DeadlineNudge NudgeProgress
}

// NudgeSettled: the card has had its nudge, or the nudge was given up; either way there is no more to do.
func (c ChatCard) NudgeSettled() bool { return !c.NudgedAt.IsZero() || !c.SkippedAt.IsZero() }

// ChatCardRef is a card kept in the chat's state: its message and the interaction it asks.
type ChatCardRef struct {
	MessageID       string    `json:"message_id"`
	Kind            string    `json:"kind,omitempty"`
	MachineID       string    `json:"machine_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	CreatedAt       time.Time `json:"created_at,omitzero"` // occurrence this message asks, for safe same-ID replacement handling
	Thread          string    `json:"thread,omitempty"`    // the topic it was posted into (ChatCard.Thread)
	ChatID          string    `json:"chat_id,omitempty"`   // the group chat of that topic (ChatCard.ChatID); "" in the owner's DM
	At              time.Time `json:"at"`
	ExpiresAt       time.Time `json:"expires_at,omitzero"` // the decision deadline when posted; a live prepared card may extend it
	ApprovalBatchID string    `json:"approval_batch_id,omitempty"`
	ApprovalItemID  string    `json:"approval_item_id,omitempty"`
	OwnerProposal   bool      `json:"owner_proposal,omitempty"`
	Resumable       bool      `json:"resumable,omitempty"`
	NoExpiry        bool      `json:"no_expiry,omitempty"`
	// NudgedAt is when the card was nudged under (the lark module's nudge.go): once set, never again, also after a
	// restart.
	NudgedAt time.Time `json:"nudged_at,omitzero"`
	// SkippedAt is when the nudge for this card was given up (not due any more when nudges came on again, the chat
	// refused it, too many tries): never nudged, and not shown as nudged. Tries and TriedAt are the sends tried and
	// when the first went out, for the retry limits.
	SkippedAt     time.Time     `json:"nudge_skipped_at,omitzero"`
	Tries         int           `json:"nudge_tries,omitempty"`
	TriedAt       time.Time     `json:"nudge_tried_at,omitzero"`
	DeadlineNudge NudgeProgress `json:"deadline_nudge,omitzero"`
}

// ChatBoardRef is the standing "waiting for you" card: its message and when it was sent.
type ChatBoardRef struct {
	MessageID string    `json:"message_id"`
	At        time.Time `json:"at"`
}

// CompanionBot is another Lark bot the workbench links to.
type CompanionBot struct {
	Kind  string `json:"kind"`   // the core's CompanionInbox, CompanionNotify or CompanionOther
	Name  string `json:"name"`   // on its button
	Icon  string `json:"icon"`   // an emoji before the name
	AppID string `json:"app_id"` // its Lark app: the button opens the chat with it
}

// ChatBook is the chat's records as the core's standing board, workbench pin and companion bots read and write them, under
// the chat's lock (larkhost.Layer.Book). Every write is saved with the chat's state.
type ChatBook interface {
	// Loaded: the chat's state was read (nothing is saved before it is).
	Loaded() bool
	// Card is the card asking interaction id; KeepCard records it (nil forgets it; one with no message yet is only
	// claimed, not saved); Cards are all of them.
	Card(id string) (ChatCard, bool)
	KeepCard(id string, c *ChatCard)
	Cards() map[string]ChatCard
	// CardRef is the card of interaction id as the state keeps it; ProposalFor is the owner-action proposal card the
	// message parent is, "" for none or for more than one.
	CardRef(id string) (ChatCardRef, bool)
	ProposalFor(parent string) string
	// Board is the standing board's card (nil: none), SetBoard records it; Pairing is the pairing the state belongs
	// to (nil: not recorded yet).
	Board() *ChatBoardRef
	SetBoard(ref *ChatBoardRef)
	Pairing() *string
	Companions() []CompanionBot
	SetCompanions(bots []CompanionBot)
}

// ChatTodo is a to-do bound to a topic of the chat, as the topic's messages are taken for it.
type ChatTodo struct {
	ID          string
	Title       string
	ThreadTitle string // the topic's title as the to-do keeps it
	Done        bool
}

// ChatTodoSource is where a to-do the chat opened came from: the request's channel, chat and message, the bot's first
// message for a to-do bound to no topic (Anchor), and the bot's message that started its run (Handoff). It is the
// core's TodoSource as the chat fills it in.
type ChatTodoSource struct {
	Channel   string
	ChatID    string
	MessageID string
	Anchor    string
	Handoff   string
}

// ChatOpenTodo is a to-do the chat opens: its title and notes, the main conversation that takes it (Origin), the topic
// it is bound to (Topic, ChatID, ThreadID), where it came from (Source, nil for a topic the owner started), whether it
// is named again later from Text (Retitle), and where it was asked, for the log (Where).
type ChatOpenTodo struct {
	Title, Notes, Origin string
	Topic                bool
	ChatID, ThreadID     string
	Source               *ChatTodoSource
	Retitle              bool
	Text                 string
	Where                string
}

// FeishuTopicMayOpenTodo holds the pairing of a run started in an unbound main_decides topic.
const FeishuTopicMayOpenTodo = "topic_may_open_todo"

// FeishuReadChatKey is the persisted run extension for the chat that asked /read.
const FeishuReadChatKey = "read_chat"

// What noting a topic message as its to-do's decision came to (larkhost.Host.TopicDecision).
const (
	DecisionNotNoted = ""          // not noted (no store, or a failure that was logged): the words still go on
	DecisionNoted    = "noted"     // noted now
	DecisionSeen     = "seen"      // this message was noted before
	DecisionTodoGone = "todo_gone" // the to-do is gone
)
