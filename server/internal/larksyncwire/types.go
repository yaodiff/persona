package larksyncwire

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// ConnectorID is the knowledge-base connector the sync files everything under (connector IDs match ^[a-z][a-z0-9]*$).
// Its chats are "larksync-<oc_ chat id>": one chat whichever binding read it, so the knowledge base's own receipts
// also keep a message once.
const ConnectorID = "larksync"

// PrimaryBot is the bot_id of the bot in settings "bots.feishu": the chat bot, the default. Older settings have only
// it, and keep working unchanged.
const PrimaryBot = "primary"

// Scope tiers: what the owner lets a bot's app read as them. Docs is the standard pack a company tenant's admin more
// readily approves; Full adds the chat history and file downloads.
const (
	TierDocs = "docs_standard"
	TierFull = "full_with_im"
)

// ScopesDocs are the user scopes of TierDocs. offline_access is what gives a refresh token.
var ScopesDocs = []string{
	"offline_access",
	"search:message",
	"drive:drive:readonly",
	// batch_query accepts metadata:readonly, not drive:readonly (linked documents in both tiers).
	"drive:drive.metadata:readonly",
	"docx:document:readonly",
	"docs:document.content:read",
	"sheets:spreadsheet:readonly",
	"bitable:app:readonly",
	"wiki:wiki:readonly",
	"wiki:node:retrieve",
	"wiki:space:retrieve",
	"minutes:minutes:readonly",
	"minutes:minutes.transcript:export",
}

// ScopesFullExtra are what TierFull adds to ScopesDocs.
var ScopesFullExtra = []string{
	"im:chat:read",
	// GET chats/:chat_id (the p2p name fallback) has a separate read permission from listing chats.
	"im:chat:readonly",
	"im:message:readonly",
	"im:message.group_msg:get_as_user",
	"im:message.p2p_msg:get_as_user",
	"drive:file:download",
	// The senders' names, so a message the Lark gateway already put in the knowledge base is recognized.
	"contact:user.base:readonly",
	// GET contact/v3/users/:open_id needs API access as well as the name field permission above.
	"contact:contact.base:readonly",
}

// ScopesFor is a tier's user scopes; an unknown tier is TierDocs.
func ScopesFor(tier string) []string {
	out := append([]string(nil), ScopesDocs...)
	if tier == TierFull {
		out = append(out, ScopesFullExtra...)
	}
	return out
}

// ValidTier reports whether tier is one of the tiers.
func ValidTier(tier string) bool { return tier == TierDocs || tier == TierFull }

// Binding health, as the settings page shows it.
const (
	HealthOK           = "ok"
	HealthUnauthorized = "unauthorized"  // never authorized yet
	HealthNeedsReauth  = "needs_reauth"  // the refresh token was refused or ran out: authorize again
	HealthRateLimited  = "rate_limited"  // the last run stopped on the platform's rate limit; it goes on next time
	HealthScopeMissing = "scope_missing" // a reader was refused for a missing scope (an admin has not approved it)
	HealthFailing      = "failing"       // the last run failed for another reason
	HealthAwaiting     = "awaiting_approval"
)

// Bot roles. Only the primary bot chats; every other bot is for syncing and holds no long connection.
const (
	RoleChatAndSync = "chat_and_sync"
	RoleSyncOnly    = "sync_only"
)

// Approval states of a bot's app, from its registration and automatic configuration.
const (
	ApprovalReady    = "ready"
	ApprovalAwaiting = "awaiting_approval" // a version is waiting for the tenant admin
	ApprovalDegraded = "degraded"          // configured without some of what it asked for (e.g. no patch permission)
)

// Resources: what one run reads. A binding's cursors are keyed by "<resource>" or "<resource>:<scope id>".
const (
	ResIM      = "im"
	ResSearch  = "search"
	ResDrive   = "drive"
	ResWiki    = "wiki"
	ResMinutes = "minutes"

	// PendingLinksCursor is the per-binding cursor key in lark_sync_cursors holding the persistent queue of unfetched
	// or retryable linked cloud documents discovered in synced messages.
	PendingLinksCursor = "links_queue"

	// SenderNamesCursor is the per-binding cursor key holding the name book: display names the sync learned for
	// senders and mentioned users (open_id or app_id -> name), so a member who left a group or a bot another message
	// mentioned is still named.
	SenderNamesCursor = "sender_names"
	// SenderNamesProgressCursor is the per-binding cursor key holding how far the sender-name backfill got: each
	// large group's member list position, and the lookups that already found nothing (retried after a week), so the
	// backfill runs in rounds that each have their own budget and resume where the last one stopped. Opaque to the
	// manager; an older hub ignores it.
	SenderNamesProgressCursor = "sender_names:progress"
)

// Canonical-key prefixes: one piece of content has one key whichever bot, account or entry point read it.
const (
	KeyMessage = "feishu_msg:"    // + om_ message id
	KeyDoc     = "feishu_doc:"    // + obj_token (docx, sheet, bitable, file; a wiki node resolves to its obj_token)
	KeyMinute  = "feishu_minute:" // + minute token
)

// Entry points a piece of content was found through (SourceRef.Entry).
const (
	EntryChat    = "chat"
	EntrySearch  = "search"
	EntryDrive   = "drive"
	EntryWiki    = "wiki"
	EntryMessage = "message_link" // a link in a message
	EntryGateway = "gateway"      // already in the knowledge base from the Lark gateway
)

// SourceRef is one place a piece of content was read from: the knowledge base keeps the set (kbwire.SourceRef).
type SourceRef = kbwire.SourceRef

// Token is a user's OAuth token pair and who it belongs to.
type Token struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
	Scopes           []string  `json:"scopes,omitempty"`
	OpenID           string    `json:"open_id,omitempty"`
	UnionID          string    `json:"union_id,omitempty"`
	TenantKey        string    `json:"tenant_key,omitempty"`
	Name             string    `json:"name,omitempty"`
}

// App is one bot's app, as the module needs it to call the open platform.
type App struct {
	AppID     string
	AppSecret string
	// Domain is "", "feishu", "lark" or the open platform's URL (botcard.ResolveDomain).
	Domain string
}

// AuthRequest starts a browser authorization (OAuth v3 with PKCE).
type AuthRequest struct {
	App         App
	RedirectURI string
	Scopes      []string
	State       string
	// Verifier is the PKCE code verifier; its S256 challenge goes on the page.
	Verifier string
}

// DeviceChallenge is a device-flow authorization under way: the page to open (or its QR code) and the code to poll.
type DeviceChallenge struct {
	DeviceCode string
	URL        string
	UserCode   string
	ExpiresIn  int
	Interval   int
}

// Device poll outcomes.
const (
	DevicePending  = "pending"
	DeviceSlowDown = "slow_down"
	DeviceDone     = "done"
	DeviceExpired  = "expired"
	DeviceDenied   = "denied"
	DeviceFailed   = "failed"
)

// DevicePoll is one poll's answer; Token is set when Status is DeviceDone.
type DevicePoll struct {
	Status    string
	Token     Token
	ErrorText string
}

// RedirectReport is how adding the redirect URL and the user scopes to an app went (application/v7 config).
type RedirectReport struct {
	// Configured: the redirect URL is on the app; Published: a version with the scopes was published (or is under
	// review, Awaiting).
	Configured, Published, Awaiting bool
	// MissingPatch: the app lacks application:application:patch, so nothing could be changed; the caller falls back
	// to the device flow, then to the owner pasting the redirect URL (ConsoleURL).
	MissingPatch bool
	ConsoleURL   string
	Detail       string
}

// UserAuth is the user OAuth of the open platform. Every call is stateless; the manager keeps the tokens.
type UserAuth interface {
	// AuthorizeURL is the authorization page for r.
	AuthorizeURL(r AuthRequest) string
	// Exchange trades the code the page redirected with for a token.
	Exchange(ctx context.Context, app App, code, redirectURI, verifier string) (Token, error)
	// Refresh trades a refresh token for a new pair. The platform rotates the refresh token: the old one stops
	// working, so the caller must hold one lock per binding around this and persist the result before anyone else
	// refreshes (two refreshes of one token: the second fails with an invalid grant).
	Refresh(ctx context.Context, app App, refreshToken string) (Token, error)
	// DeviceBegin and DevicePoll are the device flow, for an app whose redirect URL could not be set.
	DeviceBegin(ctx context.Context, app App, scopes []string) (DeviceChallenge, error)
	DevicePoll(ctx context.Context, app App, deviceCode string) (DevicePoll, error)
	// ConfigureRedirect adds redirectURI and the user scopes to the app and publishes a version.
	ConfigureRedirect(ctx context.Context, app App, redirectURI string, scopes []string) RedirectReport
}

// Errors the readers and the OAuth return are *Error with one of these kinds, so the manager can tell what to do.
const (
	ErrKindAuth         = "auth"          // the access token was refused (refresh, or authorize again if that fails)
	ErrKindInvalidGrant = "invalid_grant" // the refresh token was refused: authorize again
	ErrKindScope        = "scope"         // a scope is missing (not granted or not approved)
	ErrKindRateLimited  = "rate_limited"  // still limited after the backoff
	ErrKindTransient    = "transient"     // a network or 5xx failure
	ErrKindOther        = "other"
)

// Error is an open-platform failure.
type Error struct {
	Kind       string
	Op         string
	Code       int
	HTTPStatus int
	Msg        string
	// RetryAfter is how long the platform asked to wait (rate limits), 0 when it did not say.
	RetryAfter time.Duration
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return "lark " + e.Op + ": " + e.Kind + " " + e.Msg
}

// Sync modes (Binding.Mode): ModeFull ("", the default) runs the full daily sync; ModeIM runs only the lightweight
// IM incremental sync (new messages since cursors, plus cloud document and minutes links in those messages).
const (
	ModeFull = ""
	ModeIM   = "im"
)

// Binding is one (account, bot) pair a run reads for.
type Binding struct {
	Key       string // stable: "<bot_id>~<account_id>"
	BotID     string
	AccountID string
	// Label is the account's name the owner gave it ("Work"), for logs and the sources.
	Label string
	Tier  string
	// Mode selects which readers run: ModeFull ("") runs every reader; ModeIM reads only IM messages since cursors
	// and follows linked cloud documents and minutes found in those messages.
	Mode string
	App  App
	// Backfill is how far back the first run reads (no cursor yet).
	Backfill time.Duration
	// Budget bounds one run; what is left is read next time, from where the cursors stopped.
	Budget time.Duration
	// AuthorizedAt and GrantedScopes describe the binding's current user authorization, so readers can detect
	// re-authorization or newly granted scopes and immediately retry previously blocked linked documents.
	AuthorizedAt  time.Time
	GrantedScopes []string
}

// Attachment is a file or image of a message; the knowledge base fetches it lazily through the connector
// (connector.ArchiveMedia) with Ref.
type Attachment struct {
	Ref  string `json:"ref"` // "<binding key>|<message id>|<file key>|<image|file>"
	Name string `json:"name,omitempty"`
	Kind string `json:"kind"` // image | file
}

// Message is one chat message read as the owner.
type Message struct {
	ID         string       `json:"id"` // om_…
	ChatID     string       `json:"chat_id"`
	ChatName   string       `json:"chat_name,omitempty"`
	ChatType   string       `json:"chat_type,omitempty"` // p2p | group | topic
	ThreadID   string       `json:"thread_id,omitempty"`
	SenderID   string       `json:"sender_id,omitempty"`
	SenderName string       `json:"sender_name,omitempty"`
	Mine       bool         `json:"mine,omitempty"`
	Type       string       `json:"type,omitempty"` // text | post | image | file | interactive …
	Text       string       `json:"text"`
	At         time.Time    `json:"at"`
	Files      []Attachment `json:"files,omitempty"`
	Sources    []SourceRef  `json:"sources,omitempty"`
}

// Doc is one document's current revision: a docx, sheet, bitable, file or minutes, keyed by Key.
type Doc struct {
	Key      string `json:"key"`  // KeyDoc+obj_token or KeyMinute+token
	Kind     string `json:"kind"` // kbwire item kind: docs | meetings
	ObjToken string `json:"obj_token"`
	ObjType  string `json:"obj_type"` // docx | sheet | bitable | file | doc | mindnote | minutes
	// Aliases are other tokens the same content is known by (a wiki node token), for aligning with what is already
	// in the knowledge base.
	Aliases  []string    `json:"aliases,omitempty"`
	Revision int64       `json:"revision"`
	Title    string      `json:"title"`
	URL      string      `json:"url,omitempty"`
	Note     string      `json:"note,omitempty"` // why a body was not read (e.g. forbidden, export disabled, non-text file)
	Body     string      `json:"body,omitempty"` // Markdown, bounded
	BodyHash string      `json:"body_hash,omitempty"`
	At       time.Time   `json:"at"` // last edited
	Sources  []SourceRef `json:"sources,omitempty"`
}

// ResourceError is one resource a run could not read: its cursor did not move.
type ResourceError struct {
	Resource string `json:"resource"` // a cursor key
	Kind     string `json:"kind"`     // ErrKind*
	Detail   string `json:"detail,omitempty"`
}

// Result is what one run read. Messages and Docs are only what is new or newer (the Index decided); Cursors are the
// cursors to save, moved only for what was read and handed over in full.
type Result struct {
	Messages []Message         `json:"messages,omitempty"`
	Docs     []Doc             `json:"docs,omitempty"`
	Cursors  map[string]string `json:"cursors,omitempty"`
	Errors   []ResourceError   `json:"errors,omitempty"`
	// Partial: the budget ran out; the rest is read next time.
	Partial bool `json:"partial,omitempty"`
}

// IndexRecord is the dedup index's line for one canonical key.
type IndexRecord struct {
	Key      string
	Kind     string // message | doc | minute
	Revision int64
	BodyHash string
	Sources  []SourceRef
	Updated  time.Time
}

// Index is the dedup index the manager keeps (Postgres): one record per canonical key, across every binding.
type Index interface {
	Lookup(ctx context.Context, keys []string) (map[string]IndexRecord, error)
	Put(ctx context.Context, recs []IndexRecord) error
}

// Decision is what to do with content read again (Decide).
const (
	DecideNew   = "new"   // never seen: write it
	DecideNewer = "newer" // a higher revision: write it over the old one in place
	DecideSame  = "same"  // the same revision: only add the source
	DecideStale = "stale" // an older revision arrived late: keep the newer body, only add the source
)

// Decide is the revision state machine for a canonical key: old is the stored record (ok false when none). A body
// hash that changed at the same revision counts as newer (a platform that does not bump the revision).
func Decide(old IndexRecord, ok bool, revision int64, bodyHash string) string {
	switch {
	case !ok:
		return DecideNew
	case revision > old.Revision:
		return DecideNewer
	case revision < old.Revision:
		return DecideStale
	case bodyHash != "" && old.BodyHash != "" && bodyHash != old.BodyHash:
		return DecideNewer
	default:
		return DecideSame
	}
}

// MergeSources is have with every ref of add whose (channel, binding, entry) is new, keeping have's order.
func MergeSources(have, add []SourceRef) []SourceRef {
	out := append([]SourceRef(nil), have...)
	for _, a := range add {
		dup := false
		for _, h := range out {
			if h.Channel == a.Channel && h.Binding == a.Binding && h.Entry == a.Entry {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, a)
		}
	}
	return out
}

// Syncer reads one binding. One Syncer per binding: its rate limits are its own.
type Syncer interface {
	// Run reads what changed since cursors. token returns a fresh access token (the manager refreshes under the
	// binding's lock); a run whose token is refused stops with an *Error of ErrKindAuth. Run never returns content
	// whose resource failed part way, and leaves that resource's cursor where it was.
	Run(ctx context.Context, b Binding, token func(ctx context.Context) (string, error), cursors map[string]string, idx Index) (Result, error)
	// Media fetches an attachment by its Ref.
	Media(ctx context.Context, b Binding, token func(ctx context.Context) (string, error), ref string, limit int64) ([]byte, string, error)
}

// NameResolver is optionally implemented by a Syncer to resolve user display names by open_id using a binding's user
// access token (used by the one-time sender-name backfill for pre-#15486 messages). Non-user ids and per-user lookup
// errors are omitted; a missing contact scope or auth/context error is returned alongside any names resolved so far.
type NameResolver interface {
	ResolveUserNames(ctx context.Context, b Binding, token func(ctx context.Context) (string, error), openIDs []string) (map[string]string, error)
}

// OpenID is a Feishu/Lark user open_id in the sync contract.
type OpenID string

// ChatID is a Feishu/Lark chat_id (oc_…) in the sync contract.
type ChatID string

// MessageID is a Feishu/Lark message_id (om_…) in the sync contract.
type MessageID string

// MessageType is a Feishu/Lark message msg_type in the sync contract (for example "text", "post", "image", "file",
// or "interactive"). Unknown platform msg_type values pass through unchanged.
type MessageType string

// Common Feishu/Lark message types handled by user sync.
const (
	MessageTypeText        MessageType = "text"
	MessageTypePost        MessageType = "post"
	MessageTypeImage       MessageType = "image"
	MessageTypeFile        MessageType = "file"
	MessageTypeMedia       MessageType = "media"
	MessageTypeAudio       MessageType = "audio"
	MessageTypeInteractive MessageType = "interactive"
	MessageTypeSystem      MessageType = "system"
)

// EventLarkMessages is the extension event emitted after a sync round commits new messages to the outbox.
const EventLarkMessages = "lark.messages"

// MessageEventItem is one message's metadata inside event.lark.messages: strictly no message body, display names,
// or attachment contents; extensions read bodies within their own permissions.
type MessageEventItem struct {
	ChatID       ChatID      `json:"chat_id"`
	MessageID    MessageID   `json:"message_id"`
	SenderOpenID OpenID      `json:"sender_open_id,omitempty"`
	At           time.Time   `json:"at,omitzero"`
	Type         MessageType `json:"type,omitempty"`
}

// MessagesEvent is one paginated batch of newly synced Lark message metadata emitted as event.lark.messages.
type MessagesEvent struct {
	Binding  string             `json:"binding"`
	Page     int                `json:"page"`
	Pages    int                `json:"pages"`
	Total    int                `json:"total"`
	Messages []MessageEventItem `json:"messages"`
}

// NameResolveRequest describes the unnamed senders and chats a binding's sender-name backfill wants to resolve.
type NameResolveRequest struct {
	// OpenIDs are unique human sender open_ids to look up.
	OpenIDs []OpenID
	// ChatSenders maps chat_id -> unique unnamed sender_ids seen in that chat (group, topic, or p2p), used to
	// fetch GET /open-apis/im/v1/chats/:chat_id/members per chat when contact/v3 misses external users.
	ChatSenders map[string][]string
	// P2PChatIDs are unnamed p2p chat_ids whose display name should be resolved via chat info and chat members.
	P2PChatIDs []ChatID
	// SampleMessages maps sender_id -> a sample message_id from that sender for message-carried fallback when
	// both contact/v3 and chat members miss.
	SampleMessages map[string]string
	// SampleTimes maps sender_id -> the time of its sample message, around which the chat is read for an @mention of
	// a sender no member list names (one who left the group).
	SampleTimes map[string]time.Time
	// SampleChats maps sender_id -> the chat of its sample message.
	SampleChats map[string]string
	// NameBook is the binding's stored SenderNamesCursor value: names learned before, for senders nothing else names.
	NameBook string
	// Progress is the binding's stored SenderNamesProgressCursor value ("" for a first round).
	Progress string
}

// NameResolveResult holds display names resolved for senders and unnamed p2p chats.
type NameResolveResult struct {
	// UserNames maps sender_id -> display name.
	UserNames map[string]string
	// P2PChatNames maps p2p chat_id -> peer display name.
	P2PChatNames map[string]string
	// Partial is true when the round's budget ran out before every lookup was tried: not a failure, the next round
	// resumes from Progress.
	Partial bool
	// Progress is the new SenderNamesProgressCursor value to store, returned with or without an error ("" when
	// nothing is left to remember).
	Progress string
	// NameBook is the name book with the names this round learned ("" when unchanged).
	NameBook string
}

// ChatNameResolver is optionally implemented by a Syncer (alongside NameResolver) to resolve sender display names
// and unnamed p2p chat names with chat-scoped fallbacks (GET im/v1/chats/:chat_id/members, GET im/v1/chats/:chat_id,
// and message-carried sender/mention info) when contact/v3 cannot see external users.
type ChatNameResolver interface {
	ResolveNamesWithFallback(ctx context.Context, b Binding, token func(ctx context.Context) (string, error), req NameResolveRequest) (NameResolveResult, error)
}

// LinkObjType is the canonical cloud object type of a Feishu / Lark document or wiki link.
type LinkObjType string

// Supported cloud object types for linked documents.
const (
	LinkObjDocx     LinkObjType = "docx"
	LinkObjDoc      LinkObjType = "doc"
	LinkObjSheet    LinkObjType = "sheet"
	LinkObjBitable  LinkObjType = "bitable"
	LinkObjFile     LinkObjType = "file"
	LinkObjMindnote LinkObjType = "mindnote"
	LinkObjSlides   LinkObjType = "slides"
	LinkObjWiki     LinkObjType = "wiki"
)

// PendingLinkCandidate is a cloud document or wiki link extracted from a synced message during one-time remediation.
type PendingLinkCandidate struct {
	Token     string
	ObjType   LinkObjType
	URL       string
	MessageAt time.Time
}

// PendingLinkBackfiller is optionally implemented by a Syncer to extract cloud document and wiki links from
// already-synced messages and merge missing ones into a binding's PendingLinksCursor payload without duplicating
// queued tokens or overwriting higher-priority live entries.
type PendingLinkBackfiller interface {
	ExtractDocLinks(text string) []PendingLinkCandidate
	MergePendingLinks(existingCursor string, add []PendingLinkCandidate) (updatedCursor string, added int)
}

// Provider is what the Lark module provides for the sync (botport.Provider.Sync).
type Provider struct {
	// NewUserAuth is the OAuth client; accounts, when not "", replaces the accounts site and open is the open
	// platform's URL override (tests); httpClient replaces the HTTP client when not nil.
	NewUserAuth func(accounts, open string, httpClient *http.Client) UserAuth
	// NewSyncer is a binding's reader; now is the clock (nil: time.Now).
	NewSyncer func(httpClient *http.Client, now func() time.Time) Syncer
}

// RegisterOptions extends an app registration (botport.Registrar.BeginWith): the user scopes the app asks for, and
// an existing app to add them to instead of creating one.
type RegisterOptions struct {
	UserScopes []string
	AppID      string
}

// Settings keys for the Lark user sync (db.SealedSettings re-seals the *_enc fields on key rotation).
const (
	SettingPrefs    = "bots.feishu.sync"
	SettingBots     = "bots.feishu.sync.bots"
	SettingBindings = "bots.feishu.sync.bindings"
	SettingAccounts = "bots.feishu.sync.accounts"
)

// Sealed setting field names re-sealed by key rotation (db.SealedSettings).
var (
	SealedPrefsFields    = []string{"registration_device_enc"}
	SealedBotsFields     = []string{"secret_enc", "registration_device_enc"}
	SealedBindingsFields = []string{"access_token_enc", "refresh_token_enc", "auth_verifier_enc", "auth_device_enc"}
)

// OutboxRow is one row of the lark_sync_outbox table: a chat batch (OutboxChat) or a document (OutboxDoc), its
// payload the JSON the connector hands the knowledge base as it is. db.LarkSyncOutbox aliases this type.
type OutboxRow struct {
	Seq          int64
	Kind         OutboxKind
	CanonicalKey string
	Payload      OutboxPayload
	CreatedAt    time.Time
}

// OutboxKind is what an outbox row carries: a chat batch or a document, the only two the sync writes
// (db.CommitLarkSyncRun refuses any other).
type OutboxKind string

// Outbox kinds (db.LarkSyncChat / db.LarkSyncDoc alias these).
const (
	OutboxChat OutboxKind = "chat"
	OutboxDoc  OutboxKind = "doc"
)

// OutboxPayload is a row's payload as stored: the JSON of a connectorwire.ChatBatch for OutboxChat, of a kbwire.Item
// for OutboxDoc, handed to the knowledge base exactly as it was written.
type OutboxPayload json.RawMessage

// Store keeps what the sync's runs read and hand over (Postgres tables lark_sync_cursors, lark_sync_index and
// lark_sync_outbox). A manager whose store does not implement it leaves the sync unavailable.
type Store interface {
	LarkSyncCursors(ctx context.Context, binding string) (map[string]string, error)
	DeleteLarkSyncCursors(ctx context.Context, binding string) error
	LarkSyncIndex(ctx context.Context, keys []string) (map[string]IndexRecord, error)
	CommitLarkSyncRun(ctx context.Context, binding string, cursors map[string]string, recs []IndexRecord, outbox []OutboxRow) error
	LarkSyncOutboxAfter(ctx context.Context, kind OutboxKind, after int64, limit int) ([]OutboxRow, error)
	LarkSyncDocsSince(ctx context.Context, since time.Time) ([]OutboxRow, error)
	PruneLarkSyncOutbox(ctx context.Context, before time.Time) (int64, error)
}
