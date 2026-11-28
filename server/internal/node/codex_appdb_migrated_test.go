package node

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// writeMigratedCodexApp writes the App's global state as the migrated App leaves it: the migration markers, but no
// projectless-thread-ids, no thread-project-assignments and no local-projects — its sidebar lives only in the state
// database (#6388).
func writeMigratedCodexApp(t *testing.T, home, dbSQL string) {
	t.Helper()
	sqlite := sqlite3WithJSON(t)
	state := `{"app-server-projects-migration-by-host": {"local": 1}}`
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(sqlite, filepath.Join(home, "state_5.sqlite"), dbSQL).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3: %v: %s", err, out)
	}
	codexAppDBs.Lock()
	clear(codexAppDBs.byHome) // read the database again now, not after codexAppDBRefresh
	codexAppDBs.Unlock()
}

// A chat the migrated App writes only to the state database (no legacy sidebar keys) is still an App sidebar chat:
// listed as one, not left a read-only history fallback. An exec run in the same database is not a sidebar chat.
func TestCodexAppMigratedChatsAreListed(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeMigratedCodexApp(t, home, codexAppSchema+
		codexAppRow(appMigrated, "", filepath.Join(project, "migrated"), "Migrated chat", updated, 0)+
		codexAppRow(appExec, "", project, "A run", updated, 0, "codex_exec"))

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	m, ok := rows[appMigrated]
	if !ok {
		t.Fatalf("the migrated chat is missing from Agents: %v", rows)
	}
	if m.AgentName != "Codex App" || m.SpaceKind != "codex_app" {
		t.Fatalf("the migrated chat is not an App row: %+v", m)
	}
	if m.Title != "Migrated chat" || m.Cwd != filepath.Join(project, "migrated") {
		t.Fatalf("the migrated chat = %+v", m)
	}
	if _, ok := rows[appExec]; ok {
		t.Fatal("an exec run is not an App sidebar chat")
	}
}

func TestCodexAppMigratedSharedStampWithoutRollout(t *testing.T) {
	n := remoteTestNode(t)
	home, project := n.codexHome(), t.TempDir()
	updated := time.Now().Add(-time.Hour).Truncate(time.Millisecond)
	writeMigratedCodexApp(t, home, codexAppSchema+
		codexAppRow(appMigrated, "", project, "Shared App chat", updated, 0, "persona")+
		codexAppRow(appExec, "", project, "Shared non-sidebar run", updated, 0, "persona")+
		fmt.Sprintf("UPDATE threads SET source = 'exec' WHERE id = '%s';", appExec))

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	chat, ok := rows[appMigrated]
	if !ok || chat.SpaceKind != "codex_app" || chat.Title != "Shared App chat" {
		t.Fatalf("the database-only shared App chat: title=%q kind=%q listed=%v", chat.Title, chat.SpaceKind, ok)
	}
	if !codexDesktopChat(codexSource{home: home, thread: appMigrated}) {
		t.Fatal("the shared App chat was not eligible for App-bus delivery")
	}
	if _, ok := rows[appExec]; ok {
		t.Fatal("a shared one-shot run is not an App sidebar chat")
	}
}

// A migrated App's project lives only in the database: a chat's project is found by its cwd there (the json's
// local-projects is gone), and a cwd outside every root falls back to the ungrouped row.
func TestCodexAppProjectFromMigratedDatabase(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	writeMigratedCodexApp(t, home, codexAppSchema+
		`CREATE TABLE projects (id TEXT PRIMARY KEY, name TEXT);
		 CREATE TABLE project_roots (project_id TEXT, root TEXT);
		 INSERT INTO projects VALUES ('dbp1', 'Beta');
		 INSERT INTO project_roots VALUES ('dbp1', '`+root+`');`)
	app := loadCodexAppState(home)
	pid, name := app.project(appMigrated, filepath.Join(root, "migrated"))
	if pid != "dbp1" || name != "Beta" {
		t.Fatalf("project = %q %q, want the database project dbp1 Beta", pid, name)
	}
	if pid, _ := app.project(appMigrated, t.TempDir()); pid != "" {
		t.Fatalf("a cwd outside every root fell into %q, want the ungrouped fallback", pid)
	}
}

// A migrated App moves a thread's history into its database and deletes its rollout (history_mode "paginated"); the
// row's source still says what the rollout's first line did (#8191). A subagent the chat spawned folds under it as one
// with a rollout does: a read-only Codex App subagent naming its chat, not an agent of its own titled with the folder.
// A guardian's approval review is not listed (#5486), and neither is taken for a chat to send to through the App.
const (
	appSubagent = "019a0000-0000-7000-8000-000000000007" // spawned by appMigrated, rollout gone
	appGuardian = "019a0000-0000-7000-8000-000000000008" // an approval review, rollout gone
	appReview   = "019a0000-0000-7000-8000-000000000009" // a subagent naming no parent ({"subagent": "review"}), rollout gone
)

func migratedAppWithSubagents(project string, updated time.Time) string {
	row := func(id, source string) string { // as the App writes a subagent's row: no title, no name
		return fmt.Sprintf(`INSERT INTO threads VALUES ('%s', '', 0, %d, '%s', '%s', '', 0, %d, '', 'Codex Desktop');`,
			id, updated.Unix(), source, project, updated.UnixMilli())
	}
	return codexAppSchema + codexAppRow(appMigrated, "", project, "Migrated chat", updated, 0) +
		row(appSubagent, `{"subagent":{"thread_spawn":{"parent_thread_id":"`+appMigrated+`","depth":1,"agent_nickname":"Gibbs"}}}`) +
		row(appGuardian, `{"subagent":{"other":"guardian"}}`) +
		row(appReview, `{"subagent":"review"}`)
}

func TestCodexAppSubagentsWithoutRolloutsFoldUnderTheirChat(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	project := t.TempDir()
	writeMigratedCodexApp(t, home, migratedAppWithSubagents(project, time.Now().Add(-time.Hour).Truncate(time.Millisecond)))

	rows := rowsByThread(n.codexSessions(map[string]bool{}))
	chat, ok := rows[appMigrated]
	if !ok || chat.IsSubagent || chat.ParentThreadID != "" {
		t.Fatalf("the chat = %+v (listed %v)", chat, ok)
	}
	sub, ok := rows[appSubagent]
	if !ok {
		t.Fatalf("the subagent is missing: %v", rows)
	}
	if !sub.IsSubagent || !sub.IsSubtask() || sub.ParentThreadID != appMigrated || sub.ParentSessionID != chat.SessionID {
		t.Fatalf("the subagent is not under its chat %s: %+v", chat.SessionID, sub)
	}
	if sub.AgentName != "Codex App" || sub.Capabilities.Send || sub.Capabilities.Attach || sub.Capabilities.Interrupt ||
		sub.ReadOnlyReason != "仅查看：这个子 Agent 由父 Agent 驱动" {
		t.Fatalf("the subagent is not a read-only Codex App subagent its chat drives: %+v %+v", sub, *sub.Capabilities)
	}
	// One naming no parent is a subagent all the same, read-only, as with a rollout.
	if r, ok := rows[appReview]; !ok || !r.IsSubagent || r.Capabilities.Send || r.Capabilities.Attach {
		t.Fatalf("the review subagent = %+v (listed %v)", r, ok)
	}
	if _, ok := rows[appGuardian]; ok {
		t.Fatal("a guardian's approval review is listed as an agent")
	}
}

// With the App running, the migrated chat is one to send to through it; its subagent and its guardian review are not.
func TestCodexAppChatWithoutRolloutIsNoSubagent(t *testing.T) {
	home, _ := startFakeCodexBus(t, "")
	writeMigratedCodexApp(t, home, migratedAppWithSubagents(t.TempDir(), time.Now().Add(-time.Hour).Truncate(time.Millisecond)))
	for id, want := range map[string]bool{appMigrated: true, appSubagent: false, appGuardian: false, appReview: false} {
		if got := codexDesktopChat(codexSource{home: home, thread: id}); got != want {
			t.Errorf("codexDesktopChat(%s) = %v, want %v", id, got, want)
		}
	}
}
