package db

import (
	"context"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestMachineUsage(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	minute := func(ago time.Duration) int64 { return now.Add(-ago).Unix() }
	b := func(ago time.Duration, model, launcher string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: minute(ago), Model: model, Launcher: launcher, Input: 1, CacheWrite: 2, CacheRead: 3, Output: out, Requests: 1}
	}
	sum := func(rows []UsageRow) (out int64, req int) {
		for _, r := range rows {
			out += r.Output
			req += r.Requests
		}
		return
	}

	// An older node's rows (no launcher), then the new node's whole window: it replaces the window's minutes, so the
	// same replies are not there twice under two launchers.
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{b(time.Hour, "opus", "", 10), b(30*time.Hour, "opus", "", 100)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{b(time.Hour, "opus", "claude", 10), b(time.Hour, "gpt", "codex", 5)},
		now.Add(-26*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// What changed later replaces its minute's total.
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{b(time.Hour, "gpt", "codex", 7)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUsage(ctx, "m2", []proto.UsageBucket{b(time.Hour, "opus", "claude", 1000)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.UsageSeries(ctx, "m1", now.Add(-2*time.Hour), now, 60)
	if err != nil {
		t.Fatal(err)
	}
	if out, req := sum(rows); len(rows) != 2 || out != 17 || req != 2 {
		t.Fatalf("m1's last 2 hours = %+v", rows)
	}
	// The minute before the window (30 h ago) is kept: the node no longer has it.
	if rows, _ := d.UsageSeries(ctx, "m1", now.Add(-31*time.Hour), now.Add(-29*time.Hour), 60); len(rows) != 1 || rows[0].Output != 100 {
		t.Fatalf("before the window = %+v", rows)
	}

	// Rolled up into ten minutes past 27 hours: the 30-hour-old minute moves, the sums stay; a minute sent again
	// for a rolled-up time adds to its row.
	if n, err := d.RollupUsage(ctx, now.Add(-27*time.Hour)); err != nil || n != 1 {
		t.Fatalf("rollup = %d, %v", n, err)
	}
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{b(30*time.Hour-time.Minute, "opus", "", 1)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsage(ctx, now.Add(-27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, _ = d.UsageSeries(ctx, "m1", now.Add(-31*time.Hour), now.Add(-29*time.Hour), 600)
	if out, req := sum(rows); len(rows) != 1 || out != 101 || req != 2 {
		t.Fatalf("rolled up = %+v", rows)
	}
	// A week's chart by hour reads both kinds of rows.
	rows, _ = d.UsageSeries(ctx, "m1", now.Add(-7*24*time.Hour), now.Add(time.Minute), 3600)
	if out, _ := sum(rows); out != 118 {
		t.Fatalf("the week = %+v", rows)
	}

	if n, err := d.PruneUsage(ctx, now.Add(-28*time.Hour)); err != nil || n != 1 {
		t.Fatalf("prune = %d, %v", n, err)
	}
	if rows, _ := d.UsageSeries(ctx, "m2", now.Add(-2*time.Hour), now, 60); len(rows) != 1 || rows[0].Output != 1000 {
		t.Fatalf("m2 = %+v", rows)
	}
}

// Several nodes' writes in one transaction (PutUsages) come out as one at a time, in order: a whole window replaces
// the minutes an older write left, what changed after it replaces its minute. A write Postgres rejects costs only
// itself.
func TestPutUsagesOneTransaction(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	b := func(ago time.Duration, model, launcher string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: now.Add(-ago).Unix(), Model: model, Launcher: launcher, Output: out, Requests: 1}
	}
	writes := []UsageWrite{
		{Machine: "m1", Buckets: []proto.UsageBucket{b(time.Hour, "opus", "", 10), b(30*time.Hour, "opus", "", 100)}},
		{Machine: "m2", Buckets: []proto.UsageBucket{b(time.Hour, "opus", "claude", 1000)}},
		{Machine: "m1", Buckets: []proto.UsageBucket{b(time.Hour, "opus", "claude", 10), b(time.Hour, "gpt", "codex", 5)},
			ReplaceFrom: now.Add(-26 * time.Hour)},
		{Machine: "m1", Buckets: []proto.UsageBucket{b(time.Hour, "gpt", "codex", 7)}},
	}
	if rejected, err := d.PutUsages(ctx, writes); err != nil || rejected != nil {
		t.Fatal(rejected, err)
	}
	var rows map[string]int64
	read := func() {
		rows = map[string]int64{}
		for _, m := range []string{"m1", "m2"} {
			list, err := d.UsageSeries(ctx, m, now.Add(-31*time.Hour), now, 60)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range list {
				rows[fmt.Sprint(m, " ", (r.Minute-now.Unix())/3600, "h ", r.Model, " ", r.Launcher)] += r.Output
			}
		}
	}
	read()
	want := map[string]int64{"m1 -30h opus ": 100, "m1 -1h opus claude": 10, "m1 -1h gpt codex": 7, "m2 -1h opus claude": 1000}
	if !maps.Equal(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	var xmins int
	if err := d.pool.QueryRow(ctx, `SELECT count(DISTINCT xmin::text) FROM machine_usage`).Scan(&xmins); err != nil || xmins != 1 {
		t.Fatalf("%d transactions (%v), want one", xmins, err)
	}

	bad := proto.UsageBucket{Minute: 1 << 50, Model: "opus", Launcher: "claude", Output: 1, Requests: 1} // no such time
	rejected, err := d.PutUsages(ctx, []UsageWrite{
		{Machine: "m1", Buckets: []proto.UsageBucket{b(time.Hour, "gpt", "codex", 8)}},
		{Machine: "m2", Buckets: []proto.UsageBucket{b(time.Hour, "opus", "claude", 2000), bad}},
		{Machine: "m2", Buckets: []proto.UsageBucket{b(2*time.Hour, "opus", "claude", 3)}},
	})
	if err != nil || len(rejected) != 3 || rejected[0] != nil || rejected[1] == nil || rejected[2] != nil {
		t.Fatal(rejected, err)
	}
	read()
	want["m1 -1h gpt codex"], want["m2 -2h opus claude"] = 8, 3
	if !maps.Equal(rows, want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
}

// Past a week the ten-minute rows roll up into one row an hour; the calendar sums every machine's rows (or each
// machine's) into spans of time, each row in the span its start is in.
func TestUsageHistory(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	b := func(at time.Time, model string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: model, Launcher: "claude", Input: 1, CacheRead: 10, Output: out, Requests: 1}
	}
	old := now.Add(-8 * 24 * time.Hour)
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{b(old.Add(5*time.Minute), "opus", 1), b(old.Add(25*time.Minute), "opus", 2),
		b(old.Add(65*time.Minute), "opus", 4), b(now.Add(-time.Hour), "opus", 8)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUsage(ctx, "m2", []proto.UsageBucket{b(old.Add(30*time.Minute), "gpt", 16)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsage(ctx, now.Add(-27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsageHours(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var steps map[int]int
	if err := func() error {
		steps = map[int]int{}
		rows, err := d.pool.Query(ctx, `SELECT step, count(*) FROM machine_usage GROUP BY step`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var step, n int
			if err := rows.Scan(&step, &n); err != nil {
				return err
			}
			steps[step] = n
		}
		return rows.Err()
	}(); err != nil {
		t.Fatal(err)
	}
	// m1's first hour is one row, its second another, m2's hour one; the hour-old minute stays a minute.
	if want := map[int]int{UsageMinuteStep: 1, UsageHourStep: 3}; !maps.Equal(steps, want) {
		t.Fatalf("rows by step = %v, want %v", steps, want)
	}

	// Two spans: the old hour and its next, then up to now and a minute; every machine summed, then each apart.
	bounds := []time.Time{old, old.Add(time.Hour), old.Add(2 * time.Hour), now.Add(time.Minute)}
	spans, err := d.UsageSpans(ctx, bounds, UsageSum)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 3 || spans[0].Span != 0 || spans[0].Output != 19 || spans[0].Requests != 3 || spans[0].CacheRead != 30 ||
		spans[1].Span != 1 || spans[1].Output != 4 || spans[2].Span != 2 || spans[2].Output != 8 || spans[0].Machine != "" {
		t.Fatalf("spans = %+v", spans)
	}
	spans, err = d.UsageSpans(ctx, bounds[:2], UsageByMachine)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 2 || spans[0].Machine != "m1" || spans[0].Output != 3 || spans[1].Machine != "m2" || spans[1].Output != 16 {
		t.Fatalf("by machine = %+v", spans)
	}
	if spans, err := d.UsageSpans(ctx, bounds[:1], UsageSum); err != nil || spans != nil {
		t.Fatalf("one bound = %+v, %v", spans, err)
	}
}

// A backfill stores an hour only when the machine had no row in it (the live meter's count stays), adds nothing
// when sent again, and records how far it got.
func TestUsageBackfill(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	h1, h2, h3 := now.Add(-40*time.Hour), now.Add(-39*time.Hour), now.Add(-38*time.Hour)
	if err := d.PutUsage(ctx, "m1", []proto.UsageBucket{{Minute: h2.Add(10 * time.Minute).Unix(), Model: "opus", Output: 5, Requests: 1}}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if b, err := d.UsageBackfill(ctx, "m1", "claude"); err != nil || !b.Through.IsZero() || !b.Done.IsZero() || b.Hours != 0 {
		t.Fatalf("before = %+v, %v", b, err)
	}
	hour := func(at time.Time, model string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: model, Launcher: "claude", CacheRead: 100, Output: out, Requests: 1}
	}
	sent := []proto.UsageBucket{hour(h1, "opus", 1), hour(h1, "sonnet", 2), hour(h2, "opus", 40), hour(h3, "opus", 8)}
	stored, err := d.PutUsageBackfill(ctx, "m1", nil, sent, h3, false)
	if err != nil || stored != 2 { // h1 (both models) and h3; h2 had the live meter's minute
		t.Fatalf("stored %d, %v", stored, err)
	}
	// Sent again (a scan that was cut off before its record): nothing more.
	if stored, err = d.PutUsageBackfill(ctx, "m1", nil, sent, h3, false); err != nil || stored != 0 {
		t.Fatalf("again: stored %d, %v", stored, err)
	}
	spans, err := d.UsageSpans(ctx, []time.Time{h1, h2, h3, h3.Add(time.Hour)}, UsageSum)
	if err != nil || len(spans) != 3 || spans[0].Output != 3 || spans[1].Output != 5 || spans[2].Output != 8 || spans[0].Requests != 2 {
		t.Fatalf("spans = %+v, %v", spans, err)
	}
	// The last request: no hours, done. Through stays the latest, done is kept.
	if _, err := d.PutUsageBackfill(ctx, "m1", nil, nil, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	b, err := d.UsageBackfill(ctx, "m1", "")
	if err != nil || !b.Through.Equal(h3) || b.Done.IsZero() || b.Hours != 2 {
		t.Fatalf("record = %+v, %v", b, err)
	}
	if b, _ := d.UsageBackfill(ctx, "m2", ""); !b.Done.IsZero() {
		t.Fatalf("another machine's = %+v", b)
	}
}

// Usage by agent (#9984): a bucket of each agent is a row of its own, kept apart through the rollups and summed apart
// by UsageSpans; the backfill keeps a record per agent (Claude Code's the machine's first), and stores an agent's hour
// when the machine had none of that agent's usage in it, nor usage from before agents were kept apart if the agent
// was metered then (#10002).
func TestUsageByAgent(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	old := now.Add(-9 * 24 * time.Hour) // hours no other test here uses
	b := func(at time.Time, agent string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: "m", Launcher: "l", Agent: agent, CacheRead: 10, Output: out, Requests: 1}
	}
	if err := d.PutUsage(ctx, "agents", []proto.UsageBucket{b(old.Add(5*time.Minute), "claude", 1), b(old.Add(5*time.Minute), "codex", 2),
		b(old.Add(6*time.Minute), "claude", 4), b(old.Add(5*time.Minute), "", 8)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsage(ctx, now.Add(-27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsageHours(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// UsageSpans sums every machine: the other tests' rows are elsewhere in time.
	bounds := []time.Time{old, old.Add(time.Hour)}
	spans, err := d.UsageSpans(ctx, bounds, UsageByAgent)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 3 || spans[0].Agent != "" || spans[0].Output != 8 || spans[1].Agent != "claude" || spans[1].Output != 5 ||
		spans[1].Requests != 2 || spans[2].Agent != "codex" || spans[2].Output != 2 || spans[0].Machine != "" {
		t.Fatalf("by agent = %+v", spans)
	}

	// The backfill: pi's hour goes in beside the usage from before agents were kept apart, Claude Code's and Codex's
	// do not (that usage may hold theirs), and once pi's hour is there it is not stored again. h2 has Claude Code's
	// usage only (after the split): Codex's goes in beside it; h3 has none.
	h1, h2, h3 := old, old.Add(time.Hour), old.Add(2*time.Hour)
	if err := d.PutUsage(ctx, "agents", []proto.UsageBucket{b(h2.Add(time.Minute), "claude", 1)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	sent := []proto.UsageBucket{b(h1, "pi", 16), b(h1, "claude", 32), b(h1, "codex", 32), b(h2, "claude", 64), b(h2, "codex", 128), b(h3, "claude", 256)}
	stored, err := d.PutUsageBackfill(ctx, "agents", []string{"claude", "codex", "pi"}, sent, h3, false)
	if err != nil || stored != 3 { // pi's h1, Codex's h2, Claude Code's h3
		t.Fatalf("stored %d, %v", stored, err)
	}
	if stored, err = d.PutUsageBackfill(ctx, "agents", []string{"pi"}, sent[:1], h1, true); err != nil || stored != 0 {
		t.Fatalf("again: stored %d, %v", stored, err)
	}
	spans, err = d.UsageSpans(ctx, []time.Time{h1, h2, h3, h3.Add(time.Hour)}, UsageByAgent)
	if err != nil {
		t.Fatal(err)
	}
	// h1: "", claude, codex (the minutes') and pi; h2: claude, codex; h3: claude.
	if len(spans) != 7 || spans[3].Agent != "pi" || spans[3].Output != 16 || spans[5].Span != 1 || spans[5].Agent != "codex" ||
		spans[5].Output != 128 || spans[4].Output != 1 || spans[6].Span != 2 || spans[6].Output != 256 {
		t.Fatalf("after the backfill = %+v", spans)
	}
	first, err := d.UsageBackfill(ctx, "agents", "claude")
	if err != nil || first.Hours != 1 || !first.Through.Equal(h3) || !first.Done.IsZero() {
		t.Fatalf("Claude Code's (the first) record = %+v, %v", first, err)
	}
	codex, err := d.UsageBackfill(ctx, "agents", "codex")
	if err != nil || codex.Hours != 1 || !codex.Through.Equal(h3) {
		t.Fatalf("Codex's record = %+v, %v", codex, err)
	}
	pi, err := d.UsageBackfill(ctx, "agents", "pi")
	if err != nil || pi.Hours != 1 || !pi.Through.Equal(h3) || pi.Done.IsZero() {
		t.Fatalf("pi's record = %+v, %v", pi, err)
	}
	if UsageBackfillRecord("claude") != "" || UsageBackfillRecord("traex") != "traex" || UsageBackfillRecord("opencode") != "opencode" {
		t.Fatal("records")
	}
}

// Token usage by project (#16530): a bucket's directory is part of its key through the minute, ten-minute and hour
// rows and the backfill, the nodes' resolutions are kept (the latest of each), and UsageDirSpans sums by directory.
func TestUsageByDir(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().Truncate(time.Hour)
	old := now.Add(-12 * 24 * time.Hour) // hours no other test here uses
	b := func(at time.Time, dir string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: "m", Launcher: "l", Agent: "claude", Dir: dir, CacheRead: 10, Output: out, Requests: 1}
	}
	app := proto.UsageDir{Dir: "~/code/app", Project: "git:github.com/acme/app", Name: "acme/app", Host: "github.com", Place: "~/code/app"}
	// The same minute, model, launcher and agent in two directories, and one of before (no directory): three rows.
	if _, err := d.PutUsages(ctx, []UsageWrite{{Machine: "dirs", Dirs: []proto.UsageDir{app},
		Buckets: []proto.UsageBucket{b(old.Add(5*time.Minute), "~/code/app", 1), b(old.Add(5*time.Minute), "~/notes", 2), b(old.Add(5*time.Minute), "", 4)}}}); err != nil {
		t.Fatal(err)
	}
	// Sent again: a bucket replaces its own directory's row only.
	if err := d.PutUsage(ctx, "dirs", []proto.UsageBucket{b(old.Add(5*time.Minute), "~/code/app", 8)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsage(ctx, now.Add(-27*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RollupUsageHours(ctx, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	h1, h2 := old, old.Add(time.Hour)
	// The backfill keeps the directory too (h2 had nothing of the machine's).
	if stored, err := d.PutUsageBackfill(ctx, "dirs", []string{"claude"}, []proto.UsageBucket{b(h2, "~/notes", 16)}, h2, false); err != nil || stored != 1 {
		t.Fatalf("backfill stored %d, %v", stored, err)
	}
	spans, err := d.UsageDirSpans(ctx, []time.Time{h1, h2, h2.Add(time.Hour)}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(spans) != 4 || spans[0].Dir != "" || spans[0].Output != 4 || spans[1].Dir != "~/code/app" || spans[1].Output != 8 ||
		spans[2].Dir != "~/notes" || spans[2].Output != 2 || spans[3].Span != 1 || spans[3].Dir != "~/notes" || spans[3].Output != 16 ||
		spans[1].Machine != "dirs" || spans[1].Agent != "" {
		t.Fatalf("by dir = %+v", spans)
	}
	// Only some directories, by agent, model and launcher too.
	spans, err = d.UsageDirSpans(ctx, []time.Time{h1, h2.Add(time.Hour)}, true, []string{"~/notes"})
	if err != nil || len(spans) != 1 || spans[0].Output != 18 || spans[0].Agent != "claude" || spans[0].Model != "m" || spans[0].Launcher != "l" {
		t.Fatalf("one dir in detail = %+v, %v", spans, err)
	}
	// The calendar's sums are every directory's.
	if all, err := d.UsageSpans(ctx, []time.Time{h1, h2.Add(time.Hour)}, UsageSum); err != nil || len(all) != 1 || all[0].Output != 30 {
		t.Fatalf("all = %+v, %v", all, err)
	}

	// A later resolution replaces the earlier one: the latest of each directory is kept, per machine.
	moved := app
	moved.Project, moved.Name = "git:github.com/acme/app2", "acme/app2"
	if _, err := d.PutUsages(ctx, []UsageWrite{{Machine: "dirs", Dirs: []proto.UsageDir{moved}}, {Machine: "other", Dirs: []proto.UsageDir{app}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUsageDirs(ctx, "backfill", []proto.UsageDir{app}); err != nil { // a backfill's
		t.Fatal(err)
	}
	dirs, err := d.UsageDirs(ctx)
	if err != nil || len(dirs) != 3 || dirs[0].Machine != "backfill" || dirs[0].UsageDir != app || dirs[1].Machine != "dirs" ||
		dirs[1].Project != moved.Project || dirs[1].Name != "acme/app2" || dirs[2].Machine != "other" || dirs[2].UsageDir != app {
		t.Fatalf("dirs = %+v, %v", dirs, err)
	}

	// A runtime's deltas keep their directory as they add up.
	if err := d.AddUsage(ctx, "dirs", []proto.UsageBucket{{Minute: now.Add(-time.Hour).Unix(), Model: "m", Agent: "dsh", Dir: "~/a", Output: 1, Requests: 1},
		{Minute: now.Add(-time.Hour).Unix(), Model: "m", Agent: "dsh", Dir: "~/a", Output: 2, Requests: 1},
		{Minute: now.Add(-time.Hour).Unix(), Model: "m", Agent: "dsh", Dir: "~/b", Output: 4, Requests: 1}}); err != nil {
		t.Fatal(err)
	}
	recent, err := d.RecentRuntimeUsage(ctx, now.Add(-2*time.Hour))
	if err != nil || len(recent["dirs"]) != 2 {
		t.Fatalf("runtime = %+v, %v", recent, err)
	}
	for _, r := range recent["dirs"] {
		if r.Dir == "~/a" && r.Output != 3 || r.Dir == "~/b" && r.Output != 4 {
			t.Fatalf("runtime = %+v", recent)
		}
	}
}
