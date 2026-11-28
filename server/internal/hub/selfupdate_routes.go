package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/operations/backup"
	"github.com/nwverse/persona/server/internal/update"
)

// developmentBackupRetainMax bounds the snapshot count a Web UI choice may set.
const developmentBackupRetainMax = 50

func (u *updater) routes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/hub/update/backups", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		backups, err := u.developmentBackups(r.Context())
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, map[string]any{"backups": backups})
	})
	api.HandleFunc("POST /api/v1/hub/update/backups/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if err := u.beginDevelopmentRestore(context.WithoutCancel(r.Context()), r.PathValue("id")); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("GET /api/v1/hub/update", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("POST /api/v1/hub/update/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := u.requestCancel(); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		u.broadcastStatus()
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("POST /api/v1/hub/update/check", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		var body struct {
			Preview bool `json:"preview"`
		}
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil && !errors.Is(err, io.EOF) {
			writeBodyErr(w, err, err)
			return
		}
		if _, err := u.checkMode(r.Context(), body.Preview); err != nil && !errors.Is(err, errNotRelease) {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		if !body.Preview {
			if u.wantsAuto() {
				go u.install(context.WithoutCancel(r.Context()), true)
			} else {
				u.noteHeld(r.Context())
			}
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("POST /api/v1/hub/update", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		var body struct {
			Now    bool   `json:"now"`
			Target string `json:"target"`
		}
		// No body (or one that does not decode) is an ordinary update; only an oversized one is refused.
		if err := decodeJSONBody(w, r, &body, 4<<10); bodyTooLarge(err) {
			writeBodyErr(w, err, err)
			return
		}
		// The update outlives this request: it runs on the hub's own context.
		if err := u.beginTarget(context.WithoutCancel(r.Context()), body.Now, body.Target); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("PUT /api/v1/hub/update/settings", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		var body struct {
			Source          *string `json:"source"`
			Auto            *bool   `json:"auto"`
			AutoNoticeSeen  *bool   `json:"auto_notice_seen"`
			AutoUpdateNodes *bool   `json:"auto_update_nodes"`
			// Declarative snapshot settings only: a folder and a count, never a
			// command. An empty folder restores the recommended default.
			DevelopmentBackupDirectory *string `json:"development_backup_directory"`
			DevelopmentBackupRetain    *int    `json:"development_backup_retain"`
		}
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if body.Source != nil && *body.Source != "release" && *body.Source != "web-dev" && *body.Source != update.ChannelBeta {
			writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
			return
		}
		if body.DevelopmentBackupDirectory != nil || body.DevelopmentBackupRetain != nil {
			u.mu.Lock()
			busy := u.updateActiveLocked() || u.checking
			u.mu.Unlock()
			if busy {
				writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
				return
			}
		}
		// The folder is checked with the backup command's own rules before any
		// setting changes (IO, so outside the lock). An explicitly empty folder
		// is the Web choice of the recommended one: it is stored as that path so
		// it wins over hub.json's directory, while an omitted field keeps it.
		var backupDir *string
		if body.DevelopmentBackupDirectory != nil {
			dir := *body.DevelopmentBackupDirectory
			if dir == "" {
				dir = backup.RecommendedDevelopmentBackupDir(u.h.cfg.dir)
			}
			validated, err := backup.ValidateOutputDirectory(u.h.cfg.dir, dir)
			if err != nil {
				writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_development_backup_path", i18n.Args{"error": err.Error()}))
				return
			}
			backupDir = &validated
		}
		if body.DevelopmentBackupRetain != nil && (*body.DevelopmentBackupRetain < 1 || *body.DevelopmentBackupRetain > developmentBackupRetainMax) {
			writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_development_backup_retain", i18n.Args{"max": developmentBackupRetainMax}))
			return
		}
		var checkedCurrent string
		var checkedOpGen uint64
		var checkedRecovery bool
		if backupDir != nil {
			// Snapshots in the folder being left must not be stranded: the
			// helper's coordination marker and its incomplete snapshots both
			// stop any retry until someone looked at them.
			u.mu.Lock()
			if u.updateActiveLocked() || u.checking {
				u.mu.Unlock()
				writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
				return
			}
			checkedCurrent = u.developmentBackupConfigLocked().Directory
			checkedOpGen = u.opGen
			u.mu.Unlock()
			if !backup.SamePath(*backupDir, checkedCurrent) {
				if err := backup.UnrecoveredSnapshot(checkedCurrent); err != nil {
					writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_development_backup_recovery", i18n.Args{"error": err.Error()}))
					return
				}
				checkedRecovery = true
			}
			if hook := u.testAfterBackupRecoveryCheck; hook != nil {
				hook()
			}
		}
		u.mu.Lock()
		if err := u.loadLocked(r.Context()); err != nil {
			u.mu.Unlock()
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		prev := u.settings
		if body.Source != nil && u.adoptionUnresolved() {
			u.mu.Unlock()
			writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_development_recovery"))
			return
		}
		if body.Auto != nil && *body.Auto && (u.developmentLocked() || (body.Source != nil && *body.Source == "web-dev")) && !u.developmentAvailable() {
			u.mu.Unlock()
			writeErr(w, http.StatusConflict, u.developmentUnavailableErr())
			return
		}
		if body.Source != nil && *body.Source != u.settings.Source {
			if u.updateActiveLocked() || u.checking {
				u.mu.Unlock()
				writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
				return
			}
			if *body.Source == "web-dev" && !u.developmentAvailable() {
				u.mu.Unlock()
				writeErr(w, http.StatusConflict, u.developmentUnavailableErr())
				return
			}
			if *body.Source == update.ChannelBeta {
				if _, err := update.BetaFeed(u.h.updateURL()); err != nil {
					u.mu.Unlock()
					writeErr(w, http.StatusConflict, errInvalidBetaVersion)
					return
				}
			}
		}
		if backupDir != nil || body.DevelopmentBackupRetain != nil {
			if u.updateActiveLocked() || u.checking {
				u.mu.Unlock()
				writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
				return
			}
			if backupDir != nil {
				current := u.developmentBackupConfigLocked().Directory
				sameAsChecked := current == checkedCurrent || backup.SamePath(current, checkedCurrent)
				if !sameAsChecked || (!backup.SamePath(*backupDir, current) && !checkedRecovery) {
					u.mu.Unlock()
					writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
					return
				}
				if !backup.SamePath(*backupDir, current) {
					// Re-check under u.mu so a failed update or helper marker
					// created after the unlocked check cannot be stranded.
					if err := backup.UnrecoveredSnapshot(current); err != nil {
						u.mu.Unlock()
						writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_development_backup_recovery", i18n.Args{"error": err.Error()}))
						return
					}
				}
				if u.opGen != checkedOpGen {
					u.mu.Unlock()
					writeErr(w, http.StatusConflict, i18n.Err("hub-selfupdate.err_busy"))
					return
				}
			}
		}
		if body.Source != nil && *body.Source != u.settings.Source {
			u.settings.Source = *body.Source
			u.settings.DevelopmentAuto = false // entering a channel never opts into automatic adoption
			u.settings.DevelopmentBackup = nil
		}
		if body.AutoUpdateNodes != nil {
			enabled := *body.AutoUpdateNodes
			u.settings.AutoUpdateNodes = &enabled
		}
		if backupDir != nil {
			u.settings.DevelopmentBackupDirectory = backupDir
		}
		if body.DevelopmentBackupRetain != nil {
			u.settings.DevelopmentBackupRetain = *body.DevelopmentBackupRetain
		}
		if body.Auto != nil {
			a := *body.Auto
			if u.developmentLocked() {
				u.settings.DevelopmentAuto = a
			} else {
				u.settings.Auto = &a
			}
			u.settings.AutoNoticeSeen = true // they chose: no need to tell them again
		}
		if body.AutoNoticeSeen != nil {
			u.settings.AutoNoticeSeen = *body.AutoNoticeSeen
		}
		err := u.saveLocked(r.Context())
		if err != nil {
			// Not saved: an update waiting for idle and the view sent to the apps keep acting on the stored choice.
			u.settings = prev
		} else if u.settings.Source != prev.Source {
			u.release, u.blocked, u.lastCheck = nil, nil, time.Time{}
			u.releaseRead, u.refreshTried = time.Time{}, time.Time{}
			u.setLocked("idle", "", "")
			u.androidNote, u.behindNote = "", ""
		}
		u.mu.Unlock()
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if body.AutoUpdateNodes != nil {
			u.h.queueDevicesConfig(*body.AutoUpdateNodes)
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("GET /api/v1/hub/update/history", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		u.mu.Lock()
		out := slices.Clone(u.settings.History)
		u.mu.Unlock()
		slices.Reverse(out)
		if out == nil {
			out = []updateRecord{}
		}
		writeJSON(w, map[string]any{"history": out})
	})
}
