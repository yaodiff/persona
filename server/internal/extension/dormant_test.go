package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"testing"
)

// Dormant rows and adoption (#16263, dormant.go), in memory: these prove the registry's rules. The database's own
// guarantees (an install never overwrites a built-in's row, a stale snapshot never wins) are in
// internal/db's TestOrphanAdoptionThroughRegistry and TestExtensionOrphanAdoptionCAS.

const dormantID = "speech-example"

// dormantMarket signs catalogs for tests: the official market with a test key in place of the compiled-in one, or a
// private market with its own key.
type dormantMarket struct {
	spec, id string
	key      string
	sign     func([]byte) []byte
}

func officialTestMarket(t *testing.T) *dormantMarket {
	t.Helper()
	key, sign := testKey(t)
	previous := Official
	Official.SigningKey = key
	t.Cleanup(func() { Official = previous })
	return &dormantMarket{spec: Official.Spec, id: "official", key: key, sign: sign}
}

func privateTestMarket(t *testing.T) *dormantMarket {
	t.Helper()
	key, sign := testKey(t)
	return &dormantMarket{spec: "github:example/private-plugins", id: "private", key: key, sign: sign}
}

// dormantManifest is the example package as id at version, with its UI page; change edits it further.
func dormantManifest(version string, change func(map[string]any)) func(map[string]any) {
	return func(m map[string]any) {
		m["id"], m["version"] = dormantID, version
		m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "status", "title": "Status", "placement": "plugin_page"}}
		if change != nil {
			change(m)
		}
	}
}

// preview is r's preview of the package change writes, from this market (upgrade as Preview takes it).
func (mk *dormantMarket) preview(t *testing.T, r *Registry, upgrade string, change func(map[string]any)) (*Preview, error) {
	t.Helper()
	dir := t.TempDir()
	writePackage(t, dir, change)
	raw, err := os.ReadFile(ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	archive := tarGz(t, map[string]string{ManifestFile: string(raw), "bin/notes": "#!/bin/sh\n"})
	sum := sha256.Sum256(archive)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) }))
	t.Cleanup(srv.Close)
	spec, err := ParseMarket(mk.spec)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	entry := MarketEntry{Manifest: raw, Parsed: m, Packages: []MarketPackage{{URL: srv.URL + "/pkg.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}
	payload, err := json.Marshal(MarketIndex{MarketVersion: 1, Name: "Example", Extensions: []MarketEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := ParseCatalog(mk.sign(payload), mk.key)
	if err != nil {
		t.Fatal(err)
	}
	return r.PreviewMarket(t.Context(), &Market{Spec: spec, Client: srv.Client(), TrustedKey: mk.key}, mk.id, &verified.Index.Extensions[0], upgrade)
}

// dormantBuiltin is the built-in of dormantID (version 9.0.0, no grants of its own), as a module build registers it.
func dormantBuiltin(t *testing.T, change func(map[string]any)) *Manifest {
	t.Helper()
	dir := t.TempDir()
	writePackage(t, dir, dormantManifest("9.0.0", func(m map[string]any) {
		delete(m, "permissions")
		if change != nil {
			change(m)
		}
	}))
	raw, err := os.ReadFile(ManifestPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	return mustParse(t)(raw)
}

// moduleBuild is a registry on store as a build with the built-in b starts (b nil: a build without it).
func moduleBuild(t *testing.T, dir string, store Store, b *Manifest, off bool) *Registry {
	t.Helper()
	r := NewRegistry(dir, store, rot13{}, 202611100000)
	if b != nil {
		if err := r.AddBuiltin(b, t.TempDir(), false); err != nil {
			t.Fatal(err)
		}
		r.AllowBuiltinHandoff(b.ID)
	}
	r.HoldModule(dormantID, off)
	if err := r.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	return r
}

// ownerChoices gives the built-in of a module build the owner's settings, switch, a switched-off contribution and a
// key: its row is what a build without it later finds.
func ownerChoices(t *testing.T, r *Registry) {
	t.Helper()
	ctx := t.Context()
	if err := r.SetConfig(ctx, dormantID, json.RawMessage(`{"folders":["work"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, dormantID, "api_key", "synthetic test value", "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetContribution(ctx, dormantID, "ui", "status", false, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetEnabled(ctx, dormantID, true, "test"); err != nil {
		t.Fatal(err)
	}
}

// orphanFixture is a store holding the built-in's row with the owner's choices, and a registry of a build without
// the built-in on it.
func orphanFixture(t *testing.T, b *Manifest) (*Registry, *memStore, string) {
	t.Helper()
	store := newMemStore()
	dir := t.TempDir()
	ownerChoices(t, moduleBuild(t, dir, store, b, false))
	return moduleBuild(t, dir, store, nil, false), store, dir
}

// unchanged fails t unless the store holds exactly rows and secrets and the registry lists nothing of the id.
func unchanged(t *testing.T, r *Registry, store *memStore, rows map[string]Record, secrets map[string]map[string]string) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	if !reflect.DeepEqual(store.rows, rows) || !reflect.DeepEqual(store.secrets, secrets) {
		t.Fatalf("the stored row or its secrets changed:\n%+v\n%+v", store.rows[dormantID], rows[dormantID])
	}
	if r.Get(dormantID) != nil || r.SnapshotExact(dormantID) != nil {
		t.Fatal("a dormant row is listed")
	}
}

func storeState(store *memStore) (map[string]Record, map[string]map[string]string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	rows := map[string]Record{}
	for k, v := range store.rows {
		rows[k] = snapshotExtension(&Extension{Record: v}).Record
	}
	secrets := map[string]map[string]string{}
	for k, v := range store.secrets {
		secrets[k] = map[string]string{}
		for n, s := range v {
			secrets[k][n] = s
		}
	}
	return rows, secrets
}

// A row nothing may serve never stops the registry: a built-in's row on a build without it, one whose manifest
// cannot be read, and any row of a module the modules setting switched off are set aside, not listed and not changed.
func TestOrphanRowNeverBlocksLoad(t *testing.T) {
	r, store, dir := orphanFixture(t, dormantBuiltin(t, nil))
	rows, secrets := storeState(store)
	if rows[dormantID].Source != SourceBuiltin || len(rows[dormantID].Manifest) == 0 {
		t.Fatalf("the built-in's row: %+v", rows[dormantID])
	}
	unchanged(t, r, store, rows, secrets)
	r.mu.Lock()
	_, held := r.dormant[dormantID]
	r.mu.Unlock()
	if !held {
		t.Fatal("the orphan is not held")
	}

	// An unreadable manifest is no reason to refuse to start either.
	store.mu.Lock()
	broken := store.rows[dormantID]
	broken.Manifest = json.RawMessage(`{"contributes": 7}`)
	store.rows[dormantID] = broken
	store.mu.Unlock()
	rows, secrets = storeState(store)
	again := moduleBuild(t, dir, store, nil, false)
	unchanged(t, again, store, rows, secrets)
	pv, err := officialTestMarket(t).preview(t, again, "", dormantManifest("1.0.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := again.Install(t.Context(), pv.ID, pv.Digest, "test"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("adopted a row whose contributions are unknown: %v", err)
	}
	unchanged(t, again, store, rows, secrets)

	// A module build whose modules setting switched the module off holds even its own built-in's row.
	store.mu.Lock()
	store.rows[dormantID] = snapshotExtension(&Extension{Record: rows[dormantID]}).Record
	store.mu.Unlock()
	off := NewRegistry(dir, store, rot13{}, 202611100000)
	off.HoldModule(dormantID, true)
	if err := off.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	unchanged(t, off, store, rows, secrets)
}

// An install from the official market, verified with its current key, takes the orphaned built-in's row over in
// place: the owner's settings, switch, switched-off contribution, key and installation time stay, and the row is the
// built-in's verified successor. Restarting on either build reads it as an installed package.
func TestOrphanAdoptedByOfficialPackage(t *testing.T) {
	b := dormantBuiltin(t, nil)
	r, store, dir := orphanFixture(t, b)
	ctx := t.Context()
	rows, secrets := storeState(store)
	orphan := rows[dormantID]
	official := officialTestMarket(t)
	pv, err := official.preview(t, r, "", dormantManifest("1.0.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	if pv.Upgrade {
		t.Fatal("the orphan is not listed, so the catalog offers an install")
	}
	var changes []Change
	r.OnChange = func(c Change) { changes = append(changes, c) }
	p, err := r.Install(ctx, pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	check := func(p *Extension) {
		t.Helper()
		rec := p.Record
		if rec.Source != SourceInstalled || !rec.BuiltinPredecessor || !rec.VerifiedBuiltinPredecessor() ||
			rec.PreviousOrigin != "builtin:"+dormantID || rec.PreviousVersion != "9.0.0" || rec.Version != "1.0.0" ||
			!rec.Enabled || !sameJSON(rec.Config, orphan.Config) || !slices.Equal(rec.DisabledContributions, orphan.DisabledContributions) ||
			!rec.InstalledAt.Equal(orphan.InstalledAt) || rec.StorageKey() != dormantID {
			t.Fatalf("adopted %+v\nfrom %+v", rec, orphan)
		}
	}
	check(p)
	if len(changes) != 1 || changes[0].Kind != "installed" {
		t.Fatalf("changes %+v", changes)
	}
	store.mu.Lock()
	if !reflect.DeepEqual(store.secrets, secrets) {
		t.Fatal("the key moved")
	}
	store.mu.Unlock()
	r.mu.Lock()
	_, still := r.dormant[dormantID]
	r.mu.Unlock()
	if still {
		t.Fatal("the adopted row is still held")
	}
	check(moduleBuild(t, dir, store, nil, false).Get(dormantID))
	withBuiltin := moduleBuild(t, dir, store, b, false)
	check(withBuiltin.Get(dormantID))
	back, err := withBuiltin.Rollback(ctx, dormantID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if back.Record.Source != SourceBuiltin || !sameJSON(back.Record.Config, orphan.Config) || !back.Record.Enabled {
		t.Fatalf("back to the built-in: %+v", back.Record)
	}
}

// Nothing but the official market's package, verified with its current key and serving everything the built-in
// served, takes an orphan over; nothing takes over a row of a module switched off. Each refusal writes nothing.
func TestOrphanAdoptionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		builtin func(map[string]any)
		off     bool
		preview func(t *testing.T, r *Registry) (*Preview, error)
	}{
		{name: "local package", preview: func(t *testing.T, r *Registry) (*Preview, error) {
			dir := t.TempDir()
			writePackage(t, dir, dormantManifest("1.0.0", nil))
			return r.Preview(t.Context(), "dir:"+dir, "")
		}},
		{name: "private market", preview: func(t *testing.T, r *Registry) (*Preview, error) {
			officialTestMarket(t)
			return privateTestMarket(t).preview(t, r, "", dormantManifest("1.0.0", nil))
		}},
		{name: "retired official key", preview: func(t *testing.T, r *Registry) (*Preview, error) {
			official := officialTestMarket(t)
			Official.PreviousSigningKeys = []string{official.key}
			Official.SigningKey, _ = testKey(t)
			return official.preview(t, r, "", dormantManifest("1.0.0", nil))
		}},
		{name: "missing contribution", builtin: func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "status", "title": "Status", "placement": "plugin_page"},
				map[string]any{"id": "built-in-only", "title": "Only", "placement": "plugin_page"}}
		}, preview: func(t *testing.T, r *Registry) (*Preview, error) {
			return officialTestMarket(t).preview(t, r, "", dormantManifest("1.0.0", nil))
		}},
		{name: "module switched off", off: true, preview: func(t *testing.T, r *Registry) (*Preview, error) {
			return officialTestMarket(t).preview(t, r, "", dormantManifest("1.0.0", nil))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMemStore()
			dir := t.TempDir()
			b := dormantBuiltin(t, tc.builtin)
			ownerChoices(t, moduleBuild(t, dir, store, b, false))
			r := moduleBuild(t, dir, store, nil, tc.off)
			rows, secrets := storeState(store)
			pv, err := tc.preview(t, r)
			if err != nil {
				t.Logf("refused at preview: %v", err)
				unchanged(t, r, store, rows, secrets)
				return // refused before the commit: fine as well
			}
			if _, err := r.Install(t.Context(), pv.ID, pv.Digest, "test"); !errors.Is(err, ErrIdentity) {
				t.Fatalf("expected ErrIdentity, got %v", err)
			}
			unchanged(t, r, store, rows, secrets)
		})
	}
}

// A package rolled back to its first version on a build without the built-in loses the way back to it (there was no
// built-in to name); on a build with it again, rollback reaches the built-in with the owner's choices (#16263).
func TestRollbackReachesBuiltinAfterCrossBuildPackageRollback(t *testing.T) {
	ctx := t.Context()
	b := dormantBuiltin(t, nil)
	store := newMemStore()
	dir := t.TempDir()
	official := officialTestMarket(t)
	withModule := moduleBuild(t, dir, store, b, false)
	ownerChoices(t, withModule)
	before, _ := storeState(store)
	pv, err := official.preview(t, withModule, dormantID, dormantManifest("1.0.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = withModule.UpgradePlan(ctx, dormantID, pv.ID, pv.Digest, nil, "test"); err != nil {
		t.Fatal(err)
	}
	pv, err = official.preview(t, withModule, dormantID, dormantManifest("2.0.0", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = withModule.Upgrade(ctx, dormantID, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}

	without := moduleBuild(t, dir, store, nil, false)
	p, err := without.Rollback(ctx, dormantID, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.Version != "1.0.0" || p.Record.PreviousVersion != "" || p.Record.PreviousOrigin != "" || !p.Record.VerifiedBuiltinPredecessor() {
		t.Fatalf("rolled back without the built-in: %+v", p.Record)
	}
	if _, err := without.Rollback(ctx, dormantID, "test"); err == nil {
		t.Fatal("rolled back further with no built-in to go to")
	}

	again := moduleBuild(t, dir, store, b, false)
	back, err := again.Rollback(ctx, dormantID, "test")
	if err != nil {
		t.Fatal(err)
	}
	want := before[dormantID]
	if back.Record.Source != SourceBuiltin || back.Record.BuiltinPredecessor || !back.Record.Enabled ||
		!sameJSON(back.Record.Config, want.Config) || !slices.Equal(back.Record.DisabledContributions, want.DisabledContributions) {
		t.Fatalf("back to the built-in: %+v", back.Record)
	}
	store.mu.Lock()
	cipher := store.secrets[dormantID]["api_key"]
	store.mu.Unlock()
	if v, err := (rot13{}).Open(cipher); err != nil || v != "synthetic test value" {
		t.Fatalf("the key: %q %v", v, err)
	}
}
