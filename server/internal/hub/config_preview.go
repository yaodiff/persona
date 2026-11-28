package hub

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// PreviewConfig configures isolated wildcard-subdomain origins for web dev-server previews
// (docs/web/preview.md "Isolated preview origin", docs/operations/config.md).
// Empty (the default) disables preview-origin proxying completely.
type PreviewConfig struct {
	// Domain is the wildcard base domain (e.g. "preview.example.com"), serving each
	// (machine, port) at https://<label>.<domain>. Empty disables the feature.
	Domain string `json:"domain,omitempty"`
	// Port is an optional external HTTPS port included in preview URLs (1..65535).
	// When omitted or 0, defaults to the hub's TLS listener port if cert_file is set, else 443.
	Port int `json:"port,omitempty"`
	// CertFile and KeyFile are an optional wildcard TLS certificate and private key for
	// *.<domain>, served by public.tls_listen on matching SNI and reloaded periodically.
	// Omit both when a reverse proxy in front of the hub terminates TLS.
	CertFile string `json:"cert_file,omitempty"`
	KeyFile  string `json:"key_file,omitempty"`
	// AllowedAncestors lists additional canonical http(s)://host[:port] web origins
	// permitted to embed preview iframes via Content-Security-Policy: frame-ancestors
	// alongside the hub's own web origins (public_url, public.advertise).
	AllowedAncestors []string `json:"allowed_ancestors,omitempty"`
}

// previewDomainOf validates and normalizes preview.domain. Empty returns "", nil.
func previewDomainOf(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", nil
	}
	s = strings.TrimSuffix(s, ".")
	if s == "" || len(s) > 200 {
		return "", fmt.Errorf("preview.domain %q: invalid domain length", raw)
	}
	if strings.ContainsAny(s, ":/\\*? \t\r\n") {
		return "", fmt.Errorf("preview.domain %q: must be a bare domain name (no scheme, wildcard, port, or path)", raw)
	}
	if net.ParseIP(s) != nil {
		return "", fmt.Errorf("preview.domain %q: IP addresses are not supported; use a DNS domain", raw)
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return "", fmt.Errorf("preview.domain %q: must contain at least two DNS labels", raw)
	}
	for _, label := range labels {
		if !isValidDNSLabel(label) {
			return "", fmt.Errorf("preview.domain %q: invalid DNS label %q", raw, label)
		}
	}
	return s, nil
}

func isValidDNSLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !alnum && c != '-' {
			return false
		}
		if (i == 0 || i == len(label)-1) && c == '-' {
			return false
		}
	}
	return true
}

// previewRequestHost extracts the lowercase hostname (without port, brackets, or trailing dot)
// from an HTTP Host header or TLS SNI value.
func previewRequestHost(rawHost string) string {
	s := strings.TrimSpace(rawHost)
	if s == "" || strings.ContainsAny(s, "/\\ \t\r\n") {
		return ""
	}
	host := s
	if strings.HasPrefix(s, "[") {
		h, _, err := net.SplitHostPort(s)
		if err == nil {
			host = h
		} else if strings.HasSuffix(s, "]") {
			host = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
		} else {
			return ""
		}
	} else if strings.Contains(s, ":") {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			return ""
		}
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return ""
		}
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	return host
}

// hostMatchesPreviewDomain reports whether host equals domain or is any subdomain of it.
func hostMatchesPreviewDomain(host, domain string) bool {
	if domain == "" {
		return false
	}
	h := previewRequestHost(host)
	return h != "" && (h == domain || strings.HasSuffix(h, "."+domain))
}

// previewHostLabel parses rHost against canonical preview domain.
// matched is true for both the bare domain and any subdomain of it (none of which may fall through
// to hub API/web routes). label is non-empty only for a valid single-level <label>.<domain>.
func previewHostLabel(rHost, domain string) (label string, matched bool) {
	if domain == "" {
		return "", false
	}
	h := previewRequestHost(rHost)
	if h == "" {
		return "", false
	}
	if h == domain {
		return "", true
	}
	prefix, ok := strings.CutSuffix(h, "."+domain)
	if !ok {
		return "", false
	}
	if prefix != "" && !strings.Contains(prefix, ".") && isValidDNSLabel(prefix) {
		return prefix, true
	}
	return "", true
}

// canonicalPreviewOrigin parses an http(s) origin (or full URL, stripping path/query/fragment only
// when allowPath is true) into canonical "scheme://host[:port]" form (omitting default ports 80/443).
func canonicalPreviewOrigin(raw string, allowPath bool) string {
	s := strings.TrimSpace(raw)
	if s == "" || strings.ContainsAny(s, "\\ \t\r\n") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	if !allowPath && (u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "") {
		return ""
	}
	hostname := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if hostname == "" || strings.HasSuffix(u.Host, ":") {
		return ""
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return ""
		}
	}
	host := hostname
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" && !((u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443")) {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}

// checkPreview validates hub.json's preview section at startup.
func (c *Config) checkPreview() error {
	if c == nil {
		return nil
	}
	p := c.Preview
	hasOther := p.Port != 0 || p.CertFile != "" || p.KeyFile != "" || len(p.AllowedAncestors) > 0
	if strings.TrimSpace(p.Domain) == "" {
		if hasOther {
			return errors.New("preview.domain is required when other preview.* settings are configured")
		}
		return nil
	}
	domain, err := previewDomainOf(p.Domain)
	if err != nil {
		return err
	}
	if p.Port < 0 || p.Port > 65535 {
		return fmt.Errorf("preview.port %d: must be between 1 and 65535", p.Port)
	}
	if (p.CertFile == "") != (p.KeyFile == "") {
		return errors.New("preview.cert_file and preview.key_file must be set together")
	}
	if p.CertFile != "" && strings.TrimSpace(c.Public.TLSListen) == "" {
		return errors.New("preview.cert_file requires public.tls_listen")
	}
	if o := canonicalPreviewOrigin(c.PublicURL, true); o != "" {
		if u, err := url.Parse(o); err == nil && hostMatchesPreviewDomain(u.Hostname(), domain) {
			return fmt.Errorf("preview.domain %q must not overlap public_url %q", domain, c.PublicURL)
		}
	}
	for _, adv := range c.Public.Advertise {
		o := canonicalPreviewOrigin(adv, true)
		if o == "" && !strings.Contains(adv, "://") {
			o = canonicalPreviewOrigin("https://"+strings.TrimSpace(adv), true)
		}
		if o != "" {
			if u, err := url.Parse(o); err == nil && hostMatchesPreviewDomain(u.Hostname(), domain) {
				return fmt.Errorf("preview.domain %q must not overlap public.advertise %q", domain, adv)
			}
		}
	}
	if c.Sharing != nil && c.Sharing.PublicOrigin != "" {
		if o := canonicalPreviewOrigin(c.Sharing.PublicOrigin, false); o != "" {
			if u, err := url.Parse(o); err == nil && hostMatchesPreviewDomain(u.Hostname(), domain) {
				return fmt.Errorf("preview.domain %q must not overlap sharing.public_origin %q", domain, c.Sharing.PublicOrigin)
			}
		}
	}
	for _, anc := range p.AllowedAncestors {
		o := canonicalPreviewOrigin(anc, false)
		if o == "" {
			return fmt.Errorf("preview.allowed_ancestors: %q must be a canonical http:// or https:// origin", anc)
		}
		if u, err := url.Parse(o); err == nil && hostMatchesPreviewDomain(u.Hostname(), domain) {
			return fmt.Errorf("preview.allowed_ancestors: %q must not be under preview.domain %q", anc, domain)
		}
	}
	return nil
}
