package hub

import (
	"context"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/update"
)

func (u *updater) developmentBackups(ctx context.Context) ([]update.DevelopmentVersion, error) {
	u.mu.Lock()
	selected := u.developmentLocked()
	u.mu.Unlock()
	if !selected {
		return nil, i18n.Err("hub-selfupdate.err_development_config")
	}
	if err := u.developmentUnavailableErr(); err != nil {
		return nil, err
	}
	var out struct {
		Backups []update.DevelopmentVersion `json:"backups"`
	}
	if err := u.runDevelopment(ctx, "backups", map[string]any{"runtime": u.h.cfg.dir}, &out, nil); err != nil {
		return nil, err
	}
	if out.Backups == nil {
		out.Backups = []update.DevelopmentVersion{}
	}
	return out.Backups, nil
}

// Explicit program rollback uses the same installer, idle wait and confirmation.
// Database dumps and history archives are never inputs to installation.
func (u *updater) beginDevelopmentRestore(ctx context.Context, id string) error {
	u.mu.Lock()
	if !u.developmentLocked() {
		u.mu.Unlock()
		return i18n.Err("hub-selfupdate.err_development_config")
	}
	if err := u.developmentUnavailableErr(); err != nil {
		u.mu.Unlock()
		return err
	}
	if u.checking || u.updateActiveLocked() {
		u.mu.Unlock()
		return errUpdateBusy
	}
	if u.adoptionUnresolved() {
		u.mu.Unlock()
		return i18n.Err("hub-selfupdate.err_development_recovery")
	}
	u.checking = true // reserves source and candidate while reading snapshot metadata
	u.opGen++
	u.mu.Unlock()
	versions, err := u.developmentBackups(ctx)
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checking = false
	if err != nil {
		return err
	}
	var chosen *update.DevelopmentVersion
	for i := range versions {
		if versions[i].ID == id {
			chosen = &versions[i]
			break
		}
	}
	if chosen == nil {
		return i18n.Err("hub-selfupdate.err_development_backup")
	}
	prev := u.settings.DevelopmentAuto
	u.settings.DevelopmentAuto = false
	if err := u.saveLocked(ctx); err != nil {
		u.settings.DevelopmentAuto = prev
		return err
	}
	r := &update.Published{
		Manifest:    &update.Manifest{Version: chosen.Version, Build: chosen.Build, Commit: chosen.Commit, Edition: u.h.build.Edition.Marker()},
		Development: &update.DevelopmentCandidate{Branch: "main", Commit: chosen.Commit, BackupID: chosen.ID, UpdatedAt: chosen.UpdatedAt},
	}
	u.release = r
	u.running, u.now = true, false
	u.opGen++
	u.reserveDevelopmentLocked(ctx)
	u.setLocked("applying_backup", "", "")
	go u.installCandidate(ctx, false, r)
	return nil
}
