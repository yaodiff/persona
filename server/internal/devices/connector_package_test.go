package devices

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// putConnector copies the build from to dir as the platform's connector, with a .version saying version beside it
// ("" none).
func putConnector(t *testing.T, dir, from, version string) string {
	t.Helper()
	p := filepath.Join(dir, programConnector+"-"+otherOS+"-"+otherArch)
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(p + ".version")
	if version != "" {
		if err := os.WriteFile(p+".version", []byte(version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// sameFile: the file at path holds what from does.
func sameFile(t *testing.T, path, from string) bool {
	t.Helper()
	a, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(from)
	return bytes.Equal(a, b)
}

// The hub's own connector is used only when its .version says the hub's version (deploy.sh writes it after the
// binary) and its build info says that platform and the connector; anything else there is passed over, so the
// release's can be used instead. The node binary dir goes first, then the bundled one.
func TestConnectorPackageOwnBuild(t *testing.T) {
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	ctx := context.Background()

	t.Run("the hub's version", func(t *testing.T) {
		site := newFakeRelease(t)
		d, dir, bundled := packageDevices(t, "v2", site.srv.URL)
		inBundled := putConnector(t, bundled, link, "v2")
		path, version, err := d.connectorPackage(ctx, otherOS, otherArch)
		if err != nil || path != inBundled || version != "v2" {
			t.Fatalf("bundled: %s %s %v", path, version, err)
		}
		own := putConnector(t, dir, link, "v2")
		if path, _, err := d.connectorPackage(ctx, otherOS, otherArch); err != nil || path != own {
			t.Fatalf("the node binary dir goes first: %s %v", path, err)
		}
		putConnector(t, dir, link, "v1") // an earlier deploy's: the bundled one of the hub's version is next
		if path, _, err := d.connectorPackage(ctx, otherOS, otherArch); err != nil || path != inBundled {
			t.Fatalf("passed over the node binary dir's of another version: %s %v", path, err)
		}
		if got := site.requests(); len(got) != 0 {
			t.Fatalf("asked the release site for a build the hub has: %v", got)
		}
	})

	t.Run("not the hub's version", func(t *testing.T) {
		for _, version := range []string{"v1", "v2-1", ""} {
			d, dir, _ := packageDevices(t, "v2", "")
			putConnector(t, dir, link, version)
			if path, _, err := d.connectorPackage(ctx, otherOS, otherArch); !errors.Is(err, errNoConnector) {
				t.Fatalf(".version %q: %s %v", version, path, err)
			}
		}
	})

	t.Run("not the connector or not the platform", func(t *testing.T) {
		notGo := filepath.Join(t.TempDir(), "script")
		_ = os.WriteFile(notGo, []byte("#!/bin/sh\nexit 0\n"), 0o755)
		for name, build := range map[string]string{
			"darwin/arm64": updatetest.Program(t, programConnector, "darwin", "arm64"),
			"persona-node": updatetest.Program(t, nodeproto.ProgramPersonaNode, otherOS, otherArch),
			"personad":     updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch),
			"not Go":       notGo,
		} {
			d, dir, _ := packageDevices(t, "v2", "")
			putConnector(t, dir, build, "v2")
			if path, _, err := d.connectorPackage(ctx, otherOS, otherArch); !errors.Is(err, errNoConnector) {
				t.Fatalf("%s taken as the connector: %s %v", name, path, err)
			}
		}
		// Passed over for the release's.
		plat := otherOS + "-" + otherArch
		site := newFakeRelease(t)
		site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{
			"personad-" + plat: updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch), programConnector + "-" + plat: link})
		d, dir, _ := packageDevices(t, "v2", site.srv.URL)
		mislaid := putConnector(t, dir, updatetest.Program(t, programConnector, "darwin", "arm64"), "v2")
		path, _, err := d.connectorPackage(ctx, otherOS, otherArch)
		if err != nil || path == mislaid || !sameFile(t, path, link) {
			t.Fatalf("the release's was not fetched: %s %v", path, err)
		}
	})
}

func TestConnectorPackageOwnBuildKeepsHubEdition(t *testing.T) {
	full := updatetest.VersionedProgram(t, programConnector, otherOS, otherArch, "v2")
	core := updatetest.CoreProgram(t, programConnector, otherOS, otherArch, "v2")
	for _, tc := range []struct {
		name    string
		manager nodeproto.Edition
		build   string
		accept  bool
	}{
		{"full manager, full connector", nodeproto.EditionFull, full, true},
		{"full manager, core connector", nodeproto.EditionFull, core, false},
		{"core manager, core connector", nodeproto.EditionCore, core, true},
		{"core manager, full connector", nodeproto.EditionCore, full, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, dir, _ := packageDevices(t, "v2", "")
			d.opts.Edition = tc.manager
			local := putConnector(t, dir, tc.build, "v2")
			path, _, err := d.connectorPackage(context.Background(), otherOS, otherArch)
			if tc.accept && (err != nil || path != local) {
				t.Fatalf("the manager's own connector edition: %s %v", path, err)
			}
			if !tc.accept && (!errors.Is(err, errNoConnector) || path != "") {
				t.Fatalf("a connector of the other edition was taken: %s %v", path, err)
			}
		})
	}
}

// Without a build of its own, the connector of the release of the hub's version is downloaded, checked and kept in
// the cache, which serves it again (the site asked only for the manifest) and while the site does not answer, also
// to a manager started since.
func TestConnectorPackageFromTheRelease(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch)
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	ctx := context.Background()
	site := newFakeRelease(t)
	site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: full, programConnector + "-" + plat: link})
	d, dir, _ := packageDevices(t, "v2", site.srv.URL)
	if err := os.WriteFile(filepath.Join(dir, "personad-"+plat), mustRead(t, full), 0o755); err != nil { // a node build is not the connector
		t.Fatal(err)
	}
	path, version, err := d.connectorPackage(ctx, otherOS, otherArch)
	if err != nil || version != "v2" || path != filepath.Join(identity.NodePackageCacheDir(), "v2", programConnector+"-"+plat) || !sameFile(t, path, link) {
		t.Fatalf("fetched %s %s %v", path, version, err)
	}
	if err := checkPackagePlatform(path, otherOS, otherArch, programConnector); err != nil {
		t.Fatal(err)
	}
	if got := site.requests(); strings.Join(got, " ") != "/v2/manifest.json /v2/"+programConnector+"-"+plat+".gz" {
		t.Fatalf("asked %v", got)
	}

	asked := len(site.requests())
	if again, _, err := d.connectorPackage(ctx, otherOS, otherArch); err != nil || again != path {
		t.Fatalf("again: %s %v", again, err)
	}
	if got := site.requests()[asked:]; strings.Join(got, " ") != "/v2/manifest.json" {
		t.Fatalf("not from the cache: asked %v", got)
	}

	site.setDown(true)
	if cached, version, err := d.connectorPackage(ctx, otherOS, otherArch); err != nil || cached != path || version != "v2" {
		t.Fatalf("while the site is away: %s %s %v", cached, version, err)
	}
	e := newTestDevices(t)
	e.mu.Lock()
	e.cfg.HubVersion, e.cfg.NodePackagesURL = "v2", site.srv.URL
	e.mu.Unlock()
	if cached, _, err := e.connectorPackage(ctx, otherOS, otherArch); err != nil || cached != path {
		t.Fatalf("after a restart while the site is away: %s %v", cached, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A source without dl/<version>/ (a GitHub release base) serves the release of the hub's version as latest.json, its
// connector beside it.
func TestConnectorPackageFromAFlatReleaseSource(t *testing.T) {
	plat := otherOS + "-" + otherArch
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	site := newFakeRelease(t)
	site.publish(t, "v2", overlapping("v2", 2), true, map[string]string{
		"personad-" + plat: updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch), programConnector + "-" + plat: link})
	site.flatten("v2")
	d, _, _ := packageDevices(t, "v2", site.srv.URL)
	path, version, err := d.connectorPackage(context.Background(), otherOS, otherArch)
	if err != nil || version != "v2" || !sameFile(t, path, link) {
		t.Fatalf("%s %s %v", path, version, err)
	}
	got := site.requests()
	if len(got) < 4 || got[0] != "/v2/manifest.json" || got[1] != "/latest.json" || got[2] != "/v2/"+programConnector+"-"+plat+".gz" ||
		got[len(got)-1] != "/"+programConnector+"-"+plat+".gz" {
		t.Fatalf("asked %v", got)
	}
}

// A downloaded connector is used only once it matches the manifest (size, sha256, binary_sha256) and its build info
// says the platform and the connector; one that does not is not kept.
func TestFetchedConnectorIsChecked(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch)
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	for _, tc := range []struct {
		name  string
		build string               // what is served as persona-link-<plat>
		bend  func(f *update.File) // what the manifest says of it
	}{
		{"size", link, func(f *update.File) { f.Size-- }},
		{"sha256", link, func(f *update.File) { f.SHA256 = strings.Repeat("0", 64) }},
		{"binary_sha256", link, func(f *update.File) { f.BinarySHA256 = strings.Repeat("0", 64) }},
		{"platform", updatetest.Program(t, programConnector, "darwin", "arm64"), nil},
		{"persona-node", updatetest.Program(t, nodeproto.ProgramPersonaNode, otherOS, otherArch), nil},
		{"personad", full, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := newFakeRelease(t)
			m := site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: full, programConnector + "-" + plat: tc.build})
			if tc.bend != nil {
				f := m.Links[plat]
				tc.bend(&f)
				m.Links[plat] = f
				b, _ := json.Marshal(m)
				site.mu.Lock()
				site.files["/v2/manifest.json"] = b
				site.mu.Unlock()
			}
			d, _, _ := packageDevices(t, "v2", site.srv.URL)
			path, _, err := d.connectorPackage(context.Background(), otherOS, otherArch)
			if err == nil || errors.Is(err, errNoConnector) || !strings.Contains(i18n.DeploymentText(i18n.En, err.Error()), "did not check out") {
				t.Fatalf("taken: %s %v", path, err)
			}
			var left []string
			_ = filepath.Walk(identity.NodePackageCacheDir(), func(p string, fi os.FileInfo, err error) error {
				if err == nil && !fi.IsDir() {
					left = append(left, p)
				}
				return nil
			})
			if len(left) != 0 {
				t.Fatalf("left in the cache: %v", left)
			}
		})
	}
}

// A connector of this machine's own platform must also run and say the release's version and build.
func TestFetchedConnectorOfThisPlatformRuns(t *testing.T) {
	plat := runtime.GOOS + "-" + runtime.GOARCH
	says := func(version string) string {
		return updatetest.ProgramSource(t, programConnector, "", "", "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() {\n"+
			"\tif len(os.Args) > 2 && os.Args[1] == \"version\" && os.Args[2] == \"-v\" {\n\t\tfmt.Println(\""+version+"\")\n"+
			"\t\tfmt.Println(\"channel release, build 7\")\n\t}\n}\n")
	}
	full := updatetest.Program(t, nodeproto.ProgramPersonad, "", "")
	for _, tc := range []struct {
		says string
		ok   bool
	}{{"v7-1", true}, {"v6-1", false}} {
		site := newFakeRelease(t)
		site.publish(t, "v7-1", overlapping("v7-1", 7), false, map[string]string{"personad-" + plat: full, programConnector + "-" + plat: says(tc.says)})
		d, _, _ := packageDevices(t, "v7-1", site.srv.URL)
		if _, _, err := d.connectorPackage(context.Background(), runtime.GOOS, runtime.GOARCH); (err == nil) != tc.ok {
			t.Errorf("a connector that says %s: %v", tc.says, err)
		}
	}
}

// Only the hub's version: with no connector of it anywhere the answer is errNoConnector, never a newer release's (the
// fallback node builds have), a name outside the release or a platform that is not one. A site that does not answer
// is not "none".
func TestNoConnector(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch)
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	ctx := context.Background()
	none := func(t *testing.T, d *Devices, goos, arch string) {
		t.Helper()
		if path, _, err := d.connectorPackage(ctx, goos, arch); !errors.Is(err, errNoConnector) || path != "" {
			t.Fatalf("%s %v, want errNoConnector", path, err)
		}
	}

	t.Run("a release without it", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: full})
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		none(t, d, otherOS, otherArch)
		if got := site.requests(); strings.Join(got, " ") != "/v2/manifest.json" {
			t.Fatalf("asked %v", got)
		}
	})
	t.Run("only a newer release has it", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: full, programConnector + "-" + plat: link})
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		none(t, d, otherOS, otherArch)
		if got := site.requests(); strings.Join(got, " ") != "/v2/manifest.json /latest.json" {
			t.Fatalf("asked %v", got)
		}
		site.flatten("v9-1") // the same from a source without dl/<version>/
		none(t, d, otherOS, otherArch)
	})
	t.Run("no release", func(t *testing.T) {
		site := newFakeRelease(t)
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		none(t, d, otherOS, otherArch)
		d2, _, _ := packageDevices(t, "v2", "") // nowhere to fetch from
		none(t, d2, otherOS, otherArch)
		d3, _, _ := packageDevices(t, "dev", site.srv.URL) // a dev build has no release
		none(t, d3, otherOS, otherArch)
	})
	t.Run("a name outside the release", func(t *testing.T) {
		for _, name := range []string{"../../evil.gz", programConnector + "-" + plat + ".exe.gz", "personad-" + plat + ".gz"} {
			site := newFakeRelease(t)
			m := site.publish(t, "v2", overlapping("v2", 2), false, map[string]string{"personad-" + plat: full, programConnector + "-" + plat: link})
			f := m.Links[plat]
			f.Name = name
			m.Links[plat] = f
			b, _ := json.Marshal(m)
			site.mu.Lock()
			site.files["/v2/manifest.json"] = b
			site.mu.Unlock()
			d, _, _ := packageDevices(t, "v2", site.srv.URL)
			none(t, d, otherOS, otherArch)
		}
	})
	t.Run("not a platform", func(t *testing.T) {
		d, dir, _ := packageDevices(t, "v2", "")
		putConnector(t, dir, link, "v2")
		none(t, d, "../"+otherOS, otherArch)
		none(t, d, otherOS, "")
	})
	t.Run("the site away", func(t *testing.T) {
		site := newFakeRelease(t)
		site.setDown(true)
		d, _, _ := packageDevices(t, "v2", site.srv.URL)
		if _, _, err := d.connectorPackage(ctx, otherOS, otherArch); err == nil || errors.Is(err, errNoConnector) || !siteAway(err) {
			t.Fatalf("%v", err)
		}
	})
}

// The connector is never a node's build: a node that says it runs persona-link is looked up as personad, and the
// connector's lookup, remembered beside the node builds', changes nothing of theirs.
func TestConnectorIsNotANodeBuild(t *testing.T) {
	plat := otherOS + "-" + otherArch
	full := updatetest.Program(t, nodeproto.ProgramPersonad, otherOS, otherArch)
	link := updatetest.Program(t, programConnector, otherOS, otherArch)
	ctx := context.Background()
	d, dir, _ := packageDevices(t, "v2", "")
	personad := filepath.Join(dir, "personad-"+plat)
	if err := os.WriteFile(personad, mustRead(t, full), 0o755); err != nil {
		t.Fatal(err)
	}
	own := putConnector(t, dir, link, "v2")
	for _, program := range []string{programConnector, nodeproto.ProgramPersonad, ""} {
		if pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, program); err != nil || pkg.path != personad || pkg.Program != nodeproto.ProgramPersonad {
			t.Fatalf("a node of %q: %+v %v", program, pkg, err)
		}
	}
	if path, _, err := d.connectorPackage(ctx, otherOS, otherArch); err != nil || path != own {
		t.Fatalf("connector: %s %v", path, err)
	}
	if pkg, err := d.nodePackageFor(ctx, otherOS, otherArch, ""); err != nil || pkg.path != personad {
		t.Fatalf("a node after the connector's lookup: %+v %v", pkg, err)
	}
	info := proto.NodeInfo{OS: otherOS, Arch: otherArch, Program: programConnector}
	if got := d.nodeTarget(info); got != "v2" || d.packageBlocked(info) != nil {
		t.Fatalf("node target %s, blocked %v", got, d.packageBlocked(info))
	}
}
