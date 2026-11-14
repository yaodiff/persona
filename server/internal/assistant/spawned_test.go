package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

var startedConvRe = regexp.MustCompile(`Started conversation (\S+);`)

// spawnHarness is an assistant with conversation parent-1 on its machine, the hub's log kept.
type spawnHarness struct {
	s      *Service
	f      *fakeHub
	mu     sync.Mutex
	logs   []string
	parent proto.Session
}

func newSpawnHarness(t *testing.T) *spawnHarness {
	t.Helper()
	h := &spawnHarness{f: newFakeHub().withMachine()}
	h.f.settings = &Settings{MachineID: "server", Timezone: "Asia/Shanghai"}
	st := &memStore{}
	h.s = withSettings(t, h.f, New(Options{Hub: h.f, Store: func() Store { return st }, Logf: func(format string, args ...any) {
		h.mu.Lock()
		h.logs = append(h.logs, fmt.Sprintf(format, args...))
		h.mu.Unlock()
	}}))
	h.parent = proto.Session{SessionID: "parent-1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	h.f.known["parent-1"] = true
	h.f.setSessions("server", h.parent)
	return h
}

func (h *spawnHarness) log() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

// spawn opens a conversation from parent-1 with conversation_new; its id.
func (h *spawnHarness) spawn(t *testing.T) string {
	t.Helper()
	res := h.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: "parent-1", Name: "conversation_new",
		Arguments: json.RawMessage(`{"text":"去查一下竞品的定价"}`)})
	if res.IsError || len(res.Content) == 0 {
		t.Fatalf("conversation_new: %+v", res)
	}
	m := startedConvRe.FindStringSubmatch(res.Content[0].Text)
	if m == nil {
		t.Fatalf("no id in %q", res.Content[0].Text)
	}
	return m[1]
}

// ended lists child as its turn ended and has the assistant see the turn settle.
func (h *spawnHarness) ended(child proto.Session) {
	h.f.setSessions("server", h.parent, child)
	h.s.assistantTurnSettled(context.Background(), "server", child.SessionID)
}

func (h *spawnHarness) noticesToParent() []string {
	var out []string
	for _, n := range h.f.noticesTo("parent-1") {
		if strings.Contains(n, "你用 conversation_new 开的对话") {
			out = append(out, n)
		}
	}
	return out
}

// #4669: a conversation conversation_new opened that fails leaves one notice in the conversation that opened it,
// with its id and what failed; the hub's log has the failure with the conversation and launcher, never the words.
func TestSpawnedConversationFailureTellsParent(t *testing.T) {
	h := newSpawnHarness(t)
	id := h.spawn(t)
	child := proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusDone, Launcher: "relay-claude",
		Title: "竞品定价", TurnError: "Invalid API key · Fix external API key", TurnErrorKind: proto.TurnErrorUser, UpdatedAt: time.Now()}
	h.ended(child)
	got := h.noticesToParent()
	if len(got) != 1 {
		t.Fatalf("notices to the parent: %q", got)
	}
	for _, want := range []string{id, "出错了", "Invalid API key"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("the notice lacks %q:\n%s", want, got[0])
		}
	}
	log := h.log()
	if !strings.Contains(log, "assistant "+id+": turn failed (launcher relay-claude, kind user") {
		t.Fatalf("no failure line in the log:\n%s", log)
	}
	if strings.Contains(log, "Invalid API key") {
		t.Fatalf("the log carries the error's words:\n%s", log)
	}
	// Once: the next failed turn tells nobody again.
	h.ended(child)
	if got := h.noticesToParent(); len(got) != 1 {
		t.Fatalf("told twice: %q", got)
	}
}

// One that ends a turn without ever reporting back leaves one notice too; one that reported back with
// conversation_send leaves none.
func TestSpawnedConversationSilentEndTellsParent(t *testing.T) {
	h := newSpawnHarness(t)
	id := h.spawn(t)
	h.ended(proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusDone, Title: "竞品定价",
		LastText: "查完了：三家都按席位收费。", UpdatedAt: time.Now()})
	got := h.noticesToParent()
	if len(got) != 1 || !strings.Contains(got[0], "一直没用 conversation_send 回报") || !strings.Contains(got[0], "三家都按席位收费") {
		t.Fatalf("notices to the parent: %q", got)
	}
	if strings.Contains(h.log(), "turn failed") {
		t.Fatalf("a reply logged as a failure:\n%s", h.log())
	}

	h2 := newSpawnHarness(t)
	id2 := h2.spawn(t)
	child := proto.Session{SessionID: id2, MachineID: "server", Assistant: true, Status: proto.StatusWorking, Title: "竞品定价"}
	h2.f.setSessions("server", h2.parent, child)
	if res := h2.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: id2, Name: "conversation_send",
		Arguments: json.RawMessage(`{"id":"parent-1","text":"三家都按席位收费"}`)}); res.IsError {
		t.Fatalf("conversation_send: %+v", res)
	}
	child.Status, child.LastText = proto.StatusDone, "发回去了。"
	h2.ended(child)
	if got := h2.noticesToParent(); len(got) != 0 {
		t.Fatalf("one that reported back was noticed: %q", got)
	}
}

// The notice is a message from the opened conversation, held to the relay's hop limit like its own
// conversation_send: its first message back is the answer to the opening request and keeps its hop (#7391), so one
// opened at the last hop still reports; a second message back is past the limit, the parent is not told, and the log
// says so.
func TestSpawnedConversationNoticeKeepsHopLimit(t *testing.T) {
	atLastHop := func(t *testing.T) (*spawnHarness, string) {
		h := newSpawnHarness(t)
		b := &h.s.relays
		b.mu.Lock()
		if !h.s.relayLocked(context.Background()) {
			b.mu.Unlock()
			t.Fatal("relay state")
		}
		b.st.Hops["parent-1"] = relayHop{Hop: relayMaxHops - 1, At: time.Now().UTC()} // the parent works on a relayed message
		b.mu.Unlock()
		return h, h.spawn(t) // the child is at the last hop
	}
	failed := func(id string) proto.Session {
		return proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusDone,
			TurnError: "API Error: 503 no eligible upstream", UpdatedAt: time.Now()}
	}

	h, id := atLastHop(t)
	h.ended(failed(id))
	if got := h.noticesToParent(); len(got) != 1 {
		t.Fatalf("the answer at the last hop: %q\n%s", got, h.log())
	}

	h, id = atLastHop(t)
	h.f.setSessions("server", h.parent, proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	if res := h.s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: id, Name: "conversation_send",
		Arguments: json.RawMessage(`{"id":"parent-1","text":"先回一句"}`)}); res.IsError {
		t.Fatalf("its answer: %+v", res)
	}
	h.ended(failed(id))
	if got := h.noticesToParent(); len(got) != 0 {
		t.Fatalf("told past the hop limit: %q", got)
	}
	if !strings.Contains(h.log(), "not told") {
		t.Fatalf("the log does not say the parent was not told:\n%s", h.log())
	}
}

// A conversation nobody opened with conversation_new is never followed; one still working is not at its end.
func TestSpawnedFollowOnlyOpenedOnes(t *testing.T) {
	h := newSpawnHarness(t)
	h.ended(proto.Session{SessionID: "own-1", MachineID: "server", Assistant: true, Status: proto.StatusDone, LastText: "好的"})
	id := h.spawn(t)
	h.ended(proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	if got := h.noticesToParent(); len(got) != 0 {
		t.Fatalf("told: %q", got)
	}
}

// #4669: an assistant turn that failed (the node's turn error, or an answer that is only Claude Code's auth error line)
// is pushed as a failure (⚠️), however quick it was, as other failures are.
func TestFailedAssistantTurnPushedAsFailure(t *testing.T) {
	for _, c := range []struct{ name, turnError, lastText string }{
		{"the node's turn error", "Invalid API key · Fix external API key", ""},
		{"an answer that is only the error", "", "Invalid API key · Fix external API key"},
	} {
		t.Run(c.name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			work := proto.Session{SessionID: "a1", Title: "竞品定价", Cwd: "/home/me/.persona/assistant", Assistant: true,
				Status: proto.StatusWorking, UpdatedAt: time.Now()}
			ft.sessions("mac", work)
			done := work
			done.Status, done.TurnError, done.LastText, done.UpdatedAt = proto.StatusDone, c.turnError, c.lastText, time.Now()
			ft.sessions("mac", done)
			got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
			if len(got) != 1 || !strings.HasPrefix(got[0].Title, "⚠️") || !strings.Contains(got[0].Body, "Invalid API key") || got[0].LongMS != 0 {
				t.Fatalf("pushes %+v", got)
			}
		})
	}
}

// The notices' prompts (prompts/assistant/spawn_notice.md, task_ended.md), pinned.
func TestGoldenSpawnAndTaskEndNotices(t *testing.T) {
	golden(t, "spawn_notice.failed", prompts.RenderSpawnNotice(prompts.SpawnNotice{ID: "conv-2", Title: "1110 | 研究 | 竞品定价",
		Link: "https://hub.example.com/persona/conv-2", Failed: true, Error: "Invalid API key · Fix external API key"}))
	golden(t, "spawn_notice.silent", prompts.RenderSpawnNotice(prompts.SpawnNotice{ID: "conv-2", Title: "1110 | 研究 | 竞品定价",
		Last: "查完了：三家都按席位收费。"}))
	golden(t, "task_ended.accepted", prompts.RenderTaskEnded(prompts.TaskEnded{ID: "todo_1", Title: "修登录崩溃",
		Link: "https://hub.example.com/todos?id=todo_1", Verdict: reviewAccepted, Note: "PR 已合并"}))
	golden(t, "task_ended.cancelled", prompts.RenderTaskEnded(prompts.TaskEnded{ID: "todo_1", Title: "修登录崩溃", Verdict: reviewCancelled}))
}
