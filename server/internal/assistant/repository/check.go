package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
)

const (
	CheckTimeout = 8 * time.Second
	MaxBody      = 256 << 10
)

// TrustedTarget is operator configuration, not a model/API supplied permission.
// Only private unicast addresses inside these networks can be opted in.
type TrustedTarget struct {
	InstanceURL string   `json:"instance_url"`
	Networks    []string `json:"networks"`
}

type CheckError struct{ Code string }

func (e *CheckError) Error() string { return "repository verification: " + e.Code }

type Checker struct {
	TrustedTargets []TrustedTarget
	GitHubProxy    string // manager.json's github.proxy; other targets use the process environment
	// Unexported seams keep production callers on the guarded transport.
	transport http.RoundTripper
	resolve   func(context.Context, string) ([]net.IPAddr, error)
	dial      func(context.Context, string, string) (net.Conn, error)
}

func (c Checker) allowed(id Identity, ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	// Azure's platform virtual IP is globally numbered, but is not a repository.
	if slices.Contains([]string{"168.63.129.16", "fd00:ec2::254", "fd20:ce::254"}, a.String()) {
		return false
	}
	// IETF special-purpose IPv6 assignments are not ordinary repository targets.
	if netip.MustParsePrefix("2001::/23").Contains(a) {
		return false
	}
	switch netaddr.Classify(a) {
	case netaddr.PublicIPv4, netaddr.GlobalIPv6:
		return true
	case netaddr.PrivateIPv4, netaddr.ULA:
		for _, target := range c.TrustedTargets {
			if target.InstanceURL != id.InstanceURL {
				continue
			}
			for _, network := range target.Networks {
				p, err := netip.ParsePrefix(network)
				if err == nil && privateNetwork(p) && p.Contains(a) {
					return true
				}
			}
		}
	}
	return false
}

func privateNetwork(p netip.Prefix) bool {
	for _, b := range []netip.Prefix{
		netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 0, 0, 0}), 8),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{172, 16, 0, 0}), 12),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, 0, 0}), 16),
		netip.MustParsePrefix("fc00::/7"),
	} {
		if p.Bits() >= b.Bits() && b.Contains(p.Masked().Addr()) {
			return true
		}
	}
	return false
}

// Check succeeds only for a bounded provider metadata response identifying this
// exact remote. No permissions/identity endpoint or source-content endpoint runs.
func (c Checker) Check(ctx context.Context, id Identity) error {
	parsed, err := Parse(id.CanonicalURL, id.Provider)
	if err != nil || parsed != id {
		return &CheckError{"inconclusive"}
	}
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()
	endpoint := id.InstanceURL + "/api/v3/repos/" + id.Path
	if id.Provider == "gitlab" {
		endpoint = id.InstanceURL + "/api/v4/projects/" + url.PathEscape(id.Path)
	} else if id.InstanceURL == "https://github.com" {
		endpoint = "https://api.github.com/repos/" + id.Path
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return &CheckError{"inconclusive"}
	}
	tr := c.transport
	if tr == nil {
		upstream, err := c.proxyFor(req)
		if err != nil {
			return checkFailure(ctx, err)
		}
		transport := &http.Transport{
			// Proxying happens after destination validation, using numeric targets.
			Proxy: nil, DisableKeepAlives: true, DisableCompression: true,
			TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second,
			MaxResponseHeaderBytes: 16 << 10,
		}
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, &CheckError{"unsafe_target"}
			}
			resolve := c.resolve
			if resolve == nil {
				resolve = net.DefaultResolver.LookupIPAddr
			}
			ips, err := resolve(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 || len(ips) > 64 {
				return nil, &CheckError{"unsafe_target"}
			}
			// Fail closed for mixed answers. Dial numeric addresses only, keeping
			// TLS verification and HTTP Host pinned to the original API hostname.
			for _, ip := range ips {
				if ip.Zone != "" || !c.allowed(id, ip.IP) {
					return nil, &CheckError{"unsafe_target"}
				}
			}
			dial := c.dial
			if dial == nil {
				dial = (&net.Dialer{Timeout: 3 * time.Second}).DialContext
			}
			return dialPinned(ctx, network, port, ips, func(ctx context.Context, network, target string) (net.Conn, error) {
				return dialThroughProxy(ctx, upstream, network, target, dial)
			})
		}
		defer transport.CloseIdleConnections()
		tr = transport
	}
	client := &http.Client{Transport: tr, Timeout: CheckTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Persona-repository-verification")
	res, err := client.Do(req)
	if err != nil {
		return checkFailure(ctx, err)
	}
	defer res.Body.Close()
	if ctx.Err() != nil {
		return checkFailure(ctx, ctx.Err())
	}
	if res.StatusCode == 429 || res.StatusCode == 403 && (res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "") {
		return &CheckError{"rate_limited"}
	}
	switch res.StatusCode {
	case 401, 403:
		return &CheckError{"authentication"}
	case 404:
		return &CheckError{"unavailable"} // hidden/private is indistinguishable from missing
	case 200:
	default:
		if res.StatusCode >= 300 && res.StatusCode < 400 {
			return &CheckError{"inconclusive"}
		}
		return &CheckError{"network"}
	}
	media, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if !slices.Contains([]string{"application/json", "application/vnd.github+json"}, media) {
		return &CheckError{"inconclusive"}
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, MaxBody+1))
	if err != nil {
		return checkFailure(ctx, err)
	}
	if len(raw) > MaxBody {
		return &CheckError{"inconclusive"}
	}
	var metadata struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Path     string `json:"path_with_namespace"`
		HTMLURL  string `json:"html_url"`
		WebURL   string `json:"web_url"`
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.ID <= 0 {
		return &CheckError{"inconclusive"}
	}
	path, web := metadata.FullName, metadata.HTMLURL
	if id.Provider == "gitlab" {
		path, web = metadata.Path, metadata.WebURL
	}
	remote, err := Parse(web, id.Provider)
	if err != nil || remote != id || !strings.EqualFold(path, id.Path) {
		return &CheckError{"inconclusive"}
	}
	if ctx.Err() != nil {
		return checkFailure(ctx, ctx.Err())
	}
	return nil
}

func checkFailure(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return &CheckError{"canceled"}
	}
	var check *CheckError
	if errors.As(err, &check) {
		return check
	}
	var timeout net.Error
	if ctx.Err() != nil || errors.As(err, &timeout) && timeout.Timeout() {
		return &CheckError{"timeout"}
	}
	return &CheckError{"network"}
}

// Every address has already passed the destination policy. Stagger bounded
// numeric dials so a black-holed address cannot consume the whole check. There
// is no second destination DNS lookup, request retry or change to TLS verification.
func dialPinned(parent context.Context, network, port string, ips []net.IPAddr, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	next, active := 0, 0
	start := func() {
		target := net.JoinHostPort(ips[next].IP.String(), port)
		next++
		active++
		go func() {
			conn, err := dial(ctx, network, target)
			select {
			case results <- result{conn, err}:
			case <-ctx.Done():
				if conn != nil {
					_ = conn.Close()
				}
			}
		}()
	}
	start()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case r := <-results:
			active--
			if r.err == nil && r.conn != nil {
				return r.conn, nil
			}
			if r.conn != nil {
				_ = r.conn.Close()
			}
			if next == len(ips) && active == 0 {
				return nil, r.err
			}
			if next < len(ips) {
				start()
			}
		case <-ticker.C:
			if next < len(ips) && active < 4 {
				start()
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
