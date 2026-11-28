package assistant

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

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// Hold the first start outside the fake's lock, as a slow node would.
type threadAdmissionHub struct {
	*fakeHub
	starts    atomic.Int32
	entered   chan struct{}
	release   chan struct{}
	second    chan struct{}
	once      sync.Once
	failFirst bool
}

func (h *threadAdmissionHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceAssistantSend {
		switch h.starts.Add(1) {
		case 1:
			close(h.entered)
			select {
			case <-h.release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			if h.failFirst {
				return nil, errors.New("synthetic start failure")
			}
		case 2:
			h.once.Do(func() { close(h.second) })
		}
	}
	raw, err := h.fakeHub.DeviceCall(ctx, p)
	if p.Method == DeviceAssistantSend && err == nil {
		var r struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &r)
		h.fakeHub.mu.Lock()
		h.fakeHub.sessions[p.Device] = append(h.fakeHub.sessions[p.Device], proto.Session{SessionID: r.SessionID, MachineID: p.Device, Assistant: true})
		h.fakeHub.mu.Unlock()
	}
	return raw, err
}

func admissionChannelService(t *testing.T, threads bool, store Store) (*Service, *fakeHub, *threadAdmissionHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true})
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin, Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{MaxText: 4000, Threads: threads, Asks: channel.AsksNone}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	h := &threadAdmissionHub{fakeHub: f, entered: make(chan struct{}), release: make(chan struct{}), second: make(chan struct{})}
	if store == nil {
		store = &memStore{}
	}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return store }, Logf: testLogf(t)}))
	return s, f, h
}

func TestChannelReceiveConcurrentFirstThreadMessages(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receive := func(id string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]string{"id": id, "text": "synthetic request", "thread": "chat_example:root_example"})
		if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
			t.Fatal(err)
		}
	}
	receive("first")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	receive("second")
	// A second start while the first is unresolved proves the routing race. On a
	// serialized implementation the observation deadline expires instead.
	select {
	case <-h.second:
	case <-time.After(200 * time.Millisecond):
	}
	release.Do(func() { close(h.release) })
	waitUntil(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.sends)+len(f.sentOnce) >= 2 })
	if got := h.starts.Load(); got != 1 {
		t.Fatalf("same thread started %d conversations, want one start and one continuation", got)
	}
}

func TestChannelReceiveDualTopicKeepsOrderAcrossToggle(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receive := func(id, thread string) {
		t.Helper()
		m := channel.Message{ID: id, Text: "topic request", Thread: thread,
			ChatID: "oc_group", ChatKind: channel.ChatKindTopic, TopicID: "omt_topic"}
		raw, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
			t.Fatal(err)
		}
	}
	receive("first", "oc_group:omt_topic")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	setIntegrationsV2ViaHTTP(t, s, true, false)
	// The second message bridges the keys before a structured-only message follows it.
	receive("second", "oc_group:omt_topic")
	receive("third", "")
	select {
	case <-h.second:
	case <-time.After(200 * time.Millisecond):
	}
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.starts.Load() != 1 || len(f.sentOnce) != 2 {
		t.Fatalf("topic across toggle: starts=%d continuations=%d, want 1 and 2", h.starts.Load(), len(f.sentOnce))
	}
	for _, send := range f.sentOnce {
		if send["session_id"] != "s1" {
			t.Fatalf("continuation sent to %v, want s1", send["session_id"])
		}
	}
}

func receiveAdmissionMessage(t *testing.T, s *Service, id, text, thread string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"id": id, "text": text, "thread": thread, "forward": "synthetic preceding context", "quote": "synthetic quoted message"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
		t.Fatal(err)
	}
}

func waitAdmissionDrained(t *testing.T, s *Service) {
	t.Helper()
	waitUntil(t, func() bool { s.chans.mu.Lock(); defer s.chans.mu.Unlock(); return len(s.chans.bindings.pending) == 0 })
}

func TestChannelOrdinaryReceiptsReserveTheSameOrderedRoute(t *testing.T) {
	s, _, _ := admissionChannelService(t, true, nil)
	ctx := context.Background()
	first, err := s.admitChannelMessage(ctx, "examplechat", channel.Message{ID: "first"})
	if err != nil || first == nil {
		t.Fatalf("first ordinary receipt: %v %v", first, err)
	}
	second, err := s.admitChannelMessage(ctx, "examplechat", channel.Message{ID: "second", Thread: " "})
	if err != nil || second == nil || first.reservation.done == nil || second.reservation.slots[0].prev != first.reservation.done {
		t.Fatalf("the second ordinary receipt did not follow the first: %v %v", second, err)
	}
	topic, err := s.admitChannelMessage(ctx, "examplechat", channel.Message{ID: "topic", Thread: "independent"})
	if err != nil || topic == nil || topic.reservation.slots[0].prev != nil {
		t.Fatalf("an independent topic waited on the ordinary route: %v %v", topic, err)
	}
}

func TestChannelReceiveThreadNewFollowsInFlightStart(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receiveAdmissionMessage(t, s, "first", "first request", "topic")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	receiveAdmissionMessage(t, s, "new", "/new", "topic")
	receiveAdmissionMessage(t, s, "later", "later request", "topic")
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	bound := boundThread(s, "topic")
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.starts.Load() != 2 || len(f.sends) != 2 || len(f.sentOnce) != 0 {
		t.Fatalf("/new did not start the following request afresh: starts=%d sends=%d continuations=%d", h.starts.Load(), len(f.sends), len(f.sentOnce))
	}
	if bound != "s2" {
		t.Fatalf("binding after /new: %q, want second conversation", bound)
	}
}

func TestChannelReceiveOtherThreadDoesNotWait(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receiveAdmissionMessage(t, s, "first", "first request", "topic-a")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	receiveAdmissionMessage(t, s, "other", "independent request", "topic-b")
	select {
	case <-h.second:
	case <-time.After(5 * time.Second):
		t.Fatal("another topic waited for the blocked topic")
	}
	waitUntil(t, func() bool { return boundThread(s, "topic-b") != "" })
	receiveAdmissionMessage(t, s, "plain", "unthreaded request", "")
	waitUntil(t, func() bool { return lastSendOnceSession(f) == "m1" })
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	if boundThread(s, "topic-a") == boundThread(s, "topic-b") {
		t.Fatal("independent topics share a conversation")
	}
}

func TestChannelReceiveThreadStartFailureReleasesNext(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	h.failFirst = true
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receiveAdmissionMessage(t, s, "failed", "failed request", "topic")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	receiveAdmissionMessage(t, s, "retry", "next request", "topic")
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	if h.starts.Load() != 2 || f.sent() != 1 || boundThread(s, "topic") == "" {
		t.Fatalf("failed start held the next message: starts=%d successful=%d binding=%q", h.starts.Load(), f.sent(), boundThread(s, "topic"))
	}
}

func TestChannelReceiveThreadRoutingCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		threads bool
		thread  string
	}{
		{"no capability", false, "topic"}, {"no key", true, ""}, {"blank key", true, "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := threadsChannelService(t, tc.threads)
			receiveAdmissionMessage(t, s, "legacy", "original request", tc.thread)
			waitUntil(t, func() bool { return lastSendOnceSession(f) == "m1" })
			waitAdmissionDrained(t, s)
			if f.sent() != 0 || boundThread(s, "topic") != "" {
				t.Fatal("legacy input opened a thread conversation")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.sentOnce) != 1 {
				t.Fatalf("legacy input delivered %d times", len(f.sentOnce))
			}
			text, _ := f.sentOnce[0]["text"].(string)
			if !strings.Contains(text, "original request") || !strings.Contains(text, "synthetic preceding context") || !strings.Contains(text, "synthetic quoted message") {
				t.Fatalf("context missing from delivery: %q", text)
			}
		})
	}
}

func TestChannelReceiveUnbindDiscardsQueuedThreadMessage(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receiveAdmissionMessage(t, s, "first", "in flight", "topic")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	receiveAdmissionMessage(t, s, "old", "queued before unbind", "topic")
	if err := s.ChannelUnbound(context.Background(), ChannelUnbound{Channel: "examplechat"}); err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.starts.Load() != 1 || len(f.sentOnce) != 0 {
		t.Fatalf("queued old input delivered after unbind: starts=%d continuations=%d", h.starts.Load(), len(f.sentOnce))
	}
}

func TestChannelReceiveSameThreadKeyInOtherChannelDoesNotWait(t *testing.T) {
	s, f, h := admissionChannelService(t, true, nil)
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		var views []channel.View
		for _, id := range []string{"examplechat", "otherchat"} {
			views = append(views, channel.View{ID: id, Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin, Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Threads: true, Asks: channel.AsksNone}})
		}
		return map[string]any{"channels": views}, nil
	})
	s.forgetChannelList()
	var release sync.Once
	defer release.Do(func() { close(h.release) })
	receiveAdmissionMessage(t, s, "first", "first channel request", "same-key")
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	raw, _ := json.Marshal(map[string]string{"id": "other", "text": "another channel request", "thread": "same-key"})
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "otherchat", Message: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-h.second:
	case <-time.After(5 * time.Second):
		t.Fatal("another channel waited for the blocked channel")
	}
	waitUntil(t, func() bool { return boundThreadFor(s, "otherchat", "same-key") != "" })
	release.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	other := boundThreadFor(s, "otherchat", "same-key")
	if other == "" || other == boundThread(s, "same-key") {
		t.Fatal("channels with equal thread keys share a conversation")
	}
}

type admissionClaimStore struct {
	Store
	once     sync.Once
	observed chan bool
	release  chan struct{}
	s        *Service
}

func (st *admissionClaimStore) PutSetting(ctx context.Context, key string, value any) error {
	if state, ok := value.(channelsState); ok && key == channelsStateKey && slices.Contains(state.Seen["examplechat"], "first") {
		st.once.Do(func() {
			// saveChannelStateLocked holds the book lock: the durable claim must already have its queue position.
			st.observed <- len(st.s.chans.bindings.pending) == 1
			<-st.release
		})
	}
	return st.Store.PutSetting(ctx, key, value)
}

func TestChannelReceiveClaimReservesBeforePersistence(t *testing.T) {
	st := &admissionClaimStore{Store: &memStore{}, observed: make(chan bool, 1), release: make(chan struct{})}
	s, _, h := admissionChannelService(t, true, st)
	st.s = s
	var claimRelease, startRelease sync.Once
	defer claimRelease.Do(func() { close(st.release) })
	defer startRelease.Do(func() { close(h.release) })
	receive := func(id, text string) <-chan error {
		done := make(chan error, 1)
		go func() {
			raw, _ := json.Marshal(map[string]string{"id": id, "text": text, "thread": "topic"})
			done <- s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw})
		}()
		return done
	}
	first := receive("first", "first request")
	select {
	case reserved := <-st.observed:
		if !reserved {
			t.Error("seen claim persisted before thread admission was reserved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("claim never reached persistence")
	}
	later := receive("reset", "/new")
	claimRelease.Do(func() { close(st.release) })
	for _, done := range []<-chan error{first, later} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("receipt blocked after claim persistence")
		}
	}
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first request never started")
	}
	startRelease.Do(func() { close(h.release) })
	waitAdmissionDrained(t, s)
	if got := boundThread(s, "topic"); got != "" {
		t.Fatalf("later /new was overtaken by the accepted request: %q", got)
	}
}

func TestChannelReceiveFailedClaimCanBeRedelivered(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	st := s.store().(*memStore)
	st.setFailPut(errors.New("synthetic claim write failure"))
	raw := []byte(`{"id":"retryable","text":"request","thread":"topic"}`)
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err == nil {
		t.Fatal("failed durable claim was acknowledged")
	}
	s.chans.mu.Lock()
	reserved := len(s.chans.bindings.pending)
	seen := slices.Contains(s.chans.st.Seen["examplechat"], "retryable")
	s.chans.mu.Unlock()
	if reserved != 0 || seen || f.sent() != 0 {
		t.Fatalf("failed claim left admission state: reserved=%d seen=%v starts=%d", reserved, seen, f.sent())
	}
	st.setFailPut(nil)
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
		t.Fatal(err)
	}
	waitAdmissionDrained(t, s)
	if err := s.ChannelReceive(context.Background(), ChannelReceive{Channel: "examplechat", Message: raw}); err != nil {
		t.Fatal(err)
	}
	waitAdmissionDrained(t, s)
	if f.sent() != 1 || boundThread(s, "topic") == "" {
		t.Fatal("redelivery did not start exactly one conversation")
	}
}

func TestChannelReceiveOldStartCannotBindNewGeneration(t *testing.T) {
	for _, threads := range []bool{true, false} {
		t.Run(fmt.Sprintf("threads=%v", threads), func(t *testing.T) {
			s, _, h := admissionChannelService(t, threads, nil)
			if !threads {
				s.chans.mu.Lock()
				s.loadChannelsLocked(context.Background())
				err := s.saveChannelsLocked("examplechat", channelPrefs{Target: &channel.Target{Kind: channel.TargetNew}})
				s.chans.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			var release sync.Once
			defer release.Do(func() { close(h.release) })
			receiveAdmissionMessage(t, s, "old", "old account request", "same-key")
			select {
			case <-h.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("old start never reached node")
			}
			if err := s.ChannelUnbound(context.Background(), ChannelUnbound{Channel: "examplechat"}); err != nil {
				t.Fatal(err)
			}
			receiveAdmissionMessage(t, s, "fresh", "new account request", "same-key")
			select {
			case <-h.second:
			case <-time.After(5 * time.Second):
				t.Fatal("new generation waited for the old node RPC")
			}
			binding := func() string {
				if threads {
					return boundThread(s, "same-key")
				}
				s.chans.mu.Lock()
				defer s.chans.mu.Unlock()
				target := s.channelPrefLocked("examplechat").Target
				if target != nil && target.Kind == channel.TargetConversation {
					return target.SessionID
				}
				return ""
			}
			waitUntil(t, func() bool { return binding() != "" })
			fresh := binding()
			release.Do(func() { close(h.release) })
			waitAdmissionDrained(t, s)
			if got := binding(); got != fresh {
				t.Fatalf("late old start replaced the new binding: got %q want %q", got, fresh)
			}
			runs := s.channelRuns("examplechat")
			if len(runs) != 1 || runs[0].SessionID != fresh || runs[0].ReplyTo != "fresh" {
				t.Fatalf("late old start restored channel run state: %+v", runs)
			}
		})
	}
}
