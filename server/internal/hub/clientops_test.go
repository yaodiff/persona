package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

type clientOpsStore struct {
	sharedStore
	mu   sync.Mutex
	ops  map[string]db.ClientOp
	read chan struct{}
}

func (s *clientOpsStore) BeginClientOp(_ context.Context, op db.ClientOp) (*db.ClientOp, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.ops[op.ID]; ok {
		return &old, false, nil
	}
	op.State = "pending"
	s.ops[op.ID] = op
	return &op, true, nil
}

func (s *clientOpsStore) ClientOp(_ context.Context, id string) (*db.ClientOp, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.read != nil {
		select {
		case s.read <- struct{}{}:
		default:
		}
	}
	op, ok := s.ops[id]
	if !ok {
		return nil, nil
	}
	return &op, nil
}

func (s *clientOpsStore) FinishClientOp(_ context.Context, id, state string, result json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op := s.ops[id]
	op.State, op.Result = state, result
	s.ops[id] = op
	return nil
}

func TestClientOps(t *testing.T) {
	const id = "00000000-0000-4000-8000-000000000001"
	const body = "{ \"ok\": true }\n"
	store := &clientOpsStore{ops: map[string]db.ClientOp{}, read: make(chan struct{}, 1)}
	h := &Hub{store: store}
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	handler := h.withClientOp("message", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var params map[string]any
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil || params["client_msg_id"] != id {
			t.Error("message id was not forwarded")
		}
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(body))
	})
	request := func(ctx context.Context, params string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/sessions/m/s/messages", strings.NewReader(params)).WithContext(ctx)
		r.Header.Set("X-Persona-Op-Id", id)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- request(ctx, `{"text":"hello","options":{"b":2,"a":1}}`) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("handler did not start")
	}
	duplicate := make(chan *httptest.ResponseRecorder, 1)
	go func() { duplicate <- request(ctx, ` { "options": {"a":1,"b":2}, "text":"hello" } `) }()
	select {
	case <-store.read:
	case <-ctx.Done():
		t.Fatal("duplicate did not wait on the persisted operation")
	}
	select {
	case <-duplicate:
		t.Fatal("pending operation returned before completion")
	default:
	}
	close(release)
	for _, ch := range []chan *httptest.ResponseRecorder{first, duplicate} {
		select {
		case w := <-ch:
			if w.Code != http.StatusCreated || w.Body.String() != body {
				t.Fatalf("replay = %d %q", w.Code, w.Body.String())
			}
		case <-ctx.Done():
			t.Fatal("operation did not complete")
		}
	}
	// A new hub shares the receipt, without sharing any in-memory execution state.
	h = &Hub{store: store}
	replayed := h.withClientOp("message", func(http.ResponseWriter, *http.Request) { calls.Add(1) })
	r := httptest.NewRequest("POST", "/api/v1/sessions/m/s/messages", strings.NewReader(`{"text":"hello","options":{"a":1,"b":2},"client_msg_id":"`+id+`"}`))
	r.Header.Set("X-Persona-Op-Id", id) // receipts are only for clients that send the header
	w := httptest.NewRecorder()
	replayed(w, r)
	if w.Code != http.StatusCreated || w.Body.String() != body || calls.Load() != 1 {
		t.Fatalf("persisted replay = %d %q, executions = %d", w.Code, w.Body.String(), calls.Load())
	}
	w = request(ctx, `{"text":"changed"}`)
	if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != `{"code":"op_id_reused"}` || calls.Load() != 1 {
		t.Fatalf("different digest = %d %s, executions = %d", w.Code, w.Body.String(), calls.Load())
	}
	for _, tc := range []struct {
		status int
		body   string
		state  string
	}{
		{400, `{"error":"invalid"}`, "failed"},
		{409, `{"code":"input_draft","delivered":"none"}`, "failed"},
		{502, `{"delivered":"unknown"}`, "unknown"},
		{409, `{"delivered":"unknown"}`, "unknown"},
		{503, `{}`, "unknown"},
	} {
		if got := clientOpState(tc.status, []byte(tc.body)); got != tc.state {
			t.Errorf("state(%d, %s) = %s", tc.status, tc.body, got)
		}
	}
}

// The cleanup loop recovers pending operations first and keeps exactly 30 UTC days.
type clientOpsCleanupStore struct {
	sharedStore
	recovered bool
	cuts      chan time.Time
}

func (s *clientOpsCleanupStore) RecoverClientOps(context.Context) error {
	s.recovered = true
	return nil
}

func (s *clientOpsCleanupStore) CleanupClientOps(_ context.Context, before time.Time) error {
	s.cuts <- before
	return nil
}

func TestClientOpsCleanupRetention(t *testing.T) {
	store := &clientOpsCleanupStore{cuts: make(chan time.Time, 1)}
	h := &Hub{store: store}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := time.Now().Add(-30 * 24 * time.Hour)
	if err := h.startClientOps(ctx); err != nil {
		t.Fatal(err)
	}
	if !store.recovered {
		t.Fatal("pending operations were not recovered")
	}
	select {
	case cut := <-store.cuts:
		after := time.Now().Add(-30 * 24 * time.Hour)
		if cut.Location() != time.UTC {
			t.Errorf("cleanup cut location = %v, want UTC", cut.Location())
		}
		if cut.Before(before) || cut.After(after) {
			t.Errorf("cleanup cut = %v, want between %v and %v", cut, before, after)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client operations were not cleaned up at startup")
	}
}

// #13360: which answers let an operation's receipt go (the same op id runs again) and which are kept. Besides
// assistant_offline (no manager had it), a conversation's start, an approval question and an inbox hand-off let go
// the manager's answer that their node got nothing (apierr.Body of ErrOffline: delivered "none", retryable). A refusal
// that is not retryable, and anything that may have been delivered, stays; so does every answer of the kinds whose
// clients take a manager's answer as final for that id.
func TestClientOpRetryNodeGotNothing(t *testing.T) {
	const (
		offline      = `{"code":"assistant_offline","error":"the assistant is offline"}`
		inDoubt      = `{"code":"assistant_in_doubt","delivered":"unknown","retryable":false}`
		gotNothing   = `{"code":"node_unreachable","delivered":"none","retryable":true,"error":"machine is offline"}`
		askNothing   = `{"code":"bad_request","delivered":"none","retryable":true,"error":"machine is offline"}` // a question's ErrOffline (400)
		itemDone     = `{"code":"inbox_item_done","delivered":"none","retryable":false}`
		busy         = `{"code":"conversation_busy","delivered":"none","retryable":false}`
		sourceFailed = `{"code":"inbox_send_failed","delivered":"none","retryable":true}`
		mayHave      = `{"code":"node_unreachable","delivered":"unknown","retryable":true}`
		typed        = `{"code":"send_in_doubt","delivered":"yes","retryable":true}`
		noDelivered  = `{"code":"search_changed","retryable":true}`
		noRetryable  = `{"code":"forbidden","delivered":"none"}`
		retryText    = `{"code":"node_unreachable","delivered":"none","retryable":"true"}`
		started      = `{"session":{"machine_id":"m1","session_id":"s1"}}`
		notJSON      = `node unreachable`
	)
	bodies := []string{offline, inDoubt, gotNothing, askNothing, itemDone, busy, sourceFailed, mayHave, typed, noDelivered,
		noRetryable, retryText, started, notJSON, ``}
	released := map[string][]string{
		"assistant_start":   {offline, gotNothing, askNothing, sourceFailed},
		"clarify":           {offline, inDoubt, gotNothing, askNothing, sourceFailed},
		"inbox_handoff":     {offline, inDoubt, gotNothing, askNothing, sourceFailed},
		"inbox_reply":       {offline, inDoubt}, // a source's refusal is replayed as it is (protocol.md "Lark API")
		"choose":            {offline},
		meetingJustMeOp:     {offline},
		meetingFollowupOp:   {offline},
		todoAnswerOp:        {offline},
		todoRouteOp:         {offline},
		todoCreateOp:        {offline},
		"resolve":           nil,
		"message":           {offline},
		"start":             nil,
		"worktree_create":   nil,
		"lark_send":         nil,
		"lark_thread_reply": nil,
	}
	for kind, want := range released {
		for _, body := range bodies {
			if got, want := clientOpRetry(kind, []byte(body)), slices.Contains(want, body); got != want {
				t.Errorf("%s: clientOpRetry(%s) = %v, want %v", kind, body, got, want)
			}
		}
	}
}
