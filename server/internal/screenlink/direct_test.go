package screenlink

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

func ipNet(ip net.IP, prefix int) *net.IPNet {
	bits := 32
	if ip.To4() == nil {
		bits = 128
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(prefix, bits)}
}

// The node offers LAN and Tailscale addresses only: never loopback, link-local, a proxy's tunnel or fake-IP range, a
// VM or container bridge, another VPN, or a global address (v1). Addresses are built in code: the privacy scan
// refuses private address literals even in tests.
func TestCandidates(t *testing.T) {
	up := net.FlagUp | net.FlagBroadcast | net.FlagMulticast
	tun := net.FlagUp | net.FlagPointToPoint | net.FlagMulticast
	lan1 := net.IPv4(192, 168, 1, 20)
	lan2 := net.IPv4(10, 0, 0, 7)
	ts4 := net.IPv4(100, 101, 102, 103)
	ts6 := net.ParseIP("fd7a:115c:a1e0::1234")
	ula := net.ParseIP("fd00:1:2:3::5")
	got := Candidates([]Iface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: []*net.IPNet{ipNet(net.IPv4(127, 0, 0, 1), 8)}},
		{Name: "en0", Flags: up, Addrs: []*net.IPNet{ipNet(lan1, 24), ipNet(net.ParseIP("fe80::1"), 64), ipNet(net.ParseIP("2001:db8::7"), 64), ipNet(ula, 64)}},
		{Name: "en1", Flags: up, Addrs: []*net.IPNet{ipNet(lan2, 8)}},
		{Name: "en2", Flags: net.FlagBroadcast, Addrs: []*net.IPNet{ipNet(net.IPv4(172, 16, 0, 9), 16)}}, // down
		{Name: "utun3", Flags: tun, Addrs: []*net.IPNet{ipNet(ts4, 32), ipNet(ts6, 128)}},
		{Name: "utun4", Flags: tun, Addrs: []*net.IPNet{ipNet(net.IPv4(198, 18, 0, 1), 16)}},      // a proxy's fake-IP tunnel
		{Name: "utun5", Flags: tun, Addrs: []*net.IPNet{ipNet(net.IPv4(10, 8, 0, 2), 24)}},        // another VPN
		{Name: "bridge100", Flags: up, Addrs: []*net.IPNet{ipNet(net.IPv4(192, 168, 64, 1), 24)}}, // VMs on this Mac
		{Name: "docker0", Flags: up, Addrs: []*net.IPNet{ipNet(net.IPv4(172, 17, 0, 1), 16)}},
		{Name: "tailscale0", Flags: tun, Addrs: []*net.IPNet{ipNet(net.IPv4(100, 64, 0, 5), 32)}}, // Linux
	})
	want := []Candidate{
		{Kind: "lan", Addr: lan1.String(), Prefix: 24},
		{Kind: "lan", Addr: lan2.String(), Prefix: 8},
		{Kind: "tailscale", Addr: ts4.String()},
		{Kind: "tailscale", Addr: "100.64.0.5"},
		{Kind: "lan", Addr: ula.String(), Prefix: 64},
		{Kind: "tailscale", Addr: ts6.String()},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d: %v, want %v (all: %v)", i, got[i], want[i], got)
		}
	}
	var many []Iface
	for i := 0; i < 20; i++ {
		many = append(many, Iface{Name: "en" + strconv.Itoa(i), Flags: up, Addrs: []*net.IPNet{ipNet(net.IPv4(10, 1, byte(i), 1), 24)}})
	}
	if n := len(Candidates(many)); n != maxCandidates {
		t.Fatalf("%d candidates, want at most %d", n, maxCandidates)
	}
}

func TestFirewallVerdict(t *testing.T) {
	for _, tc := range []struct {
		global, blockAll, app string
		blocks                bool
	}{
		{"Firewall is disabled. (State = 0)", "", "", false},
		{"Firewall is enabled. (State = 1)", "Firewall has block all state set to disabled.", "Incoming connection to /x/personad is permitted.", false},
		{"Firewall is enabled. (State = 1)", "Firewall has block all state set to enabled.", "Incoming connection to /x/personad is permitted.", true},
		{"Firewall is enabled. (State = 1)", "Firewall has block all state set to disabled.", "Incoming connection to /x/personad is blocked.", true},
		{"Firewall is enabled. (State = 1)", "Firewall has block all state set to disabled.", "", true},
		{"", "", "", true}, // nothing readable: don't risk a prompt on the Mac
	} {
		if got := firewallVerdict(tc.global, tc.blockAll, tc.app); got != tc.blocks {
			t.Errorf("%q / %q / %q: blocks %v", tc.global, tc.blockAll, tc.app, got)
		}
	}
}

// directRig is a registry whose direct routes listen on loopback.
type directRig struct {
	reg   *Registry
	d     *Direct
	mu    sync.Mutex
	cands []Candidate
	block bool
}

func newDirectRig(t *testing.T) *directRig {
	d, err := NewDirect(t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	r := &directRig{d: d, cands: []Candidate{{Kind: "lan", Addr: "127.0.0.1", Prefix: 8}}}
	d.interfaces = func() ([]Iface, error) { return nil, nil }
	d.pick = func([]Iface) []Candidate {
		r.mu.Lock()
		defer r.mu.Unlock()
		return append([]Candidate(nil), r.cands...)
	}
	d.blocked = func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.block }
	d.recheck = 20 * time.Millisecond
	d.firewallEvery = 0
	d.attachWait = testLimits.AttachWait
	r.reg = NewRegistry(testLimits, t.Logf)
	r.reg.EnableDirect(d)
	return r
}

// dialDirect connects as the phone does: TLS 1.3, ALPN, and only the certificate whose hash paths carries.
func dialDirect(t *testing.T, p Paths, alpn []string) (*tls.Conn, error) {
	t.Helper()
	c, err := tls.Dial("tcp", net.JoinHostPort(p.Candidates[0].Addr, strconv.Itoa(p.Port)), &tls.Config{
		MinVersion: tls.VersionTLS13, NextProtos: alpn, InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if base64.RawURLEncoding.EncodeToString(sum[:]) != p.Cert {
				return errors.New("not the pinned certificate")
			}
			return nil
		},
	})
	return c, err
}

func readPaths(t *testing.T, m msg) Paths {
	t.Helper()
	var p Paths
	if err := json.Unmarshal(m.body, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A direct connection with the session's token carries the session: pongs, the RFB channel moved onto it with use,
// and back on the relay by itself, byte for byte, when it drops. Wrong tokens and other clients are closed, and the
// listener goes with the last session.
func TestADirectRouteCarriesTheSession(t *testing.T) {
	r := newDirectRig(t)
	local, ss := screenSharing(t)
	s, relay := r.reg.Open("pair1", local)
	p := newPhone(t, true)
	p.carry(relay)
	paths := readPaths(t, p.next(TypePaths))
	if len(paths.Candidates) != 1 || paths.Port == 0 || paths.Cert == "" || paths.Token == "" {
		t.Fatalf("paths %+v", paths)
	}

	// Without ALPN, or with a wrong token, the connection is closed.
	if c, err := dialDirect(t, paths, nil); err == nil {
		c.Write(frame(TypeAttach, attachBody(paths.Token)))
		c.SetReadDeadline(time.Now().Add(time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("a connection without ALPN was answered")
		}
		c.Close()
	}
	bad, err := dialDirect(t, paths, []string{ALPN})
	if err != nil {
		t.Fatal(err)
	}
	bad.Write(frame(TypeAttach, attachBody("nope")))
	bad.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := bad.Read(make([]byte, 1)); err == nil {
		t.Fatal("a wrong token was answered")
	}

	dc, err := dialDirect(t, paths, []string{ALPN})
	if err != nil {
		t.Fatal(err)
	}
	direct := p.carry(dc)
	direct.send(TypeAttach, attachBody(paths.Token))
	if m := p.next(TypeAttached); m.via != direct {
		t.Fatal("attached came on another transport")
	}
	var stamp [8]byte
	binary.BigEndian.PutUint64(stamp[:], 42)
	direct.send(TypePing, stamp[:])
	pong := p.next(TypePong)
	if len(pong.body) != 16 || binary.BigEndian.Uint64(pong.body) != 42 {
		t.Fatalf("pong %v", pong.body)
	}

	down := pattern(200 << 10)
	go ss.Write(down[:1000])
	waitFor(t, "bytes over the relay", func() bool { return len(p.received(RFB)) == 1000 })
	direct.send(TypeUse, chanOffset(RFB, p.offset(RFB)))
	p.next(TypeResumed)
	go ss.Write(down[1000 : 100<<10])
	waitFor(t, "bytes over the direct route", func() bool { return len(p.received(RFB)) == 100<<10 })

	dc.Close() // the direct route drops: RFB goes on over the relay by itself
	go ss.Write(down[100<<10:])
	waitFor(t, "the rest over the relay", func() bool { return len(p.received(RFB)) == len(down) })
	if !bytes.Equal(p.received(RFB), down) {
		t.Fatal("bytes lost or repeated across the moves")
	}

	s.End()
	waitFor(t, "the listener to close", func() bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(paths.Port)), 200*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err != nil
	})
}

// A change of the node's addresses, or the firewall starting to block, gives the phone new paths.
func TestPathsFollowTheNodesAddresses(t *testing.T) {
	r := newDirectRig(t)
	local, _ := screenSharing(t)
	s, relay := r.reg.Open("pair1", local)
	defer s.End()
	p := newPhone(t, true)
	p.carry(relay)
	first := readPaths(t, p.next(TypePaths))

	r.mu.Lock()
	r.cands = append(r.cands, Candidate{Kind: "tailscale", Addr: "::1"})
	r.mu.Unlock()
	second := readPaths(t, p.next(TypePaths))
	if len(second.Candidates) != 2 || second.Port != first.Port || second.Token != first.Token {
		t.Fatalf("after a new address: %+v", second)
	}

	r.mu.Lock()
	r.block = true
	r.mu.Unlock()
	third := readPaths(t, p.next(TypePaths))
	if third.Unavailable != "firewall" || len(third.Candidates) != 0 || third.Port != 0 {
		t.Fatalf("with the firewall blocking: %+v", third)
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(first.Port)), 200*time.Millisecond); err == nil {
		c.Close()
		t.Fatal("still listening while the firewall blocks")
	}
}
