package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Office network mode (security.private_network_only, docs/operations/network.md "Office network mode"): the hub,
// its computers and its phones work only inside the organization's own network, phones joining it over the
// organization's VPN. The switch refuses a start with any setting that reaches past that network, refuses every request
// from a public source whatever the firewall lets through, ignores the build's own update source, pushes only with the
// hub's own APNs key and sealed to each phone, and tells the computers' nodes to stop tailcat.

// privateNetworkOnly answers 403 private_network_only to a request from a public source. It is the outermost route
// layer, before the sharing dispatcher and the plaintext rules: no route, health and pairing included, ever answers
// the internet in this mode. A request a mesh member passed on carries the source the member judged (forwardedOf).
func (h *Hub) privateNetworkOnly(next http.Handler) http.Handler {
	if !h.cfg.Security.PrivateNetworkOnly {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.sourceOf(r) == sourcePublic {
			h.securityError(w, r, http.StatusForbidden, codePrivateNetworkOnly, 0)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// codePrivateNetworkOnly is the error code of a request privateNetworkOnly refused.
const codePrivateNetworkOnly = "private_network_only"

// errPrivateNetworkConflicts heads checkPrivateNetworkOnly's error.
var errPrivateNetworkConflicts = errors.New("security.private_network_only is on, and hub.json reaches past the private network")

// checkPrivateNetworkOnly is the start's check of the switch against the rest of hub.json: every setting that would
// reach the internet is named in one error, and the hub does not start until they are gone.
func (c *Config) checkPrivateNetworkOnly() error {
	if !c.Security.PrivateNetworkOnly {
		return nil
	}
	var conflicts []string
	if c.Security.AllowPublicPlaintext {
		conflicts = append(conflicts, "security.allow_public_plaintext: remove it")
	}
	if c.Public.DDNS != nil {
		conflicts = append(conflicts, "public.ddns publishes the hub's address in public DNS: remove it")
	}
	if c.Public.AdvertiseIPv6 != nil && *c.Public.AdvertiseIPv6 {
		conflicts = append(conflicts, "public.advertise_ipv6 hands out global IPv6 addresses: remove it or set it to false")
	}
	for _, raw := range slices.Concat([]string{c.PublicURL}, c.Public.Advertise, c.Public.LinkAdvertise) {
		if ip, ok := publicAddressIn(raw); ok {
			conflicts = append(conflicts, fmt.Sprintf("%q names the public address %s: use a private address or an internal name", raw, ip))
		}
	}
	if c.Sharing != nil && strings.TrimSpace(c.Sharing.PublicOrigin) != "" {
		conflicts = append(conflicts, "sharing.public_origin publishes shared sessions to viewers without credentials: remove it")
	}
	if c.Update.Development != nil {
		conflicts = append(conflicts, "update.development fetches from github.com: remove it")
	}
	for _, m := range c.Machines {
		switch {
		case m.Kind == "tailcat":
			conflicts = append(conflicts, fmt.Sprintf("machines %q is kind tailcat, which relays over the internet: use kind service or link", m.ID))
		case strings.TrimSpace(m.TailcatAddr) != "":
			conflicts = append(conflicts, fmt.Sprintf("machines %q has tailcat_addr, which relays over the internet: remove it", m.ID))
		}
	}
	if len(conflicts) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n- %s", errPrivateNetworkConflicts, strings.Join(conflicts, "\n- "))
}

// publicAddressIn is the IP literal in a URL, a host or a host:port when it is a public one (neither loopback,
// private, link-local nor shared carrier space). Names are not resolved: the organization's DNS answers for them.
func publicAddressIn(raw string) (netip.Addr, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, false
	}
	host := raw
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return netip.Addr{}, false
		}
		host = u.Hostname()
	} else if h, _, err := net.SplitHostPort(raw); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if err != nil {
		return netip.Addr{}, false
	}
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || sharedCarrier.Contains(ip) {
		return netip.Addr{}, false
	}
	return ip, true
}

// sharedCarrier is 100.64.0.0/10 (RFC 6598), the range many VPNs hand out.
var sharedCarrier = netip.MustParsePrefix("100.64.0.0/10")

// errNoPushKey: office network mode has nothing to send a phone without a push key (an older app) but a content-free
// alert; a background push carries data only, so it is not sent at all.
var errNoPushKey = errors.New("office network mode: the phone registered no push key, so a background push is not sent")

// sealedDirect is an office network mode push for APNs (Pusher.privateOnly): the envelope the relay builds
// (relay.Server.envelope), a fallback alert in the phone's language that its notification service extension replaces
// with the payload sealed to the phone's key in "e". APNs and Apple see neither title, body, category nor thread.
// A phone with no key gets the fallback alone, and no background push.
func (p *Pusher) sealedDirect(d Device, pushType string, payload map[string]any) ([]byte, error) {
	var sealed string
	if d.PushKey != "" {
		s, err := p.sealFor(d, payload)
		if err != nil {
			return nil, fmt.Errorf("seal: %w", err)
		}
		sealed = s
	}
	var out map[string]any
	switch pushType {
	case "background":
		if sealed == "" {
			return nil, errNoPushKey
		}
		out = map[string]any{"aps": map[string]any{"content-available": 1}}
	default:
		l := p.langOf(d)
		out = map[string]any{"aps": map[string]any{
			"alert":           map[string]any{"title": "Persona", "body": i18n.T(l, "push.sealed_fallback")},
			"mutable-content": 1, "sound": "default",
		}}
	}
	if sealed != "" {
		out["e"] = sealed
	}
	return json.Marshal(out)
}
