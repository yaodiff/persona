package kb

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/recordingspeakers/recordingspeakersadapt"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The storage port's Core side (docs/plans/plan-kb-recording-port.md, sections 3.5 and 3.6): engine e as
// recordingwire.Core, which the recording module's writers reach the store through. Every method is the function the
// writers called while they lived in this package: the read group is the knowledge base's own readers, the write
// group its write primitives, locks and settings. Nothing here is new behaviour.

// RecordingCore is engine e as the storage port.
func RecordingCore(e *Engine) recordingwire.Core { return recordingCore{e} }

type recordingCore struct{ e *Engine }

// The read group.

func (c recordingCore) ReadOnly() bool       { return c.e.Store.ReadOnly() }
func (c recordingCore) Zone() *time.Location { return c.e.Store.Zone() }
func (c recordingCore) Recording(id recordingwire.ID) (*Recording, bool) {
	return c.e.Store.Recording(string(id))
}
func (c recordingCore) Recordings() []*Recording          { return c.e.Store.Recordings() }
func (c recordingCore) RecordingName(r *Recording) string { return c.e.recordingName(r) }
func (c recordingCore) RecordingSnapshot(id recordingwire.ID) (recordingwire.Files, error) {
	files, err := c.e.Store.recordingSnapshot(string(id))
	return portFiles(files), err
}
func (c recordingCore) RecordSnapshot(id recordingwire.ID) (recordingwire.Files, *Meeting, HubTranscript, error) {
	files, m, hub, err := c.e.Store.recordSnapshot(string(id))
	return portFiles(files), m, hub, err
}
func (c recordingCore) ReadMinutes(id recordingwire.ID) (*Minutes, error) {
	return c.e.Store.ReadMinutes(string(id))
}
func (c recordingCore) ReadRecap(id recordingwire.ID) (*DayRecap, error) {
	return c.e.Store.readRecap(string(id))
}
func (c recordingCore) PhotoWords(p PhotoText, clipped bool) string { return photoWords(p, clipped) }
func (c recordingCore) PhotoBook(id recordingwire.ID) (recordingwire.PhotoBook, error) {
	return c.e.Store.readPhotos(string(id))
}
func (c recordingCore) Tombstoned(id recordingwire.ID) bool {
	_, gone := c.e.Store.tombstones()[string(id)]
	return gone
}
func (c recordingCore) Deleted() []recordingwire.ID {
	stones := c.e.Store.tombstones()
	out := make([]recordingwire.ID, 0, len(stones))
	for id := range stones {
		out = append(out, recordingwire.ID(id))
	}
	return out
}
func (c recordingCore) RecordingFile(id recordingwire.ID, f recordingwire.File) ([]byte, error) {
	return c.e.Store.recordingRead(string(id), string(f))
}
func (c recordingCore) RecordingDays() int { return c.e.RecordingDays() }
func (c recordingCore) TranscriptionReady() bool {
	return c.e.TranscriptionReady != nil && c.e.TranscriptionReady()
}
func (c recordingCore) EarlierMeetings() bool { return c.e.EarlierMeetingsInAnswers }
func (c recordingCore) Project(slug recordingwire.ProjectSlug) (recordingwire.EarlierProject, bool) {
	p, ok := c.e.Store.Project(string(slug))
	if !ok {
		return recordingwire.EarlierProject{}, false
	}
	return recordingwire.EarlierProject{Slug: p.Slug, Name: p.Name, Status: p.Status}, true
}
func (c recordingCore) HubTranscriptLocked(id recordingwire.ID) (HubTranscript, error) {
	return c.e.Store.readHubLocked(string(id))
}
func (c recordingCore) SpeakerTurnsLocked(id recordingwire.ID) (*recordingwire.SpeakerTurnsFile, error) {
	return c.e.Store.readSpeakerTurnsLocked(string(id))
}
func (c recordingCore) ImportSpeakerTimeline(in recordingwire.SpeakerImportInput) (recordingwire.SpeakerImportOutput, error) {
	return recordingspeakersadapt.ImportTimeline(in)
}
func (c recordingCore) RecomputeSpeakerStats(in recordingwire.SpeakerRecomputeInput) (recordingwire.SpeakerRecomputeOutput, error) {
	return recordingspeakersadapt.RecomputeAfterTranscriptChange(in)
}
func (c recordingCore) ApplySpeakerEdit(in recordingwire.SpeakerEditInput) (recordingwire.SpeakerEditOutput, error) {
	return recordingspeakersadapt.ApplyEdit(in)
}
func (c recordingCore) AlignTranscriptSpeakers(in recordingwire.SpeakerAlignInput) (recordingwire.SpeakerAlignOutput, error) {
	return recordingspeakersadapt.AlignTranscript(in)
}
func (c recordingCore) AttributeDebateSpeakers(in recordingwire.SpeakerDebatesAttrInput) (recordingwire.SpeakerDebatesAttrOutput, error) {
	return recordingspeakersadapt.AttributeDebates(in)
}
func (c recordingCore) ResolveDebateDisplays(in recordingwire.SpeakerDisplayInput) []recordingwire.SpeakerDisplayLabel {
	return recordingspeakersadapt.ResolveDebateDisplays(in)
}
func (c recordingCore) RenderTranscript(r *Recording, segs []RecordingSegment) string {
	return c.e.Store.renderTranscript(r, segs)
}
func (c recordingCore) CalendarItems(kind recordingwire.CalendarKind) []recordingwire.CalendarItem {
	items := c.e.Store.Items(string(kind))
	out := make([]recordingwire.CalendarItem, len(items))
	for i, it := range items {
		out[i] = recordingwire.CalendarItem{Kind: recordingwire.CalendarKind(it.Kind), ID: recordingwire.ItemID(it.ID), Title: it.Title, At: it.At}
	}
	return out
}
func (c recordingCore) Path(id recordingwire.ID, f recordingwire.File) string {
	return c.e.Store.path(KindRecording, string(id), string(f))
}

// portFiles is a snapshot's files under the port's type; nil stays nil.
func portFiles(files map[string][]byte) recordingwire.Files {
	if files == nil {
		return nil
	}
	out := make(recordingwire.Files, len(files))
	for name, data := range files {
		out[recordingwire.File(name)] = data
	}
	return out
}

// The write-and-lock group.

func (c recordingCore) Locks() recordingwire.Locks {
	s := c.e.Store
	return recordingwire.Locks{Recordings: &s.recMu, Meeting: &s.meetMu, Files: &s.recFilesMu, Meta: &s.metaMu,
		FilesRead: s.recFilesMu.RLocker()}
}
func (c recordingCore) RecoverRecording(id recordingwire.ID) error {
	return c.e.Store.recoverRecordingLocked(string(id))
}
func (c recordingCore) WriteFile(path string, data []byte) error { return writeFile(path, data) }
func (c recordingCore) WriteDurable(path string, data []byte) error {
	return writeRecordingFile(path, data)
}
func (c recordingCore) CommitRecording(r *Recording, segs []RecordingSegment) error {
	return c.e.Store.commitRecording(r, segs)
}
func (c recordingCore) PutMeta(r *Recording) error {
	return c.e.Store.putEntity(KindRecording, r.ID, r, &r.Tagging, false)
}
func (c recordingCore) PutSummary(id recordingwire.ID, text string) error {
	return c.e.Store.putSummary(KindRecording, string(id), text, false)
}
func (c recordingCore) BumpPhotosRev(id recordingwire.ID, start time.Time) error {
	return c.e.Store.bumpPhotosRev(string(id), start)
}
func (c recordingCore) PutPhotoBook(id recordingwire.ID, b *recordingwire.PhotoBook, start time.Time, zone *time.Location) error {
	return c.e.Store.putPhotosLocked(string(id), b, start, zone)
}
func (c recordingCore) PutPhotoText(id recordingwire.ID, b *recordingwire.PhotoBook, start time.Time, zone *time.Location) (bool, error) {
	return c.e.Store.putPhotosTextLocked(string(id), b, start, zone)
}
func (c recordingCore) UpdateMeetingLocked(id recordingwire.ID, fn func(m *Meeting, r Recording) error) (Meeting, []recordingwire.MeetingPart, error) {
	m, names, err := c.e.meetingUpdateLocked(string(id), fn)
	var parts []recordingwire.MeetingPart
	if names != nil {
		parts = make([]recordingwire.MeetingPart, len(names))
		for i, p := range names {
			parts[i] = recordingwire.MeetingPart(p)
		}
	}
	return m, parts, err
}
func (c recordingCore) PurgeSearch() { c.e.Store.SearchIndexPurge() }
func (c recordingCore) Notify(id recordingwire.ID, what recordingwire.Change) {
	c.e.Store.notify(KindRecording, string(id), string(what))
}
func (c recordingCore) Index(en recordingwire.IndexEntry) {
	c.e.index(KindRecording, string(en.ID), en.Name, en.Tagging, en.OneLine, en.Updated)
}
func (c recordingCore) MeetingChanged(id recordingwire.ID, parts []recordingwire.MeetingPart) {
	names := make([]string, len(parts))
	for i, p := range parts {
		names[i] = string(p)
	}
	c.e.meetingChanged(string(id), names)
}
func (c recordingCore) Now() time.Time                  { return c.e.now() }
func (c recordingCore) Logf(format string, args ...any) { c.e.logf(format, args...) }
func (c recordingCore) Model(p recordingwire.Purpose) recordingwire.Model {
	if m := c.e.modelFor(string(p)); m != nil {
		return m
	}
	return nil
}
func (c recordingCore) HasModel() bool { return c.e.Model != nil || c.e.ModelFor != nil }
func (c recordingCore) OutputLang(r *Recording) recordingwire.Language {
	return recordingwire.Language(c.e.outputLang(r))
}
func (c recordingCore) WritingOwner(lang recordingwire.Language) recordingwire.Owner {
	o := c.e.writingOwner(i18n.Lang(lang))
	return recordingwire.Owner{Name: o.Owner, Language: o.Language}
}
func (c recordingCore) Deferred(ctx context.Context, err error) bool { return deferred(ctx, err) }
func (c recordingCore) Provider() recordingwire.Provider {
	if c.e.Store == nil { // an engine with no store yet has no writers to share with (SetRecordingProvider allows one)
		return recordingwire.NoProvider{}
	}
	return c.e.Store.recordingAudio()
}
func (c recordingCore) DayRecapClock() (recordingwire.Clock, bool) {
	if c.e.DayRecapClock == nil {
		return recordingwire.Clock{}, false
	}
	h, m := c.e.DayRecapClock()
	return recordingwire.Clock{Hour: h, Minute: m}, true
}
