package db

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/larksyncwire"
)

func openSyncTables(t *testing.T) *DB {
	t.Helper()
	d := open(t)
	clear := func() {
		if _, err := d.pool.Exec(context.Background(), `TRUNCATE lark_sync_cursors, lark_sync_index, lark_sync_outbox RESTART IDENTITY`); err != nil {
			t.Error(err)
		}
	}
	clear()
	t.Cleanup(clear)
	return d
}

func larkRef(binding, entry string) larksyncwire.SourceRef {
	return larksyncwire.SourceRef{Channel: larksyncwire.ConnectorID, Binding: binding, Account: "feishu:t:" + binding, Entry: entry,
		SeenAt: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
}

// A run is committed at once: cursors upserted (others kept), index records merged, outbox rows appended; a commit
// that fails part way leaves all three as they were.
func TestLarkSyncCommitIsAtomic(t *testing.T) {
	d, ctx := openSyncTables(t), context.Background()
	doc := larksyncwire.KeyDoc + "dox1"
	if err := d.CommitLarkSyncRun(ctx, "b1", map[string]string{"drive": "10", "im:oc_1": "100"},
		[]larksyncwire.IndexRecord{{Key: doc, Kind: "doc", Revision: 2, BodyHash: "h2", Sources: []larksyncwire.SourceRef{larkRef("b1", "drive")}}},
		[]LarkSyncOutbox{{Kind: LarkSyncChat, CanonicalKey: "chat:oc_1", Payload: larksyncwire.OutboxPayload(`{"chat":{"id":"oc_1"}}`)},
			{Kind: LarkSyncDoc, CanonicalKey: doc, Payload: larksyncwire.OutboxPayload(`{"id":"dox1","revision":2}`)}}); err != nil {
		t.Fatal(err)
	}
	// A bad row in the middle: nothing of this commit stays.
	err := d.CommitLarkSyncRun(ctx, "b1", map[string]string{"drive": "20", "wiki:s": "5"},
		[]larksyncwire.IndexRecord{{Key: doc, Kind: "doc", Revision: 9, BodyHash: "h9"}},
		[]LarkSyncOutbox{{Kind: LarkSyncDoc, CanonicalKey: doc, Payload: larksyncwire.OutboxPayload(`{"revision":9}`)}, {Kind: "other", Payload: larksyncwire.OutboxPayload(`{}`)}})
	if err == nil {
		t.Fatal("a bad outbox row was committed")
	}
	cur, err := d.LarkSyncCursors(ctx, "b1")
	if err != nil || len(cur) != 2 || cur["drive"] != "10" || cur["im:oc_1"] != "100" {
		t.Fatalf("cursors: %v %v", cur, err)
	}
	idx, err := d.LarkSyncIndex(ctx, []string{doc, "absent"})
	if err != nil || len(idx) != 1 || idx[doc].Revision != 2 || idx[doc].BodyHash != "h2" || len(idx[doc].Sources) != 1 {
		t.Fatalf("index: %+v %v", idx, err)
	}
	rows, err := d.LarkSyncOutboxAfter(ctx, LarkSyncDoc, 0, 10)
	if err != nil || len(rows) != 1 || string(rows[0].Payload) != `{"id": "dox1", "revision": 2}` {
		t.Fatalf("outbox: %+v %v", rows, err)
	}

	// The next commit moves only the cursors it names.
	if err := d.CommitLarkSyncRun(ctx, "b1", map[string]string{"drive": "30"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if cur, _ := d.LarkSyncCursors(ctx, "b1"); len(cur) != 2 || cur["drive"] != "30" || cur["im:oc_1"] != "100" {
		t.Fatalf("cursors: %v", cur)
	}
	if other, _ := d.LarkSyncCursors(ctx, "b2"); len(other) != 0 {
		t.Fatalf("b2's cursors: %v", other)
	}
	if err := d.DeleteLarkSyncCursors(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := d.LarkSyncCursors(ctx, "b1"); len(cur) != 0 {
		t.Fatalf("deleted cursors: %v", cur)
	}
}

// Two bindings committing the same content in either order (and at once) end with the newest revision and both
// sources: the revision only moves forward.
func TestLarkSyncIndexMergesAcrossBindings(t *testing.T) {
	d, ctx := openSyncTables(t), context.Background()
	doc := larksyncwire.KeyDoc + "dox1"
	if err := d.PutLarkSyncIndex(ctx, []larksyncwire.IndexRecord{{Key: doc, Kind: "doc", Revision: 5, BodyHash: "h5",
		Sources: []larksyncwire.SourceRef{larkRef("b1", "drive")}}}); err != nil {
		t.Fatal(err)
	}
	// An older revision arrives late through another binding: only its source is added.
	if err := d.CommitLarkSyncRun(ctx, "b2", nil, []larksyncwire.IndexRecord{{Key: doc, Kind: "doc", Revision: 3, BodyHash: "h3",
		Sources: []larksyncwire.SourceRef{larkRef("b2", "wiki")}}}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := d.LarkSyncIndex(ctx, []string{doc})
	if r := got[doc]; r.Revision != 5 || r.BodyHash != "h5" || len(r.Sources) != 2 || r.Sources[1].Binding != "b2" || r.Updated.IsZero() {
		t.Fatalf("stale: %+v", r)
	}
	// Concurrent commits of newer revisions and the same source again.
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			binding := []string{"b1", "b2", "b3", "b4"}[i%4]
			errs <- d.CommitLarkSyncRun(ctx, binding, map[string]string{"drive": "x"}, []larksyncwire.IndexRecord{{Key: doc, Kind: "doc",
				Revision: int64(6 + i), BodyHash: "h", Sources: []larksyncwire.SourceRef{larkRef(binding, "drive")}}}, nil)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ = d.LarkSyncIndex(ctx, []string{doc})
	if r := got[doc]; r.Revision != 13 || len(r.Sources) != 5 {
		t.Fatalf("concurrent: %+v", r)
	}
	// The same revision with another body counts as newer (a platform that does not bump the revision).
	if err := d.PutLarkSyncIndex(ctx, []larksyncwire.IndexRecord{{Key: doc, Revision: 13, BodyHash: "other"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = d.LarkSyncIndex(ctx, []string{doc})
	if r := got[doc]; r.BodyHash != "other" || r.Kind != "doc" || len(r.Sources) != 5 {
		t.Fatalf("same revision, new body: %+v", r)
	}
}

// The connector reads the chat batches by sequence and the newest row of each document since a time; old rows are
// pruned.
func TestLarkSyncOutboxReads(t *testing.T) {
	d, ctx := openSyncTables(t), context.Background()
	for i, row := range []LarkSyncOutbox{
		{Kind: LarkSyncChat, CanonicalKey: "chat:oc_1", Payload: larksyncwire.OutboxPayload(`{"n":1}`)},
		{Kind: LarkSyncDoc, CanonicalKey: "feishu_doc:a", Payload: larksyncwire.OutboxPayload(`{"rev":1}`)},
		{Kind: LarkSyncChat, CanonicalKey: "chat:oc_2", Payload: larksyncwire.OutboxPayload(`{"n":2}`)},
		{Kind: LarkSyncDoc, CanonicalKey: "feishu_doc:a", Payload: larksyncwire.OutboxPayload(`{"rev":2}`)},
		{Kind: LarkSyncDoc, CanonicalKey: "feishu_doc:b", Payload: larksyncwire.OutboxPayload(`{"rev":1}`)},
		{Kind: LarkSyncChat, CanonicalKey: "chat:oc_1", Payload: larksyncwire.OutboxPayload(`{"n":3}`)},
	} {
		if err := d.CommitLarkSyncRun(ctx, "b1", map[string]string{"r": string(rune('0' + i))}, nil, []LarkSyncOutbox{row}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := d.LarkSyncOutboxAfter(ctx, LarkSyncChat, 0, 2)
	if err != nil || len(page) != 2 || string(page[0].Payload) != `{"n": 1}` || string(page[1].Payload) != `{"n": 2}` || page[0].Seq >= page[1].Seq {
		t.Fatalf("page 1: %+v %v", page, err)
	}
	page, err = d.LarkSyncOutboxAfter(ctx, LarkSyncChat, page[1].Seq, 2)
	if err != nil || len(page) != 1 || string(page[0].Payload) != `{"n": 3}` || page[0].CreatedAt.IsZero() {
		t.Fatalf("page 2: %+v %v", page, err)
	}
	docs, err := d.LarkSyncDocsSince(ctx, time.Now().Add(-time.Hour))
	if err != nil || len(docs) != 2 || docs[0].CanonicalKey != "feishu_doc:a" || string(docs[0].Payload) != `{"rev": 2}` || docs[1].CanonicalKey != "feishu_doc:b" {
		t.Fatalf("docs: %+v %v", docs, err)
	}
	if docs, _ := d.LarkSyncDocsSince(ctx, time.Now().Add(time.Hour)); len(docs) != 0 {
		t.Fatalf("docs from the future: %+v", docs)
	}
	if n, err := d.PruneLarkSyncOutbox(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("prune nothing: %d %v", n, err)
	}
	if n, err := d.PruneLarkSyncOutbox(ctx, time.Now().Add(time.Hour)); err != nil || n != 6 {
		t.Fatalf("prune all: %d %v", n, err)
	}
	if page, _ := d.LarkSyncOutboxAfter(ctx, LarkSyncChat, 0, 10); len(page) != 0 {
		t.Fatalf("after prune: %+v", page)
	}
}
