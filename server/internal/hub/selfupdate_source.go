package hub

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/nwverse/persona/server/internal/netproxy"
	"github.com/nwverse/persona/server/internal/update"
)

// The hub's update source (docs/operations/hub-update.md "Update source", #4522): where it reads latest.json and
// downloads releases from, and through which proxy. The build says where (-X main.updateURL); hub.json's update.url
// says otherwise without a new build, and update.proxy sends these requests alone through a proxy: the rest of what
// the hub talks to never goes through it.

const officialReleaseUpdateURL = "https://github.com/nwverse/persona/releases/latest/download"

// Where the update source came from (updateView.UpdateFrom).
const (
	updateFromConfig = "hub.json"
	updateFromBuild  = "build"
)

// updateSource is the effective release source and where it came from.
type updateSource struct {
	url  string // where latest.json is, without a trailing slash; "" nowhere
	from string // updateFromConfig or updateFromBuild; "" with no url
	// proxy is update.proxy; nil: the environment's (HTTPS_PROXY…), as before there was a setting.
	proxy *url.URL
	// urlErr: update.url does not read, and there is no source (not the build's either: it is the one replaced).
	// proxyErr: update.proxy does not read, and every check and download fails with it rather than going around it.
	urlErr, proxyErr error
}

// updateSource is read again at every use: the tests give the hub its build after it was made.
func (h *Hub) updateSource() updateSource {
	s := updateSource{url: strings.TrimRight(strings.TrimSpace(h.build.UpdateURL), "/")}
	privateOnly := h.cfg != nil && h.cfg.Security.PrivateNetworkOnly
	if privateOnly {
		s.url = "" // office network mode: only an update.url the organization gives (private_network.go)
	}
	if s.url != "" {
		s.from = updateFromBuild
	}
	if h.cfg != nil {
		if raw := strings.TrimSpace(h.cfg.Update.URL); raw != "" {
			u, err := parseUpdateURL(raw)
			s.url, s.from, s.urlErr = u, updateFromConfig, err
			if err != nil {
				s.from = ""
			}
		}
		if p, err := netproxy.Parse(h.cfg.Update.Proxy); err != nil {
			s.proxyErr = fmt.Errorf("hub.json update.proxy: %w", err) // netproxy leaves out user:pass
		} else {
			s.proxy = p
		}
	}
	if !privateOnly && s.url == "" && s.urlErr == nil && h.build.Channel == update.Release && strings.HasPrefix(h.version, "web-dev-") {
		s.url, s.from = officialReleaseUpdateURL, updateFromBuild
	}
	return s
}

// updateURL is where this hub looks for releases, or empty when there is no source.
func (h *Hub) updateURL() string { return h.updateSource().url }

// selfUpdates is Build.SelfUpdates with the update source hub.json may give.
func (h *Hub) selfUpdates() bool {
	b := h.build
	b.UpdateURL = h.updateURL()
	return b.SelfUpdates()
}

// parseUpdateURL reads hub.json's update.url: the directory latest.json is in, over https (http only to this machine:
// nothing is signed, docs/operations/hub-update.md "Security"), or a file:// one (a private publisher's copy), with no
// user and password, query or fragment. Its errors say why for people and never show the address, which may hold a
// password.
func parseUpdateURL(raw string) (string, error) {
	raw = strings.TrimRight(raw, "/")
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", errors.New("it is not a URL")
	case u.User != nil:
		return "", errors.New("it carries a user or password")
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", errors.New("it has a query or fragment")
	case path.Base(u.Path) == "latest.json":
		return "", errors.New("it names latest.json; give the directory latest.json is in")
	}
	switch u.Scheme {
	case "https":
		if u.Hostname() == "" {
			return "", errors.New("it has no host")
		}
	case "http":
		if !loopbackHost(u.Hostname()) {
			return "", errors.New("plain http:// is only for this machine (releases are not signed): use https://")
		}
	case "file":
		if u.Host != "" || !strings.HasPrefix(u.Path, "/") {
			return "", errors.New("a file:// address needs an absolute directory (file:///…)")
		}
	default:
		return "", errors.New("only https:// and file:// addresses work")
	}
	return raw, nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// proxyFor is the update client's Proxy: update.proxy for every request; one that does not read fails the request;
// none, the environment's as before.
func (u *updater) proxyFor(r *http.Request) (*url.URL, error) {
	s := u.h.updateSource()
	switch {
	case s.proxyErr != nil:
		return nil, s.proxyErr
	case s.proxy != nil:
		return s.proxy, nil
	}
	return http.ProxyFromEnvironment(r)
}

// proxyShown is a proxy as the log and the app show it: its scheme and host, never its user and password.
func proxyShown(p *url.URL) string {
	if p == nil {
		return ""
	}
	return (&url.URL{Scheme: p.Scheme, Host: p.Host}).String()
}

// logSource says at the start what hub.json's update section changes, and what in it does not read.
func (u *updater) logSource() {
	s := u.h.updateSource()
	switch {
	case s.urlErr != nil:
		u.h.logf("update: hub.json update.url cannot be used (%v): not looking for releases", s.urlErr)
	case s.from == updateFromConfig:
		u.h.logf("update: releases from %s (hub.json update.url)", s.url)
	}
	switch {
	case s.proxyErr != nil:
		u.h.logf("update: %v: every check and download fails until it is fixed", s.proxyErr)
	case s.proxy != nil:
		u.h.logf("update: checks and downloads through %s (hub.json update.proxy)", proxyShown(s.proxy))
	}
}
