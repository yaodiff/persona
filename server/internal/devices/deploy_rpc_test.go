package devices

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

type updatingNode struct {
	mu                  sync.Mutex
	machine             *Machine
	peer                *rpc.Peer
	data                []byte
	expected            []byte
	begin               int
	commits             int
	confirms            int
	aborts              int
	resume              int64
	badOffset           bool
	busyCommit          bool
	busySession         bool
	sessionErr          error
	background          bool
	sessions            []proto.Session // when set, what sessions.list answers
	updateBusy          *bool           // with sessions: the node's update_busy (nil leaves it out)
	installErr          error
	commitDelay         time.Duration // how long commit takes before it answers (a scanned first self-check)
	stayOld             bool
	target              string // the build it confirms ("" the fixture hub's, new-build)
	committed           chan struct{}
	handoffGraceSeconds int
}

func (n *updatingNode) handle(_ context.Context, method string, params json.RawMessage) (any, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch method {
	case "sessions.list":
		if n.sessionErr != nil {
			return nil, n.sessionErr
		}
		if n.sessions != nil {
			if n.updateBusy != nil {
				return map[string]any{"sessions": n.sessions, "update_busy": *n.updateBusy}, nil
			}
			return map[string]any{"sessions": n.sessions}, nil
		}
		if n.background {
			return map[string]any{"sessions": []proto.Session{{SessionID: "background", Status: proto.StatusIdle, Background: []proto.BackgroundTask{{ID: "task", Kind: "shell"}}}}}, nil
		}
		if n.busySession {
			return map[string]any{"sessions": []proto.Session{{SessionID: "busy", Status: proto.StatusWorking}}}, nil
		}
		return map[string]any{"sessions": []proto.Session{}}, nil
	case "node.update.begin":
		var p struct {
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(n.expected)
		if p.Size != int64(len(n.expected)) || p.SHA256 != hex.EncodeToString(sum[:]) || p.Version != cmp.Or(n.target, "new-build") {
			return nil, errors.New("wrong update metadata")
		}
		n.begin++
		n.data = append([]byte{}, n.expected[:n.resume]...)
		return map[string]any{"id": "update-one", "offset": n.resume}, nil
	case "node.update.chunk":
		var p struct {
			ID     string `json:"id"`
			Offset int64  `json:"offset"`
			Data   []byte `json:"data"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.ID != "update-one" || p.Offset != int64(len(n.data)) || len(p.Data) > proto.UploadChunkBytes {
			return nil, errors.New("wrong chunk position or size")
		}
		n.data = append(n.data, p.Data...)
		offset := len(n.data)
		if n.badOffset {
			offset++
		}
		return map[string]any{"offset": offset}, nil
	case "node.update.commit":
		var p proto.NodeUpdateCommitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.HandoffGraceSeconds != nil {
			n.handoffGraceSeconds = *p.HandoffGraceSeconds
		}
		n.commits++
		time.Sleep(n.commitDelay) // sleep-ok: the fake node's commit takes this long, as a node's first-run self-checks do
		if n.busyCommit && n.commits == 1 {
			return nil, &rpc.Error{Code: "node_update_busy", Message: "node is busy"}
		}
		if n.installErr != nil {
			return nil, n.installErr
		}
		if !bytes.Equal(n.data, n.expected) {
			return nil, errors.New("commit before all bytes arrived")
		}
		if !n.stayOld {
			n.machine.mu.Lock()
			n.machine.info.Version = cmp.Or(n.target, "new-build")
			n.machine.mu.Unlock()
		}
		if n.committed != nil {
			close(n.committed)
		}
		return map[string]any{"accepted": true}, nil
	case "node.update.confirm":
		var p struct{ Version string }
		_ = json.Unmarshal(params, &p)
		if want := n.target; p.Version != want && (want != "" || p.Version != "new-build") {
			return nil, errors.New("wrong confirmation version")
		}
		n.confirms++
		return map[string]any{"ok": true}, nil
	case "node.update.abort":
		n.aborts++
		return map[string]any{"ok": true}, nil
	}
	return nil, errors.New("unexpected method " + method)
}

func linkedDeployFixture(t *testing.T, kind string) (*Devices, *Machine, *updatingNode, string) {
	t.Helper()
	h := newTestDevices(t)
	h.cfg.HubVersion = "new-build"
	h.cfg.AutoUpdateNodes = true
	m := newMachine(h, MachineConfig{ID: "example-node", Name: "Example node", Kind: kind})
	m.deployRetry = 10 * time.Millisecond // a busy node is asked again at once, not after production's 2s
	m.online = true
	m.info = proto.NodeInfo{OS: "linux", Arch: "amd64", Version: "old-build", NodeUpdate: true}
	h.addEntry(m)
	n := &updatingNode{machine: m, expected: bytes.Repeat([]byte("a-binary-block"), proto.UploadChunkBytes/4)}
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	m.peer = rpc.New("hub", nodeIn)
	nodePeer := rpc.New("node", hubIn)
	n.peer = nodePeer
	m.peer.OnEvent = m.onEvent
	nodePeer.OnRequest = n.handle
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.ctx = ctx
	h.mu.Unlock()
	t.Cleanup(func() { cancel(); toNode.Close(); toHub.Close() })
	go m.peer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	bin := filepath.Join(t.TempDir(), "personad-linux-amd64")
	if err := os.WriteFile(bin, n.expected, 0o755); err != nil {
		t.Fatal(err)
	}
	return h, m, n, bin
}

// A failed update stops showing once the node runs the hub's build, online; another build or offline, it stays (#2868).
func TestStaleDeployErrorClearsWhenTheNodeMatches(t *testing.T) {
	_, m, _, _ := linkedDeployFixture(t, kindLink)
	hub := m.d.hubVersion()
	if hub == "" {
		t.Fatal("the fixture's hub has no version")
	}
	m.deployErr = "检查电脑是否空闲失败：machine is offline"
	m.online, m.info.Version = true, "old-build"
	if s := m.snapshot(); s.DeployError == "" {
		t.Fatalf("another build: the failure is still news: %+v", s)
	}
	m.online, m.info.Version = false, hub
	if s := m.snapshot(); s.DeployError == "" {
		t.Fatalf("offline: the failure stays: %+v", s)
	}
	m.online = true
	if s := m.snapshot(); s.DeployError != "" {
		t.Fatalf("online with the hub's build: no stale failure: %+v", s)
	}
}

func TestLinkedDeployCapabilities(t *testing.T) {
	hubNodeBinary(t, "linux", "amd64", "new-build")
	for _, kind := range []string{kindLink, kindService} {
		t.Run(kind, func(t *testing.T) {
			_, m, _, _ := linkedDeployFixture(t, kind)
			m.info.NodeUpdate = false
			if s := m.snapshot(); s.CanDeploy == nil || *s.CanDeploy || !strings.Contains(s.DeployReason, "升级一次") || s.DeployNoPackage {
				t.Fatalf("old node: %+v", s) // the node itself blocks the update, not a missing package (#10566)
			}
			if err := m.startDeploy(); err == nil || !strings.Contains(err.Error(), "升级一次") {
				t.Fatalf("old node start: %v", err)
			}
			// No build for its platform either: the node's own block still decides (#10566).
			arch := m.info.Arch
			m.info.Arch = "mips"
			_, _ = m.d.resolvedTargetKnown(context.Background(), m.info)
			if s := m.snapshot(); *s.CanDeploy || s.DeployNoPackage || !strings.Contains(s.DeployReason, "升级一次") {
				t.Fatalf("old node, no build either: %+v", s)
			}
			m.info.Arch = arch
			m.info.NodeUpdate = true
			if s := m.snapshot(); s.CanDeploy == nil || !*s.CanDeploy || s.DeployNoPackage {
				t.Fatalf("capable node: %+v", s)
			}
			m.online = false
			if s := m.snapshot(); *s.CanDeploy || !strings.Contains(s.DeployReason, "不在线") {
				t.Fatalf("offline node: %+v", s)
			}
		})
	}
}

func TestLinkedDeployTransfersAndConfirms(t *testing.T) {
	for _, kind := range []string{kindLink, kindService} {
		for _, resume := range []int64{0, 23} {
			t.Run(fmt.Sprintf("%s/resume-%d", kind, resume), func(t *testing.T) {
				_, m, n, bin := linkedDeployFixture(t, kind)
				n.resume = resume
				record := nodeDeployRecord{Target: "new-build", State: "queued"}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := m.performRPCDeployment(ctx, bin, &record); err != nil {
					t.Fatal(err)
				}
				n.mu.Lock()
				defer n.mu.Unlock()
				if n.begin != 1 || n.commits != 1 || n.confirms != 1 || n.aborts != 0 || !bytes.Equal(n.data, n.expected) {
					t.Fatalf("begin=%d commits=%d confirms=%d aborts=%d bytes=%d", n.begin, n.commits, n.confirms, n.aborts, len(n.data))
				}
			})
		}
	}
}

func TestFleetUpdateConvergesResidentsAcrossTransports(t *testing.T) {
	program := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, "linux", "amd64", "new-build")
	for _, kind := range []string{"local", kindLink, kindService, "ssh", "tailcat"} {
		t.Run(kind, func(t *testing.T) {
			isolateNodeBinaries(t)
			d, m, n, _ := linkedDeployFixture(t, kind)
			dir := nodeBinaryDir()
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			copyFile(t, program, filepath.Join(dir, "personad-linux-amd64"))
			b, err := os.ReadFile(program)
			if err != nil {
				t.Fatal(err)
			}
			n.mu.Lock()
			n.expected = b
			n.mu.Unlock()
			m.updateOnConnect(t.Context())
			waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return s.NodeVersion == "new-build" && !s.Deploying })
			// The post-confirmation fleet sweep must be safe on the same connection.
			d.updateNodes(t.Context())
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.begin != 1 || n.commits != 1 || n.confirms != 1 {
				t.Fatalf("transfers %d, commits %d, confirmations %d", n.begin, n.commits, n.confirms)
			}
		})
	}
}

// Redeploy sends the kind of program the node says it runs: persona-node to persona-node, personad to the rest; so
// does an update the hub queued before it restarted, when the node comes back (nodeUpdateConnected).
func TestRedeployBringsTheProgramTheNodeRuns(t *testing.T) {
	fullBuild := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, "linux", "amd64", "new-build") // linkedDeployFixture's hub
	nodeBuild := updatetest.Program(t, nodeproto.ProgramPersonaNode, "linux", "amd64")
	for _, tc := range []struct{ program, via, path string }{
		{nodeproto.ProgramPersonaNode, "redeploy", ""},
		{nodeproto.ProgramPersonad, "redeploy", "/opt/persona/bin/persona-node"},
		{"", "redeploy", ""},
		{nodeproto.ProgramPersonaNode, "reconnect", ""},
		{nodeproto.ProgramPersonad, "reconnect", "/opt/persona/bin/persona-node"},
		{"", "reconnect", ""},
	} {
		program := tc.program
		t.Run(tc.via+"/program="+program, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			isolateNodeBinaries(t)
			h, m, n, _ := linkedDeployFixture(t, kindLink)
			dir := nodeBinaryDir()
			_ = os.MkdirAll(dir, 0o755)
			full, node := filepath.Join(dir, "personad-linux-amd64"), filepath.Join(dir, "persona-node-linux-amd64")
			copyFile(t, fullBuild, full)
			copyFile(t, nodeBuild, node)
			_ = os.WriteFile(node+".version", []byte(h.hubVersion()+"\n"), 0o644)
			h.opts.Store = newMemStore()
			want := full
			if program == nodeproto.ProgramPersonaNode {
				want = node
			}
			expected, err := os.ReadFile(want)
			if err != nil {
				t.Fatal(err)
			}
			n.mu.Lock()
			n.expected = expected
			n.mu.Unlock()
			m.mu.Lock()
			m.info.Program = program
			m.info.Executable = tc.path
			info := m.info
			m.mu.Unlock()
			if tc.via == "reconnect" {
				if err := m.saveDeployRecord(context.Background(), nodeDeployRecord{Target: h.hubVersion(), State: "queued"}); err != nil {
					t.Fatal(err)
				}
				m.nodeUpdateConnected(context.Background(), m.peer, info)
			} else {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				if err := h.Redeploy(ctx, m.cfg.ID); err != nil {
					t.Fatal(err)
				}
			}
			waitMachine(t, m, 10*time.Second, func(s proto.Machine) bool { return !s.Deploying })
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.begin != 1 || n.commits != 1 || !bytes.Equal(n.data, expected) {
				t.Fatalf("begin=%d commits=%d sent %d bytes, want %s (%d bytes); error %q", n.begin, n.commits, len(n.data),
					filepath.Base(want), len(expected), m.snapshot().DeployError)
			}
		})
	}
}

func TestLinkedDeployAbortOnBadAcknowledgment(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.badOffset = true
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	if err := m.performRPCDeployment(context.Background(), bin, &record); err == nil || !strings.Contains(err.Error(), "传输位置") {
		t.Fatalf("error = %v", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.aborts != 1 || n.commits != 0 || n.confirms != 0 {
		t.Fatalf("aborts=%d commits=%d confirms=%d", n.aborts, n.commits, n.confirms)
	}
}

func TestLinkedDeployWaitsAgainWhenCommitBecomesBusy(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.busyCommit = true
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.performRPCDeployment(ctx, bin, &record); err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 2 || n.commits != 2 || n.aborts != 1 || n.confirms != 1 {
		t.Fatalf("begin=%d commits=%d aborts=%d confirms=%d", n.begin, n.commits, n.aborts, n.confirms)
	}
}

func TestLinkedDeployDoesNotTransferWhileBusy(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.busySession = true
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := m.performRPCDeployment(ctx, bin, &record); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 || n.commits != 0 {
		t.Fatal("updated a busy node")
	}
}

func TestLinkedDeployDoesNotTreatFailedInventoryAsIdle(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.sessionErr = errors.New("inventory unavailable")
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.performRPCDeployment(ctx, bin, &record); err == nil {
		t.Fatal("failed inventory allowed deployment")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 || n.commits != 0 {
		t.Fatal("updated a node without authoritative inventory")
	}
}

func TestLinkedDeployRequiresTargetVersion(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.stayOld, n.committed = true, make(chan struct{})
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		select {
		case <-n.committed:
			cancel() // transfer completed, but the old version never became the target
		case <-ctx.Done():
		}
	}()
	if err := m.performRPCDeployment(ctx, bin, &record); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.commits != 1 || n.confirms != 0 {
		t.Fatalf("commits=%d confirms=%d", n.commits, n.confirms)
	}
}

func TestLinkedDeployResumesAfterHubRestart(t *testing.T) {
	h, m, n, _ := linkedDeployFixture(t, kindService)
	h.opts.Store = newMemStore()
	m.info.Version = "new-build"
	if err := m.saveDeployRecord(context.Background(), nodeDeployRecord{Target: h.hubVersion(), State: "queued", Phase: "confirming"}); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(context.Background(), m.peer, m.info)
	waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return !s.Deploying })
	var saved nodeDeployRecord
	if found, err := h.opts.Store.Setting(context.Background(), m.deployRecordKey(), &saved); err != nil || !found || saved.State != "succeeded" {
		t.Fatalf("record=%+v found=%v err=%v", saved, found, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 || n.confirms < 1 {
		t.Fatalf("begin=%d confirms=%d", n.begin, n.confirms)
	}
}

func TestLinkedDeployStopsStaleDesiredVersion(t *testing.T) {
	h, m, n, _ := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	if err := m.saveDeployRecord(context.Background(), nodeDeployRecord{Target: "previous-hub-build", State: "queued"}); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(context.Background(), m.peer, m.info)
	if s := m.snapshot(); s.Deploying || !strings.Contains(s.DeployError, "hub 版本已变化") {
		t.Fatalf("snapshot: %+v", s)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 {
		t.Fatal("started a stale update")
	}
}

func TestUpdateNodesQueuesOfflineLinkedMachine(t *testing.T) {
	h, m, _, _ := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	m.online = false
	h.updateNodes(context.Background())
	var saved nodeDeployRecord
	if found, err := h.opts.Store.Setting(context.Background(), m.deployRecordKey(), &saved); err != nil || !found || saved.State != "queued" || saved.Target != h.hubVersion() {
		t.Fatalf("record=%+v found=%v err=%v", saved, found, err)
	}
	if m.snapshot().Deploying {
		t.Fatal("started transferring to an offline node")
	}
}

func TestLinkedDeployWaitsForBackgroundTasks(t *testing.T) {
	_, m, n, bin := linkedDeployFixture(t, kindLink)
	n.background = true
	record := nodeDeployRecord{Target: "new-build", State: "queued"}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err := m.performRPCDeployment(ctx, bin, &record); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 {
		t.Fatal("transferred while a background task was running")
	}
}

func TestLinkedDeployPersistsRejectedInstallation(t *testing.T) {
	h, m, n, bin := linkedDeployFixture(t, kindService)
	h.opts.Store = newMemStore()
	n.installErr = &rpc.Error{Code: "invalid_update", Message: "binary self-check failed"}
	record := nodeDeployRecord{Target: h.hubVersion(), State: "queued"}
	m.runRPCDeployment(context.Background(), nodePackage{Version: record.Target, path: bin}, record)
	var saved nodeDeployRecord
	_, err := h.opts.Store.Setting(context.Background(), m.deployRecordKey(), &saved)
	if err != nil || saved.State != "failed" || !strings.Contains(saved.Error, "binary self-check failed") {
		t.Fatalf("record=%+v error=%v", saved, err)
	}
	if s := m.snapshot(); s.Deploying || s.DeployError == "" || s.NodeVersion != "old-build" {
		t.Fatalf("snapshot=%+v", s)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.confirms != 0 || n.aborts != 1 {
		t.Fatalf("confirms=%d aborts=%d", n.confirms, n.aborts)
	}
}

// A new file's first self-check can take a minute while endpoint protection scans it (#12528), and the node answers
// node.update.commit only after its checks. The deploy waits for that answer: a check that failed is reported as
// itself, not taken for a lost answer and followed by the reconnect wait's not_confirmed.
func TestLinkedDeployWaitsForAScannedFirstSelfCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h, m, n, bin := linkedDeployFixture(t, kindService)
		h.opts.Store = newMemStore()
		n.commitDelay = 61 * time.Second // the first run's minute, and the second the process gets to exit
		n.installErr = &rpc.Error{Code: "invalid_update",
			Message: "node executable self-check: node-update-check timed out after 1m0s (first run of this file)"}
		record := nodeDeployRecord{Target: h.hubVersion(), State: "queued"}
		m.runRPCDeployment(t.Context(), nodePackage{Version: record.Target, path: bin}, record)
		var saved nodeDeployRecord
		if _, err := h.opts.Store.Setting(t.Context(), m.deployRecordKey(), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.State != "failed" || !strings.Contains(saved.Error, "first run of this file") {
			t.Fatalf("record = %+v, want the self-check's own failure", saved)
		}
	})
}

// Whoever waits for node.update.commit waits at least as long as the node may take to answer it.
func TestDeployCommitWaitCoversTheNodesChecks(t *testing.T) {
	if deployCommitWait() <= nodeupdate.CommitWait() {
		t.Fatalf("the deploy waits %s for node.update.commit, the node may take %s", deployCommitWait(), nodeupdate.CommitWait())
	}
}

func TestLinkedDeployPreflightFailureEndsConfirmation(t *testing.T) {
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	n.stayOld = true
	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- m.waitDeploy(ctx, run) }()
	waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "confirming" })
	for _, failure := range []proto.NodeUpdatePreflightFailure{
		{ID: "old-update", Error: "stale failure"},
		{ID: "update-one", Error: "exit status 23"},
	} {
		if err := n.peer.NotifyContext(ctx, nodeproto.EventUpdatePreflightFailed, failure); err != nil {
			t.Fatal(err)
		}
	}
	s := waitMachine(t, m, 5*time.Second, func(s proto.Machine) bool { return !s.Deploying })
	if s.DeployPhase != "" || s.DeployError != "exit status 23" || s.NodeVersion != "old-build" || !s.Online {
		t.Fatalf("snapshot = %+v", s)
	}
	select {
	case err := <-outcome:
		if err == nil || err.Error() != "exit status 23" {
			t.Fatalf("redeploy outcome = %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for redeploy outcome: %v", ctx.Err())
	}
	var saved nodeDeployRecord
	found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
	if err != nil || !found || saved.State != "failed" || saved.Error != s.DeployError || saved.Phase != "" {
		t.Fatalf("record = %+v, found = %t, error = %v", saved, found, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.commits != 1 || n.confirms != 0 || n.aborts != 0 {
		t.Fatalf("commits = %d, confirms = %d, aborts = %d", n.commits, n.confirms, n.aborts)
	}
}

func TestLinkedDeployDoesNotRepeatRolledBackInstall(t *testing.T) {
	h, m, n, _ := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	if err := m.saveDeployRecord(context.Background(), nodeDeployRecord{Target: h.hubVersion(), State: "queued", Phase: "confirming"}); err != nil {
		t.Fatal(err)
	}
	m.nodeUpdateConnected(context.Background(), m.peer, m.info)
	if s := m.snapshot(); s.Deploying || !strings.Contains(s.DeployError, "可能已回滚") {
		t.Fatalf("snapshot=%+v", s)
	}
	var saved nodeDeployRecord
	_, _ = h.opts.Store.Setting(context.Background(), m.deployRecordKey(), &saved)
	if saved.State != "failed" {
		t.Fatalf("record=%+v", saved)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.begin != 0 {
		t.Fatal("automatically installed a possibly rolled-back build again")
	}
}

// A node with node_update_idle is updated while terminal Agents work in the background, since its restart leaves them
// running, and waited for while it reports update_busy: only it knows a conversation it runs headless is busy, its rows
// do not say so (#6364). One that leaves update_busy out is waited for on the old rule.
func TestLinkedDeployWaitsOnlyForWhatTheRestartCuts(t *testing.T) {
	terminal := proto.Session{SessionID: "terminal", Status: proto.StatusWorking, State: proto.StateWorking, Host: proto.EngineHerdr,
		Background: []proto.BackgroundTask{{ID: "workflow", Kind: "workflow"}}, Subagents: []proto.Subagent{{Running: true}}}
	headless := proto.Session{SessionID: "chat", Status: proto.StatusIdle, State: proto.StateIdle} // as a node lists it: no Assistant flag
	busy, idle := true, false
	for _, tc := range []struct {
		name       string
		updateBusy *bool
		update     bool
	}{
		{"terminal agents at work", &idle, true},
		{"headless conversation answering", &busy, false},
		{"no update_busy", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, m, n, bin := linkedDeployFixture(t, kindLink)
			m.info.Protocol = nodeproto.Version
			m.info.Capabilities = []string{nodeproto.Sessions, nodeproto.NodeUpdate, nodeproto.NodeUpdateIdle}
			n.sessions, n.updateBusy = []proto.Session{terminal, headless}, tc.updateBusy
			record := nodeDeployRecord{Target: "new-build", State: "queued"}
			// The refusals wait for this deadline to expire; the update returns when it is done, so it gets a long one.
			wait := 200 * time.Millisecond
			if tc.update {
				wait = 30 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), wait)
			defer cancel()
			err := m.performRPCDeployment(ctx, bin, &record)
			n.mu.Lock()
			defer n.mu.Unlock()
			if tc.update && (err != nil || n.confirms != 1) {
				t.Fatalf("not updated: err=%v confirms=%d", err, n.confirms)
			}
			if !tc.update && (!errors.Is(err, context.DeadlineExceeded) || n.begin != 0) {
				t.Fatalf("updated while busy: err=%v begin=%d", err, n.begin)
			}
		})
	}
}
