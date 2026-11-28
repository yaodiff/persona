package hub

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/update"
)

func TestWebDevelopmentBuildChecksReleaseAndBeta(t *testing.T) {
	for _, tc := range []struct {
		source, url, version string
	}{
		{update.ChannelStable, "https://github.com/nwverse/persona/releases/latest/download", "0.7.0"},
		{update.ChannelBeta, "https://github.com/nwverse/persona/releases/download/beta-channel", "0.8.0-beta.1"},
	} {
		t.Run(tc.source, func(t *testing.T) {
			h := newTestHub(t)
			h.version = "web-dev-fixture"
			h.build = update.Make(h.version, update.Release, "202611171200", "")
			h.upd.settings.Source = "web-dev"
			manifest := update.Manifest{
				Version: tc.version, Build: 202611161200, Channel: tc.source, Edition: h.build.Edition.Marker(),
				Files: map[string]update.File{"linux-amd64": {Name: "personad-linux-amd64.gz"}},
			}
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			h.upd.client = &http.Client{Transport: updateLoopTransport(func(r *http.Request) (*http.Response, error) {
				if want := tc.url + "/" + update.FeedName(h.build.Edition); r.Method != http.MethodGet || r.URL.String() != want {
					t.Errorf("feed request = %s %s, want GET %s", r.Method, r.URL, want)
					return nil, http.ErrNotSupported
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(data)))}, nil
			})}
			handler := h.routes()
			call := func(method, path, body string) updateView {
				t.Helper()
				w := securityRequest(handler, method, path, h.cfg.Token, body, "127.0.0.1:1", false)
				var view updateView
				if err := json.Unmarshal(w.Body.Bytes(), &view); w.Code != http.StatusOK || err != nil {
					t.Fatalf("%s %s: %d %s (%v)", method, path, w.Code, w.Body, err)
				}
				if !view.CanUpdate || view.Source != tc.source || view.UpdateURL != tc.url || view.UpdateFrom != "build" {
					t.Fatalf("%s %s: unexpected update view: %+v", method, path, view)
				}
				return view
			}
			call(http.MethodPut, "/api/v1/hub/update/settings", `{"source":"`+tc.source+`"}`)
			if view := call(http.MethodPost, "/api/v1/hub/update/check", `{"preview":true}`); view.Latest == nil || view.Latest.Version != tc.version || view.Latest.Newer {
				t.Fatalf("an older release should be visible without being offered as an update: %+v", view.Latest)
			}
		})
	}
}

func TestBetaSettingsPersistAndInvalidateTheOtherChannel(t *testing.T) {
	h := newTestHub(t)
	h.build = update.Make("v1", update.Release, "202611171200", "https://github.com/nwverse/persona/releases/latest/download")
	h.store = &settingsStore{vals: map[string][]byte{}}
	h.upd.release = &update.Published{Manifest: &update.Manifest{Version: "0.7.0", Build: 1}}
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	call := func(body string) (int, updateView) {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/hub/update/settings", strings.NewReader(body))
		req.RemoteAddr = net.JoinHostPort(netip.MustParseAddr("127.0.0.1").String(), "1")
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(w, req)
		var view updateView
		_ = json.Unmarshal(w.Body.Bytes(), &view)
		return w.Code, view
	}
	code, view := call(`{"source":"beta"}`)
	if code != http.StatusOK || view.Source != update.ChannelBeta || !strings.HasSuffix(view.UpdateURL, "/releases/download/beta-channel") {
		t.Fatalf("beta selection: %d %+v", code, view)
	}
	if h.upd.release != nil {
		t.Fatal("kept the stable candidate after choosing beta")
	}
	var saved updateSettings
	if _, err := h.store.Setting(context.Background(), updateSettingsKey, &saved); err != nil || saved.Source != update.ChannelBeta {
		t.Fatalf("persisted selection: %+v, %v", saved, err)
	}
	if got := h.androidUpdateDir(); !strings.HasSuffix(got, "apps/android/beta") {
		t.Fatalf("Android beta cache: %q", got)
	}
	h.upd.release = &update.Published{Manifest: &update.Manifest{Version: "0.7.0-beta.1", Build: 2}}
	if code, view = call(`{"source":"release"}`); code != http.StatusOK || view.Source != "release" || !strings.HasSuffix(view.UpdateURL, "/releases/latest/download") {
		t.Fatalf("stable rollback: %d %+v", code, view)
	}
	if h.upd.release != nil || h.androidUpdateDir() == "" || strings.HasSuffix(h.androidUpdateDir(), "/beta") {
		t.Fatal("kept beta state after returning to stable")
	}
}

func TestBetaInstallSourcePinsTheSelectedImmutableTag(t *testing.T) {
	h := newTestHub(t)
	h.build = update.Make("v1", update.Release, "202611171200", "https://github.com/nwverse/persona/releases/latest/download")
	h.upd.settings.Source = update.ChannelBeta
	m := &update.Manifest{Version: "0.7.0-beta.1", Channel: update.ChannelBeta}
	source, err := h.upd.installSource(m)
	if err != nil || source.url != "https://github.com/nwverse/persona/releases/download/v0.7.0-beta.1" {
		t.Fatalf("beta install source %q, %v", source.url, err)
	}
	if _, err := h.upd.installSource(&update.Manifest{Version: "0.7.0"}); err == nil {
		t.Fatal("accepted a stable manifest for a beta install")
	}
}

func TestBetaSettingsRejectCustomSourcesAndBusySwitches(t *testing.T) {
	for _, custom := range []string{"file:///private/releases", "https://mirror.example.com/dl"} {
		h := newTestHub(t)
		h.build = update.Make("v1", update.Release, "202611171200", custom)
		srv := httptest.NewServer(h.routes())
		req := httptest.NewRequest(http.MethodPut, "/api/v1/hub/update/settings", strings.NewReader(`{"source":"beta"}`))
		req.RemoteAddr = net.JoinHostPort(netip.MustParseAddr("127.0.0.1").String(), "1")
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(w, req)
		srv.Close()
		if w.Code != http.StatusConflict || h.upd.settings.Source == update.ChannelBeta {
			t.Fatalf("custom source %s: %d", custom, w.Code)
		}
	}
	h := newTestHub(t)
	h.build = update.Make("v1", update.Release, "202611171200", "https://github.com/nwverse/persona/releases/latest/download")
	h.upd.settings.Source, h.upd.running = update.ChannelBeta, true
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/hub/update/settings", strings.NewReader(`{"source":"release"}`))
	req.RemoteAddr = net.JoinHostPort(netip.MustParseAddr("127.0.0.1").String(), "1")
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	w := httptest.NewRecorder()
	srv.Config.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || h.upd.settings.Source != update.ChannelBeta {
		t.Fatalf("busy switch: %d", w.Code)
	}
}
