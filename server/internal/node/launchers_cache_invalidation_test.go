package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func fakeLauncherScanOutput(names ...string) []byte {
	var body strings.Builder
	for _, name := range names {
		body.WriteString(name)
		body.WriteByte('\n')
	}
	return []byte(launcherBegin + body.String() + strings.Repeat("\x1d", 9) + launcherEnd)
}

func saveAndRestoreNodeLauncherState(t *testing.T) {
	t.Helper()
	origPath := os.Getenv("PATH")
	resetExtraPath(origPath)

	launcherCache.Lock()
	oldAt, oldList, oldGen, oldInv := launcherCache.at, launcherCache.list, launcherCache.generation, launcherCache.invalidated
	oldCodex, oldAliases, oldSettings, oldScripts := launcherCache.codex, launcherCache.codexAliases, launcherCache.settings, launcherCache.scripts
	launcherCache.Unlock()
	lastPathScan.Lock()
	oldScan := lastPathScan.got
	lastPathScan.Unlock()
	oldScanFn := scanLaunchersFunc

	t.Cleanup(func() {
		scanLaunchersFunc = oldScanFn
		_ = os.Setenv("PATH", origPath)
		extraPath.mu.Lock()
		extraPath.base, extraPath.dirs = nil, nil
		extraPath.mu.Unlock()
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.generation, launcherCache.invalidated = oldAt, oldList, oldGen, oldInv
		launcherCache.codex, launcherCache.codexAliases, launcherCache.settings, launcherCache.scripts = oldCodex, oldAliases, oldSettings, oldScripts
		launcherCache.Unlock()
		lastPathScan.Lock()
		lastPathScan.got = oldScan
		lastPathScan.Unlock()
	})
}

func TestSetExtraPathInvalidatesLauncherCacheOnChange(t *testing.T) {
	saveAndRestoreNodeLauncherState(t)

	launcherCache.Lock()
	launcherCache.at = time.Now()
	launcherCache.list = []proto.Launcher{{Name: "cached"}}
	baseGen, baseInv := launcherCache.generation, launcherCache.invalidated
	launcherCache.Unlock()
	lastPathScan.Lock()
	lastPathScan.got = &pathScan{list: []proto.Launcher{{Name: "path-cached"}}}
	lastPathScan.Unlock()

	dir := t.TempDir()
	n := &Node{home: dir}

	// Changing extra_path clears cache freshness and bumps invalidated without forging a new discovery generation.
	n.setExtraPath([]string{filepath.Join(dir, "bin")})
	launcherCache.Lock()
	if !launcherCache.at.IsZero() || launcherCache.generation != baseGen || launcherCache.invalidated != baseInv+1 {
		launcherCache.Unlock()
		t.Fatalf("setExtraPath cache state = at:%v gen:%d->%d inv:%d->%d; want zero at, unchanged gen, inv+1",
			launcherCache.at, baseGen, launcherCache.generation, baseInv, launcherCache.invalidated)
	}
	launcherCache.at = time.Now()
	afterGen, afterInv := launcherCache.generation, launcherCache.invalidated
	launcherCache.Unlock()
	lastPathScan.Lock()
	if lastPathScan.got != nil {
		lastPathScan.Unlock()
		t.Fatal("setExtraPath did not clear lastPathScan.got")
	}
	lastPathScan.Unlock()

	// Applying the same extra_path again is a no-op and preserves the warm cache.
	n.setExtraPath([]string{filepath.Join(dir, "bin")})
	launcherCache.Lock()
	defer launcherCache.Unlock()
	if launcherCache.at.IsZero() || launcherCache.generation != afterGen || launcherCache.invalidated != afterInv {
		t.Fatalf("unchanged setExtraPath invalidated warm launcherCache: at=%v gen=%d->%d inv=%d->%d",
			launcherCache.at, afterGen, launcherCache.generation, afterInv, launcherCache.invalidated)
	}
}

func TestSetExtraPathInvalidationDoesNotShortCircuitForcedRefresh(t *testing.T) {
	saveAndRestoreNodeLauncherState(t)

	launcherCache.Lock()
	launcherCache.at = time.Now()
	launcherCache.list = []proto.Launcher{{Name: "pre-invalidation"}}
	baseGen := launcherCache.generation
	launcherCache.Unlock()

	var scans atomic.Int32
	scanLaunchersFunc = func(context.Context) ([]byte, error) {
		scans.Add(1)
		return fakeLauncherScanOutput("post-extra-path"), nil
	}

	dir := t.TempDir()
	n := &Node{home: dir}

	// setExtraPath invalidates without bumping generation; forced refresh must still run a fresh scan.
	n.setExtraPath([]string{filepath.Join(dir, "bin")})
	got, err := n.launchers(context.Background(), true)
	if err != nil {
		t.Fatalf("forced refresh err = %v", err)
	}
	if scans.Load() != 1 {
		t.Fatalf("scan count = %d, want 1", scans.Load())
	}
	if !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "post-extra-path" }) ||
		slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "pre-invalidation" }) {
		t.Fatalf("forced refresh after setExtraPath = %v, want post-extra-path", got)
	}
	launcherCache.Lock()
	gotGen := launcherCache.generation
	launcherCache.Unlock()
	if gotGen != baseGen+1 {
		t.Fatalf("generation after published discovery = %d, want %d", gotGen, baseGen+1)
	}
}

func TestSetExtraPathInvalidationDuringInFlightScanForcesRescanForConcurrentRefresh(t *testing.T) {
	saveAndRestoreNodeLauncherState(t)

	launcherCache.Lock()
	launcherCache.at = time.Now()
	launcherCache.list = []proto.Launcher{{Name: "initial"}}
	baseGen := launcherCache.generation
	launcherCache.Unlock()

	scan1Started := make(chan struct{})
	releaseScan1 := make(chan struct{})
	var scans atomic.Int32
	scanLaunchersFunc = func(context.Context) ([]byte, error) {
		n := scans.Add(1)
		if n == 1 {
			close(scan1Started)
			<-releaseScan1
			return fakeLauncherScanOutput("stale-pre-extra-path"), nil
		}
		return fakeLauncherScanOutput("fresh-post-extra-path"), nil
	}

	dir := t.TempDir()
	n := &Node{home: dir}

	type answer struct {
		list []proto.Launcher
		err  error
	}
	r1Done := make(chan answer, 1)
	go func() {
		list, err := n.launchers(context.Background(), true)
		r1Done <- answer{list: list, err: err}
	}()

	select {
	case <-scan1Started:
	case <-time.After(5 * time.Second):
		close(releaseScan1)
		t.Fatal("first scan did not start")
	}

	// Invalidate via setExtraPath while scan #1 is still running.
	n.setExtraPath([]string{filepath.Join(dir, "bin")})

	// A second concurrent forced refresh joins the in-flight singleflight group.
	r2Queued := make(chan struct{})
	r2Ctx := context.WithValue(context.Background(), launcherFetchObserverKey{}, func() { close(r2Queued) })
	r2Done := make(chan answer, 1)
	go func() {
		list, err := n.launchers(r2Ctx, true)
		r2Done <- answer{list: list, err: err}
	}()

	select {
	case <-r2Queued:
	case <-time.After(5 * time.Second):
		close(releaseScan1)
		t.Fatal("second forced refresh did not join singleflight")
	}

	// Complete the stale scan #1; both callers must observe invalidation, re-scan, and receive fresh-post-extra-path.
	close(releaseScan1)

	for name, ch := range map[string]chan answer{"first": r1Done, "second": r2Done} {
		select {
		case got := <-ch:
			if got.err != nil {
				t.Fatalf("%s refresh err = %v", name, got.err)
			}
			if !slices.ContainsFunc(got.list, func(l proto.Launcher) bool { return l.Name == "fresh-post-extra-path" }) ||
				slices.ContainsFunc(got.list, func(l proto.Launcher) bool { return l.Name == "stale-pre-extra-path" || l.Name == "initial" }) {
				t.Fatalf("%s refresh = %v, want fresh-post-extra-path", name, got.list)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s refresh timed out", name)
		}
	}

	if got := scans.Load(); got != 2 {
		t.Fatalf("scan count = %d, want 2 (stale scan + post-invalidation rescan)", got)
	}
	launcherCache.Lock()
	cachedList, cachedAt, cachedGen := launcherCache.list, launcherCache.at, launcherCache.generation
	launcherCache.Unlock()
	if cachedAt.IsZero() || cachedGen != baseGen+1 ||
		!slices.ContainsFunc(cachedList, func(l proto.Launcher) bool { return l.Name == "fresh-post-extra-path" }) {
		t.Fatalf("final cache = list:%v at:%v gen:%d (base %d); want fresh-post-extra-path with gen %d",
			cachedList, cachedAt, cachedGen, baseGen, baseGen+1)
	}
}
