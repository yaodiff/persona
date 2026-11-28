//go:build !windows

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

func TestPreflightCheckSpeaksEveryLanguage(t *testing.T) {
	items := []preflightItem{{kind: "dir", path: "/persona-test/work", role: preflightRoleWorkDir}}
	outcome := preflightOutcome{results: []preflightResult{{item: 0, path: "/persona-test/work", state: "hung"}}}
	env := preflightEnv{goos: "linux", stall: 3 * time.Second}
	for _, tc := range []struct {
		name   string
		lang   i18n.Lang
		detail string
		hint   string
		item   string
	}{
		{name: "English", lang: i18n.En,
			detail: "Reading /persona-test/work did not return within 3s; Claude started here can remain at Loading commands and agents and never reach SessionStart",
			hint:   "Check this path's permissions and whether its mount (network disk or FUSE) still responds, or move the directory elsewhere",
			item:   "Hung (no response within 3s): /persona-test/work (working directory)"},
		{name: "Chinese", lang: i18n.ZhHans,
			detail: "读 /persona-test/work 超过 3s 没有返回：在这里启动的 Claude 会停在 Loading commands and agents，等不到 SessionStart",
			hint:   "检查这个路径的权限，或者它所在的挂载（网络盘、FUSE）是否还有响应；或者把目录挪到别处",
			item:   "卡住（3s 没有返回）：/persona-test/work（工作目录）"},
		{name: "Japanese", lang: i18n.Ja,
			detail: "/persona-test/workの読み取りが3s以内に返りませんでした。ここで起動したClaudeはLoading commands and agentsのままとなり、SessionStartに到達しません",
			hint:   "このパスの権限と、マウント（ネットワークディスク、FUSE）が応答しているかを確認するか、ディレクトリを別の場所に移動してください",
			item:   "停止（3s以内に応答なし）：/persona-test/work（作業ディレクトリ）"},
		{name: "unknown resolved to English", lang: i18n.Resolve("de"),
			detail: "Reading /persona-test/work did not return within 3s; Claude started here can remain at Loading commands and agents and never reach SessionStart",
			hint:   "Check this path's permissions and whether its mount (network disk or FUSE) still responds, or move the directory elsewhere",
			item:   "Hung (no response within 3s): /persona-test/work (working directory)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := preflightCheckIn(outcome, items, env, tc.lang)
			if c.Name != pathAccessCheck || c.Status != DoctorWarn || c.State != "" || c.Detail != tc.detail || c.Hint != tc.hint {
				t.Errorf("check = %+v", c)
			}
			if len(c.Items) != 1 || c.Items[0] != tc.item {
				t.Errorf("items = %q, want %q", c.Items, tc.item)
			}
		})
	}
}

func TestPreflightVariantsSpeakEveryLanguage(t *testing.T) {
	readable := preflightOutcome{results: []preflightResult{
		{item: 0, path: "/claude", state: "ok"},
		{item: 0, path: "/claude/old", target: "/gone", link: true, state: "missing"},
	}, unchecked: 1}
	items := []preflightItem{{kind: "dir", path: "/claude", role: preflightRoleClaudeDir, global: true}}
	for _, tc := range []struct {
		name, detail, item, hint string
		lang                     i18n.Lang
	}{
		{name: "English", lang: i18n.En, detail: "Readable paths: 1; the check did not finish", item: "Dangling link: /claude/old → /gone",
			hint: "Reads were too slow: check again; if this persists, check whether a working directory or link is on a network disk"},
		{name: "Chinese", lang: i18n.ZhHans, detail: "读了 1 个路径，都能读，但没读完", item: "链接指向的地方不存在：/claude/old → /gone",
			hint: "读得太慢：再检查一次；一直这样的话，看看工作目录或者链接是不是在网络盘上"},
		{name: "Japanese", lang: i18n.Ja, detail: "1個のパスは読み取れましたが、最後まで確認できませんでした", item: "リンクの参照先がありません：/claude/old → /gone",
			hint: "読み取りが遅すぎます。再確認し、続く場合は作業ディレクトリやリンクがネットワークディスク上にあるか確認してください"},
	} {
		t.Run("readable "+tc.name, func(t *testing.T) {
			c := preflightCheckIn(readable, items, envForPreflightLanguage(), tc.lang)
			if c.Detail != tc.detail || c.Hint != tc.hint || len(c.Items) != 2 || c.Items[0] != tc.item {
				t.Errorf("%+v", c)
			}
			if c.Items[1] != i18n.T(tc.lang, "node-doctor.preflight_unchecked_slow", i18n.Args{"count": 1}) {
				t.Errorf("unchecked item = %q", c.Items[1])
			}
		})
	}

	macItems := []preflightItem{{kind: "dir", path: "/Users/me/Documents/app", role: preflightRoleWorkDir}}
	denied := preflightOutcome{results: []preflightResult{{item: 0, path: macItems[0].path, state: "denied", msg: "open " + macItems[0].path + ": operation not permitted"}}}
	macEnv := preflightEnv{goos: "darwin", home: "/Users/me", via: "launchd", exe: "/x/personad"}
	for _, tc := range []struct {
		name  string
		lang  i18n.Lang
		words []string
	}{
		{name: "English", lang: i18n.En, words: []string{"macOS privacy protection", "/x/personad", "the Documents folder", "operation not permitted"}},
		{name: "Chinese", lang: i18n.ZhHans, words: []string{"macOS 的隐私保护", "/x/personad", "「文稿」文件夹", "operation not permitted"}},
		{name: "Japanese", lang: i18n.Ja, words: []string{"macOSのプライバシー保護", "/x/personad", "「書類」フォルダー", "operation not permitted"}},
	} {
		t.Run("privacy "+tc.name, func(t *testing.T) {
			c := preflightCheckIn(denied, macItems, macEnv, tc.lang)
			for _, word := range tc.words {
				if !strings.Contains(c.Detail+c.Hint, word) {
					t.Errorf("%q missing from %+v", word, c)
				}
			}
		})
	}

	volumeItems := []preflightItem{{kind: "dir", path: "/Volumes/share/project", role: preflightRoleWorkDir}}
	volume := preflightOutcome{results: []preflightResult{{item: 0, path: volumeItems[0].path, state: "hung"}}}
	for _, tc := range []struct {
		name, word string
		lang       i18n.Lang
	}{
		{name: "English", lang: i18n.En, word: "external and network volumes"},
		{name: "Chinese", lang: i18n.ZhHans, word: "macOS 读外接盘、网络盘也要授权"},
		{name: "Japanese", lang: i18n.Ja, word: "外部ディスクやネットワークディスク"},
	} {
		t.Run("volume "+tc.name, func(t *testing.T) {
			c := preflightCheckIn(volume, volumeItems, macEnv, tc.lang)
			if !strings.Contains(c.Hint, tc.word) || !strings.Contains(c.Hint, "/x/personad") {
				t.Errorf("hint = %q", c.Hint)
			}
		})
	}

	for _, tc := range []struct {
		name, word string
		lang       i18n.Lang
	}{
		{name: "English", lang: i18n.En, word: "Could not check: old-personad cannot run this check"},
		{name: "Chinese", lang: i18n.ZhHans, word: "没法检查：old-personad 不会做这项检查"},
		{name: "Japanese", lang: i18n.Ja, word: "old-personadではこのチェックを実行できません"},
	} {
		t.Run("old child "+tc.name, func(t *testing.T) {
			c := preflightCheckIn(preflightOutcome{unsupported: "old-personad"}, nil, preflightEnv{}, tc.lang)
			if !strings.Contains(c.Detail, tc.word) {
				t.Errorf("detail = %q", c.Detail)
			}
		})
	}
}

func envForPreflightLanguage() preflightEnv {
	return preflightEnv{goos: "linux", stall: 3 * time.Second}
}

func TestLocalPreflightKeepsAnOldNodesRawResponse(t *testing.T) {
	for _, tc := range []struct {
		name, request string
		lang          i18n.Lang
	}{
		{name: "legacy request", lang: "", request: `{"kind":"preflight"}`},
		{name: "English request", lang: i18n.En, request: `{"kind":"preflight","locale":"en"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newDoctorEnv(t)
			sock := filepath.Join(e.n.home, "old.sock")
			old := DoctorCheck{Name: pathAccessCheck, Status: DoctorWarn, Detail: "旧 node 的原文", Hint: "旧提示", Items: []string{"原项目"}}
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			ln, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ln.Close() })
			request := make(chan string, 1)
			go func() {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				request <- strings.TrimSpace(string(line))
				_, _ = conn.Write(append(raw, '\n'))
			}()
			got := preflightFromNode(sock, e.n.home, e.opts.ClaudeConfigDir)(context.Background(), tc.lang)
			gotRaw, _ := json.Marshal(got)
			if string(gotRaw) != string(raw) {
				t.Errorf("old response changed: %s, want %s", gotRaw, raw)
			}
			req := <-request
			if req != tc.request {
				t.Errorf("request = %s, want %s", req, tc.request)
			}
			var legacy struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal([]byte(req), &legacy); err != nil || legacy.Kind != "preflight" {
				t.Errorf("an old receiver cannot decode %s: %v", req, err)
			}
		})
	}
}

func TestNodeSocketPreflightUsesOnlyRequestedLocale(t *testing.T) {
	t.Setenv("PERSONA_LANG", "ja") // the resident service's ambient locale must not choose the report language
	home, _, _ := preflightHome(t)
	e := newDoctorEnv(t)
	e.n.home = home
	e.listenNode(t, e.opts.NodeSock)
	for _, tc := range []struct {
		name, locale, word string
	}{
		{name: "legacy absent", word: "Claude 启动时读的"},
		{name: "English", locale: "en", word: "Readable paths at Claude startup:"},
		{name: "Chinese", locale: "zh-Hans", word: "Claude 启动时读的"},
		{name: "Japanese", locale: "ja", word: "Claudeの起動時に読み取る"},
		{name: "unknown falls back to English", locale: "de", word: "Readable paths at Claude startup:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := nodeclient.DialAt(context.Background(), e.opts.NodeSock, sockRequest{Kind: "preflight", Locale: tc.locale})
			if err != nil {
				t.Fatal(err)
			}
			var c DoctorCheck
			if err := json.Unmarshal(line, &c); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(c.Detail, tc.word) || strings.Contains(c.Detail, "node-doctor.") {
				t.Errorf("detail = %q, want text containing %q", c.Detail, tc.word)
			}
		})
	}
}
