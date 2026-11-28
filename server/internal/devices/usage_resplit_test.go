package devices

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// usageResplitMemStore answers the re-split's record and records its requests.
type usageResplitMemStore struct {
	*memStore
	rec, tempRec           db.UsageResplitRecord // the first pass's, the temporary folders'
	notSplit, tempNotSplit bool
	from, to               time.Time // what UsageNotSplit was asked
	puts                   [][]proto.UsageBucket
	froms                  []string // the directory each ResplitUsage split
	through                []time.Time
	done                   []bool
	dirs                   []proto.UsageDir
}

func (s *usageResplitMemStore) UsageResplit(_ context.Context, _, from string) (db.UsageResplitRecord, error) {
	if from == proto.UsageDirTemp {
		return s.tempRec, nil
	}
	return s.rec, nil
}
func (s *usageResplitMemStore) UsageNotSplit(_ context.Context, _, dir string, from, to time.Time) (bool, error) {
	s.from, s.to = from, to
	if dir == proto.UsageDirTemp {
		return s.tempNotSplit, nil
	}
	return s.notSplit, nil
}
func (s *usageResplitMemStore) ResplitUsage(_ context.Context, _, from string, buckets []proto.UsageBucket, through time.Time, done bool) (int, error) {
	s.puts, s.through, s.done = append(s.puts, append([]proto.UsageBucket(nil), buckets...)), append(s.through, through), append(s.done, done)
	s.froms = append(s.froms, from)
	return len(buckets), nil
}
func (s *usageResplitMemStore) PutUsageDirs(_ context.Context, _ string, dirs []proto.UsageDir) error {
	s.dirs = append(s.dirs, dirs...)
	return nil
}

// The manager tells a node what to re-split: a week ago to where its live window starts, on from the ten minutes
// after the last recorded; nothing once done, when nothing there is not split (recorded done), or without a store.
func TestUsageResplitStatus(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 35, 0, 0, time.UTC)
	st := &usageResplitMemStore{memStore: newMemStore(), notSplit: true}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	status := func() map[string]any {
		t.Helper()
		out, err := d.usageResplitStatus(context.Background(), "a", json.RawMessage(`{}`), now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	since, before := time.Date(2026, 11, 20, 9, 40, 0, 0, time.UTC), time.Date(2026, 11, 26, 7, 30, 0, 0, time.UTC)
	if s := status(); s["done"] != false || s["since"] != since.Unix() || s["before"] != before.Unix() || !st.from.Equal(since) || !st.to.Equal(before) {
		t.Fatalf("first = %v (asked %v to %v)", s, st.from, st.to)
	}
	st.rec.Through = time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	if s := status(); s["since"] != st.rec.Through.Add(10*time.Minute).Unix() {
		t.Fatalf("cut off = %v", s)
	}
	st.notSplit = false
	if s := status(); s["done"] != true || len(st.done) != 1 || !st.done[0] {
		t.Fatalf("nothing to split = %v, recorded %v", s, st.done)
	}
	st.rec.Done = now
	if s := status(); s["done"] != true || len(st.done) != 1 {
		t.Fatalf("done = %v", s)
	}
	if out, _ := New(Options{Hub: &fakeHost{}, Store: newMemStore()}).usageResplitStatus(context.Background(), "a", nil, now); out["done"] != true {
		t.Fatalf("without a store = %v", out)
	}
}

// A request's resolutions are kept, and only its buckets inside the range a re-split may read now, with a directory,
// are split.
func TestUsageResplitPut(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 35, 0, 0, time.UTC)
	st := &usageResplitMemStore{memStore: newMemStore()}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	in := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC).Unix()
	params, _ := json.Marshal(map[string]any{"through": in, "done": true,
		"buckets": []proto.UsageBucket{
			{Minute: in, Model: "opus", Agent: "claude", Dir: "~/code/app", Output: 5},
			{Minute: in, Model: "opus", Agent: "claude", Dir: "", Output: 5},                                            // no directory
			{Minute: time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC).Unix(), Model: "opus", Agent: "claude", Dir: "~/x"}, // in the live window
			{Minute: time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC).Unix(), Model: "opus", Agent: "claude", Dir: "~/x"}, // over a week ago
		},
		"dirs": []proto.UsageDir{{Dir: "~/code/app", Project: "git:github.com/acme/app", Name: "acme/app", Host: "github.com"}}})
	out, err := d.putUsageResplit(context.Background(), "a", params, now)
	if err != nil || out["moved"] != 1 || len(st.puts) != 1 || len(st.puts[0]) != 1 || st.puts[0][0].Dir != "~/code/app" ||
		!st.done[0] || st.through[0].Unix() != in || len(st.dirs) != 1 {
		t.Fatalf("out %v, %v; puts %+v", out, err, st.puts)
	}
}

// The temporary folders' pass (#16827) is offered only to a node that says it names them, and only after the first;
// done at once when nothing is stored under :temp. Its requests keep only a directory's or :temp's buckets.
func TestUsageResplitTempPass(t *testing.T) {
	now := time.Date(2026, 11, 27, 9, 35, 0, 0, time.UTC)
	st := &usageResplitMemStore{memStore: newMemStore(), notSplit: true, tempNotSplit: true}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	status := func(params string) map[string]any {
		t.Helper()
		out, err := d.usageResplitStatus(context.Background(), "a", json.RawMessage(params), now)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if s := status(`{"temp_dirs":true}`); s["done"] != false || s["pass"] != nil { // the first pass first
		t.Fatalf("first pass pending = %v", s)
	}
	st.rec.Done = now
	if s := status(`{}`); s["done"] != true || len(st.done) != 0 { // an older node: its re-read still says :temp
		t.Fatalf("older node = %v", s)
	}
	since, before := time.Date(2026, 11, 20, 9, 40, 0, 0, time.UTC), time.Date(2026, 11, 26, 7, 30, 0, 0, time.UTC)
	if s := status(`{"temp_dirs":true}`); s["done"] != false || s["pass"] != "temp" || s["since"] != since.Unix() || s["before"] != before.Unix() {
		t.Fatalf("temporary folders = %v", s)
	}
	st.tempNotSplit = false
	if s := status(`{"temp_dirs":true}`); s["done"] != true || len(st.done) != 1 || !st.done[0] || st.froms[0] != proto.UsageDirTemp {
		t.Fatalf("nothing under :temp = %v, recorded %v %v", s, st.froms, st.done)
	}

	in := time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC).Unix()
	params, _ := json.Marshal(map[string]any{"pass": "temp", "through": in, "done": true,
		"buckets": []proto.UsageBucket{
			{Minute: in, Model: "opus", Agent: "claude", Dir: "/tmp/app-lane-x", Output: 5},
			{Minute: in, Model: "opus", Agent: "claude", Dir: proto.UsageDirTemp, Output: 1},
			{Minute: in, Model: "opus", Agent: "claude", Dir: proto.UsageDirAssistant, Output: 9}, // never a temporary folder's
		},
		"dirs": []proto.UsageDir{{Dir: "/tmp/app-lane-x", Project: "git:github.com/acme/app", Name: "acme/app", Host: "github.com",
			Place: "/tmp/app-lane-x", Worktree: true}, {Dir: "/tmp/scratch", Project: "temp"}}})
	out, err := d.putUsageResplit(context.Background(), "a", params, now)
	last := len(st.puts) - 1
	if err != nil || out["moved"] != 2 || st.froms[last] != proto.UsageDirTemp || len(st.puts[last]) != 2 ||
		st.puts[last][0].Dir != "/tmp/app-lane-x" || st.puts[last][1].Dir != proto.UsageDirTemp || len(st.dirs) != 2 {
		t.Fatalf("out %v, %v; puts %+v, dirs %+v", out, err, st.puts, st.dirs)
	}
	if _, err := d.putUsageResplit(context.Background(), "a", json.RawMessage(`{"pass":"other"}`), now); err == nil {
		t.Fatal("an unknown pass was taken")
	}
}
