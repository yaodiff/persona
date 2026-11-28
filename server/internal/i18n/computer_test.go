package i18n

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestComputerTextLanguages(t *testing.T) {
	for _, c := range []struct {
		lang               Lang
		read, action, note string
	}{
		{En, "Could not read the clipboard", "unknown action", "ego lite first-time setup is not complete"},
		{ZhHans, "读不了剪贴板", "未知操作", "ego lite 还没做完首次设置"},
		{Ja, "クリップボードを読み取れませんでした", "不明な操作です", "ego liteの初期設定が完了していません"},
	} {
		if got := ComputerText(c.lang, "读不了剪贴板"); got != c.read {
			t.Fatalf("%s: %q", c.lang, got)
		}
		if got := Localize(ErrEnglish("hub-computer.err_unknown_action"), c.lang); got != c.action {
			t.Fatalf("%s: %q", c.lang, got)
		}
		if got := ComputerText(c.lang, "ego lite 还没做完首次设置"); got != c.note {
			t.Fatalf("%s: %q", c.lang, got)
		}
	}
	for _, lang := range []Lang{En, ZhHans, Ja} {
		if got := ComputerText(lang, "没放进剪贴板："); got != T(lang, "hub-computer.rpc_clipboard_write", Args{"detail": ""}) {
			t.Fatalf("empty diagnostic: %q", got)
		}
	}
	if got := Localize(ErrEnglish("hub-computer.err_unknown_action"), ""); got != "unknown action" {
		t.Fatalf("legacy English: %q", got)
	}
	for _, raw := range []string{"Original runtime message", `{"error":"读不了剪贴板"}`, "computer.call: args must be an object", "cua-driver run_script: original detail"} {
		for _, lang := range []Lang{En, ZhHans, Ja} {
			if got := ComputerText(lang, raw); got != raw {
				t.Fatalf("unknown/runtime data changed: %q", got)
			}
		}
	}
}

func TestBrowserUnsupportedOSLanguages(t *testing.T) {
	const original = "当前 macOS 12.7.4 低于 ego lite 要求的 13.0，无法运行"
	raw := &proto.Browser{Note: original, Job: &proto.ComputerJob{Kind: "install", State: "failed", Error: original}}
	for _, tc := range []struct {
		lang Lang
		want string
	}{
		{En, "macOS 12.7.4 is below ego lite's minimum requirement (13.0)"},
		{ZhHans, original},
		{Ja, "macOS 12.7.4 は ego lite の必要バージョン（13.0）未満のため起動できません"},
		{"", original},
	} {
		if got := ComputerText(tc.lang, original); got != tc.want {
			t.Errorf("%s: ComputerText = %q, want %q", tc.lang, got, tc.want)
		}
		got := BrowserStatus(tc.lang, raw)
		if got.Note != tc.want || got.Job.Error != tc.want {
			t.Errorf("%s: status note = %q, job error = %q, want %q", tc.lang, got.Note, got.Job.Error, tc.want)
		}
	}
	if raw.Note != original || raw.Job.Error != original {
		t.Fatal("localized shared browser state")
	}
}

func TestComputerStatusCopiesAcrossLanguages(t *testing.T) {
	job := &proto.ComputerJob{Kind: "install", State: "failed", Step: "下载安装器", Error: "安装器失败（exit status 1）：original detail"}
	computer := &proto.Computer{Note: "这台 Mac 上没有装 Cua Driver", Job: job}
	browser := &proto.Browser{Note: "ego lite 还没做完首次设置", Job: job}
	for _, lang := range []Lang{En, ZhHans, Ja, En} {
		c, b := ComputerStatus(lang, computer), BrowserStatus(lang, browser)
		if c == computer || b == browser || c.Job == job || b.Job == job {
			t.Fatal("shared pointers escaped")
		}
		if c.Note != T(lang, "hub-computer.rpc_missing_cua") || b.Note != T(lang, "hub-browser.rpc_setup") || c.Job.Step != T(lang, "hub-computer.rpc_download_installer") || c.Job.Error != T(lang, "hub-computer.rpc_installer_failed", Args{"reason": "exit status 1", "detail": "original detail"}) {
			t.Fatalf("%s: %+v %+v", lang, c, b)
		}
		c.Job.Step = "modified presentation"
	}
	if job.Step != "下载安装器" || computer.Note != "这台 Mac 上没有装 Cua Driver" {
		t.Fatal("canonical state mutated")
	}
	if ComputerStatus(En, nil) != nil || BrowserStatus(En, nil) != nil {
		t.Fatal("nil capability is not nil")
	}
}

func TestLCUArchiveErrorsAreLocalized(t *testing.T) {
	for _, tc := range []struct {
		text, key string
		args      Args
	}{
		{"LCU staging directory is not private", "rpc_lcu_stage_private", nil},
		{"LCU staging directory changed or is not empty", "rpc_lcu_stage_empty", nil},
		{"LCU archive contains a duplicate path", "rpc_lcu_path_duplicate", nil},
		{"LCU archive contains privileged permission bits", "rpc_lcu_privileged", nil},
		{"LCU archive file is writable by group or others", "rpc_lcu_file_writable", nil},
		{"An LCU archive file exceeds the size limit", "rpc_lcu_file_large", nil},
		{"LCU archive contains an unsafe symlink", "rpc_lcu_link_unsafe", nil},
		{"LCU archive contains an unsupported entry type", "rpc_lcu_type_unsupported", nil},
		{"LCU archive contains a cyclic or overly long symlink", "rpc_lcu_link_cycle", nil},
		{"Could not create an LCU archive symlink: example", "rpc_lcu_link_create", Args{"detail": "example"}},
		{"Could not inspect an LCU archive symlink: example", "rpc_lcu_link_inspect", Args{"detail": "example"}},
		{"LCU release installer format is not supported; update Persona and try again", "rpc_lcu_installer_format", nil},
	} {
		for _, lang := range []Lang{En, ZhHans, Ja} {
			want := T(lang, "hub-computer."+tc.key, tc.args)
			if got := ComputerText(lang, tc.text); got != want {
				t.Errorf("%s %s: got %q, want %q", lang, tc.key, got, want)
			}
		}
	}
}
