package hub

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/update"
)

// loop checks for releases (a minute after starting, then every hour) and installs one when updating itself is
// on and the release is newer and not a build that went back. Each check first brings the Android app the phones
// update from to the release's (syncAndroid): before, so an install's restart does not cut its download short.
func (u *updater) loop(ctx context.Context) {
	wait := updateFirstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = updateCheckEvery
		// One scheduler serves both channels. A channel selected after startup is
		// picked up here too; development updates never fetch mobile artifacts.
		if !u.selectedDevelopment() {
			if !u.h.selfUpdates() && u.h.build.Channel != update.Docker {
				continue
			}
			u.syncAndroid(ctx)
		}
		if _, err := u.check(ctx); err != nil {
			continue
		}
		if u.wantsAuto() {
			go u.install(ctx, true)
		} else {
			u.noteHeld(ctx)
		}
	}
}

// wantsAuto: on, newer, not a bad build, its code contains this one's (Build.Hold), it leaves no program a service
// runs as it was (unreplaced), not already installing or confirming.
func (u *updater) wantsAuto() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.release
	if r != nil && r.Development != nil && r.Development.BackupID != "" {
		return false // Applying a backup is always an explicit manual operation.
	}
	hold := ""
	if r != nil && (r.Development == nil || r.Manifest.Ancestors != "") {
		hold = u.h.build.Hold(r.Manifest)
	}
	return (u.loaded || u.h.store == nil) && u.autoLocked() && !u.updateActiveLocked() && !u.adoptionUnresolved() && u.candidateNewer(r) &&
		!slices.Contains(u.settings.Bad, r.Manifest.Build) && (r.Development == nil || !slices.Contains(u.settings.DevelopmentBad, r.Development.Commit)) && hold == "" && u.blocked == nil
}

// noteHeld writes into the history, once per release, that a newer release was not installed by itself because its
// code may not contain this hub's (Build.Hold): the app shows it as available, with the reason, and the user can
// still install it. Or because it would leave a program a service runs as it was (unreplaced): installing it by hand
// is refused for the same reason.
func (u *updater) noteHeld(ctx context.Context) {
	if err := u.load(ctx); err != nil {
		return
	}
	u.mu.Lock()
	r := u.release
	if !u.autoLocked() || u.updateActiveLocked() || u.adoptionUnresolved() || !u.candidateNewer(r) || slices.Contains(u.settings.Bad, r.Manifest.Build) ||
		(r.Development != nil && (r.Manifest.Ancestors == "" || slices.Contains(u.settings.DevelopmentBad, r.Development.Commit))) {
		u.mu.Unlock()
		return
	}
	why := u.h.build.Hold(r.Manifest)
	if why == "" && u.blocked != nil {
		why = i18n.Localize(u.blocked, u.h.userLang())
	}
	for _, h := range u.settings.History {
		if h.Result == "held" && h.ToBuild == r.Manifest.Build {
			why = ""
		}
	}
	u.mu.Unlock()
	if why == "" {
		return
	}
	u.h.logf("update: %s is newer but not installed by itself: %s", r.Manifest.Version, why)
	now := time.Now()
	record := updateRecord{FromVersion: u.h.version, ToVersion: r.Manifest.Version, ToBuild: r.Manifest.Build,
		Started: now, Ended: now, Result: "held", Reason: why, Auto: true}
	if r.Development != nil {
		record.Source, record.Commit = "web-dev", r.Development.Commit
	}
	u.record(ctx, record)
}

// check reads the release now (a Docker hub: its image's tags).
func (u *updater) check(ctx context.Context) (*update.Published, error) {
	return u.checkMode(ctx, false)
}

func (u *updater) checkMode(ctx context.Context, preview bool) (*update.Published, error) {
	if err := u.load(ctx); err != nil {
		return nil, err
	}
	if !preview {
		u.recordRolledBack(ctx)
	}
	u.mu.Lock()
	if u.checking || (u.developmentLocked() && u.updateActiveLocked()) {
		r := u.release
		u.mu.Unlock()
		return r, nil
	}
	u.checking = true
	u.opGen++
	dev := u.developmentLocked()
	u.mu.Unlock()
	defer func() { u.mu.Lock(); u.checking = false; u.mu.Unlock() }()
	if dev {
		if !preview {
			u.setCheckState("checking", "")
		}
		r, err := u.checkDevelopment(ctx)
		if err != nil {
			u.setCheckState("failed", i18n.Localize(err, u.h.userLang()))
			return nil, err
		}
		if u.candidateNewer(r) {
			u.setCheckState("available", "")
		} else {
			u.setCheckState("idle", "")
		}
		return r, nil
	}
	if u.h.build.Channel == update.Docker {
		return nil, u.checkImage(ctx)
	}
	src, sourceErr := u.releaseSource()
	source := src.url
	if sourceErr != nil {
		u.setCheckState("failed", sourceErr.Error())
		return nil, sourceErr
	}
	if source == "" {
		return nil, errNotRelease
	}
	u.setCheckState("checking", "")
	cctx, cancel := context.WithTimeout(ctx, updateRequestLimit)
	defer cancel()
	r, err := update.CheckChannel(cctx, u.client, source, u.selectedChannel(), u.h.build.Edition)
	if err == nil && u.selectedBeta() {
		// A pointer only names the newest prerelease; download its immutable
		// tag, never the pointer's own placeholder binaries.
		pinned := update.BetaReleaseBase(source, r.Manifest)
		if pinned == "" {
			err = errInvalidBetaVersion
		} else {
			r.Source = pinned
		}
	}
	var blocked error
	if err == nil {
		blocked = u.unreplaced(r.Manifest)
	}
	u.mu.Lock()
	u.lastCheck = time.Now()
	if err == nil {
		u.release, u.releaseRead, u.blocked = r, u.lastCheck, blocked
	}
	u.mu.Unlock()
	if err != nil {
		u.setCheckState("idle", su(u.h.userLang(), "check_failed", i18n.Args{"error": i18n.Localize(err, u.h.userLang())}))
		return nil, err
	}
	u.noteBehind(src, r.Manifest)
	if u.h.build.Newer(r.Manifest.Build) {
		u.setCheckState("available", "")
	} else {
		u.setCheckState("idle", "")
	}
	return r, nil
}

// noteBehind logs that the update source looks stopped (Build.Behind, #8108): its newest release is older than the one
// this hub's code contains, so every check finds nothing newer and the phones no newer Android app. Once per source and
// release, not at every hourly check; again when either changes. The app shows it too (updateView.SourceBehind).
func (u *updater) noteBehind(src updateSource, m *update.Manifest) {
	note := ""
	if u.h.build.Behind(m) {
		note = fmt.Sprintf("%s %d", src.url, m.Build)
	}
	u.mu.Lock()
	again := note == u.behindNote
	u.behindNote = note
	u.mu.Unlock()
	if note == "" || again {
		return
	}
	from := "built in"
	if src.from == updateFromConfig {
		from = "hub.json update.url"
	}
	u.h.logf("update: the update source %s (%s) has nothing newer than %s (build %d), older than this hub's %s: it looks stopped, "+
		"so neither this hub nor its phones get updates from it; hub.json update.url sets another (docs/operations/hub-update.md \"Update source\")",
		src.url, from, m.Version, m.Build, u.h.version)
}

// syncAndroid keeps apps/android, which the phones update from (androidapp.go), at the Android app the release
// carries (docs/operations/hub-update.md "The Android app"): nothing without an update source, or for a release without
// one. It logs an APK it took, and a failure once, until what it finds changes (not every hour).
func (u *updater) syncAndroid(ctx context.Context) {
	source, sourceErr := u.androidSource()
	if sourceErr != nil {
		u.mu.Lock()
		note := "update: beta source: " + sourceErr.Error()
		again := note == u.androidNote
		u.androidNote = note
		u.mu.Unlock()
		if !again {
			u.h.logf("%s", note)
		}
		return
	}
	if source == "" || u.h.cfg == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, androidSyncLimit)
	defer cancel()
	result, r, err := update.SyncAndroid(cctx, u.client, source, filepath.Join(u.h.cfg.dir, "apps", "android", u.androidCacheDir()))
	note := ""
	switch {
	case err != nil && r != nil:
		note = fmt.Sprintf("update: the Android app %q (version code %d) in the release is not offered to the phones: %v", r.VersionName, r.VersionCode, err)
	case err != nil:
		note = fmt.Sprintf("update: the release's Android app: %v", err)
	case result == update.AndroidUpdated:
		note = fmt.Sprintf("update: the phones are offered the release's Android app %q (version code %d)", r.VersionName, r.VersionCode)
	}
	u.mu.Lock()
	again := note == u.androidNote
	u.androidNote = note
	u.mu.Unlock()
	if note != "" && !again {
		u.h.logf("%s", note)
	}
}

// updateActiveLocked includes the new process, where boot clears confirmed before confirm starts and running is false.
// Called with u.mu held.
func (u *updater) updateActiveLocked() bool {
	return u.running || !u.confirmed || u.state == "confirming"
}

// setCheckState leaves an install's state, progress and error alone, but still broadcasts refreshed metadata.
// Check under the same lock as the write, both before and after the request: an install can start while it is in flight.
func (u *updater) setCheckState(state, errText string) {
	u.mu.Lock()
	if !u.updateActiveLocked() {
		u.setLocked(state, "", errText)
	}
	u.mu.Unlock()
	u.broadcastStatus()
}

// freshRelease reads latest.json again for an install script when the release the hub has is older than
// releaseFreshFor: the script checks the download (the newest one on the site) against its sha256s, so sums from
// before the last release would fail every install until the next check. Bounded by releaseRefreshLimit and tried at
// most once every releaseRefreshEvery across requests; when it fails the release the hub had stays. Nothing for a
// Docker hub or a build with nowhere to look.
func (u *updater) freshRelease(ctx context.Context) {
	if u == nil || u.h.build.Channel == update.Docker {
		return
	}
	if err := u.load(ctx); err != nil || u.selectedDevelopment() {
		return
	}
	src, sourceErr := u.releaseSource()
	source := src.url
	if sourceErr != nil {
		u.h.logf("update: beta source: %v", sourceErr)
		return
	}
	if source == "" {
		return
	}
	u.refreshing.Lock()
	defer u.refreshing.Unlock()
	now := time.Now()
	u.mu.Lock()
	if u.developmentLocked() {
		u.mu.Unlock()
		return
	}
	stale := now.Sub(u.releaseRead) >= releaseFreshFor && now.Sub(u.refreshTried) >= releaseRefreshEvery
	if stale {
		u.refreshTried = now
	}
	u.mu.Unlock()
	if !stale {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, releaseRefreshLimit)
	defer cancel()
	r, err := update.CheckChannel(cctx, u.client, source, u.selectedChannel(), u.h.build.Edition)
	if err == nil && u.selectedBeta() {
		r.Source = update.BetaReleaseBase(source, r.Manifest)
		if r.Source == "" {
			err = errInvalidBetaVersion
		}
	}
	if err != nil {
		u.h.logf("update: reading the release for an install script: %v (keeping the one read before)", err)
		return
	}
	blocked := u.unreplaced(r.Manifest)
	u.mu.Lock()
	// An install-script request does not reserve the selected channel. Its
	// release response must not replace a development candidate chosen meanwhile.
	if u.developmentLocked() {
		u.mu.Unlock()
		return
	}
	u.lastCheck = time.Now()
	u.release, u.releaseRead, u.blocked = r, u.lastCheck, blocked
	available := !u.updateActiveLocked() && u.state == "idle" && u.h.build.Newer(r.Manifest.Build)
	if available {
		u.setLocked("available", "", "")
	}
	u.mu.Unlock()
	u.noteBehind(src, r.Manifest)
	if available {
		u.broadcastStatus()
	}
}

// dockerImage is the image this Docker hub runs: the compose file's PERSONA_IMAGE, else the one it was built as.
func (u *updater) dockerImage() (update.Image, error) {
	s := identity.Getenv("PERSONA_IMAGE")
	if s == "" {
		s = u.h.build.Image
	}
	if s == "" {
		return update.Image{}, i18n.Err("hub-selfupdate.err_no_image")
	}
	return update.ParseImage(s)
}

// checkImage asks the registry for the newest release of the image this Docker hub runs (docs/operations/hub-update.md "Docker").
// A private image needs PERSONA_REGISTRY_TOKEN (a read token) in the container's environment.
func (u *updater) checkImage(ctx context.Context) error {
	im, err := u.dockerImage()
	if err != nil {
		u.set("idle", "", i18n.Localize(err, u.h.userLang()))
		return err
	}
	u.setState("checking")
	cctx, cancel := context.WithTimeout(ctx, updateRequestLimit)
	defer cancel()
	newest, err := update.LatestVersion(cctx, u.client, im, identity.Getenv("PERSONA_REGISTRY_TOKEN"))
	u.mu.Lock()
	u.lastCheck = time.Now()
	if err == nil {
		u.newestImage = newest
	}
	u.mu.Unlock()
	switch {
	case errors.Is(err, update.ErrPrivate):
		u.set("idle", "", su(u.h.userLang(), "image_private", i18n.Args{"error": err.Error()}))
	case err != nil:
		u.set("idle", "", su(u.h.userLang(), "image_failed", i18n.Args{"error": err.Error()}))
	case update.NewerVersion(newest, u.h.version):
		u.set("available", "", "")
	default:
		u.set("idle", "", "")
	}
	return err
}
