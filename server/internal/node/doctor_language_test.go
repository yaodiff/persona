package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestDoctorCommandErrorsSpeakTerminalLanguage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		env    map[string]string
		expect string
	}{
		{name: "English", env: map[string]string{"PERSONA_LANG": "en"}, expect: "node doctor does not accept positional arguments"},
		{name: "Chinese", env: map[string]string{"PERSONA_LANG": "zh-Hans"}, expect: "node doctor 不接受位置参数"},
		{name: "Japanese", env: map[string]string{"PERSONA_LANG": "ja"}, expect: "node doctorは位置引数を受け付けません"},
		{name: "unknown falls back to English", env: map[string]string{"PERSONA_LANG": "fr"}, expect: "node doctor does not accept positional arguments"},
		{name: "POSIX locale", env: map[string]string{"LANG": "en_US.UTF-8"}, expect: "node doctor does not accept positional arguments"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, key := range i18n.TerminalEnv {
				t.Setenv(key, "")
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			var out strings.Builder
			_, err := DoctorCommand(context.Background(), []string{"extra"}, &out)
			if err == nil || err.Error() != tc.expect {
				t.Fatalf("error = %v, want %q", err, tc.expect)
			}
		})
	}
}

func TestDoctorRPCLocaleIsOptIn(t *testing.T) {
	e := newDoctorEnv(t)
	e.healthy(t)
	if err := os.Remove(filepath.Join(e.opts.Dir, "mcp.json")); err != nil {
		t.Fatal(err)
	}
	legacy := callDoctor(t, e, "node.doctor", `{}`)
	for _, tc := range []struct {
		name    string
		params  string
		detail  string
		version string
	}{
		{name: "legacy empty", params: `{}`, detail: "和当前二进制会写的一致", version: "2.1.5，支持 --plugin-dir"},
		{name: "legacy preflight false", params: `{"preflight":false}`, detail: "和当前二进制会写的一致", version: "2.1.5，支持 --plugin-dir"},
		{name: "English", params: `{"locale":"en"}`, detail: "Matches what this binary would write", version: "2.1.5, supports --plugin-dir"},
		{name: "Chinese", params: `{"locale":"zh-Hans"}`, detail: "和当前二进制会写的一致", version: "2.1.5，支持 --plugin-dir"},
		{name: "Japanese", params: `{"locale":"ja"}`, detail: "現在のバイナリが書き込む内容と一致しています", version: "2.1.5、--plugin-dirに対応しています"},
		{name: "regional English", params: `{"locale":"en-US"}`, detail: "Matches what this binary would write", version: "2.1.5, supports --plugin-dir"},
		{name: "unknown falls back to English", params: `{"locale":"de"}`, detail: "Matches what this binary would write", version: "2.1.5, supports --plugin-dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := callDoctor(t, e, "node.doctor", tc.params)
			if rep.OK != legacy.OK || rep.Dir != legacy.Dir || !slices.Equal(rep.Fixed, legacy.Fixed) || len(rep.Checks) != len(legacy.Checks) {
				t.Fatalf("machine report changed: %+v", rep)
			}
			for i := range rep.Checks {
				got, want := rep.Checks[i], legacy.Checks[i]
				if got.Name != want.Name || got.Status != want.Status || got.State != want.State || !slices.Equal(got.Items, want.Items) {
					t.Errorf("machine check %d changed: got %+v, legacy %+v", i, got, want)
				}
			}
			if got := doctorCheckNamed(t, rep, "claude-settings.json").Detail; got != tc.detail {
				t.Errorf("generated detail = %q, want %q", got, tc.detail)
			}
			if got := doctorCheckNamed(t, rep, "claude-version").Detail; got != tc.version {
				t.Errorf("version detail = %q, want %q", got, tc.version)
			}
		})
	}
}

// legacyGoldenDoctorOptions is a stable, mixed report captured from origin/main
// (1e7cb6f6b). No path is read outside this non-existent fixture root.
func legacyGoldenDoctorOptions(t *testing.T) DoctorOptions {
	t.Helper()
	agentSandboxFakes(t, sandbox.Support{OS: "linux", Why: "bwrap unavailable"})
	items := []preflightItem{{kind: "dir", path: "/persona-test/work", role: preflightRoleWorkDir}}
	return DoctorOptions{
		Dir:             "/persona-test/node",
		Bin:             "/persona-test/personad",
		NodeSock:        "/persona-test/node.sock",
		RuntimeSock:     "/persona-test/runtime.sock",
		ClaudeConfigDir: "/persona-test/claude",
		ClaudeProbe:     func(context.Context) (string, bool) { return "2.0.9", false },
		LaunchAgent:     "/persona-test/Library/LaunchAgents/com.persona.node.plist",
		Preflight: func(_ context.Context, lang i18n.Lang) DoctorCheck {
			return preflightCheckIn(preflightOutcome{results: []preflightResult{{
				item: 0, path: "/persona-test/work", state: "hung",
			}}}, items, preflightEnv{goos: "linux", stall: 3 * time.Second}, lang)
		},
	}
}

func TestDoctorLegacyOutputGolden(t *testing.T) {
	rep := RunDoctor(context.Background(), legacyGoldenDoctorOptions(t))
	var jsonOut strings.Builder
	enc := json.NewEncoder(&jsonOut)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		t.Fatal(err)
	}
	var textOut strings.Builder
	PrintDoctor(&textOut, rep)
	const wantJSON = `{
  "ok": false,
  "dir": "/persona-test/node",
  "checks": [
    {
      "name": "node.sock",
      "status": "fail",
      "state": "",
      "detail": "不存在",
      "hint": "没有 node 在监听：hook 和 MCP 会静默退回终端。确认 hub 已连上这台电脑；node 重启会重建",
      "items": []
    },
    {
      "name": "runtime.sock",
      "status": "warn",
      "state": "",
      "detail": "不存在",
      "hint": "没有 node 在监听：DSH、pi 这类 runtime 连不上；没用到可忽略",
      "items": []
    },
    {
      "name": "claude-settings.json",
      "status": "fail",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing claude-settings.json"
      ]
    },
    {
      "name": "claude-hooks",
      "status": "fail",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing claude-hooks/.claude-plugin/plugin.json",
        "missing claude-hooks/hooks/hooks.json",
        "missing claude-live-replies/.claude-plugin/plugin.json",
        "missing claude-live-replies/hooks/hooks.json"
      ]
    },
    {
      "name": "assistant-settings.json",
      "status": "fail",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing assistant-settings.json"
      ]
    },
    {
      "name": "assistant-hooks",
      "status": "fail",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing assistant-hooks/.claude-plugin/plugin.json",
        "missing assistant-hooks/hooks/hooks.json"
      ]
    },
    {
      "name": "mcp.json",
      "status": "fail",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing mcp.json"
      ]
    },
    {
      "name": "agent-plugin",
      "status": "warn",
      "state": "missing",
      "detail": "不存在",
      "hint": "Persona 启动的 Agent 没有 persona-office 技能；重启 node 或运行 personad node doctor --fix",
      "items": [
        "missing"
      ]
    },
    {
      "name": "command-paths",
      "status": "warn",
      "state": "",
      "detail": "没有可检查的生成文件",
      "hint": "先让 node 启动一次，或运行 personad node doctor --fix",
      "items": []
    },
    {
      "name": "claude-version",
      "status": "warn",
      "state": "",
      "detail": "2.0.9（低于 2.1.0，不支持 --plugin-dir）",
      "hint": "hooks 退回 --settings，persona-office 技能不可用；升级 Claude Code",
      "items": []
    },
    {
      "name": "herdr",
      "status": "warn",
      "state": "",
      "detail": "找不到 herdr 的 socket 位置",
      "hint": "设了 HERDR_SOCKET_PATH 的话确认它指向 herdr；这台电脑的引擎不是 herdr 时可忽略",
      "items": []
    },
    {
      "name": "global-permission-hooks",
      "status": "ok",
      "state": "",
      "detail": "没有全局 settings.json",
      "hint": "",
      "items": []
    },
    {
      "name": "agent-sandbox",
      "status": "ok",
      "state": "",
      "detail": "未开启（默认关闭）；工具不可用：bwrap unavailable",
      "hint": "",
      "items": []
    },
    {
      "name": "launchd-process-type",
      "status": "ok",
      "state": "",
      "detail": "没有常驻 node 的 LaunchAgent（node 经 ssh 启动，或者没装）",
      "hint": "",
      "items": []
    },
    {
      "name": "path-access",
      "status": "warn",
      "state": "",
      "detail": "读 /persona-test/work 超过 3s 没有返回：在这里启动的 Claude 会停在 Loading commands and agents，等不到 SessionStart",
      "hint": "检查这个路径的权限，或者它所在的挂载（网络盘、FUSE）是否还有响应；或者把目录挪到别处",
      "items": [
        "卡住（3s 没有返回）：/persona-test/work（工作目录）"
      ]
    }
  ],
  "fixed": []
}
`
	const wantText = `node 自检（/persona-test/node）
有问题｜node.sock：不存在；没有 node 在监听：hook 和 MCP 会静默退回终端。确认 hub 已连上这台电脑；node 重启会重建
注意｜runtime.sock：不存在；没有 node 在监听：DSH、pi 这类 runtime 连不上；没用到可忽略
有问题｜claude-settings.json：不存在；Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix
有问题｜claude-hooks：不存在；Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix
有问题｜assistant-settings.json：不存在；Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix
有问题｜assistant-hooks：不存在；Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix
有问题｜mcp.json：不存在；Persona 启动的会话收不到手机上的提问和审批；重启 node 或运行 personad node doctor --fix
注意｜agent-plugin：不存在；Persona 启动的 Agent 没有 persona-office 技能；重启 node 或运行 personad node doctor --fix
注意｜command-paths：没有可检查的生成文件；先让 node 启动一次，或运行 personad node doctor --fix
注意｜claude-version：2.0.9（低于 2.1.0，不支持 --plugin-dir）；hooks 退回 --settings，persona-office 技能不可用；升级 Claude Code
注意｜herdr：找不到 herdr 的 socket 位置；设了 HERDR_SOCKET_PATH 的话确认它指向 herdr；这台电脑的引擎不是 herdr 时可忽略
正常｜global-permission-hooks：没有全局 settings.json
正常｜agent-sandbox：未开启（默认关闭）；工具不可用：bwrap unavailable
正常｜launchd-process-type：没有常驻 node 的 LaunchAgent（node 经 ssh 启动，或者没装）
注意｜path-access：读 /persona-test/work 超过 3s 没有返回：在这里启动的 Claude 会停在 Loading commands and agents，等不到 SessionStart；检查这个路径的权限，或者它所在的挂载（网络盘、FUSE）是否还有响应；或者把目录挪到别处
共 15 项：3 正常，6 注意，6 有问题
`
	if jsonOut.String() != wantJSON || textOut.String() != wantText {
		t.Fatalf("JSON:\n%s\nTEXT:\n%s", jsonOut.String(), textOut.String())
	}
	for _, locale := range []string{"en", "zh-Hans", "ja", "fr", "LANG=en_US.UTF-8"} {
		t.Run("JSON "+locale, func(t *testing.T) {
			setDoctorTestTerminal(t, locale)
			var out strings.Builder
			failed, err := doctorCommand(context.Background(), []string{"--json"}, &out, func() (DoctorOptions, error) {
				return legacyGoldenDoctorOptions(t), nil
			})
			if !failed || err != nil {
				t.Fatalf("failed %v, error %v", failed, err)
			}
			if out.String() != wantJSON {
				t.Fatalf("JSON changed under %s:\n%s\nwant:\n%s", locale, out.String(), wantJSON)
			}
		})
	}
}

func setDoctorTestTerminal(t *testing.T, locale string) {
	t.Helper()
	for _, key := range i18n.TerminalEnv {
		t.Setenv(key, "")
	}
	if locale == "LANG=en_US.UTF-8" {
		t.Setenv("LANG", "en_US.UTF-8")
	} else {
		t.Setenv("PERSONA_LANG", locale)
	}
}

func TestDoctorCommandSpeaksEveryTerminalLanguage(t *testing.T) {
	for _, tc := range []struct {
		locale  string
		title   string
		check   string
		path    string
		summary string
	}{
		{locale: "en", title: "Node self-check (/persona-test/node)", check: "OK | global-permission-hooks: No global settings.json",
			path: "Reading /persona-test/work did not return within 3s; Claude started here can remain at Loading commands and agents and never reach SessionStart", summary: "Checks: 15 total; OK: 3; warnings: 6; problems: 6"},
		{locale: "zh-Hans", title: "node 自检（/persona-test/node）", check: "正常｜global-permission-hooks：没有全局 settings.json",
			path: "读 /persona-test/work 超过 3s 没有返回：在这里启动的 Claude 会停在 Loading commands and agents，等不到 SessionStart", summary: "共 15 项：3 正常，6 注意，6 有问题"},
		{locale: "ja", title: "ノードの自己診断（/persona-test/node）", check: "正常｜global-permission-hooks：グローバルsettings.jsonがありません",
			path: "/persona-test/workの読み取りが3s以内に返りませんでした。ここで起動したClaudeはLoading commands and agentsのままとなり、SessionStartに到達しません", summary: "合計15件：正常3件、注意6件、問題6件"},
		{locale: "fr", title: "Node self-check (/persona-test/node)", check: "OK | global-permission-hooks: No global settings.json",
			path: "Reading /persona-test/work did not return within 3s; Claude started here can remain at Loading commands and agents and never reach SessionStart", summary: "Checks: 15 total; OK: 3; warnings: 6; problems: 6"},
		{locale: "LANG=en_US.UTF-8", title: "Node self-check (/persona-test/node)", check: "OK | global-permission-hooks: No global settings.json",
			path: "Reading /persona-test/work did not return within 3s; Claude started here can remain at Loading commands and agents and never reach SessionStart", summary: "Checks: 15 total; OK: 3; warnings: 6; problems: 6"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			setDoctorTestTerminal(t, tc.locale)
			var out strings.Builder
			failed, err := doctorCommand(context.Background(), nil, &out, func() (DoctorOptions, error) {
				return legacyGoldenDoctorOptions(t), nil
			})
			if !failed || err != nil {
				t.Fatalf("failed %v, error %v", failed, err)
			}
			for _, want := range []string{tc.title, tc.check, tc.path, tc.summary} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output does not contain %q:\n%s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "node-doctor.") {
				t.Errorf("untranslated key in output:\n%s", out.String())
			}
			if (tc.locale == "en" || tc.locale == "fr" || tc.locale == "LANG=en_US.UTF-8") && strings.ContainsFunc(out.String(), func(r rune) bool { return unicode.Is(unicode.Han, r) }) {
				t.Errorf("CJK text in English output:\n%s", out.String())
			}
		})
	}
}

func TestAgentSandboxReasonsSpeakDoctorLanguage(t *testing.T) {
	for _, tc := range []struct {
		name string
		lang i18n.Lang
	}{
		{name: "English", lang: i18n.En},
		{name: "Chinese", lang: i18n.ZhHans},
		{name: "Japanese", lang: i18n.Ja},
		{name: "unsupported falls back to English", lang: i18n.Resolve("fr")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probeReason := sandbox.NewStatusReason("extension-status.sandbox_no_bwrap", nil)
			agentSandboxFakes(t, sandbox.Support{OS: "linux", Why: probeReason.Error(), Reason: probeReason})
			n := agentNode(t, false)
			if got := checkAgentSandboxIn(n.dir, tc.lang); !strings.Contains(got.Detail, probeReason.Text(tc.lang)) {
				t.Errorf("live probe reason is not localized: %+v; want %q", got, probeReason.Text(tc.lang))
			}

			n = agentNode(t, true)
			n.notePaneStart("claude")
			record := readAgentSandboxStatus(n.dir)[paneStartWhere]
			if record.Reason == nil || record.Reason.Key != "node-doctor.sandbox_pane_start" {
				t.Fatalf("new pane record has no structured reason: %+v", record)
			}
			got := checkAgentSandboxIn(n.dir, tc.lang)
			if want := record.Reason.Text(tc.lang); !strings.Contains(got.Detail, want) {
				t.Errorf("persisted reason is not localized: %+v; want %q", got, want)
			}
		})
	}
}

func TestAgentSandboxLegacyReasonStaysVerbatim(t *testing.T) {
	agentSandboxFakes(t, macSandbox)
	n := agentNode(t, true)
	// Deliberately use text that also has a catalog entry. Without metadata it is an opaque legacy value, not input
	// for reverse translation, and every requested language must retain these exact bytes.
	why := i18n.T(i18n.Default, "extension-status.sandbox_no_bwrap")
	recordAgentSandbox(n.dir, paneStartWhere, sandbox.AgentStatus{State: sandbox.AgentSkipped, Why: why})
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, i18n.Resolve("fr")} {
		if got := checkAgentSandboxIn(n.dir, lang); !strings.Contains(got.Detail, why) {
			t.Errorf("%s changed legacy why %q: %+v", lang, why, got)
		}
	}
	raw, err := os.ReadFile(filepath.Join(n.dir, agentSandboxStatusFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"reason"`) {
		t.Errorf("metadata-free status gained a reason while being rewritten: %s", raw)
	}
}

func TestAgentSandboxReasonMetadataIsAdditive(t *testing.T) {
	n := agentNode(t, true)
	n.notePaneStart("claude")
	record := readAgentSandboxStatus(n.dir)[paneStartWhere]
	if record.Reason == nil {
		t.Fatalf("missing reason metadata: %+v", record)
	}
	wantWhy := i18n.T(i18n.Default, "node-doctor.sandbox_pane_start", i18n.Args{"launcher": "claude"})
	if record.Why != wantWhy {
		t.Fatalf("canonical why changed: got %q, want %q", record.Why, wantWhy)
	}
	raw, err := os.ReadFile(filepath.Join(n.dir, agentSandboxStatusFile))
	if err != nil {
		t.Fatal(err)
	}
	// This is the old reader's schema. encoding/json ignores the additive reason object and keeps why unchanged.
	var old map[string]struct {
		State string `json:"state"`
		Why   string `json:"why"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	if got := old[paneStartWhere]; got.State != sandbox.AgentSkipped || got.Why != wantWhy {
		t.Errorf("old reader sees %+v, want state %q and why %q; JSON: %s", got, sandbox.AgentSkipped, wantWhy, raw)
	}
}

func TestAgentSandboxInnerReasonsSpeakDoctorLanguage(t *testing.T) {
	kinds := []struct {
		agent, where, key string
	}{
		{agent: "claude", where: "launch claude", key: "node-doctor.sandbox_claude_own"},
		{agent: "codex", where: "launch codex", key: "node-doctor.sandbox_codex_shared"},
		{agent: "agy", where: "launch agy", key: "node-doctor.sandbox_unmanaged_agent"},
	}
	languages := []struct {
		name string
		lang i18n.Lang
	}{
		{name: "English", lang: i18n.En},
		{name: "Chinese", lang: i18n.ZhHans},
		{name: "Japanese", lang: i18n.Ja},
		{name: "unsupported falls back to English", lang: i18n.Resolve("fr")},
	}
	for _, kind := range kinds {
		for _, language := range languages {
			t.Run(kind.agent+"/"+language.name, func(t *testing.T) {
				agentSandboxFakes(t, macSandbox)
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Chdir(home)
				dir := filepath.Join(home, ".persona")
				writeFile(t, filepath.Join(dir, sandbox.AgentOnFile), "")
				if kind.agent == "claude" {
					writeFile(t, filepath.Join(home, ".claude", "settings.json"), `{"sandbox":{"enabled":true}}`)
				}
				_, _, _, started := launchExec(kind.agent, "/opt/bin/"+kind.agent, []string{kind.agent}, nil)
				record := readAgentSandboxStatus(dir)[kind.where]
				if started.State != sandbox.AgentSkipped || record.State != sandbox.AgentSkipped ||
					record.Reason == nil || record.Reason.Key != kind.key {
					t.Fatalf("start %+v; persisted %+v; want skipped reason %q", started, record, kind.key)
				}
				if started.Why != record.Reason.Error() || record.Why != record.Reason.Error() {
					t.Errorf("canonical why changed: start %+v; persisted %+v", started, record)
				}
				switch kind.agent {
				case "claude":
					if got := claudeInner(nil, nil, home, home, nil); got != record.Why {
						t.Errorf("legacy claudeInner = %q, persisted why = %q", got, record.Why)
					}
				case "codex":
					if got := codexInner([]string{"codex"}, nil, home, home); got != record.Why {
						t.Errorf("legacy codexInner = %q, persisted why = %q", got, record.Why)
					}
				}
				got := checkAgentSandboxIn(dir, language.lang)
				if want := record.Reason.Text(language.lang); !strings.Contains(got.Detail, want) {
					t.Errorf("doctor does not contain %q: %+v", want, got)
				}
				if language.lang != i18n.ZhHans && strings.Contains(got.Detail, record.Why) {
					t.Errorf("doctor leaked canonical Chinese into %s: %+v", language.lang, got)
				}
			})
		}
	}
}
