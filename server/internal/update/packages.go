package update

import (
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

var managedPackageName = lazyre.New("^((?:personad|persona-node|persona-manager|persona-link)|" +
	legacyDaemon + ")-([a-z0-9]+)-([a-z0-9]+)(\\.exe)?$")

// PackageName is the installed name for a program and platform.
func PackageName(program, platform string) string {
	name := program + "-" + platform
	if strings.HasPrefix(platform, "windows-") {
		name += ".exe"
	}
	return name
}

// PackageNames lists local package spellings, canonical first. Read pre-suffix
// Windows builds and packages left before the daemon was renamed.
func PackageNames(program, platform string) []string {
	names := []string{PackageName(program, platform)}
	if short, ok := strings.CutSuffix(names[0], ".exe"); ok {
		names = append(names, short)
	}
	if program == "personad" {
		for _, name := range names {
			names = append(names, legacyDaemon+strings.TrimPrefix(name, program))
		}
	}
	return names
}

// PackagePath resolves a package file and thereby its matching version
// sidecar. Only the publisher's exact sibling aliases may be followed.
func PackagePath(dir, name string) string {
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return ""
	}
	if info.Mode().IsRegular() {
		return path
	}
	want, legacy := canonicalPackage(name)
	link, err := os.Readlink(path)
	if !legacy || err != nil || link != want {
		return ""
	}
	path = filepath.Join(dir, want)
	if info, err = os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

func canonicalPackage(name string) (canonical string, legacy bool) {
	parts := managedPackageName.FindStringSubmatch(name)
	if parts == nil {
		return "", false
	}
	if parts[1] == legacyDaemon {
		return "personad" + strings.TrimPrefix(name, legacyDaemon), true
	}
	return name, false
}

// PublishPackages installs the verified canonical binaries in staged, followed
// by relative compatibility symlinks. A symlink at dir is a release publisher's
// pointer into an immutable tree and is left alone.
func PublishPackages(staged, dir string, build Build) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		info, err = os.Lstat(dir)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	if opened, err := root.Stat("."); err != nil || !os.SameFile(info, opened) {
		return errors.New("package directory changed during publication")
	}
	stage, err := os.OpenRoot(staged)
	if err != nil {
		return err
	}
	defer stage.Close()
	entries, err := os.ReadDir(staged)
	if err != nil {
		return err
	}
	copies := make(map[string]string)
	for _, e := range entries {
		name := e.Name()
		canonical, legacy := canonicalPackage(name)
		if e.Type().IsRegular() && canonical != "" && (!legacy || copies[canonical] == "") {
			copies[canonical] = name
		}
	}
	aliases := make(map[string]string)
	m := &Manifest{Version: build.Version, Build: build.Number, Commit: build.Commit}
	for name, source := range copies {
		if tail, daemon := strings.CutPrefix(name, "personad-"); daemon {
			alias := legacyDaemon + "-" + tail
			if ownedPackageAlias(root, alias, name) {
				aliases[alias] = name
			}
		}
		if err := publishPackage(root, stage, name, source, build.Version); err != nil {
			return fmt.Errorf("publish %s: %w", name, err)
		}
		if build.Number > 0 && build.Commit != "" {
			if err := indexPackage(m, stage, name, source); err != nil {
				return err
			}
		}
	}
	for alias, name := range aliases {
		if err := publishAlias(root, alias, name); err != nil {
			return err
		}
		// Old readers can still look for a version beside the alias.
		// A regular companion marker belongs to the migrated package.
		marker := alias + ".version"
		info, err := root.Lstat(marker)
		if errors.Is(err, os.ErrNotExist) || err == nil && info.Mode().IsRegular() {
			if err := publishAlias(root, marker, name+".version"); err != nil {
				return err
			}
		}
	}
	if build.Number > 0 && build.Commit != "" {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if err := publishPackageFile(root, PackageIndex, strings.NewReader(string(b)), 0o644); err != nil {
			return err
		}
	}
	syncDir(dir)
	return nil
}

func publishAlias(root *os.Root, alias, name string) error {
	if found, err := root.Readlink(alias); err == nil && found == name {
		return nil
	}
	tmp := ".package-alias-" + rand.Text()
	if err := root.Symlink(name, tmp); err != nil {
		return err
	}
	defer root.Remove(tmp)
	return root.Rename(tmp, alias)
}

// PackageIndex pins the package bytes to the build that published them.
const PackageIndex = "package-manifest.json"

func indexPackage(m *Manifest, stage *os.Root, name, source string) error {
	f, err := stage.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	artifact := File{Name: name, Size: n, SHA256: sum, BinarySHA256: sum}
	parts := managedPackageName.FindStringSubmatch(name)
	platform := parts[2] + "-" + parts[3]
	switch parts[1] {
	case "personad":
		if m.Files == nil {
			m.Files = make(map[string]File)
		}
		m.Files[platform] = artifact
	case "persona-node":
		putPackage(&m.Nodes, parts[1], platform, artifact)
	case "persona-manager":
		putPackage(&m.Programs, parts[1], platform, artifact)
	case "persona-link":
		if m.Links == nil {
			m.Links = make(map[string]File)
		}
		m.Links[platform] = artifact
	}
	return nil
}

func putPackage(groups *map[string]map[string]File, program, platform string, file File) {
	if *groups == nil {
		*groups = make(map[string]map[string]File)
	}
	if (*groups)[program] == nil {
		(*groups)[program] = make(map[string]File)
	}
	(*groups)[program][platform] = file
}

func publishPackage(root, stage *os.Root, name, source, version string) error {
	in, err := stage.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	for _, entry := range []string{name, name + ".version"} {
		info, err := root.Lstat(entry)
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a regular file", entry)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	marker := name + ".version"
	if b, err := root.ReadFile(marker); err == nil && string(b) == version+"\n" {
		if current, err := root.Open(name); err == nil {
			same := samePackageContents(in, current)
			current.Close()
			if same {
				return nil
			}
		}
	}
	// While the package changes, neither version may identify the other
	// binary. A package left without its marker can be republished.
	if err := root.Remove(marker); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := publishPackageFile(root, name, in, 0o755); err != nil {
		return err
	}
	return publishPackageFile(root, marker, strings.NewReader(version+"\n"), 0o644)
}

func samePackageContents(a, b *os.File) bool {
	left, errA := a.Stat()
	right, errB := b.Stat()
	if errA != nil || errB != nil || !right.Mode().IsRegular() || right.Mode().Perm()&0o111 == 0 || left.Size() != right.Size() {
		return false
	}
	if os.SameFile(left, right) {
		return true
	}
	hash := func(f *os.File, size int64) (string, error) {
		h := sha256.New()
		_, err := io.Copy(h, io.NewSectionReader(f, 0, size))
		return string(h.Sum(nil)), err
	}
	x, errX := hash(a, left.Size())
	y, errY := hash(b, right.Size())
	return errX == nil && errY == nil && x == y
}

func publishPackageFile(root *os.Root, name string, contents io.Reader, mode os.FileMode) error {
	tmp := ".package-" + rand.Text()
	out, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer root.Remove(tmp)
	_, err = io.Copy(out, contents)
	if err == nil {
		err = out.Sync()
	}
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return root.Rename(tmp, name)
}

func ownedPackageAlias(root *os.Root, alias, canonical string) bool {
	info, err := root.Lstat(alias)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		dest, err := root.Readlink(alias)
		return err == nil && dest == canonical
	}
	if !info.Mode().IsRegular() {
		return false
	}
	// Historical self-update copies were identical when published. Later
	// deploys might have replaced the canonical copy; a stale copy still has
	// the Persona command's Go build identity for this platform.
	f, err := root.Open(alias)
	if err != nil {
		return false
	}
	defer f.Close()
	if AliasMatchesPackage(f, canonical) {
		return true
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, io.NewSectionReader(f, 0, info.Size())); err != nil {
		return false
	}
	sum := string(digest.Sum(nil))
	for _, other := range []string{canonical, canonical + ".prev"} {
		fi, err := root.Lstat(other)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() != info.Size() {
			continue
		}
		g, err := root.Open(other)
		if err != nil {
			continue
		}
		digest.Reset()
		_, err = io.Copy(digest, g)
		g.Close()
		if err == nil && string(digest.Sum(nil)) == sum {
			return true
		}
	}
	return false
}

// AliasMatchesPackage identifies a historical daemon copy by its build's main
// package and platform, even if the canonical file no longer has its bytes.
func AliasMatchesPackage(f io.ReaderAt, canonical string) bool {
	parts := managedPackageName.FindStringSubmatch(canonical)
	if parts == nil || parts[1] != "personad" {
		return false
	}
	info, err := buildinfo.Read(f)
	if err != nil || info.Path != "github.com/nwverse/persona/server/cmd/personad" {
		return false
	}
	var goos, goarch string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "GOOS":
			goos = setting.Value
		case "GOARCH":
			goarch = setting.Value
		}
	}
	return goos == parts[2] && goarch == parts[3]
}
