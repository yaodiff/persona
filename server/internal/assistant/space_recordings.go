package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// Recordings saved into a track (#13501, docs/assistant/health.md "Tracks and kinds", docs/protocol/protocol.md
// "Recording sync"): an upload naming space goes to that track instead of the knowledge base. The manager renders its
// transcript as a Markdown file headed by the track's categories and has the assistant's machine write it into the
// track's recordings/ (assistant.space_recording, nodeproto.TrackRecordings); assistant_space_recordings keeps its
// revision and tombstone for the phone's sync. Such a recording is never in the knowledge base (kb_list, kb_search,
// minutes, summaries): the track's conversations read the file where it is. Retention is recording_days, as for the
// knowledge base's; a deleted one leaves a tombstone until then.

// spaceRecordingStore is the manager's store of them (db.DB).
type spaceRecordingStore interface {
	SpaceRecording(ctx context.Context, id string) (db.SpaceRecording, bool, error)
	SpaceRecordings(ctx context.Context) ([]db.SpaceRecording, error)
	PutSpaceRecording(ctx context.Context, r db.SpaceRecording) (bool, error)
	DeleteSpaceRecording(ctx context.Context, id string, now time.Time, removed bool) (bool, error)
	SpaceRecordingRemoved(ctx context.Context, id string) error
	ForgetSpaceRecordings(ctx context.Context, cutoff time.Time) (int64, error)
}

var _ spaceRecordingStore = (*db.DB)(nil)

// spaceRecordingBook serializes the writes of these recordings, so two uploads of one never race between the node
// and the store, and spaces the retention passes.
type spaceRecordingBook struct {
	mu    sync.Mutex
	swept time.Time
}

// spaceRecordingSweepEvery is how often the retention pass runs (from instructionsLoop).
const spaceRecordingSweepEvery = 30 * time.Minute

// Errors of a recording saved into a track, each answered with its own status (writeSpaceRecordingError).
var (
	errSpaceRecordingTrack    = i18n.Err("assistant-space_recordings.unknown_space")
	errSpaceRecordingDay      = i18n.Err("assistant-space_recordings.day")
	errSpaceRecordingStore    = i18n.Err("assistant-space_recordings.unavailable")
	errSpaceRecordingOffline  = i18n.Err("assistant-space_recordings.offline")
	errSpaceRecordingOutdated = i18n.Err("assistant-space_recordings.outdated")
	errSpaceRecordingInKB     = i18n.Err("assistant-space_recordings.in_kb")
)

// codeSpaceUnknown: the upload names a track that has no directory here (or none at all).
const codeSpaceUnknown = "space_unknown"

// codeRecordingInKB: the upload names a track for a recording the knowledge base already holds.
const codeRecordingInKB = "recording_in_kb"

func (s *Service) spaceRecordingStore() (spaceRecordingStore, bool) {
	st, ok := s.store().(spaceRecordingStore)
	return st, ok
}

// spaceRecordingOf is the row of recording id when it was saved into a track; false when it was not (or the store
// cannot say: the knowledge base then answers as before).
func (s *Service) spaceRecordingOf(ctx context.Context, id string) (db.SpaceRecording, bool) {
	st, ok := s.spaceRecordingStore()
	if !ok {
		return db.SpaceRecording{}, false
	}
	row, ok, err := st.SpaceRecording(ctx, id)
	if err != nil {
		s.logf("space recordings: read %s: %v", id, err)
		return db.SpaceRecording{}, false
	}
	return row, ok
}

// inSpaceRecording: recording rawID was saved into a track (a tombstone of one too).
func (s *Service) inSpaceRecording(ctx context.Context, rawID string) bool {
	id, ok := kb.RecordingID(rawID)
	if !ok {
		return false
	}
	_, ok = s.spaceRecordingOf(ctx, id)
	return ok
}

// putSpaceRecordingRoute answers PUT /api/v1/recordings/{id} for upload u of a recording saved into a track, in the
// shape of the knowledge base's answer: attending is always off (a track's recording is not attended), the
// transcription the phone's own, and space names the track.
func (s *Service) putSpaceRecordingRoute(w http.ResponseWriter, r *http.Request, u recordingwire.RecordingUpload, clean recordingwire.UploadCleaner) {
	status, slug, err := s.putSpaceRecording(r.Context(), r.PathValue("id"), u, time.Now(), clean)
	if err != nil {
		writeSpaceRecordingError(w, err)
		return
	}
	id, _ := kb.RecordingID(r.PathValue("id"))
	out := map[string]any{"id": id, "revision": u.Revision, "status": status, "attendance": recordingwire.AttendanceOff, "space": slug}
	if strings.EqualFold(strings.TrimSpace(u.Kind), kb.RecordingMeeting) {
		out["transcription"] = kb.TranscriptionDevice
	}
	apierr.WriteJSON(w, out)
}

// trackRecordingsNode: machine's node writes a track's recordings (nodeproto.TrackRecordings).
func (s *Service) trackRecordingsNode(machine string) bool {
	nodes, ok := s.physicalDevices().(nodeAnnouncer)
	return ok && nodes.NodeAnnounced(machine, nodeproto.TrackRecordings)
}

// spaceRecordingMachine is the assistant's machine when it can write a track's recordings now.
func (s *Service) spaceRecordingMachine(ctx context.Context) (string, error) {
	machine := s.settings(ctx).MachineID
	if machine == "" || s.isWorkMachine(machine) {
		return "", errSpaceRecordingOffline
	}
	if d, ok := s.device(ctx, machine); !ok || !d.Online {
		return "", errSpaceRecordingOffline
	}
	if !s.trackRecordingsNode(machine) {
		return "", errSpaceRecordingOutdated
	}
	return machine, nil
}

// spaceRecordingName is the transcript's file name: the day it started in the owner's zone and its UUID, so the
// track's recordings/ sorts by day.
func spaceRecordingName(id string, started time.Time, loc *time.Location) string {
	return started.In(loc).Format("2006-01-02") + "-" + id + ".md"
}

// putSpaceRecording saves upload u of recording rawID into its track: recordingwire.RecordingStored, RecordingStale (a higher
// revision is there) or RecordingExpired (it started before recording_text_days); kb.ErrRecordingDeleted after a delete.
// A recording saved into a track stays in it: a later upload goes there whatever it names. The answer's slug is the
// track it is in.
func (s *Service) putSpaceRecording(ctx context.Context, rawID string, u recordingwire.RecordingUpload, now time.Time, clean recordingwire.UploadCleaner) (status, slug string, err error) {
	id, ok := kb.RecordingID(rawID)
	if !ok {
		return "", "", fmt.Errorf("%w: id must be a UUID", kb.ErrBadRecording)
	}
	if strings.EqualFold(strings.TrimSpace(u.Kind), kb.RecordingDay) {
		return "", "", errSpaceRecordingDay // an all-day recording has its own recap in the knowledge base
	}
	if err := clean(&u); err != nil {
		return "", "", err
	}
	// One the knowledge base holds stays there (a later upload naming a track would make a second copy of it).
	if e, err := s.kbGet(ctx); err == nil && e.HoldsRecording(id) {
		return "", "", errSpaceRecordingInKB
	}
	st, ok := s.spaceRecordingStore()
	if !ok {
		return "", "", errSpaceRecordingStore
	}
	b := &s.spaceRecs
	b.mu.Lock()
	defer b.mu.Unlock()
	row, exists, err := st.SpaceRecording(ctx, id)
	if err != nil {
		return "", "", errSpaceRecordingStore
	}
	loc := s.location(ctx)
	slug, name := strings.TrimSpace(u.Space), spaceRecordingName(id, u.StartedAt, loc)
	if exists {
		if row.DeletedAt != nil {
			return "", row.Slug, kb.ErrRecordingDeleted
		}
		slug, name = row.Slug, row.FileName
		if row.Revision > u.Revision {
			return recordingwire.RecordingStale, slug, nil
		}
	}
	if cut, ok := kb.RetentionCut(now, s.recordingTextDaysNow()); ok && u.StartedAt.Before(cut) {
		return recordingwire.RecordingExpired, slug, nil
	}
	e, ok := s.spaceTrack(ctx, slug)
	if !ok {
		return "", slug, errSpaceRecordingTrack
	}
	machine, err := s.spaceRecordingMachine(ctx)
	if err != nil {
		return "", slug, err
	}
	cats, _ := s.trackFloor(ctx, slug)
	content := renderSpaceRecording(id, u, cats, loc)
	params := map[string]any{"dir": trackRel(e), "slug": slug, "name": name, "content": content}
	if err := s.call(ctx, machine, assistantwire.DeviceAssistantSpaceRecording, params, nil); err != nil {
		return "", slug, err
	}
	wrote, err := st.PutSpaceRecording(ctx, db.SpaceRecording{ID: id, Slug: slug, FileName: name, Revision: u.Revision,
		StartedAt: u.StartedAt.UTC(), Complete: u.Complete, UpdatedAt: now.UTC()})
	if err != nil {
		return "", slug, errSpaceRecordingStore
	}
	if !wrote {
		return recordingwire.RecordingStale, slug, nil
	}
	return recordingwire.RecordingStored, slug, nil
}

// deleteSpaceRecording deletes recording row (saved into a track): its tombstone now, its file removed on the
// assistant's machine when that is reachable, else by the retention pass later. false: it was deleted already.
func (s *Service) deleteSpaceRecording(ctx context.Context, row db.SpaceRecording, now time.Time) (bool, error) {
	st, ok := s.spaceRecordingStore()
	if !ok {
		return false, errSpaceRecordingStore
	}
	b := &s.spaceRecs
	b.mu.Lock()
	defer b.mu.Unlock()
	removed := s.removeSpaceRecordingFile(ctx, row) == nil
	deleted, err := st.DeleteSpaceRecording(ctx, row.ID, now.UTC(), removed)
	if err != nil {
		return false, errSpaceRecordingStore
	}
	return deleted, nil
}

// removeSpaceRecordingFile removes row's transcript from its track's recordings/ (nil when it is gone there).
func (s *Service) removeSpaceRecordingFile(ctx context.Context, row db.SpaceRecording) error {
	e, ok := s.trackEntryOf(ctx, row.Slug)
	if !ok || e.DirName == "" {
		return nil // the track is gone from the registry: nothing of it to reach
	}
	machine, err := s.spaceRecordingMachine(ctx)
	if err != nil {
		return err
	}
	params := map[string]any{"dir": trackRel(e), "slug": row.Slug, "name": row.FileName, "remove": true}
	return s.call(ctx, machine, assistantwire.DeviceAssistantSpaceRecording, params, nil)
}

// spaceRecordingStates are the recordings saved into a track as the phone's reconciliation lists them, and their
// tombstones (GET /api/v1/recordings).
func (s *Service) spaceRecordingStates(ctx context.Context) (kept []recordingwire.RecordingState, deleted []string) {
	st, ok := s.spaceRecordingStore()
	if !ok {
		return nil, nil
	}
	rows, err := st.SpaceRecordings(ctx)
	if err != nil {
		s.logf("space recordings: list: %v", err)
		return nil, nil
	}
	for _, r := range rows {
		if r.DeletedAt != nil {
			deleted = append(deleted, r.ID)
			continue
		}
		kept = append(kept, recordingwire.RecordingState{ID: r.ID, Revision: r.Revision, Complete: r.Complete, StartedAt: r.StartedAt, Space: r.Slug})
	}
	return kept, deleted
}

// spaceRecordingsSweep applies recording_text_days to the recordings saved into tracks (their file is text): one that
// started before it is deleted (its file removed, its tombstone kept), a deletion whose file is still there is tried
// again, and tombstones past the audio window (recording_audio_days, the phone's, as in the knowledge base) whose file
// is gone are forgotten. At most once a spaceRecordingSweepEvery.
func (s *Service) spaceRecordingsSweep(ctx context.Context) {
	st, ok := s.spaceRecordingStore()
	if !ok {
		return
	}
	b := &s.spaceRecs
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if now.Sub(b.swept) < spaceRecordingSweepEvery {
		return
	}
	b.swept = now
	rows, err := st.SpaceRecordings(ctx)
	if err != nil {
		s.logf("space recordings: list: %v", err)
		return
	}
	cutoff, textExpires := kb.RetentionCut(now, s.recordingTextDaysNow())
	for _, r := range rows {
		switch {
		case r.DeletedAt == nil && textExpires && r.StartedAt.Before(cutoff):
			removed := s.removeSpaceRecordingFile(ctx, r) == nil
			if _, err := st.DeleteSpaceRecording(ctx, r.ID, now.UTC(), removed); err != nil {
				s.logf("space recordings: expire %s: %v", r.ID, err)
			}
		case r.DeletedAt != nil && !r.FileRemoved:
			if s.removeSpaceRecordingFile(ctx, r) == nil {
				if err := st.SpaceRecordingRemoved(ctx, r.ID); err != nil {
					s.logf("space recordings: removed %s: %v", r.ID, err)
				}
			}
		}
	}
	stoneCut, ok := kb.RetentionCut(now, s.recordingAudioDaysNow())
	if !ok {
		return
	}
	if n, err := st.ForgetSpaceRecordings(ctx, stoneCut); err != nil {
		s.logf("space recordings: forget: %v", err)
	} else if n > 0 {
		s.logf("space recordings: forgot %d tombstone(s) past recording_audio_days", n)
	}
}

// codeRecordingDeleted: the recording was deleted here; the phone drops it instead of uploading it again.
const codeRecordingDeleted = "recording_deleted"

// writeSpaceRecordingError answers an error of putSpaceRecording or deleteSpaceRecording.
func writeSpaceRecordingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, kb.ErrRecordingDeleted):
		apierr.WriteCode(w, http.StatusGone, codeRecordingDeleted, err)
	case errors.Is(err, kb.ErrBadRecording), errors.Is(err, errSpaceRecordingDay):
		apierr.Write(w, http.StatusBadRequest, err)
	case errors.Is(err, errSpaceRecordingTrack):
		apierr.WriteCode(w, http.StatusBadRequest, codeSpaceUnknown, err)
	case errors.Is(err, errSpaceRecordingInKB):
		apierr.WriteCode(w, http.StatusConflict, codeRecordingInKB, err)
	case errors.Is(err, errSpaceRecordingOutdated):
		apierr.WriteCode(w, http.StatusConflict, proto.CodeNodeOutdated, err)
	case errors.Is(err, errSpaceRecordingOffline):
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeMachineOffline, err)
	case errors.Is(err, errSpaceRecordingStore):
		apierr.Write(w, http.StatusServiceUnavailable, err)
	default:
		apierr.Call(w, err) // the node's
	}
}

// renderSpaceRecording is the transcript file of recording id saved into a track: a front matter with what the
// recording is and the categories it carries (the track's), its title, then one line per sentence with its time from
// the start. It is data the track's conversations read, not a prompt.
func renderSpaceRecording(id string, u recordingwire.RecordingUpload, categories []string, loc *time.Location) string {
	var b strings.Builder
	quote := func(v string) string {
		out, _ := json.Marshal(v)
		return string(out)
	}
	cats := slices.Clone(categories)
	slices.Sort(cats)
	quoted := make([]string, len(cats))
	for i, c := range cats {
		quoted[i] = quote(c)
	}
	started := u.StartedAt.In(loc)
	b.WriteString("---\n")
	b.WriteString("recording: " + id + "\n")
	if u.Title != "" {
		b.WriteString("title: " + quote(u.Title) + "\n")
	}
	b.WriteString("kind: " + u.Kind + "\n")
	b.WriteString("started_at: " + started.Format(time.RFC3339) + "\n")
	if u.EndedAt != nil {
		b.WriteString("ended_at: " + u.EndedAt.In(loc).Format(time.RFC3339) + "\n")
	}
	if u.Duration > 0 {
		fmt.Fprintf(&b, "duration_seconds: %d\n", int64(math.Round(u.Duration)))
	}
	if u.Language != "" {
		b.WriteString("language: " + quote(u.Language) + "\n")
	}
	b.WriteString("device: " + u.Device + "\n")
	fmt.Fprintf(&b, "complete: %t\n", u.Complete)
	fmt.Fprintf(&b, "revision: %d\n", u.Revision)
	b.WriteString("categories: [" + strings.Join(quoted, ", ") + "]\n")
	b.WriteString("---\n\n")
	title := u.Title
	if title == "" {
		title = started.Format("2006-01-02 15:04")
	}
	b.WriteString("# " + title + "\n\n")
	for _, sg := range u.Segments {
		b.WriteString("[" + spaceRecordingClock(sg.Start) + "] ")
		if sg.Echo {
			b.WriteString("(Persona) ")
		}
		b.WriteString(strings.Join(strings.Fields(sg.Text), " ") + "\n")
	}
	return b.String()
}

// spaceRecordingClock is seconds from the start as m:ss, or h:mm:ss from an hour on.
func spaceRecordingClock(sec float64) string {
	if sec < 0 || math.IsNaN(sec) || math.IsInf(sec, 0) {
		sec = 0
	}
	t := int64(sec)
	h, m, ss := t/3600, (t%3600)/60, t%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, ss)
	}
	return fmt.Sprintf("%d:%02d", m, ss)
}
