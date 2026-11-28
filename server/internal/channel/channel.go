// Package channel is what every way of talking to Persona has in common (docs/extensions/channels.md): the Persona apps, the
// WeChat bot, the Feishu bot, /agent in Feishu, voice. A channel brings the owner's message into a conversation, the
// assistant follows the turn and sends its answer back the same way (the hub carries the messages); what the
// conversation's agents ask goes there too.
//
// This package is plain data and plain rules, with no hub in it: the hub, the assistant, channel programs and tests all
// use it. The
// turn's rules (Check, Answer), splitting an answer (Split), asking and answering in text (AskText, ReadAnswer) were
// written twice before (hub/bots_runs.go, wechat/turns.go); this is the one copy.
package channel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Kind is what sort of way in a channel is.
type Kind string

const (
	KindApp     Kind = "app"     // Persona's own apps: iOS, Mac, Watch, Android, web, Siri
	KindIM      Kind = "im"      // a bot in a chat app: WeChat, the Feishu bot, later Telegram, Slack, Discord
	KindMention Kind = "mention" // called in someone else's chat, answering as the owner: /agent in Feishu
	KindVoice   Kind = "voice"   // a voice conversation
)

// Source is where a channel comes from.
type Source string

const (
	SourceBuiltin Source = "builtin" // the hub's own
	SourcePlugin  Source = "plugin"  // a plugin's contribution
	SourceMarket  Source = "market"  // a plugin in a market that would contribute it, not installed
)

// State is how a channel is doing.
type State string

const (
	StateConnected    State = "connected"     // bound, taking messages
	StateNotConnected State = "not_connected" // not bound / not set up
	StateError        State = "error"
	StateDisabled     State = "disabled"
	StateNotInstalled State = "not_installed"
)

// Bind is how a channel is bound to its owner.
type Bind string

const (
	BindNone   Bind = "none"   // nothing to bind (the apps, voice)
	BindQR     Bind = "qr"     // scan a QR code in the chat app
	BindCode   Bind = "code"   // the app shows a code, sent to the bot from the chat app
	BindFields Bind = "fields" // credentials, maybe then a code
	BindConfig Bind = "config" // only in hub.json
)

// Format is what the answer looks like when it reaches the owner: the hub always sends Markdown, the channel turns it
// into what its chat app shows; a plain one has Persona write plain text.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatPlain    Format = "plain"
)

// Asks is how what an agent asks reaches the channel.
type Asks string

const (
	AsksNone    Asks = "none"
	AsksText    Asks = "text"    // the hub writes it as text and reads the owner's next message as the answer
	AsksButtons Asks = "buttons" // the channel draws it (the Feishu bot's cards)
)

// Capabilities are what a channel can do (docs/extensions/channels.md "Capabilities"). Taking text is a given.
type Capabilities struct {
	ImagesIn bool `json:"images_in,omitempty"`
	// ImagesOut: it can send pictures; an answer's pictures go in SendParams.Images (at most ImagesMost of ImageMost).
	ImagesOut bool   `json:"images_out,omitempty"`
	FilesIn   bool   `json:"files_in,omitempty"`
	VoiceIn   bool   `json:"voice_in,omitempty"`
	Format    Format `json:"format,omitempty"`
	// MaxText is how many characters one message holds (0: no limit): the hub splits an answer to fit.
	MaxText int  `json:"max_text,omitempty"`
	Asks    Asks `json:"asks,omitempty"`
	// Push: it can send to the owner unprompted (pushes, sync, what is asked).
	Push bool `json:"push,omitempty"`
	// ReplyOnly: it only replies to incoming messages or triggers and never sends unsolicited proactive messages
	// (without reply_to); testing checks connectivity instead of calling channel.send.
	ReplyOnly bool `json:"reply_only,omitempty"`
	Typing    bool `json:"typing,omitempty"`
	// Progress: it can post a message and change it (a progress card the answer goes into).
	Progress bool `json:"progress,omitempty"`
	// Threads: a reply to one of the bot's messages goes to that message's conversation (the Feishu bot); an
	// extension channel's message carrying Thread goes to that thread's own conversation.
	Threads bool `json:"threads,omitempty"`
}

// Account is who a channel is bound to, for people: no phone number, no e-mail.
type Account struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// ChatKind is what kind of chat a channel message came from.
type ChatKind string

const (
	ChatKindDirect ChatKind = "direct"
	ChatKindGroup  ChatKind = "group"
	ChatKindTopic  ChatKind = "topic"
)

// Known returns the supported chat kind, or empty for an unclassified message.
func (k ChatKind) Known() ChatKind {
	switch kind := ChatKind(strings.TrimSpace(string(k))); kind {
	case ChatKindDirect, ChatKindGroup, ChatKindTopic:
		return kind
	default:
		return ""
	}
}

// ThreadMode controls how a channel maps IM chats and topics to Persona conversations when integrations_v2 is enabled.
type ThreadMode string

const (
	ThreadModeChannelDefault   ThreadMode = "channel_default"
	ThreadModePerGroupAndTopic ThreadMode = "per_group_and_topic"
	ThreadModePerTopic         ThreadMode = "per_topic"
)

// MaxThreadIdleHours bounds thread_idle_hours so time.Duration conversion never overflows (10 years).
const MaxThreadIdleHours = 3650 * 24

// Check validates an explicit thread_mode setting value.
func (m ThreadMode) Check() error {
	switch m {
	case ThreadModeChannelDefault, ThreadModePerGroupAndTopic, ThreadModePerTopic:
		return nil
	default:
		return fmt.Errorf("thread_mode %q: channel_default, per_group_and_topic or per_topic", m)
	}
}

// Effective preserves an explicit choice. Thread-capable plugins inherit topic routing in the experiment.
func (m ThreadMode) Effective(inheritTopics bool) ThreadMode {
	if m == "" && inheritTopics {
		return ThreadModePerTopic
	}
	if m == "" {
		return ThreadModeChannelDefault
	}
	return m
}

// CheckThreadIdleHours validates a channel's thread_idle_hours setting (0 means the default 30-day timeout).
func CheckThreadIdleHours(hours int) error {
	if hours < 0 {
		return errors.New("thread_idle_hours must be 0 or greater")
	}
	if hours > MaxThreadIdleHours {
		return fmt.Errorf("thread_idle_hours must be at most %d", MaxThreadIdleHours)
	}
	return nil
}

// Target is where a channel's messages go.
type Target struct {
	Kind      string `json:"kind"` // main | new | conversation | agent
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

const (
	TargetMain         = "main"
	TargetNew          = "new"
	TargetConversation = "conversation"
	TargetAgent        = "agent" // the Feishu bot's /agents
)

// Check says whether t is a target the app may set (agent is the Feishu bot's own, through its commands).
func (t Target) Check() error {
	switch t.Kind {
	case TargetMain, TargetNew:
		if t.MachineID != "" || t.SessionID != "" {
			return fmt.Errorf("target %s has no conversation", t.Kind)
		}
	case TargetConversation:
		if t.MachineID == "" || t.SessionID == "" {
			return errors.New("target conversation needs machine_id and session_id")
		}
	default:
		return fmt.Errorf("target kind %q: main, new or conversation", t.Kind)
	}
	return nil
}

// Feature is one part of a channel that is switched on and off on its own.
type Feature struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	On          bool   `json:"on"`
}

// The features every plugin channel has, as its capabilities allow (docs/extensions/channels.md "features").
const (
	FeatureAsks    = "asks"
	FeatureAllAsks = "all_asks"
	FeaturePushes  = "pushes"
	FeatureSync    = "sync"
)

// PluginFeatures are a plugin channel's features with what is on, in order, their names in l; only those its
// capabilities allow.
func PluginFeatures(l i18n.Lang, c Capabilities, on map[string]bool) []Feature {
	var out []Feature
	add := func(id string) {
		out = append(out, Feature{ID: id, Title: i18n.T(l, "channel-channel."+id+"_title"),
			Description: i18n.T(l, "channel-channel."+id+"_description"), On: on[id]})
	}
	if c.Asks == AsksText || c.Asks == AsksButtons {
		add(FeatureAsks)
		add(FeatureAllAsks)
	}
	if c.Push && !c.ReplyOnly {
		add(FeaturePushes)
		add(FeatureSync)
	}
	return out
}

// DefaultFeatures are what a plugin channel starts with: answering what its conversations ask.
var DefaultFeatures = map[string]bool{FeatureAsks: true}

// View is a channel as the app sees it (GET /api/v1/channels).
type View struct {
	ID              string       `json:"id"`
	Name            string       `json:"name"`
	Kind            Kind         `json:"kind"`
	Source          Source       `json:"source"`
	Plugin          string       `json:"plugin,omitempty"`
	Market          string       `json:"market,omitempty"` // for a market's: the market's id
	Installed       bool         `json:"installed"`
	Enabled         bool         `json:"enabled"`
	State           State        `json:"state"`
	Error           string       `json:"error,omitempty"`
	Account         *Account     `json:"account,omitempty"`
	Description     string       `json:"description,omitempty"`
	Capabilities    Capabilities `json:"capabilities"`
	Target          *Target      `json:"target,omitempty"`
	ThreadMode      ThreadMode   `json:"thread_mode,omitempty"`
	ThreadIdleHours int          `json:"thread_idle_hours,omitempty"`
	Features        []Feature    `json:"features,omitempty"`
	Bind            Bind         `json:"bind"`
	// Surfaces are the app channel's ends (iOS, Mac, …), for people.
	Surfaces    []string  `json:"surfaces,omitempty"`
	LastEventAt time.Time `json:"last_event_at,omitzero"`
	// ReadOnly: its settings are elsewhere (hub.json, the plugin's page): the app shows them and links there.
	ReadOnly bool `json:"read_only,omitempty"`
}

// TargetOverride modes describe why incoming messages bypass the channel's default target.
const (
	// TargetOverrideTopics: group topic routing sends each topic to its own conversation.
	TargetOverrideTopics = "topics"
	// TargetOverrideGroupsAndTopics: each group and each topic has its own conversation.
	TargetOverrideGroupsAndTopics = "groups_and_topics"
	// TargetOverrideThreads: per-thread routing sends each thread to its own conversation.
	TargetOverrideThreads = "threads"
)

// TargetOverride explains that threaded or topic messages currently route to dedicated conversations ahead of the
// channel's default target, so the default target only applies to messages outside those threads or topics.
type TargetOverride struct {
	Mode string `json:"mode"`
}

// Detail is one channel's page (GET /api/v1/channels/{id}).
type Detail struct {
	View
	Waiting       Waiting        `json:"waiting"`
	Conversations []Conversation `json:"conversations,omitempty"`
	// SettingsURL: the channel has a full page of its own (the Feishu bot's), linked from this one — an app route
	// ("settings/connectors/feishu-bot"), not a web address.
	SettingsURL string `json:"settings_url,omitempty"`
	// TargetOverride: set when topic or thread routing currently takes precedence over the channel's default target;
	// omitted when inactive so older clients and channels without overrides are unaffected.
	TargetOverride *TargetOverride `json:"target_override,omitempty"`
}

// Waiting is how many answers and questions a channel waits on.
type Waiting struct {
	Answers int `json:"answers"`
	Asks    int `json:"asks"`
}

// Conversation is one a channel's messages went to lately.
type Conversation struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Title     string    `json:"title,omitempty"`
	At        time.Time `json:"at"`
}

// The built-in channels' ids.
const (
	App       = "app"
	Feishu    = "feishu"
	LarkAgent = "lark_agent"
)

var idPattern = lazyre.New(`^[a-z][a-z0-9_]{0,31}$`)
var uuidHead = lazyre.New(`^[0-9a-f]{8}$`)

// ValidID says whether id can name a channel: lowercase letters, digits and _, a letter first, at most 32 — and not
// eight hex digits, which is how the apps' own client_msg_ids (UUIDs) begin.
func ValidID(id string) bool { return idPattern.MatchString(id) && !uuidHead.MatchString(id) }

// Reserved are the built-in channels' ids: no plugin takes them.
var Reserved = []string{App, Feishu, LarkAgent, "voice", "siri", "watch", "web"}

// ClientMsgID is the client_msg_id a channel's message goes into a conversation with: the source mark the apps read
// (docs/extensions/channels.md "Source markers"), and the hub's once-only guard for a message the chat app delivers again.
func ClientMsgID(channel, messageID string) string { return channel + "-" + messageID }

// FromClientMsgID is the channel a client_msg_id says its message came from, when it is one of known ("" when not).
func FromClientMsgID(id string, known []string) string {
	head, _, ok := strings.Cut(id, "-")
	if !ok || head == "" {
		return ""
	}
	for _, k := range known {
		if k == head {
			return k
		}
	}
	return ""
}

// Item is a contribution to the channel extension point (docs/extensions/channels.md "Manifest").
type Item struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      Kind   `json:"kind"`
	Connector string `json:"connector,omitempty"`
	// Bind is how its connector signs in, for the app's words: qr, code or fields (the default).
	Bind         Bind         `json:"bind,omitempty"`
	Description  string       `json:"description,omitempty"`
	Capabilities Capabilities `json:"capabilities"`
}

// Validate checks a contribution on its own (the ids other plugins use are the registry's).
func (it Item) Validate() error {
	switch {
	case !ValidID(it.ID):
		return fmt.Errorf("channel id %q: a-z0-9_, a letter first, at most 32, not 8 hex digits", it.ID)
	case slices.Contains(Reserved, it.ID):
		return fmt.Errorf("channel id %q is the hub's own", it.ID)
	case strings.TrimSpace(it.Name) == "" || utf8.RuneCountInString(it.Name) > 40:
		return fmt.Errorf("channel %s: name, 1 to 40 characters", it.ID)
	case it.Kind != KindIM && it.Kind != KindMention:
		return fmt.Errorf("channel %s: kind is im or mention", it.ID)
	case it.Connector != "" && !idPattern.MatchString(it.Connector):
		return fmt.Errorf("channel %s: connector %q is not a connector id", it.ID, it.Connector)
	case it.Bind != "" && it.Bind != BindQR && it.Bind != BindCode && it.Bind != BindFields:
		return fmt.Errorf("channel %s: bind is qr, code or fields", it.ID)
	case it.Bind != "" && it.Connector == "":
		return fmt.Errorf("channel %s: bind without a connector to sign in with", it.ID)
	}
	return it.Capabilities.Validate()
}

// Validate checks the values that have a closed set.
func (c Capabilities) Validate() error {
	switch c.Format {
	case "", FormatMarkdown, FormatPlain:
	default:
		return fmt.Errorf("capabilities.format %q: markdown or plain", c.Format)
	}
	switch c.Asks {
	case "", AsksNone, AsksText, AsksButtons:
	default:
		return fmt.Errorf("capabilities.asks %q: none, text or buttons", c.Asks)
	}
	if c.MaxText < 0 {
		return errors.New("capabilities.max_text below 0")
	}
	if c.MaxText > 0 && c.MaxText < 200 {
		return errors.New("capabilities.max_text below 200: an answer would come in too many pieces")
	}
	if c.ReplyOnly && c.Push {
		return errors.New("capabilities.reply_only cannot be combined with capabilities.push")
	}
	return nil
}

// Normalized fills in the defaults: Markdown, no asks.
func (c Capabilities) Normalized() Capabilities {
	if c.Format == "" {
		c.Format = FormatMarkdown
	}
	if c.Asks == "" {
		c.Asks = AsksNone
	}
	return c
}

// --- the plugin protocol's channel namespace (docs/extensions/channels.md "Protocol") ---

// Namespace is the channel extension point's: its methods are channel.<method>.
const Namespace = "channel"

// Methods the program calls on the hub, and the hub on the program.
const (
	MReceive = "receive" // program → hub: ReceiveParams
	MStatus  = "status"  // program → hub: StatusParams (a channel without a connector)
	MSend    = "send"    // hub → program: SendParams → SendResult
	MTyping  = "typing"  // hub → program: TypingParams
	MEdit    = "edit"    // hub → program: EditParams
)

// CodeCannotSend: there is nowhere to send to yet (WeChat before the owner's first message).
const CodeCannotSend = "cannot_send"

// Message is the owner's message, as a channel hands it to the hub.
type Message struct {
	ID          string   `json:"id"`
	Text        string   `json:"text,omitempty"`
	Chat        string   `json:"chat,omitempty"`
	Topic       string   `json:"topic,omitempty"`
	ChatID      string   `json:"chat_id,omitempty"`
	ChatKind    ChatKind `json:"chat_kind,omitempty"`
	TopicID     string   `json:"topic_id,omitempty"`
	Context     string   `json:"context,omitempty"`
	Owner       *bool    `json:"owner,omitempty"`
	Images      []Blob   `json:"images,omitempty"`
	Files       []Blob   `json:"files,omitempty"`
	Quote       string   `json:"quote,omitempty"`
	Forward     string   `json:"forward,omitempty"`
	ReplyTo     string   `json:"reply_to,omitempty"`
	Thread      string   `json:"thread,omitempty"` // the chat app's thread, an opaque key (Capabilities.Threads)
	Unsupported []string `json:"unsupported,omitempty"`
}

// Blob is a picture or a file (Data is base64 on the wire).
type Blob struct {
	Name string `json:"name,omitempty"`
	MIME string `json:"mime"`
	Data []byte `json:"data"`
}

// Limits on what a message brings (docs/extensions/channels.md "Protocol").
const (
	ImagesMost = 4
	ImageMost  = 3 << 20
	FileMost   = 50 << 20
)

// Empty says whether m has nothing to send on.
func (m Message) Empty() bool {
	return strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.Context) == "" && strings.TrimSpace(m.Chat) == "" && strings.TrimSpace(m.Topic) == "" &&
		len(m.Images) == 0 && len(m.Files) == 0 && m.Quote == "" && m.Forward == "" && len(m.Unsupported) == 0
}

// Out kinds: what a message the hub sends is.
const (
	OutAnswer = "answer"
	OutNote   = "note"
	OutAsk    = "ask"
	OutPush   = "push"
)

type (
	ReceiveParams struct {
		Contribution string  `json:"contribution,omitempty"`
		Message      Message `json:"message"`
	}
	StatusParams struct {
		Contribution string   `json:"contribution,omitempty"`
		State        State    `json:"state"`
		Error        string   `json:"error,omitempty"`
		Account      *Account `json:"account,omitempty"`
	}
	SendParams struct {
		Contribution string `json:"contribution,omitempty"`
		// Ref is the hub's id for this message: the same when the hub sends it again (the program sends it once).
		Ref     string `json:"ref"`
		Text    string `json:"text"`
		Kind    string `json:"kind"`
		ReplyTo string `json:"reply_to,omitempty"`
		// Images are sent after Text, only to a channel with ImagesOut.
		Images []Blob `json:"images,omitempty"`
	}
	SendResult struct {
		MessageID string `json:"message_id,omitempty"`
	}
	TypingParams struct {
		Contribution string `json:"contribution,omitempty"`
		On           bool   `json:"on"`
	}
	EditParams struct {
		Contribution string `json:"contribution,omitempty"`
		MessageID    string `json:"message_id"`
		Text         string `json:"text"`
	}
)

// InPlugin is a method's name under the plugin protocol.
func InPlugin(method string) string { return Namespace + "." + method }

// --- a channel program's side, in Go (connector/serve passes the hub's calls on) ---

// ErrCannotSend is what a program's Send says when there is nowhere to send to yet: the hub hears cannot_send.
var ErrCannotSend = errors.New("nowhere to send to yet")

// Transport is a channel program's connector, as the hub calls it: Send, and Typing / Edit when it can.
type Transport interface {
	Send(ctx context.Context, p SendParams) (SendResult, error)
}

// Typer shows "typing".
type Typer interface {
	Typing(ctx context.Context, on bool) error
}

// Editor changes a message it sent.
type Editor interface {
	Edit(ctx context.Context, p EditParams) error
}

// Hub is the hub as a channel program calls it.
type Hub interface {
	// Receive hands the hub one of the owner's messages; it returns once the hub has it (not after the turn).
	Receive(ctx context.Context, m Message) error
	// Status says how a channel without a connector is doing.
	Status(ctx context.Context, p StatusParams) error
}
