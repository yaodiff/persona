package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentNameInstructionExamples(t *testing.T) {
	cases := []struct{ title, first, name string }{
		{"◑ Claude Code", "修复导出图片丢失。", "修复导出图片"},
		{"在示例 App 的列表页增加分类筛选…", "在示例 App 的列表页增加分类筛选，空筛选时显示所有项目。", "增加列表分类筛选"},
		{"你是收件箱评审助手，执行一次只读审查…", "你是收件箱评审助手，执行一次只读审查，检查消息去重。", "审查收件箱消息去重"},
		{"参考已有的示例账户，检查代理设置…", "参考已有的示例账户，检查代理设置是否一致。", "检查账户代理设置"},
		{"这个如果合并到发布分支后，需要哪些检查…", "这个如果合并到发布分支后，需要哪些检查才能发布？", "核对发布前检查"},
		{"增加每周备份", "增加每周备份", "支持每周备份"},
		{"检查导出表格的列宽及图片保留规则，确保更长的内容不会被截断且每一张图片都可以正常显示", "检查导出表格的列宽及图片保留规则。", "修复表格导出布局"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, tc.name) })
			f.sessions["server"][0].Title = tc.title
			f.sessions["server"][0].Agent = "claude"
			f.items[0].Text = tc.first
			c := f.sessions["server"][0]
			if !s.tryAgentName(context.Background(), "server", c, time.Now()) || s.agentDisplayName("server", c) != tc.name {
				t.Fatal("instruction title did not become its task name")
			}
			if s.tryAgentName(context.Background(), "server", c, time.Now()) || calls.Load() != 1 {
				t.Fatal("instruction correction repeated a paid call")
			}
			t.Logf("%q -> %q", tc.title, tc.name)
		})
	}
}

func TestAgentNameTextEvidenceAndProvenance(t *testing.T) {
	for _, title := range []string{"请检查导出", "你是只读审查助手", "帮我整理购物清单", "参考已有配置", "这个如果发布后出错怎么办", "Please check export", "You are a review assistant", "Could you check export", "/goal check export", strings.Repeat("x", 41)} {
		if !badAgentName(title, "") {
			t.Errorf("instruction not recognized: %q", title)
		}
	}
	for _, source := range []proto.TitleSource{proto.TitleSourceAuto, proto.TitleSourceRuntime, proto.TitleSourcePrompt} {
		c := proto.Session{SessionID: "s1", Title: "增加每周备份", TitleSource: source}
		want := source == proto.TitleSourcePrompt
		if got := agentNameDue(Settings{}, agentNameRecord{}, c, time.Now()); got != want {
			t.Errorf("copied first-message provenance %s: due=%v want=%v", source, got, want)
		}
	}
	for _, title := range []string{"◑ Claude Code", "请检查导出", strings.Repeat("x", 80)} {
		for _, source := range []proto.TitleSource{proto.TitleSourceUnknown, proto.TitleSourceManual} {
			s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix export") })
			c := f.sessions["server"][0]
			c.Title, c.TitleSource = title, source
			if s.tryAgentName(context.Background(), "server", c, time.Now()) || calls.Load() != 0 || c.TitleSource != source {
				t.Fatalf("protected source %s admitted or rewritten", source)
			}
		}
	}
}

func TestAgentNameFailureLogsStaticReason(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		answer       func(http.ResponseWriter, *http.Request)
		noDialogue   bool
	}{
		{"provider", "model_call_failed", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("private-provider-token private-instruction"))
		}, false},
		{"invalid", "invalid_name", func(w http.ResponseWriter, r *http.Request) { namingReply(w, "<private-instruction>") }, false},
		{"empty", "no_dialogue", func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix export") }, true},
		{"truncated", "model_output_truncated", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"private-instruction"}],"stop_reason":"max_tokens"}`))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := namingFixture(t, tc.answer)
			if tc.noDialogue {
				f.items = nil
			}
			var lines []string
			s.logf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
			c := f.sessions["server"][0]
			if !s.tryAgentName(context.Background(), "server", c, time.Now()) {
				t.Fatal("failed naming not attempted")
			}
			log := strings.Join(lines, "\n")
			if !strings.Contains(log, `session="server"/"s1" reason=`+tc.reason) || strings.Contains(log, "private-") {
				t.Fatalf("missing or unsafe failure log: %s", log)
			}
			s.agentNames.mu.Lock()
			rec := s.agentNames.records[agentNameKey("server", "s1")]
			s.agentNames.mu.Unlock()
			if rec.Failures != 1 || rec.LastFailure != tc.reason || rec.Changes != 0 {
				t.Fatalf("failure reason not retained: %+v", rec)
			}
			t.Log(log)
		})
	}
}
