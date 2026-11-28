package apierr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A node's engine_missing about one engine (node/orcaengine.go orcaMissing, #17037) is said in the request's language,
// naming the engine and what to do by why it could not take the start, with its fields and the engine's own error
// kept; a stored receipt replays it in another language the same way. One without a known engine and reason (an
// older node, a refusal about the machine's whole engine set) keeps its words.
func TestEngineMissingSpeaksTheClientsLanguage(t *testing.T) {
	const words = "node's own words"
	for _, reason := range []string{"not_running", "not_installed", "incompatible"} {
		data := `{"delivered":"none","engine":"orca","reason":"` + reason + `","detail":"orca is not running"}`
		err := &rpc.Error{Code: "engine_missing", Message: words, Data: json.RawMessage(data)}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, err)
			want := i18n.T(lang, "rpc-errors.engine_"+reason, i18n.Args{"engine": "Orca"})
			if want == "rpc-errors.engine_"+reason || !strings.Contains(want, "Orca") || strings.Contains(want, "herdr") || body["error"] != want {
				t.Fatalf("%s %s: %v", reason, lang, body)
			}
			if rec.Code != http.StatusConflict || body["code"] != "engine_missing" || body["delivered"] != "none" ||
				body["engine"] != "orca" || body["reason"] != reason || body["detail"] != "orca is not running" {
				t.Fatalf("%s %s: %d %v", reason, lang, rec.Code, body)
			}
			stored, _ := json.Marshal(body)
			var replayed map[string]any
			_ = json.Unmarshal(LocalizeErrorJSON(i18n.Ja, stored), &replayed)
			if replayed["error"] != i18n.T(i18n.Ja, "rpc-errors.engine_"+reason, i18n.Args{"engine": "Orca"}) {
				t.Fatalf("%s replayed in ja: %v", reason, replayed)
			}
		}
	}
	for _, data := range []string{`{"delivered":"none"}`, `{"delivered":"none","engine":"orca","reason":"elsewhere"}`,
		`{"delivered":"none","engine":"nothing","reason":"not_running"}`} {
		err := &rpc.Error{Code: "engine_missing", Message: words, Data: json.RawMessage(data)}
		rec := httptest.NewRecorder()
		if body := call(t, langWriter{rec, i18n.En}, rec, err); body["error"] != words {
			t.Fatalf("%s: %v", data, body)
		}
	}
}
