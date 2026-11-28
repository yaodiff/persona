package recordingcli

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The detail endpoint returns recordings even when the recording module is off.
// Only segments carry caption timings. Recent is a clipped, display-formatted
// excerpt and must never be substituted for a transcript.
func renderRecording(detail kbwire.Detail, format string, lang i18n.Lang) ([]byte, error) {
	var meta recordingwire.Recording
	if len(detail.Meta) != 0 && json.Unmarshal(detail.Meta, &meta) != nil {
		return nil, errors.New(i18n.T(lang, "recording-export.invalid"))
	}
	empty := i18n.T(lang, "recording-export.empty")
	var cues []recordingCue
	if meta.TrimmedAt != nil {
		empty = i18n.T(lang, "recording-export.expired")
	} else {
		var err error
		cues, err = recordingCues(detail.Segments, lang)
		if err != nil {
			return nil, err
		}
	}
	switch format {
	case "vtt", "webvtt":
		if len(cues) == 0 {
			return nil, errors.New(empty)
		}
		var b bytes.Buffer
		b.WriteString("WEBVTT\n")
		for _, cue := range cues {
			fmt.Fprintf(&b, "\n%s --> %s\n%s\n", cueTime(cue.start), cueTime(cue.end), vttText.Replace(cue.text))
		}
		return b.Bytes(), nil
	case "markdown":
		return recordingMarkdown(detail, meta, cues, empty, lang)
	default:
		return nil, errors.New(i18n.T(lang, "recording-export.format"))
	}
}

type recordingCue struct {
	start, end int64 // milliseconds from the recording start
	text       string
	echo       bool
}

func recordingCues(segments []kbwire.RecordingSegment, lang i18n.Lang) ([]recordingCue, error) {
	cues := make([]recordingCue, 0, len(segments))
	for _, segment := range segments {
		line := inlineRecordingText(segment.Text)
		if line == "" {
			continue
		}
		start, end := math.Round(segment.Start*1000), math.Round(segment.End*1000)
		if !(segment.Start >= 0 && segment.End >= segment.Start && end < float64(math.MaxInt64)) {
			return nil, errors.New(i18n.T(lang, "recording-export.bad_timing"))
		}
		// The hub permits instantaneous utterances. A WebVTT cue needs at least
		// one millisecond even when both endpoints round to the same value.
		cues = append(cues, recordingCue{int64(start), max(int64(start)+1, int64(end)), line, segment.Echo})
	}
	slices.SortStableFunc(cues, func(a, b recordingCue) int { return cmp.Compare(a.start, b.start) })
	return cues, nil
}

func cueTime(ms int64) string {
	return fmt.Sprintf("%02d:%02d:%02d.%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}

// WebVTT treats markup and "-->" specially. Escaping just its three text
// characters leaves ordinary dialogue punctuation and multilingual text intact.
var vttText = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
var markdownText = strings.NewReplacer(
	"\\", "\\\\", "&", "&amp;", "<", "&lt;", ">", "&gt;",
	"*", "\\*", "_", "\\_", "`", "\\`", "[", "\\[", "]", "\\]", "~", "\\~", "#", "\\#",
)

func cleanRecordingText(text string) string {
	// Keep Markdown whitespace, including internal blank lines. Control bytes
	// from a recording or extension must not operate the receiving terminal.
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return '�'
		}
		return r
	}, text)
}

func inlineRecordingText(text string) string {
	return strings.Join(strings.Fields(cleanRecordingText(text)), " ")
}

func recordingMarkdown(detail kbwire.Detail, meta recordingwire.Recording, cues []recordingCue, empty string, lang i18n.Lang) ([]byte, error) {
	var minutes recordingwire.Minutes
	var recap recordingwire.DayRecap
	if len(detail.Minutes) > 0 && json.Unmarshal(detail.Minutes, &minutes) != nil ||
		len(detail.Recap) > 0 && json.Unmarshal(detail.Recap, &recap) != nil {
		return nil, errors.New(i18n.T(lang, "recording-export.invalid"))
	}
	title := cmp.Or(inlineRecordingText(meta.Name), inlineRecordingText(meta.Title), inlineRecordingText(minutes.Title), detail.ID)
	var b bytes.Buffer
	fmt.Fprintf(&b, "# %s\n", markdownText.Replace(title))
	section := func(label, content string) {
		if content = strings.TrimSpace(cleanRecordingText(content)); content != "" {
			fmt.Fprintf(&b, "\n## %s\n\n%s\n", i18n.T(lang, label), content)
		}
	}
	minutesText := minutes.Markdown
	if strings.TrimSpace(minutesText) == "" {
		minutesText = structuredMinutes(minutes, lang)
	}
	section("recording-export.minutes", minutesText)
	section("recording-export.recap", recap.Markdown)
	// Core also puts a day recap's summary in detail.summary. Avoid repeating
	// it alongside its complete Markdown.
	if strings.TrimSpace(recap.Markdown) == "" || strings.TrimSpace(detail.Summary) != strings.TrimSpace(recap.Summary) {
		section("recording-export.summary", cmp.Or(detail.Summary, recap.Summary))
	}
	fmt.Fprintf(&b, "\n## %s\n\n", i18n.T(lang, "recording-export.transcript"))
	if len(cues) == 0 {
		fmt.Fprintln(&b, empty)
	}
	for _, cue := range cues {
		text := markdownText.Replace(cue.text)
		if cue.echo {
			text = i18n.T(lang, "recording-export.echo", i18n.Args{"text": text})
		}
		fmt.Fprintf(&b, "- `%s` %s\n", cueTime(cue.start), text)
	}
	return b.Bytes(), nil
}

// Hubs predating detail.minutes.markdown returned the structured minutes.
func structuredMinutes(m recordingwire.Minutes, lang i18n.Lang) string {
	var parts []string
	inline := func(s string) string { return markdownText.Replace(inlineRecordingText(s)) }
	fact := func(key, field, value string) {
		if value = inline(value); value != "" {
			parts = append(parts, i18n.T(lang, key, i18n.Args{field: value}))
		}
	}
	fact("kb-minutes.when", "when", m.When)
	fact("kb-minutes.where", "where", m.Where)
	fact("kb-minutes.attendees", "names", strings.Join(m.Attendees, i18n.T(lang, "kb-minutes.list_sep")))
	var points, decisions, actions, open []string
	withOwner := func(text, owner string) string {
		if owner = inline(owner); owner != "" {
			return i18n.T(lang, "kb-minutes.owner", i18n.Args{"who": owner}) + inline(text)
		}
		return inline(text)
	}
	for _, point := range m.Points {
		points = append(points, withOwner(point.Text, point.Who))
	}
	for _, decision := range m.Decisions {
		decisions = append(decisions, inline(decision.Text))
	}
	for _, action := range m.Actions {
		text := withOwner(action.What, action.Who)
		if by := inline(cmp.Or(action.By, action.DueDate)); by != "" {
			text += i18n.T(lang, "kb-minutes.by", i18n.Args{"by": by})
		}
		actions = append(actions, text)
	}
	for _, question := range m.Open {
		open = append(open, inline(question.Text))
	}
	for _, group := range []struct {
		key   string
		lines []string
	}{{"kb-minutes.points", points}, {"kb-minutes.decisions", decisions}, {"kb-minutes.actions", actions}, {"kb-minutes.open", open}} {
		if len(group.lines) > 0 {
			parts = append(parts, "### "+i18n.T(lang, group.key)+"\n\n- "+strings.Join(group.lines, "\n- "))
		}
	}
	if m.Partial {
		parts = append(parts, i18n.T(lang, "kb-minutes.partial"))
	}
	return strings.Join(parts, "\n\n")
}
