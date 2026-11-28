package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/update"
)

func TestSettingsTextsInEachLanguage(t *testing.T) {
	st := FleetState{NeedsYou: 1, Working: 2, Unread: 3}
	for l, want := range map[i18n.Lang]string{
		i18n.ZhHans: "1 个需要你 · 2 个在工作 · 3 个做完了",
		i18n.En:     "1 needs you · 2 working · 3 done",
		i18n.Ja:     "1件が要対応 · 2件が作業中 · 3件が完了",
	} {
		if got := fleetSentence(l, st); got != want {
			t.Errorf("%s fleet: %q", l, got)
		}
	}
	if got := fleetSentence(i18n.En, FleetState{NeedsYou: 2}); got != "2 need you" {
		t.Errorf("en plural: %q", got)
	}
	// The live activity's start alert, built without a language, is the Chinese it was.
	aps := startPayload(st, time.Now(), false)["aps"].(map[string]any)
	if body := aps["alert"].(map[string]any)["body"]; body != "1 个需要你 · 2 个在工作 · 3 个做完了" {
		t.Errorf("default alert: %q", body)
	}

	if got := unknownDelivery(i18n.Ja, "hub-send.lost_during").Error(); got != "送信中にコンピューターとの接続が切れました：すでにコンピューターに届いている可能性があります。チャットを確認してから再送するか決めてください" {
		t.Errorf("send ja: %q", got)
	}
	// (The mail account's and the computer's cards, and a change's before → after line, are the manager's: extmgr's
	// TestMailAccountRequestInEnglish.)
}

func TestSelfUpdateWhyNotInEachLanguage(t *testing.T) {
	h := newTestHub(t)
	u := &updater{h: h}
	if err := u.whyNot(); err.Error() != "这个 hub 不能自己更新：开发版（"+h.version+"）" {
		t.Errorf("zh: %q", err)
	}
	if got := u.whyNotReason(i18n.En); got != "Development build ("+h.version+")" {
		t.Errorf("en: %q", got)
	}
	if got := i18n.Localize(u.whyNot(), i18n.Ja); got != "このハブは自動で更新できません：開発版（"+h.version+"）" {
		t.Errorf("ja: %q", got)
	}
}

func TestSelfUpdateMissingSourceGuidanceInEachLanguage(t *testing.T) {
	for _, tc := range []struct {
		version, key string
		privateOnly  bool
		want         map[i18n.Lang]string
	}{
		{
			version: "v0.7.0", key: "no_source",
			want: map[i18n.Lang]string{
				i18n.En:     "No release update source is configured. Set update.url in hub.json and restart the hub.",
				i18n.ZhHans: "未配置发布版更新源。在 hub.json 中设置 update.url，然后重启 hub。",
				i18n.Ja:     "リリースの更新元が設定されていません。hub.json の update.url を設定し、ハブを再起動してください。",
			},
		},
		{
			version: "web-dev-fixture", key: "private_no_source", privateOnly: true,
			want: map[i18n.Lang]string{
				i18n.En:     "Private network mode is on. Set update.url in hub.json to an internal release URL and restart the hub.",
				i18n.ZhHans: "hub 已启用私有网络模式。在 hub.json 中将 update.url 设为内部发布源的地址，然后重启 hub。",
				i18n.Ja:     "プライベートネットワークモードが有効です。hub.json の update.url にネットワーク内のリリース配信元の URL を設定し、ハブを再起動してください。",
			},
		},
	} {
		t.Run(tc.key, func(t *testing.T) {
			h := newTestHub(t)
			h.version = tc.version
			h.build = update.Make(h.version, update.Release, "202611171200", "")
			h.cfg.Security.PrivateNetworkOnly = tc.privateOnly
			if key, _ := h.upd.whyNotKey(); key != tc.key {
				t.Errorf("reason key = %q, want %q", key, tc.key)
			}
			err := h.upd.begin(context.Background(), false)
			for l, want := range tc.want {
				if v := h.upd.viewIn(l); v.CanUpdate || v.Auto || v.UpdateURL != "" || v.Why != want {
					t.Errorf("%s update status: %+v, want no source with reason %q", l, v, want)
				}
				if got := i18n.Localize(err, l); !strings.HasSuffix(got, want) {
					t.Errorf("%s manual update error: %q, want guidance %q", l, got, want)
				}
			}
		})
	}
}
