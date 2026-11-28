package recording

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// Codes of the speaker routes' refusals (docs/assistant/meeting-minutes.md "Speakers (#17674)").
const (
	codeSpeakersStale   = "speakers_stale"
	codeSpeakersCleared = "speakers_cleared"
)

func (s *Recordings) meetingSpeakerRoutes() {
	s.handle("POST /api/v1/recordings/{id}/speakers", s.speakersEditRoute)
	s.handle("PUT /api/v1/recordings/{id}/speakers/turns", s.speakersTurnsImportRoute)
}

func (s *Recordings) speakersEditRoute(w http.ResponseWriter, r *http.Request) {
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	raw, ok := meetingBody(w, r, recordingwire.MaxSpeakerEditBody, "64 KiB")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if s.inSpaceRecording(r.Context(), id) {
		apierr.WriteCode(w, http.StatusConflict, codeNotMeeting, i18n.Err("assistant-meeting.not_meeting"))
		return
	}
	if _, _, ok := meetingRecording(w, e, id); !ok {
		return
	}
	if !s.meetingWritable(r.Context(), e) {
		apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
		return
	}
	var req recordingwire.SpeakerEditRequest
	if err := json.Unmarshal(raw, &req); err != nil || req.Rev < 0 {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_speakers"))
		return
	}
	undo, err := writersOf(e).EditSpeakers(id, req.Rev, req.Ops, s.owner(r.Context()))
	if err != nil {
		writeSpeakerError(w, err)
		return
	}
	speakers, err := e.SpeakerDetail(id)
	if err != nil {
		writeSpeakerError(w, err)
		return
	}
	apierr.WriteJSON(w, recordingwire.SpeakerEditResponse{Speakers: speakers, Undo: undo})
}

func (s *Recordings) speakersTurnsImportRoute(w http.ResponseWriter, r *http.Request) {
	if !s.speakerTurnsImport() {
		http.NotFound(w, r)
		return
	}
	if !meetingCaller(w, r) {
		return
	}
	e := s.meetingEngine(w, r)
	if e == nil {
		return
	}
	raw, ok := meetingBody(w, r, recordingwire.MaxSpeakerTurnsBody, "2 MiB")
	if !ok {
		return
	}
	id := r.PathValue("id")
	if s.inSpaceRecording(r.Context(), id) {
		apierr.WriteCode(w, http.StatusConflict, codeNotMeeting, i18n.Err("assistant-meeting.not_meeting"))
		return
	}
	if _, _, ok := meetingRecording(w, e, id); !ok {
		return
	}
	if !s.meetingWritable(r.Context(), e) {
		apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
		return
	}
	var req recordingwire.SpeakerTurnsImportRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_speakers"))
		return
	}
	if _, err := writersOf(e).ImportSpeakerTurns(id, req, time.Now()); err != nil {
		writeSpeakerError(w, err)
		return
	}
	speakers, err := e.SpeakerDetail(id)
	if err != nil {
		writeSpeakerError(w, err)
		return
	}
	apierr.WriteJSON(w, recordingwire.SpeakerTurnsImportResponse{Speakers: speakers})
}

func writeSpeakerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recordingwire.ErrSpeakersStale):
		apierr.WriteCode(w, http.StatusConflict, codeSpeakersStale, i18n.Err("assistant-meeting.speakers_stale"))
	case errors.Is(err, recordingwire.ErrSpeakersCleared):
		apierr.WriteCode(w, http.StatusConflict, codeSpeakersCleared, i18n.Err("assistant-meeting.speakers_cleared"))
	case errors.Is(err, recordingwire.ErrBadSpeakers):
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_speakers"))
	case errors.Is(err, recordingwire.ErrSpeakersTooLarge):
		apierr.Write(w, http.StatusRequestEntityTooLarge, i18n.Err("assistant-meeting.too_many_speaker_turns", i18n.Args{"max": recordingwire.MaxSpeakerTurns}))
	case errors.Is(err, recordingwire.ErrOpIDReused):
		apierr.WriteCode(w, http.StatusConflict, codeOpIDReused, i18n.Err("assistant-meeting.speaker_run_reused"))
	default:
		writeMeetingError(w, err)
	}
}
