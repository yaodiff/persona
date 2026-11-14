package devices

import (
	"encoding/json"
	"net/http"

	"github.com/nwverse/persona/server/internal/apierr"
)

// getFileOperation reads a node's copy / zip progress through the manager; the original POST still owns the outcome.
func (d *Devices) getFileOperation(w http.ResponseWriter, r *http.Request, m *Machine) {
	var out json.RawMessage
	if err := m.call(r.Context(), "file.operation", map[string]any{"operation_id": r.PathValue("oid")}, &out); err != nil {
		apierr.Call(w, err)
		return
	}
	writeJSON(w, out)
}
