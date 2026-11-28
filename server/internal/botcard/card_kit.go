package botcard

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The parts every card is drawn with (docs/extensions/bots.md "Cards"): the header's color is the state, its
// title the matter in one line, its subtitle who or where and its tags the card's kind; the body opens with the point,
// then one line of small facts (Meta), the details folded (Panel); one row of actions — one primary, at most two more,
// the rest in a "more" menu (Overflow); a press that cannot be taken back asks first (Confirm); a list is rows that are
// pressed whole (Row). Card JSON 2.0 refuses a card for one field it does not know, so only documented ones are used.

// headerTagsMost is how many tags Feishu shows on a header.
const headerTagsMost = 3

// Subtitle puts a line under the title: who or where, what the title leaves out. An empty one, or a card without a
// header, changes nothing.
func (c *Card) Subtitle(s string) *Card {
	if s = strings.TrimSpace(s); s != "" && c.Header != nil {
		p := text(clipRunes(oneLine(s), 60))
		c.Header.Subtitle = &p
	}
	return c
}

// Tag adds a tag on the right of the title (Feishu shows the first three; a card without a header has none).
func (c *Card) Tag(label, color string) *Card {
	if label = strings.TrimSpace(label); label == "" || c.Header == nil || len(c.Header.TextTagList) >= headerTagsMost {
		return c
	}
	c.Header.TextTagList = append(c.Header.TextTagList, map[string]any{"tag": "text_tag", "text": text(clipRunes(label, 12)), "color": color})
	return c
}

// InlineTag is a colored tag inside Markdown text (a state at the start of a line).
func InlineTag(label, color string) string {
	return "<text_tag color='" + color + "'>" + EscapeMarkdown(label) + "</text_tag>"
}

// Confirm makes a press ask first, in Feishu's own dialog: for what cannot be taken back (a message sent as the owner,
// a permission that stays). A nil button stays nil.
func Confirm(b Button, title, body string) Button {
	if len(b) == 0 {
		return b
	}
	b["confirm"] = map[string]any{"title": text(title), "text": text(body)}
	return b
}

// TextBtn is a small button of plain text: a way somewhere beside the card's actions, not one of them.
func TextBtn(label string, value map[string]any) Button {
	b := Btn(label, "text", value)
	b["size"] = "small"
	return b
}

// MenuItem is one entry of a card's "more" menu: picked, it calls back with the menu's value and its own Value, or it
// opens URL (http(s) only).
type MenuItem struct {
	Label string
	Value map[string]any
	URL   string
	PC    string // where URL goes on the desktop, when not URL itself (SidebarURL)
}

const (
	// menuKey marks the value of a "more" menu, whose picked entry comes back as action.option and is merged into it
	// when the press is read (CardAction.UnmarshalJSON).
	menuKey = "_menu"
	// menuOpen is the option of an entry that opens a link: there is nothing to do when its press calls back.
	menuOpen = "_open"
)

// Overflow is a "more" menu, the last column of a row of buttons: base is what every entry calls back with, and an
// entry adds its own Value (sent as the option, Feishu's overflow carrying one string). Nil when no entry is left
// (Buttons leaves it out).
func Overflow(base map[string]any, items ...MenuItem) Button {
	var opts []any
	for _, it := range items {
		if strings.TrimSpace(it.Label) == "" {
			continue
		}
		o := map[string]any{"text": text(clipRunes(it.Label, 20))}
		switch {
		case it.URL != "":
			if !linkOK(it.URL) {
				continue
			}
			pc := it.PC
			if pc == "" {
				pc = it.URL
			}
			o["value"] = menuOpen
			o["multi_url"] = map[string]any{"url": it.URL, "pc_url": pc, "android_url": it.URL, "ios_url": it.URL}
		default:
			q := url.Values{}
			for k, v := range it.Value {
				q.Set(k, anyString(v))
			}
			o["value"] = q.Encode()
		}
		opts = append(opts, o)
	}
	if len(opts) == 0 {
		return nil
	}
	value := map[string]any{menuKey: "1"}
	for k, v := range base {
		value[k] = v
	}
	return Button{"tag": "overflow", "width": "default", "options": opts, "behaviors": []any{map[string]any{"type": "callback", "value": value}}}
}

// UnmarshalJSON reads a press, a "more" menu's picked entry merged into its value: the hub and the assistant read the
// same event, so both see the press as if it were a button of its own. A link entry's press (its link opened) is
// {"a": "nop"}, which nothing acts on.
func (a *CardAction) UnmarshalJSON(b []byte) error {
	type plainAction CardAction
	var p plainAction
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*a = CardAction(p)
	if a.Action.Value[menuKey] == nil || a.Action.Option == "" {
		return nil
	}
	if a.Action.Option == menuOpen {
		a.Action.Value = map[string]any{"a": "nop"}
		return nil
	}
	q, err := url.ParseQuery(a.Action.Option)
	if err != nil {
		return nil
	}
	v := make(map[string]any, len(a.Action.Value)+len(q))
	for k, x := range a.Action.Value {
		if k != menuKey {
			v[k] = x
		}
	}
	for k := range q {
		v[k] = q.Get(k)
	}
	a.Action.Value = v
	return nil
}

// linkOK: u is an http(s) address with a host (what card links are documented to take).
func linkOK(u string) bool {
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != ""
}

// rowContainer is a list row pressed whole: an interactive container around md.
func rowContainer(md string, behavior map[string]any) map[string]any {
	return map[string]any{"tag": "interactive_container", "width": "fill", "height": "auto", "background_style": "default",
		"has_border": true, "border_color": "grey", "corner_radius": "8px", "padding": "8px 12px 8px 12px",
		"behaviors": []any{behavior}, "elements": []any{map[string]any{"tag": "markdown", "content": md}}}
}

// Row adds a list row that calls back with value when pressed anywhere on it.
func (c *Card) Row(value map[string]any, md string) *Card {
	if strings.TrimSpace(md) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, rowContainer(md, map[string]any{"type": "callback", "value": value}))
	return c
}

// LinkRow adds a list row that opens u when pressed; a link a card cannot take leaves it out.
func (c *Card) LinkRow(u, md string) *Card {
	if strings.TrimSpace(md) == "" || !linkOK(u) {
		return c
	}
	open := map[string]any{"type": "open_url", "default_url": u, "pc_url": u, "android_url": u, "ios_url": u}
	c.Body.Elements = append(c.Body.Elements, rowContainer(md, open))
	return c
}

// Meta adds one line of small grey facts (who · where · when), the empty ones left out; parts are card Markdown.
func (c *Card) Meta(parts ...string) *Card {
	parts = slices.DeleteFunc(slices.Clone(parts), func(s string) bool { return strings.TrimSpace(s) == "" })
	if len(parts) == 0 {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "markdown", "text_size": "notation",
		"content": "<font color='grey'>" + strings.Join(parts, " · ") + "</font>"})
	return c
}

// LocalTime is t in the reader's own zone and language (card Markdown): the clock when it is the same day as now in
// now's zone, the date before it too. "" for a zero time.
func LocalTime(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	ms := t.UnixMilli()
	clock := fmt.Sprintf("<local_datetime millisecond='%d' format_type='time'></local_datetime>", ms)
	day := t.In(now.Location())
	if day.Year() == now.Year() && day.YearDay() == now.YearDay() {
		return clock
	}
	return fmt.Sprintf("<local_datetime millisecond='%d' format_type='date_short'></local_datetime> ", ms) + clock
}

// Panel adds a folded panel with a border, title on its header (card Markdown) and md inside, open when expanded.
// A form cannot go inside one.
func (c *Card) Panel(title string, expanded bool, md string) *Card {
	if strings.TrimSpace(md) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "collapsible_panel", "expanded": expanded,
		"header": map[string]any{"title": map[string]any{"tag": "markdown", "content": title}, "vertical_align": "center",
			"icon":          map[string]any{"tag": "standard_icon", "token": "down-small-ccm_outlined", "size": "16px 16px"},
			"icon_position": "right", "icon_expanded_angle": -180},
		"border": map[string]any{"color": "grey", "corner_radius": "6px"}, "vertical_spacing": "8px",
		"padding": "8px 12px 8px 12px", "elements": []any{map[string]any{"tag": "markdown", "content": md}}})
	return c
}

// MarkdownID adds a block of card Markdown with an element id (what a streaming card writes into), empty or not.
func (c *Card) MarkdownID(id, s string) *Card {
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "markdown", "content": s, "element_id": id})
	return c
}

// Streaming makes the card one that CardKit streams text into (feishubot's CardStream): its text comes in letter by
// letter, and the chat list shows its summary meanwhile.
func (c *Card) Streaming() *Card {
	c.Config.StreamingMode = true
	c.Config.StreamingConfig = &assistantwire.BotCardStreaming{
		PrintFrequencyMS: assistantwire.BotCardPrintDefault{Default: 50},
		PrintStep:        assistantwire.BotCardPrintDefault{Default: 2},
		PrintStrategy:    "fast",
	}
	return c
}

// Clone is a copy of c to change without changing c (a card sent two ways: as an entity, else as a message).
func (c *Card) Clone() *Card {
	var cp Card
	_ = json.Unmarshal([]byte(c.JSON()), &cp)
	if cp.Config == nil {
		cp.Config = &assistantwire.BotCardConfig{}
	}
	if cp.Body.Elements == nil {
		cp.Body.Elements = []any{}
	}
	return &cp
}

// EndStreaming takes the streaming settings off again (a card written whole).
func (c *Card) EndStreaming() *Card {
	if c.Config != nil {
		c.Config.StreamingMode = false
		c.Config.StreamingConfig = nil
	}
	return c
}
