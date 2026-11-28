package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// featureMeetingAttendance: the default manager attends recorded meetings (docs/assistant/meeting-minutes.md
// "Attending a meeting"). The recordings' routes go to it, so its hello alone decides; an extension's report cannot
// add the feature (features).
const featureMeetingAttendance = assistantwire.CapabilityMeetingAttendance

// featureMeetingConversation: the default manager takes the meeting conversation's phase 1
// (docs/assistant/meeting-minutes.md "Meeting conversation (#3862)", "Flags and compatibility").
const featureMeetingConversation = assistantwire.CapabilityMeetingConversation

// featureMeetingPhotos: the default manager takes the meeting conversation's photos (phase 3,
// docs/assistant/meeting-minutes.md "Phase 3: photos"); only with meeting_conversation.
const featureMeetingPhotos = assistantwire.CapabilityMeetingPhotos

// featureMeetingLiveView: the default manager takes the meeting conversation's live view (phase 2,
// docs/assistant/meeting-minutes.md "Phase 2: live view"); only with meeting_conversation.
const featureMeetingLiveView = assistantwire.CapabilityMeetingLiveView

// featureMeetingHighlights: the default manager takes the meeting conversation's highlights (phase 2b,
// docs/assistant/meeting-minutes.md "Phase 2b: highlights"); only with meeting_live_view.
const featureMeetingHighlights = assistantwire.CapabilityMeetingHighlights

// featureMeetingNotes: the default manager takes a room meeting's notes role (#8851, docs/assistant/meeting-minutes.md
// "The notes role (#8851)"); only with meeting_live_view, whose topics and highlights a notes room keeps.
const featureMeetingNotes = assistantwire.CapabilityMeetingNotes

// featureMeetingEcho: the default manager takes Persona's echo marked on a meeting's upload (#5905,
// docs/assistant/meeting-minutes.md "Persona's echo"); only with meeting_attendance.
const featureMeetingEcho = assistantwire.CapabilityMeetingEcho

// featureMeetingTranscription: the default manager transcribes meetings on the hub (phase 5,
// docs/assistant/meeting-minutes.md "Phase 5: hub transcription"); only with meeting_conversation and
// meeting_live_view, whose lines it writes.
const featureMeetingTranscription = assistantwire.CapabilityMeetingTranscription

// featureMeetingSpeakers: the default manager serves speaker separation data on hub-transcribed meetings (#17674,
// docs/assistant/meeting-minutes.md "Speakers (#17674)"); only with meeting_transcription.
const featureMeetingSpeakers = assistantwire.CapabilityMeetingSpeakers

// meetingAttendanceFeatures is the feature while the default manager is connected and named the capability in its
// hello; nothing otherwise, so a phone offers no role to an older manager, which would ignore it.
func (h *Hub) meetingAttendanceFeatures() []string {
	if !h.defaultManagerNames(assistantwire.CapabilityMeetingAttendance) {
		return nil
	}
	return []string{featureMeetingAttendance}
}

// meetingConversationFeatures is meeting_conversation while the default manager names it beside meeting_attendance
// (a manager that takes the room takes #2981 too); nothing otherwise, so a phone asks no older manager for a room.
func (h *Hub) meetingConversationFeatures() []string {
	if !h.defaultManagerNames(assistantwire.CapabilityMeetingAttendance) || !h.defaultManagerNames(assistantwire.CapabilityMeetingConversation) {
		return nil
	}
	return []string{featureMeetingConversation}
}

// meetingPhotosFeatures is meeting_photos while the default manager names it beside meeting_conversation (and so
// meeting_attendance); nothing otherwise, so a phone sends no photos to a manager that would answer 404.
func (h *Hub) meetingPhotosFeatures() []string {
	if len(h.meetingConversationFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingPhotos) {
		return nil
	}
	return []string{featureMeetingPhotos}
}

// meetingLiveViewFeatures is meeting_live_view while the default manager names it beside meeting_conversation (and so
// meeting_attendance); nothing otherwise, so a phone writes no shorter segments for, and no app asks lines or topics
// of, a manager that has none.
func (h *Hub) meetingLiveViewFeatures() []string {
	if len(h.meetingConversationFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingLiveView) {
		return nil
	}
	return []string{featureMeetingLiveView}
}

// meetingHighlightsFeatures is meeting_highlights while the default manager names it beside meeting_live_view (and so
// meeting_conversation and meeting_attendance); nothing otherwise, so no app looks for highlights a manager never
// keeps.
func (h *Hub) meetingHighlightsFeatures() []string {
	if len(h.meetingLiveViewFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingHighlights) {
		return nil
	}
	return []string{featureMeetingHighlights}
}

// meetingNotesFeatures is meeting_notes while the default manager names it beside meeting_live_view (and so
// meeting_conversation and meeting_attendance); nothing otherwise, so a phone sends notes to no manager that would
// read it as an unknown role (off).
func (h *Hub) meetingNotesFeatures() []string {
	if len(h.meetingLiveViewFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingNotes) {
		return nil
	}
	return []string{featureMeetingNotes}
}

// meetingEchoFeatures is meeting_echo while the default manager names it beside meeting_attendance (Persona speaks
// only in a meeting it attends); nothing otherwise, so a phone sends no echo line to a manager that would put it into
// the minutes.
func (h *Hub) meetingEchoFeatures() []string {
	if len(h.meetingAttendanceFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingEcho) {
		return nil
	}
	return []string{featureMeetingEcho}
}

// meetingTranscriptionFeatures is meeting_transcription while the default manager names it beside meeting_live_view
// (and so meeting_conversation and meeting_attendance); nothing otherwise, so a phone opens no audio socket to, and
// asks no hub mode of, a manager that has none. Whether a contribution is ready to transcribe is the manager's to say
// (meeting_transcription_ready), not the hub's.
func (h *Hub) meetingTranscriptionFeatures() []string {
	if len(h.meetingLiveViewFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingTranscription) {
		return nil
	}
	return []string{featureMeetingTranscription}
}

// meetingSpeakersFeatures is meeting_speakers while the default manager names it beside meeting_transcription (and so
// meeting_live_view, meeting_conversation and meeting_attendance); nothing otherwise, so no client expects speaker
// fields or edit routes from a manager that has none.
func (h *Hub) meetingSpeakersFeatures() []string {
	if len(h.meetingTranscriptionFeatures()) == 0 || !h.defaultManagerNames(assistantwire.CapabilityMeetingSpeakers) {
		return nil
	}
	return []string{featureMeetingSpeakers}
}

// defaultManagerNames: the default manager (the one the recordings' routes go to) is connected and named capability
// in its hello.
func (h *Hub) defaultManagerNames(capability string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return h.archiveManagerHas(h.defaultOwner(ctx), capability) == http.StatusOK
}
