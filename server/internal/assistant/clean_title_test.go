package assistant

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestOldNodeStartScriptTitleFallsBack(t *testing.T) {
	const title = ". /Users/u/.persona-nas/agent-start-3456784508.sh && true"
	sess := proto.Session{SessionID: "s1", MachineID: "mac", Title: title, Cwd: "/work/demo", Status: proto.StatusIdle}
	ix := proto.Interaction{MachineID: "mac", SessionID: "s1", SessionTitle: title, Cwd: sess.Cwd, Kind: proto.IxScreen}
	if got := cleanTitle(ix).SessionTitle; got != "demo" {
		t.Errorf("card title = %q, want demo", got)
	}
	if got := sessionTitle(&sess); got != "demo" {
		t.Errorf("session title = %q, want demo", got)
	}
	ft := newFleetTest(t)
	if got, _, _ := ft.s.feishuAgentPlaceParts(t.Context(), ix); got != "demo" {
		t.Errorf("Feishu agent label without a listed session = %q, want demo", got)
	}
	ft.d.setSessions("mac", []proto.Session{sess})
	if got, _, _ := ft.s.feishuAgentPlaceParts(t.Context(), ix); got != "demo" {
		t.Errorf("Feishu agent label with the legacy row = %q, want demo", got)
	}
}

// A card's conversation title made by clipping a first message inside its injected tag (an older node's).
func TestCleanTitleDropsInjectedTags(t *testing.T) {
	for title, want := range map[string]string{
		`<persona-context kind="context" title="接上…`:                 "demo",
		`<persona-context kind="context" title="接上旧主对话">` + "\n摘要被截": "接上旧主对话",
		"修一下登录": "修一下登录",
		"":      "",
	} {
		if got := cleanTitle(proto.Interaction{SessionTitle: title, Cwd: "/work/demo"}).SessionTitle; got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", title, got, want)
		}
	}
}

// A todo agent's link title from the prompt it was started with (assistant.todo_link without a title).
func TestCleanPromptTitle(t *testing.T) {
	cases := []struct {
		name     string
		prompt   string
		expected string
	}{
		{
			name: "with context and task",
			prompt: `<persona-context kind="context" title="待办 · 天气" url="">说明内容
多行说明</persona-context>
把 1.3 的发布说明整理出来
第二行要求`,
			expected: "把 1.3 的发布说明整理出来",
		},
		{
			name: "only context",
			prompt: `<persona-context kind="context" title="待办" url="">只有说明</persona-context>
`,
			expected: "",
		},
		{
			name: "with persona-context and task",
			prompt: `<persona-context kind="context" title="待办 · 天气" url="">说明内容
多行说明</persona-context>
把 1.3 的发布说明整理出来`,
			expected: "把 1.3 的发布说明整理出来",
		},
		{
			name:     "clipped persona-context",
			prompt:   `<persona-context kind="context" title="待办" url="">只有说明`,
			expected: "只有说明",
		},
		{
			name:     "plain text",
			prompt:   "普通的第一行任务\n第二行任务",
			expected: "普通的第一行任务",
		},
		{
			name:     "blank text",
			prompt:   "   \n\n\t",
			expected: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cleanPromptTitle(tc.prompt)
			if got != tc.expected {
				t.Fatalf("expected %q, got %q", tc.expected, got)
			}
			if strings.Contains(got, "<persona-context") {
				t.Fatalf("result must never contain a context tag, got %q", got)
			}
		})
	}
}
