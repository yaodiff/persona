package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// A newer release published between the hub's check and its update (#9263): the update installs the release it
// checked, from that release's own tag on GitHub, or says a newer one is out and offers that one from a source that is
// not GitHub's; never "the download does not match the release's manifest" for a download that was fine.

// pinNodePlatform is a platform the releases here publish a node build for besides the hub's own.
const pinNodePlatform = "plan9-arm"

// pinScript is release version's personad (and persona-hub) for this platform: a script answering version -v as the
// release's binary does, with pad in it, so two releases' downloads differ in size as well as content.
func pinScript(version string, build int64, pad string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\n%secho %s\necho 'channel release, build %d'\n", pad, version, build))
}

// pinRelease is release version as a source serves it: personad and persona-hub for this platform, a node build for
// pinNodePlatform, and latest.json.
func pinRelease(t *testing.T, version string, build int64, pad string) map[string][]byte {
	t.Helper()
	own := runtime.GOOS + "-" + runtime.GOARCH
	files := map[string][]byte{}
	file := func(name string, body []byte) update.File {
		packed := gz(body)
		outer, inner := sha256.Sum256(packed), sha256.Sum256(body)
		served := update.ThisEdition.Served(name)
		files[served] = packed
		f := update.File{Name: name, Size: int64(len(packed)), SHA256: hex.EncodeToString(outer[:]), BinarySHA256: hex.EncodeToString(inner[:])}
		if update.ThisEdition.Marker() != "" {
			f.Served = served
		}
		return f
	}
	script := pinScript(version, build, pad)
	m := update.Manifest{Version: version, Build: build, Protocol: proto.Protocol, Released: time.Now().UTC().Truncate(time.Second), Edition: update.ThisEdition.Marker(),
		Files: map[string]update.File{
			own:             file("personad-"+own+".gz", script),
			pinNodePlatform: file("personad-"+pinNodePlatform+".gz", []byte("node build of "+version+"\n"+pad)),
		},
		Programs: map[string]map[string]update.File{"persona-hub": {own: file("persona-hub-"+own+".gz", script)}}}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	files[update.FeedName(update.ThisEdition)] = data
	return files
}

// releasesHub is updatingHub with base, a source of src, as hub.json's update.url, and node updates on.
func releasesHub(t *testing.T, src *updatetest.Releases, base string) (*Hub, string, func() []string) {
	t.Helper()
	h, _, target, execs := updatingHub(t, newPublisher(t, "unused", 202611021500), "release")
	h.cfg.Update.URL = base
	h.cfg.AutoUpdateNodes = true
	h.upd.client = src.Client()
	return h, target, execs
}

// From GitHub's moving base the update installs the release it checked, every file of it from that release's tag:
// the hub's binary, the program beside it and the node builds, although a newer release is the latest by then.
func TestUpdateInstallsTheCheckedReleaseFromItsTag(t *testing.T) {
	src := updatetest.NewReleases(t)
	src.Publish("v0.6.1", pinRelease(t, "0.6.1", 202611160928, ""), true)
	h, target, execs := releasesHub(t, src, src.GitHub())
	ctx := context.Background()
	if _, err := h.upd.check(ctx); err != nil {
		t.Fatal(err)
	}
	src.Publish("v0.6.2", pinRelease(t, "0.6.2", 202611161034, "# a newer release\n"), true)
	if err := h.upd.begin(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restart", func() bool { return len(execs()) == 1 || h.upd.view().State == "failed" })
	if v := h.upd.view(); v.State == "failed" {
		t.Fatalf("failed: %s", v.Error)
	}
	want := pinScript("0.6.1", 202611160928, "")
	if b, _ := os.ReadFile(target); !bytes.Equal(b, want) {
		t.Fatalf("installed %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(target), "persona-hub")); !bytes.Equal(b, want) {
		t.Fatalf("persona-hub %q", b)
	}
	node := filepath.Join(h.upd.stagedDir(202611160928), "nodes", "personad-"+pinNodePlatform)
	if b, _ := os.ReadFile(node); string(b) != "node build of 0.6.1\n" {
		t.Fatalf("node build %q", b)
	}
	own := runtime.GOOS + "-" + runtime.GOARCH
	for _, name := range []string{"personad-" + own + ".gz", "persona-hub-" + own + ".gz", "personad-" + pinNodePlatform + ".gz"} {
		served := update.ThisEdition.Served(name)
		if got := src.Fetched(served); !slices.Equal(got, []string{updatetest.TagPath("v0.6.1", served)}) {
			t.Fatalf("%s fetched from %v", served, got)
		}
	}
	var pend update.Pending
	if ok, _ := update.Load(update.PendingPath(h.upd.dir), &pend); !ok || pend.ToVersion != "0.6.1" || pend.ToBuild != 202611160928 {
		t.Fatalf("pending %+v", pend)
	}
}

// A source that is not GitHub's serves the newer release's files once it is out: the update reads the source once
// more, stops saying a newer release was published, and offers that one, which the next Update installs. The file
// that did not match is not fetched again.
func TestUpdateAfterANewerReleaseFromAMovingSource(t *testing.T) {
	src := updatetest.NewReleases(t)
	src.Publish("v0.6.1", pinRelease(t, "0.6.1", 202611160928, ""), true)
	h, target, execs := releasesHub(t, src, src.DL())
	ctx := context.Background()
	if _, err := h.upd.check(ctx); err != nil {
		t.Fatal(err)
	}
	src.Publish("v0.6.2", pinRelease(t, "0.6.2", 202611161034, "# a newer release\n"), true)
	if err := h.upd.begin(ctx, true); err != nil {
		t.Fatal(err)
	}
	// The failure is recorded once its state is out, and the update after it starts from that state.
	failures := func() []updateRecord {
		h.upd.mu.Lock()
		defer h.upd.mu.Unlock()
		return slices.Clone(h.upd.settings.History)
	}
	waitFor(t, "the failure", func() bool { return len(failures()) == 1 })
	if b, _ := os.ReadFile(target); string(b) != "#!/bin/sh\necho old\n" || len(execs()) != 0 {
		t.Fatal("replaced the binary")
	}
	v := h.upd.view()
	if want := su(h.userLang(), "err_release_moved", i18n.Args{"version": "0.6.1", "newer": "0.6.2"}); v.Error != want {
		t.Fatalf("error %q, want %q", v.Error, want)
	}
	if v.Latest == nil || v.Latest.Version != "0.6.2" || !v.Latest.Newer {
		t.Fatalf("offers %+v", v.Latest)
	}
	own := "personad-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	if got := src.Fetched(update.ThisEdition.Served(own)); len(got) != 1 {
		t.Fatalf("fetched %v", got)
	}
	feed := update.FeedName(update.ThisEdition)
	if got := src.Fetched(feed); len(got) != 2 {
		t.Fatalf("%s read %v, want the check and once more", feed, got)
	}
	if hs := failures(); len(hs) != 1 || hs[0].Result != "failed" || hs[0].ToVersion != "0.6.1" {
		t.Fatalf("history %+v", hs)
	}
	if _, err := os.Stat(h.upd.stagedDir(202611160928)); !os.IsNotExist(err) {
		t.Fatalf("the stopped update's downloads stayed: %v", err)
	}

	if err := h.upd.begin(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the restart", func() bool { return len(execs()) == 1 || len(failures()) > 1 })
	if hs := failures(); len(hs) > 1 {
		t.Fatalf("failed again: %+v", hs[1:])
	}
	if b, _ := os.ReadFile(target); !bytes.Equal(b, pinScript("0.6.2", 202611161034, "# a newer release\n")) {
		t.Fatalf("installed %q", b)
	}
}

// A download that is broken while the source still has the release checked fails with the mismatch, as before, and
// the hub keeps offering that release.
func TestUpdateBrokenDownloadStillMismatches(t *testing.T) {
	src := updatetest.NewReleases(t)
	files := pinRelease(t, "0.6.1", 202611160928, "")
	own := "personad-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	servedOwn := update.ThisEdition.Served(own)
	files[servedOwn] = append(bytes.Clone(files[servedOwn]), "garbage"...)
	src.Publish("v0.6.1", files, true)
	h, target, execs := releasesHub(t, src, src.DL())
	ctx := context.Background()
	if _, err := h.upd.check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.upd.begin(ctx, true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failure", func() bool { return h.upd.view().State == "failed" })
	if b, _ := os.ReadFile(target); string(b) != "#!/bin/sh\necho old\n" || len(execs()) != 0 {
		t.Fatal("replaced the binary")
	}
	if v := h.upd.view(); !strings.Contains(v.Error, "does not match the release's manifest") || v.Latest == nil || v.Latest.Version != "0.6.1" {
		t.Fatalf("error %q, offers %+v", v.Error, v.Latest)
	}
	if got := src.Fetched(servedOwn); len(got) != 1 {
		t.Fatalf("fetched %v", got)
	}
}
