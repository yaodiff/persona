package transcript

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestActivitySaysWhatAToolCallDoes(t *testing.T) {
	cases := []struct {
		name, input, want string
	}{
		{"Bash", `{"command":"cd server && go test ./internal/hub/..."}`, "在跑测试"},
		{"Bash", `{"command":"./scripts/test/test-server.sh"}`, "在跑测试"},
		{"Bash", `{"command":"xcodebuild test -scheme Persona -destination id=X"}`, "在跑测试"},
		{"Bash", `{"command":"cd web && npm test && npm run build"}`, "在跑测试"},
		{"Bash", `{"command":"gh pr checks 123 --watch"}`, "在等 CI"},
		{"Bash", `{"command":"gh run watch 99 --exit-status"}`, "在等 CI"},
		{"Bash", `{"command":"go build -o /tmp/personad ./cmd/personad"}`, "在编译"},
		{"Bash", `{"command":"go vet ./..."}`, "在检查代码"},
		{"Bash", `{"command":"golangci-lint run"}`, "在检查代码"},
		{"Bash", `{"command":"git push -u origin fix/1-x"}`, "在推送代码"},
		{"Bash", `{"command":"git add a.go && git commit -m 'fix: x'"}`, "在提交代码"},
		{"Bash", `{"command":"gh pr create --title x --body y"}`, "在提 PR"},
		{"Bash", `{"command":"gh pr merge 12 --squash"}`, "在合并 PR"},
		{"Bash", `{"command":"./scripts/deploy/deploy.sh box1"}`, "在部署"},
		{"Bash", `{"command":"./scripts/release/release.sh"}`, "在发版"},
		{"Bash", `{"command":"sleep 40 && date"}`, "在等待"},
		{"Bash", `{"command":"ls -la","description":"List files"}`, "在跑 List files"},
		{"Bash", `{"command":"\n  uname   -a\n"}`, "在跑 uname -a"},
		{"Bash", `{}`, "在跑命令"},
		{"Read", `{"file_path":"/code/app/server/internal/hub/tasks.go"}`, "在读 tasks.go"},
		{"Edit", `{"file_path":"/code/app/TaskViews.swift","old_string":"a","new_string":"b"}`, "在改 TaskViews.swift"},
		{"Write", `{"file_path":"/tmp/notes.md","content":"x"}`, "在写 notes.md"},
		{"Grep", `{"pattern":"lastText"}`, "在搜 lastText"},
		{"Glob", `{"pattern":"**/*.swift"}`, "在找文件 **/*.swift"},
		{"WebFetch", `{"url":"https://example.com/docs/a"}`, "在看网页 example.com"},
		{"WebSearch", `{"query":"swift timelineview"}`, "在搜网页 swift timelineview"},
		{"Agent", `{"description":"Find the push path"}`, "在派子任务 Find the push path"},
		{"TodoWrite", `{"todos":[]}`, "在更新计划"},
		{"Skill", `{"skill":"code-review"}`, "在用技能 code-review"},
		{"ScheduleWakeup", `{"delaySeconds":270}`, "在等待"},
		{"sleep", `{"duration":"25s","duration_ms":25000}`, "在等待"},
		{"wait_agent", `{}`, "在等待"},
		{"subagent", `{"name":"review_api","event":"started"}`, "在派子任务 review_api"},
		{"subagent", `{"name":"review_api","event":"interacted"}`, "在联系子任务 review_api"},
		{"subagent", `{"name":"review_api","event":"completed"}`, "子任务完成 review_api"},
		{"mcp__persona__phone_calendar_events", `{}`, "在调用 phone_calendar_events"},
		{"SomethingNew", `{}`, "在调用 SomethingNew"},
	}
	for _, c := range cases {
		if got := Activity(c.name, json.RawMessage(c.input)); got != c.want {
			t.Errorf("Activity(%s, %s) = %q, want %q", c.name, c.input, got, c.want)
		}
	}
	long := Activity("Grep", json.RawMessage(`{"pattern":"`+strings.Repeat("x", 100)+`"}`))
	if r := []rune(long); len(r) != activityLen || !strings.HasSuffix(long, "…") {
		t.Fatalf("a long activity is clipped to one line: %q", long)
	}
}

// The latest tool call is the activity until the agent writes text again or a new prompt starts a turn.
func TestLastToolFollowsTheLatestStep(t *testing.T) {
	var m Meta
	parse := func(line string) {
		t.Helper()
		ParseLine(0, []byte(line), &m)
	}
	parse(`{"type":"user","timestamp":"2026-11-04T01:19:57Z","message":{"role":"user","content":"修一下登录"}}`)
	parse(`{"type":"assistant","timestamp":"2026-11-04T01:20:00Z","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"先看代码"}]}}`)
	if m.LastTool != nil {
		t.Fatalf("no tool call yet: %+v", m.LastTool)
	}
	parse(`{"type":"assistant","timestamp":"2026-11-04T01:20:01Z","message":{"id":"m2","role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/a/tasks.go"}}]}}`)
	parse(`{"type":"user","timestamp":"2026-11-04T01:20:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"..."}]}}`)
	parse(`{"type":"assistant","timestamp":"2026-11-04T01:20:13Z","message":{"id":"m3","role":"assistant","content":[{"type":"thinking","thinking":"run the tests"},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./..."}}]}}`)
	parse(`{"type":"user","timestamp":"2026-11-04T01:24:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}}`)
	if m.LastTool == nil || m.LastTool.Name != "Bash" || !m.LastTool.At.Equal(time.Date(2026, 11, 4, 1, 20, 13, 0, time.UTC)) {
		t.Fatalf("the latest step is the test run, from when it started: %+v", m.LastTool)
	}
	if got := Activity(m.LastTool.Name, m.LastTool.Input); got != "在跑测试" {
		t.Fatalf("activity %q", got)
	}
	if m.LastText != "先看代码" {
		t.Fatalf("the last text stays the old line: %q", m.LastText)
	}
	parse(`{"type":"assistant","timestamp":"2026-11-04T01:24:05Z","message":{"id":"m4","role":"assistant","content":[{"type":"text","text":"测试过了"}]}}`)
	if m.LastTool != nil {
		t.Fatalf("new words come first again: %+v", m.LastTool)
	}
	parse(`{"type":"assistant","timestamp":"2026-11-04T01:24:06Z","message":{"id":"m5","role":"assistant","content":[{"type":"tool_use","id":"t3","name":"Bash","input":{"command":"gh pr checks 1 --watch"}}]}}`)
	parse(`{"type":"user","timestamp":"2026-11-04T01:30:00Z","message":{"role":"user","content":"再改一下"}}`)
	if m.LastTool != nil {
		t.Fatalf("a new prompt starts a turn: %+v", m.LastTool)
	}
}

// Codex: a command it ran is a step too.
func TestLastToolFromACodexRollout(t *testing.T) {
	var m Meta
	ParseLine(1, []byte(`{"timestamp":"2026-10-26T05:06:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"e1","command":["/bin/zsh","-lc","go test ./..."],"status":"completed","aggregated_output":"ok\n","exit_code":0}}}`), &m)
	if m.LastTool == nil || Activity(m.LastTool.Name, m.LastTool.Input) != "在跑测试" {
		t.Fatalf("codex command: %+v", m.LastTool)
	}
}

// botmux's sentinel is not the agent's last words: a turn that answered in the Feishu topic and ended on it keeps the
// words before; one ending its text with it keeps that text without it. The item itself still shows as written.
func TestLastTextSkipsTheBotmuxSentinel(t *testing.T) {
	var m Meta
	parse := func(text string) {
		t.Helper()
		b, _ := json.Marshal(text)
		ParseLine(0, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":`+string(b)+`}]}}`), &m)
	}
	parse("结论已发到话题")
	parse("BOTMUX_NOTHING_TO_SEND")
	if m.LastText != "结论已发到话题" {
		t.Fatalf("the sentinel alone: %q", m.LastText)
	}
	parse("重试已提交，等它跑完。\n\nBOTMUX_NOTHING_TO_SEND")
	if m.LastText != "重试已提交，等它跑完。" {
		t.Fatalf("the sentinel after text: %q", m.LastText)
	}
	const mention = "  说明里提到 BOTMUX_NOTHING_TO_SEND 这个词  "
	parse(mention)
	if m.LastText != mention {
		t.Fatalf("the word inside a line is the text: %q", m.LastText)
	}
}

// A proxy's thinking-as-text block (ANSI dim italic lines between "💭 Thinking" and "💭 Thought for …") is thinking:
// it shows as a thinking item without the styling and is not the agent's last words.
func TestThinkingAsTextIsThinking(t *testing.T) {
	var m Meta
	parse := func(text string) []proto.Item {
		t.Helper()
		b, _ := json.Marshal(text)
		return ParseLine(0, []byte(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":`+string(b)+`}]}}`), &m)
	}
	parse("先看代码")
	at := m.LastTextAt
	items := parse("\n\x1b[2;3m💭 Thinking\x1b[0m\n\x1b[2;3mThe tests pass, so I'll write the report.\x1b[0m\n\x1b[2;3m\x1b[0m\n\n\x1b[2;3m💭 Thought for 1s (46 tokens)\x1b[0m\n")
	if m.LastText != "先看代码" {
		t.Fatalf("the thinking became the last text: %q", m.LastText)
	}
	if !m.LastTextAt.Equal(at) {
		t.Fatalf("the thinking moved the last text's time: %v", m.LastTextAt)
	}
	if len(items) != 1 || items[0].Kind != proto.KindThinking || items[0].Text != "The tests pass, so I'll write the report." {
		t.Fatalf("the thinking item: %+v", items)
	}
	if items := parse("\x1b[2;3m💭 Thinking\x1b[0m\n\x1b[2;3m💭 Thought for 0s (0 tokens)\x1b[0m\n"); len(items) != 0 {
		t.Fatalf("empty thinking shows: %+v", items)
	}
	parse("💭 Thinking about it, the fix is small.")
	if m.LastText != "💭 Thinking about it, the fix is small." {
		t.Fatalf("plain text that starts the same way is text: %q", m.LastText)
	}
}

// Every verb a Codex subagent event's line starts with is one the hub words in the user's language (ActivityKey): not
// the node's Chinese passed through as it is.
func TestSubagentActivityVerbsHaveKeys(t *testing.T) {
	for event, want := range map[string]string{
		"started": "act_subtask", "interacted": "act_subtask_message", "completed": "act_subtask_done",
	} {
		line := Activity("subagent", json.RawMessage(`{"name":"review_api","event":"`+event+`"}`))
		verb, _, _ := strings.Cut(line, " ")
		if key, isVerb := ActivityKey(verb); key != want || !isVerb {
			t.Errorf("%s: ActivityKey(%q) = %q, %v; want %q, true", event, verb, key, isVerb, want)
		}
	}
}
