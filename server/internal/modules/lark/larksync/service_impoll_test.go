package larksync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

func TestLarkSyncIMPollScheduleToggleAndMutualExclusion(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()

	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
	fullKey, fullAcc := w.adopt(t, "bsync", "ou_full", "acc-full", "ref-full", 24*time.Hour)
	docsKey, _ := w.adopt(t, larksyncwire.PrimaryBot, "ou_docs", "acc-docs", "ref-docs", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(fullKey).Tier = larksyncwire.TierFull
		st.binding(docsKey).Tier = larksyncwire.TierDocs
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var modes []string
	var nextRes larksyncwire.Result
	w.script("bsync", fullAcc, func(_ context.Context, b larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		mu.Lock()
		modes = append(modes, b.Mode)
		res := nextRes
		nextRes = larksyncwire.Result{}
		mu.Unlock()
		return res, nil
	})

	code, view := w.do(t, "GET", "/api/v1/bots/feishu/sync", "")
	if code != 200 || view["im_poll"] != true {
		t.Fatalf("initial view = %d %v, want im_poll=true", code, view)
	}

	w.svc.tickOnce(ctx)
	fullNextDaily := w.binding(t, fullKey).NextRunAt
	if w.readers != 2 || !slices.Equal(modes, []string{larksyncwire.ModeFull}) {
		t.Fatalf("after first tick: readers=%d modes=%v", w.readers, modes)
	}

	w.advance(10 * time.Minute)
	w.svc.tickOnce(ctx)
	if len(modes) != 1 {
		t.Fatalf("ran early at 10m: modes=%v", modes)
	}

	w.advance(5 * time.Minute)
	mu.Lock()
	nextRes = larksyncwire.Result{
		Messages: []larksyncwire.Message{{ID: "om_inc", ChatID: "oc_1", ChatType: "p2p", Text: "hi", At: w.now()}},
		Docs: []larksyncwire.Doc{{
			Key:      larksyncwire.KeyDoc + "doxNoPerm",
			Kind:     "docs",
			ObjToken: "doxNoPerm",
			ObjType:  "docx",
			Title:    "https://my.feishu.cn/docx/doxNoPerm",
			URL:      "https://my.feishu.cn/docx/doxNoPerm",
			Note:     "unreadable: docx document (forbidden, code 1770032)",
			Revision: 0,
		}},
		Cursors: map[string]string{"im:oc_1": "1700000001000"},
	}
	mu.Unlock()
	docsBefore := countOf(w.ran, docsKey)
	w.svc.tickOnce(ctx)
	if !slices.Equal(modes, []string{larksyncwire.ModeFull, larksyncwire.ModeIM}) {
		t.Fatalf("at 15m: modes=%v, want [full, im]", modes)
	}
	if countOf(w.ran, docsKey) != docsBefore {
		t.Fatalf("docs_standard binding ran during IM poll")
	}
	if w.readers != 2 {
		t.Fatalf("IM poll created a second reader (%d), must reuse the binding's reader/limiter", w.readers)
	}
	fb := w.binding(t, fullKey)
	if !fb.NextRunAt.Equal(fullNextDaily) {
		t.Fatalf("IM poll moved daily NextRunAt: got %v, want %v", fb.NextRunAt, fullNextDaily)
	}
	if !fb.NextIMRunAt.Equal(w.now().Add(imEvery)) {
		t.Fatalf("NextIMRunAt = %v, want %v", fb.NextIMRunAt, w.now().Add(imEvery))
	}
	items, err := syncItems(syncConnector{w.svc}).Items(ctx)
	if err != nil || len(items) != 1 || items[0].Note != "unreadable: docx document (forbidden, code 1770032)" {
		t.Fatalf("outbox items = %+v, err = %v", items, err)
	}

	w.advance(imEvery)
	mu.Lock()
	nextRes = larksyncwire.Result{
		Errors: []larksyncwire.ResourceError{{Resource: "im:oc_1", Kind: larksyncwire.ErrKindRateLimited, Detail: "429"}},
	}
	mu.Unlock()
	w.svc.tickOnce(ctx)
	if fb := w.binding(t, fullKey); fb.Health != larksyncwire.HealthRateLimited || !fb.NextIMRunAt.Equal(w.now().Add(partialNext)) {
		t.Fatalf("rate-limited IM poll: health=%s nextIM=%v", fb.Health, fb.NextIMRunAt)
	}

	code, view = w.do(t, "PUT", "/api/v1/bots/feishu/sync", `{"im_poll":false}`)
	if code != 200 || view["im_poll"] != false {
		t.Fatalf("disable im_poll = %d %v", code, view)
	}
	w.advance(2 * time.Hour)
	beforeOff := len(modes)
	w.svc.tickOnce(ctx)
	if len(modes) != beforeOff {
		t.Fatalf("IM poll ran while im_poll=false: modes=%v", modes)
	}

	w.advance(w.binding(t, fullKey).NextRunAt.Sub(w.now()))
	w.svc.tickOnce(ctx)
	if len(modes) != beforeOff+1 || modes[len(modes)-1] != larksyncwire.ModeFull {
		t.Fatalf("daily full run with im_poll=false: modes=%v", modes)
	}

	code, view = w.do(t, "PUT", "/api/v1/bots/feishu/sync", `{"im_poll":true}`)
	if code != 200 || view["im_poll"] != true {
		t.Fatalf("re-enable im_poll = %d %v", code, view)
	}
	w.advance(imEvery)
	w.svc.tickOnce(ctx)
	if modes[len(modes)-1] != larksyncwire.ModeIM {
		t.Fatalf("IM poll did not resume after re-enabling: modes=%v", modes)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	w.script("bsync", fullAcc, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		close(started)
		<-release
		return larksyncwire.Result{}, nil
	})
	done := make(chan error, 1)
	go func() { done <- w.svc.runMode(ctx, fullKey, larksyncwire.ModeFull) }()
	<-started
	if err := w.svc.runMode(ctx, fullKey, larksyncwire.ModeIM); !errors.Is(err, errRunning) {
		close(release)
		t.Fatalf("concurrent ModeIM during ModeFull returned %v, want errRunning", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("held ModeFull failed: %v", err)
	}
}

func TestLarkSyncIMPollPartialWithProgressKeeps15MinuteCadence(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()

	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
	fullKey, fullAcc := w.adopt(t, "bsync", "ou_full", "acc-full", "ref-full", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(fullKey).Tier = larksyncwire.TierFull
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var nextRes larksyncwire.Result
	w.script("bsync", fullAcc, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		mu.Lock()
		res := nextRes
		nextRes = larksyncwire.Result{}
		mu.Unlock()
		return res, nil
	})

	mu.Lock()
	nextRes = larksyncwire.Result{Cursors: map[string]string{"im:oc_big": "1000"}}
	mu.Unlock()
	w.svc.tickOnce(ctx)

	w.advance(imEvery)
	mu.Lock()
	nextRes = larksyncwire.Result{
		Partial: true,
		Cursors: map[string]string{"im:oc_big": "2000"},
	}
	mu.Unlock()
	w.svc.tickOnce(ctx)
	if fb := w.binding(t, fullKey); !fb.NextIMRunAt.Equal(w.now().Add(imEvery)) {
		t.Fatalf("partial ModeIM with cursor progress: NextIMRunAt=%v, want +15m (%v)", fb.NextIMRunAt, w.now().Add(imEvery))
	}

	w.advance(imEvery)
	mu.Lock()
	nextRes = larksyncwire.Result{
		Partial: true,
		Cursors: map[string]string{"im:oc_big": "2000"},
	}
	mu.Unlock()
	w.svc.tickOnce(ctx)
	if fb := w.binding(t, fullKey); !fb.NextIMRunAt.Equal(w.now().Add(partialNext)) {
		t.Fatalf("partial ModeIM with zero progress: NextIMRunAt=%v, want +1h (%v)", fb.NextIMRunAt, w.now().Add(partialNext))
	}
}

func TestLarkSyncBackfillSenderNamesIdempotentAndScoped(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()

	w.addSyncBot(t, "bsyncA", "cli_a", larksyncwire.TierFull)
	w.addSyncBot(t, "bsyncB", "cli_b", larksyncwire.TierFull)
	keyA, accA := w.adopt(t, "bsyncA", "ou_owner_a", "acc-a", "ref-a", 24*time.Hour)
	keyB, _ := w.adopt(t, "bsyncB", "ou_owner_b", "acc-b", "ref-b", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(keyA).Tier = larksyncwire.TierFull
		st.binding(keyB).Tier = larksyncwire.TierFull
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	seedBatch := func(cb connectorwire.ChatBatch) larksyncwire.OutboxRow {
		raw, err := json.Marshal(cb)
		if err != nil {
			t.Fatal(err)
		}
		return larksyncwire.OutboxRow{Kind: larksyncwire.OutboxChat, CanonicalKey: "chat:" + cb.Chat.ID, Payload: raw}
	}
	at := w.now().Add(-time.Hour)
	if err := w.store.CommitLarkSyncRun(ctx, keyA, nil, []larksyncwire.IndexRecord{
		{Key: larksyncwire.KeyMessage + "om_a1", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyA, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_bot", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyA, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_named", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyA, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_b1", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyB, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_dm1", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyA, Entry: larksyncwire.EntryChat}}},
	}, []larksyncwire.OutboxRow{
		seedBatch(connectorwire.ChatBatch{
			Chat: kbwire.SourceChat{ID: "oc_grp", Name: "Example Group", Kind: "group"},
			Messages: []kbwire.SourceMessage{
				{ID: "om_a1", SenderID: "ou_alice", SenderName: "", Text: "hello group", At: at},
				{ID: "om_bot", SenderID: "cli_bot", SenderName: "", Text: "bot notice", At: at.Add(time.Second)},
				{ID: "om_named", SenderID: "ou_named", SenderName: "Already Named", Text: "named", At: at.Add(2 * time.Second)},
				{ID: "om_b1", SenderID: "ou_other_tenant", SenderName: "", Text: "other binding", At: at.Add(3 * time.Second)},
			},
		}),
		seedBatch(connectorwire.ChatBatch{
			Chat: kbwire.SourceChat{ID: "oc_dm", Name: "", Kind: "dm"},
			Messages: []kbwire.SourceMessage{
				{ID: "om_dm1", SenderID: "ou_bob", SenderName: "", Text: "hi dm", At: at.Add(4 * time.Second)},
			},
		}),
	}); err != nil {
		t.Fatal(err)
	}

	var resolveCalls atomic.Int32
	var lastRequested []string
	w.mu.Lock()
	w.resolveNames = func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), openIDs []string) (map[string]string, error) {
		resolveCalls.Add(1)
		lastRequested = slices.Clone(openIDs)
		return map[string]string{
			"ou_alice": "Example Alice",
			"ou_bob":   "Example Bob",
		}, nil
	}
	w.mu.Unlock()
	w.script("bsyncA", accA, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		return larksyncwire.Result{}, nil
	})

	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("resolveCalls = %d, want 1", resolveCalls.Load())
	}
	if !slices.Equal(lastRequested, []string{"ou_alice", "ou_bob"}) {
		t.Fatalf("resolved openIDs = %v, want [ou_alice ou_bob]", lastRequested)
	}
	if got := w.binding(t, keyA).BackfillVersion; got != backfillVersion {
		t.Fatalf("BackfillVersion = %d, want %d", got, backfillVersion)
	}
	rows, err := w.store.LarkSyncOutboxAfter(ctx, larksyncwire.OutboxChat, 2, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("repair outbox rows = %d, err = %v", len(rows), err)
	}
	repaired := map[string]connectorwire.ChatBatch{}
	for _, r := range rows {
		var cb connectorwire.ChatBatch
		if err := json.Unmarshal(r.Payload, &cb); err != nil {
			t.Fatal(err)
		}
		repaired[cb.Chat.ID] = cb
	}
	if grp := repaired["oc_grp"]; len(grp.Messages) != 1 || grp.Messages[0].ID != "om_a1" || grp.Messages[0].SenderName != "Example Alice" {
		t.Fatalf("repaired oc_grp = %+v", grp)
	}
	if dm := repaired["oc_dm"]; dm.Chat.Name != "Example Bob" || len(dm.Messages) != 1 || dm.Messages[0].SenderName != "Example Bob" {
		t.Fatalf("repaired oc_dm = %+v", dm)
	}

	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("run 2 called ResolveUserNames again (%d)", resolveCalls.Load())
	}

	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(keyA).BackfillVersion = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if resolveCalls.Load() != 1 {
		t.Fatalf("reset BackfillVersion re-resolved already-repaired messages (%d calls)", resolveCalls.Load())
	}
	if rowsAfter, _ := w.store.LarkSyncOutboxAfter(ctx, larksyncwire.OutboxChat, 4, 10); len(rowsAfter) != 0 {
		t.Fatalf("unexpected extra outbox rows after idempotent rerun: %d", len(rowsAfter))
	}
}

func TestLarkSyncSenderRepairRetriesAfterReauthorization(t *testing.T) {
	for _, tc := range []struct{ partialNames, completed bool }{{false, false}, {true, false}, {false, true}} {
		t.Run(fmt.Sprintf("partial_names=%v/completed=%v", tc.partialNames, tc.completed), func(t *testing.T) {
			w := newServiceWorld(t)
			ctx := context.Background()
			w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
			key, acc := w.adopt(t, "bsync", "ou_owner", "access-old", "refresh-old", 24*time.Hour)
			if _, err := w.svc.change(ctx, func(st *syncState) error {
				st.binding(key).Tier = larksyncwire.TierFull
				if tc.completed {
					st.binding(key).BackfillVersion = backfillVersion
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			batch := connectorwire.ChatBatch{
				Chat: kbwire.SourceChat{ID: "oc_example", Name: "Example group", Kind: "group"},
				Messages: []kbwire.SourceMessage{
					{ID: "om_one", SenderID: "ou_one", At: w.now().Add(-time.Hour), Text: "example one"},
					{ID: "om_two", SenderID: "ou_two", At: w.now().Add(-time.Minute), Text: "example two"},
				},
			}
			raw, err := json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.store.CommitLarkSyncRun(ctx, key, map[string]string{"im:oc_example": "100"}, nil,
				[]larksyncwire.OutboxRow{{Kind: larksyncwire.OutboxChat, CanonicalKey: "chat:oc_example", Payload: raw}}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			var granted atomic.Bool
			w.mu.Lock()
			w.resolveNames = func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), ids []string) (map[string]string, error) {
				calls.Add(1)
				if !granted.Load() {
					names := map[string]string{}
					if tc.partialNames {
						names["ou_one"] = "Example One"
					}
					return names, &larksyncwire.Error{Kind: larksyncwire.ErrKindScope, Msg: "contact:contact.base:readonly required"}
				}
				out := map[string]string{}
				for _, id := range ids {
					out[id] = "Example " + id
				}
				return out, nil
			}
			w.mu.Unlock()
			w.script("bsync", acc, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
				res := larksyncwire.Result{Cursors: map[string]string{"im:oc_example": "200"}}
				if tc.completed && !granted.Load() {
					res.Errors = []larksyncwire.ResourceError{{Resource: "sender_names", Kind: larksyncwire.ErrKindScope, Detail: "contact:contact.base:readonly required"}}
				}
				return res, nil
			})
			if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
				t.Fatal(err)
			}
			b := w.binding(t, key)
			if b.Health != larksyncwire.HealthScopeMissing || b.BackfillVersion != 0 || !strings.Contains(b.LastError, "contact:contact.base:readonly") {
				t.Fatalf("repair refusal not exposed or incorrectly completed: health=%s version=%d error=%s", b.Health, b.BackfillVersion, b.LastError)
			}
			if got := w.store.cursorsOf(key)["im:oc_example"]; got != "200" {
				t.Fatalf("readable sync progress lost: cursor=%s", got)
			}
			w.advance(time.Minute)
			st, err := w.svc.load(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app, err := w.svc.app(ctx, &st, "bsync")
			if err != nil {
				t.Fatal(err)
			}
			granted.Store(true)
			_, err = w.svc.adopt(ctx, "bsync", pendingKey("bsync"), larksyncwire.TierFull, app, larksyncwire.Token{
				AccessToken: "access-new", RefreshToken: "refresh-new", OpenID: "ou_owner", TenantKey: "t1",
				ExpiresAt: w.now().Add(24 * time.Hour), Scopes: larksyncwire.ScopesFor(larksyncwire.TierFull),
			})
			if err != nil {
				t.Fatal(err)
			}
			if b = w.binding(t, key); b.BackfillVersion != 0 || !b.NextRunAt.Equal(w.now()) || !b.NextIMRunAt.Equal(w.now()) {
				t.Fatal("authorization did not preserve pending repair and make both schedules due")
			}
			w.svc.tickOnce(ctx)
			wantCalls := int32(2)
			if tc.completed {
				wantCalls = 1
			}
			if b = w.binding(t, key); b.Health != larksyncwire.HealthOK || b.BackfillVersion != backfillVersion || calls.Load() != wantCalls {
				t.Fatalf("scheduler did not recover repair: health=%s version=%d calls=%d", b.Health, b.BackfillVersion, calls.Load())
			}
			rows, err := w.store.LarkSyncOutboxAfter(ctx, larksyncwire.OutboxChat, 1, 10)
			if err != nil {
				t.Fatal(err)
			}
			repaired := map[string]bool{}
			for _, row := range rows {
				var cb connectorwire.ChatBatch
				if err := json.Unmarshal(row.Payload, &cb); err != nil {
					t.Fatal(err)
				}
				for _, m := range cb.Messages {
					if m.SenderName != "" {
						repaired[m.ID] = true
					}
				}
			}
			if len(repaired) != 2 {
				t.Fatalf("repair did not deliver both sender names: %v", repaired)
			}
			w.advance(imEvery)
			w.svc.tickOnce(ctx)
			if calls.Load() != wantCalls {
				t.Fatal("completed repair was not idempotent")
			}
		})
	}
}

func TestLarkSyncOldRunCannotPostponeRenewedGrant(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()
	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
	key, acc := w.adopt(t, "bsync", "ou_owner", "example-old", "example-refresh", 24*time.Hour)
	started, resume := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	w.script("bsync", acc, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-resume
			return larksyncwire.Result{Errors: []larksyncwire.ResourceError{{Resource: "sender_names", Kind: larksyncwire.ErrKindScope}}}, nil
		}
		return larksyncwire.Result{}, nil
	})
	done := make(chan error, 1)
	go func() { done <- w.svc.runSync(ctx, key) }()
	t.Cleanup(func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	})
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("old run did not start")
	}
	w.advance(time.Minute)
	w.adopt(t, "bsync", "ou_owner", "example-new", "example-new-refresh", 24*time.Hour)
	close(resume)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("old run did not finish")
	}
	b := w.binding(t, key)
	if b.Health != larksyncwire.HealthOK || !b.NextRunAt.Equal(w.now()) || b.BackfillVersion != 0 {
		t.Fatalf("old run overwrote renewed grant: health=%s due=%v repair=%d", b.Health, b.NextRunAt, b.BackfillVersion)
	}
	w.svc.tickOnce(ctx)
	if calls.Load() != 2 {
		t.Fatal("renewed grant did not automatically run on the next tick")
	}
}

func TestLarkSyncBackfillDroppedLinksIdempotentAndScoped(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()

	w.addSyncBot(t, "bsyncA", "cli_a", larksyncwire.TierFull)
	w.addSyncBot(t, "bsyncB", "cli_b", larksyncwire.TierFull)
	keyA, accA := w.adopt(t, "bsyncA", "ou_owner_a", "acc-a", "ref-a", 24*time.Hour)
	keyB, _ := w.adopt(t, "bsyncB", "ou_owner_b", "acc-b", "ref-b", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(keyA).Tier = larksyncwire.TierFull
		st.binding(keyA).BackfillVersion = backfillVersion
		st.binding(keyB).Tier = larksyncwire.TierFull
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	seedChat := func(cb connectorwire.ChatBatch) larksyncwire.OutboxRow {
		raw, err := json.Marshal(cb)
		if err != nil {
			t.Fatal(err)
		}
		return larksyncwire.OutboxRow{Kind: larksyncwire.OutboxChat, CanonicalKey: "chat:" + cb.Chat.ID, Payload: raw}
	}
	seedDoc := func(it kbwire.Item) larksyncwire.OutboxRow {
		raw, err := json.Marshal(it)
		if err != nil {
			t.Fatal(err)
		}
		return larksyncwire.OutboxRow{Kind: larksyncwire.OutboxDoc, CanonicalKey: it.CanonicalKey, Payload: raw}
	}
	at := w.now().Add(-2 * time.Hour)
	if err := w.store.CommitLarkSyncRun(ctx, keyA, map[string]string{
		larksyncwire.PendingLinksCursor: `["doxAlreadyQueued"]`,
	}, []larksyncwire.IndexRecord{
		{Key: larksyncwire.KeyMessage + "om_a1", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyA, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_b1", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: keyB, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyDoc + "doxFetched", Kind: "doc", Revision: 3, BodyHash: "body-hash-1"},
		{Key: larksyncwire.KeyDoc + "doxStub", Kind: "doc", Revision: 0, BodyHash: ""},
	}, []larksyncwire.OutboxRow{
		seedChat(connectorwire.ChatBatch{
			Chat: kbwire.SourceChat{ID: "oc_1", Name: "Example Group", Kind: "group"},
			Messages: []kbwire.SourceMessage{
				{
					ID: "om_a1", SenderID: "ou_peer", SenderName: "Peer", At: at,
					Text: "https://my.feishu.cn/docx/doxFetched " +
						"https://my.feishu.cn/docx/doxStub " +
						"https://my.feishu.cn/docx/doxDropped " +
						"https://my.feishu.cn/wiki/wikFetched " +
						"https://my.feishu.cn/wiki/wikDropped " +
						"https://my.feishu.cn/docx/doxAlreadyQueued",
				},
				{
					ID: "om_b1", SenderID: "ou_other", SenderName: "Other", At: at.Add(time.Minute),
					Text: "https://my.feishu.cn/docx/doxOtherTenant",
				},
			},
		}),
		seedDoc(kbwire.Item{
			Kind:         "docs",
			CanonicalKey: larksyncwire.KeyDoc + "doxFromWikFetched",
			Aliases:      []string{"wikFetched"},
			Revision:     2,
			BodyHash:     "wiki-body-hash",
			Content:      "already synced wiki doc",
		}),
	}); err != nil {
		t.Fatal(err)
	}

	var extractCalls, mergeCalls atomic.Int32
	w.mu.Lock()
	w.extractDocLinks = func(text string) []larksyncwire.PendingLinkCandidate {
		extractCalls.Add(1)
		var out []larksyncwire.PendingLinkCandidate
		for _, field := range strings.Fields(text) {
			switch {
			case strings.HasPrefix(field, "https://my.feishu.cn/docx/"):
				tok := field[len("https://my.feishu.cn/docx/"):]
				out = append(out, larksyncwire.PendingLinkCandidate{Token: tok, ObjType: "docx", URL: field})
			case strings.HasPrefix(field, "https://my.feishu.cn/wiki/"):
				tok := field[len("https://my.feishu.cn/wiki/"):]
				out = append(out, larksyncwire.PendingLinkCandidate{Token: tok, ObjType: "wiki", URL: field})
			}
		}
		return out
	}
	w.mergePendingLinks = func(existingCursor string, add []larksyncwire.PendingLinkCandidate) (string, int) {
		mergeCalls.Add(1)
		var list []string
		_ = json.Unmarshal([]byte(existingCursor), &list)
		have := map[string]bool{}
		for _, tok := range list {
			have[tok] = true
		}
		added := 0
		for _, c := range add {
			if have[c.Token] {
				continue
			}
			have[c.Token] = true
			list = append(list, c.Token)
			added++
		}
		if added == 0 {
			return existingCursor, 0
		}
		slices.Sort(list)
		raw, _ := json.Marshal(list)
		return string(raw), added
	}
	w.mu.Unlock()

	var seenCursor string
	var seenAuthAt time.Time
	var seenScopes []string
	w.script("bsyncA", accA, func(_ context.Context, b larksyncwire.Binding, _ func(context.Context) (string, error), cursors map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		seenCursor = cursors[larksyncwire.PendingLinksCursor]
		seenAuthAt = b.AuthorizedAt
		seenScopes = slices.Clone(b.GrantedScopes)
		return larksyncwire.Result{}, nil
	})

	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if seenAuthAt.IsZero() || len(seenScopes) == 0 {
		t.Fatalf("binding auth metadata not passed to Syncer: AuthorizedAt=%v Scopes=%v", seenAuthAt, seenScopes)
	}
	wantQueue := `["doxAlreadyQueued","doxDropped","doxStub","wikDropped"]`
	if seenCursor != wantQueue {
		t.Fatalf("run 1 received links_queue = %s, want %s", seenCursor, wantQueue)
	}
	if got := w.store.cursorsOf(keyA)[larksyncwire.PendingLinksCursor]; got != wantQueue {
		t.Fatalf("persisted links_queue = %s, want %s", got, wantQueue)
	}
	if got := w.binding(t, keyA).LinksBackfillVersion; got != linksBackfillVersion {
		t.Fatalf("LinksBackfillVersion = %d, want %d", got, linksBackfillVersion)
	}
	extractAfterRun1 := extractCalls.Load()
	mergeAfterRun1 := mergeCalls.Load()

	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if extractCalls.Load() != extractAfterRun1 || mergeCalls.Load() != mergeAfterRun1 {
		t.Fatalf("run 2 re-ran remediation despite LinksBackfillVersion=%d", linksBackfillVersion)
	}

	if err := w.store.CommitLarkSyncRun(ctx, keyA, nil, []larksyncwire.IndexRecord{
		{Key: larksyncwire.KeyDoc + "doxDropped", Kind: "doc", Revision: 1, BodyHash: "now-has-body"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(keyA).LinksBackfillVersion = 0
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.runMode(ctx, keyA, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if got := w.store.cursorsOf(keyA)[larksyncwire.PendingLinksCursor]; got != wantQueue {
		t.Fatalf("after idempotent reset rerun: links_queue = %s, want %s", got, wantQueue)
	}
}

func TestLarkSyncBackfillV2ResolvesExternalSendersAndUnnamedDMsIdempotently(t *testing.T) {
	w := newServiceWorld(t)
	ctx := context.Background()

	w.addSyncBot(t, "bsync", "cli_sync", larksyncwire.TierFull)
	key, acc := w.adopt(t, "bsync", "ou_owner", "acc-1", "ref-1", 24*time.Hour)
	if _, err := w.svc.change(ctx, func(st *syncState) error {
		b := st.binding(key)
		b.Tier = larksyncwire.TierFull
		b.BackfillVersion = 2
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	seedBatch := func(cb connectorwire.ChatBatch) larksyncwire.OutboxRow {
		raw, err := json.Marshal(cb)
		if err != nil {
			t.Fatal(err)
		}
		return larksyncwire.OutboxRow{Kind: larksyncwire.OutboxChat, CanonicalKey: "chat:" + cb.Chat.ID, Payload: raw}
	}
	at := w.now().Add(-time.Hour)
	if err := w.store.CommitLarkSyncRun(ctx, key, map[string]string{
		"im:p2p_chats":                 `{"oc_dm_human":"","oc_dm_owner_only":"","oc_dm_bot":""}`,
		larksyncwire.SenderNamesCursor: `{"ou_left":{"n":"Former Member","t":1}}`,
	}, []larksyncwire.IndexRecord{
		{Key: larksyncwire.KeyMessage + "om_ext", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_dm_h", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_dm_o", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
		{Key: larksyncwire.KeyMessage + "om_dm_b", Kind: "message", Sources: []larksyncwire.SourceRef{{Binding: key, Entry: larksyncwire.EntryChat}}},
	}, []larksyncwire.OutboxRow{
		seedBatch(connectorwire.ChatBatch{
			Chat:     kbwire.SourceChat{ID: "oc_ext_grp", Name: "External Group", Kind: "group"},
			Messages: []kbwire.SourceMessage{{ID: "om_ext", SenderID: "ou_ext", SenderName: "", Text: "hello", At: at}},
		}),
		seedBatch(connectorwire.ChatBatch{
			Chat:     kbwire.SourceChat{ID: "oc_dm_human", Name: "", Kind: "dm"},
			Messages: []kbwire.SourceMessage{{ID: "om_dm_h", SenderID: "ou_friend", SenderName: "", Text: "hi", At: at.Add(time.Second)}},
		}),
		seedBatch(connectorwire.ChatBatch{
			Chat:     kbwire.SourceChat{ID: "oc_dm_owner_only", Name: "", Kind: "dm"},
			Messages: []kbwire.SourceMessage{{ID: "om_dm_o", SenderID: "ou_owner", Mine: true, Text: "ping", At: at.Add(2 * time.Second)}},
		}),
		seedBatch(connectorwire.ChatBatch{
			Chat:     kbwire.SourceChat{ID: "oc_dm_bot", Name: "", Kind: "dm"},
			Messages: []kbwire.SourceMessage{{ID: "om_dm_b", SenderID: "cli_helper", SenderName: "", Text: "alert", At: at.Add(3 * time.Second)}},
		}),
	}); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var gotReq larksyncwire.NameResolveRequest
	w.mu.Lock()
	w.resolveNamesWithFallback = func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), req larksyncwire.NameResolveRequest) (larksyncwire.NameResolveResult, error) {
		calls.Add(1)
		gotReq = req
		return larksyncwire.NameResolveResult{
			UserNames: map[string]string{
				"ou_ext":     "External Member",
				"ou_friend":  "External Friend",
				"cli_helper": "Helper Bot",
			},
			P2PChatNames: map[string]string{
				"oc_dm_human":      "External Friend",
				"oc_dm_owner_only": "Quiet Peer",
				"oc_dm_bot":        "Helper Bot",
			},
		}, nil
	}
	w.mu.Unlock()
	w.script("bsync", acc, func(_ context.Context, _ larksyncwire.Binding, _ func(context.Context) (string, error), _ map[string]string, _ larksyncwire.Index) (larksyncwire.Result, error) {
		return larksyncwire.Result{}, nil
	})

	if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	if !slices.Equal(gotReq.P2PChatIDs, []larksyncwire.ChatID{"oc_dm_bot", "oc_dm_human", "oc_dm_owner_only"}) {
		t.Fatalf("P2PChatIDs = %v", gotReq.P2PChatIDs)
	}
	if !slices.Equal(gotReq.ChatSenders["oc_ext_grp"], []string{"ou_ext"}) || !slices.Equal(gotReq.ChatSenders["oc_dm_bot"], []string{"cli_helper"}) {
		t.Fatalf("ChatSenders = %v", gotReq.ChatSenders)
	}
	if gotReq.NameBook != `{"ou_left":{"n":"Former Member","t":1}}` {
		t.Fatalf("NameBook = %q, want the binding's stored book", gotReq.NameBook)
	}
	if !gotReq.SampleTimes["ou_ext"].Equal(at) || gotReq.SampleChats["ou_ext"] != "oc_ext_grp" || gotReq.SampleMessages["ou_ext"] != "om_ext" {
		t.Fatalf("sample of ou_ext = %v %v %v", gotReq.SampleMessages["ou_ext"], gotReq.SampleTimes["ou_ext"], gotReq.SampleChats["ou_ext"])
	}
	if backfillVersion != 3 {
		t.Fatalf("backfillVersion = %d, want 3", backfillVersion)
	}
	if got := w.binding(t, key).BackfillVersion; got != backfillVersion {
		t.Fatalf("BackfillVersion = %d, want %d", got, backfillVersion)
	}

	rows, err := w.store.LarkSyncOutboxAfter(ctx, larksyncwire.OutboxChat, 4, 20)
	if err != nil || len(rows) != 4 {
		t.Fatalf("repair rows = %d, err = %v", len(rows), err)
	}
	repaired := map[string]connectorwire.ChatBatch{}
	for _, r := range rows {
		var cb connectorwire.ChatBatch
		if err := json.Unmarshal(r.Payload, &cb); err != nil {
			t.Fatal(err)
		}
		repaired[cb.Chat.ID] = cb
	}
	if g := repaired["oc_ext_grp"]; len(g.Messages) != 1 || g.Messages[0].SenderName != "External Member" {
		t.Fatalf("repaired oc_ext_grp = %+v", g)
	}
	if d := repaired["oc_dm_human"]; d.Chat.Name != "External Friend" || len(d.Messages) != 1 || d.Messages[0].SenderName != "External Friend" {
		t.Fatalf("repaired oc_dm_human = %+v", d)
	}
	if d := repaired["oc_dm_owner_only"]; d.Chat.Name != "Quiet Peer" || len(d.Messages) != 0 {
		t.Fatalf("repaired oc_dm_owner_only = %+v", d)
	}
	if d := repaired["oc_dm_bot"]; d.Chat.Name != "Helper Bot" || len(d.Messages) != 1 || d.Messages[0].SenderName != "Helper Bot" {
		t.Fatalf("repaired oc_dm_bot = %+v", d)
	}
	var savedP2P map[string]string
	if err := json.Unmarshal([]byte(w.store.cursorsOf(key)["im:p2p_chats"]), &savedP2P); err != nil {
		t.Fatalf("im:p2p_chats cursor: %v", err)
	}
	if savedP2P["oc_dm_human"] != "External Friend" || savedP2P["oc_dm_owner_only"] != "Quiet Peer" || savedP2P["oc_dm_bot"] != "Helper Bot" {
		t.Fatalf("updated im:p2p_chats = %v", savedP2P)
	}

	if _, err := w.svc.change(ctx, func(st *syncState) error {
		st.binding(key).BackfillVersion = 1
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.runMode(ctx, key, larksyncwire.ModeIM); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("reset BackfillVersion re-resolved already-repaired data (%d calls)", calls.Load())
	}
}
