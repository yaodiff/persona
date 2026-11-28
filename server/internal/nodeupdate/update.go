// Package nodeupdate owns one resident process's executable update transaction.
// Uploads survive RPC reconnects; only the resident process may enable updates.
package nodeupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

const (
	MaxSize        = 256 << 20
	MaxChunk       = proto.UploadChunkBytes
	UploadTTL      = 10 * time.Minute
	ConfirmTimeout = 3 * time.Minute
)

// Check is emitted by node-update-check, without opening sockets or loading node state.
type Check struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// Program is which program the executable is (nodeproto.ProgramPersonad / ProgramPersonaNode): scripts ask it
	// before they replace or run one (the ssh Redeploy script, install.sh, persona-link node switch). Builds from before
	// it leave it out (personad).
	Program string `json:"program,omitempty"`
	// ResidentHandover says this image can consume a same-PID listener/run-lock descriptor ticket after exec.
	ResidentHandover bool `json:"resident_handover,omitempty"`
	// AdoptionMarker says startup understands a confirmation record for an executable installed externally.
	AdoptionMarker bool `json:"adoption_marker,omitempty"`
}

type upload struct {
	ID               string
	Spec             proto.NodeUpdateBeginParams
	Path             string
	Offset           int64
	At               time.Time
	Accepted         bool
	Restarted        bool
	HandoverArmed    bool
	PreflightErr     error
	AdoptionDeadline time.Time
}

// Options says how the resident node is run.
type Options struct {
	// Supervised declares that something other than systemd or launchd starts this process again whenever it exits:
	// a container's entry script, supervisord, runit, s6 (`node --attach --supervised`). An update still replaces the
	// program in place (exec, same process); the supervisor is what brings the node back after a crash or after the
	// rollback watchdog stops a new version that never confirmed. That watchdog runs detached in a session of its
	// own, as under launchd, so the supervisor must restart only the node and leave other processes alone.
	Supervised bool
	// RequireResidentHandover rejects uploaded legacy images before installation, not after arming their watchdog.
	RequireResidentHandover bool
	// Signer signs each build before it runs here (nodesign: a Mac whose node has a code signing certificate set up,
	// docs/operations/mac-node-signing.md); nil installs builds as they come.
	Signer Signer
	// Logf is where signing says what it did, and why a build went on ad hoc; nil is the standard logger (the node's
	// stderr, node-attach.log).
	Logf func(format string, args ...any)
}

// Signer signs builds with this computer's certificate (nodesign.Signer).
type Signer interface {
	// Meets says whether path is signed as configured.
	Meets(ctx context.Context, path string) bool
	// Sign signs path, a file nothing has run yet, with the certificate, and checks the result meets the setting.
	Sign(ctx context.Context, path string) error
	// AdHoc signs path ad hoc under the configured identifier: what an update installs when Sign fails.
	AdHoc(ctx context.Context, path string) error
}

// psNoStart is the reason a declared supervisor's node gives when ps cannot report a process's start time (BusyBox's
// ps, common in containers, has neither -p nor -o lstart). The hub translates it by this original text
// (devices-deploy.node_ps_no_start).
const psNoStart = "ps 读不出进程的启动时间，无法核对回滚进程身份"

// windowsUnsupervised is the reason a Windows node gives when nothing declared it restarts the node: a persona-link
// whose supervisor predates hub updates there, or a node run by hand. The hub translates it by this original text
// (devices-deploy.node_windows_unsupervised).
const windowsUnsupervised = "这台电脑的 Persona Link 后台服务不会替 node 接收更新：在这台电脑上重跑一次 persona-link install"

// Manager must be shared by all of a resident's connection-scoped Nodes.
type Manager struct {
	mu                      sync.Mutex
	exe                     string
	identity                os.FileInfo
	version                 string
	reason                  string
	needsRestart            bool
	upload                  *upload
	restart                 func(string)
	timer                   *time.Timer
	sweep                   *time.Timer
	closed                  atomic.Bool
	requireResidentHandover bool
	signer                  Signer
	logf                    func(format string, args ...any)
	// Tests can shorten deadlines without modifying the production environment.
	timeout      time.Duration
	startWatch   func(string, string, string) error
	drainMu      sync.Mutex
	drainCancel  context.CancelFunc
	drainDone    chan struct{}
	activeDrains atomic.Int32
}

// SetTimeoutForTest overrides the confirmation timeout on a Manager constructed by New.
// Intended for isolated fixture tests; production always uses ConfirmTimeout.
func (m *Manager) SetTimeoutForTest(d time.Duration) {
	if m == nil || d <= 0 {
		return
	}
	m.mu.Lock()
	m.timeout = d
	m.mu.Unlock()
}

func (m *Manager) confirmTimeout() time.Duration {
	if m.timeout > 0 {
		return m.timeout
	}
	return ConfirmTimeout
}

// PendingTarget returns the target version of an accepted upload transaction, or "".
func (m *Manager) PendingTarget(id string) string {
	if m == nil || id == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upload != nil && m.upload.ID == id {
		return m.upload.Spec.Version
	}
	return ""
}

// New resolves the actual running file once. The executable is never inferred
// from HOME, PATH, an attach socket, or the spelling of argv[0]. The restart callback receives the accepted upload ID.
func New(version string, restart func(string), opts Options) *Manager {
	m := &Manager{version: version, restart: restart, timeout: ConfirmTimeout, startWatch: startWatchdog, requireResidentHandover: opts.RequireResidentHandover, signer: opts.Signer, logf: opts.Logf}
	xpc := os.Getenv("XPC_SERVICE_NAME")
	systemd := runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != ""
	launchd := runtime.GOOS == "darwin" && xpc != "" && xpc != "0"
	// A declared supervisor counts only where the detached watchdog works and the node can restart onto the new file:
	// in place (exec) on Linux and macOS, through the supervisor's restart on Windows (Restart).
	declared := opts.Supervised && (runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows")
	if !systemd && !launchd && !declared {
		m.reason = "常驻 node 未由 launchd/systemd 管理，无法保证失败后恢复"
		if runtime.GOOS == "windows" {
			m.reason = windowsUnsupervised
		}
		return m
	}
	// Windows has no ps: processStart reads the creation time through a process handle there.
	if _, err := exec.LookPath("ps"); err != nil && runtime.GOOS != "windows" {
		m.reason = "缺少 ps，无法核对回滚进程身份"
		return m
	}
	switch {
	case systemd:
		if _, err := exec.LookPath("systemd-run"); err != nil {
			m.reason = "缺少 systemd-run，无法启动独立回滚守护"
			return m
		}
	case !launchd:
		// The watchdog tells the node it may stop from a process that later took its pid by the start time ps
		// reports. Ask once here, so a ps that cannot answer shows on the machine instead of failing an update.
		if start, err := processStart(os.Getpid()); err != nil || start == "" {
			m.reason = psNoStart
			return m
		}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		m.reason = "无法定位常驻 node 的实际执行文件"
		return m
	}
	m.exe = exe
	m.identity, err = executableInfo(exe)
	if err != nil {
		m.reason = "常驻 node 的执行文件不可用"
		return m
	}
	if restart == nil {
		m.reason = "常驻 node 未提供进程重启入口"
		return m
	}
	if err := m.recover(); err != nil {
		m.reason = "上次更新尚未安全恢复：" + err.Error()
		return m
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".node-update-probe-*")
	if err != nil {
		m.reason = "常驻 node 的执行文件目录不可写"
		return m
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	m.armSweepLocked()
	return m
}

func (m *Manager) Capability() (bool, string) {
	if m == nil {
		return false, "当前连接使用临时 node，请先安装常驻 node 服务"
	}
	return m.reason == "", m.reason
}

func (m *Manager) available() error {
	if ok, reason := m.Capability(); !ok {
		return errors.New(reason)
	}
	return nil
}

func (m *Manager) Begin(p proto.NodeUpdateBeginParams) (proto.NodeUpdateBeginResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.available(); err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	sum, err := hex.DecodeString(p.SHA256)
	if p.Size <= 0 || p.Size > MaxSize || err != nil || len(sum) != sha256.Size || strings.TrimSpace(p.Version) == "" || len(p.Version) > 256 {
		return proto.NodeUpdateBeginResult{}, errors.New("invalid node executable size, sha256, or version")
	}
	p.SHA256 = strings.ToLower(p.SHA256)
	m.expireLocked()
	if u := m.upload; u != nil {
		if u.Spec == p {
			return proto.NodeUpdateBeginResult{ID: u.ID, Offset: u.Offset}, nil
		}
		return proto.NodeUpdateBeginResult{}, errors.New("another node update is in progress")
	}
	// The startup rollback barrier identifies loaded code by version. Refuse
	// reinstalling different bytes under the running version's same identity.
	if p.Version == m.version {
		return proto.NodeUpdateBeginResult{}, errors.New("node already runs the target version")
	}
	if err := checkPendingSwap(m.exe); err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(m.exe), ".node-update-stage-*"+exeSuffix) // validate runs it
	if err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return proto.NodeUpdateBeginResult{}, err
	}
	m.upload = &upload{ID: hex.EncodeToString(random[:]), Spec: p, Path: path, At: time.Now()}
	m.armExpiryLocked()
	return proto.NodeUpdateBeginResult{ID: m.upload.ID}, nil
}

func (m *Manager) Chunk(p proto.NodeUpdateChunkParams) (proto.NodeUpdateChunkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	u := m.upload
	if u == nil || u.ID != p.ID {
		return proto.NodeUpdateChunkResult{}, errors.New("unknown or expired node upload")
	}
	if u.Accepted {
		return proto.NodeUpdateChunkResult{}, errors.New("node upload already committed")
	}
	if len(p.Data) == 0 || len(p.Data) > MaxChunk || p.Offset < 0 || p.Offset > u.Offset || int64(len(p.Data)) > u.Spec.Size-p.Offset {
		return proto.NodeUpdateChunkResult{}, errors.New("invalid node chunk size or offset")
	}
	f, err := os.OpenFile(u.Path, os.O_RDWR, 0)
	if err != nil {
		return proto.NodeUpdateChunkResult{}, err
	}
	defer f.Close()
	if p.Offset < u.Offset {
		if p.Offset+int64(len(p.Data)) > u.Offset {
			return proto.NodeUpdateChunkResult{}, errors.New("overlapping node chunk")
		}
		prior := make([]byte, len(p.Data))
		if _, err := f.ReadAt(prior, p.Offset); err != nil {
			return proto.NodeUpdateChunkResult{}, err
		}
		if !bytes.Equal(prior, p.Data) {
			return proto.NodeUpdateChunkResult{}, errors.New("retried node chunk differs")
		}
	} else {
		n, err := f.WriteAt(p.Data, p.Offset)
		u.Offset += int64(n)
		if err != nil {
			return proto.NodeUpdateChunkResult{}, err
		}
	}
	u.At = time.Now()
	m.armExpiryLocked()
	return proto.NodeUpdateChunkResult{Offset: u.Offset}, nil
}

// Commit checks the executable and activity before touching the installed file.
// The caller must invoke ResponseWritten only after successfully writing its RPC reply.
func (m *Manager) Commit(ctx context.Context, id string, idle func() error) (proto.NodeUpdateCommitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	u := m.upload
	if u == nil || u.ID != id {
		return proto.NodeUpdateCommitResult{}, errors.New("unknown or expired node upload")
	}
	result := proto.NodeUpdateCommitResult{ID: id, Accepted: true}
	if u.Accepted {
		if u.PreflightErr != nil {
			return proto.NodeUpdateCommitResult{}, u.PreflightErr
		}
		return result, nil
	}
	if u.Offset != u.Spec.Size {
		return proto.NodeUpdateCommitResult{}, errors.New("node executable upload is incomplete")
	}
	checked, err := executableInfo(m.exe)
	if err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	// The hub sends the kind of program the node runs (devices/deploy.go nodeBinary); this keeps a personad that may
	// run a hub or a manager too from becoming persona-node all the same.
	if err := update.CheckReplace(m.exe, u.Path); err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	check, err := validateReplacement(ctx, u.Path, u.Spec, m.requireResidentHandover)
	if err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	// What is installed: with a Signer, a signed copy of the upload. From here on the transaction's confirmation and
	// rollback compare the installed program with that file's sha256, not the upload's (the hub compares versions).
	stage, newHash := m.signedStage(ctx, u)
	defer dropCopy(stage, u.Path)
	if err := ctx.Err(); err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	err = locked(m.exe, func() error {
		if err := checkPendingSwap(m.exe); err != nil {
			return err
		}
		current, err := executableInfo(m.exe)
		if err != nil {
			return err
		}
		// Validate the program kind again if another installer changed the file during staging.
		if !sameFileState(checked, current) {
			if err := update.CheckReplace(m.exe, u.Path); err != nil {
				return err
			}
		}
		oldHash, err := fileHash(m.exe)
		if err != nil {
			return err
		}
		if err := checkFileState(m.exe, current); err != nil {
			return err
		}
		drifted := m.identity != nil && !os.SameFile(m.identity, current)
		if drifted && (oldHash == newHash || oldHash == u.Spec.SHA256) {
			return m.adoptInstalled(ctx, u, newHash, oldHash, current, check.AdoptionMarker, idle)
		}
		backup := backupPath(m.exe, id)
		if err := copyFile(m.exe, backup); err != nil {
			return err
		}
		from := m.version
		if drifted {
			// Startup releases the rollback barrier when the restored version runs.
			// The backup can hold a different version from this still-running node.
			from, err = rollbackVersion(ctx, backup, oldHash, u.Spec.Version)
			if err != nil {
				_ = os.Remove(backup)
				return err
			}
		}
		birth, err := processStart(os.Getpid())
		if err != nil {
			_ = os.Remove(backup)
			return err
		}
		p := pending{ID: id, Exe: m.exe, Backup: backup, Stage: stage, Version: u.Spec.Version, From: from, OldHash: oldHash, NewHash: newHash, PID: os.Getpid(), ProcessStart: birth, ProcessPrecise: osproc.Identify(os.Getpid()).Start, Deadline: time.Now().Add(m.timeout)}
		p.SupervisorPID, p.SupervisorStart = supervisorOf()
		if err := savePending(p); err != nil {
			_ = os.Remove(backup)
			return err
		}
		if err := m.startWatch(backup, markerPath(m.exe), id); err != nil {
			_ = os.Remove(markerPath(m.exe))
			_ = os.Remove(backup)
			return fmt.Errorf("start node rollback watchdog: %w", err)
		}
		// Recheck after self-check, backup, and watchdog startup: any of them can
		// take long enough for an agent to become active again.
		if err := checkCommitIdle(ctx, idle); err != nil {
			_ = cleanupPendingOnly(p)
			return err
		}
		// The directory lock orders Persona's own writers, but an external
		// deployment (a plain copy, a package manager) never takes it. The swap
		// lands only while the installed file is still the one this transaction
		// examined, checked through one descriptor right before the rename.
		err = checkFileState(m.exe, current)
		if err == nil {
			err = renameStill(stage, m.exe, oldHash)
		}
		if err != nil {
			_ = cleanupPendingOnly(p)
			return err
		}
		if stage != u.Path {
			// Its signed copy is installed; the upload stayed as it came only for a retried commit.
			_ = os.Remove(u.Path)
		}
		// Once renamed, keep the marker and backup even if directory fsync fails:
		// the watchdog can still restore the old executable.
		return syncDir(filepath.Dir(m.exe))
	})
	if err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	u.Accepted = true
	if m.timer != nil {
		m.timer.Stop()
	}
	return result, nil
}

func rollbackVersion(ctx context.Context, backup, oldHash, targetVersion string) (string, error) {
	sum, err := fileHash(backup)
	if err != nil {
		return "", err
	}
	if sum != oldHash {
		return "", errSuperseded
	}
	check, err := runCheck(ctx, backup)
	if err != nil {
		return "", fmt.Errorf("check node rollback backup: %w", err)
	}
	if strings.TrimSpace(check.Version) == "" || check.Version == targetVersion {
		return "", errors.New("node rollback backup must report a version distinct from the target")
	}
	return check.Version, nil
}

// adoptInstalled records confirmation for a target already installed by another updater.
// Matching the checked stage also verifies the bytes that passed its platform and self-checks.
func (m *Manager) adoptInstalled(ctx context.Context, u *upload, newHash, sum string, current os.FileInfo, adoptionMarker bool, idle func() error) error {
	// An exact upload can already meet the configured signature. Legacy targets
	// also need that signature when configured so their startup leaves the file untouched.
	if sum != newHash || (m.signer != nil && !adoptionMarker) {
		if m.signer == nil || !m.signer.Meets(ctx, m.exe) {
			return errSuperseded
		}
	}
	if err := checkCommitIdle(ctx, idle); err != nil {
		return err
	}
	// The Hub can replace the file during verification or the idle check.
	if err := checkFileState(m.exe, current); err != nil {
		return err
	}
	deadline := time.Now().Add(m.timeout)
	if adoptionMarker {
		p := pending{ID: u.ID, Exe: m.exe, Stage: u.Path, Version: u.Spec.Version,
			NewHash: sum, Adopted: true, Deadline: deadline}
		if err := savePending(p); err != nil {
			return err
		}
	} else {
		// A legacy target confirms by running version. Clear any earlier adoption
		// record, which that target would read as an invalid rollback marker.
		if err := os.Remove(markerPath(m.exe)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_ = os.Remove(u.Path)
	u.AdoptionDeadline = deadline
	return nil
}

func checkCommitIdle(ctx context.Context, idle func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if idle != nil {
		if err := idle(); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// ResponseWritten signals the resident to reload this update, at most once, after the acceptance
// was written to the transport. Failed writes leave installed swaps to the watchdog.
func (m *Manager) ResponseWritten(id string) {
	m.mu.Lock()
	u := m.upload
	if u == nil || u.ID != id || !u.Accepted || u.Restarted {
		m.mu.Unlock()
		return
	}
	u.Restarted = true
	restart := m.restart
	m.mu.Unlock()
	restart(id)
}

// BeginDrain marks an accepted update transaction as draining active work prior to handover/exec and
// keeps the marker deadline alive while the resident process drains. This prevents both current watchdogs
// (via Draining) and older backup watchdogs (via sliding Deadline) from rolling back a legitimate long drain,
// while preserving #15708 adoption semantics without ever creating a backup or watchdog for adopted binaries.
func (m *Manager) BeginDrain(id string) error {
	if m == nil || id == "" {
		return nil
	}
	m.drainMu.Lock()
	defer m.drainMu.Unlock()
	m.mu.Lock()
	if m.closed.Load() || m.upload == nil || m.upload.ID != id || !m.upload.Accepted || m.upload.HandoverArmed {
		m.mu.Unlock()
		return nil
	}
	exe := m.exe
	timeout := m.confirmTimeout()
	adopted := !m.upload.AdoptionDeadline.IsZero()
	if adopted {
		m.upload.AdoptionDeadline = time.Now().Add(timeout)
	}
	m.mu.Unlock()
	m.stopDrainLocked()
	if exe == "" {
		return nil
	}
	if err := locked(exe, func() error {
		if m.closed.Load() {
			return nil
		}
		p, err := loadPending(markerPath(exe))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.ID != id {
			return nil
		}
		if !p.Adopted {
			p.Draining = true
		}
		p.Deadline = time.Now().Add(timeout)
		return savePending(p)
	}); err != nil {
		return err
	}
	interval := min(max(timeout/3, 25*time.Millisecond), 30*time.Second)
	dctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.mu.Lock()
	if m.closed.Load() || m.upload == nil || m.upload.ID != id || !m.upload.Accepted || m.upload.HandoverArmed {
		m.mu.Unlock()
		cancel()
		return nil
	}
	m.drainCancel, m.drainDone = cancel, done
	m.activeDrains.Add(1)
	m.mu.Unlock()
	go func() {
		defer close(done)
		defer m.activeDrains.Add(-1)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-dctx.Done():
				return
			case <-ticker.C:
			}
			if adopted {
				m.mu.Lock()
				if m.upload != nil && m.upload.ID == id && !m.upload.AdoptionDeadline.IsZero() {
					m.upload.AdoptionDeadline = time.Now().Add(timeout)
				}
				m.mu.Unlock()
			}
			keep := true
			_ = locked(exe, func() error {
				p, err := loadPending(markerPath(exe))
				if err != nil || p.ID != id || (!p.Draining && !p.Adopted) {
					keep = false
					return nil
				}
				p.Deadline = time.Now().Add(timeout)
				return savePending(p)
			})
			if !keep && !adopted {
				return
			}
		}
	}()
	return nil
}

func (m *Manager) stopDrain() {
	if m == nil {
		return
	}
	m.drainMu.Lock()
	defer m.drainMu.Unlock()
	m.stopDrainLocked()
}

func (m *Manager) stopDrainLocked() {
	m.mu.Lock()
	cancel, done := m.drainCancel, m.drainDone
	m.drainCancel, m.drainDone = nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// ArmHandover ends pre-exec draining and anchors the rollback confirmation deadline to handover/exec.
// For adopted installations (#15708), it refreshes the adoption confirmation deadline without touching rollback state.
func (m *Manager) ArmHandover(id string) error {
	if m == nil || id == "" {
		return nil
	}
	m.drainMu.Lock()
	defer m.drainMu.Unlock()
	m.mu.Lock()
	if m.upload != nil && m.upload.ID != id {
		m.mu.Unlock()
		return nil
	}
	exe := m.exe
	timeout := m.confirmTimeout()
	if u := m.upload; u != nil && u.ID == id {
		u.HandoverArmed = true
		if !u.AdoptionDeadline.IsZero() {
			u.AdoptionDeadline = time.Now().Add(timeout)
		}
	}
	m.mu.Unlock()
	m.stopDrainLocked()
	if exe == "" {
		return nil
	}
	return locked(exe, func() error {
		p, err := loadPending(markerPath(exe))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.ID != id {
			return nil
		}
		p.Draining = false
		p.Deadline = time.Now().Add(timeout)
		return savePending(p)
	})
}

// PreflightFailed records why a committed reload could not proceed so a repeated commit returns the same error.
// The installed file and transaction marker still belong to the rollback watchdog, whose deadline is armed now.
func (m *Manager) PreflightFailed(id string, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if u := m.upload; u != nil && u.ID == id && u.Restarted {
		u.PreflightErr = err
	}
	m.mu.Unlock()
	_ = m.ArmHandover(id)
}

func (m *Manager) Abort(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upload == nil || m.upload.ID != id {
		return nil
	}
	if m.upload.Accepted {
		return errors.New("node update already committed")
	}
	m.clearLocked()
	return nil
}

// Confirm is safe on every exact-version reconnect, including after a hub restart.
func (m *Manager) Confirm(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.available(); err != nil {
		return err
	}
	if version != m.version {
		return errors.New("confirmation does not match the running node version")
	}
	return locked(m.exe, func() error {
		p, err := loadPending(markerPath(m.exe))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.Version != version {
			return errors.New("confirmation does not match pending node version")
		}
		if !p.Adopted && !time.Now().Before(p.Deadline) {
			return errors.New("node update confirmation deadline expired")
		}
		sum, err := fileHash(m.exe)
		if err != nil {
			return err
		}
		if sum != p.NewHash {
			return errors.New("running node executable no longer matches update")
		}
		return cleanup(p)
	})
}

// Close removes abandoned uploads; an accepted transaction belongs to the watchdog.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed.Store(true)
	if m.sweep != nil {
		m.sweep.Stop()
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	if m.upload != nil && !m.upload.Accepted {
		m.clearLocked()
	}
	m.mu.Unlock()
	m.drainMu.Lock()
	m.stopDrainLocked()
	m.drainMu.Unlock()
}

func (m *Manager) expireLocked() {
	if u := m.upload; u != nil {
		if (!u.Accepted && time.Since(u.At) >= UploadTTL) ||
			(!u.AdoptionDeadline.IsZero() && !time.Now().Before(u.AdoptionDeadline)) {
			m.clearLocked()
		}
	}
}
func (m *Manager) clearLocked() {
	if m.timer != nil {
		m.timer.Stop()
	}
	if m.upload != nil {
		_ = os.Remove(m.upload.Path)
	}
	m.upload = nil
}
func (m *Manager) armExpiryLocked() {
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = time.AfterFunc(UploadTTL, func() { m.mu.Lock(); defer m.mu.Unlock(); m.expireLocked() })
}

// Sweep uploads left by a crashed process too, even when this resident stays up
// for weeks. Its own active upload is governed by the activity-based TTL.
func (m *Manager) armSweepLocked() {
	m.sweep = time.AfterFunc(UploadTTL, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed.Load() {
			return
		}
		m.expireLocked()
		keep := ""
		if m.upload != nil {
			keep = m.upload.Path
		}
		_ = m.cleanStaleStages(keep)
		m.armSweepLocked()
	})
}

func validate(ctx context.Context, path string, spec proto.NodeUpdateBeginParams) error {
	_, err := validateReplacement(ctx, path, spec, false)
	return err
}

func validateReplacement(ctx context.Context, path string, spec proto.NodeUpdateBeginParams, requireHandover bool) (Check, error) {
	if err := ctx.Err(); err != nil {
		return Check{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Check{}, err
	}
	if st.Size() != spec.Size {
		return Check{}, errors.New("node executable size mismatch")
	}
	sum, err := fileHash(path)
	if err != nil {
		return Check{}, err
	}
	if sum != spec.SHA256 {
		return Check{}, errors.New("node executable sha256 mismatch")
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return Check{}, fmt.Errorf("node executable build info: %w", err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return Check{}, errors.New("node executable platform or architecture mismatch")
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return Check{}, err
	}
	return selfCheck(ctx, path, spec, requireHandover)
}

// selfCheck runs path's node-update-check: it must start here and say the target version, this platform and, when
// required, the listener handover.
func selfCheck(ctx context.Context, path string, spec proto.NodeUpdateBeginParams, requireHandover bool) (Check, error) {
	check, err := runCheck(ctx, path)
	if err != nil {
		return Check{}, err
	}
	if check.Version != spec.Version || check.OS != runtime.GOOS || check.Arch != runtime.GOARCH {
		return Check{}, errors.New("node executable self-check version or platform mismatch")
	}
	if requireHandover && !check.ResidentHandover {
		return Check{}, errors.New("node executable does not support resident listener handover")
	}
	return check, syncFile(path)
}

// runCheck is what path's node-update-check says, under the update's limits.
func runCheck(ctx context.Context, path string) (Check, error) {
	check, err := RunCheck(ctx, path, checkLimits)
	if err != nil {
		return Check{}, fmt.Errorf("node executable self-check: %w", err)
	}
	return check, nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// errSuperseded marks a swap that stopped because the installed file was
// replaced by a deployment this transaction did not stage: the file on disk is
// someone else's now and must be left in place, the transaction cleaned up.
var errSuperseded = errors.New("node executable was replaced by another deployment")

func executableInfo(path string) (os.FileInfo, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || (runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0) {
		return nil, errSuperseded
	}
	if runtime.GOOS != "windows" {
		return st, nil
	}
	// A Windows path stat defers reading the file ID until SameFile. Capture
	// the ID from a handle while this path still names the inspected file.
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	snapshot, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !sameFileState(st, snapshot) {
		return nil, errSuperseded
	}
	return snapshot, nil
}

func sameFileState(a, b os.FileInfo) bool {
	return a.Mode() == b.Mode() && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && os.SameFile(a, b)
}

func checkFileState(path string, expected os.FileInfo) error {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return errSuperseded
	}
	if err != nil {
		return err
	}
	if !sameFileState(expected, st) {
		return errSuperseded
	}
	return nil
}

// renameStill replaces dst with src only while dst is still the file whose
// sha256 is want. The file's inode, mode, size and modification time are snapshotted
// before and rechecked after the hash is read through one open descriptor, and
// once more against the path immediately before the rename, so a deployment
// that renamed dst into place or rewrote it in place (cp never takes the
// directory lock) between those points is detected, not overwritten. The
// window that remains is the one between the last check and the rename, two
// adjacent syscalls. The descriptor is closed before the rename: Windows will not
// rename a file over one this process holds open.
func renameStill(src, dst, want string) error {
	current, err := executableInfo(dst)
	if err != nil {
		return err
	}
	f, err := os.Open(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	if !sameFileState(current, before) {
		return errSuperseded
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != want {
		return errSuperseded
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if !sameFileState(before, after) {
		return errSuperseded
	}
	_ = f.Close()
	if err := checkFileState(dst, after); err != nil {
		return err
	}
	return replaceFile(src, dst)
}
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}

// locked runs fn holding the lock on the executable's directory (lockDir).
func locked(exe string, fn func() error) error {
	unlock, err := lockDir(filepath.Dir(exe))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
