package assistantwire_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestOutsideDecisionMethodsWire(t *testing.T) {
	const decision = `"title":"Publish?","background":"Checks passed","why_owner":"Priority",` +
		`"options":[{"id":"hold","label":"Hold","leads_to":"Wait for confirmation"}],` +
		`"recommend":"Hold","cost_of_waiting":"The build waits","reversible":true`
	const request = `{"decision":{` + decision + `}`
	const pending = `{"id":"odc_example","ask_todo_id":"todo_od_example","state":"pending",` +
		`"source":{"kind":"script","name":"nightly"},` + decision + `,"default":"none","created_at":"2026-11-27T00:00:00Z"}`

	checkMethodWire[assistantwire.OutsideDecisionRaiseParams, assistantwire.OutsideDecisionReply](t,
		assistantwire.DecisionRaiseMethod, "decision.raise", request+`,"cwd":"/work/nightly","source":"nightly"}`,
		`{"decision":`+pending+`,"inserted":true}`)
	checkJSONRoundTrip[assistantwire.OutsideDecisionRaiseParams](t, request+`,"source":""}`)
	checkMethodWire[assistantwire.OutsideDecisionIDParams, assistantwire.OutsideDecisionReply](t,
		assistantwire.DecisionGetMethod, "decision.get", `{"id":"odc_example"}`, `{"decision":`+pending+`}`)
	checkMethodWire[assistantwire.OutsideDecisionIDParams, assistantwire.OutsideDecisionReply](t,
		assistantwire.DecisionWithdrawMethod, "decision.withdraw", `{"id":"odc_example"}`,
		`{"code":"decision_not_found","error":"decision not found"}`)
	checkJSONRoundTrip[assistantwire.OutsideDecisionIDParams](t, `{"id":""}`)
}
