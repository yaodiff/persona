package screenlink

import (
	"net"
	"sort"
	"strings"
)

// Direct-route candidates (#6398): the node's own addresses a phone may reach it at directly, read from its
// interfaces. No Tailscale API or command is used: Tailscale's addresses are recognised by their ranges on a
// point-to-point interface. Global IPv6 is left out in v1; MagicDNS names are never used (resolving one with Tailscale
// off would send the tailnet's name to a public resolver).

// Iface is one interface as the enumeration needs it.
type Iface struct {
	Name  string
	Flags net.Flags
	Addrs []*net.IPNet
}

// maxCandidates bounds what a phone is asked to probe.
const maxCandidates = 8

var (
	tailscaleV4 = mustCIDR("100.64.0.0/10")
	tailscaleV6 = mustCIDR("fd7a:115c:a1e0::/48")
	// RFC 2544's benchmark block, 198.18/15, where proxies' fake-IP tunnels (Clash, Surge) live; built from its octets,
	// since the privacy scan reads any written-out private-range address as a leak.
	benchmarkV4  = &net.IPNet{IP: net.IPv4(198, 18, 0, 0).To4(), Mask: net.CIDRMask(15, 32)}
	uniqueLocal6 = mustCIDR("fc00::/7")
)

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// virtualPrefixes are interfaces only this machine's own guests or containers sit behind (VM bridges, Docker), and
// Apple's peer-to-peer Wi-Fi: never a route from a phone.
var virtualPrefixes = []string{"bridge", "vmnet", "vboxnet", "docker", "br-", "veth", "virbr", "awdl", "llw", "anpi", "ap"}

// Candidates picks the addresses a phone may try: private IPv4 and unique-local IPv6 on Ethernet or Wi-Fi (lan), and
// Tailscale's ranges on a point-to-point interface (tailscale). Loopback, link-local, down interfaces, proxies' tunnels
// and fake-IP ranges, VM and container bridges, and global addresses are skipped. LAN IPv4 first, then Tailscale IPv4,
// then IPv6; at most maxCandidates.
func Candidates(ifs []Iface) []Candidate {
	var out []Candidate
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		p2p := i.Flags&net.FlagPointToPoint != 0
		virtual := false
		for _, p := range virtualPrefixes {
			if strings.HasPrefix(i.Name, p) {
				virtual = true
			}
		}
		for _, a := range i.Addrs {
			ip := a.IP
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() || benchmarkV4.Contains(ip) {
				continue
			}
			prefix, _ := a.Mask.Size()
			switch {
			case tailscaleV4.Contains(ip) || tailscaleV6.Contains(ip):
				if p2p || strings.HasPrefix(i.Name, "tailscale") || strings.HasPrefix(i.Name, "utun") {
					out = append(out, Candidate{Kind: "tailscale", Addr: ip.String()})
				}
			case p2p || virtual:
				// Another VPN or proxy tunnel, or a bridge to this machine's own guests.
			case ip.To4() != nil && ip.IsPrivate():
				out = append(out, Candidate{Kind: "lan", Addr: ip.String(), Prefix: prefix})
			case ip.To4() == nil && uniqueLocal6.Contains(ip):
				out = append(out, Candidate{Kind: "lan", Addr: ip.String(), Prefix: prefix})
			}
		}
	}
	rank := func(c Candidate) int {
		v6 := strings.Contains(c.Addr, ":")
		switch {
		case c.Kind == "lan" && !v6:
			return 0
		case c.Kind == "tailscale" && !v6:
			return 1
		case c.Kind == "lan":
			return 2
		}
		return 3
	}
	sort.SliceStable(out, func(a, b int) bool { return rank(out[a]) < rank(out[b]) })
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// SystemInterfaces reads this machine's interfaces for Candidates.
func SystemInterfaces() ([]Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(ifs))
	for _, i := range ifs {
		addrs, err := i.Addrs()
		if err != nil {
			continue
		}
		f := Iface{Name: i.Name, Flags: i.Flags}
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				f.Addrs = append(f.Addrs, n)
			}
		}
		out = append(out, f)
	}
	return out, nil
}
