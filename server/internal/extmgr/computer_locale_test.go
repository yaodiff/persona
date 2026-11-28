package extmgr

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lcuconfirm"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func computerLocaleRequest(t *testing.T, handler http.Handler, method, path, body, token string, lang i18n.Lang) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	r.Header.Set("Authorization", "Bearer "+token)
	if lang != "" {
		r.Header.Set("Accept-Language", string(lang))
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response: %s: %v", w.Body, err)
	}
	return w.Code, out
}

// The extension registry exists but cua/ego are disabled: this is not the missing-manager case.
func TestComputerLocaleDisabledAndValidation(t *testing.T) {
	h, _ := extensionTestHub(t)
	m, _ := computerMac(t, h, "mac")
	mux := h.routes()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, c := range []struct {
			method, path, body, key string
			status                  int
		}{
			{"GET", "/computer", "", "hub-computer.err_off", 404},
			{"PUT", "/computer", `{"enabled":false}`, "hub-computer.err_off", 404},
			{"GET", "/browser", "", "hub-browser.err_off", 404},
			{"PUT", "/browser", `{"enabled":false}`, "hub-browser.err_off", 404},
			{"GET", "/computer/clipboard", "", "hub-computer.err_clipboard_off", 404},
			{"POST", "/computer/clipboard", `{"text":"unchanged"}`, "hub-computer.err_clipboard_off", 404},
			{"POST", "/computer/install", "", "hub-computer.err_not_enabled", 404},
			{"POST", "/browser/install", "", "hub-browser.err_not_enabled", 404},
			{"POST", "/computer/not-an-action", "", "hub-computer.err_unknown_action", 404},
			{"POST", "/browser/not-an-action", "", "hub-computer.err_unknown_action", 404},
		} {
			t.Run(string(lang)+c.method+c.path, func(t *testing.T) {
				status, out := computerLocaleRequest(t, mux, c.method, "/api/v1/machines/mac"+c.path, c.body, h.cfg.Token, lang)
				if status != c.status || out["error"] != i18n.T(lang, c.key) || out["code"] != "not_found" || out["retryable"] != false {
					t.Fatalf("%d %v", status, out)
				}
			})
		}
		for _, path := range []string{"/computer", "/browser"} {
			status, out := computerLocaleRequest(t, mux, "PUT", "/api/v1/machines/mac"+path, "{", h.cfg.Token, lang)
			if status != 400 || out["error"] != i18n.T(lang, "hub-computer.err_bad_body", i18n.Args{"err": "unexpected EOF"}) || out["code"] != "bad_request" {
				t.Fatalf("body: %d %v", status, out)
			}
		}
		// This is the final manager HTTP boundary after the hub has forwarded an authenticated device/extension.
		r := httptest.NewRequest("GET", "/api/v1/machines/mac/computer/clipboard", nil)
		r.Header.Set(extwire.HeaderRole, roleFull)
		r.Header.Set(extwire.HeaderDevice, "dev_example")
		r.Header.Set(extwire.HeaderLang, string(lang))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || !strings.Contains(w.Body.String(), i18n.T(lang, "hub-computer.err_clipboard_off")) {
			t.Fatalf("relay: %d %s", w.Code, w.Body)
		}
	}
	// A client without a language retains the source-language clipboard response and unchanged schema.
	status, out := computerLocaleRequest(t, mux, "GET", "/api/v1/machines/mac/computer/clipboard", "", h.cfg.Token, "")
	if status != 404 || out["error"] != "这台 Mac 没有打开操控电脑或浏览器扩展" {
		t.Fatalf("legacy: %d %v", status, out)
	}
	m.mu.Lock()
	m.online = false
	m.mu.Unlock()
}

func enableComputerLocaleExtensions(t *testing.T, h *Manager) {
	t.Helper()
	for _, id := range []string{cuaExtensionID, egoExtensionID} {
		if err := h.extensions.reg.SetEnabled(context.Background(), id, true, "test"); err != nil {
			t.Fatal(err)
		}
		if err := h.extensions.reg.SetConfig(context.Background(), id, json.RawMessage(`{"machines":["mac"]}`), "test"); err != nil {
			t.Fatal(err)
		}
	}
}

// Exercise the actual RPC serialization, on both legacy and builtin-program node routes.
func computerLocaleNode(t *testing.T, m *Machine, builtin bool, handle func(string) (any, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hub, node := rpc.New("h", h2nW), rpc.New("n", n2hW)
	node.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		method, _ = fakeBuiltinRoute(t, builtin, method, params)
		return handle(method)
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = node.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m.mu.Lock()
	m.peer, m.online = hub, true
	m.info.BuiltinPrograms = fakeBuiltinPrograms(builtin)
	m.mu.Unlock()
}

func TestComputerLocaleRPCAndStatus(t *testing.T) {
	for _, builtin := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-node", true: "builtin-node"}[builtin], func(t *testing.T) {
			h, _ := extensionTestHub(t)
			m, _ := computerMac(t, h, "mac")
			enableComputerLocaleExtensions(t, h)
			computerLocaleNode(t, m, builtin, func(method string) (any, error) {
				switch method {
				case "computer.clipboard":
					return nil, i18n.Err("hub-computer.rpc_clipboard_read")
				case "computer.install":
					return nil, &rpc.Error{Code: "busy", Message: "正在安装，等它做完"}
				case "browser.open":
					return nil, &rpc.Error{Message: "打不开 ego lite：original detail"}
				case "computer.status":
					return proto.Computer{Note: "这台 Mac 上没有装 Cua Driver", Job: &proto.ComputerJob{Kind: "install", State: "failed", Step: "下载安装器", Error: "安装器失败（exit status 1）：original detail"}, Checked: time.Now()}, nil
				case "browser.status":
					return proto.Browser{Note: "ego lite 还没做完首次设置", Job: &proto.ComputerJob{Kind: "install", State: "failed", Step: "下载 ego lite", Error: "签名校验没通过：original detail"}, Checked: time.Now()}, nil
				default:
					return map[string]any{}, nil
				}
			})
			mux := h.routes()
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				status, out := computerLocaleRequest(t, mux, "POST", "/api/v1/machines/mac/computer/clipboard", "{", h.cfg.Token, lang)
				if status != 400 || out["error"] != i18n.T(lang, "hub-computer.err_bad_body", i18n.Args{"err": "unexpected EOF"}) {
					t.Fatalf("clipboard body: %d %v", status, out)
				}
				for _, path := range []string{"/computer", "/browser"} {
					status, out = computerLocaleRequest(t, mux, "PUT", "/api/v1/machines/mac"+path, `{"enabled":false,"extra":"`+strings.Repeat("x", 4<<10)+`"}`, h.cfg.Token, lang)
					if status != 413 || out["code"] != "too_large" || out["error"] != i18n.T(lang, "hub-computer.err_body_large", i18n.Args{"limit": 4 << 10}) {
						t.Fatalf("large body: %d %v", status, out)
					}
				}
				for _, c := range []struct {
					method, path, key string
					args              i18n.Args
				}{
					{"GET", "/computer/clipboard", "hub-computer.rpc_clipboard_read", nil},
					{"POST", "/computer/install", "hub-computer.rpc_busy_install", nil},
					{"POST", "/browser/open", "hub-browser.rpc_open", i18n.Args{"detail": "original detail"}},
				} {
					status, out := computerLocaleRequest(t, mux, c.method, "/api/v1/machines/mac"+c.path, "", h.cfg.Token, lang)
					if status != 409 || out["error"] != i18n.T(lang, c.key, c.args) || out["code"] != "conflict" {
						t.Fatalf("RPC %s: %d %v", lang, status, out)
					}
				}
				for _, c := range []struct {
					path, note, step, err string
					args                  i18n.Args
				}{
					{"/computer", "hub-computer.rpc_missing_cua", "hub-computer.rpc_download_installer", "hub-computer.rpc_installer_failed", i18n.Args{"reason": "exit status 1", "detail": "original detail"}},
					{"/browser", "hub-browser.rpc_setup", "hub-browser.rpc_download_step", "hub-browser.rpc_signature", i18n.Args{"detail": "original detail"}},
				} {
					status, out := computerLocaleRequest(t, mux, "GET", "/api/v1/machines/mac"+c.path, "", h.cfg.Token, lang)
					job, _ := out["job"].(map[string]any)
					if status != 200 || out["note"] != i18n.T(lang, c.note) || job["step"] != i18n.T(lang, c.step) || job["error"] != i18n.T(lang, c.err, c.args) || job["state"] != "failed" {
						t.Fatalf("status %s: %d %v", lang, status, out)
					}
				}
			}
			m.mu.Lock()
			canonical := m.computer.Note
			step := m.computer.Job.Step
			m.mu.Unlock()
			if canonical != "这台 Mac 上没有装 Cua Driver" || step != "下载安装器" {
				t.Fatal("shared state translated")
			}
		})
	}
}

func TestBrowserLocaleUnsupportedOS(t *testing.T) {
	const note = "当前 macOS 12.7.4 低于 ego lite 要求的 13.0，无法运行"
	for _, builtin := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy-node", true: "builtin-node"}[builtin], func(t *testing.T) {
			h, _ := extensionTestHub(t)
			m, _ := computerMac(t, h, "mac")
			enableComputerLocaleExtensions(t, h)
			computerLocaleNode(t, m, builtin, func(method string) (any, error) {
				switch method {
				case "browser.open":
					return nil, &rpc.Error{Message: note}
				case "browser.status":
					return proto.Browser{Note: note, Job: &proto.ComputerJob{Kind: "install", State: "failed", Error: note}, Checked: time.Now()}, nil
				default:
					return map[string]any{}, nil
				}
			})
			mux := h.routes()
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				want := i18n.T(lang, "hub-browser.rpc_unsupported_os", i18n.Args{"current": "12.7.4", "required": "13.0"})
				status, out := computerLocaleRequest(t, mux, "POST", "/api/v1/machines/mac/browser/open", "", h.cfg.Token, lang)
				if status != 409 || out["error"] != want {
					t.Errorf("open %s: %d %v, want %q", lang, status, out, want)
				}
				status, out = computerLocaleRequest(t, mux, "GET", "/api/v1/machines/mac/browser", "", h.cfg.Token, lang)
				job, _ := out["job"].(map[string]any)
				if status != 200 || out["note"] != want || job["error"] != want {
					t.Errorf("status %s: %d %v, want %q", lang, status, out, want)
				}
			}
		})
	}
}

func TestComputerLocaleOfflineOutdatedAndDatabase(t *testing.T) {
	h, _ := extensionTestHub(t)
	m, _ := computerMac(t, h, "mac")
	enableComputerLocaleExtensions(t, h)
	mux := h.routes()
	m.mu.Lock()
	m.online = false
	m.mu.Unlock()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, path := range []string{"/computer/clipboard", "/computer/install", "/browser/open"} {
			method := "POST"
			if path == "/computer/clipboard" {
				method = "GET"
			}
			status, out := computerLocaleRequest(t, mux, method, "/api/v1/machines/mac"+path, "", h.cfg.Token, lang)
			if status != 409 || out["error"] != i18n.T(lang, "hub-computer.err_offline") {
				t.Fatalf("offline: %d %v", status, out)
			}
		}
	}
	computerLocaleNode(t, m, false, func(method string) (any, error) { return nil, &rpc.Error{Message: "unknown method " + method} })
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		status, out := computerLocaleRequest(t, mux, "GET", "/api/v1/machines/mac/computer/clipboard", "", h.cfg.Token, lang)
		if status != 409 || out["error"] != i18n.T(lang, "hub-computer.err_clipboard_outdated") {
			t.Fatalf("outdated: %d %v", status, out)
		}
		for _, path := range []string{"/computer", "/browser"} {
			status, out = computerLocaleRequest(t, mux, "GET", "/api/v1/machines/mac"+path, "", h.cfg.Token, lang)
			key := "hub-computer.note_outdated"
			if path == "/browser" {
				key = "hub-browser.note_outdated"
			}
			if status != 200 || out["note"] != i18n.T(lang, key) {
				t.Fatalf("outdated status: %d %v", status, out)
			}
		}
	}
	// A manager without a database is an explicit localized error, not a silent success.
	h.store = nil
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		status, out := computerLocaleRequest(t, mux, "DELETE", "/api/v1/machines/mac/computer/apps/example.app", "", h.cfg.Token, lang)
		if status != 500 || out["error"] != i18n.T(lang, "hub-computer_apps.err_no_database") {
			t.Fatalf("database: %d %v", status, out)
		}
	}
}

func TestComputerLocaleLCUEndToEnd(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			h, _ := extensionTestHub(t)
			h.opts.Lang = func() i18n.Lang { return lang }
			m, _ := computerMac(t, h, "mac")
			deskOf(h)
			raw := json.RawMessage(`{"conversation":"example-chat","params":{"mode":"form","message":"Original runtime message","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_params":{"app":"com.example.app"},"tool_params_display":[{"name":"app","value":"Example App"}],"persist":["always","session"]}}}`)
			for _, assistant := range []bool{false, true} {
				out := make(chan map[string]any, 1)
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				go func() { result, _ := h.answerComputerElicit(ctx, m, raw); out <- result }()
				select {
				case card := <-deskOf(h).ch:
					want := i18n.T(lang, "hub-computer.lcu_session")
					if card.ix.Questions[0].Options[1].Label != want || !strings.Contains(card.ix.Summary, i18n.T(lang, "hub-computer.lcu_app", i18n.Args{"app": "Example App"})) {
						t.Fatalf("card: %+v", card.ix)
					}
					resolution := proto.Resolution{Answers: map[string]string{card.ix.Summary: want}}
					if assistant {
						resolution.AnsweredBy = proto.AnsweredByAssistant
					}
					card.answer <- resolution
				case <-ctx.Done():
					t.Fatal("no localized card")
				}
				select {
				case result := <-out:
					meta, _ := result["_meta"].(map[string]any)
					if assistant && result["action"] != "cancel" || !assistant && (result["action"] != "accept" || meta["persist"] != "session") {
						t.Fatalf("answer: %v", result)
					}
				case <-ctx.Done():
					t.Fatal("no elicitation reply")
				}
			}
		})
	}
}

func TestComputerLocaleEnumValuesStayOriginal(t *testing.T) {
	raw := json.RawMessage(`{"mode":"form","message":"Pick one","requestedSchema":{"type":"object","properties":{"choice":{"type":"string","enum":["Deny","Deny (cancel)","拒绝","拒否"]}},"required":["choice"]}}`)
	original := lcuconfirm.Card(raw)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		ix, choices := computerElicitPresentation(lang, raw, original)
		for n, option := range original.Questions[0].Options[:4] {
			if ix.Questions[0].Options[n].Label != option.Label {
				t.Fatal("runtime enum translated")
			}
			if _, ok := choices[option.Label]; ok {
				t.Fatal("runtime enum reinterpreted as decline")
			}
		}
		if choices[ix.Questions[0].Options[4].Label] != "拒绝" {
			t.Fatal("decline not restored")
		}
	}
}

func TestComputerLocaleLCUPresentationAndAnswers(t *testing.T) {
	raw := json.RawMessage(`{"mode":"form","message":"Original runtime message","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_params":{"app":"com.example.app"},"tool_params_display":[{"name":"app","value":"Example App"}],"persist":["always","session"],"riskLevel":"high","warningSubtitle":"Original runtime warning"}}`)
	original := lcuconfirm.Card(raw)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		ix, choices := computerElicitPresentation(lang, raw, original)
		if ix.ToolName != i18n.T(lang, "hub-computer.lcu_tool") || ix.Reason != "Original runtime warning" || !strings.Contains(ix.Summary, "Original runtime message") || !strings.Contains(ix.Summary, i18n.T(lang, "hub-computer.lcu_high_risk")) {
			t.Fatalf("%s: %+v", lang, ix)
		}
		for n, persist := range []string{"", "session", "always"} {
			label := ix.Questions[0].Options[n].Label
			res := proto.Resolution{Answers: map[string]string{ix.Summary: choices[label]}}
			out := lcuconfirm.Answer(raw, res)
			meta, _ := out["_meta"].(map[string]any)
			if out["action"] != "accept" || persist != "" && meta["persist"] != persist {
				t.Fatalf("%s: %v", label, out)
			}
		}
	}
	if original.ToolName != "操控电脑" || original.Questions[0].Options[1].Label != "本会话允许" {
		t.Fatal("canonical parser card mutated")
	}
}
