package larksync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

func docRun(w *serviceWorld, rev int64, fail error) syncRunFn {
	return func(ctx context.Context, b larksyncwire.Binding, token func(context.Context) (string, error), _ map[string]string, idx larksyncwire.Index) (larksyncwire.Result, error) {
		if _, err := token(ctx); err != nil {
			return larksyncwire.Result{}, err
		}
		key := larksyncwire.KeyDoc + "dox1"
		if st, ok := idx.(interface {
			Stage([]larksyncwire.IndexRecord)
		}); ok {
			st.Stage([]larksyncwire.IndexRecord{{Key: key, Kind: "doc", Revision: rev}})
		}
		at := w.now().Add(-time.Minute)
		return larksyncwire.Result{
			Messages: []larksyncwire.Message{{ID: "om_" + string(rune('a'+rev)), ChatID: "oc_1", ChatName: "Team", ChatType: "group", Text: "hi", At: at}},
			Docs:     []larksyncwire.Doc{{Key: key, Kind: "docs", ObjToken: "dox1", Title: "Plan", Revision: rev, Body: "rev", At: at}},
			Cursors:  map[string]string{"drive": "c"},
		}, fail
	}
}

func TestLarkSyncFailedRunCommitsNothing(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, acc := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc", "ref", 2*time.Hour)

	// The reader read everything and then failed: nothing is committed.
	w.script(larksyncwire.PrimaryBot, acc, docRun(w, 1, &larksyncwire.Error{Kind: larksyncwire.ErrKindTransient, Op: "wiki", Msg: "502"}))
	if err := w.svc.runSync(ctx, key); err == nil {
		t.Fatal("the failed run succeeded")
	}
	if w.store.commits != 0 || len(w.store.outbox) != 0 || len(w.store.index) != 0 || len(w.store.cursorsOf(key)) != 0 || len(w.kicks()) != 0 {
		t.Fatalf("a failed run committed: %d commits, %v %v %v, kicks %v", w.store.commits, w.store.outbox, w.store.index, w.store.cursorsOf(key), w.kicks())
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthFailing || b.Failures != 1 {
		t.Fatalf("binding: %+v", b)
	}

	// The reader read everything and the hand-over failed: nothing either, and it counts as a failed run.
	w.script(larksyncwire.PrimaryBot, acc, docRun(w, 1, nil))
	w.store.commitErr = errors.New("database unavailable")
	if err := w.svc.runSync(ctx, key); err == nil {
		t.Fatal("the failed hand-over succeeded")
	}
	if len(w.store.outbox) != 0 || len(w.store.index) != 0 || len(w.store.cursorsOf(key)) != 0 {
		t.Fatalf("a failed hand-over committed: %v %v", w.store.outbox, w.store.index)
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthFailing || b.Failures != 2 || b.LastMessages != 0 || b.LastDocs != 0 {
		t.Fatalf("binding: %+v", b)
	}
	c := syncConnector{w.svc}
	if batches, next, _, err := c.Changes(ctx, "", 10); err != nil || len(batches) != 0 || next != "" {
		t.Fatalf("changes after failures: %v %q %v", batches, next, err)
	}

	// The same run again commits it all at once, and tells the knowledge base.
	w.store.commitErr = nil
	if err := w.svc.runSync(ctx, key); err != nil {
		t.Fatal(err)
	}
	if w.store.commits != 1 || len(w.store.outbox) != 2 || w.store.index[larksyncwire.KeyDoc+"dox1"].Revision != 1 || w.store.cursorsOf(key)["drive"] != "c" {
		t.Fatalf("committed: %d %v %v %v", w.store.commits, w.store.outbox, w.store.index, w.store.cursorsOf(key))
	}
	if kicks := w.kicks(); len(kicks) != 2 || !strings.Contains(kicks[0], larksyncwire.ConnectorID) {
		t.Fatalf("kicks: %v", kicks)
	}
	if b := w.binding(t, key); b.Health != larksyncwire.HealthOK || b.Failures != 0 || b.LastMessages != 1 || b.LastDocs != 1 {
		t.Fatalf("binding: %+v", b)
	}
}

func TestLarkSyncConnectorChangesAndItems(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	key, acc := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc", "ref", 30*24*time.Hour)
	c := syncConnector{w.svc}
	if st := c.Status(ctx); st.State != connectorwire.StateOK {
		t.Fatalf("status: %+v", st)
	}
	for rev := int64(1); rev <= 3; rev++ {
		w.script(larksyncwire.PrimaryBot, acc, docRun(w, rev, nil))
		if err := w.svc.runSync(ctx, key); err != nil {
			t.Fatal(err)
		}
		w.advance(time.Hour)
	}
	// Three chat batches, paged by sequence.
	b1, next, more, err := c.Changes(ctx, "", 2)
	if err != nil || len(b1) != 2 || !more || next == "" || b1[0].Chat.ID != "oc_1" || b1[0].Messages[0].Text != "hi" {
		t.Fatalf("page 1: %+v %q %v %v", b1, next, more, err)
	}
	b2, next2, more, err := c.Changes(ctx, next, 2)
	if err != nil || len(b2) != 1 || more || next2 == next {
		t.Fatalf("page 2: %+v %q %v %v", b2, next2, more, err)
	}
	if b3, next3, more, err := c.Changes(ctx, next2, 2); err != nil || len(b3) != 0 || more || next3 != next2 {
		t.Fatalf("page 3: %+v %q %v %v", b3, next3, more, err)
	}
	// One item per document: its newest revision.
	items, err := syncItems(c).Items(ctx)
	if err != nil || len(items) != 1 || items[0].Revision != 3 || items[0].ID != "dox1" || items[0].Kind != "docs" || items[0].Source != larksyncwire.ConnectorID {
		t.Fatalf("items: %+v %v", items, err)
	}
	// After a week the connector's items reach no further; after 30 days the outbox is pruned.
	w.advance(8 * 24 * time.Hour)
	if items, _ := syncItems(c).Items(ctx); len(items) != 0 {
		t.Fatalf("items of last week: %+v", items)
	}
	w.advance(23 * 24 * time.Hour)
	w.svc.tickOnce(ctx)
	if len(w.store.outbox) != 2 || !w.store.outbox[0].CreatedAt.Equal(w.now()) || !w.store.outbox[1].CreatedAt.Equal(w.now()) {
		t.Fatalf("pruned: %+v", w.store.outbox)
	}

	// An attachment is fetched as the binding that read it, with its token.
	m, err := c.ArchiveMedia(ctx, key+"|om_1|img_1|image")
	if err != nil || m.Mime != "image/png" || !strings.HasPrefix(string(m.Data), key+" ") {
		t.Fatalf("media: %+v %v", m, err)
	}
	if _, err := c.ArchiveMedia(ctx, "nobody~1|om_1|img_1|image"); err == nil {
		t.Fatal("an unknown binding's attachment was fetched")
	}
}

func TestLarkSyncConnectorStatus(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	c := syncConnector{w.svc}
	if st := c.Status(ctx); st.State != connectorwire.StateNotConfigured {
		t.Fatalf("no binding: %+v", st)
	}
	key, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_a", "acc", "ref", time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(key).Health = larksyncwire.HealthNeedsReauth
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if st := c.Status(ctx); st.State != connectorwire.StateNotSignedIn {
		t.Fatalf("needs reauth: %+v", st)
	}
	if m := c.Manifest(); len(m.Capabilities) != 3 {
		t.Fatalf("manifest: %+v", m)
	}
}
