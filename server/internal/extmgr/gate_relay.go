package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A relayed command gate (#12904): an extension whose assistant command talks to its unix socket on this machine
// (gate file socket_env) is reached from an assistant on any computer. The node's gate proxy holds the call to the
// gate's rules, as for a gate with an HTTP base, and sends it here (proto.MethodGateRelay); this asks the extension for
// its credentials now (assistant.credentials), dials the socket they name, puts the token on the request as the gate
// file says (GateFile.RelayAuth) and answers what the extension said. The node never gets the token for this.

const (
	// gateRelayFor bounds one relayed call, the extension's answer read to its end.
	gateRelayFor = 60 * time.Second
)

var (
	gateRelayMethod = lazyre.New(`^[A-Z]{1,16}$`)
	gateRelayRef    = lazyre.New(`\$\{([A-Z_][A-Z0-9_]*)\}`)
)

// relayGate is the enabled extension's gate that has command and a socket_env, with its extension and contribution.
func (ps *extensions) relayGate(command string) (id, contribution string, gf *extension.GateFile) {
	if ps == nil {
		return "", "", nil
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" || p.Dir == "" {
			continue
		}
		for _, a := range p.Manifest.Assistants() {
			if a.Gate == "" {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(p.Dir, filepath.FromSlash(a.Gate)))
			if err != nil {
				continue
			}
			g, err := extension.ParseGate(raw)
			if err != nil || g.Command != command || g.SocketEnv == "" {
				continue
			}
			return p.Record.ID, a.ID, g
		}
	}
	return "", "", nil
}

// RelayGate answers a node's gate.relay: machineID is the connection the request came in on, a computer of this
// manager's (permission_denied for any other).
func (h *Manager) RelayGate(ctx context.Context, machineID string, raw json.RawMessage) (any, error) {
	if h.machineByID(machineID) == nil {
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: "gate.relay: not from a computer of this manager"}
	}
	var p proto.GateRelayParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &rpc.Error{Code: "invalid_params", Message: "gate.relay: " + err.Error()}
	}
	if !gateRelayMethod.MatchString(p.Method) || !validGateRelayPath(p.Path) ||
		len(p.Body) > proto.GateRelayMaxBody {
		return nil, &rpc.Error{Code: "invalid_params", Message: "gate.relay: a method, a path from / and a body within bounds"}
	}
	id, contribution, gf := h.extensions.relayGate(p.Command)
	if gf == nil {
		return nil, &rpc.Error{Code: "not_found", Message: fmt.Sprintf("gate.relay: no relayed gate %q (the extension is switched off or gone)", p.Command)}
	}
	auth := gf.RelayAuth()
	env := h.extensions.assistantCredentials(ctx, id, contribution, gf.Env)
	socket := env[gf.SocketEnv]
	if auth == nil || socket == "" || !filepath.IsAbs(socket) {
		return nil, &rpc.Error{Code: "unavailable", Message: "gate.relay: " + p.Command + ": the extension gave no socket or credentials"}
	}
	return relayToSocket(ctx, socket, gf.Command, auth, env, p)
}

// validGateRelayPath checks that a relayed command-gate path is an unambiguous
// absolute path with no duplicate/empty interior segments, dot segments, URL
// delimiters ("?", "#"), backslashes, encoded separators/dots, or control
// characters.
func validGateRelayPath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	if p == "/" {
		return true
	}
	segs := strings.Split(p[1:], "/")
	for i, seg := range segs {
		if seg == "" {
			if i == len(segs)-1 && len(segs) > 1 {
				continue
			}
			return false
		}
		if seg == "." || seg == ".." {
			return false
		}
		for j := 0; j < len(seg); j++ {
			ch := seg[j]
			if ch < 0x20 || ch == 0x7f || ch == '\\' || ch == '?' || ch == '#' {
				return false
			}
		}
		if strings.ContainsRune(seg, '%') {
			dec, err := url.PathUnescape(seg)
			if err != nil || dec == "." || dec == ".." {
				return false
			}
			for j := 0; j < len(dec); j++ {
				ch := dec[j]
				if ch < 0x20 || ch == 0x7f || ch == '/' || ch == '\\' || ch == '?' || ch == '#' {
					return false
				}
			}
		}
	}
	return true
}

// relayToSocket sends p to the HTTP server on socket, auth's header set (each ${NAME} from env), and reads its answer.
func relayToSocket(ctx context.Context, socket, host string, auth *proto.GateAuth, env map[string]string, p proto.GateRelayParams) (proto.GateRelayAnswer, error) {
	if !validGateRelayPath(p.Path) {
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "invalid_params", Message: "gate.relay: a method, a path from / and a body within bounds"}
	}
	ctx, cancel := context.WithTimeout(ctx, gateRelayFor)
	defer cancel()
	decodedPath, err := url.PathUnescape(p.Path)
	if err != nil {
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "invalid_params", Message: "gate.relay: " + err.Error()}
	}
	u := &url.URL{
		Scheme:   "http",
		Host:     host,
		Path:     decodedPath,
		RawQuery: p.Query,
	}
	if strings.ContainsRune(p.Path, '%') {
		u.RawPath = p.Path
	}
	req, err := http.NewRequestWithContext(ctx, p.Method, u.String(), bytes.NewReader(p.Body))
	if err != nil {
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "invalid_params", Message: "gate.relay: " + err.Error()}
	}
	for k, vs := range p.Header {
		switch strings.ToLower(k) {
		case "authorization", "host", "cookie", "proxy-authorization", "connection", "transfer-encoding", "content-length":
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set(auth.Header, gateRelayRef.ReplaceAllStringFunc(auth.Value, func(m string) string {
		return env[gateRelayRef.FindStringSubmatch(m)[1]]
	}))
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}).Do(req)
	if err != nil {
		// Never the request's headers: the token is among them.
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "unavailable", Message: "gate.relay: " + host + ": the extension is not answering on its socket"}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, proto.GateRelayMaxBody+1))
	if err != nil {
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "unavailable", Message: "gate.relay: " + host + ": reading its answer: " + err.Error()}
	}
	if len(body) > proto.GateRelayMaxBody {
		return proto.GateRelayAnswer{}, &rpc.Error{Code: "unavailable", Message: "gate.relay: " + host + ": its answer is too long"}
	}
	out := proto.GateRelayAnswer{Status: res.StatusCode, Body: body, Header: map[string][]string{}}
	for _, k := range []string{"Content-Type", "Content-Encoding", "Cache-Control", "Etag", "Last-Modified"} {
		if v := res.Header.Values(k); len(v) > 0 {
			out.Header[k] = v
		}
	}
	return out, nil
}
