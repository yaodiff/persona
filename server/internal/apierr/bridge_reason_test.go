package apierr

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The Orca chat bridge's refusals keep their generic codes (runtime_away, refused, unknown, session_gone) and are worded
// by their reason in the request's language, Orca's own words in a refusal's summary and kept as its detail.
func TestOrcaBridgeRefusalsAreWordedByReason(t *testing.T) {
	for _, tc := range []struct{ code, reason, detail string }{
		{"runtime_away", "orca_away", ""},
		{"refused", "orca_refused", "The chat is busy."},
		{"unknown", "orca_unknown", ""},
		{"session_gone", "orca_session_gone", ""},
		{"unsupported", "orca_closed", ""},
		{"unsupported", "orca_mode_unsupported", "acceptEdits"},
		{"unsupported", "orca_rename_unsupported", ""},
		{"unsupported", "orca_start_unsupported", ""},
		{"unsupported", "orca_start_folder", ""},
		{"unsupported", "orca_start_launcher", "orca-unknown"},
	} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			data, _ := json.Marshal(map[string]any{"reason": tc.reason, "detail": tc.detail})
			err := &rpc.Error{Code: tc.code, Message: "node words", Data: data}
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, err)
			want := i18n.T(lang, "rpc-errors."+tc.reason, i18n.Args{"detail": tc.detail})
			if body["error"] != want || body["code"] != tc.code || want == "" || strings.Contains(want, "rpc-errors.") {
				t.Fatalf("%s %s: %v, want %q", tc.reason, lang, body, want)
			}
			if tc.detail != "" && (body["detail"] != tc.detail || !strings.Contains(want, tc.detail)) {
				t.Fatalf("%s %s: detail %v", tc.reason, lang, body)
			}
		}
	}
}

// A chat Orca can't open for its provider on that computer (#17684) is worded by why, naming the provider (data
// program) in the request's language; Orca's own words are the detail beside it.
func TestOrcaChatUnavailableIsWordedWithTheProvider(t *testing.T) {
	const orcaWords = "Codex can't open as a chat here because its chat runtime isn't installed on this host (install it from a new chat tab in Orca). Start it in a terminal instead."
	for _, reason := range []string{"orca_chat_runtime_missing", "orca_chat_unavailable"} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			data, _ := json.Marshal(map[string]any{"reason": reason, "program": "Codex", "detail": orcaWords, "delivered": "none"})
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: "unsupported", Message: "node words", Data: data})
			want := i18n.T(lang, "rpc-errors."+reason, i18n.Args{"program": "Codex"})
			if body["error"] != want || body["code"] != "unsupported" || !strings.Contains(want, "Codex") ||
				strings.Contains(want, "{") || strings.Contains(want, "rpc-errors.") {
				t.Fatalf("%s %s: %v, want %q", reason, lang, body, want)
			}
			if body["detail"] != orcaWords {
				t.Fatalf("%s %s: detail %v", reason, lang, body["detail"])
			}
		}
	}
}
