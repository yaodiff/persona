package devices

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

func TestValidateSSHHostAndRemotePath(t *testing.T) {
	for _, valid := range []string{
		"box.invalid",
		"user@box.invalid",
		"user@192.0.2.10",
		"[::1]",
		"user@[::1]",
		"fe80::1%en0",
		"user@[fe80::1%en0]",
		"dev-box_1.internal",
	} {
		if err := validateSSHHost(valid); err != nil {
			t.Errorf("validateSSHHost(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"-oProxyCommand=id",
		"user@-oProxyCommand=id",
		"-user@box.invalid",
		"ssh://-oProxyCommand=id",
		"ssh://box.invalid",
		"user@",
		"@box.invalid",
		"a@b@box.invalid",
		"box.invalid extra",
		"box.invalid\u00a0",
		"box.invalid\n",
		"box.invalid;id",
		"box`id`.invalid",
		"box$(id).invalid",
		"box|id",
		"box%h",
		"user%r@box.invalid",
		"[::1",
		"::1]",
		"[not-ipv6]",
		"fe80::1%-badzone",
		"fe80::1%en0;id",
	} {
		if err := validateSSHHost(invalid); err == nil {
			t.Errorf("validateSSHHost(%q) = nil, want error", invalid)
		}
	}

	for _, valid := range []string{
		"~/.persona/bin/personad",
		"/home/u/.persona/bin/persona-node",
		"/opt/node/run-me",
		"~/odd 'dir'/personad",
	} {
		if err := validateRemotePath(valid); err != nil {
			t.Errorf("validateRemotePath(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{
		"",
		"-o",
		"--help",
		"/tmp/dir/",
		"/tmp/.",
		"/tmp/..",
		".",
		"..",
		"/tmp/persona\nnode",
		"/tmp/persona\rnode",
		"/tmp/persona\x00node",
		"/tmp/persona\x1bnode",
		"/tmp/persona\x7fnode",
	} {
		if err := validateRemotePath(invalid); err == nil {
			t.Errorf("validateRemotePath(%q) = nil, want error", invalid)
		}
	}
}

func TestDeployRejectsUnsafeSSHHostAndRemotePath(t *testing.T) {
	calls := fakeSSH(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "persona-node-linux-amd64")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name       string
		host       string
		executable string
	}{
		{name: "leading dash ssh option", host: "-oProxyCommand=id", executable: "/home/u/.persona/bin/persona-node"},
		{name: "user at leading dash host", host: "alice@-oProxyCommand=id", executable: "/home/u/.persona/bin/persona-node"},
		{name: "ssh uri host", host: "ssh://-oProxyCommand=id", executable: "/home/u/.persona/bin/persona-node"},
		{name: "whitespace in host", host: "box.invalid extra", executable: "/home/u/.persona/bin/persona-node"},
		{name: "control byte in host", host: "box.invalid\n", executable: "/home/u/.persona/bin/persona-node"},
		{name: "newline in remote executable", host: "box.invalid", executable: "/home/u/.persona/bin/persona-node\n/tmp/x"},
		{name: "nul in remote executable", host: "box.invalid", executable: "/home/u/.persona/bin/persona-node\x00x"},
		{name: "directory remote executable", host: "box.invalid", executable: "/home/u/.persona/bin/"},
		{name: "leading dash remote executable", host: "box.invalid", executable: "-o"},
		{name: "dotdot remote executable", host: "box.invalid", executable: "/home/u/.persona/bin/.."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.Remove(calls)
			m := newMachine(newTestDevices(t), MachineConfig{ID: "box", Kind: "ssh", SSHHost: tc.host})
			m.mu.Lock()
			m.info = proto.NodeInfo{Program: nodeproto.ProgramPersonaNode, Executable: tc.executable}
			m.mu.Unlock()
			err := m.deploy(context.Background(), bin, m.d.hubVersion())
			if err == nil {
				t.Fatalf("deploy unexpectedly succeeded for %+v", tc)
			}
			if got := sshCalls(t, calls); len(got) != 0 {
				t.Fatalf("ssh was executed despite invalid input: %v", got)
			}
		})
	}

	// open() and directWorks() over ssh also refuse unsafe hosts before invoking ssh, and command() places -- before host.
	for _, badHost := range []string{"-oProxyCommand=id", "alice@-oProxyCommand=id", "ssh://-oProxyCommand=id"} {
		_ = os.Remove(calls)
		bad := newMachine(newTestDevices(t), MachineConfig{ID: "box", Kind: "ssh", SSHHost: badHost})
		if _, err := bad.open(context.Background(), "ssh"); err == nil {
			t.Fatalf("open accepted invalid ssh host %q", badHost)
		}
		if err := bad.directWorks(context.Background()); err == nil {
			t.Fatalf("directWorks accepted invalid ssh host %q", badHost)
		}
		if got := sshCalls(t, calls); len(got) != 0 {
			t.Fatalf("ssh was invoked for invalid host %q: %v", badHost, got)
		}
	}

	good := newMachine(newTestDevices(t), MachineConfig{ID: "box", Kind: "ssh", SSHHost: "box.invalid"})
	args := good.command(context.Background()).Args
	sepIdx := -1
	for i, a := range args {
		if a == "--" {
			sepIdx = i
			break
		}
	}
	if sepIdx < 0 || sepIdx+1 >= len(args) || args[sepIdx+1] != "box.invalid" {
		t.Fatalf("command() missing -- before host: %q", args)
	}

	_ = os.Remove(calls)
	_ = good.directWorks(context.Background())
	if got := strings.Join(sshCalls(t, calls), "\n"); !strings.Contains(got, "-- box.invalid true") {
		t.Fatalf("directWorks ssh invocation missing -- before host: %q", got)
	}

	_ = os.Remove(calls)
	good.mu.Lock()
	good.info = proto.NodeInfo{Program: nodeproto.ProgramPersonaNode, Executable: "/home/u/.persona/bin/persona-node"}
	good.mu.Unlock()
	_ = good.deploy(context.Background(), bin, good.d.hubVersion())
	got := strings.Join(sshCalls(t, calls), "\n")
	if !strings.Contains(got, "-- box.invalid ") {
		t.Fatalf("deploy ssh invocation missing -- before host: %q", got)
	}

	// linkSwitchRun.ssh also refuses leading-dash hosts before invoking ssh and places -- before a valid host.
	dLS := newTestDevices(t)
	var lsArgv []string
	dLS.switches.run = func(_ context.Context, _ string, argv []string, _ io.Reader, _ io.Writer) (int, error) {
		lsArgv = append([]string(nil), argv...)
		return 0, nil
	}
	badLS := &linkSwitchRun{d: dLS, rec: linkSwitchRecord{MachineID: "box", Operation: "op1", SSHHost: "-oProxyCommand=id"}}
	if exit, _ := badLS.ssh(context.Background(), "probe", "true", nil, time.Second); exit != -1 || len(lsArgv) != 0 {
		t.Fatalf("linkSwitchRun.ssh executed invalid host: exit=%d argv=%q", exit, lsArgv)
	}
	goodLS := &linkSwitchRun{d: dLS, rec: linkSwitchRecord{MachineID: "box", Operation: "op1", SSHHost: "box.invalid"}}
	if exit, _ := goodLS.ssh(context.Background(), "probe", "true", nil, time.Second); exit != 0 {
		t.Fatalf("linkSwitchRun.ssh failed for valid host: exit=%d", exit)
	}
	if len(lsArgv) < 3 || lsArgv[len(lsArgv)-3] != "--" || lsArgv[len(lsArgv)-2] != "box.invalid" {
		t.Fatalf("linkSwitchRun.ssh missing -- before host: %q", lsArgv)
	}
}

func TestLookupSharedCallerCancelDoesNotFailWaitersOrPoisonCache(t *testing.T) {
	plat := otherOS + "-" + otherArch
	bin := updatetest.Program(t, "personad", otherOS, otherArch)

	t.Run("concurrent waiter succeeds after caller 1 cancels", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: bin})

		started := make(chan struct{})
		release := make(chan struct{})
		var latestCalls atomic.Int32
		var startOnce sync.Once
		gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/latest.json" {
				latestCalls.Add(1)
				startOnce.Do(func() { close(started) })
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			site.srv.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
			gated.Close()
		})

		d, _, _ := packageDevices(t, "1.2.3", gated.URL)
		k := d.packageKey(otherOS, otherArch, "")

		ctx1, cancel1 := context.WithCancel(context.Background())
		err1Ch := make(chan error, 1)
		go func() {
			_, err := d.nodePackageFor(ctx1, otherOS, otherArch, "")
			err1Ch <- err
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("first lookup did not reach /latest.json")
		}

		type res struct {
			pkg nodePackage
			err error
		}
		res2Ch := make(chan res, 1)
		go func() {
			pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
			res2Ch <- res{pkg: pkg, err: err}
		}()

		deadline := time.Now().Add(5 * time.Second)
		for {
			d.packages.mu.Lock()
			c := d.packages.looking[k]
			waiters := 0
			if c != nil {
				waiters = c.waiters
			}
			d.packages.mu.Unlock()
			if waiters == 2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("caller 2 did not join shared lookup (waiters=%d)", waiters)
			}
			<-time.After(5 * time.Millisecond)
		}

		cancel1()
		select {
		case err := <-err1Ch:
			if !siteAway(err) {
				t.Fatalf("canceled caller 1 got %v, want package_unreachable", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled caller 1 did not return")
		}

		close(release)
		select {
		case r2 := <-res2Ch:
			if r2.err != nil || r2.pkg.Version != "v9-1" {
				t.Fatalf("caller 2 got %+v, %v; want v9-1", r2.pkg, r2.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("caller 2 did not receive shared lookup result")
		}

		if got := latestCalls.Load(); got != 1 {
			t.Fatalf("expected 1 /latest.json fetch, got %d", got)
		}
		cached, ok := d.cachedPackage(k)
		if !ok || cached.err != nil || cached.pkg.Version != "v9-1" {
			t.Fatalf("cache was not populated cleanly: ok=%v cached=%+v", ok, cached)
		}
	})

	t.Run("sole caller cancel aborts upstream lookup without poisoning cache", func(t *testing.T) {
		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: bin})

		started := make(chan struct{})
		canceled := make(chan struct{})
		var gate atomic.Bool
		gate.Store(true)
		var startOnce, cancelOnce sync.Once
		gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/latest.json" && gate.Load() {
				startOnce.Do(func() { close(started) })
				<-r.Context().Done()
				cancelOnce.Do(func() { close(canceled) })
				return
			}
			site.srv.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(gated.Close)

		d, _, _ := packageDevices(t, "1.2.3", gated.URL)
		k := d.packageKey(otherOS, otherArch, "")

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, err := d.nodePackageFor(ctx, otherOS, otherArch, "")
			errCh <- err
		}()

		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("sole lookup did not reach /latest.json")
		}

		// Capture in-flight call before canceling so we can wait for its cleanup defer before checking the cache.
		d.packages.mu.Lock()
		inFlight := d.packages.looking[k]
		d.packages.mu.Unlock()
		if inFlight == nil {
			t.Fatal("expected in-flight shared lookup before cancel")
		}

		cancel()
		select {
		case err := <-errCh:
			if !siteAway(err) {
				t.Fatalf("canceled sole caller got %v, want package_unreachable", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("canceled sole caller did not return")
		}

		select {
		case <-canceled:
		case <-time.After(5 * time.Second):
			t.Fatal("abandoned lookup did not cancel upstream HTTP request")
		}

		select {
		case <-inFlight.done:
		case <-time.After(5 * time.Second):
			t.Fatal("abandoned lookup goroutine did not finish")
		}

		if r, ok := d.cachedPackage(k); ok {
			t.Fatalf("abandoned lookup poisoned cache: %+v", r)
		}

		gate.Store(false)
		pkg, err := d.nodePackageFor(context.Background(), otherOS, otherArch, "")
		if err != nil || pkg.Version != "v9-1" {
			t.Fatalf("subsequent lookup after abandoned cancel failed: %+v %v", pkg, err)
		}
	})

	t.Run("lookupSoon site timeout preserves offline fallback and caches miss backoff", func(t *testing.T) {
		oldBudget := packageLookupBudget
		packageLookupBudget = 80 * time.Millisecond
		defer func() { packageLookupBudget = oldBudget }()

		site := newFakeRelease(t)
		site.publish(t, "v9-1", overlapping("v9-1", 9), true, map[string]string{"personad-" + plat: bin})

		var hang atomic.Bool
		gated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if hang.Load() {
				<-r.Context().Done()
				return
			}
			site.srv.Config.Handler.ServeHTTP(w, r)
		}))
		t.Cleanup(gated.Close)

		// Case 1: no prior result -> lookupSoon timeout caches package_unreachable for packageMissTTL.
		dMiss, _, _ := packageDevices(t, "1.2.3", gated.URL)
		kMiss := dMiss.packageKey(otherOS, otherArch, "")
		hang.Store(true)
		dMiss.lookupSoon(kMiss)
		deadline := time.Now().Add(5 * time.Second)
		for {
			if r, ok := dMiss.cachedPackage(kMiss); ok {
				if !siteAway(r.err) {
					t.Fatalf("cached timeout error = %v, want package_unreachable", r.err)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("lookupSoon timeout did not cache packageMissTTL backoff result")
			}
			<-time.After(10 * time.Millisecond)
		}

		// Case 2: prior found result expired -> lookupSoon timeout falls back to stale stand-in (lastFound).
		hang.Store(false)
		dFallback, _, _ := packageDevices(t, "1.2.3", gated.URL)
		now := time.Now()
		var nowMu sync.Mutex
		dFallback.packages.now = func() time.Time {
			nowMu.Lock()
			defer nowMu.Unlock()
			return now
		}
		kFB := dFallback.packageKey(otherOS, otherArch, "")
		pkg, err := dFallback.nodePackageFor(context.Background(), otherOS, otherArch, "")
		if err != nil || pkg.Version != "v9-1" {
			t.Fatalf("initial lookup = (%+v, %v), want v9-1", pkg, err)
		}
		nowMu.Lock()
		now = now.Add(packageFoundTTL + time.Second)
		nowMu.Unlock()
		hang.Store(true)
		dFallback.lookupSoon(kFB)
		deadline = time.Now().Add(5 * time.Second)
		for {
			if r, ok := dFallback.cachedPackage(kFB); ok {
				if r.err != nil || r.pkg.Version != "v9-1" || !r.pkg.stale {
					t.Fatalf("expected stale stand-in v9-1 after timeout, got %+v", r)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("lookupSoon timeout did not store offline stand-in fallback")
			}
			<-time.After(10 * time.Millisecond)
		}
	})
}

func TestStartTransferPrunesExpiredCompletedEntries(t *testing.T) {
	d, _, _, _, mux := transferDevices(t, &sourceNode{data: []byte("hello")}, &targetNode{})

	tr1 := started(t, postTransfer(t, mux, transferBody))
	d.transfers.mu.Lock()
	entry1 := d.transfers.byID[tr1.ID]
	d.transfers.mu.Unlock()
	if entry1 == nil {
		t.Fatalf("missing transfer %s", tr1.ID)
	}
	select {
	case <-entry1.done:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer 1 did not finish")
	}

	entry1.mu.Lock()
	entry1.st.UpdatedAt = time.Now().Add(-transferKeep - time.Minute)
	entry1.mu.Unlock()

	tr2 := started(t, postTransfer(t, mux, transferBody))
	d.transfers.mu.Lock()
	_, keptOld := d.transfers.byID[tr1.ID]
	entry2 := d.transfers.byID[tr2.ID]
	d.transfers.mu.Unlock()

	if keptOld {
		t.Fatalf("startTransfer failed to prune expired completed transfer %s", tr1.ID)
	}
	if entry2 == nil {
		t.Fatalf("missing new transfer %s", tr2.ID)
	}
	select {
	case <-entry2.done:
	case <-time.After(5 * time.Second):
		t.Fatal("transfer 2 did not finish")
	}
}

func TestStreamChunksWaitsForReaderGoroutineExitOnTargetError(t *testing.T) {
	data := bytes.Repeat([]byte{9}, proto.FileDownloadChunkBytes*3)
	chunk1Entered := make(chan struct{})
	releaseSrc := make(chan struct{})
	t.Cleanup(func() { close(releaseSrc) })

	src := &sourceNode{data: data}
	src.onChunk = func(off int64) error {
		if off == proto.FileDownloadChunkBytes {
			close(chunk1Entered)
			<-releaseSrc
		}
		return nil
	}
	dst := &targetNode{}
	dst.onChunk = func(n int) error {
		if n == 0 {
			select {
			case <-chunk1Entered:
			case <-time.After(5 * time.Second):
				return errors.New("timed out waiting for reader goroutine to enter chunk 1")
			}
			return errors.New("disk full")
		}
		return nil
	}

	_, a, b, _, _ := transferDevices(t, src, dst)
	req := proto.FileChunkRequest{Path: "/home/me/Movies/movie.mov", Cwd: "/home/me", Revision: "r0"}
	err := streamChunks(context.Background(), a, b, req, "u1", int64(len(data)), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("streamChunks err = %v, want disk full", err)
	}

	// Reader goroutine must exit before releaseSrc is closed (allow brief frame teardown under -race).
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		var stacks bytes.Buffer
		_ = pprof.Lookup("goroutine").WriteTo(&stacks, 2)
		if !strings.Contains(stacks.String(), "streamChunks.func") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("streamChunks reader goroutine still running after return:\n%s", stacks.String())
		}
		<-time.After(10 * time.Millisecond)
	}
}
