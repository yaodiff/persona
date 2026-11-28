package main

import (
	"fmt"
	"math"
	"sort"
	"unicode"
)

const (
	labelSureShare    = 0.7
	labelMinOverlap   = 1.0
	labelMinorSec     = 20.0
	substantiveMinSec = 2.0
	substantiveMinHan = 8
	floatEps          = 1e-12
)

type labelRule struct {
	SureShare   float64 `json:"sure_share"`
	MinOverlapS float64 `json:"min_overlap_s"`
	MinorS      float64 `json:"minor_s"`
}

type labelShares struct {
	N          int     `json:"n"`
	Right      float64 `json:"right"`
	Wrong      float64 `json:"wrong"`
	Unsure     float64 `json:"unsure"`
	Unlabelled float64 `json:"unlabelled"`
	Mixed      float64 `json:"mixed"`
}

type labelSummary struct {
	HubTimes                      labelShares `json:"hub_times"`
	TrueTimes                     labelShares `json:"true_times"`
	TimeCausedWrongPts            float64     `json:"time_caused_wrong_pts"`
	TimeCausedUnsureUnlabelledPts float64     `json:"time_caused_unsure_unlabelled_pts"`
}

type groupLabels struct {
	HubTimes                      labelShares  `json:"hub_times"`
	TrueTimes                     labelShares  `json:"true_times"`
	Unmatched                     int          `json:"unmatched"`
	TimeCausedWrongPts            float64      `json:"time_caused_wrong_pts"`
	TimeCausedUnsureUnlabelledPts float64      `json:"time_caused_unsure_unlabelled_pts"`
	Substantive                   labelSummary `json:"substantive"`
}

type labelsReport struct {
	Rule        labelRule   `json:"rule"`
	Live        groupLabels `json:"live"`
	Final       groupLabels `json:"final"`
	FinalReplay groupLabels `json:"final_replay"`
}

type timeInterval struct {
	start, end float64
}

type speakerTimeline struct {
	byVoice    map[string][]timeInterval
	voices     []string
	voiceTotal map[string]float64
}

func buildSpeakerTimeline(script []line) *speakerTimeline {
	raw := map[string][]timeInterval{}
	for _, l := range script {
		if l.Echo || l.End <= l.Start || l.Voice == "" {
			continue
		}
		raw[l.Voice] = append(raw[l.Voice], timeInterval{start: l.Start, end: l.End})
	}
	tl := &speakerTimeline{
		byVoice:    make(map[string][]timeInterval, len(raw)),
		voices:     make([]string, 0, len(raw)),
		voiceTotal: make(map[string]float64, len(raw)),
	}
	for v := range raw {
		tl.voices = append(tl.voices, v)
	}
	sort.Strings(tl.voices)
	for _, v := range tl.voices {
		iv := raw[v]
		sort.Slice(iv, func(i, j int) bool {
			if iv[i].start != iv[j].start {
				return iv[i].start < iv[j].start
			}
			return iv[i].end < iv[j].end
		})
		merged := iv[:0]
		for _, cur := range iv {
			if len(merged) == 0 || cur.start > merged[len(merged)-1].end {
				merged = append(merged, cur)
			} else if cur.end > merged[len(merged)-1].end {
				merged[len(merged)-1].end = cur.end
			}
		}
		tl.byVoice[v] = merged
		var tot float64
		for _, cur := range merged {
			tot += cur.end - cur.start
		}
		tl.voiceTotal[v] = tot
	}
	return tl
}

const (
	outcomeUnlabelled = iota
	outcomeUnsure
	outcomeSure
)

func (tl *speakerTimeline) labelSpan(start, end float64) (string, int) {
	if end <= start {
		return "", outcomeUnlabelled
	}
	var bestVoice string
	var bestOv, totalOv float64
	for _, v := range tl.voices {
		var ov float64
		for _, iv := range tl.byVoice[v] {
			a := max(start, iv.start)
			b := min(end, iv.end)
			if b > a {
				ov += b - a
			}
		}
		if ov > 0 {
			totalOv += ov
			if ov > bestOv {
				bestOv = ov
				bestVoice = v
			}
		}
	}
	if totalOv <= 0 {
		return "", outcomeUnlabelled
	}
	if bestOv+floatEps >= labelSureShare*totalOv &&
		bestOv+floatEps >= labelMinOverlap &&
		tl.voiceTotal[bestVoice]+floatEps >= labelMinorSec {
		return bestVoice, outcomeSure
	}
	return bestVoice, outcomeUnsure
}

func isSubstantive(trueStart, trueEnd float64, text string) bool {
	if trueEnd-trueStart+floatEps >= substantiveMinSec {
		return true
	}
	han := 0
	for _, r := range normalize(text) {
		if unicode.Is(unicode.Han, r) {
			han++
			if han >= substantiveMinHan {
				return true
			}
		}
	}
	return false
}

type labelTally struct {
	n, right, wrong, unsure, unlabelled, mixed int
}

func (t *labelTally) add(tl *speakerTimeline, spanStart, spanEnd float64, truthVoice string, mixed bool) {
	t.n++
	if mixed {
		t.mixed++
		return
	}
	voice, outcome := tl.labelSpan(spanStart, spanEnd)
	switch outcome {
	case outcomeUnlabelled:
		t.unlabelled++
	case outcomeUnsure:
		t.unsure++
	case outcomeSure:
		if voice == truthVoice {
			t.right++
		} else {
			t.wrong++
		}
	}
}

func (t labelTally) shares() labelShares {
	if t.n == 0 {
		return labelShares{}
	}
	d := float64(t.n)
	return labelShares{
		N:          t.n,
		Right:      float64(t.right) / d,
		Wrong:      float64(t.wrong) / d,
		Unsure:     float64(t.unsure) / d,
		Unlabelled: float64(t.unlabelled) / d,
		Mixed:      float64(t.mixed) / d,
	}
}

func cleanZero(v float64) float64 {
	if math.Abs(v) < floatEps {
		return 0
	}
	return v
}

func makeLabelSummary(hub, truth labelTally) labelSummary {
	hs, ts := hub.shares(), truth.shares()
	return labelSummary{
		HubTimes:                      hs,
		TrueTimes:                     ts,
		TimeCausedWrongPts:            cleanZero((hs.Wrong - ts.Wrong) * 100),
		TimeCausedUnsureUnlabelledPts: cleanZero(((hs.Unsure + hs.Unlabelled) - (ts.Unsure + ts.Unlabelled)) * 100),
	}
}

func scoreGroupLabels(tl *speakerTimeline, script []line, hub []hubSentence, m textMatchResult) groupLabels {
	var allHub, allTrue, subHub, subTrue labelTally
	for _, g := range m.Groups {
		if len(g.ScriptIdx) == 0 || len(g.HubIdx) == 0 {
			continue
		}
		truthVoice := script[g.ScriptIdx[0]].Voice
		mixed := false
		for _, si := range g.ScriptIdx[1:] {
			if script[si].Voice != truthVoice {
				mixed = true
				break
			}
		}
		trueStart := script[g.ScriptIdx[0]].Start
		trueEnd := script[g.ScriptIdx[len(g.ScriptIdx)-1]].End
		for _, hi := range g.HubIdx {
			h := hub[hi]
			allHub.add(tl, h.Start, h.End, truthVoice, mixed)
			allTrue.add(tl, trueStart, trueEnd, truthVoice, mixed)
			if isSubstantive(trueStart, trueEnd, h.Text) {
				subHub.add(tl, h.Start, h.End, truthVoice, mixed)
				subTrue.add(tl, trueStart, trueEnd, truthVoice, mixed)
			}
		}
	}
	all := makeLabelSummary(allHub, allTrue)
	return groupLabels{
		HubTimes:                      all.HubTimes,
		TrueTimes:                     all.TrueTimes,
		Unmatched:                     m.UnmatchedHub,
		TimeCausedWrongPts:            all.TimeCausedWrongPts,
		TimeCausedUnsureUnlabelledPts: all.TimeCausedUnsureUnlabelledPts,
		Substantive:                   makeLabelSummary(subHub, subTrue),
	}
}

func scoreAll(script []line, live, final []hubSentence) (timingReport, labelsReport) {
	liveMatch := alignByText(script, live)
	finalMatch := alignByText(script, final)
	liveKeptMask, split := splitFinalSegments(final, live)
	replayMatch := filterReplayMatch(finalMatch, liveKeptMask, split.Replay)

	timing := timingReport{
		Match: map[string]matchCounts{
			"live":         liveMatch.counts(),
			"final":        finalMatch.counts(),
			"final_replay": replayMatch.counts(),
		},
		Live:        computeGroupTiming(script, live, liveMatch.Groups),
		Final:       computeGroupTiming(script, final, finalMatch.Groups),
		FinalReplay: computeGroupTiming(script, final, replayMatch.Groups),
		FinalSplit:  split,
	}

	tl := buildSpeakerTimeline(script)
	labels := labelsReport{
		Rule: labelRule{
			SureShare:   labelSureShare,
			MinOverlapS: labelMinOverlap,
			MinorS:      labelMinorSec,
		},
		Live:        scoreGroupLabels(tl, script, live, liveMatch),
		Final:       scoreGroupLabels(tl, script, final, finalMatch),
		FinalReplay: scoreGroupLabels(tl, script, final, replayMatch),
	}
	return timing, labels
}

func formatResultLine(timing timingReport, labels labelsReport, minutesState string) string {
	fl := labels.Final
	wrongPts := cleanZero(fl.TimeCausedWrongPts)
	unsurePts := cleanZero(fl.TimeCausedUnsureUnlabelledPts)
	var out string
	if se := timing.Final.StartError; se != nil {
		out = fmt.Sprintf("RESULT final: sentence times cause %+.1f pts wrong, %+.1f pts unsure or unlabelled; start |error| p50 %.1f s p90 %.1f s max %.1f s (n %.0f)",
			wrongPts, unsurePts, se["p50"], se["p90"], se["max"], se["n"])
	} else {
		out = fmt.Sprintf("RESULT final: sentence times cause %+.1f pts wrong, %+.1f pts unsure or unlabelled; start |error| n 0",
			wrongPts, unsurePts)
	}
	if minutesState == "failed" {
		out += "; minutes failed"
	}
	return out
}
