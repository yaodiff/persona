package botcard

import (
	"bytes"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Card builds the neutral card wire value in place.
type Card assistantwire.BotCard
type cardHeader = assistantwire.BotCardHeader
type cardBody = assistantwire.BotCardBody
type plain = assistantwire.BotCardText

func text(s string) plain { return plain{Tag: "plain_text", Content: s} }

// Header colors. A header's color is the card's state (docs/extensions/bots.md "Cards"): orange, the
// owner is to act; red, urgent or failed; blue, under way or only to know; green, done; grey, over. Tags take the same
// names, and Neutral (grey) for what only says the card's kind.
const (
	Blue    = "blue"
	Green   = "green"
	Orange  = "orange"
	Red     = "red"
	Grey    = "grey"
	Purple  = "purple"
	Wathet  = "wathet"
	Neutral = "neutral"
)

// NewCard starts a card; an empty title leaves the header out. summary is what the chat list shows for it (an
// assistant card in it as its title or fallback, never its JSON).
func NewCard(title, template, summary string) *Card {
	cfg := &assistantwire.BotCardConfig{UpdateMulti: true}
	if s := strings.TrimSpace(wire.AssistantCardText(summary)); s != "" {
		cfg.Summary = &assistantwire.BotCardSummary{Content: clipRunes(oneLine(s), 60)}
	}
	c := &Card{Schema: "2.0", Config: cfg, Body: cardBody{Elements: []any{}}}
	if title != "" {
		c.Header = &cardHeader{Title: text(title), Template: template}
	}
	return c
}

// Markdown adds a block of card Markdown (CardMarkdown for a model's answer, EscapeMarkdown for anything typed).
func (c *Card) Markdown(s string) *Card {
	if strings.TrimSpace(s) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "markdown", "content": s})
	return c
}

// Note adds a line of small grey text.
func (c *Card) Note(s string) *Card {
	if s == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "div",
		"text": map[string]any{"tag": "plain_text", "content": s, "text_size": "notation", "text_color": "grey"}})
	return c
}

// Collapsible adds a folded panel: title on its header, md inside.
func (c *Card) Collapsible(title, md string) *Card {
	if strings.TrimSpace(md) == "" {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "collapsible_panel", "expanded": false,
		"header":   map[string]any{"title": map[string]any{"tag": "markdown", "content": title}},
		"elements": []any{map[string]any{"tag": "markdown", "content": md}}})
	return c
}

func (c *Card) HR() *Card {
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "hr"})
	return c
}

// Button is one button of a card.
type Button map[string]any

// Button styles.
const (
	Primary = "primary"
	Default = "default"
	Danger  = "danger"
)

// Btn is a button that calls back with value.
func Btn(label, style string, value map[string]any) Button {
	return Button{"tag": "button", "text": text(label), "type": style, "behaviors": []any{map[string]any{"type": "callback", "value": value}}}
}

// Submit is a form's button: it calls back with value and the form's fields. name is unique in the card.
func Submit(label, style, name string, value map[string]any) Button {
	b := Btn(label, style, value)
	b["form_action_type"], b["name"] = "submit", name
	return b
}

// LinkBtn opens a page (http(s): what card links are documented to take).
func LinkBtn(label, url string) Button {
	open := map[string]any{"type": "open_url", "default_url": url, "pc_url": url, "android_url": url, "ios_url": url}
	return Button{"tag": "button", "text": text(label), "type": Default, "behaviors": []any{open}}
}

// PageBtn is LinkBtn for a page of Persona's web app: on the desktop it opens in Feishu's sidebar (SidebarURL), the
// chat staying where it was; on the phone as any link.
func PageBtn(label, u string) Button {
	b := LinkBtn(label, u)
	b["behaviors"].([]any)[0].(map[string]any)["pc_url"] = SidebarURL(u)
	return b
}

// SidebarURL is u opened in Feishu's half-width sidebar on the desktop (an applink; Feishu 3.41 and later).
func SidebarURL(u string) string {
	return "https://applink.feishu.cn/client/web_url/open?mode=sidebar-semi&url=" + url.QueryEscape(u)
}

// buttonRow lays buttons side by side, wrapping onto more lines on a narrow screen.
func buttonRow(bs []Button) map[string]any {
	cols := make([]any, len(bs))
	for i, b := range bs {
		cols[i] = map[string]any{"tag": "column", "width": "auto", "elements": []any{b}}
	}
	return map[string]any{"tag": "column_set", "flex_mode": "flow", "horizontal_spacing": "8px", "columns": cols}
}

// Buttons adds a row of buttons; empty ones (nil) are left out, and no row when none is left.
func (c *Card) Buttons(bs ...Button) *Card {
	bs = slices.DeleteFunc(slices.Clone(bs), func(b Button) bool { return len(b) == 0 })
	if len(bs) == 0 {
		return c
	}
	c.Body.Elements = append(c.Body.Elements, buttonRow(bs))
	return c
}

// AppendToLinks adds a button to the card's last row of links: a link joins them as Link does, any other button (a
// way back to the workbench) is added to the last row when that row consists only of links. A callback action row
// (approval, form or another command) stays separate from navigation. Otherwise this is Buttons.
func (c *Card) AppendToLinks(b Button) *Card {
	if len(b) == 0 {
		return c
	}
	if it, ok := linkButton(b); ok {
		return c.addLink(it)
	}
	if len(c.Body.Elements) > 0 {
		row, ok := c.Body.Elements[len(c.Body.Elements)-1].(map[string]any)
		if _, links := linkRowItems(row); ok && links {
			cols, _ := row["columns"].([]any)
			row["columns"] = append(cols, buttonRow([]Button{b})["columns"].([]any)...)
			return c
		}
	}
	return c.Buttons(b)
}

// linkRowMost is how many links a row shows as buttons; past that it shows one less and a "more" menu with the rest.
const linkRowMost = 3

// linkItem is one link of a row of links: what it says, where it goes, and where it goes on the desktop (pc).
type linkItem struct{ label, url, pc string }

// Link adds a link to the card's row of links, the last element when it is one, else a new one at the end: at most
// linkRowMost buttons, the rest in the row's "more" menu, each address once. A link a card cannot open is left out.
func (c *Card) Link(label, u string) *Card { return c.addLink(linkItem{label, u, u}) }

// PageLink is Link for a page of Persona's web app, opened in Feishu's sidebar on the desktop (PageBtn).
func (c *Card) PageLink(label, u string) *Card { return c.addLink(linkItem{label, u, SidebarURL(u)}) }

func (c *Card) addLink(it linkItem) *Card {
	if it.label = strings.TrimSpace(it.label); it.label == "" || !linkOK(it.url) {
		return c
	}
	if i := c.linkRowAt(); i >= 0 {
		items, _ := linkRowItems(c.Body.Elements[i])
		for _, x := range items {
			if x.url == it.url {
				return c
			}
		}
		c.Body.Elements[i] = linkRow(append(items, it))
		return c
	}
	c.Body.Elements = append(c.Body.Elements, linkRow([]linkItem{it}))
	return c
}

// linkRowAt is where the card's row of links is: its last element, or the last before the small print that ends the
// card (an answer's foot, a line of facts); -1 when there is none.
func (c *Card) linkRowAt() int {
	for i := len(c.Body.Elements) - 1; i >= 0; i-- {
		if _, ok := linkRowItems(c.Body.Elements[i]); ok {
			return i
		}
		if !smallPrint(c.Body.Elements[i]) {
			return -1
		}
	}
	return -1
}

// smallPrint: el is a line of small grey text (Note, Meta).
func smallPrint(el any) bool {
	m, ok := el.(map[string]any)
	if !ok {
		return false
	}
	switch m["tag"] {
	case "div":
		t, _ := m["text"].(map[string]any)
		return t["text_size"] == "notation"
	case "markdown":
		return m["text_size"] == "notation"
	}
	return false
}

// linkRow lays links out as a row: all as buttons up to linkRowMost, else one less and a "more" menu.
func linkRow(items []linkItem) map[string]any {
	shown := items
	var rest []linkItem
	if len(items) > linkRowMost {
		shown, rest = items[:linkRowMost-1], items[linkRowMost-1:]
	}
	var bs []Button
	for _, it := range shown {
		b := LinkBtn(it.label, it.url)
		if it.pc != "" {
			b["behaviors"].([]any)[0].(map[string]any)["pc_url"] = it.pc
		}
		bs = append(bs, b)
	}
	if len(rest) > 0 {
		var menu []MenuItem
		for _, it := range rest {
			menu = append(menu, MenuItem{Label: it.label, URL: it.url, PC: it.pc})
		}
		bs = append(bs, Overflow(map[string]any{"a": "nop"}, menu...))
	}
	return buttonRow(bs)
}

// linkRowItems are the links of a row of links (link buttons, then a "more" menu of links); ok is false for any
// other element.
func linkRowItems(el any) (items []linkItem, ok bool) {
	row, isMap := el.(map[string]any)
	if !isMap || row["tag"] != "column_set" || row["flex_mode"] != "flow" {
		return nil, false
	}
	cols, _ := row["columns"].([]any)
	if len(cols) == 0 {
		return nil, false
	}
	for _, rawCol := range cols {
		col, isMap := rawCol.(map[string]any)
		if !isMap {
			return nil, false
		}
		elements, _ := col["elements"].([]any)
		if len(elements) == 0 {
			return nil, false
		}
		for _, rawElement := range elements {
			var b Button
			switch x := rawElement.(type) {
			case Button:
				b = x
			case map[string]any:
				b = Button(x)
			default:
				return nil, false
			}
			if it, isLink := linkButton(b); isLink {
				items = append(items, it)
				continue
			}
			menu, isMenu := linkMenu(b)
			if !isMenu {
				return nil, false
			}
			items = append(items, menu...)
		}
	}
	return items, true
}

// linkButton is b as a link when it is a button that only opens one.
func linkButton(b Button) (linkItem, bool) {
	if b["tag"] != "button" {
		return linkItem{}, false
	}
	behaviors, _ := b["behaviors"].([]any)
	if len(behaviors) != 1 {
		return linkItem{}, false
	}
	beh, isMap := behaviors[0].(map[string]any)
	if !isMap || beh["type"] != "open_url" {
		return linkItem{}, false
	}
	var it linkItem
	it.url, _ = beh["default_url"].(string)
	it.pc, _ = beh["pc_url"].(string)
	it.label = plainContent(b["text"])
	return it, it.url != ""
}

// plainContent is a plain_text's words, as built or as read back from JSON.
func plainContent(v any) string {
	switch t := v.(type) {
	case plain:
		return t.Content
	case map[string]any:
		s, _ := t["content"].(string)
		return s
	}
	return ""
}

// linkMenu is the links of b when it is a "more" menu of links only.
func linkMenu(b Button) ([]linkItem, bool) {
	if b["tag"] != "overflow" {
		return nil, false
	}
	opts, _ := b["options"].([]any)
	var items []linkItem
	for _, raw := range opts {
		o, isMap := raw.(map[string]any)
		if !isMap {
			return nil, false
		}
		mu, _ := o["multi_url"].(map[string]any)
		u, _ := mu["url"].(string)
		if u == "" {
			return nil, false
		}
		pc, _ := mu["pc_url"].(string)
		items = append(items, linkItem{plainContent(o["text"]), u, pc})
	}
	return items, len(items) > 0
}

// Form adds a form: its fields (Input, Select, MarkdownBlock) and the submit buttons under them.
func (c *Card) Form(name string, fields []any, submit ...Button) *Card {
	els := append([]any{}, fields...)
	if len(submit) > 0 {
		els = append(els, buttonRow(submit))
	}
	c.Body.Elements = append(c.Body.Elements, map[string]any{"tag": "form", "name": name, "elements": els})
	return c
}

// Input is a text field of a form; multiline for longer answers.
func Input(name, label, placeholder string, multiline bool) map[string]any {
	m := map[string]any{"tag": "input", "name": name, "placeholder": text(placeholder), "width": "fill", "max_length": 1000}
	if label != "" {
		m["label"], m["label_position"] = text(label), "top"
	}
	if multiline {
		m["input_type"], m["rows"] = "multiline_text", 2
	}
	return m
}

// Option is one choice of a Select: what it shows and the value it submits.
type Option struct{ Label, Value string }

// Select is a picker of a form; multi lets several be picked (their values come back as a list).
func Select(name, placeholder string, options []Option, multi bool) map[string]any {
	tag := "select_static"
	if multi {
		tag = "multi_select_static"
	}
	opts := make([]any, len(options))
	for i, o := range options {
		opts[i] = map[string]any{"text": text(o.Label), "value": o.Value}
	}
	return map[string]any{"tag": tag, "name": name, "placeholder": text(placeholder), "options": opts, "width": "fill"}
}

// MarkdownBlock is a Markdown block inside a form.
func MarkdownBlock(s string) map[string]any { return map[string]any{"tag": "markdown", "content": s} }

// JSON is the card as message content. HTML characters stay as they are: a card's limit is in bytes.
func (c *Card) JSON() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(c)
	return strings.TrimSuffix(b.String(), "\n")
}

// Raw is the card as a callback's new card (CardResponse.Card).
func (c *Card) Raw() *CardPayload { return &CardPayload{Type: "raw", Data: c} }

// --- Markdown ---

var (
	fenceRe  = lazyre.New("(?s)```.*?(?:```|$)|~~~.*?(?:~~~|$)")
	inlineRe = lazyre.New("`[^`\n]+`")
	// A list item's line: its indentation, its marker ("- ", "* ", "+ ", "1. ", "1) ") and what follows it.
	listItemRe = lazyre.New(`^([ \t]*)(?:[-*+]|\d{1,9}[.)])[ \t]+\S`)
)

// CardMarkdown turns GitHub Markdown (a model's answer) into what a card shows as written: outside code, "<" is
// escaped, since cards read <at>, <font>, <link>… as their own tags (and drop what they do not know); an autolink
// (<https://…>, <mailto:…>) becomes a card link first; and a list is nested the way cards nest it (listIndent). The
// assistant's persona-card charts are written as numbers first (PersonaCards).
func CardMarkdown(s string) string {
	s = listIndent(PersonaCards(s))
	var b strings.Builder
	last := 0
	for _, loc := range fenceRe.FindAllStringIndex(s, -1) {
		b.WriteString(cardProse(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(cardProse(s[last:]))
	return b.String()
}

// listIndent rewrites a list's indentation for cards: one nesting level is four leading spaces there (Lark's Markdown
// component says a list is nested "4 spaces per level"), while CommonMark — and so a model writing an answer — indents
// a sub-item by its parent marker's width: two spaces under "- ", three under "1. ". Passed through unchanged those
// sub-items are siblings of their parent to Lark, so the answer's hierarchy is lost. Each item's observed indentation
// is mapped to a level (an item deeper than the one above it, by any amount, is one level deeper) and written out as
// four spaces per level; a paragraph at the left margin ends the list. Fenced code (the levels carry across it, so a
// list continues around a code block), text indented four or more spaces at the left margin (an indented code block)
// and a list item's indented continuation lines are left as they are; a tab counts as four columns. A list inside a
// blockquote is left as written.
func listIndent(s string) string {
	lines := strings.Split(s, "\n")
	var levels []int // the indent width of each open nesting level, outermost first
	fence := ""
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if fence != "" { // inside code: as written
			if strings.HasPrefix(t, fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		}
		if f := fenceMarker(t); f != "" {
			fence = f
			continue
		}
		m := listItemRe.FindStringSubmatchIndex(line)
		if m == nil {
			if t != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				levels = levels[:0] // a paragraph at the left margin ends the list
			}
			continue
		}
		indent, rest := line[:m[3]], line[m[3]:]
		w := 0
		for _, c := range indent {
			if c == '\t' {
				w += 4
			} else {
				w++
			}
		}
		if len(levels) == 0 && w >= 4 {
			continue // four or more spaces at the left margin is an indented code block, not a list item
		}
		for len(levels) > 0 && w < levels[len(levels)-1] {
			levels = levels[:len(levels)-1]
		}
		if len(levels) == 0 || w > levels[len(levels)-1] {
			levels = append(levels, w)
		}
		lines[i] = strings.Repeat("    ", len(levels)-1) + rest
	}
	return strings.Join(lines, "\n")
}

func cardProse(s string) string {
	var b strings.Builder
	last := 0
	for _, loc := range inlineRe.FindAllStringIndex(s, -1) {
		b.WriteString(proseText(s[last:loc[0]]))
		b.WriteString(s[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(proseText(s[last:]))
	return b.String()
}

var (
	// A CommonMark autolink a card can open: http(s) or mailto, the scheme in any case.
	autolinkRe = lazyre.New(`^<((?i:https?://|mailto:)[^\s<>]+)>`)
	// Percent-encodes what would end a card link's address early. "[" and "]" stay in an http(s) host (IPv6).
	hostEncoder = strings.NewReplacer("(", "%28", ")", "%29")
	pathEncoder = strings.NewReplacer("(", "%28", ")", "%29", "[", "%5B", "]", "%5D")
	// What in a link's text would end it or be read as italics / bold, as entities (the ones EscapeMarkdown uses).
	labelEscaper = strings.NewReplacer("[", "&#91;", "]", "&#93;", "_", "&#95;", "*", "&#42;")
)

// proseText is prose outside code. Cards have no autolink form and read "<" as the start of their own tags, so an
// autolink becomes [address](address) and every other "<" is escaped. A backslash-escaped "<" is not an autolink.
func proseText(s string) string {
	var b strings.Builder
	for {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		s = s[i:]
		m := autolinkRe.FindStringSubmatch(s)
		if m == nil || escapedAt(b.String()) {
			b.WriteString("&#60;")
			s = s[1:]
			continue
		}
		if strings.HasSuffix(b.String(), "](") { // [text](<address>): only the address goes in
			b.WriteString(linkAddress(m[1]))
		} else {
			b.WriteString("[" + labelEscaper.Replace(m[1]) + "](" + linkAddress(m[1]) + ")")
		}
		s = s[len(m[0]):]
	}
}

// escapedAt reports whether before ends in an odd number of backslashes, which escape the character after it.
func escapedAt(before string) bool {
	n := len(before) - len(strings.TrimRight(before, `\`))
	return n%2 == 1
}

// linkAddress is u as a card link's address, the same address with what would end it early percent-encoded.
func linkAddress(u string) string {
	i := strings.Index(u, "://")
	if i < 0 { // mailto: has no host
		return pathEncoder.Replace(u)
	}
	host := len(u)
	if j := strings.IndexAny(u[i+3:], "/?#"); j >= 0 {
		host = i + 3 + j
	}
	return hostEncoder.Replace(u[:host]) + pathEncoder.Replace(u[host:])
}

var mdEscaper = strings.NewReplacer("\\", "&#92;", "*", "&#42;", "_", "&#95;", "`", "&#96;", "[", "&#91;", "]", "&#93;",
	"~", "&#126;", "<", "&#60;", ">", "&#62;", "#", "&#35;", "|", "&#124;")

// EscapeMarkdown makes text show as it is in a card's Markdown (names, titles, what a user typed).
func EscapeMarkdown(s string) string { return mdEscaper.Replace(s) }

// Code puts text in an inline code span (backticks in it become quotes: a span cannot hold them).
func Code(s string) string {
	s = strings.ReplaceAll(oneLine(s), "`", "'")
	if s == "" {
		return ""
	}
	return "`" + s + "`"
}

// Split cuts long Markdown into pieces of at most n runes (channel.Split).
func Split(s string, n int) []string { return channel.Split(s, n) }

// PostMarkdown is a rich-text message whose one paragraph is Markdown: what is sent when a card is refused.
func PostMarkdown(md string) string {
	b, _ := json.Marshal(map[string]any{"zh_cn": map[string]any{"content": [][]map[string]string{{{"tag": "md", "text": md}}}}})
	return string(b)
}

// TextContent is a plain text message.
func TextContent(s string) string {
	b, _ := json.Marshal(map[string]string{"text": s})
	return string(b)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// MarkdownLinkOK reports whether u is an http(s) address with a host that a Markdown link can hold (card links are documented for
// http(s) only, as safeLinkButton checks).
func MarkdownLinkOK(u string) bool {
	p, err := url.Parse(u)
	return err == nil && u != "" && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && !strings.ContainsAny(u, "() \t\r\n")
}
