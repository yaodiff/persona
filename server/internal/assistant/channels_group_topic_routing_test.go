package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func setIntegrationsV2(t *testing.T, s *Service, on bool) {
	t.Helper()
	setSettings(t, s, func(v *Settings) {
		b := on
		v.IntegrationsV2 = &b
	})
}

func channelAPIRequest(t *testing.T, s *Service, method, path, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestChannelThreadModeAndIdleHoursAPIRoundTripAndValidation(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true})
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{
			{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{MaxText: 4000, Threads: true, Push: true, Asks: channel.AsksText},
			},
			{
				ID: channel.Feishu, Name: "Lark bot", Kind: channel.KindIM, Source: channel.SourceBuiltin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{Threads: true},
			},
		}}, nil
	})
	s := newServiceOn(t, f, st)

	// Default GET detail exposes thread_mode=channel_default and thread_idle_hours=0.
	rec := channelAPIRequest(t, s, http.MethodGet, "/api/v1/channels/examplechat", "full", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET detail: %d %s", rec.Code, rec.Body.String())
	}
	var detail channel.Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ThreadMode != channel.ThreadModeChannelDefault || detail.ThreadIdleHours != 0 {
		t.Fatalf("default detail: thread_mode=%q thread_idle_hours=%d", detail.ThreadMode, detail.ThreadIdleHours)
	}

	// Read-only device cannot update channel settings.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "read", `{"thread_mode":"per_topic"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("read-only PUT: %d, want 403", rec.Code)
	}

	// Update thread_mode and thread_idle_hours.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full",
		`{"thread_mode":"per_group_and_topic","thread_idle_hours":12}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT valid: %d %s", rec.Code, rec.Body.String())
	}
	detail = channel.Detail{}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ThreadMode != channel.ThreadModePerGroupAndTopic || detail.ThreadIdleHours != 12 {
		t.Fatalf("after PUT: thread_mode=%q thread_idle_hours=%d", detail.ThreadMode, detail.ThreadIdleHours)
	}

	// Legacy partial PUT (without thread_mode/thread_idle_hours) preserves saved values losslessly.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"features":{"sync":true}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy partial PUT: %d %s", rec.Code, rec.Body.String())
	}
	detail = channel.Detail{}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.ThreadMode != channel.ThreadModePerGroupAndTopic || detail.ThreadIdleHours != 12 {
		t.Fatalf("legacy PUT clobbered thread config: thread_mode=%q thread_idle_hours=%d", detail.ThreadMode, detail.ThreadIdleHours)
	}

	// Invalid thread_mode and negative / overflow thread_idle_hours return 400 invalid without mutating saved state.
	for _, badBody := range []string{
		`{"thread_mode":""}`,
		`{"thread_mode":"invalid_mode"}`,
		`{"thread_idle_hours":-1}`,
		`{"thread_idle_hours":99999999}`,
	} {
		rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", badBody)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("PUT %s: got %d, want 400; body=%s", badBody, rec.Code, rec.Body.String())
		}
		var errResp struct {
			Code  string `json:"code"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &errResp)
		if errResp.Code != "invalid" || errResp.Error == "" {
			t.Fatalf("PUT %s: unexpected error payload %s", badBody, rec.Body.String())
		}
	}

	// Built-in Feishu rejects plugin thread_mode / thread_idle_hours.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/feishu", "full", `{"thread_mode":"per_topic"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT feishu thread_mode: got %d, want 400", rec.Code)
	}

	// Reload from store preserves persisted values and GET /api/v1/channels lists them.
	reloaded := newServiceOn(t, f, st)
	rec = channelAPIRequest(t, reloaded, http.MethodGet, "/api/v1/channels", "full", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list after reload: %d %s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Channels []channel.View `json:"channels"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	var found *channel.View
	for i := range listResp.Channels {
		if listResp.Channels[i].ID == "examplechat" {
			found = &listResp.Channels[i]
			break
		}
	}
	if found == nil || found.ThreadMode != channel.ThreadModePerGroupAndTopic || found.ThreadIdleHours != 12 {
		t.Fatalf("reloaded list view = %+v", found)
	}

	// Resetting thread_idle_hours back to 0 and thread_mode to per_topic works cleanly.
	rec = channelAPIRequest(t, reloaded, http.MethodPut, "/api/v1/channels/examplechat", "full",
		`{"thread_mode":"per_topic","thread_idle_hours":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT reset: %d %s", rec.Code, rec.Body.String())
	}
	detail = channel.Detail{}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.ThreadMode != channel.ThreadModePerTopic || detail.ThreadIdleHours != 0 {
		t.Fatalf("after reset: thread_mode=%q thread_idle_hours=%d", detail.ThreadMode, detail.ThreadIdleHours)
	}
}

func TestChannelRoutingGatedByIntegrationsV2AndLosslessRollback(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	s.setChannelTestClock(clock.Now)

	// Configure thread_mode and thread_idle_hours while integrations_v2 is OFF.
	rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full",
		`{"thread_mode":"per_group_and_topic","thread_idle_hours":1}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT config: %d %s", rec.Code, rec.Body.String())
	}

	// integrations_v2 == false: ignores thread_mode and chat_id/chat_kind/topic_id; uses 100% legacy m.Thread + 30d TTL.
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "off_group_no_thread", Text: "group msg", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
	}); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 0 || lastSendOnceSession(f) != "m1" {
		t.Fatalf("integrations_v2=false routed structured group without m.Thread away from main: starts=%d session=%q", f.sent(), lastSendOnceSession(f))
	}

	// Legacy m.Thread works as before and stays alive past 1h (thread_idle_hours=1 is ignored while off).
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "off_legacy_1", Text: "legacy 1", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:t1",
	}); err != nil {
		t.Fatal(err)
	}
	legacySess := boundThread(s, "legacy:t1")
	if legacySess == "" || f.sent() != 1 {
		t.Fatalf("legacy start failed: starts=%d sess=%q", f.sent(), legacySess)
	}
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: legacySess, MachineID: "server", Assistant: true},
	)
	clock.advance(2 * time.Hour)
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "off_legacy_2", Text: "legacy 2", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:t1",
	}); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 1 || lastSendOnceSession(f) != legacySess {
		t.Fatalf("integrations_v2=false rotated legacy thread after 2h: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacySess)
	}

	// Toggle integrations_v2 ON -> persisted thread_mode=per_group_and_topic & thread_idle_hours=1 immediately take effect.
	setIntegrationsV2(t, s, true)
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "on_group_1", Text: "group 1", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:per_msg_1",
	}); err != nil {
		t.Fatal(err)
	}
	groupKey := channelGroupRouteKey("oc_g1")
	groupSess := boundThread(s, groupKey)
	if groupSess == "" || groupSess == legacySess || f.sent() != 2 {
		t.Fatalf("integrations_v2=true did not start group conversation: starts=%d groupSess=%q", f.sent(), groupSess)
	}
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: legacySess, MachineID: "server", Assistant: true},
		proto.Session{SessionID: groupSess, MachineID: "server", Assistant: true},
	)

	// Toggle integrations_v2 back OFF -> lossless rollback:
	// 1. Saved thread_mode & thread_idle_hours remain intact on GET detail.
	// 2. Legacy m.Thread routing + 30d TTL resumes immediately.
	setIntegrationsV2(t, s, false)
	rec = channelAPIRequest(t, s, http.MethodGet, "/api/v1/channels/examplechat", "full", "")
	var detail channel.Detail
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.ThreadMode != channel.ThreadModePerGroupAndTopic || detail.ThreadIdleHours != 1 {
		t.Fatalf("rollback lost persisted settings: mode=%q idle=%d", detail.ThreadMode, detail.ThreadIdleHours)
	}
	clock.advance(5 * time.Hour) // > 1h idle, still < 30d
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "off_again_legacy", Text: "back to legacy", Thread: "legacy:t1",
	}); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 2 || lastSendOnceSession(f) != legacySess {
		t.Fatalf("after rollback off: starts=%d session=%q want legacy %q", f.sent(), lastSendOnceSession(f), legacySess)
	}

	// Toggle integrations_v2 ON again -> persisted config still active; groupSess is now idle > 1h so it rotates.
	setIntegrationsV2(t, s, true)
	rec = channelAPIRequest(t, s, http.MethodGet, "/api/v1/channels/examplechat", "full", "")
	detail = channel.Detail{}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.TargetOverride == nil || detail.TargetOverride.Mode != channel.TargetOverrideGroupsAndTopics {
		t.Fatalf("integrations_v2=true per_group_and_topic target_override = %+v, want groups_and_topics", detail.TargetOverride)
	}
	if err := s.channelForward(ctx, "examplechat", channel.Message{
		ID: "on_again_rotated", Text: "rotated group", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
	}); err != nil {
		t.Fatal(err)
	}
	rotatedSess := boundThread(s, groupKey)
	if f.sent() != 3 || rotatedSess == "" || rotatedSess == groupSess {
		t.Fatalf("re-enabled integrations_v2 did not rotate idle group: starts=%d new=%q old=%q", f.sent(), rotatedSess, groupSess)
	}

	// Switching to channel_default while integrations_v2=true clears target_override.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"channel_default"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT channel_default: %d %s", rec.Code, rec.Body.String())
	}
	detail = channel.Detail{}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.TargetOverride != nil {
		t.Fatalf("integrations_v2=true channel_default target_override = %+v, want nil", detail.TargetOverride)
	}
}

func TestChannelRoutingModesWhenIntegrationsV2Enabled(t *testing.T) {
	t.Run("channel_default routes all chat kinds to channel target", func(t *testing.T) {
		s, f, _ := threadsChannelService(t, true)
		setIntegrationsV2(t, s, true)
		ctx := context.Background()

		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"channel_default"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT channel_default: %d %s", rec.Code, rec.Body.String())
		}
		for _, msg := range []channel.Message{
			{ID: "d1", Text: "direct", ChatID: "oc_dm", ChatKind: channel.ChatKindDirect},
			{ID: "g1", Text: "group", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup},
			{ID: "t1", Text: "topic", ChatID: "oc_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_1"},
			{ID: "l1", Text: "legacy", Thread: "legacy:t1"},
		} {
			if err := s.channelForward(ctx, "examplechat", msg); err != nil {
				t.Fatalf("%s: %v", msg.ID, err)
			}
			if f.sent() != 0 || lastSendOnceSession(f) != "m1" {
				t.Fatalf("msg=%s: starts=%d session=%q, want 0 starts and m1", msg.ID, f.sent(), lastSendOnceSession(f))
			}
		}
	})

	t.Run("per_group_and_topic splits topics, merges regular groups, defaults directs, and falls back to legacy Thread", func(t *testing.T) {
		s, f, sent := threadsChannelService(t, false) // works even without legacy capabilities.threads
		setIntegrationsV2(t, s, true)
		ctx := context.Background()

		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"per_group_and_topic"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT per_group_and_topic: %d %s", rec.Code, rec.Body.String())
		}

		registerSession := func(id string) {
			f.mu.Lock()
			f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: id, MachineID: "server", Assistant: true})
			f.mu.Unlock()
		}

		// 1. Topic 1 in group A.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_t1_1", Text: "topic 1 start", ChatID: "oc_gA", ChatKind: channel.ChatKindTopic, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		topicA1 := boundThread(s, channelTopicRouteKey("oc_gA", "omt_1"))
		if topicA1 == "" || f.sent() != 1 {
			t.Fatalf("topicA1=%q starts=%d", topicA1, f.sent())
		}
		registerSession(topicA1)

		// Follow-up in same (oc_gA, omt_1) continues topicA1 (even if plugin sets chat_kind=group with topic_id).
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_t1_2", Text: "topic 1 reply", ChatID: "oc_gA", ChatKind: channel.ChatKindGroup, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 1 || lastSendOnceSession(f) != topicA1 {
			t.Fatalf("topic follow-up: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), topicA1)
		}

		// Same topic_id in group B gets its own conversation.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_tB_1", Text: "group B topic", ChatID: "oc_gB", ChatKind: channel.ChatKindTopic, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		topicB1 := boundThread(s, channelTopicRouteKey("oc_gB", "omt_1"))
		if topicB1 == "" || topicB1 == topicA1 || f.sent() != 2 {
			t.Fatalf("topicB1=%q topicA1=%q starts=%d", topicB1, topicA1, f.sent())
		}
		registerSession(topicB1)

		// 2. Regular group messages in oc_gA merge into ONE group conversation (ignoring differing legacy m.Thread).
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_gA_1", Text: "regular group 1", ChatID: "oc_gA", ChatKind: channel.ChatKindGroup, Thread: "oc_gA:m_gA_1",
		}); err != nil {
			t.Fatal(err)
		}
		groupA := boundThread(s, channelGroupRouteKey("oc_gA"))
		if groupA == "" || groupA == topicA1 || f.sent() != 3 {
			t.Fatalf("groupA=%q topicA1=%q starts=%d", groupA, topicA1, f.sent())
		}
		registerSession(groupA)

		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_gA_2", Text: "regular group 2", ChatID: "oc_gA", ChatKind: channel.ChatKindGroup, Thread: "oc_gA:m_gA_2",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 3 || lastSendOnceSession(f) != groupA {
			t.Fatalf("regular group message 2 did not merge: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), groupA)
		}

		// Regular group B gets its own group conversation separate from group A.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_gB_1", Text: "regular group B", ChatID: "oc_gB", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		groupB := boundThread(s, channelGroupRouteKey("oc_gB"))
		if groupB == "" || groupB == groupA || f.sent() != 4 {
			t.Fatalf("groupB=%q groupA=%q starts=%d", groupB, groupA, f.sent())
		}
		registerSession(groupB)

		// 3. Direct message goes to channel's default target (m1), even when chat_id and legacy thread are present.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_dm_1", Text: "private msg", ChatID: "oc_dm_1", ChatKind: channel.ChatKindDirect, Thread: "oc_dm_1:m_dm_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 4 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("direct message: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		// 4. Legacy plugin (no chat_id/chat_kind/topic_id) falls back smoothly to m.Thread.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_leg_1", Text: "legacy plugin msg 1", Thread: "legacy_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		legacySess := boundThread(s, "legacy_chat:root_1")
		if legacySess == "" || f.sent() != 5 {
			t.Fatalf("legacy fallback start: starts=%d sess=%q", f.sent(), legacySess)
		}
		registerSession(legacySess)

		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "m_leg_2", Text: "legacy plugin msg 2", Thread: "legacy_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 5 || lastSendOnceSession(f) != legacySess {
			t.Fatalf("legacy fallback continuation: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacySess)
		}

		// 5. /new inside regular group resets only that group's binding, leaving channel default target intact.
		*sent = nil
		s.channelReceive("examplechat", channel.Message{
			ID: "m_gA_new", Text: "/new", ChatID: "oc_gA", ChatKind: channel.ChatKindGroup,
		}, "")
		if boundThread(s, channelGroupRouteKey("oc_gA")) != "" {
			t.Fatal("/new in group did not unbind group conversation")
		}
		if rt, err := s.channelTarget(ctx, "examplechat"); err != nil || !rt.main {
			t.Fatalf("/new in group moved channel target: %+v %v", rt, err)
		}
		wantNote := i18n.T(s.botLang(ctx), "assistant-channels_plugin.thread_next_is_new")
		if len(*sent) == 0 || !strings.Contains((*sent)[0]["text"].(string), wantNote) {
			t.Fatalf("/new note = %+v, want %q", *sent, wantNote)
		}
	})

	t.Run("per_topic isolates only topics and sends regular groups and directs to default target", func(t *testing.T) {
		s, f, _ := threadsChannelService(t, true)
		setIntegrationsV2(t, s, true)
		ctx := context.Background()

		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"per_topic"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT per_topic: %d %s", rec.Code, rec.Body.String())
		}

		// Regular group message (even with legacy m.Thread populated) goes to channel default target m1.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "pt_g1", Text: "regular group", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "oc_g1:pt_g1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 0 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("per_topic regular group: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		// Direct message goes to m1.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "pt_dm", Text: "direct", ChatID: "oc_dm", ChatKind: channel.ChatKindDirect,
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 0 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("per_topic direct: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		// Topic message gets its own conversation.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "pt_t1", Text: "topic 1", ChatID: "oc_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		topicSess := boundThread(s, channelTopicRouteKey("oc_g1", "omt_1"))
		if f.sent() != 1 || topicSess == "" {
			t.Fatalf("per_topic topic: starts=%d sess=%q", f.sent(), topicSess)
		}

		// Legacy plugin message (only m.Thread, no structured fields) falls back smoothly to per-thread routing.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "pt_leg", Text: "legacy thread", Thread: "legacy:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		legacySess := boundThread(s, "legacy:root_1")
		if f.sent() != 2 || legacySess == "" || legacySess == topicSess {
			t.Fatalf("per_topic legacy fallback: starts=%d sess=%q", f.sent(), legacySess)
		}
	})
}

func TestChannelThreadIdleHoursRotationAndPruning(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	setIntegrationsV2(t, s, true)
	ctx := context.Background()
	clock := &fakeClock{now: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
	s.setChannelTestClock(clock.Now)

	rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full",
		`{"thread_mode":"per_group_and_topic","thread_idle_hours":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}

	groupMsg := func(id string) channel.Message {
		return channel.Message{ID: id, Text: id, ChatID: "oc_g1", ChatKind: channel.ChatKindGroup}
	}
	groupKey := channelGroupRouteKey("oc_g1")

	// t=0: starts s1.
	if err := s.channelForward(ctx, "examplechat", groupMsg("m1")); err != nil {
		t.Fatal(err)
	}
	s1 := boundThread(s, groupKey)
	if f.sent() != 1 || s1 == "" {
		t.Fatalf("m1: starts=%d s1=%q", f.sent(), s1)
	}
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: s1, MachineID: "server", Assistant: true},
	)

	// t=1h: within 2h -> continues s1 and refreshes At to t=1h.
	clock.advance(time.Hour)
	if err := s.channelForward(ctx, "examplechat", groupMsg("m2")); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 1 || lastSendOnceSession(f) != s1 {
		t.Fatalf("m2 at +1h: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), s1)
	}

	// t=2h30m: 1h30m since m2 -> still within 2h, continues s1 and refreshes At to t=2h30m.
	clock.advance(90 * time.Minute)
	if err := s.channelForward(ctx, "examplechat", groupMsg("m3")); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 1 || lastSendOnceSession(f) != s1 {
		t.Fatalf("m3 at +2h30m: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), s1)
	}

	// t=4h31m: 2h01m since m3 -> exceeds 2h idle timeout, rotates to a new conversation s2.
	clock.advance(2*time.Hour + time.Minute)
	if err := s.channelForward(ctx, "examplechat", groupMsg("m4")); err != nil {
		t.Fatal(err)
	}
	s2 := boundThread(s, groupKey)
	if f.sent() != 2 || s2 == "" || s2 == s1 {
		t.Fatalf("m4 after 2h01m idle: starts=%d s2=%q s1=%q", f.sent(), s2, s1)
	}
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: s1, MachineID: "server", Assistant: true},
		proto.Session{SessionID: s2, MachineID: "server", Assistant: true},
	)

	// Resetting thread_idle_hours to 0 restores the 30-day default timeout.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_idle_hours":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 0: %d %s", rec.Code, rec.Body.String())
	}
	clock.advance(10 * 24 * time.Hour)
	if err := s.channelForward(ctx, "examplechat", groupMsg("m5")); err != nil {
		t.Fatal(err)
	}
	if f.sent() != 2 || lastSendOnceSession(f) != s2 {
		t.Fatalf("m5 at +10d with idle=0: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), s2)
	}
	clock.advance(channelThreadsKeepFor + time.Minute)
	if err := s.channelForward(ctx, "examplechat", groupMsg("m6")); err != nil {
		t.Fatal(err)
	}
	s3 := boundThread(s, groupKey)
	if f.sent() != 3 || s3 == "" || s3 == s2 {
		t.Fatalf("m6 past 30d with idle=0: starts=%d s3=%q s2=%q", f.sent(), s3, s2)
	}

	// Custom idle timeout longer than 30 days (e.g. 1000 hours) is not prematurely pruned when another route binds.
	rec = channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_idle_hours":1000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT 1000: %d %s", rec.Code, rec.Body.String())
	}
	clock.advance(35 * 24 * time.Hour) // 840h: > 30d (720h), < 1000h
	s.seedThreadBinding("examplechat", channelGroupRouteKey("oc_other"), "server", "s_other")
	if got := boundThread(s, groupKey); got != s3 {
		t.Fatalf("binding with 1000h timeout was pruned at 35d: got %q want %q", got, s3)
	}
}

func TestChannelReceiveConcurrentGroupMessagesSerializeUnderIntegrationsV2(t *testing.T) {
	s, f, h := admissionChannelService(t, false, nil)
	setIntegrationsV2(t, s, true)
	rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"per_group_and_topic"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	var release sync.Once
	defer release.Do(func() { close(h.release) })

	sendGroup := func(id string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{
			"id":        id,
			"text":      "group message " + id,
			"chat_id":   "oc_concurrent_group",
			"chat_kind": string(channel.ChatKindGroup),
		})
		if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
			t.Fatal(err)
		}
	}

	sendGroup("g_first")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first group request never started")
	}
	sendGroup("g_second")
	select {
	case <-h.second:
	case <-time.After(200 * time.Millisecond):
	}
	release.Do(func() { close(h.release) })
	waitUntil(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sends)+len(f.sentOnce) >= 2 })
	waitAdmissionDrained(t, s)
	if got := h.starts.Load(); got != 1 {
		t.Fatalf("concurrent regular group messages started %d conversations, want 1", got)
	}
}

func TestChannelReceiveWireSchemaGroupTopicFields(t *testing.T) {
	valid := []string{
		`{"contribution":"examplechat","message":{"id":"m1","text":"legacy"}}`,
		`{"contribution":"examplechat","message":{"id":"m2","text":"dm","chat_id":"oc_dm","chat_kind":"direct"}}`,
		`{"contribution":"examplechat","message":{"id":"m3","text":"group","chat_id":"oc_g1","chat_kind":"group"}}`,
		`{"contribution":"examplechat","message":{"id":"m4","text":"topic","chat_id":"oc_g1","chat_kind":"topic","topic_id":"omt_1","thread":"oc_g1:omt_1"}}`,
	}
	for _, raw := range valid {
		if err := wire.ValidateParams("channel.receive", json.RawMessage(raw)); err != nil {
			t.Errorf("valid channel.receive rejected (%s): %v", raw, err)
		}
	}
	invalid := `{"contribution":"examplechat","message":{"id":"m5","text":"bad","chat_kind":"channel"}}`
	if err := wire.ValidateParams("channel.receive", json.RawMessage(invalid)); err == nil {
		t.Error("invalid chat_kind was accepted by channel.receive schema")
	}
}
