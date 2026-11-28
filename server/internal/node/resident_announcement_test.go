package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestResidentReportsUpdatePreflightFailure(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	peer, manager := rpc.New("node", a), rpc.New("manager", b)
	failures := make(chan proto.NodeUpdatePreflightFailure, 1)
	manager.OnEvent = func(event string, params json.RawMessage) {
		var failure proto.NodeUpdatePreflightFailure
		if event == nodeproto.EventUpdatePreflightFailed && json.Unmarshal(params, &failure) == nil {
			failures <- failure
		}
	}
	go func() { _ = manager.Serve(ctx, b) }()
	reload := &ResidentReload{}
	reload.peer(peer, func() { _ = a.Close() })
	reload.ReportUpdatePreflightFailure(ctx, "update-one", errors.New("exit status 23"))
	select {
	case got := <-failures:
		want := proto.NodeUpdatePreflightFailure{ID: "update-one", Error: "exit status 23"}
		if got != want {
			t.Fatalf("failure = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("manager did not receive the preflight failure")
	}
	if leave, err := reload.enter(); err != nil {
		t.Fatalf("preflight failure suspended the resident: %v", err)
	} else {
		leave()
	}
}

func TestResidentReportsUpdateDrainingAndIncludesInInfoAndRestarting(t *testing.T) {
	if !slices.Contains(nodeproto.Announced, nodeproto.NodeUpdateDrain) {
		t.Fatalf("nodeproto.Announced missing %q", nodeproto.NodeUpdateDrain)
	}
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	peer, manager := rpc.New("node", a), rpc.New("manager", b)
	drainingCh := make(chan proto.NodeUpdateDraining, 1)
	restartingCh := make(chan proto.NodeUpdateDraining, 1)
	manager.OnEvent = func(event string, params json.RawMessage) {
		var payload proto.NodeUpdateDraining
		switch event {
		case nodeproto.EventUpdateDraining:
			if json.Unmarshal(params, &payload) == nil {
				drainingCh <- payload
			}
		case nodeproto.EventRestarting:
			if json.Unmarshal(params, &payload) == nil {
				restartingCh <- payload
			}
		}
	}
	manager.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "state.load" {
			return map[string]any{"restart_ready": true}, nil
		}
		return map[string]any{}, nil
	}
	go func() { _ = peer.Serve(ctx, a) }()
	go func() { _ = manager.Serve(ctx, b) }()

	reload := &ResidentReload{}
	reload.peer(peer, func() { _ = a.Close() })
	if got := reload.UpdateDraining(); got != nil {
		t.Fatalf("initial UpdateDraining = %+v, want nil", got)
	}
	reload.ReportUpdateDraining(ctx, "tx-drain-1", "v2.0.0")
	want := proto.NodeUpdateDraining{ID: "tx-drain-1", Target: "v2.0.0"}
	if got := reload.UpdateDraining(); got == nil || *got != want {
		t.Fatalf("UpdateDraining = %+v, want %+v", got, want)
	}
	select {
	case got := <-drainingCh:
		if got != want {
			t.Fatalf("draining event = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("manager did not receive node.update.draining")
	}

	// Verify Node.info reports UpdateDraining while draining, and clears it once cleared.
	n := &Node{reload: reload, Version: "v1.0.0"}
	info := n.info(ctx)
	if info.UpdateDraining == nil || *info.UpdateDraining != want {
		t.Fatalf("Node.info().UpdateDraining = %+v, want %+v", info.UpdateDraining, want)
	}
	if !slices.Contains(info.Capabilities, nodeproto.NodeUpdateDrain) {
		t.Fatalf("Node.info().Capabilities missing %q: %v", nodeproto.NodeUpdateDrain, info.Capabilities)
	}

	// Announce handover carries transaction id and target on node.restarting.
	if err := reload.Announce(ctx); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	select {
	case got := <-restartingCh:
		if got != want {
			t.Fatalf("restarting event = %+v, want %+v", got, want)
		}
	case <-ctx.Done():
		t.Fatal("manager did not receive node.restarting")
	}
	reload.Resume()
	reload.SetUpdateDraining("", "")
	if got := n.info(ctx).UpdateDraining; got != nil {
		t.Fatalf("UpdateDraining after clear = %+v, want nil", got)
	}
}

type heldReloadNotice struct {
	net.Conn
	started, release, finished chan struct{}
}

func (w *heldReloadNotice) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	defer close(w.finished)
	return w.Conn.Write(p)
}

func TestResidentAnnouncementAbortCannotArriveLate(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "writing", true: "queued-behind-old-event"}[queued], func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			writer := &heldReloadNotice{Conn: a, started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(writer.release) })
			peer, manager := rpc.New("n", writer), rpc.New("m", b)
			events := make(chan string, 4)
			manager.OnEvent = func(event string, _ json.RawMessage) { events <- event }
			served := make(chan struct{})
			go func() { defer close(served); _ = manager.Serve(ctx, b) }()
			reload := &ResidentReload{}
			closed := make(chan struct{})
			var disconnect sync.Once
			reload.peer(peer, func() {
				disconnect.Do(func() { _ = a.Close(); close(closed) })
			})
			if queued {
				go func() { _ = peer.Notify("older-event", nil) }()
				select {
				case <-writer.started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			announcement, abort := context.WithCancel(ctx)
			defer abort()
			result := make(chan error, 1)
			go func() { result <- reload.Announce(announcement) }()
			if !queued {
				select {
				case <-writer.started:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			abort()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("stalled announcement did not abort: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-closed:
			default:
				t.Fatal("unconfirmed control write left the physical connection alive")
			}
			reload.Resume()
			reload.CancelAnnouncement()
			release.Do(func() { close(writer.release) })
			select {
			case <-writer.finished:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case <-served:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case event := <-events:
				t.Fatalf("control message arrived after abort: %s", event)
			default:
			}
		})
	}
}
