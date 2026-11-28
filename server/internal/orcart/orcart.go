// Package orcart talks to the runtime of the Orca desktop app (a git-worktree and terminal manager for coding agents)
// over its local socket (a unix socket on macOS and Linux, a named pipe on Windows): one JSON request per line
// {id, authToken, method, params}, answered by one JSON line {id, ok, result | error{code, message}} (slow calls send
// {"_keepalive": true} lines first). The socket and its token are in orca-runtime.json in Orca's data directory; the
// token changes every time Orca starts. Only unary methods work on this socket (streaming ones need Orca's paired
// WebSocket). See docs/integrations/orca.md "Orca source".
//
// Orca being absent (not installed, not running) is normal: calls fail fast with ErrNotRunning and log nothing until
// it was seen once.
package orcart

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ProtocolVersion is the runtime protocol this client speaks (Orca's RUNTIME_PROTOCOL_VERSION when it was written).
// A runtime refusing clients this old (minCompatibleRuntimeClientVersion above it), or older than MinServerVersion,
// is incompatible.
const (
	ProtocolVersion  = 3
	MinServerVersion = 3
)

// Client is one Orca runtime, found through its metadata file.
type Client struct {
	// MetaPath is orca-runtime.json (MetaPath()); empty: Orca is not there.
	MetaPath string
	// auto: MetaPath came from MetaPath() (New) and is looked up again when it cannot be read; a path set by hand
	// (tests, the live check) stays.
	auto bool
	// Log gets one line when calls start failing and one when they work again; Orca never seen running is not
	// logged. nil: silent. The auth token is never logged.
	Log func(format string, args ...any)
	// ReadOnly refuses, without sending, every method that could change Orca: only status.get, terminal.list,
	// worktree.ps, terminal.read, terminal.agentStatus, terminal.inspectProcess, session.tabs.listAll, the bots.* reads
	// (bots.roster, bots.chats, bots.items.list) and the structured sessions' reads (agentSession.history, .options,
	// .currentRequests) go out (live checks against the user's own Orca).
	ReadOnly bool

	seq   atomic.Int64
	trips episode

	mu     sync.Mutex
	meta   *runtimeMeta // the metadata last read; nil: read it on the next call
	compat *compat      // the status.get check of meta's runtime
}

// New is a client of this user's Orca: the metadata file is looked up again whenever it is not there (Orca started
// after the node, or in the other of its two data directories).
func New() *Client { return &Client{MetaPath: MetaPath(), auto: true} }

// MetaPath is the orca-runtime.json to read: $ORCA_USER_DATA_PATH's when set; else, of the files that exist in
// MetaPaths, the one written last (a runtime that ran before may have left its file behind); else the desktop app's.
func MetaPath() string {
	paths := MetaPaths()
	best, bestAt := "", time.Time{}
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && (best == "" || st.ModTime().After(bestAt)) {
			best, bestAt = p, st.ModTime()
		}
	}
	if best == "" && len(paths) > 0 {
		best = paths[0]
	}
	return best
}

// MetaPaths are where an Orca runtime of this user writes orca-runtime.json, the desktop app's first. Just
// $ORCA_USER_DATA_PATH's when set (Orca's own override). Otherwise two places:
//   - the desktop app's user data directory (Electron's userData for the app name "orca"): ~/Library/Application
//     Support/orca on macOS, %APPDATA%\orca on Windows, $XDG_CONFIG_HOME/orca (~/.config/orca) elsewhere;
//   - orcad's, the runtime another computer's Orca starts over SSH: $ORCA_USER_DATA, else $XDG_DATA_HOME/Orca, else
//     ~/.orca, on every platform (Orca 1.4.246's orcad).
func MetaPaths() []string {
	const name = "orca-runtime.json"
	if d := os.Getenv("ORCA_USER_DATA_PATH"); d != "" {
		return []string{filepath.Join(d, name)}
	}
	home, _ := os.UserHomeDir()
	var dirs []string
	switch runtime.GOOS {
	case "darwin":
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library", "Application Support", "orca"))
		}
	case "windows":
		if d := os.Getenv("APPDATA"); d != "" {
			dirs = append(dirs, filepath.Join(d, "orca"))
		}
	default:
		if cfg := os.Getenv("XDG_CONFIG_HOME"); cfg != "" {
			dirs = append(dirs, filepath.Join(cfg, "orca"))
		} else if home != "" {
			dirs = append(dirs, filepath.Join(home, ".config", "orca"))
		}
	}
	switch {
	case os.Getenv("ORCA_USER_DATA") != "":
		dirs = append(dirs, os.Getenv("ORCA_USER_DATA"))
	case os.Getenv("XDG_DATA_HOME") != "":
		dirs = append(dirs, filepath.Join(os.Getenv("XDG_DATA_HOME"), "Orca"))
	case home != "":
		dirs = append(dirs, filepath.Join(home, ".orca"))
	}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		out = append(out, filepath.Join(d, name))
	}
	return out
}

// runtimeMeta is what the client keeps of orca-runtime.json.
type runtimeMeta struct {
	// kind is the transport dialed (kindUnix or kindNamedPipe), socket its endpoint.
	kind      string
	socket    string
	token     string
	runtimeID string
}

// String never shows the token.
func (m *runtimeMeta) String() string { return "orca runtime at " + m.socket }

// The transport kinds Orca writes into orca-runtime.json that this client dials: a unix socket (macOS, Linux), a named
// pipe (Windows, endpoint \\.\pipe\orca-…). Orca also lists a "websocket" one, which needs pairing and is not used.
const (
	kindUnix      = "unix"
	kindNamedPipe = "named-pipe"
)

// ErrNotRunning: Orca is not running (no metadata file, no local transport in it, or its socket is gone / refuses).
var ErrNotRunning = errors.New("orca is not running")

// ErrIncompatible: the running Orca speaks a runtime protocol this client does not (status.get).
var ErrIncompatible = errors.New("orca runtime protocol is not compatible")

// Error is the runtime's error answer: Code is its stable code ("unauthorized", "terminal_handle_stale",
// "method_not_found"…).
type Error struct {
	Method  string
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" && e.Message != e.Code {
		return "orca " + e.Method + ": " + e.Code + ": " + e.Message
	}
	return "orca " + e.Method + ": " + e.Code
}

// Refused reports whether the runtime turned the request down before doing anything: bad or stale arguments, a
// terminal that is gone, exited, replaced (its incarnation) or not writable, a method it does not have, too busy. Other errors (runtime_error…) leave
// open whether it acted.
func Refused(err error) bool {
	var e *Error
	if !errors.As(err, &e) {
		return false
	}
	switch e.Code {
	case "bad_request", "unauthorized", "method_not_found", "method_not_supported", "invalid_argument",
		"terminal_handle_stale", "terminal_not_writable", "terminal_gone", "terminal_exited", "runtime_busy",
		"request_too_large":
		return true
	}
	if strings.HasPrefix(e.Code, "agent_session_") { // a structured session's write turned down (session.go)
		return e.Code != "agent_session_operation_unknown"
	}
	return strings.Contains(e.Code, "incarnation") // a fence (expectedIncarnationId) that did not hold
}

// IsCode: err is the runtime's error with that code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

const (
	// dialTimeout bounds connecting: the socket is local, anything slower means a wedged app.
	dialTimeout = time.Second
	// ListTimeout bounds terminal.list and worktree.ps, taken with every refresh of the node's sessions.
	ListTimeout = 3 * time.Second
	// CallTimeout bounds a quick call (status.get, terminal.read).
	CallTimeout = 5 * time.Second
	// SendTimeout bounds terminal.send: an agent prompt is a long poll on Orca's side (it settles the write and waits
	// for the submit), keepalive lines meanwhile.
	SendTimeout = 30 * time.Second
	// CloseTimeout bounds terminal.close.
	CloseTimeout = 10 * time.Second
	// WorktreeTimeout bounds worktree.create: Orca refreshes the base branch from its remote before `git worktree add`.
	WorktreeTimeout = 2 * time.Minute
	maxLine         = 64 << 20
	// compatRecheck: how long a status.get check stands (a runtime restarted with a new version rewrites the metadata
	// file, which is checked again anyway).
	compatRecheck = 5 * time.Minute
)

// readMeta reads orca-runtime.json: the first transport of localKinds it lists (in that order) and the token.
func (c *Client) readMeta() (*runtimeMeta, error) {
	if c.auto {
		// Each read (c.mu is held): the runtime that wrote its file last wins, as in MetaPath.
		c.MetaPath = MetaPath()
	}
	if c.MetaPath == "" {
		return nil, ErrNotRunning
	}
	b, err := os.ReadFile(c.MetaPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRunning, err)
	}
	var f struct {
		RuntimeID  string `json:"runtimeId"`
		AuthToken  string `json:"authToken"`
		Transports []struct {
			Kind     string `json:"kind"`
			Endpoint string `json:"endpoint"`
		} `json:"transports"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%w: %s is not valid JSON", ErrNotRunning, filepath.Base(c.MetaPath))
	}
	m := &runtimeMeta{token: f.AuthToken, runtimeID: f.RuntimeID}
pick:
	for _, kind := range localKinds {
		for _, t := range f.Transports {
			if t.Kind == kind && t.Endpoint != "" {
				m.kind, m.socket = kind, t.Endpoint
				break pick
			}
		}
	}
	if m.socket == "" || m.token == "" {
		return nil, fmt.Errorf("%w: no %s transport in %s", ErrNotRunning, strings.Join(localKinds, " / "),
			filepath.Base(c.MetaPath))
	}
	return m, nil
}

// currentMeta is the metadata to use, read when not known yet (or dropped after a failure).
func (c *Client) currentMeta() (*runtimeMeta, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.meta != nil {
		return c.meta, nil
	}
	m, err := c.readMeta()
	if err != nil {
		return nil, err
	}
	if c.compat != nil && c.compat.runtimeID != m.runtimeID {
		c.compat = nil
	}
	c.meta = m
	return m, nil
}

// forget drops the metadata m: the next call reads the file again (Orca restarted with a new socket or token).
func (c *Client) forget(m *runtimeMeta) {
	c.mu.Lock()
	if c.meta == m {
		c.meta = nil
	}
	c.mu.Unlock()
}

// Call runs one method within timeout and decodes its result into out (nil: ignored). The runtime's compatibility is
// checked first (once per runtime); a call refused as unauthorized, or that cannot connect, reads the metadata file
// again and is tried once more (Orca restarted).
func (c *Client) Call(ctx context.Context, timeout time.Duration, method string, params, out any) error {
	err := c.call(ctx, timeout, method, params, out, true)
	if !gaveUp(ctx) { // a caller giving up is not Orca failing
		c.note(method, err)
	}
	return err
}

// gaveUp: the caller's context ended, or its deadline passed (the connection's deadline, set from it, may fire a
// moment before the context notices).
func gaveUp(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	dl, ok := ctx.Deadline()
	return ok && !time.Now().Before(dl)
}

// readOnlyMethods are the calls a ReadOnly client makes.
var readOnlyMethods = map[string]bool{"status.get": true, "terminal.list": true, "worktree.ps": true, "terminal.read": true,
	"terminal.agentStatus": true, "terminal.inspectProcess": true, "session.tabs.listAll": true, "bots.roster": true,
	"bots.chats": true, "bots.items.list": true, "agentSession.history": true, "agentSession.options": true,
	"agentSession.currentRequests": true}

func (c *Client) call(ctx context.Context, timeout time.Duration, method string, params, out any, check bool) error {
	if c.ReadOnly && !readOnlyMethods[method] {
		return fmt.Errorf("orca %s: refused, the client is read-only", method)
	}
	for attempt := 0; ; attempt++ {
		m, err := c.currentMeta()
		if err != nil {
			return err
		}
		if check {
			if err := c.checkCompat(ctx, m); err != nil {
				if retryable(err) && attempt == 0 {
					c.forget(m)
					continue
				}
				return err
			}
		}
		err = c.callOnce(ctx, m, timeout, method, params, out)
		if err != nil && retryable(err) && attempt == 0 {
			c.forget(m)
			continue
		}
		return err
	}
}

// retryable: worth reading the metadata again (a stale token or socket).
func retryable(err error) bool {
	return IsCode(err, "unauthorized") || errors.Is(err, ErrNotRunning)
}

// compat is the status.get check of one runtime.
type compat struct {
	runtimeID string
	at        time.Time
	err       error // nil: compatible
	version   string
}

// checkCompat asks status.get once per runtime (and again after compatRecheck): its protocol version range must
// include this client's.
func (c *Client) checkCompat(ctx context.Context, m *runtimeMeta) error {
	c.mu.Lock()
	cp := c.compat
	c.mu.Unlock()
	if cp != nil && cp.runtimeID == m.runtimeID && time.Since(cp.at) < compatRecheck {
		return cp.err
	}
	var st Status
	if err := c.callOnce(ctx, m, CallTimeout, "status.get", nil, &st); err != nil {
		return err
	}
	cp = &compat{runtimeID: m.runtimeID, at: time.Now(), version: st.AppVersion, err: st.compatible()}
	c.mu.Lock()
	c.compat = cp
	c.mu.Unlock()
	return cp.err
}

// Status is status.get (the fields Persona reads).
type Status struct {
	AppVersion string `json:"appVersion"`
	// RuntimeProtocolVersion is the runtime's protocol; MinCompatibleRuntimeClientVersion the oldest client it serves.
	RuntimeProtocolVersion            int      `json:"runtimeProtocolVersion"`
	MinCompatibleRuntimeClientVersion int      `json:"minCompatibleRuntimeClientVersion"`
	ProtocolVersion                   int      `json:"protocolVersion"`
	Capabilities                      []string `json:"capabilities"`
}

// compatible: this client's protocol is within what the runtime serves.
func (s Status) compatible() error {
	v := s.RuntimeProtocolVersion
	if v == 0 {
		v = s.ProtocolVersion
	}
	switch {
	case v < MinServerVersion:
		return fmt.Errorf("%w: Orca %s speaks runtime protocol %d, Persona needs %d or later", ErrIncompatible, s.AppVersion, v,
			MinServerVersion)
	case s.MinCompatibleRuntimeClientVersion > ProtocolVersion:
		return fmt.Errorf("%w: Orca %s needs runtime clients of protocol %d or later, Persona speaks %d", ErrIncompatible,
			s.AppVersion, s.MinCompatibleRuntimeClientVersion, ProtocolVersion)
	}
	return nil
}

// dial connects to the runtime's socket within dialTimeout (and ctx). A socket that is gone or refuses means Orca is
// not running (it left its metadata file behind).
func dial(ctx context.Context, m *runtimeMeta) (net.Conn, error) {
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	if m.kind == kindNamedPipe {
		return dialPipe(dctx, m.socket)
	}
	return dialUnix(dctx, m.socket)
}

// dialUnix connects to a unix socket (AF_UNIX, which Windows has too).
func dialUnix(ctx context.Context, socket string) (net.Conn, error) {
	if _, err := os.Stat(socket); err != nil {
		return nil, fmt.Errorf("%w: socket: %v", ErrNotRunning, errors.Unwrap(err))
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w: %v", ErrNotRunning, syscall.ECONNREFUSED)
		}
		return nil, fmt.Errorf("orca: connect: %w", err)
	}
	return conn, nil
}

func (c *Client) callOnce(ctx context.Context, m *runtimeMeta, timeout time.Duration, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := dial(ctx, m)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	id := "j" + strconv.FormatInt(c.seq.Add(1), 10)
	if params == nil {
		params = struct{}{}
	}
	req, err := json.Marshal(map[string]any{"id": id, "authToken": m.token, "method": method, "params": params})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return fmt.Errorf("orca %s: write: %w", method, deadlineErr(ctx, err))
	}
	r := bufio.NewReaderSize(conn, 64<<10)
	for {
		line, err := readLine(r, maxLine)
		if err != nil {
			return fmt.Errorf("orca %s: read: %w", method, deadlineErr(ctx, err))
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var env struct {
			ID        json.RawMessage `json:"id"`
			OK        bool            `json:"ok"`
			Keepalive bool            `json:"_keepalive"`
			Result    json.RawMessage `json:"result"`
			Error     *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Meta struct {
				RuntimeID string `json:"runtimeId"`
			} `json:"_meta"`
		}
		if err := json.Unmarshal(line, &env); err != nil {
			return fmt.Errorf("orca %s: decode: %v", method, err)
		}
		if env.Keepalive {
			continue
		}
		var gotID string
		_ = json.Unmarshal(env.ID, &gotID)
		// A request Orca could not read (bad JSON, too large) is answered under the id "unknown": it is this one's.
		if gotID != id && !(gotID == "unknown" && !env.OK) {
			continue // not the answer to this request
		}
		if r := env.Meta.RuntimeID; r != "" && m.runtimeID != "" && r != m.runtimeID {
			// Another runtime answered than the metadata names (Orca restarted between reading the file and now): the
			// answer stands, the metadata is read again for the next call.
			c.forget(m)
		}
		if !env.OK {
			e := &Error{Method: method, Code: "error"}
			if env.Error != nil {
				e.Code, e.Message = env.Error.Code, env.Error.Message
			}
			return e
		}
		if out == nil || len(env.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("orca %s: decode result: %v", method, err)
		}
		return nil
	}
}

// deadlineErr names a timeout as such (the deadline set from ctx).
func deadlineErr(ctx context.Context, err error) error {
	if gaveUp(ctx) {
		return context.DeadlineExceeded
	}
	return err
}

// readLine reads one newline-terminated line of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, fmt.Errorf("line over %d bytes", max)
		}
		switch {
		case err == nil:
			return bytes.TrimRight(buf, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case len(buf) > 0 && errors.Is(err, io.EOF):
			return buf, nil
		default:
			return nil, err
		}
	}
}

// episode throttles logging of transport failures: one line when they start, one when they end.
type episode struct {
	mu       sync.Mutex
	failing  bool
	since    time.Time
	failures int
	seen     bool // it answered once: from then on, Orca going away is worth a line
}

func (c *Client) note(method string, err error) {
	if c.Log == nil {
		return
	}
	e := &c.trips
	e.mu.Lock()
	defer e.mu.Unlock()
	var oe *Error
	if err == nil || errors.As(err, &oe) && oe.Code != "unauthorized" { // an answer, even an error one, means it is reachable
		if e.failing {
			c.Log("orca: reachable again after %s (%d failed calls)", time.Since(e.since).Round(time.Second), e.failures)
		}
		e.failing, e.failures, e.seen = false, 0, true
		return
	}
	if errors.Is(err, ErrNotRunning) && !e.seen {
		return // never seen running: Orca is just not used on this machine
	}
	e.failures++
	if !e.failing {
		e.failing, e.since = true, time.Now()
		c.Log("orca: %s failed: %v", method, err)
	}
}
