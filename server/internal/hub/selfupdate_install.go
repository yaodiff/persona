package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
	"github.com/nwverse/persona/server/internal/update"
)

// begin starts an update the user asked for; now skips waiting for idle ("Update now" in the second confirmation).
func (u *updater) begin(ctx context.Context, now bool) error {
	return u.beginTarget(ctx, now, "")
}

func (u *updater) beginTarget(ctx context.Context, now bool, target string) error {
	if !u.h.selfUpdates() && !(u.selectedDevelopment() && u.developmentAvailable()) {
		return u.whyNot()
	}
	u.mu.Lock()
	if u.checking {
		u.mu.Unlock()
		return errUpdateBusy
	}
	if u.updateActiveLocked() {
		if now {
			u.now = true
		}
		u.mu.Unlock()
		return nil
	}
	if u.adoptionUnresolved() {
		u.mu.Unlock()
		return i18n.Err("hub-selfupdate.err_development_recovery")
	}
	if target == "latest" && u.developmentLocked() {
		u.running, u.now = true, now
		u.opGen++
		u.reserveDevelopmentLocked(ctx)
		resolveCtx := u.attempt.ctx
		u.setLocked("resolving", "", "")
		u.mu.Unlock()
		u.broadcastStatus()
		go u.installLatestDevelopment(ctx, resolveCtx)
		return nil
	}
	r := u.release
	if !u.candidateNewer(r) {
		u.mu.Unlock()
		return errNoUpdate
	}
	if r.Development == nil {
		// Service discovery can wait for systemctl/plutil. Keep status/settings
		// responsive, then verify the selection before reserving this object.
		u.mu.Unlock()
		err := u.unreplaced(r.Manifest)
		u.mu.Lock()
		if u.checking || u.release != r || u.adoptionUnresolved() {
			u.mu.Unlock()
			return errUpdateBusy
		}
		if u.updateActiveLocked() {
			u.mu.Unlock()
			// Discovery began before this attempt existed. Its interruption
			// consent cannot be transferred to the newly accepted operation.
			return errUpdateBusy
		}
		if err != nil {
			u.mu.Unlock()
			return err
		}
	}
	// Reserve the selected object before returning, so a settings write or second
	// trigger cannot change the candidate between acceptance and goroutine start.
	u.running = true
	u.opGen++
	if r.Development != nil {
		u.reserveDevelopmentLocked(ctx)
		u.setLocked("building", "", "")
	} else {
		u.setLocked("downloading", "", "")
	}
	u.now = now
	u.mu.Unlock()
	go u.installCandidate(ctx, false, r)
	return nil
}

// The reservation spans resolution and the build, so later checks cannot change its input.
func (u *updater) installLatestDevelopment(ctx, resolveCtx context.Context) {
	started := time.Now()
	r, err := u.resolveDevelopment(resolveCtx)
	u.mu.Lock()
	if u.attempt.cancelling {
		u.recordCancelledLocked(ctx, updateRecord{FromVersion: u.h.version, Source: "web-dev", Started: started})
	} else if err == nil {
		u.release, u.blocked, u.lastCheck = r, nil, time.Now()
		if u.candidateNewer(r) {
			u.mu.Unlock()
			u.installCandidate(ctx, false, r)
			return
		}
		u.setLocked("idle", "", "")
	} else {
		u.setLocked("failed", "", i18n.Localize(err, u.h.userLang()))
	}
	u.finishDevelopmentLocked()
	u.running, u.now = false, false
	u.mu.Unlock()
	u.broadcastStatus()
}

// install is one update: download, verify, wait for idle, swap, restart. It returns only when it did not restart.
func (u *updater) install(ctx context.Context, auto bool) {
	u.mu.Lock()
	if u.updateActiveLocked() || u.adoptionUnresolved() || u.checking || u.release == nil {
		u.mu.Unlock()
		return
	}
	u.running = true
	u.opGen++
	r := u.release
	if r.Development != nil {
		u.reserveDevelopmentLocked(ctx)
	}
	u.mu.Unlock()
	u.installCandidate(ctx, auto, r)
}

func (u *updater) installCandidate(parent context.Context, auto bool, candidate *update.Published) {
	ctx := parent
	u.mu.Lock()
	if u.attempt != nil {
		ctx = u.attempt.ctx
	}
	u.mu.Unlock()
	copy := *candidate
	manifest := *candidate.Manifest
	copy.Manifest = &manifest
	r := &copy // checks and views can retain the original immutable candidate
	started := time.Now()
	// What this update downloads goes under staged. Once it fails nothing reads that again (another try downloads
	// afresh), so it goes before another install can start.
	staged := u.stagedDir(r.Manifest.Build)
	var backup *update.DevelopmentBackup
	var adoption update.Pending
	swapped := false

	fail := func(err error) {
		u.removeDevelopmentBuild(r)
		if !swapped {
			update.ClearDevelopmentAdoption(u.dir, adoption)
		}
		recoveryRetained := swapped || u.adoptionUnresolved()
		if !recoveryRetained {
			u.removeDownloads(staged)
		}
		why := i18n.Localize(err, u.h.userLang())
		if recoveryRetained {
			why += "; " + su(u.h.userLang(), "err_development_recovery")
		}
		u.mu.Lock()
		// Finish this attempt's history before a phone sees the failure or starts another install.
		record := updateRecord{FromVersion: u.h.version, ToVersion: r.Manifest.Version, ToBuild: r.Manifest.Build,
			Started: started, Ended: time.Now(), Result: "failed", Reason: why, Auto: auto, Source: pendingSource(update.Pending{Development: r.Development}), Commit: r.Manifest.Commit, Backup: backup}
		// A failed snapshot still needs its real error and recovery instructions.
		if u.attempt != nil && u.attempt.cancelling && !recoveryRetained && (u.state != "backing_up" || errors.Is(err, context.Canceled)) {
			u.recordCancelledLocked(parent, record)
		} else {
			u.recordLocked(parent, record)
			u.setLocked("failed", "", why)
		}
		u.finishDevelopmentLocked()
		u.running, u.now = false, false
		u.busy = nil
		if r.Development != nil && r.Development.BackupID != "" {
			u.release = nil // A failed restore is not the latest main candidate.
		}
		u.mu.Unlock()
		u.broadcastStatus()
		u.h.logf("update to %s: %v", r.Manifest.Version, err)
	}
	if r.Development != nil {
		if _, err := os.Lstat(update.DevelopmentAdoptionPath(u.dir)); !errors.Is(err, os.ErrNotExist) {
			fail(i18n.Err("hub-selfupdate.err_development_recovery"))
			return
		}
		if err := u.preflightDevelopment(ctx); err != nil {
			fail(err)
			return
		}
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		if err := u.buildDevelopment(ctx, r); err != nil {
			fail(err)
			return
		}
		u.mu.Lock()
		u.release = r
		u.mu.Unlock()
		if auto && u.developmentHold(r.Manifest) != "" {
			fail(errors.New(u.developmentHold(r.Manifest)))
			return
		}
	}
	m := r.Manifest
	file, ok := m.Files[runtime.GOOS+"-"+runtime.GOARCH]
	if !ok {
		fail(i18n.Err("hub-selfupdate.err_no_binary", i18n.Args{"platform": runtime.GOOS + "/" + runtime.GOARCH}))
		return
	}
	if err := u.unreplacedCandidate(r); err != nil {
		fail(err)
		return
	}
	u.set("downloading", "hub", "")
	bin, err := u.download(ctx, m, file, staged, r.Source)
	if err != nil {
		fail(err)
		return
	}
	if err := update.CheckBinary(ctx, bin, m); err != nil {
		fail(err)
		return
	}
	// The programs the hub runs beside it (persona-hub and persona-manager): this platform's build of each, checked like personad. One
	// the release has no build of keeps its old file (persona-hub and persona-manager only when no service runs it:
	// unreplaced).
	programs := map[string]string{} // name -> the downloaded binary
	for _, name := range update.ProgramNames {
		f, ok := m.Programs[name][runtime.GOOS+"-"+runtime.GOARCH]
		if !ok {
			continue
		}
		u.set("downloading", name, "")
		pb, err := u.download(ctx, m, f, filepath.Join(staged, "programs"), r.Source)
		if err != nil {
			fail(err)
			return
		}
		if err := update.CheckBinary(ctx, pb, m); err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
			return
		}
		programs[name] = pb
	}
	nodes, err := u.stageNodesFrom(ctx, m, staged, bin, r.Source, r.Development != nil, programs)
	if err != nil {
		fail(err)
		return
	}
	if r.Development != nil {
		// Every program and Node package has an independent verified copy in
		// staged. Only the disposable checkout/build output is removed here;
		// staged survives idle waiting and exec until confirmation/rollback.
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		if err := u.stageDevelopmentPackages(r, staged); err != nil {
			fail(err)
			return
		}
		u.removeDevelopmentBuild(r)
		u.mu.Lock()
		r.Source = ""
		u.mu.Unlock() // downloads are private staged copies now, including across exec
	}
	// Wait until nothing the restart would interrupt is going on (or the user said "Update now").
	waitStarted := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			fail(err)
			return
		}
		busy := u.h.updateBusy()
		u.mu.Lock()
		u.busy = busy
		now, autoOn := u.now, u.autoLocked()
		u.mu.Unlock()
		if len(busy) == 0 || now {
			break
		}
		if auto && !autoOn {
			fail(errUpdateAgain)
			return
		}
		if !auto && time.Since(waitStarted) > u.manualIdle {
			fail(errUpdateBusy)
			return
		}
		u.set("waiting_idle", "", "")
		select {
		case <-ctx.Done():
			fail(ctx.Err())
			return
		case <-time.After(u.idleEvery):
		}
	}
	target, err := u.target()
	if err != nil {
		fail(err)
		return
	}
	if err := u.unreplacedCandidate(r); err != nil { // a service pointed at a program meanwhile
		fail(err)
		return
	}
	if r.Development != nil {
		u.mu.Lock()
		enabled := u.autoLocked()
		u.mu.Unlock()
		if auto && !enabled {
			fail(errUpdateAgain)
			return
		}
		if backup, err = u.backupDevelopment(parent, r, target); err != nil {
			fail(err)
			return
		}
	}
	p := update.Pending{FromVersion: u.h.version, FromBuild: u.h.build.Number, ToVersion: m.Version, ToBuild: m.Build,
		Target: target, Started: started, Online: u.onlineIDs(), Auto: auto, PID: os.Getpid(), Args: os.Args,
		HealthURL: u.healthURL(), Nodes: nodes, Development: r.Development, Backup: backup}
	if r.Development != nil {
		if err := u.claimDevelopmentAdoption(); err != nil {
			fail(err)
			return
		}
		ctx = parent
		if err := update.BeginDevelopmentAdoption(u.dir, p); err != nil {
			fail(err)
			return
		}
		u.broadcastStatus()
		adoption = p
		for _, name := range update.ProgramNames {
			if _, ok := programs[name]; ok {
				p.Programs = append(p.Programs, update.Swapped{Target: filepath.Join(filepath.Dir(target), name)})
			}
		}
		p.Run = runAs(u.self, p.Programs)
		if err := update.PrepareDevelopmentAdoption(u.dir, &p); err != nil {
			fail(err) // no live program has been changed
			return
		}
	}
	restore := func() {
		if err := update.RestoreAll(p); err == nil {
			swapped = false
		} else {
			u.h.logf("update: restore failed, recovery retained: %v", err)
		}
	}
	var prev string
	if r.Development != nil {
		// From here on the durable journal owns all recovery material, even
		// when a replacement fails partway through the transaction.
		swapped = true
		prev, err = p.Prev, update.InstallPrepared(target, bin)
	} else {
		prev, err = update.Swap(target, bin)
	}
	if err != nil {
		if swapped {
			restore()
		}
		fail(err)
		return
	}
	swapped = true
	p.Prev = prev
	for _, name := range update.ProgramNames {
		if pb, ok := programs[name]; ok {
			var err error
			programTarget := filepath.Join(filepath.Dir(target), name)
			if r.Development != nil {
				err = update.InstallPrepared(programTarget, pb)
			} else {
				var sw update.Swapped
				sw, err = update.SwapProgram(programTarget, pb)
				p.Programs = append(p.Programs, sw)
			}
			if err != nil {
				restore()
				fail(fmt.Errorf("%s: %w", name, err))
				return
			}
		}
	}
	p.Run = runAs(u.self, p.Programs)
	if r.Development != nil {
		if err := u.detachDevelopmentWeb(&p); err != nil {
			restore()
			fail(err)
			return
		}
	}
	if err := update.Save(update.PendingPath(u.dir), p); err != nil {
		restore()
		fail(err)
		return
	}
	if u.handover != nil {
		// Blue-green (docs/operations/hub-restart.md "Blue-green switching: the hub's edge"): the edge starts the new binary next to this one and hands over
		// only once it checked itself; this process goes on serving until then, and stops when the edge releases it.
		// The edge is the watchdog too (it goes back when the new one does not confirm the update in time).
		u.set("restarting", "", "")
		u.h.logf("update: handing over to %s", m.Version)
		if err := u.handover(ctx, p.RunPath(), m.Build); err != nil {
			restore()
			if !swapped {
				_ = os.Remove(update.PendingPath(u.dir))
			}
			fail(i18n.Err("hub-selfupdate.err_no_start", i18n.Args{"error": err.Error()}))
		}
		return
	}
	if err := u.startWatch(prev, update.PendingPath(u.dir)); err != nil {
		u.h.logf("update: watchdog: %v (the new version still checks itself)", err)
	}
	u.set("restarting", "", "")
	u.h.logf("update: restarting into %s", m.Version)
	time.Sleep(500 * time.Millisecond) // the apps get the restarting event before the socket goes
	if err := u.exec(p.RunPath()); err != nil {
		restore()
		if !swapped {
			_ = os.Remove(update.PendingPath(u.dir))
		}
		fail(err)
	}
}

// download fetches f of release m, the one the update installs, into dir: from the release's own address where the
// update source has one, so a release published since the check cannot serve its files in m's place (#9263). When
// the source moved on to another release all the same (update.MovedError), the hub offers that one from now on, as a
// check would, and the update fails saying so rather than that the download did not match.
// localSource names the prepared artifacts of a development build or retained backup. Those files are already
// pinned to the candidate; never replace them or its offer with a release from the configured moving source.
func (u *updater) download(ctx context.Context, m *update.Manifest, f update.File, dir, localSource string) (string, error) {
	if localSource != "" {
		return update.Download(ctx, u.client, localSource, f, dir)
	}
	src, sourceErr := u.installSource(m)
	if sourceErr != nil {
		return "", sourceErr
	}
	bin, err := update.DownloadRelease(ctx, u.client, src.url, m, f, dir)
	var moved *update.MovedError
	if !errors.As(err, &moved) {
		return bin, err
	}
	u.h.logf("update: %v", err)
	blocked := u.unreplaced(moved.Now.Manifest)
	u.mu.Lock()
	u.lastCheck = time.Now()
	u.release, u.releaseRead, u.blocked = moved.Now, u.lastCheck, blocked
	u.mu.Unlock()
	u.noteBehind(src, moved.Now.Manifest)
	return "", i18n.Err("hub-selfupdate.err_release_moved", i18n.Args{"version": m.Version, "newer": moved.Now.Manifest.Version})
}

// stagedDir is where the update to build downloads its files: the hub's binary, the node binaries in nodes/ and the
// programs in programs/ (docs/operations/hub-update.md "Updating").
func (u *updater) stagedDir(build int64) string {
	return filepath.Join(u.dir, strconv.FormatInt(build, 10))
}

// removeDownloads removes an update's downloads once nothing reads them any more: after the update failed, was
// confirmed (its node binaries are in NodeBinaryDir by then) or went back. Each one is tens to hundreds of MB.
func (u *updater) removeDownloads(staged string) {
	if err := os.RemoveAll(staged); err != nil {
		u.h.logf("update: remove the downloads in %s: %v", staged, err)
	}
}

// pruneDownloads removes the downloads of every update but the one to build keep (0: none) when the hub starts: one
// that went back through the watchdog or the edge (both start the old build, which never knew of it), one a stop
// cut short, and those an older build left. Only <build>/ directories go; the markers and logs beside them stay.
func (u *updater) pruneDownloads(keep int64) {
	// A failed or interrupted adoption still owns its staged programs/Nodes.
	// An unreadable journal has unknown effects, so leave all staging intact.
	var adoption update.Pending
	adopting, err := update.Load(update.DevelopmentAdoptionPath(u.dir), &adoption)
	if err != nil || adopting && adoption.ToBuild <= 0 {
		return
	}
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			u.h.logf("update: list the downloads: %v", err)
		}
		return
	}
	for _, e := range entries {
		build, err := strconv.ParseInt(e.Name(), 10, 64)
		if !e.IsDir() || err != nil || build <= 0 || build == keep || adopting && build == adoption.ToBuild || strconv.FormatInt(build, 10) != e.Name() {
			continue
		}
		u.removeDownloads(filepath.Join(u.dir, e.Name()))
	}
}

func (u *updater) onlineIDs() []string {
	var out []string
	for _, m := range u.h.machinesNow() {
		m.mu.Lock()
		if m.online {
			out = append(out, m.cfg.ID)
		}
		m.mu.Unlock()
	}
	return out
}

func (u *updater) healthURL() string {
	host, port, err := net.SplitHostPort(u.h.cfg.Listen)
	if err != nil || port == "" {
		port = "18660"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/health"
}

// updateBusy is what a restart would interrupt: any conversation working or waiting for the user (the assistant's
// and every agent's), and a machine being redeployed. A conversation whose only cards are the assistant's ask_user
// asked without waiting is not (#7392): no turn waits on them, and they outlive a restart (the manager claims them
// back from its record, docs/assistant/assistant.md "Asking without holding the turn"), so such a card, up for as long
// as 2 hours, does not hold the update back.
func (h *Hub) updateBusy() []busyItem {
	var out []busyItem
	unwaited := h.unwaitedCards()
	for _, m := range h.machinesNow() {
		if m.snapshot().Deploying {
			out = append(out, busyItem{MachineID: m.cfg.ID, Machine: m.name(), Title: su(h.userLang(), "busy_redeploying")})
		}
		m.mu.Lock()
		resident := m.info.NodeUpdate // advertised only by a resident supervisor
		m.mu.Unlock()
		// Only read, and no copies of them: the manager stamp is not read here. The archived rows told apart
		// (Machine.archived) as well, as when they were in the list.
		list, archived := m.keptWhole()
		for _, rows := range [2][]proto.Session{list, archived} {
			for i := range rows {
				s := &rows[i]
				// Terminal-host sessions outlive the node transport: herdr's and the Herdr App's panes, and Orca's
				// terminals and chats, belong to that app's own process. Unknown hosts and direct assistant
				// processes still wait: an ephemeral SSH node may own them.
				if h.slot != nil && !h.upd.nodesEnabled() && m.cfg.Kind != "local" &&
					(resident || s.Host == proto.EngineHerdr || s.Host == proto.EngineHerdrCore || s.Host == proto.EngineOrca) {
					continue
				}
				// On a turn, not working on in the background (#2228): a server an agent left running would hold the
				// update back for as long as it runs, and the restart does not stop it.
				if s.Status == proto.StatusWorking || s.State == proto.StateNeedsYou && !waitsOnlyUnwaited(s, unwaited[[2]string{m.cfg.ID, s.SessionID}]) {
					out = append(out, busyItem{MachineID: m.cfg.ID, Machine: m.name(), SessionID: s.SessionID, Title: agentTitle(*s)})
				}
			}
		}
	}
	return out
}

// unwaitedCards counts each session's authoritative pending cards (by machine and session): all of them, and those that
// are the assistant's ask_user asked without waiting (proto.Interaction.AskedWithoutWaiting). Display copies never
// make a destination conversation block a restart on their own.
func (h *Hub) unwaitedCards() map[[2]string][2]int {
	out := map[[2]string][2]int{}
	for _, ix := range h.pendingInteractions() {
		if ix.MirrorOf != "" {
			continue // restart interruption is counted from authoritative operations only
		}
		k := [2]string{ix.MachineID, ix.SessionID}
		n := out[k]
		n[0]++
		if ix.AskedWithoutWaiting() {
			n[1]++
		}
		out[k] = n
	}
	return out
}

// waitsOnlyUnwaited: s needs the user only for cards no turn waits on: its turn is not running, nothing blocks its
// terminal, and every card it counts is one asked without waiting (cards, all and unwaited, from unwaitedCards). A
// card the manager counts that the hub was not shown keeps it busy.
func waitsOnlyUnwaited(s *proto.Session, cards [2]int) bool {
	all, unwaited := cards[0], cards[1]
	return s.Status != proto.StatusWorking && s.Status != proto.StatusBlocked && unwaited > 0 && unwaited == all &&
		s.PendingInteractions <= unwaited
}

// agentTitle is an agent's title for the update's busy list.
func agentTitle(s proto.Session) string {
	t := transcript.Title(s.Title)
	if t == "" || t == "Claude Code" {
		t = path.Base(s.Cwd)
	}
	return clip(oneLine(t), 24)
}
