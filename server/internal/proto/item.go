package proto

import (
	"encoding/base64"
	"encoding/json"
	"image"
	_ "image/gif" // image sizes for lazy placeholders
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Item kinds.
const (
	KindText       = "text"
	KindThinking   = "thinking"
	KindToolUse    = "tool_use"
	KindToolResult = "tool_result"
	KindNotice     = "notice"
	KindImage      = "image"
	KindRecap      = "recap" // Claude Code's "while you were away" summary
	// KindVisualization is a Codex visualize reference: a self-contained HTML fragment on the node's disk that the
	// Codex client draws inline. The item carries the node-side path, never the bytes; clients fetch them through the
	// session visualization route.
	KindVisualization = "visualization"
	// KindFold stands for a run of the agent's work left out of a page (Item.Fold); only for callers that ask
	// (fold_work).
	KindFold = "fold"
)

// Fold is what a KindFold item stands for: a run of tool calls with their results and thoughts, from the item it is
// named after ("fold:" + its id, at its seq and time) through ThroughID. A chat shows the run folded into counts;
// opening it fetches the run (session.items with from_seq / through_seq), which replaces the fold.
type Fold struct {
	Items    int `json:"items"`
	Tools    int `json:"tools"`
	Thoughts int `json:"thoughts"` // thoughts with words (blank ones are not shown)
	// Errors: how many of the run's tool results were errors, and ToolNames the tools it called, each once in the
	// order first called, at most FoldToolNamesMax: what a reader needs to tell a run that went wrong without opening
	// it (docs/protocol/protocol.md "Folded work"). ToolNames is always a list: a fold without it (an older node) says
	// neither.
	Errors     int                `json:"errors"`
	ToolNames  []string           `json:"tool_names"`
	Until      time.Time          `json:"until"`
	ThroughSeq int                `json:"through_seq"`
	ThroughID  string             `json:"through_id"`
	Tasks      []FoldTaskSnapshot `json:"tasks,omitempty"`
	// Images: the images the run's calls looked at, in order (FoldImage): a chat shows its turn's under the answer
	// without opening the run.
	Images []FoldImage `json:"images,omitempty"`
	// Botmux: the `botmux send` replies folded into the run (Item.Botmux), in order, so a chat that shows BotMux
	// sessions as in Lark shows them without opening the run.
	Botmux []Botmux `json:"botmux,omitempty"`
	// LatestThinking is a bounded preview of the run's newest thought with words. The original item stays in the fold.
	LatestThinking *Item `json:"latest_thinking,omitempty"`
	// SupervisionUser preserves an explicit user-visible action hidden inside this work range.
	SupervisionUser bool `json:"supervision_user,omitempty"`
	// Changes: the files the run's successful edits changed, in the order first edited (FoldChange), so a chat shows its
	// turn's changed-files card without opening the run. Always a list, empty when the run edited nothing: a fold
	// without it (an older node) leaves the edits unknown.
	Changes []FoldChange `json:"changes"`
	// Usage: the tokens of the model requests the folded items stood for, summed per model and speed (TokenUsage), so
	// a chat counts its turn's usage without opening the run. Absent when they carried none and from older nodes.
	Usage []TokenUsage `json:"usage,omitempty"`
	// Sources: the web pages the run's successful web calls used (FoldSource), so a chat shows its reply's sources
	// without opening the run. Absent when it used none, and from older nodes.
	Sources []FoldSource `json:"sources,omitempty"`
	// ToolViews: the out-of-band declarative views returned by the run's connector tool calls (FoldToolView), rendered
	// beside Images under the assistant's reply without opening the fold.
	ToolViews []FoldToolView `json:"tool_views,omitempty"`
}

// FoldSourcesMax bounds Fold.Sources.
const FoldSourcesMax = 10

// FoldSource is one web page a folded run used (docs/protocol/protocol.md "Folded work"): a page it opened (WebFetch,
// Codex's open page) or a result its web search returned, with the result's title when one was given.
type FoldSource struct {
	URL   string `json:"url"`
	Title string `json:"title,omitempty"`
}

// FoldToolNamesMax bounds Fold.ToolNames.
const FoldToolNamesMax = 10

// FoldChange is one file a folded run's successful edits changed (docs/protocol/protocol.md "Folded work"): the lines
// they added and removed, counted as the apps count an edit's diff and summed over the run's edits of that path.
// Exact is false when an edit's input was clipped for good: its counts would be a guess, which the apps do not show.
type FoldChange struct {
	Path  string `json:"path"`
	Add   int    `json:"add"`
	Del   int    `json:"del"`
	Exact bool   `json:"exact"`
}

// FoldImage is an image a tool call of a folded run looked at (docs/protocol/protocol.md "Images viewed this turn"): one its result
// carried (ItemID, fetched from GET …/items/{item id}/image), or a picture file Read opened whose result carried none
// (only Path; the file's thumbnail is GET /machines/{mid}/file/thumb).
type FoldImage struct {
	ItemID    string `json:"item_id,omitempty"`
	ToolUseID string `json:"tool_use_id"`
	Tool      string `json:"tool"`              // the call's tool as written (Read, mcp__persona__computer_state)
	Source    string `json:"source"`            // read, screenshot (computer_*), browser (browser_*), mcp (another MCP tool), tool
	Path      string `json:"path,omitempty"`    // the picture file Read opened (a Codex view_image too)
	Machine   string `json:"machine,omitempty"` // screenshot / browser: the machine the call named (an id or a name)
	Mime      string `json:"mime,omitempty"`
	Size      int    `json:"size,omitempty"` // bytes, about (the base64's length × 3 / 4, like Image.Placeholder)
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
}

// FoldToolViewsMax bounds Fold.ToolViews.
const FoldToolViewsMax = 8

// FoldToolView is an out-of-band declarative view returned by a connector tool call in a folded run.
type FoldToolView struct {
	ItemID    string    `json:"item_id,omitempty"`
	ToolUseID string    `json:"tool_use_id"`
	Tool      string    `json:"tool"`
	View      wire.View `json:"view"`
}

// FoldTaskSnapshot preserves a successful task result without its raw tool payload.
// Replacement boundaries stay ordered so partial updates can inherit older loaded tasks.
type FoldTaskSnapshot struct {
	Replace         string     `json:"replace,omitempty"`
	FallbackStatus  string     `json:"fallback_status,omitempty"`
	Entries         []FoldTask `json:"entries"`
	SourceItemID    string     `json:"source_item_id"`
	SourceToolUseID string     `json:"source_tool_use_id"`
	SourceSeq       int        `json:"source_seq"`
}

type FoldTask struct {
	ID          string  `json:"id"`
	Subject     *string `json:"subject,omitempty"`
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

type Image struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
	// Lazy: the hub left Data out (clients that asked for lazy_images); fetch it from
	// GET /api/v1/sessions/{mid}/{sid}/items/{item id}/image when the image is shown. Size is the byte count.
	// Width / Height (pixels) let the app reserve the image's space before the bytes arrive.
	Lazy   bool `json:"lazy,omitempty"`
	Size   int  `json:"size,omitempty"`
	Width  int  `json:"width,omitempty"`
	Height int  `json:"height,omitempty"`
}

// Placeholder is the image without its data: what a page carries, fetched on demand when shown.
func (img *Image) Placeholder() *Image {
	if img.Data == "" {
		return img
	}
	lazy := &Image{Mime: img.Mime, Lazy: true, Size: len(img.Data) * 3 / 4}
	// Only the header is read: the decoder stops once it knows the size.
	if c, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(img.Data))); err == nil {
		lazy.Width, lazy.Height = c.Width, c.Height
	}
	return lazy
}

// WithoutImageData returns items whose images carry no data, only a placeholder to fetch on demand: chat pages
// with screenshots inline ran to megabytes.
func WithoutImageData(items []Item) []Item {
	var out []Item
	for i, it := range items {
		if it.Image == nil || it.Image.Data == "" {
			continue
		}
		if out == nil {
			out = append([]Item(nil), items...)
		}
		out[i].Image = it.Image.Placeholder()
	}
	if out == nil {
		return items
	}
	return out
}

// PositionalItemID identifies the fallback IDs assigned to JSONL records that lack a runtime ID.
func PositionalItemID(id string) bool {
	if _, source, ok := ParseSegmentItemID(id); ok {
		id = source
	}
	suffix, ok := strings.CutPrefix(id, "line")
	if !ok {
		return false
	}
	line, block, ok := strings.Cut(suffix, "#")
	return ok && decimalItemPart(line) && decimalItemPart(block)
}

// SegmentItemID namespaces only later segments. The first segment retains its
// existing identifiers, including positional IDs used by client caches.
func SegmentItemID(segment, source string) string { return "segment:" + segment + ":" + source }

func ParseSegmentItemID(id string) (segment, source string, ok bool) {
	rest, ok := strings.CutPrefix(id, "segment:")
	if !ok {
		return "", "", false
	}
	segment, source, ok = strings.Cut(rest, ":")
	return segment, source, ok && segment != "" && source != ""
}

func decimalItemPart(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
}

// QuestionReply preserves the question quoted by a Codex reply. A single answer is already in Item.Text.
type QuestionReply struct {
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}

// Item is one content block of a Claude Code transcript.
type Item struct {
	// Stored identifies an incomplete durable copy. Its source can be requested from
	// the existing full-item/image routes; availability still depends on the runtime.
	Stored    *StoredItem     `json:"stored,omitempty"`
	ID        string          `json:"id"`
	Seq       int             `json:"seq"`
	TS        time.Time       `json:"ts"`
	Role      string          `json:"role"`
	Kind      string          `json:"kind"`
	Text      string          `json:"text,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
	Image     *Image          `json:"image,omitempty"`
	Queued    bool            `json:"queued,omitempty"` // user input typed while the agent worked, absorbed mid-turn

	QuestionReplies    []QuestionReply `json:"question_replies,omitempty"`
	QuestionRepliesCut bool            `json:"question_replies_cut,omitempty"`
	// Outcome: what a tool_result's call came to (status, exit code, patch, git…), set by the node from the
	// runtime's record; absent on other items and from older nodes (ToolOutcome).
	Outcome *ToolOutcome `json:"outcome,omitempty"`
	// ClientMsgID: this item came from the message an app sent with this client_msg_id (session.send): its words,
	// its pictures, or the notice it became (/compact). Set by the node, which ties the transcript's line to the send
	// (node/echo.go); absent for anything else and from older nodes. docs/protocol/protocol.md "Matching pending messages".
	ClientMsgID string `json:"client_msg_id,omitempty"`
	// FullSize: Text was cut for pages and pushes (it ends in a "truncated, open for the full text" line, in Chinese); the whole of it, this many
	// bytes, comes from GET /api/v1/sessions/{mid}/{sid}/items/{item id}/full.
	FullSize int `json:"full_size,omitempty"`
	// SourceFingerprint identifies the original text record behind a positional item ID. Unlike the line
	// number, it does not match different content that reuses that position after a rewrite.
	SourceFingerprint string `json:"source_fingerprint,omitempty"`
	// Wake marks a background task's notice that started new work after the agent had finished its turn
	// (not one absorbed mid-turn): the exchange before it is over and its last words are an answer.
	Wake bool `json:"wake,omitempty"`
	// Narration marks public progress text (Claude narration or Codex commentary), including on unfolded pages.
	Narration bool `json:"narration,omitempty"`
	// From names the Claude session that sent this message (cross-session messaging), not the user.
	From string `json:"from,omitempty"`
	// Injected: this user line is a message Persona put into the conversation for the model (a task's notice, a
	// scheduled run, a wake-up…), not something the user wrote. Set by the hub from the line's text
	// (prompts.ParseInjected) on its way to the apps; docs/protocol/protocol.md "Messages injected by Persona".
	Injected *Injected `json:"injected,omitempty"`
	// Source is the author of an automated input, independent of its model role.
	// Unknown nonempty kinds are still not user-authored input.
	Source *MessageSource `json:"source,omitempty"`
	Fold   *Fold          `json:"fold,omitempty"`
	// SupervisionUser is a user's visible action folded into this work item. Ordinary items retain it across pages
	// and cache restores, matching the iOS model; old items without the field stay unmarked.
	SupervisionUser bool `json:"supervision_user,omitempty"`
	// Task: a background task notice, or a subagent tool result associated with its launching call.
	Task         *TaskNote     `json:"task,omitempty"`
	TurnDuration *TurnDuration `json:"turn_duration,omitempty"`
	// Compact: this notice is where the context was compacted (Claude Code's compact_boundary).
	Compact *Compact `json:"compact,omitempty"`
	// ModelRefusal preserves Claude Code's refusal record on a notice.
	ModelRefusal *ModelRefusal `json:"model_refusal,omitempty"`
	// CodexHeartbeatQuiet identifies a generated answer from a DONT_NOTIFY envelope, not model-written prose.
	// It travels from the node to the hub so the hub can localize Text without guessing from its wording.
	CodexHeartbeatQuiet bool `json:"codex_heartbeat_quiet,omitempty"`
	// Model is the model that wrote this assistant item as the transcript records it: Claude Code's message.model
	// (never its "<synthetic>" placeholder), a Codex turn's turn_context model. Absent when the transcript does not
	// say, on other roles, and from older nodes. Clients draw a divider where the model of the replies changes.
	Model string `json:"model,omitempty"`
	// Usage: the tokens of the model requests this item stands for, one entry per model and speed (TokenUsage, #5426).
	// Claude Code: a request's usage once, on the first item its lines made. Codex: the turn's total, on its
	// turn_duration item (on its interrupted notice when the user stopped it). A finished subagent's result or
	// completion notice: what its own transcript used and what each of its nested subagents used (in-process teammates
	// never), each agent under its own ID "agent:<agent id>"; a finished workflow's report: each of its run's agents the
	// same way. An agent reported on several items counts once in the chat. Absent on anything else and from older
	// nodes. A chat sums a turn's items (folds' Fold.Usage included) into the turn's usage
	// (docs/protocol/protocol.md "Token usage on items").
	Usage []TokenUsage `json:"usage,omitempty"`
	// Botmux is what this item says without the wrapper BotMux put around it (docs/integrations/engines.md "botmux"):
	// a Lark message BotMux handed the agent, a message sent through BotMux's trigger, or the agent's `botmux send`
	// reply. Text keeps the transcript's words, for the apps to show the original. Set by the node; absent for
	// anything else, for text it cannot read, and from older nodes.
	Botmux *Botmux `json:"botmux,omitempty"`
	// Visualization is set on KindVisualization items: a Codex visualize marker's reference to an HTML fragment on
	// the node. The bytes are not carried here; the session visualization route serves them after path checks.
	Visualization *Visualization `json:"visualization,omitempty"`
	// TTFTMS is the observed time-to-first-token in milliseconds from turn send/request start to the first
	// assistant token/chunk during live streaming execution. Nil when not observed (such as historical replays).
	TTFTMS *int64 `json:"ttft_ms,omitempty"`
	// ToolViews are out-of-band declarative views attached to a tool_result by the node when recording the turn,
	// rendered inline in conversation UIs and never included in model context.
	ToolViews []wire.View `json:"tool_views,omitempty"`
	// RawTextSHA256 and RawTextTrimmedSHA256 hold the SHA-256 hex digest of the untruncated user Text (and its
	// \r\n-trimmed form when different), populated before light/clip truncation for local node-side matching
	// (e.g. BotMux structured turn anchors). Never serialized over the wire.
	RawTextSHA256        string `json:"-"`
	RawTextTrimmedSHA256 string `json:"-"`
}

type StoredItem struct {
	Complete    bool   `json:"complete"`
	SourceBytes int    `json:"source_bytes"`
	MachineID   string `json:"machine_id"`
	SessionID   string `json:"session_id"`
	History     string `json:"history"`
	ItemID      string `json:"item_id"`
}

// Visualization is one Codex visualize reference (the visualize plugin's in-conversation HTML contract): the
// fragment's absolute path on the node, its display mode ("" for the regular 736 px surface, "wide" for up to
// 1024 px) and an optional title.
type Visualization struct {
	Path  string `json:"path"`
	Mode  string `json:"mode,omitempty"`
	Title string `json:"title,omitempty"`
}

// Botmux is a BotMux-wrapped item's own words and the facts the apps show as tags (docs/protocol/protocol.md
// "BotMux messages"). Only display names leave the node: never a Lark open_id, email or message id.
type Botmux struct {
	Kind BotmuxKind `json:"kind"` // BotmuxMessage | BotmuxTrigger | BotmuxReply
	Text string     `json:"text"`
	// Sender is the Lark sender's display name and SenderType its kind ("user", "bot"…), when BotMux named one.
	Sender     string           `json:"sender,omitempty"`
	SenderType BotmuxSenderType `json:"sender_type,omitempty"`
	// Quoted: the message quoted another one; TopicContext: the topic had messages before the agent joined it.
	Quoted       bool `json:"quoted,omitempty"`
	TopicContext bool `json:"topic_context,omitempty"`
	// Thread: the message was posted in a topic (BotMux's placement "thread"), not to the chat itself.
	Thread         bool               `json:"thread,omitempty"`
	Attachments    int                `json:"attachments,omitempty"`
	AttachmentMeta []BotmuxAttachment `json:"attachment_meta,omitempty"`
	Mentions       []string           `json:"mentions,omitempty"`
	// Source names who handed a trigger turn to BotMux (its sourceName, "Persona" for the apps' own sends).
	Source string `json:"source,omitempty"`
	// Final and Voice describe a reply: BotMux's --response-kind final, and a voice message.
	Final bool `json:"final,omitempty"`
	Voice bool `json:"voice,omitempty"`
	// Truncated: the item's text was clipped inside the wrapper, so Text is only its start.
	Truncated bool `json:"truncated,omitempty"`
}

// BotmuxAttachment is safe attachment metadata from BotMux's structured turn record (never a local path or Lark id).
type BotmuxAttachment struct {
	Kind     BotmuxAttachmentKind `json:"kind"`
	Name     string               `json:"name,omitempty"`
	MimeType MIMEType             `json:"mime_type,omitempty"`
}

// BotmuxKind identifies the role of a BotMux-decoded transcript item.
type BotmuxKind string

// Botmux kinds.
const (
	BotmuxMessage BotmuxKind = "message"
	BotmuxTrigger BotmuxKind = "trigger"
	BotmuxReply   BotmuxKind = "reply"
)

// Valid reports whether k is one of the known BotMux item kinds.
func (k BotmuxKind) Valid() bool {
	return k == BotmuxMessage || k == BotmuxTrigger || k == BotmuxReply
}

// BotmuxSenderType identifies the sender category reported by BotMux (for example "user" or "bot").
type BotmuxSenderType string

const (
	BotmuxSenderUser BotmuxSenderType = "user"
	BotmuxSenderBot  BotmuxSenderType = "bot"
)

// BotmuxAttachmentKind identifies the kind of an attachment reported by BotMux (for example "image", "file", "audio").
type BotmuxAttachmentKind string

// MIMEType is an IANA media type reported on wire metadata (for example "image/png" or "application/pdf").
type MIMEType string

// TokenUsage is what model requests used, as the transcript records it (#5426): tokens by kind, the way the usage
// meter counts them (UsageBucket: Input excludes the cache). Model is the model the transcript names (never Claude
// Code's "<synthetic>"); empty when it does not say.
type TokenUsage struct {
	// ID: entries with the same ID (and model) are one report, counted once; empty: count it as it is.
	ID         string `json:"id,omitempty"`
	Model      string `json:"model,omitempty"`
	Input      int64  `json:"in,omitempty"`  // input not read from the cache
	Output     int64  `json:"out,omitempty"` // Codex: reasoning included
	CacheRead  int64  `json:"cache_read,omitempty"`
	CacheWrite int64  `json:"cache_write,omitempty"` // every cache write, the 1-hour ones included
	// CacheWrite1h: of CacheWrite, what was written for an hour (priced higher than the 5-minute writes).
	CacheWrite1h int64 `json:"cache_write_1h,omitempty"`
	// Fast: the requests ran at Claude Code's fast speed (usage.speed "fast"), priced higher than the standard rate.
	Fast bool `json:"fast,omitempty"`
}

// ModelRefusal is a Claude Code model_refusal_* record as clients see it: its known fields, values kept as recorded
// (unknown subtypes, categories and scopes included), and Text, the refusal's own words (the record's string
// content). Nothing else of the record leaves the node (#2528): the line may carry fields no one reviewed.
type ModelRefusal struct {
	Subtype       string `json:"subtype"`
	OriginalModel string `json:"original_model,omitempty"`
	FallbackModel string `json:"fallback_model,omitempty"`
	Category      string `json:"category,omitempty"`
	Scope         string `json:"scope,omitempty"`
	Explanation   string `json:"explanation,omitempty"`
	Text          string `json:"text,omitempty"`
	// Raw is the whole JSONL line, for the node's own use only (telling one record from another): never encoded, so
	// no RPC, REST answer, push or snapshot carries it. Older builds sent it as "raw"; clients must not show that.
	Raw string `json:"-"`
	// The following fields are present only when the node projects its own bounded recovery onto this fallback shape
	// (#7824): a runtime record never sets them, so older clients ignore them safely. RecoveryReset says when the
	// configured model is available again; RecoveryExhausted marks an exhausted chain; RecoveryReturning that the
	// configured model answered a probe and the next request runs on it again; RecoveryTried lists every model tried,
	// in order, joined by " → " (a string, not a slice, so ModelRefusal stays comparable with ==).
	RecoveryReset     *time.Time `json:"recovery_reset,omitempty"`
	RecoveryExhausted bool       `json:"recovery_exhausted,omitempty"`
	RecoveryReturning bool       `json:"recovery_returning,omitempty"`
	RecoveryTried     string     `json:"recovery_tried,omitempty"`
}

// ModelRestore is a pending "back to the configured model" (node session.restore_model, #2528): State
// ModelRestoreNextStart says the session's idle headless process was closed (or none ran), so its next start asks for
// To, the model the conversation is configured with as the node read it at At. Empty To means the launcher default.
// Nothing about which model runs.
type ModelRestore struct {
	State string    `json:"state"`
	To    string    `json:"to,omitempty"`
	At    time.Time `json:"at"`
}

// ModelRestoreNextStart: the next start of the session's process uses the configured model.
const ModelRestoreNextStart = "next_start"

// ModelRecoveryChoice is the non-secret execution identity the conversation asked for before a recovery switch.
// A pointer keeps records written by older nodes distinguishable: nil means no recorded choice, not a zero-valued one.
type ModelRecoveryChoice struct {
	Effort      string `json:"effort,omitempty"`
	UseEffort   bool   `json:"use_effort,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Launcher    string `json:"launcher,omitempty"`
}

// ModelRecovery records the node's own bounded recovery moving a conversation off its configured model (#7824).
// It is node-owned state persisted through session state, not a transcript record: the runtime's own refusal fallback
// has ModelRefusal, while a switch the node drives by restarting the runtime on the next launcher model had no
// client-visible evidence before this. Main and leader/supervisor conversations recover automatically and return to
// the configured model as soon as it is available again; worker Agents keep their own behavior.
type ModelRecovery struct {
	// From is the configured model the conversation asked for; empty and "default" both mean the launcher default.
	// To is the model that answers after the switch.
	From string `json:"from"`
	To   string `json:"to"`
	// Choice is the execution identity From had when this chain began, preserved independently of the session's
	// current model and effort, which recovery sends can overwrite. Nil on records written before this field existed.
	Choice *ModelRecoveryChoice `json:"choice,omitempty"`
	// ProbeFrom is the concrete configured default used only for availability probes and unavailable-cache cleanup.
	// It never replaces From in the execution request, so returning does not pin a launcher default to a concrete model.
	ProbeFrom string `json:"probe_from,omitempty"`
	// Reason is one of the ModelRecoveryReason* values: why From could not answer.
	Reason string `json:"reason"`
	// At is when the node committed the switch to To.
	At time.Time `json:"at"`
	// ResetsAt is when the upstream said From becomes available again (a gateway quota's "available again at …"),
	// when it named one. Nil when the upstream gave no time; clients then offer a manual retry without a countdown.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Tried lists every model the recovery chain attempted, in order, including the configured model first.
	Tried []string `json:"tried,omitempty"`
	// ChainExhausted marks the final record when every model in the chain failed: the turn ends on the failure card,
	// which offers Retry and names the cause and ResetsAt. A later successful switch replaces it.
	ChainExhausted bool `json:"chain_exhausted,omitempty"`
	// Returning says From answered an availability probe: the conversation stays on To only until its next request,
	// which runs on From again. It makes the automatic return visible before the model actually changes.
	Returning bool `json:"returning,omitempty"`
}

// ModelRecovery Reason values.
const (
	// ModelRecoveryReasonSafeguards: the upstream answered with a safety-classifier refusal (stop_reason "refusal").
	ModelRecoveryReasonSafeguards = "safeguards_refusal"
	// ModelRecoveryReasonQuota: the gateway's account quota is exhausted (its quota_exhausted 429).
	ModelRecoveryReasonQuota = "quota_exhausted"
	// ModelRecoveryReasonRateLimit: ordinary throttling, not a quota window.
	ModelRecoveryReasonRateLimit = "rate_limited"
	// ModelRecoveryReasonServer: a 5xx/529 or another transient server failure.
	ModelRecoveryReasonServer = "server_error"
	// ModelRecoveryReasonUnavailable: the model is not served (a 404/model_not_found on a recovery-picked model).
	ModelRecoveryReasonUnavailable = "model_unavailable"
)

// MessageSource preserves an input's provenance without changing the model's role.
// ConversationID is a stable session id, never a peer's display name (Item.From).
type MessageSource struct {
	Kind           string `json:"kind"`
	ConversationID string `json:"conversation_id,omitempty"`
	Title          string `json:"title,omitempty"`
	// Push is a scheduled input's delivery mode, persisted with the input so a queued user message cannot change it.
	// "asked" suppresses that run's normal completion; absent keeps the existing every-answer behavior.
	Push string `json:"push,omitempty"`
	// ScheduleRunID correlates a reference-only prompt with the run that sent it.
	ScheduleRunID string `json:"schedule_run_id,omitempty"`
}

const (
	A2ADeliveryPrefix   = "persona-a2a-" // Manager-assigned client message IDs for A2A deliveries.
	SourceConversation  = "conversation"
	SourcePatrol        = "patrol"
	SourceFollowup      = "followup"
	SourceAutoContinue  = "auto-continue" // a hub-generated continuation after a transient error or usage-limit reset
	SourceSupervision   = "supervision"
	SourceNotice        = "notice"         // a notice relayed by Persona
	SourceScheduledTask = "scheduled_task" // a scheduler outside Persona, e.g. a Codex heartbeat
	// SourceMeeting is a batch of a recorded meeting the session attends (docs/assistant/meeting-minutes.md "Feeding
	// the attending session"): other people's words, never the user's; Title is the meeting's name.
	SourceMeeting = "meeting"
	// SourceExtension: an event an extension observed and reported (assistant.event). Title is the extension's name.
	SourceExtension = "extension"
	// SourceSideConversation is an agent_send request wrapped by RenderSideConversation: another conversation's
	// request to an Agent that already holds a task, never the owner answering that task (prompts/assistant/side_conversation.md).
	SourceSideConversation = "side_conversation"
)

// SourceFromInjected upgrades existing metadata without guessing from ordinary text.
func SourceFromInjected(in *Injected) *MessageSource {
	if in == nil {
		return nil
	}
	kind := ""
	switch {
	case in.Kind == InjectedFollowup && (in.State == "daily" || in.State == "interval"):
		kind = SourcePatrol
	case in.Kind == InjectedFollowup:
		kind = SourceFollowup
	case in.Kind == InjectedSystem && in.State == "supervise":
		kind = SourceSupervision
	}
	if kind == "" {
		return nil
	}
	return &MessageSource{Kind: kind, Title: in.Title}
}

// Injected says what a message Persona wrote for the model is, for the apps to show one line instead of it.
type Injected struct {
	// Level classifies supervision: info updates only, triage folds the turn, user is visible.
	Level   string `json:"level,omitempty"`
	Kind    string `json:"kind"`              // InjectedTaskNotice | InjectedFollowup | InjectedSystem
	State   string `json:"state,omitempty"`   // task_notice: done|needs|interrupted|failed; followup: once|daily|interval|wake; system: lost_tasks|supervise|inbox|ask_answered|ask_expired|ask_closed
	Title   string `json:"title,omitempty"`   // the task's, the scheduled task's, the inbox item's, what a woken agent waits for, or a question's answers
	Machine string `json:"machine,omitempty"` // task_notice: where its agent runs (a name)
	TodoID  string `json:"todo_id,omitempty"` // task_notice: the task, when the hub can tell
	Line    string `json:"line"`              // the one line to show
	Detail  string `json:"detail,omitempty"`  // followup once|daily|interval: what the task asks, as the user wrote it
}

// Injected kinds.
const (
	InjectedTaskNotice = "task_notice"
	InjectedFollowup   = "followup"
	InjectedSystem     = "system"
)

// Compact says how a compaction came about: Trigger "manual" (/compact) or "auto", and the context's size before it.
type Compact struct {
	Trigger   string `json:"trigger,omitempty"`
	PreTokens int    `json:"pre_tokens,omitempty"`
}

// Compaction states.
const (
	CompactionRunning = "running"
	CompactionDone    = "done"
	CompactionFailed  = "failed"
)

// Compaction is a compaction the user asked for: State running, done or failed (Error says why).
type Compaction struct {
	State      string     `json:"state"`
	Error      string     `json:"error,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TurnDuration preserves a recorded whole-turn duration and its native association.
type TurnDuration struct {
	DurationMS int64  `json:"duration_ms"`
	Source     string `json:"source"`
	TurnID     string `json:"turn_id,omitempty"`
	ParentUUID string `json:"parent_uuid,omitempty"`
	UserItemID string `json:"user_item_id,omitempty"`
}

// TaskNote is what a <task-notification> says, read from the notification alone (the call it came from is found by
// the app, through TaskID / ToolUseID): a command run in the background finishing, a Monitor's event or its end, a
// subagent finishing. Title, Detail and Text come decoded (Claude Code writes &gt; &lt; &amp; in them).
type TaskNote struct {
	Kind   string `json:"kind"`             // shell | monitor | agent | workflow | task (anything else)
	Status string `json:"status,omitempty"` // running | completed | failed | killed | stopped | event | ended | waiting
	// Title: the call's description, or the first line of its command when it had none.
	Title      string `json:"title"`
	TaskID     string `json:"task_id,omitempty"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	ExitCode   *int   `json:"exit_code,omitempty"`
	OutputFile string `json:"output_file,omitempty"`
	// Detail: a Monitor's event, a subagent's result, or a command's whole text when the title is only its first line.
	Detail string `json:"detail,omitempty"`
}
