package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestClarifyRouteRejectsNonOwnerPrincipalsBeforeRelay(t *testing.T) {
	h := newTestHub(t)
	mux := http.NewServeMux()
	h.fleetRoutesRelayed(mux)
	for _, role := range []string{roleAnswer, roleExtension} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/ix_owner_only/clarify",
			strings.NewReader(`{"text":"Why?","client_msg_id":"owner-only-question"}`))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: "restricted", Role: role, Manager: assistantwire.AssistantID}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s clarify = %d %s, want 403", role, w.Code, w.Body)
		}
	}
}

func TestClarifyClientOperationCanRetryOnlyUncertainDelivery(t *testing.T) {
	for _, tc := range []struct {
		code string
		want bool
	}{{assistantwire.CodeAssistantOffline, true}, {assistantwire.CodeAssistantInDoubt, true}, {"conversation_deleted", false}, {"", false}} {
		body := []byte(`{"code":"` + tc.code + `"}`)
		if got := clientOpRetry("clarify", body); got != tc.want {
			t.Errorf("clarify retry for %q = %v, want %v", tc.code, got, tc.want)
		}
		if got := clientOpRetry("resolve", body); got {
			t.Errorf("resolve unexpectedly retries %q", tc.code)
		}
	}
}

type clarifyRetryStore struct {
	*receiptStore
	ownerTokenHash string
}

func (s *clarifyRetryStore) DeviceTokenByHash(_ context.Context, hash string) (*db.DeviceToken, error) {
	if hash != s.ownerTokenHash {
		return nil, nil
	}
	return &db.DeviceToken{ID: "clarify-owner", Role: roleFull, Kind: "phone"}, nil
}

// A clarification whose first relay is in doubt keeps no operation receipt. Its identical retry must carry the same
// generated client_msg_id, or the manager sees two owner questions.
func TestClarifyRetryReusesGeneratedClientMessageID(t *testing.T) {
	h := newTestHub(t)
	const ownerToken = "clarify-retry-owner-token"
	h.store = &clarifyRetryStore{
		receiptStore:   &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}},
		ownerTokenHash: hashToken(ownerToken),
	}
	manager, _ := connectInboxManager(t, h)
	forwarded := make(chan assistantwire.HTTPRequest, 2)
	var calls atomic.Int32
	manager.mu.Lock()
	manager.http = func(_ context.Context, r assistantwire.HTTPRequest) (any, error) {
		forwarded <- r
		if calls.Add(1) == 1 {
			return assistantwire.HTTPResponse{Status: http.StatusBadGateway,
				Body: []byte(`{"code":"assistant_in_doubt","delivered":"unknown"}`)}, nil
		}
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	manager.mu.Unlock()

	const interactionID = "ix_clarify_retry"
	h.reported.interactions = map[string]proto.Interaction{
		interactionID: {ID: interactionID, MachineID: "box", SessionID: "session", Kind: proto.IxPermission,
			Manager: assistantwire.AssistantID, CanClarify: true, CreatedAt: time.Unix(1000, 0).UTC()},
	}
	mux := http.NewServeMux()
	h.fleetRoutesRelayed(mux)
	const opID = "00000000-0000-4000-8000-000000011310"
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+interactionID+"/clarify", strings.NewReader(`{"text":"Why?"}`))
		r.Header.Set("Authorization", "Bearer "+ownerToken)
		r.Header.Set("X-Persona-Op-Id", opID)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: "clarify-owner", Role: roleFull}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	first := post()
	if first.Code != http.StatusBadGateway || !strings.Contains(first.Body.String(), assistantwire.CodeAssistantInDoubt) {
		t.Fatalf("first clarify = %d %s, want assistant_in_doubt", first.Code, first.Body)
	}
	firstForwarded := <-forwarded
	second := post()
	if second.Code != http.StatusOK {
		t.Fatalf("clarify retry = %d %s, want the retry to reach the manager", second.Code, second.Body)
	}
	secondForwarded := <-forwarded
	if got := calls.Load(); got != 2 {
		t.Fatalf("clarify was forwarded %d times, want both attempts", got)
	}

	clientMessageID := func(r assistantwire.HTTPRequest) string {
		if r.Path != "/api/v1/interactions/"+interactionID+"/clarify" {
			t.Fatalf("forwarded path = %q", r.Path)
		}
		var body struct {
			ClientMsgID string `json:"client_msg_id"`
		}
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("forwarded body %q: %v", r.Body, err)
		}
		return body.ClientMsgID
	}
	firstID, secondID := clientMessageID(firstForwarded), clientMessageID(secondForwarded)
	if firstID == "" || secondID == "" {
		t.Fatalf("generated client_msg_id is empty: first %q, retry %q", firstID, secondID)
	}
	if firstID != secondID {
		t.Fatalf("retry generated a new client_msg_id: first %q, retry %q; the owner question would be duplicated", firstID, secondID)
	}
}
