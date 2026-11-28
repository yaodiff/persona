package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/channel"
)

func TestChannelDetailTargetOverride(t *testing.T) {
	getDetailJSON := func(t *testing.T, s *Service, id string) map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/channels/"+id, nil)
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/channels/%s status=%d body=%s", id, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return out
	}

	t.Run("exposes target_override when active and sets plugin settings_url", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{
				"channels": []channel.View{{
					ID: "topicchat", Name: "Topic Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
					Plugin: "topic-ext", Installed: true, Enabled: true, State: channel.StateConnected,
					Capabilities: channel.Capabilities{Threads: true},
				}},
				"target_overrides": map[string]*channel.TargetOverride{
					"topicchat": {Mode: channel.TargetOverrideTopics},
				},
			}, nil
		})
		s := newTestService(t, f)
		body := getDetailJSON(t, s, "topicchat")
		ov, _ := body["target_override"].(map[string]any)
		if ov == nil || ov["mode"] != channel.TargetOverrideTopics {
			t.Fatalf("target_override = %#v, want mode=%q", body["target_override"], channel.TargetOverrideTopics)
		}
		if body["settings_url"] != "settings/plugins/topic-ext" {
			t.Fatalf("settings_url = %#v", body["settings_url"])
		}
	})

	t.Run("omits target_override when inactive even if channel declares threads capability", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{
				"channels": []channel.View{{
					ID: "topicchat", Name: "Topic Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
					Plugin: "topic-ext", Installed: true, Enabled: true, State: channel.StateConnected,
					Capabilities: channel.Capabilities{Threads: true},
				}},
				"target_overrides": map[string]*channel.TargetOverride{},
			}, nil
		})
		s := newTestService(t, f)
		body := getDetailJSON(t, s, "topicchat")
		if _, exists := body["target_override"]; exists {
			t.Fatalf("expected target_override to be omitted when inactive, got %#v", body["target_override"])
		}
	})

	t.Run("falls back to capabilities.threads when older hub omits target_overrides", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{
				"channels": []channel.View{
					{
						ID: "threadchat", Name: "Thread Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
						Plugin: "thread-ext", Installed: true, Enabled: true, State: channel.StateConnected,
						Capabilities: channel.Capabilities{Threads: true},
					},
					{
						ID: "plainchat", Name: "Plain Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
						Plugin: "plain-ext", Installed: true, Enabled: true, State: channel.StateConnected,
						Capabilities: channel.Capabilities{Threads: false},
					},
				},
			}, nil
		})
		s := newTestService(t, f)

		threaded := getDetailJSON(t, s, "threadchat")
		ov, _ := threaded["target_override"].(map[string]any)
		if ov == nil || ov["mode"] != channel.TargetOverrideThreads {
			t.Fatalf("threadchat target_override = %#v, want mode=%q", threaded["target_override"], channel.TargetOverrideThreads)
		}

		plain := getDetailJSON(t, s, "plainchat")
		if _, exists := plain["target_override"]; exists {
			t.Fatalf("plainchat should omit target_override, got %#v", plain["target_override"])
		}
	})

	t.Run("never exposes target_override on read-only or market channels", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			return map[string]any{
				"channels": []channel.View{
					{
						ID: "app", Name: "Persona", Kind: channel.KindApp, Source: channel.SourceBuiltin,
						Installed: true, Enabled: true, State: channel.StateConnected, ReadOnly: true,
					},
					{
						ID: "market_chat", Name: "Market Chat", Kind: channel.KindIM, Source: channel.SourceMarket,
						Installed: false, State: channel.StateNotInstalled,
						Capabilities: channel.Capabilities{Threads: true},
					},
				},
				"target_overrides": map[string]*channel.TargetOverride{
					"app":         {Mode: channel.TargetOverrideTopics},
					"market_chat": {Mode: channel.TargetOverrideThreads},
				},
			}, nil
		})
		s := newTestService(t, f)
		for _, id := range []string{"app", "market_chat"} {
			body := getDetailJSON(t, s, id)
			if _, exists := body["target_override"]; exists {
				t.Fatalf("%s should omit target_override, got %#v", id, body["target_override"])
			}
		}
	})
}

func TestChannelDetailV2RespectsPluginOverride(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides map[string]*channel.TargetOverride
		want      *channel.TargetOverride
	}{
		{name: "suppressed", overrides: map[string]*channel.TargetOverride{}},
		{name: "active", overrides: map[string]*channel.TargetOverride{"topicchat": {Mode: channel.TargetOverrideThreads}},
			want: &channel.TargetOverride{Mode: channel.TargetOverrideTopics}},
		{name: "older hub", want: &channel.TargetOverride{Mode: channel.TargetOverrideTopics}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
				res := map[string]any{"channels": []channel.View{{
					ID: "topicchat", Name: "Topic Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
					Installed: true, Enabled: true, State: channel.StateConnected,
					Capabilities: channel.Capabilities{Threads: true},
				}}}
				if tc.overrides != nil {
					res["target_overrides"] = tc.overrides
				}
				return res, nil
			})
			s := newTestService(t, f)
			setIntegrationsV2(t, s, true)
			detail := getChannelDetail(t, s, "topicchat")
			if detail.ThreadMode != channel.ThreadModePerTopic {
				t.Fatalf("effective thread mode = %q, want per_topic", detail.ThreadMode)
			}
			if got := detail.TargetOverride; (got == nil) != (tc.want == nil) || got != nil && got.Mode != tc.want.Mode {
				t.Fatalf("v2 target_override = %+v, want %+v", got, tc.want)
			}
		})
	}
}
