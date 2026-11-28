package node

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

var errCodexNoOwner = errors.New("没有可共享的 app-server 运行入口")

const codexWriterBusy = "codex_writer_busy"

// An active-writer answer means this app-server did not accept the thread. Nothing was delivered, and trying another
// server would only contend for the same writer lock; continue through the original owner or wait for it to disconnect.
func codexWriterConflict(err error) error {
	var re *codexError
	if !errors.As(err, &re) || re.Code != -32600 || !strings.Contains(strings.ToLower(re.Message), "already has an active writer") {
		return nil
	}
	return refusal(codexWriterBusy, "这个 Codex 会话正由电脑上的另一个进程写入，这次没有发送：请在原 Codex 窗口或终端里继续；如果那边已退出，等它断开后刷新再试。",
		map[string]any{"detail": re.Error()})
}

type remoteThread struct {
	id, publicID, title, cwd, path, status, turn, parent, partial string
	// ended is the last turn turn/completed ended: a snapshot read before it (join, activeTurn, the metadata read)
	// does not bring it back, and its completion arriving again is not another end (#5598).
	ended string
	// model and effort are what the app-server says the thread is configured with (codex_effort.go); "" unknown / unset.
	model, effort       string
	serviceTier         string
	serviceTierRevision uint64
	// failure / failureKind: the latest turn failed, as turn/completed said it (status failed, its error, #2185);
	// cleared when a turn starts. Session.TurnError / TurnErrorKind, before the rollout has it.
	failure, failureKind string
	joined               bool
	// provisional marks an in-flight coldResume pre-registration: incoming requests and notifications for the thread
	// are accepted, while codexOwner does not advertise ownership until thread/resume succeeds.
	provisional bool
	// ready is closed when an in-flight provisional coldResume finishes (success or rollback), so early approval
	// responses wait for ownership confirmation before forwarding their reply.
	ready chan struct{}
	// pinnedSeq is the refreshSeq when coldResume registered or completed this thread; refreshes started with an
	// older snapshot sequence must preserve it instead of dropping it as unloaded.
	pinnedSeq uint64
	buffer    *strings.Builder
	lastDelta time.Time
	created   time.Time // local thread/start completed while a loaded-list snapshot was in flight
	revision  uint64
	files     map[string][]string
}

type remoteAsk struct {
	cancel              context.CancelFunc
	thread, interaction string
}

type codexRemote struct {
	n          *Node
	spec       proto.CodexServer
	generation string
	wire       *codexConn
	socket     *websocket.Conn
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	once       sync.Once
	delivery   sync.Mutex
	mu         sync.Mutex
	threads    map[string]remoteThread
	asks       map[string]*remoteAsk
	joins      typingLocks
	finished   map[string]bool
	ready      bool
	// models: the efforts each model takes, from model/list (codex_effort.go); nil until read.
	models map[string]codexModelEfforts
	// slow: loaded threads whose last thread/read timed out; read again only after codexRetryWait (codex_backoff.go).
	slow map[string]codexRetry
	// seen: what the refreshes learned about each loaded thread, listed or not; an id is forgotten once it is no
	// longer loaded, so this follows the loaded list.
	seen map[string]codexSeen
	// readWait, refreshWait: one thread/read's timeout and a whole refresh's; 0 is codexReadWait, codexRefreshWait.
	readWait, refreshWait time.Duration
	// refreshSeq orders refresh snapshots against coldResume updates so a refresh started before thread/resume
	// never drops a newly cold-activated thread.
	refreshSeq uint64
	// A loaded-list snapshot can outlive the startup that created its thread.
	// Keep discarded starts out of the registry until the server drops them.
	discardedStarts map[string]time.Time
	// Shared paginated thread/loaded/list cache and singleflight for background joins (#13578).
	loadedAt     time.Time
	loadedSet    map[string]bool
	loadedActive *codexLoadedFlight
	// Bounded background join dispatcher for this app-server instance (#13578).
	joinMu     sync.Mutex
	joinQueue  []string
	joinQueued map[string]bool
	joinActive int
}

type codexLoadedFlight struct {
	done chan struct{}
	set  map[string]bool
	err  error
}

// codexLoadedCacheTTL bounds how long background joins share one paginated thread/loaded/list snapshot.
var codexLoadedCacheTTL = 2 * time.Second

// codexSeen is what refreshes keep about a loaded thread besides its listing (#12517).
type codexSeen struct {
	// ephemeral: its read said ephemeral, one of the app-server's own background jobs (a title, a sub-Agent helper).
	// A thread is ephemeral or not from its creation, so it is not read again while it stays loaded.
	ephemeral bool
	// read: when a refresh last had its thread/read end on its own, answered, failed or timed out (readOrder).
	read time.Time
	// cut: when a refresh last sent its thread/read and ran out of its own time before the read ended. That is not a
	// read: counted as one, the same threads would be cut on every refresh that falls short the same way (readOrder).
	cut time.Time
}

// A refresh reads the loaded threads a few at a time, each read with its own timeout inside the refresh's: one slow
// thread neither uses up the others' time nor fails them with it.
const (
	codexRefreshWait = 20 * time.Second
	codexReadWait    = 5 * time.Second
	codexReadWorkers = 4
)

func (n *Node) ensureCodexRemote(ctx context.Context, spec proto.CodexServer) {
	r := n.codices
	r.mu.Lock()
	old := r.remotes[spec.ID]
	if old != nil && old.spec == spec {
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()
	now, print := time.Now(), codexEndpointPrint(spec.Endpoint) // a few lstats, outside the lock
	r.mu.Lock()
	old = r.remotes[spec.ID]
	if old != nil && old.spec == spec || old == nil && !r.retryDue(spec, now, print) { // codex_backoff.go
		r.mu.Unlock()
		return
	}
	r.attempted(spec, now, print)
	cctx, cancel := context.WithCancel(ctx)
	c := &codexRemote{n: n, spec: spec, generation: newUUID(), ctx: cctx, cancel: cancel, done: make(chan struct{}), threads: map[string]remoteThread{}, asks: map[string]*remoteAsk{}, finished: map[string]bool{}}
	r.remotes[spec.ID] = c
	r.mu.Unlock()
	if old != nil {
		old.close()
	}
	go c.run()
}

func rejectCodexRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func validateCodexEndpoint(endpoint string) (string, *http.Client, error) {
	parse := endpoint
	if rest, ok := strings.CutPrefix(endpoint, "unix://"); ok && runtime.GOOS == "windows" {
		parse = "unix:///" + filepath.ToSlash(rest) // C:\… is no URL; the checks below want only its query and the like
	}
	u, err := url.Parse(parse)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, errors.New("app-server 入口无效")
	}
	if u.Scheme == "unix" {
		path := strings.TrimPrefix(endpoint, "unix://")
		if !filepath.IsAbs(path) {
			return "", nil, errors.New("Unix 入口必须是绝对路径")
		}
		path, err := codexSocketPath(path)
		if err != nil {
			return "", nil, err
		}
		if err := privateSocket(path); err != nil { // codex_remote_unix.go, codex_remote_windows.go
			return "", nil, err
		}
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}}
		return "ws://localhost/", &http.Client{Transport: tr, CheckRedirect: rejectCodexRedirect}, nil
	}
	if u.Scheme != "ws" || u.Port() == "" || u.Path != "" && u.Path != "/" {
		return "", nil, errors.New("只支持本机 Unix 或回环 WebSocket 入口")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return "", nil, errors.New("WebSocket 入口必须是回环 IP，不能是外部地址或主机名")
	}
	// Never use HTTP_PROXY for a private local endpoint.
	return endpoint, &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: rejectCodexRedirect}, nil
}

// codexSocketPath is the socket a Unix control endpoint names: the path itself (0.153), or — from 0.163, where every
// control socket is an alias — the one target Codex itself recognises as its own: a socket named after the SHA-256 of
// the alias's canonical path in <canonical /tmp>/codex-daemon-<uid> (codex-rs uds/src/daemon_directory.rs,
// app-server-transport unix_socket.rs). Any other symlink is refused; the alias's directory must be the user's and not
// writable by others. The caller checks the target's own directory and mode.
func codexSocketPath(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSymlink == 0 {
		return path, nil // a missing path fails the caller's checks
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", errors.New("Unix 入口不可访问")
	}
	if pst, err := os.Stat(parent); err != nil {
		return "", errors.New("Unix 入口不可访问")
	} else if !osproc.OwnedByMe(pst) || pst.Mode().Perm()&0o022 != 0 {
		return "", errors.New("Unix 入口所在目录不属于当前用户，或别人能改")
	}
	tmp, err := filepath.EvalSymlinks("/tmp")
	if err != nil {
		return "", errors.New("Unix 入口不可访问")
	}
	sum := sha256.Sum256([]byte(filepath.Join(parent, filepath.Base(path))))
	want := filepath.Join(tmp, "codex-daemon-"+strconv.Itoa(os.Geteuid()), hex.EncodeToString(sum[:]))
	if target, err := os.Readlink(path); err != nil || target != want {
		return "", errors.New("Unix 入口是指向别处的符号链接")
	}
	if _, err := os.Stat(want); err != nil {
		return "", fmt.Errorf("Codex control socket symlink target: %w", err)
	}
	return want, nil
}

func (c *codexRemote) run() {
	reached := false // the handshake went through: the endpoint's backoff ends (codex_backoff.go)
	defer func() {
		c.close()
		c.n.codices.mu.Lock()
		if c.n.codices.remotes[c.spec.ID] == c {
			delete(c.n.codices.remotes, c.spec.ID)
			c.n.codices.attemptEnded(c.spec, reached)
		}
		c.n.codices.mu.Unlock()
	}()
	target, client, err := validateCodexEndpoint(c.spec.Endpoint)
	if err != nil {
		diag := codexDiagEndpointInvalid
		if c.spec.Endpoint == codexControlEndpoint(c.spec.Home) && codexDaemonSocketGone(c.spec.Home) != nil {
			diag = codexDiagSocketGone
		}
		c.n.codices.setRemoteDiag(c.spec.Endpoint, diag)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	socket, resp, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: client})
	cancel()
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		diag := codexDiagUnreachable
		if c.spec.Endpoint == codexControlEndpoint(c.spec.Home) && codexDaemonSocketGone(c.spec.Home) != nil {
			diag = codexDiagSocketGone
		}
		c.n.codices.setRemoteDiag(c.spec.Endpoint, diag)
		return
	}
	socket.SetReadLimit(64 << 20)
	c.mu.Lock()
	c.socket = socket
	c.mu.Unlock()
	wire := &codexConn{pending: map[int64]chan codexReply{}, ended: c.done}
	wire.writeMessage = func(b []byte) error {
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		defer cancel()
		return socket.Write(ctx, websocket.MessageText, b)
	}
	wire.onMessage = c.message
	c.mu.Lock()
	c.wire = wire
	c.mu.Unlock()
	go func() {
		for {
			_, b, err := socket.Read(c.ctx)
			if err != nil {
				c.close()
				return
			}
			var m codexMessage
			if json.Unmarshal(b, &m) == nil {
				wire.receive(m)
			}
		}
	}()
	ctx, cancel = context.WithTimeout(c.ctx, 15*time.Second)
	var init struct {
		CodexHome string `json:"codexHome"`
	}
	err = wire.call(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "persona", "title": "Persona", "version": c.n.Version}, "capabilities": map[string]any{"experimentalApi": true}}, &init)
	cancel()
	if err != nil {
		c.n.codices.setRemoteDiag(c.spec.Endpoint, codexDiagHandshakeFailed)
		return
	}
	if init.CodexHome != "" && canonicalCodexHome(init.CodexHome, c.n.home) != c.spec.Home {
		c.n.codices.setRemoteDiag(c.spec.Endpoint, codexDiagHomeMismatch)
		return
	}
	if err = wire.send(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		c.n.codices.setRemoteDiag(c.spec.Endpoint, codexDiagHandshakeFailed)
		return
	}
	if err = c.refresh(); err != nil {
		c.n.codices.setRemoteDiag(c.spec.Endpoint, codexDiagHandshakeFailed)
		return
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
	c.n.codices.setRemoteDiag(c.spec.Endpoint, codexDiagNone)
	reached = true
	c.n.notifyCodex()
	go c.loadModels()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		cfg := c.n.codexInventory()
		var rejoin []string
		for id, instance := range cfg.Joined {
			if instance == c.spec.ID {
				rejoin = append(rejoin, id)
			}
		}
		if len(rejoin) > 0 {
			c.enqueueBackgroundJoins(rejoin)
		}
		select {
		case <-c.ctx.Done():
			return
		case <-tick.C:
			if c.refresh() != nil {
				return
			}
		}
	}
}

func (c *codexRemote) close() {
	c.once.Do(func() {
		c.delivery.Lock()
		defer c.delivery.Unlock()
		c.cancel()
		c.mu.Lock()
		c.ready = false
		socket := c.socket
		wire := c.wire
		for _, a := range c.asks {
			a.cancel()
		}
		ids := make([]string, 0, len(c.threads))
		for _, t := range c.threads {
			ids = append(ids, t.publicID)
		}
		c.mu.Unlock()
		if wire != nil {
			wire.mu.Lock()
			wire.closed = true
			wire.mu.Unlock()
		}
		close(c.done)
		if socket != nil {
			socket.CloseNow()
		}
		for _, id := range ids {
			c.n.emitDelta(id, "")
		}
		c.n.notifyCodex()
	})
}

func (c *codexRemote) loaded(ctx context.Context) ([]string, error) {
	var ids []string
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		p := map[string]any{"limit": 100}
		if cursor != "" {
			p["cursor"] = cursor
		}
		if err := c.wire.call(ctx, "thread/loaded/list", p, &page); err != nil {
			return nil, err
		}
		ids = append(ids, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			break
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, errors.New("app-server 返回重复的分页游标")
		}
		seen[cursor] = true
	}
	c.noteLoaded(ids, time.Now())
	return ids, nil
}

func (c *codexRemote) noteLoaded(ids []string, at time.Time) {
	set := make(map[string]bool, len(ids))
	for _, raw := range ids {
		set[raw] = true
	}
	c.mu.Lock()
	c.loadedSet = set
	c.loadedAt = at
	c.mu.Unlock()
}

// loadedShared returns the loaded thread ID set for this app-server, coalescing concurrent callers into one
// paginated thread/loaded/list query and reusing a recent snapshot for background joins (fresh=false) (#13578).
func (c *codexRemote) loadedShared(ctx context.Context, fresh bool) (map[string]bool, error) {
	c.mu.Lock()
	if !fresh && c.loadedSet != nil && time.Since(c.loadedAt) < codexLoadedCacheTTL {
		set := c.loadedSet
		c.mu.Unlock()
		return set, nil
	}
	if f := c.loadedActive; f != nil {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.set, f.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f := &codexLoadedFlight{done: make(chan struct{})}
	c.loadedActive = f
	c.mu.Unlock()

	go func() {
		lctx, cancel := context.WithTimeout(c.ctx, codexRefreshWait)
		ids, err := c.loaded(lctx)
		cancel()
		var set map[string]bool
		if err == nil {
			set = make(map[string]bool, len(ids))
			for _, raw := range ids {
				set[raw] = true
			}
		}
		f.set, f.err = set, err
		c.mu.Lock()
		if c.loadedActive == f {
			c.loadedActive = nil
		}
		c.mu.Unlock()
		close(f.done)
	}()

	select {
	case <-f.done:
		return f.set, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// enqueueBackgroundJoins runs background thread joins for this app-server with bounded concurrency (#13578).
func (c *codexRemote) enqueueBackgroundJoins(ids []string) {
	if c == nil || len(ids) == 0 || c.ctx.Err() != nil {
		return
	}
	limit := max(codexJoinConcurrency, 1)
	c.joinMu.Lock()
	if c.joinQueued == nil {
		c.joinQueued = map[string]bool{}
	}
	for _, id := range ids {
		if id == "" || c.joinQueued[id] {
			continue
		}
		c.joinQueued[id] = true
		c.joinQueue = append(c.joinQueue, id)
	}
	for c.joinActive < limit && len(c.joinQueue) > 0 {
		c.joinActive++
		go c.backgroundJoinWorker()
	}
	c.joinMu.Unlock()
}

func (c *codexRemote) backgroundJoinWorker() {
	defer func() {
		c.joinMu.Lock()
		c.joinActive--
		c.joinMu.Unlock()
	}()
	for {
		if c.ctx.Err() != nil {
			return
		}
		c.joinMu.Lock()
		if len(c.joinQueue) == 0 {
			c.joinMu.Unlock()
			return
		}
		id := c.joinQueue[0]
		c.joinQueue = c.joinQueue[1:]
		delete(c.joinQueued, id)
		c.joinMu.Unlock()

		owner, t, err := c.n.codexOwner(id)
		if err != nil || owner != c || t.joined || !c.n.cx.startJoin(id) {
			continue
		}
		jctx, cancel := context.WithTimeout(c.ctx, 30*time.Second)
		err = c.join(jctx, id, false)
		cancel()
		c.n.cx.endJoin(id, err)
		if err != nil {
			if c.ctx.Err() == nil {
				c.n.logf("codex %s: joining it on app-server %s failed, it stays on the terminal: %v", id, c.spec.ID, err)
			}
			continue
		}
		c.n.logf("codex %s: joined on app-server %s", id, c.spec.ID)
	}
}

type remoteThreadStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

func (s remoteThreadStatus) sessionStatus() string {
	if slices.Contains(s.ActiveFlags, "waitingOnApproval") || slices.Contains(s.ActiveFlags, "waitingOnUserInput") {
		return proto.StatusBlocked
	}
	if s.Type == "active" {
		return proto.StatusWorking
	}
	return proto.StatusDone
}

type remoteThreadData struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Model          string          `json:"model"`
	Effort         string          `json:"reasoningEffort"` // configured now; null (unset) reads as ""
	Preview        string          `json:"preview"`
	Cwd            string          `json:"cwd"`
	Path           string          `json:"path"`
	HistoryMode    string          `json:"historyMode"`
	ParentThreadID string          `json:"parentThreadId"`
	Ephemeral      bool            `json:"ephemeral"`
	Source         json.RawMessage `json:"source"`
	Turns          []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"turns"`
	Status remoteThreadStatus `json:"status"`
}

func (c *codexRemote) refresh() error {
	snapshot := time.Now()
	c.mu.Lock()
	wait, budget := cmp.Or(c.readWait, codexReadWait), cmp.Or(c.refreshWait, codexRefreshWait)
	c.refreshSeq++
	snapSeq := c.refreshSeq
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(c.ctx, budget)
	defer cancel()
	ids, err := c.loaded(ctx)
	if err != nil {
		return err
	}
	seenRevision := map[string]uint64{}
	next := map[string]remoteThread{}
	known := map[string]remoteThread{}
	var reads []string
	waiting, ephemeral := 0, 0
	now := time.Now()
	c.mu.Lock()
	for _, raw := range ids {
		if _, dup := known[raw]; dup || !validID(raw) {
			continue
		}
		t := c.threads[raw]
		if _, discarded := c.discardedStarts[raw]; discarded {
			known[raw] = remoteThread{}
			continue
		}
		seenRevision[raw] = t.revision
		if t.provisional {
			// Keep provisional coldResume state out of refresh's server-confirmed copy so a rolled-back
			// provisional thread is never resurrected as provisional or joined.
			t.provisional = false
			t.joined = false
			t.pinnedSeq = 0
			t.ready = nil
		}
		known[raw] = t
		if c.seen[raw].ephemeral {
			// Read once and found to be the app-server's own background job: never a session, so not read again
			// while it stays loaded.
			ephemeral++
			continue
		}
		if b, ok := c.slow[raw]; ok && now.Sub(b.at) < codexRetryWait(b.fails)-codexRetrySlack {
			// Timed out lately: keep the last copy until its next read is due.
			if t.id != "" {
				next[raw] = t
			}
			waiting++
			continue
		}
		reads = append(reads, raw)
	}
	c.readOrder(reads)
	c.mu.Unlock()
	type read struct {
		data remoteThreadData
		err  error
		// cut: not asked, or not answered before the refresh's own time ran out; says nothing about the thread.
		cut bool
		// asked: its thread/read went out, answered or not.
		asked bool
	}
	results := make([]read, len(reads))
	for i := range results {
		results[i].cut = true
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range min(codexReadWorkers, len(reads)) {
		wg.Go(func() {
			for i := range work {
				if ctx.Err() != nil {
					continue
				}
				var reply struct {
					Thread remoteThreadData `json:"thread"`
				}
				rctx, rcancel := context.WithTimeout(ctx, wait)
				err := c.wire.call(rctx, "thread/read", map[string]any{"threadId": reads[i], "includeTurns": false}, &reply)
				rcancel()
				results[i] = read{data: reply.Thread, err: err, cut: err != nil && ctx.Err() != nil, asked: true}
			}
		})
	}
dispatch:
	for i := range reads {
		select {
		case work <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(work)
	wg.Wait()
	// Backoff counts from when the reads ended: a refresh that waited out a slow read is followed by the next at once.
	ended := time.Now()
	failed, cut, timedOut, settled := 0, 0, map[string]bool{}, map[string]bool{}
	// readEnded, readCut: its thread/read went out and ended on its own, or went out and the refresh's time ran out
	// first (codexSeen).
	readEnded, readCut, found := map[string]bool{}, map[string]bool{}, map[string]bool{}
	var firstErr error
	for i, raw := range reads {
		t := known[raw]
		r := results[i]
		readEnded[raw], readCut[raw] = r.asked && !r.cut, r.asked && r.cut
		if r.cut || r.err != nil {
			// Still loaded, just not read this time (a slow or failed call): keep the last copy and its asks.
			if r.cut {
				cut++
			} else {
				if failed == 0 {
					firstErr = r.err
				}
				failed++
				timedOut[raw] = errors.Is(r.err, context.DeadlineExceeded)
				settled[raw] = !timedOut[raw]
			}
			if t.id != "" {
				next[raw] = t
			}
			continue
		}
		settled[raw] = true
		d := r.data
		// Ephemeral threads are Codex's own background jobs (a title, say): no rollout, never resumable, and they stay
		// loaded as "active" after they finish. Not a conversation, so not a session; and since a thread is ephemeral
		// or not from its creation, not read again while it stays loaded (codexSeen).
		if d.ID == raw && d.Ephemeral {
			found[raw] = true
		}
		if d.ID != raw || d.Status.Type == "notLoaded" || d.Ephemeral {
			continue
		}
		path := rolloutPath(c.spec.Home, raw)
		c.mu.Lock()
		if _, discarded := c.discardedStarts[raw]; discarded {
			c.mu.Unlock()
			continue
		}
		public := c.n.registerCodex(c.spec.Home, raw, path)
		c.mu.Unlock()
		t.id, t.publicID, t.title, t.cwd = raw, public, firstNonEmpty(d.Name, d.Preview), d.Cwd
		t.path = d.Path
		if d.Model != "" { // an app-server that reports a thread's settings (older ones do not: keep what join said)
			t.model, t.effort = d.Model, d.Effort
		}
		t.parent = d.ParentThreadID
		if t.parent == "" && strings.Contains(string(d.Source), "subAgent") {
			t.parent = "parent-managed"
		}
		if t.parent != "" && validID(t.parent) {
			// Keep the raw thread ID: decorateCodexSession maps ancestry to public IDs once.
			c.n.registerCodex(c.spec.Home, t.parent, "")
		}
		if t.files == nil {
			t.files = map[string][]string{}
		}
		t.status = d.Status.sessionStatus()
		if d.Status.Type != "active" {
			t.turn = ""
			t.partial = ""
			t.buffer = nil
		}
		for _, turn := range d.Turns {
			if turn.Status == "inProgress" && turn.ID != t.ended {
				t.turn = turn.ID
			}
		}
		next[raw] = t
	}
	if failed > 0 || cut > 0 {
		timeouts := 0
		for _, ok := range timedOut {
			if ok {
				timeouts++
			}
		}
		c.n.logf("codex %s: thread/read failed for %d of %d threads (%d timed out, %d not read in time, %d waiting after a timeout, %d ephemeral skipped): %v", c.spec.ID, failed, len(reads), timeouts, cut, waiting, ephemeral, cmp.Or(firstErr, ctx.Err()))
	}
	c.mu.Lock()
	for raw, at := range c.discardedStarts {
		delete(next, raw)
		if _, listed := known[raw]; !listed && snapshot.Sub(at) > budget {
			delete(c.discardedStarts, raw)
		}
	}
	slow := map[string]codexRetry{}
	seen := make(map[string]codexSeen, len(known))
	for raw := range known { // threads no longer loaded are forgotten
		if b, ok := c.slow[raw]; timedOut[raw] {
			b.at, b.fails = ended, b.fails+1
			slow[raw] = b
		} else if ok && !settled[raw] {
			slow[raw] = b // still waiting, or its read was cut short
		}
		s := c.seen[raw]
		s.ephemeral = s.ephemeral || found[raw]
		// A refresh that ran alongside may have asked later: keep its stamp.
		if readEnded[raw] && s.read.Before(now) {
			s.read = now
		}
		if readCut[raw] && s.cut.Before(now) {
			s.cut = now
		}
		seen[raw] = s
	}
	c.slow, c.seen = slow, seen
	// Preserve events that arrived during metadata RPCs; metadata never overwrites a joined thread's current turn.
	for raw, t := range next {
		if old, ok := c.threads[raw]; ok {
			if old.revision != seenRevision[raw] || old.provisional {
				t.joined, t.turn, t.partial, t.files, t.status, t.revision, t.buffer, t.lastDelta = old.joined, old.turn, old.partial, old.files, old.status, old.revision, old.buffer, old.lastDelta
				t.failure, t.failureKind, t.ended = old.failure, old.failureKind, old.ended
				t.serviceTier, t.serviceTierRevision = old.serviceTier, old.serviceTierRevision
				t.model, t.effort = old.model, old.effort // an effort just set (codex_effort.go) over a read from before it
				t.path, t.title = old.path, old.title
				t.provisional, t.pinnedSeq, t.ready = old.provisional, old.pinnedSeq, old.ready
			}
			if old.status == proto.StatusBlocked && t.turn != "" && t.turn == old.turn {
				t.status = proto.StatusBlocked
			}
			next[raw] = t
		} else if t.provisional || t.joined || t.ready != nil {
			// A provisional pre-registration rolled back while this refresh was in flight: keep only the
			// server-confirmed loaded thread as non-provisional and unjoined.
			t.provisional = false
			t.joined = false
			t.pinnedSeq = 0
			t.ready = nil
			next[raw] = t
		}
	}
	// A local start after this snapshot owns the first turn and its settings. A cold resume also
	// stays in the registry when an older loaded list omits it or the resume is still provisional.
	for raw, old := range c.threads {
		_, listed := next[raw]
		if old.created.After(snapshot) || (!listed && (old.provisional || old.pinnedSeq > snapSeq)) {
			next[raw] = old
		}
	}
	for _, a := range c.asks {
		if t, ok := next[a.thread]; ok {
			t.status = proto.StatusBlocked
			next[a.thread] = t
		} else {
			a.cancel()
		}
	}
	var gone []remoteThread
	for raw, t := range c.threads {
		if _, ok := next[raw]; !ok {
			gone = append(gone, t)
		}
	}
	c.threads = next
	c.mu.Unlock()
	for _, t := range gone { // no longer loaded: an effort still waiting for its next turn is not for a later one
		c.n.dropCodexPending(t.publicID, c.generation, t.id)
	}
	return nil
}

// readOrder sorts a refresh's reads into the order they go out in, which matters only when the refresh's time cannot
// cover them all (#12517): first the threads with a turn running or a request waiting, whose next state is the one
// Unread and pushes wait for; then the others by when each was last read, never read first. A read the refresh's time
// cut short does not count as read, but among threads last read at the same time goes behind those not asked, and the
// loaded list's order breaks the remaining ties. So a thread left unread goes ahead of those read in its place, and a
// long loaded list does not cut the same threads on every refresh. Callers hold c.mu.
func (c *codexRemote) readOrder(reads []string) {
	busy := func(raw string) bool {
		status := c.threads[raw].status
		return status == proto.StatusWorking || status == proto.StatusBlocked
	}
	slices.SortStableFunc(reads, func(a, b string) int {
		if x, y := busy(a), busy(b); x != y {
			if x {
				return -1
			}
			return 1
		}
		sa, sb := c.seen[a], c.seen[b]
		return cmp.Or(sa.read.Compare(sb.read), sa.cut.Compare(sb.cut))
	})
}

func (n *Node) codexOwner(id string) (*codexRemote, remoteThread, error) {
	if n.codexBlocked(id) {
		return nil, remoteThread{}, errors.New("会话 ID 与另一来源重复，不能判断操作目标")
	}
	src, known := n.codexSource(id)
	if !known {
		return nil, remoteThread{}, errCodexNoOwner
	}
	n.codices.mu.Lock()
	var remotes []*codexRemote
	for _, c := range n.codices.remotes {
		remotes = append(remotes, c)
	}
	n.codices.mu.Unlock()
	var owner *codexRemote
	var thread remoteThread
	for _, c := range remotes {
		if c.spec.Home != src.home {
			continue
		}
		c.mu.Lock()
		t, ok := c.threads[src.thread]
		ready := c.ready
		c.mu.Unlock()
		if !ready || !ok || t.provisional {
			continue
		}
		if owner != nil {
			return nil, remoteThread{}, errors.New("仅查看：多个 app-server 声称运行这个会话，不能确定发送目标")
		}
		owner, thread = c, t
	}
	if owner == nil {
		return nil, remoteThread{}, errCodexNoOwner
	}
	return owner, thread, nil
}

// codexSharedRemote returns an unambiguous ready shared app-server connection for home, preferring the official
// control socket when multiple instances exist. Returns nil when none is connected or selection is ambiguous.
func (n *Node) codexSharedRemote(home string) *codexRemote {
	if n == nil || n.codices == nil || home == "" {
		return nil
	}
	home = canonicalCodexHome(home, n.home)
	if home == "" {
		return nil
	}
	controlEndpoint := codexControlEndpoint(home)
	n.codices.mu.Lock()
	var candidates []*codexRemote
	for _, c := range n.codices.remotes {
		if c.spec.Home == home {
			candidates = append(candidates, c)
		}
	}
	n.codices.mu.Unlock()
	var ready []*codexRemote
	for _, c := range candidates {
		c.mu.Lock()
		ok := c.ready && c.wire != nil
		c.mu.Unlock()
		if !ok {
			continue
		}
		if c.spec.Endpoint == controlEndpoint {
			return c
		}
		ready = append(ready, c)
	}
	if len(ready) == 1 {
		return ready[0]
	}
	return nil
}

// coldResume explicitly loads and subscribes an idle history thread on this shared app-server after the caller
// verified no live writer or active writer lock holds it. Never called during session listing.
func (c *codexRemote) coldResume(ctx context.Context, id string, src codexSource, cwd string) error {
	defer c.joins.lock(id)()
	c.mu.Lock()
	ready, wire := c.ready, c.wire
	existing, loaded := c.threads[src.thread]
	c.mu.Unlock()
	if !ready || wire == nil {
		return takeoverRefusal("shared_unavailable")
	}
	if owner, _, err := c.n.codexOwner(id); err == nil && owner != c {
		return takeoverRefusal("writer_busy")
	} else if err != nil && !errors.Is(err, errCodexNoOwner) {
		return err
	}
	if loaded && !existing.provisional && existing.joined {
		return nil
	}
	// Pre-register the subscription before thread/resume so immediate approval requests and turn events emitted
	// before the RPC returns are received instead of dropped, and concurrent refreshes cannot erase it.
	var readyCh chan struct{}
	c.mu.Lock()
	c.refreshSeq++
	preSeq := c.refreshSeq
	wasLoaded := false
	if cur, ok := c.threads[src.thread]; ok {
		wasLoaded = !cur.provisional
		cur.joined = true
		cur.pinnedSeq = preSeq
		cur.revision++
		if cur.files == nil {
			cur.files = map[string][]string{}
		}
		c.threads[src.thread] = cur
	} else {
		readyCh = make(chan struct{})
		c.threads[src.thread] = remoteThread{
			id:          src.thread,
			publicID:    id,
			cwd:         cwd,
			path:        src.path,
			status:      proto.StatusDone,
			joined:      true,
			provisional: true,
			ready:       readyCh,
			pinnedSeq:   preSeq,
			revision:    1,
			files:       map[string][]string{},
		}
	}
	preRev := c.threads[src.thread].revision
	c.mu.Unlock()

	var readyOnce sync.Once
	closeReady := func() {
		if readyCh != nil {
			readyOnce.Do(func() { close(readyCh) })
		}
	}

	rollback := func() {
		var cancelAsks []*remoteAsk
		c.mu.Lock()
		if cur, ok := c.threads[src.thread]; ok {
			if cur.provisional || !wasLoaded {
				delete(c.threads, src.thread)
			} else {
				cur.joined = false
				cur.ready = nil
				cur.revision++
				c.threads[src.thread] = cur
			}
		}
		for key, ask := range c.asks {
			if ask.thread == src.thread {
				delete(c.asks, key)
				cancelAsks = append(cancelAsks, ask)
			}
		}
		c.mu.Unlock()
		for _, ask := range cancelAsks {
			if c.n.peer != nil {
				_ = c.n.peer.Notify("interaction.dismiss", map[string]any{"id": ask.interaction})
			}
			ask.cancel()
		}
		closeReady()
	}

	var reply struct {
		Thread      remoteThreadData `json:"thread"`
		Model       string           `json:"model"`
		Effort      string           `json:"reasoningEffort"`
		ServiceTier json.RawMessage  `json:"serviceTier"`
	}
	if err := wire.call(ctx, "thread/resume", map[string]any{"threadId": src.thread, "excludeTurns": true}, &reply); err != nil {
		rollback()
		if codexWriterConflict(err) != nil {
			return takeoverRefusal("writer_busy")
		}
		return err
	}
	if reply.Thread.ID != src.thread {
		rollback()
		return errors.New("app-server 接入返回了不同的会话")
	}
	d := reply.Thread
	c.mu.Lock()
	c.refreshSeq++
	doneSeq := c.refreshSeq
	t := c.threads[src.thread]
	eventsArrived := t.revision != preRev
	t.id = src.thread
	t.publicID = id
	t.joined = true
	t.provisional = false
	t.ready = nil
	t.pinnedSeq = doneSeq
	if title := firstNonEmpty(d.Name, d.Preview); title != "" {
		t.title = title
	}
	if d.Cwd != "" {
		t.cwd = d.Cwd
	} else if t.cwd == "" {
		t.cwd = cwd
	}
	t.path = firstNonEmpty(d.Path, t.path, src.path)
	if m := firstNonEmpty(reply.Model, d.Model); m != "" && (!eventsArrived || t.model == "") {
		t.model, t.effort = m, firstNonEmpty(reply.Effort, d.Effort)
	}
	if tier := codexServiceTier(reply.ServiceTier); tier != "" && t.serviceTier == "" {
		t.serviceTier = tier
		t.serviceTierRevision++
	}
	if t.files == nil {
		t.files = map[string][]string{}
	}
	if !eventsArrived {
		t.status = proto.StatusDone
		if d.Status.Type == "active" {
			t.status = proto.StatusWorking
		}
		if slices.Contains(d.Status.ActiveFlags, "waitingOnApproval") || slices.Contains(d.Status.ActiveFlags, "waitingOnUserInput") {
			t.status = proto.StatusBlocked
		}
	}
	for _, turn := range d.Turns {
		if turn.Status == "inProgress" && turn.ID != t.ended && t.turn == "" {
			t.turn = turn.ID
		}
	}
	t.revision++
	c.threads[src.thread] = t
	c.mu.Unlock()
	closeReady()
	c.n.codices.mu.Lock()
	if c.n.codices.config.Joined == nil {
		c.n.codices.config.Joined = map[string]string{}
	}
	c.n.codices.config.Joined[id] = c.spec.ID
	c.n.codices.mu.Unlock()
	c.n.rememberCodex()
	c.n.notifyCodex()
	return nil
}

func (c *codexRemote) join(ctx context.Context, id string, remember bool) error {
	defer c.joins.lock(id)()
	owner, t, err := c.n.codexOwner(id)
	if err != nil || owner != c {
		return refusal("codex_read_only", "无法确定原 app-server，未接入", nil)
	}
	if t.parent != "" && remember {
		return refusal("codex_read_only", "这个子 Agent 由父 Agent 驱动", nil)
	}
	if t.joined {
		return nil
	}
	loadedSet, err := c.loadedShared(ctx, remember)
	if err != nil || !loadedSet[t.id] {
		return refusal("codex_read_only", "会话已不在原 app-server 中运行，未恢复或新建会话", nil)
	}
	// Subscribe to the loaded thread with its existing runtime configuration.
	var reply struct {
		Thread      remoteThreadData `json:"thread"`
		Model       string           `json:"model"`
		Effort      string           `json:"reasoningEffort"`
		ServiceTier json.RawMessage  `json:"serviceTier"`
	}
	c.mu.Lock()
	joining := c.threads[t.id]
	joining.joined = true
	joining.revision++
	c.threads[t.id] = joining
	c.mu.Unlock()
	if err = c.wire.call(ctx, "thread/resume", map[string]any{"threadId": t.id, "excludeTurns": true}, &reply); err != nil {
		c.mu.Lock()
		joining = c.threads[t.id]
		joining.joined = false
		joining.revision++
		c.threads[t.id] = joining
		for key, ask := range c.asks {
			if ask.thread == t.id {
				delete(c.asks, key)
				ask.cancel()
			}
		}
		c.mu.Unlock()
		return err
	}
	if reply.Thread.ID != t.id {
		return errors.New("app-server 接入返回了不同的会话")
	}
	c.mu.Lock()
	current, ok := c.threads[t.id]
	if ok {
		current.joined = true
		if tier := codexServiceTier(reply.ServiceTier); tier != "" && current.serviceTier == "" {
			current.serviceTier = tier
			current.serviceTierRevision++
		}
		current.revision++
		if reply.Thread.Path != "" {
			current.path = reply.Thread.Path
		}
		if m := firstNonEmpty(reply.Model, reply.Thread.Model); m != "" {
			current.model, current.effort = m, firstNonEmpty(reply.Effort, reply.Thread.Effort)
		}
		for _, turn := range reply.Thread.Turns {
			if turn.Status == "inProgress" && turn.ID != current.ended {
				current.turn = turn.ID
			}
		}
		c.threads[t.id] = current
	}
	c.mu.Unlock()
	if remember && ok {
		c.n.codices.mu.Lock()
		c.n.codices.config.Joined[id] = c.spec.ID
		c.n.codices.mu.Unlock()
		c.n.rememberCodex()
	}
	c.n.notifyCodex()
	return nil
}

func (n *Node) attachCodex(ctx context.Context, id string) error {
	defer n.typing.lock(id)()
	c, _, err := n.codexOwner(id)
	if err != nil {
		return refusal("codex_read_only", err.Error(), nil)
	}
	return c.join(ctx, id, true)
}

func (n *Node) sendCodex(ctx context.Context, id, text string, images []proto.Image) error {
	selected, err := n.sessionSelectedSkills(ctx, id, text)
	if err != nil {
		return err
	}
	c, t, err := n.codexOwner(id)
	if errors.Is(err, errCodexNoOwner) {
		if src, ok := n.codexSource(id); ok && codexDesktopChat(src) {
			return n.sendCodexDesktop(ctx, src, text, images)
		}
	}
	if err != nil {
		return refusal("codex_read_only", firstNonEmpty(err.Error(), "仅查看"), nil)
	}
	if err = c.join(ctx, id, true); err != nil {
		if conflict := codexWriterConflict(err); conflict != nil {
			return conflict
		}
		return refusal("codex_read_only", err.Error(), nil)
	}
	if codexCompactCommand(text, images) { // Codex's own command, not words for the model (codex_remote_compact.go)
		return n.compactCodex(ctx, id)
	}
	if len(selected) > 0 {
		selected, err = n.sessionSelectedSkills(ctx, id, text)
		if err != nil {
			return err
		}
	}
	paths, err := n.saveImages(images)
	if err != nil {
		return refusal("bad_image", err.Error(), nil)
	}
	input := []map[string]any{}
	for _, skill := range selected {
		input = append(input, map[string]any{"type": "skill", "name": skill.Name, "path": skill.Path})
	}
	if text != "" {
		input = append(input, map[string]any{"type": "text", "text": text, "text_elements": []any{}})
	}
	for _, p := range paths {
		input = append(input, map[string]any{"type": "localImage", "path": p})
	}
	previous := c
	c, t, err = n.codexOwner(id)
	if err != nil {
		return refusal("codex_read_only", err.Error(), nil)
	}
	if len(selected) > 0 && c != previous {
		return skillSelectionError(assistantskills.ErrReference)
	}
	params := map[string]any{"threadId": t.id, "input": input}
	method := "turn/start"
	if t.status == proto.StatusWorking || t.status == proto.StatusBlocked {
		if _, a2a := ctx.Value(a2aIdleKey{}).(bool); a2a {
			return a2aBusy()
		}
		clientID, _ := ctx.Value(sendTraceKey{}).(string)
		if clientID == "" {
			clientID = newUUID()
		}
		queueParams := map[string]any{"threadId": t.id, "input": input, "clientUserMessageId": clientID}
		var ack struct {
			QueuedSubmission struct {
				ID string `json:"id"`
			} `json:"queuedSubmission"`
		}
		queueErr := c.wire.call(ctx, "thread/queue/add", queueParams, &ack)
		if queueErr == nil && ack.QueuedSubmission.ID != "" {
			markSendQueued(ctx)
			n.notifyCodex()
			return nil
		}
		var rejected *codexError
		if !errors.As(queueErr, &rejected) {
			// Without an explicit rejection, steering could deliver the same message twice.
			if queueErr == nil {
				queueErr = errors.New("Codex returned no queued message receipt")
			}
			return uncertain(queueErr)
		}
		if t.turn == "" {
			t.turn = c.activeTurn(ctx, t.id) // a turn already running when Persona joined (codex_daemon.go)
		}
		if t.turn == "" {
			return refusal("codex_busy", "这个轮次仍在运行，但尚未收到轮次标识：稍后再发", nil)
		}
		method = "turn/steer"
		params["expectedTurnId"] = t.turn
	}
	// Model and/or effort the phone chose that its app-server could not take on its own (codex_effort.go): a turn
	// takes them for itself and the turns after; a steer cannot, so they wait for the next turn.
	var pModel, pEffort string
	if method == "turn/start" {
		pModel, pEffort = n.codexPendingTurn(id, c, t)
		if pModel != "" {
			params["model"] = pModel
		}
		if pEffort != "" {
			params["effort"] = pEffort
		}
	}
	var reply struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	err = c.wire.call(ctx, method, params, &reply)
	var re *codexError
	if errors.As(err, &re) && (pModel != "" || pEffort != "") {
		// Codex refused a turn that carried the choice: drop it, so it does not fail every message after this one.
		n.dropCodexPending(id, c.generation, t.id)
	}
	if conflict := codexWriterConflict(err); conflict != nil {
		return conflict
	}
	if errors.As(err, &re) {
		if re.Code == -32601 || re.Code == -32602 {
			return refusal("codex_refused", re.Error(), nil)
		}
		return uncertain(re)
	}
	if err != nil {
		return uncertain(err)
	}
	if pModel != "" || pEffort != "" {
		n.codexTurnSettingsTaken(id, c, t.id, pModel, pEffort)
	}
	if reply.Turn.ID != "" {
		c.mu.Lock()
		t = c.threads[t.id]
		t.turn = reply.Turn.ID
		t.status = proto.StatusWorking
		t.revision++
		c.threads[t.id] = t
		c.mu.Unlock()
	}
	n.notifyCodex()
	return nil
}

// activeTurn reads the thread's running turn once (it began before this connection joined, so no turn/started told
// it), and keeps it; "" when none runs or it cannot be read.
func (c *codexRemote) activeTurn(ctx context.Context, thread string) string {
	var reply struct {
		Thread remoteThreadData `json:"thread"`
	}
	if c.wire.call(ctx, "thread/read", map[string]any{"threadId": thread, "includeTurns": true}, &reply) != nil {
		return ""
	}
	turn := ""
	for _, t := range reply.Thread.Turns {
		if t.Status == "inProgress" {
			turn = t.ID
		}
	}
	if turn != "" {
		c.mu.Lock()
		if t, ok := c.threads[thread]; ok && turn == t.ended {
			turn = "" // the read is older than its end
		} else if ok && t.turn == "" {
			t.turn = turn
			t.revision++
			c.threads[thread] = t
		}
		c.mu.Unlock()
	}
	return turn
}

func (n *Node) interruptCodex(ctx context.Context, id string) error {
	defer n.typing.lock(id)()
	c, t, err := n.codexOwner(id)
	if errors.Is(err, errCodexNoOwner) {
		if src, ok := n.codexSource(id); ok && codexDesktopChat(src) {
			return n.interruptCodexDesktop(ctx, src)
		}
	}
	if err != nil {
		return refusal("codex_read_only", err.Error(), nil)
	}
	if t.turn == "" && t.parent == "" {
		t.turn = c.activeTurn(ctx, t.id) // a turn already running when Persona joined (codex_daemon.go)
	}
	if t.turn == "" || t.parent != "" {
		return refusal("codex_read_only", "没有可停止的已观察轮次", nil)
	}
	return c.wire.call(ctx, "turn/interrupt", map[string]any{"threadId": t.id, "turnId": t.turn}, nil)
}

// interruptCodexActiveTurn interrupts a running app-server turn without acquiring n.typing.lock, for callers like
// quitAgent that already hold it; returns true when turn/interrupt was sent (#12389).
func (n *Node) interruptCodexActiveTurn(ctx context.Context, id string) bool {
	c, t, err := n.codexOwner(id)
	if err != nil {
		return false
	}
	c.mu.Lock()
	tid, turn, parent := t.id, t.turn, t.parent
	c.mu.Unlock()
	if tid == "" || parent != "" {
		return false
	}
	ictx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if turn == "" {
		turn = c.activeTurn(ictx, tid)
	}
	if turn == "" {
		return false
	}
	if c.wire.call(ictx, "turn/interrupt", map[string]any{"threadId": tid, "turnId": turn}, nil) != nil {
		return false
	}
	for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline) && ctx.Err() == nil; {
		c.mu.Lock()
		idle := c.threads[tid].turn == ""
		c.mu.Unlock()
		if idle {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

func (c *codexRemote) message(m codexMessage) {
	if len(m.ID) > 0 {
		c.request(m)
		return
	}
	c.delivery.Lock()
	defer c.delivery.Unlock()
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	if !ready {
		return
	}
	var ev struct {
		ThreadID  string          `json:"threadId"`
		TurnID    string          `json:"turnId"`
		Delta     string          `json:"delta"`
		RequestID json.RawMessage `json:"requestId"`
		Turn      struct {
			ID     string          `json:"id"`
			Status string          `json:"status"`
			Error  json.RawMessage `json:"error"`
		} `json:"turn"`
		Item     remoteEventItem `json:"item"`
		Settings struct {
			Model       string          `json:"model"`
			Effort      string          `json:"effort"`
			ServiceTier json.RawMessage `json:"serviceTier"`
		} `json:"threadSettings"`
		Status     remoteThreadStatus              `json:"status"`
		ThreadName *string                         `json:"threadName"`
		Plan       []struct{ Step, Status string } `json:"plan"`
	}
	if json.Unmarshal(m.Params, &ev) != nil {
		return
	}
	if m.Method == "serverRequest/resolved" {
		c.mu.Lock()
		c.finished[codexRequestKey(ev.RequestID)] = true
		a := c.asks[codexRequestKey(ev.RequestID)]
		if a != nil && a.thread == ev.ThreadID {
			delete(c.asks, codexRequestKey(ev.RequestID))
		}
		c.mu.Unlock()
		if a != nil && a.thread == ev.ThreadID {
			if c.n.peer != nil {
				_ = c.n.peer.Notify("interaction.dismiss", map[string]any{"id": a.interaction})
			}
			a.cancel()
		}
		return
	}
	c.mu.Lock()
	t, ok := c.threads[ev.ThreadID]
	if !ok {
		c.mu.Unlock()
		return
	}
	delta := ""
	emit := false
	refresh := false
	switch m.Method {
	case "thread/status/changed":
		t.status = ev.Status.sessionStatus()
		refresh = true
	case "thread/closed":
		t.ended = firstNonEmpty(t.turn, t.ended)
		t.status, t.turn, t.partial, t.buffer = proto.StatusDone, "", "", nil
		emit, refresh = true, true
	case "thread/name/updated":
		if ev.ThreadName != nil && *ev.ThreadName != "" {
			t.title = *ev.ThreadName
		}
		refresh = true
	case "thread/queue/changed", "turn/plan/updated":
		refresh = true
	case "thread/reverted":
		invalidateCodexPaginated(c.spec.Home)
		t.partial, t.failure, t.failureKind = "", "", ""
		t.buffer = nil
		emit, refresh = true, true
	case "turn/started":
		t.turn = ev.Turn.ID
		t.status = proto.StatusWorking
		t.failure, t.failureKind = "", ""
		t.partial = ""
		t.buffer = nil
		emit = true
	case "turn/completed":
		// A turn that began before Persona joined (a resume started with its message, #5598) never had its
		// turn/started here: with no turn known, this is its end, or its live text would stay on the hub.
		if t.turn == ev.Turn.ID || (t.turn == "" && ev.Turn.ID != t.ended) {
			t.turn = ""
			t.ended = ev.Turn.ID
			t.status = proto.StatusDone
			t.failure, t.failureKind = "", ""
			if ev.Turn.Status == "failed" { // interrupted is the user's Esc, no failure
				if t.failure, t.failureKind = transcript.CodexTurnError(ev.Turn.Error); t.failure == "" {
					t.failure = transcript.CodexTurnFailed // failed with no error to say why
				}
			}
			t.partial = ""
			t.buffer = nil
			emit = true
			refresh = true
		}
	case "item/started":
		if ev.Item.Type == "fileChange" {
			var paths []string
			for _, ch := range ev.Item.Changes {
				paths = append(paths, ch.Path)
			}
			t.files[ev.Item.ID] = paths
		}
		if ev.Item.Type == "agentMessage" || ev.Item.Type == "plan" {
			t.partial = ""
			t.buffer = nil
			emit = true
			refresh = true
		}
	case "item/agentMessage/delta", "item/plan/delta":
		if t.buffer == nil {
			t.buffer = &strings.Builder{}
		}
		t.buffer.WriteString(ev.Delta)
		t.partial = t.buffer.String()
		delta = t.partial
		emit = time.Since(t.lastDelta) >= 80*time.Millisecond
		if emit {
			t.lastDelta = time.Now()
		}
	case "thread/settings/updated": // the thread's settings changed: on the computer, or from here (codex_effort.go)
		t.model, t.effort = firstNonEmpty(ev.Settings.Model, t.model), ev.Settings.Effort
		if tier := codexServiceTier(ev.Settings.ServiceTier); tier != "" {
			t.serviceTier = tier
			t.serviceTierRevision++
		}
	case "item/completed":
		delete(t.files, ev.Item.ID)
		refresh = true
		if ev.Item.Type == "agentMessage" || ev.Item.Type == "plan" {
			t.partial = ev.Item.Text
			t.buffer = nil
			delta = t.partial
			emit = true
		}
	}
	t.revision++
	c.threads[ev.ThreadID] = t
	c.mu.Unlock()
	if refresh {
		go func() {
			if m.Method == "thread/reverted" {
				_ = c.refresh()
			}
			if f, err := c.n.file(t.publicID); err == nil {
				c.n.refresh(t.publicID, f)
			}
		}()
	}
	if emit {
		c.n.emitDelta(t.publicID, delta)
	}
	switch m.Method {
	case "turn/started", "turn/completed", "thread/settings/updated", "thread/reverted",
		"thread/status/changed", "thread/closed", "thread/name/updated", "thread/queue/changed", "item/plan/delta", "turn/plan/updated":
		c.n.notifyCodex()
	}
}

func remoteInteractionID(machine, instance, generation, thread string, id json.RawMessage) string {
	h := sha256.Sum256([]byte(machine + "\x00" + instance + "\x00" + generation + "\x00" + thread + "\x00" + codexRequestKey(id)))
	return "codex-" + hex.EncodeToString(h[:])
}
