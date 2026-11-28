package assistantskills

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"github.com/nwverse/persona/server/internal/lazyre"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

// Import limits apply before extraction and to actual bytes read, not just ZIP headers.
const (
	MaxImportZIP      = 4 << 20
	MaxImportExpanded = 16 << 20
	MaxImportFile     = 4 << 20
	MaxImportEntries  = 512
	MaxImportDepth    = 12
	maxManifest       = 64 << 10
	maxImportRatio    = 200
)

// ImportError has a stable, localized-by-the-client reason. Never expose filesystem errors or archive content.
type ImportError struct{ Code string }

func (e *ImportError) Error() string { return e.Code }
func importErr(code string) error    { return &ImportError{Code: code} }

type archiveFile struct {
	name string
	data []byte
	mode os.FileMode
}

type importPackage struct {
	name, description string
	dirs              []string
	files             []archiveFile
	bytes             int
}

var importName = lazyre.New(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func windowsReserved(s string) bool {
	base := strings.ToLower(strings.TrimRight(strings.SplitN(s, ".", 2)[0], " "))
	switch base {
	case "con", "prn", "aux", "nul", "clock$", "conin$", "conout$":
		return true
	}
	suffix := strings.TrimPrefix(strings.TrimPrefix(base, "com"), "lpt")
	return suffix != base && strings.Contains("0123456789¹²³", suffix) && len([]rune(suffix)) == 1
}

func safeImportName(s string) bool {
	return len(s) <= 64 && importName.MatchString(s) && !windowsReserved(s)
}

func archivePath(s string) (string, error) {
	if !utf8.ValidString(s) || len(s) > 1024 || strings.HasPrefix(s, "/") || strings.ContainsAny(s, `\:`) {
		return "", importErr("skill_zip_path")
	}
	name := strings.TrimSuffix(s, "/")
	parts := strings.Split(name, "/")
	if len(parts) > MaxImportDepth {
		return "", importErr("skill_zip_limits")
	}
	for _, part := range parts {
		if part == "" || len(part) > 255 || part == "." || part == ".." || strings.TrimRight(part, ". ") != part || windowsReserved(part) ||
			strings.ContainsAny(part, `<>|"?*`) || strings.IndexFunc(part, unicode.IsControl) >= 0 {
			return "", importErr("skill_zip_path")
		}
	}
	return name, nil
}

// inspectArchive never executes content. It reads each stream once with byte/work limits, checking its CRC.
func inspectArchive(ctx context.Context, data []byte) (importPackage, error) {
	var out importPackage
	if len(data) > MaxImportZIP {
		return out, importErr("skill_zip_limits")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return out, importErr("skill_zip_invalid")
	}
	if len(z.File) > MaxImportEntries {
		return out, importErr("skill_zip_limits")
	}
	// Include implicit directories in the collision index. Case and Unicode normalization must agree on all OSes.
	type entry struct {
		spelling string
		dir      bool
		explicit bool
	}
	seen := map[string]entry{}
	var manifest string
	manifests := 0
	var declared uint64
	for _, f := range z.File {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		name, err := archivePath(f.Name)
		if err != nil {
			return out, err
		}
		mode := f.Mode()
		dir := strings.HasSuffix(f.Name, "/")
		if (dir && (!mode.IsDir() || f.UncompressedSize64 != 0)) || (!dir && !mode.IsRegular()) ||
			mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			return out, importErr("skill_zip_type")
		}
		if f.Flags&1 != 0 || (f.Method != zip.Store && f.Method != zip.Deflate) {
			return out, importErr("skill_zip_invalid")
		}
		parts := strings.Split(name, "/")
		for n := range parts {
			prefix := strings.Join(parts[:n+1], "/")
			isDir := n < len(parts)-1 || dir
			explicit := n == len(parts)-1
			key := cases.Fold().String(norm.NFC.String(prefix))
			if old, ok := seen[key]; ok {
				if old.spelling != prefix || old.dir != isDir || (old.explicit && explicit) {
					return out, importErr("skill_zip_path")
				}
				explicit = explicit || old.explicit
			}
			seen[key] = entry{prefix, isDir, explicit}
		}
		// Internal provisioning stamps are not user package resources.
		if strings.HasPrefix(path.Base(name), ".persona-") || path.Base(name) == legacyStamp {
			return out, importErr("skill_zip_path")
		}
		if strings.EqualFold(path.Base(name), "SKILL.md") {
			manifests++
			if path.Base(name) != "SKILL.md" || len(parts) > 2 || dir {
				return out, importErr("skill_manifest_layout")
			}
			manifest = name
		}
		if f.CompressedSize64 > uint64(len(data)) || f.UncompressedSize64 > MaxImportFile || f.UncompressedSize64 > MaxImportExpanded-declared ||
			f.UncompressedSize64 > f.CompressedSize64*maxImportRatio {
			return out, importErr("skill_zip_limits")
		}
		declared += f.UncompressedSize64
		if dir {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return out, importErr("skill_zip_invalid")
		}
		b, readErr := io.ReadAll(io.LimitReader(&importContextReader{ctx, r}, MaxImportFile+1))
		closeErr := r.Close()
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if len(b) > MaxImportFile || out.bytes+len(b) > MaxImportExpanded {
			return out, importErr("skill_zip_limits")
		}
		if readErr != nil || closeErr != nil || uint64(len(b)) != f.UncompressedSize64 {
			return out, importErr("skill_zip_invalid")
		}
		perm := os.FileMode(0o644)
		if mode.Perm()&0o111 != 0 {
			perm = 0o755
		}
		out.files = append(out.files, archiveFile{name, b, perm})
		out.bytes += len(b)
	}
	if manifests != 1 {
		return out, importErr("skill_manifest_layout")
	}
	wrapper := path.Dir(manifest)
	for _, e := range seen {
		if wrapper != "." && e.spelling != wrapper && !strings.HasPrefix(e.spelling, wrapper+"/") {
			return out, importErr("skill_manifest_layout")
		}
		if e.dir && e.spelling != wrapper {
			out.dirs = append(out.dirs, strings.TrimPrefix(e.spelling, wrapper+"/"))
		}
	}
	for n := range out.files {
		f := &out.files[n]
		if f.name == manifest {
			out.name, out.description, err = importMetadata(f.data)
			if err != nil {
				return out, err
			}
		}
		if wrapper != "." {
			f.name = strings.TrimPrefix(f.name, wrapper+"/")
		}
	}
	return out, nil
}

type importContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *importContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func importMetadata(b []byte) (string, string, error) {
	if !utf8.Valid(b) {
		return "", "", importErr("skill_manifest_utf8")
	}
	if len(b) > maxManifest {
		return "", "", importErr("skill_zip_limits")
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) < 3 || lines[0] != "---" {
		return "", "", importErr("skill_manifest_yaml")
	}
	end := 0
	for n := 1; n < len(lines); n++ {
		if lines[n] == "---" {
			end = n
			break
		}
	}
	if end == 0 {
		return "", "", importErr("skill_manifest_yaml")
	}
	var document yaml.Node
	d := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "\n")))
	if err := d.Decode(&document); err != nil || len(document.Content) != 1 {
		return "", "", importErr("skill_manifest_yaml")
	}
	var extra yaml.Node
	if d.Decode(&extra) != io.EOF || !plainYAML(&document, 0) || document.Content[0].Kind != yaml.MappingNode {
		return "", "", importErr("skill_manifest_yaml")
	}
	values := map[string]*yaml.Node{}
	nodes := document.Content[0].Content
	for n := 0; n < len(nodes); n += 2 {
		values[nodes[n].Value] = nodes[n+1]
	}
	value := func(key string) (string, error) {
		v := values[key]
		if v == nil || v.Kind != yaml.ScalarNode || v.Tag != "!!str" || strings.TrimSpace(v.Value) == "" {
			return "", importErr("skill_manifest_" + key)
		}
		return v.Value, nil
	}
	name, err := value("name")
	if err != nil {
		return "", "", err
	}
	desc, err := value("description")
	if err != nil {
		return "", "", err
	}
	if !safeImportName(name) {
		return "", "", importErr("skill_name_unsafe")
	}
	if len(desc) > 4096 {
		return "", "", importErr("skill_manifest_description")
	}
	return name, strings.TrimSpace(desc), nil
}

func plainYAML(n *yaml.Node, depth int) bool {
	if depth > 20 || n.Kind == yaml.AliasNode || n.Anchor != "" {
		return false
	}
	switch n.Tag {
	case "", "!!map", "!!seq", "!!str", "!!bool", "!!int", "!!float", "!!null", "!!timestamp":
	default:
		return false
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for p := 0; p < len(n.Content); p += 2 {
			k := n.Content[p]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || keys[k.Value] {
				return false
			}
			keys[k.Value] = true
		}
	}
	for _, c := range n.Content {
		if !plainYAML(c, depth+1) {
			return false
		}
	}
	return true
}

func (p importPackage) extract(ctx context.Context, dir string) error {
	for _, name := range p.dirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(name)), 0o755); err != nil {
			return err
		}
	}
	for _, f := range p.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		at := filepath.Join(dir, filepath.FromSlash(f.name))
		if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
			return fmt.Errorf("create staged directory: %w", err)
		}
		// Staging is private and newly created; no package links can be present.
		if err := os.WriteFile(at, f.data, f.mode); err != nil {
			return err
		}
	}
	return ctx.Err()
}
