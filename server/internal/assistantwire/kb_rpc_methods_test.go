package assistantwire_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestKnowledgeBaseRPCMethodsWire(t *testing.T) {
	checkMethodWire(t, assistantwire.KBListMethod, "kb.list",
		`{"kind":"projects","tag":"example","limit":1,"offset":2,"since":"2026-01-02T00:00:00Z"}`,
		`{"items":[],"tags":["example"],"total":4,"next_offset":3}`)
	checkMethodWire(t, assistantwire.KBRecentMethod, "kb.recent",
		`{"since":"2026-01-02T00:00:00Z","until":"2026-01-03T00:00:00Z","kinds":["recordings"],"limit":1}`,
		`{"items":[],"total":2,"next_offset":1}`)
	checkMethodWire(t, assistantwire.KBGetMethod, "kb.get",
		`{"kind":"chats","id":"example","recent_chars":0}`,
		`{"detail":{"kind":"chats","id":"example","path":"","meta":{},"summary":"Example"}}`)
	const dayParams = `{"id":"example-chat","day":"2026-01-02"}`
	checkMethodWire(t, assistantwire.KBDayMethod, "kb.day", dayParams,
		`{"sent_origin":"unavailable","day":"2026-01-02","messages":[{"n":1,"text":"Example"}],"prev":"","next":""}`)
	checkMethodWire(t, assistantwire.KBExtensionDayMethod, "kb.extension_day", dayParams,
		`{"sent_origin":"available","day":"2026-01-02","messages":[{"sent_by":"assistant","sent_ledger_id":7,"recalled_at":"2026-01-03T00:00:00Z","n":1,"text":"Example"}],"prev":"","next":""}`)
	checkMethodWire(t, assistantwire.KBSearchMethod, "kb.search",
		`{"query":"example","messages":false,"limit":2}`, `{"hits":[]}`)
	checkMethodWire(t, assistantwire.KBUpdateMethod, "kb.update",
		`{"kind":"projects","id":"example","summary":"Updated"}`,
		`{"id":"example","ignored":[{"field":"events","reason":"unsupported"}]}`)
	checkMethodWire(t, assistantwire.KBExtensionUpdateMethod, "kb.extension_update",
		`{"storage_id":"previous-id","extension":"example","params":{"kind":"projects","id":"example","summary":"Updated","evidence":[{"chat":"example-chat","day":"2026-01-02","n":1}],"unlock":["pinned"]}}`,
		`{"id":"example","before":{"summary":null}}`)
	checkMethodWire(t, assistantwire.KBExtensionPutRecordingMethod, "kb.extension_put_recording",
		`{"extension":"example","params":{"id":"55555555-5555-4555-8555-555555555555","started_at":"2026-01-02T00:00:00Z","minutes":{"markdown":"Example"}}}`,
		`{"id":"55555555-5555-4555-8555-555555555555","revision":1,"created":true,"minutes_version":1}`)
}

func TestMeetingTranscriptionChoiceMethodWire(t *testing.T) {
	checkMethodWire(t, assistantwire.MeetingTranscriptionChoiceMethod, "meeting.transcription_choice",
		`{"set":true,"use":true,"extension":"example/text"}`, `{"extension":"example/text","default":"hub"}`)
	checkJSONRoundTrip[assistantwire.MeetingTranscriptionChoose](t, `{}`)
	checkJSONRoundTrip[assistantwire.MeetingTranscriptionChoose](t, `{"set":true,"extension":"example/text"}`)
	checkJSONRoundTrip[assistantwire.MeetingTranscriptionChoice](t, `{"extension":"","default":"device"}`)
}
