//go:build !windows

package kblark

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// TestRealImportWithTheOwnersLogin imports into a temporary data directory through the real lark-cli login. It never
// touches ~/.persona/data (it only reads the owner's document files and copies them into the temporary store) and is
// skipped unless KB_LARK_REAL=1. KB_LARK_BIN names lark-cli when it is not on PATH.
func TestRealImportWithTheOwnersLogin(t *testing.T) {
	if os.Getenv("KB_LARK_REAL") != "1" {
		t.Skip("set KB_LARK_REAL=1 to run against the real lark-cli")
	}
	needsLarkCLI(t)
	e := newEngine(t)
	// The owner's documents as they are, and a copy of one with its title blanked: the fill must bring it back.
	if home, err := os.UserHomeDir(); err == nil {
		entries, _ := os.ReadDir(filepath.Join(home, ".persona", "data", "docs"))
		blanked := false
		for _, ent := range entries {
			if ent.IsDir() || filepath.Ext(ent.Name()) != ".json" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(home, ".persona", "data", "docs", ent.Name()))
			if err != nil {
				continue
			}
			var it kb.Item
			if json.Unmarshal(raw, &it) != nil {
				continue
			}
			if !blanked {
				it.Title = ""
				blanked = true
			}
			if err := e.Store.PutItem(&it); err != nil {
				t.Fatal(err)
			}
		}
	}
	client := NewClient(cliport.NewRunner(cliwire.Config{Bin: os.Getenv("KB_LARK_BIN"), Timeout: time.Minute}), e.Store.Zone())
	res, err := Run(context.Background(), e, client, Config{}, time.Now(), t.Logf)
	if err != nil {
		t.Fatalf("the import failed: %v", err)
	}
	t.Logf("result: %d minute(s) read, %d changed, %d title(s) filled, %d document(s) found", res.Minutes, res.Changed, res.Filled, res.Docs)
	for _, m := range e.List(kb.KindMeeting, "") {
		t.Logf("meeting: %s | %s | %s | %s", m.Name, m.Updated.Format("2006-01-02 15:04"), m.Extra, m.OneLine)
	}
	for _, d := range e.List(kb.KindDoc, "") {
		t.Logf("doc: %s | %s", d.Name, d.Extra)
	}
	if res.Minutes == 0 {
		t.Log("no minutes in the window; nothing to import")
	}
}
