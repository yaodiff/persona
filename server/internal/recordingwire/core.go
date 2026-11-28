package recordingwire

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// Core is the storage port: what the recording module's writers ask of the knowledge base that holds the recordings
// (docs/plans/plan-kb-recording-port.md, sections 3.5 and 3.6). The knowledge base implements it (an adapter in
// internal/kb) and hands it to the module in its hook; the module's writers reach the store through nothing else. It
// has two groups: Reader is the knowledge base's own read path, the same functions every other reader calls, and
// Writer is the write primitives, the store's locks and the engine's settings the writers were using while they
// lived in internal/kb. It is wider than a port should be (the minutes and day-recap writers read and write the store
// in many places); it shrinks as the writers stop needing a primitive, and it never grows a second way to read.
// The photo intake, the audio, title and hub-transcript writers (PR 4 of the plan) reach the store through it too.
type Core interface {
	Reader
	Writer
}

// Reader is the read group: what the writers read of the knowledge base. Every method is a reader the knowledge base
// has with or without the module; none of them writes.
type Reader interface {
	// ReadOnly: the knowledge base was opened read-only; nothing is written.
	ReadOnly() bool
	// Zone is the knowledge base's zone (the user's).
	Zone() *time.Location
	// Recording is recording id as committed; Recordings every recording.
	Recording(id ID) (*Recording, bool)
	Recordings() []*Recording
	// RecordingName is r's name in lists as it stands now (a day recording's from the day).
	RecordingName(r *Recording) string
	// RecordingSnapshot is recording id's committed files; RecordSnapshot the same with its meeting.json and the
	// hub's transcript (the transcript the minutes are made from is the rule TranscriptOfRecord of the two).
	RecordingSnapshot(id ID) (Files, error)
	RecordSnapshot(id ID) (Files, *Meeting, HubTranscript, error)
	// ReadMinutes is recording id's minutes.json; ReadRecap its recap.json.
	ReadMinutes(id ID) (*Minutes, error)
	ReadRecap(id ID) (*DayRecap, error)
	// PhotoWords is one photo's words as a line has them; PhotoBook recording id's photos as stored (photos.json;
	// an empty book without one).
	PhotoWords(p PhotoText, clipped bool) string
	PhotoBook(id ID) (PhotoBook, error)
	// Tombstoned: recording id was deleted (its tombstone is there). Deleted is every recording deleted on purpose
	// (the phone is told of them and drops its copies).
	Tombstoned(id ID) bool
	Deleted() []ID
	// RecordingFile is file f of recording id as committed (read through a pending undo journal, like every read);
	// nil when it has none. A day's append reads the stored segments with it, not the whole snapshot.
	RecordingFile(id ID, f File) ([]byte, error)
	// RecordingDays is how many days a recording's text is kept (the owner's recording_text_days, the default without
	// one; 0 means forever): an upload or an append of a recording that started before them is answered as expired.
	RecordingDays() int
	// TranscriptionReady: the hub can transcribe a meeting now (asked when the first upload of a meeting names the
	// hub, under Locks().Meeting). EarlierMeetings: the material of an answer may hold earlier meetings (the owner's
	// setting).
	TranscriptionReady() bool
	EarlierMeetings() bool
	// Project is the project slug names as an earlier meeting's material lists it (name and status); false when the
	// knowledge base has none.
	Project(slug ProjectSlug) (EarlierProject, bool)
	// HubTranscriptLocked is recording id's hub_segments.json (an empty one when there is none); the caller holds
	// Locks().Files. SpeakerTurnsLocked is recording id's speaker_turns.json (nil when there is none); the caller
	// holds Locks().Files. RenderTranscript is transcript.md of r with segs its transcript of record.
	HubTranscriptLocked(id ID) (HubTranscript, error)
	SpeakerTurnsLocked(id ID) (*SpeakerTurnsFile, error)
	// Pure speaker rules delegated through Core so the recording module never imports the domain adapter directly.
	ImportSpeakerTimeline(in SpeakerImportInput) (SpeakerImportOutput, error)
	RecomputeSpeakerStats(in SpeakerRecomputeInput) (SpeakerRecomputeOutput, error)
	ApplySpeakerEdit(in SpeakerEditInput) (SpeakerEditOutput, error)
	AlignTranscriptSpeakers(in SpeakerAlignInput) (SpeakerAlignOutput, error)
	AttributeDebateSpeakers(in SpeakerDebatesAttrInput) (SpeakerDebatesAttrOutput, error)
	ResolveDebateDisplays(in SpeakerDisplayInput) []SpeakerDisplayLabel
	RenderTranscript(r *Recording, segs []kbwire.RecordingSegment) string
	// CalendarItems is the calendar entries of kind a recording is matched to.
	CalendarItems(kind CalendarKind) []CalendarItem
	// Path is where file f of recording id is on disk.
	Path(id ID, f File) string
}

// Writer is the write-and-lock group: the store's write primitives for a recording, its locks, and what the
// writers took from the engine (clock, log, models, the owner's language, the change feed).
type Writer interface {
	// Locks is the store's locks the writers take themselves. Transitional: the order is the store's and must not
	// change (Recordings, then Meeting, then Files, then Meta; the live log's lock and then the audio writers'
	// per-recording mutex, both the module's own, sit between Meeting and Files).
	Locks() Locks
	// RecoverRecording finishes or undoes an interrupted commit of recording id; the caller holds Locks().Files.
	RecoverRecording(id ID) error
	// WriteFile writes data to path (one Reader.Path gave) atomically, a temporary file renamed, making missing
	// directories. WriteDurable writes it as a recording's committed files are written: synced before the rename,
	// its directory after (the photos, the hub's transcript, transcript.md, the audio index); the caller holds
	// Locks().Files or its read side.
	WriteFile(path string, data []byte) error
	WriteDurable(path string, data []byte) error
	// CommitRecording writes upload r with its segments segs (meta.json, segments.json, transcript.md) under the undo
	// journal, so that a reader sees the old upload or the new one and a crash between leaves either; the caller holds
	// Locks().Recordings. It takes Locks().Files and Locks().Meta itself.
	CommitRecording(r *Recording, segs []kbwire.RecordingSegment) error
	// PutMeta writes r's meta.json as the knowledge base writes every entry's (its tagging rules, the commit
	// recovery, the change feed). PutSummary writes recording id's summary.md unless the user wrote it.
	PutMeta(r *Recording) error
	PutSummary(id ID, text string) error
	// BumpPhotosRev moves recording id's photos revision; the caller holds Locks().Recordings.
	BumpPhotosRev(id ID, start time.Time) error
	// PutPhotoBook writes recording id's photos.json and photos.md from b (start and zone place the photos' clock
	// times); PutPhotoText only makes photos.md what b lists, changed when it was not so. The caller holds
	// Locks().Files and checked that the recording is there.
	PutPhotoBook(id ID, b *PhotoBook, start time.Time, zone *time.Location) error
	PutPhotoText(id ID, b *PhotoBook, start time.Time, zone *time.Location) (changed bool, err error)
	// UpdateMeetingLocked changes recording id's meeting.json with fn (given it and the stored upload) and writes
	// the result unless fn fails or changed nothing; the parts whose revision moved. The caller holds
	// Locks().Meeting and tells MeetingChanged of the parts once it let go.
	UpdateMeetingLocked(id ID, fn func(m *Meeting, r Recording) error) (Meeting, []MeetingPart, error)
	// PurgeSearch has the search index drop what was deleted on purpose at its next update.
	PurgeSearch()
	// Notify tells the change feed that part what of recording id changed; Index puts the recording into the entry
	// index; MeetingChanged tells the meeting's listeners which parts of id changed.
	Notify(id ID, what Change)
	Index(e IndexEntry)
	MeetingChanged(id ID, parts []MeetingPart)
	// Now is the engine's clock; Logf its log (a format and its arguments, as fmt takes them).
	Now() time.Time
	Logf(format string, args ...any)
	// Model is the engine's model for purpose p, nil when it has none; HasModel whether it has any model at all.
	Model(p Purpose) Model
	HasModel() bool
	// OutputLang is the language r's minutes and recap are written in; WritingOwner how the owner is named to the
	// model and, when lang is not the default, the language to write in.
	OutputLang(r *Recording) Language
	WritingOwner(lang Language) Owner
	// DayRecapClock is the evening routine's time of day; ok false when the engine has none.
	DayRecapClock() (c Clock, ok bool)
	// Deferred: err, a model call's, puts the work off rather than failing it (the connection is paused, no model is
	// set up for it, or ctx ended), so the round stops and asks again later.
	Deferred(ctx context.Context, err error) bool
	// Provider is the write side this store was given before (NoProvider: none). A second engine over the same
	// store gets its own writers, which share what the first ones hold in memory through it: the live log's lock and
	// the audio writers' state are one per store.
	Provider() Provider
}

// ID is a recording's id as stored: a lowercase UUID (ParseID). The stored JSON and the knowledge base's own code
// keep it as text; the port's methods take this type so that no other text is passed for one.
type ID string

// ParseID is raw as a recording's id; false when it is not one.
func ParseID(raw string) (ID, bool) {
	id, ok := RecordingID(raw)
	return ID(id), ok
}

// File is a file of a recording the writers read or write, by its name next to meta.json.
type File string

const (
	FileMeta            File = "meta.json"
	FileSegments        File = "segments.json"
	FileSummary         File = "summary.md"
	FileMinutes         File = "minutes.json"
	FileMinutesMarkdown File = "minutes.md"
	FileRecap           File = RecapFile
	FileRecapMarkdown   File = RecapMarkdown
	FileRecapNotes      File = RecapNotesFile
	FileTranscript      File = "transcript.md"
	FileHubSegments     File = "hub_segments.json"
	FileSpeakerTurns    File = "speaker_turns.json"
	// FileMeeting is meeting.json (the room, the private mark, the follow-up, the transcription, the speakers); FileLive the live
	// log of a meeting Persona attends or the hub transcribes.
	FileMeeting File = "meeting.json"
	FileLive    File = "live.json"
	// DirAttachments is the directory of a recording's photos (p<n>.jpg, p<n>.png) and, in its audio/, of a
	// hub-mode meeting's audio: the writers name the files in it themselves.
	DirAttachments File = "attachments"
)

// Known: f is one of the files above.
func (f File) Known() bool {
	switch f {
	case FileMeta, FileSegments, FileSummary, FileMinutes, FileMinutesMarkdown, FileRecap, FileRecapMarkdown, FileRecapNotes,
		FileTranscript, FileHubSegments, FileSpeakerTurns, FileMeeting, FileLive, DirAttachments:
		return true
	}
	return false
}

// Files is a recording's committed files by name (a snapshot may hold more than the writers' own, e.g. the transcript).
type Files map[File][]byte

// Change is the part of a recording the change feed is told of.
type Change string

const (
	ChangeSummary  Change = "summary"
	ChangeMetadata Change = "metadata"
	ChangeMessages Change = "messages" // the transcript
)

// Known: c is one of the changes above.
func (c Change) Known() bool { return c == ChangeSummary || c == ChangeMetadata || c == ChangeMessages }

// MeetingPart is a part of meeting.json's view whose revision moved (the Part constants).
type MeetingPart string

// MeetingPartPhotos: the photos, and which of them the minutes cite. The others are the parts an update of
// meeting.json may move.
const (
	MeetingPartPhotos        MeetingPart = PartPhotos
	MeetingPartRoom          MeetingPart = PartRoom
	MeetingPartAsks          MeetingPart = PartAsks
	MeetingPartPrivate       MeetingPart = PartPrivate
	MeetingPartFollowup      MeetingPart = PartFollowup
	MeetingPartLines         MeetingPart = PartLines
	MeetingPartTopics        MeetingPart = PartTopics
	MeetingPartHighlights    MeetingPart = PartHighlights
	MeetingPartTranscription MeetingPart = PartTranscription
	MeetingPartInterim       MeetingPart = PartInterim
	MeetingPartSpeakers      MeetingPart = PartSpeakers
)

// Known: p is one of the parts above.
func (p MeetingPart) Known() bool {
	switch p {
	case MeetingPartPhotos, MeetingPartRoom, MeetingPartAsks, MeetingPartPrivate, MeetingPartFollowup, MeetingPartLines,
		MeetingPartTopics, MeetingPartHighlights, MeetingPartTranscription, MeetingPartInterim, MeetingPartSpeakers:
		return true
	}
	return false
}

// CalendarKind is a kind of calendar entry a recording is matched to.
type CalendarKind string

const (
	CalendarEvents   CalendarKind = KindEvent   // the scheduled ones
	CalendarMeetings CalendarKind = KindMeeting // the video calls held, imported
)

// Known: k is one of the kinds above.
func (k CalendarKind) Known() bool { return k == CalendarEvents || k == CalendarMeetings }

// Valid reports whether k is a known calendar kind.
func (k CalendarKind) Valid() bool { return k.Known() }

// ProjectSlug is a project's slug in the knowledge base, as a recording's Projects lists it.
type ProjectSlug string

// ItemID is a calendar entry's id in the knowledge base.
type ItemID string

// CalendarItem is a calendar entry of the knowledge base as a recording is matched to it.
type CalendarItem struct {
	Kind  CalendarKind
	ID    ItemID
	Title string
	At    time.Time
}

// IndexEntry is a recording as the entry index lists it.
type IndexEntry struct {
	ID      ID
	Name    string
	Tagging kbwire.Tagging
	OneLine string
	Updated time.Time
}

// Purpose is what a model call of the writers is for; the engine chooses the model by it.
type Purpose string

const (
	PurposeMinutes  Purpose = PurposeMeetingMinutes // a minutes job's calls
	PurposeCompress Purpose = "kb.compress"         // the day recap's (Information processing)
	PurposeTitle    Purpose = PurposeMeetingTitle   // the live title's
)

// Known: p is one of the purposes above.
func (p Purpose) Known() bool {
	return p == PurposeMinutes || p == PurposeCompress || p == PurposeTitle
}

// Language is the language something is written in, as the knowledge base's translations name it ("zh-Hans", "en",
// "ja"); the knowledge base computes it (Writer.OutputLang) and the writers hand it back unchanged.
type Language string

// Owner is how the owner is named to the model, and the language to write in when it is not the default ("": the
// default).
type Owner struct {
	Name     string
	Language string
}

// Clock is a time of day.
type Clock struct{ Hour, Minute int }

// Locks is the store's locks as the writers take them (Writer.Locks).
type Locks struct {
	// Recordings guards a recording's read-compare-write; Meeting meeting.json's read-modify-write; Files its files
	// and the commit recovery; Meta every entry's meta.json. Acquire in this order.
	Recordings, Meeting, Files, Meta sync.Locker
	// FilesRead is Files held for reading: an audio append holds it, so that a delete, the trim and retention (which
	// hold Files) wait for a write in flight and every write after them sees the directory gone.
	FilesRead sync.Locker
}

// Model is the language model the writers call: a JSON answer of at most maxTokens tokens decoded into out (a
// pointer to the answer's type, as encoding/json takes one).
type Model interface {
	JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error
}
