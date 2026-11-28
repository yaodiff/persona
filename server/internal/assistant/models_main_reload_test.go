package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type memLogicalStore struct {
	Store
	mu    sync.Mutex
	convs map[[2]string]*db.LogicalConversation
}

func newMemLogicalFixture(t *testing.T, active conversationlog.Segment) (*Service, *fakeHub, *memLogicalStore) {
	t.Helper()
	s, f := pickFixture(t)
	st := &memLogicalStore{
		Store: s.store(),
		convs: map[[2]string]*db.LogicalConversation{
			{"server", "main"}: {
				MachineID: "server",
				SessionID: "main",
				Epoch:     2,
				Segments: []conversationlog.Segment{
					reloadSegment("one", 1, "main", "claude", "claude", "retired"),
					active,
				},
			},
		},
	}
	svc := newServiceOn(t, f, st)
	f.mu.Lock()
	f.known["main"] = true
	f.known[active.SessionID] = true
	f.mu.Unlock()
	return svc, f, st
}

// reloadSegment is one segment of a conversation on the machine "server", built from the documented zero (archguard's
// out-literal rule, as placeholderSegment is); a case that needs a model or an effort sets it on the result.
func reloadSegment(id string, ordinal int, session, engine, launcher, state string) conversationlog.Segment {
	var s conversationlog.Segment
	s.ID, s.Ordinal, s.MachineID, s.SessionID, s.Engine, s.Launcher, s.State = id, ordinal, "server", session, engine, launcher, state
	return s
}

func (m *memLogicalStore) LogicalConversation(_ context.Context, machine, session string) (*db.LogicalConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.convs[[2]string{machine, session}]
	if c == nil {
		return nil, nil
	}
	cp := *c
	cp.Segments = append([]conversationlog.Segment(nil), c.Segments...)
	return &cp, nil
}

func (m *memLogicalStore) LogicalConversations(_ context.Context) ([]db.LogicalConversation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]db.LogicalConversation, 0, len(m.convs))
	for _, c := range m.convs {
		cp := *c
		cp.Segments = append([]conversationlog.Segment(nil), c.Segments...)
		out = append(out, cp)
	}
	return out, nil
}

func (m *memLogicalStore) AdoptConversationAliases(context.Context, string, string, string, string, string, []string) error {
	return nil
}
func (m *memLogicalStore) WithConversationEpoch(ctx context.Context, machine, session string, _ int64, fn func(*db.LogicalConversation) error) error {
	c, err := m.LogicalConversation(ctx, machine, session)
	if err != nil || c == nil {
		return err
	}
	return fn(c)
}
func (m *memLogicalStore) WithDrainedConversationEpoch(context.Context, string, string, int64, func(*db.LogicalConversation) error) error {
	return nil
}
func (m *memLogicalStore) BeginConversationSwitch(context.Context, string, string, db.ConversationSwitch) error {
	return nil
}
func (m *memLogicalStore) PrepareConversationSegment(context.Context, string, string, string, conversationlog.Segment, int) error {
	return nil
}
func (m *memLogicalStore) ActivateConversationSegment(context.Context, string, string, string, int64) error {
	return nil
}
func (m *memLogicalStore) ConversationSwitch(context.Context, string, string, string) (*db.ConversationSwitch, error) {
	return nil, nil
}
func (m *memLogicalStore) ObserveConversationSource(context.Context, string, string, string) error {
	return nil
}
func (m *memLogicalStore) ObserveConversationRevision(context.Context, string, string, int64) error {
	return nil
}
func (m *memLogicalStore) ConversationSegmentClosed(context.Context, string) error { return nil }
func (m *memLogicalStore) EnqueueConversationInput(context.Context, string, string, string, int64, json.RawMessage) error {
	return nil
}
func (m *memLogicalStore) ConversationInputReceipt(context.Context, string, string, string, json.RawMessage) (string, error) {
	return "", nil
}
func (m *memLogicalStore) ClaimConversationInput(context.Context, string, string, int64) (*db.ConversationInput, error) {
	return nil, nil
}

var (
	_ logicalConversationStore = (*memLogicalStore)(nil)
	_ logicalPickStore         = (*memLogicalStore)(nil)
	_ logicalSegmentInputStore = (*memLogicalStore)(nil)
)

func (m *memLogicalStore) ResolveConversationInput(context.Context, string, string, string, string) error {
	return nil
}
func (m *memLogicalStore) ConversationSegmentDispatched(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (m *memLogicalStore) MarkConversationSegmentDispatched(context.Context, string, string, string) error {
	return nil
}
func (m *memLogicalStore) SetConversationPick(_ context.Context, machine, session string, _ int64, launcher, model, effort string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.convs[[2]string{machine, session}]; c != nil {
		for i := range c.Segments {
			if c.Segments[i].State == "active" {
				c.Segments[i].Launcher = launcher
				c.Segments[i].Model = model
				c.Segments[i].Effort = effort
			}
		}
	}
	return nil
}

func TestLogicalSegmentWithoutExplicitModelInheritsConfiguredModel(t *testing.T) {
	t.Run("same launcher inherits shared and main-override models consistently across send and relay", func(t *testing.T) {
		s, f, _ := newMemLogicalFixture(t, reloadSegment("two", 2, "target-agy", "agy|runtime:runtime-agy", "agy", "active"))
		f.mu.Lock()
		f.launcherList = append(f.launcherList, proto.Launcher{Name: "agy", Source: "runtime-agy", Agent: "agy", Assistant: true})
		f.mu.Unlock()
		f.setSessions("server", proto.Session{
			SessionID: "main", Assistant: true, Agent: "agy", Launcher: "agy",
			SourceKind: proto.SourceRuntime, SourceInstanceID: "runtime-agy",
		})

		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy", Model: "gemini-4-neon-xhigh", Effort: "xhigh"})
		userParams := map[string]any{}
		if err := s.sendModel(t.Context(), "server", "main", userParams, s.lang(t.Context())); err != nil {
			t.Fatal(err)
		}
		if userParams["launcher"] != "agy" || userParams["model"] != "gemini-4-neon-xhigh" || userParams["effort"] != "xhigh" {
			t.Fatalf("user sendModel dropped configured model on unpinned segment: %v", userParams)
		}

		// A main-specific override on the same launcher applies equally to user sends and relayed messages.
		setModel(t, s, purposeMain, ModelBinding{Provider: "launcher.agy", Model: "gemini-4-neon-main", Effort: "high"})
		userParams = map[string]any{}
		if err := s.sendModel(t.Context(), "server", "main", userParams, s.lang(t.Context())); err != nil {
			t.Fatal(err)
		}
		if userParams["launcher"] != "agy" || userParams["model"] != "gemini-4-neon-main" || userParams["effort"] != "high" {
			t.Fatalf("user sendModel ignored purposeMain override on unpinned segment: %v", userParams)
		}
		f.sends = nil
		if err := s.deliverRelay(t.Context(), "server", "main", relayMsg{From: "other", Text: "status update", Tool: "conversation_send"}); err != nil {
			t.Fatal(err)
		}
		relayParams := f.lastSend(t)
		if relayParams["launcher"] != "agy" || relayParams["model"] != "gemini-4-neon-main" || relayParams["effort"] != "high" {
			t.Fatalf("relay send diverged from user sendModel: %v", relayParams)
		}
	})

	t.Run("segment explicit effort is preserved while inheriting global model", func(t *testing.T) {
		seg := reloadSegment("two", 2, "target-agy", "agy|runtime:runtime-agy", "agy", "active")
		seg.Effort = "low"
		s, f, _ := newMemLogicalFixture(t, seg)
		f.mu.Lock()
		f.launcherList = append(f.launcherList, proto.Launcher{Name: "agy", Source: "runtime-agy", Agent: "agy", Assistant: true})
		f.mu.Unlock()
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy", Model: "gemini-4-neon-xhigh", Effort: "xhigh"})
		p := map[string]any{}
		if err := s.sendModel(t.Context(), "server", "main", p, s.lang(t.Context())); err != nil {
			t.Fatal(err)
		}
		if p["launcher"] != "agy" || p["model"] != "gemini-4-neon-xhigh" || p["effort"] != "low" {
			t.Fatalf("explicit segment effort lost when inheriting model: %v", p)
		}
	})

	t.Run("segment explicit model stays pinned over global model", func(t *testing.T) {
		seg := reloadSegment("two", 2, "target-agy", "agy|runtime:runtime-agy", "agy", "active")
		seg.Model, seg.Effort = "gemini-4-neon-pinned", "medium"
		s, _, _ := newMemLogicalFixture(t, seg)
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy", Model: "gemini-4-neon-xhigh", Effort: "xhigh"})
		p := map[string]any{}
		if err := s.sendModel(t.Context(), "server", "main", p, s.lang(t.Context())); err != nil {
			t.Fatal(err)
		}
		if p["launcher"] != "agy" || p["model"] != "gemini-4-neon-pinned" || p["effort"] != "medium" {
			t.Fatalf("global model overwrote explicit segment model: %v", p)
		}
	})

	t.Run("different configured launcher never rebinds or injects foreign model into active segment", func(t *testing.T) {
		s, _, _ := newMemLogicalFixture(t, reloadSegment("two", 2, "target-codex", "codex", "codex", "active"))
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "claude-opus", Effort: "high"})
		p := map[string]any{}
		if err := s.sendModel(t.Context(), "server", "main", p, s.lang(t.Context())); err != nil {
			t.Fatal(err)
		}
		if p["launcher"] != "codex" {
			t.Fatalf("rebound active segment to global launcher: %v", p)
		}
		if m, _ := p["model"].(string); m != "" {
			t.Fatalf("injected foreign launcher model into active segment: %v", p)
		}
		if e, _ := p["effort"].(string); e != "" {
			t.Fatalf("injected foreign launcher effort into active segment: %v", p)
		}
	})
}

func TestRuntimeMainInheritsSameLauncherAssistantModelWithoutCrossingLaunchers(t *testing.T) {
	s, f := pickFixture(t)
	f.mu.Lock()
	f.launcherList = append(f.launcherList, proto.Launcher{Name: "agy", Source: "runtime-agy", Agent: "agy", Assistant: true})
	f.known["main"] = true
	f.mu.Unlock()
	f.setSessions("server", proto.Session{
		SessionID: "main", Assistant: true, Agent: "agy", Launcher: "agy",
		SourceKind: proto.SourceRuntime, SourceInstanceID: "runtime-agy",
	})
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.agy", Model: "gemini-4-neon-xhigh", Effort: "xhigh"})

	userParams := map[string]any{}
	if err := s.sendModel(t.Context(), "server", "main", userParams, s.lang(t.Context())); err != nil {
		t.Fatal(err)
	}
	if userParams["launcher"] != "agy" || userParams["model"] != "gemini-4-neon-xhigh" || userParams["effort"] != "xhigh" {
		t.Fatalf("unsegmented runtime main dropped inherited same-launcher model: %v", userParams)
	}

	f.sends = nil
	if err := s.deliverRelay(t.Context(), "server", "main", relayMsg{From: "other", Text: "wake main", Tool: "conversation_send"}); err != nil {
		t.Fatal(err)
	}
	relayParams := f.lastSend(t)
	if relayParams["launcher"] != "agy" || relayParams["model"] != "gemini-4-neon-xhigh" || relayParams["effort"] != "xhigh" {
		t.Fatalf("relay to unsegmented runtime main dropped inherited same-launcher model: %v", relayParams)
	}

	// Switching global assistant to a different launcher must still keep the runtime main off that foreign launcher.
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.gw", Model: "other-model", Effort: "low"})
	userParams = map[string]any{}
	if err := s.sendModel(t.Context(), "server", "main", userParams, s.lang(t.Context())); err != nil {
		t.Fatal(err)
	}
	if userParams["launcher"] != nil || userParams["model"] != nil || userParams["effort"] != nil {
		t.Fatalf("foreign global assistant setting crossed into runtime main: %v", userParams)
	}
	f.sends = nil
	if err := s.deliverRelay(t.Context(), "server", "main", relayMsg{From: "other", Text: "wake main foreign", Tool: "conversation_send"}); err != nil {
		t.Fatal(err)
	}
	relayParams = f.lastSend(t)
	if relayParams["launcher"] != nil || relayParams["model"] != nil || relayParams["effort"] != nil {
		t.Fatalf("foreign global assistant setting crossed into runtime main relay: %v", relayParams)
	}
}
