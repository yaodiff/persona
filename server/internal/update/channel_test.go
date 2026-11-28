package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

func TestBetaFeedIsIndependentAndPinsImmutableTag(t *testing.T) {
	const stable = "https://github.com/nwverse/persona/releases/latest/download"
	feed, err := BetaFeed(stable)
	if err != nil || feed != "https://github.com/nwverse/persona/releases/download/beta-channel" {
		t.Fatalf("feed %q, %v", feed, err)
	}
	m := &Manifest{Version: "0.7.0-beta.1", Build: 202611171300, Channel: ChannelBeta}
	if got := BetaReleaseBase(feed, m); got != "https://github.com/nwverse/persona/releases/download/v0.7.0-beta.1" {
		t.Fatalf("pinned source %q", got)
	}
	if got := BetaReleaseBase(feed, &Manifest{Version: "0.7.0", Channel: ChannelStable}); got != "" {
		t.Fatal("stable manifest was accepted as beta")
	}
	for _, base := range []string{"file:///private/feed", "https://mirror.example.com/dl", "https://github.com/nwverse/persona/releases/download/v0.6.2"} {
		if _, err := BetaFeed(base); err == nil {
			t.Fatalf("replaced custom source %q", base)
		}
	}
}

func TestCheckChannelRejectsStableAndBetaMixups(t *testing.T) {
	stableFile := `{"name":"personad-darwin-arm64.gz","size":1,"sha256":"` + strings.Repeat("0", 64) + `","binary_sha256":"` + strings.Repeat("0", 64) + `"}`
	for _, test := range []struct {
		name, base, channel, manifest string
		valid                         bool
	}{
		{"old stable", "/dl", ChannelStable, `{"version":"0.7.0","build":1,"files":{"darwin-arm64":` + stableFile + `}}`, true},
		{"stable marked", "/dl", ChannelStable, `{"version":"0.7.0","build":1,"channel":"release","files":{"darwin-arm64":` + stableFile + `}}`, true},
		{"beta pointer", "/releases/download/beta-channel", ChannelBeta, `{"version":"0.7.0-beta.1","build":2,"channel":"beta","files":{"darwin-arm64":` + stableFile + `}}`, true},
		{"beta stable confusion", "/releases/download/beta-channel", ChannelBeta, `{"version":"0.7.0","build":2,"files":{"darwin-arm64":` + stableFile + `}}`, false},
		{"stable beta confusion", "/releases/latest/download", ChannelStable, `{"version":"0.7.0-beta.1","build":2,"channel":"beta","files":{"darwin-arm64":` + stableFile + `}}`, false},
		{"beta from stable pointer", "/releases/latest/download", ChannelBeta, `{"version":"0.7.0-beta.1","build":2,"channel":"beta","files":{"darwin-arm64":` + stableFile + `}}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.base+"/latest.json" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(test.manifest))
			}))
			defer srv.Close()
			_, err := CheckChannel(context.Background(), srv.Client(), srv.URL+test.base, test.channel, nodeproto.EditionFull)
			if (err == nil) != test.valid {
				t.Fatalf("channel validation %v", err)
			}
		})
	}
}

func TestCheckChannelReadsTheCoreFeed(t *testing.T) {
	stableFile := `{"name":"personad-darwin-arm64.gz","size":1,"sha256":"` + strings.Repeat("0", 64) + `","binary_sha256":"` + strings.Repeat("0", 64) + `"}`
	manifest := `{"version":"0.7.0","build":1,"channel":"release","edition":"core","files":{"darwin-arm64":` + stableFile + `}}`
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if asked != "/dl/core-latest.json" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(manifest))
	}))
	defer srv.Close()
	if _, err := CheckChannel(context.Background(), srv.Client(), srv.URL+"/dl", ChannelStable, nodeproto.EditionCore); err != nil {
		t.Fatal(err)
	}
	if asked != "/dl/core-latest.json" {
		t.Fatalf("core channel check asked for %s", asked)
	}
}
