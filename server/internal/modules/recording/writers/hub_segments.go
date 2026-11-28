package writers

import (
	"encoding/json"
	"os"
	"slices"
	"time"
)

// cleanHubSegments are the hub's sentences as hub_segments.json keeps them: mended as the live log's are (cleanLive),
// empty ones left out, in time order, Source SourceHub, Persona's echo kept with its mark (Echo, echo.go); at most as
// many and as long as an upload's.
func cleanHubSegments(segs []RecordingSegment) []RecordingSegment {
	out := make([]RecordingSegment, 0, len(segs))
	total := 0
	for _, sg := range segs {
		l := cleanLive(LiveSegment{Start: sg.Start, End: sg.End, Text: sg.Text})
		if l.Text == "" {
			continue
		}
		if len(out) == maxRecordingSegments || total+len(l.Text) > maxRecordingText {
			break
		}
		total += len(l.Text)
		out = append(out, RecordingSegment{Start: l.Start, End: l.End, Text: l.Text, Source: SourceHub, Echo: sg.Echo})
	}
	slices.SortStableFunc(out, func(a, b RecordingSegment) int {
		switch {
		case a.Start < b.Start:
			return -1
		case a.Start > b.Start:
			return 1
		}
		return 0
	})
	return out
}

// HubTranscriptPut replaces recording rawID's hub transcript with segs (cleanHubSegments) from pass (HubPassLive or
// HubPassFinal), renders transcript.md again from the transcript of record, counts its characters in meta.json's
// chars (and plans the minutes again with them) and indexes the recording; rev is the transcript's revision after it.
// ErrNotFound when the recording was deleted or trimmed (also meanwhile), ErrReadOnly, ErrNotHub when it is not
// transcribed on the hub, ErrTranscriptionFinal for a live pass once a final one is stored.
func (e *engine) HubTranscriptPut(rawID, pass string, segs []RecordingSegment) (rev int64, err error) {
	return e.HubTranscriptPutWithTiming(rawID, pass, "", segs)
}

// HubTranscriptPutWithTiming is HubTranscriptPut that also records hub_segments.json's timing reliability (when valid;
// empty preserves any previously stored timing, absent reads as TimingSpread). When a speaker timeline is already
// stored, stats, minor marks and quality are recomputed under the store locks.
func (e *engine) HubTranscriptPutWithTiming(rawID, pass string, timing TranscriptTiming, segs []RecordingSegment) (rev int64, err error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return 0, ErrNotFound
	}
	if pass != HubPassLive && pass != HubPassFinal {
		return 0, badRecording("a hub transcript's pass is live or final")
	}
	if timing != "" && !timing.Valid() {
		return 0, badRecording("a hub transcript's timing must be exact, turn or spread")
	}
	clean := cleanHubSegments(segs)
	st := e.Store
	var spParts []string
	defer func() {
		if len(spParts) > 0 {
			e.meetingChanged(id, spParts)
		}
	}()
	st.recMu.Lock()
	defer st.recMu.Unlock()
	if st.ReadOnly() {
		return 0, ErrReadOnly
	}
	r, ok := st.Recording(id)
	if gone := st.tombstoned(id); gone || !ok || r.Trimmed() {
		return 0, ErrNotFound
	}
	if !r.HubTranscribed() {
		return 0, ErrNotHub
	}
	h, record, err := st.putHubLocked(r, pass, timing, clean)
	if err != nil {
		return 0, err
	}
	if chars := recordChars(record); chars != r.Chars {
		conv := r.Conv
		r.Chars = chars
		planMinutes(r)
		if err := st.putEntity(KindRecording, r.ID, r, &r.Tagging, false); err != nil {
			return h.Rev, err
		}
		r.Conv = conv
	}
	if parts, minChanged, err := e.recomputeSpeakersLocked(r, 0); err != nil {
		e.logf("recording %s: speakers recompute after hub transcript: %v", id, err)
	} else {
		spParts = parts
		if minChanged {
			st.notify(KindRecording, id, "summary")
		}
	}
	st.notify(KindRecording, id, "messages")
	e.index(KindRecording, id, r.Name, r.Tagging, r.OneLine, r.StartedAt)
	return h.Rev, nil
}

// putHubLocked writes recording r's hub_segments.json (segs, cleaned, from pass) and transcript.md from the transcript
// of record after it, not when the recording was deleted or trimmed meanwhile (ErrNotFound). The caller holds recMu.
func (s *store) putHubLocked(r *Recording, pass string, timing TranscriptTiming, segs []RecordingSegment) (HubTranscript, []RecordingSegment, error) {
	s.recFilesMu.Lock()
	defer s.recFilesMu.Unlock()
	if err := s.recoverRecordingLocked(r.ID); err != nil {
		return HubTranscript{}, nil, err
	}
	if !s.photosHereLocked(r.ID) || s.trimmedLocked(r.ID) {
		return HubTranscript{}, nil, ErrNotFound
	}
	h, err := s.readHubLocked(r.ID)
	if err != nil {
		// Unreadable: replaced, its revision counted on from the time (above any it can have had).
		h = HubTranscript{Rev: time.Now().Unix()}
	}
	if h.Pass == HubPassFinal && pass == HubPassLive {
		return HubTranscript{}, nil, ErrTranscriptionFinal // a late live flush never replaces the final pass's
	}
	if timing == "" {
		timing = h.Timing
	}
	h = HubTranscript{Rev: h.Rev + 1, Pass: pass, Timing: timing, Segments: segs}
	raw, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return HubTranscript{}, nil, err
	}
	if err := s.writeRecordingFile(s.path(KindRecording, r.ID, hubSegmentsFile), append(raw, '\n')); err != nil {
		return HubTranscript{}, nil, err
	}
	device, err := s.diskSegmentsLocked(r.ID)
	if err != nil {
		return h, nil, err
	}
	record := s.TranscriptOfRecord(true, device, segs)
	md := []byte(s.renderTranscript(r, record))
	return h, record, s.writeRecordingFile(s.path(KindRecording, r.ID, "transcript.md"), md)
}

// diskSegmentsLocked is recording id's segments.json as committed. The caller holds recFilesMu and recovered it.
func (s *store) diskSegmentsLocked(id string) ([]RecordingSegment, error) {
	var segs []RecordingSegment
	raw, err := os.ReadFile(s.path(KindRecording, id, "segments.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return segs, json.Unmarshal(raw, &segs)
}

// trimmedLocked: recording id's meta.json says it was trimmed. The caller holds recFilesMu.
func (s *store) trimmedLocked(id string) bool {
	var trim struct {
		TrimmedAt *time.Time `json:"trimmed_at"`
	}
	raw, err := os.ReadFile(s.path(KindRecording, id, "meta.json"))
	return err == nil && json.Unmarshal(raw, &trim) == nil && trim.TrimmedAt != nil
}

// RecordOf is recording rawID's transcript of record (the final pass's reference frames): ErrRecordingDeleted after a
// delete, ErrNotFound with no upload of it.
func (e *engine) RecordOf(rawID string) ([]RecordingSegment, error) {
	id, ok := RecordingID(rawID)
	if !ok {
		return nil, ErrNotFound
	}
	if gone := e.Store.tombstoned(id); gone {
		return nil, ErrRecordingDeleted
	}
	files, m, h, err := e.Store.recordSnapshot(id)
	if err != nil {
		return nil, err
	}
	if files["meta.json"] == nil {
		return nil, ErrNotFound
	}
	return nonNilSegments(e.Store.recordOf(files, m, h)), nil
}

// repairRecord mends hub-mode recording id after a crash between hub_segments.json and transcript.md (HubTranscriptPut)
// or meta.json: transcript.md and chars made what the transcript of record says, each only when it does not already.
// Whether anything changed. The caller holds recMu.
func (e *engine) repairRecord(r *Recording) (bool, error) {
	st := e.Store
	if !r.HubTranscribed() || r.Trimmed() {
		return false, nil
	}
	st.recFilesMu.Lock()
	if err := st.recoverRecordingLocked(r.ID); err != nil {
		st.recFilesMu.Unlock()
		return false, err
	}
	if !st.photosHereLocked(r.ID) || st.trimmedLocked(r.ID) {
		st.recFilesMu.Unlock()
		return false, nil
	}
	device, err := st.diskSegmentsLocked(r.ID)
	if err != nil {
		st.recFilesMu.Unlock()
		return false, err
	}
	record := st.recordLocked(r, device)
	want := st.renderTranscript(r, record)
	path := st.path(KindRecording, r.ID, "transcript.md")
	changed := false
	if raw, err := os.ReadFile(path); err != nil || string(raw) != want {
		if err := st.writeRecordingFile(path, []byte(want)); err != nil {
			st.recFilesMu.Unlock()
			return false, err
		}
		changed = true
	}
	st.recFilesMu.Unlock()
	if chars := recordChars(record); chars != r.Chars {
		r.Chars = chars
		planMinutes(r)
		if err := st.putRecordingMeta(r); err != nil {
			return changed, err
		}
		changed = true
	}
	return changed, nil
}
