package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"). Placeholders only.

// overviewFixtures is the shared fixture the mock hub, the iPhone and the web check too.
const overviewFixtures = "../../../spec/fixtures/minutes-overview/cases.json"

type overviewCase struct {
	Name  string `json:"name"`
	Input struct {
		Minutes json.RawMessage `json:"minutes"`
	} `json:"input"`
	Expected struct {
		Layout   string          `json:"layout"`
		Overview json.RawMessage `json:"overview"`
		Markdown *string         `json:"markdown"`
	} `json:"expected"`
}

func overviewCases(t *testing.T) map[string]overviewCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(overviewFixtures))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int            `json:"version"`
		Cases   []overviewCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("fixtures: version %d, %d cases, %v", file.Version, len(file.Cases), err)
	}
	out := map[string]overviewCase{}
	for _, c := range file.Cases {
		if _, dup := out[c.Name]; dup || c.Name == "" {
			t.Fatalf("case name %q twice or empty", c.Name)
		}
		out[c.Name] = c
	}
	return out
}

// sprintMeeting is the contract's example meeting: 70 minutes from 14:00, a line at each item's time, the calendar
// naming it, its place and its people; photo 2 (at 1950 s) has words, photo 1 none.
func sprintMeeting(t *testing.T, e *Engine, start time.Time) {
	t.Helper()
	lines := []struct {
		at   float64
		text string
	}{
		{0, "Let's start: the export, the release date and the cache bug."},
		{312, "Ada says the export drops entries over 4 MB."},
		{655, "The size-limit message is done."},
		{1504.5, "So we keep the cap, and warn when an entry is skipped."},
		{1810, "The 20th is at risk."},
		{1830, "Bo raised the load-test QPS to 40K."},
		{2100, "Bo adds the warning text and the regression test by Friday."},
		{2290, "If the cache bug is not fixed by Wednesday, the release moves a week."},
		{2350, "Cy shares the load-test report."},
		{2408, "Is the cap per entry or per recording? Not decided."},
	}
	u := RecordingUpload{Revision: 1, StartedAt: start, Duration: 4200, Language: "en-US", Device: "phone", Complete: true,
		Kind: RecordingMeeting, Calendar: &RecordingCalendar{Title: "Sprint review", Attendees: []string{"Ada", "Bo", "Cy"}, Location: "Room 3"}}
	for _, l := range lines {
		u.Segments = append(u.Segments, RecordingSegment{Start: l.at, End: l.at + 9, Text: l.text})
	}
	end := start.Add(70 * time.Minute)
	u.EndedAt = &end
	putRecording(t, e, recID, u, start.Add(2*time.Hour))
	addPhoto(t, e, recID, photoUpload(1, 100, "", ""), false)
	addPhoto(t, e, recID, photoUpload(2, 1950, "Whiteboard", "cache bug: reproduced once, weak network"), false)
}

// sprintReply is what a model answers for sprintMeeting in the summary card's shape, its key figures marked.
const sprintReply = `{
  "title": "Sprint review",
  "where": "",
  "attendees": ["Ada", "Bo", "Cy"],
  "headline": "Export ships with the size cap; the release date waits on the cache fix",
  "conclusions": [
    {"text": "Export ships with the **4 MB** cap and a warning", "line": 4},
    {"text": "The release date hangs on the cache fix by **Wednesday**", "line": 8},
    {"text": "Load target raised to **40K** QPS", "line": "[6]"}
  ],
  "topics": [
    {"title": "Export", "group": "Project progress", "icon": "progress",
     "points": [{"text": "Export drops entries over **4 MB**", "line": 2, "who": "Ada"},
                {"text": "The export size-limit message is **done**", "line": 3}],
     "decisions": [{"text": "Export **keeps the cap**, with a warning when an entry is skipped", "line": 4}]},
    {"title": "Release date", "group": "Project progress", "icon": "schedule",
     "points": [{"text": "The release on the 20th is **at risk**", "line": 5}],
     "decisions": [{"text": "The release moves **a week** unless the cache bug is fixed by Wednesday", "line": 8}]},
    {"title": "Load testing", "group": "Tooling and performance", "icon": "performance",
     "points": [{"text": "Load-test QPS raised to **40K**", "line": 6, "who": "Bo"}]},
    {"title": "Cache bug", "group": "Tooling and performance", "icon": "quality",
     "points": [{"text": "The cache bug reproduced **once** on a weak network", "photo": "P2"}]}
  ],
  "actions": [
    {"who": "Bo", "what": "Add the warning text and the regression test", "by": "Friday", "due_date": "2026-11-13", "line": 7},
    {"who": "Cy", "what": "Share the load-test report", "line": 9}
  ],
  "open": [{"text": "Whether the cap is per entry or per recording", "line": 10}]
}`

// jsonEqual: a and b are the same JSON once decoded into T (and so in T's field order).
func jsonEqual[T any](t *testing.T, a, b any) bool {
	t.Helper()
	norm := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var x T
		if err := json.Unmarshal(raw, &x); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(x)
		return string(out)
	}
	return norm(a) == norm(b)
}

// overviewProblems are the guarantees ("The `overview` field") m's overview breaks.
func overviewProblems(m *Minutes) []string {
	var bad []string
	add := func(f string, a ...any) { bad = append(bad, fmt.Sprintf(f, a...)) }
	for _, p := range m.Points {
		if strings.Contains(p.Text, "**") {
			add("point %q has **", p.Text)
		}
	}
	for _, d := range m.Decisions {
		if strings.Contains(d.Text, "**") {
			add("decision %q has **", d.Text)
		}
	}
	o := m.Overview
	if o == nil {
		return bad
	}
	noMarks := func(what, s string, most int) {
		if strings.Contains(s, "**") {
			add("%s %q has **", what, s)
		}
		if utf8.RuneCountInString(s) > most {
			add("%s %q is over %d", what, s, most)
		}
	}
	strongOK := func(text string, strong []string) {
		if len(strong) > minutesStrongMax || len(strongSpans(text, strong)) != len(strong) {
			add("strong %q of %q", strong, text)
		}
		for _, s := range strong {
			if n := utf8.RuneCountInString(s); n < 1 || n > minutesStrongRunes || s == text {
				add("strong %q of %q", s, text)
			}
		}
	}
	noMarks("headline", o.Headline, minutesHeadlineRunes)
	if strings.EqualFold(o.Headline, m.Title) && o.Headline != "" {
		add("headline is the title")
	}
	if len(o.Groups) == 0 || len(o.Groups) > minutesGroupsMax || len(o.Groups) == 1 && o.Groups[0].Title != "" {
		add("%d groups", len(o.Groups))
	}
	var bullets []MinutesBullet
	cites := map[int]bool{}
	topics := 0
	lastGroup := math.Inf(-1)
	for _, g := range o.Groups {
		noMarks("group", g.Title, minutesGroupRunes)
		if len(o.Groups) > 1 && g.Title == "" {
			add("an untitled group among %d", len(o.Groups))
		}
		if len(g.Topics) == 0 {
			add("group %q has no topic", g.Title)
			continue
		}
		if f := firstAt(g.Topics[0]); f < lastGroup {
			add("group %q out of order", g.Title)
		} else {
			lastGroup = f
		}
		lastTopic := math.Inf(-1)
		for _, tp := range g.Topics {
			topics++
			noMarks("topic", tp.Title, minutesTopicRunes)
			if tp.Title == "" || !slices.Contains(minutesTopicIcons, tp.Icon) || len(tp.Bullets) == 0 {
				add("topic %+v", tp)
				continue
			}
			if firstAt(tp) < lastTopic {
				add("topic %q out of order", tp.Title)
			}
			lastTopic = firstAt(tp)
			for i, b := range tp.Bullets {
				noMarks("bullet", b.Text, minutesTextRunes)
				strongOK(b.Text, b.Strong)
				if i > 0 && b.At < tp.Bullets[i-1].At {
					add("bullet %q out of order", b.Text)
				}
				if b.Decision && b.Who != "" {
					add("decision %q has a who", b.Text)
				}
				if b.Photo > 0 {
					cites[b.Photo] = true
				}
				bullets = append(bullets, b)
			}
		}
	}
	if topics > minutesTopics {
		add("%d topics", topics)
	}
	for i := 0; i < len(o.Groups); i++ {
		for j := i + 1; j < len(o.Groups); j++ {
			if strings.EqualFold(o.Groups[i].Title, o.Groups[j].Title) {
				add("group %q twice", o.Groups[i].Title)
			}
		}
	}
	if len(o.Conclusions) > minutesConclusions {
		add("%d conclusions", len(o.Conclusions))
	}
	for _, c := range o.Conclusions {
		noMarks("conclusion", c.Text, minutesConclusionRunes)
		strongOK(c.Text, c.Strong)
		if c.Photo > 0 && !cites[c.Photo] {
			add("conclusion %q cites photo %d no bullet cites", c.Text, c.Photo)
		}
	}
	// One to one: every point and decision is one bullet, every bullet one of them.
	var mirror []MinutesBullet
	for _, p := range m.Points {
		mirror = append(mirror, MinutesBullet{Text: p.Text, At: p.At, Photo: p.Photo, Who: p.Who})
	}
	for _, d := range m.Decisions {
		mirror = append(mirror, MinutesBullet{Text: d.Text, At: d.At, Photo: d.Photo, Decision: true})
	}
	key := func(b MinutesBullet) string {
		return fmt.Sprintf("%v|%q|%v|%d|%q", b.Decision, b.Text, b.At, b.Photo, b.Who)
	}
	var got, want []string
	for _, b := range bullets {
		got = append(got, key(b))
	}
	for _, b := range mirror {
		want = append(want, key(b))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		add("bullets %v, items %v", got, want)
	}
	return bad
}

func checkOverview(t *testing.T, m *Minutes) {
	t.Helper()
	for _, p := range overviewProblems(m) {
		t.Error(p)
	}
}

// The contract's example meeting answered in the summary card's shape: the stored minutes are the shared fixture's
// "rich" case, overview and legacy lists alike, word for word, and so is their Markdown; the legacy lists have no
// marks, the push counts the decisions as before.
func TestMinutesOverview(t *testing.T) {
	rich := overviewCases(t)["rich"]
	var want Minutes
	if err := json.Unmarshal(rich.Input.Minutes, &want); err != nil || want.Overview == nil {
		t.Fatalf("fixture: %v", err)
	}
	m := &captureModel{reply: sprintReply}
	e := goldenEngine(t, m)
	var told []Minutes
	e.MinutesWritten = func(_ context.Context, _ Recording, mm Minutes) { told = append(told, mm) }
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	sprintMeeting(t, e, start)
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	got, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != want.Title || got.When != want.When || got.Where != want.Where || strings.Join(got.Attendees, ",") != "Ada,Bo,Cy" ||
		got.Language != "en" {
		t.Errorf("head %+v", got)
	}
	for name, pair := range map[string][2]any{"points": {got.Points, want.Points}, "decisions": {got.Decisions, want.Decisions},
		"actions": {got.Actions, want.Actions}, "open": {got.Open, want.Open}} {
		if !jsonEqual[any](t, pair[0], pair[1]) {
			a, _ := json.Marshal(pair[0])
			b, _ := json.Marshal(pair[1])
			t.Errorf("%s\n got %s\nwant %s", name, a, b)
		}
	}
	if !jsonEqual[MinutesOverview](t, got.Overview, want.Overview) {
		a, _ := json.MarshalIndent(got.Overview, "", " ")
		t.Errorf("overview:\n%s", a)
	}
	checkOverview(t, got)
	if got.Markdown != *rich.Expected.Markdown {
		t.Errorf("markdown:\n%s\n--- want ---\n%s", got.Markdown, *rich.Expected.Markdown)
	}
	if md := readFile(t, e, recID, "minutes.md"); md != got.Markdown+"\n" {
		t.Errorf("minutes.md %q", md)
	}
	// What minutes.json carries for clients to show has no ** (only its Markdown has): strong is a field of its own.
	var stored map[string]any
	if err := json.Unmarshal([]byte(readFile(t, e, recID, "minutes.json")), &stored); err != nil {
		t.Fatal(err)
	}
	delete(stored, "markdown")
	if raw, _ := json.Marshal(stored); strings.Contains(string(raw), "**") || !strings.Contains(string(raw), `"strong":[`) {
		t.Errorf("minutes.json:\n%s", raw)
	}
	if len(told) != 1 || len(told[0].Decisions) != 2 || len(told[0].Actions) != 2 {
		t.Errorf("told %+v", told)
	}
	// Searched as before: a decision is found in minutes.md, after its "Decided:".
	if ms := e.Matches(KindRecording, recID, "keeps the cap", 40); len(ms) != 1 || !strings.HasSuffix(ms[0].Path, "/minutes.md") {
		t.Errorf("matches %+v", ms)
	}
	// The cited photos are the items': photo 2 only.
	if ps, _, _ := e.Photos(recID); len(ps) != 2 || ps[0].Cited || !ps[1].Cited {
		t.Errorf("photos %+v", ps)
	}
}

// writeOne writes the minutes of a three-line English meeting (lines 1–3 at 0, 10 and 20 s) answered with reply,
// and what the log said.
func writeOne(t *testing.T, reply string) (*Engine, *Minutes, []string) {
	t.Helper()
	e := goldenEngine(t, &funcModel{f: func(int, string, string) (string, error) { return reply, nil }})
	var mu sync.Mutex
	var logged []string
	e.Logf = func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		logged = append(logged, fmt.Sprintf(f, a...))
	}
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	u := meetingUpload(1, start, "First line", "Second line", "Third line")
	u.Language = "en-US"
	putRecording(t, e, recID, u, start.Add(time.Hour))
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d of %s; log %v", n, reply, logged)
	}
	m, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if r := recordingNow(t, e, recID); r.MinutesState != MinutesReady || r.MinutesFails != 0 {
		t.Fatalf("%q after %d failures", r.MinutesState, r.MinutesFails)
	}
	return e, m, logged
}

// topicsReply is an answer with n topics, topic i titled title(i) holding one point that cites line 1 + i%3.
func topicsReply(n int, title func(i int) string) string {
	var topics []string
	for i := range n {
		topics = append(topics, fmt.Sprintf(`{"title":%q,"icon":"general","points":[{"text":"Point %d","line":%d}]}`, title(i), i, 1+i%3))
	}
	return `{"title":"t","topics":[` + strings.Join(topics, ",") + `]}`
}

// The overview is all or nothing: an old-shaped answer, a mix, a topic with items but no title, more than 12 topics,
// or nothing to put under a topic leaves it out, with the minutes written as before (the Markdown byte for byte),
// never as a failure, and the log saying why. What counts is what is kept.
func TestMinutesOverviewLeftOut(t *testing.T) {
	legacy := "# t\n\n- When: 2026-11-10 14:00–14:01\n\n## Key points\n\n- [14:00:00] First\n\n## Decisions\n\n- [14:00:10] Second"
	for _, tc := range []struct {
		name, reply, why, markdown string
	}{
		{"old shape", `{"title":"t","points":[{"text":"First","line":1}],"decisions":[{"text":"Second","line":2}]}`,
			"points or decisions outside topics", legacy},
		{"mix", `{"title":"t","topics":[{"title":"A","points":[{"text":"First","line":1}]}],"decisions":[{"text":"Second","line":2}]}`,
			"points or decisions outside topics", legacy},
		{"untitled topic", `{"title":"t","topics":[{"title":"A","points":[{"text":"First","line":1}]},{"title":" ** ","decisions":[{"text":"Second","line":2}]}]}`,
			"a topic with items has no title", legacy},
		{"13 topics", topicsReply(13, func(i int) string { return fmt.Sprintf("Topic %d", i) }), "more than 12 topics", ""},
		{"no point or decision", `{"title":"t","topics":[{"title":"A","points":[{"text":"Nowhere","line":9}]}],"actions":[{"what":"Send it","line":3}]}`,
			"no point or decision", "# t\n\n- When: 2026-11-10 14:00–14:01\n\n## Action items\n\n- [14:00:20] Send it"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, m, logged := writeOne(t, tc.reply)
			if m.Overview != nil {
				t.Fatalf("overview %+v", m.Overview)
			}
			if tc.markdown != "" && m.Markdown != tc.markdown {
				t.Errorf("markdown:\n%s\n--- want ---\n%s", m.Markdown, tc.markdown)
			}
			if strings.Contains(m.Markdown, "AI-generated") || strings.Contains(m.Markdown, "Core conclusions") {
				t.Errorf("markdown in the card's layout:\n%s", m.Markdown)
			}
			if want := "kb: recording " + recID + ": minutes: no overview: " + tc.why; !slices.Contains(logged, want) {
				t.Errorf("log %q, want %q", logged, want)
			}
		})
	}
	// Counted after the checks: a point outside topics that cites nothing read, and an untitled topic whose items all
	// went, leave the overview in; two titles that differ only in case are one topic, so 13 are 12.
	for name, reply := range map[string]string{
		"dropped outside": `{"title":"t","topics":[{"title":"A","points":[{"text":"First","line":1}]},{"title":"","points":[{"text":"Gone","line":99}]}],` +
			`"points":[{"text":"Nowhere","line":42}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, m, logged := writeOne(t, reply)
			if m.Overview == nil {
				t.Fatalf("no overview: %v", logged)
			}
			checkOverview(t, m)
		})
	}
	_, m, _ := writeOne(t, topicsReply(13, func(i int) string {
		if i == 12 {
			return "TOPIC 0"
		}
		return fmt.Sprintf("Topic %d", i)
	}))
	if g := m.Overview.Groups; len(g) != 1 || len(g[0].Topics) != 12 || g[0].Topics[0].Title != "Topic 0" || len(g[0].Topics[0].Bullets) != 2 {
		t.Errorf("merged %+v", g)
	}
}

// build decodes reply as the model's answer does (JSONMax unmarshals it) and builds the minutes from read.
func build(t *testing.T, e *Engine, reply string, read []minutesLine) (*Minutes, error) {
	t.Helper()
	var a minutesAnswer
	if err := json.Unmarshal([]byte(reply), &a); err != nil {
		return nil, err
	}
	r := &Recording{ID: recID, StartedAt: time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone), Duration: 600, Title: "Weekly sync"}
	return e.buildMinutes(r, a, read, goldenZone, i18n.En)
}

var overviewRead = []minutesLine{{N: 1, At: 0, Text: "first"}, {N: 2, At: 10, Text: "second"}, {N: 3, At: 20, Text: "third"},
	{N: 4, At: 30, Text: "fourth"}, {Photo: 1, At: 15, Text: "whiteboard"}}

// The summary card's own fields never cost the minutes: a malformed headline, conclusions (or one of them), a topic's
// title, group or icon is dropped (an icon becomes general); only topics that are not a list, a topic that is not an
// object or a topic's malformed points or decisions make the answer unusable, as malformed points do. Lengths are
// clipped, marks taken out, and a conclusion cites a photo only when a bullet does.
func TestMinutesOverviewLenient(t *testing.T) {
	e := goldenEngine(t, nil)
	one := func(reply string) *Minutes {
		t.Helper()
		m, err := build(t, e, reply, overviewRead)
		if err != nil {
			t.Fatalf("%s: %v", reply, err)
		}
		return m
	}
	m := one(`{"headline":3,"conclusions":{},"topics":[{"title":"Export","group":5,"icon":"rocket","points":[{"text":"First **done**","line":1}]},` +
		`{"title":"Cache","icon":" Quality ","points":[{"text":"Second","line":2}]},{"title":"Load","icon":7,"decisions":[{"text":"Third","line":3}]}]}`)
	if o := m.Overview; o == nil || o.Headline != "" || o.Conclusions != nil || len(o.Groups) != 1 || o.Groups[0].Title != "" ||
		o.Groups[0].Topics[0].Icon != "general" || o.Groups[0].Topics[1].Icon != "quality" || o.Groups[0].Topics[2].Icon != "general" ||
		!slices.Equal(o.Groups[0].Topics[0].Bullets[0].Strong, []string{"done"}) {
		t.Fatalf("overview %+v", m.Overview)
	}
	checkOverview(t, m)

	long := strings.Repeat("长", 300)
	m = one(`{"title":"weekly SYNC","headline":"Weekly sync","conclusions":["x",{"text":5,"line":1},null,{"text":"Ok **here**","line":1},` +
		`{"text":"No citation","line":99},{"text":"Photo only","photo":1},{"text":"Photo and line","photo":1,"line":2},{"text":"Ok **here**","line":2},` +
		`{"text":"` + long + `","line":3},{"text":"Fifth","line":4},{"text":"Sixth","line":4}],` +
		`"topics":[{"title":"**` + long + `**","points":[{"text":"First","line":1}]}],` +
		`"actions":[{"what":"Send **the** notes","line":1}],"open":[{"text":"Is it **per entry**?","line":2}]}`)
	o := m.Overview
	if o == nil || o.Headline != "" || len(o.Conclusions) != minutesConclusions || o.Conclusions[0].Text != "Ok here" ||
		!jsonEqual[MinutesConclusion](t, o.Conclusions[1], MinutesConclusion{Text: "Photo and line", At: 10}) ||
		utf8.RuneCountInString(o.Conclusions[2].Text) != minutesConclusionRunes || !strings.HasSuffix(o.Conclusions[2].Text, "…") ||
		o.Conclusions[3].Text != "Fifth" {
		t.Fatalf("conclusions %+v", o)
	}
	if title := o.Groups[0].Topics[0].Title; utf8.RuneCountInString(title) != minutesTopicRunes || strings.Contains(title, "*") {
		t.Errorf("title %q", title)
	}
	if m.Actions[0].What != "Send the notes" || m.Open[0].Text != "Is it per entry?" {
		t.Errorf("marks left in %+v %+v", m.Actions, m.Open)
	}
	checkOverview(t, m)

	// A photo a bullet cites: the conclusion cites it too, at its time. The headline is one line, clipped.
	m = one(`{"headline":"A\nheadline ` + long + `","conclusions":[{"text":"From the board","photo":"P1","line":1}],"topics":[{"title":"Board","points":[{"text":"Board","photo":1}]}]}`)
	if o := m.Overview; o == nil || !jsonEqual[MinutesConclusion](t, o.Conclusions[0], MinutesConclusion{Text: "From the board", At: 15, Photo: 1}) ||
		!strings.HasPrefix(o.Headline, "A headline 长") || utf8.RuneCountInString(o.Headline) != minutesHeadlineRunes {
		t.Fatalf("overview %+v", o)
	}
	// A topic's title that is not a string leaves the overview out; the minutes stay.
	if m := one(`{"topics":[{"title":5,"points":[{"text":"First","line":1}]}]}`); m.Overview != nil || len(m.Points) != 1 {
		t.Errorf("numeric title: %+v", m)
	}
	// A topic that is not an object (null too) costs the answer, even beside a topic whose cited point would make it
	// usable; a null or missing topics is none.
	ready := `{"title":"A","points":[{"text":"Ready","line":1}]}`
	for _, unusable := range []string{`{"topics":"x"}`, `{"topics":{}}`, `{"topics":["x"]}`, `{"topics":[[]]}`,
		`{"topics":[null,` + ready + `]}`, `{"topics":[` + ready + `, null ]}`, `{"topics":[` + ready + `,5]}`, `{"topics":[true,` + ready + `]}`,
		`{"topics":[{"title":"A","points":"x"}]}`, `{"topics":[{"title":"A","points":[{"text":5,"line":1}]}]}`,
		`{"topics":[{"title":"A","decisions":[{"text":"x","line":1}],"points":[{"text":"y","line":1,"who":3}]}]}`} {
		if _, err := build(t, e, unusable, overviewRead); err == nil || !unusableAnswer(err) {
			t.Errorf("%s: %v, want an unusable answer", unusable, err)
		}
	}
	for _, none := range []string{`{"topics":null,"points":[{"text":"Ready","line":1}]}`, `{"topics":[],"points":[{"text":"Ready","line":1}]}`} {
		if m := one(none); m.Overview != nil || len(m.Points) != 1 {
			t.Errorf("%s: %+v", none, m)
		}
	}
	// A stray ** outside the texts that may carry marks (the title, the place, a name, a deadline as said) is taken
	// out too: the names still match the calendar and the transcript, so the point and the action keep their owners.
	r := &Recording{ID: recID, StartedAt: time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone), Duration: 600,
		Calendar: &RecordingCalendar{Attendees: []string{"Ada"}}}
	read := []minutesLine{{N: 1, At: 0, Text: "first"}, {N: 2, At: 10, Text: "Bo says second"}, {N: 3, At: 20, Text: "third, in Room 3"}}
	var a minutesAnswer
	if err := json.Unmarshal([]byte(`{"title":"**Weekly** sync","where":"**Room 3**","attendees":["**Bo**"],`+
		`"topics":[{"title":"A","points":[{"text":"First","line":1,"who":"**Ada**"},{"text":"Second","line":2,"who":"**Bo**"}]}],`+
		`"actions":[{"who":"**Bo**","what":"Send it","by":"**Friday**","due_date":"**2026-11-13**","line":3}]}`), &a); err != nil {
		t.Fatal(err)
	}
	m, err := e.buildMinutes(r, a, read, goldenZone, i18n.En)
	if err != nil {
		t.Fatal(err)
	}
	if m.Title != "Weekly sync" || m.Where != "Room 3" || !slices.Equal(m.Attendees, []string{"Ada", "Bo"}) ||
		m.Points[0].Who != "Ada" || m.Points[1].Who != "Bo" || m.Overview.Groups[0].Topics[0].Bullets[1].Who != "Bo" {
		t.Errorf("marks left in %+v", m)
	}
	if ac := m.Actions[0]; ac.Who != "Bo" || ac.By != "Friday" || ac.DueDate != "2026-11-13" {
		t.Errorf("action %+v", ac)
	}

	// Every item's text is at most 400 runes, the "…" of a cut counted: 399 and 400 stay whole, 401 is cut; a bullet
	// is its point word for word.
	x := func(n int) string { return strings.Repeat("x", n) }
	m = one(`{"topics":[{"title":"A","points":[{"text":"` + x(399) + `","line":1},{"text":"` + x(400) + `","line":2},` +
		`{"text":"` + x(401) + `","line":3}],"decisions":[{"text":"` + x(397) + ` **cut**","line":4}]}],` +
		`"actions":[{"what":"` + x(401) + `","line":1}],"open":[{"text":"` + x(400) + `y","line":2}]}`)
	var lengths []int
	for _, p := range m.Points {
		lengths = append(lengths, utf8.RuneCountInString(p.Text))
	}
	if !slices.Equal(lengths, []int{399, 400, 400}) || m.Points[2].Text != x(399)+"…" || m.Decisions[0].Text != x(397)+" c…" ||
		m.Overview.Groups[0].Topics[0].Bullets[3].Strong != nil || m.Actions[0].What != x(399)+"…" || m.Open[0].Text != x(399)+"…" {
		t.Errorf("clipped %v %+v %+v %+v", lengths, m.Decisions, m.Actions, m.Open)
	}
	checkOverview(t, m)

	// Conclusions alone are not minutes: they never count as the citation that makes an answer usable.
	if _, err := build(t, e, `{"headline":"h","conclusions":[{"text":"c","line":1}],"topics":[{"title":"A"}]}`, overviewRead); !errors.Is(err, errMinutesUnusable) {
		t.Errorf("conclusions only: %v", err)
	}
}

// groupsOf is each group's title and its topics' titles, "title: a, b".
func groupsOf(o *MinutesOverview) []string {
	var out []string
	if o == nil {
		return nil
	}
	for _, g := range o.Groups {
		var ts []string
		for _, tp := range g.Topics {
			ts = append(ts, tp.Title)
		}
		out = append(out, g.Title+": "+strings.Join(ts, ", "))
	}
	return out
}

// Named groups only with at least 4 topics that all name one of 2 to 4 groups (names compared after cleaning,
// ignoring case); otherwise one untitled group. Topics are ordered by their first bullet, groups by their first
// topic; same-titled topics are merged into the first, which keeps its title, group and icon.
func TestMinutesOverviewGroups(t *testing.T) {
	e := goldenEngine(t, nil)
	topic := func(title, group string, lines ...int) string {
		var ps []string
		for _, l := range lines {
			ps = append(ps, fmt.Sprintf(`{"text":"%s %d","line":%d}`, title, l, l))
		}
		return fmt.Sprintf(`{"title":%q,"group":%q,"icon":"progress","points":[%s]}`, title, group, strings.Join(ps, ","))
	}
	long := strings.Repeat("g", 30)
	for _, tc := range []struct {
		name   string
		topics []string
		want   []string
	}{
		{"three named", []string{topic("A", "X", 1), topic("B", "Y", 2), topic("C", "Y", 3)}, []string{": A, B, C"}},
		{"four in two, by time", []string{topic("A", "Late", 3), topic("B", "Early", 1), topic("C", "Late", 4), topic("D", "Early", 2)},
			[]string{"Early: B, D", "Late: A, C"}},
		{"one without a group", []string{topic("A", "X", 1), topic("B", "Y", 2), topic("C", "", 3), topic("D", "Y", 4)}, []string{": A, B, C, D"}},
		{"five groups", []string{topic("A", "V", 1), topic("B", "W", 2), topic("C", "X", 3), topic("D", "Y", 4), topic("E", "Z", 4)},
			[]string{": A, B, C, D, E"}},
		{"one group", []string{topic("A", "X", 1), topic("B", "X", 2), topic("C", "x", 3), topic("D", "X", 4)}, []string{": A, B, C, D"}},
		{"case and clipping", []string{topic("A", "Progress", 1), topic("B", "ops", 2), topic("C", " progress ", 3), topic("D", "**OPS**", 4)},
			[]string{"Progress: A, C", "ops: B, D"}},
		{"names equal once clipped", []string{topic("A", long+"1", 1), topic("B", long+"2", 2), topic("C", "Other", 3), topic("D", "Other", 4)},
			[]string{strings.Repeat("g", 23) + "…: A, B", "Other: C, D"}},
		{"merged topics", []string{topic("Export", "X", 3), topic("Cache", "Y", 2), topic("export", "Y", 1), topic("Load", "Y", 4)},
			[]string{": Export, Cache, Load"}},
		{"merged into four", []string{topic("A", "X", 3), topic("B", "Y", 2), topic("a", "Y", 1), topic("C", "Y", 4), topic("D", "X", 4)},
			[]string{"X: A, D", "Y: B, C"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := build(t, e, `{"topics":[`+strings.Join(tc.topics, ",")+`]}`, overviewRead)
			if err != nil {
				t.Fatal(err)
			}
			if got := groupsOf(m.Overview); !slices.Equal(got, tc.want) {
				t.Errorf("groups %q, want %q", got, tc.want)
			}
			checkOverview(t, m)
		})
	}
	// The merged topic keeps the first one's title, group and icon, and has both's bullets in time order.
	m, _ := build(t, e, `{"topics":[{"title":"Export","group":"X","icon":"risk","points":[{"text":"late","line":3}]},`+
		`{"title":"EXPORT","group":"Y","icon":"design","points":[{"text":"early","line":1}]}]}`, overviewRead)
	if tp := m.Overview.Groups[0].Topics[0]; tp.Title != "Export" || tp.Icon != "risk" || tp.Bullets[0].Text != "early" || tp.Bullets[1].Text != "late" {
		t.Errorf("merged %+v", tp)
	}
}

// strongText takes the model's marks out and keeps the places every client will find where the model marked them.
func TestStrongText(t *testing.T) {
	x := func(n int) string { return strings.Repeat("x", n) }
	long, longer := x(395), x(400)
	for _, tc := range []struct {
		in, plain string
		strong    []string
	}{
		{"QPS raised to **40K**", "QPS raised to 40K", []string{"40K"}},
		{"module X **done**", "module X done", []string{"done"}},
		{"**from 20K** to **40K**", "from 20K to 40K", []string{"from 20K", "40K"}},
		{"**a** **b** **c**", "a b c", []string{"a", "b"}},
		{"**The whole text**", "The whole text", nil},
		{"**Done**.", "Done.", nil},
		{"QPS ** 40K, ** now", "QPS 40K, now", []string{"40K"}},
		{"40K before, now **40K**", "40K before, now 40K", nil},
		{"From **20K** to 40K and **40K** holds", "From 20K to 40K and 40K holds", []string{"20K"}},
		{"From **20K** to **40K** and 40K holds", "From 20K to 40K and 40K holds", []string{"20K", "40K"}},
		{"an **unclosed mark", "an unclosed mark", nil},
		{"a **b** c **d", "a b c d", []string{"b"}},
		{"QPS ***40K***", "QPS *40K*", []string{"40K"}},
		{"****", "", nil},
		{"", "", nil},
		{"  spaced   **out**  text ", "spaced out text", []string{"out"}},
		{"line\nbreak **kept**\tout", "line break kept out", []string{"kept"}},
		{"**" + strings.Repeat("长", 21) + "** and more", strings.Repeat("长", 21) + " and more", nil},
		{"**" + strings.Repeat("长", 20) + "** and more", strings.Repeat("长", 20) + " and more", []string{strings.Repeat("长", 20)}},
		{"导出模块**已完成**。", "导出模块已完成。", []string{"已完成"}},
		{"状态**「已完成」**", "状态「已完成」", []string{"已完成"}},
		{"命中率 **92%**，延迟 **-5%**", "命中率 92%，延迟 -5%", []string{"92%", "-5%"}},
		{"ticket **#42** for **@Ada**", "ticket #42 for @Ada", []string{"#42", "@Ada"}},
		{"Load target raised to **40%.**", "Load target raised to 40%.", []string{"40%"}}, // the full stop goes out, % stays
		{"latency **(-5%)**", "latency (-5%)", []string{"-5%"}},
		{"plain text", "plain text", nil},
		// At most 400 runes, the "…" of a cut counted: 399 and 400 stay whole, 401 keeps 399 and "…".
		{x(399), x(399), nil},
		{x(400), x(400), nil},
		{x(401), x(399) + "…", nil},
		{long + " **cut here**", long + " cut…", nil}, // the cut reaches the mark: its words stay, not bold
		{x(395) + " **abcde**", x(395) + " abc…", nil},
		{x(394) + " **abcde**", x(394) + " abcde", []string{"abcde"}},    // 400: not cut
		{x(393) + " **abcde** y", x(393) + " abcde…", []string{"abcde"}}, // cut right after the mark
		{x(393) + " **abcde y**", x(393) + " abcde…", nil},               // cut inside it
		{"**kept** " + longer, "kept " + x(394) + "…", []string{"kept"}},
	} {
		plain, strong := strongText(tc.in, minutesTextRunes)
		if plain != tc.plain || !slices.Equal(strong, tc.strong) {
			t.Errorf("strongText(%q) = %q %q, want %q %q", tc.in, plain, strong, tc.plain, tc.strong)
		}
	}
	for _, tc := range []struct {
		text   string
		strong []string
		want   string
	}{
		{"a*b is **x**", []string{"a*b"}, `**a\*b** is \*\*x\*\*`},
		{"40K now, 20K last week", []string{"20K", "40K"}, "40K now, **20K** last week"},
		{"none here", []string{"", "x"}, "none here"},
	} {
		if got := cardMarkdown(tc.text, tc.strong); got != tc.want {
			t.Errorf("cardMarkdown(%q, %q) = %q, want %q", tc.text, tc.strong, got, tc.want)
		}
	}
}

// Whatever the model writes: no ** is left, the text is the text without marks on one line, at most two places of
// 1 to 20 runes are kept, each found where it is by the clients' rule, none the whole text or with a space or
// punctuation at its ends; a text without marks is the same text, clipped, with none.
func FuzzStrongText(f *testing.F) {
	for _, s := range []string{"QPS raised to **40K**", "**a** **b** **c**", "40K before **40K**", "***x***", "** **", "a**", "**「完成」**",
		"From **20K** to 40K and **40K**", "**" + strings.Repeat("y", 30) + "**", "é **é**"} {
		f.Add(s, 400)
		f.Add(s, 5)
	}
	f.Fuzz(func(t *testing.T, s string, n int) {
		if !utf8.ValidString(s) || n < 1 || n > 1000 {
			return // the model's words come through JSON: valid UTF-8
		}
		plain, strong := strongText(s, n)
		if strings.Contains(plain, "**") {
			t.Fatalf("%q: ** left in %q", s, plain)
		}
		if want := cardLine(s, n); plain != want || utf8.RuneCountInString(plain) > n {
			t.Fatalf("%q: %q, want %q", s, plain, want)
		}
		if !strings.Contains(s, "**") && strong != nil {
			t.Fatalf("%q: strong %q without marks", s, strong)
		}
		if len(strong) > minutesStrongMax || len(strongSpans(plain, strong)) != len(strong) {
			t.Fatalf("%q: strong %q in %q", s, strong, plain)
		}
		whole := strings.TrimFunc(plain, strongEdge)
		for _, x := range strong {
			r, _ := utf8.DecodeRuneInString(x)
			l, _ := utf8.DecodeLastRuneInString(x)
			if n := utf8.RuneCountInString(x); n < 1 || n > minutesStrongRunes || x == whole || strongEdge(r) || strongEdge(l) {
				t.Fatalf("%q: strong %q in %q", s, x, plain)
			}
		}
	})
}

// cleanOverview is the clients' reading of an overview ("Clients"), for the shared fixture: nil when no bullet is
// left. Each conclusion and bullet is its words as segments (the bold rule is strongSpans), its at (nil: no time) and
// photo (nil: none).
func cleanOverview(raw json.RawMessage) map[string]any {
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	groups, ok := o["groups"].([]any)
	if !ok {
		return nil
	}
	text := func(x map[string]any) (string, bool) {
		s, ok := x["text"].(string)
		return s, ok && strings.TrimSpace(s) != ""
	}
	strong := func(x map[string]any) []string {
		var out []string
		list, _ := x["strong"].([]any)
		for _, s := range list {
			if s, ok := s.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	segments := func(s string, strong []string) []any {
		out := []any{}
		at := 0
		for _, sp := range strongSpans(s, strong) {
			if sp[0] > at {
				out = append(out, map[string]any{"text": s[at:sp[0]], "bold": false})
			}
			out = append(out, map[string]any{"text": s[sp[0]:sp[1]], "bold": true})
			at = sp[1]
		}
		if at < len(s) {
			out = append(out, map[string]any{"text": s[at:], "bold": false})
		}
		return out
	}
	at := func(x map[string]any) any {
		if f, ok := x["at"].(float64); ok && f >= 0 {
			return f
		}
		return nil
	}
	photo := func(x map[string]any) any {
		if f, ok := x["photo"].(float64); ok && f > 0 && f == math.Trunc(f) {
			return f
		}
		return nil
	}
	out := map[string]any{"headline": nil, "conclusions": []any{}, "groups": []any{}}
	if h, ok := o["headline"].(string); ok && strings.TrimSpace(h) != "" {
		out["headline"] = h
	}
	list, _ := o["conclusions"].([]any)
	for _, c := range list {
		c, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if s, ok := text(c); ok {
			out["conclusions"] = append(out["conclusions"].([]any), map[string]any{"segments": segments(s, strong(c)), "at": at(c), "photo": photo(c)})
		}
	}
	for _, g := range groups {
		g, ok := g.(map[string]any)
		if !ok {
			continue
		}
		topics, ok := g["topics"].([]any)
		if !ok {
			continue
		}
		title, _ := g["title"].(string)
		var kept []any
		for _, tp := range topics {
			tp, ok := tp.(map[string]any)
			if !ok {
				continue
			}
			bullets, ok := tp["bullets"].([]any)
			name, named := tp["title"].(string)
			if !ok || !named || strings.TrimSpace(name) == "" {
				continue
			}
			icon, _ := tp["icon"].(string)
			if !slices.Contains(minutesTopicIcons, icon) {
				icon = "general"
			}
			var bs []any
			for _, b := range bullets {
				b, ok := b.(map[string]any)
				if !ok {
					continue
				}
				s, ok := text(b)
				if !ok {
					continue
				}
				var who any
				if w, ok := b["who"].(string); ok {
					who = w
				}
				decision, _ := b["decision"].(bool)
				bs = append(bs, map[string]any{"segments": segments(s, strong(b)), "at": at(b), "photo": photo(b), "who": who, "decision": decision})
			}
			if len(bs) > 0 {
				kept = append(kept, map[string]any{"title": name, "icon": icon, "bullets": bs})
			}
		}
		if len(kept) > 0 {
			out["groups"] = append(out["groups"].([]any), map[string]any{"title": title, "topics": kept})
		}
	}
	if len(out["groups"].([]any)) == 0 {
		return nil
	}
	return out
}

// The shared fixture (spec/fixtures/minutes-overview), as the clients read it: the layout each case gets, what is
// left of its overview and its bold segments; and, for the cases the hub writes (or reads as none), the hub's
// Markdown of the same minutes, byte for byte. The hub reads an overview it cannot decode as none.
func TestMinutesOverviewFixture(t *testing.T) {
	cases := overviewCases(t)
	for _, name := range []string{"legacy_only", "rich", "one_group", "repeated_figure", "unknown_icon", "strong_not_found", "empty_topic",
		"overview_empty_object", "overview_string", "overview_null", "groups_not_array", "nothing_left", "malformed_members", "figure_signs",
		"photo_numbers"} {
		if _, ok := cases[name]; !ok {
			t.Errorf("no case %s", name)
		}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(c.Input.Minutes, &fields); err != nil {
				t.Fatal(err)
			}
			got := cleanOverview(fields["overview"])
			layout := "legacy"
			if got != nil {
				layout = "overview"
			}
			if layout != c.Expected.Layout {
				t.Fatalf("layout %s, want %s", layout, c.Expected.Layout)
			}
			var want any
			if err := json.Unmarshal(c.Expected.Overview, &want); err != nil {
				t.Fatal(err)
			}
			if got != nil || want != nil {
				a, _ := json.Marshal(got)
				b, _ := json.Marshal(want)
				if string(a) != string(b) {
					t.Errorf("overview\n got %s\nwant %s", a, b)
				}
			}
			// The legacy fields decode whatever the overview is.
			var m Minutes
			if err := json.Unmarshal(c.Input.Minutes, &m); err != nil {
				t.Fatalf("the minutes do not decode: %v", err)
			}
			if c.Expected.Markdown == nil {
				return
			}
			if (m.Overview != nil) != (layout == "overview") {
				t.Errorf("the hub reads an overview: %v, the layout is %s", m.Overview != nil, layout)
			}
			if md := renderMinutes(&m, m.Start, m.Start.Location()); md != *c.Expected.Markdown {
				t.Errorf("markdown:\n%s\n--- want ---\n%s", md, *c.Expected.Markdown)
			}
			if name == "rich" || name == "one_group" || name == "figure_signs" { // the others break what the hub guarantees on purpose
				checkOverview(t, &m)
			}
		})
	}
}

// A part's notes for the merging call: every point and decision it gave, in a topic or not, those of a titled topic
// tagged with it, without marks; each section in the order of what it cites before the per-part cap, so the cap
// drops the part's latest lines, not a whole topic; a part's headline and conclusions are not read.
func TestMinutesNotesForMerge(t *testing.T) {
	chunk := []minutesLine{{N: 5, At: 40}, {N: 6, At: 50}, {Photo: 2, At: 55}, {N: 7, At: 60}, {N: 8, At: 70}}
	var a minutesAnswer
	if err := json.Unmarshal([]byte(`{"headline":"partheadline","conclusions":[{"text":"partconclusion","line":5}],
	  "topics":[{"title":"Late","points":[{"text":"late **one**","line":8},{"text":"later","line":9}]},
	            {"title":"**Early**","points":[{"text":"early","line":5}],"decisions":[{"text":"board","photo":2},{"text":"decided","line":7}]},
	            {"title":"","points":[{"text":"untitled","line":6}]}],
	  "points":[{"text":"outside","line":7}],"open":[{"text":"open **q**","line":8},{"text":"first q","line":5}],
	  "where":"**Room 3**","attendees":["**Bo**"],"actions":[{"who":"**Bo**","what":"**send** it","by":"**Friday**","line":6}]}`), &a); err != nil {
		t.Fatal(err)
	}
	p := notesForMerge(2, a, chunk, time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone), goldenZone, 3)
	show := func(list []prompts.KBMinutesNote) string {
		var out []string
		for _, n := range list {
			out = append(out, n.Text+"〈"+n.Topic+"〉")
		}
		return strings.Join(out, " | ")
	}
	if got := show(p.Points); got != "early〈Early〉 | untitled〈〉 | outside〈〉" {
		t.Errorf("points %s", got)
	}
	if got := show(p.Decisions); got != "board〈Early〉 | decided〈Early〉" || p.Decisions[0].Photo != 2 {
		t.Errorf("decisions %s", got)
	}
	if got := show(p.Open); got != "first q〈〉 | open q〈〉" {
		t.Errorf("open %s", got)
	}
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "partheadline") || strings.Contains(string(raw), "partconclusion") {
		t.Errorf("a part's headline or conclusions in its notes: %s", raw)
	}
	// Notes carry no marks: not in the texts, the place, the names or a deadline either.
	if raw, _ := json.Marshal(p); strings.Contains(string(raw), "**") || p.Where != "Room 3" || !slices.Equal(p.Attendees, []string{"Bo"}) ||
		len(p.Actions) != 1 || p.Actions[0].Who != "Bo" || p.Actions[0].By != "Friday" || p.Actions[0].Text != "send it" {
		t.Errorf("marks in the notes: %s", raw)
	}
}

// A deleted photo takes the bullets and conclusions citing it with its items: an empty topic goes, then an empty
// group, the group rule is applied again, and the overview goes once no topic is left.
func TestMinutesOverviewPhotoDropped(t *testing.T) {
	b := func(text string, at float64, photo int) MinutesBullet {
		return MinutesBullet{Text: text, At: at, Photo: photo}
	}
	m := &Minutes{Title: "t",
		Points: []MinutesPoint{{Text: "a", At: 1, Photo: 1}, {Text: "b", At: 2}, {Text: "c", At: 3}, {Text: "d", At: 4, Photo: 2}, {Text: "e", At: 5}},
		Overview: &MinutesOverview{
			Conclusions: []MinutesConclusion{{Text: "from the board", At: 1, Photo: 1}, {Text: "said", At: 2}},
			Groups: []MinutesGroup{
				{Title: "X", Topics: []MinutesTopic{{Title: "A", Icon: "general", Bullets: []MinutesBullet{b("a", 1, 1)}},
					{Title: "B", Icon: "general", Bullets: []MinutesBullet{b("b", 2, 0)}}}},
				{Title: "Y", Topics: []MinutesTopic{{Title: "C", Icon: "general", Bullets: []MinutesBullet{b("c", 3, 0)}},
					{Title: "D", Icon: "general", Bullets: []MinutesBullet{b("d", 4, 2), b("e", 5, 0)}}}},
			}}}
	checkOverview(t, m)
	dropPhotos(m, func(n int) bool { return n == 1 })
	if got := groupsOf(m.Overview); !slices.Equal(got, []string{": B, C, D"}) || len(m.Overview.Conclusions) != 1 || len(m.Points) != 4 {
		t.Fatalf("after photo 1: %q %+v", got, m.Overview.Conclusions)
	}
	checkOverview(t, m)
	dropPhotos(m, func(n int) bool { return n == 2 })
	if got := groupsOf(m.Overview); !slices.Equal(got, []string{": B, C, D"}) || len(m.Overview.Groups[0].Topics[2].Bullets) != 1 {
		t.Fatalf("after photo 2: %q", got)
	}
	checkOverview(t, m)
	only := &Minutes{Points: []MinutesPoint{{Text: "a", At: 1, Photo: 3}}, Actions: []MinutesAction{{ID: "a1", What: "x", At: 2}},
		Overview: &MinutesOverview{Headline: "h", Conclusions: []MinutesConclusion{{Text: "c", At: 1, Photo: 3}},
			Groups: []MinutesGroup{{Topics: []MinutesTopic{{Title: "A", Icon: "general", Bullets: []MinutesBullet{b("a", 1, 3)}}}}}}}
	dropPhotos(only, func(n int) bool { return n == 3 })
	if only.Overview != nil || len(only.Points) != 0 || len(only.Actions) != 1 {
		t.Fatalf("nothing left: %+v", only)
	}
	if got := renderMinutes(only, time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone), goldenZone); strings.Contains(got, "AI") {
		t.Errorf("markdown:\n%s", got)
	}
}
