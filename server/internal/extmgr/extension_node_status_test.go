package extmgr

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The top-level status of a plugin whose work happens on computers comes from its machines while the hub's own
// process is idle: one running machine is "running", only abnormal machines are "error", then "starting", else
// "stopped"; the counts let the apps say "Running on N machines" with one rule.
func TestNodeStatusAggregation(t *testing.T) {
	cases := []struct {
		name       string
		nodes      []ExtensionNodeView
		wantOK     bool
		wantStatus string
		want       ExtensionNodeStatusView
	}{
		{"no machines", nil, false, "", ExtensionNodeStatusView{}},
		{
			"all running",
			[]ExtensionNodeView{{MachineID: "a", State: "running"}, {MachineID: "b", State: "running"}},
			true, host.StateRunning, ExtensionNodeStatusView{Total: 2, Running: 2},
		},
		{
			"one running one abnormal stays running and counts the abnormal one",
			[]ExtensionNodeView{{MachineID: "a", State: "running"}, {MachineID: "b", State: "error"}},
			true, host.StateRunning, ExtensionNodeStatusView{Total: 2, Running: 1, Abnormal: 1},
		},
		{
			"none running, an abnormal machine is an error",
			[]ExtensionNodeView{{MachineID: "a", State: "offline"}, {MachineID: "b", State: "error"}},
			true, host.StateError, ExtensionNodeStatusView{Total: 2, Offline: 1, Abnormal: 1},
		},
		{
			"missing, lease expired and outdated count as abnormal",
			[]ExtensionNodeView{{MachineID: "a", State: "missing"}, {MachineID: "b", State: "lease_expired"}, {MachineID: "c", State: "outdated"}},
			true, host.StateError, ExtensionNodeStatusView{Total: 3, Abnormal: 3},
		},
		{
			"only starting machines say starting",
			[]ExtensionNodeView{{MachineID: "a", State: "starting"}, {MachineID: "b", State: "offline"}},
			true, host.StateStarting, ExtensionNodeStatusView{Total: 2, Starting: 1, Offline: 1},
		},
		{
			"all stopped",
			[]ExtensionNodeView{{MachineID: "a", State: "stopped"}, {MachineID: "b", State: "stopped"}},
			true, host.StateStopped, ExtensionNodeStatusView{Total: 2, Stopped: 2},
		},
		{
			"all offline is stopped with the offline count",
			[]ExtensionNodeView{{MachineID: "a", State: "offline"}},
			true, host.StateStopped, ExtensionNodeStatusView{Total: 1, Offline: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, got, ok := nodeStatus(tc.nodes)
			if ok != tc.wantOK || status != tc.wantStatus || got != tc.want {
				t.Fatalf("nodeStatus(%+v) = %q %+v %v; want %q %+v %v", tc.nodes, status, got, ok, tc.wantStatus, tc.want, tc.wantOK)
			}
		})
	}
}

// helperPackageWithHubRuntime is helperPackage plus an on-call hub process (the same script stands in for it): the
// hub process is idle while the node program does the work, as with a hook installer.
func helperPackageWithHubRuntime(t *testing.T) string {
	dir := helperPackage(t)
	manifest := filepath.Join(dir, extension.ManifestFile)
	raw, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	runtimeLine := `"runtime": {"command": {"linux-amd64": "node/helper-linux", "darwin-arm64": "node/helper-mac"}, "args": ["hub"]}, "node": {`
	raw = []byte(strings.Replace(string(raw), `"node": {`, runtimeLine, 1))
	if err := os.WriteFile(manifest, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// An extension whose hub process is idle shows the state of its program on the machines, not "stopped": running while
// one machine runs it, stopped when every machine stops, error when only abnormal machines are left. The hub process
// status still governs while it runs itself.
func TestExtensionViewStatusFollowsNodeProgram(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	box, _ := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackageWithHubRuntime(t), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	// The on-call hub process has nothing to do and parks as stopped.
	waitLong(t, "the idle hub process to stop", func() bool {
		hp := h.extensions.get("example-helper")
		return hp != nil && hp.proc != nil && func() bool {
			st, _ := hp.proc.Status()
			return st == host.StateStopped
		}()
	})
	h.syncExtensionNodes(ctx, box)

	p := reg.Get("example-helper")
	view := h.extensions.view(ctx, p)
	if view.Status != host.StateRunning {
		t.Fatalf("one machine runs it: status %q, want running; view %+v", view.Status, view)
	}
	if view.NodeStatus == nil || *view.NodeStatus != (ExtensionNodeStatusView{Total: 1, Running: 1}) {
		t.Fatalf("node summary: %+v", view.NodeStatus)
	}
	if view.Error != "" {
		t.Fatalf("an aggregated running status carries no error: %q", view.Error)
	}

	// Every machine stopped: the extension says stopped, still with the machine summary.
	h.NodeEvent("box", "plugin.node.state", json.RawMessage(`{"id":"example-helper","version":"1.0.0","state":"stopped"}`))
	view = h.extensions.view(ctx, p)
	if view.Status != host.StateStopped || view.NodeStatus == nil || view.NodeStatus.Stopped != 1 {
		t.Fatalf("all stopped: %+v", view)
	}

	// Only abnormal machines left: the extension itself is in error.
	h.NodeEvent("box", "plugin.node.state", json.RawMessage(`{"id":"example-helper","version":"1.0.0","state":"error","error":"boom"}`))
	view = h.extensions.view(ctx, p)
	if view.Status != host.StateError || view.NodeStatus == nil || view.NodeStatus.Abnormal != 1 {
		t.Fatalf("abnormal only: %+v", view)
	}
}

// An extension with only a node program (the hub runs no process of its own) gets the same aggregate: the hub's
// declaration-only "running" gives way to what the machines say, so all machines stopped reads as stopped.
func TestNodeOnlyExtensionStatusComesFromMachines(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	box, _ := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackage(t), "")
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
	p := reg.Get("example-helper")
	if hp := h.extensions.get("example-helper"); hp == nil || hp.proc != nil {
		t.Fatalf("the hub runs no process for it: %+v", hp)
	}
	view := h.extensions.view(ctx, p)
	if view.Status != host.StateRunning || view.NodeStatus == nil || view.NodeStatus.Running != 1 {
		t.Fatalf("one machine runs it: %+v", view)
	}

	h.NodeEvent("box", "plugin.node.state", json.RawMessage(`{"id":"example-helper","version":"1.0.0","state":"stopped"}`))
	view = h.extensions.view(ctx, p)
	if view.Status != host.StateStopped || view.NodeStatus == nil || view.NodeStatus.Stopped != 1 {
		t.Fatalf("all machines stopped: %+v", view)
	}

	// Disabled keeps the hub's own status even if a machine state lingers before its next sync.
	if err := reg.SetEnabled(ctx, "example-helper", false, "test"); err != nil {
		t.Fatal(err)
	}
	view = h.extensions.view(ctx, p)
	if view.Status != extensionDisabled {
		t.Fatalf("disabled keeps the hub's say: %+v", view)
	}
	if view.NodeStatus != nil {
		t.Fatalf("no aggregate while the hub's own status governs (disabled): %+v", view.NodeStatus)
	}
}

// While the hub's own process lives, its status governs and no aggregate is shipped: clients must never pair a
// hub-process status with machine counts (which would render "0 machines have an issue" while the hub process errors).
func TestLiveHubProcessKeepsItsStatusWithoutNodeSummary(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	box, _ := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+helperPackageWithHubRuntime(t), "")
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

	// A live, startup RPC process: the hub has a process of its own that reports running once initialized.
	socket := filepath.Join(shortTempDir(t), "live.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		peer := rpc.New("test", conn)
		peer.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
			if method == extension.MInitialize {
				return extension.InitializeResult{Protocol: extension.Protocol, ID: "example-helper", Version: "1.0.0"}, nil
			}
			return map[string]any{}, nil
		}
		_ = peer.Serve(context.Background(), conn)
	}()
	procCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	live := host.Start(procCtx, host.Spec{ID: "example-helper", Name: "Example Helper", RPC: true, RPCSocket: socket, Startup: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: extension.Protocol}, nil
		}})
	t.Cleanup(func() { live.Stop(); _ = listener.Close() })
	waitLong(t, "the live hub process to run", func() bool {
		st, _ := live.Status()
		return st == host.StateRunning
	})
	h.extensions.byID["example-helper"] = &hubExtension{id: "example-helper", proc: live}

	view := h.extensions.view(ctx, reg.Get("example-helper"))
	if view.Status != host.StateRunning {
		t.Fatalf("the live hub process governs: %+v", view)
	}
	if view.NodeStatus != nil {
		t.Fatalf("no aggregate while the hub's own process governs: %+v", view.NodeStatus)
	}
	if len(view.Nodes) != 1 {
		t.Fatalf("the per-machine rows still ship: %+v", view.Nodes)
	}
}
