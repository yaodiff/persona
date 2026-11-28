package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// search-reindex empties the built-in manager's index in the database it runs on: the hub's until its data moved, then
// its own (a second database on the hub's server, or a container's), as its manager.json says.
func TestSearchReindexFindsTheBuiltinManagersDatabase(t *testing.T) {
	shared := "postgres://hub:pw@127.0.0.1:5432/persona?sslmode=disable"
	second, err := db.WithDatabase(shared, "persona_manager")
	if err != nil {
		t.Fatal(err)
	}
	box := "postgres://mgr:pw2@127.0.0.1:55432/persona?sslmode=disable"
	moved := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name         string
		own          *managerconf.OwnDatabase
		shared, want string
		err          string
	}{
		{name: "no manager.json", shared: shared, want: shared},
		{name: "a second database made, not moved yet", own: &managerconf.OwnDatabase{Name: "persona_manager"}, shared: shared, want: shared},
		{name: "a container made, not moved yet", own: &managerconf.OwnDatabase{Container: "persona-mgr-assistant-pg", URL: box}, shared: shared, want: shared},
		{name: "moved to a second database", own: &managerconf.OwnDatabase{Name: "persona_manager", Moved: moved}, shared: shared, want: second},
		{name: "moved to a container", own: &managerconf.OwnDatabase{Container: "persona-mgr-assistant-pg", URL: box, Moved: moved}, shared: shared, want: box},
		{name: "moved to a container, no hub database", own: &managerconf.OwnDatabase{Container: "persona-mgr-assistant-pg", URL: box, Moved: moved}, want: box},
		{name: "no hub database", err: "no database_url"},
		{name: "moved to a second database, no hub database", own: &managerconf.OwnDatabase{Name: "persona_manager", Moved: moved}, err: "no database_url"},
		{name: "moved, not saying where", own: &managerconf.OwnDatabase{Moved: moved}, shared: shared, err: "but not where"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.own != nil {
				if err := os.MkdirAll(managerconf.DefaultDir(home), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := managerconf.WriteOwnDatabase(home, *tc.own); err != nil {
					t.Fatal(err)
				}
			}
			got, err := builtinDatabaseNow(home, tc.shared)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("got %q, %v; want an error with %q", got, err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

// `assistant search-reindex` on a hub whose built-in manager has not moved: the index in the hub's database is
// emptied and the reset counted (a running manager reads its chats again, search_index.go); a database without the
// index (a manager from before chat search) is refused and left as it is.
func TestSearchReindexEmptiesTheIndex(t *testing.T) {
	ctx := context.Background()
	url := newMigratedDatabase(t, db.ManagerSet)
	d, err := db.Open(ctx, url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.PutSearchItems(ctx, "server", "trip", "h1", 2, []db.SearchItemRow{
		{Seq: 1, ItemID: "i1#0", Role: "user", Body: "BlueLane", Folded: "bluelane"},
	}, false); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_DATABASE_URL", "")
	hub, _ := json.Marshal(map[string]string{"database_url": url})
	if err := os.WriteFile(filepath.Join(home, "hub.json"), hub, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	if err := SearchReindexCommand(ctx, nil, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if cursors, err := d.SearchCursors(ctx); err != nil || len(cursors) != 0 {
		t.Fatalf("cursors after search-reindex: %+v, %v", cursors, err)
	}
	if hits, err := d.SearchAssistantItems(ctx, []string{"server"}, []string{"trip"}, []string{"bluelane"}, 3); err != nil || len(hits) != 0 {
		t.Fatalf("found after search-reindex: %+v, %v", hits, err)
	}
	if n, err := d.SearchReset(ctx); err != nil || n != 1 {
		t.Fatalf("resets %d, %v; want 1", n, err)
	}
	if s := out.String(); !strings.Contains(s, "the built-in manager (database "+db.DatabaseName(url)+")") || !strings.Contains(s, "reads its chats again") {
		t.Fatalf("said %q", s)
	}
	if strings.Contains(out.String(), "@") || strings.Contains(out.String(), "postgres://") {
		t.Fatalf("said the database's address: %q", out.String())
	}
	if err := SearchReindexCommand(ctx, []string{"--purge"}, &out, &stderr); err == nil {
		t.Fatal("an unknown flag was taken")
	}

	bare := newDatabase(t)
	out.Reset()
	err = SearchReindex(ctx, bare, "manager m1", &out)
	if err == nil || !strings.Contains(err.Error(), "manager m1's database "+db.DatabaseName(bare)+" has no chat search index yet") {
		t.Fatalf("a database without the index: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("said %q on a database without the index", out.String())
	}
	b, err := db.Attach(ctx, bare, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if tables, err := b.Tables(ctx); err != nil || len(tables) != 0 {
		t.Fatalf("tables made in a database without the index: %v, %v", tables, err)
	}
}
