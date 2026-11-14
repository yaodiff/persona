package kb

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Minutes, a recording's summary and a day's recap are written in the recording's language, whatever languages its
// transcript mixes, their Markdown's headings too, and keep it for every later render (docs/assistant/
// meeting-minutes.md "How the hub generates them"). Placeholders only.

// mixedMeeting is a meeting said mostly in Chinese, with English and a little Japanese in it.
var mixedMeeting = []string{
	"大家好，我们先看 export 的 bug",
	"Ada said the export drops entries over 4 MB",
	"那这一版先保留上限",
	"Bo will add the regression test by Friday",
	"上限は件ごとか録音ごとか、まだ決まっていません",
}

// cjkText is any Han, Hiragana or Katakana letter.
var cjkText = regexp.MustCompile(`[\p{Han}\p{Hiragana}\p{Katakana}]`)

// storedLanguage is the language field of recording id's minutes.json or recap.json as stored.
func storedLanguage(t *testing.T, e *Engine, id, name string) string {
	t.Helper()
	raw, err := os.ReadFile(e.Store.Path(KindRecording, id, name))
	if err != nil {
		t.Fatal(err)
	}
	var head struct {
		Language *string `json:"language"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		t.Fatal(err)
	}
	if head.Language == nil {
		return "<none>"
	}
	return *head.Language
}

// A mixed-language meeting recorded as English or Japanese: the system prompt asks for that language whatever the
// transcript mixes (not 「用中文写」), the Markdown's headings are in it, and minutes.json says it.
func TestMinutesInTheRecordingsLanguage(t *testing.T) {
	for _, tc := range []struct {
		tag, golden, ask, code string
		reply                  string
		want, never            []string
	}{
		{tag: "en-US", golden: "en", ask: "都用English写", code: "en",
			reply: `{"title":"Export review","attendees":["Ada","Bo"],
			  "points":[{"text":"The export drops entries over 4 MB","line":2,"who":"Ada"}],
			  "decisions":[{"text":"Keep the cap for this release","line":3}],
			  "actions":[{"who":"Bo","what":"Add the regression test","by":"by Friday","line":4}],
			  "open":[{"text":"Whether the cap is per entry or per recording","line":5}]}`,
			want: []string{"# Export review\n", "- When: 2026-11-10 14:00–14:01", "- Attendees: Ada, Bo", "## Key points\n",
				"- [14:00:10] The export drops entries over 4 MB (Ada)", "## Decisions\n", "## Action items\n",
				"- [14:00:30] Bo: Add the regression test (by Friday)", "## Open questions\n"}},
		{tag: "ja-JP", golden: "ja", ask: "都用日本語写", code: "ja",
			reply: `{"title":"エクスポートのレビュー","attendees":["Ada","Bo"],
			  "points":[{"text":"4 MB を超えるエントリがエクスポートで落ちる","line":2,"who":"Ada"}],
			  "decisions":[{"text":"今回のリリースでは上限を残す","line":3}],
			  "actions":[{"who":"Bo","what":"回帰テストを追加する","by":"金曜日まで","line":4}],
			  "open":[{"text":"上限は件ごとか録音ごとか","line":5}]}`,
			want: []string{"- 日時：2026-11-10 14:00–14:01", "- 参加者：Ada、Bo", "## 要点\n", "落ちる（Ada）", "## 決定事項\n",
				"## アクション項目\n", "- [14:00:30] Bo：回帰テストを追加する（金曜日まで）", "## 未解決の質問\n"},
			never: []string{"时间：", "参会", "讨论要点", "结论与决定", "待办", "未决问题"}},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			m := &captureModel{reply: tc.reply}
			e := goldenEngine(t, m)
			start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
			u := meetingUpload(1, start, mixedMeeting...)
			u.Language = tc.tag
			ended := start.Add(50 * time.Second)
			u.EndedAt = &ended
			putRecording(t, e, recID, u, start.Add(time.Hour))
			if n := e.WriteMinutes(context.Background(), 2); n != 1 || len(m.calls) != 1 {
				t.Fatalf("wrote %d in %d calls", n, len(m.calls))
			}
			system := m.calls[0][0]
			if !strings.Contains(system, tc.ask) || !strings.Contains(system, "不管原文里混着几种语言") || strings.Contains(system, "用中文写") {
				t.Errorf("the system prompt does not ask for %s:\n%s", tc.tag, system)
			}
			golden(t, "minutes.system."+tc.golden, system)
			golden(t, "minutes.user."+tc.golden, m.calls[0][1])
			got, err := e.Store.ReadMinutes(recID)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got.Markdown, want) {
					t.Errorf("markdown lacks %q:\n%s", want, got.Markdown)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(got.Markdown, never) {
					t.Errorf("markdown has the Chinese %q:\n%s", never, got.Markdown)
				}
			}
			if tc.code == "en" && cjkText.MatchString(got.Markdown) {
				t.Errorf("English minutes with CJK text:\n%s", got.Markdown)
			}
			if lang := storedLanguage(t, e, recID, "minutes.json"); lang != tc.code {
				t.Errorf("minutes.json language %q, want %q", lang, tc.code)
			}
			if md := readFile(t, e, recID, "minutes.md"); strings.TrimSpace(md) != got.Markdown {
				t.Errorf("minutes.md %q", md)
			}
		})
	}
}

// Minutes nothing names (no title from the user, the calendar or the model, no summary's title yet) are called when the
// meeting started in their language, not by the recording's Chinese name in lists: English minutes never start with
// 「# 录音」. Chinese ones keep that name.
func TestUntitledMinutesInTheRecordingsLanguage(t *testing.T) {
	for tag, want := range map[string]string{"en-US": "Recording 2026-11-10 14:00", "ja-JP": "録音 2026-11-10 14:00",
		"zh-CN": "录音 2026-11-10 14:00"} {
		t.Run(tag, func(t *testing.T) {
			e := goldenEngine(t, &captureModel{reply: `{"title":"","points":[{"text":"The export drops entries","line":2}]}`})
			start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
			u := meetingUpload(1, start, mixedMeeting...)
			u.Language = tag
			putRecording(t, e, recID, u, start.Add(time.Hour))
			if n := e.WriteMinutes(context.Background(), 2); n != 1 {
				t.Fatalf("wrote %d", n)
			}
			m, err := e.Store.ReadMinutes(recID)
			if err != nil {
				t.Fatal(err)
			}
			if m.Title != want || !strings.HasPrefix(m.Markdown, "# "+want+"\n") {
				t.Errorf("title %q, markdown:\n%s", m.Title, m.Markdown)
			}
		})
	}
}

// A long mixed-language meeting recorded as English: every part's notes and the merge are asked for in English, and the
// minutes are English.
func TestMinutesChunkedInTheRecordingsLanguage(t *testing.T) {
	var texts []string
	for i := range 300 {
		texts = append(texts, fmt.Sprintf("%d %s", i+1, strings.Repeat(mixedMeeting[i%len(mixedMeeting)]+" ", 3)))
	}
	model := &funcModel{f: func(_ int, system, user string) (string, error) {
		if strings.Contains(system, "已经分段整理过") {
			return `{"title":"Long review","points":[{"text":"The opening","line":1}],"decisions":[{"text":"Keep the cap","line":1}]}`, nil
		}
		first := strings.SplitN(strings.SplitN(user, "转写：\n", 2)[1], " ", 2)[0] // "[N]"
		return fmt.Sprintf(`{"points":[{"text":"The opening","line":%q}]}`, first), nil
	}}
	e := goldenEngine(t, model)
	now := time.Date(2026, 11, 10, 18, 0, 0, 0, goldenZone)
	u := meetingUpload(1, now.Add(-2*time.Hour), texts...)
	u.Language = "en-US"
	putRecording(t, e, recID, u, now)
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	merges := 0
	for _, c := range model.calls {
		if !strings.Contains(c[0], "都用English写") || strings.Contains(c[0], "用中文写") {
			t.Errorf("a call not asked for English:\n%s", c[0])
		}
		if strings.Contains(c[0], "已经分段整理过") {
			merges++
			golden(t, "minutes_merge.system.en", c[0])
		}
	}
	if len(model.calls) < 3 || merges != 1 {
		t.Fatalf("%d calls, %d merges", len(model.calls), merges)
	}
	got, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Markdown, "## Key points\n") || !strings.Contains(got.Markdown, "## Decisions\n") || cjkText.MatchString(got.Markdown) {
		t.Errorf("markdown:\n%s", got.Markdown)
	}
	if lang := storedLanguage(t, e, recID, "minutes.json"); lang != "en" {
		t.Errorf("minutes.json language %q", lang)
	}
}

// A recording's summary of a mixed-language recording made in English is asked for in English, its fixed sections'
// names and 「无」 too.
func TestRecordingSummaryInTheRecordingsLanguage(t *testing.T) {
	m := &captureModel{reply: `{"title":"Export review","summary":"## What this is\nAn export review.\n\n## Key points\n- 14:00 The cap stays\n\n## To-dos and open questions\nNone",` +
		`"one_line":"The export cap stays for now","importance":"P2","relevance":50,"tags":["export"]}`}
	e := goldenEngine(t, m)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, goldenZone)
	u := recUpload(1, now.Add(-time.Hour), true, mixedMeeting...)
	u.Language = "en-US"
	putRecording(t, e, recID, u, now)
	if n := e.SummarizeRecordings(context.Background(), 3); n != 1 || len(m.calls) != 1 {
		t.Fatalf("summarized %d in %d calls", n, len(m.calls))
	}
	system := m.calls[0][0]
	for _, want := range []string{"都用English写", "不管原文里混着几种语言", "小节名和「不确定」「无」都换成English的说法"} {
		if !strings.Contains(system, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, system)
		}
	}
	if strings.Contains(system, "全部用中文") || strings.Contains(system, "用中文写") {
		t.Errorf("the system prompt still asks for Chinese:\n%s", system)
	}
	golden(t, "recording.system.en", system)
	golden(t, "recording.user.en", m.calls[0][1])
}

// A day recorded in English: its hours' notes and the recap are asked for in English, the recap's Markdown is English
// (headings, the gaps' reasons, so far) and recap.json says it.
func TestDayRecapInTheRecordingsLanguage(t *testing.T) {
	now := dayClock.Add(2 * time.Hour)
	m := &captureModel{reply: `{"summary":"Reviewed the export","entries":[{"from_line":1,"to_line":3,"kind":"meeting","title":"Export review",
	  "points":[{"text":"The export drops entries over 4 MB","line":2,"who":"Ada"},{"text":"Bo adds the regression test","line":3}]}],
	  "actions":[{"who":"Bo","what":"Add the regression test","by":"by Friday","line":3}]}`}
	e := dayEngine(t, m, &now)
	u := dayUpload(1, "大家好，今天评审 export", "Ada says the export drops entries over 4 MB", "Bo、金曜日までに regression test を追加してください")
	u.Language = "en-US"
	u.Coverage = &RecordingCoverage{To: 7200, Gaps: []CoverageGap{{From: 600, To: 1200, Reason: GapInterrupted}}}
	mustPut(t, e, dayID, u, now)
	if n := e.WriteDayRecaps(context.Background(), 1); n != 1 || len(m.calls) != 1 {
		t.Fatalf("worked on %d, %d calls", n, len(m.calls))
	}
	if system := m.calls[0][0]; !strings.Contains(system, "都用English写") || strings.Contains(system, "用中文写") {
		t.Errorf("the notes' prompt does not ask for English:\n%s", system)
	}
	golden(t, "day_recap.system.en", m.calls[0][0])
	golden(t, "day_recap.user.en", m.calls[0][1])

	f := &dayFake{merge: func(string) (string, error) {
		return `{"summary":"Reviewed the export cap.","timeline":[
		  {"from_line":1,"to_line":2,"kind":"meeting","meeting":0,"title":"Export review","points":[{"text":"The export drops entries over 4 MB","line":2,"who":"Ada"}]},
		  {"from_line":3,"to_line":3,"kind":"outside","meeting":0,"title":"Hallway chat","points":[{"text":"Bo adds the regression test","line":3}]}],
		  "actions":[{"who":"Bo","what":"Add the regression test","by":"by Friday","line":3}]}`, nil
	}}
	e.Model = f
	if err := e.AskMinutes(dayID); err != nil {
		t.Fatal(err)
	}
	if n := e.WriteDayRecaps(context.Background(), 1); n != 1 {
		t.Fatalf("recap: worked on %d", n)
	}
	if len(f.system) != 1 || !strings.Contains(f.system[0], "写这一天的全天回顾") || !strings.Contains(f.system[0], "都用English写") ||
		strings.Contains(f.system[0], "用中文写") {
		t.Fatalf("the merge's prompt does not ask for English: %q", f.system)
	}
	golden(t, "day_recap_merge.system.en", f.system[0])
	rc, err := e.ReadDayRecap(dayID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Day recap · 2026-11-11\n", "Reviewed the export cap.", "- Recorded: 08:00–09:00 (so far)",
		"- Not recorded: 08:10–08:20 (interrupted)", "## Meetings\n", "- 08:00–08:01 Export review", "  - [08:01] The export drops entries over 4 MB (Ada)",
		"## Outside meetings\n", "## Suggested to-dos\n", "- [08:02] Bo: Add the regression test (by Friday)"} {
		if !strings.Contains(rc.Markdown, want) {
			t.Errorf("markdown lacks %q:\n%s", want, rc.Markdown)
		}
	}
	if cjkText.MatchString(rc.Markdown) {
		t.Errorf("an English recap with CJK text:\n%s", rc.Markdown)
	}
	if lang := storedLanguage(t, e, dayID, recapFile); lang != "en" {
		t.Errorf("recap.json language %q", lang)
	}
}

// A recap keeps its language when an erase writes its Markdown again (repairDayRecap), whatever the recording says
// now: an English one stays English after a PUT that says Japanese, and one an older hub wrote of an English day
// (recap.json saying no language) stays Chinese.
func TestDayRecapRepairedKeepsItsLanguage(t *testing.T) {
	for _, tc := range []struct {
		name, later string
		old         bool   // recap.json without its language, as written before it was kept
		code        string // recap.json's language after the erase
		want, never []string
	}{
		{name: "recording changed", later: "ja-JP", code: "en",
			want: []string{"# Day recap · 2026-11-11\n", "- Recorded: 08:00–09:00 (so far)", "- Not recorded: 08:02–08:03 (erased)",
				"## Meetings\n", "- 08:00–08:02 Morning standup", "## Suggested to-dos\n", "- [08:01] Bo: Ship the release"},
			never: []string{"Hallway"}},
		{name: "old", later: "en-US", old: true, code: "<none>",
			want: []string{"# 2026-11-11 全天回顾\n", "- 时段：08:00–09:00（截至目前）", "- 没有录到：08:02–08:03（已擦除）", "## 会议\n",
				"## 建议待办\n", "- [08:01] Bo：Ship the release"},
			never: []string{"Hallway", "Day recap", "Not recorded", "Meetings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := dayClock.Add(2 * time.Hour)
			f := &dayFake{merge: func(string) (string, error) {
				return `{"summary":"Standup, then a hallway chat.","timeline":[
				  {"from_line":1,"to_line":3,"kind":"meeting","title":"Morning standup","points":[{"text":"Ada runs the tests","line":3,"who":"Ada"}]},
				  {"from_line":4,"to_line":4,"kind":"outside","title":"Hallway chat","points":[{"text":"The hallway chat","line":4}]}],
				  "actions":[{"who":"Bo","what":"Ship the release","line":2}]}`, nil
			}}
			e := dayEngine(t, f, &now)
			texts := []string{"Morning standup", "Bo ships the release", "Ada runs the tests", "a hallway chat"}
			u := dayUpload(1, texts...)
			u.Language, u.Coverage.To = "en-US", 3600
			mustPut(t, e, dayID, u, now)
			writeTestRecapWith(t, e, dayID, f)
			if lang := storedLanguage(t, e, dayID, recapFile); lang != "en" {
				t.Fatalf("recap.json language %q", lang)
			}
			if tc.old {
				path := e.Store.Path(KindRecording, dayID, recapFile)
				var raw map[string]any
				if err := json.Unmarshal([]byte(readFile(t, e, dayID, recapFile)), &raw); err != nil {
					t.Fatal(err)
				}
				delete(raw, "language")
				b, _ := json.MarshalIndent(raw, "", "  ")
				if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The phone erased 08:03 (line 4).
			erase := dayUpload(5, texts[0], texts[1], texts[2], "")
			erase.Language = tc.later
			erase.Coverage = &RecordingCoverage{To: 3600, Gaps: []CoverageGap{{From: 170, To: 200, Reason: GapErased}}}
			mustPut(t, e, dayID, erase, now)
			rc, err := e.ReadDayRecap(dayID)
			if err != nil || rc.Version != 2 || len(rc.Gaps) != 1 {
				t.Fatalf("after the erase: %+v %v", rc, err)
			}
			md := readFile(t, e, dayID, recapMarkdown)
			for _, want := range tc.want {
				if !strings.Contains(md, want) {
					t.Errorf("recap.md lacks %q:\n%s", want, md)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(md, never) {
					t.Errorf("recap.md has %q:\n%s", never, md)
				}
			}
			if tc.code == "en" && cjkText.MatchString(md) {
				t.Errorf("an English recap with CJK text:\n%s", md)
			}
			if lang := storedLanguage(t, e, dayID, recapFile); lang != tc.code {
				t.Errorf("recap.json language %q, want %q", lang, tc.code)
			}
		})
	}
}

// Minutes keep their language when a photo's delete renders them again, whatever the recording says now: English ones
// stay English, after a PUT that says Japanese too, and ones an older hub wrote of an English recording (minutes.json
// saying no language) stay Chinese. The summary card's Markdown too (#6563): its headings and AI note in that language,
// the topic and the conclusion the photo held gone with it.
func TestMinutesRenderedAgainKeepTheirLanguage(t *testing.T) {
	reply := func(_ int, _, user string) (string, error) {
		if strings.Contains(user, "[P1] ") {
			return `{"title":"Review","points":[{"text":"The whiteboard","photo":1},{"text":"The first line","line":1}],
			  "actions":[{"who":"Bo","what":"Send the notes","line":2}]}`, nil
		}
		return `{"title":"Review","points":[{"text":"The first line","line":1}]}`, nil
	}
	card := func(_ int, _, user string) (string, error) {
		if strings.Contains(user, "[P1] ") {
			return `{"title":"Review","headline":"The review in one line",
			  "conclusions":[{"text":"From the **whiteboard**","photo":1},{"text":"The first line **holds**","line":1}],
			  "topics":[{"title":"Board","icon":"design","points":[{"text":"The whiteboard","photo":1}]},
			            {"title":"Opening","icon":"general","points":[{"text":"The first line","line":1}]}],
			  "actions":[{"who":"Bo","what":"Send the notes","line":2}]}`, nil
		}
		return `{"title":"Review","points":[{"text":"The first line","line":1}]}`, nil
	}
	english := []string{"## Key points\n", "## Action items\n", "- [14:00:10] Bo: Send the notes"}
	for _, tc := range []struct {
		name, tag string
		card      bool   // answered in the summary card's shape
		code      string // the minutes' language ("en" when "")
		old       bool   // minutes.json without its language, as written before it was kept
		later     string // the language a PUT after the minutes says, before the delete
		want      []string
		never     []string
	}{
		{name: "english", tag: "en-US", want: english, never: []string{"讨论要点", "待办", "照片"}},
		{name: "recording changed", tag: "en-US", later: "ja-JP", want: english,
			never: []string{"要点", "アクション項目", "日時", "讨论要点", "待办"}},
		{name: "old", tag: "en-US", old: true, want: []string{"- 时间：", "## 讨论要点\n", "## 待办\n", "- [14:00:10] Bo：Send the notes"},
			never: []string{"Key points", "Action items", "When:"}},
		{name: "english card", tag: "en-US", card: true, later: "ja-JP",
			want: []string{"\nThe review in one line\n", "## Core conclusions\n\n- [14:00:00] The first line **holds**\n", "## Opening\n\n- [14:00:00] The first line\n",
				"## Action items\n", "- [14:00:10] Bo: Send the notes\n\n*AI-generated from the recording. Check anything important.*"},
			never: []string{"whiteboard", "Board", "Key points", "photo", "主な結論"}},
		{name: "japanese card", tag: "ja-JP", card: true, code: "ja",
			want: []string{"## 主な結論\n", "## Opening\n", "## アクション項目\n", "- [14:00:10] Bo：Send the notes",
				"*録音から AI が作成しました。重要な内容は確認してください。*"},
			never: []string{"whiteboard", "Core conclusions", "## 要点", "写真"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reply
			if tc.card {
				f = card
			}
			e := goldenEngine(t, &funcModel{f: f})
			start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
			u := meetingUpload(1, start, "第一句 the first line", "Bo sends the notes", "第三句")
			u.Language = tc.tag
			end := start.Add(50 * time.Second)
			u.EndedAt = &end
			putRecording(t, e, recID, u, start.Add(time.Minute))
			addPhoto(t, e, recID, photoUpload(1, 0, "whiteboard", "budget"), false)
			e.WriteMinutes(context.Background(), 2)
			if lang := storedLanguage(t, e, recID, "minutes.json"); lang != cmp.Or(tc.code, "en") {
				t.Fatalf("minutes.json language %q", lang)
			}
			if m, _ := e.Store.ReadMinutes(recID); (m.Overview != nil) != tc.card || tc.card && len(m.Overview.Groups[0].Topics) != 2 {
				t.Fatalf("overview %+v", m.Overview)
			}
			if tc.later != "" {
				again := meetingUpload(2, start, "第一句 the first line", "Bo sends the notes", "第三句")
				again.Language, again.EndedAt = tc.later, &end
				putRecording(t, e, recID, again, start.Add(2*time.Minute))
				if r := recordingNow(t, e, recID); r.Language != tc.later {
					t.Fatalf("the recording says %q", r.Language)
				}
			}
			if tc.old {
				path := e.Store.Path(KindRecording, recID, "minutes.json")
				var raw map[string]any
				if err := json.Unmarshal([]byte(readFile(t, e, recID, "minutes.json")), &raw); err != nil {
					t.Fatal(err)
				}
				delete(raw, "language")
				b, _ := json.MarshalIndent(raw, "", "  ")
				if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := e.PhotoDelete(recID, 1); !ok || err != nil {
				t.Fatalf("delete: %v %v", ok, err)
			}
			m, err := e.Store.ReadMinutes(recID)
			if err != nil || len(m.Points) != 1 || tc.card && (m.Overview == nil || len(m.Overview.Conclusions) != 1) {
				t.Fatalf("after the delete: %+v %v", m, err)
			}
			checkOverview(t, m)
			md := readFile(t, e, recID, "minutes.md")
			if strings.Contains(md, "The whiteboard") {
				t.Fatalf("minutes.md not written again:\n%s", md)
			}
			for _, want := range tc.want {
				if !strings.Contains(md, want) {
					t.Errorf("minutes.md lacks %q:\n%s", want, md)
				}
			}
			for _, never := range tc.never {
				if strings.Contains(md, never) {
					t.Errorf("minutes.md has %q:\n%s", never, md)
				}
			}
		})
	}
}

// The minutes' and the recap's Markdown name their sections as the web's recording page does in every language, so
// minutes copied from it read like the screen (web/src/i18n/locales/<lang>/knowledge-kbdetail.json).
func TestMinutesHeadingsAreTheWebsSections(t *testing.T) {
	for _, l := range i18n.Langs {
		raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "i18n", "locales", string(l), "knowledge-kbdetail.json"))
		if err != nil {
			t.Fatal(err)
		}
		var web map[string]string
		if err := json.Unmarshal(raw, &web); err != nil {
			t.Fatal(err)
		}
		for hub, page := range map[string]string{"kb-minutes.points": "minutes_points", "kb-minutes.decisions": "minutes_decisions",
			"kb-minutes.actions": "minutes_actions", "kb-minutes.open": "minutes_open", "kb-day-recap.actions": "recap_actions",
			"kb-minutes.conclusions": "minutes_conclusions", "kb-minutes.ai_note": "minutes_ai_note"} {
			if got := i18n.T(l, hub); got != web[page] {
				t.Errorf("%s: %s is %q, the web's %s %q", l, hub, got, page, web[page])
			}
		}
	}
}
