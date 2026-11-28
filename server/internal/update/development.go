package update

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// DevelopmentConfig is trusted installation configuration, never supplied by an
// HTTP request. The source is always this project's main branch.
type DevelopmentConfig struct {
	Backup DevelopmentBackupConfig `json:"backup"`
}

type DevelopmentBackupConfig struct {
	// Quiesce and Resume are the optional advanced override for installations
	// with writers outside the hub's control. When both are empty the hub
	// pauses its own writers (the built-in child manager, background writes and
	// request writes) for the snapshot, and Builtin is set for the helper.
	Quiesce        []string `json:"quiesce,omitempty"`
	Resume         []string `json:"resume,omitempty"`
	ExtraRoots     []string `json:"extra_roots,omitempty"`
	ExtraDatabases []string `json:"extra_databases,omitempty"`
	// Directory is the snapshot store (an absolute path; empty uses the
	// runtime's update/development-backups) and Retain how many verified
	// snapshots are kept (0 uses the default of three).
	Directory string `json:"directory,omitempty"`
	Retain    int    `json:"retain,omitempty"`
	// Builtin is true when the hub itself paused the writers, so the helper
	// must not run quiesce/resume hooks or its own coordination marker.
	Builtin bool `json:"builtin,omitempty"`
}

// ExternalQuiesce reports whether both quiesce and resume hooks are configured.
// A half-configured pair is refused rather than silently replaced by the
// built-in pause.
func (c DevelopmentBackupConfig) ExternalQuiesce() bool {
	return len(c.Quiesce) > 0 && len(c.Resume) > 0
}

// RequiresExternalQuiesce reports whether the backup configuration includes
// extra roots or extra databases that the hub's built-in pause cannot stop or
// verify.
func (c DevelopmentBackupConfig) RequiresExternalQuiesce() bool {
	return len(c.ExtraDatabases) > 0 || len(c.ExtraRoots) > 0
}

// Coordinated reports whether the configured writer coordination is usable:
// both hooks together, or neither (the built-in pause) when no extra databases
// or extra roots are configured.
func (c DevelopmentBackupConfig) Coordinated() bool {
	if len(c.Quiesce) > 0 || len(c.Resume) > 0 {
		return len(c.Quiesce) > 0 && len(c.Resume) > 0
	}
	return !c.RequiresExternalQuiesce()
}

type DevelopmentCandidate struct {
	Branch    string `json:"branch"`
	Commit    string `json:"commit"`
	UpdatedAt string `json:"updated_at,omitempty"` // Pinned commit's committer date, not check time.
	BackupID  string `json:"backup_id,omitempty"`
}

// DevelopmentVersion describes the programs saved before an update, not its target.
type DevelopmentVersion struct {
	ID        string `json:"id"`
	Version   string `json:"version"`
	Build     int64  `json:"build"`
	Commit    string `json:"commit"`
	UpdatedAt string `json:"updated_at"`
	CreatedAt string `json:"created_at"`
}

type DevelopmentBackup struct {
	ID       string   `json:"id"`
	Retained int      `json:"retained"`
	Pruned   []string `json:"pruned"`
}

// DevelopmentProgress is the latest step in a development phase. Zero counts
// and percentages are meaningful, so unknown metrics are represented by nil.
type DevelopmentProgress struct {
	Phase          string   `json:"phase"`
	Step           string   `json:"step"`
	StepIndex      int      `json:"step_index"`
	StepTotal      int      `json:"step_total"`
	Steps          []string `json:"steps,omitempty"`
	ItemLabel      string   `json:"item_label,omitempty"`
	ItemDone       *int64   `json:"item_done,omitempty"`
	ItemTotal      *int64   `json:"item_total,omitempty"`
	BytesDone      *int64   `json:"bytes_done,omitempty"`
	BytesTotal     *int64   `json:"bytes_total,omitempty"`
	BytesPerSecond *int64   `json:"bytes_per_second,omitempty"`
	Percent        *int64   `json:"percent,omitempty"`
	ElapsedSeconds int64    `json:"elapsed_seconds"`
}

func developmentSteps(operation string) (string, []string) {
	switch operation {
	case "build":
		return "building", []string{"fetch", "npm_install", "web_build", "runtime_packages", "go_build", "manifest"}
	case "preflight":
		return "backing_up", []string{"preflight"}
	case "backup":
		return "backing_up", []string{"preflight", "quiesce", "database_dump", "archive_files", "resume_writers", "archive_programs", "verify_snapshot"}
	case "prepare_backup":
		return "applying_backup", []string{"verify_snapshot", "prepare_artifacts"}
	}
	return "", nil
}

// DevelopmentStep describes a step that can be reported for an operation.
func DevelopmentStep(operation, step string) DevelopmentProgress {
	phase, steps := developmentSteps(operation)
	if i := slices.Index(steps, step); i >= 0 {
		return DevelopmentProgress{Phase: phase, Step: step, StepIndex: i + 1, StepTotal: len(steps), Steps: steps}
	}
	return DevelopmentProgress{}
}

// Progress labels can only name compiled programs and Go's public target
// components. Paths, configuration, filenames and command output are excluded.
var developmentTargetLabel = lazyre.New(`^(personad|persona-(hub|node|manager)) \((aix|android|darwin|dragonfly|freebsd|illumos|ios|js|linux|netbsd|openbsd|plan9|solaris|wasip1|windows)-(386|amd64|arm|arm64|loong64|mips|mipsle|mips64|mips64le|ppc64|ppc64le|riscv64|s390x|wasm)\)$`)

func readDevelopmentProgress(r io.Reader, operation string, report func(DevelopmentProgress)) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 1024), 4096)
	for s.Scan() {
		var p DevelopmentProgress
		if json.Unmarshal(s.Bytes(), &p) != nil {
			continue
		}
		step := DevelopmentStep(operation, p.Step)
		if step.Phase == "" || p.Phase != step.Phase || p.StepIndex != step.StepIndex || p.StepTotal != step.StepTotal ||
			(p.ItemLabel != "" && (p.Step != "go_build" || !developmentTargetLabel.MatchString(p.ItemLabel))) {
			continue
		}
		if p.ElapsedSeconds < 0 || p.Percent != nil && *p.Percent > 100 {
			continue
		}
		valid := true
		for _, count := range []*int64{p.ItemDone, p.ItemTotal, p.BytesDone, p.BytesTotal, p.BytesPerSecond, p.Percent} {
			valid = valid && (count == nil || *count >= 0)
		}
		if valid {
			p.Steps = step.Steps // Only Hub-defined step names reach clients.
			report(p)
		}
	}
	if s.Err() != nil {
		// Keep consuming after an invalid oversized record so it cannot stall the helper.
		_, _ = io.Copy(io.Discard, r)
	}
}

// The adoption fence reserves the attempt, then becomes a write-ahead recovery
// journal before any live program is replaced. Incomplete older fences remain
// fail-closed: their effects cannot be inferred safely.
func DevelopmentAdoptionPath(dir string) string {
	return filepath.Join(dir, "development", "adopting.json")
}

func BeginDevelopmentAdoption(dir string, p Pending) error {
	path := DevelopmentAdoptionPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(p); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// PrepareDevelopmentAdoption follows BeginDevelopmentAdoption and saves every
// preimage before making the complete recovery record durable. Callers must use
// InstallPrepared afterward: Swap would overwrite the saved preimages.
func PrepareDevelopmentAdoption(dir string, p *Pending) error {
	p.Prev = p.Target + ".prev"
	if err := copyFile(p.Target, p.Prev); err != nil {
		return err
	}
	for i := range p.Programs {
		s := &p.Programs[i]
		if _, err := os.Lstat(s.Target); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		s.Prev = s.Target + ".prev"
		if err := copyFile(s.Target, s.Prev); err != nil {
			return err
		}
	}
	p.RecoveryReady = true
	return Save(DevelopmentAdoptionPath(dir), p)
}

// InstallPrepared atomically replaces a program whose preimage is already in
// the durable adoption journal. It never overwrites the saved preimage.
func InstallPrepared(target, newBin string) error { return copyFile(newBin, target) }

// DetachDevelopmentWeb journals the rename intent before moving the override.
func DetachDevelopmentWeb(dir, web string, p *Pending) error {
	if _, err := os.Lstat(web); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	previous := filepath.Join(dir, "development", fmt.Sprintf("web-previous-%d", p.ToBuild))
	if _, err := os.Lstat(previous); !errors.Is(err, os.ErrNotExist) {
		return errors.New("previous Web recovery directory already exists")
	}
	p.Web, p.WebPrev = web, previous
	if err := Save(DevelopmentAdoptionPath(dir), p); err != nil {
		return err
	}
	if err := os.Rename(web, previous); err != nil {
		return err
	}
	syncDir(filepath.Dir(web))
	syncDir(filepath.Dir(previous))
	return nil
}

func ClearDevelopmentAdoption(dir string, p Pending) {
	if p.Development == nil {
		return
	}
	var fence Pending
	path := DevelopmentAdoptionPath(dir)
	if ok, err := Load(path, &fence); err == nil && ok && fence.Development != nil && fence.ToBuild == p.ToBuild && fence.Development.Commit == p.Development.Commit {
		_ = os.Remove(path)
		syncDir(filepath.Dir(path))
	}
}

//go:embed webdev.py
var developmentHelper string

type backupFailure struct {
	Reason   string `json:"reason"`
	ExitCode int    `json:"exit_code,omitempty"`
}

func (backupFailure) Error() string { return "development backup failed" }

// DevelopmentBackupReason returns a catalog lookup code and numeric exit
// status. Only a recognized translation may be shown to the user.
func DevelopmentBackupReason(err error) (string, int) {
	var failure backupFailure
	if errors.As(err, &failure) {
		return failure.Reason, failure.ExitCode
	}
	return "", 0
}

// DevelopmentRequest runs one bounded phase in an owned private directory. Raw
// subprocess output stays in log, since tools/hooks may print private details.
func DevelopmentRequest(ctx context.Context, config *DevelopmentConfig, dir, operation string, input map[string]any, result any, resume func(), report func(DevelopmentProgress)) error {
	if config == nil {
		return errors.New("development updates are not configured")
	}
	if runtime.GOOS == "windows" {
		return errors.New("development builds require a Linux or macOS Hub")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(dir, operation+"-")
	if err != nil {
		return err
	}
	// Build artifacts remain until the shared installer has downloaded them.
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(work)
		}
	}()
	logPath := filepath.Join(dir, operation+".log")
	if operation == "backup" {
		info, err := os.Lstat(logPath)
		if err == nil && info.Size() > 0 {
			if err := os.Rename(logPath, filepath.Join(dir, "backup.prev.log")); err != nil {
				return err
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	if err := log.Chmod(0o600); err != nil {
		return err
	}
	if input == nil {
		input = map[string]any{}
	}
	input["operation"], input["config"], input["work"] = operation, config, work
	input["result"] = filepath.Join(work, "result.json")
	delete(input, "progress_fd")
	var reader, writer *os.File
	if phase, _ := developmentSteps(operation); report != nil && phase != "" {
		reader, writer, err = os.Pipe()
		if err != nil {
			return err
		}
		defer reader.Close()
		defer writer.Close()
		input["progress_fd"] = 3 // the first exec.Cmd.ExtraFiles descriptor
	}
	// Writer coordination is a reliable request/ack pair. Progress events
	// are optional and may be dropped if their pipe fills.
	var extraFiles []*os.File
	if writer != nil {
		extraFiles = append(extraFiles, writer)
	}
	var releaseCapture func()
	delete(input, "capture_fd")
	delete(input, "resumed_fd")
	if operation == "backup" && config.Backup.Builtin && resume != nil {
		captured, notify, err := os.Pipe()
		if err != nil {
			return err
		}
		defer captured.Close()
		defer notify.Close()
		resumed, ack, err := os.Pipe()
		if err != nil {
			return err
		}
		defer resumed.Close()
		defer ack.Close()
		input["capture_fd"], input["resumed_fd"] = 3+len(extraFiles), 4+len(extraFiles)
		extraFiles = append(extraFiles, notify, resumed)
		releaseCapture = func() {
			notify.Close()
			resumed.Close()
			var request [1]byte
			if _, err := io.ReadFull(captured, request[:]); err == nil && request[0] == 'R' {
				resume()
				_, _ = ack.Write(request[:])
			}
			ack.Close() // A helper that lost its acknowledgement cannot publish.
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	limit := 45 * time.Minute
	if operation == "resolve" {
		limit = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-c", developmentHelper)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(data), log, log
	cmd.ExtraFiles = extraFiles
	// Interrupt lets the helper resume writers on cancellation. A killed helper leaves
	// its durable coordination marker; a later attempt fails closed.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	// Allow child-group escalation (6s) plus a fresh 60s resume deadline and
	// its own escalation, with margin for journal syncing and process exit.
	cmd.WaitDelay = 90 * time.Second
	err = cmd.Start()
	if err == nil {
		var drained chan struct{}
		if writer != nil {
			_ = writer.Close()
			drained = make(chan struct{})
			go func() {
				defer close(drained)
				readDevelopmentProgress(reader, operation, report)
			}()
		}
		if releaseCapture != nil {
			releaseCapture()
		}
		err = cmd.Wait()
		if drained != nil {
			<-drained
		}
	}
	if ctx.Err() != nil {
		return ctx.Err() // The helper and its child group have exited before work is removed.
	}
	if err != nil {
		if operation == "backup" || operation == "preflight" {
			var failure backupFailure
			if ok, readErr := Load(filepath.Join(work, "failure.json"), &failure); ok && readErr == nil {
				return failure
			}
		}
		return fmt.Errorf("development %s failed; inspect update/development/%s.log on the Hub", operation, operation)
	}
	data, err = os.ReadFile(filepath.Join(work, "result.json"))
	if err != nil {
		return err
	}
	err = json.Unmarshal(data, result)
	keep = err == nil && (operation == "build" || operation == "prepare_backup")
	return err
}

// DevelopmentNumber keeps the existing rollback marker's monotonic build identity,
// including two commits built in the same minute and an installation with build 0.
func DevelopmentNumber(current int64) int64 {
	now := time.Now().UTC()
	n, _ := strconv.ParseInt(now.Format("200601021504"), 10, 64)
	if n <= current {
		if floor, err := time.Parse("200601021504", strconv.FormatInt(current, 10)); err == nil {
			n, _ = strconv.ParseInt(floor.Add(time.Minute).Format("200601021504"), 10, 64)
		}
	}
	return n
}
