package extension

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Every settings write moves the row's revision, and the record must carry the one the store kept: storage binds an
// installation by it (#16920). The clock stands still, as two writes within one microsecond see it, and each write
// must still move past the last, as the database's own revision fence does (#16754).
func TestSettingsWritesKeepTheRecordAtTheStoredRevision(t *testing.T) {
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	still := time.Date(2026, 11, 27, 0, 0, 0, 731, time.UTC)
	r.now = func() time.Time { return still }
	pkg := t.TempDir()
	writePackage(t, pkg, func(m map[string]any) {
		m["contributes"].(map[string]any)["providers"] = []any{map[string]any{"id": "notes-mini", "name": "Notes Mini", "base_url": "https://mini.notes.example.com"}}
	})
	pv, err := r.Preview(ctx, pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	last := r.Snapshot("example-notes").Record.UpdatedAt
	for _, change := range []struct {
		name string
		do   func() error
	}{
		{"config", func() error { return r.SetConfig(ctx, "example-notes", json.RawMessage(`{"folders": ["a"]}`), "cli") }},
		{"switch", func() error { return r.SetEnabled(ctx, "example-notes", false, "cli") }},
		{"contribution", func() error { return r.SetContribution(ctx, "example-notes", "providers", "notes-mini", false, "cli") }},
	} {
		if err := change.do(); err != nil {
			t.Fatal(change.name, err)
		}
		got := r.Snapshot("example-notes").Record.UpdatedAt
		store.mu.Lock()
		kept := store.rows["example-notes"].UpdatedAt
		store.mu.Unlock()
		if !got.Equal(kept) || !got.After(last) {
			t.Errorf("%s: the record is at %v, the store kept %v, the revision before was %v", change.name, got, kept, last)
		}
		last = got
	}
}
