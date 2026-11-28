package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
)

// errNotForSettings: an address in an extension's settings that leads to this machine, a link-local or metadata
// address, or the hub's own listener (#5906).
var errNotForSettings = errors.New("is this machine, a link-local or metadata address, or the hub itself")

// metadataIPs are cloud metadata services outside the link-local range (the usual one is link-local already).
var metadataIPs = []string{"fd00:ec2::254", "100.100.100.200"}

// dialConfigured reaches an address an extension's settings name: resolved here, every address checked, and the one
// dialled is the one checked (a name that resolves again to somewhere else is not followed). Through the hub's
// upstream proxy when it has one, after the same check, each attempt a CONNECT to one checked address: the proxy
// never sees the name, so it cannot resolve it again to an address the check refused (#3121).
func (p *Proxy) dialConfigured(ctx context.Context, host, port string) (net.Conn, error) {
	ips, err := p.resolveAll(ctx, host)
	if err != nil {
		return nil, err
	}
	var ok []net.IP
	for _, ip := range ips {
		if !p.forSettings(ip, port) {
			return nil, fmt.Errorf("%s (%s) %w", host, ip, errNotForSettings) // one bad answer refuses the name
		}
		ok = append(ok, ip)
	}
	if len(ok) == 0 {
		return nil, fmt.Errorf("%s: no address", host)
	}
	target := net.JoinHostPort(host, port)
	upstream := p.Upstream
	if upstream == nil {
		upstream = fromEnvironment
	}
	up, err := upstream(&url.URL{Scheme: "https", Host: target})
	if err != nil {
		return nil, err
	}
	if up != nil {
		return dialRace(ctx, ok, port, func(ctx context.Context, target string) (net.Conn, error) { return p.connectVia(ctx, up, target) })
	}
	return p.dialResolved(ctx, host, port, ok)
}

// resolveAll is host's addresses (itself, for an IP).
func (p *Proxy) resolveAll(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	if p.Resolve != nil {
		return p.Resolve(ctx, host)
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		ips = append(ips, a.IP)
	}
	return ips, err
}

// Unreachable says whether ip is one no extension is let reach by an address its settings name or its owner allowed:
// loopback, link-local, unspecified/0.0.0.0/8, multicast, limited broadcast or a metadata service. Private LAN addresses are reachable.
func Unreachable(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.IPv4bcast) {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 0 {
		return true
	}
	return slices.ContainsFunc(metadataIPs, func(m string) bool { return net.ParseIP(m).Equal(ip) })
}

// forSettings says whether an address from an extension's settings (or one its owner allowed) may be dialled at port:
// not Unreachable, and not this machine's own address at the hub's or this proxy's port.
func (p *Proxy) forSettings(ip net.IP, port string) bool {
	if Unreachable(ip) {
		return false
	}
	if !p.hubPort(port) && !p.ownPort(port) {
		return true
	}
	var addrs []net.IP
	if p.LocalAddrs != nil {
		addrs = p.LocalAddrs()
	} else {
		var err error
		addrs, err = machineAddrs()
		if err != nil {
			return false
		}
	}
	return !slices.ContainsFunc(addrs, ip.Equal)
}

// machineAddrs are this machine's own addresses.
func machineAddrs() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			out = append(out, n.IP)
		}
	}
	return out, nil
}
