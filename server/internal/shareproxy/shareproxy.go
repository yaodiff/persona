// Package shareproxy exposes only the isolated public-sharing graph on a separate HTTP port.
// It holds no hub state, credentials or business logic and never trusts forwarded client addresses.
package shareproxy

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/shareproof"
	"golang.org/x/net/netutil"
)

const (
	maxResponse    = 1 << 20
	maxRelays      = 16
	maxConnections = 64
	csp            = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'none'; font-src 'none'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; worker-src 'none'"
)

// Config pins both the external authority and a literal-loopback upstream. Listen must be unprivileged.
type Config struct {
	Listen, Upstream, PublicOrigin string
	IngressKey                     []byte
}

func endpoint(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(u.Host, "\\ \t\r\n") {
		return nil, errors.New("invalid HTTP endpoint")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("endpoint needs an explicit valid port")
	}
	u.Host = strings.ToLower(u.Host)
	return u, nil
}

func validate(c Config) (*url.URL, *url.URL, error) {
	if len(c.IngressKey) != 32 {
		return nil, nil, errors.New("a private 256-bit sharing ingress key is required")
	}
	upstream, err := endpoint(c.Upstream)
	if err != nil {
		return nil, nil, err
	}
	ip, err := netip.ParseAddr(upstream.Hostname())
	if err != nil || !ip.IsLoopback() {
		return nil, nil, errors.New("upstream must be literal loopback")
	}
	origin, err := endpoint(c.PublicOrigin)
	if err != nil {
		return nil, nil, err
	}
	_, p, err := net.SplitHostPort(c.Listen)
	port, perr := strconv.Atoi(p)
	if err != nil || perr != nil || port < 1024 || port > 65535 || p != origin.Port() || origin.Host == upstream.Host {
		return nil, nil, errors.New("listen needs an independent unprivileged origin port")
	}
	return upstream, origin, nil
}

func headers(h http.Header) {
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, noarchive, nofollow")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("X-Frame-Options", "DENY")
}

func localError(w http.ResponseWriter, status int, code string) {
	headers(w.Header())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "{\"code\":%q}\n", code)
}

func capability(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, ch := range s {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '-' || ch == '_') {
			return false
		}
	}
	return true
}

func publicPath(p string) bool {
	for _, prefix := range []string{"/share/", "/api/public/agent-shares/"} {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			return capability(rest)
		}
	}
	asset, ok := strings.CutPrefix(p, "/share-assets/")
	if !ok || len(asset) > 256 || strings.Contains(asset, "..") || !(strings.HasSuffix(asset, ".js") || strings.HasSuffix(asset, ".css")) {
		return false
	}
	for _, ch := range asset {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || strings.ContainsRune("-_.", ch)) {
			return false
		}
	}
	return true
}

func boundedResponse(r *http.Response) error {
	defer r.Body.Close()
	if r.StatusCode != 200 && r.StatusCode != 400 && r.StatusCode != 404 && r.StatusCode != 409 && r.StatusCode != 429 {
		return errors.New("unexpected upstream status")
	}
	if r.Header.Get("Set-Cookie") != "" || r.Header.Get("Location") != "" || r.Header.Get("WWW-Authenticate") != "" || r.Header.Get("Content-Encoding") != "" || r.ContentLength > maxResponse {
		return errors.New("unexpected upstream response")
	}
	kind, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (kind != "application/json" && kind != "text/html" && kind != "text/css" && kind != "text/javascript" && kind != "application/javascript" && kind != "text/plain") {
		return errors.New("unexpected upstream content type")
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return errors.New("upstream response budget")
	}
	h := make(http.Header)
	h.Set("Content-Type", r.Header.Get("Content-Type"))
	if retry := r.Header.Get("Retry-After"); retry != "" {
		if n, err := strconv.Atoi(retry); err == nil && n >= 1 && n <= 3600 {
			h.Set("Retry-After", retry)
		}
	}
	headers(h)
	r.Header, r.Body, r.ContentLength = h, io.NopCloser(bytes.NewReader(body)), int64(len(body))
	r.Trailer = nil
	return nil
}

// shutdownGrace bounds how long Serve waits for in-flight HTTP requests to drain on cancellation.
var shutdownGrace = 5 * time.Second

// Handler rejects nonpublic traffic before proxying, even if a client forges the application's Host.
func Handler(c Config) (http.Handler, error) {
	h, _, err := handlerWithTransport(c)
	return h, err
}

func handlerWithTransport(c Config) (http.Handler, *http.Transport, error) {
	upstream, origin, err := validate(c)
	if err != nil {
		return nil, nil, err
	}
	transport := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		DisableCompression: true, MaxConnsPerHost: maxRelays, MaxIdleConnsPerHost: maxRelays,
		IdleConnTimeout: 30 * time.Second, ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 16 << 10}
	c.IngressKey = append([]byte(nil), c.IngressKey...)
	var lostAuthentication atomic.Bool
	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(upstream)
			p.Out.Host = origin.Host
			p.Out.Header = make(http.Header)
			for _, key := range []string{"Accept", "Accept-Language"} {
				if v := p.In.Header.Get(key); v != "" {
					p.Out.Header.Set(key, v)
				}
			}
			p.Out.Header.Set("Cache-Control", "no-store")
			p.Out.Header.Set("User-Agent", "Persona-share-http")
			if ip, ok := shareproof.Peer(p.In.RemoteAddr); ok {
				if proof := shareproof.Sign(c.IngressKey, p.Out, ip, time.Now()); proof != "" {
					p.Out.Header.Set(shareproof.Header, proof)
				}
			}
		},
		ModifyResponse: func(r *http.Response) error {
			if shareproof.Scope(r.Request) && r.Header.Get(shareproof.Acknowledgement) != shareproof.Accepted {
				lostAuthentication.Store(true)
				_ = r.Body.Close()
				return errors.New("sharing ingress authentication lost")
			}
			return boundedResponse(r)
		},
		ErrorLog:     log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) { localError(w, 502, "unavailable") },
	}
	slots := make(chan struct{}, maxRelays)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers(w.Header())
		if r.Method != http.MethodGet || !strings.EqualFold(r.Host, origin.Host) || r.URL.IsAbs() || r.URL.RawPath != "" || r.URL.Opaque != "" || !publicPath(r.URL.Path) || len(r.RequestURI) > 4096 || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || r.Header.Get("Upgrade") != "" {
			localError(w, 404, "not_found")
			return
		}
		if shareproof.Scope(r) {
			if _, ok := shareproof.Peer(r.RemoteAddr); !ok {
				localError(w, 404, "not_found")
				return
			}
		}
		if shareproof.Scope(r) && lostAuthentication.Load() {
			localError(w, 502, "unavailable")
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			w.Header().Set("Retry-After", "15")
			localError(w, 429, "busy")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		proxy.ServeHTTP(w, r.WithContext(ctx))
	}), transport, nil
}

// Serve owns only this new listener. Cancellation drains in-flight requests for up to shutdownGrace
// before closing active connections, and never signals or switches the upstream hub.
func Serve(ctx context.Context, c Config) error {
	h, transport, err := handlerWithTransport(c)
	if err != nil {
		return err
	}
	defer transport.CloseIdleConnections()
	if err := probeIngress(ctx, c); err != nil {
		return err
	}
	listener, err := osproc.Listen("tcp", c.Listen)
	if err != nil {
		return errors.New("sharing HTTP listener unavailable")
	}
	server := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: log.New(io.Discard, "", 0)}
	server.RegisterOnShutdown(transport.CloseIdleConnections)
	served := make(chan error, 1)
	go func() {
		served <- server.Serve(netutil.LimitListener(listener, maxConnections))
	}()
	select {
	case err = <-served:
		_ = server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return errors.New("sharing HTTP server stopped")
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := server.Shutdown(shutdown); err != nil {
		_ = server.Close()
	}
	<-served
	return nil
}

func Run(ctx context.Context, args []string, output io.Writer) error {
	f := flag.NewFlagSet("share-http", flag.ContinueOnError)
	f.SetOutput(output)
	var c Config
	var configPath string
	f.StringVar(&configPath, "hub-config", "", "owned mode0600 hub configuration containing sharing.ingress_key")
	f.StringVar(&c.Listen, "listen", "", "independent unprivileged HTTP listen address")
	f.StringVar(&c.Upstream, "upstream", "", "literal-loopback HTTP hub origin with explicit port")
	f.StringVar(&c.PublicOrigin, "public-origin", "", "configured independent HTTP sharing origin with explicit port")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("share-http accepts only its named flags")
	}
	key, err := readIngressKey(configPath)
	if err != nil {
		return err
	}
	c.IngressKey = key
	return Serve(ctx, c)
}
