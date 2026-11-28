package connectorwire

import (
	"encoding/json"
	"time"
)

// Tier is the inbox's tier a connector's rules decide for a thing (InboxThing.Tier).
type Tier string

// Tiers: needs the user, worth knowing, noise.
const (
	TierAction Tier = "action"
	TierFYI    Tier = "fyi"
	TierNoise  Tier = "noise"
)

// Known says whether t is one of the tiers ("" is not: it leaves the tier to the model).
func (t Tier) Known() bool { return t == TierAction || t == TierFYI || t == TierNoise }

// Done reasons of an InboxThing (the inbox item's done_by).
const (
	DoneRead    = "read"    // over without the user: merged, closed, the request withdrawn
	DoneReplied = "replied" // the user answered (here or elsewhere)
)

// InboxThing is one thread a connector thinks may need the user. The hub turns things into inbox items: one open
// item per thread gathers its later moves until the user deals with it; a move after that starts a new item. What
// happens to an item afterwards (triage, the AI twin, done, kept for a while) is the hub's, the same for every source.
type InboxThing struct {
	// Thread is the connector's stable key of the thread ("example-org/example-repo#12").
	Thread string `json:"thread"`
	// Kind is the connector's kind of thing ("review_requested"): prompts/inbox/kinds/<id>.md tells the models
	// what it means; Label is the row's tag for people ("review requested").
	Kind    string `json:"kind"`
	Label   string `json:"label,omitempty"`
	Title   string `json:"title,omitempty"`
	Sender  string `json:"sender,omitempty"`  // who made the newest move
	Preview string `json:"preview,omitempty"` // the newest move in a line
	Context string `json:"context,omitempty"` // the thread as text, for triage, drafting and the AI twin
	// At is when the newest move happened; Last marks it, and a different Last adds to the open item.
	At   time.Time `json:"at,omitempty"`
	Last string    `json:"last,omitempty"`
	URL  string    `json:"url,omitempty"` // the thread on the web, opened in the app
	// ThreadState is the observed lifecycle of a GitHub pull request: open, closed or merged.
	ThreadState string `json:"thread_state,omitempty"`
	// OpenURL is a link the system opens instead (sms:…: "Reply in Messages"), kept apart from URL.
	OpenURL string `json:"open_url,omitempty"`
	// ReplyTo says where a reply goes and who sees it ("Comment on example-org/example-repo#12 (anyone who can see the repo)");
	// CanReply: Reply works for this thread.
	ReplyTo  string `json:"reply_to,omitempty"`
	CanReply bool   `json:"can_reply,omitempty"`
	// Avatar is the thread's picture (docs/protocol/protocol.md "Avatars"): one of the connector's own media refs
	// (Threads.Media; the ref its ThreadChat.Avatar has) or an https address the hub fetches, either always naming the
	// same bytes. The other person of a private chat, a group's own; the Inbox row draws it, the initial without one.
	// At most 2048 bytes (extwire.AvatarValueMost); a longer one, or an http:// address, is none.
	Avatar string `json:"avatar,omitempty"`
	// Tier is the tier the connector's rules decide (action, fyi, noise): the item gets it without a model. "" leaves it
	// to the model (a human's ordinary comment). Why says which rule, for people ("Rule: a human @-mentions you").
	Tier Tier   `json:"tier,omitempty"`
	Why  string `json:"why,omitempty"`
	// Rules are the user's own triage rules for this thing, in their words (a source's or an account's): when set
	// the model gets them instead of the hub's inbox.rules; the default criteria stay underneath. "" uses inbox.rules.
	Rules string `json:"rules,omitempty"`
	// Done ends the open item (DoneRead, DoneReplied): the thread is over; Why says how when it is not obvious
	// ("GitHub: succeeded later").
	Done string `json:"done,omitempty"`
	// Read: seen somewhere else (the notification read on GitHub); an item that does not need the user is done.
	Read bool `json:"read,omitempty"`
	// Tags are what the thing is about, for muting and rules: its repository, chat, sender (TagRepo, TagChat,
	// TagSender: the row's menu offers to mute those), a CI failure's branch. The hub mutes by them for every source.
	Tags []Tag `json:"tags,omitempty"`
	// Extra is what an item the hub used to make itself had besides the generic fields (ThingExtra): only a connector
	// taking over one of the hub's own (MigratableConnectors) sends it, and only for it does the hub read it.
	Extra *ThingExtra `json:"extra,omitempty"`
	// Group makes the thing a batch of a conversation's new messages to relate to its events (a connector that
	// declares CapGroup, docs/assistant/inbox.md "Event grouping"): the hub never makes it an item. Thread, Context (the new messages'
	// text), Rules and Last go with it.
	Group *ThingGroup `json:"group,omitempty"`
	// ID and Meta are an event's (Grouper.GroupAppend's answer): the item's id, and what the connector keeps about the
	// event (its messages…), stored and handed back as it is: the hub never reads it.
	ID   string          `json:"id,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
	// Parent is what the thing grew out of, when the connector knows (a reply to a todo's card, a thread an agent
	// opened): the item keeps it, unless the user set another. Optional; the hub only keeps and lists it.
	Parent *ThingParent `json:"parent,omitempty"`
}

// ThingParent is a thing's parent: kind "inbox" (an item's id), "todo" (a todo's id), "session" (a coding Agent's
// "<machine>/<session>") or "agent" (a personal Agent's "<source>:<sessionKey>").
type ThingParent struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// ThingGroup is a batch to relate: its id (the same each time the same batch is handed again) and the new messages'
// ids, opaque to the hub.
type ThingGroup struct {
	Batch string   `json:"batch"`
	New   []string `json:"new"`
}

// The connector's tools for relating (GroupToolCall.Name): the messages before one, around one, and a search.
const (
	GroupMessagesBefore = "messages_before"
	GroupMessagesAround = "messages_around"
	GroupSearch         = "search"
)

// GroupToolCall is one of the connector's tools called for the model, in a conversation; Args as the model wrote them.
type GroupToolCall struct {
	Thread string          `json:"thread"`
	Name   string          `json:"name"`
	Args   json.RawMessage `json:"args,omitempty"`
}

// GroupAppend adds messages to one event: an existing one (Event, with its Meta) or a new one (Event "", Meta null),
// which may continue a done one. The answer is the event as a thing, with its ID and Meta.
type GroupAppend struct {
	Thread    string          `json:"thread"`
	Event     string          `json:"event"`
	Meta      json.RawMessage `json:"meta"`
	Messages  []string        `json:"messages"`
	Continues string          `json:"continues,omitempty"`
}

// GroupEvent is an event as the connector's fallback reads it.
type GroupEvent struct {
	ID   string          `json:"id"`
	Meta json.RawMessage `json:"meta"`
	Done bool            `json:"done"`
}

// GroupFallback asks the connector to group a batch by structure (replies and quotes, topics, one sender's run,
// gaps in time), with the conversation's events.
type GroupFallback struct {
	Thread string       `json:"thread"`
	Batch  string       `json:"batch"`
	Events []GroupEvent `json:"events"`
}

// GroupFallbackResult puts each new message in an existing event (Assign) or a new one (New, by its own key, with its
// rule tier and a title).
type GroupFallbackResult struct {
	Assign []GroupAssign `json:"assign"`
	New    []GroupNew    `json:"new"`
}

type GroupAssign struct {
	Message string `json:"message"`
	Event   string `json:"event"`
}

type GroupNew struct {
	Key      string   `json:"key"`
	Messages []string `json:"messages"`
	RuleTier Tier     `json:"rule_tier,omitempty"`
	Title    string   `json:"title,omitempty"`
}

// MigratableConnectors are the hub's own connectors a plugin may take over while they move out of the hub (Feishu
// through the Lark extension, docs/plans/plan-extensions.md "Step 3"; GitHub, #2830): a plugin's connector with the same id replaces
// the hub's while the plugin is on, and its inbox things may carry the items' own blocks (ThingExtra).
var MigratableConnectors = []string{"lark", "github"}

// ThingExtra is an inbox item's own fields that the generic thing has no place for: the item keeps the shape the
// apps know (the Feishu item's lark / doc / moment block, its avatar and count) instead of the generic connector block.
type ThingExtra struct {
	Source      string    `json:"source,omitempty"`
	Account     string    `json:"account,omitempty"`
	AccountName string    `json:"account_name,omitempty"`
	LegacyID    string    `json:"legacy_id,omitempty"` // the id the hub would have given the item itself
	Avatar      string    `json:"avatar,omitempty"`
	Count       int       `json:"count,omitempty"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	RuleTier    string    `json:"rule_tier,omitempty"`
	// The item's block, as the hub's own item had it (one of them).
	Lark   json.RawMessage `json:"lark,omitempty"`
	Doc    json.RawMessage `json:"doc,omitempty"`
	Moment json.RawMessage `json:"moment,omitempty"`
}

// HasBlock says whether the extra carries one of the items' own blocks (lark, doc, moment): only those keep the
// shape the hub's own items had. Extra with a rule tier alone (a connector taking over GitHub) is a generic item's.
func (e *ThingExtra) HasBlock() bool {
	return e != nil && (len(e.Lark) > 0 || len(e.Doc) > 0 || len(e.Moment) > 0)
}

// Tag is one thing an inbox thing is about: Key (TagRepo…), the Value it matches by (an id), the Name people read.
type Tag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Name  string `json:"name,omitempty"`
}

// Tag keys. The user can mute by the first three (the inbox row's "Mute this repository" / "chat" / "sender").
const (
	TagRepo   = "repo"
	TagChat   = "chat"
	TagSender = "sender"
	TagBranch = "branch"
	// TagExternalObject is a canonical source object URL when the row opens a related view instead.
	TagExternalObject = "external_object"
)

// MentionUserID is a person's user ID on the connector's own service, as verified for a real @ mention.
type MentionUserID string

// InboxMention binds an exact @name in InboxReply.Text to a verified external user ID.
type InboxMention struct {
	ID   MentionUserID `json:"id"`
	Name string        `json:"name"`
}

// InboxReply is a reply to one of a connector's inbox items.
type InboxReply struct {
	Thread string `json:"thread"`
	ItemID string `json:"item_id,omitempty"` // the hub's inbox item
	Last   string `json:"last,omitempty"`    // the newest move the item holds (InboxThing.Last): what the reply answers
	Text   string `json:"text,omitempty"`
	// Mentions lists explicit @name bindings (verified user ID + exact display name) for connectors that support real @ mentions.
	Mentions []InboxMention `json:"mentions,omitempty"`
	// ClientID is the app's id for the send: a retry with the same one goes out once (empty: the connector derives one).
	ClientID string `json:"client_id,omitempty"`
	// Changes move a document row's status first (a document item's "Change status", the AI twin's proposal); Text may then be empty.
	Changes []StatusChange `json:"changes,omitempty"`
}

// StatusChange moves one row's status column to a value it offers (the hub's DocChange).
type StatusChange struct {
	Ref   string `json:"ref"`
	Field string `json:"field"`
	Value string `json:"value"`
}
