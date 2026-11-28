package kb

// The writes of recordings.go that delete, retention and recovery need: Core, with or without the recording module (docs/plans/plan-kb-recording-port.md,
// section 1; docs/architecture/core-and-modules.md "Recording").

import (
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Re-exported from recordingwire.
const (
	MinutesPending = recordingwire.MinutesPending
	MinutesReady   = recordingwire.MinutesReady
	MinutesFailed  = recordingwire.MinutesFailed
)

func (s *Store) tombstonesPath() string { return filepath.Join(s.Dir, KindRecording, ".deleted.json") }

func (s *Store) tombstones() map[string]time.Time {
	m := map[string]time.Time{}
	_, _ = readJSON(s.tombstonesPath(), &m)
	return m
}

// DeleteRecording removes recording id (files and index) and leaves a tombstone; false when there was none.
func (e *Engine) DeleteRecording(rawID string, now time.Time) (bool, error) {
	if e.Store.ReadOnly() {
		return false, ErrReadOnly
	}
	defer e.Store.SearchIndexPurge() // its summary, minutes and recap leave the search index at its next update
	id, ok := RecordingID(rawID)
	if !ok {
		return false, badRecording("id must be a UUID")
	}
	st := e.Store
	st.recMu.Lock()
	defer st.recMu.Unlock()
	t := st.tombstones()
	if _, ok := t[id]; !ok {
		t[id] = now.UTC()
		if err := writeJSON(st.tombstonesPath(), t); err != nil {
			return false, err
		}
	}
	r, existed := st.Recording(id)
	if !existed {
		return false, nil
	}
	st.recFilesMu.Lock()
	st.recordingAudio().AudioDropped(recordingwire.ID(id), ErrRecordingDeleted)
	err := os.RemoveAll(st.path(KindRecording, id))
	st.recFilesMu.Unlock()
	st.recordingAudio().AudioClosed(recordingwire.ID(id))
	if err != nil {
		return false, err
	}
	e.unlinkRecording(id, r.Projects)
	e.unindex(KindRecording, []string{id})
	st.notify(KindRecording, id, "metadata")
	return true, nil
}

// unlinkRecording takes a gone recording off its projects.
func (e *Engine) unlinkRecording(id string, projects []string) {
	for _, slug := range projects {
		if p, ok := e.Store.Project(slug); ok {
			p.Items = removeStrings(p.Items, []string{KindRecording + "/" + id})
			_ = e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false)
		}
	}
}

// PruneRecordings applies retention to files and the index. A recording's text is kept for recording_text_days
// (RecordingDays; forever by default, when nothing is pruned). Past a finite one, minutes are kept forever or within
// a finite minutes_days window, and the recording is trimmed to them (recording_trim.go); other recordings go.
// Interrupted trims finish whatever the window. Tombstones older than the audio window (AudioDays) go too.
// It returns how many recordings were deleted, not counting trimmed ones. Then the recording module, when there is
// one, deletes the hub-mode audio that should be gone and mends what a crash left of a hub transcript
// (Provider.AfterPrune: sweepTranscriptions). Whether it runs at all without the module is retentionRuns' call
// (RetentionWithoutRecordingModule, recording_port.go).
func (e *Engine) PruneRecordings(now time.Time) int {
	if e.Store.ReadOnly() || !e.retentionRuns() {
		return 0
	}
	n := e.pruneRecordings(now)
	e.afterPrune(now)
	if n > 0 {
		e.Store.SearchIndexPurge() // their summaries, minutes and recaps leave the search index at its next update
	}
	return n
}

func (e *Engine) pruneRecordings(now time.Time) int {
	textDays := e.RecordingDays()
	cut, textExpires := RetentionCut(now, textDays)
	minutesDays := e.MinutesDays()
	minutesCut := now.UTC().AddDate(0, 0, -minutesDays)
	st := e.Store
	// The trims' meeting changes (photos, transcription) are told once recMu is let go (deferred calls run last first).
	told := map[string][]string{}
	var order []string
	tell := func(id string, parts []string) {
		if len(parts) > 0 {
			if told[id] == nil {
				order = append(order, id)
			}
			told[id] = append(told[id], parts...)
		}
	}
	defer func() {
		if len(order) > 0 {
			st.SearchIndexPurge() // a trimmed recording's files, too
		}
		for _, id := range order {
			e.meetingChanged(id, told[id])
		}
	}()
	st.recMu.Lock()
	defer st.recMu.Unlock()
	var gone []string
	for _, r := range st.Recordings() {
		if !textExpires || !r.StartedAt.Before(cut) {
			// Within recording_days only a trimmed one has anything to do: the files an interrupted trim left (a crash
			// after its meta.json, a failed removal) go even once recording_days made longer lets it back in the window.
			if r.Trimmed() {
				tell(r.ID, e.trimPruned(r, now))
			}
			continue
		}
		if (minutesDays == 0 || !r.StartedAt.Before(minutesCut)) && (r.Trimmed() || st.hasMinutes(r.ID) || st.hasRecap(r.ID)) {
			tell(r.ID, e.trimPruned(r, now))
			continue
		}
		st.recFilesMu.Lock()
		st.recordingAudio().AudioDropped(recordingwire.ID(r.ID), ErrNotFound)
		err := os.RemoveAll(st.path(KindRecording, r.ID))
		st.recFilesMu.Unlock()
		st.recordingAudio().AudioClosed(recordingwire.ID(r.ID))
		if err == nil {
			e.unlinkRecording(r.ID, r.Projects)
			st.notify(KindRecording, r.ID, "metadata")
			gone = append(gone, r.ID)
		}
	}
	e.unindex(KindRecording, gone)
	// Tombstones keep the audio window (AudioDays, the phone's), not the text's or minutes_days. One only has to
	// outlive the uploads it refuses, and the phone never uploads a recording older than that window: it drops every
	// one older than days itself (its other use, the phone dropping a copy deleted elsewhere, ends with the window too).
	// A tombstone is never older than its recording's start (it is written when the recording is deleted), so one that
	// falls out of the window stands for a recording no phone still holds. Forever audio keeps them forever.
	t := st.tombstones()
	n := len(t)
	if stoneCut, ok := RetentionCut(now, e.AudioDays()); ok {
		for id, at := range t {
			if at.Before(stoneCut) {
				delete(t, id)
			}
		}
	}
	if len(t) != n {
		_ = writeJSON(st.tombstonesPath(), t)
	}
	return len(gone)
}

// trimPruned trims recording r for PruneRecordings (which holds recMu), or finishes its trim: idempotent, it says
// what changed and indexes the recording again only when something did. parts are the meeting parts that changed
// (PartPhotos, PartTranscription), which the caller tells MeetingChanged once it let recMu go.
func (e *Engine) trimPruned(r *Recording, now time.Time) (parts []string) {
	st := e.Store
	trimmed, photos, err := st.trimRecording(r, now)
	if err != nil {
		e.logf("recording %s: trim: %v", r.ID, err)
	} else if trimmed {
		st.notify(KindRecording, r.ID, "metadata")
		st.notify(KindRecording, r.ID, "messages")
		st.notify(KindRecording, r.ID, "summary")
		e.index(KindRecording, r.ID, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	}
	if photos {
		parts = append(parts, PartPhotos)
	}
	if err == nil {
		parts = append(parts, e.trimTranscription(r.ID, now)...)
	}
	return parts
}

func (e *Engine) unindex(kind string, ids []string) {
	if e.Unindex != nil && len(ids) > 0 {
		e.Unindex(kind, ids)
	}
}
