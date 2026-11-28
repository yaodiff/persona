package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/wireids"
)

// The knowledge base is the assistant's (docs/assistant/knowledge.md "Who owns what"): its engine and loop, its files under the data
// directory and its index rows. The hub keeps the sources: the assistant reads what the connectors deliver through
// app.call connectors.* (data in, data out; the cursors are the assistant's, so nothing is lost while it is away), and
// the hub's own features that read the knowledge base ask the assistant (kb.*), as the posts the connectors' programs
// hand the hub go to it. What the hub sees happen goes to it as events.

// What the hub's connectors offer the knowledge base (app.call connectors.*).
const (
	// ConnectorsArchives: {} → Archives: the connectors whose conversations go into the knowledge base now (signed in,
	// "Add to knowledge base" on), and those with documents, meetings and events.
	ConnectorsArchives = "connectors.archives"
	// ConnectorsChanges: {id, cursor, limit} → {batches: [connectorwire.ChatBatch], next, more}: what moved in a connector's
	// account since cursor ("" the first time), and the cursor to go on from (connector.Archive).
	ConnectorsChanges = "connectors.changes"
	// ArchiveBegin/End reserve and release an iMessage page's commit; Abort retires an uncertain begin.
	ConnectorsArchiveBegin = "connectors.archive_begin"
	ConnectorsArchiveEnd   = "connectors.archive_end"
	ConnectorsArchiveAbort = "connectors.archive_abort"
	// ConnectorsArchiveMedia: {id, ref} → {media: connectorwire.Media}: an archived message's attachment, at most
	// connectorwire.ArchiveMediaMost bytes (connector.ArchiveMedia).
	ConnectorsArchiveMedia = "connectors.archive_media"
	// ConnectorsItems: {id} → {items: [kbwire.Item]}: a connector's documents, meetings and events (connector.Items).
	ConnectorsItems = "connectors.items"
	// ConnectorsDocTitle: {id, url} → {title}: a document's title ("" when the connector cannot tell).
	ConnectorsDocTitle = "connectors.doc_title"

	// Importing older history (connector.History; docs/protocol/connector-protocol.md "History"), read-only, for a
	// connector that archives into the knowledge base now and declares history (ArchiveSource.History):
	//   ConnectorsHistoryChats: {id} → HistoryChatsResult: the conversations it can import.
	//   ConnectorsHistory: HistoryParams {id, query} → connectorwire.HistoryPage: one page of one of them.
	//   ConnectorsHistoryAnchors: HistoryAnchorsParams {id, query} → connectorwire.HistoryAnchors: the owner's
	//   messages over a window, for one declaring history_anchors (ArchiveSource.HistoryAnchors).
	// A connector's error comes back as it is; a call that timed out is never sent again by the caller.
	ConnectorsHistoryChats   = "connectors.history_chats"
	ConnectorsHistory        = "connectors.history"
	ConnectorsHistoryAnchors = "connectors.history_anchors"
)

// Archives is connectors.archives' answer.
type Archives struct {
	Archives []ArchiveSource `json:"archives"`
	Items    []string        `json:"items"` // the ids of the connectors with documents, meetings and events
}

// ArchiveSource is a connector whose conversations the knowledge base reads now.
type ArchiveSource struct {
	ID string `json:"id"`
	// FenceVersion identifies the commit protocol required by this source; zero is the legacy protocol.
	FenceVersion int `json:"fence_version,omitempty"`
	// PollMS is how often it wants to be asked (its manifest's Poll); 0: the knowledge base's own default.
	PollMS int64 `json:"poll_ms,omitempty"`
	// Sources are the other names its batches may be filed under: its manifest's ArchiveSources, minus the ids of the
	// other connectors (one connector never writes into another's chats).
	Sources []string `json:"sources,omitempty"`
	// Media: it hands over its messages' attachments (connectors.archive_media).
	Media bool `json:"media,omitempty"`
	// History: it can import a conversation's older history (connectors.history_chats, connectors.history).
	History bool `json:"history,omitempty"`
	// HistoryAnchors: it can search for the owner's messages to read around (connectors.history_anchors).
	HistoryAnchors bool `json:"history_anchors,omitempty"`
}

// HistoryChatsResult is connectors.history_chats' answer.
type HistoryChatsResult struct {
	Chats []connectorwire.HistoryChat `json:"chats"`
}

// HistoryParams is connectors.history's params.
type HistoryParams struct {
	ID    string                     `json:"id"`
	Query connectorwire.HistoryQuery `json:"query"`
}

// HistoryAnchorsParams is connectors.history_anchors' params.
type HistoryAnchorsParams struct {
	ID    string                            `json:"id"`
	Query connectorwire.HistoryAnchorsQuery `json:"query"`
}

// ChangesResult is connectors.changes' answer.
type ChangesResult struct {
	Batches    []connectorwire.ChatBatch `json:"batches"`
	Next       string                    `json:"next"`
	More       bool                      `json:"more,omitempty"`
	Generation string                    `json:"generation,omitempty"`
}

const ArchiveFenceV1 = 1

// ArchiveFence identifies a page and its caller-generated commit reservation.
type ArchiveFence struct {
	ID         string `json:"id"`
	Generation string `json:"generation"`
	Token      string `json:"token"`
}

type ArchiveFenceResult struct {
	Acquired bool `json:"acquired"`
}

// The hub asks the knowledge base (calls), and tells it (events).
const (
	// MethodKBContext: {chat_id} → {context: kbwire.ChatContext | null}: what the knowledge base knows about one chat
	// and its first three projects (the inbox's AI twin starts with it); null without such a chat.
	MethodKBContext = "kb.context"
	// MethodKBProjects: {machine_id, cwd} → {projects: [KBProject]}: the projects whose code directories hold cwd on
	// that machine (an agent's turn is triaged with their names).
	MethodKBProjects = "kb.projects"
	// MethodKBForget: KBForget → KBForgotten: a connector's knowledge content goes, with its index rows, project
	// associations and cursors ("Delete saved data").
	MethodKBForget = "kb.forget"
	// The posts a connector's program hands the hub (connector.KnowledgeBase, docs/protocol/connector-protocol.md "Knowledge base"),
	// for that connector (KBPosts.Connector; the hub checked it may write now):
	//   kb.posts_put {connector, sources, posts} → {statuses}
	//   kb.posts_comments {connector, id, count, comments} → {}
	//   kb.posts_pending {connector, limit} → {posts: [connectorwire.PendingPost]}
	//   kb.archived_chats {connector, sources, source} → {chats: [connectorwire.ArchivedChat]}
	MethodKBPostsPut      = "kb.posts_put"
	MethodKBPostsComments = "kb.posts_comments"
	MethodKBPostsPending  = "kb.posts_pending"
	MethodKBArchivedChats = "kb.archived_chats"

	// EventKBKick: KBKick: a source has something new (a connector's realtime event, a feed turned on): go round soon,
	// and read that connector's chats (or the items) at that round even before its poll interval. A manager before
	// #1562 sends {}: only the round.
	EventKBKick = "kb.kick"
	// EventKBAgentTurn: KBAgentTurn: an agent finished a turn (after the hub's done delay, not the assistant's own
	// conversations); the projects of its directory get a timeline line.
	EventKBAgentTurn = "kb.agent_turn"
)

// Typed descriptors for the knowledge base's internal bus calls.
var (
	KBContextMethod       = buswire.Method[KBContextParams, KBContextResult]{Name: MethodKBContext}
	KBProjectsMethod      = buswire.Method[KBProjectsParams, KBProjectsResult]{Name: MethodKBProjects}
	KBForgetMethod        = buswire.Method[KBForget, KBForgotten]{Name: MethodKBForget}
	KBPostsPutMethod      = buswire.Method[KBPosts, KBPostsPutResult]{Name: MethodKBPostsPut}
	KBPostsCommentsMethod = buswire.Method[KBPosts, KBPostsCommentsAck]{Name: MethodKBPostsComments}
	KBPostsPendingMethod  = buswire.Method[KBPosts, KBPostsPendingResult]{Name: MethodKBPostsPending}
	KBArchivedChatsMethod = buswire.Method[KBPosts, KBArchivedChatsResult]{Name: MethodKBArchivedChats}
)

// KBKick is EventKBKick's params: the connector that has something new, and what (a connector scope: chats, items).
type KBKick struct {
	Connector string `json:"connector,omitempty"`
	Scope     string `json:"scope,omitempty"`
}

// KBRemoteFolder identifies a user-configured, read-in-place knowledge source.
type KBRemoteFolder struct {
	Name    string `json:"name"`
	Machine string `json:"machine"`
	devicewire.KnowledgeFolder
}

// KBSettings is the knowledge base's settings the manager goes by: its manager.json's kb section, laid over the hub's
// hello (an older hub still sends its hub.json's). Its ai is the models' (HubModels.KBAI).
type KBSettings struct {
	// ImportProvider is manager-local binding information. A remote hello cannot supply it.
	ImportProvider *KBImportProvider `json:"-"`
	ReadOnly       bool              `json:"read_only,omitempty"`       // serve local archives without collection, models or writes
	Off            bool              `json:"off,omitempty"`             // kb.enabled false: no knowledge base at all
	TextDays       int               `json:"text_days,omitempty"`       // 0: 365
	AttachmentDays int               `json:"attachment_days,omitempty"` // 0: 30
	ExtraDirs      []string          `json:"extra_dirs,omitempty"`      // as written; "~/" is the assistant's home
	RemoteFolders  []KBRemoteFolder  `json:"remote_folders,omitempty"`
	// AttachmentDaysBySource: attachment_days for the chats of one source (kb.attachment_days_by_source); the sources
	// not in it, and non-positive values, keep attachment_days.
	AttachmentDaysBySource map[string]int `json:"attachment_days_by_source,omitempty"`
	// History is kb.history, the history import (KBHistory; nil: off). Only manager.json sets it, and a change never
	// reopens the knowledge base: the import reads it afresh before every call.
	History *KBHistory `json:"history,omitempty"`
	// LarkMinutes is kb.lark_minutes, importing the owner's Lark minutes and document titles through lark-cli (nil or
	// not enabled: off). Like History it is read afresh every round, so a change never reopens the knowledge base.
	LarkMinutes *KBLarkMinutes `json:"lark_minutes,omitempty"`
}

// KBSourceChatID identifies a chat in its connector account, before filing in the knowledge base.
type KBSourceChatID string

// KBImportProvider pins one installation to an exact connector chat and filing namespace.
type KBImportProvider struct {
	Extension   wireids.ExtensionID `json:"extension"`
	StorageID   wireids.ExtensionID `json:"storage_id"`
	InstalledAt time.Time           `json:"installed_at"`
	Connector   wireids.ConnectorID `json:"connector"`
	SourceID    KBSourceChatID      `json:"source_id"`
	FiledAs     string              `json:"filed_as"`
}

// KBLarkMinutes is kb.lark_minutes (docs/assistant/knowledge.md "Lark minutes and documents"): meeting minutes and
// document titles brought in through the owner's lark-cli login on the manager's computer. Off unless enabled.
type KBLarkMinutes struct {
	Enabled bool `json:"enabled,omitempty"`
	// SinceDays is how far back each run searches for minutes (0: 30).
	SinceDays int `json:"since_days,omitempty"`
	// IntervalMinutes is the least time between two runs (0: 60).
	IntervalMinutes int `json:"interval_minutes,omitempty"`
	// Binary is lark-cli's path or name ("" : lark-cli on PATH, else in the login shell's PATH).
	Binary string `json:"binary,omitempty"`
	// Profile is the lark-cli profile to read as ("" : its default profile).
	Profile string `json:"profile,omitempty"`
	// DocSearch discovers documents with these search keywords (empty: off; each run reads the first page of each).
	DocSearch []string `json:"doc_search,omitempty"`
}

// On: the import is enabled.
func (k *KBLarkMinutes) On() bool { return k != nil && k.Enabled }

// CodeKBUnavailable: the knowledge base is not open yet (the user's time zone not known yet, another assistant process
// has the data directory); the hub answers it like assistant_offline (503, try again).
const CodeKBUnavailable = "kb_unavailable"

// CodeKBReadOnly refuses edits and ingestion while local archive reads remain available.
const CodeKBReadOnly = "kb_read_only"

// CodeKBDisabled means the manager has disabled knowledge; retrying cannot enable it.
const CodeKBDisabled = "kb_disabled"

// KBProjectsParams identifies the machine and working directory to match against knowledge base projects.
type KBProjectsParams struct {
	MachineID wireids.MachineID `json:"machine_id"`
	Cwd       string            `json:"cwd"`
}

// KBProjectsResult lists the projects whose code directories match the request.
type KBProjectsResult struct {
	Projects []KBProject `json:"projects"`
}

// KBProject is a project as kb.projects names it.
type KBProject struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// KBForget identifies the connector and the additional sources its archive may write to.
// ArchiveSources comes from the registered manifest, never from a client deletion request.
type KBForget struct {
	Source         string   `json:"source"`
	ArchiveSources []string `json:"archive_sources,omitempty"`
}

// KBForgotten is kb.forget's answer: how many entities were removed.
type KBForgotten struct {
	Chats  int `json:"chats"`
	People int `json:"people"`
	Posts  int `json:"posts"`
	Items  int `json:"items"`
}

// KBChatID identifies a chat as filed in the knowledge base.
type KBChatID string

// KBContextParams identifies the chat whose context to read.
type KBContextParams struct {
	ChatID KBChatID `json:"chat_id"`
}

// KBContextResult is kb.context's answer.
type KBContextResult struct {
	Context *kbwire.ChatContext `json:"context"`
}

// KBPosts is the params of the kb.posts_* and kb.archived_chats calls (each takes the fields its line above names).
type KBPosts struct {
	Connector string `json:"connector"`
	// Sources are the names the connector's conversations are filed under: its id, then its manifest's ArchiveSources.
	Sources  []string                    `json:"sources,omitempty"`
	Posts    []connectorwire.PostPut     `json:"posts,omitempty"`
	ID       string                      `json:"id,omitempty"`
	Count    int                         `json:"count,omitempty"`
	Comments []connectorwire.PostComment `json:"comments"`
	Limit    int                         `json:"limit,omitempty"`
	Source   string                      `json:"source,omitempty"`
}

// KBPostsPutResult contains one status per submitted post.
type KBPostsPutResult struct {
	Statuses []string `json:"statuses"`
}

// KBPostsCommentsAck is the empty acknowledgment of a comment update.
type KBPostsCommentsAck = buswire.None

// KBPostsPendingResult lists posts whose comments need to be read again.
type KBPostsPendingResult struct {
	Posts []connectorwire.PendingPost `json:"posts"`
}

// KBArchivedChatsResult lists conversations archived under the requested source.
type KBArchivedChatsResult struct {
	Chats []connectorwire.ArchivedChat `json:"chats"`
}

// KBAgentTurn is kb.agent_turn's params: the agent, where it works, and what its turn comes to (the hub's verdict
// when it found it done, else its last words).
type KBAgentTurn struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	Line      string `json:"line"`
}

// KBItems is connectors.items' answer.
type KBItems struct {
	Items []kbwire.Item `json:"items"`
}
