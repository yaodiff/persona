package agents

import (
	"encoding/json"
)

// The personal agents' wire shapes: what a source answers and the apps read (docs/protocol/protocol.md "Personal Agent source status"), the same
// fields every source uses (they began as the Lark gateway's agent-bridge's, which a plugin serves now).

// AgentMaxText is the longest message a source accepts (characters, as JavaScript counts them).
const AgentMaxText = 20000

type AgentProvider struct {
	Provider   string `json:"provider"`
	Connection string `json:"connection,omitempty"` // server | browser
	Available  bool   `json:"available"`
}

type AgentAccount struct {
	AccountID         string `json:"accountId"`
	Provider          string `json:"provider"`
	DisplayName       string `json:"displayName,omitempty"`
	Status            string `json:"status,omitempty"` // active, paused, …
	ExternalAccountID string `json:"externalAccountId,omitempty"`
	LastSyncedAt      int64  `json:"lastSyncedAt,omitempty"`
	LastError         string `json:"lastError,omitempty"`
	SessionCount      int    `json:"sessionCount"`
}

type AgentSession struct {
	SessionKey       string `json:"sessionKey"` // e.g. agent:grok_bot:<accountId>:<uuid>/ or agent:muse:<accountId>:main
	AccountID        string `json:"accountId"`
	Provider         string `json:"provider"`
	Title            string `json:"title"`
	Kind             string `json:"kind"` // main, …
	ParentSessionKey string `json:"parentSessionKey,omitempty"`
	CanSend          bool   `json:"canSend"`
	UnreadCount      int    `json:"unreadCount"`
	RankTimeMs       int64  `json:"rankTimeMs"`
	// PreviewText is the latest message's text as the source shows it in its own list, PreviewIsMine whether the user
	// sent it; OnTopRankTimeMs is when the user pinned the session at the source (0: not pinned there). All three are
	// optional: older sources leave them out and the apps show the row without a preview.
	PreviewText     string `json:"previewText,omitempty"`
	PreviewIsMine   bool   `json:"previewIsMine,omitempty"`
	OnTopRankTimeMs int64  `json:"onTopRankTimeMs,omitempty"`
	// Quota is what is left of the account's allowance at its service, when the source knows: {state, remaining?,
	// percent?, tone?, kind?, label?, selected?, resetsAt?, checkedAt?, source?, sourceNote?, stale?, show?, buckets?}.
	// The hub reads none of it, so it is kept as the source wrote it and fields added later pass through; the apps
	// draw it as a charge. Optional: older sources leave it out.
	Quota json.RawMessage `json:"quota,omitempty"`
}

// AgentBlock is one block of a message: text, tool-call {name,status,summary,input}, tool-result, or anything
// newer, whose JSON is kept in Raw and written back unchanged.
type AgentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	Status  string          `json:"status,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

func (b *AgentBlock) UnmarshalJSON(data []byte) error {
	type plain AgentBlock
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*b = AgentBlock(p)
	b.Raw = append(json.RawMessage(nil), data...)
	return nil
}

func (b AgentBlock) MarshalJSON() ([]byte, error) {
	if len(b.Raw) > 0 {
		return b.Raw, nil
	}
	type plain AgentBlock
	return json.Marshal(plain(b))
}

type AgentLocal struct {
	ClientID string `json:"clientId"`
	Status   string `json:"status"` // pending | claimed | sent | unconfirmed | failed
}

type AgentMessage struct {
	ExternalID   string       `json:"externalId"`
	SessionKey   string       `json:"sessionKey"`
	Role         string       `json:"role"` // user | assistant | tool (seen); others possible
	Blocks       []AgentBlock `json:"blocks"`
	CreateTimeMs int64        `json:"createTimeMs"`
	Position     int64        `json:"position"`
	Local        *AgentLocal  `json:"local,omitempty"` // only on messages sent through the bridge
}

type AgentMessagePage struct {
	Session    *AgentSession  `json:"session,omitempty"`
	Messages   []AgentMessage `json:"messages"`
	HasMore    bool           `json:"hasMore"`
	NextCursor string         `json:"nextCursor,omitempty"` // opaque; pass back as cursor for older messages
}

type AgentSearchHit struct {
	SessionKey   string `json:"sessionKey"`
	ExternalID   string `json:"externalId"`
	Text         string `json:"text"`
	Role         string `json:"role"`
	CreateTimeMs int64  `json:"createTimeMs"`
	Title        string `json:"title"`
	Provider     string `json:"provider"`
}

// AgentFeed is GET /agent-bridge/feed: sessions shaped as Lark gateway feed items (ids prefixed "agent:"); kept raw.
