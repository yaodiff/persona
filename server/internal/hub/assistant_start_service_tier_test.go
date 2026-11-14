package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestAssistantStartServiceTierFollowsManagerCapability(t *testing.T) {
	h, m, raw := handoffBusHubWithCapabilities(t, nil)
	if slices.Contains(h.features(), featureAssistantStartServiceTier) {
		t.Fatal("an older manager advertised first-message Fast")
	}
	start := h.startConversationOnce()
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(`{"prompt":"hi","service_tier":"priority"}`))
		req.Header.Set("Accept-Language", string(lang))
		rec := httptest.NewRecorder()
		w := inboxLocaleWriter{rec, lang}
		start(w, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusNotImplemented || body["code"] != "assistant_service_tier_manager_outdated" ||
			body["error"] != i18n.T(lang, "hub-assistant_link.service_tier_manager_outdated") {
			t.Fatalf("%s: %d %v", lang, rec.Code, body)
		}
		if relayed, _, _ := m.took(); len(relayed) != 0 {
			t.Fatalf("%s: an older manager received Fast: %+v", lang, relayed)
		}
	}

	for _, body := range []string{`{"prompt":"hi","service_tier":123}`, `{"prompt":"hi","service_tier":{}}`} {
		w := httptest.NewRecorder()
		start(w, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body)))
		if w.Code != http.StatusNotImplemented {
			t.Fatalf("a nonempty tier reached an older manager: %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"prompt":"hi"}`, `{"prompt":"hi","service_tier":""}`, `{"prompt":"hi","service_tier":null}`} {
		w := httptest.NewRecorder()
		start(w, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body)))
		relayed, _, _ := m.took()
		if w.Code != http.StatusOK || len(relayed) != 1 || string(relayed[0].Body) != body {
			t.Fatalf("an untouched start changed: %s: %d %s relayed %+v", body, w.Code, w.Body, relayed)
		}
	}

	if err := raw.call("manager.hello", map[string]any{"id": assistantwire.AssistantID,
		"capabilities": []string{assistantwire.CapabilityAssistantStartServiceTier}}, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(h.features(), featureAssistantStartServiceTier) {
		t.Fatal("the capable default manager did not advertise first-message Fast")
	}
	const body = `{"prompt":"hi","effort":"high","service_tier":"priority"}`
	w := httptest.NewRecorder()
	start(w, httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(body)))
	relayed, _, _ := m.took()
	if w.Code != http.StatusOK || len(relayed) != 1 || string(relayed[0].Body) != body {
		t.Fatalf("capable start: %d %s relayed %+v", w.Code, w.Body, relayed)
	}

	if err := raw.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the default manager offline", func() bool { return h.busConnOf(assistantwire.AssistantID) == nil })
	if slices.Contains(h.features(), featureAssistantStartServiceTier) {
		t.Fatal("an offline manager still advertised first-message Fast")
	}
}
