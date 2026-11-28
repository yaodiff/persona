package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	rq "github.com/nwverse/persona/server/internal/resourcequeue"
)

func resourceFixture(t *testing.T) (*Service, *db.DB, rq.Definition) {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return database }})
	d := rq.Definition{ID: "ci.example", Capacity: 3, Mirror: rq.Mirror{Machine: "computer-a", Target: "runner.example.com"}}
	if err := database.PutSetting(t.Context(), resourceDefinitionsKey, []rq.Definition{d}); err != nil {
		t.Fatal(err)
	}
	return s, database, d
}
func TestResourceConcurrentAdmissionAndRestart(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := s.resourceRows(ctx, d, func(rows *[]rq.Lease, seq int64) (rq.Reply, error) {
				return rq.Acquire(rows, rq.Request{Resource: d.ID, Project: "example", Commit: fmt.Sprintf("%040x", i+1), Holder: rq.Holder{Machine: "computer-a", Session: fmt.Sprint(i)}}, time.Now(), fmt.Sprint(i), seq)
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	restarted := New(Options{Hub: newFakeHub(), Store: func() Store { return st }})
	status, err := restarted.resourceStatus(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	granted, queued := 0, 0
	for _, l := range status[0].Leases {
		if l.State == "granted" {
			granted++
		}
		if l.State == "queued" {
			queued++
		}
	}
	if granted != 3 || queued != 47 {
		t.Fatalf("after restart granted=%d queued=%d", granted, queued)
	}
}
func TestResourceNewJoinedCachedAndMirrorUnavailable(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	in := rq.Request{Action: "acquire", Resource: d.ID, Project: "example", Commit: strings.Repeat("a", 40), Groups: []string{"web"}, Holder: rq.Holder{Machine: "computer-a", Session: "one"}}
	first, err := s.resourceRequest(ctx, in)
	if err != nil || first.Status != "granted" {
		t.Fatalf("first: %+v %v", first, err)
	}
	in.Holder.Session = "two"
	joined, err := s.resourceRequest(ctx, in)
	if err != nil || joined.Status != "joined" || joined.Lease.ID != first.Lease.ID {
		t.Fatalf("joined: %+v %v", joined, err)
	}
	_, err = s.resourceRequest(ctx, rq.Request{Action: "release", Resource: d.ID, ID: first.Lease.ID, Token: first.Lease.Token, Holder: first.Lease.Holder, Outcome: &rq.Outcome{Exit: 0, Run: "example-run"}})
	if err != nil {
		t.Fatal(err)
	}
	cached, err := s.resourceRequest(ctx, in)
	if err != nil || cached.Status != "cached" {
		t.Fatalf("cached: %+v %v", cached, err)
	}
	previous := rq.Snapshot{ObservedAt: time.Now().Add(-time.Minute), Queue: &rq.Queue{V: 1, Slots: 3, Running: []rq.Job{}, Waiting: []rq.Job{}}, Unknown: false}
	if err := st.PutSetting(ctx, resourceMirrorKey(d.ID), previous); err != nil {
		t.Fatal(err)
	}
	s.resourceRefresh(context.Background())
	snap := s.resourceMirror(ctx, d.ID)
	if !snap.Unknown || snap.Queue == nil || !snap.ObservedAt.Equal(previous.ObservedAt) {
		t.Fatalf("stale snapshot lost: %+v", snap)
	}
}

func TestResourceExternalTicketsJoinAndOccupyCapacity(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	commit := strings.Repeat("b", 40)
	snap := rq.Snapshot{ObservedAt: time.Now().UTC(), Queue: &rq.Queue{V: 1, Slots: 3, Running: []rq.Job{{Ticket: "external-one", Slot: 1, Project: "example", Commit: commit, Groups: []string{"web"}}}, Waiting: []rq.Job{}}}
	if err := st.PutSetting(ctx, resourceMirrorKey(d.ID), snap); err != nil {
		t.Fatal(err)
	}
	joined, err := s.resourceRequest(ctx, rq.Request{Action: "acquire", Resource: d.ID, Project: "example", Commit: commit, Groups: []string{"web"}, Holder: rq.Holder{Machine: "computer-a", Session: "one"}})
	if err != nil || joined.Status != "joined" || !joined.Lease.External {
		t.Fatalf("external join: %+v %v", joined, err)
	}
	list, err := s.resourceStatus(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list[0].Leases) != 1 || list[0].Leases[0].State != "granted" {
		t.Fatal(list)
	}
}

func TestResourceFencingTokensFollowGrantOrderUnderPriority(t *testing.T) {
	s, _, d := resourceFixture(t)
	d.Capacity = 1
	ctx := t.Context()
	now := time.Now()
	_, rows, err := s.resourceRows(ctx, d, func(rows *[]rq.Lease, seq int64) (rq.Reply, error) {
		normal, err := rq.Acquire(rows, rq.Request{Resource: d.ID, Project: "example", Commit: strings.Repeat("a", 40), Holder: rq.Holder{Machine: "computer-a", Session: "normal"}}, now, "normal", seq)
		if err != nil {
			return rq.Reply{}, err
		}
		_ = normal
		_, err = rq.Acquire(rows, rq.Request{Resource: d.ID, Project: "example", Commit: strings.Repeat("b", 40), Priority: 1, Holder: rq.Holder{Machine: "computer-a", Session: "urgent"}}, now, "urgent", seq+1)
		return rq.Reply{}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	var urgent rq.Lease
	for _, l := range rows {
		if l.ID == "urgent" {
			urgent = l
		}
	}
	if urgent.State != "granted" {
		t.Fatal(rows)
	}
	_, rows, err = s.resourceRows(ctx, d, func(rows *[]rq.Lease, _ int64) (rq.Reply, error) {
		return rq.Mutate(*rows, rq.Request{Action: "release", ID: urgent.ID, Token: urgent.Token, Holder: urgent.Holder, Outcome: &rq.Outcome{Exit: 1, Run: "example-run"}}, time.Now())
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range rows {
		if l.ID == "normal" && (l.State != "granted" || l.Token <= urgent.Token) {
			t.Fatalf("later grant token=%d, prior=%d, state=%s", l.Token, urgent.Token, l.State)
		}
	}
}

func TestResourceUnchangedStatusDoesNotRewriteLeases(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	first, err := s.resourceRequest(ctx, rq.Request{Action: "acquire", Resource: d.ID, Project: "example", Commit: strings.Repeat("a", 40), Holder: rq.Holder{Machine: "computer-a", Session: "one"}})
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := st.QueryRow(ctx, `SELECT xmin::text FROM resource_leases WHERE id=$1`, first.Lease.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resourceStatus(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.QueryRow(ctx, `SELECT xmin::text FROM resource_leases WHERE id=$1`, first.Lease.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("unchanged status polling created another lease row version")
	}
}

func TestResourceUnknownMirrorDoesNotReserveStaleExternalSlotsForever(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	snapshot := rq.Snapshot{Unknown: true, ObservedAt: time.Now().Add(-2 * time.Minute), Queue: &rq.Queue{V: 1, Slots: 3, Running: []rq.Job{{Slot: 1}, {Slot: 2}, {Slot: 3}}, Waiting: []rq.Job{}}}
	if err := st.PutSetting(ctx, resourceMirrorKey(d.ID), snapshot); err != nil {
		t.Fatal(err)
	}
	first, err := s.resourceRequest(ctx, rq.Request{Action: "acquire", Resource: d.ID, Project: "example", Commit: strings.Repeat("a", 40), Holder: rq.Holder{Machine: "computer-a", Session: "one"}})
	if err != nil || first.Status != "granted" {
		t.Fatalf("stale occupancy blocked the external runner fallback: %+v %v", first, err)
	}
	retained := s.resourceMirror(ctx, d.ID)
	if !retained.Unknown || len(retained.Queue.Running) != 3 {
		t.Fatal("display history was lost during degraded admission")
	}
}

type resourceResultDevices struct {
	*fakeDevices
	queue  rq.Snapshot
	result *rq.Outcome
}

func (d *resourceResultDevices) CallNode(_ context.Context, _, method string, _ json.RawMessage) (json.RawMessage, error) {
	if method == "resource.queue" {
		return json.Marshal(d.queue)
	}
	if method == "resource.result" {
		return json.Marshal(d.result)
	}
	return nil, fmt.Errorf("unexpected node method %s", method)
}

func TestResourceExternalReconciliationWaitsForTheSameRun(t *testing.T) {
	s, st, d := resourceFixture(t)
	ctx := t.Context()
	queue := &rq.Queue{V: 1, Slots: 3, Running: []rq.Job{{Run: "current-run", Slot: 1, Project: "example", Commit: strings.Repeat("b", 40), Groups: []string{"web"}}}, Waiting: []rq.Job{}}
	if err := st.PutSetting(ctx, resourceMirrorKey(d.ID), rq.Snapshot{ObservedAt: time.Now().UTC(), Queue: queue}); err != nil {
		t.Fatal(err)
	}
	holder := rq.Holder{Machine: "computer-a", Session: "joining-session"}
	joined, err := s.resourceRequest(ctx, rq.Request{Action: "acquire", Resource: d.ID, Project: "example", Commit: strings.Repeat("b", 40), Groups: []string{"web"}, Holder: holder})
	if err != nil || joined.Status != "joined" {
		t.Fatalf("join: %+v %v", joined, err)
	}
	queue.Running = []rq.Job{}
	devs := &resourceResultDevices{fakeDevices: &fakeDevices{}, queue: rq.Snapshot{ObservedAt: time.Now().UTC(), Queue: queue}}
	s.devs = devs
	for _, outcome := range []*rq.Outcome{nil, {Run: "previous-run", Exit: 0, FinishedAt: time.Now().UTC()}, {Run: "current-run", Exit: 7, FinishedAt: time.Now().UTC()}} {
		devs.result = outcome
		s.resourceRefresh(ctx)
		reply, err := s.resourceRequest(ctx, rq.Request{Action: "get", Resource: d.ID, ID: joined.Lease.ID, Holder: holder})
		if err != nil {
			t.Fatal(err)
		}
		if outcome == nil || outcome.Run == "previous-run" {
			if reply.Status != "granted" || reply.Lease.Outcome != nil {
				t.Fatalf("settled from a missing or unrelated result: %+v", reply)
			}
		} else if reply.Status != "released" || reply.Lease.Outcome.Run != "current-run" || reply.Lease.Outcome.Exit != 7 {
			t.Fatalf("current run was not settled: %+v", reply)
		}
	}
}
