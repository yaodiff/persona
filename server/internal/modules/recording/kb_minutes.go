package recording

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/modules/recording/writers"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// Meeting minutes of the phone's recordings (modules/recording/writers/minutes.go, docs/assistant/meeting-minutes.md): asking for them, the
// push when they are written, and the to-dos the user confirms from their action items.

// codeMinutesStale: the minutes were written again since the version the app sent; it reads them again.
const codeMinutesStale = "minutes_stale"

// codeTranscriptExpired: the recording's transcript went with recording_days and only its minutes are kept, so they
// can't be made again (kb/recording_trim.go).
const codeTranscriptExpired = "transcript_expired"

// minutesWritten pushes the minutes the knowledge-base loop wrote for a recording's current revision: one push per
// recording and version (a stable id, so a restart does not repeat it), carrying the recording (an app that knows it
// opens the meeting's thread; one that does not, the main conversation, as reminders do), and the same text with the
// recording's web page to the Lark bot and the channels with pushes on.
func (s *Recordings) minutesWritten(ctx context.Context, r recordingwire.Recording, m recordingwire.Minutes) {
	l := s.lang(ctx)
	title := i18n.T(l, "assistant-minutes.ready_title")
	body := i18n.T(l, "assistant-minutes.ready_body", i18n.Args{"name": m.Title, "decisions": len(m.Decisions), "actions": len(m.Actions)})
	set := s.settings(ctx)
	n := Notification{ID: "ntf_minutes_" + r.ID + "_" + strconv.Itoa(m.Version), Kind: NotifyPush, Title: title, Body: body,
		DeviceID: set.MachineID, SessionID: set.Main, RecordingID: r.ID}
	if err := s.h.Notify(ctx, n); err != nil {
		s.logf("assistant: minutes push: %v", err)
	} else {
		s.noteCard(ctx, cardMinutesReady, n)
	}
	if link := recordingLink(s.webBase(ctx), r.ID); link != "" {
		body += "\n" + link
	}
	// Not as the main conversation's: a channel holds back its mirror of a conversation it was just pushed for.
	s.channelsPushed("", "", title, body)
}

// recordingLink is a recording's page in the web app at web (web/src/lib/router.ts), "" when web is.
func recordingLink(web, id string) string {
	return deeplink.Web(web, deeplink.Recording(id))
}

func (s *Recordings) minutesRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	// Make (or make again) a recording's minutes at the knowledge base's next round, which this wakes; a day
	// recording's recap (kb_day.go), from what the hub holds now, which never makes it a meeting and never pushes.
	s.handle("POST /api/v1/recordings/{id}/minutes", func(w http.ResponseWriter, r *http.Request) {
		e := engine(w, r)
		if e == nil {
			return
		}
		_, day := dayRecording(e, r.PathValue("id"))
		switch err := writersOf(e).AskMinutes(r.PathValue("id")); {
		case errors.Is(err, kbwire.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.no_recording"))
			return
		case errors.Is(err, recordingwire.ErrTranscriptExpired) && day:
			apierr.WriteCode(w, http.StatusConflict, codeTranscriptExpired, i18n.Err("assistant-day-recap.transcript_expired"))
			return
		case errors.Is(err, recordingwire.ErrTranscriptExpired):
			apierr.WriteCode(w, http.StatusConflict, codeTranscriptExpired, i18n.Err("assistant-minutes.transcript_expired"))
			return
		case errors.Is(err, writers.ErrRecordingIncomplete) && day: // nothing of the day transcribed yet
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-day-recap.no_text"))
			return
		case errors.Is(err, writers.ErrRecordingIncomplete):
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-minutes.incomplete"))
			return
		case err != nil:
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		s.kbWake()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"state": recordingwire.MinutesPending})
	})
	// The action items the user confirmed become to-dos.
	s.handle("POST /api/v1/recordings/{id}/minutes/todos", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-minutes.todo_forbidden"))
			return
		}
		e := engine(w, r)
		if e == nil {
			return
		}
		var req struct {
			Version int      `json:"version"`
			Actions []string `json:"actions"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if len(req.Actions) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-minutes.no_actions"))
			return
		}
		if rec, ok := dayRecording(e, r.PathValue("id")); ok { // a day recording's recap: its suggested to-dos
			todos, rc, err := s.dayRecapTodos(r.Context(), e, rec, req.Version, req.Actions)
			var unknown unknownActionError
			switch {
			case errors.Is(err, writers.ErrMinutesStale):
				apierr.WriteCode(w, http.StatusConflict, codeMinutesStale, i18n.Err("assistant-day-recap.stale"))
				return
			case errors.As(err, &unknown):
				apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-day-recap.unknown_action", i18n.Args{"id": string(unknown)}))
				return
			case errors.Is(err, kbwire.ErrNotFound):
				apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-day-recap.no_recap"))
				return
			case err != nil:
				apierr.Write(w, http.StatusServiceUnavailable, err)
				return
			}
			apierr.WriteJSON(w, map[string]any{"todos": todos, "recap": rc})
			return
		}
		todos, m, err := s.minutesTodos(r.Context(), e, r.PathValue("id"), req.Version, req.Actions)
		var unknown unknownActionError
		switch {
		case errors.Is(err, writers.ErrMinutesStale):
			apierr.WriteCode(w, http.StatusConflict, codeMinutesStale, i18n.Err("assistant-minutes.stale"))
			return
		case errors.As(err, &unknown):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.unknown_action", i18n.Args{"id": string(unknown)}))
			return
		case errors.Is(err, kbwire.ErrNotFound):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-minutes.no_minutes"))
			return
		case err != nil:
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"todos": todos, "minutes": m})
	})
}

// unknownActionError names an action id the minutes don't have.
type unknownActionError string

func (u unknownActionError) Error() string { return "no action item " + string(u) }

// minutesTodos makes a to-do of each action of version of recording id's minutes (one that has one already keeps it)
// and records them in the minutes. A to-do's id comes from the recording, the version and the action, so a request
// retried after its answer was lost makes none twice.
func (s *Recordings) minutesTodos(ctx context.Context, e *kb.Engine, id string, version int, actions []string) ([]any, *recordingwire.Minutes, error) {
	s.minutesMu.Lock()
	defer s.minutesMu.Unlock()
	rid, _ := recordingwire.RecordingID(id)
	m, err := e.Store.ReadMinutes(rid)
	if err != nil {
		return nil, nil, err
	}
	if m.Version != version {
		return nil, nil, writers.ErrMinutesStale
	}
	rec, ok := e.Store.Recording(rid)
	if !ok {
		return nil, nil, kbwire.ErrNotFound
	}
	note := s.minutesTodoNotes(ctx, rec, m)
	out, made, byTask, err := s.actionTodos(ctx, m.Actions, actions, todosOfMinutes, rid, version, note)
	if err != nil {
		if made != nil {
			// Those made so far are recorded all the same: a retry after the user deleted one must not make it again.
			if _, serr := writersOf(e).SetMinutesTodos(rid, version, made, byTask); serr != nil {
				s.logf("assistant: minutes to-dos of %s: %v", rid, serr)
			}
		}
		return out, nil, err
	}
	// Minutes written again meanwhile get the to-dos by task, so confirming the same task there makes none twice.
	m, err = writersOf(e).SetMinutesTodos(rid, version, made, byTask)
	return out, m, err
}

// minutesTodoNotes is what a to-do made of an action of minutes m of recording rec says: which meeting, when it was
// said, the deadline as said, the recording's page. The zone, language and web address are read once a request.
func (s *Recordings) minutesTodoNotes(ctx context.Context, rec *recordingwire.Recording, m *recordingwire.Minutes) func(recordingwire.MinutesAction) string {
	loc, l, link := s.location(ctx), s.lang(ctx), recordingLink(s.webBase(ctx), rec.ID)
	return func(a recordingwire.MinutesAction) string {
		clock := rec.StartedAt.Add(time.Duration(a.At * float64(time.Second))).In(loc).Format("15:04:05")
		notes := i18n.T(l, "assistant-minutes.todo_notes", i18n.Args{"meeting": m.Title, "when": m.When, "clock": clock})
		if a.By != "" {
			notes += "\n" + i18n.T(l, "assistant-minutes.todo_by", i18n.Args{"by": a.By})
		}
		if link != "" {
			notes += "\n" + link
		}
		return notes
	}
}
