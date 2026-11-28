package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// sentStore keeps sent_messages like the table does.
type sentStore struct {
	*memStore
	mu   sync.Mutex
	rows map[string]string
}

func newSentStore() *sentStore { return &sentStore{memStore: newMemStore(), rows: map[string]string{}} }

func (s *sentStore) ClaimSend(_ context.Context, id, _, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.rows[id]; ok {
		return st, nil
	}
	s.rows[id] = sendSending
	return "", nil
}

func (s *sentStore) SettleSend(_ context.Context, id, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == "" {
		delete(s.rows, id)
	} else {
		s.rows[id] = state
	}
	return nil
}

// pipeNode connects machine id to an in-process node answering session.send with send; it returns the hub's side of
// the connection, which the test may close like a dropped ssh link.
func pipeNode(t *testing.T, h *Hub, id string, send func(ctx context.Context) error) *rpc.Peer {
	t.Helper()
	return pipeNodeRPC(t, h, id, func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "session.send" {
			return map[string]any{}, nil
		}
		return map[string]any{"ok": true}, send(ctx)
	})
}

func pipeNodeRPC(t *testing.T, h *Hub, id string, reply func(context.Context, string, json.RawMessage) (any, error)) *rpc.Peer {
	t.Helper()
	m := h.byID[id]
	if m == nil {
		m = newMachine(h, MachineConfig{ID: id, Name: id})
		h.byID[id] = m
		h.machines = append(h.machines, m)
	}
	hubIn, nodeOut := io.Pipe()
	nodeIn, hubOut := io.Pipe()
	hubPeer, nodePeer := rpc.New("h", hubOut), rpc.New("n", nodeOut)
	nodePeer.OnRequest = reply
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = hubPeer.Serve(ctx, hubIn) }()
	go func() { _ = nodePeer.Serve(ctx, nodeIn) }()
	m.mu.Lock()
	m.peer, m.online = hubPeer, true
	m.mu.Unlock()
	return hubPeer
}

const sendPath = "/api/v1/sessions/box/s1/messages"

// An exact approval reply ends at the manager: it has no node or model delivery receipt.
func TestOwnerApprovalReplyDoesNotReachNode(t *testing.T) {
	var typed atomic.Int32
	h, m, manager := undeliveredHub(t, func(context.Context) error {
		typed.Add(1)
		return nil
	})
	manager.approvalTarget = "ix_owner_batch_exact"
	w := sendAs(h, m, `{"text":"发","client_msg_id":"approval-1","approval_reply_to":"ix_owner_batch_exact"}`)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || out["ok"] != true || out["approval_reply"] != true || out["interaction_id"] != manager.approvalTarget || out["delivered"] != nil {
		t.Fatalf("approval response: %d %+v", w.Code, out)
	}
	if typed.Load() != 0 {
		t.Fatalf("exact approval reached the node %d time(s)", typed.Load())
	}
	if kept := manager.keeps(); len(kept) != 0 {
		t.Fatalf("exact approval was kept as an undelivered model message: %+v", kept)
	}
}

// A retry with the same client_msg_id — the app resends what got no answer — is not typed again, also when it comes
// while the first is still being typed.
func TestSendRetryIsNotTypedTwice(t *testing.T) {
	h := newTestHub(t)
	h.store = newSentStore()
	var typed atomic.Int32
	pipeNode(t, h, "box", func(context.Context) error {
		typed.Add(1)
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	mux := h.routes()
	body := `{"text":"你好","client_msg_id":"m1"}`
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body); code != 200 {
				t.Errorf("%d %v", code, out)
			}
		}()
	}
	wg.Wait()
	if code, _ := call(t, mux, "POST", sendPath, h.cfg.Token, body); code != 200 || typed.Load() != 1 {
		t.Fatalf("typed %d times (last %d)", typed.Load(), code)
	}
	call(t, mux, "POST", sendPath, h.cfg.Token, `{"text":"你好","client_msg_id":"m2"}`)
	if typed.Load() != 2 {
		t.Fatal("a new id is a new message")
	}
}

// The connection to the node broke while the message was being typed (2026-10-31: a reconnect during a send — the
// message arrived, the app was told "rpc: connection closed" and offered a resend). The answer says it may have
// arrived; the app's retry is not typed by the new node, nor after a hub restart.
func TestSendCutOffIsUnknownAndNotRetyped(t *testing.T) {
	store := newSentStore()
	h := newTestHub(t)
	h.store = store
	var typed atomic.Int32
	started := make(chan struct{})
	hubSide := pipeNode(t, h, "box", func(context.Context) error {
		typed.Add(1)
		close(started)
		time.Sleep(time.Second)
		return nil
	})
	go func() {
		<-started
		hubSide.Close(io.ErrUnexpectedEOF)
	}()
	mux := h.routes()
	body := `{"text":"部署一下","client_msg_id":"m1"}`
	code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body)
	if code != 502 || out["delivered"] != "unknown" {
		t.Fatalf("cut off: %d %v", code, out)
	}
	pipeNode(t, h, "box", func(context.Context) error { typed.Add(1); return nil }) // the node reconnected
	if code, out := call(t, mux, "POST", sendPath, h.cfg.Token, body); out["delivered"] != "unknown" || typed.Load() != 1 {
		t.Fatalf("retry after reconnect: %d %v, typed %d", code, out, typed.Load())
	}
	h2 := newTestHub(t) // the hub restarted
	h2.store = store
	pipeNode(t, h2, "box", func(context.Context) error { typed.Add(1); return nil })
	if code, out := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); out["delivered"] != "unknown" || typed.Load() != 1 {
		t.Fatalf("retry after hub restart: %d %v, typed %d", code, out, typed.Load())
	}
}

// Delivered before the hub restarted: the retry is acknowledged. Refused (nothing typed): the retry sends.
func TestSendRetryAfterRestartAndRefusal(t *testing.T) {
	store := newSentStore()
	h := newTestHub(t)
	h.store = store
	var typed atomic.Int32
	pipeNode(t, h, "box", func(context.Context) error { typed.Add(1); return nil })
	body := `{"text":"好","client_msg_id":"m1"}`
	call(t, h.routes(), "POST", sendPath, h.cfg.Token, body)
	h2 := newTestHub(t)
	h2.store = store
	pipeNode(t, h2, "box", func(context.Context) error { typed.Add(1); return nil })
	if code, _ := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 200 || typed.Load() != 1 {
		t.Fatalf("delivered then retried after a restart: %d, typed %d", code, typed.Load())
	}

	refuse := true
	pipeNode(t, h2, "box", func(context.Context) error {
		if refuse {
			refuse = false
			return &rpc.Error{Code: "input_draft", Message: "draft", Data: json.RawMessage(`{"delivered":"none","draft":"x"}`)}
		}
		typed.Add(1)
		return nil
	})
	body = `{"text":"再来","client_msg_id":"m2"}`
	if code, out := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 409 || out["code"] != "input_draft" {
		t.Fatalf("refusal: %d %v", code, out)
	}
	if code, _ := call(t, h2.routes(), "POST", sendPath, h2.cfg.Token, body); code != 200 || typed.Load() != 2 {
		t.Fatalf("retry of a refused send: %d, typed %d", code, typed.Load())
	}
}

// A node delivery refusal (dialog_open or not_delivered with delivered=none) maps to HTTP 409 and records the
// client_ops receipt as "failed" instead of 502 + "unknown".
func TestSendMessageDeliveryRefusalReturns409AndFailedClientOp(t *testing.T) {
	for _, tc := range []struct {
		name, opID, code, data, dialog string
		screenReads                    int32
	}{
		{name: "named dialog", opID: "00000000-0000-4000-8000-000000000502", code: "dialog_open", data: `{"delivered":"none","dialog":"Choose a workspace"}`, dialog: "Choose a workspace"},
		{name: "host dialog", opID: "00000000-0000-4000-8000-000000000503", code: "dialog_open", data: `{"delivered":"none"}`, dialog: "Select a model", screenReads: 1},
		{name: "other refusal", opID: "00000000-0000-4000-8000-000000000504", code: "not_delivered", data: `{"delivered":"none"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &clientOpsStore{sharedStore: newSentStore(), ops: map[string]db.ClientOp{}}
			h := newTestHub(t)
			h.store = store
			var reads atomic.Int32
			pipeNodeRPC(t, h, "box", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				switch method {
				case "session.send":
					return nil, &rpc.Error{Code: tc.code, Message: "refused", Data: json.RawMessage(tc.data)}
				case "session.screen":
					reads.Add(1)
					return map[string]string{"text": "Select a model\n❯ 1. Default\n  2. Other\nEnter to select · Esc to cancel\n"}, nil
				default:
					return map[string]any{}, nil
				}
			})
			h.machineByID("box").setSessions([]proto.Session{{SessionID: "s1", Agent: "claude", PaneID: "w1:p1", Status: proto.StatusIdle}})
			req := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(`{"text":"hello"}`))
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Persona-Op-Id", tc.opID)
			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, req)
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode response %d %q: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != http.StatusConflict || out["code"] != tc.code || out["delivered"] != "none" {
				t.Fatalf("response = %d %v, want 409 code=%q delivered=none", w.Code, out, tc.code)
			}
			if tc.dialog != "" && (out["dialog"] != tc.dialog || out["machine_id"] != "box" || out["session_id"] != "s1") {
				t.Fatalf("response = %v, want dialog %q on box/s1", out, tc.dialog)
			}
			if n := reads.Load(); n != tc.screenReads {
				t.Fatalf("read the terminal %d time(s), want %d", n, tc.screenReads)
			}
			op, err := store.ClientOp(context.Background(), tc.opID)
			if err != nil || op == nil || op.State != "failed" {
				t.Fatalf("client_op = %+v (err=%v), want state=failed", op, err)
			}
		})
	}
}

type sendReceiptStore struct {
	*sentStore
	ops *clientOpsStore
}

func (s *sendReceiptStore) BeginClientOp(ctx context.Context, op db.ClientOp) (*db.ClientOp, bool, error) {
	return s.ops.BeginClientOp(ctx, op)
}
func (s *sendReceiptStore) ClientOp(ctx context.Context, id string) (*db.ClientOp, error) {
	return s.ops.ClientOp(ctx, id)
}
func (s *sendReceiptStore) FinishClientOp(ctx context.Context, id, state string, result json.RawMessage) error {
	return s.ops.FinishClientOp(ctx, id, state, result)
}
func (s *sendReceiptStore) ForgetClientOp(_ context.Context, id string) error {
	s.ops.mu.Lock()
	defer s.ops.mu.Unlock()
	delete(s.ops.ops, id)
	return nil
}

// #15251: 503 assistant_offline from sendMessageOnce (assistantSend or todoHandoff) happens before ClaimSend / deliverOnce,
// so the response carries delivered:"none" and retryable:true, ForgetClientOp drops the receipt, and retrying with the
// same X-Persona-Op-Id succeeds once the assistant reconnects.
func TestSendMessageAssistantOfflineReleasesClientOpAndRetriesWithSameID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opID      string
		assistant bool
		body      string
	}{
		{
			name:      "assistant_send",
			opID:      "00000000-0000-4000-8000-000000000511",
			assistant: true,
			body:      `{"text":"hello assistant"}`,
		},
		{
			name:      "todo_handoff",
			opID:      "00000000-0000-4000-8000-000000000512",
			assistant: false,
			body:      `{"text":"work on todo","todo_id":"todo-1"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &sendReceiptStore{sentStore: newSentStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
			h := newTestHub(t)
			h.store = store
			var typed atomic.Int32
			pipeNode(t, h, "box", func(context.Context) error {
				typed.Add(1)
				return nil
			})
			m := h.byID["box"]
			editSessions(m, func([]proto.Session) []proto.Session {
				return []proto.Session{{SessionID: "s1", Assistant: tc.assistant, Status: proto.StatusIdle}}
			})

			post := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, sendPath, strings.NewReader(tc.body))
				req.RemoteAddr = "127.0.0.1:12345"
				req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Persona-Op-Id", tc.opID)
				w := httptest.NewRecorder()
				h.routes().ServeHTTP(w, req)
				return w
			}

			// 1. While the assistant is offline, return 503 with delivered:"none", retryable:true and release client_ops.
			w := post()
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode offline response %d %q: %v", w.Code, w.Body.String(), err)
			}
			if w.Code != http.StatusServiceUnavailable || out["code"] != assistantwire.CodeAssistantOffline ||
				out["delivered"] != "none" || out["retryable"] != true {
				t.Fatalf("offline response = %d %v, want 503 assistant_offline delivered=none retryable=true", w.Code, out)
			}
			if typed.Load() != 0 {
				t.Fatalf("node was called while assistant was offline: %d", typed.Load())
			}
			if op, err := store.ClientOp(context.Background(), tc.opID); err != nil || op != nil {
				t.Fatalf("client_op after assistant_offline = %+v (err=%v), want forgotten (nil)", op, err)
			}

			// 2. Reconnect the assistant manager and retry with the exact same X-Persona-Op-Id.
			h.bus.idents = &memIdents{}
			bmux := http.NewServeMux()
			h.busRoutes(bmux)
			srv := httptest.NewServer(bmux)
			t.Cleanup(srv.Close)
			tok, err := h.createBusToken(context.Background(), assistantwire.AssistantID, "Test manager")
			if err != nil {
				t.Fatal(err)
			}
			connectGuardManagerWith(t, srv.URL, assistantwire.AssistantID, tok, func(method string, raw json.RawMessage) (any, error) {
				switch method {
				case assistantwire.MethodSendParams:
					var p assistantwire.SendParams
					_ = json.Unmarshal(raw, &p)
					return map[string]any{"params": p.Params}, nil
				case assistantwire.MethodTodoHandoff:
					var p assistantwire.TodoHandoff
					_ = json.Unmarshal(raw, &p)
					return assistantwire.TodoHandoffResult{Found: true, Text: p.Text}, nil
				default:
					return map[string]any{"ok": true}, nil
				}
			})

			w = post()
			if w.Code != http.StatusOK {
				t.Fatalf("retry after reconnect = %d %s, want 200", w.Code, w.Body.String())
			}
			if typed.Load() != 1 {
				t.Fatalf("node typed = %d, want 1", typed.Load())
			}
		})
	}
}

func TestSendToAssistantReturnsDialogTerminal(t *testing.T) {
	h, m, manager := undeliveredHub(t, func(context.Context) error {
		return &rpc.Error{Code: "dialog_open", Message: "refused", Data: json.RawMessage(`{"delivered":"none","dialog":"Choose a workspace"}`)}
	})
	w := sendAs(h, m, `{"text":"Carry on","client_msg_id":"assistant-dialog"}`)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || out["code"] != "dialog_open" || out["delivered"] != "none" || out["dialog"] != "Choose a workspace" || out["machine_id"] != "box" || out["session_id"] != "s1" {
		t.Fatalf("response = %d %v, want 409 dialog_open with the dialog on box/s1", w.Code, out)
	}
	if len(manager.keeps()) != 0 {
		t.Fatal("a definite refusal must not be queued again")
	}
}
