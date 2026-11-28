package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An older hub still names a runtime (assistant.warm, assistant.send, session.send, a guest's start and a meeting's
// room): the node ignores it. Every conversation starts on the CLI command (claude -p through the launcher), a message
// on one already running goes to the same process, and nothing of another runtime is unpacked.
func TestAssistantIgnoresARuntimeFromAnOlderHub(t *testing.T) {
	for _, runtime := range []any{"agentsdk", "cli", "another", nil} {
		name, _ := json.Marshal(runtime)
		t.Run("runtime="+string(name), func(t *testing.T) {
			f := cliTest(t)
			cwd := t.TempDir()
			call := func(method string, params map[string]any) string {
				t.Helper()
				params["runtime"] = runtime
				raw, err := nodeCall(t, f.n, method, params)
				if err != nil {
					t.Fatalf("%s: %v", method, err)
				}
				var out struct {
					SessionID string `json:"session_id"`
				}
				_ = json.Unmarshal(raw, &out)
				return out.SessionID
			}
			onCLI := func(what string, p *assistantProc) {
				t.Helper()
				if p == nil || !slices.Contains(p.cmd.Args, shellFlag(p.cmd.Args[0])) || !slices.Contains(p.cmd.Args, "--input-format") {
					t.Fatalf("%s does not run claude -p through the launcher: %+v", what, p)
				}
			}

			call("assistant.warm", map[string]any{"launcher": "claude", "cwd": cwd, "model_fingerprint": "fp"})
			f.n.mu.Lock()
			spare := f.n.spare
			f.n.mu.Unlock()
			onCLI("the warm spare", spare)

			id := call("assistant.send", map[string]any{"launcher": "claude", "cwd": cwd, "text": "hello", "model_fingerprint": "fp"})
			f.idle(t, id)
			first := f.proc(id)
			onCLI("a new conversation", first)
			if !strings.Contains(f.file("cli-argv"), "--session-id "+id) {
				t.Fatalf("claude args: %s", f.file("cli-argv"))
			}

			call("session.send", map[string]any{"session_id": id, "text": "again", "model_fingerprint": "fp"})
			f.idle(t, id)
			if f.proc(id) != first {
				t.Fatal("a runtime on session.send restarted the conversation")
			}
			if strings.Count(f.file("stdin.jsonl"), `"type":"user"`) != 2 {
				t.Fatalf("messages written: %s", f.file("stdin.jsonl"))
			}

			guest := call("assistant.send", map[string]any{"launcher": "claude", "cwd": t.TempDir(), "text": "hi",
				"guest": map[string]any{"tools": []string{"lark_agent_reply"}}})
			onCLI("a guest", f.proc(guest))
			if args := f.proc(guest).cmd.Args; !slices.Contains(args, "--strict-mcp-config") {
				t.Fatalf("the guest's args: %q", args)
			}

			rec := "0f8fad5b-d9cb-469f-a165-70867728950e"
			out, err := f.n.assistantGuestPrepare(json.RawMessage(`{"dir":".persona/guest-meeting/` + rec + `","claude_md":"# room","kind":"meeting"}`))
			if err != nil {
				t.Fatal(err)
			}
			roomDir, _ := out.(map[string]any)["cwd"].(string)
			room := "7d4a3c1e-1111-4222-8333-444455556666"
			if got := call("assistant.send", map[string]any{"launcher": "claude", "cwd": roomDir, "text": "the header",
				"new_session_id": room, "guest": map[string]any{"tools": []string{"meeting_lines"}, "kind": guestMeeting}}); got != room {
				t.Fatalf("the room started as %q, want %q", got, room)
			}
			onCLI("a meeting's room", f.proc(room))

			if _, err := os.Stat(filepath.Join(f.n.dir, "runtime-packages")); !os.IsNotExist(err) {
				t.Fatalf("a runtime package was unpacked: %v", err)
			}
			for _, sid := range []string{id, guest, room} {
				st, _ := f.n.state.get(sid)
				if raw, _ := json.Marshal(st); strings.Contains(string(raw), `"runtime"`) {
					t.Fatalf("the state records a runtime: %s", raw)
				}
			}
		})
	}
}

// A conversation an older node ran on the Agent SDK: the hub's state for it has the launcher and the profile that node
// chose (never a runtime), and its transcript was started by the SDK. It continues on the CLI: claude itself resumes
// the same conversation id in the profile its state recorded, whether an older hub still names the SDK runtime or a
// current one names none; the state the node writes back has no runtime.
func TestAssistantResumesAConversationAnOlderNodeRanOnAnotherRuntime(t *testing.T) {
	for _, hub := range []string{"older hub", "current hub"} {
		t.Run(hub, func(t *testing.T) {
			f, _ := resumeTest(t)
			n := f.n
			id := "a5500000-0000-4000-8000-000000000901"
			cwd := filepath.Join(n.dir, "assistant")
			profile := filepath.Join(n.dir, "profiles", "claude")
			writeRunTranscript(t, profile, cwd, id, "sdk-ts")
			// state.load's answer for the conversation, as the hub keeps it.
			load := []byte(`{"sessions":{"` + id + `":{"launcher":"claude","cwd":` + jsonString(t, cwd) + `,` +
				`"model_fingerprint":"fp-main","config_dir":` + jsonString(t, profile) + `,` +
				`"is_main":true,"started_at":"2026-11-01T00:00:00Z"}}}`)
			var res struct {
				Sessions map[string]sessionState `json:"sessions"`
			}
			if err := json.Unmarshal(load, &res); err != nil {
				t.Fatal(err)
			}
			n.state.merge(res.Sessions)

			params := map[string]any{"session_id": id, "launcher": "claude", "cwd": cwd, "text": "hello", "model_fingerprint": "fp-main", "is_main": true}
			if hub == "older hub" {
				params["runtime"] = "agentsdk"
			}
			raw, err := nodeCall(t, n, "assistant.send", params)
			if err != nil {
				t.Fatalf("resume failed: %v (claude saw %q)", err, f.file("cli-profile"))
			}
			var out struct {
				SessionID string `json:"session_id"`
			}
			if json.Unmarshal(raw, &out) != nil || out.SessionID != id {
				t.Fatalf("answered %s, want the same conversation %s", raw, id)
			}
			p := f.proc(id)
			if p == nil {
				t.Fatal("no process")
			}
			if i := slices.Index(p.cmd.Args, "--resume"); i < 0 || i+1 >= len(p.cmd.Args) || p.cmd.Args[i+1] != id ||
				!slices.Contains(p.cmd.Args, shellFlag(p.cmd.Args[0])) || slices.Contains(p.cmd.Args, "--session-id") ||
				!strings.Contains(strings.Join(p.cmd.Args, " "), "command claude") {
				t.Fatalf("not claude -p itself resuming %s: %q", id, p.cmd.Args)
			}
			if got := envValue(p.cmd.Environ(), "CLAUDE_CONFIG_DIR"); !sameScopePath(got, profile) {
				t.Fatalf("process profile %q, want %q", got, profile)
			}
			cliWait(t, "claude's profile", func() bool { return f.file("cli-profile") != "" })
			if got := strings.TrimSuffix(f.file("cli-profile"), "\n"); !sameScopePath(strings.TrimPrefix(got, "set:"), profile) {
				t.Fatalf("claude ran in profile %q, want %q", got, profile)
			}
			st, _ := n.state.get(id)
			if !sameScopePath(st.ConfigDir, profile) || st.Launcher != "claude" {
				t.Fatalf("state %+v", st)
			}
			if b, _ := json.Marshal(st); strings.Contains(string(b), `"runtime"`) {
				t.Fatalf("the state written back records a runtime: %s", b)
			}
		})
	}
}

// writeRunTranscript writes assistant conversation id's transcript in profile: a user line and an answer that a
// Claude Code started through entrypoint wrote.
func writeRunTranscript(t *testing.T, profile, cwd, id, entrypoint string) {
	t.Helper()
	ep := jsonString(t, entrypoint)
	writeFile(t, filepath.Join(profile, "projects", "-assistant", id+".jsonl"),
		`{"type":"user","cwd":`+jsonString(t, cwd)+`,"entrypoint":`+ep+`,"message":{"role":"user","content":"earlier"}}`+"\n"+
			`{"type":"assistant","entrypoint":`+ep+`,"message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`+"\n")
}

// A conversation the Agent SDK started with the plain claude launcher keeps running claude as the SDK did, the
// executable itself (`command claude`, past an alias or shell function named claude) in the profile its state
// recorded, resuming it there: also where `claude` sets a profile of its own, and after the CLI's lines followed the
// SDK's. A conversation the CLI started goes through the launcher as before, and is refused where the launcher's own
// profile is another (#4310).
func TestAssistantResumesAnAgentSDKConversationPastAClaudeWrapper(t *testing.T) {
	for _, c := range []struct{ wrapper, started string }{
		{"none", "sdk-ts"}, {"alias", "sdk-ts"}, {"function", "sdk-ts"}, {"none", "sdk-cli"}, {"alias", "sdk-cli"},
	} {
		t.Run(c.wrapper+"/"+c.started, func(t *testing.T) {
			f, _ := resumeTest(t)
			n := f.n
			own := filepath.Join(n.home, ".claude-work")
			switch c.wrapper {
			case "alias":
				writeFile(t, filepath.Join(n.home, ".zshrc"), `alias claude='CLAUDE_CONFIG_DIR=`+shellQuote(own)+` claude'`+"\n")
				if got := n.assistantLauncherProfile(context.Background(), "claude"); !sameScopePath(got, own) {
					t.Fatalf("the claude alias's own profile %q, want %q", got, own)
				}
			case "function": // the probe's own claude stands in for it: no profile of its own is seen
				writeFile(t, filepath.Join(n.home, ".zshrc"), `function claude { CLAUDE_CONFIG_DIR=`+shellQuote(own)+` command claude "$@"; }`+"\n")
			}
			id := "a5500000-0000-4000-8000-000000000902"
			cwd := filepath.Join(n.dir, "assistant")
			profile := filepath.Join(n.dir, "profiles", "claude")
			transcript := filepath.Join(profile, "projects", "-assistant", id+".jsonl")
			writeRunTranscript(t, profile, cwd, id, c.started)
			putSessionState(t, n, id, map[string]any{"launcher": "claude", "cwd": cwd, "config_dir": profile})
			params := map[string]any{"session_id": id, "launcher": "claude", "cwd": cwd, "text": "hello", "model_fingerprint": "fp-main"}
			_, err := nodeCall(t, n, "assistant.send", params)
			if c.wrapper == "alias" && c.started == "sdk-cli" {
				if err == nil || !strings.Contains(err.Error(), "conflicts with the launcher's own profile") {
					t.Fatalf("a conversation the CLI started: %v, want the profile conflict", err)
				}
				return
			}
			for round := 1; round <= 2; round++ {
				if err != nil {
					t.Fatalf("round %d: resume failed: %v (claude saw %q)", round, err, f.file("cli-profile"))
				}
				p := f.proc(id)
				if p == nil {
					t.Fatalf("round %d: no process", round)
				}
				if i := slices.Index(p.cmd.Args, "--resume"); i < 0 || i+1 >= len(p.cmd.Args) || p.cmd.Args[i+1] != id {
					t.Fatalf("round %d: not resuming %s: %q", round, id, p.cmd.Args)
				}
				if itself := strings.Contains(strings.Join(p.cmd.Args, " "), "command claude"); itself != (c.started == "sdk-ts") {
					t.Fatalf("round %d: claude itself %v: %q", round, itself, p.cmd.Args)
				}
				cliWait(t, "claude's profile", func() bool { return f.file("cli-profile") != "" })
				if got := strings.TrimSuffix(f.file("cli-profile"), "\n"); !sameScopePath(strings.TrimPrefix(got, "set:"), profile) {
					t.Fatalf("round %d: claude ran in profile %q, want %q", round, got, profile)
				}
				if st, _ := n.state.get(id); !sameScopePath(st.ConfigDir, profile) {
					t.Fatalf("round %d: recorded profile %q", round, st.ConfigDir)
				}
				// The CLI's lines after the SDK's: the conversation is still the one the SDK started.
				n.stopAssistant(id)
				_ = os.Remove(filepath.Join(f.dir, "cli-profile"))
				appendFile(t, transcript, `{"type":"user","entrypoint":"sdk-cli","message":{"role":"user","content":"later"}}`+"\n")
				_, err = nodeCall(t, n, "assistant.send", params)
			}
		})
	}
}

// The first entrypoint on a transcript is the start's, wherever the reads that take it in cut it.
func TestStartedOnAgentSDK(t *testing.T) {
	dir := t.TempDir()
	read := 64 << 10
	sdk, cli := `"entrypoint":"sdk-ts"`, `"entrypoint":"sdk-cli"`
	for name, c := range map[string]struct {
		text string
		want bool
	}{
		"the CLI's":               {`{` + cli + `}` + "\n" + `{` + sdk + `}` + "\n", false},
		"the SDK's":               {`{` + sdk + `}` + "\n" + `{` + cli + `}` + "\n", true},
		"after lines without one": {`{"type":"summary"}` + "\n" + `{` + sdk + `}` + "\n", true},
		"past the first read":     {`{"type":"user","text":"` + strings.Repeat("x", 2*read) + `",` + sdk + `}` + "\n", true},
		"its key cut by a read":   {strings.Repeat("x", read-10) + sdk + "\n", true},
		"its value cut by a read": {strings.Repeat("x", read-len(`"entrypoint":"`)-2) + sdk + "\n", true},
		"none":                    {`{"type":"summary"}` + "\n", false},
	} {
		p := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".jsonl")
		writeFile(t, p, c.text)
		if got := startedOnAgentSDK(p); got != c.want {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if startedOnAgentSDK("") || startedOnAgentSDK(filepath.Join(dir, "missing.jsonl")) {
		t.Error("no transcript was started on the SDK")
	}
}

// A CLI conversation's execution fingerprint is the one a node before #9799 computed for it (golden), so a node update
// does not restart the conversations it runs; a runtime of "cli" counted as none there.
func TestCLIExecutionFingerprintIsUnchanged(t *testing.T) {
	n, _ := testNode(t)
	for _, c := range []struct {
		m    modelExecution
		want string
	}{
		{modelExecution{}, "5544069c813ca3c85cbaad795109f79164da3209f4af64a29c0def96499ce994"},
		{modelExecution{Model: "claude-opus-4-1", Effort: "high", UseEffort: true, Fingerprint: "binding-1", IsMain: true,
			FollowsSetting: true, MainCompactWindow: 400000, SupervisionInstructions: "role"},
			"8d6a7d05620513ee720dfb253343240b789761f6c142b6878c189b5f56d3b3fd"},
		// what an older node computed for {Model, Fingerprint, Runtime: "cli"}
		{modelExecution{Model: "claude-opus-4-1", Fingerprint: "binding-1"}, "5c300fcdd50cbc8adced417983961abdc858273c17bee69eb73debdff7fccc65"},
	} {
		if got := n.executionFingerprint("claude", c.m); got != c.want {
			raw, _ := json.Marshal(c.m)
			t.Errorf("%s: fingerprint %s, want %s", raw, got, c.want)
		}
	}
}
