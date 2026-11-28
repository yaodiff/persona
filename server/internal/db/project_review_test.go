package db

import (
	"encoding/json"
	"testing"
	"time"
)

// A blocking episode collapses later blocked observations until newer work closes it.
func TestProjectReviewEpisodeObservations(t *testing.T) {
	d, ctx := open(t), t.Context()
	// Sub-microsecond digits: the fleet and transcripts carry nanoseconds, Postgres keeps microseconds.
	at := time.Date(2026, 11, 20, 10, 0, 0, 123456789, time.UTC)
	event := func(kind string) json.RawMessage { return json.RawMessage(`{"kind":"` + kind + `"}`) }
	record := func(binding, id, kind, phase string, at time.Time) bool {
		t.Helper()
		queued, err := d.RecordProjectReview(ctx, id, "target", binding, event(kind), phase, at)
		if err != nil {
			t.Fatal(err)
		}
		return queued
	}

	t.Run("replayed work with nanoseconds keeps the episode", func(t *testing.T) {
		if !record("replay", "replay-1", "waiting_for_owner", "blocked", at) {
			t.Fatal("first blocked observation was not queued")
		}
		// The same row seen again after a restart, still with its nanoseconds.
		record("replay", "replay-work", "", "working", at)
		record("replay", "replay-work", "", "working_after_reconnect", at)
		if record("replay", "replay-2", "waiting_for_owner", "blocked", at.Add(time.Second)) {
			t.Fatal("a replayed observation closed the open episode")
		}
	})

	t.Run("newer work after a reconnect closes a stall", func(t *testing.T) {
		if !record("stall", "stall-1", "stalled", "blocked", at) {
			t.Fatal("first stall was not queued")
		}
		// The first row after the manager reconnected, unchanged since the stall.
		record("stall", "stall-same", "", "working_after_reconnect", at)
		if record("stall", "stall-2", "stalled", "blocked", at.Add(time.Second)) {
			t.Fatal("an unchanged row closed the stall")
		}
		// The agent moved while the manager was away: the first row it sends is newer.
		record("stall", "stall-moved", "", "working_after_reconnect", at.Add(2*time.Minute))
		if !record("stall", "stall-3", "stalled", "blocked", at.Add(3*time.Minute)) {
			t.Fatal("newer work after a reconnect did not close the stall: the next stall was collapsed into it")
		}
	})
}

// Session ids are only unique on their machine: the same id on two machines is two inputs.
func TestProjectReviewOwnerRelayKeyedBySource(t *testing.T) {
	d, ctx := open(t), t.Context()
	if err := d.SetProjectReviewAppInputs(ctx, "manager", true); err != nil {
		t.Fatal(err)
	}
	for _, machine := range []string{"box", "laptop"} {
		if err := d.RecordProjectReviewOwnerInput(ctx, ProjectReviewOwnerRelay{MessageID: "first-agent:s1", Manager: "manager",
			Machine: machine, Session: "s1"}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := d.PendingProjectReviewOwners(ctx)
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %+v, %v; want both machines' first inputs", pending, err)
	}
	if err := d.AckProjectReviewOwner(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	left, err := d.PendingProjectReviewOwners(ctx)
	if err != nil || len(left) != 1 || left[0].Machine == pending[0].Machine {
		t.Fatalf("after acking %s: pending = %+v, %v", pending[0].Machine, left, err)
	}
}

// The relay reads a batch of the managers connected now: another manager's rows are not read, and the batch is bounded.
func TestProjectReviewOwnerRelayReadsManagersBatch(t *testing.T) {
	d, ctx := open(t), t.Context()
	for _, manager := range []string{"here", "away"} {
		if err := d.SetProjectReviewAppInputs(ctx, manager, true); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"m1", "m2", "m3"} {
			if err := d.RecordProjectReviewOwnerInput(ctx, ProjectReviewOwnerRelay{MessageID: id, Manager: manager,
				Machine: "box", Session: manager}); err != nil {
				t.Fatal(err)
			}
		}
	}
	batch, err := d.PendingProjectReviewOwnersOf(ctx, []string{"here"}, 2)
	if err != nil || len(batch) != 2 || batch[0].Manager != "here" || batch[1].Manager != "here" || batch[0].MessageID != "m1" {
		t.Fatalf("batch = %+v, %v; want here's first two", batch, err)
	}
	if none, err := d.PendingProjectReviewOwnersOf(ctx, nil, 200); err != nil || len(none) != 0 {
		t.Fatalf("no managers read %+v, %v", none, err)
	}
}
