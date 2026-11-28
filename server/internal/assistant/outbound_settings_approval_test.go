package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

const outboundSettingsPath = "/api/v1/assistant/outbound-messages"
const outboundScopeRequest = `{"revision":0,"scope":{"channel":"larkcli","target":"oc_example_group","scenario":"inbox_reply","identity":"bot"}}`

func postOutboundSettingsCard(t *testing.T, s *Service, body string) proto.Interaction {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, "POST", outboundSettingsPath, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	var receipt struct {
		ID     string `json:"approval_id"`
		Status string `json:"status"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &receipt) != nil || receipt.Status != "pending" {
		t.Fatalf("POST must return a pending receipt before any approval: %d %s", w.Code, w.Body)
	}
	card, ok := s.pendingIx(receipt.ID)
	if !ok || card.Kind != proto.IxPermission || !card.Resumable || card.CanClarify ||
		card.MachineID != "" || card.SessionID != "" || card.ExpiresAt.Sub(card.CreatedAt) <= 5*time.Minute {
		t.Fatalf("missing durable settings card: %+v", card)
	}
	t.Cleanup(func() {
		s.fleet.mu.Lock()
		defer s.fleet.mu.Unlock()
		if p := s.fleet.interactions[card.ID]; p != nil && p.orphaned != nil {
			p.orphaned.Stop()
		}
	})
	return card
}

func answerOutboundSettingsCard(s *Service, id string, owner bool, answer string) *httptest.ResponseRecorder {
	ctx := context.Background()
	if owner {
		ctx = devicewire.WithOwnerAnswer(ctx)
	}
	r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/interactions/"+id+"/resolve", strings.NewReader(answer))
	w := httptest.NewRecorder()
	s.fleetHandler(s).ServeHTTP(w, r)
	return w
}

func TestOutboundOwnerApprovalCardShowsScopeAndDoesNotSelfApprove(t *testing.T) {
	database := taskRecoveryDatabase(t)
	old, _ := newTaskServiceOn(t, database)
	card := postOutboundSettingsCard(t, old, outboundScopeRequest)
	if !strings.Contains(card.Summary, "oc_example_group") || !strings.Contains(card.Summary, "larkcli") {
		t.Fatalf("missing explicit scope: %s", card.Summary)
	}
	if w := answerOutboundSettingsCard(old, card.ID, false, `{"allow":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("a forwarded non-owner answer must be refused: %d %s", w.Code, w.Body)
	}
	allow := true
	if err := old.resolve(card.ID, proto.Resolution{Allow: &allow}); err == nil {
		t.Fatal("an internal answer must not approve the settings card")
	}
	if _, err := old.nodeInteraction(t.Context(), card); err == nil {
		t.Fatal("a node must not use the settings-card ID namespace")
	}
	before, err := old.standingAuthorizations(t.Context())
	if err != nil || len(before.Authorizations) != 0 {
		t.Fatalf("active before the owner approved: %+v %v", before, err)
	}

	// The original HTTP context is cancelled, and the new process starts with
	// no waiter or closure from the old one. Only the database is shared.
	restarted, _ := newTaskServiceOn(t, database)
	restarted.restoreInteractions(t.Context())
	t.Cleanup(func() {
		restarted.fleet.mu.Lock()
		defer restarted.fleet.mu.Unlock()
		if p := restarted.fleet.interactions[card.ID]; p != nil && p.orphaned != nil {
			p.orphaned.Stop()
		}
	})
	back, ok := restarted.pendingIx(card.ID)
	if !ok || back.Summary != card.Summary {
		t.Fatalf("pending scope was lost on restart: %+v", back)
	}
	if w := serve(restarted, "GET", outboundSettingsPath, ""); !strings.Contains(w.Body.String(), card.ID) {
		t.Fatalf("refresh did not recover the pending approval: %d %s", w.Code, w.Body)
	}
	if w := answerOutboundSettingsCard(restarted, card.ID, true, `{"allow":true}`); w.Code != http.StatusOK {
		t.Fatalf("late owner approval: %d %s", w.Code, w.Body)
	}
	// An older slot can still have the same pending card in memory. The stored
	// outcome and revision must prevent it from writing another grant.
	_ = answerOutboundSettingsCard(old, card.ID, true, `{"allow":true}`)
	readback := serve(restarted, "GET", "/api/v1/assistant/standing-authorizations", "")
	var registry StandingAuthorizations
	if readback.Code != http.StatusOK || json.Unmarshal(readback.Body.Bytes(), &registry) != nil || len(registry.Authorizations) != 1 ||
		registry.Authorizations[0].Messages == nil || registry.Authorizations[0].Messages.Target != "oc_example_group" || registry.Revision != 1 {
		t.Fatalf("one exact grant must survive refresh: %d %s", readback.Code, readback.Body)
	}
	if w := serve(restarted, "GET", outboundSettingsPath+"?approval="+card.ID, ""); !strings.Contains(w.Body.String(), `"status":"approved"`) {
		t.Fatalf("missing approved receipt: %d %s", w.Code, w.Body)
	}
}

func TestOutboundSettingsDeclineAndStaleApproval(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, _ := newTaskServiceOn(t, database)
	scope := postOutboundSettingsCard(t, s, outboundScopeRequest)
	other := postOutboundSettingsCard(t, s, strings.ReplaceAll(outboundScopeRequest, "oc_example_group", "oc_other"))
	identity := postOutboundSettingsCard(t, s, `{"identity":"user"}`)
	for _, tc := range []struct {
		id, answer, want string
		code             int
	}{
		{scope.ID, `{"allow":true}`, "approved", http.StatusOK},
		{other.ID, `{"allow":true}`, "failed", http.StatusConflict},
		{identity.ID, `{"allow":false}`, "declined", http.StatusOK},
	} {
		if w := answerOutboundSettingsCard(s, tc.id, true, tc.answer); w.Code != tc.code || tc.code == http.StatusConflict && !strings.Contains(w.Body.String(), "outbound_settings_changed") {
			t.Fatalf("answer %s: %d %s", tc.want, w.Code, w.Body)
		}
		if w := serve(s, "GET", outboundSettingsPath+"?approval="+tc.id, ""); !strings.Contains(w.Body.String(), `"status":"`+tc.want+`"`) {
			t.Fatalf("expected %s, got %d %s", tc.want, w.Code, w.Body)
		}
	}
	list, err := s.standingAuthorizations(t.Context())
	if err != nil || len(list.Authorizations) != 1 || list.Revision != 1 {
		t.Fatalf("stale card changed the registry: %+v %v", list, err)
	}
	if sender, err := s.outboundIdentity(t.Context()); err != nil || sender != "bot" {
		t.Fatalf("declined identity changed the setting: %q %v", sender, err)
	}
	identity = postOutboundSettingsCard(t, s, `{"identity":"user"}`)
	if w := answerOutboundSettingsCard(s, identity.ID, true, `{"allow":true}`); w.Code != http.StatusOK {
		t.Fatalf("approve identity: %d %s", w.Code, w.Body)
	}
	if sender, err := s.outboundIdentity(t.Context()); err != nil || sender != "user" {
		t.Fatalf("approved identity was not applied: %q %v", sender, err)
	}
}

func TestOutboundSettingsOnlyAdvertisesAnswerableCards(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, _ := newTaskServiceOn(t, database)
	first := postOutboundSettingsCard(t, s, outboundScopeRequest)
	latest := postOutboundSettingsCard(t, s, `{"identity":"user"}`)
	s.fleet.mu.Lock()
	s.fleet.interactions[latest.ID].claimed = true
	s.fleet.mu.Unlock()
	w := serve(s, "GET", outboundSettingsPath, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), first.ID) || strings.Contains(w.Body.String(), latest.ID) {
		t.Fatalf("should return only the answerable card: %d %s", w.Code, w.Body)
	}
}

func TestOutboundSettingsConcurrentIdentityChangeReturnsConflict(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, _ := newTaskServiceOn(t, database)
	card := postOutboundSettingsCard(t, s, `{"identity":"user"}`)
	if err := s.store().PutSetting(t.Context(), outboundIdentityKey, "user"); err != nil {
		t.Fatal(err)
	}
	w := answerOutboundSettingsCard(s, card.ID, true, `{"allow":true}`)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "outbound_settings_changed") {
		t.Fatalf("stale database write: %d %s", w.Code, w.Body)
	}
	w = serve(s, "GET", outboundSettingsPath+"?approval="+card.ID, "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"failed"`) {
		t.Fatalf("conflict receipt: %d %s", w.Code, w.Body)
	}
}
