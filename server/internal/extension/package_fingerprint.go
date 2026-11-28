package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// packageFingerprint binds the preview to every staged path, mode and byte, not merely the permission digest.
// Symlinks may resolve inside the package only; a changed executable, skill or prompt requires another preview.
func packageFingerprint(root string) (string, error) {
	hash := sha256.New()
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		fmt.Fprintf(hash, "%d:%s%o:", len(name), name, info.Mode())
		switch {
		case entry.Type()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(path)
			if err != nil {
				return err
			}
			targetRel, err := filepath.Rel(resolvedRoot, resolved)
			targetRelSlash := filepath.ToSlash(targetRel)
			if err != nil || (targetRelSlash != "." && !inside(targetRelSlash)) {
				return fmt.Errorf("包里的链接 %s 指向包外", rel)
			}
			fmt.Fprintf(hash, "%d:", len(target))
			_, err = io.WriteString(hash, target)
			if err != nil {
				return err
			}
		case info.IsDir():
			_, _ = io.WriteString(hash, "0:")
		case info.Mode().IsRegular():
			fmt.Fprintf(hash, "%d:", info.Size())
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, err = io.Copy(hash, file)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("包里的 %s 不是普通文件、目录或内部链接", rel)
		}
		_, err = hash.Write([]byte{0})
		return err
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
