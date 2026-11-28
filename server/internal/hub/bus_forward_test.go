package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Exercise authentication and the bus codec with in-memory tokens and an RPC pipe, without Postgres.
func TestBusForwardsTrustedDeviceActor(t *testing.T) {
	h := newTestHub(t)
	const token = "test-device-token"
	const limited = "test-answer-token"
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{
		hashToken(token):   {ID: "device-1", Name: "Trusted device", Role: roleFull},
		hashToken(limited): {ID: "device-2", Role: roleAnswer},
	}, rev: map[string]bool{}}
	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.HTTPRequest, 1)
	ap.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP {
			return nil, errors.New("unexpected " + method)
		}
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		got <- req
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}
	routes := h.routes()
	for _, tt := range []struct{ name, forged string }{{"device name", ""}, {"forged header", "Forged device"}} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/model-profiles/economy/apply", strings.NewReader(`{}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Cookie", "test-cookie=private")
			r.Header.Set(assistantwire.RoleHeader, roleAnswer)
			r.Header.Set(assistantwire.ExtensionHeader, "forged-extension")
			r.Header.Set(assistantwire.AdminHeader, "1")
			if tt.forged != "" {
				r.Header.Set(assistantwire.DeviceNameHeader, tt.forged)
				r.Header.Add(assistantwire.DeviceNameHeader, "Another forged device")
			}
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
				t.Fatalf("forward: %d %s", w.Code, w.Body)
			}
			select {
			case req := <-got:
				if req.Method != http.MethodPost || req.Path != r.URL.Path || string(req.Body) != `{}` {
					t.Fatalf("forwarded request: %+v", req)
				}
				if req.ModeActor == nil || req.ModeActor.Kind != "device" || req.ModeActor.DeviceName != "Trusted device" {
					t.Errorf("trusted mode actor: %+v", req.ModeActor)
				}
				for name, want := range map[string]string{assistantwire.DeviceNameHeader: "Trusted device", assistantwire.RoleHeader: roleFull} {
					if values := req.Header[name]; len(values) != 1 || values[0] != want {
						t.Errorf("forwarded %s=%v want [%s]", name, values, want)
					}
				}
				for _, name := range []string{"Authorization", "Cookie", assistantwire.ExtensionHeader, assistantwire.AdminHeader} {
					if _, ok := req.Header[name]; ok {
						t.Errorf("forwarded untrusted or private header %s", name)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no request reached the assistant")
			}
		})
	}
	for _, tt := range []struct {
		name, token string
		want        bool
	}{
		{"owner card answer", token, true},
		{"answer-only with forged fields", limited, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/test-card/resolve",
				strings.NewReader(`{"allow":false,"owner_answer":true,"owner_input":true}`))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+tt.token)
			r.Header.Set("Owner-Answer", "true")
			w := httptest.NewRecorder()
			routes.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("resolve: %d %s", w.Code, w.Body)
			}
			if req := <-got; req.OwnerAnswer != tt.want {
				t.Errorf("typed owner provenance = %v, want %v", req.OwnerAnswer, tt.want)
			}
		})
	}

}

// The assistant's routes (assistantwire.Routes) are its to answer, unchanged; the hub's own stay the hub's; while the
// assistant is not connected they answer 503 assistant_offline.
func TestBusForwardsTheAssistantRoutes(t *testing.T) {
	h, srv, _ := newBusHub(t)
	got := make(chan assistantwire.HTTPRequest, 4)
	r := helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == assistantwire.MethodTools {
			return map[string]any{"tools": []any{}}, nil
		}
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil {
			return nil, errors.New("unexpected " + method)
		}
		got <- req
		if req.Path == "/api/v1/assistant/runs" {
			return nil, &rpc.Error{Code: "boom", Message: "it failed"}
		}
		return assistantwire.HTTPResponse{Status: 201, Header: map[string][]string{"Content-Type": {"application/json"}, "X-Test": {"1"}},
			Body: []byte(`{"answered":"by the assistant"}`)}, nil
	})
	do := func(method, path, body string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set(assistantwire.RoleHeader, "forged")
		req.Header.Set(assistantwire.ExtensionHeader, "forged")
		req.Header.Set(assistantwire.DeviceNameHeader, "forged")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}
	resp, body := do("POST", "/api/v1/assistant/conversations/c1/route?x=1", `{"text":"你好"}`)
	if resp.StatusCode != 201 || body != `{"answered":"by the assistant"}` || resp.Header.Get("X-Test") != "1" {
		t.Fatalf("forwarded: %d %s %v", resp.StatusCode, body, resp.Header)
	}
	req := <-got
	if req.Method != "POST" || req.Path != "/api/v1/assistant/conversations/c1/route" || req.Query != "x=1" || string(req.Body) != `{"text":"你好"}` ||
		req.Header[assistantwire.RoleHeader][0] != roleFull || req.Header["Authorization"] != nil || req.Header[assistantwire.ExtensionHeader] != nil ||
		req.Header[assistantwire.DeviceNameHeader] != nil { // the hub's own token names no device
		t.Fatalf("the assistant got %+v", req)
	}
	if resp, body := do("GET", "/api/v1/assistant/runs", ""); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "boom") {
		t.Fatalf("the assistant's error: %d %s", resp.StatusCode, body)
	}
	<-got
	if resp, body := do("GET", "/api/v1/task-plans?conversation=c1", ""); resp.StatusCode != 201 {
		t.Fatalf("pending task plans: %d %s", resp.StatusCode, body)
	}
	if req := <-got; req.Method != "GET" || req.Path != "/api/v1/task-plans" || req.Query != "conversation=c1" {
		t.Fatalf("pending task plans request reached the assistant as %+v", req)
	}
	for _, c := range [][2]string{{"POST", "/api/v1/task-plans/p1/start"}, {"GET", "/api/v1/task-trust"},
		{"DELETE", "/api/v1/task-trust"}, {"POST", "/api/v1/todos/t1/done-check"}, {"GET", "/api/v1/todos"},
		{"POST", "/api/v1/todos/reorganize"},
		{"POST", "/api/v1/todos/decisions/batch"},
		{"POST", "/api/v1/todos/t1/blockers"},
		{"POST", "/api/v1/todos/t1/backend-workspace/prepare"},
		{"POST", "/api/v1/assistant/repositories/repo_web/mirrors/node1/sync"},
		{"DELETE", "/api/v1/todos/t1/blockers/blk_1"},
		{"PUT", "/api/v1/todos/t1/spaces/m1/group/alpha"}, {"GET", "/api/v1/assistant"}, {"PUT", "/api/v1/assistant"},
		{"POST", "/api/v1/assistant/conversations"}, {"POST", "/api/v1/assistant/main/new"}, {"POST", "/api/v1/assistant/main/clear"},
		{"DELETE", "/api/v1/assistant/memory/note.md"}} {
		if resp, _ := do(c[0], c[1], ""); resp.StatusCode != 201 {
			t.Fatalf("%s %s: %d", c[0], c[1], resp.StatusCode)
		}
		if req := <-got; req.Method != c[0] || req.Path != c[1] {
			t.Fatalf("%s %s reached the assistant as %s %s", c[0], c[1], req.Method, req.Path)
		}
	}
	// The hub's own routes stay the hub's.
	for _, c := range [][2]string{{"GET", "/api/v1/skills"}, {"GET", "/api/v1/machines"},
		{"GET", "/api/v1/todosx"}, {"GET", "/api/v1/assistantx"}} {
		do(c[0], c[1], "")
	}
	select {
	case req := <-got:
		t.Fatalf("forwarded %s %s", req.Method, req.Path)
	default:
	}
	// Not connected: 503 assistant_offline, the hub's own routes as before.
	_ = r.ws.CloseNow()
	eventually(t, 5*time.Second, "the assistant gone", func() bool { return h.busConnOf(busAssistant) == nil })
	resp, body = do("GET", "/api/v1/assistant/tasks", "")
	if resp.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"assistant_offline"`) {
		t.Fatalf("offline: %d %s", resp.StatusCode, body)
	}
	if resp, _ := do("GET", "/api/v1/machines", ""); resp.StatusCode != 200 {
		t.Fatalf("the hub's own: %d", resp.StatusCode)
	}
}

func TestBusForwardWorkspaceTimeouts(t *testing.T) {
	mux := http.NewServeMux()
	(&Hub{}).assistantRoutesRelayed(mux)
	for _, tc := range []struct {
		path string
		want time.Duration
	}{
		{"/api/v1/task-plans/p1/start", 5 * time.Minute},
		{"/api/v1/assistant/repositories/repo_web/mirrors/node1/sync", repositorywire.MirrorEnsureTimeout + time.Minute},
		{"/api/v1/todos/t1/backend-workspace/prepare", repositorywire.MaxTaskRepositories*repositorywire.MirrorEnsureTimeout + 6*time.Minute},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		_, pattern := mux.Handler(req)
		if pattern == "" {
			t.Fatalf("no route registered for %s", tc.path)
		}
		if got := relayHTTPTimeout(pattern); got != tc.want {
			t.Errorf("%s relay timeout = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// A manager is third-party code over the bus: an answer whose status is not a final HTTP status (outside 200-599)
// reaches the app as a 502 with a JSON error and none of the manager's headers or body, where net/http would panic or
// leave the connection unusable. Valid statuses pass through unchanged.
func TestBusForwardRefusesAnImpossibleStatus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	logs := &syncBuffer{}
	h.logger.SetOutput(logs)
	helloAssistant(t, h, srv, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == assistantwire.MethodTools {
			return map[string]any{"tools": []any{}}, nil
		}
		var req assistantwire.HTTPRequest
		if method != assistantwire.MethodHTTP || json.Unmarshal(params, &req) != nil {
			return nil, errors.New("unexpected " + method)
		}
		q, _ := url.ParseQuery(req.Query)
		status, _ := strconv.Atoi(q.Get("s"))
		res := assistantwire.HTTPResponse{Status: status, Header: map[string][]string{"X-Manager": {"1"}}}
		if status != http.StatusNoContent {
			res.Body = []byte(`{"answered":true}`)
		}
		return res, nil
	})
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	do := func(t *testing.T, path string, status int) (*http.Response, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second) // a regression must not hang the run
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path+"?s="+strconv.Itoa(status), nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("status %d: reading the body: %v", status, err)
		}
		return resp, string(b)
	}
	// One route the hub passes on, one whose answers it merges over the managers (mergedLists).
	for _, path := range []string{"/api/v1/assistant/runs", "/api/v1/todos"} {
		for _, s := range []int{42, 99, 101, 600, 1000} {
			t.Run(fmt.Sprintf("%s refused %d", path, s), func(t *testing.T) {
				resp, body := do(t, path, s)
				var e struct{ Error, Code, Detail string } // the status in error, or in detail under a localized error
				if resp.StatusCode != http.StatusBadGateway || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") ||
					json.Unmarshal([]byte(body), &e) != nil || !strings.Contains(e.Error+" "+e.Detail, strconv.Itoa(s)) || e.Code == "" {
					t.Fatalf("got %d %v %s, want 502 with a JSON error naming %d", resp.StatusCode, resp.Header, body, s)
				}
				if resp.Header.Get("X-Manager") != "" || strings.Contains(body, `"answered":true`) {
					t.Errorf("the manager's answer was passed on: %v %s", resp.Header, body)
				}
				if want := fmt.Sprintf("bus: [%s] http.request GET %s: answered HTTP status %d", busAssistant, path, s); !logs.WaitFor(want, 5*time.Second) {
					t.Errorf("no log %q in %q", want, logs.String())
				}
			})
		}
	}
	for _, c := range [][2]int{{0, http.StatusOK}, {http.StatusCreated, http.StatusCreated}, {http.StatusNoContent, http.StatusNoContent},
		{http.StatusNotFound, http.StatusNotFound}} {
		t.Run(fmt.Sprintf("passed %d", c[0]), func(t *testing.T) {
			resp, body := do(t, "/api/v1/assistant/runs", c[0])
			want := `{"answered":true}`
			if c[1] == http.StatusNoContent {
				want = ""
			}
			if resp.StatusCode != c[1] || body != want || resp.Header.Get("X-Manager") != "1" {
				t.Fatalf("got %d %v %q, want %d %q", resp.StatusCode, resp.Header, body, c[1], want)
			}
		})
	}
}

// A relayed body is read as io.ReadAll read it, whatever its Content-Length says: the same bytes, or the same error
// (413 too_large past the limit, 400 for one that broke off).
func TestReadRelayedIsReadAll(t *testing.T) {
	body := `{"prompt":"<a long message>"}`
	broken := func() io.Reader {
		return io.MultiReader(strings.NewReader(body[:9]), iotest.ErrReader(io.ErrUnexpectedEOF))
	}
	for _, c := range []struct {
		name string
		size int64
		body func() io.Reader
	}{
		{"its length", int64(len(body)), nil},
		{"longer than said", int64(len(body)) - 7, nil},
		{"shorter than said", int64(len(body)) + 7, nil},
		{"said empty", 0, nil},
		{"empty", 0, func() io.Reader { return http.NoBody }},
		{"unknown length", -1, nil},
		{"over the limit", busHTTPLimit + 1, func() io.Reader { return &endlessBody{} }},
		{"over the limit, said within", int64(len(body)), func() io.Reader { return &endlessBody{} }},
		{"broken", int64(len(body)), broken},
		{"broken, unknown length", -1, broken},
	} {
		t.Run(c.name, func(t *testing.T) {
			read := func(relayed bool) ([]byte, string) {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/conversations", nil)
				r.Body, r.ContentLength = io.NopCloser(strings.NewReader(body)), c.size
				if c.body != nil {
					r.Body = io.NopCloser(c.body())
				}
				w := httptest.NewRecorder()
				if relayed {
					b, err := readRelayed(w, r)
					return b, fmt.Sprintf("%T %v", err, err)
				}
				b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, busHTTPLimit))
				return b, fmt.Sprintf("%T %v", err, err)
			}
			got, gerr := read(true)
			want, werr := read(false)
			if gerr != werr || (werr == "<nil> <nil>" && !bytes.Equal(got, want)) {
				t.Fatalf("read %d bytes, %s; io.ReadAll read %d, %s", len(got), gerr, len(want), werr)
			}
		})
	}
}

// callLineOf is the line a Call of http.request with params writes on a peer with the frame budget given, or the
// Call's error.
func callLineOf(params any, budget int) ([]byte, error) {
	w := &lineThenClose{}
	w.p = rpc.New("h", w)
	w.p.Limits.FrameBytes = budget
	err := w.p.Call(context.Background(), assistantwire.MethodHTTP, params, nil)
	if errors.Is(err, rpc.ErrDropped) {
		err = nil
	}
	return w.line, err
}

// lineThenClose keeps the line a peer writes and closes the peer, so its Call returns without an answer.
type lineThenClose struct {
	p    *rpc.Peer
	line []byte
}

func (c *lineThenClose) Write(b []byte) (int, error) {
	c.line = append(c.line, b...)
	c.p.Close(io.EOF)
	return len(b), nil
}

// A relayed request goes out in the line json.Marshal writes for it: header and query with HTML characters, U+2028
// and U+2029, other non-ASCII and invalid UTF-8; bodies of 0 to 3 bytes (base64's padding) and longer; and the frame
// budget the same at the line's size and one byte under it.
func TestHTTPParamsAreJSONMarshals(t *testing.T) {
	long := make([]byte, 1000)
	for i := range long {
		long[i] = byte(i * 31)
	}
	header := map[string][]string{"X-Html": {`<a href="?a=1&b=2">`}, "X-Sep": {"a\u2028b\u2029c"}, "X-Name": {"日本語 é"},
		"X-Bad": {"\xff\xfe\xc3"}, "Content-Type": {"application/json"}}
	for _, req := range []assistantwire.HTTPRequest{
		{Method: "POST", Path: "/api/v1/assistant/conversations", Header: header, Body: []byte(`{"prompt":"<&>\u2028"}`)},
		{Method: "POST", Path: "/api/v1/kb/recordings", Query: "q=<&>&x=\u2029\xff", Body: []byte{0}},
		{Method: "PUT", Path: "/p", Body: []byte{1, 2}},
		{Method: "PUT", Path: "/p", Body: []byte{1, 2, 3}},
		{Method: "PUT", Path: "/é", Header: header, Body: long},
		{Method: "GET", Path: "/p", Header: header, Body: []byte{}},
		{Method: "GET", Path: "/p"},
		{Body: []byte("x")}, // no method or path: written all the same, before the body
	} {
		want, werr := callLineOf(req, 0)
		got, err := callLineOf(httpParams(req), 0)
		if err != nil || werr != nil || !bytes.Equal(got, want) {
			t.Errorf("%s %s with %d body bytes:\n%q, %v\nwas %q, %v", req.Method, req.Path, len(req.Body), got, err, want, werr)
			continue
		}
		for _, budget := range []int{len(want), len(want) - 1} {
			got, err := callLineOf(httpParams(req), budget)
			want, werr := callLineOf(req, budget)
			if !bytes.Equal(got, want) || err != werr {
				t.Errorf("%s %s, frame budget %d: wrote %d bytes, %v; was %d, %v", req.Method, req.Path, budget, len(got), err, len(want), werr)
			}
		}
	}
}

func TestCallHTTPInlineUsesAppendedParams(t *testing.T) {
	req := assistantwire.HTTPRequest{
		Method: http.MethodPut, Path: "/example", Query: "a=1&b=2",
		Body: bytes.Repeat([]byte{0, 0xff, 0x1f}, bodyStreamMin/3+1),
	}
	wantLine, err := callLineOf(req, 0)
	if err != nil {
		t.Fatal(err)
	}
	caller := managerBusCaller{call: func(_ context.Context, _, method string, params, out any) error {
		if method != "http.request" {
			t.Fatalf("method = %q, want http.request", method)
		}
		if _, ok := params.(rpc.Appended); !ok {
			t.Fatalf("large body sent as %T, want rpc.Appended", params)
		}
		got, err := callLineOf(params, 0)
		if err != nil || !bytes.Equal(got, wantLine) {
			t.Fatalf("appended request wrote %d bytes, %v, want the same %d wire bytes", len(got), err, len(wantLine))
		}
		*out.(*assistantwire.HTTPResponse) = assistantwire.HTTPResponse{Status: http.StatusCreated}
		return nil
	}}
	var res assistantwire.HTTPResponse
	if err := callHTTPInline(t.Context(), caller, assistantwire.HTTPMethod, req, &res); err != nil || res.Status != http.StatusCreated {
		t.Fatalf("callHTTPInline = %+v, %v, want HTTP 201", res, err)
	}
}

// httpParams appends the body after the rest of the request: it is HTTPRequest's last field, left out when empty,
// and method and path, always written, come before it.
func TestHTTPRequestBodyIsLast(t *testing.T) {
	typ := reflect.TypeFor[assistantwire.HTTPRequest]()
	last := typ.Field(typ.NumField() - 1)
	if last.Name != "Body" || last.Tag.Get("json") != "body,omitempty" || last.Type != reflect.TypeFor[[]byte]() {
		t.Fatalf("HTTPRequest's last field is %s %s `%s`", last.Name, last.Type, last.Tag)
	}
	if b, _ := json.Marshal(assistantwire.HTTPRequest{}); !bytes.HasPrefix(b, []byte(`{"method":"","path":""`)) {
		t.Fatalf("an empty HTTPRequest is %s", b)
	}
}

// A relayed upload costs the hub its body and the request's line, once each: no buffers grown and copied to read it,
// no copy of its base64 encoded and copied out, and none of it kept in an encoder's buffer past a collection.
func TestRelayCopiesALargeBodyOnce(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector's instrumentation allocates on its own")
	}
	h, body := answeringManager(t), largePrompt(4<<20)
	relayOnce(t, h, body)
	var before, after runtime.MemStats
	least := uint64(math.MaxUint64)
	func() {
		defer debug.SetGCPercent(debug.SetGCPercent(-1)) // no GC empties a pool in the middle of a run
		for range 3 {
			runtime.ReadMemStats(&before)
			relayOnce(t, h, body)
			runtime.ReadMemStats(&after)
			least = min(least, after.TotalAlloc-before.TotalAlloc)
		}
	}()
	if limit := uint64(float64(len(body)+base64.StdEncoding.EncodedLen(len(body)))*1.03) + 256<<10; least > limit {
		t.Errorf("relaying a %d-byte body allocated %d bytes, over %d (the body and its line)", len(body), least, limit)
	}
	runtime.GC()
	runtime.GC() // what pools keep goes at the second
	runtime.ReadMemStats(&before)
	relayOnce(t, h, body)
	runtime.GC()
	runtime.ReadMemStats(&after)
	if kept := int64(after.HeapAlloc) - int64(before.HeapAlloc); kept > int64(len(body))/8 {
		t.Errorf("relaying a %d-byte body left %d bytes on the heap after a collection", len(body), kept)
	}
}

// What the assistant sends the apps (ws.broadcast) reaches them unchanged; a message without a type does not.
func TestBusBroadcast(t *testing.T) {
	h, srv, _ := newBusHub(t)
	app := wsApp(t, h)
	r := helloAssistant(t, h, srv, echoService)
	_ = r.peer.Notify("ws.broadcast", map[string]any{"message": map[string]any{"oops": true}})
	msg := json.RawMessage(`{"type":"task_plan","plan":{"id":"p1","steps":["一","二"]}}`)
	_ = r.peer.Notify("ws.broadcast", map[string]any{"message": msg})
	select {
	case raw := <-app.out:
		if !jsonEqual(withoutSeq(raw), withoutSeq(msg)) {
			t.Fatalf("the apps got %s, want %s (and the hub's event_seq)", raw, msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the apps")
	}
}

// wsApp is an app connected to the hub's WebSocket.
func wsApp(t *testing.T, h *Hub) *client {
	t.Helper()
	c := &client{device: "test-device", out: make(chan []byte, 64)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	return c
}

// helloAssistant connects a fake assistant answering the hub's calls with handle, past its handshake.
func helloAssistant(t *testing.T, h *Hub, srv *httptest.Server, handle rpc.Handler) *rawBus {
	t.Helper()
	tok := busToken(t, h, busAssistant, "助手")
	r := dialRawBusWith(t, srv, tok, handle)
	if err := r.call("manager.hello", map[string]any{"id": busAssistant}, nil); err != nil {
		t.Fatal(err)
	}
	return r
}

// withoutSeq is a message the apps got without the event_seq the hub numbers every message with.
func withoutSeq(b []byte) json.RawMessage {
	var m map[string]any // at every depth: the hub passes a manager's message on as it read it (keys in order)
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	delete(m, "event_seq")
	out, _ := json.Marshal(m)
	return out
}
