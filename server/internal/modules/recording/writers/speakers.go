package writers

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/recordingwire"
)

// EditSpeakers applies speaker roster edits (name, merge, unmerge, clear) atomically to recording rawID under the
// store lock order recMu -> meetMu -> recFilesMu (docs/assistant/meeting-minutes.md "Speakers (#17674)"). It bumps
// PartSpeakers, writes meeting.json (and removes speaker_turns.json on clear), and emits MeetingChanged + ChangeSummary
// after releasing all locks. ownerName is used when an op sets "me": true with an empty name.
func (e *engine) EditSpeakers(rawID string, rev int64, ops []SpeakerEditOp, ownerName string) ([]SpeakerEditOp, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, ErrNotFound
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("%w: ops is required", ErrBadSpeakers)
	}
	allClear := true
	for _, op := range ops {
		if op.Op != SpeakerOpClear {
			allClear = false
			break
		}
	}

	st := e.Store
	changed := false
	var undo []SpeakerEditOp

	err := func() error {
		st.recMu.Lock()
		defer st.recMu.Unlock()
		if st.tombstoned(id) {
			return ErrRecordingDeleted
		}
		r, ok := st.Recording(id)
		if !ok {
			return ErrNotFound
		}
		if st.ReadOnly() {
			return ErrReadOnly
		}
		if !r.IsMeeting() || !r.HubTranscribed() {
			return ErrNotMeeting
		}

		st.meetMu.Lock()
		defer st.meetMu.Unlock()

		var before Meeting
		if m, err := st.readMeeting(id); err != nil {
			return err
		} else if m != nil {
			before = m.Clone()
		}
		m := before.Clone()

		curRev := int64(0)
		if m.Revs != nil {
			curRev = m.Revs[PartSpeakers]
		}
		if m.Speakers != nil && m.Speakers.State == SpeakerStateCleared {
			if !allClear {
				return ErrSpeakersCleared
			}
			if rev != curRev {
				return ErrSpeakersStale
			}
			undo = []SpeakerEditOp{}
			return nil
		}
		if rev != curRev {
			return ErrSpeakersStale
		}
		if m.Speakers == nil {
			if allClear {
				// Contract state table: no block + clear -> no change.
				undo = []SpeakerEditOp{}
				return nil
			}
			// Delegate to domain rules so non-clear ops fail with the canonical bad-request error.
			_, err := st.core.ApplySpeakerEdit(recordingwire.SpeakerEditInput{
				Speakers: MeetingSpeakers{},
				Ops:      ops,
			})
			return err
		}

		resolved := slices.Clone(ops)
		owner := strings.TrimSpace(ownerName)
		if owner == "" {
			owner = strings.TrimSpace(e.writingOwner("").Owner)
		}
		for i := range resolved {
			if resolved[i].Op == SpeakerOpName && resolved[i].Me && strings.TrimSpace(resolved[i].Name) == "" {
				resolved[i].Name = owner
			}
		}

		out, err := st.core.ApplySpeakerEdit(recordingwire.SpeakerEditInput{
			Speakers: *m.Speakers,
			Ops:      resolved,
		})
		if err != nil {
			return err
		}

		m.Speakers.Next = out.Next
		m.Speakers.People = out.People
		m.Speakers.Edits = out.Edits
		if out.Cleared {
			m.Speakers.State = SpeakerStateCleared
			m.Speakers.Done = 0
			m.Speakers.Reason = ""
			m.Speakers.Quality = ""
			m.Speakers.QualityWhy = ""
			m.Speakers.People = nil
			m.Speakers.Stats = nil
		}
		bumpRevs(&m, []string{PartSpeakers})

		raw, err := json.MarshalIndent(&m, "", "  ")
		if err != nil {
			return err
		}

		st.recFilesMu.Lock()
		defer st.recFilesMu.Unlock()
		if st.tombstoned(id) {
			return ErrRecordingDeleted
		}
		if _, err := os.Stat(st.path(KindRecording, id)); err != nil {
			return ErrNotFound
		}
		if err := st.writeRecordingFile(st.path(KindRecording, id, meetingFile), append(raw, '\n')); err != nil {
			return err
		}
		if out.Cleared {
			if err := os.Remove(st.path(KindRecording, id, speakerTurnsFile)); err != nil && !os.IsNotExist(err) {
				return err
			}
			if _, err := e.rerenderMinutesLocked(r, &m, nil, "", r.Trimmed()); err != nil {
				return err
			}
		} else if r.Trimmed() {
			// Explicit trimmed re-render path: never reads transcript, hub segments, or speaker_turns.json.
			if _, err := e.rerenderMinutesLocked(r, &m, nil, "", true); err != nil {
				return err
			}
		} else if m.Speakers.State == SpeakerStateReady {
			turns, _ := st.readSpeakerTurnsLocked(id)
			hub, _ := st.readHubLocked(id)
			if _, err := e.rerenderMinutesLocked(r, &m, turns, hub.EffectiveTiming(), false); err != nil {
				return err
			}
		}
		changed = true
		undo = out.Undo
		if undo == nil {
			undo = []SpeakerEditOp{}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if changed {
		e.meetingChanged(id, []string{PartSpeakers})
		st.notify(KindRecording, id, "summary")
		e.afterSpeakerEdit(id)
	}
	return undo, nil
}

// ImportSpeakerTurns imports a speaker timeline into recording rawID under the store lock order recMu -> meetMu ->
// recFilesMu (PUT /api/v1/recordings/{id}/speakers/turns, debug only). Same run + same body is idempotent (changed
// false, nil error); same run + different body returns ErrOpIDReused.
func (e *engine) ImportSpeakerTurns(rawID string, req SpeakerTurnsImportRequest, now time.Time) (bool, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return false, ErrNotFound
	}
	if err := validateSpeakerImportRequest(req); err != nil {
		return false, err
	}
	reqDigest, err := speakerImportDigest(req)
	if err != nil {
		return false, err
	}

	st := e.Store
	changed := false

	err = func() error {
		st.recMu.Lock()
		defer st.recMu.Unlock()
		if st.tombstoned(id) {
			return ErrRecordingDeleted
		}
		r, ok := st.Recording(id)
		if !ok {
			return ErrNotFound
		}
		if st.ReadOnly() {
			return ErrReadOnly
		}
		if !r.IsMeeting() || !r.HubTranscribed() {
			return ErrNotMeeting
		}
		if r.Trimmed() {
			return ErrTranscriptExpired
		}

		st.meetMu.Lock()
		defer st.meetMu.Unlock()

		var before Meeting
		if m, err := st.readMeeting(id); err != nil {
			return err
		} else if m != nil {
			before = m.Clone()
		}
		m := before.Clone()
		if m.Speakers != nil && m.Speakers.State == SpeakerStateCleared {
			return ErrSpeakersCleared
		}

		st.recFilesMu.Lock()
		defer st.recFilesMu.Unlock()
		if err := st.recoverRecordingLocked(id); err != nil {
			return err
		}
		if st.tombstoned(id) {
			return ErrRecordingDeleted
		}
		if !st.photosHereLocked(id) {
			return ErrNotFound
		}
		if st.trimmedLocked(id) {
			return ErrTranscriptExpired
		}

		prevTurnsFile, err := st.readSpeakerTurnsLocked(id)
		if err != nil {
			prevTurnsFile = nil
		}
		if prevTurnsFile != nil && prevTurnsFile.Run == req.Run {
			if prevTurnsFile.RunDigest == reqDigest {
				return nil // idempotent retry of the exact same run and body
			}
			return ErrOpIDReused
		}

		hub, err := st.readHubLocked(id)
		if err != nil {
			hub = HubTranscript{Segments: []RecordingSegment{}}
		}
		device, err := st.diskSegmentsLocked(id)
		if err != nil {
			return err
		}
		record := st.TranscriptOfRecord(true, device, hub.Segments)

		var prevTurns []SpeakerTurn
		if prevTurnsFile != nil {
			prevTurns = prevTurnsFile.Turns
		}
		attendees := 0
		if r.Calendar != nil {
			attendees = len(r.Calendar.Attendees)
		}
		out, err := st.core.ImportSpeakerTimeline(recordingwire.SpeakerImportInput{
			PrevTurns:    prevTurns,
			PrevSpeakers: m.Speakers,
			RawTurns:     req.Turns,
			Covered:      req.Covered,
			Duration:     speakerRecordingDuration(r, record),
			Segments:     record,
			Timing:       hub.EffectiveTiming(),
			Attendees:    attendees,
		})
		if err != nil {
			return err
		}

		turnsFile := SpeakerTurnsFile{
			Run:       req.Run,
			Source:    req.Source,
			Engine:    strings.TrimSpace(req.Engine),
			Covered:   out.Covered,
			Turns:     out.Turns,
			Signals:   maps.Clone(req.Signals),
			RunDigest: reqDigest,
		}
		turnsRaw, err := json.MarshalIndent(turnsFile, "", "  ")
		if err != nil {
			return err
		}

		tries := 1
		if m.Speakers != nil && m.Speakers.Tries > 0 {
			tries = m.Speakers.Tries
		}
		m.Speakers = &MeetingSpeakers{
			State:      SpeakerStateReady,
			Since:      now.UTC(),
			Tries:      tries,
			Quality:    out.Quality,
			QualityWhy: out.QualityWhy,
			Next:       out.Next,
			People:     out.People,
			Stats:      out.Stats,
			Edits:      out.Edits,
		}
		bumpRevs(&m, []string{PartSpeakers})
		meetingRaw, err := json.MarshalIndent(&m, "", "  ")
		if err != nil {
			return err
		}

		// Write speaker_turns.json before meeting.json so readers never see state=ready without the matching timeline.
		if err := st.writeRecordingFile(st.path(KindRecording, id, speakerTurnsFile), append(turnsRaw, '\n')); err != nil {
			return err
		}
		if err := st.writeRecordingFile(st.path(KindRecording, id, meetingFile), append(meetingRaw, '\n')); err != nil {
			return err
		}
		if _, err := e.rerenderMinutesLocked(r, &m, &turnsFile, hub.EffectiveTiming(), false); err != nil {
			return err
		}
		changed = true
		return nil
	}()
	if err != nil {
		return false, err
	}
	if changed {
		e.meetingChanged(id, []string{PartSpeakers})
		st.notify(KindRecording, id, "summary")
		e.afterSpeakerTimelineStored(id)
	}
	return changed, nil
}

// recomputeSpeakersLocked cuts turns/covered past trimTo (when trimTo > 0), recomputes stats, minor marks and
// quality against the current transcript of record, and re-renders stored minutes without calling a model. Caller
// holds recMu; acquires meetMu -> recFilesMu.
func (e *engine) recomputeSpeakersLocked(r *Recording, trimTo float64) ([]string, bool, error) {
	if r == nil || r.Trimmed() {
		return nil, false, nil
	}
	st := e.Store
	st.meetMu.Lock()
	defer st.meetMu.Unlock()

	mPtr, err := st.readMeeting(r.ID)
	if err != nil || mPtr == nil || !hubMode(mPtr) || mPtr.Speakers == nil || mPtr.Speakers.State != SpeakerStateReady {
		return nil, false, err
	}
	before := mPtr.Clone()
	m := before.Clone()

	st.recFilesMu.Lock()
	defer st.recFilesMu.Unlock()
	if st.tombstoned(r.ID) || !st.photosHereLocked(r.ID) || st.trimmedLocked(r.ID) {
		return nil, false, nil
	}
	turnsFile, err := st.readSpeakerTurnsLocked(r.ID)
	if err != nil || turnsFile == nil {
		return nil, false, err
	}
	hub, err := st.readHubLocked(r.ID)
	if err != nil {
		hub = HubTranscript{Segments: []RecordingSegment{}}
	}
	device, err := st.diskSegmentsLocked(r.ID)
	if err != nil {
		return nil, false, err
	}
	record := st.TranscriptOfRecord(true, device, hub.Segments)
	attendees := 0
	if r.Calendar != nil {
		attendees = len(r.Calendar.Attendees)
	}
	out, err := st.core.RecomputeSpeakerStats(recordingwire.SpeakerRecomputeInput{
		Turns:     turnsFile.Turns,
		Covered:   turnsFile.Covered,
		Speakers:  *m.Speakers,
		TrimTo:    trimTo,
		Duration:  speakerRecordingDuration(r, record),
		Segments:  record,
		Timing:    hub.EffectiveTiming(),
		Attendees: attendees,
	})
	if err != nil {
		return nil, false, err
	}

	turnsChanged := false
	effectiveTurns := turnsFile
	if trimTo > 0 {
		updatedTurns := turnsFile.Clone()
		updatedTurns.Turns = out.Turns
		updatedTurns.Covered = out.Covered
		oldJSON, _ := json.Marshal(turnsFile)
		newJSON, _ := json.Marshal(updatedTurns)
		if !bytes.Equal(oldJSON, newJSON) {
			turnsRaw, err := json.MarshalIndent(updatedTurns, "", "  ")
			if err != nil {
				return nil, false, err
			}
			if err := st.writeRecordingFile(st.path(KindRecording, r.ID, speakerTurnsFile), append(turnsRaw, '\n')); err != nil {
				return nil, false, err
			}
			turnsChanged = true
			effectiveTurns = &updatedTurns
		}
	}

	m.Speakers.People = out.People
	m.Speakers.Stats = out.Stats
	m.Speakers.Quality = out.Quality
	m.Speakers.QualityWhy = out.QualityWhy

	var parts []string
	if turnsChanged || !meetingSame(before, m) {
		bumpRevs(&m, []string{PartSpeakers})
		meetingRaw, err := json.MarshalIndent(&m, "", "  ")
		if err != nil {
			return nil, false, err
		}
		if err := st.writeRecordingFile(st.path(KindRecording, r.ID, meetingFile), append(meetingRaw, '\n')); err != nil {
			return nil, false, err
		}
		r.Conv = &m
		parts = []string{PartSpeakers}
	}
	minChanged, err := e.rerenderMinutesLocked(r, &m, effectiveTurns, hub.EffectiveTiming(), false)
	if err != nil {
		return parts, false, err
	}
	return parts, minChanged, nil
}

// meetingSpeakersOf returns m.Speakers or nil.
func meetingSpeakersOf(m *Meeting) *MeetingSpeakers {
	if m == nil {
		return nil
	}
	return m.Speakers
}

// rerenderMinutesLocked recomputes debate speaker attribution and re-renders minutes.json and minutes.md without
// calling a model, without pushing, and without bumping minutes.version. Caller holds recMu -> meetMu -> recFilesMu.
// When trimmed is true, it never touches transcript files and recomputes only from stored position overlaps and roster.
func (e *engine) rerenderMinutesLocked(r *Recording, meeting *Meeting, turns *SpeakerTurnsFile, timing TranscriptTiming, trimmed bool) (bool, error) {
	if r == nil {
		return false, nil
	}
	st := e.Store
	raw, err := os.ReadFile(st.path(KindRecording, r.ID, "minutes.json"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var m Minutes
	if err := json.Unmarshal(raw, &m); err != nil {
		return false, err
	}
	beforeJSON, err := json.Marshal(&m)
	if err != nil {
		return false, err
	}
	speakers := meetingSpeakersOf(meeting)
	e.attributeDebates(&m, speakers, turns, timing, trimmed)
	m.Markdown = e.renderMinutes(&m, r.StartedAt, st.Zone(), speakers)
	afterJSON, err := json.Marshal(&m)
	if err != nil {
		return false, err
	}
	wantMD := strings.TrimSpace(m.Markdown) + "\n"
	if bytes.Equal(beforeJSON, afterJSON) {
		if curMD, err := os.ReadFile(st.path(KindRecording, r.ID, "minutes.md")); err == nil && string(curMD) == wantMD {
			return false, nil
		}
	}
	return st.putMinutesLocked(r.ID, &m)
}

// expireSpeakersWaiting marks recording id's speakers state failed with reason timeout once the wait limit has passed,
// only if meeting.json is still on the exact same waiting attempt (wantSince and wantTries), so a ready timeline stored
// moments earlier is never overwritten. Returns the current Meeting state on disk.
func (e *engine) expireSpeakersWaiting(id string, wantSince time.Time, wantTries int) (*Meeting, error) {
	st := e.Store
	changed := false
	var current *Meeting
	err := func() error {
		st.recMu.Lock()
		defer st.recMu.Unlock()
		if st.tombstoned(id) || st.ReadOnly() {
			return nil
		}
		if _, ok := st.Recording(id); !ok {
			return nil
		}
		st.meetMu.Lock()
		defer st.meetMu.Unlock()
		m, err := st.readMeeting(id)
		if err != nil || m == nil {
			return err
		}
		current = m
		if m.Speakers == nil || m.Speakers.State != SpeakerStateWaiting ||
			!m.Speakers.Since.Equal(wantSince) || m.Speakers.Tries != wantTries {
			return nil
		}
		next := m.Clone()
		next.Speakers.State = SpeakerStateFailed
		next.Speakers.Reason = SpeakerFailTimeout
		next.Speakers.Done = 0
		bumpRevs(&next, []string{PartSpeakers})
		raw, err := json.MarshalIndent(&next, "", "  ")
		if err != nil {
			return err
		}
		st.recFilesMu.Lock()
		defer st.recFilesMu.Unlock()
		if st.tombstoned(id) {
			return nil
		}
		if _, err := os.Stat(st.path(KindRecording, id)); err != nil {
			return nil
		}
		if err := st.writeRecordingFile(st.path(KindRecording, id, meetingFile), append(raw, '\n')); err != nil {
			return err
		}
		current = &next
		changed = true
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if changed {
		e.meetingChanged(id, []string{PartSpeakers})
	}
	return current, nil
}

// afterSpeakerEdit and afterSpeakerTimelineStored are the post-commit hook points for PR 2-4 (minutes attribution
// re-render without a model call and waking the minutes round).
func (e *engine) afterSpeakerEdit(id string) {
	if e.SpeakersChanged != nil {
		e.SpeakersChanged(id)
	}
}

func (e *engine) afterSpeakerTimelineStored(id string) {
	if e.SpeakersChanged != nil {
		e.SpeakersChanged(id)
	}
}

func validateSpeakerImportRequest(req SpeakerTurnsImportRequest) error {
	if len(req.Turns) > MaxSpeakerTurns {
		return fmt.Errorf("%w: at most %d turns", ErrSpeakersTooLarge, MaxSpeakerTurns)
	}
	if strings.TrimSpace(string(req.Run)) == "" || strings.ContainsAny(string(req.Run), "\n\r") {
		return fmt.Errorf("%w: run is required", ErrBadSpeakers)
	}
	if strings.TrimSpace(req.Engine) == "" || strings.ContainsAny(req.Engine, "\n\r") {
		return fmt.Errorf("%w: engine is required", ErrBadSpeakers)
	}
	if !req.Source.Valid() {
		return fmt.Errorf("%w: source must be hub", ErrBadSpeakers)
	}
	for _, sp := range req.Covered {
		if !finite(sp[0]) || !finite(sp[1]) || sp[0] < 0 || sp[1] < sp[0] {
			return fmt.Errorf("%w: invalid covered span", ErrBadSpeakers)
		}
	}
	for _, t := range req.Turns {
		if !finite(t.Start) || !finite(t.End) || t.Start < 0 || t.End <= t.Start || strings.TrimSpace(string(t.Cluster)) == "" {
			return fmt.Errorf("%w: invalid turn", ErrBadSpeakers)
		}
	}
	return nil
}

func speakerImportDigest(req SpeakerTurnsImportRequest) (string, error) {
	canon := struct {
		Run     SpeakerRunID             `json:"run"`
		Engine  string                   `json:"engine"`
		Source  SpeakerSource            `json:"source"`
		Covered []SpeakerSpan            `json:"covered"`
		Turns   []RawSpeakerTurn         `json:"turns"`
		Signals map[string]SpeakerSignal `json:"signals,omitempty"`
	}{
		Run:     req.Run,
		Engine:  strings.TrimSpace(req.Engine),
		Source:  req.Source,
		Covered: req.Covered,
		Turns:   req.Turns,
		Signals: req.Signals,
	}
	b, err := json.Marshal(canon)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func speakerRecordingDuration(r *Recording, segs []RecordingSegment) float64 {
	if r != nil {
		if r.Duration > 0 {
			return r.Duration
		}
		if r.EndedAt != nil && !r.StartedAt.IsZero() && r.EndedAt.After(r.StartedAt) {
			return r.EndedAt.Sub(r.StartedAt).Seconds()
		}
	}
	maxEnd := 0.0
	for _, sg := range segs {
		if sg.End > maxEnd {
			maxEnd = sg.End
		}
	}
	return maxEnd
}
