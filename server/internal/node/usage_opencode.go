package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/tokenuse"
)

// opencode's token usage (docs/server/machines.md "Token usage"): it keeps its sessions in a SQLite database, a row a
// step (tokenuse.OpencodeRow), so its adapter reads by query instead of by line, with the system sqlite3 as the Codex
// App's database is read (querySQLite): the live meter the rows added since its last read and the unfinished ones
// again, nothing while the database has not changed; the backfill a week's rows by their time (the database indexes
// it). Without sqlite3, or with a database of another schema (an older opencode), opencode is not metered.

// usageQuery is an agent whose usage is in a database. A value keeps what its live reads need between sweeps.
type usageQuery interface {
	agent() string
	// poll is what finished since the last poll (the first: since cutoff).
	poll(ctx context.Context, n *Node, cutoff time.Time) []tokenuse.Record
	// between is what finished from start to end, by the time a request began.
	between(ctx context.Context, n *Node, start, end time.Time) ([]tokenuse.Record, error)
	// first is the time of the earliest request it holds (zero: none, or unknown).
	first(ctx context.Context, n *Node) time.Time
	// dir is the directory session started in, as the database says ("" when it does not): read with the rows that
	// name the session (usage_dirs.go).
	dir(session string) string
}

// usageQueries are the agents this node meters by query (new values: a live meter keeps its own).
func (n *Node) usageQueries() []usageQuery { return []usageQuery{&opencodeUsage{}} }

// opencodeDB is opencode's database: PERSONA_OPENCODE_DB (the opencode runtime's too), else opencode's own place under
// XDG_DATA_HOME (default <home>/.local/share); "" for a node bound to a session scope.
func (n *Node) opencodeDB() string {
	if n.sessionScope != nil {
		return ""
	}
	if p := strings.TrimSpace(os.Getenv("PERSONA_OPENCODE_DB")); p != "" {
		return p
	}
	data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if data == "" {
		data = filepath.Join(n.home, ".local", "share")
	}
	return filepath.Join(data, "opencode", "opencode.db")
}

// opencodeUsage reads opencode's steps.
type opencodeUsage struct {
	stamp   string          // the database's and its WAL's sizes and times at the last read
	last    int64           // the highest rowid read
	pending map[int64]int64 // rows read before they finished: rowid → when the step began (Unix ms)
	started bool
	failed  string // the last read's error, logged once
	// dirs: each session's directory (session.directory), read once for the sessions the rows name; "" for one
	// the database does not say (an older schema, a session gone).
	dirs map[string]string
}

func (o *opencodeUsage) dir(session string) string { return o.dirs[session] }

// readDirs reads the directories of the sessions recs name that it has not read, apart from the usage query: a
// database without a session table still has its usage read.
func (o *opencodeUsage) readDirs(ctx context.Context, n *Node, recs []tokenuse.Record) {
	if o.dirs == nil {
		o.dirs = map[string]string{}
	}
	var ids []string
	for _, r := range recs {
		if _, ok := o.dirs[r.Session]; !ok && opencodeSessionID(r.Session) && !slices.Contains(ids, r.Session) {
			ids = append(ids, r.Session)
		}
	}
	if len(ids) == 0 || n.opencodeDB() == "" {
		return
	}
	for _, id := range ids {
		o.dirs[id] = ""
	}
	var rows []struct {
		ID        string `json:"id"`
		Directory string `json:"directory"`
	}
	if querySQLite(ctx, n.opencodeDB(), `SELECT id, coalesce(directory, '') AS directory FROM session WHERE id IN ('`+
		strings.Join(ids, "', '")+`')`, &rows) != nil {
		return
	}
	for _, r := range rows {
		o.dirs[r.ID] = r.Directory
	}
}

// opencodeSessionID: an id that can go into the query as it is (opencode's are letters, digits, "_" and "-").
func opencodeSessionID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (*opencodeUsage) agent() string { return tokenuse.Opencode }

// opencodeColumns are what a usage read takes of a row: its rowid, and tokenuse.OpencodeRow's fields.
const opencodeColumns = `SELECT rowid AS rowid, id, session_id, type, time_created,
	json_extract(data, '$.time.completed') AS completed, coalesce(json_extract(data, '$.status'), '') AS status,
	coalesce(json_extract(data, '$.model.id'), '') AS model,
	coalesce(json_extract(data, '$.tokens.input'), 0) AS input, coalesce(json_extract(data, '$.tokens.output'), 0) AS output,
	coalesce(json_extract(data, '$.tokens.reasoning'), 0) AS reasoning,
	coalesce(json_extract(data, '$.tokens.cache.read'), 0) AS cache_read,
	coalesce(json_extract(data, '$.tokens.cache.write'), 0) AS cache_write
	FROM session_message WHERE type IN ('assistant', 'compaction')`

type opencodeRow struct {
	RowID int64 `json:"rowid"`
	tokenuse.OpencodeRow
}

func (o *opencodeUsage) query(ctx context.Context, n *Node, where string) ([]opencodeRow, bool) {
	db := n.opencodeDB()
	if db == "" {
		return nil, false
	}
	if _, err := os.Stat(db); err != nil {
		return nil, false
	}
	var rows []opencodeRow
	if err := querySQLite(ctx, db, opencodeColumns+" AND "+where+" ORDER BY rowid", &rows); err != nil {
		if msg := err.Error(); msg != o.failed && ctx.Err() == nil {
			o.failed = msg
			n.logger.Printf("usage: opencode's database not read: %v", err)
		}
		return nil, false
	}
	o.failed = ""
	return rows, true
}

func (o *opencodeUsage) poll(ctx context.Context, n *Node, cutoff time.Time) []tokenuse.Record {
	db := n.opencodeDB()
	if db == "" {
		return nil
	}
	stamp := fileStamp(db) + fileStamp(db+"-wal")
	if stamp == o.stamp {
		return nil
	}
	where := "time_created >= " + strconv.FormatInt(cutoff.UnixMilli(), 10)
	if o.started {
		ids := make([]string, 0, len(o.pending)+1)
		ids = append(ids, strconv.FormatInt(o.last, 10))
		for id := range o.pending {
			ids = append(ids, strconv.FormatInt(id, 10))
		}
		where = "(rowid > " + strconv.FormatInt(o.last, 10) + " OR rowid IN (" + strings.Join(ids, ", ") + "))"
	}
	rows, ok := o.query(ctx, n, where)
	if !ok {
		return nil
	}
	o.stamp, o.started = stamp, true
	if o.pending == nil {
		o.pending = map[int64]int64{}
	}
	var out []tokenuse.Record
	for _, r := range rows {
		o.last = max(o.last, r.RowID)
		if !r.Finished() {
			o.pending[r.RowID] = r.Created
			continue
		}
		delete(o.pending, r.RowID)
		if rec, ok := r.Record(); ok {
			out = append(out, rec)
		}
	}
	for id, created := range o.pending { // a step that never finished (opencode stopped) leaves with the window
		if created < cutoff.UnixMilli() {
			delete(o.pending, id)
		}
	}
	o.readDirs(ctx, n, out)
	return out
}

func (o *opencodeUsage) between(ctx context.Context, n *Node, start, end time.Time) ([]tokenuse.Record, error) {
	rows, _ := o.query(ctx, n, "time_created >= "+strconv.FormatInt(start.UnixMilli(), 10)+
		" AND time_created < "+strconv.FormatInt(end.UnixMilli(), 10))
	var out []tokenuse.Record
	for _, r := range rows {
		if rec, ok := r.Record(); ok {
			out = append(out, rec)
		}
	}
	o.readDirs(ctx, n, out)
	return out, ctx.Err()
}

func (o *opencodeUsage) first(ctx context.Context, n *Node) time.Time {
	db := n.opencodeDB()
	if db == "" {
		return time.Time{}
	}
	if _, err := os.Stat(db); err != nil {
		return time.Time{}
	}
	var rows []struct {
		First *int64 `json:"first"`
	}
	if querySQLite(ctx, db, `SELECT min(time_created) AS first FROM session_message WHERE type IN ('assistant', 'compaction')`, &rows) != nil ||
		len(rows) == 0 || rows[0].First == nil {
		return time.Time{}
	}
	return time.UnixMilli(*rows[0].First)
}
