package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

func TestEditionWireAndParse(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want nodeproto.Edition
		ok   bool
	}{{"", nodeproto.EditionFull, true}, {"full", nodeproto.EditionFull, true}, {"core", nodeproto.EditionCore, true}, {"Core", "", false}, {"lite", "", false}} {
		got, ok := nodeproto.ParseEdition(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseEdition(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
	// A full build's marker is empty and its names are the ones every install reads today: nothing of the full
	// feed changes.
	if nodeproto.EditionFull.Marker() != "" || nodeproto.EditionCore.Marker() != "core" {
		t.Fatalf("marker: full %q, core %q", nodeproto.EditionFull.Marker(), nodeproto.EditionCore.Marker())
	}
	if FeedName(nodeproto.EditionFull) != "latest.json" || ReleaseManifestName(nodeproto.EditionFull) != "manifest.json" {
		t.Fatalf("the full edition's manifests are %s and %s", FeedName(nodeproto.EditionFull), ReleaseManifestName(nodeproto.EditionFull))
	}
	if FeedName(nodeproto.EditionCore) != "core-latest.json" || ReleaseManifestName(nodeproto.EditionCore) != "core-manifest.json" {
		t.Fatalf("the core edition's manifests are %s and %s", FeedName(nodeproto.EditionCore), ReleaseManifestName(nodeproto.EditionCore))
	}
}

func TestEditionOfTags(t *testing.T) {
	for tags, want := range map[string]nodeproto.Edition{
		"":                               nodeproto.EditionFull,
		"public,ts_omit_ssh":             nodeproto.EditionFull,
		"public,persona_core_only":       nodeproto.EditionCore,
		"persona_core_only ts_omit_ssh":  nodeproto.EditionCore,
		"public,persona_without_voice":   nodeproto.EditionFull,
		"public,xpersona_core_only,more": nodeproto.EditionFull,
	} {
		if got := editionOfTags(tags); got != want {
			t.Errorf("editionOfTags(%q) = %q, want %q", tags, got, want)
		}
	}
}

// buildEdition builds a small Go program with tags and returns its path: a real executable whose build info says
// the edition, as a release's programs do.
func buildEdition(t *testing.T, name, tags string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.test/edition\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-tags", tags, "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOFLAGS=", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}
	return out
}

func TestEditionOfReadsTheBuildInfo(t *testing.T) {
	full := buildEdition(t, "personad", "public")
	core := buildEdition(t, "personad", "public,"+nodeproto.CoreOnlyTag)
	if got, ok := EditionOf(full); !ok || got != nodeproto.EditionFull {
		t.Fatalf("a default build reads as %q, %v", got, ok)
	}
	if got, ok := EditionOf(core); !ok || got != nodeproto.EditionCore {
		t.Fatalf("a persona_core_only build reads as %q, %v", got, ok)
	}
	script := filepath.Join(t.TempDir(), "personad")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := EditionOf(script); ok {
		t.Fatal("a file without build info has an edition")
	}

	// Neither edition is ever replaced by the other, in both directions; the same edition is.
	for _, tc := range []struct {
		current, next string
		refused       bool
	}{{core, full, true}, {full, core, true}, {core, core, false}, {full, full, false}, {script, core, false}, {core, script, false}} {
		err := CheckReplace(tc.current, tc.next)
		if tc.refused != (err != nil) {
			t.Errorf("CheckReplace(%s, %s) = %v, refused want %v", tc.current, tc.next, err, tc.refused)
		}
		if err != nil && !strings.Contains(err.Error(), "edition") {
			t.Errorf("the refusal does not say why: %v", err)
		}
	}
	if err := CheckEdition(full, nodeproto.EditionCore, "the new personad"); err == nil {
		t.Fatal("a full build passed as the core edition")
	}
	if err := CheckEdition(core, nodeproto.EditionFull, "the new personad"); err == nil {
		t.Fatal("a core-only build passed as the full edition")
	}
	if err := CheckEdition(core, nodeproto.EditionCore, "the new personad"); err != nil {
		t.Fatal(err)
	}
}

func TestManifestValidateEdition(t *testing.T) {
	full, core, odd := &Manifest{}, &Manifest{Edition: "core"}, &Manifest{Edition: "lite"}
	if err := full.ValidateEdition(nodeproto.EditionFull); err != nil {
		t.Fatal(err)
	}
	if err := core.ValidateEdition(nodeproto.EditionCore); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		m    *Manifest
		want nodeproto.Edition
	}{{full, nodeproto.EditionCore}, {core, nodeproto.EditionFull}, {odd, nodeproto.EditionFull}, {odd, nodeproto.EditionCore}} {
		if err := tc.m.ValidateEdition(tc.want); err == nil {
			t.Errorf("a manifest of edition %q passed as %s", tc.m.Edition, tc.want)
		}
	}
}

// A build reads only its own edition's feed, and refuses a manifest of the other edition served under that name: a
// source that answers every name with the same file cannot turn one edition into the other.
func TestCheckFeedReadsOneEditionOnly(t *testing.T) {
	manifest := func(e nodeproto.Edition) string {
		edition := ""
		if e.Marker() != "" {
			edition = `"edition":"` + e.Marker() + `",`
		}
		return `{"version":"v1.0.0","build":202611260000,` + edition + `"files":{"linux-amd64":{"name":"personad-linux-amd64.gz"}}}`
	}
	for _, own := range []nodeproto.Edition{nodeproto.EditionFull, nodeproto.EditionCore} {
		other := nodeproto.EditionCore
		if own == nodeproto.EditionCore {
			other = nodeproto.EditionFull
		}
		var asked []string
		serve := func(body string) *httptest.Server {
			return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				asked = append(asked, r.URL.Path)
				_, _ = w.Write([]byte(body))
			}))
		}
		right := serve(manifest(own))
		if _, err := CheckFeed(context.Background(), right.Client(), right.URL, own); err != nil {
			t.Fatalf("%s: %v", own, err)
		}
		right.Close()
		if len(asked) != 1 || asked[0] != "/"+FeedName(own) {
			t.Fatalf("a %s build asked for %v, want only /%s", own, asked, FeedName(own))
		}
		wrong := serve(manifest(other))
		_, err := CheckFeed(context.Background(), wrong.Client(), wrong.URL, own)
		wrong.Close()
		if err == nil || !strings.Contains(err.Error(), "edition") {
			t.Fatalf("a manifest of the %s edition was taken by a %s build: %v", other, own, err)
		}
	}
	// Check, and a Build nothing set an edition on, are the full edition: what every caller from before editions gets.
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		_, _ = w.Write([]byte(manifest(nodeproto.EditionFull)))
	}))
	defer srv.Close()
	if _, err := Check(context.Background(), srv.Client(), srv.URL); err != nil || asked != "/latest.json" {
		t.Fatalf("Check asked for %s: %v", asked, err)
	}
	if _, err := CheckFeed(context.Background(), srv.Client(), srv.URL, Build{}.Edition); err != nil || asked != "/latest.json" {
		t.Fatalf("a zero Build asked for %s: %v", asked, err)
	}
	if got := Make("v1", Release, "202611260000", "https://example.test/dl").Edition; got != ThisEdition {
		t.Fatalf("Make records edition %q, this build is %q", got, ThisEdition)
	}
}

func TestServeAsNamesTheCoreDownloads(t *testing.T) {
	files := func() *Manifest {
		return &Manifest{
			Files:    map[string]File{"linux-amd64": {Name: "personad-linux-amd64.gz"}},
			Links:    map[string]File{"windows-amd64": {Name: "persona-link-windows-amd64.exe.gz"}},
			Programs: map[string]map[string]File{"persona-hub": {"linux-amd64": {Name: "persona-hub-linux-amd64.gz"}}},
			Nodes:    map[string]map[string]File{"persona-node": {"linux-amd64": {Name: "persona-node-linux-amd64.gz"}}},
		}
	}
	full := files()
	full.serveAs(nodeproto.EditionFull)
	if f := full.Files["linux-amd64"]; f.Served != "" || f.Remote() != "personad-linux-amd64.gz" {
		t.Fatalf("a full download is served as %q (%q)", f.Remote(), f.Served)
	}
	core := files()
	core.serveAs(nodeproto.EditionCore)
	for what, f := range map[string]File{"personad": core.Files["linux-amd64"], "link": core.Links["windows-amd64"],
		"hub": core.Programs["persona-hub"]["linux-amd64"], "node": core.Nodes["persona-node"]["linux-amd64"]} {
		if f.Remote() != "core-"+f.Name || strings.HasPrefix(f.Name, "core-") {
			t.Errorf("%s: name %q served as %q", what, f.Name, f.Remote())
		}
	}
}
