package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestDrainRelaysDefersTransientGoneForLogicalPlaceholderAndDeliversWhenListed(t *testing.T) {
	ctx := context.Background()
	gap := relayRetryGap
	relayRetryGap = 25 * time.Millisecond
	t.Cleanup(func() { relayRetryGap = gap })

	s, f := newQuietService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.mu.Lock()
	f.known["conv1"] = true
	f.known["lead1"] = true
	f.mu.Unlock()

	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	placeholder := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusOffline, LogicalPlaceholder: true}
	physical := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", main, placeholder)

	id, done := s.handoverFor(ctx, "conv1", "lead1", "Lead", &relayRequest{BriefRequest: BriefRequest{Conversation: "conv1", MessageID: "m1"}})
	s.queueRelay("lead1", relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "take this task", At: time.Now().UTC(), Handover: id})
	done()

	// Simulate an older node returning transient "is gone" while the logical target is still a placeholder.
	refuseSendsTo(f, "lead1", errors.New("conversation lead1 is gone"))
	drainSettled(t, s, placeholder)
	if got := s.queuedFor("lead1"); got != 1 {
		t.Fatalf("after first transient gone on placeholder: queued = %d, want 1", got)
	}
	if got := handoverState(s, id); got != handoverQueued {
		t.Fatalf("handover state after first transient gone = %s, want %s", got, handoverQueued)
	}
	if got := f.sentTo("conv1"); got != 0 {
		t.Fatalf("unexpected sender notification before terminal gone: %d", got)
	}

	// Once the real physical session appears in the list, queued delivery succeeds immediately without waiting for relayRetryGap.
	f.mu.Lock()
	f.onAssistantSend = nil
	f.failAssistant = nil
	f.mu.Unlock()
	f.setSessions("server", main, physical)
	drainSettled(t, s, physical)
	if got := s.queuedFor("lead1"); got != 0 {
		t.Fatalf("after physical row listed: queued = %d, want 0", got)
	}
	if got := f.sentTo("lead1"); got != 1 {
		t.Fatalf("sent to lead1 = %d, want 1", got)
	}
	if got := handoverState(s, id); got != handoverDelivered {
		t.Fatalf("handover state = %s, want %s", got, handoverDelivered)
	}
}

func TestDrainRelaysPlaceholderRuntimeAwayDeliversImmediatelyWhenPhysicalRowArrives(t *testing.T) {
	gap := relayRetryGap
	relayRetryGap = 10 * time.Second
	t.Cleanup(func() { relayRetryGap = gap })

	s, f := newQuietService(t)
	f.mu.Lock()
	f.known["conv1"] = true
	f.known["lead1"] = true
	f.mu.Unlock()

	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	placeholder := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusOffline, LogicalPlaceholder: true}
	physical := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", main, placeholder)

	s.queueRelay("lead1", relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "deliver on wake", At: time.Now().UTC()})
	refuseSendsTo(f, "lead1", &rpc.Error{Code: "runtime_away", Message: "Antigravity not connected yet"})
	drainSettled(t, s, placeholder)
	if got := s.queuedFor("lead1"); got != 1 {
		t.Fatalf("after runtime_away: queued = %d, want 1", got)
	}

	// Clear refusal and report the real physical row well before the 10s retry gap elapses.
	f.mu.Lock()
	f.onAssistantSend = nil
	f.failAssistant = nil
	f.mu.Unlock()
	f.setSessions("server", main, physical)
	start := time.Now()
	drainSettled(t, s, physical)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("physical row appearance waited %v instead of delivering immediately", elapsed)
	}
	if got := f.sentTo("lead1"); got != 1 || s.queuedFor("lead1") != 0 {
		t.Fatalf("after physical row listed: sent=%d queued=%d, want 1 and 0", got, s.queuedFor("lead1"))
	}
}

func TestDrainRelaysNotifiesSenderWhenGoneBecomesTerminal(t *testing.T) {
	for _, tc := range []struct {
		name        string
		placeholder bool
		locale      string
		wantSnippet string
	}{
		{name: "physical gone zh", placeholder: false, locale: "", wantSnippet: "那段对话已不存在（gone）"},
		{name: "placeholder exhausted en", placeholder: true, locale: "en", wantSnippet: "no longer available (gone)"},
		{name: "physical gone ja", placeholder: false, locale: "ja", wantSnippet: "その会話はもう存在しないため（gone）"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			gap := relayRetryGap
			relayRetryGap = 10 * time.Second
			t.Cleanup(func() { relayRetryGap = gap })

			s, f := newQuietService(t)
			setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
			f.mu.Lock()
			f.known["conv1"] = true
			f.known["lead1"] = true
			f.hello.Locale = tc.locale
			f.mu.Unlock()

			main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			target := proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle, LogicalPlaceholder: tc.placeholder}
			if tc.placeholder {
				target.Status = proto.StatusOffline
			}
			f.setSessions("server", main, target)

			id, done := s.handoverFor(ctx, "conv1", "lead1", "Lead", &relayRequest{BriefRequest: BriefRequest{Conversation: "conv1", MessageID: "m1"}})
			s.queueRelay("lead1", relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "important handover", At: time.Now().UTC(), Handover: id})
			done()

			tries := relayTries(f, "lead1")
			refuseSendsTo(f, "lead1", errors.New("conversation lead1 is gone"))
			if tc.placeholder {
				// Drive each placeholder retry deterministically without relying on background timer races.
				for attempt := 1; attempt < relayPlaceholderGoneRetries; attempt++ {
					drainSettled(t, s, target)
					if got := s.queuedFor("lead1"); got != 1 {
						t.Fatalf("attempt %d: queued = %d, want 1", attempt, got)
					}
					if got := handoverState(s, id); got != handoverQueued {
						t.Fatalf("attempt %d: handover = %s, want %s", attempt, got, handoverQueued)
					}
					if got := f.sentTo("conv1"); got != 0 {
						t.Fatalf("attempt %d: notified sender early (%d)", attempt, got)
					}
					s.relays.mu.Lock()
					if timer := s.relays.holds["lead1"]; timer != nil {
						timer.Stop()
						delete(s.relays.holds, "lead1")
					}
					s.relays.failedAt["lead1"] = time.Now().Add(-2 * relayRetryGap)
					s.relays.mu.Unlock()
				}
				drainSettled(t, s, target)
			} else {
				drainSettled(t, s, target)
			}

			waitFor(t, "terminal gone drops queue and notifies sender", func() bool {
				return s.queuedFor("lead1") == 0 && handoverState(s, id) == handoverNotDelivered && f.sentTo("conv1") == 1
			})
			wantTries := 1
			if tc.placeholder {
				wantTries = relayPlaceholderGoneRetries
			}
			if got := len(triesOf(f, tries)); got != wantTries {
				t.Fatalf("tries = %d, want %d", got, wantTries)
			}
			notice := f.sentTexts("conv1")[0]
			if !strings.Contains(notice, tc.wantSnippet) {
				t.Fatalf("sender notice = %q, want %q", notice, tc.wantSnippet)
			}
		})
	}
}
