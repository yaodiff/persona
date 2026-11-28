package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The T3 Code engine (docs/integrations/engines.md "T3 Code"). T3 Code (github.com/pingdotgg/t3code) runs coding agents
// (Codex, Claude Code and others) in threads its local server owns; its desktop, web and phone clients all talk to that
// server. While the machine's engine is T3 Code, the node runs an in-process runtime bridge (runtimes.go) to it:
//
//   - It finds the server by the runtime state file the server writes (<T3 home>/userdata/server-runtime.json, the T3
//     home being $T3CODE_HOME or ~/.t3), which names its loopback origin, pid and version.
//   - It reads threads and their timelines through the server's MCP endpoint (/mcp, the tools outside agents use:
//     t3_project_list, t3_thread_list, t3_thread_read, t3_thread_interrupt), signed in with a session it issues itself
//     on this machine with the t3 command line (`t3 auth session issue`), kept in memory and revoked when the bridge
//     stops.
//   - It starts threads and sends messages with `t3 thread start` and `t3 thread send`, the command line T3 Code
//     documents for scripts on the host. These run with the machine owner's authority, as T3 Code's own clients do, so
//     a thread in any mode takes them. A session issued with `t3 auth session issue` carries T3 Code's narrowest mode
//     ceiling (approval-required), which would refuse sends to threads in the default full-access mode.
//
//   - It reads what a live thread waits on, so a thread that asks shows as Needs you, and raises its questions as cards
//     where T3 Code takes the bridge's answer (t3codebridge_questions.go).
//
// What T3 Code does not let the bridge do stays in T3 Code: approvals (its MCP tools cannot answer one), answers in a
// thread above the bridge's mode ceiling, rewinds, worktree management and mode changes.

// t3codeRuntimeID is the runtime id the T3 Code bridge registers with (and its sessions' space kind and host).
const t3codeRuntimeID = proto.EngineT3Code

var (
	// t3codeBridgePoll is how often the bridge refreshes T3 Code's threads.
	t3codeBridgePoll = 2 * time.Second
	// t3codeBridgeRestart is the wait before the bridge looks for the server again after its loop ends.
	t3codeBridgeRestart = 3 * time.Second
	// t3codeSessionTTL is how long a session the bridge issues lasts when the bridge never revokes it (the node died).
	t3codeSessionTTL = "7d"
	// t3codeListMost is the most threads the bridge lists (newest first).
	t3codeListMost = 300
	// t3codeReadPages is the most timeline pages (of 100 items) one session.items reads.
	t3codeReadPages = 20
)

// t3codeServerState is what the T3 server writes to server-runtime.json while it runs (PersistedServerRuntimeState).
type t3codeServerState struct {
	Version       int    `json:"version"`
	PID           int    `json:"pid"`
	Port          int    `json:"port"`
	Origin        string `json:"origin"`
	ServerVersion string `json:"serverVersion"`

	mcpEndpoint string // the MCP endpoint built from the parsed, checked origin; set by t3codeServerOf
}

// t3codeHome is the T3 home: $T3CODE_HOME, else ~/.t3 (T3 Code's resolveBaseDir).
func t3codeHome() string {
	if v := strings.TrimSpace(os.Getenv("T3CODE_HOME")); v != "" {
		return proto.ExpandHome(v, userHome())
	}
	if home := userHome(); home != "" {
		return filepath.Join(home, ".t3")
	}
	return ""
}

// t3codeServerOf reads the running server's state under home: the packaged server's (userdata) first, then a
// development server's (dev). ok only for a state that names a loopback http origin and a process that is alive.
func t3codeServerOf(home string) (t3codeServerState, bool) {
	if home == "" {
		return t3codeServerState{}, false
	}
	for _, dir := range []string{"userdata", "dev"} {
		raw, err := os.ReadFile(filepath.Join(home, dir, "server-runtime.json"))
		if err != nil {
			continue
		}
		var st t3codeServerState
		if json.Unmarshal(raw, &st) != nil || st.PID <= 0 {
			continue
		}
		endpoint, ok := t3codeMCPEndpoint(st.Origin)
		if !ok {
			continue
		}
		st.mcpEndpoint = endpoint
		if !t3codeAlive(st.PID) {
			continue
		}
		return st, true
	}
	return t3codeServerState{}, false
}

// t3codeAlive reports whether the server's process is running; tests replace it.
var t3codeAlive = osproc.Alive

// t3codeMCPEndpoint is the MCP endpoint of origin when origin is a plain http origin on this computer (127.0.0.1, ::1
// or localhost): ok is false otherwise. The bridge sends the session it issued only there, so the origin is parsed the
// way the HTTP client will read it, and anything that could move the request elsewhere or hide where it goes (user
// info, a path, a query, a fragment, an opaque form) is refused. The endpoint is built from the parsed host, never by
// joining raw strings.
func t3codeMCPEndpoint(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Opaque != "" || u.User != nil || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return "", false
	}
	return (&url.URL{Scheme: "http", Host: u.Host, Path: "/mcp"}).String(), true
}

// t3codeBin is the t3 command line: on PATH, else the launcher the desktop app writes in the T3 home (bin/t3), else
// the one `t3 install` puts in ~/.local/bin. "" when there is none. Tests replace it.
var t3codeBin = func() string {
	name := "t3"
	if runtime.GOOS == "windows" {
		name = "t3.cmd"
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	var cands []string
	if home := t3codeHome(); home != "" {
		cands = append(cands, filepath.Join(home, "bin", name))
	}
	if home := userHome(); home != "" {
		cands = append(cands, filepath.Join(home, ".local", "bin", name))
	}
	for _, c := range cands {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	return ""
}

// t3codeRun runs the t3 command line with args (stdin when not ""), and answers its standard output. A failure carries
// the first line of its standard error, which is how the t3 command line reports an expected failure. Tests replace it.
var t3codeRun = func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
	bin := t3codeBin()
	if bin == "" {
		return nil, errT3CodeNoCLI
	}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if line, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n"); line != "" {
			return nil, fmt.Errorf("t3 %s: %s", args[0], strings.TrimSpace(line))
		}
		return nil, fmt.Errorf("t3 %s: %w", args[0], err)
	}
	return out, nil
}

var errT3CodeNoCLI = errors.New("这台电脑上找不到 t3 命令行")

// t3codeBridgeState holds the in-process T3 Code bridge lifecycle on RuntimeHost.
type t3codeBridgeState struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
	status string
}

// setT3CodeBridge starts or stops the T3 Code runtime bridge on rh.
func (rh *RuntimeHost) setT3CodeBridge(on bool) {
	b := &rh.t3code
	b.mu.Lock()
	defer b.mu.Unlock()
	if on == (b.cancel != nil) {
		return
	}
	if !on {
		b.cancel()
		b.cancel = nil
		done := b.done
		b.mu.Unlock()
		<-done
		b.mu.Lock()
		rh.logger.Printf("t3code bridge: stopped (the engine is not T3 Code any more)")
		return
	}
	base := rh.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	b.cancel, b.done, b.status = cancel, make(chan struct{}), ""
	go rh.t3codeBridgeLoop(ctx, b.done)
}

func (rh *RuntimeHost) t3codeBridgeStatus() string {
	rh.t3code.mu.Lock()
	defer rh.t3code.mu.Unlock()
	if rh.t3code.cancel == nil {
		return ""
	}
	return rh.t3code.status
}

func (rh *RuntimeHost) setT3CodeStatus(s string) {
	rh.t3code.mu.Lock()
	rh.t3code.status = s
	rh.t3code.mu.Unlock()
}

func (rh *RuntimeHost) t3codeBridgeLoop(ctx context.Context, done chan struct{}) {
	defer close(done)
	for ctx.Err() == nil {
		if st, ok := t3codeServerOf(t3codeHome()); ok {
			err := rh.runT3CodeBridge(ctx, st)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				rh.setT3CodeStatus(err.Error())
				rh.logger.Printf("t3code bridge: %v (again in %s)", err, t3codeBridgeRestart)
			}
		} else {
			rh.setT3CodeStatus("")
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(t3codeBridgeRestart):
		}
	}
}

func (rh *RuntimeHost) runT3CodeBridge(ctx context.Context, st t3codeServerState) error {
	// If an external runtime connection already owns "t3code", yield until it leaves.
	if rh.reg != nil {
		rh.reg.mu.Lock()
		existing := rh.reg.byRuntime(t3codeRuntimeID)
		rh.reg.mu.Unlock()
		if existing != nil {
			return nil
		}
	}
	sess, err := issueT3CodeSession(ctx)
	if err != nil {
		return err
	}
	// Revoked however the bridge ends, on a context of its own: ctx may be over already.
	defer func() {
		rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := t3codeRun(rctx, "", "auth", "session", "revoke", sess.SessionID); err != nil {
			rh.logger.Printf("t3code bridge: revoking its session: %v", err)
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hostEnd, bridgeEnd := net.Pipe()
	go func() {
		rh.serveConn(ctx, hostEnd, "")
		cancel()
	}()
	b := newT3CodeBridge(newT3CodeMCP(st.mcpEndpoint, sess.Token), bridgeEnd, rh.logger.Printf)
	rh.setT3CodeStatus("")
	ver := firstOf(st.ServerVersion, rh.version)
	return b.run(ctx, ver)
}

// t3codeSession is a session `t3 auth session issue --json` issued.
type t3codeSession struct {
	SessionID string `json:"sessionId"`
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
}

// issueT3CodeSession issues the bridge's MCP client session: subject mcp-client (the only one /mcp takes), reading and
// operating orchestration, labelled so the user can tell it apart in T3 Code's Settings › Sharing & access.
func issueT3CodeSession(ctx context.Context) (t3codeSession, error) {
	out, err := t3codeRun(ctx, "", "auth", "session", "issue",
		"--subject", "mcp-client",
		"--scope", "orchestration:read", "--scope", "orchestration:operate",
		"--label", "Persona", "--ttl", t3codeSessionTTL, "--json")
	if err != nil {
		return t3codeSession{}, err
	}
	var s t3codeSession
	if err := json.Unmarshal(out, &s); err != nil || s.Token == "" || s.SessionID == "" {
		return t3codeSession{}, errors.New("t3 auth session issue 没有给出会话")
	}
	return s, nil
}

// t3codeEngine is T3 Code as found on this machine: available when its bridge is connected, its server runs or the
// t3 command line is there; the version is the server's. Note says why a running server cannot be used.
func (n *Node) t3codeEngine(ctx context.Context) proto.Engine {
	e := proto.Engine{ID: proto.EngineT3Code, Name: proto.EngineNames[proto.EngineT3Code]}
	if n.runtimes != nil {
		n.runtimes.mu.Lock()
		h := n.runtimes.byRuntime(t3codeRuntimeID)
		n.runtimes.mu.Unlock()
		if h != nil {
			h.mu.Lock()
			e.Version = h.hello.Version
			h.mu.Unlock()
			e.Available = true
			return e
		}
	}
	st, running := t3codeServerOf(t3codeHome())
	bin := t3codeBin()
	e.Available = running || bin != ""
	if running {
		e.Version = st.ServerVersion
		switch {
		case bin == "":
			e.Note = "T3 Code 在运行，但这台电脑上找不到 t3 命令行"
		case n.runtimeHost != nil:
			if s := n.runtimeHost.t3codeBridgeStatus(); s != "" {
				e.Note = "T3 Code 在运行，但 Persona 连不上它：" + s
			}
		}
	}
	return e
}

// t3codeBridge bridges T3 Code's server to the node's runtime protocol.
type t3codeBridge struct {
	mcp  *t3codeMCP
	conn net.Conn
	peer *rpc.Peer
	logf func(string, ...any)
	wake chan struct{} // a refresh now (after a change Persona made)

	mu       sync.Mutex
	threadOf map[string]string             // Persona session_id -> T3 thread id
	projects map[string]t3codeProject      // T3 project id -> project
	threads  map[string]t3codeThreadListed // T3 thread id -> last listed
	asks     map[string]*t3codeAsk         // "<thread id>/<request id>" -> a question's card (t3codebridge_questions.go)
	pushed   string                        // last marshalled sessions list
}

// t3codeProject is a project as t3_project_list answers it (the fields the bridge reads).
type t3codeProject struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	WorkspaceRoot string `json:"workspaceRoot"`
}

// t3codeThreadListed is a thread as t3_thread_list answers it (OrchestratorMcpThreadListItem, the fields the bridge
// reads).
type t3codeThreadListed struct {
	ThreadID           string `json:"threadId"`
	ProjectID          string `json:"projectId"`
	Title              string `json:"title"`
	Status             string `json:"status"`
	ProviderInstanceID string `json:"providerInstanceId"`
	Model              string `json:"model"`
	RuntimeMode        string `json:"runtimeMode"`
	InteractionMode    string `json:"interactionMode"`
	ParentThreadID     string `json:"parentThreadId"`
	ItemCount          int    `json:"itemCount"`
	Settled            bool   `json:"settled"`
	UpdatedAt          string `json:"updatedAt"`

	// pending is how many approvals and questions the thread waits on (its read's pendingRequestCount), read for live
	// threads only; nil when not read or from a server that does not count them.
	pending *int
}

func newT3CodeBridge(mcp *t3codeMCP, conn net.Conn, logf func(string, ...any)) *t3codeBridge {
	return &t3codeBridge{
		mcp:      mcp,
		conn:     conn,
		logf:     logf,
		wake:     make(chan struct{}, 1),
		threadOf: map[string]string{},
		projects: map[string]t3codeProject{},
		threads:  map[string]t3codeThreadListed{},
		asks:     map[string]*t3codeAsk{},
	}
}

func (b *t3codeBridge) run(ctx context.Context, version string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.conn.Close()
	defer b.mcp.close()
	if err := b.mcp.initialize(ctx); err != nil {
		return err
	}
	b.peer = rpc.New("t", b.conn)
	b.peer.OnRequest = b.request
	served := make(chan error, 1)
	go func() { served <- b.peer.Serve(ctx, b.conn) }()

	hello := map[string]any{
		"runtime":  t3codeRuntimeID,
		"name":     proto.EngineNames[proto.EngineT3Code],
		"agent":    "codex",
		"version":  version,
		"instance": newUUID(),
		"caps":     map[string]any{"send": true, "interrupt": true, "start": true, "close": true},
	}
	if err := b.peer.Call(ctx, "runtime.hello", hello, nil); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	t := time.NewTicker(t3codeBridgePoll)
	defer t.Stop()
	for {
		if err := b.refresh(ctx); err != nil {
			return err
		}
		select {
		case err := <-served:
			if err == nil {
				err = errors.New("the runtime host closed the connection")
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		case <-b.wake:
		}
	}
}

// refresh lists T3 Code's projects and threads, with what the live ones wait on, pushes them as sessions when they
// changed, and keeps the cards of their questions up to date.
func (b *t3codeBridge) refresh(ctx context.Context) error {
	projects, err := b.listProjects(ctx)
	if err != nil {
		return err
	}
	threads, err := b.listThreads(ctx)
	if err != nil {
		return err
	}
	b.readPending(ctx, threads)
	defer b.syncQuestions(ctx, threads)
	threadOf := map[string]string{}
	byID := map[string]t3codeThreadListed{}
	out := make([]map[string]any, 0, len(threads))
	for _, th := range threads {
		row, sid := mapT3CodeThread(th, projects[th.ProjectID])
		if sid == "" {
			continue
		}
		out = append(out, row)
		threadOf[sid] = th.ThreadID
		byID[th.ThreadID] = th
	}
	b.mu.Lock()
	b.threadOf, b.projects, b.threads = threadOf, projects, byID
	b.mu.Unlock()
	if raw, err := json.Marshal(out); err == nil && string(raw) != b.pushed {
		if b.peer.Notify("runtime.sessions", map[string]any{"sessions": out}) == nil {
			b.pushed = string(raw)
		}
	}
	return nil
}

func (b *t3codeBridge) listProjects(ctx context.Context) (map[string]t3codeProject, error) {
	out := map[string]t3codeProject{}
	cursor := 0
	for page := 0; page < 20; page++ {
		var res struct {
			Projects   []t3codeProject `json:"projects"`
			NextCursor *int            `json:"nextCursor"`
		}
		if err := b.mcp.call(ctx, "t3_project_list", map[string]any{"cursor": cursor, "limit": 100}, &res); err != nil {
			return nil, err
		}
		for _, p := range res.Projects {
			out[p.ID] = p
		}
		if res.NextCursor == nil || *res.NextCursor <= cursor {
			break
		}
		cursor = *res.NextCursor
	}
	return out, nil
}

func (b *t3codeBridge) listThreads(ctx context.Context) ([]t3codeThreadListed, error) {
	var out []t3codeThreadListed
	cursor := 0
	for len(out) < t3codeListMost {
		var res struct {
			Threads    []t3codeThreadListed `json:"threads"`
			NextCursor *int                 `json:"nextCursor"`
		}
		args := map[string]any{"allProjects": true, "includeSubagents": false, "cursor": cursor, "limit": 100}
		if err := b.mcp.call(ctx, "t3_thread_list", args, &res); err != nil {
			return nil, err
		}
		out = append(out, res.Threads...)
		if res.NextCursor == nil || *res.NextCursor <= cursor {
			break
		}
		cursor = *res.NextCursor
	}
	if len(out) > t3codeListMost {
		out = out[:t3codeListMost]
	}
	return out, nil
}

// t3codeSessionID is the Persona session id of a T3 thread: "t3code-" and its id, "" when that is no valid id.
func t3codeSessionID(threadID string) string {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return ""
	}
	sid := "t3code-" + id
	if !runtimeIDRe.MatchString(sid) {
		return ""
	}
	return sid
}

// t3codeAgent is the Persona agent kind of a T3 provider instance: its default instances are named after their driver
// (codex, claudeAgent, cursor, opencode, pi…), and a custom one usually keeps the driver in its name.
func t3codeAgent(instance string) string {
	s := strings.ToLower(instance)
	switch {
	case strings.Contains(s, "claude"):
		return "claude"
	case strings.Contains(s, "codex"):
		return "codex"
	case strings.Contains(s, "opencode"):
		return "opencode"
	case strings.Contains(s, "gemini"):
		return "gemini"
	case s == "pi" || strings.HasPrefix(s, "pi-") || strings.HasSuffix(s, "-pi"):
		return "pi"
	case strings.Contains(s, "cursor"):
		return "cursor"
	}
	return "codex" // T3 Code's default provider
}

// t3codeMode is the Persona permission mode of a thread's runtime and interaction modes (T3 Code's narrowest runtime
// mode asks before every change, as Claude Code's default does).
func t3codeMode(runtimeMode, interactionMode string) string {
	if interactionMode == "plan" {
		return "plan"
	}
	switch runtimeMode {
	case "approval-required":
		return "default"
	case "auto-accept-edits":
		return "acceptEdits"
	case "auto":
		return "auto"
	case "full-access":
		return proto.ModeBypass
	}
	return ""
}

// t3codeStatus is the Persona status of a thread's status (idle, or its latest run's) and the approvals and questions
// it waits on (nil: not known). A run that asks keeps running in T3 Code, so only the count says it waits on the user;
// "waiting" is background work the run still holds (T3 Code's threadStatus). From a server that does not count, the
// count is nil and "waiting" stays the only sign of one.
func t3codeStatus(status string, items int, pending *int) string {
	if pending != nil && *pending > 0 {
		return proto.StatusBlocked
	}
	switch status {
	case "preparing", "queued", "starting", "running":
		return proto.StatusWorking
	case "waiting":
		if pending != nil {
			return proto.StatusWorking
		}
		return proto.StatusBlocked
	case "interrupted", "cancelled":
		return proto.StatusInterrupted
	case "completed", "failed", "rolled_back":
		return proto.StatusDone
	}
	if items > 0 {
		return proto.StatusDone
	}
	return proto.StatusIdle
}

func mapT3CodeThread(th t3codeThreadListed, p t3codeProject) (map[string]any, string) {
	sid := t3codeSessionID(th.ThreadID)
	if sid == "" {
		return nil, ""
	}
	cwd := strings.TrimSpace(p.WorkspaceRoot)
	title := strings.TrimSpace(th.Title)
	if title == "" {
		title = firstOf(strings.TrimSpace(p.Title), filepath.Base(cwd), "T3 Code")
	}
	updated := time.Now().UTC()
	if t, err := time.Parse(time.RFC3339Nano, th.UpdatedAt); err == nil {
		updated = t.UTC()
	}
	row := map[string]any{
		"session_id": sid,
		"title":      title,
		"cwd":        cwd,
		"status":     t3codeStatus(th.Status, th.ItemCount, th.pending),
		"updated_at": updated,
		"rev":        updated.UnixMilli(),
		"agent":      t3codeAgent(th.ProviderInstanceID),
	}
	if cwd != "" {
		row["space_id"] = cwd
		row["space_name"] = firstOf(strings.TrimSpace(p.Title), filepath.Base(cwd))
	}
	if m := strings.TrimSpace(th.Model); m != "" {
		row["model"] = m
	}
	if mode := t3codeMode(th.RuntimeMode, th.InteractionMode); mode != "" {
		row["permission_mode"] = mode
	}
	return row, sid
}
