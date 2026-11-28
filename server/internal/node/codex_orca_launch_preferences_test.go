package node

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

const orcaCodexPreferencePrompt = "hello"

type orcaCodexPreferenceFixture struct {
	n    *Node
	o    *orcarttest.Fake
	cwd  string
	home string

	mu            sync.Mutex
	argv          [][]string
	handlerErrors []error
}

func newOrcaCodexPreferenceFixture(t *testing.T, launchers map[string][]string) *orcaCodexPreferenceFixture {
	t.Helper()
	withCodexLaunchers(t, launchers)
	oldWait := codexStartWait
	codexStartWait = 5 * time.Second
	t.Cleanup(func() { codexStartWait = oldWait })

	n, _, o, _, home := orcaNode(t)
	n.shellSettled = true
	cwd := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", cwd, "refs/heads/main"))
	f := &orcaCodexPreferenceFixture{n: n, o: o, cwd: cwd, home: home}
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		f.recordError(f.capture(text))
		f.recordError(f.completeRollout())
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	return f
}

func (f *orcaCodexPreferenceFixture) recordError(err error) {
	if err == nil {
		return
	}
	f.mu.Lock()
	f.handlerErrors = append(f.handlerErrors, err)
	f.mu.Unlock()
}

func (f *orcaCodexPreferenceFixture) capture(text string) error {
	_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
	cmd, ok := startFileCommand(line)
	if !ok {
		return fmt.Errorf("typed command %q has no start file", text)
	}
	match := startFileRE.FindStringSubmatch(cmd)
	if match == nil {
		return fmt.Errorf("start command %q is not codex-start", cmd)
	}
	b, err := os.ReadFile(match[1])
	if err != nil {
		return fmt.Errorf("read Codex start file: %w", err)
	}
	var file codexStartFile
	if err := json.Unmarshal(b, &file); err != nil {
		return fmt.Errorf("decode Codex start file: %w", err)
	}
	f.mu.Lock()
	f.argv = append(f.argv, slices.Clone(file.Argv))
	f.mu.Unlock()
	return nil
}

func (f *orcaCodexPreferenceFixture) completeRollout() error {
	now := time.Now()
	dir := filepath.Join(f.home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("make fake Codex rollout directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(f.cwd)
	if err != nil {
		return fmt.Errorf("resolve fake Codex cwd: %w", err)
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	meta, err := json.Marshal(map[string]any{"timestamp": stamp, "type": "session_meta", "payload": map[string]any{"id": startThread, "cwd": resolved, "cli_version": startRolloutVersion}})
	if err != nil {
		return fmt.Errorf("encode fake Codex metadata: %w", err)
	}
	user, err := json.Marshal(map[string]any{"timestamp": stamp, "type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{"type": "UserMessage", "id": "u1", "content": []any{map[string]any{"type": "text", "text": orcaCodexPreferencePrompt}}}}})
	if err != nil {
		return fmt.Errorf("encode fake Codex prompt: %w", err)
	}
	path := filepath.Join(dir, "rollout-"+startThread+".jsonl")
	if err := os.WriteFile(path, []byte(string(meta)+"\n"+string(user)+"\n"), 0o600); err != nil {
		return fmt.Errorf("write fake Codex rollout: %w", err)
	}
	return nil
}

func (f *orcaCodexPreferenceFixture) start(t *testing.T, extra map[string]any) error {
	t.Helper()
	params := map[string]any{"cwd": f.cwd, "launcher": "codex", "prompt": orcaCodexPreferencePrompt}
	for key, value := range extra {
		params[key] = value
	}
	_, err := request(t, f.n, "session.start", params)
	assertNoOrcaSettingsMutations(t, f.o)
	f.mu.Lock()
	errs := slices.Clone(f.handlerErrors)
	f.mu.Unlock()
	if len(errs) != 0 {
		t.Fatalf("fake Orca terminal handler: %v", errs)
	}
	return err
}

func (f *orcaCodexPreferenceFixture) requireArgv(t *testing.T, want []string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.argv) != 1 {
		t.Fatalf("captured %d Codex start files, want one: %q", len(f.argv), f.argv)
	}
	if !slices.Equal(f.argv[0], want) {
		t.Errorf("Codex argv = %q, want %q", f.argv[0], want)
	}
}

func orcaCodexPreferenceSettings(args string) map[string]any {
	return map[string]any{
		"agentDefaultArgs":  map[string]string{"codex": args},
		"agentCmdOverrides": map[string]string{},
		"environmentNames":  map[string]any{},
	}
}

func orcaCodexPreferenceAnswer(settings map[string]any) map[string]any {
	return map[string]any{"settings": settings}
}

func (f *orcaCodexPreferenceFixture) answerPreferences(answer any) {
	f.o.Handle("settings.getAgentLaunch", func(map[string]any) any { return answer })
}

func assertNoOrcaSettingsMutations(t *testing.T, o *orcarttest.Fake) {
	t.Helper()
	for _, method := range []string{"settings.mutate", "settings.mutateAgentLaunch", "settings.update"} {
		if got := o.Count(method); got != 0 {
			t.Errorf("%s calls = %d, want zero", method, got)
		}
	}
}

func TestOrcaCodexLaunchPreferencesSupplyPermissionModelAndEffort(t *testing.T) {
	f := newOrcaCodexPreferenceFixture(t, map[string][]string{})
	f.answerPreferences(orcaCodexPreferenceAnswer(orcaCodexPreferenceSettings(
		"--sandbox workspace-write --ask-for-approval on-request --model orca-model -c model_reasoning_effort=low",
	)))
	if err := f.start(t, nil); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	f.requireArgv(t, []string{
		"codex", "--sandbox", "workspace-write", "--ask-for-approval", "on-request",
		"--model", "orca-model", "-c", "model_reasoning_effort=low",
	})
	if got := f.o.Count("settings.getAgentLaunch"); got != 1 {
		t.Errorf("settings.getAgentLaunch calls = %d, want one", got)
	}
}

func TestOrcaCodexLaunchPreferencesPreserveExplicitModelAndEffortPrecedence(t *testing.T) {
	f := newOrcaCodexPreferenceFixture(t, map[string][]string{})
	f.answerPreferences(orcaCodexPreferenceAnswer(orcaCodexPreferenceSettings(
		"--sandbox workspace-write --ask-for-approval on-request --model orca-model -c model_reasoning_effort=low",
	)))
	if err := f.start(t, map[string]any{"model": "persona-model", "effort": "high"}); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	f.requireArgv(t, []string{
		"codex", "--sandbox", "workspace-write", "--ask-for-approval", "on-request",
		"-c", "model_reasoning_effort=low", "--model", "persona-model", "-c", "model_reasoning_effort=high",
	})
	if got := f.o.Count("settings.getAgentLaunch"); got != 1 {
		t.Errorf("settings.getAgentLaunch calls = %d, want one", got)
	}
}

func TestOrcaCodexLaunchPreferencesParseLiteralQuoting(t *testing.T) {
	f := newOrcaCodexPreferenceFixture(t, map[string][]string{})
	f.answerPreferences(orcaCodexPreferenceAnswer(orcaCodexPreferenceSettings(
		`--sandbox workspace-write --ask-for-approval on-request --model 'orca model' -c "model_reasoning_effort=low" -c 'literal_text="two words; still literal"' --add-dir 'directory with spaces'`,
	)))
	if err := f.start(t, nil); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	f.requireArgv(t, []string{
		"codex", "--sandbox", "workspace-write", "--ask-for-approval", "on-request",
		"--model", "orca model", "-c", "model_reasoning_effort=low",
		"-c", `literal_text="two words; still literal"`, "--add-dir", "directory with spaces",
	})
}

func TestOrcaCodexLaunchPreferencesBypassConfiguredLaunchersAndExplicitModes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		launchers map[string][]string
		launcher  string
		mode      string
		want      []string
	}{
		{
			name:      "alias",
			launchers: map[string][]string{"cx": {"codex", "--full-auto"}},
			launcher:  "cx",
			want:      []string{"codex", "--full-auto"},
		},
		{
			name:      "configured codex launcher",
			launchers: map[string][]string{"codex": {"codex", "--model", "configured-model"}},
			want:      []string{"codex", "--model", "configured-model"},
		},
		{
			name:      "configured no-op codex launcher",
			launchers: map[string][]string{"codex": {"codex"}},
			want:      []string{"codex"},
		},
		{
			name: "explicit default mode",
			mode: "default",
			want: []string{"codex"},
		},
		{
			name: "explicit plan mode",
			mode: "plan",
			want: []string{"codex", "--sandbox", "read-only", "--ask-for-approval", "never"},
		},
		{
			name: "explicit bypass mode",
			mode: "bypassPermissions",
			want: []string{"codex", "--dangerously-bypass-approvals-and-sandbox"},
		},
		{
			name: "explicit accept edits mode",
			mode: "acceptEdits",
			want: []string{"codex"},
		},
		{
			name:      "alias with explicit bypass mode",
			launchers: map[string][]string{"cx": {"codex", "--model", "alias-model"}},
			launcher:  "cx",
			mode:      "bypassPermissions",
			want:      []string{"codex", "--model", "alias-model", "--dangerously-bypass-approvals-and-sandbox"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launchers := tc.launchers
			if launchers == nil {
				launchers = map[string][]string{}
			}
			f := newOrcaCodexPreferenceFixture(t, launchers)
			f.answerPreferences(orcaCodexPreferenceAnswer(orcaCodexPreferenceSettings(
				"--sandbox read-only --model orca-model -c model_reasoning_effort=low",
			)))
			extra := map[string]any{"launcher": tc.launcher}
			if tc.mode != "" {
				extra["permission_mode"] = tc.mode
			}
			if tc.launcher == "" {
				extra["launcher"] = "codex"
			}
			if err := f.start(t, extra); err != nil {
				t.Fatalf("session.start: %v", err)
			}
			f.requireArgv(t, tc.want)
			if got := f.o.Count("settings.getAgentLaunch"); got != 0 {
				t.Errorf("settings.getAgentLaunch calls = %d, want zero", got)
			}
		})
	}
}

func TestOrcaCodexLaunchPreferencesRefuseUnsafeOrUnavailableLaunches(t *testing.T) {
	const safeArgs = "--sandbox workspace-write --ask-for-approval on-request"
	for _, tc := range []struct {
		name        string
		reason      string // the refusal's data reason, the hub's words (#17057); "" is shell
		args        string
		answer      any
		command     string
		environment []string
	}{
		{name: "preferences method unavailable", reason: "unreadable", answer: orcarttest.Fail{Code: "method_not_found"}},
		{name: "preferences runtime unavailable", reason: "unreadable", answer: orcarttest.Fail{Code: "runtime_unavailable"}},
		{name: "preferences missing from response", reason: "unreadable", answer: map[string]any{}},
		{name: "custom command", reason: "custom", args: safeArgs, command: "/opt/custom/codex --remote unix:///tmp/codex.sock"},
		{name: "custom environment", reason: "custom", args: safeArgs, environment: []string{"OPENAI_BASE_URL"}},
		{name: "unfinished single quote", reason: "quoting", args: "--sandbox 'workspace-write"},
		{name: "unfinished double quote", reason: "quoting", args: `--model "orca-model`},
		{name: "trailing backslash", reason: "quoting", args: `--model orca-model\`},
		{name: "newline", args: "--model orca-model\n--sandbox read-only"},
		{name: "carriage return", args: "--model orca-model\r--sandbox read-only"},
		{name: "variable substitution", args: "--model $CODEX_MODEL"},
		{name: "quoted dollar sign", args: "--model '$CODEX_MODEL'"},
		{name: "braced variable substitution", args: "--model ${CODEX_MODEL}"},
		{name: "command substitution", args: "--model $(printf orca-model)"},
		{name: "backtick command substitution", args: "--model `printf orca-model`"},
		{name: "output redirection", args: "--model orca-model > /tmp/codex-model"},
		{name: "quoted greater than", args: "--model 'orca>model'"},
		{name: "input redirection", args: "--model orca-model < /tmp/codex-model"},
		{name: "and operator", args: "--model orca-model && codex --yolo"},
		{name: "or operator", args: "--model orca-model || codex --yolo"},
		{name: "sequence operator", args: "--model orca-model; codex --yolo"},
		{name: "pipeline", args: "--model orca-model | tee /tmp/codex-model"},
		{name: "background operator", args: "--model orca-model &"},
		{name: "tilde expansion", args: "--add-dir ~/project"},
		{name: "star glob", args: "--add-dir ./*"},
		{name: "question mark glob", args: "--add-dir ./file?"},
		{name: "character class glob", args: "--add-dir ./file[123]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOrcaCodexPreferenceFixture(t, map[string][]string{})
			answer := tc.answer
			if answer == nil {
				settings := orcaCodexPreferenceSettings(tc.args)
				if tc.command != "" {
					settings["agentCmdOverrides"] = map[string]string{"codex": tc.command}
				}
				if len(tc.environment) != 0 {
					settings["environmentNames"] = map[string]any{"codex": tc.environment}
				}
				answer = orcaCodexPreferenceAnswer(settings)
			}
			f.answerPreferences(answer)
			err := f.start(t, nil)
			if got := rpcCode(err); got != "codex_launch_preferences" {
				t.Errorf("session.start error code = %q, want codex_launch_preferences (error: %v)", got, err)
			}
			want := cmp.Or(tc.reason, "shell")
			if _, data := refusalOf(t, err); data["reason"] != want || data["delivered"] != "none" {
				t.Errorf("refusal data = %v, want reason %s", data, want)
			}
			if got := f.o.Count("settings.getAgentLaunch"); got != 1 {
				t.Errorf("settings.getAgentLaunch calls = %d, want one", got)
			}
			if got := f.o.Count("terminal.create"); got != 0 {
				t.Errorf("terminal.create calls = %d, want zero", got)
			}
			assertNoOrcaSettingsMutations(t, f.o)
		})
	}
}
