package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestChannelUpdateSaveFailurePreservesPreferences(t *testing.T) {
	f := newFakeHub().withMachine()
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{
			ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, Enabled: true, State: channel.StateConnected,
			Capabilities: channel.Capabilities{Threads: true, Asks: channel.AsksText},
		}}}, nil
	})
	s := newTestService(t, f)
	st := s.store().(*memStore)
	setIntegrationsV2(t, s, true)
	put := func(body string) (int, string) {
		t.Helper()
		res := channelAPIRequest(t, s, http.MethodPut, "/api/v1/channels/examplechat", "full", body)
		return res.Code, res.Body.String()
	}
	if status, body := put(`{"target":{"kind":"main"},"thread_mode":"per_topic","thread_idle_hours":12,"features":{"asks":false}}`); status != http.StatusOK {
		t.Fatalf("seed PUT: %d %s", status, body)
	}
	before := getChannelDetail(t, s, "examplechat")
	saved, exists := st.rawSetting(channelsKey)
	if !exists {
		t.Fatal("seed PUT did not persist the channel settings")
	}

	st.setFailPut(errors.New("synthetic channel settings write failure"))
	const update = `{"target":{"kind":"new"},"thread_mode":"channel_default","thread_idle_hours":24,"features":{"all_asks":true}}`
	if status, body := put(update); status != http.StatusServiceUnavailable {
		t.Errorf("failed PUT: %d %s, want 503", status, body)
	}
	if after := getChannelDetail(t, s, "examplechat"); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed PUT changed in-memory detail: got %+v, want %+v", after, before)
	}
	if current, _ := st.rawSetting(channelsKey); !bytes.Equal(current, saved) {
		t.Fatalf("failed PUT changed persisted preferences: got %s, want %s", current, saved)
	}

	st.setFailPut(nil)
	if status, body := put(update); status != http.StatusOK {
		t.Fatalf("retried PUT: %d %s", status, body)
	}
	after := getChannelDetail(t, s, "examplechat")
	reloaded := getChannelDetail(t, newServiceOn(t, f, st), "examplechat")
	if after.Target == nil || after.Target.Kind != channel.TargetNew ||
		after.ThreadMode != channel.ThreadModeChannelDefault || after.ThreadIdleHours != 24 ||
		!reflect.DeepEqual(after, reloaded) {
		t.Fatalf("retried settings were not saved and reloaded: current %+v, reloaded %+v", after, reloaded)
	}
	for _, id := range []string{channel.FeatureAsks, channel.FeatureAllAsks} {
		if on, ok := featureValue(after.Features, id); !ok || !on {
			t.Fatalf("%s after retry = %t, present=%t, want enabled", id, on, ok)
		}
	}
}

func TestChannelNewSaveFailurePreservesThread(t *testing.T) {
	s, _, sent := threadsChannelService(t, true)
	const thread = "oc_group:omt_topic"
	s.seedThreadBinding("examplechat", thread, "server", "existing")
	s.store().(*memStore).setFailPut(errors.New("synthetic thread reset write failure"))

	ctx := context.Background()
	message := channel.Message{ID: "reset", Thread: thread}
	if handled, rest := s.channelCommand(ctx, "examplechat", message, "/new next message"); !handled || rest != "" {
		t.Fatalf("failed reset forwarded follow-up text: handled=%t rest=%q", handled, rest)
	}
	if got := boundThread(s, thread); got != "existing" {
		t.Fatalf("failed reset changed thread binding to %q", got)
	}
	if want := i18n.T(s.botLang(ctx), "http-status.unavailable"); len(*sent) != 1 || (*sent)[0]["text"] != want {
		t.Fatalf("replies = %+v, want one reply %q", *sent, want)
	}
}
