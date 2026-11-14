package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// sharedHost is a resident node's runtime host on a short temp socket, listening until the test ends.
func sharedHost(t *testing.T) *RuntimeHost {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jrh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rh := NewRuntimeHost("test", filepath.Join(dir, "runtime.sock"))
	rh.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := rh.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	return rh
}

func (f *fakeRuntime) hubStates() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.hubs)
}

// A resident node's runtimes connect before any hub does and stay connected while the hub reconnects: the next
// connection lists their sessions in its first list, and drives them, with no reconnect on the runtime's side. They
// hear the hub come and go.
func TestRuntimeStaysAcrossHubConnections(t *testing.T) {
	rh := sharedHost(t)
	rt := dialRuntimeAt(t, rh.path, dshHello()) // no hub yet
	if hub, _ := rt.helloR["hub"].(bool); hub {
		t.Fatal("hello said a hub is connected before one was")
	}
	if f, _ := json.Marshal(rt.helloR["features"]); string(f) != `["prefer_running","resume_argv","handover","transcript","launchers","start_params","modes","resume","codex_thread","unlisted_transcripts","workspaces"]` {
		t.Fatalf("features %s", f)
	}
	_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-1", "status": "done"}}})
	waitFor(t, "the list kept without a hub", func() bool { return len(rh.reg.appendSessions(nil)) == 1 })

	a, _ := runtimeNodeOn(t, rh)
	if _, ok := listed(t, a)["session-1"]; !ok {
		t.Fatal("the first hub connection does not list what the runtime said before it")
	}
	waitFor(t, "told a hub is here", func() bool { return slices.Equal(rt.hubStates(), []bool{true}) })
	if _, err := nodeCall(t, a, "session.watch", map[string]any{"session_id": "session-1", "since_seq": 3}); err != nil {
		t.Fatal(err)
	}

	rh.detach(a) // that connection ends
	waitFor(t, "told the hub left", func() bool { return slices.Equal(rt.hubStates(), []bool{true, false}) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := rt.peer.Call(ctx, "interaction.create", map[string]any{"interaction": map[string]any{"kind": "permission", "session_id": "session-1"}}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "node_gone" {
		t.Fatalf("an ask without a hub: %v (want node_gone, so the runtime asks again once one is back)", err)
	}

	b, _ := runtimeNodeOn(t, rh) // the hub reconnected
	if _, ok := listed(t, b)["session-1"]; !ok {
		t.Fatal("the next connection does not list the runtime's session")
	}
	rh.reg.mu.Lock()
	watching := len(rh.reg.watching)
	rh.reg.mu.Unlock()
	if watching != 0 {
		t.Fatal("the last connection's watches carried over (the hub watches afresh)")
	}
	if _, err := nodeCall(t, b, "session.send", map[string]any{"session_id": "session-1", "text": "back", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	if len(rt.got("session.send")) != 1 {
		t.Fatal("the same runtime connection does not get the next connection's send")
	}
	waitFor(t, "told a hub is here again", func() bool { return slices.Equal(rt.hubStates(), []bool{true, false, true}) })

	// A late end of the old connection (after the new one attached) changes nothing.
	rh.detach(a)
	time.Sleep(50 * time.Millisecond)
	if rh.current() != b || len(rt.hubStates()) != 3 {
		t.Fatalf("a stale detach took over: current %p, hub pushes %v", rh.current(), rt.hubStates())
	}
}

// A node serving its one connection itself (no resident node) still opens and closes its own runtime socket with it.
func TestRuntimeOwnSocketWithoutResidentNode(t *testing.T) {
	n, _ := runtimeNode(t)
	if n.runtimeHost == nil || n.runtimeHost.reg != n.runtimes || n.runtimeHost.current() != n {
		t.Fatal("no host of its own")
	}
	rt := dialRuntime(t, n, dshHello())
	if hub, _ := rt.helloR["hub"].(bool); !hub {
		t.Fatal("hello on a node serving a hub said no hub")
	}
}
