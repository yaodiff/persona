package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

type manualProgressTicker struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func newManualProgressTicker() *manualProgressTicker {
	return &manualProgressTicker{ch: make(chan time.Time, 32), stopped: make(chan struct{})}
}

func (t *manualProgressTicker) ticks() <-chan time.Time { return t.ch }
func (t *manualProgressTicker) Stop()                   { t.once.Do(func() { close(t.stopped) }) }
func (t *manualProgressTicker) beat()                   { t.ch <- time.Time{} }

// waitingCall is a real local node socket whose tool call waits until release, or until its caller hangs up. Its ticker
// is advanced by the test, so no assertion depends on a wall-clock sleep. Updates travel through nodeclient's interim
// line decoder exactly as they do in production.
type waitingCall struct {
	tick      *manualProgressTicker
	state     atomic.Pointer[mcpWaitState]
	requested atomic.Bool
	requests  chan map[string]any
	updates   chan proto.MCPWaitKind
	release   chan struct{}
	done      chan struct{}
	once      sync.Once
}

func waitingNode(t *testing.T) *waitingCall {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "pm") // a short path: a Unix socket's is bounded
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	path := nodeclient.SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	call := &waitingCall{tick: newManualProgressTicker(), requests: make(chan map[string]any, 1),
		updates: make(chan proto.MCPWaitKind), release: make(chan struct{}), done: make(chan struct{})}
	oldDial, oldTicker := dialNode, newProgressTicker
	newProgressTicker = func(time.Duration) progressTicker { return call.tick }
	dialNode = func(ctx context.Context, req any) ([]byte, error) {
		call.state.Store(mcpWaitFrom(ctx))
		return nodeclient.DialInterruptible(ctx, req)
	}
	go func() {
		defer close(call.done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var request map[string]any
		if json.Unmarshal(line, &request) != nil {
			return
		}
		call.requests <- request
		call.requested.Store(true)
		for {
			select {
			case kind := <-call.updates:
				line, _ := json.Marshal(map[string]any{"wait_kind": kind})
				if _, err := conn.Write(append(line, '\n')); err != nil {
					return
				}
			case <-call.release:
				_, _ = conn.Write([]byte(`{"content":[{"type":"text","text":"saved"}]}` + "\n"))
				return
			}
		}
	}()
	t.Cleanup(func() {
		dialNode, newProgressTicker = oldDial, oldTicker
		call.stop()
		_ = listener.Close()
		select {
		case <-call.done:
		case <-time.After(5 * time.Second):
			t.Error("the fake node socket did not stop")
		}
	})
	return call
}

func (c *waitingCall) stop() { c.once.Do(func() { close(c.release) }) }
func (c *waitingCall) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-c.tick.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("the progress ticker did not stop")
	}
}
func (c *waitingCall) ready() bool {
	return c.requested.Load()
}
func (c *waitingCall) request(t *testing.T) map[string]any {
	t.Helper()
	select {
	case request := <-c.requests:
		return request
	case <-time.After(5 * time.Second):
		t.Fatal("the MCP call did not reach the node socket")
		return nil
	}
}
func (c *waitingCall) set(t *testing.T, kind proto.MCPWaitKind) {
	t.Helper()
	select {
	case c.updates <- kind:
	case <-time.After(5 * time.Second):
		t.Fatal("the fake node did not accept a wait update")
	}
	want := proto.NormalizeMCPWaitKind(kind)
	waitFor(t, "the MCP receiver to apply "+string(want), func() bool {
		state := c.state.Load()
		return state != nil && state.current() == want
	})
}

// serve runs Serve on a pipe: send writes a line to it, out is what it wrote, end closes its input and waits.
func serve(t *testing.T) (send func(string), out *syncBuf, end func()) {
	t.Helper()
	in, w := io.Pipe()
	out = &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, out) }()
	send = func(s string) { _, _ = io.WriteString(w, s+"\n") }
	var once sync.Once
	end = func() {
		once.Do(func() {
			_ = w.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Serve did not stop")
			}
		})
	}
	t.Cleanup(end)
	return send, out, end
}

type progressLine struct {
	Method string `json:"method"`
	Params struct {
		ProgressToken json.RawMessage `json:"progressToken"`
		Progress      float64         `json:"progress"`
		Message       string          `json:"message"`
	} `json:"params"`
	ID json.RawMessage `json:"id"`
}

// lines decodes what Serve wrote, one JSON-RPC message per line.
func lines(t *testing.T, out string) []progressLine {
	t.Helper()
	var all []progressLine
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l == "" {
			continue
		}
		var p progressLine
		if err := json.Unmarshal([]byte(l), &p); err != nil {
			t.Fatalf("not JSON: %q: %v", l, err)
		}
		all = append(all, p)
	}
	return all
}

func progressCount(t *testing.T, out *syncBuf) int {
	n := 0
	for _, l := range lines(t, out.String()) {
		if l.Method == "notifications/progress" {
			n++
		}
	}
	return n
}

func progressMessages(t *testing.T, out *syncBuf) []string {
	var messages []string
	for _, l := range lines(t, out.String()) {
		if l.Method == "notifications/progress" {
			messages = append(messages, l.Params.Message)
		}
	}
	return messages
}

func checkProgressParams(t *testing.T, out *syncBuf) {
	t.Helper()
	allowed := map[string]bool{
		"Waiting for your approval": true,
		"Waiting for your answer":   true,
		"Waiting for the computer":  true,
		"Still waiting":             true,
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var raw struct {
			Method string                     `json:"method"`
			Params map[string]json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil || raw.Method != "notifications/progress" {
			continue
		}
		if len(raw.Params) != 3 || raw.Params["progressToken"] == nil || raw.Params["progress"] == nil || raw.Params["message"] == nil {
			t.Fatalf("progress params must be exactly progressToken, progress and message: %s", line)
		}
		var message string
		if err := json.Unmarshal(raw.Params["message"], &message); err != nil || !allowed[message] {
			t.Fatalf("progress message is not one of the four fixed strings: %s", line)
		}
	}
}

// A tool call that waits longer than the client's idle limit stays alive under the token the client gave, with a
// growing counter and no progress after its final answer (#1498). Without a live update the fixed fallback is used.
func TestServeSendsProgressWhileACallWaits(t *testing.T) {
	call := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"phone_reminders_create","arguments":{},"_meta":{"progressToken":"tok-7"}}}`)
	for range 3 {
		call.tick.beat()
	}
	waitFor(t, "three progress lines", func() bool { return progressCount(t, out) >= 3 })
	call.tick.beat() // already due when the call ends: it may precede the answer, but may never follow it
	call.stop()
	waitFor(t, "the answer", func() bool {
		for _, line := range lines(t, out.String()) {
			if string(line.ID) == "7" {
				return true
			}
		}
		return false
	})
	call.waitStopped(t)
	send(`{"jsonrpc":"2.0","id":9,"method":"ping"}`) // a later response fences every write the tool goroutine could still make
	waitFor(t, "the post-answer ping", func() bool {
		for _, line := range lines(t, out.String()) {
			if string(line.ID) == "9" {
				return true
			}
		}
		return false
	})
	end()

	all := lines(t, out.String())
	result := -1
	for i, line := range all {
		if string(line.ID) == "7" {
			result = i
			break
		}
	}
	if result < 0 || !strings.Contains(out.String(), `"saved"`) {
		t.Fatalf("the tool result is missing: %s", out.String())
	}
	before, after := all[:result], all[result+1:]
	if len(before) != 3 && len(before) != 4 {
		t.Fatalf("the queued beat produced %d pre-result lines, want three or four: %s", len(before), out.String())
	}
	if len(after) != 1 || string(after[0].ID) != "9" || after[0].Method != "" {
		t.Fatalf("anything other than the ping followed the result: %s", out.String())
	}
	prev := 0.0
	for _, l := range before {
		if l.Method != "notifications/progress" || string(l.Params.ProgressToken) != `"tok-7"` || l.ID != nil {
			t.Fatalf("not a progress line for tok-7: %+v", l)
		}
		if l.Params.Progress <= prev {
			t.Fatalf("progress %v after %v: it must grow", l.Params.Progress, prev)
		}
		if l.Params.Message != "Still waiting" {
			t.Fatalf("progress message %q, want the fixed fallback %q", l.Params.Message, "Still waiting")
		}
		prev = l.Params.Progress
	}
	checkProgressParams(t, out)
}

// Each live enum changes only the fixed message on the next beat. Approval moving on to execution is visible as
// computer, and an unrecognized value conservatively returns to the fallback without echoing that value.
func TestServeProgressMessagesFollowWaitKind(t *testing.T) {
	call := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"ask_user","arguments":{},"_meta":{"progressToken":"tok-8"}}}`)
	waitFor(t, "the node call", call.ready)
	if optedIn, _ := call.request(t)["wait_updates"].(bool); !optedIn {
		t.Fatal("a token-bearing MCP call did not opt into wait updates")
	}
	steps := []struct {
		kind proto.MCPWaitKind
		want string
	}{
		{proto.MCPWaitApproval, "Waiting for your approval"},
		{proto.MCPWaitAnswer, "Waiting for your answer"},
		{proto.MCPWaitComputer, "Waiting for the computer"},
		{proto.MCPWaitKind("a future value with text that must not be echoed"), "Still waiting"},
	}
	for i, step := range steps {
		call.set(t, step.kind)
		call.tick.beat()
		waitFor(t, step.want, func() bool {
			got := progressMessages(t, out)
			return len(got) == i+1 && got[i] == step.want
		})
	}
	call.stop()
	waitFor(t, "the answer", func() bool { return strings.Contains(out.String(), `"id":8`) })
	end()
	checkProgressParams(t, out)
	if strings.Contains(out.String(), "future value") {
		t.Fatalf("an unknown enum value was echoed: %s", out.String())
	}
}

// A call without a token gets no progress (MCP: progress only for a token the client asked with), and neither does
// any other request.
func TestServeSendsNoProgressWithoutAToken(t *testing.T) {
	call := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{}}}`)
	waitFor(t, "the tokenless node call", call.ready)
	if _, optedIn := call.request(t)["wait_updates"]; optedIn {
		t.Fatal("a call without a progress token opted into wait updates")
	}
	for range 3 {
		call.tick.beat()
	}
	call.stop()
	waitFor(t, "the answer", func() bool { return strings.Contains(out.String(), `"id":1`) })
	end()
	if n := progressCount(t, out); n != 0 {
		t.Fatalf("%d progress lines without a token: %s", n, out.String())
	}
}

// A call the client cancels stops its progress with its answer: nothing more is written for it.
func TestServeStopsProgressWhenACallIsCancelled(t *testing.T) {
	call := waitingNode(t)
	send, out, end := serve(t)
	send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"ask_user","arguments":{},"_meta":{"progressToken":42}}}`)
	call.tick.beat()
	waitFor(t, "progress", func() bool { return progressCount(t, out) >= 1 })
	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3,"reason":"interrupted"}}`)
	send(`{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	waitFor(t, "ping", func() bool { return strings.Contains(out.String(), `"id":4`) })
	call.waitStopped(t)
	n := progressCount(t, out)
	call.tick.beat()
	end()
	if m := progressCount(t, out); m != n {
		t.Fatalf("progress went on after the cancel: %d, then %d", n, m)
	}
	if strings.Contains(out.String(), `"id":3`) {
		t.Fatalf("a cancelled call was answered: %s", out.String())
	}
	for _, l := range lines(t, out.String()) {
		if l.Method == "notifications/progress" && string(l.Params.ProgressToken) != "42" {
			t.Fatalf("progress token %s, want the number 42 as given", l.Params.ProgressToken)
		}
	}
}

func TestProgressToken(t *testing.T) {
	cases := map[string]string{
		`{"name":"x","_meta":{"progressToken":"a1"}}`: `"a1"`,
		`{"name":"x","_meta":{"progressToken":17}}`:   `17`,
		`{"name":"x","_meta":{"progressToken":null}}`: ``,
		`{"name":"x","_meta":{"progressToken":{}}}`:   ``,
		`{"name":"x","_meta":{"progressToken":true}}`: ``,
		`{"name":"x","_meta":{}}`:                     ``,
		`{"name":"x"}`:                                ``,
		`"not an object"`:                             ``,
	}
	for params, want := range cases {
		got := progressToken(request{Method: "tools/call", Params: json.RawMessage(params)})
		if string(got) != want {
			t.Errorf("%s: token %q, want %q", params, got, want)
		}
	}
	if got := progressToken(request{Method: "tools/list", Params: json.RawMessage(`{"_meta":{"progressToken":"a"}}`)}); got != nil {
		t.Errorf("tools/list: token %q, want none", got)
	}
}

// progressHeldContext holds a beat after it has decided to send and before it writes its notification.
type progressHeldContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
}

func (c *progressHeldContext) Value(key any) any {
	if _, ok := key.(mcpWaitContextKey); ok {
		select {
		case c.entered <- struct{}{}:
		default:
		}
		<-c.release
	}
	return c.Context.Value(key)
}

func TestKeepAliveStopWaitsForBeatBeingSent(t *testing.T) {
	tick := newManualProgressTicker()
	old := newProgressTicker
	newProgressTicker = func(time.Duration) progressTicker { return tick }
	t.Cleanup(func() { newProgressTicker = old })

	ctx := &progressHeldContext{Context: context.Background(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	stopped := make(chan struct{})
	sent := make(chan bool, 1)
	stop := keepAlive(ctx, json.RawMessage(`"tok"`), func(notification) {
		select {
		case <-stopped:
			sent <- true
		default:
			sent <- false
		}
	})
	tick.beat()
	select {
	case <-ctx.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the beat was not taken")
	}
	go func() {
		stop()
		close(stopped)
	}()
	// A correct stop cannot return before the held beat is released; a broken one can close stopped here.
	select {
	case <-stopped:
	case <-time.After(300 * time.Millisecond):
	}
	close(ctx.release)
	select {
	case afterStop := <-sent:
		if afterStop {
			t.Fatal("a progress notification was written after stop returned: it would follow the final result")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the held beat was never sent")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return")
	}
}
