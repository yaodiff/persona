package recording

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/modules/recording/writers"
	"github.com/nwverse/persona/server/internal/recordingwire"

	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the core asks of the module (internal/recordinghost.Layer).

// Routes registers the module's HTTP routes; engine is the knowledge base for a route.
func (s *Recordings) Routes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	s.recordingRoutes(engine)
}

// AudioSocket serves GET /api/v1/recordings/{id}/audio.
func (s *Recordings) AudioSocket(w http.ResponseWriter, r *http.Request) { s.audioSocket(w, r) }

// Hook gives knowledge base e the recordings' write side (recordingwire.Provider: the minutes, title, recap and summary
// rounds, the sweep after a prune, the audio writers), which a knowledge base has only with this module, and the
// module's callbacks: the minutes' push, the live log's and meeting.json's changes, the day recordings' names and the
// day's push. The writers are this module's (internal/modules/recording/writers: the upload and a day's appends, the
// live log, meeting.json, the minutes and day recaps, the titles, the photo intake, the audio, the hub's transcript)
// and reach the store through core, the storage port; the summary round alone is kb's own code (kb.RecordingWriters).
func (s *Recordings) Hook(e *kb.Engine, core recordingwire.Core) {
	w := writers.New(core, kb.RecordingWriters(e))
	w.OnWritten(s.minutesWritten, func(ctx context.Context, r recordingwire.Recording, d recordingwire.DayRecap) ([]string, error) {
		return s.dayRecapWritten(ctx, e, r, d)
	})
	w.OnLive(s.meetingKick)
	w.OnSpeakersChanged(func(string) { s.kbWake() })
	e.SetRecordingProvider(w)
	e.MeetingChanged, e.DayRecordingName = s.meetingLiveChanged, s.dayRecordingName
}

// writersOf is the writers of knowledge base e: the ones Hook gave it. One that was never
// hooked (the core opens a read-only knowledge base without the module's hook) gets writers over its port for the
// request: they read, and answer a write as a read-only knowledge base does.
func writersOf(e *kb.Engine) *writers.Writers {
	if w := writers.Of(e.RecordingProvider()); w != nil {
		return w
	}
	return writers.New(kb.RecordingCore(e), nil)
}

func (s *Recordings) AttendResume(e *kb.Engine)                    { s.attendResume(e) }
func (s *Recordings) FinalPasses(ctx context.Context)              { s.finalPasses(ctx) }
func (s *Recordings) RoomsSweep(ctx context.Context, e *kb.Engine) { s.meetingRoomsSweep(ctx, e) }
func (s *Recordings) HubSweep(e *kb.Engine)                        { s.hubSweep(e) }

// TranscriptionChanged: the meeting transcription the owner chose, or whether it is ready, changed: the live jobs of
// a choice no longer standing are ended (transcriptionJobsCheck), never under the caller's locks.
func (s *Recordings) TranscriptionChanged() {
	s.hubTr.wg.Add(1)
	go func() {
		defer s.hubTr.wg.Done()
		s.transcriptionJobsCheck(s.runCtx())
	}()
}

func (s *Recordings) TranscriptionJobsCheck(ctx context.Context) { s.transcriptionJobsCheck(ctx) }

// MeetingTurnSent marks that a meeting's batch went to machine's session (recording's; persona: an assistant
// conversation): its turn, once started, is the meeting's (meeting_turns.go).
func (s *Recordings) MeetingTurnSent(machine, session, recording string, persona bool) (time.Time, error) {
	return s.meetingTurnSent(machine, session, recording, persona)
}

func (s *Recordings) JustMeNow(machine, session string) bool { return s.justMeNow(machine, session) }
func (s *Recordings) JustMeSent(machine, session string)     { s.justMeSent(machine, session) }
func (s *Recordings) JustMeStarted(machine, session string, newTurn, woken bool) {
	s.justMeStarted(machine, session, newTurn, woken)
}
func (s *Recordings) JustMeEnded(machine, session string, held bool) {
	s.justMeEnded(machine, session, held)
}
func (s *Recordings) MeetingTurnNow(machine, session string) bool {
	return s.meetingTurnNow(machine, session)
}
func (s *Recordings) MeetingTurnStarted(machine, session string, newTurn bool) bool {
	return s.meetingTurnStarted(machine, session, newTurn)
}
func (s *Recordings) MeetingTurnEnded(machine, session string) bool {
	return s.meetingTurnEnded(machine, session)
}
func (s *Recordings) MeetingTurnStopped(machine, session string, row proto.Session, restarted bool) bool {
	return s.meetingTurnStopped(machine, session, row, restarted)
}
func (s *Recordings) MeetingTurnPrompt(machine, session string, p map[string]any) {
	s.meetingTurnPrompt(machine, session, p)
}
func (s *Recordings) MeetingTurnRunning(ctx context.Context, machine, session string) (bool, error) {
	return s.meetingTurnRunning(ctx, machine, session)
}
func (s *Recordings) MeetingTurnAgentRunning(ctx context.Context, machine string) (bool, error) {
	return s.meetingTurnAgentRunning(ctx, machine)
}
func (s *Recordings) MeetingTurnUserSpoke(machine, session string) {
	s.meetingTurnUserSpoke(machine, session)
}
func (s *Recordings) MeetingRefuses(ctx context.Context, machine, session, name string, assistant bool) (proto.ToolResult, bool) {
	return s.meetingRefuses(ctx, machine, session, name, assistant)
}
func (s *Recordings) RoomRefusesSend(ctx context.Context, machine, session, lang string) error {
	return s.roomRefusesSend(ctx, machine, session, lang)
}
func (s *Recordings) RoomToolCall(ctx context.Context, recording, name string, args json.RawMessage) proto.ToolResult {
	return s.roomToolCall(ctx, recording, name, args)
}
func (s *Recordings) RoomTools() []proto.Tool { return meetingRoomTools() }
func (s *Recordings) RoomToolNames() []string { return meetingRoomToolNames() }

// Wait ends the live hub transcription runs and waits until the module's background work has ended (hub
// transcription's goroutines, the questions being answered), or ctx ends: the service stops.
func (s *Recordings) Wait(ctx context.Context) error {
	s.hubTr.mu.Lock()
	runs := make([]*hubRun, 0, len(s.hubTr.by))
	for _, r := range s.hubTr.by {
		runs = append(runs, r)
	}
	s.hubTr.mu.Unlock()
	for _, r := range runs { // the service stops: the live runs end with it (their sockets, clocks and jobs)
		r.stop("")
	}
	done := make(chan struct{})
	go func() {
		s.hubTr.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		s.asks.mu.Lock()
		n := len(s.asks.running)
		s.asks.mu.Unlock()
		if n == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
