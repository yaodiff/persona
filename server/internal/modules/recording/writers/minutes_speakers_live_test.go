package writers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// speakerLiveScenario is a synthetic numbered-transcript behaviour-check case for #17674 ("The minutes").
type speakerLiveScenario struct {
	name      string
	title     string
	attendees []string
	segments  []RecordingSegment
	turns     []RawSpeakerTurn
	passNum   int
	passDen   int
	check     func(m *Minutes) string
}

// leakedSpeakerNumberRe catches anonymous speaker labels (S1, S2, Speaker 1, 发言人 1, 話者 1) leaking into text fields.
var leakedSpeakerNumberRe = regexp.MustCompile(`(?i)(〔\s*s[0-9]+\s*\??\s*〕|\bS[1-9][0-9]*\b|\bSpeaker\s+[1-9][0-9]*\b|发言人\s*[1-9][0-9]*|話者\s*[1-9][0-9]*)`)

// minutesTextHasSpeakerNumber returns the first offending text field if any text field in m contains a speaker number.
func minutesTextHasSpeakerNumber(m *Minutes) string {
	check := func(field, val string) string {
		if leakedSpeakerNumberRe.MatchString(val) {
			return fmt.Sprintf("%s contains speaker number: %q", field, val)
		}
		return ""
	}
	if s := check("title", m.Title); s != "" {
		return s
	}
	if m.Overview != nil {
		if s := check("overview.headline", m.Overview.Headline); s != "" {
			return s
		}
		for i, c := range m.Overview.Conclusions {
			if s := check(fmt.Sprintf("overview.conclusions[%d]", i), c.Text); s != "" {
				return s
			}
		}
		for gi, g := range m.Overview.Groups {
			if s := check(fmt.Sprintf("overview.groups[%d].title", gi), g.Title); s != "" {
				return s
			}
			for ti, tp := range g.Topics {
				if s := check(fmt.Sprintf("overview.groups[%d].topics[%d].title", gi, ti), tp.Title); s != "" {
					return s
				}
				for bi, b := range tp.Bullets {
					if s := check(fmt.Sprintf("overview.groups[%d].topics[%d].bullets[%d]", gi, ti, bi), b.Text); s != "" {
						return s
					}
				}
			}
		}
	}
	for i, p := range m.Points {
		if s := check(fmt.Sprintf("points[%d]", i), p.Text); s != "" {
			return s
		}
	}
	for i, d := range m.Decisions {
		if s := check(fmt.Sprintf("decisions[%d]", i), d.Text); s != "" {
			return s
		}
	}
	for i, a := range m.Actions {
		if s := check(fmt.Sprintf("actions[%d]", i), a.What); s != "" {
			return s
		}
	}
	for i, o := range m.Open {
		if s := check(fmt.Sprintf("open[%d]", i), o.Text); s != "" {
			return s
		}
	}
	for i, d := range m.Debates {
		if s := check(fmt.Sprintf("debates[%d].question", i), d.Question); s != "" {
			return s
		}
		for j, pos := range d.Positions {
			if s := check(fmt.Sprintf("debates[%d].positions[%d].stance", i, j), pos.Stance); s != "" {
				return s
			}
		}
		if d.Outcome != nil {
			if s := check(fmt.Sprintf("debates[%d].outcome", i), d.Outcome.Text); s != "" {
				return s
			}
		}
	}
	return ""
}

var speakerLiveScenarios = []speakerLiveScenario{
	{
		name:      "restated_holder",
		title:     "Log retention review",
		attendees: []string{"Ada", "Bo", "Cy"},
		segments: []RecordingSegment{
			{Start: 0, End: 25, Text: "Today we are reviewing how long diagnostic logs stay on disk."},
			{Start: 26, End: 55, Text: "I think we should keep diagnostic logs for only 7 days so small disks never fill up."},
			{Start: 56, End: 85, Text: "I disagree, we need 30 days of logs so engineers can trace monthly billing issues."},
			{Start: 86, End: 115, Text: "To restate both views before we wrap up: the first proposal is a 7-day cap to protect disk space, while the other is 30 days for monthly billing."},
			{Start: 116, End: 140, Text: "We are still split, so let us keep this open until we measure weekly log size next Tuesday."},
		},
		turns: []RawSpeakerTurn{
			{Start: 0, End: 25, Cluster: "c1"},
			{Start: 26, End: 55, Cluster: "c1"},
			{Start: 56, End: 85, Cluster: "c2"},
			{Start: 86, End: 115, Cluster: "c3"},
			{Start: 116, End: 140, Cluster: "c3"},
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes) string {
			if miss := minutesTextHasSpeakerNumber(m); miss != "" {
				return miss
			}
			if len(m.Debates) != 1 {
				return fmt.Sprintf("got %d debates, want 1", len(m.Debates))
			}
			d := m.Debates[0]
			if len(d.Positions) != 2 {
				return fmt.Sprintf("got %d positions, want 2", len(d.Positions))
			}
			var gotS1, gotS2 bool
			for _, p := range d.Positions {
				if p.At == 86 {
					return "cited restatement line (86) instead of original holder line"
				}
				if p.SaidBy == "s1" && p.Speaker == "s1" && !p.SpeakerUnsure {
					gotS1 = true
				}
				if p.SaidBy == "s2" && p.Speaker == "s2" && !p.SpeakerUnsure {
					gotS2 = true
				}
			}
			if !gotS1 || !gotS2 {
				return fmt.Sprintf("expected sure s1 and s2 holders, got %+v", d.Positions)
			}
			return ""
		},
	},
	{
		name:      "ordinary_no_leaked_numbers",
		title:     "Onboarding guide check-in",
		attendees: []string{"Ada", "Bo", "Cy"},
		segments: []RecordingSegment{
			{Start: 0, End: 25, Text: "Let us do a quick status check on the onboarding guide and icon cleanup."},
			{Start: 26, End: 55, Text: "I finished the first draft of the onboarding guide yesterday and added two screenshots."},
			{Start: 56, End: 85, Text: "Looks good; adding a link to the troubleshooting page at the bottom will help new users."},
			{Start: 86, End: 115, Text: "Great idea, I will add that troubleshooting link before Friday's build."},
			{Start: 116, End: 140, Text: "Everyone is aligned, so we can ship the guide update with Friday's release."},
		},
		turns: []RawSpeakerTurn{
			{Start: 0, End: 25, Cluster: "c1"},
			{Start: 26, End: 55, Cluster: "c1"},
			{Start: 56, End: 85, Cluster: "c2"},
			{Start: 86, End: 115, Cluster: "c1"},
			{Start: 116, End: 140, Cluster: "c3"},
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes) string {
			if miss := minutesTextHasSpeakerNumber(m); miss != "" {
				return miss
			}
			if len(m.Debates) != 0 {
				return fmt.Sprintf("ordinary meeting got %d debates, want 0", len(m.Debates))
			}
			return ""
		},
	},
}

// TestMinutesSpeakersLiveScenariosAreWellFormed runs offline without a model to verify every live scenario's structure
// and that each checker accepts valid output and rejects leaked speaker numbers or wrong holders.
func TestMinutesSpeakersLiveScenariosAreWellFormed(t *testing.T) {
	if len(speakerLiveScenarios) != 2 {
		t.Fatalf("got %d scenarios, want 2", len(speakerLiveScenarios))
	}
	goodRestated := &Minutes{
		Title: "Log retention review",
		Debates: []MinutesDebate{{
			Question: "Keep logs 7 or 30 days?", Status: DebateOpen, At: 26,
			Positions: []MinutesDebatePosition{
				{Stance: "Keep logs for 7 days", At: 26, SaidBy: "s1", Speaker: "s1"},
				{Stance: "Keep logs for 30 days", At: 56, SaidBy: "s2", Speaker: "s2"},
			},
		}},
	}
	badRestated := &Minutes{
		Title: "Log retention review",
		Debates: []MinutesDebate{{
			Question: "Keep logs 7 or 30 days?", Status: DebateOpen, At: 56,
			Positions: []MinutesDebatePosition{
				{Stance: "Keep logs for 7 days", At: 86, SaidBy: "s3", Speaker: "s3"},
				{Stance: "Keep logs for 30 days", At: 56, SaidBy: "s2", Speaker: "s2"},
			},
		}},
	}
	if miss := speakerLiveScenarios[0].check(goodRestated); miss != "" {
		t.Errorf("restated valid rejected: %s", miss)
	}
	if miss := speakerLiveScenarios[0].check(badRestated); miss == "" {
		t.Errorf("restated invalid accepted")
	}

	goodOrdinary := &Minutes{Title: "Onboarding guide check-in", Points: []MinutesPoint{{Text: "Draft finished", At: 26}}}
	badOrdinary := &Minutes{Title: "Onboarding guide check-in", Points: []MinutesPoint{{Text: "S1 finished the draft", At: 26}}}
	if miss := speakerLiveScenarios[1].check(goodOrdinary); miss != "" {
		t.Errorf("ordinary valid rejected: %s", miss)
	}
	if miss := speakerLiveScenarios[1].check(badOrdinary); miss == "" {
		t.Errorf("ordinary leaked number accepted")
	}
}

// TestMinutesSpeakersLive runs the two synthetic numbered-transcript cases against a real model (PERSONA_LIVE_AI),
// PERSONA_LIVE_RUNS times each (default 5). Skips without PERSONA_LIVE_AI; never runs in CI.
func TestMinutesSpeakersLive(t *testing.T) {
	raw := os.Getenv("PERSONA_LIVE_AI")
	if raw == "" {
		t.Skip("PERSONA_LIVE_AI not set")
	}
	var cfg aiwire.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	runs := 5
	if s := strings.TrimSpace(os.Getenv("PERSONA_LIVE_RUNS")); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			t.Fatalf("PERSONA_LIVE_RUNS=%q: %v", s, err)
		}
		runs = n
	}
	baseModel := kb.NewModel(cfg)
	ctx := context.Background()
	start := time.Date(2026, 11, 20, 14, 0, 0, 0, goldenZone)
	now := start.Add(time.Hour)

	var rows, summary []string
	for _, sc := range speakerLiveScenarios {
		passed, truncTotal := 0, 0
		for run := 1; run <= runs; run++ {
			tracker := &debatesLiveTracker{Model: baseModel}
			e, _ := hubEngine(t, tracker)
			e.Logf = func(f string, a ...any) { t.Logf("[%s #%d] "+f, append([]any{sc.name, run}, a...)...) }
			setupHubFinalMeeting(t, e, recID, start, now, "en-US", sc.segments,
				&recordingwire.RecordingCalendar{Title: sc.title, Attendees: sc.attendees})
			if _, err := e.ImportSpeakerTurns(recID, SpeakerTurnsImportRequest{
				Run:     "live-run-1",
				Engine:  "live-fixture 1.0",
				Source:  SpeakerSourceHub,
				Covered: []SpeakerSpan{{0, 145}},
				Turns:   sc.turns,
			}, now); err != nil {
				t.Fatalf("import turns: %v", err)
			}
			wrote := e.WriteMinutes(ctx, 1)
			_, trunc := tracker.stats()
			truncTotal += trunc

			var miss string
			if trunc > 0 {
				miss = fmt.Sprintf("truncated (%d calls)", trunc)
			} else if wrote != 1 {
				miss = fmt.Sprintf("wrote %d minutes", wrote)
			} else if m, err := e.Store.ReadMinutes(recID); err != nil {
				miss = fmt.Sprintf("read minutes: %v", err)
			} else {
				miss = sc.check(m)
			}
			mark := "ok  "
			if miss == "" {
				passed++
			} else {
				mark = "MISS"
			}
			rows = append(rows, fmt.Sprintf("%s %-26s run=%d/%d trunc=%d %s", mark, sc.name, run, runs, trunc, miss))
		}
		need := requiredPasses(runs, sc.passNum, sc.passDen)
		status := "PASS"
		if passed < need || truncTotal > 0 {
			status = "FAIL"
			t.Errorf("%s: passed %d/%d (need %d/%d), truncated=%d", sc.name, passed, runs, need, runs, truncTotal)
		}
		summary = append(summary, fmt.Sprintf("%-4s %-26s %d/%d (threshold %d/%d) trunc=%d",
			status, sc.name, passed, runs, need, runs, truncTotal))
	}
	t.Logf("model %s\n%s\n\nSummary:\n%s", cfg.Model, strings.Join(rows, "\n"), strings.Join(summary, "\n"))
}
