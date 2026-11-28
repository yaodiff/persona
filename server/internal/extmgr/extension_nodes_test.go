package extmgr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

func TestExtensionIdentityRevisionSurvivesPostgresTimestampPrecision(t *testing.T) {
	stamp := time.Date(2026, 11, 8, 12, 0, 0, 123456789, time.UTC)
	before := &extension.Extension{Record: extension.Record{ID: "new-name", StorageID: "old-name", UpdatedAt: stamp, InstalledAt: stamp.Add(-time.Hour)}}
	after := *before
	after.Record.UpdatedAt = stamp.Truncate(time.Microsecond)
	after.Record.InstalledAt = before.Record.InstalledAt.Truncate(time.Microsecond)
	if extensionNodeIdentity(before) != extensionNodeIdentity(&after) {
		t.Fatal("Postgres reload changed the node revision or installation generation")
	}
	legacy := *before
	legacy.Record.StorageID = ""
	if extensionNodeRevision(&legacy) != 0 {
		t.Fatal("same-ID legacy wire gained an identity revision")
	}
}

func TestExtensionIdentityChecksConfiguredNodesBeforeCommit(t *testing.T) {
	h, _ := extensionTestHub(t)
	reg := h.extensions.reg
	pv, err := reg.Preview(t.Context(), "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(t.Context(), pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	legacy, calls := extensionNodeMachine(t, h, "legacy-node", "linux", "amd64", false, false)
	modern, _ := extensionNodeMachine(t, h, "modern-node", "linux", "amd64", false)
	// during holds the legacy node's calls made by the last preflight alone. The config change also syncs the program
	// to its computers in the background (have, put, start: not the preflight's doing); holding the node's sync lock
	// around the preflight keeps those calls out of this window.
	var during []browserCallSeen
	check := func(computers string) error {
		t.Helper()
		if err := reg.SetConfig(t.Context(), "example-helper", json.RawMessage(`{"computers":`+computers+`}`), "test"); err != nil {
			t.Fatal(err)
		}
		l := h.extensions.nodes.lock("legacy-node")
		l.Lock()
		defer l.Unlock()
		n := len(calls())
		err := h.checkRenameNodeCapabilities(t.Context(), reg.SnapshotExact("example-helper"))
		during = calls()[n:]
		return err
	}
	refused := func(err error, key, want string) {
		t.Helper()
		var m *i18n.Message
		if !errors.As(err, &m) || m.Key != key {
			t.Fatalf("refusal %v, want %s", err, key)
		}
		if text := m.Text(i18n.En); !strings.Contains(text, want) {
			t.Fatalf("refusal does not name %q: %s", want, text)
		}
	}
	refused(check(`["legacy-node"]`), "extension-identity.node_outdated", "Update the node on legacy-node ")
	if len(during) == 0 {
		t.Fatal("the preflight did not probe the legacy node")
	}
	for _, call := range during {
		if call.Method != "plugin.node.identity" {
			t.Fatalf("capability preflight mutated a legacy node: %s", call.Method)
		}
	}
	legacy.mu.Lock()
	legacy.online = false
	legacy.mu.Unlock()
	refused(check(`["legacy-node"]`), "extension-identity.node_unreachable", "Can't reach legacy-node. Connect it,")
	// A picked ID the fleet no longer has (removed, or from before the hub moved) is not a node to wait for (#2510).
	if err := check(`["modern-node","gone-node"]`); err != nil {
		t.Fatalf("a machine ID that is not a computer blocked the identity change: %v", err)
	}
	refused(check(`["gone-node","legacy-node","modern-node"]`), "extension-identity.node_unreachable", "Can't reach legacy-node.")
	// A node whose link dropped before it answered the probe is not reported as outdated.
	modern.mu.Lock()
	modern.peer = nil
	modern.mu.Unlock()
	refused(check(`["legacy-node","modern-node"]`), "extension-identity.node_unreachable", "Can't reach legacy-node, modern-node. Connect them,")
	if text := i18n.Localize(check(`["legacy-node","modern-node"]`), i18n.ZhHans); !strings.Contains(text, "legacy-node、modern-node") {
		t.Fatalf("zh-Hans list: %s", text)
	}
}

func TestPluginIdentityCapabilityBeforeAnyDestructivePhase(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers":["legacy-node","modern-node"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetSecret(ctx, "example-helper", "api_key", "fixture-key", "test"); err != nil {
		t.Fatal(err)
	}
	// A committed renamed-record fixture, kept in this test's fake registry/store.
	p := reg.GetExact("example-helper")
	p.Record.StorageID = "previous-helper"
	legacy, seenLegacy := extensionNodeMachine(t, h, "legacy-node", "linux", "amd64", false, false)
	h.syncExtensionNodes(ctx, legacy)
	for _, call := range seenLegacy() {
		if call.Method != "plugin.node.identity" {
			t.Fatalf("legacy node got identity payload before capability: %+v", call)
		}
	}
	modern, seenModern := extensionNodeMachine(t, h, "modern-node", "linux", "amd64", false)
	h.syncExtensionNodes(ctx, modern)
	var started, synced bool
	for _, call := range seenModern() {
		switch call.Method {
		case "plugin.node.start":
			started = true
			if call.Params["id"] != "example-helper" || call.Params["storage_id"] != "previous-helper" {
				t.Fatalf("public/storage identities: %+v", call.Params)
			}
		case "plugin.node.sync":
			synced = true
			if call.Params["identity"] != true || len(call.Params["installed_slots"].([]any)) == 0 {
				t.Fatalf("missing stable installed slots: %+v", call.Params)
			}
		}
	}
	if !started || !synced {
		t.Fatal("capable node did not apply identity and synchronize")
	}
	// A late old revision cannot replace the newly recorded node state.
	h.extensions.keepNodeState(p.Record.ID, p.Record.Version, modern.cfg.ID, proto.PluginNodeState{ID: p.Record.ID, Version: p.Record.Version, Revision: 1, State: "error"})
	for _, st := range h.extensions.nodes.views(p.Record.ID) {
		if st.MachineID == modern.cfg.ID && st.State != "running" {
			t.Fatalf("late revision overwrote current state: %+v", st)
		}
	}
	// A fresh committed row after uninstall defaults StorageID to empty. The authenticated
	// bound-slot probe adapts only this capable node; the registry and legacy wire stay unchanged.
	p.Record.StorageID = ""
	p.Record.InstalledAt = p.Record.UpdatedAt.Add(time.Second)
	p.Record.UpdatedAt = p.Record.InstalledAt
	h.syncExtensionNodes(ctx, modern)
	var lastStart map[string]any
	for _, call := range seenModern() {
		if call.Method == "plugin.node.start" {
			lastStart = call.Params
		}
	}
	if lastStart["storage_id"] != "example-helper" || p.Record.StorageID != "" {
		t.Fatalf("fresh reinstall adaptation mutated registry or used old slot: %+v", lastStart)
	}
	before := len(seenModern())
	modern.mu.Lock()
	modern.online = false
	modern.mu.Unlock()
	h.syncExtensionNodes(ctx, modern)
	if len(seenModern()) != before {
		t.Fatal("offline node received identity/start/destructive sync")
	}
}

// extensionNodeMachine is a machine whose node answers plugin.node.* as one that runs plugins' programs does (or, old,
// as one from before them), and says what it was asked.
func extensionNodeMachine(t *testing.T, h *Manager, id, goos, goarch string, old bool, identity ...bool) (*Machine, func() []browserCallSeen) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	var mu sync.Mutex
	var seen []browserCallSeen
	have := map[string]bool{}
	bound := map[string]proto.PluginNodeIdentity{}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		seen = append(seen, browserCallSeen{method, p})
		if old {
			return nil, &rpc.Error{Message: "unknown method " + method}
		}
		switch method {
		case "plugin.node.identity":
			if len(identity) > 0 && !identity[0] {
				return nil, &rpc.Error{Message: "unknown method " + method}
			}
			var request proto.PluginNodeIdentityRequest
			if err := json.Unmarshal(params, &request); err != nil {
				return nil, err
			}
			for _, slot := range request.Slots {
				bound[slot.StorageID] = slot
			}
			reply := proto.PluginNodeIdentityReply{Supported: true}
			for _, slot := range bound {
				reply.Slots = append(reply.Slots, slot)
				reply.Revision = max(reply.Revision, slot.Revision)
			}
			return reply, nil
		case "plugin.node.have":
			return proto.PluginNodeHave{Have: have[p["digest"].(string)]}, nil
		case "plugin.node.put.begin":
			have[p["digest"].(string)] = true // as if it all came
			return map[string]string{"upload_id": "u1"}, nil
		case "plugin.node.put.chunk", "plugin.node.put.commit", "plugin.node.renew", "plugin.node.stop":
			return map[string]bool{"ok": true}, nil
		case "plugin.node.start":
			var start proto.PluginNodeStart
			if err := json.Unmarshal(params, &start); err != nil {
				return nil, err
			}
			return proto.PluginNodeState{ID: start.ID, Version: start.Version, Revision: start.Revision, State: "running"}, nil
		case "plugin.node.sync":
			return proto.PluginNodeStates{}, nil
		case "plugin.node.call":
			return json.RawMessage(`{"echo":` + string(must(json.Marshal(p["params"]))) + `}`), nil
		}
		return nil, &rpc.Error{Message: "unknown method " + method}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online, m.info = hubSide, true, proto.NodeInfo{OS: goos, Arch: goarch}
	h.fleetMu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleetMu.Unlock()
	return m, func() []browserCallSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]browserCallSeen(nil), seen...)
	}
}

func must(b []byte, _ error) []byte { return b }

// helperPackage is a plugin whose only program runs on computers (Linux and Mac ones), picked in "computers"; its
// api_key goes to the computers, hub_only does not.
func helperPackage(t *testing.T) string {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "node"), 0o755)
	for _, f := range []string{"helper-linux", "helper-mac"} {
		_ = os.WriteFile(filepath.Join(dir, "node", f), []byte("#!/bin/sh\n"), 0o755)
	}
	manifest := `{"manifest_version": 1, "id": "example-helper", "name": "Example Helper", "version": "1.0.0",
		"engines": {"plugin_protocol": 1},
		"node": {"command": {"linux-amd64": "node/helper-linux", "darwin-arm64": "node/helper-mac"}, "machines": "computers", "secrets": ["api_key"]},
		"contributes": {"providers": [{"id": "example-ai", "name": "Example AI", "kind": "api", "api": "responses",
			"base_url": "https://api.example.com/v1", "models": [{"id": "example-large"}]}]},
		"configuration": {"type": "object", "properties": {"computers": {"type": "array", "title": "在哪些电脑上运行",
			"items": {"type": "string", "format": "machine", "x-os": ["darwin", "linux"]}}}},
		"secrets": [{"name": "api_key", "title": "API Key"}, {"name": "hub_only", "title": "只在 hub 上"}]}`
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A plugin's program goes to the machines picked for it and starts there with only the secrets it sends them;
// the others are left alone; a node from before #1830 says so; the program's state and calls go through the hub.
func TestPluginProgramsOnMachines(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	box, boxSeen := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	_, otherSeen := extensionNodeMachine(t, h, "other", "linux", "amd64", false)
	oldBox, _ := extensionNodeMachine(t, h, "oldbox", "darwin", "arm64", true)
	win, winSeen := extensionNodeMachine(t, h, "win", "windows", "amd64", false)

	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	_ = reg.SetSecret(ctx, "example-helper", "api_key", "k-1", "test")
	_ = reg.SetSecret(ctx, "example-helper", "hub_only", "h-1", "test")
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box", "oldbox", "win"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	for _, m := range []*Machine{box, oldBox, win} {
		h.syncExtensionNodes(ctx, m)
	}

	var start map[string]any
	for _, c := range boxSeen() {
		if c.Method == "plugin.node.start" {
			start = c.Params
		}
	}
	if start == nil {
		t.Fatalf("not started on box: %+v", boxSeen())
	}
	if start["command"] != "node/helper-linux" || start["keep"] != "lease" || start["lease_seconds"] != float64(1800) {
		t.Fatalf("%+v", start)
	}
	initJSON, _ := json.Marshal(start["init"])
	if s := string(initJSON); !strings.Contains(s, `"api_key":"k-1"`) || strings.Contains(s, "h-1") {
		t.Fatalf("only the secrets for computers: %s", s)
	}
	for _, c := range otherSeen() {
		if strings.HasPrefix(c.Method, "plugin.node.start") || strings.HasPrefix(c.Method, "plugin.node.put") {
			t.Fatalf("a machine not picked got it: %+v", c)
		}
	}
	for _, c := range winSeen() {
		if strings.HasPrefix(c.Method, "plugin.node.start") {
			t.Fatal("started on a system it has no program for")
		}
	}
	var puts int
	for _, c := range boxSeen() {
		if c.Method == "plugin.node.put.commit" {
			puts++
		}
	}
	h.syncExtensionNodes(ctx, box) // again: it has the package
	var putsAfter int
	for _, c := range boxSeen() {
		if c.Method == "plugin.node.put.commit" {
			putsAfter++
		}
	}
	if puts == 0 || putsAfter != puts {
		t.Fatalf("the package goes once: %d, then %d", puts, putsAfter)
	}

	views := map[string]ExtensionNodeView{}
	for _, v := range h.extensions.view(ctx, reg.Get("example-helper")).Nodes {
		views[v.MachineID] = v
	}
	if views["box"].State != "running" || views["oldbox"].State != extensionNodeOutdated || views["other"].State != "" {
		t.Fatalf("%+v", views)
	}

	// What the node says of it later (its lease ran out while the hub was away) shows.
	h.NodeEvent("box", "plugin.node.state", json.RawMessage(`{"id":"example-helper","version":"1.0.0","state":"lease_expired"}`))
	for _, v := range h.extensions.view(ctx, reg.Get("example-helper")).Nodes {
		if v.MachineID == "box" && v.State != "lease_expired" {
			t.Fatalf("%+v", v)
		}
	}

	// nodes.call reaches the program on a machine it runs on, not on another.
	out, err := h.nodesCall(ctx, "example-helper", json.RawMessage(`{"machine": "box", "method": "x.echo", "params": {"a": 1}}`))
	if err != nil || !strings.Contains(string(out.(json.RawMessage)), `"a":1`) {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := h.nodesCall(ctx, "example-helper", json.RawMessage(`{"machine": "other", "method": "x.echo"}`)); err == nil {
		t.Fatal("a machine it is not switched on for")
	}

	// Switched off for box: box is told to keep nothing of it (the package goes, its data stays: still installed).
	_ = reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["oldbox"]}`), "test")
	h.syncExtensionNodes(ctx, box)
	var last map[string]any
	for _, c := range boxSeen() {
		if c.Method == "plugin.node.sync" {
			last = c.Params
		}
	}
	if progs, _ := last["programs"].([]any); len(progs) != 0 || !strings.Contains(string(must(json.Marshal(last["installed"]))), "example-helper") {
		t.Fatalf("%+v", last)
	}
}

// extensionHTTPNode is a machine whose node answers plugin.node.dial by streaming to the program's HTTP server, here
// addr (a dev server standing in for it), and says which plugins it was asked for.
func extensionHTTPNode(t *testing.T, h *Manager, id, addr string) (*Machine, *[]string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	hubMux, nodeMux := streams.New(hubSide.Notify), streams.New(nodeSide.Notify)
	hubSide.OnEvent = func(e string, p json.RawMessage) { hubMux.Event(e, p) }
	nodeSide.OnEvent = func(e string, p json.RawMessage) { nodeMux.Event(e, p) }
	var mu sync.Mutex
	var asked []string
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "plugin.node.dial" {
			return map[string]any{}, nil
		}
		var p struct{ ID, Plugin string }
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		asked = append(asked, p.Plugin)
		mu.Unlock()
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			return nil, &rpc.Error{Code: "not_running", Message: "not answering"}
		}
		if err := nodeMux.Open(p.ID); err != nil {
			c.Close()
			return nil, err
		}
		nodeMux.Start(p.ID, c)
		return map[string]any{}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); h2nW.Close(); n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online, m.streams, m.info = hubSide, true, hubMux, proto.NodeInfo{OS: "linux", Arch: "amd64"}
	h.fleetMu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleetMu.Unlock()
	return m, &asked
}

// An app's requests reach a plugin's program on the computer it runs on, through the hub: a page, a WebSocket both
// ways; not on a machine it is not switched on for, not while that machine is away, not without the plugin's http.
func TestAppsRequestsReachTheProgramOnAComputer(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	h.cfg.Token = "test-token"
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	dev, _ := b1DevServer(t)
	box, asked := extensionHTTPNode(t, h, "box", dev.Listener.Addr().String())
	extensionHTTPNode(t, h, "other", dev.Listener.Addr().String())

	pkg := helperPackage(t)
	raw, _ := os.ReadFile(filepath.Join(pkg, extension.ManifestFile))
	_ = os.WriteFile(filepath.Join(pkg, extension.ManifestFile), []byte(strings.Replace(string(raw), `"machines": "computers"`, `"machines": "computers", "http": true`, 1)), 0o644)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	base := srv.URL + "/api/v1/plugins/example-helper/-/machines/"
	get := func(url string) (int, string) {
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer test-token")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if code, body := get(base + "box/"); code != 200 || !strings.Contains(body, "<title>Dev</title>") || !strings.Contains(body, "host=plugin") {
		t.Fatalf("%d %q", code, body)
	}
	if len(*asked) == 0 || (*asked)[0] != "example-helper" {
		t.Fatalf("the node was asked for %v", *asked)
	}
	// The program is told who is calling, as an extension on the hub's machine is (extension_caller.go): the credential,
	// its role and language, nothing else the hub passed on (name, address, owner) or the client sent.
	phone := deviceToken("dev_phone", roleFull)
	for _, c := range []struct{ name, token, device string }{{"the master token", "test-token", ""}, {"a phone", phone, "dev_phone"}} {
		req, _ := http.NewRequest("GET", base+"box/caller", nil)
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept-Language", "ja")
		req.Header.Set("X-Persona-Device", "someone-else")
		req.Header.Set("X-Persona-Forged", "1")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		_ = json.NewDecoder(res.Body).Decode(&got)
		res.Body.Close()
		if res.StatusCode != 200 || len(got) != 3 || got[extwire.HeaderDevice] != c.device || got[extwire.HeaderRole] != roleFull ||
			got[extwire.HeaderLang] != "ja" {
			t.Fatalf("%s: %d %v", c.name, res.StatusCode, got)
		}
	}

	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ws := "ws" + strings.TrimPrefix(base, "http") + "box/hmr"
	c, _, err := websocket.Dial(wctx, ws, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test-token"}}})
	if err != nil {
		t.Fatalf("websocket to the program: %v", err)
	}
	defer c.CloseNow()
	if err := c.Write(wctx, websocket.MessageText, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if _, got, err := c.Read(wctx); err != nil || string(got) != "echo:hi" {
		t.Fatalf("%q %v", got, err)
	}

	if code, _ := get(base + "other/"); code != http.StatusNotFound {
		t.Fatalf("a machine it is not switched on for: %d", code)
	}
	box.mu.Lock()
	box.online = false
	box.mu.Unlock()
	if code, _ := get(base + "box/"); code != http.StatusConflict {
		t.Fatalf("the machine away: %d", code)
	}
	if code, _ := get(srv.URL + "/api/v1/plugins/nobody/-/machines/box/"); code != http.StatusNotFound {
		t.Fatalf("no such plugin: %d", code)
	}
}

// The apps learn which field picks the computers, and switching one on asks first (a program will run there), unless
// the manifest asks something of its own; the plugin's own form stays as it is.
func TestNodeProgramsFieldAsksBeforeAMachine(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	p := reg.Get("example-helper")
	d := h.extensions.detail(ctx, p)
	if d.NodeMachines != "computers" {
		t.Fatalf("node_machines %q", d.NodeMachines)
	}
	ask := func(s *extension.Schema) string {
		for _, f := range s.Fields {
			if f.Name == "computers" {
				return f.Confirm
			}
		}
		return "no field"
	}
	if got := ask(d.Form); !strings.Contains(got, "将在这台电脑上运行「Example Helper」的程序") {
		t.Fatalf("confirm %q", got)
	}
	if got := ask(p.Schema); got != "" {
		t.Fatalf("the plugin's own form changed: %q", got)
	}
}

// nodePackageFiles are the package files the hub keeps for plugin id's program on computers.
func nodePackageFiles(t *testing.T, h *Manager, id string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(h.extensions.root, "node-packages", id+"-*.tgz"))
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range files {
		files[i] = filepath.Base(f)
	}
	return files
}

// nodePackageKeys are the hub's cached package entries of plugin id.
func nodePackageKeys(h *Manager, id string) []string {
	pn := &h.extensions.nodes
	pn.mu.Lock()
	defer pn.mu.Unlock()
	var keys []string
	for k := range pn.packages {
		if strings.HasPrefix(k, id+"/") {
			keys = append(keys, k)
		}
	}
	return keys
}

// An upgrade leaves only the new version's package; an uninstall leaves no package and no machine states, and a late
// state from a machine for the plugin that is gone is not taken.
func TestNodeProgramsForgotten(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	box, _ := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	reg := h.extensions.reg
	dir := helperPackage(t)
	pv, err := reg.Preview(ctx, "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.syncExtensionNodes(ctx, box)
	// Another plugin whose id starts with this one's keeps its package.
	other := filepath.Join(h.extensions.root, "node-packages", "example-helper-two-1.0.0-linux-amd64.tgz")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := nodePackageFiles(t, h, "example-helper"); !slices.Contains(got, "example-helper-1.0.0-linux-amd64.tgz") {
		t.Fatalf("the package was made: %v", got)
	}

	// Upgraded: only 2.0.0's package is left.
	old := reg.Snapshot("example-helper")
	manifest := filepath.Join(dir, extension.ManifestFile)
	raw, _ := os.ReadFile(manifest)
	_ = os.WriteFile(manifest, []byte(strings.Replace(string(raw), `"version": "1.0.0"`, `"version": "2.0.0"`, 1)), 0o644)
	pv, err = reg.Preview(ctx, "dir:"+dir, "example-helper")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Upgrade(ctx, "example-helper", pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	h.syncExtensionNodes(ctx, box)
	if got := nodePackageFiles(t, h, "example-helper"); !slices.Equal(got, []string{"example-helper-2.0.0-linux-amd64.tgz", "example-helper-two-1.0.0-linux-amd64.tgz"}) {
		t.Fatalf("after the upgrade: %v", got)
	}
	if got := nodePackageKeys(h, "example-helper"); !slices.Equal(got, []string{"example-helper/2.0.0/linux-amd64"}) {
		t.Fatalf("after the upgrade: %v", got)
	}
	// Nor does a sync that read the registry before the upgrade and goes on after it (each change starts one): no 1.0.0
	// package, and the machine's 2.0.0 state stays.
	h.syncExtensionNode(ctx, old, box, true)
	if got := nodePackageFiles(t, h, "example-helper"); !slices.Equal(got, []string{"example-helper-2.0.0-linux-amd64.tgz", "example-helper-two-1.0.0-linux-amd64.tgz"}) {
		t.Fatalf("after a sync of the old version: %v", got)
	}
	if got := nodePackageKeys(h, "example-helper"); !slices.Equal(got, []string{"example-helper/2.0.0/linux-amd64"}) {
		t.Fatalf("after a sync of the old version: %v", got)
	}
	if v := h.extensions.nodes.views("example-helper"); len(v) != 1 || v[0].Version != "2.0.0" {
		t.Fatalf("machine state after a sync of the old version: %+v", v)
	}

	// Uninstalled: nothing of it is left on the hub. The sync waits for the ones in flight (they share a lock per
	// machine), so a package one of them made and removed again is not caught in between.
	cur := reg.Snapshot("example-helper")
	if err := reg.Uninstall(ctx, "example-helper", false, "test"); err != nil {
		t.Fatal(err)
	}
	h.syncExtensionNodes(ctx, box)
	if v := h.extensions.nodes.views("example-helper"); len(v) != 0 {
		t.Fatalf("machine states kept: %+v", v)
	}
	if got := nodePackageKeys(h, "example-helper"); len(got) != 0 {
		t.Fatalf("packages kept: %v", got)
	}
	if got := nodePackageFiles(t, h, "example-helper"); !slices.Equal(got, []string{"example-helper-two-1.0.0-linux-amd64.tgz"}) {
		t.Fatalf("package files kept: %v", got)
	}
	// Nor does a sync that read the registry before the uninstall leave a state or a package of it.
	h.syncExtensionNode(ctx, cur, box, true)
	if v := h.extensions.nodes.views("example-helper"); len(v) != 0 {
		t.Fatalf("machine state after a sync of the uninstalled version: %+v", v)
	}
	if got := nodePackageFiles(t, h, "example-helper"); !slices.Equal(got, []string{"example-helper-two-1.0.0-linux-amd64.tgz"}) {
		t.Fatalf("package files after a sync of the uninstalled version: %v", got)
	}

	// A machine's late word of it is not taken.
	h.NodeEvent("box", "plugin.node.state", json.RawMessage(`{"id":"example-helper","version":"2.0.0","state":"stopped"}`))
	if v := h.extensions.nodes.views("example-helper"); len(v) != 0 {
		t.Fatalf("a state for a plugin that is gone: %+v", v)
	}
}

func TestNodePackageConcurrentSamePlatform(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	boxA, seenA := extensionNodeMachine(t, h, "box-a", "darwin", "arm64", false)
	boxB, seenB := extensionNodeMachine(t, h, "box-b", "darwin", "arm64", false)

	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box-a", "box-b"]}`), "test"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for _, m := range []*Machine{boxA, boxB} {
		wg.Add(1)
		go func(m *Machine) {
			defer wg.Done()
			h.syncExtensionNodes(ctx, m)
		}(m)
	}
	wg.Wait()

	ext := reg.Get("example-helper")
	pkgA, err := h.extensions.nodePackage(ext, "darwin-arm64")
	if err != nil {
		t.Fatal(err)
	}
	if pkgA.size <= 0 || pkgA.digest == "" {
		t.Fatalf("invalid package: %+v", pkgA)
	}
	for _, v := range h.extensions.view(ctx, ext).Nodes {
		if v.State != "running" || v.Error != "" {
			t.Fatalf("concurrent same-platform sync failed for %s: %+v", v.MachineID, v)
		}
	}
	if len(seenA()) == 0 || len(seenB()) == 0 {
		t.Fatalf("expected calls to both machines: a=%d b=%d", len(seenA()), len(seenB()))
	}
}

// Every caller of nodePackage gets a file whose bytes match the digest and size it was handed, even while other
// callers for the same platform are building the same package (#15444).
func TestNodePackageFileMatchesWhileOthersBuild(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	dir := helperPackage(t)
	payload := make([]byte, 4<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node", "helper-mac"), payload, 0o755); err != nil {
		t.Fatal(err)
	}

	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	ext := reg.Get("example-helper")

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			time.Sleep(time.Duration(i) * 3 * time.Millisecond) // sleep-ok: stagger callers so reads overlap competing builds
			pkg, err := h.extensions.nodePackage(ext, "darwin-arm64")
			if err != nil {
				t.Errorf("caller %d: build node package: %v", i, err)
				return
			}
			raw, err := os.ReadFile(pkg.file)
			if err != nil {
				t.Errorf("caller %d: read node package: %v", i, err)
				return
			}
			if got := int64(len(raw)); got != pkg.size {
				t.Errorf("caller %d: package size = %d, want %d", i, got, pkg.size)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(raw)); got != pkg.digest {
				t.Errorf("caller %d: package digest = %s, want %s", i, got, pkg.digest)
			}
		})
	}
	wg.Wait()
}

func TestNodePackageBuildLocksReleasedAfterDrop(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	ps := h.extensions
	pv, err := ps.reg.Preview(ctx, "dir:"+helperPackage(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ps.reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	ext := ps.reg.Get("example-helper")
	pn := &ps.nodes
	key := ext.Record.ID + "/" + ext.Record.Version + "/darwin-arm64"

	// Keep a build lock held across drop to check that another builder cannot get a different lock for the key.
	unlockBuild := pn.lockPkgBuild(key)
	pn.mu.Lock()
	entry := pn.pkgLocks[key]
	pn.mu.Unlock()
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := ps.nodePackage(ext, "darwin-arm64"); err != nil {
				t.Errorf("build node package: %v", err)
			}
		})
	}
	pn.drop(ps.root, ext.Record.ID, "")
	pn.mu.Lock()
	retained := entry != nil && pn.pkgLocks[key] == entry
	pn.mu.Unlock()
	unlockBuild()
	wg.Wait()
	if !retained {
		t.Error("drop removed an active package build lock")
	}

	pn.mu.Lock()
	defer pn.mu.Unlock()
	if got := len(pn.pkgLocks); got != 0 {
		t.Fatalf("retained %d package build locks, want 0", got)
	}
}
