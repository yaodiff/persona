package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// POST / PUT / GET /api/v1/assistant/tasks validate, preserve, and clear optional delivery anchors.
func TestTaskDeliveryAPIValidationAndBackwardCompatibility(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	setTasks(s)

	// Invalid delivery anchors are rejected with 400.
	for _, body := range []string{
		`{"title":"Daily","prompt":"Check","repeat":"daily","time":"09:00","enabled":true,"delivery":{"channel":"slack","chat_id":"oc_1","thread_id":"omt_1"}}`,
		`{"title":"Daily","prompt":"Check","repeat":"daily","time":"09:00","enabled":true,"delivery":{"channel":"feishu","chat_id":"","thread_id":"omt_1"}}`,
		`{"title":"Daily","prompt":"Check","repeat":"daily","time":"09:00","enabled":true,"delivery":{"channel":"feishu","chat_id":"oc_1","thread_id":""}}`,
		`{"title":"Daily","prompt":"Check","repeat":"daily","time":"09:00","enabled":true,"delivery":{"channel":"feishu","chat_id":"oc bad","thread_id":"omt_1"}}`,
	} {
		if w := request(s, "POST", "/api/v1/assistant/tasks", body); w.Code != 400 {
			t.Fatalf("bad delivery %s: status %d body %s", body, w.Code, w.Body.String())
		}
	}

	// Create a task with a valid Feishu thread delivery anchor.
	w := request(s, "POST", "/api/v1/assistant/tasks",
		`{"title":"Daily standup","prompt":"Summarize progress","repeat":"daily","time":"09:00","enabled":true,"delivery":{"channel":"feishu","chat_id":"oc_team","thread_id":"omt_standup"}}`)
	if w.Code != 200 {
		t.Fatalf("POST task: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		Task Task `json:"task"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Task.Delivery == nil || created.Task.Delivery.Channel != channel.Feishu ||
		created.Task.Delivery.ChatID != "oc_team" || created.Task.Delivery.ThreadID != "omt_standup" {
		t.Fatalf("created delivery = %+v", created.Task.Delivery)
	}

	// Legacy PUT omitting delivery preserves the saved anchor.
	w = request(s, "PUT", "/api/v1/assistant/tasks/"+created.Task.ID,
		`{"title":"Daily standup v2","prompt":"Summarize progress","repeat":"daily","time":"09:30","enabled":true}`)
	if w.Code != 200 {
		t.Fatalf("PUT legacy: %d %s", w.Code, w.Body.String())
	}
	var updated struct {
		Task Task `json:"task"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.Task.Delivery == nil || updated.Task.Delivery.ThreadID != "omt_standup" {
		t.Fatalf("legacy PUT dropped delivery: %+v", updated.Task.Delivery)
	}

	// PUT with delivery: null clears it.
	w = request(s, "PUT", "/api/v1/assistant/tasks/"+created.Task.ID,
		`{"title":"Daily standup v2","prompt":"Summarize progress","repeat":"daily","time":"09:30","enabled":true,"delivery":null}`)
	if w.Code != 200 {
		t.Fatalf("PUT clear: %d %s", w.Code, w.Body.String())
	}
	updated.Task = Task{}
	_ = json.Unmarshal(w.Body.Bytes(), &updated)
	if updated.Task.Delivery != nil {
		t.Fatalf("PUT null did not clear delivery: %+v", updated.Task.Delivery)
	}
}

// schedule_followup and schedule_routine accept and validate optional delivery parameters.
func TestScheduleToolsAcceptDeliveryAnchor(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	setTasks(s)

	if _, err := s.runTool(ctx, "server", "main", "schedule_followup", json.RawMessage(`{
		"at":"30m","title":"Check build","prompt":"Check CI status",
		"delivery":{"channel":"feishu","chat_id":"oc_ci","thread_id":"omt_ci"}
	}`)); err != nil {
		t.Fatalf("schedule_followup with delivery: %v", err)
	}
	if _, err := s.runTool(ctx, "server", "main", "schedule_routine", json.RawMessage(`{
		"title":"Morning report","prompt":"Write morning report","time":"09:00",
		"delivery":{"channel":"feishu","chat_id":"oc_ops","thread_id":"omt_morning"}
	}`)); err != nil {
		t.Fatalf("schedule_routine with delivery: %v", err)
	}
	if _, err := s.runTool(ctx, "server", "main", "schedule_routine", json.RawMessage(`{
		"title":"Bad","prompt":"Write report","time":"09:00",
		"delivery":{"channel":"unknown","chat_id":"oc_ops","thread_id":"omt_morning"}
	}`)); err == nil {
		t.Fatalf("expected invalid delivery error")
	}

	tasks := s.tasksView(ctx, nil)
	byTitle := map[string]Task{}
	for _, tk := range tasks {
		byTitle[tk.Title] = tk
	}
	if d := byTitle["Check build"].Delivery; d == nil || d.ChatID != "oc_ci" || d.ThreadID != "omt_ci" {
		t.Fatalf("followup delivery = %+v", d)
	}
	if d := byTitle["Morning report"].Delivery; d == nil || d.ChatID != "oc_ops" || d.ThreadID != "omt_morning" {
		t.Fatalf("routine delivery = %+v", d)
	}
}

// A scheduled task configured with a Feishu thread delivery anchor posts its finished result back
// into that thread (in_thread, even when private DM pushes are off) and binds the thread to its
// execution conversation so an owner reply in that thread continues the conversation directly.
func TestScheduledTaskThreadDeliveryPostsInThreadAndCapturesReply(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	e.features = FeishuFeatures{Main: true, Cards: true, Pushes: false} // private DM pushes off; thread delivery still works
	e.topicModes = map[string]assistantwire.TopicTodoMode{testChat: assistantwire.TopicAlwaysTodo}
	e.chats = []string{"oc_private", testChat}

	// Capture FeishuPost calls.
	var posts []map[string]any
	e.f.answerApp(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		posts = append(posts, p)
		return map[string]any{"message_id": "om_task_card"}, nil
	})

	// Seed a known anchor message in the source thread (as happens once the owner opened the topic).
	e.f.setItems(
		proto.Item{Role: "user", Kind: proto.KindText, Text: "Daily metrics patrol"},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "All services healthy. Error rate 0.02%."},
	)
	e.sessions("server",
		proto.Session{SessionID: "m1", Assistant: true},
		proto.Session{SessionID: "sched_conv_1", Assistant: true},
	)

	// Record a scheduled task whose execution conversation is sched_conv_1 and delivery anchor is testChat/testThread.
	// Use a root message ID as thread_id fallback, or seed anchor via an earlier message; test both via anchor lookup.
	setTasks(e.s, Task{
		ID:           "rt_metrics",
		Title:        "Daily metrics",
		Prompt:       "Check metrics",
		Repeat:       repeatDaily,
		Time:         "09:00",
		Enabled:      true,
		RunMachineID: "server",
		RunSessionID: "sched_conv_1",
		Delivery:     &TaskDelivery{Channel: channel.Feishu, ChatID: assistantwire.FeishuChatID(testChat), ThreadID: assistantwire.FeishuThreadID("om_origin_root")},
	})

	// Scheduled run finishes -> feishuMirror delivers into the source Feishu thread.
	e.s.feishuMirror(context.Background(), "server", "sched_conv_1")
	if len(posts) != 1 {
		t.Fatalf("expected 1 post into source thread, got %d: %+v", len(posts), posts)
	}
	post := posts[0]
	if post["chat_id"] != testChat || post["reply_to"] != "om_origin_root" || post["in_thread"] != true || post["no_detour"] != true {
		t.Fatalf("unexpected thread delivery params: %+v", post)
	}
	if card, _ := post["card"].(string); !strings.Contains(card, "All services healthy") {
		t.Fatalf("card missing task result: %s", card)
	}

	// Owner replies directly in that Feishu thread -> routed back to sched_conv_1 (not main chat, no to-do created).
	reply := topicEvent("om_followup_1", "Break down error rate by region")
	reply.Message.ThreadID = "om_origin_root"
	e.deliver(reply)

	h := onlyHandoff(t, e.f)
	if h.machine != "server" || h.session != "sched_conv_1" || !strings.Contains(h.text, "Break down error rate by region") {
		t.Fatalf("thread reply routed to %+v, want server/sched_conv_1", h)
	}
	if todos := e.list(); len(todos) != 0 {
		t.Fatalf("thread reply unexpectedly created to-dos: %+v", todos)
	}
}

// Thread delivery refuses to post into a group chat that is not opted in (not in FeishuStatus.Chats).
func TestScheduledTaskThreadDeliveryRefusesNonOptedInChat(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	e.features = FeishuFeatures{Main: true, Cards: true, Pushes: true}
	e.chats = []string{"oc_private"} // testChat is NOT opted in

	var posts []map[string]any
	e.f.answerApp(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		posts = append(posts, p)
		return map[string]any{"message_id": "om_card"}, nil
	})
	e.f.setItems(
		proto.Item{Role: "user", Kind: proto.KindText, Text: "Check"},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "Done"},
	)
	e.sessions("server", proto.Session{SessionID: "sched_conv_2", Assistant: true})
	setTasks(e.s, Task{
		ID:           "rt_denied",
		Title:        "Denied",
		Prompt:       "Check",
		Repeat:       repeatDaily,
		Time:         "09:00",
		Enabled:      true,
		RunMachineID: "server",
		RunSessionID: "sched_conv_2",
		Delivery:     &TaskDelivery{Channel: channel.Feishu, ChatID: assistantwire.FeishuChatID(testChat), ThreadID: assistantwire.FeishuThreadID("om_origin_root")},
	})

	e.s.feishuMirror(context.Background(), "server", "sched_conv_2")
	if len(posts) != 0 {
		t.Fatalf("posted into non-opted-in chat: %+v", posts)
	}
}
