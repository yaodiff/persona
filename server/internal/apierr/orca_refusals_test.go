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

// Every refusal an Orca path can return reaches the client in the request's language (#17057): its code stays as the
// node wrote it, its words come from the catalog by code and data reason, filled from the data (the engine's own words,
// the engine, a program, a number), and the node's own words become the detail unless the data carries one.
func TestOrcaPathRefusalsSpeakTheClientsLanguage(t *testing.T) {
	const words = "node words"
	for _, tc := range []struct {
		code, data, key string
		args            i18n.Args
		detail          string // "" is the node's words
	}{
		{code: "orca_not_registered", data: `{"delivered":"none"}`, key: "orca_not_registered"},
		{code: "orca_workspace_unavailable", data: `{"delivered":"none"}`, key: "orca_workspace_unavailable"},
		{code: "runtime_session", data: `{"delivered":"none"}`, key: "runtime_session"},
		{code: "codex_launch_preferences", data: `{"delivered":"none"}`, key: "codex_launch_preferences"},
		{code: "codex_launch_preferences", data: `{"delivered":"none","reason":"unreadable"}`, key: "codex_launch_preferences_unreadable"},
		{code: "codex_launch_preferences", data: `{"delivered":"none","reason":"custom"}`, key: "codex_launch_preferences_custom"},
		{code: "codex_launch_preferences", data: `{"delivered":"none","reason":"shell"}`, key: "codex_launch_preferences_shell"},
		{code: "codex_launch_preferences", data: `{"delivered":"none","reason":"quoting"}`, key: "codex_launch_preferences_quoting"},
		{code: "terminal_open_failed", data: `{"detail":"orca: terminal.create refused"}`, key: "terminal_open_failed",
			args: i18n.Args{"detail": "orca: terminal.create refused"}, detail: "orca: terminal.create refused"},
		{code: "tab_close_failed", data: `{"detail":"orca: the selected tab is no longer available"}`, key: "tab_close_failed",
			args: i18n.Args{"detail": "orca: the selected tab is no longer available"}, detail: "orca: the selected tab is no longer available"},
		{code: "pane_close_failed", data: `{"detail":"orca: refused"}`, key: "pane_close_failed",
			args: i18n.Args{"detail": "orca: refused"}, detail: "orca: refused"},
		{code: "pane_split_failed", data: `{"detail":"orca: the terminal cannot be split now"}`, key: "pane_split_failed",
			args: i18n.Args{"detail": "orca: the terminal cannot be split now"}, detail: "orca: the terminal cannot be split now"},
		{code: "tab_name_invalid", data: `{"max":80}`, key: "tab_name_invalid", args: i18n.Args{"max": 80}},
		{code: "tab_gone", key: "tab_gone"},
		{code: "pane_gone", key: "pane_gone"},
		{code: "pane_has_agent", data: `{"delivered":"none"}`, key: "pane_has_agent"},
		{code: "pane_not_at_prompt", data: `{"delivered":"none","program":"vim"}`, key: "pane_not_at_prompt", args: i18n.Args{"program": "vim"}},
		{code: "pane_directory_unknown", data: `{"delivered":"none"}`, key: "pane_directory_unknown"},
		{code: "pane_foreground_unknown", data: `{"delivered":"none"}`, key: "pane_foreground_unknown"},
		{code: "pane_foreground_unknown", data: `{"delivered":"none","reason":"unreadable","detail":"ps: exit status 1"}`,
			key: "pane_foreground_unreadable", args: i18n.Args{"detail": "ps: exit status 1"}, detail: "ps: exit status 1"},
		{code: "close_not_in_terminal", key: "close_not_in_terminal"},
		{code: "agent_busy", data: `{"delivered":"none","reason":"terminal_changed"}`, key: "agent_busy_terminal_changed"},
		{code: "not_running", data: `{"delivered":"none","reason":"directory_gone"}`, key: "not_running_directory_gone"},
		{code: "not_running", data: `{"delivered":"none","reason":"resume_timeout","seconds":30,"detail":"$ pi --session"}`,
			key: "not_running_resume_timeout", args: i18n.Args{"seconds": 30}, detail: "$ pi --session"},
		{code: "not_running", data: `{"delivered":"none","reason":"agent_exited"}`, key: "not_running_agent_exited"},
		{code: "not_delivered", data: `{"delivered":"none","reason":"screen_unreadable"}`, key: "not_delivered_screen_unreadable"},
		{code: "engine_missing", data: `{"delivered":"none","engine":"orca","reason":"unavailable","detail":"Orca: refused"}`,
			key: "engine_unavailable", args: i18n.Args{"engine": "Orca"}, detail: "Orca: refused"},
		{code: "engine_missing", data: `{"delivered":"none","engine":"herdr","reason":"start_failed","detail":"herdr: no socket"}`,
			key: "engine_start_failed", args: i18n.Args{"engine": "herdr"}, detail: "herdr: no socket"},
		{code: "engine_missing", data: `{"delivered":"none","engine":"orca","reason":"resume_unavailable"}`,
			key: "engine_resume_unavailable", args: i18n.Args{"engine": "Orca"}},
		{code: "engine_missing", data: `{"delivered":"none","engine":"orca","reason":"not_enabled"}`,
			key: "engine_not_enabled", args: i18n.Args{"engine": "Orca"}},
		{code: "engine_missing", data: `{"delivered":"none","reason":"none"}`, key: "engine_none"},
		{code: "unsupported", data: `{"reason":"engine_unsupported","engine":"orca"}`, key: "engine_unsupported", args: i18n.Args{"engine": "Orca"}},
		{code: "unsupported", data: `{"reason":"engine_unsupported","engine":"botmux"}`, key: "engine_unsupported", args: i18n.Args{"engine": "botmux"}},
		{code: "unsupported", data: `{"reason":"orca_unsupported"}`, key: "orca_unsupported"},
	} {
		var data json.RawMessage
		if tc.data != "" {
			data = json.RawMessage(tc.data)
		}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: tc.code, Message: words, Data: data})
			want := i18n.T(lang, "rpc-errors."+tc.key, tc.args)
			if want == "rpc-errors."+tc.key || strings.ContainsAny(want, "{}") || body["error"] != want {
				t.Errorf("%s %s %s: error %q, want %q", tc.code, tc.data, lang, body["error"], want)
			}
			if rec.Code != http.StatusConflict || body["code"] != tc.code {
				t.Errorf("%s %s %s: %d %v", tc.code, tc.data, lang, rec.Code, body)
			}
			if detail := tc.detail; body["detail"] != detail && (detail != "" || body["detail"] != words) {
				t.Errorf("%s %s %s: detail %v", tc.code, tc.data, lang, body["detail"])
			}
		}
		// A request without a language keeps the node's words, as before.
		rec := httptest.NewRecorder()
		if body := call(t, rec, rec, &rpc.Error{Code: tc.code, Message: words, Data: data}); body["error"] != words {
			t.Errorf("%s %s without a language: %v", tc.code, tc.data, body)
		}
	}
}

// A reason the hub has no words for, or an engine it does not know, keeps the node's words (an older or newer node).
func TestOrcaPathRefusalsWithoutKnownReasonKeepTheirWords(t *testing.T) {
	const words = "node words"
	for _, tc := range []struct{ code, data string }{
		{"agent_busy", `{"delivered":"none"}`}, // the Agent no longer waits for confirmation (node.go): its own words
		{"agent_busy", `{"delivered":"none","reason":"elsewhere"}`},
		{"not_running", `{"delivered":"none","reason":"elsewhere"}`},
		{"unsupported", `{"reason":"elsewhere"}`},
		{"unsupported", `{}`},
		{"engine_missing", `{"delivered":"none","engine":"nothing","reason":"unavailable"}`},
	} {
		rec := httptest.NewRecorder()
		if body := call(t, langWriter{rec, i18n.En}, rec, &rpc.Error{Code: tc.code, Message: words, Data: json.RawMessage(tc.data)}); body["error"] != words {
			t.Errorf("%s %s: %v", tc.code, tc.data, body)
		}
	}
}

// A stored receipt of one of these refusals replays in another language the same way (client_ops): the engine's and
// the unread screen's, which LocalizeErrorJSON knows by their reasons.
func TestOrcaPathRefusalsReplayInAnotherLanguage(t *testing.T) {
	for _, tc := range []struct {
		data, key string
		args      i18n.Args
	}{
		{`{"code":"engine_missing","error":"x","engine":"orca","reason":"unavailable"}`, "engine_unavailable", i18n.Args{"engine": "Orca"}},
		{`{"code":"engine_missing","error":"x","reason":"none"}`, "engine_none", nil},
		{`{"code":"not_delivered","error":"x","delivered":"none","reason":"screen_unreadable"}`, "not_delivered_screen_unreadable", nil},
	} {
		var replayed map[string]any
		if err := json.Unmarshal(LocalizeErrorJSON(i18n.Ja, []byte(tc.data)), &replayed); err != nil {
			t.Fatal(err)
		}
		if want := i18n.T(i18n.Ja, "rpc-errors."+tc.key, tc.args); replayed["error"] != want {
			t.Errorf("%s: %v, want %q", tc.data, replayed["error"], want)
		}
	}
}
