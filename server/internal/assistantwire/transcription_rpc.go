package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
)

// Transcription for extensions (#8485, docs/extensions/extensions.md "Transcription for extensions",
// schema/extension/transcription.schema.json): an extension granted the transcription scope hands its own audio, a
// piece at a time, to the transcription the owner chose for meetings. The manager checks the scope and hands each call
// to the assistant with the extension's id (ExtensionTranscription), which runs the job on that contribution's replay
// mode. The extension never sees a key, nor anyone else's audio. No budget (the owner's decision, as for model calls).
const (
	MethodTranscriptionStart  = "transcription.start"
	MethodTranscriptionAudio  = "transcription.audio"
	MethodTranscriptionFinish = "transcription.finish"
	MethodTranscriptionCancel = "transcription.cancel"
	// MethodTranscriptionExtension is how the manager hands the assistant one of the four: ExtensionTranscription → that
	// call's result. Not on the extensions' allowlist.
	MethodTranscriptionExtension = "transcription.extension"
)

var (
	TranscriptionStartMethod     = buswire.Method[TranscriptionStartParams, TranscriptionStartResult]{Name: MethodTranscriptionStart}
	TranscriptionAudioMethod     = buswire.Method[TranscriptionAudioParams, buswire.None]{Name: MethodTranscriptionAudio}
	TranscriptionFinishMethod    = buswire.Method[TranscriptionJobParams, TranscriptionFinishResult]{Name: MethodTranscriptionFinish}
	TranscriptionCancelMethod    = buswire.Method[TranscriptionJobParams, buswire.None]{Name: MethodTranscriptionCancel}
	TranscriptionExtensionMethod = buswire.Method[ExtensionTranscription, json.RawMessage]{Name: MethodTranscriptionExtension}
)

// The transcription calls' error codes besides permission_denied, CodeInvalidParams and CodeAssistantOffline
// (TranscriptionErrorCode in the schema).
const (
	// CodeTranscriptionUnavailable: no transcription is chosen, ready (its /info has setup) or able to replay.
	CodeTranscriptionUnavailable = "transcription_unavailable"
	// CodeTranscriptionBusy: the extension has a job running, or the most extensions' jobs at once already run.
	CodeTranscriptionBusy = "busy"
	// CodeTranscriptionTooLong: the audio went past the most a job takes; the job is ended.
	CodeTranscriptionTooLong = "too_long"
	// CodeTranscriptionTimeout: the transcription stopped answering; the job is ended.
	CodeTranscriptionTimeout = "timeout"
	// CodeTranscriptionFailed: the transcription failed the job (a fatal error frame); data {code}.
	CodeTranscriptionFailed = "transcription_failed"
)

// ExtensionTranscription is one of an extension's transcription calls as the manager hands it to the assistant: the
// extension's id (which it cannot set), the method and its params as the extension sent them.
type ExtensionTranscription struct {
	Extension string          `json:"extension"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params,omitempty"`
}

// TranscriptionStartParams is transcription.start's.
type TranscriptionStartParams struct {
	Language string `json:"language,omitempty"`
	Label    string `json:"label,omitempty"`
}

// TranscriptionStartResult is transcription.start's answer.
type TranscriptionStartResult struct {
	Job string `json:"job"`
}

// TranscriptionAudioParams is transcription.audio's: base64 PCM16 LE mono 16 kHz.
type TranscriptionAudioParams struct {
	Job  string `json:"job"`
	Data string `json:"data"`
}

// TranscriptionJobParams is transcription.finish's and transcription.cancel's.
type TranscriptionJobParams struct {
	Job string `json:"job"`
}

// TranscriptionSegment is one sentence, in seconds from the start of the audio.
type TranscriptionSegment struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Text string  `json:"text"`
}

// TranscriptionGap is a span no text was trusted for.
type TranscriptionGap struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// TranscriptionFinishResult is transcription.finish's answer.
type TranscriptionFinishResult struct {
	Segments []TranscriptionSegment `json:"segments"`
	Gaps     []TranscriptionGap     `json:"gaps,omitempty"`
	Seconds  float64                `json:"seconds"`
}
