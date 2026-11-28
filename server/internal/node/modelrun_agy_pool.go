package node

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// On-demand warm `agy acp` process reuse for model.run (Antigravity).
//
// Each Node owns at most one warm `agy acp` worker with five strict zero-side-effect guardrails:
//  1. Fail-closed capability probe: `initialize` must advertise
//     `agentCapabilities._meta.antigravity.systemPrompt.append == true`; per-request system prompts travel
//     exclusively in `session/new._meta.antigravity.systemPrompt.append` (never AGENTS.md or agent.md body).
//  2. Two-stage slash-command fallback: static built-in commands (`plan`, `grill-me`, `learn`) unioned with
//     `available_commands_update` are matched with path-safe boundaries both before acquiring the worker and
//     after `session/new` (before `session/prompt`); matches close/clean the empty session and fall back to
//     `modelRunAgyCold` (`--disable-slash-commands`).
//  3. Isolated HOME, per-session disk cleanup, and ordered teardown: workerDir (`persona-check-*`) holds an
//     empty-body zero-tool agent.md, isolated HOME with 0600 strict settings.json, immediate deletion of
//     brain/<sid>, conversations/<sid>.db*, and annotations/<sid>.pbtxt after every `session/close`, and
//     strict teardown ordering (close stdin -> SIGTERM grace -> SIGKILL -> Wait + SweepGroup -> RemoveAll).
//  4. Zero-tool enforcement and security fail-closed: `mcpServers: []`; any `session/request_permission`,
//     `elicitation/create`, unexpected server request, or non-`manage_task` `tool_call` is refused and
//     immediately destroys the worker.
//  5. Key invalidation, single-process cap, destroy-on-anomaly, and rotation: concurrent requests while busy
//     fall back immediately to `modelRunAgyCold` without blocking; any config/auth/env key change, error,
//     timeout, cancellation, or crash destroys the worker; idle workers auto-evict after 120s and rotate
//     after 50 requests; Node.Serve closes the pool on exit.

const (
	agyWarmDefaultIdleTTL     = 120 * time.Second
	agyWarmDefaultMaxRequests = 50
	agyWarmDefaultStopGrace   = 2 * time.Second
	agyWarmCloseSessionWait   = 2 * time.Second
	agyWarmControlWriteWait   = 200 * time.Millisecond
	agyWarmDefaultWriteWait   = 30 * time.Second
	agyWarmCommandsProbeWait  = 50 * time.Millisecond
	agyWarmMaxScanTokenSize   = 16 * 1024 * 1024
	agyWarmStderrBufLimit     = 8 * 1024
)

var agyWarmBuiltinSlashCommands = []string{"plan", "grill-me", "learn"}

type agyWarmKey struct {
	launcherKey   string
	settingsHash  [32]byte
	oauthStat     agyFileStamp
	keychainStamp string // macOS login-keychain sign-in (modelrun_agy_account.go)
	geminiEnvHash [32]byte
	realDataRoot  string
}

type agyModelRunPool struct {
	mu            sync.Mutex
	closed        bool
	busy          bool
	worker        *agyWarmWorker
	active        *agyWarmWorker // non-nil while starting or serving a turn so close() can abort immediately
	unsupported   map[string]bool
	knownCommands map[string]map[string]struct{} // per launcherKey dynamic slash commands learned from available_commands_update
	idleTimer     *time.Timer
	idleGen       uint64

	// Test-injectable lifecycle controls (defaults apply when zero/nil).
	idleTTL     time.Duration
	maxRequests int
	stopGrace   time.Duration
	onIdleEvict func()
}

func newAgyModelRunPool() *agyModelRunPool {
	return &agyModelRunPool{
		unsupported:   map[string]bool{},
		knownCommands: map[string]map[string]struct{}{},
	}
}

func (n *Node) agyPool() *agyModelRunPool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.agyModelPool == nil {
		n.agyModelPool = newAgyModelRunPool()
	}
	return n.agyModelPool
}

func (n *Node) closeAgyModelRunPool() {
	n.mu.Lock()
	p := n.agyModelPool
	n.agyModelPool = nil
	n.mu.Unlock()
	if p != nil {
		p.close()
	}
}

func (p *agyModelRunPool) ttl() time.Duration {
	if p.idleTTL > 0 {
		return p.idleTTL
	}
	return agyWarmDefaultIdleTTL
}

func (p *agyModelRunPool) maxReq() int {
	if p.maxRequests > 0 {
		return p.maxRequests
	}
	return agyWarmDefaultMaxRequests
}

func (p *agyModelRunPool) grace() time.Duration {
	if p.stopGrace > 0 {
		return p.stopGrace
	}
	return agyWarmDefaultStopGrace
}

func (p *agyModelRunPool) close() {
	p.mu.Lock()
	p.closed = true
	p.idleGen++
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
	w := p.worker
	p.worker = nil
	active := p.active
	p.mu.Unlock()

	if w != nil {
		w.destroy()
	}
	if active != nil && active != w {
		active.destroy()
	}
}

func (p *agyModelRunPool) isUnsupported(launcherKey string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.unsupported[launcherKey]
}

func (p *agyModelRunPool) markUnsupported(launcherKey string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.unsupported[launcherKey] = true
}

func (p *agyModelRunPool) recordCommands(launcherKey string, cmds []string) {
	if len(cmds) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	set := p.knownCommands[launcherKey]
	if set == nil {
		set = map[string]struct{}{}
		p.knownCommands[launcherKey] = set
	}
	for _, c := range cmds {
		if norm := normalizeSlashCommandName(c); norm != "" {
			set[norm] = struct{}{}
		}
	}
}

// acquire reserves the single warm slot if the pool is open, supported, idle, and the prompt passes Stage 1
// slash-command detection. If busy (e.g. a 2nd concurrent request), closed, unsupported, or slash-command matched,
// acquired is false and the caller immediately falls back to modelRunAgyCold without blocking.
func (p *agyModelRunPool) acquire(key agyWarmKey, prompt string) (reused *agyWarmWorker, toDestroy *agyWarmWorker, acquired bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.busy || p.unsupported[key.launcherKey] {
		return nil, nil, false
	}
	if promptMatchesSlashCommand(prompt, p.stage1CommandsLocked(key.launcherKey)) {
		return nil, nil, false
	}
	p.busy = true
	p.idleGen++
	if p.idleTimer != nil {
		p.idleTimer.Stop()
		p.idleTimer = nil
	}
	if p.worker != nil {
		if p.worker.key == key && p.worker.isAlive() && p.worker.requests < p.maxReq() {
			p.active = p.worker
			return p.worker, nil, true
		}
		toDestroy = p.worker
		p.worker = nil
	}
	return nil, toDestroy, true
}

func (p *agyModelRunPool) stage1CommandsLocked(launcherKey string) map[string]struct{} {
	cmds := make(map[string]struct{}, len(agyWarmBuiltinSlashCommands)+len(p.knownCommands[launcherKey]))
	for _, c := range agyWarmBuiltinSlashCommands {
		cmds[c] = struct{}{}
	}
	for c := range p.knownCommands[launcherKey] {
		cmds[c] = struct{}{}
	}
	if p.worker != nil && p.worker.key.launcherKey == launcherKey {
		for _, c := range p.worker.snapshotDynamicCommands() {
			cmds[c] = struct{}{}
		}
	}
	return cmds
}

func (p *agyModelRunPool) setActive(w *agyWarmWorker) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.active = w
	return true
}

// release finishes an acquired warm slot. When keep is true, the worker is kept idle (or gracefully rotated if it
// reached maxRequests) and the idle eviction timer is armed. When keep is false, w is detached and destroyed.
func (p *agyModelRunPool) release(w *agyWarmWorker, keep bool, countRequest bool) {
	var destroyW *agyWarmWorker

	p.mu.Lock()
	p.busy = false
	if p.active == w {
		p.active = nil
	}
	if !keep || w == nil || p.closed || !w.isAlive() {
		if p.worker == w {
			p.worker = nil
		}
		destroyW = w
	} else {
		if countRequest {
			w.requests++
		}
		if w.requests >= p.maxReq() {
			if p.worker == w {
				p.worker = nil
			}
			destroyW = w
		} else {
			p.worker = w
			p.idleGen++
			gen := p.idleGen
			p.idleTimer = time.AfterFunc(p.ttl(), func() {
				p.evictIfIdle(gen)
			})
		}
	}
	p.mu.Unlock()

	if destroyW != nil {
		destroyW.destroy()
	}
}

func (p *agyModelRunPool) evictIfIdle(gen uint64) {
	p.mu.Lock()
	if p.closed || p.busy || p.worker == nil || p.idleGen != gen {
		p.mu.Unlock()
		return
	}
	w := p.worker
	p.worker = nil
	p.idleTimer = nil
	cb := p.onIdleEvict
	p.mu.Unlock()

	w.destroy()
	if cb != nil {
		cb()
	}
}

// agyModelRunWarmAgentMarkdown builds the empty-body zero-tool workspace custom agent definition used by the
// warm `agy acp` worker. Per-request system prompts are never written here; they travel in
// `session/new._meta.antigravity.systemPrompt.append`.
func agyModelRunWarmAgentMarkdown() string {
	return "---\n" +
		"name: " + agyModelRunAgentName + "\n" +
		"description: Zero-tool single-turn text completion agent for Persona model.run\n" +
		"tools: []\n" +
		"mainAgent: true\n" +
		"subagent: false\n" +
		"commandExecutionPolicy: off\n" +
		"mcpServers: []\n" +
		"skills: []\n" +
		"plugins: []\n" +
		"---\n"
}

func (n *Node) agyWarmKeyFor(p proto.ModelRunParams, builtinAgyBacked bool) (key agyWarmKey, settingsJSON []byte, modelProvider string, ok bool, err error) {
	// Managed agy launchers that inject default model, effort, API key, gateway URL, env, or extra args are routed
	// through `personad launch <name>`, whose launchArgv prepends top-level flags before subcommands. Keep them on
	// modelRunAgyCold so semantics remain 100% identical with zero side effects.
	if l, managed := n.managedLauncher(p.Launcher); managed {
		if l.Model != "" || l.Effort != "" || l.APIKey != "" || l.BaseURL != "" || l.Env != "" || len(l.Args) > 0 {
			return agyWarmKey{}, nil, "", false, nil
		}
	}

	var launcherKey string
	if builtinAgyBacked {
		bin := n.builtinAgyExecutable()
		if bin == "" {
			return agyWarmKey{}, nil, "", false, nil
		}
		launcherKey = "builtin:" + bin
	} else {
		launcherKey = "shell:" + n.launcherForScript(p.Launcher)
	}

	settingsJSON, err = n.agyModelRunSettings()
	if err != nil {
		return agyWarmKey{}, nil, "", false, err
	}
	var parsedSettings struct {
		ModelProvider string `json:"modelProvider"`
	}
	_ = json.Unmarshal(settingsJSON, &parsedSettings)
	modelProvider = strings.ToLower(strings.TrimSpace(parsedSettings.ModelProvider))

	realDataRoot := n.agyDataRoot()
	var geminiEnvHash [32]byte
	if modelProvider == "gemini" {
		h := sha256.New()
		for _, k := range []string{"GEMINI_API_KEY", "GOOGLE_GEMINI_BASE_URL", "PERSONA_AGY_MODEL_PROVIDER"} {
			_, _ = h.Write([]byte(k))
			_, _ = h.Write([]byte{'='})
			_, _ = h.Write([]byte(os.Getenv(k)))
			_, _ = h.Write([]byte{0})
		}
		h.Sum(geminiEnvHash[:0])
	}

	return agyWarmKey{
		launcherKey:   launcherKey,
		settingsHash:  sha256.Sum256(settingsJSON),
		oauthStat:     statAgyFileStamp(filepath.Join(realDataRoot, "antigravity-oauth-token")),
		keychainStamp: agyKeychainStampFn(),
		geminiEnvHash: geminiEnvHash,
		realDataRoot:  realDataRoot,
	}, settingsJSON, modelProvider, true, nil
}

// modelRunAgyWarm attempts to answer an Antigravity model.run request via the Node's single warm `agy acp`
// worker. handled == false means the caller should transparently fall back to modelRunAgyCold.
func (n *Node) modelRunAgyWarm(ctx context.Context, p proto.ModelRunParams, builtinAgyBacked bool, timedOut error) (ans proto.ModelRunAnswer, handled bool, err error) {
	// Guardrail ④ parity: when both Model and Effort are given and conflict (e.g. `gemini-4-hydrogen-low` + `medium`),
	// fall back to cold CLI so conflict errors match `--model ... conflicts with --effort=...` identically.
	if p.Model != "" && p.Effort != "" {
		if me := agyModelEffort(p.Model); me != "" && !strings.EqualFold(me, p.Effort) {
			return proto.ModelRunAnswer{}, false, nil
		}
	}

	key, settingsJSON, modelProvider, ok, keyErr := n.agyWarmKeyFor(p, builtinAgyBacked)
	if keyErr != nil {
		return proto.ModelRunAnswer{}, true, keyErr
	}
	if !ok {
		return proto.ModelRunAnswer{}, false, nil
	}

	pool := n.agyPool()
	worker, toDestroy, acquired := pool.acquire(key, p.Prompt)
	if !acquired {
		return proto.ModelRunAnswer{}, false, nil
	}
	if toDestroy != nil {
		toDestroy.destroy()
	}

	keepWorker := false
	countRequest := false
	defer func() {
		pool.release(worker, keepWorker, countRequest)
	}()

	if worker == nil {
		var startOutcome agyWarmStartOutcome
		worker, startOutcome, err = n.startAgyWarmWorker(ctx, p, builtinAgyBacked, key, settingsJSON, modelProvider, pool.grace())
		switch startOutcome {
		case agyWarmStartOK:
			if !pool.setActive(worker) {
				// Pool closed concurrently during startup.
				return proto.ModelRunAnswer{}, false, nil
			}
		case agyWarmStartUnsupported:
			pool.markUnsupported(key.launcherKey)
			return proto.ModelRunAnswer{}, false, nil
		case agyWarmStartFallbackNoCache:
			return proto.ModelRunAnswer{}, false, nil
		case agyWarmStartTimeout:
			return proto.ModelRunAnswer{}, true, timedOut
		case agyWarmStartError:
			return proto.ModelRunAnswer{}, true, err
		}
	}

	ans, fallback, keep, counted, turnErr := worker.runTurn(ctx, p, timedOut, func(cmds []string) {
		pool.recordCommands(key.launcherKey, cmds)
	})
	keepWorker = keep
	countRequest = counted
	if fallback {
		return proto.ModelRunAnswer{}, false, nil
	}
	return ans, true, turnErr
}

type agyWarmStartOutcome int

const (
	agyWarmStartOK agyWarmStartOutcome = iota
	agyWarmStartUnsupported
	agyWarmStartFallbackNoCache
	agyWarmStartTimeout
	agyWarmStartError
)

type agyRPCResponse struct {
	Result json.RawMessage
	Err    *agyRPCError
}

type agyRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *agyRPCError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("ACP error %d", e.Code)
}

type agyTurnCollector struct {
	mu           sync.Mutex
	sessionID    string
	text         strings.Builder
	activeModel  string
	thoughtLevel string
	violation    error
	violationCh  chan struct{}
	violatedOnce sync.Once
}

func newAgyTurnCollector() *agyTurnCollector {
	return &agyTurnCollector{violationCh: make(chan struct{})}
}

func (c *agyTurnCollector) setSessionID(sid string) {
	c.mu.Lock()
	c.sessionID = sid
	c.mu.Unlock()
}

func (c *agyTurnCollector) appendText(s string) {
	c.mu.Lock()
	c.text.WriteString(s)
	c.mu.Unlock()
}

func (c *agyTurnCollector) getText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text.String()
}

func (c *agyTurnCollector) updateModel(model, effort string) {
	c.mu.Lock()
	if model != "" {
		c.activeModel = model
	}
	if effort != "" {
		c.thoughtLevel = effort
	}
	c.mu.Unlock()
}

func (c *agyTurnCollector) getModel() (model, effort string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeModel, c.thoughtLevel
}

func (c *agyTurnCollector) failSecurity(err error) {
	c.violatedOnce.Do(func() {
		c.mu.Lock()
		c.violation = err
		c.mu.Unlock()
		close(c.violationCh)
	})
}

func (c *agyTurnCollector) securityErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.violation
}

type agyWarmWorker struct {
	key       agyWarmKey
	workerDir string
	wsDir     string
	isoHome   string
	isoData   string
	stopGrace time.Duration

	cmd      *exec.Cmd
	pid      int
	stdin    *os.File // immutable after init; closed only via abortStdin/closeStdinOnce
	stdout   *os.File
	stderr   *os.File
	nextID   atomic.Int64
	requests int

	writeCh        chan agyWriteReq
	stopWriteCh    chan struct{}
	stopWriteOnce  sync.Once
	closeStdinOnce sync.Once
	stdinAborted   atomic.Bool
	writerDone     chan struct{}
	onBeforeWrite  func()

	mu             sync.Mutex
	pending        map[int64]chan agyRPCResponse
	turn           *agyTurnCollector
	dynamicCmds    map[string]struct{}
	initialized    bool
	startupAuthHit bool
	stderrBuf      bytes.Buffer
	destroyed      bool

	startupAuthCh chan struct{}
	startupAuth1  sync.Once
	commandsReady chan struct{}
	commandsOnce  sync.Once
	stdoutDone    chan struct{}
	stderrDone    chan struct{}
	waitDone      chan struct{}
	waitErr       error
	destroyOnce   sync.Once
}

func (w *agyWarmWorker) isAlive() bool {
	select {
	case <-w.waitDone:
		return false
	case <-w.stdoutDone:
		return false
	default:
	}
	w.mu.Lock()
	dead := w.destroyed
	w.mu.Unlock()
	return !dead
}

func (w *agyWarmWorker) hasCommands() bool {
	select {
	case <-w.commandsReady:
		return true
	default:
		return false
	}
}

func (w *agyWarmWorker) snapshotDynamicCommands() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.dynamicCmds) == 0 {
		return nil
	}
	out := make([]string, 0, len(w.dynamicCmds))
	for c := range w.dynamicCmds {
		out = append(out, c)
	}
	return out
}

func (w *agyWarmWorker) allSlashCommands() map[string]struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	cmds := make(map[string]struct{}, len(agyWarmBuiltinSlashCommands)+len(w.dynamicCmds))
	for _, c := range agyWarmBuiltinSlashCommands {
		cmds[c] = struct{}{}
	}
	for c := range w.dynamicCmds {
		cmds[c] = struct{}{}
	}
	return cmds
}

func (w *agyWarmWorker) stderrText() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(w.stderrBuf.String())
}

func (w *agyWarmWorker) noteStartupAuth() {
	w.mu.Lock()
	if w.initialized {
		w.mu.Unlock()
		return
	}
	w.startupAuthHit = true
	w.mu.Unlock()
	w.startupAuth1.Do(func() { close(w.startupAuthCh) })
}

func (w *agyWarmWorker) markInitialized() {
	w.mu.Lock()
	w.initialized = true
	w.mu.Unlock()
}

func (w *agyWarmWorker) sawStartupAuth() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.startupAuthHit
}

type agyWriteReq struct {
	data     []byte
	deadline time.Time
	errCh    chan error
}

func (w *agyWarmWorker) stopWrite() {
	w.stopWriteOnce.Do(func() { close(w.stopWriteCh) })
}

// abortStdin immediately interrupts any blocked w.stdin.Write (via write deadline + Close on Go's netpoller)
// without acquiring any write lock, so destroy() and ctx cancellation can never deadlock against a full pipe.
func (w *agyWarmWorker) abortStdin() {
	w.stdinAborted.Store(true)
	w.stopWrite()
	w.closeStdinOnce.Do(func() {
		if w.stdin != nil {
			_ = w.stdin.SetWriteDeadline(time.Now())
			_ = w.stdin.Close()
		}
	})
}

func (w *agyWarmWorker) writeLoop() {
	defer close(w.writerDone)
	for {
		select {
		case <-w.stopWriteCh:
			return
		case req := <-w.writeCh:
			if w.stdinAborted.Load() {
				req.errCh <- io.ErrClosedPipe
				return
			}
			// Apply write deadline when supported by the pipe; ignore os.ErrNoDeadline on platforms without pollable pipes.
			if w.onBeforeWrite != nil {
				w.onBeforeWrite()
			}
			_ = w.stdin.SetWriteDeadline(req.deadline)
			_, err := w.stdin.Write(req.data)
			if err != nil {
				req.errCh <- err
				w.abortStdin()
				return
			}
			_ = w.stdin.SetWriteDeadline(time.Time{})
			req.errCh <- nil
		}
	}
}

func (w *agyWarmWorker) writeFrame(ctx context.Context, v any, abortCh <-chan struct{}) error {
	if w.stdinAborted.Load() {
		return io.ErrClosedPipe
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(agyWarmDefaultWriteWait)
	}
	req := agyWriteReq{
		data:     b,
		deadline: deadline,
		errCh:    make(chan error, 1),
	}

	select {
	case w.writeCh <- req:
	case <-w.stopWriteCh:
		return io.ErrClosedPipe
	case <-abortCh:
		return errAgyWarmAborted
	case <-ctx.Done():
		return ctx.Err()
	}

	select {
	case err := <-req.errCh:
		return err
	case <-w.stopWriteCh:
		select {
		case err := <-req.errCh:
			return err
		default:
			return io.ErrClosedPipe
		}
	case <-abortCh:
		w.abortStdin()
		return errAgyWarmAborted
	case <-ctx.Done():
		// Interrupt any blocked write immediately; partial writes invalidate JSON-RPC framing so this worker cannot be reused.
		w.abortStdin()
		return ctx.Err()
	}
}

func (w *agyWarmWorker) notify(ctx context.Context, method string, params any) error {
	return w.writeFrame(ctx, map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}, nil)
}

func (w *agyWarmWorker) call(ctx context.Context, method string, params any, abortCh <-chan struct{}) (json.RawMessage, *agyRPCError, error) {
	id := w.nextID.Add(1)
	ch := make(chan agyRPCResponse, 1)

	w.mu.Lock()
	if w.destroyed {
		w.mu.Unlock()
		return nil, nil, errors.New("agy acp worker closed")
	}
	w.pending[id] = ch
	w.mu.Unlock()

	defer func() {
		w.mu.Lock()
		delete(w.pending, id)
		w.mu.Unlock()
	}()

	if err := w.writeFrame(ctx, map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}, abortCh); err != nil {
		return nil, nil, err
	}

	select {
	case resp := <-ch:
		if resp.Err != nil {
			return nil, resp.Err, nil
		}
		return resp.Result, nil, nil
	case <-abortCh:
		return nil, nil, errAgyWarmAborted
	case <-w.stdoutDone:
		// Check if response arrived right before EOF.
		select {
		case resp := <-ch:
			if resp.Err != nil {
				return nil, resp.Err, nil
			}
			return resp.Result, nil, nil
		default:
			return nil, nil, errors.New("agy acp process exited")
		}
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
}

var errAgyWarmAborted = errors.New("agy acp call aborted")

func (w *agyWarmWorker) readStdoutLoop() {
	defer close(w.stdoutDone)
	defer func() {
		// Unblock any pending RPC callers on EOF.
		w.mu.Lock()
		for id, ch := range w.pending {
			delete(w.pending, id)
			select {
			case ch <- agyRPCResponse{Err: &agyRPCError{Code: -32001, Message: "agy acp stdout closed"}}:
			default:
			}
		}
		w.mu.Unlock()
	}()

	sc := bufio.NewScanner(w.stdout)
	sc.Buffer(make([]byte, 64*1024), agyWarmMaxScanTokenSize)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			if isAgyInteractiveAuthPrompt(line) {
				w.noteStartupAuth()
			}
			continue
		}
		var msg struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
			Result  json.RawMessage `json:"result"`
			Error   *agyRPCError    `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			if isAgyInteractiveAuthPrompt(line) {
				w.noteStartupAuth()
			}
			continue
		}

		// Response to a client->server RPC request.
		if len(msg.ID) > 0 && msg.Method == "" && (len(msg.Result) > 0 || msg.Error != nil) {
			var id int64
			if err := json.Unmarshal(msg.ID, &id); err == nil {
				w.mu.Lock()
				ch := w.pending[id]
				delete(w.pending, id)
				w.mu.Unlock()
				if ch != nil {
					ch <- agyRPCResponse{Result: msg.Result, Err: msg.Error}
				}
			}
			continue
		}

		// Server->client RPC request: Guardrail ④ immediately refuses and fails closed.
		if len(msg.ID) > 0 && msg.Method != "" {
			w.handleServerRequest(msg.ID, msg.Method)
			continue
		}

		// Server->client notification.
		if msg.Method == "session/update" && len(msg.Params) > 0 {
			w.handleSessionUpdate(msg.Params)
		}
	}
}

func (w *agyWarmWorker) readStderrLoop() {
	defer close(w.stderrDone)
	sc := bufio.NewScanner(w.stderr)
	sc.Buffer(make([]byte, 16*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if isAgyInteractiveAuthPrompt(line) {
			w.noteStartupAuth()
		}
		w.mu.Lock()
		if w.stderrBuf.Len() < agyWarmStderrBufLimit {
			if w.stderrBuf.Len() > 0 {
				w.stderrBuf.WriteByte('\n')
			}
			rem := agyWarmStderrBufLimit - w.stderrBuf.Len()
			if len(line) > rem {
				line = line[:rem]
			}
			w.stderrBuf.WriteString(line)
		}
		w.mu.Unlock()
	}
}

func (w *agyWarmWorker) handleServerRequest(rawID json.RawMessage, method string) {
	replyCtx, cancel := context.WithTimeout(context.Background(), agyWarmControlWriteWait)
	defer cancel()
	switch method {
	case "session/request_permission":
		_ = w.writeFrame(replyCtx, map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result":  map[string]any{"outcome": map[string]any{"outcome": "cancelled"}},
		}, nil)
		w.failActiveSecurity(errors.New("agy requested tool permission during zero-tool model.run"))
	case "elicitation/create":
		_ = w.writeFrame(replyCtx, map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"result":  map[string]any{"action": "cancel"},
		}, nil)
		w.failActiveSecurity(errors.New("agy requested elicitation during zero-tool model.run"))
	default:
		_ = w.writeFrame(replyCtx, map[string]any{
			"jsonrpc": "2.0",
			"id":      rawID,
			"error":   map[string]any{"code": -32601, "message": "Method not found: " + method},
		}, nil)
		w.failActiveSecurity(fmt.Errorf("agy sent unexpected ACP request %q during zero-tool model.run", method))
	}
}

func (w *agyWarmWorker) failActiveSecurity(err error) {
	w.mu.Lock()
	turn := w.turn
	w.mu.Unlock()
	if turn != nil {
		turn.failSecurity(err)
	}
}

func (w *agyWarmWorker) handleSessionUpdate(raw json.RawMessage) {
	var env struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate     string `json:"sessionUpdate"`
			AvailableCommands []struct {
				Name string `json:"name"`
			} `json:"availableCommands"`
			Content *struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			ConfigOptions []agyACPConfigOption `json:"configOptions"`
			Name          string               `json:"name"`
			Title         string               `json:"title"`
			Kind          string               `json:"kind"`
			Meta          *struct {
				ToolName string `json:"toolName"`
			} `json:"_meta"`
		} `json:"update"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return
	}

	switch env.Update.SessionUpdate {
	case "available_commands_update":
		w.mu.Lock()
		for _, c := range env.Update.AvailableCommands {
			if norm := normalizeSlashCommandName(c.Name); norm != "" {
				w.dynamicCmds[norm] = struct{}{}
			}
		}
		w.mu.Unlock()
		w.commandsOnce.Do(func() { close(w.commandsReady) })

	case "agent_thought_chunk":
		// Explicitly ignored: never surfaced in ModelRunAnswer.Text.

	case "agent_message_chunk":
		if env.Update.Content != nil && env.Update.Content.Text != "" {
			w.mu.Lock()
			turn := w.turn
			w.mu.Unlock()
			if turn != nil {
				turn.appendText(env.Update.Content.Text)
			}
		}

	case "config_option_update":
		model, effort := extractAgyConfigOptions(env.Update.ConfigOptions)
		if model != "" || effort != "" {
			w.mu.Lock()
			turn := w.turn
			w.mu.Unlock()
			if turn != nil {
				turn.updateModel(model, effort)
			}
		}

	case "tool_call", "tool_call_update":
		toolName := ""
		if env.Update.Meta != nil && strings.TrimSpace(env.Update.Meta.ToolName) != "" {
			toolName = strings.TrimSpace(env.Update.Meta.ToolName)
		} else if strings.TrimSpace(env.Update.Name) != "" {
			toolName = strings.TrimSpace(env.Update.Name)
		} else if strings.TrimSpace(env.Update.Title) != "" {
			toolName = strings.TrimSpace(env.Update.Title)
		}
		if !strings.EqualFold(toolName, "manage_task") {
			w.failActiveSecurity(fmt.Errorf("agy attempted unexpected tool call %q during zero-tool model.run", cmp.Or(toolName, "unknown")))
		}
	}
}

// destroy enforces Guardrail ③ teardown ordering:
// close stdin -> StopGroup(SIGTERM, grace) -> StopGroup(SIGKILL) -> Wait() + SweepGroup() -> drain readers -> RemoveAll(workerDir).
func (w *agyWarmWorker) destroy() {
	if w == nil {
		return
	}
	w.destroyOnce.Do(func() {
		w.mu.Lock()
		w.destroyed = true
		w.mu.Unlock()

		// 1. Interrupt any blocked stdin write and close stdin without holding a write lock so agy acp sees EOF
		// and teardown can never deadlock against a stalled pipe.
		w.abortStdin()

		// 2. Give the process a brief window to drain any final stdin frame (cancel / permission refusal) and exit on EOF,
		// then send SIGTERM with grace window, escalating to SIGKILL if still alive.
		if w.pid > 0 {
			grace := w.stopGrace
			if grace <= 0 {
				grace = agyWarmDefaultStopGrace
			}
			eofDrain := min(grace/4, 100*time.Millisecond)
			eofTimer := time.NewTimer(eofDrain)
			select {
			case <-w.waitDone:
				eofTimer.Stop()
			case <-eofTimer.C:
				_ = osproc.StopGroup(w.pid, false)
				timer := time.NewTimer(grace)
				select {
				case <-w.waitDone:
					timer.Stop()
				case <-timer.C:
					_ = osproc.StopGroup(w.pid, true)
					<-w.waitDone
				}
			}
			// 3. Reap any surviving descendant in the process group before touching workerDir.
			osproc.SweepGroup(w.pid, time.Second)
		} else {
			<-w.waitDone
		}

		// 4. Close read ends and wait for writer + reader goroutines so zero goroutines or fds leak.
		if w.stdout != nil {
			_ = w.stdout.Close()
		}
		if w.stderr != nil {
			_ = w.stderr.Close()
		}
		<-w.writerDone
		<-w.stdoutDone
		<-w.stderrDone

		// 5. Only after the process group has fully exited may workerDir be removed (prevents shutdown flush race).
		if w.workerDir != "" {
			_ = os.RemoveAll(w.workerDir)
		}
	})
}

func (n *Node) startAgyWarmWorker(
	ctx context.Context,
	p proto.ModelRunParams,
	builtinAgyBacked bool,
	key agyWarmKey,
	settingsJSON []byte,
	modelProvider string,
	stopGrace time.Duration,
) (*agyWarmWorker, agyWarmStartOutcome, error) {
	workerDir, err := mkModelRunTempDir()
	if err != nil {
		return nil, agyWarmStartError, err
	}
	cleanupDir := true
	defer func() {
		if cleanupDir {
			_ = os.RemoveAll(workerDir)
		}
	}()

	wsDir := filepath.Join(workerDir, "ws")
	agentDir := filepath.Join(wsDir, ".agents", "agents", agyModelRunAgentName)
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		return nil, agyWarmStartError, err
	}
	if _, err := writeTempFile(agentDir, "agent.md", agyModelRunWarmAgentMarkdown()); err != nil {
		return nil, agyWarmStartError, err
	}

	isoHome, err := n.prepareAgyIsolatedHome(workerDir)
	if err != nil {
		return nil, agyWarmStartError, err
	}
	isoData := filepath.Join(isoHome, ".gemini", "antigravity-cli")
	if err := os.WriteFile(filepath.Join(isoData, "settings.json"), settingsJSON, 0o600); err != nil {
		return nil, agyWarmStartError, err
	}

	cmd, err := n.buildAgyWarmCmd(p, builtinAgyBacked, wsDir, isoHome)
	if err != nil {
		return nil, agyWarmStartFallbackNoCache, nil
	}

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return nil, agyWarmStartError, err
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		return nil, agyWarmStartError, err
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		return nil, agyWarmStartError, err
	}

	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	msysCmdLine(cmd)

	if err := cmd.Start(); err != nil {
		_ = stdinR.Close()
		_ = stdinW.Close()
		_ = stdoutR.Close()
		_ = stdoutW.Close()
		_ = stderrR.Close()
		_ = stderrW.Close()
		// Transient spawn/exec errors (e.g. ETXTBSY/EAGAIN during CLI upgrade) must not permanently disable warm reuse.
		return nil, agyWarmStartFallbackNoCache, nil
	}
	// Close child ends in parent immediately so EOF propagates accurately and cmd.Wait never races readers.
	_ = stdinR.Close()
	_ = stdoutW.Close()
	_ = stderrW.Close()

	cleanupDir = false
	w := &agyWarmWorker{
		key:           key,
		workerDir:     workerDir,
		wsDir:         wsDir,
		isoHome:       isoHome,
		isoData:       isoData,
		stopGrace:     stopGrace,
		cmd:           cmd,
		pid:           cmd.Process.Pid,
		stdin:         stdinW,
		stdout:        stdoutR,
		stderr:        stderrR,
		writeCh:       make(chan agyWriteReq),
		stopWriteCh:   make(chan struct{}),
		writerDone:    make(chan struct{}),
		pending:       map[int64]chan agyRPCResponse{},
		dynamicCmds:   map[string]struct{}{},
		startupAuthCh: make(chan struct{}),
		commandsReady: make(chan struct{}),
		stdoutDone:    make(chan struct{}),
		stderrDone:    make(chan struct{}),
		waitDone:      make(chan struct{}),
	}
	go func() {
		w.waitErr = cmd.Wait()
		close(w.waitDone)
	}()
	go w.writeLoop()
	go w.readStdoutLoop()
	go w.readStderrLoop()

	initRaw, initRPCErr, initErr := w.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]any{"name": "persona", "version": "model-run"},
	}, w.startupAuthCh)
	if initErr != nil || initRPCErr != nil {
		authHit := w.sawStartupAuth()
		w.destroy()
		stderr := w.stderrText()
		if authHit || isAgyACPAuthError(initRPCErr) || isAgyLoggedOut(stderr) {
			return nil, agyWarmStartFallbackNoCache, nil
		}
		if isAgyTimeoutErr(ctx, initErr) {
			return nil, agyWarmStartTimeout, nil
		}
		if isAgyACPPermanentUnsupported(initRPCErr, stderr) {
			return nil, agyWarmStartUnsupported, nil
		}
		// Transient I/O, crash, or non-capability RPC failures fall back without permanently disabling warm reuse.
		return nil, agyWarmStartFallbackNoCache, nil
	}

	// Guardrail ①: strictly verify native per-session systemPrompt.append capability.
	var initRes struct {
		AgentCapabilities struct {
			Meta struct {
				Antigravity struct {
					SystemPrompt struct {
						Append bool `json:"append"`
					} `json:"systemPrompt"`
				} `json:"antigravity"`
			} `json:"_meta"`
		} `json:"agentCapabilities"`
		AuthMethods []struct {
			ID string `json:"id"`
		} `json:"authMethods"`
	}
	if err := json.Unmarshal(initRaw, &initRes); err != nil || !initRes.AgentCapabilities.Meta.Antigravity.SystemPrompt.Append {
		w.destroy()
		return nil, agyWarmStartUnsupported, nil
	}
	w.markInitialized()

	// Guardrail ③: authenticate with gemini-api-key when configured for Gemini provider and offered by initialize.
	if modelProvider == "gemini" {
		hasAPIKeyMethod := slices.ContainsFunc(initRes.AuthMethods, func(m struct {
			ID string `json:"id"`
		}) bool {
			return m.ID == "gemini-api-key"
		})
		if hasAPIKeyMethod {
			_, authRPCErr, authErr := w.call(ctx, "authenticate", map[string]any{"methodId": "gemini-api-key"}, nil)
			if authErr != nil {
				w.destroy()
				if isAgyTimeoutErr(ctx, authErr) {
					return nil, agyWarmStartTimeout, nil
				}
				return nil, agyWarmStartError, errors.New("model.run: " + checkOutput(cmp.Or(w.stderrText(), authErr.Error())))
			}
			if authRPCErr != nil {
				w.destroy()
				if isAgyACPAuthError(authRPCErr) {
					return nil, agyWarmStartError, &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + p.Launcher + " is not logged in here"}
				}
				return nil, agyWarmStartError, errors.New("model.run: " + checkOutput(authRPCErr.Error()))
			}
		}
	}

	return w, agyWarmStartOK, nil
}

func (n *Node) buildAgyWarmCmd(p proto.ModelRunParams, builtinAgyBacked bool, wsDir, isoHome string) (*exec.Cmd, error) {
	acpArgs := []string{
		"acp",
		"--agent", agyModelRunAgentName,
		"--include-default-customizations=false",
		"--disable-telemetry",
	}
	if builtinAgyBacked {
		bin := n.builtinAgyExecutable()
		if bin == "" {
			return nil, fmt.Errorf("model.run: %s CLI executable not found", p.Launcher)
		}
		cmd := exec.Command(bin, acpArgs...)
		cmd.Dir = wsDir
		cmd.Env = agyWarmProcessEnv(os.Environ(), isoHome)
		return cmd, nil
	}

	// Shell/PATH launcher: start via loginShell so ~/.zshrc / ~/.bashrc PATH, aliases, and functions resolve under
	// the user's real HOME, then unset ANTIGRAVITY_CLI_CONFIG_DIR and switch HOME (and USERPROFILE on Windows) to
	// isoHome right before invoking `agy acp`.
	var envExports strings.Builder
	envExports.WriteString("export HOME=" + scriptPath(isoHome) + " AGY_CLI_DISABLE_AUTO_UPDATE=true")
	if runtime.GOOS == "windows" {
		envExports.WriteString(" USERPROFILE=" + scriptPath(isoHome))
	}
	script := "cd " + scriptPath(wsDir) +
		" && unset ANTIGRAVITY_CLI_CONFIG_DIR && " +
		envExports.String() + " && " +
		n.launcherForScript(p.Launcher) +
		" acp --agent " + shellQuote(agyModelRunAgentName) +
		" --include-default-customizations=false --disable-telemetry"
	cmd := loginShell(context.Background(), script)
	return cmd, nil
}

func agyWarmProcessEnv(base []string, isoHome string) []string {
	out := make([]string, 0, len(base)+4)
	for _, kv := range base {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(k, "ANTIGRAVITY_CLI_CONFIG_DIR") ||
			strings.EqualFold(k, "AGY_CLI_DISABLE_AUTO_UPDATE") ||
			k == "HOME" ||
			(runtime.GOOS == "windows" && strings.EqualFold(k, "USERPROFILE")) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+isoHome, "AGY_CLI_DISABLE_AUTO_UPDATE=true")
	if runtime.GOOS == "windows" {
		out = append(out, "USERPROFILE="+isoHome)
	}
	return out
}

type agyACPConfigOption struct {
	ID           string `json:"id"`
	Category     string `json:"category"`
	CurrentValue string `json:"currentValue"`
}

func extractAgyConfigOptions(opts []agyACPConfigOption) (model, effort string) {
	for _, opt := range opts {
		if opt.CurrentValue == "" {
			continue
		}
		if opt.Category == "model" || opt.ID == "model" {
			model = opt.CurrentValue
		} else if opt.ID == "thought_level" {
			effort = opt.CurrentValue
		}
	}
	return model, effort
}

type agyACPUsage struct {
	InputTokens       int `json:"inputTokens"`
	OutputTokens      int `json:"outputTokens"`
	CachedReadTokens  int `json:"cachedReadTokens"`
	CachedWriteTokens int `json:"cachedWriteTokens"`
	CacheReadTokens   int `json:"cacheReadTokens"`
	CacheWriteTokens  int `json:"cacheWriteTokens"`
}

func mapAgyACPUsage(u *agyACPUsage) aiwire.Usage {
	if u == nil {
		return aiwire.Usage{}
	}
	cacheRead := u.CachedReadTokens
	if cacheRead == 0 {
		cacheRead = u.CacheReadTokens
	}
	cacheWrite := u.CachedWriteTokens
	if cacheWrite == 0 {
		cacheWrite = u.CacheWriteTokens
	}
	return aiwire.Usage{
		InputTokens:              u.InputTokens,
		OutputTokens:             u.OutputTokens,
		CacheReadInputTokens:     cacheRead,
		CacheCreationInputTokens: cacheWrite,
	}
}

func (w *agyWarmWorker) runTurn(
	ctx context.Context,
	p proto.ModelRunParams,
	timedOut error,
	onCommands func([]string),
) (ans proto.ModelRunAnswer, fallback bool, keep bool, counted bool, err error) {
	turn := newAgyTurnCollector()
	w.mu.Lock()
	w.turn = turn
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		if w.turn == turn {
			w.turn = nil
		}
		w.mu.Unlock()
	}()

	// 1. session/new with mcpServers: [] and per-session native systemPrompt.append.
	newParams := map[string]any{
		"cwd":        w.wsDir,
		"mcpServers": []any{},
	}
	if p.System != "" {
		newParams["_meta"] = map[string]any{
			"antigravity": map[string]any{
				"systemPrompt": map[string]any{
					"append": p.System,
				},
			},
		}
	}
	newRaw, newRPCErr, newErr := w.call(ctx, "session/new", newParams, turn.violationCh)
	if secErr := turn.securityErr(); secErr != nil {
		return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: %s", secErr.Error())
	}
	if newErr != nil {
		if isAgyTimeoutErr(ctx, newErr) {
			return proto.ModelRunAnswer{}, false, false, false, timedOut
		}
		return proto.ModelRunAnswer{}, false, false, false, w.transportError(p.Launcher, newErr)
	}
	if newRPCErr != nil {
		return proto.ModelRunAnswer{}, false, false, false, agyACPRPCError(p.Launcher, newRPCErr)
	}

	var newRes struct {
		SessionID string `json:"sessionId"`
		Models    *struct {
			CurrentModelID string `json:"currentModelId"`
		} `json:"models"`
		ConfigOptions []agyACPConfigOption `json:"configOptions"`
	}
	if err := json.Unmarshal(newRaw, &newRes); err != nil || strings.TrimSpace(newRes.SessionID) == "" {
		return proto.ModelRunAnswer{}, false, false, false, errors.New("model.run: agy session/new returned empty sessionId")
	}
	sid := strings.TrimSpace(newRes.SessionID)
	turn.setSessionID(sid)

	activeModel := ""
	activeEffort := ""
	if newRes.Models != nil && newRes.Models.CurrentModelID != "" {
		activeModel = newRes.Models.CurrentModelID
	}
	if cm, ce := extractAgyConfigOptions(newRes.ConfigOptions); cm != "" || ce != "" {
		if activeModel == "" {
			activeModel = cm
		}
		activeEffort = ce
	}
	turn.updateModel(activeModel, activeEffort)

	// 2. Guardrail ② Stage 2 check: if prompt contains '/', inspect any commands learned during session/new
	// (including newly pushed available_commands_update) BEFORE sending session/prompt.
	if strings.Contains(p.Prompt, "/") {
		if !w.hasCommands() {
			timer := time.NewTimer(agyWarmCommandsProbeWait)
			select {
			case <-w.commandsReady:
				timer.Stop()
			case <-w.stdoutDone:
				timer.Stop()
			case <-ctx.Done():
				timer.Stop()
				return proto.ModelRunAnswer{}, false, false, false, timedOut
			case <-timer.C:
			}
		}
		if dyn := w.snapshotDynamicCommands(); len(dyn) > 0 && onCommands != nil {
			onCommands(dyn)
		}
		if promptMatchesSlashCommand(p.Prompt, w.allSlashCommands()) {
			closeOK := w.closeAndCleanSession(sid)
			return proto.ModelRunAnswer{}, true, closeOK && turn.securityErr() == nil, closeOK, nil
		}
	} else if dyn := w.snapshotDynamicCommands(); len(dyn) > 0 && onCommands != nil {
		onCommands(dyn)
	}

	// 3. Apply explicit Model / Effort for this session.
	if p.Model != "" {
		resRaw, rpcErr, callErr := w.call(ctx, "session/set_model", map[string]any{
			"sessionId": sid,
			"modelId":   p.Model,
		}, turn.violationCh)
		if secErr := turn.securityErr(); secErr != nil {
			return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: %s", secErr.Error())
		}
		if callErr != nil {
			if isAgyTimeoutErr(ctx, callErr) {
				return proto.ModelRunAnswer{}, false, false, false, timedOut
			}
			return proto.ModelRunAnswer{}, false, false, false, w.transportError(p.Launcher, callErr)
		}
		if rpcErr != nil {
			return proto.ModelRunAnswer{}, false, false, false, agyACPRPCError(p.Launcher, rpcErr)
		}
		var setRes struct {
			ConfigOptions []agyACPConfigOption `json:"configOptions"`
		}
		if len(resRaw) > 0 && json.Unmarshal(resRaw, &setRes) == nil {
			cm, ce := extractAgyConfigOptions(setRes.ConfigOptions)
			turn.updateModel(cmp.Or(cm, p.Model), ce)
		} else {
			turn.updateModel(p.Model, "")
		}
	}

	if p.Effort != "" {
		_, curEffort := turn.getModel()
		suffixedMatch := p.Model != "" && strings.EqualFold(agyModelEffort(p.Model), p.Effort)
		if !suffixedMatch || (curEffort != "" && !strings.EqualFold(curEffort, p.Effort)) {
			resRaw, rpcErr, callErr := w.call(ctx, "session/set_config_option", map[string]any{
				"sessionId": sid,
				"configId":  "thought_level",
				"value":     p.Effort,
			}, turn.violationCh)
			if secErr := turn.securityErr(); secErr != nil {
				return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: %s", secErr.Error())
			}
			if callErr != nil {
				if isAgyTimeoutErr(ctx, callErr) {
					return proto.ModelRunAnswer{}, false, false, false, timedOut
				}
				return proto.ModelRunAnswer{}, false, false, false, w.transportError(p.Launcher, callErr)
			}
			if rpcErr != nil {
				return proto.ModelRunAnswer{}, false, false, false, agyACPRPCError(p.Launcher, rpcErr)
			}
			var setRes struct {
				ConfigOptions []agyACPConfigOption `json:"configOptions"`
			}
			if len(resRaw) > 0 && json.Unmarshal(resRaw, &setRes) == nil {
				cm, ce := extractAgyConfigOptions(setRes.ConfigOptions)
				turn.updateModel(cm, ce)
			}
		}
	}

	// Re-check dynamic commands in case available_commands_update arrived during set_model/set_config_option.
	if strings.Contains(p.Prompt, "/") {
		if dyn := w.snapshotDynamicCommands(); len(dyn) > 0 && onCommands != nil {
			onCommands(dyn)
		}
		if promptMatchesSlashCommand(p.Prompt, w.allSlashCommands()) {
			closeOK := w.closeAndCleanSession(sid)
			return proto.ModelRunAnswer{}, true, closeOK && turn.securityErr() == nil, closeOK, nil
		}
	}

	// 4. session/prompt.
	promptRaw, promptRPCErr, promptErr := w.call(ctx, "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt": []map[string]any{
			{"type": "text", "text": p.Prompt},
		},
	}, turn.violationCh)
	if secErr := turn.securityErr(); secErr != nil {
		return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: %s", secErr.Error())
	}
	if promptErr != nil {
		if isAgyTimeoutErr(ctx, promptErr) {
			// Guardrail ⑤: send best-effort session/cancel with a short bounded write deadline before tearing down.
			cancelCtx, cancelWrite := context.WithTimeout(context.Background(), agyWarmControlWriteWait)
			_ = w.notify(cancelCtx, "session/cancel", map[string]any{"sessionId": sid})
			cancelWrite()
			return proto.ModelRunAnswer{}, false, false, false, timedOut
		}
		return proto.ModelRunAnswer{}, false, false, false, w.transportError(p.Launcher, promptErr)
	}
	if promptRPCErr != nil {
		return proto.ModelRunAnswer{}, false, false, false, agyACPRPCError(p.Launcher, promptRPCErr)
	}

	var promptRes struct {
		StopReason string       `json:"stopReason"`
		Usage      *agyACPUsage `json:"usage"`
	}
	if len(promptRaw) > 0 {
		_ = json.Unmarshal(promptRaw, &promptRes)
	}
	if !isAgyNormalStopReason(promptRes.StopReason) {
		return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: agy ended with stopReason %q", promptRes.StopReason)
	}

	// 5. Guardrail ③: close session and immediately delete per-session disk artifacts.
	closeOK := w.closeAndCleanSession(sid)
	if secErr := turn.securityErr(); secErr != nil {
		return proto.ModelRunAnswer{}, false, false, false, fmt.Errorf("model.run: %s", secErr.Error())
	}

	finalModel, finalEffort := turn.getModel()
	if finalModel != "" && finalEffort != "" && agyModelEffort(finalModel) == "" && strings.EqualFold(p.Model, finalModel+"-"+finalEffort) {
		finalModel = p.Model
	}
	ans = proto.ModelRunAnswer{
		Text:  strings.TrimSuffix(turn.getText(), "\n"),
		Model: cmp.Or(p.Model, finalModel),
		Usage: mapAgyACPUsage(promptRes.Usage),
	}
	if p.Model == "" && finalModel != "" {
		ans.Model = finalModel
	} else if finalModel != "" && p.Model != "" {
		// Preserve reported model when agy resolves an unsuffixed base model to a concrete variant, or when p.Model was explicit.
		if agyModelEffort(finalModel) != "" {
			ans.Model = finalModel
		}
	}
	// An answer that is agy's quota or sign-in error line is returned as before, but its worker is not reused (#17860).
	return ans, false, closeOK && !agyAccountTrouble(ans.Text), true, nil
}

func (w *agyWarmWorker) closeAndCleanSession(sid string) bool {
	closeCtx, cancel := context.WithTimeout(context.Background(), agyWarmCloseSessionWait)
	defer cancel()
	_, rpcErr, callErr := w.call(closeCtx, "session/close", map[string]any{"sessionId": sid}, nil)
	w.cleanSessionFiles(sid)
	return rpcErr == nil && callErr == nil
}

func (w *agyWarmWorker) cleanSessionFiles(sid string) {
	if sid == "" || strings.ContainsAny(sid, `/\`) || sid == "." || sid == ".." || strings.Contains(sid, "..") {
		return
	}
	_ = os.RemoveAll(filepath.Join(w.isoData, "brain", sid))
	for _, suffix := range []string{"", "-wal", "-shm"} {
		_ = os.Remove(filepath.Join(w.isoData, "conversations", sid+".db"+suffix))
	}
	_ = os.Remove(filepath.Join(w.isoData, "annotations", sid+".pbtxt"))
}

func (w *agyWarmWorker) transportError(launcher string, callErr error) error {
	stderr := w.stderrText()
	if isAgyLoggedOut(stderr) {
		return &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + launcher + " is not logged in here"}
	}
	msg := checkOutput(cmp.Or(stderr, callErr.Error()))
	if msg == "" {
		msg = clipText(redactCheck(callErr.Error()), 400)
	}
	return errors.New("model.run: " + msg)
}

func isAgyTimeoutErr(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded)
}

// isAgyNormalStopReason reports whether an ACP session/prompt stopReason is a normal terminal stop.
// Matching cold CLI semantics (which returns any SUCCESS result), empty/omitted, end_turn,
// max_tokens, max_turn_requests, and refusal preserve the aggregated text and warm worker;
// cancelled or unknown stop reasons fail closed and destroy the worker.
func isAgyNormalStopReason(stopReason string) bool {
	switch stopReason {
	case "", "end_turn", "max_tokens", "max_turn_requests", "refusal":
		return true
	default:
		return false
	}
}

// isAgyACPPermanentUnsupported distinguishes permanent CLI/protocol incompatibility (no `acp` subcommand,
// missing ACP isolation flags, or JSON-RPC -32601 Method not found) from transient spawn, I/O, or auth errors.
func isAgyACPPermanentUnsupported(rpcErr *agyRPCError, stderr string) bool {
	if rpcErr != nil {
		if rpcErr.Code == -32601 || strings.Contains(strings.ToLower(rpcErr.Message), "method not found") {
			return true
		}
	}
	if isIsolationFlagError(stderr) {
		return true
	}
	low := strings.ToLower(stderr)
	for _, needle := range []string{
		"unknown command",
		"unknown subcommand",
		"unrecognized subcommand",
		"invalid subcommand",
		"no such command",
	} {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

func isAgyACPAuthError(e *agyRPCError) bool {
	if e == nil {
		return false
	}
	if e.Code == -32000 && (isAgyLoggedOut(e.Message) || isAgyLoggedOut(string(e.Data))) {
		return true
	}
	return isAgyLoggedOut(e.Message) || isAgyLoggedOut(string(e.Data))
}

func agyACPRPCError(launcher string, e *agyRPCError) error {
	if isAgyACPAuthError(e) {
		return &rpc.Error{Code: proto.CodeModelRunLoggedOut, Message: "model.run: " + launcher + " is not logged in here"}
	}
	msg := checkOutput(textfmt.StripTerminalEscapes(e.Error()))
	if msg == "" {
		msg = fmt.Sprintf("ACP error %d", e.Code)
	}
	return errors.New("model.run: " + msg)
}

func normalizeSlashCommandName(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimPrefix(s, "/")
	return strings.ToLower(strings.TrimSpace(s))
}

// promptMatchesSlashCommand reports whether prompt contains any `/<cmd>` invocation from cmds on path-safe
// word boundaries (case-insensitive). Matches `/plan`, `please check /plan now`, `(/plan)`, `run /plan.`,
// while avoiding false positives on filesystem paths or URLs like `/plan/file.txt`, `./plan`, `foo/plan`,
// `/plan.md`, `/planning`, or `/plan-v2`.
func promptMatchesSlashCommand(prompt string, cmds map[string]struct{}) bool {
	if len(cmds) == 0 || !strings.Contains(prompt, "/") {
		return false
	}
	for i := 0; i < len(prompt); i++ {
		if prompt[i] != '/' {
			continue
		}
		if i > 0 {
			prev, _ := utf8.DecodeLastRuneInString(prompt[:i])
			if isSlashCmdLeftDisallowed(prev) {
				continue
			}
		}
		rest := prompt[i+1:]
		for cmd := range cmds {
			if len(cmd) == 0 || len(rest) < len(cmd) {
				continue
			}
			if !strings.EqualFold(rest[:len(cmd)], cmd) {
				continue
			}
			after := rest[len(cmd):]
			if isSlashCmdRightBoundary(after) {
				return true
			}
		}
	}
	return false
}

func isSlashCmdLeftDisallowed(r rune) bool {
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
		return true
	}
	switch r {
	case '_', '/', '.', '-', ':', '\\', '~':
		return true
	}
	return false
}

func isSlashCmdRightBoundary(after string) bool {
	if after == "" {
		return true
	}
	next, size := utf8.DecodeRuneInString(after)
	if (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9') {
		return false
	}
	switch next {
	case '_', '-', '/', '\\':
		return false
	case '.':
		// Allow sentence-ending punctuation like "run /plan." while ignoring filename extensions like "/plan.md".
		if len(after) > size {
			r2, _ := utf8.DecodeRuneInString(after[size:])
			if (r2 >= 'a' && r2 <= 'z') || (r2 >= 'A' && r2 <= 'Z') || (r2 >= '0' && r2 <= '9') {
				return false
			}
		}
	}
	return true
}
