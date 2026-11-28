package assistantwire

// CapabilityMeetingSpeakers: the manager supports speaker separation data on hub-transcribed meetings
// (detail.speakers, detail.segments[].speaker / speaker_unsure, POST /api/v1/recordings/{id}/speakers, and speakers in
// meeting_live; docs/assistant/meeting-minutes.md "Speakers (#17674)"). The hub lists meeting_speakers only while the
// default manager names it beside CapabilityMeetingTranscription.
const CapabilityMeetingSpeakers = "meeting_speakers"

// recordingSpeakerRoutes are the speaker write routes served by the recording module (full credentials only; not in
// extwire.RouteScopes).
var recordingSpeakerRoutes = []string{
	"POST /api/v1/recordings/{id}/speakers",
	"PUT /api/v1/recordings/{id}/speakers/turns",
}

func init() { Routes = append(Routes, recordingSpeakerRoutes...) }
