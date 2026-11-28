package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/update"
)

// SyncLocalNode requests an idle handover from the managed resident node of
// this state root, after the Hub has started serving the verified build.
func SyncLocalNode(ctx context.Context, version string, build int64) error {
	return syncLocalNode(ctx, identity.HomeDir(), systemServices(), &update.Manifest{Version: version, Build: build})
}

func residentProgram(binDir, path string) (string, string) {
	if path == "" || resolvedPath(filepath.Dir(path)) != binDir {
		return "", ""
	}
	switch name := filepath.Base(path); name {
	case "personad", "persona-node":
		// Only the fixed compatibility alias may be a symlink.
		target := filepath.Join(binDir, name)
		if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() {
			program := update.ProgramOf(target)
			if program == name || program == "personad" && update.IsDeviceFile(target) {
				return target, program
			}
		}
	case compat.DaemonBinary:
		target := resolvedPath(path)
		if target == filepath.Join(binDir, "personad") || target == filepath.Join(binDir, compat.DaemonBinary) {
			if info, err := os.Lstat(target); err == nil && info.Mode().IsRegular() && update.ProgramOf(target) == "personad" {
				return target, "personad"
			}
		}
	}
	return "", ""
}

// Only a package whose own checks match the confirmed build can replace a
// local split node. The install never changes a service's program or wrapper.
func prepareResidentTarget(ctx context.Context, path, program string, want *update.Manifest) error {
	if err := verifyResident(ctx, path, program, want); err != nil {
		if !update.IsDeviceFile(path) {
			return err
		}
		dir := identity.NodeBinaryDir()
		name := update.PackageName(program, runtime.GOOS+"-"+runtime.GOARCH)
		pkg := update.PackagePath(dir, name)
		if pkg == "" {
			return err
		}
		version, readErr := os.ReadFile(pkg + ".version")
		if readErr != nil || strings.TrimSpace(string(version)) != want.Version {
			return err
		}
		if err := verifyResident(ctx, pkg, program, want); err != nil {
			return err
		}
		return update.InstallPrepared(path, pkg)
	}
	return nil
}

func verifyResident(ctx context.Context, path, program string, want *update.Manifest) error {
	if update.ProgramOf(path) != program {
		return errors.New("replacement does not match the resident node's program")
	}
	return update.CheckBinary(ctx, path, want)
}
