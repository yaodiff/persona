package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func assertIntegrationsV2Flag(t *testing.T, s *Service, want bool) {
	t.Helper()
	w, state := permissionSettingsRequest(t, s, http.MethodGet, "/api/v1/assistant", s.managerIdentity(), "full", "")
	if w.Code != http.StatusOK || state.IntegrationsV2 != want {
		t.Fatalf("GET /api/v1/assistant: code=%d integrations_v2=%t, want %t (body=%s)",
			w.Code, state.IntegrationsV2, want, w.Body.String())
	}
}

func setIntegrationsV2ViaHTTP(t *testing.T, s *Service, want, expected bool) {
	t.Helper()
	body := fmt.Sprintf(`{"integrations_v2":%t,"expected_integrations_v2":%t}`, want, expected)
	w, state := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", body)
	if w.Code != http.StatusOK || state.IntegrationsV2 != want {
		t.Fatalf("PUT /api/v1/assistant (%s): code=%d integrations_v2=%t body=%s",
			body, w.Code, state.IntegrationsV2, w.Body.String())
	}
}

func getChannelDetail(t *testing.T, s *Service, id string) channel.Detail {
	t.Helper()
	rec := channelAPIRequest(t, s, http.MethodGet, "/api/v1/channels/"+id, "full", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels/%s: code=%d body=%s", id, rec.Code, rec.Body.String())
	}
	var d channel.Detail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode GET /api/v1/channels/%s: %v", id, err)
	}
	return d
}

func getChannelListByID(t *testing.T, s *Service) map[string]channel.View {
	t.Helper()
	rec := channelAPIRequest(t, s, http.MethodGet, "/api/v1/channels", "full", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels: code=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Channels []channel.View `json:"channels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode GET /api/v1/channels: %v", err)
	}
	byID := make(map[string]channel.View, len(resp.Channels))
	for _, ch := range resp.Channels {
		byID[ch.ID] = ch
	}
	return byID
}

func boundThreadFor(s *Service, channelID, key string) string {
	ctx := context.Background()
	if t := s.lookupThreadBinding(ctx, s.storedThreadBindingForTest(channelID, key, s.integrationsV2On(ctx))); t != nil {
		return t.SessionID
	}
	return ""
}

func registerAssistantSession(f *fakeHub, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.sessions["server"] {
		if existing.SessionID == sessionID {
			return
		}
	}
	f.sessions["server"] = append(f.sessions["server"], proto.Session{
		SessionID: sessionID,
		MachineID: "server",
		Assistant: true,
	})
}

func featureValue(features []channel.Feature, id string) (bool, bool) {
	for _, f := range features {
		if f.ID == id {
			return f.On, true
		}
	}
	return false, false
}

func TestIntegrationsV2DualTopicRollback(t *testing.T) {
	for _, startOn := range []bool{false, true} {
		t.Run(fmt.Sprintf("start_v2=%t", startOn), func(t *testing.T) {
			ctx := context.Background()
			s, f, _ := threadsChannelService(t, true)
			st := s.store().(*memStore)
			const legacy = "oc_group:omt_topic"
			topic := channelTopicRouteKey("oc_group", "omt_topic")
			clock := &fakeClock{now: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
			s.setChannelTestClock(clock.Now)
			receive := func(id, text, thread string, structured bool) {
				t.Helper()
				m := channel.Message{ID: id, Text: text, Thread: thread}
				if structured {
					m.ChatID, m.ChatKind, m.TopicID = "oc_group", channel.ChatKindTopic, "omt_topic"
				}
				raw, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
					t.Fatal(err)
				}
				waitAdmissionDrained(t, s)
			}

			setIntegrationsV2ViaHTTP(t, s, startOn, false)
			rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"per_topic","thread_idle_hours":2}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("seed routing mode: %d %s", rec.Code, rec.Body.String())
			}
			// Seed the single-key state left by an older routing version.
			key := legacy
			if startOn {
				receive("seed", "old structured topic", "", true)
				key = topic
			} else {
				receive("seed", "old legacy thread", legacy, false)
			}
			session := boundThread(s, key)
			if session == "" || f.sent() != 1 {
				t.Fatalf("seed route: session=%q starts=%d, want one bound conversation", session, f.sent())
			}
			registerAssistantSession(f, session)

			// Lark topic messages carry both keys on every delivery.
			for i, v2 := range []bool{startOn, !startOn, startOn} {
				if i > 0 {
					setIntegrationsV2ViaHTTP(t, s, v2, !v2)
				}
				clock.advance(45 * time.Minute)
				receive(fmt.Sprintf("topic_%d", i), "follow-up in the same topic", legacy, true)
				f.mu.Lock()
				sends := len(f.sentOnce)
				f.mu.Unlock()
				if f.sent() != 1 || sends != i+1 || lastSendOnceSession(f) != session {
					t.Fatalf("v2=%t: starts=%d sends=%d session=%q, want one start and %d sends to %q",
						v2, f.sent(), sends, lastSendOnceSession(f), i+1, session)
				}
			}
			var stored map[string]*channelPrefs
			if found, err := st.Setting(ctx, channelsKey, &stored); !found || err != nil {
				t.Fatalf("read stored bindings: found=%t err=%v", found, err)
			}
			now := clock.Now()
			for _, key := range []string{legacy, topic} {
				got := stored["examplechat"].Threads[key]
				if got == nil || got.SessionID != session || !got.At.Equal(now) {
					t.Fatalf("stored binding %q = %+v, want %q at %v", key, got, session, now)
				}
			}

			// A plugin can send /new using only its current routing format.
			resetThread := legacy
			if startOn {
				resetThread = ""
			}
			receive("reset", "/new", resetThread, startOn)
			setIntegrationsV2ViaHTTP(t, s, !startOn, startOn)
			receive("after_reset", "start fresh", legacy, true)
			if f.sent() != 2 {
				t.Fatalf("/new followed by rollback started %d conversations total, want 2", f.sent())
			}
		})
	}
}

// TestIntegrationsV2CombinedRollbackSuite exercises the full combined integrations_v2 lifecycle in one
// end-to-end HTTP + routing + store-reload suite (#14846, graduation baseline for #14802):
//  1. Legacy baseline (integrations_v2=false)
//  2. Enable integrations_v2=true, configure per_group_and_topic / per_topic + thread_idle_hours,
//     exercise group merging, topic isolation, idle-timeout rotation, target_override, and legacy plugin fallback
//  3. Roll back integrations_v2=false + full service reload: verify immediate revert to legacy m.Thread + 30d TTL
//     and legacy target_override while preserving stored thread_mode, thread_idle_hours, and legacy client partial updates
//  4. Re-enable integrations_v2=true + full service reload: verify zero-loss recovery of config, target_override,
//     and active bindings alongside restored idle-timeout enforcement
func TestIntegrationsV2CombinedRollbackSuite(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub().withMachine()
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "c_extra", MachineID: "server", Assistant: true},
	)
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{
			{
				ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{MaxText: 4000, Threads: true, Push: true, Asks: channel.AsksText},
			},
			{
				ID: "topicbot", Name: "Topic Bot", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{MaxText: 4000, Threads: false, Push: true, Asks: channel.AsksText},
			},
			{
				ID: "legacybot", Name: "Legacy Bot", Kind: channel.KindIM, Source: channel.SourcePlugin,
				Installed: true, Enabled: true, State: channel.StateConnected,
				Capabilities: channel.Capabilities{MaxText: 4000, Threads: true, Push: true, Asks: channel.AsksText},
			},
		}}, nil
	})
	f.answerApp(AppChannels, ChannelsEnable, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})

	clock := &fakeClock{now: time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)}
	newReloadedService := func(t *testing.T) *Service {
		t.Helper()
		svc := newServiceOn(t, f, st)
		svc.setChannelTestClock(clock.Now)
		return svc
	}

	s := newReloadedService(t)
	setSettings(t, s, func(v *Settings) {
		v.MachineID = "server"
		v.Main = "m1"
	})

	var (
		legacyExSess      string
		legacyBotSess     string
		legacyBotSess2    string
		groupSess1        string
		groupSess1Rotated string
		groupSess2        string
		topicSess1        string
		topicSess2        string
		ptTopicSess       string
	)

	// --- Step 1: Legacy baseline (integrations_v2=false) ---
	t.Run("step1_legacy_baseline_off", func(t *testing.T) {
		assertIntegrationsV2Flag(t, s, false)

		byID := getChannelListByID(t, s)
		for _, id := range []string{"examplechat", "topicbot", "legacybot"} {
			v, ok := byID[id]
			if !ok {
				t.Fatalf("GET /api/v1/channels missing %q", id)
			}
			if v.ThreadMode != channel.ThreadModeChannelDefault || v.ThreadIdleHours != 0 {
				t.Fatalf("baseline %s list: thread_mode=%q thread_idle_hours=%d", id, v.ThreadMode, v.ThreadIdleHours)
			}
		}

		exDetail := getChannelDetail(t, s, "examplechat")
		if exDetail.TargetOverride == nil || exDetail.TargetOverride.Mode != channel.TargetOverrideThreads {
			t.Fatalf("baseline examplechat target_override = %+v, want threads", exDetail.TargetOverride)
		}
		tbDetail := getChannelDetail(t, s, "topicbot")
		if tbDetail.TargetOverride != nil {
			t.Fatalf("baseline topicbot target_override = %+v, want nil", tbDetail.TargetOverride)
		}
		lbDetail := getChannelDetail(t, s, "legacybot")
		if lbDetail.TargetOverride == nil || lbDetail.TargetOverride.Mode != channel.TargetOverrideThreads {
			t.Fatalf("baseline legacybot target_override = %+v, want threads", lbDetail.TargetOverride)
		}

		// Structured group message without m.Thread routes to main while integrations_v2=false.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s1_ex_group_no_thread", Text: "structured group while off", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 0 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("off structured group: starts=%d session=%q want 0/m1", f.sent(), lastSendOnceSession(f))
		}

		// Legacy m.Thread on examplechat starts its own legacy session and stays alive over 5h (30d TTL).
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s1_ex_leg_1", Text: "legacy thread 1", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:t1",
		}); err != nil {
			t.Fatal(err)
		}
		legacyExSess = boundThreadFor(s, "examplechat", "legacy:t1")
		if legacyExSess == "" || f.sent() != 1 {
			t.Fatalf("s1_ex_leg_1: starts=%d sess=%q", f.sent(), legacyExSess)
		}
		registerAssistantSession(f, legacyExSess)

		clock.advance(5 * time.Hour)
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s1_ex_leg_2", Text: "legacy thread 2", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:t1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 1 || lastSendOnceSession(f) != legacyExSess {
			t.Fatalf("s1_ex_leg_2: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyExSess)
		}

		// Legacy plugin sending ONLY m.Thread (omitting chat_id/chat_kind/topic_id) on legacybot.
		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s1_lb_1", Text: "legacy bot thread 1", Thread: "old_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		legacyBotSess = boundThreadFor(s, "legacybot", "old_chat:root_1")
		if legacyBotSess == "" || legacyBotSess == legacyExSess || f.sent() != 2 {
			t.Fatalf("s1_lb_1: starts=%d sess=%q", f.sent(), legacyBotSess)
		}
		registerAssistantSession(f, legacyBotSess)

		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s1_lb_2", Text: "legacy bot thread 2", Thread: "old_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 2 || lastSendOnceSession(f) != legacyBotSess {
			t.Fatalf("s1_lb_2: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyBotSess)
		}

		// topicbot without capabilities.threads sends topic message to default target m1 while off.
		if err := s.channelForward(ctx, "topicbot", channel.Message{
			ID: "s1_tb_1", Text: "topic while off", ChatID: "oc_pt_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_pt_1", Thread: "oc_pt_g1:omt_pt_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != 2 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("s1_tb_1: starts=%d session=%q want 2/m1", f.sent(), lastSendOnceSession(f))
		}
	})

	// --- Step 2: Enable integrations_v2=true, configure channels, and exercise v2 routing ---
	t.Run("step2_enable_configure_and_exercise_v2", func(t *testing.T) {
		setIntegrationsV2ViaHTTP(t, s, true, false)
		assertIntegrationsV2Flag(t, s, true)

		// Before any preference is saved, a legacy-only plugin continues the conversation opened while off.
		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "after-toggle", Text: "follow-up", Thread: "old_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		if got := lastSendOnceSession(f); got != legacyBotSess {
			t.Fatalf("inherited topic mode sent follow-up to %q, want existing conversation %q", got, legacyBotSess)
		}

		// Stale optimistic lock must fail with 409 configuration_conflict without toggling off.
		wConflict, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full",
			`{"integrations_v2":false,"expected_integrations_v2":false}`)
		if wConflict.Code != http.StatusConflict || !strings.Contains(wConflict.Body.String(), `"configuration_conflict"`) {
			t.Fatalf("stale expected_integrations_v2: code=%d body=%s", wConflict.Code, wConflict.Body.String())
		}
		assertIntegrationsV2Flag(t, s, true)

		// Configure per_group_and_topic (2h idle) on examplechat, per_topic (24h idle) on topicbot,
		// and per_group_and_topic (24h idle) on legacybot.
		for _, tc := range []struct {
			id   string
			body string
			mode channel.ThreadMode
			idle int
		}{
			{"examplechat", `{"thread_mode":"per_group_and_topic","thread_idle_hours":2,"features":{"sync":true}}`, channel.ThreadModePerGroupAndTopic, 2},
			{"topicbot", `{"thread_mode":"per_topic","thread_idle_hours":24}`, channel.ThreadModePerTopic, 24},
			{"legacybot", `{"thread_mode":"per_group_and_topic","thread_idle_hours":24}`, channel.ThreadModePerGroupAndTopic, 24},
		} {
			rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/"+tc.id, "full", tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("PUT /api/v1/channels/%s: code=%d body=%s", tc.id, rec.Code, rec.Body.String())
			}
			var d channel.Detail
			if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			if d.ThreadMode != tc.mode || d.ThreadIdleHours != tc.idle {
				t.Fatalf("PUT %s: mode=%q idle=%d, want %q/%d", tc.id, d.ThreadMode, d.ThreadIdleHours, tc.mode, tc.idle)
			}
			wantOverride := channel.TargetOverrideTopics
			if tc.mode == channel.ThreadModePerGroupAndTopic {
				wantOverride = channel.TargetOverrideGroupsAndTopics
			}
			if d.TargetOverride == nil || d.TargetOverride.Mode != wantOverride {
				t.Fatalf("PUT %s: target_override=%+v, want %s", tc.id, d.TargetOverride, wantOverride)
			}
		}

		// 2a. examplechat (per_group_and_topic): regular group messages merge across different legacy m.Thread values.
		starts := f.sent()
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_g1_1", Text: "group 1 msg 1", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "oc_g1:m1",
		}); err != nil {
			t.Fatal(err)
		}
		groupSess1 = boundThreadFor(s, "examplechat", channelGroupRouteKey("oc_g1"))
		if groupSess1 == "" || groupSess1 == legacyExSess || f.sent() != starts+1 {
			t.Fatalf("s2_g1_1: starts=%d groupSess1=%q", f.sent(), groupSess1)
		}
		registerAssistantSession(f, groupSess1)

		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_g1_2", Text: "group 1 msg 2", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "oc_g1:m2",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+1 || lastSendOnceSession(f) != groupSess1 {
			t.Fatalf("s2_g1_2 did not merge into groupSess1: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), groupSess1)
		}

		// Distinct regular group gets its own conversation.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_g2_1", Text: "group 2 msg 1", ChatID: "oc_g2", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		groupSess2 = boundThreadFor(s, "examplechat", channelGroupRouteKey("oc_g2"))
		if groupSess2 == "" || groupSess2 == groupSess1 || f.sent() != starts+2 {
			t.Fatalf("s2_g2_1: starts=%d groupSess2=%q", f.sent(), groupSess2)
		}
		registerAssistantSession(f, groupSess2)

		// 2b. Topic splitting inside oc_g1: each topic gets an isolated conversation separate from regular group chat.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_t1_1", Text: "topic 1 start", ChatID: "oc_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		topicSess1 = boundThreadFor(s, "examplechat", channelTopicRouteKey("oc_g1", "omt_1"))
		if topicSess1 == "" || topicSess1 == groupSess1 || f.sent() != starts+3 {
			t.Fatalf("s2_t1_1: starts=%d topicSess1=%q", f.sent(), topicSess1)
		}
		registerAssistantSession(f, topicSess1)

		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_t1_2", Text: "topic 1 follow-up", ChatID: "oc_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+3 || lastSendOnceSession(f) != topicSess1 {
			t.Fatalf("s2_t1_2: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), topicSess1)
		}

		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_t2_1", Text: "topic 2 start", ChatID: "oc_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_2",
		}); err != nil {
			t.Fatal(err)
		}
		topicSess2 = boundThreadFor(s, "examplechat", channelTopicRouteKey("oc_g1", "omt_2"))
		if topicSess2 == "" || topicSess2 == topicSess1 || topicSess2 == groupSess1 || f.sent() != starts+4 {
			t.Fatalf("s2_t2_1: starts=%d topicSess2=%q", f.sent(), topicSess2)
		}
		registerAssistantSession(f, topicSess2)

		// Direct messages stay on default target m1.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_dm_1", Text: "direct msg", ChatID: "oc_dm_1", ChatKind: channel.ChatKindDirect, Thread: "oc_dm_1:dm1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+4 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("s2_dm_1: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		// 2c. Idle-timeout rotation on examplechat (thread_idle_hours=2):
		// +1h30m continues groupSess1 (refreshing timestamp); +2h05m rotates to a new session.
		clock.advance(90 * time.Minute)
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_g1_keep", Text: "within 2h", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+4 || lastSendOnceSession(f) != groupSess1 {
			t.Fatalf("s2_g1_keep: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), groupSess1)
		}

		clock.advance(2*time.Hour + 5*time.Minute)
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s2_g1_rotate", Text: "past 2h idle", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		groupSess1Rotated = boundThreadFor(s, "examplechat", channelGroupRouteKey("oc_g1"))
		if f.sent() != starts+5 || groupSess1Rotated == "" || groupSess1Rotated == groupSess1 {
			t.Fatalf("s2_g1_rotate: starts=%d new=%q old=%q", f.sent(), groupSess1Rotated, groupSess1)
		}
		registerAssistantSession(f, groupSess1Rotated)

		// 2d. topicbot (per_topic, thread_idle_hours=24): regular groups stay on default target; topics isolate.
		if err := s.channelForward(ctx, "topicbot", channel.Message{
			ID: "s2_pt_g1", Text: "regular group in per_topic", ChatID: "oc_pt_g1", ChatKind: channel.ChatKindGroup, Thread: "oc_pt_g1:m1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+5 || lastSendOnceSession(f) != "m1" {
			t.Fatalf("s2_pt_g1: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		if err := s.channelForward(ctx, "topicbot", channel.Message{
			ID: "s2_pt_t1", Text: "topic in per_topic", ChatID: "oc_pt_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_pt_1",
		}); err != nil {
			t.Fatal(err)
		}
		ptTopicSess = boundThreadFor(s, "topicbot", channelTopicRouteKey("oc_pt_g1", "omt_pt_1"))
		if f.sent() != starts+6 || ptTopicSess == "" {
			t.Fatalf("s2_pt_t1: starts=%d ptTopicSess=%q", f.sent(), ptTopicSess)
		}
		registerAssistantSession(f, ptTopicSess)

		// 2e. legacybot (legacy plugin sending ONLY m.Thread while integrations_v2=true):
		// continues existing Step 1 session and starts a new legacy thread cleanly.
		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s2_lb_cont", Text: "legacy follow-up under v2", Thread: "old_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+6 || lastSendOnceSession(f) != legacyBotSess {
			t.Fatalf("s2_lb_cont: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyBotSess)
		}

		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s2_lb_new", Text: "legacy second thread under v2", Thread: "old_chat:root_2",
		}); err != nil {
			t.Fatal(err)
		}
		legacyBotSess2 = boundThreadFor(s, "legacybot", "old_chat:root_2")
		if f.sent() != starts+7 || legacyBotSess2 == "" || legacyBotSess2 == legacyBotSess {
			t.Fatalf("s2_lb_new: starts=%d sess=%q", f.sent(), legacyBotSess2)
		}
		registerAssistantSession(f, legacyBotSess2)
	})

	// --- Step 3: Roll back integrations_v2=false + reload service ---
	t.Run("step3_rollback_off_reload_and_preserve_config", func(t *testing.T) {
		setIntegrationsV2ViaHTTP(t, s, false, true)
		assertIntegrationsV2Flag(t, s, false)

		// Immediate rollback of target_override while preserving stored thread_mode and thread_idle_hours.
		if d := getChannelDetail(t, s, "examplechat"); d.ThreadMode != channel.ThreadModePerGroupAndTopic || d.ThreadIdleHours != 2 ||
			d.TargetOverride == nil || d.TargetOverride.Mode != channel.TargetOverrideThreads {
			t.Fatalf("immediate rollback examplechat detail = %+v", d)
		}
		if d := getChannelDetail(t, s, "topicbot"); d.ThreadMode != channel.ThreadModePerTopic || d.ThreadIdleHours != 24 ||
			d.TargetOverride != nil {
			t.Fatalf("immediate rollback topicbot detail = %+v", d)
		}

		// Simulate full Hub/assistant restart from persisted store only.
		s = newReloadedService(t)
		assertIntegrationsV2Flag(t, s, false)

		// Verify persisted config survived reload and legacy target_override holds.
		byID := getChannelListByID(t, s)
		if byID["examplechat"].ThreadMode != channel.ThreadModePerGroupAndTopic || byID["examplechat"].ThreadIdleHours != 2 {
			t.Fatalf("reloaded off examplechat list = %+v", byID["examplechat"])
		}
		if byID["topicbot"].ThreadMode != channel.ThreadModePerTopic || byID["topicbot"].ThreadIdleHours != 24 {
			t.Fatalf("reloaded off topicbot list = %+v", byID["topicbot"])
		}
		if byID["legacybot"].ThreadMode != channel.ThreadModePerGroupAndTopic || byID["legacybot"].ThreadIdleHours != 24 {
			t.Fatalf("reloaded off legacybot list = %+v", byID["legacybot"])
		}
		if d := getChannelDetail(t, s, "examplechat"); d.TargetOverride == nil || d.TargetOverride.Mode != channel.TargetOverrideThreads {
			t.Fatalf("reloaded off examplechat target_override = %+v, want threads", d.TargetOverride)
		}
		if d := getChannelDetail(t, s, "topicbot"); d.TargetOverride != nil {
			t.Fatalf("reloaded off topicbot target_override = %+v, want nil", d.TargetOverride)
		}

		// Advance clock 10h (> examplechat's 2h idle timeout, < topicbot/legacybot's 24h, < 30d legacy TTL).
		clock.advance(10 * time.Hour)
		starts := f.sent()

		// Structured group without m.Thread reverts to channel default target m1.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s3_off_group", Text: "group while rolled back", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != "m1" {
			t.Fatalf("s3_off_group: starts=%d session=%q want %d/m1", f.sent(), lastSendOnceSession(f), starts)
		}

		// Legacy m.Thread on examplechat ignores configured 2h idle timeout and uses 30-day TTL -> resumes legacyExSess.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s3_off_legacy", Text: "legacy thread past 2h while off", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "legacy:t1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != legacyExSess {
			t.Fatalf("s3_off_legacy: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyExSess)
		}

		// topicbot (capabilities.threads=false) reverts to default target even with topic fields.
		if err := s.channelForward(ctx, "topicbot", channel.Message{
			ID: "s3_off_pt_topic", Text: "topic while off", ChatID: "oc_pt_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_pt_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != "m1" {
			t.Fatalf("s3_off_pt_topic: starts=%d session=%q want m1", f.sent(), lastSendOnceSession(f))
		}

		// legacybot with ONLY m.Thread continues legacyBotSess under rolled-back legacy mode.
		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s3_off_lb", Text: "legacy bot while off", Thread: "old_chat:root_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != legacyBotSess {
			t.Fatalf("s3_off_lb: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyBotSess)
		}

		// Legacy client partial PUT while rolled back must preserve saved thread_mode and thread_idle_hours.
		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"features":{"pushes":true}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("legacy partial PUT while off: code=%d body=%s", rec.Code, rec.Body.String())
		}
		d := getChannelDetail(t, s, "examplechat")
		if d.ThreadMode != channel.ThreadModePerGroupAndTopic || d.ThreadIdleHours != 2 {
			t.Fatalf("legacy partial PUT while off clobbered routing config: mode=%q idle=%d", d.ThreadMode, d.ThreadIdleHours)
		}
		if syncOn, _ := featureValue(d.Features, channel.FeatureSync); !syncOn {
			t.Fatalf("legacy partial PUT lost existing sync feature: %+v", d.Features)
		}
		if pushOn, _ := featureValue(d.Features, channel.FeaturePushes); !pushOn {
			t.Fatalf("legacy partial PUT did not save pushes feature: %+v", d.Features)
		}
	})

	// --- Step 4: Re-enable integrations_v2=true + reload -> zero-loss recovery ---
	t.Run("step4_reenable_v2_reload_zero_loss_recovery", func(t *testing.T) {
		setIntegrationsV2ViaHTTP(t, s, true, false)
		// Reload once more after re-enable to prove persisted flag + channel config + bindings all recover seamlessly.
		s = newReloadedService(t)
		assertIntegrationsV2Flag(t, s, true)

		exDetail := getChannelDetail(t, s, "examplechat")
		if exDetail.ThreadMode != channel.ThreadModePerGroupAndTopic || exDetail.ThreadIdleHours != 2 ||
			exDetail.TargetOverride == nil || exDetail.TargetOverride.Mode != channel.TargetOverrideGroupsAndTopics {
			t.Fatalf("re-enabled examplechat detail = %+v", exDetail)
		}
		if syncOn, _ := featureValue(exDetail.Features, channel.FeatureSync); !syncOn {
			t.Fatalf("re-enabled examplechat lost sync feature: %+v", exDetail.Features)
		}
		if pushOn, _ := featureValue(exDetail.Features, channel.FeaturePushes); !pushOn {
			t.Fatalf("re-enabled examplechat lost pushes feature: %+v", exDetail.Features)
		}

		tbDetail := getChannelDetail(t, s, "topicbot")
		if tbDetail.ThreadMode != channel.ThreadModePerTopic || tbDetail.ThreadIdleHours != 24 ||
			tbDetail.TargetOverride == nil || tbDetail.TargetOverride.Mode != channel.TargetOverrideTopics {
			t.Fatalf("re-enabled topicbot detail = %+v", tbDetail)
		}

		starts := f.sent()

		// 4a. Active binding within 24h idle timeout (topicbot: elapsed 10h < 24h) resumes with zero data loss!
		if err := s.channelForward(ctx, "topicbot", channel.Message{
			ID: "s4_pt_t1_resume", Text: "resume topic after re-enable", ChatID: "oc_pt_g1", ChatKind: channel.ChatKindTopic, TopicID: "omt_pt_1",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != ptTopicSess {
			t.Fatalf("re-enabled topicbot did not resume ptTopicSess: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), ptTopicSess)
		}

		// 4b. Legacy plugin binding within 24h idle timeout (legacybot: elapsed 10h < 24h) resumes with zero data loss!
		if err := s.channelForward(ctx, "legacybot", channel.Message{
			ID: "s4_lb_resume_2", Text: "resume legacy second thread", Thread: "old_chat:root_2",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts || lastSendOnceSession(f) != legacyBotSess2 {
			t.Fatalf("re-enabled legacybot did not resume legacyBotSess2: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), legacyBotSess2)
		}

		// 4c. Binding past restored 2h idle timeout (examplechat: elapsed 10h > 2h) rotates cleanly.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s4_ex_rotate", Text: "examplechat group after 10h idle", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup,
		}); err != nil {
			t.Fatal(err)
		}
		groupSess1After := boundThreadFor(s, "examplechat", channelGroupRouteKey("oc_g1"))
		if f.sent() != starts+1 || groupSess1After == "" || groupSess1After == groupSess1Rotated {
			t.Fatalf("re-enabled examplechat did not rotate idle group: starts=%d new=%q old=%q", f.sent(), groupSess1After, groupSess1Rotated)
		}
		registerAssistantSession(f, groupSess1After)

		// Follow-up in oc_g1 merges into the newly rotated conversation.
		if err := s.channelForward(ctx, "examplechat", channel.Message{
			ID: "s4_ex_merge", Text: "examplechat group follow-up", ChatID: "oc_g1", ChatKind: channel.ChatKindGroup, Thread: "oc_g1:unrelated",
		}); err != nil {
			t.Fatal(err)
		}
		if f.sent() != starts+1 || lastSendOnceSession(f) != groupSess1After {
			t.Fatalf("re-enabled examplechat follow-up did not merge: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), groupSess1After)
		}

		// 4d. Setting channel_default clears target_override under integrations_v2=true;
		// rolling integrations_v2 back to false restores legacy capabilities.threads target_override.
		rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", `{"thread_mode":"channel_default"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("PUT channel_default: code=%d body=%s", rec.Code, rec.Body.String())
		}
		if d := getChannelDetail(t, s, "examplechat"); d.TargetOverride != nil {
			t.Fatalf("channel_default under v2 target_override = %+v, want nil", d.TargetOverride)
		}
		setIntegrationsV2ViaHTTP(t, s, false, true)
		if d := getChannelDetail(t, s, "examplechat"); d.TargetOverride == nil || d.TargetOverride.Mode != channel.TargetOverrideThreads {
			t.Fatalf("rollback after channel_default target_override = %+v, want threads", d.TargetOverride)
		}
	})
}

// TestIntegrationsV2LegacyClientPartialChannelUpdatePreservesRoutingConfig verifies that older clients
// omitting thread_mode and thread_idle_hours on PUT /api/v1/channels/{id} never clear saved routing
// settings or active thread bindings, regardless of whether integrations_v2 is on or off.
func TestIntegrationsV2LegacyClientPartialChannelUpdatePreservesRoutingConfig(t *testing.T) {
	st := &memStore{}
	f := newFakeHub().withMachine()
	f.setSessions("server",
		proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "c1", MachineID: "server", Assistant: true},
	)
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{
			ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, Enabled: true, State: channel.StateConnected,
			Capabilities: channel.Capabilities{MaxText: 4000, Threads: true, Push: true, Asks: channel.AsksText},
		}}}, nil
	})
	f.answerApp(AppChannels, ChannelsEnable, func(json.RawMessage) (any, error) {
		return map[string]any{}, nil
	})

	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) {
		v.MachineID = "server"
		v.Main = "m1"
	})
	setIntegrationsV2ViaHTTP(t, s, true, false)

	// Save v2 routing configuration and seed an active thread binding.
	rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full",
		`{"thread_mode":"per_group_and_topic","thread_idle_hours":18}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed PUT: %d %s", rec.Code, rec.Body.String())
	}
	routeKey := channelGroupRouteKey("oc_keep_me")
	if !s.seedThreadBinding("examplechat", routeKey, "server", "c1") {
		t.Fatal("failed to bind channel thread")
	}

	partialBodies := []string{
		`{"enabled":true}`,
		`{"target":{"kind":"conversation","machine_id":"server","session_id":"c1"}}`,
		`{"features":{"sync":true}}`,
		`{"target":{"kind":"main"}}`,
		`{}`,
	}

	for _, v2On := range []bool{true, false, true} {
		setIntegrationsV2ViaHTTP(t, s, v2On, s.integrationsV2On(context.Background()))
		for _, body := range partialBodies {
			rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", body)
			if rec.Code != http.StatusOK {
				t.Fatalf("v2=%t partial PUT %s: code=%d body=%s", v2On, body, rec.Code, rec.Body.String())
			}
			var d channel.Detail
			if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			if d.ThreadMode != channel.ThreadModePerGroupAndTopic || d.ThreadIdleHours != 18 {
				t.Fatalf("v2=%t partial PUT %s clobbered config: mode=%q idle=%d", v2On, body, d.ThreadMode, d.ThreadIdleHours)
			}
			if got := boundThreadFor(s, "examplechat", routeKey); got != "c1" {
				t.Fatalf("v2=%t partial PUT %s clobbered thread binding: got %q, want c1", v2On, body, got)
			}
		}
	}

	// Verify everything survived store reload intact.
	reloaded := newServiceOn(t, f, st)
	d := getChannelDetail(t, reloaded, "examplechat")
	if d.ThreadMode != channel.ThreadModePerGroupAndTopic || d.ThreadIdleHours != 18 {
		t.Fatalf("after reload: mode=%q idle=%d, want per_group_and_topic/18", d.ThreadMode, d.ThreadIdleHours)
	}
	if got := boundThreadFor(reloaded, "examplechat", routeKey); got != "c1" {
		t.Fatalf("after reload: thread binding = %q, want c1", got)
	}
	if d.Target == nil || !reflect.DeepEqual(*d.Target, channel.Target{Kind: channel.TargetMain}) {
		t.Fatalf("after reload: target = %+v, want main", d.Target)
	}
}

// TestIntegrationsV2LegacyPluginThreadOnlyMatrix verifies that legacy channel plugins emitting only
// m.Thread (never chat_id/chat_kind/topic_id) route cleanly across both integrations_v2=false and
// integrations_v2=true, including admission queue delivery and /new thread reset.
func TestIntegrationsV2LegacyPluginThreadOnlyMatrix(t *testing.T) {
	cases := []struct {
		name           string
		v2On           bool
		capThreads     bool
		mode           channel.ThreadMode
		wantDedicated  bool
		wantNewUnbinds bool
	}{
		{"v2_off_cap_threads_on", false, true, channel.ThreadModeChannelDefault, true, true},
		{"v2_off_cap_threads_off", false, false, channel.ThreadModeChannelDefault, false, false},
		{"v2_on_unset_cap_threads_on", true, true, "", true, true},
		{"v2_on_unset_cap_threads_off", true, false, "", false, false},
		{"v2_on_channel_default", true, true, channel.ThreadModeChannelDefault, false, false},
		{"v2_on_per_group_and_topic_cap_on", true, true, channel.ThreadModePerGroupAndTopic, true, true},
		{"v2_on_per_group_and_topic_cap_off", true, false, channel.ThreadModePerGroupAndTopic, true, true},
		{"v2_on_per_topic_cap_on", true, true, channel.ThreadModePerTopic, true, true},
		{"v2_on_per_topic_cap_off", true, false, channel.ThreadModePerTopic, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f, sent := threadsChannelService(t, tc.capThreads)
			setIntegrationsV2(t, s, tc.v2On)
			if tc.mode != "" {
				rec := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full",
					fmt.Sprintf(`{"thread_mode":%q}`, tc.mode))
				if rec.Code != http.StatusOK {
					t.Fatalf("PUT mode: %d %s", rec.Code, rec.Body.String())
				}
			}

			// Validate legacy wire payload (no chat_id/chat_kind/topic_id) against channel.receive schema
			// and deliver through the live ChannelReceive admission path.
			rawMsg := json.RawMessage(`{"id":"leg_1","text":"hello from legacy plugin","thread":"legacy:thread_42"}`)
			wirePayload := fmt.Sprintf(`{"contribution":"examplechat","message":%s}`, rawMsg)
			if err := wire.ValidateParams("channel.receive", json.RawMessage(wirePayload)); err != nil {
				t.Fatalf("legacy channel.receive rejected by schema: %v", err)
			}
			if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: rawMsg}); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				return len(f.sends)+len(f.sentOnce) >= 1
			})
			waitAdmissionDrained(t, s)

			if !tc.wantDedicated {
				if f.sent() != 0 || lastSendOnceSession(f) != "m1" || boundThread(s, "legacy:thread_42") != "" {
					t.Fatalf("expected default target m1: starts=%d session=%q bound=%q",
						f.sent(), lastSendOnceSession(f), boundThread(s, "legacy:thread_42"))
				}
				return
			}

			sess := boundThread(s, "legacy:thread_42")
			if f.sent() != 1 || sess == "" || sess == "m1" {
				t.Fatalf("expected dedicated thread session: starts=%d sess=%q", f.sent(), sess)
			}
			registerAssistantSession(f, sess)

			// Second message with same legacy m.Thread continues the same conversation.
			rawMsg2 := json.RawMessage(`{"id":"leg_2","text":"follow-up from legacy plugin","thread":"legacy:thread_42"}`)
			if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: rawMsg2}); err != nil {
				t.Fatal(err)
			}
			waitUntil(t, func() bool {
				f.mu.Lock()
				defer f.mu.Unlock()
				return len(f.sentOnce) >= 1
			})
			waitAdmissionDrained(t, s)
			if f.sent() != 1 || lastSendOnceSession(f) != sess {
				t.Fatalf("legacy follow-up: starts=%d session=%q want %q", f.sent(), lastSendOnceSession(f), sess)
			}

			// /new in legacy thread resets only that thread binding without moving default channel target.
			if tc.wantNewUnbinds {
				*sent = nil
				s.channelReceive("examplechat", channel.Message{ID: "leg_new", Text: "/new", Thread: "legacy:thread_42"}, "")
				if boundThread(s, "legacy:thread_42") != "" {
					t.Fatal("/new did not unbind legacy thread")
				}
				if rt, err := s.channelTarget(context.Background(), "examplechat"); err != nil || !rt.main {
					t.Fatalf("/new moved channel default target: %+v %v", rt, err)
				}
				wantNote := i18n.T(s.botLang(context.Background()), "assistant-channels_plugin.thread_next_is_new")
				if len(*sent) == 0 || !strings.Contains((*sent)[0]["text"].(string), wantNote) {
					t.Fatalf("/new reply = %+v, want %q", *sent, wantNote)
				}
			}
		})
	}
}
