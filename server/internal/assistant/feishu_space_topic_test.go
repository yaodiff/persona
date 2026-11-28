package assistant

import (
	"context"
	"encoding/json"
	"path"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func newSpaceThreadEnv(t *testing.T, spaceSlug string) (*threadEnv, *fleetDevs) {
	t.Helper()
	st := &trackKindMemStore{trackRegistryStore: &trackRegistryStore{memStore: &memStore{}, rows: []db.Track{
		{Slug: spaceSlug, Kind: trackKindSpace, Title: "Research Space", Template: trackTemplateSpace, Revision: 1},
	}}}
	env := newTodoEnv(t, st)
	env.f.devices = []Device{
		{ID: "server", Name: "server", Kind: "local", Home: "/home/me", Online: true},
		{ID: "box", Name: "box", Kind: "remote", Home: "/home/box", Online: true},
	}
	fd := newFleetDevs()
	forward := func(dev string) func(string, json.RawMessage) (any, error) {
		return func(method string, params json.RawMessage) (any, error) {
			raw, err := env.f.DeviceCall(context.Background(), DeviceCall{Device: dev, Method: method, Params: params})
			if err != nil || len(raw) == 0 {
				return map[string]any{}, err
			}
			return raw, nil
		}
	}
	srv := fd.add("server", "server")
	srv.home = "/home/me"
	srv.call = forward("server")
	box := fd.add("box", "box")
	box.call = forward("box")
	env.s.devs = spaceDevs{fleetDevs: fd}
	e := withFeishuThread(env)
	fd.setSessions("server", []proto.Session{
		{SessionID: "m1", MachineID: "server", Cwd: "/home/me/.persona/assistant", Assistant: true},
		{SessionID: "c1x1x1", MachineID: "server", Cwd: "/home/me/.persona/assistant", Assistant: true},
	})
	fd.setSessions("box", []proto.Session{{SessionID: "agent01", MachineID: "box", Title: "修发版"}})
	e.topicModes = map[string]assistantwire.TopicTodoMode{testChat: assistantwire.TopicAnswerOnly}
	e.topicSpaces = map[string]string{testChat: spaceSlug}
	return e, fd
}

func spaceTopicEvent(id, threadID, text string, root bool) botcard.MessageEvent {
	ev := topicEvent(id, text)
	ev.Message.ThreadID = threadID
	if root {
		ev.Message.RootID, ev.Message.ParentID = "", ""
	}
	return ev
}

// A topic group bound to a domain space creates one isolated conversation per topic inside that space
// (inheriting its directory, SPACE.md and space context), routes follow-up replies back to the same
// conversation, creates no to-do and never disturbs the main conversation.
func TestFeishuSpaceBoundTopicGroupOneConversationPerTopic(t *testing.T) {
	needsLarkLayer(t)
	e, fd := newSpaceThreadEnv(t, "research")

	// Topic 1 first message -> starts a new conversation in space "research".
	e.deliver(spaceTopicEvent("om_t1_1", "omt_topic_1", "Summarize the benchmark notes", true))

	// No to-do created and main chat m1 was never sent to.
	if todoCount(e.s) != 0 {
		t.Fatalf("unexpected to-dos created: %d", todoCount(e.s))
	}
	for _, h := range handoffs(e.f) {
		if h.session == "m1" {
			t.Fatalf("space topic disturbed main conversation: %+v", h)
		}
	}

	// Prepare was called with the space's directory and space payload.
	e.f.mu.Lock()
	prepared := append([]map[string]any(nil), e.f.prepared...)
	sends := append([]map[string]any(nil), e.f.sends...)
	e.f.mu.Unlock()
	if len(prepared) == 0 {
		t.Fatalf("expected assistant.prepare for space conversation")
	}
	wantDir := path.Join(compat.StateDir, "tracks/research")
	foundSpacePrepare := false
	for _, p := range prepared {
		sp, _ := p["space"].(map[string]any)
		if p["dir"] == wantDir && sp["slug"] == "research" {
			foundSpacePrepare = true
			break
		}
	}
	if !foundSpacePrepare {
		t.Fatalf("assistant.prepare did not target space directory %q: %+v", wantDir, prepared)
	}
	if len(sends) != 1 {
		t.Fatalf("expected 1 new space conversation start, got %d: %+v", len(sends), sends)
	}

	runs := e.s.channelRuns(channel.Feishu)
	if len(runs) != 1 || runs[0].SessionID == "" || runs[0].SessionID == "m1" {
		t.Fatalf("expected isolated space run, got %+v", runs)
	}
	sess1 := runs[0].SessionID
	if runs[0].Ext["chat"] != testChat || runs[0].Ext["thread"] != "omt_topic_1" {
		t.Fatalf("run ext missing topic metadata: %+v", runs[0].Ext)
	}

	// Register the newly started session on the fake node so follow-up lookup finds it.
	sessList := []proto.Session{
		{SessionID: "m1", MachineID: "server", Cwd: "/home/me/.persona/assistant", Assistant: true},
		{SessionID: sess1, MachineID: "server", Cwd: "/home/me/.persona/tracks/research", Assistant: true, Space: "research"},
	}
	fd.setSessions("server", sessList)
	e.sessions("server", sessList...)

	// Topic 1 second message -> reuses sess1 via DeviceSessionSendOnce instead of starting a new conversation.
	e.deliver(spaceTopicEvent("om_t1_2", "omt_topic_1", "Add latency numbers too", false))
	gotHandoffs := handoffs(e.f)
	if len(gotHandoffs) != 1 || gotHandoffs[0].session != sess1 || !strings.Contains(gotHandoffs[0].text, "Add latency numbers too") {
		t.Fatalf("follow-up in topic 1 did not reuse %s: %+v", sess1, gotHandoffs)
	}
	e.f.mu.Lock()
	sendsAfterReply := len(e.f.sends)
	e.f.mu.Unlock()
	if sendsAfterReply != 1 {
		t.Fatalf("follow-up in topic 1 unexpectedly started a new conversation (sends=%d)", sendsAfterReply)
	}

	// Topic 2 first message -> starts a distinct isolated conversation in the same space.
	e.deliver(spaceTopicEvent("om_t2_1", "omt_topic_2", "Draft next week's agenda", true))
	e.f.mu.Lock()
	sendsAfterTopic2 := len(e.f.sends)
	e.f.mu.Unlock()
	if sendsAfterTopic2 != 2 {
		t.Fatalf("topic 2 did not start a second space conversation (sends=%d)", sendsAfterTopic2)
	}
	var sess2 string
	for _, r := range e.s.channelRuns(channel.Feishu) {
		if r.Ext["thread"] == "omt_topic_2" {
			sess2 = r.SessionID
			break
		}
	}
	if sess2 == "" || sess2 == sess1 || sess2 == "m1" {
		t.Fatalf("topic 2 session = %q, want distinct from %q and m1", sess2, sess1)
	}

	// Private DM target was not altered by topic-group space conversations.
	if cur := e.s.larkModule().Route("", ""); cur.Kind != targetMain {
		t.Fatalf("private chat target mutated to %+v", cur)
	}
}

// Explicit /todo still opens a to-do in a space-bound topic group when the owner asks for one.
func TestFeishuSpaceBoundTopicExplicitTodoStillWorks(t *testing.T) {
	needsLarkLayer(t)
	e, _ := newSpaceThreadEnv(t, "research")

	e.deliver(spaceTopicEvent("om_todo_1", "omt_todo_topic", "/todo Track release checklist", true))
	if todoCount(e.s) != 1 {
		t.Fatalf("expected 1 to-do from explicit /todo, got %d (notes=%v)", todoCount(e.s), topicNotes(e.f))
	}
	todo := onlyTodo(e.s)
	if todo.Thread == nil || todo.Thread.ThreadID != "omt_todo_topic" {
		t.Fatalf("to-do not bound to topic: %+v", todo.Thread)
	}
}

// A topic group without a space binding keeps its legacy answer_only routing to the main conversation.
func TestFeishuUnboundTopicWithoutSpaceKeepsMainChatRouting(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	e.topicModes = map[string]assistantwire.TopicTodoMode{testChat: assistantwire.TopicAnswerOnly}
	e.topicSpaces = nil

	e.deliver(spaceTopicEvent("om_legacy_1", "omt_legacy", "Quick check", true))
	h := onlyHandoff(t, e.f)
	if h.session != "m1" {
		t.Fatalf("legacy answer_only group routed to %q, want m1", h.session)
	}
	// Ensure no unexpected space conversation was started.
	e.f.mu.Lock()
	sends := len(e.f.sends)
	e.f.mu.Unlock()
	if sends != 0 {
		t.Fatalf("legacy group started %d new conversations, want 0", sends)
	}
	// Ensure no stray error note was posted.
	if notes := topicNotes(e.f); len(notes) != 0 {
		b, _ := json.Marshal(notes)
		t.Fatalf("unexpected notes: %s", string(b))
	}
}
