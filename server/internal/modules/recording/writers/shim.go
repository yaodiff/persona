package writers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"github.com/nwverse/persona/server/internal/textfmt"
	"golang.org/x/text/unicode/norm"
)

// The writers were written as methods of the knowledge base's Engine and Store. Here those two are this package's
// own engine and store: they hold the storage port (recordingwire.Core) and give the writers every member they used
// under the name it had, each a call of the port (a few small readers of the knowledge base that are plain
// arithmetic on what the port gave are here under their names too, each saying where its original is). So the writers' bodies are the ones that were in
// internal/kb, and this file is the whole of the adaptation: what a name means here is read below, not in the body.

// engine is the knowledge base's engine as the writers see it: the port behind Store, the models and the evening
// clock as the port has them when it is made (Writers.engine makes one a call), and the module's callbacks.
type engine struct {
	Store *store
	// ModelFor is the engine's model for a purpose (recordingwire.Writer.Model), nil when the engine has no model at
	// all; Model is always nil here (the writers ask "is there any model" of the two together).
	Model    Model
	ModelFor func(string) Model
	// MinutesWritten is told when a recording's minutes for its current revision are written; DayRecapWritten once a
	// day of the recap the day's push goes with (took: the push ids it took; an error leaves the push due).
	MinutesWritten  func(ctx context.Context, r Recording, m Minutes)
	DayRecapWritten func(ctx context.Context, r Recording, d DayRecap) (took []string, err error)
	// DayRecapClock is the evening routine's time of day (recordingwire.Writer.DayRecapClock).
	DayRecapClock func() (hour, minute int)
	// LiveChanged is told of a recording whose live log or stored upload changed, for the attending feeder (the
	// module's, OnLive); TranscriptionReady whether the hub can transcribe a meeting now and EarlierMeetingsInAnswers
	// the owner's setting, as the port has them (recordingwire.Reader).
	LiveChanged              func(id string)
	SpeakersChanged          func(id string)
	TranscriptionReady       func() bool
	EarlierMeetingsInAnswers bool

	// jobLeft is what a job has left of its time (tests); nil: ctx's deadline.
	jobLeft func(ctx context.Context) time.Duration
	// minutesCall and minutesPart are the runes of transcript one minutes call and one part read (tests); 0:
	// minutesOneCall and minutesChunk.
	minutesCall, minutesPart int
}

// store is the knowledge base's store as the writers see it: the port, the store's locks under their names, and the
// audio writers' state, which is this module's and one per store.
type store struct {
	core     recordingwire.Core
	readOnly bool
	// recMu guards a recording's read-compare-write, meetMu meeting.json's read-modify-write, recFilesMu its files
	// and the commit recovery, metaMu every entry's meta.json: the store's own locks (recordingwire.Writer.Locks),
	// acquired in this order. The live log's lock (liveMu) and then a recording's audio mutex (audioRec.mu), both this
	// module's (storeState), come after meetMu and before recFilesMu.
	recMu, meetMu, metaMu sync.Locker
	recFilesMu            filesLock
	*storeState
}

// filesLock is the store's recording files lock: held for writing as a sync.Locker, for reading through RLock (an
// audio append). Both are plain calls of the store's sync.RWMutex.
type filesLock struct {
	sync.Locker
	read sync.Locker
}

func (l filesLock) RLock()   { l.read.Lock() }
func (l filesLock) RUnlock() { l.read.Unlock() }

// storeState is what the writers of every engine over one store share while this process runs: the live log's lock
// and the audio writers' state. liveMu guards a live log's read-modify-write (live.go); it comes after meetMu and
// before a recording's audio mutex and recFilesMu, as it did while it was the store's own field. Nothing of the
// knowledge base takes it: its retention removes live.json under recFilesMu alone.
type storeState struct {
	liveMu sync.Mutex
	audioState
}

// audioState is what the audio writers know of a store's recordings while this process runs (recording_audio.go):
// audioMapMu guards audioRecs, a leaf lock; each recording's own mutex comes after the store's meeting lock and
// before its files lock. It is one per store: the writers of every engine over a store share it (Writers.audio), so
// that a delete, the trim and retention reach the state an open socket writes through.
type audioState struct {
	audioMapMu sync.Mutex
	audioRecs  map[string]*audioRec
}

func newStore(core recordingwire.Core, state *storeState) *store {
	l := core.Locks()
	return &store{core: core, readOnly: core.ReadOnly(), recMu: l.Recordings, meetMu: l.Meeting, metaMu: l.Meta,
		recFilesMu: filesLock{Locker: l.Files, read: l.FilesRead}, storeState: state}
}

// The types the writers name, under the names they had.
type (
	Model                      = recordingwire.Model
	Tagging                    = kbwire.Tagging
	RecordingSegment           = kbwire.RecordingSegment
	Recording                  = recordingwire.Recording
	RecapTarget                = recordingwire.RecapTarget
	RecapSpan                  = recordingwire.RecapSpan
	RecapTodo                  = recordingwire.RecapTodo
	DayRecap                   = recordingwire.DayRecap
	DayRecapEntry              = recordingwire.DayRecapEntry
	Minutes                    = recordingwire.Minutes
	MinutesItem                = recordingwire.MinutesItem
	MinutesPoint               = recordingwire.MinutesPoint
	MinutesAction              = recordingwire.MinutesAction
	Meeting                    = recordingwire.Meeting
	MeetingTranscription       = recordingwire.MeetingTranscription
	MeetingRoom                = recordingwire.MeetingRoom
	Timeline                   = recordingwire.Timeline
	HubTranscript              = recordingwire.HubTranscript
	Photo                      = recordingwire.Photo
	PhotoRef                   = recordingwire.PhotoRef
	PhotoText                  = recordingwire.PhotoText
	PhotoUpload                = recordingwire.PhotoUpload
	MinutesOverview            = recordingwire.MinutesOverview
	MinutesConclusion          = recordingwire.MinutesConclusion
	MinutesGroup               = recordingwire.MinutesGroup
	MinutesTopic               = recordingwire.MinutesTopic
	MinutesBullet              = recordingwire.MinutesBullet
	DebateStatus               = recordingwire.DebateStatus
	MinutesDebate              = recordingwire.MinutesDebate
	MinutesDebatePosition      = recordingwire.MinutesDebatePosition
	MinutesDebateAlso          = recordingwire.MinutesDebateAlso
	MinutesDebateOutcome       = recordingwire.MinutesDebateOutcome
	SpeakerID                  = recordingwire.SpeakerID
	SpeakerState               = recordingwire.SpeakerState
	SpeakerFailReason          = recordingwire.SpeakerFailReason
	SpeakerQuality             = recordingwire.SpeakerQuality
	SpeakerQualityReason       = recordingwire.SpeakerQualityReason
	SpeakerSource              = recordingwire.SpeakerSource
	SpeakerRunID               = recordingwire.SpeakerRunID
	SpeakerClusterID           = recordingwire.SpeakerClusterID
	TranscriptTiming           = recordingwire.TranscriptTiming
	SpeakerSpan                = recordingwire.SpeakerSpan
	SpeakerTurn                = recordingwire.SpeakerTurn
	RawSpeakerTurn             = recordingwire.RawSpeakerTurn
	SpeakerStat                = recordingwire.SpeakerStat
	SpeakerPerson              = recordingwire.SpeakerPerson
	SpeakerEdits               = recordingwire.SpeakerEdits
	MeetingSpeakers            = recordingwire.MeetingSpeakers
	SpeakerSignal              = recordingwire.SpeakerSignal
	SpeakerTurnsFile           = recordingwire.SpeakerTurnsFile
	SpeakerEditOpKind          = recordingwire.SpeakerEditOpKind
	SpeakerEditOp              = recordingwire.SpeakerEditOp
	SpeakerEditRequest         = recordingwire.SpeakerEditRequest
	SpeakerEditResponse        = recordingwire.SpeakerEditResponse
	SpeakerTurnsImportRequest  = recordingwire.SpeakerTurnsImportRequest
	SpeakerTurnsImportResponse = recordingwire.SpeakerTurnsImportResponse
	DetailSpeakers             = recordingwire.DetailSpeakers
	DetailSpeakerPerson        = recordingwire.DetailSpeakerPerson
	photoBook                  = recordingwire.PhotoBook
	storedPhoto                = recordingwire.StoredPhoto
)

// calendarEntry is a calendar entry as the writers read it (recordingwire.CalendarItem, its kind and id as the text
// the stored meeting reference keeps); the writers call it Item.
type calendarEntry struct {
	Kind, ID, Title string
	At              time.Time
}

type Item = calendarEntry

// The constants and errors the writers name.
const (
	KindRecording = recordingwire.KindRecording
	KindMeeting   = recordingwire.KindMeeting
	KindEvent     = recordingwire.KindEvent

	RecordingDay     = recordingwire.RecordingDay
	RecordingMeeting = recordingwire.RecordingMeeting
	MinutesPending   = recordingwire.MinutesPending
	MinutesReady     = recordingwire.MinutesReady
	MinutesFailed    = recordingwire.MinutesFailed
	DebateOpen       = recordingwire.DebateOpen
	DebateSettled    = recordingwire.DebateSettled
	PartPhotos       = recordingwire.PartPhotos
	PartLines        = recordingwire.PartLines
	PartAsks         = recordingwire.PartAsks
	PartTopics       = recordingwire.PartTopics
	PartHighlights   = recordingwire.PartHighlights
	PartSpeakers     = recordingwire.PartSpeakers
	RecordingNote    = recordingwire.RecordingNote
	RoomNone         = recordingwire.RoomNone

	SpeakerStateWaiting = recordingwire.SpeakerStateWaiting
	SpeakerStateReady   = recordingwire.SpeakerStateReady
	SpeakerStateFailed  = recordingwire.SpeakerStateFailed
	SpeakerStateCleared = recordingwire.SpeakerStateCleared
	SpeakerFailTimeout  = recordingwire.SpeakerFailTimeout
	SpeakerQualityOk    = recordingwire.SpeakerQualityOk
	SpeakerQualityLow   = recordingwire.SpeakerQualityLow
	SpeakerSourceHub    = recordingwire.SpeakerSourceHub
	TimingExact         = recordingwire.TimingExact
	TimingTurn          = recordingwire.TimingTurn
	TimingSpread        = recordingwire.TimingSpread
	SpeakerOpName       = recordingwire.SpeakerOpName
	SpeakerOpMerge      = recordingwire.SpeakerOpMerge
	SpeakerOpUnmerge    = recordingwire.SpeakerOpUnmerge
	SpeakerOpClear      = recordingwire.SpeakerOpClear
	MaxSpeakerTurns     = recordingwire.MaxSpeakerTurns

	RecapDueEvening  = recordingwire.RecapDueEvening
	RecapDueComplete = recordingwire.RecapDueComplete
	RecapDueCutoff   = recordingwire.RecapDueCutoff
	RecapDueAsked    = recordingwire.RecapDueAsked
	RecapDueRepair   = recordingwire.RecapDueRepair
	FinalComplete    = recordingwire.FinalComplete
	FinalCutoff      = recordingwire.FinalCutoff
	RecapMeeting     = recordingwire.RecapMeeting
	RecapOutside     = recordingwire.RecapOutside

	recapFile      = recordingwire.RecapFile
	recapMarkdown  = recordingwire.RecapMarkdown
	recapNotesFile = recordingwire.RecapNotesFile

	photosDir        = string(recordingwire.DirAttachments)
	audioDir         = recordingwire.DirAudio
	hubSegmentsFile  = string(recordingwire.FileHubSegments)
	speakerTurnsFile = string(recordingwire.FileSpeakerTurns)
	meetingFile      = string(recordingwire.FileMeeting)
	liveFile         = string(recordingwire.FileLive)

	MaxPhotos       = recordingwire.MaxPhotos
	MaxPhotoText    = recordingwire.MaxPhotoText
	MaxPhotoCaption = recordingwire.MaxPhotoCaption
	MaxPhotoNote    = recordingwire.MaxPhotoNote
	PhotoJPEG       = recordingwire.PhotoJPEG
	PhotoPNG        = recordingwire.PhotoPNG

	TranscriptionHub         = recordingwire.TranscriptionHub
	TranscriptionDevice      = recordingwire.TranscriptionDevice
	TranscriptionUnavailable = recordingwire.TranscriptionUnavailable
	TranscriptionNotMeeting  = recordingwire.TranscriptionNotMeeting
	SourceDevice             = recordingwire.SourceDevice
	HubPassLive              = recordingwire.HubPassLive
	HubPassFinal             = recordingwire.HubPassFinal
	SourceHub                = recordingwire.SourceHub
	AudioRate                = recordingwire.AudioRate
	MaxAudioSeconds          = recordingwire.MaxAudioSeconds

	// PurposeMeetingMinutes is the model purpose every call of a minutes job asks for (#7980): the whole transcript, or
	// each part's notes and their merge, with the summary card in the same answers.
	PurposeMeetingMinutes = recordingwire.PurposeMeetingMinutes
)

var (
	ErrNotFound           = kbwire.ErrNotFound
	ErrReadOnly           = kbwire.ErrReadOnly
	ErrTranscriptExpired  = recordingwire.ErrTranscriptExpired
	ErrTranscriptionFinal = recordingwire.ErrTranscriptionFinal
	ErrHubTranscribes     = recordingwire.ErrHubTranscribes
	ErrRecordingDeleted   = recordingwire.ErrRecordingDeleted
	ErrBadRecording       = recordingwire.ErrBadRecording
	ErrNotHub             = recordingwire.ErrNotHub
	ErrAudioFull          = recordingwire.ErrAudioFull
	ErrBadAudio           = recordingwire.ErrBadAudio
	ErrPhotoNotFound      = recordingwire.ErrPhotoNotFound
	ErrPhotoDeleted       = recordingwire.ErrPhotoDeleted
	ErrPhotosFull         = recordingwire.ErrPhotosFull
	ErrOpIDReused         = recordingwire.ErrOpIDReused
	ErrBadPhoto           = recordingwire.ErrBadPhoto
	ErrSpeakersStale      = recordingwire.ErrSpeakersStale
	ErrSpeakersCleared    = recordingwire.ErrSpeakersCleared
	ErrBadSpeakers        = recordingwire.ErrBadSpeakers
	ErrSpeakersTooLarge   = recordingwire.ErrSpeakersTooLarge
)

// The rules of a recording's stored state, which the knowledge base applies too (recordingwire/rules.go).
var (
	MinutesTaskKey      = recordingwire.MinutesTaskKey
	RecordingID         = recordingwire.RecordingID
	dayLocation         = recordingwire.DayLocation
	clockAt             = recordingwire.ClockAt
	coverageTo          = recordingwire.CoverageTo
	dayStart            = recordingwire.DayStart
	isMeeting           = recordingwire.GetsMinutes
	minutesPhotos       = recordingwire.MinutesPhotos
	minutesSpan         = recordingwire.MinutesSpan
	photoLine           = recordingwire.PhotoLine
	planMinutes         = recordingwire.PlanMinutes
	planRecap           = recordingwire.PlanRecap
	recapFailed         = recordingwire.RecapFailed
	recordingEnd        = recordingwire.RecordingEnd
	transcriptionDone   = recordingwire.TranscriptionDone
	transcriptionHolds  = recordingwire.TranscriptionHolds
	speakersHolds       = recordingwire.SpeakersHolds
	hubTranscription    = recordingwire.HubTranscription
	settleTranscription = recordingwire.SettleTranscription
	meetingSame         = recordingwire.MeetingSame
	meetingParts        = recordingwire.MeetingParts
	bumpRevs            = recordingwire.BumpRevs
	MeetingLines        = recordingwire.MeetingLines
	recordChars         = recordingwire.RecordChars
	GapsIn              = recordingwire.GapsIn
	sameSpans           = recordingwire.SameSpans
	atSample            = recordingwire.AtSample
	sampleAt            = recordingwire.SampleAt
	taggingFieldLocked  = kbwire.TaggingFieldLocked
	clip                = textfmt.Clip
)

// Small helpers the knowledge base has under the same names (store.go, edit.go, language.go, hub_segments_read.go).

func calendarTitle(r *Recording) string     { return r.CalendarTitle() }
func hubMode(m *recordingwire.Meeting) bool { return m != nil && m.Transcription.Hub() }
func storedLang(lang string) i18n.Lang      { return i18n.Lang(lang).Or(i18n.ZhHans) }
func finite(f float64) bool                 { return !math.IsNaN(f) && !math.IsInf(f, 0) }
func fold(s string) string                  { return norm.NFC.String(strings.ToLower(s)) }
func seconds(sec float64) time.Duration     { return time.Duration(sec * float64(time.Second)) }
func oneLine(s string) string               { return clip(strings.Join(strings.Fields(s), " "), 200) }
func clipRunes(s string, n int) string      { return clip(strings.Join(strings.Fields(s), " "), n) }
func removeStrings(list, remove []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(v string) bool { return slices.Contains(remove, v) })
}

func addUnique(list []string, v ...string) []string {
	for _, x := range v {
		if x != "" && !slices.Contains(list, x) {
			list = append(list, x)
		}
	}
	return list
}

// minutesSpanOf gives minutes written before they had a span the one of their recording (the knowledge base's,
// minutes_read.go).
func minutesSpanOf(m *Minutes, r *Recording, zone *time.Location) {
	if m.Start.IsZero() && r != nil && !r.StartedAt.IsZero() {
		m.Start, m.End = minutesSpan(r, zone)
	}
}

// readJSON reads the JSON file p into out; false when there is no such file.
func readJSON(p string, out any) (bool, error) {
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

// uuidRe is the knowledge base's check of a lowercase UUID, here of a photo's id, as its pattern has it (recordingwire.RecordingID
// normalizes before it checks; a text it leaves as it is matched the pattern).
var uuidRe uuidCheck

type uuidCheck struct{}

func (uuidCheck) MatchString(s recordingwire.PhotoID) bool {
	id, ok := recordingwire.RecordingID(string(s))
	return ok && id == string(s)
}

func badRecording(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBadRecording, fmt.Sprintf(f, a...))
}

// nonNilSegments is segs, empty rather than nil (the knowledge base's, recordings_read.go).
func nonNilSegments(segs []RecordingSegment) []RecordingSegment {
	if segs == nil {
		return []RecordingSegment{}
	}
	return segs
}

// syncRecordingDir makes a directory's entries durable, best effort, as the knowledge base's commit does
// (internal/kb/recording_commit_keep.go): the audio writers call it once a chunk file was made.
func syncRecordingDir(dir string) {
	if f, err := os.Open(dir); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}

// The store's members the writers call, each the port's. The writers name ids, files, kinds and changes as text,
// as they did; here each becomes the port's type, and a text that is none of the port's values is a defect of this
// package (known panics on it: the names are literals of the writers' code).

func known[T interface{ Known() bool }](v T) T {
	if !v.Known() {
		panic(fmt.Sprintf("recording writers: %T %v is not one the storage port takes", v, v))
	}
	return v
}

func textFiles(files recordingwire.Files) map[string][]byte {
	if files == nil {
		return nil
	}
	out := make(map[string][]byte, len(files))
	for f, data := range files {
		out[string(f)] = data
	}
	return out
}

func (s *store) ReadOnly() bool       { return s.core.ReadOnly() }
func (s *store) Zone() *time.Location { return s.core.Zone() }
func (s *store) RecordingZone(r *Recording) *time.Location {
	return recordingwire.RecordingZone(r, s.core.Zone())
}
func (s *store) Recording(id string) (*Recording, bool) {
	return s.core.Recording(recordingwire.ID(id))
}
func (s *store) Recordings() []*Recording { return s.core.Recordings() }
func (s *store) Items(kind string) []*Item {
	items := s.core.CalendarItems(known(recordingwire.CalendarKind(kind)))
	out := make([]*Item, len(items))
	for i, it := range items {
		out[i] = &Item{Kind: string(it.Kind), ID: string(it.ID), Title: it.Title, At: it.At}
	}
	return out
}
func (s *store) ReadMinutes(id string) (*Minutes, error) {
	return s.core.ReadMinutes(recordingwire.ID(id))
}
func (s *store) readRecap(id string) (*DayRecap, error) {
	return s.core.ReadRecap(recordingwire.ID(id))
}
func (s *store) readPhotos(id string) (photoBook, error) {
	return s.core.PhotoBook(recordingwire.ID(id))
}

// citedPhotos are the photos recording id's current minutes cite; hasMinutes: it has minutes that can be read.
func (s *store) citedPhotos(id string) map[int]bool {
	m, _ := s.ReadMinutes(id)
	return minutesPhotos(m)
}
func (s *store) hasMinutes(id string) bool {
	_, err := s.ReadMinutes(id)
	return err == nil
}
func (s *store) tombstoned(id string) bool { return s.core.Tombstoned(recordingwire.ID(id)) }

// deleted is every recording deleted on purpose, by id.
func (s *store) deleted() []string {
	ids := s.core.Deleted()
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// commitRecording is the knowledge base's commit of an upload under its undo journal; recordingRead one committed
// file of a recording, read through a pending journal.
func (s *store) commitRecording(r *Recording, segs []RecordingSegment) error {
	return s.core.CommitRecording(r, segs)
}
func (s *store) recordingRead(id, name string) ([]byte, error) {
	return s.core.RecordingFile(recordingwire.ID(id), known(recordingwire.File(name)))
}

// hubSegmentsOf is the hub's stored segments of recording id (none without them), as the knowledge base's reader of
// the same name reads them (hub_segments_read.go): under the files lock's read side.
func (s *store) hubSegmentsOf(id string) []RecordingSegment {
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	h, _ := s.readHubLocked(id)
	return h.Segments
}

// readMeeting is recording id's meeting.json as it is on disk, nil without one (the knowledge base's,
// meeting_read.go); the upload reads it under meetMu.
func (s *store) readMeeting(id string) (*Meeting, error) {
	raw, err := os.ReadFile(s.path(KindRecording, id, meetingFile))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Meeting
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("meeting of %s: %w", id, err)
	}
	return &m, nil
}

// Project is the project slug names as an earlier meeting's material lists it.
func (s *store) Project(slug string) (EarlierProject, bool) {
	return s.core.Project(recordingwire.ProjectSlug(slug))
}
func (s *store) putPhotosLocked(id string, b *photoBook, start time.Time, zone *time.Location) error {
	return s.core.PutPhotoBook(recordingwire.ID(id), b, start, zone)
}
func (s *store) putPhotosTextLocked(id string, b *photoBook, start time.Time, zone *time.Location) (bool, error) {
	return s.core.PutPhotoText(recordingwire.ID(id), b, start, zone)
}
func (s *store) SearchIndexPurge() { s.core.PurgeSearch() }
func (s *store) writeRecordingFile(p string, data []byte) error {
	return s.core.WriteDurable(p, data)
}
func (s *store) readHubLocked(id string) (HubTranscript, error) {
	return s.core.HubTranscriptLocked(recordingwire.ID(id))
}
func (s *store) readSpeakerTurnsLocked(id string) (*SpeakerTurnsFile, error) {
	return s.core.SpeakerTurnsLocked(recordingwire.ID(id))
}
func (s *store) speakerTurns(id string) (*SpeakerTurnsFile, error) {
	s.recFilesMu.RLock()
	defer s.recFilesMu.RUnlock()
	return s.readSpeakerTurnsLocked(id)
}
func (s *store) renderTranscript(r *Recording, segs []RecordingSegment) string {
	return s.core.RenderTranscript(r, segs)
}

// recordLocked is the transcript of record of recording r (its Conv read) with device its upload's segments; the
// caller holds recFilesMu (the knowledge base's, hub_segments_keep.go).
func (s *store) recordLocked(r *Recording, device []RecordingSegment) []RecordingSegment {
	if !r.HubTranscribed() {
		return device
	}
	h, _ := s.readHubLocked(r.ID)
	return s.TranscriptOfRecord(true, device, h.Segments)
}

// recordOf is the transcript of record of a recordSnapshot (the knowledge base's, hub_segments_read.go).
func (s *store) recordOf(files map[string][]byte, m *Meeting, h HubTranscript) []RecordingSegment {
	var segs []RecordingSegment
	_ = json.Unmarshal(files["segments.json"], &segs)
	return s.TranscriptOfRecord(hubMode(m), segs, h.Segments)
}

// recordingName is r's name in lists. The knowledge base's store names a day recording without the module's name
// for the day; the one caller here (matchRecordings) never has a day recording, so the engine's name is the same.
func (s *store) recordingName(r *Recording) string { return s.core.RecordingName(r) }
func (s *store) recoverRecordingLocked(id string) error {
	return s.core.RecoverRecording(recordingwire.ID(id))
}
func (s *store) bumpPhotosRev(id string, start time.Time) error {
	return s.core.BumpPhotosRev(recordingwire.ID(id), start)
}
func (s *store) writeFile(p string, data []byte) error { return s.core.WriteFile(p, data) }
func (s *store) photoWords(p recordingwire.PhotoText, clipped bool) string {
	return s.core.PhotoWords(p, clipped)
}
func (s *store) TranscriptOfRecord(hubMode bool, device, hub []RecordingSegment) []RecordingSegment {
	return recordingwire.TranscriptOfRecord(hubMode, device, hub)
}
func (s *store) recordingSnapshot(id string) (map[string][]byte, error) {
	files, err := s.core.RecordingSnapshot(recordingwire.ID(id))
	return textFiles(files), err
}
func (s *store) recordSnapshot(id string) (map[string][]byte, *recordingwire.Meeting, recordingwire.HubTranscript, error) {
	files, m, hub, err := s.core.RecordSnapshot(recordingwire.ID(id))
	return textFiles(files), m, hub, err
}

// path, entityPath, notify, putEntity and putSummary take the entry's kind as the knowledge base's do; the writers
// only ever name a recording.
func (s *store) path(_, id string, parts ...string) string {
	if len(parts) == 0 { // the recording's directory
		return filepath.Dir(s.core.Path(recordingwire.ID(id), recordingwire.FileMeta))
	}
	p := s.core.Path(recordingwire.ID(id), known(recordingwire.File(parts[0])))
	if len(parts) == 1 {
		return p
	}
	return filepath.Join(append([]string{p}, parts[1:]...)...) // a file the writers name in a directory of the recording
}
func (s *store) entityPath(_, id string) string {
	return s.core.Path(recordingwire.ID(id), recordingwire.FileMeta)
}
func (s *store) notify(_, id, what string) {
	s.core.Notify(recordingwire.ID(id), known(recordingwire.Change(what)))
}
func (s *store) putEntity(_, _ string, r *Recording, _ *Tagging, _ bool) error {
	return s.core.PutMeta(r)
}
func (s *store) putSummary(_, id, text string, _ bool) error {
	return s.core.PutSummary(recordingwire.ID(id), text)
}

// The engine's members the writers call.

// RecordingDays is the text's retention in days as it is in effect (the port computes it, default included): 0 means
// forever.
func (e *engine) RecordingDays() int { return e.Store.core.RecordingDays() }

// pastRetention: a recording that started at start is past a retention of days at now (never when days is 0).
func pastRetention(start, now time.Time, days int) bool {
	return days > 0 && start.Before(now.UTC().AddDate(0, 0, -days))
}

func (e *engine) now() time.Time                    { return e.Store.core.Now() }
func (e *engine) logf(f string, a ...any)           { e.Store.core.Logf(f, a...) }
func (e *engine) recordingName(r *Recording) string { return e.Store.core.RecordingName(r) }
func (e *engine) deferred(ctx context.Context, err error) bool {
	return e.Store.core.Deferred(ctx, err)
}

// meetingUpdateLocked is the knowledge base's update of meeting.json; the caller holds meetMu (MeetingUpdate,
// meeting.go, takes it and tells MeetingChanged).
func (e *engine) meetingUpdateLocked(id string, fn func(m *Meeting, r Recording) error) (Meeting, []string, error) {
	m, parts, err := e.Store.core.UpdateMeetingLocked(recordingwire.ID(id), fn)
	var names []string
	if parts != nil {
		names = make([]string, len(parts))
		for i, p := range parts {
			names[i] = string(p)
		}
	}
	return m, names, err
}
func (e *engine) meetingChanged(id string, parts []string) {
	typed := make([]recordingwire.MeetingPart, len(parts))
	for i, p := range parts {
		typed[i] = known(recordingwire.MeetingPart(p))
	}
	e.Store.core.MeetingChanged(recordingwire.ID(id), typed)
}
func (e *engine) outputLang(r *Recording) i18n.Lang { return i18n.Lang(e.Store.core.OutputLang(r)) }
func (e *engine) daySpan(r *Recording, now time.Time) (time.Time, time.Time) {
	return recordingwire.DaySpan(r, now, e.Store.core.Zone())
}
func (e *engine) recordedWithin(r *Recording, now, from, to time.Time) bool {
	return recordingwire.RecordedWithin(r, now, from, to, e.Store.core.Zone())
}
func (e *engine) index(_, id, name string, t Tagging, one string, updated time.Time) {
	e.Store.core.Index(recordingwire.IndexEntry{ID: recordingwire.ID(id), Name: name, Tagging: t, OneLine: one, Updated: updated})
}
func (e *engine) writingOwner(lang i18n.Lang) prompts.KBOwner {
	o := e.Store.core.WritingOwner(recordingwire.Language(lang))
	return prompts.KBOwner{Owner: o.Name, Language: o.Language}
}

// modelFor is the model for purpose id: the engine's model per purpose, else its model.
func (e *engine) modelFor(id string) Model {
	if e.ModelFor != nil {
		return e.ModelFor(id)
	}
	return e.Model
}
