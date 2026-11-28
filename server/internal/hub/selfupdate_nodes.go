package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

func (u *updater) nodesEnabled() bool {
	if u == nil {
		return false // No updater means the persisted deployment policy has not been read.
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded {
		ctx, cancel := context.WithTimeout(context.Background(), updateRequestLimit)
		defer cancel()
		if err := u.loadLocked(ctx); err != nil {
			return false // An unread persisted choice must never enable deployment.
		}
	}
	return u.nodesEnabledLocked()
}

// nodesEnabledLocked is also used by the view while holding the updater lock.
func (u *updater) nodesEnabledLocked() bool {
	if u.settings.AutoUpdateNodes != nil {
		return *u.settings.AutoUpdateNodes
	}
	return u.h.cfg != nil && u.h.cfg.AutoUpdateNodes
}

// stageNodes never fetches or recreates the node package directory unless fleet
// updates are explicitly enabled. Hub-only releases can contain just one platform.
func (u *updater) stageNodesFrom(ctx context.Context, m *update.Manifest, staged, bin, localSource string, development bool, programs map[string]string) (string, error) {
	if !u.nodesEnabled() && !development {
		return "", nil
	}
	nodes := filepath.Join(staged, "nodes")
	if err := os.MkdirAll(nodes, 0o755); err != nil {
		return "", err
	}
	own := runtime.GOOS + "-" + runtime.GOARCH
	for _, pkg := range []struct {
		program string
		files   map[string]update.File
		own     string
	}{
		{"personad", m.Files, bin},
		{"persona-node", m.Nodes["persona-node"], ""},
		{"persona-manager", m.Programs["persona-manager"], programs["persona-manager"]},
		{"persona-link", m.Links, ""},
	} {
		for _, plat := range sortedKeys(pkg.files) {
			name := update.PackageName(pkg.program, plat)
			if filepath.Base(name) != name || strings.ContainsAny(plat, `/\`) {
				return "", fmt.Errorf("invalid platform %q", plat)
			}
			u.set("downloading", name, "")
			dest := filepath.Join(nodes, name)
			if plat == own && pkg.own != "" {
				// Reuse the Hub and Manager binaries already verified on this platform.
				if err := update.InstallPrepared(dest, pkg.own); err != nil {
					return "", err
				}
				continue
			}
			src, err := u.download(ctx, m, pkg.files[plat], nodes, localSource)
			if err != nil {
				return "", err
			}
			if src != dest {
				if err := os.Rename(src, dest); err != nil {
					return "", err
				}
			}
		}
	}
	return nodes, nil
}

// nodeBuildName: a personad build for one platform as bin/nodes holds it. Windows keeps .exe: dropping it would
// leave the downloaded program outside both manager lookup and update retention.
var nodeBuildName = lazyre.New(`^(?:personad|` + regexp.QuoteMeta(compat.DaemonBinary) + `)-[a-z0-9]+-[a-z0-9]+(?:\.exe)?$`)

// setAsideOldNodes renames the personad builds in dir that would pass for the hub's own version and no longer are. The
// manager takes a personad there with no version file beside it for a build of the hub's version
// (internal/devices/packages.go), which stops being true once the hub updates itself without its nodes: Redeploy would
// put the old build back on every computer (#1825). Set aside as <name>.prev, the manager fetches the hub's own build
// from the release instead. A build with a version file says what it is and stays; the directory is never created. It
// returns the names it moved.
func setAsideOldNodes(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var moved []string
	var first error
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !nodeBuildName.MatchString(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path + ".version"); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			first = errors.Join(first, err)
			continue
		}
		if tail, legacy := strings.CutPrefix(name, compat.DaemonBinary+"-"); legacy {
			f, err := os.Open(path)
			if err != nil {
				first = errors.Join(first, err)
				continue
			}
			owned := update.AliasMatchesPackage(f, "personad-"+tail)
			f.Close()
			if !owned {
				continue
			}
		}
		if err := os.Rename(path, path+".prev"); err != nil {
			first = errors.Join(first, err)
			continue
		}
		moved = append(moved, name)
	}
	return moved, first
}

// labelUnstagedNodes preserves omitted backup platforms without advertising them
// as the restored Hub's version. Existing explicit version identities stay intact.
func labelUnstagedNodes(dir, staged, from string) error {
	stagedEntries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, e := range stagedEntries {
		if e.Type().IsRegular() && nodeBuildName.MatchString(e.Name()) {
			present[e.Name()] = true
		}
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !nodeBuildName.MatchString(e.Name()) || present[e.Name()] {
			continue
		}
		marker := filepath.Join(dir, e.Name()+".version")
		if info, err := os.Lstat(marker); err == nil {
			if !info.Mode().IsRegular() {
				return errors.New("retained Node package has an invalid version marker")
			}
			version, err := os.ReadFile(marker)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(version)) == "" {
				return errors.New("retained Node package has an empty version marker")
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := writeRetainedNodeVersion(marker, from); err != nil {
			return err
		}
	}
	return nil
}

func writeRetainedNodeVersion(marker, version string) error {
	f, err := os.CreateTemp(filepath.Dir(marker), ".retained-node-version-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.WriteString(version + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Link atomically only when the destination is still absent.
	if err := os.Link(f.Name(), marker); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	d, err := os.Open(filepath.Dir(marker))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
