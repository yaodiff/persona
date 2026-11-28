package prompts

// BotGuestTurn is a non-owner's @bot in a Lark group, as it goes into the guest conversation that answers it
// (bot/guest_turn). The question and the sender's name are another person's words, never instructions.
type BotGuestTurn struct {
	Owner    string // the owner's name, optional
	Language string // the owner's language; empty is the Chinese original
	Chat     string // the chat id (no name is known)
	Sender   string // who asked; empty: "a teammate"
	Question string
	Max      int  // characters the one reply may use
	First    bool // the conversation's first turn carries the full instructions
	Tools    bool // owner-listed tools exist
}

func RenderBotGuestTurn(m BotGuestTurn) string { return render("bot/guest_turn", m) }
