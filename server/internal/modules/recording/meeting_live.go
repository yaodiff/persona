package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/modules/recording/writers"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// Attending a recorded meeting (docs/assistant/meeting-minutes.md "Attending a meeting", writers/live.go): the phone's
// live sentences (POST / GET …/live) and its questions (POST …/ask, meeting_ask.go). The live log is the knowledge
// base's; feeding it to the attending session is the feeder's (meetingKick). Full credentials only: the watch's
// answer-only token and extension tokens never reach these routes at the hub (they are not in extwire.RouteScopes),
// and any other role is refused here.

// Codes of the attending routes' refusals (the contract's).
const (
	codeRecordingNotFound = "recording_not_found" // no upload of this recording yet: the phone uploads it, then sends again
	codeNotAttending      = "not_attending"
	codeLiveGap           = "live_gap"
	codeLiveFull          = "live_full"
	codeOpIDReused        = "op_id_reused"
	codeAsksFull          = "asks_full"
	codeAskBusy           = "ask_busy"
	codeMeetingEnded      = "meeting_ended" // the room ended: ask in the follow-up
	codeNotMeeting        = "not_meeting"
	codeMeetingRunning    = "meeting_running"
	codeNoMaterial        = "no_material"
)

// The bodies the attending routes read at most.
const (
	maxLiveBody = 1 << 20
	maxAskBody  = 64 << 10
)

func (s *Recordings) meetingRoutes() {
	s.handle("POST /api/v1/recordings/{id}/live", func(w http.ResponseWriter, r *http.Request) {
		if !meetingCaller(w, r) {
			return
		}
		e := s.meetingEngine(w, r)
		if e == nil {
			return
		}
		raw, ok := meetingBody(w, r, maxLiveBody, "1 MiB")
		if !ok {
			return
		}
		id := r.PathValue("id")
		_, _, ok = meetingRecording(w, e, id) // 410 and 404 before the body is read as live lines
		if !ok {
			return
		}
		if !s.meetingWritable(r.Context(), e) {
			apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
			return
		}
		from, segs, err := parseLive(raw)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		l, rec, err := writersOf(e).LiveAppend(id, from, segs)
		var gap *recordingwire.LiveGapError
		switch {
		case errors.As(err, &gap):
			body := apierr.LocalizedBody(http.StatusConflict, codeLiveGap, i18n.Err("assistant-meeting.live_gap", i18n.Args{"next": gap.Next}), i18n.OfWriter(w))
			body["next"] = gap.Next
			apierr.WriteBody(w, http.StatusConflict, body)
			return
		case errors.Is(err, recordingwire.ErrLiveFull):
			apierr.WriteCode(w, http.StatusRequestEntityTooLarge, codeLiveFull, i18n.Err("assistant-meeting.live_full"))
			return
		case err != nil:
			writeMeetingError(w, err)
			return
		}
		apierr.WriteJSON(w, s.liveView(r.Context(), l, rec, nil))
	})
	s.handle("GET /api/v1/recordings/{id}/live", func(w http.ResponseWriter, r *http.Request) {
		if !meetingCaller(w, r) {
			return
		}
		e := s.meetingEngine(w, r) // a read-only knowledge base answers it too
		if e == nil {
			return
		}
		from, hasFrom, fromOK := liveFrom(r)
		l, rec, err := s.asksRead(e, r.PathValue("id"))
		if err != nil {
			writeMeetingError(w, err)
			return
		}
		if !fromOK { // after 410 and 404, as POST reads its body
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_from"))
			return
		}
		asks := make([]askView, 0, len(l.Asks))
		for _, a := range l.Asks {
			asks = append(asks, viewAsk(a))
		}
		view := s.liveView(r.Context(), l, rec, asks)
		if rev := writersOf(e).PhotosRev(rec.ID); rev > 0 { // photos has a revision and no field: GET …/photos reads them
			view.Revs[recordingwire.PartPhotos] = rev
		}
		if rec.HubTranscribed() { // the latest caption (meeting_transcribe.go), its revision its time in milliseconds
			view.Interim = s.hubInterim(rec.ID)
			if rev := s.hubInterimRev(rec.ID); rev > 0 {
				view.Revs[recordingwire.PartInterim] = rev
			}
		}
		if hasFrom {
			lines, more := liveLines(l, from)
			view.Lines, view.More = &lines, more
		}
		if parts := r.URL.Query().Get("parts"); parts != "" {
			apierr.WriteJSON(w, view.only(strings.Split(parts, ",")))
			return
		}
		apierr.WriteJSON(w, view)
	})
	s.askRoute()
	s.meetingConversationRoutes()
	s.meetingPhotoRoutes()
	s.meetingSpeakerRoutes()
}

// meetingCaller: the request carries full credentials; anything else has been answered 403 forbidden.
func meetingCaller(w http.ResponseWriter, r *http.Request) bool {
	if fullRole(r) {
		return true
	}
	apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-meeting.forbidden"))
	return false
}

// meetingEngine is the knowledge base for an attending route: 503 kb_disabled or kb_unavailable when it is not there,
// as the other knowledge-base routes answer. Unlike theirs it lets a read-only one through: the contract refuses
// changes there only after the body and the recording are checked (meetingWritable).
func (s *Recordings) meetingEngine(w http.ResponseWriter, r *http.Request) *kb.Engine {
	e, err := s.kbGet(r.Context())
	switch {
	case s.h.KBOff(err):
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBDisabled, errKBRouteOff)
	case err != nil:
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBUnavailable, errKBRouteNotOpen)
	}
	return e
}

// meetingWritable: the knowledge base takes changes now (not read-only, in the settings or as opened).
func (s *Recordings) meetingWritable(ctx context.Context, e *kb.Engine) bool {
	return !s.kbReadOnly(ctx) && !e.Store.ReadOnly()
}

// meetingBody is the request's body, at most most bytes; over it has been answered 413 too_large (said as size).
func meetingBody(w http.ResponseWriter, r *http.Request, most int64, size string) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, most+1))
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return nil, false
	}
	if int64(len(raw)) > most {
		apierr.Write(w, http.StatusRequestEntityTooLarge, i18n.Err("assistant-meeting.too_large", i18n.Args{"size": size}))
		return nil, false
	}
	return raw, true
}

// meetingRecording is recording id with its live log; else 410 recording_deleted or 404 recording_not_found has been
// answered.
func meetingRecording(w http.ResponseWriter, e *kb.Engine, id string) (recordingwire.Live, recordingwire.Recording, bool) {
	l, rec, err := writersOf(e).LiveRead(id)
	if err != nil {
		writeMeetingError(w, err)
		return l, rec, false
	}
	return l, rec, true
}

// writeMeetingError answers the knowledge base's refusals of a meeting route as the contract codes them (the photos'
// too, meeting_photos.go).
func writeMeetingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, recordingwire.ErrRecordingDeleted):
		apierr.WriteCode(w, http.StatusGone, codeRecordingDeleted, i18n.Err("assistant-meeting.deleted"))
	case errors.Is(err, kbwire.ErrNotFound):
		apierr.WriteCode(w, http.StatusNotFound, codeRecordingNotFound, i18n.Err("assistant-meeting.no_recording"))
	case errors.Is(err, kbwire.ErrReadOnly):
		apierr.WriteCode(w, http.StatusForbidden, assistantwire.CodeKBReadOnly, i18n.Err("assistant-kb_routes.read_only"))
	case errors.Is(err, recordingwire.ErrNotAttending):
		apierr.WriteCode(w, http.StatusConflict, codeNotAttending, i18n.Err("assistant-meeting.not_attending"))
	case errors.Is(err, recordingwire.ErrHubTranscribes):
		apierr.WriteCode(w, http.StatusConflict, codeHubTranscribes, i18n.Err("assistant-meeting.hub_transcribes"))
	case errors.Is(err, recordingwire.ErrNotHub):
		apierr.WriteCode(w, http.StatusConflict, codeNotHub, i18n.Err("assistant-meeting.not_hub"))
	case errors.Is(err, recordingwire.ErrTranscriptionFinal):
		apierr.WriteCode(w, http.StatusConflict, codeTranscriptionFinal, i18n.Err("assistant-meeting.transcription_final"))
	case errors.Is(err, recordingwire.ErrAudioFull):
		apierr.WriteCode(w, http.StatusRequestEntityTooLarge, codeAudioFull, i18n.Err("assistant-meeting.audio_full"))
	case errors.Is(err, recordingwire.ErrBadAudio):
		apierr.WriteCode(w, http.StatusBadRequest, codeBadAudio, i18n.Err("assistant-meeting.bad_audio"))
	case errors.Is(err, recordingwire.ErrBadLive), errors.Is(err, recordingwire.ErrBadPhoto):
		apierr.Write(w, http.StatusBadRequest, err)
	case errors.Is(err, recordingwire.ErrNotMeeting):
		apierr.WriteCode(w, http.StatusConflict, codeNotMeeting, i18n.Err("assistant-meeting.not_meeting"))
	case errors.Is(err, recordingwire.ErrTranscriptExpired):
		apierr.WriteCode(w, http.StatusConflict, codeTranscriptExpired, i18n.Err("assistant-meeting.transcript_expired"))
	case errors.Is(err, recordingwire.ErrPhotoNotFound):
		apierr.WriteCode(w, http.StatusNotFound, codePhotoNotFound, i18n.Err("assistant-meeting.photo_not_found"))
	case errors.Is(err, recordingwire.ErrPhotoDeleted):
		apierr.WriteCode(w, http.StatusGone, codePhotoDeleted, i18n.Err("assistant-meeting.photo_deleted"))
	case errors.Is(err, recordingwire.ErrOpIDReused):
		apierr.WriteCode(w, http.StatusConflict, codeOpIDReused, i18n.Err("assistant-meeting.photo_id_reused"))
	case errors.Is(err, recordingwire.ErrPhotosFull):
		apierr.WriteCode(w, http.StatusRequestEntityTooLarge, codePhotosFull, i18n.Err("assistant-meeting.photos_full", i18n.Args{"max": recordingwire.MaxPhotos}))
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// parseLive reads a POST …/live body: from (required, a whole number of 0 or more) and the sentences (at most
// writers.MaxLiveRequest; each time a number). Anything else wrong with a sentence is mended by the knowledge base, never
// refused: an absent time is 0, text that is not a string an empty placeholder.
func parseLive(raw []byte) (int, []recordingwire.LiveSegment, error) {
	var in struct {
		From     json.RawMessage   `json:"from"`
		Segments []json.RawMessage `json:"segments"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, nil, i18n.Err("assistant-meeting.bad_json", i18n.Args{"error": err.Error()})
	}
	from, ok := wholeNumber(in.From)
	if !ok || from < 0 {
		return 0, nil, i18n.Err("assistant-meeting.bad_from")
	}
	if len(in.Segments) > writers.MaxLiveRequest {
		return 0, nil, i18n.Err("assistant-meeting.too_many_segments", i18n.Args{"max": writers.MaxLiveRequest})
	}
	segs := make([]recordingwire.LiveSegment, len(in.Segments))
	for i, rawSeg := range in.Segments {
		var sg struct {
			Start, End, Text json.RawMessage
		}
		if err := json.Unmarshal(rawSeg, &sg); err != nil {
			return 0, nil, i18n.Err("assistant-meeting.bad_json", i18n.Args{"error": err.Error()})
		}
		for _, t := range []struct {
			raw json.RawMessage
			to  *float64
		}{{sg.Start, &segs[i].Start}, {sg.End, &segs[i].End}} {
			if t.raw != nil && json.Unmarshal(t.raw, t.to) != nil {
				return 0, nil, i18n.Err("assistant-meeting.bad_time")
			}
		}
		_ = json.Unmarshal(sg.Text, &segs[i].Text)
	}
	return int(from), segs, nil
}

// wholeNumber is raw as a whole JSON number (12 or 12.0); false for anything else, null and absent included.
func wholeNumber(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] == '"' {
		return 0, false
	}
	var n json.Number
	if json.Unmarshal(raw, &n) != nil || n == "" {
		return 0, false
	}
	if v, err := strconv.ParseInt(string(n), 10, 64); err == nil {
		return v, true
	}
	f, err := n.Float64()
	if err != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return 0, false
	}
	return int64(f), true
}

// maxLiveLines is how many live lines one GET …/live?from= gives at most (more says there are more).
const maxLiveLines = 500

// liveFrom is GET …/live's from (phase 2's live lines from that position): has says it was given, ok that it is a
// whole number of 0 or more (400 otherwise).
func liveFrom(r *http.Request) (from int, has, ok bool) {
	vals, has := r.URL.Query()["from"]
	if !has {
		return 0, false, true
	}
	v := vals[0]
	if v == "" || strings.Trim(v, "0123456789") != "" {
		return 0, true, false
	}
	n, err := strconv.Atoi(v)
	return n, true, err == nil
}

// liveLine is one position of the live log as GET …/live gives it ("" text included: a placeholder position). In hub
// mode Source says whose text it is: the hub's (hub) or the phone's filling in (device); absent in device mode.
type liveLine struct {
	N      int     `json:"n"`
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Text   string  `json:"text"`
	Source string  `json:"source,omitempty"`
}

// liveLines are l's positions from from, at most maxLiveLines, and whether more follow them.
func liveLines(l recordingwire.Live, from int) ([]liveLine, bool) {
	out := []liveLine{}
	for n := from; n < len(l.Segments) && len(out) < maxLiveLines; n++ {
		sg := l.Segments[n]
		out = append(out, liveLine{N: n, Start: sg.Start, End: sg.End, Text: sg.Text, Source: sg.Source})
	}
	return out, from+len(out) < len(l.Segments)
}

// meetingLiveView is what …/live answers: the live log's count, the role in effect and the session being fed. With
// attending off it is only the role, the counts and the questions.
type meetingLiveView struct {
	Next             int           `json:"next"`
	Attendance       string        `json:"attendance"`
	AttendingSession *attendeeView `json:"attending_session,omitempty"`
	Fed              int           `json:"fed"`
	Problem          string        `json:"problem,omitempty"`
	Asks             *[]askView    `json:"asks,omitempty"` // GET only
	// The meeting conversation (#3862): the private mark (absent until chosen), a room meeting's room, the follow-up
	// once made, and each part's revision (GET only).
	Private  *bool                   `json:"private,omitempty"`
	Room     *recordingwire.RoomView `json:"room,omitempty"`
	Followup *recordingwire.ConvView `json:"followup,omitempty"`
	Revs     map[string]int64        `json:"revs,omitempty"`
	// The live view (#3862 phase 2), GET only: the live lines from ?from= (none without it) with more when there are
	// more, and the room's topics ([] before the first) with their last change. Never in detail.meta.
	Lines    *[]liveLine                `json:"lines,omitempty"`
	More     bool                       `json:"more,omitempty"`
	Topics   *[]recordingwire.LiveTopic `json:"topics,omitempty"`
	TopicsAt *time.Time                 `json:"topics_at,omitempty"`
	// The highlights (#3862 phase 2b), GET only: the room's highlights so far ([] before the first) with their last
	// change. Never in detail.meta.
	Highlights   *[]recordingwire.LiveHighlight `json:"highlights,omitempty"`
	HighlightsAt *time.Time                     `json:"highlights_at,omitempty"`
	// Hub transcription (#3862 phase 5): the recording's transcription once its mode was decided (also in
	// detail.meta), and the latest caption while the hub transcribes it (GET only, never in detail.meta).
	Transcription *recordingwire.TranscriptionView `json:"transcription,omitempty"`
	Interim       *liveInterim                     `json:"interim,omitempty"`
}

// only is v cut to parts (GET …/live?parts=asks,room): those of them it has, with their revisions.
func (v meetingLiveView) only(parts []string) map[string]any {
	out := map[string]any{}
	revs := map[string]int64{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		switch p {
		case recordingwire.PartAsks:
			if v.Asks != nil {
				out[p] = *v.Asks
			}
		case recordingwire.PartRoom:
			if v.Room != nil {
				out[p] = v.Room
			}
		case recordingwire.PartPrivate:
			if v.Private != nil {
				out[p] = *v.Private
			}
		case recordingwire.PartFollowup:
			if v.Followup != nil {
				out[p] = v.Followup
			}
		case recordingwire.PartPhotos:
			// Only its revision: the photos are read with GET …/photos.
		case recordingwire.PartLines:
			if v.Lines != nil { // only with ?from=
				out[p] = *v.Lines
				if v.More {
					out["more"] = true
				}
			}
		case recordingwire.PartTopics:
			if v.Topics != nil {
				out[p] = *v.Topics
			}
			if v.TopicsAt != nil {
				out["topics_at"] = v.TopicsAt
			}
		case recordingwire.PartHighlights:
			if v.Highlights != nil {
				out[p] = *v.Highlights
			}
			if v.HighlightsAt != nil {
				out["highlights_at"] = v.HighlightsAt
			}
		case recordingwire.PartTranscription:
			if v.Transcription != nil {
				out[p] = v.Transcription
			}
		case recordingwire.PartInterim:
			if v.Interim != nil {
				out[p] = v.Interim
			}
		case recordingwire.PartSpeakers:
			// Only its revision: speakers are read from GET /api/v1/kb/recordings/{id}.
		default:
			continue
		}
		if rev, ok := v.Revs[p]; ok {
			revs[p] = rev
		}
	}
	out["revs"] = revs
	return out
}

// attendeeView is the session an attended meeting is fed to; Main says it is the main chat.
type attendeeView struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Main      bool   `json:"main,omitempty"`
}

// liveView is recording rec's live state l as …/live answers it; asks (nil: left out) are its questions.
func (s *Recordings) liveView(ctx context.Context, l recordingwire.Live, rec recordingwire.Recording, asks []askView) meetingLiveView {
	out := meetingLiveView{Next: len(l.Segments), Attendance: rec.AttendanceRole(), Fed: min(l.Fed, len(l.Segments))}
	if asks != nil {
		out.Asks = &asks
	}
	out.Revs = map[string]int64{recordingwire.PartAsks: l.AsksRev, recordingwire.PartLines: int64(len(l.Segments)), recordingwire.PartTopics: l.TopicsRev,
		recordingwire.PartHighlights: l.HighlightsRev}
	if m := rec.Conv; m != nil {
		out.Private, out.Room, out.Followup, out.Transcription = m.Private, m.RoomView(), m.FollowupView(), m.TranscriptionView(nil)
		for _, p := range []string{recordingwire.PartRoom, recordingwire.PartPrivate, recordingwire.PartFollowup, recordingwire.PartTranscription, recordingwire.PartSpeakers} {
			if rev, ok := m.Revs[p]; ok {
				out.Revs[p] = rev
			}
		}
	}
	if asks == nil {
		out.Revs = nil // POST …/live: GET reads them, the topics and the highlights
	} else {
		topics := l.Topics
		if topics == nil {
			topics = []recordingwire.LiveTopic{}
		}
		out.Topics = &topics
		if !l.TopicsAt.IsZero() {
			at := l.TopicsAt
			out.TopicsAt = &at
		}
		highlights := l.Highlights
		if highlights == nil {
			highlights = []recordingwire.LiveHighlight{}
		}
		out.Highlights = &highlights
		if !l.HighlightsAt.IsZero() {
			at := l.HighlightsAt
			out.HighlightsAt = &at
		}
	}
	if rec.RoomMeeting() {
		if l.Full { // the room's own problems are its room's
			out.Problem = codeLiveFull
		}
		return out
	}
	if !rec.Attending() {
		return out
	}
	if ref, main, ok := s.meetingAttendee(ctx, rec); ok {
		out.AttendingSession = &attendeeView{MachineID: ref.MachineID, SessionID: ref.SessionID, Main: main}
	}
	switch {
	case l.Full:
		out.Problem = codeLiveFull
	default:
		out.Problem = l.Problem
	}
	return out
}

// meetingAttendee is the session recording rec's meeting goes to now: the one its upload names, else the main chat
// as it is now (false when there is none); main says it is the main chat.
func (s *Recordings) meetingAttendee(ctx context.Context, rec recordingwire.Recording) (ref recordingwire.SessionRef, main, ok bool) {
	if a := rec.AttendingSession; a != nil {
		return *a, s.isMainOn(ctx, a.MachineID, a.SessionID), true
	}
	set := s.settings(ctx)
	if set.Main == "" || set.MachineID == "" {
		return recordingwire.SessionRef{}, false, false
	}
	return recordingwire.SessionRef{MachineID: set.MachineID, SessionID: set.Main}, true, true
}
