// Package nodeclient is the client side of the node's local socket (node.sock): what the short-lived commands a
// computer runs for its Agents (`personad hook`, `personad mcp`, `personad ask`, `personad notify`) use to reach the
// node. It holds the socket path, dialing, the request and reply types, and resuming across a node restart, and it
// imports only contracts (proto, host, identity) and procinfo, never the node itself: a command links this package,
// not all of internal/node (the guard test in this package keeps it so). The node serves the same types
// (internal/node/hooks.go).
package nodeclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// SocketPath is where hook and mcp subprocesses reach the node.
func SocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.SocketDir(home), "node.sock")
}

// LaunchKeyPath is the machine's launch key, beside the socket: `personad launch` reads it and sends it with
// launcher.env, so the node gives a managed launcher's settings only to a caller that can read the data directory.
func LaunchKeyPath() string {
	return filepath.Join(filepath.Dir(SocketPath()), "launch.key")
}

// HookTimeout is how long a hook waits for the phone before handing the dialog back to the terminal.
var HookTimeout = 30 * time.Minute

// ResumeWait is how long a hook whose node went away mid-wait (a hub or node restart) keeps reconnecting to the next
// node; the hub holds its interaction at least that long (hub.resumeGrace).
var ResumeWait = 2 * time.Minute

// ResumeRetry is how often a resuming hook tries the node socket.
var ResumeRetry = time.Second

// ErrNoNode: nothing listens on the node socket (no hub connected to this machine).
var ErrNoNode = errors.New("no persona node")

// Request is one line a client sends on the node socket; the node answers with one line.
type Request struct {
	Kind      string          `json:"kind"`
	Input     json.RawMessage `json:"input,omitempty"`
	Pane      string          `json:"pane,omitempty"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
	// CodexThreadID is the tool process's CODEX_THREAD_ID; the node resolves it in its own Codex registry.
	CodexThreadID string `json:"codex_thread_id,omitempty"`
	Assistant     bool   `json:"assistant,omitempty"` // the caller is the assistant's MCP server
	Guest         bool   `json:"guest,omitempty"`     // a guest conversation's MCP server (assistant_guest.go)
	// WaitUpdates explicitly opts this MCP call into interim {"wait_kind":...} lines before its final response.
	// Older clients omit it and keep the single-response protocol.
	WaitUpdates bool `json:"wait_updates,omitempty"`
	// Attempt is the hook process's own id, the same on each reconnect; Resume marks those reconnects.
	Attempt string `json:"attempt,omitempty"`
	Resume  bool   `json:"resume,omitempty"`
	// AgentPID / AgentStart: the claude the hook runs under and when it started (AgentOf); 0 / "" when not found.
	AgentPID   int    `json:"agent_pid,omitempty"`
	AgentStart string `json:"agent_start,omitempty"`
	// ReportOnly: the hook is one of the global report-only entries (node/globalhooks.go), which never ask the phone.
	ReportOnly bool `json:"report_only,omitempty"`
	// AwayOK: the client takes {"away":true} (a resident node in standby) for no node. An older `personad mcp` does
	// not, so standby gives one without it the answer of a node that cannot reach its manager.
	AwayOK bool `json:"away_ok,omitempty"`
	// LaunchKey: launcher.env's and secret.env's proof that the caller can read LaunchKeyPath.
	LaunchKey string `json:"launch_key,omitempty"`
	// Ticket: secret.env's one-time ticket (a hand-off's values for one Agent start).
	Ticket string `json:"ticket,omitempty"`
}

// Dialer sends one request line to the node's socket and returns its answer line (Dial).
type Dialer func(ctx context.Context, req any) ([]byte, error)

type mcpWaitContextKey struct{}

// WithMCPWait installs the receiver for an explicitly opted-in call's interim wait_kind lines. A nil receiver simply
// consumes those lines; an older client never opts in and keeps reading exactly one final line.
func WithMCPWait(ctx context.Context, receive func(proto.MCPWaitKind)) context.Context {
	return context.WithValue(ctx, mcpWaitContextKey{}, receive)
}

func mcpWaitReceiver(ctx context.Context) (func(proto.MCPWaitKind), bool) {
	receive, ok := ctx.Value(mcpWaitContextKey{}).(func(proto.MCPWaitKind))
	return receive, ok
}

const (
	mcpWaitInterimPrefix  = `{"wait_kind":`
	mcpWaitInterimMaxSize = 256
)

// mcpWaitInterim recognizes only the node's exact one-field interim envelope. The size and prefix checks keep a large
// final response from a second JSON decode; additional top-level fields make it a final response, not an interim line.
func mcpWaitInterim(line []byte) (proto.MCPWaitKind, bool) {
	if len(line) > mcpWaitInterimMaxSize || !bytes.HasPrefix(line, []byte(mcpWaitInterimPrefix)) {
		return "", false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(line, &envelope) != nil || len(envelope) != 1 {
		return "", false
	}
	var kind proto.MCPWaitKind
	if json.Unmarshal(envelope["wait_kind"], &kind) != nil {
		kind = proto.MCPWaitUnknown
	}
	return proto.NormalizeMCPWaitKind(kind), true
}

// Dial sends one request to the local node and returns the raw response line. It fails with ErrNoNode when no node
// listens.
func Dial(ctx context.Context, req any) ([]byte, error) {
	return DialAt(ctx, SocketPath(), req)
}

// DialInterruptible is Dial that also hangs up when ctx is cancelled (Ctrl-C in personad ask): the wait ends at once
// and, with the connection, the node withdraws the card.
func DialInterruptible(ctx context.Context, req any) ([]byte, error) {
	return dial(ctx, SocketPath(), req, true)
}

// DialAt is Dial for the node socket at path.
func DialAt(ctx context.Context, path string, req any) ([]byte, error) {
	return dial(ctx, path, req, false)
}

func dial(ctx context.Context, path string, req any, hangUpOnCancel bool) ([]byte, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoNode, err)
	}
	defer c.Close()
	if hangUpOnCancel {
		defer context.AfterFunc(ctx, func() { c.Close() })()
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(c)
	readLine := func() ([]byte, error) {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, fmt.Errorf("node closed connection: %w", err)
		}
		return line, nil
	}
	receive, optedIn := mcpWaitReceiver(ctx)
	var line []byte
	if !optedIn {
		// The legacy protocol is exactly one response line, even if a final response has a wait_kind field of its own.
		line, err = readLine()
	} else {
		for {
			line, err = readLine()
			if err != nil {
				break
			}
			kind, interim := mcpWaitInterim(line)
			if !interim {
				break
			}
			if receive != nil {
				receive(kind)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	var away struct {
		Away bool `json:"away"`
	}
	if json.Unmarshal(line, &away) == nil && away.Away {
		// A resident node in standby (its manager is away) that does not serve this request: as if no node listened,
		// so the caller does what it always did then (nothing was run).
		return nil, fmt.Errorf("%w: the node's manager is away", ErrNoNode)
	}
	return line, nil
}

// DialResumable sends req to the node and returns its answer line. When the node goes away while the phone is deciding
// (it answers {"retry":true}, or the connection breaks) it reconnects to the next one with the same request, marked
// Resume, for up to ResumeWait. The error is ErrNoNode when nothing listens at the first try.
func DialResumable(ctx context.Context, dial Dialer, req Request) ([]byte, error) {
	line, err := dial(ctx, req)
	if errors.Is(err, ErrNoNode) {
		return nil, err
	}
	var until time.Time
	for {
		var rep struct {
			Retry bool `json:"retry"`
		}
		if err == nil && json.Unmarshal(line, &rep) == nil && !rep.Retry {
			return line, nil
		}
		if until.IsZero() {
			until = time.Now().Add(ResumeWait)
		}
		if time.Now().After(until) {
			if err == nil {
				err = errors.New("the node went away")
			}
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(ResumeRetry):
		}
		req.Resume = true
		line, err = dial(ctx, req)
	}
}

// NewAttempt is a fresh attempt id (a UUIDv4) for Request.Attempt.
func NewAttempt() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
