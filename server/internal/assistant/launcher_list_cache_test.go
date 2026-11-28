package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
)

func countLauncherListRPCs(f *fakeHub, machine string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.Method == DeviceLaunchersList && (machine == "" || call.Device == machine) {
			n++
		}
	}
	return n
}

func TestLauncherListCacheHitAcrossHotPaths(t *testing.T) {
	f := newFakeHub().withMachine()
	f.devices = []Device{{ID: "server", Name: "server", Online: true, Reachable: true, ClaudeVersion: "2.1.308"}}
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "claude"},
			{Name: "opencode", Source: "opencode", Agent: "opencode", Assistant: true},
		},
	}
	f.doctor = map[string]string{"server": "2.1.308"}
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.opencode"})

	// 1. Machine launchers picker (?purpose=assistant) populates the per-machine cache.
	if names := pickerNames(t, s, "assistant"); !slices.Contains(names, "opencode") {
		t.Fatalf("assistant picker = %v, want opencode", names)
	}
	if got := countLauncherListRPCs(f, "server"); got != 1 {
		t.Fatalf("after first picker: launchers.list RPCs = %d, want 1", got)
	}

	// 2. Subsequent picker calls across purposes share the same host="" cache entry.
	for _, purpose := range []string{"assistant", "task", "agent.new", ""} {
		if names := pickerNames(t, s, purpose); !slices.Contains(names, "opencode") {
			t.Fatalf("picker(%q) = %v, want opencode", purpose, names)
		}
	}

	// 3. GET /api/v1/assistant/readiness hits the same cache entry.
	r := request(s, "GET", "/api/v1/assistant/readiness", "")
	if r.Code != http.StatusOK {
		t.Fatalf("readiness: %d %s", r.Code, r.Body.String())
	}

	// 4. POST /api/v1/assistant/conversations reuses the cache across mainChatFit,
	// finishMainChatModel, withNewConversationProfile, startPermissionModeSupported, and launcherAgent.
	r = request(s, "POST", "/api/v1/assistant/conversations", `{"prompt":"hello","launcher":"opencode"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("start conversation: %d %s", r.Code, r.Body.String())
	}

	// 5. runtimeAssistantLauncher also shares the cache.
	if !s.runtimeAssistantLauncher(context.Background(), "server", "opencode") {
		t.Fatal("runtimeAssistantLauncher returned false for opencode")
	}

	if got := countLauncherListRPCs(f, "server"); got != 1 {
		t.Fatalf("total launchers.list RPCs across all hot paths = %d, want 1", got)
	}
}

func TestLauncherListCachePerMachineAndHostIsolation(t *testing.T) {
	f := newFakeHub().withMachine()
	f.devices = []Device{
		{ID: "m1", Name: "M1", Online: true, Reachable: true},
		{ID: "m2", Name: "M2", Online: true, Reachable: true},
	}
	f.zshrc = map[string][]proto.Launcher{
		"m1": {{Name: "m1-shell", Source: "zshrc"}},
		"m2": {{Name: "m2-shell", Source: "zshrc"}},
	}
	f.hostLaunchers = map[string][]proto.Launcher{
		"m1": {{Name: "m1-paseo", Source: "paseo", Agent: "paseo"}},
	}
	f.sessions["m1"], f.sessions["m2"] = nil, nil
	s := newTestService(t, f)
	ctx := context.Background()

	l1, hostRT, _, err := s.machineLaunchersOn(ctx, "m1", "")
	if err != nil || hostRT || !slices.ContainsFunc(l1, func(l proto.Launcher) bool { return l.Name == "m1-shell" }) {
		t.Fatalf("m1 default = %v hostRT=%v err=%v", l1, hostRT, err)
	}
	l1Paseo, hostRT, _, err := s.machineLaunchersOn(ctx, "m1", "paseo")
	if err != nil || !hostRT || !slices.ContainsFunc(l1Paseo, func(l proto.Launcher) bool { return l.Name == "m1-paseo" }) {
		t.Fatalf("m1 paseo = %v hostRT=%v err=%v", l1Paseo, hostRT, err)
	}
	l2, hostRT, _, err := s.machineLaunchersOn(ctx, "m2", "")
	if err != nil || hostRT || !slices.ContainsFunc(l2, func(l proto.Launcher) bool { return l.Name == "m2-shell" }) {
		t.Fatalf("m2 default = %v hostRT=%v err=%v", l2, hostRT, err)
	}

	// Repeat all three reads; each distinct (machine, host) key must hit its own cached entry.
	for i := 0; i < 3; i++ {
		if got, rt, _, err := s.machineLaunchersOn(ctx, "m1", ""); err != nil || rt || len(got) != len(l1) {
			t.Fatalf("repeat m1 default: %v rt=%v err=%v", got, rt, err)
		}
		if got, rt, _, err := s.machineLaunchersOn(ctx, "m1", "paseo"); err != nil || !rt || len(got) != len(l1Paseo) {
			t.Fatalf("repeat m1 paseo: %v rt=%v err=%v", got, rt, err)
		}
		if got, rt, _, err := s.machineLaunchersOn(ctx, "m2", ""); err != nil || rt || len(got) != len(l2) {
			t.Fatalf("repeat m2 default: %v rt=%v err=%v", got, rt, err)
		}
	}
	if got := countLauncherListRPCs(f, "m1"); got != 2 {
		t.Fatalf("m1 RPCs = %d, want 2 (default + paseo)", got)
	}
	if got := countLauncherListRPCs(f, "m2"); got != 1 {
		t.Fatalf("m2 RPCs = %d, want 1", got)
	}
}

func TestLauncherListCacheExpiresAfterTTL(t *testing.T) {
	f := newFakeHub().withMachine()
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "before", Source: "zshrc"}},
	}
	s := newTestService(t, f)

	var clockMu sync.Mutex
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	s.models.launcherLists.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	ctx := context.Background()
	list, err := s.machineLaunchers(ctx, "server")
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "before" }) {
		t.Fatalf("initial = %v, %v", list, err)
	}

	f.mu.Lock()
	f.zshrc["server"] = []proto.Launcher{{Name: "after", Source: "zshrc"}}
	f.mu.Unlock()

	// Just before TTL expires: still returns cached entry.
	advance(launcherListCacheTTL - time.Second)
	list, err = s.machineLaunchers(ctx, "server")
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "before" }) {
		t.Fatalf("before TTL = %v, %v; want cached before", list, err)
	}
	if got := countLauncherListRPCs(f, "server"); got != 1 {
		t.Fatalf("RPCs before TTL = %d, want 1", got)
	}

	// Past TTL: re-fetches from node and caches the fresh response.
	advance(2 * time.Second)
	list, err = s.machineLaunchers(ctx, "server")
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "after" }) {
		t.Fatalf("after TTL = %v, %v; want fresh after", list, err)
	}
	if got := countLauncherListRPCs(f, "server"); got != 2 {
		t.Fatalf("RPCs after TTL = %d, want 2", got)
	}
	// Subsequent read within new TTL hits cache again.
	list, err = s.machineLaunchers(ctx, "server")
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "after" }) {
		t.Fatalf("after re-cache = %v, %v", list, err)
	}
	if got := countLauncherListRPCs(f, "server"); got != 2 {
		t.Fatalf("RPCs after re-cache = %d, want 2", got)
	}
}

func TestLauncherListCacheSingleflightDeduplicatesConcurrentRequests(t *testing.T) {
	f := newFakeHub().withMachine()
	gate := make(chan struct{})
	f.launcherGate = gate
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "shared-launcher", Source: "zshrc"}},
	}
	s := newTestService(t, f)

	const callers = 10
	var entered atomic.Int32
	allEntered := make(chan struct{})
	s.models.launcherLists.onFlightEnter = func(launcherListCacheKey) {
		if entered.Add(1) == callers {
			close(allEntered)
		}
	}

	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				list, err := s.machineLaunchers(context.Background(), "server")
				if err != nil {
					errs <- err
					return
				}
				if !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "shared-launcher" }) {
					errs <- errors.New("missing shared-launcher")
				}
				return
			}
			res, err := s.listLaunchers(context.Background(), Device{ID: "server", Online: true})
			if err != nil {
				errs <- err
				return
			}
			if !slices.ContainsFunc(res.Launchers, func(l proto.Launcher) bool { return l.Name == "shared-launcher" }) {
				errs <- errors.New("missing shared-launcher in listLaunchers")
			}
		}(i)
	}

	select {
	case <-allEntered:
	case <-time.After(5 * time.Second):
		close(gate)
		t.Fatalf("only %d/%d callers entered singleflight", entered.Load(), callers)
	}
	close(gate)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent caller failed: %v", err)
	}
	if got := countLauncherListRPCs(f, "server"); got != 1 {
		t.Fatalf("concurrent callers triggered %d launchers.list RPCs, want 1", got)
	}
}

func TestLauncherListCacheSingleflightCancelIsolation(t *testing.T) {
	t.Run("canceling one waiter does not abort sibling waiters", func(t *testing.T) {
		f := newFakeHub().withMachine()
		gate := make(chan struct{})
		f.launcherGate = gate
		f.zshrc = map[string][]proto.Launcher{
			"server": {{Name: "alive", Source: "zshrc"}},
		}
		s := newTestService(t, f)

		var entered atomic.Int32
		bothEntered := make(chan struct{})
		s.models.launcherLists.onFlightEnter = func(launcherListCacheKey) {
			if entered.Add(1) == 2 {
				close(bothEntered)
			}
		}

		ctxA, cancelA := context.WithCancel(context.Background())
		errA := make(chan error, 1)
		errB := make(chan error, 1)
		go func() {
			_, err := s.machineLaunchers(ctxA, "server")
			errA <- err
		}()
		go func() {
			list, err := s.machineLaunchers(context.Background(), "server")
			if err == nil && !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "alive" }) {
				err = errors.New("missing alive launcher")
			}
			errB <- err
		}()

		select {
		case <-bothEntered:
		case <-time.After(5 * time.Second):
			close(gate)
			t.Fatal("waiters did not enter singleflight")
		}
		cancelA()
		select {
		case err := <-errA:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled waiter err = %v, want context.Canceled", err)
			}
		case <-time.After(2 * time.Second):
			close(gate)
			t.Fatal("canceled waiter did not return promptly")
		}
		close(gate)
		select {
		case err := <-errB:
			if err != nil {
				t.Fatalf("sibling waiter failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("sibling waiter did not complete")
		}
		if got := countLauncherListRPCs(f, "server"); got != 1 {
			t.Fatalf("RPCs = %d, want 1", got)
		}
	})

	t.Run("canceling last waiter cancels rpc and allows immediate fresh retry", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.slowLaunchers = map[string]bool{"server": true}
		f.zshrc = map[string][]proto.Launcher{
			"server": {{Name: "recovered", Source: "zshrc"}},
		}
		s := newTestService(t, f)

		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		defer cancel()
		if _, err := s.machineLaunchers(ctx, "server"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timed out single waiter err = %v, want DeadlineExceeded", err)
		}

		f.mu.Lock()
		f.slowLaunchers = nil
		f.mu.Unlock()
		list, err := s.machineLaunchers(context.Background(), "server")
		if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "recovered" }) {
			t.Fatalf("retry after abandoned flight = %v, %v", list, err)
		}
	})
}

func TestLauncherListCacheExplicitRefreshBypassesAndBackfills(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "assistant refresh=1", query: "purpose=assistant&refresh=1"},
		{name: "assistant refresh=true", query: "purpose=assistant&refresh=true"},
		{name: "task refresh=1", query: "purpose=task&refresh=1"},
		{name: "task refresh=true", query: "purpose=task&refresh=true"},
		{name: "agent.new refresh=1", query: "purpose=agent.new&refresh=1"},
		{name: "default refresh=true", query: "refresh=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.zshrc = map[string][]proto.Launcher{
				"server": {{Name: "v1", Source: "zshrc"}},
			}
			s := newTestService(t, f)

			baseQuery, _, _ := strings.Cut(tc.query, "refresh=")
			baseQuery = strings.TrimSuffix(baseQuery, "&")
			path := "/api/v1/machines/server/launchers"
			if baseQuery != "" {
				path += "?" + baseQuery
			}
			refreshPath := "/api/v1/machines/server/launchers?" + tc.query

			// Initial ordinary read populates cache with v1.
			r := request(s, "GET", path, "")
			if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"v1"`) {
				t.Fatalf("initial %s: %d %s", path, r.Code, r.Body.String())
			}

			// Node changes to v2; ordinary read still serves cached v1.
			f.mu.Lock()
			f.zshrc["server"] = []proto.Launcher{{Name: "v2", Source: "zshrc"}}
			f.mu.Unlock()
			r = request(s, "GET", path, "")
			if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"v1"`) || strings.Contains(r.Body.String(), `"v2"`) {
				t.Fatalf("cached read %s: %d %s", path, r.Code, r.Body.String())
			}
			if got := countLauncherListRPCs(f, "server"); got != 1 {
				t.Fatalf("RPCs before refresh = %d, want 1", got)
			}

			// Explicit refresh bypasses cache, forwards refresh:true, returns v2, and backfills cache.
			r = request(s, "GET", refreshPath, "")
			if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"v2"`) || strings.Contains(r.Body.String(), `"v1"`) {
				t.Fatalf("refresh %s: %d %s", refreshPath, r.Code, r.Body.String())
			}
			if got := countLauncherListRPCs(f, "server"); got != 2 {
				t.Fatalf("RPCs after refresh = %d, want 2", got)
			}
			params := launcherListCalls(f)
			if len(params) < 2 || params[len(params)-1]["refresh"] != true {
				t.Fatalf("refresh call params = %v, want refresh:true", params)
			}

			// Subsequent ordinary read sees backfilled v2 with no new RPC.
			r = request(s, "GET", path, "")
			if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"v2"`) {
				t.Fatalf("backfilled read %s: %d %s", path, r.Code, r.Body.String())
			}
			if got := countLauncherListRPCs(f, "server"); got != 2 {
				t.Fatalf("RPCs after backfilled read = %d, want 2", got)
			}
		})
	}
}

type stubConnectedAtDevices struct {
	Devices
	mu   sync.Mutex
	conn map[string]time.Time
}

func (d *stubConnectedAtDevices) ConnectedAt(id string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn[id]
}

func (d *stubConnectedAtDevices) Canonical(id string) string { return id }

func (d *stubConnectedAtDevices) Has(string) bool { return false }

func (d *stubConnectedAtDevices) List() []devices.Summary { return nil }

func (d *stubConnectedAtDevices) Summary(string) (devices.Summary, bool) {
	return devices.Summary{}, false
}

func (d *stubConnectedAtDevices) MachineResources(string) (*proto.Machine, *proto.MetricSample, *bool) {
	return nil, nil, nil
}

func TestLauncherListCacheInvalidatesOnChangeAndReconnect(t *testing.T) {
	t.Run("launchers.changed invalidates only affected machine", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.devices = []Device{
			{ID: "m1", Name: "M1", Online: true, Reachable: true},
			{ID: "m2", Name: "M2", Online: true, Reachable: true},
		}
		f.zshrc = map[string][]proto.Launcher{
			"m1": {{Name: "m1-v1", Source: "zshrc"}},
			"m2": {{Name: "m2-v1", Source: "zshrc"}},
		}
		f.sessions["m1"], f.sessions["m2"] = nil, nil
		s := newTestService(t, f)
		ctx := context.Background()

		if _, err := s.machineLaunchers(ctx, "m1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.machineLaunchers(ctx, "m2"); err != nil {
			t.Fatal(err)
		}

		f.mu.Lock()
		f.zshrc["m1"] = []proto.Launcher{{Name: "m1-v2", Source: "zshrc"}}
		f.zshrc["m2"] = []proto.Launcher{{Name: "m2-v2", Source: "zshrc"}}
		f.mu.Unlock()

		// launchers.changed on m1 invalidates m1 without touching m2's cache.
		s.NodeEvent("m1", "launchers.changed", json.RawMessage(`{}`))

		gotM1, err := s.machineLaunchers(ctx, "m1")
		if err != nil || !slices.ContainsFunc(gotM1, func(l proto.Launcher) bool { return l.Name == "m1-v2" }) {
			t.Fatalf("m1 after launchers.changed = %v, %v; want m1-v2", gotM1, err)
		}
		gotM2, err := s.machineLaunchers(ctx, "m2")
		if err != nil || !slices.ContainsFunc(gotM2, func(l proto.Launcher) bool { return l.Name == "m2-v1" }) {
			t.Fatalf("m2 was unexpectedly invalidated = %v, %v; want cached m2-v1", gotM2, err)
		}
		if got := countLauncherListRPCs(f, "m2"); got != 1 {
			t.Fatalf("m2 RPCs = %d, want 1", got)
		}
	})

	t.Run("in-flight pre-invalidation fetch does not poison cache", func(t *testing.T) {
		f := newFakeHub().withMachine()
		gate := make(chan struct{})
		f.launcherGate = gate
		f.zshrc = map[string][]proto.Launcher{
			"server": {{Name: "stale", Source: "zshrc"}},
		}
		s := newTestService(t, f)

		entered := make(chan struct{})
		s.models.launcherLists.onFlightEnter = func(launcherListCacheKey) { close(entered) }

		firstDone := make(chan error, 1)
		go func() {
			_, err := s.machineLaunchers(context.Background(), "server")
			firstDone <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(gate)
			t.Fatal("first fetch did not enter singleflight")
		}

		// Wait until the RPC actually reaches fakeHub before mutating & invalidating.
		waitFor(t, "in-flight launchers.list RPC", func() bool {
			return countLauncherListRPCs(f, "server") >= 1
		})

		s.models.launcherLists.mu.Lock()
		s.models.launcherLists.onFlightEnter = nil
		s.models.launcherLists.mu.Unlock()
		// Node state changes and reconnect/invalidation arrives while the first RPC is in flight.
		s.NodeConnected(context.Background(), "server")
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "fresh", Source: "zshrc"}}
		f.launcherGate = nil
		f.mu.Unlock()
		close(gate)

		if err := <-firstDone; err != nil {
			t.Fatalf("first fetch err = %v", err)
		}
		// Next read must fetch fresh data rather than reading the stale in-flight result.
		got, err := s.machineLaunchers(context.Background(), "server")
		if err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "fresh" }) {
			t.Fatalf("post-invalidation read = %v, %v; want fresh", got, err)
		}
	})

	t.Run("node detach, reconnect, managedNodeCalls and ConnectedAt change invalidate machine", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.devices = []Device{
			{ID: "m1", Name: "M1", Online: true, Reachable: true},
			{ID: "m2", Name: "M2", Online: true, Reachable: true},
		}
		f.zshrc = map[string][]proto.Launcher{
			"m1": {{Name: "v1", Source: "zshrc"}},
			"m2": {{Name: "m2-v1", Source: "zshrc"}},
		}
		f.sessions["m1"], f.sessions["m2"] = nil, nil
		s := newTestService(t, f)
		conn1 := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
		devs := &stubConnectedAtDevices{conn: map[string]time.Time{"m1": conn1, "m2": conn1}}
		s.devs = devs
		ctx := context.Background()

		if _, err := s.machineLaunchers(ctx, "m1"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.machineLaunchers(ctx, "m2"); err != nil {
			t.Fatal(err)
		}

		// 1. NodeDetached invalidates m1 only.
		f.mu.Lock()
		f.zshrc["m1"] = []proto.Launcher{{Name: "v2", Source: "zshrc"}}
		f.mu.Unlock()
		s.NodeDetached("m1")
		if got, err := s.machineLaunchers(ctx, "m1"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v2" }) {
			t.Fatalf("after NodeDetached: %v, %v", got, err)
		}

		// 2. NodeConnected invalidates m1 only.
		f.mu.Lock()
		f.zshrc["m1"] = []proto.Launcher{{Name: "v3", Source: "zshrc"}}
		f.mu.Unlock()
		s.NodeConnected(ctx, "m1")
		if got, err := s.machineLaunchers(ctx, "m1"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v3" }) {
			t.Fatalf("after NodeConnected: %v, %v", got, err)
		}

		// 3. managedNodeCalls (new connection setup) invalidates m1 only.
		f.mu.Lock()
		f.zshrc["m1"] = []proto.Launcher{{Name: "v4", Source: "zshrc"}}
		f.mu.Unlock()
		_ = s.managedNodeCalls(ctx, "m1")
		if got, err := s.machineLaunchers(ctx, "m1"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v4" }) {
			t.Fatalf("after managedNodeCalls: %v, %v", got, err)
		}

		// 4. ConnectedAt change automatically invalidates m1 even without an explicit callback.
		f.mu.Lock()
		f.zshrc["m1"] = []proto.Launcher{{Name: "v5", Source: "zshrc"}}
		f.mu.Unlock()
		devs.mu.Lock()
		devs.conn["m1"] = conn1.Add(10 * time.Second)
		devs.mu.Unlock()
		if got, err := s.machineLaunchers(ctx, "m1"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v5" }) {
			t.Fatalf("after ConnectedAt change: %v, %v", got, err)
		}

		// m2 remained cached throughout.
		if got := countLauncherListRPCs(f, "m2"); got != 1 {
			t.Fatalf("m2 RPCs = %d, want 1", got)
		}
	})

	t.Run("launcher config mutations and ProfileChanged invalidate cache", func(t *testing.T) {
		s, f, _ := modelService(t)
		// Seed a primary provider first so the launcher added below is not auto-bound to model purposes and can be deleted.
		if code, body := launcherCall(t, s, "POST", "/api/v1/launchers", `{"name":"primary","base_url":"https://primary.example","model":"m0","api_key":"k0"}`); code != http.StatusOK {
			t.Fatalf("seed POST /launchers: %d %s", code, body)
		}
		f.mu.Lock()
		f.zshrc = map[string][]proto.Launcher{"server": {{Name: "claude"}, {Name: "v1", Source: "zshrc"}}}
		f.mu.Unlock()
		ctx := context.Background()

		if _, err := s.machineLaunchers(ctx, "server"); err != nil {
			t.Fatal(err)
		}

		// POST /api/v1/launchers invalidates cache.
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v2", Source: "zshrc"}}
		f.mu.Unlock()
		if code, body := launcherCall(t, s, "POST", "/api/v1/launchers", `{"name":"gw","base_url":"https://gw.example","model":"m1","api_key":"k"}`); code != http.StatusOK {
			t.Fatalf("POST /launchers: %d %s", code, body)
		}
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v2" }) {
			t.Fatalf("after POST /launchers: %v, %v", got, err)
		}

		// PUT /api/v1/launchers/{name} invalidates cache.
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v3", Source: "zshrc"}}
		f.mu.Unlock()
		if code, body := launcherCall(t, s, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example","model":"m2"}`); code != http.StatusOK {
			t.Fatalf("PUT /launchers: %d %s", code, body)
		}
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v3" }) {
			t.Fatalf("after PUT /launchers: %v, %v", got, err)
		}

		// DELETE /api/v1/launchers/{name} (managed) invalidates cache.
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v4", Source: "zshrc"}}
		f.mu.Unlock()
		if code, body := launcherCall(t, s, "DELETE", "/api/v1/launchers/gw", ""); code != http.StatusOK {
			t.Fatalf("DELETE /launchers: %d %s", code, body)
		}
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v4" }) {
			t.Fatalf("after DELETE /launchers: %v, %v", got, err)
		}

		// DELETE (hide) + POST /restore for an unmanaged launcher invalidate cache.
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v5", Source: "zshrc"}}
		f.mu.Unlock()
		if code, body := launcherCall(t, s, "DELETE", "/api/v1/launchers/claude", ""); code != http.StatusOK {
			t.Fatalf("hide /launchers/claude: %d %s", code, body)
		}
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v5" }) {
			t.Fatalf("after hide /launchers/claude: %v, %v", got, err)
		}

		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v6", Source: "zshrc"}}
		f.mu.Unlock()
		if code, body := launcherCall(t, s, "POST", "/api/v1/launchers/claude/restore", ""); code != http.StatusOK {
			t.Fatalf("restore /launchers/claude: %d %s", code, body)
		}
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v6" }) {
			t.Fatalf("after restore /launchers/claude: %v, %v", got, err)
		}

		// ProfileChanged (hub.json / profile reload) invalidates cache.
		f.mu.Lock()
		f.zshrc["server"] = []proto.Launcher{{Name: "claude"}, {Name: "v7", Source: "zshrc"}}
		f.mu.Unlock()
		s.ProfileChanged(ctx)
		if got, err := s.machineLaunchers(ctx, "server"); err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v7" }) {
			t.Fatalf("after ProfileChanged: %v, %v", got, err)
		}
	})
}

func TestLauncherListCacheDoesNotCacheFailuresOrTimeouts(t *testing.T) {
	t.Run("rpc error is not cached", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.launcherList = nil // causes fakeHub to return an error
		s := newTestService(t, f)
		ctx := context.Background()

		if _, err := s.machineLaunchers(ctx, "server"); err == nil {
			t.Fatal("first call unexpectedly succeeded")
		}
		f.mu.Lock()
		f.launcherList = []proto.Launcher{{Name: "ok-after-err", Source: "zshrc"}}
		f.mu.Unlock()

		got, err := s.machineLaunchers(ctx, "server")
		if err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "ok-after-err" }) {
			t.Fatalf("retry after error = %v, %v; want ok-after-err", got, err)
		}
		if calls := countLauncherListRPCs(f, "server"); calls != 2 {
			t.Fatalf("RPCs = %d, want 2", calls)
		}
	})

	t.Run("timeout is not cached", func(t *testing.T) {
		old := launcherSyncTimeout
		launcherSyncTimeout = 50 * time.Millisecond
		t.Cleanup(func() { launcherSyncTimeout = old })

		f := newFakeHub().withMachine()
		f.slowLaunchers = map[string]bool{"server": true}
		f.zshrc = map[string][]proto.Launcher{"server": {{Name: "ok-after-timeout", Source: "zshrc"}}}
		s := newTestService(t, f)
		ctx := context.Background()

		if _, err := s.listLaunchers(ctx, Device{ID: "server", Online: true}); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timed out read err = %v, want DeadlineExceeded", err)
		}

		f.mu.Lock()
		f.slowLaunchers = nil
		f.mu.Unlock()

		res, err := s.listLaunchers(ctx, Device{ID: "server", Online: true})
		if err != nil || !slices.ContainsFunc(res.Launchers, func(l proto.Launcher) bool { return l.Name == "ok-after-timeout" }) {
			t.Fatalf("retry after timeout = %+v, %v; want ok-after-timeout", res, err)
		}
		if calls := countLauncherListRPCs(f, "server"); calls != 2 {
			t.Fatalf("RPCs = %d, want 2", calls)
		}
	})

	t.Run("failed forced refresh does not cache error or poison subsequent retry", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.failLauncherRefresh = true
		f.zshrc = map[string][]proto.Launcher{"server": {{Name: "v1", Source: "zshrc"}}}
		s := newTestService(t, f)
		ctx := context.Background()

		if _, err := s.machineLaunchers(withLauncherRefresh(ctx), "server"); err == nil {
			t.Fatal("failed refresh unexpectedly succeeded")
		}

		f.mu.Lock()
		f.failLauncherRefresh = false
		f.zshrc["server"] = []proto.Launcher{{Name: "v2", Source: "zshrc"}}
		f.mu.Unlock()

		got, err := s.machineLaunchers(ctx, "server")
		if err != nil || !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "v2" }) {
			t.Fatalf("read after failed refresh = %v, %v; want v2", got, err)
		}
		if calls := countLauncherListRPCs(f, "server"); calls != 2 {
			t.Fatalf("RPCs = %d, want 2", calls)
		}
	})
}

type concurrentLauncherHub struct {
	*fakeHub
	onLaunchersList func(context.Context, DeviceCall) (json.RawMessage, error)
}

func (h *concurrentLauncherHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceLaunchersList && h.onLaunchersList != nil {
		return h.onLaunchersList(ctx, p)
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestLauncherListCacheOutOfOrderRefreshDoesNotOverwriteNewerEntry(t *testing.T) {
	t.Run("older concurrent refresh does not overwrite newer refresh backfill", func(t *testing.T) {
		f := newFakeHub().withMachine()
		r1Started := make(chan struct{})
		releaseR1 := make(chan struct{})
		var calls atomic.Int32
		h := &concurrentLauncherHub{
			fakeHub: f,
			onLaunchersList: func(_ context.Context, _ DeviceCall) (json.RawMessage, error) {
				n := calls.Add(1)
				if n == 1 {
					close(r1Started)
					<-releaseR1
					return json.Marshal(map[string]any{"launchers": []proto.Launcher{{Name: "stale-r1", Source: "zshrc"}}})
				}
				return json.Marshal(map[string]any{"launchers": []proto.Launcher{{Name: "fresh-r2", Source: "zshrc"}}})
			},
		}
		s := newTestService(t, f)
		s.hub = h

		ctx := context.Background()
		type res struct {
			list []proto.Launcher
			err  error
		}
		r1Done := make(chan res, 1)
		go func() {
			list, err := s.machineLaunchers(withLauncherRefresh(ctx), "server")
			r1Done <- res{list: list, err: err}
		}()

		select {
		case <-r1Started:
		case <-time.After(5 * time.Second):
			close(releaseR1)
			t.Fatal("first refresh did not start")
		}

		// Second refresh starts after R1 and completes before R1 finishes.
		r2List, err := s.machineLaunchers(withLauncherRefresh(ctx), "server")
		if err != nil || !slices.ContainsFunc(r2List, func(l proto.Launcher) bool { return l.Name == "fresh-r2" }) {
			close(releaseR1)
			t.Fatalf("second refresh = %v, %v; want fresh-r2", r2List, err)
		}

		// Release the older refresh; it must return its own answer without overwriting fresh-r2 in cache.
		close(releaseR1)
		r1 := <-r1Done
		if r1.err != nil || !slices.ContainsFunc(r1.list, func(l proto.Launcher) bool { return l.Name == "stale-r1" }) {
			t.Fatalf("first refresh = %v, %v; want stale-r1", r1.list, r1.err)
		}

		cached, err := s.machineLaunchers(ctx, "server")
		if err != nil || !slices.ContainsFunc(cached, func(l proto.Launcher) bool { return l.Name == "fresh-r2" }) ||
			slices.ContainsFunc(cached, func(l proto.Launcher) bool { return l.Name == "stale-r1" }) {
			t.Fatalf("cached entry after out-of-order refresh = %v, %v; want fresh-r2", cached, err)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("total RPCs = %d, want 2 (third read must hit cache)", got)
		}
	})

	t.Run("bypassRead finishing during in-flight explicit refresh does not outrank explicit refresh", func(t *testing.T) {
		f := newFakeHub().withMachine()
		refreshStarted := make(chan struct{})
		releaseRefresh := make(chan struct{})
		var calls atomic.Int32
		h := &concurrentLauncherHub{
			fakeHub: f,
			onLaunchersList: func(_ context.Context, p DeviceCall) (json.RawMessage, error) {
				calls.Add(1)
				var a map[string]any
				_ = json.Unmarshal(p.Params, &a)
				if a["refresh"] == true {
					close(refreshStarted)
					<-releaseRefresh
					return json.Marshal(map[string]any{"launchers": []proto.Launcher{{Name: "fresh-refresh", Source: "zshrc"}}})
				}
				return json.Marshal(map[string]any{"launchers": []proto.Launcher{{Name: "stale-bypass", Source: "zshrc"}}})
			},
		}
		s := newTestService(t, f)
		s.hub = h

		ctx := context.Background()
		refreshDone := make(chan error, 1)
		go func() {
			_, err := s.machineLaunchers(withLauncherRefresh(ctx), "server")
			refreshDone <- err
		}()

		select {
		case <-refreshStarted:
		case <-time.After(5 * time.Second):
			close(releaseRefresh)
			t.Fatal("explicit refresh did not start")
		}

		// Concurrent bypassRead (e.g. zshrcLaunchers snapshot refresh) returns fast from node cache.
		if _, err := s.listLaunchersWith(ctx, Device{ID: "server", Online: true}, launcherListReadOpts{bypassRead: true}); err != nil {
			close(releaseRefresh)
			t.Fatalf("bypassRead err = %v", err)
		}

		close(releaseRefresh)
		if err := <-refreshDone; err != nil {
			t.Fatalf("explicit refresh err = %v", err)
		}

		cached, err := s.machineLaunchers(ctx, "server")
		if err != nil || !slices.ContainsFunc(cached, func(l proto.Launcher) bool { return l.Name == "fresh-refresh" }) ||
			slices.ContainsFunc(cached, func(l proto.Launcher) bool { return l.Name == "stale-bypass" }) {
			t.Fatalf("cached entry = %v, %v; want fresh-refresh", cached, err)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("total RPCs = %d, want 2 (third read must hit cache)", got)
		}
	})
}
