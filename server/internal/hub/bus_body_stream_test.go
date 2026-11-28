package hub

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// bodyManager is the assistant on a real bus connection, answering the forwarded requests: http.request as it
// comes, http.request_streamed with the body read from the relay (bus.ReadBody, as the assistant's BusHub does)
// unless turn says to turn it down with a code.
type bodyManager struct {
	mu        sync.Mutex
	got       []assistantwire.HTTPRequest // as the route would see them: Body filled, BodyStream as it came
	how       []string                    // the method each came with
	turn      string                      // "": take the stream; else answer http.request_streamed with this code
	onRequest func()
}

func (m *bodyManager) requests() ([]string, []assistantwire.HTTPRequest) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string{}, m.how...), append([]assistantwire.HTTPRequest{}, m.got...)
}

// connectBodyManager connects m as the assistant to the hub at srv over client (nil: plain), naming the body
// stream's capability in its hello when streams is set.
func connectBodyManager(t *testing.T, h *Hub, srv *httptest.Server, client *http.Client, streams bool, m *bodyManager) {
	t.Helper()
	token := busToken(t, h, assistantwire.AssistantID, "assistant")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ws, err := bus.Dial(ctx, srv.URL, token, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.CloseNow() })
	conn := bus.NewConn(ctx, ws)
	peer := rpc.New("m", conn)
	peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != assistantwire.MethodHTTP && method != assistantwire.MethodHTTPStreamed {
			return map[string]any{}, nil
		}
		var p assistantwire.HTTPRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		m.mu.Lock()
		notify := m.onRequest
		m.mu.Unlock()
		if notify != nil {
			notify()
		}
		if method == assistantwire.MethodHTTPStreamed {
			m.mu.Lock()
			turn := m.turn
			m.mu.Unlock()
			if turn != "" {
				m.mu.Lock()
				m.how, m.got = append(m.how, method+" turned down"), append(m.got, p)
				m.mu.Unlock()
				return nil, &rpc.Error{Code: turn, Message: "turned down"}
			}
			c, err := bus.DialAt(ctx, srv.URL, token, client, devicewire.RelayPath, url.Values{"id": {p.BodyStream.ID}}.Encode())
			if err != nil {
				return nil, &rpc.Error{Code: assistantwire.CodeBodyIncomplete, Message: err.Error()}
			}
			if p.Body, err = bus.ReadBody(ctx, c, p.BodyStream.Size); err != nil {
				return nil, &rpc.Error{Code: assistantwire.CodeBodyIncomplete, Message: err.Error()}
			}
		}
		m.mu.Lock()
		m.how, m.got = append(m.how, method), append(m.got, p)
		m.mu.Unlock()
		return assistantwire.HTTPResponse{Status: http.StatusOK, Header: map[string][]string{"Content-Type": {"application/json"}},
			Body: []byte(`{"size":` + jsonInt(len(p.Body)) + `}`)}, nil
	}
	go func() { _ = peer.Serve(ctx, conn) }()
	hello := map[string]any{"id": assistantwire.AssistantID, "capabilities": []string{assistantwire.CapabilityConfigOwn}}
	if streams {
		hello["capabilities"] = []string{assistantwire.CapabilityConfigOwn, assistantwire.CapabilityHTTPBodyStream}
	}
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := peer.Call(hctx, busHello, hello, nil); err != nil {
		t.Fatal(err)
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

// bodyHub is a hub without a database, its routes served, identities in memory.
func bodyHub(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	t.Cleanup(func() {
		for _, c := range h.stopBus() {
			_ = c.ws.CloseNow()
		}
	})
	return h, srv
}

// putRecording is an app's upload of body on one of the assistant's routes, with the hub's own token.
func putRecording(t *testing.T, h *Hub, srv *httptest.Server, id string, body []byte) (int, string) {
	t.Helper()
	r, err := http.NewRequest(http.MethodPut, srv.URL+"/api/v1/recordings/"+id+"?x=1", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	r.Header.Set("Content-Type", "application/json")
	res, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func bodyOf(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// relaysLeft is how many relays the hub still holds for a manager or has handed to one.
func relaysLeft(h *Hub) int {
	h.relay.mu.Lock()
	defer h.relay.mu.Unlock()
	return len(h.relay.pending) + len(h.relay.owner) + len(h.relay.taken)
}

// A body of bodyStreamMin or more goes to a manager that takes body streams on a relay of its own, the request
// without it on the bus; a smaller one, or any body to a manager that does not name the capability, goes inline as
// before. Either way the route gets the same bytes and the app the manager's answer.
func TestLargeBodyGoesOnAStreamOfItsOwn(t *testing.T) {
	for _, streams := range []bool{true, false} {
		h, srv := bodyHub(t)
		m := &bodyManager{}
		connectBodyManager(t, h, srv, nil, streams, m)
		small, large := bodyOf(t, bodyStreamMin-1), bodyOf(t, bodyStreamMin)
		for _, body := range [][]byte{small, large} {
			if code, out := putRecording(t, h, srv, "r1", body); code != http.StatusOK || out != `{"size":`+jsonInt(len(body))+`}` {
				t.Fatalf("streams %v, %d bytes: %d %s", streams, len(body), code, out)
			}
		}
		how, got := m.requests()
		want := []string{assistantwire.MethodHTTP, assistantwire.MethodHTTP}
		if streams {
			want[1] = assistantwire.MethodHTTPStreamed
		}
		if !reflect.DeepEqual(how, want) {
			t.Fatalf("streams %v: the manager got %v, want %v", streams, how, want)
		}
		if !bytes.Equal(got[0].Body, small) || !bytes.Equal(got[1].Body, large) {
			t.Fatalf("streams %v: the bodies differ", streams)
		}
		if streams && (got[1].BodyStream == nil || got[1].BodyStream.Size != int64(len(large))) {
			t.Fatalf("the body stream as told: %+v", got[1].BodyStream)
		}
		eventually(t, 5*time.Second, "the relay let go", func() bool { return relaysLeft(h) == 0 })
	}
}

// A manager that turns the stream down before anything ran (a method it does not know, a body that did not arrive
// whole) gets the request again inline, the same request but for the body's place: the app sees one answer.
func TestTurnedDownStreamGoesInline(t *testing.T) {
	for _, code := range []string{bus.CodeUnknownMethod, assistantwire.CodeBodyIncomplete} {
		h, srv := bodyHub(t)
		m := &bodyManager{turn: code}
		connectBodyManager(t, h, srv, nil, true, m)
		body := bodyOf(t, bodyStreamMin+5)
		if status, out := putRecording(t, h, srv, "r%2F2", body); status != http.StatusOK || out != `{"size":`+jsonInt(len(body))+`}` {
			t.Fatalf("%s: %d %s", code, status, out)
		}
		how, got := m.requests()
		if want := []string{assistantwire.MethodHTTPStreamed + " turned down", assistantwire.MethodHTTP}; !reflect.DeepEqual(how, want) {
			t.Fatalf("%s: the manager got %v, want %v", code, how, want)
		}
		streamed, inline := got[0], got[1]
		if !bytes.Equal(inline.Body, body) || len(streamed.Body) != 0 || streamed.BodyStream == nil || inline.BodyStream != nil {
			t.Fatalf("%s: the body's place: streamed %d bytes %+v, inline %d bytes %+v", code, len(streamed.Body),
				streamed.BodyStream, len(inline.Body), inline.BodyStream)
		}
		streamed.Body, streamed.BodyStream, inline.Body = nil, nil, nil
		if !reflect.DeepEqual(streamed, inline) {
			t.Fatalf("%s: the request differs but for its body:\nstreamed %+v\ninline   %+v", code, streamed, inline)
		}
		if streamed.Path != "/api/v1/recordings/r%2F2" || streamed.Query != "x=1" {
			t.Fatalf("%s: path %q query %q", code, streamed.Path, streamed.Query)
		}
		eventually(t, 5*time.Second, "the relay let go", func() bool { return relaysLeft(h) == 0 })
	}
}

func TestStreamedRequestDoesNotRetryOtherErrors(t *testing.T) {
	h, srv := bodyHub(t)
	m := &bodyManager{turn: bus.CodeInvalidParams}
	connectBodyManager(t, h, srv, nil, true, m)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	req := assistantwire.HTTPRequest{Method: http.MethodPut, Path: "/example", Body: bodyOf(t, bodyStreamMin)}
	var res assistantwire.HTTPResponse
	err := h.callHTTP(ctx, assistantwire.AssistantID, req, &res)
	var coded *rpc.Error
	if !errors.As(err, &coded) || coded.Code != bus.CodeInvalidParams {
		t.Fatalf("callHTTP = %v, want the manager's invalid_params error", err)
	}
	how, _ := m.requests()
	if want := []string{assistantwire.MethodHTTPStreamed + " turned down"}; !reflect.DeepEqual(how, want) {
		t.Fatalf("the manager got %v, want %v", how, want)
	}
	eventually(t, 5*time.Second, "the relay let go", func() bool { return relaysLeft(h) == 0 })
}

// slowConn reads at most rate bytes a second, like a manager behind a slow link.
type slowConn struct {
	net.Conn
	rate   int
	start  time.Time
	read   int
	reads  int
	mu     sync.Mutex
	onBody func()
}

func (c *slowConn) Read(p []byte) (int, error) {
	if len(p) > 16<<10 {
		p = p[:16<<10]
	}
	n, err := c.Conn.Read(p)
	c.read += n
	if c.onBody != nil {
		c.mu.Lock()
		c.reads++
		found := c.reads >= 16
		notify := c.onBody
		if found {
			c.onBody = nil
		}
		c.mu.Unlock()
		if found {
			notify()
		}
	}
	if ahead := time.Duration(float64(c.read)/float64(c.rate)*float64(time.Second)) - time.Since(c.start); ahead > 0 {
		timer := time.NewTimer(ahead)
		<-timer.C
	}
	return n, err
}

func slowClient(rate int) *http.Client {
	var d net.Dialer
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &slowConn{Conn: c, rate: rate, start: time.Now()}, nil
	}}}
}

// The regression of #6574: while a large body goes to a manager behind a slow link, another call to it is not held
// up behind the body. Inline (a manager without the capability, as before), the small call waits for the whole
// body's line; on a stream of its own it answers at once.
func TestLargeBodyDoesNotHoldUpTheBus(t *testing.T) {
	const rate = 1 << 20 // bytes a second the manager reads
	body := bodyOf(t, 2<<20)
	smallWait := func(streams bool) time.Duration {
		t.Helper()
		h, srv := bodyHub(t)
		started := make(chan struct{})
		m := &bodyManager{}
		var begin sync.Once
		if streams {
			m.onRequest = func() { begin.Do(func() { close(started) }) }
		}
		client := slowClient(rate)
		transport := client.Transport.(*http.Transport)
		dial := transport.DialContext
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := dial(ctx, network, addr)
			if c != nil && !streams {
				c.(*slowConn).onBody = func() { begin.Do(func() { close(started) }) }
			}
			return c, err
		}
		connectBodyManager(t, h, srv, client, streams, m)
		done := make(chan string, 1)
		go func() {
			code, out := putRecording(t, h, srv, "big", body)
			done <- jsonInt(code) + " " + out
		}()
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("streams %v: the large body did not reach the manager", streams)
		}
		start := time.Now()
		if code, out := putRecording(t, h, srv, "small", []byte(`{}`)); code != http.StatusOK {
			t.Fatalf("streams %v: the small call: %d %s", streams, code, out)
		}
		took := time.Since(start)
		t.Logf("streams %v: the small call took %v", streams, took)
		select {
		case got := <-done:
			if got != `200 {"size":`+jsonInt(len(body))+`}` {
				t.Fatalf("streams %v: the large call: %s", streams, got)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("streams %v: the large call did not end", streams)
		}
		if h.busConnOf(assistantwire.AssistantID) == nil {
			t.Fatalf("streams %v: the bus dropped", streams)
		}
		return took
	}
	if took := smallWait(true); took > 700*time.Millisecond {
		t.Fatalf("with a body stream the small call took %v", took)
	}
	// The control: the same link without the stream holds the small call up, or the test proves nothing.
	if took := smallWait(false); took < 1200*time.Millisecond {
		t.Fatalf("inline the small call took only %v: the test's link is not slow enough to show the stall", took)
	}
}

// The assistant names the capability, and a large upload to one of its routes reaches the route: it answers it as
// it answers a small one.
func TestAssistantTakesBodyStreams(t *testing.T) {
	h, srv := bodyHub(t)
	logs := &syncBuffer{}
	h.logger = log.New(logs, "", 0)
	connectAssistant(t, h)
	if !h.managerCan(assistantwire.AssistantID, assistantwire.CapabilityHTTPBodyStream) {
		t.Fatal("the assistant's hello does not name the body stream")
	}
	code, small := putRecording(t, h, srv, "r1", []byte(`{}`))
	large := append([]byte(`{"pad":"`), bytes.Repeat([]byte("a"), bodyStreamMin)...)
	large = append(large, `"}`...)
	if c, out := putRecording(t, h, srv, "r1", large); c != code || out != small {
		t.Fatalf("large: %d %s, small: %d %s", c, out, code, small)
	}
	eventually(t, 5*time.Second, "the relay let go", func() bool { return relaysLeft(h) == 0 })
	if strings.Contains(logs.String(), "turned down") {
		t.Fatalf("the assistant turned the stream down:\n%s", logs.String())
	}
}
