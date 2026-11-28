package assistant

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// transcript.Activity's lines, the node's code again, in the user's language.
func TestLocalizedActivity(t *testing.T) {
	act := func(name, input string) string { return transcript.Activity(name, json.RawMessage(input)) }
	for _, c := range []struct{ zh, en, ja string }{
		{act("Read", `{"file_path":"/x/tasks.go"}`), "Reading tasks.go", "tasks.goを読み取り中"},
		{act("Bash", `{"command":"go test ./..."}`), "Running tests", "テスト実行中"},
		{act("Bash", `{"command":"gh pr checks 12 --watch"}`), "Waiting for CI", "CI待ち"},
		{act("Bash", `{"command":"echo hi","description":"Say hi"}`), "Running Say hi", "Say hiを実行中"},
		{act("WebSearch", `{"query":"go generics"}`), "Searching the web for go generics", "Web検索中：go generics"},
		{act("Grep", `{}`), "Searching", "検索中"},
		{act("mcp__persona__mail_send", `{}`), "Calling mail_send", "mail_sendを呼び出し中"},
		{act("AskUserQuestion", `{}`), "Asking you a question", "質問しています"},
		{act("subagent", `{"name":"review_api","event":"started"}`), "Delegating: review_api", "サブタスク依頼中：review_api"},
		{act("subagent", `{"name":"review_api","event":"interacted"}`), "Messaging subtask review_api", "サブタスクreview_apiに連絡中"},
		{act("subagent", `{"name":"review_api","event":"completed"}`), "Subtask done: review_api", "サブタスク完了：review_api"},
		{act("subagent", `{"event":"completed"}`), "Subtask done", "サブタスク完了"},
		{"在做别的", "在做别的", "在做别的"},
	} {
		if got := activityText(i18n.En, c.zh); got != c.en {
			t.Errorf("en %q → %q, want %q", c.zh, got, c.en)
		}
		if got := activityText(i18n.Ja, c.zh); got != c.ja {
			t.Errorf("ja %q → %q, want %q", c.zh, got, c.ja)
		}
	}
	// A long one is cut to a task card's line again.
	long := act("Read", `{"file_path":"/x/a_very_long_file_name_that_goes_on_and_on_and_on.go"}`)
	if got := []rune(activityText(i18n.En, long)); len(got) > activityLen || got[len(got)-1] != '…' {
		t.Fatalf("%q", string(got))
	}
}
func TestLocalizeSession(t *testing.T) {
	bg := []proto.BackgroundTask{{ID: "b1", Kind: "shell", Description: "超时转到后台的命令"}}
	queued := []string{"【来自 另一个会话】look at this", "my own line"}
	s := proto.Session{Activity: "在跑测试", Background: bg, Queued: queued}
	zh := s
	localizeSession(i18n.ZhHans, &zh)
	if zh.Activity != "在跑测试" || zh.Background[0].Description != "超时转到后台的命令" || zh.Queued[0] != queued[0] {
		t.Fatalf("zh-Hans changed it: %+v", zh)
	}
	localizeSession(i18n.En, &s)
	if s.Activity != "Running tests" || s.Background[0].Description != "Command moved to the background after a timeout" ||
		!slices.Equal(s.Queued, []string{"[From Another session] look at this", "my own line"}) {
		t.Fatalf("%+v", s)
	}
	if bg[0].Description != "超时转到后台的命令" || queued[0] != "【来自 另一个会话】look at this" {
		t.Fatal("the node's slices were written to")
	}
}

// A runtime session's last words, when its agent stopped on a failure, are the bridge's own (transcript.RuntimeError…,
// Paseo's): they read in the user's language (#8133). Any other session's last words are its agent's and stay as they
// are, Chinese included, and so do a runtime agent's words that only look like the bridge's.
func TestLocalizeRuntimeFailureLastText(t *testing.T) {
	row := func(source, text string) proto.Session { return proto.Session{SourceKind: source, LastText: text} }
	for _, c := range []struct {
		s      proto.Session
		en, ja string
	}{
		{row(proto.SourceRuntime, "出错了：Provider crashed"), "Error: Provider crashed", "エラー：Provider crashed"},
		{row(proto.SourceRuntime, "出错了"), "Something went wrong", "エラーが発生しました"},
		{row(proto.SourceRuntime, "出错了吗？再看看"), "出错了吗？再看看", "出错了吗？再看看"},
		{row("", "出错了：编译没过"), "出错了：编译没过", "出错了：编译没过"}, // a terminal agent's own words
	} {
		for l, want := range map[i18n.Lang]string{i18n.ZhHans: c.s.LastText, i18n.En: c.en, i18n.Ja: c.ja} {
			s := c.s
			localizeSession(l, &s)
			if s.LastText != want {
				t.Errorf("%s %q: %q, want %q", l, c.s.LastText, s.LastText, want)
			}
		}
	}
}

// What a workflow agent's latest tool call does is the node's Chinese activity line: the apps get it in their
// language (#5511), the node's workflows untouched.
func TestLocalizeWorkflowAgentTools(t *testing.T) {
	act := func(name, input string) string { return transcript.Activity(name, json.RawMessage(input)) }
	read := &proto.WorkflowTool{ID: "toolu_r", Name: "Read", Summary: act("Read", `{"file_path":"/work/project/example.go"}`)}
	search := &proto.WorkflowTool{ID: "srvtoolu_s", Name: "WebSearch", Summary: act("WebSearch", `{"query":"example"}`)}
	other := &proto.WorkflowTool{ID: "toolu_o", Name: "Custom", Summary: "在做别的"}
	runs := []proto.Workflow{
		{TaskID: "w1", Agents: []proto.WorkflowAgent{{ID: "a1", LatestTool: read}, {ID: "a2"}, {ID: "a3", LatestTool: other}}},
		{TaskID: "w2", Agents: []proto.WorkflowAgent{{ID: "b1", LatestTool: search}}},
		{TaskID: "w3"},
	}
	summaries := func(s proto.Session) []string {
		var out []string
		for _, w := range s.Workflows {
			for _, a := range w.Agents {
				if a.LatestTool != nil {
					out = append(out, a.LatestTool.ID+" "+a.LatestTool.Name+": "+a.LatestTool.Summary)
				}
			}
		}
		return out
	}
	for _, c := range []struct {
		l    i18n.Lang
		want []string
	}{
		{i18n.ZhHans, []string{"toolu_r Read: 在读 example.go", "toolu_o Custom: 在做别的", "srvtoolu_s WebSearch: 在搜网页 example"}},
		{i18n.En, []string{"toolu_r Read: Reading example.go", "toolu_o Custom: 在做别的", "srvtoolu_s WebSearch: Searching the web for example"}},
		{i18n.Ja, []string{"toolu_r Read: example.goを読み取り中", "toolu_o Custom: 在做别的", "srvtoolu_s WebSearch: Web検索中：example"}},
	} {
		s := proto.Session{Workflows: runs}
		localizeSession(c.l, &s)
		if got := summaries(s); !slices.Equal(got, c.want) {
			t.Errorf("%s: %q, want %q", c.l, got, c.want)
		}
	}
	if read.Summary != "在读 example.go" || runs[0].Agents[0].LatestTool != read || search.Summary != "在搜网页 example" {
		t.Fatal("the node's workflows were written to")
	}
}

// So is a subagent's latest tool call (#5602): the apps get it in their language with its call id, the node's
// subagents untouched.
func TestLocalizeSubagentTools(t *testing.T) {
	bash := &proto.WorkflowTool{ID: "toolu_b", Name: "Bash", Summary: transcript.Activity("Bash", json.RawMessage(`{"command":"go test ./..."}`))}
	other := &proto.WorkflowTool{ID: "toolu_o", Name: "Custom", Summary: "在做别的"}
	subs := []proto.Subagent{{ID: "a1", LastTool: bash, Steps: 3}, {ID: "a2"}, {ID: "a3", LastTool: other}}
	s := proto.Session{Subagents: subs}
	localizeSession(i18n.En, &s)
	if got := s.Subagents[0].LastTool; got == bash || got.ID != "toolu_b" || got.Name != "Bash" || got.Summary == bash.Summary ||
		got.Summary != activityText(i18n.En, bash.Summary) || s.Subagents[0].Steps != 3 {
		t.Fatalf("en: %+v", got)
	}
	if s.Subagents[1].LastTool != nil || s.Subagents[2].LastTool != other {
		t.Fatalf("en, the others: %+v", s.Subagents)
	}
	if subs[0].LastTool != bash || bash.Summary != transcript.Activity("Bash", json.RawMessage(`{"command":"go test ./..."}`)) {
		t.Fatal("the node's subagents were written to")
	}
	zh := proto.Session{Subagents: subs}
	localizeSession(i18n.ZhHans, &zh)
	if zh.Subagents[0].LastTool != bash {
		t.Fatal("zh-Hans rewritten")
	}
}

// What the node says about a Claude session outside Persona's terminals (why it is view only, that it waits on the
// computer) goes out in the user's language; zh-Hans as the node wrote it, anything else untouched.
func TestClaudeLiveTextInEachLanguage(t *testing.T) {
	for _, reason := range []string{proto.ReadOnlyClaudeBg, proto.ReadOnlyClaudeNoInbox, proto.ReadOnlyClaudeInbox} {
		s := proto.Session{ReadOnlyReason: reason, AttentionReason: proto.AttentionClaudeComputer}
		localizeSession(i18n.En, &s)
		if s.ReadOnlyReason == reason || s.ReadOnlyReason == "" || s.AttentionReason == proto.AttentionClaudeComputer {
			t.Errorf("en: %q / %q", s.ReadOnlyReason, s.AttentionReason)
		}
		j := proto.Session{ReadOnlyReason: reason}
		localizeSession(i18n.Ja, &j)
		if j.ReadOnlyReason == reason || j.ReadOnlyReason == s.ReadOnlyReason {
			t.Errorf("ja: %q", j.ReadOnlyReason)
		}
		z := proto.Session{ReadOnlyReason: reason}
		localizeSession(i18n.ZhHans, &z)
		if z.ReadOnlyReason != reason {
			t.Errorf("zh-Hans: %q", z.ReadOnlyReason)
		}
	}
	other := proto.Session{ReadOnlyReason: "仅查看：这个子 Agent 由父 Agent 驱动"}
	localizeSession(i18n.En, &other)
	if other.ReadOnlyReason != "仅查看：这个子 Agent 由父 Agent 驱动" {
		t.Errorf("a reason the hub does not know was rewritten: %q", other.ReadOnlyReason)
	}
}

func TestCodexHistoryReadOnlyReasonsLocalizeInEachLanguage(t *testing.T) {
	cases := []struct {
		key  string
		args i18n.Args
	}{
		{"codex-history.stalled_turn", i18n.Args{"holder": "Codex"}},
		{"codex-history.stalled_turn", i18n.Args{"holder": "app-server"}},
		{"codex-history.shared_socket_gone", nil},
		{"codex-history.shared_endpoint_invalid", nil},
		{"codex-history.shared_unreachable", nil},
		{"codex-history.shared_home_mismatch", nil},
		{"codex-history.shared_not_loaded", nil},
	}
	for _, tc := range cases {
		zh := i18n.T(i18n.ZhHans, tc.key, tc.args)
		enWant := i18n.T(i18n.En, tc.key, tc.args)
		jaWant := i18n.T(i18n.Ja, tc.key, tc.args)
		if zh == "" || enWant == "" || jaWant == "" || zh == enWant || zh == jaWant || enWant == jaWant {
			t.Fatalf("%s translations not distinct: zh=%q en=%q ja=%q", tc.key, zh, enWant, jaWant)
		}
		for lang, want := range map[i18n.Lang]string{i18n.ZhHans: zh, i18n.En: enWant, i18n.Ja: jaWant} {
			s := proto.Session{ReadOnlyReason: zh}
			localizeSession(lang, &s)
			if s.ReadOnlyReason != want {
				t.Errorf("%s (%s): got %q, want %q", tc.key, lang, s.ReadOnlyReason, want)
			}
		}
	}
}

// A computer-only dialog also tells the owner how to make it answerable from the phone next time (#5965).
func TestClaudeComputerAttentionSuggestsTmuxOrPersona(t *testing.T) {
	for _, c := range []struct {
		name string
		lang i18n.Lang
		want []string
	}{
		{"en", i18n.En, []string{"next time", "start claude", "tmux", "persona", "phone"}},
		{"zh-Hans", i18n.ZhHans, []string{"下次", "启动 claude", "tmux", "persona", "手机"}},
		{"ja", i18n.Ja, []string{"次回", "claudeを起動", "tmux", "persona", "スマホ"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := proto.Session{AttentionReason: proto.AttentionClaudeComputer}
			localizeSession(c.lang, &s)
			got := strings.ToLower(s.AttentionReason)
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("attention reason %q does not mention %q", s.AttentionReason, want)
				}
			}
		})
	}

	// An older node still sends the sentence used before #5965; a new hub localizes it to the same guidance.
	for _, lang := range []i18n.Lang{i18n.En, i18n.Ja} {
		legacy := proto.Session{AttentionReason: proto.AttentionClaudeComputerBefore5965}
		current := proto.Session{AttentionReason: proto.AttentionClaudeComputer}
		localizeSession(lang, &legacy)
		localizeSession(lang, &current)
		if legacy.AttentionReason != current.AttentionReason {
			t.Errorf("%s: legacy attention %q, want the localized current guidance %q", lang, legacy.AttentionReason, current.AttentionReason)
		}
	}
}

// A Claude Desktop session with Persona's permission plugin waiting on a computer-only safety check (e.g. Auto mode
// personOnly review that bypassed hooks) gets accurate Desktop guidance and never tells the user to start in tmux.
func TestClaudeDesktopComputerAttentionDoesNotSuggestTmux(t *testing.T) {
	for _, c := range []struct {
		name string
		lang i18n.Lang
		want []string
	}{
		{"en", i18n.En, []string{"claude desktop", "computer"}},
		{"zh-Hans", i18n.ZhHans, []string{"claude 桌面端", "电脑"}},
		{"ja", i18n.Ja, []string{"claude デスクトップ", "コンピューター"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := proto.Session{AttentionReason: proto.AttentionClaudeDesktopComputer}
			localizeSession(c.lang, &s)
			got := strings.ToLower(s.AttentionReason)
			if strings.Contains(got, "tmux") {
				t.Errorf("desktop attention reason %q must not suggest tmux", s.AttentionReason)
			}
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("desktop attention reason %q does not mention %q", s.AttentionReason, want)
				}
			}
		})
	}
}

func TestClaudePluginPassedOverAttentionExplainsEngineAlternative(t *testing.T) {
	for _, c := range []struct {
		name string
		lang i18n.Lang
		want []string
	}{
		{"en", i18n.En, []string{"managed settings", "computer", "terminal engine"}},
		{"zh-Hans", i18n.ZhHans, []string{"管理设置", "电脑", "终端引擎"}},
		{"ja", i18n.Ja, []string{"管理設定", "コンピューター", "ターミナルエンジン"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := proto.Session{AttentionReason: proto.AttentionClaudePluginPassedOver}
			localizeSession(c.lang, &s)
			got := strings.ToLower(s.AttentionReason)
			for _, want := range c.want {
				if !strings.Contains(got, strings.ToLower(want)) {
					t.Errorf("plugin pass-over attention %q does not mention %q", s.AttentionReason, want)
				}
			}
		})
	}
}

func TestWorkAgentFirstDialogAttentionLocalized(t *testing.T) {
	raw := i18n.T(i18n.Default, "hub-transcript_text.work_agent_first_dialog")
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			s := proto.Session{AttentionReason: raw}
			localizeSession(lang, &s)
			want := i18n.T(lang, "hub-transcript_text.work_agent_first_dialog")
			if s.AttentionReason != want {
				t.Fatalf("localizeSession(%s) = %q, want %q", lang, s.AttentionReason, want)
			}
		})
	}
}
