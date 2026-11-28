package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/chatsearch"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// countingRelayStore wraps memStore to count PutSetting calls on relayKey and inspect its serialized size.
type countingRelayStore struct {
	*memStore
	cntMu      sync.Mutex
	relayWrite int
}

func newCountingRelayStore() *countingRelayStore {
	return &countingRelayStore{memStore: &memStore{}}
}

func (c *countingRelayStore) PutSetting(ctx context.Context, key string, v any) error {
	if key == relayKey {
		c.cntMu.Lock()
		c.relayWrite++
		c.cntMu.Unlock()
	}
	return c.memStore.PutSetting(ctx, key, v)
}

func (c *countingRelayStore) relayWrites() int {
	c.cntMu.Lock()
	defer c.cntMu.Unlock()
	return c.relayWrite
}

func (c *countingRelayStore) relayRaw() []byte {
	c.memStore.mu.Lock()
	defer c.memStore.mu.Unlock()
	return slices.Clone(c.memStore.vals[relayKey])
}

func laterSendsCount(s *Service) int {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.st.LaterSends)
}

func storedRelayState(t *testing.T, st Store) relayState {
	t.Helper()
	var rs relayState
	if _, err := st.Setting(context.Background(), relayKey, &rs); err != nil {
		t.Fatalf("read stored relay state: %v", err)
	}
	return rs
}

func requestSendParams(t *testing.T, s *Service, machine, id string, params map[string]any) (map[string]any, error) {
	t.Helper()
	b := &BusHub{svc: s, helloed: make(chan struct{})}
	close(b.helloed)
	raw, err := json.Marshal(assistantwire.SendParams{
		MachineID: machine,
		SessionID: id,
		Params:    params,
		Human:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.request(context.Background(), assistantwire.MethodSendParams, raw)
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	got, _ := m["params"].(map[string]any)
	return got, nil
}

func snapshotQueuedFor(snap Snapshot, id string) []string {
	for _, sv := range snap.Sessions {
		if sv.SessionID == id {
			return sv.Queued
		}
	}
	return nil
}

// Sending a user message to a managed assistant conversation carries queued wake:false relays in front as a
// <persona-context> block, consumes them (and deletes its owner: LaterSends reservation) only once the node accepts
// the message, and publishes a snapshot that clears queuedRelays without starting any extra manager turn.
func TestOwnerSendCarriesAndDrainsQuietRelay(t *testing.T) {
	st := newCountingRelayStore()
	s, f := newQuietServiceOn(t, st)
	f.known["conv2"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle, Title: "Release Lead"},
	)
	ctx := context.Background()
	if _, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"Quiet sync: PR is green.","wake":false}`)); err != nil {
		t.Fatal(err)
	}
	if labels, ok := s.queuedRelays(); !ok || len(labels["conv1"]) != 1 || !strings.Contains(labels["conv1"][0], "Quiet sync: PR is green.") {
		t.Fatalf("queuedRelays before owner send = %v, %v", labels, ok)
	}
	if got := snapshotQueuedFor(f.lastSnapshot(), "conv1"); len(got) != 1 {
		t.Fatalf("snapshot queued before owner send = %v, want 1", got)
	}

	sent, err := requestSendParams(t, s, "server", "conv1", map[string]any{
		"text":          "Check the latest status.",
		"client_msg_id": "owner-msg-1",
	})
	if err != nil {
		t.Fatalf("MethodSendParams: %v", err)
	}
	payload, _ := sent["text"].(string)
	if !strings.Contains(payload, `<persona-context kind="context" title="没有叫醒你的消息" url="">`) ||
		!strings.Contains(payload, "Quiet sync: PR is green.") ||
		!strings.HasSuffix(payload, "\nCheck the latest status.") {
		t.Fatalf("owner send payload did not prepend quiet relay context:\n%s", payload)
	}
	// User-visible body extraction and chat search keep only what the user typed, never raw <persona-context> markup.
	if got := strings.TrimSpace(stripContext(payload)); got != "Check the latest status." {
		t.Fatalf("stripContext = %q, want user text only", got)
	}
	if indexed, ok := chatsearch.IndexText(proto.Item{Role: "user", Kind: proto.KindText, ClientMsgID: "owner-msg-1", Text: payload}); !ok || indexed != "Check the latest status." {
		t.Fatalf("chatsearch.IndexText = %q, %v; want user text only", indexed, ok)
	}
	// Quiet context carries another conversation's words: secret hand-off Always rules must not bypass approval,
	// while missing-reply recovery still recognizes the owner's message.
	if s.humanSent.has("conv1", "owner-msg-1", payload, false, "line-1") {
		t.Fatal("quiet relay carry must downgrade humanSent to reply-only")
	}
	if !s.humanSent.claimReply("conv1", proto.Item{ID: "line-1#0", ClientMsgID: "owner-msg-1", Text: payload}) {
		t.Fatal("missing-reply recovery stopped matching an owner message carrying quiet context")
	}

	// While in-flight: Later is still kept and the owner: carry reservation is persisted in LaterSends.
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("quiet relay dropped before ConversationDelivered (%d left)", n)
	}
	if n := laterSendsCount(s); n != 1 {
		t.Fatalf("LaterSends while in-flight = %d, want 1", n)
	}

	f.mu.Lock()
	snapsBefore := len(f.snaps)
	f.mu.Unlock()
	writesBeforeSettle := st.relayWrites()

	s.ConversationSent(ctx, ConversationSent{
		MachineID:   "server",
		SessionID:   "conv1",
		ClientMsgID: "owner-msg-1",
		Outcome:     assistantwire.ConversationDelivered,
	})

	if n := len(laterFor(s, "conv1")); n != 0 {
		t.Fatalf("quiet relay still waiting after ConversationDelivered: %d", n)
	}
	// Confirmed delivery deletes the owner: reservation in the same single save that drops Later.
	if n := laterSendsCount(s); n != 0 {
		t.Fatalf("LaterSends after ConversationDelivered = %d, want 0", n)
	}
	if got := st.relayWrites() - writesBeforeSettle; got != 1 {
		t.Fatalf("settlement relay writes = %d, want 1 combined save", got)
	}
	persisted := storedRelayState(t, st)
	if len(persisted.Later) != 0 || len(persisted.LaterSends) != 0 {
		t.Fatalf("persisted relay state not cleared after delivery: Later=%+v LaterSends=%+v", persisted.Later, persisted.LaterSends)
	}
	if labels, ok := s.queuedRelays(); !ok || len(labels["conv1"]) != 0 {
		t.Fatalf("queuedRelays after delivery = %v, %v; want empty", labels, ok)
	}
	f.mu.Lock()
	snapsAfter := len(f.snaps)
	f.mu.Unlock()
	if snapsAfter <= snapsBefore {
		t.Fatalf("expected snapshot publish after draining quiet relay (before=%d after=%d)", snapsBefore, snapsAfter)
	}
	if got := snapshotQueuedFor(f.lastSnapshot(), "conv1"); len(got) != 0 {
		t.Fatalf("snapshot queued after delivery = %v, want empty", got)
	}
	if n := f.sentTo("conv1"); n != 0 {
		t.Fatalf("manager started %d extra turn(s), want 0", n)
	}
}

// Reproduces the high-rate owner messaging scenario: 300 delivered owner sends with nothing queued write nothing to
// LaterSends, trigger zero relay state saves, and keep the serialized relay state at 2 B ("{}").
func TestOwnerSendEmptyQueueZeroRelayWritesAndNoBloat(t *testing.T) {
	st := newCountingRelayStore()
	if err := st.memStore.PutSetting(context.Background(), relayKey, relayState{}); err != nil {
		t.Fatal(err)
	}
	if got := len(st.relayRaw()); got != 2 {
		t.Fatalf("initial relay state size = %d B, want 2 B", got)
	}
	s, f := newQuietServiceOn(t, st)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	ctx := context.Background()

	const sends = 300
	for i := range sends {
		id := fmt.Sprintf("owner-empty-%03d", i)
		sent, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Routine user message",
			"client_msg_id": id,
		})
		if err != nil {
			t.Fatalf("send %d MethodSendParams: %v", i, err)
		}
		if sent["text"] != "Routine user message" {
			t.Fatalf("send %d altered text: %q", i, sent["text"])
		}
		s.ConversationSent(ctx, ConversationSent{
			MachineID:   "server",
			SessionID:   "conv1",
			ClientMsgID: id,
			Outcome:     assistantwire.ConversationDelivered,
		})
	}

	if got := st.relayWrites(); got != 0 {
		t.Fatalf("relay state written %d times across %d empty-queue owner sends, want 0", got, sends)
	}
	if got := laterSendsCount(s); got != 0 {
		t.Fatalf("LaterSends = %d after %d empty-queue owner sends, want 0", got, sends)
	}
	if raw := st.relayRaw(); len(raw) != 2 || string(raw) != "{}" {
		t.Fatalf("relay state bloated to %d B (%q), want 2 B (\"{}\")", len(raw), string(raw))
	}
	reloaded, _ := newQuietServiceOn(t, st)
	reloaded.relays.mu.Lock()
	ok := reloaded.relayLocked(ctx)
	reloadedCount := len(reloaded.relays.st.LaterSends)
	reloaded.relays.mu.Unlock()
	if !ok || reloadedCount != 0 {
		t.Fatalf("reloaded LaterSends = %d (ok=%v), want 0", reloadedCount, ok)
	}
}

// Failed, unknown, refused, and slash-command owner sends must never drop queued wake:false messages.
func TestOwnerSendFailureAndCommandsKeepQuietRelay(t *testing.T) {
	s, f := newQuietService(t)
	f.known["conv2"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
	)
	ctx := context.Background()
	if _, err := s.toolConversationSend(ctx, "conv2", []byte(`{"id":"conv1","text":"Keep me safe.","wake":false}`)); err != nil {
		t.Fatal(err)
	}

	// Failed send keeps Later.
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "First try", "client_msg_id": "fail-1"}); err != nil {
		t.Fatal(err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "fail-1", Outcome: assistantwire.ConversationFailed})
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("failed send dropped quiet relay (%d left)", n)
	}

	// Unknown outcome keeps Later (at worst read twice, never lost).
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Second try", "client_msg_id": "unknown-1"}); err != nil {
		t.Fatal(err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "unknown-1", Outcome: assistantwire.ConversationUnknown})
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("unknown outcome dropped quiet relay (%d left)", n)
	}

	// Slash command carries nothing and clears nothing.
	cmd, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "/compact", "client_msg_id": "cmd-1"})
	if err != nil {
		t.Fatal(err)
	}
	if cmd["text"] != "/compact" {
		t.Fatalf("slash command carried quiet relay: %q", cmd["text"])
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "cmd-1", Outcome: assistantwire.ConversationDelivered})
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("slash command dropped quiet relay (%d left)", n)
	}

	// Refused SendParams leaves Later untouched.
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Bad model", "model": "not a model id!", "client_msg_id": "bad-1"}); err == nil {
		t.Fatal("expected invalid model refusal")
	}
	if n := len(laterFor(s, "conv1")); n != 1 {
		t.Fatalf("refused SendParams dropped quiet relay (%d left)", n)
	}
}

// Retrying an owner send under the same client_msg_id:
//   - when attempt 1 carried quiet relays, retries reuse that exact reservation across manager restarts, never consume
//     later arrivals, and delete the owner: reservation once delivered;
//   - when attempt 1 had nothing queued, zero relay writes occur; a retry after a refused-before-typing failure safely
//     picks up newly arrived quiet relays, whereas an owner Retry of an in-doubt kept send (TriedAt set) does not pick
//     up newly arrived quiet relays because the node may still deduplicate client_msg_id.
func TestOwnerSendRetryIdempotentAndPreservesNewQuietRelays(t *testing.T) {
	t.Run("with_initial_quiet_relay", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()

		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Original quiet note.", At: time.Now().UTC()}); !ok {
			t.Fatal("could not keep original quiet note")
		}

		first, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Owner prompt",
			"client_msg_id": "owner-retry-1",
		})
		if err != nil {
			t.Fatalf("first MethodSendParams: %v", err)
		}
		firstText, _ := first["text"].(string)
		if !strings.Contains(firstText, "Original quiet note.") {
			t.Fatalf("first send missing original quiet note: %q", firstText)
		}

		// First attempt fails; a new quiet relay arrives before the retry.
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "owner-retry-1", Outcome: assistantwire.ConversationFailed})
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Arrived during retry.", At: time.Now().UTC()}); !ok {
			t.Fatal("could not keep new quiet note")
		}

		// Restart the manager between attempt 1 and attempt 2.
		again, fAgain := newQuietServiceOn(t, st)
		fAgain.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		retry, err := requestSendParams(t, again, "server", "conv1", map[string]any{
			"text":          "Owner prompt",
			"client_msg_id": "owner-retry-1",
		})
		if err != nil {
			t.Fatalf("retry MethodSendParams: %v", err)
		}
		if retry["text"] != first["text"] {
			t.Fatalf("retry payload changed:\nfirst=%q\nretry=%q", first["text"], retry["text"])
		}

		// Restart again before session.assistant_sent arrives to verify durable settlement and owner: key cleanup.
		settled, fSettled := newQuietServiceOn(t, st)
		fSettled.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		settled.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "owner-retry-1", Outcome: assistantwire.ConversationDelivered})

		left := laterFor(settled, "conv1")
		if len(left) != 1 || left[0].Text != "Arrived during retry." {
			t.Fatalf("retry consumed the wrong quiet messages: %+v", left)
		}
		if n := laterSendsCount(settled); n != 0 {
			t.Fatalf("owner: reservation lingered after retry delivery: %d", n)
		}

		// Next send with a fresh client_msg_id carries and drains the newly arrived quiet message and cleans up LaterSends.
		next, err := requestSendParams(t, settled, "server", "conv1", map[string]any{
			"text":          "Follow up",
			"client_msg_id": "owner-retry-2",
		})
		if err != nil {
			t.Fatalf("next MethodSendParams: %v", err)
		}
		nextText, _ := next["text"].(string)
		if !strings.Contains(nextText, "Arrived during retry.") || strings.Contains(nextText, "Original quiet note.") {
			t.Fatalf("next send payload = %q", nextText)
		}
		settled.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "owner-retry-2", Outcome: assistantwire.ConversationDelivered})
		if len(laterFor(settled, "conv1")) != 0 || laterSendsCount(settled) != 0 {
			t.Fatalf("next send did not clear Later/LaterSends: Later=%+v LaterSends=%d", laterFor(settled, "conv1"), laterSendsCount(settled))
		}
		if labels, ok := settled.queuedRelays(); !ok || len(labels["conv1"]) != 0 {
			t.Fatalf("queuedRelays still non-empty: %v, %v", labels, ok)
		}
	})

	t.Run("initially_empty_after_failed_send", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()

		first, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Owner prompt",
			"client_msg_id": "empty-fail-1",
		})
		if err != nil {
			t.Fatalf("first MethodSendParams: %v", err)
		}
		if first["text"] != "Owner prompt" {
			t.Fatalf("unexpected context on empty Later: %q", first["text"])
		}
		if st.relayWrites() != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("empty-queue send wrote relay state: writes=%d LaterSends=%d", st.relayWrites(), laterSendsCount(s))
		}

		// Refused before typing: nothing reached the node. A quiet relay arrives before the retry.
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "empty-fail-1", Outcome: assistantwire.ConversationFailed})
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Arrived before retry.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}

		retry, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Owner prompt",
			"client_msg_id": "empty-fail-1",
		})
		if err != nil {
			t.Fatalf("retry MethodSendParams: %v", err)
		}
		retryText, _ := retry["text"].(string)
		if !strings.Contains(retryText, "Arrived before retry.") || !strings.HasSuffix(retryText, "\nOwner prompt") {
			t.Fatalf("retry after refused empty send should carry newly queued note: %q", retryText)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "empty-fail-1", Outcome: assistantwire.ConversationDelivered})
		if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("after delivery: Later=%+v LaterSends=%d", laterFor(s, "conv1"), laterSendsCount(s))
		}
	})

	t.Run("initially_empty_in_doubt_owner_retry", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()

		// Attempt 1 had nothing queued and ended with unknown outcome; hub keeps it with CodeDeliveryUnknown (TriedAt set).
		if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Did this land?",
			"client_msg_id": "empty-doubt-1",
		}); err != nil {
			t.Fatal(err)
		}
		if st.relayWrites() != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("empty-queue send wrote relay state: writes=%d LaterSends=%d", st.relayWrites(), laterSendsCount(s))
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "empty-doubt-1", Outcome: assistantwire.ConversationUnknown})
		if _, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
			MachineID: "server", SessionID: "conv1",
			Params: map[string]any{"text": "Did this land?", "client_msg_id": "empty-doubt-1"},
			Human:  true, Code: assistantwire.CodeDeliveryUnknown, TriedAt: time.Now().Add(-time.Second),
		}); err != nil {
			t.Fatal(err)
		}

		// A quiet relay arrives after attempt 1; manager restarts; owner clicks Retry.
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Must not be swallowed by in-doubt retry.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}
		again, fAgain := newQuietServiceOn(t, st)
		fAgain.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		if code := ownerRetry(again, "empty-doubt-1"); code != http.StatusOK {
			t.Fatalf("ownerRetry status = %d, want 200", code)
		}
		again.undeliveredPass(ctx, time.Now().Add(time.Minute))

		sentText := ""
		fAgain.mu.Lock()
		for _, c := range fAgain.calls {
			if c.Method != DeviceSessionSend {
				continue
			}
			var a map[string]any
			_ = json.Unmarshal(c.Params, &a)
			if a["client_msg_id"] == "empty-doubt-1" {
				sentText, _ = a["text"].(string)
			}
		}
		fAgain.mu.Unlock()
		if sentText != "Did this land?" {
			t.Fatalf("in-doubt OwnerRetry attached quiet relay: %q", sentText)
		}
		if left := laterFor(again, "conv1"); len(left) != 1 || left[0].Text != "Must not be swallowed by in-doubt retry." {
			t.Fatalf("in-doubt OwnerRetry disturbed Later: %+v", left)
		}
		if n := laterSendsCount(again); n != 0 {
			t.Fatalf("in-doubt OwnerRetry created LaterSends entry: %d", n)
		}
	})
}

// Unnamed owner sends (no client_msg_id) lack a durable settlement key so they leave relayState.Later untouched
// instead of risking non-idempotent duplicate carry, and non-assistant Agent sessions never attach or consume quiet
// relays even if SendParams is invoked.
func TestOwnerSendWithoutClientMsgIDAndNonAssistantGuard(t *testing.T) {
	st := newCountingRelayStore()
	s, f := newQuietServiceOn(t, st)
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "agent1", MachineID: "server", Assistant: false, PaneID: "%1", Status: proto.StatusIdle},
	)
	ctx := context.Background()
	if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Needs named delivery.", At: time.Now().UTC()}); !ok {
		t.Fatal("could not keep conv1 note")
	}
	if _, ok := s.keepRelayLater("agent1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Must stay untouched.", At: time.Now().UTC()}); !ok {
		t.Fatal("could not keep agent1 note")
	}
	writesBefore := st.relayWrites()

	// Non-assistant Agent session is unaffected.
	agentParams, err := requestSendParams(t, s, "server", "agent1", map[string]any{"text": "Run tests", "client_msg_id": "agent-1"})
	if err != nil {
		t.Fatalf("agent MethodSendParams: %v", err)
	}
	if agentParams["text"] != "Run tests" {
		t.Fatalf("non-assistant session got quiet relay context: %q", agentParams["text"])
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "agent1", ClientMsgID: "agent-1", Outcome: assistantwire.ConversationDelivered})
	if len(laterFor(s, "agent1")) != 1 {
		t.Fatal("non-assistant session modified relayState.Later")
	}

	// Repeated unnamed owner sends (no client_msg_id) neither attach quiet context nor mutate Later/LaterSends.
	for i := range 12 {
		out, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": fmt.Sprintf("Hello without id %d", i)})
		if err != nil {
			t.Fatalf("unnamed send %d: %v", i, err)
		}
		if out["text"] != fmt.Sprintf("Hello without id %d", i) {
			t.Fatalf("unnamed send %d carried quiet relay: %q", i, out["text"])
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", Outcome: assistantwire.ConversationDelivered})
	}
	if len(laterFor(s, "conv1")) != 1 || laterSendsCount(s) != 0 || st.relayWrites() != writesBefore {
		t.Fatalf("unnamed sends disturbed relay state: Later=%d LaterSends=%d extraWrites=%d",
			len(laterFor(s, "conv1")), laterSendsCount(s), st.relayWrites()-writesBefore)
	}

	// Next named owner send carries and drains the quiet relay cleanly.
	named, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Named follow-up", "client_msg_id": "named-1"})
	if err != nil || !strings.Contains(named["text"].(string), "Needs named delivery.") {
		t.Fatalf("named send after unnamed sends: %+v, %v", named, err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "named-1", Outcome: assistantwire.ConversationDelivered})
	if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
		t.Fatalf("named send did not clear Later/LaterSends: Later=%d LaterSends=%d", len(laterFor(s, "conv1")), laterSendsCount(s))
	}
}

// Reusing a previously failed client_msg_id for a non-carrying send (slash command, public turn) or delivering a send
// that degraded during a relay read outage must never drop quiet messages that were not actually carried.
func TestOwnerSendReusedClientMsgIDNonCarryingDoesNotDropQuietRelay(t *testing.T) {
	t.Run("slash_command_reuses_failed_id", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Do not drop on /compact.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}

		// Attempt 1 reserves Q1, then fails before typing.
		if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "First try", "client_msg_id": "reuse-1"}); err != nil {
			t.Fatal(err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "reuse-1", Outcome: assistantwire.ConversationFailed})
		if laterSendsCount(s) != 1 {
			t.Fatalf("expected 1 reservation after failed send, got %d", laterSendsCount(s))
		}

		// Client reuses reuse-1 for a slash command: carries nothing, clears stale reservation without touching Later.
		cmd, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "/compact", "client_msg_id": "reuse-1"})
		if err != nil || cmd["text"] != "/compact" {
			t.Fatalf("reused slash command = %+v, %v", cmd, err)
		}
		if laterSendsCount(s) != 0 {
			t.Fatalf("stale reservation survived non-carrying reuse: %d", laterSendsCount(s))
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "reuse-1", Outcome: assistantwire.ConversationDelivered})
		if left := laterFor(s, "conv1"); len(left) != 1 || left[0].Text != "Do not drop on /compact." {
			t.Fatalf("slash command delivery dropped uncarried quiet relay: %+v", left)
		}

		// Next real send still carries and drains Q1.
		next, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Continue", "client_msg_id": "fresh-1"})
		if err != nil || !strings.Contains(next["text"].(string), "Do not drop on /compact.") {
			t.Fatalf("next send = %+v, %v", next, err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "fresh-1", Outcome: assistantwire.ConversationDelivered})
		if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("next send did not clear state: Later=%d LaterSends=%d", len(laterFor(s, "conv1")), laterSendsCount(s))
		}
	})

	t.Run("public_turn_reuses_failed_id", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Do not drop on public turn.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}

		if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Private try", "client_msg_id": "reuse-pub-1"}); err != nil {
			t.Fatal(err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "reuse-pub-1", Outcome: assistantwire.ConversationFailed})

		pub, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Public turn", "public": true, "client_msg_id": "reuse-pub-1"})
		if err != nil || pub["text"] != "Public turn" {
			t.Fatalf("reused public turn = %+v, %v", pub, err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "reuse-pub-1", Outcome: assistantwire.ConversationDelivered})
		if left := laterFor(s, "conv1"); len(left) != 1 || left[0].Text != "Do not drop on public turn." {
			t.Fatalf("public turn delivery dropped uncarried quiet relay: %+v", left)
		}
	})

	t.Run("kept_undelivered_reservation_survives_stale_slash_command", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()
		if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Kept in doubt.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}

		if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Original kept", "client_msg_id": "kept-reuse-1"}); err != nil {
			t.Fatal(err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "kept-reuse-1", Outcome: assistantwire.ConversationUnknown})
		if _, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
			MachineID: "server", SessionID: "conv1",
			Params: map[string]any{"text": "Original kept", "client_msg_id": "kept-reuse-1"},
			Human:  true, Code: assistantwire.CodeDeliveryUnknown, TriedAt: time.Now().Add(-time.Second),
		}); err != nil {
			t.Fatal(err)
		}

		// A stale SendParams with the same ID as a slash command must not discard the kept undelivered reservation.
		if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "/status", "client_msg_id": "kept-reuse-1"}); err != nil {
			t.Fatal(err)
		}
		if laterSendsCount(s) != 1 {
			t.Fatalf("kept undelivered reservation was cleared early: %d", laterSendsCount(s))
		}

		// Once undeliveredLook finds the kept message landed, it drains both Later and LaterSends.
		f.setItems(proto.Item{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: "kept-reuse-1", Text: "Original kept", TS: time.Now()})
		s.undeliveredPass(ctx, time.Now().Add(time.Hour))
		if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("undeliveredLook did not drain kept-reuse-1: Later=%d LaterSends=%d", len(laterFor(s, "conv1")), laterSendsCount(s))
		}
	})

	t.Run("read_outage_degrade_recovery_before_delivered_does_not_drop_later", func(t *testing.T) {
		st := newCountingRelayStore()
		seed, fSeed := newQuietServiceOn(t, st)
		fSeed.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		ctx := context.Background()
		if _, ok := seed.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Must survive degraded retry.", At: time.Now().UTC()}); !ok {
			t.Fatal("keep")
		}
		// Attempt 1 persists a reservation for degrade-reuse-1 and fails before typing.
		if _, err := requestSendParams(t, seed, "server", "conv1", map[string]any{"text": "Try 1", "client_msg_id": "degrade-reuse-1"}); err != nil {
			t.Fatal(err)
		}
		seed.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "degrade-reuse-1", Outcome: assistantwire.ConversationFailed})

		// Manager restarts during a transient relay read outage; attempt 2 degrades and sends without quiet relays.
		st.failKey(relayKey, errors.New("transient read failure"))
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		degraded, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Try 2", "client_msg_id": "degrade-reuse-1"})
		if err != nil || degraded["text"] != "Try 2" {
			t.Fatalf("degraded retry = %+v, %v", degraded, err)
		}

		// Storage recovers before ConversationDelivered arrives: must NOT drop Q1 (which Try 2 never carried).
		st.failKey(relayKey, nil)
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "degrade-reuse-1", Outcome: assistantwire.ConversationDelivered})
		if left := laterFor(s, "conv1"); len(left) != 1 || left[0].Text != "Must survive degraded retry." {
			t.Fatalf("degraded delivery dropped uncarried quiet relay: %+v", left)
		}
		if laterSendsCount(s) != 0 {
			t.Fatalf("stale reservation not cleared after degraded delivery: %d", laterSendsCount(s))
		}

		// Next owner send carries and drains Q1 cleanly.
		next, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Try 3", "client_msg_id": "after-degrade-1"})
		if err != nil || !strings.Contains(next["text"].(string), "Must survive degraded retry.") {
			t.Fatalf("post-recovery send = %+v, %v", next, err)
		}
		s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "after-degrade-1", Outcome: assistantwire.ConversationDelivered})
		if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("post-recovery send left state: Later=%d LaterSends=%d", len(laterFor(s, "conv1")), laterSendsCount(s))
		}
	})
}

// An owner send that failed, was kept in undelivered, and later succeeded via deliverKept (or landed detection)
// reuses its original client_msg_id reservation, clears only the reserved quiet relays, and deletes its owner:
// reservation from LaterSends. Deleting a kept message also forgets its owner: reservation without dropping Later.
func TestOwnerSendUndeliveredRetryAndLandedDrainQuietRelay(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	ctx := context.Background()

	if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Before outage.", At: time.Now().UTC()}); !ok {
		t.Fatal("keep Q1")
	}
	// Hub prepared send "kept-1" (reserving Q1), node failed, hub reported ConversationFailed and kept raw message.
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Resume work", "client_msg_id": "kept-1"}); err != nil {
		t.Fatal(err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "kept-1", Outcome: assistantwire.ConversationFailed})
	if _, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
		MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": "Resume work", "client_msg_id": "kept-1"},
		Human:  true, Code: "machine_offline",
	}); err != nil {
		t.Fatal(err)
	}

	// Q2 arrives while kept-1 waits.
	if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "During outage.", At: time.Now().UTC()}); !ok {
		t.Fatal("keep Q2")
	}

	// Automatic undelivered retry delivers kept-1 carrying only Q1, then clears Q1 and owner:kept-1 from LaterSends.
	s.undeliveredPass(ctx, time.Now().Add(time.Hour))
	sentText := ""
	f.mu.Lock()
	for _, c := range f.calls {
		if c.Method != DeviceSessionSend {
			continue
		}
		var a map[string]any
		_ = json.Unmarshal(c.Params, &a)
		if a["client_msg_id"] == "kept-1" {
			sentText, _ = a["text"].(string)
		}
	}
	f.mu.Unlock()
	if !strings.Contains(sentText, "Before outage.") || strings.Contains(sentText, "During outage.") || !strings.HasSuffix(sentText, "\nResume work") {
		t.Fatalf("deliverKept payload = %q", sentText)
	}
	left := laterFor(s, "conv1")
	if len(left) != 1 || left[0].Text != "During outage." {
		t.Fatalf("after deliverKept, Later = %+v; want only Q2", left)
	}
	if n := laterSendsCount(s); n != 0 {
		t.Fatalf("after deliverKept, LaterSends = %d; want 0", n)
	}

	// Now simulate an unknown-outcome send ("kept-2") that actually landed in the transcript.
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Check landed", "client_msg_id": "kept-2"}); err != nil {
		t.Fatal(err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "kept-2", Outcome: assistantwire.ConversationUnknown})
	if _, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
		MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": "Check landed", "client_msg_id": "kept-2"},
		Human:  true, Code: assistantwire.CodeDeliveryUnknown, TriedAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	f.setItems(proto.Item{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, ClientMsgID: "kept-2", Text: "Check landed", TS: time.Now()})
	s.undeliveredPass(ctx, time.Now().Add(time.Hour))
	if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
		t.Fatalf("landed check did not clear Q2 and owner:kept-2: Later=%+v LaterSends=%d", laterFor(s, "conv1"), laterSendsCount(s))
	}

	// Deleting a kept undelivered message forgets its owner: reservation while preserving Later.
	if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Survives delete.", At: time.Now().UTC()}); !ok {
		t.Fatal("keep Q3")
	}
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Will delete", "client_msg_id": "kept-del"}); err != nil {
		t.Fatal(err)
	}
	s.ConversationSent(ctx, ConversationSent{MachineID: "server", SessionID: "conv1", ClientMsgID: "kept-del", Outcome: assistantwire.ConversationFailed})
	if _, err := s.KeepUndelivered(ctx, assistantwire.KeepUndeliveredParams{
		MachineID: "server", SessionID: "conv1",
		Params: map[string]any{"text": "Will delete", "client_msg_id": "kept-del"},
		Human:  true, Code: "machine_offline",
	}); err != nil {
		t.Fatal(err)
	}
	if laterSendsCount(s) != 1 {
		t.Fatalf("expected 1 reservation before delete, got %d", laterSendsCount(s))
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/v1/assistant/conversations/conv1/undelivered/kept-del", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete kept message: %d %s", rec.Code, rec.Body)
	}
	if laterSendsCount(s) != 0 {
		t.Fatalf("deleting kept message left owner: reservation: %d", laterSendsCount(s))
	}
	if left := laterFor(s, "conv1"); len(left) != 1 || left[0].Text != "Survives delete." {
		t.Fatalf("deleting kept message disturbed Later: %+v", left)
	}
}

// Unsettled owner: reservations expire after ownerLaterKeep (24h, matching undeliveredGiveUp), while manager
// assistant.send once_id reservations stay until relayLaterKeep (7d).
func TestOwnerRelayLaterExpiresSoonerThanManagerOnceID(t *testing.T) {
	s, _ := newQuietService(t)
	now := time.Now().UTC()
	ownerFresh := relayLaterKey("conv1", relayLaterOwnerOnce("fresh-owner"))
	ownerStale := relayLaterKey("conv1", relayLaterOwnerOnce("stale-owner"))
	managerOnce := relayLaterKey("conv1", "manager-once-1")

	b := &s.relays
	b.mu.Lock()
	s.relayLocked(context.Background())
	b.st.LaterSends = map[string]relayLaterSend{
		ownerFresh:  {At: now.Add(-ownerLaterKeep + time.Hour), Text: "fresh"},
		ownerStale:  {At: now.Add(-ownerLaterKeep - time.Hour), Text: "stale"},
		managerOnce: {At: now.Add(-ownerLaterKeep - time.Hour), Text: "manager"},
	}
	s.pruneRelayLaterLocked(now)
	_, hasFresh := b.st.LaterSends[ownerFresh]
	_, hasStale := b.st.LaterSends[ownerStale]
	_, hasManager := b.st.LaterSends[managerOnce]
	b.mu.Unlock()

	if !hasFresh {
		t.Fatal("owner reservation within ownerLaterKeep (24h) was pruned early")
	}
	if hasStale {
		t.Fatal("owner reservation older than ownerLaterKeep (24h) was not pruned")
	}
	if !hasManager {
		t.Fatal("manager once_id reservation was pruned before relayLaterKeep (7d)")
	}

	b.mu.Lock()
	s.pruneRelayLaterLocked(now.Add(relayLaterKeep))
	_, hasManagerAfterWeek := b.st.LaterSends[managerOnce]
	b.mu.Unlock()
	if hasManagerAfterWeek {
		t.Fatal("manager once_id reservation survived past relayLaterKeep (7d)")
	}
}

// When nothing is queued or relay state cannot be read, an owner's own message is never blocked by relay storage
// failures; when quiet messages ARE waiting and saving the carry reservation fails, the send still fails closed.
func TestOwnerSendStorageResilienceVsCarryFailClosed(t *testing.T) {
	t.Run("empty_queue_ignores_write_failure", func(t *testing.T) {
		st := newCountingRelayStore()
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		st.setFailPut(errors.New("database read-only"))

		sent, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "Still works when relay store is read-only",
			"client_msg_id": "empty-readonly-1",
		})
		if err != nil {
			t.Fatalf("empty-queue owner send failed on storage write error: %v", err)
		}
		if sent["text"] != "Still works when relay store is read-only" {
			t.Fatalf("unexpected text: %q", sent["text"])
		}
		if st.relayWrites() != 0 {
			t.Fatalf("empty-queue send attempted %d relay writes, want 0", st.relayWrites())
		}
	})

	t.Run("unreadable_relay_state_degrades_without_losing_later", func(t *testing.T) {
		st := newCountingRelayStore()
		seed, _ := newQuietServiceOn(t, st)
		if _, ok := seed.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Safe while unreadable.", At: time.Now().UTC()}); !ok {
			t.Fatal("seed keep")
		}

		// Fresh service started while relay state is unreadable: owner message goes through without quiet context.
		st.failKey(relayKey, errors.New("corrupt relay setting"))
		s, f := newQuietServiceOn(t, st)
		f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
		s.relays.mu.Lock()
		loaded := s.relays.loaded
		s.relays.mu.Unlock()
		if loaded {
			t.Fatal("expected relay state to stay unloaded while relayKey fails to read")
		}

		sent, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "User message during relay outage",
			"client_msg_id": "unreadable-1",
		})
		if err != nil {
			t.Fatalf("owner send refused when relay state could not be read: %v", err)
		}
		if sent["text"] != "User message during relay outage" {
			t.Fatalf("unexpected text during relay read outage: %q", sent["text"])
		}
		s.ConversationSent(context.Background(), ConversationSent{
			MachineID: "server", SessionID: "conv1", ClientMsgID: "unreadable-1", Outcome: assistantwire.ConversationDelivered,
		})

		// Once relay storage recovers, the queued message is still intact and carried by the next owner send.
		st.failKey(relayKey, nil)
		next, err := requestSendParams(t, s, "server", "conv1", map[string]any{
			"text":          "After recovery",
			"client_msg_id": "recovered-1",
		})
		if err != nil {
			t.Fatalf("post-recovery MethodSendParams: %v", err)
		}
		nextText, _ := next["text"].(string)
		if !strings.Contains(nextText, "Safe while unreadable.") || !strings.HasSuffix(nextText, "\nAfter recovery") {
			t.Fatalf("queued quiet relay was lost during read outage: %q", nextText)
		}
		s.ConversationSent(context.Background(), ConversationSent{
			MachineID: "server", SessionID: "conv1", ClientMsgID: "recovered-1", Outcome: assistantwire.ConversationDelivered,
		})
		if len(laterFor(s, "conv1")) != 0 || laterSendsCount(s) != 0 {
			t.Fatalf("post-recovery delivery left stale state: Later=%+v LaterSends=%d", laterFor(s, "conv1"), laterSendsCount(s))
		}
	})
}

// Storage failure while reserving quiet relay context stops before returning params to the hub.
func TestOwnerSendCarrySaveFailureRefusesBeforeNodeWrite(t *testing.T) {
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if _, ok := s.keepRelayLater("conv1", relayMsg{From: "conv2", FromTitle: "Lead", Text: "Must reserve first.", At: time.Now().UTC()}); !ok {
		t.Fatal("keep")
	}
	st.setFailPut(errors.New("database unavailable"))
	if _, err := requestSendParams(t, s, "server", "conv1", map[string]any{"text": "Go", "client_msg_id": "fail-save-1"}); err == nil {
		t.Fatal("expected error when carry reservation could not be saved")
	}
	st.setFailPut(nil)
	if len(laterFor(s, "conv1")) != 1 {
		t.Fatal("failed reservation save lost quiet relay")
	}
}
