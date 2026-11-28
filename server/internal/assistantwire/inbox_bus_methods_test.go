package assistantwire_test

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestInboxBusMethodsWire(t *testing.T) {
	checkMethodWire[assistantwire.InboxRound, assistantwire.InboxAck](t, assistantwire.InboxCollectedMethod, "inbox.collected",
		`{"epoch":"example","seq":1,"round":"poll","sources":[]}`, `{}`)
	checkMethodWire[assistantwire.InboxItemParams, assistantwire.InboxItemResult](t, assistantwire.InboxItemMethod, "inbox.item",
		`{"id":"example:1"}`, `{"item":{"id":"example:1","title":"Example"},"ok":true}`)
	checkMethodWire[assistantwire.InboxForgetParams, assistantwire.InboxForgetResult](t, assistantwire.InboxForgetMethod, "inbox.forget",
		`{"source":"example"}`, `{"deleted":3}`)
	checkMethodWire[assistantwire.InboxRepoMutes, assistantwire.InboxRepoMutesResult](t, assistantwire.InboxRepoMutesMethod, "inbox.repo_mutes",
		`{"source":"github","set":true,"add":true,"repos":["example/site"]}`, `{"repos":["example/site"]}`)
	checkMethodWire[assistantwire.InboxMutesParams, assistantwire.InboxMutesResult](t, assistantwire.InboxMutesMethod, "inbox.mutes",
		`{"source":""}`, `{"mutes":[]}`)
	checkMethodWire[assistantwire.InboxPostParams, assistantwire.InboxPostResult](t, assistantwire.InboxPostMethod, "inbox.post",
		`{}`, `{"on":false}`)
	checkMethodWire[assistantwire.InboxTriagePreview, assistantwire.InboxTriagePreviewResult](t, assistantwire.InboxTriagePreviewMethod,
		"inbox.triage_preview", `{"source":"example","name":"Example","things":[]}`, `{"verdicts":[{"tier":"fyi","summary":"A note"}]}`)
	checkMethodWire[assistantwire.InboxUnmuteParams, assistantwire.InboxAck](t, assistantwire.InboxUnmuteMethod, "inbox.unmute",
		`{"id":"m1"}`, `{}`)
}
