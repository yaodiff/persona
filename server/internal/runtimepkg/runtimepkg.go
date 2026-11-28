// Package runtimepkg is the code of the runtimes personad keeps up to date itself (docs/integrations/runtimes.md "Auto-update"): a
// runtime's plugin loaded into another program (DSH's), which that program would otherwise only reload by hand. The
// files are packed from integrations/ at build time (go generate) and embedded, so a node carries the plugin of its
// own build; a runtime that says it runs a package with another hash is offered this one (runtime.hello's update) and
// fetches it (runtime.package), and its loader switches to it.
//
// The embedded copy is compressed (Generate): dist/index.json lists each package's parts, a part being a gzipped tar
// of the package's files under one directory of integrations/, so a directory several packages take (runtime-sdk) is
// stored once. A package is unpacked the first time it is loaded and kept; the others stay compressed.
package runtimepkg

//go:generate go run ./gen

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// embeddedDist is the packages this build carries: dist/ as Generate wrote it.
var embeddedDist = &dist{fsys: embedded, root: "dist"}

const (
	// indexFile names each package's parts: {"<package>": ["<part file>", ...]}.
	indexFile = "index.json"
	// partSuffix ends a part's file name, <directory>-<hash of its files>.tar.gz.
	partSuffix = ".tar.gz"
)

// Packages are the packages and the files of integrations/ each is made of (path patterns, `*` only in the last
// segment). The generator packs exactly these; a runtime's loader hashes its own copy by the same list
// (integrations/runtime-sdk/lib/package.js PACKAGES). Keep the two in step: a file one side counts and the other does
// not makes every hello look like another version.
var Packages = map[string][]string{
	"persona-dsh-runtime":         {"dsh/package.json", "dsh/lib/*.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/*.js"},
	"persona-pi-runtime":          {"pi/package.json", "pi/index.js", "pi/companion.mjs", "pi/lib/*.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/*.js"},
	"persona-antigravity-runtime": {"antigravity/package.json", "antigravity/companion.mjs", "antigravity/desktop.mjs", "antigravity/mcp-proxy.mjs", "antigravity/media-guard.mjs", "antigravity/lib/*.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/*.js"},
	"persona-opencode-runtime":    {"opencode/package.json", "opencode/index.js", "opencode/companion.mjs", "opencode/lib/*.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/*.js"},
	// The Paseo bridge is not loaded by another program: the node writes it out and runs it (node/paseobridge.go).
	"persona-paseo-runtime": {"paseo/package.json", "paseo/bridge.mjs", "paseo/lib/*.js", "runtime-sdk/package.json", "runtime-sdk/index.js", "runtime-sdk/lib/*.js"},
}

// ClaudeCodePlugin is the Persona plugin for Claude Code (integrations/claude-code-plugin): no runtime loads it, the
// node writes it out and has Claude Code install it (internal/claudeplugin, docs/operations/link.md "Claude Code
// plugin").
const ClaudeCodePlugin = "persona-claude-code-plugin"

// Assets are packages that are not a runtime's: carried and loaded like Packages, but no runtime loader hashes them, so
// they are not in the loader's list (package.js PACKAGES). A pattern ending in "/**" takes every file under that
// directory (assetSkipped leaves out tests and dependencies).
var Assets = map[string][]string{
	ClaudeCodePlugin: {"claude-code-plugin/**"},
}

// OverrideEnv names a directory whose <package>/ trees replace the embedded ones: end-to-end runs offer a package
// that differs from the runtime's without building another personad.
const OverrideEnv = "PERSONA_RUNTIME_PACKAGES"

// NameRe: a package name.
var NameRe = lazyre.New(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// MaxSize bounds a package (all its files): far above what a runtime's JS is, far below a message the peers refuse.
const MaxSize = 8 << 20

// File is one file of a package, at a slash-separated path relative to the package root.
type File struct {
	Path string
	Data []byte
}

// Package is a package's files and their hash.
type Package struct {
	Name  string
	Hash  string
	Files []File
}

// Hash is a package's identity: sha256 over its files sorted by path, each as "<path>\x00<hex sha256 of the
// content>\n", in hex. The same function as the loader's (package.js packageHash).
func Hash(files []File) string {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		sum := sha256.Sum256(f.Data)
		fmt.Fprintf(h, "%s\x00%s\n", f.Path, hex.EncodeToString(sum[:]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ValidPath: a package file's path is relative, slash-separated, without empty, "." or ".." segments.
func ValidPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || len(p) > 256 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Load is the package this personad offers under name: the override directory's when OverrideEnv is set, else the
// embedded one. nil without an error: this build carries no such package (built without go generate).
func Load(name string) (*Package, error) {
	requested := name
	name = compat.RuntimePackage(name)
	if !NameRe.MatchString(name) {
		return nil, fmt.Errorf("bad package name %q", name)
	}
	if dir := identity.Getenv(OverrideEnv); dir != "" {
		root := filepath.Join(dir, name)
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			root = filepath.Join(dir, compat.LegacyRuntimePackage(name))
			if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
		}
		return read(os.DirFS(root), requested)
	}
	pkg, err := embeddedDist.load(name)
	if pkg == nil || err != nil {
		return nil, err
	}
	copy := *pkg
	copy.Name = requested
	return &copy, nil
}

// dist is a directory Generate wrote. Its index is read on the first load; each package, and each part, is unpacked
// on its own first load and kept, so a node that serves one runtime never inflates another's code.
type dist struct {
	fsys fs.FS
	root string

	once  sync.Once
	err   error
	pkgs  map[string]*distPackage
	parts map[string]*distPart
}

type distPackage struct {
	once  sync.Once
	parts []string
	pkg   *Package
	err   error
}

type distPart struct {
	once  sync.Once
	files []File
	err   error
}

// load is the package name (nil when the index has none), unpacked once.
func (d *dist) load(name string) (*Package, error) {
	d.once.Do(d.readIndex)
	if d.err != nil {
		return nil, d.err
	}
	p := d.pkgs[name]
	if p == nil {
		return nil, nil
	}
	p.once.Do(func() { p.pkg, p.err = d.unpack(name, p.parts) })
	return p.pkg, p.err
}

// readIndex reads index.json. Without one (built without go generate) the directory carries no package.
func (d *dist) readIndex() {
	d.pkgs, d.parts = map[string]*distPackage{}, map[string]*distPart{}
	data, err := fs.ReadFile(d.fsys, path.Join(d.root, indexFile))
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		d.err = err
		return
	}
	var index map[string][]string
	if err := json.Unmarshal(data, &index); err != nil {
		d.err = fmt.Errorf("runtime package index: %w", err)
		return
	}
	for name, parts := range index {
		if !NameRe.MatchString(name) {
			d.err = fmt.Errorf("runtime package index: bad package name %q", name)
			return
		}
		for _, part := range parts {
			if strings.Contains(part, "/") || !strings.HasSuffix(part, partSuffix) || !ValidPath(part) {
				d.err = fmt.Errorf("runtime package index: bad part %q", part)
				return
			}
			if d.parts[part] == nil {
				d.parts[part] = &distPart{}
			}
		}
		d.pkgs[name] = &distPackage{parts: parts}
	}
}

// unpack joins a package's parts: the same files, sorted by path, and the same hash as read gives the tree they came from.
func (d *dist) unpack(name string, parts []string) (*Package, error) {
	p := &Package{Name: name}
	seen := map[string]bool{}
	size := 0
	for _, part := range parts {
		dp := d.parts[part]
		dp.once.Do(func() { dp.files, dp.err = readPart(d.fsys, path.Join(d.root, part)) })
		if dp.err != nil {
			return nil, fmt.Errorf("package %s: %w", name, dp.err)
		}
		for _, f := range dp.files {
			if seen[f.Path] {
				return nil, fmt.Errorf("package %s: %s is in two parts", name, f.Path)
			}
			seen[f.Path] = true
			if size += len(f.Data); size > MaxSize {
				return nil, fmt.Errorf("package %s is over %d bytes", name, MaxSize)
			}
			p.Files = append(p.Files, f)
		}
	}
	if len(p.Files) == 0 {
		return nil, nil
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	p.Hash = Hash(p.Files)
	return p, nil
}

// readPart unpacks one part: a gzipped tar of regular files at valid paths, at most MaxSize bytes in all.
func readPart(fsys fs.FS, name string) ([]File, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	base := path.Base(name)
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	tr := tar.NewReader(zr)
	var files []File
	size := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", base, err)
		}
		if h.Typeflag != tar.TypeReg || !ValidPath(h.Name) {
			return nil, fmt.Errorf("%s: bad entry %q", base, h.Name)
		}
		if h.Size < 0 || h.Size > int64(MaxSize-size) {
			return nil, fmt.Errorf("%s is over %d bytes", base, MaxSize)
		}
		data := make([]byte, h.Size)
		if _, err := io.ReadFull(tr, data); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", base, h.Name, err)
		}
		size += len(data)
		files = append(files, File{Path: h.Name, Data: data})
	}
	// Read the gzip stream to its end, where its checksum is checked.
	if _, err := io.Copy(io.Discard, zr); err != nil {
		return nil, fmt.Errorf("%s: %w", base, err)
	}
	return files, nil
}

// read takes every regular file under fsys as the package's.
func read(fsys fs.FS, name string) (*Package, error) {
	p := &Package{Name: name}
	size := 0
	err := fs.WalkDir(fsys, ".", func(pth string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if !ValidPath(pth) {
			return fmt.Errorf("package %s: bad path %q", name, pth)
		}
		data, err := fs.ReadFile(fsys, pth)
		if err != nil {
			return err
		}
		if size += len(data); size > MaxSize {
			return fmt.Errorf("package %s is over %d bytes", name, MaxSize)
		}
		p.Files = append(p.Files, File{Path: pth, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(p.Files) == 0 {
		return nil, nil
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].Path < p.Files[j].Path })
	p.Hash = Hash(p.Files)
	return p, nil
}

// Generate packs each package's files (Packages and Assets) from src (the integrations directory) into out, replacing what an
// earlier run wrote there, and returns them in pattern order. A package's files under one directory of integrations/
// make a part, a gzipped tar written once however many packages take it; index.json names each package's parts. The
// same files give the same bytes (no times, owners or names in the archives), so builds stay reproducible. A pattern
// that matches no file fails: a renamed file must not leave a package short.
func Generate(src, out string) (map[string][]File, error) {
	if err := clean(out); err != nil {
		return nil, err
	}
	all := allPackages()
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	res := map[string][]File{}
	index := map[string][]string{}
	written := map[string]bool{}
	for _, name := range names {
		var files []File
		for _, pattern := range all[name] {
			matches, err := matchPattern(src, pattern)
			if err != nil {
				return nil, err
			}
			sort.Strings(matches)
			n := 0
			for _, m := range matches {
				st, err := os.Stat(m)
				if err != nil || !st.Mode().IsRegular() {
					continue
				}
				rel, err := filepath.Rel(src, m)
				if err != nil {
					return nil, err
				}
				rel = filepath.ToSlash(rel)
				if !ValidPath(rel) {
					return nil, fmt.Errorf("package %s: bad path %q", name, rel)
				}
				data, err := os.ReadFile(m)
				if err != nil {
					return nil, err
				}
				files = append(files, File{Path: rel, Data: data})
				n++
			}
			if n == 0 {
				return nil, fmt.Errorf("package %s: %s matches no file under %s", name, pattern, src)
			}
		}
		res[name] = files
		for _, group := range byDirectory(files) {
			part := group.dir + "-" + Hash(group.files)[:16] + partSuffix
			if !written[part] {
				data, err := pack(group.files)
				if err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(out, part), data, 0o644); err != nil {
					return nil, err
				}
				written[part] = true
			}
			index[name] = append(index[name], part)
		}
	}
	data, err := json.Marshal(index) // keys sorted: the same bytes every run
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, indexFile), append(data, '\n'), 0o644); err != nil {
		return nil, err
	}
	return res, nil
}

// allPackages is Packages and Assets together.
func allPackages() map[string][]string {
	all := make(map[string][]string, len(Packages)+len(Assets))
	for name, patterns := range Packages {
		all[name] = patterns
	}
	for name, patterns := range Assets {
		all[name] = patterns
	}
	return all
}

// matchPattern is the files under src a package pattern names: a glob (`*` only in the last segment), or with "/**"
// at its end every regular file under that directory but those assetSkipped leaves out, sorted.
func matchPattern(src, pattern string) ([]string, error) {
	dir, recursive := strings.CutSuffix(pattern, "/**")
	if !recursive {
		return filepath.Glob(filepath.Join(src, filepath.FromSlash(pattern)))
	}
	root := filepath.Join(src, filepath.FromSlash(dir))
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return nil // no such directory: matches no file, which Generate refuses
			}
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel != "." && assetSkipped(filepath.ToSlash(rel), d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// assetSkipped: what a "/**" pattern leaves out of a package, at rel (slash-separated, under the pattern's directory):
// dependencies (node_modules), hidden files and directories other than .claude-plugin (a Claude Code plugin's
// manifest), and tests (*.test.*, test/, tests/, evals/).
func assetSkipped(rel string, dir bool) bool {
	base := path.Base(rel)
	switch {
	case base == "node_modules" || base == "test" || base == "tests" || base == "evals":
		return dir
	case strings.HasPrefix(base, "."):
		return base != ".claude-plugin"
	case !dir && strings.Contains(base, ".test."):
		return true
	}
	return false
}

// clean removes what an earlier Generate wrote in out (index.json, the parts, and the per-package trees written before
// parts), so nothing stale is embedded; anything else (dist/README.md) stays.
func clean(out string) error {
	entries, err := os.ReadDir(out)
	if errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(out, 0o755)
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		_, tree := allPackages()[compat.RuntimePackage(n)]
		if n == indexFile || strings.HasSuffix(n, partSuffix) || (tree && e.IsDir()) {
			if err := os.RemoveAll(filepath.Join(out, n)); err != nil {
				return err
			}
		}
	}
	return nil
}

// partFiles is a package's files under one top directory of integrations/.
type partFiles struct {
	dir   string
	files []File
}

// byDirectory groups files by their first path segment, in order of first appearance; a path listed twice is taken once.
func byDirectory(files []File) []partFiles {
	var groups []partFiles
	at := map[string]int{}
	seen := map[string]bool{}
	for _, f := range files {
		if seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		dir, _, _ := strings.Cut(f.Path, "/")
		i, ok := at[dir]
		if !ok {
			i = len(groups)
			at[dir] = i
			groups = append(groups, partFiles{dir: dir})
		}
		groups[i].files = append(groups[i].files, f)
	}
	return groups
}

// pack is files as a gzipped tar in a deterministic order. Every header field that could vary between machines or runs is fixed:
// the tar entries carry mode 0644, time 0 and no owner; the gzip header (left zero) carries no name and time 0.
func pack(files []File) ([]byte, error) {
	sorted := append([]File(nil), files...)
	// Keep small files and their headers together. Write the large files largest first so the following
	// files can reuse more of the 32 KiB gzip window. Paths break ties for reproducibility.
	// The 21 KiB cutoff keeps the current runtime packages within the embedded size budget.
	const largeFile = 21 << 10
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		aSize, bSize := len(a.Data), len(b.Data)
		if aSize == bSize {
			return a.Path < b.Path
		}
		if aSize >= largeFile && bSize >= largeFile {
			return aSize > bSize
		}
		return aSize < bSize
	})
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(zw)
	for _, f := range sorted {
		h := &tar.Header{Typeflag: tar.TypeReg, Name: f.Path, Mode: 0o644, Size: int64(len(f.Data)), ModTime: time.Unix(0, 0)}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
