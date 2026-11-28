package devices

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func withShortDrainTimers(t *testing.T, confirmTimeout, poll, disconnectGrace, maxWait time.Duration) {
	t.Helper()
	prevConfirm, prevPoll, prevGrace, prevMax := nodeUpdateConfirmTimeout, nodeUpdateConfirmPoll, nodeUpdateDrainDisconnectGrace, nodeUpdateDrainMaxWait
	nodeUpdateConfirmTimeout = confirmTimeout
	nodeUpdateConfirmPoll = poll
	nodeUpdateDrainDisconnectGrace = disconnectGrace
	nodeUpdateDrainMaxWait = maxWait
	t.Cleanup(func() {
		nodeUpdateConfirmTimeout = prevConfirm
		nodeUpdateConfirmPoll = prevPoll
		nodeUpdateDrainDisconnectGrace = prevGrace
		nodeUpdateDrainMaxWait = prevMax
	})
}

func makeDrainCapable(m *Machine) {
	m.mu.Lock()
	m.info.Protocol = nodeproto.Version
	m.info.Capabilities = []string{nodeproto.Sessions, nodeproto.NodeUpdate, nodeproto.NodeUpdateIdle, nodeproto.NodeUpdateDrain}
	m.mu.Unlock()
}

func waitElapsed(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func waitMachineFast(t *testing.T, m *Machine, within time.Duration, ok func(proto.Machine) bool) proto.Machine {
	t.Helper()
	deadline := time.Now().Add(within)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if s := m.snapshot(); ok(s) {
			return s
		}
		if !time.Now().Before(deadline) {
			s := m.snapshot()
			t.Fatalf("timed out: %+v", s)
			return s
		}
		<-tick.C
	}
}

func TestLinkedDeployLongDrainSucceedsWithoutNotConfirmed(t *testing.T) {
	withShortDrainTimers(t, 250*time.Millisecond, 20*time.Millisecond, 400*time.Millisecond, 5*time.Second)
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	makeDrainCapable(m)
	n.stayOld = true
	n.committed = make(chan struct{})

	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- m.waitDeploy(ctx, run) }()

	select {
	case <-n.committed:
	case <-ctx.Done():
		t.Fatalf("commit did not arrive: %v", ctx.Err())
	}
	waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.Deploying && s.DeployPhase == "draining" })
	if err := n.peer.NotifyContext(ctx, nodeproto.EventUpdateDraining, proto.NodeUpdateDraining{ID: "update-one", Target: "new-build"}); err != nil {
		t.Fatal(err)
	}

	// Drain well past the 250ms confirmation timeout (2.6x): must stay draining without not_confirmed.
	if err := waitElapsed(ctx, 650*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if s := m.snapshot(); !s.Deploying || s.DeployPhase != "draining" || s.DeployError != "" {
		t.Fatalf("snapshot during long drain = %+v, want draining without error", s)
	}
	n.mu.Lock()
	if n.confirms != 0 {
		n.mu.Unlock()
		t.Fatalf("confirmed before handover: %d", n.confirms)
	}
	n.mu.Unlock()

	// Announce handover/exec; Hub moves to confirming and starts the post-handover timer.
	if err := n.peer.NotifyContext(ctx, nodeproto.EventRestarting, proto.NodeUpdateDraining{ID: "update-one", Target: "new-build"}); err != nil {
		t.Fatal(err)
	}
	waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "confirming" })

	m.mu.Lock()
	m.info.Version = "new-build"
	m.info.UpdateDraining = nil
	m.mu.Unlock()

	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("long drain deployment failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for deployment outcome: %v", ctx.Err())
	}
	if s := m.snapshot(); s.Deploying || s.DeployPhase != "" || s.DeployError != "" {
		t.Fatalf("final snapshot = %+v", s)
	}
	var saved nodeDeployRecord
	found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
	if err != nil || !found || saved.State != "succeeded" || saved.Phase != "" || saved.TransactionID != "" || saved.Error != "" {
		t.Fatalf("saved record = %+v, found=%v, err=%v", saved, found, err)
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.commits != 1 || n.confirms != 1 {
		t.Fatalf("commits=%d confirms=%d, want 1/1", n.commits, n.confirms)
	}
}

func TestLinkedDeployPostHandoverHangStillFailsNotConfirmed(t *testing.T) {
	withShortDrainTimers(t, 200*time.Millisecond, 20*time.Millisecond, 400*time.Millisecond, 5*time.Second)
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	makeDrainCapable(m)
	n.stayOld = true

	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- m.waitDeploy(ctx, run) }()

	waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "draining" })
	if err := waitElapsed(ctx, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if s := m.snapshot(); !s.Deploying || s.DeployPhase != "draining" {
		t.Fatalf("left draining early: %+v", s)
	}

	handoverAt := time.Now()
	if err := n.peer.NotifyContext(ctx, nodeproto.EventRestarting, proto.NodeUpdateDraining{ID: "update-one", Target: "new-build"}); err != nil {
		t.Fatal(err)
	}

	wantErr := i18n.Err("devices-deploy.not_confirmed").Error()
	select {
	case err := <-outcome:
		if err == nil || err.Error() != wantErr {
			t.Fatalf("outcome = %v, want %q", err, wantErr)
		}
		if elapsed := time.Since(handoverAt); elapsed < 150*time.Millisecond {
			t.Fatalf("not_confirmed fired %s after handover, expected >= 150ms", elapsed)
		}
	case <-ctx.Done():
		t.Fatalf("waiting for not_confirmed: %v", ctx.Err())
	}
	var saved nodeDeployRecord
	found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
	if err != nil || !found || saved.State != "failed" || saved.Error != wantErr {
		t.Fatalf("saved record = %+v, found=%v, err=%v", saved, found, err)
	}
}

func TestLinkedDeployDeadDisconnectDuringDrainSurfacesFailure(t *testing.T) {
	withShortDrainTimers(t, 150*time.Millisecond, 20*time.Millisecond, 150*time.Millisecond, 5*time.Second)
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	makeDrainCapable(m)
	n.stayOld = true

	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- m.waitDeploy(ctx, run) }()

	waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "draining" })

	// Simulate abrupt host/link loss without node.restarting.
	m.mu.Lock()
	m.online = false
	m.peer = nil
	m.mu.Unlock()

	wantErr := i18n.Err("devices-deploy.not_confirmed").Error()
	select {
	case err := <-outcome:
		if err == nil || err.Error() != wantErr {
			t.Fatalf("outcome = %v, want %q", err, wantErr)
		}
	case <-ctx.Done():
		t.Fatalf("dead disconnect during drain hung: %v", ctx.Err())
	}
}

func TestLinkedDeployBriefDisconnectDuringDrainResumesOrDetectsRollback(t *testing.T) {
	for _, stillDraining := range []bool{true, false} {
		name := "resumes-draining"
		if !stillDraining {
			name = "detects-rollback"
		}
		t.Run(name, func(t *testing.T) {
			withShortDrainTimers(t, 200*time.Millisecond, 20*time.Millisecond, 500*time.Millisecond, 5*time.Second)
			h, m, n, bin := linkedDeployFixture(t, kindLink)
			h.opts.Store = newMemStore()
			makeDrainCapable(m)
			n.stayOld = true

			run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			outcome := make(chan error, 1)
			go func() { outcome <- m.waitDeploy(ctx, run) }()

			waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "draining" })

			// Brief disconnect well under disconnectGrace, then reconnect on a fresh rpc.Peer.
			m.mu.Lock()
			m.online = false
			m.mu.Unlock()
			if err := waitElapsed(ctx, 40*time.Millisecond); err != nil {
				t.Fatal(err)
			}

			toNode, nodeIn := io.Pipe()
			toHub, hubIn := io.Pipe()
			t.Cleanup(func() { _ = toNode.Close(); _ = toHub.Close() })
			newHubPeer := rpc.New("hub-2", nodeIn)
			newNodePeer := rpc.New("node-2", hubIn)
			newHubPeer.OnEvent = m.onEvent
			newNodePeer.OnRequest = n.handle
			go newHubPeer.Serve(ctx, toHub)
			go newNodePeer.Serve(ctx, toNode)

			m.mu.Lock()
			m.peer = newHubPeer
			m.online = true
			if stillDraining {
				m.info.UpdateDraining = &proto.NodeUpdateDraining{ID: "update-one", Target: "new-build"}
			} else {
				m.info.UpdateDraining = nil
			}
			m.mu.Unlock()

			if !stillDraining {
				wantErr := i18n.Err("devices-deploy.rolled_back").Error()
				select {
				case err := <-outcome:
					if err == nil || err.Error() != wantErr {
						t.Fatalf("outcome = %v, want %q", err, wantErr)
					}
				case <-ctx.Done():
					t.Fatalf("waiting for rolled_back: %v", ctx.Err())
				}
				return
			}

			// Still draining past confirmTimeout on the new peer, then handover succeeds.
			if err := waitElapsed(ctx, 350*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if s := m.snapshot(); !s.Deploying || s.DeployPhase != "draining" {
				t.Fatalf("expected draining after brief reconnect, got %+v", s)
			}
			if err := newNodePeer.NotifyContext(ctx, nodeproto.EventRestarting, proto.NodeUpdateDraining{ID: "update-one", Target: "new-build"}); err != nil {
				t.Fatal(err)
			}
			waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "confirming" })
			m.mu.Lock()
			m.info.Version = "new-build"
			m.info.UpdateDraining = nil
			m.mu.Unlock()

			select {
			case err := <-outcome:
				if err != nil {
					t.Fatalf("resumed drain failed: %v", err)
				}
			case <-ctx.Done():
				t.Fatalf("waiting for outcome: %v", ctx.Err())
			}
		})
	}
}

func TestLinkedDeployDrainMaxWaitFailsWithDrainTimeout(t *testing.T) {
	withShortDrainTimers(t, 100*time.Millisecond, 20*time.Millisecond, 500*time.Millisecond, 220*time.Millisecond)
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	makeDrainCapable(m)
	n.stayOld = true

	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wantErr := i18n.Err("devices-deploy.drain_timeout").Error()
	if err := m.waitDeploy(ctx, run); err == nil || err.Error() != wantErr {
		t.Fatalf("outcome = %v, want %q", err, wantErr)
	}
}

func TestLinkedDeployHubRestartDuringDrainDoesNotMisclassifyRolledBack(t *testing.T) {
	for _, autoUpdate := range []bool{false, true} {
		name := "auto-update-off"
		if autoUpdate {
			name = "auto-update-on"
		}
		t.Run(name, func(t *testing.T) {
			withShortDrainTimers(t, 180*time.Millisecond, 20*time.Millisecond, 400*time.Millisecond, 5*time.Second)
			h, m, n, _ := linkedDeployFixture(t, kindLink)
			h.cfg.AutoUpdateNodes = autoUpdate
			h.opts.Store = newMemStore()
			makeDrainCapable(m)
			m.mu.Lock()
			m.info.Version = "old-build"
			m.info.UpdateDraining = &proto.NodeUpdateDraining{ID: "update-one", Target: h.hubVersion()}
			info := m.info
			m.mu.Unlock()

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := m.saveDeployRecord(ctx, nodeDeployRecord{
				Target:        h.hubVersion(),
				TransactionID: "update-one",
				State:         "queued",
				Phase:         "draining",
				At:            time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}

			m.nodeUpdateConnected(ctx, m.peer, info)
			waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.Deploying && s.DeployPhase == "draining" })
			if s := m.snapshot(); s.DeployError != "" {
				t.Fatalf("misclassified draining node after hub restart: %+v", s)
			}
			// Stay draining longer than confirmTimeout: must not fail or re-transfer.
			if err := waitElapsed(ctx, 380*time.Millisecond); err != nil {
				t.Fatal(err)
			}
			if s := m.snapshot(); !s.Deploying || s.DeployPhase != "draining" || s.DeployError != "" {
				t.Fatalf("snapshot while resumed draining = %+v", s)
			}

			if err := n.peer.NotifyContext(ctx, nodeproto.EventRestarting, proto.NodeUpdateDraining{ID: "update-one", Target: h.hubVersion()}); err != nil {
				t.Fatal(err)
			}
			waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "confirming" })
			m.mu.Lock()
			m.info.Version = h.hubVersion()
			m.info.UpdateDraining = nil
			m.mu.Unlock()

			waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return !s.Deploying })
			if s := m.snapshot(); s.DeployError != "" || s.DeployPhase != "" {
				t.Fatalf("final snapshot = %+v", s)
			}
			var saved nodeDeployRecord
			found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
			if err != nil || !found || saved.State != "succeeded" || saved.Error != "" {
				t.Fatalf("saved record = %+v, found=%v, err=%v", saved, found, err)
			}
			n.mu.Lock()
			defer n.mu.Unlock()
			if n.begin != 0 || n.commits != 0 || n.confirms != 1 {
				t.Fatalf("begin=%d commits=%d confirms=%d, want 0/0/1", n.begin, n.commits, n.confirms)
			}
		})
	}

	t.Run("without-draining-signal-still-reports-rolled-back", func(t *testing.T) {
		h, m, n, _ := linkedDeployFixture(t, kindLink)
		h.opts.Store = newMemStore()
		makeDrainCapable(m)
		ctx := t.Context()
		if err := m.saveDeployRecord(ctx, nodeDeployRecord{
			Target:        h.hubVersion(),
			TransactionID: "update-one",
			State:         "queued",
			Phase:         "draining",
		}); err != nil {
			t.Fatal(err)
		}
		m.nodeUpdateConnected(ctx, m.peer, m.info)
		if s := m.snapshot(); s.Deploying || !strings.Contains(s.DeployError, "回滚") {
			t.Fatalf("expected rolled_back when UpdateDraining is absent, got %+v", s)
		}
		var saved nodeDeployRecord
		found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
		if err != nil || !found || saved.State != "failed" {
			t.Fatalf("saved = %+v, found=%v, err=%v", saved, found, err)
		}
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.begin != 0 {
			t.Fatal("re-transferred after rollback")
		}
	})
}

func TestLinkedDeployLateReconnectClearsPersistedFailedRecord(t *testing.T) {
	ctx := t.Context()
	staleErr := i18n.Err("devices-deploy.not_confirmed").Error()

	t.Run("failed-confirm-keeps-persisted-error", func(t *testing.T) {
		h, m, n, _ := linkedDeployFixture(t, kindLink)
		h.opts.Store = newMemStore()
		if err := m.saveDeployRecord(ctx, nodeDeployRecord{
			Target: h.hubVersion(), State: "failed", Error: staleErr, At: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		orig := n.peer.OnRequest
		n.peer.OnRequest = func(c context.Context, method string, params json.RawMessage) (any, error) {
			if method == "node.update.confirm" {
				return nil, errors.New("confirm refused")
			}
			return orig(c, method, params)
		}
		m.mu.Lock()
		m.info.Version = h.hubVersion()
		m.deployErr = staleErr
		info := m.info
		m.mu.Unlock()

		m.nodeUpdateConnected(ctx, m.peer, info)
		var saved nodeDeployRecord
		found, err := h.opts.Store.Setting(ctx, m.deployRecordKey(), &saved)
		if err != nil || !found || saved.State != "failed" || saved.Error != staleErr {
			t.Fatalf("unconfirmed reconnect must keep failed record: %+v, found=%v, err=%v", saved, found, err)
		}
	})

	t.Run("confirmed-late-reconnect-clears-persisted-error-permanently", func(t *testing.T) {
		h, m, n, _ := linkedDeployFixture(t, kindLink)
		store := newMemStore()
		h.opts.Store = store
		if err := m.saveDeployRecord(ctx, nodeDeployRecord{
			Target:        h.hubVersion(),
			TransactionID: "update-one",
			State:         "failed",
			Error:         staleErr,
			At:            time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		m.mu.Lock()
		m.info.Version = h.hubVersion()
		m.deployErr = staleErr
		info := m.info
		m.mu.Unlock()

		m.nodeUpdateConnected(ctx, m.peer, info)
		var saved nodeDeployRecord
		found, err := store.Setting(ctx, m.deployRecordKey(), &saved)
		if err != nil || !found || saved.State != "succeeded" || saved.Error != "" || saved.Phase != "" || saved.TransactionID != "" {
			t.Fatalf("expected persisted failed record cleared to succeeded, got %+v (found=%v err=%v)", saved, found, err)
		}
		n.mu.Lock()
		confirms := n.confirms
		n.mu.Unlock()
		if confirms != 1 {
			t.Fatalf("confirms = %d, want 1", confirms)
		}

		// Offline/online cycle must never resurface the stale deploy_error.
		m.mu.Lock()
		m.online = false
		m.mu.Unlock()
		if s := m.snapshot(); s.DeployError != "" {
			t.Fatalf("stale deploy_error resurfaced while offline: %+v", s)
		}
		m.mu.Lock()
		m.online = true
		m.mu.Unlock()
		if s := m.snapshot(); s.DeployError != "" {
			t.Fatalf("stale deploy_error resurfaced while online: %+v", s)
		}

		// Simulated Hub restart loading from the same store must also keep deploy_error empty.
		restarted := newMachine(h, m.cfg)
		restarted.online = true
		restarted.info = info
		restarted.nodeUpdateConnected(ctx, m.peer, info)
		if s := restarted.snapshot(); s.DeployError != "" {
			t.Fatalf("stale deploy_error resurfaced after hub restart: %+v", s)
		}
	})
}

func TestLinkedDeployLegacyNodeWithoutDrainCapabilityUsesCommitConfirmTimer(t *testing.T) {
	withShortDrainTimers(t, 180*time.Millisecond, 20*time.Millisecond, 500*time.Millisecond, 5*time.Second)
	h, m, n, bin := linkedDeployFixture(t, kindLink)
	h.opts.Store = newMemStore()
	// Legacy node: advertises NodeUpdate + NodeUpdateIdle, NOT NodeUpdateDrain.
	m.mu.Lock()
	m.info.Protocol = nodeproto.Version
	m.info.Capabilities = []string{nodeproto.Sessions, nodeproto.NodeUpdate, nodeproto.NodeUpdateIdle}
	m.mu.Unlock()
	n.stayOld = true

	run, err := m.startRPCDeploymentRun(nodePackage{Version: h.hubVersion(), path: bin}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	outcome := make(chan error, 1)
	go func() { outcome <- m.waitDeploy(ctx, run) }()

	waitMachineFast(t, m, 2*time.Second, func(s proto.Machine) bool { return s.DeployPhase == "confirming" })
	wantErr := i18n.Err("devices-deploy.not_confirmed").Error()
	select {
	case err := <-outcome:
		if err == nil || err.Error() != wantErr {
			t.Fatalf("legacy outcome = %v, want %q", err, wantErr)
		}
	case <-ctx.Done():
		t.Fatalf("legacy node confirmation did not time out: %v", ctx.Err())
	}
}
