package briefing

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
)

func TestBriefCardsKeepEveryOwnerWaitWithinPhysicalBudget(t *testing.T) {
	f := &modapi.BriefSnapshot{Lang: string(i18n.En), Loc: time.UTC, At: time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC), Web: "https://example.com"}
	d := modapi.BriefDirection{Name: "Main", Open: 220, Link: "https://example.com/main"}
	for i := 0; i < 220; i++ {
		d.Waits = append(d.Waits, modapi.BriefWait{
			Kind: "needs_user", TodoID: fmt.Sprintf("todo-%03d", i),
			Line: fmt.Sprintf("Owner wait %03d %s", i, strings.Repeat("x", 140)),
			Link: fmt.Sprintf("https://example.com/todos/%03d", i),
		})
	}
	f.Directions = []modapi.BriefDirection{d}
	const header = "Model unavailable; these are the computed facts"
	card, plain, _ := (&runner{prompts: fakePrompts{}}).briefCardWithHeader(f, "Morning briefing", "", header)
	card.Buttons(botcard.Btn("Open", botcard.Default, map[string]any{"a": "open"}))
	cards := splitBriefAnswerCards(card, plain, header)
	if len(cards) < 2 {
		t.Fatalf("the oversized fixture produced %d card, want continuations", len(cards))
	}
	var all strings.Builder
	for i, c := range cards {
		if n := len((*botcard.Card)(c.Card).JSON()); n > botcard.MaxBytes {
			t.Errorf("card %d is %d bytes, limit %d", i, n, botcard.MaxBytes)
		}
		if n := len((*botcard.Card)(c.Card).Body.Elements); n > botcard.MaxElements {
			t.Errorf("card %d has %d elements, limit %d", i, n, botcard.MaxElements)
		}
		all.WriteString(briefCardElementText((*botcard.Card)(c.Card), ""))
		if !strings.HasPrefix(c.Fallback, header) {
			t.Errorf("card %d plain substitute does not start with the fixed header", i)
		}
	}
	if !strings.HasPrefix(plain, header+"\n") {
		t.Fatalf("the phone fallback's first line changed: %q", plain[:min(len(plain), 100)])
	}
	last := -1
	rendered := all.String()
	for i, w := range d.Waits {
		row := botcard.EscapeMarkdown(w.Line) + briefLink(i18n.Lang(f.Lang), w.Link)
		if n := strings.Count(rendered, row); n != 1 {
			t.Fatalf("owner row %d occurs %d times in the rich cards, want exactly once", i, n)
		}
		at := strings.Index(rendered, row)
		if at <= last {
			t.Fatalf("owner row %d is out of order", i)
		}
		last = at
		if n := strings.Count(plain, w.Line); n != 1 {
			t.Fatalf("owner row %d occurs %d times in the phone fallback, want exactly once", i, n)
		}
	}
}

// The answer the card quotes is cut as Markdown: a code block the cut lands in is closed and an emphasis or link the cut
// lands in is dropped, so the quoted answer does not turn the rest of itself into code or show half a marker.
func TestBriefCardQuotesItsAnswerAsMarkdown(t *testing.T) {
	f := &modapi.BriefSnapshot{Lang: string(i18n.En), Loc: time.UTC, At: time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)}
	card := func(answer string) string {
		t.Helper()
		c, _, _ := (&runner{prompts: fakePrompts{}}).briefCardWithHeader(f, "Morning brief", answer, "")
		return c.Body.Elements[0].(map[string]any)["content"].(string)
	}
	long := func(s string) string {
		t.Helper()
		if n := len([]rune(s)); n <= briefAnswerRunes {
			t.Fatalf("the fixture is %d runes, not over %d", n, briefAnswerRunes)
		}
		return s
	}
	prose := strings.Repeat("Nothing needs you this morning. ", 44) // 1408 runes, so the cut lands inside the marker

	code := card(long("**Today**\n\n```\n" + strings.Repeat("log line\n", 200) + "```\n\nAll good.\n"))
	if n := strings.Count(code, "```"); n%2 != 0 {
		t.Errorf("%d fence lines, so the block swallows the rest of the answer: %q", n, code[max(0, len(code)-160):])
	}
	if !strings.HasSuffix(code, "…") {
		t.Errorf("the cut is not marked with an ellipsis: %q", code[max(0, len(code)-160):])
	}

	bold := card(long(prose + "**" + strings.Repeat("still to come ", 15)))
	if n := strings.Count(bold, "**"); n%2 != 0 {
		t.Errorf("%d emphasis markers, so the cut left one open: %q", n, bold[max(0, len(bold)-160):])
	}
	link := card(long(prose + "[the long page](https://example.com/notes/2026/11/" + strings.Repeat("a", 80) + ")"))
	if strings.Contains(link, "example.com") {
		t.Errorf("half a link is left in the answer: %q", link[max(0, len(link)-160):])
	}

	// A chart the model drew is still drawn as numbers on the card: cutting must not leave it as the block clipMarkdown
	// would drop it to.
	chart := card(long("```persona-card\n" + `{"type":"line","title":"Sleep this week","unit":"h","points":[{"label":"Mon","value":7.2},{"label":"Tue","value":6.5}]}` + "\n```\n\n" + strings.Repeat("Nothing needs you this morning. ", 44)))
	if strings.Contains(chart, "persona-card") || !strings.Contains(chart, "Mon") || !strings.Contains(chart, "7.2") {
		t.Errorf("the chart is not drawn as numbers: %q", chart[max(0, len(chart)-200):])
	}
}
