package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// Model and effort of a Codex on the terminal route (codex_terminal_modes.go), against a fake herdr whose Codex answers
// a typed /model the way Codex 0.169 does: thread_settings_applied in its rollout, or its refusal on the screen.

// cxModelsCache is the Codex home's models_cache.json in these tests: gpt-a takes low to high, gpt-b low, medium and
// max (low its default), gpt-c no level at all.
const cxModelsCache = `{"models":[
{"slug":"gpt-a","default_reasoning_level":"medium","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"}]},
{"slug":"gpt-b","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"max"}]},
{"slug":"gpt-c","supported_reasoning_levels":[]}]}`

// cxTerminalNode is codexNode's Codex on the terminal route, its session started by Codex version, running model at
// effort (its rollout's turn_context), with cxModelsCache in its home and an empty box on the screen.
func cxTerminalNode(t *testing.T, status, version, model, effort string) (*Node, *fakeHerdr, string) {
	t.Helper()
	oldWait, oldLook, oldRetry := codexSettingsWait, codexSettingsLook, codexStartingRetry
	codexSettingsWait, codexSettingsLook, codexStartingRetry = 300*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { codexSettingsWait, codexSettingsLook, codexStartingRetry = oldWait, oldLook, oldRetry })
	n, f, path := codexNode(t, status)
	meta := strings.Replace(cxMeta, `"cli_version":"0.163.0"`, `"cli_version":"`+version+`"`, 1)
	turn := `{"timestamp":"2026-11-01T13:40:01.000Z","type":"turn_context","payload":{"model":"` + model + `","effort":"` + effort + `"}}`
	if err := os.WriteFile(path, []byte(cxToday(meta+"\n"+turn)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(n.codexHome(), "models_cache.json"), []byte(cxModelsCache), 0o600); err != nil {
		t.Fatal(err)
	}
	f.setScreen(cxPane, cxEmpty)
	return n, f, path
}

// cxSettingsLine is the thread_settings_applied Codex writes when it applies model and effort ("" effort: null).
func cxSettingsLine(model, effort string) string {
	var e any
	if effort != "" {
		e = effort
	}
	b, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg",
		"payload": map[string]any{"type": "thread_settings_applied", "thread_id": cxID,
			"thread_settings": map[string]any{"model": model, "reasoning_effort": e, "approval_policy": "on-request"}}})
	return string(b)
}

// cxModelAnswers makes the Codex in cxPane, running model, take a typed /model as Codex 0.169 does: the settings it
// applies go to its rollout (no --reasoning: the new model's default from cxModelsCache), unless refuse returns the
// line Codex prints instead (a refusal, "" for none).
func cxModelAnswers(t *testing.T, f *fakeHerdr, path, model string, refuse func(cmd string) string) {
	t.Helper()
	var mu sync.Mutex
	defaults := map[string]string{"gpt-a": "medium", "gpt-b": "low"}
	f.after(func(c herdrCall) {
		cmd := str(c.Params["text"])
		if c.Method != "agent.prompt" || !strings.HasPrefix(cmd, "/model") {
			return
		}
		if refuse != nil {
			if line := refuse(cmd); line != "" {
				f.setScreen(cxPane, "• Done.\n\n■ "+line+"\n\n› Ask Codex to do anything\n\n  gpt-6 low · /private/tmp/project\n")
				return
			}
		}
		mu.Lock()
		defer mu.Unlock()
		effort := ""
		for _, arg := range strings.Fields(cmd)[1:] {
			if level, ok := strings.CutPrefix(arg, "--reasoning="); ok {
				effort = level
			} else {
				model, effort = arg, defaults[arg]
			}
		}
		appendLine(t, path, cxSettingsLine(model, effort))
	})
}

// cxTyped is every line the node typed into Codex.
func cxTyped(f *fakeHerdr) []string {
	var typed []string
	for _, c := range f.calls() {
		if c.Method == "agent.prompt" {
			typed = append(typed, str(c.Params["text"]))
		}
	}
	return typed
}

func cxModes(t *testing.T, n *Node, params map[string]any) error {
	t.Helper()
	params["session_id"] = cxID
	_, err := request(t, n, "session.modes", params)
	return err
}

// A terminal-route Codex that takes /model with arguments offers what an app-server thread offers: the model, and the
// efforts of the model it runs (its home's models_cache.json; Codex's whole list for a model the cache does not name;
// none for a model that takes none). An older Codex's session offers neither.
func TestCodexTerminalRow(t *testing.T) {
	for _, c := range []struct {
		name, version, model string
		canModel             bool
		efforts              []string
		effort               string
	}{
		{"listed", "0.169.1", "gpt-a", true, []string{"low", "medium", "high"}, "medium"},
		{"prerelease", "0.171.0-alpha.13", "gpt-a", true, []string{"low", "medium", "high"}, "medium"},
		{"not in the cache", "0.169.1", "gpt-z", true, codexEffortLevels, "medium"},
		{"no levels", "0.169.1", "gpt-c", true, nil, ""},
		{"older Codex", "0.168.2", "gpt-a", false, nil, "medium"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, _, _ := cxTerminalNode(t, proto.StatusIdle, c.version, c.model, "medium")
			s := paneRow(t, n, cxID)
			if got := s.Capabilities.Model != nil && *s.Capabilities.Model; got != c.canModel {
				t.Errorf("capabilities.model %v", got)
			}
			if !slices.Equal(s.Efforts, c.efforts) || s.Effort != c.effort {
				t.Errorf("efforts %v, effort %q", s.Efforts, s.Effort)
			}
		})
	}
}

func TestCodexTerminalLaunchSettings(t *testing.T) {
	n, f, path := cxTerminalNode(t, proto.StatusIdle, "0.172.0", "", "")
	if err := os.WriteFile(path, []byte(cxToday(strings.Replace(cxMeta, "0.163.0", "0.172.0", 1))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := codexHomeOf(path)
	n.state.put(cxID, sessionState{Launcher: "codex", Cwd: t.TempDir()})
	for file, content := range map[string]string{
		"config.toml":       "model = 'gpt-a'\nmodel_reasoning_effort = 'high'\nservice_tier = 'fast'\n",
		"models_cache.json": strings.Replace(cxModelsCache, `"slug":"gpt-a"`, `"slug":"gpt-a","service_tiers":[{"id":"priority"},{"id":"ultrafast"}]`, 1),
	} {
		if err := os.WriteFile(filepath.Join(home, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := paneRow(t, n, cxID)
	if s.Model != "gpt-a" || s.Effort != "high" || s.ServiceTier != "priority" || len(s.ServiceTiers) != 3 || s.ServiceTiers[2].ID != "ultrafast" {
		t.Fatalf("startup config: %q / %q / %q, tiers %+v", s.Model, s.Effort, s.ServiceTier, s.ServiceTiers)
	}
	if _, err := request(t, n, "session.speed", map[string]any{"session_id": cxID, "service_tier": "unknown"}); err == nil || len(cxTyped(f)) != 0 {
		t.Fatalf("unadvertised speed: %v", err)
	}
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		tier := map[string]string{"/model --speed=fast": "priority", "/model --speed=ultrafast": "ultrafast", "/model --speed=standard": "default"}[str(c.Params["text"])]
		if tier != "" {
			appendLine(t, path, strings.Replace(cxSettingsLine("gpt-a", "high"), `"reasoning_effort":`, `"service_tier":"`+tier+`","reasoning_effort":`, 1))
		}
	})
	for _, tier := range []string{"ultrafast", "default", "priority"} {
		if _, err := request(t, n, "session.speed", map[string]any{"session_id": cxID, "service_tier": tier}); err != nil {
			t.Fatal(err)
		}
		if s := paneRow(t, n, cxID); s.ServiceTier != tier || s.Effort != "high" {
			t.Fatalf("confirmed speed: %q / %q", s.ServiceTier, s.Effort)
		}
	}
	appendLine(t, path, cxSettingsLine("gpt-c", ""))
	n.forgetMeta(path)
	if s := paneRow(t, n, cxID); s.Model != "gpt-c" || s.Effort != "" || len(s.ServiceTiers) != 0 {
		t.Fatalf("recorded settings: %q / %q, tiers %+v", s.Model, s.Effort, s.ServiceTiers)
	}
}

func TestCodexTerminalLaunchOverride(t *testing.T) {
	n, _, path := cxTerminalNode(t, proto.StatusIdle, "0.172.0", "", "")
	withCodexLaunchers(t, map[string][]string{"demo": {"codex", "-m", "gpt-b", "-c", "model_reasoning_effort=max"}})
	n.state.put(cxID, sessionState{Launcher: "demo", Cwd: t.TempDir(), CodexStartTier: "default"})
	terminal := codexTerminal{home: codexHomeOf(path)}
	if got := n.codexTerminalSettingsOf(cxID, terminal, quickMeta{}); got != (codexTerminalSettings{model: "gpt-b", effort: "max", tier: "default"}) {
		t.Fatalf("launcher settings: %+v", got)
	}
	n.state.put(cxID, sessionState{Launcher: "demo", Cwd: t.TempDir(), Model: "gpt-a", ModelEffort: "low", CodexStartTier: "priority"})
	if got := n.codexTerminalSettingsOf(cxID, terminal, quickMeta{}); got != (codexTerminalSettings{model: "gpt-a", effort: "low", tier: "priority"}) {
		t.Fatalf("explicit launch settings: %+v", got)
	}
}

// The phone's pick goes in as one /model line, and is made once the rollout records it: an effort alone; a model,
// keeping the effort the new model takes (Codex would put it on its default); a model that does not take it, which
// Codex puts on its default; both at once. Mid-turn too. The row shows the new values from the record.
func TestCodexTerminalModesTyped(t *testing.T) {
	for _, c := range []struct {
		name, status, effortNow string
		params                  map[string]any
		typed                   string
		model, effort           string
	}{
		{"effort", proto.StatusIdle, "medium", map[string]any{"effort": "high"}, "/model --reasoning=high", "gpt-a", "high"},
		{"model keeps the effort", proto.StatusIdle, "medium", map[string]any{"model": "gpt-b"}, "/model gpt-b --reasoning=medium", "gpt-b", "medium"},
		{"model it does not take", proto.StatusIdle, "high", map[string]any{"model": "gpt-b"}, "/model gpt-b", "gpt-b", "low"},
		{"both", proto.StatusIdle, "medium", map[string]any{"model": "gpt-b", "effort": "max"}, "/model gpt-b --reasoning=max", "gpt-b", "max"},
		{"mid-turn", proto.StatusWorking, "medium", map[string]any{"effort": "low"}, "/model --reasoning=low", "gpt-a", "low"},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, path := cxTerminalNode(t, c.status, "0.169.1", "gpt-a", c.effortNow)
			cxModelAnswers(t, f, path, "gpt-a", nil)
			if err := cxModes(t, n, c.params); err != nil {
				t.Fatal(err)
			}
			if typed := cxTyped(f); len(typed) != 1 || typed[0] != c.typed {
				t.Fatalf("typed %q", typed)
			}
			if s := paneRow(t, n, cxID); s.Model != c.model || s.Effort != c.effort {
				t.Errorf("row %q / %q", s.Model, s.Effort)
			}
		})
	}
	t.Run("what it runs already", func(t *testing.T) {
		n, f, path := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		cxModelAnswers(t, f, path, "gpt-a", nil)
		for _, p := range []map[string]any{{"effort": "medium"}, {"model": "gpt-a"}, {"model": "gpt-a", "effort": "medium"}} {
			if err := cxModes(t, n, p); err != nil {
				t.Fatal(err)
			}
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
}

// Every refusal says why, with nothing typed when it is the node's own: a permission mode, a level the model does not
// take, an older Codex, no rollout yet, a draft in the box, a dialog on the screen, a botmux pane, a subagent. Codex's
// own refusal is passed on in its words; a Codex still starting gets one more try, and its second answer is passed on;
// a change the rollout never shows is unconfirmed, typed once.
func TestCodexTerminalModesRefused(t *testing.T) {
	t.Run("permission mode", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		if err := cxModes(t, n, map[string]any{"permission_mode": "plan", "effort": "high"}); err == nil || !strings.Contains(err.Error(), "权限模式要在电脑上改") {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("level the model does not take", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		if err := cxModes(t, n, map[string]any{"effort": "max"}); err == nil || !strings.Contains(err.Error(), "low / medium / high") {
			t.Fatalf("err %v", err)
		}
		if err := cxModes(t, n, map[string]any{"model": "gpt-c", "effort": "low"}); err == nil || !strings.Contains(err.Error(), "不支持设置思考强度") {
			t.Fatalf("a model with no levels: %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("older Codex", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		if err := cxModes(t, n, map[string]any{"effort": "high"}); refusalCode(err) != proto.CodeModesCodexVersion {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("no rollout yet", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		fresh := "01a19756-0000-7000-8000-000000000001"
		n.mu.Lock()
		n.agents[fresh] = host.Agent{Agent: "codex", PaneID: "w1:p2", AgentStatus: proto.StatusIdle}
		n.mu.Unlock()
		if _, err := request(t, n, "session.modes", map[string]any{"session_id": fresh, "effort": "high"}); refusalCode(err) != proto.CodeModesCodexNoRollout {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("draft in the box", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		f.setScreen(cxPane, cxBox("half typed"))
		if err := cxModes(t, n, map[string]any{"effort": "high"}); refusalCode(err) != screen.InputDraft {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("Codex refuses", func(t *testing.T) {
		n, f, path := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		words := "Model 'gpt-x' is unavailable. Use /model to see available models."
		cxModelAnswers(t, f, path, "gpt-a", func(string) string { return words })
		err := cxModes(t, n, map[string]any{"model": "gpt-x"})
		if refusalCode(err) != proto.CodeModesCodexRefused || err.Error() != words {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 1 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("still starting", func(t *testing.T) {
		n, f, path := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		var mu sync.Mutex
		tries := 0
		cxModelAnswers(t, f, path, "gpt-a", func(string) string {
			mu.Lock()
			defer mu.Unlock()
			if tries++; tries == 1 {
				return "Session is still starting; try /model again in a moment."
			}
			f.setScreen(cxPane, cxEmpty)
			return ""
		})
		if err := cxModes(t, n, map[string]any{"effort": "high"}); err != nil {
			t.Fatal(err)
		}
		if typed := cxTyped(f); len(typed) != 2 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("unconfirmed", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		err := cxModes(t, n, map[string]any{"effort": "high"})
		var re *rpc.Error
		if refusalCode(err) != proto.CodeModesCodexUnconfirmed || !errors.As(err, &re) || deliveredOf(re) != deliveredUnknown {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 1 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("still starting twice", func(t *testing.T) {
		// Codex prints its answer again below the first: the second try's answer is new, and passed on.
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		words := "Session is still starting; try /model again in a moment."
		var mu sync.Mutex
		shown := "• Done.\n\n"
		f.after(func(c herdrCall) {
			if c.Method != "agent.prompt" || !strings.HasPrefix(str(c.Params["text"]), "/model") {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			shown += "■ " + words + "\n\n"
			f.setScreen(cxPane, shown+"› Ask Codex to do anything\n\n  gpt-6 low · /private/tmp/project\n")
		})
		err := cxModes(t, n, map[string]any{"effort": "high"})
		if refusalCode(err) != proto.CodeModesCodexRefused || err.Error() != words {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 2 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("dialog on the screen", func(t *testing.T) {
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		f.setScreen(cxPane, screenFixture(t, "codex-0.163.0-exec-approval-120.txt"))
		if err := cxModes(t, n, map[string]any{"effort": "high"}); refusalCode(err) != screen.DialogOpen {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("botmux pane", func(t *testing.T) {
		// botmux hands what is typed over as a whole message: /model would reach the model as a turn.
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		n.mu.Lock()
		a := n.agents[cxID]
		a.PaneID = host.Botmux + ":b1"
		n.agents[cxID] = a
		n.mu.Unlock()
		if s := paneRow(t, n, cxID); s.Capabilities != nil && s.Capabilities.Model != nil && *s.Capabilities.Model || len(s.Efforts) != 0 {
			t.Fatalf("a botmux pane offers model %v, efforts %v", s.Capabilities.Model, s.Efforts)
		}
		if err := cxModes(t, n, map[string]any{"effort": "high"}); err == nil || !strings.Contains(err.Error(), "Codex / TRAE CLI 的会话不支持") {
			t.Fatalf("err %v", err)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
	t.Run("subagent", func(t *testing.T) {
		// Its parent drives it, through the app-server the parent runs on: nothing is typed into the pane.
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		n.codices = newCodexRegistry()
		home := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)
		n.codices.config.LegacyHome = home // the default home keeps the thread ids as they are
		if id := n.registerCodex(home, cxID, ""); id != cxID {
			t.Fatalf("registered as %q", id)
		}
		n.codices.remotes = map[string]*codexRemote{"daemon": {n: n, spec: proto.CodexServer{ID: "daemon", Home: home}, ready: true,
			threads: map[string]remoteThread{cxID: {id: cxID, publicID: cxID, parent: "01a19756-0000-7000-8000-0000000000aa"}}}}
		if _, _, ok := n.codexOnTerminal(cxID); ok {
			t.Fatal("a subagent counted as on the terminal route")
		}
		if err := cxModes(t, n, map[string]any{"effort": "high"}); err == nil {
			t.Fatal("a subagent's effort changed")
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q", typed)
		}
	})
}

func TestCodexModelArgsCommand(t *testing.T) {
	for _, c := range []struct{ model, effort, want string }{
		{"gpt-6.2-astra", "high", "/model gpt-6.2-astra --reasoning=high"},
		{"", "ultra", "/model --reasoning=ultra"},
		{"acme/gpt-5-pro", "", "/model acme/gpt-5-pro"},
		{"gpt-5.1", "none", "/model gpt-5.1 --reasoning=none"}, // a model's own level beyond Codex's settings list
	} {
		if got, err := codexModelArgsCommand(c.model, c.effort); err != nil || got != c.want {
			t.Errorf("%q %q: %q %v", c.model, c.effort, got, err)
		}
	}
	for _, c := range [][2]string{{"", ""}, {"gpt x", ""}, {"--reasoning=high", ""}, {"gpt-a", "x-high"}, {"gpt-a", "High"}, {"gpt-a", "high --speed=fast"}} {
		if got, err := codexModelArgsCommand(c[0], c[1]); err == nil {
			t.Errorf("%q %q made %q", c[0], c[1], got)
		}
	}
}

// Codex's refusals as 0.169 to 0.171 print them, each only when it is new on the screen (one more copy than before
// counts), at the start of its line and about what was typed.
func TestCodexModelArgsOutcome(t *testing.T) {
	box := "\n› Ask Codex to do anything\n\n  GPT-6.2-Astra medium · ~/project\n"
	for _, c := range []struct {
		name, before, after, model, effort string
		code, words                        string
	}{
		{"model", box, "■ Model 'gpt-9' is unavailable. Use /model to see available models." + box, "gpt-9", "", proto.CodeModesCodexRefused, "Model 'gpt-9' is unavailable. Use /model to see available models."},
		{"level", box, "■ Reasoning level 'ultra' is unavailable for gpt-6.2-sol. Available: low, medium." + box, "gpt-6.2-sol", "ultra", proto.CodeModesCodexRefused, "Reasoning level 'ultra' is unavailable for gpt-6.2-sol. Available: low, medium."},
		{"account list", box, "■ Your ChatGPT model list doesn't include `gpt-9`. Check the spelling or use /model to choose another model." + box, "gpt-9", "", proto.CodeModesCodexRefused, "Your ChatGPT model list doesn't include `gpt-9`. Check the spelling or use /model to choose another model."},
		{"reserve", box, "■ Model arguments are unavailable while using Reserve. Use /model to adjust effort." + box, "", "high", proto.CodeModesCodexRefused, "Model arguments are unavailable while using Reserve. Use /model to adjust effort."},
		{"starting", box, "■ Session is still starting; try /model again in a moment." + box, "", "high", "", "Session is still starting; try /model again in a moment."},
		{"changed", box, "• Model changed to gpt-6.2-astra high for this session only" + box, "", "high", "", ""},
		{"an earlier answer", "■ Model 'gpt-9' is unavailable. Use /model to see available models." + box, "■ Model 'gpt-9' is unavailable. Use /model to see available models." + box, "gpt-9", "", "", ""},
		{"busy", box, "■ '/model' is disabled while a task is in progress." + box, "", "high", proto.CodeModesCodexRefused, "'/model' is disabled while a task is in progress."},
		{"the same answer again", "■ Model 'gpt-9' is unavailable. Use /model to see available models." + box, "■ Model 'gpt-9' is unavailable. Use /model to see available models.\n\n■ Model 'gpt-9' is unavailable. Use /model to see available models." + box, "gpt-9", "", proto.CodeModesCodexRefused, "Model 'gpt-9' is unavailable. Use /model to see available models."},
		{"quoted mid-line", box, "• The tool said: Unexpected argument 'x'. Use '--reasoning=<level>' or '--speed=<tier>'." + box, "", "high", "", ""},
		{"another model", box, "■ Model 'gpt-8' is unavailable. Use /model to see available models." + box, "gpt-9", "", "", ""},
	} {
		err := codexTerminalOutcome(c.before, c.after, codexTerminalSettings{model: c.model, effort: c.effort})
		var starting codexStarting
		switch {
		case c.words == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.words != "" && (err == nil || err.Error() != c.words):
			t.Errorf("%s: %v", c.name, err)
		case c.code != "" && refusalCode(err) != c.code:
			t.Errorf("%s: code %q", c.name, refusalCode(err))
		case c.words != "" && c.code == "" && !errors.As(err, &starting):
			t.Errorf("%s: not a retry: %v", c.name, err)
		}
	}
}

// Only Codex's record written after the typing counts, and only with the values typed; a level alone matches whatever
// model, a model alone whatever level.
func TestCodexSettingsRecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	earlier := cxSettingsLine("gpt-b", "max") + "\n"
	if err := os.WriteFile(path, []byte(earlier+cxSettingsLine("gpt-a", "")+"\n"+cxSettingsLine("gpt-a", "low")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	from := int64(len(earlier))
	for _, c := range []struct {
		model, effort string
		want          bool
	}{
		{"gpt-a", "low", true}, {"", "low", true}, {"gpt-a", "", true},
		{"gpt-b", "max", false}, {"", "max", false}, {"gpt-a", "high", false},
	} {
		if got := codexSettingsRecorded(path, from, codexTerminalSettings{model: c.model, effort: c.effort}); got != c.want {
			t.Errorf("%q %q: %v", c.model, c.effort, got)
		}
	}
	if codexSettingsRecorded(filepath.Join(t.TempDir(), "none.jsonl"), 0, codexTerminalSettings{effort: "low"}) {
		t.Error("a missing rollout recorded a change")
	}
}

func TestCodexHomeOf(t *testing.T) {
	home := filepath.Join("/tmp", "h", ".codex")
	if got := codexHomeOf(filepath.Join(home, "sessions", "2026", "11", "21", "rollout-x.jsonl")); got != home {
		t.Errorf("home %q", got)
	}
	if got := codexHomeOf(filepath.Join(home, "archived_sessions", "rollout-x.jsonl")); got != "" {
		t.Errorf("an archived rollout's home %q", got)
	}
}

// resetCodexVersionTestHooks resets the process-lookup and binary-version test seams and cache between tests.
func resetCodexVersionTestHooks(t *testing.T) {
	t.Helper()
	oldExec, oldArgv, oldRun := codexProcessExecPath, codexProcessArgv, codexRunVersion
	oldTimeout, oldRetry := codexBinaryVersionTimeout, codexBinaryVersionRetry
	clearCache := func() {
		codexBinaryVersions.Lock()
		codexBinaryVersions.byPath = map[string]codexBinaryVersionEntry{}
		codexBinaryVersions.Unlock()
	}
	clearCache()
	t.Cleanup(func() {
		codexProcessExecPath, codexProcessArgv, codexRunVersion = oldExec, oldArgv, oldRun
		codexBinaryVersionTimeout, codexBinaryVersionRetry = oldTimeout, oldRetry
		clearCache()
	})
}

// writeFakeCodexBin writes an executable file at path and returns its path. On Unix it is a runnable shell script that
// records each `--version` invocation into countFile (when non-empty) and runs body; on Windows a regular placeholder
// file is written for stubbed-runner tests.
func writeFakeCodexBin(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setPaneForegroundPIDs(f *fakeHerdr, pids ...int) {
	procs := make([]any, 0, len(pids))
	for _, pid := range pids {
		procs = append(procs, map[string]any{"pid": pid, "name": "codex", "argv0": "codex"})
	}
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": procs}})
}

// Issue #13290: the running Codex binary's version decides whether `/model` takes arguments, not the rollout's first
// session_meta. A pre-0.169 session resumed by 0.169+ offers model/effort and switches them; a session whose running
// Codex is older than 0.169 is refused with modes_codex_version with nothing typed, whatever started it; an unreadable
// process or failing binary falls back to session_meta.cli_version; unknown on both sides stays refused.
func TestCodexTerminalRunningVersion(t *testing.T) {
	t.Run("pre-0.169 rollout resumed by 0.169+ switches model and effort", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, path := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		cxModelAnswers(t, f, path, "gpt-a", nil)
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
		const pid = 41001
		setPaneForegroundPIDs(f, pid)
		codexProcessExecPath = func(p int) string {
			if p == pid {
				return bin
			}
			return ""
		}
		codexRunVersion = func(_ context.Context, exe string) ([]byte, error) {
			if exe != bin {
				t.Errorf("unexpected exe %q", exe)
			}
			return []byte("codex-cli 0.169.1\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatalf("capabilities.model = %v, want true", s.Capabilities.Model)
		}
		if !slices.Equal(s.Efforts, []string{"low", "medium", "high"}) {
			t.Fatalf("efforts = %v, want [low medium high]", s.Efforts)
		}
		if err := cxModes(t, n, map[string]any{"model": "gpt-b", "effort": "max"}); err != nil {
			t.Fatalf("session.modes refused: %v", err)
		}
		if typed := cxTyped(f); len(typed) != 1 || typed[0] != "/model gpt-b --reasoning=max" {
			t.Fatalf("typed %q", typed)
		}
		if row := paneRow(t, n, cxID); row.Model != "gpt-b" || row.Effort != "max" {
			t.Fatalf("row after switch = %q / %q", row.Model, row.Effort)
		}
	})

	t.Run("pre-0.169 rollout resumed by prerelease 0.171.0-alpha.13.2", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
		const pid = 41002
		setPaneForegroundPIDs(f, pid)
		codexProcessExecPath = func(p int) string {
			if p == pid {
				return bin
			}
			return ""
		}
		codexRunVersion = func(context.Context, string) ([]byte, error) {
			return []byte("codex-cli 0.171.0-alpha.13.2\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatalf("capabilities.model = %v, want true", s.Capabilities.Model)
		}
	})

	t.Run("0.170 rollout resumed by pre-0.169 binary refused with nothing typed", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.170.0", "gpt-a", "medium")
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
		const pid = 41003
		setPaneForegroundPIDs(f, pid)
		codexProcessExecPath = func(p int) string {
			if p == pid {
				return bin
			}
			return ""
		}
		codexRunVersion = func(context.Context, string) ([]byte, error) {
			return []byte("codex-cli 0.168.2\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model != nil && *s.Capabilities.Model {
			t.Fatalf("downgraded Codex offered model control")
		}
		if len(s.Efforts) != 0 {
			t.Fatalf("downgraded Codex offered efforts %v", s.Efforts)
		}
		if err := cxModes(t, n, map[string]any{"effort": "high"}); refusalCode(err) != proto.CodeModesCodexVersion {
			t.Fatalf("err = %v, want %s", err, proto.CodeModesCodexVersion)
		}
		if typed := cxTyped(f); len(typed) != 0 {
			t.Fatalf("typed %q into downgraded Codex", typed)
		}
	})

	t.Run("binary error falls back to rollout session_meta cli_version", func(t *testing.T) {
		for _, tc := range []struct {
			name, rolloutVer string
			runOut           string
			runErr           error
			wantModel        bool
		}{
			{"exec error with 0.169 rollout", "0.169.1", "", errors.New("permission denied"), true},
			{"exec error with 0.163 rollout", "0.163.0", "", errors.New("permission denied"), false},
			{"unparseable output with 0.169 rollout", "0.169.1", "not a version", nil, true},
			{"missing version on both sides", "", "", errors.New("failed"), false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				resetCodexVersionTestHooks(t)
				n, f, _ := cxTerminalNode(t, proto.StatusIdle, tc.rolloutVer, "gpt-a", "medium")
				bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
				const pid = 41004
				setPaneForegroundPIDs(f, pid)
				codexProcessExecPath = func(p int) string {
					if p == pid {
						return bin
					}
					return ""
				}
				codexRunVersion = func(context.Context, string) ([]byte, error) {
					return []byte(tc.runOut), tc.runErr
				}
				s := paneRow(t, n, cxID)
				if got := s.Capabilities.Model != nil && *s.Capabilities.Model; got != tc.wantModel {
					t.Fatalf("capabilities.model = %v, want %v", got, tc.wantModel)
				}
				if !tc.wantModel {
					if err := cxModes(t, n, map[string]any{"effort": "high"}); refusalCode(err) != proto.CodeModesCodexVersion {
						t.Fatalf("err = %v, want %s", err, proto.CodeModesCodexVersion)
					}
				}
			})
		}
	})

	t.Run("unreadable process falls back to rollout cli_version", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		setPaneForegroundPIDs(f, 41005)
		codexProcessExecPath = func(int) string { return "" } // another user or remote pane
		called := false
		codexRunVersion = func(context.Context, string) ([]byte, error) {
			called = true
			return nil, nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatal("fallback to 0.169.1 rollout failed when process executable was unreadable")
		}
		if called {
			t.Fatal("ran --version when no executable was resolved")
		}
	})

	t.Run("ForegroundPID resolves version without pane.process_info", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
		const pid = 41006
		n.mu.Lock()
		a := n.agents[cxID]
		a.ForegroundPID = pid
		n.agents[cxID] = a
		n.mu.Unlock()
		codexProcessExecPath = func(p int) string {
			if p == pid {
				return bin
			}
			return ""
		}
		codexRunVersion = func(context.Context, string) ([]byte, error) {
			return []byte("codex-cli 0.170.0\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatal("ForegroundPID did not enable terminal model controls")
		}
		for _, c := range f.calls() {
			if c.Method == "pane.process_info" {
				t.Fatal("queried pane.process_info despite readable ForegroundPID")
			}
		}
	})

	t.Run("skips non-codex foreground binaries and prefers exact codex name", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		dir := t.TempDir()
		zshBin := writeFakeCodexBin(t, filepath.Join(dir, "zsh"), "stub")
		helperBin := writeFakeCodexBin(t, filepath.Join(dir, "codex-mcp-server"), "stub")
		codexBin := writeFakeCodexBin(t, filepath.Join(dir, "codex"), "stub")
		setPaneForegroundPIDs(f, 41010, 41011, 41012)
		codexProcessExecPath = func(p int) string {
			switch p {
			case 41010:
				return zshBin
			case 41011:
				return helperBin
			case 41012:
				return codexBin
			}
			return ""
		}
		var ran []string
		codexRunVersion = func(_ context.Context, exe string) ([]byte, error) {
			ran = append(ran, exe)
			return []byte("codex-cli 0.169.1\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatal("exact codex binary not selected")
		}
		if !slices.Equal(ran, []string{codexBin}) {
			t.Fatalf("ran --version on %v, want only [%s]", ran, codexBin)
		}
	})

	t.Run("resolves interpreter wrapper script via process argv", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.163.0", "gpt-a", "medium")
		dir := t.TempDir()
		nodeBin := writeFakeCodexBin(t, filepath.Join(dir, "node"), "stub")
		script := writeFakeCodexBin(t, filepath.Join(dir, "codex.js"), "#!/usr/bin/env node\n")
		setPaneForegroundPIDs(f, 41020)
		codexProcessExecPath = func(p int) string {
			if p == 41020 {
				return nodeBin
			}
			return ""
		}
		codexProcessArgv = func(p int) ([]string, error) {
			if p == 41020 {
				return []string{nodeBin, "--no-warnings", script, "resume"}, nil
			}
			return nil, errors.New("unknown pid")
		}
		codexRunVersion = func(_ context.Context, exe string) ([]byte, error) {
			if exe != script {
				t.Fatalf("ran --version on %q, want %q", exe, script)
			}
			return []byte("codex-cli 0.169.2\n"), nil
		}
		s := paneRow(t, n, cxID)
		if s.Capabilities.Model == nil || !*s.Capabilities.Model {
			t.Fatal("wrapper script version did not enable model control")
		}
	})

	t.Run("missing hosts falls back to rollout version without panicking", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		n, _, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
		n.hosts = nil
		if _, why, ok := n.codexOnTerminal(cxID); !ok || why != nil {
			t.Fatalf("codexOnTerminal with nil hosts: why=%v ok=%v", why, ok)
		}
	})
}

// codexBinaryVersion caches `<exe> --version` by (path, mtime, size, inode), invalidates on mtime change or atomic
// replacement, keeps separate paths independent, throttles failed probes without poisoning cancelled callers, and
// executes a real binary end-to-end on Unix.
func TestCodexVersionCache(t *testing.T) {
	t.Run("caches by path and invalidates on mtime change with identical file size", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "12345678")
		var runs atomic.Int32
		ver := "codex-cli 0.168.0\n"
		codexRunVersion = func(context.Context, string) ([]byte, error) {
			runs.Add(1)
			return []byte(ver), nil
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.168.0" {
			t.Fatalf("first read = %q, want 0.168.0", got)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.168.0" {
			t.Fatalf("cached read = %q, want 0.168.0", got)
		}
		if runs.Load() != 1 {
			t.Fatalf("ran --version %d times on unchanged file, want 1", runs.Load())
		}
		// Bump mtime while keeping size and inode identical.
		ver = "codex-cli 0.169.0\n"
		next := time.Now().Add(5 * time.Second)
		if err := os.Chtimes(bin, next, next); err != nil {
			t.Fatal(err)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.169.0" {
			t.Fatalf("after mtime change = %q, want 0.169.0", got)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.169.0" {
			t.Fatalf("re-cached read = %q, want 0.169.0", got)
		}
		if runs.Load() != 2 {
			t.Fatalf("ran --version %d times after mtime change, want 2", runs.Load())
		}
	})

	t.Run("invalidates on atomic replacement and caches distinct paths independently", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		dir := t.TempDir()
		binA := writeFakeCodexBin(t, filepath.Join(dir, "a", "codex"), "same-len")
		binB := writeFakeCodexBin(t, filepath.Join(dir, "b", "codex"), "same-len")
		stamp := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(binA, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		versions := map[string]string{binA: "codex-cli 0.168.1\n", binB: "codex-cli 0.170.0\n"}
		var runs atomic.Int32
		codexRunVersion = func(_ context.Context, exe string) ([]byte, error) {
			runs.Add(1)
			return []byte(versions[exe]), nil
		}
		if got := codexBinaryVersion(context.Background(), binA); got != "0.168.1" {
			t.Fatalf("binA = %q", got)
		}
		if got := codexBinaryVersion(context.Background(), binB); got != "0.170.0" {
			t.Fatalf("binB = %q", got)
		}
		if got := codexBinaryVersion(context.Background(), binA); got != "0.168.1" {
			t.Fatalf("binA second read = %q", got)
		}
		if got := codexBinaryVersion(context.Background(), binB); got != "0.170.0" {
			t.Fatalf("binB second read = %q", got)
		}
		if runs.Load() != 2 {
			t.Fatalf("two distinct binaries thrashed cache: %d runs, want 2", runs.Load())
		}
		// Atomically replace binA with a new file carrying the exact same size and mtime.
		tmp := writeFakeCodexBin(t, filepath.Join(dir, "a", "codex.new"), "same-len")
		if err := os.Chtimes(tmp, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, binA); err != nil {
			t.Fatal(err)
		}
		versions[binA] = "codex-cli 0.171.0\n"
		if got := codexBinaryVersion(context.Background(), binA); got != "0.171.0" {
			t.Fatalf("after atomic replace = %q, want 0.171.0", got)
		}
		if runs.Load() != 3 {
			t.Fatalf("runs = %d, want 3", runs.Load())
		}
	})

	t.Run("failed run backs off until mtime change or retry window, cancelled ctx not cached", func(t *testing.T) {
		resetCodexVersionTestHooks(t)
		codexBinaryVersionRetry = time.Minute
		bin := writeFakeCodexBin(t, filepath.Join(t.TempDir(), "codex"), "stub")
		var runs atomic.Int32
		fail := true
		codexRunVersion = func(ctx context.Context, _ string) ([]byte, error) {
			runs.Add(1)
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if fail {
				return nil, errors.New("exit status 1")
			}
			return []byte("codex-cli 0.169.5\n"), nil
		}
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if got := codexBinaryVersion(cancelled, bin); got != "" {
			t.Fatalf("cancelled ctx returned %q", got)
		}
		if runs.Load() != 0 {
			t.Fatalf("pre-cancelled ctx ran --version %d times", runs.Load())
		}
		// First real failure caches backoff so repeated list refreshes do not respawn failing binary.
		if got := codexBinaryVersion(context.Background(), bin); got != "" {
			t.Fatalf("failing binary returned %q", got)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "" {
			t.Fatalf("backed-off binary returned %q", got)
		}
		if runs.Load() != 1 {
			t.Fatalf("failing binary ran %d times during backoff, want 1", runs.Load())
		}
		// Updating the binary's mtime immediately bypasses backoff and reads the fixed binary.
		fail = false
		next := time.Now().Add(10 * time.Second)
		if err := os.Chtimes(bin, next, next); err != nil {
			t.Fatal(err)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.169.5" {
			t.Fatalf("fixed binary after mtime bump = %q, want 0.169.5", got)
		}
		if runs.Load() != 2 {
			t.Fatalf("runs = %d, want 2", runs.Load())
		}
	})

	t.Run("real executable script end-to-end with mtime invalidation and timeout fallback", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("uses POSIX shell script as fake codex binary")
		}
		resetCodexVersionTestHooks(t)
		dir := t.TempDir()
		logPath := filepath.Join(dir, "runs")
		bin := filepath.Join(dir, "codex")
		writeFakeCodexBin(t, bin, "#!/bin/sh\necho x >> "+logPath+"\necho 'codex-cli 0.168.9'\n")
		if got := codexBinaryVersion(context.Background(), bin); got != "0.168.9" {
			t.Fatalf("real script first read = %q, want 0.168.9", got)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.168.9" {
			t.Fatalf("real script cached read = %q, want 0.168.9", got)
		}
		b, err := os.ReadFile(logPath)
		if err != nil || strings.Count(string(b), "x") != 1 {
			t.Fatalf("real script ran %d times, want 1", strings.Count(string(b), "x"))
		}
		writeFakeCodexBin(t, bin, "#!/bin/sh\necho x >> "+logPath+"\necho 'codex-cli 0.169.1'\n")
		next := time.Now().Add(5 * time.Second)
		if err := os.Chtimes(bin, next, next); err != nil {
			t.Fatal(err)
		}
		if got := codexBinaryVersion(context.Background(), bin); got != "0.169.1" {
			t.Fatalf("real script upgraded read = %q, want 0.169.1", got)
		}
		b, err = os.ReadFile(logPath)
		if err != nil || strings.Count(string(b), "x") != 2 {
			t.Fatalf("real script ran %d times after upgrade, want 2", strings.Count(string(b), "x"))
		}
		// Hanging binary times out cleanly and returns "" (triggering rollout fallback).
		codexBinaryVersionTimeout = 100 * time.Millisecond
		hang := writeFakeCodexBin(t, filepath.Join(dir, "codex-hang"), "#!/bin/sh\nsleep 5\n")
		if got := codexBinaryVersion(context.Background(), hang); got != "" {
			t.Fatalf("hanging binary returned %q, want empty", got)
		}
	})
}

// When a terminal-backed Codex session already runs with the requested model and effort, session.modes reports
// changed=false so a subsequent refused step (such as ultracode) is not misreported as a partial change.
func TestCodexTerminalNoopReportsChangedFalse(t *testing.T) {
	n, f, _ := cxTerminalNode(t, proto.StatusIdle, "0.169.1", "gpt-a", "medium")
	res, err := request(t, n, "session.modes", map[string]any{"session_id": cxID, "model": "gpt-a", "effort": "medium"})
	if err != nil {
		t.Fatalf("no-op codex session.modes: %v", err)
	}
	if res["changed"] != false || len(cxTyped(f)) != 0 {
		t.Fatalf("no-op codex reply=%v typed=%v, want changed=false and nothing typed", res, cxTyped(f))
	}
}
