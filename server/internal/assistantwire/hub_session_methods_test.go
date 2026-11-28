package assistantwire_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestHubSessionMethodsWire(t *testing.T) {
	const at = "2026-11-27T00:00:00Z"
	checkMethodWire[assistantwire.FleetAskParams, proto.Resolution](t, assistantwire.FleetAskMethod, "fleet.ask",
		`{"interaction":{"id":"ask","machine_id":"box","session_id":"conversation","kind":"permission",`+
			`"created_at":"`+at+`","expires_at":"2026-11-27T00:15:00Z"}}`, `{"allow":true}`)

	// Use the session codec for the contents that the original reply wrapped in "session".
	session, err := json.Marshal(proto.Session{
		MachineID: "box", SessionID: "conversation", StartedByPersona: true, Assistant: true, Title: "New conversation",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkMethodWire[assistantwire.StartParams, assistantwire.StartResult](t, assistantwire.StartMethod, "assistant.start",
		`{"text":"Start a conversation","machine_id":"box"}`, `{"session":`+string(session)+`}`)

	checkMethodWire[assistantwire.KeepUndeliveredParams, assistantwire.KeepUndeliveredResult](t,
		assistantwire.KeepUndeliveredMethod, "assistant.keep_undelivered",
		`{"machine_id":"box","session_id":"conversation","params":{"client_msg_id":"message","text":"Try again"},`+
			`"human":true,"credential_id":"device","tried_at":"`+at+`"}`,
		`{"undelivered":{"id":"message","text":"Try again","at":"`+at+`","state":"waiting"}}`)
	checkJSONRoundTrip[assistantwire.KeepUndeliveredResult](t, `{"undelivered":null}`)

	checkMethodWire[assistantwire.AgentTabNameParams, assistantwire.AgentTabNameResult](t,
		assistantwire.AgentTabNameMethod, "agents.tab_name", `{"prompt":"Fix login"}`, `{"name":"fix-login"}`)
	checkJSONRoundTrip[assistantwire.AgentTabNameResult](t, `{"name":""}`)
}
