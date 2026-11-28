package assistantwire

// BotCard is a message card (card JSON 2.0: Markdown with headings, tables and code blocks; Feishu 7.20 and later).
// Buttons carry a value that comes back in the card.action.trigger callback (CardAction.Action.Value); buttons inside
// a form submit it too (CardAction.Action.FormValue). 2.0 rejects unknown fields, so only documented ones are written.
type BotCard struct {
	Schema string         `json:"schema"`
	Config *BotCardConfig `json:"config"`
	Header *BotCardHeader `json:"header,omitempty"`
	Body   BotCardBody    `json:"body"`
}

// BotCardConfig uses the existing JSON key order so switching to typed fields preserves the card bytes.
type BotCardConfig struct {
	StreamingConfig *BotCardStreaming `json:"streaming_config,omitempty"`
	StreamingMode   bool              `json:"streaming_mode,omitempty"`
	Summary         *BotCardSummary   `json:"summary,omitempty"`
	UpdateMulti     bool              `json:"update_multi,omitempty"`
}

type BotCardSummary struct {
	Content string `json:"content"`
}
type BotCardStreaming struct {
	PrintFrequencyMS BotCardPrintDefault `json:"print_frequency_ms"`
	PrintStep        BotCardPrintDefault `json:"print_step"`
	PrintStrategy    string              `json:"print_strategy"`
}
type BotCardPrintDefault struct {
	Default int `json:"default"`
}

type BotCardHeader struct {
	Title       BotCardText  `json:"title"`
	Subtitle    *BotCardText `json:"subtitle,omitempty"` // who or where, under the title
	Template    string       `json:"template,omitempty"`
	TextTagList []any        `json:"text_tag_list,omitempty"` // tags on the right of the title
}

type BotCardBody struct {
	Elements []any `json:"elements"`
}

type BotCardText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}
