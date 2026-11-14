package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/ai"
)

// Behaviour check for the language of what is written from a recording (docs/assistant/prompts.md "Behaviour checks"):
// TestMinutesLanguageLive runs one synthetic meeting that mixes Chinese, English and Japanese through the real minutes,
// summary and day recap paths, once as recorded in each of zh-CN, en-US and ja-JP, and prints how much of the written
// text is in which script. It asks a real model and runs only when PERSONA_LIVE_AI is set, never in CI:
//
//	PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}' go test -run MinutesLanguageLive -v -count=1 ./internal/kb
//
// Placeholders only: the people are Ada, Bo and Cy, the work an export, a release and a cache bug.

// liveMeeting is a product sync said in three languages, each line a few seconds of the recording.
var liveMeeting = []string{
	"好，我们开始吧，今天在三楼会议室，主要看三件事：导出、发布时间、还有缓存的问题。",
	"First, the export. Ada, can you give us the status?",
	"Sure. The export drops any entry over 4 MB, and users only notice when the file is incomplete.",
	"那我们这一版先保留 4 MB 的上限，但是要在界面上提示用户。",
	"Agreed. Let's keep the cap for this release and show a warning when an entry is skipped.",
	"Bo，你周五之前把提示的文案和回归测试补上，可以吗？",
	"はい、金曜日までに文言と回帰テストを追加します。",
	"Next, the release date. Cy, are we still on track for the 20th?",
	"20 日は厳しいかもしれません。キャッシュのバグがまだ再現できていません。",
	"缓存的问题只有在弱网的时候才出现，我这边昨天复现了一次。",
	"Can you share the steps? If we can reproduce it by Wednesday, we keep the 20th.",
	"好的，我今天下班前把复现步骤发到群里。",
	"水曜日までに再現できなければ、リリースを一週間延ばしましょう。",
	"那就这么定：周三之前复现不了，就推迟一周。",
	"One open question: should the cap be per entry or per recording?",
	"それはまだ決まっていません。来週のミーティングで決めましょう。",
	"OK, that's all for today. Thanks everyone.",
}

// liveWhiteboard is the words of the whiteboard photographed in liveMeeting just after the cache bug (95 s in).
const liveWhiteboard = "Cache bug: reproduced once, weak network only. Not reproduced by Wed: release +1 week."

// liveAnswers is a model that keeps every minutes answer it gives and counts the merging calls.
type liveAnswers struct {
	Model
	mu      sync.Mutex
	answers []minutesAnswer
	merges  int
}

func (m *liveAnswers) JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error {
	err := m.Model.JSONMax(ctx, system, user, maxTokens, out)
	if a, ok := out.(*minutesAnswer); ok && err == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.answers = append(m.answers, *a)
		if strings.Contains(system, "已经分段整理过") { // i18n-ignore: the merging prompt's words
			m.merges++
		}
	}
	return err
}

// last is the answer the minutes were made from: the only call's, or the merging call's.
func (m *liveAnswers) last() minutesAnswer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.answers) == 0 {
		return minutesAnswer{}
	}
	return m.answers[len(m.answers)-1]
}

// strayPhotoConclusions are the conclusions of a that cite a photo no point or decision of its topics cites, which the
// prompts ask not to write (minutes_card.md): the hub keeps them only by their line.
func strayPhotoConclusions(a minutesAnswer) []string {
	cited := map[modelPhoto]bool{}
	for _, tp := range a.Topics {
		for _, p := range tp.Points {
			cited[p.Photo] = cited[p.Photo] || p.Photo > 0
		}
		for _, d := range tp.Decisions {
			cited[d.Photo] = cited[d.Photo] || d.Photo > 0
		}
	}
	var out []string
	for _, c := range a.Conclusions {
		if c.Photo > 0 && !cited[c.Photo] {
			out = append(out, string(c.Text))
		}
	}
	return out
}

// cardMisses says what the summary card o of liveMeeting's minutes lacks ("" nothing): a headline, 2 to 4 core
// conclusions, and the key figures in bold: most bullets have a bold place, and one naming the 4 MB cap bolds it.
func cardMisses(o *MinutesOverview) string {
	var miss []string
	if strings.TrimSpace(o.Headline) == "" {
		miss = append(miss, "no headline")
	}
	if n := len(o.Conclusions); n < 2 || n > minutesConclusions {
		miss = append(miss, fmt.Sprintf("%d conclusions", n))
	}
	bullets, bold, capped, cappedBold := 0, 0, 0, 0
	for _, g := range o.Groups {
		for _, tp := range g.Topics {
			for _, b := range tp.Bullets {
				bullets++
				if len(b.Strong) > 0 {
					bold++
				}
				if strings.Contains(strings.ReplaceAll(b.Text, " ", ""), "4MB") {
					capped++
					if slices.ContainsFunc(b.Strong, func(s string) bool { return strings.Contains(s, "4") }) {
						cappedBold++
					}
				}
			}
		}
	}
	if 2*bold < bullets {
		miss = append(miss, fmt.Sprintf("%d of %d bullets bold", bold, bullets))
	}
	if capped == 0 || cappedBold == 0 {
		miss = append(miss, fmt.Sprintf("the 4 MB cap in %d bullets, bold in %d", capped, cappedBold))
	}
	return strings.Join(miss, ", ")
}

// liveNames are the people of liveMeeting, left out of the counts: a name stays as it was said.
var liveNames = regexp.MustCompile(`\b(Ada|Bo|Cy)\b`)

// scripts is the share of Han, Kana and Latin letters among the letters of text, names left out.
type scripts struct{ han, kana, latin, letters int }

func countScripts(text string) scripts {
	var s scripts
	for _, r := range liveNames.ReplaceAllString(text, "") {
		switch {
		case unicode.Is(unicode.Han, r):
			s.han++
		case unicode.In(r, unicode.Hiragana, unicode.Katakana) || r == 'ー':
			s.kana++
		case unicode.Is(unicode.Latin, r):
			s.latin++
		default:
			continue
		}
		s.letters++
	}
	return s
}

func (s scripts) share(n int) float64 {
	if s.letters == 0 {
		return 0
	}
	return float64(n) / float64(s.letters)
}

func (s scripts) String() string {
	return fmt.Sprintf("Han %3.0f%%  Kana %3.0f%%  Latin %3.0f%%", 100*s.share(s.han), 100*s.share(s.kana), 100*s.share(s.latin))
}

// wrongLanguage says why text counted as s is not in lang ("" when it is).
func (s scripts) wrongLanguage(lang string) string {
	switch {
	case s.letters == 0:
		return "nothing written"
	case lang == "en" && s.share(s.han+s.kana) > 0.05:
		return "English with more than 5% Han and Kana"
	case lang == "zh-Hans" && s.share(s.han) < 0.5:
		return "Chinese with less than 50% Han"
	case lang == "zh-Hans" && s.share(s.kana) > 0.05:
		return "Chinese with more than 5% Kana"
	case lang == "ja" && s.share(s.kana) < 0.05:
		return "Japanese with less than 5% Kana"
	}
	return ""
}

// minutesText is the written text of m: its title, every item and the summary card's headline, conclusions and
// titles (names apart); the counts are points/decisions/actions/open, then the card's groups/topics/conclusions.
func minutesText(m *Minutes) (string, string) {
	parts := []string{m.Title}
	card := "no card"
	if o := m.Overview; o != nil {
		topics := 0
		parts = append(parts, o.Headline)
		for _, c := range o.Conclusions {
			parts = append(parts, c.Text)
		}
		for _, g := range o.Groups {
			parts = append(parts, g.Title)
			for _, tp := range g.Topics {
				parts = append(parts, tp.Title)
				topics++
			}
		}
		card = fmt.Sprintf("%dg/%dt/%dc", len(o.Groups), topics, len(o.Conclusions))
	}
	for _, p := range m.Points {
		parts = append(parts, p.Text)
	}
	for _, d := range m.Decisions {
		parts = append(parts, d.Text)
	}
	for _, a := range m.Actions {
		parts = append(parts, a.What, a.By)
	}
	for _, o := range m.Open {
		parts = append(parts, o.Text)
	}
	counts := fmt.Sprintf("%d/%d/%d/%d %s", len(m.Points), len(m.Decisions), len(m.Actions), len(m.Open), card)
	return strings.Join(parts, "\n"), counts
}

// recapText is the written text of rc: its summary, the entries' titles and points, and the actions.
func recapText(rc *DayRecap) (string, string) {
	parts := []string{rc.Summary}
	points := 0
	for _, en := range rc.Timeline {
		parts = append(parts, en.Title)
		for _, p := range en.Points {
			parts = append(parts, p.Text)
			points++
		}
	}
	for _, a := range rc.Actions {
		parts = append(parts, a.What, a.By)
	}
	return strings.Join(parts, "\n"), fmt.Sprintf("%d entries/%d points/%d actions", len(rc.Timeline), points, len(rc.Actions))
}

// TestMinutesLanguageLive asks a real model (PERSONA_LIVE_AI) for the minutes, summary and day recap of liveMeeting
// recorded in each language, prints a row each, and fails when one is not written in the recording's language or its
// Markdown's headings are not. The minutes are written twice: in one call, and as a long meeting would be (the
// transcript in parts of a few lines, then the merging call). Both must have their summary card (#6563) with 2 to 10
// topics, a headline, 2 to 4 core conclusions and the key figures in bold (cardMisses), and no conclusion may cite the
// whiteboard photo unless a topic's point or decision does; the cards (groups, topics, conclusions and bullets with
// their bold places and photos) are printed after the table, or why one was left out.
func TestMinutesLanguageLive(t *testing.T) {
	raw := os.Getenv("PERSONA_LIVE_AI")
	if raw == "" {
		t.Skip("PERSONA_LIVE_AI not set")
	}
	var cfg ai.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	model := ai.New(cfg)
	ctx := context.Background()
	var rows, cards []string
	row := func(tag, kind, title, counts string, s scripts, miss string) {
		mark := "ok  "
		if miss != "" {
			mark = "MISS"
			t.Errorf("%s %s: %s (%s)", tag, kind, miss, s)
		}
		rows = append(rows, fmt.Sprintf("%s %-6s %-8s %-30.30q %-28s %s", mark, tag, kind, title, counts, s))
	}
	// card prints m's summary card, or why there is none (the log line buildMinutes writes).
	card := func(tag string, m *Minutes, why []string) {
		o := m.Overview
		if o == nil {
			cards = append(cards, fmt.Sprintf("%s: left out: %s", tag, strings.Join(why, "; ")))
			return
		}
		photo := func(n int) string {
			if n > 0 {
				return fmt.Sprintf(" (photo %d)", n)
			}
			return ""
		}
		cards = append(cards, fmt.Sprintf("%s: %q", tag, o.Headline))
		for _, c := range o.Conclusions {
			cards = append(cards, fmt.Sprintf("  conclusion %q %q%s", c.Text, c.Strong, photo(c.Photo)))
		}
		for _, g := range o.Groups {
			cards = append(cards, fmt.Sprintf("  group %q", g.Title))
			for _, tp := range g.Topics {
				cards = append(cards, fmt.Sprintf("    topic %q (%s)", tp.Title, tp.Icon))
				for _, b := range tp.Bullets {
					kind := "point"
					if b.Decision {
						kind = "decision"
					}
					cards = append(cards, fmt.Sprintf("      %s %q %q%s", kind, b.Text, b.Strong, photo(b.Photo)))
				}
			}
		}
	}
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	// minutes writes the minutes of liveMeeting and its whiteboard photo recorded in tag on a knowledge base of its
	// own, the transcript in parts of at most part runes when part is not 0: the engine, the minutes, the model's
	// answers and why the summary card was left out.
	minutes := func(tag string, part int) (*Engine, *Minutes, *liveAnswers, []string) {
		answers := &liveAnswers{Model: model}
		e := goldenEngine(t, answers)
		e.minutesCall, e.minutesPart = part, part
		var why []string
		e.Logf = func(f string, a ...any) {
			line := fmt.Sprintf(f, a...)
			if _, reason, ok := strings.Cut(line, "minutes: no overview: "); ok {
				why = append(why, reason)
			}
			t.Log(line)
		}
		u := meetingUpload(1, start, liveMeeting...)
		u.Language = tag
		putRecording(t, e, recID, u, start.Add(time.Hour))
		addPhoto(t, e, recID, photoUpload(1, 95, "Whiteboard", liveWhiteboard), false)
		if n := e.WriteMinutes(ctx, 1); n != 1 {
			t.Fatalf("%s: wrote %d minutes", tag, n)
		}
		m, err := e.Store.ReadMinutes(recID)
		if err != nil {
			t.Fatalf("%s: %v", tag, err)
		}
		return e, m, answers, why
	}
	for _, tc := range []struct{ tag, lang, heading, recapHeading string }{
		{"zh-CN", "zh-Hans", "## 待办", "全天回顾"}, // a heading of either layout (#6563)
		{"en-US", "en", "## Action items", "# Day recap · "},
		{"ja-JP", "ja", "## アクション項目", "の1日のまとめ"},
	} {
		// Minutes: in one call, then in parts of a few lines and the merging call, as a long meeting's.
		var e *Engine
		for _, way := range []struct {
			kind string
			part int
		}{{"minutes", 0}, {"merged", 400}} {
			ee, m, answers, why := minutes(tc.tag, way.part)
			if way.part == 0 {
				e = ee
			}
			text, counts := minutesText(m)
			s := countScripts(text)
			miss := s.wrongLanguage(tc.lang)
			if m.Language != tc.lang || !strings.Contains(m.Markdown, tc.heading) {
				miss = strings.TrimSpace(miss + fmt.Sprintf(" language %q, headings %q", m.Language, m.Markdown[:min(len(m.Markdown), 200)]))
			}
			// The place is kept only as it was said (buildMinutes checks it against the transcript): written in
			// another language it would be dropped. Merged minutes are checked against the lines the notes cite,
			// which need not hold the place.
			if way.part == 0 && m.Where == "" {
				miss = strings.TrimSpace(miss + " no place (where): translated, so it did not match what was said")
			}
			if way.part > 0 && answers.merges != 1 {
				miss = strings.TrimSpace(miss + fmt.Sprintf(" %d merging calls", answers.merges))
			}
			if o := m.Overview; o == nil {
				miss = strings.TrimSpace(miss + " no summary card: " + strings.Join(why, "; "))
			} else if n := topicCount(o); n < 2 || n > 10 {
				miss = strings.TrimSpace(miss + fmt.Sprintf(" %d topics", n))
			} else if c := cardMisses(o); c != "" {
				miss = strings.TrimSpace(miss + " " + c)
			}
			if stray := strayPhotoConclusions(answers.last()); len(stray) > 0 {
				miss = strings.TrimSpace(miss + fmt.Sprintf(" conclusions cite a photo no bullet cites: %q", stray))
			}
			row(tc.tag, way.kind, m.Title, counts, s, miss)
			card(tc.tag+" "+way.kind, m, why)
		}

		// The recording's summary (a meeting gets one besides its minutes).
		if n := e.SummarizeRecordings(ctx, 1); n != 1 {
			t.Fatalf("%s: summarized %d", tc.tag, n)
		}
		r := recordingNow(t, e, recID)
		sum, _ := os.ReadFile(e.Store.Path(KindRecording, recID, "summary.md"))
		// The section headings count too: they are asked for in the recording's language.
		s := countScripts(strings.Join([]string{r.AITitle, r.OneLine, string(sum)}, "\n"))
		row(tc.tag, "summary", r.AITitle, fmt.Sprintf("%d runes", len([]rune(string(sum)))), s, s.wrongLanguage(tc.lang))

		// A day recorded with the same words, a minute a line: an hour's notes, then the recap.
		now := dayClock.Add(2 * time.Hour)
		e.clock = func() time.Time { return now }
		du := dayUpload(1, liveMeeting...)
		du.Language = tc.tag
		du.Coverage.To = 7200
		mustPut(t, e, dayID, du, now)
		if err := e.AskMinutes(dayID); err != nil {
			t.Fatal(err)
		}
		e.WriteDayRecaps(ctx, 1)
		rc, err := e.ReadDayRecap(dayID)
		if err != nil {
			t.Fatalf("%s: recap: %v", tc.tag, err)
		}
		text, counts := recapText(rc)
		s = countScripts(text)
		miss := s.wrongLanguage(tc.lang)
		if rc.Language != tc.lang || !strings.Contains(rc.Markdown, tc.recapHeading) {
			miss = strings.TrimSpace(miss + fmt.Sprintf(" language %q, heading %q", rc.Language, strings.SplitN(rc.Markdown, "\n", 2)[0]))
		}
		title := ""
		if len(rc.Timeline) > 0 {
			title = rc.Timeline[0].Title
		}
		row(tc.tag, "recap", title, counts, s, miss)
	}
	t.Logf("model %s\n%s\n\n%s", cfg.Model, strings.Join(rows, "\n"), strings.Join(cards, "\n"))
}

// topicCount is how many topics o has in all.
func topicCount(o *MinutesOverview) int {
	n := 0
	for _, g := range o.Groups {
		n += len(g.Topics)
	}
	return n
}
