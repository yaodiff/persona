package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

func TestChannelRouteKeyPartialClassification(t *testing.T) {
	var bindings threadBindingIndex
	for _, tc := range []struct {
		name              string
		kind              channel.ChatKind
		chat, topic, want string
	}{
		{"direct with stray topic", channel.ChatKindDirect, "chat", "topic", ""},
		{"topic without chat", channel.ChatKindTopic, " ", "topic", "legacy"},
		{"chat alone", "", "chat", "", "legacy"},
		{"future kind", "future", "chat", "", "legacy"},
		{"topic without topic ID", channel.ChatKindTopic, "chat", "", "legacy"},
		{"topic pair without kind", "", "chat", "topic", channelTopicRouteKey("chat", "topic")},
	} {
		for _, mode := range []channel.ThreadMode{channel.ThreadModePerTopic, channel.ThreadModePerGroupAndTopic} {
			m := channel.Message{ChatKind: tc.kind, ChatID: tc.chat, TopicID: tc.topic, Thread: " legacy "}
			if got := bindings.resolve("examplechat", m, mode, true).primary; got != tc.want {
				t.Errorf("%s (%s): got %q, want %q", tc.name, mode, got, tc.want)
			}
			m.Thread = ""
			if tc.want == "legacy" && bindings.resolve("examplechat", m, mode, true).primary != "" {
				t.Errorf("%s (%s): incomplete route without a legacy thread must use the default target", tc.name, mode)
			}
		}
	}
	if got := channelThreadIdleTTL(true, &channelPrefs{ThreadIdleHours: channel.MaxThreadIdleHours + 1}); got != channel.MaxThreadIdleHours*time.Hour {
		t.Errorf("loaded idle timeout = %v", got)
	}
}

func TestChannelRoutingDefaultsAcrossManagers(t *testing.T) {
	// Both managers see the same channel ID and capability but own separate settings and bindings.
	newManager := func(id string) (*Service, *fakeHub, *memStore) {
		_, f, _ := threadsChannelService(t, true)
		st := &memStore{}
		return withSettings(t, f, New(Options{ID: id, Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})), f, st
	}
	a, hubA, storeA := newManager("assistant-a")
	b, _, _ := newManager("assistant-b")
	check := func(s *Service, on bool, mode channel.ThreadMode, override, key string, idle int) {
		t.Helper()
		assertIntegrationsV2Flag(t, s, on)
		list := getChannelListByID(t, s)["examplechat"]
		d := getChannelDetail(t, s, "examplechat")
		if list.ThreadMode != mode || d.ThreadMode != mode || d.ThreadIdleHours != idle {
			t.Fatalf("%s: list mode %q, detail mode %q, idle %d", s.id, list.ThreadMode, d.ThreadMode, d.ThreadIdleHours)
		}
		if override == "" && d.TargetOverride != nil || override != "" && (d.TargetOverride == nil || d.TargetOverride.Mode != override) {
			t.Fatalf("%s: target override %+v, want %q", s.id, d.TargetOverride, override)
		}
		msg := channel.Message{Thread: "legacy", ChatID: "chat", TopicID: "topic"}
		binding, v, err := s.threadBindingForTest(context.Background(), "examplechat", msg)
		if err != nil || binding.primary != key || binding.isThread(v) != (key != "") {
			t.Fatalf("%s: route %q, want %q, err=%v", s.id, binding.primary, key, err)
		}
	}
	setIntegrationsV2ViaHTTP(t, a, true, false)
	topic := channelTopicRouteKey("chat", "topic")
	check(a, true, channel.ThreadModePerTopic, channel.TargetOverrideTopics, topic, 0)
	check(b, false, channel.ThreadModeChannelDefault, channel.TargetOverrideThreads, "legacy", 0)
	rec := channelAPIRequest(t, a, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"channel_default","thread_idle_hours":7}`)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	setIntegrationsV2ViaHTTP(t, b, true, false)
	setIntegrationsV2ViaHTTP(t, a, false, true)
	check(a, false, channel.ThreadModeChannelDefault, channel.TargetOverrideThreads, "legacy", 7)
	check(b, true, channel.ThreadModePerTopic, channel.TargetOverrideTopics, topic, 0)
	a = New(Options{ID: a.id, Hub: hubA, Store: func() Store { return storeA }, Logf: testLogf(t)})
	setIntegrationsV2ViaHTTP(t, a, true, false)
	check(a, true, channel.ThreadModeChannelDefault, "", "", 7)
}

func TestIntegrationsV2PartialAssistantPUT(t *testing.T) {
	s, _, _ := threadsChannelService(t, true)
	setIntegrationsV2ViaHTTP(t, s, true, false)
	for _, body := range []string{`{"notes":"updated"}`, `{"timezone":"UTC"}`, `{"todos_v2":true}`, `{"labs_seen":["todos_v2"]}`, `{"integrations_v2":null}`} {
		w, state := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", body)
		if w.Code != http.StatusOK || !state.IntegrationsV2 {
			t.Fatalf("PUT %s: %d %s", body, w.Code, w.Body.String())
		}
		assertIntegrationsV2Flag(t, s, true)
	}
}

func TestChannelRoutingPUTRequiresReadablePrefs(t *testing.T) {
	_, f, _ := threadsChannelService(t, true)
	st := &memStore{}
	st.setRawSetting(channelsKey, []byte(`{"examplechat":{"thread_mode":"per_topic"}}`))
	st.failKey(channelsKey, errors.New("temporarily unavailable"))
	s := newServiceOn(t, f, st)
	put := func() int {
		return channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_idle_hours":8}`).Code
	}
	if got := put(); got != http.StatusServiceUnavailable {
		t.Fatalf("PUT with unreadable prefs: %d", got)
	}
	st.failKey(channelsKey, nil)
	if got := put(); got != http.StatusOK {
		t.Fatalf("retry: %d", got)
	}
	if d := getChannelDetail(t, newServiceOn(t, f, st), "examplechat"); d.ThreadIdleHours != 8 || d.ThreadMode != channel.ThreadModePerTopic {
		t.Fatalf("reloaded: %+v", d.View)
	}
}

func TestMentionChannelRoutingAndIdleHoursHTTP(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "mention", Kind: channel.KindMention, Source: channel.SourcePlugin, Installed: true, Capabilities: channel.Capabilities{Threads: true}}}}, nil
	})
	setIntegrationsV2ViaHTTP(t, s, true, false)
	if d := getChannelDetail(t, s, "mention"); d.ThreadMode != channel.ThreadModePerTopic || d.Target == nil || d.Target.Kind != channel.TargetNew {
		t.Fatal(d.View)
	}
	for _, tc := range []struct {
		value string
		code  int
	}{{"87600", 200}, {"87601", 400}, {"1.5", 400}, {"null", 200}} {
		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/mention", "full", fmt.Sprintf(`{"thread_idle_hours":%s}`, tc.value))
		if rec.Code != tc.code {
			t.Fatalf("PUT %s: %d %s", tc.value, rec.Code, rec.Body.String())
		}
		if d := getChannelDetail(t, s, "mention"); d.ThreadIdleHours != channel.MaxThreadIdleHours || d.ThreadMode != channel.ThreadModePerTopic {
			t.Fatalf("after PUT %s: %+v", tc.value, d.View)
		}
	}
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "mention", Message: json.RawMessage(`{"id":"mention-1","owner":true,"text":"hello","chat_id":"chat","topic_id":"topic"}`)}); err != nil {
		t.Fatal(err)
	}
	waitAdmissionDrained(t, s)
	if got := boundThreadFor(s, "mention", channelTopicRouteKey("chat", "topic")); got == "" {
		t.Fatal("owner mention did not start a topic conversation")
	}
}

func TestFeishuTopicRoutingAcrossIntegrationsToggle(t *testing.T) {
	needsLarkLayer(t)
	e := newThreadEnv(t, nil)
	// The extension manager supplies this override for an opted-in topic chat.
	e.f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{
			"channels":         []channel.View{{ID: channel.Feishu, Kind: channel.KindIM, Source: channel.SourceBuiltin, Installed: true}},
			"target_overrides": map[string]*channel.TargetOverride{channel.Feishu: {Mode: channel.TargetOverrideTopics}},
		}, nil
	})
	putTodo(e.s, Todo{ID: "topic-todo", Title: "Topic", Origin: "c1x1x1", Thread: &TodoThread{Channel: channel.Feishu, ChatID: testChat, ThreadID: testThread}})
	for i, on := range []bool{false, true, false} {
		setIntegrationsV2(t, e.s, on)
		rec := channelAPIRequest(t, e.s, http.MethodGet, "/api/v1/channels/feishu", "full", "")
		var fields map[string]json.RawMessage
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &fields) != nil || fields["thread_mode"] != nil {
			t.Fatalf("Feishu detail: %d %s", rec.Code, rec.Body.String())
		}
		var ov channel.TargetOverride
		if err := json.Unmarshal(fields["target_override"], &ov); err != nil || ov.Mode != channel.TargetOverrideTopics {
			t.Fatalf("Feishu override: %+v, %v", ov, err)
		}
		rec = channelAPIRequest(t, e.s, http.MethodPut, "/api/v1/channels/feishu", "full", `{"thread_idle_hours":12}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("Feishu PUT: %d", rec.Code)
		}
		raw, err := json.Marshal(topicEvent(fmt.Sprintf("topic-%d", i), "topic reply"))
		if err != nil {
			t.Fatal(err)
		}
		if err := e.s.FeishuMessage(context.Background(), FeishuMessage{Event: raw}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "Feishu topic handoff", func() bool { return len(handoffs(e.f)) == i+1 })
		if got := handoffs(e.f)[i]; got.machine != "server" || got.session != "c1x1x1" {
			t.Fatalf("v2=%t: handed off to %+v", on, got)
		}
	}
}
