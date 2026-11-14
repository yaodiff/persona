package node

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// New folders, text files written from the phone and zips (#5828, #5829; docs/protocol/protocol.md "Changing files"):
// the same writes the file page already makes (only the home, checked by writeguard.go), never replacing a name that is
// taken, and a text file replaced only when it is still the version the app opened (its revision, or the same bytes).

// maxWriteBytes caps file.write: the editor opens text files up to 1 MB; the rest is room for a longer save.
const maxWriteBytes = 4 << 20

// The most a zip takes: as many files and bytes (before compression) as a folder upload may bring.
const (
	maxZipFiles = 20000
	maxZipBytes = proto.MaxDirUploadBytes
)

// zipTempMaxAge: a zip made for a download is removed once it is this old (the next zip and the node's start look).
const zipTempMaxAge = time.Hour

type fileMakeParams struct {
	Path        string   `json:"path"`
	Paths       []string `json:"paths"`
	Cwd         string   `json:"cwd"`
	Dir         string   `json:"dir"`
	Name        string   `json:"name"`
	Text        *string  `json:"text"`
	Create      bool     `json:"create"`
	Revision    string   `json:"revision"`
	SHA256      string   `json:"sha256"`
	Force       bool     `json:"force"`
	Conflict    string   `json:"conflict"`
	OperationID string   `json:"operation_id"`
	Mode        *int     `json:"mode"`
}

func (n *Node) fileMake(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p fileMakeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case "file.mkdir":
		path, err := n.makeDir(p.Dir, p.Cwd, p.Name)
		if err != nil {
			return nil, err
		}
		return map[string]any{"path": path}, nil
	case "file.write":
		return n.writeText(p)
	case "file.zip":
		if p.OperationID == "" {
			return n.zipPaths(ctx, p.Paths, p.Cwd, p.Dir, p.Name, nil)
		}
		return n.runFileOperation(p.OperationID, "zip", func(report *fileOperationReporter) (any, error) {
			return n.zipPaths(ctx, p.Paths, p.Cwd, p.Dir, p.Name, report)
		})
	case "file.copy": // filecopy.go
		if p.OperationID == "" {
			return n.copyFiles(ctx, p.Paths, p.Cwd, p.Dir, p.Conflict, nil)
		}
		return n.runFileOperation(p.OperationID, "copy", func(report *fileOperationReporter) (any, error) {
			return n.copyFiles(ctx, p.Paths, p.Cwd, p.Dir, p.Conflict, report)
		})
	case "file.operation":
		op, ok := n.fileOps.get(p.OperationID)
		if !ok {
			return nil, &rpc.Error{Code: proto.CodeNotFound, Message: "file operation not found"}
		}
		return op, nil
	case "file.chmod": // #5831
		return n.changeMode(p.Path, p.Cwd, p.Mode)
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// makeDir makes folder name in dir; a taken name is CodeExists.
func (n *Node) makeDir(dir, cwd, name string) (string, error) {
	if err := checkName(name); err != nil {
		return "", err
	}
	shown, real, err := n.writableDir(dir, cwd)
	if err != nil {
		return "", err
	}
	target := filepath.Join(real, name)
	if err := n.writable(target); err != nil {
		return "", err
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", errExists(name)
		}
		return "", err
	}
	return inFolder(shown, name), nil
}

// fileWritten is file.write's answer: the file as it is now, for the next save's check.
type fileWritten struct {
	Path     string    `json:"path"`
	Size     int64     `json:"size"`
	Revision string    `json:"revision"`
	ModTime  time.Time `json:"mtime"`
}

// errChanged: the file is not the version the app opened; nothing was written. data: the file's revision and time now.
func errChanged(st os.FileInfo) error {
	data, _ := json.Marshal(map[string]any{"revision": fileRevision(st), "mtime": st.ModTime()})
	return &rpc.Error{Code: proto.CodeFileChanged, Message: "the file changed on the computer since it was opened", Data: data}
}

// writeText writes a text file: create makes a new one (a taken name is CodeExists); otherwise it replaces one whose
// revision is still p.Revision, or whose bytes still hash to p.SHA256 (touched, not changed); force skips the check.
// A link is written at its target, which keeps its permissions; the bytes go to a file beside it renamed over it, so
// a reader sees the old file or the new one, never half.
func (n *Node) writeText(p fileMakeParams) (*fileWritten, error) {
	if p.Text == nil {
		return nil, errors.New("text is required")
	}
	data := []byte(*p.Text)
	if len(data) > maxWriteBytes {
		return nil, &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("too large to save from here (over %d MB)", maxWriteBytes>>20)}
	}
	if p.Create {
		dir, name := filepath.Split(strings.TrimRight(strings.TrimSpace(p.Path), "/"))
		if err := checkName(name); err != nil {
			return nil, err
		}
		if dir == "" {
			dir = p.Cwd
		}
		shown, real, err := n.writableDir(dir, p.Cwd)
		if err != nil {
			return nil, err
		}
		if err := n.writable(filepath.Join(real, name)); err != nil {
			return nil, err
		}
		path, err := reserveNumbered(real, name, false)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o644); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			_ = os.Remove(path)
			return nil, err
		}
		return written(inFolder(shown, name), path)
	}
	shown := strings.TrimSpace(p.Path)
	asked := n.resolvePath(shown, p.Cwd)
	if asked == "" {
		return nil, errors.New("empty path")
	}
	real, st, err := n.statSafe(asked)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNotFound(shown)
	}
	if err != nil {
		return nil, explainDenied(asked, n.home, err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a file", shown)
	}
	if err := n.writable(real); err != nil {
		return nil, err
	}
	if !p.Force && fileRevision(st) != p.Revision {
		if p.SHA256 == "" || !sameHash(real, p.SHA256) {
			return nil, errChanged(st)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(real), ".persona-write-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // gone after the rename; left only by a failure
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(st.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	// Changed while the bytes were written: leave it.
	if now, err := os.Stat(real); err == nil && !p.Force && !sameFileRead(st, now) {
		return nil, errChanged(now)
	}
	if err := os.Rename(tmp.Name(), real); err != nil {
		return nil, err
	}
	pastRevision(real, st)
	return written(shown, real)
}

// pastRevision moves a new version's mtime past the old one's when the two would share a revision. A coarse clock (a
// Linux file system stamps mtimes at the kernel's tick, HFS+ to the second) can give a save of the same size the
// mtime the opened version had, and an editor still on that version would then overwrite it unasked.
func pastRevision(real string, old os.FileInfo) {
	for _, d := range []time.Duration{time.Microsecond, time.Second} {
		now, err := os.Stat(real)
		if err != nil || fileRevision(now) != fileRevision(old) {
			return
		}
		_ = os.Chtimes(real, time.Time{}, old.ModTime().Add(d)) // zero atime: left as it is
	}
}

func written(shown, real string) (*fileWritten, error) {
	st, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	return &fileWritten{Path: shown, Size: st.Size(), Revision: fileRevision(st), ModTime: st.ModTime()}, nil
}

// sameHash: the file's bytes hash to want (hex SHA-256).
func sameHash(path, want string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxWriteBytes+1)); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want)
}

// fileZipped is file.zip's answer.
type fileZipped struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Files int    `json:"files"`
}

// zipPaths zips paths (files and folders, each under its own name) into dir as name (a taken name gets " 2"…, as
// Finder names a copy), or with no dir into a temporary file for a download (removed after an hour). Links are stored
// as links, never followed; what the read guard refuses (keys, credentials) is left out.
func (n *Node) zipPaths(ctx context.Context, paths []string, cwd, dir, name string, report *fileOperationReporter) (*fileZipped, error) {
	if len(paths) == 0 {
		return nil, errors.New("paths is required")
	}
	if name == "" {
		name = "Archive.zip"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".zip") {
		name += ".zip"
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	type item struct {
		real string
		name string
	}
	items := make([]item, 0, len(paths))
	for _, p := range paths {
		e, err := n.readEntry(p, cwd)
		if err != nil {
			return nil, err
		}
		items = append(items, item{real: e, name: filepath.Base(e)})
	}

	// Count source metadata before writing so progress has a real total and cancellation checks the scan too.
	scanFiles, scanBytes := 0, int64(0)
	scan := func(real string, d fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.offLimitsResolved(real) != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(real)
		if err != nil {
			return nil // gone meanwhile
		}
		if scanFiles++; scanFiles > maxZipFiles {
			return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d files to zip", maxZipFiles)}
		}
		if info.Mode().IsRegular() {
			if scanBytes += info.Size(); scanBytes > maxZipBytes {
				return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d GB to zip", maxZipBytes>>30)}
			}
		}
		return nil
	}
	for _, it := range items {
		info, err := os.Lstat(it.real)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			if err := scan(it.real, nil); err != nil {
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(it.real, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() && path != it.real {
					return filepath.SkipDir
				}
				return err
			}
			return scan(path, d)
		})
		if err != nil {
			return nil, err
		}
	}
	report.totals(scanFiles, scanBytes)

	var outDir, shown string
	if strings.TrimSpace(dir) == "" {
		base := filepath.Join(os.TempDir(), "persona-zips")
		cleanOldZips(base)
		var err error
		if err = os.MkdirAll(base, 0o700); err != nil {
			return nil, err
		}
		if outDir, err = os.MkdirTemp(base, "z-"); err != nil {
			return nil, err
		}
		shown = outDir
	} else {
		var err error
		if shown, outDir, err = n.writableDir(dir, cwd); err != nil {
			return nil, err
		}
	}
	target, err := reserveNumbered(outDir, name, true)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(target)
		}
	}()
	tmp, err := os.CreateTemp(outDir, ".persona-zip-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	zw := zip.NewWriter(tmp)
	files, total := 0, int64(0)
	add := func(real, inZip string, d fs.DirEntry) (err error) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n.offLimitsResolved(real) != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		defer func() {
			if err == nil {
				report.fileDone()
			}
		}()
		info, err := os.Lstat(real)
		if err != nil {
			return nil // gone meanwhile
		}
		if files++; files > maxZipFiles {
			return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d files to zip", maxZipFiles)}
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(inZip)
		switch {
		case info.IsDir():
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(real)
			if err != nil {
				return nil
			}
			w, err := zw.CreateHeader(h)
			if err == nil {
				_, err = io.WriteString(w, target)
			}
			return err
		case !info.Mode().IsRegular():
			return nil // sockets, devices, pipes
		}
		if total += info.Size(); total > maxZipBytes {
			return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d GB to zip", maxZipBytes>>30)}
		}
		h.Method = zip.Deflate
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		f, err := os.Open(real)
		if err != nil {
			return nil // unreadable: left out
		}
		defer f.Close()
		_, err = copyFileContents(ctx, w, f, report)
		return err
	}
	for _, it := range items {
		info, err := os.Lstat(it.real)
		if err != nil {
			_ = tmp.Close()
			return nil, err
		}
		if !info.IsDir() {
			if err := add(it.real, it.name, nil); err != nil {
				_ = tmp.Close()
				return nil, err
			}
			continue
		}
		err = filepath.WalkDir(it.real, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if d != nil && d.IsDir() && path != it.real {
					return filepath.SkipDir
				}
				return err
			}
			rel, _ := filepath.Rel(it.real, path)
			return add(path, filepath.Join(it.name, rel), d)
		})
		if err != nil {
			_ = tmp.Close()
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return nil, err
	}
	ok = true
	st, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	return &fileZipped{Path: filepath.Join(shown, filepath.Base(target)), Name: filepath.Base(target), Size: st.Size(), Files: files}, nil
}

// readEntry is a path to read as itself: its folder's links followed through the read guard, its own name not (a
// link is zipped as a link).
func (n *Node) readEntry(path, cwd string) (string, error) {
	shown := strings.TrimRight(strings.TrimSpace(path), "/")
	abs := n.expandPath(shown, cwd)
	if abs == "" {
		return "", errors.New("empty path")
	}
	abs = absPath(abs)
	parent, err := n.realPath(filepath.Dir(abs))
	if err != nil {
		return "", explainDenied(shown, n.home, err)
	}
	real := filepath.Join(parent, filepath.Base(abs))
	if err := n.offLimitsResolved(real); err != nil {
		return "", err
	}
	if _, err := os.Lstat(real); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", errNotFound(shown)
		}
		return "", explainDenied(shown, n.home, err)
	}
	return real, nil
}

// cleanOldZips removes the download zips older than zipTempMaxAge.
func cleanOldZips(base string) {
	ents, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range ents {
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > zipTempMaxAge {
			_ = os.RemoveAll(filepath.Join(base, e.Name()))
		}
	}
}

// errNoPermissions: Windows has no permission bits for the file page to change.
var errNoPermissions = &rpc.Error{Code: "unsupported", Message: "this computer has no Unix permissions to change"}

// changeMode sets a file's or folder's permission bits (#5831): 0–0777 only (no setuid, setgid or sticky from the phone),
// where the home takes changes; a link changes what it leads to (as chmod does). Answers the bits now.
func (n *Node) changeMode(path, cwd string, mode *int) (map[string]any, error) {
	if runtime.GOOS == "windows" {
		return nil, errNoPermissions
	}
	if mode == nil || *mode < 0 || *mode > 0o777 {
		return nil, errors.New("mode must be from 0 to 0777")
	}
	shown := strings.TrimSpace(path)
	asked := n.resolvePath(shown, cwd)
	if asked == "" {
		return nil, errors.New("empty path")
	}
	real, _, err := n.statSafe(asked)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errNotFound(shown)
	}
	if err != nil {
		return nil, explainDenied(asked, n.home, err)
	}
	if isHome(fold(real), n.home) {
		return nil, errReadOnly(shown)
	}
	if err := n.writable(real); err != nil {
		return nil, err
	}
	if err := os.Chmod(real, fs.FileMode(*mode)); err != nil {
		return nil, err
	}
	st, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	if n.logger != nil {
		n.logger.Printf("permissions changed from the phone: %s %o", real, st.Mode().Perm())
	}
	return map[string]any{"path": shown, "mode": unixMode(st.Mode())}, nil
}
