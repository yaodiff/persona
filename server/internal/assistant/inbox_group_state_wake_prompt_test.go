package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
)

func TestInboxSelfRepliedPromptRestrictions(t *testing.T) {
	got := prompts.RenderInboxHandlingNote(prompts.NoteSelfReplied, prompts.InboxHandlingNote{
		Owner: "示例用户",
		Text:  "2026-11-13 14:30",
	})
	requireInboxWakePrompt(t, got,
		"交 conclusion（outcome 写 done）",
		"只交 recap 和 follow",
	)
}

func TestInboxSelfReadPromptRestrictions(t *testing.T) {
	got := prompts.RenderInboxHandlingNote(prompts.NoteSelfRead, prompts.InboxHandlingNote{Owner: "示例用户"})
	requireInboxWakePrompt(t, got,
		"交 conclusion 了结",
		"只交建议，或写 recap 和 follow 继续等",
	)
}

func TestInboxCheckPromptRestrictions(t *testing.T) {
	got := prompts.RenderInboxHandlingNote(prompts.NoteCheck, prompts.InboxHandlingNote{
		Owner:  "示例用户",
		Who:    "张三",
		Next:   "等他给报价",
		Checks: 2,
	})
	requireInboxWakePrompt(t, got,
		"交 kind conclusion",
		"交一条建议",
		"给 recap 和 follow",
	)
}

func requireInboxWakePrompt(t *testing.T, got string, outcomes ...string) {
	t.Helper()
	want := []string{
		"不能自己开 Agent",
		"不能发送任何消息",
		"也不能直接做任何会被别人看到的动作",
		"查看和调查只用只读工具",
		"建议只在示例用户确认后由程序执行",
	}
	want = append(want, outcomes...)
	for _, part := range want {
		if !strings.Contains(got, part) {
			t.Errorf("prompt does not contain %q:\n%s", part, got)
		}
	}
}
