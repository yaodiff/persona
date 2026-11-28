package proto

// Orca bots (docs/integrations/orca.md "Bots found on a computer", #16891): the bots of the Orca app on one
// computer, as that computer's node reads them from Orca's local runtime socket (bots.roster, bots.chats,
// bots.items.list) and answers the manager's agentbot.roster and agentbot.items. Nothing here goes through Orca
// Cloud. Orca's own words (an item's status, a reply's label) are kept as Orca wrote them.

// OrcaRef is an identifier Orca gives one of its own things: a bot, a chat (an Orca structured session) or an item. It
// is opaque to Persona and never a Persona session or machine id: the Persona session of a chat is the Claude session
// its node lists it under.
type OrcaRef string

// OrcaBotRoster is what agentbot.roster answers: the bots of the computer's Orca. Running is false when Orca is not
// running there (no bots then, and nothing failed).
type OrcaBotRoster struct {
	Running bool      `json:"running"`
	Bots    []OrcaBot `json:"bots"`
}

// OrcaBot is one bot. BotID is Orca's id for it, the same for the bot's whole life (a new Home Chat keeps it).
// HomeSessionID is the Orca session of its open Home Chat, empty while it has none; every read and send of the bot
// resolves it again, so a bot that started a new Home Chat is followed there (bots.chats). Folder is the bot's home
// folder on the computer (where its Home Chat runs), empty while that chat is not listed. NeedsYou and Open count its
// items waiting on the owner and still open.
type OrcaBot struct {
	BotID         OrcaRef `json:"bot_id"`
	Name          string  `json:"name"`
	Agent         string  `json:"agent,omitempty"`
	Paused        bool    `json:"paused,omitempty"`
	Specialty     string  `json:"specialty,omitempty"`
	HomeSessionID OrcaRef `json:"home_session_id,omitempty"`
	HomeTitle     string  `json:"home_title,omitempty"`
	LastActiveMs  int64   `json:"last_active_ms,omitempty"`
	Folder        string  `json:"folder,omitempty"`
	NeedsYou      int     `json:"needs_you,omitempty"`
	Open          int     `json:"open,omitempty"`
}

// OrcaBotItems is what agentbot.items answers: one bot's open items, newest activity first.
type OrcaBotItems struct {
	BotID OrcaRef       `json:"bot_id"`
	Name  string        `json:"name"`
	Items []OrcaBotItem `json:"items"`
}

// OrcaItemStatus is an item's status in Orca's words. Waiting waits on something outside the bot and its owner;
// needs_you waits on the owner. A status this side does not know keeps its raw string and is not Valid.
type OrcaItemStatus string

// Orca's item statuses (Orca's BOT_ITEM_STATUSES).
const (
	OrcaItemPlanned  OrcaItemStatus = "planned"
	OrcaItemWorking  OrcaItemStatus = "working"
	OrcaItemWaiting  OrcaItemStatus = "waiting"
	OrcaItemNeedsYou OrcaItemStatus = "needs_you"
	OrcaItemDone     OrcaItemStatus = "done"
	OrcaItemCanceled OrcaItemStatus = "canceled"
)

// Valid reports whether s is one of Orca's item statuses.
func (s OrcaItemStatus) Valid() bool {
	switch s {
	case OrcaItemPlanned, OrcaItemWorking, OrcaItemWaiting, OrcaItemNeedsYou, OrcaItemDone, OrcaItemCanceled:
		return true
	}
	return false
}

// OrcaBotItem is one item: what the bot keeps track of for its owner. ChatSessionID is the Orca session of the chat it
// was raised in. Question is the bot's open question to the owner, set while the item needs them.
type OrcaBotItem struct {
	ItemID        OrcaRef           `json:"item_id"`
	Number        int               `json:"number,omitempty"`
	Title         string            `json:"title"`
	DoneMeans     string            `json:"done_means,omitempty"`
	Status        OrcaItemStatus    `json:"status"`
	ChatSessionID OrcaRef           `json:"chat_session_id,omitempty"`
	ChatTitle     string            `json:"chat_title,omitempty"`
	LastActiveMs  int64             `json:"last_active_ms,omitempty"`
	Question      *OrcaItemQuestion `json:"question,omitempty"`
}

// OrcaItemQuestion is a bot's question on an item: its event (EventID, what an answer names), a one-line Gist, the
// whole Text (Markdown) and the decisions it offers quick replies for. An answer to a single decision is the reply's
// label; with several, one line per decision, "<question line> → <label>" (Orca's own format).
type OrcaItemQuestion struct {
	EventID   OrcaRef            `json:"event_id"`
	Gist      string             `json:"gist,omitempty"`
	Text      string             `json:"text,omitempty"`
	Decisions []OrcaItemDecision `json:"decisions,omitempty"`
}

// OrcaItemDecision is one question of an ask with its quick replies; Multi when several may be picked.
type OrcaItemDecision struct {
	Question string          `json:"question"`
	Multi    bool            `json:"multi,omitempty"`
	Replies  []OrcaItemReply `json:"replies"`
}

// OrcaItemReply is one quick reply: its label (what answering with it posts), what it means, and whether the bot
// recommends it.
type OrcaItemReply struct {
	Label       string `json:"label"`
	Detail      string `json:"detail,omitempty"`
	Recommended bool   `json:"recommended,omitempty"`
}
