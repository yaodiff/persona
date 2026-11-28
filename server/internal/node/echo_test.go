package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestA2ARuntimeEchoKeepsPeerSourceAfterRestore(t *testing.T) {
	record, err := json.Marshal(echoRecord{
		SessionID: "session", ClientMsgID: "a2a-message", Line: "line", In: echoInRuntime, SentAt: time.Now(),
		Source: &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "peer", Title: "computer/peer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var restored echoRecord
	if err := json.Unmarshal(record, &restored); err != nil {
		t.Fatal(err)
	}
	n := &Node{}
	n.restoreEchoes([]echoRecord{restored})
	raw := n.echoedRaw("session", json.RawMessage(`{"items":[{"id":"line#0","seq":1,"role":"user","kind":"text","text":"Peer request","client_msg_id":"a2a-message"}]}`), "items")
	var page struct{ Items []proto.Item }
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("unexpected page %s: %v", raw, err)
	}
	if got := prompts.ItemSource(page.Items[0]); got == nil || *got != *restored.Source {
		t.Fatalf("A2A echo lost peer provenance: %+v", page.Items[0])
	}
}

// echoItems parses Claude Code transcript lines the way the node serves them.
func echoItems(t *testing.T, lines ...string) []proto.Item {
	t.Helper()
	var meta transcript.Meta
	var out []proto.Item
	for i, l := range lines {
		out = append(out, transcript.ParseLine(i+1, []byte(l), &meta)...)
	}
	return out
}

func userLine(uuid string, at time.Time, content string) string {
	b, _ := json.Marshal(content)
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"message":{"role":"user","content":%s}}`, uuid, at.UTC().Format(time.RFC3339Nano), b)
}

func userBlocks(uuid string, at time.Time, blocks string) string {
	return fmt.Sprintf(`{"type":"user","uuid":%q,"timestamp":%q,"message":{"role":"user","content":%s}}`, uuid, at.UTC().Format(time.RFC3339Nano), blocks)
}

func queuedLine(uuid string, at time.Time, prompt string) string {
	b, _ := json.Marshal(prompt)
	return fmt.Sprintf(`{"type":"attachment","uuid":%q,"timestamp":%q,"attachment":{"type":"queued_command","prompt":%s,"commandMode":"prompt","origin":{"kind":"human"}}}`, uuid, at.UTC().Format(time.RFC3339Nano), b)
}

func assistantLine(uuid string, at time.Time, text string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"assistant","uuid":%q,"timestamp":%q,"message":{"role":"assistant","content":[{"type":"text","text":%s}]}}`, uuid, at.UTC().Format(time.RFC3339Nano), b)
}

func echoTurnEndLine(at time.Time) string {
	return fmt.Sprintf(`{"type":"system","subtype":"turn_duration","uuid":"turn-end","parentUuid":"older","timestamp":%q,"durationMs":1000}`, at.UTC().Format(time.RFC3339Nano))
}

func appendEchoTestLine(t *testing.T, path, line string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
}

func tagsOf(items []proto.Item) map[string]string {
	out := map[string]string{}
	for _, it := range items {
		if it.ClientMsgID != "" {
			out[it.ID] = it.ClientMsgID
		}
	}
	return out
}

func sameTags(t *testing.T, got []proto.Item, want map[string]string) {
	t.Helper()
	g := tagsOf(got)
	if len(g) != len(want) {
		t.Fatalf("tags %v, want %v", g, want)
	}
	for id, c := range want {
		if g[id] != c {
			t.Fatalf("tags %v, want %v", g, want)
		}
	}
}

func TestEchoRuntimeRebindsToNativeLineWithClientMessageID(t *testing.T) {
	var log echoLog
	at := time.Now()
	log.expect(echoRecord{SessionID: "agy-session", ClientMsgID: "sent", Text: "Check this", Images: 1, SentAt: at})
	live := []proto.Item{{ID: "1@live#0", Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: "sent"}}
	if found := log.match("agy-session", echoInRuntime, live); len(found) != 1 || found[0].Line != "1@live" {
		t.Fatalf("live match: %+v", found)
	}
	native := []proto.Item{
		{ID: "2@native#0", Seq: 2, Role: "user", Kind: proto.KindText, Text: "/plan Check this", ClientMsgID: "sent"},
		{ID: "2@native#media0", Seq: 2, Role: "user", Kind: proto.KindImage},
	}
	if found := log.match("agy-session", echoInRuntime, native); len(found) != 1 || found[0].Line != "2@native" {
		t.Fatalf("native match: %+v", found)
	}
	sameTags(t, log.tag("agy-session", echoInRuntime, native), map[string]string{"2@native#0": "sent", "2@native#media0": "sent"})
	if found := log.match("agy-session", echoInRuntime, native); len(found) != 0 {
		t.Fatalf("already matched: %+v", found)
	}
}

// Two sends with the same words ("continue") are tied to their own lines, in the order sent; a line from before the send
// (an older "continue") and one typed at the computer with other words get nothing.
func TestEchoTiesSendsInOrder(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "A", Text: "继续", SentAt: t0, AfterSeq: 1})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "B", Text: "继续", SentAt: t0.Add(time.Second), AfterSeq: 1})
	items := echoItems(t,
		userLine("old", t0.Add(-time.Minute), "继续"),
		userLine("mine1", t0.Add(time.Second), "继续"),
		userLine("desk", t0.Add(2*time.Second), "我在电脑上打的"),
		assistantLine("a1", t0.Add(3*time.Second), "好的"),
		userLine("mine2", t0.Add(4*time.Second), "继续"),
	)
	from, ok := l.waitingFrom("s", echoInTranscript)
	if !ok || from != 1 {
		t.Fatalf("waiting from %d %v", from, ok)
	}
	found := l.match("s", echoInTranscript, items[1:])
	if len(found) != 2 || found[0].Line != "mine1" || found[1].Line != "mine2" {
		t.Fatalf("found %+v", found)
	}
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"mine1#0": "A", "mine2#0": "B"})
	if _, ok := l.waitingFrom("s", echoInTranscript); ok {
		t.Fatal("nothing should be expected any more")
	}
	if got := l.tag("other", echoInTranscript, items); len(tagsOf(got)) != 0 {
		t.Fatalf("another session's items tagged: %v", tagsOf(got))
	}
}

// A message with a picture is one line, several items: every one of them carries the id. Claude's [Image #n] chip and
// the phone's line breaks do not stop the words fitting.
func TestEchoTagsEveryItemOfTheLine(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "P", Text: "看一下这个报错，\n是不是构建脚本的问题", Images: 1, SentAt: t0})
	items := echoItems(t, userBlocks("u1", t0.Add(time.Second),
		`[{"type":"text","text":"[Image #1] 看一下这个报错， 是不是构建脚本的问题"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]`))
	if len(items) < 2 {
		t.Fatalf("want words and a picture, got %+v", items)
	}
	l.match("s", echoInTranscript, items)
	want := map[string]string{}
	for _, it := range items {
		want[it.ID] = "P"
	}
	sameTags(t, l.tag("s", echoInTranscript, items), want)
}

// Input typed while the agent works comes back as a queued_command attachment: that is the copy too.
func TestEchoTiesAQueuedMessage(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "Q", Text: "顺便把测试也跑一下", SentAt: t0, AfterSeq: 1})
	items := echoItems(t,
		assistantLine("a0", t0.Add(-time.Second), "正在改"),
		queuedLine("q1", t0.Add(30*time.Second), "顺便把测试也跑一下"),
	)
	l.match("s", echoInTranscript, items)
	got := l.tag("s", echoInTranscript, items)
	sameTags(t, got, map[string]string{"q1#0": "Q"})
	if !got[1].Queued {
		t.Fatalf("not the queued item: %+v", got[1])
	}
}

// /compact leaves no user line, only the notice it became: that notice is its copy. A message that leaves nothing
// (or something else) is never tied, and stops being looked for after echoWithin.
func TestEchoTiesCompactToItsNotice(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "C", Text: "/compact", SentAt: t0})
	items := echoItems(t, userLine("c1", t0.Add(time.Second), "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>"))
	if len(items) != 1 || items[0].Kind != proto.KindNotice {
		t.Fatalf("want a notice, got %+v", items)
	}
	l.match("s", echoInTranscript, items)
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"c1#0": "C"})

	l.expect(echoRecord{SessionID: "s", ClientMsgID: "C2", Text: "/compact focus on auth tests", SentAt: t0})
	withArgs := echoItems(t, userLine("c2", t0.Add(2*time.Second), "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args>focus on auth tests</command-args>"))
	if len(withArgs) != 1 || withArgs[0].Kind != proto.KindNotice {
		t.Fatalf("want a notice for /compact with args, got %+v", withArgs)
	}
	l.match("s", echoInTranscript, withArgs)
	sameTags(t, l.tag("s", echoInTranscript, withArgs), map[string]string{"c2#0": "C2"})

	var other echoLog
	other.expect(echoRecord{SessionID: "s", ClientMsgID: "C3", Text: "/compact-other", SentAt: t0})
	if found := other.match("s", echoInTranscript, withArgs); len(found) != 0 {
		t.Fatalf("unrelated /compact-other must not tie to CompactNotice: %+v", found)
	}

	l.expect(echoRecord{SessionID: "s", ClientMsgID: "gone", Text: "/clear", SentAt: t0.Add(-echoWithin - time.Minute)})
	if from, ok := l.waitingFrom("s", echoInTranscript); ok {
		t.Fatalf("an expired send is still looked for from %d", from)
	}
}

// Rewritten words do not fit (the apps fall back to theirs), a short text must be the same, a long one only its
// start (the page cuts long texts); lines from before the send by the clock are not it.
func TestEchoFits(t *testing.T) {
	t0 := time.Now()
	long := "请把 server/internal/node 里的发送流程重新梳理一遍，写成文档"
	cases := []struct {
		name string
		sent string
		line string
		at   time.Time
		want bool
	}{
		{"same", "好", "好", t0, true},
		{"short text inside another", "好", "好的，我先看看日志", t0, false},
		{"long text cut by the page", long, "请把 server/internal/node 里的发送\n…（已截断，共 9999 字节）", t0, true},
		{"other words", "帮我看看构建", "完全不同的一句话", t0, false},
		{"pasted wrapper", "第一行\n第二行很长很长很长很长", `<pasted_content id="a1">第一行 第二行很长很长很长很长</pasted_content id="a1">`, t0, true},
		{"written before the send", "继续", "继续", t0.Add(-time.Minute), false},
		{"a command with the arguments its picker chose", "/model", "<command-name>/model</command-name>\n<command-args>opus</command-args>", t0, true},
		{"a command is not another one", "/mod", "<command-name>/model</command-name>\n<command-args>opus</command-args>", t0, false},
		{"hook context on the user's line", "部署到测试环境", "<system-reminder>\nhook says hi\n</system-reminder>\n部署到测试环境", t0, true},
		{"controls pasted inert", "go\x1b[201~\x1b[Z", "go<ESC>[201~<ESC>[Z", t0, true},
		{"controls a runtime kept", "看看\x03构建", "看看\x03构建", t0, true},
		{"inert controls are not other words", "go\x1b[201~", "go<ESC>[202~", t0, false},
	}
	for _, c := range cases {
		var l echoLog
		l.expect(echoRecord{SessionID: "s", ClientMsgID: "x", Text: c.sent, SentAt: t0})
		found := l.match("s", echoInTranscript, echoItems(t, userLine("u", c.at, c.line)))
		if got := len(found) == 1; got != c.want {
			t.Errorf("%s: tied %v, want %v", c.name, got, c.want)
		}
	}
}

// Lines looked at and passed over are not looked at again: a later push only reads what is new.
func TestEchoScansEachLineOnce(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "x", Text: "第三条消息的内容在这里", SentAt: t0})
	first := echoItems(t, userLine("u1", t0, "别的"), userLine("u2", t0, "也是别的"))
	if found := l.match("s", echoInTranscript, first); len(found) != 0 {
		t.Fatalf("found %+v", found)
	}
	if from, _ := l.waitingFrom("s", echoInTranscript); from != 2 {
		t.Fatalf("waiting from %d, want 2", from)
	}
}

// echoHub records what the node notifies the hub of.
type echoHub struct {
	mu     sync.Mutex
	echoes []echoRecord
}

func (h *echoHub) got() []echoRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]echoRecord(nil), h.echoes...)
}

func withEchoHub(t *testing.T, n *Node) *echoHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	end := &echoHub{}
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event != "session.echo" {
			return
		}
		var r echoRecord
		_ = json.Unmarshal(params, &r)
		end.mu.Lock()
		end.echoes = append(end.echoes, r)
		end.mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return end
}

func pageItems(t *testing.T, n *Node, id string) []proto.Item {
	t.Helper()
	res, err := request(t, n, "session.items", map[string]any{"session_id": id, "limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res["items"])
	var items []proto.Item
	_ = json.Unmarshal(b, &items)
	return items
}

// End to end on the node: a send into a Claude pane (the fake herdr's prompt makes Claude write the line) is tied to
// that line in pages, and the hub is told what was expected and what was found. A node started again knows nothing
// until the hub hands its records back (session.echoes): then the same page carries the id again, and a message it
// was still waiting for when it stopped is found.
func TestEchoTiesAnImageOnlyClaudeStart(t *testing.T) {
	n, fake, home := herdrNode(t)
	withSubmitGrace(t, 60*time.Millisecond)
	withFirstPromptWaits(t, 500*time.Millisecond, 3*time.Second)
	cwd := mkdir(t, filepath.Join(home, "project"))
	startPane(t, fake, screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	const image = "aW1hZ2U="
	const clientID = "image-only-start"
	fake.after(func(call herdrCall) {
		if call.Method != "agent.prompt" {
			return
		}
		prompt := str(call.Params["text"])
		if !strings.HasPrefix(prompt, "<attached_images>") || !strings.Contains(prompt, "\npath:") {
			t.Errorf("missing Claude attachment paths in %q", prompt)
		}
		text, _ := json.Marshal(prompt)
		blocks := fmt.Sprintf(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},{"type":"text","text":%s}]`, image, text)
		appendTranscript(t, home, cwd, startedID(fake), userBlocks("image-input", time.Now(), blocks))
	})
	if _, err := request(t, n, "session.start", map[string]any{
		"cwd": cwd, "workspace_id": "w1", "client_msg_id": clientID,
		"images": []proto.Image{{Mime: "image/png", Data: image}},
	}); err != nil {
		t.Fatal(err)
	}
	n.firstWait.Wait()
	items := pageItems(t, n, startedID(fake))
	if len(items) == 0 || items[0].ClientMsgID != clientID {
		t.Fatalf("image-only start did not match its transcript: %+v", items)
	}
}

func TestEchoAcrossSendPagesAndRestart(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, home+"/proj")
	t0 := time.Now()
	path := writeTranscript(t, home, cwd, sidA, t0, assistantLine("a0", t0.Add(-time.Minute), "上一轮的回答"))
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	withSubmitGrace(t, 10*time.Millisecond)
	appendLine := func(line string) {
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fh.WriteString(line + "\n")
		fh.Close()
	}
	claudeWrites := true // Claude writes the prompted line at once
	f.after(func(c herdrCall) {
		if c.Method == "agent.prompt" && claudeWrites {
			var p struct {
				Text string `json:"text"`
			}
			b, _ := json.Marshal(c.Params)
			_ = json.Unmarshal(b, &p)
			appendLine(userLine(fmt.Sprintf("u-%d", time.Now().UnixNano()), time.Now(), p.Text))
		}
	})
	hub := withEchoHub(t, n)
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "把发送流程梳理一遍", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	items := pageItems(t, n, sidA)
	var mine string
	for _, it := range items {
		if it.ClientMsgID == "m-1" {
			mine = it.ID
		}
	}
	if mine == "" || len(tagsOf(items)) != 1 {
		t.Fatalf("tags %v in %+v", tagsOf(items), items)
	}
	waitFor(t, "the hub to hear of it", func() bool { return len(hub.got()) >= 2 })
	got := hub.got()
	if got[0].Line != "" || got[0].AfterSeq != 1 || got[0].Text != "把发送流程梳理一遍" || got[1].Line != echoLineKey(mine) {
		t.Fatalf("hub heard %+v", got)
	}

	// A second send whose line is written only after the node stopped.
	claudeWrites = false
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "再补一个测试用例", "client_msg_id": "m-2"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second record", func() bool { return len(hub.got()) >= 3 })
	kept := hub.got()

	n2, f2, _ := herdrNode(t)
	n2.home = home
	seedAgents(t, n2, f2, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	appendLine(userLine("late", time.Now(), "再补一个测试用例"))
	if got := pageItems(t, n2, sidA); len(tagsOf(got)) != 0 {
		t.Fatalf("a new node tagged %v before it was told", tagsOf(got))
	}
	if _, err := request(t, n2, "session.echoes", map[string]any{"echoes": kept}); err != nil {
		t.Fatal(err)
	}
	sameTags(t, pageItems(t, n2, sidA), map[string]string{mine: "m-1", "late#0": "m-2"})
}

// Codex writes a message as one UserMessage item, its pictures as paths under the words (a message sent mid-turn
// when the turn ends): tied all the same.
func TestEchoTiesACodexMessage(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "X", Text: "这张截图里的按钮为什么换行了", Images: 1, SentAt: t0})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "Y", Images: 1, SentAt: t0.Add(time.Second)})
	at := t0.Add(time.Minute).UTC().Format(time.RFC3339Nano)
	items := echoItems(t,
		`{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"um1","content":[{"type":"text","text":"这张截图里的按钮为什么换行了"},{"type":"local_image","path":"/tmp/a.jpg"}]}}}`,
		`{"timestamp":"`+at+`","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"um2","content":[{"type":"local_image","path":"/tmp/b.jpg"}]}}}`,
	)
	l.match("s", echoInTranscript, items)
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"um1#0": "X", "um2#0": "Y"})
}

// A runtime that put the client_msg_id on its own item is taken at its word: that line is the record's, and no other
// record takes it by its words.
func TestEchoKeepsARuntimesOwnTie(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "A", Text: "继续", SentAt: t0})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "B", Text: "继续", SentAt: t0.Add(time.Second)})
	items := []proto.Item{
		{ID: "r1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "继续", ClientMsgID: "B"},
		{ID: "r2", Seq: 2, Role: "user", Kind: proto.KindText, Text: "继续"},
	}
	found := l.match("s", echoInTranscript, items)
	if len(found) != 2 {
		t.Fatalf("found %+v", found)
	}
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"r1": "B", "r2": "A"})
}

// A record sent or found while a runtime's session read its runtime's own items is looked for again in the transcript
// those stood in for (#8358), from the start: the runtime's seqs (40, 41) count further than the transcript's. Found
// there, it stays: a batch of the runtime's own matched after that does not take it back.
func TestEchoFollowsTheSessionToItsTranscript(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "A", Text: "run the tests please", SentAt: t0, AfterSeq: 40, In: echoInRuntime})
	l.expect(echoRecord{SessionID: "s", ClientMsgID: "B", Text: "and then deploy", SentAt: t0.Add(time.Second), AfterSeq: 41, In: echoInRuntime})
	own := []proto.Item{{ID: "41#0", Seq: 41, Role: "user", Kind: proto.KindText, Text: "run the tests please", ClientMsgID: "A"}}
	if found := l.match("s", echoInRuntime, own); len(found) != 1 || found[0].Line != "41" {
		t.Fatalf("found among the runtime's items: %+v", found)
	}
	items := echoItems(t,
		userLine("u1", t0.Add(time.Second), "run the tests please"),
		assistantLine("a1", t0.Add(2*time.Second), "running them"),
		userLine("u2", t0.Add(3*time.Second), "and then deploy"),
	)
	if from, ok := l.waitingFrom("s", echoInTranscript); !ok || from != 0 {
		t.Fatalf("waiting in the transcript from %d %v", from, ok)
	}
	if found := l.match("s", echoInTranscript, items); len(found) != 2 {
		t.Fatalf("found in the transcript: %+v", found)
	}
	want := map[string]string{"u1#0": "A", "u2#0": "B"}
	sameTags(t, l.tag("s", echoInTranscript, items), want)
	l.match("s", echoInRuntime, own)
	sameTags(t, l.tag("s", echoInTranscript, items), want)
	if _, ok := l.waitingFrom("s", echoInTranscript); ok {
		t.Fatal("still expected in the transcript")
	}
}

// Records a hub hands back from a node before #8358 say nothing of where they count: their lines are tagged wherever
// the session's items come from, as then, and none of them moves. A record still expected is found where its words
// are, and counts there from then on.
func TestEchoKeepsRecordsFromBefore(t *testing.T) {
	t0 := time.Now()
	var l echoLog
	for _, r := range []echoRecord{
		{SessionID: "s", ClientMsgID: "R", Text: "run the tests please", SentAt: t0, Line: "41"}, // among a runtime's items
		{SessionID: "s", ClientMsgID: "T", Text: "and then deploy", SentAt: t0.Add(time.Second), Line: "u2"},
		{SessionID: "s", ClientMsgID: "W", Text: "thanks", SentAt: t0.Add(2 * time.Second), AfterSeq: 2},
	} {
		l.expect(r)
	}
	own := []proto.Item{{ID: "41#0", Seq: 41, Role: "user", Kind: proto.KindText, Text: "run the tests please"}}
	items := echoItems(t,
		userLine("u1", t0.Add(time.Second), "run the tests please"),
		userLine("u2", t0.Add(2*time.Second), "and then deploy"),
		userLine("u3", t0.Add(3*time.Second), "thanks"),
	)
	if found := l.match("s", echoInTranscript, items); len(found) != 1 || found[0].ClientMsgID != "W" || found[0].In != echoInTranscript {
		t.Fatalf("found in the transcript: %+v", found)
	}
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"u2#0": "T", "u3#0": "W"})
	l.match("s", echoInRuntime, own)
	sameTags(t, l.tag("s", echoInRuntime, own), map[string]string{"41#0": "R"})
	sameTags(t, l.tag("s", echoInTranscript, items), map[string]string{"u2#0": "T", "u3#0": "W"})
}

// A watch pushes the line Claude writes as soon as it takes a message, before the send has returned: that push must
// already carry the client_msg_id. It used to be expected only after the send, so the one push of the line went out
// without it and the app's "sending" bubble waited for a page (#4855).
func TestEchoTagsThePushOfALineWrittenWhileTyping(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, home+"/proj")
	t0 := time.Now()
	path := writeTranscript(t, home, cwd, sidA, t0, assistantLine("a0", t0.Add(-time.Minute), "上一轮的回答"))
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
	withSubmitGrace(t, 10*time.Millisecond)
	hub := withHub(t, n)
	if err := n.watch(sidA, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.unwatch(sidA) })
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		_, _ = fh.WriteString(userLine("u-typed", time.Now(), "把发送流程梳理一遍") + "\n")
		fh.Close()
		// the send is still on its way when the watch reads the line (it ticks every 250 ms)
		deadline := time.Now().Add(3 * time.Second)
		for !hasText(hub, "把发送流程梳理一遍") && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	})
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "把发送流程梳理一遍", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	for _, it := range hub.got() {
		if it.Text == "把发送流程梳理一遍" {
			if it.ClientMsgID != "m-1" {
				t.Fatalf("the line was pushed with client_msg_id %q, want m-1", it.ClientMsgID)
			}
			return
		}
	}
	t.Fatal("the line written while typing was never pushed")
}

// A send that surely did not reach the pane leaves no record: a later line with the same words is not tied to it.
func TestEchoForgetsASendThatFailed(t *testing.T) {
	n := &Node{}
	err := n.sendEchoed(sidA, "m-x", "继续", 0, echoAfter{}, func() error { return fmt.Errorf("pane gone") })
	if err == nil {
		t.Fatal("the send's error was lost")
	}
	if _, ok := n.echoes.record(sidA, "m-x"); ok {
		t.Fatal("a failed send is still expected")
	}
	if err := n.sendEchoed(sidA, "m-y", "继续", 0, echoAfter{}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := n.echoes.record(sidA, "m-y"); !ok {
		t.Fatal("a send that went out is not expected")
	}
}

func TestHeldEchoStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change string
		want   proto.SessionEchoState
	}{
		{name: "idle after the turn ended", want: proto.EchoKnownUnseen},
		{name: "idle but the turn ended before the send", change: "before", want: proto.EchoUnknown},
		{name: "idle but the turn ended at the send time", change: "equal", want: proto.EchoUnknown},
		{name: "idle without a recorded turn end", change: "no end", want: proto.EchoUnknown},
		{name: "agent still working", change: "working", want: proto.EchoUnknown},
		{name: "another turn started", change: "open", want: proto.EchoUnknown},
		{name: "agent no longer live", change: "offline", want: proto.EchoUnknown},
		{name: "known unseen after restart", change: "restored", want: proto.EchoKnownUnseen},
		{name: "seen outside latest turns", change: "seen", want: proto.EchoSeen},
		{name: "no record", change: "missing", want: proto.EchoUnknown},
		{name: "another session", change: "other", want: proto.EchoUnknown},
		{name: "forgotten", change: "forgotten", want: proto.EchoUnknown},
		{name: "runtime without a tie", change: "runtime", want: proto.EchoUnknown},
		{name: "expired matching window", change: "old", want: proto.EchoUnknown},
		{name: "queued or accepted", change: "accepted", want: proto.EchoUnknown},
		{name: "in flight", change: "active", want: proto.EchoUnknown},
		{name: "incomplete transcript line", change: "partial", want: proto.EchoUnknown},
		{name: "rewritten transcript", change: "rewritten", want: proto.EchoUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, home+"/proj")
			now := time.Now()
			path := writeTranscript(t, home, cwd, sidA, now, userLine("older", now.Add(-time.Minute), "hello"))
			base := n.echoBase(sidA)
			r := echoRecord{SessionID: sidA, ClientMsgID: "m1", Text: "hello", SentAt: now, In: base.in, AfterSeq: base.seq, History: base.history}
			ended := now.Add(time.Second)
			switch tc.change {
			case "before":
				ended = now.Add(-time.Second)
			case "equal":
				ended = now
			}
			if tc.change != "no end" {
				appendEchoTestLine(t, path, echoTurnEndLine(ended))
			}
			params := map[string]any{"session_id": sidA, "client_msg_id": "m1"}
			switch tc.change {
			case "seen":
				appendEchoTestLine(t, path, userLine("landed", now, "hello"))
				for i := range 6 {
					appendEchoTestLine(t, path, userLine(fmt.Sprintf("later-%d", i), now, "other"))
				}
			case "open":
				appendEchoTestLine(t, path, userLine("new-turn", now.Add(2*time.Second), "other"))
			case "other":
				params["session_id"] = sidB
			case "runtime":
				r.In = echoInRuntime
			case "old":
				r.SentAt = now.Add(-echoWithin - time.Minute)
			case "accepted":
				r.Accepted = true
			case "active":
				n.sends.sent = map[string]*sendRecord{"m1": {at: now, done: make(chan struct{})}}
			case "partial":
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteString(`{"type":"user"`)
				file.Close()
				if err != nil {
					t.Fatal(err)
				}
			case "rewritten":
				replacement := userLine("replacement", now, "new history") + "\n" + echoTurnEndLine(ended) + "\n"
				if err := os.WriteFile(path, []byte(replacement), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.change {
			case "missing":
			case "restored":
				n, f, _ = herdrNode(t)
				n.home = home
				if _, err := request(t, n, "session.echoes", map[string]any{"echoes": []echoRecord{r}}); err != nil {
					t.Fatal(err)
				}
			default:
				n.echoes.expect(r)
			}
			if tc.change == "forgotten" {
				n.echoes.forget(sidA, "m1")
			}
			if tc.change != "offline" {
				status := proto.StatusIdle
				if tc.change == "working" {
					status = proto.StatusWorking
				}
				seedAgents(t, n, f, claudeAgent(sidA, status, "w1:p1", "w1:t1", "w1", cwd, ""))
			}
			out, err := request(t, n, proto.MethodSessionEchoStatus, params)
			if err != nil || out["state"] != string(tc.want) {
				t.Fatalf("status %v, error %v, want %s", out, err, tc.want)
			}
		})
	}
}

func TestHeldEchoStatusWaitsForCodexTurnEnd(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, home+"/proj")
	now := time.Now()
	writeRollout(t, home, sidA, cwd, now, now, event("task_started"))
	seedAgents(t, n, f, agentOf("codex", sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
	base := n.echoBase(sidA)
	n.echoes.expect(echoRecord{SessionID: sidA, ClientMsgID: "m1", Text: "hello", SentAt: now,
		In: base.in, AfterSeq: base.seq, History: base.history})
	params := map[string]any{"session_id": sidA, "client_msg_id": "m1"}
	check := func(want proto.SessionEchoState) {
		t.Helper()
		out, err := request(t, n, proto.MethodSessionEchoStatus, params)
		if err != nil || out["state"] != string(want) {
			t.Fatalf("status %v, error %v, want %s", out, err, want)
		}
	}
	check(proto.EchoUnknown)
	path := n.transcriptPath(sidA)
	eventAt := func(kind string, at time.Time) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":%q}}`, at.UTC().Format(time.RFC3339Nano), kind)
	}
	appendEchoTestLine(t, path, eventAt("task_complete", now.Add(-time.Second)))
	check(proto.EchoUnknown)
	appendEchoTestLine(t, path, eventAt("task_started", now))
	appendEchoTestLine(t, path, eventAt("task_complete", now.Add(time.Second)))
	check(proto.EchoKnownUnseen)
	appendEchoTestLine(t, path, eventAt("task_started", now.Add(2*time.Second)))
	check(proto.EchoUnknown)
}

func TestHeldEchoGuardedSendReplacesOnlyUnseenAttempt(t *testing.T) {
	for _, name := range []string{"unseen", "line appeared after status check", "agent started a turn while the retry waited"} {
		t.Run(name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			cwd := mkdir(t, home+"/proj")
			now := time.Now()
			path := writeTranscript(t, home, cwd, sidA, now, assistantLine("older", now, "previous answer"))
			seedAgents(t, n, f, claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""))
			f.setScreens("w1:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi"))
			withSubmitGrace(t, 10*time.Millisecond)
			f.reply("agent.prompt", herdrFail{Code: "server_error", Message: "host reply lost"})
			p := map[string]any{"session_id": sidA, "text": "hello", "client_msg_id": "m1"}
			for range 2 {
				if _, err := request(t, n, "session.send", p); !maybeDelivered(err) {
					t.Fatalf("ordinary send should return the cached ambiguous reply, got %v", err)
				}
			}
			appendEchoTestLine(t, path, echoTurnEndLine(time.Now()))
			out, err := request(t, n, proto.MethodSessionEchoStatus, p)
			if err != nil || out["state"] != string(proto.EchoKnownUnseen) {
				t.Fatalf("status %v, error %v", out, err)
			}
			if name == "agent started a turn while the retry waited" {
				unlock := sync.OnceFunc(n.typing.lock(sidA))
				defer unlock()
				done := make(chan error, 1)
				go func() {
					_, err := request(t, n, proto.MethodSessionSendUnseen, p)
					done <- err
				}()
				waitFor(t, "the retry to wait for input", func() bool {
					_, canRetry := n.sends.completed("m1")
					return !canRetry
				})
				n.mu.Lock()
				a := n.agents[sidA]
				a.AgentStatus = proto.StatusWorking
				n.agents[sidA] = a
				n.mu.Unlock()
				unlock()
				if err := <-done; rpcCode(err) != proto.CodeEchoUnconfirmed {
					t.Fatalf("guarded retry while working: %v", err)
				}
				if _, err := request(t, n, "session.send", p); !maybeDelivered(err) {
					t.Fatalf("the ambiguous send receipt changed: %v", err)
				}
				if calls := callsOf(f, "agent.prompt"); len(calls) != 1 {
					t.Fatalf("prompted %d times, want 1", len(calls))
				}
				return
			}
			seen := name == "line appeared after status check"
			if seen {
				appendEchoTestLine(t, path, userLine("late", time.Now(), "hello"))
			}
			f.reply("agent.prompt", map[string]any{})
			if _, err := request(t, n, proto.MethodSessionSendUnseen, p); err != nil {
				t.Fatalf("guarded retry: %v", err)
			}
			_, err = request(t, n, proto.MethodSessionSendUnseen, p)
			want := 1
			if seen {
				if err != nil {
					t.Fatalf("seen reply: %v", err)
				}
			} else {
				want = 2
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != proto.CodeEchoUnconfirmed {
					t.Fatalf("repeat after acceptance: %v", err)
				}
			}
			if calls := callsOf(f, "agent.prompt"); len(calls) != want {
				t.Fatalf("prompted %d times, want %d", len(calls), want)
			}
		})
	}
}
