package screenlink

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"sync"
	"time"
)

// The node's direct routes (#6398): one TLS listener port, bound to each candidate address and open only while a
// session is live. A connection must finish TLS 1.3 with ALPN persona-screen/1 and send attach with a live session's
// token within AttachWait, or it is closed; it then carries that session as one more transport.

// ALPN is the TLS application protocol a direct connection speaks.
const ALPN = "persona-screen/1"

const (
	maxHandshakes = 16 // unfinished direct handshakes at once
	maxDirect     = 8  // direct transports per session
	certName      = "screen.persona.invalid"
)

// Direct is the node's listener for direct routes, shared by its sessions.
type Direct struct {
	logf       func(string, ...any)
	interfaces func() ([]Iface, error)
	pick       func([]Iface) []Candidate
	blocked    func() bool // the firewall would stop or ask about incoming connections
	recheck    time.Duration
	attachWait time.Duration
	// firewallEvery: how long the firewall's answer is kept (reading it starts three processes).
	firewallEvery time.Duration

	cert tls.Certificate
	pin  string

	mu          sync.Mutex
	sessions    map[[32]byte]*Session
	listeners   []net.Listener
	port        int
	cands       []Candidate
	unavailable string
	handshakes  int
	stop        chan struct{}
	blockedAt   time.Time
	blockedWas  bool
}

// NewDirect makes the node's direct listener with a certificate on a key made now, in memory only: each node process
// has its own, and the phone pins it from the session's paths.
func NewDirect(logf func(string, ...any)) (*Direct, error) {
	cert, pin, err := selfSigned()
	if err != nil {
		return nil, err
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Direct{logf: logf, interfaces: SystemInterfaces, pick: Candidates, blocked: firewallBlocks, recheck: 5 * time.Second, firewallEvery: time.Minute,
		attachWait: DefaultLimits.AttachWait, cert: cert, pin: pin, sessions: map[[32]byte]*Session{}}, nil
}

// selfSigned is a P-256 certificate naming no host or address (whoever reaches the port can read it), and its pin:
// SHA-256 of the certificate, base64url without padding.
func selfSigned() (tls.Certificate, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: certName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(10 * 365 * 24 * time.Hour), // the process outlives no certificate check: the pin is what counts
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func tokenKey(token string) [32]byte { return sha256.Sum256([]byte(token)) }

// join registers s and returns the paths to tell its phone; the first session starts listening.
func (d *Direct) join(s *Session) Paths {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sessions[tokenKey(s.token)] = s
	if d.stop == nil {
		d.stop = make(chan struct{})
		d.refreshLocked()
		go d.watch(d.stop)
	}
	return d.pathsLocked(s.token)
}

// leave forgets s; the last session to go stops listening.
func (d *Direct) leave(s *Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sessions, tokenKey(s.token))
	if len(d.sessions) == 0 && d.stop != nil {
		close(d.stop)
		d.stop = nil
		d.closeListenersLocked()
		d.cands, d.unavailable, d.port = nil, "", 0
		d.blockedAt = time.Time{}
	}
}

func (d *Direct) pathsLocked(token string) Paths {
	p := Paths{Version: 1, Token: token, Candidates: append([]Candidate{}, d.cands...), Unavailable: d.unavailable}
	if len(d.cands) > 0 {
		p.Cert, p.Port = d.pin, d.port
	}
	return p
}

// watch re-reads the interfaces every recheck while listening; a change rebinds and tells every session.
func (d *Direct) watch(stop chan struct{}) {
	t := time.NewTicker(d.recheck)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		d.mu.Lock()
		if d.stop != stop {
			d.mu.Unlock()
			return
		}
		before, why := d.cands, d.unavailable
		d.refreshLocked()
		changed := !reflect.DeepEqual(before, d.cands) || why != d.unavailable
		var tell []*Session
		if changed {
			for _, s := range d.sessions {
				tell = append(tell, s)
			}
		}
		d.mu.Unlock()
		for _, s := range tell {
			d.mu.Lock()
			p := d.pathsLocked(s.token)
			d.mu.Unlock()
			s.setPaths(p)
		}
	}
}

// refreshLocked reads the candidates and listens on each, on one port (the same one again when it can).
func (d *Direct) refreshLocked() {
	if d.blockedAt.IsZero() || time.Since(d.blockedAt) >= d.firewallEvery {
		d.blockedAt, d.blockedWas = time.Now(), d.blocked()
	}
	if d.blockedWas {
		if d.unavailable != "firewall" {
			d.logf("screenlink: the firewall blocks incoming connections: no direct routes")
		}
		d.closeListenersLocked()
		d.cands, d.unavailable = nil, "firewall"
		return
	}
	d.unavailable = ""
	ifs, err := d.interfaces()
	if err != nil {
		d.logf("screenlink: interfaces: %v", err)
		return
	}
	want := d.pick(ifs)
	if reflect.DeepEqual(want, d.cands) && len(d.listeners) > 0 {
		return
	}
	d.closeListenersLocked()
	d.cands = nil
	port := d.port
	for _, c := range want {
		l, err := net.Listen("tcp", net.JoinHostPort(c.Addr, strconv.Itoa(port)))
		if err != nil && port != 0 {
			continue // the port is taken on this address: the candidate is left out
		}
		if err != nil {
			continue
		}
		if port == 0 {
			port = l.Addr().(*net.TCPAddr).Port
		}
		d.listeners = append(d.listeners, l)
		d.cands = append(d.cands, c)
		go d.accept(l)
	}
	d.port = port
	d.logf("screenlink: listening for direct routes on %d addresses", len(d.cands))
}

func (d *Direct) closeListenersLocked() {
	for _, l := range d.listeners {
		_ = l.Close()
	}
	d.listeners = nil
}

func (d *Direct) accept(l net.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		busy := d.handshakes >= maxHandshakes
		if !busy {
			d.handshakes++
		}
		d.mu.Unlock()
		if busy {
			_ = c.Close()
			continue
		}
		go func() {
			ok := d.handshake(c)
			d.mu.Lock()
			d.handshakes--
			d.mu.Unlock()
			if !ok {
				_ = c.Close()
			}
		}()
	}
}

// handshake is TLS, then attach with a live session's token, within attachWait; the connection then belongs to that
// session.
func (d *Direct) handshake(c net.Conn) bool {
	_ = c.SetDeadline(time.Now().Add(d.attachWait))
	tc := tls.Server(c, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{d.cert}, NextProtos: []string{ALPN}})
	if err := tc.Handshake(); err != nil || tc.ConnectionState().NegotiatedProtocol != ALPN {
		return false
	}
	r := newReader(tc)
	typ, body, err := r.next()
	if err != nil || typ != TypeAttach {
		return false
	}
	var a struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(body, &a) != nil || a.Token == "" {
		return false
	}
	d.mu.Lock()
	s := d.sessions[tokenKey(a.Token)]
	d.mu.Unlock()
	if s == nil {
		d.logf("screenlink: a direct connection sent no live session's token")
		return false
	}
	_ = c.SetDeadline(time.Time{})
	var counter func() (uint32, uint32)
	if t, ok := c.(*net.TCPConn); ok {
		counter = packetCounter(t)
	}
	return s.addDirect(tc, r, counter)
}

// packetCounter is the packets retransmitted and sent on c since its last call.
func packetCounter(c *net.TCPConn) func() (uint32, uint32) {
	var mu sync.Mutex
	var lastSent, lastRetrans uint64
	return func() (uint32, uint32) {
		sent, retrans, ok := tcpPackets(c)
		if !ok {
			return 0, 0
		}
		mu.Lock()
		defer mu.Unlock()
		ds, dr := sent-lastSent, retrans-lastRetrans
		lastSent, lastRetrans = sent, retrans
		return uint32(min(dr, 1<<32-1)), uint32(min(ds, 1<<32-1))
	}
}
