package node

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A work agent (pi, the Cursor CLI, the Antigravity CLI) starts in a herdr pane with the model the start names, and
// herdr is told which agent it is. The first message is not an argument of the command. pi also gets --approve: nobody is
// at the pane to answer its project Trust prompt (#9462).
func TestWorkAgentStartTypesTheModel(t *testing.T) {
	for _, tc := range []struct{ kind, program, model, mode, want string }{
		{piAgent, "pi", "ag-chat/cursor/grok-4.7", "", "HERDR_AGENT=pi pi --model ag-chat/cursor/grok-4.7 --approve"},
		{cursorAgent, "cursor-agent", "cursor/grok-4.7", "", "HERDR_AGENT=cursor cursor-agent --model cursor/grok-4.7"},
		{cursorAgent, "cursor-agent", "", proto.ModeBypass, "HERDR_AGENT=cursor cursor-agent"}, // cursor takes no mode
		// agy runs in Allow all unless the start names another mode, in agy's own flags.
		{agyAgent, "agy", "", "", "HERDR_AGENT=agy agy --dangerously-skip-permissions"},
		{agyAgent, "agy", "", proto.ModeBypass, "HERDR_AGENT=agy agy --dangerously-skip-permissions"},
		{agyAgent, "agy", "", "acceptEdits", "HERDR_AGENT=agy agy --mode accept-edits"},
		{agyAgent, "agy", "", "auto", "HERDR_AGENT=agy agy --mode auto"},
		{agyAgent, "agy", "", "default", "HERDR_AGENT=agy agy"},
		{piAgent, "pi", "", "", "HERDR_AGENT=pi pi --approve"},
	} {
		t.Run(tc.kind+" "+tc.model+" "+tc.mode, func(t *testing.T) {
			n, f, home := herdrNode(t)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.after(func(c herdrCall) {
				// newTab marks the pane with the persona token first; stop on the command that runs the agent.
				if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=") {
					cancel()
				}
			})
			_, _ = n.startWorkAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), home, tc.kind, tc.kind, tc.model, "", tc.mode, "do the thing", "w1", "")
			want := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"}, "text": inAgentEnv(n.dir, tc.want, shellQuote)})
			var got []string
			for _, c := range f.trace("pane.send_input") {
				if strings.Contains(c, "HERDR_AGENT=") {
					got = append(got, c)
				}
			}
			if len(got) != 1 || got[0] != want {
				t.Errorf("typed %v, want %s", got, want)
			}
		})
	}
}

// pi trusts the directory the start chose for that run only, on the first start and when herdr resumes it, whatever
// launcher runs it; cursor-agent and agy get no pi flag (#9462).
func TestWorkAgentCommandApprovesPiProjectFiles(t *testing.T) {
	for _, tc := range []struct{ launcher, program, model, want string }{
		{"pi", "pi", "", "pi --approve"},
		{"pi", "pi", "ag-chat/cursor/grok-4.7", "pi --model ag-chat/cursor/grok-4.7 --approve"},
		{"my-pi", "pi", "", "my-pi --approve"},
		{"cursor", "cursor-agent", "cursor/grok-4.7", "cursor-agent --model cursor/grok-4.7"},
		{"agy", "agy", "", "agy"},
	} {
		if got := workAgentCommand(tc.launcher, tc.program, tc.model, "", ""); got != tc.want {
			t.Errorf("workAgentCommand(%q, %q, %q) = %q, want %q", tc.launcher, tc.program, tc.model, got, tc.want)
		}
	}
	if got, want := workAgentResume(piAgent, workAgentCommand("pi", "pi", "", "", ""), "/s/a.jsonl"), `pi --approve --session /s/a.jsonl`; got != want {
		t.Errorf("resume = %q, want %q", got, want)
	}
}

func newWorkAgentTestTab(t *testing.T) (n *Node, f *fakeHerdr, home string, created *host.Created) {
	t.Helper()
	n, f, home = herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	created, err := n.newTab(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, "w1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return n, f, home, created
}

// Abandoning after the start's context was cancelled still quits the work agent under its pane-derived session id,
// without closing the terminal it leaves for the user.
func TestAbandonStartAfterCancelQuitsAgentAndKeepsTheTab(t *testing.T) {
	for _, kind := range []string{piAgent, cursorAgent, agyAgent} {
		t.Run(kind, func(t *testing.T) {
			n, f, home, created := newWorkAgentTestTab(t)
			id := otherSession(host.Agent{Agent: kind, PaneID: "w1:p2"})
			running := agentOf(kind, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, kind)
			f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
			quitsOn(f, "w1:p2", id, nil, shellPane("w1:p2", "w1:t2", "w1", home, id))
			withStop(t, 1, time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := n.abandonStart(ctx, created, kind, ctx.Err()); err != context.Canceled {
				t.Fatalf("abandonStart: %v", err)
			}
			assertTrace(t, f.trace("pane.send_keys"), []string{
				sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
			})
			closesNothing(t, f)
		})
	}
}

func TestAbandonStartWaitsForLateAgent(t *testing.T) {
	for _, kind := range []string{piAgent, cursorAgent, agyAgent} {
		t.Run(kind, func(t *testing.T) {
			n, f, home, created := newWorkAgentTestTab(t)
			id := otherSession(host.Agent{Agent: kind, PaneID: "w1:p2"})
			running := agentOf(kind, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, kind)
			refreshes := 0
			f.handle("session.snapshot", func(map[string]any) any {
				refreshes++
				if refreshes == 1 {
					return snapshot(nil, nil, nil, nil)
				}
				return snapshot(nil, nil, []any{running}, nil)
			})
			quitsOn(f, "w1:p2", id, nil, shellPane("w1:p2", "w1:t2", "w1", home, id))
			withStop(t, 1, time.Millisecond)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := n.abandonStart(ctx, created, kind, ctx.Err()); err != context.Canceled {
				t.Fatalf("abandonStart: %v", err)
			}
			if refreshes < 2 {
				t.Fatalf("only %d refresh before the agent appeared", refreshes)
			}
			assertTrace(t, f.trace("pane.send_keys"), []string{
				sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
			})
			closesNothing(t, f)
		})
	}
}

func TestReportedWorkAgentRejectsDuplicateRawRows(t *testing.T) {
	n, _, _ := herdrNode(t)
	first := host.Agent{Agent: cursorAgent, PaneID: "w1:p2", AgentStatus: proto.StatusIdle}
	second := host.Agent{Agent: cursorAgent, PaneID: "w1:p2", AgentStatus: proto.StatusIdle}
	n.mu.Lock()
	n.hostAgents = map[string][]host.Agent{host.Herdr: {first, second}}
	n.mu.Unlock()
	if id, ambiguous := n.reportedWorkAgent("w1:p2", cursorAgent); !ambiguous || id != "" {
		t.Fatalf("duplicate raw rows were treated as one agent: id %q ambiguous %v", id, ambiguous)
	}
}

func TestAbandonStartFollowsPiSessionIDChange(t *testing.T) {
	n, f, home, created := newWorkAgentTestTab(t)
	fallback := agentOf(piAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, piAgent)
	const realID = "pi-real-session"
	running := agentOf(piAgent, realID, proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, piAgent)
	f.reply("session.snapshot", snapshot(nil, nil, []any{fallback}, nil))
	presses := 0
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_keys" || str(c.Params["pane_id"]) != "w1:p2" {
			return
		}
		presses++
		switch presses {
		case 2:
			// The fallback row disappears, but the same pi is still live under its real session ID.
			f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
		case 4:
			f.reply("session.snapshot", snapshot(nil, nil, nil, []any{shellPane("w1:p2", "w1:t2", "w1", home, realID)}))
		}
	})
	withStop(t, 1, time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.abandonStart(ctx, created, piAgent, ctx.Err()); err != context.Canceled {
		t.Fatalf("abandonStart: %v", err)
	}
	assertTrace(t, f.trace("pane.send_keys"), []string{
		sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
		sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
	})
	closesNothing(t, f)
}

func TestWorkAgentStartHoldsPaneLockThroughCleanup(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cleaning := false
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	f.handle("session.snapshot", func(map[string]any) any {
		if cleaning {
			close(cleanupStarted)
			cleaning = false
			<-releaseCleanup
		}
		return snapshot(nil, nil, nil, nil)
	})
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=cursor") {
			cleaning = true
			cancel()
		}
	})
	startDone := make(chan error, 1)
	go func() {
		_, err := n.startWorkAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), home, cursorAgent, cursorAgent, "", "", "", "do the thing", "w1", "")
		startDone <- err
	}()
	select {
	case <-cleanupStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("start did not reach abandoned cleanup")
	}
	lockTaken := make(chan struct{})
	go func() {
		unlock := n.typing.lock(workAgentPaneLockPrefix + "w1:p2")
		close(lockTaken)
		unlock()
	}()
	select {
	case <-lockTaken:
		t.Fatal("another start took the pane lock during cleanup")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCleanup)
	select {
	case err := <-startDone:
		if err == nil {
			t.Fatal("cancelled start unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned start did not return")
	}
	select {
	case <-lockTaken:
	case <-time.After(time.Second):
		t.Fatal("pane lock was not released after cleanup")
	}
	closesNothing(t, f)
}

func TestAbandonStartStopsKeysWhenRefreshFails(t *testing.T) {
	n, f, home, created := newWorkAgentTestTab(t)
	running := agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, cursorAgent)
	f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
	presses := 0
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_keys" || str(c.Params["pane_id"]) != "w1:p2" {
			return
		}
		presses++
		if presses == 2 {
			f.handle("session.snapshot", func(map[string]any) any {
				return herdrFail{Code: "unavailable", Message: "host went away"}
			})
			f.handle("agent.list", func(map[string]any) any {
				return herdrFail{Code: "unavailable", Message: "host went away"}
			})
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.abandonStart(ctx, created, cursorAgent, ctx.Err()); err != context.Canceled {
		t.Fatalf("abandonStart: %v", err)
	}
	assertTrace(t, f.trace("pane.send_keys"), []string{
		sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
	})
	closesNothing(t, f)
}

func TestAbandonStartWithoutReportedAgentSendsNoKeys(t *testing.T) {
	n, f, _, created := newWorkAgentTestTab(t)
	old := abandonStartWait
	abandonStartWait = 20 * time.Millisecond
	t.Cleanup(func() { abandonStartWait = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.abandonStart(ctx, created, cursorAgent, ctx.Err()); err != context.Canceled {
		t.Fatalf("abandonStart: %v", err)
	}
	if got := f.trace("pane.send_keys"); len(got) != 0 {
		t.Errorf("sent unverified keys: %v", got)
	}
	closesNothing(t, f)
}

func TestAbandonStartLeavesAdoptedPaneUntouched(t *testing.T) {
	n, f, home, created := newWorkAgentTestTab(t)
	running := agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, cursorAgent)
	f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
	f.reset()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	adoptedCtx := withAdoptedPane(ctx, created, []string{"-zsh"})
	if err := n.abandonStart(adoptedCtx, created, cursorAgent, ctx.Err()); err != context.Canceled {
		t.Fatalf("abandonStart: %v", err)
	}
	if got := f.trace("session.snapshot", "pane.send_keys"); len(got) != 0 {
		t.Errorf("touched the adopted pane: %v", got)
	}
	closesNothing(t, f)
}

func TestWorkAgentLaunchRunErrorKeepsTabAndQuitsAgent(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	id := otherSession(host.Agent{Agent: cursorAgent, PaneID: "w1:p2"})
	running := agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, cursorAgent)
	f.handle("pane.send_input", func(map[string]any) any {
		return herdrFail{Code: "unavailable", Message: "response lost"}
	})
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=cursor") {
			f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
		}
	})
	quitsOn(f, "w1:p2", id, nil, shellPane("w1:p2", "w1:t2", "w1", home, id))
	withStop(t, 1, time.Millisecond)
	_, err := n.startWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, cursorAgent, cursorAgent, "", "", "", "do the thing", "w1", "")
	if err == nil || !strings.Contains(err.Error(), "run launcher") {
		t.Fatalf("start: %v", err)
	}
	assertTrace(t, f.trace("pane.send_keys"), []string{
		sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
	})
	closesNothing(t, f)
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Errorf("delivered the first prompt: %v", got)
	}
}

func TestWorkAgentStartRefusesWhenPaneHostRefreshFails(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.handle("session.snapshot", func(map[string]any) any {
		return herdrFail{Code: "unavailable", Message: "snapshot failed"}
	})
	f.handle("agent.list", func(map[string]any) any {
		return herdrFail{Code: "unavailable", Message: "snapshot failed"}
	})
	_, err := n.startWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, cursorAgent, cursorAgent, "", "", "", "", "w1", "")
	if err == nil || !strings.Contains(err.Error(), "snapshot failed") {
		t.Fatalf("start: %v", err)
	}
	if got := f.trace("pane.send_input", "pane.send_keys"); len(got) != 0 {
		t.Errorf("typed into a pane whose host could not be checked: %v", got)
	}
	closesNothing(t, f)
}

func TestRunAgentInPaneStartsCursorWithoutReentrantLock(t *testing.T) {
	n, f, home := actionNode(t)
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": 1, "name": "zsh", "argv0": "-zsh"}}}})
	f.reply("agent.prompt", map[string]any{})
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=cursor") {
			f.reply("session.snapshot", snapshot(nil, nil, []any{
				agentOf(cursorAgent, "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, cursorAgent),
			}, nil))
		}
	})
	type answer struct {
		s   *proto.Session
		err error
	}
	done := make(chan answer, 1)
	go func() {
		s, err := n.runAgentInPane(context.Background(), "w1:p2", cursorAgent)
		done <- answer{s, err}
	}()
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.s.Agent != cursorAgent || got.s.PaneID != "w1:p2" {
			t.Fatalf("session: %+v", got.s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("adopted work-agent start deadlocked on the pane lock")
	}
	if got := f.trace("agent.prompt"); len(got) != 0 {
		t.Errorf("unexpected first-prompt call for Run Agent Here: %v", got)
	}
	closesNothing(t, f)
}

// The first message of a cursor or agy start goes through herdr's agent.prompt once herdr reports the agent, and the
// session is bound to the pane with the launcher and model it was started with.
func TestWorkAgentStartPromptsThroughHerdr(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.reply("agent.prompt", map[string]any{})
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=cursor") {
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("cursor", "", proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, "cursor")}, nil))
		}
	})
	s, err := n.startWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, "cursor", cursorAgent,
		"cursor/grok-4.7", "", "", "do the thing", "w1", "")
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != cursorAgent || s.PaneID != "w1:p2" || s.Launcher != "cursor" || !s.StartedByPersona {
		t.Errorf("session agent %q pane %q launcher %q started %v", s.Agent, s.PaneID, s.Launcher, s.StartedByPersona)
	}
	got := f.trace("agent.prompt")
	if len(got) != 1 || !strings.Contains(got[0], "do the thing") {
		t.Errorf("prompted %v", got)
	}
	for _, c := range f.trace("pane.send_input") {
		if strings.Contains(c, "do the thing") {
			t.Errorf("the first message was typed: %s", c)
		}
	}
}

// A pi whose extension never lists the session is quit and keeps its tab; the first message is not typed into the pane.
func TestPiStartWithoutExtensionQuitsAgentAndKeepsTheTab(t *testing.T) {
	t.Run("hi in the temporary path", func(t *testing.T) {
		n, f, home := herdrNode(t)
		old := piResumeWait
		piResumeWait = 300 * time.Millisecond
		t.Cleanup(func() { piResumeWait = old })
		withStop(t, 1, time.Millisecond)
		f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
		f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
		f.reply("pane.read", map[string]any{"text": ""})
		const id = "pi-session"
		running := agentOf(piAgent, id, proto.StatusIdle, "w1:p2", "w1:t2", "w1", home, piAgent)
		quitsOn(f, "w1:p2", id, nil, shellPane("w1:p2", "w1:t2", "w1", home, id))
		f.after(func(c herdrCall) {
			if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=pi") {
				f.reply("session.snapshot", snapshot(nil, nil, []any{running}, nil))
			}
		})
		// A short message such as "hi" can also occur in the launch command's temporary path.
		const message = "the first message must stay unsent"
		_, err := n.startWorkAgent(context.Background(), n.hosts.Get(host.Herdr).(*herdrHost), home, piAgent, piAgent, "", "", "", message, "w1", "")
		if err == nil || !strings.Contains(err.Error(), "pi_no_extension") && !strings.Contains(err.Error(), "没有 Persona 扩展") {
			t.Fatalf("start: %v", err)
		}
		assertTrace(t, f.trace("pane.send_keys"), []string{
			sendKeys("w1:p2", "ctrl+c"), sendKeys("w1:p2", "ctrl+c"),
		})
		closesNothing(t, f)
		for _, c := range f.trace("pane.send_input", "agent.prompt") {
			if strings.Contains(c, message) {
				t.Errorf("the message went in: %s", c)
			}
		}
	})
}

// session.start with launcher pi and a model reaches the work-agent start, not the Claude one.
func TestSessionStartPiUsesTheWorkAgent(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=") {
			cancel()
		}
	})
	_, err := n.start(ctx, home, piAgent, "fix it", "", "w1", "", "", "", "", "", modelExecution{Model: "ag-chat/cursor/grok-4.7"})
	// The hook cancels mid-call. The in-flight herdr read then ends either as "context canceled" or, when the
	// cancellation wins the race and expires the connection's deadline first, as "i/o timeout". Both are this test
	// stopping the start on purpose; anything else is a real failure. The trace below checks the command landed.
	if err != nil && !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "i/o timeout") {
		t.Fatal(err)
	}
	typed := f.trace("pane.send_input")
	if len(typed) != 1 || !strings.Contains(typed[0], "HERDR_AGENT=pi pi --model ag-chat/cursor/grok-4.7") || strings.Contains(typed[0], "--session-id") {
		t.Errorf("typed %v", typed)
	}
}

// The hub decides what to do with a permission mode or effort by proto.WorkAgentTypes and proto.NoModeAgentTypes
// (assistant/launcher_modes.go, #7559): it drops them for the agents a start goes through without (work agents) and
// refuses unsupported choices. TRAE CLI keeps an effort even though it takes no Persona permission mode.
func TestStartModeAgentTypesMatchTheNode(t *testing.T) {
	kinds := []string{piAgent, cursorAgent, agyAgent, traexAgent, ompAgent}
	for _, k := range append(append([]string{}, proto.WorkAgentTypes...), proto.NoModeAgentTypes...) {
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			n, f, home := herdrNode(t)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			startLine := "HERDR_AGENT="
			if kind == traexAgent {
				startLine = "model_reasoning_effort=high"
			}
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && strings.Contains(c.String(), startLine) {
					cancel() // the start went through to the agent's command: that is all this asks
				}
			})
			_, err := n.start(ctx, home, kind, "do the thing", "", "", "", "high", "", "", kind)
			typed := f.trace("pane.send_input")
			ran := slices.ContainsFunc(typed, func(c string) bool { return strings.Contains(c, startLine) })
			// Refused: an error before anything was typed. Went through: the agent's command was typed.
			refused := err != nil && len(typed) == 0
			work, none := proto.IsWorkAgentType(kind), proto.IsNoModeAgentType(kind)
			switch {
			case work == none:
				t.Fatalf("%s: in proto.WorkAgentTypes %v, in proto.NoModeAgentTypes %v: want exactly one", kind, work, none)
			case kind == ompAgent && !refused:
				t.Fatalf("%s with an effort: the node did not refuse it (%v)", kind, err)
			case (work || kind == traexAgent) && (refused || !ran):
				t.Fatalf("%s with an effort: the node did not start it (%v)", kind, err)
			}
		})
	}
}

// Classic herdr returning agent_blocked on a work-agent send (agy or cursor) becomes a structured dialog_open refusal
// with delivered=none instead of an uncoded internal/502 error.
func TestWorkAgentSendRefusedByHerdrBlocked(t *testing.T) {
	for _, kind := range []string{agyAgent, cursorAgent} {
		for _, tc := range []struct {
			name       string
			screen     string
			wantDialog string
		}{
			{name: "bare blocked", screen: "ready\n"},
			{name: "dialog on screen", screen: "Allow command?\n❯ 1. Yes\n  2. No\n\nEsc to cancel\n", wantDialog: "Allow command?"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				n, f, home := herdrNode(t)
				const pane = "w1:p1"
				agent := agentOf(kind, "", proto.StatusBlocked, pane, "w1:t1", "w1", home, kind)
				seedAgents(t, n, f, agent)
				f.reply("agent.get", map[string]any{"agent": agent})
				f.reply("agent.prompt", herdrFail{Code: "agent_blocked", Message: "interactive input required"})
				f.setScreen(pane, tc.screen)
				id := otherSession(host.Agent{Agent: kind, PaneID: pane})
				_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": "hello"})
				code, data := refusalOf(t, err)
				if code != "dialog_open" || data["delivered"] != "none" {
					t.Fatalf("code=%q data=%v, want dialog_open delivered=none", code, data)
				}
				if tc.wantDialog != "" && kind == agyAgent {
					if data["dialog"] != tc.wantDialog || data["notice"] != false {
						t.Fatalf("dialog data = %v, want dialog=%q notice=false", data, tc.wantDialog)
					}
				} else if _, has := data["dialog"]; has {
					t.Fatalf("unexpected dialog headline: %v", data)
				}
			})
		}
	}

	// Windows pre-paste blocked detection also marks ErrNotDelivered and ErrDialogOpen alongside ErrAgentBlocked.
	n, f, home := herdrNode(t)
	f.reply("agent.get", map[string]any{"agent": agentOf(agyAgent, "", proto.StatusBlocked, "w1:p2", "w1:t2", "w1", home, agyAgent)})
	err := n.defaultHost().(*herdrHost).prompt(context.Background(), host.ParsePane("w1:p2"), "hello", true)
	if !errors.Is(err, host.ErrAgentBlocked) || !errors.Is(err, host.ErrNotDelivered) || !errors.Is(err, host.ErrDialogOpen) {
		t.Fatalf("windows blocked error missing markers: %v", err)
	}
	if code, data := refusalOf(t, deliveryRefusal(err)); code != "dialog_open" || data["delivered"] != "none" {
		t.Fatalf("deliveryRefusal(windows blocked) = %q %v", code, data)
	}

	// A confirmed blocked refusal never issues a post-send screen read that could overrun the caller's RPC deadline.
	t.Run("cursor/blocked refusal never issues post-send screen read", func(t *testing.T) {
		n, f, home := herdrNode(t)
		const pane = "w1:p1"
		agent := agentOf(cursorAgent, "", proto.StatusBlocked, pane, "w1:t1", "w1", home, cursorAgent)
		seedAgents(t, n, f, agent)
		f.reply("agent.get", map[string]any{"agent": agent})
		f.reply("agent.prompt", herdrFail{Code: "agent_blocked", Message: "interactive input required"})
		reads := 0
		f.handle("pane.read", func(map[string]any) any {
			reads++
			return map[string]any{"text": "Allow command?\n❯ 1. Yes\n  2. No\n\nEsc to cancel\n"}
		})
		id := otherSession(host.Agent{Agent: cursorAgent, PaneID: pane})
		raw, _ := json.Marshal(map[string]any{"session_id": id, "text": "hello"})
		_, err := n.handle(context.Background(), "session.send", raw)
		code, data := refusalOf(t, err)
		if code != "dialog_open" || data["delivered"] != "none" {
			t.Fatalf("code=%q data=%v, want dialog_open delivered=none", code, data)
		}
		if reads != 0 {
			t.Fatalf("pane.read called %d times on blocked refusal, want 0", reads)
		}
	})
}
