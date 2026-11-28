package writers

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
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// debatesFixtures is the shared fixture the hub, mock hub, web and iPhone check (#16728).
const debatesFixtures = "../../../../../spec/fixtures/minutes-debates/cases.json"

type debatesCase struct {
	Name  string `json:"name"`
	Input struct {
		Minutes json.RawMessage `json:"minutes"`
	} `json:"input"`
	Expected struct {
		Debates  json.RawMessage `json:"debates"`
		Markdown *string         `json:"markdown"`
	} `json:"expected"`
}

func debatesCases(t *testing.T) map[string]debatesCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(debatesFixtures))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Version int           `json:"version"`
		Cases   []debatesCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != 1 || len(file.Cases) == 0 {
		t.Fatalf("fixtures: version %d, %d cases, %v", file.Version, len(file.Cases), err)
	}
	out := map[string]debatesCase{}
	for _, c := range file.Cases {
		if _, dup := out[c.Name]; dup || c.Name == "" {
			t.Fatalf("case name %q twice or empty", c.Name)
		}
		out[c.Name] = c
	}
	return out
}

// cleanDebates is the clients' reading of detail.minutes.debates ("Clients" in docs/assistant/meeting-minutes.md),
// returning []any{} when none survives.
func cleanDebates(raw json.RawMessage, decisions int) []any {
	out := []any{}
	var v any
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return out
	}
	list, ok := v.([]any)
	if !ok {
		return out
	}
	nonBlank := func(x any) (string, bool) {
		s, ok := x.(string)
		return s, ok && strings.TrimSpace(s) != ""
	}
	nonNeg := func(x any) any {
		if f, ok := x.(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 {
			return f
		}
		return nil
	}
	for _, entry := range list {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		q, ok := nonBlank(obj["question"])
		if !ok {
			continue
		}
		rawPos, ok := obj["positions"].([]any)
		if !ok {
			continue
		}
		var positions []any
		for _, pe := range rawPos {
			po, ok := pe.(map[string]any)
			if !ok {
				continue
			}
			stance, ok := nonBlank(po["stance"])
			if !ok {
				continue
			}
			var who any
			if w, ok := nonBlank(po["who"]); ok {
				who = w
			}
			positions = append(positions, map[string]any{
				"stance": stance,
				"at":     nonNeg(po["at"]),
				"who":    who,
			})
		}
		if len(positions) < minutesDebatePosMin {
			continue
		}
		var topic any
		if tp, ok := nonBlank(obj["topic"]); ok {
			topic = tp
		}
		var status any
		if s, ok := obj["status"].(string); ok && (s == string(DebateOpen) || s == string(DebateSettled)) {
			status = s
		}
		var outcome any
		if status == string(DebateSettled) {
			if oo, ok := obj["outcome"].(map[string]any); ok {
				if text, ok := nonBlank(oo["text"]); ok {
					var dec any
					if f, ok := oo["decision"].(float64); ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f >= 0 && math.Trunc(f) == f {
						if idx := int(f); idx >= 0 && idx < decisions {
							dec = float64(idx)
						}
					}
					outcome = map[string]any{
						"text":     text,
						"at":       nonNeg(oo["at"]),
						"decision": dec,
					}
				}
			}
		}
		out = append(out, map[string]any{
			"question":  q,
			"topic":     topic,
			"status":    status,
			"at":        nonNeg(obj["at"]),
			"positions": positions,
			"outcome":   outcome,
		})
	}
	return out
}

// TestMinutesDebatesFixture verifies every case of spec/fixtures/minutes-debates/cases.json:
// client cleaning, lenient recordingwire.Minutes decoding, hub Markdown in both layouts across en/zh-Hans/ja,
// and byte-for-byte identity with no-debates Markdown when debates are empty or clean down to nothing.
func TestMinutesDebatesFixture(t *testing.T) {
	cases := debatesCases(t)
	required := []string{
		"overview_with_debates", "legacy_with_debates_en", "legacy_with_debates_zh", "legacy_with_debates_ja",
		"legacy_no_debates_zh", "legacy_no_debates_ja", "empty_debates_overview", "empty_debates_legacy",
		"cleaned_to_empty_overview", "debates_not_array", "debates_null", "debates_object",
		"unknown_status_and_extra_keys", "malformed_members", "overview_star_escape",
	}
	for _, name := range required {
		if _, ok := cases[name]; !ok {
			t.Errorf("missing fixture case %s", name)
		}
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(c.Input.Minutes, &fields); err != nil {
				t.Fatal(err)
			}
			var decs []json.RawMessage
			if raw, ok := fields["decisions"]; ok {
				_ = json.Unmarshal(raw, &decs)
			}
			got := cleanDebates(fields["debates"], len(decs))
			var want any
			if err := json.Unmarshal(c.Expected.Debates, &want); err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(got)
			b, _ := json.Marshal(want)
			if string(a) != string(b) {
				t.Errorf("debates\n got %s\nwant %s", a, b)
			}

			// The rest of the minutes always decodes whatever debates holds.
			var m Minutes
			if err := json.Unmarshal(c.Input.Minutes, &m); err != nil {
				t.Fatalf("the minutes do not decode: %v", err)
			}
			if c.Expected.Markdown == nil {
				return
			}
			if (len(m.Debates) > 0) != (len(got) > 0) {
				t.Errorf("hub decoded %d debates, client kept %d", len(m.Debates), len(got))
			}
			if md := renderMinutes(&m, m.Start, m.Start.Location()); md != *c.Expected.Markdown {
				t.Errorf("markdown:\n%s\n--- want ---\n%s", md, *c.Expected.Markdown)
			}

			// Empty or malformed debates must render byte for byte identically to minutes without the debates key.
			if len(got) == 0 {
				delete(fields, "debates")
				stripped, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				var plain Minutes
				if err := json.Unmarshal(stripped, &plain); err != nil {
					t.Fatal(err)
				}
				if without := renderMinutes(&plain, plain.Start, plain.Start.Location()); without != *c.Expected.Markdown {
					t.Errorf("without debates differs:\n%s\n--- with ---\n%s", without, *c.Expected.Markdown)
				}
			}
		})
	}
}

// TestMinutesDebatesBuildAndLenient verifies hub-side verification, caps, outcome-to-decision linking,
// lenient decoding of malformed model answers, usability isolation, omitempty in minutes.json,
// unchanged earlier-meeting material, and output size within the 6000-token cap.
func TestMinutesDebatesBuildAndLenient(t *testing.T) {
	e := goldenEngine(t, nil)
	r := &Recording{
		ID:        recID,
		StartedAt: time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone),
		Duration:  1800,
		Title:     "Export review",
		Calendar:  &recordingwire.RecordingCalendar{Attendees: []string{"Ada", "Bo"}},
	}
	read := []minutesLine{
		{N: 1, At: 10, Text: "Ada says keep cap per item"},
		{N: 2, At: 20, Text: "Bo says keep cap per recording"},
		{N: 3, At: 30, Text: "Cy suggests a third option"},
		{N: 4, At: 40, Text: "Fourth option mentioned"},
		{N: 5, At: 50, Text: "Fifth option mentioned"},
		{N: 6, At: 60, Text: "Decided: keep the 4 MB limit"},
		{N: 7, At: 70, Text: "Decided: count per item for this release"},
		{N: 8, At: 80, Text: "Decided without matching decision item"},
		{Photo: 1, At: 25, Text: "whiteboard notes"},
	}
	runBuild := func(reply string) (*Minutes, error) {
		t.Helper()
		var a minutesAnswer
		if err := json.Unmarshal([]byte(reply), &a); err != nil {
			return nil, err
		}
		return e.eng().buildMinutes(r, a, read, goldenZone, i18n.En)
	}

	// 1. Settled outcome links to the matching decision (index 1 here, not 0), names checked via named(),
	// ** stripped, positions sorted by at, photo citations rejected, >4 positions capped to 4,
	// settled without valid outcome downgraded to open, <2 positions dropped.
	long := strings.Repeat("x", 450)
	reply := `{
		"title": "Export review",
		"topics": [{"title": "Export", "icon": "progress",
			"points": [{"text": "Discussed cap", "line": 1}],
			"decisions": [
				{"text": "Keep the 4 MB limit", "line": 6},
				{"text": "Count per item for this release", "line": 7}
			]}],
		"debates": [
			"not-an-object",
			null,
			{"question": "Too few after photo drop", "status": "open", "positions": [
				{"stance": "From line", "line": 1},
				{"stance": "From photo", "photo": 1, "line": "P1"},
				{"stance": "Out of range", "line": 99}
			]},
			{"question": "**Count** per item or per recording?", "topic": "**Export**", "status": "settled", "positions": [
				{"stance": "Per **recording**", "line": 2, "who": "**Bo**"},
				{"stance": "Per **item**", "line": 1, "who": "**Ada**"},
				{"stance": "Third **stance**", "line": 3, "who": "Ghost"},
				{"stance": "Fourth stance", "line": 4},
				{"stance": "Fifth stance (over cap)", "line": 5}
			], "outcome": {"text": "Per **item** for this release", "line": 7}},
			{"question": "Settled with invalid outcome downgrades to open", "status": "settled", "positions": [
				{"stance": "Option A", "line": 3},
				{"stance": "Option B", "line": 4}
			], "outcome": "not-an-object"},
			{"question": "Settled with unlinked outcome stays settled", "status": "settled", "positions": [
				{"stance": "Option C", "line": 4},
				{"stance": "Option D", "line": 5}
			], "outcome": {"text": "` + long + `", "line": 8}}
		]
	}`
	m, err := runBuild(reply)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(m.Debates) != 3 {
		t.Fatalf("got %d debates, want 3: %+v", len(m.Debates), m.Debates)
	}
	d0 := m.Debates[0]
	if d0.Question != "Count per item or per recording?" || d0.Topic != "Export" || d0.Status != DebateSettled || d0.At != 10 {
		t.Errorf("d0 header: %+v", d0)
	}
	if len(d0.Positions) != minutesDebatePosMax {
		t.Fatalf("d0 positions len %d, want %d", len(d0.Positions), minutesDebatePosMax)
	}
	if d0.Positions[0].Stance != "Per item" || d0.Positions[0].At != 10 || d0.Positions[0].Who != "Ada" ||
		d0.Positions[1].Stance != "Per recording" || d0.Positions[1].At != 20 || d0.Positions[1].Who != "Bo" ||
		d0.Positions[2].Who != "" {
		t.Errorf("d0 positions: %+v", d0.Positions)
	}
	if d0.Outcome == nil || d0.Outcome.Text != "Per item for this release" || d0.Outcome.At != 70 ||
		d0.Outcome.Decision == nil || *d0.Outcome.Decision != 1 {
		t.Errorf("d0 outcome: %+v", d0.Outcome)
	}
	d1 := m.Debates[1]
	if d1.Status != DebateOpen || d1.Outcome != nil || d1.At != 30 {
		t.Errorf("d1 downgraded: %+v", d1)
	}
	d2 := m.Debates[2]
	if d2.Status != DebateSettled || d2.Outcome == nil || d2.Outcome.Decision != nil ||
		utf8.RuneCountInString(d2.Outcome.Text) != minutesTextRunes || !strings.HasSuffix(d2.Outcome.Text, "…") {
		t.Errorf("d2 unlinked+clipped: %+v", d2)
	}

	// 2. Cap at 6 debates, ordered by earliest position At.
	var many []string
	for i := range 8 {
		// Give descending times so sorting by At is exercised before capping.
		l1 := 8 - i
		if l1 < 1 {
			l1 = 1
		}
		l2 := (l1 % 8) + 1
		many = append(many, fmt.Sprintf(
			`{"question":"Debate %d","status":"open","positions":[{"stance":"A%d","line":%d},{"stance":"B%d","line":%d}]}`,
			i, i, l1, i, l2))
	}
	m, err = runBuild(`{"points":[{"text":"Ok","line":1}],"debates":[` + strings.Join(many, ",") + `]}`)
	if err != nil {
		t.Fatalf("cap build: %v", err)
	}
	if len(m.Debates) != minutesDebatesMax {
		t.Fatalf("got %d debates, want %d", len(m.Debates), minutesDebatesMax)
	}
	for i := 1; i < len(m.Debates); i++ {
		if m.Debates[i].At < m.Debates[i-1].At {
			t.Errorf("debates out of order: %+v", m.Debates)
		}
	}

	// 3. Disagreements alone never make an answer usable.
	if _, err := runBuild(`{"debates":[{"question":"Q","status":"open","positions":[{"stance":"A","line":1},{"stance":"B","line":2}]}]}`); !errors.Is(err, errMinutesUnusable) {
		t.Errorf("debates only: %v, want errMinutesUnusable", err)
	}

	// 4. Malformed debates never cause json.Unmarshal or buildMinutes to fail.
	for _, bad := range []string{
		`{"points":[{"text":"Ok","line":1}],"debates":"none"}`,
		`{"points":[{"text":"Ok","line":1}],"debates":42}`,
		`{"points":[{"text":"Ok","line":1}],"debates":{"question":"not-a-list"}}`,
		`{"points":[{"text":"Ok","line":1}],"debates":null}`,
		`{"points":[{"text":"Ok","line":1}],"debates":[{"question":123,"status":true,"positions":"bad","outcome":[1,2]}]}`,
	} {
		m, err := runBuild(bad)
		if err != nil {
			t.Errorf("malformed %s failed: %v", bad, err)
			continue
		}
		if len(m.Debates) != 0 {
			t.Errorf("malformed %s kept debates: %+v", bad, m.Debates)
		}
	}

	// 5. Minutes written without disagreements have no "debates" key in minutes.json.
	e2, m2, _ := writeOne(t, `{"title":"t","points":[{"text":"First","line":1}]}`)
	rawJSON := readFile(t, e2, recID, "minutes.json")
	if strings.Contains(rawJSON, `"debates"`) {
		t.Errorf("minutes.json carries debates key when absent: %s", rawJSON)
	}
	if strings.Contains(m2.Markdown, "Disagreements") {
		t.Errorf("markdown carries Disagreements when absent:\n%s", m2.Markdown)
	}

	// 6. Output cap check: a maximal valid minutes answer (12 topics, 4 conclusions, 6 debates x 4 positions + outcomes,
	// actions and open questions) stays comfortably within the 6000-token output cap (JSONMax(..., 6000, ...)).
	var maxTopics, maxDebates []string
	for i := range 12 {
		maxTopics = append(maxTopics, fmt.Sprintf(
			`{"title":"Topic %02d","group":"Group %d","icon":"progress","points":[{"text":"Point %d about the release schedule and test metrics","line":1,"who":"Ada"},{"text":"Second point %d on rollout readiness","line":2}],"decisions":[{"text":"Decision %d kept for this milestone","line":6}]}`,
			i+1, (i%3)+1, i+1, i+1, i+1))
	}
	for i := range minutesDebatesMax {
		maxDebates = append(maxDebates, fmt.Sprintf(
			`{"question":"Should milestone policy %d count per item or per recording?","topic":"Topic 01","status":"settled","positions":[{"stance":"Per item avoids one large record blocking the rest","line":1,"who":"Ada"},{"stance":"Per recording is simpler to document for users","line":2,"who":"Bo"},{"stance":"Warn before cutting off large records","line":3},{"stance":"Revisit after two weeks of telemetry","line":4}],"outcome":{"text":"Count per item for this release with a warning","line":7}}`,
			i+1))
	}
	maxReply := `{"title":"Quarterly review","where":"","attendees":["Ada","Bo"],"headline":"Milestone ships with per-item export limits and weekly checks",` +
		`"conclusions":[{"text":"Export cap counts **per item**","line":7},{"text":"Keep the **4 MB** limit","line":6},{"text":"Rollout starts **Monday**","line":1},{"text":"Metrics stay under **200 MB**","line":2}],` +
		`"topics":[` + strings.Join(maxTopics, ",") + `],` +
		`"debates":[` + strings.Join(maxDebates, ",") + `],` +
		`"actions":[{"who":"Bo","what":"Add skipped-item warning and regression test","by":"Friday","due_date":"2026-11-13","line":2}],` +
		`"open":[{"text":"Whether Japanese copy needs an extra review pass","line":3}]}`
	if _, err := runBuild(maxReply); err != nil {
		t.Fatalf("maximal reply failed build: %v", err)
	}
	// At ~3–4 ASCII characters per token, 6000 tokens is ~18,000–24,000 characters; require substantial headroom (<12,000 bytes).
	if len(maxReply) >= 12000 {
		t.Errorf("maximal valid answer is %d bytes (too close to 6000-token cap)", len(maxReply))
	}
	t.Logf("maximal valid minutesAnswer with 12 topics and 6x4 debates: %d bytes (~%d tokens at 3 chars/token, cap 6000)",
		len(maxReply), (len(maxReply)+2)/3)
}

// TestMinutesDebatesLongMeeting verifies the four long-meeting changes through a forced two-part path:
// part notes carry debates under their own quota, notedLines collects debate position and outcome lines,
// out-of-chunk lines are dropped, and a cross-part disagreement survives final verification.
func TestMinutesDebatesLongMeeting(t *testing.T) {
	// Unit-check notesForMerge (separate quota from points/decisions) and notedLines.
	chunk := []minutesLine{{N: 1, At: 0}, {N: 2, At: 10}, {N: 3, At: 20}, {N: 4, At: 30}}
	var partAns minutesAnswer
	if err := json.Unmarshal([]byte(`{
		"points": [{"text":"p1","line":1},{"text":"p2","line":2},{"text":"p3","line":3}],
		"debates": [
			{"question":"Debate **one**","topic":"**Backup**","status":"open","positions":[
				{"stance":"Use zip","line":1,"who":"**Ada**"},
				{"stance":"Out of chunk","line":99}
			]},
			{"question":"Debate two","status":"settled","positions":[
				{"stance":"7 days","line":2},{"stance":"30 days","line":3}
			],"outcome":{"text":"Keep 7 days","line":4}},
			{"question":"Debate three","status":"open","positions":[
				{"stance":"On","line":3},{"stance":"Off","line":4}
			]}
		]
	}`), &partAns); err != nil {
		t.Fatal(err)
	}
	// Limit 2 on points/decisions; debates have their own separate quota (not capped by points limit 2).
	part := notesForMerge(1, partAns, chunk, time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone), goldenZone, 2)
	if len(part.Points) != 2 {
		t.Fatalf("points capped to %d, want 2", len(part.Points))
	}
	if len(part.Debates) != 3 {
		t.Fatalf("debates got %d, want 3 (separate quota from points limit 2)", len(part.Debates))
	}
	if part.Debates[0].Question != "Debate one" || part.Debates[0].Topic != "Backup" ||
		len(part.Debates[0].Positions) != 1 || part.Debates[0].Positions[0].Line != 1 || part.Debates[0].Positions[0].Who != "Ada" {
		t.Errorf("part debate 0: %+v", part.Debates[0])
	}
	if !part.Debates[1].Settled || part.Debates[1].OutcomeLine != 4 || part.Debates[1].OutcomeText != "Keep 7 days" {
		t.Errorf("part debate 1: %+v", part.Debates[1])
	}
	allLines := []minutesLine{
		{N: 1, At: 0, Text: "l1"}, {N: 2, At: 10, Text: "l2"}, {N: 3, At: 20, Text: "l3"}, {N: 4, At: 30, Text: "l4"},
	}
	// Put ONLY debate lines in a part (no points/decisions) to prove notedLines collects debate positions & outcome.
	debateOnlyPart := prompts.KBMinutesPart{Part: 1, Debates: part.Debates[:2]}
	kept := notedLines(allLines, []prompts.KBMinutesPart{debateOnlyPart})
	var keptNums []int
	for _, l := range kept {
		keptNums = append(keptNums, l.N)
	}
	if !slices.Equal(keptNums, []int{1, 2, 3, 4}) {
		t.Fatalf("notedLines from debates only = %v, want [1 2 3 4]", keptNums)
	}

	// End-to-end forced two-part meeting: side A in part 1 (line 2), side B in part 2 (line 6), settled on line 7.
	var calls [][2]string
	model := &funcModel{f: func(n int, system, user string) (string, error) {
		calls = append(calls, [2]string{system, user})
		switch n {
		case 1: // part 1 (lines 1..3)
			return `{"title":"Architecture review","points":[{"text":"Reviewed backup format","line":1}],
				"debates":[{"question":"Zip or plain folders for backups?","topic":"Backups","status":"open",
					"positions":[{"stance":"Use zip archives to save disk","line":2,"who":"Ada"},{"stance":"Bogus later line","line":6}]}]}`, nil
		case 2: // part 2 (lines 4..7)
			return `{"title":"Architecture review","points":[{"text":"Metrics stay under 200 MB","line":5}],
				"decisions":[{"text":"Use zip archives for backups","line":7}],
				"debates":[{"question":"Zip or plain folders for backups?","topic":"Backups","status":"settled",
					"positions":[{"stance":"Use plain folders for incremental sync","line":6,"who":"Bo"}],
					"outcome":{"text":"Use zip archives for now","line":7}}]}`, nil
		case 3: // merging call
			return `{"title":"Architecture review","headline":"Backups use zip archives",
				"conclusions":[{"text":"Backups use **zip archives**","line":7}],
				"topics":[{"title":"Backups","icon":"tooling",
					"points":[{"text":"Reviewed backup format","line":1},{"text":"Metrics stay under **200 MB**","line":5}],
					"decisions":[{"text":"Use **zip archives** for backups","line":7}]}],
				"debates":[{"question":"Should backups use zip archives or plain folders?","topic":"Backups","status":"settled",
					"positions":[
						{"stance":"Use zip archives to save disk","line":2,"who":"Ada"},
						{"stance":"Use plain folders for incremental sync","line":6,"who":"Bo"},
						{"stance":"Uncited filler line dropped","line":4}
					],
					"outcome":{"text":"Use zip archives for now","line":7}}]}`, nil
		default:
			return "", fmt.Errorf("unexpected call %d", n)
		}
	}}
	e := goldenEngine(t, model)
	e.minutesCall, e.minutesPart = 270, 270
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	u := meetingUpload(1, start,
		"First let us review the backup storage format today.",
		"Ada proposes zip archives to save disk space.",
		"Next we check staging search latency numbers.",
		"Line four is an uncited filler transition note.",
		"Metrics pipeline stays under 200 MB all week.",
		"Bo argues for plain folders so incremental sync is easy.",
		"We decide to use zip archives for backups this release.",
	)
	u.Language = "en-US"
	u.Calendar = &recordingwire.RecordingCalendar{Title: "Architecture review", Attendees: []string{"Ada", "Bo"}}
	putRecording(t, e, recID, u, start.Add(time.Hour))

	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	if len(calls) != 3 {
		t.Fatalf("got %d model calls, want 3 (2 parts + 1 merge)", len(calls))
	}
	mergeUser := calls[2][1]
	if !strings.Contains(mergeUser, "Use zip archives to save disk") ||
		!strings.Contains(mergeUser, "Use plain folders for incremental sync") ||
		strings.Contains(mergeUser, "Bogus later line") {
		t.Errorf("merge input missing part debates or kept out-of-chunk line:\n%s", mergeUser)
	}
	m, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Debates) != 1 {
		t.Fatalf("merged debates = %+v, want 1", m.Debates)
	}
	d := m.Debates[0]
	if d.Status != DebateSettled || d.At != 10 || len(d.Positions) != 2 ||
		d.Positions[0].At != 10 || d.Positions[0].Who != "Ada" ||
		d.Positions[1].At != 50 || d.Positions[1].Who != "Bo" ||
		d.Outcome == nil || d.Outcome.At != 60 || d.Outcome.Decision == nil || *d.Outcome.Decision != 0 {
		t.Errorf("cross-part debate: %+v", d)
	}
	if !strings.Contains(m.Markdown, "## Disagreements\n") || !strings.Contains(m.Markdown, "Use zip archives to save disk (Ada)") {
		t.Errorf("merged markdown lacks Disagreements:\n%s", m.Markdown)
	}
}

// TestMinutesDebatesRemadeTrimAndPhotoDelete verifies older recordings:
//  1. Minutes without debates -> confirm to-do -> AskMinutes -> WriteMinutes with debate:
//     new version has debates + Disagreements section (searchable via minutes.md) and to-do carries over.
//  2. Trimmed recording still answers ErrTranscriptExpired on AskMinutes.
//  3. Photo delete (photoUncite) removes photo-citing decisions/items, remaps outcome.decision, and keeps debates intact.
func TestMinutesDebatesRemadeTrimAndPhotoDelete(t *testing.T) {
	// 1. Older recording remade with debates; to-do carries over.
	withDebate := false
	model := &funcModel{f: func(int, string, string) (string, error) {
		if !withDebate {
			return `{"title":"Export sync","points":[{"text":"Reviewed export cap","line":1}],
				"actions":[{"who":"Bo","what":"Add regression test","line":4}]}`, nil
		}
		return `{"title":"Export sync","points":[{"text":"Reviewed export cap","line":1}],
			"decisions":[{"text":"Count cap per item","line":3}],
			"actions":[{"who":"Bo","what":"Add regression test","line":4}],
			"debates":[{"question":"Count cap per item or per recording?","status":"settled",
				"positions":[{"stance":"Per item avoids one large file blocking export","line":1,"who":"Ada"},
				             {"stance":"Per recording is easier to explain","line":2,"who":"Bo"}],
				"outcome":{"text":"Count per item","line":3}}]}`, nil
	}}
	e := goldenEngine(t, model)
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)
	u := meetingUpload(1, start,
		"Ada wants the export cap per item.",
		"Bo prefers the export cap per recording.",
		"Decided to count the cap per item.",
		"Bo will add the regression test.",
	)
	u.Language = "en-US"
	u.Calendar = &recordingwire.RecordingCalendar{Title: "Export sync", Attendees: []string{"Ada", "Bo"}}
	putRecording(t, e, recID, u, start.Add(time.Hour))
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("first write: %d", n)
	}
	m1, err := e.Store.ReadMinutes(recID)
	if err != nil || len(m1.Debates) != 0 || len(m1.Actions) != 1 {
		t.Fatalf("v1: %+v %v", m1, err)
	}
	if _, err := e.SetMinutesTodos(recID, m1.Version, map[string]string{m1.Actions[0].ID: "todo_bo"}, nil); err != nil {
		t.Fatal(err)
	}

	withDebate = true
	if err := e.AskMinutes(recID); err != nil {
		t.Fatalf("AskMinutes: %v", err)
	}
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("remake write: %d", n)
	}
	m2, err := e.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Version != m1.Version+1 || len(m2.Debates) != 1 || m2.Debates[0].Outcome == nil ||
		m2.Debates[0].Outcome.Decision == nil || *m2.Debates[0].Outcome.Decision != 0 {
		t.Fatalf("v2 debates: v%d %+v", m2.Version, m2.Debates)
	}
	if len(m2.Actions) != 1 || m2.Actions[0].TodoID != "todo_bo" {
		t.Fatalf("to-do did not carry over: %+v", m2.Actions)
	}
	if !strings.Contains(m2.Markdown, "## Disagreements\n") {
		t.Errorf("v2 markdown lacks Disagreements:\n%s", m2.Markdown)
	}
	if ms := e.Matches(KindRecording, recID, "easier to explain", 40); len(ms) != 1 || !strings.HasSuffix(ms[0].Path, "/minutes.md") {
		t.Errorf("disagreement not searchable in minutes.md: %+v", ms)
	}

	// 2. Trimmed recording still refuses AskMinutes with ErrTranscriptExpired.
	eTrim := goldenEngine(t, trimModel{t})
	eTrim.RecordingDaysFunc = func() int { return 14 }
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	oldStart := now.AddDate(0, 0, -20)
	putRecording(t, eTrim, recID, meetingUpload(1, oldStart, "old transcript line"), oldStart)
	putMeetingMinutes(t, eTrim, recID, Minutes{Version: 1, Revision: 1, Title: "Old sync", When: "2026-10-31 12:00–12:01",
		Points: []MinutesPoint{{Text: "Old point", At: 0}}, Markdown: "# Old sync\n\n## Key points\n\n- [12:00:00] Old point"})
	changeRecording(t, eTrim, recID, func(r *Recording) { r.MinutesRev, r.MinutesVersion = 1, 1 })
	eTrim.PruneRecordings(now)
	if err := eTrim.AskMinutes(recID); !errors.Is(err, ErrTranscriptExpired) {
		t.Fatalf("trimmed AskMinutes = %v, want ErrTranscriptExpired", err)
	}

	// 3. Photo delete (photoUncite) keeps debates and Disagreements section intact and remaps outcome.decision.
	photoModel := &funcModel{f: func(int, string, string) (string, error) {
		return `{"title":"Photo sync","headline":"Export cap decided per item",
			"conclusions":[{"text":"From **whiteboard**","photo":1},{"text":"Cap **per item**","line":3}],
			"topics":[
				{"title":"Whiteboard","icon":"design","points":[{"text":"Board photocanary","photo":1}],"decisions":[{"text":"Follow photocanary","photo":1}]},
				{"title":"Export","icon":"progress","points":[{"text":"Export cap discussed","line":1}],"decisions":[{"text":"Count cap per item","line":3}]}
			],
			"debates":[{"question":"Per item or per recording?","topic":"Export","status":"settled",
				"positions":[{"stance":"Per item","line":1,"who":"Ada"},{"stance":"Per recording","line":2,"who":"Bo"}],
				"outcome":{"text":"Count per item","line":3}}]}`, nil
	}}
	ePhoto := goldenEngine(t, photoModel)
	uPhoto := meetingUpload(1, start,
		"Ada proposes counting per item.",
		"Bo prefers counting per recording.",
		"Decided to count per item.",
	)
	uPhoto.Language = "en-US"
	uPhoto.Calendar = &recordingwire.RecordingCalendar{Title: "Photo sync", Attendees: []string{"Ada", "Bo"}}
	putRecording(t, ePhoto, recID, uPhoto, start.Add(time.Hour))
	addPhoto(t, ePhoto, recID, photoUpload(1, 0, "Whiteboard", "photocanary"), false)
	if n := ePhoto.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("photo write: %d", n)
	}
	before, err := ePhoto.Store.ReadMinutes(recID)
	if err != nil || len(before.Decisions) != 2 || len(before.Debates) != 1 ||
		before.Debates[0].Outcome == nil || before.Debates[0].Outcome.Decision == nil || *before.Debates[0].Outcome.Decision != 1 {
		t.Fatalf("before photo delete: decisions=%+v debates=%+v err=%v", before.Decisions, before.Debates, err)
	}
	if ok, err := ePhoto.PhotoDelete(recID, 1); !ok || err != nil {
		t.Fatalf("PhotoDelete: %v %v", ok, err)
	}
	after, err := ePhoto.Store.ReadMinutes(recID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Decisions) != 1 || after.Decisions[0].Text != "Count cap per item" {
		t.Fatalf("after photo delete decisions: %+v", after.Decisions)
	}
	if len(after.Debates) != 1 || after.Debates[0].Outcome == nil || after.Debates[0].Outcome.Decision == nil ||
		*after.Debates[0].Outcome.Decision != 0 {
		t.Fatalf("after photo delete debates not preserved/remapped: %+v", after.Debates)
	}
	if strings.Contains(after.Markdown, "photocanary") || !strings.Contains(after.Markdown, "## Disagreements\n") ||
		!strings.Contains(after.Markdown, "Per item or per recording? (settled)") {
		t.Errorf("after photo delete markdown:\n%s", after.Markdown)
	}

	// Direct dropPhotos check when a linked decision itself is dropped: Outcome.Decision becomes nil, debate stays.
	idx0, idx1 := 0, 1
	direct := &Minutes{
		Decisions: []MinutesItem{{Text: "Board decision", At: 5, Photo: 1}, {Text: "Spoken decision", At: 20}},
		Debates: []MinutesDebate{
			{Question: "Q1", Status: DebateSettled, At: 1,
				Positions: []MinutesDebatePosition{{Stance: "A", At: 1}, {Stance: "B", At: 2}},
				Outcome:   &MinutesDebateOutcome{Text: "Board", At: 5, Decision: &idx0}},
			{Question: "Q2", Status: DebateSettled, At: 10,
				Positions: []MinutesDebatePosition{{Stance: "C", At: 10}, {Stance: "D", At: 15}},
				Outcome:   &MinutesDebateOutcome{Text: "Spoken", At: 20, Decision: &idx1}},
		},
	}
	dropPhotos(direct, func(n int) bool { return n == 1 })
	if len(direct.Debates) != 2 || direct.Debates[0].Outcome.Decision != nil ||
		direct.Debates[1].Outcome.Decision == nil || *direct.Debates[1].Outcome.Decision != 0 {
		t.Errorf("direct dropPhotos remap: %+v", direct.Debates)
	}
}
