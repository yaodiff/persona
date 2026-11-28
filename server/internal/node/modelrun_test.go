package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func runModel(t *testing.T, p proto.ModelRunParams) (any, error) {
	t.Helper()
	home := os.Getenv("HOME")
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	raw, _ := json.Marshal(p)
	return n.modelRun(context.Background(), raw)
}

func runModelWithNode(t *testing.T, n *Node, p proto.ModelRunParams) (any, error) {
	t.Helper()
	raw, _ := json.Marshal(p)
	return n.modelRun(context.Background(), raw)
}

func runCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// fakeModelRunCLIHome creates a temporary HOME with a fake executable named bin (and registers it in launcherCache with agent)
// so model.run executes the script through the real login shell without touching host CLIs or user config.
func fakeModelRunCLIHome(t *testing.T, bin, agent, script string) string {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, bin)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export PATH="+shellQuote(binDir)+":$PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	withShellLaunchers(t)
	launcherCache.Lock()
	launcherCache.list = []proto.Launcher{{Name: bin, Source: "zshrc", Agent: agent}}
	launcherCache.Unlock()
	probeCache.Lock()
	oldAt, oldGot := probeCache.at, probeCache.got
	probeCache.at = map[string]time.Time{bin: time.Now()}
	probeCache.got = map[string]proto.LauncherDetail{bin: {Agent: agent}}
	probeCache.Unlock()
	t.Cleanup(func() {
		probeCache.Lock()
		probeCache.at, probeCache.got = oldAt, oldGot
		probeCache.Unlock()
	})
	return home
}

// model.run runs claude once, headless, with no tools, MCP servers, hooks or session, the system prompt from a file and
// the conversation on stdin, and answers with its result; a CLI that is not logged in and a run past its time say so
// with their codes, and a word that is no launcher never runs.
func TestModelRunAnswersWithAHeadlessClaude(t *testing.T) {
	out := t.TempDir()
	fakeClaudeHome(t, `printf '%s\n' "$@" > `+shellQuote(filepath.Join(out, "args"))+`
cat > `+shellQuote(filepath.Join(out, "stdin"))+`
pwd > `+shellQuote(filepath.Join(out, "cwd"))+`
while [ $# -gt 0 ]; do
  [ "$1" = --system-prompt-file ] && cat "$2" > `+shellQuote(filepath.Join(out, "system"))+`
  [ "$1" = --settings ] && cat "$2" > `+shellQuote(filepath.Join(out, "settings"))+`
  shift
done
printf '{"type":"result","subtype":"success","is_error":false,"result":"pong","modelUsage":{"m-1":{}},"usage":{"input_tokens":14,"output_tokens":7,"cache_read_input_tokens":95,"cache_creation_input_tokens":83}}\n'
`)
	got, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Model: "m-1", Effort: "high", System: "be brief", Prompt: "ping"})
	usage := aiwire.Usage{InputTokens: 14, OutputTokens: 7, CacheReadInputTokens: 95, CacheCreationInputTokens: 83}
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "pong" || a.Model != "m-1" || a.Usage != usage {
		t.Fatalf("answer: %#v %v", got, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(out, name))
		return strings.TrimSpace(string(b))
	}
	args := "\n" + read("args") + "\n"
	for _, want := range []string{"\n-p\n", "\n--no-session-persistence\n", "\n--tools\n\n", "\n--strict-mcp-config\n", "\n--max-turns\n1\n", "\n--model\nm-1\n", "\n--effort\nhigh\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	if read("stdin") != "ping" || read("system") != "be brief" || !strings.Contains(read("settings"), `"disableAllHooks":true`) {
		t.Errorf("stdin %q, system %q, settings %q", read("stdin"), read("system"), read("settings"))
	}
	if cwd := read("cwd"); !strings.Contains(cwd, launcherCheckDir) {
		t.Errorf("ran in %q", cwd)
	} else if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("its directory was left behind: %v", err)
	}

	fakeClaudeHome(t, `printf '{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}\n'`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping"}); runCode(err) != proto.CodeModelRunLoggedOut {
		t.Fatalf("logged out: %v", err)
	}

	fakeClaudeHome(t, "sleep 5\n")
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping", TimeoutMS: 300}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("timeout: %v", err)
	}

	if _, err := runModel(t, proto.ModelRunParams{Launcher: "reboot", Prompt: "ping"}); err == nil || runCode(err) != "" {
		t.Fatalf("not a launcher: %v", err)
	}
}

// model.run executes Codex headlessly with full zero-tool, zero-MCP, zero-hook, and ephemeral isolation flags, parses
// JSONL events and usage, maps logged-out / timeout / bad-model errors, and fails closed on missing isolation flags.
func TestModelRunAnswersWithIsolatedCodex(t *testing.T) {
	out := t.TempDir()
	fakeModelRunCLIHome(t, "codex", "codex", `printf '%s\n' "$@" > `+shellQuote(filepath.Join(out, "args"))+`
cat > `+shellQuote(filepath.Join(out, "stdin"))+`
pwd > `+shellQuote(filepath.Join(out, "cwd"))+`
while [ $# -gt 0 ]; do
  case "$1" in
    -c)
      case "$2" in
        model_instructions_file=*)
          p=$(printf '%s' "$2" | sed 's/^model_instructions_file="//; s/"$//')
          cat "$p" > `+shellQuote(filepath.Join(out, "system"))+`
          ;;
      esac
      shift
      ;;
  esac
  shift
done
printf '{"type":"thread.started","thread_id":"t-1"}\n'
printf '{"type":"item.completed","item":{"id":"item_0","type":"error","message":"Ignoring malformed agent role definition"}}\n'
printf '{"type":"turn.started"}\n'
printf '{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"codex-pong"}}\n'
printf '{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":400,"cache_write_input_tokens":50,"output_tokens":12}}\n'
`)
	got, err := runModel(t, proto.ModelRunParams{
		Launcher: "codex", Model: "gpt-6.1-terra", Effort: "ultra", System: "be brief", Prompt: "ping codex",
	})
	wantUsage := aiwire.Usage{InputTokens: 600, OutputTokens: 12, CacheReadInputTokens: 400, CacheCreationInputTokens: 50}
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "codex-pong" || a.Model != "gpt-6.1-terra" || a.Usage != wantUsage {
		t.Fatalf("answer: %#v %v", got, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(out, name))
		return strings.TrimSpace(string(b))
	}
	args := "\n" + read("args") + "\n"
	for _, want := range []string{
		"\nexec\n",
		"\n--ephemeral\n",
		"\n--ignore-user-config\n",
		"\n--ignore-rules\n",
		"\n--skip-git-repo-check\n",
		"\n-s\nread-only\n",
		"\n--json\n",
		"\n-c\napproval_policy=\"never\"\n",
		"\n-c\nmcp_servers={}\n",
		"\n-c\nweb_search=\"disabled\"\n",
		"\n-c\nagents.max_depth=0\n",
		"\n-c\ninclude_apply_patch_tool=false\n",
		"\n--disable\nshell_tool\n",
		"\n--disable\nunified_exec\n",
		"\n--disable\napps\n",
		"\n--disable\nplugins\n",
		"\n--disable\nhooks\n",
		"\n--disable\nmulti_agent_v2\n",
		"\n--disable\nmulti_agent\n",
		"\n--disable\nsleep_tool\n",
		"\n--disable\nview_image\n",
		"\n--disable\ngoals\n",
		"\n--disable\ncode_mode_host\n",
		"\n--disable\nbrowser_use\n",
		"\n--disable\ncomputer_use\n",
		"\n--disable\nimage_generation\n",
		"\n--disable\nmemories\n",
		"\n-m\ngpt-6.1-terra\n",
		"\n-c\nmodel_reasoning_effort=\"ultra\"\n",
		"\n-\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("codex args lack %q: %q", want, args)
		}
	}
	if read("stdin") != "ping codex" || read("system") != "be brief" {
		t.Errorf("stdin %q, system %q", read("stdin"), read("system"))
	}
	if cwd := read("cwd"); !strings.Contains(cwd, launcherCheckDir) {
		t.Errorf("ran in %q", cwd)
	} else if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("its directory was left behind: %v", err)
	}

	// Logged-out 401 detection.
	fakeModelRunCLIHome(t, "codex", "codex", `printf '{"type":"turn.failed","error":{"message":"unexpected status 401 Unauthorized: Missing bearer or basic authentication in header"}}\n'; exit 1`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "codex", Prompt: "ping"}); runCode(err) != proto.CodeModelRunLoggedOut {
		t.Fatalf("codex logged out: %v", err)
	}

	// Bad model / API error surfaced from turn.failed.
	fakeModelRunCLIHome(t, "codex", "codex", `printf '{"type":"turn.failed","error":{"message":"The model bad-x is not supported"}}\n'; exit 1`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "codex", Model: "bad-x", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("codex bad model: %v", err)
	}

	// Fail closed when an older codex binary lacks required isolation flags (never retry without flags).
	fakeModelRunCLIHome(t, "codex", "codex", `echo "error: unexpected argument '--ignore-user-config' found" >&2; exit 2`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "codex", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "does not support required isolation flags") {
		t.Fatalf("codex fail closed: %v", err)
	}

	// Timeout returns CodeModelRunTimeout.
	fakeModelRunCLIHome(t, "codex", "codex", "sleep 5\n")
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "codex", Prompt: "ping", TimeoutMS: 300}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("codex timeout: %v", err)
	}
}

// model.run executes Antigravity (`agy`) with a zero-tool custom agent, strict/deny settings, isolated HOME (no global
// hooks/MCP and zero session persistence into the real ~/.gemini/antigravity-cli), and NDJSON stdin/stdout streaming.
func TestModelRunAnswersWithIsolatedAgy(t *testing.T) {
	out := t.TempDir()
	home := fakeModelRunCLIHome(t, "agy", "agy", `[ "$1" = acp ] && exit 2
printf '%s\n' "$@" > `+shellQuote(filepath.Join(out, "args"))+`
cat > `+shellQuote(filepath.Join(out, "stdin"))+`
pwd > `+shellQuote(filepath.Join(out, "cwd"))+`
printf '%s\n' "$HOME" > `+shellQuote(filepath.Join(out, "home"))+`
cat .agents/agents/persona-model-run/agent.md > `+shellQuote(filepath.Join(out, "agent.md"))+`
while [ $# -gt 0 ]; do
  [ "$1" = --settings ] && cat "$2" > `+shellQuote(filepath.Join(out, "settings.json"))+`
  shift
done
[ -L "$HOME/.gemini/antigravity-cli/antigravity-oauth-token" ] && echo "linked" > `+shellQuote(filepath.Join(out, "oauth-link"))+`
printf '{"event":"init","conversation_id":"c-1","init":{"model":"gemini-4-hydrogen-low"}}\n'
printf '{"event":"result","result":{"conversation_id":"c-1","status":"SUCCESS","response":"agy-pong\\n","num_turns":1,"usage":{"input_tokens":250,"output_tokens":15,"cache_read_tokens":1000}}}\n'
`)
	// Seed a real oauth token and safe modelProvider in the user's real agy data root, plus a hook in global config.
	realAgy := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(realAgy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realAgy, "antigravity-oauth-token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realAgy, "settings.json"), []byte(`{"modelProvider":"gemini","toolPermission":"always-proceed","permissions":{"allow":["command(*)"]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := runModel(t, proto.ModelRunParams{
		Launcher: "agy", Model: "gemini-4-hydrogen-low", Effort: "low", System: "stay concise", Prompt: "ping agy",
	})
	wantUsage := aiwire.Usage{InputTokens: 250, OutputTokens: 15, CacheReadInputTokens: 1000}
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "agy-pong" || a.Model != "gemini-4-hydrogen-low" || a.Usage != wantUsage {
		t.Fatalf("answer: %#v %v", got, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(out, name))
		return strings.TrimSpace(string(b))
	}
	args := "\n" + read("args") + "\n"
	for _, want := range []string{
		"\n--agent\npersona-model-run\n",
		"\n--input-format\nstream-json\n",
		"\n--output-format\nstream-json\n",
		"\n--disable-slash-commands\n",
		"\n--settings\n",
		"\n--model\ngemini-4-hydrogen-low\n",
		"\n--effort\nlow\n",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("agy args lack %q: %q", want, args)
		}
	}
	agentMD := read("agent.md")
	for _, want := range []string{"tools: []", "commandExecutionPolicy: off", "mcpServers: []", "skills: []", "plugins: []", "subagent: false", "stay concise"} {
		if !strings.Contains(agentMD, want) {
			t.Errorf("agent.md lacks %q: %q", want, agentMD)
		}
	}
	settings := read("settings.json")
	for _, want := range []string{
		`"toolPermission":"strict"`,
		`"allowNonWorkspaceAccess":false`,
		`"disableSlashCommands":true`,
		`"modelProvider":"gemini"`,
		`"command(*)"`,
		`"read_file(*)"`,
		`"write_file(*)"`,
		`"read_url(*)"`,
		`"execute_url(*)"`,
		`"mcp(*)"`,
		`"unsandboxed(*)"`,
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.json lacks %q: %q", want, settings)
		}
	}
	if strings.Contains(settings, "always-proceed") {
		t.Errorf("settings.json leaked user always-proceed: %q", settings)
	}
	if !strings.Contains(read("stdin"), `"content":"ping agy"`) {
		t.Errorf("stdin: %q", read("stdin"))
	}
	if isoHome := read("home"); isoHome == home || !strings.Contains(isoHome, launcherCheckDir) {
		t.Errorf("expected isolated HOME under %s, got %q (real %q)", launcherCheckDir, isoHome, home)
	}
	if read("oauth-link") != "linked" {
		t.Errorf("oauth token was not symlinked into isolated HOME")
	}

	// Fast logged-out cancellation when agy starts waiting 60s for interactive OAuth callback.
	fakeModelRunCLIHome(t, "agy", "agy", `echo "Authentication required. Please visit the URL to log in:"; echo "Waiting for authentication (timeout 60s)..."; sleep 30`)
	start := time.Now()
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "ping", TimeoutMS: 5000}); runCode(err) != proto.CodeModelRunLoggedOut {
		t.Fatalf("agy fast logged out: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("agy logged-out did not cancel early: took %s", elapsed)
	}

	// Bad model error envelope.
	fakeModelRunCLIHome(t, "agy", "agy", `printf '{"conversation_id":"","status":"ERROR","response":"","error":"invalid model selection: unknown model"}\n'; exit 1`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Model: "bad-m", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "invalid model selection") {
		t.Fatalf("agy bad model: %v", err)
	}

	// Fail closed when an older agy CLI rejects isolation flags.
	fakeModelRunCLIHome(t, "agy", "agy", `echo "flag provided but not defined: -settings" >&2; exit 2`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "does not support required isolation flags") {
		t.Fatalf("agy fail closed: %v", err)
	}

	// Timeout returns CodeModelRunTimeout.
	fakeModelRunCLIHome(t, "agy", "agy", "sleep 5\n")
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "ping", TimeoutMS: 300}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("agy timeout: %v", err)
	}
}

// Unsupported agents (e.g. omp) are still rejected fail-closed by model.run.
func TestModelRunRejectsUnsupportedAgent(t *testing.T) {
	fakeModelRunCLIHome(t, "omp", "omp", "echo should-not-run\n")
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "omp", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "can't answer these requests") {
		t.Fatalf("expected unsupported agent refusal, got: %v", err)
	}
}

// Regression test (#12043 P2): structured NDJSON stdout events whose model text contains "authentication required" or
// "waiting for authentication" must never trigger early interactive-auth cancellation; once a complete SUCCESS result
// has been emitted, it also wins deterministically even if a subsequent non-JSON diagnostic triggers early auth cancel.
func TestModelRunAgyIgnoresAuthPhrasesInStructuredOutput(t *testing.T) {
	// Phrase in both step_update and final result response.
	fakeModelRunCLIHome(t, "agy", "agy", `printf '{"event":"init","conversation_id":"c-auth","init":{"model":"gemini-4-hydrogen-low"}}\n'
printf '{"event":"step_update","step":{"text":"Explaining HTTP 401: waiting for authentication before retry"}}\n'
printf '{"event":"result","result":{"conversation_id":"c-auth","status":"SUCCESS","response":"HTTP 401 means authentication required; client is waiting for authentication.","num_turns":1,"usage":{"input_tokens":10,"output_tokens":12}}}\n'
`)
	got, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "explain 401"})
	wantText := "HTTP 401 means authentication required; client is waiting for authentication."
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != wantText {
		t.Fatalf("structured auth phrases failed: %#v %v", got, err)
	}

	// Deterministic result-before-loggedOutFast check (#12120 review 5998639344): emit complete SUCCESS before the
	// plain-text auth diagnostic so the result is guaranteed buffered before early-auth cancellation kills the process;
	// trailing sleep proves early-auth cancel actually fired (without hitting TimeoutMS) while SUCCESS still wins.
	fakeModelRunCLIHome(t, "agy", "agy", `printf '{"event":"init","conversation_id":"c-auth-2","init":{"model":"gemini-4-hydrogen-low"}}\n'
printf '{"event":"result","result":{"conversation_id":"c-auth-2","status":"SUCCESS","response":"all good","num_turns":1}}\n'
echo "note: waiting for authentication cache refresh" >&2
sleep 5
`)
	got, err = runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "ping", TimeoutMS: 2000})
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "all good" {
		t.Fatalf("complete SUCCESS before early-auth diagnostic failed: %#v %v", got, err)
	}
}

// A timed-out agy process that emits a complete SUCCESS event and then hangs past TimeoutMS must still return
// CodeModelRunTimeout rather than accepting the stale output (#12120 P2).
func TestModelRunAgyTimeoutAfterSuccessReturnsTimeout(t *testing.T) {
	fakeModelRunCLIHome(t, "agy", "agy", `printf '{"event":"init","conversation_id":"c-to","init":{"model":"gemini-4-hydrogen-low"}}\n'
printf '{"event":"result","result":{"conversation_id":"c-to","status":"SUCCESS","response":"too slow","num_turns":1}}\n'
sleep 5
`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "agy", Prompt: "ping", TimeoutMS: 200}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("expected timeout after SUCCESS+sleep, got: %v", err)
	}
}

func TestModelRunAgyFirstCancellationDeterminesOutcome(t *testing.T) {
	sh, err := exec.LookPath(loginShellProgram())
	if err != nil {
		t.Skip("no supported launcher shell")
	}
	for _, tc := range []struct {
		name        string
		parentFirst bool
		success     bool
		wantCode    string
	}{
		{"auth before deadline preserves answer", false, true, ""},
		{"auth before deadline reports logged out", false, false, proto.CodeModelRunLoggedOut},
		{"deadline before auth rejects answer", true, true, proto.CodeModelRunTimeout},
		{"deadline before auth reports timeout", true, false, proto.CodeModelRunTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent, cancelParent := context.WithCancelCause(t.Context())
			defer cancelParent(nil)
			ctx, cancel := context.WithCancelCause(parent)
			defer cancel(nil)
			script := "echo 'Authentication required'"
			if tc.success {
				script = `echo '{"event":"result","result":{"status":"SUCCESS","response":"ok"}}'` + "\n" + script
			}
			cmd := exec.CommandContext(ctx, sh, "-c", script)
			killGroupOnCancel(cmd)
			msysCmdLine(cmd)
			// Deliver both causes at the auth prompt, before Wait and the process-group sweep.
			onAuth := func(cause error) {
				if tc.parentFirst {
					cancelParent(context.DeadlineExceeded)
				}
				cancel(cause)
				cancelParent(context.DeadlineExceeded)
			}
			timeout := &rpc.Error{Code: proto.CodeModelRunTimeout}
			answer, err := runAgyModelCommand(ctx, onAuth, cmd, proto.ModelRunParams{Launcher: "agy"}, timeout)
			if context.Cause(parent) != context.DeadlineExceeded || runCode(err) != tc.wantCode ||
				(tc.wantCode == "" && (err != nil || answer.Text != "ok")) {
				t.Fatalf("parent=%v answer=%q code=%q (want %q) err=%v", context.Cause(parent), answer.Text, runCode(err), tc.wantCode, err)
			}
		})
	}
}

func TestModelRunAgyDeadlineReleasesInheritedOutput(t *testing.T) {
	sh, err := exec.LookPath(loginShellProgram())
	if err != nil {
		t.Skip("no supported launcher shell")
	}
	gate, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer gate.Close()
	defer release.Close()
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	setupGuard := time.AfterFunc(15*time.Second, func() {
		cancel(context.DeadlineExceeded)
		_ = release.Close()
	})
	defer setupGuard.Stop()
	// Cancel just the launcher to deterministically leave the child holding stdout through the deadline.
	// Save stdin on fd 3 before backgrounding so the child waits on the test's pipe.
	cmd := exec.CommandContext(ctx, sh, "-c", `exec 3<&0; { echo holding; read -r line <&3; } & wait`)
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	cmd.Stdin = gate
	var watchdog *time.Timer
	out, _ := runWithEarlyAuthDetect(ctx, cancel, cmd, func(line string) bool {
		if line == "holding" && setupGuard.Stop() {
			// Start the cancellation and the regression guard only once the child owns stdout.
			timer := time.AfterFunc(20*time.Millisecond, func() { cancel(context.DeadlineExceeded) })
			t.Cleanup(func() { timer.Stop() })
			watchdog = time.AfterFunc(3*time.Second, func() { _ = release.Close() })
		}
		return false
	})
	if watchdog == nil {
		t.Fatalf("child did not report holding stdout: %q", out)
	}
	if !watchdog.Stop() {
		t.Fatal("output scanner waited for the child to close its inherited stdout")
	}
	if context.Cause(ctx) != context.DeadlineExceeded {
		t.Fatalf("deadline with live output writer: cause=%v", context.Cause(ctx))
	}
}

// Runtime provenance (#12043 P1): only the verified built-in Antigravity companion is admitted for runtime-backed
// model.run; external agy runtimes and other runtimes advertising agy or codex launchers are refused and never fall
// back to findAgyExecutable() or local CLI logins.
func TestModelRunRuntimeProvenance(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ranFile := filepath.Join(t.TempDir(), "ran")
	fakeAgyBin := filepath.Join(binDir, "agy")
	script := "#!/bin/sh\necho ran >> " + shellQuote(ranFile) + "\n" +
		`printf '{"event":"init","conversation_id":"c-rt","init":{"model":"gemini-4-hydrogen-low"}}'
printf '\n'
printf '{"event":"result","result":{"conversation_id":"c-rt","status":"SUCCESS","response":"builtin-agy-ok","num_turns":1,"usage":{"input_tokens":5,"output_tokens":3}}}'
printf '\n'
`
	if err := os.WriteFile(fakeAgyBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export PATH="+shellQuote(binDir)+":$PATH\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/zsh")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	oldFind := findAgyExecutable
	findAgyExecutable = func() string { return fakeAgyBin }
	t.Cleanup(func() { findAgyExecutable = oldFind })

	// 1. Verified built-in Antigravity companion is admitted and sets BuiltinCompanion=true.
	t.Run("builtin Antigravity companion admitted", func(t *testing.T) {
		withShellLaunchers(t)
		launcherCache.Lock()
		launcherCache.list = []proto.Launcher{{Name: "agy", Source: "zshrc", Agent: "agy", RuntimeFallback: true}}
		launcherCache.Unlock()

		n, _ := runtimeNode(t)
		n.home = home
		const builtinInst = "builtin-agy-test-1"
		n.runtimeHost.agy.mu.Lock()
		n.runtimeHost.agy.instance = builtinInst
		n.runtimeHost.agy.bin = fakeAgyBin
		n.runtimeHost.agy.mu.Unlock()

		dialRuntime(t, n, map[string]any{
			"runtime":  "agy",
			"agent":    "agy",
			"instance": builtinInst,
			"launcher": "agy",
			"caps":     map[string]any{"start": true},
		})
		ls := n.runtimes.launchers()
		if len(ls) != 1 || ls[0].Name != "agy" || !ls[0].BuiltinCompanion {
			t.Fatalf("expected BuiltinCompanion=true for built-in agy, got %+v", ls)
		}
		got, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "ping"})
		if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "builtin-agy-ok" {
			t.Fatalf("builtin agy model.run: %#v %v", got, err)
		}
	})

	// 2. External agy runtime (different instance) is refused even when local agy exists on PATH, and sets BuiltinCompanion=false.
	t.Run("external agy runtime refused without invoking local agy", func(t *testing.T) {
		_ = os.Remove(ranFile)
		withShellLaunchers(t)
		launcherCache.Lock()
		launcherCache.list = []proto.Launcher{{Name: "agy", Source: "zshrc", Agent: "agy", RuntimeFallback: true}}
		launcherCache.Unlock()

		n, _ := runtimeNode(t)
		n.home = home
		dialRuntime(t, n, map[string]any{
			"runtime":  "agy",
			"name":     "External Agy",
			"agent":    "agy",
			"instance": "external-agy-instance",
			"launcher": "agy",
			"caps":     map[string]any{"start": true},
		})
		ls := n.runtimes.launchers()
		if len(ls) != 1 || ls[0].Name != "agy" || ls[0].BuiltinCompanion {
			t.Fatalf("expected BuiltinCompanion=false for external agy, got %+v", ls)
		}
		if _, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "can't answer these requests") {
			t.Fatalf("expected external agy refusal, got %v", err)
		}
		if _, err := os.Stat(ranFile); !os.IsNotExist(err) {
			t.Fatal("external agy runtime unexpectedly invoked local findAgyExecutable()")
		}
	})

	// 3. Another runtime advertising agy or codex launchers is refused and sets BuiltinCompanion=false.
	t.Run("other runtime advertising agy and codex launchers refused", func(t *testing.T) {
		_ = os.Remove(ranFile)
		withShellLaunchers(t)
		n, _ := runtimeNode(t)
		n.home = home
		dialRuntime(t, n, map[string]any{
			"runtime": "other-rt",
			"name":    "Other Runtime",
			"launchers": []map[string]any{
				{"name": "rt-agy", "agent": "agy"},
				{"name": "rt-codex", "agent": "codex"},
			},
			"caps": map[string]any{"start": true},
		})
		for _, l := range n.runtimes.launchers() {
			if l.BuiltinCompanion {
				t.Fatalf("non-builtin runtime launcher marked BuiltinCompanion: %+v", l)
			}
		}
		for _, name := range []string{"rt-agy", "rt-codex"} {
			if _, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: name, Prompt: "ping"}); err == nil || !strings.Contains(err.Error(), "can't answer these requests") {
				t.Fatalf("expected refusal for runtime launcher %q, got %v", name, err)
			}
		}
		if _, err := os.Stat(ranFile); !os.IsNotExist(err) {
			t.Fatal("other runtime agy launcher unexpectedly invoked local findAgyExecutable()")
		}
	})
}

// A run waits for one of the computer's slots within its own time: with every slot taken it times out unrun.
func TestModelRunWaitsForASlot(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeClaudeHome(t, "touch "+shellQuote(ran)+"\n")
	for range cap(modelRunSlots) {
		modelRunSlots <- struct{}{}
	}
	defer func() {
		for range cap(modelRunSlots) {
			<-modelRunSlots
		}
	}()
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping", TimeoutMS: 200}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("busy: %v", err)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("ran without a slot")
	}
}
