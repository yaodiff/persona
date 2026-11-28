package db

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The re-split's parts add up to what was stored, in the re-read shares, kind by kind (#16530).
func TestSplitUsage(t *testing.T) {
	stored := proto.UsageBucket{Input: 10, CacheWrite: 7, CacheRead: 1000, Output: 101, Requests: 3}
	read := []proto.UsageBucket{
		{Dir: "~/b", Input: 1, CacheRead: 300, Output: 50, Requests: 1},
		{Dir: "~/a", Input: 2, CacheRead: 600, Output: 50, Requests: 1},
		{Dir: "~/a", Requests: 1},
	}
	parts := splitUsage(stored, read)
	if len(parts) != 2 || parts[0].Dir != "~/a" || parts[1].Dir != "~/b" {
		t.Fatalf("parts %+v", parts)
	}
	var sum proto.UsageBucket
	for _, p := range parts {
		sum.Input, sum.CacheWrite, sum.CacheRead, sum.Output, sum.Requests = sum.Input+p.Input, sum.CacheWrite+p.CacheWrite,
			sum.CacheRead+p.CacheRead, sum.Output+p.Output, sum.Requests+p.Requests
	}
	if sum != stored {
		t.Fatalf("parts add up to %+v, stored %+v", sum, stored)
	}
	// Input 10 in 2:1 is 6.67 / 3.33: 7 and 3. Cache writes, which the re-read has none of, go by its tokens (652:351).
	// Output 101 in 1:1 is 50.5 each: the tie goes to the earlier directory. Requests 3 in 2:1.
	a, b := parts[0], parts[1]
	if a.Input != 7 || b.Input != 3 || a.CacheRead != 667 || b.CacheRead != 333 || a.Output != 51 || b.Output != 50 ||
		a.Requests != 2 || b.Requests != 1 || a.CacheWrite != 5 || b.CacheWrite != 2 {
		t.Fatalf("a %+v, b %+v", a, b)
	}
	if splitUsage(stored, []proto.UsageBucket{{Dir: "~/a", Requests: 1}}) != nil {
		t.Fatal("a re-read with no tokens split it")
	}
	// Numbers whose products overflow int64 still split exactly.
	huge := largestRemainder(9_000_000_000_000, []int64{4_000_000_000_000, 5_000_000_000_000, 1})
	if huge[0]+huge[1]+huge[2] != 9_000_000_000_000 || huge[0] != 4_000_000_000_000 {
		t.Fatalf("huge = %v", huge)
	}
}

// clearResplit drops machine's re-split record, which open does not clear: a test that reads it runs again on the
// same database (-count) as it ran the first time.
func clearResplit(t *testing.T, d *DB, machine string) {
	t.Helper()
	if _, err := d.pool.Exec(context.Background(), `DELETE FROM usage_resplit WHERE machine_id = $1`, machine); err != nil {
		t.Fatal(err)
	}
}

// A machine's usage stored without directories is split by what its node re-read, every key's sums (and so the
// calendar's) unchanged; a key with nothing re-read stays not split, and a second re-split changes nothing.
func TestResplitUsage(t *testing.T) {
	d, ctx := open(t), context.Background()
	clearResplit(t, d, "m")
	now := time.Now().Truncate(10 * time.Minute)
	slot := now.Add(-3 * 24 * time.Hour)
	b := func(at time.Time, dir, model string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: model, Launcher: "l", Agent: "claude", Dir: dir, Input: 4, CacheRead: 100, Output: out, Requests: 2}
	}
	// Stored before directories: ten minutes rolled up from two minutes, a minute row in the same ten minutes after
	// it, and another model's.
	if err := d.PutUsage(ctx, "m", []proto.UsageBucket{b(slot.Add(time.Minute), "", "opus", 30), b(slot.Add(2*time.Minute), "", "opus", 10),
		b(slot, "", "sonnet", 7)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if n, err := d.RollupUsage(ctx, slot.Add(10*time.Minute)); err != nil || n != 2 {
		t.Fatalf("rollup %d, %v", n, err)
	}
	if err := d.PutUsage(ctx, "m", []proto.UsageBucket{b(slot.Add(5*time.Minute), "", "opus", 20)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.UsageNotSplit(ctx, "m", "", slot, slot.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("not split = %v, %v", ok, err)
	}
	bounds := []time.Time{slot, slot.Add(time.Hour)}
	before, err := d.UsageSpans(ctx, bounds, UsageSum)
	if err != nil {
		t.Fatal(err)
	}
	read := []proto.UsageBucket{b(slot, "~/a", "opus", 30), b(slot, "~/b", "opus", 10), b(slot, "~/a", "haiku", 99)}
	moved, err := d.ResplitUsage(ctx, "m", "", read, slot, false)
	if err != nil || moved != 1 {
		t.Fatalf("moved %d, %v", moved, err)
	}
	after, err := d.UsageSpans(ctx, bounds, UsageSum)
	if err != nil || len(before) != 1 || len(after) != 1 || after[0] != before[0] {
		t.Fatalf("calendar before %+v, after %+v, %v", before, after, err)
	}
	spans, err := d.UsageDirSpans(ctx, bounds, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, s := range spans {
		got[s.Model+" "+s.Dir] = s.Output
	}
	// opus: 60 stored, re-read 30:10 → 45 and 15; sonnet's had nothing re-read.
	if len(got) != 3 || got["opus ~/a"] != 45 || got["opus ~/b"] != 15 || got["sonnet "] != 7 {
		t.Fatalf("by dir %v", got)
	}
	if moved, err := d.ResplitUsage(ctx, "m", "", read, slot, true); err != nil || moved != 0 {
		t.Fatalf("again: moved %d, %v", moved, err)
	}
	rec, err := d.UsageResplit(ctx, "m", "")
	if err != nil || rec.Moved != 1 || !rec.Through.Equal(slot) || rec.Done.IsZero() {
		t.Fatalf("record %+v, %v", rec, err)
	}
	if ok, _ := d.UsageNotSplit(ctx, "m", "", slot, slot.Add(time.Hour)); !ok { // sonnet's stays
		t.Fatal("sonnet's is split")
	}
}

// The temporary folders' pass (#16827): what a node stored under :temp is split among the temporary folders it
// re-read, together with what was stored already for them in the same ten minutes (a newer node naming them by path
// for part of them), every key's sums and the calendar unchanged; other directories' rows and the first pass's record
// are left alone, and a second run changes nothing.
func TestResplitUsageTemp(t *testing.T) {
	d, ctx := open(t), context.Background()
	const machine = "temp-pass"
	clearResplit(t, d, machine)
	now := time.Now().Truncate(10 * time.Minute)
	slot := now.Add(-4 * 24 * time.Hour)
	edge := slot.Add(10 * time.Minute)
	b := func(at time.Time, dir string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: "opus", Launcher: "l", Agent: "claude", Dir: dir, Input: 4, CacheRead: 100, Output: out, Requests: 2}
	}
	// An older node: its temporary folders under :temp, a folder of its own beside them. In the next ten minutes a
	// newer node took over, so the worktree has a row of its own beside :temp's.
	if err := d.PutUsage(ctx, machine, []proto.UsageBucket{b(slot.Add(time.Minute), proto.UsageDirTemp, 40), b(slot.Add(time.Minute), "~/notes", 5),
		b(edge.Add(time.Minute), proto.UsageDirTemp, 6), b(edge.Add(2*time.Minute), "/tmp/app-lane-x", 12)}, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.UsageNotSplit(ctx, machine, proto.UsageDirTemp, slot, slot.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("under :temp = %v, %v", ok, err)
	}
	if ok, err := d.UsageNotSplit(ctx, machine, "", slot, slot.Add(time.Hour)); err != nil || ok {
		t.Fatalf("not split = %v, %v", ok, err)
	}
	bounds := []time.Time{slot, slot.Add(time.Hour)}
	before, err := d.UsageSpans(ctx, bounds, UsageByMachine)
	if err != nil {
		t.Fatal(err)
	}
	read := []proto.UsageBucket{b(slot, "/tmp/app-lane-x", 30), b(slot, "/tmp/scratch", 10), b(edge, "/tmp/app-lane-x", 18)}
	if moved, err := d.ResplitUsage(ctx, machine, proto.UsageDirTemp, read, edge, true); err != nil || moved != 2 {
		t.Fatalf("moved %d, %v", moved, err)
	}
	after, err := d.UsageSpans(ctx, bounds, UsageByMachine)
	if err != nil || len(before) != len(after) {
		t.Fatalf("calendar before %+v, after %+v, %v", before, after, err)
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("calendar before %+v, after %+v", before, after)
		}
	}
	spans, err := d.UsageDirSpans(ctx, []time.Time{slot, edge, slot.Add(time.Hour)}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]proto.UsageBucket{}
	for _, s := range spans {
		if s.Machine == machine {
			got[strconv.Itoa(s.Span)+" "+s.Dir] = proto.UsageBucket{Input: s.Input, CacheRead: s.CacheRead, Output: s.Output, Requests: s.Requests}
		}
	}
	// The older node's ten minutes: :temp's 40 in the re-read's 30:10. The next: :temp's and the worktree's together,
	// all the worktree's. The folder's row is as it was.
	want := map[string]proto.UsageBucket{
		"0 /tmp/app-lane-x": {Input: 2, CacheRead: 50, Output: 30, Requests: 1},
		"0 /tmp/scratch":    {Input: 2, CacheRead: 50, Output: 10, Requests: 1},
		"0 ~/notes":         {Input: 4, CacheRead: 100, Output: 5, Requests: 2},
		"1 /tmp/app-lane-x": {Input: 8, CacheRead: 200, Output: 18, Requests: 4},
	}
	if len(got) != len(want) {
		t.Fatalf("by dir %+v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Fatalf("%s = %+v, want %+v (all %+v)", k, got[k], w, got)
		}
	}
	if moved, err := d.ResplitUsage(ctx, machine, proto.UsageDirTemp, read, edge, true); err != nil || moved != 0 {
		t.Fatalf("again: moved %d, %v", moved, err)
	}
	rec, err := d.UsageResplit(ctx, machine, proto.UsageDirTemp)
	if err != nil || rec.Moved != 2 || !rec.Through.Equal(edge) || rec.Done.IsZero() {
		t.Fatalf("record %+v, %v", rec, err)
	}
	if first, err := d.UsageResplit(ctx, machine, ""); err != nil || !first.Done.IsZero() || !first.Through.IsZero() || first.Moved != 0 {
		t.Fatalf("first pass's record %+v, %v", first, err)
	}
	if ok, _ := d.UsageNotSplit(ctx, machine, proto.UsageDirTemp, slot, slot.Add(time.Hour)); ok {
		t.Fatal(":temp is left")
	}
}

// A temporary folder's temp never replaces another resolution of its directory (a worktree removed since keeps its
// repository), while a repository replaces temp (#16827).
func TestUsageDirsTempKeepsRepository(t *testing.T) {
	d, ctx := open(t), context.Background()
	wt := proto.UsageDir{Dir: "/tmp/app-lane-x", Project: "git:github.com/acme/app", Name: "acme/app", Host: "github.com", Place: "/tmp/app-lane-x", Worktree: true}
	if err := d.PutUsageDirs(ctx, "m", []proto.UsageDir{wt, {Dir: "/tmp/scratch", Project: "temp"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PutUsages(ctx, []UsageWrite{{Machine: "m", Dirs: []proto.UsageDir{{Dir: wt.Dir, Project: "temp"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutUsageDirs(ctx, "m", []proto.UsageDir{{Dir: wt.Dir, Project: "temp"}, {Dir: "/tmp/scratch", Project: wt.Project,
		Name: wt.Name, Host: wt.Host, Place: "/tmp/scratch"}}); err != nil {
		t.Fatal(err)
	}
	dirs, err := d.UsageDirs(ctx)
	if err != nil || len(dirs) != 2 || dirs[0].UsageDir != wt || dirs[1].Dir != "/tmp/scratch" || dirs[1].Project != wt.Project {
		t.Fatalf("dirs = %+v, %v", dirs, err)
	}
}
