package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// A refused final edit leaves the progress card's original Stop button visible.
// Its callback must not stop a later turn in the same conversation.
func TestFeishuStaleStopAfterRefusedFinalEdit(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, posts := endHub(t)
	ctx := context.Background()
	f.setSessions("server", proto.Session{MachineID: "server", SessionID: "c1", Assistant: true, State: proto.StateWorking, Status: proto.StatusWorking})
	old := r.Ext["card"]
	if old == "" || !strings.Contains((*posts)[0]["card"].(string), `"a":"stop"`) {
		t.Fatal("normal progress did not send a Stop card")
	}
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return nil, feishuAPIError(230001) })
	before := len(*posts)
	s.finishChannelRun(r, &channel.Result{Answer: "Synthetic final answer"})
	if current, _ := s.channelRunByID(r.ID); current != nil {
		t.Fatal("finished run remains current")
	}
	if len(*posts) != before+1 || !strings.Contains((*posts)[before]["card"].(string), "Synthetic final answer") {
		t.Fatalf("answer fallback did not post: %v", *posts)
	}
	next, _ := cotRun(s)
	next.ID = "fb_new"
	s.chans.mu.Lock()
	s.chans.live = map[string]*chanLive{next.ID: {mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}}
	s.chans.mu.Unlock()
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		*posts = append(*posts, m)
		return map[string]any{"message_id": "om_current"}, nil
	})
	feishuSink{s}.progress(ctx, next, time.Second, nil, "")
	if next.Ext["card"] != "om_current" {
		t.Fatal("new progress message was not recorded")
	}
	a := stopCardAction(botTargetOf(r))
	a.Context.MessageID = old
	result := s.larkModule().Action(ctx, a, "")
	if got := f.interrupted(); len(got) != 0 {
		t.Fatalf("stale progress card interrupted the newer working turn: %v (toast %+v)", got, result.Toast)
	}
	a = stopCardAction(botTargetOf(next))
	a.Context.MessageID = "om_current"
	result = s.larkModule().Action(ctx, a, "")
	if result.Toast == nil || result.Toast.Type != "success" {
		t.Fatalf("current progress card rejected: %+v", result)
	}
	s.larkModule().Action(ctx, a, "") // redelivery of the current card is harmless too
	if got := f.interrupted(); len(got) != 1 || got[0] != "server/c1" {
		t.Fatalf("current card interrupts %v", got)
	}
}

// Keep callbacks in the same old wire shape: no new run identifier is required.
func staleStopAction(target botTarget, mid string) *botcard.CardAction {
	a := stopCardAction(target)
	a.Context.MessageID = mid
	return a
}

// currentStopCard registers the watched ownership that a normal progress send keeps.
func currentStopCard(s *Service, target botTarget) *botcard.CardAction {
	r := &channel.Run{ID: "fb_stop", Channel: channel.Feishu, MachineID: target.MachineID, SessionID: target.SessionID, TargetKind: target.Kind, Ext: map[string]string{"card": "om_stop"}}
	p := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	if s.chans.live == nil {
		s.chans.live = map[string]*chanLive{}
	}
	s.chans.live[r.ID] = p
	s.chans.mu.Unlock()
	return staleStopAction(botTargetOf(r), r.Ext["card"])
}

func TestFeishuStaleStopOwnership(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, name := range []string{"current", "missing message", "unknown message", "missing target", "wrong machine", "wrong session", "wrong kind", "unknown kind", "wrong channel", "entity only", "unwatched", "removed", "recalled", "finished", "answering", "answer retry", "held waiting", "held continuation", "finished turn"} {
		t.Run(name, func(t *testing.T) {
			s, f := stopCardHub(t, FeishuFeatures{Main: true, Agents: true})
			target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}
			a := currentStopCard(s, target)
			r, p := s.channelRunByID("fb_stop")
			sess := proto.Session{MachineID: "server", SessionID: "conv0001", Assistant: true, State: proto.StateWorking, Status: proto.StatusWorking}
			switch name {
			case "missing message":
				a.Context.MessageID = ""
			case "unknown message":
				a.Context.MessageID = "om_unknown"
			case "missing target":
				a.Action.Value["s"] = ""
			case "wrong machine":
				a.Action.Value["m"] = "elsewhere"
			case "wrong session":
				a.Action.Value["s"] = "another"
			case "wrong kind":
				a.Action.Value["k"] = targetAgent
			case "unknown kind":
				a.Action.Value["k"] = "unknown"
			case "wrong channel":
				r.Channel = "example"
			case "entity only":
				r.Ext = map[string]string{"card_id": "om_stop"}
			case "unwatched":
				delete(s.chans.live, r.ID)
			case "removed":
				s.chans.st.Runs = nil
			case "recalled":
				r.Recalled = true
			case "finished":
				p.finished = true
			case "answering":
				s.chans.answering = map[string]bool{r.ID: true}
			case "answer retry":
				p.failing = time.Now()
			case "held waiting":
				r.Held = true
				sess.State = proto.StateIdle
				sess.Status = proto.StatusIdle
			case "held continuation":
				r.Held = true
				r.Seen = true
			case "finished turn":
				r.Seen = true
				sess.State = proto.StateIdle
				sess.Status = proto.StatusIdle
			}
			f.setSessions("server", sess)
			out := s.larkModule().Action(context.Background(), a, "")
			want := 0
			if name == "current" || name == "held continuation" {
				want = 1
			}
			if got := len(f.interrupted()); got != want {
				t.Fatalf("interrupts=%d want %d; toast=%+v", got, want, out.Toast)
			}
			if out.Toast == nil || (out.Toast.Type == "success") != (want == 1) {
				t.Fatalf("toast=%+v", out.Toast)
			}
		})
	}
}

// A lookup can yield while lifecycle state changes. Even an identical run ID and
// card string on a replacement object must not authorize the original callback.
func TestFeishuStaleStopRechecksAfterResolution(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, name := range []string{"remove", "replace run", "replace live", "replace card", "answer starts", "recall", "finish"} {
		t.Run(name, func(t *testing.T) {
			s, f := stopCardHub(t, FeishuFeatures{Main: true})
			a := currentStopCard(s, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"})
			r, p := s.channelRunByID("fb_stop")
			f.beforeSessionGet = func() {
				p.mu.Lock()
				defer p.mu.Unlock()
				s.chans.mu.Lock()
				defer s.chans.mu.Unlock()
				switch name {
				case "remove":
					s.chans.st.Runs = nil
				case "replace run":
					replacement := *r
					replacement.Ext = cloneExt(r.Ext)
					s.chans.st.Runs = []*channel.Run{&replacement}
				case "replace live":
					s.chans.live[r.ID] = &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
				case "replace card":
					r.Ext["card"] = "om_replacement"
				case "answer starts":
					s.chans.answering = map[string]bool{r.ID: true}
				case "recall":
					r.Recalled = true
				case "finish":
					p.finished = true
				}
			}
			out := s.larkModule().Action(context.Background(), a, "")
			if got := f.interrupted(); len(got) != 0 {
				t.Fatalf("changed owner interrupted %v; toast=%+v", got, out.Toast)
			}
		})
	}
}

func TestFeishuStaleStopConcurrentCurrentPresses(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	a := currentStopCard(s, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"})
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; s.larkModule().Action(context.Background(), a, "") }()
	}
	close(start)
	wg.Wait()
	if got := f.interrupted(); len(got) != 1 {
		t.Fatalf("concurrent presses interrupted %v", got)
	}
}

func TestFeishuStopHoldsProgressLockDuringInterrupt(t *testing.T) {
	needsLarkLayer(t)
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	a := currentStopCard(s, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"})
	_, prog := s.channelRunByID("fb_stop")
	f.answerNode("session.interrupt", func(string, json.RawMessage) (any, error) {
		if prog.mu.TryLock() {
			prog.mu.Unlock()
			t.Error("progress lock was released before interrupt delivery")
		}
		if s.chans.mu.TryLock() {
			s.chans.mu.Unlock()
		} else {
			t.Error("channel state lock was held during interrupt delivery")
		}
		return nil, nil
	})
	out := s.larkModule().Action(t.Context(), a, "")
	if out.Toast == nil || out.Toast.Type != "success" || len(f.interrupted()) != 1 {
		t.Fatalf("current card did not interrupt once: %+v / %v", out.Toast, f.interrupted())
	}
}

// stopWatcherHub gates only the first device list after this test's message send returned. Other device lists before
// that send belong to routing, not the run watcher.
type stopWatcherHub struct {
	*fakeHub
	sent     atomic.Bool
	watching chan struct{}
	release  chan struct{}
	once     sync.Once
	hold     bool
}

func (h *stopWatcherHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Device == DeviceAll && p.Method == DeviceDevicesList && h.sent.Load() {
		h.once.Do(func() { close(h.watching) })
		if h.hold {
			<-h.release
		}
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	raw, err := h.fakeHub.DeviceCall(ctx, p)
	if p.Method == DeviceSessionSendOnce && err == nil {
		h.sent.Store(true)
	}
	return raw, err
}

// A test that posts progress directly can beat the asynchronous watcher's initial devices.list. Exercise that exact
// ordering: the unregistered card cannot interrupt; after registration the same current card acts once. Production's
// progress tick already requires the watcher, so this preserves its ownership guard rather than adding a bypass.
func TestFeishuStopBeforeTheRunWatcher(t *testing.T) {
	needsLarkLayer(t)
	delivered := make(chan struct{})
	testFeishuStopBeforeTheRunWatcher(t, delivered, nil)
	<-delivered
}

// holdDelivery is non-nil only for the early-failure regression: delivery waits
// before calling the real e.deliver, so only cleanup can release it.
func testFeishuStopBeforeTheRunWatcher(t *testing.T, delivered chan struct{}, holdDelivery chan struct{}) {
	f := newFakeHub().withMachine()
	h := &stopWatcherHub{fakeHub: f, watching: make(chan struct{}), release: make(chan struct{}), hold: holdDelivery != nil}
	s := withSettings(t, f, New(Options{Hub: h, Logf: testLogf(t)}))
	e := withFeishuThread(&todoEnv{t: t, s: s, f: f})
	e.topicTodo("发版")
	go func() {
		defer close(delivered)
		if holdDelivery != nil {
			<-holdDelivery
		}
		e.deliver(topicEvent("om_watched", "开始"))
	}()
	defer func() {
		select {
		case <-h.release:
		default:
			close(h.release)
		}
		if holdDelivery != nil {
			close(holdDelivery)
		}
		select {
		case <-delivered:
			if holdDelivery != nil {
				fmt.Fprintln(os.Stderr, "STOP_WATCHER_DELIVERY_RELEASED")
			}
		case <-time.After(5 * time.Second):
			t.Error("topic delivery did not finish after the watcher was released")
		}
	}()
	select {
	case <-h.watching:
	case <-time.After(5 * time.Second):
		t.Fatal("run watcher did not start its initial devices.list")
	}
	ctx := context.Background()
	runs := e.s.channelRuns(channel.Feishu)
	if len(runs) != 1 {
		t.Fatalf("topic runs = %d, want 1", len(runs))
	}
	r, prog := e.s.channelRunByID(runs[0].ID)
	if r == nil || prog != nil {
		t.Fatalf("run=%v watcher=%v before its initial devices.list returned", r != nil, prog != nil)
	}
	feishuSink{e.s}.progress(ctx, r, time.Second, nil, "")
	if r.Ext["card"] == "" {
		t.Fatal("progress was not posted")
	}
	a := staleStopAction(botTargetOf(r), r.Ext["card"])
	out := e.s.larkModule().Action(ctx, a, "")
	wantExpired := i18n.T(e.s.botLang(ctx), "assistant-feishu_cards.card_expired")
	if out.Toast == nil || out.Toast.Content != wantExpired || len(e.f.interrupted()) != 0 {
		t.Fatalf("unwatched current card acted: %+v / %v", out.Toast, e.f.interrupted())
	}
	close(h.release)
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("topic delivery did not finish after the watcher was released")
	}
	waitFor(t, "registered topic run watcher", func() bool { _, prog := e.s.channelRunByID(r.ID); return prog != nil })
	out = e.s.larkModule().Action(ctx, a, "")
	if out.Toast == nil || out.Toast.Type != "success" || len(e.f.interrupted()) != 1 {
		t.Fatalf("watched current card did not interrupt once: %+v / %v", out.Toast, e.f.interrupted())
	}
}

// The failing subtest has its own process. Cleanup writes a completion marker
// only after releasing and joining the real delivery; the old cleanup order never
// runs, so a matching timeout without that marker fails this regression.
func TestFeishuStopWatcherTimeoutReleasesDelivery(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	const childEnv = "PERSONA_TEST_STOP_WATCHER_TIMEOUT"
	if os.Getenv(childEnv) == "1" {
		testFeishuStopBeforeTheRunWatcher(t, make(chan struct{}), make(chan struct{}))
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestFeishuStopWatcherTimeoutReleasesDelivery$", "-test.count=1", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), childEnv+"=1")
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("watcher-start timeout did not fail: %s", output)
	}
	for _, want := range []string{"run watcher did not start its initial devices.list", "--- FAIL: TestFeishuStopWatcherTimeoutReleasesDelivery", "STOP_WATCHER_DELIVERY_RELEASED"} {
		if !strings.Contains(string(output), want) {
			t.Fatalf("child output missing %q: %s", want, output)
		}
	}
	for _, unwanted := range []string{"topic delivery did not finish", "goroutine after test has completed", "test timed out"} {
		if strings.Contains(string(output), unwanted) {
			t.Fatalf("delivery was not released by the early-timeout cleanup: %s", output)
		}
	}
}

func TestFeishuStaleStopFailedInterruptCanRetry(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	a := currentStopCard(s, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"})
	f.failInterrupt = errors.New("synthetic node refusal")
	if out := s.larkModule().Action(context.Background(), a, ""); out.Toast == nil || out.Toast.Type != "error" {
		t.Fatalf("failure=%+v", out.Toast)
	}
	f.mu.Lock()
	f.failInterrupt = nil
	f.mu.Unlock()
	if out := s.larkModule().Action(context.Background(), a, ""); out.Toast == nil || out.Toast.Type != "success" {
		t.Fatalf("retry=%+v", out.Toast)
	}
	s.larkModule().Action(context.Background(), a, "")
	if got := f.interrupted(); len(got) != 2 {
		t.Fatalf("want one failed attempt and one successful attempt, got %v", got)
	}
}

// Stored historical routing is not restored by pressing a card. Only after the
// ordinary channel-state load and watcher restore can its current message act.
func TestFeishuStaleStopRestoredOwnership(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	s, f := stopCardHub(t, FeishuFeatures{Main: true})
	target := botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}
	r := &channel.Run{ID: "fb_restored", Channel: channel.Feishu, MachineID: target.MachineID, SessionID: target.SessionID, Ext: map[string]string{"card": "om_restored"}, Started: time.Now()}
	if err := s.store().PutSetting(context.Background(), channelsStateKey, channelsState{Runs: []*channel.Run{r}}); err != nil {
		t.Fatal(err)
	}
	s.chans.mu.Lock()
	s.chans.stLoaded = false
	s.chans.st.Runs = nil
	s.chans.mu.Unlock()
	a := staleStopAction(target, "om_restored")
	s.larkModule().Action(context.Background(), a, "")
	if s.chans.stLoaded || len(f.interrupted()) != 0 {
		t.Fatal("callback restored historical state")
	}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	r = s.chans.st.Runs[0]
	s.chans.mu.Unlock()
	s.larkModule().Action(context.Background(), a, "")
	if len(f.interrupted()) != 0 {
		t.Fatal("unwatched stored run acted")
	}
	done := make(chan struct{})
	go func() { defer close(done); s.watchChannelRun(r) }()
	waitFor(t, "restored watcher", func() bool { _, p := s.channelRunByID(r.ID); return p != nil })
	_, p := s.channelRunByID(r.ID)
	defer func() {
		close(p.done)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("restored watcher did not exit")
		}
	}()
	out := s.larkModule().Action(context.Background(), a, "")
	if out.Toast == nil || out.Toast.Type != "success" || len(f.interrupted()) != 1 {
		t.Fatalf("restored current owner: %+v / %v", out.Toast, f.interrupted())
	}
}

// Hold keeps the same run object but gives its answered card back. A later
// continuation gets a new message and its own one-use Stop control.
func TestFeishuStaleStopAcrossHeldContinuation(t *testing.T) {
	needsLarkLayer(t)
	s, f, r, _ := endHub(t)
	ctx := context.Background()
	f.setSessions("server", proto.Session{MachineID: "server", SessionID: r.SessionID, Assistant: true, State: proto.StateWorking, Status: proto.StatusWorking})
	old := r.Ext["card"]
	s.finishChannelRun(r, &channel.Result{Answer: "Synthetic initial answer", Held: true})
	if !r.Held || r.Ext["card"] != "" {
		t.Fatal("hold did not retire the answered card")
	}
	s.larkModule().Action(ctx, staleStopAction(botTargetOf(r), old), "")
	if len(f.interrupted()) != 0 {
		t.Fatal("answered held card interrupted work")
	}
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_continuation"}, nil })
	feishuSink{s}.progress(ctx, r, time.Second, nil, "")
	a := staleStopAction(botTargetOf(r), "om_continuation")
	if out := s.larkModule().Action(ctx, a, ""); out.Toast == nil || out.Toast.Type != "success" {
		t.Fatalf("continuation toast %+v", out.Toast)
	}
	s.larkModule().Action(ctx, staleStopAction(botTargetOf(r), old), "")
	s.larkModule().Action(ctx, a, "")
	if got := f.interrupted(); len(got) != 1 {
		t.Fatalf("held continuation interrupts %v", got)
	}
}

func TestFeishuStaleStopFeaturePolicy(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, tc := range []struct {
		name     string
		features FeishuFeatures
		target   botTarget
		want     string
	}{
		{"assistant enabled", FeishuFeatures{Main: true}, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}, "success"},
		{"assistant disabled", FeishuFeatures{Agents: true}, botTarget{Kind: targetPersona, MachineID: "server", SessionID: "conv0001"}, "error"},
		{"agent enabled", FeishuFeatures{Agents: true}, botTarget{Kind: targetAgent, MachineID: "server", SessionID: "agent001"}, "success"},
		{"agent disabled", FeishuFeatures{Main: true}, botTarget{Kind: targetAgent, MachineID: "server", SessionID: "agent001"}, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := stopCardHub(t, tc.features)
			f.setSessions("server", proto.Session{MachineID: "server", SessionID: tc.target.SessionID, Assistant: tc.target.Kind == targetPersona, State: proto.StateWorking, Status: proto.StatusWorking})
			a := currentStopCard(s, tc.target)
			out := s.larkModule().Action(context.Background(), a, "")
			if out.Toast == nil || out.Toast.Type != tc.want {
				t.Fatalf("toast %+v want %s", out.Toast, tc.want)
			}
			if got := len(f.interrupted()); (got == 1) != (tc.want == "success") {
				t.Fatalf("interrupts=%d", got)
			}
		})
	}
}
