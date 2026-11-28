package devices

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

const sourceHub = "hub_package"
const sourceBuild = "local_build"

type developmentTransport struct {
	base      *url.URL
	token     string
	commit    string
	transport http.RoundTripper
}

func (t developmentTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != t.base.Scheme || r.URL.Host != t.base.Host || !strings.HasPrefix(r.URL.EscapedPath(), t.base.EscapedPath()+"/") {
		return nil, errors.New("development package request left the bound Hub")
	}
	req := r.Clone(r.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.transport.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusNotFound && resp.Header.Get(devicewire.DevelopmentCommitHeader) != t.commit {
		// A proxy, older Hub, or withdrawn route cannot authorize a source build.
		resp.Body.Close()
		return nil, i18n.Err("devices-deploy.hub_changed")
	}
	return resp, err
}

func (d *Devices) developmentSite(t devicewire.DevelopmentTarget) (*http.Client, string, error) {
	cfg := d.config()
	current := cfg.DevelopmentTarget
	if t.ValidPublished() {
		current = cfg.PublishedTarget
	}
	if (!t.Valid() && !t.ValidPublished()) || cfg.DevelopmentHold || current == nil || *current != t || cfg.HubVersion != t.Version {
		return nil, "", i18n.Err("devices-deploy.hub_changed")
	}
	if d.opts.PackageHub == nil || !d.busUp() {
		return nil, "", bus.ErrOffline
	}
	c := d.opts.PackageHub()
	if c == nil || c.Token == "" {
		return nil, "", bus.ErrOffline
	}
	address, err := bus.HTTPURL(c.Hub, t.Packages)
	if err != nil {
		return nil, "", err
	}
	base, err := url.Parse(address)
	if err != nil || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, "", errors.New("invalid manager Hub address")
	}
	transport := http.DefaultTransport
	if c.HTTPClient != nil && c.HTTPClient.Transport != nil {
		transport = c.HTTPClient.Transport // includes the binding's TLS fingerprint
	}
	client := &http.Client{Transport: developmentTransport{base, c.Token, t.Commit, transport}, Timeout: packageDownload,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client, address, nil
}

// A 404 from this authenticated route means the current target lacks a package.
// A withdrawn target is 410. Auth, network and integrity failures all stop the update.
func (d *Devices) developmentManifest(ctx context.Context, t devicewire.DevelopmentTarget) (*update.Manifest, error) {
	client, base, err := d.developmentSite(t)
	if err != nil {
		return nil, err
	}
	m, err := readPackageManifest(ctx, client, base+"/manifest.json")
	if errors.Is(err, errNoManifest) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if m.Version != t.Version || m.Commit != t.Commit || m.Build != t.Build {
		return nil, i18n.Err("devices-deploy.hub_changed")
	}
	return m, nil
}

var platformPart = lazyre.New(`^[a-z0-9]+$`)

var errNoPublishedPackage = errors.New("no package for this platform on the Hub")

func (d *Devices) lookupDevelopment(ctx context.Context, k packageKey) (nodePackage, error) {
	if !platformPart.MatchString(k.goos) || !platformPart.MatchString(k.arch) {
		return nodePackage{}, i18n.Err("devices-deploy.os_unknown")
	}
	t := k.development
	m, err := d.developmentManifest(ctx, t)
	if err != nil {
		return nodePackage{}, err
	}
	plat := k.goos + "-" + k.arch
	program := k.program
	var f update.File
	var found bool
	if m != nil {
		switch k.program {
		case "persona-manager":
			f, found = m.Programs[k.program][plat]
			if !found {
				if full, ok := m.Files[plat]; ok {
					program, f, found = "personad", full, true
				}
			}
		case "persona-node":
			f, found = m.Nodes[k.program][plat]
		default:
			f, found = m.Files[plat]
		}
	}
	name := nodeBinaryName(program, plat)
	if !t.ValidPublished() {
		name += ".gz"
	} else if !found {
		return nodePackage{}, errNoPublishedPackage
	}
	p := nodePackage{Version: t.Version, build: t.Build, goos: k.goos, arch: k.arch, Program: program, development: &t, file: update.File{Name: name}}
	if found {
		if f.Name != name || f.Size <= 0 || !validDigest(f.SHA256) || !validDigest(f.BinarySHA256) {
			return nodePackage{}, errors.New("invalid development package manifest entry")
		}
		p.file = f
	} else if _, _, ok := developmentCached(p, ""); !ok {
		if err := developmentTools(k.goos, k.arch); err != nil {
			return nodePackage{}, err
		}
	}
	return p, nil
}

func validDigest(sum string) bool {
	b, err := hex.DecodeString(sum)
	return err == nil && len(b) == 32
}

type developmentReceipt struct {
	Target devicewire.DevelopmentTarget `json:"target"`
	SHA256 string                       `json:"sha256"`
	Source string                       `json:"source"`
}

func developmentCache(p nodePackage) (string, string) {
	return cachePaths(filepath.Join("development", p.Version), p.file)
}

func developmentCached(p nodePackage, want string) (string, string, bool) {
	bin, metadata := developmentCache(p)
	var r developmentReceipt
	ok, err := update.Load(metadata, &r)
	if !ok || err != nil || r.Target != *p.development || (r.Source != sourceHub && r.Source != sourceBuild) ||
		!validDigest(r.SHA256) || want != "" && r.SHA256 != want {
		return "", "", false
	}
	sum, err := fileSHA256(bin)
	return bin, r.Source, err == nil && sum == r.SHA256
}

func (d *Devices) fetchDevelopment(ctx context.Context, p nodePackage) (string, string, error) {
	t := *p.development
	client, base, err := d.developmentSite(t)
	if err != nil {
		return "", "", err
	}
	bin, metadata := developmentCache(p)
	defer d.packages.lockFile(bin)()
	if file, source, ok := developmentCached(p, p.file.BinarySHA256); ok {
		return file, source, nil
	}
	dir := filepath.Dir(bin)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	stage, err := os.MkdirTemp(dir, ".fetch-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(stage)
	var got string
	source, sum := sourceHub, p.file.BinarySHA256
	if sum != "" {
		if t.ValidPublished() {
			got, err = update.DownloadBinary(ctx, client, base, p.file, stage)
		} else {
			got, err = update.Download(ctx, client, base, p.file, stage)
		}
		if err != nil && (t.ValidPublished() || !update.IsNotFound(err)) {
			return "", "", i18n.Err("devices-deploy.package_bad", i18n.Args{"os": p.goos, "arch": p.arch, "err": err.Error()})
		}
	}
	if got == "" {
		// A source build is permitted only while the Hub still vouches for this identity.
		if _, err := d.developmentManifest(ctx, t); err != nil {
			return "", "", err
		}
		if file, source, ok := developmentCached(p, ""); ok && (source == sourceBuild || p.file.BinarySHA256 == "") {
			return file, source, nil
		}
		source = sourceBuild
		got, err = buildDevelopmentPackage(ctx, stage, p)
		if err == nil {
			sum, err = fileSHA256(got)
		}
		if err != nil {
			return "", "", err
		}
	}
	if err := checkPackageBuild(ctx, got, p); err != nil {
		return "", "", err
	}
	if err := os.Rename(got, bin); err != nil {
		return "", "", err
	}
	if err := update.Save(metadata, developmentReceipt{t, sum, source}); err != nil {
		return "", "", err
	}
	pruneCache(filepath.Join(identity.NodePackageCacheDir(), "development"), t.Version, t.Version, d.packages.clock())
	return bin, source, nil
}

// ManagerDevelopmentPackage uses the same verified store as the nodes. Only an
// independent manager that received the target can obtain its own program.
func (d *Devices) ManagerDevelopmentPackage(ctx context.Context, t devicewire.DevelopmentTarget) (string, error) {
	k := packageKey{goos: runtime.GOOS, arch: runtime.GOARCH, program: "persona-manager", development: t}
	p, err := d.lookupDevelopment(ctx, k)
	if errors.Is(err, errNoPublishedPackage) {
		return d.managerReleasePackage(ctx, k)
	}
	if err != nil {
		return "", err
	}
	name, _, err := d.fetchDevelopment(ctx, p)
	return name, err
}

func (d *Devices) managerReleasePackage(ctx context.Context, k packageKey) (string, error) {
	t := k.development
	k.base = d.config().NodePackagesURL
	if k.base == "" {
		return "", errNoPublishedPackage
	}
	m, err := d.fetchManifest(ctx, k.base+"/"+url.PathEscape(t.Version)+"/manifest.json")
	flat := errors.Is(err, errNoManifest)
	if flat {
		m, err = d.fetchManifest(ctx, k.base+"/latest.json")
	}
	if err != nil {
		return "", err
	}
	if m.Version != t.Version || m.Build != t.Build || m.Commit != t.Commit {
		return "", i18n.Err("devices-deploy.hub_changed")
	}
	plat := k.goos + "-" + k.arch
	program, f := k.program, m.Programs[k.program][plat]
	if f.Name == "" {
		program, f = "personad", m.Files[plat]
	}
	if f.Name != nodeBinaryName(program, plat)+".gz" || f.Size <= 0 || !validDigest(f.SHA256) || !validDigest(f.BinarySHA256) {
		return "", errNoPublishedPackage
	}
	p := remotePackage(k, m, t.Version, f, false)
	p.Program, p.flat = program, flat
	return d.fetchPackage(ctx, p)
}

// CheckDevelopmentTarget rechecks the Hub immediately before activation, including after an idle wait.
func (d *Devices) CheckDevelopmentTarget(ctx context.Context, t *devicewire.DevelopmentTarget) error {
	if t == nil {
		return nil
	}
	_, err := d.developmentManifest(ctx, *t)
	return err
}

func (m *Machine) fetchDeployment(ctx context.Context, p nodePackage) (string, error) {
	if p.development == nil {
		return m.d.fetchPackage(ctx, p)
	}
	path, source, err := m.d.fetchDevelopment(ctx, p)
	if err == nil {
		m.mu.Lock()
		m.deployVersion, m.deploySource = p.Version, source
		m.mu.Unlock()
		m.d.changed()
	}
	return path, err
}

// DevelopmentTarget is the installed version this independent manager may adopt
// under the fleet update policy. The bus must still be connected.
func (d *Devices) DevelopmentTarget() *devicewire.DevelopmentTarget {
	cfg := d.config()
	if !cfg.AutoUpdateNodes || cfg.DevelopmentHold || cfg.DevelopmentTarget == nil || !cfg.DevelopmentTarget.Valid() ||
		cfg.HubVersion != cfg.DevelopmentTarget.Version || !d.busUp() {
		return nil
	}
	return cfg.DevelopmentTarget
}

// ManagerBuild is what the Hub currently authorizes this manager to follow.
// With fleet updates disabled the manager checks for a local installation of
// the same build before taking it.
func (d *Devices) ManagerBuild() (target *devicewire.DevelopmentTarget, automatic bool) {
	cfg := d.config()
	if cfg.DevelopmentHold || !d.busUp() {
		return nil, false
	}
	for _, t := range []*devicewire.DevelopmentTarget{cfg.DevelopmentTarget, cfg.PublishedTarget} {
		if t != nil && (t.Valid() || t.ValidPublished()) && t.Version == cfg.HubVersion {
			return t, cfg.AutoUpdateNodes
		}
	}
	return nil, false
}

func (d *Devices) DevelopmentIdle() bool {
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		busy := m.deploying || m.online && slices.ContainsFunc(m.sessions, sessionAtWork)
		m.mu.Unlock()
		if busy {
			return false
		}
	}
	return true
}
