package osproc

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestSealInheritedSealsSystemResolver: macOS's system resolver opens one AF_SYSTEM control socket on its first
// lookup and retains it. It must exist when SealInherited runs, and later local lookups and a loopback dial must reuse
// it rather than opening another inheritable descriptor.
func TestSealInheritedSealsSystemResolver(t *testing.T) {
	res := runResolverHelper(t, "cgo")
	t.Logf("system resolver descriptors: before=%v sealed=%v after lookup=%v after later use=%v",
		res.Before.FDs, res.Sealed.FDs, res.AfterLookup.FDs, res.AfterUse.FDs)
	t.Logf("children: first=%v (system %v), second=%v (system %v)",
		res.FirstChild.FDs, res.FirstChild.SystemSockets, res.SecondChild.FDs, res.SecondChild.SystemSockets)
	if len(res.Before.SystemSockets) != 0 {
		t.Fatalf("the fresh helper already held system sockets: %+v", res.Before)
	}
	if len(res.AfterLookup.SystemSockets) != 1 {
		t.Fatalf("the first system lookup did not leave exactly one system socket: %+v", res.AfterLookup)
	}
	if !slices.Equal(res.AfterUse.SystemSockets, res.AfterLookup.SystemSockets) {
		t.Errorf("later lookups and a dial changed system sockets from %v to %v",
			res.AfterLookup.SystemSockets, res.AfterUse.SystemSockets)
	}
	for name, child := range map[string]struct {
		fds     map[int]string
		systems []int
	}{"first": {res.FirstChild.FDs, res.FirstChild.SystemSockets}, "second": {res.SecondChild.FDs, res.SecondChild.SystemSockets}} {
		if len(child.fds) != 0 {
			t.Errorf("%s child inherited descriptors %v (system sockets %v); want none", name, child.fds, child.systems)
		}
	}
}

func TestGoResolverOpensNoSystemControlSocket(t *testing.T) {
	res := runResolverHelper(t, "go")
	if !slices.Equal(res.AfterLookup.SystemSockets, res.Before.SystemSockets) ||
		!slices.Equal(res.AfterUse.SystemSockets, res.Before.SystemSockets) {
		t.Fatalf("the Go resolver changed system sockets: before %v, after lookup %v, after later use %v",
			res.Before.SystemSockets, res.AfterLookup.SystemSockets, res.AfterUse.SystemSockets)
	}
	for name, child := range map[string]map[int]string{"first": res.FirstChild.FDs, "second": res.SecondChild.FDs} {
		if len(child) != 0 {
			t.Errorf("%s child inherited descriptors %v; want none", name, child)
		}
	}
}

// reliableResolverWarmup keeps the real resolver but gives a helper five seconds, so the test does not reject the
// production contract's allowed 50 ms timeout path when the machine is loaded. A missing warm-up still fails the test.
func reliableResolverWarmup() {
	resolverLookupHost = func(_ context.Context, host string) ([]string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return net.DefaultResolver.LookupHost(ctx, host)
	}
}

func instantResolverWarmup(t *testing.T) {
	t.Helper()
	original := resolverLookupHost
	resolverLookupHost = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	t.Cleanup(func() { resolverLookupHost = original })
}

func TestResolverWarmupDeadline(t *testing.T) {
	original := resolverLookupHost
	t.Cleanup(func() { resolverLookupHost = original })
	type lookupResult struct {
		host string
		err  error
	}
	started := make(chan struct{})
	lookupDone := make(chan lookupResult, 1)
	resolverLookupHost = func(ctx context.Context, host string) ([]string, error) {
		close(started)
		<-ctx.Done()
		lookupDone <- lookupResult{host: host, err: ctx.Err()}
		return nil, ctx.Err()
	}
	sealed := make(chan struct{})
	go func() {
		SealInherited()
		close(sealed)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("SealInherited did not start the resolver warm-up")
	}
	select {
	case <-sealed:
	case <-time.After(time.Second):
		t.Fatal("SealInherited stayed blocked after the resolver deadline")
	}
	result := <-lookupDone
	if result.host != "localhost" {
		t.Errorf("warm-up looked up %q, want localhost", result.host)
	}
	if !errors.Is(result.err, context.DeadlineExceeded) {
		t.Errorf("slow lookup ended with %v, want context deadline exceeded", result.err)
	}
}

func TestResolverWarmupIgnoresErrors(t *testing.T) {
	original := resolverLookupHost
	t.Cleanup(func() { resolverLookupHost = original })
	called := make(chan string, 1)
	resolverLookupHost = func(_ context.Context, host string) ([]string, error) {
		called <- host
		return nil, errors.New("injected resolver failure")
	}
	SealInherited()
	select {
	case host := <-called:
		if host != "localhost" {
			t.Errorf("warm-up looked up %q, want localhost", host)
		}
	case <-time.After(time.Second):
		t.Fatal("SealInherited did not call the resolver warm-up")
	}
}

// TestResolverWarmupCost measures fresh processes, not repeated lookups in one already-warm process. It is opt-in
// because startup timings belong in the change's measurements, not in pass/fail assertions on a shared machine.
func TestResolverWarmupCost(t *testing.T) {
	if os.Getenv("PERSONA_TEST_RESOLVER_TIMING") != "1" {
		t.Skip("set PERSONA_TEST_RESOLVER_TIMING=1 to measure 10 fresh processes")
	}
	durations := make([]time.Duration, 0, 10)
	for range 10 {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), resolverHelperEnv+"=timing", "GODEBUG=netdns=cgo")
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("timing helper: %v", err)
		}
		text := strings.TrimSpace(string(out))
		ns, ok := strings.CutPrefix(text, resolverTimingPrefix)
		if !ok {
			t.Fatalf("timing helper returned %q", out)
		}
		value, err := strconv.ParseInt(ns, 10, 64)
		if err != nil {
			t.Fatalf("timing helper duration %q: %v", text, err)
		}
		durations = append(durations, time.Duration(value))
	}
	slices.Sort(durations)
	median := (durations[4] + durations[5]) / 2
	t.Logf("10 fresh processes: median=%s worst=%s samples=%s", median, durations[len(durations)-1], durations)
}
