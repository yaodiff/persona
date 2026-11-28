package assistant

import (
	"context"
	"debug/buildinfo"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/managerupdate"
	"github.com/nwverse/persona/server/internal/update"
)

// Only the independent assistant uses this loop. The Hub restarts its own child.
func followDevelopment(ctx context.Context, dir, hostDataDir string, build update.Build, d *devices.Devices, idle Idle, logf func(string, ...any)) *managerupdate.DevelopmentImage {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" || build.Channel == update.Docker {
		return nil
	}
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	var lastError string
	var ready bool
	for {
		t, _ := d.ManagerBuild()
		if t != nil && (t.Version != build.Version || t.Commit != build.Commit || t.Build != build.Number) {
			// The service's own watcher handles an installed replacement. A
			// manager with its own executable can also follow a co-located
			// node that just received this exact build on Redeploy.
			exe, _ := os.Executable()
			exe = strings.TrimSuffix(exe, " (deleted)")
			eligible := func(automatic bool) bool {
				if managerProgramAt(ctx, exe, *t, "personad", "persona-manager") {
					return false
				}
				return automatic || nodeInstalledAt(ctx, hostDataDir, *t)
			}
			image, err := prepareDevelopmentManager(ctx, dir, *t, d, idle, eligible)
			if err != nil && err.Error() != lastError {
				lastError = err.Error()
				logf("development manager update: %v", err)
			}
			if image != nil {
				return image
			}
		} else if t != nil && !ready && d.CheckDevelopmentTarget(ctx, t) == nil {
			if err := managerupdate.DevelopmentReady(dir, build, *t); err != nil {
				logf("development manager readiness: %v", err)
			} else {
				ready = true
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func prepareDevelopmentManager(ctx context.Context, dir string, t devicewire.DevelopmentTarget, d *devices.Devices, idle Idle, local ...func(bool) bool) (*managerupdate.DevelopmentImage, error) {
	tried, err := managerupdate.DevelopmentTried(dir, t)
	if err != nil || tried {
		return nil, err
	}
	allowed := func() bool {
		current, automatic := d.ManagerBuild()
		if current == nil || *current != t || !d.DevelopmentIdle() {
			return false
		}
		if len(local) > 0 {
			return local[0](automatic)
		}
		return automatic
	}
	canStop := func() bool {
		if !allowed() {
			return false
		}
		check, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		ok, _ := idle(check)
		return ok && allowed()
	}
	if !canStop() {
		return nil, nil
	}
	program, err := d.ManagerDevelopmentPackage(ctx, t)
	if err != nil {
		return nil, err
	}
	image, err := managerupdate.StageDevelopment(dir, program, t)
	if err != nil {
		return nil, err
	}
	if !canStop() {
		return nil, nil
	}
	if err := d.CheckDevelopmentTarget(ctx, &t); err != nil {
		return nil, err
	}
	return image, nil
}

func nodeInstalledAt(ctx context.Context, root string, t devicewire.DevelopmentTarget) bool {
	if root == "" {
		return false
	}
	for _, name := range []string{"persona-node", "personad"} {
		if managerProgramAt(ctx, filepath.Join(root, "bin", name), t, name) {
			return true
		}
	}
	return false
}

func managerProgramAt(ctx context.Context, path string, t devicewire.DevelopmentTarget, programs ...string) bool {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return false
	}
	for _, program := range programs {
		if info.Path == "github.com/nwverse/persona/server/cmd/"+program {
			return update.CheckBinary(ctx, path, &update.Manifest{Version: t.Version, Build: t.Build}) == nil
		}
	}
	return false
}
