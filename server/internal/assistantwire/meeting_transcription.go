package assistantwire

import "github.com/nwverse/persona/server/internal/buswire"

// The meeting transcription choice, for the manager's built-in Volcengine speech extension (docs/extensions/volcengine-speech.md
// "One switch"): its settings' "Use for Meeting Transcription" shows and sets the assistant's
// meeting_transcription_extension, so the owner turns it on in one place. Only the manager calls it, for a change the
// owner made in that extension's settings (a full device); it is not on the extensions' allowlist.
const MethodMeetingTranscriptionChoice = "meeting.transcription_choice"

var MeetingTranscriptionChoiceMethod = buswire.Method[MeetingTranscriptionChoose, MeetingTranscriptionChoice]{Name: MethodMeetingTranscriptionChoice}

// MeetingTranscriptionChoose is MethodMeetingTranscriptionChoice's params: without Set it only reads the choice. With
// Set and Use, Extension (a transcription contribution, "<extension id>/<contribution id>") becomes the choice and new
// meetings default to transcribing on the hub; with Set and not Use, a choice of Extension goes back to the built-in
// one ("") and new meetings to the phone (any other choice is left as it is).
type MeetingTranscriptionChoose struct {
	Set       bool   `json:"set,omitempty"`
	Use       bool   `json:"use,omitempty"`
	Extension string `json:"extension,omitempty"`
}

// MeetingTranscriptionChoice is the choice after the call: meeting_transcription_extension and
// meeting_transcription_default (device or hub).
type MeetingTranscriptionChoice struct {
	Extension string `json:"extension"`
	Default   string `json:"default"`
}
