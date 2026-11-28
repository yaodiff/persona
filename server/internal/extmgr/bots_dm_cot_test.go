package extmgr

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// dmCotBotFeatures reads the bot's saved switches, as the settings page does.
func dmCotBotFeatures(t *testing.T, w *botWorld) map[string]any {
	t.Helper()
	code, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if code != 200 {
		t.Fatalf("GET bot settings: %d, %v", code, out)
	}
	features, _ := out["features"].(map[string]any)
	return features
}

// Both switches are present and off by default: an absent key would look the same as off to a reader that checks the
// value, but a client that hides a switch the hub leaves out needs the key to be there.
func TestBotDMCotSettingsDefaultOff(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	features := dmCotBotFeatures(t, w)
	for _, id := range []string{"dm_cot", "cot_answer"} {
		on, ok := features[id].(bool)
		if !ok || on {
			t.Fatalf("default features = %v, want %s present and off", features, id)
		}
	}
}

// Each switch turns on and off on its own, and leaves main and topic_cot as they were.
func TestBotDMCotSettingsRoundTrip(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	for _, tc := range []struct {
		body string
		id   string
		want bool
	}{
		{`{"features":{"dm_cot":true}}`, "dm_cot", true},
		{`{"features":{"dm_cot":false}}`, "dm_cot", false},
		{`{"features":{"cot_answer":true}}`, "cot_answer", true},
		{`{"features":{"cot_answer":false}}`, "cot_answer", false},
	} {
		if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", tc.body); code != 200 {
			t.Fatalf("PUT %s: %d, %v", tc.body, code, out)
		}
		features := dmCotBotFeatures(t, w)
		if got, ok := features[tc.id].(bool); !ok || got != tc.want || features["main"] != true || features["topic_cot"] != true {
			t.Fatalf("after PUT %s: features = %v, want %s %v and main and topic_cot unchanged", tc.body, features, tc.id, tc.want)
		}
	}
}

// Settings saved before the two switches have no such keys and read as off, while topic_cot (on by default) stays on.
func TestBotSettingsSavedBeforeDMCotReadOff(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	st := w.h.store.(*botStore)
	var saved FeishuBotSettings
	if ok, err := st.Setting(context.Background(), botSettingsKey, &saved); !ok || err != nil {
		t.Fatalf("read settings: ok %v, %v", ok, err)
	}
	raw, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatal(err)
	}
	features, _ := loose["features"].(map[string]any)
	delete(features, "dm_cot")
	delete(features, "cot_answer")
	features["topic_cot"] = true
	if err := st.PutSetting(context.Background(), botSettingsKey, loose); err != nil {
		t.Fatal(err)
	}
	w.h.bots.mu.Lock()
	w.h.bots.loaded = false
	w.h.bots.mu.Unlock()
	got := dmCotBotFeatures(t, w)
	if got["dm_cot"] != false || got["cot_answer"] != false || got["topic_cot"] != true {
		t.Fatalf("settings saved before the switches: %v, want both off and topic_cot on", got)
	}
}

// The Channels list the apps draw from carries both switches, labelled, off, in every language.
func TestChannelListIncludesDMCotSettings(t *testing.T) {
	needsLarkModule(t)
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(l), func(t *testing.T) {
			w := newChannelWorld(t)
			raw, _ := json.Marshal(nil)
			out, ok, err := w.h.AppCall(i18n.With(context.Background(), l), assistantwire.AppCall{AppID: assistantwire.AppChannels, Method: assistantwire.ChannelsList, Params: raw})
			if !ok || err != nil {
				t.Fatalf("channels.list: handled %v, %v", ok, err)
			}
			res := map[string]any{}
			_ = json.Unmarshal(out, &res)
			seen := map[string]bool{}
			for _, c := range res["channels"].([]any) {
				m := c.(map[string]any)
				if m["id"] != "feishu" {
					continue
				}
				for _, f := range m["features"].([]any) {
					fm := f.(map[string]any)
					id, _ := fm["id"].(string)
					if id != "dm_cot" && id != "cot_answer" {
						continue
					}
					if fm["on"] != false || fm["title"] == "" || fm["description"] == "" {
						t.Fatalf("%s %s setting = %v, want labelled and off", l, id, fm)
					}
					seen[id] = true
				}
			}
			if !seen["dm_cot"] || !seen["cot_answer"] {
				t.Fatalf("%s: switches seen %v", l, seen)
			}
		})
	}
}

// feishu.configure sets both switches, and still refuses an id it does not know.
func TestConfigureFeishuAcceptsDMCotSwitches(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if err := w.h.configureFeishu(w.ctx, nil, map[string]bool{"dm_cot": true, "cot_answer": true}); err != nil {
		t.Fatal(err)
	}
	features := dmCotBotFeatures(t, w)
	if features["dm_cot"] != true || features["cot_answer"] != true || features["main"] != true || features["topic_cot"] != true {
		t.Fatalf("after configure: %v", features)
	}
	if err := w.h.configureFeishu(w.ctx, nil, map[string]bool{"not_a_switch": true}); err == nil {
		t.Fatal("an unknown switch was accepted")
	}
	if features := dmCotBotFeatures(t, w); features["dm_cot"] != true || features["cot_answer"] != true {
		t.Fatalf("a refused configure changed the switches: %v", features)
	}
}
