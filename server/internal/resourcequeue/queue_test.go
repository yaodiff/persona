package resourcequeue

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestV1QueueAndResult(t *testing.T) {
	raw, err := os.ReadFile("testdata/queue-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	q, err := ParseQueue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if q.Slots != 3 || len(q.Running) != 3 || len(q.Waiting) != 5 || q.Waiting[0].DuplicateOf != "t1" {
		t.Fatalf("bad queue: %+v", q)
	}
	for _, s := range []string{`{"v":2}`, `{"v":1,"slots":3}`, `{"v":1,"slots":3,"now":"bad","running":[],"waiting":[]}`} {
		if _, err := ParseQueue([]byte(s)); err == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	o, err := ParseResult([]byte(`{"v":1,"exit":0,"run":"example-run","finished_at":"2026-11-22T10:00:00Z"}`))
	if err != nil || o.Exit != 0 {
		t.Fatalf("result: %+v %v", o, err)
	}
	if _, err := ParseResult([]byte(`{"v":1,"run":"example-run","finished_at":"2026-11-22T10:00:00Z"}`)); err == nil {
		t.Fatal("missing exit interpreted as success")
	}
	if o, err := ParseResult([]byte(`null`)); err != nil || o != nil {
		t.Fatalf("missing result: %v %v", o, err)
	}
}
func TestNewJoinedCachedAndFencing(t *testing.T) {
	now := time.Now().UTC()
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Groups: []string{"web", "scripts", "web"}, Holder: Holder{Machine: "computer-a", Session: "one"}}
	first, err := Acquire(&rows, in, now, "one", 1)
	if err != nil || first.Status != "queued" {
		t.Fatalf("new: %+v %v", first, err)
	}
	Advance(rows, 1, 0, now)
	if rows[0].State != "granted" {
		t.Fatal(rows)
	}
	in.Groups = []string{"scripts", "web"}
	in.Holder.Session = "two"
	joined, err := Acquire(&rows, in, now, "two", 2)
	if err != nil || joined.Status != "joined" || len(rows) != 1 {
		t.Fatalf("join: %+v %v", joined, err)
	}
	if _, err := Mutate(rows, Request{Action: "release", ID: "one", Holder: in.Holder, Token: 1}, now); err == nil {
		t.Fatal("joiner released holder")
	}
	rep, err := Mutate(rows, Request{Action: "release", ID: "one", Holder: rows[0].Holder, Token: 1, Outcome: &Outcome{Exit: 0, Run: "example-run"}}, now)
	if err != nil || rep.Status != "released" {
		t.Fatalf("release: %+v %v", rep, err)
	}
	cached, err := Acquire(&rows, in, now, "three", 3)
	if err != nil || cached.Status != "cached" || cached.Lease.Outcome.Run != "example-run" {
		t.Fatalf("cache: %+v %v", cached, err)
	}
	if CacheOK(rows[0].Outcome, now.Add(13*time.Hour)) {
		t.Fatal("old cache accepted")
	}
}
func TestAdmissionLimitFIFOExpiryAndExternalSlots(t *testing.T) {
	now := time.Now()
	rows := []Lease{}
	for i := 0; i < 3; i++ {
		in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat(string(rune('a'+i)), 40), Holder: Holder{Machine: "computer-a", Session: "one"}}
		rep, err := Acquire(&rows, in, now, string(rune('a'+i)), int64(i+1))
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 && rep.Status != "refused" {
			t.Fatal(rep)
		}
	}
	Advance(rows, 3, 2, now)
	if rows[0].State != "granted" || rows[1].State != "queued" {
		t.Fatal(rows)
	}
	if _, err := Mutate(rows, Request{Action: "renew", ID: rows[1].ID, Token: 0, Holder: rows[1].Holder}, now.Add(60*time.Second)); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 3, 2, now.Add(90*time.Second))
	if rows[0].State != "expired" || rows[1].State != "granted" {
		t.Fatal(rows)
	}
	rep, _ := Mutate(rows, Request{Action: "renew", ID: rows[0].ID, Token: 1, Holder: rows[0].Holder}, now.Add(90*time.Second))
	if rep.Status != "fenced" {
		t.Fatal(rep)
	}
}
func TestRunnerUnavailableAndLegacyFallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := filepath.Join(dir, "ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 255\n"), 0700); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadQueue(context.Background(), Mirror{Target: "runner.example.com"})
	if err == nil || !snap.Unknown || !snap.ObservedAt.IsZero() {
		t.Fatalf("unreachable: %+v %v", snap, err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\ncase \"$*\" in *--json*) exit 64;; esac\necho 'running: ticket-1'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	snap, err = ReadQueue(context.Background(), Mirror{Target: "runner.example.com"})
	if err != nil || !snap.Unknown || snap.Queue != nil || !strings.Contains(snap.Raw, "ticket-1") {
		t.Fatalf("legacy: %+v %v", snap, err)
	}
}

func TestPriorityCancellationAndWaitTimeout(t *testing.T) {
	now := time.Now()
	rows := []Lease{
		{ID: "normal", State: "queued", Seq: 1, WaitUntil: now.Add(time.Hour), Holder: Holder{Machine: "computer-a", Session: "one"}},
		{ID: "urgent", State: "queued", Seq: 2, Priority: 1, WaitUntil: now.Add(time.Hour), Holder: Holder{Machine: "computer-a", Session: "two"}},
	}
	Advance(rows, 1, 0, now)
	if rows[0].State != "queued" || rows[1].State != "granted" {
		t.Fatal(rows)
	}
	cancelled, err := Mutate(rows, Request{Action: "cancel", ID: "normal", Holder: rows[0].Holder}, now)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel: %+v %v", cancelled, err)
	}
	rows = append(rows, Lease{ID: "late", State: "queued", Seq: 3, WaitUntil: now})
	Advance(rows, 1, 0, now)
	if rows[2].State != "timeout" {
		t.Fatal(rows)
	}
}

func TestV1LegacyEmptyFieldsRetainIndependentEntries(t *testing.T) {
	raw, err := os.ReadFile("testdata/queue-v1-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := ParseQueue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Running) != 3 || queue.Running[0].Ticket != "" || queue.Running[1].Commit != "" || queue.Running[1].Tag != "" {
		t.Fatal(queue)
	}
	if queue.Waiting[0].DuplicateOf != "example-build-one" {
		t.Fatal(queue.Waiting)
	}
	if queue.Running[1].DuplicateOf != "" || queue.Running[2].DuplicateOf != "" || queue.Waiting[1].DuplicateOf != "" {
		t.Fatal("empty commits were coalesced")
	}
	if queue.Running[1].Identity() == queue.Running[2].Identity() {
		t.Fatal("legacy entries lost their identity")
	}
	outcome, err := ParseResult([]byte(`{"v":1,"project":"example","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","groups":["web"],"exit":null,"run":"","finished_at":"","tag":""}`))
	if err != nil || outcome != nil {
		t.Fatalf("no-result response: %+v %v", outcome, err)
	}
}

func TestQueuedClientHeartbeatExpiresWithoutPromotingDeadWork(t *testing.T) {
	now := time.Now()
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Holder: Holder{Machine: "computer-a", Session: "dead"}}
	if _, err := Acquire(&rows, in, now, "dead", 1); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 1, 1, now)
	Advance(rows, 1, 0, now.Add(90*time.Second))
	if rows[0].State != "expired" {
		t.Fatalf("dead queued client was admitted: %+v", rows[0])
	}
}

func TestLegacyRunIdentitySurvivesAnEmptyRunningTicket(t *testing.T) {
	waiting := Job{Ticket: "000000000034", Run: "example-legacy-run", Position: 1}
	running := Job{Ticket: "", Run: "example-legacy-run", Slot: 1}
	if waiting.Identity() != running.Identity() {
		t.Fatal("legacy queue-to-slot transition lost its result identity")
	}
}

func TestActiveRunTakesPrecedenceOverPreviousCachedSuccess(t *testing.T) {
	now := time.Now().UTC()
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("a", 40), Holder: Holder{Machine: "computer-a", Session: "joining"}}
	key, err := Key(in.Project, in.Commit, in.Groups)
	if err != nil {
		t.Fatal(err)
	}
	rows := []Lease{
		{ID: "previous", Key: key, State: "released", Outcome: &Outcome{Run: "previous-run", Exit: 0, FinishedAt: now.Add(-time.Minute)}},
		{ID: "current", Key: key, State: "granted", Holder: Holder{Machine: "computer-a", Session: "holder"}},
	}
	reply, err := Acquire(&rows, in, now, "new", 3)
	if err != nil || reply.Status != "joined" || reply.Lease.ID != "current" || len(rows) != 2 {
		t.Fatalf("did not join the current run: %+v %v", reply, err)
	}
}

func TestAcquireRejectsReusedIDAfterTerminalLease(t *testing.T) {
	now := time.Now().UTC()
	holder := Holder{Machine: "computer-a", Session: "one"}
	commit := strings.Repeat("c", 40)

	for _, tc := range []struct {
		name      string
		wantState string
	}{
		{name: "cancelled", wantState: "cancelled"},
		{name: "expired", wantState: "expired"},
		{name: "timeout", wantState: "timeout"},
		{name: "failed-release", wantState: "released"},
		{name: "expired-cache", wantState: "released"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := []Lease{}
			in := Request{Resource: "ci.example", Project: "example", Commit: commit, ID: "attempt-1", Holder: holder}
			rep, err := Acquire(&rows, in, now, "fallback-1", 1)
			if err != nil || rep.Status != "queued" || rep.Lease.ID != "attempt-1" {
				t.Fatalf("initial acquire: %+v %v", rep, err)
			}
			// Idempotent retry while active must still return the active lease.
			again, err := Acquire(&rows, in, now, "fallback-active", 2)
			if err != nil || again.Status != "queued" || again.Lease.ID != "attempt-1" || len(rows) != 1 {
				t.Fatalf("active replay: %+v %v len=%d", again, err, len(rows))
			}

			retryAt := now
			switch tc.name {
			case "cancelled":
				if _, err := Mutate(rows, Request{Action: "cancel", ID: "attempt-1", Holder: holder}, now); err != nil {
					t.Fatal(err)
				}
			case "expired":
				Expire(rows, now.Add(2*time.Minute))
			case "timeout":
				// Keep heartbeat alive past WaitUntil so Expire enters timeout rather than expired.
				for d := time.Minute; d < 90*time.Minute; d += time.Minute {
					if r, err := Mutate(rows, Request{Action: "renew", ID: "attempt-1", Holder: holder}, now.Add(d)); err != nil || r.Status != "queued" {
						t.Fatalf("renew before timeout at %v: %+v %v", d, r, err)
					}
				}
				if r, err := Mutate(rows, Request{Action: "renew", ID: "attempt-1", Holder: holder}, now.Add(89*time.Minute+30*time.Second)); err != nil || r.Status != "queued" {
					t.Fatalf("final renew before timeout: %+v %v", r, err)
				}
				retryAt = now.Add(90 * time.Minute)
				Expire(rows, retryAt)
			case "failed-release":
				Advance(rows, 1, 0, now)
				if _, err := Mutate(rows, Request{Action: "release", ID: "attempt-1", Token: rows[0].Token, Holder: holder, Outcome: &Outcome{Exit: 1, Run: "failed-run"}}, now); err != nil {
					t.Fatal(err)
				}
			case "expired-cache":
				Advance(rows, 1, 0, now)
				if _, err := Mutate(rows, Request{Action: "release", ID: "attempt-1", Token: rows[0].Token, Holder: holder, Outcome: &Outcome{Exit: 0, Run: "old-ok-run"}}, now); err != nil {
					t.Fatal(err)
				}
				retryAt = now.Add(13 * time.Hour)
			}

			if rows[0].State != tc.wantState {
				t.Fatalf("state before retry = %q, want %q", rows[0].State, tc.wantState)
			}

			dup, err := Acquire(&rows, in, retryAt, "fallback-2", 3)
			if err == nil {
				t.Fatalf("reused ID after %s succeeded: %+v (rows=%d)", tc.name, dup, len(rows))
			}
			if len(rows) != 1 {
				t.Fatalf("duplicate row appended after %s: %+v", tc.name, rows)
			}

			// A fresh request ID for the same key after a terminal lease must still be admitted.
			freshReq := in
			freshReq.ID = "attempt-fresh"
			fresh, err := Acquire(&rows, freshReq, retryAt, "fallback-3", 4)
			if err != nil || fresh.Status != "queued" || fresh.Lease.ID != "attempt-fresh" || len(rows) != 2 {
				t.Fatalf("fresh acquire after %s: %+v %v len=%d", tc.name, fresh, err, len(rows))
			}
		})
	}

	// Idempotent retry after a successful cached release still returns the cached outcome.
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: commit, ID: "attempt-ok", Holder: holder}
	if _, err := Acquire(&rows, in, now, "fallback", 1); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 1, 0, now)
	if _, err := Mutate(rows, Request{Action: "release", ID: "attempt-ok", Token: rows[0].Token, Holder: holder, Outcome: &Outcome{Exit: 0, Run: "ok-run"}}, now); err != nil {
		t.Fatal(err)
	}
	cached, err := Acquire(&rows, in, now.Add(time.Minute), "fallback-2", 2)
	if err != nil || cached.Status != "cached" || cached.Lease.ID != "attempt-ok" || len(rows) != 1 {
		t.Fatalf("cached replay: %+v %v len=%d", cached, err, len(rows))
	}
}

func TestMutateEnforcesHoldUntilAndWaitUntilCaps(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	holder := Holder{Machine: "computer-a", Session: "one"}

	// Granted lease: heartbeat renewed just before HoldUntil must not allow renew or release at/after HoldUntil.
	rows := []Lease{}
	in := Request{Resource: "ci.example", Project: "example", Commit: strings.Repeat("d", 40), Holder: holder}
	if _, err := Acquire(&rows, in, t0, "held", 1); err != nil {
		t.Fatal(err)
	}
	Advance(rows, 1, 0, t0)
	if rows[0].State != "granted" {
		t.Fatalf("expected granted: %+v", rows[0])
	}
	// Renew heartbeats every minute up to 59m; at 59m30s HeartbeatUntil extends to 61m (past HoldUntil=60m).
	for d := time.Minute; d < time.Hour; d += time.Minute {
		if rep, err := Mutate(rows, Request{Action: "renew", ID: "held", Token: rows[0].Token, Holder: holder}, t0.Add(d)); err != nil || rep.Status != "granted" {
			t.Fatalf("renew at %v: %+v %v", d, rep, err)
		}
	}
	if rep, err := Mutate(rows, Request{Action: "renew", ID: "held", Token: rows[0].Token, Holder: holder}, t0.Add(59*time.Minute+30*time.Second)); err != nil || rep.Status != "granted" {
		t.Fatalf("renew before HoldUntil: %+v %v", rep, err)
	}
	// HeartbeatUntil is now t0+61m, past HoldUntil (t0+60m). At t0+60m both renew and release must be fenced.
	atHoldCap := t0.Add(time.Hour)
	heldHBBefore := rows[0].HeartbeatUntil
	rep, err := Mutate(rows, Request{Action: "renew", ID: "held", Token: rows[0].Token, Holder: holder}, atHoldCap)
	if err != nil || rep.Status != "fenced" {
		t.Fatalf("renew at HoldUntil = %+v, %v; want fenced", rep, err)
	}
	if !rows[0].HeartbeatUntil.Equal(heldHBBefore) {
		t.Fatalf("granted renew extended HeartbeatUntil past HoldUntil: %v -> %v", heldHBBefore, rows[0].HeartbeatUntil)
	}
	rep, err = Mutate(rows, Request{Action: "release", ID: "held", Token: rows[0].Token, Holder: holder, Outcome: &Outcome{Exit: 0, Run: "late-run"}}, atHoldCap)
	if err != nil || rep.Status != "fenced" {
		t.Fatalf("release at HoldUntil = %+v, %v; want fenced", rep, err)
	}
	if rows[0].Outcome != nil || rows[0].State == "released" {
		t.Fatalf("late release mutated lease after HoldUntil: %+v", rows[0])
	}

	// Queued lease: heartbeat renewed every minute up to WaitUntil must not allow renew or cancel at/after WaitUntil.
	qrows := []Lease{}
	if _, err := Acquire(&qrows, in, t0, "queued", 1); err != nil {
		t.Fatal(err)
	}
	for d := time.Minute; d < 90*time.Minute; d += time.Minute {
		if rep, err := Mutate(qrows, Request{Action: "renew", ID: "queued", Holder: holder}, t0.Add(d)); err != nil || rep.Status != "queued" {
			t.Fatalf("queued renew at %v: %+v %v", d, rep, err)
		}
	}
	if rep, err := Mutate(qrows, Request{Action: "renew", ID: "queued", Holder: holder}, t0.Add(89*time.Minute+30*time.Second)); err != nil || rep.Status != "queued" {
		t.Fatalf("queued renew before WaitUntil: %+v %v", rep, err)
	}
	hbBefore := qrows[0].HeartbeatUntil
	rep, err = Mutate(qrows, Request{Action: "renew", ID: "queued", Holder: holder}, t0.Add(90*time.Minute))
	if err != nil || rep.Status != "fenced" {
		t.Fatalf("queued renew at WaitUntil = %+v, %v; want fenced", rep, err)
	}
	if !qrows[0].HeartbeatUntil.Equal(hbBefore) {
		t.Fatalf("queued renew extended HeartbeatUntil past WaitUntil: %v -> %v", hbBefore, qrows[0].HeartbeatUntil)
	}
	rep, err = Mutate(qrows, Request{Action: "cancel", ID: "queued", Holder: holder}, t0.Add(90*time.Minute))
	if err != nil || rep.Status != "fenced" || qrows[0].State != "queued" {
		t.Fatalf("queued cancel at WaitUntil = %+v, %v, state=%q; want fenced and unchanged", rep, err, qrows[0].State)
	}

	// Queued lease whose heartbeat expired must also reject cancel with fenced without overwriting state.
	erows := []Lease{}
	if _, err := Acquire(&erows, in, t0, "queued-expired", 2); err != nil {
		t.Fatal(err)
	}
	rep, err = Mutate(erows, Request{Action: "cancel", ID: "queued-expired", Holder: holder}, t0.Add(90*time.Second))
	if err != nil || rep.Status != "fenced" || erows[0].State != "queued" {
		t.Fatalf("queued cancel after HeartbeatUntil = %+v, %v, state=%q; want fenced and unchanged", rep, err, erows[0].State)
	}
}
