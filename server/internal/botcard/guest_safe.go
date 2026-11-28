package botcard

import (
	"net"
	"net/url"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// GuestSafeText is text as it may be shown to people who are not the owner: local paths and links into the owner's
// hub are replaced, Lark's mention tags (<at user_id="all">) are taken out so a guest cannot make the bot ping the
// group, control characters dropped. Idempotent.
func GuestSafeText(text string, privateHosts []string) string {
	if text == "" {
		return ""
	}
	text = guestAt.ReplaceAllString(text, "")
	// Links first, each kept whole or replaced whole: a path inside a public link is part of that link.
	var b strings.Builder
	last := 0
	for _, m := range guestURL.FindAllStringIndex(text, -1) {
		b.WriteString(guestPath.ReplaceAllString(text[last:m[0]], "[path]"))
		if u := text[m[0]:m[1]]; guestURLPrivate(u, privateHosts) {
			b.WriteString("[link]")
		} else {
			b.WriteString(u)
		}
		last = m[1]
	}
	b.WriteString(guestPath.ReplaceAllString(text[last:], "[path]"))
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, b.String())
}

// GuestSafeToolName is a tool's name as a guest may see it: lower-case letters, digits and "_", at most 40.
func GuestSafeToolName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if b.Len() >= 40 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			continue // anything else is dropped, never shown
		default:
			continue
		}
	}
	out := b.String()
	if out == "" {
		return "tool"
	}
	return out
}

// guestPath matches an absolute path under a home or temp root, up to the next whitespace or closing bracket or quote.
var guestPath = lazyre.New(`(?:~[/\\]|(?:[A-Za-z]:)?(?:\\|/)+(?:Users|home|root|private|var[/\\]+folders|tmp|opt|etc|Volumes)[/\\])[^\s"'\])>]*`)

// guestAt matches a Lark mention tag, opening or closing.
var guestAt = lazyre.New(`(?i)<\s*/?\s*at\b[^>]*>`)

// guestURL matches a URL with a scheme. Its host is checked afterwards: only the owner's own are replaced.
var guestURL = lazyre.New(`[A-Za-z][A-Za-z0-9+.-]*://[^\s"'\])>]*`)

// guestURLPrivate says whether the URL is one a guest must not see: the owner's hub, a machine on the local network,
// or the app's own deep link.
func guestURLPrivate(raw string, privateHosts []string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" && u.Scheme == "" {
		return false
	}
	if compat.IsAppScheme(strings.ToLower(u.Scheme)) {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return true // file:///Users/…: a link to no host is a link into a machine
	}
	for _, h := range privateHosts {
		if strings.EqualFold(host, hostOnly(h)) {
			return true
		}
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".ts.net") || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || guestCGNAT.Contains(ip)
}

// guestCGNAT is the shared address space (100.64.0.0/10) mesh VPNs hand out to machines.
var guestCGNAT = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// hostOnly is a host with its port taken off, as the hub's public URL may carry one.
func hostOnly(h string) string {
	if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	host, _, err := net.SplitHostPort(h)
	if err != nil {
		return h
	}
	return host
}
