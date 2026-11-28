package botcard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// cardMap is c as Feishu reads it.
func cardMap(t *testing.T, c *Card) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(c.JSON()), &m); err != nil {
		t.Fatalf("card JSON: %v", err)
	}
	return m
}

// The header says the matter in one line, who or where under it, and what kind of card it is on the right: a
// subtitle and up to three tags. A card without a header takes neither.
func TestCardSubtitleAndTags(t *testing.T) {
	c := NewCard("Deploy finished", Green, "").Subtitle(" persona · dev ").Tag("Agent", Neutral).Tag("Done", Green).
		Tag("third", Blue).Tag("fourth", Red)
	h := cardMap(t, c)["header"].(map[string]any)
	if got := h["subtitle"].(map[string]any)["content"]; got != "persona · dev" {
		t.Errorf("subtitle = %v", got)
	}
	tags := h["text_tag_list"].([]any)
	if len(tags) != 3 {
		t.Fatalf("tags = %d, want the first three", len(tags))
	}
	first := tags[0].(map[string]any)
	if first["tag"] != "text_tag" || first["color"] != Neutral || first["text"].(map[string]any)["content"] != "Agent" {
		t.Errorf("first tag = %v", first)
	}
	if NewCard("Title", Blue, "").Subtitle("").Header.Subtitle != nil {
		t.Error("an empty subtitle was set")
	}
	bare := NewCard("", "", "").Subtitle("x").Tag("y", Blue)
	if bare.Header != nil {
		t.Errorf("a card without a header got one: %s", bare.JSON())
	}
}

// A press that cannot be taken back asks first, in Feishu's own dialog.
func TestConfirm(t *testing.T) {
	b := Confirm(Btn("Send", Primary, map[string]any{"a": "x"}), "Send as you?", "It goes to Alice.")
	cf := b["confirm"].(map[string]any)
	if cf["title"].(plain).Content != "Send as you?" || cf["text"].(plain).Content != "It goes to Alice." {
		t.Fatalf("confirm = %v", cf)
	}
	if Confirm(nil, "a", "b") != nil {
		t.Fatal("a nil button got a confirm")
	}
}

// A text button is small and plain: a way somewhere next to the card's actions, not one of them.
func TestTextBtn(t *testing.T) {
	b := TextBtn("Workbench", map[string]any{"a": "nav", "to": "home"})
	if b["type"] != "text" || b["size"] != "small" {
		t.Fatalf("text button = %v", b)
	}
}

// The "more" menu: each entry calls back with the menu's value and its own (sent as the option and merged back when
// the press is read), or opens its link; no entries, no menu.
func TestOverflowMergesTheOption(t *testing.T) {
	menu := Overflow(map[string]any{"a": "ix", "id": "q1"},
		MenuItem{Label: "Dismiss", Value: map[string]any{"dismiss": "1"}},
		MenuItem{Label: "Open on web", URL: "https://persona.example/s/m/1"})
	if menu["tag"] != "overflow" {
		t.Fatalf("menu = %v", menu)
	}
	opts := menu["options"].([]any)
	if len(opts) != 2 {
		t.Fatalf("options = %v", opts)
	}
	link := opts[1].(map[string]any)
	if link["multi_url"].(map[string]any)["url"] != "https://persona.example/s/m/1" {
		t.Errorf("link entry = %v", link)
	}
	if Overflow(map[string]any{"a": "x"}) != nil {
		t.Error("a menu with no entries")
	}
	if Overflow(map[string]any{"a": "x"}, MenuItem{Label: "Bad", URL: "javascript:alert(1)"}) != nil {
		t.Error("an entry with a non-http link was kept")
	}

	// The press as Feishu sends it: the menu's value, and the picked entry's value as the option.
	press := func(option string) *CardAction {
		v := menu["behaviors"].([]any)[0].(map[string]any)["value"]
		raw, _ := json.Marshal(map[string]any{"action": map[string]any{"tag": "overflow", "value": v, "option": option}})
		var a CardAction
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatal(err)
		}
		return &a
	}
	a := press(opts[0].(map[string]any)["value"].(string))
	if a.Str("a") != "ix" || a.Str("id") != "q1" || a.Str("dismiss") != "1" {
		t.Fatalf("merged value = %v", a.Action.Value)
	}
	if _, left := a.Action.Value[menuKey]; left {
		t.Errorf("the menu's marker was left in %v", a.Action.Value)
	}
	if got := press(opts[1].(map[string]any)["value"].(string)); got.Str("a") != "nop" {
		t.Errorf("a link entry's press = %v, want nothing to do", got.Action.Value)
	}
	// An option that is not a menu's (a select outside a form) is left as it came.
	raw := []byte(`{"action":{"tag":"select_static","value":{"a":"pick"},"option":"x=1"}}`)
	var sel CardAction
	if err := json.Unmarshal(raw, &sel); err != nil {
		t.Fatal(err)
	}
	if sel.Str("x") != "" || sel.Action.Option != "x=1" {
		t.Errorf("a select's option was merged: %v", sel.Action.Value)
	}
}

// Buttons takes the menu as the row's last column; a row with a menu is not a row of links.
func TestButtonsWithMenu(t *testing.T) {
	c := NewCard("t", Blue, "").Buttons(Btn("Approve", Primary, map[string]any{"a": "x"}),
		Overflow(map[string]any{"a": "x"}, MenuItem{Label: "More", Value: map[string]any{"k": "v"}}))
	row := c.Body.Elements[0].(map[string]any)
	if n := len(row["columns"].([]any)); n != 2 {
		t.Fatalf("columns = %d", n)
	}
	c.AppendToLinks(LinkBtn("Open", "https://example.com"))
	if len(c.Body.Elements) != 2 {
		t.Fatalf("a link went into a row of actions: %s", c.JSON())
	}
}

// A list row is one press: the whole row calls back (or opens its link), no button per row.
func TestRow(t *testing.T) {
	c := NewCard("t", Blue, "").Row(map[string]any{"a": "switch", "s": "1"}, "**Agent** · dev").
		LinkRow("https://example.com/x", "**Open**").LinkRow("ftp://bad", "x")
	els := cardMap(t, c)["body"].(map[string]any)["elements"].([]any)
	if len(els) != 2 {
		t.Fatalf("elements = %d, want the bad link left out", len(els))
	}
	row := els[0].(map[string]any)
	if row["tag"] != "interactive_container" {
		t.Fatalf("row = %v", row)
	}
	bh := row["behaviors"].([]any)[0].(map[string]any)
	if bh["type"] != "callback" || bh["value"].(map[string]any)["a"] != "switch" {
		t.Errorf("row behavior = %v", bh)
	}
	if els[1].(map[string]any)["behaviors"].([]any)[0].(map[string]any)["type"] != "open_url" {
		t.Errorf("link row = %v", els[1])
	}
}

// One line of small facts: the empty ones left out, grey, small.
func TestMeta(t *testing.T) {
	c := NewCard("t", Blue, "").Meta("Alice", "", "dev", " ").Meta("", "")
	els := c.Body.Elements
	if len(els) != 1 {
		t.Fatalf("elements = %d", len(els))
	}
	m := els[0].(map[string]any)
	if m["text_size"] != "notation" || m["content"] != "<font color='grey'>Alice · dev</font>" {
		t.Fatalf("meta = %v", m)
	}
}

// A time shows in the reader's own zone: the clock for today, the date too before.
func TestLocalTime(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 20, 15, 0, 0, 0, loc)
	today := time.Date(2026, 11, 20, 9, 30, 0, 0, loc)
	if got := LocalTime(today, now); !strings.Contains(got, "format_type='time'") || strings.Contains(got, "date_short") ||
		!strings.Contains(got, "millisecond='1795138200000'") {
		t.Errorf("today = %s", got)
	}
	if got := LocalTime(today.AddDate(0, 0, -1), now); !strings.Contains(got, "date_short") {
		t.Errorf("yesterday = %s", got)
	}
	if LocalTime(time.Time{}, now) != "" {
		t.Error("a zero time was shown")
	}
}

// A folded panel for the details, with a border and an arrow.
func TestPanel(t *testing.T) {
	c := NewCard("t", Blue, "").Panel("Details", false, "line").Panel("Empty", true, " ")
	if len(c.Body.Elements) != 1 {
		t.Fatalf("elements = %d", len(c.Body.Elements))
	}
	p := c.Body.Elements[0].(map[string]any)
	if p["tag"] != "collapsible_panel" || p["expanded"] != false || p["border"] == nil {
		t.Fatalf("panel = %v", p)
	}
}

// What the card streams into has an element id; a streaming card says so in its config.
func TestStreamingCard(t *testing.T) {
	c := NewCard("", "", "Working").MarkdownID("live", "").Streaming()
	m := cardMap(t, c)
	cfg := m["config"].(map[string]any)
	if cfg["streaming_mode"] != true || cfg["streaming_config"] == nil {
		t.Fatalf("config = %v", cfg)
	}
	el := m["body"].(map[string]any)["elements"].([]any)[0].(map[string]any)
	if el["element_id"] != "live" || el["tag"] != "markdown" {
		t.Fatalf("element = %v", el)
	}
	c.EndStreaming()
	if c.Config.StreamingMode || c.Config.StreamingConfig != nil {
		t.Errorf("config after the end = %v", c.Config)
	}
}

// InlineTag is a coloured tag inside Markdown.
func TestInlineTag(t *testing.T) {
	if got := InlineTag("Working", Blue); got != "<text_tag color='blue'>Working</text_tag>" {
		t.Fatalf("got %s", got)
	}
	if got := InlineTag("a<b", Blue); strings.Contains(got, "a<b") {
		t.Fatalf("not escaped: %s", got)
	}
}

// A clone is changed without changing the card it was made from.
func TestClone(t *testing.T) {
	c := NewCard("t", Blue, "s").Markdown("a").Buttons(Btn("b", Primary, map[string]any{"a": "x"}))
	before := c.JSON()
	cp := c.Clone().Streaming().Tag("x", Blue).Markdown("more")
	cp.Body.Elements[0].(map[string]any)["content"] = "changed"
	if c.JSON() != before {
		t.Fatalf("the card changed: %s", c.JSON())
	}
	if !strings.Contains(cp.JSON(), "streaming_mode") || !strings.Contains(cp.JSON(), "changed") {
		t.Fatalf("clone %s", cp.JSON())
	}
}

// rowLinks is a row's links as a reader sees them: the buttons' labels and addresses, then the "more" menu's.
func rowLinks(t *testing.T, row any) (buttons, menu []string) {
	t.Helper()
	m, _ := json.Marshal(row)
	var r struct {
		Tag     string `json:"tag"`
		Columns []struct {
			Elements []struct {
				Tag       string `json:"tag"`
				Text      plain  `json:"text"`
				Behaviors []struct {
					Type string `json:"type"`
					URL  string `json:"default_url"`
				} `json:"behaviors"`
				Options []struct {
					Text     plain `json:"text"`
					MultiURL struct {
						URL string `json:"url"`
					} `json:"multi_url"`
				} `json:"options"`
			} `json:"elements"`
		} `json:"columns"`
	}
	_ = json.Unmarshal(m, &r)
	if r.Tag != "column_set" {
		t.Fatalf("not a row: %s", m)
	}
	for _, col := range r.Columns {
		for _, e := range col.Elements {
			switch e.Tag {
			case "button":
				buttons = append(buttons, e.Text.Content+"="+e.Behaviors[0].URL)
			case "overflow":
				for _, o := range e.Options {
					menu = append(menu, o.Text.Content+"="+o.MultiURL.URL)
				}
			}
		}
	}
	return buttons, menu
}

// A card's links are one row: three as buttons, past that two and a "more" menu with the rest; the same address once.
func TestLinkRow(t *testing.T) {
	c := NewCard("t", Blue, "").Markdown("x")
	for _, l := range []string{"a", "b", "c"} {
		c.Link("Open "+l, "https://"+l+".example/")
	}
	c.Link("Again", "https://a.example/").Link("Bad", "ftp://x")
	if len(c.Body.Elements) != 2 {
		t.Fatalf("elements = %d", len(c.Body.Elements))
	}
	bs, menu := rowLinks(t, c.Body.Elements[1])
	if strings.Join(bs, " ") != "Open a=https://a.example/ Open b=https://b.example/ Open c=https://c.example/" || menu != nil {
		t.Fatalf("buttons %v menu %v", bs, menu)
	}
	c.Link("Open d", "https://d.example/")
	bs, menu = rowLinks(t, c.Body.Elements[1])
	if len(bs) != 2 || strings.Join(menu, " ") != "Open c=https://c.example/ Open d=https://d.example/" {
		t.Fatalf("buttons %v menu %v", bs, menu)
	}
	// A link button added to the links joins them, as any link.
	c.AppendToLinks(LinkBtn("Open e", "https://e.example/"))
	if _, menu = rowLinks(t, c.Body.Elements[1]); len(menu) != 3 || len(c.Body.Elements) != 2 {
		t.Fatalf("menu %v, elements %d", menu, len(c.Body.Elements))
	}
	// After something else, a link starts a row of its own.
	c.Markdown("more").Link("Open f", "https://f.example/")
	if bs, _ = rowLinks(t, c.Body.Elements[3]); len(bs) != 1 {
		t.Fatalf("buttons %v", bs)
	}
}

// A page of Persona's web app opens in Feishu's sidebar on the desktop, as a button or in the "more" menu.
func TestPageLinks(t *testing.T) {
	b := PageBtn("Open the chat", "http://persona.example/persona/c1")
	beh := b["behaviors"].([]any)[0].(map[string]any)
	if beh["default_url"] != "http://persona.example/persona/c1" ||
		beh["pc_url"] != "https://applink.feishu.cn/client/web_url/open?mode=sidebar-semi&url=http%3A%2F%2Fpersona.example%2Fpersona%2Fc1" {
		t.Fatalf("page button %v", beh)
	}
	c := NewCard("t", Blue, "").Link("a", "https://a.example/").Link("b", "https://b.example/").Link("c", "https://c.example/")
	c.AppendToLinks(b)
	js := c.JSON()
	if !strings.Contains(js, `"pc_url":"https://applink.feishu.cn/client/web_url/open?mode=sidebar-semi`) {
		t.Fatalf("the page lost its sidebar in the menu: %s", js)
	}
}

// The small print that ends a card (an answer's foot, a meta line) does not split its links: a link added after it
// joins the row of links above it.
func TestLinkRowAboveSmallPrint(t *testing.T) {
	c := NewCard("t", Blue, "").Markdown("x").Link("Open a", "https://a.example/").Note("Done · 2 min").Meta("small")
	c.AppendToLinks(PageBtn("Open the chat", "https://persona.example/persona/c1"))
	if len(c.Body.Elements) != 4 {
		t.Fatalf("elements = %d: %s", len(c.Body.Elements), c.JSON())
	}
	if bs, _ := rowLinks(t, c.Body.Elements[1]); len(bs) != 2 {
		t.Fatalf("buttons %v", bs)
	}
	// Something that is not small print in between: a row of its own.
	c.Markdown("more").Link("Open b", "https://b.example/")
	if len(c.Body.Elements) != 6 {
		t.Fatalf("elements = %d", len(c.Body.Elements))
	}
}
