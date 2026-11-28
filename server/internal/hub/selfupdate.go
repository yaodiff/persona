package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/hubedge"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/operations/backup"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/update"
)

// The hub updating itself to the publisher's release (docs/operations/hub-update.md"Updates"): check the manifest every hour, download and verify, wait until the assistant and every agent are idle, swap the binary (keeping the old
// one), restart into the new one, and count it a success only once the machines that were connected are back;
// otherwise go back to the old binary, by the new process itself or by the old one's watchdog. A build that went
// back is not tried again automatically. Then the nodes follow, through deploy.go.

const (
	updateCheckEvery   = time.Hour // docs/operations/hub-update.md "Checking for updates" (hourly since 2026-11-07)
	updateFirstCheck   = time.Minute
	updateConfirmIn    = 2 * time.Minute
	updateWatchFor     = 3 * time.Minute
	updateMaxStarts    = 3
	updateIdleEvery    = 30 * time.Second
	updateManualIdle   = 2 * time.Hour
	updateHistoryKeep  = 50
	updateSettingsKey  = "hub_update"
	updateRequestLimit = 30 * time.Second
	// androidSyncLimit bounds reading android.json and downloading the APK it names (about 20 MB) at each check.
	androidSyncLimit = 10 * time.Minute

	// An install script (GET /i/<code>, managers_install.go) carries the release's sha256s: when the hub last read
	// latest.json longer than releaseFreshFor ago it reads it again first, waiting at most releaseRefreshLimit, and
	// tries that at most once every releaseRefreshEvery whatever the requests. Fresh for no longer than that: someone
	// whose download failed its checksum right after a release gets the new sums by asking again a minute later.
	releaseRefreshEvery = time.Minute
	releaseFreshFor     = releaseRefreshEvery
	releaseRefreshLimit = 5 * time.Second
)

var (
	errNoUpdate    = i18n.Err("hub-selfupdate.err_no_update")
	errNotRelease  = i18n.Err("hub-selfupdate.err_not_release")
	errUpdateBusy  = i18n.Err("hub-selfupdate.err_busy")
	errUpdateAgain = i18n.Err("hub-selfupdate.err_auto_off")
)

// updateRecord is one update in the history (Settings › hub › Update history).
type updateRecord struct {
	FromVersion string                    `json:"from_version"`
	ToVersion   string                    `json:"to_version"`
	ToBuild     int64                     `json:"to_build"`
	Started     time.Time                 `json:"started"`
	Ended       time.Time                 `json:"ended"`
	Result      string                    `json:"result"` // updated | rolled_back | failed | cancelled | held (newer, not installed by itself: Reason)
	Reason      string                    `json:"reason,omitempty"`
	Auto        bool                      `json:"auto"`
	Source      string                    `json:"source,omitempty"`
	Commit      string                    `json:"commit,omitempty"`
	Backup      *update.DevelopmentBackup `json:"backup,omitempty"`
}

// updateSettings are kept in Postgres (settings hub_update).
type updateSettings struct {
	ConfirmedDevelopment *devicewire.DevelopmentTarget `json:"confirmed_development,omitempty"`
	Source               string                        `json:"source,omitempty"`           // empty/release or web-dev
	DevelopmentAuto      bool                          `json:"development_auto,omitempty"` // opt-in; never inherited from release
	DevelopmentBad       []string                      `json:"development_bad,omitempty"`
	DevelopmentBackup    *update.DevelopmentBackup     `json:"development_backup,omitempty"`
	// The snapshot settings chosen in Settings › Hub › Update source › Web
	// development. Directory is a tri-state: nil keeps hub.json's directory
	// (else the recommended one), and a persisted path — including the
	// recommended one, which "Use recommended" stores — is the Web choice and
	// wins over hub.json. Retain 0 is unset (hub.json's, else three).
	DevelopmentBackupDirectory *string        `json:"development_backup_directory,omitempty"`
	DevelopmentBackupRetain    int            `json:"development_backup_retain,omitempty"`
	AutoUpdateNodes            *bool          `json:"auto_update_nodes,omitempty"` // nil: use the startup configuration
	Auto                       *bool          `json:"auto,omitempty"`              // nil: on (the default)
	AutoNoticeSeen             bool           `json:"auto_notice_seen"`
	Bad                        []int64        `json:"bad,omitempty"` // builds that went back: not installed automatically again
	History                    []updateRecord `json:"history,omitempty"`
}

func (s updateSettings) auto() bool { return s.Auto == nil || *s.Auto }

// busyItem is something the restart would interrupt.
type busyItem struct {
	MachineID string `json:"machine_id"`
	Machine   string `json:"machine"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
}

type updater struct {
	h      *Hub
	dir    string
	client *http.Client
	// Hooks the tests replace: run another binary in this process, start the watchdog, find the running binary,
	// alert the phones.
	exec func(path string) error
	// handover asks the edge to switch to target, the update to build (a slot only, else nil); nil once this slot is
	// released, else why the new one did not come up.
	handover func(ctx context.Context, target string, build int64) error
	// edgeConfirmed tells the edge the update this slot is the result of is confirmed (a slot only, else nil).
	edgeConfirmed func()
	startWatch    func(prev, pendingPath string) error
	target        func() (string, error)
	// self is the binary this hub runs as (runningBinary): persona-hub goes on as the persona-hub the update
	// swapped, personad as personad (runAs).
	self func() (string, error)
	// services finds what runs a program beside personad: a release that would leave one a service runs as it was is
	// not installed (unreplaced).
	services   serviceFinder
	notify     func(title, body string)
	confirmIn  time.Duration
	idleEvery  time.Duration
	manualIdle time.Duration

	mu          sync.Mutex
	settings    updateSettings
	loaded      bool
	state       string // idle | checking | available | downloading | waiting_idle | restarting | confirming | updated | rolled_back | failed
	progress    string
	errText     string
	release     *update.Published
	blocked     error  // why release cannot be installed here (unreplaced), as of when it was read; nil when it can
	newestImage string // a Docker hub: the newest release tag of its image
	lastCheck   time.Time
	busy        []busyItem
	attempt     *developmentAttempt // guarded by mu; nil once adoption wins
	running     bool
	checking    bool
	opGen       uint64 // bumped whenever an update or check starts, preventing busy-check ABA races
	now         bool   // the user confirmed "Update now": stop waiting for idle
	confirmed   bool   // no update to confirm, or this process confirmed it

	developmentProgress *update.DevelopmentProgress // protected by mu, like the phase
	developmentStarted  time.Time

	// refreshing is held by the one request reading latest.json for an install script (freshRelease); refreshTried
	// is when one last did. releaseRead is when release was read: a read that worked (lastCheck is when the hub last
	// tried, which the app shows).
	refreshing   sync.Mutex
	refreshTried time.Time
	releaseRead  time.Time

	// androidNote is what syncAndroid last logged: the same failure is not logged again every hour.
	androidNote string
	devRun      func(context.Context, *update.DevelopmentConfig, string, string, map[string]any, any, func(), func(update.DevelopmentProgress)) error
	// behindNote is the update source and release noteBehind last logged as behind: once per pair, not every hour.
	behindNote string
	// testAfterBackupRecoveryCheck runs in tests right after UnrecoveredSnapshot
	// and before re-acquiring u.mu in PUT /api/v1/hub/update/settings.
	testAfterBackupRecoveryCheck func()
}

func newUpdater(h *Hub) *updater {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Private publishers may atomically deliver verified releases over SSH.
	// A file source uses the same manifest, checksum and ancestry checks as HTTPS.
	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	u := &updater{h: h, dir: update.Dir(), client: &http.Client{Timeout: 10 * time.Minute, Transport: transport},
		devRun: update.DevelopmentRequest,
		exec: func(path string) error {
			// The sockets systemd passed go on to the new binary (listen.go): it could not listen otherwise.
			// The hub's arguments; `hub …` when it runs as persona-hub and path is personad (update.TargetArgs).
			return execKeepingListeners(path, append([]string{path}, update.TargetArgs(os.Args, path)...), os.Environ())
		},
		startWatch: startWatchdog, target: ServerBinary, self: runningBinary, services: systemServices(), confirmIn: updateConfirmIn, idleEvery: updateIdleEvery,
		manualIdle: updateManualIdle, state: "idle", confirmed: true,
		notify: func(title, body string) {
			h.push.send(map[string]any{"aps": map[string]any{
				"alert": map[string]any{"title": title, "body": body}, "sound": "default", "thread-id": "hub_update",
			}}, "hub_update", nil)
		}}
	// hub.json's update.proxy, for this client alone (selfupdate_source.go).
	transport.Proxy = u.proxyFor
	if h.slot != nil {
		u.exec = u.slotExec
		u.handover = func(ctx context.Context, target string, build int64) error {
			return h.slot.Switch(ctx, target, "", hubedge.KindUpdate, build)
		}
		u.edgeConfirmed = h.slot.Confirmed
	}
	return u
}

// slotExec is how a slot (the edge started it, hub_edge.go) runs the binary at path instead of exec'ing it: once it
// serves, the edge switches to path (rollback: it stops this one); before that (boot) it exits and the edge starts it
// again at once on the binary now at its path.
func (u *updater) slotExec(path string) error {
	if !u.h.slot.Serving() {
		u.h.logf("update: starting again on %s", path)
		os.Exit(hubedge.RespawnCode)
	}
	return u.h.slot.Switch(context.Background(), path, "", hubedge.KindRollback, 0)
}

func runningBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// ServerBinary is the program that is the whole server: this one, unless it is persona-hub, which runs the hub
// alone: then the personad beside it (docs/server/server.md "Programs"). The update's target goes by it (personad is
// the release's main download; persona-hub is swapped as a program and the hub goes on as it: runAs), and so does
// setup's service binary. What the hub runs goes by the program it runs as: the built-in manager it starts is
// persona-manager beside persona-hub (managerBinary).
func ServerBinary() (string, error) {
	p, err := runningBinary()
	if err != nil {
		return "", err
	}
	return serverBinaryOf(p)
}

// serverBinaryOf is ServerBinary for the running binary p. The personad beside persona-hub is resolved through
// symlinks, as the running binary is: an update replaces the file, never a link to it (an install under the former
// name keeps personad as a link to that file).
func serverBinaryOf(p string) (string, error) {
	if !isProgram(p, "persona-hub") {
		return p, nil
	}
	sibling := filepath.Join(filepath.Dir(p), "personad"+filepath.Ext(p))
	resolved, err := filepath.EvalSymlinks(sibling)
	if err != nil {
		return "", fmt.Errorf("persona-hub runs the hub alone and needs the personad beside it (%s): %w", sibling, err)
	}
	return resolved, nil
}

// isProgram says whether the binary at p is the program name (on Windows name.exe).
func isProgram(p, name string) bool {
	return strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) == name
}

// runAs is Pending.Run for an update that swapped programs beside personad: the persona-hub among them when the hub
// runs as that persona-hub, so it goes on as the hub's own program; "" otherwise (it goes on as personad, the target).
// A release without persona-hub leaves it on personad, the same build, rather than on a persona-hub the update did
// not replace.
func runAs(running func() (string, error), programs []update.Swapped) string {
	self, err := running()
	if err != nil || !isProgram(self, "persona-hub") {
		return ""
	}
	for _, s := range programs {
		if filepath.Clean(s.Target) == filepath.Clean(self) {
			return s.Target
		}
	}
	return ""
}

// startWatchdog runs `<old binary> update-watch <pending>` in its own session, its output in update/watch.log.
func startWatchdog(prev, pendingPath string) error {
	log, err := os.OpenFile(filepath.Join(filepath.Dir(pendingPath), "watch.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(prev, "update-watch", pendingPath)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = osproc.DetachedAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Wait instead of releasing: when the exec into the new version fails the pending marker goes, the watchdog
	// exits, and a child nobody waits for stays a zombie for as long as this hub runs. It stays this process's direct
	// child (update-watch's hubIsParent relies on that); after a successful exec this goroutine is gone and the new
	// image inherits it.
	go func() { _ = cmd.Wait() }()
	return nil
}

// boot runs once, first thing when the hub starts, before the config or the database: a new version that keeps
// dying (in a service manager's restart loop) goes back after updateMaxStarts starts; an old version that finds a
// marker for an update it is not (the watchdog or a person put it back) clears it for recording. A marker a watchdog
// claimed and then died with (update.Unclaim) counts as the marker. It says whether this process has an update of its
// own to confirm. Recovery failures stop startup before config/database work.
func (u *updater) boot() (bool, error) {
	if err := update.Unclaim(update.PendingPath(u.dir)); err != nil {
		u.h.logf("update: put back the watchdog's claim on the pending marker: %v", err)
	}
	var p update.Pending
	ok, err := update.Load(update.PendingPath(u.dir), &p)
	if err != nil {
		return false, nil
	}
	if !ok {
		// The adoption journal is durable before the first replacement. A
		// crash before pending.json must restore the whole transaction too.
		ok, err = update.Load(update.DevelopmentAdoptionPath(u.dir), &p)
		if err == nil && ok && p.RecoveryReady && p.Development != nil &&
			(u.h.build.Number == p.FromBuild || u.h.build.Number == p.ToBuild) {
			if u.h.build.Number == p.ToBuild {
				if err := u.rollback(p, su(u.h.userLang(), "reason_went_back")); err != nil {
					return false, err
				}
				return true, nil // exec normally does not return
			}
			if err := update.RestoreAll(p); err != nil {
				u.h.logf("update: interrupted adoption recovery retained: %v", err)
				return false, err
			} else {
				u.markRolledBack(p, su(u.h.userLang(), "reason_went_back"))
				update.ClearDevelopmentAdoption(u.dir, p)
				if !u.adoptionUnresolved() {
					u.removeDownloads(u.stagedDir(p.ToBuild))
				}
			}
		}
		return false, nil
	}
	if p.ToBuild != u.h.build.Number {
		if u.h.build.Number == p.FromBuild {
			if p.Development != nil {
				if err := update.RestoreAll(p); err != nil {
					u.h.logf("update: restore failed, recovery retained: %v", err)
					return false, err
				}
				update.ClearDevelopmentAdoption(u.dir, p)
			}
			u.markRolledBack(p, su(u.h.userLang(), "reason_went_back"))
		} else {
			_ = os.Remove(update.PendingPath(u.dir)) // replaced by a build deployed straight from master: not a failure
		}
		return false, nil
	}
	u.confirmed = false
	p.Starts++
	if err := update.Save(update.PendingPath(u.dir), p); err != nil {
		// Without the count on disk every restart reads the same Starts, so a build that keeps crashing would never
		// reach updateMaxStarts: go back to the version that ran before while this process still can.
		u.h.logf("update: save start count: %v", err)
		if err := u.rollback(p, su(u.h.userLang(), "reason_marker_unsaved")); err != nil {
			return false, err
		}
		return true, nil
	}
	if p.Starts > updateMaxStarts {
		if err := u.rollback(p, su(u.h.userLang(), "reason_no_start", i18n.Args{"count": p.Starts - 1})); err != nil {
			return false, err
		}
	}
	return true, nil
}

// start loads the settings, records an update that went back, confirms one under way, removes the downloads nothing
// reads any more, and checks for releases.
func (u *updater) start(ctx context.Context) {
	u.load(ctx)
	u.recordRolledBack(ctx)
	var p update.Pending
	ok, _ := update.Load(update.PendingPath(u.dir), &p)
	confirming := ok && p.ToBuild == u.h.build.Number
	keep := int64(0) // the update this process confirms: its node binaries are put in place once it is confirmed
	if confirming {
		keep = p.ToBuild
	}
	u.pruneDownloads(keep)
	if confirming {
		go u.confirm(ctx, p)
	}
	go u.loop(ctx)
}

func (u *updater) load(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.loadLocked(ctx)
}

// loadLocked publishes settings only after a complete read, so a failed read can be retried.
func (u *updater) loadLocked(ctx context.Context) error {
	if u.loaded {
		return nil
	}
	if u.h.store != nil {
		var settings updateSettings
		if _, err := u.h.store.Setting(ctx, updateSettingsKey, &settings); err != nil {
			u.h.logf("update: settings: %v", err)
			return err
		}
		u.settings = settings
	}
	u.loaded = true
	return nil
}

// saveLocked writes the settings (called with u.mu held after a successful load).
func (u *updater) saveLocked(ctx context.Context) error {
	if err := u.loadLocked(ctx); err != nil {
		return err
	}
	if len(u.settings.History) > updateHistoryKeep {
		u.settings.History = u.settings.History[len(u.settings.History)-updateHistoryKeep:]
	}
	if u.h.store != nil {
		if err := u.h.store.PutSetting(ctx, updateSettingsKey, u.settings); err != nil {
			u.h.logf("update: save settings: %v", err)
			u.loaded = false // The next attempt must start from the persisted settings.
			return err
		}
	}
	return nil
}

func (u *updater) record(ctx context.Context, r updateRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.recordLocked(ctx, r)
}

func (u *updater) recordLocked(ctx context.Context, r updateRecord) error {
	if err := u.loadLocked(ctx); err != nil {
		return err
	}
	u.settings.History = append(u.settings.History, r)
	if r.Result == "updated" {
		u.settings.ConfirmedDevelopment = nil
		target := devicewire.DevelopmentBuild(r.Commit, r.ToVersion, r.ToBuild)
		if r.Source == "web-dev" && r.Reason == "" && target.Valid() {
			u.settings.ConfirmedDevelopment = &target
		}
	}
	if r.Result == "rolled_back" && !slices.Contains(u.settings.Bad, r.ToBuild) {
		u.settings.Bad = append(u.settings.Bad, r.ToBuild)
	}
	if r.Result == "rolled_back" && r.Source == "web-dev" && !slices.Contains(u.settings.DevelopmentBad, r.Commit) {
		u.settings.DevelopmentBad = append(u.settings.DevelopmentBad, r.Commit)
	}
	return u.saveLocked(ctx)
}

func (u *updater) markRolledBack(p update.Pending, reason string) {
	path := update.RolledBackPath(u.dir)
	if _, err := os.Stat(path); err != nil {
		if err := update.Save(path, update.RolledBack{FromVersion: p.FromVersion, ToVersion: p.ToVersion, ToBuild: p.ToBuild,
			Started: p.Started, At: time.Now(), Reason: reason, Auto: p.Auto, Development: p.Development, Backup: p.Backup}); err != nil {
			u.h.logf("update: save rollback marker (history and bad builds miss it): %v", err)
		}
	}
	if err := os.Remove(update.PendingPath(u.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.h.logf("update: remove pending marker: %v", err)
	}
}

// recordRolledBack puts an update that went back into the history and the bad builds, once the database is there.
func (u *updater) recordRolledBack(ctx context.Context) {
	var rb update.RolledBack
	if ok, err := update.Load(update.RolledBackPath(u.dir), &rb); err != nil || !ok {
		return
	}
	if err := u.record(ctx, updateRecord{FromVersion: rb.FromVersion, ToVersion: rb.ToVersion, ToBuild: rb.ToBuild, Started: rb.Started,
		Ended: rb.At, Result: "rolled_back", Reason: rb.Reason, Auto: rb.Auto, Source: pendingSource(update.Pending{Development: rb.Development}), Commit: pendingCommit(update.Pending{Development: rb.Development}), Backup: rb.Backup}); err != nil {
		return // Keep the marker until the history and bad-build list have been saved.
	}
	l := u.h.userLang()
	why := su(l, "rolled_back", i18n.Args{"to": rb.ToVersion, "from": u.h.version, "reason": rb.Reason})
	u.set("rolled_back", "", why)
	_ = os.Remove(update.RolledBackPath(u.dir))
	u.notify(su(l, "push_failed_title"), su(l, "push_failed_body", i18n.Args{"why": why}))
	u.h.logf("update: %s went back to %s: %s", rb.ToVersion, u.h.version, rb.Reason)
}

// confirm decides whether the update this process is the result of worked: the database answers and every machine
// connected before the restart is back within confirmIn. Then the nodes follow; otherwise back to the old binary.
func (u *updater) confirm(ctx context.Context, p update.Pending) {
	u.set("confirming", "", "")
	deadline := time.Now().Add(u.confirmIn)
	missing := p.Online
	for {
		missing = u.offline(p.Online)
		dbOK := u.h.store == nil
		if !dbOK {
			var x updateSettings
			_, err := u.h.store.Setting(ctx, updateSettingsKey, &x)
			dbOK = err == nil
		}
		if dbOK && len(missing) == 0 {
			break
		}
		if !time.Now().Before(deadline) {
			l := u.h.userLang()
			why := su(l, "reason_no_database")
			if dbOK {
				why = su(l, "reason_machines_missing", i18n.Args{"machines": strings.Join(missing, su(l, "list_sep"))})
			}
			u.rollback(p, why)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	if err := os.Remove(update.PendingPath(u.dir)); err != nil {
		// The watchdog claimed the marker at its deadline and is putting the old version back (update.Watch):
		// recording this update as done would contradict the rollback the old version records.
		u.h.logf("update: %s -> %s: cannot claim confirmation: %v", p.FromVersion, p.ToVersion, err)
		return
	}
	if u.edgeConfirmed != nil {
		u.edgeConfirmed()
	}
	nodeErr := u.updateNodes(ctx, p.Nodes, p.FromVersion, p.Development)
	// A co-located resident can have an old executable inode even when its
	// relay returned during confirmation. It drains on a main-process HUP.
	if nodeErr == nil && u.nodesEnabled() {
		if err := syncLocalNode(ctx, identity.HomeDir(), u.services, &update.Manifest{Version: p.ToVersion, Build: p.ToBuild}); err != nil {
			u.h.logf("update: local node reload: %v", err)
		}
	}
	if p.Development != nil {
		nodeErr = errors.Join(nodeErr, u.publishDevelopmentPackages(p))
	}
	u.mu.Lock()
	u.confirmed = true
	u.mu.Unlock()
	why := ""
	if nodeErr != nil {
		why = su(u.h.userLang(), "err_computer_packages")
		u.h.logf("update: Hub confirmed, computer packages retained for recovery: %v", nodeErr)
	}
	u.record(ctx, updateRecord{FromVersion: p.FromVersion, ToVersion: p.ToVersion, ToBuild: p.ToBuild, Started: p.Started,
		Ended: time.Now(), Result: "updated", Reason: why, Auto: p.Auto, Source: pendingSource(p), Commit: pendingCommit(p), Backup: p.Backup})
	u.set("updated", "", why)
	u.h.logf("update: %s -> %s confirmed", p.FromVersion, p.ToVersion)
	if p.Auto { // one the user started is watched in the app
		l := u.h.userLang()
		u.notify(su(l, "push_updated_title", i18n.Args{"version": p.ToVersion}), su(l, "push_updated_body"))
	}
	update.ClearDevelopmentAdoption(u.dir, p)
	if p.Development != nil || u.nodesEnabled() || u.publishedBuild() != nil {
		u.h.queueDevicesConfig(nodeErr == nil && u.nodesEnabled())
	}
	if p.WebPrev != "" && filepath.Dir(p.WebPrev) == filepath.Join(u.dir, "development") && strings.HasPrefix(filepath.Base(p.WebPrev), "web-previous-") {
		_ = os.RemoveAll(p.WebPrev)
	}
	if nodeErr == nil {
		u.removeDownloads(u.stagedDir(p.ToBuild))
	} // retain failed package preparation for recovery
}

// offline lists which of ids are not connected.
func (u *updater) offline(ids []string) []string {
	var out []string
	for _, id := range ids {
		m := u.h.localMachine(id)
		if m == nil {
			continue // gone from the config: nothing to wait for
		}
		m.mu.Lock()
		on := m.online
		m.mu.Unlock()
		if !on {
			out = append(out, id)
		}
	}
	return out
}

// rollback restores the complete transaction before recording success or
// deleting recovery material. It returns only when restoring or exec fails.
func (u *updater) rollback(p update.Pending, reason string) error {
	u.h.logf("update: going back to %s: %s", p.FromVersion, reason)
	if err := update.RestoreAll(p); err != nil {
		u.h.logf("update: restore %s: %v", p.Prev, err)
		return err
	}
	u.markRolledBack(p, reason)
	// Keep development recovery evidence until the restored executable runs.
	// A successful exec never returns; the old build clears the journal at boot.
	if p.Development == nil && !u.adoptionUnresolved() {
		u.removeDownloads(u.stagedDir(p.ToBuild))
	}
	// The old build reads only the hub.push_sent setting, and an exec skips the stop that writes it (push_grading.go).
	if err := u.h.push.mirrorSent(context.Background()); err != nil {
		u.h.logf("update: delivered pushes for %s: %v", p.FromVersion, err)
	}
	if err := u.exec(p.RunPath()); err != nil {
		u.h.logf("update: run %s: %v", p.RunPath(), err)
		return err
	}
	update.ClearDevelopmentAdoption(u.dir, p)
	if !u.adoptionUnresolved() {
		u.removeDownloads(u.stagedDir(p.ToBuild))
	}
	return nil
}

// updateNodes puts the release's node binaries where "Redeploy" takes them (the manager's, internal/devices deploy.go)
// and has the manager redeploy each connected machine whose node is not this build, one at a time. The builds of
// version from that carry no version file go aside first, whether node updates are on or off: only the release's own
// builds may pass for the hub's version (#1825; a platform the release does not carry, #12958).
func (u *updater) updateNodes(ctx context.Context, staged, from string, development ...*update.DevelopmentCandidate) error {
	dir := identity.NodeBinaryDir()
	info, err := os.Lstat(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		u.h.logf("update: inspect node binary directory: %v", err)
		return err
	}
	// A release publisher owns this pointer and both immutable release trees.
	// Only it may activate packages after the hub is confirmed healthy.
	managed := info != nil && info.Mode()&os.ModeSymlink != 0
	dev := len(development) > 0 && development[0] != nil
	if dev && managed {
		return errors.New("development package directory changed to a managed pointer")
	}
	if !u.nodesEnabled() && !dev {
		if !managed && from != "" && from != u.h.version {
			if moved, _ := setAsideOldNodes(identity.NodeBinaryDir()); len(moved) > 0 {
				u.h.logf("update: node builds of %s set aside (node updates are off, Redeploy fetches %s): %s",
					from, u.h.version, strings.Join(moved, ", "))
			}
		}
		return nil
	}
	if dev && from != "" && from != u.h.version {
		if development[0].BackupID != "" {
			// A saved version may cover fewer platforms. Keep other usable
			// packages, explicitly labelled with their pre-rollback version.
			if err := labelUnstagedNodes(dir, staged, from); err != nil {
				return err
			}
		}
	}
	if staged != "" && !managed {
		build := u.h.build
		build.Version = u.h.version
		if err := update.PublishPackages(staged, dir, build); err != nil {
			return err
		}
	}
	if !managed && from != "" && from != u.h.version && (!dev || development[0].BackupID == "") {
		// Canonical replacements are already labelled. Set aside unlabelled
		// packages for platforms this build did not supply.
		if _, err := setAsideOldNodes(dir); err != nil {
			return err
		}
	}
	return nil
}

// whyNot is why this hub cannot update itself (begin's error); whyNotReason is the reason alone (the view's why_not).
func (u *updater) whyNot() error {
	key, args := u.whyNotKey()
	return i18n.Err("hub-selfupdate.not_release_"+key, args)
}

func (u *updater) whyNotReason(l i18n.Lang) string {
	key, args := u.whyNotKey()
	return su(l, "why_"+key, args)
}

func (u *updater) whyNotKey() (string, i18n.Args) {
	switch u.h.build.Channel {
	case update.Docker:
		return "docker", nil
	case update.Release:
		if err := u.h.updateSource().urlErr; err != nil {
			return "bad_source", i18n.Args{"error": err.Error()}
		}
		if u.h.cfg != nil && u.h.cfg.Security.PrivateNetworkOnly {
			return "private_no_source", nil
		}
		return "no_source", nil
	}
	return "dev", i18n.Args{"version": u.h.version}
}

// su is a hub-selfupdate text in l.
func su(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "hub-selfupdate."+name, args...)
}

func (u *updater) setState(state string) { u.set(state, "", "") }

func (u *updater) isConfirmed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.confirmed
}

// set changes the state the apps see and tells them.
func (u *updater) set(state, progress, errText string) {
	u.mu.Lock()
	u.setLocked(state, progress, errText)
	u.mu.Unlock()
	u.broadcastStatus()
}

func (u *updater) broadcastStatus() {
	u.h.broadcast(map[string]any{"type": "hub_update", "update": u.view()})
}

func (u *updater) setLocked(state, progress, errText string) {
	u.state, u.progress = state, progress
	u.developmentProgress, u.developmentStarted = nil, time.Time{}
	if errText != "" || state != "rolled_back" {
		u.errText = errText
	}
}

// updateView is GET /api/v1/hub/update.
type updateView struct {
	CanCancel       bool                      `json:"can_cancel"`
	Cancelling      bool                      `json:"cancelling"`
	SupportsPreview bool                      `json:"supports_preview"`
	Source          string                    `json:"source"`
	Development     *developmentView          `json:"development,omitempty"`
	Backup          *update.DevelopmentBackup `json:"backup,omitempty"`
	Version         string                    `json:"version"`
	Build           int64                     `json:"build"`
	Channel         string                    `json:"channel"`
	CanUpdate       bool                      `json:"can_update"`
	Why             string                    `json:"why_not,omitempty"`
	Latest          *latestView               `json:"latest,omitempty"`
	State           string                    `json:"state"`
	Progress        string                    `json:"progress,omitempty"`
	Busy            []busyItem                `json:"busy"`
	Error           string                    `json:"error,omitempty"`
	Auto            bool                      `json:"auto"`
	AutoUpdateNodes bool                      `json:"auto_update_nodes"`
	AutoNotice      bool                      `json:"auto_notice"`
	LastCheck       *time.Time                `json:"last_check,omitempty"`
	Confirmed       bool                      `json:"confirmed"`
	Image           *imageView                `json:"image,omitempty"` // a Docker hub's image

	DevelopmentProgress *update.DevelopmentProgress `json:"development_progress,omitempty"`

	// UpdateURL is where the hub looks for releases. UpdateFrom is "hub.json" for a configured source or "build"
	// for an embedded or built-in source. UpdateProxy shows the proxy for checks and downloads without credentials.
	UpdateURL   string `json:"update_url,omitempty"`
	UpdateFrom  string `json:"update_url_from,omitempty"`
	UpdateProxy string `json:"update_proxy,omitempty"`
	// SourceBehind: the newest release the update source has (Latest) is older than this hub and than the release its
	// code contains (Build.Behind, #8108): the source looks stopped, and nothing newer will come from it until update.url
	// names another. Left out otherwise.
	SourceBehind bool `json:"source_behind,omitempty"`
}

type latestView struct {
	Commit    string     `json:"commit,omitempty"`
	UpdatedAt string     `json:"updated_at,omitempty"`
	Branch    string     `json:"branch,omitempty"`
	BackupID  string     `json:"backup_id,omitempty"`
	Version   string     `json:"version"`
	Build     int64      `json:"build"` // 0 for an image (tags have no build numbers)
	Released  *time.Time `json:"released,omitempty"`
	Notes     string     `json:"notes,omitempty"`
	Newer     bool       `json:"newer"`
	Bad       bool       `json:"bad"` // went back before: not installed automatically
	// Held: why a newer release is not installed automatically even with auto-update on (Build.Hold, unreplaced), for
	// people.
	Held string `json:"held,omitempty"`
}

func (u *updater) view() updateView { return u.viewIn(u.h.userLang()) }

// viewIn is the view with its texts for people in l.
func (u *updater) viewIn(l i18n.Lang) updateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	selfUpdates, source := u.h.selfUpdates(), u.h.updateSource()
	v := updateView{SupportsPreview: true, Version: u.h.version, Build: u.h.build.Number, Channel: u.h.build.Channel, CanUpdate: selfUpdates,
		CanCancel: u.attempt != nil, Cancelling: u.attempt != nil && u.attempt.cancelling,
		State: u.state, Progress: u.progress, Busy: u.busy, Error: u.errText, Auto: u.settings.auto() && selfUpdates,
		AutoNotice: selfUpdates && !u.settings.AutoNoticeSeen, Confirmed: u.confirmed,
		AutoUpdateNodes: u.nodesEnabledLocked(), UpdateURL: source.url, UpdateFrom: source.from, UpdateProxy: proxyShown(source.proxy)}
	if p := u.developmentProgress; p != nil && p.Phase == u.state {
		progress := *p
		progress.ElapsedSeconds = max(progress.ElapsedSeconds, int64(time.Since(u.developmentStarted)/time.Second))
		v.DevelopmentProgress = &progress
	}
	v.Source = "release"
	v.Backup = u.settings.DevelopmentBackup
	dv := &developmentView{Configured: u.developmentAvailable(), Branch: "main", CurrentCommit: u.h.build.Commit,
		CurrentUpdatedAt: u.currentUpdatedAtLocked()}
	if !dv.Configured {
		dv.Why = u.developmentUnavailableWhy(l)
	}
	backupConfig := u.developmentBackupConfigLocked()
	dv.BackupDirectory, dv.BackupRetain = backupConfig.Directory, backupConfig.Retain
	dv.BuiltinQuiesce = backupConfig.Builtin && u.developmentQuiesceError(backupConfig) == nil
	if u.h.cfg != nil {
		dv.BackupDefaultDirectory = backup.RecommendedDevelopmentBackupDir(u.h.cfg.dir)
		dv.BackupDirectoryIsDefault = backup.SamePath(backupConfig.Directory, dv.BackupDefaultDirectory)
	}
	v.Development = dv
	if u.developmentLocked() {
		v.Source, v.CanUpdate, v.Auto, v.AutoNotice = "web-dev", u.developmentAvailable(), u.settings.DevelopmentAuto, false
	} else {
		v.Source = u.settings.Source
		if u.betaLocked() {
			selected, err := u.releaseSource()
			if err == nil {
				v.UpdateURL = selected.url
			} else {
				v.UpdateURL, v.UpdateFrom, v.CanUpdate, v.Auto = "", "", false, false
			}
		}
	}
	if v.Busy == nil {
		v.Busy = []busyItem{}
	}
	if !v.CanUpdate && u.developmentLocked() {
		v.Why = u.developmentUnavailableWhy(l)
		if v.Why == "" {
			v.Why = su(l, "err_development_config")
		}
	} else if !v.CanUpdate {
		v.Why = u.whyNotReason(l)
	}
	if !u.lastCheck.IsZero() {
		t := u.lastCheck
		v.LastCheck = &t
	}
	if r := u.release; r != nil {
		v.Latest = &latestView{Version: r.Manifest.Version, Build: r.Manifest.Build, Notes: r.Manifest.Notes,
			Newer: u.h.build.Newer(r.Manifest.Build), Bad: slices.Contains(u.settings.Bad, r.Manifest.Build)}
		if r.Development != nil {
			v.Latest.Commit, v.Latest.Branch = r.Development.Commit, r.Development.Branch
			v.Latest.UpdatedAt = r.Development.UpdatedAt
			v.Latest.BackupID = r.Development.BackupID
			v.Latest.Newer = u.candidateNewer(r)
			v.Latest.Bad = v.Latest.Bad || slices.Contains(u.settings.DevelopmentBad, r.Development.Commit)
			if r.Manifest.Ancestors != "" {
				v.Latest.Held = u.developmentHold(r.Manifest)
			}
		} else if released := r.Manifest.Released; !released.IsZero() {
			v.Latest.Released = &released
		}
		// A deliberately selected saved program version is not evidence that
		// the development Git source stopped publishing releases.
		v.SourceBehind = r.Development == nil && u.h.build.Behind(r.Manifest)
		if r.Development == nil && v.Latest.Newer && selfUpdates {
			v.Latest.Held = u.h.build.Hold(r.Manifest)
			if v.Latest.Held == "" && u.blocked != nil {
				v.Latest.Held = i18n.Localize(u.blocked, l)
			}
		}
	}
	if u.h.build.Channel == update.Docker {
		if im, err := u.dockerImage(); err == nil {
			v.Image = &imageView{Name: im.Name(), Tag: im.Tag, Pinned: im.Pinned()}
		}
		if u.newestImage != "" {
			v.Latest = &latestView{Version: u.newestImage, Newer: update.NewerVersion(u.newestImage, u.h.version)}
		}
	}
	return v
}

// imageView is the image a Docker hub runs, for the app's instructions: a pinned tag has to be changed in .env.
type imageView struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Pinned bool   `json:"pinned"`
}
