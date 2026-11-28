package hub

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestTCPNetworkForListenHost(t *testing.T) {
	for _, tc := range []struct {
		host, want string
	}{
		{"0.0.0.0", "tcp4"},
		{"127.0.0.1", "tcp4"},
		{"192.0.2.1", "tcp4"},
		{"", "tcp"},
		{"::", "tcp"},
		{"::1", "tcp"},
		{"2001:db8::1", "tcp"},
		{"fe80::1%en0", "tcp"},
		{"example.com", "tcp"},
	} {
		if got := tcpNetworkForListenHost(tc.host); got != tc.want {
			t.Fatalf("tcpNetworkForListenHost(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}

// TestListenTCPIPv4WildcardIsIPv4Only binds the address the issue reports: "tcp" made 0.0.0.0 a dual-stack IPv6
// socket even though collectEndpoints published it as IPv4-only.
func TestListenTCPIPv4WildcardIsIPv4Only(t *testing.T) {
	ln, activated, err := listenTCP("0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if activated {
		t.Fatal("listenTCP reported a socket this test did not pass")
	}
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok || addr.IP.To4() == nil {
		t.Fatalf("0.0.0.0 listener is %T %v, want an IPv4 socket", ln.Addr(), ln.Addr())
	}
	port := strconv.Itoa(addr.Port)
	dial := func(network, host string) net.Conn {
		t.Helper()
		conn, err := net.DialTimeout(network, net.JoinHostPort(host, port), 2*time.Second)
		if err != nil {
			t.Fatalf("dial %s %s: %v", network, host, err)
		}
		return conn
	}
	conn := dial("tcp4", "127.0.0.1")
	accepted, err := ln.Accept()
	conn.Close()
	if err != nil {
		t.Fatalf("accept IPv4 connection: %v", err)
	}
	accepted.Close()

	if probe, err := net.Listen("tcp6", "[::1]:0"); err != nil {
		t.Logf("IPv6 loopback is unavailable; skipped the IPv6 reachability check: %v", err)
	} else {
		probe.Close()
		if conn, err := net.DialTimeout("tcp6", net.JoinHostPort("::1", port), 2*time.Second); err == nil {
			conn.Close()
			t.Fatalf("IPv4 wildcard listener accepted IPv6 loopback on port %s", port)
		}
	}

	cfg := &Config{Public: PublicConfig{TLSListen: net.JoinHostPort("0.0.0.0", port)}}
	endpoints := collectEndpoints(cfg, addr.Port, []localAddress{testLocalAddress("lan4"), testLocalAddress("ipv6")}, time.Now())
	wantURL := "https://" + net.JoinHostPort(testLocalAddress("lan4").Addr.String(), port)
	if len(endpoints) != 1 || endpoints[0].Kind != "lan" || endpoints[0].URL != wantURL {
		t.Fatalf("IPv4-only listener published %+v, want only the LAN IPv4 endpoint", endpoints)
	}
}

// TestUnclaimedInherited: a socket systemd passed for a port hub.json no longer listens on stays unclaimed, and the
// hub says so, until something listens on that port.
func TestUnclaimedInherited(t *testing.T) {
	a, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	inherited.once.Do(func() {}) // as if the process had taken LISTEN_FDS already
	inherited.mu.Lock()
	saved := inherited.lns
	inherited.lns = []net.Listener{a, b}
	inherited.mu.Unlock()
	t.Cleanup(func() {
		inherited.mu.Lock()
		inherited.lns = saved
		inherited.mu.Unlock()
	})

	ln, activated, err := listenTCP(a.Addr().String())
	if err != nil || !activated || ln != a {
		t.Fatalf("listenTCP(%s) = %v, %v, %v; want the passed socket", a.Addr(), ln, activated, err)
	}
	got := unclaimedInherited()
	if len(got) != 1 || got[0].String() != b.Addr().String() {
		t.Fatalf("unclaimedInherited() = %v, want [%s]", got, b.Addr())
	}
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	logUnclaimedInherited(logf)
	if len(logs) != 1 || !strings.Contains(logs[0], b.Addr().String()) || !strings.Contains(logs[0], "personad hub setup") {
		t.Fatalf("log = %q, want one line naming %s and the fix", logs, b.Addr())
	}

	if _, activated, err := listenTCP(b.Addr().String()); err != nil || !activated {
		t.Fatalf("listenTCP(%s): %v, %v", b.Addr(), activated, err)
	}
	if got := unclaimedInherited(); len(got) != 0 {
		t.Fatalf("unclaimedInherited() after both ports = %v, want none", got)
	}
	logs = nil
	logUnclaimedInherited(logf)
	if len(logs) != 0 {
		t.Fatalf("log with every socket claimed = %q", logs)
	}
}
