package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestRuntimeHelloAdvertisesAssistantCommandGate(t *testing.T) {
	t.Run("local assistant_environment runtime gets assistant_command_gate", func(t *testing.T) {
		n, _ := runtimeNode(t)
		hello := assistantRuntimeHello()
		hello["caps"].(map[string]any)["assistant_environment"] = true
		host := dialRuntime(t, n, hello)
		features, _ := host.helloR["features"].([]any)
		if !slices.Contains(features, any("assistant_command_gate")) {
			t.Fatalf("assistant_command_gate not advertised: %v", features)
		}
	})

	t.Run("local assistant runtime without assistant_environment omits assistant_command_gate", func(t *testing.T) {
		n, _ := runtimeNode(t)
		host := dialRuntime(t, n, assistantRuntimeHello())
		features, _ := host.helloR["features"].([]any)
		if slices.Contains(features, any("assistant_command_gate")) {
			t.Fatalf("runtime without assistant_environment unexpectedly got assistant_command_gate: %v", features)
		}
	})

	t.Run("remote_agent assistant runtime omits assistant_command_gate", func(t *testing.T) {
		n, _ := runtimeNode(t)
		hello := assistantRuntimeHello()
		hello["caps"].(map[string]any)["assistant_environment"] = true
		hello["caps"].(map[string]any)["remote_agent"] = true
		host := dialRuntime(t, n, hello)
		features, _ := host.helloR["features"].([]any)
		if slices.Contains(features, any("assistant_command_gate")) {
			t.Fatalf("remote_agent runtime unexpectedly got assistant_command_gate: %v", features)
		}
	})

	t.Run("local non-assistant runtime omits assistant_command_gate", func(t *testing.T) {
		n, _ := runtimeNode(t)
		host := dialRuntime(t, n, dshHello())
		features, _ := host.helloR["features"].([]any)
		if slices.Contains(features, any("assistant_command_gate")) {
			t.Fatalf("non-assistant runtime unexpectedly got assistant_command_gate: %v", features)
		}
	})

	t.Run("daemon assistant runtime omits assistant_command_gate", func(t *testing.T) {
		n, _ := runtimeNode(t)
		clientConn, serverConn := net.Pipe()
		t.Cleanup(func() {
			_ = clientConn.Close()
			_ = serverConn.Close()
		})
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		go n.runtimeHost.serveConn(ctx, serverConn, "remote-daemon")

		peer := rpc.New("t", clientConn)
		go func() { _ = peer.Serve(ctx, clientConn) }()
		t.Cleanup(func() { peer.Close(nil) })

		daemonHello := assistantRuntimeHello()
		daemonHello["runtime"] = "remote-daemon"
		daemonHello["caps"].(map[string]any)["assistant_environment"] = true
		var helloR map[string]any
		callCtx, callCancel := context.WithTimeout(ctx, 5*time.Second)
		defer callCancel()
		if err := peer.Call(callCtx, "runtime.hello", daemonHello, &helloR); err != nil {
			t.Fatal(err)
		}
		features, _ := helloR["features"].([]any)
		if slices.Contains(features, any("assistant_command_gate")) {
			t.Fatalf("daemon runtime unexpectedly got assistant_command_gate: %v", features)
		}
	})
}

func TestRuntimeAssistantBashRoutesThroughLarkGateAndGrantsProxy(t *testing.T) {
	oldCtx := larkContextFromCLI
	larkContextFromCLI = func(context.Context, *Node, proto.AssistantLark, larkCall) larkCardContext {
		return larkCardContext{}
	}
	t.Cleanup(func() { larkContextFromCLI = oldCtx })

	api := newFakeAPI(t)
	n, hub := runtimeNode(t)
	t.Cleanup(func() { n.gates().close() })
	assistantHub(hub)
	cwd := prepareRuntimeAssistantEnvironment(t, n, true)
	n.mu.Lock()
	n.larkCfg.Gates[0].Env["EXAMPLE_BASE"] = api.URL
	n.larkCfg.Gates[0].Rules = []proto.LarkRule{
		{Method: "GET", Path: "/**", Action: "allow"},
		{Method: "DELETE", Path: "/forbidden", Action: "deny"},
	}
	n.mu.Unlock()

	hello := assistantRuntimeHello()
	hello["caps"].(map[string]any)["assistant_environment"] = true
	host := dialRuntime(t, n, hello)
	sid := startedAssistant(t, n, host)
	startEnv := runtimeAssistantEnvironment(t, host.got("session.start")[0])
	base := startEnv["EXAMPLE_BASE"]
	if !gateProxyURLRe.MatchString(base) {
		t.Fatalf("EXAMPLE_BASE = %q, want local gate proxy address", base)
	}

	// Register a regular (non-assistant) session on the same runtime to verify isolation.
	regularDir := t.TempDir()
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
		{SessionID: "session-regular", Title: "Regular Agent", Cwd: regularDir, Status: proto.StatusIdle},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "both sessions listed", func() bool {
		_, _, a := n.runtimes.rowOf(sid)
		_, _, b := n.runtimes.rowOf("session-regular")
		return a && b
	})

	resetAsked := func() {
		hub.mu.Lock()
		hub.asked = nil
		hub.mu.Unlock()
	}
	askedSnapshot := func() []proto.Interaction {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return append([]proto.Interaction(nil), hub.asked...)
	}
	askBash := func(session, toolName, toolUseID, cmd, cmdCwd string) (proto.Resolution, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		ix := map[string]any{
			"kind":        "permission",
			"session_id":  session,
			"tool_name":   toolName,
			"tool_use_id": toolUseID,
			"tool_input":  map[string]any{"command": cmd},
			"summary":     cmd,
		}
		if cmdCwd != "" {
			ix["cwd"] = cmdCwd
		}
		var res proto.Resolution
		err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": ix}, &res)
		return res, err
	}

	t.Run("classified read auto-allows without raising a card", func(t *testing.T) {
		resetAsked()
		res, err := askBash(sid, "Bash", "call-read", "ex GET /me", "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("read resolution = %+v, want allow=true", res)
		}
		if asked := askedSnapshot(); len(asked) != 0 {
			t.Fatalf("classified read raised %d card(s): %+v", len(asked), asked)
		}
	})

	t.Run("proxied write confirms on phone and issues one-use proxy grant", func(t *testing.T) {
		n.noteModes(sid, proto.ModeDefault, "")
		resetAsked()
		if code, _ := send(t, "POST", base+"/api/v1/conversations/c1/messages", `{"text":"hello"}`); code != http.StatusForbidden {
			t.Fatalf("unapproved write = %d, want 403", code)
		}
		allow := true
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: &allow}
		}
		hub.mu.Unlock()

		res, err := askBash(sid, "Bash", "call-write", `ex POST /conversations/c1/messages '{"text":"hello"}'`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow || res.Always {
			t.Fatalf("write resolution = %+v, want allow=true always=false", res)
		}
		asked := askedSnapshot()
		if len(asked) != 1 {
			t.Fatalf("expected 1 confirmation card, got %d", len(asked))
		}
		card := asked[0]
		if card.ToolName != "示例" {
			t.Fatalf("card.ToolName = %q, want gate title %q", card.ToolName, "示例")
		}
		if card.Agent != "" || card.AgentLabel != "" {
			t.Fatalf("gate card kept runtime agent metadata: agent=%q label=%q", card.Agent, card.AgentLabel)
		}
		if !card.OutwardWrite {
			t.Fatalf("expected OutwardWrite=true on send card: %+v", card)
		}
		if !card.Resumable {
			t.Fatalf("expected Resumable=true on runtime gate card")
		}
		if card.Cwd != cwd {
			t.Fatalf("card.Cwd = %q, want %q", card.Cwd, cwd)
		}
		if !slices.Contains(card.AlwaysKeys, "lark:POST /conversations/*/messages") {
			t.Fatalf("card.AlwaysKeys = %v, want lark route key", card.AlwaysKeys)
		}

		// The one-use grant lets this exact request through once, then blocks replay.
		if code, body := send(t, "POST", base+"/api/v1/conversations/c1/messages", `{"text":"hello"}`); code != http.StatusOK || body != `{"ok":true}` {
			t.Fatalf("approved proxied write = %d %s, want 200", code, body)
		}
		if code, _ := send(t, "POST", base+"/api/v1/conversations/c1/messages", `{"text":"hello"}`); code != http.StatusForbidden {
			t.Fatalf("replayed proxied write = %d, want 403", code)
		}
		reqs := api.requests()
		last := reqs[len(reqs)-1]
		if last.Method != "POST" || last.Path != "/api/v1/conversations/c1/messages" || last.Auth != "Bearer fake-credential-handle=with-equals" {
			t.Fatalf("unexpected upstream request: %+v", last)
		}
	})

	t.Run("phone denial and configured deny rules return allow=false with no grant", func(t *testing.T) {
		resetAsked()
		allow := false
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: &allow, Message: "do not send"}
		}
		hub.mu.Unlock()

		res, err := askBash(sid, "Bash", "call-deny-phone", `ex POST /conversations/c1/messages '{"text":"nope"}'`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || *res.Allow {
			t.Fatalf("phone deny resolution = %+v, want allow=false", res)
		}
		if !strings.Contains(res.Message, "do not send") {
			t.Fatalf("deny message = %q, want user reason", res.Message)
		}
		if code, _ := send(t, "POST", base+"/api/v1/conversations/c1/messages", `{"text":"nope"}`); code != http.StatusForbidden {
			t.Fatalf("denied write still reached proxy: %d", code)
		}

		resetAsked()
		res, err = askBash(sid, "Bash", "call-deny-rule", `ex DELETE /forbidden`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || *res.Allow {
			t.Fatalf("rule deny resolution = %+v, want allow=false", res)
		}
		if asked := askedSnapshot(); len(asked) != 0 {
			t.Fatalf("rule deny should not raise a card, got %d", len(asked))
		}
	})

	t.Run("bypassPermissions auto-allows non-gated commands but still gates outbound writes and fails closed on empty command", func(t *testing.T) {
		n.noteModes(sid, proto.ModeBypass, "")
		t.Cleanup(func() { n.noteModes(sid, proto.ModeDefault, "") })

		resetAsked()
		res, err := askBash(sid, "Bash", "call-bypass-ls", "ls -la", "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("bypass non-gated command = %+v, want allow=true", res)
		}
		if asked := askedSnapshot(); len(asked) != 0 {
			t.Fatalf("bypass non-gated command raised card: %+v", asked)
		}

		// Outbound send still asks even in bypassPermissions and grants the proxy token on approval.
		resetAsked()
		allow := true
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub.mu.Unlock()
		res, err = askBash(sid, "Bash", "call-bypass-send", `ex POST /conversations/c1/messages '{"text":"bypass-confirmed"}'`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("bypass send resolution = %+v, want allow=true", res)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "示例" || !asked[0].OutwardWrite {
			t.Fatalf("bypass send cards = %+v, want 1 示例 OutwardWrite card", asked)
		}
		if code, _ := send(t, "POST", base+"/api/v1/conversations/c1/messages", `{"text":"bypass-confirmed"}`); code != http.StatusOK {
			t.Fatalf("bypass-approved write = %d, want 200", code)
		}

		// Empty and truncated Bash commands fail closed to the phone instead of bypassing uninspected.
		for _, tc := range []struct {
			id, cmd string
		}{
			{"call-bypass-empty", "   "},
			{"call-bypass-truncated", "echo " + strings.Repeat("a", 64) + "\n…（已截断，共 30000 字节）"},
		} {
			resetAsked()
			res, err = askBash(sid, "Bash", tc.id, tc.cmd, "")
			if err != nil {
				t.Fatal(err)
			}
			if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "Bash" {
				t.Fatalf("%s in bypass should fail closed to Bash card, got %+v", tc.id, asked)
			}
		}

		// A runtime reporting a concrete non-bypass mode (e.g. plan) is never silently bypassed by a stale node mode.
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle, PermissionMode: "plan"},
			{SessionID: "session-regular", Title: "Regular Agent", Cwd: regularDir, Status: proto.StatusIdle},
		}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "row permission_mode plan updated", func() bool {
			row, _, ok := n.runtimes.rowOf(sid)
			return ok && row.PermissionMode == "plan"
		})
		resetAsked()
		res, err = askBash(sid, "Bash", "call-bypass-plan-drift", "ls -la", "")
		if err != nil {
			t.Fatal(err)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "Bash" {
			t.Fatalf("stale node bypass must not override runtime plan mode, got %+v", asked)
		}
		_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
			{SessionID: "session-regular", Title: "Regular Agent", Cwd: regularDir, Status: proto.StatusIdle},
		}})
		waitFor(t, "row permission_mode reset", func() bool {
			row, _, ok := n.runtimes.rowOf(sid)
			return ok && row.PermissionMode == ""
		})
	})

	t.Run("settings-driven row bypass auto-allows non-gated Bash when node mode is unset without overriding explicit default or weakening larkCheck", func(t *testing.T) {
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle, PermissionMode: proto.ModeBypass},
			{SessionID: "session-regular", Title: "Regular Agent", Cwd: regularDir, Status: proto.StatusIdle},
		}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "row permission_mode bypass updated", func() bool {
			row, _, ok := n.runtimes.rowOf(sid)
			return ok && row.PermissionMode == proto.ModeBypass
		})
		t.Cleanup(func() {
			_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
				{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
				{SessionID: "session-regular", Title: "Regular Agent", Cwd: regularDir, Status: proto.StatusIdle},
			}})
		})

		// Explicit node mode "default" still takes precedence over a row-reported bypass mode.
		n.noteModes(sid, proto.ModeDefault, "")
		allow := true
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub.mu.Unlock()
		resetAsked()
		res, err := askBash(sid, "Bash", "call-explicit-default-ls", "ls -la", "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("explicit default non-gated command = %+v", res)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "Bash" {
			t.Fatalf("explicit default mode must not be bypassed by row mode, got %+v", asked)
		}

		// Clear both live and persisted mode so node mode is genuinely unset.
		n.mu.Lock()
		delete(n.modes, sid)
		n.mu.Unlock()
		if st, ok := n.state.get(sid); ok {
			st.AppMode = ""
			n.state.put(sid, st)
		}
		resetAsked()
		res, err = askBash(sid, "Bash", "call-row-bypass-ls", "ls -la", "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("row bypass non-gated command = %+v, want allow=true", res)
		}
		if asked := askedSnapshot(); len(asked) != 0 {
			t.Fatalf("row bypass non-gated command raised card: %+v", asked)
		}

		// Even an ordinary (non-outbound) gated write still confirms because larkCheck uses n.modesOf, not row mode.
		resetAsked()
		res, err = askBash(sid, "Bash", "call-row-bypass-write", `ex POST /notes '{"title":"draft"}'`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("row bypass gated write = %+v", res)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "示例" {
			t.Fatalf("row bypass must not weaken larkCheck for gated writes, got %+v", asked)
		}

		// Auto-mode refusal fallback to bypassPermissions auto-allows non-gated ls while still confirming gated writes.
		n.noteModes(sid, "auto", "")
		resetAsked()
		res, err = askBash(sid, "Bash", "call-auto-fallback-ls", "ls -la", "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("auto-fallback non-gated command = %+v, want allow=true", res)
		}
		if asked := askedSnapshot(); len(asked) != 0 {
			t.Fatalf("auto-fallback non-gated command raised card: %+v", asked)
		}
	})

	t.Run("per-call cwd resolves relative @file bodies and enforces space confirmation", func(t *testing.T) {
		subDir := filepath.Join(cwd, "sub")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "body.json"), []byte(`{"text":"from-subdir"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		resetAsked()
		allow := true
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub.mu.Unlock()
		res, err := askBash(sid, "Bash", "call-rel-cwd", `ex POST /conversations/c1/messages @body.json`, "sub")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("rel cwd write = %+v", res)
		}
		asked := askedSnapshot()
		if len(asked) != 1 {
			t.Fatalf("expected 1 card, got %d", len(asked))
		}
		if asked[0].Cwd != cwd {
			t.Fatalf("card.Cwd = %q, want session cwd %q", asked[0].Cwd, cwd)
		}
		if !strings.Contains(asked[0].Summary, "from-subdir") {
			t.Fatalf("card summary did not read relative @body.json from sub cwd: %q", asked[0].Summary)
		}

		// A per-call cwd inside the health space must force confirmation even for an ordinary write in bypass mode.
		n.noteModes(sid, proto.ModeBypass, "")
		t.Cleanup(func() { n.noteModes(sid, proto.ModeDefault, "") })
		healthDir := filepath.Join(n.home, ".persona", "health")
		if err := os.MkdirAll(healthDir, 0o755); err != nil {
			t.Fatal(err)
		}
		resetAsked()
		res, err = askBash(sid, "Bash", "call-space-cwd", `ex POST /notes '{"title":"ok"}'`, healthDir)
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("space cwd write = %+v", res)
		}
		asked = askedSnapshot()
		if len(asked) != 1 {
			t.Fatalf("expected space write confirmation card in bypass mode, got %d", len(asked))
		}
		if asked[0].Cwd != healthDir {
			t.Fatalf("space card.Cwd = %q, want %q", asked[0].Cwd, healthDir)
		}
	})

	t.Run("non-assistant session and non-Bash assistant tool stay on normal runtime card flow", func(t *testing.T) {
		allow := true
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub.mu.Unlock()

		resetAsked()
		res, err := askBash("session-regular", "Bash", "call-reg", `ex POST /conversations/c1/messages '{"text":"reg"}'`, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("regular resolution = %+v", res)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "Bash" || asked[0].Agent != "dsh" {
			t.Fatalf("regular session card = %+v, want normal runtime Bash card", asked)
		}

		resetAsked()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var editRes proto.Resolution
		err = host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": sid, "tool_name": "Edit", "tool_use_id": "call-edit",
			"tool_input": map[string]any{"file_path": filepath.Join(cwd, "note.md")}, "summary": "edit note.md",
		}}, &editRes)
		if err != nil {
			t.Fatal(err)
		}
		if asked := askedSnapshot(); len(asked) != 1 || asked[0].ToolName != "Edit" || asked[0].Agent != "dsh" {
			t.Fatalf("non-Bash assistant card = %+v, want normal runtime Edit card", asked)
		}
	})

	t.Run("hub disconnect while waiting on Lark card returns node_gone", func(t *testing.T) {
		n2, hub2 := runtimeNode(t)
		t.Cleanup(func() { n2.gates().close() })
		assistantHub(hub2)
		cwd2 := prepareRuntimeAssistantEnvironment(t, n2, true)
		hello2 := assistantRuntimeHello()
		hello2["caps"].(map[string]any)["assistant_environment"] = true
		host2 := dialRuntime(t, n2, hello2)
		sid2 := startedAssistant(t, n2, host2)
		_ = cwd2

		askedCh := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		t.Cleanup(func() { close(release) })
		hub2.mu.Lock()
		hub2.resolve = func(ix proto.Interaction) proto.Resolution {
			once.Do(func() { close(askedCh) })
			<-release
			return proto.Resolution{Dismiss: true}
		}
		hub2.mu.Unlock()

		errCh := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var res proto.Resolution
			errCh <- host2.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
				"kind": "permission", "session_id": sid2, "tool_name": "Bash", "tool_use_id": "call-drop",
				"tool_input": map[string]any{"command": `ex POST /conversations/c1/messages '{"text":"drop"}'`},
			}}, &res)
		}()

		select {
		case <-askedCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for Lark card")
		}
		hub2.mu.Lock()
		dropCard := hub2.asked[0]
		hub2.mu.Unlock()
		if dropCard.ToolName != "示例" || !dropCard.OutwardWrite || !dropCard.Resumable {
			t.Fatalf("expected resumable Lark gate card before drop, got %+v", dropCard)
		}
		hub2.drop()

		var rpcErr *rpc.Error
		select {
		case err := <-errCh:
			if !errors.As(err, &rpcErr) || rpcErr.Code != "node_gone" {
				t.Fatalf("err = %v, want rpc.Error code node_gone", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for node_gone")
		}
	})

	t.Run("non-assistant_environment runtime and remote_agent Bash asks are never auto-bypassed", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			remote bool
		}{
			{name: "dsh without assistant_environment", remote: false},
			{name: "remote_agent with assistant_environment", remote: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				n2, hub2 := runtimeNode(t)
				assistantHub(hub2)
				cwd2 := prepareRuntimeAssistantEnvironment(t, n2, false)
				hello2 := assistantRuntimeHello()
				if tc.remote {
					hello2["caps"].(map[string]any)["assistant_environment"] = true
					hello2["caps"].(map[string]any)["remote_agent"] = true
				}
				host2 := dialRuntime(t, n2, hello2)
				sid2 := startedAssistant(t, n2, host2)
				n2.noteModes(sid2, proto.ModeBypass, "")
				_ = host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
					{SessionID: sid2, Title: "Main Assistant", Cwd: cwd2, Status: proto.StatusIdle},
				}})
				waitFor(t, "listed", func() bool { _, _, ok := n2.runtimes.rowOf(sid2); return ok })

				allow := true
				hub2.mu.Lock()
				hub2.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
				hub2.mu.Unlock()

				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				var res proto.Resolution
				err := host2.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
					"kind": "permission", "session_id": sid2, "tool_name": "Bash", "tool_use_id": "call-danger",
					"tool_input": map[string]any{"command": "rm -rf /tmp/some-dir"}, "summary": "rm -rf /tmp/some-dir",
				}}, &res)
				if err != nil {
					t.Fatal(err)
				}
				hub2.mu.Lock()
				got := append([]proto.Interaction(nil), hub2.asked...)
				hub2.mu.Unlock()
				if len(got) != 1 || got[0].ToolName != "Bash" {
					t.Fatalf("expected ordinary Bash card on phone, got %+v", got)
				}
			})
		}
	})

	t.Run("space session after node restart still confirms writes even when per-call cwd is outside space", func(t *testing.T) {
		n2, hub2 := runtimeNode(t)
		t.Cleanup(func() { n2.gates().close() })
		assistantHub(hub2)
		prepareRuntimeAssistantEnvironment(t, n2, true)
		healthDir := filepath.Join(n2.home, ".persona", "health")
		if err := os.MkdirAll(healthDir, 0o755); err != nil {
			t.Fatal(err)
		}
		hello2 := assistantRuntimeHello()
		hello2["caps"].(map[string]any)["assistant_environment"] = true
		host2 := dialRuntime(t, n2, hello2)
		const spaceSid = "session-health-1"
		_ = host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: spaceSid, Title: "Health", Cwd: healthDir, Space: "health", Status: proto.StatusIdle},
		}})
		waitFor(t, "space session listed", func() bool { _, _, ok := n2.runtimes.rowOf(spaceSid); return ok })
		n2.noteModes(spaceSid, proto.ModeBypass, "")
		// Ensure gateProxy.spaces is empty (as immediately after a personad restart).
		gp := n2.gates()
		gp.mu.Lock()
		gp.spaces = nil
		gp.mu.Unlock()

		allow := true
		hub2.mu.Lock()
		hub2.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub2.mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res proto.Resolution
		err := host2.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": spaceSid, "cwd": t.TempDir(), "tool_name": "Bash", "tool_use_id": "call-space-restart",
			"tool_input": map[string]any{"command": `ex POST /notes '{"title":"health-note"}'`},
		}}, &res)
		if err != nil {
			t.Fatal(err)
		}
		hub2.mu.Lock()
		got := append([]proto.Interaction(nil), hub2.asked...)
		hub2.mu.Unlock()
		if len(got) != 1 || got[0].ToolName != "示例" || got[0].Cwd != healthDir {
			t.Fatalf("space session with outside per-call cwd must confirm in space, got %+v", got)
		}
		if !gp.spaceSession(spaceSid) {
			t.Fatalf("expected gateProxy to latch markSpace(%q)", spaceSid)
		}
	})

	t.Run("blocked agent directory cannot be bypassed by per-call cwd and nil node state does not panic", func(t *testing.T) {
		n2, hub2 := runtimeNode(t)
		blockedDir := t.TempDir()
		n2.setBlockedAgentDirs([]string{blockedDir})
		host2 := dialRuntime(t, n2, dshHello())
		_ = host2.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: "session-blocked", Title: "Secret Bot", Cwd: blockedDir, Status: proto.StatusIdle},
		}})
		waitFor(t, "blocked session listed", func() bool { _, _, ok := n2.runtimes.rowOf("session-blocked"); return ok })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res proto.Resolution
		err := host2.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": "session-blocked", "cwd": t.TempDir(), "tool_name": "Bash", "tool_use_id": "call-blocked",
			"tool_input": map[string]any{"command": "ls"},
		}}, &res)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Dismiss {
			t.Fatalf("blocked session with outside cwd = %+v, want dismiss=true", res)
		}
		hub2.mu.Lock()
		askedCount := len(hub2.asked)
		hub2.mu.Unlock()
		if askedCount != 0 {
			t.Fatalf("blocked session leaked %d card(s) to hub", askedCount)
		}

		// Verify runtimeAsk itself tolerates a lightweight Node with n.state == nil without panicking.
		n3, hub3 := runtimeNode(t)
		host3 := dialRuntime(t, n3, dshHello())
		_ = host3.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: "session-nil-state", Title: "Plain", Cwd: t.TempDir(), Status: proto.StatusIdle},
		}})
		waitFor(t, "nil-state session listed", func() bool { _, _, ok := n3.runtimes.rowOf("session-nil-state"); return ok })
		h3, _, ok := n3.runtimes.belongs("session-nil-state")
		if !ok || h3 == nil {
			t.Fatal("missing runtime conn for session-nil-state")
		}
		allow := true
		hub3.mu.Lock()
		hub3.resolve = func(ix proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub3.mu.Unlock()
		lightNode := &Node{
			peer:     n3.peer,
			runtimes: n3.runtimes,
			home:     n3.home,
			logger:   n3.logger,
		}
		rawParams, err := json.Marshal(map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": "session-nil-state", "tool_name": "Bash", "tool_use_id": "call-nil",
			"tool_input": map[string]any{"command": "echo ok"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		resAny, err := lightNode.runtimeAsk(ctx, h3, rawParams)
		if err != nil {
			t.Fatal(err)
		}
		res3, ok := resAny.(proto.Resolution)
		if !ok || res3.Allow == nil || !*res3.Allow {
			t.Fatalf("nil-state runtimeAsk = %#v, want allow=true", resAny)
		}
	})
}

func TestRuntimeAssistantLarkEditRewritesViaGateProxy(t *testing.T) {
	oldCtx := larkContextFromCLI
	larkContextFromCLI = func(context.Context, *Node, proto.AssistantLark, larkCall) larkCardContext {
		return larkCardContext{}
	}
	t.Cleanup(func() { larkContextFromCLI = oldCtx })

	const original = "original runtime draft"
	edited := "Edited $HOME `printf nope` $(printf nope) \\\" ' ;\n第二行 😀"
	const body = `{"text":"original runtime draft","mentions":[{"key":"@_user_1","id":"ou_example"}],"large":9007199254740993}`
	const query = "receive_id_type=open_id&tag=a%20b"
	cmd := `ex POST '/conversations/c1/messages?` + query + `' '` + body + `'`

	t.Run("direct HTTP proxy rewrites body, preserves mentions and large ints, enforces one-use, and reports ledger", func(t *testing.T) {
		api := newFakeAPI(t)
		n, hub := runtimeNode(t)
		t.Cleanup(func() { n.gates().close() })
		assistantHub(hub)
		cwd := prepareRuntimeAssistantEnvironment(t, n, true)

		var larkSentMu sync.Mutex
		var larkSent []proto.LarkSent
		hub.mu.Lock()
		baseAnswer := hub.answer
		hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
			if method == proto.MethodLarkSent {
				var s proto.LarkSent
				if err := json.Unmarshal(params, &s); err != nil {
					return nil, err, true
				}
				larkSentMu.Lock()
				larkSent = append(larkSent, s)
				larkSentMu.Unlock()
				return struct{}{}, nil, true
			}
			return baseAnswer(method, params)
		}
		hub.mu.Unlock()

		n.mu.Lock()
		n.larkCfg.Gates[0].Env["EXAMPLE_BASE"] = api.URL
		n.larkCfg.Gates[0].Rules = []proto.LarkRule{
			{Method: "GET", Path: "/**", Action: "allow"},
			{Method: "POST", Path: "/conversations/*/messages", Body: map[string]any{"text": "blocked"}, Action: "deny"},
		}
		n.mu.Unlock()

		hello := assistantRuntimeHello()
		hello["caps"].(map[string]any)["assistant_environment"] = true
		host := dialRuntime(t, n, hello)
		sid := startedAssistant(t, n, host)
		base := runtimeAssistantEnvironment(t, host.got("session.start")[0])["EXAMPLE_BASE"]
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
		}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "assistant session listed", func() bool { _, _, ok := n.runtimes.rowOf(sid); return ok })

		ask := func(toolUseID, command string) (proto.Resolution, error) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var res proto.Resolution
			err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
				"kind":        "permission",
				"session_id":  sid,
				"tool_name":   "Bash",
				"tool_use_id": toolUseID,
				"tool_input":  map[string]any{"command": command},
				"summary":     command,
			}}, &res)
			return res, err
		}

		// Eligible send declares editable_text; edited Allow rewrites body via proxy while strictly binding original body.
		hub.mu.Lock()
		hub.asked = nil
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			if ix.EditableText != "" {
				return proto.Resolution{Allow: &allow, EditedText: edited}
			}
			return proto.Resolution{Allow: &allow}
		}
		hub.mu.Unlock()

		res, err := ask("call-edit-direct", cmd)
		if err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("edited allow resolution = %+v, want allow=true", res)
		}
		hub.mu.Lock()
		if len(hub.asked) != 1 {
			t.Fatalf("expected 1 card, got %d", len(hub.asked))
		}
		card := hub.asked[0]
		hub.mu.Unlock()
		if card.EditableText != original {
			t.Fatalf("card.EditableText = %q, want %q", card.EditableText, original)
		}

		url := base + "/api/v1/conversations/c1/messages"
		// Tampered query, mentions, large integer, or pre-edited body must all be rejected (403).
		if code, _ := send(t, http.MethodPost, url+"?receive_id_type=user_id&tag=a%20b", body); code != http.StatusForbidden {
			t.Fatalf("tampered query = %d, want 403", code)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, strings.Replace(body, "ou_example", "ou_other", 1)); code != http.StatusForbidden {
			t.Fatalf("tampered mention = %d, want 403", code)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, strings.Replace(body, "9007199254740993", "9007199254740992", 1)); code != http.StatusForbidden {
			t.Fatalf("adjacent large int = %d, want 403", code)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, `{"text":"wrong"}`); code != http.StatusForbidden {
			t.Fatalf("different body = %d, want 403", code)
		}
		if len(api.requests()) != 0 {
			t.Fatalf("rejected probes reached upstream: %+v", api.requests())
		}

		// Executing the unmodified original command hits the proxy with original body and upstream receives edited text.
		if code, resp := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusOK || resp != `{"ok":true}` {
			t.Fatalf("edited proxied write = %d %s, want 200", code, resp)
		}
		// One-use grant: replaying original or edited body both return 403.
		if code, _ := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusForbidden {
			t.Fatalf("replayed original body = %d, want 403", code)
		}
		reqs := api.requests()
		if len(reqs) != 1 || reqs[0].Query != query {
			t.Fatalf("upstream requests = %+v, want 1 with query %q", reqs, query)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, reqs[0].Body); code != http.StatusForbidden {
			t.Fatalf("replayed rewritten body = %d, want 403", code)
		}

		oldFields, _, ok := editableTextFields(body)
		if !ok {
			t.Fatal("original body not editable")
		}
		gotFields, gotText, ok := editableTextFields(reqs[0].Body)
		if !ok || gotText != edited {
			t.Fatalf("upstream body text = %q (ok=%v), want %q; raw=%s", gotText, ok, edited, reqs[0].Body)
		}
		for _, k := range []string{"mentions", "large"} {
			if string(gotFields[k]) != string(oldFields[k]) {
				t.Fatalf("upstream field %s changed: %s -> %s", k, oldFields[k], gotFields[k])
			}
		}

		waitFor(t, "edited LarkSent ledger report", func() bool {
			larkSentMu.Lock()
			defer larkSentMu.Unlock()
			return len(larkSent) == 1
		})
		larkSentMu.Lock()
		sentReport := larkSent[0]
		larkSentMu.Unlock()
		if sentReport.Text != edited || sentReport.LetThrough.EditedFrom != original ||
			sentReport.LetThrough.InteractionID != card.ID || sentReport.LetThrough.Kind != proto.LarkThroughCard {
			t.Fatalf("LarkSent = %+v, want edited text + EditedFrom=%q + InteractionID=%q", sentReport, original, card.ID)
		}

		// Shorter rewritten body also frames cleanly (Content-Length shrinks without truncation/hang).
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow, EditedText: "ok"}
		}
		hub.mu.Unlock()
		res, err = ask("call-edit-short", cmd)
		if err != nil || res.Allow == nil || !*res.Allow {
			t.Fatalf("short edit allow = %+v, %v", res, err)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusOK {
			t.Fatalf("short edit proxied write = %d, want 200", code)
		}
		reqs = api.requests()
		if _, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body); !ok || gotText != "ok" {
			t.Fatalf("short edit upstream text = %q (ok=%v), want ok", gotText, ok)
		}

		// Edited text matching a deny rule or blank text retires original grant and refuses (403 on original).
		for _, tc := range []struct {
			name, id, badEdit string
		}{
			{name: "deny rule", id: "call-edit-denied", badEdit: "blocked"},
			{name: "blank edit", id: "call-edit-blank", badEdit: "   "},
		} {
			hub.mu.Lock()
			hub.resolve = func(ix proto.Interaction) proto.Resolution {
				allow := true
				return proto.Resolution{Allow: &allow, EditedText: tc.badEdit}
			}
			hub.mu.Unlock()
			res, err := ask(tc.id, cmd)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if res.Allow == nil || *res.Allow || res.Reason != "gate_rule" {
				t.Fatalf("%s resolution = %+v, want allow=false reason=gate_rule", tc.name, res)
			}
			if code, _ := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusForbidden {
				t.Fatalf("%s left original grant usable: %d", tc.name, code)
			}
		}

		// Single-quoted literal '$' original body: denied edit leaves ZERO grants (no temporary wildcard leak),
		// and valid edit pins the exact original '$5' body and rewrites to the edited text.
		const dollarBody = `{"text":"price $5"}`
		const dollarCmd = `ex POST /conversations/c1/messages '{"text":"price $5"}'`
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow, EditedText: "blocked"}
		}
		hub.mu.Unlock()
		res, err = ask("call-edit-dollar-denied", dollarCmd)
		if err != nil || res.Allow == nil || *res.Allow || res.Reason != "gate_rule" {
			t.Fatalf("dollar denied edit = %+v, %v", res, err)
		}
		if code, _ := send(t, http.MethodPost, url, dollarBody); code != http.StatusForbidden {
			t.Fatalf("dollar denied edit left original usable: %d", code)
		}
		if code, _ := send(t, http.MethodPost, url, `{"text":"anything"}`); code != http.StatusForbidden {
			t.Fatalf("dollar denied edit leaked wildcard grant: %d", code)
		}

		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow, EditedText: "price $10"}
		}
		hub.mu.Unlock()
		res, err = ask("call-edit-dollar-ok", dollarCmd)
		if err != nil || res.Allow == nil || !*res.Allow {
			t.Fatalf("dollar valid edit = %+v, %v", res, err)
		}
		if code, _ := send(t, http.MethodPost, url, `{"text":"price /tmp"}`); code != http.StatusForbidden {
			t.Fatalf("expanded dollar body matched pinned original grant: %d", code)
		}
		if code, _ := send(t, http.MethodPost, url, dollarBody); code != http.StatusOK {
			t.Fatalf("exact literal-dollar original body = %d, want 200", code)
		}
		reqs = api.requests()
		if _, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body); !ok || gotText != "price $10" {
			t.Fatalf("dollar edit upstream text = %q (ok=%v), want price $10", gotText, ok)
		}

		// Hot-reloaded gate reorder + deny rule during card wait rejects cleanly with zero grants leaked.
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			other := proxiedGate(api.URL)
			other.Command = "othercmd"
			n.mu.Lock()
			moved := n.larkCfg.Gates[0]
			moved.Rules = append([]proto.LarkRule{
				{Method: http.MethodPost, Path: "/conversations/*/messages", Body: map[string]any{"text": "hot-denied"}, Action: "deny"},
			}, moved.Rules...)
			n.larkCfg.Gates = []proto.CommandGate{other, moved}
			n.mu.Unlock()
			allow := true
			return proto.Resolution{Allow: &allow, EditedText: "hot-denied"}
		}
		hub.mu.Unlock()
		res, err = ask("call-edit-hot-reload", cmd)
		if err != nil || res.Allow == nil || *res.Allow || res.Reason != "gate_rule" {
			t.Fatalf("hot-reloaded deny edit = %+v, %v", res, err)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusForbidden {
			t.Fatalf("hot-reloaded deny edit leaked original grant: %d", code)
		}
		n.mu.Lock()
		n.larkCfg.Gates = []proto.CommandGate{n.larkCfg.Gates[1]}
		n.mu.Unlock()

		// Detached approval retry carrying ApprovalID + EditedText skips the unedited grant and rewrites cleanly.
		const origDetachedID = "ix_detached_orig_1"
		waitFor(t, "literal-dollar edited LarkSent report", func() bool {
			larkSentMu.Lock()
			defer larkSentMu.Unlock()
			return len(larkSent) >= 3
		})
		larkSentMu.Lock()
		beforeDetachedSent := len(larkSent)
		larkSentMu.Unlock()
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow, ApprovalID: origDetachedID, EditedText: "detached edited text"}
		}
		hub.mu.Unlock()
		res, err = ask("call-edit-detached-retry", cmd)
		if err != nil || res.Allow == nil || !*res.Allow {
			t.Fatalf("detached retry edit = %+v, %v", res, err)
		}
		if code, _ := send(t, http.MethodPost, url+"?"+query, body); code != http.StatusOK {
			t.Fatalf("detached retry proxied write = %d, want 200", code)
		}
		reqs = api.requests()
		if _, gotText, ok := editableTextFields(reqs[len(reqs)-1].Body); !ok || gotText != "detached edited text" {
			t.Fatalf("detached retry upstream text = %q (ok=%v)", gotText, ok)
		}
		waitFor(t, "detached edited LarkSent report", func() bool {
			larkSentMu.Lock()
			defer larkSentMu.Unlock()
			return len(larkSent) >= beforeDetachedSent+1
		})
		larkSentMu.Lock()
		detachedReport := larkSent[len(larkSent)-1]
		larkSentMu.Unlock()
		if detachedReport.Text != "detached edited text" || detachedReport.LetThrough.EditedFrom != original ||
			detachedReport.LetThrough.InteractionID != origDetachedID {
			t.Fatalf("detached LarkSent = %+v, want ApprovalID=%q EditedFrom=%q", detachedReport, origDetachedID, original)
		}

		// Ineligible commands must NOT declare editable_text.
		if err := os.WriteFile(filepath.Join(cwd, "msg.json"), []byte(`{"text":"from file"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow}
		}
		hub.mu.Unlock()
		for _, tc := range []struct {
			name, id, badCmd string
		}{
			{name: "non-message route", id: "call-inelig-route", badCmd: `ex POST /messages/om_1/reactions '{"text":"hi"}'`},
			{name: "multi-call", id: "call-inelig-multi", badCmd: `ex POST /conversations/c1/messages '{"text":"a"}' && ex POST /conversations/c1/messages '{"text":"b"}'`},
			{name: "dynamic dollar", id: "call-inelig-dyn", badCmd: `ex POST /conversations/c1/messages "$BODY"`},
			{name: "at-file", id: "call-inelig-file", badCmd: `ex POST /conversations/c1/messages @msg.json`},
			{name: "non-object json", id: "call-inelig-arr", badCmd: `ex POST /conversations/c1/messages '["text"]'`},
		} {
			hub.mu.Lock()
			hub.asked = nil
			hub.mu.Unlock()
			if _, err := ask(tc.id, tc.badCmd); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			hub.mu.Lock()
			asked := append([]proto.Interaction(nil), hub.asked...)
			hub.mu.Unlock()
			for _, ix := range asked {
				if ix.EditableText != "" {
					t.Fatalf("%s unexpectedly set EditableText=%q", tc.name, ix.EditableText)
				}
			}
		}
	})

	t.Run("relayed gate forwards rewritten body and records ledger", func(t *testing.T) {
		n, hub := runtimeNode(t)
		t.Cleanup(func() { n.gates().close() })
		assistantHub(hub)
		cwd := prepareRuntimeAssistantEnvironment(t, n, false)

		var mu sync.Mutex
		var relayed []proto.GateRelayParams
		var larkSent []proto.LarkSent
		hub.mu.Lock()
		baseAnswer := hub.answer
		hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
			switch method {
			case proto.MethodGateRelay:
				var req proto.GateRelayParams
				if err := json.Unmarshal(params, &req); err != nil {
					return nil, err, true
				}
				mu.Lock()
				relayed = append(relayed, req)
				mu.Unlock()
				return proto.GateRelayAnswer{
					Status: http.StatusOK,
					Header: map[string][]string{"Content-Type": {"application/json"}},
					Body:   []byte(`{"data":{"message_id":"om_relayed_1"}}`),
				}, nil, true
			case proto.MethodLarkSent:
				var s proto.LarkSent
				if err := json.Unmarshal(params, &s); err != nil {
					return nil, err, true
				}
				mu.Lock()
				larkSent = append(larkSent, s)
				mu.Unlock()
				return struct{}{}, nil, true
			}
			return baseAnswer(method, params)
		}
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			allow := true
			return proto.Resolution{Allow: &allow, EditedText: "relayed edited text"}
		}
		hub.mu.Unlock()

		g := exampleGate("")
		g.BaseEnv = "RELAY_BASE"
		g.SocketEnv = "RELAY_SOCKET"
		g.Relay = true
		g.Env = map[string]string{"RELAY_SOCKET": "/run/fake.sock"}
		cfg := proto.AssistantLark{Gates: []proto.CommandGate{g}}
		relayGates(&cfg)
		n.mu.Lock()
		n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
		n.mu.Unlock()

		hello := assistantRuntimeHello()
		hello["caps"].(map[string]any)["assistant_environment"] = true
		host := dialRuntime(t, n, hello)
		sid := startedAssistant(t, n, host)
		base := runtimeAssistantEnvironment(t, host.got("session.start")[0])["RELAY_BASE"]
		if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
			{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
		}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "relay session listed", func() bool { _, _, ok := n.runtimes.rowOf(sid); return ok })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res proto.Resolution
		if err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind":        "permission",
			"session_id":  sid,
			"tool_name":   "Bash",
			"tool_use_id": "call-relay-edit",
			"tool_input":  map[string]any{"command": cmd},
		}}, &res); err != nil {
			t.Fatal(err)
		}
		if res.Allow == nil || !*res.Allow {
			t.Fatalf("relay edit resolution = %+v, want allow=true", res)
		}
		if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/c1/messages?"+query, body); code != http.StatusOK {
			t.Fatalf("relayed edited write = %d, want 200", code)
		}
		if code, _ := send(t, http.MethodPost, base+"/api/v1/conversations/c1/messages?"+query, body); code != http.StatusForbidden {
			t.Fatalf("replayed relayed write = %d, want 403", code)
		}
		mu.Lock()
		gotRelayed := append([]proto.GateRelayParams(nil), relayed...)
		mu.Unlock()
		if len(gotRelayed) != 1 {
			t.Fatalf("relayed count = %d, want 1", len(gotRelayed))
		}
		if fields, text, ok := editableTextFields(string(gotRelayed[0].Body)); !ok || text != "relayed edited text" || string(fields["large"]) != "9007199254740993" {
			t.Fatalf("relayed body = %s, want edited text + preserved large int", gotRelayed[0].Body)
		}
		waitFor(t, "relayed LarkSent report", func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(larkSent) == 1
		})
		mu.Lock()
		rep := larkSent[0]
		mu.Unlock()
		if rep.Text != "relayed edited text" || rep.LetThrough.EditedFrom != original || rep.MessageRef != "om_relayed_1" {
			t.Fatalf("relayed LarkSent = %+v", rep)
		}
	})
}

func TestRuntimeAssistantLarkPendingAndDenyPropagation(t *testing.T) {
	oldCtx := larkContextFromCLI
	larkContextFromCLI = func(context.Context, *Node, proto.AssistantLark, larkCall) larkCardContext {
		return larkCardContext{}
	}
	t.Cleanup(func() { larkContextFromCLI = oldCtx })

	api := newFakeAPI(t)
	n, hub := runtimeNode(t)
	t.Cleanup(func() { n.gates().close() })
	assistantHub(hub)
	cwd := prepareRuntimeAssistantEnvironment(t, n, true)
	n.mu.Lock()
	n.larkCfg.Gates[0].Env["EXAMPLE_BASE"] = api.URL
	n.larkCfg.Gates[0].Rules = []proto.LarkRule{
		{Method: "DELETE", Path: "/forbidden", Action: "deny"},
	}
	n.mu.Unlock()

	hello := assistantRuntimeHello()
	hello["caps"].(map[string]any)["assistant_environment"] = true
	host := dialRuntime(t, n, hello)
	sid := startedAssistant(t, n, host)
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: sid, Title: "Main Assistant", Cwd: cwd, Status: proto.StatusIdle},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "session listed", func() bool { _, _, ok := n.runtimes.rowOf(sid); return ok })

	ask := func(toolUseID, command string) proto.Resolution {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var res proto.Resolution
		if err := host.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{
			"kind": "permission", "session_id": sid, "tool_name": "Bash", "tool_use_id": toolUseID,
			"tool_input": map[string]any{"command": command},
		}}, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	const sendCmd = `ex POST /conversations/c1/messages '{"text":"hello"}'`

	t.Run("detached pending propagates Pending and Dismiss without Allow", func(t *testing.T) {
		hub.mu.Lock()
		hub.resolve = func(ix proto.Interaction) proto.Resolution {
			return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: "Waiting for owner decision."}
		}
		hub.mu.Unlock()
		res := ask("call-pending", sendCmd)
		if !res.Pending || !res.Dismiss || res.Allow != nil || res.ApprovalID == "" || !strings.Contains(res.Message, "Waiting") {
			t.Fatalf("pending resolution = %+v, want dismiss=true pending=true allow=nil", res)
		}
	})

	t.Run("plain phone deny without note leaves Message empty and Reason empty", func(t *testing.T) {
		allow := false
		hub.mu.Lock()
		hub.resolve = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow} }
		hub.mu.Unlock()
		res := ask("call-plain-deny", sendCmd)
		if res.Allow == nil || *res.Allow || res.Message != "" || res.Reason != "" || res.Pending || res.Dismiss {
			t.Fatalf("plain deny resolution = %+v, want allow=false empty message/reason", res)
		}
	})

	t.Run("phone deny with note preserves trimmed Message and leaves Reason empty", func(t *testing.T) {
		allow := false
		hub.mu.Lock()
		hub.resolve = func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: &allow, Message: "  Use a shorter reply  "}
		}
		hub.mu.Unlock()
		res := ask("call-note-deny", sendCmd)
		if res.Allow == nil || *res.Allow || res.Message != "Use a shorter reply" || res.Reason != "" {
			t.Fatalf("deny-with-note resolution = %+v, want allow=false message='Use a shorter reply' reason=''", res)
		}
	})

	t.Run("configured rule deny sets Reason gate_rule", func(t *testing.T) {
		res := ask("call-rule-deny", `ex DELETE /forbidden`)
		if res.Allow == nil || *res.Allow || res.Reason != "gate_rule" || res.Message == "" {
			t.Fatalf("rule deny resolution = %+v, want allow=false reason=gate_rule", res)
		}
	})

	t.Run("assistant-attributed deny is not treated as an owner note", func(t *testing.T) {
		allow := false
		hub.mu.Lock()
		hub.resolve = func(proto.Interaction) proto.Resolution {
			return proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant, Message: "supervisor refused"}
		}
		hub.mu.Unlock()
		res := ask("call-asst-deny", sendCmd)
		if res.Allow == nil || *res.Allow || res.Reason != "gate_rule" {
			t.Fatalf("assistant-attributed deny = %+v, want allow=false reason=gate_rule", res)
		}
	})
}
