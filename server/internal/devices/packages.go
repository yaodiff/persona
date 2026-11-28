package devices

import (
	"bytes"
	"cmp"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/netproxy"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

// Node packages (docs/operations/node-update.md "Node packages"): the build a computer's node gets, for its OS, architecture
// and the kind of program it runs (personad, or persona-node, the device's own). In order:
//
//  1. the hub's own builds on this machine: the node binary dir (deploy.sh's, a self-updating hub's release, what an
//     air-gapped hub's owner dropped in) and the bundled dir (the Docker image's own platform), of the hub's version;
//  2. the release of the hub's version, dl/<version>/manifest.json at the hub's node_packages.url (devicewire.Config),
//     fetched into the cache;
//  3. when there is none (an edge, dev or pull request image), the newest release, dl/latest.json, if this manager
//     drives the node it has (its node protocol range and capabilities, as compatOf judges a node): the fallback;
//  4. else no build: Redeploy is not offered, and the machine card says what to do.
//
// Nothing is signed (the user's call, docs/operations/hub-update.md "Security: no signing"): a fetched build must match the
// manifest's size and sha256 (archive) and binary_sha256 (unpacked), read as a build for that OS and architecture of
// that kind of program, and, on this machine's own platform, run and say the release's version and build (as hub
// self-update checks its own); the node's node-update-check checks the version once more on arrival. It lands in the
// cache whole: downloaded and checked in a staging directory, then renamed into place.

// Package lookups are remembered: found ones for a while, failures shortly (a manifest that appears, a network that
// comes back).
const (
	packageFoundTTL = 10 * time.Minute
	packageMissTTL  = time.Minute
	// packageLookup bounds one lookup's manifest fetches; packageDownload one download.
	packageLookup   = 20 * time.Second
	packageDownload = 10 * time.Minute
)

// packageLookupBudget is the active timeout used by lookupShared and readPackageManifest (overridden in tests).
var packageLookupBudget = packageLookup

// nodePackage is a node build for a platform.
type nodePackage struct {
	// Version is what a node running it reports: the hub's version for its own builds, the release's otherwise.
	Version string
	Program string // nodeproto.ProgramPersonad or ProgramPersonaNode, or programConnector (connector_package.go): what it is
	// Fallback: not of the hub's version (the newest release this manager drives).
	Fallback   bool
	goos, arch string
	// edition is the edition a fetched release build must be (the manager's own); "" for a local or development build.
	edition nodeproto.Edition
	path    string // a file on this machine; "" until fetched
	// A fetched one: <base>/<dir>/<file.Name>, of release build; dir also names its cache directory.
	base, dir string
	// flat: listed in <base>/latest.json, so also served at <base>/<file.Name>, where hub self-update fetches it. A
	// source without per-version directories has it only there: a GitHub repository's releases/latest/download.
	development *devicewire.DevelopmentTarget
	flat        bool
	file        update.File
	build       int64
	// The release's node protocol range and capabilities (a fetched one), kept with it in the cache so a cached
	// fallback is judged again before it is used.
	nodeProtocol, nodeProtocolMin int
	nodeCapabilities              []string
	// stale: the last build found, used again because the release site does not answer now (remembered shortly).
	stale bool
}

type packageKey struct {
	goos, arch, program, hub, base string
	// edition is the edition of the node the build is for (nodeproto.EditionFull or EditionCore; "" for a node that
	// said a word this manager does not know): only a build of that edition is ever sent to it.
	edition nodeproto.Edition
	// own is this manager's edition (Devices.edition): the edition of every build it fetches and keeps in its cache.
	own         nodeproto.Edition
	development devicewire.DevelopmentTarget
	published   devicewire.DevelopmentTarget
	hold        bool
}

type packageResult struct {
	pkg nodePackage
	err error
	at  time.Time
	// local is localStamp(k) as the lookup began: a lookup remembered under another one is not fresh, so a build
	// put in place afterwards (deploy.sh, a person, a hub update) is taken at once, not packageFoundTTL later.
	local string
	// localRecent means that stamp included a file too new for size/mtime alone to identify a rapid replacement.
	localRecent bool
}

// packageLookupCall tracks one in-flight shared package lookup and its active waiters.
type packageLookupCall struct {
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int
	abandoned bool
	pkg       nodePackage
	err       error
}

// nodePackages remembers lookups, makes one lookup per platform and kind at a time (the others wait for it) and one
// download per file at a time.
type nodePackages struct {
	mu       sync.Mutex
	results  map[packageKey]packageResult
	looking  map[packageKey]*packageLookupCall
	fetching map[string]*sync.Mutex // by the cache file a download goes to
	scans    map[string]versionScan // carriesVersion's answers, by path
	// httpClient replaces netproxy's client (tests); nil: netproxy.HTTPClient.
	httpClient func(proxy *url.URL, timeout time.Duration) *http.Client
	now        func() time.Time
}

func (p *nodePackages) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// localNodeDirs are where the hub's own node builds are: the node binary dir, then the bundled one.
func localNodeDirs() []string {
	dirs := []string{nodeBinaryDir()}
	if b := identity.NodeBundledDir(); b != "" && b != dirs[0] {
		dirs = append(dirs, b)
	}
	return dirs
}

// safeVersion: a version that can name a directory of the release (dl/<version>/) and of the cache.
var safeVersion = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (d *Devices) packageKey(goos, arch, program string) packageKey {
	return d.editionKey(goos, arch, program, nodeproto.EditionFull)
}

// infoKey is packageKey for the node that reported info: its platform, its kind of program and its edition (a node
// from before editions says none and is a full build).
func (d *Devices) infoKey(info proto.NodeInfo) packageKey {
	edition, _ := nodeproto.ParseEdition(string(info.Edition)) // an unknown word stays "", for which there is no build
	return d.editionKey(info.OS, info.Arch, info.Program, edition)
}

func (d *Devices) editionKey(goos, arch, program string, edition nodeproto.Edition) packageKey {
	cfg := d.config()
	if program != nodeproto.ProgramPersonaNode {
		program = nodeproto.ProgramPersonad // personad, or an older node that does not say
	}
	key := packageKey{goos: goos, arch: arch, program: program, edition: edition, own: d.edition(), hub: cfg.HubVersion, base: cfg.NodePackagesURL, hold: cfg.DevelopmentHold}
	if cfg.DevelopmentTarget != nil {
		key.development = *cfg.DevelopmentTarget
		key.hold = key.hold || !key.development.Valid() || key.development.Version != cfg.HubVersion
	}
	if cfg.PublishedTarget != nil && cfg.PublishedTarget.ValidPublished() && cfg.PublishedTarget.Version == key.hub {
		key.published = *cfg.PublishedTarget
	}
	return key
}

// cachedPackage is the lookup remembered for k, if it is still fresh.
func (d *Devices) cachedPackage(k packageKey) (packageResult, bool) {
	r, fresh, ok := d.rememberedPackage(k, false)
	return r, ok && fresh
}

// rememberedPackage is the lookup remembered for k however old (ok), and whether it is still fresh. A result that
// stands in for the site (stale) or says the site is away is looked up again after a minute; what the site said,
// found or not, after ten. allowRecent is only for a caller that already waited on this lookup generation; later,
// independent cache decisions keep it false.
func (d *Devices) rememberedPackage(k packageKey, allowRecent bool) (r packageResult, fresh, ok bool) {
	p := &d.packages
	local, localRecent := p.localStamp(k)
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok = p.results[k]
	if !ok {
		return r, false, false
	}
	ttl := packageFoundTTL
	if r.pkg.stale || (r.err != nil && siteAway(r.err)) {
		ttl = packageMissTTL
	}
	return r, p.clock().Sub(r.at) <= ttl && (allowRecent || !localRecent && !r.localRecent) && r.local == local, true
}

func (d *Devices) rememberLocked(k packageKey, pkg nodePackage, err error, local string, localRecent bool) {
	p := &d.packages
	if p.results == nil {
		p.results = map[packageKey]packageResult{}
	}
	p.results[k] = packageResult{pkg: pkg, err: err, at: p.clock(), local: local, localRecent: localRecent}
}

// standIn marks pkg as standing in for what the site would say (stale): remembered for a minute only.
func standIn(pkg nodePackage) nodePackage {
	pkg.stale = true
	return pkg
}

// lastFound is the build last found for k, however long ago, while the release site does not answer: what the node
// should run stays that (a node on the fallback build does not turn out of date because the site is away).
func (d *Devices) lastFound(k packageKey) (nodePackage, bool) {
	p := &d.packages
	p.mu.Lock()
	defer p.mu.Unlock()
	r, ok := p.results[k]
	if !ok || r.err != nil {
		return nodePackage{}, false
	}
	pkg := r.pkg
	pkg.stale = true
	return pkg, true
}

// nodePackageFor looks the build up (manifests over the network when the hub has none of its own), and remembers it.
// edition is the edition this manager was built as (Options.Edition; the full build when nothing says).
func (d *Devices) edition() nodeproto.Edition { return d.opts.Edition.OrFull() }

func (d *Devices) nodePackageFor(ctx context.Context, goos, arch, program string) (nodePackage, error) {
	return d.lookupShared(ctx, d.packageKey(goos, arch, program))
}

// nodePackageForInfo is nodePackageFor for the node that reported info, of its edition (infoKey).
func (d *Devices) nodePackageForInfo(ctx context.Context, info proto.NodeInfo) (nodePackage, error) {
	return d.lookupShared(ctx, d.infoKey(info))
}

// lookupShared looks k up and remembers it, or waits for the lookup of k that is running already and takes its
// result: the machines of one platform make one lookup between them. Canceling one caller detaches only that waiter;
// the underlying lookup is canceled (without caching the cancellation) only if all waiters give up or the manager stops.
func (d *Devices) lookupShared(ctx context.Context, k packageKey) (nodePackage, error) {
	if err := ctx.Err(); err != nil {
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
	}
	deployCtx := d.deployContext()
	p := &d.packages
	p.mu.Lock()
	call, ok := p.looking[k]
	if ok {
		call.waiters++
		p.mu.Unlock()
	} else {
		lctx, lcancel := context.WithTimeout(deployCtx, packageLookupBudget)
		call = &packageLookupCall{done: make(chan struct{}), cancel: lcancel, waiters: 1}
		if p.looking == nil {
			p.looking = map[packageKey]*packageLookupCall{}
		}
		p.looking[k] = call
		p.mu.Unlock()
		go func() {
			defer lcancel()
			var (
				pkg         nodePackage
				err         error
				local       string
				localRecent bool
				completed   bool
			)
			defer func() {
				p.mu.Lock()
				if completed && !call.abandoned && deployCtx.Err() == nil {
					d.rememberLocked(k, pkg, err, local, localRecent)
				}
				if !completed && err == nil {
					err = errors.New("package lookup failed")
				}
				call.pkg, call.err = pkg, err
				if p.looking[k] == call {
					delete(p.looking, k)
				}
				close(call.done)
				p.mu.Unlock()
			}()
			local, localRecent = p.localStamp(k)
			pkg, err = d.lookupPackage(lctx, k)
			completed = true
		}()
	}
	select {
	case <-call.done:
		return call.pkg, call.err
	case <-ctx.Done():
		p.mu.Lock()
		call.waiters--
		if call.waiters == 0 {
			call.abandoned = true
			if p.looking[k] == call {
				delete(p.looking, k)
			}
			call.cancel()
		}
		p.mu.Unlock()
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": ctx.Err().Error()})
	}
}

// lookupSoon looks k up in the background (once at a time) and says the machines may have changed when done.
func (d *Devices) lookupSoon(k packageKey) {
	p := &d.packages
	p.mu.Lock()
	_, running := p.looking[k]
	p.mu.Unlock()
	if running {
		return
	}
	go func() {
		_, _ = d.lookupShared(d.deployContext(), k)
		d.changed()
	}()
}

// nodeTarget is the build the node with info should run: the version of the build its platform resolves to once
// that is known (the hub's own, or the fallback's), the hub's version until then and when there is none. Never
// blocks: an unknown platform is looked up in the background.
func (d *Devices) nodeTarget(info proto.NodeInfo) string {
	hub := d.hubVersion()
	if info.OS == "" || info.Arch == "" {
		return hub
	}
	k := d.infoKey(info)
	r, fresh, ok := d.rememberedPackage(k, false)
	if !fresh {
		d.lookupSoon(k) // what was remembered stands until the lookup lands, so a node does not flip out of date meanwhile
	}
	if !ok || r.err != nil {
		return hub
	}
	return r.pkg.Version
}

// resolvedTargetKnown is nodeTarget once the lookup is done (it may fetch manifests), and whether it is known: false
// when the release site did not answer (and nothing found before stands in), so the hub's version is a guess. A lookup
// that found no build at all leaves the hub's version as the target, which is known.
func (d *Devices) resolvedTargetKnown(ctx context.Context, info proto.NodeInfo) (string, bool) {
	pkg, known := d.resolvedPackageKnown(ctx, info)
	return pkg.Version, known
}

// resolvedPackageKnown is the build behind resolvedTargetKnown: its version and whether it is a fallback. A lookup
// that found no build gives the hub's version, not a fallback.
func (d *Devices) resolvedPackageKnown(ctx context.Context, info proto.NodeInfo) (nodePackage, bool) {
	if info.OS == "" || info.Arch == "" {
		return nodePackage{Version: d.hubVersion()}, true
	}
	k := d.infoKey(info)
	r, ok := d.cachedPackage(k)
	if !ok {
		r.pkg, r.err = d.lookupShared(ctx, k)
	}
	if r.err != nil {
		return nodePackage{Version: d.hubVersion()}, !siteAway(r.err)
	}
	return r.pkg, true
}

// siteAway: err is the release site not answering (or the lookup cut short), not an answer that there is no build.
func siteAway(err error) bool {
	var msg *i18n.Message
	return errors.As(err, &msg) && msg.Key == "devices-deploy.package_unreachable"
}

// packageBlocked is why the node with info cannot be sent a build, as the last lookup found (nil: it can, or it is
// not known yet).
func (d *Devices) packageBlocked(info proto.NodeInfo) error {
	if info.OS == "" || info.Arch == "" {
		return nil
	}
	if r, _, ok := d.rememberedPackage(d.infoKey(info), false); ok {
		return r.err // the last lookup's, also while a newer one runs (nodeTarget starts it)
	}
	return nil
}

// lookupPackage finds k's build (see the order above; the connector's: lookupConnector).
func (d *Devices) lookupPackage(ctx context.Context, k packageKey) (nodePackage, error) {
	if k.hold {
		return nodePackage{}, i18n.Err("devices-deploy.development_hold")
	}
	if k.development.Commit != "" {
		return d.lookupDevelopment(ctx, k)
	}
	if k.program == programConnector {
		return d.lookupConnector(ctx, k)
	}
	if k.goos == "" || k.arch == "" {
		return nodePackage{}, i18n.Err("devices-deploy.os_unknown")
	}
	plat := k.goos + "-" + k.arch
	name := nodeBinaryName(k.program, plat)
	// Resident preflight requires the running program's identity, even if it is installed under another name.
	for _, dir := range localNodeDirs() {
		for _, local := range localNodeNames(k.program, plat) {
			path := update.PackagePath(dir, local)
			if path == "" {
				continue
			}
			version, inferred := k.hub, false
			b, err := os.ReadFile(path + ".version")
			if k.program == nodeproto.ProgramPersonaNode {
				// A dedicated node left by an earlier release is only usable with a matching sidecar.
				if err != nil || version == "" || strings.TrimSpace(string(b)) != version {
					continue
				}
			} else if stamped := strings.TrimSpace(string(b)); err == nil && stamped != "" {
				version = stamped // a packaged personad can be of another build (docs/operations/docker.md "Node builds")
			} else {
				inferred = true
			}
			if err := checkPackagePlatform(path, k.goos, k.arch, k.program); err != nil {
				d.logf("node package for %s: ignore local %s: %v", plat, local, err)
				continue
			}
			// And of the node's edition: a core-only node never gets a full build, nor a full node a core-only one.
			if err := checkPackageEdition(path, k.edition); err != nil {
				d.logf("node package for %s: ignore local %s: %v", plat, local, err)
				continue
			}
			// A build without a version file is taken for the hub's own; one that cannot be it (an older build a hub
			// update or a deploy left in place) would only fail the node's self-check (#12958).
			if inferred {
				if carries, err := d.packages.carriesVersion(path, version); err != nil || !carries {
					d.logf("node package for %s: ignore local %s: it is not build %s (%v)", plat, local, version, err)
					continue
				}
			}
			return nodePackage{Version: version, Program: k.program, goos: k.goos, arch: k.arch, path: path}, nil
		}
	}
	if k.edition != d.edition() {
		// The release this manager reads is of its own edition (update.FeedName): it has no build for a node of the
		// other one, which keeps what it runs until a build of its edition is put in the node binary dir.
		return nodePackage{}, i18n.Err("devices-deploy.other_edition", i18n.Args{"os": k.goos, "arch": k.arch, "name": name,
			"edition": string(cmp.Or(k.edition, "unknown")), "hub": string(d.edition())})
	}
	if k.published.Commit != "" {
		hosted := k
		hosted.development = k.published
		pkg, err := d.lookupDevelopment(ctx, hosted)
		if !errors.Is(err, errNoPublishedPackage) {
			return pkg, err
		}
	}
	if k.base == "" || k.hub == "dev" || !safeVersion.MatchString(k.hub) {
		// Nowhere to fetch from (a hub built without an update URL and no node_packages.url, or "off"; a dev build).
		return nodePackage{}, i18n.Err("devices-deploy.no_binary", i18n.Args{"os": k.goos, "arch": k.arch, "name": name})
	}
	m, err := d.fetchManifest(ctx, k.base+"/"+url.PathEscape(k.hub)+"/"+update.ReleaseManifestName(d.edition()))
	switch {
	case err == nil:
		if f, ok := m.NodeFile(k.program, plat); ok && f.Name == name+".gz" {
			return remotePackage(k, m, k.hub, f, false), nil
		}
	case !errors.Is(err, errNoManifest):
		// The site does not answer: what was fetched or found before stands in for a minute.
		if pkg, ok := cachedRelease(k, k.hub); ok {
			return standIn(pkg), nil
		}
		if pkg, ok := d.lastFound(k); ok {
			return pkg, nil
		}
		if pkg, ok := cachedFallback(k); ok {
			return standIn(pkg), nil
		}
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
	}
	// No build of the hub's version anywhere: the newest release, when this manager drives its node.
	latest, err := d.fetchManifest(ctx, k.base+"/"+update.FeedName(d.edition()))
	if err != nil {
		if errors.Is(err, errNoManifest) {
			return nodePackage{}, i18n.Err("devices-deploy.no_release", i18n.Args{"os": k.goos, "arch": k.arch, "name": name})
		}
		if pkg, ok := d.lastFound(k); ok {
			return pkg, nil
		}
		if pkg, ok := cachedFallback(k); ok {
			return standIn(pkg), nil
		}
		return nodePackage{}, i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": k.goos, "arch": k.arch, "err": err.Error()})
	}
	f, ok := latest.NodeFile(k.program, plat)
	matches := ok && f.Name == name+".gz"
	if matches && latest.Version == k.hub {
		// The newest release is the hub's own version, from a source that keeps no dl/<version>/ (a GitHub
		// repository's releases/latest/download): the release of the hub's version, not a fallback.
		pkg := remotePackage(k, latest, k.hub, f, false)
		pkg.flat = true
		return pkg, nil
	}
	why := releaseNodeIncompatible(latest)
	if why == "" && (!matches || !safeVersion.MatchString(latest.Version)) {
		why = "no build for " + plat
	}
	if why != "" {
		d.logf("node package for %s: the newest release %s does not do (%s)", plat, latest.Version, why)
		return nodePackage{}, i18n.Err("devices-deploy.no_package", i18n.Args{"os": k.goos, "arch": k.arch, "version": latest.Version,
			"name": name})
	}
	pkg := remotePackage(k, latest, latest.Version, f, true)
	pkg.flat = true
	return pkg, nil
}

// releaseNodeIncompatible is why this manager would not drive the node of release m ("" when it would): its node
// protocol range and capabilities, judged as compatOf judges a connected node. A release that does not say
// (published before the fields) is not taken in another build's place.
func releaseNodeIncompatible(m *update.Manifest) string {
	if m.NodeProtocol == 0 {
		return "its node protocol is not known"
	}
	c := compatOf(proto.NodeInfo{Protocol: m.NodeProtocol, ProtocolMin: m.NodeProtocolMin, Capabilities: m.NodeCapabilities})
	switch c.Incompatible {
	case "":
		return ""
	case incompatibleNodeNew:
		return fmt.Sprintf("its node needs a manager of node protocol %d (this one speaks %d)", m.NodeProtocolMin, nodeproto.Version)
	default:
		return fmt.Sprintf("its node speaks node protocol %d (this manager needs %d) or lacks %v", m.NodeProtocol, nodeproto.MinVersion, c.Missing)
	}
}

// nodeBinaryName is a cached or bundled personad for plat. A Windows executable keeps .exe; it must be looked up
// with the same suffix that the release download and hub update retain.
func nodeBinaryName(program, plat string) string {
	return update.PackageName(program, plat)
}

// localNodeNames are the names a build of program for plat may have in a local node dir, the current one first. A
// Windows build left by a hub or a person from before Windows builds kept their .exe has none; it is still taken when
// it is the version it would be sent as (carriesVersion).
func localNodeNames(program, plat string) []string {
	return update.PackageNames(program, plat)
}

// localBuildRecent is the window in which equal-sized replacements may still share an mtime on a coarse filesystem
// clock. Do not reuse a result captured in this window without reading the executable again once. Only small clock skew
// counts as recent; a far-future timestamp must not disable reuse for the file's whole lifetime (#13298, #13326 review).
const localBuildRecent = 2 * time.Second

func localBuildMtimeRecent(now, mod time.Time) bool {
	age := now.Sub(mod)
	return age >= -localBuildRecent && age < localBuildRecent
}

// localStamp is what the local builds k may be taken from look like now: each candidate and its version file, with
// size and modification time ("" when there are none). recent says at least one file is inside localBuildRecent.
func (p *nodePackages) localStamp(k packageKey) (stamp string, recent bool) {
	if k.goos == "" || k.arch == "" {
		return "", false
	}
	var b strings.Builder
	now := p.clock()
	for _, dir := range localNodeDirs() {
		for _, name := range localNodeNames(k.program, k.goos+"-"+k.arch) {
			for _, f := range []string{name, name + ".version"} {
				if st, err := os.Stat(filepath.Join(dir, f)); err == nil {
					fmt.Fprintf(&b, "%s|%d|%d\n", filepath.Join(dir, f), st.Size(), st.ModTime().UnixNano())
					recent = recent || localBuildMtimeRecent(now, st.ModTime())
				}
			}
		}
	}
	return b.String(), recent
}

// versionScan is carriesVersion's answer for a file as it was then.
type versionScan struct {
	size    int64
	mod     time.Time
	version string
	carries bool
	recent  bool // the file was inside localBuildRecent when scanned: confirm it once after that window too
}

// carriesVersion: the build at path can be version. A node installs a build only when its node-update-check prints
// the version it is sent as (internal/nodeupdate), and that is the version the build was linked with, so its bytes are
// in the file. A file without them is another build: one a hub update or a deploy left in place, or one a later step
// of a deploy has not replaced yet. Finding them proves nothing (a short version can occur in any build), so this
// never turns down a build that would pass. Answers are remembered while the file stays the same.
func (p *nodePackages) carriesVersion(path, version string) (bool, error) {
	if version == "" {
		return true, nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	scan, ok := p.scans[path]
	p.mu.Unlock()
	recent := localBuildMtimeRecent(p.clock(), st.ModTime())
	if ok && !recent && !scan.recent && scan.size == st.Size() && scan.mod.Equal(st.ModTime()) && scan.version == version {
		return scan.carries, nil
	}
	carries, err := fileContains(path, []byte(version))
	if err != nil {
		return false, err
	}
	p.mu.Lock()
	if p.scans == nil {
		p.scans = map[string]versionScan{}
	}
	p.scans[path] = versionScan{size: st.Size(), mod: st.ModTime(), version: version, carries: carries, recent: recent}
	p.mu.Unlock()
	return carries, nil
}

// fileContains reads the file at path in chunks, looking for needle (not empty).
func fileContains(path string, needle []byte) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, len(needle)-1+1<<20)
	kept := 0
	for {
		n, err := f.Read(buf[kept:])
		if n > 0 {
			window := buf[:kept+n]
			if bytes.Contains(window, needle) {
				return true, nil
			}
			kept = min(len(needle)-1, len(window))
			copy(buf, window[len(window)-kept:])
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// fileProgram is the kind of program a release download is, by its name.
func fileProgram(f update.File) string {
	switch {
	case strings.HasPrefix(f.Name, nodeproto.ProgramPersonaNode+"-"):
		return nodeproto.ProgramPersonaNode
	case strings.HasPrefix(f.Name, programConnector+"-"):
		return programConnector
	}
	return nodeproto.ProgramPersonad
}

func remotePackage(k packageKey, m *update.Manifest, dir string, f update.File, fallback bool) nodePackage {
	return nodePackage{Version: m.Version, Program: fileProgram(f), Fallback: fallback, goos: k.goos, arch: k.arch, edition: k.own.OrFull(),
		base: k.base, dir: dir, file: f, build: m.Build, nodeProtocol: m.NodeProtocol, nodeProtocolMin: m.NodeProtocolMin,
		nodeCapabilities: m.NodeCapabilities}
}

var errNoManifest = errors.New("no such release")

// siteError is a download that could not be made (as opposed to one that did not check out).
type siteError struct{ error }

func (e siteError) Unwrap() error { return e.error }

// redacted is err without the user and password that the release site's address (node_packages.url) may carry: its
// text goes to the machine's card (deploy_reason) and the log.
func redacted(err error, address string) error {
	u, perr := url.Parse(address)
	if err == nil || perr != nil || u.User == nil {
		return err
	}
	text := err.Error()
	for _, cred := range []string{u.User.String() + "@", u.User.Username() + ":***@", u.User.Username() + "@"} {
		text = strings.ReplaceAll(text, cred, "")
	}
	return errors.New(text)
}

// fetchManifest reads a release manifest of this manager's edition (dl/<version>/manifest.json, dl/latest.json; the
// core edition's core-manifest.json, core-latest.json): errNoManifest when the site has none (404). One of the other
// edition, whatever name it was served under, is an error: none of its builds is sent.
func (d *Devices) fetchManifest(ctx context.Context, u string) (*update.Manifest, error) {
	client, err := d.packageClient(packageLookup)
	if err != nil {
		return nil, err
	}
	m, err := readPackageManifest(ctx, client, u)
	if err != nil {
		return nil, err
	}
	if err := m.ValidateEdition(d.edition()); err != nil {
		return nil, redacted(fmt.Errorf("%s: %w", u, err), u)
	}
	return m, nil
}

func readPackageManifest(ctx context.Context, client *http.Client, u string) (*update.Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, packageLookupBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, redacted(err, u)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, redacted(err, u)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNoManifest
	}
	if resp.StatusCode != http.StatusOK {
		return nil, redacted(fmt.Errorf("%s: HTTP %d", u, resp.StatusCode), u)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(b) > 1<<20 {
		return nil, redacted(fmt.Errorf("%s: larger than 1 MiB", u), u)
	}
	return update.ParseManifest(b)
}

// packageClient is the HTTP client for the release site: through node_packages.proxy, with timeout.
func (d *Devices) packageClient(timeout time.Duration) (*http.Client, error) {
	proxy, err := netproxy.Parse(d.config().NodePackagesProxy)
	if err != nil {
		return nil, fmt.Errorf("node_packages.proxy: %w", err)
	}
	if d.packages.httpClient != nil {
		return d.packages.httpClient(proxy, timeout), nil
	}
	return netproxy.HTTPClient(proxy, timeout), nil
}

// packageCacheEntry is what the cache keeps beside a build it fetched: what it is (written once it checked out).
type packageCacheEntry struct {
	Version      string `json:"version"`
	Program      string `json:"program"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	BinarySHA256 string `json:"binary_sha256"`
	Build        int64  `json:"build,omitempty"`
	// The release's node protocol range and capabilities, for a cached fallback judged again (releaseNodeIncompatible).
	NodeProtocol     int      `json:"node_protocol,omitempty"`
	NodeProtocolMin  int      `json:"node_protocol_min,omitempty"`
	NodeCapabilities []string `json:"node_capabilities,omitempty"`
}

// readCacheEntry returns the cached build for the requested program, OS and architecture.
func readCacheEntry(k packageKey, dir string) (nodePackage, packageCacheEntry, bool) {
	plat := k.goos + "-" + k.arch
	name := nodeBinaryName(k.program, plat)
	if k.program == programConnector {
		name = programConnector + "-" + plat
	}
	bin, entryPath := cachePaths(dir, update.File{Name: name + ".gz"})
	var e packageCacheEntry
	b, err := os.ReadFile(entryPath)
	if err != nil || json.Unmarshal(b, &e) != nil || e.Program != k.program || e.OS != k.goos || e.Arch != k.arch || !fileExists(bin) {
		return nodePackage{}, e, false
	}
	// A cache another edition of the manager filled on this data directory is not this one's.
	if got, ok := update.EditionOf(bin); ok && got != k.own.OrFull() {
		return nodePackage{}, e, false
	}
	return nodePackage{Version: e.Version, Program: e.Program, goos: k.goos, arch: k.arch, path: bin,
		file: update.File{Name: name + ".gz", BinarySHA256: e.BinarySHA256}, build: e.Build, nodeProtocol: e.NodeProtocol,
		nodeProtocolMin: e.NodeProtocolMin, nodeCapabilities: e.NodeCapabilities}, e, true
}

func cachePaths(dir string, f update.File) (bin, entry string) {
	bin = filepath.Join(identity.NodePackageCacheDir(), dir, strings.TrimSuffix(f.Name, ".gz"))
	return bin, bin + ".json"
}

// cachedRelease is a build of the release dir fetched before, for k's platform and kind, when the site does not
// answer now.
func cachedRelease(k packageKey, dir string) (nodePackage, bool) {
	pkg, _, ok := readCacheEntry(k, dir)
	return pkg, ok
}

// cachedFallback is the newest fallback build the cache keeps for k's platform and kind that this manager drives
// (judged again from what the cache kept of its release), when the site does not answer and nothing of the hub's
// version is there: what an earlier lookup found, also after a restart forgot it.
func cachedFallback(k packageKey) (nodePackage, bool) {
	dirs, err := os.ReadDir(identity.NodePackageCacheDir())
	if err != nil {
		return nodePackage{}, false
	}
	var best nodePackage
	found := false
	for _, dir := range dirs {
		if !dir.IsDir() || !safeVersion.MatchString(dir.Name()) || dir.Name() == k.hub {
			continue
		}
		pkg, e, ok := readCacheEntry(k, dir.Name())
		if !ok || e.NodeProtocol == 0 || releaseNodeIncompatible(&update.Manifest{NodeProtocol: e.NodeProtocol,
			NodeProtocolMin: e.NodeProtocolMin, NodeCapabilities: e.NodeCapabilities}) != "" {
			continue
		}
		if !found || pkg.build > best.build {
			best, found = pkg, true
		}
	}
	best.Fallback = found
	return best, found
}

// fetchPackage is p as a file on this machine: its own, the cache's, or downloaded into the cache and checked.
func (d *Devices) fetchPackage(ctx context.Context, p nodePackage) (string, error) {
	if p.development != nil {
		path, _, err := d.fetchDevelopment(ctx, p)
		return path, err
	}
	if p.path != "" {
		if root := identity.NodePackageCacheDir(); strings.HasPrefix(p.path, root+string(filepath.Separator)) {
			now := d.packages.clock() // a cached build used now (a stand-in): pruneCache keeps its dir a while
			_ = os.Chtimes(filepath.Dir(p.path), now, now)
		}
		return p.path, nil
	}
	if p.file.Name == "" {
		return "", i18n.Err("devices-deploy.package_bad", i18n.Args{"os": p.goos, "arch": p.arch, "err": "no build to send"})
	}
	bin, entryPath := cachePaths(p.dir, p.file)
	// One download of a file at a time; other files go on.
	defer d.packages.lockFile(bin)()
	// Used now: pruneCache keeps the dir a while.
	defer func() {
		now := d.packages.clock()
		_ = os.Chtimes(filepath.Dir(bin), now, now)
	}()
	var e packageCacheEntry
	if b, err := os.ReadFile(entryPath); err == nil && json.Unmarshal(b, &e) == nil && e.BinarySHA256 == p.file.BinarySHA256 &&
		e.Version == p.Version && fileExists(bin) {
		return bin, nil
	}
	if err := d.downloadPackage(ctx, p, bin, entryPath); err != nil {
		var away siteError
		if errors.As(err, &away) {
			return "", i18n.Err("devices-deploy.package_unreachable", i18n.Args{"os": p.goos, "arch": p.arch, "err": redacted(err, p.base).Error()})
		}
		var moved *update.MovedError
		if errors.As(err, &moved) {
			return "", i18n.Err("devices-deploy.package_moved", i18n.Args{"os": p.goos, "arch": p.arch, "version": moved.Now.Manifest.Version})
		}
		return "", i18n.Err("devices-deploy.package_bad", i18n.Args{"os": p.goos, "arch": p.arch, "err": redacted(err, p.base).Error()})
	}
	return bin, nil
}

// lockFile takes the download lock of the cache file bin; the function it returns gives it back.
func (p *nodePackages) lockFile(bin string) func() {
	p.mu.Lock()
	if p.fetching == nil {
		p.fetching = map[string]*sync.Mutex{}
	}
	l := p.fetching[bin]
	if l == nil {
		l = &sync.Mutex{}
		p.fetching[bin] = l
	}
	p.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// staleStage is how old a staging directory must be to be someone's interrupted download (a process killed while it
// downloaded, a blue-green switch): longer than any download takes.
const staleStage = 2 * packageDownload

// sweepStages removes the staging directories of interrupted downloads from the cache.
func sweepStages(root string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), ".fetch-") {
			continue
		}
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > staleStage {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// cacheKeep is how long a cache version dir that is not the hub's version stays after its last use: a node of
// another platform may still be sent its build (a lookup is remembered ten minutes; a deployment fetches its build,
// then waits for the computer to be idle).
const cacheKeep = 24 * time.Hour

// pruneCache removes the cache's version dirs that no node is sent any more: all but the hub's version, keep (the
// build just fetched) and those used within cacheKeep (fetchPackage marks a dir used).
func pruneCache(root, hub, keep string, now time.Time) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == hub || e.Name() == keep || !safeVersion.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err != nil || now.Sub(fi.ModTime()) <= cacheKeep {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

func (d *Devices) downloadPackage(ctx context.Context, p nodePackage, bin, entryPath string) error {
	root := identity.NodePackageCacheDir()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return err
	}
	sweepStages(root, d.packages.clock())
	// Staged beside the cache's files (one file system: the rename is whole), never left behind.
	stage, err := os.MkdirTemp(root, ".fetch-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	client, err := d.packageClient(packageDownload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, packageDownload)
	defer cancel()
	var got string
	if p.flat && update.ReleaseBase(p.base, p.Version) != "" {
		// GitHub's moving base would redirect the legacy <version>/<file> path to a duplicate-version URL.
		// Go straight to the release's own tag, retaining DownloadRelease's missing-tag fallback.
		got, err = update.DownloadRelease(ctx, client, p.base, &update.Manifest{Version: p.Version, Build: p.build}, p.file, stage)
	} else {
		got, err = update.Download(ctx, client, p.base+"/"+url.PathEscape(p.dir), p.file, stage) // size, sha256, binary_sha256
		if p.flat && update.IsNotFound(err) {
			// No dl/<version>/ there: the file beside latest.json, checked against the same manifest; a release
			// published since the lookup is said as such (#9263).
			got, err = update.DownloadRelease(ctx, client, p.base, &update.Manifest{Version: p.Version, Build: p.build}, p.file, stage)
		}
	}
	if err != nil {
		var local *fs.PathError
		if update.IsMismatch(err) || errors.As(err, &local) {
			return err // what was served is not the build, or this machine could not keep it
		}
		return siteError{err} // the download could not be made: the site, the network, the proxy
	}
	if err := checkPackageBuild(ctx, got, p); err != nil {
		return err
	}
	if err := os.Chmod(got, 0o755); err != nil {
		return err
	}
	if err := os.Rename(got, bin); err != nil {
		return err
	}
	b, _ := json.Marshal(packageCacheEntry{Version: p.Version, Program: p.Program, OS: p.goos, Arch: p.arch,
		BinarySHA256: p.file.BinarySHA256, Build: p.build, NodeProtocol: p.nodeProtocol, NodeProtocolMin: p.nodeProtocolMin,
		NodeCapabilities: p.nodeCapabilities})
	tmp := entryPath + ".part"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, entryPath); err != nil {
		return err
	}
	pruneCache(root, d.hubVersion(), p.dir, d.packages.clock())
	return nil
}

// checkPackagePlatform: path's Go build info names the requested OS, architecture and kind of program. Unlike a
// downloaded build's full check, a local build is never run here: it may be for another machine's platform.
func checkPackagePlatform(path, goos, goarch, program string) error {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("not a Go executable: %w", err)
	}
	var builtOS, builtArch string
	for _, s := range info.Settings {
		switch s.Key {
		case "GOOS":
			builtOS = s.Value
		case "GOARCH":
			builtArch = s.Value
		}
	}
	if builtOS != goos || builtArch != goarch {
		return fmt.Errorf("built for %s/%s, not %s/%s", builtOS, builtArch, goos, goarch)
	}
	if !strings.HasSuffix(info.Path, "/cmd/"+program) { // the main package: server/cmd/personad, persona-node or persona-link
		return fmt.Errorf("it is %s, not %s", info.Path, program)
	}
	return nil
}

// checkPackageEdition: path is a build of edition want. Read from the Go build info, so nothing is run and any
// platform's build is told; a file without build info was refused by checkPackagePlatform already.
func checkPackageEdition(path string, want nodeproto.Edition) error {
	if want == "" {
		return errors.New("the node names an edition this manager does not know")
	}
	got, ok := update.EditionOf(path)
	if !ok {
		return errors.New("not a Go executable")
	}
	if got != want {
		return fmt.Errorf("it is the %s edition, the node runs the %s edition", got, want)
	}
	return nil
}

// checkPackageBuild: the file is a build for p's OS and architecture of p's kind of program, and on this machine's
// own platform it runs and says the release's version and build.
func checkPackageBuild(ctx context.Context, path string, p nodePackage) error {
	if err := checkPackagePlatform(path, p.goos, p.arch, p.Program); err != nil {
		return err
	}
	// A fetched release build is of this manager's edition (lookupPackage fetches for no other node). A development
	// build names none: the hub built it for the commit it confirmed.
	if p.edition != "" {
		if err := checkPackageEdition(path, p.edition); err != nil {
			return err
		}
	}
	if p.goos == runtime.GOOS && p.arch == runtime.GOARCH {
		if err := os.Chmod(path, 0o755); err != nil {
			return err
		}
		return update.CheckBinary(ctx, path, &update.Manifest{Version: p.Version, Build: p.build, Edition: p.edition.Marker()})
	}
	return nil
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}
