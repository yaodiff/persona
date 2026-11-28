package extmgr

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

type spacesMigrationTestStore struct {
	*extensionStore
	tracks []db.Track
}

func (s *spacesMigrationTestStore) Tracks(context.Context) ([]db.Track, error) {
	return s.tracks, nil
}

func spacesMigrationManager(t *testing.T, tracks []db.Track) (*Manager, *spacesMigrationTestStore) {
	t.Helper()
	h := newTestManager(t)
	st := &spacesMigrationTestStore{extensionStore: newExtensionStore(), tracks: tracks}
	h.store = st
	startTestExtensions(t, h)
	return h, st
}

func TestSpacesMigrationFreshHubDoesNotInstall(t *testing.T) {
	h, st := spacesMigrationManager(t, nil)
	done, err := h.migrateSpaces(t.Context())
	if err != nil || !done {
		t.Fatalf("fresh migration: %v %v", done, err)
	}
	if h.extensions.reg.Get(spacesMigrationID) != nil {
		t.Fatal("a fresh hub received the extension")
	}
	var state spacesMigrationState
	if found, _ := st.Setting(t.Context(), spacesMigrationKey, &state); !found || state.Status != "not_needed" {
		t.Fatalf("state: %+v", state)
	}
}

func TestSpacesMigrationDisabledChoiceWins(t *testing.T) {
	h, _ := spacesMigrationManager(t, []db.Track{{Slug: "existing", Kind: "space"}})
	manifest, err := extension.Parse([]byte(`{"manifest_version":1,"id":"space-conversations","name":"Spaces","version":"0.1.0","engines":{"plugin_protocol":1},"runtime":{"command":{"linux-amd64":"bin/spaces"}},"contributes":{"ui":[{"id":"home","title":"Spaces","placement":"nav_page"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.AddHubJSON(manifest, nil, false); err != nil {
		t.Fatal(err)
	}
	done, err := h.migrateSpaces(t.Context())
	if err != nil || !done || h.extensions.reg.Get(spacesMigrationID).Record.Enabled {
		t.Fatalf("disabled choice was overwritten: %v %v", done, err)
	}
}

func TestSpacesMigrationInstallsVerifiedOfficialPackage(t *testing.T) {
	h, _ := spacesMigrationManager(t, []db.Track{{Slug: "existing", Kind: "space"}})
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	dir := uiPackage(t)
	manifestPath := filepath.Join(dir, extension.ManifestFile)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatal(err)
	}
	m["id"] = spacesMigrationID
	if err := os.WriteFile(filepath.Join(dir, "bin", "ui"), []byte("#!/bin/sh\nEXAMPLE_UI_ID=space-conversations PERSONA_TEST_PLUGIN=ui exec "+os.Args[0]+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m["contributes"] = map[string]any{"track_kinds": []any{map[string]any{"id": "space", "title": "Space", "config": map[string]any{
		"schema": 1, "template": "space", "context": "track", "default_categories": []string{}, "tag_focus": []string{}, "rhythms": []any{},
		"attention": map[string]any{"push_cap": "P0", "deep_default": "quick", "enter_mode": "none"}, "supervision": "none",
		"usage": []any{}, "assembly": "track", "channels": []string{}, "sidebar": map[string]any{"group": "spaces"},
	}}}}
	manifest, _ = json.Marshal(m)
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := tarDir(t, dir)
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: map[string][]byte{"space.tar.gz": pkg}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	gh.files[extension.IndexFile] = key.sign(marketIndex(t, "Official", marketEntry(t, dir, "release:v1/space.tar.gz", pkg)))
	h.extensions.markets.client = srv.Client()
	h.extensions.markets.githubAPI = srv.URL
	done, err := h.migrateSpaces(t.Context())
	if err != nil || !done {
		t.Fatalf("migration: %v %v", done, err)
	}
	installed := h.extensions.reg.Get(spacesMigrationID)
	if installed == nil || !installed.Record.Enabled || len(installed.Manifest.TrackKinds()) != 1 {
		t.Fatalf("installed: %+v", installed)
	}
	items, err := h.DomainTrackKinds(t.Context())
	if err != nil || len(items) != 1 || items[0].ID != "space" {
		t.Fatalf("contributed kinds: %+v, %v", items, err)
	}
	// The assistant is told which extension contributes the kind (assistant/track_space_md.go spaceStateRefusal).
	raw, err := h.DomainTrackKindsJSON(t.Context())
	var withOwner []struct {
		ID        string `json:"id"`
		Extension string `json:"extension"`
	}
	if err != nil || json.Unmarshal(raw, &withOwner) != nil || len(withOwner) != 1 || withOwner[0].ID != "space" ||
		withOwner[0].Extension != spacesMigrationID {
		t.Fatalf("contributed kinds for the assistant: %s, %v", raw, err)
	}
	// A disabled choice made after installation is never reversed by the next upgrade.
	if err := h.extensions.reg.SetEnabled(t.Context(), spacesMigrationID, false, "test"); err != nil {
		t.Fatal(err)
	}
	if done, err := h.migrateSpaces(t.Context()); err != nil || !done || h.extensions.reg.Get(spacesMigrationID).Record.Enabled {
		t.Fatalf("disabled extension was reinstalled: %v %v", done, err)
	}
}
