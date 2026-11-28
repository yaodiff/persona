package recordingcli

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kbwire"
)

func TestRenderWebVTT(t *testing.T) {
	detail := kbwire.Detail{
		Meta: json.RawMessage(`{"started_at":"2026-01-23T14:03:17+09:00"}`),
		Segments: []kbwire.RecordingSegment{
			{Start: 3600.27, End: 3601, Text: "the end"},
			{Start: 59.9998, End: 60.0002, Text: "你 & I\r\n\n<b>hello</b> --> 'world'"},
			{Start: 59.9998, End: 60.05, Text: "same time"},
			{Start: 10, End: 20, Text: " \n "},
		},
	}
	got, err := renderRecording(detail, "vtt", i18n.En)
	want := "WEBVTT\n\n00:01:00.000 --> 00:01:00.001\n你 &amp; I &lt;b&gt;hello&lt;/b&gt; --&gt; 'world'\n" +
		"\n00:01:00.000 --> 00:01:00.050\nsame time\n\n01:00:00.270 --> 01:00:01.000\nthe end\n"
	if err != nil || string(got) != want {
		t.Fatalf("WebVTT = %q, %v; want %q", got, err, want)
	}
}

func TestRenderMarkdownTranscript(t *testing.T) {
	detail := kbwire.Detail{
		Meta:     json.RawMessage(`{"name":"Weekly [notes]\nreview"}`),
		Minutes:  json.RawMessage(`{"markdown":"- A **formatted** decision\n\n  Details."}`),
		Segments: []kbwire.RecordingSegment{{Start: 1.25, End: 2.5, Text: "<b>hello</b>\r\n\n# title [link](x)\x1b[1m", Echo: true}},
	}
	got, err := renderRecording(detail, "markdown", i18n.En)
	want := "# Weekly \\[notes\\] review\n\n## Meeting minutes\n\n- A **formatted** decision\n\n  Details.\n\n" +
		"## Transcript\n\n- `00:00:01.250` [Persona echo] &lt;b&gt;hello&lt;/b&gt; \\# title \\[link\\](x)�\\[1m\n"
	if err != nil || string(got) != want {
		t.Fatalf("Markdown = %q, %v; want %q", got, err, want)
	}
}

func TestRenderTrimmedRecording(t *testing.T) {
	detail := kbwire.Detail{
		ID:       "old",
		Meta:     json.RawMessage(`{"trimmed_at":"2026-01-25T00:00:00Z"}`),
		Minutes:  json.RawMessage(`{"title":"A retained meeting","decisions":[{"text":"Pick Tuesday."}],"actions":[{"what":"Send invitation","who":"Jo","by":"Friday"}]}`),
		Recent:   "Do not use this excerpt for captions.",
		Segments: []kbwire.RecordingSegment{{Text: "obsolete segment"}},
	}
	got, err := renderRecording(detail, "markdown", i18n.En)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(got)
	for _, want := range []string{"# A retained meeting", "### Decisions\n\n- Pick Tuesday.", "Jo: Send invitation (Friday)", "transcript expired"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q in %q", want, doc)
		}
	}
	if strings.Contains(doc, detail.Recent) || strings.Contains(doc, detail.Segments[0].Text) {
		t.Errorf("trimmed Markdown included a transcript: %q", doc)
	}
	if got, err := renderRecording(detail, "vtt", i18n.Ja); got != nil || err == nil || err.Error() != i18n.T(i18n.Ja, "recording-export.expired") {
		t.Fatalf("expired captions = %q, %v", got, err)
	}
}

func TestRenderEmptyRecordingAndDayRecap(t *testing.T) {
	detail := kbwire.Detail{
		ID:      "day-1",
		Summary: "the summary\n",
		Recap:   json.RawMessage(`{"markdown":"**the summary**\n\n09:00 – 10:00","summary":"the summary"}`),
	}
	got, err := renderRecording(detail, "markdown", i18n.En)
	if err != nil || strings.Count(string(got), "the summary") != 1 || !strings.Contains(string(got), "**the summary**") ||
		!strings.Contains(string(got), i18n.T(i18n.En, "recording-export.empty")) {
		t.Fatalf("recap = %q, %v", got, err)
	}
	if got, err := renderRecording(detail, "vtt", i18n.ZhHans); got != nil || err == nil || err.Error() != i18n.T(i18n.ZhHans, "recording-export.empty") {
		t.Fatalf("empty captions = %q, %v", got, err)
	}
}

func TestRenderRejectsMalformedCaptionTiming(t *testing.T) {
	for _, s := range []kbwire.RecordingSegment{
		{Start: -1}, {Start: 3, End: 2}, {End: math.NaN()}, {End: math.MaxFloat64}, {Start: math.Inf(1), End: math.Inf(1)},
	} {
		s.Text = "word"
		detail := kbwire.Detail{Segments: []kbwire.RecordingSegment{{Text: "valid"}, s}}
		if got, err := renderRecording(detail, "vtt", i18n.En); got != nil || err == nil {
			t.Errorf("offsets %v, %v yielded %q, %v", s.Start, s.End, got, err)
		}
	}
	// A malformed minutes payload must not break an independent captions export.
	detail := kbwire.Detail{Minutes: json.RawMessage(`["wrong"]`), Segments: []kbwire.RecordingSegment{{Text: "ok"}}}
	if _, err := renderRecording(detail, "vtt", i18n.En); err != nil {
		t.Fatal(err)
	}
	if got, err := renderRecording(detail, "markdown", i18n.En); got != nil || err == nil {
		t.Fatalf("malformed minutes = %q, %v", got, err)
	}
}
