package cachewarm

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

const warmBody = `{ "model":"example-model", "system": [{"type":"text", "text":"private prompt", "cache_control":{"type":"ephemeral","ttl":"1h"}}], "tools" : [{ "name":"example", "input_schema": { "type":"object" }}], "messages" : [{"role":"user", "content":"private history"}], "thinking":{"type":"disabled"}, "max_tokens" : 1024, "stream" : true }`

type warmUpstream struct {
	mu      sync.Mutex
	bodies  [][]byte
	headers []http.Header
	path    []string
	status  int
	read    int
}

func (u *warmUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.bodies = append(u.bodies, body)
	u.headers = append(u.headers, r.Header.Clone())
	u.path = append(u.path, r.URL.RequestURI())
	id := len(u.bodies)
	status := u.status
	read := u.read
	u.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "private error api-key-example")
		return
	}
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_%d\",\"usage\":{\"cache_read_input_tokens\":%d}}}\n\n", id, read)
		w.(http.Flusher).Flush()
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		return
	}
	fmt.Fprintf(w, `{"id":"msg_%d","usage":{"cache_read_input_tokens":%d,"cache_creation_input_tokens":0,"output_tokens":1}}`, id, read)
}
func (u *warmUpstream) count() int { u.mu.Lock(); defer u.mu.Unlock(); return len(u.bodies) }
func proxyFixture(t *testing.T) (*Proxy, *warmUpstream, *bytes.Buffer) {
	t.Helper()
	up := &warmUpstream{read: 12345}
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	p, err := New("session-example", srv.URL+"/provider", func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p, up, logs
}
func normal(t *testing.T, p *Proxy, body string, confirm bool) []byte {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, p.BaseURL()+"/v1/messages?beta=true", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Api-Key", "api-key-example")
	r.Header.Set("Anthropic-Beta", "example-beta")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if confirm {
		var e struct {
			Message struct {
				ID string `json:"id"`
			} `json:"message"`
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "data: ") {
				_ = json.Unmarshal([]byte(line[6:]), &e)
				if e.Message.ID != "" {
					p.Confirm(e.Message.ID)
					break
				}
			}
		}
	}
	return out
}
func policy() *assistantwire.CacheKeepalive {
	return &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 24}
}
func lastAt(p *Proxy) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.last == nil {
		return time.Time{}
	}
	return p.last.at
}

func TestForwardAndReplayPreservePrefixBytesAndHeaders(t *testing.T) {
	p, u, logs := proxyFixture(t)
	out := normal(t, p, warmBody, true)
	if !bytes.Contains(out, []byte("event: message_start\n")) || !bytes.HasSuffix(out, []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")) {
		t.Fatal("stream changed")
	}
	if !bytes.Equal(u.bodies[0], []byte(warmBody)) || u.path[0] != "/provider/v1/messages?beta=true" {
		t.Fatalf("normal request changed: %s", u.path[0])
	}
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	if u.count() != 2 {
		t.Fatalf("requests %d", u.count())
	}
	expected := strings.Replace(strings.Replace(warmBody, `"max_tokens" : 1024`, `"max_tokens" : 1`, 1), `"stream" : true`, `"stream" : false`, 1)
	if string(u.bodies[1]) != expected {
		t.Fatalf("refresh changed cached bytes: %s", u.bodies[1])
	}
	for _, h := range u.headers {
		if h.Get("X-Api-Key") != "api-key-example" || h.Get("Anthropic-Beta") != "example-beta" {
			t.Fatal("auth/beta headers changed")
		}
	}
	if u.headers[0].Get(RequestKindHeader) != "" || u.headers[1].Get(RequestKindHeader) != "cache-keepalive" {
		t.Fatal("missing attribution")
	}
	for _, secret := range []string{"private prompt", "private history", "api-key-example", "example-beta"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("sensitive log")
		}
	}
	if !strings.Contains(logs.String(), "cache_read=12345 cache_creation=0 output=1") {
		t.Fatal(logs.String())
	}
}
func TestOnlyConfirmedRootRequestCanWarm(t *testing.T) {
	for _, kind := range []string{"subagent", "worker", "unconfirmed"} {
		t.Run(kind, func(t *testing.T) {
			p, u, _ := proxyFixture(t)
			normal(t, p, warmBody, false)
			p.Tick(context.Background(), time.Now().Add(55*time.Minute), policy(), true)
			if u.count() != 1 {
				t.Fatal("unconfirmed request warmed")
			}
		})
	}
	p, u, _ := proxyFixture(t)
	normal(t, p, warmBody, true)
	normal(t, p, warmBody, false)
	p.Tick(context.Background(), time.Now().Add(55*time.Minute), policy(), true)
	if u.count() != 2 {
		t.Fatal("child request inherited the root confirmation")
	}
}
func TestCapResetAndSwitchOff(t *testing.T) {
	p, u, _ := proxyFixture(t)
	normal(t, p, warmBody, true)
	for i := 0; i < 25; i++ {
		p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	}
	if u.count() != 25 {
		t.Fatalf("want real + 24 refreshes, got %d", u.count())
	}
	p.Input()
	normal(t, p, warmBody, true)
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	if u.count() != 27 {
		t.Fatal("real request did not reset cap")
	}
	off := policy()
	off.Enabled = false
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), off, true)
	if u.count() != 27 || p.last != nil || len(p.pending) != 0 {
		t.Fatal("disabled proxy retained or sent a request")
	}
	normal(t, p, warmBody, false)
	if u.count() != 28 {
		t.Fatal("switch off broke ordinary forwarding")
	}
}
func TestForgetIdleAndExpiredSnapshots(t *testing.T) {
	for _, why := range []string{"archive", "close", "compact", "model", "launcher", "disabled", "unknown-role"} {
		t.Run(why, func(t *testing.T) {
			p, u, _ := proxyFixture(t)
			normal(t, p, warmBody, true)
			p.Forget()
			p.Tick(context.Background(), time.Now().Add(55*time.Minute), policy(), true)
			if u.count() != 1 || p.last != nil || len(p.pending) != 0 {
				t.Fatal("invalidation failed")
			}
		})
	}
	p, u, _ := proxyFixture(t)
	normal(t, p, warmBody, true)
	at := lastAt(p)
	p.Tick(context.Background(), at.Add(54*time.Minute), policy(), true)
	p.Tick(context.Background(), at.Add(55*time.Minute), policy(), false)
	if u.count() != 1 {
		t.Fatal("early or busy refresh")
	}
	p.Tick(context.Background(), at.Add(time.Hour), policy(), true)
	if u.count() != 1 || p.last != nil {
		t.Fatal("expired cache rewritten")
	}
}
func TestReleasedProcessCanStillRefresh(t *testing.T) {
	p, u, _ := proxyFixture(t)
	normal(t, p, warmBody, true)
	p.CloseListener()
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	if u.count() != 2 {
		t.Fatal("idle release lost cache")
	}
}
func TestErrorsAreVisibleAndMissStopsWarming(t *testing.T) {
	p, u, logs := proxyFixture(t)
	normal(t, p, warmBody, true)
	u.mu.Lock()
	u.read = 0
	u.mu.Unlock()
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	p.Tick(context.Background(), time.Now().Add(110*time.Minute), policy(), true)
	if u.count() != 2 || p.last != nil {
		t.Fatal("kept spending after cache miss")
	}
	p.Input()
	u.mu.Lock()
	u.status = 429
	u.mu.Unlock()
	out := normal(t, p, warmBody, false)
	if string(out) != "private error api-key-example" {
		t.Fatal("upstream error swallowed")
	}
	if strings.Contains(logs.String(), "private error") || strings.Contains(logs.String(), "api-key-example") {
		t.Fatal("error leaked to logs")
	}
}
func TestStreamingFlushesBeforeUpstreamFinishes(t *testing.T) {
	finish := make(chan struct{})
	first := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		close(first)
		<-finish
		_, _ = io.WriteString(w, "data: last\n\n")
	}))
	defer srv.Close()
	p, err := New("stream-example", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, _ := http.NewRequestWithContext(ctx, "POST", p.BaseURL()+"/v1/messages", strings.NewReader(warmBody))
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		close(finish)
		t.Fatal(err)
	}
	<-first
	buf := make([]byte, len("data: first\n\n"))
	_, err = io.ReadFull(resp.Body, buf)
	close(finish)
	if err != nil || string(buf) != "data: first\n\n" {
		t.Fatal("stream buffered")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestUncachedNormalRequestDoesNotSpendOnRefresh(t *testing.T) {
	p, u, _ := proxyFixture(t)
	u.mu.Lock()
	u.read = 0
	u.mu.Unlock()
	normal(t, p, warmBody, true)
	p.Tick(context.Background(), time.Now().Add(55*time.Minute), policy(), true)
	if u.count() != 1 {
		t.Fatal("warmed a prefix the provider never cached")
	}
}

// Claude Code asks for gzip, br and zstd. A provider that compresses its event stream would hand the response tap
// bytes it cannot read, and no request would ever be retained. The proxy asks the provider for identity instead.
func TestCompressionIsNotRequestedFromTheProvider(t *testing.T) {
	up := &warmUpstream{read: 12345}
	var asked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.Header.Get("Accept-Encoding"))
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			up.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		z := gzip.NewWriter(w)
		rec := httptest.NewRecorder()
		up.ServeHTTP(rec, r)
		w.Header().Set("Content-Type", rec.Header().Get("Content-Type"))
		_, _ = z.Write(rec.Body.Bytes())
		_ = z.Close()
	}))
	t.Cleanup(srv.Close)
	p, err := New("session-example", srv.URL+"/provider", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	r, err := http.NewRequest(http.MethodPost, p.BaseURL()+"/v1/messages", strings.NewReader(warmBody))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	// A raw transport, as the CLI's: it neither adds its own encoding nor decodes for us.
	resp, err := (&http.Transport{DisableCompression: true}).RoundTrip(r)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if got, _ := asked.Load().(string); got != "" {
		t.Fatalf("provider was asked for %q", got)
	}
	if !bytes.Contains(out, []byte(`"id":"msg_1"`)) {
		t.Fatalf("stream not readable: %q", out)
	}
	p.Confirm("msg_1")
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	if up.count() != 2 {
		t.Fatalf("requests %d: the compressed stream never armed a refresh", up.count())
	}
}

// A subsequent Tick while the final allowed keepalive refresh (p.attempts == policy.MaxRequests) is
// still in flight must not call forgetLocked() and cancel that valid in-flight refresh; disabling
// the policy while a refresh is in flight must still cancel it immediately.
func TestTickDoesNotCancelInFlightFinalRefresh(t *testing.T) {
	t.Run("enabled_tick_preserves_inflight_final_refresh", func(t *testing.T) {
		started := make(chan struct{})
		var startedOnce sync.Once
		release := make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		var cancelled atomic.Bool
		var refreshes atomic.Int32
		up := &warmUpstream{read: 12345}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(RequestKindHeader) == "cache-keepalive" {
				body, _ := io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewReader(body))
				refreshes.Add(1)
				startedOnce.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
					cancelled.Store(true)
					return
				case <-release:
				}
			}
			up.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(unblock)

		p, err := New("session-final-tick", srv.URL+"/provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)

		normal(t, p, warmBody, true)
		pol := &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 1}
		tickDone := make(chan struct{})
		tickAt := lastAt(p).Add(55 * time.Minute)
		go func() {
			defer close(tickDone)
			p.Tick(context.Background(), tickAt, pol, true)
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("final refresh did not start")
		}

		// A second Tick arrives while the final refresh is in flight (p.attempts == 1 == MaxRequests).
		p.Tick(context.Background(), tickAt.Add(time.Minute), pol, true)
		unblock()

		select {
		case <-tickDone:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight Tick did not finish")
		}
		if cancelled.Load() {
			t.Fatal("concurrent Tick cancelled the in-flight final refresh")
		}
		if got := refreshes.Load(); got != 1 {
			t.Fatalf("refreshes = %d, want 1", got)
		}
		if got := up.count(); got != 2 {
			t.Fatalf("completed upstream requests = %d, want 2 (1 normal + 1 final refresh)", got)
		}
		if lastAt(p).IsZero() {
			t.Fatal("concurrent Tick wiped p.last before the final refresh completed")
		}
		// Once the final refresh has completed (p.attempts == MaxRequests), a subsequent Tick forgets p.last.
		p.Tick(context.Background(), tickAt.Add(2*time.Minute), pol, true)
		if !lastAt(p).IsZero() {
			t.Fatal("subsequent Tick after final refresh completed did not forget p.last")
		}
	})

	t.Run("disabled_policy_still_cancels_inflight_refresh", func(t *testing.T) {
		started := make(chan struct{})
		var startedOnce sync.Once
		cancelled := make(chan struct{})
		var cancelledOnce sync.Once
		cleanup := make(chan struct{})
		up := &warmUpstream{read: 12345}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(RequestKindHeader) == "cache-keepalive" {
				_, _ = io.Copy(io.Discard, r.Body)
				startedOnce.Do(func() { close(started) })
				select {
				case <-r.Context().Done():
					cancelledOnce.Do(func() { close(cancelled) })
				case <-cleanup:
				}
				return
			}
			up.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(cleanup) })

		p, err := New("session-disable-cancel", srv.URL+"/provider", nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)

		normal(t, p, warmBody, true)
		pol := &assistantwire.CacheKeepalive{Enabled: true, IntervalMinutes: 55, MaxRequests: 1}
		tickDone := make(chan struct{})
		tickAt := lastAt(p).Add(55 * time.Minute)
		go func() {
			defer close(tickDone)
			p.Tick(context.Background(), tickAt, pol, true)
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("refresh did not start")
		}

		off := &assistantwire.CacheKeepalive{Enabled: false, IntervalMinutes: 55, MaxRequests: 1}
		p.Tick(context.Background(), tickAt.Add(time.Second), off, true)

		select {
		case <-cancelled:
		case <-time.After(5 * time.Second):
			t.Fatal("disabled policy did not cancel in-flight refresh")
		}
		select {
		case <-tickDone:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled Tick did not finish")
		}
	})
}

// Calling Close more than once on the response body returned by roundTripper must not underflow
// p.active, which would permanently break Tick() and Discarded().
func TestResponseBodyDoubleCloseDoesNotUnderflowActive(t *testing.T) {
	p, up, _ := proxyFixture(t)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	t.Cleanup(transport.CloseIdleConnections)
	rt := roundTripper{p: p, transport: transport}

	req, err := http.NewRequest(http.MethodPost, p.upstream.String()+"/v1/messages", strings.NewReader(warmBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "api-key-example")
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- resp.Body.Close()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Close: %v", err)
		}
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("repeat Close: %v", err)
	}
	if !bytes.Contains(out, []byte(`"id":"msg_1"`)) {
		t.Fatalf("unexpected response: %s", out)
	}

	p.mu.Lock()
	active := p.active
	p.mu.Unlock()
	if active != 0 {
		t.Fatalf("p.active = %d after double Close, want 0", active)
	}

	p.Confirm("msg_1")
	p.Tick(context.Background(), lastAt(p).Add(55*time.Minute), policy(), true)
	if got := up.count(); got != 2 {
		t.Fatalf("upstream count = %d after double Close + Tick, want 2", got)
	}

	p.CloseListener()
	p.Forget()
	if !p.Discarded() {
		t.Fatal("Discarded() = false after double Close + CloseListener + Forget")
	}
}
