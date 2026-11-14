package assistant

import (
	"context"
	"encoding/json"
	"strings"
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
		if p := callParams(t, c); c.Method == DeviceSessionSend && (p["switch_now"] != true || !strings.Contains(p["text"].(string), prompts.RenderAgentLimitContinue())) {
			t.Errorf("continuation: %v", p)
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
	ft.sessions("mac", agentRow("s1dflt0001", "", proto.StatusIdle), agentRow("s2other001", "sub", proto.StatusIdle))
	ft.f.nodes = map[string]nodeAnswer{
		DeviceSessionSwitch: func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeSwitchBusy, Message: "a background command runs"}
		},
	}
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
