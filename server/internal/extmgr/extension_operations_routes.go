package extmgr

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// opsView is the operations to list (newest first), with an expired preview's one canceled.
func (h *Manager) opsView() []ExtensionOperation {
	ps := h.extensions
	h.expireOps()
	ps.ops.mu.Lock()
	ps.ops.prune(time.Now())
	ops := slices.Collect(maps.Values(ps.ops.byID))
	ps.ops.mu.Unlock()
	out := []ExtensionOperation{}
	for _, op := range ops {
		out = append(out, h.snapshot(op))
	}
	slices.SortFunc(out, func(a, b ExtensionOperation) int { return b.StartedAt.Compare(a.StartedAt) })
	return out
}

// withOperation is v's JSON with "operation_id" added (the synchronous answers name their operation too).
func withOperation(v any, op *extOp) any {
	if op == nil {
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return v
	}
	m["operation_id"], _ = json.Marshal(op.v.ID) // set before the operation is shared, never changed
	return m
}

// writeOp answers 202 with op.
func (h *Manager) writeOp(w http.ResponseWriter, op *extOp) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	raw, err := json.Marshal(map[string]any{"operation": h.snapshot(op)})
	if err == nil {
		_, _ = w.Write(i18n.ExtensionClientJSON(i18n.OfWriter(w), raw))
	}
}

// operationRoutes list, show, retry and cancel the operations. Who may is who may start them: a full-rights device
// for a market's extension, the owner over loopback for everything.
func (h *Manager) operationRoutes(api *http.ServeMux) {
	may := func(r *http.Request, op *extOp) bool {
		return h.fromThisMachine(r) || (principalOf(r.Context()).Role == roleFull && (op == nil || op.ofMarket))
	}
	gate := func(f func(w http.ResponseWriter, r *http.Request, op *extOp)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if h.extensions == nil || h.extensions.reg == nil {
				writeExtensionResponseError(w, r, http.StatusServiceUnavailable, "no_plugins", errors.New("plugins are not set up"))
				return
			}
			var op *extOp
			if id := r.PathValue("op"); id != "" {
				if op = h.opByID(id); op == nil {
					writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", i18n.Err("hub-extension_operations.not_found"))
					return
				}
			}
			if !may(r, op) {
				writeExtensionResponseError(w, r, http.StatusForbidden, "this_machine_only", i18n.Err("hub-extension_routes.market_only"))
				return
			}
			f(w, r, op)
		}
	}
	api.HandleFunc("GET /api/v1/extensions/-/operations", gate(func(w http.ResponseWriter, r *http.Request, _ *extOp) {
		writeExtensionJSON(w, r, map[string]any{"operations": h.opsView()})
	}))
	api.HandleFunc("GET /api/v1/extensions/-/operations/{op}", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		h.expireOps()
		ps := h.extensions
		v := h.snapshot(op)
		if v.State == opNeedsConsent {
			if pv := ps.reg.PreviewOf(v.PreviewID); pv != nil {
				v.Preview = pv.In(h.requestLang(r))
			}
		}
		writeExtensionJSON(w, r, v)
	}))
	api.HandleFunc("POST /api/v1/extensions/-/operations/{op}/retry", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		ps := h.extensions
		// Taken under the lock, so a failed operation is retried once: a second POST (a double tap, a second app)
		// finds it gone and starts nothing; the new operation carries on.
		ps.ops.mu.Lock()
		state, retry := op.v.State, op.retry
		if state == opFailed && retry != nil {
			op.retry, op.v.Retryable = nil, false
		}
		ps.ops.mu.Unlock()
		switch {
		case state != opFailed:
			writeExtensionResponseError(w, r, http.StatusConflict, "not_failed", i18n.Err("hub-extension_operations.not_failed"))
			return
		case retry == nil:
			writeExtensionResponseError(w, r, http.StatusConflict, "not_retryable", i18n.Err("hub-extension_operations.not_retryable"))
			return
		}
		h.pushOp(op, true) // the apps hide its Retry
		next, err := retry()
		if err != nil {
			// It did not start: give the retry back, so the user can try again.
			ps.ops.mu.Lock()
			op.retry, op.v.Retryable = retry, true
			ps.ops.mu.Unlock()
			h.pushOp(op, true)
			writeExtensionResponseError(w, r, http.StatusBadRequest, "retry_failed", err)
			return
		}
		h.writeOp(w, next)
	}))
	api.HandleFunc("DELETE /api/v1/extensions/-/operations/{op}", gate(func(w http.ResponseWriter, r *http.Request, op *extOp) {
		ps := h.extensions
		ps.ops.mu.Lock()
		state, previewID, cancel := op.v.State, op.v.PreviewID, op.cancel
		consent := stepOf(&op.v, extension.StepConsent) // an uninstall has none
		previewing := consent != nil && consent.State == extension.StepPending
		canceledConsent := false
		var cleanups []func()
		switch {
		case state == opRunning && previewing && cancel != nil:
			op.canceling = true
		case state == opRunning:
		case state == opNeedsConsent:
			// Transition to canceled and detach the primary and bundle member previews
			// from the registry atomically under ops.mu so a concurrent POST /install
			// cannot either claim the op or fall back to installing the canceled preview.
			if bundleIDs, ok := cancelOpLocked(op); ok {
				op.v.UpdatedAt = time.Now()
				canceledConsent = true
				if c := ps.reg.DetachPreview(previewID); c != nil {
					cleanups = append(cleanups, c)
				}
				for _, bid := range bundleIDs {
					if c := ps.reg.DetachPreview(bid); c != nil {
						cleanups = append(cleanups, c)
					}
				}
			}
		default:
			delete(ps.ops.byID, op.v.ID)
		}
		ps.ops.mu.Unlock()
		switch {
		case state == opRunning && previewing && cancel != nil:
			cancel(extension.ErrUserCanceled) // the preview fails with it, and the operation is canceled
		case state == opRunning:
			writeExtensionResponseError(w, r, http.StatusConflict, "cannot_cancel", i18n.Err("hub-extension_operations.cannot_cancel"))
			return
		case canceledConsent:
			for _, c := range cleanups {
				c()
			}
			h.pushOp(op, true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
}
