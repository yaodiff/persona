package kbhistory

import (
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func TestProviderCheckpointAcrossImporterRestart(t *testing.T) {
	const chat, count = "account-one-thread", 60
	src := &fakeHistory{
		chats: []connectorwire.HistoryChat{{ID: chat, Kind: "dm"}},
		convs: map[string][]kb.SourceMessage{chat: historyConv(chat, count, recentStart(count, time.Hour), time.Hour)},
	}
	src.hook = func(call int, _ connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if call == 3 {
			return connectorwire.HistoryPage{Pace: &connectorwire.Pace{Stop: true}}, nil, true
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht := newHistoryTest(t, src)
	p := ht.plan(true, nil)
	key := kb.ChatID(testSource, chat)
	read := func(day string) (checkpoint, bool) {
		return providerCheckpoint(ht.e, ht.cfg, testSource, key, day, ht.clock.Now())
	}
	before, ok := read("")
	if !ok || before.State != "pending" || before.Read != 0 || before.SourceID != chat || before.FiledAs != testSource {
		t.Fatalf("pending projection: %+v, %v", before, ok)
	}
	ht.run()
	if _, ok := read(""); ok {
		t.Fatal("STOP must withdraw the projection")
	}
	if err := RemoveStop(ht.data); err != nil {
		t.Fatal(err)
	}
	part, ok := read("")
	if !ok || part.Read == 0 || part.Read >= count || part.Approval != before.Approval {
		t.Fatalf("partial checkpoint: %+v, %v", part, ok)
	}
	serialized, _ := json.Marshal(part)
	for _, private := range []string{chat, "cursor", "positions", "last_error", "source_id"} {
		if strings.Contains(strings.ToLower(string(serialized)), private) {
			t.Fatalf("private field %q present in checkpoint", private)
		}
	}
	// A fresh store and driver instance pick up the cursor written by the first.
	reopened, err := kb.NewStore(ht.data)
	if err != nil {
		t.Fatal(err)
	}
	ht.e.Store = reopened
	fresh := NewDriver(Deps{
		Data: ht.data, Src: src, Eng: ht.d.eng, Cfg: ht.d.cfg, Connected: ht.d.connected,
		Notify: ht.d.notify, Calls: new(sync.Mutex), Instance: "second-process",
		Indexed: ht.d.indexed, Running: new(atomic.Pointer[string]), Logf: t.Logf,
	})
	fresh.d.now, fresh.d.sleep = ht.clock.Now, ht.clock.Sleep
	ht.d = fresh.d
	src.hook = nil
	calls := len(src.queries)
	ht.run()
	done, ok := read(ht.clock.Now().Format(time.DateOnly))
	if !ok || done.State != kb.HistoryDone || done.Approval != before.Approval ||
		done.Written != count || len(src.queries) <= calls || src.queries[calls].Before == "" {
		t.Fatalf("resumed checkpoint: %+v, %v", done, ok)
	}
	ht.run()
	again, ok := read("")
	if !ok || again != done || len(ht.kept(chat)) != count {
		t.Fatal("replayed import duplicated archived lines or changed the checkpoint")
	}
	p.GeneratedAt = p.GeneratedAt.Add(time.Second)
	if err := SavePlan(ht.data, p); err != nil {
		t.Fatal(err)
	}
	replaced, ok := read("")
	if !ok || replaced.Approval == done.Approval {
		t.Fatal("replacement approval kept the old capability")
	}
	if _, ok := read("1900-01-01"); ok {
		t.Fatal("granted a day outside retention")
	}
	ht.item(chat, "off")
	if _, ok := read(""); ok {
		t.Fatal("excluded chat still exposed its checkpoint")
	}
}

func TestProviderCheckpointRefusesAmbiguousIdentity(t *testing.T) {
	const raw = "account/one-thread"
	key := kb.ChatID(testSource, raw)
	ht := newHistoryTest(t, &fakeHistory{chats: []connectorwire.HistoryChat{{ID: raw, Kind: "dm"}}})
	p := ht.plan(true, nil)
	rejected := func() {
		t.Helper()
		if _, ok := providerCheckpoint(ht.e, ht.cfg, testSource, key, "", ht.clock.Now()); ok {
			t.Fatal("a second account or connector matched the authorized chat")
		}
	}
	other := strings.ReplaceAll(raw, "/", "_")
	if other == raw || kb.ChatID(testSource, other) != key {
		t.Fatal("invalid collision fixture")
	}
	if err := ht.e.Store.PutChat(&kb.Chat{ID: key, Source: testSource, SourceID: other, Kind: "dm"}); err != nil {
		t.Fatal(err)
	}
	rejected()
	if err := ht.e.Store.PutChat(&kb.Chat{ID: key, Source: testSource, SourceID: raw, Connector: "foreign", Kind: "dm"}); err != nil {
		t.Fatal(err)
	}
	rejected()
	if err := ht.e.Store.PutChat(&kb.Chat{ID: key, Source: testSource, SourceID: raw, Kind: "dm"}); err != nil {
		t.Fatal(err)
	}
	p.Regenerate([]connectorwire.HistoryChat{{ID: other, Kind: "dm"}}, ht.clock.Now())
	ht.approve(p)
	if err := SavePlan(ht.data, p); err != nil {
		t.Fatal(err)
	}
	rejected()
}
