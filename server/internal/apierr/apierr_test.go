package apierr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// langWriter is a response that knows its request's language, as the hub's request logger's does (i18n.Carrier).
type langWriter struct {
	*httptest.ResponseRecorder
	lang i18n.Lang
}

func (w langWriter) Lang() i18n.Lang { return w.lang }

// A session or file the node says is not there is 404 not_found, never the 502 internal of a failed call: a request
// for a session id its machine has no transcript for (#9551) is the client's, and nothing is retried. The summary is
// in the request's language, the node's words the detail; with no language they stay the error.
func TestCallNotFoundIs404(t *testing.T) {
	const words = "transcript for session 11111111-2222-3333-4444-555555555555 not found"
	rec := httptest.NewRecorder()
	body := call(t, rec, rec, &rpc.Error{Code: proto.CodeNotFound, Message: words})
	if rec.Code != http.StatusNotFound || body["code"] != "not_found" || body["retryable"] != false || body["error"] != words {
		t.Errorf("no language: %d %v", rec.Code, body)
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, lang}, rec, fmt.Errorf("session.items: %w", &rpc.Error{Code: proto.CodeNotFound, Message: words}))
		if rec.Code != http.StatusNotFound || body["code"] != "not_found" || body["retryable"] != false ||
			body["error"] != i18n.T(lang, "http-status.not_found") || body["detail"] != words {
			t.Errorf("%s: %d %v", lang, rec.Code, body)
		}
	}
	// An uncoded failure is still the hub's 502.
	rec = httptest.NewRecorder()
	if body := call(t, rec, rec, errors.New("boom")); rec.Code != http.StatusBadGateway || body["code"] != "internal" {
		t.Errorf("plain error: %d %v", rec.Code, body)
	}
}

// A session's history the node can't read says why (data.reason, #11388): reason stays in the body, error is the
// sentence for it in the request's language and detail the node's words; with no language the node's words stay the
// error. A not_found with no or an unknown reason (an older node, a runtime's own answer, a missing file) keeps the
// plain summary. The sentences are for everyday users: they never say the history was deleted (a configuration
// directory the node missed looks the same) nor name the node or its transcript.
func TestCallNotFoundReason(t *testing.T) {
	const words = "transcript for session 11111111-2222-3333-4444-555555555555 not found"
	for reason, key := range map[string]string{
		proto.NotFoundNoTranscript:   "apierr-apierr.session_no_transcript",
		proto.NotFoundUnknownSession: "apierr-apierr.session_unknown",
		"":                           "http-status.not_found",
		"gone_elsewhere":             "http-status.not_found",
	} {
		var data json.RawMessage
		if reason != "" {
			data, _ = json.Marshal(map[string]string{"reason": reason})
		}
		refusal := func() error {
			return fmt.Errorf("session.items: %w", &rpc.Error{Code: proto.CodeNotFound, Message: words, Data: data})
		}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			want := i18n.T(lang, key)
			if want == "" || want == key {
				t.Fatalf("%s: no %s sentence for %s", lang, key, reason)
			}
			if key != "http-status.not_found" {
				if want == i18n.T(lang, "http-status.not_found") {
					t.Errorf("%s %s: the plain summary", lang, reason)
				}
				if l := strings.ToLower(want); strings.Contains(l, "delet") || strings.ContainsAny(want, "删削") ||
					strings.Contains(l, "node") || strings.Contains(l, "transcript") || strings.Contains(want, "ノード") || strings.Contains(want, "节点") {
					t.Errorf("%s %s: %q", lang, reason, want)
				}
			}
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, refusal())
			if rec.Code != http.StatusNotFound || body["code"] != "not_found" || body["retryable"] != false || body["error"] != want ||
				body["detail"] != words || reason != "" && body["reason"] != reason {
				t.Errorf("%s %q: %d %v", lang, reason, rec.Code, body)
			}
		}
		rec := httptest.NewRecorder()
		if body := call(t, rec, rec, refusal()); body["error"] != words || reason != "" && body["reason"] != reason {
			t.Errorf("no language %q: %v", reason, body)
		}
	}
}

// The hub localizes start refusals with no parameters for both a session's start_error and the HTTP response.
func TestPlainStartRefusalLocalization(t *testing.T) {
	for _, code := range []string{"readonly_unsupported", "directory_untypable", "omp_options", proto.CodeAgyCompanionOffline} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			want := i18n.T(lang, "rpc-errors."+code)
			if got := Sentence(lang, code, nil); got != want || got == "" || got == "rpc-errors."+code {
				t.Fatalf("%s %s task refusal = %q, want %q", lang, code, got, want)
			}
			body := LocalizedBody(http.StatusConflict, code, errors.New("node diagnostic"), lang)
			if body["error"] != want {
				t.Fatalf("%s %s HTTP refusal = %v, want %q", lang, code, body, want)
			}
		}
	}
}

// A Stop that found the turn already over (#10828) is said in the request's language, not the node's.
func TestTurnOverBackgroundLocalization(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		want := i18n.T(lang, "rpc-errors."+proto.CodeTurnOverBackground)
		err := &rpc.Error{Code: proto.CodeTurnOverBackground, Message: i18n.T(i18n.Default, "rpc-errors."+proto.CodeTurnOverBackground), Data: json.RawMessage(`{"delivered":"none"}`)}
		body := LocalizedBody(http.StatusConflict, proto.CodeTurnOverBackground, err, lang)
		if body["error"] != want || body["code"] != proto.CodeTurnOverBackground {
			t.Fatalf("%s: %v, want %q", lang, body, want)
		}
	}
}

func call(t *testing.T, w http.ResponseWriter, rec *httptest.ResponseRecorder, err error) map[string]any {
	t.Helper()
	Call(w, err)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// The refusals that say why the main conversation cannot start are 409 with a sentence for people in the request's
// language, the caller's words as the detail and what to do (docs/assistant/onboarding.md 5.2), wrapped or not.
func TestCallExplainsCodedRefusals(t *testing.T) {
	raw := "claude 没有启动起来（0s 后退出），消息没有送达：zsh:1: command not found: claude"
	refusal := &rpc.Error{Code: proto.CodeClaudeMissing, Message: raw, Data: json.RawMessage(`{"delivered":"none","launcher":"claude"}`)}
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		err  error
		want string
	}{
		{"no language", "", refusal, "这台电脑上还没装 Claude Code。"},
		{"English", i18n.En, refusal, "Claude Code isn't installed on this computer yet."},
		{"named", "", OnMachine(fmt.Errorf("send: %w", refusal), "小林的 MacBook"), "小林的 MacBook 上还没装 Claude Code。"},
		{"Japanese", i18n.Ja, OnMachine(refusal, "小林的 MacBook"), "小林的 MacBook にはまだ Claude Code がインストールされていません。"},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, tc.err)
		if rec.Code != http.StatusConflict || body["code"] != proto.CodeClaudeMissing || body["error"] != tc.want ||
			body["detail"] != raw || body["action"] != "install_claude" || body["delivered"] != "none" {
			t.Errorf("%s: %d %v", tc.name, rec.Code, body)
		}
	}

	rec := httptest.NewRecorder()
	body := call(t, rec, rec, &rpc.Error{Code: proto.CodeLauncherMissing, Message: "cc_gone 没有启动起来",
		Data: json.RawMessage(`{"launcher":"cc_gone","machine_name":"mbp","action":"edit_provider"}`)})
	if body["error"] != "在 mbp 上找不到「cc_gone」这个模型入口，可能被删了。" || body["action"] != "edit_provider" {
		t.Errorf("launcher_missing: %v", body)
	}

	rec = httptest.NewRecorder()
	body = call(t, rec, rec, &rpc.Error{Code: proto.CodeNoMachine, Message: "assistant machine is not configured"})
	if rec.Code != http.StatusConflict || body["error"] != "Persona 要在你的一台电脑上干活，现在还没接上电脑。" || body["action"] != "add_computer" {
		t.Errorf("no_machine: %d %v", rec.Code, body)
	}
}

func TestCallExplainsPrivateProfileSignIn(t *testing.T) {
	const raw = `the selected profile is not signed in; run CLAUDE_CONFIG_DIR='/example/profile' claude`
	refusal := &rpc.Error{Code: proto.TurnErrorNotLoggedIn, Message: raw,
		Data: json.RawMessage(`{"delivered":"none","retryable":false,"launcher":"claude"}`)}
	for _, tc := range []struct {
		lang i18n.Lang
		want string
	}{
		{"", "这个对话的 Claude Code 配置还没有登录。请登录该配置，或者改用 API Key。"},
		{i18n.En, "This conversation's Claude Code profile isn't signed in. Sign in to that profile, or use an API key."},
		{i18n.Ja, "この会話の Claude Code プロファイルにログインしていません。ログインするか、API キーを使ってください。"},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, OnMachine(refusal, "server"))
		if rec.Code != http.StatusConflict || body["code"] != proto.TurnErrorNotLoggedIn || body["error"] != tc.want ||
			body["detail"] != raw || body["action"] != "connect_model" || body["delivered"] != "none" || body["retryable"] != false {
			t.Errorf("%s: %d %v", tc.lang, rec.Code, body)
		}
	}
}

// A start whose new tab would not take its command (node/typedstart.go, #2022) is 503 and retryable, with nothing
// delivered, not the generic 502.
func TestCallTerminalNotReadyIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	body := call(t, rec, rec, &rpc.Error{Code: "terminal_not_ready", Message: "pane_send_failed: example",
		Data: json.RawMessage(`{"delivered":"none","retryable":true}`)})
	if rec.Code != http.StatusServiceUnavailable || body["code"] != "terminal_not_ready" || body["retryable"] != true || body["delivered"] != "none" {
		t.Errorf("%d %v", rec.Code, body)
	}
}

// A start whose new tab's shell could not run the launcher (#1642, node/shellmiss.go) is said as the terminal's
// problem, naming the shells and the rc file, in each language; a path the shell could not run and a missing claude
// have words of their own. The shell's line stays the detail, the remedy the launcher's.
func TestCallExplainsTerminalRefusals(t *testing.T) {
	data := func(code, kind, missing string) *rpc.Error {
		b, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": "cc", "missing": missing, "kind": kind, "where": "terminal",
			"pane_shell": "bash", "launcher_shell": "zsh", "rc_file": "~/.zshrc", "line": "bash: " + missing + ": command not found"})
		return &rpc.Error{Code: code, Message: "the new tab's shell (bash) has no command " + missing, Data: b}
	}
	for _, tc := range []struct {
		name   string
		lang   i18n.Lang
		err    error
		action string
		want   []string
	}{
		{"default", "", OnMachine(data(proto.CodeLauncherMissing, "not_found", "cc"), "mbp"), ActionSwitchLauncher,
			[]string{"mbp 上新开的终端标签页（bash）里没有「cc」", "「cc」", "zsh（~/.zshrc）"}},
		{"English, here", i18n.En, data(proto.CodeLauncherMissing, "not_found", "cc"), ActionSwitchLauncher,
			[]string{"new terminal tabs on this computer (bash) has no command “cc”", "“cc”", "zsh (~/.zshrc)"}},
		{"Japanese", i18n.Ja, OnMachine(data(proto.CodeLauncherMissing, "not_found", "cc"), "mbp"), ActionSwitchLauncher,
			[]string{"mbp の新しいターミナルタブ（bash）には「cc」", "「cc」", "zsh（~/.zshrc）"}},
		{"a path", i18n.En, OnMachine(data(proto.CodeLauncherMissing, "no_such_file", "/opt/p/personad"), "mbp"), ActionSwitchLauncher,
			[]string{"new terminal tabs on mbp (bash) can't run “/opt/p/personad”", "“cc”"}},
		{"claude", i18n.En, OnMachine(data(proto.CodeClaudeMissing, "not_found", "claude"), "mbp"), "install_claude",
			[]string{"new terminal tabs on mbp (bash) can't find Claude Code (“claude”)", "“cc”"}},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, tc.err)
		text, _ := body["error"].(string)
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", tc.name, text, w)
			}
		}
		if rec.Code != http.StatusConflict || body["action"] != tc.action || body["where"] != "terminal" || body["pane_shell"] != "bash" ||
			!strings.HasPrefix(body["detail"].(string), "the new tab's shell") || strings.Contains(text, "{") {
			t.Errorf("%s: %d %v", tc.name, rec.Code, body)
		}
	}
	if got := Sentence(i18n.En, "input_draft", nil); got != "" {
		t.Errorf("a refusal the hub does not word: %q", got)
	}
	// A launcher the terminal could not run also has a short line for a row (#10365), in each language; Claude Code
	// missing, or a launcher missing from the computer's list, has none, and keeps its own action.
	for _, tc := range []struct {
		lang i18n.Lang
		err  error
		want string
	}{
		{i18n.En, OnMachine(data(proto.CodeLauncherMissing, "no_such_file", "/opt/p/personad"), "mbp"), "“cc” can’t start in the terminal on mbp"},
		{i18n.ZhHans, data(proto.CodeLauncherMissing, "not_found", "cc"), "「cc」在这台电脑的终端里开不起来"},
		{i18n.Ja, OnMachine(data(proto.CodeLauncherMissing, "not_found", "cc"), "mbp"), "「cc」は mbp のターミナルで開始できません"},
		{i18n.En, OnMachine(data(proto.CodeClaudeMissing, "not_found", "claude"), "mbp"), ""},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, tc.err)
		if got, _ := body["summary"].(string); got != tc.want {
			t.Errorf("%s %v: summary %q, want %q", tc.lang, tc.err, got, tc.want)
		}
	}
	if got := Action(proto.CodeLauncherMissing, map[string]any{"where": "list"}); got != "connect_model" {
		t.Errorf("a launcher the computer's list lacks: %q", got)
	}
	// A tab whose shell the node could not tell (herdr on Windows names none): the default shell, in the language.
	for l, want := range map[i18n.Lang]string{i18n.En: "(the default shell)", i18n.ZhHans: "（默认的 shell）", i18n.Ja: "（既定のシェル）"} {
		for _, code := range []string{proto.CodeLauncherMissing, proto.CodeClaudeMissing} {
			got := Sentence(l, code, map[string]any{"launcher": "cc", "missing": "cc", "kind": "not_found", "where": "terminal",
				"launcher_shell": "zsh", "rc_file": "~/.zshrc"})
			if !strings.Contains(got, want) || strings.Contains(got, "{") {
				t.Errorf("%s %s, no pane_shell: %q", l, code, got)
			}
		}
	}
}

// Other coded refusals keep the words the node wrote; OnMachine leaves them and plain errors alone.
func TestCallKeepsOtherRefusals(t *testing.T) {
	rec := httptest.NewRecorder()
	refusal := &rpc.Error{Code: "input_draft", Message: "请先处理示例终端状态。", Data: json.RawMessage(`{"delivered":"none"}`)}
	body := call(t, rec, rec, OnMachine(refusal, "mbp"))
	if rec.Code != http.StatusConflict || body["error"] != "请先处理示例终端状态。" || body["detail"] != nil || body["action"] != nil || body["machine_name"] != nil {
		t.Errorf("input_draft: %d %v", rec.Code, body)
	}
	plain := errors.New("boom")
	if OnMachine(plain, "mbp") != plain {
		t.Error("a plain error is changed")
	}
}

func TestCallExplainsAssistantControlFailure(t *testing.T) {
	const reason = "CLAUDE_CODE_EFFORT_LEVEL overrides the session effort"
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		want string
	}{
		{"default", "", "Claude 未能应用这项设置：" + reason},
		{"English", i18n.En, "Claude could not apply this setting: " + reason},
		{"Chinese", i18n.ZhHans, "Claude 未能应用这项设置：" + reason},
		{"Japanese", i18n.Ja, "Claudeはこの設定を適用できませんでした：" + reason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			refusal := &rpc.Error{Code: "assistant_control_failed", Message: reason,
				Data: json.RawMessage(`{"delivered":"none","retryable":false}`)}
			body := call(t, langWriter{rec, tc.lang}, rec, fmt.Errorf("session modes: %w", refusal))
			if rec.Code != http.StatusConflict || body["code"] != "assistant_control_failed" || body["error"] != tc.want ||
				body["detail"] != reason || body["delivered"] != "none" || body["retryable"] != false || body["action"] != nil {
				t.Fatalf("%d %v", rec.Code, body)
			}
		})
	}
}

// Status codes and retryability stay stable; only exact standard status text is translated for an explicit locale.
func TestStandardStatusTextLocalization(t *testing.T) {
	for status, key := range standardStatusKeys {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, ""} {
			rec := httptest.NewRecorder()
			original := errors.New(http.StatusText(status))
			Write(langWriter{rec, lang}, status, original)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := original.Error()
			if lang != "" {
				want = i18n.T(lang, key)
			}
			if body["error"] != want || body["code"] != Body(status, "", original)["code"] || rec.Code != status {
				t.Fatalf("%d %s: %v, want %q", status, lang, body, want)
			}
		}
	}
	rec := httptest.NewRecorder()
	Write(langWriter{rec, i18n.Ja}, 404, errors.New("custom diagnostic"))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "custom diagnostic" {
		t.Fatalf("custom text changed: %v", body)
	}
}

func TestRPCFailureLocalePreservesOutcomeAndDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		err  error
		key  string
	}{
		{"offline", ErrOffline, "rpc-errors.offline"},
		{"closed", fmt.Errorf("read: %w", rpc.ErrClosed), "rpc-errors.unreachable"},
		{"timeout", fmt.Errorf("read: %w", context.DeadlineExceeded), "rpc-errors.timeout"},
		{"internal", errors.New("example raw diagnostic"), "rpc-errors.failed"},
		{"draft", &rpc.Error{Code: "input_draft", Message: "original terminal instructions", Data: json.RawMessage(`{"delivered":"none","retryable":false,"draft":"example user draft"}`)}, "rpc-errors.input_draft"},
		{"unknown delivery", &rpc.Error{Code: "timeout", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"unknown","retryable":false}`)}, "rpc-errors.timeout"},
		{"partial delivery", &rpc.Error{Code: "internal", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"yes","retryable":false}`)}, "rpc-errors.failed"},
		{"busy", &rpc.Error{Code: "attach_busy", Message: "original diagnostic"}, "rpc-errors.attach_busy"},
		{"terminal not ready", &rpc.Error{Code: "terminal_not_ready", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"none","retryable":true}`)}, "rpc-errors.terminal_not_ready"},
		{"old node", &rpc.Error{Code: "node_outdated", Message: "original diagnostic"}, "rpc-errors.node_outdated"},
		// a Claude session outside Persona's terminals: refused before anything reached it, or handed over unconfirmed
		{"peer refused", &rpc.Error{Code: "peer_unsupported", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none","pid":42}`)}, "rpc-errors.peer_unsupported"},
		{"peer gone", &rpc.Error{Code: "peer_unreachable", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_unreachable"},
		{"peer long", &rpc.Error{Code: "peer_too_long", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_too_long"},
		{"peer command", &rpc.Error{Code: "peer_command", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_command"},
		{"peer unconfirmed", &rpc.Error{Code: "peer_unconfirmed", Message: "node's own words", Data: json.RawMessage(`{"delivered":"unknown"}`)}, "rpc-errors.peer_unconfirmed"},
		{"peer held", &rpc.Error{Code: "peer_held", Message: "node's own words", Data: json.RawMessage(`{"delivered":"unknown"}`)}, "rpc-errors.peer_held"},
		// a mode change the node cannot make (#1843): change it on the computer, or resume the session first
		{"modes outside", &rpc.Error{Code: proto.CodeModesOnComputer, Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.modes_on_computer"},
		{"modes not running", &rpc.Error{Code: proto.CodeModesNotRunning, Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.modes_not_running"},
		// queued messages sent now: not a Claude, or a send-now hint the node does not know (#2467)
		{"send now unsupported", &rpc.Error{Code: "send_now_unsupported", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.send_now_unsupported"},
		{"send now unrecognized", &rpc.Error{Code: "send_now_unrecognized", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.send_now_unrecognized"},
		{"untypable directory", &rpc.Error{Code: "directory_untypable", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.directory_untypable"},
		// a send to an omp the node cannot resume (node/control.go, node/ompcalls.go)
		{"omp not resumable", &rpc.Error{Code: "omp_resume_unknown", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.omp_resume_unknown"},
		{"omp stopped", &rpc.Error{Code: "omp_stopped", Message: "node's own words"}, "rpc-errors.omp_stopped"},
		// a read of an agent its engine let go, which only an open chat starts (Paseo, #8132)
		{"not loaded", &rpc.Error{Code: proto.CodeNotLoaded, Message: "the runtime's own words"}, "rpc-errors.not_loaded"},
		// an owner's message to an Agent with older ones held for it (devices held.go, #10661)
		{"held ahead", &rpc.Error{Code: "held_ahead", Message: "the manager's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.held_ahead"},
		{"held not kept", &rpc.Error{Code: "held_not_kept", Message: "the manager's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.held_not_kept"},
		{"held full", &rpc.Error{Code: "held_full", Message: "the manager's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.held_full"},
		{"held start full", &rpc.Error{Code: "held_start_full", Message: "the manager's own words"}, "rpc-errors.held_start_full"},
	}
	for _, tc := range cases {
		legacy := httptest.NewRecorder()
		before := call(t, legacy, legacy, tc.err)
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			after := call(t, langWriter{rec, lang}, rec, tc.err)
			if rec.Code != legacy.Code || after["error"] != i18n.T(lang, tc.key) {
				t.Fatalf("%s %s: %d %v", tc.name, lang, rec.Code, after)
			}
			for _, key := range []string{"code", "retryable", "delivered", "draft", "clearable"} {
				if after[key] != before[key] {
					t.Fatalf("%s %s changed %s: before=%v after=%v", tc.name, lang, key, before, after)
				}
			}
			if after["detail"] != before["error"] {
				t.Fatalf("diagnostic lost: before=%v after=%v", before, after)
			}
		}
	}
	raw := &rpc.Error{Code: "external_custom", Message: "external author text"}
	rec := httptest.NewRecorder()
	if body := call(t, langWriter{rec, i18n.Ja}, rec, raw); body["error"] != raw.Message {
		t.Fatal("external custom message guessed")
	}
	typed := i18n.ErrEnglish("inbox-errors.no_item")
	rec = httptest.NewRecorder()
	if body := call(t, langWriter{rec, i18n.Ja}, rec, typed); body["error"] != i18n.T(i18n.Ja, "inbox-errors.no_item") {
		t.Fatal("typed summary overwritten")
	}
}

// The node's open_elsewhere refusal (#6803) is said in the request's language by where the session is open, with the
// node's words as the detail and its fields kept; one without a known where (an older node, an Agent runtime's own
// refusal) keeps its words.
func TestOpenElsewhereSpeaksTheClientsLanguage(t *testing.T) {
	const words = "node's own words"
	for _, where := range []string{"desktop", "terminal", "program", "other", "agents_list"} {
		err := &rpc.Error{Code: "open_elsewhere", Message: words, Data: json.RawMessage(`{"delivered":"none","pid":42,"where":"` + where + `"}`)}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, err)
			want := i18n.T(lang, "rpc-errors.open_elsewhere_"+where)
			if want == "rpc-errors.open_elsewhere_"+where || body["error"] != want {
				t.Fatalf("%s %s: %v", where, lang, body)
			}
			if rec.Code != http.StatusConflict || body["code"] != "open_elsewhere" || body["delivered"] != "none" || body["where"] != where || body["detail"] != words {
				t.Fatalf("%s %s: %d %v", where, lang, rec.Code, body)
			}
			if lang == i18n.ZhHans && strings.Contains(want, "手机") {
				t.Fatalf("%s names the phone: %s", where, want)
			}
		}
	}
	for _, data := range []string{`{"delivered":"none","pid":42}`, `{"delivered":"none","where":"elsewhere"}`} {
		err := &rpc.Error{Code: "open_elsewhere", Message: words, Data: json.RawMessage(data)}
		rec := httptest.NewRecorder()
		if body := call(t, langWriter{rec, i18n.En}, rec, err); body["error"] != words {
			t.Fatalf("%s: %v", data, body)
		}
	}
}

// The node's refusals of a resume or a message it cannot carry out on its engine (#17044) are said in the request's
// language: orca_resume, work_agent_resume and agent_not_in_front by their code; not_running only when it names the
// engine (data reason and engine). A not_running with other words (a runtime's own, an older node's) keeps them.
func TestResumeRefusalsSpeakTheClientsLanguage(t *testing.T) {
	const words = "node's own words"
	for _, code := range []string{"orca_resume", "work_agent_resume", "agent_not_in_front"} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: code, Message: words, Data: json.RawMessage(`{"delivered":"none"}`)})
			want := i18n.T(lang, "rpc-errors."+code)
			if want == "rpc-errors."+code || body["error"] != want || body["code"] != code || body["delivered"] != "none" || body["detail"] != words {
				t.Fatalf("%s %s: %v", code, lang, body)
			}
		}
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		err := &rpc.Error{Code: "not_running", Message: words,
			Data: json.RawMessage(`{"delivered":"none","reason":"` + proto.NotRunningEngine + `","engine":"Orca"}`)}
		body := call(t, langWriter{rec, lang}, rec, err)
		want := i18n.T(lang, "rpc-errors.not_running_engine", i18n.Args{"engine": "Orca"})
		if !strings.Contains(want, "Orca") || body["error"] != want || body["engine"] != "Orca" || body["detail"] != words {
			t.Fatalf("not_running %s: %v", lang, body)
		}
	}
	for _, data := range []string{`{"delivered":"none"}`, `{"reason":"` + proto.NotRunningEngine + `"}`, `{"reason":"other","engine":"Orca"}`} {
		rec := httptest.NewRecorder()
		if body := call(t, langWriter{rec, i18n.En}, rec, &rpc.Error{Code: "not_running", Message: words, Data: json.RawMessage(data)}); body["error"] != words {
			t.Fatalf("not_running %s: %v", data, body)
		}
	}
}

func TestCodexComposerTimeoutUsesNodeWait(t *testing.T) {
	for _, code := range []string{"codex_no_box", "codex_no_box_here"} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			err := &rpc.Error{Code: code, Message: "node diagnostic", Data: json.RawMessage(`{"seconds":181.5}`)}
			body := call(t, langWriter{rec, lang}, rec, err)
			summary, _ := body["error"].(string)
			if !strings.Contains(summary, "181.5") || body["seconds"] != 181.5 || body["code"] != code {
				t.Fatalf("%s %s: %v", code, lang, body)
			}
		}
		rec := httptest.NewRecorder()
		old := call(t, langWriter{rec, i18n.En}, rec, &rpc.Error{Code: code, Message: "old node diagnostic"})
		if summary, _ := old["error"].(string); !strings.Contains(summary, "a minute") {
			t.Fatalf("%s without a duration: %v", code, old)
		}
	}
}

func TestCodexWriterConflictSpeaksTheClientsLanguage(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		raw := &rpc.Error{Code: "codex_writer_busy", Message: "node recovery words", Data: json.RawMessage(`{"delivered":"none","detail":"codex: thread 11111111-2222-3333-4444-555555555555 already has an active writer (-32600)"}`)}
		body := call(t, langWriter{rec, lang}, rec, raw)
		if rec.Code != http.StatusConflict || body["code"] != "codex_writer_busy" || body["error"] != i18n.T(lang, "rpc-errors.codex_writer_busy") {
			t.Fatalf("%s: %d %v", lang, rec.Code, body)
		}
		if body["delivered"] != "none" || body["detail"] != "codex: thread 11111111-2222-3333-4444-555555555555 already has an active writer (-32600)" {
			t.Fatalf("%s lost delivery or diagnostic evidence: %v", lang, body)
		}
	}
}

func TestAssistantActionableRefusalsWithholdNativeDiagnostics(t *testing.T) {
	for _, code := range []string{"assistant_launcher_profile_switch", "assistant_service_tier_start_only", "assistant_service_tier_unsupported", "assistant_service_tier_refused"} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			raw := &rpc.Error{Code: code, Message: "native English diagnostic", Data: json.RawMessage(`{"delivered":"none","retryable":false}`)}
			body := call(t, langWriter{rec, lang}, rec, raw)
			if rec.Code != http.StatusConflict || body["code"] != code || body["error"] != i18n.T(lang, "rpc-errors."+code) {
				t.Fatalf("%s %s: %d %v", code, lang, rec.Code, body)
			}
			if _, exists := body["detail"]; exists {
				t.Fatalf("%s %s exposed the native diagnostic: %v", code, lang, body)
			}
			if body["delivered"] != "none" || body["retryable"] != false {
				t.Fatalf("%s %s changed delivery: %v", code, lang, body)
			}
		}
	}
}

// The node's Git pull / push / fetch refusals (#1888) are 409s in the request's language, the node's words (git's
// included) as the detail; a failure git itself explains carries git's words in the summary too.
func TestGitOpRefusalsSpeakTheClientsLanguage(t *testing.T) {
	for _, code := range []string{"no_directory", "not_a_repository", "detached_head", "no_upstream", "uncommitted_changes", "not_fast_forward", "push_rejected"} {
		err := &rpc.Error{Code: code, Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, err)
			if rec.Code != http.StatusConflict || body["code"] != code || body["error"] != i18n.T(lang, "rpc-errors."+code) || body["detail"] != "node's own words" {
				t.Fatalf("%s %s: %d %v", code, lang, rec.Code, body)
			}
			if strings.Contains(body["error"].(string), "{") || strings.HasPrefix(body["error"].(string), "rpc-errors.") {
				t.Fatalf("%s %s: unfilled %q", code, lang, body["error"])
			}
		}
	}
	words := "git push: fatal: unable to access 'https://git.example.com/app.git/': Could not resolve host (nothing was forced)"
	for _, code := range []string{"pull_failed", "push_failed", "fetch_failed"} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: code, Message: words})
			if rec.Code != http.StatusConflict || body["error"] != i18n.T(lang, "rpc-errors."+code, i18n.Args{"reason": words}) ||
				!strings.HasSuffix(body["error"].(string), words) || body["detail"] != words {
				t.Fatalf("%s %s: %d %v", code, lang, rec.Code, body)
			}
		}
	}
}

// The apps still tell a timeout by these words in the hub's summary (iOS APIError.isTimeoutText keeps it quiet, Android's
// herdr notice explains it), so the sentence that now says what to do next keeps them (#5031).
func TestTimeoutSummaryKeepsTheWordsTheAppsMatch(t *testing.T) {
	for lang, words := range map[i18n.Lang]string{i18n.En: "timed out", i18n.ZhHans: "超时"} {
		if s := i18n.T(lang, "rpc-errors.timeout"); !strings.Contains(strings.ToLower(s), words) {
			t.Errorf("%s: %q no longer says %q", lang, s, words)
		}
	}
}

func TestInvalidJSONAndInDoubtLocale(t *testing.T) {
	var v struct {
		Count int `json:"count"`
	}
	decodeErr := json.Unmarshal([]byte(`{"count":"bad"}`), &v)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		Write(langWriter{rec, lang}, 400, decodeErr)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != i18n.T(lang, "http-status.bad_request") || body["detail"] != decodeErr.Error() || body["code"] != "bad_request" {
			t.Fatal(body)
		}
		doubt := LocalizedBody(502, "assistant_in_doubt", rpc.ErrDropped, lang)
		if doubt["error"] != i18n.T(lang, "rpc-errors.in_doubt") || doubt["delivered"] != nil {
			t.Fatal("delivery certainty invented", doubt)
		}
	}
	if RPCText(i18n.Ja, "third-party raw text") != "third-party raw text" || RPCText("", rpc.ErrClosed.Error()) != rpc.ErrClosed.Error() {
		t.Fatal("raw/legacy stream text changed")
	}
	if RPCText(i18n.Ja, rpc.ErrClosed.Error()) != i18n.T(i18n.Ja, "rpc-errors.unreachable") {
		t.Fatal("stream error not localized")
	}
}

// Describe keeps the summary and adds the cause it hides; Reason gives only the cause (#5728).
func TestDescribeKeepsTheSummaryAndTheCause(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":"the request did not complete","detail":"connection refused"}`, "the request did not complete：connection refused"},
		{`{"error":"plain"}`, "plain"},
		{`{"error":"same","detail":"same"}`, "same"},
		{`{"error":"summary","detail":{"k":1}}`, "summary"},
		{`{"detail":"only the cause"}`, "only the cause"},
		{`not json`, ""},
	} {
		if got := Describe([]byte(c.body)); got != c.want {
			t.Errorf("Describe(%s) = %q, want %q", c.body, got, c.want)
		}
	}
}

// A start's new tab that stopped on its shell (#2570): the hub words it with the machine (or "this computer") and
// the shell's line, in every language, as a task's start_error and an HTTP refusal.
func TestStartTabRefusalsAreWorded(t *testing.T) {
	for _, code := range []string{proto.CodeShellWaiting, proto.CodeStartLineRejected} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			on := Sentence(lang, code, map[string]any{"machine_name": "Studio", "line": "Update now? [Y/n]"})
			here := Sentence(lang, code, nil)
			if !strings.Contains(on, "Studio") || !strings.Contains(on, "Update now? [Y/n]") || here == "" || strings.Contains(here, "{") {
				t.Errorf("%s %s: %q / %q", code, lang, on, here)
			}
			body := LocalizedBody(http.StatusConflict, code, errors.New("node text"), lang)
			if body["error"] != i18n.T(lang, "rpc-errors."+code) || body["error"] == "" {
				t.Errorf("%s %s HTTP: %v", code, lang, body)
			}
		}
	}
}

// A Codex start that met its sign-in screen (node/codex_start.go): the hub words it with the machine (or "this
// computer") and the fix, in every language, as a task's start_error and an HTTP refusal.
func TestCodexNotLoggedInIsWorded(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		on := Sentence(lang, proto.CodeCodexNotLoggedIn, map[string]any{"machine_name": "Studio"})
		here := Sentence(lang, proto.CodeCodexNotLoggedIn, nil)
		for _, got := range []string{on, here} {
			if !strings.Contains(got, "codex login") || strings.Contains(got, "{") {
				t.Errorf("%s: %q", lang, got)
			}
		}
		if !strings.Contains(on, "Studio") || strings.Contains(here, "Studio") {
			t.Errorf("%s: %q / %q", lang, on, here)
		}
		body := LocalizedBody(http.StatusConflict, proto.CodeCodexNotLoggedIn, errors.New("node text"), lang)
		if body["error"] != i18n.T(lang, "rpc-errors.codex_not_logged_in") || !strings.Contains(body["error"].(string), "codex login") {
			t.Errorf("%s HTTP: %v", lang, body)
		}
	}
}

// The node's refusal as an HTTP answer (CallStatus): nothing was typed, so delivered "none" and not retryable, and the
// hub's sentence names the machine OnMachine adds (else "this computer"), the node's words kept as the detail. There is
// no action: the fix is a sign-in on that computer.
func TestCallWordsCodexNotLoggedIn(t *testing.T) {
	const raw = "Codex 停在登录界面"
	refusal := &rpc.Error{Code: proto.CodeCodexNotLoggedIn, Message: raw}
	for _, lang := range []i18n.Lang{"", i18n.En, i18n.Ja} {
		for _, tc := range []struct {
			err     error
			machine string
		}{{refusal, ""}, {OnMachine(fmt.Errorf("session.start: %w", refusal), "Studio"), "Studio"}} {
			rec := httptest.NewRecorder()
			body := call(t, langWriter{rec, lang}, rec, tc.err)
			want := Sentence(lang, proto.CodeCodexNotLoggedIn, map[string]any{"machine_name": tc.machine})
			if rec.Code != http.StatusConflict || body["code"] != proto.CodeCodexNotLoggedIn || body["error"] != want ||
				body["detail"] != raw || body["delivered"] != "none" || body["retryable"] != false {
				t.Errorf("%q %q: %d %v", lang, tc.machine, rec.Code, body)
			}
			if _, has := body["action"]; has {
				t.Errorf("%q %q: action %v", lang, tc.machine, body["action"])
			}
		}
	}
}

// shell_waiting is a session's start_error, a planned task's start failure and a refused message's 409 on every app
// (#10365): only the web lists the question's answers, and only from a computer that offers them (not an older one,
// nor a start that took a hand-off's values), so its words never say an answer is picked "here" or "in the chat".
func TestShellWaitingWordsHoldOnEveryClient(t *testing.T) {
	promised := map[i18n.Lang][]string{
		i18n.En:     {"Pick an answer here", "Pick an answer in the chat"},
		i18n.ZhHans: {"在这里选一个回答", "在对话里选一个回答"},
		i18n.Ja:     {"ここで回答を選ぶ", "チャットで回答を選ぶ"},
	}
	for lang, words := range promised {
		refused, _ := LocalizedBody(http.StatusConflict, proto.CodeShellWaiting, errors.New("node text"), lang)["error"].(string)
		for _, got := range []string{
			Sentence(lang, proto.CodeShellWaiting, map[string]any{"machine_name": "Studio", "line": "Update now? [Y/n]"}),
			Sentence(lang, proto.CodeShellWaiting, nil),
			refused,
		} {
			for _, w := range words {
				if got == "" || strings.Contains(got, w) {
					t.Errorf("%s: %q", lang, got)
				}
			}
		}
	}
}

// The refusals of an answer to that question from the app (#10365, node/startcontinue.go) read in every language.
func TestStartContinueRefusalsAreWorded(t *testing.T) {
	for _, code := range []string{"start_tab_gone", "start_not_waiting", "choice_not_offered", "prompt_changed", proto.CodeSecretEnvGone} {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			body := LocalizedBody(http.StatusConflict, code, errors.New("node text"), lang)
			if body["error"] != i18n.T(lang, "rpc-errors."+code) || body["error"] == "" || body["error"] == "node text" {
				t.Errorf("%s %s: %v", code, lang, body)
			}
		}
	}
}

func TestSupervisionRefusalsSpeakTheClientsLanguage(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, code := range []string{"assistant_supervision_runtime", "assistant_supervision_account"} {
			rec := httptest.NewRecorder()
			data := json.RawMessage(`{"delivered":"none","launchers":["OpenCode","agy"]}`)
			body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: code, Message: "runtime diagnostic", Data: data})
			want := i18n.T(lang, "rpc-errors."+code, i18n.Args{"launchers": "OpenCode, agy"})
			if rec.Code != http.StatusConflict || body["code"] != code || body["error"] != want || body["delivered"] != "none" {
				t.Fatalf("%s %s: %d %v", lang, code, rec.Code, body)
			}
		}
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: "assistant_supervision_runtime", Message: "legacy refusal"})
		want := i18n.T(lang, "rpc-errors.assistant_supervision_runtime", i18n.Args{"launchers": i18n.T(lang, "rpc-errors.assistant_supervision_none")})
		if body["error"] != want {
			t.Fatalf("legacy %s: %v", lang, body)
		}
	}
}

// notingWriter takes note of the errors written to it (ErrorNoter), as the hub's request logger does; passing hides it
// one Unwrap down, as the hub's other writers do.
type notingWriter struct {
	*httptest.ResponseRecorder
	status int
	body   map[string]any
}

func (w *notingWriter) NoteError(status int, body map[string]any) { w.status, w.body = status, body }

type passing struct{ http.ResponseWriter }

func (w passing) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// What WriteBody writes is noted on the first writer down the chain that takes note; bytes written another way never
// are, and a chain without one only gets the body (#14490).
func TestWriteBodyNotesItsError(t *testing.T) {
	w := &notingWriter{ResponseRecorder: httptest.NewRecorder()}
	Call(passing{w}, errors.New("the node went away"))
	if w.status != http.StatusBadGateway || w.body["code"] != "internal" || !strings.Contains(w.Body.String(), `"code":"internal"`) {
		t.Fatalf("noted %d %v, wrote %d %s", w.status, w.body, w.Code, w.Body.String())
	}
	other := &notingWriter{ResponseRecorder: httptest.NewRecorder()}
	other.WriteHeader(http.StatusBadGateway)
	_, _ = passing{other}.Write([]byte(`{"code":"internal","error":"passed on"}`))
	if other.body != nil {
		t.Fatalf("bytes were noted: %v", other.body)
	}
	plain := httptest.NewRecorder()
	WriteCode(passing{plain}, http.StatusServiceUnavailable, "assistant_offline", errors.New("not connected"))
	if plain.Code != http.StatusServiceUnavailable || !strings.Contains(plain.Body.String(), "assistant_offline") {
		t.Fatalf("without a noter: %d %s", plain.Code, plain.Body.String())
	}
}

func TestUnresolvedModelRefusalSpeaksTheClientsLanguage(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		data := json.RawMessage(`{"delivered":"none","retryable":false,"launcher":"gw","model":"opus"}`)
		body := call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: "assistant_model_unresolved", Message: "native diagnostic", Data: data})
		want := i18n.T(lang, "rpc-errors.assistant_model_unresolved", i18n.Args{"launcher": "gw", "model": "opus"})
		if rec.Code != http.StatusConflict || body["code"] != "assistant_model_unresolved" || body["error"] != want ||
			body["delivered"] != "none" || body["retryable"] != false || body["detail"] != nil {
			t.Fatalf("%s: %d %v", lang, rec.Code, body)
		}
		// Missing metadata keeps the original message rather than leaking unexpanded placeholders.
		rec = httptest.NewRecorder()
		body = call(t, langWriter{rec, lang}, rec, &rpc.Error{Code: "assistant_model_unresolved", Message: "explicit fallback"})
		if body["error"] != "explicit fallback" || strings.Contains(body["error"].(string), "{") {
			t.Fatalf("fallback %s: %v", lang, body)
		}
	}
}
