package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// An Agent another Agent starts (a2a_send's new Agent, dispatch_start) opens where agent_start's would: an agy launcher
// on the computer's Antigravity companion where that runs it; a read-only start its launcher cannot hold there is
// refused before anything is recorded; and a start that did not happen says why (#14887, start_host.go).

// startedHostFixture is memA2AFixture (an Agent on mac, A2A on its default scope) with Agent dispatch on, answers in
// English and the starts answered with the session id the launch reserved. Every computer lists launchers.
func startedHostFixture(t *testing.T, launchers []proto.Launcher) (*Service, *a2aTestHub, *memA2AStore, a2aCaller) {
	t.Helper()
	s, h, st, parent, _ := memA2AFixture(t)
	h.fakeModel(t)
	on := true
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hello.Locale = string(i18n.En)
	h.hello.Agents.Dispatch.Enabled = &on
	h.launcherList = launchers
	h.startReply = func(args map[string]any, row *proto.Session) *proto.StartInstead {
		if id, _ := args["session_id"].(string); id != "" {
			row.SessionID = id
		}
		return nil
	}
	return s, h, st, parent
}

// startedCall is the Agent's call of one of its tools.
func startedCall(ctx context.Context, s *Service, parent a2aCaller, tool, args string) proto.ToolResult {
	return s.mcpCall(ctx, parent.Ref.Machine, parent.Ref.Session, tool, json.RawMessage(args), false)
}

// startedLaunch makes a new a2a_send launch with args (in ctx, which may carry the caller's attestation) and runs it
// until its start has an outcome: the task as it ended up.
func startedLaunch(t *testing.T, ctx context.Context, s *Service, st *memA2AStore, parent a2aCaller, args string) *a2aTask {
	t.Helper()
	res := startedCall(ctx, s, parent, "a2a_send", args)
	var view a2aTaskView
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &view) != nil || view.ID == "" {
		t.Fatalf("a2a_send: %s", resultText(res))
	}
	return startedOutcome(t, s, st, view.ID)
}

// startedOutcome runs launch id once through the delivery loop and waits for what its start came to: started or failed
// (no longer pending), or in doubt.
func startedOutcome(t *testing.T, s *Service, st *memA2AStore, id string) *a2aTask {
	t.Helper()
	ctx := t.Context()
	processMemA2A(t, s, st, s.a2aConfig(ctx), id)
	var task *a2aTask
	waitFor(t, "the launch's outcome", func() bool {
		var err error
		task, err = decodeA2A(st.A2AGet(ctx, id))
		return err == nil && task != nil && (!task.Launch.Pending || task.Events[0].Delivery == a2aUncertain)
	})
	return task
}

// startedHosts is the host each session.start named, "" for none.
func startedHosts(f *fakeHub) []string {
	var hosts []string
	for _, start := range startsOf(f) {
		host, _ := start["host"].(string)
		hosts = append(hosts, host)
	}
	return hosts
}

// failedWith is the text of task's failed status, "" while it has none.
func failedWith(task *a2aTask) string {
	for i := len(task.Events) - 1; i >= 0; i-- {
		if e := task.Events[i]; e.Kind == "status" && e.State == a2aFailed {
			return e.Text
		}
	}
	return ""
}

// An agy launch starts on the computer's Antigravity companion where that runs its launcher, in plan, which the
// companion holds read-only, as agent_start's and a read-only task's starts do. Another launcher names no host.
func TestA2ALaunchStartsAgyOnTheCompanion(t *testing.T) {
	s, h, st, parent := startedHostFixture(t, companionLaunchers())
	ctx := t.Context()
	task := startedLaunch(t, ctx, s, st, parent,
		`{"machine_id":"box","cwd":"/code/app","launcher":"agy","message_id":"m-agy","text":"Review the change."}`)
	starts := startsOf(h.fakeHub)
	if task.Launch.Pending || failedWith(task) != "" || len(starts) != 1 || starts[0]["host"] != proto.HostAgy ||
		starts[0]["permission_mode"] != "plan" {
		t.Fatalf("agy launch = %+v; starts %v", task, starts)
	}
	startedLaunch(t, ctx, s, st, parent,
		`{"machine_id":"box","cwd":"/code/app","launcher":"claude","message_id":"m-claude","text":"Review the change."}`)
	if starts = startsOf(h.fakeHub); len(starts) != 2 || !unset(starts[1]["host"]) || starts[1]["permission_mode"] != "plan" {
		t.Fatalf("a claude launch named a host: %v", starts)
	}
}

// A launch in plan whose launcher has no read-only mode where it would start (agy in a pane while no companion runs
// it, pi) is refused when a2a_send is called, naming the launchers that can start it read-only: no task is recorded
// and nothing starts. A launch its caller is attested to start writable goes on, in the pane.
func TestA2ALaunchRefusesAReadOnlyStartNoPaneHolds(t *testing.T) {
	s, h, st, parent := startedHostFixture(t, append(paneLaunchers(), proto.Launcher{Name: "pi", Agent: "pi"}))
	ctx := t.Context()
	for _, launcher := range []string{"agy", "pi"} {
		id := "m-" + launcher
		res := startedCall(ctx, s, parent, "a2a_send",
			`{"machine_id":"box","cwd":"/code/app","launcher":"`+launcher+`","message_id":"`+id+`","text":"Review the change."}`)
		if text := resultText(res); !res.IsError || !strings.HasPrefix(text, launcher+" on box starts ") ||
			!strings.Contains(text, "no read-only mode") || !strings.HasSuffix(text, "launchers: claude") {
			t.Fatalf("read-only %s launch: %s", launcher, text)
		}
		if row, err := st.A2ARequest(ctx, parent.Owner, id); err != nil || row != nil {
			t.Fatalf("a refused %s launch recorded a task: %+v %v", launcher, row, err)
		}
	}
	if starts := startsOf(h.fakeHub); len(starts) != 0 {
		t.Fatalf("a refused launch started: %v", starts)
	}

	raw := `{"machine_id":"box","cwd":"/code/app","launcher":"agy","permission_mode":"bypassPermissions","message_id":"m-writable","text":"Fix it."}`
	attested := devicewire.WithSourcePermission(ctx, testSourcePermission(parent.Ref.Session, "a2a_send", json.RawMessage(raw), "bypass"))
	task := startedLaunch(t, attested, s, st, parent, raw)
	starts := startsOf(h.fakeHub)
	if task.Launch.Pending || failedWith(task) != "" || len(starts) != 1 || !unset(starts[0]["host"]) ||
		starts[0]["permission_mode"] != "bypassPermissions" {
		t.Fatalf("writable agy launch = %+v; starts %v", task, starts)
	}
}

// A start that did not happen says why on its failed task: the node's refusal, or, when the companion picked for a
// read-only launch went away before the start reached it, the node's reason for that, since no pane holds the start
// read-only (a writable one starts in the default host instead), or the error its launcher stopped with. Only a start
// whose outcome stays unknown says that Persona could not confirm it.
func TestA2ALaunchSaysWhyItDidNotStart(t *testing.T) {
	launch := func(id string) string {
		return `{"machine_id":"box","cwd":"/code/app","launcher":"agy","message_id":"` + id + `","text":"Review the change."}`
	}
	t.Run("refused by the node", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		h.mu.Lock()
		h.failStart = &rpc.Error{Code: "launcher_kind_missing", Message: "agy is not installed on this computer, so nothing was started.",
			Data: json.RawMessage(`{"delivered":"none"}`)}
		h.mu.Unlock()
		task := startedLaunch(t, t.Context(), s, st, parent, launch("m-refused"))
		if got := failedWith(task); got != "The Agent did not start: agy is not installed on this computer, so nothing was started." {
			t.Fatalf("failed status = %q; task %+v", got, task)
		}
	})
	t.Run("companion went away, read-only", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		h.mu.Lock()
		h.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
		h.mu.Unlock()
		task := startedLaunch(t, t.Context(), s, st, parent, launch("m-away"))
		if got := failedWith(task); got != "The Agent did not start: "+companionAwayErr.Message {
			t.Fatalf("failed status = %q; task %+v", got, task)
		}
		if hosts := startedHosts(h.fakeHub); !slices.Equal(hosts, []string{proto.HostAgy}) {
			t.Fatalf("a read-only start went on to a pane: hosts %q", hosts)
		}
	})
	t.Run("companion went away, writable", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		h.mu.Lock()
		h.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
		h.mu.Unlock()
		raw := `{"machine_id":"box","cwd":"/code/app","launcher":"agy","permission_mode":"bypassPermissions","message_id":"m-writable","text":"Fix it."}`
		attested := devicewire.WithSourcePermission(t.Context(), testSourcePermission(parent.Ref.Session, "a2a_send", json.RawMessage(raw), "bypass"))
		task := startedLaunch(t, attested, s, st, parent, raw)
		if hosts := startedHosts(h.fakeHub); task.Launch.Pending || failedWith(task) != "" || !slices.Equal(hosts, []string{proto.HostAgy, ""}) {
			t.Fatalf("writable launch = %+v; hosts %q", task, hosts)
		}
	})
	t.Run("the launcher stopped", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		h.mu.Lock()
		h.failStart = rpc.ErrClosed // the answer is lost: the start is in doubt
		h.mu.Unlock()
		task := startedLaunch(t, t.Context(), s, st, parent, launch("m-stopped"))
		if !task.Launch.Pending || task.Events[0].Delivery != a2aUncertain {
			t.Fatalf("a lost answer = %+v", task)
		}
		h.mu.Lock()
		h.sessions["box"] = append(h.sessions["box"], proto.Session{SessionID: task.TargetSession, Status: proto.StatusIdle,
			StartError: "agy exited: sign in to Antigravity first"})
		h.mu.Unlock()
		task = startedOutcome(t, s, st, task.ID)
		if got := failedWith(task); got != "The Agent did not start: agy exited: sign in to Antigravity first" {
			t.Fatalf("failed status = %q; task %+v", got, task)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		h.mu.Lock()
		h.failStart = rpc.ErrClosed
		h.mu.Unlock()
		task := startedLaunch(t, t.Context(), s, st, parent, launch("m-unknown"))
		task.Events[0].Attempted = task.Events[0].Attempted.Add(-agentStartTimeout - 2*time.Minute) // no session since
		if err := s.a2aSave(t.Context(), st, task); err != nil {
			t.Fatal(err)
		}
		task = startedOutcome(t, s, st, task.ID)
		if got := failedWith(task); got != "Persona could not confirm the Agent started." {
			t.Fatalf("failed status = %q; task %+v", got, task)
		}
	})
}

// A retry by message_id is answered as before and not checked again: a launch accepted while the companion ran its
// launcher still finds its task once the companion is gone, and so does a dispatch. A refused one is refused again
// and records nothing.
func TestAgentStartedRetryIsAnsweredAsBefore(t *testing.T) {
	s, h, st, parent := startedHostFixture(t, companionLaunchers())
	ctx := t.Context()
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.agy", PermissionMode: "plan"}) // dispatch_start's plan
	launch := `{"machine_id":"box","cwd":"/code/app","launcher":"agy","message_id":"m-retry","text":"Review the change."}`
	first := startedLaunch(t, ctx, s, st, parent, launch)
	dispatch := `{"machine":"box","cwd":"/code/app","prompt":"Review the change.","message_id":"d-retry"}`
	if res := startedCall(ctx, s, parent, "dispatch_start", dispatch); res.IsError {
		t.Fatalf("dispatch_start: %s", resultText(res))
	}
	if hosts := startedHosts(h.fakeHub); !slices.Equal(hosts, []string{proto.HostAgy, proto.HostAgy}) {
		t.Fatalf("hosts %q", hosts)
	}
	h.mu.Lock()
	h.launcherList = paneLaunchers()
	h.mu.Unlock()
	s.invalidateLauncherListCache("box") // launchers.list is cached for a minute (launcher_list_cache.go)
	var again a2aTaskView
	res := startedCall(ctx, s, parent, "a2a_send", launch)
	if res.IsError || json.Unmarshal([]byte(resultText(res)), &again) != nil || again.ID != first.ID {
		t.Fatalf("launch retry: %s", resultText(res))
	}
	if res := startedCall(ctx, s, parent, "dispatch_start", dispatch); res.IsError || !strings.Contains(resultText(res), "Task ") {
		t.Fatalf("dispatch retry: %s", resultText(res))
	}
	refused := `{"machine_id":"box","cwd":"/code/app","launcher":"agy","message_id":"m-refused","text":"Review the change."}`
	for range 2 {
		if res := startedCall(ctx, s, parent, "a2a_send", refused); !res.IsError || !strings.Contains(resultText(res), "no read-only mode") {
			t.Fatalf("refused launch: %s", resultText(res))
		}
	}
	if row, err := st.A2ARequest(ctx, parent.Owner, "m-refused"); err != nil || row != nil {
		t.Fatalf("a refused launch recorded a task: %+v %v", row, err)
	}
	if n := len(startsOf(h.fakeHub)); n != 2 {
		t.Fatalf("%d starts, want the first two only", n)
	}
}

// dispatch_start takes the same host: an agy launcher starts on the companion where that runs it, in dispatch_start's
// own mode (Allow all for agy, dispatchAllowAll), in a pane while no companion runs it, and in the default host when
// the companion went away. A read-only start (Model configuration › Agents in plan) that its launcher cannot hold is
// refused before its task is recorded, also without the A2A store; one whose companion went away fails with the
// node's reason instead of going to a pane.
func TestDispatchStartTakesTheAgentStartHost(t *testing.T) {
	for _, tc := range []struct {
		name      string
		launchers []proto.Launcher
		away      bool
		want      []string
	}{
		{"companion connected", companionLaunchers(), false, []string{proto.HostAgy}},
		{"no companion", paneLaunchers(), false, []string{""}},
		{"companion went away", companionLaunchers(), true, []string{proto.HostAgy, ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h, st, parent := startedHostFixture(t, tc.launchers)
			ctx := t.Context()
			if tc.away {
				h.mu.Lock()
				h.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
				h.mu.Unlock()
			}
			res := startedCall(ctx, s, parent, "dispatch_start",
				`{"machine":"box","cwd":"/code/app","prompt":"Fix the build.","launcher":"agy","message_id":"d-agy"}`)
			if res.IsError {
				t.Fatalf("dispatch_start: %s", resultText(res))
			}
			if hosts := startedHosts(h.fakeHub); !slices.Equal(hosts, tc.want) {
				t.Fatalf("hosts %q, want %q", hosts, tc.want)
			}
			for _, start := range startsOf(h.fakeHub) {
				if start["permission_mode"] != "bypassPermissions" {
					t.Fatalf("dispatch_start lost its own mode: %v", start)
				}
			}
			if task, err := decodeA2A(st.A2ARequest(ctx, parent.Owner, "d-agy")); err != nil || task == nil || task.Launch.Pending || failedWith(task) != "" {
				t.Fatalf("dispatch task = %+v, %v", task, err)
			}
		})
	}
	t.Run("read-only", func(t *testing.T) {
		s, h, st, parent := startedHostFixture(t, companionLaunchers())
		ctx := t.Context()
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.agy", PermissionMode: "plan"})
		if res := startedCall(ctx, s, parent, "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review the change.","message_id":"d-plan"}`); res.IsError {
			t.Fatalf("dispatch_start in plan: %s", resultText(res))
		}
		if starts := startsOf(h.fakeHub); len(starts) != 1 || starts[0]["host"] != proto.HostAgy || starts[0]["permission_mode"] != "plan" {
			t.Fatalf("read-only dispatch: %v", starts)
		}
		h.mu.Lock()
		h.launcherList = paneLaunchers()
		h.mu.Unlock()
		s.invalidateLauncherListCache("box") // launchers.list is cached for a minute (launcher_list_cache.go)
		res := startedCall(ctx, s, parent, "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review the change.","message_id":"d-pane"}`)
		if text := resultText(res); !res.IsError || !strings.HasPrefix(text, "agy on box starts ") || !strings.HasSuffix(text, "launchers: claude") {
			t.Fatalf("read-only dispatch to a pane: %s", text)
		}
		if row, err := st.A2ARequest(ctx, parent.Owner, "d-pane"); err != nil || row != nil {
			t.Fatalf("a refused dispatch recorded a task: %+v %v", row, err)
		}
		h.mu.Lock()
		h.launcherList = companionLaunchers()
		h.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
		h.mu.Unlock()
		s.invalidateLauncherListCache("box")
		res = startedCall(ctx, s, parent, "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review the change.","message_id":"d-away"}`)
		if !res.IsError || !strings.Contains(resultText(res), companionAwayErr.Message) {
			t.Fatalf("read-only dispatch whose companion went away: %s", resultText(res))
		}
		task, err := decodeA2A(st.A2ARequest(ctx, parent.Owner, "d-away"))
		if err != nil || task == nil || failedWith(task) != "The Agent did not start: "+companionAwayErr.Message {
			t.Fatalf("dispatch task = %+v, %v", task, err)
		}
		if hosts := startedHosts(h.fakeHub); !slices.Equal(hosts, []string{proto.HostAgy, proto.HostAgy}) {
			t.Fatalf("a read-only dispatch went on to a pane: hosts %q", hosts)
		}
	})
	t.Run("read-only without the A2A store", func(t *testing.T) {
		s, f, _ := newDispatchService(t, true)
		f.hello.Locale = string(i18n.En)
		f.launcherList = paneLaunchers()
		setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.agy", PermissionMode: "plan"})
		r := dispatchCall(s, "mac", "caller-0001", "dispatch_start", `{"machine":"box","cwd":"/code/app","prompt":"Review the change."}`)
		if !r.IsError || !strings.Contains(resultText(r), "no read-only mode") || len(f.starts) != 0 || len(s.dispatched(context.Background())) != 0 {
			t.Fatalf("legacy read-only dispatch: %q; starts %v", resultText(r), f.starts)
		}
	})
}
