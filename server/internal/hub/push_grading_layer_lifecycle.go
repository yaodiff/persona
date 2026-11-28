package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	codePushGradingLayerTaken     = "layer_taken"
	pushGradingReconcileRetryBase = time.Second
	pushGradingReconcileRetryMax  = 30 * time.Second
)

// pushGradingLayerView is the public HTTP representation of the persisted
// singleton extension layer. Manager stays internal to storage and ownership
// enforcement; Source carries the public extension:<id> attribution.
type pushGradingLayerView struct {
	Source    string            `json:"source"`
	UpdatedAt time.Time         `json:"updated_at"`
	Revision  int64             `json:"revision"`
	Config    pushGradingConfig `json:"config"`
}

type pushGradingLayerResponse struct {
	Layer         *pushGradingLayerView `json:"layer"`
	OwnerEnabled  bool                  `json:"owner_enabled"`
	AttentionMode string                `json:"attention_mode"`
}

type pushGradingLayerDeleteResponse struct {
	OK            bool   `json:"ok"`
	Removed       bool   `json:"removed"`
	OwnerEnabled  bool   `json:"owner_enabled"`
	AttentionMode string `json:"attention_mode"`
}

func pushGradingLayerViewOf(layer *pushGradingLayer) *pushGradingLayerView {
	if layer == nil {
		return nil
	}
	return &pushGradingLayerView{
		Source:    layer.Source,
		UpdatedAt: layer.UpdatedAt,
		Revision:  layer.Revision,
		Config:    layer.Config,
	}
}

// pushGradingLayerReconciler coalesces per-manager extension lifecycle reports
// off the bus read loop and serializes singleton layer writes with pruning so
// stale reports cannot delete a newer claim after re-enable.
type pushGradingLayerReconciler struct {
	writeMu        sync.Mutex
	mu             sync.Mutex
	pending        map[string][]string
	deferred       map[string][]string
	running        bool
	idle           chan struct{}
	retryScheduled bool
	retryDelay     time.Duration
	retryBase      time.Duration
	retryMax       time.Duration
}

func (h *Hub) pushGradingLayerStore() Store {
	if h == nil {
		return nil
	}
	if h.push != nil && h.push.store != nil {
		return h.push.store
	}
	return h.store
}

func (h *Hub) pushGradingStoreTimeout() time.Duration {
	if h != nil && h.push != nil && h.push.storeTimeout > 0 {
		return h.push.storeTimeout
	}
	return deviceStoreTimeout
}

func (h *Hub) pushGradingNow() time.Time {
	if h != nil && h.push != nil && h.push.now != nil {
		return h.push.now()
	}
	return time.Now()
}

func (h *Hub) pushGradingLogf(format string, args ...any) {
	if h != nil && h.push != nil && h.push.logf != nil {
		h.push.logf(format, args...)
	} else if h != nil && h.logger != nil {
		h.logger.Printf(format, args...)
	}
}

func (h *Hub) pushGradingOwnerStatus() (ownerEnabled bool, attentionMode string) {
	if h == nil {
		return false, ""
	}
	return h.push.pushGradingOn(), h.push.attentionNow().Mode
}

func (h *Hub) withPushGradingLayerWrite(ctx context.Context, fn func(context.Context, Store) error) error {
	store := h.pushGradingLayerStore()
	if store == nil {
		return errors.New("push grading storage is not available")
	}
	if !quiesceHeld(ctx) {
		if !h.quiesce.begin() {
			return i18n.Err("hub-selfupdate.err_quiesced")
		}
		defer h.quiesce.end()
	}
	h.gradingReconcile.writeMu.Lock()
	defer h.gradingReconcile.writeMu.Unlock()
	wctx, cancel := context.WithTimeout(ctx, h.pushGradingStoreTimeout())
	defer cancel()
	return fn(wctx, store)
}

func (h *Hub) handleGetPushGradingLayer(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Role != roleExtension && !isFullCredential(p) {
		writeErr(w, http.StatusForbidden, errors.New("this credential cannot read push grading layers"))
		return
	}
	var extKey gradingLayerKey
	if p.Role == roleExtension {
		var ok bool
		extKey, ok = checkExtensionLayerOwnership(w, r, p, "", "")
		if !ok {
			return
		}
	}
	store := h.pushGradingLayerStore()
	if store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("push grading storage is not available"))
		return
	}
	rctx, cancel := context.WithTimeout(r.Context(), h.pushGradingStoreTimeout())
	snapshot, err := readPushGradingSnapshot(rctx, store)
	cancel()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	var view *pushGradingLayerView
	if snapshot.Layer != nil {
		layerMgr := normalizeGradingManager(snapshot.Layer.Manager)
		if p.Role == roleExtension {
			if layerMgr == extKey.Manager && snapshot.Layer.Source == pushGradingExtensionSource(extKey.Extension) {
				view = pushGradingLayerViewOf(snapshot.Layer)
			}
		} else {
			qMgr := strings.TrimSpace(namedManager(r))
			qExt := strings.TrimSpace(r.URL.Query().Get("extension"))
			mgrOK := qMgr == "" || layerMgr == normalizeGradingManager(qMgr)
			extOK := qExt == "" || snapshot.Layer.Source == pushGradingExtensionSource(qExt)
			if mgrOK && extOK {
				view = pushGradingLayerViewOf(snapshot.Layer)
			}
		}
	}
	ownerEnabled, attentionMode := h.pushGradingOwnerStatus()
	writeJSON(w, pushGradingLayerResponse{
		Layer:         view,
		OwnerEnabled:  ownerEnabled,
		AttentionMode: attentionMode,
	})
}

func (h *Hub) handlePutPushGradingLayer(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Role != roleExtension {
		writeErr(w, http.StatusForbidden, errors.New("push grading layers belong to extensions; use PUT /api/v1/push/grading for owner settings"))
		return
	}
	key, ok := checkExtensionLayerOwnership(w, r, p, "", "")
	if !ok {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pushGradingLayerBodyLimit))
	if err != nil {
		writeBodyErr(w, err, err)
		return
	}
	if err := checkUniqueObjectKeys(raw); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := parsePushGradingConfig(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := cfg.validate(true); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	layer := pushGradingLayer{
		Source:    pushGradingExtensionSource(key.Extension),
		Manager:   key.Manager,
		UpdatedAt: h.pushGradingNow().UTC(),
		Config:    cfg,
	}
	var saved pushGradingLayer
	err = h.withPushGradingLayerWrite(r.Context(), func(wctx context.Context, store Store) error {
		var writeErr error
		saved, writeErr = writePushGradingLayer(wctx, store, layer)
		return writeErr
	})
	if errors.Is(err, errPushGradingLayerTaken) {
		writeErrCode(w, http.StatusConflict, codePushGradingLayerTaken, errors.New("push grading layer is already owned by another extension"))
		return
	}
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	h.push.invalidatePushGradingCache()
	h.assistantUpdated()
	ownerEnabled, attentionMode := h.pushGradingOwnerStatus()
	writeJSON(w, pushGradingLayerResponse{
		Layer:         pushGradingLayerViewOf(&saved),
		OwnerEnabled:  ownerEnabled,
		AttentionMode: attentionMode,
	})
}

func (h *Hub) handleDeletePushGradingLayer(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Role != roleExtension && !isFullCredential(p) {
		writeErr(w, http.StatusForbidden, errors.New("this credential cannot remove push grading layers"))
		return
	}
	if p.Role == roleExtension {
		key, ok := checkExtensionLayerOwnership(w, r, p, "", "")
		if !ok {
			return
		}
		var revision int64
		if rawRev := strings.TrimSpace(r.URL.Query().Get("revision")); rawRev != "" {
			parsed, err := strconv.ParseInt(rawRev, 10, 64)
			if err != nil || parsed < 1 {
				writeErr(w, http.StatusBadRequest, errors.New("revision must be a positive integer"))
				return
			}
			revision = parsed
		}
		var removed bool
		err := h.withPushGradingLayerWrite(r.Context(), func(wctx context.Context, store Store) error {
			var delErr error
			removed, delErr = deletePushGradingLayer(wctx, store, key.Manager, pushGradingExtensionSource(key.Extension), revision)
			return delErr
		})
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, err)
			return
		}
		if removed {
			h.push.invalidatePushGradingCache()
			h.assistantUpdated()
		}
		ownerEnabled, attentionMode := h.pushGradingOwnerStatus()
		writeJSON(w, pushGradingLayerDeleteResponse{
			OK:            true,
			Removed:       removed,
			OwnerEnabled:  ownerEnabled,
			AttentionMode: attentionMode,
		})
		return
	}
	qMgr := strings.TrimSpace(namedManager(r))
	if qMgr != "" {
		qMgr = normalizeGradingManager(qMgr)
	}
	source := pushGradingExtensionSource(r.URL.Query().Get("extension"))
	var removed bool
	err := h.withPushGradingLayerWrite(r.Context(), func(wctx context.Context, store Store) error {
		var delErr error
		removed, delErr = clearPushGradingLayer(wctx, store, qMgr, source)
		return delErr
	})
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	if removed {
		h.push.invalidatePushGradingCache()
		h.assistantUpdated()
	}
	ownerEnabled, attentionMode := h.pushGradingOwnerStatus()
	writeJSON(w, pushGradingLayerDeleteResponse{
		OK:            true,
		Removed:       removed,
		OwnerEnabled:  ownerEnabled,
		AttentionMode: attentionMode,
	})
}

// reconcilePushGradingLayer schedules pruning of the singleton hub.push_grading_layer
// entry for manager when its owning extension is no longer in activeExtensions
// (disabled, uninstalled, incompatible, or no longer granted push.grading).
func (h *Hub) reconcilePushGradingLayer(manager string, activeExtensions []string) {
	if h == nil || h.pushGradingLayerStore() == nil {
		return
	}
	mgr := normalizeGradingManager(manager)
	active := slices.Clone(activeExtensions)
	if active == nil {
		active = []string{}
	}
	rec := &h.gradingReconcile
	rec.mu.Lock()
	if rec.pending == nil {
		rec.pending = map[string][]string{}
	}
	rec.pending[mgr] = active
	delete(rec.deferred, mgr)
	if rec.running {
		rec.mu.Unlock()
		return
	}
	rec.running = true
	if rec.idle == nil {
		rec.idle = make(chan struct{})
	}
	h.nodeWorkers.Add(1)
	rec.mu.Unlock()
	go h.runPushGradingLayerReconcile()
}

// resumePushGradingLayerReconcile restarts any layer reconciliation deferred
// while a snapshot paused hub writes (quiesce.go) or after a transient store error.
func (h *Hub) resumePushGradingLayerReconcile() {
	if h == nil || h.pushGradingLayerStore() == nil {
		return
	}
	rec := &h.gradingReconcile
	rec.mu.Lock()
	if len(rec.deferred) > 0 {
		if rec.pending == nil {
			rec.pending = map[string][]string{}
		}
		for mgr, active := range rec.deferred {
			if _, exists := rec.pending[mgr]; !exists {
				rec.pending[mgr] = active
			}
			delete(rec.deferred, mgr)
		}
	}
	if len(rec.pending) == 0 || rec.running {
		rec.mu.Unlock()
		return
	}
	rec.running = true
	if rec.idle == nil {
		rec.idle = make(chan struct{})
	}
	h.nodeWorkers.Add(1)
	rec.mu.Unlock()
	go h.runPushGradingLayerReconcile()
}

func (rec *pushGradingLayerReconciler) stopLocked() {
	rec.running = false
	if rec.idle != nil {
		close(rec.idle)
		rec.idle = nil
	}
}

func (h *Hub) runPushGradingLayerReconcile() {
	defer h.nodeWorkers.Done()
	rec := &h.gradingReconcile
	for {
		rec.mu.Lock()
		if len(rec.pending) == 0 {
			rec.stopLocked()
			rec.mu.Unlock()
			return
		}
		rec.mu.Unlock()

		if !h.quiesce.beginOrDefer(func() {
			rec.mu.Lock()
			rec.stopLocked()
			rec.mu.Unlock()
		}) {
			return
		}

		rec.writeMu.Lock()
		rec.mu.Lock()
		var mgr string
		var active []string
		for k := range rec.pending {
			if mgr == "" || k < mgr {
				mgr = k
			}
		}
		if mgr != "" {
			active = rec.pending[mgr]
			delete(rec.pending, mgr)
		}
		rec.mu.Unlock()

		var pruned bool
		var err error
		if mgr != "" {
			if store := h.pushGradingLayerStore(); store != nil {
				ctx, cancel := context.WithTimeout(context.Background(), h.pushGradingStoreTimeout())
				pruned, err = prunePushGradingLayer(ctx, store, mgr, active)
				cancel()
			}
		}
		if err == nil && pruned {
			h.push.invalidatePushGradingCache()
		}
		rec.writeMu.Unlock()
		if err == nil && pruned {
			h.assistantUpdated()
		}

		// Record any transient failure before releasing the quiesce slot so a
		// concurrent pause/resume cannot observe an empty deferred map and skip retry.
		var retryDelay time.Duration
		if err != nil {
			h.pushGradingLogf("push grading: reconcile layer for %s: %v", mgr, err)
			rec.mu.Lock()
			if _, superseded := rec.pending[mgr]; !superseded {
				if rec.deferred == nil {
					rec.deferred = map[string][]string{}
				}
				rec.deferred[mgr] = active
			}
			if len(rec.deferred) > 0 && !rec.retryScheduled && h.deployCtx != nil && h.deployCtx.Err() == nil {
				base := rec.retryBase
				if base <= 0 {
					base = pushGradingReconcileRetryBase
				}
				maxDelay := rec.retryMax
				if maxDelay <= 0 {
					maxDelay = pushGradingReconcileRetryMax
				}
				if maxDelay < base {
					maxDelay = base
				}
				if rec.retryDelay <= 0 {
					rec.retryDelay = base
				} else {
					rec.retryDelay = min(rec.retryDelay*2, maxDelay)
				}
				retryDelay = rec.retryDelay
				rec.retryScheduled = true
				h.nodeWorkers.Add(1)
			}
			rec.mu.Unlock()
		} else {
			rec.mu.Lock()
			if len(rec.deferred) == 0 {
				rec.retryDelay = 0
			}
			rec.mu.Unlock()
		}

		h.quiesce.end()
		if retryDelay > 0 {
			go h.waitAndResumePushGradingLayerReconcile(retryDelay)
		}
	}
}

func (h *Hub) waitAndResumePushGradingLayerReconcile(delay time.Duration) {
	defer h.nodeWorkers.Done()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-h.deployCtx.Done():
		rec := &h.gradingReconcile
		rec.mu.Lock()
		rec.retryScheduled = false
		rec.mu.Unlock()
		return
	case <-timer.C:
	}
	rec := &h.gradingReconcile
	rec.mu.Lock()
	rec.retryScheduled = false
	hasDeferred := len(rec.deferred) > 0
	rec.mu.Unlock()
	if hasDeferred && h.deployCtx.Err() == nil {
		h.resumePushGradingLayerReconcile()
	}
}

// waitPushGradingLayerReconciled waits for the background layer reconciliation
// worker to become idle (for tests, without sleeping). It returns true when all
// queued reconciliations succeeded without deferred failures.
func (h *Hub) waitPushGradingLayerReconciled(ctx context.Context) bool {
	if h == nil {
		return true
	}
	rec := &h.gradingReconcile
	for {
		rec.mu.Lock()
		running := rec.running
		pending := len(rec.pending)
		deferred := len(rec.deferred)
		ch := rec.idle
		rec.mu.Unlock()
		if !running {
			return pending == 0 && deferred == 0
		}
		if ch == nil {
			return false
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return false
		}
	}
}
