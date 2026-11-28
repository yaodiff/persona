package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// agents_switch (agents_switch.go, #6008): many agents to another launcher at once, each as its state allows.

func batchSwitch(ft *fleetTest, args map[string]any) (string, error) {
	raw, _ := json.Marshal(args)
	return ft.s.runTool(context.Background(), "server", "conv1", "agents_switch", raw)
}

// agentRow is a running Claude Code agent s on mac, on launcher, in status.
func agentRow(id, launcher, status string) proto.Session {
	state := proto.StateIdle
	if status == proto.StatusWorking {
		state = proto.StateWorking
	}
	return proto.Session{SessionID: id, MachineID: "mac", Cwd: "/code/" + id, Agent: "claude", Launcher: launcher, Status: status,
		State: state, UpdatedAt: time.Now()}
}

// callsFor are the node calls about session id, by method.
func callsFor(f *fakeHub, id string) []string {
	var out []string
	for _, c := range nodeCalls(f) {
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		if p["session_id"] == id {
			out = append(out, c.Method)
		}
	}
	return out
}

// waitCallsFor waits for n node calls about session id.
func waitCallsFor(t *testing.T, f *fakeHub, id string, n int) []string {
	t.Helper()
	var got []string
	waitFor(t, "the agent switch node calls", func() bool {
		got = callsFor(f, id)
		return len(got) >= n
	})
	return got
}

// One call: the agent its usage limit stopped is switched and continued, the idle one switched with nothing sent, the
// working one queued (nothing reaches its node), the unsupported ones refused; then the queued one is switched when
// its turn ends, with nothing sent.
func TestAgentsSwitchEachByItsState(t *testing.T) {
	ft, _ := switchRig(t)
	limited := limitTurn("s1limit001", time.Now().Add(2*time.Hour), false)
	limited.MachineID, limited.Cwd, limited.Agent, limited.Launcher = "mac", "/code/s1", "claude", "sub"
	codex := agentRow("s4codex001", "sub", proto.StatusIdle)
	codex.Agent = "codex"
	ft.sessions("mac", limited, agentRow("s2idle0001", "sub", proto.StatusIdle), agentRow("s3busy0001", "sub", proto.StatusWorking),
		codex, agentRow("s5there001", "work", proto.StatusIdle))

	var agents []map[string]any
	for _, id := range []string{"s1limit001", "s2idle0001", "s3busy0001", "s4codex001", "s5there001"} {
		agents = append(agents, map[string]any{"machine": "mac", "session_id": id})
	}
	preview, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": agents, "preview": true})
	if err != nil {
		t.Fatal(err)
	}
	if calls := nodeCalls(ft.f); len(calls) != 0 || !strings.Contains(preview, "will be queued") || ft.s.queuedSwitchOf("mac", "s3busy0001") != "" {
		t.Fatalf("the preview did something: %+v\n%s", calls, preview)
	}

	res, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": agents})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 switched_and_continued", "1 switched,", "1 queued", "2 refused", "runs codex", "runs on work already"} {
		if !strings.Contains(res, want) {
			t.Errorf("answer lacks %q:\n%s", want, res)
		}
	}
	if got := callsFor(ft.f, "s1limit001"); strings.Join(got, ",") != "session.modes,session.send" {
		t.Errorf("limited: %v", got)
	}
	for _, c := range nodeCalls(ft.f) {
		if p := callParams(t, c); c.Method == DeviceSessionSend {
			if p["switch_now"] != true || !strings.Contains(p["text"].(string), prompts.RenderAgentLimitContinue("")) {
				t.Errorf("continuation: %v", p)
			}
			requireAutoContinueSource(t, p)
		}
	}
	if got := callsFor(ft.f, "s2idle0001"); strings.Join(got, ",") != "session.modes,session.switch" {
		t.Errorf("idle: %v", got)
	}
	for _, id := range []string{"s3busy0001", "s4codex001", "s5there001"} {
		if got := callsFor(ft.f, id); len(got) != 0 {
			t.Errorf("%s reached its node: %v", id, got)
		}
	}
	if to := ft.s.queuedSwitchOf("mac", "s3busy0001"); to != "work" {
		t.Fatalf("queued: %q", to)
	}
	if list := ft.s.describeAgents(context.Background(), "all", "", ""); !strings.Contains(list, "switch_queued=work") {
		t.Errorf("agents_list:\n%s", list)
	}

	// Its turn ends: the switch goes, with nothing sent, and leaves the queue.
	ft.sessions("mac", agentRow("s3busy0001", "sub", proto.StatusIdle))
	if got := waitCallsFor(t, ft.f, "s3busy0001", 2); strings.Join(got, ",") != "session.modes,session.switch" {
		t.Fatalf("at the end of its turn: %v", got)
	}
	waitFor(t, "the queued agent switch to finish", func() bool { return ft.s.queuedSwitchOf("mac", "s3busy0001") == "" })
	if to := ft.s.queuedSwitchOf("mac", "s3busy0001"); to != "" {
		t.Errorf("still queued: %q", to)
	}
}

// The filter takes the running Claude Code agents on from_launcher; one the node finds busy after all is queued; a
// queued one the user moved to another launcher meanwhile is dropped with nothing done.
func TestAgentsSwitchFilterBusyAndMovedMeanwhile(t *testing.T) {
	ft, _ := switchRig(t)
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionSwitch: func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "a background command runs"}
		},
	}
	ft.sessions("mac", agentRow("s1dflt0001", "", proto.StatusIdle), agentRow("s2other001", "sub", proto.StatusIdle))
	// Finish the initial inventory event before creating the queued request. Otherwise
	// that older event can start a retry before the test reports the user's move.
	waitFleetOrder(t, ft.s)
	res, err := batchSwitch(ft, map[string]any{"launcher": "work", "from_launcher": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res, "s2other001") || !strings.Contains(res, "1 queued") {
		t.Fatalf("answer:\n%s", res)
	}
	if got := callsFor(ft.f, "s2other001"); len(got) != 0 {
		t.Errorf("the filter took one on another launcher: %v", got)
	}
	if to := ft.s.queuedSwitchOf("mac", "s1dflt0001"); to != "work" {
		t.Fatalf("not queued after switch_busy: %q", to)
	}
	n := len(callsFor(ft.f, "s1dflt0001"))
	moved := agentRow("s1dflt0001", "elsewhere", proto.StatusIdle)
	ft.sessions("mac", moved)
	waitFor(t, "the moved agent switch to leave the queue", func() bool { return ft.s.queuedSwitchOf("mac", "s1dflt0001") == "" })
	if to := ft.s.queuedSwitchOf("mac", "s1dflt0001"); to != "" {
		t.Fatalf("still queued after the user moved it: %q", to)
	}
	if got := callsFor(ft.f, "s1dflt0001"); len(got) != n {
		t.Errorf("moved meanwhile, switched anyway: %v", got)
	}
}

// Initial queue loading can overlap an inventory event. Both paths must read the
// loaded flag under the queue mutex, including the no-store early return.
func TestAgentsSwitchConcurrentLoadAndList(t *testing.T) {
	for range 256 {
		s := &Service{}
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			<-start
			s.loadSwitchQueue(context.Background())
		}()
		go func() {
			defer done.Done()
			<-start
			for range 8 {
				s.switchQueueListed("sample-machine")
			}
		}()
		close(start)
		done.Wait()
		s.switchQ.mu.Lock()
		ready := s.switchQ.loaded && s.switchQ.items != nil && s.switchQ.running != nil && s.switchQ.retry != nil
		s.switchQ.mu.Unlock()
		if !ready {
			t.Fatal("concurrent listing lost the initialized queue")
		}
	}
}

// A queued switch is kept in the database: a manager started anew carries it out when the turn ends, with the
// continuation when the turn ended on its usage limit.
func TestAgentsSwitchQueueSurvivesARestart(t *testing.T) {
	st := newFleetMem()
	ft, _ := switchRigOn(t, st)
	ft.sessions("mac", agentRow("s1busy0001", "sub", proto.StatusWorking))
	if _, err := batchSwitch(ft, map[string]any{"launcher": "work"}); err != nil {
		t.Fatal(err)
	}
	if ft.s.queuedSwitchOf("mac", "s1busy0001") != "work" {
		t.Fatal("not queued")
	}

	again, _ := switchRigOn(t, st)
	limited := limitTurn("s1busy0001", time.Now().Add(2*time.Hour), false)
	limited.MachineID, limited.Cwd, limited.Agent, limited.Launcher = "mac", "/code/s1", "claude", "sub"
	again.sessions("mac", limited)
	if got := waitCallsFor(t, again.f, "s1busy0001", 2); strings.Join(got, ",") != "session.modes,session.send" {
		t.Fatalf("after the restart: %v", got)
	}
	for _, c := range nodeCalls(again.f) {
		if c.Method == DeviceSessionSend {
			requireAutoContinueSource(t, callParams(t, c))
		}
	}
}

// stoppedRow is a Claude Code agent on mac on launcher that nothing runs now; persona: Persona started it.
func stoppedRow(id, launcher string, persona bool) proto.Session {
	c := agentRow(id, launcher, proto.StatusOffline)
	c.State, c.StartedByPersona = "", persona
	c.Capabilities = &proto.SessionCapabilities{}
	return c
}

// A stopped agent Persona started has its Resume pointed at the new launcher (session.switch with launcher and from,
// no pick, nothing sent): "switched (takes effect when resumed)". One Persona did not start is left out of the filter.
// A switch that fails on the node is reported refused with its reason, and not tried again.
func TestAgentsSwitchStoppedAndFailed(t *testing.T) {
	ft, _ := switchRig(t)
	ft.sessions("mac", stoppedRow("s1stop0001", "", true), stoppedRow("s2ext00001", "", false), agentRow("s3idle0001", "", proto.StatusIdle))
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionSwitch: func(_ string, raw json.RawMessage) (any, error) {
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			if p["session_id"] == "s3idle0001" {
				return nil, &rpc.Error{Code: proto.CodeSwitchFailed, Message: "the agent did not come up on launcher work (context deadline exceeded); it is back on claude"}
			}
			return nil, nil
		},
	}
	res, err := batchSwitch(ft, map[string]any{"launcher": "work", "from_launcher": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1 switched_on_resume", "takes effect when resumed", "1 refused", "did not come up on launcher work"} {
		if !strings.Contains(res, want) {
			t.Errorf("answer lacks %q:\n%s", want, res)
		}
	}
	if strings.Contains(res, "s2ext00001") {
		t.Errorf("took a stopped agent Persona did not start:\n%s", res)
	}
	if got := callsFor(ft.f, "s1stop0001"); strings.Join(got, ",") != "session.switch" {
		t.Errorf("stopped: %v", got)
	}
	for _, c := range nodeCalls(ft.f) {
		if p := callParams(t, c); p["session_id"] == "s1stop0001" && (p["launcher"] != "work" || p["from"] != "claude") {
			t.Errorf("stopped: %v", p)
		}
	}
	// The pick, the switch, and the pick taken back.
	n := len(callsFor(ft.f, "s3idle0001"))
	if got := strings.Join(callsFor(ft.f, "s3idle0001"), ","); got != "session.modes,session.switch,session.modes" || ft.s.queuedSwitchOf("mac", "s3idle0001") != "" {
		t.Fatalf("failed: %s, queued %q", got, ft.s.queuedSwitchOf("mac", "s3idle0001"))
	}
	ft.sessions("mac", agentRow("s3idle0001", "", proto.StatusIdle))
	observe := time.NewTicker(200 * time.Millisecond)
	<-observe.C
	observe.Stop()
	if got := callsFor(ft.f, "s3idle0001"); len(got) != n {
		t.Errorf("a failed switch was tried again: %v", got)
	}

	// Queued while busy, then stopped before its turn ended: its Resume is pointed there instead.
	ft.f.nodes = nil
	ft.sessions("mac", agentRow("s4busy0001", "", proto.StatusWorking))
	if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": "s4busy0001"}}}); err != nil {
		t.Fatal(err)
	}
	if ft.s.queuedSwitchOf("mac", "s4busy0001") != "work" {
		t.Fatal("not queued")
	}
	ft.sessions("mac", stoppedRow("s4busy0001", "", true))
	if got := waitCallsFor(t, ft.f, "s4busy0001", 1); strings.Join(got, ",") != "session.switch" {
		t.Fatalf("stopped while queued: %v", got)
	}
}

// The queue's loaded flag is read under its lock when a session list arrives while the queue is being loaded (#7303);
// the reader and the loader run together, so only a -race run tells it from a read outside the lock.
func TestSwitchQueueListedReadsLoadedUnderItsLock(t *testing.T) {
	for range 500 {
		s := &Service{} // no store: nothing to start, the flag is all there is
		loaded := make(chan struct{})
		go func() {
			s.loadSwitchQueue(context.Background())
			close(loaded)
		}()
		s.switchQueueListed("mac")
		<-loaded
	}
}

// agents_switch to the launcher an off_launcher agent was started on (#11792) switches it back there, while an agent
// that does run on that launcher is still refused as being on it already.
func TestAgentsSwitchBackOntoTheLauncherAnAgentRunsOff(t *testing.T) {
	ft, _ := switchRig(t)
	off := agentRow("s1off00001", "kimi", proto.StatusIdle)
	off.StartedByPersona, off.OffLauncher = true, true
	ft.sessions("mac", off, agentRow("s2there001", "kimi", proto.StatusIdle))
	res, err := batchSwitch(ft, map[string]any{"launcher": "kimi", "agents": []map[string]any{
		{"machine": "mac", "session_id": "s1off00001"}, {"machine": "mac", "session_id": "s2there001"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "1 switched,") || !strings.Contains(res, "1 refused") || !strings.Contains(res, "runs on kimi already") {
		t.Fatalf("answer:\n%s", res)
	}
	if got := callsFor(ft.f, "s1off00001"); strings.Join(got, ",") != "session.modes,session.switch" {
		t.Errorf("off its launcher: %v", got)
	}
	if got := callsFor(ft.f, "s2there001"); len(got) != 0 {
		t.Errorf("on it already, reached its node: %v", got)
	}

	// Working, it is queued, and switched back when its turn ends, not dropped as running there already.
	busy := agentRow("s3offbusy1", "kimi", proto.StatusWorking)
	busy.StartedByPersona, busy.OffLauncher = true, true
	ft.sessions("mac", busy)
	if res, err := batchSwitch(ft, map[string]any{"launcher": "kimi", "agents": []map[string]any{{"machine": "mac", "session_id": "s3offbusy1"}}}); err != nil || !strings.Contains(res, "1 queued") {
		t.Fatalf("busy, off its launcher: %v\n%s", err, res)
	}
	idle := busy
	idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
	ft.sessions("mac", idle)
	if got := waitCallsFor(t, ft.f, "s3offbusy1", 2); strings.Join(got, ",") != "session.modes,session.switch" {
		t.Fatalf("at the end of its turn: %v", got)
	}
}

func savedSwitchQueue(t *testing.T, st Store) map[string]queuedSwitch {
	t.Helper()
	var saved map[string]queuedSwitch
	if _, err := st.Setting(context.Background(), switchQueueKey, &saved); err != nil {
		t.Fatalf("read saved switch queue: %v", err)
	}
	return saved
}

func waitSwitchIdle(t *testing.T, s *Service, machine, id string) {
	t.Helper()
	k := switchKey(machine, id)
	waitFor(t, "in-flight queued switch to finish", func() bool {
		s.switchQ.mu.Lock()
		defer s.switchQ.mu.Unlock()
		return !s.switchQ.running[k]
	})
}

func pickedLaunchersFor(t *testing.T, f *fakeHub, id string) []string {
	t.Helper()
	var out []string
	for _, c := range nodeCalls(f) {
		if c.Method != DeviceSessionModes {
			continue
		}
		p := callParams(t, c)
		if p["session_id"] == id {
			if l, ok := p["launcher"].(string); ok {
				out = append(out, l)
			}
		}
	}
	return out
}

// holdFirstSwitch blocks the first session.switch call on ft.s.hub outside fakeHub.mu until release is called.
func holdFirstSwitch(t *testing.T, ft *fleetTest) (started <-chan struct{}, release func()) {
	t.Helper()
	chStarted := make(chan struct{})
	chRelease := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(chRelease) }) }
	t.Cleanup(release)
	var calls atomic.Int32
	inner := ft.s.hub
	ft.s.hub = &firstToolsTimingHub{
		Hub: inner,
		beforeCall: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
			if p.Method == DeviceSessionSwitch && calls.Add(1) == 1 {
				close(chStarted)
				<-chRelease
			}
			return inner.DeviceCall(ctx, p)
		},
	}
	return chStarted, release
}

type hookSwitchQueueStore struct {
	Store
	onPut func(key string, value any)
}

func (h *hookSwitchQueueStore) PutSetting(ctx context.Context, key string, value any) error {
	if h.onPut != nil && key == switchQueueKey {
		h.onPut(key, value)
	}
	return h.Store.PutSetting(ctx, key, value)
}

// A newer agents_switch queued while an earlier queued switch is executing moveAgent must not be deleted
// or stalled by the earlier switch's completion cleanup (#16110).
func TestRunQueuedSwitchPreservesNewerQueuedSwitch(t *testing.T) {
	t.Run("in_flight_failure_preserves_newer_switch", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		firstStarted, releaseFirst := holdFirstSwitch(t, ft)
		var switchCalls atomic.Int32
		ft.f.answerNode(DeviceSessionSwitch, func(string, json.RawMessage) (any, error) {
			if switchCalls.Add(1) == 1 {
				return nil, &rpc.Error{Code: proto.CodeSwitchFailed, Message: "switch failed; back on sub"}
			}
			return nil, nil
		})
		const id = "s1race0001"
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}
		if got := ft.s.queuedSwitchOf("mac", id); got != "work" {
			t.Fatalf("initial queued launcher = %q, want work", got)
		}

		// First queued switch starts executing moveAgent and blocks inside session.switch.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		<-firstStarted

		// While the first switch is in flight, queue a replacement switch to backup.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if res, err := batchSwitch(ft, map[string]any{"launcher": "backup", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil || !strings.Contains(res, "1 queued") {
			t.Fatalf("queue replacement switch: err=%v\n%s", err, res)
		}
		if got := ft.s.queuedSwitchOf("mac", id); got != "backup" {
			t.Fatalf("replacement queued launcher = %q, want backup", got)
		}

		// Finish the first switch with a non-busy failure (rolled back to sub).
		releaseFirst()
		waitSwitchIdle(t, ft.s, "mac", id)

		if got := ft.s.queuedSwitchOf("mac", id); got != "backup" {
			t.Fatalf("newer queued switch was dropped from memory after in-flight failure: got %q, want backup", got)
		}
		if got := savedSwitchQueue(t, st)[switchKey("mac", id)].Launcher; got != "backup" {
			t.Fatalf("newer queued switch was dropped from persisted store: got %q, want backup", got)
		}

		// On the next idle session list, the preserved backup switch runs and completes.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		waitFor(t, "replacement switch to complete", func() bool { return ft.s.queuedSwitchOf("mac", id) == "" })
		if got := pickedLaunchersFor(t, ft.f, id); !slices.Contains(got, "backup") {
			t.Fatalf("replacement switch never picked backup: %v", got)
		}
	})

	t.Run("in_flight_success_preserves_newer_switch", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		firstStarted, releaseFirst := holdFirstSwitch(t, ft)
		const id = "s1succ0001"
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}

		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		<-firstStarted

		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "backup", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}

		releaseFirst()
		waitSwitchIdle(t, ft.s, "mac", id)

		if got := ft.s.queuedSwitchOf("mac", id); got != "backup" {
			t.Fatalf("newer queued switch was dropped from memory after in-flight success: got %q, want backup", got)
		}
		if got := savedSwitchQueue(t, st)[switchKey("mac", id)].Launcher; got != "backup" {
			t.Fatalf("newer queued switch was dropped from persisted store: got %q, want backup", got)
		}
	})

	t.Run("in_flight_busy_does_not_delay_newer_switch", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		firstStarted, releaseFirst := holdFirstSwitch(t, ft)
		var switchCalls atomic.Int32
		ft.f.answerNode(DeviceSessionSwitch, func(string, json.RawMessage) (any, error) {
			if switchCalls.Add(1) == 1 {
				return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "busy"}
			}
			return nil, nil
		})
		const id = "s1busy0002"
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}

		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		<-firstStarted

		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "backup", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}

		releaseFirst()
		waitSwitchIdle(t, ft.s, "mac", id)

		ft.s.switchQ.mu.Lock()
		retryAt := ft.s.switchQ.retry[switchKey("mac", id)]
		ft.s.switchQ.mu.Unlock()
		if !retryAt.IsZero() {
			t.Fatalf("superseded busy switch set retry backoff %v for newer queued switch", retryAt)
		}

		// Replacement switch must execute immediately on the next idle list without a 30s retry delay.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		waitFor(t, "replacement switch to run without stale busy backoff", func() bool {
			return ft.s.queuedSwitchOf("mac", id) == ""
		})
		if got := pickedLaunchersFor(t, ft.f, id); !slices.Contains(got, "backup") {
			t.Fatalf("replacement switch never picked backup: %v", got)
		}
	})

	t.Run("requeue_after_busy_clears_stale_retry_backoff", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		var switchCalls atomic.Int32
		ft.f.answerNode(DeviceSessionSwitch, func(string, json.RawMessage) (any, error) {
			if switchCalls.Add(1) == 1 {
				return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "busy"}
			}
			return nil, nil
		})
		const id = "s1rtry0001"
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}

		// First attempt hits CodeSwitchBusy and records a 30s retry backoff.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		waitFor(t, "busy attempt to record retry backoff", func() bool {
			ft.s.switchQ.mu.Lock()
			defer ft.s.switchQ.mu.Unlock()
			k := switchKey("mac", id)
			return !ft.s.switchQ.running[k] && !ft.s.switchQ.retry[k].IsZero()
		})

		// Queueing a replacement switch clears the stale retry backoff so it runs on the next idle list.
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "backup", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}
		ft.s.switchQ.mu.Lock()
		retryAt := ft.s.switchQ.retry[switchKey("mac", id)]
		ft.s.switchQ.mu.Unlock()
		if !retryAt.IsZero() {
			t.Fatalf("queueSwitch left stale retry backoff %v", retryAt)
		}

		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		waitFor(t, "replacement switch to run after retry backoff cleared", func() bool {
			return ft.s.queuedSwitchOf("mac", id) == ""
		})
		if got := pickedLaunchersFor(t, ft.f, id); !slices.Contains(got, "backup") {
			t.Fatalf("replacement switch never picked backup: %v", got)
		}
	})

	t.Run("same_target_identical_timestamp_distinguished_by_seq", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		firstStarted, releaseFirst := holdFirstSwitch(t, ft)
		var switchCalls atomic.Int32
		ft.f.answerNode(DeviceSessionSwitch, func(string, json.RawMessage) (any, error) {
			if switchCalls.Add(1) == 1 {
				return nil, &rpc.Error{Code: proto.CodeSwitchFailed, Message: "switch failed; back on sub"}
			}
			return nil, nil
		})
		const id = "s1same0001"
		k := switchKey("mac", id)
		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}
		ft.s.switchQ.mu.Lock()
		firstAt := ft.s.switchQ.items[k].At
		ft.s.switchQ.mu.Unlock()

		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		<-firstStarted

		ft.sessions("mac", agentRow(id, "sub", proto.StatusWorking))
		waitFleetOrder(t, ft.s)
		if _, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": id}}}); err != nil {
			t.Fatal(err)
		}
		// Force identical timestamps to prove seq alone distinguishes the replacement entry.
		ft.s.switchQ.mu.Lock()
		w2 := ft.s.switchQ.items[k]
		w2.At = firstAt
		ft.s.switchQ.items[k] = w2
		ft.s.switchQ.mu.Unlock()

		releaseFirst()
		waitSwitchIdle(t, ft.s, "mac", id)

		if got := ft.s.queuedSwitchOf("mac", id); got != "work" {
			t.Fatalf("re-queued same-target switch was dropped after in-flight failure: got %q, want work", got)
		}
		if got := savedSwitchQueue(t, st)[k].Launcher; got != "work" {
			t.Fatalf("re-queued same-target switch was dropped from persisted store: got %q, want work", got)
		}
		ft.sessions("mac", agentRow(id, "sub", proto.StatusIdle))
		waitFor(t, "re-queued same-target switch to run", func() bool {
			return ft.s.queuedSwitchOf("mac", id) == ""
		})
		if got := switchCalls.Load(); got != 2 {
			t.Fatalf("session.switch calls = %d, want 2", got)
		}
	})

	t.Run("load_switch_queue_preserves_in_flight_running_state", func(t *testing.T) {
		st := newFleetMem()
		ft, _ := switchRigOn(t, st)
		const id = "s1load0001"
		k := switchKey("mac", id)
		retryUntil := time.Now().Add(time.Minute)
		ft.s.switchQ.mu.Lock()
		ft.s.switchQ.items = map[string]queuedSwitch{k: {Machine: "mac", Session: id, Launcher: "work", From: "sub", At: time.Now().UTC(), seq: 1}}
		ft.s.switchQ.running = map[string]bool{k: true}
		ft.s.switchQ.retry = map[string]time.Time{k: retryUntil}
		ft.s.switchQ.loaded = false
		ft.s.switchQ.mu.Unlock()

		ft.s.loadSwitchQueue(context.Background())

		ft.s.switchQ.mu.Lock()
		defer ft.s.switchQ.mu.Unlock()
		if !ft.s.switchQ.running[k] {
			t.Fatal("loadSwitchQueue wiped active q.running state")
		}
		if !ft.s.switchQ.retry[k].Equal(retryUntil) {
			t.Fatalf("loadSwitchQueue wiped active q.retry state: got %v, want %v", ft.s.switchQ.retry[k], retryUntil)
		}
	})

	t.Run("save_switch_queue_serializes_concurrent_writes", func(t *testing.T) {
		base := newFleetMem()
		putEntered := make(chan struct{})
		releaseFirstPut := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(releaseFirstPut) }) }
		t.Cleanup(release)
		var puts atomic.Int32
		st := &hookSwitchQueueStore{
			Store: base,
			onPut: func(string, any) {
				if puts.Add(1) == 1 {
					close(putEntered)
					<-releaseFirstPut
				}
			},
		}
		ft, _ := switchRigOn(t, st)
		const id = "s1save0001"
		k := switchKey("mac", id)
		ft.s.switchQ.mu.Lock()
		ft.s.switchQ.loaded = true
		ft.s.switchQ.items = map[string]queuedSwitch{}
		ft.s.switchQ.running = map[string]bool{}
		ft.s.switchQ.retry = map[string]time.Time{}
		ft.s.switchQ.mu.Unlock()

		// Start a slow saveSwitchQueue of the empty queue.
		doneFirst := make(chan struct{})
		go func() {
			defer close(doneFirst)
			ft.s.saveSwitchQueue(context.Background())
		}()
		<-putEntered

		// Concurrently queue a new switch and save it while the first PutSetting is still in flight.
		doneSecond := make(chan struct{})
		go func() {
			defer close(doneSecond)
			ft.s.queueSwitch(context.Background(), &switchItem{
				m: Device{ID: "mac", Name: "Mac"},
				c: agentRow(id, "sub", proto.StatusWorking),
			}, switchTarget{Launcher: "backup"}, "conv1")
		}()
		// Wait until queueSwitch has inserted the item into memory before releasing the first PutSetting.
		waitFor(t, "concurrent queueSwitch to populate memory", func() bool {
			return ft.s.queuedSwitchOf("mac", id) == "backup"
		})
		release()
		<-doneFirst
		<-doneSecond

		if got := savedSwitchQueue(t, base)[k].Launcher; got != "backup" {
			t.Fatalf("stale concurrent saveSwitchQueue overwrote persisted switch: got %q, want backup", got)
		}
	})
}
