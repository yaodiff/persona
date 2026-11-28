package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

const (
	testLegacyThreadKey = "chat_example:topic_example"
	testTopicThreadKey  = "v2:topic:12:chat_example:topic_example"
)

func testDualThreadMessage() channel.Message {
	return channel.Message{Thread: testLegacyThreadKey, ChatID: "chat_example", ChatKind: channel.ChatKindTopic, TopicID: "topic_example"}
}

func TestThreadBindingLookupPriorityAndExpiry(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const missing = -1
	for _, tc := range []struct {
		name                     string
		v2                       bool
		legacyAge, structuredAge int // hours
		want                     string
	}{
		{"live primary wins over newer alias", true, 1, 2, "structured"},
		{"expired primary falls back", true, 1, 3, "legacy"},
		{"missing primary falls back", true, 1, missing, "legacy"},
		{"both expired", true, 3, 3, ""},
		{"legacy primary uses thirty days", false, 30 * 24, 1, "legacy"},
		{"expired legacy primary falls back", false, 30*24 + 1, 3, "structured"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s := newTestService(t, newFakeHub())
			s.setChannelTestClock(func() time.Time { return now })
			stored := map[string]*channelThread{}
			if tc.legacyAge != missing {
				stored[testLegacyThreadKey] = &channelThread{MachineID: "server", SessionID: "legacy", At: now.Add(-time.Duration(tc.legacyAge) * time.Hour)}
			}
			if tc.structuredAge != missing {
				stored[testTopicThreadKey] = &channelThread{MachineID: "server", SessionID: "structured", At: now.Add(-time.Duration(tc.structuredAge) * time.Hour)}
			}
			if err := s.store().PutSetting(ctx, channelsKey, map[string]channelPrefs{"examplechat": {ThreadIdleHours: 2, Threads: stored}}); err != nil {
				t.Fatal(err)
			}
			var index threadBindingIndex
			ref := index.resolve("examplechat", testDualThreadMessage(), channel.ThreadModePerTopic, tc.v2)
			var got string
			if binding := s.lookupThreadBinding(ctx, ref); binding != nil {
				got = binding.SessionID
			}
			if got != tc.want {
				t.Fatalf("binding = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestThreadBindingRefreshAndResetPersistence(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub())
	store := s.store().(*memStore)
	clock := &fakeClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	s.setChannelTestClock(clock.Now)
	var index threadBindingIndex
	legacy := index.resolve("examplechat", channel.Message{Thread: testLegacyThreadKey}, "", false)
	dual := testDualThreadMessage()
	structured := index.resolve("examplechat", dual, channel.ThreadModePerTopic, true)
	if !s.refreshThreadBinding(structured, "server", "first") {
		t.Fatal("current binding did not accept the first delivery")
	}
	if got := s.lookupThreadBinding(ctx, legacy); got != nil {
		t.Fatalf("the first structured delivery created a legacy binding: %+v", got)
	}

	clock.advance(time.Hour)
	now := clock.Now()
	if !s.refreshThreadBinding(index.resolve("examplechat", dual, "", false), "server", "second") {
		t.Fatal("current binding did not accept the second delivery")
	}
	var persisted map[string]channelPrefs
	if found, err := store.Setting(ctx, channelsKey, &persisted); !found || err != nil {
		t.Fatalf("read stored bindings: found=%t err=%v", found, err)
	}
	for _, key := range []string{testLegacyThreadKey, testTopicThreadKey} {
		if b := persisted["examplechat"].Threads[key]; b == nil || b.SessionID != "second" || !b.At.Equal(now) {
			t.Fatalf("stored binding %q = %+v, want second at %v", key, b, now)
		}
	}
	before, _ := store.rawSetting(channelsKey)
	failure := errors.New("synthetic binding write failure")
	store.setFailPut(failure)
	if err := s.resetThreadBinding(legacy); !errors.Is(err, failure) || !errors.Is(err, errChannelSettingsWrite) {
		t.Fatalf("reset error = %v, want channel settings write failure", err)
	}
	if got, _ := store.rawSetting(channelsKey); !bytes.Equal(got, before) {
		t.Fatal("failed reset changed persisted channel preferences")
	}
	for _, key := range []string{testLegacyThreadKey, testTopicThreadKey} {
		if got := boundThread(s, key); got != "second" {
			t.Errorf("failed reset changed the in-memory binding for %q to %q", key, got)
		}
	}
	store.setFailPut(nil)
	if err := s.resetThreadBinding(legacy); err != nil {
		t.Fatal(err)
	}
	if got := s.lookupThreadBinding(ctx, structured); got != nil {
		t.Fatalf("single-format reset left a live topic binding: %+v", got)
	}
}

func TestThreadBindingFollowsDeliveryAfterRefreshFailure(t *testing.T) {
	ctx := context.Background()
	store := &failPutKeyStore{memStore: &memStore{}, key: channelsKey, err: errors.New("synthetic refresh write failure")}
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	previous := now.Add(-time.Hour)
	if err := store.memStore.PutSetting(ctx, channelsKey, map[string]channelPrefs{"examplechat": {Threads: map[string]*channelThread{
		testLegacyThreadKey: {MachineID: "server", SessionID: "topic-session", At: previous},
		testTopicThreadKey:  {MachineID: "server", SessionID: "topic-session", At: previous},
	}}}); err != nil {
		t.Fatal(err)
	}
	s, f, hub := admissionChannelService(t, true, store)
	close(hub.release)
	registerAssistantSession(f, "topic-session")
	s.setChannelTestClock(func() time.Time { return now })

	m := testDualThreadMessage()
	m.ID, m.Text = "delivered", "Continue the topic"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
		t.Fatal(err)
	}
	waitAdmissionDrained(t, s)
	if got := lastSendOnceSession(f); got != "topic-session" {
		t.Errorf("delivered to %q, want topic-session", got)
	}
	var state channelsState
	if found, err := store.Setting(ctx, channelsStateKey, &state); !found || err != nil || len(state.Runs) != 1 {
		t.Fatalf("delivered turn was not persisted for following: found=%t runs=%d err=%v", found, len(state.Runs), err)
	}
	if got := state.Runs[0].SessionID; got != "topic-session" {
		t.Fatalf("following conversation %q, want topic-session", got)
	}
	for _, key := range []string{testLegacyThreadKey, testTopicThreadKey} {
		ref := s.storedThreadBindingForTest("examplechat", key, false)
		if got := s.lookupThreadBinding(ctx, ref); got == nil || !got.At.Equal(previous) {
			t.Errorf("failed refresh changed the in-memory binding for %q: %+v", key, got)
		}
	}
}

func TestThreadBindingQueuedNewKeepsAdmittedRoute(t *testing.T) {
	s, _, hub := admissionChannelService(t, true, nil)
	setIntegrationsV2ViaHTTP(t, s, true, false)
	var release sync.Once
	defer release.Do(func() { close(hub.release) })
	ctx := context.Background()
	receive := func(id, text string) {
		t.Helper()
		m := testDualThreadMessage()
		m.ID, m.Text, m.Thread = id, text, ""
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ChannelReceive(ctx, ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
			t.Fatal(err)
		}
	}
	receive("first", "First topic message")
	select {
	case <-hub.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first topic message never reached the node")
	}
	receive("reset", "/new Start this topic over")
	setIntegrationsV2ViaHTTP(t, s, false, true)
	release.Do(func() { close(hub.release) })
	waitAdmissionDrained(t, s)

	if got := boundThread(s, testTopicThreadKey); got != "s2" {
		t.Errorf("topic binding after the queued reset = %q, want the new topic conversation s2", got)
	}
	if rt, err := s.channelTarget(ctx, "examplechat"); err != nil || !rt.main {
		t.Errorf("queued topic reset changed the default target to %q: %v", rt.session.SessionID, err)
	}
}
