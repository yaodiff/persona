package proc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/proto"
)

// serveRawAutoConfirm is a protocol-only connector subprocess implementing confirm_always + guarded conditional
// auto-confirm. It validates single-use tokens bound to exact (action, raw args JSON) bytes, and supports modes via
// PERSONA_PROC_AC_MODE:
//   - "guarded": returns a single-use token only when approval_protocol == guarded-auto-confirm-v1 and amount <= 100
//   - "legacy": ignores approval_protocol and never returns auto_confirm
//   - "unnegotiated": always returns auto_confirm even when approval_protocol is absent
//   - "reject-token": issues a token in describe, then rejects it in actions.do
func serveRawAutoConfirm() {
	mode := os.Getenv("PERSONA_PROC_AC_MODE")
	if mode == "" {
		mode = "guarded"
	}
	var (
		mu      sync.Mutex
		nextSeq int
		tokens  = map[string]struct {
			action  string
			rawArgs []byte
		}{}
		doCounts = map[string]int{}
	)
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{
		ID:      "example-guarded-ext",
		Version: "1.2.0",
		Namespaces: map[string]pserve.Handler{
			"connector": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				switch method {
				case "status":
					return map[string]string{"state": "ok"}, nil
				case "actions.list":
					return serve.ToolsResult{Tools: []proto.Tool{{
						Name:        "example_submit",
						Description: "Submit an example write",
						InputSchema: json.RawMessage(`{"type":"object"}`),
					}}}, nil
				case "actions.describe":
					var p struct {
						Name             string          `json:"name"`
						Args             json.RawMessage `json:"args"`
						ApprovalProtocol string          `json:"approval_protocol"`
					}
					if err := json.Unmarshal(params, &p); err != nil {
						return nil, err
					}
					var a struct {
						Amount float64 `json:"amount"`
						Label  string  `json:"label"`
					}
					_ = json.Unmarshal(p.Args, &a)
					res := map[string]any{
						"action":  map[string]string{"name": p.Name, "visible": "example account"},
						"summary": fmt.Sprintf("Submit %.0f (%s)", a.Amount, a.Label),
					}
					issue := func() {
						mu.Lock()
						nextSeq++
						tok := fmt.Sprintf("tok_proc_%d", nextSeq)
						tokens[tok] = struct {
							action  string
							rawArgs []byte
						}{action: p.Name, rawArgs: append([]byte(nil), p.Args...)}
						mu.Unlock()
						res["auto_confirm"] = map[string]any{
							"token":      tok,
							"expires_at": time.Now().UTC().Add(45 * time.Second).Format(time.RFC3339),
						}
					}
					switch mode {
					case "guarded", "reject-token":
						if p.ApprovalProtocol == connector.ApprovalProtocolGuardedAutoConfirmV1 && a.Amount > 0 && a.Amount <= 100 {
							issue()
						}
					case "unnegotiated":
						issue()
					case "legacy":
						// Legacy extension ignores approval_protocol and omits auto_confirm.
					}
					return res, nil
				case "actions.do":
					var p struct {
						Name          string          `json:"name"`
						Args          json.RawMessage `json:"args"`
						RequestID     string          `json:"request_id"`
						ApprovalMode  string          `json:"approval_mode"`
						ApprovalToken string          `json:"approval_token"`
					}
					if err := json.Unmarshal(params, &p); err != nil {
						return nil, err
					}
					if p.RequestID == "" {
						return nil, errors.New("missing request_id")
					}
					if (p.ApprovalMode == "") != (p.ApprovalToken == "") {
						return nil, errors.New("unpaired approval fields")
					}
					mu.Lock()
					doCounts[p.Name]++
					count := doCounts[p.Name]
					mu.Unlock()
					if p.ApprovalMode != "" {
						if p.ApprovalMode != connector.ApprovalModeAutoConfirm {
							return nil, fmt.Errorf("unsupported approval_mode %q", p.ApprovalMode)
						}
						if mode == "reject-token" {
							return nil, errors.New("token rejected by extension guard")
						}
						mu.Lock()
						bound, ok := tokens[p.ApprovalToken]
						if ok {
							delete(tokens, p.ApprovalToken) // single-use
						}
						mu.Unlock()
						if !ok {
							return nil, errors.New("unknown or already used approval_token")
						}
						if bound.action != p.Name || !bytes.Equal(bound.rawArgs, p.Args) {
							return nil, fmt.Errorf("args mismatch between describe (%s) and do (%s)", bound.rawArgs, p.Args)
						}
						return serve.CallResult{
							Text:       fmt.Sprintf("auto:%d:%s", count, string(p.Args)),
							MessageRef: fmt.Sprintf("ref-auto-%d", count),
						}, nil
					}
					return serve.CallResult{
						Text:       fmt.Sprintf("manual:%d:%s", count, string(p.Args)),
						MessageRef: fmt.Sprintf("ref-manual-%d", count),
					}, nil
				}
				return nil, pserve.UnknownMethod(method)
			},
		},
	})
}

func startRawAutoConfirmProc(t *testing.T, mode string, ident *connector.ExtensionIdentity) *Conn {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	hp := host.Start(context.Background(), host.Spec{
		ID:      "example-guarded-ext",
		Name:    "Example",
		Command: exe,
		RPC:     true,
		Startup: true,
		Env:     []string{"PERSONA_PROC_CHILD=1", "PERSONA_PROC_KIND=raw-auto-confirm", "PERSONA_PROC_AC_MODE=" + mode},
		Logf:    t.Logf,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{
				Extension: extension.ExtensionInfo{ID: "example-guarded-ext", Version: "1.2.0"},
				Active:    map[string][]string{"connectors": {"example"}},
			}, nil
		},
		Handle: func(context.Context, string, json.RawMessage) (any, error) {
			return nil, errors.New("not asked")
		},
	})
	conn := New(Config{
		ID:   "example",
		Name: "Example",
		Manifest: connector.Manifest{
			Capabilities:  []connector.Capability{connector.CapStatus, connector.CapActions},
			ConfirmAlways: []string{"example_submit"},
		},
		Link:     &Link{Proc: hp, ID: "example"},
		Identity: ident,
		Logf:     t.Logf,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		hp.Stop()
	})
	go conn.Run(ctx)
	waitUntil(t, "guarded auto-confirm connector ready", func() bool {
		return hp.Ready() && conn.Status(ctx).State == connector.StateOK && len(conn.Actions()) > 0
	})
	return conn
}

func TestProcGuardedAutoConfirmEndToEnd(t *testing.T) {
	ident := &connector.ExtensionIdentity{
		Extension: "example-guarded-ext",
		Version:   "1.2.0",
		Source:    "installed",
	}
	policy := connector.AutoConfirmPolicy{
		Name:          "small-tier",
		Extension:     "example-guarded-ext",
		Connector:     "example",
		Actions:       []string{"example_submit"},
		MaxTTLSeconds: 120,
	}

	t.Run("guarded small tier auto-confirms with byte-identical args; large tier falls back to card", func(t *testing.T) {
		conn := startRawAutoConfirmProc(t, "guarded", ident)
		reg := connector.NewRegistry(nil)
		if err := reg.Add(conn); err != nil {
			t.Fatal(err)
		}
		if err := reg.SetAutoConfirmPolicies([]connector.AutoConfirmPolicy{policy}); err != nil {
			t.Fatal(err)
		}
		var sent []connector.Sent
		reg.SetSent(func(_ context.Context, s connector.Sent) { sent = append(sent, s) })
		asked := 0
		ask := func(context.Context, string, json.RawMessage) bool { asked++; return true }

		// Small tier: non-alphabetical JSON keys must arrive byte-identical in describe and do.
		smallArgs := json.RawMessage(`{"label":"tier1","amount":25}`)
		res := reg.Call(connector.WithOwnerCaller(context.Background()), "example_submit", smallArgs, ask)
		if res.IsError {
			t.Fatalf("small tier failed: %+v", res)
		}
		if asked != 0 {
			t.Fatalf("expected 0 cards for small tier, got %d", asked)
		}
		if !strings.HasPrefix(res.Content[0].Text, "auto:1:") {
			t.Fatalf("expected auto-confirmed execution, got %q", res.Content[0].Text)
		}
		if len(sent) != 1 || sent[0].Through.Kind != connector.ThroughAutoConfirm || sent[0].MessageRef != "ref-auto-1" {
			t.Fatalf("unexpected sent record: %+v", sent)
		}

		// Large tier: extension omits auto_confirm -> Hub falls back to phone card, Do runs without approval fields.
		largeArgs := json.RawMessage(`{"label":"tier2","amount":500}`)
		res = reg.Call(connector.WithOwnerCaller(context.Background()), "example_submit", largeArgs, ask)
		if res.IsError {
			t.Fatalf("large tier failed: %+v", res)
		}
		if asked != 1 {
			t.Fatalf("expected 1 card for large tier, got %d", asked)
		}
		if !strings.HasPrefix(res.Content[0].Text, "manual:2:") {
			t.Fatalf("expected manual execution after card, got %q", res.Content[0].Text)
		}
		if len(sent) != 2 || sent[1].Through.Kind != connector.ThroughCard || sent[1].MessageRef != "ref-manual-2" {
			t.Fatalf("unexpected large tier sent record: %+v", sent)
		}
	})

	t.Run("legacy subprocess remains compatible on phone card path", func(t *testing.T) {
		conn := startRawAutoConfirmProc(t, "legacy", ident)
		reg := connector.NewRegistry(nil)
		if err := reg.Add(conn); err != nil {
			t.Fatal(err)
		}
		if err := reg.SetAutoConfirmPolicies([]connector.AutoConfirmPolicy{policy}); err != nil {
			t.Fatal(err)
		}
		asked := 0
		res := reg.Call(connector.WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{"label":"small","amount":10}`),
			func(context.Context, string, json.RawMessage) bool { asked++; return true })
		if res.IsError || asked != 1 || !strings.HasPrefix(res.Content[0].Text, "manual:1:") {
			t.Fatalf("legacy subprocess: res=%+v asked=%d", res, asked)
		}
	})

	t.Run("unnegotiated auto_confirm grant is ignored when hub has no matching policy", func(t *testing.T) {
		conn := startRawAutoConfirmProc(t, "unnegotiated", ident)
		reg := connector.NewRegistry(nil) // NO auto-confirm policies
		if err := reg.Add(conn); err != nil {
			t.Fatal(err)
		}
		asked := 0
		res := reg.Call(connector.WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{"label":"sneaky","amount":10}`),
			func(context.Context, string, json.RawMessage) bool { asked++; return true })
		if res.IsError || asked != 1 || !strings.HasPrefix(res.Content[0].Text, "manual:1:") {
			t.Fatalf("unnegotiated grant must fall back to card: res=%+v asked=%d", res, asked)
		}
	})

	t.Run("rejected token in actions.do fails without silent retry", func(t *testing.T) {
		conn := startRawAutoConfirmProc(t, "reject-token", ident)
		reg := connector.NewRegistry(nil)
		if err := reg.Add(conn); err != nil {
			t.Fatal(err)
		}
		if err := reg.SetAutoConfirmPolicies([]connector.AutoConfirmPolicy{policy}); err != nil {
			t.Fatal(err)
		}
		var sent []connector.Sent
		reg.SetSent(func(_ context.Context, s connector.Sent) { sent = append(sent, s) })
		asked := 0
		res := reg.Call(connector.WithOwnerCaller(context.Background()), "example_submit", json.RawMessage(`{"label":"small","amount":10}`),
			func(context.Context, string, json.RawMessage) bool { asked++; return true })
		if !res.IsError || !strings.Contains(res.Content[0].Text, "token rejected") || asked != 0 || len(sent) != 0 {
			t.Fatalf("rejected token must fail cleanly without retry: res=%+v asked=%d sent=%d", res, asked, len(sent))
		}
	})
}
