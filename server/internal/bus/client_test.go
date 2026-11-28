package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

// signal wakes the waiters of a test's counts: more is closed at the next change.
type signal struct {
	mu   sync.Mutex
	more chan struct{}
}

func (s *signal) changed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.more != nil {
		close(s.more)
		s.more = nil
	}
}

// until returns once done says so, looking again at each change; it fails the test after within.
func (s *signal) until(t *testing.T, what string, within time.Duration, done func() bool) {
	t.Helper()
	deadline := time.NewTimer(within)
	defer deadline.Stop()
	for {
		s.mu.Lock()
		if s.more == nil {
			s.more = make(chan struct{})
		}
		more := s.more
		s.mu.Unlock()
		if done() {
			return
		}
		select {
		case <-more:
		case <-deadline.C:
			t.Fatalf("%s: not within %s", what, within)
		}
	}
}

// events counts something a test waits for: dials, tries, probes.
type events struct {
	signal
	n atomic.Int32
}

func (e *events) add() {
	e.n.Add(1)
	e.changed()
}

func (e *events) count() int { return int(e.n.Load()) }

// waitFor returns once there were n of them.
func (e *events) waitFor(t *testing.T, what string, n int, within time.Duration) {
	t.Helper()
	e.until(t, fmt.Sprintf("%d %s (%d so far)", n, what, e.count()), within, func() bool { return e.count() >= n })
}

// logLines keeps a client's log for a test to look at.
type logLines struct {
	signal
	lmu   sync.Mutex
	lines []string
}

func (l *logLines) logf(format string, args ...any) {
	l.lmu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.lmu.Unlock()
	l.changed()
}

func (l *logLines) count(part string) int {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

func (l *logLines) all() []string {
	l.lmu.Lock()
	defer l.lmu.Unlock()
	return append([]string(nil), l.lines...)
}

// waitFor returns once n lines with part were logged.
func (l *logLines) waitFor(t *testing.T, part string, n int, within time.Duration) {
	t.Helper()
	l.until(t, fmt.Sprintf("%d of %q in the log", n, part), within, func() bool { return l.count(part) >= n })
}

// tryEnded is what Run logs after each try that failed.
const tryEnded = "the connection to the hub ended"

// freeAddr is a loopback address nothing listens on (yet).
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// listenAt listens at addr (a port freeAddr gave or a server let go).
func listenAt(t *testing.T, addr string) net.Listener {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen again at %s: %v", addr, err)
	}
	return l
}

// serveOn serves h on l.
func serveOn(l net.Listener, h http.Handler) *httptest.Server {
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close()
	srv.Listener = l
	srv.Start()
	return srv
}

// countingClient dials as net.Dialer does, one connection per request, and counts the dials: the full tries' and
// the probes'.
func countingClient(dials *events) *http.Client {
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.add()
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}}}
}

// runClient runs c until the test ends, then cancels it and waits for Run to return.
func runClient(t *testing.T, c *Client) (ctx context.Context, cancel context.CancelFunc, ran <-chan error) {
	t.Helper()
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	done, returned := make(chan error, 1), make(chan struct{})
	go func() {
		done <- c.Run(ctx)
		close(returned)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after its context ended")
		}
	})
	return ctx, cancel, done
}

// stop cancels Run and waits for it to return.
func stop(t *testing.T, cancel context.CancelFunc, ran <-chan error) {
	t.Helper()
	cancel()
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}

// probeUA is how a test tells the client's probes from another program's port scan landing on its server.
func probeUA(r *http.Request) bool { return strings.HasPrefix(r.UserAgent(), "Go-http-client/") }

// hangUp drops the request's connection without an answer: a reset during the handshake, or an SSH forward whose
// tunnel cannot reach the hub.
func hangUp(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if conn, _, err := hj.Hijack(); err == nil {
			_ = conn.Close()
		}
	}
}

// noBus is a hub whose health answers and whose bus does not: it hangs up on each try (counted), and counts the
// probes of its health.
type noBus struct{ tries, probes events }

func (n *noBus) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == healthPath:
		if probeUA(r) {
			n.probes.add()
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	case r.URL.Path == Path && r.Header.Get("Authorization") != "":
		n.tries.add()
		hangUp(w)
	default:
		http.NotFound(w, r)
	}
}

// forward stands in for an SSH local forward to the hub: its port takes connections all along. While its tunnel is
// stalled, a health request gets no answer (stuck counts them) and the bus's request is hung up on; then everything
// goes through to hub.
type forward struct {
	stalled atomic.Bool
	stuck   events
	hub     http.Handler
}

func (f *forward) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !f.stalled.Load() {
		f.hub.ServeHTTP(w, r)
		return
	}
	if r.URL.Path == healthPath {
		if probeUA(r) {
			f.stuck.add()
		}
		<-r.Context().Done() // until the probe gives up
		return
	}
	hangUp(w)
}

// A client whose backoff is at its cap (a laptop that slept through failed tries) is back within moments of the hub
// answering again, not after the rest of its minute: one full try, once the probe finds the port.
func TestClientReconnectsSoonAfterTheHubComesBack(t *testing.T) {
	addr := freeAddr(t)
	var dials events
	var log logLines
	c := &Client{Hub: "http://" + addr, Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		HTTPClient: countingClient(&dials), Backoff: time.Minute, MaxBackoff: time.Minute, probeEvery: 20 * time.Millisecond}
	ctx, _, _ := runClient(t, c)
	log.waitFor(t, "again in 1m0s", 1, 5*time.Second)
	dials.waitFor(t, "dials (the try, then probes that find nothing)", 3, 5*time.Second)

	f := &fakeHub{}
	srv := serveOn(listenAt(t, addr), f)
	defer srv.Close()
	back := time.Now()
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := c.Wait(wctx); err != nil {
		t.Fatalf("not connected %s after the hub came back (the backoff is a minute): %v", time.Since(back), err)
	}
	if n := f.dials.Load(); n != 1 {
		t.Errorf("%d full tries once the hub answered, want 1", n)
	}
	if f.healths.Load() < 1 {
		t.Error("connected without asking the hub's health")
	}
	if log.count("the hub answers again") != 1 {
		t.Errorf("the log does not say why it tried early: %q", log.all())
	}
}

// A hub that stays out of reach gets only probes between full tries, and the full tries keep the backoff's pace.
func TestClientProbesWhileTheHubStaysDown(t *testing.T) {
	var dials events
	var log logLines
	c := &Client{Hub: "http://" + freeAddr(t), Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		HTTPClient: countingClient(&dials), Backoff: 100 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
		probeOver: time.Nanosecond, probeEvery: 10 * time.Millisecond}
	began := time.Now()
	_, cancel, ran := runClient(t, c)
	log.waitFor(t, tryEnded, 4, 10*time.Second)
	took := time.Since(began)
	stop(t, cancel, ran)
	tries := log.count(tryEnded)
	probes := dials.count() - tries
	t.Logf("%d full tries in %s, %d probes", tries, took.Round(time.Millisecond), probes)
	// Four tries take the waits of 100, 200 and 200 ms between them.
	if took < 450*time.Millisecond {
		t.Fatalf("4 full tries within %s, faster than the backoff's 500 ms", took)
	}
	if probes < 3*(tries-1) {
		t.Fatalf("%d probes between %d full tries, want several in each wait", probes, tries)
	}
	if log.count("the hub answers again") != 0 {
		t.Fatal("a wait ended early while the hub was down")
	}
}

// A hub reached through a local SSH forward: the forward's port takes connections while the tunnel behind it is
// stalled (a laptop asleep), so a TCP dial would find "the hub" all along. The probe asks the hub's health through
// it instead: no answer while the tunnel is stalled, so the wait began with the hub out of reach, and it ends within
// a probe of the tunnel carrying again, not after the rest of the minute.
func TestClientReconnectsThroughAForwardWhoseTunnelComesBack(t *testing.T) {
	f := &fakeHub{}
	fw := &forward{hub: f}
	fw.stalled.Store(true)
	srv := httptest.NewServer(fw)
	defer srv.Close()
	var log logLines
	// No HTTPClient: the default one, as a manager whose hub is a plain-http forward on the loopback.
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		Backoff: time.Minute, MaxBackoff: time.Minute, probeEvery: 20 * time.Millisecond}
	ctx, _, _ := runClient(t, c)
	log.waitFor(t, "again in 1m0s", 1, 5*time.Second)
	fw.stuck.waitFor(t, "health probes the stalled tunnel left unanswered", 2, 5*time.Second)

	fw.stalled.Store(false)
	back := time.Now()
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := c.Wait(wctx); err != nil {
		t.Fatalf("not connected %s after the tunnel came back (the backoff is a minute): %v", time.Since(back), err)
	}
	if n := f.dials.Load(); n != 1 {
		t.Errorf("%d full tries once the hub answered, want 1", n)
	}
	if log.count("the hub answers again") != 1 {
		t.Errorf("the log does not say why it tried early: %q", log.all())
	}
}

// A hub whose health answers but whose bus does not (a reset during the handshake, a bus that refuses for now) keeps
// the doubling backoff: it answered as each wait began, so no wait ends early, and it sees one probe a wait, not one
// every probeEvery.
func TestClientKeepsBackoffWhenTheHubIsHealthyButNotTheBus(t *testing.T) {
	n := &noBus{}
	srv := httptest.NewServer(n)
	defer srv.Close()
	var log logLines
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		Backoff: 100 * time.Millisecond, MaxBackoff: 200 * time.Millisecond,
		probeOver: time.Nanosecond, probeEvery: 10 * time.Millisecond}
	began := time.Now()
	_, cancel, ran := runClient(t, c)
	n.tries.waitFor(t, "full tries", 4, 10*time.Second)
	took := time.Since(began)
	n.probes.waitFor(t, "probes (one in each wait so far)", 3, 5*time.Second)
	stop(t, cancel, ran)
	tries, probes := n.tries.count(), n.probes.count()
	t.Logf("%d full tries in %s, %d probes", tries, took.Round(time.Millisecond), probes)
	if took < 450*time.Millisecond {
		t.Fatalf("4 full tries within %s, faster than the backoff's 500 ms (a try every probe would take 30 ms)", took)
	}
	if probes > tries+1 {
		t.Fatalf("%d probes for %d waits, want one a wait", probes, tries)
	}
	if log.count("the hub answers again") != 0 {
		t.Fatal("a wait ended early though the hub answered as it began")
	}
}

// A try that a probe brought early and that fails past the hub's health doubles the backoff as any other: the hub
// came back but its bus did not, and the next wait is the longer one, run to its end.
func TestClientEarlyTryThatFailsKeepsDoubling(t *testing.T) {
	addr := freeAddr(t)
	var dials events
	var log logLines
	c := &Client{Hub: "http://" + addr, Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		HTTPClient: countingClient(&dials), Backoff: 30 * time.Second, MaxBackoff: time.Minute, probeEvery: 10 * time.Millisecond}
	runClient(t, c)
	log.waitFor(t, "again in 30s", 1, 5*time.Second)
	dials.waitFor(t, "dials (the try, then probes that find nothing)", 3, 5*time.Second)

	srv := serveOn(listenAt(t, addr), &noBus{}) // healthy, and hangs up on the bus's request
	defer srv.Close()
	log.waitFor(t, "the hub answers again", 1, 5*time.Second)
	log.waitFor(t, "again in 1m0s", 1, 5*time.Second)
	if n := log.count(tryEnded); n != 2 {
		t.Fatalf("%d tries, want the first and the early one: %q", n, log.all())
	}
}

// A wait no longer than probeOver (the first ones, 1 s and 2 s by default) is waited whole without probes.
func TestClientShortWaitsDoNotProbe(t *testing.T) {
	var dials events
	var log logLines
	c := &Client{Hub: "http://" + freeAddr(t), Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		HTTPClient: countingClient(&dials), Backoff: 50 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
		probeOver: 100 * time.Millisecond, probeEvery: time.Millisecond}
	_, cancel, ran := runClient(t, c)
	log.waitFor(t, tryEnded, 4, 10*time.Second)
	stop(t, cancel, ran)
	if n, tries := dials.count(), log.count(tryEnded); n > tries+1 {
		t.Fatalf("%d dials for %d tries with waits of 50 ms: probes in a short wait", n, tries)
	}
}

// A refused token waits MaxBackoff whole, even when the hub goes away and comes back during the wait: only someone
// fixing the token helps, and a probe would only bring the next refusal sooner. It does not probe at all.
func TestClientRejectedTokenKeepsItsWait(t *testing.T) {
	addr := freeAddr(t)
	f := &fakeHub{}
	f.refuse.Store(true)
	f.plain.Store(true)
	var gone sync.Once
	l := listenAt(t, addr)
	srv := serveOn(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gone.Do(func() { _ = l.Close() }) // out of reach before the client hears the refusal
		f.ServeHTTP(w, r)
	}))
	defer srv.Close()
	var dials events
	var log logLines
	c := &Client{Hub: "http://" + addr, Token: "t", HelloMethod: "manager.hello", Logf: log.logf,
		HTTPClient: countingClient(&dials), Backoff: 10 * time.Millisecond, MaxBackoff: 300 * time.Millisecond,
		probeOver: time.Nanosecond, probeEvery: 10 * time.Millisecond}
	runClient(t, c)
	log.waitFor(t, "again in 300ms", 1, 5*time.Second)
	refused := time.Now()

	var again events
	f2 := &fakeHub{}
	f2.refuse.Store(true)
	f2.plain.Store(true)
	srv2 := serveOn(listenAt(t, addr), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f2.ServeHTTP(w, r)
		again.add()
	}))
	defer srv2.Close()
	again.waitFor(t, "tries after the refusal", 1, 5*time.Second)
	if took := time.Since(refused); took < 250*time.Millisecond {
		t.Fatalf("tried again %s after the refusal, before its wait of %s", took, c.MaxBackoff)
	}
	if n := dials.count(); n != 2 {
		t.Fatalf("%d dials for two tries with a refused token, want no probes between them", n)
	}
}

// Run returns as soon as its context ends: between probes, and in the middle of one that hangs.
func TestClientPauseEndsWithItsContext(t *testing.T) {
	t.Run("between probes", func(t *testing.T) {
		var dials events
		c := &Client{Hub: "http://" + freeAddr(t), Token: "t", HelloMethod: "manager.hello",
			HTTPClient: countingClient(&dials), Backoff: time.Minute, MaxBackoff: time.Minute, probeEvery: 20 * time.Millisecond}
		_, cancel, ran := runClient(t, c)
		dials.waitFor(t, "dials (the try, then probes)", 3, 5*time.Second)
		cancel()
		select {
		case err := <-ran:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Run went on waiting after its context ended")
		}
	})
	t.Run("in a probe", func(t *testing.T) {
		var dials atomic.Int32
		probing, release := make(chan struct{}), make(chan struct{})
		t.Cleanup(func() { close(release) }) // a dial the transport goes on with after its request gave up
		var once sync.Once
		dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
			if dials.Add(1) == 1 {
				return nil, errors.New("down") // the try
			}
			once.Do(func() { close(probing) }) // a probe: nothing answers, for as long as it may wait (a minute)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return nil, errors.New("released")
			}
		}
		c := &Client{Hub: "http://hub.example", Token: "t", HelloMethod: "manager.hello",
			HTTPClient: &http.Client{Transport: &http.Transport{DialContext: dial}},
			Backoff:    time.Minute, MaxBackoff: time.Minute, probeEvery: time.Minute}
		_, cancel, ran := runClient(t, c)
		select {
		case <-probing:
		case <-time.After(5 * time.Second):
			t.Fatal("no probe")
		}
		cancel()
		select {
		case err := <-ran:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("Run returned %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("Run waited out the probe after its context ended")
		}
	})
}

func TestHealthURL(t *testing.T) {
	for _, c := range []struct{ hub, want string }{
		{"http://hub.example:18660", "http://hub.example:18660/api/v1/health"},
		{"https://hub.example/base/", "https://hub.example/base/api/v1/health"},
		{"ws://hub.example", "http://hub.example/api/v1/health"},
		{"wss://[::1]:8443", "https://[::1]:8443/api/v1/health"},
	} {
		if got, err := healthURL(c.hub); err != nil || got != c.want {
			t.Errorf("healthURL(%q) = %q, %v; want %q", c.hub, got, err, c.want)
		}
	}
	for _, hub := range []string{"hub.example", "ftp://hub.example", ""} {
		if got, err := healthURL(hub); err == nil {
			t.Errorf("healthURL(%q) = %q, want an error", hub, got)
		}
	}
	if (&Client{Hub: "hub.example"}).prober() != nil {
		t.Error("a probe for a hub URL that does not parse")
	}
}

// Only the hub's own answer counts: a 2xx whose body says "ok": true, in time.
func TestProbeReadsTheHubsHealth(t *testing.T) {
	var mode atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath {
			http.NotFound(w, r)
			return
		}
		switch mode.Load().(string) {
		case "ok":
			_, _ = w.Write([]byte(`{"ok":true,"hub_id":"h"}`))
		case "not ok":
			_, _ = w.Write([]byte(`{"ok":false}`))
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "not json":
			_, _ = w.Write([]byte(`<html>a proxy's page</html>`))
		case "hang":
			<-r.Context().Done()
		}
	}))
	defer srv.Close()
	reach := (&Client{Hub: srv.URL}).prober()
	for _, c := range []struct {
		mode string
		want bool
	}{{"ok", true}, {"not ok", false}, {"503", false}, {"not json", false}, {"hang", false}, {"ok", true}} {
		mode.Store(c.mode)
		if got := reach(context.Background(), 300*time.Millisecond); got != c.want {
			t.Errorf("health %s: reachable %v, want %v", c.mode, got, c.want)
		}
	}
	if (&Client{Hub: srv.URL + "/elsewhere"}).prober()(context.Background(), time.Second) {
		t.Error("a 404 for the health is a hub that answers")
	}
}

// recorder answers every request with {"ok": true} and keeps what was asked.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req.Method+" "+req.URL.String()+" auth="+req.Header.Get("Authorization"))
	r.mu.Unlock()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: req}, nil
}

// The probe goes the bus's own way: through the client's HTTP client, whatever its transport (a pinned TLS client,
// a proxy), and without the token, which the health does not need.
func TestProbeGoesTheClientsWay(t *testing.T) {
	rec := &recorder{}
	c := &Client{Hub: "https://hub.example/base/", Token: "secret", HTTPClient: &http.Client{Transport: rec}}
	if !c.prober()(context.Background(), time.Second) {
		t.Fatal("the client's own transport said ok")
	}
	rec.mu.Lock()
	if len(rec.seen) != 1 || rec.seen[0] != "GET https://hub.example/base/api/v1/health auth=" {
		t.Errorf("asked %q", rec.seen)
	}
	rec.mu.Unlock()

	// Through a proxy: what the proxy says of the hub is the answer (502 when it cannot reach it).
	var upstreamDown atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "hub.invalid:18660" || r.URL.Path != healthPath {
			http.Error(w, "unexpected "+r.URL.String(), http.StatusBadRequest)
			return
		}
		if upstreamDown.Load() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	via := &Client{Hub: "http://hub.invalid:18660", HTTPClient: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}}
	reach := via.prober()
	if !reach(context.Background(), time.Second) {
		t.Fatal("the hub answered through the proxy")
	}
	upstreamDown.Store(true)
	if reach(context.Background(), time.Second) {
		t.Fatal("a proxy that cannot reach the hub passed for it")
	}
}

// A peer the service closed itself (rpc.Peer.Close: it wants everything read again on a new connection) ends the
// connection: the client hangs up and dials again. It used to wait for the hub to close a socket whose peer
// answered nothing any more.
func TestClientDialsAgainOnceItsPeerIsClosed(t *testing.T) {
	f := &fakeHub{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var log logLines
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", Logf: log.logf, Backoff: 10 * time.Millisecond}
	ctx, _, _ := runClient(t, c)
	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	defer wcancel()
	if err := c.Wait(wctx); err != nil {
		t.Fatal(err)
	}
	first := c.Peer()
	if first == nil {
		t.Fatal("no peer once connected")
	}
	why := errors.New("read everything again")
	first.Close(why)
	log.waitFor(t, tryEnded, 1, 10*time.Second)
	if n := log.count(why.Error()); n != 1 {
		t.Errorf("the log does not say why the connection ended: %q", log.all())
	}
	if err := c.Wait(wctx); err != nil { // the peer was let go before the log said so: this waits for the next
		t.Fatalf("no new connection: %v", err)
	}
	if p := c.Peer(); p == nil || p == first {
		t.Fatalf("the peer after the new connection: %p, the closed one %p", p, first)
	}
	if n := f.dials.Load(); n != 2 {
		t.Errorf("%d dials, want the first and the one after the peer was closed", n)
	}
}

// A peer closed with no error (rpc.Peer.Close(nil), an orderly close) still ends the connection with a reason:
// session returns rpc.ErrClosed, and Run logs it instead of "<nil>".
func TestClientSaysWhyWhenItsPeerClosesWithoutAnError(t *testing.T) {
	f := &fakeHub{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	var log logLines
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", Logf: log.logf}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type result struct {
		helloed bool
		err     error
	}
	ended := make(chan result, 1)
	go func() {
		helloed, err := c.session(ctx)
		ended <- result{helloed, err}
	}()
	if err := c.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	c.Peer().Close(nil)
	select {
	case r := <-ended:
		if !r.helloed || !errors.Is(r.err, rpc.ErrClosed) {
			t.Fatalf("session after a nil close: helloed %v, err %v; want helloed and rpc.ErrClosed", r.helloed, r.err)
		}
	case <-ctx.Done():
		t.Fatal("session did not end once its peer was closed")
	}

	c.Backoff = 10 * time.Millisecond
	rctx, _, _ := runClient(t, c)
	if err := c.Wait(rctx); err != nil {
		t.Fatal(err)
	}
	c.Peer().Close(nil)
	log.waitFor(t, tryEnded, 1, 10*time.Second)
	if log.count("<nil>") != 0 || log.count(rpc.ErrClosed.Error()) != 1 {
		t.Fatalf("Run's log after a nil close: %q; want the reason %q", log.all(), rpc.ErrClosed)
	}
}

// Closing the WebSocket with CloseRevoked (4002) during the HelloMethod handshake must still map to
// ErrRevoked so StopWhenRevoked stops immediately instead of retrying forever.
func TestClientHelloCloseRevokedStopsWhenRevoked(t *testing.T) {
	for _, readFirst := range []bool{true, false} {
		name := "immediate_close"
		if readFirst {
			name = "after_reading_hello"
		}
		t.Run(name, func(t *testing.T) {
			var dials atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != Path {
					http.NotFound(w, r)
					return
				}
				dials.Add(1)
				ws, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				if readFirst {
					_, _, _ = ws.Read(r.Context())
				}
				_ = ws.Close(CloseRevoked, "token revoked during hello")
			}))
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			c := &Client{
				Hub:             srv.URL,
				Token:           "t",
				HelloMethod:     "manager.hello",
				StopWhenRevoked: true,
				Backoff:         10 * time.Millisecond,
				MaxBackoff:      50 * time.Millisecond,
			}
			err := c.Run(ctx)
			if !errors.Is(err, ErrRevoked) || !errors.Is(err, ErrRejected) {
				t.Fatalf("Run returned %v (dials=%d), want ErrRevoked", err, dials.Load())
			}
			if got := dials.Load(); got != 1 {
				t.Fatalf("dials = %d, want 1", got)
			}
		})
	}
}

// Closing the WebSocket with CloseRefused (4003) during HelloMethod must map to ErrRejected (not
// ErrRevoked), so StopWhenRejected stops it and normal Run waits MaxBackoff without health probes.
func TestClientHelloCloseRefusedMapsToRejectedAndMaxBackoff(t *testing.T) {
	var probes events
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case healthPath:
			if probeUA(r) {
				probes.add()
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		case Path:
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			_, _, _ = ws.Read(r.Context())
			_ = ws.Close(CloseRefused, "wrong identity")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello"}
	helloed, err := c.session(ctx)
	if helloed || !errors.Is(err, ErrRejected) || errors.Is(err, ErrRevoked) {
		t.Fatalf("session = (%v, %v), want (false, ErrRejected && !ErrRevoked)", helloed, err)
	}

	// With StopWhenRevoked + StopWhenRejected, CloseRefused during hello stops Run as ErrRevoked.
	cStop := &Client{
		Hub:              srv.URL,
		Token:            "t",
		HelloMethod:      "manager.hello",
		StopWhenRevoked:  true,
		StopWhenRejected: true,
		Backoff:          10 * time.Millisecond,
		MaxBackoff:       50 * time.Millisecond,
	}
	if err := cStop.Run(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Run with StopWhenRejected returned %v, want ErrRevoked", err)
	}

	// Without StopWhenRejected, CloseRefused during hello backs off directly to MaxBackoff with no health probes.
	var log logLines
	cWait := &Client{
		Hub:         srv.URL,
		Token:       "t",
		HelloMethod: "manager.hello",
		Logf:        log.logf,
		Backoff:     10 * time.Millisecond,
		MaxBackoff:  300 * time.Millisecond,
		probeOver:   time.Nanosecond,
		probeEvery:  10 * time.Millisecond,
	}
	_, stopWait, ran := runClient(t, cWait)
	log.waitFor(t, "again in 300ms", 1, 2*time.Second)
	stop(t, stopWait, ran)
	if got := probes.count(); got != 0 {
		t.Fatalf("CloseRefused during hello triggered %d health probes, want 0", got)
	}

	// An ordinary non-identity RPC error during hello must return promptly and not map to ErrRejected.
	rpcErrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path {
			http.NotFound(w, r)
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(r.Context(), ws)
		p := rpc.New("h", conn)
		p.OnRequest = func(_ context.Context, _ string, _ json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: CodeStoreUnavailable, Message: "store busy"}
		}
		_ = p.Serve(r.Context(), conn)
	}))
	defer rpcErrSrv.Close()
	cRPC := &Client{Hub: rpcErrSrv.URL, Token: "t", HelloMethod: "manager.hello"}
	helloed, err = cRPC.session(ctx)
	var coded *rpc.Error
	if helloed || !errors.As(err, &coded) || coded.Code != CodeStoreUnavailable || errors.Is(err, ErrRejected) {
		t.Fatalf("ordinary hello RPC error = (%v, %v), want CodeStoreUnavailable and !ErrRejected", helloed, err)
	}
}

// When ws.Write fails with a raw transport error right before p.Serve finishes decoding the hub's
// Close frame, helloError waits briefly on served so CloseRevoked / CloseRefused is not lost,
// while an expired hello context returns immediately without waiting.
func TestHelloErrorSurfacesDelayedCloseFromServed(t *testing.T) {
	p := rpc.New("s", io.Discard)
	served := make(chan error, 1)
	sctx := context.Background()
	hctx, hcancel := context.WithTimeout(sctx, time.Second)
	defer hcancel()

	go func() {
		served <- websocket.CloseError{Code: CloseRevoked, Reason: "revoked during write"}
	}()
	got := helloError(errors.New("write: broken pipe"), p, served, sctx, hctx)
	if !errors.Is(got, ErrRevoked) || !errors.Is(got, ErrRejected) {
		t.Fatalf("helloError = %v, want ErrRevoked", got)
	}

	// An already-expired hello context returns its own error immediately without reading served.
	expiredCtx, cancelExpired := context.WithCancel(sctx)
	cancelExpired()
	blockedServed := make(chan error)
	gotExpired := helloError(context.Canceled, p, blockedServed, sctx, expiredCtx)
	if !errors.Is(gotExpired, context.Canceled) {
		t.Fatalf("helloError with canceled hctx = %v, want context.Canceled", gotExpired)
	}
}
