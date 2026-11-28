package writers

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	ai "github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

var (
	// ErrMinutesStale: the minutes were written again since the version a request names.
	ErrMinutesStale = errors.New("the minutes changed since; read them again")
	// ErrRecordingIncomplete: the transcript is not complete yet, so there is nothing to make minutes of.
	ErrRecordingIncomplete = errors.New("the transcript is not complete yet")
	// errMinutesUnusable: the model answered, but nothing in it cites the transcript (or a photo).
	errMinutesUnusable = errors.New("no item of the answer cites a transcript line")
)

// putMinutes writes minutes.json and minutes.md of recording id, unless it was deleted meanwhile (false then). The
// caller holds recMu.
func (s *store) putMinutes(id string, m *Minutes) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	return s.putMinutesLocked(id, m)
}

// putMinutesLocked writes minutes.json and minutes.md of recording id, unless it was deleted meanwhile (false then).
// The caller holds recMu and recFilesMu.
func (s *store) putMinutesLocked(id string, m *Minutes) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	if err := s.recoverRecordingLocked(id); err != nil {
		return false, err
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	// writeFile makes missing directories: a recording deleted meanwhile must stay gone.
	if _, err := os.Stat(s.path(KindRecording, id, "meta.json")); err != nil {
		return false, nil
	}
	if err := s.writeFile(s.path(KindRecording, id, "minutes.json"), append(raw, '\n')); err != nil {
		return false, err
	}
	if err := s.writeFile(s.path(KindRecording, id, "minutes.md"), []byte(strings.TrimSpace(m.Markdown)+"\n")); err != nil {
		return false, err
	}
	return true, nil
}

// AskMinutes has recording id's minutes made (again) at the next round, whatever its kind: ErrNotFound when there is
// no such recording, ErrTranscriptExpired once it was trimmed, ErrRecordingIncomplete before its transcript is
// complete, and also for a complete one with no text (409 as well: there is nothing to make minutes of, and its
// minutes_state stays absent). A day recording gets its recap instead (askDayRecap): it never becomes a meeting.
func (e *engine) AskMinutes(rawID string) error {
	if e.Store.ReadOnly() {
		return ErrReadOnly
	}
	id, ok := RecordingID(rawID)
	if !ok {
		return ErrNotFound
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	r, ok := st.Recording(id)
	if !ok {
		return ErrNotFound
	}
	if r.Trimmed() {
		return ErrTranscriptExpired
	}
	if r.Kind == RecordingDay {
		return e.askDayRecap(r)
	}
	if !r.Complete || r.Chars == 0 {
		return ErrRecordingIncomplete
	}
	r.MinutesAsked, r.MatchedRev = true, 0
	remakeMinutes(r)
	return st.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
}

// remakeMinutes has r's minutes made again at the next round (the caller holds recMu and writes r): minutes a model
// call running now brings don't settle it (MinutesAsks), and the minutes kept show meanwhile.
func remakeMinutes(r *Recording) {
	r.MinutesRev = 0
	clearMinutesFailures(r)
	r.MinutesAsks++
	planMinutes(r)
}

// quietMinutes: the minutes being made now are made again only because a photo changed: not pushed once written
// (photosRemake).
func quietMinutes(r *Recording) bool { return r.MinutesQuiet > 0 && r.MinutesQuiet == r.MinutesAsks }

// SetMinutesTodos records the to-dos made of actions (action id → to-do id) in version of recording id's minutes;
// ErrNotFound without minutes. Minutes written again since get them by task instead (byTask: MinutesTaskKey → to-do
// id), on their actions without one; without byTask that is ErrMinutesStale.
func (e *engine) SetMinutesTodos(rawID string, version int, todos, byTask map[string]string) (*Minutes, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, ErrNotFound
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	m, err := st.ReadMinutes(id)
	if err != nil {
		return nil, err
	}
	changed := false
	switch {
	case m.Version == version:
		for i := range m.Actions {
			if t, ok := todos[m.Actions[i].ID]; ok && t != "" && m.Actions[i].TodoID != t {
				m.Actions[i].TodoID, changed = t, true
			}
		}
	case byTask == nil:
		return m, ErrMinutesStale
	default:
		for i, a := range m.Actions {
			if t := byTask[MinutesTaskKey(a)]; t != "" && a.TodoID == "" {
				m.Actions[i].TodoID, changed = t, true
			}
		}
	}
	if !changed {
		return m, nil
	}
	if ok, err := st.putMinutes(id, m); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotFound
	}
	st.notify(KindRecording, id, "summary")
	return m, nil
}

// WriteMinutes matches complete recordings to the calendar and writes the minutes of up to max meetings that need
// them, newest first; how many it wrote. With no model, or the model paused or unreachable, they stay pending. One
// whose last try ran out of time waits for its next (minutes_timeout.go).
func (e *engine) WriteMinutes(ctx context.Context, max int) int {
	if e.Store.ReadOnly() {
		return 0
	}
	now := e.now()
	e.matchRecordings(now)
	if e.Model == nil && e.ModelFor == nil {
		return 0
	}
	var todo []*Recording
	for _, r := range e.Store.Recordings() {
		// In hub mode they wait for the final transcript (at most minutesHubWait after the complete upload), then for
		// speaker separation while speakers.state is waiting (up to SpeakersWaitDeadline).
		if r.Complete && r.Chars > 0 && r.MinutesState == MinutesPending && !minutesWaiting(r, now) && !r.Trimmed() &&
			!transcriptionHolds(r, now) && !speakersHolds(r, now) {
			todo = append(todo, r)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].StartedAt.After(todo[j].StartedAt) })
	n := 0
	for i, r := range todo {
		if i >= max || ctx.Err() != nil { // at most max tried a round, written or not
			break
		}
		jctx, cancel := context.WithTimeout(ctx, minutesJobTimeout)
		err := e.writeMinutes(jctx, r.ID)
		cancel()
		if err == nil {
			n++
			continue
		}
		e.logf("recording %s: minutes: %v", r.ID, err)
		switch {
		case timedOut(ctx, err):
			// Slow, not wrong: tried again at a later round with more time, the others first meanwhile.
			e.minutesTimedOut(r.ID, r.Revision, time.Now())
		case unusableAnswer(err):
			e.minutesFailed(r.ID, r.Revision)
		default:
			return n // the model is paused or unreachable: the others would fail alike; still pending next round
		}
	}
	return n
}

// unusableAnswer: the model answered but the answer can't make minutes (counted towards failed); anything else (no
// connection, paused, an HTTP error) leaves them pending.
func unusableAnswer(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.Is(err, errMinutesUnusable) || errors.Is(err, ai.ErrNoObject) || errors.Is(err, ai.ErrUnterminated) || errors.Is(err, ai.ErrTruncated) ||
		errors.Is(err, ai.ErrRefused) || errors.As(err, &syntax) || errors.As(err, &typ)
}

// minutesFailed counts an unusable answer for revision rev of recording id. Minutes that end failed from a final
// transcript take the hub's audio with them.
func (e *engine) minutesFailed(id string, rev int64) {
	st := e.Store
	st.recMu.Lock()
	r, ok := st.Recording(id)
	if !ok || r.Revision != rev {
		st.recMu.Unlock()
		return
	}
	minutesFailuresFor(r, rev)
	r.MinutesFails++
	planMinutes(r)
	drop := r.MinutesState == MinutesFailed && transcriptionDone(r)
	_ = st.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
	st.recMu.Unlock()
	e.dropAudioAfterMinutes(id, drop)
}

// dropAudioAfterMinutes deletes recording id's hub-mode audio when drop (the minutes from its final transcript are
// written, or ended failed). The caller holds none of the store's locks.
func (e *engine) dropAudioAfterMinutes(id string, drop bool) {
	if !drop {
		return
	}
	if err := e.dropAudio(id, ErrTranscriptionFinal); err != nil {
		e.logf("recording %s: audio after the minutes: %v", id, err)
	}
}

// matchRecordings links each complete recording not matched at its current revision to the calendar item it overlaps
// and to the imported meeting it overlaps; one that no calendar item overlaps is looked at again until
// minutesMatchWait after it ended, and one matched without an imported meeting until minutesImportWait (#8872).
func (e *engine) matchRecordings(now time.Time) {
	var due, late []string
	for _, r := range e.Store.Recordings() {
		if !r.Complete || r.Trimmed() || r.Kind == RecordingDay {
			continue
		}
		if r.MatchedRev < r.Revision {
			due = append(due, r.ID)
		} else if r.ImportedMeeting == nil && now.Sub(recordingEnd(r)) < minutesImportWait {
			late = append(late, r.ID)
		}
	}
	if len(due) == 0 && len(late) == 0 {
		return
	}
	events, meetings := e.Store.Items(KindEvent), e.Store.Items(KindMeeting)
	st := e.Store
	for _, id := range due {
		st.recMu.Lock()
		r, ok := st.Recording(id)
		if ok && r.Complete && r.MatchedRev < r.Revision && !r.Trimmed() && r.Kind != RecordingDay {
			for _, old := range []*MeetingRef{r.Meeting, r.ImportedMeeting} {
				if old != nil {
					r.Items = removeStrings(r.Items, []string{old.Kind + "/" + old.ID})
				}
			}
			it := matchCalendar(r, events, meetings)
			if it == nil && r.Meeting == nil && now.Sub(recordingEnd(r)) < minutesMatchWait {
				st.recMu.Unlock()
				continue // nothing yet: maybe at the next collection of calendar items
			}
			r.Meeting = nil
			if it != nil {
				r.Meeting = &MeetingRef{Kind: it.Kind, ID: it.ID, Name: oneLine(it.Title)}
				r.Items = addUnique(r.Items, it.Kind+"/"+it.ID)
			}
			r.ImportedMeeting = nil
			linkImported(r, matchCalendar(r, meetings))
			r.MatchedRev = r.Revision
			r.Name = st.recordingName(r)
			planMinutes(r)
			if st.putEntity(KindRecording, r.ID, r, &r.Tagging, false) == nil {
				e.index(KindRecording, r.ID, r.Name, r.Tagging, r.OneLine, r.StartedAt)
			}
		}
		st.recMu.Unlock()
	}
	// An imported meeting that came after the match: linked alone, the calendar match and the minutes left as they are.
	for _, id := range late {
		st.recMu.Lock()
		r, ok := st.Recording(id)
		if ok && r.Complete && r.MatchedRev >= r.Revision && r.ImportedMeeting == nil && !r.Trimmed() && r.Kind != RecordingDay {
			if it := matchCalendar(r, meetings); it != nil {
				linkImported(r, it)
				if err := st.putEntity(KindRecording, r.ID, r, &r.Tagging, false); err != nil {
					e.logf("recording %s: link the imported meeting: %v", id, err)
				}
			}
		}
		st.recMu.Unlock()
	}
}

// linkImported makes it (a meetings item, or nil) r's imported meeting, linked in Items.
func linkImported(r *Recording, it *Item) {
	if it == nil {
		return
	}
	r.ImportedMeeting = &MeetingRef{Kind: it.Kind, ID: it.ID, Name: oneLine(it.Title)}
	r.Items = addUnique(r.Items, it.Kind+"/"+it.ID)
}

// matchCalendar is the calendar item r overlaps: one starting at most minutesCalendarAhead before r and before r
// ends, the closest start winning, an event (the scheduled one) before a meeting (the video call held).
func matchCalendar(r *Recording, kinds ...[]*Item) *Item {
	start, end := r.StartedAt, recordingEnd(r)
	for _, items := range kinds {
		var best *Item
		var gap time.Duration
		for _, it := range items {
			if it.At.IsZero() || strings.TrimSpace(it.Title) == "" || it.At.Before(start.Add(-minutesCalendarAhead)) || !it.At.Before(end) {
				continue
			}
			g := it.At.Sub(start)
			if g < 0 {
				g = -g
			}
			if best == nil || g < gap {
				best, gap = it, g
			}
		}
		if best != nil {
			return best
		}
	}
	return nil
}

// minutesLine is a line as the model reads it: a transcript line, N what it cites, or a photo's (Photo its number, N
// 0). End, Speaker, Unsure and UnreliableTiming are set for hub-transcribed meetings (#17674).
type minutesLine struct {
	N                int
	Photo            int
	At               float64
	End              float64
	Text             string
	Speaker          SpeakerID
	Unsure           bool
	UnreliableTiming bool
}

// minutesAnswer is the model's minutes (kb/minutes, kb/minutes_merge): items cite transcript lines, or photos, by
// number. The points and decisions come in topics (#6563), or outside them in the old shape; the summary card's
// headline, conclusions and a topic's title, group and icon are lenient (modelText, modelList): malformed, they are
// dropped rather than making the answer unusable.
type minutesAnswer struct {
	Title       string                      `json:"title"`
	Where       string                      `json:"where"`
	Attendees   []string                    `json:"attendees"`
	Headline    modelText                   `json:"headline"`
	Conclusions modelList[answerConclusion] `json:"conclusions"`
	Topics      answerTopics                `json:"topics"`
	Points      []answerPoint               `json:"points"`
	Decisions   []answerItem                `json:"decisions"`
	Actions     []answerAction              `json:"actions"`
	Open        []answerItem                `json:"open"`
	Debates     modelList[answerDebate]     `json:"debates"`
}

// answerPoint is a point of the model's answer, in a topic or (the old shape) outside topics.
type answerPoint struct {
	Text  string     `json:"text"`
	Line  modelLine  `json:"line"`
	Photo modelPhoto `json:"photo"`
	Who   string     `json:"who"`
}

// answerItem is a decision or an open question of the model's answer.
type answerItem struct {
	Text  string     `json:"text"`
	Line  modelLine  `json:"line"`
	Photo modelPhoto `json:"photo"`
}

// answerAction is an action item of the model's answer.
type answerAction struct {
	Who     string     `json:"who"`
	What    string     `json:"what"`
	By      string     `json:"by"`
	DueDate string     `json:"due_date"`
	Line    modelLine  `json:"line"`
	Photo   modelPhoto `json:"photo"`
}

// modelLine is a cited line number as models write it: 12, "12", "[12]" or "L12"; anything else is 0 (no line).
type modelLine int

func (l *modelLine) UnmarshalJSON(data []byte) error {
	*l = modelLine(modelNumber(data, "[]#Ll "))
	return nil
}

// modelPhoto is a cited photo number as models write it: 3, "3", "P3" or "[P3]"; anything else is 0 (no photo).
type modelPhoto int

func (p *modelPhoto) UnmarshalJSON(data []byte) error {
	*p = modelPhoto(modelNumber(data, "[]#Pp "))
	return nil
}

// modelNumber is the positive whole number a model wrote as JSON data, a string's cut characters trimmed; 0 for
// anything else.
func modelNumber(data []byte, cut string) int {
	data = bytes.TrimSpace(data)
	text := string(data)
	if len(data) > 0 && data[0] == '"' {
		if json.Unmarshal(data, &text) != nil {
			return 0
		}
	}
	text = strings.Trim(strings.TrimSpace(text), cut)
	if f, err := strconv.ParseFloat(text, 64); err == nil && f == float64(int(f)) && f > 0 {
		return int(f)
	}
	return 0
}

// writeMinutes makes and stores the minutes of recording id from what is committed now: its transcript of record
// (in hub mode the hub's sentences, waiting for the final ones as long as transcriptionHolds).
func (e *engine) writeMinutes(ctx context.Context, id string) error {
	files, meeting, hub, err := e.Store.recordSnapshot(id)
	if err != nil {
		return err
	}
	if files["meta.json"] == nil {
		return nil // deleted
	}
	var r Recording
	if err := json.Unmarshal(files["meta.json"], &r); err != nil {
		return err
	}
	r.Conv = meeting
	now := e.now()
	if !r.Complete || r.Chars == 0 || r.MinutesState != MinutesPending || r.Trimmed() ||
		transcriptionHolds(&r, now) || speakersHolds(&r, now) {
		return nil
	}
	if meeting != nil && meeting.Speakers != nil && meeting.Speakers.State == SpeakerStateWaiting && transcriptionDone(&r) {
		updated, err := e.expireSpeakersWaiting(id, meeting.Speakers.Since, meeting.Speakers.Tries)
		if err != nil {
			return err
		}
		if updated != nil {
			meeting = updated
			r.Conv = meeting
			if speakersHolds(&r, e.now()) {
				return nil
			}
		}
	}
	var device []RecordingSegment
	if err := json.Unmarshal(files["segments.json"], &device); err != nil {
		return err
	}
	segs := e.Store.TranscriptOfRecord(hubMode(meeting), device, hub.Segments)
	// Minutes made from the final transcript take the hub's audio with them once written (storeMinutes).
	final := transcriptionDone(&r)
	zone := e.Store.Zone()
	var segLabels []recordingwire.AlignedSpeakerLine
	if r.HubTranscribed() && meeting != nil && meeting.Speakers != nil &&
		meeting.Speakers.State == SpeakerStateReady && meeting.Speakers.Quality == SpeakerQualityOk {
		if turns, err := e.Store.speakerTurns(id); err == nil && turns != nil {
			if aligned, err := e.Store.core.AlignTranscriptSpeakers(recordingwire.SpeakerAlignInput{
				Segments: segs,
				Turns:    turns.Turns,
				Covered:  turns.Covered,
				Speakers: *meeting.Speakers,
				Timing:   hub.EffectiveTiming(),
			}); err == nil {
				segLabels = aligned.Lines
			} else {
				e.logf("recording %s: align transcript speakers: %v", id, err)
			}
		}
	}
	timingSpread := hub.EffectiveTiming() == TimingSpread
	numbered := false
	lines := make([]minutesLine, 0, len(segs))
	for i, sg := range segs {
		if sg.Echo {
			continue // Persona's own answer, heard by the microphone: not the meeting's (N still counts it: citations index segs)
		}
		if t := strings.Join(strings.Fields(sg.Text), " "); t != "" {
			end := sg.End
			if end < sg.Start {
				end = sg.Start
			}
			ml := minutesLine{
				N:                i + 1,
				At:               sg.Start,
				End:              end,
				Text:             clip(t, minutesLineRunes),
				UnreliableTiming: r.HubTranscribed() && strings.EqualFold(sg.Source, SourceHub) && timingSpread,
			}
			if i < len(segLabels) && segLabels[i].Speaker != "" {
				ml.Speaker = segLabels[i].Speaker
				ml.Unsure = segLabels[i].Unsure
				numbered = true
			}
			lines = append(lines, ml)
		}
	}
	// The photos with words, among the lines in time order (after the lines of the same second). A photo added or
	// deleted from now on makes them again (photosRemake), and one deleted is cited by nothing stored
	// (storeMinutes).
	for _, p := range e.PhotoTexts(id) {
		if t := e.Store.photoWords(p, true); t != "" {
			lines = append(lines, minutesLine{Photo: p.N, At: p.At, Text: t})
		}
	}
	slices.SortStableFunc(lines, func(a, b minutesLine) int { return cmp.Compare(a.At, b.At) })
	chunks, partial := chunkLines(lines, cmp.Or(e.minutesCall, minutesOneCall), cmp.Or(e.minutesPart, minutesChunk), minutesMaxChunks)
	if len(chunks) == 0 {
		return nil
	}
	in := e.minutesInput(&r, zone)
	model := e.modelFor(PurposeMeetingMinutes)
	callTime := minutesCallTime(minutesTimeouts(&r))
	call := func(system, user string, out *minutesAnswer) error {
		cctx, cancel := context.WithTimeout(ctx, callTime)
		defer cancel()
		return model.JSONMax(cctx, system, user, 6000, out)
	}
	lang := e.outputLang(&r)
	owner := e.writingOwner(lang)
	owner.Speakers = numbered
	system := prompts.RenderKBMinutes(owner)
	var read []minutesLine
	var ans minutesAnswer
	if len(chunks) == 1 {
		read = chunks[0]
		in.Part, in.Parts, in.Transcript = 1, 1, renderMinutesLines(chunks[0], r.StartedAt, zone)
		if err := call(system, prompts.RenderKBMinutesInput(in), &ans); err != nil {
			return err
		}
	} else {
		merge := prompts.KBMinutesMergeInput{Meeting: in}
		for i, c := range chunks {
			in.Part, in.Parts, in.Transcript = i+1, len(chunks), renderMinutesLines(c, r.StartedAt, zone)
			var notes minutesAnswer
			if err := call(system, prompts.RenderKBMinutesInput(in), &notes); err != nil {
				return fmt.Errorf("part %d of %d: %w", i+1, len(chunks), err)
			}
			read = append(read, c...)
			part := notesForMerge(i+1, notes, c, r.StartedAt, zone, max(2, minutesMergeItems/len(chunks)))
			if limit := max(2, minutesMergeDebateTotal/len(chunks)); len(part.Debates) > limit {
				part.Debates = part.Debates[:limit]
			}
			merge.Parts = append(merge.Parts, part)
		}
		merge.Meeting.Transcript = ""
		err := call(prompts.RenderKBMinutesMerge(owner), prompts.RenderKBMinutesMergeInput(merge), &ans)
		if err != nil {
			return fmt.Errorf("merging %d parts: %w", len(chunks), err)
		}
		read = notedLines(read, merge.Parts) // the merging call saw the notes, not the transcript
	}
	m, err := e.buildMinutesWith(&r, ans, read, zone, lang, numbered)
	if err != nil {
		return err
	}
	m.Partial = partial
	return e.storeMinutes(ctx, &r, m, final)
}

// minutesInput is what the model is told about recording r besides its transcript.
func (e *engine) minutesInput(r *Recording, zone *time.Location) prompts.KBMinutesInput {
	in := prompts.KBMinutesInput{Pinned: r.Pinned, Title: cmp.Or(r.Title, calendarTitle(r)), Start: r.StartedAt.In(zone),
		End: recordingEnd(r).In(zone), Weekday: weekdayZH[r.StartedAt.In(zone).Weekday()], Minutes: int(r.Duration / 60),
		Language: r.Language}
	if r.Meeting != nil {
		in.Meeting = r.Meeting.Name
	}
	if c := r.Calendar; c != nil {
		in.Attendees, in.Location = c.Attendees, c.Location
	}
	return in
}

var weekdayZH = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// untitledMinutes is the title of r's minutes in lang when neither the user, the calendar nor the model gives one: r's
// summary's title, else when it started, said in lang (kb-minutes.untitled). Chinese minutes take r's name in lists,
// which says the same (Store.recordingName).
func untitledMinutes(r *Recording, zone *time.Location, lang i18n.Lang) string {
	switch {
	case lang == i18n.ZhHans:
		return r.Name
	case r.AITitle != "":
		return r.AITitle
	}
	return i18n.T(lang, "kb-minutes.untitled", i18n.Args{"when": r.StartedAt.In(zone).Format("2006-01-02 15:04")})
}

// minutesLineWeight is the rune budget of l for chunkLines, including the optional 〔S…〕 speaker marker (#17674).
func minutesLineWeight(l minutesLine) int {
	w := len([]rune(l.Text)) + 16
	if tag := formatPromptSpeaker(l.Speaker, l.Unsure); tag != "" {
		w += len([]rune(tag)) + 3 // "〔" + tag + "〕 "
	}
	return w
}

// chunkLines cuts lines into chunks of about size runes at line boundaries: one chunk when all of them fit in one,
// at most max chunks (partial: lines were left out).
func chunkLines(lines []minutesLine, one, size, max int) (chunks [][]minutesLine, partial bool) {
	total := 0
	for _, l := range lines {
		total += minutesLineWeight(l)
	}
	if total <= one {
		if len(lines) == 0 {
			return nil, false
		}
		return [][]minutesLine{lines}, false
	}
	var cur []minutesLine
	n := 0
	for i, l := range lines {
		w := minutesLineWeight(l)
		if n > 0 && n+w > size {
			chunks = append(chunks, cur)
			cur, n = nil, 0
			if len(chunks) == max {
				return chunks, i < len(lines)
			}
		}
		cur = append(cur, l)
		n += w
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks, false
}

// renderMinutesLines is the transcript as the model reads it: "[N] HH:MM:SS text" per line (or "[N] HH:MM:SS 〔S2〕 text"
// when a speaker label is present) in the user's zone, a photo's line as photos.md has it (photoLine).
func renderMinutesLines(lines []minutesLine, start time.Time, zone *time.Location) string {
	var b strings.Builder
	for _, l := range lines {
		if l.Photo > 0 {
			b.WriteString(photoLine(l.Photo, clockAt(start, l.At, zone), l.Text) + "\n")
			continue
		}
		if tag := formatPromptSpeaker(l.Speaker, l.Unsure); tag != "" {
			fmt.Fprintf(&b, "[%d] %s 〔%s〕 %s\n", l.N, clockAt(start, l.At, zone), tag, l.Text)
		} else {
			fmt.Fprintf(&b, "[%d] %s %s\n", l.N, clockAt(start, l.At, zone), l.Text)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// notesForMerge is a chunk's notes for the merging call, keeping only items citing a line or a photo of that chunk,
// each without the model's marks (the merging call marks them again) and clipped to minutesNoteRunes. Every point and
// decision is kept, in a topic or not; one in a titled topic carries its title (#6563). Each section is put in the
// order of what its items cite, then cut to each: a part's latest lines go, not whole topics. Its headline and
// conclusions are not read: the merging call writes the meeting's own.
func notesForMerge(part int, a minutesAnswer, chunk []minutesLine, start time.Time, zone *time.Location, each int) prompts.KBMinutesPart {
	lines, photos := map[int]int{}, map[int]int{} // what the chunk has, by its place in it
	lineByN := map[int]minutesLine{}
	for i, l := range chunk {
		if l.Photo > 0 {
			photos[l.Photo] = i
		} else {
			lines[l.N] = i
			lineByN[l.N] = l
		}
	}
	p := prompts.KBMinutesPart{Part: part, From: clockAt(start, chunk[0].At, zone), To: clockAt(start, chunk[len(chunk)-1].At, zone),
		Where: plainLine(a.Where)}
	for _, n := range a.Attendees {
		p.Attendees = append(p.Attendees, plainLine(n))
	}
	type placedNote struct {
		note prompts.KBMinutesNote
		at   int // the place in chunk of what it cites
	}
	// topic: the title of the topic it came in ("" none).
	note := func(list *[]placedNote, topic, text string, line modelLine, photo modelPhoto, extra ...string) {
		text = clipRunes(stripMarks(text), minutesNoteRunes)
		n := prompts.KBMinutesNote{Text: text, Topic: topic}
		var at int
		if text == "" {
			return
		}
		if i, ok := photos[int(photo)]; ok && photo > 0 {
			n.Photo, at = int(photo), i
		} else if i, ok := lines[int(line)]; ok {
			n.Line, at = int(line), i
			if ml, has := lineByN[int(line)]; has {
				n.Speaker = formatPromptSpeaker(ml.Speaker, ml.Unsure)
			}
		} else {
			return
		}
		if len(extra) > 0 {
			n.Who = plainLine(extra[0])
		}
		if len(extra) > 2 {
			n.By, n.DueDate = plainLine(extra[1]), strings.TrimSpace(stripMarks(extra[2]))
		}
		*list = append(*list, placedNote{n, at})
	}
	var points, decisions, actions, open []placedNote
	for _, t := range a.Topics {
		title := cardLine(string(t.Title), minutesTopicRunes)
		for _, x := range t.Points {
			note(&points, title, x.Text, x.Line, x.Photo, x.Who)
		}
		for _, x := range t.Decisions {
			note(&decisions, title, x.Text, x.Line, x.Photo)
		}
	}
	for _, x := range a.Points {
		note(&points, "", x.Text, x.Line, x.Photo, x.Who)
	}
	for _, x := range a.Decisions {
		note(&decisions, "", x.Text, x.Line, x.Photo)
	}
	for _, x := range a.Actions {
		note(&actions, "", x.What, x.Line, x.Photo, x.Who, x.By, x.DueDate)
	}
	for _, x := range a.Open {
		note(&open, "", x.Text, x.Line, x.Photo)
	}
	section := func(list []placedNote) []prompts.KBMinutesNote {
		slices.SortStableFunc(list, func(x, y placedNote) int { return cmp.Compare(x.at, y.at) })
		var out []prompts.KBMinutesNote
		for _, n := range firstN(list, each) {
			out = append(out, n.note)
		}
		return out
	}
	p.Points, p.Decisions, p.Actions, p.Open = section(points), section(decisions), section(actions), section(open)
	p.Debates = debateNotesForMerge(a, lines, lineByN, minutesDebatesMax)
	return p
}

// notedLines is the lines and photos of read the parts' notes cite.
func notedLines(read []minutesLine, parts []prompts.KBMinutesPart) []minutesLine {
	cited, photos := map[int]bool{}, map[int]bool{}
	for _, p := range parts {
		for _, list := range [][]prompts.KBMinutesNote{p.Points, p.Decisions, p.Actions, p.Open} {
			for _, n := range list {
				if n.Photo > 0 {
					photos[n.Photo] = true
				} else {
					cited[n.Line] = true
				}
			}
		}
		for _, d := range p.Debates {
			for _, pos := range d.Positions {
				if pos.Line > 0 {
					cited[pos.Line] = true
				}
				for _, al := range pos.Also {
					if al.Line > 0 {
						cited[al.Line] = true
					}
				}
			}
			if d.OutcomeLine > 0 {
				cited[d.OutcomeLine] = true
			}
		}
	}
	var out []minutesLine
	for _, l := range read {
		if l.Photo > 0 && photos[l.Photo] || l.Photo == 0 && cited[l.N] {
			out = append(out, l)
		}
	}
	return out
}

func firstN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func clipText(s string) string { return clipRunes(s, minutesTextRunes) }

// buildMinutes checks the model's answer against the lines it read: every item must cite one of them (a transcript
// line, or a photo: its time then), a person must be named in them or in the calendar, a due date must be a real day
// from the meeting on (within a year). A photo's words count as said. The minutes are in lang.
func (e *engine) buildMinutes(r *Recording, a minutesAnswer, read []minutesLine, zone *time.Location, lang i18n.Lang) (*Minutes, error) {
	return e.buildMinutesWith(r, a, read, zone, lang, false)
}

func (e *engine) buildMinutesWith(r *Recording, a minutesAnswer, read []minutesLine, zone *time.Location, lang i18n.Lang, numbered bool) (*Minutes, error) {
	at, photoSec := map[int]float64{}, map[int]float64{}
	lineByN := map[int]minutesLine{}
	var saidB strings.Builder
	for _, l := range read {
		if l.Photo > 0 {
			photoSec[l.Photo] = l.At
		} else {
			at[l.N] = l.At
			lineByN[l.N] = l
		}
		saidB.WriteString(l.Text)
		saidB.WriteByte('\n')
	}
	raw := saidB.String()
	said := strings.ToLower(raw)
	var calendar []string
	if r.Calendar != nil {
		calendar = r.Calendar.Attendees
	}
	named := func(who string) string {
		who = plainLine(who)
		if who == "" {
			return ""
		}
		for _, c := range calendar {
			if strings.EqualFold(c, who) {
				return c
			}
		}
		if spoken(raw, who) {
			return who
		}
		return ""
	}
	cited, dropped := 0, 0
	// The photo when the item cites one read, else its line: one citing neither is dropped.
	line := func(l modelLine, p modelPhoto) (float64, int, bool) {
		if sec, ok := photoSec[int(p)]; ok && p > 0 {
			cited++
			return sec, int(p), true
		}
		sec, ok := at[int(l)]
		if ok {
			cited++
		} else {
			dropped++
		}
		return sec, 0, ok
	}
	m := &Minutes{Title: cmp.Or(r.Title, calendarTitle(r), plainLine(a.Title), untitledMinutes(r, zone, lang)),
		When: minutesWhen(r, zone), Language: string(lang), ModelTitle: titleLine(a.Title)}
	m.Start, m.End = minutesSpan(r, zone)
	if r.Calendar != nil && r.Calendar.Location != "" {
		m.Where = r.Calendar.Location
	} else if w := plainLine(a.Where); w != "" && strings.Contains(said, strings.ToLower(w)) {
		m.Where = w
	}
	m.Attendees = append(m.Attendees, calendar...)
	for _, p := range a.Attendees {
		if n := named(p); n != "" && !slices.Contains(m.Attendees, n) {
			m.Attendees = append(m.Attendees, n)
		}
	}
	seen := map[string]bool{}
	fresh := func(section, text string) bool {
		k := section + "\x00" + text
		if text == "" || seen[k] {
			return false
		}
		seen[k] = true
		return true
	}
	// The citation first: an item citing nothing read must not hide a later copy of it that does. The points and
	// decisions of the topics first, then those outside them (the old shape); each without its ** marks, which become
	// its bold places (strongText). topic is the answer's topic it came in, -1 outside topics.
	var points []placed[MinutesPoint]
	var decisions []placed[MinutesItem]
	point := func(x answerPoint, topic int) {
		if t, strong := strongText(x.Text, minutesTextRunes); t != "" {
			if sec, photo, ok := line(x.Line, x.Photo); ok && fresh("p", t) {
				lineN := 0
				if photo == 0 {
					lineN = int(x.Line)
				}
				points = append(points, placed[MinutesPoint]{MinutesPoint{Text: t, At: sec, Photo: photo, Who: named(x.Who)}, topic, strong, lineN})
			}
		}
	}
	decision := func(x answerItem, topic int) {
		if t, strong := strongText(x.Text, minutesTextRunes); t != "" {
			if sec, photo, ok := line(x.Line, x.Photo); ok && fresh("d", t) {
				lineN := 0
				if photo == 0 {
					lineN = int(x.Line)
				}
				decisions = append(decisions, placed[MinutesItem]{MinutesItem{Text: t, At: sec, Photo: photo}, topic, strong, lineN})
			}
		}
	}
	for i, t := range a.Topics {
		for _, x := range t.Points {
			point(x, i)
		}
		for _, x := range t.Decisions {
			decision(x, i)
		}
	}
	for _, x := range a.Points {
		point(x, -1)
	}
	for _, x := range a.Decisions {
		decision(x, -1)
	}
	day := r.StartedAt.In(zone).Format("2006-01-02")
	for _, x := range a.Actions {
		if t := cardLine(x.What, minutesTextRunes); t != "" {
			// The same task for two people is two actions.
			if sec, photo, ok := line(x.Line, x.Photo); ok && fresh("a"+plainLine(x.Who), t) {
				by := plainLine(x.By)
				due := ""
				if by != "" { // a date only for a deadline someone said
					due = dueDate(stripMarks(x.DueDate), day)
				}
				m.Actions = append(m.Actions, MinutesAction{Who: named(x.Who), What: t, By: by, DueDate: due, At: sec, Photo: photo})
			}
		}
	}
	for _, x := range a.Open {
		if t := cardLine(x.Text, minutesTextRunes); t != "" {
			if sec, photo, ok := line(x.Line, x.Photo); ok && fresh("o", t) {
				m.Open = append(m.Open, MinutesItem{Text: t, At: sec, Photo: photo})
			}
		}
	}
	// Nothing that cites the transcript (an empty or unrelated answer) is not minutes. The conclusions never count.
	if cited == 0 {
		return nil, errMinutesUnusable
	}
	slices.SortStableFunc(points, func(x, y placed[MinutesPoint]) int { return cmp.Compare(x.item.At, y.item.At) })
	slices.SortStableFunc(decisions, func(x, y placed[MinutesItem]) int { return cmp.Compare(x.item.At, y.item.At) })
	sort.SliceStable(m.Actions, func(i, j int) bool { return m.Actions[i].At < m.Actions[j].At })
	sort.SliceStable(m.Open, func(i, j int) bool { return m.Open[i].At < m.Open[j].At })
	points, decisions, m.Actions, m.Open = capItems(points), capItems(decisions), capItems(m.Actions), capItems(m.Open)
	m.Points, m.Decisions = placedItems(points), placedItems(decisions)
	for i := range m.Actions {
		m.Actions[i].ID = "a" + strconv.Itoa(i+1)
	}
	// The summary card, when every point and decision kept is under a titled topic (buildOverview); otherwise the
	// minutes are as before, and the log says why.
	var why string
	if m.Overview, why = buildOverview(a, points, decisions, at, photoSec, m.Title); why != "" {
		e.logf("recording %s: minutes: no overview: %s", r.ID, why)
	}
	storeSpan := r.HubTranscribed() && (r.Conv == nil || r.Conv.Speakers == nil || r.Conv.Speakers.State != SpeakerStateCleared)
	m.Debates = buildDebates(a.Debates, lineByN, decisions, named, storeSpan, numbered)
	return m, nil
}

func capItems[T any](s []T) []T {
	if len(s) > minutesMaxItems {
		return s[:minutesMaxItems]
	}
	return s
}

// dueDate is s when it is a YYYY-MM-DD day from day (the meeting's, YYYY-MM-DD) to a year after it, else "". Days are
// compared as calendar dates (UTC midnights), so a time zone's missing midnight can't move one.
func dueDate(s string, day string) string {
	d, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	from, err2 := time.Parse("2006-01-02", day)
	if err != nil || err2 != nil || d.Before(from) || d.After(from.AddDate(1, 0, 0)) {
		return ""
	}
	return d.Format("2006-01-02")
}

// spoken: name is said in text. A name in Latin letters must be a whole word written as given ("Ann" is not in
// "planning", "Will" is not "will"); any other (Chinese and the like, which have no word breaks) is found as it is.
func spoken(text, name string) bool {
	latin := true
	for _, c := range name {
		if c > unicode.MaxASCII {
			latin = false
			break
		}
	}
	if !latin {
		return strings.Contains(strings.ToLower(text), strings.ToLower(name))
	}
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(name)
		if !wordByte(text, start-1) && !wordByte(text, end) {
			return true
		}
		i = start + 1
	}
}

// wordByte: text[i] is an ASCII letter or digit (false outside text).
func wordByte(text string, i int) bool {
	if i < 0 || i >= len(text) {
		return false
	}
	c := text[i]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// minutesWhen is "2006-01-02 15:04–16:10" in the user's zone (the end's date too when it is another day).
func minutesWhen(r *Recording, zone *time.Location) string {
	start, end := r.StartedAt.In(zone), recordingEnd(r).In(zone)
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return start.Format("2006-01-02 15:04") + "–" + end.Format("15:04")
	}
	return start.Format("2006-01-02 15:04") + "–" + end.Format("2006-01-02 15:04")
}

// keepTodos carries the to-dos made of the previous minutes' actions over to the same task for the same person in m,
// so minutes made again don't offer what the user already added.
func keepTodos(m, old *Minutes) {
	made := map[string]string{}
	for _, a := range old.Actions {
		if a.TodoID != "" {
			made[MinutesTaskKey(a)] = a.TodoID
		}
	}
	for i, a := range m.Actions {
		if t, ok := made[MinutesTaskKey(a)]; ok {
			m.Actions[i].TodoID = t
		}
	}
}

// renderMinutes is minutes.md: the minutes with clock times, sections without anything left out; an item citing a
// photo ends with its number. Its headings and fixed words are in the minutes' language (kb-minutes.json). Minutes
// with an overview follow it (renderOverview).
func renderMinutes(m *Minutes, start time.Time, zone *time.Location) string {
	return renderMinutesWith(m, start, zone, nil)
}

func (e *engine) renderMinutes(m *Minutes, start time.Time, zone *time.Location, speakers *MeetingSpeakers) string {
	return renderMinutesWith(m, start, zone, e.debateWhoFunc(speakers))
}

func renderMinutesWith(m *Minutes, start time.Time, zone *time.Location, debateWho func(MinutesDebatePosition, i18n.Lang, func(string) string) string) string {
	if m.Overview != nil {
		return renderOverviewWith(m, start, zone, debateWho)
	}
	w := newMinutesMarkdown(m, start, zone, func(s string) string { return s }, debateWho)
	b, l := &w.b, w.l
	w.head(m)
	if len(m.Points) > 0 {
		b.WriteString("\n## " + i18n.T(l, "kb-minutes.points") + "\n\n")
		for _, p := range m.Points {
			b.WriteString("- " + w.at(p.At) + p.Text)
			if p.Who != "" {
				b.WriteString(i18n.T(l, "kb-minutes.who", i18n.Args{"who": p.Who}))
			}
			w.end(p.Photo)
		}
	}
	w.section(i18n.T(l, "kb-minutes.decisions"), m.Decisions)
	w.tail(m, true)
	return strings.TrimRight(b.String(), "\n")
}

// renderOverview is minutes.md of minutes with an overview (docs/assistant/meeting-minutes.md "Markdown"): the
// headline, the core conclusions, any disagreements, then each group's topics (a topic a ## heading with one group, a
// ### under its group's ## with several), each decision after "Decided:", then the action items and open questions as
// without one, and the AI note last. A literal * is escaped, and the bold places are wrapped in **.
func renderOverview(m *Minutes, start time.Time, zone *time.Location) string {
	return renderOverviewWith(m, start, zone, nil)
}

func renderOverviewWith(m *Minutes, start time.Time, zone *time.Location, debateWho func(MinutesDebatePosition, i18n.Lang, func(string) string) string) string {
	o := m.Overview
	w := newMinutesMarkdown(m, start, zone, escapeStars, debateWho)
	b, l := &w.b, w.l
	w.head(m)
	if o.Headline != "" {
		b.WriteString("\n" + escapeStars(o.Headline) + "\n")
	}
	if len(o.Conclusions) > 0 {
		b.WriteString("\n## " + i18n.T(l, "kb-minutes.conclusions") + "\n\n")
		for _, c := range o.Conclusions {
			b.WriteString("- " + w.at(c.At) + cardMarkdown(c.Text, c.Strong))
			w.end(c.Photo)
		}
	}
	w.debates(m.Debates)
	topic := "\n## "
	for _, g := range o.Groups {
		if len(o.Groups) > 1 {
			b.WriteString("\n## " + escapeStars(g.Title) + "\n")
			topic = "\n### "
		}
		for _, t := range g.Topics {
			b.WriteString(topic + escapeStars(t.Title) + "\n\n")
			for _, x := range t.Bullets {
				b.WriteString("- " + w.at(x.At))
				if x.Decision {
					b.WriteString(i18n.T(l, "kb-minutes.decided"))
				}
				b.WriteString(cardMarkdown(x.Text, x.Strong))
				if x.Who != "" {
					b.WriteString(i18n.T(l, "kb-minutes.who", i18n.Args{"who": escapeStars(x.Who)}))
				}
				w.end(x.Photo)
			}
		}
	}
	w.tail(m, false)
	b.WriteString("\n*" + i18n.T(l, "kb-minutes.ai_note") + "*\n")
	return strings.TrimRight(b.String(), "\n")
}

// minutesMarkdown writes minutes.md: esc is what every text the minutes hold goes through (the overview's layout
// escapes a literal *, the legacy one writes them as they are).
type minutesMarkdown struct {
	b         strings.Builder
	l         i18n.Lang
	start     time.Time
	zone      *time.Location
	esc       func(string) string
	debateWho func(MinutesDebatePosition, i18n.Lang, func(string) string) string
}

func newMinutesMarkdown(m *Minutes, start time.Time, zone *time.Location, esc func(string) string, debateWho func(MinutesDebatePosition, i18n.Lang, func(string) string) string) *minutesMarkdown {
	return &minutesMarkdown{l: storedLang(m.Language), start: start, zone: zone, esc: esc, debateWho: debateWho}
}

func (w *minutesMarkdown) at(sec float64) string { return "[" + clockAt(w.start, sec, w.zone) + "] " }

// end ends an item's line, after its photo's number when it cites one.
func (w *minutesMarkdown) end(photo int) {
	if photo > 0 {
		w.b.WriteString(i18n.T(w.l, "kb-minutes.photo", i18n.Args{"n": photo}))
	}
	w.b.WriteByte('\n')
}

// head is the title, when, where and the attendees.
func (w *minutesMarkdown) head(m *Minutes) {
	b, l := &w.b, w.l
	b.WriteString("# " + w.esc(m.Title) + "\n\n- " + i18n.T(l, "kb-minutes.when", i18n.Args{"when": m.When}) + "\n")
	if m.Where != "" {
		b.WriteString("- " + i18n.T(l, "kb-minutes.where", i18n.Args{"where": w.esc(m.Where)}) + "\n")
	}
	if len(m.Attendees) > 0 {
		names := make([]string, len(m.Attendees))
		for i, n := range m.Attendees {
			names[i] = w.esc(n)
		}
		b.WriteString("- " + i18n.T(l, "kb-minutes.attendees", i18n.Args{"names": strings.Join(names, i18n.T(l, "kb-minutes.list_sep"))}) + "\n")
	}
}

// section is a heading over items, nothing without any.
func (w *minutesMarkdown) section(title string, items []MinutesItem) {
	if len(items) == 0 {
		return
	}
	w.b.WriteString("\n## " + title + "\n\n")
	for _, x := range items {
		w.b.WriteString("- " + w.at(x.At) + w.esc(x.Text))
		w.end(x.Photo)
	}
}

// tail is the action items, optionally the disagreements (legacy layout, before open questions), the open questions
// and the partial note.
func (w *minutesMarkdown) tail(m *Minutes, withDebates bool) {
	b, l := &w.b, w.l
	if len(m.Actions) > 0 {
		b.WriteString("\n## " + i18n.T(l, "kb-minutes.actions") + "\n\n")
		for _, a := range m.Actions {
			b.WriteString("- " + w.at(a.At))
			if a.Who != "" {
				b.WriteString(i18n.T(l, "kb-minutes.owner", i18n.Args{"who": w.esc(a.Who)}))
			}
			b.WriteString(w.esc(a.What))
			if by := cmp.Or(a.By, a.DueDate); by != "" {
				b.WriteString(i18n.T(l, "kb-minutes.by", i18n.Args{"by": w.esc(by)}))
			}
			w.end(a.Photo)
		}
	}
	if withDebates {
		w.debates(m.Debates)
	}
	w.section(i18n.T(l, "kb-minutes.open"), m.Open)
	if m.Partial {
		b.WriteString("\n> " + i18n.T(l, "kb-minutes.partial") + "\n")
	}
}

// storeMinutes writes m for snapshot r (the revision read) unless the recording was deleted or trimmed meanwhile, then
// tells MinutesWritten once they are the current revision's, unless they were made again only for a photo or the final
// transcript (quietMinutes). Items citing a photo no longer listed are left out; when the photos the minutes cite
// changed, so does PartPhotos, before the minutes are written. final: they were made from a hub-mode recording's final
// transcript, so its audio goes once they are stored and nothing asked for them again meanwhile (quiet or not).
func (e *engine) storeMinutes(ctx context.Context, r *Recording, m *Minutes, final bool) error {
	st := e.Store
	st.recMu.Lock()
	cur, ok := st.Recording(r.ID)
	if !ok || cur.Trimmed() {
		// Trimmed while the model wrote them: the minutes the trim kept (and the photos they cite) stay as they are.
		st.recMu.Unlock()
		return nil
	}
	book, err := st.readPhotos(cur.ID)
	if err != nil {
		st.recMu.Unlock()
		return err
	}
	// A photo deleted while the model wrote them is cited by nothing stored (and they are made again: photoDelete).
	dropUnlistedPhotos(m, book)
	// The version after both meta's and the file's: a write of minutes.json that meta.json never caught up with (a
	// crash between the two) must not give different actions the version an app may still confirm.
	m.Version, m.Revision, m.GeneratedAt = cur.MinutesVersion+1, r.Revision, e.now().UTC()
	var before map[int]bool
	if old, err := st.ReadMinutes(cur.ID); err == nil {
		m.Version = max(m.Version, old.Version+1)
		keepTodos(m, old)
		before = minutesPhotos(old)
	}
	// Each photo's cited follows the minutes: PartPhotos' revision moves first, so that a failure leaves the minutes
	// pending (made again at the next round) and a crash between costs only a revision too many.
	photos := citedChanged(book, before, minutesPhotos(m))
	if photos {
		if err := st.bumpPhotosRev(cur.ID, cur.StartedAt); err != nil {
			st.recMu.Unlock()
			return fmt.Errorf("photos before the minutes: %w", err)
		}
	}
	// Attribute disagreement positions and render Markdown under recMu -> meetMu -> recFilesMu against the latest
	// speaker roster and timeline, so edits or a late timeline arriving during generation are never overwritten.
	wrote, err := func() (bool, error) {
		st.meetMu.Lock()
		defer st.meetMu.Unlock()
		st.recFilesMu.Lock()
		defer st.recFilesMu.Unlock()
		meeting, _ := st.readMeeting(cur.ID)
		speakers := meetingSpeakersOf(meeting)
		if cur.HubTranscribed() && speakers != nil && speakers.State == SpeakerStateReady {
			turns, _ := st.readSpeakerTurnsLocked(cur.ID)
			hub, _ := st.readHubLocked(cur.ID)
			e.attributeDebates(m, speakers, turns, hub.EffectiveTiming(), false)
		} else if speakers != nil && speakers.State == SpeakerStateCleared {
			clearDebateSpeakerData(m)
		} else {
			clearDebateAttribution(m)
		}
		m.Markdown = e.renderMinutes(m, r.StartedAt, st.Zone(), speakers)
		return st.putMinutesLocked(cur.ID, m)
	}()
	if err != nil || !wrote {
		st.recMu.Unlock()
		return err
	}
	quiet := quietMinutes(cur)
	cur.MinutesVersion = m.Version
	if m.ModelTitle != "" { // the minutes' title is the recording's from now on, unless someone named it (NameSource)
		cur.AITitle, cur.AITitleFrom = m.ModelTitle, AITitleMinutes
		cur.Name = e.recordingName(cur)
	}
	clearMinutesFailures(cur)
	if cur.MinutesAsks == r.MinutesAsks { // else asked again meanwhile (the user, a photo): made again at the next round
		cur.MinutesRev = r.Revision
	}
	planMinutes(cur)
	ready := cur.MinutesState == MinutesReady
	if ready {
		cur.MinutesQuiet = 0 // settled: minutes of a later revision or request are pushed again
	}
	drop := final && cur.MinutesAsks == r.MinutesAsks
	err = st.putEntity(KindRecording, cur.ID, cur, &cur.Tagging, false)
	st.notify(KindRecording, cur.ID, "summary")
	if err == nil && m.ModelTitle != "" {
		st.notify(KindRecording, cur.ID, "metadata")
		e.index(KindRecording, cur.ID, cur.Name, cur.Tagging, cur.OneLine, cur.StartedAt)
	}
	snap := *cur
	st.recMu.Unlock()
	if photos {
		e.meetingChanged(cur.ID, []string{PartPhotos})
	}
	if err != nil {
		return err
	}
	e.dropAudioAfterMinutes(cur.ID, drop)
	if ready && !quiet && e.MinutesWritten != nil {
		e.MinutesWritten(ctx, snap, *m)
	}
	return nil
}
