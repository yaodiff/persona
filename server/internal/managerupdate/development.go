package managerupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/update"
)

// Each independent manager uses private images. Its service entry point may
// also run the Hub or another manager, so it stays the known-good launcher.
type DevelopmentImage struct {
	Target devicewire.DevelopmentTarget `json:"target"`
	SHA256 string                       `json:"sha256"`
}

func (i *DevelopmentImage) Error() string { return "restart manager on " + i.Target.Version }

func developmentDir(dir string) string { return filepath.Join(dir, "update", "development-manager") }

func (i *DevelopmentImage) binary(dir string) string {
	return filepath.Join(developmentDir(dir), i.Target.Version, "persona-manager")
}

func (i *DevelopmentImage) check(dir string) error {
	if !validManagerBuild(i.Target) || !shaPattern.MatchString(i.SHA256) {
		return errors.New("invalid development manager image")
	}
	sum, err := hashFile(i.binary(dir))
	if err != nil || sum != i.SHA256 {
		return errors.New("development manager image checksum mismatch")
	}
	return nil
}

// StageDevelopment stores the verified binary separately from the node cache,
// whose normal pruning must never remove the last working manager.
func StageDevelopment(dir, binary string, t devicewire.DevelopmentTarget) (*DevelopmentImage, error) {
	if !validManagerBuild(t) {
		return nil, errors.New("invalid development manager target")
	}
	sum, err := hashFile(binary)
	if err != nil {
		return nil, err
	}
	image := &DevelopmentImage{Target: t, SHA256: sum}
	dest := image.binary(dir)
	if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return nil, err
	}
	if image.check(dir) == nil {
		return image, nil
	}
	if err := update.InstallPrepared(dest, binary); err != nil {
		return nil, err
	}
	return image, image.check(dir)
}

type developmentState struct {
	Launcher string            `json:"launcher"`
	Current  *DevelopmentImage `json:"current,omitempty"`
	Previous *DevelopmentImage `json:"previous,omitempty"`
	// Set before launch. An interrupted/failed trial cannot request itself again.
	Tried string `json:"tried,omitempty"`
}

func developmentStatePath(dir string) string { return filepath.Join(developmentDir(dir), "state.json") }

func DevelopmentTried(dir string, t devicewire.DevelopmentTarget) (bool, error) {
	var s developmentState
	_, err := update.Load(developmentStatePath(dir), &s)
	return s.Tried == t.Version, err
}

const workerEnv = "PERSONA_DEVELOPMENT_MANAGER_WORKER"

// ErrReloadLauncher is returned after a worker drains and asks its
// supervisor to follow a replacement of the installed launcher.
var ErrReloadLauncher = errors.New("manager launcher was replaced")

func validManagerBuild(t devicewire.DevelopmentTarget) bool { return t.Valid() || t.ValidPublished() }

func workerDirectory(dir string, build update.Build) string {
	work := os.Getenv(workerEnv)
	name := filepath.Base(work)
	if filepath.Dir(work) != developmentDir(dir) || !strings.HasPrefix(name, "worker-") || !runPattern.MatchString(name) {
		return ""
	}
	image := DevelopmentImage{Target: devicewire.DevelopmentBuild(build.Commit, build.Version, build.Number)}
	if !image.Target.Valid() {
		image.Target = devicewire.PublishedBuild(build.Commit, build.Version, build.Number)
	}
	if !validManagerBuild(image.Target) {
		return ""
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	running, err := os.Stat(exe)
	expected, statErr := os.Stat(image.binary(dir))
	if err != nil || statErr != nil || !os.SameFile(running, expected) {
		return ""
	}
	return work
}

// IsUpdateWorker reports whether this process was started by the manager's
// update supervisor. The supervisor still holds the service's lock.
func IsUpdateWorker(dir string, build update.Build) bool { return workerDirectory(dir, build) != "" }

// Called after stores opened, the bus handshake succeeded, and the Hub still
// confirms the running image. Only the trial's parent can promote it.
func DevelopmentReady(dir string, build update.Build, t devicewire.DevelopmentTarget) error {
	work := workerDirectory(dir, build)
	if work == "" || !validManagerBuild(t) || build.Commit != t.Commit || build.Version != t.Version || build.Number != t.Build {
		return nil
	}
	return update.Save(filepath.Join(work, "ready.json"), t)
}

// RunDevelopment normally runs the installed manager in process. Once it asks
// for a development image, this process supervises the trial. Only a readiness
// receipt advances Current. Losing the supervisor during a trial therefore
// leaves service restart pointed at the previous binary.
func RunDevelopment(ctx context.Context, dir string, build update.Build, log io.Writer, run func(context.Context) error, args ...string) error {
	if work := workerDirectory(dir, build); work != "" {
		// The supervisor holds the write end of stdin. Its abrupt exit must
		// also stop this process, including during an unconfirmed trial.
		ctx, stop := context.WithCancel(ctx)
		defer stop()
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
		var next *DevelopmentImage
		err := run(ctx)
		if errors.Is(err, ErrReloadLauncher) {
			return update.Save(filepath.Join(work, "reload.json"), true)
		}
		if errors.As(err, &next) {
			return update.Save(filepath.Join(work, "next.json"), next)
		}
		return err
	}
	if len(args) == 0 {
		args = []string{"manager", "run", "--dir", dir}
	}
	// The manager points PERSONA_HOME at its own data during run. A worker
	// must still start with the supervisor's original host environment.
	env := os.Environ()
	statePath := developmentStatePath(dir)
	var s developmentState
	found, err := update.Load(statePath, &s)
	if err != nil {
		return err
	}
	launcher := fmt.Sprintf("%s/%d/%s", build.Version, build.Number, build.Commit)
	if !found || s.Launcher != launcher {
		s = developmentState{Launcher: launcher}
	}
	var next *DevelopmentImage
	if s.Current == nil {
		err := run(ctx)
		if !errors.As(err, &next) {
			return err
		}
	}
	unlock, err := acquire(filepath.Join(developmentDir(dir), "run.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// A concurrent service start may have completed an update since the
	// original process first read the state, before it became idle.
	if found, err = update.Load(statePath, &s); err != nil {
		return err
	}
	if !found || s.Launcher != launcher {
		s = developmentState{Launcher: launcher}
	}
	if next != nil && (s.Tried == next.Target.Version || s.Current != nil && s.Current.Target == next.Target) {
		next = nil
	}
	for ctx.Err() == nil {
		trial := next != nil
		image := s.Current
		if trial {
			image, next = next, nil
			s.Tried = image.Target.Version
			if err := update.Save(statePath, s); err != nil {
				return err
			}
		} else if image == nil {
			err := run(ctx)
			if !errors.As(err, &next) {
				return err
			}
			continue
		}
		next, err = runDevelopmentImage(ctx, dir, image, log, trial, func() error {
			before := s
			s.Previous, s.Current, s.Tried = s.Current, image, ""
			if err := update.Save(statePath, s); err != nil {
				s = before
				return err
			}
			if old := before.Previous; old != nil && validManagerBuild(old.Target) && old.Target != image.Target && (before.Current == nil || old.Target != before.Current.Target) {
				_ = os.RemoveAll(filepath.Dir(old.binary(dir)))
			}
			return nil
		}, env, args)
		if ctx.Err() != nil {
			break
		}
		if errors.Is(err, ErrReloadLauncher) {
			return err
		}
		if trial && s.Tried == image.Target.Version {
			fmt.Fprintf(log, "manager update: %s failed startup, returning to the previous program: %v\n", image.Target.Version, err)
			next = nil
			continue
		}
		if err != nil && s.Current != nil && s.Current.Target == image.Target {
			// An already promoted image can later disappear, be damaged, or fail to restart.
			s.Tried, s.Current, s.Previous = image.Target.Version, s.Previous, nil
			if saveErr := update.Save(statePath, s); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			fmt.Fprintf(log, "manager update: returning to the previous program after %s exited: %v\n", image.Target.Version, err)
			next = nil
			continue
		}
		if err != nil || next == nil {
			return err
		}
	}
	return ctx.Err()
}

func runDevelopmentImage(ctx context.Context, dir string, image *DevelopmentImage, log io.Writer, trial bool, promote func() error, env, args []string) (*DevelopmentImage, error) {
	if err := image.check(dir); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(developmentDir(dir), "worker-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	child, stop := context.WithCancel(ctx)
	defer stop()
	cmd := exec.CommandContext(child, image.binary(dir), args...)
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer writer.Close()
	cmd.Stdin = reader
	cmd.Env = append(env, workerEnv+"="+work)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		reader.Close()
		return nil, err
	}
	reader.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if trial {
		ready := func() bool {
			var target devicewire.DevelopmentTarget
			found, err := update.Load(filepath.Join(work, "ready.json"), &target)
			return found && err == nil && target == image.Target
		}
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		timeout := time.NewTimer(90 * time.Second)
		defer timeout.Stop()
		for !ready() {
			select {
			case err := <-done:
				return nil, errors.Join(errors.New("manager exited before readiness"), err)
			case <-ctx.Done():
				stop()
				<-done
				return nil, ctx.Err()
			case <-timeout.C:
				stop()
				<-done
				return nil, errors.New("manager startup timed out")
			case <-tick.C:
			}
		}
		if err := promote(); err != nil {
			stop()
			<-done
			return nil, err
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	var reload bool
	if found, err := update.Load(filepath.Join(work, "reload.json"), &reload); found && reload && err == nil {
		return nil, ErrReloadLauncher
	}
	var next DevelopmentImage
	found, err := update.Load(filepath.Join(work, "next.json"), &next)
	if !found || err != nil {
		return nil, err
	}
	return &next, next.check(dir)
}
