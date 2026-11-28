package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Codex App keeps its threads in <CODEX_HOME>/state_<n>.sqlite (table threads). Since it moved their history into
// thread_history_1.sqlite (history_mode "paginated") it deletes their rollouts, so most chats in its sidebar have none and
// rollouts lists none of them (#3305: 12 of a sidebar's 21 chats were missing from Agents). codexAppThreads reads the rows of
// the chats the sidebar lists (codexAppState.sidebar): one the database has, unarchived, is listed as the sidebar lists it —
// through its rollout while the file is there, whatever its age, else from its row alone (title, directory, last update).
// An id in the sidebar's lists that the database lacks is a chat deleted in the App, which the sidebar does not show either.
//
// The database is read with the system sqlite3, read-only, as the Messages and Cindy readers are (cindybridge.go), also
// while the App has it closed (codexDBTarget). Without sqlite3, without the database, or with a schema these columns are
// not in, nothing more is listed than before.

// codexAppDBRefresh: how often the database is looked at; it is read again only when it or the sidebar's lists changed.
const codexAppDBRefresh = 5 * time.Second

// codexAppSidebarMax bounds the ids put in one query; a sidebar lists far fewer.
const codexAppSidebarMax = 2000

type codexAppThread struct {
	ID          string `json:"id"`
	RolloutPath string `json:"rolloutPath"`
	Title       string `json:"title"`
	Cwd         string `json:"cwd"`
	Source      string `json:"source"`
	Originator  string `json:"originator"`
	UpdatedAtMs int64  `json:"updatedAtMs"`
	Paginated   bool   `json:"-"`
	// Model and Effort are what the App runs the chat with (its model and reasoning_effort columns, #6501): its
	// history in the App's database says neither. Empty in a schema without them.
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Terminal CLIs have no app-server stream: include the stored items of their running turn too.
	includeRunning bool
}

func (t codexAppThread) updated() time.Time {
	if t.UpdatedAtMs <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(t.UpdatedAtMs).UTC()
}

// origin is where the thread came from as its database row says, for a row whose rollout is gone (codexThreadOrigin):
// its source is the rollout's session_meta source, a plain string ("vscode", "exec") or a subagent's object
// (codexSourceObject), and every row read is the App's (readCodexAppThreads). It names no root and no start time.
func (t codexAppThread) origin() codexOrigin {
	o := codexOrigin{desktop: true}
	source := strings.TrimSpace(t.Source)
	if !strings.HasPrefix(source, "{") {
		o.exec, o.sub = source == "exec", source == "subagent"
		return o
	}
	o.parent, o.guardian, o.sub = codexSourceObject(json.RawMessage(source))
	return o
}

type codexAppDB struct {
	at    time.Time // when the database was last looked at
	stamp string    // the database's files and the sidebar's ids when last read
	byID  map[string]codexAppThread
	// sqliteMissingUntil backs off database reads (and their path Globs) after the system sqlite3 CLI was found absent.
	// Unlike at, this also bounds fresh callers, which otherwise bypass the refresh TTL on every high-frequency re-entry.
	sqliteMissingUntil time.Time
}

var codexAppDBs = struct {
	sync.Mutex
	byHome map[string]*codexAppDB
}{byHome: map[string]*codexAppDB{}}

// codexAppDBGlob is what codexAppDBPath globs with (a test counts the globs).
var codexAppDBGlob = filepath.Glob

// codexAppDBPath is the newest state database in home (state_5.sqlite in 2026-11): its number goes up with its schema.
func codexAppDBPath(home string) string {
	paths, _ := codexAppDBGlob(filepath.Join(home, "state_*.sqlite"))
	best, bestN := "", -1
	for _, p := range paths {
		n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), "state_"), ".sqlite"))
		if err == nil && n > bestN {
			best, bestN = p, n
		}
	}
	return best
}

// codexAppThreads: the database's unarchived rows of the chats the Codex App's sidebar lists, by thread id; nil without
// the App's state or its database. The list is rebuilt every second and asks for each row: within codexAppDBRefresh the
// last answer is given without touching a file. A read that fails keeps the last rows read.
func codexAppThreads(home string) map[string]codexAppThread {
	codexAppDBs.Lock()
	c := codexAppDBs.byHome[home]
	if c == nil {
		c = &codexAppDB{}
		codexAppDBs.byHome[home] = c
	}
	now := time.Now()
	if now.Sub(c.at) < codexAppDBRefresh {
		defer codexAppDBs.Unlock()
		return c.byID
	}
	c.at = now // one caller looks; the others keep the last answer meanwhile
	last, lastStamp := c.byID, c.stamp
	codexAppDBs.Unlock()

	app := loadCodexAppState(home)
	db := ""
	if app != nil { // the migrated App's sidebar is the database alone: read it even with no legacy ids (#6388)
		db = codexAppDBPath(home)
	}
	stamp := ""
	if db != "" {
		stamp = fileStamp(db) + fileStamp(db+"-wal") + strings.Join(app.sidebar, ",")
	}
	if stamp == lastStamp {
		return last
	}
	var rows map[string]codexAppThread
	if db != "" {
		var err error
		if rows, err = readCodexAppThreads(context.Background(), db, app.sidebar); err != nil {
			return last
		}
	}
	codexAppDBs.Lock()
	defer codexAppDBs.Unlock()
	c.byID, c.stamp = rows, stamp
	return rows
}

// codexDBTarget is what sqlite3 -readonly is given to open the App's database at db. The App keeps its databases in WAL
// mode and removes their -wal and -shm when it closes one (it quit, or the database was idle); a read-only connection
// cannot open a WAL database whose -shm is gone ("unable to open database file", seen with macOS's sqlite3 3.51), so such
// a database is opened as immutable — without locks or a -shm — and read as it was last checkpointed: with the App's
// connection closed, the file holds every change it committed. The path otherwise (the App has it open, or it is not in
// WAL mode).
func codexDBTarget(db string) string {
	if _, err := os.Stat(db + "-shm"); err == nil {
		return db
	}
	f, err := os.Open(db)
	if err != nil {
		return db
	}
	defer f.Close()
	var header [20]byte // bytes 18 and 19, the file format's write and read versions, are 2 in WAL mode
	if _, err := io.ReadFull(f, header[:]); err != nil || header[18] != 2 || header[19] != 2 {
		return db
	}
	p := filepath.ToSlash(db)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/… on Windows: file:///C:/…
	}
	return (&url.URL{Scheme: "file", Path: p, RawQuery: "immutable=1"}).String()
}

func fileStamp(path string) string {
	st, err := os.Stat(path)
	if err != nil {
		return "-;"
	}
	return fmt.Sprintf("%d.%d;", st.ModTime().UnixNano(), st.Size())
}

// readCodexAppThreads reads the unarchived rows of ids (validID only: they are put in the query as they are), plus
// the migrated App's sidebar chats and their subagents, which have no legacy sidebar key (#6388). Exec runs are not
// sidebar chats; subagent rows must stay available so their origin and guardian filters still work once the rollout
// is gone (#8191). Shared app-server chats can carry the paired persona/vscode stamp (#10438).
// That second read is a separate query so a schema without the originator column keeps the explicit
// sidebar answer instead of failing it.
func readCodexAppThreads(ctx context.Context, db string, ids []string) (map[string]codexAppThread, error) {
	byID := map[string]codexAppThread{}
	if len(ids) > 0 {
		rows, err := readCodexThreads(ctx, db, `archived = 0 AND id IN ('`+strings.Join(ids, "', '")+`')`)
		if err != nil {
			return nil, err
		}
		byID = rows
	}
	migrated, err := readCodexThreads(ctx, db, `archived = 0 AND (
		(originator IN ('Codex Desktop', 'codex_work_desktop', 'codex-tui') AND source != '' AND source != 'exec')
		OR (originator = 'persona' AND source = 'vscode')
	)`)
	if err == nil {
		for id, t := range migrated {
			// Subagent metadata is needed for parent folding and guardian suppression. Other inferred sources are not
			// desktop or terminal conversations; explicit sidebar IDs remain authoritative for legacy rows.
			if t.Originator != "codex-tui" && strings.TrimSpace(t.Source) != "cli" && strings.TrimSpace(t.Source) != "vscode" && !t.origin().sub {
				continue
			}
			if _, ok := byID[id]; !ok {
				byID[id] = t
			}
		}
	}
	return byID, nil
}

func readCodexThreads(ctx context.Context, db, where string) (map[string]codexAppThread, error) {
	sqlite, err := codexSQLiteProgram()
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	q := func(settings string) string {
		// Native threads can store the whole opening prompt in title. Project unnamed titles before caching or sending them.
		return `SELECT id, rollout_path AS rolloutPath, COALESCE(NULLIF(name, ''), substr(title, 1, 256)) AS title, cwd, source, ` +
			`COALESCE(NULLIF(updated_at_ms, 0), updated_at * 1000) AS updatedAtMs` + settings + ` FROM threads WHERE ` + where
	}
	target := codexDBTarget(db)
	query := func(settings string) ([]byte, error) {
		cmd := exec.CommandContext(cctx, sqlite, "-readonly", "-bail", "-json", "-cmd", ".timeout 2000", target, q(settings))
		hideCodexSQLiteWindow(cmd)
		return cmd.Output()
	}
	settings := `, COALESCE(model, '') AS model, COALESCE(reasoning_effort, '') AS effort`
	origin := `, COALESCE(originator, '') AS originator`
	var out []byte
	for _, columns := range []string{settings + origin, origin, settings, ""} {
		out, err = query(columns) // older schemas may lack settings, originator, or both
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	byID := map[string]codexAppThread{}
	if len(bytes.TrimSpace(out)) == 0 { // no row: none of the sidebar's chats is in the database
		return byID, nil
	}
	var rows []codexAppThread
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		r.includeRunning = r.Originator == "codex-tui" || strings.TrimSpace(r.Source) == "cli"
		byID[r.ID] = r
	}
	return byID, nil
}

// readCodexAppProjects reads the migrated App's projects and their roots from its state database (#6388): the
// projects the App moved out of local-projects, so a chat's project is still found by its cwd (codexAppState.project).
// A database without these tables or columns answers nothing and leaves the json's projects alone.
func readCodexAppProjects(home string) map[string]codexAppProject {
	db := codexAppDBPath(home)
	if db == "" {
		return nil
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Root string `json:"root"`
	}
	if err := querySQLite(context.Background(), db,
		`SELECT p.id AS id, p.name AS name, r.root AS root FROM projects p LEFT JOIN project_roots r ON r.project_id = p.id`, &rows); err != nil {
		return nil
	}
	out := map[string]codexAppProject{}
	for _, r := range rows {
		p := out[r.ID]
		p.Name = r.Name
		if r.Root != "" {
			p.RootPaths = append(p.RootPaths, r.Root)
		}
		out[r.ID] = p
	}
	return out
}

// querySQLite runs q against a SQLite database with the system sqlite3 and decodes the json rows into into
// (read-only, as readCodexThreads; a WAL database nobody has open is read as it is on disk, codexDBTarget): the Codex
// App's state database, opencode's (usage_opencode.go).
func querySQLite(ctx context.Context, db, q string, into any) error {
	sqlite, err := codexSQLiteProgram()
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, sqlite, "-readonly", "-bail", "-json", "-cmd", ".timeout 2000", codexDBTarget(db), q)
	hideCodexSQLiteWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	return json.Unmarshal(out, into)
}

// codexAppThreadOf: src is a chat the Codex App's sidebar lists that has no rollout, and its row in the App's database.
func codexAppThreadOf(src codexSource) (codexAppThread, bool) {
	if src.path != "" {
		return codexAppThread{}, false
	}
	t, ok := codexAppThreads(src.home)[src.thread]
	return t, ok
}

// registerCodexAppThreads registers the chats the Codex App's sidebar lists, which rollouts misses when their rollout is
// older than its two weeks or gone: with the rollout the App's database names while it exists, else with none.
func (n *Node) registerCodexAppThreads(home string, pass *codexListPass) {
	threads := codexAppThreads(home)
	if len(threads) == 0 {
		return
	}
	canonical := canonicalCodexHome(home, n.home)
	for _, t := range threads {
		path := ""
		if t.RolloutPath != "" {
			if _, err := pass.stat(t.RolloutPath); err == nil {
				path = t.RolloutPath
				if !pathWithin(path, canonical) {
					// The App names the rollout as it reached its home, which may be through a link (macOS's /var, a
					// linked ~/.codex). The rollout scan registers the same file under the resolved home, so register
					// it resolved too, or the two spellings of one file contend for the chat's path.
					path = realPath(path)
				}
			}
		}
		n.registerCanonicalCodex(canonical, t.ID, path)
	}
}
