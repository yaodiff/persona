package devices

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

// programConnector is the Persona Link connector (server/cmd/persona-link): persona-link-<os>-<arch> beside the node
// builds, persona-link-<os>-<arch>.gz under a release's links.
const programConnector = "persona-link"

// connectorPackage is the Persona Link connector build for a computer of goos/arch, found where the node builds are
// (docs/operations/node-update.md "Node packages"): the hub's own (the node binary dir's persona-link-<os>-<arch>,
// counted only when its .version says the hub's), else the release's of the hub's version (manifest links), fetched
// into the cache and checked like a node build. path is a file on this machine; version what it reports.
// A switch to Persona Link (link_switch.go) sends it over ssh. errNoConnector: none anywhere.
func (d *Devices) connectorPackage(ctx context.Context, goos, arch string) (path, version string, err error) {
	pkg, err := d.lookupShared(ctx, d.connectorKey(goos, arch))
	if err != nil {
		return "", "", err
	}
	if path, err = d.fetchPackage(ctx, pkg); err != nil {
		return "", "", err
	}
	return path, pkg.Version, nil
}

// errNoConnector: there is no Persona Link build for that platform (proto.LinkSwitchReasonNoPackage).
var errNoConnector = errors.New("no Persona Link build for this platform")

// connectorKey is the connector's lookup for goos/arch, remembered and shared like a node build's. packageKey never
// makes one: what a node reports of itself cannot have it sent the connector as its build.
func (d *Devices) connectorKey(goos, arch string) packageKey {
	cfg := d.config()
	return packageKey{goos: goos, arch: arch, program: programConnector, own: d.edition(), hub: cfg.HubVersion, base: cfg.NodePackagesURL}
}

// platformWord is an OS or architecture as Go names them (linux, arm64): it goes into a file name and a URL.
var platformWord = lazyre.New(`^[a-z0-9]+$`)

// lookupConnector finds the connector of the hub's version for k's platform, in order: the hub's own (the node binary
// dir's, then the bundled dir's persona-link-<os>-<arch>, only with a .version beside it that says the hub's, as
// deploy.sh writes it after the binary, and Go build info of that platform, program and manager edition); the release of the hub's
// version (dl/<version>/manifest.json's links, or latest.json's from a source without dl/<version>/ when it is that
// release). A newer release is never taken in its place (a node build's fallback is judged by the node protocol,
// which says nothing of the connector). While the site does not answer, the build fetched or found before stands.
func (d *Devices) lookupConnector(ctx context.Context, k packageKey) (nodePackage, error) {
	name := programConnector + "-" + k.goos + "-" + k.arch
	missing := func(why string) error {
		return fmt.Errorf("%w: %s (%s of version %s)", errNoConnector, why, name, k.hub)
	}
	if !platformWord.MatchString(k.goos) || !platformWord.MatchString(k.arch) {
		return nodePackage{}, missing("not a platform")
	}
	for _, dir := range localNodeDirs() {
		p := filepath.Join(dir, name)
		b, err := os.ReadFile(p + ".version")
		if err != nil || k.hub == "" || strings.TrimSpace(string(b)) != k.hub || !fileExists(p) {
			continue // none, or one an earlier deploy left (the hub's own updates replace personad-* alone)
		}
		if err := checkPackagePlatform(p, k.goos, k.arch, programConnector); err != nil {
			d.logf("connector package for %s-%s: ignore local %s: %v", k.goos, k.arch, name, err)
			continue
		}
		if err := checkPackageEdition(p, k.own); err != nil {
			d.logf("connector package for %s-%s: ignore local %s: %v", k.goos, k.arch, name, err)
			continue
		}
		return nodePackage{Version: k.hub, Program: programConnector, goos: k.goos, arch: k.arch, path: p}, nil
	}
	if k.base == "" || k.hub == "dev" || !safeVersion.MatchString(k.hub) {
		return nodePackage{}, missing("not among the hub's builds, and no release to fetch one from")
	}
	release := func(m *update.Manifest, flat bool) (nodePackage, error) {
		f, ok := m.LinkFile(k.goos + "-" + k.arch)
		if !ok || f.Name != name+".gz" { // its name goes into a URL and the cache's paths
			return nodePackage{}, missing("not in the release of the hub's version")
		}
		pkg := remotePackage(k, m, k.hub, f, false)
		pkg.flat = flat
		return pkg, nil
	}
	m, err := d.fetchManifest(ctx, k.base+"/"+url.PathEscape(k.hub)+"/"+update.ReleaseManifestName(d.edition()))
	if err == nil {
		return release(m, false)
	}
	if errors.Is(err, errNoManifest) {
		// A source without dl/<version>/ (a GitHub repository's releases/latest/download) serves the release of the
		// hub's version as its latest.json while that is the newest.
		m, err = d.fetchManifest(ctx, k.base+"/"+update.FeedName(d.edition()))
		switch {
		case err == nil && m.Version == k.hub:
			return release(m, true)
		case err == nil || errors.Is(err, errNoManifest):
			return nodePackage{}, missing("no release of the hub's version")
		}
	}
	// The site does not answer: what was fetched or found before stands in for a minute.
	if pkg, ok := cachedRelease(k, k.hub); ok {
		return standIn(pkg), nil
	}
	if pkg, ok := d.lastFound(k); ok {
		return pkg, nil
	}
	return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
}
