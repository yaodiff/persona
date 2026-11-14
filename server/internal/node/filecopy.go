package node

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Copying files and folders from the phone (#5830, docs/protocol/protocol.md "Changing files"): into a folder the home
// takes (writeguard.go), named as moveFiles names a move (a taken name refuses the whole copy before anything is copied,
// keep makes "name 2.ext", replace puts the one there in the trash first and never replaces a folder). Folders are copied
// whole, links as links (never followed), permissions kept; what the read guard refuses (keys, credentials) is left out.
// Duplicate is a copy into the same folder with keep.

func (n *Node) copyFiles(ctx context.Context, paths []string, cwd, dir, conflict string, report *fileOperationReporter) (map[string]any, error) {
	switch conflict {
	case "", proto.UploadConflictReplace, proto.UploadConflictKeep:
	default:
		return nil, fmt.Errorf("unknown conflict %q", conflict)
	}
	if len(paths) == 0 {
		return nil, errors.New("no paths")
	}
	_, realDir, err := n.writableDir(dir, cwd)
	if err != nil {
		return nil, err
	}
	shownDir := strings.TrimSpace(dir)
	if s := strings.TrimRight(shownDir, "/"); s != "" {
		shownDir = s
	}
	type source struct {
		shown, real string
		info        os.FileInfo
	}
	sources := make([]source, 0, len(paths))
	names := map[string]bool{}
	for _, p := range paths {
		real, err := n.readEntry(p, cwd)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(real)
		if err != nil {
			return nil, err
		}
		if info.IsDir() && (realDir == real || strings.HasPrefix(realDir, real+string(filepath.Separator))) {
			return nil, fmt.Errorf("%s can't be copied into itself", p)
		}
		name := filepath.Base(real)
		if names[fold(name)] && conflict != proto.UploadConflictKeep {
			return nil, errExists(name)
		}
		names[fold(name)] = true
		if _, err := os.Lstat(filepath.Join(realDir, name)); err == nil && conflict == "" {
			return nil, errExists(name)
		}
		sources = append(sources, source{shown: strings.TrimSpace(p), real: real, info: info})
	}
	// Everything counted first: a copy too big is refused before a byte is written.
	files, total := 0, int64(0)
	for _, s := range sources {
		err := filepath.WalkDir(s.real, func(path string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if d != nil && d.IsDir() && path != s.real {
					return filepath.SkipDir
				}
				return err
			}
			if n.offLimitsResolved(path) != nil {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if files++; files > maxZipFiles {
				return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d files to copy", maxZipFiles)}
			}
			if d.Type().IsRegular() {
				if info, err := d.Info(); err == nil {
					if total += info.Size(); total > maxZipBytes {
						return &rpc.Error{Code: proto.CodeTooLarge, Message: fmt.Sprintf("more than %d GB to copy", maxZipBytes>>30)}
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	report.totals(files, total)
	copied := make([]map[string]string, 0, len(sources))
	for _, s := range sources {
		final, err := n.copyOne(ctx, s.real, s.info, realDir, conflict, report)
		if err != nil {
			return nil, err
		}
		copied = append(copied, map[string]string{"from": s.shown, "to": inFolder(shownDir, final)})
	}
	if n.logger != nil {
		n.logger.Printf("copied from the phone: %d into %s", len(copied), realDir)
	}
	return map[string]any{"copied": copied}, nil
}

// copyOne copies real into dir under its name, or as conflict says when the name is taken; the name it got.
func (n *Node) copyOne(ctx context.Context, real string, info os.FileInfo, dir, conflict string, report *fileOperationReporter) (string, error) {
	name := filepath.Base(real)
	to := filepath.Join(dir, name)
	if err := n.writable(to); err != nil {
		return "", err
	}
	var replaced string // the trash id of an existing destination, put back if this copy fails
	if st, err := os.Lstat(to); err == nil {
		switch conflict {
		case proto.UploadConflictReplace:
			if st.IsDir() || info.IsDir() || to == real {
				return "", errExists(name)
			}
			replaced, err = n.toTrash(to, to)
			if err != nil {
				return "", err
			}
		case proto.UploadConflictKeep:
			stem, ext := splitName(name)
			if info.IsDir() {
				stem, ext = name, ""
			}
			for i := 2; ; i++ {
				if i >= 1000 {
					return "", errExists(name)
				}
				name = fmt.Sprintf("%s %d%s", stem, i, ext)
				to = filepath.Join(dir, name)
				if _, err := os.Lstat(to); errors.Is(err, fs.ErrNotExist) {
					break
				}
			}
		default:
			return "", errExists(name)
		}
	}
	if err := n.copyTree(ctx, real, to, report); err != nil {
		_ = os.RemoveAll(to) // what this copy made (the name was free: made with O_EXCL / Mkdir)
		if replaced != "" {
			// A failed or cancelled replace must not lose the file that was already there (#6113).
			if _, rerr := n.restoreFiles([]string{replaced}); rerr != nil {
				return "", fmt.Errorf("%w; %s is in the trash and could not be put back: %v", err, name, rerr)
			}
		}
		if errors.Is(err, fs.ErrExist) {
			return "", errExists(name)
		}
		return "", err
	}
	return name, nil
}

// copyTree copies from to the free name to: a file with its permissions, a folder whole, a link as a link.
func (n *Node) copyTree(ctx context.Context, from, to string, report *fileOperationReporter) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(from)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(from)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, to); err != nil {
			return err
		}
		report.fileDone()
		return nil
	case info.IsDir():
		if err := os.Mkdir(to, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		ents, err := os.ReadDir(from)
		if err != nil {
			return err
		}
		for _, e := range ents {
			src := filepath.Join(from, e.Name())
			if n.offLimitsResolved(src) != nil {
				continue
			}
			if err := n.copyTree(ctx, src, filepath.Join(to, e.Name()), report); err != nil {
				return err
			}
		}
		if err := os.Chmod(to, info.Mode().Perm()); err != nil {
			return err
		}
		report.fileDone()
		return nil
	case info.Mode().IsRegular():
		src, err := os.Open(from)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm()|0o600)
		if err != nil {
			return err
		}
		if _, err := copyFileContents(ctx, dst, src, report); err != nil {
			_ = dst.Close()
			return err
		}
		if err := dst.Chmod(info.Mode().Perm()); err != nil {
			_ = dst.Close()
			return err
		}
		if err := dst.Close(); err != nil {
			return err
		}
		report.fileDone()
		return nil
	}
	report.fileDone()
	return nil // sockets, devices, pipes: left out
}
