// Package egress is the hub's proxy for extensions' outbound connections (docs/extensions/extensions.md "Egress"): each extension
// it knows reaches only the hosts of its permissions.network, the hub's own API under the name HubHost when it was
// granted any of it, and the hub machine's loopback under the name LocalHost when its permissions.network lists a
// loopback address. A sandboxed extension has no other way out; one outside a sandbox is pointed at it.
package egress

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// HubHost is the hub's own API as an extension behind the proxy reaches it (its loopback is not the hub's).
const HubHost = "hub.persona.internal"

// LocalHost is the hub machine's loopback as an extension behind the proxy reaches it: LocalHost:<port> is
// 127.0.0.1:<port> there. A sandbox's loopback is not the hub machine's, and HTTP clients (Go's net/http among them)
// never send a request for localhost or a loopback IP through a proxy, so a loopback address in permissions.network
// works only under this name.
const LocalHost = "host.persona.internal"

// loopbackNames are what permissions.network lists to let an extension reach LocalHost.
var loopbackNames = []string{"127.0.0.1", "::1", "localhost", LocalHost}

// User is the proxy's user name in an extension's proxy URL (the password tells which extension it is).
const User = "ext"

// Rule is what one extension may reach.
type Rule struct {
	// Hosts are permissions.network: names, or *.suffix for any name under it. An IP address passes only when it is
	// listed as it is.
	Hosts []string
	// Hub: it may reach HubHost (it holds a token of the hub's API).
	Hub bool
	// Targets are the addresses its settings name now (permissions.network "config:<field>", #5906): each lets
	// through that host and port only, as written (no subdomain), an https tunnel unless HTTP.
	Targets []Target
	// Owner are the hosts the owner allowed it in the admin UI (#5697): each that host only (no subdomain), on its
	// port or on any port when it has none, https or http. Reached like Targets: every address the name resolves to is
	// checked on each connection (private LAN addresses pass; loopback, link-local and metadata ones refuse the name),
	// and only checked addresses are dialled.
	Owner []Target
}

// owned says whether host:port is one of the hosts the owner allowed (Rule.Owner).
func (r Rule) owned(host, port string) bool {
	return slices.ContainsFunc(r.Owner, func(t Target) bool { return t.Host == host && (t.Port == "" || t.Port == port) })
}

// Target is one address an extension's settings name (extension.ConfiguredTarget).
type Target struct {
	Host, Port string
	// HTTP: plain http to it is let through too (the manifest says config:<field>:http, and the address is http).
	HTTP bool
}

// IsLoopbackHost reports whether h is a loopback host on this machine (127.0.0.1, localhost, ::1 or LocalHost).
func IsLoopbackHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), "."))
	return slices.Contains(loopbackNames, h)
}

// target says whether host:port is one of r's Targets, for a CONNECT tunnel (connect) or a plain http request.
func (r Rule) target(host, port string, connect bool) bool {
	return slices.ContainsFunc(r.Targets, func(t Target) bool {
		return t.Host == host && t.Port == port && (connect || t.HTTP)
	})
}

// loopbackTarget says whether r has a loopback Target on port for connect/http, returning the loopback IP to dial.
func (r Rule) loopbackTarget(port string, connect bool) (string, bool) {
	for _, t := range r.Targets {
		if IsLoopbackHost(t.Host) && t.Port == port && (connect || t.HTTP) {
			if strings.Trim(t.Host, "[]") == "::1" {
				return "::1", true
			}
			return "127.0.0.1", true
		}
	}
	return "", false
}

// Loopback says whether the rule lets its extension reach LocalHost: its permissions.network lists a loopback address
// (or LocalHost itself) as it is.
func (r Rule) Loopback() bool {
	return slices.ContainsFunc(r.Hosts, func(h string) bool {
		return slices.Contains(loopbackNames, strings.ToLower(strings.Trim(h, "[]")))
	})
}

// Counts are an extension's connections through the proxy.
type Counts struct {
	Allowed    int64     `json:"allowed"`
	Denied     int64     `json:"denied"`
	LastDenied string    `json:"last_denied,omitempty"`
	DeniedAt   time.Time `json:"denied_at,omitzero"`
	// Recent are the last RecentDenied different hosts it was refused, the latest first (#5697): in memory only.
	Recent []Denied `json:"recent_denied,omitempty"`
}

// Denied is one host an extension was refused: how often and when last.
type Denied struct {
	Host  string    `json:"target"`
	Count int64     `json:"count"`
	At    time.Time `json:"at"`
}

// RecentDenied is how many refused hosts Counts.Recent keeps.
const RecentDenied = 5

// Proxy is the hub's egress proxy: one loopback TCP listener (an extension says who it is with its credential) and a
// unix socket per sandboxed extension (the socket says who it is).
type Proxy struct {
	// HubAddr is the hub's own listener (host:port) that HubHost stands for.
	HubAddr string
	// Upstream is the proxy the hub itself goes out through for u (nil, nil: straight). Default: the hub's
	// environment (HTTPS_PROXY / HTTP_PROXY / NO_PROXY).
	Upstream func(u *url.URL) (*url.URL, error)
	// OnDecision is told each connection allowed or denied (the hub's metrics, docs/extensions/extensions.md "Egress").
	OnDecision func(ext, host string, allowed bool)
	// LocalAddrs are this machine's addresses (nil: its interfaces); tests replace it.
	LocalAddrs func() []net.IP
	// Resolve and Dial: tests replace them.
	Resolve func(ctx context.Context, host string) ([]net.IP, error)
	Dial    func(ctx context.Context, network, addr string) (net.Conn, error)
	Logf    func(string, ...any)

	unixMu        sync.Mutex // serializes Unix socket bind/replace/close; lock order: unixMu -> mu
	mu            sync.Mutex
	closed        bool
	ln            net.Listener
	rules         map[string]Rule
	creds         map[string]string // credential → extension
	credOf        map[string]string // extension → its credential
	checks        map[string]*check // a settings check's credential → the check (Check)
	unix          map[string]net.Listener
	relay         map[string]net.Listener // ext → its socket that leads to one fixed unix socket (RelayUnix)
	counts        map[string]*Counts
	active        map[string]map[*ioLease]struct{}
	runtimeActive map[string]int
	runtimeTotal  int
	runtimeSlots  chan struct{}     // shared by every runtimeListener (runtimeSlotsPerProxy)
	lastGood      map[string]string // CONNECT host:port → responding address; bounded by connectCacheMost
	connectWait   time.Duration     // tests may shorten the client first-flight wait
	connectBudget time.Duration     // tests may shorten the per-address response budget
}

// Start listens on loopback (an unused port).
func (p *Proxy) Start() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return net.ErrClosed
	}
	p.mu.Unlock()
	ln, err := osproc.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	p.ln = ln
	p.mu.Unlock()
	go p.serve(ln, "", false)
	return nil
}

// Addr is the loopback listener's host:port.
func (p *Proxy) Addr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// parseStrictPort parses a canonical ASCII decimal TCP port in 1..65535 (no leading zero, no sign, no service names).
func parseStrictPort(port string) (uint16, bool) {
	if len(port) == 0 || len(port) > 5 || port[0] == '0' {
		return 0, false
	}
	var n uint32
	for i := 0; i < len(port); i++ {
		c := port[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint32(c-'0')
	}
	if n == 0 || n > 65535 {
		return 0, false
	}
	return uint16(n), true
}

// ownPort says whether port is the loopback listener's own (LocalHost must not lead back into the proxy).
// Non-canonical or invalid ports fail closed (true).
func (p *Proxy) ownPort(port string) bool {
	want, ok := parseStrictPort(port)
	if !ok {
		return true
	}
	addr := p.Addr()
	if addr == "" {
		return false
	}
	_, own, err := net.SplitHostPort(addr)
	ownNum, ok := parseStrictPort(own)
	if err != nil || !ok {
		return true
	}
	return want == ownNum
}

// hubPort says whether port is the hub's own listener port (or 18660 when HubAddr is unset).
// Non-canonical, invalid, or unparseable ports fail closed (true).
func (p *Proxy) hubPort(port string) bool {
	want, ok := parseStrictPort(port)
	if !ok {
		return true
	}
	if p.HubAddr == "" {
		return want == 18660
	}
	_, hp, err := net.SplitHostPort(p.HubAddr)
	hubNum, ok := parseStrictPort(hp)
	if err != nil || !ok {
		return true
	}
	return want == hubNum
}

// Close stops every listener and revokes all registrations, settings checks, and active I/O.
func (p *Proxy) Close() {
	p.unixMu.Lock()
	defer p.unixMu.Unlock()
	p.mu.Lock()
	p.closed = true
	ln := p.ln
	p.ln = nil
	toClose := make([]net.Listener, 0, len(p.unix)+len(p.relay))
	for _, l := range p.unix {
		toClose = append(toClose, l)
	}
	for _, l := range p.relay {
		toClose = append(toClose, l)
	}
	for ext := range p.active {
		p.cancelIOLocked(ext)
	}
	for _, c := range p.checks {
		p.endCheckLocked(c)
	}
	clear(p.rules)
	clear(p.creds)
	clear(p.credOf)
	p.unix, p.relay = nil, nil
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for _, l := range toClose {
		_ = l.Close()
	}
}

// SetTargets replaces the addresses ext's settings name (Rule.Targets) while its program runs: its settings changed,
// and the address they named before is not let through from now on. Nothing for an extension not registered.
func (p *Proxy) SetTargets(ext string, targets []Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r, ok := p.rules[ext]; ok {
		p.cancelIOLocked(ext)
		r.Targets = slices.Clone(targets)
		p.rules[ext] = r
	}
}

// SetOwner replaces the hosts the owner allowed ext (Rule.Owner) while its program runs. A host taken away is refused
// from now on: its open connections are closed, as SetTargets does; one only added leaves them be.
func (p *Proxy) SetOwner(ext string, owner []Target) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.rules[ext]
	if !ok {
		return
	}
	for _, t := range r.Owner {
		if !slices.Contains(owner, t) {
			p.cancelIOLocked(ext)
			break
		}
	}
	r.Owner = slices.Clone(owner)
	p.rules[ext] = r
}

// Register sets ext's rule and gives it a new credential (each start of its program): the URL of the loopback
// listener with it, for HTTP(S)_PROXY.
func (p *Proxy) Register(ext string, r Rule) string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	cred := hex.EncodeToString(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ""
	}
	if p.rules == nil {
		p.rules, p.creds, p.credOf, p.counts = map[string]Rule{}, map[string]string{}, map[string]string{}, map[string]*Counts{}
	}
	p.cancelIOLocked(ext)
	p.endChecksLocked(ext)
	if old, ok := p.credOf[ext]; ok {
		delete(p.creds, old)
	}
	p.rules[ext] = Rule{Hosts: slices.Clone(r.Hosts), Hub: r.Hub, Targets: slices.Clone(r.Targets), Owner: slices.Clone(r.Owner)}
	p.creds[cred], p.credOf[ext] = ext, cred
	addr := ""
	if p.ln != nil {
		addr = p.ln.Addr().String()
	}
	return (&url.URL{Scheme: "http", User: url.UserPassword(User, cred), Host: addr}).String()
}

// Unregister forgets ext: its credential and its socket stop working (switched off, removed).
func (p *Proxy) Unregister(ext string) {
	p.unixMu.Lock()
	defer p.unixMu.Unlock()
	p.mu.Lock()
	p.cancelIOLocked(ext)
	p.endChecksLocked(ext)
	if cred, ok := p.credOf[ext]; ok {
		delete(p.creds, cred)
		delete(p.credOf, ext)
	}
	delete(p.rules, ext)
	unixLn := p.unix[ext]
	delete(p.unix, ext)
	relayLn := p.relay[ext]
	delete(p.relay, ext)
	p.mu.Unlock()
	if unixLn != nil {
		_ = unixLn.Close()
	}
	if relayLn != nil {
		_ = relayLn.Close()
	}
}

// Stats are ext's counts so far.
func (p *Proxy) Stats(ext string) Counts {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.counts[ext]; c != nil {
		out := *c
		out.Recent = slices.Clone(c.Recent)
		return out
	}
	return Counts{}
}

// serve serves ln's HTTP proxy for ext ("": the loopback listener, which asks for credentials). source: a source
// runtime's socket, which takes only its fetch broker's requests.
func (p *Proxy) serve(ln net.Listener, ext string, source bool) {
	srv := &http.Server{Handler: p.handler(ext), ReadHeaderTimeout: 30 * time.Second,
		ErrorLog: nil}
	if source {
		// The guest can open raw sockets to its broker. Enforce the bounded
		// runtime route here, not just in the replaceable JavaScript environment.
		handler := srv.Handler
		srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Host != "" || r.URL.Path != RuntimeFetchPath || r.Method != http.MethodPost {
				// This path never enters runtimeFetch's body deadline. Do not let
				// net/http drain an incomplete hostile body before rejecting it.
				r.Close = true
				w.Header().Set("Connection", "close")
				http.Error(w, "runtime fetch broker required", http.StatusForbidden)
				return
			}
			handler.ServeHTTP(w, r)
		})
		srv.MaxHeaderBytes = 64 << 10
		srv.ReadHeaderTimeout = 10 * time.Second
	}
	_ = srv.Serve(ln)
}

func (p *Proxy) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// who is the extension a request on the loopback listener is from (its Proxy-Authorization), and the settings check
// whose credential it came with (nil: the program's own).
func (p *Proxy) who(r *http.Request) (string, *check) {
	pass := credential(r)
	if pass == "" {
		return "", nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if ext := p.creds[pass]; ext != "" {
		return ext, nil
	}
	if c := p.liveCheckLocked(pass); c != nil {
		return c.ext, c
	}
	return "", nil
}

// checkOf is the settings check of ext whose credential a request on ext's own socket came with (nil: none, or
// another extension's).
func (p *Proxy) checkOf(r *http.Request, ext string) *check {
	pass := credential(r)
	if pass == "" {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.liveCheckLocked(pass); c != nil && c.ext == ext {
		return c
	}
	return nil
}

// credential is the password of a request's Proxy-Authorization for User ("": none).
func credential(r *http.Request) string {
	auth := r.Header.Get("Proxy-Authorization")
	scheme, b64, ok := strings.Cut(auth, " ")
	if !ok || !strings.EqualFold(scheme, "Basic") {
		return ""
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return ""
	}
	user, pass, _ := strings.Cut(string(raw), ":")
	if user != User {
		return ""
	}
	return pass
}

func (p *Proxy) rule(ext string) (Rule, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.rules[ext]
	return r, ok
}

func (p *Proxy) count(ext, host string, allowed bool) {
	p.mu.Lock()
	if p.counts == nil {
		p.counts = map[string]*Counts{}
	}
	c := p.counts[ext]
	if c == nil {
		c = &Counts{}
		p.counts[ext] = c
	}
	if allowed {
		c.Allowed++
	} else {
		now := time.Now()
		c.Denied++
		c.LastDenied, c.DeniedAt = host, now
		d := Denied{Host: host, At: now}
		if i := slices.IndexFunc(c.Recent, func(d Denied) bool { return d.Host == host }); i >= 0 {
			d.Count = c.Recent[i].Count
			c.Recent = slices.Delete(c.Recent, i, i+1)
		}
		d.Count++
		c.Recent = slices.Insert(c.Recent, 0, d)
		if len(c.Recent) > RecentDenied {
			c.Recent = c.Recent[:RecentDenied]
		}
	}
	p.mu.Unlock()
	if p.OnDecision != nil {
		p.OnDecision(ext, host, allowed)
	}
}

// Matches says whether host is one of the patterns: the name itself, or under a *.suffix one.
func Matches(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pat := range patterns {
		if pat == host || strings.HasPrefix(pat, "*.") && strings.HasSuffix(host, pat[1:]) {
			return true
		}
	}
	return false
}
