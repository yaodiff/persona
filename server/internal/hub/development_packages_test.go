package hub

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/update"
)

func TestPublishedPackagesArePinnedToServingHubBuild(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	token, err := h.createBusToken(t.Context(), "remote", "Remote manager")
	if err != nil {
		t.Fatal(err)
	}
	version, number, commit := "v2", int64(202611270123), strings.Repeat("a", 40)
	h.build = update.Build{Version: version, Number: number, Commit: commit, Channel: update.Release}
	h.version = version
	target := devicewire.PublishedBuild(commit, version, number)
	dir, staged := t.TempDir(), t.TempDir()
	t.Setenv("PERSONA_NODE_BINARIES", dir)
	for _, name := range []string{"personad-linux-amd64", "persona-manager-linux-amd64", "persona-node-linux-amd64"} {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := update.PublishPackages(staged, dir, h.build); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	request := func(file, credential string, want int) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target.Packages+"/"+file, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s returned %d, want %d", file, w.Code, want)
		}
		return w.Body.String()
	}
	h.upd.confirmed = false
	request("manifest.json", token, http.StatusGone)
	h.upd.confirmed = true
	request("manifest.json", "", http.StatusUnauthorized)
	manifest, err := update.ParseManifest([]byte(request("manifest.json", token, http.StatusOK)))
	if err != nil || manifest.Version != version || manifest.Commit != commit || manifest.Build != number {
		t.Fatalf("manifest %+v, %v", manifest, err)
	}
	name := manifest.Programs["persona-manager"]["linux-amd64"].Name
	if got := request(name, token, http.StatusOK); name == "" || got != name {
		t.Fatalf("requested program %q, received %q", name, got)
	}
	request("private.txt", token, http.StatusNotFound)

	// Older private publishers keep the original archive manifest in their
	// immutable package tree and publish only the verified unpacked files.
	managed := t.TempDir()
	daemon := manifest.Files["linux-amd64"]
	if err := os.WriteFile(filepath.Join(managed, daemon.Name), []byte(daemon.Name), 0o755); err != nil {
		t.Fatal(err)
	}
	daemon.Name += ".gz"
	daemon.Size++
	manifest.Files["linux-amd64"] = daemon
	if err := update.Save(filepath.Join(managed, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(t.TempDir(), "nodes")
	if err := os.Symlink(managed, pointer); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_NODE_BINARIES", pointer)
	index, err := update.ParseManifest([]byte(request("manifest.json", token, http.StatusOK)))
	daemonName := "personad-linux-amd64"
	if err != nil || index.Files["linux-amd64"].Name != daemonName || index.Files["linux-amd64"].Size != int64(len(daemonName)) {
		t.Fatalf("immutable package index %+v, %v", index, err)
	}
	if got := request(daemonName, token, http.StatusOK); got != daemonName {
		t.Fatalf("immutable program = %q", got)
	}
	if _, err := os.Lstat(filepath.Join(managed, update.PackageIndex)); !os.IsNotExist(err) {
		t.Fatalf("immutable package directory was modified: %v", err)
	}
	h.build.Number++
	request("manifest.json", token, http.StatusNotFound)
}

func TestDevelopmentPackagesRequireConfirmedAdoptionAndManagerAuth(t *testing.T) {
	h := newTestHub(t)
	h.bus.idents = &memIdents{}
	token, err := h.createBusToken(t.Context(), "remote", "Remote manager")
	if err != nil {
		t.Fatal(err)
	}
	target := devicewire.DevelopmentBuild(strings.Repeat("a", 40), "web-dev-aaaaaaaaaaaa-202611230910", 202611230910)
	h.version, h.build.Number, h.build.Commit = target.Version, target.Build, target.Commit
	h.upd.settings.ConfirmedDevelopment = &target
	h.upd.loaded = true
	dir := filepath.Join(h.upd.packageStore(), target.Version)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"manifest.json", "persona-node-linux-amd64.gz", "database-0.dump"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0600); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	h.busRoutes(mux)
	get := func(file, bearer string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target.Packages+"/"+file, nil)
		r.RemoteAddr = "127.0.0.1:12345"
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("HTTP %d, want %d", w.Code, status)
		}
		return w
	}
	get("manifest.json", "", http.StatusUnauthorized)
	get("manifest.json?token="+token, "", http.StatusUnauthorized)
	get("persona-node-linux-amd64.gz", "invalid", http.StatusUnauthorized)
	get("database-0.dump", token, http.StatusNotFound)
	for _, file := range []string{"manifest.json", "persona-node-linux-amd64.gz"} {
		w := get(file, token, http.StatusOK)
		if w.Body.String() != file || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("wrong artifact or cache policy")
		}
	}
	if w := get("persona-node-linux-arm64.gz", token, http.StatusNotFound); w.Header().Get(devicewire.DevelopmentCommitHeader) != target.Commit {
		t.Fatal("current platform absence must identify the commit")
	}
	h.bus.current = map[string]*busConn{"remote": {presence: busPresence{Capabilities: []string{devicewire.DevelopmentPackagesCapability}}}}
	cfg := h.devicesConfigFor("remote")
	if cfg.DevelopmentTarget == nil || *cfg.DevelopmentTarget != target || cfg.DevelopmentHold {
		t.Fatalf("confirmed handover: %+v", cfg.DevelopmentTarget)
	}
	if cfg := h.devicesConfigFor("older"); !cfg.DevelopmentHold || cfg.HubVersion != "" || cfg.AutoUpdateNodes {
		t.Fatal("an older manager must not deploy this version from its release source")
	}
	held := func() {
		t.Helper()
		if cfg := h.devicesConfigFor("remote"); !cfg.DevelopmentHold || cfg.DevelopmentTarget != nil || cfg.AutoUpdateNodes || cfg.HubVersion != "" {
			t.Fatal("unconfirmed development identity escaped in configuration")
		}
		get("manifest.json", token, http.StatusGone)
		get("persona-node-linux-amd64.gz", token, http.StatusGone)
	}
	h.upd.confirmed = false
	held()
	h.upd.confirmed = true
	journal := update.DevelopmentAdoptionPath(h.upd.dir)
	if err := update.Save(journal, update.Pending{}); err != nil {
		t.Fatal(err)
	}
	held()
	if err := os.Remove(journal); err != nil {
		t.Fatal(err)
	}
	h.build.Commit = strings.Repeat("b", 40)
	held()
	h.build.Commit = target.Commit
	if err := h.upd.record(t.Context(), updateRecord{Source: "web-dev", Result: "rolled_back", ToBuild: target.Build, Commit: target.Commit}); err != nil {
		t.Fatal(err)
	}
	held()
	delete(h.bus.current, "remote")
	if _, err := h.revokeBusToken(t.Context(), "remote"); err != nil {
		t.Fatal(err)
	}
	get("manifest.json", token, http.StatusUnauthorized)
}

func TestDevelopmentRollbackCanOfferTheRestoredGoodVersion(t *testing.T) {
	h := newTestHub(t)
	u := h.upd
	good := devicewire.DevelopmentBuild(strings.Repeat("a", 40), "web-dev-aaaaaaaaaaaa-202611230910", 202611230910)
	failed := devicewire.DevelopmentBuild(strings.Repeat("b", 40), "web-dev-bbbbbbbbbbbb-202611230911", 202611230911)
	h.version, h.build.Number, h.build.Commit = good.Version, good.Build, good.Commit
	u.loaded = true
	for _, target := range []devicewire.DevelopmentTarget{good, failed} {
		if err := u.record(t.Context(), updateRecord{Source: "web-dev", Result: "updated", ToVersion: target.Version, ToBuild: target.Build, Commit: target.Commit}); err != nil {
			t.Fatal(err)
		}
	}
	if err := u.record(t.Context(), updateRecord{Source: "web-dev", Result: "rolled_back", ToVersion: failed.Version, ToBuild: failed.Build, Commit: failed.Commit}); err != nil {
		t.Fatal(err)
	}
	if target, hold := u.confirmedDevelopment(); hold || target == nil || *target != good {
		t.Fatal("previous confirmation unavailable after rollback", target)
	}
	h.version, h.build.Number, h.build.Commit = failed.Version, failed.Build, failed.Commit
	if target, hold := u.confirmedDevelopment(); !hold || target != nil {
		t.Fatal("rolled-back receipt accepted", target)
	}
}
