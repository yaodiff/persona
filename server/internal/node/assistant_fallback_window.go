package node

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a fallback that can hold the conversation (#10277) ----
//
// The bounded recovery (#3204) once moved a Codex conversation from a 1M-context model to the next entry of the
// launcher's list, a 131k one, while the conversation's fixed part (instructions, tool declarations, the message) was
// 166–194k tokens: Codex compacted before every step and the turn never ended. The list Codex's model/list gives
// carries no window; `codex debug models` renders the same catalog with each model's. Claude Code's lists declare
// none, so their order stays the list's.

// assistantFit is what a fallback must fit: need, the conversation's last request with room to answer (0 when the
// transcript has not said); prefer, the window of the model the conversation was asked to run (0 when not known); and
// windows, the windows the launcher's catalog declares by model id (none: nothing is skipped or put first).
type assistantFit struct {
	need, prefer int
	windows      map[string]int
}

// window is m's declared window, 0 when the catalog declares none for it.
func (f assistantFit) window(m proto.AgentModel) int {
	if w := f.windows[m.ID]; w > 0 {
		return w
	}
	if m.Resolved != "" {
		return f.windows[m.Resolved]
	}
	return 0
}

// holds: m's window, when known, takes the conversation.
func (f assistantFit) holds(m proto.AgentModel) bool {
	w := f.window(m)
	return w == 0 || f.need == 0 || w >= f.need
}

// preferred: m's window, when known, is at least the asked-for model's.
func (f assistantFit) preferred(m proto.AgentModel) bool {
	return f.prefer > 0 && f.window(m) >= f.prefer
}

// assistantWindowNeed is the window a fallback needs for a conversation whose last request took tokens: that request
// and a quarter more for the reply and the next one. A model with less compacts at once, and when what survives a
// compaction is already above its limit, before every step after that (#10278).
func assistantWindowNeed(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens + tokens/4
}

// assistantFallbackFit is the fit for conversation id on launcher: its last request from its transcript, the asked-for
// model's window (configured, else the launcher default it stands for, else the model that failed; else the window
// the transcript recorded), and the launcher's declared windows.
func (n *Node) assistantFallbackFit(ctx context.Context, id, launcher string, list proto.LauncherModels, configured, current string) assistantFit {
	fit := assistantFit{windows: assistantRecoveryWindows(ctx, n, launcher, list)}
	tokens, recorded := assistantRecoveryContext(n, id)
	fit.need = assistantWindowNeed(tokens)
	for _, model := range []string{configured, resolvedAssistantDefault(list, configured), current} {
		if w := fit.windows[model]; model != "" && w > 0 {
			fit.prefer = w
			return fit
		}
	}
	fit.prefer = recorded
	return fit
}

// assistantRecoveryContext is conversation id's last request in input tokens (the transcript's ContextTokens: a Codex
// rollout's token_count, Claude Code's last reply) and the window its transcript recorded (Codex's), 0 when not known;
// a variable for tests.
var assistantRecoveryContext = func(n *Node, id string) (tokens, window int) {
	m := n.metaOf(id)
	return m.contextTokens, m.contextWindow
}

// assistantRecoveryWindows is the windows launcher's agent declares for its models: a Codex launcher's catalog, none
// for any other; a variable for tests.
var assistantRecoveryWindows = func(ctx context.Context, n *Node, launcher string, list proto.LauncherModels) map[string]int {
	if list.Agent != "codex" {
		return nil
	}
	return n.codexModelWindows(ctx, launcher)
}

// codexWindowsTTL is how long a launcher's windows are kept: a chain asks for them once per model it tries, and a
// catalog changes when its file or its Codex does. A failed read is kept as long, so a Codex without the subcommand
// is not asked on every step.
const codexWindowsTTL = 10 * time.Minute

var codexWindows = struct {
	sync.Mutex
	at  map[string]time.Time
	got map[string]map[string]int
}{at: map[string]time.Time{}, got: map[string]map[string]int{}}

// codexModelWindows is launcher's models' windows, read from its codex's `debug models` and kept for codexWindowsTTL.
func (n *Node) codexModelWindows(ctx context.Context, launcher string) map[string]int {
	key := n.home + "\x00" + launcher
	codexWindows.Lock()
	if at, ok := codexWindows.at[key]; ok && time.Since(at) < codexWindowsTTL {
		w := codexWindows.got[key]
		codexWindows.Unlock()
		return w
	}
	codexWindows.Unlock()
	var windows map[string]int
	out, err := codexDebugModels(ctx, n, launcher)
	if err == nil {
		windows, err = codexDebugModelWindows(out)
	}
	if err != nil {
		n.logger.Printf("assistant recovery: %s's model windows: %v", launcher, err)
	}
	codexWindows.Lock()
	if len(codexWindows.at) > 64 {
		codexWindows.at, codexWindows.got = map[string]time.Time{}, map[string]map[string]int{}
	}
	codexWindows.at[key], codexWindows.got[key] = time.Now(), windows
	codexWindows.Unlock()
	return windows
}

// codexDebugModelsScript runs launcher's codex as `codex debug models`, the way agentModelsCommand runs it as an
// app-server: a managed launcher's `personad launch` runs codex itself, so the subcommand follows its words.
func (n *Node) codexDebugModelsScript(launcher string) string {
	script := n.launcherForScript(launcher)
	if _, managed := n.managedLauncher(launcher); managed {
		script += " debug models"
	}
	return codexShellWith(script, "debug models")
}

// codexDebugModels is the output of launcher's `codex debug models`, run in an empty directory; a variable for tests.
var codexDebugModels = func(ctx context.Context, n *Node, launcher string) ([]byte, error) {
	shell, err := interactiveShell()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "persona-models-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(ctx, launcherModelsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, shellFlag(shell), n.codexDebugModelsScript(launcher), "persona")
	cmd.Dir = dir
	killGroupOnCancel(cmd)
	return cmd.Output()
}

// codexDebugModelWindows reads `codex debug models` ({"models":[{"slug", "context_window",
// "effective_context_window_percent", …}]}, on one line) into each model's effective window: the part of its
// context window Codex lets a conversation fill (131,072 × 95% = 124,518, the window a turn on that model reports).
func codexDebugModelWindows(out []byte) (map[string]int, error) {
	var windows map[string]int
	err := jsonLines(bytes.NewReader(out), func(line []byte) (bool, error) {
		var catalog struct {
			Models []struct {
				Slug    string `json:"slug"`
				Window  int64  `json:"context_window"`
				Percent int64  `json:"effective_context_window_percent"`
			} `json:"models"`
		}
		if json.Unmarshal(line, &catalog) != nil || catalog.Models == nil {
			return false, nil // a banner or another object: read on
		}
		windows = map[string]int{}
		for _, m := range catalog.Models {
			w := m.Window
			if m.Percent > 0 && m.Percent < 100 {
				w = w * m.Percent / 100
			}
			if m.Slug != "" && w > 0 {
				windows[m.Slug] = int(w)
			}
		}
		return true, nil
	})
	if windows != nil {
		return windows, nil
	}
	return nil, err
}
