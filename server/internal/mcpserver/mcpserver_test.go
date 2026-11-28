package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
)

func listRequest() request {
	return request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"}
}

// fakeNode answers the n-th dial (from 1) with dial(n).
func fakeNode(t *testing.T, dial func(n int) ([]byte, error)) *int {
	t.Helper()
	oldDial, oldEvery := dialNode, retryEvery
	t.Cleanup(func() { dialNode, retryEvery = oldDial, oldEvery })
	retryEvery = time.Millisecond
	n := 0
	dialNode = func(context.Context, any) ([]byte, error) { n++; return dial(n) }
	return &n
}

func shortCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

func TestToolsListWithoutNodeIsAnError(t *testing.T) {
	n := fakeNode(t, func(int) ([]byte, error) { return nil, errors.New("dial unix node.sock: no such file or directory") })
	res, rerr := handle(shortCtx(t), "test", listRequest())
	if rerr == nil {
		t.Fatalf("no node: want an error, got %+v", res)
	}
	if !strings.Contains(rerr.Message, "no such file") {
		t.Fatalf("error should say why: %q", rerr.Message)
	}
	if *n < 2 {
		t.Fatalf("dialed %d times, want retries", *n)
	}
}

func TestToolsListNodeErrorIsAnError(t *testing.T) {
	fakeNode(t, func(int) ([]byte, error) { return []byte(`{"tools":[],"error":"hub not connected"}`), nil })
	_, rerr := handle(shortCtx(t), "test", listRequest())
	if rerr == nil || !strings.Contains(rerr.Message, "hub not connected") {
		t.Fatalf("node reported an error: want it passed on, got %+v", rerr)
	}
}

func TestToolsListRetriesUntilTheNodeIsUp(t *testing.T) {
	fakeNode(t, func(n int) ([]byte, error) {
		switch {
		case n == 1:
			return nil, errors.New("dial unix node.sock: connection refused")
		case n == 2:
			return []byte(`{"tools":[],"error":"hub not connected"}`), nil
		}
		return []byte(`{"tools":[{"name":"phone_health_summary","description":"health"}]}`), nil
	})
	res, rerr := handle(context.Background(), "test", listRequest())
	if rerr != nil {
		t.Fatalf("unexpected error %+v", rerr)
	}
	tools := res.(map[string]any)["tools"].([]map[string]any)
	if len(tools) != 1 || tools[0]["name"] != "phone_health_summary" || tools[0]["inputSchema"] == nil {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestToolsListAssistantToolsAreAlwaysLoaded(t *testing.T) {
	t.Setenv("PERSONA_ASSISTANT", "1")
	fakeNode(t, func(int) ([]byte, error) { return []byte(`{"tools":[{"name":"ask_user","description":"ask"}]}`), nil })
	res, rerr := handle(context.Background(), "test", listRequest())
	if rerr != nil {
		t.Fatal(rerr)
	}
	tools := res.(map[string]any)["tools"].([]map[string]any)
	meta, ok := tools[0]["_meta"].(map[string]any)
	if !ok || meta["anthropic/alwaysLoad"] != true {
		t.Fatalf("assistant tool lacks alwaysLoad metadata: %+v", tools[0])
	}
}

func TestToolsListAgentToolsHaveNoAlwaysLoadMarker(t *testing.T) {
	t.Setenv("PERSONA_ASSISTANT", "0")
	fakeNode(t, func(int) ([]byte, error) {
		return []byte(`{"tools":[{"name":"phone_health_summary","description":"health"}]}`), nil
	})
	res, rerr := handle(context.Background(), "test", listRequest())
	if rerr != nil {
		t.Fatal(rerr)
	}
	tools := res.(map[string]any)["tools"].([]map[string]any)
	if _, present := tools[0]["_meta"]; present {
		t.Fatalf("a non-assistant tool unexpectedly carries metadata: %+v", tools[0])
	}
}

func TestToolsListEmptyFromAHealthyNodeIsNotAnError(t *testing.T) {
	n := fakeNode(t, func(int) ([]byte, error) { return []byte(`{"tools":[]}`), nil })
	res, rerr := handle(shortCtx(t), "test", listRequest())
	if rerr != nil {
		t.Fatalf("unexpected error %+v", rerr)
	}
	if tools := res.(map[string]any)["tools"].([]map[string]any); len(tools) != 0 || *n != 1 {
		t.Fatalf("tools = %+v after %d dials", tools, *n)
	}
}

func callRequest() request {
	return request{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/call", Params: json.RawMessage(`{"name":"notify_user","arguments":{}}`)}
}

// A tool call made while the resident node's socket is gone for a hub restart waits for it; one the node received
// and dropped is not sent again.
func TestToolsCallWaitsForTheNodeSocket(t *testing.T) {
	n := fakeNode(t, func(n int) ([]byte, error) {
		if n < 3 {
			return nil, fmt.Errorf("%w: dial unix node.sock: no such file or directory", nodeclient.ErrNoNode)
		}
		return []byte(`{"content":[{"type":"text","text":"sent"}]}`), nil
	})
	res, rerr := handle(context.Background(), "test", callRequest())
	if rerr != nil || *n != 3 || !strings.Contains(fmt.Sprint(res), "sent") {
		t.Fatalf("call across the gap: %+v %+v after %d dials", res, rerr, *n)
	}
	n = fakeNode(t, func(int) ([]byte, error) { return nil, errors.New("node closed connection: EOF") })
	res, _ = handle(context.Background(), "test", callRequest())
	if *n != 1 || !strings.Contains(fmt.Sprint(res), "may have run") {
		t.Fatalf("a call the node may have run was sent %d times: %+v", *n, res)
	}
}

// A failed tool call tells the model whether the tool may have run: only a call nothing received is safe to retry,
// so mail_send or agent_send are not sent twice after a dropped connection or a timeout.
func TestToolsCallFailureSaysWhetherTheToolMayHaveRun(t *testing.T) {
	oldRedial := callRedialFor
	t.Cleanup(func() { callRedialFor = oldRedial })
	callRedialFor = 0
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)
	for _, tc := range []struct {
		name      string
		ctx       context.Context
		dial      func(int) ([]byte, error)
		want, not []string
	}{
		{"no node", context.Background(),
			func(int) ([]byte, error) {
				return nil, fmt.Errorf("%w: dial unix node.sock: connection refused", nodeclient.ErrNoNode)
			},
			[]string{"not reachable", "was not run", "connection refused"}, []string{"may have run"}},
		{"connection lost after sending", context.Background(),
			func(int) ([]byte, error) { return nil, errors.New("node closed connection: EOF") },
			[]string{"after sending the call", "may have run", "check", "EOF"}, []string{"not reachable"}},
		{"deadline", expired,
			func(int) ([]byte, error) { return nil, errors.New("node closed connection: read unix: i/o timeout") },
			[]string{"timed out", "may still have run", "check"}, []string{"not reachable"}},
		{"non-JSON reply", context.Background(),
			func(int) ([]byte, error) { return []byte("panic: oops\n"), nil },
			[]string{"bad response from Persona node", "invalid character"}, []string{"not reachable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeNode(t, tc.dial)
			res, rerr := handle(tc.ctx, "test", callRequest())
			if rerr != nil {
				t.Fatalf("unexpected rpc error %+v", rerr)
			}
			r, ok := res.(map[string]any)
			if !ok || r["isError"] != true {
				t.Fatalf("isError not set: %#v", res)
			}
			text := fmt.Sprint(r["content"])
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Errorf("text %q lacks %q", text, w)
				}
			}
			for _, w := range tc.not {
				if strings.Contains(text, w) {
					t.Errorf("text %q contains %q", text, w)
				}
			}
		})
	}
}

// syncBuf is an io.Writer the test reads while Serve writes.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// A tool call waiting for the phone holds no other request back; one Claude Code cancels (the user stopped the turn)
// hangs up on the node, so its card comes off the phone, and gets no answer.
func TestServeRunsCallsAtOnceAndCancelsThem(t *testing.T) {
	old := dialNode
	t.Cleanup(func() { dialNode = old })
	var mu sync.Mutex
	hungUp := map[string]bool{}
	dialNode = func(ctx context.Context, req any) ([]byte, error) {
		name, _ := req.(map[string]any)["name"].(string)
		if name == "mail_send" { // waits for the phone until its caller goes away
			<-ctx.Done()
			mu.Lock()
			hungUp[name] = true
			mu.Unlock()
			return nil, ctx.Err()
		}
		return []byte(`{"content":[{"type":"text","text":"here"}]}`), nil
	}
	in, w := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, out) }()
	send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

	send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"mail_send","arguments":{}}}`)
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"phone_location_current","arguments":{}}}`)
	waitFor(t, "the second call answered while the first waits", func() bool { return strings.Contains(out.String(), `"id":2`) })
	if strings.Contains(out.String(), `"id":1`) {
		t.Fatalf("the waiting call answered: %s", out.String())
	}

	send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"interrupted"}}`)
	waitFor(t, "the cancelled call hung up on the node", func() bool { mu.Lock(); defer mu.Unlock(); return hungUp["mail_send"] })
	send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	waitFor(t, "ping", func() bool { return strings.Contains(out.String(), `"id":3`) })
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"id":1`) {
		t.Fatalf("a cancelled call was answered: %s", out.String())
	}
}

// The client going away (stdin closed) ends what still waits: no card is left on the phone for a dead process.
func TestServeEndsCallsWhenTheClientGoes(t *testing.T) {
	old := dialNode
	t.Cleanup(func() { dialNode = old })
	ended := make(chan struct{})
	dialNode = func(ctx context.Context, req any) ([]byte, error) {
		<-ctx.Done()
		close(ended)
		return nil, ctx.Err()
	}
	in := strings.NewReader(`{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"ask_user","arguments":{}}}` + "\n")
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, &syncBuf{}) }()
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("the call kept waiting after the client went away")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A line that is not a JSON-RPC request gets a JSON-RPC error instead of silence, so the client learns why instead
// of waiting for its own timeout: -32700 when it is not JSON, -32600 when it is JSON but not a request (echoing a usable
// id). Blank lines and notifications stay unanswered, and the server keeps answering what follows.
func TestServeAnswersMalformedLines(t *testing.T) {
	in, w := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, out) }()
	send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

	send(`not json`)
	send(`{"jsonrpc":"2.0","id":4,"method":"ping"`) // cut short
	send(`[1,2]`)
	send(`null`)
	send(`{"jsonrpc":"2.0","id":9}`)
	send(`{"jsonrpc":"2.0","id":"a","method":7}`)
	send(`{"jsonrpc":"2.0","id":{},"method":""}`)
	send(``)
	send(`{"jsonrpc":"2.0"}`)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	waitFor(t, "ping after the malformed lines", func() bool { return strings.Contains(out.String(), `"id":3`) })
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	got := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var r struct {
			ID    json.RawMessage `json:"id"`
			Error *rpcError       `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("unparsable answer %q: %v", line, err)
		}
		code := 0
		if r.Error != nil {
			code = r.Error.Code
		}
		got[fmt.Sprintf("%d %s", code, r.ID)]++
	}
	want := map[string]int{"-32700 null": 2, "-32600 null": 3, "-32600 9": 1, `-32600 "a"`: 1, "0 3": 1}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("answers (code id: count) = %v, want %v:\n%s", got, want, out.String())
	}
}

func TestIDKey(t *testing.T) {
	if idKey(json.RawMessage(` 7`)) != idKey(json.RawMessage(`7`)) || idKey(json.RawMessage(`"7"`)) == idKey(json.RawMessage(`7`)) {
		t.Fatal("ids are keyed by value, a string apart from a number")
	}
}

// A tool call without a name, or with arguments that are not a JSON object, is invalid params (-32602) and never
// reaches the node: the handlers behind it would run on zero values.
func TestToolsCallRejectsInvalidParams(t *testing.T) {
	for _, params := range []string{
		`{"arguments":{}}`,
		`{"name":"","arguments":{}}`,
		`{"name":"  ","arguments":{}}`,
		`{"name":"conversations_list","arguments":[1]}`,
		`{"name":"conversations_list","arguments":"q"}`,
		`{"name":"conversations_list","arguments":3}`,
		`{"name":"conversations_list","arguments":true}`,
		`{"name":"conversations_list","arguments": [ ]}`,
	} {
		n := fakeNode(t, func(int) ([]byte, error) { return []byte(`{"content":[{"type":"text","text":"ran"}]}`), nil })
		req := request{JSONRPC: "2.0", ID: json.RawMessage(`4`), Method: "tools/call", Params: json.RawMessage(params)}
		res, rerr := handle(context.Background(), "test", req)
		if rerr == nil || rerr.Code != -32602 || *n != 0 {
			t.Fatalf("%s: res %+v, err %+v after %d dials", params, res, rerr, *n)
		}
	}
}

// Absent or null arguments reach the node as an empty object; an object is passed on as sent.
func TestToolsCallSendsArgumentsAsAnObject(t *testing.T) {
	for params, want := range map[string]string{
		`{"name":"notify_user"}`:                            `{}`,
		`{"name":"notify_user","arguments":null}`:           `{}`,
		`{"name":"notify_user","arguments": {"text":"hi"}}`: `{"text":"hi"}`,
	} {
		old := dialNode
		var got string
		dialNode = func(_ context.Context, req any) ([]byte, error) {
			b, _ := json.Marshal(req.(map[string]any)["args"])
			got = string(b)
			return []byte(`{"content":[{"type":"text","text":"sent"}]}`), nil
		}
		req := request{JSONRPC: "2.0", ID: json.RawMessage(`5`), Method: "tools/call", Params: json.RawMessage(params)}
		_, rerr := handle(context.Background(), "test", req)
		dialNode = old
		if rerr != nil || got != want {
			t.Fatalf("%s: node got args %s (want %s), err %+v", params, got, want, rerr)
		}
	}
}

// Over the real request path the client gets a -32602 error answer for its id.
func TestToolsCallInvalidParamsOverServe(t *testing.T) {
	n := fakeNode(t, func(int) ([]byte, error) { return []byte(`{"content":[{"type":"text","text":"ran"}]}`), nil })
	in, w := io.Pipe()
	out := &syncBuf{}
	done := make(chan error, 1)
	go func() { done <- Serve(context.Background(), "test", in, out) }()
	// stdin stays open until the answer is out: Serve does not answer what runs when the client goes away.
	_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"conversations_list","arguments":[1]}}`+"\n")
	waitFor(t, "the answer", func() bool { return strings.Contains(out.String(), `"id":7`) })
	_ = w.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var resp struct {
		ID    int       `json:"id"`
		Error *rpcError `json:"error"`
	}
	if err := json.Unmarshal([]byte(out.String()), &resp); err != nil {
		t.Fatalf("answer %q: %v", out.String(), err)
	}
	if resp.ID != 7 || resp.Error == nil || resp.Error.Code != -32602 || *n != 0 {
		t.Fatalf("answer %s after %d dials", out.String(), *n)
	}
}

type testProgressTicker struct {
	ch      chan time.Time
	stopped chan struct{}
	once    sync.Once
}

func (m *testProgressTicker) ticks() <-chan time.Time { return m.ch }
func (m *testProgressTicker) Stop()                   { m.once.Do(func() { close(m.stopped) }) }

// When a request ID is reused while an earlier call with the same ID is still finishing or
// unwinding from cancellation, the earlier call's cleanup must not delete the newer call's
// cancellation entry from inflight.
func TestServeReusedIDDoesNotClobberActiveCancellation(t *testing.T) {
	t.Run("completing_older_call_preserves_newer_cancel", func(t *testing.T) {
		old := dialNode
		t.Cleanup(func() { dialNode = old })

		firstStarted := make(chan struct{})
		releaseFirst := make(chan struct{})
		var releaseFirstOnce sync.Once
		stopFirst := func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) }
		secondStarted := make(chan struct{})
		secondCancelled := make(chan struct{})
		dialNode = func(ctx context.Context, req any) ([]byte, error) {
			name, _ := req.(map[string]any)["name"].(string)
			switch name {
			case "first":
				close(firstStarted)
				<-releaseFirst
				return []byte(`{"content":[{"type":"text","text":"first-done"}]}`), nil
			case "second":
				close(secondStarted)
				<-ctx.Done()
				close(secondCancelled)
				return nil, ctx.Err()
			default:
				return []byte(`{"content":[{"type":"text","text":"ok"}]}`), nil
			}
		}

		in, w := io.Pipe()
		out := &syncBuf{}
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), "test", in, out) }()
		var closeOnce sync.Once
		stopServe := func() error {
			var err error
			closeOnce.Do(func() {
				stopFirst()
				_ = w.Close()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					err = errors.New("Serve did not stop")
				}
			})
			return err
		}
		t.Cleanup(func() {
			if err := stopServe(); err != nil {
				t.Errorf("Serve cleanup: %v", err)
			}
		})
		send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"first","arguments":{}}}`)
		<-firstStarted
		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"second","arguments":{}}}`)
		<-secondStarted

		// Let the first call finish and run its inflight cleanup; seeing its response guarantees cleanup ran.
		stopFirst()
		waitFor(t, "first response", func() bool { return strings.Contains(out.String(), `"first-done"`) })

		// Cancelling id 1 must still cancel the second call.
		send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		select {
		case <-secondCancelled:
		case <-time.After(2 * time.Second):
			t.Fatal("second call was not cancelled after older call with the same ID finished")
		}
		if err := stopServe(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("unwinding_cancelled_call_preserves_reused_id_cancel", func(t *testing.T) {
		oldDial := dialNode
		oldCleanup := afterInflightCleanup
		t.Cleanup(func() {
			dialNode = oldDial
			afterInflightCleanup = oldCleanup
		})

		firstStarted := make(chan struct{})
		firstCancelled := make(chan struct{})
		releaseFirst := make(chan struct{})
		var releaseFirstOnce sync.Once
		stopFirst := func() { releaseFirstOnce.Do(func() { close(releaseFirst) }) }
		secondStarted := make(chan struct{})
		secondCancelled := make(chan struct{})
		dialNode = func(ctx context.Context, req any) ([]byte, error) {
			name, _ := req.(map[string]any)["name"].(string)
			switch name {
			case "first":
				close(firstStarted)
				<-ctx.Done()
				close(firstCancelled)
				<-releaseFirst
				return nil, ctx.Err()
			case "second":
				close(secondStarted)
				<-ctx.Done()
				close(secondCancelled)
				return nil, ctx.Err()
			default:
				return []byte(`{"content":[{"type":"text","text":"ok"}]}`), nil
			}
		}

		cleaned := make(chan string, 4)
		afterInflightCleanup = func(key string) { cleaned <- key }

		in, w := io.Pipe()
		out := &syncBuf{}
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), "test", in, out) }()
		var closeOnce sync.Once
		stopServe := func() error {
			var err error
			closeOnce.Do(func() {
				stopFirst()
				_ = w.Close()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					err = errors.New("Serve did not stop")
				}
			})
			return err
		}
		t.Cleanup(func() {
			if err := stopServe(); err != nil {
				t.Errorf("Serve cleanup: %v", err)
			}
		})
		send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"first","arguments":{}}}`)
		<-firstStarted
		send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		<-firstCancelled

		// Client reuses id 1 while the cancelled first call is still unwinding.
		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"second","arguments":{}}}`)
		<-secondStarted

		// Release the first call and wait deterministically for its inflight cleanup critical section to finish.
		stopFirst()
		select {
		case key := <-cleaned:
			if key != "1" {
				t.Fatalf("unexpected cleaned key %q, want %q", key, "1")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("first call did not finish inflight cleanup")
		}

		// Now cancelling id 1 must cancel the second call.
		send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		select {
		case <-secondCancelled:
		case <-time.After(2 * time.Second):
			t.Fatal("second call was not cancelled after cancelled older call unwound")
		}
		if err := stopServe(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("single_cancel_cancels_overlapping_pair_without_affecting_third", func(t *testing.T) {
		oldDial := dialNode
		oldCleanup := afterInflightCleanup
		t.Cleanup(func() {
			dialNode = oldDial
			afterInflightCleanup = oldCleanup
		})

		firstStarted := make(chan struct{})
		secondStarted := make(chan struct{})
		thirdStarted := make(chan struct{})
		firstCancelled := make(chan struct{})
		secondCancelled := make(chan struct{})
		thirdCancelled := make(chan struct{})
		releaseOld := make(chan struct{})
		var releaseOldOnce sync.Once
		stopOld := func() { releaseOldOnce.Do(func() { close(releaseOld) }) }

		dialNode = func(ctx context.Context, req any) ([]byte, error) {
			name, _ := req.(map[string]any)["name"].(string)
			switch name {
			case "first":
				close(firstStarted)
				<-ctx.Done()
				close(firstCancelled)
				<-releaseOld
				return nil, ctx.Err()
			case "second":
				close(secondStarted)
				<-ctx.Done()
				close(secondCancelled)
				<-releaseOld
				return nil, ctx.Err()
			case "third":
				close(thirdStarted)
				<-ctx.Done()
				close(thirdCancelled)
				return nil, ctx.Err()
			default:
				return []byte(`{"content":[{"type":"text","text":"ok"}]}`), nil
			}
		}

		cleaned := make(chan string, 8)
		afterInflightCleanup = func(key string) { cleaned <- key }

		in, w := io.Pipe()
		out := &syncBuf{}
		done := make(chan error, 1)
		go func() { done <- Serve(context.Background(), "test", in, out) }()
		var closeOnce sync.Once
		stopServe := func() error {
			var err error
			closeOnce.Do(func() {
				stopOld()
				_ = w.Close()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					err = errors.New("Serve did not stop")
				}
			})
			return err
		}
		t.Cleanup(func() {
			if err := stopServe(); err != nil {
				t.Errorf("Serve cleanup: %v", err)
			}
		})
		send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"first","arguments":{}}}`)
		<-firstStarted
		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"second","arguments":{}}}`)
		<-secondStarted

		// A single notifications/cancelled must cancel both overlapping calls with id 1.
		send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		for _, ch := range []struct {
			name string
			c    <-chan struct{}
		}{{"first", firstCancelled}, {"second", secondCancelled}} {
			select {
			case <-ch.c:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s call was not cancelled by single notifications/cancelled", ch.name)
			}
		}

		// A 3rd call reusing id 1 arrives before first/second unwind; their cleanup must not drop it.
		send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"third","arguments":{}}}`)
		<-thirdStarted
		stopOld()
		for range 2 {
			select {
			case key := <-cleaned:
				if key != "1" {
					t.Fatalf("unexpected cleaned key %q, want %q", key, "1")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for cancelled calls to complete inflight cleanup")
			}
		}
		select {
		case <-thirdCancelled:
			t.Fatal("third call was unexpectedly cancelled while older pair unwound")
		default:
		}

		// A subsequent notifications/cancelled must still cancel the third call.
		send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
		select {
		case <-thirdCancelled:
		case <-time.After(2 * time.Second):
			t.Fatal("third call was not cancelled after older pair unwound")
		}
		if err := stopServe(); err != nil {
			t.Fatal(err)
		}
	})
}

// Once a call's or Serve's context is cancelled, writeNotificationFor must drop late
// notifications/progress and notifications/tools/list_changed under mu.
func TestServeDropsProgressTickAfterCancellation(t *testing.T) {
	t.Run("progress_tick", func(t *testing.T) {
		oldDial := dialNode
		oldTicker := newProgressTicker
		t.Cleanup(func() {
			dialNode = oldDial
			newProgressTicker = oldTicker
		})

		for range 5 {
			callStarted := make(chan struct{})
			callCancelled := make(chan struct{})
			releaseCall := make(chan struct{})
			var releaseCallOnce sync.Once
			stopCall := func() { releaseCallOnce.Do(func() { close(releaseCall) }) }
			dialNode = func(ctx context.Context, _ any) ([]byte, error) {
				close(callStarted)
				<-ctx.Done()
				close(callCancelled)
				<-releaseCall
				return nil, ctx.Err()
			}

			// Hold Serve's mu inside out.Write on a ping response while keepAlive receives a progress tick
			// and blocks on mu.Lock(). Cancelling Serve's ctx while mu is held must cause writeNotificationFor
			// to drop the queued progress notification once mu is released.
			var mu sync.Mutex
			var linesOut []string
			pingWriting := make(chan struct{})
			allowPingWrite := make(chan struct{})
			var pingOnce, allowOnce sync.Once
			unblockPing := func() { allowOnce.Do(func() { close(allowPingWrite) }) }
			out := writerFunc(func(p []byte) (int, error) {
				line := string(p)
				if strings.Contains(line, `"id":50`) {
					pingOnce.Do(func() { close(pingWriting) })
					<-allowPingWrite
				}
				mu.Lock()
				linesOut = append(linesOut, line)
				mu.Unlock()
				return len(p), nil
			})

			ticker := &testProgressTicker{ch: make(chan time.Time), stopped: make(chan struct{})}
			newProgressTicker = func(time.Duration) progressTicker { return ticker }

			sctx, cancelServe := context.WithCancel(context.Background())
			in, w := io.Pipe()
			done := make(chan error, 1)
			go func() { done <- Serve(sctx, "test", in, out) }()
			var closeOnce sync.Once
			stopServe := func() error {
				var err error
				closeOnce.Do(func() {
					cancelServe()
					unblockPing()
					stopCall()
					_ = w.Close()
					select {
					case err = <-done:
					case <-time.After(5 * time.Second):
						err = errors.New("Serve did not stop")
					}
				})
				return err
			}
			t.Cleanup(func() {
				if err := stopServe(); err != nil {
					t.Errorf("Serve cleanup: %v", err)
				}
			})
			send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

			send(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{},"_meta":{"progressToken":"pt-race"}}}`)
			<-callStarted
			// Block mu inside out.Write via a ping response.
			send(`{"jsonrpc":"2.0","id":50,"method":"ping"}`)
			<-pingWriting

			// Deliver a progress tick over the unbuffered channel while mu is held and sctx is still live,
			// so keepAlive advances past its ctx check into writeNotificationFor and blocks on mu.Lock().
			ticker.ch <- time.Now()

			// Cancel Serve's context while mu is still held and wait until the tool call observes cancellation.
			cancelServe()
			<-callCancelled

			// Release mu and shut down Serve.
			unblockPing()
			stopCall()
			if err := stopServe(); err != nil {
				t.Fatal(err)
			}

			mu.Lock()
			joined := strings.Join(linesOut, "")
			mu.Unlock()
			if strings.Contains(joined, `"notifications/progress"`) {
				t.Fatalf("cancelled call emitted notifications/progress after cancellation: %s", joined)
			}
			if strings.Contains(joined, `"id":1`) {
				t.Fatalf("cancelled call received a response: %s", joined)
			}
		}
	})

	t.Run("watch_tools_list_changed", func(t *testing.T) {
		oldDial := dialNode
		t.Cleanup(func() { dialNode = oldDial })

		watchPolling := make(chan struct{})
		releaseWatch := make(chan struct{})
		var watchOnce, releaseWatchOnce sync.Once
		unblockWatch := func() { releaseWatchOnce.Do(func() { close(releaseWatch) }) }
		dialNode = func(ctx context.Context, req any) ([]byte, error) {
			cursor, _ := req.(map[string]any)["input"].(map[string]string)
			if cursor["revision"] == "" {
				return []byte(`{"revision":"r1","session":"s1","tools":[{"name":"tool_a"}]}`), nil
			}
			watchOnce.Do(func() { close(watchPolling) })
			select {
			case <-releaseWatch:
				return []byte(`{"revision":"r2","session":"s1","tools":[{"name":"tool_a"},{"name":"tool_b"}]}`), nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		var mu sync.Mutex
		var linesOut []string
		pingWriting := make(chan struct{})
		allowPingWrite := make(chan struct{})
		var pingOnce, allowOnce sync.Once
		unblockPing := func() { allowOnce.Do(func() { close(allowPingWrite) }) }
		out := writerFunc(func(p []byte) (int, error) {
			line := string(p)
			if strings.Contains(line, `"id":50`) {
				pingOnce.Do(func() { close(pingWriting) })
				<-allowPingWrite
			}
			mu.Lock()
			linesOut = append(linesOut, line)
			mu.Unlock()
			return len(p), nil
		})

		sctx, cancelServe := context.WithCancel(context.Background())
		in, w := io.Pipe()
		done := make(chan error, 1)
		go func() { done <- Serve(sctx, "test", in, out) }()
		var closeOnce sync.Once
		stopServe := func() error {
			var err error
			closeOnce.Do(func() {
				cancelServe()
				unblockWatch()
				unblockPing()
				_ = w.Close()
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					err = errors.New("Serve did not stop")
				}
			})
			return err
		}
		t.Cleanup(func() {
			if err := stopServe(); err != nil {
				t.Errorf("Serve cleanup: %v", err)
			}
		})
		send := func(s string) { _, _ = io.WriteString(w, s+"\n") }

		// Trigger initial tools/list so Serve starts watchTools(ctx, snapshot, writeNotificationFor(ctx)).
		send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
		select {
		case <-watchPolling:
		case <-time.After(2 * time.Second):
			t.Fatal("watchTools did not start polling")
		}

		// Hold mu inside out.Write via a ping response while watchTools receives a changed tool list.
		send(`{"jsonrpc":"2.0","id":50,"method":"ping"}`)
		<-pingWriting
		unblockWatch()

		// Cancel Serve's context while mu is held, then release mu.
		cancelServe()
		unblockPing()
		if err := stopServe(); err != nil {
			t.Fatal(err)
		}

		mu.Lock()
		joined := strings.Join(linesOut, "")
		mu.Unlock()
		if strings.Contains(joined, `"notifications/tools/list_changed"`) {
			t.Fatalf("watchTools emitted list_changed after Serve context was cancelled: %s", joined)
		}
	})
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
