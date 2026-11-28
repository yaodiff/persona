package writers

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Writers is the recordings' write side of one knowledge base (recordingwire.Provider): the writers of this package
// over the storage port (the upload and a day's appends, the live log, meeting.json, the minutes and day recaps, the
// titles, the photo intake, the audio, the hub's transcript), and the one round that is the knowledge base's own code
// (recordingwire.WritersInCore: the summary round). The module makes one in its hook and gives it to the knowledge base.
type Writers struct {
	core recordingwire.Core
	// rest is the round that is the knowledge base's own code: the summaries'.
	rest recordingwire.WritersInCore
	// audio is the audio writers' state of the store behind core: the one the store's writers before these had (a
	// second engine over the same store), else new.
	state *storeState
	// liveChanged is told of a recording whose live log or stored upload changed (OnLive): the attending feeder.
	liveChanged func(id string)
	// speakersChanged is told after a speaker timeline import or speaker edit succeeds (OnSpeakersChanged): hook point
	// for minutes attribution re-render and waking the minutes round (PR 2-4).
	speakersChanged func(id string)
	// minutesWritten is told when a recording's minutes for its current revision are written: the assistant pushes
	// them. Not for minutes made again only because a photo was added or deleted. Called from the round, outside the
	// store's locks. dayRecapWritten is told once a day (Recording.Day, across the day's parts) of the recap the day's
	// push goes with: it pushes to the destinations not in r.RecapPushTook and returns those that took it now; an
	// error leaves the push due (OnWritten sets both).
	minutesWritten  func(ctx context.Context, r Recording, m Minutes)
	dayRecapWritten func(ctx context.Context, r Recording, d DayRecap) (took []string, err error)

	// What tests set on the engine a call makes (engine.jobLeft, minutesCall, minutesPart).
	jobLeft                  func(ctx context.Context) time.Duration
	minutesCall, minutesPart int
}

// New is the write side of the knowledge base behind core; rest is the round that is its own code, the summaries'
// (nil: none, for a knowledge base that only answers requests).
func New(core recordingwire.Core, rest recordingwire.WritersInCore) *Writers {
	w := &Writers{core: core, rest: rest, state: &storeState{}}
	if before := Of(core.Provider()); before != nil {
		w.state = before.state
	}
	return w
}

// OnWritten sets what is told when minutes are written and when a day's recap is to be pushed, before the knowledge
// base is used.
func (w *Writers) OnWritten(minutes func(ctx context.Context, r Recording, m Minutes),
	dayRecap func(ctx context.Context, r Recording, d DayRecap) (took []string, err error)) {
	w.minutesWritten, w.dayRecapWritten = minutes, dayRecap
}

// OnLive sets what is told of a recording whose live log or stored upload changed, before the knowledge base is used.
func (w *Writers) OnLive(changed func(id string)) { w.liveChanged = changed }

// OnSpeakersChanged sets the post-commit hook called after a speaker timeline is imported or speaker edits are saved
// (hook point for PR 2-4 minutes re-render and minutes wake).
func (w *Writers) OnSpeakersChanged(changed func(id string)) { w.speakersChanged = changed }

// Of is the Writers a knowledge base was given as its provider; nil when p is another provider (no module).
func Of(p recordingwire.Provider) *Writers {
	w, _ := p.(*Writers)
	return w
}

// engine is the knowledge base's engine as the writers see it now: its models and evening clock as they stand, the
// module's callbacks.
func (w *Writers) engine() *engine {
	e := &engine{Store: w.store(), MinutesWritten: w.minutesWritten, DayRecapWritten: w.dayRecapWritten,
		jobLeft: w.jobLeft, minutesCall: w.minutesCall, minutesPart: w.minutesPart,
		LiveChanged: w.liveChanged, SpeakersChanged: w.speakersChanged, TranscriptionReady: w.core.TranscriptionReady, EarlierMeetingsInAnswers: w.core.EarlierMeetings()}
	if w.core.HasModel() {
		e.ModelFor = func(purpose string) Model { return w.core.Model(known(recordingwire.Purpose(purpose))) }
	}
	if _, ok := w.core.DayRecapClock(); ok {
		e.DayRecapClock = func() (hour, minute int) {
			c, _ := w.core.DayRecapClock()
			return c.Hour, c.Minute
		}
	}
	return e
}

// store is the knowledge base's store as the writers see it now.
func (w *Writers) store() *store { return newStore(w.core, w.state) }

// The rounds (recordingwire.Provider): the summaries' is the knowledge base's own code; without it (a knowledge base
// that only answers requests) it writes nothing.

func (w *Writers) WriteMinutes(ctx context.Context, max int) int {
	return w.engine().WriteMinutes(ctx, max)
}
func (w *Writers) WriteDayRecaps(ctx context.Context, max int) int {
	return w.engine().WriteDayRecaps(ctx, max)
}

// TellDayRecap tells the module of recap d of day recording r as a round does for a push that is due
// (OnWritten): the push ids it took, and an error while some destination has not taken it.
func (w *Writers) TellDayRecap(ctx context.Context, r Recording, d DayRecap) ([]string, error) {
	return w.dayRecapWritten(ctx, r, d)
}

func (w *Writers) TitleRecordings(ctx context.Context, max int) int {
	return w.engine().TitleRecordings(ctx, max)
}
func (w *Writers) SummarizeRecordings(ctx context.Context, max int) int {
	if w.rest == nil {
		return 0
	}
	return w.rest.SummarizeRecordings(ctx, max)
}

// AfterPrune is the sweep of hub transcripts; AudioDropped and AudioClosed the audio writers' touch points with the
// knowledge base's delete, retention and trim (recordingwire.Provider).
func (w *Writers) AfterPrune(time.Time) { w.engine().sweepTranscriptions() }
func (w *Writers) AudioDropped(id recordingwire.ID, why error) {
	w.store().audioDroppedLocked(string(id), why)
}
func (w *Writers) AudioClosed(id recordingwire.ID) { w.store().audioFileClosed(string(id)) }

// The requests of the module's routes.

// AskMinutes has recording id's minutes (a day recording's recap) made again at the next round.
func (w *Writers) AskMinutes(id string) error { return w.engine().AskMinutes(id) }

// SetMinutesTodos records the to-dos made of the actions of version of recording id's minutes.
func (w *Writers) SetMinutesTodos(id string, version int, todos, byTask map[string]string) (*Minutes, error) {
	return w.engine().SetMinutesTodos(id, version, todos, byTask)
}

// SetDayRecapTodos records the to-dos made of the actions of version of day recording id's recap.
func (w *Writers) SetDayRecapTodos(id string, version int, confirmed []MinutesAction) (*DayRecap, error) {
	return w.engine().SetDayRecapTodos(id, version, confirmed)
}

// The requests of the module's routes and jobs about photos, titles, audio and the hub's transcript: each is the
// writer of the same name (recording_photos.go, recording_title.go, recording_audio.go, transcription.go,
// hub_segments.go).

func (w *Writers) PhotoAdd(rawID string, u PhotoUpload, queue bool) (Photo, bool, error) {
	return w.engine().PhotoAdd(rawID, u, queue)
}
func (w *Writers) Photos(rawID string) ([]Photo, int64, error) { return w.engine().Photos(rawID) }
func (w *Writers) PhotosRev(rawID string) int64                { return w.engine().PhotosRev(rawID) }
func (w *Writers) PhotoDelete(rawID string, n int) (bool, error) {
	return w.engine().PhotoDelete(rawID, n)
}
func (w *Writers) PhotosForRoom(rawID string) (queued []Photo, withdraw []PhotoRef) {
	return w.engine().PhotosForRoom(rawID)
}
func (w *Writers) PhotoSending(rawID string, n int) error { return w.engine().PhotoSending(rawID, n) }
func (w *Writers) PhotoForwarded(rawID string, n int) error {
	return w.engine().PhotoForwarded(rawID, n)
}
func (w *Writers) PhotoWithdrawn(rawID string, n int) error {
	return w.engine().PhotoWithdrawn(rawID, n)
}
func (w *Writers) PhotoNote(rawID string, n int, text string) error {
	return w.engine().PhotoNote(rawID, n, text)
}
func (w *Writers) RenameRecording(rawID, title string, now time.Time) (*Recording, error) {
	return w.engine().RenameRecording(rawID, title, now)
}
func (w *Writers) AudioOpen(rawID string) (*AudioWriter, error) { return w.engine().AudioOpen(rawID) }
func (w *Writers) AudioHeld(rawID string) ([][2]float64, error) { return w.engine().AudioHeld(rawID) }
func (w *Writers) AudioPut(rawID string, at float64, pcm []byte) ([][2]float64, error) {
	return w.engine().AudioPut(rawID, at, pcm)
}
func (w *Writers) AudioRead(rawID string, from float64, p []byte) (int, error) {
	return w.engine().AudioRead(rawID, from, p)
}
func (w *Writers) AudioDelete(rawID string) error { return w.engine().AudioDelete(rawID) }
func (w *Writers) FinalTranscriptStored(rawID string) error {
	return w.engine().FinalTranscriptStored(rawID)
}
func (w *Writers) TranscriptionUpdate(rawID string, fn func(t *MeetingTranscription, r Recording) error) (MeetingTranscription, error) {
	return w.engine().TranscriptionUpdate(rawID, fn)
}
func (w *Writers) HubRecordings() ([]string, error) { return w.engine().HubRecordings() }
func (w *Writers) HubTranscriptPut(rawID, pass string, segs []RecordingSegment) (rev int64, err error) {
	return w.engine().HubTranscriptPut(rawID, pass, segs)
}
func (w *Writers) HubTranscriptPutWithTiming(rawID, pass string, timing TranscriptTiming, segs []RecordingSegment) (rev int64, err error) {
	return w.engine().HubTranscriptPutWithTiming(rawID, pass, timing, segs)
}
func (w *Writers) EditSpeakers(rawID string, rev int64, ops []SpeakerEditOp, ownerName string) ([]SpeakerEditOp, error) {
	return w.engine().EditSpeakers(rawID, rev, ops, ownerName)
}
func (w *Writers) ImportSpeakerTurns(rawID string, req SpeakerTurnsImportRequest, now time.Time) (bool, error) {
	return w.engine().ImportSpeakerTurns(rawID, req, now)
}
func (w *Writers) RecordOf(rawID string) ([]RecordingSegment, error) {
	return w.engine().RecordOf(rawID)
}

var _ recordingwire.Provider = (*Writers)(nil)

// The requests of the module's routes and jobs about the upload, a day's appends, the live log, meeting.json and the
// material of an answer: each is the writer of the same name (recordings.go, recording_day.go, live.go, meeting.go,
// meeting_material.go).

func (w *Writers) PutRecording(rawID string, u RecordingUpload, now time.Time) (RecordingPut, error) {
	return w.engine().PutRecording(rawID, u, now)
}
func (w *Writers) AppendRecording(rawID string, a RecordingAppend) (RecordingAppended, error) {
	return w.engine().AppendRecording(rawID, a)
}
func (w *Writers) RecordingStates() (kept []RecordingState, deleted []string) {
	return w.engine().RecordingStates()
}

func (w *Writers) LiveAppend(rawID string, from int, segs []LiveSegment) (Live, Recording, error) {
	return w.engine().LiveAppend(rawID, from, segs)
}
func (w *Writers) LiveAppendHub(rawID string, segs []LiveSegment) (first int, err error) {
	return w.engine().LiveAppendHub(rawID, segs)
}
func (w *Writers) LiveRead(rawID string) (Live, Recording, error) { return w.engine().LiveRead(rawID) }
func (w *Writers) LiveUpdate(rawID string, fn func(l *Live, r Recording) error) (Live, error) {
	return w.engine().LiveUpdate(rawID, fn)
}
func (w *Writers) TopicsSet(rawID string, topics []LiveTopic, now time.Time) (bool, error) {
	return w.engine().TopicsSet(rawID, topics, now)
}
func (w *Writers) HighlightsSet(rawID string, said []HighlightSaid, loc *time.Location, now time.Time) (bool, error) {
	return w.engine().HighlightsSet(rawID, said, loc, now)
}
func (w *Writers) AttendingRecordings() []Recording { return w.engine().AttendingRecordings() }
func (w *Writers) MeetingRead(rawID string) (Meeting, Recording, error) {
	return w.engine().MeetingRead(rawID)
}
func (w *Writers) MeetingUpdate(rawID string, fn func(m *Meeting, r Recording) error) (Meeting, error) {
	return w.engine().MeetingUpdate(rawID, fn)
}
func (w *Writers) SetPrivate(rawID string, private bool) (Meeting, error) {
	return w.engine().SetPrivate(rawID, private)
}
func (w *Writers) RoomMeetings() []Recording { return w.engine().RoomMeetings() }
func (w *Writers) MeetingMaterial(rawID, question string) (MeetingMaterial, error) {
	return w.engine().MeetingMaterial(rawID, question)
}
func (w *Writers) EarlierMaterial(rawID, query string) ([]EarlierMeeting, error) {
	return w.engine().EarlierMaterial(rawID, query)
}

// HasMinutes: recording id has minutes that can be read.
func (w *Writers) HasMinutes(id string) bool { return w.store().HasMinutes(id) }

// CleanUpload checks upload u as PutRecording does and puts its segments in order (empty ones dropped, Persona's echo
// marked): what the upload of a recording saved into a track runs before it writes (the core's track recordings take
// it from the module's route, recordinghost.Host.PutSpaceRecordingRoute).
func CleanUpload(u *RecordingUpload) error { return cleanUpload(u) }
