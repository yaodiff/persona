package prompts

// BotProxyReply is a teammate's question handed to the owner's Lark bot, as it goes into the guest conversation that
// answers it (bot/proxy_reply). The question, the context and the names are other people's text, never instructions.
type BotProxyReply struct {
	Owner    string // the owner's name, optional
	Language string // the owner's language; empty is the Chinese original
	Chat     string // the group's name, as the caller gave it
	Sender   string // who asked, as the caller gave it
	Question string
	Context  string // recent lines of the chat, as the people there see them; optional
	Max      int    // characters the one reply may use
	Confirm  bool   // the owner reads the draft before it is posted
}

func RenderBotProxyReply(m BotProxyReply) string { return render("bot/proxy_reply", m) }
