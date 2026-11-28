package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
)

// detachMemStore extends memStore with in-memory setting persistence so the
// real assistant.Service over the bus can journal detached approvals and grants
// without requiring Postgres.
type detachMemStore struct {
	*memStore
	mu       sync.Mutex
	settings map[string]json.RawMessage
}

func newDetachMemStore() *detachMemStore {
	return &detachMemStore{memStore: newMemStore(), settings: map[string]json.RawMessage{}}
}

func (s *detachMemStore) Setting(_ context.Context, key string, dst any) (bool, error) {
	s.mu.Lock()
	raw, ok := s.settings[key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, err
	}
	return true, nil
}

func (s *detachMemStore) PutSetting(_ context.Context, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value == nil {
		delete(s.settings, key)
		return nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.settings[key] = raw
	return nil
}

// End-to-end Hub -> Bus WebSocket -> Assistant Manager verification:
// POST /api/v1/sessions/{mid}/{sid}/messages from an authenticated owner device
// immediately detaches a pending Detachable command approval card in that
// assistant conversation well before the 75s initial wait, while keeping the
// card pending in the Hub's interaction inventory with the extended 24h window.
func TestHubSendMessageDetachesPendingCommandApprovalOverBus(t *testing.T) {
	h := newTestHub(t)
	h.store = newSentStore()
	pipeNode(t, h, "box", func(context.Context) error { return nil })
	m := h.byID["box"]
	m.info.Home = "/home/u"
	editSessions(m, func([]proto.Session) []proto.Session {
		return []proto.Session{{
			SessionID: "s1",
			Assistant: true,
			Agent:     "claude",
			Cwd:       "/home/u/.persona/assistant",
			Status:    proto.StatusWorking,
		}}
	})
	st := newDetachMemStore()
	a := connectAssistant(t, h, st)
	eventually(t, 10*time.Second, "initial interaction snapshot", func() bool {
		h.reported.mu.Lock()
		defer h.reported.mu.Unlock()
		return h.reported.interactions != nil
	})

	ix := proto.Interaction{
		ID:           "ix_hub_bus_detach",
		MachineID:    "box",
		SessionID:    "s1",
		Cwd:          "/home/u/.persona/assistant",
		ExecutionCwd: "/home/u/.persona/assistant",
		Kind:         proto.IxPermission,
		OutwardWrite: true,
		ToolName:     "Bash",
		ToolInput:    json.RawMessage(`{"command":"printf bus_detach"}`),
		Detachable:   true,
		Resumable:    true,
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    time.Now().Add(time.Hour).UTC(),
	}
	raw, _ := json.Marshal(map[string]any{"interaction": ix})
	type reqRes struct {
		val     any
		handled bool
		err     error
	}
	done := make(chan reqRes, 1)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		_, _ = a.svc.Action(context.Background(), assistant.Action{
			Op:            "answer",
			InteractionID: ix.ID,
			Answer:        json.RawMessage(`{"allow":false}`),
		})
		cancel()
	})
	go func() {
		v, ok, err := a.svc.NodeRequest(ctx, "box", "interaction.create", raw)
		done <- reqRes{val: v, handled: ok, err: err}
	}()

	eventually(t, 5*time.Second, "pending detachable card published to hub", func() bool {
		for _, card := range h.pendingInteractions() {
			if card.ID == ix.ID {
				return true
			}
		}
		return false
	})

	// Send an owner chat message through Hub HTTP sendMessage (full device principal).
	req := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(`{"text":"先等一下，帮我看另一个问题","client_msg_id":"hub-owner-1"}`))
	req = req.WithContext(context.WithValue(req.Context(), principalKey{}, principal{ID: "device-owner-1", Role: roleFull}))
	req.SetPathValue("sid", "s1")
	rec := httptest.NewRecorder()
	h.sendMessage(rec, req, m)
	if rec.Code != http.StatusOK {
		t.Fatalf("sendMessage = %d %s, want 200", rec.Code, rec.Body.String())
	}

	// The blocked interaction.create call must release promptly (well under 75s).
	select {
	case got := <-done:
		if got.err != nil || !got.handled {
			t.Fatalf("NodeRequest = %+v, handled=%v, err=%v", got.val, got.handled, got.err)
		}
		res, ok := got.val.(proto.Resolution)
		if !ok {
			t.Fatalf("NodeRequest result type = %T (%+v), want proto.Resolution", got.val, got.val)
		}
		if !res.Dismiss || !res.Pending || res.Allow != nil || res.ApprovalID != ix.ID ||
			!strings.Contains(res.Message, approvalops.WaitingText) ||
			!strings.Contains(res.Message, "The command was not executed.") {
			t.Fatalf("detached resolution = %+v, want non-final Pending WaitingText", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("interaction.create did not detach promptly after Hub sendMessage")
	}

	// Card remains pending on the hub with its extended ~24h waiting deadline.
	eventually(t, 5*time.Second, "extended waiting deadline published to hub", func() bool {
		for _, card := range h.pendingInteractions() {
			if card.ID == ix.ID && time.Until(card.ExpiresAt) > 20*time.Hour {
				return true
			}
		}
		return false
	})
}
