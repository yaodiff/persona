package hub

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/operations/backup"
	"github.com/nwverse/persona/server/internal/update"
)

// developmentBackupRetainDefault is how many verified snapshots are kept when
// the operator did not choose another number.
const developmentBackupRetainDefault = 3

type developmentView struct {
	Configured bool   `json:"configured"`
	Why        string `json:"why,omitempty"` // localized: why it cannot be selected
	Branch     string `json:"branch"`
	// BackupDirectory is the effective snapshot folder, BackupDefaultDirectory
	// the recommended one under the runtime; BackupRetain how many verified
	// snapshots are kept. BuiltinQuiesce: the hub pauses its own writers.
	BackupDirectory          string `json:"backup_directory,omitempty"`
	BackupDefaultDirectory   string `json:"backup_default_directory,omitempty"`
	BackupDirectoryIsDefault bool   `json:"backup_directory_is_default"`
	BackupRetain             int    `json:"backup_retain,omitempty"`
	BuiltinQuiesce           bool   `json:"builtin_quiesce,omitempty"`
	CurrentCommit            string `json:"current_commit,omitempty"`
	CurrentUpdatedAt         string `json:"current_updated_at,omitempty"`
}

// Installation time is recorded by confirmation, never inferred from a build or Git date.
// Caller holds u.mu.
func (u *updater) currentUpdatedAtLocked() string {
	for i := len(u.settings.History) - 1; i >= 0; i-- {
		r := u.settings.History[i]
		if r.Result == "updated" && r.ToVersion == u.h.version && r.ToBuild == u.h.build.Number && !r.Ended.IsZero() {
			return r.Ended.Format(time.RFC3339)
		}
	}
	return ""
}

func (u *updater) developmentConfig() *update.DevelopmentConfig {
	if u.h.cfg == nil {
		return nil
	}
	return u.h.cfg.Update.Development
}

// hasOtherLocalManagers reports whether the runtime has any local manager
// besides the built-in assistant (managers/<id>/manager.json), which webdev.py
// inventories alongside the hub and built-in manager.
func hasOtherLocalManagers(home string) (bool, error) {
	if home == "" {
		return false, nil
	}
	managersDir := filepath.Join(home, "managers")
	entries, err := os.ReadDir(managersDir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	for _, entry := range entries {
		if entry.Name() == assistantwire.AssistantID {
			continue
		}
		// Match webdev.py's managers/*/manager.json inventory: ignore stray
		// non-directory files (e.g. .DS_Store) while still checking directories
		// and symlinks that may resolve to a manager directory.
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			continue
		}
		_, err := os.Lstat(filepath.Join(managersDir, entry.Name(), "manager.json"))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return true, err
		}
	}
	return false, nil
}

func (u *updater) developmentQuiesceError(bc update.DevelopmentBackupConfig) error {
	if !bc.Coordinated() {
		if bc.RequiresExternalQuiesce() && len(bc.Quiesce) == 0 && len(bc.Resume) == 0 {
			return i18n.Err("hub-selfupdate.err_development_external_quiesce")
		}
		return i18n.Err("hub-selfupdate.err_development_config")
	}
	if !bc.ExternalQuiesce() && u.h.cfg != nil {
		if other, err := hasOtherLocalManagers(u.h.cfg.dir); err != nil || other {
			return i18n.Err("hub-selfupdate.err_development_external_quiesce")
		}
	}
	return nil
}

func (u *updater) developmentUnavailableErr() error {
	if u.h.cfg == nil {
		return i18n.Err("hub-selfupdate.err_development_config")
	}
	if u.h.build.Channel == update.Docker {
		return i18n.Err("hub-selfupdate.err_development_docker")
	}
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return i18n.Err("hub-selfupdate.err_development_platform")
	}
	var bc update.DevelopmentBackupConfig
	if c := u.developmentConfig(); c != nil {
		bc = c.Backup
	}
	if err := u.developmentQuiesceError(bc); err != nil {
		return err
	}
	if err := u.developmentTopology(); err != nil {
		return err
	}
	return nil
}

// developmentAvailable: the hub can prepare Web development updates. The hub
// pauses its own writers for the snapshot by default (quiesceDevelopmentWriters)
// when only the hub and built-in manager are snapshotted; hub.json's
// quiesce/resume hooks are an optional override and are required as a pair
// whenever extra databases, extra roots or another local manager are present.
// The topology check is part of availability so the Web UI does not offer a
// channel that its first check would refuse.
func (u *updater) developmentAvailable() bool {
	return u.h.cfg != nil && u.developmentUnavailableErr() == nil
}

// developmentUnavailableWhy is what the Web UI says when development updates
// cannot be selected, localized; "" when available or without config.
func (u *updater) developmentUnavailableWhy(l i18n.Lang) string {
	if u.h.cfg == nil {
		return ""
	}
	err := u.developmentUnavailableErr()
	if err == nil {
		return ""
	}
	return i18n.Localize(err, l)
}

// developmentBackupConfigLocked is the snapshot configuration in effect: the
// folder chosen in the Web UI (which may be the recommended one an operator
// chose over hub.json's), else hub.json's, else the recommended one under the
// runtime's update/ directory; the chosen retention, else three. Builtin says
// the hub itself pauses the writers. Caller holds u.mu.
func (u *updater) developmentBackupConfigLocked() update.DevelopmentBackupConfig {
	var c update.DevelopmentBackupConfig
	if u.h.cfg != nil && u.h.cfg.Update.Development != nil {
		c = u.h.cfg.Update.Development.Backup
	}
	if u.settings.DevelopmentBackupDirectory != nil {
		c.Directory = *u.settings.DevelopmentBackupDirectory
	} else if c.Directory == "" && u.h.cfg != nil {
		c.Directory = backup.RecommendedDevelopmentBackupDir(u.h.cfg.dir)
	}
	if u.settings.DevelopmentBackupRetain > 0 {
		c.Retain = u.settings.DevelopmentBackupRetain
	}
	if c.Retain <= 0 {
		c.Retain = developmentBackupRetainDefault
	}
	c.Builtin = !c.ExternalQuiesce()
	return c
}

// developmentRequestConfig is what one helper phase receives, with the snapshot
// settings resolved.
func (u *updater) developmentRequestConfig() *update.DevelopmentConfig {
	u.mu.Lock()
	defer u.mu.Unlock()
	return &update.DevelopmentConfig{Backup: u.developmentBackupConfigLocked()}
}

func (u *updater) developmentLocked() bool { return u.settings.Source == "web-dev" }

func (u *updater) adoptionUnresolved() bool {
	_, err := os.Lstat(update.DevelopmentAdoptionPath(u.dir))
	return !errors.Is(err, os.ErrNotExist)
}

func (u *updater) autoLocked() bool {
	if u.developmentLocked() {
		return u.settings.DevelopmentAuto
	}
	return u.settings.auto()
}

func (u *updater) selectedDevelopment() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.developmentLocked()
}

func (u *updater) candidateNewer(r *update.Published) bool {
	if r == nil {
		return false
	}
	if r.Development != nil {
		return r.Development.BackupID == "" && r.Development.Commit != u.h.build.Commit
	}
	return u.h.build.Newer(r.Manifest.Build)
}

func (u *updater) developmentHold(m *update.Manifest) string {
	// A known clean installed commit must be an ancestor for automatic adoption.
	// Unknown/dirty builds can still be updated manually, like release updates.
	return u.h.build.Hold(m)
}

func (u *updater) unreplacedCandidate(r *update.Published) error {
	if r.Development == nil {
		return u.unreplaced(r.Manifest)
	}
	// A rollback can have a lower build number, but must still replace every active split service.
	target, err := u.target()
	if err != nil {
		return err
	}
	self, _ := u.self()
	return unreplaced(r.Manifest, target, self, u.services)
}

func (u *updater) runDevelopment(ctx context.Context, operation string, input map[string]any, result any, resume func()) error {
	cfg := u.developmentRequestConfig()
	u.mu.Lock()
	gen := u.opGen
	u.mu.Unlock()
	report := func(p update.DevelopmentProgress) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if u.opGen == gen && u.state == p.Phase {
			u.developmentProgress = &p
		}
	}
	if err := u.devRun(ctx, cfg, filepath.Join(u.dir, "development"), operation, input, result, resume, report); err != nil {
		if errors.Is(err, context.Canceled) {
			return err
		}
		u.mu.Lock()
		step := ""
		if p := u.developmentProgress; p != nil && u.opGen == gen && update.DevelopmentStep(operation, p.Step).Phase == p.Phase {
			step = p.Step
		}
		u.mu.Unlock()
		if reason, code := update.DevelopmentBackupReason(err); reason != "" && step != "" {
			key := "hub-selfupdate.backup_reason_" + reason
			if i18n.Has(key) {
				return i18n.Err("hub-selfupdate.err_development_detail", i18n.Args{
					"step": i18n.Err("hub-selfupdate.development_step_" + step), "phase": operation,
					"reason": i18n.Err(key, i18n.Args{"code": code}),
				})
			}
		}
		return developmentPhaseError(operation, step)
	}
	return nil
}

func developmentPhaseError(operation, step string) error {
	if step == "" {
		return i18n.Err("hub-selfupdate.err_development_phase", i18n.Args{"phase": operation})
	}
	return i18n.Err("hub-selfupdate.err_development_step", i18n.Args{
		"phase": operation, "step": i18n.Err("hub-selfupdate.development_step_" + step),
	})
}

func (u *updater) startDevelopmentPhase(p update.DevelopmentProgress, legacyProgress string) {
	u.mu.Lock()
	u.startDevelopmentPhaseLocked(p, legacyProgress)
	u.mu.Unlock()
	u.broadcastStatus()
}

func (u *updater) startDevelopmentPhaseLocked(p update.DevelopmentProgress, legacyProgress string) {
	u.setLocked(p.Phase, legacyProgress, "")
	u.developmentProgress, u.developmentStarted = &p, time.Now()
}

// quiesceDevelopmentWriters pauses the writers a snapshot must not see moving.
// With hub.json's quiesce/resume hooks the helper runs them itself; otherwise
// the hub pauses itself: request writes, the built-in child manager and the
// background writers, then checks both databases with backup.Quiesced rules.
// The returned resume always runs, including after a partial pause.
func (u *updater) quiesceDevelopmentWriters(ctx context.Context, cfg update.DevelopmentBackupConfig) (func(), error) {
	if err := u.developmentQuiesceError(cfg); err != nil {
		return nil, err
	}
	if cfg.ExternalQuiesce() {
		return func() {}, nil
	}
	if u.h.cfg == nil || !u.h.cfg.Assistant.StartsBuiltin() {
		return nil, i18n.Err("hub-selfupdate.err_development_topology")
	}
	if err := u.h.pauseDevelopmentWriters(ctx); err != nil {
		return nil, i18n.Err("hub-selfupdate.err_development_quiesce", i18n.Args{"error": i18n.Localize(err, u.h.userLang())})
	}
	resumed := false
	resume := func() {
		if resumed {
			return
		}
		resumed = true
		u.h.resumeDevelopmentWriters()
	}
	if err := u.waitDevelopmentQuiesced(ctx); err != nil {
		resume()
		return nil, i18n.Err("hub-selfupdate.err_development_quiesce", i18n.Args{"error": i18n.Localize(err, u.h.userLang())})
	}
	return resume, nil
}

// waitDevelopmentQuiesced checks the databases the snapshot dumps with the
// backup command's own Quiesced rules, after giving the manager's connections a
// moment to close. Only this hub's pool sessions count as its own.
func (u *updater) waitDevelopmentQuiesced(ctx context.Context) error {
	pids, ok := u.h.store.(interface{ BackendPIDs() []int32 })
	if !ok {
		return nil // no real database in this hub (tests): nothing to verify
	}
	if u.h.cfg == nil || u.h.cfg.DatabaseURL == "" {
		return errors.New("the hub has no database connection to verify")
	}
	// The same targets webdev.py's inventory dumps: a built-in manager whose
	// tables are still in the hub database needs only that one checked, and one
	// that moved to its own is checked separately.
	hubTarget, managerTarget, shared, err := backup.DiscoverDevelopment(backup.Options{Home: u.h.cfg.dir, DatabaseURL: u.h.cfg.DatabaseURL})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := backup.QuiescedExcept(ctx, hubTarget, hubTarget.Database, pids.BackendPIDs()...)
		if err == nil && !shared {
			err = backup.QuiescedDatabase(ctx, managerTarget, managerTarget.Database, true)
		}
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (u *updater) checkDevelopment(ctx context.Context) (*update.Published, error) {
	r, err := u.resolveDevelopment(ctx)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	if !u.updateActiveLocked() {
		u.release, u.blocked, u.lastCheck = r, nil, time.Now()
	}
	u.mu.Unlock()
	return r, nil
}

func (u *updater) resolveDevelopment(ctx context.Context) (*update.Published, error) {
	if err := u.developmentUnavailableErr(); err != nil {
		return nil, err
	}
	var candidate update.DevelopmentCandidate
	err := u.runDevelopment(ctx, "resolve", nil, &candidate, nil)
	if err != nil || !devicewire.ValidDevelopmentCommit(candidate.Commit) {
		return nil, i18n.Err("hub-selfupdate.err_resolve_latest")
	}
	number := update.DevelopmentNumber(u.h.build.Number)
	m := &update.Manifest{Version: fmt.Sprintf("web-dev-%s-%d", candidate.Commit[:12], number), Build: number,
		Commit: candidate.Commit, Released: time.Now(), Edition: u.h.build.Edition.Marker()}
	r := &update.Published{Manifest: m, Development: &candidate}
	u.mu.Lock()
	// A repeated check of the same object retains the attempt identity (including
	// its bad-build marker), rather than creating a new build to bypass rollback.
	if u.release != nil && u.release.Development != nil && u.release.Development.BackupID == "" && u.release.Development.Commit == candidate.Commit {
		copy := *u.release
		manifest := *copy.Manifest
		manifest.Edition = m.Edition
		copy.Manifest = &manifest
		copy.Development = &candidate
		r = &copy
	}
	for _, record := range u.settings.History {
		if record.Source == "web-dev" && record.Commit == candidate.Commit && record.Result == "rolled_back" {
			r.Manifest.Build = record.ToBuild
			r.Manifest.Version = record.ToVersion
		}
	}
	u.mu.Unlock()
	return r, nil
}

// Build only this Hub and the platforms its registered computers last reported.
// Offline computers keep their platform; never-seen computers supply none yet.
func (u *updater) developmentPlatforms() []string {
	platforms := u.developmentComputerPlatforms(true)
	for _, peer := range u.h.busRoster() {
		if peer.Role == bus.RoleManager && peer.ID != devicesOwner && peer.OS != "" && peer.Arch != "" {
			platforms = append(platforms, peer.OS+"-"+peer.Arch)
		}
	}
	slices.Sort(platforms)
	return slices.Compact(platforms)
}

func (u *updater) developmentComputerPlatforms(allOwners bool) []string {
	platforms := []string{runtime.GOOS + "-" + runtime.GOARCH}
	for _, mc := range u.h.knownEntries() {
		if !allOwners && u.h.entryOwner(mc.ID) != devicesOwner {
			continue
		}
		if m := u.h.machineByID(mc.ID); m != nil {
			s := m.snapshot()
			if s.OS != "" && s.Arch != "" {
				platforms = append(platforms, s.OS+"-"+s.Arch)
			}
		}
	}
	slices.Sort(platforms)
	return slices.Compact(platforms)
}

func (u *updater) buildDevelopment(ctx context.Context, r *update.Published) error {
	operation, firstStep, lastStep := "build", "fetch", "manifest"
	if r.Development.BackupID != "" {
		operation, firstStep, lastStep = "prepare_backup", "verify_snapshot", "prepare_artifacts"
	}
	u.startDevelopmentPhase(update.DevelopmentStep(operation, firstStep), "web/hub/node")
	var built struct {
		Artifacts string   `json:"artifacts"`
		Ancestors []string `json:"ancestors"`
	}
	platforms := u.developmentPlatforms()
	if operation == "prepare_backup" {
		platforms = []string{runtime.GOOS + "-" + runtime.GOARCH}
		if u.nodesEnabled() {
			platforms = u.developmentComputerPlatforms(false)
		}
	}
	source := u.h.updateSource()
	releaseURL := source.url
	if releaseURL == "" && source.urlErr == nil && !u.h.cfg.Security.PrivateNetworkOnly {
		// The first Web development build can also start from a hub with no release source.
		releaseURL = officialReleaseUpdateURL
	}
	err := u.runDevelopment(ctx, operation, map[string]any{
		"candidate": r.Development, "version": r.Manifest.Version, "build": r.Manifest.Build,
		"backup_id": r.Development.BackupID, "runtime": u.h.cfg.dir,
		"platform": runtime.GOOS + "-" + runtime.GOARCH, "platforms": platforms, "release_url": releaseURL}, &built, nil)
	if err != nil {
		return err
	}
	r.Source = (&url.URL{Scheme: "file", Path: built.Artifacts}).String()
	if err := ctx.Err(); err != nil {
		return err
	}
	var ancestors string
	if operation == "build" {
		ancestors, err = update.EncodeAncestors(built.Ancestors)
		if err != nil {
			return developmentPhaseError(operation, lastStep)
		}
	}
	// Development artifacts and their hosted packages use File.Name locally, not the publisher's core- aliases.
	// Build that shape first, then restore the hub's edition below for CheckBinary.
	local := *r.Manifest
	local.Edition = ""
	m, err := update.BuildManifest(built.Artifacts, local)
	if err != nil {
		return developmentPhaseError(operation, lastStep)
	}
	if operation == "build" {
		// Compare the producer's native and archive hashes with a fresh read of every archive.
		var produced update.Manifest
		ok, err := update.Load(filepath.Join(built.Artifacts, "manifest.json"), &produced)
		if err != nil || !ok || produced.Version != m.Version || produced.Build != m.Build || produced.Commit != m.Commit ||
			!reflect.DeepEqual(produced.Files, m.Files) || !reflect.DeepEqual(produced.Programs, m.Programs) ||
			!reflect.DeepEqual(produced.Nodes, m.Nodes) {
			return developmentPhaseError(operation, lastStep)
		}
	}
	m.Ancestors = ancestors
	m.Edition = r.Manifest.Edition
	r.Manifest = m
	return nil
}

func (u *updater) preflightDevelopment(ctx context.Context) error {
	cfg := u.developmentRequestConfig()
	u.startDevelopmentPhase(update.DevelopmentStep("preflight", "preflight"), "")
	return u.checkBackupBeforePause(ctx, cfg.Backup)
}

func (u *updater) checkBackupBeforePause(ctx context.Context, cfg update.DevelopmentBackupConfig) error {
	if err := u.developmentTopology(); err != nil {
		return err
	}
	// Immutable package pointers belong to the release publisher. Do not alter it
	// or silently leave development nodes on the previous release.
	if info, err := os.Lstat(identity.NodeBinaryDir()); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("node package directory is publisher-managed; configure a dedicated development installation")
	}
	if err := u.developmentQuiesceError(cfg); err != nil {
		return err
	}
	return u.runDevelopment(ctx, "preflight", map[string]any{
		"runtime": u.h.cfg.dir, "hub_database": u.h.cfg.DatabaseURL,
	}, &struct{}{}, nil)
}

func (u *updater) backupDevelopment(ctx context.Context, r *update.Published, target string) (*update.DevelopmentBackup, error) {
	u.mu.Lock()
	updatedAt := u.currentUpdatedAtLocked()
	cfg := u.developmentBackupConfigLocked()
	if u.attempt != nil && u.attempt.cancelling {
		u.mu.Unlock()
		return nil, context.Canceled
	}
	u.startDevelopmentPhaseLocked(update.DevelopmentStep("backup", "preflight"), "")
	u.mu.Unlock()
	u.broadcastStatus()
	programs := map[string]string{"personad": target}
	for _, name := range update.ProgramNames {
		path := filepath.Join(filepath.Dir(target), name)
		if _, err := os.Stat(path); err == nil {
			programs[name] = path
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, developmentPhaseError("backup", "preflight")
		}
	}
	var saved update.DevelopmentBackup
	// The hub's own writers pause around every dump and the file archive; with
	// hub.json's hooks the helper does it instead.
	if cfg.Builtin {
		// Idle waiting and the build may have outlived the first check.
		if err := u.checkBackupBeforePause(ctx, cfg); err != nil {
			return nil, err
		}
		u.startDevelopmentPhase(update.DevelopmentStep("backup", "quiesce"), "")
	}
	resume, err := u.quiesceDevelopmentWriters(ctx, cfg)
	if err != nil {
		return nil, developmentPhaseError("backup", "quiesce")
	}
	defer resume()
	err = u.runDevelopment(ctx, "backup", map[string]any{
		"runtime": u.h.cfg.dir, "candidate": r.Development, "hub_database": u.h.cfg.DatabaseURL,
		"from_version": u.h.version, "from_commit": u.h.build.Commit, "from_build": u.h.build.Number,
		"from_updated_at": updatedAt, "platform": runtime.GOOS + "-" + runtime.GOARCH,
		"executables": programs, "node_directory": identity.NodeBinaryDir()}, &saved, resume)
	if err != nil {
		return nil, err
	}
	resume()
	u.mu.Lock()
	u.settings.DevelopmentBackup = &saved
	err = u.saveLocked(ctx)
	u.mu.Unlock()
	if err != nil {
		return nil, developmentPhaseError("backup", "")
	}
	return &saved, nil
}

// The Hub owns its child and can pause its writers. Independent assistants have their own update path.
func (u *updater) developmentTopology() error {
	if u.h.cfg == nil || !u.h.cfg.Assistant.StartsBuiltin() {
		return i18n.Err("hub-selfupdate.err_development_topology")
	}
	return nil
}

func pendingSource(p update.Pending) string {
	if p.Development != nil {
		return "web-dev"
	}
	return "release"
}

func pendingCommit(p update.Pending) string {
	if p.Development != nil {
		return p.Development.Commit
	}
	return ""
}

// detachDevelopmentWeb makes the newly embedded Web authoritative. Keep a local
// override with the same transaction so rollback restores the old UI too.
func (u *updater) detachDevelopmentWeb(p *update.Pending) error {
	return update.DetachDevelopmentWeb(u.dir, filepath.Join(u.h.cfg.dir, "web"), p)
}

// Remove only the checkout built by this exact invocation, after the existing
// installer has copied its artifacts. The owned helper directory is not a Git checkout supplied by a user.
func (u *updater) removeDevelopmentBuild(r *update.Published) {
	if r.Source == "" {
		return
	}
	parsed, err := url.Parse(r.Source)
	if err != nil || parsed.Scheme != "file" {
		return
	}
	root := filepath.Dir(parsed.Path)
	if filepath.Dir(root) == filepath.Join(u.dir, "development") && (strings.HasPrefix(filepath.Base(root), "build-") || strings.HasPrefix(filepath.Base(root), "prepare_backup-")) {
		_ = os.RemoveAll(root)
	}
}
