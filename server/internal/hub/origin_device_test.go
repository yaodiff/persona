package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestOriginDeviceIDFromPrincipal(t *testing.T) {
	cases := []struct {
		name string
		p    principal
		want string
	}{
		{"paired full phone", principal{ID: "dev_a1b2c3d4e5f6", registeredDevice: true, Role: roleFull}, "dev_a1b2c3d4e5f6"},
		{"paired answer-only watch", principal{ID: "dev_112233445566", registeredDevice: true, Role: roleAnswer}, "dev_112233445566"},
		{"master token empty id", principal{Role: roleFull}, ""},
		{"pairing code", principal{ID: "pair_deadbeef", Role: roleFull, PairHash: "hash"}, ""},
		{"extension token", principal{ID: "extension:sample", Role: roleExtension, Extension: "sample"}, ""},
		{"machine link", principal{ID: "dev_link1", Role: roleLink}, ""},
		{"peer token", principal{ID: "dev_peer1", Role: rolePeer}, ""},
		{"read-only session", principal{ID: "dev_ro1", Role: roleReadOnly}, ""},
		{"admin session", principal{ID: "admin", Role: "admin"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), principalKey{}, tc.p)
			if got := originDeviceID(ctx); got != tc.want {
				t.Fatalf("originDeviceID(%+v) = %q, want %q", tc.p, got, tc.want)
			}
		})
	}
}

func TestRelayHTTPDeviceIDHeaderAntiSpoofing(t *testing.T) {
	h := newTestHub(t)
	const phoneToken = "phone-origin-token-1"
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{
		hashToken(phoneToken): {ID: "dev_phone_real1", Name: "Test Phone", Kind: "android", Role: roleFull},
	}, rev: map[string]bool{}}

	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.HTTPRequest, 4)
	ap.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != assistantwire.MethodHTTP {
			return nil, errors.New("unexpected " + method)
		}
		var req assistantwire.HTTPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		got <- req
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}
	routes := h.routes()

	cases := []struct {
		name   string
		token  string
		forged string
		want   string
	}{
		{"paired device replaces forged header", phoneToken, "dev_spoofed", "dev_phone_real1"},
		{"master token strips forged header", h.cfg.Token, "dev_spoofed", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", strings.NewReader(`{"prompt":"hi"}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+tc.token)
			r.Header.Set(assistantwire.DeviceIDHeader, tc.forged)
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
			}
			select {
			case req := <-got:
				vals := req.Header[assistantwire.DeviceIDHeader]
				if tc.want == "" {
					if len(vals) != 0 {
						t.Fatalf("expected %s to be stripped, got %v", assistantwire.DeviceIDHeader, vals)
					}
				} else if len(vals) != 1 || vals[0] != tc.want {
					t.Fatalf("%s = %v, want [%s]", assistantwire.DeviceIDHeader, vals, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for forwarded request")
			}
		})
	}
}

func TestAssistantRemoteSendParamsOriginDeviceID(t *testing.T) {
	h := newTestHub(t)
	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.SendParams, 4)
	ap.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != assistantwire.MethodSendParams {
			return nil, errors.New("unexpected " + method)
		}
		var p assistantwire.SendParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		got <- p
		return map[string]any{"params": p.Params}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}

	// Paired device
	devCtx := context.WithValue(context.Background(), principalKey{}, principal{ID: "dev_phone_a", Role: roleFull, registeredDevice: true})
	if _, err := h.asst().SendParams(devCtx, "m1", "s1", map[string]any{"text": "open settings", "origin_device_id": "dev_spoofed"}); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.OriginDeviceID != "dev_phone_a" {
		t.Fatalf("paired device OriginDeviceID = %q, want dev_phone_a", p.OriginDeviceID)
	}

	// Extension token -> empty OriginDeviceID
	extCtx := context.WithValue(context.Background(), principalKey{}, principal{ID: "extension:bot", Role: roleExtension, Extension: "bot"})
	if _, err := h.asst().SendParams(extCtx, "m1", "s1", map[string]any{"text": "automated"}); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p.OriginDeviceID != "" {
		t.Fatalf("extension OriginDeviceID = %q, want empty", p.OriginDeviceID)
	}
}

func TestKeepUndeliveredOriginDeviceIDExcludesPairingCode(t *testing.T) {
	h := newTestHub(t)
	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.KeepUndeliveredParams, 4)
	ap.OnRequest = func(_ context.Context, method string, raw json.RawMessage) (any, error) {
		if method != assistantwire.MethodKeepUndelivered {
			return nil, errors.New("unexpected " + method)
		}
		var p assistantwire.KeepUndeliveredParams
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		got <- p
		return assistantwire.KeepUndeliveredResult{
			Undelivered: &proto.UndeliveredMessage{ID: "msg-1", State: proto.UndeliveredWaiting},
		}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}
	m := &Machine{cfg: MachineConfig{ID: "m1"}}

	// Paired device: both CredentialID and OriginDeviceID carry the paired device ID.
	devCtx := context.WithValue(context.Background(), principalKey{}, principal{ID: "dev_phone_a", Role: roleFull, registeredDevice: true})
	if _, _, ok := h.keepUndelivered(devCtx, m, "s1", map[string]any{"client_msg_id": "msg-1", "text": "hello"}, assistantwire.CodeConversationBusy, "busy"); !ok {
		t.Fatal("keepUndelivered paired device returned false")
	}
	if p := <-got; p.CredentialID != "dev_phone_a" || p.OriginDeviceID != "dev_phone_a" {
		t.Fatalf("paired device keepUndelivered = (cred=%q, origin=%q), want (dev_phone_a, dev_phone_a)", p.CredentialID, p.OriginDeviceID)
	}

	// Unredeemed pairing code: CredentialID stays set for revocation checks, while OriginDeviceID is strictly empty.
	pairCtx := context.WithValue(context.Background(), principalKey{}, principal{ID: "pair_deadbeef", Role: roleFull, PairHash: "pairhash"})
	if _, _, ok := h.keepUndelivered(pairCtx, m, "s1", map[string]any{"client_msg_id": "msg-2", "text": "hello"}, assistantwire.CodeConversationBusy, "busy"); !ok {
		t.Fatal("keepUndelivered pairing code returned false")
	}
	if p := <-got; p.CredentialID != "pair_deadbeef" || p.OriginDeviceID != "" {
		t.Fatalf("pairing code keepUndelivered = (cred=%q, origin=%q), want (pair_deadbeef, empty)", p.CredentialID, p.OriginDeviceID)
	}
}
