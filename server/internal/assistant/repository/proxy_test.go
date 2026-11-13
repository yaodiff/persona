package repository

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ProxyFromEnvironment caches the environment. Each routing scenario runs in a
// fresh process, and ordinary unit tests never use the developer's proxy.
func TestMain(m *testing.M) {
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "all_proxy", "http_proxy", "https_proxy", "no_proxy"} {
		_ = os.Unsetenv(key)
	}
	os.Exit(m.Run())
}

func TestRepositoryProxySelection(t *testing.T) {
	scenario := os.Getenv("PERSONA_TEST_PROXY_SELECTION")
	if scenario == "" {
		for _, scenario := range []string{"configured", "malformed", "environment", "bypass", "gitlab", "enterprise"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.timeout=15s", "-test.run=^TestRepositoryProxySelection$", "-test.v")
				cmd.Env = append(os.Environ(), "PERSONA_TEST_PROXY_SELECTION="+scenario)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("selection scenario: %v\n%s", err, out)
				}
			})
		}
		return
	}
	t.Setenv("HTTPS_PROXY", "http://environment.example:3128")
	c := Checker{GitHubProxy: "http://configured.example:8080"}
	endpoint, want := "https://api.github.com/repos/example/project", "http://environment.example:3128"
	switch scenario {
	case "configured":
		t.Setenv("NO_PROXY", "*") // explicit configuration wins over the environment's bypass
		want = c.GitHubProxy
	case "malformed":
		c.GitHubProxy = "http://user:private-password@configured.example:invalid"
	case "environment":
		c.GitHubProxy = ""
	case "bypass":
		c.GitHubProxy = ""
		t.Setenv("NO_PROXY", "api.github.com")
		want = ""
	case "gitlab":
		endpoint = "https://gitlab.com/api/v4/projects/example%2Fproject"
	case "enterprise":
		endpoint = "https://code.example.com/api/v3/repos/example/project"
	}
	req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
	upstream, err := c.proxyFor(req)
	if scenario == "malformed" {
		if err == nil || strings.Contains(err.Error(), "private-password") {
			t.Fatalf("malformed proxy silently fell back or exposed credentials: %v", err)
		}
		return
	}
	got := ""
	if upstream != nil {
		got = upstream.String()
	}
	if err != nil || got != want {
		t.Fatalf("selected %q, %v; want %q", got, err, want)
	}
}

func TestSOCKSProxyPinsNumericDestination(t *testing.T) {
	for _, scenario := range []struct{ scheme, ip string }{{"socks5", "93.184.216.34"}, {"socks5h", "2606:4700:4700::1111"}} {
		t.Run(scenario.scheme, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				greeting := make([]byte, 3)
				if _, err = io.ReadFull(conn, greeting); err != nil {
					done <- err
					return
				}
				if string(greeting) != string([]byte{5, 1, 0}) {
					done <- errors.New("unexpected SOCKS greeting")
					return
				}
				_, _ = conn.Write([]byte{5, 0})
				header := make([]byte, 4)
				if _, err = io.ReadFull(conn, header); err != nil {
					done <- err
					return
				}
				size := 4
				if header[3] == 4 {
					size = 16
				} else if header[3] != 1 {
					done <- errors.New("SOCKS proxy received an unpinned hostname")
					return
				}
				address := make([]byte, size+2)
				if _, err = io.ReadFull(conn, address); err != nil {
					done <- err
					return
				}
				if !net.IP(address[:size]).Equal(net.ParseIP(scenario.ip)) || address[size] != 1 || address[size+1] != 187 {
					done <- errors.New("wrong SOCKS destination")
					return
				}
				_, err = conn.Write(append([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}, []byte("tunnel-ready")...))
				done <- err
			}()
			upstream, _ := url.Parse(scenario.scheme + "://" + listener.Addr().String())
			conn, err := dialThroughProxy(t.Context(), upstream, "tcp", net.JoinHostPort(scenario.ip, "443"), (&net.Dialer{}).DialContext)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			payload, err := io.ReadAll(conn)
			if err != nil || string(payload) != "tunnel-ready" {
				t.Fatalf("tunnel payload %q: %v", payload, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestProxyTunnelPreservesBufferedData(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	payload := strings.Repeat("x", 32<<10)
	go func() {
		defer server.Close()
		if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
			return
		}
		_, _ = io.WriteString(server, "HTTP/1.1 200 Connection established\r\n\r\n"+payload)
	}()
	upstream, _ := url.Parse("http://proxy.example:3128")
	conn, err := dialThroughProxy(t.Context(), upstream, "tcp", "93.184.216.34:443", func(context.Context, string, string) (net.Conn, error) { return client, nil })
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got, err := io.ReadAll(conn)
	if err != nil || string(got) != payload {
		t.Fatalf("buffered tunnel data lost: %d bytes, %v", len(got), err)
	}
}

func TestProxyHandshakeRejectsOversizedHeaders(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		defer server.Close()
		if _, err := http.ReadRequest(bufio.NewReader(server)); err != nil {
			return
		}
		_, _ = io.WriteString(server, "HTTP/1.1 200 Connection established\r\nX-Oversized: "+strings.Repeat("x", 32<<10)+"\r\n\r\n")
	}()
	upstream, _ := url.Parse("http://proxy.example:3128")
	conn, err := dialThroughProxy(t.Context(), upstream, "tcp", "93.184.216.34:443", func(context.Context, string, string) (net.Conn, error) { return client, nil })
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("unbounded proxy response accepted")
	}
}

func TestCheckUsesEnvironmentProxy(t *testing.T) {
	if os.Getenv("PERSONA_TEST_REPOSITORY_PROXY") == "" {
		for _, scenario := range []string{"http", "https", "https_proxy", "direct", "no_proxy", "auth", "blocked", "mixed", "refused", "redirect", "cancel", "timeout", "bad_tls"} {
			t.Run(scenario, func(t *testing.T) {
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.timeout=15s", "-test.run=^TestCheckUsesEnvironmentProxy$", "-test.v")
				cmd.Env = append(os.Environ(), "PERSONA_TEST_REPOSITORY_PROXY="+scenario)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("proxy scenario: %v\n%s", err, out)
				}
			})
		}
		return
	}
	scenario := os.Getenv("PERSONA_TEST_REPOSITORY_PROXY")
	host, scheme, port := "code.example.com", "http", "80"
	if scenario == "https" || scenario == "bad_tls" {
		scheme, port = "https", "443"
	}
	if scenario == "bad_tls" {
		host = "code.invalid"
	}
	canonical := scheme + "://" + host + "/example/project"
	var tunneled, reached, direct atomic.Int32
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Host != host || r.URL.Path != "/api/v3/repos/example/project" || r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("origin identity or credentials changed: %s %s", r.Host, r.URL.Path)
		}
		if scenario == "redirect" {
			http.Redirect(w, r, "http://127.0.0.1/metadata", 302)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":1,"full_name":"example/project","html_url":"`+canonical+`"}`)
	}))
	if scheme == "https" {
		origin.StartTLS()
	} else {
		origin.Start()
	}
	defer origin.Close()
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "93.184.216.34:"+port {
			t.Errorf("proxy must receive only the checked numeric destination: %s %s", r.Method, r.Host)
			http.Error(w, "invalid destination", http.StatusBadRequest)
			return
		}
		tunneled.Add(1)
		wantAuth := ""
		if scenario == "auth" {
			wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password"))
		}
		if r.Header.Get("Proxy-Authorization") != wantAuth {
			t.Error("proxy credentials were not confined to CONNECT")
		}
		if scenario == "refused" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if scenario == "cancel" || scenario == "timeout" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			started <- struct{}{}
			_, _ = io.Copy(io.Discard, conn)
			stopped <- struct{}{}
			return
		}
		remote, err := net.Dial("tcp", strings.TrimPrefix(strings.TrimPrefix(origin.URL, "http://"), "https://"))
		if err != nil {
			t.Error(err)
			return
		}
		defer remote.Close()
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		go func() { _, _ = io.Copy(remote, rw); _ = remote.Close() }()
		_, _ = io.Copy(conn, remote)
	}))
	if scenario == "https_proxy" {
		upstream.StartTLS()
	} else {
		upstream.Start()
	}
	defer upstream.Close()
	if scheme == "https" || scenario == "https_proxy" {
		cert := origin.Certificate()
		if scenario == "https_proxy" {
			cert = upstream.Certificate()
		}
		file := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SSL_CERT_FILE", file)
	}
	proxyURL := upstream.URL
	if scenario == "auth" {
		proxyURL = strings.Replace(proxyURL, "://", "://proxy-user:proxy-password@", 1)
	}
	if scenario != "direct" {
		t.Setenv("HTTP_PROXY", proxyURL)
		t.Setenv("HTTPS_PROXY", proxyURL)
	}
	if scenario == "no_proxy" {
		t.Setenv("NO_PROXY", host)
	}
	c := Checker{
		resolve: func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host != "code.example.com" && host != "code.invalid" {
				t.Errorf("resolved proxy as destination: %s", host)
			}
			ips := []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}
			if scenario == "blocked" {
				ips[0].IP = net.ParseIP("127.0.0.1")
			}
			if scenario == "mixed" {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(private4(169, 254, 169, 254))})
			}
			return ips, nil
		},
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == "93.184.216.34:"+port {
				direct.Add(1)
				if scenario != "direct" && scenario != "no_proxy" {
					return nil, errors.New("direct path unavailable")
				}
				addr = strings.TrimPrefix(strings.TrimPrefix(origin.URL, "http://"), "https://")
			} else if addr != strings.TrimPrefix(strings.TrimPrefix(upstream.URL, "http://"), "https://") {
				return nil, errors.New("unexpected dial")
			}
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	id, _ := Parse(canonical, "github")
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if scenario == "cancel" {
		go func() {
			select {
			case <-started:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	if scenario == "timeout" {
		ctx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
	}
	want := map[string]string{"blocked": "unsafe_target", "mixed": "unsafe_target", "refused": "network", "redirect": "inconclusive", "cancel": "canceled", "timeout": "timeout", "bad_tls": "network"}[scenario]
	if err := c.Check(ctx, id); code(err) != want {
		t.Fatalf("check: %v, want %q", err, want)
	}
	if scenario == "cancel" || scenario == "timeout" {
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("canceled proxy handshake leaked a connection")
		}
	}
	wantTunnel, wantOrigin, wantDirect := int32(1), int32(1), int32(0)
	switch scenario {
	case "direct", "no_proxy":
		wantTunnel, wantDirect = 0, 1
	case "blocked", "mixed":
		wantTunnel, wantOrigin = 0, 0
	case "refused", "cancel", "timeout", "bad_tls":
		wantOrigin = 0
	}
	if tunneled.Load() != wantTunnel || reached.Load() != wantOrigin || direct.Load() != wantDirect {
		t.Fatalf("tunnels %d, metadata %d, direct %d; want %d/%d/%d", tunneled.Load(), reached.Load(), direct.Load(), wantTunnel, wantOrigin, wantDirect)
	}
}
