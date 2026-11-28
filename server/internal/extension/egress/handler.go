package egress

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
)

func (p *Proxy) handler(fixed string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ext, chk := fixed, (*check)(nil)
		switch {
		case ext == "":
			ext, chk = p.who(r)
			if ext == "" {
				w.Header().Set("Proxy-Authenticate", `Basic realm="persona"`)
				http.Error(w, "persona: this proxy is for the hub's extensions", http.StatusProxyAuthRequired)
				return
			}
		default:
			chk = p.checkOf(r, fixed) // a sandbox's socket says who it is; a check's credential says which check
			if bound, ok := r.Context().Value(runtimeCheckKey{}).(*check); ok {
				chk = bound
			} else if r.URL.Path == RuntimeFetchPath && chk == nil && credential(r) != "" {
				// A runtime request with no credential is ordinary. A presented credential must name a live
				// check of this extension, never fall back to that ordinary authority.
				http.Error(w, "persona: settings check credential unavailable", http.StatusForbidden)
				return
			}
		}
		ctx, finish, live := p.requestLifetime(r.Context(), ext, chk)
		if !live {
			http.Error(w, "extension registration unavailable", http.StatusForbidden)
			return
		}
		defer finish()
		if r.Method == http.MethodConnect {
			ctx = context.WithValue(ctx, connectDialKey{}, true)
		}
		r = r.WithContext(ctx)
		if fixed != "" && r.URL.Host == "" && r.URL.Path == RuntimeFetchPath {
			p.runtimeFetch(w, r, fixed, chk)
			return
		}
		rule, ok := p.rule(ext)
		if !ok {
			http.Error(w, "persona: this extension is not running", http.StatusForbidden)
			return
		}
		if chk != nil {
			rule.Targets = chk.targets // what the proposed settings name, in place of the saved ones
		}
		broker, _ := r.Context().Value(runtimeFetchKey{}).(bool)
		hostport := r.Host
		if r.Method != http.MethodConnect {
			if r.URL.Host == "" || (r.URL.Scheme != "http" && !(broker && r.URL.Scheme == "https")) {
				http.Error(w, "persona: only http:// requests and CONNECT", http.StatusBadRequest)
				return
			}
			hostport = r.URL.Host
		}
		defaultPort := "80"
		if r.Method == http.MethodConnect || r.URL.Scheme == "https" {
			defaultPort = "443"
		}
		host, port, ok := parseRequestHostPort(hostport, defaultPort)
		if !ok {
			http.Error(w, "persona: invalid host or port", http.StatusBadRequest)
			return
		}
		hub, local := host == HubHost, host == LocalHost
		connect := r.Method == http.MethodConnect || r.URL.Scheme == "https"
		var allowed, configured bool
		loopbackDial := "127.0.0.1"
		switch {
		case hub:
			allowed = rule.Hub
		case local:
			if rule.Loopback() && !p.ownPort(port) {
				allowed = true
			} else if lip, ok := rule.loopbackTarget(port, connect); ok && !p.ownPort(port) && !p.hubPort(port) {
				allowed, loopbackDial = true, lip
			}
		default:
			allowed = Matches(host, rule.Hosts)
			if allowed && (IsLoopbackHost(host) || isSelfIP(host)) && p.ownPort(port) {
				allowed, local = false, true
			}
			if !allowed && !local && rule.target(host, port, connect) {
				if IsLoopbackHost(host) {
					if !p.ownPort(port) && !p.hubPort(port) {
						allowed, local = true, true
						if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
							loopbackDial = "::1"
						}
					}
				} else {
					allowed, configured = true, true
				}
			}
			// A host the owner allowed is reached the way a settings address is (dialConfigured): never this machine
			// by name, and each answer of its name checked (#5697).
			if !allowed && !local && rule.owned(host, port) && !IsLoopbackHost(host) {
				allowed, configured = true, true
			}
		}
		p.count(ext, host, allowed)
		if !allowed {
			if local && (rule.Loopback() || isSelfIP(host)) {
				p.logf("extension %s: refused %s:%s (the proxy itself)", ext, host, port)
				http.Error(w, fmt.Sprintf("persona: %s:%s is this proxy", host, port), http.StatusForbidden)
				return
			}
			// Say it is the sandbox's egress rule, with the port, in the log and in what the program reads: a connector
			// that only reports "unreachable" is then traced to the missing grant (#15707).
			p.logf("extension %s: sandbox egress refused %s:%s (not in its permissions.network, its settings addresses or the hosts its owner allowed)", ext, host, port)
			http.Error(w, fmt.Sprintf("persona sandbox egress: %s:%s is not in this extension's permissions.network, its settings addresses or the hosts its owner allowed", host, port), http.StatusForbidden)
			return
		}
		dial := func() (net.Conn, error) {
			var c net.Conn
			var err error
			switch {
			case hub:
				c, err = p.dial(ctx, "tcp", p.HubAddr)
			case local:
				c, err = p.dial(ctx, "tcp", net.JoinHostPort(loopbackDial, port))
			case configured:
				if broker {
					c, err = p.runtimeDial(ctx, host, port, true)
				} else {
					c, err = p.dialConfigured(ctx, host, port)
				}
			default:
				if broker {
					c, err = p.runtimeDial(ctx, host, port, false)
				} else {
					c, err = p.dialOut(ctx, host, port, rule)
				}
			}
			if c != nil {
				context.AfterFunc(ctx, func() { _ = c.Close() })
			}
			return c, err
		}
		if r.Method == http.MethodConnect || isUpgrade(r) {
			p.tunnel(w, r, dial)
			return
		}
		p.forward(w, r, ext, dial)
	})
}

// farHost is the Host header a request for hostport goes on with: the hub's address for HubHost, the loopback (with
// the port as written) for LocalHost, else def.
func (p *Proxy) farHost(hostport, def string) string {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, ""
	}
	switch strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), ".")) {
	case HubHost:
		return p.HubAddr
	case LocalHost:
		if port == "" {
			return "127.0.0.1"
		}
		return net.JoinHostPort("127.0.0.1", port)
	}
	return def
}

// parseRequestHostPort strictly parses and canonicalizes a proxy target host[:port].
// It accepts at most one enclosing IPv6 bracket pair, at most one trailing dot, and
// canonical decimal ports in 1..65535 (no leading zero, sign, or service name).
func parseRequestHostPort(hostport, defaultPort string) (host, port string, ok bool) {
	if hostport == "" {
		return "", "", false
	}
	if h, p, err := net.SplitHostPort(hostport); err == nil {
		if _, valid := parseStrictPort(p); !valid {
			return "", "", false
		}
		host, port = h, p
	} else {
		if strings.HasPrefix(hostport, "[") && strings.HasSuffix(hostport, "]") {
			inner := hostport[1 : len(hostport)-1]
			if inner == "" || strings.ContainsAny(inner, "[]%") || net.ParseIP(inner) == nil {
				return "", "", false
			}
			host, port = inner, defaultPort
		} else if !strings.ContainsAny(hostport, ":[]") {
			host, port = hostport, defaultPort
		} else {
			return "", "", false
		}
	}
	if host == "" || strings.ContainsAny(host, "[]% \t\r\n") || strings.HasSuffix(host, "..") {
		return "", "", false
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return "", "", false
	}
	return host, port, true
}

// isSelfIP reports whether host is a literal loopback or unspecified/0.0.0.0/8 IP address
// that can route back to this machine's loopback listener.
func isSelfIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil && ip4[0] == 0 {
		return true
	}
	return false
}

func isUpgrade(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") && r.Header.Get("Upgrade") != ""
}
