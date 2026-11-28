package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

const snapA = `**这是主对话**：示例用户平时都在这里找你，接着之前聊过的事往下做。

- 现在：2026-11-01 周日 15:04（Asia/Shanghai）
- 在线的电脑：服务器（server）
- Agents：需要你 1 个，完成未看 0 个，工作中 0 个（详情用 agents_list）
  - 需要你：修登录（server，session_id=aaaaaaaa）
- 飞书日历今天接下来：16:00–17:00 评审
- 已安排的跟进：没有`

// The first message carries the whole snapshot; the next ones only what changed, and the time.
func TestContextDelta(t *testing.T) {
	first, seen := contextDelta(nil, snapA)
	if !strings.HasPrefix(first, "<persona-context>\n实时快照（") || !strings.HasSuffix(first, "</persona-context>") {
		t.Fatalf("first block:\n%s", first)
	}
	for _, want := range []string{"**这是主对话**", "- 在线的电脑", "  - 需要你：修登录", "- 飞书日历今天接下来", "- 已安排的跟进：没有"} {
		if !strings.Contains(first, want) {
			t.Errorf("first block lacks %q:\n%s", want, first)
		}
	}

	same := strings.Replace(snapA, "15:04", "15:30", 1)
	block, seen := contextDelta(seen, same)
	if want := "<persona-context>\n实时快照里和上一份比变了的（没列出的照旧）：\n- 现在：2026-11-01 周日 15:30（Asia/Shanghai）\n</persona-context>"; block != want {
		t.Fatalf("nothing changed but the time:\n%s", block)
	}

	// The waiting agent answered (its indented line goes with the Agents section), the meeting is over (the section
	// is named the same either way).
	later := strings.NewReplacer(
		"需要你 1 个", "需要你 0 个", "\n  - 需要你：修登录（server，session_id=aaaaaaaa）", "",
		"- 飞书日历今天接下来：16:00–17:00 评审", "- 飞书日历：今天接下来没有会",
	).Replace(same)
	block, _ = contextDelta(seen, later)
	for _, want := range []string{"- 现在：", "- Agents：需要你 0 个", "- 飞书日历：今天接下来没有会"} {
		if !strings.Contains(block, want) {
			t.Errorf("lacks %q:\n%s", want, block)
		}
	}
	for _, not := range []string{"这是主对话", "在线的电脑", "修登录", "跟进"} {
		if strings.Contains(block, not) {
			t.Errorf("repeats %q, unchanged:\n%s", not, block)
		}
	}
	if b, s := contextDelta(seen, ""); b != "" || s == nil {
		t.Fatal("no snapshot (the hub didn't answer): no block, and what was seen stays")
	}
}

func TestContextDeltaReplacesDoneAndMutedDetails(t *testing.T) {
	before := strings.Replace(snapA, "  - 需要你：修登录（server，session_id=aaaaaaaa）", `  - 需要你：修登录（server，session_id=aaaaaaaa）
  - 做完的（24 小时内）：旧完成标题（server，session_id=done-1）：旧结论
  - 已屏蔽（用户关了它们的通知，上面的数不算它们；别主动提，问到再说）：旧屏蔽标题（server，session_id=mute-1）`, 1)
	_, seen := contextDelta(nil, before)
	counts := strings.NewReplacer(
		"旧完成标题（server，session_id=done-1）：旧结论", "1 个，agents_list state=done 查",
		"旧屏蔽标题（server，session_id=mute-1）", "1 个，agents_list state=muted 查",
	).Replace(before)
	block, seen := contextDelta(seen, counts)
	for _, want := range []string{"- Agents（", "agents_list state=done 查", "agents_list state=muted 查"} {
		if !strings.Contains(block, want) {
			t.Errorf("the count-only Agents rows lack %q:\n%s", want, block)
		}
	}
	for _, unwanted := range []string{"旧完成标题", "旧屏蔽标题", "旧结论", "done-1", "mute-1"} {
		if strings.Contains(block, unwanted) {
			t.Errorf("stale detail %q survived the rows' replacement:\n%s", unwanted, block)
		}
	}
	empty := strings.NewReplacer(
		"\n  - 做完的（24 小时内）：1 个，agents_list state=done 查", "",
		"\n  - 已屏蔽（用户关了它们的通知，上面的数不算它们；别主动提，问到再说）：1 个，agents_list state=muted 查", "",
	).Replace(counts)
	block, _ = contextDelta(seen, empty)
	if !strings.Contains(block, "（已移除的行：做完的；已屏蔽）") || strings.Contains(block, "agents_list state=") {
		t.Fatalf("the done and muted rows that are gone were not told removed:\n%s", block)
	}
}

type stdinBuf struct{ bytes.Buffer }

func (*stdinBuf) Close() error { return nil }

// A long conversation about to be let go is compacted first; a compaction makes the next message carry the whole
// snapshot again.
func TestAssistantIdleCompaction(t *testing.T) {
	n := assistantNode(t)
	const id = "11111111-2222-3333-4444-555555555555"
	in := &stdinBuf{}
	p := &assistantProc{id: id, agent: "claude", stdin: in}
	n.procs[id] = p
	stream := func(lines ...string) { n.readAssistant(p, strings.NewReader(strings.Join(lines, "\n")+"\n")) }
	_, commit := n.turnContext(id, contextAnswer{Text: snapA})
	commit()

	stream(`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":180000,"cache_creation_input_tokens":900}}},"parent_tool_use_id":null}`,
		`{"type":"stream_event","event":{"type":"message_start","message":{"usage":{"input_tokens":2,"cache_read_input_tokens":9000}}},"parent_tool_use_id":"toolu_1"}`,
		`{"type":"result","subtype":"success"}`)
	later := time.Now().Add(assistantIdleTimeout + time.Minute)
	p.mu.Lock()
	tokens, reap, compact := p.context, p.reapable(later), p.idleCompactable()
	p.mu.Unlock()
	if tokens != 180902 {
		t.Fatalf("context %d: the main thread's last request, not a subagent's", tokens)
	}
	if !reap || !compact {
		t.Fatalf("idle and long: reapable %v, compact first %v", reap, compact)
	}

	n.compactIdle(p)
	if got := strings.TrimSpace(in.String()); got != `{"message":{"content":"/compact","role":"user"},"type":"user"}` {
		t.Fatalf("sent %s", got)
	}
	p.mu.Lock()
	reap, stuck := p.reapable(time.Now().Add(5*time.Minute)), p.reapable(time.Now().Add(11*time.Minute))
	p.mu.Unlock()
	if reap || !stuck {
		t.Fatalf("while compacting: let go %v; one that never reports back let go after 10 minutes %v", reap, stuck)
	}
	stream(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":180902,"post_tokens":9500}}`,
		`{"type":"result","subtype":"success"}`)
	p.mu.Lock()
	tokens, busy := p.context, p.busy
	reap, compact = p.reapable(time.Now().Add(assistantIdleTimeout+time.Minute)), p.idleCompactable()
	p.mu.Unlock()
	if tokens != 9500 || busy {
		t.Fatalf("after the compaction: context %d, busy %v", tokens, busy)
	}
	if !reap || compact {
		t.Fatalf("compacted: let go next time (reapable %v), not compacted again (%v)", reap, compact)
	}
	if block, _ := n.turnContext(id, contextAnswer{Text: snapA}); !strings.Contains(block, "实时快照（") {
		t.Fatalf("after a compaction the whole snapshot again:\n%s", block)
	}
}

// A process that starts with other tools than the conversation's last one says so: that start writes the whole
// history again.
func TestToolsChangedIsLogged(t *testing.T) {
	n := assistantNode(t)
	var logs bytes.Buffer
	n.logger = log.New(&logs, "", 0)
	const id = "11111111-2222-3333-4444-555555555555"
	p := &assistantProc{id: id, agent: "claude"}
	start := func(tools string) {
		n.readAssistant(p, strings.NewReader(`{"type":"system","subtype":"init","tools":[`+tools+`]}`+"\n"))
	}
	start(`"Read","mcp__persona__agents_list","mcp__persona__phone_device_info"`)
	start(`"mcp__persona__phone_device_info","Read","mcp__persona__agents_list"`)
	if logs.Len() != 0 {
		t.Fatalf("the same tools in another order: %s", logs.String())
	}
	start(`"Read","mcp__persona__agents_list"`)
	if !strings.Contains(logs.String(), "tools changed since its last start (+[] -[mcp__persona__phone_device_info])") {
		t.Fatalf("logged %q", logs.String())
	}
}

// assistant.context carries the conversation's launch directory so the hub can resolve a new space conversation's
// context on its first turn, before assistant.send returns or sessions.changed arrives (#17557).
func TestAssistantContextSendsCwd(t *testing.T) {
	n := codexTestNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	peer := rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	n.peer = peer

	var mu sync.Mutex
	var calls []map[string]any
	hub.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method == "assistant.context" {
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			mu.Lock()
			calls = append(calls, body)
			mu.Unlock()
			return map[string]any{"text": "- 现在：测试时间"}, nil
		}
		return map[string]any{}, nil
	}
	go peer.Serve(ctx, h2nR)
	go hub.Serve(ctx, n2hR)
	t.Cleanup(func() {
		cancel()
		_ = n2hW.Close()
		_ = h2nW.Close()
		_ = n2hR.Close()
		_ = h2nR.Close()
	})

	// 1. Existing session state supplies cwd to assistantContext(id).
	const spaceDir = "/home/u/.persona/tracks/qa-plain"
	n.state.put("sess-known", sessionState{Launcher: "claude", Cwd: spaceDir})
	_ = n.assistantContext("sess-known")

	// 2. New Codex/app-server start has an empty session id when asking for context, and still carries cwd.
	_ = n.assistantContextForCwd("", spaceDir, "hello codex")

	// 3. Real assistantSend supplies cwd on assistant.context during a send (even if state was not set yet).
	in := &stdinBuf{}
	p := viewErrorsClaude(t, n, in)
	p.cwd = spaceDir
	n.state.put(testThread, sessionState{Launcher: "claude", StartedAt: time.Now().UTC()})
	if _, err := n.assistantSend(context.Background(), testThread, spaceDir, "claude", "hello space", nil, "", ""); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 3 {
		t.Fatalf("expected at least 3 assistant.context calls, got %d", len(calls))
	}
	if got, _ := calls[0]["cwd"].(string); got != spaceDir {
		t.Fatalf("assistantContext(sess-known) cwd = %q, want %q (params=%v)", got, spaceDir, calls[0])
	}
	if got, _ := calls[1]["cwd"].(string); got != spaceDir {
		t.Fatalf("assistantContextForCwd(empty id) cwd = %q, want %q (params=%v)", got, spaceDir, calls[1])
	}
	if got, _ := calls[2]["cwd"].(string); got != spaceDir {
		t.Fatalf("assistantSend assistant.context cwd = %q, want %q (params=%v)", got, spaceDir, calls[2])
	}
}
