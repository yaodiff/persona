package cachewarm

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/osproc"
)

const bodyMost = 32 << 20
const RequestKindHeader = "X-Persona-Request-Kind"

type captured struct {
	body       []byte
	header     http.Header
	target     string
	at         time.Time
	generation uint64
}

// Proxy belongs to one native Claude conversation, not to its process: an idle
// process may be released after twenty minutes while its cache is still useful.
// No body, header, response text or upstream error is logged or written to disk.
type Proxy struct {
	mu         sync.Mutex
	session    string
	server     *http.Server
	listener   net.Listener
	upstream   *url.URL
	prefix     string
	client     *http.Client
	logf       func(string, ...any)
	last       *captured
	pending    map[string]*captured
	confirmed  map[string]bool
	generation uint64
	attempts   int
	next       time.Time
	active     int
	blocked    bool
	cancel     context.CancelFunc
	checking   bool
	released   bool
}

func New(session, upstream string, logf func(string, ...any)) (*Proxy, error) {
	u, err := url.Parse(upstream)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, &url.Error{Op: "cache proxy", URL: "", Err: errInvalidUpstream{}}
	}
	ln, err := osproc.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	key := make([]byte, 24)
	if _, err = rand.Read(key); err != nil {
		_ = ln.Close()
		return nil, err
	}
	p := &Proxy{session: session, upstream: u, listener: ln, prefix: "/" + hex.EncodeToString(key), logf: logf, pending: map[string]*captured{}, confirmed: map[string]bool{}}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	p.client = &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rp := &httputil.ReverseProxy{FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(r *httputil.ProxyRequest) {
			r.Out.URL.Path = strings.TrimPrefix(r.Out.URL.Path, p.prefix)
			r.Out.URL.RawPath = strings.TrimPrefix(r.Out.URL.RawPath, p.prefix)
			r.SetURL(u)
			r.Out.Host = u.Host
			// The response tap reads message_start from the stream. Claude Code asks for gzip, br and zstd, which
			// the transport would hand back undecoded; a compressed stream never armed a refresh. Loopback to the
			// CLI needs no compression, so ask the provider for identity (the transport adds no encoding of its own).
			if r.Out.Method == http.MethodPost && strings.HasSuffix(r.Out.URL.Path, "/messages") {
				r.Out.Header.Del("Accept-Encoding")
			}
		},
		Transport: roundTripper{p, transport},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "upstream request failed", http.StatusBadGateway)
		},
	}
	p.server = &http.Server{ReadHeaderTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0), Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, p.prefix+"/") {
			http.NotFound(w, r)
			return
		}
		rp.ServeHTTP(w, r)
	})}
	go func() { _ = p.server.Serve(ln) }()
	return p, nil
}

type errInvalidUpstream struct{}

func (errInvalidUpstream) Error() string { return "unsupported upstream" }
func (p *Proxy) BaseURL() string         { return "http://" + p.listener.Addr().String() + p.prefix }

// Forget cancels refreshes and discards all retained secrets; forwarding remains
// available until the CLI exits, so toggling off or archiving cannot break it.
func (p *Proxy) Forget() { p.mu.Lock(); defer p.mu.Unlock(); p.forgetLocked(); p.blocked = true }
func (p *Proxy) forgetLocked() {
	p.generation++
	p.last = nil
	p.pending = map[string]*captured{}
	p.confirmed = map[string]bool{}
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
}
func (p *Proxy) Close() { p.Forget(); _ = p.server.Close(); p.client.CloseIdleConnections() }
func (p *Proxy) CloseListener() {
	p.mu.Lock()
	p.released = true
	p.pending = map[string]*captured{}
	p.confirmed = map[string]bool{}
	p.mu.Unlock()
	_ = p.server.Close()
}

// Discarded lets the node retire a released process's empty proxy and transport.
func (p *Proxy) Discarded() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.released && p.last == nil && p.active == 0 && !p.checking
}

// Input invalidates a pre-input snapshot before Claude can compact or prepare the
// next provider request. Only a subsequent confirmed root request rearms it.
func (p *Proxy) Input() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.forgetLocked()
	p.attempts = 0
	p.blocked = false
}

// Confirm accepts only a root message_start observed on the CLI's stream-json.
// Provider traffic alone cannot distinguish an inherited subagent's endpoint.
func (p *Proxy) Confirm(id string) {
	if id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blocked {
		return
	}
	if c := p.pending[id]; c != nil {
		p.commitLocked(c)
		return
	}
	if len(p.confirmed) >= 8 {
		p.confirmed = map[string]bool{}
	}
	p.confirmed[id] = true
}
func (p *Proxy) commitLocked(c *captured) {
	if c.generation != p.generation || p.blocked {
		return
	}
	p.last = c
	p.attempts = 0
	p.next = c.at
	p.pending = map[string]*captured{}
	p.confirmed = map[string]bool{}
}
func (p *Proxy) candidate(id string, c *captured) {
	if id == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.blocked || c.generation != p.generation {
		return
	}
	if p.confirmed[id] {
		p.commitLocked(c)
		return
	}
	if len(p.pending) >= 1 {
		p.pending = map[string]*captured{}
	}
	p.pending[id] = c
}

// Tick is called with freshly manager-authorized policy and execution identity.
// No manager answer, role removal, archive, compaction or a switch fails closed.
// This never writes to the CLI or to the conversation/event/notification stores.
func (p *Proxy) Tick(ctx context.Context, now time.Time, policy *assistantwire.CacheKeepalive, idle bool) {
	p.mu.Lock()
	if policy == nil || !policy.Enabled || !policy.Valid() {
		p.forgetLocked()
		p.blocked = true
		p.mu.Unlock()
		return
	}
	if p.checking {
		p.mu.Unlock()
		return
	}
	c := p.last
	if p.attempts >= policy.MaxRequests {
		p.forgetLocked()
		p.mu.Unlock()
		return
	}
	if !idle || p.blocked || p.active != 0 || c == nil || now.Before(p.next.Add(time.Duration(policy.IntervalMinutes)*time.Minute)) {
		p.mu.Unlock()
		return
	}
	if now.Sub(c.at) >= time.Hour {
		p.forgetLocked()
		p.mu.Unlock()
		return
	}
	body, _, err := Replay(c.body)
	if err != nil {
		p.forgetLocked()
		p.mu.Unlock()
		return
	}
	run, cancel := context.WithTimeout(ctx, 2*time.Minute)
	p.cancel = cancel
	p.checking = true
	p.attempts++
	p.next = now
	generation := p.generation
	p.mu.Unlock()
	defer cancel()
	req, err := http.NewRequestWithContext(run, http.MethodPost, c.target, bytes.NewReader(body))
	var usage struct {
		CacheRead     int `json:"cache_read_input_tokens"`
		CacheCreation int `json:"cache_creation_input_tokens"`
		Output        int `json:"output_tokens"`
	}
	hit := false
	if err == nil {
		req.Header = c.header.Clone()
		req.Header.Del("Content-Length")
		req.Header.Del("Accept-Encoding")
		req.Header.Set("Accept", "application/json")
		req.Header.Set(RequestKindHeader, "cache-keepalive")
		if resp, e := p.client.Do(req); e == nil {
			var result struct {
				Usage *struct {
					CacheRead     int `json:"cache_read_input_tokens"`
					CacheCreation int `json:"cache_creation_input_tokens"`
					Output        int `json:"output_tokens"`
				} `json:"usage"`
			}
			e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
			_ = resp.Body.Close()
			if e == nil && result.Usage != nil {
				usage = *result.Usage
				hit = resp.StatusCode >= 200 && resp.StatusCode < 300 && usage.CacheRead > 0
			}
		}
	}
	p.mu.Lock()
	p.checking = false
	if p.generation == generation {
		p.cancel = nil
		if hit {
			p.last = &captured{c.body, c.header, c.target, now, generation}
		} else {
			p.forgetLocked()
		}
	}
	p.mu.Unlock()
	if p.logf != nil {
		p.logf("cache_keepalive session=%s at=%s cache_read=%d cache_creation=%d output=%d", p.session, now.UTC().Format(time.RFC3339), usage.CacheRead, usage.CacheCreation, usage.Output)
	}
}

type roundTripper struct {
	p         *Proxy
	transport http.RoundTripper
}

func (rt roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	p := rt.p
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") || r.Body == nil {
		return rt.transport.RoundTrip(r)
	}
	// A normal request immediately cancels an in-flight refresh, never waiting for it.
	p.mu.Lock()
	p.forgetLocked()
	p.attempts = 0
	p.active++
	gen := p.generation
	blocked := p.blocked
	p.mu.Unlock()
	capture := &boundedBuffer{}
	r.Body = &teeBody{Reader: io.TeeReader(r.Body, capture), Closer: r.Body}
	at := time.Now()
	resp, err := rt.transport.RoundTrip(r)
	if err != nil {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
		return resp, err
	}
	capturedBody, over := capture.snapshot()
	c := &captured{body: capturedBody, header: r.Header.Clone(), target: r.URL.String(), at: at, generation: gen}
	valid := !blocked && !over && resp.StatusCode >= 200 && resp.StatusCode < 300
	if _, _, e := Replay(c.body); e != nil {
		valid = false
	}
	tap := &responseTap{p: p, c: c, valid: valid, stream: strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")}
	originalBody := resp.Body
	if originalBody == nil {
		originalBody = http.NoBody
	}
	var closeOnce sync.Once
	var closeErr error
	resp.Body = &teeBody{
		Reader: io.TeeReader(originalBody, tap),
		Closer: closeFunc(func() error {
			closeOnce.Do(func() {
				p.mu.Lock()
				p.active--
				p.mu.Unlock()
				closeErr = originalBody.Close()
			})
			return closeErr
		}),
	}
	return resp, nil
}

type boundedBuffer struct {
	mu sync.Mutex
	bytes.Buffer
	over bool
}

func (b *boundedBuffer) Write(v []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(v)
	if b.Len()+n > bodyMost {
		b.over = true
		return n, nil
	}
	_, _ = b.Buffer.Write(v)
	return n, nil
}

func (b *boundedBuffer) snapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.Bytes()), b.over
}

type teeBody struct {
	io.Reader
	io.Closer
}
type closeFunc func() error

func (f closeFunc) Close() error { return f() }

type responseTap struct {
	p                   *Proxy
	c                   *captured
	valid, stream, done bool
	buf                 []byte
}

func (t *responseTap) Write(v []byte) (int, error) {
	n := len(v)
	if !t.valid || t.done {
		return n, nil
	}
	if len(t.buf)+n > 64<<10 {
		t.done = true
		t.buf = nil
		return n, nil
	}
	t.buf = append(t.buf, v...)
	var event struct {
		Type    string     `json:"type"`
		ID      string     `json:"id"`
		Usage   cacheUsage `json:"usage"`
		Message struct {
			Usage cacheUsage `json:"usage"`
			ID    string     `json:"id"`
		} `json:"message"`
	}
	if !t.stream {
		if json.Unmarshal(t.buf, &event) == nil && event.ID != "" && event.Usage.cached() {
			t.p.candidate(event.ID, t.c)
			t.done = true
			t.buf = nil
		}
		return n, nil
	}
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(t.buf[:i])
		t.buf = t.buf[i+1:]
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if json.Unmarshal(bytes.TrimSpace(line[5:]), &event) == nil && event.Type == "message_start" && event.Message.ID != "" && event.Message.Usage.cached() {
			t.p.candidate(event.Message.ID, t.c)
			t.done = true
			t.buf = nil
			break
		}
	}
	return n, nil
}

// Cache-control declarations alone do not prove that the provider cached anything
// (for example a prompt below its minimum). Retain only a reported read or write.
type cacheUsage struct {
	Read  int `json:"cache_read_input_tokens"`
	Write int `json:"cache_creation_input_tokens"`
}

func (u cacheUsage) cached() bool { return u.Read > 0 || u.Write > 0 }
