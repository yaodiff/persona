package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var clientOpUUID = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Body is encoded as bytes so JSONB cannot change whitespace or the original response bytes.
type clientOpResult struct {
	Status      int    `json:"status"`
	Body        []byte `json:"body"`
	ContentType string `json:"content_type,omitempty"`
}

func (h *Hub) withClientOp(kind string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		// Only clients that send X-Persona-Op-Id use receipts. Messages without it (today's iOS) are deduplicated by
		// client_msg_id in sendMessage (sent_messages), so a non-UUID client_msg_id must still go through.
		id := r.Header.Get("X-Persona-Op-Id")
		if id == "" {
			next(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		_ = r.Body.Close()
		if err != nil {
			writeBodyErr(w, err, err)
			return
		}
		params := normalizedOpParams(raw)
		object, _ := params.(map[string]any)
		if !clientOpUUID.MatchString(id) {
			writeErr(w, http.StatusBadRequest, errors.New("operation id must be a UUID"))
			return
		}
		id = strings.ToLower(id)
		if kind == "message" && (object != nil || len(bytes.TrimSpace(raw)) == 0) {
			if object == nil {
				object = map[string]any{}
			}
			object["client_msg_id"] = id
			params = object
			raw, _ = json.Marshal(object)
		}
		if kind == "clarify" && object != nil && len(bytes.TrimSpace(raw)) != 0 {
			if clientMsgID, _ := object["client_msg_id"].(string); strings.TrimSpace(clientMsgID) == "" {
				// A retry after uncertain delivery must keep the manager's dedupe ID even though its receipt was forgotten.
				object["client_msg_id"] = "approval-question-" + id
				params = object
				raw, _ = json.Marshal(object)
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		target := devicewire.ClientOpTarget(r.URL.Path, r.URL.Query().Encode())
		if occurrence := r.Header.Get(assistantwire.InteractionCreatedAtHeader); occurrence != "" {
			// A mirror retry must keep the occurrence normalized on its first try,
			// even if the origin has since been replaced under the same ID.
			target = devicewire.ClientOpTargetWithOccurrence(r.URL.Path, r.URL.Query().Encode(), occurrence)
		}
		valid := len(bytes.TrimSpace(raw)) == 0 || json.Valid(raw)
		digest := devicewire.ClientOpDigest(kind, target, valid, opDigestParams(kind, raw, params))
		var timing *sendTiming
		if kind == "message" {
			timing = &sendTiming{hub: h, id: proto.SendTraceID(id), start: started}
			timing.mark("admission_started", true)
		}
		op, claimed, err := h.store.BeginClientOp(r.Context(), db.ClientOp{
			ID: id, DeviceID: principalOf(r.Context()).ID, Kind: kind, Target: target, Digest: digest,
		})
		timing.mark("admission_done", err == nil && op != nil)
		if err != nil || op == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("operation receipt unavailable"))
			return
		}
		if op.Digest != digest || (principalOf(r.Context()).ID != "" && op.DeviceID != principalOf(r.Context()).ID) {
			writeErrorBody(w, http.StatusConflict, map[string]any{"code": "op_id_reused"})
			return
		}
		if !claimed {
			if h.rerunClientOp(op) { // a meeting follow-up's call a restart cut off (meeting_receipts.go)
				h.executeClientOp(w, r, kind, id, next)
				return
			}
			h.waitClientOp(w, r, op)
			return
		}
		h.executeClientOp(w, r, kind, id, next)
	}
}

// opDigestParams are the params a receipt's digest covers: params, but for a card's answer (resolve) its body as the
// hub and the manager read it (proto.Resolution: field names in any case), each filled-in value replaced by its key,
// and one fixed body for any that is not one. The digest is stored, and the sha256 of a short password in it could be
// guessed offline.
func opDigestParams(kind string, raw []byte, params any) any {
	if kind != "resolve" {
		return params
	}
	var r proto.Resolution
	if json.Unmarshal(raw, &r) != nil {
		return "not a resolution"
	}
	for k := range r.Values {
		r.Values[k] = k
	}
	return r
}

func normalizedOpParams(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value, extra any
	if dec.Decode(&value) == nil && errors.Is(dec.Decode(&extra), io.EOF) {
		return value
	}
	// Invalid JSON still gets a stable digest, allowing a deterministic 400 to be replayed.
	return raw
}

type clientOpWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
	lang   i18n.Lang
}

func (w *clientOpWriter) Header() http.Header { return w.header }
func (w *clientOpWriter) Lang() i18n.Lang     { return w.lang }
func (w *clientOpWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *clientOpWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

// clientOpForgetter drops a receipt claimed for nothing (db.DB): the operation's retry runs it again.
type clientOpForgetter interface {
	ForgetClientOp(ctx context.Context, id string) error
}

// clientOpRetry: an operation of kind answered body keeps no receipt (ForgetClientOp), so the same request asked
// again runs again instead of the stored refusal coming back for good. Each kind below lets go an answer the manager
// never got (assistant_offline) — including "message", which fails in assistantSend / todoHandoff or when the devices
// manager is offline before anything reaches a node. A conversation's start, an approval question and an inbox hand-off also let go the
// manager's answer that their node got nothing (#13360: delivered "none" and retryable, apierr.Body's for ErrOffline,
// a start's 503 node_unreachable): nothing was started or sent, and what the manager did before the node's call makes
// nothing twice. An inbox reply or hand-off and an approval question cut off on the way to the manager
// (assistant_in_doubt) keep none either: the manager's send id has it sent once (extmgr inbox_reply.go sendInboxOnce,
// session.send_once). Everything else stays recorded: a refusal that is not retryable (inbox_item_done,
// conversation_busy), delivery that may have happened ("unknown", "yes"), an inbox reply's refusal by its source
// (replayed as it is), a choice (the manager claims the card before its node is called), and a meeting's or a to-do's
// answer, which their apps take as final for that op id (meeting_receipts.go, todo_receipts.go).
func clientOpRetry(kind string, body []byte) bool {
	var res struct {
		Code      string `json:"code"`
		Delivered string `json:"delivered"`
		Retryable bool   `json:"retryable"`
	}
	_ = json.Unmarshal(body, &res)
	offline, inDoubt := res.Code == assistantwire.CodeAssistantOffline, res.Code == assistantwire.CodeAssistantInDoubt
	gotNothing := res.Delivered == "none" && res.Retryable
	switch kind {
	case "choose", "message", meetingJustMeOp, meetingFollowupOp, todoAnswerOp, todoRouteOp, todoCreateOp:
		return offline
	case "assistant_start":
		return offline || gotNothing
	case "clarify", "inbox_handoff":
		return offline || inDoubt || gotNothing
	case "inbox_reply":
		return offline || inDoubt
	}
	return false
}

// clientOpStoredKey carries a *bool that executeClientOp sets once it has stored the operation's receipt (or let it
// go, for the same op id to run again): a wrapper outside withClientOp says a final state only then (todo_receipts.go).
type clientOpStoredKey struct{}

func (h *Hub) executeClientOp(w http.ResponseWriter, r *http.Request, kind, id string, next http.HandlerFunc) {
	state := "unknown"
	var result json.RawMessage
	finished := false
	persist := func() error {
		// A disconnected caller must not prevent the operation's outcome from being recorded.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.store.FinishClientOp(ctx, id, state, result)
	}
	defer func() {
		if !finished {
			_ = persist() // A panic may have happened after the side effect.
		}
	}()
	capture := &clientOpWriter{header: make(http.Header), lang: i18n.OfWriter(w)}
	next(capture, r)
	capture.WriteHeader(http.StatusOK)
	state = clientOpState(capture.status, capture.body.Bytes())
	if kind == todoAnswerOp || kind == todoRouteOp { // read as its final todo_send_state is (todo_receipts.go)
		state = todoReceiptState(capture.status, capture.body.Bytes())
	}
	saved := clientOpResult{Status: capture.status, Body: capture.body.Bytes(), ContentType: capture.header.Get("Content-Type")}
	result, _ = json.Marshal(saved)
	var err error
	if f, ok := h.store.(clientOpForgetter); ok && clientOpRetry(kind, saved.Body) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = f.ForgetClientOp(ctx, id)
		cancel()
		if err != nil { // kept, then: the stored answer comes back
			err = persist()
		}
	} else {
		err = persist()
	}
	finished = true
	if stored, ok := r.Context().Value(clientOpStoredKey{}).(*bool); ok {
		*stored = err == nil
	}
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable, map[string]any{"state": "unknown"})
		return
	}
	for key, values := range capture.header {
		w.Header()[key] = values
	}
	writeClientOpResultForKind(w, kind, saved)
}

func clientOpState(status int, body []byte) string {
	var reply struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(body, &reply)
	if reply.Delivered == "unknown" || reply.Delivered == "yes" && status >= 400 {
		return "unknown"
	}
	if status >= 200 && status < 300 {
		return "done"
	}
	if status == http.StatusBadRequest || status == http.StatusConflict {
		return "failed"
	}
	return "unknown"
}

func writeClientOpResult(w http.ResponseWriter, result clientOpResult) {
	writeClientOpResultForKind(w, "", result)
}

func writeClientOpResultForKind(w http.ResponseWriter, kind string, result clientOpResult) {
	if result.ContentType != "" {
		w.Header().Set("Content-Type", result.ContentType)
	}
	localized := i18n.InboxJSON(i18n.OfWriter(w), result.Body)
	if (kind == "modes" || kind == "message") && result.Status >= 400 {
		localized = apierr.LocalizeErrorJSON(i18n.OfWriter(w), localized)
	}
	if !bytes.Equal(localized, result.Body) {
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(result.Status)
	_, _ = w.Write(localized)
}

func (h *Hub) waitClientOp(w http.ResponseWriter, r *http.Request, op *db.ClientOp) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch op.State {
		case "done", "failed":
			var result clientOpResult
			if json.Unmarshal(op.Result, &result) != nil || result.Status < 200 || result.Status > 599 {
				writeErrorBody(w, http.StatusServiceUnavailable, map[string]any{"state": "unknown"})
				return
			}
			writeClientOpResultForKind(w, op.Kind, result)
			return
		case "unknown":
			// Start, checkout and reply retries must not repeat side effects or replace a recorded HTTP outcome.
			// A recovered pending operation has no result and remains explicitly unknown. A meeting's Just me or
			// follow-up call, and an answer or a message to a to-do, replay what they answered, a definite failure
			// too (meeting_receipts.go, todo_receipts.go).
			if op.Kind == "start" || op.Kind == "assistant_start" || op.Kind == "worktree_create" || op.Kind == "lark_send" || op.Kind == "inbox_reply" || op.Kind == "inbox_handoff" || op.Kind == "lark_thread_reply" ||
				op.Kind == meetingJustMeOp || op.Kind == meetingFollowupOp || op.Kind == todoAnswerOp || op.Kind == todoRouteOp || op.Kind == todoCreateOp {
				var result clientOpResult
				if json.Unmarshal(op.Result, &result) == nil && result.Status >= 200 && result.Status <= 599 {
					writeClientOpResultForKind(w, op.Kind, result)
					return
				}
			}
			writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "unknown"})
			return
		}
		select {
		case <-ctx.Done():
			writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "pending"})
			return
		case <-ticker.C:
			fresh, err := h.store.ClientOp(ctx, op.ID)
			if ctx.Err() != nil {
				writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "pending"})
				return
			}
			if err != nil || fresh == nil {
				writeErr(w, http.StatusServiceUnavailable, errors.New("operation receipt unavailable"))
				return
			}
			op = fresh
		}
	}
}

func (h *Hub) getClientOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !clientOpUUID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("operation id must be a UUID"))
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
		return
	}
	op, err := h.store.ClientOp(r.Context(), strings.ToLower(id))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	if op == nil || (principalOf(r.Context()).ID != "" && op.DeviceID != principalOf(r.Context()).ID) {
		writeErr(w, http.StatusNotFound, errors.New("no such operation"))
		return
	}
	writeJSON(w, op)
}

// clientOpsCleanupEvery bounds the periodic client_ops cleanup (var for tests).
var clientOpsCleanupEvery = 24 * time.Hour

type clientOpsWake struct {
	mu       sync.Mutex
	wake     chan struct{}
	deferred bool
}

func (c *clientOpsWake) channel() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wake == nil {
		c.wake = make(chan struct{}, 1)
	}
	return c.wake
}

func (h *Hub) cleanupClientOps(ctx context.Context) {
	if h.store == nil {
		return
	}
	if !h.quiesce.beginOrDefer(func() {
		h.clientOps.mu.Lock()
		h.clientOps.deferred = true
		h.clientOps.mu.Unlock()
	}) {
		return
	}
	defer h.quiesce.end()
	cleanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := h.store.CleanupClientOps(cleanCtx, time.Now().UTC().AddDate(0, 0, -30))
	cancel()
	if err != nil && ctx.Err() == nil {
		h.logf("clean client operations: %v", err)
	}
}

func (h *Hub) wakeClientOps() {
	h.clientOps.mu.Lock()
	deferred := h.clientOps.deferred
	h.clientOps.deferred = false
	ch := h.clientOps.wake
	h.clientOps.mu.Unlock()
	if !deferred || ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (h *Hub) startClientOps(ctx context.Context) error {
	if err := h.store.RecoverClientOps(ctx); err != nil {
		return err
	}
	wake := h.clientOps.channel()
	go func() {
		ticker := time.NewTicker(clientOpsCleanupEvery)
		defer ticker.Stop()
		for {
			h.cleanupClientOps(ctx)
			select {
			case <-ctx.Done():
				return
			case <-wake:
			case <-ticker.C:
			}
		}
	}()
	return nil
}
