package assistantwire

import "time"

// Group access on the built-in Lark bot (docs/extensions/bots.md "Group access for teammates").
// The manager owns the policy and every gate; the types here are the contract the clients and the bus share.

// FeishuOpenID is a Lark open-platform user id scoped to the bot's app (ou_…).
type FeishuOpenID string

// FeishuGuestID names one guest trigger ("bg_" and hex), chosen by the manager.
type FeishuGuestID string

// FeishuGroupAccessMode is who besides the owner may @ the bot in a group.
type FeishuGroupAccessMode string

const (
	FeishuGroupOwnerOnly              FeishuGroupAccessMode = "owner_only"
	FeishuGroupAllowlist              FeishuGroupAccessMode = "allowlist"
	FeishuGroupEveryoneInAllowedChats FeishuGroupAccessMode = "everyone_in_allowed_chats"
)

// Valid says whether m is one of the three modes a client may send.
func (m FeishuGroupAccessMode) Valid() bool {
	return m == FeishuGroupOwnerOnly || m == FeishuGroupAllowlist || m == FeishuGroupEveryoneInAllowedChats
}

// Effective is the mode in force: an empty stored value is owner_only.
func (m FeishuGroupAccessMode) Effective() FeishuGroupAccessMode {
	if !m.Valid() {
		return FeishuGroupOwnerOnly
	}
	return m
}

// FeishuGroupUser is a person on one of the policy's lists.
type FeishuGroupUser struct {
	OpenID FeishuOpenID `json:"open_id"`
	Name   string       `json:"name,omitempty"`
}

// FeishuGroupChat is a group on one of the policy's lists.
type FeishuGroupChat struct {
	ChatID FeishuChatID `json:"chat_id"`
	Name   string       `json:"name,omitempty"`
}

// FeishuGroupAccessPolicy is who else may @ the bot in a group, and what a guest may use.
type FeishuGroupAccessPolicy struct {
	Mode          FeishuGroupAccessMode `json:"mode"`
	AllowedUsers  []FeishuGroupUser     `json:"allowed_users"`
	AllowedChats  []FeishuGroupChat     `json:"allowed_chats"`
	BlockedUsers  []FeishuGroupUser     `json:"blocked_users"`
	BlockedChats  []FeishuGroupChat     `json:"blocked_chats"`
	GuestTools    []string              `json:"guest_tools"`
	PerChatHourly int                   `json:"per_chat_hourly"`
}

// FeishuGuestToolOption is a read-only connector tool the owner may give guests.
type FeishuGuestToolOption struct {
	Name      string `json:"name"`
	Connector string `json:"connector"` // the connector's display name
}

// FeishuGroupRequest is a recent @bot the policy refused (memory only): whom to allow, without looking ids up.
type FeishuGroupRequest struct {
	OpenID   FeishuOpenID `json:"open_id"`
	Name     string       `json:"name,omitempty"`
	ChatID   FeishuChatID `json:"chat_id"`
	ChatName string       `json:"chat_name,omitempty"` // the topic group's label, when it is one
	At       int64        `json:"at"`                  // unix seconds
}

// FeishuGroupAccessLimits are the bounds the hub enforces, so every client shows the same ones.
type FeishuGroupAccessLimits struct {
	ListMax              int `json:"list_max"`                // 200
	GuestToolsMax        int `json:"guest_tools_max"`         // 8
	PerChatHourlyMin     int `json:"per_chat_hourly_min"`     // 1
	PerChatHourlyMax     int `json:"per_chat_hourly_max"`     // 120
	PerChatHourlyDefault int `json:"per_chat_hourly_default"` // 20
}

// FeishuGroupAccessResponse is the body of GET and PUT /api/v1/bots/feishu/group-access.
type FeishuGroupAccessResponse struct {
	Revision       int64                   `json:"revision"`
	Paired         bool                    `json:"paired"`
	Policy         FeishuGroupAccessPolicy `json:"policy"`
	AvailableTools []FeishuGuestToolOption `json:"available_tools"`
	RecentRequests []FeishuGroupRequest    `json:"recent_requests"`
	Limits         FeishuGroupAccessLimits `json:"limits"`
}

// FeishuGroupAccessUpdate is the body of PUT: the whole policy, and the revision it was read at.
type FeishuGroupAccessUpdate struct {
	Revision int64                   `json:"revision"`
	Policy   FeishuGroupAccessPolicy `json:"policy"`
}

// FeishuGuestTurn is feishu.guest's params. The question and the name are another person's, untrusted, and are
// never stored with the bot's settings. The assistant is not told the message id: the hub alone decides where the
// reply lands.
type FeishuGuestTurn struct {
	ID           FeishuGuestID `json:"id"`
	ChatID       FeishuChatID  `json:"chat_id"`
	Conversation string        `json:"conversation"` // thread, root or message id: one guest conversation each
	SenderName   string        `json:"sender_name,omitempty"`
	Question     string        `json:"question"`
	Tools        []string      `json:"tools"` // owner-listed read-only connector tools
	Launcher     string        `json:"launcher,omitempty"`
	ExpiresAt    time.Time     `json:"expires_at"` // At + botGuestReplyFor
}
