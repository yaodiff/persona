package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// featureAssistantStartServiceTier is a start option of the default manager, not a hub-global promise: an older
// manager would drop the unknown field and start at Standard while the panel said Fast (#6240).
const featureAssistantStartServiceTier = assistantwire.CapabilityAssistantStartServiceTier

func (h *Hub) assistantStartServiceTierFeatures() []string {
	if !h.defaultManagerNames(assistantwire.CapabilityAssistantStartServiceTier) {
		return nil
	}
	return []string{featureAssistantStartServiceTier}
}

// checkAssistantStartServiceTier refuses an explicit tier before an incapable manager can drop it. Only that rolling
// upgrade path buffers and restores the body; a capable manager's start follows the ordinary relay unchanged.
func (h *Hub) checkAssistantStartServiceTier(w http.ResponseWriter, r *http.Request, id string) bool {
	if h.archiveManagerHas(id, assistantwire.CapabilityAssistantStartServiceTier) != http.StatusNotImplemented || r.Body == nil || r.ContentLength == 0 {
		return true
	}
	body, err := readRelayed(w, r)
	if err != nil {
		writeBodyErr(w, err, err)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var in struct {
		ServiceTier json.RawMessage `json:"service_tier"`
	}
	if json.Unmarshal(body, &in) != nil || len(in.ServiceTier) == 0 || bytes.Equal(in.ServiceTier, []byte("null")) {
		return true
	}
	var tier string
	if json.Unmarshal(in.ServiceTier, &tier) == nil && tier == "" {
		return true
	}
	writeErrCode(w, http.StatusNotImplemented, "assistant_service_tier_manager_outdated", i18n.Err("hub-assistant_link.service_tier_manager_outdated"))
	return false
}
