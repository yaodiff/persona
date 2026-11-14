package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

func muteHub(t *testing.T) *inboxHub {
	t.Helper()
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitNeedsYouIdle(t, h.Service)  // fixture writes start after the initial asynchronous read finishes
	t.Cleanup(h.inbox.titling.Wait) // a merge titles its row in the background (inbox_merge_title.go)
	return h
}

func mailItem(id, from, name string) *InboxItem {
	return &InboxItem{ID: id, Source: "mail", Kind: "mail", Title: "示例订单", UpdatedAt: time.Now(),
		Mail: &InboxMail{AccountID: "acc1", From: MailAddress{Name: name, Email: from}}}
}

// What an item can be muted by, for each kind of source.
func TestMutesOf(t *testing.T) {
	for name, c := range map[string]struct {
		it   *InboxItem
		want string
	}{
		"feishu dm":    {&InboxItem{Source: "lark", Title: "韩梅梅", Sender: "韩梅梅", Lark: &InboxLark{FeedID: "f1", SenderID: "u1"}}, "[{chat f1 韩梅梅}]"},
		"feishu group": {&InboxItem{Source: "lark", Title: "周末徒步群", Sender: "Tom", Lark: &InboxLark{FeedID: "f3", IsGroup: true, SenderID: "u-tom"}}, "[{chat f3 周末徒步群} {sender u-tom Tom}]"},
		"feishu doc":   {&InboxItem{Source: "lark", Lark: &InboxLark{FeedID: "f9"}, Doc: &InboxDoc{}}, "[]"},
		"mail":         {mailItem("m", "Shop@Example.com", ""), "[{sender shop@example.com shop@example.com}]"},
		"connector": {&InboxItem{Source: "github", Connector: &InboxConnector{Tags: []connector.Tag{
			{Key: connector.TagRepo, Value: "example-org/example-repo", Name: "example-org/example-repo"},
			{Key: connector.TagBranch, Value: "main"}, {Key: connector.TagSender, Value: "alice", Name: "alice"}}}},
			"[{repo example-org/example-repo example-org/example-repo} {sender alice alice}]"},
		"larkcli chat and sender": {&InboxItem{Source: "larkcli", Connector: &InboxConnector{Tags: []connector.Tag{
			{Key: connector.TagChat, Value: "oc_example_chat", Name: "Example Chat"},
			{Key: connector.TagSender, Value: "ou_example_sender", Name: "Example Sender"}}}},
			"[{chat oc_example_chat Example Chat} {sender ou_example_sender Example Sender}]"},
		// a subscription's row (#5306): its sender, never the subscription itself (turning it off is that)
		"subscription": {&InboxItem{Source: "sub_x", Connector: &InboxConnector{Tags: []connector.Tag{
			{Key: "source", Value: "sub_x", Name: "Notices"}, {Key: connector.TagSender, Value: "office", Name: "Office"}}}},
			"[{sender office Office}]"},
	} {
		if got := fmt.Sprint(mutesOf(c.it)); got != c.want && !(c.want == "[]" && got == "[]") {
			t.Errorf("%s: %s, want %s", name, got, c.want)
		}
	}
}

// Muting a mail's sender files the open items it covers (that sender's, not another's), keeps the mute, lists it,
// files what comes in later at the next collection, and unmuting leaves what it covered done.
func TestMuteFromAnItem(t *testing.T) {
	h := muteHub(t)
	ctx := context.Background()
	h.inbox.items["a"] = mailItem("a", "shop@example.com", "示例商城")
	h.inbox.items["b"] = mailItem("b", "SHOP@example.com", "示例商城")
	h.inbox.items["c"] = mailItem("c", "lucy@example.com", "Lucy")
	mux := h.routes()

	_, out := inboxCall(t, mux, "GET", "/api/v1/inbox", "")
	if !strings.Contains(fmt.Sprint(out["items"]), "mutes:[map[name:示例商城 tag:sender value:shop@example.com]]") {
		t.Fatalf("items do not say what mutes them: %v", out["items"])
	}
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"a","tag":"repo"}`); code != 400 {
		t.Fatalf("a mail muted by repository: %d", code)
	}
	code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"a","tag":"sender"}`)
	if code != 200 || out["settled"] != float64(2) {
		t.Fatalf("mute: %d %v", code, out)
	}
	for id, done := range map[string]bool{"a": true, "b": true, "c": false} {
		it := h.inbox.items[id]
		if it.open() == done || done && it.Reason != "已静音：示例商城" {
			t.Errorf("%s: done %v, reason %q", id, it.DoneAt, it.Reason)
		}
	}
	_, out = inboxCall(t, mux, "GET", "/api/v1/inbox/mutes", "")
	list := out["mutes"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["source_name"] != "邮件" || list[0].(map[string]any)["value"] != "shop@example.com" {
		t.Fatalf("mutes %v", out)
	}
	// Muting it again is the same mute.
	if _, out := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"b","tag":"sender"}`); out["settled"] != float64(0) {
		t.Fatalf("again: %v", out)
	}
	if len(h.listedMutes()) != 1 {
		t.Fatal("muted twice")
	}

	// Kept: the manager started again has it, and a later mail of the sender is filed at the next collection.
	h2 := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	h2.inboxReady(ctx)
	h2.inbox.items["d"] = mailItem("d", "shop@example.com", "示例商城")
	h2.inbox.mu.Lock()
	h2.inbox.applyMutes(time.Now()) // as each collection does
	h2.inbox.mu.Unlock()
	if h2.inbox.items["d"].open() {
		t.Fatal("a later mail of a muted sender stayed open")
	}

	id := list[0].(map[string]any)["id"].(string)
	if code, _ := inboxCall(t, mux, "DELETE", "/api/v1/inbox/mutes/"+id, ""); code != 200 || len(h.listedMutes()) != 0 {
		t.Fatalf("unmute: %d %v", code, h.listedMutes())
	}
	if h.inbox.items["a"].open() {
		t.Fatal("unmuting reopened what it covered")
	}
}

// muteFailStore fails PutSetting for the mutes key on demand, then recovers, so a mute change can be retried.
type muteFailStore struct {
	*inboxMemStore
	putMutesFail bool
}

func (s *muteFailStore) PutSetting(ctx context.Context, key string, v any) error {
	if s.putMutesFail && key == inboxMutesKey {
		return errMuteStoreDown
	}
	return s.inboxMemStore.PutSetting(ctx, key, v)
}

func newMuteFailHub(t *testing.T) (*inboxHub, *muteFailStore) {
	t.Helper()
	f := newFakeHub().withMachine()
	in := newFakeInbox()
	f.inbox = in
	st := &muteFailStore{inboxMemStore: newInboxMemStore()}
	h := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)})
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &inboxHub{Service: h, f: f, in: in, st: st.inboxMemStore}, st
}

// keptMutes reads the mutes the store keeps (false: no mutes were ever written).
func keptMutes(t *testing.T, st Store) []InboxMute {
	t.Helper()
	var got []InboxMute
	ok, err := st.Setting(context.Background(), inboxMutesKey, &got)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return nil
	}
	return got
}

// Muting while the settings store is down reports the failure and changes nothing: the mute is not in memory, the
// items stay open and nothing is announced; retrying after recovery files them and keeps the mute.
func TestMutePersistFailureKeepsMemory(t *testing.T) {
	h, st := newMuteFailHub(t)
	ctx := context.Background()
	h.inbox.mu.Lock()
	h.inbox.items["a"] = mailItem("a", "shop@example.com", "Shop")
	h.inbox.items["b"] = mailItem("b", "lucy@example.com", "Lucy")
	h.inbox.mu.Unlock()
	mute := InboxMute{Source: "mail", Tag: connector.TagSender, Value: "shop@example.com", Name: "Shop"}

	st.putMutesFail = true
	if _, _, err := h.addMute(ctx, mute); !errors.Is(err, errMuteStoreDown) {
		t.Fatalf("addMute with the store down: %v", err)
	}
	h.inbox.mu.Lock()
	mutesInMemory := len(h.inbox.mutes)
	shopOpen := h.inbox.items["a"].open()
	lucyOpen := h.inbox.items["b"].open()
	h.inbox.mu.Unlock()
	if mutesInMemory != 0 || !shopOpen || !lucyOpen {
		t.Fatalf("the failed mute changed memory: mutes=%d, shop open=%v lucy open=%v", mutesInMemory, shopOpen, lucyOpen)
	}
	if got := keptMutes(t, st); len(got) != 0 {
		t.Fatalf("the failed mute wrote the store: %+v", got)
	}
	if pushes := h.in.pushes(); pushes != 0 {
		t.Fatalf("the failed mute announced %d changes, want 0", pushes)
	}

	st.putMutesFail = false
	m, settled, err := h.addMute(ctx, mute)
	if err != nil || settled != 1 {
		t.Fatalf("retry: settled=%d err=%v", settled, err)
	}
	h.inbox.mu.Lock()
	mutesInMemory = len(h.inbox.mutes)
	shopOpen = h.inbox.items["a"].open()
	lucyOpen = h.inbox.items["b"].open()
	h.inbox.mu.Unlock()
	if mutesInMemory != 1 || shopOpen || !lucyOpen {
		t.Fatalf("after the retry: mutes=%d, shop open=%v lucy open=%v", mutesInMemory, shopOpen, lucyOpen)
	}
	if got := keptMutes(t, st); len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("the retry kept %+v", got)
	}

	// A restart reads the kept mute, so the failure did not leave an unpersisted mute.
	h2 := New(Options{Hub: h.f, Store: func() Store { return st }, Logf: testLogf(t)})
	if err := h2.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h2.inbox.mu.Lock()
	kept := len(h2.inbox.mutes)
	h2.inbox.mu.Unlock()
	if kept != 1 {
		t.Fatalf("after a restart %d mutes, want 1", kept)
	}
}

// Unmuting while the settings store is down reports the failure and leaves the mute in memory and in the store;
// retrying after recovery removes it from both.
func TestUnmutePersistFailureRollsBack(t *testing.T) {
	h, st := newMuteFailHub(t)
	ctx := context.Background()
	h.inbox.mu.Lock()
	h.inbox.items["a"] = mailItem("a", "shop@example.com", "Shop")
	h.inbox.mu.Unlock()
	m, _, err := h.addMute(ctx, InboxMute{Source: "mail", Tag: connector.TagSender, Value: "shop@example.com", Name: "Shop"})
	if err != nil {
		t.Fatal(err)
	}

	st.putMutesFail = true
	if err := h.removeMutes(ctx, func(x InboxMute) bool { return x.ID == m.ID }); !errors.Is(err, errMuteStoreDown) {
		t.Fatalf("removeMutes with the store down: %v", err)
	}
	h.inbox.mu.Lock()
	mutesInMemory := len(h.inbox.mutes)
	h.inbox.mu.Unlock()
	if mutesInMemory != 1 {
		t.Fatalf("the failed unmute left %d mutes in memory, want 1", mutesInMemory)
	}
	if got := keptMutes(t, st); len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("the failed unmute changed the store: %+v", got)
	}

	st.putMutesFail = false
	if err := h.removeMutes(ctx, func(x InboxMute) bool { return x.ID == m.ID }); err != nil {
		t.Fatalf("retry: %v", err)
	}
	h.inbox.mu.Lock()
	mutesInMemory = len(h.inbox.mutes)
	h.inbox.mu.Unlock()
	if mutesInMemory != 0 {
		t.Fatalf("after the retry %d mutes in memory, want 0", mutesInMemory)
	}
	if got := keptMutes(t, st); len(got) != 0 {
		t.Fatalf("after the retry the store keeps %+v", got)
	}

	// A restart reads no mute: the removal really was kept.
	h2 := New(Options{Hub: h.f, Store: func() Store { return st }, Logf: testLogf(t)})
	if err := h2.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h2.inbox.mu.Lock()
	kept := len(h2.inbox.mutes)
	h2.inbox.mu.Unlock()
	if kept != 0 {
		t.Fatalf("after a restart %d mutes, want 0", kept)
	}
}

// An already-kept mute and a removal that matches nothing need no write: both still work while the settings store
// is down.
func TestMuteChangesThatNeedNoWriteIgnoreStoreFailure(t *testing.T) {
	h, st := newMuteFailHub(t)
	ctx := context.Background()
	h.inbox.mu.Lock()
	h.inbox.items["a"] = mailItem("a", "shop@example.com", "Shop")
	h.inbox.mu.Unlock()
	mute := InboxMute{Source: "mail", Tag: connector.TagSender, Value: "shop@example.com", Name: "Shop"}
	if _, _, err := h.addMute(ctx, mute); err != nil {
		t.Fatal(err)
	}

	st.putMutesFail = true
	h.inbox.mu.Lock()
	h.inbox.items["e"] = mailItem("e", "shop@example.com", "Shop")
	h.inbox.mu.Unlock()
	// The same mute again: no write needed, so a new item it covers is still filed.
	if _, settled, err := h.addMute(ctx, mute); err != nil || settled != 1 {
		t.Fatalf("the existing mute with the store down: settled=%d err=%v", settled, err)
	}
	if h.inbox.items["e"].open() {
		t.Fatal("the existing mute did not file a newly covered item")
	}
	// Removing a mute that is not there writes nothing, so it reports success too.
	if err := h.removeMutes(ctx, func(m InboxMute) bool { return m.ID == "nonexistent" }); err != nil {
		t.Fatalf("removing nothing with the store down: %v", err)
	}
	if got := len(h.listedMutes()); got != 1 {
		t.Fatalf("the no-op removal changed the mutes: %d left", got)
	}
}

// Replacing a source's muted repositories while the settings store is down fails atomically: nothing is kept and
// nothing enters memory; the same request after recovery works.
func TestInboxRepoMutesPersistFailure(t *testing.T) {
	h, st := newMuteFailHub(t)
	ctx := context.Background()
	h.inbox.srcMu.Lock()
	h.inbox.names["github"] = "GitHub"
	h.inbox.srcMu.Unlock()
	p := assistantwire.InboxRepoMutes{Source: "github", Set: true, Repos: []string{"example-org/repo-a"}}

	st.putMutesFail = true
	if _, err := h.InboxRepoMutes(ctx, p); !errors.Is(err, errMuteStoreDown) {
		t.Fatalf("repo mutes with the store down: %v", err)
	}
	if got := len(h.listedMutes()); got != 0 {
		t.Fatalf("the failed request left %d mutes in memory", got)
	}
	if got := keptMutes(t, st); len(got) != 0 {
		t.Fatalf("the failed request kept %+v", got)
	}

	st.putMutesFail = false
	repos, err := h.InboxRepoMutes(ctx, p)
	if err != nil || !slices.Equal(repos, []string{"example-org/repo-a"}) {
		t.Fatalf("retry: %v %v", repos, err)
	}
	if got := keptMutes(t, st); len(got) != 1 {
		t.Fatalf("the retry kept %+v", got)
	}
}

// A source's muted repositories (GitHub's page lists and sets them as its muted_repos): set replaces them (the others
// unmuted), add only adds (the ones its options kept before, moved over), a repository needs its owner.
func TestInboxRepoMutes(t *testing.T) {
	h := muteHub(t)
	ctx := context.Background()
	h.inbox.srcMu.Lock()
	h.inbox.names["github"] = "GitHub"
	h.inbox.srcMu.Unlock()
	if repos, err := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github", Set: true, Add: true, Repos: []string{"Example-Org/Old-Site"}}); err != nil ||
		!slices.Equal(repos, []string{"Example-Org/Old-Site"}) {
		t.Fatalf("moved: %v %v", repos, err)
	}
	if got := h.listedMutes(); len(got) != 1 || got[0].Value != "example-org/old-site" || got[0].SourceName != "GitHub" || got[0].Tag != connector.TagRepo {
		t.Fatalf("mutes %+v", got)
	}
	if repos, _ := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github", Set: true, Add: true, Repos: []string{"example-org/old-site"}}); len(repos) != 1 {
		t.Fatalf("moved again: %v", repos)
	}
	if repos, err := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github", Set: true, Repos: []string{"example-org/new"}}); err != nil ||
		!slices.Equal(repos, []string{"example-org/new"}) {
		t.Fatalf("set: %v %v", repos, err)
	}
	if _, err := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github", Set: true, Repos: []string{"nope"}}); err == nil {
		t.Fatal("a repository without its owner was taken")
	}
	if repos, _ := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github"}); !slices.Equal(repos, []string{"example-org/new"}) {
		t.Fatalf("listed: %v", repos)
	}
}
