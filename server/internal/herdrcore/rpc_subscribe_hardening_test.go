//go:build !windows

package herdrcore

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func listenRawFake(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hcrw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				handler(c)
			}()
		}
	}()
	return sock
}

func TestSubscribeRequiresMatchingResponseID(t *testing.T) {
	sock := listenRawFake(t, func(c net.Conn) {
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &req)
		// Send unrelated numeric ID, empty object, id:null non-error, and string ID
		// matching the numeric request ID before the real matching error response.
		_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":999999,"result":{}}` + "\n"))
		_, _ = c.Write([]byte(`{}` + "\n"))
		_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":null,"result":{}}` + "\n"))
		_, _ = fmt.Fprintf(c, "{\"jsonrpc\":\"2.0\",\"id\":%q,\"result\":{}}\n", string(req.ID))
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"error": map[string]any{
				"code":    -32000,
				"message": "denied",
				"data":    map[string]any{"kind": "denied"},
			},
		})
		_, _ = c.Write(append(resp, '\n'))
	})

	var startedCalls atomic.Int32
	c := &Client{Socket: sock}
	err := c.Subscribe(context.Background(), func() { startedCalls.Add(1) }, func(Event) {})
	if !IsKind(err, "denied") {
		t.Fatalf("Subscribe = %v, want denied error matching request ID", err)
	}
	if n := startedCalls.Load(); n != 0 {
		t.Fatalf("started() called %d times on unmatched response before error", n)
	}
}

func TestSubscribeNullIDErrorWithNonObjectData(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	sock := listenRawFake(t, func(c net.Conn) {
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		// Valid JSON-RPC 2.0 id:null error with string data (not an object).
		_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request","data":"extra detail"}}` + "\n"))
		<-hold
	})

	var startedCalls atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- (&Client{Socket: sock}).Subscribe(context.Background(), func() { startedCalls.Add(1) }, func(Event) {})
	}()

	select {
	case err := <-done:
		var he *Error
		if !errors.As(err, &he) || he.Code != -32600 || he.Message != "invalid request" || he.Kind != "" {
			t.Fatalf("Subscribe = %v, want *Error{-32600, invalid request, ''}", err)
		}
		if n := startedCalls.Load(); n != 0 {
			t.Fatalf("started() called %d times on id:null error", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe hung on id:null error with string data")
	}
}

func TestSubscribeAllowsNilCallbacks(t *testing.T) {
	f := newFakeCore(t, nil)
	c := &Client{Socket: f.sock}

	// Nil started callback while receiving live events.
	ctx1, cancel1 := context.WithCancel(context.Background())
	got := make(chan Event, 2)
	done1 := make(chan error, 1)
	go func() {
		done1 <- c.Subscribe(ctx1, nil, func(e Event) { got <- e })
	}()
	<-f.sub
	f.push("event", map[string]any{"type": "status", "terminal": "t1"})
	select {
	case ev := <-got:
		if ev.Type != "status" || ev.Terminal != "t1" {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event with nil started callback")
	}
	cancel1()
	if err := <-done1; !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe(nil, fn) = %v, want context.Canceled", err)
	}

	// Nil fn callback while started fires and events arrive.
	ctx2, cancel2 := context.WithCancel(context.Background())
	started := make(chan struct{}, 1)
	done2 := make(chan error, 1)
	go func() {
		done2 <- c.Subscribe(ctx2, func() { started <- struct{}{} }, nil)
	}()
	<-f.sub
	<-started
	f.push("event", map[string]any{"type": "status", "terminal": "t2"})
	f.push("missed", map[string]any{"seq": 2})
	cancel2()
	select {
	case err := <-done2:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe(started, nil) = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe(started, nil) did not return on cancel")
	}
}

func TestSubscribeCancelInCallbacksStopsBufferedDispatch(t *testing.T) {
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	sock := listenRawFake(t, func(c net.Conn) {
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &req)
		ack, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		payload := string(ack) + "\n" +
			`{"jsonrpc":"2.0","method":"event","params":{"type":"status","terminal":"t1"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"event","params":{"type":"status","terminal":"t2"}}` + "\n" +
			`{"jsonrpc":"2.0","method":"event","params":{"type":"status","terminal":"t3"}}` + "\n"
		_, _ = c.Write([]byte(payload))
		<-hold
	})

	// Canceling inside started() must not dispatch any buffered events.
	ctx1, cancel1 := context.WithCancel(context.Background())
	var fnCalls1 atomic.Int32
	err := (&Client{Socket: sock}).Subscribe(ctx1, func() { cancel1() }, func(Event) { fnCalls1.Add(1) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe(cancel in started) = %v, want context.Canceled", err)
	}
	if n := fnCalls1.Load(); n != 0 {
		t.Fatalf("fn called %d times after started() canceled ctx, want 0", n)
	}

	// Canceling inside fn() on the first event must not dispatch remaining buffered events.
	ctx2, cancel2 := context.WithCancel(context.Background())
	var fnCalls2 atomic.Int32
	err = (&Client{Socket: sock}).Subscribe(ctx2, nil, func(Event) {
		fnCalls2.Add(1)
		cancel2()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe(cancel in fn) = %v, want context.Canceled", err)
	}
	if n := fnCalls2.Load(); n != 1 {
		t.Fatalf("fn called %d times after fn() canceled ctx, want 1", n)
	}
}

func TestPreCanceledContextOnCallAndSubscribe(t *testing.T) {
	var accepted atomic.Int32
	sock := listenRawFake(t, func(c net.Conn) {
		accepted.Add(1)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := &Client{Socket: sock}
	if _, err := c.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List(canceled) = %v, want context.Canceled", err)
	}
	var startedCalls, fnCalls atomic.Int32
	if err := c.Subscribe(ctx, func() { startedCalls.Add(1) }, func(Event) { fnCalls.Add(1) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe(canceled) = %v, want context.Canceled", err)
	}
	if startedCalls.Load() != 0 || fnCalls.Load() != 0 || accepted.Load() != 0 {
		t.Fatalf("pre-canceled ctx dialed or fired callbacks: accepted=%d started=%d fn=%d",
			accepted.Load(), startedCalls.Load(), fnCalls.Load())
	}
}

func TestCallReturnsContextErrorsNullIDErrorAndSkipsReverseRequest(t *testing.T) {
	// Part A: canceled context must wrap context.Canceled rather than raw net i/o timeout.
	received := make(chan struct{}, 1)
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	sockHold := listenRawFake(t, func(c net.Conn) {
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		select {
		case received <- struct{}{}:
		default:
		}
		<-hold
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := (&Client{Socket: sockHold}).List(ctx)
		done <- err
	}()
	<-received
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled List error = %v, want errors.Is(context.Canceled)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List did not abort on cancel")
	}

	// Part B: timeout / caller deadline must deterministically wrap context.DeadlineExceeded.
	if err := (&Client{Socket: sockHold}).Call(context.Background(), 20*time.Millisecond, "terminal.list", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call timeout error = %v, want errors.Is(context.DeadlineExceeded)", err)
	}
	dlCtx, dlCancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer dlCancel()
	if err := (&Client{Socket: sockHold}).Call(dlCtx, 5*time.Second, "terminal.list", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call caller-deadline error = %v, want errors.Is(context.DeadlineExceeded)", err)
	}

	// Part C: JSON-RPC parse/invalid-request error with id:null (and non-object data) must return immediately.
	sockNull := listenRawFake(t, func(c net.Conn) {
		_, _ = bufio.NewReader(c).ReadBytes('\n')
		_, _ = c.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error","data":"bad json"}}` + "\n"))
		<-hold
	})
	callDone := make(chan error, 1)
	go func() {
		_, err := (&Client{Socket: sockNull}).List(context.Background())
		callDone <- err
	}()
	select {
	case err := <-callDone:
		var he *Error
		if !errors.As(err, &he) || he.Code != -32700 || he.Message != "parse error" || he.Kind != "" {
			t.Fatalf("id:null error = %v, want *Error{-32700, parse error, ''}", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("List hung on JSON-RPC id:null error response")
	}

	// Part D: server-initiated reverse JSON-RPC request with matching ID and method != "" must be skipped.
	sockReverse := listenRawFake(t, func(c net.Conn) {
		line, err := bufio.NewReader(c).ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(line, &req)
		rev, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "method": "ping", "params": map[string]any{}})
		_, _ = c.Write(append(rev, '\n'))
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  map[string]any{"terminals": []any{map[string]any{"address": "t9", "state": "live"}}},
		})
		_, _ = c.Write(append(resp, '\n'))
	})
	terms, err := (&Client{Socket: sockReverse}).List(context.Background())
	if err != nil || len(terms) != 1 || terms[0].Address != "t9" {
		t.Fatalf("List with reverse request = %+v, %v; want [t9]", terms, err)
	}
}

func TestConfigSocketTOMLCompliance(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "escaped_windows_path", body: `socket = "C:\\Users\\alice\\hc.sock"` + "\n", want: `C:\Users\alice\hc.sock`},
		{name: "trailing_escaped_backslash", body: `socket = "C:\\dir\\"` + "\n", want: `C:\dir\`},
		{name: "escaped_quotes", body: `socket = "/tmp/my \"hc\"/c.sock"` + "\n", want: `/tmp/my "hc"/c.sock`},
		{name: "literal_string", body: `socket = 'C:\literal\path.sock'` + "\n", want: `C:\literal\path.sock`},
		{name: "hash_in_string_and_comment", body: `socket = "/tmp/a # b.sock" # comment` + "\n", want: `/tmp/a # b.sock`},
		{name: "multiline_basic", body: `socket = """/tmp/a.sock"""` + "\n", want: `/tmp/a.sock`},
		{name: "multiline_basic_leading_newline", body: "socket = \"\"\"\n/tmp/a.sock\"\"\"\n", want: `/tmp/a.sock`},
		{name: "multiline_literal", body: `socket = '''/tmp/b.sock'''` + "\n", want: `/tmp/b.sock`},
		{name: "multiline_literal_leading_newline", body: "socket = '''\n/tmp/b.sock'''\n", want: `/tmp/b.sock`},
		{name: "unicode_escape", body: `socket = "/tmp/\u0041.sock"` + "\n", want: `/tmp/A.sock`},
		{name: "case_sensitive_key", body: `SOCKET = "/tmp/upper.sock"` + "\n", want: ""},
		{name: "non_string_value", body: "socket = 123\n", want: ""},
		{name: "table_named_socket", body: "[socket]\npath = \"/tmp/x.sock\"\n", want: ""},
		{name: "invalid_escape_bell", body: `socket = "/tmp/\a.sock"` + "\n", want: ""},
		{name: "invalid_escape_vtab", body: `socket = "/tmp/\v.sock"` + "\n", want: ""},
		{name: "invalid_escape_octal", body: `socket = "/tmp/\000.sock"` + "\n", want: ""},
		{name: "invalid_escape_hex", body: `socket = "/tmp/\x41.sock"` + "\n", want: ""},
		{name: "trailing_garbage_double_quote", body: `socket = "/tmp/a.sock" garbage` + "\n", want: ""},
		{name: "trailing_garbage_single_quote", body: `socket = '/tmp/a.sock' garbage` + "\n", want: ""},
		{name: "unclosed_double_quote", body: `socket = "/tmp/a.sock` + "\n", want: ""},
		{name: "unclosed_single_quote", body: `socket = '/tmp/a.sock` + "\n", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".toml")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := configSocket(p); got != tc.want {
				t.Errorf("configSocket(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}
