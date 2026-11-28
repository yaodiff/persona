package assistant

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

// meetingChoiceRequest answers the manager's meeting.transcription_choice (assistantwire.MeetingTranscriptionChoose,
// docs/extensions/volcengine-speech.md "One switch"): the built-in Volcengine speech extension's switch reads and
// sets the meeting transcription choice, as PUT /api/v1/assistant would (the owner made the change on a full device).
// On: the contribution is chosen, pinned to its installation, and new meetings transcribe on the hub. Off: a choice of
// that contribution goes back to the built-in one and new meetings to the phone; another choice stays.
func (s *Service) meetingChoiceRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	if method != assistantwire.MethodMeetingTranscriptionChoice {
		return nil, false, nil
	}
	decode := func(out any) error {
		if len(params) > 0 {
			if err := json.Unmarshal(params, out); err != nil {
				return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + err.Error()}
			}
		}
		return nil
	}
	return handleAssistantRequest(ctx, assistantwire.MeetingTranscriptionChoiceMethod, decode, s.meetingTranscriptionChoice)
}

func (s *Service) meetingTranscriptionChoice(ctx context.Context, p assistantwire.MeetingTranscriptionChoose) (assistantwire.MeetingTranscriptionChoice, error) {
	if p.Set {
		if p.Extension == "" || p.Use && (s.trans == nil || !s.trans.IsTranscriptionContribution(p.Extension)) {
			return assistantwire.MeetingTranscriptionChoice{}, &rpc.Error{Code: bus.CodeInvalidParams,
				Message: assistantwire.MethodMeetingTranscriptionChoice + ": not a transcription contribution"}
		}
		changed := false
		if _, err := s.updateSettings(ctx, func(v *Settings) error {
			switch {
			case p.Use:
				ref, install, hub := p.Extension, s.trans.TranscriptionInstall(p.Extension), "hub"
				v.MeetingTranscriptionExtension, v.MeetingTranscriptionInstall, v.MeetingTranscriptionDefault = &ref, &install, &hub
				changed = true
			case meetingTranscriptionExtensionOf(*v) == p.Extension:
				builtIn, device := "", "device"
				v.MeetingTranscriptionExtension, v.MeetingTranscriptionInstall, v.MeetingTranscriptionDefault = &builtIn, nil, &device
				changed = true
			}
			return nil
		}); err != nil {
			return assistantwire.MeetingTranscriptionChoice{}, err
		}
		if changed {
			s.settingsChanged(ctx)
			s.transcriptionReadinessChanged() // named again: probed again
		}
	}
	v := s.settings(ctx)
	return assistantwire.MeetingTranscriptionChoice{Extension: meetingTranscriptionExtensionOf(v), Default: meetingTranscriptionDefaultOf(v)}, nil
}
