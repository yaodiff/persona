//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// eventHub records interaction.create calls and distinguishes interaction.dismiss (answered_by: "terminal")
// from interaction.cancel.
type eventHub struct {
	mu        sync.Mutex
	asked     []proto.Interaction
	cancelled []string
	dismissed []string
}

func (h *eventHub) counts() (asked, cancelled, dismissed int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.asked), len(h.cancelled), len(h.dismissed)
}

func withEventHub(t testing.TB, n *Node) *eventHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	peer := rpc.New("n", n2hW)
	n.peer = peer
	hub := rpc.New("h", h2nW)
	h := &eventHub{}
	hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "interaction.create" {
			return map[string]any{}, nil
		}
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		h.asked = append(h.asked, p.Interaction)
		h.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}
	hub.OnEvent = func(event string, params json.RawMessage) {
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(params, &p)
		h.mu.Lock()
		switch event {
		case "interaction.cancel":
			h.cancelled = append(h.cancelled, p.ID)
		case "interaction.dismiss":
			h.dismissed = append(h.dismissed, p.ID)
		}
		h.mu.Unlock()
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })
	return h
}

func TestCCPluginPollAuthorizesPermissionsAndBlocksPersonaHooks(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	fh := withFakeHub(t, n)
	p := bgTranscript(t, home, home, sidA, "default")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// Older plugin without "permissions" capability does not get permissions: true or Approve.
	status, out := pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.2.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings"},
		"wait_ms":        50,
	})
	if status != http.StatusOK || out["permissions"] == true {
		t.Fatalf("legacy poll: %d %v", status, out)
	}
	if n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatal("legacy plugin marked permissionsCapable")
	}

	// Custom user --settings /other/claude-settings.json or --plugin-dir /other/claude-hooks must NOT be mistaken for persona-hooks.
	oldArgv := processArgv
	processArgv = func(int) ([]string, error) {
		return []string{"claude", "--settings", "/other/dir/claude-settings.json", "--plugin-dir", "/other/dir/claude-hooks"}, nil
	}
	t.Cleanup(func() { processArgv = oldArgv })

	// New plugin with "permissions" capability gets immediate permissions: true authorization (< 500 ms even with wait_ms=5000).
	start := time.Now()
	status, out = pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.3.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":        5000,
	})
	if status != http.StatusOK || out["permissions"] != true || time.Since(start) > time.Second {
		t.Fatalf("initial permissions poll with custom unrelated settings path: %d %v after %v", status, out, time.Since(start))
	}
	if !n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatal("expected permissionsCapable=true after authorized poll")
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("session Capabilities.Approve not set: %+v", s.Capabilities)
	}

	// A ReportOnly global hook must NOT suppress plugin permissions.
	roRaw, _ := json.Marshal(hookInput{HookEventName: "SessionStart", SessionID: sidA, Cwd: home})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: roRaw, ReportOnly: true, AgentPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if !n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatal("ReportOnly global hook unexpectedly disabled plugin permissions")
	}

	// Process launched with Persona's actual managed hooks path in argv is refused permissions on both /poll and /permission.
	processArgv = func(int) ([]string, error) {
		return []string{"claude", "--plugin-dir", filepath.Join(n.dir, "claude-hooks")}, nil
	}
	status, out = pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.3.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":        50,
	})
	if status != http.StatusOK || out["permissions"] == true {
		t.Fatalf("persona-hooks argv session got permissions: %d %v", status, out)
	}
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-blocked-argv",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" || len(fh.interactions()) != 0 {
		t.Fatalf("persona-hooks argv /permission: %d %v (cards=%d)", status, out, len(fh.interactions()))
	}

	// Persona's actual managed claude-settings.json is also blocked.
	processArgv = func(int) ([]string, error) {
		return []string{"claude", "--settings=" + filepath.Join(n.dir, "claude-settings.json")}, nil
	}
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-blocked-settings",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" || len(fh.interactions()) != 0 {
		t.Fatalf("persona-settings argv /permission: %d %v (cards=%d)", status, out, len(fh.interactions()))
	}

	// Clear argv stub, re-authorize, then verify a non-ReportOnly hook dynamically revokes permissions.
	processArgv = oldArgv
	status, out = pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.3.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":        200,
	})
	if status != http.StatusOK || out["permissions"] != true {
		t.Fatalf("re-authorize poll: %d %v", status, out)
	}
	managedRaw, _ := json.Marshal(hookInput{HookEventName: "SessionStart", SessionID: sidA, Cwd: home})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: managedRaw, ReportOnly: false, AgentPID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatal("managed non-ReportOnly hook did not revoke plugin permissions")
	}
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-blocked-hook",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" || len(fh.interactions()) != 0 {
		t.Fatalf("managed hook /permission: %d %v (cards=%d)", status, out, len(fh.interactions()))
	}

	// External tmux session keeps tmux screen cards instead of plugin permissions (avoids scr_ + ix_ double cards).
	writeOutsideClaude(t, home, sidB, "cli", "waiting", inbox, map[string]any{"tmux": "main:@1.%1"})
	status, out = pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidB,
		"plugin_version": "0.3.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":        50,
	})
	if status != http.StatusOK || out["permissions"] == true {
		t.Fatalf("external tmux /poll got permissions: %d %v", status, out)
	}
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidB,
		"req_id":     "req-blocked-tmux",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" || len(fh.interactions()) != 0 {
		t.Fatalf("external tmux /permission: %d %v (cards=%d)", status, out, len(fh.interactions()))
	}
}

func TestCCPluginClassicPassedOverDisablesPermissionBridge(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	h := withEventHub(t, n)
	p := bgTranscript(t, home, home, sidA, "policy")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	poll := func(passedBy bool) (int, map[string]any) {
		t.Helper()
		body := map[string]any{
			"session_id":     sidA,
			"plugin_version": "0.3.1",
			"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
			"wait_ms":        5000,
		}
		if passedBy {
			body["classic_passed_over"] = true
		}
		return pluginPost(t, c, "poll", body)
	}

	// Before the plugin has seen a pass-over, the normal authorization and row are unchanged.
	status, out := poll(false)
	if status != http.StatusOK || out["permissions"] != true || !n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatalf("initial authorized poll: %d %v", status, out)
	}
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("normal desktop row lost Approve: %+v", s.Capabilities)
	}

	type permResult struct {
		status int
		out    map[string]any
	}
	permDone := make(chan permResult, 1)
	go func() {
		st, o := pluginPost(t, c, "permission", map[string]any{
			"session_id":  sidA,
			"req_id":      "req-before-passed-over",
			"tool_name":   "Bash",
			"tool_use_id": "toolu_before",
			"tool_input":  map[string]any{"command": "ls"},
			"wait_ms":     5000,
		})
		permDone <- permResult{st, o}
	}()
	waitFor(t, "plugin permission card before pass-over", func() bool { asked, _, _ := h.counts(); return asked == 1 })

	// A poll carrying the plugin's observation revokes permission bridging immediately and changes the row.
	start := time.Now()
	status, out = poll(true)
	if status != http.StatusOK || out["permissions"] == true || time.Since(start) > time.Second {
		t.Fatalf("passed-over poll: %d %v after %v", status, out, time.Since(start))
	}
	if n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatal("passed-over plugin still permissionsCapable")
	}
	s := byID(listSessions(t, n))[sidA]
	if s.Capabilities == nil || s.Capabilities.Approve || s.AttentionReason != proto.AttentionClaudePluginPassedOver {
		t.Fatalf("passed-over desktop row: %+v caps=%+v", s, s.Capabilities)
	}
	select {
	case got := <-permDone:
		if got.status != http.StatusOK || got.out["action"] != "passthrough" {
			t.Fatalf("active card after pass-over: %d %v", got.status, got.out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active plugin permission card did not end on pass-over")
	}
	waitFor(t, "active card cancelled on pass-over", func() bool { _, cancelled, _ := h.counts(); return cancelled == 1 })

	// Even a direct permission call cannot create a card after the plugin reported the pass-over.
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-passed-over",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    200,
	})
	asked, _, _ := h.counts()
	if status != http.StatusOK || out["action"] != "passthrough" || asked != 1 {
		t.Fatalf("passed-over /permission: %d %v (cards=%d)", status, out, asked)
	}
}

func TestCCPluginClassicPassedOverKeepsQuestionCards(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"ask me"}}`,
		askCall(sidA, "toolu_question", `{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	status, out := pluginPost(t, c, "poll", map[string]any{
		"session_id":          sidA,
		"plugin_version":      "0.3.1",
		"capabilities":        []string{"busy_input", "interrupt", "settings", "permissions"},
		"classic_passed_over": true,
		"wait_ms":             50,
	})
	if status != http.StatusOK || out["permissions"] == true || n.ccPluginPermissionsCapable(sidA, os.Getpid()) {
		t.Fatalf("passed-over poll enabled plugin permissions: %d %v", status, out)
	}
	cards := n.outsideAskCards()
	if len(cards) != 1 || !strings.HasPrefix(cards[0].ID, outsideAskPrefix) {
		t.Fatalf("question card changed under pass-over: %+v", cards)
	}
}

func TestCCPluginPermissionLongPollRenewalAndDecisions(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	fh := withFakeHub(t, n)
	p := bgTranscript(t, home, home, sidA, "default")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// 1. Renewable long poll: 2 renew rounds, then phone allows with Always=true during the gap before poll 3.
	release := make(chan struct{})
	yes := true
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		<-release
		return proto.Resolution{Allow: &yes, Always: true}
	}
	fh.mu.Unlock()

	permReq := map[string]any{
		"session_id":  sidA,
		"req_id":      "req-bash-renew",
		"tool_name":   "Bash",
		"tool_use_id": "toolu_bash_renew",
		"tool_input":  map[string]any{"command": "echo hi"},
		"wait_ms":     60,
	}
	for round := 1; round <= 2; round++ {
		status, out := pluginPost(t, c, "permission", permReq)
		if status != http.StatusOK || out["action"] != "renew" {
			t.Fatalf("round %d: %d %v", round, status, out)
		}
	}
	if got := len(fh.interactions()); got != 1 {
		t.Fatalf("expected exactly 1 hub card across renewals, got %d", got)
	}
	// Resolve in the gap between renewal polls; the next poll must immediately return the cached decision.
	close(release)
	key := ccPluginPermID{sessionID: sidA, pid: os.Getpid(), reqID: "req-bash-renew"}
	waitFor(t, "perm entry settled", func() bool {
		n.ccPlugins.mu.Lock()
		e := n.ccPlugins.perms[key]
		n.ccPlugins.mu.Unlock()
		if e == nil {
			return false
		}
		_, _, done := e.outcome()
		return done
	})
	status, out := pluginPost(t, c, "permission", permReq)
	if status != http.StatusOK || out["action"] != "decide" {
		t.Fatalf("decided poll: %d %v", status, out)
	}
	dec, _ := out["decision"].(map[string]any)
	if dec["behavior"] != "allow" {
		t.Fatalf("decision: %v", dec)
	}
	perms, _ := dec["updatedPermissions"].([]any)
	if len(perms) != 1 {
		t.Fatalf("expected session addRules updatedPermissions: %v", dec)
	}

	// 2. Same req_id from a different session is scoped by (session_id, pid, req_id) and does not collide with 403.
	writeOutsideClaude(t, home, sidB, "claude-desktop-3p", "waiting", inbox, nil)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &yes} }
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidB,
		"req_id":     "req-bash-renew",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "echo other"},
		"wait_ms":    500,
	})
	if status != http.StatusOK || out["action"] != "decide" {
		t.Fatalf("same req_id on sidB collided: %d %v", status, out)
	}
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)

	// 3. Deny with custom message.
	no := false
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Allow: &no, Message: "do not run this"}
	}
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-deny",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "rm -rf /tmp/x"},
		"wait_ms":    500,
	})
	dec, _ = out["decision"].(map[string]any)
	if status != http.StatusOK || out["action"] != "decide" || dec["behavior"] != "deny" || dec["message"] != "do not run this" {
		t.Fatalf("deny result: %d %v", status, out)
	}

	// 4. Dismiss ("Handle on computer") returns passthrough.
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Dismiss: true} }
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-dismiss",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "ls"},
		"wait_ms":    500,
	})
	if status != http.StatusOK || out["action"] != "passthrough" {
		t.Fatalf("dismiss result: %d %v", status, out)
	}

	// 5. AskUserQuestion answer & cancel (Skip).
	fh.mu.Lock()
	fh.answer = func(ix proto.Interaction) proto.Resolution {
		if ix.Kind != proto.IxQuestion || !ix.CanCancel {
			t.Errorf("unexpected question interaction: %+v", ix)
		}
		return proto.Resolution{Answers: map[string]string{"Color?": "Blue"}}
	}
	fh.mu.Unlock()
	qInput := map[string]any{"questions": []any{map[string]any{
		"question": "Color?", "header": "Pick", "options": []any{map[string]any{"label": "Blue"}, map[string]any{"label": "Red"}},
	}}}
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id":  sidA,
		"req_id":      "req-ask",
		"tool_name":   "AskUserQuestion",
		"tool_use_id": "toolu_ask_1",
		"tool_input":  qInput,
		"wait_ms":     500,
	})
	dec, _ = out["decision"].(map[string]any)
	updated, _ := dec["updatedInput"].(map[string]any)
	answers, _ := updated["answers"].(map[string]any)
	if status != http.StatusOK || out["action"] != "decide" || dec["behavior"] != "allow" || answers["Color?"] != "Blue" {
		t.Fatalf("ask answer result: %d %v", status, out)
	}

	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Cancel: true} }
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id":  sidA,
		"req_id":      "req-ask-cancel",
		"tool_name":   "AskUserQuestion",
		"tool_use_id": "toolu_ask_2",
		"tool_input":  qInput,
		"wait_ms":     500,
	})
	dec, _ = out["decision"].(map[string]any)
	if status != http.StatusOK || out["action"] != "decide" || dec["behavior"] != "deny" || dec["message"] != prompts.RenderQuestionCancelled() {
		t.Fatalf("ask cancel result: %d %v", status, out)
	}

	// 6. ExitPlanMode approve with setMode and reject with feedback.
	fh.mu.Lock()
	fh.answer = func(ix proto.Interaction) proto.Resolution {
		if ix.Kind != proto.IxPlan || ix.Plan != "Step 1" {
			t.Errorf("unexpected plan interaction: %+v", ix)
		}
		return proto.Resolution{Approve: &yes, Mode: "acceptEdits"}
	}
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id":  sidA,
		"req_id":      "req-plan-ok",
		"tool_name":   "ExitPlanMode",
		"tool_use_id": "toolu_plan_1",
		"tool_input":  map[string]any{"plan": "Step 1"},
		"wait_ms":     500,
	})
	dec, _ = out["decision"].(map[string]any)
	planPerms, _ := dec["updatedPermissions"].([]any)
	firstPerm, _ := planPerms[0].(map[string]any)
	if status != http.StatusOK || out["action"] != "decide" || dec["behavior"] != "allow" || firstPerm["type"] != "setMode" || firstPerm["mode"] != "acceptEdits" {
		t.Fatalf("plan approve result: %d %v", status, out)
	}

	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Approve: &no, Feedback: "add tests first"}
	}
	fh.mu.Unlock()
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-plan-no",
		"tool_name":  "ExitPlanMode",
		"tool_input": map[string]any{"plan": "Step 1"},
		"wait_ms":    500,
	})
	dec, _ = out["decision"].(map[string]any)
	if status != http.StatusOK || out["action"] != "decide" || dec["behavior"] != "deny" || !strings.Contains(dec["message"].(string), "add tests first") {
		t.Fatalf("plan reject result: %d %v", status, out)
	}
}

func TestCCPluginPermissionConcurrentAlsoWaitingAndDoneWithdrawal(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	t.Cleanup(func() { <-n.notifySessionsSoon() })
	h := withEventHub(t, n)
	p := bgTranscript(t, home, home, sidA, "default")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// Authorize plugin first so claudebg knows permissionsCapable=true.
	status, out := pluginPost(t, c, "poll", map[string]any{
		"session_id":   sidA,
		"capabilities": []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":      500,
	})
	if status != http.StatusOK || out["permissions"] != true {
		t.Fatalf("poll: %d %v", status, out)
	}

	// Open two concurrent permission requests ("Also waiting"): one main-loop, one subagent.
	type res struct {
		status int
		out    map[string]any
	}
	res1, res2 := make(chan res, 1), make(chan res, 1)
	go func() {
		st, o := pluginPost(t, c, "permission", map[string]any{
			"session_id":  sidA,
			"req_id":      "req-concurrent-1",
			"tool_name":   "Bash",
			"tool_use_id": "toolu_c1",
			"tool_input":  map[string]any{"command": "echo 1"},
			"wait_ms":     5000,
		})
		res1 <- res{st, o}
	}()
	go func() {
		st, o := pluginPost(t, c, "permission", map[string]any{
			"session_id":  sidA,
			"req_id":      "req-concurrent-2",
			"tool_name":   "Bash",
			"tool_use_id": "toolu_c2",
			"agent_id":    "sub-1",
			"tool_input":  map[string]any{"command": "echo 2"},
			"wait_ms":     5000,
		})
		res2 <- res{st, o}
	}()
	waitFor(t, "both concurrent cards on hub", func() bool { asked, _, _ := h.counts(); return asked == 2 })

	// While an active plugin card exists, session row has Approve=true and NO computer-only AttentionReason.
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.Approve || s.AttentionReason != "" {
		t.Fatalf("active plugin card row: %+v caps=%+v", s, s.Capabilities)
	}

	// Computer allows toolu_c1 first -> /permission-done (post_tool_use) withdraws ONLY card 1 via interaction.dismiss
	// so hub history records answered_by: "terminal".
	status, out = pluginPost(t, c, "permission-done", map[string]any{
		"session_id":  sidA,
		"tool_use_id": "toolu_c1",
		"reason":      "post_tool_use",
	})
	if status != http.StatusOK || out["ok"] != true {
		t.Fatalf("permission-done c1: %d %v", status, out)
	}
	select {
	case r := <-res1:
		if r.status != http.StatusOK || r.out["action"] != "passthrough" {
			t.Fatalf("req 1 after done: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("req 1 did not wake on permission-done")
	}
	waitFor(t, "card 1 dismissed as terminal on hub", func() bool { _, cancelled, dismissed := h.counts(); return dismissed == 1 && cancelled == 0 })

	// Main turn_complete must NOT cancel subagent card 2.
	status, _ = pluginPost(t, c, "permission-done", map[string]any{
		"session_id": sidA,
		"reason":     "turn_complete",
	})
	if status != http.StatusOK {
		t.Fatalf("turn_complete: %d", status)
	}
	select {
	case r := <-res2:
		t.Fatalf("subagent req 2 was unexpectedly cancelled by main turn_complete: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	// Subagent turn_complete withdraws card 2 via interaction.cancel (not terminal approval).
	status, _ = pluginPost(t, c, "permission-done", map[string]any{
		"session_id": sidA,
		"agent_id":   "sub-1",
		"reason":     "turn_complete",
	})
	if status != http.StatusOK {
		t.Fatalf("subagent turn_complete: %d", status)
	}
	select {
	case r := <-res2:
		if r.status != http.StatusOK || r.out["action"] != "passthrough" {
			t.Fatalf("req 2 after subagent turn_complete: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("req 2 did not wake on subagent turn_complete")
	}
	waitFor(t, "card 2 cancelled on hub", func() bool { _, cancelled, dismissed := h.counts(); return cancelled == 1 && dismissed == 1 })

	// Early permission-done by tool_use_id before /permission prevents card creation altogether.
	pluginPost(t, c, "permission-done", map[string]any{
		"session_id":  sidA,
		"tool_use_id": "toolu_already_done",
		"reason":      "post_tool_use",
	})
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id":  sidA,
		"req_id":      "req-late-after-done",
		"tool_name":   "Bash",
		"tool_use_id": "toolu_already_done",
		"tool_input":  map[string]any{"command": "pwd"},
		"wait_ms":     200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" {
		t.Fatalf("late /permission after tool_use_id done: %d %v", status, out)
	}

	// Early permission-done carrying req_ids (turn_complete arriving before first /permission) also prevents orphan card creation.
	pluginPost(t, c, "permission-done", map[string]any{
		"session_id": sidA,
		"req_ids":    []string{"req-early-turn-done"},
		"reason":     "turn_complete",
	})
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-early-turn-done",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "pwd"},
		"wait_ms":    200,
	})
	if status != http.StatusOK || out["action"] != "passthrough" {
		t.Fatalf("late /permission after req_id done: %d %v", status, out)
	}
	if asked, _, _ := h.counts(); asked != 2 {
		t.Fatalf("early done did not suppress card creation: asked=%d", asked)
	}
}

func TestCCPluginPermissionDedupWithHookDecidedAndOutsideAskAndDesktopPersonOnly(t *testing.T) {
	defer func(g time.Duration) { permissionGrace = g }(permissionGrace)
	permissionGrace = 10 * time.Millisecond

	n, f, home := herdrNode(t)
	noPaneEnv(t)
	t.Cleanup(func() { <-n.notifySessionsSoon() })
	h := withEventHub(t, n)
	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","permissionMode":"auto","message":{"role":"user","content":"check"}}`,
		askCall(sidA, "toolu_q_dedup", `{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// Without permissions capability (older 0.2.0 plugin), clq_ fallback stays active.
	pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.2.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings"},
		"wait_ms":        50,
	})
	if cards := n.outsideAskCards(); len(cards) != 1 {
		t.Fatalf("older plugin should still fall back to clq_ card, got %d", len(cards))
	}

	// If AskUserQuestion opened BEFORE the plugin's initial /poll authorization, authorizing permissions must NOT
	// withdraw the existing clq_ fallback card until an active plugin request actually covers that question call.
	status, out := pluginPost(t, c, "poll", map[string]any{
		"session_id":     sidA,
		"plugin_version": "0.3.0",
		"capabilities":   []string{"busy_input", "interrupt", "settings", "permissions"},
		"wait_ms":        500,
	})
	if status != http.StatusOK || out["permissions"] != true {
		t.Fatalf("poll: %d %v", status, out)
	}
	if cards := n.outsideAskCards(); len(cards) != 1 {
		t.Fatalf("pre-poll AskUserQuestion must retain clq_ fallback while no plugin request covers it, got %+v", cards)
	}

	// Once an active plugin request covers toolu_q_dedup, outsideAskCards suppresses clq_ to avoid double cards.
	doneAsk := make(chan struct{})
	go func() {
		defer close(doneAsk)
		_, _ = pluginPost(t, c, "permission", map[string]any{
			"session_id":  sidA,
			"req_id":      "req-ask-active",
			"tool_name":   "AskUserQuestion",
			"tool_use_id": "toolu_q_dedup",
			"tool_input":  map[string]any{"questions": []any{map[string]any{"question": "Which?", "header": "Pick", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}},
			"wait_ms":     5000,
		})
	}()
	waitFor(t, "plugin AskUserQuestion card on hub", func() bool { asked, _, _ := h.counts(); return asked == 1 })
	if cards := n.outsideAskCards(); len(cards) != 0 {
		t.Fatalf("active plugin AskUserQuestion must suppress clq_ fallback, got %+v", cards)
	}
	pluginPost(t, c, "permission-done", map[string]any{
		"session_id":  sidA,
		"tool_use_id": "toolu_q_dedup",
		"reason":      "post_tool_use",
	})
	<-doneAsk
	waitFor(t, "plugin AskUserQuestion card dismissed", func() bool { _, _, dismissed := h.counts(); return dismissed == 1 })

	// Desktop session waiting on a Bash safety review (Auto-mode personOnly that bypassed hooks, neither plugin card nor clq_):
	// Approve=true and accurate Claude Desktop attention reason (never tmux guidance).
	appendLines(t, p, toolResult(sidA, "toolu_q_dedup", "A"),
		`{"type":"assistant","uuid":"a_bash","timestamp":"2026-11-04T09:02:00Z","sessionId":"`+sidA+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_bash_po","name":"Bash","input":{"command":"rm -rf build"}}]}}`)
	s := byID(listSessions(t, n))[sidA]
	if s.Status != proto.StatusBlocked || s.Capabilities == nil || !s.Capabilities.Approve {
		t.Fatalf("desktop waiting row: %+v caps=%+v", s, s.Capabilities)
	}
	if s.AttentionReason != proto.AttentionClaudeDesktopComputer {
		t.Fatalf("desktop personOnly AttentionReason = %q, want %q", s.AttentionReason, proto.AttentionClaudeDesktopComputer)
	}

	// Another PermissionRequest hook already decided this tool call (hookdecided.go): no card created.
	bashInput, _ := json.Marshal(map[string]any{"command": "echo bypass"})
	n.decided.decide(hookInput{
		HookEventName: "PermissionRequest",
		SessionID:     sidA,
		ToolName:      "Bash",
		ToolInput:     bashInput,
	}, time.Now())
	status, out = pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-already-decided",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "echo bypass"},
		"wait_ms":    500,
	})
	if asked, _, _ := h.counts(); status != http.StatusOK || out["action"] != "passthrough" || asked != 1 {
		t.Fatalf("hook.decided dedup failed: %d %v (asked=%d)", status, out, asked)
	}
}

func TestCCPluginPermissionIdleAbandonmentCancelsCard(t *testing.T) {
	oldIdle := ccPluginPermIdleTTL
	ccPluginPermIdleTTL = 120 * time.Millisecond
	t.Cleanup(func() { ccPluginPermIdleTTL = oldIdle })

	n, f, home := herdrNode(t)
	noPaneEnv(t)
	h := withEventHub(t, n)
	p := bgTranscript(t, home, home, sidA, "default")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	status, out := pluginPost(t, c, "permission", map[string]any{
		"session_id": sidA,
		"req_id":     "req-abandoned",
		"tool_name":  "Bash",
		"tool_input": map[string]any{"command": "sleep 1"},
		"wait_ms":    40,
	})
	if status != http.StatusOK || out["action"] != "renew" {
		t.Fatalf("initial poll: %d %v", status, out)
	}
	waitFor(t, "abandoned card cancelled after idle TTL", func() bool {
		asked, cancelled, dismissed := h.counts()
		return asked == 1 && cancelled == 1 && dismissed == 0
	})
	// Keep race detector busy checking concurrent reads while cleanup completes.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = n.ccPlugins.hasActivePermission(sidA)
	}()
	wg.Wait()
}

// Two concurrent processes on the same session without tool_use_id produce distinct Hub interaction IDs
// even if both choose the same req_id in the same millisecond, while renewals keep the same ID.
func TestCCPluginInteractionIDIncludesProcessScope(t *testing.T) {
	in := hookInput{
		HookEventName: "PermissionRequest",
		SessionID:     sidA,
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"ls"}`),
	}
	id1 := interactionID(in, ccPluginPermAttempt(1001, "p_1000_1"))
	id1Renew := interactionID(in, ccPluginPermAttempt(1001, "p_1000_1"))
	id2 := interactionID(in, ccPluginPermAttempt(1002, "p_1000_1"))
	if id1 != id1Renew {
		t.Fatalf("same process renewal changed interactionID: %q vs %q", id1, id1Renew)
	}
	if id1 == id2 {
		t.Fatalf("two processes sharing (session, req_id) collided on interactionID %q", id1)
	}
}

// Replacing an already-published clq_ fallback card withdraws clq_ strictly before publishing the plugin's
// ix_ card, and handing back (dismissing) the plugin's AskUserQuestion prevents clq_ from resurrecting.
func TestCCPluginAskWithdrawsPublishedCLQBeforePublishAndDismissDoesNotResurrect(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	t.Cleanup(func() { <-n.notifySessionsSoon() })

	// Wire-order hub recording codex.interactions events vs interaction.create requests.
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	peer := rpc.New("n", n2hW)
	n.peer = peer
	hub := rpc.New("h", h2nW)

	var mu sync.Mutex
	var wire []string
	hub.OnEvent = func(event string, params json.RawMessage) {
		if event != "codex.interactions" {
			return
		}
		var p struct {
			Interactions []proto.Interaction `json:"interactions"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		if len(p.Interactions) == 0 {
			wire = append(wire, "codex.interactions:empty")
		} else {
			wire = append(wire, "codex.interactions:"+p.Interactions[0].ID)
		}
		mu.Unlock()
	}
	hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "interaction.create" {
			return map[string]any{}, nil
		}
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		wire = append(wire, "interaction.create:"+p.Interaction.ID)
		mu.Unlock()
		return proto.Resolution{Dismiss: true}, nil
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	go func() { _ = peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })

	p := writeTranscript(t, home, home, sidA, time.Now(),
		`{"type":"user","uuid":"u1","timestamp":"2026-11-04T09:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","permissionMode":"default","message":{"role":"user","content":"pick"}}`,
		askCall(sidA, "toolu_handback", `{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A"},{"label":"B"}],"multiSelect":false}]}`))
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	// 1. Before plugin interception, syncCodexCards publishes the clq_ fallback card.
	n.syncCodexCards(context.Background())
	if !n.cx.hasOutsideAsk(sidA) {
		t.Fatal("expected clq_ fallback card to be published before plugin permission arrives")
	}

	// 2. Plugin AskUserQuestion arrives -> withdraws clq_ BEFORE interaction.create, then user dismisses it.
	status, out := pluginPost(t, c, "permission", map[string]any{
		"session_id":  sidA,
		"req_id":      "req-handback-ask",
		"tool_name":   "AskUserQuestion",
		"tool_use_id": "toolu_handback",
		"tool_input":  map[string]any{"questions": []any{map[string]any{"question": "Which?", "header": "Pick", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}},
		"wait_ms":     2000,
	})
	if status != http.StatusOK || out["action"] != "passthrough" {
		t.Fatalf("dismissed ask: %d %v", status, out)
	}

	// Verify strict wire ordering: clq_ published -> clq_ withdrawn -> ix_ created.
	mu.Lock()
	gotWire := append([]string(nil), wire...)
	mu.Unlock()
	if len(gotWire) < 3 ||
		!strings.HasPrefix(gotWire[0], "codex.interactions:"+outsideAskPrefix) ||
		gotWire[1] != "codex.interactions:empty" ||
		!strings.HasPrefix(gotWire[2], "interaction.create:ix_") {
		t.Fatalf("unexpected wire order: %v", gotWire)
	}

	// 3. Subsequent syncCodexCards must NOT resurrect clq_ while the handed-back question is still open.
	n.syncCodexCards(context.Background())
	if n.cx.hasOutsideAsk(sidA) || n.cx.sent != "" {
		t.Fatalf("handed-back plugin AskUserQuestion resurrected clq_ card: hasOutsideAsk=%v sent=%q", n.cx.hasOutsideAsk(sidA), n.cx.sent)
	}
}
