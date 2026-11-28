package link

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/osproc"
)

// The passive listener (docs/operations/link.md "Passive mode"): TLS with the connector's own identity, four endpoints and nothing
// else. Pairing trades a one-time code for the hub's credential (its hash is kept); info, attention and stream need
// that credential; a stream is joined to the resident node's socket byte for byte, as active mode's streams are.

// errBadCode: the pairing code is wrong, ran out or was used.
var errBadCode = errors.New("bad pairing code")

// pairBodyMax caps a pairing request (a settings value).
const pairBodyMax = 16 << 10

// passiveReadTimeout bounds how long one request may take to arrive, body included (a settings value). Pairing reads
// its body before any code is checked, and net/http drains a small unread body before it answers, so without it a peer
// that sends headers and then stalls the body holds a goroutine and a connection forever. Streams are not cut: net/http
// clears the deadline when websocket.Accept hijacks the connection. A variable so that tests can shrink it.
var passiveReadTimeout = 30 * time.Second

// streamClose is why the listener ends a stream itself: the credential it came with is no longer the one (another
// hub paired), or persona-link stops listening (it stops, or switched to active mode).
const (
	closeCredential = CloseRevoked              // 4002, as active mode's revoked credential
	closeStopping   = websocket.StatusGoingAway // 1001
)

// passive is one listener's state.
type passive struct {
	o       Options
	rep     *reporter
	id      *identity
	lim     *authLimiter
	listen  string // as shown: ":18663", or the address it is bound to
	stopped chan struct{}
	// handlers: the streams' handlers still running (serve waits for them when it stops).
	handlers sync.WaitGroup

	// syncMu serializes reading link.json and applying it to s.cfg (lock order: syncMu -> mu).
	syncMu  sync.Mutex
	mu      sync.Mutex
	cfg     Passive
	streams map[*pstream]struct{}
	addrs   []string
	// lastSeen, from: when the hub last came in (pairing, info, a stream) and from where; seenSaid: when the status last
	// said so (info comes often: not every call is a status line).
	lastSeen time.Time
	from     string
	seenSaid time.Time
	// node: whether the last stream got through to the resident node (online | offline), and why not.
	node, nodeErr string
	// attention: the paired hub's latest "Needs you" summary (POST ConnectorAttentionPath), dropped with its
	// credential or once the hub stopped coming in (attentionStale).
	attention *Attention
	// logged, suppressed: refusals are logged at most every refusalLog.
	logged     time.Time
	suppressed int

	// testAfterPairUpdate, when non-nil in tests, runs in pair after Update releases
	// the file lock and before syncing in-memory config.
	testAfterPairUpdate func()
	// testReadConfig, when non-nil in tests, replaces Read inside syncFromDisk.
	testReadConfig func(string) (*Config, error)
	// testAfterKeepNameUpdate, when non-nil in tests, runs in keepMachineName
	// after Update releases the file lock and before acquiring s.mu.
	testAfterKeepNameUpdate func()
	// testDialNode, when non-nil in tests, overrides dialing the resident node socket in stream.
	testDialNode func() (net.Conn, error)
}

// pstream is a stream from the hub to the resident node.
type pstream struct {
	token string // the hash of the credential it came with
	since time.Time
	from  string
	end   func(code websocket.StatusCode, reason string)
}

const refusalLog = 30 * time.Second // at most one line per this about refused requests (a setting)

// runPassive listens for the hub until ctx ends (another mode, or persona-link stops). A port that cannot be had is
// retried with the same backoff as active mode's attempts, sooner when link.json changes (another port, say).
func runPassive(ctx context.Context, o Options, rep *reporter) {
	rep.switchTo(ModePassive)
	lim := o.limiter
	if lim == nil {
		lim = newAuthLimiter()
	}
	var b backoff
	for ctx.Err() == nil {
		was := stampOf(o.Path) // a change from here on (another port, say) cuts the waits below short
		c, err := Read(o.Path)
		if err != nil {
			rep.set(func(s *Status) { *s = Status{State: "waiting", Error: err.Error()} })
			o.wait(ctx, b.next(), was)
			continue
		}
		p := Passive{}
		if c.Passive != nil {
			p = *c.Passive
		}
		listen := p.ListenAddr()
		id, err := loadIdentity(TLSDir(o.Path), time.Now())
		if err != nil {
			rep.set(func(s *Status) {
				*s = Status{State: "waiting", Listen: listen, Error: defaultText("link-errors.tls_identity", i18n.Args{"error": err})}
			})
			o.wait(ctx, b.next(), was)
			continue
		}
		ln, err := osproc.Listen("tcp", listen)
		if err != nil {
			wait := b.next()
			retry := time.Now().Add(wait)
			o.Logf("passive: listen %s: %v; next attempt in %s", listen, err, wait.Round(time.Second))
			rep.set(func(s *Status) {
				*s = Status{State: "waiting", Listen: listen, Machine: p.Machine, HubName: p.HubName, MachineName: p.MachineName, Retry: &retry,
					Error: defaultText("link-errors.listen_failed", i18n.Args{"listen": listen, "error": describeListen(err)})}
			})
			o.wait(ctx, wait, was)
			continue
		}
		b = backoff{}
		s := &passive{o: o, rep: rep, id: id, lim: lim, listen: shownListen(listen, ln.Addr()), cfg: p,
			streams: map[*pstream]struct{}{}, stopped: make(chan struct{})}
		s.serve(ctx, ln)
	}
}

func describeListen(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return defaultText("link-errors.listen_permission")
	case errors.Is(err, syscall.EADDRINUSE), strings.Contains(err.Error(), "address already in use"), strings.Contains(err.Error(), "Only one usage"):
		return defaultText("link-errors.listen_in_use")
	case errors.Is(err, syscall.EADDRNOTAVAIL), strings.Contains(err.Error(), "assign requested address"):
		return defaultText("link-errors.listen_address_missing")
	}
	return err.Error()
}

// shownListen is where the listener is, as people write it: ":18663" for every address.
func shownListen(listen string, bound net.Addr) string {
	host, _, _ := net.SplitHostPort(listen)
	ap, err := netip.ParseAddrPort(bound.String())
	if err != nil {
		return listen
	}
	if host == "" || ap.Addr().IsUnspecified() {
		if host == "0.0.0.0" {
			return net.JoinHostPort(host, strconv.Itoa(int(ap.Port())))
		}
		return ":" + strconv.Itoa(int(ap.Port()))
	}
	return ap.String()
}

// serve runs the listener on ln until ctx ends or link.json moves it elsewhere.
func (s *passive) serve(ctx context.Context, ln net.Listener) {
	o := s.o
	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       passiveReadTimeout,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return s.id.certificate(time.Now()) }},
		// Handshakes that fail (scanners, a hub that pinned another key) are not news: counted in, not printed one by one.
		ErrorLog: log.New(writerFunc(func(p []byte) { s.refusal("tls: %s", strings.TrimSpace(string(p))) }), "", 0),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.ServeTLS(ln, "", "")
	}()
	// The addresses the hub may use: the configured host (all of them when none) with the port actually bound.
	cfg := s.config()
	listenAddr := cfg.ListenAddr()
	host, _, _ := net.SplitHostPort(listenAddr)
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	reachable := net.JoinHostPort(host, port)
	addrs, _ := ListenCandidates(reachable)
	s.mu.Lock()
	s.addrs = addrs
	s.mu.Unlock()
	o.Logf("passive: listening on %s (connector %s)", s.listen, s.id.fp)
	s.report()
	was := stampOf(o.Path)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case <-o.Check: // the network changed: the addresses to show
			addrs, _ := ListenCandidates(reachable)
			s.mu.Lock()
			s.addrs = addrs
			s.mu.Unlock()
			s.report()
		case <-tick.C:
			s.dropStaleAttention(time.Now())
			now := stampOf(o.Path)
			if now.Equal(was) {
				continue
			}
			was = now
			modeOK, addr, err := s.syncFromDisk(nil, listenAddr)
			if err != nil || !modeOK {
				running = false // Run's watcher ends ctx as well
				continue
			}
			if addr != listenAddr {
				o.Logf("passive: moving from %s to %s", listenAddr, addr)
				running = false
				continue
			}
		}
	}
	s.mu.Lock()
	close(s.stopped)
	for st := range s.streams {
		st.end(closeStopping, "persona-link stopped listening")
	}
	s.mu.Unlock()
	_ = srv.Close() // the listener and connections still speaking HTTP; the streams are hijacked: ended above
	<-done
	// Their close handshakes take a moment (coder/websocket waits up to 5 s for the hub's answer).
	waited := make(chan struct{})
	go func() { s.handlers.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
	}
	o.Logf("passive: stopped listening on %s", s.listen)
}

// config is passive mode's part of link.json as the listener last took it in.
func (s *passive) config() Passive {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// setConfig takes in passive mode's part of link.json as it is now: streams that came with a credential that is no
// longer the one end.
func (s *passive) setConfig(p Passive) {
	s.mu.Lock()
	s.setConfigLocked(p)
	s.reportLocked()
	s.mu.Unlock()
}

func (s *passive) setConfigLocked(p Passive) bool {
	changed := !samePassive(s.cfg, p)
	if !p.Paired() || p.TokenSHA256 != s.cfg.TokenSHA256 {
		if s.attention != nil {
			s.attention = nil // another hub's, or none's
			changed = true
		}
	}
	s.cfg = p
	for st := range s.streams {
		if !p.Paired() || st.token != p.TokenSHA256 {
			st.end(closeCredential, "credential replaced")
		}
	}
	return changed
}

type pairSync struct {
	paired     Passive
	prevPaired bool
	prevToken  string
	from       string
}

// syncFromDisk reloads passive mode's config from link.json under syncMu so concurrent pair()
// calls and serve()'s config watcher cannot roll s.cfg backward to a stale snapshot. When ps
// is non-nil, lastSeen/from are updated only if the latest on-disk config still holds that pairing;
// if Read hits a transient non-ErrNotExist error after pair()'s Update, ps.paired is applied only
// while s.cfg still matches the exact pre-pair credential state, so a concurrent newer pairing can
// never be rolled back while the previous credential is still revoked immediately.
// When expectedListen is non-empty and the mode ended or listen address moved, s.cfg is left
// untouched so serve() shuts down streams with closeStopping.
func (s *passive) syncFromDisk(ps *pairSync, expectedListen string) (modeOK bool, listenAddr string, err error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	read := Read
	if s.testReadConfig != nil {
		read = s.testReadConfig
	}
	c, err := read(s.o.Path)
	if err != nil {
		if expectedListen == "" {
			if errors.Is(err, os.ErrNotExist) {
				s.mu.Lock()
				if !samePassive(s.cfg, Passive{}) || s.attention != nil {
					s.cfg, s.attention = Passive{}, nil
					s.reportLocked()
				}
				s.mu.Unlock()
			} else if ps != nil {
				s.o.Logf("passive: reload after pair: %v", err)
				s.mu.Lock()
				matchesPrePair := (ps.prevPaired && s.cfg.Paired() && s.cfg.TokenSHA256 == ps.prevToken) ||
					(!ps.prevPaired && !s.cfg.Paired())
				if matchesPrePair {
					changed := s.setConfigLocked(ps.paired)
					if !s.seenLocked(ps.from) && changed {
						s.reportLocked()
					}
				}
				s.mu.Unlock()
			}
		}
		return false, "", err
	}
	if c.CurrentMode() != ModePassive {
		if expectedListen == "" {
			s.mu.Lock()
			if !samePassive(s.cfg, Passive{}) || s.attention != nil {
				s.cfg, s.attention = Passive{}, nil
				s.reportLocked()
			}
			s.mu.Unlock()
		}
		return false, "", nil
	}
	cur := Passive{}
	if c.Passive != nil {
		cur = *c.Passive
	}
	listenAddr = cur.ListenAddr()
	if expectedListen != "" && listenAddr != expectedListen {
		return true, listenAddr, nil
	}
	s.mu.Lock()
	changed := s.setConfigLocked(cur)
	if ps != nil && samePairing(cur, ps.paired) {
		if !s.seenLocked(ps.from) && changed {
			s.reportLocked()
		}
	} else if changed {
		s.reportLocked()
	}
	s.mu.Unlock()
	return true, listenAddr, nil
}

func sameTimePtr(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func samePairCode(a, b *PairCode) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.SHA256 == b.SHA256 && a.Expires.Equal(b.Expires)
}

func samePassive(a, b Passive) bool {
	return a.Listen == b.Listen &&
		a.HubID == b.HubID &&
		a.HubName == b.HubName &&
		a.Machine == b.Machine &&
		a.MachineName == b.MachineName &&
		a.TokenSHA256 == b.TokenSHA256 &&
		sameTimePtr(a.PairedAt, b.PairedAt) &&
		samePairCode(a.Code, b.Code)
}

func samePairing(cur, paired Passive) bool {
	return cur.Paired() &&
		cur.TokenSHA256 == paired.TokenSHA256 &&
		cur.HubID == paired.HubID &&
		cur.HubName == paired.HubName &&
		cur.Machine == paired.Machine &&
		sameTimePtr(cur.PairedAt, paired.PairedAt)
}

// report says what the listener is doing: connected while a stream is open, else listening (paired) or unpaired.
func (s *passive) report() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportLocked()
}

func (s *passive) reportLocked() {
	now := time.Now()
	st := Status{Listen: s.listen, Machine: s.cfg.Machine, HubName: s.cfg.HubName, MachineName: s.cfg.MachineName, Addrs: s.addrs,
		Node: s.node, NodeError: s.nodeErr, Attention: s.attention}
	if !s.lastSeen.IsZero() {
		seen := s.lastSeen
		st.LastSeen, st.From = &seen, s.from
	}
	if until := s.cfg.CodeUntil(now); !until.IsZero() {
		st.CodeExpires = &until
	}
	var newest *pstream
	for p := range s.streams {
		if newest == nil || p.since.After(newest.since) {
			newest = p
		}
	}
	switch {
	case newest != nil:
		since := newest.since
		st.State, st.Since, st.Address = "connected", &since, newest.from
	case s.cfg.Paired():
		st.State = "listening"
	default:
		st.State = "unpaired"
	}
	s.seenSaid = now
	s.rep.set(func(x *Status) { *x = st })
}

// seen notes that the hub came in from r just now; the status says so at once unless it said so lately.
func (s *passive) seen(r *http.Request) {
	from := remoteHost(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seenLocked(from)
}

// seenLocked updates lastSeen/from under s.mu and reports status when the source
// changed or it has not reported lately, returning whether it reported.
func (s *passive) seenLocked(from string) bool {
	now := time.Now()
	changed := from != s.from
	s.lastSeen, s.from = now, from
	if changed || now.Sub(s.seenSaid) > 30*time.Second {
		s.reportLocked()
		return true
	}
	return false
}

func (s *passive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == ConnectorPairPath:
		s.pair(w, r)
	case r.Method == http.MethodGet && r.URL.Path == ConnectorInfoPath:
		s.info(w, r)
	case r.Method == http.MethodPost && r.URL.Path == ConnectorAttentionPath:
		s.takeAttention(w, r)
	case r.Method == http.MethodGet && r.URL.Path == ConnectorStreamPath:
		s.stream(w, r)
	default: // nothing else is here: no shell, no files, no forwarding
		writeError(w, http.StatusNotFound, "not_found", "")
	}
}

func remoteIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap().WithZone("")
}

func remoteHost(r *http.Request) string {
	if ip := remoteIP(r); ip.IsValid() {
		return ip.String()
	}
	return r.RemoteAddr
}

// admit turns a blocked source away before anything else is looked at.
func (s *passive) admit(w http.ResponseWriter, r *http.Request) (key string, ok bool) {
	key = sourceKey(remoteIP(r))
	if wait := s.lim.retry(key, time.Now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((wait+time.Second-1)/time.Second))))
		writeError(w, http.StatusTooManyRequests, "rate_limited", defaultText("link-peer.rate_limited"))
		return key, false
	}
	return key, true
}

// refuse counts a failed authentication of the source and answers 401.
func (s *passive) refuse(w http.ResponseWriter, r *http.Request, key, code, message string) {
	blocked := s.lim.fail(key, time.Now())
	s.refusal("refused %s %s from %s: %s (blocked %v)", r.Method, r.URL.Path, maskSource(remoteIP(r)), code, blocked)
	writeError(w, http.StatusUnauthorized, code, message)
}

// refusal logs a refused request or handshake, at most one line every refusalLog with how many were left out.
func (s *passive) refusal(format string, args ...any) {
	s.mu.Lock()
	now := time.Now()
	if now.Sub(s.logged) < refusalLog {
		s.suppressed++
		s.mu.Unlock()
		return
	}
	n := s.suppressed
	s.logged, s.suppressed = now, 0
	s.mu.Unlock()
	if n > 0 {
		format += fmt.Sprintf(" (%d more left out)", n)
	}
	s.o.Logf("passive: "+format, args...)
}

// maskSource is a source for the log: its /24 or /64, not the address.
func maskSource(ip netip.Addr) string {
	if !ip.IsValid() {
		return "unknown"
	}
	bits := 64
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

// wrongCredential answers a request without the paired hub's credential.
var wrongCredential = defaultText("link-peer.wrong_credential")

// authorized admits r when it carries the paired hub's credential, and answers it otherwise (429 blocked, 401 refused).
func (s *passive) authorized(w http.ResponseWriter, r *http.Request) (Passive, bool) {
	key, ok := s.admit(w, r)
	if !ok {
		return Passive{}, false
	}
	p, ok := s.credential(r)
	if !ok {
		s.refuse(w, r, key, "unauthorized", wrongCredential)
	}
	return p, ok
}

// credential is the paired hub's part of link.json when r carries its credential.
func (s *passive) credential(r *http.Request) (Passive, bool) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	p := s.cfg
	s.mu.Unlock()
	if !ok || token == "" || !p.Paired() || !sameHash(token, p.TokenSHA256) {
		return p, false
	}
	return p, true
}

type pairRequest struct {
	Code    string `json:"code"`
	Token   string `json:"token"`
	Machine string `json:"machine"`
	HubID   string `json:"hub_id"`
	HubName string `json:"hub_name"`
	// MachineName: what the hub calls this computer (hubs that rename computers).
	MachineName string `json:"machine_name"`
}

func (s *passive) pair(w http.ResponseWriter, r *http.Request) {
	key, ok := s.admit(w, r)
	if !ok {
		return
	}
	var req pairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, pairBodyMax)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", defaultText("link-peer.pair_json_required"))
		return
	}
	machine, hubID, hubName := clipText(req.Machine, 128), clipText(req.HubID, 128), clipName(req.HubName)
	switch {
	case strings.TrimSpace(req.Code) == "":
		writeError(w, http.StatusBadRequest, "bad_request", defaultText("link-peer.code_required"))
		return
	case !goodToken(req.Token):
		writeError(w, http.StatusBadRequest, "bad_request", defaultText("link-peer.token_invalid"))
		return
	case machine == "":
		writeError(w, http.StatusBadRequest, "bad_request", defaultText("link-peer.machine_required"))
		return
	}
	now := time.Now()
	var paired Passive
	var prevPaired bool
	var prevToken string
	err := Update(s.o.Path, func(c *Config) error {
		p := c.Passive
		if c.CurrentMode() != ModePassive || p == nil || p.Code == nil || !now.Before(p.Code.Expires) || !sameHash(req.Code, p.Code.SHA256) {
			return errBadCode
		}
		prevPaired, prevToken = p.Paired(), p.TokenSHA256
		at := now.UTC()
		p.HubID, p.HubName, p.Machine, p.TokenSHA256, p.PairedAt, p.Code = hubID, hubName, machine, hashHex(req.Token), &at, nil
		p.MachineName = clipName(req.MachineName)
		paired = *p
		return nil
	})
	switch {
	case errors.Is(err, errBadCode):
		s.refuse(w, r, key, "bad_code", defaultText("link-peer.bad_code"))
		return
	case err != nil:
		s.o.Logf("passive: pairing: %v", err)
		writeError(w, http.StatusInternalServerError, "internal", defaultText("link-peer.pair_save_failed"))
		return
	}
	s.o.Logf("passive: paired with hub %q (%s) as %q, from %s", hubName, hubID, machine, maskSource(remoteIP(r)))
	if s.testAfterPairUpdate != nil {
		s.testAfterPairUpdate()
	}
	_, _, _ = s.syncFromDisk(&pairSync{
		paired:     paired,
		prevPaired: prevPaired,
		prevToken:  prevToken,
		from:       remoteHost(r),
	}, "") // streams of a hub paired before end now; never roll back a newer link.json
	writeJSON(w, http.StatusOK, map[string]string{"version": s.o.Version})
}

// goodToken: what a hub's credential looks like (it makes it up: long, random, printable).
func goodToken(t string) bool {
	if len(t) < 32 || len(t) > 512 {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] <= ' ' || t[i] > '~' {
			return false
		}
	}
	return true
}

// clipText keeps one line of printable text of at most n characters.
func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

func (s *passive) info(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r)
	if !ok {
		return
	}
	keptName, err := s.keepMachineName(p, r.Header.Get(MachineNameHeader))
	if errors.Is(err, errPairedAgain) {
		writeError(w, http.StatusUnauthorized, "unauthorized", wrongCredential)
		return
	}
	info := ConnectorInfo{Machine: p.Machine, Version: s.o.Version, Features: []string{FeatureAttention}}
	from := remoteHost(r)
	s.mu.Lock()
	if !s.cfg.Paired() || s.cfg.TokenSHA256 != p.TokenSHA256 {
		// Another hub paired (or pairing was cleared) while keepMachineName ran.
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "unauthorized", wrongCredential)
		return
	}
	nameChanged := keptName != "" && s.cfg.MachineName != keptName
	if nameChanged {
		s.cfg.MachineName = keptName
	}
	if s.attention != nil {
		info.AttentionRev = s.attention.Rev
	}
	if !s.seenLocked(from) && nameChanged {
		s.reportLocked()
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, info)
}

// ConnectorInfo is GET ConnectorInfoPath's answer: which computer pairing named and which build, what this connector
// takes besides streams (FeatureAttention), and the Rev of the summary it holds, so the hub posts one again whenever
// they differ (a post that failed, a connector that restarted). Older connectors answer only machine and version, and
// older managers read only those.
type ConnectorInfo struct {
	Machine      string   `json:"machine"`
	Version      string   `json:"version"`
	Features     []string `json:"features,omitempty"`
	AttentionRev string   `json:"attention_rev,omitempty"`
}

// attentionStale is how long a summary is kept after the paired hub last came in: four of its 30 s info reads (a
// setting). A hub that stopped coming in may have answered the cards meanwhile; it posts the summary again when it
// comes back. A variable so that tests can shrink it.
var attentionStale = 2 * time.Minute

// takeAttention is POST ConnectorAttentionPath: the paired hub's "Needs you" summary, kept for the status.
func (s *passive) takeAttention(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r)
	if !ok {
		return
	}
	// One JSON object and nothing after it but white space, the whole body within the cap: a second value, anything
	// else or a body over the cap is refused, however short the first object.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, AttentionBodyMax))
	var a Attention
	err := dec.Decode(&a)
	if err == nil {
		if err = dec.Decode(&json.RawMessage{}); err == io.EOF {
			err = nil
		} else if err == nil {
			err = errors.New("more than one value")
		}
	}
	if err != nil || a.Rev == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "want one attention object with its rev, at most 64 KiB")
		return
	}
	from := remoteHost(r)
	s.mu.Lock()
	if !s.cfg.Paired() || s.cfg.TokenSHA256 != p.TokenSHA256 {
		// Another hub paired while the body came in: this summary went with the old credential (setConfig).
		s.mu.Unlock()
		writeError(w, http.StatusUnauthorized, "unauthorized", wrongCredential)
		return
	}
	attnChanged := s.attention == nil || s.attention.Rev != a.Rev
	if attnChanged {
		s.attention = &a
	}
	if !s.seenLocked(from) && attnChanged {
		s.reportLocked()
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// dropStaleAttention forgets the summary once the paired hub has not come in for attentionStale.
func (s *passive) dropStaleAttention(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attention != nil && now.Sub(s.lastSeen) > attentionStale {
		s.attention = nil
		s.reportLocked()
	}
}

// keepMachineName keeps what the paired hub calls this computer (MachineNameHeader, escaped) in link.json, when it
// changed. It returns the persisted name when updated, or errPairedAgain if link.json no longer holds p's credential.
func (s *passive) keepMachineName(p Passive, escaped string) (string, error) {
	raw, err := url.QueryUnescape(escaped)
	name := clipName(raw)
	if err != nil || name == "" || name == p.MachineName {
		return "", nil
	}
	err = Update(s.o.Path, func(c *Config) error {
		if c.Passive == nil || c.Passive.TokenSHA256 != p.TokenSHA256 {
			return errPairedAgain
		}
		c.Passive.MachineName = name
		return nil
	})
	if err != nil {
		return "", err
	}
	if s.testAfterKeepNameUpdate != nil {
		s.testAfterKeepNameUpdate()
	}
	return name, nil
}

func isUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, value := range r.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "Upgrade") {
				return true
			}
		}
	}
	return false
}

// stream joins a WebSocket from the hub to the resident node: the node's socket first, so that a node that is not
// running is a 503 with the reason rather than a stream that ends at once.
func (s *passive) stream(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authorized(w, r)
	if !ok {
		return
	}
	if !isUpgrade(r) {
		writeError(w, http.StatusBadRequest, "bad_request", defaultText("link-peer.websocket_required"))
		return
	}
	from := remoteHost(r)
	var node net.Conn
	var err error
	if s.testDialNode != nil {
		node, err = s.testDialNode()
	} else {
		node, err = net.DialTimeout("unix", s.o.Target, 2*time.Second)
	}
	if err != nil {
		msg := nodeMissing(runtime.GOOS, s.o.Target).Error() // how this OS runs it, as in active mode (#3723)
		s.mu.Lock()
		if !s.cfg.Paired() || s.cfg.TokenSHA256 != p.TokenSHA256 {
			// Another hub paired (or pairing was cleared) while dialing the resident node.
			s.mu.Unlock()
			writeError(w, http.StatusUnauthorized, "unauthorized", wrongCredential)
			return
		}
		s.node, s.nodeErr = "offline", msg
		s.lastSeen, s.from = time.Now(), from
		s.reportLocked()
		s.mu.Unlock()
		writeError(w, http.StatusServiceUnavailable, "node_unavailable", msg)
		return
	}
	defer node.Close()
	// Compressed like active mode's streams and the ssh connections before them: node traffic is JSON, each message on
	// its own (StreamCompression). No Origin check (InsecureSkipVerify): the credential is a header no browser page can
	// set, as on the hub's own link endpoints.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: StreamCompression})
	if err != nil {
		return
	}
	// Not bound to a context that ends first: a stream ended with a reason (4002, 1001) must get its close frame out.
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer nc.Close()
	var once sync.Once
	finished := make(chan struct{})
	st := &pstream{token: p.TokenSHA256, since: time.Now(), from: from}
	st.end = func(code websocket.StatusCode, reason string) {
		once.Do(func() {
			go func() {
				_ = ws.Close(code, reason)
				_ = node.Close()
			}()
		})
	}
	s.mu.Lock()
	select {
	case <-s.stopped: // stopped listening meanwhile
		s.mu.Unlock()
		_ = ws.Close(closeStopping, "persona-link stopped listening")
		return
	default:
	}
	if !s.cfg.Paired() || s.cfg.TokenSHA256 != p.TokenSHA256 { // paired again meanwhile
		s.mu.Unlock()
		_ = ws.Close(closeCredential, "credential replaced")
		return
	}
	s.streams[st] = struct{}{}
	s.handlers.Add(1)
	defer s.handlers.Done()
	s.node, s.nodeErr = "online", ""
	s.lastSeen, s.from = time.Now(), from
	s.reportLocked()
	s.mu.Unlock()
	s.o.Logf("passive: stream from %s", maskSource(remoteIP(r)))
	defer func() {
		close(finished)
		s.mu.Lock()
		delete(s.streams, st)
		s.reportLocked()
		s.mu.Unlock()
		s.o.Logf("passive: stream from %s ended", maskSource(remoteIP(r)))
	}()
	// The hub's heartbeat when it says one (hub.json link: a slow link needs a longer one), else this side's own.
	every, timeout := s.o.PingEvery, s.o.PingTimeout
	if e, t, ok := pingFromHeader(r.Header.Get(PingHeader)); ok {
		every, timeout = e, t
	}
	go func() { // a stream whose TCP connection died quietly: the hub pings too, this side does not count on it
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-finished:
				return
			case <-t.C:
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				err := ws.Ping(ctx) // a timeout closes the connection: both copies below end
				cancel()
				if err != nil {
					_ = node.Close()
					return
				}
			}
		}
	}()
	// hub -> node: when the hub ends the stream, half-close so the node sees EOF and finishes. node -> hub decides
	// when the stream is over, as in active mode (forward) and node.Relay: everything the node wrote must reach the hub.
	go func() {
		_, _ = io.Copy(node, nc)
		if u, ok := node.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
	}()
	_, _ = io.Copy(nc, node)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]string{"error": code}
	if message != "" {
		body["message"] = message
	}
	writeJSON(w, status, body)
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) {
	f(p)
	return len(p), nil
}
