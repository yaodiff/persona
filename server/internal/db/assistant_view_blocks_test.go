package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

func TestAssistantInlineBlockProvenanceAndReplay(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	at := time.Now().UTC()
	conversation := "inline-db"
	b := AssistantViewBlockRow{Conversation: conversation, MachineID: "example-machine", ItemID: "item#0", Index: 1, Key: wire.AssistantBlockKey("item#0", 1), View: json.RawMessage(`{"fallback":"Original","sections":[]}`), CreatedAt: at}
	if _, err := d.RegisterAssistantViewBlock(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.View = json.RawMessage(`{"fallback":"Changed","sections":[]}`)
	if _, err := d.RegisterAssistantViewBlock(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := d.AssistantCard(ctx, conversation, b.Key)
	if err != nil || got.Version != 1 || !json.Valid(got.View) || string(got.View) == string(b.View) {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := d.AssistantCard(ctx, "other", b.Key); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("other card %v", err)
	}
	if _, err := d.AssistantView(ctx, conversation, b.Key); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("inline card confused with a live card: %v", err)
	}
	if _, err := d.UpdateAssistantView(ctx, conversation, b.Key, 1, b.View, at); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("inline card updated as live: %v", err)
	}
	keys, err := d.AssistantViewBlockKeys(ctx, conversation)
	if err != nil || len(keys) != 1 || keys[0] != b.Key {
		t.Fatalf("%v %v", keys, err)
	}
	b.Key = "client-forged"
	if _, err := d.RegisterAssistantViewBlock(ctx, b); err == nil {
		t.Fatal("forged key accepted")
	}
}

func TestAssistantInlineActionAndErrorFeedback(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	at := time.Now().UTC()
	conversation := "inline-action-db"
	b := AssistantViewBlockRow{Conversation: conversation, MachineID: "example-machine", ItemID: "item#0", Index: 0, Key: "item#0/0", View: json.RawMessage(`{"fallback":"Original","sections":[]}`), CreatedAt: at}
	if _, err := d.RegisterAssistantViewBlock(ctx, b); err != nil {
		t.Fatal(err)
	}
	a := AssistantViewActionRow{Conversation: conversation, Key: b.Key, Action: "yes", ClientMsgID: "client", MessageID: "message", MachineID: b.MachineID, Text: "Yes", At: at}
	if _, first, err := d.BeginAssistantViewAction(ctx, a, 1); err != nil || !first {
		t.Fatalf("inline selection %v %v", first, err)
	}
	a.MachineID = "other-machine"
	if _, _, err := d.BeginAssistantViewAction(ctx, a, 1); !errors.Is(err, ErrAssistantViewNotFound) {
		t.Fatalf("foreign machine selection %v", err)
	}
	problems := []string{"persona-view block 0: fallback missing"}
	if err := d.RecordAssistantViewErrors(ctx, conversation, b.MachineID, b.ItemID, problems, at); err != nil {
		t.Fatal(err)
	}
	if got, err := d.PeekAssistantViewErrors(ctx, "other", b.MachineID, 5); err != nil || len(got) != 0 {
		t.Fatalf("foreign feedback %v %v", got, err)
	}
	got, err := d.PeekAssistantViewErrors(ctx, conversation, b.MachineID, 5)
	if err != nil || len(got) != 1 || len(got[0].Errors) != 1 {
		t.Fatalf("feedback %v %v", got, err)
	}
	if n, err := d.AckAssistantViewErrors(ctx, conversation, b.MachineID, []string{got[0].ItemID}, []string{got[0].Digest}); err != nil || n != 1 {
		t.Fatalf("ack %d %v", n, err)
	}
	if err := d.RecordAssistantViewErrors(ctx, conversation, b.MachineID, b.ItemID, problems, at); err != nil {
		t.Fatal(err)
	}
	if got, err := d.PeekAssistantViewErrors(ctx, conversation, b.MachineID, 5); err != nil || len(got) != 0 {
		t.Fatalf("replayed feedback %v %v", got, err)
	}
	if err := d.RecordAssistantViewErrors(ctx, conversation, b.MachineID, "corrected", problems, at); err != nil {
		t.Fatal(err)
	}
	if err := d.RecordAssistantViewErrors(ctx, conversation, b.MachineID, "corrected", nil, at); err != nil {
		t.Fatal(err)
	}
	if got, err := d.PeekAssistantViewErrors(ctx, conversation, b.MachineID, 5); err != nil || len(got) != 0 {
		t.Fatalf("corrected feedback %v %v", got, err)
	}
}

// Reading view errors consumes nothing; only an ack of what was read does, and only for this conversation on this
// machine, and not once the errors changed since the read.
func TestAssistantViewErrorsPeekAndAck(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	const conv, machine = "errors-conv", "example-machine"
	at := time.Now().UTC().Add(-time.Hour)
	record := func(conversation, m, item string, errs []string, at time.Time) {
		t.Helper()
		if err := d.RecordAssistantViewErrors(ctx, conversation, m, item, errs, at); err != nil {
			t.Fatal(err)
		}
	}
	peek := func(conversation, m string) []AssistantViewErrorRow {
		t.Helper()
		got, err := d.PeekAssistantViewErrors(ctx, conversation, m, 5)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ack := func(conversation, m string, rows ...AssistantViewErrorRow) int64 {
		t.Helper()
		var items, digests []string
		for _, r := range rows {
			items, digests = append(items, r.ItemID), append(digests, r.Digest)
		}
		n, err := d.AckAssistantViewErrors(ctx, conversation, m, items, digests)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	record(conv, machine, "a", []string{"fallback: include a plain text summary"}, at)
	record(conv, machine, "b", []string{"close the persona-view code fence"}, at.Add(time.Minute))
	record(conv, "other-machine", "a", []string{"x"}, at)
	record("other-conv", machine, "a", []string{"x"}, at)

	// Repeated reads (a debug read, a message that never went out) see the same and change nothing.
	first := peek(conv, machine)
	if again := peek(conv, machine); len(first) != 2 || len(again) != 2 || first[0].ItemID != "a" || first[0].Digest != again[0].Digest {
		t.Fatalf("peek %+v %+v", first, again)
	}
	// An ack is scoped: another conversation or machine with the same receipts marks nothing.
	if n := ack("other-conv", "other-machine", first...); n != 0 {
		t.Fatalf("foreign ack marked %d", n)
	}
	if n := ack(conv, "other-machine", first...); n != 0 {
		t.Fatalf("other machine's ack marked %d", n)
	}
	// "b" changes after the read: the old receipt must not swallow the new errors.
	record(conv, machine, "b", []string{"pages.x: do not nest pages"}, at.Add(time.Minute))
	if n := ack(conv, machine, first...); n != 1 {
		t.Fatalf("ack marked %d, want only a", n)
	}
	left := peek(conv, machine)
	if len(left) != 1 || left[0].ItemID != "b" || left[0].Errors[0] != "pages.x: do not nest pages" {
		t.Fatalf("left %+v", left)
	}
	// The same ack again is a no-op; a forged digest marks nothing.
	if n := ack(conv, machine, first...); n != 0 {
		t.Fatalf("replayed ack marked %d", n)
	}
	forged := left[0]
	forged.Digest = "00"
	if n := ack(conv, machine, forged); n != 0 {
		t.Fatalf("forged ack marked %d", n)
	}
	// Other conversations and machines kept theirs.
	if len(peek(conv, "other-machine")) != 1 || len(peek("other-conv", machine)) != 1 {
		t.Fatal("foreign errors lost")
	}
	// A later reply whose views pass clears the earlier unreported ones; it leaves later ones alone.
	record(conv, machine, "c", []string{"later problem"}, at.Add(3*time.Minute))
	record(conv, machine, "ok", nil, at.Add(2*time.Minute))
	if got := peek(conv, machine); len(got) != 1 || got[0].ItemID != "c" {
		t.Fatalf("after a passing reply %+v", got)
	}
	// A passing reply without a time (replayed) clears only its own.
	record(conv, machine, "untimed", nil, time.Time{})
	if got := peek(conv, machine); len(got) != 1 || got[0].ItemID != "c" {
		t.Fatalf("an untimed passing reply cleared others %+v", got)
	}
	// Problems too old to matter are not read; empty scopes read nothing.
	record(conv, machine, "old", []string{"stale"}, time.Now().Add(-8*24*time.Hour))
	if got := peek(conv, machine); len(got) != 1 || got[0].ItemID != "c" {
		t.Fatalf("stale read %+v", got)
	}
	if got := peek("", machine); len(got) != 0 {
		t.Fatalf("empty conversation %+v", got)
	}
	// Recorded problems survive a new connection (a hub restart).
	d2 := connectTestDB(t)
	if got, err := d2.PeekAssistantViewErrors(ctx, conv, machine, 5); err != nil || len(got) != 1 || got[0].ItemID != "c" {
		t.Fatalf("after reopen %+v %v", got, err)
	}
}

// A passing reply redoes the replies written before it even when their problems are recorded after it: replies are
// registered concurrently and in any order. The mark that says so is in the database.
func TestAssistantViewErrorsPassMarkOrdersLateRecords(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	const machine = "example-machine"
	t1 := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	t2, t3 := t1.Add(time.Minute), t1.Add(2*time.Minute)
	peek := func(d *DB, conv string) []string {
		t.Helper()
		got, err := d.PeekAssistantViewErrors(ctx, conv, machine, 5)
		if err != nil {
			t.Fatal(err)
		}
		var items []string
		for _, r := range got {
			items = append(items, r.ItemID)
		}
		return items
	}
	record := func(d *DB, conv, item string, errs []string, at time.Time) {
		t.Helper()
		if err := d.RecordAssistantViewErrors(ctx, conv, machine, item, errs, at); err != nil {
			t.Fatal(err)
		}
	}
	// t2 passes first; t1's problems arrive late: already redone. t3's, written after the pass, are not.
	record(d, "late", "pass", nil, t2)
	record(d, "late", "t1", []string{"close the persona-view code fence"}, t1)
	record(d, "late", "t3", []string{"fallback: include a plain text summary"}, t3)
	if got := peek(d, "late"); len(got) != 1 || got[0] != "t3" {
		t.Fatalf("late record %v", got)
	}
	// A changed replay of t1 is still before the pass.
	record(d, "late", "t1", []string{"pages.x: do not nest pages"}, t1)
	if got := peek(d, "late"); len(got) != 1 || got[0] != "t3" {
		t.Fatalf("changed late record %v", got)
	}
	// An older pass does not move the mark back.
	record(d, "late", "old-pass", nil, t1.Add(-time.Hour))
	record(d, "late", "t1b", []string{"x"}, t1)
	if got := peek(d, "late"); len(got) != 1 || got[0] != "t3" {
		t.Fatalf("after an older pass %v", got)
	}
	// The mark outlives the connection (a hub restart).
	d2 := connectTestDB(t)
	record(d2, "late", "t1c", []string{"x"}, t1)
	if got := peek(d2, "late"); len(got) != 1 || got[0] != "t3" {
		t.Fatalf("after reopen %v", got)
	}
	// Another conversation or machine has its own mark.
	record(d, "other", "t1", []string{"x"}, t1)
	if got := peek(d, "other"); len(got) != 1 {
		t.Fatalf("foreign mark applied %v", got)
	}

	// Concurrently, in both orders: an earlier reply's problems never stay unreported after a later pass.
	for i := range 40 {
		conv := fmt.Sprintf("race-%d", i)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errs <- d.RecordAssistantViewErrors(ctx, conv, machine, "pass", nil, t2)
		}()
		go func() {
			defer wg.Done()
			errs <- d2.RecordAssistantViewErrors(ctx, conv, machine, "t1", []string{"x"}, t1)
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := peek(d, conv); len(got) != 0 {
			t.Fatalf("%s: %v left after a later pass", conv, got)
		}
	}
}

// A valid registration and a changed replay of the same reply's old problems, at the same time, must not deadlock:
// every writer locks the conversation's mark before any error row. The interleaving is forced: a third transaction
// holds the reply's error row until both writers are waiting, then lets go.
func TestAssistantViewErrorsSameItemValidAndInvalidDoNotDeadlock(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	const machine = "example-machine"
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	for i := range 6 {
		conv := fmt.Sprintf("same-item-%d", i)
		if err := d.RecordAssistantViewErrors(ctx, conv, machine, "item", []string{"fallback: include a plain text summary"}, at); err != nil {
			t.Fatal(err)
		}
		hold, err := d.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := hold.Exec(ctx, `SELECT 1 FROM assistant_view_errors WHERE conversation=$1 AND machine_id=$2 AND item_id='item' FOR UPDATE`, conv, machine); err != nil {
			t.Fatal(err)
		}
		waiting := func(n int) {
			t.Helper()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var got int
				if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'`).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got >= n {
					return
				}
				if time.Now().After(deadline) {
					t.Fatalf("%d writers waiting, want %d", got, n)
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		errs := make(chan error, 2)
		// The valid one first: it queues on the held row (or, locking the mark first, takes the mark and queues).
		go func() { errs <- d.RecordAssistantViewErrors(ctx, conv, machine, "item", nil, at) }()
		waiting(1)
		go func() {
			errs <- d.RecordAssistantViewErrors(ctx, conv, machine, "item", []string{"pages.x: do not nest pages"}, at)
		}()
		waiting(2)
		if err := hold.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			select {
			case err := <-errs:
				if err != nil {
					t.Fatalf("%s: %v", conv, err)
				}
			case <-time.After(15 * time.Second):
				t.Fatalf("%s: a writer never finished", conv)
			}
		}
		// Whichever ran second, the reply is redone: its own pass covers its replayed problems.
		got, err := d.PeekAssistantViewErrors(ctx, conv, machine, 5)
		if err != nil || len(got) != 0 {
			t.Fatalf("%s: %+v %v", conv, got, err)
		}
	}
	// Left to themselves, with acks of the same reply in between, over two pools: never an error.
	d2 := connectTestDB(t)
	for i := range 40 {
		conv := fmt.Sprintf("same-item-free-%d", i)
		if err := d.RecordAssistantViewErrors(ctx, conv, machine, "item", []string{"a"}, at); err != nil {
			t.Fatal(err)
		}
		rows, err := d.PeekAssistantViewErrors(ctx, conv, machine, 5)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 3)
		wg.Add(3)
		go func() { defer wg.Done(); errs <- d.RecordAssistantViewErrors(ctx, conv, machine, "item", nil, at) }()
		go func() {
			defer wg.Done()
			errs <- d2.RecordAssistantViewErrors(ctx, conv, machine, "item", []string{"b"}, at)
		}()
		go func() {
			defer wg.Done()
			_, err := d2.AckAssistantViewErrors(ctx, conv, machine, []string{"item"}, []string{rows[0].Digest})
			errs <- err
		}()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("%s: %v", conv, err)
			}
		}
		if got, err := d.PeekAssistantViewErrors(ctx, conv, machine, 5); err != nil || len(got) != 0 {
			t.Fatalf("%s: %+v %v", conv, got, err)
		}
	}
}
