package extmgr

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

func TestTopicSpaceReadbackValidationAndLegacyUpdate(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	archived := time.Unix(1_700_000_000, 0)
	w.st.tracks = []db.Track{
		{Slug: "research", Title: "Research", Template: "space", DirName: "tracks/research"},
		{Slug: "archived-space", Title: "Archived", Template: "space", DirName: "tracks/archived-space", ArchivedAt: &archived},
		{Slug: "builtin-space", Title: "Builtin", Template: "space", Builtin: true},
	}

	putRow := func(body string) map[string]any {
		t.Helper()
		code, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
		if code != 200 {
			t.Fatalf("PUT %d: %v", code, out)
		}
		return out["topic_chats"].([]any)[0].(map[string]any)
	}

	// Bind a valid domain space alongside answer_only mode.
	row := putRow(`{"topic_chats":[{"chat_id":"oc_space","name":"Research group","todo_mode":"answer_only","space":"research"}]}`)
	if row["space"] != "research" || row["todo_mode"] != string(assistantwire.TopicAnswerOnly) {
		t.Fatalf("bound row: %v", row)
	}
	st := w.feishu(t, assistantwire.FeishuStatusGet, nil)
	if spaces, _ := st["topic_spaces"].(map[string]any); spaces["oc_space"] != "research" {
		t.Fatalf("assistant status topic_spaces: %v", st)
	}

	// Older client omitting space preserves the existing binding.
	row = putRow(`{"topic_chats":[{"chat_id":"oc_space","name":"Renamed"}]}`)
	if row["space"] != "research" {
		t.Fatalf("legacy PUT cleared space: %v", row)
	}

	// Built-in health space is also accepted.
	row = putRow(`{"topic_chats":[{"chat_id":"oc_space","space":"health"}]}`)
	if row["space"] != "health" {
		t.Fatalf("health space: %v", row)
	}

	// Invalid, archived, builtin, or malformed spaces are rejected with 400 and keep previous state.
	for _, bad := range []string{"nonexistent", "archived-space", "builtin-space", "../escape", "bad space"} {
		code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_space","space":"`+bad+`"}]}`)
		if code != 400 {
			t.Fatalf("space %q: PUT %d: %v, want 400", bad, code, out)
		}
	}
	_, getOut := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if got := getOut["topic_chats"].([]any)[0].(map[string]any)["space"]; got != "health" {
		t.Fatalf("state changed after rejected PUT: %v", got)
	}

	// Explicit empty string clears the space binding.
	row = putRow(`{"topic_chats":[{"chat_id":"oc_space","space":""}]}`)
	if _, exists := row["space"]; exists && row["space"] != "" {
		t.Fatalf("space not cleared: %v", row)
	}
	st = w.feishu(t, assistantwire.FeishuStatusGet, nil)
	if st["topic_spaces"] != nil {
		t.Fatalf("topic_spaces should be omitted when empty: %v", st)
	}
}

func TestTopicsSpaceCommand(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.st.tracks = []db.Track{
		{Slug: "ops", Title: "Ops", Template: "space", DirName: "tracks/ops"},
	}

	// /topics space <slug> opts the group in and binds the space in one step.
	before := w.api.total()
	w.send(t, groupEvent("om_sp1", "ou_me", "/topics space ops", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "topics space reply", func() bool { return w.api.total() > before })
	if got := w.settings().TopicChats; len(got) != 1 || got[0].ChatID != topicChat || got[0].Space != "ops" {
		t.Fatalf("topic chats after /topics space ops: %+v", got)
	}

	// Unknown space is rejected without changing the existing binding.
	before = w.api.total()
	w.send(t, groupEvent("om_sp_bad", "ou_me", "/topics space missing", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "topics space bad reply", func() bool { return w.api.total() > before })
	if got := w.settings().TopicChats; len(got) != 1 || got[0].Space != "ops" {
		t.Fatalf("topic chats changed on unknown space: %+v", got)
	}

	// /topics space off (and Chinese alias) clears the space while keeping the group opted in.
	before = w.api.total()
	w.send(t, groupEvent("om_sp_off", "ou_me", "/topics space off", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "topics space off reply", func() bool { return w.api.total() > before })
	if got := w.settings().TopicChats; len(got) != 1 || got[0].ChatID != topicChat || got[0].Space != "" {
		t.Fatalf("topic chats after /topics space off: %+v", got)
	}

	// Chinese command binds again.
	before = w.api.total()
	w.send(t, groupEvent("om_sp_zh", "ou_me", "/话题群 空间 ops", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "topics space zh reply", func() bool { return w.api.total() > before })
	if got := w.settings().TopicChats; len(got) != 1 || got[0].Space != "ops" {
		t.Fatalf("topic chats after Chinese space bind: %+v", got)
	}

	// Audit records include bot_topic_space.
	foundAudit := false
	w.st.mu.Lock()
	for _, row := range w.st.audit {
		if row.Action == auditBotTopicSpace && row.Detail["space"] == "ops" {
			foundAudit = true
			break
		}
	}
	w.st.mu.Unlock()
	if !foundAudit {
		t.Fatalf("missing %s audit record", auditBotTopicSpace)
	}
}
