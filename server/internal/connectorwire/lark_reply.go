package connectorwire

import "time"

// LarkReplyWatch is an explicit source identity, not a person's display name or a model's Inbox association.
type LarkReplyWatch struct {
	Source           string `json:"source"`
	Brand            string `json:"brand"`
	AppID            string `json:"app_id"`
	AccountOpenID    string `json:"account_open_id"`
	PersonOpenID     string `json:"person_open_id"`
	ChatID           string `json:"chat_id"`
	ThreadID         string `json:"thread_id"`
	RequestMessageID string `json:"request_message_id"`
}

// LarkReplyMessage keeps only source identity and time, never conversation text. Before is the exclusive upper
// bound of the source timestamp's precision; an ambiguous minute cannot prove ordering within that minute.
type LarkReplyMessage struct {
	ID         string    `json:"id"`
	SenderID   string    `json:"sender_id"`
	SenderType string    `json:"sender_type"`
	IDType     string    `json:"id_type"`
	AppID      string    `json:"app_id,omitempty"`
	ChatID     string    `json:"chat_id"`
	ThreadID   string    `json:"thread_id"`
	URL        string    `json:"url,omitempty"`
	At         time.Time `json:"at"`
	Before     time.Time `json:"before"`
}

// LarkReplyObservation is the bounded reader's evidence. Watch echoes the identity actually checked by the source,
// so a peer ignoring a new request field cannot accidentally satisfy a watch. No request or reply is a no-match.
type LarkReplyObservation struct {
	Watch     LarkReplyWatch   `json:"watch"`
	Request   LarkReplyMessage `json:"request"`
	Reply     LarkReplyMessage `json:"reply"`
	CheckedAt time.Time        `json:"checked_at"`
}

// LarkAppID identifies the application used to read a linked Lark topic.
type LarkAppID string

// LarkOpenID identifies a user within a Lark application.
type LarkOpenID string

// LarkChatID identifies a chat containing a linked Lark topic.
type LarkChatID string

// LarkThreadID identifies a topic within a Lark chat.
type LarkThreadID string

// LarkTopicIdentity is empty only when establishing the binding's configured source for the first time.
type LarkTopicIdentity struct {
	Brand         string     `json:"brand"`
	AppID         LarkAppID  `json:"app_id"`
	AccountOpenID LarkOpenID `json:"account_open_id"`
}

// LarkTopicWatch observes activity in an already linked topic.
type LarkTopicWatch struct {
	Identity LarkTopicIdentity `json:"identity"`
	ChatID   LarkChatID        `json:"chat_id"`
	ThreadID LarkThreadID      `json:"thread_id"`
	Since    time.Time         `json:"since"`
}

type LarkTopicObservation struct {
	Watch     LarkTopicWatch     `json:"watch"`
	Messages  []LarkReplyMessage `json:"messages"`
	CheckedAt time.Time          `json:"checked_at"`
}
