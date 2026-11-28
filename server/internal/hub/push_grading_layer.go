package hub

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Extension push grading layer helpers and read-only preview (docs/server/server.md "Push grading", issue #14380).
//
// The persisted singleton extension layer lives under hub_settings key hub.push_grading_layer
// (push_grading_store.go, push_grading_layer_lifecycle.go) and is resolved independently per
// manager through pushGradingEffectiveCache (push_grading_effective_cache.go):
//   - When no extension layer belongs to a push's manager, grading uses only the owner's settings.
//   - System alerts (pairing, computer joining) are never modified or held by base grading config or extension layers.
//   - Active owner attention modes (fragment, immersive, quiet) are never weakened by extension layers.
//   - Extension layer writes never read or write the owner's hub.ln_mode switch, cannot set enabled, and cannot
//     hold or silence P0.
//   - Removing the layer or disabling / uninstalling / dropping push.grading prunes hub.push_grading_layer,
//     invalidates the effective-config cache, and restores the owner's settings immediately.
//   - POST /api/v1/push/grading/preview evaluates the exact same pure decision function (evaluatePushGradingDecision)
//     as live applyPushGrading without sending, recording, or mutating any notification, dedup, intercept,
//     delivered-push, or layer state.

const (
	pushGradingLayerBodyLimit = 16 << 10
	pushGradingMaxFieldRunes  = 128

	codeLayerOwnershipDenied = "layer_ownership_denied"

	pushGradingSourceAttention = "attention"
)

// gradingLayerKey uniquely identifies the single push grading layer owned by one extension on one manager.
type gradingLayerKey struct {
	Manager   string
	Extension string
}

func normalizeGradingManager(manager string) string {
	return cmp.Or(strings.TrimSpace(manager), assistantwire.AssistantID)
}

type pushGradingPreviewNotification struct {
	Kind       string `json:"kind,omitempty"`
	Level      string `json:"level,omitempty"`
	RelatedKey string `json:"related_key,omitempty"`
	MachineID  string `json:"machine_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	TodoID     string `json:"todo_id,omitempty"`
	Track      string `json:"track,omitempty"`
	Depth      string `json:"depth,omitempty"`
	Manager    string `json:"manager,omitempty"`
	Extension  string `json:"extension,omitempty"`
	Asked      *bool  `json:"asked,omitempty"`
	Done       bool   `json:"done,omitempty"`
	System     bool   `json:"system,omitempty"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body,omitempty"`
}

type pushGradingPreviewRequest struct {
	Notification pushGradingPreviewNotification
	Draft        *pushGradingConfig
	DropLayer    bool
}

// pushGradingPreviewResponse is the read-only grading verdict returned by
// POST /api/v1/push/grading/preview. Reason carries the neutral evaluator outcome
// ("held", "silenced", "deduped") or an active attention-mode reason ("attn_*");
// LiveReason carries the exact reason live delivery persists on the intercept
// boundary via legacyPushGradingReason (or "attn_*").
type pushGradingPreviewResponse struct {
	Allow              bool              `json:"allow"`
	Held               bool              `json:"held"`
	Action             string            `json:"action"`
	Reason             string            `json:"reason,omitempty"`
	LiveReason         string            `json:"live_reason,omitempty"`
	Hush               bool              `json:"hush"`
	InputLevel         string            `json:"input_level"`
	EffectiveLevel     string            `json:"effective_level"`
	Source             string            `json:"source"`
	OwnerEnabled       bool              `json:"owner_enabled"`
	EffectiveGrading   bool              `json:"effective_grading"`
	AttentionMode      string            `json:"attention_mode"`
	DedupWindowSeconds int               `json:"dedup_window_seconds"`
	DedupHit           bool              `json:"dedup_hit"`
	LayerSource        string            `json:"layer_source,omitempty"`
	By                 map[string]string `json:"by,omitempty"`
}

func validPushGradingKind(kind string) bool {
	switch kind {
	case assistantwire.NotifyInput, assistantwire.NotifyPush, assistantwire.NotifyDone,
		assistantwire.NotifyReply, assistantwire.NotifyDecide, kindAlert:
		return true
	default:
		return false
	}
}

func validPushGradingLevel(level string) bool {
	switch level {
	case assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2:
		return true
	default:
		return false
	}
}

func validPushGradingDepth(depth string) bool {
	return depth == "" || depth == assistantwire.DepthQuick || depth == assistantwire.DepthDeep
}

func validPushGradingField(s string, maxRunes int) bool {
	if s == "" {
		return true
	}
	if strings.TrimSpace(s) != s || utf8.RuneCountInString(s) > maxRunes {
		return false
	}
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func normalizePushLevel(level string) string {
	switch level {
	case assistantwire.LevelP0, assistantwire.LevelP2:
		return level
	default:
		return assistantwire.LevelP1
	}
}

func (n pushGradingPreviewNotification) validate() error {
	if n.Kind != "" && !validPushGradingKind(n.Kind) {
		return errors.New("notification.kind must be input, assistant, done, reply, decide or alert")
	}
	if n.Level != "" && !validPushGradingLevel(n.Level) {
		return errors.New("notification.level must be P0, P1 or P2")
	}
	if !validPushGradingDepth(n.Depth) {
		return errors.New("notification.depth must be quick or deep")
	}
	for name, val := range map[string]string{
		"related_key": n.RelatedKey,
		"machine_id":  n.MachineID,
		"session_id":  n.SessionID,
		"todo_id":     n.TodoID,
		"track":       n.Track,
		"manager":     n.Manager,
		"extension":   n.Extension,
	} {
		if !validPushGradingField(val, pushGradingMaxFieldRunes) {
			return fmt.Errorf("notification.%s is invalid", name)
		}
	}
	if utf8.RuneCountInString(n.Title) > notifyTitleMost {
		return errors.New("notification.title is too long")
	}
	if utf8.RuneCountInString(n.Body) > notifyBodyMost {
		return errors.New("notification.body is too long")
	}
	return nil
}

func decodePushGradingPreviewRequest(raw []byte) (pushGradingPreviewRequest, error) {
	if err := checkUniqueObjectKeys(raw); err != nil {
		return pushGradingPreviewRequest{}, err
	}
	obj, err := decodePushGradingObject(raw, "notification", "layer")
	if err != nil {
		return pushGradingPreviewRequest{}, err
	}
	rawNotif, ok := obj["notification"]
	if !ok || pushGradingNull(rawNotif) {
		return pushGradingPreviewRequest{}, errors.New("notification is required")
	}
	var req pushGradingPreviewRequest
	dec := json.NewDecoder(bytes.NewReader(rawNotif))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req.Notification); err != nil {
		return pushGradingPreviewRequest{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return pushGradingPreviewRequest{}, errors.New("notification must be a single JSON object")
	}
	if err := req.Notification.validate(); err != nil {
		return pushGradingPreviewRequest{}, err
	}
	if rawLayer, hasLayer := obj["layer"]; hasLayer {
		if pushGradingNull(rawLayer) {
			req.DropLayer = true
		} else {
			cfg, err := parsePushGradingConfig(rawLayer)
			if err != nil {
				return pushGradingPreviewRequest{}, err
			}
			if err := cfg.validate(true); err != nil {
				return pushGradingPreviewRequest{}, err
			}
			req.Draft = &cfg
		}
	}
	return req, nil
}

// previewPushGrading evaluates a notification against the owner's settings and
// the manager-scoped singleton extension layer without writing or mutating any
// push, intercept, delivered-push, dedup, or layer state. When draft is non-nil,
// it substitutes for any persisted layer for this evaluation only and is never
// stored; when dropLayer is true, any persisted layer is excluded for this
// evaluation only.
func (p *Pusher) previewPushGrading(
	ctx context.Context,
	policy pushPolicy,
	draft *pushGradingLayer,
	dropLayer bool,
) (pushGradingPreviewResponse, error) {
	now := time.Now()
	if p != nil && p.now != nil {
		now = p.now()
	}
	attn := assistantwire.Attention{Mode: assistantwire.AttentionNormal}
	if p != nil {
		attn = p.attentionNow()
	}
	attnMode := attn.Mode
	if attnMode == "" {
		attnMode = assistantwire.AttentionNormal
	}

	var snap pushGradingSnapshot
	var supported bool
	if p != nil {
		var err error
		snap, supported, err = p.loadPushGradingSnapshotCached(ctx)
		if err != nil {
			return pushGradingPreviewResponse{}, err
		}
	}
	cfg, baseCfg, layerSource, ownerEnabled := p.resolveEffectiveFromSnapshot(snap, supported, policy.manager, draft, dropLayer)

	var eval pushGradingEvaluation
	if p != nil {
		mgr := normalizeGradingManager(policy.manager)
		p.gradingMu.Lock()
		eval = evaluatePushGradingDecision(now, cfg, baseCfg, attn, p.gradingLastAlert[mgr], policy)
		p.gradingMu.Unlock()
	} else {
		eval = evaluatePushGradingDecision(now, cfg, baseCfg, attn, nil, policy)
	}

	return pushGradingPreviewResponse{
		Allow:              eval.Allow,
		Held:               !eval.Allow,
		Action:             eval.Action,
		Reason:             eval.Reason,
		LiveReason:         eval.LiveReason,
		Hush:               eval.Hush,
		InputLevel:         eval.InputLevel,
		EffectiveLevel:     eval.EffectiveLevel,
		Source:             eval.Source,
		OwnerEnabled:       ownerEnabled,
		EffectiveGrading:   attn.Active() || cfg.Enabled,
		AttentionMode:      attnMode,
		DedupWindowSeconds: int(cfg.DedupWindow.Seconds()),
		DedupHit:           eval.DedupHit,
		LayerSource:        layerSource,
		By:                 maps.Clone(cfg.By),
	}, nil
}

// checkUniqueObjectKeys verifies that raw is a single JSON object with no duplicate or case-colliding keys at any depth.
func checkUniqueObjectKeys(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	type frame struct {
		object, wantKey bool
		keys            map[string]bool
	}
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
	values := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if len(stack) == 0 {
			if values++; values > 1 {
				return errors.New("request body must contain a single JSON object")
			}
			if d, ok := tok.(json.Delim); !ok || d != '{' {
				return errors.New("request body must be a JSON object")
			}
		}
		if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
			if d, ok := tok.(json.Delim); ok && d == '}' {
				stack = stack[:n-1]
				valueDone()
				continue
			}
			k, _ := tok.(string)
			lower := strings.ToLower(k)
			if stack[n-1].keys[lower] {
				return fmt.Errorf("duplicate key %q", clip(k, 64))
			}
			stack[n-1].keys[lower] = true
			stack[n-1].wantKey = false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{object: true, wantKey: true, keys: map[string]bool{}})
		case json.Delim('['):
			stack = append(stack, &frame{})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
	if values == 0 || len(stack) > 0 {
		return errors.New("request body must be a JSON object")
	}
	return nil
}

// checkExtensionLayerOwnership verifies that an extension caller has an extension identity and is not targeting
// another extension or manager through headers, query parameters, or payload fields.
func checkExtensionLayerOwnership(w http.ResponseWriter, r *http.Request, p principal, bodyExt, bodyMgr string) (gradingLayerKey, bool) {
	if p.Extension == "" {
		writeErrCode(w, http.StatusForbidden, codeLayerOwnershipDenied, errors.New("extension identity is required"))
		return gradingLayerKey{}, false
	}
	ownMgr := normalizeGradingManager(p.Manager)
	q := r.URL.Query()
	hdrMgr := strings.TrimSpace(r.Header.Get(ManagerHeader))
	qMgr := strings.TrimSpace(q.Get(ManagerQuery))
	bMgr := strings.TrimSpace(bodyMgr)
	if (hdrMgr != "" && hdrMgr != ownMgr) || (qMgr != "" && qMgr != ownMgr) || (bMgr != "" && bMgr != ownMgr) {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
		return gradingLayerKey{}, false
	}
	qExt := strings.TrimSpace(q.Get("extension"))
	if qExt != "" && qExt != p.Extension {
		writeErrCode(w, http.StatusForbidden, codeLayerOwnershipDenied, errors.New("an extension may access only its own push grading layer"))
		return gradingLayerKey{}, false
	}
	if bExt := strings.TrimSpace(bodyExt); bExt != "" && bExt != p.Extension {
		writeErrCode(w, http.StatusForbidden, codeLayerOwnershipDenied, errors.New("an extension may modify only its own push grading layer"))
		return gradingLayerKey{}, false
	}
	return gradingLayerKey{Manager: ownMgr, Extension: p.Extension}, true
}

func isFullCredential(p principal) bool {
	return p.Role == "" || p.Role == roleFull
}

// pushGradingLayerRoutes registers GET/PUT/DELETE /api/v1/push/grading/layer and POST /api/v1/push/grading/preview.
// Extension tokens reach them only with the push.grading scope (extwire/push_grading_scopes.go).
func (h *Hub) pushGradingLayerRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/push/grading/layer", h.handleGetPushGradingLayer)
	api.HandleFunc("PUT /api/v1/push/grading/layer", h.handlePutPushGradingLayer)
	api.HandleFunc("DELETE /api/v1/push/grading/layer", h.handleDeletePushGradingLayer)
	api.HandleFunc("POST /api/v1/push/grading/preview", h.handlePostPushGradingPreview)
}

func (h *Hub) handlePostPushGradingPreview(w http.ResponseWriter, r *http.Request) {
	p := principalOf(r.Context())
	if p.Role != roleExtension && !isFullCredential(p) {
		writeErr(w, http.StatusForbidden, errors.New("this credential cannot preview push grading"))
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
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pushGradingLayerBodyLimit))
	if err != nil {
		writeBodyErr(w, err, err)
		return
	}
	req, err := decodePushGradingPreviewRequest(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var draft *pushGradingLayer
	mgr := normalizeGradingManager(cmp.Or(req.Notification.Manager, namedManager(r)))
	if p.Role == roleExtension {
		if req.Notification.Manager != "" && normalizeGradingManager(req.Notification.Manager) != extKey.Manager {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		if !h.ownsRef(extKey.Manager, req.Notification.MachineID, req.Notification.SessionID) {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		mgr = extKey.Manager
		if req.Draft != nil {
			draft = &pushGradingLayer{
				Source:  pushGradingExtensionSource(extKey.Extension),
				Manager: extKey.Manager,
				Config:  *req.Draft,
			}
		}
	} else if req.Draft != nil {
		draft = &pushGradingLayer{
			Source:  "draft",
			Manager: mgr,
			Config:  *req.Draft,
		}
	}

	kind := req.Notification.Kind
	if kind == "" {
		kind = assistantwire.NotifyPush
	}
	derived := levelFor(kind, req.Notification.Level)
	if kind == kindAlert && req.Notification.Level == "" {
		derived = assistantwire.LevelP1
	}
	done := req.Notification.Done || kind == assistantwire.NotifyDone
	asked := true
	if req.Notification.Asked != nil {
		asked = *req.Notification.Asked
	}
	mainConv := kind == assistantwire.NotifyPush &&
		h.isMainForManager(mgr, req.Notification.MachineID, req.Notification.SessionID)
	policy := pushPolicy{
		kind:         kind,
		level:        derived,
		relatedKey:   req.Notification.RelatedKey,
		todoID:       req.Notification.TodoID,
		machineID:    req.Notification.MachineID,
		sessionID:    req.Notification.SessionID,
		title:        req.Notification.Title,
		body:         req.Notification.Body,
		manager:      mgr,
		extension:    req.Notification.Extension,
		depth:        req.Notification.Depth,
		track:        req.Notification.Track,
		done:         done,
		system:       req.Notification.System,
		derivedLevel: req.Notification.Level == "",
		asked:        asked,
		mainConv:     mainConv,
	}
	decision, err := h.push.previewPushGrading(r.Context(), policy, draft, req.DropLayer)
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, decision)
}
