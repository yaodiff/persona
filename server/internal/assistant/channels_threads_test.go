package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// threadsChannelService is a connected IM plugin channel that keeps a conversation per thread (capabilities.threads)
// when threads is true, and an existing main conversation. Replies and notes sent to the channel are recorded in sent.
func threadsChannelService(t *testing.T, threads bool) (*Service, *fakeHub, *[]map[string]any) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true})
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM,
			Source: channel.SourcePlugin, Installed: true, State: channel.StateConnected,
			Capabilities: channel.Capabilities{MaxText: 4000, Threads: threads}}}}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m) // under the fake's lock
		return map[string]any{}, nil
	})
	return newTestService(t, f), f, &sent
}

// lastSendOnceSession is the conversation the latest session.send_once went to ("" when there was none).
func lastSendOnceSession(f *fakeHub) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sentOnce) == 0 {
		return ""
	}
	id, _ := f.sentOnce[len(f.sentOnce)-1]["session_id"].(string)
	return id
}

// boundThread is the conversation thread is bound to now ("" when none).
func boundThread(s *Service, thread string) string {
	return boundThreadFor(s, "examplechat", thread)
}

func (s *Service) setChannelTestClock(now func() time.Time) {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	s.chans.now = now
}

func (s *Service) threadBindingForTest(ctx context.Context, id string, m channel.Message) (threadBindingRef, channel.View, error) {
	v, _ := s.pluginChannel(ctx, id)
	v2 := s.integrationsV2On(ctx)
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	binding, err := s.resolveThreadBindingLocked(ctx, id, m, v, v2)
	return binding, v, err
}

func (s *Service) channelReceive(id string, m channel.Message, action string) {
	binding, _, err := s.threadBindingForTest(context.Background(), id, m)
	if err != nil {
		panic(err)
	}
	s.receiveChannelMessage(binding, m, action)
}

func (s *Service) channelForward(ctx context.Context, id string, m channel.Message) error {
	binding, v, err := s.threadBindingForTest(ctx, id, m)
	if err != nil {
		return err
	}
	return s.forwardChannelMessage(ctx, binding, v, m)
}

func (s *Service) channelCommand(ctx context.Context, id string, m channel.Message, text string) (bool, string) {
	binding, v, err := s.threadBindingForTest(ctx, id, m)
	if err != nil {
		panic(err)
	}
	return s.handleChannelCommand(ctx, binding, m.ID, text, func() channel.View { return v })
}

func (s *Service) storedThreadBindingForTest(id, key string, v2 bool) threadBindingRef {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	return threadBindingRef{channel: id, generation: s.chans.bindings.generation(id), primary: key, v2: v2}
}

func (s *Service) seedThreadBinding(id, key, machine, session string) bool {
	return s.refreshThreadBinding(s.storedThreadBindingForTest(id, key, false), machine, session)
}

// A thread's first message starts a conversation of its own and later ones continue it; another thread gets another,
// and a message without a thread still goes to the channel's target, which the threads never move (#11939).
func TestChannelThreadsGetConversationsOfTheirOwn(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	ctx := context.Background()
	forward := func(m channel.Message) {
		t.Helper()
		if err := s.channelForward(ctx, "examplechat", m); err != nil {
			t.Fatalf("%s: %v", m.ID, err)
		}
	}

	// Raw JSON, as the extension sends it: a host that drops the field would send this to the main chat.
	var m channel.Message
	if err := json.Unmarshal([]byte(`{"id":"om_1","text":"first topic","thread":"chat_a:om_1"}`), &m); err != nil {
		t.Fatal(err)
	}
	forward(m)
	if n := f.sent(); n != 1 {
		t.Fatalf("a thread's first message started %d conversations, want 1", n)
	}
	first := boundThread(s, "chat_a:om_1")
	if first == "" || first == "m1" {
		t.Fatalf("the thread is bound to %q, want a new conversation", first)
	}
	if main := s.settings(ctx).Main; main != "m1" {
		t.Fatalf("a thread's conversation became the main one: %q", main)
	}
	if rt, err := s.channelTarget(ctx, "examplechat"); err != nil || !rt.main || rt.session.SessionID != "m1" {
		t.Fatalf("a thread moved the channel's target: %+v %v", rt, err)
	}

	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: first, MachineID: "server", Assistant: true})
	forward(channel.Message{ID: "om_2", Text: "in the first topic again", Thread: "chat_a:om_1"})
	if n := f.sent(); n != 1 || lastSendOnceSession(f) != first {
		t.Fatalf("the thread's next message: %d starts, sent to %q, want its conversation %q", n, lastSendOnceSession(f), first)
	}

	forward(channel.Message{ID: "om_3", Text: "second topic", Thread: "chat_a:om_3"})
	second := boundThread(s, "chat_a:om_3")
	if n := f.sent(); n != 2 || second == "" || second == first {
		t.Fatalf("another thread: %d starts, bound to %q (the first is %q)", n, second, first)
	}

	forward(channel.Message{ID: "om_4", Text: "no thread"})
	if n := f.sent(); n != 2 || lastSendOnceSession(f) != "m1" {
		t.Fatalf("a message without a thread: %d starts, sent to %q, want the main chat", n, lastSendOnceSession(f))
	}
	// om_2 folds into om_1's run (the same conversation, still answering); the other conversations have runs of their own.
	runs := s.channelRuns("examplechat")
	sessions := map[string]bool{}
	for _, r := range runs {
		sessions[r.SessionID] = true
	}
	if len(runs) != 3 || !sessions[first] || !sessions[second] || !sessions["m1"] {
		t.Fatalf("one run per conversation: %+v", runs)
	}
}

// Without capabilities.threads the key means nothing: the message goes to the channel's target as before.
func TestChannelThreadIgnoredWithoutCapability(t *testing.T) {
	s, f, _ := threadsChannelService(t, false)
	if err := s.channelForward(context.Background(), "examplechat", channel.Message{ID: "om_1", Text: "hi", Thread: "chat_a:om_1"}); err != nil {
		t.Fatal(err)
	}
	if n := f.sent(); n != 0 || lastSendOnceSession(f) != "m1" || boundThread(s, "chat_a:om_1") != "" {
		t.Fatalf("%d starts, sent to %q, bound %q: want the main chat and no binding", n, lastSendOnceSession(f), boundThread(s, "chat_a:om_1"))
	}
}

// A thread whose conversation is gone starts a new one and is bound to it.
func TestChannelThreadWhoseConversationIsGoneStartsAnew(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	s.seedThreadBinding("examplechat", "chat_a:om_1", "server", "deleted")
	if err := s.channelForward(context.Background(), "examplechat", channel.Message{ID: "om_2", Text: "hi", Thread: "chat_a:om_1"}); err != nil {
		t.Fatal(err)
	}
	if got := boundThread(s, "chat_a:om_1"); f.sent() != 1 || got == "" || got == "deleted" {
		t.Fatalf("%d starts, bound to %q: want a new conversation", f.sent(), got)
	}
}

// A thread whose conversation can't be looked up now (its computer doesn't answer) is an error, not a new conversation:
// the thread keeps its history for when the computer is back.
func TestChannelThreadUnreachableKeepsItsConversation(t *testing.T) {
	s, f, _ := threadsChannelService(t, true)
	s.seedThreadBinding("examplechat", "chat_a:om_1", "server", "s_topic")
	f.answerNode(DeviceSessionGet, func(string, json.RawMessage) (any, error) { return nil, errors.New("the computer did not answer") })
	if err := s.channelForward(context.Background(), "examplechat", channel.Message{ID: "om_2", Text: "hi", Thread: "chat_a:om_1"}); err == nil {
		t.Fatal("a thread whose conversation could not be looked up was sent on")
	}
	if got := boundThread(s, "chat_a:om_1"); f.sent() != 0 || got != "s_topic" {
		t.Fatalf("%d starts, bound to %q: want none and the thread's conversation kept", f.sent(), got)
	}
}

// In a thread, /new starts the thread over without touching the channel's target, /status says where the thread's
// next message goes, and /stop stops the thread's conversation, not the target's.
func TestChannelThreadCommands(t *testing.T) {
	s, f, sent := threadsChannelService(t, true)
	ctx := context.Background()
	say := func(key string) string { return i18n.T(s.botLang(ctx), "assistant-channels_plugin."+key) }
	s.seedThreadBinding("examplechat", "chat_a:om_1", "server", "s_topic")
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "s_topic", MachineID: "server", Assistant: true})
	notes := func() string {
		var b strings.Builder
		for _, n := range *sent {
			fmt.Fprintln(&b, n["text"])
		}
		*sent = nil
		return b.String()
	}

	s.channelReceive("examplechat", channel.Message{ID: "om_2", Text: "/stop", Thread: "chat_a:om_1"}, "")
	if !strings.Contains(notes(), say("stopped")) {
		t.Fatal("/stop in a thread did not stop its conversation")
	}
	f.mu.Lock()
	var stopped string
	for _, c := range f.calls {
		if c.Method == "session.interrupt" {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			stopped, _ = p["session_id"].(string)
		}
	}
	f.mu.Unlock()
	if stopped != "s_topic" {
		t.Fatalf("/stop in a thread interrupted %q, want the thread's conversation", stopped)
	}

	s.channelReceive("examplechat", channel.Message{ID: "om_3", Text: "/new", Thread: "chat_a:om_1"}, "")
	if got := notes(); !strings.Contains(got, say("thread_next_is_new")) {
		t.Fatalf("/new in a thread said %q", got)
	}
	if boundThread(s, "chat_a:om_1") != "" {
		t.Fatal("/new left the thread bound")
	}
	if rt, err := s.channelTarget(ctx, "examplechat"); err != nil || !rt.main {
		t.Fatalf("/new in a thread moved the channel's target: %+v %v", rt, err)
	}

	s.channelReceive("examplechat", channel.Message{ID: "om_4", Text: "/status", Thread: "chat_a:om_1"}, "")
	if got := notes(); !strings.Contains(got, say("next_new_chat")) {
		t.Fatalf("/status in a started-over thread said %q", got)
	}

	s.channelReceive("examplechat", channel.Message{ID: "om_5", Text: "/new start again", Thread: "chat_a:om_1"}, "")
	if got := boundThread(s, "chat_a:om_1"); f.sent() != 1 || got == "" || got == "s_topic" {
		t.Fatalf("/new with text in a thread: %d starts, bound to %q", f.sent(), got)
	}
	if text, _ := f.lastSend(t)["text"].(string); strings.Contains(text, "/new") || !strings.Contains(text, "start again") {
		t.Fatalf("the new conversation started with %q", text)
	}
}

// Bindings quiet for longer than channelThreadsKeepFor go, then the oldest beyond channelThreadsKeep; a stale one is
// not followed even before it is pruned.
func TestChannelThreadsPruned(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	threads := map[string]*channelThread{"stale": {SessionID: "s0", At: now.Add(-channelThreadsKeepFor - time.Hour)}}
	for i := range channelThreadsKeep + 2 {
		threads[fmt.Sprintf("t%d", i)] = &channelThread{SessionID: fmt.Sprintf("s%d", i), At: now.Add(time.Duration(i) * time.Minute)}
	}
	pruneThreadBindings(threads, now, channelThreadsKeepFor)
	if len(threads) != channelThreadsKeep || threads["stale"] != nil || threads["t0"] != nil || threads["t1"] != nil || threads["t2"] == nil {
		t.Fatalf("%d kept; stale %v, t0 %v, t1 %v, t2 %v", len(threads), threads["stale"], threads["t0"], threads["t1"], threads["t2"])
	}

	s, _, _ := threadsChannelService(t, true)
	s.setChannelTestClock(func() time.Time { return now })
	s.seedThreadBinding("examplechat", "old", "server", "s_old")
	s.setChannelTestClock(func() time.Time { return now.Add(channelThreadsKeepFor + time.Minute) })
	if boundThread(s, "old") != "" {
		t.Fatal("a thread quiet for longer than it is kept still has its conversation")
	}
}
