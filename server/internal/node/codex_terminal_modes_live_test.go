//go:build !windows

package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// What codex_terminal_modes.go counts on, against a real Codex TUI (opt-in): the /model line it builds is taken between
// turns, Codex records it in the rollout as thread_settings_applied with the values typed, a model Codex does not have
// is refused on the screen with nothing recorded, and config.toml keeps its model and effort. A private pty, temporary
// home and two local test providers let us verify which provider receives ordinary turns before and after the switch.
//
//	PERSONA_LIVE_CODEX_TUI     the codex binary (0.169 or later)
//	PERSONA_LIVE_CODEX_MODELS  two models its catalog has, comma-separated: the one it starts on, then another
func TestCodexTerminalLive(t *testing.T) {
	bin := os.Getenv("PERSONA_LIVE_CODEX_TUI")
	if bin == "" {
		t.Skip("PERSONA_LIVE_CODEX_TUI (a codex 0.169 or later) not set")
	}
	first, second := "gpt-6.2-astra", "gpt-6.2-sol"
	if m := strings.Split(os.Getenv("PERSONA_LIVE_CODEX_MODELS"), ","); len(m) == 2 {
		first, second = strings.TrimSpace(m[0]), strings.TrimSpace(m[1])
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, work := filepath.Join(dir, "codex-home"), filepath.Join(dir, "work")
	for _, d := range []string{home, work} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	type turn struct{ model, effort string }
	requests := map[string]chan turn{"say hi": make(chan turn, 1), "say bye": make(chan turn, 1)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/responses") {
			var body struct {
				Model     string
				Reasoning struct{ Effort string }
				Input     []struct {
					Role    string
					Content []struct{ Type, Text string }
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode model request: %v", err)
			}
			if r.URL.Path != "/gateway/v1/responses" {
				t.Errorf("request reached the base provider: %s", r.URL.Path)
			}
			var prompt string
			for _, item := range body.Input {
				if item.Role != "user" {
					continue
				}
				for _, part := range item.Content {
					if part.Type != "input_text" {
						continue
					}
					prompt = part.Text
					if strings.HasPrefix(strings.TrimSpace(prompt), "/model") {
						t.Error("a /model command reached the provider as a user message")
					}
				}
			}
			if seen := requests[prompt]; seen != nil {
				select {
				case seen <- turn{body.Model, body.Reasoning.Effort}:
				default: // a retry of the same synthetic prompt
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"end of test turn"}}`))
	}))
	t.Cleanup(server.Close)
	wantTurn := func(prompt, model, effort string) {
		t.Helper()
		want := turn{model, effort}
		select {
		case got := <-requests[prompt]:
			if got != want {
				t.Fatalf("%q request = %+v, want %+v", prompt, got, want)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("no request to the selected provider for %q", prompt)
		}
	}
	config := fmt.Sprintf("model = %q\nmodel_reasoning_effort = \"medium\"\nmodel_provider = \"base\"\ncheck_for_update_on_startup = false\n\n"+
		"[model_providers.base]\nname = \"base\"\nbase_url = %q\nwire_api = \"responses\"\n\n"+
		"[model_providers.gateway]\nname = \"gateway\"\nbase_url = %q\nwire_api = \"responses\"\n\n"+
		"[projects.%q]\ntrust_level = \"trusted\"\n",
		first, server.URL+"/base/v1", server.URL+"/gateway/v1", work)
	// Newer CLIs use a named layer and reject a legacy table for the same profile.
	help, err := exec.Command(bin, "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(help, []byte("<name>.config.toml")) {
		config += "\n[profiles.test]\nmodel_provider = \"gateway\"\n"
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	profile := []byte("model_provider = \"gateway\"\n")
	profilePath := filepath.Join(home, "test.config.toml")
	if err := os.WriteFile(profilePath, profile, 0o600); err != nil {
		t.Fatal(err)
	}
	tui := startLiveCodexTUI(t, bin, dir, home, work)
	tui.until(t, 60*time.Second, "its input box", func() bool { return strings.Contains(tui.text(), "Ask Codex") })

	// The first ordinary turn creates the rollout and reaches the named provider.
	tui.typeLine(t, "say hi")
	wantTurn("say hi", first, "medium")
	var rollout string
	tui.until(t, 30*time.Second, "its rollout", func() bool {
		found, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"))
		if len(found) == 1 {
			rollout = found[0]
		}
		return rollout != ""
	})
	if v := codexRolloutVersion(rollout); !versionAtLeast(v, codexModelArgsMinVersion) {
		t.Skipf("codex %q has no /model arguments", v)
	}
	tui.write(t, "\x1b")
	tui.until(t, 30*time.Second, "the turn to end", func() bool {
		b, _ := os.ReadFile(rollout)
		return bytes.Contains(b, []byte(`"turn_aborted"`)) || bytes.Contains(b, []byte(`"task_complete"`))
	})

	for _, c := range []struct{ model, effort string }{{"", "high"}, {first, "xhigh"}, {second, "low"}} {
		cmd, err := codexModelArgsCommand(c.model, c.effort)
		if err != nil {
			t.Fatal(err)
		}
		from := rolloutSize(rollout)
		tui.typeLine(t, cmd)
		tui.until(t, 15*time.Second, "the rollout to record "+cmd, func() bool {
			return codexSettingsRecorded(rollout, from, codexTerminalSettings{model: c.model, effort: c.effort})
		})
		t.Logf("LIVE %s: recorded", cmd)
	}

	cmd, _ := codexModelArgsCommand("no-such-model", "")
	from, shown := rolloutSize(rollout), len(tui.text())
	tui.typeLine(t, cmd)
	tui.until(t, 15*time.Second, "Codex's refusal", func() bool {
		out := tui.text()[shown:]
		return strings.Contains(out, "'no-such-model' is unavailable") || strings.Contains(out, "include `no-such-model`")
	})
	if codexSettingsRecorded(rollout, from, codexTerminalSettings{model: "no-such-model"}) {
		t.Error("a refused model was recorded")
	}
	t.Logf("LIVE %s: refused", cmd)
	tui.typeLine(t, "say bye")
	wantTurn("say bye", second, "low")

	if model, effort := readCodexConfigDefault(home); model != first || effort != "medium" {
		t.Errorf("config.toml now says %q / %q", model, effort)
	}
	got, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	// The TUI may persist onboarding state while the test runs.
	if values := tomlTopLevel(string(got)); values["model_provider"] != "gateway" || values["model"] != nil || values["model_reasoning_effort"] != nil {
		t.Error("the TUI changed the selected profile's provider, model or effort")
	}
}

// liveCodexTUI is a Codex TUI in a private pty, its output kept, the terminal queries crossterm sends answered.
type liveCodexTUI struct {
	tty *os.File
	mu  sync.Mutex
	out []byte
}

// ansiRE matches the escape sequences a TUI writes (CSI, OSC, charset and single-character ones).
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;?<=>]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)|\x1b[()][A-Za-z0-9]|\x1b[=>78DEHMNOc]")

// liveTerminalAnswers are the answers a terminal gives the queries a Codex TUI sends at start.
var liveTerminalAnswers = [][2]string{
	{"\x1b[6n", "\x1b[1;1R"}, {"\x1b[?u", "\x1b[?0u"}, {"\x1b[c", "\x1b[?62;22c"}, {"\x1b[0c", "\x1b[?62;22c"},
	{"\x1b]10;?\x07", "\x1b]10;rgb:ffff/ffff/ffff\x07"}, {"\x1b]11;?\x07", "\x1b]11;rgb:0000/0000/0000\x07"},
	{"\x1b]10;?\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"}, {"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
}

func startLiveCodexTUI(t *testing.T, bin, dir, home, work string) *liveCodexTUI {
	t.Helper()
	cmd := exec.Command(bin, "--profile", "test", "-C", work)
	cmd.Dir = work
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin", "TERM=xterm-256color", "LANG=en_US.UTF-8", "CODEX_HOME=" + home}
	tty, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 150, Rows: 45})
	if err != nil {
		t.Fatal(err)
	}
	tui := &liveCodexTUI{tty: tty}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 64<<10)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				chunk := string(buf[:n])
				for _, qa := range liveTerminalAnswers {
					if strings.Contains(chunk, qa[0]) {
						_, _ = tty.WriteString(qa[1])
					}
				}
				tui.mu.Lock()
				tui.out = append(tui.out, buf[:n]...)
				tui.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_, _ = tty.WriteString("\x03\x03")
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = tty.Close()
		<-done
	})
	return tui
}

// text is everything the TUI printed, without escape sequences.
func (c *liveCodexTUI) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ansiRE.ReplaceAllString(string(c.out), "")
}

func (c *liveCodexTUI) write(t *testing.T, s string) {
	t.Helper()
	if _, err := c.tty.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// typeLine pastes text into the box as herdr's agent.prompt does and, once the box shows it, submits it.
func (c *liveCodexTUI) typeLine(t *testing.T, text string) {
	t.Helper()
	shown := len(c.text())
	c.write(t, pasteStart+text+pasteEnd)
	squeeze := func(s string) string { return strings.Join(strings.Fields(s), "") }
	c.until(t, 10*time.Second, "the box to show "+text, func() bool { return strings.Contains(squeeze(c.text()[shown:]), squeeze(text)) })
	c.write(t, "\r")
}

func (c *liveCodexTUI) until(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for deadline := time.Now().Add(d); !ok(); <-tick.C {
		if time.Now().After(deadline) {
			text := c.text()
			t.Fatalf("timed out after %s waiting for %s; the screen ends with:\n%s", d, what, text[max(0, len(text)-2000):])
		}
	}
}

func rolloutSize(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}
