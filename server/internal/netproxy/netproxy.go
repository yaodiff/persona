// Package netproxy parses outbound HTTP proxies, plus SOCKS5 where a caller supports it, and builds HTTP clients.
// Proxy addresses are deployment-specific: they live only in local configuration, never in code, docs or tests.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Parse reads an http://[user:pass@]host:port proxy (port 1-65535, 80 when left out); "" (or blanks) is no proxy
// (nil). Errors never show any part of user:pass. The URL's Host always has a port, so callers that dial it themselves
// (mail's CONNECT tunnels) need no default of their own.
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || !wellFormed(u) {
		return nil, fmt.Errorf("proxy must be http://[user:pass@]host:port, got %q", redact(raw))
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "80")
	}
	return u, nil
}

// wellFormed reports whether u is http://[user:pass@]host[:port][/] with a host and a valid port.
func wellFormed(u *url.URL) bool {
	if u.Scheme != "http" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	port := u.Port()
	if port == "" {
		return !strings.HasSuffix(u.Host, ":")
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// HTTPClient is a client with timeout that sends everything through proxy, or directly when proxy is nil. Direct
// means direct: the process's HTTP(S)_PROXY environment is not consulted, so only hub.json decides. EnvHTTPClient is
// the client that follows the environment.
func HTTPClient(proxy *url.URL, timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Timeout: timeout, Transport: t}
}

// EnvHTTPClient is a client with timeout that follows the process's proxy environment: HTTPS_PROXY, HTTP_PROXY and
// NO_PROXY, with the rules http.ProxyFromEnvironment applies to them. A host NO_PROXY lists and a loopback host go
// direct, and with no proxy set everything does. A value Go cannot read as a proxy is ignored there too, so the
// request goes direct: unlike a setting, the environment is not this package's to refuse. The environment is read
// when the client is made, not once per process, so a value that arrives later counts for the next client. The
// proxy's address and its user:pass stay in the environment: nothing here stores or logs them.
func EnvHTTPClient(timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	env := envProxyConfig().ProxyFunc()
	t.Proxy = func(r *http.Request) (*url.URL, error) { return env(r.URL) }
	return &http.Client{Timeout: timeout, Transport: t}
}

// envProxyConfig reads the proxy environment in net/http's order, each name in upper case before lower case, so this
// client picks the proxy the process's http.ProxyFromEnvironment clients pick when both spellings are set (the
// golang.org/x/net release this module uses reads lower case first on its own). REQUEST_METHOD marks a CGI process,
// where HTTP_PROXY may come from a request header and is refused for http URLs.
func envProxyConfig() *httpproxy.Config {
	first := func(names ...string) string {
		for _, n := range names {
			if v := os.Getenv(n); v != "" {
				return v
			}
		}
		return ""
	}
	return &httpproxy.Config{
		HTTPProxy:  first("HTTP_PROXY", "http_proxy"),
		HTTPSProxy: first("HTTPS_PROXY", "https_proxy"),
		NoProxy:    first("NO_PROXY", "no_proxy"),
		CGI:        os.Getenv("REQUEST_METHOD") != "",
	}
}

// schemePrefix matches a URL's leading "scheme://".
var schemePrefix = lazyre.New(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// redact is raw for an error message, with everything that may be user:pass (up to the last '@') cut out. Neither
// url.Parse's error nor the URL it built is shown: a '/', '?' or '#' in a password ends the host early, so either can
// hold part of the password outside the userinfo.
func redact(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	return schemePrefix.FindString(raw[:at]) + "xxxxx" + raw[at:]
}
