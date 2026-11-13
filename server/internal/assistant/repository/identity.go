// Package repository normalizes remote references and checks existence metadata.
// It never clones, fetches source, or establishes a caller's repository permissions.
package repository

import (
	"errors"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

type Identity struct {
	Provider     string `json:"provider"`
	InstanceURL  string `json:"instance_url"`
	Path         string `json:"path"`
	CanonicalURL string `json:"canonical_url"`
}

var ErrRemote = errors.New("invalid repository remote")

// Parse accepts web and git SSH references. Provider-specific page boundaries
// determine the repository root; GitLab namespaces are never truncated to two
// segments. Custom instances require an explicit provider. No network runs here.
func Parse(remote, provider string) (Identity, error) {
	bad := func() (Identity, error) { return Identity{}, ErrRemote }
	if len(remote) > 2048 || remote == "" || strings.ContainsAny(remote, " \t\r\n\\") {
		return bad()
	}
	if strings.HasPrefix(remote, "git@") && !strings.Contains(remote, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(remote, "git@"), ":")
		if !ok || strings.ContainsAny(host, "/:@") {
			return bad()
		}
		remote = "ssh://git@" + host + "/" + path
	}
	u, err := url.Parse(remote)
	if err != nil || u.Opaque != "" || u.Host == "" || strings.HasSuffix(u.Host, ":") {
		return bad()
	}
	switch u.Scheme {
	case "http", "https":
		if u.User != nil {
			return bad()
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return bad()
		}
		for key := range query {
			if slices.Contains([]string{"token", "access_token", "private_token", "auth", "authorization", "password", "api_key", "apikey", "signature"}, strings.ToLower(key)) {
				return bad()
			}
		}
	case "ssh":
		if u.User == nil || u.User.String() != "git" || u.RawQuery != "" || u.Fragment != "" {
			return bad()
		}
	default:
		return bad()
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || len(host) > 253 {
		return bad()
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return bad()
			}
			for _, r := range label {
				if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
					return bad()
				}
			}
		}
	}
	port, scheme := u.Port(), u.Scheme
	if scheme == "ssh" {
		alternative := host == "ssh.github.com" || host == "altssh.gitlab.com"
		if port != "" && port != "22" && !(alternative && port == "443") {
			return bad()
		}
		if host == "ssh.github.com" {
			host = "github.com"
		} else if host == "altssh.gitlab.com" {
			host = "gitlab.com"
		}
		scheme, port = "https", ""
	} else if host == "ssh.github.com" || host == "altssh.gitlab.com" {
		return bad()
	}
	api := host == "api.github.com"
	if host == "www.github.com" || api {
		host = "github.com"
	}
	defaultPort := map[string]string{"http": "80", "https": "443"}[scheme]
	if port != "" && port != defaultPort {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1024 || n > 65535 || strconv.Itoa(n) != port {
			return bad()
		}
	} else {
		port = ""
	}
	// Public web links have one HTTPS identity. Self-hosted HTTP and ports are
	// kept exactly as supplied; an SSH port never implies a web/API port.
	if (host == "github.com" || host == "gitlab.com") && port == "" {
		scheme = "https"
	}
	authority := host
	if strings.Contains(host, ":") {
		authority = "[" + host + "]"
	}
	if port != "" {
		authority = net.JoinHostPort(host, port)
	}
	known := ""
	switch host {
	case "github.com":
		known = "github"
	case "gitlab.com":
		known = "gitlab"
	}
	if provider == "" {
		provider = known
	}
	if provider != "github" && provider != "gitlab" || known != "" && provider != known {
		return bad()
	}
	// Validate before trimming pages, including encoded traversal that browsers
	// would otherwise normalize away. Encoded filenames on a subpage are fine.
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return bad()
		}
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	parts := strings.Split(path, "/")
	if api {
		if len(parts) < 3 || parts[0] != "repos" {
			return bad()
		}
		parts = parts[1:]
	}
	if u.Scheme != "ssh" {
		if provider == "github" && len(parts) > 2 {
			if !slices.Contains(strings.Fields("tree blob raw blame commit commits pull pulls issues actions releases tags branches compare discussions wiki projects settings security network deployments packages graphs stargazers watchers forks"), parts[2]) {
				return bad()
			}
			parts = parts[:2]
		} else if provider == "gitlab" {
			if index := slices.Index(parts, "-"); index >= 0 {
				parts = parts[:index]
			}
		}
	}
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if strings.HasSuffix(strings.ToLower(last), ".git") {
			parts[len(parts)-1] = last[:len(last)-4]
		}
	}
	path = strings.Join(parts, "/")
	if len(parts) < 2 || provider == "github" && len(parts) != 2 || len(parts) > 20 || len(path) > 512 {
		return bad()
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(strings.ToLower(part), ".git") || strings.HasPrefix(part, "-") || strings.HasPrefix(part, ".") {
			return bad()
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-", r)) {
				return bad()
			}
		}
	}
	path = strings.ToLower(path)
	instance := scheme + "://" + authority
	return Identity{Provider: provider, InstanceURL: instance, Path: path, CanonicalURL: instance + "/" + path}, nil
}
