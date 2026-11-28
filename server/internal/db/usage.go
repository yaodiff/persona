package db

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/tokenuse"
)

// ---- token usage by machine (docs/server/machines.md "Token usage") ----

// UsageMinuteStep is the step of a row as a node reported it; UsageRolledStep of ten minutes rolled up; UsageHourStep
// of an hour, rolled up from the ten-minute rows past a week and kept for the calendar's year.
const (
	UsageMinuteStep = 60
	UsageRolledStep = 600
	UsageHourStep   = 3600
)

const putUsageSQL = `INSERT INTO machine_usage (machine_id, ts, step, model, launcher, agent, dir, input, cache_write,
		cache_read, output, requests)
	VALUES ($1, to_timestamp($2), 60, $3, $4, $5, $11, $6, $7, $8, $9, $10)
	ON CONFLICT (machine_id, ts, step, model, launcher, agent, dir) DO UPDATE SET input = $6, cache_write = $7,
		cache_read = $8, output = $9, requests = $10`

const addUsageSQL = `INSERT INTO machine_usage AS u (machine_id, ts, step, model, launcher, agent, dir, input, cache_write,
cache_read, output, requests) VALUES ($1, to_timestamp($2), 60, $3, $4, $5, $11, $6, $7, $8, $9, $10)
ON CONFLICT (machine_id, ts, step, model, launcher, agent, dir) DO UPDATE SET input = u.input + excluded.input,
cache_write = u.cache_write + excluded.cache_write, cache_read = u.cache_read + excluded.cache_read,
output = u.output + excluded.output, requests = u.requests + excluded.requests`

// putUsageDirSQL keeps a node's latest resolution of a directory (proto.UsageDir; docs/server/machines.md "Token usage
// by project"), except that a temporary folder's "temp" never replaces another resolution (#16827: a node says it for
// a folder that is gone too, and a worktree removed since keeps its repository; devices' replacesResolution).
const putUsageDirSQL = `INSERT INTO usage_dirs AS d (machine_id, dir, project, name, host, place, worktree)
	VALUES ($1, $2, $3, $4, $5, $6, $7)
	ON CONFLICT (machine_id, dir) DO UPDATE SET project = excluded.project, name = excluded.name, host = excluded.host,
		place = excluded.place, worktree = excluded.worktree, updated_at = now()
	WHERE excluded.project <> 'temp' OR d.project = 'temp'`

// AddUsage stores usage that has no local record to reconstruct on reconnect.
func (d *DB) AddUsage(ctx context.Context, machine string, buckets []proto.UsageBucket) error {
	rejected, err := d.PutUsages(ctx, []UsageWrite{{Machine: machine, Buckets: buckets, Add: true}})
	if err == nil && len(rejected) > 0 {
		return rejected[0]
	}
	return err
}

// RecentRuntimeUsage restores the minute buckets the nodes cannot reconstruct.
func (d *DB) RecentRuntimeUsage(ctx context.Context, from time.Time) (map[string][]proto.UsageBucket, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, extract(epoch FROM ts)::bigint, model, launcher, agent, dir,
		input, cache_write, cache_read, output, requests FROM machine_usage
		WHERE step = 60 AND ts >= $1 AND agent <> ALL($2)`, from, append(tokenuse.FileAgents(), ""))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string][]proto.UsageBucket)
	for rows.Next() {
		var machine string
		var b proto.UsageBucket
		if err := rows.Scan(&machine, &b.Minute, &b.Model, &b.Launcher, &b.Agent, &b.Dir, &b.Input, &b.CacheWrite, &b.CacheRead, &b.Output, &b.Requests); err != nil {
			return nil, err
		}
		out[machine] = append(out[machine], b)
	}
	return out, rows.Err()
}

// UsageWrite is one node's "usage" notification to store (PutUsage's arguments).
type UsageWrite struct {
	Machine     string
	Buckets     []proto.UsageBucket
	ReplaceFrom time.Time
	Add         bool // runtime-only deltas, never reconstructed by a file scan
	// Dirs: what the node resolved directories to (the notification's "dirs"), kept before the buckets.
	Dirs []proto.UsageDir
}

func queueUsage(b *pgx.Batch, w UsageWrite) {
	for _, x := range w.Dirs {
		b.Queue(putUsageDirSQL, w.Machine, x.Dir, x.Project, x.Name, x.Host, x.Place, x.Worktree)
	}
	if !w.ReplaceFrom.IsZero() {
		b.Queue(`DELETE FROM machine_usage WHERE machine_id = $1 AND step = 60 AND ts >= to_timestamp($2) AND agent = ANY($3)`,
			w.Machine, w.ReplaceFrom.Unix(), append(tokenuse.FileAgents(), ""))
	}
	sql := putUsageSQL
	if w.Add {
		sql = addUsageSQL
	}
	for _, x := range w.Buckets {
		b.Queue(sql, w.Machine, x.Minute, x.Model, x.Launcher, x.Agent, x.Input, x.CacheWrite, x.CacheRead, x.Output,
			x.Requests, x.Dir)
	}
}

// PutUsage stores a node's buckets: a bucket is its minute's whole total, so it replaces what was there. With
// replaceFrom set (the node's whole window, "full"), only the file-metered agents' minute rows and legacy
// unknown-agent rows from then on are deleted first. Runtime-only agents survive; file records replace older
// attribution and duplicate counting.
func (d *DB) PutUsage(ctx context.Context, machine string, buckets []proto.UsageBucket, replaceFrom time.Time) error {
	_, err := d.PutUsages(ctx, []UsageWrite{{Machine: machine, Buckets: buckets, ReplaceFrom: replaceFrom}})
	return err
}

// PutUsages stores several writes as PutUsage does, in order, in one transaction: one commit, so one WAL flush,
// instead of one a write. A write Postgres rejects costs only itself: rejected[i] is the error writes[i] got
// (writeAll).
func (d *DB) PutUsages(ctx context.Context, writes []UsageWrite) (rejected []error, err error) {
	return d.writeAll(ctx, len(writes), func(b *pgx.Batch, i int) { queueUsage(b, writes[i]) })
}

// RollupUsage sums the minute rows before before (rounded down to ten minutes) into one row a machine, ten minutes,
// model, launcher, agent and dir. Moving and summing is one statement, so a minute is rolled up once; a ten-minute row already
// there (a node that sent an old minute again) is added to.
func (d *DB) RollupUsage(ctx context.Context, before time.Time) (int64, error) {
	return d.rollupUsage(ctx, UsageMinuteStep, UsageRolledStep, before)
}

// RollupUsageHours sums the ten-minute rows before before (rounded down to the hour) into one row a machine, hour,
// model, launcher, agent and dir, as RollupUsage does the minutes.
func (d *DB) RollupUsageHours(ctx context.Context, before time.Time) (int64, error) {
	return d.rollupUsage(ctx, UsageRolledStep, UsageHourStep, before)
}

// rollupUsage moves the rows of step from before before (rounded down to step to) into rows of step to.
func (d *DB) rollupUsage(ctx context.Context, from, to int, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `WITH moved AS (
			DELETE FROM machine_usage WHERE step = $2 AND ts < to_timestamp($1) RETURNING *
		)
		INSERT INTO machine_usage AS u (machine_id, ts, step, model, launcher, agent, dir, input, cache_write, cache_read,
			output, requests)
		SELECT machine_id, date_bin(make_interval(secs => $3), ts, timestamptz 'epoch'), $3, model, launcher, agent, dir,
			sum(input), sum(cache_write), sum(cache_read), sum(output), sum(requests)
		FROM moved GROUP BY 1, 2, 4, 5, 6, 7
		ON CONFLICT (machine_id, ts, step, model, launcher, agent, dir) DO UPDATE SET input = u.input + excluded.input,
			cache_write = u.cache_write + excluded.cache_write, cache_read = u.cache_read + excluded.cache_read,
			output = u.output + excluded.output, requests = u.requests + excluded.requests`,
		before.Truncate(time.Duration(to)*time.Second).Unix(), from, to)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// PruneUsage deletes every row older than before.
func (d *DB) PruneUsage(ctx context.Context, before time.Time) (int64, error) {
	tag, err := d.pool.Exec(ctx, `DELETE FROM machine_usage WHERE ts < to_timestamp($1)`, before.Unix())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// UsageRow is one model and launcher's usage over a step (Minute: the step's start).
type UsageRow = proto.UsageBucket

// UsageSeries sums a machine's rows from from (inclusive) to to over steps of step seconds (aligned to the Unix
// epoch; a multiple of ten minutes, or of a minute under 27 hours), one row a step, model and launcher (every agent's
// together), in time order.
func (d *DB) UsageSeries(ctx context.Context, machine string, from, to time.Time, step int) ([]UsageRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT extract(epoch FROM date_bin(make_interval(secs => $4), ts, timestamptz 'epoch'))::bigint,
			model, launcher, sum(input)::bigint, sum(cache_write)::bigint, sum(cache_read)::bigint, sum(output)::bigint, sum(requests)::bigint
		FROM machine_usage WHERE machine_id = $1 AND ts >= to_timestamp($2) AND ts < to_timestamp($3)
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, machine, from.Unix(), to.Unix(), step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageRow
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Minute, &r.Model, &r.Launcher, &r.Input, &r.CacheWrite, &r.CacheRead, &r.Output, &r.Requests); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageBy is how UsageSpans splits a span's tokens: every machine's and agent's together, or by machine, or by agent.
type UsageBy int

const (
	UsageSum UsageBy = iota
	UsageByMachine
	UsageByAgent
)

// UsageSpan is the tokens of one span of UsageSpans' bounds: every machine's, or one machine's or one agent's.
type UsageSpan struct {
	Span       int    // bounds[Span] to bounds[Span+1]
	Machine    string // UsageByMachine's; "" otherwise
	Agent      string // UsageByAgent's ("" for the rows stored before agents were kept apart); "" otherwise
	Input      int64  // not from the cache
	CacheWrite int64
	CacheRead  int64
	Output     int64
	Requests   int
}

// UsageSpans sums every machine's rows into the spans between bounds (in time order: a span is [bounds[i],
// bounds[i+1])), all together or each machine's or agent's apart (by); only the spans with rows, in order. A row counts in the
// span its start is in: the days and hours of a zone the clock is not a whole hour from UTC in split the hour rows
// at the hour (docs/server/machines.md "Token usage").
func (d *DB) UsageSpans(ctx context.Context, bounds []time.Time, by UsageBy) ([]UsageSpan, error) {
	if len(bounds) < 2 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT width_bucket(ts, $1::timestamptz[]) - 1,
			CASE WHEN $2::int = 1 THEN machine_id ELSE '' END, CASE WHEN $2::int = 2 THEN agent ELSE '' END,
			sum(input)::bigint, sum(cache_write)::bigint, sum(cache_read)::bigint, sum(output)::bigint, sum(requests)::bigint
		FROM machine_usage WHERE ts >= $3 AND ts < $4
		GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, bounds, int(by), bounds[0], bounds[len(bounds)-1])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSpan
	for rows.Next() {
		var s UsageSpan
		var req int64
		if err := rows.Scan(&s.Span, &s.Machine, &s.Agent, &s.Input, &s.CacheWrite, &s.CacheRead, &s.Output, &req); err != nil {
			return nil, err
		}
		s.Requests = int(req)
		out = append(out, s)
	}
	return out, rows.Err()
}

// UsageDirRow is one machine's latest resolution of one directory (usage_dirs; docs/server/machines.md "Token usage by
// project").
type UsageDirRow struct {
	Machine string
	proto.UsageDir
}

// UsageDirs is every machine's resolution of its directories, by machine and directory.
func (d *DB) UsageDirs(ctx context.Context) ([]UsageDirRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, dir, project, name, host, place, worktree FROM usage_dirs
		ORDER BY machine_id, dir`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDirRow
	for rows.Next() {
		var r UsageDirRow
		if err := rows.Scan(&r.Machine, &r.Dir, &r.Project, &r.Name, &r.Host, &r.Place, &r.Worktree); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutUsageDirs keeps a node's latest resolution of each of the directories (a backfill's: usage_dirs).
func (d *DB) PutUsageDirs(ctx context.Context, machine string, dirs []proto.UsageDir) error {
	if len(dirs) == 0 {
		return nil
	}
	b := &pgx.Batch{}
	for _, x := range dirs {
		b.Queue(putUsageDirSQL, machine, x.Dir, x.Project, x.Name, x.Host, x.Place, x.Worktree)
	}
	return d.pool.SendBatch(ctx, b).Close()
}

// UsageDirSpan is the tokens of one span of UsageDirSpans' bounds of one machine's directory, and with detail of one
// agent, model and launcher there.
type UsageDirSpan struct {
	Span                         int // bounds[Span] to bounds[Span+1]
	Machine, Dir                 string
	Agent, Model, Launcher       string // UsageDirSpans' detail; "" otherwise
	Input, CacheWrite, CacheRead int64
	Output                       int64
	Requests                     int
}

// UsageDirSpans sums every machine's rows into the spans between bounds (in time order) by machine and directory, and
// with detail by agent, model and launcher too; only the directories in dirs when it is not nil. A row counts in the
// span its start is in, as in UsageSpans.
func (d *DB) UsageDirSpans(ctx context.Context, bounds []time.Time, detail bool, dirs []string) ([]UsageDirSpan, error) {
	if len(bounds) < 2 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT width_bucket(ts, $1::timestamptz[]) - 1, machine_id, dir,
			CASE WHEN $2::boolean THEN agent ELSE '' END, CASE WHEN $2::boolean THEN model ELSE '' END,
			CASE WHEN $2::boolean THEN launcher ELSE '' END,
			sum(input)::bigint, sum(cache_write)::bigint, sum(cache_read)::bigint, sum(output)::bigint, sum(requests)::bigint
		FROM machine_usage WHERE ts >= $3 AND ts < $4 AND ($5::text[] IS NULL OR dir = ANY($5::text[]))
		GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, 2, 3, 4, 5, 6`, bounds, detail, bounds[0], bounds[len(bounds)-1], dirs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageDirSpan
	for rows.Next() {
		var s UsageDirSpan
		var req int64
		if err := rows.Scan(&s.Span, &s.Machine, &s.Dir, &s.Agent, &s.Model, &s.Launcher, &s.Input, &s.CacheWrite,
			&s.CacheRead, &s.Output, &req); err != nil {
			return nil, err
		}
		s.Requests = int(req)
		out = append(out, s)
	}
	return out, rows.Err()
}

// UsageBackfill is how far a machine's backfill of an agent got (usage_backfill, docs/server/machines.md "Backfill").
type UsageBackfill struct {
	Through time.Time // the start of the last hour sent; zero: none yet
	Done    time.Time // when the scan finished; zero: not yet
	Hours   int       // hours stored
}

// UsageMeteredBefore are the agents the nodes metered before the manager kept agents apart (#9984): the usage stored
// then ("") may hold theirs, so an hour with it has theirs, and their backfill does not add to it.
var UsageMeteredBefore = []string{"claude", "codex", "traex"}

// usageFirstRecord are the agents whose backfill is the machine's first record (""): Claude Code's, read before
// agents were kept apart, and an older node's. Codex's and traex's have records of their own since #10002 (their
// first backfill read only two weeks of rollouts).
var usageFirstRecord = []string{"", "claude"}

// UsageBackfillRecord is the record an agent's backfill is kept in: "" for Claude Code (and an older node's), its own
// otherwise.
func UsageBackfillRecord(agent string) string {
	if slices.Contains(usageFirstRecord, agent) {
		return ""
	}
	return agent
}

// UsageBackfill is machine's backfill of agent so far (zero: never started), from its record (UsageBackfillRecord).
func (d *DB) UsageBackfill(ctx context.Context, machine, agent string) (UsageBackfill, error) {
	var b UsageBackfill
	var through, done *time.Time
	err := d.pool.QueryRow(ctx, `SELECT through, done_at, hours FROM usage_backfill WHERE machine_id = $1 AND agent = $2`,
		machine, UsageBackfillRecord(agent)).Scan(&through, &done, &b.Hours)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, nil
	}
	if through != nil {
		b.Through = *through
	}
	if done != nil {
		b.Done = *done
	}
	return b, err
}

// PutUsageBackfill stores one request of machine's backfill of agents (the agents it read, an older node's none: the
// first record): buckets (Minute an hour's start, any order) go in as hour rows, but only for the hours that had no
// row of the machine and the bucket's agent (of any step), nor one stored before agents were kept apart when the agent
// was metered then (UsageMeteredBefore), before it: the live meter's count stays and a backfill sent again adds
// nothing; through (the last hour sent) and done move the agents' records on. One transaction; stored is the hours
// added, an hour counted once a record.
func (d *DB) PutUsageBackfill(ctx context.Context, machine string, agents []string, buckets []proto.UsageBucket, through time.Time, done bool) (stored int, err error) {
	n := len(buckets)
	hours, models, launchers, names, dirs := make([]int64, n), make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	in, cw, cr, out, req := make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n), make([]int64, n)
	for i, b := range buckets {
		hours[i], models[i], launchers[i], names[i], dirs[i] = b.Minute, b.Model, b.Launcher, b.Agent, b.Dir
		in[i], cw[i], cr[i], out[i], req[i] = b.Input, b.CacheWrite, b.CacheRead, b.Output, int64(b.Requests)
	}
	records := map[string]int{}
	for _, a := range agents {
		records[UsageBackfillRecord(a)] = 0
	}
	if len(records) == 0 {
		records[""] = 0
	}
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if n > 0 {
			// had: the machine's hours and the agents with a row in them, as the statement starts (its own rows are
			// not in it).
			rows, err := tx.Query(ctx, `WITH b AS (
					SELECT date_bin('1 hour', to_timestamp(hour), timestamptz 'epoch') AS ts, model, launcher, agent, dir, input,
						cache_write, cache_read, output, requests
					FROM unnest($2::bigint[], $3::text[], $4::text[], $5::text[], $6::bigint[], $7::bigint[], $8::bigint[],
						$9::bigint[], $10::bigint[], $13::text[]) AS u(hour, model, launcher, agent, input, cache_write, cache_read, output,
						requests, dir)
				), had AS (
					SELECT DISTINCT date_bin('1 hour', ts, timestamptz 'epoch') AS ts, agent
					FROM machine_usage
					WHERE machine_id = $1 AND ts >= (SELECT min(ts) FROM b) AND ts < (SELECT max(ts) FROM b) + interval '1 hour'
				), added AS (
					INSERT INTO machine_usage (machine_id, ts, step, model, launcher, agent, dir, input, cache_write, cache_read,
						output, requests)
					SELECT $1, ts, 3600, model, launcher, agent, dir, sum(input), sum(cache_write), sum(cache_read), sum(output),
						sum(requests)
					FROM b WHERE NOT EXISTS (SELECT 1 FROM had h WHERE h.ts = b.ts
						AND (h.agent = b.agent OR h.agent = '' AND b.agent = ANY($12::text[])))
					GROUP BY ts, model, launcher, agent, dir
					ON CONFLICT (machine_id, ts, step, model, launcher, agent, dir) DO NOTHING
					RETURNING ts, CASE WHEN agent = ANY($11::text[]) THEN '' ELSE agent END AS rec
				)
				SELECT rec, count(DISTINCT ts) FROM added GROUP BY rec`,
				machine, hours, models, launchers, names, in, cw, cr, out, req, usageFirstRecord, UsageMeteredBefore, dirs)
			if err != nil {
				return err
			}
			for rows.Next() {
				var rec string
				var c int
				if err := rows.Scan(&rec, &c); err != nil {
					rows.Close()
					return err
				}
				if _, ok := records[rec]; ok {
					records[rec] = c
				}
				stored += c
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		var th *time.Time
		if !through.IsZero() {
			th = &through
		}
		for rec, c := range records {
			if _, err := tx.Exec(ctx, `INSERT INTO usage_backfill (machine_id, agent, through, done_at, hours)
				VALUES ($1, $2, $3, CASE WHEN $4::boolean THEN now() END, $5)
				ON CONFLICT (machine_id, agent) DO UPDATE SET through = GREATEST(usage_backfill.through, excluded.through),
					done_at = COALESCE(usage_backfill.done_at, excluded.done_at), hours = usage_backfill.hours + excluded.hours,
					updated_at = now()`, machine, rec, th, done, c); err != nil {
				return err
			}
		}
		return nil
	})
	return stored, err
}
