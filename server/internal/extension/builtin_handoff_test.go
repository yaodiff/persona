package extension

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// Exercise the same signature-verified, downloaded marketplace package and
// registry preview as the public installation flow. A built-in starts with
// fewer grants than its independently versioned successor.
func builtinHandoffFixture(t *testing.T, signed bool) (*Registry, *identityStore, *Manifest, func() (*Preview, error)) {
	t.Helper()
	r, store, preview := identityFixture(t, false, signed)
	const id = "old-example"
	m := *r.Snapshot(id).Full
	if err := r.Uninstall(t.Context(), id, false, "test"); err != nil {
		t.Fatal(err)
	}
	m.Version = "9.0.0"
	m.Permissions = Permissions{}
	r.AllowBuiltinHandoff(id)
	if err := r.AddBuiltin(&m, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	return r, store, &m, func() (*Preview, error) { return preview(id, "", "example-market", id) }
}

func TestBuiltinHandoffPreservesOwnerStateAndRollback(t *testing.T) {
	r, store, builtin, preview := builtinHandoffFixture(t, true)
	id, ctx := builtin.ID, t.Context()
	if err := r.SetEnabled(ctx, id, false, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetContribution(ctx, id, "ui", "identity-page", false, "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetConfig(ctx, id, []byte(`{"folders":["work"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(ctx, id, "api_key", "synthetic test value", "test"); err != nil {
		t.Fatal(err)
	}
	cipher := store.secrets[id]["api_key"]
	data := filepath.Join(r.DataDir(id), "example")
	if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(data, []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	pv, err := preview()
	if err != nil {
		t.Fatal(err)
	}
	if !pv.Upgrade || !pv.MorePermissions {
		t.Fatal("expected upgrade with new grants")
	}
	if _, err := r.UpgradePlan(ctx, id, pv.ID, "", nil, "test"); !errors.Is(err, errPlanDigest) {
		t.Fatalf("missing permission digest: %v", err)
	}
	store.fail = true
	if _, err := r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test"); err == nil || r.Snapshot(id).Record.Source != SourceBuiltin {
		t.Fatalf("failed commit changed the provider: %v", err)
	}
	store.fail = false
	p, err := r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	check := func(r *Registry, p *Extension, source string) {
		t.Helper()
		if len(r.List()) != 1 || p.Record.Source != source || p.Record.StorageKey() != id || p.Record.Enabled ||
			len(p.Manifest.Items("ui")) != 0 || !sameJSON(p.Record.Config, []byte(`{"folders":["work"]}`)) || store.secrets[id]["api_key"] != cipher {
			t.Fatalf("owner state changed: %+v", p.Record)
		}
		raw, err := os.ReadFile(data)
		if err != nil || string(raw) != "kept" {
			t.Fatal("associated data missing", err)
		}
	}
	check(r, p, SourceInstalled)
	if !p.Record.BuiltinPredecessor || !p.Record.VerifiedBuiltinPredecessor() || !slices.Equal(p.Record.Granted.Network, []string{"notes.example.com"}) {
		t.Fatalf("missing provider identity or grants: %+v", p.Record)
	}
	if _, err := r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("consumed preview reused")
	}
	start := func() *Registry {
		next := NewRegistry(r.dir, store, rot13{}, r.build)
		next.AllowBuiltinHandoff(id)
		if err := next.AddBuiltin(builtin, t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
		if err := next.Load(ctx); err != nil {
			t.Fatal(err)
		}
		return next
	}
	next := start()
	check(next, next.Snapshot(id), SourceInstalled)
	back, err := next.Rollback(ctx, id, "test")
	if err != nil {
		t.Fatal(err)
	}
	check(next, back, SourceBuiltin)
	if back.Record.BuiltinPredecessor || len(back.Record.Granted.Network) > 0 {
		t.Fatal("built-in still has package grants or provenance")
	}
	again := start()
	check(again, again.Snapshot(id), SourceBuiltin)
}

func TestBuiltinHandoffTrustBeforeState(t *testing.T) {
	for _, tc := range []struct {
		name               string
		registered, signed bool
	}{
		{"unregistered", false, true}, {"unsigned", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, m, preview := builtinHandoffFixture(t, tc.signed)
			if !tc.registered {
				delete(r.handoffs, m.ID)
			}
			if _, err := preview(); err == nil {
				t.Fatal("accepted unrelated package")
			}
			if r.Snapshot(m.ID).Record.Source != SourceBuiltin {
				t.Fatal("lost builtin before consent")
			}
		})
	}
}

func TestBuiltinHandoffIndeterminateFreshInstall(t *testing.T) {
	r, store, m, preview := builtinHandoffFixture(t, true)
	if len(store.rows) != 0 {
		t.Fatalf("fixture retained an old row: %v", store.rows)
	}
	pv, err := preview()
	if err != nil {
		t.Fatal(err)
	}
	store.unknown = true
	if _, err := r.UpgradePlan(t.Context(), m.ID, pv.ID, pv.Digest, nil, "test"); err == nil {
		t.Fatal("unknown commit reported success")
	}
	if r.Get(m.ID).Record.Source != SourceBuiltin {
		t.Fatal("activated before confirmation")
	}
	store.unknown = false
	if err := r.SetEnabled(t.Context(), m.ID, false, "test"); err == nil {
		t.Fatal("indeterminate registry allowed a stale write")
	}
	next := NewRegistry(r.dir, store, rot13{}, r.build)
	next.AllowBuiltinHandoff(m.ID)
	if err := next.AddBuiltin(m, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if err := next.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	row := next.Snapshot(m.ID).Record
	if !row.BuiltinPredecessor || !row.Enabled || row.Source != SourceInstalled || !slices.Equal(row.DisabledContributions, m.StartsOff(nil, nil)) {
		t.Fatalf("unexpected restart state: %+v", row)
	}
	if _, err := next.Rollback(t.Context(), m.ID, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinHandoffConflictStaysIsolated(t *testing.T) {
	r, store, builtin, preview := builtinHandoffFixture(t, true)
	ctx, id := t.Context(), builtin.ID
	pv, err := preview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test"); err != nil {
		t.Fatal(err)
	}
	row := store.rows[id]
	row.BuiltinPredecessor = false
	store.rows[id] = row
	next := NewRegistry(r.dir, store, rot13{}, r.build)
	next.AllowBuiltinHandoff(id)
	if err := next.AddBuiltin(builtin, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	other := *builtin
	other.ID = "unrelated"
	if err := next.AddBuiltin(&other, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	if err := next.Load(ctx); err != nil {
		t.Fatal(err)
	}
	conflict := next.Snapshot(id)
	if conflict.Record.Enabled || conflict.Problem == "" || !conflict.ReadOnly() || !next.Snapshot(other.ID).Record.Enabled {
		t.Fatal("conflict was active, editable, or disrupted another installation")
	}
	if err := next.Uninstall(ctx, id, true, "test"); !errors.Is(err, ErrIdentity) {
		t.Fatal("unrelated data exposed by uninstall", err)
	}
	if err := next.Uninstall(ctx, id, false, "test"); err != nil {
		t.Fatal(err)
	}
	back := next.Snapshot(id)
	if !back.Record.Enabled || back.Record.Source != SourceBuiltin || back.Problem != "" || len(back.Record.Config) != 0 {
		t.Fatal("compiled defaults were not restored")
	}
}

func TestBuiltinHandoffRejectsStaleOwnerSnapshot(t *testing.T) {
	r, store, builtin, preview := builtinHandoffFixture(t, true)
	ctx, id := t.Context(), builtin.ID
	if err := r.SetEnabled(ctx, id, false, "test"); err != nil {
		t.Fatal(err)
	}
	pv, err := preview()
	if err != nil {
		t.Fatal(err)
	}
	row := store.rows[id]
	row.Enabled = true
	store.rows[id] = row
	if _, err = r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test"); !errors.Is(err, ErrSettingsMoved) {
		t.Fatalf("overwrote changed owner switch: %v", err)
	}
	delete(store.rows, id)
	if _, err = r.UpgradePlan(ctx, id, pv.ID, pv.Digest, nil, "test"); !errors.Is(err, ErrIdentity) {
		t.Fatalf("resurrected removed ownership row: %v", err)
	}
}
