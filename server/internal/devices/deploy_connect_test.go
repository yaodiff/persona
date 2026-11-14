package devices

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A machine that comes back on another node build is sent this hub's build (deploy.go updateOnConnect): scripts/deploy
// deploys and restarts the hub as a plain start, so the manager's only redeploy (after a hub self-update) never ran
// and a machine that was asleep then stayed on the old node, only logging the mismatch (#2986).

// buildNode is a node over a dial that reports a build other than the hub's; it answers what the manager asks when a
// machine connects and counts what an update asks (the link's node.update.* protocol).
type buildNode struct {
	mu      sync.Mutex
	info    proto.NodeInfo
	calls   []string
	updates int
}

func (n *buildNode) dial(context.Context) (net.Conn, error) {
	managerSide, nodeSide := net.Pipe()
	p := rpc.New("n", nodeSide)
	p.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		n.calls = append(n.calls, method)
		switch method {
		case "node.info":
			return n.info, nil
		case "sessions.list":
			return map[string]any{"sessions": []proto.Session{}}, nil
		case "node.update.begin":
			n.updates++
			return map[string]any{"id": "update-one", "offset": 0}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	go func() { _ = p.Serve(context.Background(), nodeSide); nodeSide.Close() }()
	return managerSide, nil
}

func (n *buildNode) seen(method string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, c := range n.calls {
		if c == method {
			count++
		}
	}
	return count
}

func (n *buildNode) updateCalls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.updates
}

// connectMachine runs m's run loop against the in-process node and waits until it is served.
func connectMachine(t *testing.T, d *Devices, cfg MachineConfig, n *buildNode) *Machine {
	t.Helper()
	m := newMachine(d, cfg)
	m.dialNode = n.dial
	d.addEntry(m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, 5*time.Second, cfg.ID+" connected", func() bool { return m.reachable() && n.seen("sessions.list") > 0 })
	return m
}

// hubNodeBinary writes a node for the machine's platform where Redeploy finds it (nodeBinary, deploy.go).
func hubNodeBinary(t *testing.T, goos, arch string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "personad-"+goos+"-"+arch), []byte("the hub's node"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_NODE_BINARIES", dir)
}

// fakeSSH puts an ssh first on PATH that records its arguments and fails: a machine the hub cannot reach that way.
// It returns the file the calls are written to.
func fakeSSH(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "ssh.calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + shellQuote(calls) + "\nexit 255\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	return calls
}

func sshCalls(t *testing.T, calls string) []string {
	t.Helper()
	b, err := os.ReadFile(calls)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// A machine that connects on another build is sent the hub's node, and the outcome is kept so the same build is not
// sent again on every reconnect.
func TestMachineBackOnAnotherBuildGetsTheHubsNode(t *testing.T) {
	calls := fakeSSH(t)
	hubNodeBinary(t, "linux", "amd64")
	d := newTestDevices(t)
	d.cfg.HubVersion = "hub-build"
	d.cfg.AutoUpdateNodes = true
	n := &buildNode{info: proto.NodeInfo{Hostname: "laptop", OS: "linux", Arch: "amd64", Version: "old-build", NodeUpdate: true}}
	m := connectMachine(t, d, MachineConfig{ID: "laptop", Name: "laptop", Kind: "ssh", SSHHost: "laptop.invalid"}, n)
	if s := m.snapshot(); !s.NodeMismatch {
		t.Fatalf("connected on another build, no mismatch shown: %+v", s)
	}
	eventually(t, 5*time.Second, "the hub's node sent to the machine", func() bool { return len(sshCalls(t, calls)) > 0 })
	var record nodeDeployRecord
	eventually(t, 5*time.Second, "the attempt kept", func() bool {
		found, err := d.opts.Store.(*memStore).Setting(context.Background(), m.deployRecordKey(), &record)
		return err == nil && found && record.Target == "hub-build"
	})
	if record.State != "failed" {
		t.Fatalf("record of an unreachable machine: %+v", record)
	}

	// Back again (the machine was still not reachable, nothing installed): the same build is not pushed a second time.
	m.reconnect()
	eventually(t, 5*time.Second, "connected again", func() bool { return n.seen("sessions.list") > 1 })
	time.Sleep(300 * time.Millisecond)
	if got := len(sshCalls(t, calls)); got != 1 {
		t.Fatalf("the same build sent %d times over two connections, want 1", got)
	}
}

// A linked machine (the update protocol, not ssh) is sent the hub's node too, and the record names the build it is for.
func TestLinkedMachineBackOnAnotherBuildGetsTheHubsNode(t *testing.T) {
	hubNodeBinary(t, "linux", "amd64")
	d := newTestDevices(t)
	d.cfg.HubVersion = "hub-build"
	d.cfg.AutoUpdateNodes = true
	n := &buildNode{
		info: proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "amd64", Version: "old-build", NodeUpdate: true},
	}
	m := connectMachine(t, d, MachineConfig{ID: "box", Name: "box", Kind: kindLink}, n)
	eventually(t, 5*time.Second, "the update sent over the link", func() bool { return n.updateCalls() > 0 })
	eventually(t, 5*time.Second, "the hub's node transferred", func() bool { return n.seen("node.update.chunk") > 0 })
	var record nodeDeployRecord
	if found, err := d.opts.Store.(*memStore).Setting(context.Background(), m.deployRecordKey(), &record); err != nil || !found {
		t.Fatalf("record=%+v found=%v err=%v", record, found, err)
	}
	if record.Target != "hub-build" {
		t.Fatalf("record=%+v", record)
	}
}

// A node this manager cannot drive still uses the update protocol to become compatible (#7774).
func TestIncompatibleLinkedMachineGetsUpdateOnConnect(t *testing.T) {
	hubNodeBinary(t, "linux", "amd64")
	d := newTestDevices(t)
	d.cfg.HubVersion = "hub-build"
	d.cfg.AutoUpdateNodes = true
	n := &buildNode{info: proto.NodeInfo{
		Hostname: "box", OS: "linux", Arch: "amd64", Version: "old-build",
		Protocol: nodeproto.Version + 2, ProtocolMin: nodeproto.Version + 1,
		Capabilities: nodeproto.Announced, NodeUpdate: true,
	}}
	m := newMachine(d, MachineConfig{ID: "box", Name: "box", Kind: kindLink})
	m.dialNode = n.dial
	d.addEntry(m)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	eventually(t, 5*time.Second, "the incompatible node connected", m.reachable)
	if got := m.snapshot().NodeIncompatible; got != incompatibleNodeNew {
		t.Fatalf("the fixture must exercise the incompatible branch, got %q", got)
	}
	eventually(t, 5*time.Second, "the update sent over the link", func() bool { return n.updateCalls() == 1 })
	eventually(t, 5*time.Second, "the hub's node transferred", func() bool { return n.seen("node.update.chunk") > 0 })
	if got := n.updateCalls(); got != 1 {
		t.Fatalf("one reconnect must begin one update, got %d", got)
	}
}

// Redeploy over ssh writes to the file the node runs from and sends the guard that keeps personad in place only where
// persona-node goes to a file that is not the device's own (bin/persona-node takes either program).
func TestSSHDeployTargetAndGuard(t *testing.T) {
	calls := fakeSSH(t)
	m := newMachine(newTestDevices(t), MachineConfig{ID: "box", Kind: "ssh", SSHHost: "box.invalid"})
	dir := t.TempDir()
	for _, tc := range []struct {
		build, program, executable, target string
		guard                              bool
	}{
		{"persona-node-linux-amd64", "persona-node", "/home/u/.persona/bin/persona-node", "/home/u/.persona/bin/persona-node", false},
		{"personad-linux-amd64", "personad", "/home/u/.persona/bin/persona-node", "/home/u/.persona/bin/persona-node", false},
		{"persona-node-linux-amd64", "persona-node", "/opt/node/run-me", "/opt/node/run-me", true},
		{"personad-linux-amd64", "personad", "/home/u/.persona/bin/personad", ".persona/bin/personad", false},
	} {
		bin := filepath.Join(dir, tc.build)
		_ = os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755)
		_ = os.Remove(calls)
		m.mu.Lock()
		m.info = proto.NodeInfo{Program: tc.program, Executable: tc.executable}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := m.deploy(ctx, bin, m.d.hubVersion())
		cancel()
		got := strings.Join(sshCalls(t, calls), "\n")
		if err == nil || !strings.Contains(got, tc.target) || strings.Contains(got, "node-update-check") != tc.guard {
			t.Errorf("%s from %s: err %v, want %s guard %v in %q", tc.build, tc.executable, err, tc.target, tc.guard, got)
		}
	}
}

// Connecting never waits on the release site: a machine whose build needs it to be looked up (another platform, another
// build, nothing of the hub's own) is served, and the calls that waited for it go, while the site has not answered.
func TestConnectNeverWaitsOnTheSite(t *testing.T) {
	release := make(chan struct{})
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select { // a site that takes the connection and does not answer
		case <-release:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(site.Close)
	t.Cleanup(func() { close(release) })
	isolateNodeBinaries(t)
	d := newTestDevices(t)
	d.cfg.HubVersion, d.cfg.NodePackagesURL = "1.2.3", site.URL
	n := &buildNode{info: proto.NodeInfo{Hostname: "box", OS: "linux", Arch: "riscv64", Version: "old-build", NodeUpdate: true}}
	start := time.Now()
	m := connectMachine(t, d, MachineConfig{ID: "box", Name: "box", Kind: kindLink}, n)
	select {
	case <-m.first:
	case <-time.After(3 * time.Second):
		t.Fatal("the calls that waited for the machine are still waiting on the release site")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("connecting took %s", took)
	}
}
