package node

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// The command gates' credentials stay in the node (#1317). An assistant process gets, for a gate with Auth, its
// BaseEnv pointing at this proxy (http://127.0.0.1:<port>/<key>, a key of its own that goes when the process ends)
// and none of the credentials. The proxy adds them and holds every request to the gate's rules, however it was sent
// (the gated command, curl, a script): allow goes, deny is refused, and a call to confirm goes only once, after
// larkCheck let that very call through (the phone confirmed it, or "Always allow in this session" covers it). Printing the
// environment so yields an address that works on this machine only and skips nothing.
//
// In a conversation in a space (docs/assistant/health.md "Outbound") every write goes that way, whatever the rules say: the address of
// a process started in a space is marked so when it is opened, and a write there goes only with the grant larkCheck
// gave it after the phone confirmed, as larkCheck itself holds it. Reads go as everywhere.
//
// The proxy also sees what the API answered. A write it forwarded that came back 2xx is told to the manager
// (proto.MethodLarkSent), which keeps it in the sent-as-user ledger with how the gate let it through (docs/extensions/connectors.md
// "Sent as the user", #5278): the gate decides before the command runs and cannot know whether it sent. What does not
// pass here is not in the ledger: lark-cli carries its own credentials, and a gate without Auth puts them in the
// command's environment.
//
// The report also carries the id the API's answer gave for the message the write made (LarkSent.MessageRef), so that a
// row can name it (docs/extensions/connectors.md "Sent as the user"). The proxy only watches the answer on its way to the
// command: it keeps a copy of its first gateReplyMax bytes and reads the id from the copy once the answer has been
// passed on. What the command gets is what the API sent, as it came and when it came.

const (
	// gateGrantFor: how long a call larkCheck let through may take to be sent.
	gateGrantFor = 10 * time.Minute
	// gateBodyMax: the most of a JSON body read to match it to the rules and to what was confirmed.
	gateBodyMax = 4 << 20
	// gateGrantsMax: grants kept at most (the oldest go first).
	gateGrantsMax = 256
	// gateTextMost: the most of a write's words (characters) told to the manager for the ledger.
	gateTextMost = 4000
	// gateReplyMax: the most of an answer's body copied to read the message id from; an answer longer than that (or,
	// compressed, longer once read) is not read and the write is told with no id.
	gateReplyMax = 256 << 10
	// gateReportsMax: reports to the manager in flight at once. A flood of allowed writes does not pile up goroutines
	// behind a slow manager: one over the limit waits up to gateReportWait for a place, then is logged and not told.
	gateReportsMax = 32
	gateReportWait = time.Second
	// gateReportFor: how long a report may take.
	gateReportFor = 15 * time.Second
)

type gateProxy struct {
	// cur is the Node whose config (credentials, rules) requests go by: the last to use the proxy, the one serving
	// the hub now.
	cur atomic.Pointer[Node]

	mu            sync.Mutex
	ln            net.Listener
	keys          map[string]gateKey        // key → the gate's command and the conversation its process was started for
	sessionLeases map[gateSessionKey]string // (session, command) → active key in keys for runtime assistant sessions
	grants        []gateGrant
	// spaces: the conversations known to be in a space (markSpace). A conversation does not change space, so one
	// stays in it, and only the ones in a space are kept.
	spaces map[string]struct{}
	// remotes latches conversations known to carry remote-runtime confirm-writes posture (markRemote).
	remotes map[string]struct{}

	// reporting holds a slot for each report to the manager in flight (gateReportsMax).
	reporting chan struct{}
}

// gateSessionKey identifies one idempotent session-scoped proxy lease.
type gateSessionKey struct {
	session string
	command string
}

// gateKey is what an address at the proxy stands for.
type gateKey struct {
	command string
	// session is the conversation the process was started for ("" when none is known). The assistant's process serves
	// one, so a write a rule allowed belongs to it.
	session string
	// space: the process was started in a space. Decided once, from the directory the node started it in,
	// when its address is opened: the address stays with that one process, so it never serves another space.
	space bool
	// remote: the conversation carries the remote-runtime confirm-writes posture.
	remote bool
}

// gateGrant is a call larkCheck let through, to go once.
type gateGrant struct {
	command, method, path, query string
	account                      string
	body                         any    // the JSON body confirmed, when bodyBound (nil there is JSON null)
	bodyBound                    bool   // false: any body (not JSON, cut short, an upload, $… in it)
	bodyUseNumber                bool   // an edited grant compares JSON numbers without float64 rounding
	rewriteBody                  []byte // when set, proxy forwards these JSON bytes instead of the matched original body
	until                        time.Time
	// session is the conversation larkCheck decided for, and through how it let the call through: what the ledger says
	// of the write once it has gone out.
	session string
	through proto.LarkLetThrough
}

func newGateProxy() *gateProxy {
	return &gateProxy{
		keys:          map[string]gateKey{},
		sessionLeases: map[gateSessionKey]string{},
		reporting:     make(chan struct{}, gateReportsMax),
	}
}

// existingGates returns the node's gate proxy if one has already been created, without creating one or changing cur.
func (n *Node) existingGates() *gateProxy {
	if n == nil {
		return nil
	}
	if k := n.keeper; k != nil {
		k.mu.Lock()
		p := k.gp
		k.mu.Unlock()
		return p
	}
	n.mu.Lock()
	p := n.gp
	n.mu.Unlock()
	return p
}

// gates is the gate proxy, made at its first use (it listens once a process needs it, open). A resident node has one
// for the process (assistantKeeper): the conversations it keeps from one connection to the next keep their
// addresses, and a call the next connection's Node lets through is the one they send. Otherwise it is the Node's.
func (n *Node) gates() *gateProxy {
	var p *gateProxy
	if k := n.keeper; k != nil {
		k.mu.Lock()
		if k.gp == nil {
			k.gp = newGateProxy()
		}
		p = k.gp
		k.mu.Unlock()
	} else {
		n.mu.Lock()
		if n.gp == nil {
			n.gp = newGateProxy()
		}
		p = n.gp
		n.mu.Unlock()
	}
	p.cur.Store(n)
	return p
}

// gateProxyIfInit returns the active gateProxy without lazily initializing one.
func (n *Node) gateProxyIfInit() *gateProxy {
	if n == nil {
		return nil
	}
	if k := n.keeper; k != nil {
		k.mu.Lock()
		gp := k.gp
		k.mu.Unlock()
		return gp
	}
	n.mu.Lock()
	gp := n.gp
	n.mu.Unlock()
	return gp
}

// isSpaceSession reports whether the gate proxy already latched session as belonging to a space,
// without lazily initializing a gate proxy listener when none exists yet.
func (n *Node) isSpaceSession(session string) bool {
	if session == "" {
		return false
	}
	gp := n.gateProxyIfInit()
	return gp != nil && gp.spaceSession(session)
}

// isRemoteSession reports whether session carries the remote-assistant confirm-writes posture,
// mirroring isRemoteRuntimeSession without lazily initializing a gate proxy listener.
func (n *Node) isRemoteSession(session string) bool {
	if n == nil || session == "" {
		return false
	}
	id := session
	if parent, _, ok := splitRuntimeChild(session); ok {
		id = parent
	}
	if gp := n.gateProxyIfInit(); gp != nil && (gp.remoteSession(id) || (id != session && gp.remoteSession(session))) {
		return true
	}
	if n.state != nil {
		for _, candidate := range []string{id, session} {
			if st, ok := n.state.get(candidate); ok {
				if st.RemoteAgent {
					return true
				}
				if st.Launcher != "" {
					if h := n.assistantRuntime(st.Launcher); h != nil && h.remoteAgent() {
						return true
					}
				}
			}
			if candidate == session {
				break
			}
		}
	}
	if n.runtimes != nil && n.runtimes.isRemoteSession(session) {
		return true
	}
	if n.state != nil && n.state.isLoading() && runtimeSessionID(id) {
		return true
	}
	return false
}

func (p *gateProxy) ensureListenerLocked() error {
	if p.ln != nil {
		return nil
	}
	ln, err := osproc.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if ln, err = osproc.Listen("tcp", "[::1]:0"); err != nil {
			return err
		}
	}
	p.ln = ln
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 30 * time.Second, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = srv.Serve(ln) }()
	return nil
}

func (p *gateProxy) allocKeyLocked(gk gateKey) (key, base string, err error) {
	if err := p.ensureListenerLocked(); err != nil {
		return "", "", err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	key = hex.EncodeToString(b)
	p.keys[key] = gk
	return key, "http://" + p.ln.Addr().String() + "/" + key, nil
}

// openIn gives one process of the assistant, the one started for session ("" when that is not known yet: nameAddr gives
// it later), its own address for command's gate: the base to put in its BaseEnv, and release, which takes the address
// back when the process ends. space says whether the process is in a space; remote says whether the session has
// remote-runtime confirm-writes posture.
func (p *gateProxy) openIn(command, session string, space, remote bool) (base string, release func(), err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, base, err := p.allocKeyLocked(gateKey{command: command, session: session, space: space, remote: remote})
	if err != nil {
		return "", nil, err
	}
	return base, func() {
		p.mu.Lock()
		if gk, ok := p.keys[key]; ok {
			delete(p.keys, key)
			if gk.session != "" {
				lk := gateSessionKey{session: gk.session, command: gk.command}
				if p.sessionLeases[lk] == key {
					delete(p.sessionLeases, lk)
				}
			}
		}
		p.mu.Unlock()
	}, nil
}

// openSession returns an idempotent session-scoped proxy address for (session, command), creating one on first use
// and reusing it across subsequent runtime RPCs until releaseSession/releaseSessions revokes it. space and remote
// latch monotonically (never downgraded).
func (p *gateProxy) openSession(command, session string, space, remote bool) (base string, err error) {
	if session == "" {
		return "", errors.New("gate proxy session lease requires a session id")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.ensureListenerLocked(); err != nil {
		return "", err
	}
	if _, ok := p.spaces[session]; ok {
		space = true
	}
	if _, ok := p.remotes[session]; ok {
		remote = true
	}
	lk := gateSessionKey{session: session, command: command}
	if key, ok := p.sessionLeases[lk]; ok {
		if gk, live := p.keys[key]; live {
			if space {
				gk.space = true
			}
			if remote {
				gk.remote = true
			}
			p.keys[key] = gk
			return "http://" + p.ln.Addr().String() + "/" + key, nil
		}
		delete(p.sessionLeases, lk)
	}
	key, base, err := p.allocKeyLocked(gateKey{command: command, session: session, space: space, remote: remote})
	if err != nil {
		return "", err
	}
	if p.sessionLeases == nil {
		p.sessionLeases = map[gateSessionKey]string{}
	}
	p.sessionLeases[lk] = key
	return base, nil
}

// bindSessionAddr promotes an unnamed address opened before session.start returned its session id into the
// session-scoped lease for (session, command). space and remote latch monotonically.
func (p *gateProxy) bindSessionAddr(base, session string, space, remote bool) {
	if base == "" || session == "" {
		return
	}
	key := base[strings.LastIndex(base, "/")+1:]
	p.mu.Lock()
	defer p.mu.Unlock()
	gk, ok := p.keys[key]
	if !ok {
		return
	}
	if gk.session == "" {
		gk.session = session
	}
	if _, ok := p.spaces[gk.session]; ok {
		space = true
	}
	if _, ok := p.remotes[gk.session]; ok {
		remote = true
	}
	if space {
		gk.space = true
	}
	if remote {
		gk.remote = true
	}
	p.keys[key] = gk
	if p.sessionLeases == nil {
		p.sessionLeases = map[gateSessionKey]string{}
	}
	lk := gateSessionKey{session: gk.session, command: gk.command}
	if prev, exists := p.sessionLeases[lk]; exists && prev != key {
		delete(p.keys, prev)
	}
	p.sessionLeases[lk] = key
}

// releaseSession revokes all session-scoped proxy addresses leased to session.
func (p *gateProxy) releaseSession(session string) {
	if session != "" {
		p.releaseSessions([]string{session})
	}
}

// releaseSessions revokes all session-scoped proxy addresses leased to any id in sessions.
// Space and remote latches are kept so confirm-writes boundaries cannot be downgraded.
func (p *gateProxy) releaseSessions(sessions []string) {
	if p == nil || len(sessions) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sessionLeases) == 0 {
		return
	}
	var drop map[string]struct{}
	if len(sessions) > 1 {
		drop = make(map[string]struct{}, len(sessions))
		for _, s := range sessions {
			if s != "" {
				drop[s] = struct{}{}
			}
		}
	}
	for lk, key := range p.sessionLeases {
		match := false
		if drop != nil {
			_, match = drop[lk.session]
		} else {
			match = sessions[0] != "" && lk.session == sessions[0]
		}
		if match {
			delete(p.keys, key)
			delete(p.sessionLeases, lk)
		}
	}
}

// nameAddr says which conversation the address base stands for, when it was opened for a process that did not know its
// conversation yet (a new Codex conversation has an id only once its thread starts). An address that has one keeps it;
// remote only latches true.
func (p *gateProxy) nameAddr(base, session string, remote bool) {
	if session == "" && !remote {
		return
	}
	key := base[strings.LastIndex(base, "/")+1:]
	p.mu.Lock()
	defer p.mu.Unlock()
	if gk, ok := p.keys[key]; ok {
		if gk.session == "" && session != "" {
			gk.session = session
		}
		if remote {
			gk.remote = true
		}
		p.keys[key] = gk
	}
}

// markSpace notes that session is a conversation in a space (larkCheck asks spaceSession).
func (p *gateProxy) markSpace(session string) {
	if session == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.spaces == nil {
		p.spaces = map[string]struct{}{}
	}
	p.spaces[session] = struct{}{}
}

// spaceSession says whether session was noted as a conversation in a space: the space its process was started in.
func (p *gateProxy) spaceSession(session string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.spaces[session]
	return ok && session != ""
}

// markRemote latches that session carries remote-runtime confirm-writes posture.
func (p *gateProxy) markRemote(session string) {
	if session == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.remotes == nil {
		p.remotes = map[string]struct{}{}
	}
	p.remotes[session] = struct{}{}
}

// remoteSession reports whether session was latched with remote-runtime confirm-writes posture.
func (p *gateProxy) remoteSession(session string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.remotes[session]
	return ok && session != ""
}

// close stops listening (tests; the node's proxy lives as long as the node).
func (p *gateProxy) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		_ = p.ln.Close()
		p.ln = nil
	}
	clear(p.keys)
	clear(p.sessionLeases)
}

func gateAgentToken(g proto.CommandGate) (string, bool) {
	if g.PathPrefix != "/agent/v1" || g.Auth == nil || !strings.EqualFold(g.Auth.Header, "Authorization") {
		return "", false
	}
	token, ok := strings.CutPrefix(gateAuthValue(g.Auth.Value, g.Env), "Bearer ")
	token = strings.TrimSpace(token)
	return token, ok && token != ""
}

func normalizeAgentAccount(account string) string {
	if account == "1" {
		return ""
	}
	return account
}

func gateAgentQueryAccount(raw string) string {
	query, _ := url.ParseQuery(raw)
	return query.Get("ol_account")
}

func gateAgentAccountHeader(g proto.CommandGate) string {
	for i := len(g.Headers) - 1; i >= 0; i-- {
		if strings.HasSuffix(g.Headers[i], "-account") {
			return g.Headers[i]
		}
	}
	return ""
}

func gateAgentAccountEnv(g proto.CommandGate) string {
	for i := len(g.AllowedEnv) - 1; i >= 0; i-- {
		if strings.HasSuffix(strings.ToLower(g.AllowedEnv[i]), "_account") {
			return g.AllowedEnv[i]
		}
	}
	return ""
}

func gateAgentRequestAccount(g proto.CommandGate, r *http.Request) string {
	if _, ok := gateAgentToken(g); !ok {
		return ""
	}
	account := ""
	if header := gateAgentAccountHeader(g); header != "" {
		account = r.Header.Get(header)
	}
	if account == "" {
		account = gateAgentQueryAccount(r.URL.RawQuery)
	}
	return normalizeAgentAccount(account)
}

// grant lets c, which larkCheck let through for session and the way through says, go once through command's gate.
func (p *gateProxy) grant(command, session string, c larkCall, through proto.LarkLetThrough) {
	g := gateGrant{command: command, method: strings.ToUpper(c.Method), path: c.Path, query: gateQueryKey(c.Query), account: c.Account, until: time.Now().Add(gateGrantFor),
		session: session, through: through}
	// $ in it: the shell fills it in when the command runs, so the body sent is not the one on the card.
	if !strings.Contains(c.Body, "$") {
		var v any
		if json.Unmarshal([]byte(c.Body), &v) == nil {
			g.body, g.bodyBound = v, true
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	p.grants = append(p.grants, g)
	if len(p.grants) > gateGrantsMax {
		p.grants = p.grants[len(p.grants)-gateGrantsMax:]
	}
}

// gateQueryKey binds a grant to the confirmed query's semantics. Distinct keys may be reordered, and equivalent
// escaping may differ; values of a repeated key keep their order, which an API may treat as significant. If either
// query is malformed, only the same raw bytes match: ParseQuery can return a partial map together with an error.
func gateQueryKey(raw string) string {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return "raw\x00" + raw
	}
	return "parsed\x00" + q.Encode()
}

// take uses up a grant for a request at the address k stands for: its command's gate, the method, path and query, and
// the body confirmed if one was. A grant of one conversation is not for another's request: when both are known and
// differ, it stays for its own. A space conversation's address takes only a grant of its own conversation: not one made
// for no conversation, nor any when it has none. It returns the grant.
func (p *gateProxy) take(k gateKey, method, path, query, account string, body []byte, isJSON bool) (gateGrant, bool) {
	var sent any
	parsed := isJSON && json.Unmarshal(body, &sent) == nil
	var sentNumbers any
	numbersParsed, numbersTried := false, false
	query = gateQueryKey(query)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pruneLocked()
	for i, g := range p.grants {
		if g.command != k.command || g.method != method || g.path != path || g.query != query || g.account != account {
			continue
		}
		if (k.space || k.remote) && (k.session == "" || g.session != k.session) {
			continue
		}
		if g.session != "" && k.session != "" && g.session != k.session {
			continue
		}
		candidate, candidateParsed := sent, parsed
		if g.bodyUseNumber {
			if !numbersTried {
				numbersTried = true
				dec := json.NewDecoder(bytes.NewReader(body))
				dec.UseNumber()
				numbersParsed = isJSON && dec.Decode(&sentNumbers) == nil
				if numbersParsed {
					var extra any
					numbersParsed = dec.Decode(&extra) == io.EOF
				}
			}
			candidate, candidateParsed = sentNumbers, numbersParsed
		}
		if g.bodyBound && (!candidateParsed || !reflect.DeepEqual(g.body, candidate)) {
			continue
		}
		p.grants = slices.Delete(p.grants, i, i+1)
		return g, true
	}
	return gateGrant{}, false
}

func (p *gateProxy) pruneLocked() {
	now := time.Now()
	p.grants = slices.DeleteFunc(p.grants, func(g gateGrant) bool { return now.After(g.until) })
}

// ServeHTTP is a request of a gated command's (or anything else the assistant runs) to its API.
func (p *gateProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	p.mu.Lock()
	gk, ok := p.keys[key]
	p.mu.Unlock()
	command := gk.command
	n := p.cur.Load()
	if !ok || n == nil {
		http.NotFound(w, r)
		return
	}
	cfg := n.assistantLarkForGate(r.Context(), command)
	gi := slices.IndexFunc(cfg.Gates, func(g proto.CommandGate) bool { return g.Command == command && g.Auth != nil })
	if gi < 0 {
		gateRefuse(w, http.StatusServiceUnavailable, command+" 现在用不了：提供它的扩展停用了，或者 hub 没给它凭据。")
		return
	}
	g := cfg.Gates[gi]
	var up *url.URL
	if !g.Relay { // a relayed gate's API is the extension's socket at the manager (gate.relay, #12904)
		var err error
		up, err = url.Parse(g.Env[g.BaseEnv])
		if err != nil || up.Host == "" || up.Scheme != "http" && up.Scheme != "https" {
			gateRefuse(w, http.StatusServiceUnavailable, command+" 没有可用的接口地址（扩展没给，或者给的不对）。")
			return
		}
		// A sandboxed extension names this machine's loopback as host.persona.internal (#13540): the node dials
		// 127.0.0.1 itself.
		if strings.EqualFold(up.Hostname(), "host.persona.internal") {
			if p := up.Port(); p != "" {
				up.Host = net.JoinHostPort("127.0.0.1", p)
			} else {
				up.Host = "127.0.0.1"
			}
		}
	}
	restPath, err := validateGateProxyPath(rest)
	if err != nil {
		gateRefuse(w, http.StatusBadRequest, "路径不对："+err.Error())
		return
	}
	// The call as larkCheck reads `<command> METHOD PATH`: the path past the base, without the gate's prefix.
	c := larkCall{Gate: gi, Method: strings.ToUpper(r.Method), Path: strings.TrimPrefix(restPath, g.PathPrefix), Query: r.URL.RawQuery,
		Account: gateAgentRequestAccount(g, r)}
	if !strings.HasPrefix(c.Path, "/") {
		c.Path = "/" + c.Path
	}
	var body []byte
	isJSON := false
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt == "application/json" || strings.HasSuffix(mt, "+json") {
		if body, err = io.ReadAll(io.LimitReader(r.Body, gateBodyMax+1)); err != nil {
			gateRefuse(w, http.StatusBadRequest, "读不完请求："+err.Error())
			return
		}
		isJSON = len(body) <= gateBodyMax // longer: no rule's body matches it, nor does a body confirmed
		if isJSON {
			c.Body = string(body)
			// All of the body is in hand, so it goes on from here and not from the server's: once the proxy begins to
			// answer, net/http closes the server's body and a read of it fails, and the transport does read it, past the
			// body's length, to see that there was no more. Where that read comes after the API's answer has begun, the
			// failed read closes the connection to the API with the answer half read.
			r.Body = io.NopCloser(bytes.NewReader(body))
		} else {
			r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), gateRest(r, len(body))))
		}
	} else if r.ContentLength > 0 {
		// Not read here: it goes on from the server's, to the length it says and no further (see gateRest).
		r.Body = gateBody{gateRest(r, 0), r.Body}
	}
	// How the call was let through, for the ledger once the API has taken it.
	session, through := gk.session, proto.LarkLetThrough{}
	var rewriteBody []byte
	switch larkDecision(cfg, c) {
	case "allow":
		// A call larkCheck confirmed and a rule allows too (a space or remote runtime conversation confirms what a rule allows)
		// was let through by the card: it uses up its grant, as a confirmed call does. In a space or remote runtime conversation
		// a write goes only so: a rule's allow is not the phone's yes.
		if gr, ok := p.take(gk, c.Method, c.Path, c.Query, c.Account, body, isJSON); ok {
			session, through, rewriteBody = firstNonEmpty(gr.session, session), gr.through, gr.rewriteBody
		} else if (gk.space || gk.remote || n.isRemoteRuntimeSession(session)) && !larkGateRead(g, c) {
			gateNeedsPhone(w, command, c)
			return
		} else if rule, ok := firstLark(g.Rules, c); ok {
			through = proto.LarkLetThrough{Kind: proto.LarkThroughRule, Rule: &rule}
		}
	case "deny":
		gateRefuse(w, http.StatusForbidden, "这个操作按配置不允许（"+c.Method+" "+c.Path+"）。")
		return
	default:
		gr, ok := p.take(gk, c.Method, c.Path, c.Query, c.Account, body, isJSON)
		if !ok {
			gateNeedsPhone(w, command, c)
			return
		}
		session, through, rewriteBody = firstNonEmpty(gr.session, session), gr.through, gr.rewriteBody
	}
	if len(rewriteBody) > 0 {
		body = rewriteBody
		isJSON = true
		c.Body = string(body)
		if larkDecision(cfg, c) == "deny" {
			gateRefuse(w, http.StatusForbidden, "这个操作按配置不允许（"+c.Method+" "+c.Path+"）。")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		r.Header.Del("Content-Length")
		r.TransferEncoding = nil
	}
	sw := &statusWriter{ResponseWriter: w}
	if !larkGateRead(g, c) {
		sw.reply = &replyCopy{}
	}
	// Told once the answer has been passed on, also when ReverseProxy gives up on its body halfway (it panics with
	// http.ErrAbortHandler then): the API had taken the write by the time it answered 2xx. A client that went away
	// before the API answered leaves no 2xx, and whether the API executed the call is not known: nothing is told.
	defer func() {
		if sw.status/100 == 2 && !larkGateRead(g, c) {
			p.reportSent(n, proto.LarkSent{Command: command, Method: c.Method, Path: c.Path, SessionID: session, LetThrough: through}, body, isJSON, sw.reply)
		}
	}()
	if g.Relay {
		p.relay(r.Context(), n, sw, r, command, restPath, g.Headers)
		return
	}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			out := pr.Out
			out.URL.Scheme, out.URL.Host, out.Host = up.Scheme, up.Host, up.Host
			out.URL.Path = strings.TrimSuffix(up.Path, "/") + restPath
			out.URL.RawPath = strings.TrimSuffix(up.EscapedPath(), "/") + "/" + rest
			out.URL.RawQuery = r.URL.RawQuery
			for name := range out.Header {
				if !gatePassHeader(name, g.Headers) {
					out.Header.Del(name)
				}
			}
			if out.Header.Get("Origin") != "" { // the script names its base as the origin: the API knows its own
				out.Header.Set("Origin", up.Scheme+"://"+up.Host)
			}
			out.Header.Set(g.Auth.Header, gateAuthValue(g.Auth.Value, g.Env))
		},
		FlushInterval: -1, // streams (events, long polls) go as they come
		ErrorLog:      log.New(io.Discard, "", 0),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			gateRefuse(w, http.StatusBadGateway, command+" 的接口连不上："+err.Error())
		},
	}
	rp.ServeHTTP(sw, r)
}

// validateGateProxyPath validates and unescapes the escaped path after /<key>/
// so every segment seen by gate rules, AgentAPIWriteGET, grant matching, and
// the upstream or relayed server agrees unambiguously. It rejects empty
// interior segments ("//"), dot segments (".", "..", literal or
// percent-encoded), percent-encoded path separators ("%2f"), backslashes, URL
// delimiters ("?", "#"), and ASCII control characters.
func validateGateProxyPath(rawRest string) (string, error) {
	if rawRest == "" {
		return "/", nil
	}
	segs := strings.Split(rawRest, "/")
	var b strings.Builder
	b.Grow(len(rawRest) + 1)
	for i, rawSeg := range segs {
		if rawSeg == "" {
			if i == len(segs)-1 && len(segs) > 1 {
				b.WriteByte('/')
				continue
			}
			return "", errors.New("empty path segment")
		}
		seg, err := url.PathUnescape(rawSeg)
		if err != nil {
			return "", err
		}
		if seg == "." || seg == ".." {
			return "", errors.New("dot path segment")
		}
		for j := 0; j < len(seg); j++ {
			ch := seg[j]
			if ch < 0x20 || ch == 0x7f || ch == '/' || ch == '\\' || ch == '?' || ch == '#' {
				return "", errors.New("disallowed character in path")
			}
		}
		b.WriteByte('/')
		b.WriteString(seg)
	}
	return b.String(), nil
}

// gateBody is a request's body on its way to the API: what is read of it, and the server's body to close.
type gateBody struct {
	io.Reader
	io.Closer
}

// gateRest is what is left of the request's body after got bytes of it were read, no more than its Content-Length says.
// The transport reads once past the length of a body it sends, to see that there was no more, and that read must end
// here and not reach the server's body: net/http closes that as soon as the proxy begins to answer, and a read of it
// then fails and closes the connection to the API with the answer half read. A body of unknown length (chunked) goes
// on as it is: it is sent to its end, and nothing reads past that.
func gateRest(r *http.Request, got int) io.Reader {
	if r.ContentLength < 0 {
		return r.Body
	}
	return io.LimitReader(r.Body, max(r.ContentLength-int64(got), 0))
}

// statusWriter keeps the status the API answered with, which ReverseProxy writes through it, and, for a write (reply is
// set), a copy of the start of a JSON answer. Unwrap lets the response controller reach the real writer for flushing
// (streams go as they come) and upgrades.
type statusWriter struct {
	http.ResponseWriter
	status int
	reply  *replyCopy
}

func (s *statusWriter) WriteHeader(code int) {
	if code >= 200 && s.status == 0 { // not an informational 1xx before the answer
		s.answered(code)
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.answered(http.StatusOK)
	}
	n, err := s.ResponseWriter.Write(b)
	s.reply.add(b[:n]) // only what the command got, after it got it
	return n, err
}

func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// answered notes the status the API answered with, and starts copying the body when it is a 2xx JSON one of a write.
func (s *statusWriter) answered(code int) {
	s.status = code
	if s.reply != nil && code/100 == 2 && gateJSON(s.Header().Get("Content-Type")) {
		s.reply.on = true
		s.reply.encoding = strings.ToLower(strings.TrimSpace(s.Header().Get("Content-Encoding")))
	}
}

// gateJSON says whether a Content-Type is JSON's (the gate reads a request's body by the same words).
func gateJSON(contentType string) bool {
	mt, _, _ := mime.ParseMediaType(contentType)
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// replyCopy is the start of an answer's body, as it went to the command: at most gateReplyMax bytes, and whether there
// was more. Nothing is kept for an answer that is not a 2xx JSON one.
type replyCopy struct {
	on       bool
	encoding string // the answer's Content-Encoding
	buf      []byte
	over     bool
}

func (r *replyCopy) add(b []byte) {
	if r == nil || !r.on || len(b) == 0 {
		return
	}
	room := gateReplyMax - len(r.buf)
	if len(b) > room {
		b, r.over = b[:max(room, 0)], true
	}
	r.buf = append(r.buf, b...)
}

// messageRef is the id of the message the API's answer says the write made: the first non-empty string among
// data.message_id, data.messageId, message_id and messageId (a gateway's receipt names it one way, the Open Platform's
// answer another), as the API wrote it (proto.CleanMessageRef: one longer than proto.MessageRefMost characters is none,
// never a cut one). It is empty when the answer has none, is not JSON, was longer than gateReplyMax (compressed or not),
// or is compressed in a way other than gzip: the write is told all the same, only without an id. The copy is dropped once
// it is read.
func (r *replyCopy) messageRef() string {
	if r == nil {
		return ""
	}
	body := r.buf
	r.buf = nil
	if !r.on || r.over || len(body) == 0 {
		return ""
	}
	switch r.encoding {
	case "", "identity":
	case "gzip", "x-gzip":
		var ok bool
		if body, ok = gunzipCapped(body, gateReplyMax); !ok {
			return ""
		}
	default:
		return ""
	}
	var answer map[string]any
	if json.Unmarshal(body, &answer) != nil {
		return ""
	}
	data, _ := answer["data"].(map[string]any)
	for _, from := range []map[string]any{data, answer} {
		for _, key := range []string{"message_id", "messageId"} {
			if id, _ := from[key].(string); strings.TrimSpace(id) != "" {
				return proto.CleanMessageRef(id)
			}
		}
	}
	return ""
}

// gunzipCapped is b gunzipped, which may be at most most bytes; false when it is not gzip or has more. It reads most+1
// bytes at the most, so a small stream that inflates to gigabytes costs no more than an answer of most bytes.
func gunzipCapped(b []byte, most int) ([]byte, bool) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, false
	}
	return readCapped(zr, most)
}

// readCapped is all of r, which may be at most most bytes; false when it has more or fails. It reads most+1 bytes at
// the most.
func readCapped(r io.Reader, most int) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r, int64(most)+1))
	if err != nil || len(body) > most {
		return nil, false
	}
	return body, true
}

// reportSent tells the manager of a write the API took. It runs once the API's answer has gone to the command and does
// not wait for the manager's: the words it said come from the body when it was JSON (proto.WrittenText), the id of the
// message from the copy of the answer's start (replyCopy), and a report that fails is only logged, the write is done all
// the same. At most gateReportsMax are in flight, the one over waiting up to gateReportWait for a place; a node without
// a hub has nobody to tell.
func (p *gateProxy) reportSent(n *Node, s proto.LarkSent, body []byte, isJSON bool, reply *replyCopy) {
	peer := n.peer
	if peer == nil {
		return
	}
	wait := time.NewTimer(gateReportWait)
	defer wait.Stop()
	select {
	case p.reporting <- struct{}{}:
	case <-wait.C:
		n.logger.Printf("assistant: gate proxy: %s %s: too many reports in flight, not told of it", s.Method, s.Path)
		return
	}
	go func() {
		defer func() { <-p.reporting }()
		if isJSON {
			s.Text = proto.WrittenText(body, gateTextMost)
		}
		s.MessageRef = reply.messageRef()
		body = nil // the call below can take a while: the request body is let go of, as messageRef let go of the copy of the answer
		ctx, cancel := context.WithTimeout(context.Background(), gateReportFor)
		defer cancel()
		if err := peer.Call(ctx, proto.MethodLarkSent, s, nil); err != nil { // never the words it said
			n.logger.Printf("assistant: gate proxy: telling the manager of %s %s: %v", s.Method, s.Path, err)
		}
	}()
}

// gatePassHeader: a header of the request that goes on to the API. A credential of the sender's own, cookies and
// forwarding headers stay here.
func gatePassHeader(name string, allowed []string) bool {
	switch name = strings.ToLower(name); name {
	case "accept", "accept-encoding", "accept-language", "content-type", "user-agent", "origin", "range", "if-none-match",
		"if-modified-since":
		return true
	}
	return name != "authorization" && slices.Contains(allowed, name)
}

var gateAuthRef = lazyre.New(`\$\{([A-Z_][A-Z0-9_]*)\}`)

// gateAuthValue is a gate's Auth.Value with its credentials filled in.
func gateAuthValue(tmpl string, env map[string]string) string {
	return gateAuthRef.ReplaceAllStringFunc(tmpl, func(m string) string { return env[m[2:len(m)-1]] })
}

// gateNeedsPhone refuses a call that goes only after the user confirmed it on the phone, and says how to ask.
func gateNeedsPhone(w http.ResponseWriter, command string, c larkCall) {
	gateRefuse(w, http.StatusForbidden, "这个操作要用户先在手机上确认（"+c.Method+" "+c.Path+"）：单独一条 `"+command+
		" METHOD PATH [BODY]` 发，不放进脚本、循环或 $(…) 里，确认卡片会发到用户手机上，确认过的才发得出去。")
}

// gateRefuse answers for the proxy, in JSON as the APIs do: the gated command prints it, so the model reads why.
func gateRefuse(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg, "from": "persona gate"})
}
