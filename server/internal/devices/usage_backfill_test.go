package devices

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// usageBackfillMemStore records the backfill's requests and answers its records (record: the legacy one, records:
// the others by agent).
type usageBackfillMemStore struct {
	*memStore
	record  db.UsageBackfill
	records map[string]db.UsageBackfill
	puts    []usageBackfillPut
}

type usageBackfillPut struct {
	agents  []string
	buckets []proto.UsageBucket
	through time.Time
	done    bool
}

func (s *usageBackfillMemStore) UsageBackfill(_ context.Context, _, agent string) (db.UsageBackfill, error) {
	if rec := db.UsageBackfillRecord(agent); rec != "" {
		return s.records[rec], nil
	}
	return s.record, nil
}

func (s *usageBackfillMemStore) PutUsageBackfill(_ context.Context, _ string, agents []string, buckets []proto.UsageBucket, through time.Time, done bool) (int, error) {
	s.puts = append(s.puts, usageBackfillPut{agents, append([]proto.UsageBucket(nil), buckets...), through, done})
	return len(buckets), nil
}

// The manager tells the node what to read: from where the history begins, or the hour after the last stored, to the
// hour the node's live window starts in; nothing once it is done, or without a store to keep it.
func TestUsageBackfillStatus(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	st := &usageBackfillMemStore{memStore: newMemStore()}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	status := func() map[string]any {
		t.Helper()
		out, err := d.usageBackfillStatus(context.Background(), "a", json.RawMessage(`{}`), now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := now.Add(-usageHistoryKeep).Truncate(time.Hour).Add(time.Hour)
	before := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC) // 26 hours back, the start of its hour
	if s := status(); s["done"] != false || s["since"] != first.Unix() || s["before"] != before.Unix() {
		t.Fatalf("a first scan = %v", s)
	}
	st.record.Through = time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)
	if s := status(); s["since"] != st.record.Through.Add(time.Hour).Unix() {
		t.Fatalf("after a scan cut off = %v", s)
	}
	st.record.Done = now
	if s := status(); s["done"] != true || s["since"] != nil {
		t.Fatalf("done = %v", s)
	}
	if s, _ := New(Options{Hub: &fakeHost{}, Store: newMemStore()}).usageBackfillStatus(context.Background(), "a", nil, now); s["done"] != true {
		t.Fatalf("no store = %v", s)
	}
}

// A request's hours outside what the manager asked for are not stored (the live window is the live meter's).
func TestPutUsageBackfill(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	st := &usageBackfillMemStore{memStore: newMemStore()}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	hour := func(at time.Time) proto.UsageBucket {
		return proto.UsageBucket{Minute: at.Unix(), Model: "opus", CacheRead: 10, Output: 1, Requests: 1}
	}
	params, _ := json.Marshal(map[string]any{"through": now.Add(-2 * time.Hour).Unix(), "done": true, "buckets": []proto.UsageBucket{
		hour(now.Add(-60 * 24 * time.Hour).Truncate(time.Hour)),
		hour(now.Add(-400 * 24 * time.Hour).Truncate(time.Hour)), // before the history
		hour(now.Add(-2 * time.Hour).Truncate(time.Hour)),        // the live window's
	}})
	out, err := d.putUsageBackfill(context.Background(), "a", params, now)
	if err != nil || out["stored"] != 1 {
		t.Fatalf("stored %v, %v", out, err)
	}
	if p := st.puts[0]; len(p.buckets) != 1 || !p.done || p.through.Unix() != now.Add(-2*time.Hour).Unix() {
		t.Fatalf("put = %+v", p)
	}
	if _, err := d.putUsageBackfill(context.Background(), "a", json.RawMessage(`{"buckets":`), now); err == nil {
		t.Fatal("a broken request was taken")
	}
}

// A node that names its agents gets each one's range: Claude Code's is the machine's first record (done stays done),
// any other agent's a record of its own, from the history's start or after its last hour stored.
func TestUsageBackfillStatusByAgent(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	st := &usageBackfillMemStore{memStore: newMemStore(), record: db.UsageBackfill{Done: now.Add(-time.Hour)},
		records: map[string]db.UsageBackfill{"omp": {Through: time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC)}}}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	out, err := d.usageBackfillStatus(context.Background(), "a", json.RawMessage(`{"agents":["claude","codex","pi","omp"]}`), now)
	if err != nil || out["done"] != true {
		t.Fatalf("status = %v, %v: its own fields are the first record's", out, err)
	}
	agents := out["agents"].(map[string]any)
	first := now.Add(-usageHistoryKeep).Truncate(time.Hour).Add(time.Hour)
	before := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC)
	for a, want := range map[string]map[string]any{
		"claude": {"done": true},
		"codex":  {"done": false, "since": first.Unix(), "before": before.Unix()}, // a record of its own since #10002
		"pi":     {"done": false, "since": first.Unix(), "before": before.Unix()},
		"omp":    {"done": false, "since": time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC).Unix(), "before": before.Unix()},
	} {
		if got := agents[a].(map[string]any); len(got) != len(want) || got["done"] != want["done"] || got["since"] != want["since"] || got["before"] != want["before"] {
			t.Errorf("%s = %v, want %v", a, got, want)
		}
	}
}

// A request that names its agents stores only their buckets, and says them to the store (their records move on).
func TestPutUsageBackfillByAgent(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	st := &usageBackfillMemStore{memStore: newMemStore()}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	at := now.Add(-60 * 24 * time.Hour).Truncate(time.Hour).Unix()
	params, _ := json.Marshal(map[string]any{"through": at, "agents": []string{"pi"}, "buckets": []proto.UsageBucket{
		{Minute: at, Model: "m", Agent: "pi", Output: 1, Requests: 1},
		{Minute: at, Model: "opus", Agent: "claude", Output: 2, Requests: 1}, // not what the request says it read
	}})
	if out, err := d.putUsageBackfill(context.Background(), "a", params, now); err != nil || out["stored"] != 1 {
		t.Fatalf("stored %v, %v", out, err)
	}
	if p := st.puts[0]; len(p.buckets) != 1 || p.buckets[0].Agent != "pi" || len(p.agents) != 1 || p.agents[0] != "pi" {
		t.Fatalf("put = %+v", p)
	}
}

// usageBackfillDirStore keeps the resolutions a backfill sends.
type usageBackfillDirStore struct {
	*usageBackfillMemStore
	dirs []proto.UsageDir
}

func (s *usageBackfillDirStore) PutUsageDirs(_ context.Context, _ string, dirs []proto.UsageDir) error {
	s.dirs = append(s.dirs, dirs...)
	return nil
}

// A backfill names the directories its buckets started in (#16530): the resolutions are kept, a refused one left out.
func TestUsageBackfillKeepsDirs(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	st := &usageBackfillDirStore{usageBackfillMemStore: &usageBackfillMemStore{memStore: newMemStore()}}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	hour := time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC).Unix()
	params, _ := json.Marshal(map[string]any{"through": hour, "agents": []string{"claude"},
		"buckets": []proto.UsageBucket{{Minute: hour, Model: "opus", Agent: "claude", Dir: "~/code/app", Output: 5, Requests: 1}},
		"dirs": []proto.UsageDir{
			{Dir: "~/code/app", Project: "git:github.com/acme/app", Name: "acme/app", Host: "github.com", Place: "~/code/app"},
			{Dir: "~/x", Project: "git:github.com/acme/app?t=1", Name: "acme/app", Host: "github.com"},
		}})
	out, err := d.putUsageBackfill(context.Background(), "a", params, now)
	if err != nil || out["stored"] != 1 || len(st.dirs) != 1 || st.dirs[0].Dir != "~/code/app" || st.puts[0].buckets[0].Dir != "~/code/app" {
		t.Fatalf("stored %v, %v; dirs %+v", out, err, st.dirs)
	}
}
