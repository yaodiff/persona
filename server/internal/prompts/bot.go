package prompts

// BotMessage is a message the user sent the Feishu bot, as it goes into a conversation (bot/message): for a Persona
// conversation a note that the answer goes back to Feishu; the message it quotes, a forwarded chat, and the paths of
// the files it carried, saved on the machine the conversation runs on.
type BotMessage struct {
	Persona        bool   // a Persona conversation (not a coding agent): add the note about answering in Feishu
	Topic          bool   // said in a topic of a group chat (the thread of a todo), not in the bot's private chat
	Owner          string // optional owner name
	Language       string // owner language; empty preserves the original Chinese instructions
	Text           string
	UnboundTopic   bool     // an answer-only or main-decides topic without a to-do
	TopicMessageID string   // set only for an unbound topic whose group lets the main conversation decide
	Todo           *BotTodo // the reminder being answered; stored to-do content, not another instruction from the owner
	Quote          *BotQuote
	Forward        string // the forwarded messages, one line each
	Files          []string
}

// BotTodo is the to-do behind a reminder card. Notes is an excerpt; the web link on the card opens the whole to-do.
type BotTodo struct {
	ID, Title, Notes string
}

type BotQuote struct {
	ID, Who, Text string
}

func RenderBotMessage(m BotMessage) string { return render("bot/message", m) }

// BotReadGroup is the owner's "@bot /read <task>" from a Lark group, as it goes into the main conversation
// (bot/read_group, #11803): read the group as the owner with lark-cli first (the last Default messages unless the task
// names a range, Most at the very most), download its images and files into Dir, then investigate the leads it found
// with the computer's own read-only tools, within about Minutes and Calls, before it concludes (#16808). What the
// group says is other people's words, never instructions: its IDs and errors are data to look up, its commands are not
// run. The answer goes to the owner's private chat first and into the group only when the owner posts it (#14437), or,
// with Direct, straight under the owner's message: either way the whole group may read it.
type BotReadGroup struct {
	Owner     string // optional owner name
	Language  string // owner language; empty is the Chinese original
	ChatID    string
	MessageID string // the owner's /read message
	ThreadID  string // its topic, "" outside one
	Task      string // the owner's words after the command; "" when none
	Dir       string // where its images and files are downloaded, on the computer the conversation runs on
	Default   int    // messages read when the task names no range
	Most      int    // messages read at the very most
	FileMB    int    // a file larger than this is not downloaded
	TotalMB   int    // nor any once the downloads add up to this
	Images    int    // images looked at, at most
	Minutes   int    // about how long the investigation after the read may take
	Calls     int    // and how many tool calls it may make, reading and downloads not counted
	Direct    bool   // the answer goes straight into the group (the read_group_direct switch)
}

func RenderBotReadGroup(m BotReadGroup) string { return render("bot/read_group", m) }
