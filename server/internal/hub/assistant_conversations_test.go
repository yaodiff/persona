package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
)

const convDir = "/home/me/.persona/assistant"

// conversationsHub is a hub whose assistant runs on a fake node with a main conversation and three side ones (one
// of them Codex), plus a coding agent elsewhere; settings are kept in memory.
func conversationsHub(t *testing.T) (*Hub, *assistantNode) {
	t.Helper()
	h := newTestHub(t)
	h.store = &settingsStore{vals: map[string][]byte{}}
	n := newAssistantNode(t, h)
	n.m.info.Home = "/home/me"
	if err := h.store.PutSetting(context.Background(), "assistant", assistant.Settings{MachineID: "server", Main: "main-0001", Timezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	list := []proto.Session{
		{SessionID: "main-0001", Cwd: convDir, Title: "长期对话", UpdatedAt: now.Add(-time.Minute), LastText: "早上好"},
		{SessionID: "trip-0002", Cwd: convDir, Title: "1030 | 探索 | 周末出游", UpdatedAt: now.Add(-2 * time.Hour), LastText: "订好了酒店"},
		{SessionID: "code-0003", Cwd: convDir, Title: "1031 | 修复 | 登录报错", UpdatedAt: now.Add(-3 * time.Hour), Status: proto.StatusWorking},
		{SessionID: "codex-0004", Cwd: convDir, Title: "codex 的对话", Agent: "codex", UpdatedAt: now.Add(-4 * time.Hour)},
		{SessionID: "fresh-0006", Cwd: convDir, Title: "assistant", UpdatedAt: now.Add(-5 * time.Hour)}, // not named yet
		{SessionID: "agent-0005", Cwd: "/code/app", Title: "不是助手的"},
	}
	editSessions(n.m, func([]proto.Session) []proto.Session { return list })
	for _, s := range list {
		n.known[s.SessionID] = true
	}
	connectAssistant(t, h)
	return h, n
}

func tool(t *testing.T, h *Hub, n *assistantNode, self, name, args string) (string, bool) {
	t.Helper()
	r := h.callAssistantTool(context.Background(), n.m, self, name, json.RawMessage(args))
	if len(r.Content) == 0 {
		t.Fatalf("%s: no content", name)
	}
	return r.Content[0].Text, r.IsError
}

func ids(list []proto.Session) string {
	var out []string
	for _, s := range list {
		id := s.SessionID
		if s.Archived {
			id += "(archived)"
		}
		out = append(out, id)
	}
	return strings.Join(out, " ")
}

// Archiving only hides: the list the app gets leaves it out unless asked, the sessions push flags it, the main
// conversation can't be archived, and a message from the user brings it back.
func TestArchiveConversations(t *testing.T) {
	h, n := conversationsHub(t)
	mux := http.NewServeMux()
	h.assistantRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	post := func(path string) int {
		res, err := http.Post(srv.URL+path, "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	list := func(q string) string {
		res, err := http.Get(srv.URL + "/api/v1/assistant" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v assistantView
		_ = json.NewDecoder(res.Body).Decode(&v)
		return ids(v.Conversations)
	}

	if c := post("/api/v1/assistant/conversations/trip-0002/archive"); c != http.StatusOK {
		t.Fatalf("archive: %d", c)
	}
	if got := list(""); got != "main-0001 code-0003 codex-0004 fresh-0006" {
		t.Fatalf("archived ones are left out: %s", got)
	}
	if got := list("?archived=1"); got != "main-0001 trip-0002(archived) code-0003 codex-0004 fresh-0006" {
		t.Fatalf("?archived=1 lists them flagged: %s", got)
	}
	if got := ids(h.decorated(n.m)); !strings.Contains(got, "trip-0002(archived)") || strings.Contains(got, "agent-0005(archived)") {
		t.Fatalf("the sessions push flags it: %s", got)
	}
	if c := post("/api/v1/assistant/conversations/main-0001/archive"); c != http.StatusBadRequest {
		t.Fatalf("the main conversation can't be archived: %d", c)
	}
	if c := post("/api/v1/assistant/conversations/bad%20id/archive"); c != http.StatusBadRequest {
		t.Fatalf("a bad id: %d", c)
	}

	// Kept across a restart (the database's settings, not memory).
	h2 := newTestHub(t)
	h2.store = h.store
	connectAssistant(t, h2)
	if !shown(h2, "trip-0002").Archived {
		t.Fatal("not kept")
	}

	if c := post("/api/v1/assistant/conversations/trip-0002/unarchive"); c != http.StatusOK || list("") != "main-0001 trip-0002 code-0003 codex-0004 fresh-0006" {
		t.Fatalf("unarchive: %d %s", c, list(""))
	}
	post("/api/v1/assistant/conversations/codex-0004/archive")
	if userSpoke(h, "server", "codex-0004"); shown(h, "codex-0004").Archived {
		t.Fatal("the user's message brings it back")
	}
}

func TestConversationsListTool(t *testing.T) {
	h, n := conversationsHub(t)
	out, isErr := tool(t, h, n, "trip-0002", "conversations_list", `{}`)
	if isErr || !strings.HasPrefix(out, "5 conversation(s)") || strings.Contains(out, "agent-0005") {
		t.Fatalf("%v %s", isErr, out)
	}
	for _, want := range []string{"main-0001 「主对话」 [main]", "trip-0002 「1030 | 探索 | 周末出游」 [this conversation]",
		"code-0003 「1031 | 修复 | 登录报错」 [running]", "codex-0004 「codex 的对话」 [codex]", "last: 订好了酒店", "fresh-0006 「新对话」"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	if strings.Index(out, "main-0001") > strings.Index(out, "trip-0002") {
		t.Errorf("newest first:\n%s", out)
	}
	out, _ = tool(t, h, n, "trip-0002", "conversations_list", `{"query":"酒店"}`)
	if !strings.HasPrefix(out, "1 conversation(s)") || !strings.Contains(out, "trip-0002") {
		t.Fatalf("the query matches the last message: %s", out)
	}
	if c := archive(t, h, "codex-0004", true); c != http.StatusOK {
		t.Fatal(c)
	}
	if out, _ = tool(t, h, n, "", "conversations_list", `{}`); strings.Contains(out, "codex-0004") || !strings.Contains(out, "archived ones left out") {
		t.Fatalf("archived ones hidden: %s", out)
	}
	if out, _ = tool(t, h, n, "", "conversations_list", `{"include_archived":true,"limit":1}`); !strings.Contains(out, "5 conversation(s)") ||
		!strings.Contains(out, "the first 1") || strings.Count(out, "\n- ") != 1 {
		t.Fatalf("include_archived, limit: %s", out)
	}
}

func TestConversationReadTool(t *testing.T) {
	h, n := conversationsHub(t)
	n.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: "订周六的酒店"},
		{Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		{Role: "assistant", Kind: proto.KindText, Text: "订好了酒店"}}
	out, isErr := tool(t, h, n, "main-0001", "conversation_read", `{"id":"trip-00"}`)
	if !isErr || !strings.Contains(out, "no conversation") {
		t.Fatalf("a prefix needs 8 characters: %s", out)
	}
	out, isErr = tool(t, h, n, "main-0001", "conversation_read", `{"id":"trip-0002","turns":1}`)
	if isErr || !strings.HasPrefix(out, "「1030 | 探索 | 周末出游」 trip-0002") || !strings.Contains(out, "User: 订周六的酒店") ||
		!strings.Contains(out, "→ Bash ls") || !strings.Contains(out, "Agent: 订好了酒店") {
		t.Fatalf("%s", out)
	}
	if _, isErr = tool(t, h, n, "main-0001", "conversation_read", `{"id":"agent-0005"}`); !isErr {
		t.Fatal("a coding agent is not a conversation")
	}
}

// "MMDD | type | topic" with the day it started; the user's title stays unless they asked; a Codex conversation's title
// is only the hub's; the automatic title doesn't replace the assistant's.
func TestConversationRenameTool(t *testing.T) {
	h, n := conversationsHub(t)
	started := time.Now().Add(-20 * time.Hour) // a pending title older than a day is forgotten
	noteStarted(t, h, "server", "trip-0002", started)
	want := started.In(h.userLocation()).Format("0102") + " | 研究 | 周末出游的酒店和路线安排比较"
	out, isErr := tool(t, h, n, "trip-0002", "conversation_rename", `{"id":"trip-0002","kind":"研究","topic":"周末出游的酒店和路线安排比较表"}`)
	if isErr || shown(h, "trip-0002").Title != want || !strings.Contains(out, "this conversation") {
		t.Fatalf("%v %s %q", isErr, out, shown(h, "trip-0002").Title)
	}
	if p := n.lastSend(t)["rename"].(map[string]any); p["title"] != want {
		t.Fatalf("Claude's transcript gets it too: %v", p)
	}
	turnEnded(t, h, "server", "trip-0002")
	if shown(h, "trip-0002").Title != want {
		t.Fatal("the automatic title does not replace the assistant's")
	}

	if _, isErr = tool(t, h, n, "x", "conversation_rename", `{"id":"trip-0002","kind":"闲聊","topic":"x"}`); !isErr {
		t.Fatal("an unknown kind")
	}
	if _, isErr = tool(t, h, n, "x", "conversation_rename", `{"id":"main-0001","kind":"研究","topic":"x"}`); !isErr {
		t.Fatal("the main conversation has no title")
	}

	userRenamed(t, h, "server", "code-0003", "我自己起的")
	if out, isErr = tool(t, h, n, "x", "conversation_rename", `{"id":"code-0003","kind":"修复","topic":"登录"}`); !isErr || !strings.Contains(out, "我自己起的") {
		t.Fatalf("the user's title stays: %s", out)
	}
	if _, isErr = tool(t, h, n, "x", "conversation_rename", `{"id":"code-0003","kind":"修复","topic":"登录","user_asked":true}`); isErr {
		t.Fatal("the user asked")
	}
	if by := titleBy(t, h, "code-0003"); by != "user" {
		t.Fatalf("asked for by the user: theirs, %q", by)
	}

	sends := n.sent()
	if _, isErr = tool(t, h, n, "x", "conversation_rename", `{"id":"codex-0004","kind":"功能","topic":"导出"}`); isErr || n.sent() != sends ||
		!strings.HasSuffix(shown(h, "codex-0004").Title, " | 功能 | 导出") {
		t.Fatalf("a Codex conversation's title is the assistant's only: %d calls, %q", n.sent()-sends, shown(h, "codex-0004").Title)
	}
}

func TestConversationArchiveTool(t *testing.T) {
	h, n := conversationsHub(t)
	if out, isErr := tool(t, h, n, "trip-0002", "conversation_archive", `{"id":"main-0001"}`); !isErr || !strings.Contains(out, "主对话") {
		t.Fatalf("not the main one: %s", out)
	}
	out, isErr := tool(t, h, n, "trip-0002", "conversation_archive", `{"id":"trip-0002"}`)
	if isErr || !strings.Contains(out, "this conversation") || !shown(h, "trip-0002").Archived {
		t.Fatalf("itself: %s", out)
	}
	if out, _ = tool(t, h, n, "x", "conversation_archive", `{"id":"trip-0002"}`); !strings.Contains(out, "already") {
		t.Fatal(out)
	}
	// Long out of the node's list: the id alone brings it back.
	archive(t, h, "gone-0009", true)
	if out, isErr = tool(t, h, n, "x", "conversation_unarchive", `{"id":"gone-0009"}`); isErr || shown(h, "gone-0009").Archived {
		t.Fatalf("%s", out)
	}
	if out, isErr = tool(t, h, n, "x", "conversation_unarchive", `{"id":"trip-0002"}`); isErr || shown(h, "trip-0002").Archived {
		t.Fatalf("%s", out)
	}
}

// A new conversation and a message to another: marked as coming from the sender, queued while the receiver runs
// and delivered when its turn ends, never to itself, and cut after a few hops or too many in a row.
func TestConversationNewAndSend(t *testing.T) {
	h, n := conversationsHub(t)
	out, isErr := tool(t, h, n, "trip-0002", "conversation_new", `{"text":"把行程整理成表格"}`)
	if isErr || !strings.Contains(out, "Started conversation s1") {
		t.Fatalf("%s", out)
	}
	sent := n.lastSend(t)["text"].(string)
	if !strings.Contains(sent, `title="来自对话「1030 | 探索 | 周末出游」"`) || !strings.Contains(sent, "trip-0002") ||
		!strings.Contains(sent, "第 1 跳") || !strings.HasSuffix(sent, "把行程整理成表格") {
		t.Fatalf("the opening message: %q", sent)
	}
	if hopOf(t, h, "s1") != 1 {
		t.Fatal("the new conversation works at hop 1")
	}
	if first := strings.TrimSpace(regexp.MustCompile(`(?s)<persona-context\b[^>]*>.*?</persona-context>`).ReplaceAllString(sent, "")); first != "把行程整理成表格" {
		t.Fatalf("its title is made from the message, not the note: %q", first)
	}

	if out, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"trip-0002","text":"x"}`); !isErr {
		t.Fatalf("not to itself: %s", out)
	}

	// An idle one gets it now.
	sends := n.sent()
	if out, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"main-0001","text":"行程定了"}`); isErr || n.sent() != sends+1 {
		t.Fatalf("%s", out)
	}
	if p := n.lastSend(t); p["session_id"] != "main-0001" || p["resume_only"] != true || !strings.HasSuffix(p["text"].(string), "行程定了") {
		t.Fatalf("%v", p)
	}

	// A running one: queued, shown waiting, delivered when the turn ends.
	sends = n.sent()
	if out, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"code-0003","text":"顺便看下登录"}`); isErr || !strings.Contains(out, "queued") || n.sent() != sends {
		t.Fatalf("%s", out)
	}
	for _, s := range h.decorated(n.m) {
		if s.SessionID == "code-0003" && (len(s.Queued) != 1 || !strings.Contains(s.Queued[0], "顺便看下登录")) {
			t.Fatalf("shown waiting: %v", s.Queued)
		}
	}
	list := n.m.sessionList()
	list[2].Status = proto.StatusIdle
	n.m.setSessions(list)
	deadline := time.Now().Add(5 * time.Second)
	for n.sent() != sends+1 {
		if time.Now().After(deadline) {
			t.Fatal("not delivered after the turn")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p := n.lastSend(t); p["session_id"] != "code-0003" {
		t.Fatalf("%v", p)
	}
	for len(shown(h, "code-0003").Queued) != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(shown(h, "code-0003").Queued) != 0 || hopOf(t, h, "code-0003") != 1 {
		t.Fatalf("off the queue, working at hop 1: %d %d", len(shown(h, "code-0003").Queued), hopOf(t, h, "code-0003"))
	}

	// The chain: code-0003 (hop 1) → codex (2) → trip (3) → refused at 4.
	if _, isErr = tool(t, h, n, "code-0003", "conversation_send", `{"id":"codex-0004","text":"a"}`); isErr {
		t.Fatal("hop 2")
	}
	if _, isErr = tool(t, h, n, "codex-0004", "conversation_send", `{"id":"trip-0002","text":"b"}`); isErr {
		t.Fatal("hop 3")
	}
	if out, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"main-0001","text":"c"}`); !isErr || !strings.Contains(out, "hop 4") {
		t.Fatalf("hop 4: %s", out)
	}
	// The user speaks in it: back to hop 0, and it may send again.
	userSpoke(h, "server", "trip-0002")
	if _, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"main-0001","text":"d"}`); isErr {
		t.Fatal("after the user spoke")
	}
	// trip → main twice so far in ten minutes; a third is fine, a fourth is not.
	if _, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"main-0001","text":"e"}`); isErr {
		t.Fatal("the third")
	}
	if out, isErr = tool(t, h, n, "trip-0002", "conversation_send", `{"id":"main-0001","text":"f"}`); !isErr || !strings.Contains(out, "Wait for its answer") {
		t.Fatalf("the fourth to one conversation: %s", out)
	}
}

// #7391: a request and its answer are one exchange. The main conversation and another go back and forth three times
// (each first message back keeps the request's hop) and the fourth request is past the limit: a chain counts its
// exchanges, and two conversations that keep writing to each other stop after 2 × 3 messages.
func TestConversationSendAnswerKeepsHop(t *testing.T) {
	h, n := conversationsHub(t)
	for i, want := range []int{1, 1, 2, 2, 3, 3} {
		from, to := "main-0001", "trip-0002"
		if i%2 == 1 {
			from, to = to, from
		}
		out, isErr := tool(t, h, n, from, "conversation_send", fmt.Sprintf(`{"id":%q,"text":"第 %d 条"}`, to, i+1))
		if isErr || !strings.Contains(out, "Sent to") {
			t.Fatalf("message %d, %s → %s: %s", i+1, from, to, out)
		}
		if p := n.lastSend(t); p["session_id"] != to || !strings.Contains(p["text"].(string), fmt.Sprintf("第 %d 跳", want)) {
			t.Fatalf("message %d went in as %v, want hop %d", i+1, p, want)
		}
		if got := hopOf(t, h, to); got != want {
			t.Fatalf("message %d: %s works at hop %d, want %d", i+1, to, got, want)
		}
	}
	if out, isErr := tool(t, h, n, "main-0001", "conversation_send", `{"id":"trip-0002","text":"第 7 条"}`); !isErr || !strings.Contains(out, "hop 4") {
		t.Fatalf("the fourth request: %s", out)
	}
}

// Sending to an archived conversation brings it back.
func TestConversationSendUnarchives(t *testing.T) {
	h, n := conversationsHub(t)
	archive(t, h, "trip-0002", true)
	out, isErr := tool(t, h, n, "main-0001", "conversation_send", `{"id":"trip-0002","text":"还在吗"}`)
	if isErr || !strings.Contains(out, "back in the list") || shown(h, "trip-0002").Archived {
		t.Fatalf("%s", out)
	}
}
