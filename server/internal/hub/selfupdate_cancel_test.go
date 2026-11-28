package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/operations/backup"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

// Produce a private build workdir with the same ownership and manifest checks
// as the Python helper. Staging must move these artifacts and remove the workdir.
func cancellationBuildFixture(dir string, in map[string]any, result any) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	work, err := os.MkdirTemp(dir, "build-")
	if err != nil {
		return err
	}
	artifacts := filepath.Join(work, "artifacts")
	if err := os.Mkdir(artifacts, 0700); err != nil {
		return err
	}
	m := update.Manifest{Version: in["version"].(string), Build: in["build"].(int64), Commit: in["candidate"].(*update.DevelopmentCandidate).Commit}
	program := []byte(fmt.Sprintf("#!/bin/sh\necho %s\necho 'channel release, build %d'\n", m.Version, m.Build))
	for _, name := range append([]string{"personad"}, update.ProgramNames...) {
		if err := os.WriteFile(filepath.Join(artifacts, name+"-"+runtime.GOOS+"-"+runtime.GOARCH+".gz"), gz(program), 0600); err != nil {
			return err
		}
	}
	manifest, err := update.BuildManifest(artifacts, m)
	if err != nil {
		return err
	}
	if err := update.Save(filepath.Join(artifacts, "manifest.json"), manifest); err != nil {
		return err
	}
	data, err := json.Marshal(map[string]any{"artifacts": artifacts, "ancestors": []string{m.Commit}})
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func TestDevelopmentCancelUntilAdoption(t *testing.T) {
	for _, phase := range []string{"resolve", "preflight", "build", "waiting_idle", "backup", "backup_failure", "archive_programs", "restarting"} {
		t.Run(phase, func(t *testing.T) {
			h, machine, target, _ := updatingHub(t, newPublisher(t, "unused", 202611021700), "release")
			h.store = newMeshStore()
			h.cfg.Token = "fixture-update-owner"
			u := h.upd
			u.client = newUpdater(h).client
			u.settings.Source, u.loaded = "web-dev", true
			h.build.Commit = strings.Repeat("a", 40)
			t.Setenv("PERSONA_NODE_BINARIES", filepath.Join(t.TempDir(), "nodes"))
			h.cfg.Update.Development = &update.DevelopmentConfig{}
			h.cfg.DatabaseURL = "fixture-db"
			if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"), []byte(`{"database_url":"fixture-db"}`), 0600); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			for name, body := range map[string]string{
				"pg_dump":    "for dest do :; done\nprintf PGDMP > \"$dest\"",
				"pg_restore": "echo fixture",
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Stop the first attempt exactly inside the requested phase. Backup
			// runs the real snapshot helper, gated while writers are quiesced.
			entered, release := make(chan struct{}), make(chan struct{})
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			// Hold the real compressor on a FIFO until the HTTP assertions finish.
			var programPipe *os.File
			if phase == "archive_programs" {
				path := filepath.Join(bin, "program-input")
				if err := exec.Command("mkfifo", path).Run(); err != nil {
					t.Fatal(err)
				}
				var err error
				programPipe, err = os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { programPipe.Close() })
				if _, err := programPipe.WriteString("fixture saved program"); err != nil {
					t.Fatal(err)
				}
			}
			first := true
			u.devRun = func(ctx context.Context, cfg *update.DevelopmentConfig, dir, op string, in map[string]any, out any, resume func(), report func(update.DevelopmentProgress)) error {
				if first && (phase == op || phase == "backup_failure" && op == "backup") {
					first = false
					close(entered)
					if op != "backup" {
						<-ctx.Done()
						return ctx.Err()
					}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				switch op {
				case "resolve":
					*out.(*update.DevelopmentCandidate) = update.DevelopmentCandidate{Branch: "main", Commit: strings.Repeat("b", 40)}
					return nil
				case "build":
					return cancellationBuildFixture(dir, in, out)
				default:
					if first && phase == "archive_programs" && op == "backup" {
						first = false
						in["executables"].(map[string]string)["personad"] = programPipe.Name()
						return update.DevelopmentRequest(ctx, cfg, dir, op, in, out, resume, func(p update.DevelopmentProgress) {
							report(p)
							if p.Step == "archive_programs" {
								close(entered)
							}
						})
					}
					return update.DevelopmentRequest(ctx, cfg, dir, op, in, out, resume, report)
				}
			}
			handed := make(chan struct{})
			u.handover = func(context.Context, string, int64) error { close(handed); return nil }
			if phase == "waiting_idle" {
				setSessions(machine, proto.Session{SessionID: "working", Status: proto.StatusWorking})
				u.idleEvery = time.Hour
			}
			mux := http.NewServeMux()
			u.routes(mux)
			mux.HandleFunc("POST /test-write", func(http.ResponseWriter, *http.Request) {})
			gate := h.auth(h.quiesceGate(mux))
			request := func(method, path, body string, code int) *httptest.ResponseRecorder {
				t.Helper()
				w := httptest.NewRecorder()
				req := httptest.NewRequest(method, path, strings.NewReader(body))
				req.RemoteAddr = "127.0.0.1:1234"
				req.Header.Set("Authorization", "Bearer fixture-update-owner")
				gate.ServeHTTP(w, req)
				if w.Code != code {
					t.Fatalf("%s %s returned %d, want %d: %s", method, path, w.Code, code, w.Body)
				}
				return w
			}
			const route = "/api/v1/hub/update"
			status := func() updateView {
				t.Helper()
				var v updateView
				w := request("GET", route, "", 200)
				if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			request("POST", route, `{"target":"latest"}`, 200)
			switch phase {
			case "waiting_idle":
				waitForUpdate(t, u, "idle wait", func() bool { return status().State == "waiting_idle" })
			case "restarting":
				select {
				case <-handed:
				case <-ctx.Done():
					t.Fatal("handover did not run", status())
				}
				request("POST", route+"/cancel", "", 409)
				if v := status(); v.CanCancel || v.Cancelling || !u.adoptionUnresolved() {
					t.Fatal("late cancel disturbed adoption", v)
				}
				for _, path := range []string{target + ".prev", update.PendingPath(u.dir)} {
					if _, err := os.Stat(path); err != nil {
						t.Fatal(err)
					}
				}
				return
			default:
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("phase did not start", status())
				}
			}
			if !status().CanCancel {
				t.Fatal("cancel not offered")
			}
			if phase == "backup" || phase == "backup_failure" {
				request("POST", "/test-write", "", 503)
				unauth := httptest.NewRecorder()
				gate.ServeHTTP(unauth, httptest.NewRequest(http.MethodPost, route+"/cancel", nil))
				if unauth.Code != http.StatusUnauthorized || status().Cancelling {
					t.Fatal("unauthenticated cancellation", unauth.Code)
				}
			}
			request("POST", route+"/cancel", "", 200)
			if phase == "backup" || phase == "backup_failure" {
				request("POST", route+"/cancel", "", 200)
				if v := status(); !v.Cancelling || !v.CanCancel || v.State != "backing_up" {
					t.Fatal("backup cancel not pending", v)
				}
				request("POST", "/test-write", "", 503)
			}
			if phase == "archive_programs" {
				request("POST", "/test-write", "", 200)
				if v := status(); !v.Cancelling || v.Backup != nil || v.DevelopmentProgress.Step != phase {
					t.Fatal("cancellation lost the running snapshot", v)
				}
				programPipe.Close() // EOF lets the helper finish compressing the captured bytes.
			}

			if phase == "preflight" {
				if _, err := os.Stat(filepath.Join(u.dir, "development", "backup.log")); !os.IsNotExist(err) {
					t.Fatal("early cancellation started a snapshot", err)
				}
			}
			if phase == "backup_failure" {
				if err := os.WriteFile(filepath.Join(bin, "pg_dump"), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
					t.Fatal(err)
				}
				close(release)
				waitForUpdate(t, u, "snapshot failure", func() bool { return status().State == "failed" })
				v := status()
				if v.Cancelling || v.Backup != nil || !strings.Contains(v.Error, "7") ||
					!strings.Contains(v.Error, su(h.userLang(), "development_step_database_dump")) {
					t.Fatal("cancellation hid the snapshot failure", v)
				}
				request("POST", "/test-write", "", 200)
				return
			}
			close(release)
			waitForUpdate(t, u, "cancelled attempt to finish", func() bool { return status().State == "idle" })
			request("POST", route+"/cancel", "", 409)
			request("POST", "/test-write", "", 200)
			if v := status(); v.CanCancel || v.Cancelling || v.Error != "" {
				t.Fatal(v)
			}
			var persisted updateSettings
			if ok, err := h.store.Setting(ctx, updateSettingsKey, &persisted); !ok || err != nil {
				t.Fatal("missing stored update history", err)
			}
			if len(persisted.History) != 1 || persisted.History[0].Result != "cancelled" {
				t.Fatal(persisted.History)
			}
			if phase == "backup" || phase == "archive_programs" {
				v := status()
				if v.Backup == nil || persisted.DevelopmentBackup == nil || persisted.DevelopmentBackup.ID != v.Backup.ID || persisted.History[0].Backup == nil || persisted.History[0].Backup.ID != v.Backup.ID {
					t.Fatal("verified receipt missing", v)
				}
				if err := backup.UnrecoveredSnapshot(v.Development.BackupDirectory); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(v.Development.BackupDirectory, v.Backup.ID)); err != nil {
					t.Fatal(err)
				}
			}
			for _, pattern := range []string{filepath.Join(u.dir, "development", "build-*"), filepath.Join(u.dir, "[0-9]*"), update.DevelopmentAdoptionPath(u.dir), update.PendingPath(u.dir), target + ".prev"} {
				if paths, _ := filepath.Glob(pattern); len(paths) > 0 {
					t.Fatal("cancel retained", paths)
				}
			}
			if data, err := os.ReadFile(target); err != nil || !strings.Contains(string(data), "echo old") {
				t.Fatal("live binary changed", err)
			}
			setSessions(machine)
			request("POST", route, `{"target":"latest"}`, 200)
			select {
			case <-handed:
			case <-ctx.Done():
				t.Fatal("retry did not reach handover", status())
			}
		})
	}
}
