package writers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// debateScenario is one synthetic, public behaviour-check case for disagreements in minutes (#16728).
type debateScenario struct {
	name          string
	title         string
	attendees     []string
	lines         []string
	partRunes     int // >0 forces chunked minutesCall/minutesPart for the split case
	passNum       int // numerator of required pass rate (e.g. 4 for 4/5)
	passDen       int // denominator of required pass rate (5)
	maxDebatesAny int // >0: fail if any run exceeds this many debates (ordinary: 1)
	check         func(m *Minutes, merges int) string
}

// hasPositionAt reports whether d has a position citing sec.
func hasPositionAt(d MinutesDebate, sec float64) bool {
	return slices.ContainsFunc(d.Positions, func(p MinutesDebatePosition) bool { return p.At == sec })
}

// debateScenarios are the five synthetic, public cases specified in #16728 ("Behaviour check").
// meetingUpload places line i (1-based) at (i-1)*10 seconds.
var debateScenarios = []debateScenario{
	{
		name:      "clear",
		title:     "Release and digest sync",
		attendees: []string{"Ada", "Bo", "Cy"},
		lines: []string{
			"Good morning everyone, let's review the release checklist and the weekly digest setting.",
			"Cy reports that the release checklist is complete and all export tests are green.",
			"Cy will publish the release notes by Friday afternoon.",
			"Next question: should the weekly digest email default to on or off for new accounts?",
			"Ada argues it should default to on, because otherwise new users never discover the weekly recap.",
			"Bo disagrees and says it must default to off, because sending unsolicited email without opt-in erodes trust.",
			"We are still split on the default setting, so let's leave that undecided and revisit it next Tuesday.",
			"That wraps up today's sync. Thanks everyone.",
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes, _ int) string {
			if len(m.Debates) != 1 {
				return fmt.Sprintf("got %d debates, want 1", len(m.Debates))
			}
			d := m.Debates[0]
			if d.Status != DebateOpen {
				return fmt.Sprintf("status=%q, want open", d.Status)
			}
			if d.Outcome != nil {
				return "open debate has outcome"
			}
			if len(d.Positions) != 2 {
				return fmt.Sprintf("got %d positions, want 2", len(d.Positions))
			}
			if !hasPositionAt(d, 40) || !hasPositionAt(d, 50) {
				return fmt.Sprintf("position times %v/%v, want 40 and 50", d.Positions[0].At, d.Positions[1].At)
			}
			return ""
		},
	},
	{
		name:      "settled",
		title:     "Export cap review",
		attendees: []string{"Ada", "Bo", "Cy"},
		lines: []string{
			"Let's review the export size cap and the test schedule today.",
			"Export currently drops entries over 4 MB, and we need to decide whether the cap counts per item or per recording.",
			"Ada proposes counting the cap per item so one oversized file does not fail the entire export.",
			"Bo disagrees and prefers counting per recording because one total limit is much easier to explain to users.",
			"Let's pause on that and check the load-test numbers first.",
			"Cy confirms the load test reached 40K QPS with no errors.",
			"Coming back to the export cap: we agree and decide to count the cap per item for this release, with a warning when an item is skipped.",
			"Bo will add the skipped-item warning and regression test by Friday.",
			"All set, thanks everyone.",
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes, _ int) string {
			if len(m.Debates) != 1 {
				return fmt.Sprintf("got %d debates, want 1", len(m.Debates))
			}
			d := m.Debates[0]
			if d.Status != DebateSettled {
				return fmt.Sprintf("status=%q, want settled", d.Status)
			}
			if !hasPositionAt(d, 20) || !hasPositionAt(d, 30) {
				return fmt.Sprintf("positions %+v do not cite both holder lines (20, 30)", d.Positions)
			}
			if d.Outcome == nil {
				return "settled debate has no outcome"
			}
			if d.Outcome.Decision == nil {
				return fmt.Sprintf("outcome at %.0f not linked to a kept decision", d.Outcome.At)
			}
			idx := *d.Outcome.Decision
			if idx < 0 || idx >= len(m.Decisions) {
				return fmt.Sprintf("outcome decision index %d out of range (%d)", idx, len(m.Decisions))
			}
			if m.Decisions[idx].At != 60 || d.Outcome.At != 60 {
				return fmt.Sprintf("outcome at %.0f / decision[%d] at %.0f, want 60", d.Outcome.At, idx, m.Decisions[idx].At)
			}
			return ""
		},
	},
	{
		name:      "restated",
		title:     "Log retention review",
		attendees: []string{"Ada", "Bo", "Cy"},
		lines: []string{
			"Today we need to talk about log retention and the backup dashboard.",
			"The backup dashboard moved to staging this morning and passed smoke checks.",
			"Next topic: how long should we keep diagnostic logs on disk?",
			"Ada argues we should keep logs for only 7 days so disk usage stays bounded on small machines.",
			"Bo disagrees and says we need 30 days of logs so engineers can investigate monthly billing issues.",
			"Cy summarizes the two views: to restate what Ada said, Ada wants a 7-day cap to protect disk space, while Bo wants 30 days.",
			"Neither side is convinced yet, so we will keep this open until we measure disk usage next week.",
			"Cy will measure average weekly log size by Thursday.",
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes, _ int) string {
			if len(m.Debates) != 1 {
				return fmt.Sprintf("got %d debates, want 1", len(m.Debates))
			}
			d := m.Debates[0]
			if hasPositionAt(d, 50) {
				return "a position cited the restatement line (50) instead of the holder's line"
			}
			if !hasPositionAt(d, 30) || !hasPositionAt(d, 40) {
				return fmt.Sprintf("positions %+v do not cite both holder lines (30, 40)", d.Positions)
			}
			return ""
		},
	},
	{
		name:          "ordinary",
		title:         "Onboarding guide check-in",
		attendees:     []string{"Ada", "Bo", "Cy"},
		maxDebatesAny: 1,
		lines: []string{
			"Let's do a quick status check on the onboarding guide and icon cleanup.",
			"Ada finished the first draft of the onboarding guide yesterday.",
			"Bo asks whether the guide already includes the keyboard shortcuts table.",
			"Ada says yes, it is in section three, and she also added two screenshots.",
			"Cy suggests adding a link to the troubleshooting page at the bottom.",
			"Ada agrees that is a great idea and will add the link before Friday.",
			"Bo finished exporting the updated toolbar icons and merged them into the asset folder.",
			"Everyone is aligned, so we can ship the guide update with Friday's build.",
		},
		passNum: 4,
		passDen: 5,
		check: func(m *Minutes, _ int) string {
			if len(m.Debates) != 0 {
				return fmt.Sprintf("got %d debates, want 0", len(m.Debates))
			}
			return ""
		},
	},
	{
		name:      "split",
		title:     "Quarterly architecture review",
		attendees: []string{"Ada", "Bo", "Cy"},
		partRunes: 700,
		lines: []string{
			"Welcome to the quarterly architecture review. First let's talk about the backup storage format.",
			"Ada proposes storing backups as compressed zip archives to save disk space and make downloads faster.",
			"Before deciding on backups, let's go through the other agenda updates.",
			"Cy reports that the search index migration finished on staging with zero downtime and query latency dropped to 18 ms.",
			"Bo says the metrics pipeline now exports hourly summaries and memory usage stayed under 200 MB all week.",
			"Cy will archive the old migration scripts by Thursday.",
			"Moving on to notifications: the retry queue cleared its backlog after we tuned the batch size to 50.",
			"Ada notes that the help center articles for export errors are now translated into Japanese.",
			"Returning to the backup storage format from earlier: Bo strongly disagrees with using zip archives and argues for plain uncompressed folders so incremental sync and file inspection stay simple.",
			"Ada and Bo still disagree on zip archives versus plain folders for backups, so this stays unresolved for next week.",
			"Bo will benchmark incremental sync on both formats by Monday.",
		},
		passNum: 3,
		passDen: 5,
		check: func(m *Minutes, merges int) string {
			if merges != 1 {
				return fmt.Sprintf("got %d merging calls, want 1", merges)
			}
			if len(m.Debates) != 1 {
				return fmt.Sprintf("got %d debates, want 1", len(m.Debates))
			}
			d := m.Debates[0]
			if !hasPositionAt(d, 10) || !hasPositionAt(d, 80) {
				return fmt.Sprintf("positions %+v do not cite both part-1 (10) and part-2 (80) lines", d.Positions)
			}
			return ""
		},
	},
}

// debatesLiveTracker wraps a kb.Model to track merging calls and whether any call hit aiwire.ErrTruncated.
type debatesLiveTracker struct {
	kb.Model
	mu        sync.Mutex
	merges    int
	truncated int
}

func (m *debatesLiveTracker) JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error {
	err := m.Model.JSONMax(ctx, system, user, maxTokens, out)
	m.mu.Lock()
	defer m.mu.Unlock()
	if errors.Is(err, aiwire.ErrTruncated) {
		m.truncated++
	}
	if _, ok := out.(*minutesAnswer); ok && err == nil {
		if strings.Contains(system, "已经分段整理过") { // i18n-ignore: the merging prompt's words
			m.merges++
		}
	}
	return err
}

func (m *debatesLiveTracker) stats() (merges, truncated int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.merges, m.truncated
}

func requiredPasses(runs, num, den int) int {
	return (num*runs + den - 1) / den
}

// TestMinutesDebatesScenariosAreWellFormed runs without a model: verifies every scenario has unique names,
// the split scenario actually splits into 2 parts with side A in part 1 and side B in part 2,
// and each scenario's checker accepts a valid fake answer and rejects a wrong fake answer.
func TestMinutesDebatesScenariosAreWellFormed(t *testing.T) {
	wantNames := []string{"clear", "settled", "restated", "ordinary", "split"}
	if len(debateScenarios) != len(wantNames) {
		t.Fatalf("got %d scenarios, want %d", len(debateScenarios), len(wantNames))
	}
	byName := map[string]debateScenario{}
	for i, sc := range debateScenarios {
		if sc.name != wantNames[i] {
			t.Errorf("scenario %d = %q, want %q", i, sc.name, wantNames[i])
		}
		if len(sc.lines) < 5 || sc.check == nil {
			t.Errorf("scenario %s incomplete", sc.name)
		}
		byName[sc.name] = sc
	}

	// Verify split actually produces 2 chunks with line 2 (At 10) in chunk 0 and line 9 (At 80) in chunk 1.
	sp := byName["split"]
	var ml []minutesLine
	for i, text := range sp.lines {
		ml = append(ml, minutesLine{N: i + 1, At: float64(i * 10), Text: text})
	}
	chunks, partial := chunkLines(ml, sp.partRunes, sp.partRunes, minutesMaxChunks)
	if partial || len(chunks) != 2 {
		t.Fatalf("split chunks = %d (partial=%v), want 2", len(chunks), partial)
	}
	inChunk := func(c []minutesLine, n int) bool {
		return slices.ContainsFunc(c, func(l minutesLine) bool { return l.N == n })
	}
	if !inChunk(chunks[0], 2) || inChunk(chunks[0], 9) || inChunk(chunks[1], 2) || !inChunk(chunks[1], 9) {
		t.Fatalf("split lines not separated across parts: chunk0=%v chunk1=%v", chunks[0], chunks[1])
	}

	// Verify each checker passes on a valid Minutes and fails on a broken Minutes.
	idx0 := 0
	for _, tc := range []struct {
		name       string
		good, bad  *Minutes
		goodMerges int
		badMerges  int
	}{
		{
			name: "clear",
			good: &Minutes{Debates: []MinutesDebate{{
				Question: "Digest default?", Status: DebateOpen, At: 40,
				Positions: []MinutesDebatePosition{{Stance: "On", At: 40, Who: "Ada"}, {Stance: "Off", At: 50, Who: "Bo"}},
			}}},
			bad: &Minutes{Debates: []MinutesDebate{{
				Question: "Digest default?", Status: DebateSettled, At: 40,
				Positions: []MinutesDebatePosition{{Stance: "On", At: 40}, {Stance: "Off", At: 50}},
				Outcome:   &MinutesDebateOutcome{Text: "On", At: 60},
			}}},
		},
		{
			name: "settled",
			good: &Minutes{
				Decisions: []MinutesItem{{Text: "Per item", At: 60}},
				Debates: []MinutesDebate{{
					Question: "Export cap?", Status: DebateSettled, At: 20,
					Positions: []MinutesDebatePosition{{Stance: "Per item", At: 20}, {Stance: "Per recording", At: 30}},
					Outcome:   &MinutesDebateOutcome{Text: "Per item", At: 60, Decision: &idx0},
				}},
			},
			bad: &Minutes{
				Decisions: []MinutesItem{{Text: "Per item", At: 60}},
				Debates: []MinutesDebate{{
					Question: "Export cap?", Status: DebateSettled, At: 20,
					Positions: []MinutesDebatePosition{{Stance: "Per item", At: 20}, {Stance: "Per recording", At: 30}},
					Outcome:   &MinutesDebateOutcome{Text: "Per item", At: 60}, // unlinked
				}},
			},
		},
		{
			name: "restated",
			good: &Minutes{Debates: []MinutesDebate{{
				Question: "Log retention?", Status: DebateOpen, At: 30,
				Positions: []MinutesDebatePosition{{Stance: "7 days", At: 30}, {Stance: "30 days", At: 40}},
			}}},
			bad: &Minutes{Debates: []MinutesDebate{{
				Question: "Log retention?", Status: DebateOpen, At: 40,
				Positions: []MinutesDebatePosition{{Stance: "7 days", At: 50}, {Stance: "30 days", At: 40}}, // cited restatement line 50
			}}},
		},
		{
			name: "ordinary",
			good: &Minutes{},
			bad: &Minutes{Debates: []MinutesDebate{{
				Question: "Fake debate", Status: DebateOpen, At: 10,
				Positions: []MinutesDebatePosition{{Stance: "A", At: 10}, {Stance: "B", At: 20}},
			}}},
		},
		{
			name:       "split",
			goodMerges: 1,
			good: &Minutes{Debates: []MinutesDebate{{
				Question: "Backup format?", Status: DebateOpen, At: 10,
				Positions: []MinutesDebatePosition{{Stance: "Zip", At: 10}, {Stance: "Folders", At: 80}},
			}}},
			badMerges: 1,
			bad: &Minutes{Debates: []MinutesDebate{{
				Question: "Backup format?", Status: DebateOpen, At: 80,
				Positions: []MinutesDebatePosition{{Stance: "Zip", At: 80}, {Stance: "Folders", At: 90}},
			}}},
		},
	} {
		sc := byName[tc.name]
		if miss := sc.check(tc.good, tc.goodMerges); miss != "" {
			t.Errorf("%s: valid answer rejected: %s", tc.name, miss)
		}
		if miss := sc.check(tc.bad, tc.badMerges); miss == "" {
			t.Errorf("%s: invalid answer accepted", tc.name)
		}
	}
}

// TestMinutesDebatesLive runs the five synthetic disagreement cases against a real model (PERSONA_LIVE_AI),
// PERSONA_LIVE_RUNS times each (default 5), prints one row per case and run plus a summary table with the model name,
// and fails if any case misses its pass threshold, if ordinary exceeds one debate on any run, or if any call truncates.
func TestMinutesDebatesLive(t *testing.T) {
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
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, goldenZone)

	var rows, summary []string
	for _, sc := range debateScenarios {
		passed, truncTotal, maxDebates := 0, 0, 0
		for run := 1; run <= runs; run++ {
			tracker := &debatesLiveTracker{Model: baseModel}
			e := goldenEngine(t, tracker)
			if sc.partRunes > 0 {
				e.minutesCall, e.minutesPart = sc.partRunes, sc.partRunes
			}
			e.Logf = func(f string, a ...any) { t.Logf("[%s #%d] "+f, append([]any{sc.name, run}, a...)...) }
			u := meetingUpload(1, start, sc.lines...)
			u.Language = "en-US"
			u.Calendar = &recordingwire.RecordingCalendar{Title: sc.title, Attendees: sc.attendees}
			putRecording(t, e, recID, u, start.Add(time.Hour))
			wrote := e.WriteMinutes(ctx, 1)
			merges, trunc := tracker.stats()
			truncTotal += trunc

			var miss string
			var m *Minutes
			if trunc > 0 {
				miss = fmt.Sprintf("truncated (%d calls)", trunc)
			} else if wrote != 1 {
				miss = fmt.Sprintf("wrote %d minutes", wrote)
			} else {
				var err error
				m, err = e.Store.ReadMinutes(recID)
				if err != nil {
					miss = fmt.Sprintf("read minutes: %v", err)
				} else {
					if len(m.Debates) > maxDebates {
						maxDebates = len(m.Debates)
					}
					miss = sc.check(m, merges)
				}
			}
			mark := "ok  "
			if miss == "" {
				passed++
			} else {
				mark = "MISS"
			}
			nDebates := 0
			detail := ""
			if m != nil {
				nDebates = len(m.Debates)
				if nDebates > 0 {
					var parts []string
					for _, d := range m.Debates {
						var ats []string
						for _, p := range d.Positions {
							ats = append(ats, fmt.Sprintf("%.0f", p.At))
						}
						outStr := ""
						if d.Outcome != nil {
							if d.Outcome.Decision != nil {
								outStr = fmt.Sprintf(" -> %.0f(d%d)", d.Outcome.At, *d.Outcome.Decision)
							} else {
								outStr = fmt.Sprintf(" -> %.0f(unlinked)", d.Outcome.At)
							}
						}
						parts = append(parts, fmt.Sprintf("%s[%s]%s", d.Status, strings.Join(ats, ","), outStr))
					}
					detail = strings.Join(parts, "; ")
				}
			}
			rows = append(rows, fmt.Sprintf("%s %-8s run=%d/%d debates=%d merges=%d trunc=%d  %-28s %s",
				mark, sc.name, run, runs, nDebates, merges, trunc, detail, miss))
		}
		need := requiredPasses(runs, sc.passNum, sc.passDen)
		okCase := passed >= need && truncTotal == 0 && (sc.maxDebatesAny == 0 || maxDebates <= sc.maxDebatesAny)
		status := "PASS"
		if !okCase {
			status = "FAIL"
			t.Errorf("%s: passed %d/%d (need %d/%d), truncated=%d, maxDebates=%d",
				sc.name, passed, runs, need, runs, truncTotal, maxDebates)
		}
		summary = append(summary, fmt.Sprintf("%-4s %-8s %d/%d (threshold %d/%d)  truncated=%d  max_debates=%d",
			status, sc.name, passed, runs, need, runs, truncTotal, maxDebates))
	}
	t.Logf("model %s\n%s\n\nSummary:\n%s", cfg.Model, strings.Join(rows, "\n"), strings.Join(summary, "\n"))
}
