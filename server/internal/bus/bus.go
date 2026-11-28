// Package bus is how a service reaches the hub (docs/protocol/protocol.md "Bus"): it dials GET /api/v1/bus with a token of
// its own and keeps the connection up. Over it runs the internal/rpc protocol unchanged, one
// JSON line per WebSocket text message, calls going both ways. Client is the service's side; the hub's is
// hub/bus.go.
package bus

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	// Path is where the hub accepts the services' connections.
	Path = "/api/v1/bus"
	// JoinPath is where a new manager trades its one-time install code for its token (POST {"code"} → {id, name,
	// token, hub_id}; hub/managers.go). It needs no credential.
	JoinPath = "/api/v1/bus/join"

	// RoleManager is the role of a token that speaks for a manager: a process that owns business state, such as the
	// assistant. It is the only role so far.
	RoleManager = "manager"

	// MaxMessage is the largest RPC line either side takes.
	MaxMessage = 64 << 20
)

// Both sides ping every PingEvery and drop the connection after PingTimeout without a pong: a laptop that went to
// sleep leaves a TCP connection that looks open for a long time. The hub drops a connection that has not completed
// its handshake within HelloTimeout. These are settings, not measurements.
const (
	PingEvery    = 30 * time.Second
	PingTimeout  = 20 * time.Second
	HelloTimeout = 40 * time.Second
)

// Error codes of the bus's own refusals (rpc.Error.Code).
const (
	CodeWrongIdentity   = "wrong_identity"   // the handshake names another identity than the token's: the hub hangs up
	CodeStaleConnection = "stale_connection" // no handshake on this connection yet, or a newer one replaced it
	// CodeTokenRevoked is the code of the hub's 401 to a bus token it has no identity for (revoked or replaced): the only
	// 401 a service takes for ErrRevoked.
	CodeTokenRevoked = "token_revoked"
	// CodeStoreUnavailable is the code of the hub's 503 when it cannot look tokens up (its database): try again.
	CodeStoreUnavailable = "store_unavailable"
	CodeUnknownMethod    = "unknown_method"
	CodeInvalidParams    = "invalid_params"
)

// Close codes besides the standard ones (1001: the hub is stopping), as on the computer link (internal/link).
const (
	CloseReplaced websocket.StatusCode = 4001 // a newer connection of the same identity took over
	CloseRevoked  websocket.StatusCode = 4002 // the token was revoked or replaced
	CloseRefused  websocket.StatusCode = 4003 // the handshake was wrong or did not come in time
)

// ErrRejected: the hub refused the token (401), or closed the connection because the token is no longer good or the
// handshake does not match it. Retrying with the same token only helps once someone fixes it on the hub, so Client
// waits its longest backoff between tries.
var ErrRejected = errors.New("the hub rejected the token")

// ErrRevoked is ErrRejected for the token itself, on the hub's explicit word only: it hung up because the token was
// revoked or replaced (CloseRevoked), or refused the dial with 401 code CodeTokenRevoked (a bus token it has no
// identity for). A plain 401, or a hub that cannot check tokens right now (503 CodeStoreUnavailable), is not it. Only a
// new token helps (a manager made in the app: a new install code, `personad manager join`). A Client with
// StopWhenRevoked returns it from Run instead of trying again.
var ErrRevoked = fmt.Errorf("%w: it was revoked or replaced", ErrRejected)

// Conn adapts a WebSocket to the io.Reader and io.Writer an rpc.Peer serves on: each line written goes out as one
// text message, each message read comes back as one line. Neither side copies a whole message: a read hands the
// message on from the WebSocket's reader as it comes, a write sends complete lines straight from the caller's bytes,
// so a large message leaves no buffer of its size behind (one 21 MB request body kept 28 MB on each side).
type Conn struct {
	ws  *websocket.Conn
	ctx context.Context

	rmu   sync.Mutex
	limit int64     // the largest message read: MaxMessage
	msg   io.Reader // the text message being read; nil between messages
	got   int64     // its bytes handed on so far
	tail  []byte    // owed before anything else is read: a message's newline, and a byte read past limit (atLimit)
	one   [2]byte   // tail's storage
	rerr  error     // owed next: the error of a message that failed part-way

	wmu  sync.Mutex
	wbuf []byte // a line written in pieces, until its newline
}

// keptLine is the largest partial-line buffer Write keeps once its line went out; a larger one is let go.
const keptLine = 64 << 10

// NewConn wraps ws; ctx bounds every read and write (canceling it closes the connection without a close frame).
func NewConn(ctx context.Context, ws *websocket.Conn) *Conn { return newConn(ctx, ws, MaxMessage) }

func newConn(ctx context.Context, ws *websocket.Conn, limit int64) *Conn {
	ws.SetReadLimit(limit)
	return &Conn{ws: ws, ctx: ctx, limit: limit}
}

// Read hands on the current text message, then a newline; binary messages are skipped. A message that fails part-way
// (over MaxMessage, the connection lost, ctx canceled) may already have handed on its start: it then hands on a NUL,
// which no JSON has there, and the error. The rpc reader takes that start as its last line and json rejects it, so
// no handler sees any of it, as when the message was read whole.
func (c *Conn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	for {
		if len(c.tail) > 0 {
			n := copy(p, c.tail)
			c.tail = c.tail[n:]
			return n, nil
		}
		if err := c.rerr; err != nil {
			c.rerr = nil
			return 0, readError(err)
		}
		if c.msg == nil {
			typ, r, err := c.ws.Reader(c.ctx)
			if err != nil {
				return 0, readError(err)
			}
			if typ != websocket.MessageText {
				if _, err := io.Copy(io.Discard, r); err != nil {
					return 0, readError(err)
				}
				continue
			}
			c.msg, c.got = r, 0
		}
		// Never past the limit before atLimit has seen what follows it.
		q := p
		if rest := c.limit - c.got; int64(len(q)) > rest {
			q = q[:rest]
		}
		n, err := c.msg.Read(q)
		c.got += int64(n)
		if err == nil && c.got == c.limit {
			err = c.atLimit()
		}
		if err == io.EOF { // the message is complete: its newline
			c.msg = nil
			if k := len(c.tail); k > 0 || n == len(p) {
				c.one[k] = '\n'
				c.tail = c.one[:k+1]
			} else {
				p[n] = '\n'
				n++
			}
		} else if err != nil {
			c.msg, c.tail = nil, nil
			if c.got > int64(n) { // its start was handed on
				p[0], c.rerr = 0, err
				return 1, nil
			}
			return 0, readError(err)
		}
		if n > 0 {
			return n, nil
		}
	}
}

// atLimit runs once the limit's bytes of the message are out. A whole read (websocket.Conn.Read) of a longer one fails
// on the library's limit, with its error and its 1009 close, and hands on nothing; so find out before the rpc reader
// takes those bytes for a line too long of its own. io.EOF: the message ends here, or one byte later (in tail) where
// the library's limit lets that byte through.
func (c *Conn) atLimit() error {
	for {
		n, err := c.msg.Read(c.one[:1])
		if n == 0 && err == nil {
			continue
		}
		if n == 1 {
			c.tail = c.one[:1]
			if err == nil { // past the limit: the library fails the next read
				if _, err = c.msg.Read(c.one[1:]); err == nil || err == io.EOF {
					err = fmt.Errorf("bus: a message over %d bytes", c.limit)
				}
			}
		}
		return err
	}
}

// readError is a read's error as the reader takes it: a normal closure is the end of the input.
func readError(err error) error {
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return io.EOF
	}
	return err
}

// Write sends each complete line as one text message, straight from p; the start of a line without its newline waits
// in wbuf for the rest. Blank lines are not sent.
func (c *Conn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	rest := p
	if len(c.wbuf) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			c.wbuf = append(c.wbuf, rest...)
			return len(p), nil
		}
		c.wbuf = append(c.wbuf, rest[:i]...)
		err := c.send(c.wbuf)
		c.dropLine()
		if err != nil {
			return 0, err
		}
		rest = rest[i+1:]
	}
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			break
		}
		if err := c.send(rest[:i]); err != nil {
			return 0, err
		}
		rest = rest[i+1:]
	}
	c.wbuf = append(c.wbuf, rest...)
	return len(p), nil
}

func (c *Conn) send(line []byte) error {
	if len(bytes.TrimSpace(line)) == 0 {
		return nil
	}
	if c.limit > 0 && int64(len(line)) > c.limit {
		return fmt.Errorf("bus: outgoing message is %d bytes (limit %d): %w", len(line), c.limit, rpc.ErrMessageTooLarge)
	}
	return c.ws.Write(c.ctx, websocket.MessageText, line)
}

// dropLine empties wbuf, and lets a large one go.
func (c *Conn) dropLine() {
	if cap(c.wbuf) > keptLine {
		c.wbuf = nil
		return
	}
	c.wbuf = c.wbuf[:0]
}

// Close ends the connection with a normal closure (the other side reads io.EOF).
func (c *Conn) Close() error { return c.ws.Close(websocket.StatusNormalClosure, "") }

// Keepalive pings every interval and returns when a pong does not come back within timeout, or when ctx ends. The
// connection must be being read for pongs to arrive (an rpc peer serving on it always is).
func Keepalive(ctx context.Context, ws *websocket.Conn, every, timeout time.Duration) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		err := ws.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("no pong within %s: %w", timeout, err)
		}
	}
}

// Dial connects to the hub's bus with a service's token. hub is the hub's base URL (http(s)://host:port, the same
// one the apps use); client may be nil (a pinned TLS client for the hub's public port otherwise).
func Dial(ctx context.Context, hub, token string, client *http.Client) (*websocket.Conn, error) {
	return dial(ctx, hub, token, client, websocket.CompressionContextTakeover)
}

// dial is Dial offering compression as mode says (CompressionDisabled offers none: the hub compresses only when
// offered).
func dial(ctx context.Context, hub, token string, client *http.Client, mode websocket.CompressionMode) (*websocket.Conn, error) {
	u, err := Endpoint(hub)
	if err != nil {
		return nil, err
	}
	ws, resp, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient:      client,
		HTTPHeader:      http.Header{"Authorization": {"Bearer " + token}},
		CompressionMode: mode,
	})
	if err != nil {
		if resp != nil {
			if resp.Body != nil {
				defer resp.Body.Close()
			}
			if resp.StatusCode == http.StatusUnauthorized {
				if errorCode(resp) == CodeTokenRevoked {
					return nil, ErrRevoked
				}
				return nil, ErrRejected
			}
		}
		return nil, err
	}
	return ws, nil
}

// errorCode is the "code" of a refused dial's JSON error body ("" when there is none).
func errorCode(resp *http.Response) string {
	if resp.Body == nil {
		return ""
	}
	var e struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1024)).Decode(&e)
	return e.Code
}

// Endpoint is the bus's URL on the hub at base URL hub.
func Endpoint(hub string) (string, error) {
	base, err := url.Parse(strings.TrimRight(hub, "/") + "/")
	if err != nil || base.Host == "" {
		return "", fmt.Errorf("bad hub url %q", hub)
	}
	switch base.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return "", fmt.Errorf("bad hub url %q: want http(s)://", hub)
	}
	return base.JoinPath(strings.TrimPrefix(Path, "/")).String(), nil
}
