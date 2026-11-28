package db

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

// Each snapshot represents a different manager. Even two writes in the same
// microsecond must not let a handoff restore consent or settings from a stale one.
func TestExtensionBuiltinHandoffCAS(t *testing.T) {
	d, ctx := open(t), t.Context()
	const id = "builtin-cas-test"
	cleanupStorageFixture(t, d, id)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	builtin := extension.Record{
		ID: id, Source: extension.SourceBuiltin, Version: "1.0.0", Manifest: json.RawMessage("{}"),
		Config: json.RawMessage("{}"), Enabled: true, InstalledAt: now, UpdatedAt: now,
	}
	read := func() extension.Record {
		t.Helper()
		rows, err := d.Extensions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(rows, func(r extension.Record) bool { return r.StorageKey() == id })
		if i < 0 {
			t.Fatal("missing built-in storage slot")
		}
		return rows[i]
	}
	write := func(r extension.Record, want error) {
		t.Helper()
		if err := d.PutExtension(ctx, r); !errors.Is(err, want) {
			t.Fatalf("write %s: expected %v, got %v", r.Source, want, err)
		}
	}
	cutover := func(before extension.Record, source string) extension.Record {
		r := before
		r.Source = source
		r.ExpectedSource, r.ExpectedID, r.ExpectedVersion = before.Source, before.ID, before.Version
		r.ExpectedUpdatedAt = before.UpdatedAt
		r.BuiltinPredecessor = source == extension.SourceInstalled
		return r
	}

	write(builtin, nil)
	first := read()
	if first.BuiltinPredecessor {
		t.Fatal("fresh built-in incorrectly marked as a replacement")
	}
	if err := d.PutExtensionSecret(ctx, id, "key", "sealed-fixture"); err != nil {
		t.Fatal(err)
	}
	// A built-in owns business data in the same stable slot as its credentials.
	if _, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id, plugin, extension_key, installed_at, state)
		VALUES ('builtin-cas-test-space', $1, $1, $2, '{"notes":"retained"}')`, id, first.InstalledAt); err != nil {
		t.Fatal(err)
	}
	edited := first
	edited.ExpectedSource = extension.SourceBuiltin
	edited.Config = json.RawMessage(`{"city":"new"}`)
	edited.Enabled = false
	edited.DisabledContributions = []string{"ui/summary"}
	write(edited, nil)

	write(cutover(first, extension.SourceInstalled), extension.ErrSettingsMoved)
	current := read()
	if current.Source != extension.SourceBuiltin || !current.UpdatedAt.After(first.UpdatedAt) {
		t.Fatal("first cutover overwrote a concurrently edited built-in")
	}
	pv := cutover(current, extension.SourceInstalled)
	pv.GrantedDigest = "new-permission-digest"
	write(pv, nil)
	write(pv, extension.ErrIdentity)
	write(edited, extension.ErrIdentity)
	installed := read()
	if !installed.BuiltinPredecessor || installed.Enabled || !slices.Equal(installed.DisabledContributions, edited.DisabledContributions) || installed.GrantedDigest != pv.GrantedDigest {
		t.Fatalf("replacement lost the marker or owner choices: %+v", installed)
	}
	fresh := pv
	fresh.ExpectedID, fresh.ExpectedSource, fresh.NewInstallation = "", "", true
	write(fresh, extension.ErrIdentity)

	rollback := cutover(installed, extension.SourceBuiltin)
	revised := installed
	revised.ExpectedSource = extension.SourceInstalled
	revised.GrantedDigest = "revised-grant"
	revised.Config = json.RawMessage(`{"city":"latest"}`)
	write(revised, nil)
	write(rollback, extension.ErrSettingsMoved)
	latest := read()
	if latest.Source != extension.SourceInstalled || latest.GrantedDigest != revised.GrantedDigest {
		t.Fatal("stale rollback changed the installed grant")
	}
	write(cutover(latest, extension.SourceBuiltin), nil)
	write(rollback, extension.ErrIdentity)
	write(revised, extension.ErrIdentity)
	if err := d.DeleteExtensionRecord(ctx, installed); !errors.Is(err, extension.ErrIdentity) {
		t.Fatalf("stale uninstall reached a restored built-in of the same version: %v", err)
	}
	for _, keepData := range []bool{true, false} {
		if err := d.DeleteExtensionRecordRetainingData(ctx, installed, keepData); !errors.Is(err, extension.ErrIdentity) {
			t.Fatalf("stale uninstall with keepData=%v: %v", keepData, err)
		}
	}
	var notes string
	if err := d.pool.QueryRow(ctx, `SELECT state->>'notes' FROM extension_storage_spaces
		WHERE plugin=$1 AND installed_at=$2 AND NOT retained`, id, first.InstalledAt).Scan(&notes); err != nil || notes != "retained" {
		t.Fatalf("rollback lost its active storage space: %v", err)
	}
	restored := read()
	var config map[string]string
	if err := json.Unmarshal(restored.Config, &config); err != nil || config["city"] != "latest" ||
		restored.BuiltinPredecessor || restored.Source != extension.SourceBuiltin || restored.Enabled {
		t.Fatalf("rollback did not retain current settings and switches: %+v", restored)
	}
	secrets, err := d.ExtensionSecrets(ctx, id)
	if err != nil || secrets["key"] != "sealed-fixture" {
		t.Fatalf("secret did not survive cutover and rollback: %v", err)
	}
}

// An orphaned built-in row (#16263: its module is not in this build) is taken over only by the adoption write the
// registry prepares (extension/dormant.go), pinned to the row as it was read. ① A plain new install never overwrites
// it. ② The adoption takes the same slot in place: settings, switch and the sealed key stay. ③ Of two concurrent
// adoptions only one wins. ④ A snapshot older than the row is refused.
// The same holds for a row of the built-in voice extension's shape (#16264): two contribution points (calls and
// meeting transcription), the full hub token for the built-in, and a package granted named scopes and its address.
func TestExtensionOrphanAdoptionCAS(t *testing.T) {
	voicePoints := `"contributes":{"voice":[{"id":"voice","name":"Voice"}],"transcription":[{"id":"gpt-live","name":"GPT-Live"}]}`
	for _, tc := range []struct {
		name, id, builtin, pkg string
		granted                extension.Permissions
	}{
		{"generic", "orphan-cas-test", `{"id":"orphan-cas-test"}`, `{"id":"orphan-cas-test","version":"1.0.0"}`, extension.Permissions{}},
		{"voice-shaped", "voice-orphan-cas-test",
			`{"id":"voice-orphan-cas-test",` + voicePoints + `,"permissions":{"hub_api":true}}`,
			`{"id":"voice-orphan-cas-test","version":"1.0.0",` + voicePoints + `,"permissions":{"hub":["kb.read","agents.read","agents.control","machines.read","assistant.ask"],"network":["config:url:http"]}}`,
			extension.Permissions{Hub: []string{"kb.read", "agents.read", "agents.control", "machines.read", "assistant.ask"}, Network: []string{"config:url:http"}}},
	} {
		t.Run(tc.name, func(t *testing.T) { orphanAdoptionCAS(t, tc.id, tc.builtin, tc.pkg, tc.granted) })
	}
}

func orphanAdoptionCAS(t *testing.T, id, builtinManifest, pkgManifest string, granted extension.Permissions) {
	d, ctx := open(t), t.Context()
	cleanupStorageFixture(t, d, id)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	if err := d.PutExtension(ctx, extension.Record{
		ID: id, Source: extension.SourceBuiltin, Version: "9.0.0", Manifest: json.RawMessage(builtinManifest),
		Config: json.RawMessage(`{"voice":"kept"}`), Enabled: true, DisabledContributions: []string{"ui/status"}, InstalledAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutExtensionSecret(ctx, id, "key", "sealed-fixture"); err != nil {
		t.Fatal(err)
	}
	raw := func() string {
		t.Helper()
		var s string
		if err := d.pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM plugins p WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	read := func() extension.Record {
		t.Helper()
		rows, err := d.Extensions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(rows, func(r extension.Record) bool { return r.StorageKey() == id })
		if i < 0 {
			t.Fatal("missing the orphan's slot")
		}
		return rows[i]
	}
	orphan := read()
	pkg := extension.Record{ID: id, Source: extension.SourceInstalled, Origin: "market:official/" + id, Version: "1.0.0",
		Manifest: json.RawMessage(pkgManifest), Granted: granted, GrantedDigest: "package-digest",
		MarketKey: "ed25519:fixture", Config: json.RawMessage(`{}`), Enabled: true, InstalledAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour)}

	// ① A plain new install (what an install over an unknown row prepares) is refused and leaves the row's bytes.
	before := raw()
	fresh := pkg
	fresh.NewInstallation = true
	if err := d.PutExtension(ctx, fresh); !errors.Is(err, extension.ErrIdentity) {
		t.Fatalf("① a new install over the orphan: %v", err)
	}
	if raw() != before {
		t.Fatal("① the refused install changed the row")
	}

	adoption := func(from extension.Record) extension.Record {
		r := pkg
		r.Config, r.Enabled, r.DisabledContributions, r.PackOff = from.Config, from.Enabled, from.DisabledContributions, from.PackOff
		r.BuiltinPredecessor, r.PreviousOrigin, r.PreviousVersion = true, "builtin:"+id, from.Version
		r.ExpectedID, r.ExpectedVersion, r.ExpectedSource, r.ExpectedUpdatedAt = from.ID, from.Version, from.Source, from.UpdatedAt
		r.InstalledAt = from.InstalledAt
		return r
	}

	// ④ The row moved after the snapshot (another manager saved a setting): the stale adoption is refused.
	stale := adoption(orphan)
	moved := orphan
	moved.ExpectedSource = extension.SourceBuiltin
	moved.UpdatedAt = now.Add(time.Minute)
	if err := d.PutExtension(ctx, moved); err != nil {
		t.Fatal(err)
	}
	before = raw()
	if err := d.PutExtension(ctx, stale); !errors.Is(err, extension.ErrSettingsMoved) {
		t.Fatalf("④ a stale adoption: %v", err)
	}
	if raw() != before {
		t.Fatal("④ the stale adoption changed the row")
	}

	// ③ Two adoptions from the same current snapshot: exactly one wins.
	current := read()
	results := make(chan error, 2)
	for range 2 {
		go func() { results <- d.PutExtension(ctx, adoption(current)) }()
	}
	var won, lost int
	for range 2 {
		switch err := <-results; {
		case err == nil:
			won++
		case errors.Is(err, extension.ErrIdentity) || errors.Is(err, extension.ErrSettingsMoved):
			lost++
		default:
			t.Fatalf("③ unexpected: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("③ %d adoptions won, %d lost", won, lost)
	}

	// ② The winner took the slot in place.
	got := read()
	if got.Source != extension.SourceInstalled || !got.BuiltinPredecessor || got.PreviousOrigin != "builtin:"+id || got.PreviousVersion != "9.0.0" ||
		!got.Enabled || string(compactJSON(t, got.Config)) != `{"voice":"kept"}` || !slices.Equal(got.DisabledContributions, []string{"ui/status"}) ||
		!got.InstalledAt.Equal(current.InstalledAt) || got.StorageKey() != id {
		t.Fatalf("② adopted row %+v", got)
	}
	secrets, err := d.ExtensionSecrets(ctx, id)
	if err != nil || secrets["key"] != "sealed-fixture" {
		t.Fatalf("② the key under the same storage key: %v %v", secrets, err)
	}
	if !slices.Equal(got.Granted.Hub, granted.Hub) || !slices.Equal(got.Granted.Network, granted.Network) || got.Granted.HubAPI {
		t.Fatalf("② the grant: %+v, want %+v", got.Granted, granted)
	}
	// And a later plain install still cannot take it.
	if err := d.PutExtension(ctx, fresh); !errors.Is(err, extension.ErrIdentity) {
		t.Fatalf("a new install over the adopted row: %v", err)
	}
}

func compactJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return out
}
