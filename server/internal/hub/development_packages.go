package hub

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

// Both the handover and every download consult the installed identity. A check,
// a staged build, or an unresolved adoption can never authorize distribution.
func (u *updater) confirmedDevelopment() (*devicewire.DevelopmentTarget, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.adoptionUnresolved() {
		return nil, true
	}
	if !strings.HasPrefix(u.h.version, "web-dev-") {
		return nil, false
	}
	installed := devicewire.DevelopmentBuild(u.h.build.Commit, u.h.version, u.h.build.Number)
	if !u.confirmed || !u.loaded || !installed.Valid() || slices.Contains(u.settings.DevelopmentBad, installed.Commit) {
		return nil, true
	}
	if receipt := u.settings.ConfirmedDevelopment; receipt != nil && *receipt == installed {
		return &installed, false
	}
	// An earlier Hub wrote history without a separate receipt. A rollback
	// can also restore the previous binary after its successor wrote history.
	for i := len(u.settings.History) - 1; i >= 0; i-- {
		r := u.settings.History[i]
		if r.Source == "web-dev" && r.Commit == installed.Commit && r.ToVersion == installed.Version && r.ToBuild == installed.Build {
			if r.Result == "updated" && r.Reason == "" {
				return &installed, false
			}
			break
		}
	}
	return nil, true
}

func (u *updater) stageDevelopmentPackages(r *update.Published, staged string) error {
	target := devicewire.DevelopmentBuild(r.Manifest.Commit, r.Manifest.Version, r.Manifest.Build)
	if !target.Valid() { // restoring an ordinary release from a backup
		return nil
	}
	source, err := url.Parse(r.Source)
	if err != nil || source.Scheme != "file" {
		return errors.New("development packages require a local build directory")
	}
	if err := update.Save(filepath.Join(source.Path, "manifest.json"), r.Manifest); err != nil {
		return err
	}
	return os.Rename(source.Path, filepath.Join(staged, "packages"))
}

func (u *updater) packageStore() string { return filepath.Join(u.dir, "development-packages") }

func (u *updater) publishDevelopmentPackages(p update.Pending) error {
	t := devicewire.DevelopmentBuild(p.Development.Commit, p.ToVersion, p.ToBuild)
	if !t.Valid() {
		return nil
	}
	src := filepath.Join(u.stagedDir(p.ToBuild), "packages")
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		// A transaction prepared by an older Hub had no hosted packages. The
		// confirmed identity still permits an exact source build on a manager.
		return nil
	}
	root := u.packageStore()
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	dest := filepath.Join(root, t.Version)
	if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(src, dest); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if d.IsDir() && d.Name() != t.Version && d.Name() != p.FromVersion && strings.HasPrefix(d.Name(), "web-dev-") {
			if err := os.RemoveAll(filepath.Join(root, d.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

var developmentArchive = lazyre.New(`^(?:personad|persona-manager|persona-node)-[a-z0-9]+-[a-z0-9]+(?:\.exe)?\.gz$`)

func (u *updater) publishedBuild() *devicewire.DevelopmentTarget {
	if u == nil || u.h.build.Channel != update.Release || !u.isConfirmed() || u.adoptionUnresolved() {
		return nil
	}
	t := devicewire.PublishedBuild(u.h.build.Commit, u.h.version, u.h.build.Number)
	if !t.ValidPublished() {
		return nil
	}
	return &t
}

func (h *Hub) serveDevelopmentPackage(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.busCaller(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if t := h.upd.publishedBuild(); t != nil && t.Commit == r.PathValue("commit") && t.Version == r.PathValue("version") {
		if ident.Role != bus.RoleManager {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		servePublishedPackage(w, r, *t)
		return
	}
	t, _ := h.upd.confirmedDevelopment()
	if t == nil || t.Commit != r.PathValue("commit") || t.Version != r.PathValue("version") {
		// Distinct from a missing file of the current target: no source fallback.
		w.WriteHeader(http.StatusGone)
		return
	}
	w.Header().Set(devicewire.DevelopmentCommitHeader, t.Commit)
	name := r.PathValue("file")
	if name != "manifest.json" && !developmentArchive.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.OpenInRoot(filepath.Join(h.upd.packageStore(), t.Version), name)
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func servePublishedPackage(w http.ResponseWriter, r *http.Request, t devicewire.DevelopmentTarget) {
	w.Header().Set(devicewire.DevelopmentCommitHeader, t.Commit)
	dir := identity.NodeBinaryDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	defer root.Close()
	index, err := root.Open(update.PackageIndex)
	managed := false
	if errors.Is(err, os.ErrNotExist) {
		// The private release publisher has its own immutable directory.
		// Its manifest pins the unpacked binaries without a second index.
		if info, statErr := os.Lstat(dir); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			index, err = root.Open("manifest.json")
			managed = true
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	defer index.Close()
	data, err := io.ReadAll(io.LimitReader(index, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	m, err := update.ParseManifest(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if m.Version != t.Version || m.Build != t.Build || m.Commit != t.Commit {
		http.NotFound(w, r)
		return
	}
	if managed {
		useInstalledArtifacts(root, m.Files, "personad")
		useInstalledArtifacts(root, m.Nodes["persona-node"], "persona-node")
		useInstalledArtifacts(root, m.Programs["persona-manager"], "persona-manager")
		if len(m.Files) == 0 {
			http.NotFound(w, r)
			return
		}
		data, err = json.Marshal(m)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
	}
	name := r.PathValue("file")
	if name == "manifest.json" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
		return
	}
	if !developmentArchive.MatchString(name+".gz") || !listedPackageFile(m, name) {
		http.NotFound(w, r)
		return
	}
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	f, err := root.Open(name)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if opened, err := f.Stat(); err != nil || !os.SameFile(info, opened) {
		w.WriteHeader(http.StatusConflict)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), f)
}

func useInstalledArtifacts(root *os.Root, files map[string]update.File, program string) {
	for platform, f := range files {
		name := update.PackageName(program, platform)
		if !developmentArchive.MatchString(name+".gz") || f.BinarySHA256 == "" {
			delete(files, platform)
			continue
		}
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			delete(files, platform)
			continue
		}
		files[platform] = update.File{Name: name, Size: info.Size(), SHA256: f.BinarySHA256, BinarySHA256: f.BinarySHA256}
	}
}

func listedPackageFile(m *update.Manifest, name string) bool {
	for _, group := range []map[string]update.File{m.Files, m.Nodes["persona-node"], m.Programs["persona-manager"]} {
		for _, f := range group {
			if f.Name == name {
				return true
			}
		}
	}
	return false
}
