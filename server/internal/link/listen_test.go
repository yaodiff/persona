package link

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/netaddr"
)

// The tests here play the hub as plan-hub-center.md section 4 has it: pin the connector's key from the pairing link,
// trade the code for a credential, and open streams with that credential to a fake resident node. Everything listens
// on 127.0.0.1 and a port the system picks, in temporary directories.

// fakeNode is a resident node: it echoes what a stream sends, after a greeting of its own.
type fakeNode struct {
	path    string
	attachs atomic.Int32
}

const nodeHello = "node says hello\n"

func newFakeNode(t *testing.T) *fakeNode {
	t.Helper()
	dir, err := os.MkdirTemp("", "jl") // short: a unix socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	n := &fakeNode{path: filepath.Join(dir, "node.sock")}
	ln, err := net.Listen("unix", n.path)
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
			n.attachs.Add(1)
			go func() {
				defer c.Close()
				if _, err := c.Write([]byte(nodeHello)); err != nil {
					return
				}
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return n
}

// connector runs persona-link (Run) on a link.json of its own in passive mode, listening on 127.0.0.1.
type connector struct {
	path   string
	addr   string // host:port it listens on
	status chan Status
	last   atomic.Pointer[Status]
}

func startConnector(t *testing.T, target string, o Options) *connector {
	t.Helper()
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	if _, err := SetMode(c.path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	c.run(t, target, o)
	s := c.waitFor(t, func(s Status) bool { return s.Mode == ModePassive && s.State == "unpaired" && s.Listen != "" })
	c.addr = s.Listen
	return c
}

func (c *connector) run(t *testing.T, target string, o Options) {
	o.Path, o.Target, o.Version = c.path, target, "test-version"
	o.Status = func(s Status) {
		c.last.Store(&s)
		select {
		case c.status <- s:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = Run(ctx, o) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (c *connector) waitFor(t *testing.T, ok func(Status) bool) Status {
	t.Helper()
	if s := c.last.Load(); s != nil && ok(*s) {
		return *s
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case s := <-c.status:
			if ok(s) {
				return s
			}
		case <-timeout:
			last := c.last.Load()
			t.Fatalf("no such status; the last was %+v", last)
		}
	}
}

// invite is `persona-link pair --listen --addr <where it listens>`.
func (c *connector) invite(t *testing.T) (fp, code string) {
	t.Helper()
	inv, err := PairListen(c.path, "", []string{c.addr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addrs, fp, code, err := ParseConnectorLink(inv.Link)
	if err != nil || len(addrs) != 1 || addrs[0] != c.addr || fp != inv.FP {
		t.Fatalf("link %s: %v %v", inv.Link, addrs, err)
	}
	return fp, code
}

// hubSide is the hub's side: a client pinned to the connector's key.
type hubSide struct {
	t      *testing.T
	base   string
	client *http.Client
}

func hubFor(t *testing.T, addr, fp string) *hubSide {
	h := &hubSide{t: t, base: "https://" + addr, client: pinned(fp, nil)}
	t.Cleanup(h.client.CloseIdleConnections)
	return h
}

type reply struct {
	status int
	body   map[string]string
	header http.Header
}

func (h *hubSide) do(method, path, token string, body any) reply {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out := reply{status: resp.StatusCode, header: resp.Header}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func (h *hubSide) pair(code, token string) reply {
	return h.do(http.MethodPost, ConnectorPairPath, "", map[string]string{"code": code, "token": token, "machine": "example-workstation",
		"hub_id": "0f1e2d3c-0000-4000-8000-000000000000", "hub_name": "家里"})
}

func (h *hubSide) stream(token string) (*websocket.Conn, *http.Response, error) {
	return h.streamWith(bearer(token))
}

// streamWith opens a stream with header: the credential, and what else the hub says.
func (h *hubSide) streamWith(header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "wss"+strings.TrimPrefix(h.base, "https")+ConnectorStreamPath, &websocket.DialOptions{
		HTTPClient: h.client, HTTPHeader: header, CompressionMode: websocket.CompressionContextTakeover})
}

var tokenA = "jd_" + strings.Repeat("a", 40)
var tokenB = "jd_" + strings.Repeat("b", 40)

func TestPassivePairsAndStreamsToTheNode(t *testing.T) {
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})
	fp, code := c.invite(t)
	if want, _ := Identity(TLSDir(c.path)); fp != want {
		t.Fatalf("link pin %s, identity %s", fp, want)
	}
	// A hub that pinned another key does not get to talk to it.
	if resp, err := pinned("another-key", nil).Get("https://" + c.addr + ConnectorInfoPath); err == nil || !errors.Is(err, errPin) {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("another pin: %v", err)
	}
	h := hubFor(t, c.addr, fp)
	c.waitFor(t, func(s Status) bool { return s.State == "unpaired" && s.CodeExpires != nil })

	if r := h.pair("jc_wrong", tokenA); r.status != http.StatusUnauthorized || r.body["error"] != "bad_code" {
		t.Fatalf("wrong code: %+v", r)
	}
	for _, bad := range []map[string]string{{"code": code, "token": "short", "machine": "example-workstation"}, {"code": code, "token": tokenA}, {"token": tokenA, "machine": "example-workstation"}} {
		if r := h.do(http.MethodPost, ConnectorPairPath, "", bad); r.status != http.StatusBadRequest {
			t.Fatalf("pairing with %v: %+v", bad, r)
		}
	}
	if r := h.pair(code, tokenA); r.status != http.StatusOK || r.body["version"] != "test-version" {
		t.Fatalf("pair: %+v", r)
	}
	if r := h.pair(code, tokenB); r.status != http.StatusUnauthorized {
		t.Fatalf("the code twice: %+v", r)
	}
	s := c.waitFor(t, func(s Status) bool { return s.State == "listening" })
	if s.Machine != "example-workstation" || s.HubName != "家里" || s.CodeExpires != nil || s.From != "127.0.0.1" || s.LastSeen == nil {
		t.Fatalf("status %+v", s)
	}

	// link.json keeps the credential's hash only, 0600; the identity is 0700 / 0600.
	raw, _ := os.ReadFile(c.path)
	cfg, _ := Read(c.path)
	if strings.Contains(string(raw), tokenA) || cfg.Passive.TokenSHA256 != hashHex(tokenA) || cfg.Passive.HubID == "" || cfg.Passive.Code != nil ||
		cfg.Mode != ModePassive {
		t.Fatalf("link.json %s", raw)
	}
	for p, want := range map[string]os.FileMode{c.path: 0o600, TLSDir(c.path): 0o700 | os.ModeDir,
		filepath.Join(TLSDir(c.path), "link.key"): 0o600, filepath.Join(TLSDir(c.path), "link.crt"): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode() != want {
			t.Errorf("%s: %v %v, want %v", p, fi.Mode(), err, want)
		}
	}

	if r := h.do(http.MethodGet, ConnectorInfoPath, "", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("info without a credential: %+v", r)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("info with another credential: %+v", r)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenA, nil); r.status != http.StatusOK || r.body["machine"] != "example-workstation" || r.body["version"] != "test-version" {
		t.Fatalf("info: %+v", r)
	}
	// Nothing else is there, with a credential or without.
	for _, req := range [][2]string{{"GET", "/"}, {"GET", "/api/v1/connector"}, {"GET", ConnectorInfoPath + "/"}, {"POST", ConnectorInfoPath},
		{"GET", ConnectorPairPath}, {"PUT", ConnectorStreamPath}, {"GET", "/api/v1/link"}, {"GET", "/../etc/passwd"}} {
		if r := h.do(req[0], req[1], tokenA, nil); r.status != http.StatusNotFound {
			t.Errorf("%s %s: %d", req[0], req[1], r.status)
		}
	}
	if r := h.do(http.MethodGet, ConnectorStreamPath, tokenA, nil); r.status != http.StatusBadRequest {
		t.Fatalf("a stream without the upgrade: %+v", r)
	}

	// The stream: the node's bytes, both ways, as they are.
	ws, resp, err := h.stream(tokenA)
	if err != nil {
		t.Fatalf("stream: %v %v", err, resp)
	}
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer nc.Close()
	hello := make([]byte, len(nodeHello))
	if _, err := io.ReadFull(nc, hello); err != nil || string(hello) != nodeHello {
		t.Fatalf("node hello %q %v", hello, err)
	}
	s = c.waitFor(t, func(s Status) bool { return s.State == "connected" })
	if s.Address != "127.0.0.1" || s.Node != "online" || s.Since == nil {
		t.Fatalf("connected status %+v", s)
	}
	payload := make([]byte, 300<<10) // every byte value, past any buffer size
	_, _ = rand.Read(payload)
	go func() { _, _ = nc.Write(payload) }()
	back := make([]byte, len(payload))
	if _, err := io.ReadFull(nc, back); err != nil || !bytes.Equal(back, payload) {
		t.Fatalf("echo: %v, same %v", err, bytes.Equal(back, payload))
	}
	// The hub ends the stream: the node sees EOF and goes, and the connector is back to listening.
	nc.Close()
	c.waitFor(t, func(s Status) bool { return s.State == "listening" })
	if node.attachs.Load() != 1 {
		t.Fatalf("%d node connections", node.attachs.Load())
	}
}

func TestPassiveRateLimitsFailedAuthentication(t *testing.T) {
	node := newFakeNode(t)
	lim := newAuthLimiter()
	lim.failures, lim.block = 3, time.Minute
	c := startConnector(t, node.path, Options{limiter: lim})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair %+v", r)
	}
	for i := range 3 {
		if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("failure %d: %+v", i+1, r)
		}
	}
	// Blocked now: even the right credential waits, and says how long.
	for _, token := range []string{tokenB, tokenA} {
		r := h.do(http.MethodGet, ConnectorInfoPath, token, nil)
		if retry, _ := strconv.Atoi(r.header.Get("Retry-After")); r.status != http.StatusTooManyRequests || r.body["error"] != "rate_limited" || retry < 1 || retry > 60 {
			t.Fatalf("blocked: %+v", r)
		}
	}
	if _, resp, err := h.stream(tokenA); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream while blocked: %v %v", err, resp)
	}
	// Wrong pairing codes count the same way.
	lim2 := newAuthLimiter()
	lim2.failures = 2
	c2 := startConnector(t, node.path, Options{limiter: lim2})
	fp2, code2 := c2.invite(t)
	h2 := hubFor(t, c2.addr, fp2)
	h2.pair("jc_x", tokenA)
	h2.pair("jc_y", tokenA)
	if r := h2.pair(code2, tokenA); r.status != http.StatusTooManyRequests {
		t.Fatalf("the right code from a blocked source: %+v", r)
	}
}

func TestPassiveCodeRunsOut(t *testing.T) {
	c := startConnector(t, newFakeNode(t).path, Options{})
	fp, code := c.invite(t)
	if err := Update(c.path, func(cfg *Config) error {
		cfg.Passive.Code.Expires = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r := hubFor(t, c.addr, fp).pair(code, tokenA); r.status != http.StatusUnauthorized || r.body["error"] != "bad_code" {
		t.Fatalf("expired code: %+v", r)
	}
	if cfg, _ := Read(c.path); cfg.Passive.Paired() {
		t.Fatal("paired with an expired code")
	}
}

func TestPassivePairingAgainEndsTheOldCredential(t *testing.T) {
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair %+v", r)
	}
	ws, _, err := h.stream(tokenA)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				ended <- err
				return
			}
		}
	}()
	c.waitFor(t, func(s Status) bool { return s.State == "connected" })
	// Another hub pairs (the center changed): the first one's stream ends with 4002 and its credential is no good.
	_, code2 := c.invite(t)
	if r := h.pair(code2, tokenB); r.status != http.StatusOK {
		t.Fatalf("pair again %+v", r)
	}
	select {
	case err := <-ended:
		if websocket.CloseStatus(err) != CloseRevoked {
			t.Fatalf("old stream ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("old stream still open")
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenA, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("old credential: %+v", r)
	}
	if _, resp, err := h.stream(tokenA); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old credential's stream: %v", err)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusOK {
		t.Fatalf("new credential: %+v", r)
	}
}

func TestPassiveWithoutTheNode(t *testing.T) {
	target := filepath.Join(t.TempDir(), "no-node.sock")
	c := startConnector(t, target, Options{})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	h.pair(code, tokenA)
	_, resp, err := h.stream(tokenA)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%v %v", err, resp)
	}
	// The same advice as active mode: how this OS runs the node, not the bare command (#3723). The hub shows the
	// answer's message on the machine's page; the menu bar shows node_error.
	want := nodeMissing(runtime.GOOS, target).Error()
	var body struct{ Error, Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error != "node_unavailable" || body.Message != want {
		t.Fatalf("answer %+v (%v), want message %q", body, err, want)
	}
	s := c.waitFor(t, func(s Status) bool { return s.Node == "offline" })
	if s.State != "listening" || s.NodeError != want {
		t.Fatalf("status %+v, want node_error %q", s, want)
	}
}

// A stream follows the heartbeat the hub says in PingHeader (hub.json link), either way; without one, or with one that
// is not two numbers, the connector keeps its own.
func TestPassiveStreamFollowsTheHubsHeartbeat(t *testing.T) {
	open := func(t *testing.T, o Options, ping string) *connector {
		t.Helper()
		node := newFakeNode(t)
		c := startConnector(t, node.path, o)
		fp, code := c.invite(t)
		h := hubFor(t, c.addr, fp)
		h.pair(code, tokenA)
		header := bearer(tokenA)
		if ping != "" {
			header.Set(PingHeader, ping)
		}
		ws, _, err := h.streamWith(header) // and never read again: no pongs
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		c.waitFor(t, func(s Status) bool { return s.State == "connected" })
		return c
	}
	for _, tc := range []struct {
		name, ping string
		o          Options
	}{
		{"shorter", PingHeaderValue(50*time.Millisecond, 50*time.Millisecond), Options{}}, // 10 s and 5 s: 15 s
		{"none", "", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
		{"not numbers", "soon later", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
		{"half", "60000", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := open(t, tc.o, tc.ping)
			// The latest status, not the queue: it holds statuses from before the stream.
			eventually(t, func() bool { return c.last.Load().State == "listening" }) // within 5 s
		})
	}
	t.Run("longer", func(t *testing.T) {
		c := open(t, Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond},
			PingHeaderValue(time.Minute, time.Minute))
		for until := time.Now().Add(time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			if s := c.last.Load(); s.State != "connected" {
				t.Fatalf("dropped despite the hub's longer heartbeat: %+v", s)
			}
		}
	})
}

// A request whose body stalls is closed after passiveReadTimeout, whatever the path (pairing reads the body before any
// code is checked; net/http drains a small unread body before it answers), while a stream outlives that deadline.
func TestPassiveClosesARequestWhoseBodyStalls(t *testing.T) {
	was := passiveReadTimeout
	passiveReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { passiveReadTimeout = was }) // after the connector's cleanup has stopped it
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})

	// The stream is opened first: the stalled requests below each take passiveReadTimeout to be closed, so once they
	// are, the stream's own request deadline has passed too.
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair: %+v", r)
	}
	ws, resp, err := h.stream(tokenA)
	if err != nil {
		t.Fatalf("stream: %v %v", err, resp)
	}
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer nc.Close()
	hello := make([]byte, len(nodeHello))
	if _, err := io.ReadFull(nc, hello); err != nil {
		t.Fatalf("node hello: %v", err)
	}

	for _, head := range []string{"POST " + ConnectorPairPath, "GET /elsewhere"} {
		conn, err := tls.Dial("tcp", c.addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, head+" HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 4000\r\n\r\n{\"code\":")
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(conn)
		conn.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("%s: the listener kept the connection of a stalled body open (read %q)", head, got)
		}
		if len(got) > 0 && !strings.HasPrefix(string(got), "HTTP/1.1 4") { // 400 from pairing, 404 elsewhere, or just closed
			t.Fatalf("%s: %q", head, got)
		}
	}

	if _, err := nc.Write([]byte("still here")); err != nil {
		t.Fatalf("write after the deadline: %v", err)
	}
	back := make([]byte, len("still here"))
	if _, err := io.ReadFull(nc, back); err != nil || string(back) != "still here" {
		t.Fatalf("echo after the deadline: %q %v", back, err)
	}
}

func TestModeSwitchesBothWays(t *testing.T) {
	node := newFakeNode(t)
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	c.run(t, node.path, Options{})
	c.waitFor(t, func(s Status) bool { return s.Mode == ModeActive && s.State == "unpaired" })
	if _, err := SetMode(c.path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s := c.waitFor(t, func(s Status) bool { return s.Mode == ModePassive && s.Listen != "" })
	addr := s.Listen
	if conn, err := net.Dial("tcp", addr); err != nil {
		t.Fatalf("not listening: %v", err)
	} else {
		conn.Close()
	}
	if _, err := SetMode(c.path, ModeActive, ""); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, func(s Status) bool { return s.Mode == ModeActive && s.State == "unpaired" })
	eventually(t, func() bool {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
		}
		return err != nil
	})
	cfg, _ := Read(c.path)
	if cfg.Passive == nil || cfg.Passive.Listen != "127.0.0.1:0" {
		t.Fatalf("passive mode's settings stay for switching back: %+v", cfg)
	}
}

func TestPassiveListenFailsWhenThePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	if _, err := SetMode(c.path, ModePassive, taken.Addr().String()); err != nil {
		t.Fatal(err)
	}
	c.run(t, filepath.Join(t.TempDir(), "x.sock"), Options{})
	s := c.waitFor(t, func(s Status) bool { return s.State == "waiting" })
	if !strings.Contains(s.Error, "端口被占用") || s.Retry == nil {
		t.Fatalf("status %+v", s)
	}
}

func TestActivePairingKeepsPassiveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		c.Mode, c.Passive = ModePassive, &Passive{Listen: ":18663", TokenSHA256: hashHex(tokenA), Machine: "example-workstation"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnpaired) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("only passive: %v", err)
	}
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "jd_link", "role": "link"})
	}))
	defer hub.Close()
	u, _ := netip.ParseAddrPort(strings.TrimPrefix(hub.URL, "https://"))
	link := fmt.Sprintf("persona://link?url=http%%3A%%2F%%2F127.0.0.1%%3A1&tls=%d&token=jp_x&hub=h1&fp=%s&machine=laptop", u.Port(), pinOf(hub))
	if _, err := Pair(context.Background(), link, "", "", path); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.CurrentMode() != ModeActive || c.Token != "jd_link" || c.Passive == nil || c.Passive.TokenSHA256 != hashHex(tokenA) {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestIdentityKeepsItsKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "link-tls")
	now := time.Now()
	id, err := loadIdentity(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	first := id.crt.Leaf
	if len(first.DNSNames) != 1 || first.DNSNames[0] != tlsName || len(first.IPAddresses) != 0 || first.NotAfter.Before(now.Add(300*24*time.Hour)) {
		t.Fatalf("certificate %v %v %v", first.DNSNames, first.IPAddresses, first.NotAfter)
	}
	again, _ := loadIdentity(dir, now)
	if again.fp != id.fp || !bytes.Equal(again.crt.Leaf.Raw, first.Raw) {
		t.Fatal("loading again changed the identity")
	}
	// Near its end the certificate is renewed with the same key: the pin stays.
	later := first.NotAfter.Add(-time.Hour)
	crt, err := again.certificate(later)
	if err != nil || bytes.Equal(crt.Leaf.Raw, first.Raw) || Pin(crt.Leaf) != id.fp || !crt.Leaf.NotAfter.After(later.Add(300*24*time.Hour)) {
		t.Fatalf("renewed %v", err)
	}
}

func TestListenAddresses(t *testing.T) {
	for raw, want := range map[string]string{"": ":18663", "18663": ":18663", ":1": ":1", "0.0.0.0:18663": "0.0.0.0:18663",
		"[::]:18663": "[::]:18663", "127.0.0.1:0": "127.0.0.1:0", "[fd00::1]:9": "[fd00::1]:9"} {
		if got, err := NormalizeListen(raw); err != nil || got != want {
			t.Errorf("NormalizeListen(%q) = %q %v, want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"host.example.com:1", ":99999", "x", "[fe80::1%en0]:1"} {
		if _, err := NormalizeListen(bad); err == nil {
			t.Errorf("NormalizeListen(%q) passed", bad)
		}
	}
	for raw, want := range map[string]string{"hub.example.com": "hub.example.com:18663", "2001:db8::5": "[2001:db8::5]:18663",
		"[2001:db8::5]:1": "[2001:db8::5]:1", "192.0.2.10:443": "192.0.2.10:443"} {
		if got, err := normalizeAddr(raw, ":18663"); err != nil || got != want {
			t.Errorf("normalizeAddr(%q) = %q %v, want %q", raw, got, err, want)
		}
	}
	if _, err := normalizeAddr("hub.example.com", "127.0.0.1:0"); err == nil {
		t.Error("no port to take")
	}
}

func TestListenCandidatesFollowActiveModesOrder(t *testing.T) {
	up := net.FlagUp
	stable := func(ip string) netaddr.Addr {
		return netaddr.Addr{IP: netip.MustParseAddr(ip), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true, ScopeKnown: true,
			Flags: netaddr.FlagPermanent}
	}
	addrs := []netaddr.Addr{
		{IP: netip.MustParseAddr("8.8.4.4"), Interface: "eth0", InterfaceFlags: up},
		stable("2400:cb00::10"),
		{IP: netip.MustParseAddr("2400:cb00::99"), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true, ScopeKnown: true, Flags: netaddr.FlagTemporary},
		{IP: netip.MustParseAddr("fd00::10"), Interface: "eth0", InterfaceFlags: up},
		{IP: netip.MustParseAddr("127.0.0.1"), Interface: "lo", InterfaceFlags: up | net.FlagLoopback},
		{IP: netip.MustParseAddr("fe80::1"), Interface: "eth0", InterfaceFlags: up},
		{IP: netip.MustParseAddr("fd00::20"), Interface: "docker0", InterfaceFlags: up, Virtual: true},
		{IP: netip.MustParseAddr("fd00::30"), Interface: "eth1"},                       // down
		{IP: netip.MustParseAddr("192.0.2.10"), Interface: "eth0", InterfaceFlags: up}, // documentation
	}
	old := localAddrs
	localAddrs = func() ([]netaddr.Addr, error) { return addrs, nil }
	defer func() { localAddrs = old }()
	got, err := ListenCandidates(":18663")
	if err != nil || strings.Join(got, " ") != "[fd00::10]:18663 [2400:cb00::10]:18663 8.8.4.4:18663" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := ListenCandidates("0.0.0.0:18663"); strings.Join(got, " ") != "8.8.4.4:18663" {
		t.Fatalf("IPv4 only: %v", got)
	}
	if got, _ := ListenCandidates("[fd00::10]:7"); strings.Join(got, " ") != "[fd00::10]:7" {
		t.Fatalf("one address: %v", got)
	}
	// pair --listen without --addr puts them in the link, in that order.
	path := filepath.Join(t.TempDir(), "link.json")
	inv, err := PairListen(path, "", nil, time.Now())
	if err != nil || !strings.HasPrefix(inv.Link, "persona://connector?addr=%5Bfd00%3A%3A10%5D%3A18663&addr=%5B2400%3Acb00%3A%3A10%5D%3A18663&addr=8.8.4.4%3A18663&fp=") {
		t.Fatalf("%+v %v", inv, err)
	}
	localAddrs = func() ([]netaddr.Addr, error) { return addrs[4:6], nil }
	if _, err := PairListen(path, "", nil, time.Now()); !errors.Is(err, ErrNoAddress) || !strings.Contains(err.Error(), "--addr") {
		t.Fatalf("nothing usable: %v", err)
	}
	// Every interface virtual (a container's eth0): those count then, tunnels and bridges still not.
	localAddrs = func() ([]netaddr.Addr, error) {
		return []netaddr.Addr{
			{IP: netip.MustParseAddr("fd00::42"), Interface: "eth0", InterfaceFlags: up, Virtual: true},
			{IP: netip.MustParseAddr("fd00::43"), Interface: "docker0", InterfaceFlags: up, Virtual: true},
			{IP: netip.MustParseAddr("fd00::44"), Interface: "utun3", InterfaceFlags: up, Virtual: true},
		}, nil
	}
	if got, _ := ListenCandidates(":18663"); strings.Join(got, " ") != "[fd00::42]:18663" {
		t.Fatalf("all virtual: %v", got)
	}
}

func TestConnectorLinks(t *testing.T) {
	link := ConnectorLink([]string{"[fd00::10]:18663", "hub.example.com:18663"}, "pin_-x", "jc_abc")
	addrs, fp, code, err := ParseConnectorLink(" " + link + "\n")
	if err != nil || strings.Join(addrs, ",") != "[fd00::10]:18663,hub.example.com:18663" || fp != "pin_-x" || code != "jc_abc" {
		t.Fatalf("%v %v %v %v", addrs, fp, code, err)
	}
	for _, bad := range []string{"persona://link?addr=a:1&fp=x&code=y", "persona://connector?fp=x&code=y", "persona://connector?addr=a:1&code=y",
		"persona://connector?addr=nohost&fp=x&code=y"} {
		if _, _, _, err := ParseConnectorLink(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAuthLimiterMakesRoom(t *testing.T) {
	l := newAuthLimiter()
	l.failures, l.max = 2, 2
	now := time.Now()
	l.fail("a", now)
	l.fail("a", now) // a is blocked
	l.fail("b", now)
	l.fail("c", now) // full: b goes (not blocked), a stays blocked
	if l.retry("a", now) == 0 || len(l.buckets) != 2 {
		t.Fatalf("%v", l.buckets)
	}
	if l.retry("a", now.Add(authBlock)) != 0 {
		t.Fatal("still blocked after the block")
	}
	if sourceKey(netip.MustParseAddr("2400:cb00::1")) != sourceKey(netip.MustParseAddr("2400:cb00::ffff")) ||
		sourceKey(netip.MustParseAddr("::ffff:203.0.113.5")) != "203.0.113.5" {
		t.Fatal("sources")
	}
}

// Re-pairing with a new hub while an old hub's GET /info is updating MachineName must never revert
// in-memory credentials back to the revoked hub or leak the new hub's attention state.
func TestPassiveKeepMachineNameDoesNotRevertConcurrentRepair(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	invA, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _, codeA, err := ParseConnectorLink(invA.Link)
	if err != nil {
		t.Fatal(err)
	}
	s := &passive{
		o:       Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
		rep:     &reporter{},
		lim:     newAuthLimiter(),
		streams: map[*pstream]struct{}{},
		stopped: make(chan struct{}),
	}
	serve := func(method, target, remoteAddr, token string, hdr http.Header, body any) *httptest.ResponseRecorder {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, target, rd)
		req.RemoteAddr = remoteAddr
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for k, vs := range hdr {
			for _, v := range vs {
				req.Header.Add(k, v)
			}
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	// Pair Hub A first.
	if rec := serve(http.MethodPost, ConnectorPairPath, "127.0.0.1:43210", "", nil, map[string]string{
		"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
	}); rec.Code != http.StatusOK {
		t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
	}
	// Issue a new invite so Hub B can pair concurrently while Hub A's /info is in flight.
	invB, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _, codeB, err := ParseConnectorLink(invB.Link)
	if err != nil {
		t.Fatal(err)
	}
	cfgWithCodeB, err := Read(path)
	if err != nil || cfgWithCodeB.Passive == nil {
		t.Fatalf("read after invite B: %+v %v", cfgWithCodeB, err)
	}
	s.setConfig(*cfgWithCodeB.Passive)

	hookRan := false
	s.testAfterKeepNameUpdate = func() {
		hookRan = true
		if rec := serve(http.MethodPost, ConnectorPairPath, "127.0.0.2:43211", "", nil, map[string]string{
			"code": codeB, "token": tokenB, "machine": "workstation-b", "hub_id": "hub-b", "hub_name": "Hub B",
		}); rec.Code != http.StatusOK {
			t.Errorf("concurrent pair hub B: %d %s", rec.Code, rec.Body.String())
		}
		// Hub B posts an attention summary right after pairing.
		if rec := serve(http.MethodPost, ConnectorAttentionPath, "127.0.0.2:43211", tokenB, nil, map[string]any{
			"rev": "rev-hub-b", "count": 1,
		}); rec.Code != http.StatusNoContent {
			t.Errorf("hub B attention: %d %s", rec.Code, rec.Body.String())
		}
	}

	// Hub A calls GET /info with a changed machine name, hitting the post-Update / pre-lock window.
	hdr := http.Header{MachineNameHeader: {url.QueryEscape("RenamedByA")}}
	recA := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, hdr, nil)
	if !hookRan {
		t.Fatal("test hook did not run")
	}
	if recA.Code != http.StatusUnauthorized {
		t.Fatalf("stale Hub A /info after concurrent re-pair: got %d (%s), want 401", recA.Code, recA.Body.String())
	}

	// In-memory and on-disk config must still hold Hub B's credential, never rolled back to revoked Hub A,
	// and Hub B's last-seen source must not be overwritten by stale Hub A.
	if got := s.config(); got.TokenSHA256 != hashHex(tokenB) || got.HubID != "hub-b" || got.MachineName != "" {
		t.Fatalf("in-memory passive config reverted after keepMachineName: %+v", got)
	}
	diskCfg, err := Read(path)
	if err != nil || diskCfg.Passive == nil {
		t.Fatalf("Read(path): %+v %v", diskCfg, err)
	}
	if diskCfg.Passive.TokenSHA256 != hashHex(tokenB) || diskCfg.Passive.HubID != "hub-b" || diskCfg.Passive.MachineName != "" {
		t.Fatalf("on-disk passive config corrupted after keepMachineName: %+v", diskCfg.Passive)
	}
	s.mu.Lock()
	gotFrom := s.from
	s.mu.Unlock()
	if gotFrom != "127.0.0.2" {
		t.Fatalf("stale Hub A overwrote Hub B last-seen source: got %q, want 127.0.0.2", gotFrom)
	}
	// Hub B must still be authorized with its attention rev intact, and revoked Hub A must be rejected.
	recB := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.2:43211", tokenB, nil, nil)
	if recB.Code != http.StatusOK {
		t.Fatalf("newly paired Hub B rejected after Hub A keepMachineName: %d %s", recB.Code, recB.Body.String())
	}
	var infoB ConnectorInfo
	if err := json.Unmarshal(recB.Body.Bytes(), &infoB); err != nil {
		t.Fatalf("decode Hub B info: %v", err)
	}
	if infoB.AttentionRev != "rev-hub-b" {
		t.Fatalf("Hub B attention_rev = %q, want rev-hub-b", infoB.AttentionRev)
	}
	if rec := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked Hub A re-admitted after keepMachineName: %d %s", rec.Code, rec.Body.String())
	}

	// Subcase: Hub B unpaired concurrently between keepMachineName Update and s.mu re-check.
	s.testAfterKeepNameUpdate = func() {
		s.setConfig(Passive{Listen: "127.0.0.1:0"})
	}
	recUnpaired := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.2:43211", tokenB,
		http.Header{MachineNameHeader: {url.QueryEscape("RenamedBeforeUnpair")}}, nil)
	if recUnpaired.Code != http.StatusUnauthorized {
		t.Fatalf("info after concurrent unpair: got %d (%s), want 401", recUnpaired.Code, recUnpaired.Body.String())
	}
	if got := s.config(); got.Paired() || got.MachineName != "" {
		t.Fatalf("unpaired passive config reverted after keepMachineName: %+v", got)
	}

	// Subcase: link.json is replaced on disk before the listener reloads s.cfg; keepMachineName
	// returns errPairedAgain and /info must reject immediately with 401 without updating lastSeen.
	s.testAfterKeepNameUpdate = nil
	s.setConfig(Passive{Listen: "127.0.0.1:0", TokenSHA256: hashHex(tokenA), Machine: "workstation-a", HubID: "hub-a"})
	s.mu.Lock()
	s.from = "127.0.0.9"
	s.mu.Unlock()
	recDiskStale := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA,
		http.Header{MachineNameHeader: {url.QueryEscape("StaleOnDisk")}}, nil)
	if recDiskStale.Code != http.StatusUnauthorized {
		t.Fatalf("info with stale on-disk credential: got %d (%s), want 401", recDiskStale.Code, recDiskStale.Body.String())
	}
	s.mu.Lock()
	gotFromAfterDiskStale := s.from
	s.mu.Unlock()
	if gotFromAfterDiskStale != "127.0.0.9" {
		t.Fatalf("stale on-disk /info updated s.from to %q, want 127.0.0.9", gotFromAfterDiskStale)
	}
}

// Updating MachineName while the same hub remains paired must not clobber a pairing code
// concurrently loaded into s.cfg via setConfig.
func TestPassiveKeepMachineNamePreservesConcurrentPairingCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	invA, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _, codeA, err := ParseConnectorLink(invA.Link)
	if err != nil {
		t.Fatal(err)
	}
	s := &passive{
		o:       Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
		rep:     &reporter{},
		lim:     newAuthLimiter(),
		streams: map[*pstream]struct{}{},
		stopped: make(chan struct{}),
	}
	pairBody, _ := json.Marshal(map[string]string{
		"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
	})
	pairReq := httptest.NewRequest(http.MethodPost, ConnectorPairPath, bytes.NewReader(pairBody))
	pairReq.RemoteAddr = "127.0.0.1:43210"
	pairRec := httptest.NewRecorder()
	s.ServeHTTP(pairRec, pairReq)
	if pairRec.Code != http.StatusOK {
		t.Fatalf("pair hub A: %d %s", pairRec.Code, pairRec.Body.String())
	}

	var wantCodeSHA string
	s.testAfterKeepNameUpdate = func() {
		invNext, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
		if err != nil {
			t.Errorf("PairListen: %v", err)
			return
		}
		_, _, codeNext, err := ParseConnectorLink(invNext.Link)
		if err != nil {
			t.Errorf("ParseConnectorLink: %v", err)
			return
		}
		wantCodeSHA = hashHex(codeNext)
		cfg, err := Read(path)
		if err != nil || cfg.Passive == nil {
			t.Errorf("Read: %+v %v", cfg, err)
			return
		}
		// Simulate the listener loading the new pairing code before MachineName reached s.cfg.
		p := *cfg.Passive
		p.MachineName = ""
		s.setConfig(p)
	}

	infoReq := httptest.NewRequest(http.MethodGet, ConnectorInfoPath, nil)
	infoReq.RemoteAddr = "127.0.0.1:43210"
	infoReq.Header.Set("Authorization", "Bearer "+tokenA)
	infoReq.Header.Set(MachineNameHeader, url.QueryEscape("RenamedByA"))
	infoRec := httptest.NewRecorder()
	s.ServeHTTP(infoRec, infoReq)
	if infoRec.Code != http.StatusOK {
		t.Fatalf("info: %d %s", infoRec.Code, infoRec.Body.String())
	}
	got := s.config()
	if got.MachineName != "RenamedByA" {
		t.Fatalf("MachineName = %q, want RenamedByA", got.MachineName)
	}
	if got.Code == nil || got.Code.SHA256 != wantCodeSHA {
		t.Fatalf("concurrently loaded pairing code was clobbered: %+v (want %s)", got.Code, wantCodeSHA)
	}
	s.rep.mu.Lock()
	repStatus := s.rep.st
	s.rep.mu.Unlock()
	if repStatus.MachineName != "RenamedByA" || repStatus.LastSeen == nil {
		t.Fatalf("reported Status missing updated MachineName/LastSeen: %+v", repStatus)
	}
}

type hookReader struct {
	before func()
	once   bool
	r      io.Reader
}

func (h *hookReader) Read(p []byte) (int, error) {
	if !h.once {
		h.once = true
		if h.before != nil {
			h.before()
		}
	}
	return h.r.Read(p)
}

// A stale hub's POST /attention that loses a concurrent re-pair race while its body is read
// must receive 401 without overwriting the new hub's lastSeen/from or Status.
func TestPassiveTakeAttentionStaleHubDoesNotClobberSeen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	var repMu sync.Mutex
	var reports int
	s := &passive{
		o:       Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
		rep:     &reporter{out: func(Status) { repMu.Lock(); reports++; repMu.Unlock() }},
		lim:     newAuthLimiter(),
		cfg:     Passive{Listen: "127.0.0.1:0", TokenSHA256: hashHex(tokenA), Machine: "workstation-a", HubID: "hub-a"},
		streams: map[*pstream]struct{}{},
		stopped: make(chan struct{}),
	}
	hubBTime := time.Now().Add(-10 * time.Second)
	var reportsAfterHubB int
	body := &hookReader{
		before: func() {
			s.setConfig(Passive{Listen: "127.0.0.1:0", TokenSHA256: hashHex(tokenB), Machine: "workstation-b", HubID: "hub-b"})
			s.mu.Lock()
			s.from = "127.0.0.2"
			s.lastSeen = hubBTime
			s.attention = &Attention{Rev: "rev-hub-b", NeedsYou: 2}
			s.reportLocked()
			s.mu.Unlock()
			repMu.Lock()
			reportsAfterHubB = reports
			repMu.Unlock()
		},
		r: strings.NewReader(`{"rev":"rev-hub-a","needs_you":9}`),
	}
	req := httptest.NewRequest(http.MethodPost, ConnectorAttentionPath, body)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Authorization", "Bearer "+tokenA)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale Hub A /attention: got %d (%s), want 401", rec.Code, rec.Body.String())
	}
	s.mu.Lock()
	gotFrom, gotSeen, gotAttn := s.from, s.lastSeen, s.attention
	s.mu.Unlock()
	if gotFrom != "127.0.0.2" || !gotSeen.Equal(hubBTime) {
		t.Fatalf("stale Hub A clobbered Hub B seen metadata: from=%q lastSeen=%v (want 127.0.0.2 / %v)", gotFrom, gotSeen, hubBTime)
	}
	if gotAttn == nil || gotAttn.Rev != "rev-hub-b" {
		t.Fatalf("Hub B attention corrupted: %+v", gotAttn)
	}
	s.rep.mu.Lock()
	st := s.rep.st
	s.rep.mu.Unlock()
	if st.From != "127.0.0.2" || st.LastSeen == nil || !st.LastSeen.Equal(hubBTime) {
		t.Fatalf("reported Status clobbered by stale Hub A: %+v", st)
	}
	repMu.Lock()
	gotReports := reports
	repMu.Unlock()
	if gotReports != reportsAfterHubB {
		t.Fatalf("stale Hub A /attention emitted %d spurious Status reports", gotReports-reportsAfterHubB)
	}
}

// A stale hub's GET /stream whose resident-node dial fails after another hub pairs must return
// 401 without clobbering the new hub's node/from state or leaking the local socket path.
func TestPassiveStreamDialErrorStaleHubReturns401WithoutClobberingNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	secretTarget := filepath.Join(t.TempDir(), "private-resident-node.sock")
	s := &passive{
		o:       Options{Path: path, Target: secretTarget, Version: "test-version", Logf: func(string, ...any) {}},
		rep:     &reporter{},
		lim:     newAuthLimiter(),
		cfg:     Passive{Listen: "127.0.0.1:0", TokenSHA256: hashHex(tokenA), Machine: "workstation-a", HubID: "hub-a"},
		streams: map[*pstream]struct{}{},
		stopped: make(chan struct{}),
	}
	hubBTime := time.Now().Add(-10 * time.Second)
	s.testDialNode = func() (net.Conn, error) {
		s.setConfig(Passive{Listen: "127.0.0.1:0", TokenSHA256: hashHex(tokenB), Machine: "workstation-b", HubID: "hub-b"})
		s.mu.Lock()
		s.from = "127.0.0.2"
		s.lastSeen = hubBTime
		s.node, s.nodeErr = "online", ""
		s.reportLocked()
		s.mu.Unlock()
		return nil, errors.New("dial failed")
	}

	req := httptest.NewRequest(http.MethodGet, ConnectorStreamPath, nil)
	req.RemoteAddr = "127.0.0.1:43210"
	req.Header.Set("Authorization", "Bearer "+tokenA)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale Hub A /stream dial error: got %d (%s), want 401", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secretTarget) {
		t.Fatalf("stale Hub A /stream response leaked local socket path: %s", rec.Body.String())
	}
	s.mu.Lock()
	gotNode, gotNodeErr, gotFrom, gotSeen := s.node, s.nodeErr, s.from, s.lastSeen
	s.mu.Unlock()
	if gotNode != "online" || gotNodeErr != "" {
		t.Fatalf("stale Hub A clobbered Hub B node status: node=%q err=%q", gotNode, gotNodeErr)
	}
	if gotFrom != "127.0.0.2" || !gotSeen.Equal(hubBTime) {
		t.Fatalf("stale Hub A clobbered Hub B seen metadata: from=%q lastSeen=%v", gotFrom, gotSeen)
	}
}

// A lagging POST /pair from Hub A must not roll back s.cfg, close Hub B's streams, clear Hub B's
// attention, or clobber Hub B's lastSeen/from if Hub B paired after Hub A's Update released link.json.
func TestPassivePairDoesNotRevertConcurrentRepairOrClobberState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	invA, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _, codeA, err := ParseConnectorLink(invA.Link)
	if err != nil {
		t.Fatal(err)
	}

	var repMu sync.Mutex
	var reports int
	s := &passive{
		o:       Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
		rep:     &reporter{out: func(Status) { repMu.Lock(); reports++; repMu.Unlock() }},
		lim:     newAuthLimiter(),
		streams: map[*pstream]struct{}{},
		stopped: make(chan struct{}),
	}
	serve := func(method, target, remoteAddr, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, target, rd)
		req.RemoteAddr = remoteAddr
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}

	var hubBStreamClosed atomic.Bool
	var hubBSeen time.Time
	var reportsAfterHubB int
	var hookRan atomic.Bool
	s.testAfterPairUpdate = func() {
		if !hookRan.CompareAndSwap(false, true) {
			return
		}
		invB, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
		if err != nil {
			t.Errorf("PairListen B: %v", err)
			return
		}
		_, _, codeB, err := ParseConnectorLink(invB.Link)
		if err != nil {
			t.Errorf("ParseConnectorLink B: %v", err)
			return
		}
		if rec := serve(http.MethodPost, ConnectorPairPath, "127.0.0.2:43211", "", map[string]string{
			"code": codeB, "token": tokenB, "machine": "workstation-b", "hub_id": "hub-b", "hub_name": "Hub B",
		}); rec.Code != http.StatusOK {
			t.Errorf("pair hub B: %d %s", rec.Code, rec.Body.String())
			return
		}
		if rec := serve(http.MethodPost, ConnectorAttentionPath, "127.0.0.2:43211", tokenB, map[string]any{
			"rev": "rev-hub-b", "needs_you": 1,
		}); rec.Code != http.StatusNoContent {
			t.Errorf("hub B attention: %d %s", rec.Code, rec.Body.String())
			return
		}
		s.mu.Lock()
		s.streams[&pstream{
			token: hashHex(tokenB),
			since: time.Now(),
			from:  "127.0.0.2",
			end:   func(websocket.StatusCode, string) { hubBStreamClosed.Store(true) },
		}] = struct{}{}
		hubBSeen = s.lastSeen
		s.mu.Unlock()
		repMu.Lock()
		reportsAfterHubB = reports
		repMu.Unlock()
	}

	if rec := serve(http.MethodPost, ConnectorPairPath, "127.0.0.1:43210", "", map[string]string{
		"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
	}); rec.Code != http.StatusOK {
		t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
	}
	if !hookRan.Load() {
		t.Fatal("testAfterPairUpdate hook did not run")
	}

	if got := s.config(); got.TokenSHA256 != hashHex(tokenB) || got.HubID != "hub-b" {
		t.Fatalf("lagging Hub A pair rolled back s.cfg: %+v", got)
	}
	if hubBStreamClosed.Load() {
		t.Fatal("lagging Hub A pair closed Hub B's active stream")
	}
	s.mu.Lock()
	gotFrom, gotSeen, gotAttn := s.from, s.lastSeen, s.attention
	s.mu.Unlock()
	if gotFrom != "127.0.0.2" || !gotSeen.Equal(hubBSeen) {
		t.Fatalf("lagging Hub A pair clobbered Hub B seen metadata: from=%q seen=%v (want 127.0.0.2 / %v)", gotFrom, gotSeen, hubBSeen)
	}
	if gotAttn == nil || gotAttn.Rev != "rev-hub-b" {
		t.Fatalf("lagging Hub A pair wiped Hub B attention: %+v", gotAttn)
	}
	repMu.Lock()
	gotReports := reports
	repMu.Unlock()
	if gotReports != reportsAfterHubB {
		t.Fatalf("lagging Hub A pair emitted %d spurious Status reports", gotReports-reportsAfterHubB)
	}

	recB := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.2:43211", tokenB, nil)
	if recB.Code != http.StatusOK {
		t.Fatalf("Hub B /info rejected after lagging Hub A pair: %d %s", recB.Code, recB.Body.String())
	}
	var infoB ConnectorInfo
	if err := json.Unmarshal(recB.Body.Bytes(), &infoB); err != nil || infoB.AttentionRev != "rev-hub-b" {
		t.Fatalf("Hub B /info = %+v (err=%v), want attention_rev=rev-hub-b", infoB, err)
	}
	if recA := serve(http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, nil); recA.Code != http.StatusUnauthorized {
		t.Fatalf("revoked Hub A /info admitted after lagging pair: %d %s", recA.Code, recA.Body.String())
	}
}

// A lagging POST /pair must preserve a concurrently issued invite code and must not undo a
// concurrent unpair or switch to active mode.
func TestPassivePairPreservesConcurrentInviteUnpairAndModeSwitch(t *testing.T) {
	setup := func(t *testing.T) (string, string, *passive) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "link.json")
		if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		invA, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, _, codeA, err := ParseConnectorLink(invA.Link)
		if err != nil {
			t.Fatal(err)
		}
		s := &passive{
			o:       Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
			rep:     &reporter{},
			lim:     newAuthLimiter(),
			streams: map[*pstream]struct{}{},
			stopped: make(chan struct{}),
		}
		return path, codeA, s
	}
	doPairA := func(t *testing.T, s *passive, codeA string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{
			"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
		})
		req := httptest.NewRequest(http.MethodPost, ConnectorPairPath, bytes.NewReader(body))
		req.RemoteAddr = "127.0.0.1:43210"
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
		}
	}

	t.Run("preserves_concurrent_invite_code", func(t *testing.T) {
		path, codeA, s := setup(t)
		var wantCodeSHA string
		s.testAfterPairUpdate = func() {
			invNext, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
			if err != nil {
				t.Errorf("PairListen next: %v", err)
				return
			}
			_, _, codeNext, err := ParseConnectorLink(invNext.Link)
			if err != nil {
				t.Errorf("ParseConnectorLink next: %v", err)
				return
			}
			wantCodeSHA = hashHex(codeNext)
		}
		doPairA(t, s, codeA)
		got := s.config()
		if !got.Paired() || got.TokenSHA256 != hashHex(tokenA) {
			t.Fatalf("Hub A not paired: %+v", got)
		}
		if got.Code == nil || got.Code.SHA256 != wantCodeSHA {
			t.Fatalf("concurrent invite code was clobbered by pair(): %+v (want %s)", got.Code, wantCodeSHA)
		}
		s.mu.Lock()
		gotFrom := s.from
		s.mu.Unlock()
		if gotFrom != "127.0.0.1" {
			t.Fatalf("Hub A last-seen source = %q, want 127.0.0.1", gotFrom)
		}
	})

	t.Run("does_not_undo_concurrent_unpair", func(t *testing.T) {
		path, codeA, s := setup(t)
		s.mu.Lock()
		s.from = "127.0.0.9"
		s.mu.Unlock()
		s.testAfterPairUpdate = func() {
			if err := Update(path, func(c *Config) error {
				if c.Passive != nil {
					c.Passive.TokenSHA256, c.Passive.HubID, c.Passive.HubName, c.Passive.PairedAt = "", "", "", nil
				}
				return nil
			}); err != nil {
				t.Errorf("unpair update: %v", err)
			}
		}
		doPairA(t, s, codeA)
		if got := s.config(); got.Paired() {
			t.Fatalf("lagging pair() undid concurrent unpair: %+v", got)
		}
		s.mu.Lock()
		gotFrom := s.from
		s.mu.Unlock()
		if gotFrom != "127.0.0.9" {
			t.Fatalf("lagging pair() overwrote s.from after concurrent unpair: got %q, want 127.0.0.9", gotFrom)
		}
		req := httptest.NewRequest(http.MethodGet, ConnectorInfoPath, nil)
		req.RemoteAddr = "127.0.0.1:43210"
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("unpaired Hub A /info got %d, want 401", rec.Code)
		}
	})

	t.Run("does_not_reauthorize_after_mode_switch", func(t *testing.T) {
		path, codeA, s := setup(t)
		var reports atomic.Int32
		s.rep = &reporter{out: func(Status) { reports.Add(1) }}
		s.mu.Lock()
		s.cfg = Passive{Listen: "127.0.0.1:0", HubID: "hub-prev", Machine: "workstation-prev", TokenSHA256: hashHex(tokenB)}
		s.attention = &Attention{Rev: "rev-prev", NeedsYou: 1}
		s.mu.Unlock()
		s.testAfterPairUpdate = func() {
			if _, err := SetMode(path, ModeActive, ""); err != nil {
				t.Errorf("SetMode active: %v", err)
			}
		}
		doPairA(t, s, codeA)
		if got := s.config(); !samePassive(got, Passive{}) {
			t.Fatalf("lagging pair() left s.cfg populated after switch to active mode: %+v", got)
		}
		s.mu.Lock()
		gotAttn := s.attention
		s.mu.Unlock()
		if gotAttn != nil {
			t.Fatalf("lagging pair() left s.attention populated after switch to active mode: %+v", gotAttn)
		}
		if reports.Load() == 0 {
			t.Fatal("clearing s.cfg after switch to active mode did not emit a Status report")
		}
		for _, tok := range []string{tokenA, tokenB} {
			req := httptest.NewRequest(http.MethodGet, ConnectorInfoPath, nil)
			req.RemoteAddr = "127.0.0.1:43210"
			req.Header.Set("Authorization", "Bearer "+tok)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("active-mode passive /info got %d, want 401", rec.Code)
			}
		}
	})

	t.Run("does_not_reauthorize_after_config_deleted", func(t *testing.T) {
		path, codeA, s := setup(t)
		var reports atomic.Int32
		s.rep = &reporter{out: func(Status) { reports.Add(1) }}
		s.mu.Lock()
		s.cfg = Passive{Listen: "127.0.0.1:0", HubID: "hub-prev", Machine: "workstation-prev", TokenSHA256: hashHex(tokenB)}
		s.attention = &Attention{Rev: "rev-prev", NeedsYou: 1}
		s.mu.Unlock()
		s.testAfterPairUpdate = func() {
			if err := os.Remove(path); err != nil {
				t.Errorf("remove link.json: %v", err)
			}
		}
		doPairA(t, s, codeA)
		if got := s.config(); !samePassive(got, Passive{}) {
			t.Fatalf("lagging pair() left s.cfg populated after link.json removal: %+v", got)
		}
		s.mu.Lock()
		gotAttn := s.attention
		s.mu.Unlock()
		if gotAttn != nil {
			t.Fatalf("lagging pair() left s.attention populated after link.json removal: %+v", gotAttn)
		}
		if reports.Load() == 0 {
			t.Fatal("clearing s.cfg after link.json removal did not emit a Status report")
		}
		for _, tok := range []string{tokenA, tokenB} {
			req := httptest.NewRequest(http.MethodGet, ConnectorInfoPath, nil)
			req.RemoteAddr = "127.0.0.1:43210"
			req.Header.Set("Authorization", "Bearer "+tok)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("deleted-config passive /info got %d, want 401", rec.Code)
			}
		}
	})
}

// A transient non-ErrNotExist Read error during syncFromDisk after pair()'s Update must still
// revoke the previous hub and activate the new pairing when s.cfg still holds the pre-pair state,
// while never rolling back a concurrent newer pairing that already updated s.cfg in memory.
func TestPassivePairTransientReadErrorFallback(t *testing.T) {
	const tokenC = "hub-c-credential-at-least-32-chars-0123456789"

	newServer := func(t *testing.T) (string, *passive, *[]string, *sync.Mutex) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "link.json")
		if _, err := SetMode(path, ModePassive, "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		var logMu sync.Mutex
		var logs []string
		s := &passive{
			o: Options{
				Path:    path,
				Version: "test-version",
				Logf: func(format string, args ...any) {
					logMu.Lock()
					logs = append(logs, fmt.Sprintf(format, args...))
					logMu.Unlock()
				},
			},
			rep:     &reporter{},
			lim:     newAuthLimiter(),
			streams: map[*pstream]struct{}{},
			stopped: make(chan struct{}),
		}
		return path, s, &logs, &logMu
	}
	serve := func(t *testing.T, s *passive, method, target, remoteAddr, token string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, target, rd)
		req.RemoteAddr = remoteAddr
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		return rec
	}
	issueCode := func(t *testing.T, path string) string {
		t.Helper()
		inv, err := PairListen(path, "", []string{"127.0.0.1:18663"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, _, code, err := ParseConnectorLink(inv.Link)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	hasLog := func(logs *[]string, mu *sync.Mutex, substr string) bool {
		mu.Lock()
		defer mu.Unlock()
		for _, line := range *logs {
			if strings.Contains(line, substr) {
				return true
			}
		}
		return false
	}

	t.Run("revokes_previous_hub_and_activates_new_hub", func(t *testing.T) {
		path, s, logs, logMu := newServer(t)
		codeA := issueCode(t, path)
		if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.1:43210", "", map[string]string{
			"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
		}); rec.Code != http.StatusOK {
			t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
		}
		if rec := serve(t, s, http.MethodPost, ConnectorAttentionPath, "127.0.0.1:43210", tokenA, map[string]any{
			"rev": "rev-hub-a", "needs_you": 2,
		}); rec.Code != http.StatusNoContent {
			t.Fatalf("hub A attention: %d %s", rec.Code, rec.Body.String())
		}
		var closedCode atomic.Int32
		s.mu.Lock()
		s.streams[&pstream{
			token: hashHex(tokenA),
			since: time.Now(),
			from:  "127.0.0.1",
			end:   func(code websocket.StatusCode, _ string) { closedCode.Store(int32(code)) },
		}] = struct{}{}
		s.mu.Unlock()

		codeB := issueCode(t, path)
		s.testAfterPairUpdate = func() {
			s.testReadConfig = func(string) (*Config, error) {
				return nil, errors.New("transient read failure")
			}
		}
		if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.2:43211", "", map[string]string{
			"code": codeB, "token": tokenB, "machine": "workstation-b", "hub_id": "hub-b", "hub_name": "Hub B",
		}); rec.Code != http.StatusOK {
			t.Fatalf("pair hub B: %d %s", rec.Code, rec.Body.String())
		}

		if !hasLog(logs, logMu, "passive: reload after pair: transient read failure") {
			t.Fatalf("expected reload error log, got %v", *logs)
		}
		if got := websocket.StatusCode(closedCode.Load()); got != closeCredential {
			t.Fatalf("Hub A stream closed with %v, want %v", got, closeCredential)
		}
		if got := s.config(); got.TokenSHA256 != hashHex(tokenB) || got.HubID != "hub-b" {
			t.Fatalf("s.cfg = %+v, want Hub B", got)
		}
		s.mu.Lock()
		gotFrom, gotAttn := s.from, s.attention
		s.mu.Unlock()
		if gotFrom != "127.0.0.2" || gotAttn != nil {
			t.Fatalf("after fallback: from=%q attn=%+v, want 127.0.0.2 / nil", gotFrom, gotAttn)
		}
		if recB := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.2:43211", tokenB, nil); recB.Code != http.StatusOK {
			t.Fatalf("newly paired Hub B /info got %d %s, want 200", recB.Code, recB.Body.String())
		}
		if recA := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, nil); recA.Code != http.StatusUnauthorized {
			t.Fatalf("revoked Hub A /info got %d %s, want 401", recA.Code, recA.Body.String())
		}
	})

	t.Run("activates_first_pairing_from_unpaired", func(t *testing.T) {
		path, s, logs, logMu := newServer(t)
		codeA := issueCode(t, path)
		s.testAfterPairUpdate = func() {
			s.testReadConfig = func(string) (*Config, error) {
				return nil, errors.New("transient read failure")
			}
		}
		if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.1:43210", "", map[string]string{
			"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
		}); rec.Code != http.StatusOK {
			t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
		}
		if !hasLog(logs, logMu, "passive: reload after pair: transient read failure") {
			t.Fatalf("expected reload error log, got %v", *logs)
		}
		if got := s.config(); !got.Paired() || got.TokenSHA256 != hashHex(tokenA) {
			t.Fatalf("s.cfg = %+v, want Hub A", got)
		}
		if recA := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, nil); recA.Code != http.StatusOK {
			t.Fatalf("Hub A /info got %d %s, want 200", recA.Code, recA.Body.String())
		}
	})

	t.Run("does_not_roll_back_concurrent_hub_c", func(t *testing.T) {
		path, s, logs, logMu := newServer(t)
		codeA := issueCode(t, path)
		if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.1:43210", "", map[string]string{
			"code": codeA, "token": tokenA, "machine": "workstation-a", "hub_id": "hub-a", "hub_name": "Hub A",
		}); rec.Code != http.StatusOK {
			t.Fatalf("pair hub A: %d %s", rec.Code, rec.Body.String())
		}

		codeB := issueCode(t, path)
		var hubCStreamClosed atomic.Bool
		var hookRan atomic.Bool
		s.testAfterPairUpdate = func() {
			if !hookRan.CompareAndSwap(false, true) {
				return
			}
			// Hub C pairs cleanly while Hub B's pair is between Update and syncFromDisk.
			codeC := issueCode(t, path)
			if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.3:43212", "", map[string]string{
				"code": codeC, "token": tokenC, "machine": "workstation-c", "hub_id": "hub-c", "hub_name": "Hub C",
			}); rec.Code != http.StatusOK {
				t.Errorf("pair hub C: %d %s", rec.Code, rec.Body.String())
				return
			}
			if rec := serve(t, s, http.MethodPost, ConnectorAttentionPath, "127.0.0.3:43212", tokenC, map[string]any{
				"rev": "rev-hub-c", "needs_you": 1,
			}); rec.Code != http.StatusNoContent {
				t.Errorf("hub C attention: %d %s", rec.Code, rec.Body.String())
				return
			}
			s.mu.Lock()
			s.streams[&pstream{
				token: hashHex(tokenC),
				since: time.Now(),
				from:  "127.0.0.3",
				end:   func(websocket.StatusCode, string) { hubCStreamClosed.Store(true) },
			}] = struct{}{}
			s.mu.Unlock()
			// Now Hub B's syncFromDisk hits a transient disk read failure.
			s.testReadConfig = func(string) (*Config, error) {
				return nil, errors.New("transient read failure")
			}
		}

		if rec := serve(t, s, http.MethodPost, ConnectorPairPath, "127.0.0.2:43211", "", map[string]string{
			"code": codeB, "token": tokenB, "machine": "workstation-b", "hub_id": "hub-b", "hub_name": "Hub B",
		}); rec.Code != http.StatusOK {
			t.Fatalf("pair hub B: %d %s", rec.Code, rec.Body.String())
		}
		if !hookRan.Load() {
			t.Fatal("testAfterPairUpdate hook did not run")
		}
		if !hasLog(logs, logMu, "passive: reload after pair: transient read failure") {
			t.Fatalf("expected reload error log, got %v", *logs)
		}
		if got := s.config(); got.TokenSHA256 != hashHex(tokenC) || got.HubID != "hub-c" {
			t.Fatalf("Hub B transient-read fallback rolled back Hub C: %+v", got)
		}
		if hubCStreamClosed.Load() {
			t.Fatal("Hub B transient-read fallback closed Hub C's active stream")
		}
		s.mu.Lock()
		gotFrom, gotAttn := s.from, s.attention
		s.mu.Unlock()
		if gotFrom != "127.0.0.3" || gotAttn == nil || gotAttn.Rev != "rev-hub-c" {
			t.Fatalf("Hub C state clobbered: from=%q attn=%+v", gotFrom, gotAttn)
		}
		if recC := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.3:43212", tokenC, nil); recC.Code != http.StatusOK {
			t.Fatalf("Hub C /info got %d %s, want 200", recC.Code, recC.Body.String())
		}
		if recB := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.2:43211", tokenB, nil); recB.Code != http.StatusUnauthorized {
			t.Fatalf("stale Hub B /info got %d %s, want 401", recB.Code, recB.Body.String())
		}
		if recA := serve(t, s, http.MethodGet, ConnectorInfoPath, "127.0.0.1:43210", tokenA, nil); recA.Code != http.StatusUnauthorized {
			t.Fatalf("revoked Hub A /info got %d %s, want 401", recA.Code, recA.Body.String())
		}
	})
}

// When serve() calls syncFromDisk with a non-empty expectedListen, switching out of passive mode,
// deleting link.json, or changing the listen address must leave s.cfg and active streams untouched
// so serve() exits and ends streams with closeStopping (1001) instead of closeCredential (4002).
func TestPassivePairSyncFromDiskExpectedListenLeavesStreamsForStopping(t *testing.T) {
	const listenAddr = "127.0.0.1:18663"
	seed := func(t *testing.T) (string, *passive, *atomic.Bool) {
		t.Helper()
		path := filepath.Join(t.TempDir(), "link.json")
		if _, err := SetMode(path, ModePassive, listenAddr); err != nil {
			t.Fatal(err)
		}
		var streamEnded atomic.Bool
		cfg := Passive{Listen: listenAddr, HubID: "hub-a", Machine: "workstation-a", TokenSHA256: hashHex(tokenA)}
		s := &passive{
			o:         Options{Path: path, Version: "test-version", Logf: func(string, ...any) {}},
			rep:       &reporter{},
			lim:       newAuthLimiter(),
			cfg:       cfg,
			attention: &Attention{Rev: "rev-a", NeedsYou: 1},
			streams: map[*pstream]struct{}{
				{
					token: hashHex(tokenA),
					since: time.Now(),
					from:  "127.0.0.1",
					end:   func(websocket.StatusCode, string) { streamEnded.Store(true) },
				}: {},
			},
			stopped: make(chan struct{}),
		}
		return path, s, &streamEnded
	}
	assertUntouched := func(t *testing.T, s *passive, streamEnded *atomic.Bool) {
		t.Helper()
		if streamEnded.Load() {
			t.Fatal("syncFromDisk with expectedListen closed active stream prematurely")
		}
		if got := s.config(); got.TokenSHA256 != hashHex(tokenA) || got.HubID != "hub-a" {
			t.Fatalf("syncFromDisk with expectedListen mutated s.cfg: %+v", got)
		}
		s.mu.Lock()
		gotAttn := s.attention
		s.mu.Unlock()
		if gotAttn == nil || gotAttn.Rev != "rev-a" {
			t.Fatalf("syncFromDisk with expectedListen cleared s.attention: %+v", gotAttn)
		}
	}

	t.Run("mode_switched_to_active", func(t *testing.T) {
		path, s, streamEnded := seed(t)
		if _, err := SetMode(path, ModeActive, ""); err != nil {
			t.Fatal(err)
		}
		modeOK, addr, err := s.syncFromDisk(nil, listenAddr)
		if err != nil || modeOK || addr != "" {
			t.Fatalf("syncFromDisk = (%v, %q, %v), want (false, \"\", nil)", modeOK, addr, err)
		}
		assertUntouched(t, s, streamEnded)
	})

	t.Run("config_deleted", func(t *testing.T) {
		path, s, streamEnded := seed(t)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		modeOK, addr, err := s.syncFromDisk(nil, listenAddr)
		if !errors.Is(err, os.ErrNotExist) || modeOK || addr != "" {
			t.Fatalf("syncFromDisk = (%v, %q, %v), want (false, \"\", ErrNotExist)", modeOK, addr, err)
		}
		assertUntouched(t, s, streamEnded)
	})

	t.Run("listen_addr_moved", func(t *testing.T) {
		path, s, streamEnded := seed(t)
		if _, err := SetMode(path, ModePassive, "127.0.0.1:18664"); err != nil {
			t.Fatal(err)
		}
		modeOK, addr, err := s.syncFromDisk(nil, listenAddr)
		if err != nil || !modeOK || addr != "127.0.0.1:18664" {
			t.Fatalf("syncFromDisk = (%v, %q, %v), want (true, 127.0.0.1:18664, nil)", modeOK, addr, err)
		}
		assertUntouched(t, s, streamEnded)
	})
}
