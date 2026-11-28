package db

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// settingsStoragePackage is a package that declares business storage, one setting and one contribution.
func settingsStoragePackage(t *testing.T, id string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"manifest_version": 1, "id": id, "name": id, "version": "1.0.0",
		"permissions":   map[string]any{"storage": map[string]any{"patterns": []string{"state/*"}}},
		"configuration": map[string]any{"type": "object", "properties": map[string]any{"saved": map[string]string{"type": "string"}}},
		"contributes":   map[string]any{"providers": []map[string]string{{"id": "fixture", "name": id, "base_url": "https://provider.example.com"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, extension.ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every settings change restarts the extension from the registry's record, and its new run binds storage with that
// record's revision: the record must carry the revision the change stored, or the extension's own data is refused
// as a retired installation until the hub restarts.
func TestExtensionStorageAfterSettingsChange(t *testing.T) {
	d, ctx := open(t), t.Context()
	const id = "fixture-settings-storage"
	cleanup := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, q := range []string{`DELETE FROM extension_storage_spaces WHERE extension_key=$1`, `DELETE FROM plugin_audit WHERE plugin=$1`, `DELETE FROM plugins WHERE id=$1`} {
			if _, err := d.pool.Exec(cleanupCtx, q, id); err != nil {
				t.Error(err)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	reg := extension.NewRegistry(t.TempDir(), d, planTestSealer{}, 202611100000)
	pv, err := reg.Preview(ctx, settingsStoragePackage(t, id), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	runs := 0
	start := func(update StorageUpdate) (any, error) { // as the manager starts a run (extmgr storageScope)
		p := reg.SnapshotExact(id)
		runs++
		scope := extension.StorageScope{Plugin: p.Record.StorageKey(), PublicID: p.Record.ID, InstalledAt: p.Record.InstalledAt,
			UpdatedAt: p.Record.UpdatedAt, Run: fmt.Sprint("run-", runs), Granted: p.Record.Granted.Storage, Start: true}
		return d.ExtensionStorage(ctx, scope, fixtureInitial(), update)
	}
	if value, err := start(fixtureStorageUpdate("kept")); err != nil || value != "kept" {
		t.Fatal("a fresh installation could not write its storage", value, err)
	}
	for _, change := range []struct {
		name string
		do   func() error
	}{
		{"config", func() error { return reg.SetConfig(ctx, id, json.RawMessage(`{"saved":"changed"}`), "test") }},
		{"switch", func() error {
			if err := reg.SetEnabled(ctx, id, false, "test"); err != nil {
				return err
			}
			return reg.SetEnabled(ctx, id, true, "test")
		}},
		{"contribution", func() error { return reg.SetContribution(ctx, id, "providers", "fixture", false, "test") }},
	} {
		t.Run(change.name, func(t *testing.T) {
			if err := change.do(); err != nil {
				t.Fatal(err)
			}
			if value, err := start(fixtureStorageUpdate("read")); err != nil || value != "kept" {
				t.Fatal("the restarted extension could not read its own storage", value, err)
			}
		})
	}
}
