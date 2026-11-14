package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestNodeAnnouncesMCPWaitCapability(t *testing.T) {
	if !slices.Contains((&Node{}).capabilities(), nodeproto.MCPWait) {
		t.Fatal("the node does not advertise nested MCP wait correlation")
	}
}

type mcpWaitRecordingConn struct {
	net.Conn
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (c *mcpWaitRecordingConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.Write(p)
}

func (c *mcpWaitRecordingConn) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.String()
}

func (c *mcpWaitRecordingConn) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buffer.Len()
}

func (c *mcpWaitRecordingConn) SetWriteDeadline(time.Time) error { return nil }

func TestMCPWaitUpdateHeldAcrossFinishWritesNothing(t *testing.T) {
	n := &Node{}
	n.hubMCPWait.Store(true)
	conn := &mcpWaitRecordingConn{}
	id, pending := n.beginMCPWait(conn)
	if pending == nil {
		t.Fatal("the opted-in call was not registered")
	}
	// Hold the looked-up pointer across the final write, exactly as an event that lost the race with finish does.
	n.mcpWaitMu.Lock()
	held := n.mcpWaits[id]
	n.mcpWaitMu.Unlock()
	if held != pending {
		t.Fatal("the registry did not return the pending call")
	}
	n.finishMCPWait(id, pending, proto.TextResult("done", false))
	held.update(proto.MCPWaitApproval)

	lines := strings.Split(strings.TrimSpace(conn.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("a held update followed the final response: %q", conn.String())
	}
	var final map[string]json.RawMessage
	if err := json.Unmarshal([]byte(lines[0]), &final); err != nil || final["content"] == nil || final["wait_kind"] != nil {
		t.Fatalf("the only line is not the final response: %s (%v)", lines[0], err)
	}
}

func TestNodeServeDispatchesMCPWait(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "pn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	n, err := New("test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.starts.close)
	n.logger.SetOutput(io.Discard)
	n.hubMCPWait.Store(true)
	conn := &mcpWaitRecordingConn{}
	id, pending := n.beginMCPWait(conn)
	if pending == nil {
		t.Fatal("the opted-in call was not registered")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	hub := rpc.New("h", h2nW)
	hub.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{}, nil
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- n.Serve(ctx, h2nR, n2hW) }()
	go func() { _ = hub.Serve(ctx, n2hR) }()
	defer func() {
		_ = n2hW.Close()
		_ = h2nW.Close()
		cancel()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Error("Node.Serve did not stop")
		}
	}()

	params, _ := json.Marshal(proto.MCPWaitEvent{CallID: id, Kind: proto.MCPWaitApproval})
	if err := hub.Notify(proto.EventMCPWait, json.RawMessage(params)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Node.Serve to dispatch mcp.wait", func() bool { return conn.Len() > 0 })
	if got := strings.TrimSpace(conn.String()); got != `{"wait_kind":"approval"}` {
		t.Fatalf("the real event dispatch wrote %q", got)
	}
}

func TestMCPWaitLocalLinesStopAtFinalResponse(t *testing.T) {
	n := &Node{}
	nodeSide, clientSide := net.Pipe()
	defer nodeSide.Close()
	defer clientSide.Close()
	if id, pending := n.beginMCPWait(nodeSide); id != "" || pending != nil {
		t.Fatal("a hub without mcp_wait_v1 must keep the single-response protocol")
	}
	n.hubMCPWait.Store(true)
	id, pending := n.beginMCPWait(nodeSide)
	if id == "" || pending == nil {
		t.Fatal("an opted-in call on a capable hub was not registered")
	}
	lines := make(chan string, 4)
	readDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(clientSide)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		readDone <- scanner.Err()
	}()
	next := func() string {
		t.Helper()
		select {
		case line := <-lines:
			return line
		case <-time.After(5 * time.Second):
			t.Fatal("local socket line did not arrive")
			return ""
		}
	}
	for _, invalidID := range []string{"not-a-uuid", strings.Repeat("x", 1<<20)} {
		params, _ := json.Marshal(proto.MCPWaitEvent{CallID: invalidID, Kind: proto.MCPWaitApproval})
		if !n.onMCPWait(proto.EventMCPWait, params) {
			t.Fatal("mcp.wait was not handled")
		}
		select {
		case line := <-lines:
			t.Fatalf("an invalid peer call id produced a local line: %s", line)
		default:
		}
	}
	writeUpdate := func(kind proto.MCPWaitKind) {
		t.Helper()
		params, _ := json.Marshal(struct {
			proto.MCPWaitEvent
			Question string `json:"question,omitempty"`
		}{MCPWaitEvent: proto.MCPWaitEvent{CallID: id, Kind: kind}, Question: "must not cross the local boundary"})
		if !n.onMCPWait(proto.EventMCPWait, params) {
			t.Fatal("mcp.wait was not handled")
		}
	}
	writeUpdate(proto.MCPWaitApproval)
	if got := next(); got != `{"wait_kind":"approval"}` {
		t.Fatalf("interim line = %s", got)
	}
	writeUpdate(proto.MCPWaitKind("future"))
	if got := next(); got != `{"wait_kind":"unknown"}` {
		t.Fatalf("unknown interim line = %s", got)
	}
	n.finishMCPWait(id, pending, proto.TextResult("done", false))
	var final map[string]json.RawMessage
	if err := json.Unmarshal([]byte(next()), &final); err != nil || final["wait_kind"] != nil || final["content"] == nil {
		t.Fatalf("final response = %v (%v)", final, err)
	}
	writeUpdate(proto.MCPWaitComputer) // late updates find no registered call and write nothing
	select {
	case line := <-lines:
		t.Fatalf("update followed the final response: %s", line)
	default:
	}
	n.mcpWaitMu.Lock()
	remaining := len(n.mcpWaits)
	n.mcpWaitMu.Unlock()
	if remaining != 0 {
		t.Fatalf("%d pending calls remained after the final response", remaining)
	}
}

func TestMCPWaitNonReadingClientDoesNotBlockHub(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.Dial("unix", path) // a suspended MCP process: no reads until the hub input is exhausted
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	nodeSide, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer nodeSide.Close()
	if err := nodeSide.(*net.UnixConn).SetWriteBuffer(4096); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.UnixConn).SetReadBuffer(4096); err != nil {
		t.Fatal(err)
	}

	n := &Node{}
	n.hubMCPWait.Store(true)
	id, pending := n.beginMCPWait(nodeSide)
	if pending == nil {
		t.Fatal("the opted-in call was not registered")
	}
	hubR, hubW := io.Pipe()
	n.peer = rpc.New("n", io.Discard)
	n.peer.OnEvent = func(event string, params json.RawMessage) {
		if !n.onMCPWait(event, params) {
			// The unrelated event stands for terminal input and stream traffic on the same read loop.
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() { serveDone <- n.peer.Serve(ctx, hubR) }()
	defer func() {
		_ = hubW.Close()
		_ = nodeSide.Close()
		select {
		case <-serveDone:
		case <-time.After(5 * time.Second):
			t.Error("the hub read loop did not stop")
		}
	}()

	params, err := json.Marshal(proto.MCPWaitEvent{CallID: id, Kind: proto.MCPWaitApproval})
	if err != nil {
		t.Fatal(err)
	}
	waitLine, err := json.Marshal(map[string]any{"event": proto.EventMCPWait, "params": json.RawMessage(params)})
	if err != nil {
		t.Fatal(err)
	}
	waitLine = append(waitLine, '\n')
	otherLine := []byte("{\"event\":\"terminal.input\",\"params\":{}}\n")
	const updates = 20000
	wrote := make(chan error, 1)
	go func() {
		for range updates {
			if _, err := hubW.Write(waitLine); err != nil {
				wrote <- err
				return
			}
			if _, err := hubW.Write(otherLine); err != nil {
				wrote <- err
				return
			}
		}
		wrote <- nil
	}()
	select {
	case err := <-wrote:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an interim update to the non-reading client stopped the hub read loop")
	}

	finished := make(chan struct{})
	go func() {
		n.finishMCPWait(id, pending, proto.TextResult("done", false))
		close(finished)
	}()
	scanner := bufio.NewScanner(client)
	interim := 0
	for {
		if !scanner.Scan() {
			t.Fatalf("the local socket ended before the final response: %v", scanner.Err())
		}
		var line map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("invalid local line %q: %v", scanner.Text(), err)
		}
		if line["content"] != nil {
			break
		}
		if line["wait_kind"] == nil {
			t.Fatalf("unexpected local line %q", scanner.Text())
		}
		interim++
	}
	if interim >= updates {
		t.Fatalf("all %d interim lines reached a client that stopped reading; a failed write did not stop later updates", updates)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the final response remained blocked after the client resumed")
	}
}

func TestMCPWaitSocketRequiresClientOptIn(t *testing.T) {
	for _, optedIn := range []bool{false, true} {
		t.Run(map[bool]string{false: "old client", true: "opted in"}[optedIn], func(t *testing.T) {
			n, _ := testNode(t)
			n.hubMCPWait.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			n2hR, n2hW := io.Pipe()
			h2nR, h2nW := io.Pipe()
			n.peer = rpc.New("n", n2hW)
			hub := rpc.New("h", h2nW)
			called := make(chan map[string]any, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				if method != "mcp.call" {
					return map[string]any{}, nil
				}
				var p map[string]any
				_ = json.Unmarshal(params, &p)
				called <- p
				select {
				case <-release:
					return proto.TextResult("done", false), nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			go func() { _ = hub.Serve(ctx, n2hR) }()
			go func() { _ = n.peer.Serve(ctx, h2nR) }()
			defer func() { _ = n2hW.Close(); _ = h2nW.Close() }()

			nodeSide, clientSide := net.Pipe()
			defer nodeSide.Close()
			defer clientSide.Close()
			if err := clientSide.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			go n.serveSock(context.Background(), nodeSide)
			request := map[string]any{"kind": "mcp.call", "name": "example", "args": map[string]any{}}
			if optedIn {
				request["wait_updates"] = true
			}
			line, _ := json.Marshal(request)
			if _, err := clientSide.Write(append(line, '\n')); err != nil {
				t.Fatal(err)
			}
			var forwarded map[string]any
			select {
			case forwarded = <-called:
			case <-time.After(5 * time.Second):
				t.Fatal("the socket call did not reach the hub")
			}
			callID, _ := forwarded["call_id"].(string)
			if !optedIn {
				if _, ok := forwarded["wait_updates"]; ok || callID != "" {
					t.Fatalf("an old client was opted into wait updates: %v", forwarded)
				}
				n.mcpWaitMu.Lock()
				registered := len(n.mcpWaits)
				n.mcpWaitMu.Unlock()
				if registered != 0 {
					t.Fatalf("an old client registered %d pending calls", registered)
				}
			} else if enabled, _ := forwarded["wait_updates"].(bool); !enabled || callID == "" {
				t.Fatalf("the opted-in call lost its correlators: %v", forwarded)
			}

			reader := bufio.NewReader(clientSide)
			readLine := func() map[string]json.RawMessage {
				t.Helper()
				line, err := reader.ReadBytes('\n')
				if err != nil {
					t.Fatal(err)
				}
				var decoded map[string]json.RawMessage
				if err := json.Unmarshal(line, &decoded); err != nil {
					t.Fatal(err)
				}
				return decoded
			}
			handled := make(chan bool, 1)
			if callID != "" {
				params, _ := json.Marshal(proto.MCPWaitEvent{CallID: callID, Kind: proto.MCPWaitApproval})
				go func() { handled <- n.onMCPWait(proto.EventMCPWait, params) }()
			}
			if optedIn {
				if first := readLine(); string(first["wait_kind"]) != `"approval"` {
					t.Fatalf("the opted-in client's first line = %v", first)
				}
			}
			unblock()
			final := readLine()
			if final["wait_kind"] != nil || final["content"] == nil {
				t.Fatalf("the client's final response = %v", final)
			}
			if callID != "" {
				select {
				case ok := <-handled:
					if !ok {
						t.Fatal("the wait update was not handled")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("the wait update did not return")
				}
			}
		})
	}
}

func TestLCUElicitForwardsOnlyAnOptedInCallID(t *testing.T) {
	n, _ := testNode(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	called := make(chan map[string]any, 3)
	hub.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "computer.elicit" {
			t.Fatalf("method = %q", method)
		}
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		called <- p
		return map[string]any{"action": "cancel"}, nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = n.peer.Serve(ctx, h2nR) }()
	defer func() { _ = n2hW.Close(); _ = h2nW.Close() }()

	for _, callID := range []string{"66666666-6666-4666-8666-666666666666", "", "not-a-uuid"} {
		callCtx := mcpwait.WithCall(ctx, callID)
		if _, err := n.elicitLCU(callCtx, "conversation", json.RawMessage(`{"message":"private question"}`)); err != nil {
			t.Fatal(err)
		}
		wantID := callID
		if !proto.ValidMCPWaitCallID(callID) {
			wantID = ""
		}
		select {
		case params := <-called:
			got, _ := params["call_id"].(string)
			if got != wantID {
				t.Fatalf("computer.elicit call ID = %q, want %q: %v", got, wantID, params)
			}
			if _, present := params["wait_machine"]; present {
				t.Fatalf("computer.elicit exposed an origin machine: %v", params)
			}
			wantFields := 2
			if wantID != "" {
				wantFields++
			}
			if len(params) != wantFields || params["conversation"] != "conversation" || params["params"] == nil {
				t.Fatalf("computer.elicit params = %v", params)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("computer.elicit did not reach the hub")
		}
	}
}

// mcpWaitFinalSignalConn reports when the final response enters its write, before the real socket can block it.
type mcpWaitFinalSignalConn struct {
	net.Conn
	once    sync.Once
	entered chan struct{}
}

func (c *mcpWaitFinalSignalConn) Write(p []byte) (int, error) {
	var line map[string]json.RawMessage
	if json.Unmarshal(p, &line) == nil && line["content"] != nil {
		c.once.Do(func() { close(c.entered) })
	}
	return c.Conn.Write(p)
}

func mcpWaitSocketPair(t *testing.T) (nodeSide, client *net.UnixConn) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	clientConn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientConn.Close() })
	serverConn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverConn.Close() })
	return serverConn.(*net.UnixConn), clientConn.(*net.UnixConn)
}

func TestMCPWaitUpdateDoesNotWaitBehindFinalWrite(t *testing.T) {
	nodeSide, client := mcpWaitSocketPair(t)
	conn := &mcpWaitFinalSignalConn{Conn: nodeSide, entered: make(chan struct{})}
	n := &Node{}
	n.hubMCPWait.Store(true)
	id, pending := n.beginMCPWait(conn)
	if pending == nil {
		t.Fatal("the opted-in call was not registered")
	}
	// Keep the pointer from an event lookup that won the race just before finish removed the call.
	n.mcpWaitMu.Lock()
	held := n.mcpWaits[id]
	n.mcpWaitMu.Unlock()

	// A large final response blocks until the client reads, as a screenshot-sized result can.
	big := proto.TextResult(string(make([]byte, 4<<20)), false)
	finished := make(chan struct{})
	go func() {
		n.finishMCPWait(id, pending, big)
		close(finished)
	}()
	select {
	case <-conn.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the final write did not start")
	}

	updated := make(chan struct{})
	go func() {
		held.update(proto.MCPWaitApproval)
		close(updated)
	}()
	blocked := false
	select {
	case <-updated:
	case <-time.After(2 * time.Second):
		blocked = true
	}

	// Drain the client: exactly one complete final line may follow any earlier interim lines.
	lines := 0
	scanner := bufio.NewScanner(client)
	scanner.Buffer(make([]byte, 0, 64<<10), 64<<20)
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Second))
	for scanner.Scan() {
		lines++
		var line map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("corrupt local line (%d bytes): %v", len(scanner.Bytes()), err)
		}
		if line["content"] != nil {
			break
		}
	}
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the final response did not complete after the client resumed")
	}
	select {
	case <-updated:
	case <-time.After(10 * time.Second):
		t.Fatal("the held update did not return after the final response completed")
	}
	if lines != 1 {
		t.Fatalf("%d local lines, want only the final response", lines)
	}
	if blocked {
		t.Fatal("an interim update waited behind the final write to a client that was not reading")
	}
}
