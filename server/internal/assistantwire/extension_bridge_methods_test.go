package assistantwire_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
)

func TestExtensionBridgeMethodsWire(t *testing.T) {
	checkMethodWire[assistantwire.TranscriptionStartParams, assistantwire.TranscriptionStartResult](t,
		assistantwire.TranscriptionStartMethod, "transcription.start",
		`{"language":"en","label":"example"}`, `{"job":"example-job"}`)
	checkMethodWire[assistantwire.TranscriptionAudioParams, buswire.None](t,
		assistantwire.TranscriptionAudioMethod, "transcription.audio",
		`{"job":"example-job","data":"AAE="}`, `{}`)
	checkMethodWire[assistantwire.TranscriptionJobParams, assistantwire.TranscriptionFinishResult](t,
		assistantwire.TranscriptionFinishMethod, "transcription.finish",
		`{"job":"example-job"}`, `{"segments":[{"from":0.5,"to":1.5,"text":"Example"}],"gaps":[{"from":1.5,"to":2}],"seconds":2}`)
	checkMethodWire[assistantwire.TranscriptionJobParams, buswire.None](t,
		assistantwire.TranscriptionCancelMethod, "transcription.cancel",
		`{"job":"example-job"}`, `{}`)
	checkMethodWire[assistantwire.ExtensionTranscription, json.RawMessage](t,
		assistantwire.TranscriptionExtensionMethod, "transcription.extension",
		`{"extension":"example","method":"transcription.start","params":{"label":"example"}}`,
		`{"job":"example-job","future_field":{"receipt":9007199254740993}}`)
	checkMethodWire[assistantwire.AssistantExtensionRead, json.RawMessage](t,
		assistantwire.AssistantExtensionReadMethod, "assistant.extension_read",
		`{"extension":"example","method":"assistant.memory","params":{"include_content":true}}`,
		`{"entries":[],"future_field":{"count":9007199254740993}}`)
}
