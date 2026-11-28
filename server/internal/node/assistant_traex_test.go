package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTraexAssistantCommandUsesShellFlag(t *testing.T) {
	const shell = "/bin/zsh" // On macOS a login shell loads PATH from .zprofile.
	n := codexTestNode(t)
	cmd := n.traexAssistantCommand(shell, traexAgent, nil)
	if got, want := cmd.Args[1], shellFlag(shell); got != want {
		t.Fatalf("shell argument = %q, want %q", got, want)
	}
}

func TestTraexAssistantStartAndResumeWire(t *testing.T) {
	for _, perm := range []struct {
		mode, wantApproval, wantSandbox string
		wantReviewer                    any
	}{
		{proto.ModeDefault, "untrusted", "workspaceWrite", "user"},
		{"acceptEdits", "untrusted", "workspaceWrite", "user"},
		{"auto", "on-request", "workspaceWrite", "user"},
		{proto.ModeBypass, "never", "dangerFullAccess", nil},
	} {
		for _, mode := range []struct {
			method string
			resume bool
		}{
			{"thread/start", false},
			{"thread/resume", true},
		} {
			t.Run(perm.mode+"_"+mode.method, func(t *testing.T) {
				n := codexTestNode(t)
				n.dir = t.TempDir()
				cwd := t.TempDir()
				p, c, f := newFakeCodex(t, n, cwd)
				p.agent, p.launcher, p.origin = traexAgent, traexAgent, originInbox
				p.personaMCPOnly = true
				p.model = modelExecution{Model: "gpt-6.2-sol", Effort: "xhigh", UseEffort: true, Origin: originInbox, OriginKnown: true}
				f.results[mode.method] = map[string]any{
					"thread":         map[string]any{"id": testThread},
					"approvalPolicy": perm.wantApproval, "sandbox": map[string]any{"type": perm.wantSandbox},
				}
				f.results["turn/start"] = map[string]any{"turn": map[string]any{"id": "turn-1"}}
				id := ""
				if mode.resume {
					id = testThread
				}
				if err := c.start(t.Context(), id, cwd, mode.resume, perm.mode); err != nil {
					t.Fatal(err)
				}
				n.noteModes(testThread, perm.mode, "low")
				if err := c.deliver(t.Context(), "", "fixture", nil); err != nil {
					t.Fatal(err)
				}
				for _, method := range []string{mode.method, "turn/start"} {
					sent := f.sent(method)
					if len(sent) != 1 {
						t.Fatalf("%s calls: %d", method, len(sent))
					}
					var got map[string]any
					if err := json.Unmarshal(sent[0].Params, &got); err != nil {
						t.Fatal(err)
					}
					if got["approvalPolicy"] != perm.wantApproval || got["approvalsReviewer"] != perm.wantReviewer || got["model"] != "gpt-6.2-sol" {
						t.Fatalf("%s policy/model: %+v", method, got)
					}
					if method == "thread/resume" && (got["threadId"] != testThread || got["excludeTurns"] != true) {
						t.Fatalf("resume: %+v", got)
					}
					if method == "turn/start" {
						sandbox, _ := got["sandboxPolicy"].(map[string]any)
						capabilities, _ := got["capabilities"].(map[string]any)
						names, _ := capabilities["mcpServers"].([]any)
						if got["effort"] != "xhigh" || sandbox["type"] != perm.wantSandbox || !slices.Equal(names, []any{"persona"}) {
							t.Fatalf("turn policy, MCP or effort: %+v", got)
						}
					}
				}
				args, _ := n.traexAssistantArgs("proc-fixture", "xhigh")
				command := n.traexAssistantCommand("/bin/bash", traexAgent, args)
				joined := strings.Join(command.Args, " ")
				for _, want := range []string{"traex app-server --stdio", "PERSONA_ASSISTANT", "PERSONA_HOME", "proc-fixture", `model_reasoning_effort="xhigh"`} {
					if !strings.Contains(joined, want) {
						t.Errorf("start args missing %q", want)
					}
				}
			})
		}
	}
}

func TestTraexAssistantRefusesUnattestedStartAndProtocolMismatches(t *testing.T) {
	n := codexTestNode(t)
	for _, execution := range []modelExecution{{}, {Origin: originInbox}, {OriginKnown: true}} {
		if _, err := n.spawnAssistantAppServer(traexAgent, "", t.TempDir(), traexAgent, false, false, "", execution); !errors.Is(err, errTraexAssistantOrigin) {
			t.Fatalf("unattested start returned %v", err)
		}
	}
	for _, tc := range []struct {
		name, thread, approval, sandbox, failure string
	}{
		{"wrong thread", "other", "untrusted", "workspaceWrite", "different thread"},
		{"wrong policy", testThread, "never", "dangerFullAccess", "did not attest"},
		{"rpc error", testThread, "untrusted", "workspaceWrite", "fixture resume error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			p, c, f := newFakeCodex(t, n, cwd)
			p.agent = traexAgent
			f.results["thread/resume"] = map[string]any{"thread": map[string]any{"id": tc.thread},
				"approvalPolicy": tc.approval, "sandbox": map[string]any{"type": tc.sandbox}}
			if tc.name == "rpc error" {
				f.errors = map[string]string{"thread/resume": tc.failure}
			}
			if err := c.start(t.Context(), testThread, cwd, true); err == nil || !strings.Contains(err.Error(), tc.failure) {
				t.Fatalf("resume returned %v, want %s", err, tc.failure)
			}
			if c.threadID != "" {
				t.Fatal("failed start registered a thread")
			}
		})
	}
}

func TestTraexInboxFullAccessKeepsPersonaToolGates(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	p, _, f := newFakeCodex(t, n, t.TempDir())
	p.id, p.agent, p.origin, p.personaMCPOnly = testThread, traexAgent, originInbox, true
	n.noteModes(testThread, proto.ModeBypass, "")
	n.state.put(testThread, sessionState{Origin: originInbox, Cwd: p.cwd})
	cfg := olCfg()
	cfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
	n.larkCfg, n.larkCfgAt = cfg, time.Now().Add(time.Hour)
	fh.answer = func(proto.Interaction) proto.Resolution {
		yes := true
		return proto.Resolution{Allow: &yes}
	}
	for i, server := range []string{"external", "jarvis", "persona"} {
		reply := f.request(i+1, "mcpServer/elicitation/request", map[string]any{
			"serverName": server, "_meta": map[string]any{"codex_approval_kind": "mcp_tool_call"},
		})
		want := "decline"
		if server == "persona" {
			want = "accept"
		}
		if reply["action"] != want {
			t.Fatalf("MCP %s: %+v", server, reply)
		}
	}
	reply := f.request(4, "item/permissions/requestApproval", map[string]any{
		"permissions": map[string]any{"network": map[string]any{"enabled": true}},
	})
	if grants, _ := reply["permissions"].(map[string]any); grants == nil || len(grants) != 0 || reply["scope"] != "turn" {
		t.Fatalf("permission granted: %+v", reply)
	}
	reply = f.request(5, "item/tool/requestUserInput", map[string]any{"questions": []any{}})
	if answers, _ := reply["answers"].(map[string]any); answers == nil || len(answers) != 0 {
		t.Fatalf("native user input request returned answers: %+v", reply)
	}
	reply = f.request(6, "item/commandExecution/requestApproval", map[string]any{
		"command": "lark-cli im +messages-send --as bot --text fixture",
	})
	if reply["decision"] != "decline" || len(fh.interactions()) != 0 {
		t.Fatalf("Inbox outbound command was not refused by the Lark gate: %+v", reply)
	}
}

func TestTraexAssistantResumeKeepsOriginalHarness(t *testing.T) {
	n := codexTestNode(t)
	probedLaunchers(t, map[string]string{"cx_test": "codex"})
	dir := filepath.Join(n.traexHome(), "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, originator, requested, want string }{
		{dir, "persona", "cx_test", traexAgent},
		{t.TempDir(), "traex-persona", "cx_test", traexAgent},
		{t.TempDir(), "persona", traexAgent, "codex"},
	} {
		path := filepath.Join(tc.path, "rollout-2026-11-25T17-00-00-"+testThread+".jsonl")
		if err := os.WriteFile(path, []byte(`{"type":"session_meta","payload":{"originator":"`+tc.originator+`"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		n.setPath(testThread, path)
		got, switched := n.assistantResumeLauncher(t.Context(), testThread, tc.requested, sessionState{})
		if !switched || got != tc.want {
			t.Fatalf("resume from %s: %q, changed=%v; want %s", tc.originator, got, switched, tc.want)
		}
	}
}
