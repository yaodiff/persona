package kb

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

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Meeting minutes (docs/assistant/meeting-minutes.md): a complete recording that is a meeting (the phone said so, the
// user asked, or it overlaps a calendar item of the knowledge base) gets minutes.json (Minutes, what detail.minutes
// is) and minutes.md (the same as Markdown, what the searches and kb_get read) in its directory, so a delete takes them
// with it; the retention keeps them past the transcript, until minutes_days (recording_trim.go). The transcript goes to
// the model as numbered lines, the meeting's photos (recording_photos.go) as lines of their own among them, by their
// words only; the model cites line or photo numbers, which become seconds here, and an item citing no line or photo it
// read is dropped. Long transcripts are read in chunks whose notes one more call merges.

// Minutes is minutes.json, and detail.minutes of GET /api/v1/kb/recordings/{id}.
type Minutes struct {
	Version     int             `json:"version"`  // counts the minutes written for this recording
	Revision    int64           `json:"revision"` // the recording revision they were made from
	GeneratedAt time.Time       `json:"generated_at"`
	Title       string          `json:"title"`
	When        string          `json:"when"` // Start–End in one fixed format, for older clients and the Markdown
	Where       string          `json:"where,omitempty"`
	Attendees   []string        `json:"attendees,omitempty"`
	Points      []MinutesPoint  `json:"points,omitempty"`
	Decisions   []MinutesItem   `json:"decisions,omitempty"`
	Actions     []MinutesAction `json:"actions,omitempty"`
	Open        []MinutesItem   `json:"open,omitempty"`
	// Overview is the same minutes as one summary card (#6563, minutes_overview.go): only when every point and
	// decision is a bullet of one of its topics. Minutes from before it, or from an older hub, have none.
	Overview *MinutesOverview `json:"overview,omitempty"`
	Partial  bool             `json:"partial,omitempty"` // the transcript's end was not read
	// Language is what they are written in, their Markdown's headings too (language.go): an i18n code (zh-Hans, en,
	// ja); minutes written before it was kept have none, and are Chinese.
	Language string `json:"language,omitempty"`
	Markdown string `json:"markdown"`
	// Start and End are the recording's span When says, in the user's zone (#4713). Clients format them with their own
	// localized dates and show When only without them (minutes from an older hub).
	Start time.Time `json:"start,omitzero"`
	End   time.Time `json:"end,omitzero"`
}

// MinutesItem is a decision or an open question; At is seconds from the recording's start (a segment's start, or the
// photo's time when it cites photo Photo).
type MinutesItem struct {
	Text  string  `json:"text"`
	At    float64 `json:"at"`
	Photo int     `json:"photo,omitempty"`
}

// MinutesPoint is a point discussed; Who only when the transcript or the calendar names the person.
type MinutesPoint struct {
	Text  string  `json:"text"`
	At    float64 `json:"at"`
	Photo int     `json:"photo,omitempty"`
	Who   string  `json:"who,omitempty"`
}

// MinutesAction is an action item: a suggested to-do until the user confirms it (TodoID then).
type MinutesAction struct {
	ID      string  `json:"id"`
	Who     string  `json:"who,omitempty"`
	What    string  `json:"what"`
	By      string  `json:"by,omitempty"`       // the deadline as said
	DueDate string  `json:"due_date,omitempty"` // YYYY-MM-DD, when By is one date
	At      float64 `json:"at"`
	Photo   int     `json:"photo,omitempty"`
	TodoID  string  `json:"todo_id,omitempty"`
}

var (
	// ErrMinutesStale: the minutes were written again since the version a request names.
	ErrMinutesStale = errors.New("the minutes changed since; read them again")
	// ErrRecordingIncomplete: the transcript is not complete yet, so there is nothing to make minutes of.
	ErrRecordingIncomplete = errors.New("the transcript is not complete yet")
	// errMinutesUnusable: the model answered, but nothing in it cites the transcript (or a photo).
	errMinutesUnusable = errors.New("no item of the answer cites a transcript line")
)

const (
	minutesTries         = 3      // unusable answers for one revision before the minutes are failed
	minutesMinCalendar   = 5 * 60 // seconds: shorter recordings are not meetings just because a calendar item overlaps
	minutesCalendarAhead = 15 * time.Minute
	// minutesMatchWait: a recording no calendar item matched is looked at again until this long after it ended, the
	// calendar items being collected every half hour (a meeting's video call is listed once it has been held).
	minutesMatchWait   = time.Hour
	minutesOneCall     = 16000 // runes of transcript read in one call; longer ones go in chunks
	minutesChunk       = 12000
	minutesMaxChunks   = 40
	minutesCallTimeout = 6 * time.Minute  // a call's at the first try, twice a summary's (minutes_timeout.go)
	minutesJobTimeout  = 45 * time.Minute // all the calls of one recording: the knowledge-base round waits for them
	minutesMaxItems    = 100
	minutesTextRunes   = 400  // an item's text at most, the "…" of a cut included
	minutesLineRunes   = 4000 // a transcript line longer than this is cut (one segment, not the whole meeting)
	// What the merging call reads of all the parts' notes: per section, at most this many items over all parts (an
	// equal share each, at least 2), each at most minutesNoteRunes.
	minutesMergeItems = 60
	minutesNoteRunes  = 160
)

// isMeeting: the recording gets minutes. A day recording never does (it gets a recap, day_recap.go).
func (r *Recording) isMeeting() bool {
	if r.Kind == RecordingDay {
		return false
	}
	return r.Kind == RecordingMeeting || r.MinutesAsked || r.Meeting != nil && r.Duration >= minutesMinCalendar
}

// planMinutes sets MinutesState from the rest: pending until minutes of the current revision are written, failed after
// minutesTries unusable answers for it or minutesTimeoutTries tries that ran out of time; minutes of an older revision
// keep showing meanwhile. A meeting that completed with no text gets none ("", docs/assistant/meeting-minutes.md
// "Attendance fields"): an attended one is uploaded before anything was said, and may end so.
func (r *Recording) planMinutes() {
	switch {
	case !r.isMeeting():
		r.MinutesState = "" // minutes made while it was one stay readable; nothing more is made
	case r.Trimmed():
		r.MinutesState = MinutesReady // the transcript is gone: the minutes kept are all there will be
	case r.Complete && r.Chars == 0:
		r.MinutesState = ""
	case r.MinutesRev > 0 && r.MinutesRev >= r.Revision:
		r.MinutesState = MinutesReady
	case r.MinutesFailRev == r.Revision && (r.MinutesFails >= minutesTries || r.MinutesTimeouts >= minutesTimeoutTries):
		r.MinutesState = MinutesFailed
	default:
		r.MinutesState = MinutesPending
	}
}

// ReadMinutes is recording id's minutes, ErrNotFound when it has none (or there is no such recording).
func (s *Store) ReadMinutes(id string) (*Minutes, error) {
	id, ok := RecordingID(id)
	if !ok {
		return nil, ErrNotFound
	}
	raw, err := os.ReadFile(s.path(KindRecording, id, "minutes.json"))
	if err != nil {
		return nil, ErrNotFound
	}
	var m Minutes
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// putMinutes writes minutes.json and minutes.md of recording id, unless it was deleted meanwhile (false then). The
// caller holds recMu.
func (s *Store) putMinutes(id string, m *Minutes) (bool, error) {
	if s.readOnly {
		return false, ErrReadOnly
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	if err := s.recoverRecordingLocked(id); err != nil {
		return false, err
	}
	s.metaMu.Lock()
	defer s.metaMu.Unlock()
	// writeFile makes missing directories: a recording deleted meanwhile must stay gone.
	if _, err := os.Stat(s.path(KindRecording, id, "meta.json")); err != nil {
		return false, nil
	}
	if err := writeFile(s.path(KindRecording, id, "minutes.json"), append(raw, '\n')); err != nil {
		return false, err
	}
	if err := writeFile(s.path(KindRecording, id, "minutes.md"), []byte(strings.TrimSpace(m.Markdown)+"\n")); err != nil {
		return false, err
	}
	return true, nil
}

// AskMinutes has recording id's minutes made (again) at the next round, whatever its kind: ErrNotFound when there is
// no such recording, ErrTranscriptExpired once it was trimmed, ErrRecordingIncomplete before its transcript is
// complete, and also for a complete one with no text (409 as well: there is nothing to make minutes of, and its
// minutes_state stays absent). A day recording gets its recap instead (askDayRecap): it never becomes a meeting.
func (e *Engine) AskMinutes(rawID string) error {
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
	r.remakeMinutes()
	return st.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
}

// remakeMinutes has r's minutes made again at the next round (the caller holds recMu and writes r): minutes a model
// call running now brings don't settle it (MinutesAsks), and the minutes kept show meanwhile.
func (r *Recording) remakeMinutes() {
	r.MinutesRev = 0
	r.clearMinutesFailures()
	r.MinutesAsks++
	r.planMinutes()
}

// quietMinutes: the minutes being made now are made again only because a photo changed: not pushed once written
// (photosRemake).
func (r *Recording) quietMinutes() bool { return r.MinutesQuiet > 0 && r.MinutesQuiet == r.MinutesAsks }

// SetMinutesTodos records the to-dos made of actions (action id → to-do id) in version of recording id's minutes;
// ErrNotFound without minutes. Minutes written again since get them by task instead (byTask: MinutesTaskKey → to-do
// id), on their actions without one; without byTask that is ErrMinutesStale.
func (e *Engine) SetMinutesTodos(rawID string, version int, todos, byTask map[string]string) (*Minutes, error) {
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
func (e *Engine) WriteMinutes(ctx context.Context, max int) int {
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
		// In hub mode they wait for the final transcript, at most minutesHubWait after the complete upload.
		if r.Complete && r.Chars > 0 && r.MinutesState == MinutesPending && !r.minutesWaiting(now) && !r.Trimmed() &&
			!r.transcriptionHolds(now) {
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
func (e *Engine) minutesFailed(id string, rev int64) {
	st := e.Store
	st.recMu.Lock()
	r, ok := st.Recording(id)
	if !ok || r.Revision != rev {
		st.recMu.Unlock()
		return
	}
	r.minutesFailuresFor(rev)
	r.MinutesFails++
	r.planMinutes()
	drop := r.MinutesState == MinutesFailed && r.transcriptionDone()
	_ = st.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
	st.recMu.Unlock()
	e.dropAudioAfterMinutes(id, drop)
}

// dropAudioAfterMinutes deletes recording id's hub-mode audio when drop (the minutes from its final transcript are
// written, or ended failed). The caller holds none of the store's locks.
func (e *Engine) dropAudioAfterMinutes(id string, drop bool) {
	if !drop {
		return
	}
	if err := e.dropAudio(id, ErrTranscriptionFinal); err != nil {
		e.logf("recording %s: audio after the minutes: %v", id, err)
	}
}

// matchRecordings links each complete recording not matched at its current revision to the calendar item it overlaps;
// one that none overlaps is looked at again until minutesMatchWait after it ended.
func (e *Engine) matchRecordings(now time.Time) {
	var due []string
	for _, r := range e.Store.Recordings() {
		if r.Complete && r.MatchedRev < r.Revision && !r.Trimmed() && r.Kind != RecordingDay {
			due = append(due, r.ID)
		}
	}
	if len(due) == 0 {
		return
	}
	events, meetings := e.Store.Items(KindEvent), e.Store.Items(KindMeeting)
	st := e.Store
	for _, id := range due {
		st.recMu.Lock()
		r, ok := st.Recording(id)
		if ok && r.Complete && r.MatchedRev < r.Revision && !r.Trimmed() && r.Kind != RecordingDay {
			if old := r.Meeting; old != nil {
				r.Items = removeStrings(r.Items, []string{old.Kind + "/" + old.ID})
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
			r.MatchedRev = r.Revision
			r.Name = st.recordingName(r)
			r.planMinutes()
			if st.putEntity(KindRecording, r.ID, r, &r.Tagging, false) == nil {
				e.index(KindRecording, r.ID, r.Name, r.Tagging, r.OneLine, r.StartedAt)
			}
		}
		st.recMu.Unlock()
	}
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

// recordingEnd is when r ended: ended_at, else its start plus what was recorded (at least a minute).
func recordingEnd(r *Recording) time.Time {
	end := r.StartedAt.Add(time.Duration(r.Duration * float64(time.Second)))
	if r.EndedAt != nil && r.EndedAt.After(end) {
		end = *r.EndedAt
	}
	if !end.After(r.StartedAt.Add(time.Minute)) {
		end = r.StartedAt.Add(time.Minute)
	}
	return end
}

// minutesLine is a line as the model reads it: a transcript line, N what it cites, or a photo's (Photo its number, N
// 0).
type minutesLine struct {
	N     int
	Photo int
	At    float64
	Text  string
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
func (e *Engine) writeMinutes(ctx context.Context, id string) error {
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
	if !r.Complete || r.Chars == 0 || r.MinutesState != MinutesPending || r.Trimmed() || r.transcriptionHolds(e.now()) {
		return nil
	}
	var device []RecordingSegment
	if err := json.Unmarshal(files["segments.json"], &device); err != nil {
		return err
	}
	segs := TranscriptOfRecord(hubMode(meeting), device, hub.Segments)
	// Minutes made from the final transcript take the hub's audio with them once written (storeMinutes).
	final := r.transcriptionDone()
	zone := e.Store.Zone()
	lines := make([]minutesLine, 0, len(segs))
	for i, sg := range segs {
		if sg.Echo {
			continue // Persona's own answer, heard by the microphone: not the meeting's (N still counts it: citations index segs)
		}
		if t := strings.Join(strings.Fields(sg.Text), " "); t != "" {
			lines = append(lines, minutesLine{N: i + 1, At: sg.Start, Text: clip(t, minutesLineRunes)})
		}
	}
	// The photos with words, among the lines in time order (after the lines of the same second). A photo added or
	// deleted from now on makes them again (photosRemake), and one deleted is cited by nothing stored
	// (storeMinutes).
	for _, p := range e.PhotoTexts(id) {
		if t := photoWords(p, true); t != "" {
			lines = append(lines, minutesLine{Photo: p.N, At: p.At, Text: t})
		}
	}
	slices.SortStableFunc(lines, func(a, b minutesLine) int { return cmp.Compare(a.At, b.At) })
	chunks, partial := chunkLines(lines, cmp.Or(e.minutesCall, minutesOneCall), cmp.Or(e.minutesPart, minutesChunk), minutesMaxChunks)
	if len(chunks) == 0 {
		return nil
	}
	in := e.minutesInput(&r, zone)
	model := e.modelFor("kb.compress")
	callTime := minutesCallTime(r.minutesTimeouts())
	call := func(system, user string, out *minutesAnswer) error {
		cctx, cancel := context.WithTimeout(ctx, callTime)
		defer cancel()
		return model.JSONMax(cctx, system, user, 6000, out)
	}
	lang := e.outputLang(&r)
	owner := e.writingOwner(lang)
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
			merge.Parts = append(merge.Parts, notesForMerge(i+1, notes, c, r.StartedAt, zone, max(2, minutesMergeItems/len(chunks))))
		}
		merge.Meeting.Transcript = ""
		err := call(prompts.RenderKBMinutesMerge(owner), prompts.RenderKBMinutesMergeInput(merge), &ans)
		if err != nil {
			return fmt.Errorf("merging %d parts: %w", len(chunks), err)
		}
		read = notedLines(read, merge.Parts) // the merging call saw the notes, not the transcript
	}
	m, err := e.buildMinutes(&r, ans, read, zone, lang)
	if err != nil {
		return err
	}
	m.Partial = partial
	return e.storeMinutes(ctx, &r, m, final)
}

// minutesInput is what the model is told about recording r besides its transcript.
func (e *Engine) minutesInput(r *Recording, zone *time.Location) prompts.KBMinutesInput {
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

// calendarTitle is the phone calendar's title of r, else the matched calendar item's ("" without either).
func calendarTitle(r *Recording) string {
	if r.Calendar != nil && r.Calendar.Title != "" {
		return r.Calendar.Title
	}
	if r.Meeting != nil {
		return r.Meeting.Name
	}
	return ""
}

// chunkLines cuts lines into chunks of about size runes at line boundaries: one chunk when all of them fit in one,
// at most max chunks (partial: lines were left out).
func chunkLines(lines []minutesLine, one, size, max int) (chunks [][]minutesLine, partial bool) {
	total := 0
	for _, l := range lines {
		total += len([]rune(l.Text)) + 16
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
		w := len([]rune(l.Text)) + 16
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

// renderMinutesLines is the transcript as the model reads it: "[N] HH:MM:SS text" per line in the user's zone, a
// photo's line as photos.md has it (photoLine).
func renderMinutesLines(lines []minutesLine, start time.Time, zone *time.Location) string {
	var b strings.Builder
	for _, l := range lines {
		if l.Photo > 0 {
			b.WriteString(photoLine(l.Photo, clockAt(start, l.At, zone), l.Text) + "\n")
			continue
		}
		fmt.Fprintf(&b, "[%d] %s %s\n", l.N, clockAt(start, l.At, zone), l.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

func clockAt(start time.Time, sec float64, zone *time.Location) string {
	return start.Add(time.Duration(sec * float64(time.Second))).In(zone).Format("15:04:05")
}

// notesForMerge is a chunk's notes for the merging call, keeping only items citing a line or a photo of that chunk,
// each without the model's marks (the merging call marks them again) and clipped to minutesNoteRunes. Every point and
// decision is kept, in a topic or not; one in a titled topic carries its title (#6563). Each section is put in the
// order of what its items cite, then cut to each: a part's latest lines go, not whole topics. Its headline and
// conclusions are not read: the merging call writes the meeting's own.
func notesForMerge(part int, a minutesAnswer, chunk []minutesLine, start time.Time, zone *time.Location, each int) prompts.KBMinutesPart {
	lines, photos := map[int]int{}, map[int]int{} // what the chunk has, by its place in it
	for i, l := range chunk {
		if l.Photo > 0 {
			photos[l.Photo] = i
		} else {
			lines[l.N] = i
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

// clipRunes is s on one line, at most n runes ("…" marking a cut).
func clipRunes(s string, n int) string { return clip(strings.Join(strings.Fields(s), " "), n) }

// buildMinutes checks the model's answer against the lines it read: every item must cite one of them (a transcript
// line, or a photo: its time then), a person must be named in them or in the calendar, a due date must be a real day
// from the meeting on (within a year). A photo's words count as said. The minutes are in lang.
func (e *Engine) buildMinutes(r *Recording, a minutesAnswer, read []minutesLine, zone *time.Location, lang i18n.Lang) (*Minutes, error) {
	at, photoSec := map[int]float64{}, map[int]float64{}
	var saidB strings.Builder
	for _, l := range read {
		if l.Photo > 0 {
			photoSec[l.Photo] = l.At
		} else {
			at[l.N] = l.At
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
		When: minutesWhen(r, zone), Language: string(lang)}
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
				points = append(points, placed[MinutesPoint]{MinutesPoint{Text: t, At: sec, Photo: photo, Who: named(x.Who)}, topic, strong})
			}
		}
	}
	decision := func(x answerItem, topic int) {
		if t, strong := strongText(x.Text, minutesTextRunes); t != "" {
			if sec, photo, ok := line(x.Line, x.Photo); ok && fresh("d", t) {
				decisions = append(decisions, placed[MinutesItem]{MinutesItem{Text: t, At: sec, Photo: photo}, topic, strong})
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

// minutesSpan is the recording's start and end in the user's zone, to the second: the span minutes are about.
func minutesSpan(r *Recording, zone *time.Location) (time.Time, time.Time) {
	return r.StartedAt.In(zone).Truncate(time.Second), recordingEnd(r).In(zone).Truncate(time.Second)
}

// spanOf fills Start and End of minutes written before they were (#4713) from their recording.
func (m *Minutes) spanOf(r *Recording, zone *time.Location) {
	if m.Start.IsZero() && r != nil && !r.StartedAt.IsZero() {
		m.Start, m.End = minutesSpan(r, zone)
	}
}

// minutesWithSpan is minutes.json as a recording's detail sends it: minutes written before Start and End were get them
// from the recording (spanOf), every other field as stored.
func minutesWithSpan(raw []byte, r *Recording, zone *time.Location) []byte {
	var stored struct {
		Start time.Time `json:"start"`
	}
	if json.Unmarshal(raw, &stored) != nil || !stored.Start.IsZero() || r == nil || r.StartedAt.IsZero() {
		return raw
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	start, end := minutesSpan(r, zone)
	fields["start"], _ = json.Marshal(start)
	fields["end"], _ = json.Marshal(end)
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
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

// MinutesTaskKey is what makes an action the same task in minutes made again: who and what.
func MinutesTaskKey(a MinutesAction) string { return a.Who + "\x00" + a.What }

// renderMinutes is minutes.md: the minutes with clock times, sections without anything left out; an item citing a
// photo ends with its number. Its headings and fixed words are in the minutes' language (kb-minutes.json). Minutes
// with an overview follow it (renderOverview).
func renderMinutes(m *Minutes, start time.Time, zone *time.Location) string {
	if m.Overview != nil {
		return renderOverview(m, start, zone)
	}
	w := newMinutesMarkdown(m, start, zone, func(s string) string { return s })
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
	w.tail(m)
	return strings.TrimRight(b.String(), "\n")
}

// renderOverview is minutes.md of minutes with an overview (docs/assistant/meeting-minutes.md "Markdown"): the
// headline, the core conclusions, then each group's topics (a topic a ## heading with one group, a ### under its
// group's ## with several), each decision after "Decided:", then the action items and open questions as without one,
// and the AI note last. A literal * is escaped, and the bold places are wrapped in **.
func renderOverview(m *Minutes, start time.Time, zone *time.Location) string {
	o := m.Overview
	w := newMinutesMarkdown(m, start, zone, escapeStars)
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
	w.tail(m)
	b.WriteString("\n*" + i18n.T(l, "kb-minutes.ai_note") + "*\n")
	return strings.TrimRight(b.String(), "\n")
}

// minutesMarkdown writes minutes.md: esc is what every text the minutes hold goes through (the overview's layout
// escapes a literal *, the legacy one writes them as they are).
type minutesMarkdown struct {
	b     strings.Builder
	l     i18n.Lang
	start time.Time
	zone  *time.Location
	esc   func(string) string
}

func newMinutesMarkdown(m *Minutes, start time.Time, zone *time.Location, esc func(string) string) *minutesMarkdown {
	return &minutesMarkdown{l: storedLang(m.Language), start: start, zone: zone, esc: esc}
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

// tail is the action items, the open questions and the partial note.
func (w *minutesMarkdown) tail(m *Minutes) {
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
func (e *Engine) storeMinutes(ctx context.Context, r *Recording, m *Minutes, final bool) error {
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
	m.Markdown = renderMinutes(m, r.StartedAt, st.Zone())
	// Each photo's cited follows the minutes: PartPhotos' revision moves first, so that a failure leaves the minutes
	// pending (made again at the next round) and a crash between costs only a revision too many.
	photos := citedChanged(book, before, minutesPhotos(m))
	if photos {
		if err := st.bumpPhotosRev(cur.ID, cur.StartedAt); err != nil {
			st.recMu.Unlock()
			return fmt.Errorf("photos before the minutes: %w", err)
		}
	}
	if wrote, err := st.putMinutes(cur.ID, m); err != nil || !wrote {
		st.recMu.Unlock()
		return err
	}
	quiet := cur.quietMinutes()
	cur.MinutesVersion = m.Version
	cur.clearMinutesFailures()
	if cur.MinutesAsks == r.MinutesAsks { // else asked again meanwhile (the user, a photo): made again at the next round
		cur.MinutesRev = r.Revision
	}
	cur.planMinutes()
	ready := cur.MinutesState == MinutesReady
	if ready {
		cur.MinutesQuiet = 0 // settled: minutes of a later revision or request are pushed again
	}
	drop := final && cur.MinutesAsks == r.MinutesAsks
	err = st.putEntity(KindRecording, cur.ID, cur, &cur.Tagging, false)
	st.notify(KindRecording, cur.ID, "summary")
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
