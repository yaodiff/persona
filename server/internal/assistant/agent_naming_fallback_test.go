package assistant

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentNamePromptFallbackDisplayAndRestart(t *testing.T) {
	for _, initial := range []string{"◑ Claude Code", "Task: repair PDF export"} {
		t.Run(initial, func(t *testing.T) {
			s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
				namingReply(w, "Fix PDF export")
			})
			f.mu.Lock()
			f.sessions["server"][0].Agent = "claude"
			f.sessions["server"][0].Title = initial
			c := f.sessions["server"][0]
			f.mu.Unlock()
			if !s.tryAgentName(context.Background(), "server", c, time.Now()) {
				t.Fatal("initial naming was not dispatched")
			}
			restarted := newServiceOn(t, f, s.store())
			for _, service := range []*Service{s, restarted} {
				for _, title := range []string{"◐ Claude Code", "Task: repair PDF export", "Claude Code", "## Task", "◑ Claude Code", "在示例 App 的列表页增加分类筛选…", "你是收件箱评审助手，执行一次只读审查…", "参考已有的示例账户，检查代理设置…", "这个如果合并到发布分支后，需要哪些检查…", "Fix search"} {
					current := c
					current.Title = title
					if got := service.agentDisplayName("server", current); got != "Fix PDF export" {
						t.Errorf("fallback %q hid saved name: %q", title, got)
					}
					if service.tryAgentName(context.Background(), "server", current, time.Now()) {
						t.Errorf("fallback %q dispatched another default naming", title)
					}
				}
				service.agentNames.mu.Lock()
				record := service.agentNames.records[agentNameKey("server", c.SessionID)]
				service.agentNames.mu.Unlock()
				if record.Changes != 1 || record.RawTitle != initial {
					t.Fatalf("display changed the durable record: %+v", record)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("model calls = %d, want 1", calls.Load())
			}
			for _, test := range []struct {
				name   string
				mutate func(*proto.Session)
			}{
				{"meaningful generated title", func(c *proto.Session) { c.Title = "Fix search"; c.TitleSource = proto.TitleSourceAuto }},
				{"other tool", func(c *proto.Session) { c.Title = "◐ Codex" }},
				{"changed runtime", func(c *proto.Session) { c.Agent = "codex" }},
				{"unknown runtime", func(c *proto.Session) { c.Agent = "" }},
				{"manual flag", func(c *proto.Session) { c.TitleManual = true }},
				{"manual source", func(c *proto.Session) { c.TitleSource = proto.TitleSourceManual }},
				{"unknown source", func(c *proto.Session) { c.TitleSource = proto.TitleSourceUnknown }},
				{"auto source", func(c *proto.Session) { c.TitleSource = proto.TitleSourceAuto }},
				{"runtime source", func(c *proto.Session) { c.TitleSource = proto.TitleSourceRuntime }},
			} {
				t.Run(test.name, func(t *testing.T) {
					current := c
					current.Title = "Task: a different fallback"
					test.mutate(&current)
					if got := restarted.agentDisplayName("server", current); got != "" {
						t.Fatalf("protected native title overridden: %q", got)
					}
				})
			}
		})
	}
}

func TestAgentNamePromptFallbackDuringModelResult(t *testing.T) {
	for _, test := range []struct {
		name, before, after, agent string
		source                     proto.TitleSource
		manual                     bool
		want                       string
	}{
		{"tool to template", "◑ Claude Code", "Task: repair PDF export", "claude", proto.TitleSourcePrompt, false, "Fix PDF export"},
		{"template to tool", "Task: repair PDF export", "◐ Claude Code", "claude", proto.TitleSourcePrompt, false, "Fix PDF export"},
		{"copied short prompt", "◑ Claude Code", "Fix search", "claude", proto.TitleSourcePrompt, false, "Fix PDF export"},
		{"meaningful generated title", "◑ Claude Code", "Fix search", "claude", proto.TitleSourceAuto, false, ""},
		{"tool to unlisted instruction", "◑ Claude Code", "在示例 App 的列表页增加分类筛选…", "claude", proto.TitleSourcePrompt, false, "Fix PDF export"},
		{"other tool", "Task: repair PDF export", "Codex", "claude", proto.TitleSourcePrompt, false, ""},
		{"changed runtime", "◑ Claude Code", "Task: repair PDF export", "codex", proto.TitleSourcePrompt, false, ""},
		{"unknown runtime", "◑ Claude Code", "Task: repair PDF export", "", proto.TitleSourcePrompt, false, ""},
		{"manual flag", "◑ Claude Code", "Task: repair PDF export", "claude", proto.TitleSourcePrompt, true, ""},
		{"manual source", "◑ Claude Code", "Task: repair PDF export", "claude", proto.TitleSourceManual, false, ""},
		{"unknown source", "◑ Claude Code", "Task: repair PDF export", "claude", proto.TitleSourceUnknown, false, ""},
		{"auto source", "◑ Claude Code", "Task: repair PDF export", "claude", proto.TitleSourceAuto, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var f *fakeHub
			s, hub, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
				f.mu.Lock()
				f.sessions["server"][0].Title = test.after
				f.sessions["server"][0].Agent = test.agent
				f.sessions["server"][0].TitleSource = test.source
				f.sessions["server"][0].TitleManual = test.manual
				f.mu.Unlock()
				namingReply(w, "Fix PDF export")
			})
			f = hub
			f.mu.Lock()
			f.sessions["server"][0].Agent = "claude"
			f.sessions["server"][0].Title = test.before
			c := f.sessions["server"][0]
			f.mu.Unlock()
			if !s.tryAgentName(context.Background(), "server", c, time.Now()) {
				t.Fatal("initial naming was not dispatched")
			}
			f.mu.Lock()
			current := f.sessions["server"][0]
			f.mu.Unlock()
			if got := s.agentDisplayName("server", current); got != test.want {
				t.Fatalf("late result = %q, want %q", got, test.want)
			}
			s.agentNames.mu.Lock()
			record := s.agentNames.records[agentNameKey("server", c.SessionID)]
			s.agentNames.mu.Unlock()
			wantChanges := 0
			if test.want != "" {
				wantChanges = 1
			}
			if record.Changes != wantChanges || calls.Load() != 1 {
				t.Fatalf("changes=%d calls=%d, want changes=%d calls=1", record.Changes, calls.Load(), wantChanges)
			}
		})
	}
}
