package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

// A session's preview and quota go through the hub as the source wrote them: unknown quota fields included, nothing
// added when the source sent none.
func TestAgentSessionPassesPreviewAndQuotaThrough(t *testing.T) {
	in := `{"sessionKey":"k","accountId":"a","provider":"p","title":"T","kind":"main","canSend":true,"unreadCount":1,"rankTimeMs":5,` +
		`"previewText":"hi","previewIsMine":true,"onTopRankTimeMs":7,` +
		`"quota":{"state":"exhausted","remaining":0,"show":false,"buckets":[{"id":"day"}],"newer":{"x":1}}}`
	var s AgentSession
	if err := json.Unmarshal([]byte(in), &s); err != nil {
		t.Fatal(err)
	}
	if s.PreviewText != "hi" || !s.PreviewIsMine || s.OnTopRankTimeMs != 7 || !strings.Contains(string(s.Quota), `"state":"exhausted"`) {
		t.Fatalf("decoded %+v", s)
	}
	out, err := json.Marshal(Wrap("src", s, ""))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"previewText":"hi"`, `"previewIsMine":true`, `"onTopRankTimeMs":7`, `"newer":{"x":1}`, `"show":false`, `"buckets":[{"id":"day"}]`} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("%s missing from %s", want, out)
		}
	}
	var bare AgentSession
	_ = json.Unmarshal([]byte(`{"sessionKey":"k","provider":"p","title":"T","canSend":false,"unreadCount":0,"rankTimeMs":1}`), &bare)
	b, _ := json.Marshal(bare)
	for _, absent := range []string{"quota", "previewText", "previewIsMine", "onTopRankTimeMs"} {
		if strings.Contains(string(b), absent) {
			t.Fatalf("%s written for a source without it: %s", absent, b)
		}
	}
}
