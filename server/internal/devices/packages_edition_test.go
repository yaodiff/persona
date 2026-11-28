package devices

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

// publishCore puts the core edition's release up beside the full one, as publish.sh does: the manifest as
// core-manifest.json (and core-latest.json), every download under core-<name>, each file's Name unchanged.
func (r *fakeRelease) publishCore(t *testing.T, dir string, m update.Manifest, builds map[string]string) {
	t.Helper()
	inner := newFakeRelease(t)
	core := inner.publish(t, dir, m, true, builds)
	core.Edition = nodeproto.EditionCore.Marker()
	rename := func(files map[string]update.File) {
		for plat, f := range files {
			f.Served = nodeproto.EditionCore.Served(f.Name)
			files[plat] = f
			inner.mu.Lock()
			body := inner.files["/"+dir+"/"+f.Name]
			inner.mu.Unlock()
			r.mu.Lock()
			r.files["/"+dir+"/"+f.Served] = body
			r.mu.Unlock()
		}
	}
	rename(core.Files)
	rename(core.Links)
	for _, files := range core.Nodes {
		rename(files)
	}
	b, _ := json.Marshal(core)
	r.mu.Lock()
	r.files["/"+dir+"/"+update.ReleaseManifestName(nodeproto.EditionCore)] = b
	r.files["/"+update.FeedName(nodeproto.EditionCore)] = b
	r.mu.Unlock()
}

func isOtherEdition(err error) bool {
	var msg *i18n.Message
	return errors.As(err, &msg) && msg.Key == "devices-deploy.other_edition"
}

// An install keeps its edition (docs/operations/core-install.md "Updates stay core-only"): a manager fetches only
// its own edition's release, sends a node only a build of the node's edition, and has none for a node of the other.
func TestNodePackagesKeepTheEdition(t *testing.T) {
	const hub = "0.9.9-202611260000"
	plat := otherOS + "-" + otherArch
	name := "personad-" + plat
	fullBuild := updatetest.VersionedProgram(t, nodeproto.ProgramPersonad, otherOS, otherArch, hub)
	coreBuild := updatetest.CoreProgram(t, nodeproto.ProgramPersonad, otherOS, otherArch, hub)
	ctx := context.Background()
	fullNode := proto.NodeInfo{OS: otherOS, Arch: otherArch, Program: nodeproto.ProgramPersonad}
	coreNode := fullNode
	coreNode.Edition = nodeproto.EditionCore
	site := func(t *testing.T) *fakeRelease {
		r := newFakeRelease(t)
		r.publish(t, hub, overlapping(hub, 202611260000), true, map[string]string{name: fullBuild})
		r.publishCore(t, hub, overlapping(hub, 202611260000), map[string]string{name: coreBuild})
		return r
	}
	manager := func(t *testing.T, edition nodeproto.Edition, url string) (*Devices, string) {
		d, dir, _ := packageDevices(t, hub, url)
		d.opts.Edition = edition
		return d, dir
	}
	askedFor := func(r *fakeRelease, part string) bool {
		for _, p := range r.requests() {
			if strings.Contains(p, part) {
				return true
			}
		}
		return false
	}

	t.Run("a full manager and a full node: the full release, as before", func(t *testing.T) {
		r := site(t)
		d, _ := manager(t, "", r.srv.URL) // nothing set: the full build
		pkg, err := d.nodePackageForInfo(ctx, fullNode)
		if err != nil || pkg.file.Remote() != name+".gz" {
			t.Fatalf("%+v %v", pkg, err)
		}
		bin, err := d.fetchPackage(ctx, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := update.EditionOf(bin); got != nodeproto.EditionFull {
			t.Fatalf("fetched a %s build", got)
		}
		if askedFor(r, "core-") {
			t.Fatalf("a full manager asked for a core file: %v", r.requests())
		}
	})

	t.Run("a core manager and a core node: the core release only", func(t *testing.T) {
		r := site(t)
		d, _ := manager(t, nodeproto.EditionCore, r.srv.URL)
		pkg, err := d.nodePackageForInfo(ctx, coreNode)
		if err != nil || pkg.file.Name != name+".gz" || pkg.file.Remote() != "core-"+name+".gz" {
			t.Fatalf("%+v %v", pkg, err)
		}
		bin, err := d.fetchPackage(ctx, pkg)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := update.EditionOf(bin); got != nodeproto.EditionCore {
			t.Fatalf("fetched a %s build", got)
		}
		for _, p := range r.requests() {
			if !strings.Contains(p, "core-") {
				t.Fatalf("a core manager asked for a full file: %v", r.requests())
			}
		}
	})

	t.Run("a node of the other edition gets nothing from the release", func(t *testing.T) {
		for _, tc := range []struct {
			manager nodeproto.Edition
			node    proto.NodeInfo
		}{{nodeproto.EditionFull, coreNode}, {nodeproto.EditionCore, fullNode}} {
			r := site(t)
			d, _ := manager(t, tc.manager, r.srv.URL)
			pkg, err := d.nodePackageForInfo(ctx, tc.node)
			if !isOtherEdition(err) {
				t.Fatalf("a %s manager found %+v for a node of edition %q: %v", tc.manager, pkg, tc.node.Edition, err)
			}
			if len(r.requests()) != 0 {
				t.Fatalf("it asked the release site: %v", r.requests())
			}
		}
		// A word this manager does not know is neither edition.
		odd := fullNode
		odd.Edition = "lite"
		d, _ := manager(t, nodeproto.EditionFull, site(t).srv.URL)
		if _, err := d.nodePackageForInfo(ctx, odd); !isOtherEdition(err) {
			t.Fatalf("an unknown edition: %v", err)
		}
	})

	t.Run("a build in the node binary dir goes only to a node of its edition", func(t *testing.T) {
		d, dir := manager(t, nodeproto.EditionFull, "")
		local := filepath.Join(dir, name)
		copyLocalNodeBuild(t, coreBuild, local)
		if pkg, err := d.nodePackageForInfo(ctx, fullNode); err == nil {
			t.Fatalf("a core-only build was taken for a full node: %+v", pkg)
		}
		// The owner put the core edition's build there for a core-only computer: that one gets it.
		pkg, err := d.nodePackageForInfo(ctx, coreNode)
		if err != nil || pkg.path != local {
			t.Fatalf("the core-only build for the core-only node: %+v %v", pkg, err)
		}
		d, dir = manager(t, nodeproto.EditionCore, "")
		copyLocalNodeBuild(t, fullBuild, filepath.Join(dir, name))
		if pkg, err := d.nodePackageForInfo(ctx, coreNode); err == nil {
			t.Fatalf("a full build was taken for a core-only node: %+v", pkg)
		}
	})

	t.Run("a manifest of the other edition under this edition's name is refused", func(t *testing.T) {
		r := newFakeRelease(t)
		full := r.publish(t, hub, overlapping(hub, 202611260000), true, map[string]string{name: fullBuild})
		b, _ := json.Marshal(full)
		r.mu.Lock()
		r.files["/"+hub+"/core-manifest.json"], r.files["/core-latest.json"] = b, b
		r.mu.Unlock()
		d, _ := manager(t, nodeproto.EditionCore, r.srv.URL)
		if pkg, err := d.nodePackageForInfo(ctx, coreNode); err == nil {
			t.Fatalf("a full release served as the core's was taken: %+v", pkg)
		}
	})

	t.Run("a full build listed in the core's manifest is refused after the download", func(t *testing.T) {
		r := newFakeRelease(t)
		r.publishCore(t, hub, overlapping(hub, 202611260000), map[string]string{name: fullBuild})
		d, _ := manager(t, nodeproto.EditionCore, r.srv.URL)
		pkg, err := d.nodePackageForInfo(ctx, coreNode)
		if err != nil {
			t.Fatal(err)
		}
		if bin, err := d.fetchPackage(ctx, pkg); err == nil || !strings.Contains(err.Error(), "edition") {
			t.Fatalf("a full build fetched as the core's: %s %v", bin, err)
		}
	})
}
