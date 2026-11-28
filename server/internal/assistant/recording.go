package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/recordingwire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/recordinghost"
)

// The meeting recording module (internal/modules/recording, docs/architecture/core-and-modules.md "Recording"): the
// recordings' routes, meetings attended and transcribed on the hub, their asks, photos, rooms and follow-ups, minutes
// and day recaps. The core reaches it only through internal/recordinghost; s.rec is nil when the module is left out of
// the build (persona_without_recording, persona_core_only) or switched off (the modules row), and then the core
// serves no recording route, announces no meeting capability, offers no meeting tool and runs none of its jobs. The
// methods below keep the names the core called before, with what the core does without the module.

// recordingLayer is the module as the core reaches it.
type recordingLayer = recordinghost.Layer

// loadRecording makes the module's layer when the build has the module and the modules row does not switch it off
// (the port asks the switch, moduleswitch, as every provider seam does), before the kernel loads, since the module's
// routes are registered with the knowledge base's (kbRoutes).
func (s *Service) loadRecording() {
	if rec, ok := recordinghost.New(s.moduleSwitch, recordingHost{s}); ok {
		s.rec = rec
	}
}

// recordingOptions are Options' knobs of the recording module (Options.RecordingTimings).
type recordingOptions struct {
	mu      sync.Mutex // tests change them while the module runs
	timings map[string]time.Duration
	lists   map[string][]time.Duration
	now     func() time.Time
	after   func(time.Duration, func()) func() bool
}

// recordingWait waits, at most d, for the module's background work to end (the service stops).
func (s *Service) recordingWait(d time.Duration) {
	if s.rec == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := s.rec.Wait(ctx); err != nil {
		s.logf("recording: background work still running at the end: %v", err)
	}
}

// recordingCapabilities are the capabilities of the manager's hello that the recording module serves: the hub
// announces the meeting features only while the default manager's hello names them (hub/extensions_link.go).
var recordingCapabilities = []string{assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation,
	assistantwire.CapabilityMeetingPhotos, assistantwire.CapabilityMeetingLiveView, assistantwire.CapabilityMeetingHighlights,
	assistantwire.CapabilityMeetingNotes, assistantwire.CapabilityMeetingEcho, assistantwire.CapabilityMeetingTranscription,
	assistantwire.CapabilityMeetingSpeakers, assistantwire.CapabilityRecordingDay}

// withRecording is caps without the recording module's capabilities when svc has no recording layer (none yet: when
// the build has no recording module), in their order.
func withRecording(caps []string, svc *Service) []string {
	on := recordinghost.InBuild(moduleswitch.Set{}) // no service yet: as the build has it
	if svc != nil {
		on = svc.recordingOn()
	}
	if on {
		return caps
	}
	return slices.DeleteFunc(caps, func(c string) bool { return slices.Contains(recordingCapabilities, c) })
}

// recordingOn says whether the module is loaded.
func (s *Service) recordingOn() bool { return s.rec != nil }

// Model purposes of meetings (models.go lists them).
const (
	purposeMeetingAnswer  = kb.PurposeMeetingAnswer
	purposeMeetingMinutes = kb.PurposeMeetingMinutes
	purposeMeetingTitle   = recordingwire.PurposeMeetingTitle
	purposeMeetingRoom    = kb.PurposeMeetingRoom
)

// Origins of a meeting's conversations (proto.Session.Origin): the room, and the follow-up made after the meeting.
// The module writes them (its meeting_room.go names them the same).
const (
	OriginMeeting         = "meeting"
	OriginMeetingFollowup = "meeting_followup"
)

// attendeeNeverOrigins are the origins of assistant conversations whose replies leave the apps: they never attend a
// meeting (the module asks, AttendeeNeverOrigin), and no outward reader reads them.
var attendeeNeverOrigins = []string{OriginLarkAgent, OriginChannel, OriginExtension, OriginInbox}

// justMeMsgPrefix starts the client message id of a Just me message (the module's meeting_justme.go sends them).
const justMeMsgPrefix = "just-me-"

// promptSourceUser is a prompt the user typed (a meeting turn's prompt_source).
const promptSourceUser = "user"

// justMeNotice is what a tool that would notify the owner during a Just me turn says instead.
func justMeNotice(l i18n.Lang) string { return i18n.T(l, "assistant-meeting.just_me_notify") }

// meetingSource: send params p carry a message whose source is a meeting.
func meetingSource(p map[string]any) bool {
	src, _ := p["message_source"].(*proto.MessageSource)
	return src != nil && src.Kind == proto.SourceMeeting
}

func (s *Service) recordingRoutes(engine func(http.ResponseWriter, *http.Request) *kb.Engine) {
	if s.rec != nil {
		s.rec.Routes(engine)
	}
}

// recordingAudioSocket is the phone's audio socket; 404 without the module, as an unknown route.
func (s *Service) recordingAudioSocket(w http.ResponseWriter, r *http.Request) {
	if s.rec == nil {
		http.NotFound(w, r)
		return
	}
	s.rec.AudioSocket(w, r)
}

func (s *Service) recordingHook(e *kb.Engine) {
	if s.rec != nil {
		s.rec.Hook(e, kb.RecordingCore(e))
	}
}

func (s *Service) attendResume(e *kb.Engine) {
	if s.rec != nil {
		s.rec.AttendResume(e)
	}
}

func (s *Service) finalPasses(ctx context.Context) {
	if s.rec != nil {
		s.rec.FinalPasses(ctx)
	}
}

func (s *Service) meetingRoomsSweep(ctx context.Context, e *kb.Engine) {
	if s.rec != nil {
		s.rec.RoomsSweep(ctx, e)
	}
}

func (s *Service) hubSweep(e *kb.Engine) {
	if s.rec != nil {
		s.rec.HubSweep(e)
	}
}

func (s *Service) justMeNow(machine, session string) bool {
	return s.rec != nil && s.rec.JustMeNow(machine, session)
}

func (s *Service) justMeSent(machine, session string) {
	if s.rec != nil {
		s.rec.JustMeSent(machine, session)
	}
}

func (s *Service) justMeStarted(machine, session string, newTurn, woken bool) {
	if s.rec != nil {
		s.rec.JustMeStarted(machine, session, newTurn, woken)
	}
}

func (s *Service) justMeEnded(machine, session string, held bool) {
	if s.rec != nil {
		s.rec.JustMeEnded(machine, session, held)
	}
}

func (s *Service) meetingTurnNow(machine, session string) bool {
	return s.rec != nil && s.rec.MeetingTurnNow(machine, session)
}

func (s *Service) meetingTurnStarted(machine, session string, newTurn bool) bool {
	return s.rec != nil && s.rec.MeetingTurnStarted(machine, session, newTurn)
}

func (s *Service) meetingTurnEnded(machine, session string) bool {
	return s.rec != nil && s.rec.MeetingTurnEnded(machine, session)
}

func (s *Service) meetingTurnStopped(machine, session string, row proto.Session, restarted bool) bool {
	return s.rec != nil && s.rec.MeetingTurnStopped(machine, session, row, restarted)
}

func (s *Service) meetingTurnPrompt(machine, session string, p map[string]any) {
	if s.rec != nil {
		s.rec.MeetingTurnPrompt(machine, session, p)
	}
}

func (s *Service) meetingTurnRunning(ctx context.Context, machine, session string) (bool, error) {
	if s.rec == nil {
		return false, nil
	}
	return s.rec.MeetingTurnRunning(ctx, machine, session)
}

func (s *Service) meetingTurnAgentRunning(ctx context.Context, machine string) (bool, error) {
	if s.rec == nil {
		return false, nil
	}
	return s.rec.MeetingTurnAgentRunning(ctx, machine)
}

func (s *Service) meetingTurnUserSpoke(machine, session string) {
	if s.rec != nil {
		s.rec.MeetingTurnUserSpoke(machine, session)
	}
}

func (s *Service) meetingRefuses(ctx context.Context, machine, session, name string, assistant bool) (proto.ToolResult, bool) {
	if s.rec == nil {
		return proto.ToolResult{}, false
	}
	return s.rec.MeetingRefuses(ctx, machine, session, name, assistant)
}

func (s *Service) roomRefusesSend(ctx context.Context, machine, session, lang string) error {
	if s.rec == nil {
		return nil
	}
	return s.rec.RoomRefusesSend(ctx, machine, session, lang)
}

func (s *Service) roomToolCall(ctx context.Context, recording, name string, args json.RawMessage) proto.ToolResult {
	if s.rec == nil {
		return proto.TextResult("this meeting is not available now", true)
	}
	return s.rec.RoomToolCall(ctx, recording, name, args)
}

// meetingRoomTools are the meeting rooms' tools; none without the module.
func (s *Service) meetingRoomTools() []proto.Tool {
	if s.rec == nil {
		return nil
	}
	return s.rec.RoomTools()
}

// meetingRoomToolNames are their names; none without the module.
func (s *Service) meetingRoomToolNames() []string {
	if s.rec == nil {
		return nil
	}
	return s.rec.RoomToolNames()
}

// meetingTurnSent marks that a meeting's batch went to machine's session (the module's meeting turns); an error
// without the module.
func (s *Service) meetingTurnSent(machine, session, recording string, persona bool) (time.Time, error) {
	if s.rec == nil {
		return time.Time{}, errors.New("no recording module")
	}
	return s.rec.MeetingTurnSent(machine, session, recording, persona)
}
