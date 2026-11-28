package hub

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/netaddr"
)

// Endpoint is a hub URL, ordered by the preferred connection path.
type Endpoint struct {
	URL    string    `json:"url"`
	Kind   string    `json:"kind"` // ipv6 | name | lan | ipv4
	Port   int       `json:"port"`
	Scheme string    `json:"scheme"`
	SeenAt time.Time `json:"seen_at"`
}

// localAddress and addressSource are the seam for the netaddr lane. This fallback cannot read address lifetimes
// or privacy flags; the replacement must exclude temporary, deprecated, tentative and failed addresses.
type localAddress struct {
	Addr      netip.Addr
	Interface string
	Flags     net.Flags
}

type addressSource func() ([]localAddress, error)

func interfaceAddresses() ([]localAddress, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []localAddress
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || virtualInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return nil, fmt.Errorf("read interface addresses: %w", err)
		}
		for _, raw := range addrs {
			if prefix, err := netip.ParsePrefix(raw.String()); err == nil {
				out = append(out, localAddress{Addr: prefix.Addr().Unmap(), Interface: iface.Name, Flags: iface.Flags})
			}
		}
	}
	return out, nil
}

func virtualInterface(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"lo", "docker", "veth", "virbr", "br-", "tun", "utun", "tap", "tailscale", "wg", "vpn", "dummy", "vxlan", "cni", "flannel", "podman"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func localAddressKind(a localAddress) string {
	ip := a.Addr.Unmap()
	if a.Flags&net.FlagUp == 0 || a.Flags&net.FlagLoopback != 0 || virtualInterface(a.Interface) || !ip.IsGlobalUnicast() || ip.Zone() != "" {
		return ""
	}
	// Documentation and transition prefixes are not usable public candidates.
	for _, prefix := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "2001:db8::/32", "3fff::/20", "2002::/16", "2001::/32"} {
		if netip.MustParsePrefix(prefix).Contains(ip) {
			return ""
		}
	}
	if ip.IsPrivate() {
		return "lan"
	}
	if ip.Is6() && netip.MustParsePrefix("2000::/3").Contains(ip) {
		return "ipv6"
	}
	// Public IPv4 is only published when it was explicitly configured.
	return ""
}

func endpointRank(kind string) int {
	return map[string]int{"ipv6": 0, "name": 1, "lan": 2, "ipv4": 3}[kind]
}

func configuredEndpoint(raw, scheme string, port int, now time.Time) (Endpoint, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Endpoint{}, false
	}
	if !strings.Contains(raw, "://") {
		if strings.ContainsAny(raw, "/?#@") {
			return Endpoint{}, false
		}
		host := raw
		if ip, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
			host = net.JoinHostPort(ip.String(), strconv.Itoa(port))
		} else if !strings.Contains(raw, ":") {
			host = net.JoinHostPort(raw, strconv.Itoa(port))
		}
		raw = scheme + "://" + host
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return Endpoint{}, false
	}
	port = 80
	if u.Scheme == "https" {
		port = 443
	}
	if u.Port() != "" {
		port, err = strconv.Atoi(u.Port())
	}
	if err != nil || port < 1 || port > 65535 || strings.HasSuffix(u.Host, ":") {
		return Endpoint{}, false
	}
	kind, host := "name", strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil {
		ip = ip.Unmap()
		if !ip.IsGlobalUnicast() || ip.Zone() != "" {
			return Endpoint{}, false
		}
		host = ip.String()
		switch {
		case ip.IsPrivate():
			kind = "lan"
		case ip.Is6():
			kind = "ipv6"
		default:
			kind = "ipv4"
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	u.Path = strings.TrimRight(u.Path, "/")
	return Endpoint{URL: u.String(), Kind: kind, Scheme: u.Scheme, Port: port, SeenAt: now}, true
}

func collectEndpoints(cfg *Config, tlsPort int, addrs []localAddress, now time.Time) []Endpoint {
	scheme, port, listen := "http", 0, cfg.Listen
	if tlsPort != 0 {
		scheme, port, listen = "https", tlsPort, cfg.Public.TLSListen
	} else if _, rawPort, err := net.SplitHostPort(cfg.Listen); err == nil {
		port, _ = strconv.Atoi(rawPort)
	}
	boundHost, _, _ := net.SplitHostPort(listen)
	bound, _ := netip.ParseAddr(boundHost)
	out := []Endpoint{}
	seen := map[string]bool{}
	appendEndpoint := func(e Endpoint) {
		if !seen[e.URL] {
			seen[e.URL] = true
			out = append(out, e)
		}
	}
	for _, raw := range append([]string{cfg.PublicURL}, cfg.Public.Advertise...) {
		if e, ok := configuredEndpoint(raw, scheme, port, now); ok {
			appendEndpoint(e)
		}
	}
	for _, a := range addrs {
		kind := localAddressKind(a)
		if kind == "" || port < 1 || port > 65535 || (bound.Is4() && a.Addr.Unmap().Is6()) ||
			(bound.IsValid() && !bound.IsUnspecified() && bound.Unmap() != a.Addr.Unmap()) {
			continue
		}
		// Office network mode hands out no global IPv6 address unless asked to (the start refuses one asked for).
		if (kind == "ipv6" && ((cfg.Public.AdvertiseIPv6 != nil && !*cfg.Public.AdvertiseIPv6) || (cfg.Public.AdvertiseIPv6 == nil && cfg.Security.PrivateNetworkOnly))) ||
			(kind == "lan" && cfg.Public.AdvertiseLAN != nil && !*cfg.Public.AdvertiseLAN) {
			continue
		}
		appendEndpoint(Endpoint{URL: scheme + "://" + net.JoinHostPort(a.Addr.Unmap().String(), strconv.Itoa(port)), Kind: kind, Port: port, Scheme: scheme, SeenAt: now})
	}
	slices.SortFunc(out, func(a, b Endpoint) int {
		if rank := endpointRank(a.Kind) - endpointRank(b.Kind); rank != 0 {
			return rank
		}
		if a.Scheme != b.Scheme {
			if a.Scheme == "https" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.URL, b.URL)
	})
	return out
}

func (h *Hub) endpointList() []Endpoint {
	h.public.mu.RLock()
	defer h.public.mu.RUnlock()
	return append([]Endpoint{}, h.public.endpoints...)
}

func (h *Hub) linkEndpointList() []Endpoint {
	if len(h.cfg.Public.LinkAdvertise) == 0 {
		return h.endpointList()
	}
	port, _ := h.tlsPin()
	if port == 0 {
		port = 443
	}
	var endpoints []Endpoint
	seen := make(map[string]bool)
	for _, raw := range h.cfg.Public.LinkAdvertise {
		if e, ok := configuredEndpoint(raw, "https", port, time.Time{}); ok && e.Scheme == "https" && !seen[e.URL] {
			seen[e.URL] = true
			endpoints = append(endpoints, e)
		}
	}
	return endpoints
}

func (h *Hub) localAddresses() ([]localAddress, error) {
	scan := h.public.addresses
	if scan == nil {
		scan = kernelAddresses
	}
	return scan()
}

// kernelAddresses lists this machine's addresses through netaddr. On Linux it knows the kernel's address flags, so
// temporary (privacy), deprecated, tentative and expired IPv6 addresses never go out (netaddr.Stable). Elsewhere the
// flags are unknown and global IPv6 addresses go out as net.Interfaces lists them, temporary ones included: a phone
// that cannot reach one moves on to the next candidate.
func kernelAddresses() ([]localAddress, error) {
	addrs, err := netaddr.List()
	if err != nil {
		return interfaceAddresses()
	}
	return publishable(addrs), nil
}

func publishable(addrs []netaddr.Addr) []localAddress {
	var out []localAddress
	for _, a := range addrs {
		if a.Virtual || (a.IP.Is6() && a.FlagsKnown && netaddr.Classify(a.IP) == netaddr.GlobalIPv6 && !netaddr.Stable(a)) {
			continue
		}
		out = append(out, localAddress{Addr: a.IP, Interface: a.Interface, Flags: a.InterfaceFlags})
	}
	return out
}

func (h *Hub) refreshEndpoints(now time.Time) error {
	h.public.refreshMu.Lock()
	defer h.public.refreshMu.Unlock()
	addrs, err := h.localAddresses()
	if err != nil {
		return err
	}
	port, _ := h.tlsPin()
	endpoints := collectEndpoints(h.cfg, port, addrs, now)
	h.public.mu.Lock()
	changed := !slices.EqualFunc(h.public.endpoints, endpoints, func(a, b Endpoint) bool {
		a.SeenAt, b.SeenAt = time.Time{}, time.Time{}
		return a == b
	})
	h.public.endpoints = endpoints
	h.public.mu.Unlock()
	h.updateDDNS()
	if changed {
		h.sendEndpoints(endpoints)
		h.sendLinkEndpoints()
	}
	return nil
}

func (h *Hub) sendEndpoints(endpoints []Endpoint) {
	b, _ := json.Marshal(map[string]any{"type": "endpoints", "endpoints": endpoints})
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.has("endpoints") {
			select {
			case c.out <- b:
			default:
			}
		}
	}
}

func (h *Hub) serveEndpoints(w http.ResponseWriter, r *http.Request) {
	port, fp := h.tlsPin()
	writeJSON(w, map[string]any{"hub_id": h.hubID(), "tls_port": port, "fp": fp, "endpoints": h.endpointList()})
}
