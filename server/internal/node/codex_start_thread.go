package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Translate only settings that can be preserved on the server that will own the
// thread. For example, a profile or a provider wrapper can change its identity.
func codexStartParams(argv []string, cwd string, e modelExecution) (params map[string]any, endpoint string) {
	if len(argv) == 0 || filepath.Base(argv[0]) != "codex" {
		return nil, ""
	}
	params = map[string]any{"cwd": cwd}
	for i := 1; i < len(argv); i++ {
		flag, value, inline := strings.Cut(argv[i], "=")
		switch flag {
		case "--no-alt-screen":
			continue
		case "--dangerously-bypass-approvals-and-sandbox", "--yolo":
			params["sandbox"], params["approvalPolicy"] = "danger-full-access", "never"
		case "--model", "-m", "--sandbox", "-s", "--ask-for-approval", "-a", "--remote", "-c":
			if !inline {
				i++
				if i == len(argv) {
					return nil, ""
				}
				value = argv[i]
			}
			switch flag {
			case "--model", "-m":
				params["model"] = value
			case "--sandbox", "-s":
				params["sandbox"] = value
			case "--ask-for-approval", "-a":
				params["approvalPolicy"] = value
			case "--remote":
				endpoint = value
			case "-c":
				if e.TaskInstructions == "" || value != codexTaskConfig(e.TaskInstructions) {
					return nil, ""
				}
				params["developerInstructions"] = e.TaskInstructions
			}
		default:
			return nil, ""
		}
	}
	if e.Model != "" {
		params["model"] = e.Model
	}
	if e.ServiceTier != "" {
		params["serviceTier"] = e.ServiceTier
	}
	return params, endpoint
}

func (n *Node) readyStartRemote(home, endpoint string) *codexRemote {
	n.codices.mu.Lock()
	var remote *codexRemote
	for _, c := range n.codices.remotes {
		if c.spec.Home == home && c.spec.Endpoint == endpoint {
			remote = c
			break
		}
	}
	n.codices.mu.Unlock()
	if remote != nil {
		remote.mu.Lock()
		defer remote.mu.Unlock()
		if remote.ready {
			return remote
		}
	}
	return nil
}

func (n *Node) startEffortRemote(ctx context.Context, home, endpoint, exe, cwd string, pane []string) *codexRemote {
	home = canonicalCodexHome(home, n.home)
	socket := filepath.Join(home, "app-server-control", "app-server-control.sock")
	local := endpoint == "" || endpoint == "unix://" || endpoint == "unix://"+socket
	if local {
		endpoint = "unix://" + socket
	}
	if c := n.readyStartRemote(home, endpoint); c != nil {
		return c
	}
	cfg := n.codexInventory()
	if !local || n.sessionScope != nil || cfg.Discovery != nil && !*cfg.Discovery {
		return nil
	}
	if _, err := os.Stat(socket); os.IsNotExist(err) {
		cmd := exec.CommandContext(ctx, exe, "app-server", "daemon", "start")
		if runtime.GOOS != "windows" {
			// Load the same proxy, credentials and PATH as the pane on a cold daemon start.
			shell := codexStartShellProgram()
			cmd = exec.CommandContext(ctx, shell, shellFlag(shell), `builtin cd -- "$1" && exec "$2" app-server daemon start`, "persona-codex-daemon", cwd, exe)
		}
		cmd.Dir = cwd
		cmd.Env = append(append(cmd.Environ(), pane...), codexStartMarker, "CODEX_HOME="+home)
		cmd.WaitDelay = time.Second
		if err := cmd.Run(); err != nil {
			return nil
		}
	}
	if _, _, err := validateCodexEndpoint(endpoint); err != nil {
		return nil
	}
	n.discoverCodex()
	// The node's regular inventory loop owns this connection beyond the start.
	for ctx.Err() == nil {
		if c := n.readyStartRemote(home, endpoint); c != nil {
			return c
		}
		sleepCtx(ctx, 100*time.Millisecond)
	}
	return nil
}

type codexPreparedThread struct {
	server      *codexRemote
	raw, public string
	path        string
}

func (t *codexPreparedThread) discard() {
	c := t.server
	c.mu.Lock()
	if c.discardedStarts == nil {
		c.discardedStarts = map[string]time.Time{}
	}
	c.discardedStarts[t.raw] = time.Now()
	delete(c.threads, t.raw)
	c.mu.Unlock()
	r := c.n.codices
	r.mu.Lock()
	delete(r.threads, codexSourceID(c.spec.Home, t.raw, r.config.LegacyHome))
	r.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	params := map[string]any{"threadId": t.raw}
	_ = c.wire.call(ctx, "thread/unsubscribe", params, nil)
	// A history read can leave an empty rollout. Use Codex's reversible archive
	// operation so that the abandoned start is also removed from active history.
	if codexRolloutExists(t.path) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := c.wire.call(ctx, "thread/archive", params, nil); err != nil {
			c.n.logf("codex %s: archive unused thread: %v", t.raw, err)
		}
	}
	c.n.notifyCodex()
}

func codexRolloutExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

// A nil thread and nil error allow terminal fallback. An ambiguous creation error
// stops the start: retrying could create a second conversation.
func (n *Node) prepareCodexEffort(ctx context.Context, launch host.Launcher, argv []string, cwd string, e modelExecution) (*codexPreparedThread, error) {
	if n.codices == nil {
		return nil, nil
	}
	params, endpoint := codexStartParams(argv, cwd, e)
	if params == nil {
		return nil, nil
	}
	if owner, ok := launch.(interface{ Kind() string }); ok && owner.Kind() == host.Orca && endpoint == "" {
		return nil, nil
	}
	pane := codexPaneEnv(launch)
	exe, home := n.codexStartCLI(ctx, argv, pane, nil, cwd)
	if exe == "" || !sameDir(home, n.codexHome()) {
		return nil, nil
	}
	// Process-specific attribution headers depend on environment bindings that a
	// shared daemon cannot inherit. Preserve that launcher's native configuration.
	if sourced, _ := codexModelSource(argv, append(os.Environ(), modelSourceEnvironment(e.Source, n.bin)...), cwd, n.home); len(sourced) != len(argv) {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c := n.startEffortRemote(ctx, home, endpoint, exe, cwd, pane)
	if c == nil {
		return nil, nil
	}
	// The app-server accepts even efforts outside its model catalog in thread/start
	// and settings/update. Validate the selected model when the catalog knows it.
	models := c.startEffortModels(ctx)
	if model, _ := params["model"].(string); model != "" {
		if err := checkCodexStartEffort(models, model, e.Effort); err != nil {
			return nil, err
		}
	}
	var reply struct {
		Thread      remoteThreadData
		ServiceTier json.RawMessage `json:"serviceTier"`
		Model       string
		Effort      string `json:"reasoningEffort"`
	}
	params["config"] = map[string]any{"model_reasoning_effort": e.Effort}
	err := c.wire.call(ctx, "thread/start", params, &reply)
	var re *codexError
	if errors.As(err, &re) && re.Code == -32601 {
		return nil, nil // the server has no thread/start method
	}
	if err != nil {
		return nil, err
	}
	if !validID(reply.Thread.ID) {
		return nil, errors.New("Codex thread/start returned no valid thread ID")
	}
	t := &codexPreparedThread{server: c, raw: reply.Thread.ID, path: reply.Thread.Path}
	keep := false
	defer func() {
		if !keep {
			t.discard()
		}
	}()
	if err := checkCodexStartEffort(models, reply.Model, e.Effort); err != nil {
		return nil, err
	}
	if reply.Thread.Ephemeral {
		return nil, errors.New("Codex app-server created an ephemeral thread that cannot be resumed")
	}
	tier := codexServiceTier(reply.ServiceTier)
	if e.ServiceTier != "" && e.ServiceTier != tier {
		return nil, nil
	}
	if reply.Thread.Path != "" && !codexRolloutExists(reply.Thread.Path) {
		// Codex 0.169 defers the rollout until the first turn. A full read of a
		// paginated thread writes its initial history before listing turns. On
		// 0.169.1 that listing can still return -32601 until settings are written
		// to the materialized thread. Confirm the file before applying effort.
		if reply.Thread.HistoryMode == "paginated" {
			err := c.wire.call(ctx, "thread/read", map[string]any{"threadId": t.raw, "includeTurns": true}, nil)
			var readErr *codexError
			if err != nil && !(errors.As(err, &readErr) && readErr.Code == -32601 && strings.Contains(readErr.Message, "list_turns")) {
				return nil, err
			}
		}
		if !codexRolloutExists(reply.Thread.Path) {
			if endpoint != "" {
				return nil, errors.New("Codex app-server did not persist the new thread for resume")
			}
			return nil, nil
		}
	}
	effort := reply.Effort
	err = c.wire.call(ctx, "thread/settings/update", map[string]any{"threadId": t.raw, "effort": e.Effort}, nil)
	switch {
	case errors.As(err, &re) && re.Code == -32601:
		// Older servers can confirm the start configuration in the creation reply.
	case err == nil || (errors.As(err, &re) && re.Code == -32602):
		// An acknowledgement has no settings. Read the live thread before opening
		// the pane, including after a rejection that may have partially applied.
		var read struct {
			Thread remoteThreadData
		}
		if err := c.wire.call(ctx, "thread/read", map[string]any{"threadId": t.raw, "includeTurns": false}, &read); err != nil || read.Thread.ID != t.raw {
			return nil, nil
		}
		effort = read.Thread.Effort
	default:
		return nil, nil
	}
	if effort != e.Effort {
		return nil, nil
	}
	t.public = n.registerCodex(c.spec.Home, t.raw, reply.Thread.Path)
	if t.public == "" || n.codexBlocked(t.public) {
		return nil, nil
	}
	c.mu.Lock()
	thread := c.threads[t.raw]
	thread.id, thread.publicID, thread.cwd, thread.path = t.raw, t.public, cwd, reply.Thread.Path
	thread.model, thread.effort, thread.joined, thread.status = reply.Model, effort, true, proto.StatusDone
	thread.serviceTier = tier
	thread.created = time.Now()
	thread.revision++
	c.threads[t.raw] = thread
	c.mu.Unlock()
	keep = true
	return t, nil
}

func (c *codexRemote) startEffortModels(ctx context.Context) map[string]codexModelEfforts {
	c.mu.Lock()
	models := c.models
	c.mu.Unlock()
	if models == nil {
		// The connection's asynchronous model/list may still be in flight.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		models, _ = readCodexModels(ctx, c.wire.call)
	}
	return models
}

func checkCodexStartEffort(models map[string]codexModelEfforts, model, effort string) error {
	if m, ok := models[model]; ok && !slices.Contains(m.levels, effort) {
		return fmt.Errorf("Codex model %q does not support effort %q (available: %s)", model, effort, strings.Join(m.levels, ", "))
	}
	return nil
}
