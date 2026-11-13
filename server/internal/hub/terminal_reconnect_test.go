package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A phone takes over a terminal while the computer's node is replaced (a deploy restarts it): the terminal.watch on its
// way goes with the old connection. The app must hear that at once, never sit on "Taking over…" with nothing coming,
// and its next subscribe must take the terminal over on the new connection (#6327).

// termConn is one connection of a node to the hub: the hub's end of it, and drop, which closes it as a node that exits
// does. watch answers terminal.watch (nil: control at the size asked, at once); everything else answers empty.
func termConn(t *testing.T, watch func() (any, error)) (hubSide *rpc.Peer, drop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide = rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "terminal.watch" {
			return map[string]any{}, nil
		}
		if watch != nil {
			return watch()
		}
		var p struct {
			Control *termCtl `json:"control"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Control == nil {
			return map[string]any{"cols": 172, "rows": 45, "control": false}, nil
		}
		return map[string]any{"cols": p.Control.Cols, "rows": p.Control.Rows, "control": true}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	drop = func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() }
	t.Cleanup(drop)
	return hubSide, drop
}

// hangingWatch is a node that got terminal.watch and never answers it (the process is about to go): asked hears each
// one; the handlers end with the test.
func hangingWatch(t *testing.T) (watch func() (any, error), asked chan struct{}) {
	asked = make(chan struct{}, 4)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	return func() (any, error) {
		asked <- struct{}{}
		<-release
		return nil, errors.New("too late")
	}, asked
}

func termMachine() (*Hub, *Machine) {
	h := &Hub{byID: map[string]*Machine{}, logger: log.New(io.Discard, "", 0)}
	m := newMachine(h, MachineConfig{ID: "m1", Name: "m1"})
	h.byID["m1"] = m
	return h, m
}

func (m *Machine) swapPeerForTest(p *rpc.Peer) {
	m.mu.Lock()
	m.peer = p
	m.mu.Unlock()
}

func awaitWatch(t *testing.T, asked chan struct{}) {
	t.Helper()
	select {
	case <-asked:
	case <-time.After(3 * time.Second):
		t.Fatal("the node was never asked to watch the terminal")
	}
}

// awaitTerminalError waits for the `terminal` message that tells the app its stream ended, and returns its error.
func awaitTerminalError(t *testing.T, c *client) string {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case b := <-c.out:
			var m struct {
				Type  string `json:"type"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(b, &m)
			if m.Type == "terminal" && m.Error != "" {
				return m.Error
			}
		case <-deadline:
			t.Fatal("the app was never told the takeover went with the old connection: it would wait on Taking over… forever")
			return ""
		}
	}
}

// The old connection closes with the watch on its way: the call fails at once, the app is told, and subscribing again
// takes the terminal over on the node's new connection.
func TestTerminalTakeoverWhenTheNodeDropsMidway(t *testing.T) {
	h, m := termMachine()
	watch, asked := hangingWatch(t)
	old, dropOld := termConn(t, watch)
	m.swapPeerForTest(old)
	a := &client{out: make(chan []byte, 64)}
	h.terminalSubscribe(context.Background(), a, "m1", "pane:w1:p3", "", 0, &termCtl{44, 30}, false)
	awaitWatch(t, asked)

	dropOld()
	if why := awaitTerminalError(t, a); why == "" {
		t.Fatal("no reason")
	}

	fresh, _ := termConn(t, nil)
	m.swapPeerForTest(fresh)
	h.terminalSubscribe(context.Background(), a, "m1", "pane:w1:p3", "", 0, &termCtl{44, 30}, false)
	awaitStates(t, a, "active")
}

// The hub hears that the node connected again before the old connection is seen to close (its watch still hangs there):
// the app is told at once, and its next takeover goes to the new connection now, not after the old call's 20-second
// deadline, which held the session's call lock.
func TestTerminalTakeoverWhenTheNodeIsReplacedMidway(t *testing.T) {
	h, m := termMachine()
	watch, asked := hangingWatch(t)
	old, _ := termConn(t, watch)
	m.swapPeerForTest(old)
	a := &client{out: make(chan []byte, 64)}
	h.terminalSubscribe(context.Background(), a, "m1", "pane:w1:p3", "", 0, &termCtl{44, 30}, false)
	awaitWatch(t, asked)

	fresh, _ := termConn(t, nil)
	m.swapPeerForTest(fresh)
	h.terminalsEnded("m1", "the computer connected again") // what connectionEnded does with its terminals
	if why := awaitTerminalError(t, a); why != "the computer connected again" {
		t.Fatalf("told %q", why)
	}

	h.terminalSubscribe(context.Background(), a, "m1", "pane:w1:p3", "", 0, &termCtl{44, 30}, false)
	awaitStates(t, a, "active") // within its 3 seconds
}
