package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

func TestAgyBridgeCommandKeepsExternalRuntimeStore(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	pkgRoot := filepath.Join(dir, "pkgs", agyPackage)
	for rel, body := range map[string]string{
		"antigravity/package.json":    `{"type":"module"}`,
		"antigravity/companion.mjs":   "console.log('ok')\n",
		"antigravity/desktop.mjs":     "console.log('ok')\n",
		"antigravity/mcp-proxy.mjs":   "export {}\n",
		"antigravity/media-guard.mjs": "export {}\n",
		"antigravity/lib/runtime.js":  "export {}\n",
		"runtime-sdk/package.json":    `{"type":"module"}`,
		"runtime-sdk/index.js":        "export {}\n",
		"runtime-sdk/lib/runtime.js":  "export {}\n",
	} {
		p := filepath.Join(pkgRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeNode := filepath.Join(dir, "node-22")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.19.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, filepath.Join(dir, "pkgs"))
	t.Setenv("PERSONA_NODEJS", fakeNode)

	externalStore := filepath.Join(dir, "state", "runtime-packages", agyPackage)
	externalVersion := strings.Repeat("a", 64)
	externalProxy := filepath.Join(externalStore, externalVersion, "antigravity", "mcp-proxy.mjs")
	if err := os.MkdirAll(filepath.Dir(externalProxy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(externalProxy, []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalVersion, filepath.Join(externalStore, "current")); err != nil {
		t.Fatal(err)
	}

	builtinStore := filepath.Join(dir, "state", "runtime-packages", agyPackage+"-builtin")
	busyVersion := strings.Repeat("b", 64)
	busyProxy := filepath.Join(builtinStore, busyVersion, "antigravity", "mcp-proxy.mjs")
	idleProxy := filepath.Join(builtinStore, strings.Repeat("c", 64), "antigravity", "mcp-proxy.mjs")
	for _, path := range []string{busyProxy, idleProxy} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("builtin\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lease := filepath.Join(builtinStore, ".active", busyVersion)
	if err := os.MkdirAll(lease, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lease, fmt.Sprintf("%d-2222222222222222", os.Getpid())), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	idleLease := filepath.Join(builtinStore, ".active", strings.Repeat("c", 64))
	if err := os.MkdirAll(idleLease, 0o700); err != nil {
		t.Fatal(err)
	}
	// This is an old node startup reservation, not a companion lease. It must not survive a same-PID exec upgrade.
	if err := os.WriteFile(filepath.Join(idleLease, fmt.Sprintf("%d-0000000000000000", os.Getpid())), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sock := filepath.Join(dir, "state", "runtime.sock")
	rh := NewRuntimeHost("test", sock)
	lock := filepath.Join(builtinStore, ".install.lock")
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	owner := filepath.Join(lock, fmt.Sprintf("%d-1111111111111111", os.Getpid()))
	if err := os.WriteFile(owner, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agyBridgeCommand(context.Background(), rh, "/usr/local/bin/agy", "builtin-agy-1-1"); err == nil {
		t.Fatal("built-in cleanup proceeded while the runtime-sdk store lock was live")
	}
	if _, err := os.Stat(idleProxy); err != nil {
		t.Fatalf("a blocked cleanup still removed an idle generation: %v", err)
	}
	if err := os.WriteFile(owner, []byte("not-this-process-start"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The same live PID with a different recorded start identity is a reused PID, so Go reclaims the stale lock.
	cmd, err := agyBridgeCommand(context.Background(), rh, "/usr/local/bin/agy", "builtin-agy-1-1")
	if err != nil {
		t.Fatalf("agyBridgeCommand: %v", err)
	}
	wantPrefix := builtinStore + string(filepath.Separator)
	if !strings.HasPrefix(cmd.Dir, wantPrefix) {
		t.Fatalf("cmd.Dir = %q, want isolated builtin prefix %q", cmd.Dir, wantPrefix)
	}
	reservation := filepath.Join(builtinStore, ".active", filepath.Base(cmd.Dir), fmt.Sprintf("%d-0000000000000000", os.Getpid()))
	if _, err := os.Stat(reservation); err != nil {
		t.Fatalf("the launched generation has no node-owned lease before companion registration: %v", err)
	}
	if got, err := os.ReadFile(externalProxy); err != nil || string(got) != "external\n" {
		t.Fatalf("external running version changed: data %q, error %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(externalStore, "current", "antigravity", "mcp-proxy.mjs")); err != nil {
		t.Fatalf("external current is no longer usable: %v", err)
	}
	if _, err := os.Stat(busyProxy); err != nil {
		t.Fatalf("built-in cleanup removed a generation with a live lease: %v", err)
	}
	if _, err := os.Stat(idleProxy); !os.IsNotExist(err) {
		t.Fatalf("idle built-in generation was not reclaimed: %v", err)
	}
	for _, flag := range []string{"--socket", sock, "--agy-bin", "/usr/local/bin/agy", "--no-update", "--parent-stdin", "--instance", "builtin-agy-1-1"} {
		if !slices.Contains(cmd.Args, flag) {
			t.Fatalf("cmd.Args %v missing %q", cmd.Args, flag)
		}
	}
}

func TestAgyBridgeWakesOnExternalDisconnect(t *testing.T) {
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() {
		findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe
	})
	agyBridgeProbeEvery = 30 * time.Second
	findAgyExecutable = func() string { return "agy" }
	synctest.Test(t, func(t *testing.T) {
		start := make(chan struct{}, 1)
		agyBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
			start <- struct{}{}
			return nil, errors.New("test companion")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		client, hello := startAgyHello(t, ctx, host, "external")
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		host.setAgyBridge(true)
		defer host.setAgyBridge(false)
		synctest.Wait()
		select {
		case <-start:
			t.Fatal("started while external agy was connected")
		default:
		}
		_ = client.Close()
		select {
		case <-start:
		case <-time.After(100 * time.Millisecond):
			t.Fatal("builtin agy did not resume within 100ms of external disconnect")
		}
	})
}

func startAgyHello(t *testing.T, ctx context.Context, host *RuntimeHost, instance string) (net.Conn, <-chan error) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go host.serve(ctx, server)
	peer := rpc.New("test", client)
	go peer.Serve(ctx, client)
	hello := make(chan error, 1)
	go func() {
		hello <- peer.Call(ctx, "runtime.hello", map[string]any{"runtime": "agy", "instance": instance}, nil)
	}()
	return client, hello
}

func TestAgyBridgeSupervisesAndYieldsToExternalRuntime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	oldFind, oldCmd, oldMin, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeRestartMin, agyBridgeProbeEvery
	t.Cleanup(func() {
		findAgyExecutable, agyBridgeCommand = oldFind, oldCmd
		agyBridgeRestartMin, agyBridgeProbeEvery = oldMin, oldProbe
	})
	agyBridgeRestartMin, agyBridgeProbeEvery = 10*time.Millisecond, 10*time.Millisecond
	var probes atomic.Int32
	findAgyExecutable = func() string {
		probes.Add(1)
		return "/opt/agent/bin/agy"
	}
	agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
		script := fmt.Sprintf("echo %s >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", agyBin, marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["runtime"], hello["agent"], hello["launcher"] = "agy", "agy", "agy"
	ext := dialRuntime(t, n, hello)

	// While an external agy runtime is connected, the built-in supervisor yields.
	n.runtimeHost.setAgyBridge(true)
	defer n.runtimeHost.setAgyBridge(false)
	waitFor(t, "probed while external agy runtime connected", func() bool { return probes.Load() >= 2 })
	if len(starts(marks)) != 0 {
		t.Fatalf("started while external agy runtime was connected: %v", starts(marks))
	}

	// Once the external runtime leaves, the built-in supervisor starts companion.mjs and stops cleanly.
	_ = ext.conn.Close()
	waitFor(t, "built-in agy companion started", func() bool { return len(starts(marks)) == 1 })
	if got := starts(marks); got[0] != "/opt/agent/bin/agy" {
		t.Fatalf("unexpected agy binary: %v", got)
	}
	n.runtimeHost.setAgyBridge(false)
	if got := starts(marks); len(got) != 2 || got[1] != "stopped" {
		t.Fatalf("companion was not stopped cleanly: %v", got)
	}
}

func TestAgyBridgeYieldsWhenExternalRuntimeConnectsAfterStart(t *testing.T) {
	t.Run("settled", func(t *testing.T) { testAgyBridgeHandoff(t, false) })
	t.Run("deadline", func(t *testing.T) { testAgyBridgeHandoff(t, true) })
}

func testAgyBridgeHandoff(t *testing.T, expires bool) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	oldFind, oldCmd, oldMin, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeRestartMin, agyBridgeProbeEvery
	t.Cleanup(func() {
		findAgyExecutable, agyBridgeCommand = oldFind, oldCmd
		agyBridgeRestartMin, agyBridgeProbeEvery = oldMin, oldProbe
	})
	agyBridgeRestartMin, agyBridgeProbeEvery = 10*time.Millisecond, 10*time.Millisecond
	var probes atomic.Int32
	findAgyExecutable = func() string {
		probes.Add(1)
		return "/opt/agent/bin/agy"
	}
	instances := make(chan string, 4)
	agyBridgeCommand = func(ctx context.Context, rh *RuntimeHost, agyBin, instance string) (*exec.Cmd, error) {
		instances <- instance
		script := fmt.Sprintf("echo %s >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", agyBin, marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	if expires {
		n.runtimeHost.agy.yieldGrace = 100 * time.Millisecond
	}

	// Start the built-in companion first and register its socket connection as the supervised instance.
	n.runtimeHost.setAgyBridge(true)
	defer n.runtimeHost.setAgyBridge(false)
	builtinInstance := <-instances
	waitFor(t, "built-in agy companion started", func() bool { return len(starts(marks)) == 1 })

	builtinHello := assistantRuntimeHello()
	builtinHello["runtime"], builtinHello["agent"], builtinHello["launcher"], builtinHello["instance"] = "agy", "agy", "agy", builtinInstance
	builtin := dialRuntime(t, n, builtinHello)
	n.runtimeHost.reg.mu.Lock()
	handle := n.runtimeHost.reg.byRuntime(agyRuntimeID)
	n.runtimeHost.reg.mu.Unlock()
	const session = "active"
	setStatus := func(status string) {
		t.Helper()
		if err := builtin.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{{SessionID: session, Status: status}}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "session status "+status, func() bool {
			handle.mu.Lock()
			defer handle.mu.Unlock()
			return len(handle.sessions) == 1 && handle.sessions[0].Status == status
		})
	}
	setStatus(proto.StatusWorking)
	callCtx, cancelCall := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelCall()
	var reply chan struct{}
	var callDone <-chan error
	if !expires {
		reply, callDone = holdAgyCall(t, callCtx, builtin, handle)
	}

	started := time.Now()
	external, hello := startAgyHello(t, callCtx, n.runtimeHost, "external")
	if !expires {
		waitWithin(t, time.Second, "builtin agy stopped admitting new calls", func() bool {
			err := handle.peer.Call(callCtx, "test.probe", nil, nil)
			if errors.Is(err, rpc.ErrSuspended) {
				return true
			}
			if err != nil {
				t.Fatalf("builtin agy disconnected during a pending call: %v", err)
			}
			return false
		})
		close(reply)
		if err := <-callDone; err != nil {
			t.Fatalf("pending builtin call was interrupted: %v", err)
		}
		setStatus(proto.StatusIdle)
	}
	if err := <-hello; err != nil {
		t.Fatalf("external runtime registration: %v", err)
	}
	if expires && time.Since(started) < n.runtimeHost.agy.yieldGrace {
		t.Fatal("external runtime registered before the grace window elapsed")
	}

	waitWithin(t, time.Second, "built-in agy companion yielded and stopped", func() bool {
		got := starts(marks)
		return len(got) == 2 && got[1] == "stopped"
	})
	probesBefore := probes.Load()
	waitFor(t, "probed while external agy runtime stayed connected", func() bool { return probes.Load() >= probesBefore+2 })
	if got := starts(marks); len(got) != 2 {
		t.Fatalf("built-in companion restarted while external runtime was connected: %v", got)
	}
	n.runtimeHost.reg.mu.Lock()
	active := n.runtimeHost.reg.byRuntime(agyRuntimeID)
	var activeInst string
	if active != nil {
		active.mu.Lock()
		activeInst = active.hello.Instance
		active.mu.Unlock()
	}
	n.runtimeHost.reg.mu.Unlock()
	if activeInst != "external" {
		t.Fatalf("active agy instance = %q, want external", activeInst)
	}

	// Once the external runtime disconnects, the built-in supervisor resumes.
	_ = external.Close()
	waitFor(t, "built-in agy companion resumed after external disconnect", func() bool { return len(starts(marks)) == 3 })
	n.runtimeHost.setAgyBridge(false)
	if got := starts(marks); len(got) != 4 || got[3] != "stopped" {
		t.Fatalf("resumed companion was not stopped cleanly: %v", got)
	}
}

func holdAgyCall(t *testing.T, ctx context.Context, builtin *fakeRuntime, handle *runtimeConn) (chan struct{}, <-chan error) {
	t.Helper()
	started, reply := make(chan struct{}), make(chan struct{})
	builtin.mu.Lock()
	builtin.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "test.hold" {
			close(started)
			select {
			case <-reply:
			case <-builtin.peer.Done():
			}
		}
		return nil, nil
	}
	builtin.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- handle.peer.Call(ctx, "test.hold", nil, nil) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("builtin did not receive the pending call")
	}
	return reply, done
}

func TestAgyBridgeCancelsHandoffWhenExternalDisconnects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		host := NewRuntimeHost("test", "runtime.sock")
		host.agy.instance = "builtin"
		builtin, hello := startAgyHello(t, ctx, host, "builtin")
		defer builtin.Close()
		if err := <-hello; err != nil {
			t.Fatal(err)
		}
		handle, _ := host.agyPeers("builtin")
		host.reg.setSessions(handle, []proto.Session{{SessionID: "active", Status: proto.StatusWorking}})
		external, hello := startAgyHello(t, ctx, host, "external")
		synctest.Wait()
		select {
		case err := <-hello:
			t.Fatalf("external runtime did not wait for the working session: %v", err)
		default:
		}
		if err := handle.peer.Call(ctx, "test.probe", nil, nil); !errors.Is(err, rpc.ErrSuspended) {
			t.Fatalf("builtin RPC during drain: %v, want suspended", err)
		}
		_ = external.Close()
		synctest.Wait()
		if err := handle.peer.Call(ctx, "test.probe", nil, nil); err == nil || err.Error() != "no handler for test.probe" {
			t.Fatalf("builtin RPC after canceled drain: %v", err)
		}
	})
}

func TestAgyBridgeJSRuntimeRequiresSQLite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	writeFakeNode := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	noSQLite := writeFakeNode("node-20", "exit 1")
	t.Setenv("PERSONA_NODEJS", noSQLite)
	t.Setenv("PATH", dir)
	t.Setenv("HOME", dir)
	if js, err := findAgyJSRuntime(context.Background()); err == nil && js.Path == noSQLite {
		t.Fatalf("accepted Node without node:sqlite: %+v", js)
	}

	withSQLite := writeFakeNode("node-22", "printf 22.14.0")
	t.Setenv("PERSONA_NODEJS", withSQLite)
	js, err := findAgyJSRuntime(context.Background())
	if err != nil || js.Path != withSQLite {
		t.Fatalf("findAgyJSRuntime = %+v, %v; want %s", js, err, withSQLite)
	}
}

func TestAgyChannelForResolvesIsolatedPoolsOnDemand(t *testing.T) {
	n := &Node{bin: "/usr/local/bin/personad"}
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "claude-gw", APIKey: "claude-key"},
		{Name: "gw-b", Agent: "agy", BaseURL: "https://gw-b.example.com", APIKey: "key-b", Rev: "rev-b1"},
		{Name: "gw-a", Agent: "agy", BaseURL: "https://gw-a.example.com", APIKey: "key-a", Rev: "rev-a1"},
		{Name: "agy-signin", Agent: "agy"},
	})

	// Native agy and a keyless managed agy launcher both route to the native OAuth pool (nil channel spec).
	if ch, err := n.agyChannelFor(t.Context(), "agy"); err != nil || ch != nil {
		t.Fatalf("native agy channel = %+v, %v; want nil", ch, err)
	}
	if ch, err := n.agyChannelFor(t.Context(), "agy-signin"); err != nil || ch != nil {
		t.Fatalf("keyless agy channel = %+v, %v; want nil", ch, err)
	}

	// Each managed agy launcher resolves its own isolated pool key and credentials without overwriting others.
	chA, err := n.agyChannelFor(t.Context(), "gw-a")
	if err != nil || chA == nil {
		t.Fatalf("gw-a channel = %+v, %v", chA, err)
	}
	chB, err := n.agyChannelFor(t.Context(), "gw-b")
	if err != nil || chB == nil {
		t.Fatalf("gw-b channel = %+v, %v", chB, err)
	}
	if chA.ID != "gw-a" || chA.AuthMode != "gemini-api-key" || chA.Rev != "rev-a1" ||
		chA.Env["GOOGLE_GEMINI_BASE_URL"] != "https://gw-a.example.com" ||
		chA.Env["GEMINI_API_KEY"] != "key-a" || chA.Env["PERSONA_AGY_MODEL_PROVIDER"] != "gemini" {
		t.Fatalf("unexpected gw-a spec: %+v", chA)
	}
	if chB.ID != "gw-b" || chB.AuthMode != "gemini-api-key" || chB.Rev != "rev-b1" ||
		chB.Env["GOOGLE_GEMINI_BASE_URL"] != "https://gw-b.example.com" ||
		chB.Env["GEMINI_API_KEY"] != "key-b" || chB.Env["PERSONA_AGY_MODEL_PROVIDER"] != "gemini" {
		t.Fatalf("unexpected gw-b spec: %+v", chB)
	}

	// Rotating the key without an explicit rev still yields a distinct deterministic rev hash.
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "gw-a", Agent: "agy", BaseURL: "https://gw-a.example.com", APIKey: "key-a-v2"},
	})
	chA2, err := n.agyChannelFor(t.Context(), "gw-a")
	if err != nil || chA2 == nil || chA2.Rev == "" || chA2.Rev == chA.Rev {
		t.Fatalf("rotated gw-a spec = %+v, %v; want new rev != %q", chA2, err, chA.Rev)
	}

	// Companion control-plane env never carries channel secrets or PERSONA_AGY_MODEL_PROVIDER.
	rh := NewRuntimeHost("test", filepath.Join(t.TempDir(), "runtime.sock"))
	bridgeEnv := agyBridgeEnv(t.Context(), rh, []string{"PATH=/usr/bin"})
	for _, kv := range bridgeEnv {
		if strings.HasPrefix(kv, "GEMINI_API_KEY=") || strings.HasPrefix(kv, "GOOGLE_GEMINI_BASE_URL=") || strings.HasPrefix(kv, "PERSONA_AGY_MODEL_PROVIDER=") {
			t.Fatalf("companion control-plane env leaked channel setting: %s", kv)
		}
	}
}

// Redline regression test: catalog updates (adding a managed agy launcher, expanding scope, rotating key/BaseURL)
// must never stop or restart a running Antigravity companion or interrupt active sessions.
func TestAgyCatalogChangesNeverRestartRunningCompanion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "starts")
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	var probes atomic.Int32
	findAgyExecutable = func() string {
		probes.Add(1)
		return filepath.Join(dir, "agy")
	}
	writeAgy(t, filepath.Join(dir, "agy"), "v1")
	agyBridgeCommand = func(_ context.Context, rh *RuntimeHost, _, _ string) (*exec.Cmd, error) {
		for _, kv := range agyBridgeEnv(t.Context(), rh, []string{"PATH=/usr/bin"}) {
			if strings.HasPrefix(kv, "GEMINI_API_KEY=") || strings.HasPrefix(kv, "PERSONA_AGY_MODEL_PROVIDER=") {
				t.Errorf("companion started with channel secret in control-plane env: %s", kv)
			}
		}
		script := fmt.Sprintf("echo start >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}

	n, hub := runtimeNode(t)
	assistantHub(hub)
	n.runtimeHost.setAgyBridge(true)
	defer n.runtimeHost.setAgyBridge(false)
	waitFor(t, "companion started once", func() bool { return slices.Equal(starts(marks), []string{"start"}) })

	// Push managed agy launchers, expand scope, rotate key/BaseURL/rev, and remove them.
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "gw-one", Agent: "agy", BaseURL: "https://gw1.example.com", APIKey: "secret-1", Rev: "r1"},
	})
	n.setCatalog([]proto.ManagedLauncher{
		{Name: "gw-one", Agent: "agy", BaseURL: "https://gw1.example.com", APIKey: "secret-2", Rev: "r2"},
		{Name: "gw-two", Agent: "agy", BaseURL: "https://gw2.example.com", APIKey: "secret-3", Rev: "r1"},
	})
	n.setCatalog(nil)

	probesAfter := probes.Load()
	waitFor(t, "probes continued after catalog changes", func() bool { return probes.Load() >= probesAfter+5 })
	if got := starts(marks); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("catalog/scope/key changes restarted the running companion: %v", got)
	}
}

// A Persona-managed agy launcher with a key reaches a herdr pane the way a managed Claude launcher does: the pane
// types `personad launch <name>` (the name, never the key), and launch gets the launcher's environment from the node
// and starts agy with the Gemini provider setting (#13762).
func TestManagedAgyLauncherPaneStartAndResumeCarryNoSecret(t *testing.T) {
	const key = "pane-secret-key"
	l := proto.ManagedLauncher{Name: "agy-gw", Agent: "agy", BaseURL: "https://gateway.example.com", APIKey: key}
	n := &Node{bin: "/usr/local/bin/personad"}
	n.setCatalog([]proto.ManagedLauncher{l})

	env := managedEnv(l)
	if !slices.Equal(env, []string{"GOOGLE_GEMINI_BASE_URL=https://gateway.example.com", "GEMINI_API_KEY=" + key}) {
		t.Fatalf("launcher env = %v", env)
	}

	start := workAgentCommand(n.launcherCommand("agy-gw"), "agy", "gemini-4-neon-high", "high", proto.ModeBypass)
	if want := "/usr/local/bin/personad launch agy-gw --model gemini-4-neon-high --effort high --dangerously-skip-permissions"; start != want {
		t.Fatalf("start command = %q, want %q", start, want)
	}
	spec := agentLaunch{Agent: agyAgent, Launcher: "agy-gw", Model: "gemini-4-neon-high", Effort: "high", Mode: proto.ModeBypass, AgyConversation: "11111111-2222-3333-4444-555555555555"}
	argv, ok := n.agentResumeArgv(spec, "11111111-2222-3333-4444-555555555555")
	if !ok || !strings.Contains(argv[1], "launch agy-gw --model") || !strings.Contains(argv[1], "--conversation 11111111-2222-3333-4444-555555555555") {
		t.Fatalf("resume argv = %q, %v", argv, ok)
	}
	for _, typed := range []string{start, strings.Join(argv, " ")} {
		if strings.Contains(typed, key) || strings.Contains(typed, "gateway.example.com") {
			t.Fatalf("the pane's typed command carries the provider's secret or address: %q", typed)
		}
	}

	// What personad launch then execs: agy with the provider setting before the pane's own flags, the key in the env.
	got := launchArgv(agyAgent, []string{"--model", "gemini-4-neon-high", "--dangerously-skip-permissions"}, nil, "", "", env, "")
	want := []string{"agy", "--settings", `{"modelProvider":"gemini"}`, "--model", "gemini-4-neon-high", "--dangerously-skip-permissions"}
	if !slices.Equal(got, want) {
		t.Fatalf("launch argv = %q, want %q", got, want)
	}
	for _, a := range got {
		if strings.Contains(a, key) {
			t.Fatalf("argv carries the key: %q", got)
		}
	}
}

func TestCustomAntigravityRuntimeCoexistsWithBuiltinAgy(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)

	builtinHello := assistantRuntimeHello()
	builtinHello["runtime"], builtinHello["agent"], builtinHello["launcher"], builtinHello["instance"] = "agy", "agy", "agy", "builtin-1"
	_ = dialRuntime(t, n, builtinHello)

	customHello := assistantRuntimeHello()
	customHello["runtime"], customHello["agent"], customHello["launcher"], customHello["instance"] = "agy-wrap", "agy", "agy-wrap", "custom-1"
	customHello["name"] = "Antigravity Wrapped"
	customHello["launchers"] = []map[string]any{{"name": "agy-wrap", "agent": "agy", "display_name": "Antigravity Wrapped"}}
	_ = dialRuntime(t, n, customHello)

	if _, external := n.runtimeHost.agyPeers("builtin-1"); external {
		t.Fatal("custom runtime id agy-wrap must not be treated as an external agy peer that makes built-in yield")
	}
	if h := n.assistantRuntime("agy"); h == nil || h.hello.Runtime != "agy" {
		t.Fatalf("assistantRuntime(agy) = %v, want agy runtime", h)
	}
	if h := n.assistantRuntime("agy-wrap"); h == nil || h.hello.Runtime != "agy-wrap" {
		t.Fatalf("assistantRuntime(agy-wrap) = %v, want agy-wrap runtime", h)
	}
}

// writeAgy puts a new file at path the way an installer replaces one: written beside it, then renamed over it.
func writeAgy(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// The companion keeps the agy it started on until the node notices another (#14533): one replaced in place (an update)
// or installed elsewhere with the old one removed restarts it on the agy found now; an unchanged one never does.
func TestAgyBridgeRestartsOnANewAgyBinary(t *testing.T) {
	dir := t.TempDir()
	first, moved := filepath.Join(dir, "a", "agy"), filepath.Join(dir, "b", "agy")
	writeAgy(t, first, "v1")
	var where atomic.Value
	where.Store(first)
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	findAgyExecutable = func() string { return where.Load().(string) }
	started := make(chan string, 8)
	agyBridgeCommand = func(_ context.Context, _ *RuntimeHost, agyBin, _ string) (*exec.Cmd, error) {
		started <- agyBin
		return exec.Command("sleep", "30"), nil
	}
	host := NewRuntimeHost("test", filepath.Join(t.TempDir(), "runtime.sock"))
	logs := &syncBuffer{}
	host.logger = log.New(logs, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	next := func(what string) string {
		t.Helper()
		select {
		case bin := <-started:
			return bin
		case <-time.After(10 * time.Second):
			t.Fatalf("%s: the companion was not (re)started; log:\n%s", what, logs.String())
			return ""
		}
	}
	if got := next("first start"); got != first {
		t.Fatalf("first start on %q, want %q", got, first)
	}
	select {
	case bin := <-started:
		t.Fatalf("restarted on an unchanged agy (%s); log:\n%s", bin, logs.String())
	case <-time.After(150 * time.Millisecond): // fifteen probes
	}

	writeAgy(t, first, "v2")
	if got := next("replaced in place"); got != first {
		t.Fatalf("restart after an update on %q, want %q", got, first)
	}
	if !strings.Contains(logs.String(), "agy at "+first+" was replaced; restarting") {
		t.Fatalf("the restart does not say why:\n%s", logs.String())
	}

	writeAgy(t, moved, "v3")
	where.Store(moved)
	if err := os.Remove(first); err != nil {
		t.Fatal(err)
	}
	if got := next("installed elsewhere"); got != moved {
		t.Fatalf("restart after a move on %q, want %q", got, moved)
	}
	if !strings.Contains(logs.String(), "agy moved from "+first+" to "+moved) {
		t.Fatalf("the restart does not say where agy went:\n%s", logs.String())
	}
}

// A new agy does not cut the companion's work: while one of its sessions is working the restart waits (the old file
// keeps running it), and it happens once the session is idle.
func TestAgyBridgeRestartWaitsForAWorkingSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	bin, marks := filepath.Join(dir, "agy"), filepath.Join(dir, "marks")
	writeAgy(t, bin, "v1")
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	findAgyExecutable = func() string { return bin }
	agyBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
		script := fmt.Sprintf("echo start >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	logs := &syncBuffer{}
	host.logger = log.New(logs, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	waitFor(t, "the companion started", func() bool { return slices.Equal(starts(marks), []string{"start"}) })

	// the companion's own connection, with one session at work
	instance := host.builtinAgyInstance()
	client, hello := startAgyHello(t, ctx, host, instance)
	if err := <-hello; err != nil {
		t.Fatal(err)
	}
	var builtin *runtimeConn
	waitFor(t, "the companion registered", func() bool { builtin, _ = host.agyPeers(instance); return builtin != nil })
	status := func(s string) {
		builtin.mu.Lock()
		builtin.sessions = []proto.Session{{SessionID: "s1", Status: s}}
		builtin.mu.Unlock()
	}
	status(proto.StatusWorking)

	writeAgy(t, bin, "v2")
	waitFor(t, "the restart to wait for the session", func() bool {
		return strings.Contains(logs.String(), "agy at "+bin+" was replaced; restarting once no agy session is working") &&
			host.agyDraining(instance)
	})
	if got := starts(marks); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the companion was stopped while its session was working: %v", got)
	}

	status(proto.StatusIdle)
	waitFor(t, "the restart once the session is idle", func() bool { return slices.Contains(starts(marks), "stopped") })
	if host.agyDraining(instance) {
		t.Fatal("draining flag was not cleared after switching generations")
	}
	_ = client.Close() // the old companion's connection goes with it
	waitFor(t, "the companion started again", func() bool { m := starts(marks); return len(m) >= 3 && m[2] == "start" })
}

// A session whose turn ended keeps the companion working while finite background work it started still runs (#17200):
// a command, workflow or subagent, as a Background entry or a running Subagents / Workflows entry. A standing schedule
// (kind monitor) never ends and does not.
func TestAgyWorkingCountsFiniteBackgroundWork(t *testing.T) {
	idle := func(edit func(*proto.Session)) proto.Session {
		s := proto.Session{SessionID: "s1", Status: proto.StatusIdle}
		if edit != nil {
			edit(&s)
		}
		return s
	}
	task := func(kind string) proto.BackgroundTask {
		t := proto.BackgroundTask{ID: "s1/task-1", Kind: kind, StartedAt: time.Now()}
		if kind == "shell" {
			t.Command = "make test" // a matched run_command carries its command line
		}
		return t
	}
	for _, tc := range []struct {
		name     string
		sessions []proto.Session
		want     bool
	}{
		{"no sessions", nil, false},
		{"idle", []proto.Session{idle(nil)}, false},
		{"working", []proto.Session{{SessionID: "s1", Status: proto.StatusWorking}}, true},
		{"background command", []proto.Session{idle(func(s *proto.Session) { s.Background = []proto.BackgroundTask{task("shell")} })}, true},
		{"background workflow", []proto.Session{idle(func(s *proto.Session) { s.Background = []proto.BackgroundTask{task("workflow")} })}, true},
		{"background subagent", []proto.Session{idle(func(s *proto.Session) { s.Background = []proto.BackgroundTask{task("agent")} })}, true},
		{"only monitor schedules", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{task("monitor"), {ID: "s1/task-2", Kind: "monitor"}}
		})}, false},
		{"cron schedule reported as a shell", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{{ID: "s1/task-1", Kind: "shell", Description: "Cron: */5 * * * * Poll"}}
		})}, false},
		{"command described as cron", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{{ID: "s1/task-1", Kind: "shell", Description: "Cron: rotate logs", Command: "./rotate.sh"}}
		})}, true},
		{"unmatched timer reported as a shell", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{{ID: "s1/task-1", Kind: "shell", Description: "Timer: 60s"}}
		})}, false},
		{"unmatched task reported as a shell", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{{ID: "s1/task-1", Kind: "shell", Description: "Check status"}}
		})}, false},
		{"one-shot timer (monitor)", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{{ID: "s1/task-1", Kind: "monitor", Description: "Timer: 60s"}}
		})}, false},
		{"monitor beside a command", []proto.Session{idle(func(s *proto.Session) {
			s.Background = []proto.BackgroundTask{task("monitor"), {ID: "s1/task-2", Kind: "shell", Command: "./serve"}}
		})}, true},
		{"running subagent", []proto.Session{idle(func(s *proto.Session) { s.Subagents = []proto.Subagent{{ID: "a1", Running: true}} })}, true},
		{"finished subagent", []proto.Session{idle(func(s *proto.Session) { s.Subagents = []proto.Subagent{{ID: "a1"}} })}, false},
		{"running workflow", []proto.Session{idle(func(s *proto.Session) {
			s.Workflows = []proto.Workflow{{TaskID: "w1", Status: proto.WorkflowRunning, Running: true}}
		})}, true},
		{"finished workflow", []proto.Session{idle(func(s *proto.Session) {
			s.Workflows = []proto.Workflow{{TaskID: "w1", Status: proto.WorkflowCompleted}}
		})}, false},
		{"another session's command", []proto.Session{idle(nil), {SessionID: "s2", Status: proto.StatusDone,
			Background: []proto.BackgroundTask{{ID: "s2/task-1", Kind: "shell", Command: "make test"}}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &runtimeConn{sessions: tc.sessions}
			if got := h.agyWorking(); got != tc.want {
				t.Fatalf("agyWorking() = %v, want %v", got, tc.want)
			}
		})
	}
}

// agyWorking reads the companion's runtime.sessions as the node keeps them: the background rows the companion sends
// (integrations/antigravity/lib/background.js backgroundRow) keep their kinds through normalizeRuntimeSessions, so a
// command holds the drain and a schedule alone does not; a stopped session's tasks are dropped (#17200).
func TestAgyWorkingReadsTheCompanionsList(t *testing.T) {
	reg := newRuntimeRegistry()
	h := reg.add(nil)
	h.ready, h.hello.Runtime = true, agyRuntimeID
	id := agyAgent + "-" + agyTestID
	pushTasks := func(status string, tasks ...string) bool {
		t.Helper()
		raw := fmt.Sprintf(`{"sessions":[{"session_id":%q,"status":%q,"background":[%s]}]}`, id, status, strings.Join(tasks, ","))
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatal(err)
		}
		reg.setSessions(h, normalizeRuntimeSessions(h, p.Sessions))
		return h.agyWorking()
	}
	push := func(status string, kinds ...string) bool {
		t.Helper()
		var tasks []string
		for i, kind := range kinds {
			command := ""
			if kind == "shell" {
				command = `,"command":"make test"` // a matched run_command carries its command line
			}
			tasks = append(tasks, fmt.Sprintf(`{"id":"%s/task-%d","kind":%q%s,"started_at":"2026-11-28T09:00:00Z"}`, agyTestID, i+1, kind, command))
		}
		return pushTasks(status, tasks...)
	}
	if !push(proto.StatusIdle, "monitor", "shell") {
		t.Error("an idle session with a background command does not hold the drain")
	}
	if !push(proto.StatusIdle, "workflow") || !push(proto.StatusIdle, "agent") {
		t.Error("an idle session with a background workflow or subagent does not hold the drain")
	}
	if push(proto.StatusIdle, "monitor") {
		t.Error("a standing schedule alone holds the drain")
	}
	if push(proto.StatusOffline, "shell") {
		t.Error("a stopped session's tasks hold the drain")
	}
	if push(proto.StatusIdle) {
		t.Error("an idle session with nothing running holds the drain")
	}
	// A cron schedule whose start notice the companion could not match to its call arrives as a shell (background.js
	// extractBackground's default); its description still says it is one.
	unmatchedCron := `{"id":"` + agyTestID + `/task-1","kind":"shell","description":"Cron: */30 * * * * Check status","started_at":"2026-11-28T09:00:00Z"}`
	if pushTasks(proto.StatusIdle, unmatchedCron) {
		t.Error("a cron schedule reported as a shell holds the drain")
	}
	command := `{"id":"` + agyTestID + `/task-2","kind":"shell","description":"Run the tests","command":"make test","started_at":"2026-11-28T09:00:00Z"}`
	unmatchedCommand := `{"id":"` + agyTestID + `/task-3","kind":"shell","description":"Wait for ready","started_at":"2026-11-28T09:00:00Z"}`
	if !pushTasks(proto.StatusIdle, unmatchedCron, command) {
		t.Error("a background command beside a cron schedule does not hold the drain")
	}
	// Unmatched tasks (no command line) and schedules cannot be told apart from the row, so neither holds the drain:
	// a misread one must never keep an upgrade waiting forever.
	if pushTasks(proto.StatusIdle, unmatchedCommand) {
		t.Error("an unmatched task without its command line holds the drain")
	}
}

// A new agy waits for a background command a session left running, as it does for a working turn (#17200), and both
// the restart and waitAgyIdle go ahead once only a standing schedule (monitor) is left.
func TestAgyBridgeRestartWaitsForFiniteBackgroundWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	bin, marks := filepath.Join(dir, "agy"), filepath.Join(dir, "marks")
	writeAgy(t, bin, "v1")
	oldFind, oldCmd, oldProbe := findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery
	t.Cleanup(func() { findAgyExecutable, agyBridgeCommand, agyBridgeProbeEvery = oldFind, oldCmd, oldProbe })
	agyBridgeProbeEvery = 10 * time.Millisecond
	var probes atomic.Int32 // the bridge looks up agy once per probe, before it asks whether the companion is busy
	findAgyExecutable = func() string {
		probes.Add(1)
		return bin
	}
	agyBridgeCommand = func(context.Context, *RuntimeHost, string, string) (*exec.Cmd, error) {
		script := fmt.Sprintf("echo start >> %q; trap 'echo stopped >> %q; exit 0' TERM; while :; do sleep 0.02; done", marks, marks)
		return exec.Command("/bin/sh", "-c", script), nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	host := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	logs := &syncBuffer{}
	host.logger = log.New(logs, "", 0)
	host.setAgyBridge(true)
	defer host.setAgyBridge(false)
	waitFor(t, "the companion started", func() bool { return slices.Equal(starts(marks), []string{"start"}) })

	instance := host.builtinAgyInstance()
	client, hello := startAgyHello(t, ctx, host, instance)
	if err := <-hello; err != nil {
		t.Fatal(err)
	}
	var builtin *runtimeConn
	waitFor(t, "the companion registered", func() bool { builtin, _ = host.agyPeers(instance); return builtin != nil })
	monitor := proto.BackgroundTask{ID: "s1/task-1", Kind: "monitor", Description: "Cron: */5 * * * *"}
	background := func(tasks ...proto.BackgroundTask) {
		builtin.mu.Lock()
		builtin.sessions = []proto.Session{{SessionID: "s1", Status: proto.StatusIdle, Background: tasks}}
		builtin.mu.Unlock()
	}
	command := proto.BackgroundTask{ID: "s1/task-2", Kind: "shell", Command: "make test"}

	// waitAgyIdle (the external-runtime handoff's wait) holds while the command runs. Without it waitAgyIdle returns on
	// its first look, with no tick to wait for, so the window below only has to outlast a goroutine start.
	background(monitor, command)
	idle := make(chan struct{})
	go func() {
		builtin.waitAgyIdle(ctx)
		close(idle)
	}()
	select {
	case <-idle:
		t.Fatal("waitAgyIdle returned while a background command was running")
	case <-time.After(100 * time.Millisecond):
	}
	background(monitor)
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("waitAgyIdle still waits with only a monitor schedule left")
	}

	// So does the restart on a new agy.
	background(monitor, command)
	writeAgy(t, bin, "v2")
	waitFor(t, "the restart to wait for the background command", func() bool {
		return strings.Contains(logs.String(), "agy at "+bin+" was replaced; restarting once no agy session is working or running background work") &&
			host.agyDraining(instance)
	})
	// Two more probes found the command still running. Had one restarted, it would have stopped the companion (and its
	// trap written "stopped") before the next lookup of agy, the one for the companion's next start.
	seen := probes.Load()
	waitFor(t, "two more probes", func() bool { return probes.Load() >= seen+2 })
	if got := starts(marks); !slices.Equal(got, []string{"start"}) {
		t.Fatalf("the companion was stopped while a background command was running: %v", got)
	}

	background(monitor)
	waitFor(t, "the restart once only the monitor is left", func() bool { return slices.Contains(starts(marks), "stopped") })
	_ = client.Close()
	waitFor(t, "the companion started again", func() bool { m := starts(marks); return len(m) >= 3 && m[2] == "start" })
}

// What counts as a change: another path, another file at the path, none any more; the same one seen twice in a row.
func TestAgyChangeSettlesOnTwoProbes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agy")
	writeAgy(t, path, "v1")
	bin := statAgy(path)
	var c agyChange
	if c.see(bin, statAgy(path)) || c.seen != nil {
		t.Fatalf("the same file is a change: %+v", c)
	}
	writeAgy(t, path, "v2")
	if c.see(bin, statAgy(path)) {
		t.Fatal("one probe's change restarted")
	}
	if !c.see(bin, statAgy(path)) {
		t.Fatal("the same change on the next probe did not")
	}
	if c.see(bin, statAgy(path+"-gone")) || c.see(bin, statAgy("")) {
		t.Fatal("a different change must be seen twice of its own")
	}
	if !c.see(bin, statAgy("")) || c.describe(bin) != "agy at "+path+" is gone" {
		t.Fatalf("agy gone: %q", c.describe(bin))
	}
	if c.see(bin, bin) || c.seen != nil {
		t.Fatal("back to the companion's own agy is no change")
	}
}

// The companion is told the node's own program, which serves its chats' Persona tools (`<program> mcp`): a Persona
// Link install calls it persona-node, and the companion's lookup by name found only personad (#14582).
func TestAgyBridgeCommandPassesTheNodeProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell script fixture")
	}
	dir := t.TempDir()
	for rel, body := range map[string]string{
		"antigravity/package.json":  `{"type":"module"}`,
		"antigravity/companion.mjs": "console.log('ok')\n",
		"runtime-sdk/package.json":  `{"type":"module"}`,
		"runtime-sdk/index.js":      "export {}\n",
	} {
		p := filepath.Join(dir, "pkgs", agyPackage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fakeNode := filepath.Join(dir, "node-22")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\nprintf 22.19.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimepkg.OverrideEnv, filepath.Join(dir, "pkgs"))
	t.Setenv("PERSONA_NODEJS", fakeNode)
	program := filepath.Join(dir, "home", ".persona", "bin", "persona-node")
	oldSelf := selfProgram
	selfProgram = func(string, string) (string, string, error) { return program, program, nil }
	t.Cleanup(func() { selfProgram = oldSelf })

	rh := NewRuntimeHost("test", filepath.Join(dir, "state", "runtime.sock"))
	cmd, err := agyBridgeCommand(context.Background(), rh, "/usr/local/bin/agy", "builtin-agy-1-1")
	if err != nil {
		t.Fatalf("agyBridgeCommand: %v", err)
	}
	i := slices.Index(cmd.Args, "--personad-bin")
	if i < 0 || i+1 >= len(cmd.Args) || cmd.Args[i+1] != program {
		t.Fatalf("cmd.Args %q: want --personad-bin %s", cmd.Args, program)
	}
}

func TestManagedAgyRuntimeCallsCarryIsolatedChannelSpec(t *testing.T) {
	n, f, home := herdrNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.runtimeSock = filepath.Join(filepath.Dir(f.sock), "runtime.sock")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if _, err := n.listenRuntimeSocket(ctx); err != nil {
		t.Fatal(err)
	}

	hello := assistantRuntimeHello()
	hello["runtime"], hello["agent"], hello["launcher"] = agyAgent, agyAgent, agyAgent
	hello["caps"].(map[string]any)["models"] = true
	hello["launchers"] = []map[string]any{{"name": agyAgent, "agent": agyAgent}}
	companion := dialRuntime(t, n, hello)
	nextID := 0
	sessionIDs := []string{"agy-native-1", "agy-managed-1"}
	companion.mu.Lock()
	companion.answer = func(method string, raw json.RawMessage) (any, error) {
		switch method {
		case "session.start":
			id := sessionIDs[nextID]
			nextID++
			return map[string]any{"session": proto.Session{SessionID: id, Cwd: home, Status: proto.StatusIdle}}, nil
		case "launchers.models":
			var req struct {
				Channel *agyChannelSpec `json:"channel"`
			}
			_ = json.Unmarshal(raw, &req)
			if req.Channel != nil && req.Channel.ID == "gw-alpha" {
				return proto.LauncherModels{Agent: agyAgent, Models: []proto.AgentModel{{ID: "gw-alpha-model"}}}, nil
			}
			return proto.LauncherModels{Agent: agyAgent, Models: []proto.AgentModel{{ID: "native-oauth-model"}}}, nil
		default:
			return map[string]any{"ok": true}, nil
		}
	}
	companion.mu.Unlock()

	n.setCatalog([]proto.ManagedLauncher{
		{Name: "gw-alpha", Agent: agyAgent, BaseURL: "https://alpha.example.com", APIKey: "alpha-secret", Rev: "r7"},
	})

	// 1. Native agy start carries no channel spec; managed agy start carries its isolated channel spec.
	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd": home, "launcher": agyAgent, "host": proto.HostAgy,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd": home, "launcher": "gw-alpha", "host": proto.HostAgy,
	}); err != nil {
		t.Fatal(err)
	}
	starts := companion.got("session.start")
	if len(starts) != 2 {
		t.Fatalf("session.start count = %d, want 2", len(starts))
	}
	var nativeStart, managedStart struct {
		Channel *agyChannelSpec `json:"channel"`
	}
	_ = json.Unmarshal(starts[0], &nativeStart)
	_ = json.Unmarshal(starts[1], &managedStart)
	if nativeStart.Channel != nil {
		t.Fatalf("native agy start unexpectedly carried channel: %+v", nativeStart.Channel)
	}
	if managedStart.Channel == nil || managedStart.Channel.ID != "gw-alpha" ||
		managedStart.Channel.AuthMode != "gemini-api-key" || managedStart.Channel.Rev != "r7" ||
		managedStart.Channel.Env["GEMINI_API_KEY"] != "alpha-secret" {
		t.Fatalf("managed agy start channel = %+v", managedStart.Channel)
	}

	// 2. Follow-up session.send on the managed session replays its isolated channel spec.
	if _, err := nodeCall(t, n, "session.send", map[string]any{
		"session_id": "agy-managed-1", "text": "hello",
	}); err != nil {
		t.Fatal(err)
	}
	sends := companion.got("session.send")
	if len(sends) != 1 {
		t.Fatalf("session.send count = %d, want 1", len(sends))
	}
	var managedSend struct {
		Channel *agyChannelSpec `json:"channel"`
	}
	_ = json.Unmarshal(sends[0], &managedSend)
	if managedSend.Channel == nil || managedSend.Channel.ID != "gw-alpha" || managedSend.Channel.Rev != "r7" {
		t.Fatalf("managed agy send channel = %+v", managedSend.Channel)
	}

	// 3. launchers.models isolates native agy catalog from managed channel catalog.
	nativeModels, err := nodeCall(t, n, "launchers.models", map[string]any{"name": agyAgent, "host": proto.HostAgy})
	if err != nil || !strings.Contains(string(nativeModels), "native-oauth-model") {
		t.Fatalf("native models = %s, %v", nativeModels, err)
	}
	managedModels, err := nodeCall(t, n, "launchers.models", map[string]any{"name": "gw-alpha", "host": proto.HostAgy})
	if err != nil || !strings.Contains(string(managedModels), "gw-alpha-model") {
		t.Fatalf("managed models = %s, %v", managedModels, err)
	}
}
