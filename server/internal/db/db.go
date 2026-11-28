// Package db is the hub's Postgres store (docker container persona-postgres on the hub machine, see docs/server/server.md).
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The migrations are two sets (docs/server/server.md "Migrations"): the hub's tables in migrations/hub, the manager's in
// migrations/manager. A hub's database runs the hub set, a manager's the manager set; while the default manager
// shares the hub's database, both run there, each recording its own part.
//
//go:embed migrations/hub/*.sql migrations/manager/*.sql
var migrations embed.FS

// Sets says which migrations a database gets (and so which side's tables it has).
type Sets uint8

const (
	// HubSet is the hub's own tables: connections, devices and their tokens, pairing, push delivery, peers and the
	// mesh, the admin page, the managers' roster and bus identities.
	HubSet Sets = 1 << iota
	// ManagerSet is a manager's: the assistant, inbox, knowledge base, todos, boards, extensions, the computers'
	// sessions and metrics.
	ManagerSet
	// AllSets is both, the hub's first: a test that uses either side's tables.
	AllSets = HubSet | ManagerSet
)

// setDirs are the sets' directories under migrations/, in the order they run.
var setDirs = []struct {
	set  Sets
	part string // what schema_migrations.part records
}{{HubSet, "hub"}, {ManagerSet, "manager"}}

type DB struct {
	pool *pgxpool.Pool
	sets Sets
	// olderHub: the hub's database is shared with the default manager's settings table, which a hub build from before
	// hub_settings reads (followOlderHub): the hub's keys are kept there too.
	olderHub  atomic.Bool
	olderMu   sync.Mutex // one syncOlderHub at a time
	closeOnce sync.Once
	closeDone chan struct{}
	// pids are the server processes of this pool's connections (OtherManagers tells them from another process's).
	pids *pidSet
}

// QueryRow runs one ordinary read on the existing pool. It carries no manager-owned types.
func (d *DB) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.pool.QueryRow(ctx, query, args...)
}

// Transaction runs fn in one transaction on the existing pool.
func (d *DB) Transaction(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, d.pool, fn)
}

// ReadSnapshot runs related reads against the same committed state.
func ReadSnapshot(ctx context.Context, d *DB, fn func(pgx.Tx) error) error {
	return pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

// pidSet is the backend process ids of a pool's open connections.
type pidSet struct {
	mu sync.Mutex
	m  map[uint32]bool
}

func (p *pidSet) add(pid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.m == nil {
		p.m = map[uint32]bool{}
	}
	p.m[pid] = true
}

func (p *pidSet) remove(pid uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, pid)
}

func (p *pidSet) list() []int32 {
	if p == nil {
		return []int32{} // not NULL: pid = ANY(NULL) would match nothing and hide every connection
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int32, 0, len(p.m))
	for pid := range p.m {
		out = append(out, int32(pid))
	}
	return out
}

// Open connects and applies the pending migrations of sets. It retries for a while so the hub can start alongside
// the container.
func Open(ctx context.Context, url string, sets Sets) (*DB, error) {
	if sets == 0 || sets&^AllSets != 0 {
		return nil, fmt.Errorf("db.Open: bad migration sets %d", sets)
	}
	pids := &pidSet{}
	pool, err := connectTracked(ctx, url, appName(sets), pids)
	if err != nil {
		return nil, err
	}
	d := &DB{pool: pool, sets: sets, pids: pids}
	if err := d.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if sets == HubSet {
		if err := d.followOlderHub(ctx); err != nil {
			pool.Close()
			return nil, err
		}
	}
	return d, nil
}

// Attach connects the way Open does for sets, migrating nothing and taking up nothing (a tool that looks at a database
// another build owns: assistant drop-shared-tables).
func Attach(ctx context.Context, url string, sets Sets) (*DB, error) {
	pids := &pidSet{}
	pool, err := connectTracked(ctx, url, appName(sets), pids)
	if err != nil {
		return nil, err
	}
	return &DB{pool: pool, sets: sets, pids: pids}, nil
}

// Check connects the way Open does and lets go again, migrating nothing: a new hub binary waiting to take over
// (hubedge, a standby slot) checks it can reach the database while the one serving still owns the schema.
func Check(ctx context.Context, url string) error {
	pool, err := connect(ctx, url, appName(HubSet))
	if err != nil {
		return err
	}
	pool.Close()
	return nil
}

// BackendPIDs are the server process ids of this pool's open connections. A
// process that must tell its own sessions from another writer's (the hub,
// quiescing its own writers for a snapshot) passes them as the ones to ignore.
func (d *DB) BackendPIDs() []int32 { return d.pids.list() }

// appName is the application_name a part's connections give Postgres, so a part can tell the other's connections
// (and ones of a build from before, which give none) on a database they share (managerdb.go).
func appName(sets Sets) string {
	switch sets {
	case HubSet:
		return AppHub
	case ManagerSet:
		return AppManager
	}
	return "persona"
}

// AppHub and AppManager are the application_name of the hub's connections and a manager's.
const (
	AppHub     = "persona-hub"
	AppManager = "persona-manager"
)

func connect(ctx context.Context, url, app string) (*pgxpool.Pool, error) {
	return connectTracked(ctx, url, app, nil)
}

// connectTracked is connect, keeping the server process id of each connection in pids while it is open (nil: none).
func connectTracked(ctx context.Context, url, app string, pids *pidSet) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, errors.New("hub 的数据库还没装好。运行 personad hub setup，它会帮你建好。")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database_url: %w", err)
	}
	if _, set := cfg.ConnConfig.RuntimeParams["application_name"]; !set && app != "" {
		cfg.ConnConfig.RuntimeParams["application_name"] = app
	}
	cfg.MaxConns = 8
	cfg.AfterConnect = func(_ context.Context, c *pgx.Conn) error {
		utcTimes(c.TypeMap())
		if pids != nil {
			pids.add(c.PgConn().PID())
		}
		return nil
	}
	if pids != nil {
		cfg.BeforeClose = func(c *pgx.Conn) { pids.remove(c.PgConn().PID()) }
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.Ping(pctx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("connect postgres: %w", err)
		}
		time.Sleep(time.Second)
	}
	return pool, nil
}

// utcTimes makes m read timestamptz values in UTC. pgx reads them in time.Local by default, which puts the hub
// machine's offset ("+08:00") into every time the protocol sends, where docs/protocol/protocol.md has UTC.
func utcTimes(m *pgtype.Map) {
	m.RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
}

func (d *DB) Close() { _ = d.CloseContext(context.Background()) }

// CloseContext stops new acquisitions and bounds waiting for borrowed connections.
// pgxpool has no forced close: outstanding borrowers may finish after ctx expires.
func (d *DB) CloseContext(ctx context.Context) error {
	d.closeOnce.Do(func() {
		d.closeDone = make(chan struct{})
		go func() {
			d.pool.Close()
			close(d.closeDone)
		}()
	})
	select {
	case <-d.closeDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// migrate runs the pending migrations of the database's sets, in order, each in its own transaction, recording them
// in schema_migrations.
func (d *DB) migrate(ctx context.Context) error {
	return d.migrateFrom(ctx, migrations)
}

// renumbered are migrations a database may have recorded under the number they had before a merge gave them another
// one (two lines of work picked the same number). Such a record would make migrate skip the migration that has the old
// number now, and run the renumbered one again; so it is moved to the new number first, or dropped when the new number
// is recorded too. The renumbered migrations are idempotent; the ones that took over the old numbers never ran there.
var renumbered = []struct{ from, to string }{
	{"0520_task_workspaces.sql", "20261119163935_task_workspaces.sql"},   // earlier backend preparation builds
	{"0515_workspace_naming.sql", "20261118234802_workspace_naming.sql"}, // naming branch before timestamped migrations
	{"0513_todo_repositories.sql", "0516_todo_repositories.sql"},         // 0513 is assistant_search_plain
	{"0514_todo_repositories.sql", "0516_todo_repositories.sql"},         // 0514 is assistant_search_strong
	{"0071_pair_tokens.sql", "0111_pair_tokens.sql"},                     // 0071 is sent_messages
	{"0081_device_push_relay.sql", "0141_device_push_relay.sql"},         // 0081 is device_sessions
	{"0428_todo_source.sql", "0429_todo_source.sql"},                     // 0428 is manager_moved
	{"0492_client_password.sql", "0505_client_password.sql"},             // 0492 is the manager's agent_shares
}

// lastUnified is the last migration from before the split: a NULL-part row up to it stands for both sets.
const lastUnified = 419

// migrateFrom is migrate over the migrations/hub and migrations/manager directories of fsys (tests start from a
// database at an older state).
//
// schema_migrations has a row per migration a part applied: part is "hub" or "manager", NULL for the rows from before
// the split, when one set ran everything (such a row stands for both parts). A version is done for a set when it has a
// row of that part, or a NULL one from up to lastUnified (or, after it, of the same file: a number both sets use for
// different files, migrations_test.go sharedNumbers, is not taken as done by the other's row from a build before the
// split). So a database migrated before the split runs nothing again, and the few early migrations that are in both
// sets (they made tables for both sides; migrations_test.go bothSets lists them) run once per part on a database the
// hub and the default manager share.
//
// The rows are read once, under the lock: no migration writes schema_migrations, so they change only by what this
// records itself. A database that is up to date costs a few reads and no write (docs/server/server.md "Migrations").
// The files of every set are checked first (planMigrations): a set this build can't run refuses the open before the
// database is touched.
func (d *DB) migrateFrom(ctx context.Context, fsys fs.FS) error {
	var parts []string
	for _, sd := range setDirs {
		if d.sets&sd.set != 0 {
			parts = append(parts, sd.part)
		}
	}
	plan, err := planMigrations(fsys, parts)
	if err != nil {
		return err
	}
	// Serialize concurrent hubs, managers (and tests) on one advisory lock.
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(424242)`); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(424242)`) //nolint:errcheck
	if err := splitSchemaMigrations(ctx, conn); err != nil {
		return err
	}
	recs, err := readSchemaRecords(ctx, conn)
	if err != nil {
		return err
	}
	for _, r := range renumbered {
		if !recs.named(r.from) {
			continue // renumberMigration would change no row
		}
		if err := renumberMigration(ctx, conn, r.from, r.to); err != nil {
			return fmt.Errorf("renumber migration %s: %w", r.from, err)
		}
		if recs, err = readSchemaRecords(ctx, conn); err != nil {
			return err
		}
	}
	for _, part := range parts {
		if err := migrateSet(ctx, conn, fsys, part, plan[part], recs); err != nil {
			return err
		}
	}
	return nil
}

// schemaMigrationsSplit says whether schema_migrations is already as splitSchemaMigrations' statements leave it: a
// table in the schema CREATE TABLE makes it in, with a part column and a bigint version, without
// schema_migrations_pkey, and with a relation named schema_migrations_version_part next to it. Each condition is what
// one statement's IF [NOT] EXISTS, or the type it sets, looks at.
const schemaMigrationsSplit = `SELECT coalesce((
	SELECT c.relkind = 'r' AND n.nspname = current_schema()
		AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'part' AND a.attnum > 0 AND NOT a.attisdropped)
		AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.oid AND a.attname = 'version' AND a.atttypid = 'bigint'::regtype)
		AND NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid = c.oid AND k.conname = 'schema_migrations_pkey')
		AND EXISTS (SELECT 1 FROM pg_class i WHERE i.relnamespace = c.relnamespace AND i.relname = 'schema_migrations_version_part')
	FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.oid = to_regclass('schema_migrations')), false)`

// splitSchemaMigrations makes schema_migrations, or gives one from before the split its part column: the rows it has
// keep part NULL, and a version may have a row for each part. It also widens an int version to bigint, as a
// timestamped migration's version needs (migrationVersion); the rows keep their numbers. A build from before reads the
// wider column into a Go int, 64 bits on every platform the hub and the managers are built for, and finds its own
// migrations done: v0.8.2 itself was run against such databases (#10618; TestOlderBuildAfterTimestampedMigrations
// replays only its catalog check and its read). A table that is so already is left alone: an ALTER TABLE
// that changes nothing still takes an ACCESS EXCLUSIVE lock and a transaction id, on every start.
func splitSchemaMigrations(ctx context.Context, conn *pgxpool.Conn) error {
	var split bool
	if err := conn.QueryRow(ctx, schemaMigrationsSplit).Scan(&split); err != nil {
		return fmt.Errorf("schema_migrations: %w", err)
	}
	if split {
		return nil
	}
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version bigint NOT NULL, name text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now(), part text)`,
		`ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS part text`,
		`ALTER TABLE schema_migrations DROP CONSTRAINT IF EXISTS schema_migrations_pkey`,
		`CREATE UNIQUE INDEX IF NOT EXISTS schema_migrations_version_part ON schema_migrations (version, (coalesce(part, '')))`,
		`ALTER TABLE schema_migrations ALTER COLUMN version TYPE bigint`,
	} {
		if _, err := conn.Exec(ctx, q); err != nil {
			return fmt.Errorf("schema_migrations: %w", err)
		}
	}
	return nil
}

// schemaRecord is a schema_migrations row of some version: part is NULL on the rows from before the split.
type schemaRecord struct {
	name string
	part pgtype.Text
}

// schemaRecords are schema_migrations' rows by version.
type schemaRecords map[int64][]schemaRecord

func readSchemaRecords(ctx context.Context, conn *pgxpool.Conn) (schemaRecords, error) {
	rows, err := conn.Query(ctx, `SELECT version, name, part FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	recs := schemaRecords{}
	var version int64
	var r schemaRecord
	_, err = pgx.ForEachRow(rows, []any{&version, &r.name, &r.part}, func() error {
		recs[version] = append(recs[version], r)
		return nil
	})
	return recs, err
}

// named says whether a row records the file name.
func (recs schemaRecords) named(name string) bool {
	for _, rs := range recs {
		for _, r := range rs {
			if r.name == name {
				return true
			}
		}
	}
	return false
}

// done says whether part has migration name (numbered version) applied: a row of that part, or a NULL-part one from up
// to lastUnified or of the same file (migrateFrom).
func (recs schemaRecords) done(version int64, name, part string) bool {
	for _, r := range recs[version] {
		if r.part.Valid && r.part.String == part || !r.part.Valid && (version <= lastUnified || r.name == name) {
			return true
		}
	}
	return false
}

// migrationFile is a migration a part runs: its file name and the version it records.
type migrationFile struct {
	name    string
	version int64
}

// planMigrations is each part's migrations (directories under migrations/ of fsys) in name order, checked as a whole
// before any runs: a name with no version refuses the open, and so do two files of one version in a part, since the
// record of one would make the other look done. Nothing is applied then, in any part, so no database is left with
// half of them; the error names every file to fix, and how.
func planMigrations(fsys fs.FS, parts []string) (map[string][]migrationFile, error) {
	plan := map[string][]migrationFile{}
	var errs []error
	for _, part := range parts {
		entries, err := fs.ReadDir(fsys, "migrations/"+part)
		if err != nil {
			return nil, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		byVersion := map[int64][]string{}
		for _, e := range entries {
			version, err := migrationVersion(e.Name())
			if err != nil {
				errs = append(errs, fmt.Errorf("migrations/%s: %w", part, err))
				continue
			}
			byVersion[version] = append(byVersion[version], e.Name())
			plan[part] = append(plan[part], migrationFile{e.Name(), version})
		}
		for _, f := range plan[part] {
			if names := byVersion[f.version]; len(names) > 1 && names[0] == f.name {
				errs = append(errs, fmt.Errorf("migrations/%s: %s have the same version %d: keep the name of the "+
					"one that merged first (a database may have recorded it) and rename the others, each to a UTC "+
					"second of its own: $(date -u +%%Y%%m%%d%%H%%M%%S)_<name>.sql", part, strings.Join(names, ", "), f.version))
			}
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("no migration was applied: %w", errors.Join(errs...))
	}
	return plan, nil
}

// migrateSet applies part's migrations (planMigrations) that part has not (recs), adding each to recs as it records
// it.
func migrateSet(ctx context.Context, conn *pgxpool.Conn, fsys fs.FS, part string, files []migrationFile, recs schemaRecords) error {
	for _, f := range files {
		if recs.done(f.version, f.name, part) {
			continue
		}
		sql, err := fs.ReadFile(fsys, "migrations/"+part+"/"+f.name)
		if err != nil {
			return err
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version, name, part) VALUES ($1, $2, $3)`, f.version, f.name, part)
			return err
		})
		if err != nil {
			return fmt.Errorf("migration %s/%s: %w", part, f.name, err)
		}
		recs[f.version] = append(recs[f.version], schemaRecord{name: f.name, part: pgtype.Text{String: part, Valid: true}})
	}
	return nil
}

// A migration file is named YYYYMMDDHHMMSS_<name>.sql, the UTC second it was written, so two lines of work adding one
// at once practically never pick the same version, and neither has to look anything up (docs/server/server.md
// "Migrations"; planMigrations refuses a set where they did). The files
// from before are NNNN_<name>.sql, numbers picked by hand, frozen (migrations_test.go lastNumbered). A timestamp has
// more digits than a number and starts with 2 where every number starts with 0, so the timestamped files sort after
// the numbered ones, and name order is version order.
const (
	numberedDigits = 4
	stampLayout    = "20060102150405"
)

// migrationVersion is the version a migration file name starts with: its number, or its timestamp read as one
// (20261119123000).
func migrationVersion(name string) (int64, error) {
	prefix, _, ok := strings.Cut(name, "_")
	if !ok || strings.Trim(prefix, "0123456789") != "" ||
		len(prefix) != numberedDigits && len(prefix) != len(stampLayout) {
		return 0, fmt.Errorf("bad migration name %s: want YYYYMMDDHHMMSS_<name>.sql", name)
	}
	if len(prefix) == len(stampLayout) {
		if _, err := time.Parse(stampLayout, prefix); err != nil {
			return 0, fmt.Errorf("bad migration name %s: %s is not a UTC time YYYYMMDDHHMMSS", name, prefix)
		}
	}
	return strconv.ParseInt(prefix, 10, 64)
}

// renumberMigration moves a database's record of migration from (applied under its old number) to to's name and
// number; when to is recorded already, the old record only goes away.
func renumberMigration(ctx context.Context, conn *pgxpool.Conn, from, to string) error {
	version, err := migrationVersion(to)
	if err != nil {
		return err
	}
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM schema_migrations WHERE name = $1
			AND EXISTS (SELECT 1 FROM schema_migrations WHERE version = $2)`, from, version); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE schema_migrations SET version = $2, name = $3 WHERE name = $1`, from, version, to)
		return err
	})
}

// TruncateForTest clears business tables in a test database and preserves its migration ledger.
func (d *DB) TruncateForTest(ctx context.Context) error {
	tables, err := d.tablesToTruncateForTest(ctx, false)
	if err != nil {
		return fmt.Errorf("discover test tables: %w", err)
	}
	return d.truncateNonEmpty(ctx, tables)
}

// tablesToTruncateForTest finds business tables independently of the search path.
// A heap with no descendants and zero main-fork bytes has no rows to probe. Tables with
// allocated pages, inheritance, partitioning, or a different access method still need EXISTS.
// includeEmpty bypasses the physical check when testing migration coverage.
func (d *DB) tablesToTruncateForTest(ctx context.Context, includeEmpty bool) ([]string, error) {
	var tables []string
	err := d.pool.QueryRow(ctx, `SELECT ARRAY(
		SELECT pg_catalog.quote_ident(t.schemaname) || '.' || pg_catalog.quote_ident(t.tablename)
		FROM pg_catalog.pg_tables t
		JOIN pg_catalog.pg_namespace n ON n.nspname = t.schemaname
		JOIN pg_catalog.pg_class c ON c.relnamespace = n.oid AND c.relname = t.tablename
		WHERE left(t.schemaname, 3) <> 'pg_' AND t.schemaname <> 'information_schema'
			AND t.tablename <> 'schema_migrations'
			AND ($1 OR CASE WHEN c.relkind = 'r' AND NOT c.relhassubclass
				AND c.relam = (SELECT oid FROM pg_catalog.pg_am WHERE amname = 'heap')
				THEN pg_catalog.pg_relation_size(c.oid) > 0 ELSE true END)
		ORDER BY t.schemaname, t.tablename)`, includeEmpty).Scan(&tables)
	return tables, err
}

// truncateNonEmpty uses one TRUNCATE ... CASCADE for tables with rows and their foreign-key dependents.
// Skipping unrelated empty tables saves their exclusive locks and file rewrites.
//
// Discovery and the row probe run before TRUNCATE. Call the reset at the start of a test,
// before its writers start: a row committed after a probe observes an empty table can remain.
func (d *DB) truncateNonEmpty(ctx context.Context, tables []string) error {
	if len(tables) == 0 {
		return nil
	}
	probe := make([]string, len(tables))
	for i, t := range tables {
		probe[i] = `EXISTS (SELECT FROM ` + t + `)`
	}
	var hasRows []bool
	if err := d.pool.QueryRow(ctx, `SELECT ARRAY[`+strings.Join(probe, ", ")+`]`).Scan(&hasRows); err != nil {
		return err
	}
	// Keep catalog order when taking the truncate locks.
	var names []string
	for i, found := range hasRows {
		if found {
			names = append(names, tables[i])
		}
	}
	if len(names) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `TRUNCATE `+strings.Join(names, ", ")+` CASCADE`)
	return err
}
