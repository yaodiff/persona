package node

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Desktop origin alone is insufficient: background runs retain it. The raw catalog also keeps subagent metadata
// for parent folding and guardian suppression; those rows never become independently writable conversations.
func TestCodexAppMigratedDiscoveryKeepsDesktopAndTerminalSources(t *testing.T) {
	cases := []struct {
		name, origin, source string
		archived             int
		inferred, explicit   bool
	}{
		{name: "desktop conversation", origin: "Codex Desktop", source: "vscode", inferred: true},
		{name: "work conversation", origin: "codex_work_desktop", source: "vscode", inferred: true},
		{name: "desktop background run", origin: "Codex Desktop", source: "exec", explicit: true},
		{name: "work background run", origin: "codex_work_desktop", source: "exec"},
		{name: "subagent source", origin: "Codex Desktop", source: "subagent", inferred: true},
		{name: "spawned subagent", origin: "Codex Desktop", source: `{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}`, inferred: true},
		{name: "work guardian", origin: "codex_work_desktop", source: `{"subagent":{"other":"guardian"}}`, inferred: true},
		{name: "CLI conversation", origin: "codex-tui", source: "cli", inferred: true, explicit: true},
		{name: "CLI vscode source", origin: "codex-tui", source: "vscode", inferred: true},
		{name: "desktop CLI source", origin: "Codex Desktop", source: "cli", inferred: true},
		{name: "trimmed CLI source", origin: "Codex Desktop", source: " cli ", inferred: true},
		{name: "SDK vscode source", origin: "codex_sdk_test", source: "vscode"},
		{name: "unknown source", origin: "Codex Desktop", source: "unknown"},
		{name: "unrelated object source", origin: "Codex Desktop", source: `{"kind":"job"}`},
		{name: "malformed subagent source", origin: "Codex Desktop", source: `{"subagent":`},
		{name: "empty source", origin: "Codex Desktop"},
		{name: "archived conversation", origin: "Codex Desktop", source: "vscode", archived: 1, explicit: true},
	}
	sqlite := sqlite3WithJSON(t)
	db := filepath.Join(t.TempDir(), "state_5.sqlite")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	var sql strings.Builder
	sql.WriteString(codexAppSchema)
	ids := make([]string, len(cases))
	var explicit []string
	for i, tc := range cases {
		id := fmt.Sprintf("019a0000-0000-7000-8000-%012d", i+1)
		ids[i] = id
		if tc.explicit {
			explicit = append(explicit, id)
		}
		sql.WriteString(codexAppRow(id, "", filepath.Dir(db), tc.name, time.Unix(1, 0), tc.archived, tc.origin))
		fmt.Fprintf(&sql, "UPDATE threads SET source = %s WHERE id = %s;", quote(tc.source), quote(id))
	}
	if out, err := exec.Command(sqlite, db, sql.String()).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	for _, withExplicit := range []bool{false, true} {
		t.Run(fmt.Sprintf("explicit_sidebar=%v", withExplicit), func(t *testing.T) {
			var sidebar []string
			if withExplicit {
				sidebar = explicit
			}
			got, err := readCodexAppThreads(context.Background(), db, sidebar)
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 0
			for i, tc := range cases {
				want := tc.archived == 0 && (tc.inferred || withExplicit && tc.explicit)
				if want {
					wantCount++
				}
				row, found := got[ids[i]]
				if found && (row.Originator != tc.origin || row.includeRunning != (tc.origin == "codex-tui" || strings.TrimSpace(tc.source) == "cli")) {
					t.Errorf("%s origin/running = %q/%v", tc.name, row.Originator, row.includeRunning)
				}
				if found != want {
					t.Errorf("%s discovered = %v, want %v", tc.name, found, want)
				}
			}
			if len(got) != wantCount {
				t.Fatalf("discovered %d rows, want %d", len(got), wantCount)
			}
		})
	}
}

func TestCodexAppDatabaseWithoutOriginatorKeepsSidebar(t *testing.T) {
	sqlite := sqlite3WithJSON(t)
	db := filepath.Join(t.TempDir(), "state_5.sqlite")
	if out, err := exec.Command(sqlite, db, codexAppSchema+codexAppRow(appChat, "", "/w", "Old schema", time.Now(), 0)+"ALTER TABLE threads DROP COLUMN originator;").CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	if rows, err := readCodexAppThreads(context.Background(), db, []string{appChat}); err != nil || rows[appChat].Title != "Old schema" {
		t.Fatalf("legacy sidebar = %+v, %v", rows, err)
	}
}
