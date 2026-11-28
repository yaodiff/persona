package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func almostEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

func TestSpeakerLabellingHandComputed(t *testing.T) {
	// Timeline:
	//   F: [0, 12], [25, 38] -> total 25 s (major)
	//   M: [12, 24], [38, 51] -> total 25 s (major)
	//   P: [24, 25] echo (excluded from timeline)
	//   X: [52, 62] -> total 10 s (minor, < 20 s)
	//   F: [65, 65.8] -> short turn (0.8 s, still voice F total 25.8 s)
	//   F: [70, 74], M: [74, 78] -> for mixed sentence (F+M)
	script := []line{
		{Voice: "F", Lang: "zh", Text: "第一句中文测试内容足够长。", Start: 0, End: 12},
		{Voice: "M", Lang: "en", Text: "Second sentence in English for testing.", Start: 12, End: 24},
		{Voice: "P", Lang: "zh", Text: "这是回声句子。", Echo: true, Start: 24, End: 25},
		{Voice: "F", Lang: "zh", Text: "第三句会落在不确定的边界上。", Start: 25, End: 38},
		{Voice: "M", Lang: "en", Text: "Fourth sentence will fall into silence.", Start: 38, End: 51},
		{Voice: "X", Lang: "zh", Text: "第五句是次要发言人的话。", Start: 52, End: 62},
		{Voice: "F", Lang: "zh", Text: "短句", Start: 65, End: 65.8},
		{Voice: "F", Lang: "zh", Text: "合并的前半句在这里。", Start: 70, End: 74},
		{Voice: "M", Lang: "zh", Text: "合并的后半句在这里。", Start: 74, End: 78},
	}

	// First check: when hub times == true times, both time-caused differences must be 0.
	exactHub := []hubSentence{
		{Start: 0, End: 12, Text: "第一句中文测试内容足够长。"},
		{Start: 12, End: 24, Text: "Second sentence in English for testing."},
		{Start: 24, End: 25, Text: "这是回声句子。", Echo: true},
		{Start: 25, End: 38, Text: "第三句会落在不确定的边界上。"},
		{Start: 38, End: 51, Text: "Fourth sentence will fall into silence."},
		{Start: 52, End: 62, Text: "第五句是次要发言人的话。"},
		{Start: 65, End: 65.8, Text: "短句"},
		{Start: 70, End: 78, Text: "合并的前半句在这里。合并的后半句在这里。"},
	}
	_, exactLabels := scoreAll(script, nil, exactHub)
	if exactLabels.Rule.SureShare != 0.7 || exactLabels.Rule.MinOverlapS != 1 || exactLabels.Rule.MinorS != 20 {
		t.Fatalf("rule = %+v, want {0.7 1 20}", exactLabels.Rule)
	}
	// Exact hub has 7 matched non-echo sentences:
	//   0 (F, [0,12]): right
	//   1 (M, [12,24]): right
	//   3 (F, [25,38]): right
	//   4 (M, [38,51]): right
	//   5 (X, [52,62]): minor voice -> unsure
	//   6 (F, [65,65.8]): 0.8 s < 1 s -> unsure
	//   7 (F+M, [70,78]): mixed
	// Echo segment is left out, so n = 7.
	ef := exactLabels.Final
	if ef.HubTimes.N != 7 || ef.TrueTimes.N != 7 || ef.Unmatched != 0 {
		t.Fatalf("exact counts: hub=%+v true=%+v unmatched=%d", ef.HubTimes, ef.TrueTimes, ef.Unmatched)
	}
	if !almostEq(ef.HubTimes.Right, 4.0/7.0) || !almostEq(ef.HubTimes.Wrong, 0) ||
		!almostEq(ef.HubTimes.Unsure, 2.0/7.0) || !almostEq(ef.HubTimes.Unlabelled, 0) ||
		!almostEq(ef.HubTimes.Mixed, 1.0/7.0) {
		t.Errorf("exact hub_times = %+v", ef.HubTimes)
	}
	if ef.TimeCausedWrongPts != 0 || ef.TimeCausedUnsureUnlabelledPts != 0 {
		t.Errorf("exact time-caused pts = (%v, %v), want (0, 0)", ef.TimeCausedWrongPts, ef.TimeCausedUnsureUnlabelledPts)
	}
	if ef.Substantive.TimeCausedWrongPts != 0 || ef.Substantive.TimeCausedUnsureUnlabelledPts != 0 {
		t.Errorf("exact substantive time-caused pts = (%v, %v), want (0, 0)",
			ef.Substantive.TimeCausedWrongPts, ef.Substantive.TimeCausedUnsureUnlabelledPts)
	}

	// Now shift hub times to trigger every label bucket on the same timeline:
	//   0 (truth F): [0, 12] -> sure F -> right
	//   1 (truth M): [0, 10] -> sure F (10 s, 100%, major) -> wrong
	//   echo (P):    [24, 25] echo=true -> left out
	//   3 (truth F): [35, 40] -> 3 s F + 2 s M (60% < 70%) -> unsure (<70%)
	//   4 (truth M): [24, 25] -> overlaps only echo P (0 s non-echo) -> unlabelled
	//   5 (truth X): [52, 62] -> 10 s X (minor < 20 s) -> unsure (minor)
	//   6 (truth F): [65, 65.8] -> 0.8 s F (< 1 s) -> unsure (<1 s)
	//   7 (truth F+M): [70, 78] -> mixed
	//   extra unmatched hub sentence -> counted in Unmatched (1)
	shiftedHub := []hubSentence{
		{Start: 0, End: 12, Text: "第一句中文测试内容足够长。"},
		{Start: 0, End: 10, Text: "Second sentence in English for testing."},
		{Start: 24, End: 25, Text: "这是回声句子。", Echo: true},
		{Start: 35, End: 40, Text: "第三句会落在不确定的边界上。"},
		{Start: 24, End: 25, Text: "Fourth sentence will fall into silence."},
		{Start: 52, End: 62, Text: "第五句是次要发言人的话。"},
		{Start: 65, End: 65.8, Text: "短句"},
		{Start: 70, End: 78, Text: "合并的前半句在这里。合并的后半句在这里。"},
		{Start: 90, End: 95, Text: "Completely unrelated hallucinated sentence about weather."},
	}
	_, shiftedLabels := scoreAll(script, nil, shiftedHub)
	sf := shiftedLabels.Final
	if sf.HubTimes.N != 7 || sf.TrueTimes.N != 7 || sf.Unmatched != 1 {
		t.Fatalf("shifted counts: hub=%+v true=%+v unmatched=%d", sf.HubTimes, sf.TrueTimes, sf.Unmatched)
	}
	// Shifted hub_times: 1 right, 1 wrong, 3 unsure (<70%, minor, <1s), 1 unlabelled, 1 mixed.
	if !almostEq(sf.HubTimes.Right, 1.0/7.0) ||
		!almostEq(sf.HubTimes.Wrong, 1.0/7.0) ||
		!almostEq(sf.HubTimes.Unsure, 3.0/7.0) ||
		!almostEq(sf.HubTimes.Unlabelled, 1.0/7.0) ||
		!almostEq(sf.HubTimes.Mixed, 1.0/7.0) {
		t.Errorf("shifted hub_times = %+v", sf.HubTimes)
	}
	// True times: 4 right, 0 wrong, 2 unsure (minor X, <1s F), 0 unlabelled, 1 mixed.
	// Differences:
	//   wrong: (1/7 - 0) * 100 = +100/7 pts
	//   unsure+unlabelled: ((3+1)/7 - (2+0)/7) * 100 = +200/7 pts
	wantWrongPts := (1.0 / 7.0) * 100
	wantUnsurePts := (2.0 / 7.0) * 100
	if !almostEq(sf.TimeCausedWrongPts, wantWrongPts) {
		t.Errorf("time_caused_wrong_pts = %v, want %v", sf.TimeCausedWrongPts, wantWrongPts)
	}
	if !almostEq(sf.TimeCausedUnsureUnlabelledPts, wantUnsurePts) {
		t.Errorf("time_caused_unsure_unlabelled_pts = %v, want %v", sf.TimeCausedUnsureUnlabelledPts, wantUnsurePts)
	}
	// Substantive drops only sentence 6 ("短句", 0.8 s, 2 Han chars), leaving 6 sentences:
	//   hub_times: 1 right, 1 wrong, 2 unsure, 1 unlabelled, 1 mixed
	//   true_times: 4 right, 0 wrong, 1 unsure (minor X), 0 unlabelled, 1 mixed
	if sf.Substantive.HubTimes.N != 6 || sf.Substantive.TrueTimes.N != 6 {
		t.Fatalf("substantive n = (%d, %d), want (6, 6)", sf.Substantive.HubTimes.N, sf.Substantive.TrueTimes.N)
	}
	if !almostEq(sf.Substantive.TimeCausedWrongPts, (1.0/6.0)*100) ||
		!almostEq(sf.Substantive.TimeCausedUnsureUnlabelledPts, (2.0/6.0)*100) {
		t.Errorf("substantive pts = (%v, %v), want (%v, %v)",
			sf.Substantive.TimeCausedWrongPts, sf.Substantive.TimeCausedUnsureUnlabelledPts,
			(1.0/6.0)*100, (2.0/6.0)*100)
	}
}

func TestSubstantiveFilter(t *testing.T) {
	cases := []struct {
		name       string
		start, end float64
		text       string
		want       bool
	}{
		{"english >= 2s", 0, 2.0, "OK.", true},
		{"english < 2s even with many letters", 0, 1.9, "This has plenty of English words.", false},
		{"chinese < 2s with 8 Han chars", 0, 1.2, "今天，我们一起发布！", true},
		{"chinese < 2s with 7 Han chars", 0, 1.2, "今天，我们一起发！", false},
		{"mixed < 2s with 7 Han and Latin", 0, 1.5, "今天我们做 API 测试。", false},
		{"mixed < 2s with 8 Han and Latin", 0, 1.5, "今天我们做一下 API 测试。", true},
	}
	for _, c := range cases {
		if got := isSubstantive(c.start, c.end, c.text); got != c.want {
			t.Errorf("%s: isSubstantive(%v, %v, %q) = %v, want %v", c.name, c.start, c.end, c.text, got, c.want)
		}
	}
}

func TestEmptyGroupJSONMarshalNoNaN(t *testing.T) {
	script := []line{
		{Voice: "F", Lang: "zh", Text: "今天开始评审。", Start: 0, End: 25},
	}
	live := []hubSentence{
		{Start: 0, End: 25, Text: "今天开始评审。"},
	}
	// Final equals live, so final_replay is empty.
	timing, labels := scoreAll(script, live, live)
	b, err := json.Marshal(map[string]any{"timing": timing, "labels": labels})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if strings.Contains(string(b), "NaN") {
		t.Fatalf("marshaled JSON contains NaN: %s", b)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	fr := raw["timing"].(map[string]any)["final_replay"].(map[string]any)
	for _, k := range []string{"start_error", "end_error", "start_signed_p50", "end_signed_p50"} {
		if fr[k] != nil {
			t.Errorf("timing.final_replay.%s = %v, want nil", k, fr[k])
		}
	}
	lfr := labels.FinalReplay
	if lfr.HubTimes.N != 0 || lfr.TrueTimes.N != 0 || lfr.Unmatched != 0 ||
		lfr.TimeCausedWrongPts != 0 || lfr.TimeCausedUnsureUnlabelledPts != 0 ||
		lfr.Substantive.HubTimes.N != 0 || lfr.Substantive.TrueTimes.N != 0 ||
		lfr.Substantive.TimeCausedWrongPts != 0 || lfr.Substantive.TimeCausedUnsureUnlabelledPts != 0 {
		t.Errorf("non-zero empty labels.final_replay: %+v", lfr)
	}

	// Also check when every group is empty.
	tEmpty, lEmpty := scoreAll(script, nil, nil)
	if _, err := json.Marshal(map[string]any{"timing": tEmpty, "labels": lEmpty}); err != nil {
		t.Fatalf("json.Marshal all-empty failed: %v", err)
	}
	if got := formatResultLine(tEmpty, lEmpty, "failed"); !strings.HasPrefix(got, "RESULT final:") || !strings.HasSuffix(got, "; minutes failed") {
		t.Errorf("formatResultLine(empty, failed) = %q", got)
	}
}

func TestRescoreFromTestdata(t *testing.T) {
	src := filepath.Join("testdata", "rescore_sample")
	dst := t.TempDir()
	for _, name := range []string{"script.json", "frames.json", "hub_segments.json"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o600); err != nil {
			t.Fatalf("write temp %s: %v", name, err)
		}
	}

	resLine, err := rescore(dst)
	if err != nil {
		t.Fatalf("rescore failed: %v", err)
	}
	wantLine := "RESULT final: sentence times cause +25.0 pts wrong, +0.0 pts unsure or unlabelled; start |error| p50 1.0 s p90 10.0 s max 10.0 s (n 4)"
	if resLine != wantLine {
		t.Errorf("result line:\ngot:  %q\nwant: %q", resLine, wantLine)
	}

	rb, err := os.ReadFile(filepath.Join(dst, "rescore.json"))
	if err != nil {
		t.Fatalf("read rescore.json: %v", err)
	}
	var got struct {
		Timing timingReport `json:"timing"`
		Labels labelsReport `json:"labels"`
	}
	if err := json.Unmarshal(rb, &got); err != nil {
		t.Fatalf("unmarshal rescore.json: %v", err)
	}

	if got.Timing.FinalSplit.LiveKept != 2 || got.Timing.FinalSplit.Replay != 2 {
		t.Errorf("final_split = %+v, want {Replay:2 LiveKept:2}", got.Timing.FinalSplit)
	}
	if got.Timing.Match["live"].MatchedScriptLines != 4 ||
		got.Timing.Match["final"].MatchedScriptLines != 4 ||
		got.Timing.Match["final_replay"].MatchedScriptLines != 2 {
		t.Errorf("timing.match = %+v", got.Timing.Match)
	}
	// Live has exact times.
	if got.Timing.Live.StartError["max"] != 0 || got.Timing.Live.EndError["max"] != 0 {
		t.Errorf("timing.live = %+v, want 0 errors", got.Timing.Live)
	}
	// Final: abs start errors [0, 0, 1, 10] -> p50=1, p90=10, max=10; signed [-10, 0, 0, 1] -> p50=0.
	//        abs end errors   [0, 0, 1, 13] -> p50=1, p90=13, max=13; signed [-13, 0, 0, 1] -> p50=0.
	if got.Timing.Final.StartError["n"] != 4 || got.Timing.Final.StartError["p50"] != 1 ||
		got.Timing.Final.StartError["p90"] != 10 || got.Timing.Final.StartError["max"] != 10 ||
		*got.Timing.Final.StartSignedP50 != 0 {
		t.Errorf("timing.final start = %+v (signed %v)", got.Timing.Final.StartError, got.Timing.Final.StartSignedP50)
	}
	if got.Timing.Final.EndError["n"] != 4 || got.Timing.Final.EndError["p50"] != 1 ||
		got.Timing.Final.EndError["p90"] != 13 || got.Timing.Final.EndError["max"] != 13 ||
		*got.Timing.Final.EndSignedP50 != 0 {
		t.Errorf("timing.final end = %+v (signed %v)", got.Timing.Final.EndError, got.Timing.Final.EndSignedP50)
	}
	// Final replay: abs start errors [1, 10] -> n=2, p50=10, max=10; signed [-10, 1] -> p50=1.
	//               abs end errors   [1, 13] -> n=2, p50=13, max=13; signed [-13, 1] -> p50=1.
	if got.Timing.FinalReplay.StartError["n"] != 2 || got.Timing.FinalReplay.StartError["p50"] != 10 ||
		*got.Timing.FinalReplay.StartSignedP50 != 1 ||
		got.Timing.FinalReplay.EndError["n"] != 2 || got.Timing.FinalReplay.EndError["max"] != 13 ||
		*got.Timing.FinalReplay.EndSignedP50 != 1 {
		t.Errorf("timing.final_replay = %+v", got.Timing.FinalReplay)
	}
	// Labels: live=0 pts, final=+25 pts wrong, final_replay=+50 pts wrong.
	if got.Labels.Live.TimeCausedWrongPts != 0 || got.Labels.Live.TimeCausedUnsureUnlabelledPts != 0 {
		t.Errorf("labels.live = %+v", got.Labels.Live)
	}
	if !almostEq(got.Labels.Final.HubTimes.Right, 0.75) || !almostEq(got.Labels.Final.HubTimes.Wrong, 0.25) ||
		!almostEq(got.Labels.Final.TrueTimes.Right, 1.0) ||
		!almostEq(got.Labels.Final.TimeCausedWrongPts, 25.0) ||
		!almostEq(got.Labels.Final.TimeCausedUnsureUnlabelledPts, 0) {
		t.Errorf("labels.final = %+v", got.Labels.Final)
	}
	if !almostEq(got.Labels.FinalReplay.HubTimes.Right, 0.5) || !almostEq(got.Labels.FinalReplay.HubTimes.Wrong, 0.5) ||
		!almostEq(got.Labels.FinalReplay.TimeCausedWrongPts, 50.0) ||
		!almostEq(got.Labels.FinalReplay.TimeCausedUnsureUnlabelledPts, 0) {
		t.Errorf("labels.final_replay = %+v", got.Labels.FinalReplay)
	}
}
