package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/lcuconfirm"
	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/proto"
)

// lcuElicit asks the hub to show one LCU elicitation on the phone and returns the MCP result. A nil func, or any
// error, cancels: the model never gets an accept from this process by default.
type lcuElicit func(ctx context.Context, conversation string, params json.RawMessage) (map[string]any, error)

// lcuSession is one stdio MCP session with the `lcu` command. Calls are one at a time, so an elicitation that does
// not name its call belongs to the call that is in flight. The process stays up so a conversation's session id keeps
// the runtime's own session grants.
type lcuSession struct {
	mu       sync.Mutex
	wmu      sync.Mutex
	callMu   sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	wait     chan struct{}
	pending  map[int64]chan mcpMessage
	next     int64
	turns    map[string]string
	ask      lcuElicit
	inflight string
	inCtx    context.Context
	failed   error
}

func newLCUSession(ask lcuElicit) *lcuSession {
	return &lcuSession{pending: map[int64]chan mcpMessage{}, turns: map[string]string{}, ask: ask, wait: make(chan struct{})}
}

// lcuCommand is how the session starts `lcu` (tests pass a stand-in).
var lcuCommand = func() (string, []string) {
	return lcuBin(), nil
}

func (n *Node) lcuSession() *lcuSession {
	n.lcuMu.Lock()
	defer n.lcuMu.Unlock()
	if n.lcuSess == nil {
		n.lcuSess = newLCUSession(n.elicitLCU)
	}
	return n.lcuSess
}

func (n *Node) elicitLCU(ctx context.Context, conversation string, params json.RawMessage) (map[string]any, error) {
	if n.peer == nil {
		return map[string]any{"action": "cancel"}, nil
	}
	var out map[string]any
	request := map[string]any{"conversation": conversation, "params": json.RawMessage(params)}
	if callID := mcpwait.CallFrom(ctx); proto.ValidMCPWaitCallID(callID) {
		request["call_id"] = callID
	}
	if err := n.peer.Call(ctx, "computer.elicit", request, &out); err != nil {
		return map[string]any{"action": "cancel"}, nil
	}
	return out, nil
}

// lcuCallTimeout is how long one LCU call may wait, including the person answering on the phone. The card itself
// expires at 15 minutes; this is a little longer so the answer gets back.
const lcuCallTimeout = 16 * time.Minute

func (n *Node) lcuComputerCall(ctx context.Context, params json.RawMessage) (*proto.ComputerResult, error) {
	var p struct {
		Session      string          `json:"session"`
		Conversation string          `json:"conversation"`
		Tool         string          `json:"tool"`
		Args         json.RawMessage `json:"args"`
		KeepAwake    bool            `json:"keep_awake"`
		CallID       json.RawMessage `json:"call_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if p.KeepAwake {
		awake.operated()
	}
	name, args, err := lcuTool(p.Tool, p.Args)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, lcuCallTimeout)
	defer cancel()
	var callID string
	_ = json.Unmarshal(p.CallID, &callID)
	if proto.ValidMCPWaitCallID(callID) {
		ctx = mcpwait.WithCall(ctx, callID)
	}
	return n.lcuSession().call(ctx, name, args, p.Session, p.Conversation)
}

func (s *lcuSession) call(ctx context.Context, name string, args map[string]any, session, conversation string) (*proto.ComputerResult, error) {
	s.callMu.Lock()
	defer s.callMu.Unlock()
	if err := s.ensure(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	cmd, wait := s.cmd, s.wait
	s.mu.Unlock()
	if session == "" {
		session = "persona"
	}
	turn := s.turn(session)
	id, ch := s.begin(ctx, conversation)
	defer s.end(id)
	body, _ := json.Marshal(map[string]any{
		"name":      name,
		"arguments": args,
		"_meta": map[string]any{
			"x-codex-turn-metadata": map[string]any{"session_id": session, "turn_id": turn},
		},
	})
	if err := s.write(mcpMessage{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: body}); err != nil {
		return nil, s.writeFailed(cmd, err)
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, errors.New(msg.Error.Message)
		}
		return lcuToolResult(msg.Result)
	case <-ctx.Done():
		return nil, errors.New("操控电脑的确认超时或已取消")
	case <-wait:
		return nil, errLCUEnded
	}
}

func (s *lcuSession) begin(ctx context.Context, conversation string) (int64, chan mcpMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	id := s.next
	ch := make(chan mcpMessage, 1)
	s.pending[id] = ch
	s.inflight, s.inCtx = conversation, ctx
	return id, ch
}

func (s *lcuSession) end(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.inflight, s.inCtx = "", nil
	s.mu.Unlock()
}

func (s *lcuSession) turn(session string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.turns[session]; id != "" {
		return id
	}
	id := session + "-turn"
	s.turns[session] = id
	return id
}

func (s *lcuSession) ensure(ctx context.Context) error {
	s.mu.Lock()
	if s.stdin != nil && s.failed == nil {
		s.mu.Unlock()
		return nil
	}
	if s.failed != nil {
		s.resetLocked()
	}
	bin, args := lcuCommand()
	if bin == "" {
		s.mu.Unlock()
		return errors.New("这台 Mac 上没有装 LCU")
	}
	// The process outlives the call that started it. A cancelled confirmation must not kill the runtime.
	cmd := exec.Command(bin, args...)
	cmd.Env = lcuChildEnv()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return fmt.Errorf("启动 LCU：%w", err)
	}
	s.cmd, s.stdin, s.failed = cmd, stdin, nil
	s.wait = make(chan struct{})
	wait := s.wait
	s.mu.Unlock()
	go func() {
		s.readLoop(stdout)
		_ = cmd.Wait()
		s.mu.Lock()
		// Only the session's current process may mark it failed: a replaced one that exits late must not.
		if s.cmd == cmd && s.failed == nil {
			s.failed = errLCUEnded
		}
		s.mu.Unlock()
		close(wait)
	}()
	if err := s.initialize(ctx, cmd, wait); err != nil {
		s.mu.Lock()
		if s.cmd == cmd {
			s.failed = err
		}
		s.mu.Unlock()
		// A process that never finished the handshake is of no use, and one that ignores stdin EOF would stay.
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		select {
		case <-wait:
		case <-time.After(lcuExitWait):
		}
		return err
	}
	return nil
}

// lcuExitWait bounds how long a failed start waits for its killed process to be seen gone (a child of lcu can keep
// its stdout open after the kill).
const lcuExitWait = 2 * time.Second

func (s *lcuSession) resetLocked() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		// os.Process.Kill is a no-op on a process already waited for, so this never reaches a reused pid.
		_ = s.cmd.Process.Kill()
	}
	s.stdin, s.cmd, s.failed = nil, nil, nil
	s.pending = map[int64]chan mcpMessage{}
}

// initialize is the MCP handshake with cmd, whose exit closes exited.
func (s *lcuSession) initialize(ctx context.Context, cmd *exec.Cmd, exited <-chan struct{}) error {
	s.mu.Lock()
	s.next++
	id := s.next
	ch := make(chan mcpMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	params, _ := json.Marshal(map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{"elicitation": map[string]any{}},
		"clientInfo":      map[string]any{"name": "persona", "version": "0"},
	})
	if err := s.write(mcpMessage{JSONRPC: "2.0", ID: &id, Method: "initialize", Params: params}); err != nil {
		return s.writeFailed(cmd, err)
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return errors.New(msg.Error.Message)
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-exited:
		return errLCUEnded
	case <-time.After(30 * time.Second):
		return errors.New("LCU 没有完成 MCP 握手")
	}
	if err := s.write(mcpMessage{JSONRPC: "2.0", Method: "notifications/initialized"}); err != nil {
		return s.writeFailed(cmd, err)
	}
	return nil
}

// errLCUEnded is the error of a call whose lcu process ended.
var errLCUEnded = errors.New("LCU 的进程结束了")

// writeFailed is the error of a write to cmd's stdin that failed. A closed pipe (a broken pipe, or one cmd.Wait has
// closed) means the process ended, maybe before its exit is seen (a child of lcu can keep its stdout open): the
// session counts cmd as ended, so the next call starts a new one, and the error says so at once, wrapping the
// write's. Any other write error is itself.
func (s *lcuSession) writeFailed(cmd *exec.Cmd, err error) error {
	if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	s.mu.Lock()
	if s.cmd == cmd && s.failed == nil { // only the session's current process: a replaced one's late write is not
		s.failed = errLCUEnded
	}
	s.mu.Unlock()
	return fmt.Errorf("%w: %w", errLCUEnded, err)
}

func (s *lcuSession) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		raw, err := readMCP(br)
		if err != nil {
			return
		}
		var msg mcpMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		if msg.Method != "" && msg.ID != nil {
			go s.serve(msg)
			continue
		}
		if msg.ID == nil {
			continue
		}
		s.mu.Lock()
		ch := s.pending[*msg.ID]
		s.mu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

func (s *lcuSession) serve(msg mcpMessage) {
	var result map[string]any
	if msg.Method == "elicitation/create" {
		result = s.onElicit(msg.Params)
	} else {
		result = map[string]any{"action": "cancel"}
	}
	body, _ := json.Marshal(result)
	_ = s.write(mcpMessage{JSONRPC: "2.0", ID: msg.ID, Result: body})
}

func (s *lcuSession) onElicit(params json.RawMessage) map[string]any {
	s.mu.Lock()
	ask, conv, ctx := s.ask, s.inflight, s.inCtx
	s.mu.Unlock()
	if ask == nil || ctx == nil {
		return map[string]any{"action": "cancel"}
	}
	out, err := ask(ctx, conv, params)
	if err != nil || out == nil {
		return map[string]any{"action": "cancel"}
	}
	return normalizeElicit(out)
}

// normalizeElicit forwards accept, decline or cancel and nothing else. A missing action cancels.
func normalizeElicit(m map[string]any) map[string]any {
	action, _ := m["action"].(string)
	switch action {
	case "accept", "decline", "cancel":
		return m
	default:
		return map[string]any{"action": "cancel"}
	}
}

func (s *lcuSession) write(msg mcpMessage) error {
	msg.JSONRPC = "2.0"
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// LCU MCP stdio is one JSON object per line (newline-delimited), not LSP Content-Length framing.
	frame := string(body) + "\n"
	s.mu.Lock()
	w := s.stdin
	s.mu.Unlock()
	if w == nil {
		return errors.New("LCU 没有连上")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = io.WriteString(w, frame)
	return err
}

// attachPipes is the session's stdio for a test, already past process start. The caller speaks MCP on the other ends.
func (s *lcuSession) attachPipes(stdout io.Reader, stdin io.WriteCloser) {
	s.mu.Lock()
	s.stdin, s.failed = stdin, nil
	s.wait = make(chan struct{})
	wait := s.wait
	s.mu.Unlock()
	go func() {
		s.readLoop(stdout)
		s.mu.Lock()
		if s.failed == nil {
			s.failed = errors.New("closed")
		}
		s.mu.Unlock()
		close(wait)
	}()
}

func lcuToolResult(raw json.RawMessage) (*proto.ComputerResult, error) {
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return nil, errors.New("LCU 的回答不是 JSON")
	}
	out := &proto.ComputerResult{}
	var text strings.Builder
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			if text.Len() > 0 {
				text.WriteByte('\n')
			}
			text.WriteString(c.Text)
		case "image":
			if out.Image == nil && c.Data != "" {
				mime := c.MimeType
				if mime == "" {
					mime = "image/png"
				}
				out.Image = &proto.Image{Mime: mime, Data: c.Data}
			}
		}
	}
	if res.IsError {
		msg := strings.TrimSpace(text.String())
		if msg == "" {
			msg = "LCU 拒绝了这次操控"
		}
		return nil, errors.New(msg)
	}
	if json.Valid([]byte(text.String())) && strings.TrimSpace(text.String()) != "" {
		out.Output = json.RawMessage(text.String())
	} else {
		out.Text = text.String()
	}
	return out, nil
}

// lcuTool is the Cua Driver tool name the hub sent, as an LCU `js` call.
func lcuTool(tool string, raw json.RawMessage) (string, map[string]any, error) {
	args := map[string]any{}
	if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
		return "", nil, errors.New("computer.call: args must be an object")
	}
	if args == nil { // "args": null is no args, not "arguments": null to LCU
		args = map[string]any{}
	}
	switch tool {
	case "js", "js_reset":
		return tool, args, nil
	case "run_script":
		code, _ := args["script"].(string)
		out := map[string]any{"code": code}
		if t, ok := args["timeout_ms"]; ok {
			out["timeout_ms"] = t
		}
		return "js", out, nil
	case "run_steps":
		steps, _ := json.Marshal(args["steps"])
		app, _ := args["app"].(string)
		code, err := lcuconfirm.StepsJS(app, steps)
		if err != nil {
			return "", nil, err
		}
		return "js", map[string]any{"code": code}, nil
	case "get_desktop_state", "get_accessibility_tree", "get_window_state", "list_apps", "list_windows":
		return "js", map[string]any{"code": lcuconfirm.StateJS("", "")}, nil
	default:
		return "", nil, fmt.Errorf("computer.call: %q is not a tool the hub may call", tool)
	}
}

type mcpMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      *int64          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// readMCP reads one newline-delimited JSON MCP message (LCU stdio). Content-Length / LSP framing is not used.
func readMCP(r *bufio.Reader) (json.RawMessage, error) {
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if line[0] != '{' {
			// Official cua-repl reads line-by-line; non-JSON lines (e.g. leftover Content-Length) are skipped there too,
			// but we only speak line-delimited JSON now — reject anything else so a framing bug fails loudly.
			return nil, errors.New("bad MCP frame: expected JSON line")
		}
		return line, nil
	}
}

// lcuChildEnv is the environment LCU's process gets, without values whose names look like secrets. The runtime
// does not need them, and a desktop it controls must not read them back.
func lcuChildEnv() []string {
	var out []string
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		u := strings.ToUpper(name)
		if strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") || strings.Contains(u, "PASSWORD") || strings.Contains(u, "_KEY") {
			continue
		}
		out = append(out, e)
	}
	return out
}
