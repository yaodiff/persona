package devices

import (
	"context"
	"errors"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/update"
)

// ErrNoHelperSource: the hub has nowhere to look for releases (no update source, or node_packages.url "off"); ErrNoHelper:
// the releases it can see have no build of the helper for that platform.
var (
	ErrNoHelperSource = errors.New("no release source to look for helpers in")
	ErrNoHelper       = errors.New("no build of the helper for this platform")
)

// HelperDownload is where a computer's node fetches the helper program name (persona-iphone-bridge,
// docs/extensions/iphone-bridge.md "Releases") for goos/arch, and what the download must be: from the hub's node
// packages source (docs/operations/node-update.md "Node packages"), the release of the hub's version
// (dl/<version>/manifest.json), else the newest (latest.json). The addresses go in the order to try them: a build
// listed in latest.json is fetched from dl/<version>/ first and beside latest.json after, as node builds are. The
// computer downloads it itself, so node_packages.proxy does not apply to it.
func (d *Devices) HelperDownload(ctx context.Context, goos, arch, name string) ([]string, update.File, error) {
	cfg := d.config()
	base := strings.TrimRight(strings.TrimSpace(cfg.NodePackagesURL), "/")
	if base == "" {
		return nil, update.File{}, ErrNoHelperSource
	}
	plat := goos + "-" + arch
	at := func(dir, file string) string {
		if dir == "" {
			return base + "/" + url.PathEscape(file)
		}
		return base + "/" + url.PathEscape(dir) + "/" + url.PathEscape(file)
	}
	if v := cfg.HubVersion; v != "" && v != "dev" && safeVersion.MatchString(v) {
		m, err := d.fetchManifest(ctx, at(v, update.ReleaseManifestName(d.edition())))
		switch {
		case err == nil:
			if f, ok := m.HelperFile(name, plat); ok {
				return []string{at(v, f.Remote())}, f, nil
			}
		case !errors.Is(err, errNoManifest):
			return nil, update.File{}, err
		}
	}
	latest, err := d.fetchManifest(ctx, at("", update.FeedName(d.edition())))
	if errors.Is(err, errNoManifest) {
		return nil, update.File{}, ErrNoHelper
	}
	if err != nil {
		return nil, update.File{}, err
	}
	f, ok := latest.HelperFile(name, plat)
	if !ok {
		return nil, update.File{}, ErrNoHelper
	}
	urls := []string{at("", f.Remote())}
	if safeVersion.MatchString(latest.Version) {
		urls = append([]string{at(latest.Version, f.Remote())}, urls...)
	}
	return urls, f, nil
}
