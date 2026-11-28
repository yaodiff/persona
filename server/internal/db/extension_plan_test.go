package db

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

type planTestStore struct {
	*DB
	commits int
}

func (s *planTestStore) PutExtensionPlan(ctx context.Context, rows []extension.Record) error {
	s.commits++
	return s.DB.PutExtensionPlan(ctx, rows)
}

type planTestSealer struct{}

func (planTestSealer) Seal(s string) (string, error) { return "synthetic:" + s, nil }
func (planTestSealer) Open(s string) (string, error) { return strings.TrimPrefix(s, "synthetic:"), nil }

func planTestPackage(t *testing.T, id, version string, pack bool) string {
	t.Helper()
	m := map[string]any{"manifest_version": 1, "id": id, "name": id, "version": version}
	if pack {
		m["extension_pack"] = []map[string]string{{"id": "a-existing", "version": ">=2.0.0"}, {"id": "b-new"}}
	} else {
		m["contributes"] = map[string]any{"providers": []map[string]string{{"id": id + "-provider", "name": id, "base_url": "https://provider.example.com"}}}
		m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{"saved": map[string]string{"type": "string"}}}
		m["secrets"] = []map[string]any{{"name": "api_key", "title": "Example key", "required": true}}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.WriteFile(filepath.Join(dir, extension.ManifestFile), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "README.md"), []byte(id+" "+version), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestExtensionPlanPostgresFailureRestoresPackagesAndRecords(t *testing.T) {
	// Without CREATE DATABASE, dbtest falls back to a shared database; fault triggers are never allowed in that mode.
	u, err := url.Parse(os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil || !regexp.MustCompile(`^/persona_test_[0-9]{10}_[0-9a-f]{8}$`).MatchString(u.Path) {
		t.Skip("fault injection requires dbtest's per-run isolated database; shared fallback is not allowed")
	}
	for _, failure := range []string{"prevalidation", "second", "main", "commit"} {
		t.Run(failure, func(t *testing.T) {
			d, ctx := open(t), t.Context()
			cleanupRows := func() {
				t.Helper()
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, err := d.pool.Exec(cleanupCtx, `DELETE FROM plugin_audit WHERE plugin IN ('a-existing','b-new','z-pack'); DELETE FROM plugins WHERE id IN ('a-existing','b-new','z-pack')`); err != nil {
					t.Error(err)
				}
			}
			cleanupRows()
			t.Cleanup(cleanupRows)
			root := t.TempDir()
			store := &planTestStore{DB: d}
			reg := extension.NewRegistry(root, store, planTestSealer{}, 202611100000)
			preview := func(id, version, upgrade string, pack bool) *extension.Preview {
				t.Helper()
				pv, err := reg.Preview(ctx, planTestPackage(t, id, version, pack), upgrade)
				if err != nil {
					t.Fatal(err)
				}
				return pv
			}
			first := preview("a-existing", "0.9.0", "", false)
			if _, err := reg.Install(ctx, first.ID, first.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			first = preview("a-existing", "1.0.0", "a-existing", false)
			if _, err := reg.Upgrade(ctx, "a-existing", first.ID, first.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			if err := reg.SetConfig(ctx, "a-existing", json.RawMessage(`{"saved":"retained"}`), "test"); err != nil {
				t.Fatal(err)
			}
			if err := reg.SetSecret(ctx, "a-existing", "api_key", "synthetic-value", "test"); err != nil {
				t.Fatal(err)
			}
			if err := reg.SetContribution(ctx, "a-existing", "providers", "a-existing-provider", false, "test"); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(reg.DataDir("a-existing"), 0o700); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(reg.DataDir("a-existing"), "retained.txt")
			if err := os.WriteFile(data, []byte("synthetic user data"), 0o600); err != nil {
				t.Fatal(err)
			}
			beforeReg := reg.Snapshot("a-existing").Record
			beforeRows, err := d.Extensions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeSecrets, err := d.ExtensionSecrets(ctx, "a-existing")
			if err != nil {
				t.Fatal(err)
			}
			var beforeSecretTime time.Time
			if err = d.pool.QueryRow(ctx, `SELECT updated_at FROM plugin_secrets WHERE plugin='a-existing' AND name='api_key'`).Scan(&beforeSecretTime); err != nil {
				t.Fatal(err)
			}
			var beforeAudit int
			if err = d.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_audit`).Scan(&beforeAudit); err != nil {
				t.Fatal(err)
			}
			first = preview("a-existing", "2.0.0", "a-existing", false)
			second := preview("b-new", "1.0.0", "", false)
			main := preview("z-pack", "1.0.0", "", true)
			main.Bundle = []*extension.Preview{first, second}
			refs := []extension.PreviewRef{{PreviewID: first.ID, Digest: first.Digest}, {PreviewID: second.ID, Digest: second.Digest}}
			id := "b-new"
			when := "BEFORE"
			constraint := ""
			deferred := ""
			if failure != "second" {
				id = "z-pack"
			}
			if failure == "commit" {
				when = "AFTER"
				constraint = "CONSTRAINT "
				deferred = "DEFERRABLE INITIALLY DEFERRED"
			}
			sql := fmt.Sprintf(`CREATE FUNCTION extension_plan_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id = '%s' THEN RAISE EXCEPTION 'synthetic plan failure'; END IF; RETURN NEW; END $$; CREATE %sTRIGGER extension_plan_fail %s INSERT OR UPDATE ON plugins %s FOR EACH ROW EXECUTE FUNCTION extension_plan_fail()`, id, constraint, when, deferred)
			if failure == "prevalidation" {
				main.Unresolved = []string{"shared >=2.0.0"}
			} else if _, err = d.pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			cleanup := func() {
				t.Helper()
				if failure == "prevalidation" {
					return
				}
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if _, e := d.pool.Exec(cleanupCtx, `DROP TRIGGER IF EXISTS extension_plan_fail ON plugins; DROP FUNCTION IF EXISTS extension_plan_fail()`); e != nil {
					t.Error(e)
				}
			}
			t.Cleanup(cleanup)
			calls := 0
			beforeCommits := store.commits
			reg.OnChange = func(extension.Change) { calls++ }
			if _, err = reg.InstallPlan(ctx, main.ID, main.Digest, refs, "test"); err == nil {
				t.Fatal("injected plan failure was accepted")
			}
			if failure == "prevalidation" && store.commits != beforeCommits {
				t.Fatal("prevalidation reached the PostgreSQL transaction")
			}
			if !reflect.DeepEqual(beforeReg, reg.Snapshot("a-existing").Record) {
				t.Error("registry record changed")
			}
			rows, e := d.Extensions(ctx)
			if e != nil || !reflect.DeepEqual(beforeRows, rows) {
				t.Error("database records changed:", e)
			}
			secrets, e := d.ExtensionSecrets(ctx, "a-existing")
			if e != nil || !reflect.DeepEqual(beforeSecrets, secrets) {
				t.Error("sealed secrets changed:", e)
			}
			var secretTime time.Time
			if e = d.pool.QueryRow(ctx, `SELECT updated_at FROM plugin_secrets WHERE plugin='a-existing' AND name='api_key'`).Scan(&secretTime); e != nil || !secretTime.Equal(beforeSecretTime) {
				t.Error("secret was rewritten:", e)
			}
			var audit int
			if e = d.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_audit`).Scan(&audit); e != nil || audit != beforeAudit || calls != 0 {
				t.Error("failure published, audited or activated:", e)
			}
			for _, version := range []string{"0.9.0", "1.0.0"} {
				b, e := os.ReadFile(filepath.Join(root, "installed", "a-existing", version, "README.md"))
				if e != nil || string(b) != "a-existing "+version {
					t.Error("old package changed:", version, e)
				}
			}
			for _, entry := range []struct{ id, version string }{{"a-existing", "2.0.0"}, {"b-new", "1.0.0"}, {"z-pack", "1.0.0"}} {
				if _, e = os.Stat(filepath.Join(root, "installed", entry.id, entry.version)); !os.IsNotExist(e) {
					t.Error("partial package remained:", entry, e)
				}
			}
			if b, e := os.ReadFile(data); e != nil || !bytes.Equal(b, []byte("synthetic user data")) {
				t.Error("user data changed:", e)
			}
			for _, pv := range []*extension.Preview{first, second, main} {
				if reg.PreviewOf(pv.ID) == nil {
					t.Error("failed preview consumed:", pv.Manifest.ID)
				}
			}
			cleanup()
			main.Unresolved = nil
			if _, err = reg.InstallPlan(ctx, main.ID, main.Digest, refs, "test"); err != nil {
				t.Fatal("retry failed:", err)
			}
			if reg.Snapshot("a-existing").Record.Version != "2.0.0" || calls != 3 {
				t.Fatal("retry did not publish complete plan")
			}
			secrets, e = d.ExtensionSecrets(ctx, "a-existing")
			if e != nil || !reflect.DeepEqual(beforeSecrets, secrets) {
				t.Error("successful retry changed sealed secrets:", e)
			}
		})
	}
}

// orphanTestManifest is the package (or, at 9.0.0 without grants, the built-in) whose rows TestOrphanAdoptionThroughRegistry moves.
func orphanTestManifest(t *testing.T, id, version string) []byte {
	t.Helper()
	m := map[string]any{"manifest_version": 1, "id": id, "name": id, "version": version,
		"contributes": map[string]any{
			"providers": []map[string]string{{"id": id + "-provider", "name": id, "base_url": "https://provider.example.com"}}},
		"configuration": map[string]any{"type": "object", "properties": map[string]any{"saved": map[string]string{"type": "string"}}},
		"secrets":       []map[string]any{{"name": "api_key", "title": "Example key", "required": true}}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// orphanTestMarket is a market signing its catalog with a fresh key: the official market's (the compiled-in key
// swapped for it until the test ends) or a private one's.
type orphanTestMarket struct {
	spec, id, key string
	priv          ed25519.PrivateKey
}

func newOrphanTestMarket(t *testing.T, official bool) *orphanTestMarket {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mk := &orphanTestMarket{spec: "github:example/private-plugins", id: "private", key: "ed25519:" + base64.StdEncoding.EncodeToString(pub), priv: priv}
	if official {
		previous := extension.Official
		extension.Official.SigningKey = mk.key
		t.Cleanup(func() { extension.Official = previous })
		mk.spec, mk.id = extension.Official.Spec, "official"
	}
	return mk
}

func (mk *orphanTestMarket) preview(t *testing.T, reg *extension.Registry, raw []byte) (*extension.Preview, error) {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: extension.ManifestFile, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(raw))})
	_, _ = tw.Write(raw)
	_ = tw.Close()
	_ = gz.Close()
	pkg := archive.Bytes()
	sum := sha256.Sum256(pkg)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(pkg) }))
	t.Cleanup(srv.Close)
	spec, err := extension.ParseMarket(mk.spec)
	if err != nil {
		t.Fatal(err)
	}
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	entry := extension.MarketEntry{Manifest: raw, Parsed: m, Packages: []extension.MarketPackage{{URL: srv.URL + "/pkg.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}
	payload, err := json.Marshal(extension.MarketIndex{MarketVersion: 1, Name: "Example", Extensions: []extension.MarketEntry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := json.Marshal(map[string]any{"payload": string(payload), "signatures": []map[string]string{{"alg": "ed25519",
		"key_id": extension.KeyID(mk.priv.Public().(ed25519.PublicKey)), "sig": base64.StdEncoding.EncodeToString(ed25519.Sign(mk.priv, payload))}}})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := extension.ParseCatalog(signed, mk.key)
	if err != nil {
		t.Fatal(err)
	}
	return reg.PreviewMarket(t.Context(), &extension.Market{Spec: spec, Client: srv.Client(), TrustedKey: mk.key}, mk.id, &verified.Index.Extensions[0], "")
}

// A build without a module finds its built-in's row (#16263). Before the dormant table, installing the official
// package of that id from such a build was a plain new install the database refused (ErrIdentity), so the owner could
// never get the module back. Now the registry adopts the row in place on real Postgres; a local upload or a private
// market's package over it is still refused and leaves its bytes and its key as they were.
func TestOrphanAdoptionThroughRegistry(t *testing.T) {
	d, ctx := open(t), t.Context()
	const id = "orphan-registry-test"
	cleanupStorageFixture(t, d, id)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = d.pool.Exec(cleanupCtx, `DELETE FROM plugin_audit WHERE plugin=$1; DELETE FROM plugin_secrets WHERE plugin=$1; DELETE FROM plugin_identities WHERE plugin=$1`, id)
	})
	root := t.TempDir()
	store := &planTestStore{DB: d}
	builtin, err := extension.Parse(orphanTestManifest(t, id, "9.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	// A build with the module: the owner configures its built-in.
	withModule := extension.NewRegistry(root, store, planTestSealer{}, 202611100000)
	if err = withModule.AddBuiltin(builtin, t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	withModule.HoldModule(id, false)
	if err = withModule.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err = withModule.SetConfig(ctx, id, json.RawMessage(`{"saved":"retained"}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err = withModule.SetSecret(ctx, id, "api_key", "synthetic-value", "test"); err != nil {
		t.Fatal(err)
	}
	if err = withModule.SetContribution(ctx, id, "providers", id+"-provider", false, "test"); err != nil {
		t.Fatal(err)
	}
	if err = withModule.SetEnabled(ctx, id, true, "test"); err != nil {
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
	var manifestColumn string
	if err = d.pool.QueryRow(ctx, `SELECT source || ' ' || COALESCE(manifest::text, '') FROM plugins WHERE id=$1`, id).Scan(&manifestColumn); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(manifestColumn, extension.SourceBuiltin+" {") || !strings.Contains(manifestColumn, `"providers"`) {
		t.Fatalf("the built-in's row keeps no manifest to adopt by: %s", manifestColumn) // plan §14-2
	}
	secrets, err := d.ExtensionSecrets(ctx, id)
	if err != nil || secrets["api_key"] == "" {
		t.Fatalf("the built-in's key: %v %v", secrets, err)
	}

	// The build without it holds the row: not listed, Load fine.
	without := extension.NewRegistry(root, store, planTestSealer{}, 202611100000)
	without.HoldModule(id, false)
	if err = without.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if without.Get(id) != nil {
		t.Fatal("the orphan is listed")
	}
	refused := func(t *testing.T, pv *extension.Preview, err error) {
		t.Helper()
		before := raw()
		if err == nil {
			_, err = without.Install(ctx, pv.ID, pv.Digest, "test")
		}
		if !errors.Is(err, extension.ErrIdentity) {
			t.Fatalf("expected ErrIdentity, got %v", err)
		}
		if raw() != before {
			t.Fatal("the refused install changed the row")
		}
		if now, err := d.ExtensionSecrets(ctx, id); err != nil || !reflect.DeepEqual(now, secrets) {
			t.Fatalf("the refused install changed the key: %v", err)
		}
		if without.Get(id) != nil {
			t.Fatal("the refused install is listed")
		}
	}
	t.Run("upload", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), orphanTestManifest(t, id, "1.0.0"), 0o644); err != nil {
			t.Fatal(err)
		}
		pv, err := without.Preview(ctx, "dir:"+dir, "")
		refused(t, pv, err)
	})
	t.Run("private_market", func(t *testing.T) {
		pv, err := newOrphanTestMarket(t, false).preview(t, without, orphanTestManifest(t, id, "1.0.0"))
		refused(t, pv, err)
	})

	// The official package adopts it in place.
	pv, err := newOrphanTestMarket(t, true).preview(t, without, orphanTestManifest(t, id, "1.0.0"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = without.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatalf("the official package over the orphan: %v", err)
	}
	rows, err := d.Extensions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(r extension.Record) bool { return r.StorageKey() == id })
	if i < 0 {
		t.Fatal("the slot is gone")
	}
	got := rows[i]
	if got.Source != extension.SourceInstalled || !got.VerifiedBuiltinPredecessor() || got.PreviousOrigin != "builtin:"+id || got.PreviousVersion != "9.0.0" ||
		!got.Enabled || !strings.Contains(string(got.Config), `"retained"`) || !slices.Contains(got.DisabledContributions, "providers/"+id+"-provider") {
		t.Fatalf("adopted row %+v", got)
	}
	if now, err := d.ExtensionSecrets(ctx, id); err != nil || !reflect.DeepEqual(now, secrets) {
		t.Fatalf("the key moved: %v", err)
	}
	if p := without.Get(id); p == nil || p.Record.Source != extension.SourceInstalled {
		t.Fatal("the adopted package is not listed")
	}
	// A restart of the build without the module reads it as an installed package.
	again := extension.NewRegistry(root, store, planTestSealer{}, 202611100000)
	again.HoldModule(id, false)
	if err = again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if p := again.Get(id); p == nil || !p.Record.VerifiedBuiltinPredecessor() {
		t.Fatal("the adopted package is not read back")
	}
}
