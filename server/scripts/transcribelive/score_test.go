package main

import (
	"encoding/json"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestCER(t *testing.T) {
	for _, c := range []struct {
		ref, hyp string
		want     float64
	}{
		{"那 export cap 这次还是保持在 4 MB 吗？", "那export cap这次还是保持在4MB吗", 0},
		{"Hello, world.", "hello world", 0},
		{"好的", "好", 0.5},
		{"", "", 0},
		{"", "多余", 1},
		{"Bo owns it.", "", 1},
	} {
		if got := cer(c.ref, c.hyp); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("cer(%q, %q) = %v, want %v", c.ref, c.hyp, got, c.want)
		}
	}
}

func TestLatinRecallSeesTranslation(t *testing.T) {
	refs := []string{"周三有点紧，regression test 至少要留两天。", "Let's keep the landing page on the web."}
	if got := latinRecall(refs, "周三有点紧，regression test 至少要留两天。Let's keep the landing page on the web."); got != 1 {
		t.Errorf("as said: %v, want 1", got)
	}
	// "regression test" translated into Chinese loses those words.
	if got := latinRecall(refs, "周三有点紧，回归测试至少要留两天。Let's keep the landing page on the web."); got >= 1 {
		t.Errorf("translated: %v, want below 1", got)
	}
}

func TestHanShare(t *testing.T) {
	if got := hanShare("决定：Bo 负责 dashboard"); got <= 0.1 || got >= 0.9 {
		t.Errorf("mixed: %v", got)
	}
	if hanShare("Decisions: Bo owns the dashboard.") != 0 || hanShare("决定保持不变") != 1 || hanShare("— 12 —") != 0 {
		t.Error("pure texts")
	}
}

func TestSummary(t *testing.T) {
	s := summary([]float64{3, 1, 2, 10})
	if s["n"] != 4 || s["min"] != 1 || s["max"] != 10 || s["p50"] != 3 || s["p90"] != 10 {
		t.Errorf("summary = %v", s)
	}
	if summary(nil) != nil {
		t.Error("summary of nothing")
	}
}

// The embedded meeting is a third Chinese, a third English and a third both in one sentence, with one line marked as
// Persona's own answer (its echo).
func TestEmbeddedMeeting(t *testing.T) {
	var m meeting
	if err := json.Unmarshal(meetingJSON, &m); err != nil {
		t.Fatal(err)
	}
	langs, echo := map[string]int{}, 0
	for _, l := range m.Lines {
		langs[l.Lang]++
		if l.Echo {
			echo++
		}
		if l.Text == "" || l.Voice == "" {
			t.Errorf("line without text or voice: %+v", l)
		}
	}
	if langs["zh"] < 5 || langs["en"] < 5 || langs["mixed"] < 5 || echo != 1 || m.Title == "" || len(m.Glossary) == 0 {
		t.Errorf("meeting: %v languages, %d echo lines, title %q, glossary %v", langs, echo, m.Title, m.Glossary)
	}
}

// What the check uploads is what the hub takes: a body the upload refuses (a device that is not a phone)
// would stop every live run at its first upload.
func TestUploadBodyIsAValidMeetingUpload(t *testing.T) {
	var m meeting
	if err := json.Unmarshal(meetingJSON, &m); err != nil {
		t.Fatal(err)
	}
	tl := map[string]any{"roles": []any{map[string]any{"at": 0.0, "attendance": "listener"}},
		"speaking": []map[string]any{{"start": 52.1, "end": 55.8, "text": m.Lines[10].Text}}}
	b, _ := json.Marshal(uploadBody(&m, "zh-CN", "listener", time.Now().UTC(), 1, tl))
	var u recordingwire.RecordingUpload
	if err := json.Unmarshal(b, &u); err != nil {
		t.Fatal(err)
	}
	// The upload itself is the recording module's (internal/modules/recording/writers, cleanUpload), which this
	// package cannot import: what it refuses outright is checked here, rule by rule.
	if u.Revision <= 0 || u.StartedAt.IsZero() || (u.Device != "phone" && u.Device != "watch") ||
		u.Kind != recordingwire.RecordingMeeting || len(u.Segments) > recordingwire.MaxRecordingSegments {
		t.Fatalf("the hub refuses the check's upload: %+v", u)
	}
}

func assertGroupsEqual(t *testing.T, name string, got, want []textGroup) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d groups %+v, want %d groups %+v", name, len(got), got, len(want), want)
	}
	for i := range want {
		if !slices.Equal(got[i].ScriptIdx, want[i].ScriptIdx) || !slices.Equal(got[i].HubIdx, want[i].HubIdx) {
			t.Errorf("%s: group[%d] = %+v, want %+v", name, i, got[i], want[i])
		}
	}
}

func TestAlignByText(t *testing.T) {
	t.Run("1:1 mixed zh/en with echo left out and time invariance", func(t *testing.T) {
		script := []line{
			{Voice: "F", Lang: "zh", Text: "好，人都到齐了，我们开始今天的发布评审。", Start: 1, End: 4},
			{Voice: "M", Lang: "mixed", Text: "那 export cap 这次还是保持在 4 MB 吗？", Start: 5, End: 8},
			{Voice: "P", Lang: "zh", Text: "上次会议决定 export cap 保持在四兆。", Echo: true, Start: 9, End: 12},
			{Voice: "E", Lang: "en", Text: "The backend merge API now returns the conflicting paragraphs.", Start: 13, End: 17},
		}
		hub := []hubSentence{
			{Start: 1, End: 4, Text: "好人都到齐了我们开始今天的发布评审"},
			{Start: 5, End: 8, Text: "那export cap这次还是保持在4MB吗"},
			{Start: 9, End: 12, Text: "上次会议决定 export cap 保持在四兆。", Echo: true},
			{Start: 13, End: 17, Text: "the backend merge api now returns the conflicting paragraphs"},
		}
		want := []textGroup{
			{ScriptIdx: []int{0}, HubIdx: []int{0}},
			{ScriptIdx: []int{1}, HubIdx: []int{1}},
			{ScriptIdx: []int{3}, HubIdx: []int{3}},
		}
		res := alignByText(script, hub)
		assertGroupsEqual(t, "exact", res.Groups, want)
		if res.ScriptLines != 3 || res.HubSentences != 3 || res.MatchedScript != 3 || res.UnmatchedScript != 0 || res.UnmatchedHub != 0 {
			t.Errorf("counts = %+v", res)
		}

		// Shift and scramble every hub timestamp while keeping order: pairs must be identical.
		scrambled := append([]hubSentence(nil), hub...)
		scrambled[0].Start, scrambled[0].End = 999, 1005
		scrambled[1].Start, scrambled[1].End = 10, 11
		scrambled[3].Start, scrambled[3].End = 0.5, 0.6
		resScrambled := alignByText(script, scrambled)
		assertGroupsEqual(t, "scrambled times", resScrambled.Groups, want)
	})

	t.Run("one hub sentence over two lines and over three lines", func(t *testing.T) {
		script := []line{
			{Text: "先说 iOS 这边，离线编辑基本做完了。"},
			{Text: "现在在修 sync conflict 的问题。"},
			{Text: "First short part."},
			{Text: "Second short part."},
			{Text: "Third short part."},
		}
		hub := []hubSentence{
			{Text: "先说 iOS 这边，离线编辑基本做完了。现在在修 sync conflict 的问题。"},
			{Text: "First short part. Second short part. Third short part."},
		}
		res := alignByText(script, hub)
		want := []textGroup{
			{ScriptIdx: []int{0, 1}, HubIdx: []int{0}},
			{ScriptIdx: []int{2, 3, 4}, HubIdx: []int{1}},
		}
		assertGroupsEqual(t, "2:1 and 3:1", res.Groups, want)
		if res.MatchedScript != 5 || res.MatchedHub != 2 || res.UnmatchedScript != 0 || res.UnmatchedHub != 0 {
			t.Errorf("counts = %+v", res)
		}
	})

	t.Run("two and three hub sentences for one script line", func(t *testing.T) {
		script := []line{
			{Text: "That works for me. Alice can own the rollout plan and the release notes."},
			{Text: "好，今天就到这里，谢谢大家，下周再见。"},
		}
		hub := []hubSentence{
			{Text: "That works for me."},
			{Text: "Alice can own the rollout plan and the release notes."},
			{Text: "好，今天就到这里，"},
			{Text: "谢谢大家，"},
			{Text: "下周再见。"},
		}
		res := alignByText(script, hub)
		want := []textGroup{
			{ScriptIdx: []int{0}, HubIdx: []int{0, 1}},
			{ScriptIdx: []int{1}, HubIdx: []int{2, 3, 4}},
		}
		assertGroupsEqual(t, "1:2 and 1:3", res.Groups, want)
		if res.MatchedScript != 2 || res.MatchedHub != 5 || res.UnmatchedScript != 0 || res.UnmatchedHub != 0 {
			t.Errorf("counts = %+v", res)
		}
	})

	t.Run("missing script line and extra hub sentence", func(t *testing.T) {
		script := []line{
			{Text: "周三有点紧，regression test 至少要留两天。"},
			{Text: "这条发言在转写里完全丢失了没有出现。"},
			{Text: "Agreed. Beta on Wednesday, public release the following Wednesday."},
		}
		hub := []hubSentence{
			{Text: "周三有点紧，regression test 至少要留两天。"},
			{Text: "Agreed. Beta on Wednesday, public release the following Wednesday."},
			{Text: "Extra hallucinated sentence unrelated to the script at all."},
		}
		res := alignByText(script, hub)
		want := []textGroup{
			{ScriptIdx: []int{0}, HubIdx: []int{0}},
			{ScriptIdx: []int{2}, HubIdx: []int{1}},
		}
		assertGroupsEqual(t, "missing and extra", res.Groups, want)
		if res.UnmatchedScript != 1 || res.UnmatchedHub != 1 {
			t.Errorf("unmatched = (script %d, hub %d), want (1, 1)", res.UnmatchedScript, res.UnmatchedHub)
		}
	})

	t.Run("repeated identical lines keep order without crossing", func(t *testing.T) {
		script := []line{
			{Text: "好，人都到齐了，我们开始今天的发布评审。", Start: 1, End: 4},
			{Text: "Agreed. Beta on Wednesday.", Start: 5, End: 8},
			{Text: "好，人都到齐了，我们开始今天的发布评审。", Start: 10, End: 13},
			{Text: "Agreed. Beta on Wednesday.", Start: 14, End: 17},
			{Text: "好，人都到齐了，我们开始今天的发布评审。", Start: 20, End: 23},
			{Text: "Agreed. Beta on Wednesday.", Start: 24, End: 27},
		}
		// All 6 present: must pair 0->0, 1->1, ..., 5->5 monotonically.
		hubAll := []hubSentence{
			{Start: 100, End: 101, Text: "好，人都到齐了，我们开始今天的发布评审。"},
			{Start: 102, End: 103, Text: "Agreed. Beta on Wednesday."},
			{Start: 104, End: 105, Text: "好，人都到齐了，我们开始今天的发布评审。"},
			{Start: 106, End: 107, Text: "Agreed. Beta on Wednesday."},
			{Start: 108, End: 109, Text: "好，人都到齐了，我们开始今天的发布评审。"},
			{Start: 110, End: 111, Text: "Agreed. Beta on Wednesday."},
		}
		resAll := alignByText(script, hubAll)
		wantAll := []textGroup{
			{ScriptIdx: []int{0}, HubIdx: []int{0}},
			{ScriptIdx: []int{1}, HubIdx: []int{1}},
			{ScriptIdx: []int{2}, HubIdx: []int{2}},
			{ScriptIdx: []int{3}, HubIdx: []int{3}},
			{ScriptIdx: []int{4}, HubIdx: []int{4}},
			{ScriptIdx: []int{5}, HubIdx: []int{5}},
		}
		assertGroupsEqual(t, "repeated all", resAll.Groups, wantAll)

		// Missing middle pair (script 2, 3 dropped): outer pairs still align monotonically.
		hubSkip := []hubSentence{hubAll[0], hubAll[1], hubAll[4], hubAll[5]}
		resSkip := alignByText(script, hubSkip)
		if resSkip.MatchedScript != 4 || resSkip.MatchedHub != 4 {
			t.Fatalf("repeated skip counts = %+v, want 4 matched", resSkip)
		}
		for i := 1; i < len(resSkip.Groups); i++ {
			if resSkip.Groups[i].ScriptIdx[0] <= resSkip.Groups[i-1].ScriptIdx[0] ||
				resSkip.Groups[i].HubIdx[0] <= resSkip.Groups[i-1].HubIdx[0] {
				t.Errorf("non-monotonic groups: %+v", resSkip.Groups)
			}
		}
	})
}

func TestComputeGroupTiming(t *testing.T) {
	script := []line{
		{Start: 10, End: 15, Text: "A"},
		{Start: 16, End: 20, Text: "B"},
		{Start: 30, End: 40, Text: "C"},
	}
	hub := []hubSentence{
		// Group 0 (2:1): H0 covers L0..L1 -> start err 11-10=+1, end err 18-20=-2
		{Start: 11, End: 18, Text: "AB"},
		// Group 1 (1:2): H1..H2 cover L2 -> start err 27-30=-3, end err 44-40=+4
		{Start: 27, End: 35, Text: "C1"},
		{Start: 35, End: 44, Text: "C2"},
	}
	groups := []textGroup{
		{ScriptIdx: []int{0, 1}, HubIdx: []int{0}},
		{ScriptIdx: []int{2}, HubIdx: []int{1, 2}},
	}
	gt := computeGroupTiming(script, hub, groups)
	// abs start errors: [1, 3] -> n=2, min=1, p50=3, p90=3, max=3; signed [-3, +1] -> p50=+1
	// abs end errors:   [2, 4] -> n=2, min=2, p50=4, p90=4, max=4; signed [-2, +4] -> p50=+4
	if gt.StartError["n"] != 2 || gt.StartError["min"] != 1 || gt.StartError["p50"] != 3 || gt.StartError["max"] != 3 {
		t.Errorf("start_error = %v", gt.StartError)
	}
	if gt.EndError["n"] != 2 || gt.EndError["min"] != 2 || gt.EndError["p50"] != 4 || gt.EndError["max"] != 4 {
		t.Errorf("end_error = %v", gt.EndError)
	}
	if gt.StartSignedP50 == nil || *gt.StartSignedP50 != 1 {
		t.Errorf("start_signed_p50 = %v, want 1", gt.StartSignedP50)
	}
	if gt.EndSignedP50 == nil || *gt.EndSignedP50 != 4 {
		t.Errorf("end_signed_p50 = %v, want 4", gt.EndSignedP50)
	}

	empty := computeGroupTiming(script, hub, nil)
	if empty.StartError != nil || empty.EndError != nil || empty.StartSignedP50 != nil || empty.EndSignedP50 != nil {
		t.Errorf("empty groupTiming = %+v, want all nil", empty)
	}
}

func TestSplitFinalSegments(t *testing.T) {
	live := []hubSentence{
		{Start: 1.00, End: 4.00, Text: "Hello, world!"},
		{Start: 5.00, End: 8.00, Text: "第二句。"},
		{Start: 10.00, End: 13.00, Text: "Third line."},
	}
	final := []hubSentence{
		// Exact normalized text, start/end within 0.01 s -> live_kept.
		{Start: 1.01, End: 3.99, Text: "hello world"},
		// Start differs by 0.02 s (> 0.01 s) -> replay.
		{Start: 5.02, End: 8.00, Text: "第二句。"},
		// Same times as live[2], different text -> replay.
		{Start: 10.00, End: 13.00, Text: "Completely different text."},
		// Persona echo segment -> excluded from split counts.
		{Start: 15.00, End: 17.00, Text: "Echo line.", Echo: true},
	}
	mask, split := splitFinalSegments(final, live)
	wantMask := []bool{true, false, false, false}
	if !slices.Equal(mask, wantMask) {
		t.Errorf("mask = %v, want %v", mask, wantMask)
	}
	if split.LiveKept != 1 || split.Replay != 2 {
		t.Errorf("split = %+v, want {Replay:2 LiveKept:1}", split)
	}
}

func TestLiveSentencesFromFramesOrdering(t *testing.T) {
	frames := []frame{
		{At: 0.1, Frame: json.RawMessage(`{"type":"ready"}`)},
		{At: 5.0, Frame: json.RawMessage(`{"type":"line","n":2,"start":4,"end":6,"text":"second"}`)},
		{At: 2.0, Frame: json.RawMessage(`{"type":"interim","start":1,"text":"fir"}`)},
		{At: 3.0, Frame: json.RawMessage(`{"type":"line","n":1,"start":1,"end":3,"text":"first"}`)},
	}
	got := liveSentencesFromFrames(frames)
	if len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
		t.Fatalf("liveSentencesFromFrames = %+v, want [first, second]", got)
	}
}

func TestReuseScriptOmitsEchoAndResetsZeroValues(t *testing.T) {
	var m meeting
	if err := json.Unmarshal(meetingJSON, &m); err != nil {
		t.Fatal(err)
	}
	if !m.Lines[10].Echo {
		t.Fatalf("precondition failed: embedded line 10 should have echo=true")
	}
	// Build a 12-line reuse script with no "echo" key on any line and no "lang" on line 10.
	raw := make([]map[string]any, 12)
	for i := range raw {
		entry := map[string]any{
			"voice": "F",
			"text":  "line text",
			"start": float64(i * 3),
			"end":   float64(i*3 + 2),
		}
		if i != 10 {
			entry["lang"] = "zh"
		}
		raw[i] = entry
	}
	b, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "meeting.pcm"), make([]byte, 3200), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	synthesize(nil, &m, nil, dir)
	if len(m.Lines) != 12 {
		t.Fatalf("len(m.Lines) = %d, want 12", len(m.Lines))
	}
	for i, l := range m.Lines {
		if l.Echo {
			t.Errorf("line %d kept Echo=true from embedded meeting", i)
		}
	}
	if m.Lines[10].Lang != "" {
		t.Errorf("line 10 Lang = %q, want empty zero value", m.Lines[10].Lang)
	}
}

func TestWaitForMeta(t *testing.T) {
	kb := t.TempDir()
	id := "rec-1"
	if waitForMeta(kb, id, 5*time.Millisecond) {
		t.Fatal("waitForMeta returned true when meta.json is absent")
	}
	recDir := filepath.Join(kb, "recordings", id)
	if err := os.MkdirAll(recDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recDir, "meta.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !waitForMeta(kb, id, 5*time.Millisecond) {
		t.Fatal("waitForMeta returned false when meta.json exists")
	}
}
