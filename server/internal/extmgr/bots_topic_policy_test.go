package extmgr

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

func TestTopicTodoModeReadbackAndLegacyUpdate(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	put := func(body string, mode assistantwire.TopicTodoMode) {
		t.Helper()
		code, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
		if code != 200 || out["topic_chats"].([]any)[0].(map[string]any)["todo_mode"] != string(mode) {
			t.Fatalf("PUT %d: %v, want %s", code, out, mode)
		}
	}
	put(`{"topic_chats":[{"chat_id":"oc_example"}]}`, assistantwire.TopicAnswerOnly)
	put(`{"topic_chats":[{"chat_id":"oc_example","todo_mode":"main_decides"}]}`, assistantwire.TopicMainDecides)
	put(`{"topic_chats":[{"chat_id":"oc_example","name":"Renamed"}]}`, assistantwire.TopicMainDecides)
	code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_example","todo_mode":"invalid"}]}`)
	if code != 400 {
		t.Fatalf("invalid mode: PUT %d: %v", code, out)
	}
	st := w.feishu(t, assistantwire.FeishuStatusGet, nil)
	if modes, _ := st["topic_todo_modes"].(map[string]any); modes["oc_example"] != "main_decides" {
		t.Fatalf("assistant status: %v", st)
	}
	put(`{"topic_chats":[{"chat_id":"oc_example","todo_mode":"always_todo"}]}`, assistantwire.TopicAlwaysTodo)
	put(`{"topic_chats":[{"chat_id":"oc_example","todo_mode":"answer_only"}]}`, assistantwire.TopicAnswerOnly)
	if st := w.feishu(t, assistantwire.FeishuStatusGet, nil); st["topic_todo_modes"] != nil {
		t.Fatalf("assistant status did not return to default: %v", st)
	}
}

func TestTopicStrictPolicyReadbackAndLegacyUpdate(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	put := func(body string) map[string]any {
		t.Helper()
		code, out := w.do(t, "PUT", "/api/v1/bots/feishu", body)
		if code != 200 {
			t.Fatalf("PUT %d: %v", code, out)
		}
		return out["topic_chats"].([]any)[0].(map[string]any)
	}
	row := put(`{"topic_chats":[{"chat_id":"oc_strict","name":"Example group","require_owner_mention":true}]}`)
	if row["require_owner_mention"] != true || row["home"] == true {
		t.Fatalf("isolated strict enrollment: %v", row)
	}
	row = put(`{"topic_chats":[{"chat_id":"oc_strict","name":"Renamed"}]}`)
	if row["require_owner_mention"] != true {
		t.Fatalf("old client cleared policy: %v", row)
	}
	row = put(`{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":false}]}`)
	if row["home"] == true {
		t.Fatalf("reversing policy silently chose a home: %v", row)
	}
	row = put(`{"topic_chats":[{"chat_id":"oc_strict","home":true,"require_owner_mention":true}]}`)
	if row["home"] != true {
		t.Fatalf("explicit home: %v", row)
	}
	row = put(`{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":false}]}`)
	if row["require_owner_mention"] != false || row["home"] != true {
		t.Fatalf("reversal lost home: %v", row)
	}
	_, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	row = out["topic_chats"].([]any)[0].(map[string]any)
	if row["require_owner_mention"] != false {
		t.Fatalf("GET: %v", row)
	}
}

func TestTopicStrictPolicyBeforeCommandsAndHandoff(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":true},{"chat_id":"oc_topic"}]}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	total := w.api.total()
	for _, thread := range []string{"", "omt_bound"} {
		for _, body := range []string{"hello", "/topics off", "/topics home"} {
			w.send(t, groupEvent("om_drop_"+thread+body, "ou_me", body, "oc_strict", thread))
		}
	}
	w.quiet(t, total, "om_marker")
	if len(w.handed()) != 1 || !w.settings().topicChat("oc_strict") || w.settings().homeChat() != topicChat {
		t.Fatalf("denied input acted: %v %+v", w.handed(), w.settings().TopicChats)
	}
	event := botMessageEvent("om_allowed", "ou_me", "text", textContent("@_user_1 hello"), map[string]any{"chat_type": "group", "chat_id": "oc_strict", "thread_id": "omt_bound", "mentions": []any{map[string]any{"key": "@_user_1", "name": "Example bot", "id": map[string]string{"open_id": "ou_bot"}}}})
	w.send(t, event)
	w.send(t, event)
	w.send(t, groupEvent("om_last", "ou_me", "marker", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "last marker", func() bool { got := w.handed(); return len(got) > 0 && got[len(got)-1] == "om_last" })
	if got := w.handed(); len(got) != 3 || got[1] != "om_allowed" {
		t.Fatalf("deliveries %v", got)
	}
}

// Text, rich text and attachment captions all use the event's structured identity,
// never a display name, textual @, or the binding of an existing topic.
func TestTopicStrictPolicyMentionMatrix(t *testing.T) {
	needsLarkModule(t)
	for _, kind := range []string{"text", "post", "image", "file"} {
		t.Run(kind, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":true},{"chat_id":"oc_topic"}]}`)
			total := w.api.total()
			for i, tc := range []struct{ sender, mention string }{
				{"ou_me", ""}, {"ou_me", "all"}, {"ou_me", "ou_other_bot"}, {"ou_me", "ou_other_user"}, {"ou_stranger", "ou_bot"},
			} {
				extra := map[string]any{"chat_type": "group", "chat_id": "oc_strict", "thread_id": "omt_bound"}
				if tc.mention != "" {
					extra["mentions"] = []any{map[string]any{"key": "@_user_1", "name": "Persona", "id": map[string]string{"open_id": tc.mention}}}
				}
				content := textContent("@Persona @_user_1 /topics home")
				if kind == "post" {
					content = `{"en_us":{"title":"@Persona","content":[[{"tag":"text","text":"@Persona /topics home"}]]}}`
				}
				if kind == "image" {
					content = `{"image_key":"img_example","caption":"@Persona"}`
				}
				if kind == "file" {
					content = `{"file_key":"file_example","file_name":"@Persona.txt"}`
				}
				w.send(t, botMessageEvent(fmt.Sprintf("om_matrix_%d", i), tc.sender, kind, content, extra))
			}
			w.quiet(t, total, "om_matrix_marker")
			if got := w.handed(); len(got) != 1 {
				t.Fatalf("denied messages handed: %v", got)
			}
			for i, thread := range []string{"om_root", "omt_bound"} {
				if i == 0 {
					thread = "om_allow_0"
				}
				extra := map[string]any{"chat_type": "group", "chat_id": "oc_strict", "thread_id": thread, "mentions": []any{map[string]any{"key": "@_user_1", "name": "Example bot", "id": map[string]string{"open_id": "ou_bot"}}}}
				content := textContent("@_user_1 hello")
				if kind == "post" {
					content = `{"en_us":{"title":"Example","content":[[{"tag":"at","user_id":"ou_bot","user_name":"Example bot"},{"tag":"text","text":"hello"}]]}}`
				}
				if kind == "image" {
					content = `{"image_key":"img_example","caption":"@_user_1"}`
				}
				if kind == "file" {
					content = `{"file_key":"file_example","file_name":"example.txt","caption":"@_user_1"}`
				}
				event := botMessageEvent(fmt.Sprintf("om_allow_%d", i), "ou_me", kind, content, extra)
				w.send(t, event)
				w.send(t, event)
			}
			w.send(t, botMessageEvent("om_dm_control", "ou_me", "text", textContent("DM control"), nil))
			w.send(t, groupEvent("om_end", "ou_me", "marker", topicChat, "omt_1"))
			eventually(t, 5*time.Second, "matrix drained", func() bool { g := w.handed(); return len(g) > 0 && g[len(g)-1] == "om_end" })
			if got := w.handed(); len(got) != 5 || got[1] != "om_allow_0" || got[2] != "om_allow_1" || got[3] != "om_dm_control" {
				t.Fatalf("deliveries: %v", got)
			}
		})
	}
}

func TestTopicStrictPolicyUnknownIdentityAndQueuedSettings(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat, "oc_strict")
	// Queue the event before enabling the policy. The handler must read the policy
	// when it gets its turn, rather than retain an older settings snapshot.
	blocker := make(chan struct{})
	w.h.bots.mu.Lock()
	w.h.bots.line = blocker
	w.h.bots.mu.Unlock()
	w.send(t, groupEvent("om_queued", "ou_me", "hello", "oc_strict", "omt_bound"))
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_topic"},{"chat_id":"oc_strict","require_owner_mention":true}]}`)
	close(blocker)
	total := w.api.total()
	w.quiet(t, total, "om_queue_marker")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("queued stale policy: %v", got)
	}
	// Forget a successfully loaded identity and make its refetch fail. Only the
	// identity lookup may happen; no message/reaction or assistant call is allowed.
	w.api.mu.Lock()
	w.api.botInfoFail = true
	w.api.mu.Unlock()
	w.h.bots.mu.Lock()
	w.h.bots.info = botcard.BotInfo{}
	w.h.bots.mu.Unlock()
	before := len(w.handed())
	w.send(t, botMessageEvent("om_unknown", "ou_me", "text", textContent("@_user_1 /topics home"), map[string]any{"chat_type": "group", "chat_id": "oc_strict", "thread_id": "omt_bound", "mentions": []any{map[string]any{"key": "@_user_1", "id": map[string]string{"open_id": "ou_bot"}}}}))
	w.send(t, groupEvent("om_unknown_end", "ou_me", "marker", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "unknown drained", func() bool { g := w.handed(); return len(g) > before && g[len(g)-1] == "om_unknown_end" })
	if got := w.handed(); len(got) != before+1 || w.settings().homeChat() != topicChat {
		t.Fatalf("unknown identity acted: %v", got)
	}
}

// A failed durable write must not loosen the running gate or publish success.
type topicPolicyFailStore struct{ Store }

func (s topicPolicyFailStore) PutSetting(context.Context, string, any) error {
	return errors.New("synthetic settings failure")
}

func TestTopicStrictPolicyDurabilityAndFailedSave(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":true}]}`)
	var persisted FeishuBotSettings
	if ok, err := w.h.store.Setting(w.ctx, botSettingsKey, &persisted); err != nil || !ok {
		t.Fatalf("saved settings: %v %v", ok, err)
	}
	if !persisted.TopicChats[0].RequireOwnerMention || persisted.homeChat() != "" {
		t.Fatalf("persisted: %+v", persisted.TopicChats)
	}
	original := w.h.store
	w.h.store = topicPolicyFailStore{original}
	code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_strict","require_owner_mention":false}]}`)
	w.h.store = original
	if code == 200 || !w.settings().TopicChats[0].RequireOwnerMention {
		t.Fatalf("failed save applied: %d %+v", code, w.settings().TopicChats)
	}
	_, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if out["topic_chats"].([]any)[0].(map[string]any)["require_owner_mention"] != true {
		t.Fatalf("readback after failure: %v", out)
	}
}

func TestTopicStrictPolicyKeepsLegacyImplicitHome(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_topic","require_owner_mention":true}]}`)
	if got := w.settings().homeChat(); got != topicChat {
		t.Fatalf("tightening policy moved legacy home: %q", got)
	}
}
