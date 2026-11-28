package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A hub TTL expiry and a hub restart reach the assistant through the same honest
// wire result: the opaque executable handle is gone, so nothing was applied.
// Exercise the real bus adapter and the clarified-card terminal journal, not a
// direct call to the stale-result branch.
func TestPreparedHubLostHandleReportsOriginalConversationExactlyOnce(t *testing.T) {
	for _, scenario := range []string{"TTL expiry", "hub restart"} {
		t.Run(scenario, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			ft.f.mu.Lock()
			ft.f.known["s1"] = true // direct delivery to the original conversation
			ft.f.mu.Unlock()

			var handleValid, applyRequests, executions atomic.Int32
			handleValid.Store(1)
			discarded := make(chan json.RawMessage, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				conn := bus.NewConn(r.Context(), ws)
				peer := rpc.New("h", conn)
				peer.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
					switch method {
					case assistantwire.MethodHello:
						return map[string]any{}, nil
					case assistantwire.MethodFleetApprovalApply:
						applyRequests.Add(1)
						var in assistantwire.FleetApprovalApply
						if err := json.Unmarshal(raw, &in); err != nil {
							return nil, err
						}
						if !in.Resolution.UserAllowed() || in.Resolution.Remembered {
							return nil, &rpc.Error{Code: "invalid_owner_answer", Message: "a fresh owner Allow is required"}
						}
						if !handleValid.CompareAndSwap(1, 0) {
							return assistantwire.FleetApprovalApplyResult{Stale: true}, nil
						}
						executions.Add(1)
						return assistantwire.FleetApprovalApplyResult{Text: "applied"}, nil
					case assistantwire.MethodFleetApprovalDiscard:
						handleValid.Store(0)
						select {
						case discarded <- raw:
						default:
						}
						return map[string]any{"ok": true}, nil
					default:
						return map[string]any{}, nil
					}
				}
				_ = peer.Serve(r.Context(), conn)
			}))

			b := NewBusHub(BusOptions{Hub: srv.URL, Token: "synthetic", Logf: testLogf(t)})
			ctx, cancel := context.WithCancel(context.Background())
			served := make(chan error, 1)
			go func() { served <- b.Serve(ctx, ft.s) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-served:
				case <-time.After(5 * time.Second):
					t.Error("the synthetic bus did not stop")
				}
				srv.Close()
			})
			connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
			err := b.Wait(connectCtx)
			connectCancel()
			if err != nil {
				t.Fatalf("connect synthetic hub: %v", err)
			}

			id := "ix_hub_lost_" + strings.ReplaceAll(scenario, " ", "_")
			ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
				ToolName: "persona_settings", Summary: "Change the example setting", ExpiresAt: time.Now().Add(time.Hour)}
			done := make(chan preparedTestResult, 1)
			go func() {
				out, err := b.askPreparedHub(context.Background(), ix, "")
				done <- preparedTestResult{out: out, err: err}
			}()
			ft.waitPending(1)
			if err := ft.s.clarify(context.Background(), id, "Why?", "owner-"+id, nil); err != nil {
				t.Fatal(err)
			}
			awaitPreparedTest(t, done)

			// The hub-side unit tests drive the real TTL and restart boundaries. At
			// this integration boundary both mean that the retained closure is gone.
			handleValid.Store(0)
			allow := true
			if err := ft.s.resolve(id, proto.Resolution{Allow: &allow}); err != nil {
				t.Fatal(err)
			}
			result := waitApprovalResult(t, ft, id)
			for _, want := range []string{id, "Change the example setting", "stale and cancelled", "Nothing was applied", "fresh approval"} {
				if !strings.Contains(result, want) {
					t.Errorf("terminal result = %q, want %q", result, want)
				}
			}
			if strings.Contains(result, "uncertain") {
				t.Fatalf("a missing pre-execution handle was reported as uncertain: %q", result)
			}
			if applyRequests.Load() != 1 || executions.Load() != 0 {
				t.Fatalf("apply requests=%d executions=%d, want one refused request and no execution", applyRequests.Load(), executions.Load())
			}
			select {
			case raw := <-discarded:
				var discard assistantwire.FleetApprovalDiscard
				if err := json.Unmarshal(raw, &discard); err != nil || discard.InteractionID != id || discard.Status != "stale" {
					t.Fatalf("discard request = %s, %v", raw, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("discard request did not reach the hub")
			}

			// Repeating the owner answer and draining the durable journal cannot
			// manufacture a second result. waitApprovalResult fails on either zero
			// or two messages to the originating conversation.
			_ = ft.s.resolve(id, proto.Resolution{Allow: &allow}) // a late repeat is rejected
			ft.s.deliverApprovalResults()
			waitApprovalResult(t, ft, id)
			if applyRequests.Load() != 1 || executions.Load() != 0 {
				t.Fatalf("after duplicate answer: apply requests=%d executions=%d", applyRequests.Load(), executions.Load())
			}
		})
	}
}
