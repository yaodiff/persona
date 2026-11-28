package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

type codexRemoteDiag string

const (
	codexDiagNone            codexRemoteDiag = ""
	codexDiagSocketGone      codexRemoteDiag = "socket_gone"
	codexDiagEndpointInvalid codexRemoteDiag = "endpoint_invalid"
	codexDiagUnreachable     codexRemoteDiag = "unreachable"
	codexDiagHomeMismatch    codexRemoteDiag = "home_mismatch"
	codexDiagHandshakeFailed codexRemoteDiag = "handshake_failed"
)

type codexRegistry struct {
	mu                sync.Mutex
	index             codexHomeIndex
	paneReads         codexPaneReads
	lastRows          map[string]proto.Session // immutable last observations, reused when their home is inaccessible
	listSeq, savedSeq uint64
	config            proto.CodexConfig
	claudeIDs         map[string]bool
	liveRollouts      map[string]string          // rollout id → what holds it open (codexHolder)
	liveEndpoints     map[string]string          // rollout id → resolved shared endpoint when held by a --listen app-server
	liveCanQuit       map[string]bool            // rollout id → standalone CLI in a safe-quit terminal
	stdioHomes        map[string]bool            // canonical home → a stdio app-server currently runs there
	desktopHomes      map[string]bool            // canonical Codex home → desktop App process running
	remoteDiags       map[string]codexRemoteDiag // resolved endpoint → last connection failure kind
	byPane            map[string]string
	byPanePID         map[string]int
	threads           map[string]codexSource
	desktopAsks       map[string]codexSource
	remotes           map[string]*codexRemote
	retries           map[string]codexRetry // server id -> attempts that did not connect (codex_backoff.go)
	report            func(proto.CodexConfig)
	// handed: the hub handed this connection the inventory it keeps for the computer (node.configure's codex, every
	// connection: devices.connected). Until then the registry is a new Node's, without the threads the user joined, the
	// homes and the app-servers the hub kept, and the hub would store it over what it kept: reports wait for it
	// (rememberCodex), and held says one did.
	handed, held bool
	kick         chan struct{}
	scan         func() []codexProcess
	// checked lists the processes as scan does, and says whether it could (scanCodexProcessesChecked); nil: scan's
	// list, taken as complete.
	checked func() ([]codexProcess, bool)
	// claimed: server id -> the runtime that claims the instance, which the node keeps no connection to
	// (codex_instance_claims.go); kept per Node, so a new hub connection logs each claim once more. attach serializes
	// checking the claims with attaching, so an instance a runtime claims meanwhile is not dialled after the claim
	// took it.
	claimed map[string]string
	attach  sync.Mutex
}

type codexSource struct{ home, thread, path string }

func newCodexRegistry() *codexRegistry {
	return &codexRegistry{
		threads:     map[string]codexSource{},
		remotes:     map[string]*codexRemote{},
		remoteDiags: map[string]codexRemoteDiag{},
		scan:        scanCodexProcesses,
		checked:     scanCodexProcessesChecked,
		kick:        make(chan struct{}, 1),
	}
}

func (r *codexRegistry) setRemoteDiag(endpoint string, diag codexRemoteDiag) {
	if r == nil || endpoint == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.remoteDiags == nil {
		r.remoteDiags = map[string]codexRemoteDiag{}
	}
	if diag == codexDiagNone {
		delete(r.remoteDiags, endpoint)
		return
	}
	r.remoteDiags[endpoint] = diag
}

// codexControlEndpoint is the default Unix control socket endpoint for home.
func codexControlEndpoint(home string) string {
	if home == "" {
		return ""
	}
	return "unix://" + filepath.Join(home, "app-server-control", "app-server-control.sock")
}

// codexResolvedEndpoint normalizes a process's --listen endpoint for home; returns "" for standalone CLIs,
// stdio app-servers, or disabled listeners.
func codexResolvedEndpoint(home, endpoint string) string {
	if endpoint == "" || endpoint == "stdio://" || endpoint == "off" {
		return ""
	}
	if endpoint == "unix://" {
		return codexControlEndpoint(home)
	}
	return endpoint
}

// canonicalCodexHome is home absolute, with its links resolved as far as it exists: a home that does not exist yet
// (Codex not used yet when the node starts) canonicalizes the same as it will once it does, so LegacyHome, fixed on
// the first configure, still equals that home in Homes later under a linked ancestor (macOS's /var, a linked /home).
func canonicalCodexHome(home, base string) string {
	p := proto.ExpandHome(home, base)
	if !filepath.IsAbs(p) {
		return ""
	}
	return realPath(filepath.Clean(p))
}

func codexSourceID(home, raw, legacy string) string {
	if home == legacy {
		return raw
	}
	h := sha256.Sum256([]byte(compat.CodexHashPrefix() + home + "\x00" + raw))
	// The reserved shape cannot collide with a raw UUID and still satisfies validID.
	return "c0de-" + hex.EncodeToString(h[:16])
}

// matchesSourceID reports whether id is the source's registry key: the raw thread ID in the legacy home or an opaque key in other homes.
func (r *codexRegistry) matchesSourceID(id string, src codexSource) bool {
	if r == nil || src.thread == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return codexSourceID(src.home, src.thread, r.config.LegacyHome) == id
}

func (n *Node) configureCodex(c proto.CodexConfig) {
	if n.sessionScope != nil {
		discovery := false
		c = proto.CodexConfig{Homes: []string{n.sessionScope.codex}, LegacyHome: n.sessionScope.codex, Discovery: &discovery}
	}
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range c.BlockedIDs {
		if validID(id) && !slices.Contains(r.config.BlockedIDs, id) {
			r.config.BlockedIDs = append(r.config.BlockedIDs, id)
		}
	}
	if c.Discovery != nil {
		v := *c.Discovery
		r.config.Discovery = &v
	}
	if c.LegacyHome != "" && len(r.threads) == 0 {
		r.config.LegacyHome = canonicalCodexHome(c.LegacyHome, n.home)
	}
	if r.config.LegacyHome == "" {
		r.config.LegacyHome = canonicalCodexHome(firstNonEmpty(c.LegacyHome, n.codexHome()), n.home)
	}
	for _, home := range append(c.Homes, n.codexHome()) {
		p := canonicalCodexHome(home, n.home)
		if p != "" && !slices.Contains(r.config.Homes, p) {
			r.config.Homes = append(r.config.Homes, p)
		}
	}
	for _, s := range c.Servers {
		s.Home = canonicalCodexHome(s.Home, n.home)
		if s.ID == "" || s.Home == "" || s.Endpoint == "" {
			continue
		}
		i := slices.IndexFunc(r.config.Servers, func(v proto.CodexServer) bool { return v.ID == s.ID })
		if i >= 0 {
			r.config.Servers[i] = s
		} else {
			r.config.Servers = append(r.config.Servers, s)
		}
		if !slices.Contains(r.config.Homes, s.Home) {
			r.config.Homes = append(r.config.Homes, s.Home)
		}
	}
	if r.config.Joined == nil {
		r.config.Joined = map[string]string{}
	}
	for id, instance := range c.Joined {
		if validID(id) {
			r.config.Joined[id] = instance
		}
	}
}

func cloneCodexConfig(c proto.CodexConfig) proto.CodexConfig {
	c.BlockedIDs = slices.Clone(c.BlockedIDs)
	c.Homes = slices.Clone(c.Homes)
	c.Servers = slices.Clone(c.Servers)
	joined := map[string]string{}
	for k, v := range c.Joined {
		joined[k] = v
	}
	c.Joined = joined
	return c
}

func (n *Node) codexInventory() proto.CodexConfig {
	if n.codices == nil {
		return proto.CodexConfig{}
	}
	n.codices.mu.Lock()
	defer n.codices.mu.Unlock()
	return cloneCodexConfig(n.codices.config)
}

// rememberCodex reports the inventory for the hub to keep, once the hub handed this connection what it kept
// (codexHanded); before that it only notes that a report waits.
func (n *Node) rememberCodex() {
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.handed {
		r.held = true
		r.mu.Unlock()
		return
	}
	cfg, report := cloneCodexConfig(r.config), r.report
	r.mu.Unlock()
	if report != nil {
		report(cfg)
	}
}

// codexHanded: node.configure handed the inventory the hub keeps (configureCodex merged it). A report that waited for
// it goes now, with what the node found meanwhile added to the hub's.
func (n *Node) codexHanded() {
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	held := r.held && !r.handed
	r.handed, r.held = true, false
	r.mu.Unlock()
	if held {
		n.rememberCodex()
	}
}

func (n *Node) registerCodex(home, thread, path string) string {
	if n.codices == nil || !validID(thread) {
		return ""
	}
	return n.registerCanonicalCodex(canonicalCodexHome(home, n.home), thread, path)
}

// registerCanonicalCodex is registerCodex for a home canonicalCodexHome already resolved: the loops that register every
// rollout of a home resolve it once, not once per rollout (each resolution walks the home's path with a stat per part).
func (n *Node) registerCanonicalCodex(home, thread, path string) string {
	r := n.codices
	if r == nil || !validID(thread) {
		return ""
	}
	if n.sessionScope != nil && !sameScopePath(home, n.sessionScope.codex) {
		return ""
	}
	if home == "" || !n.scopedTranscript(path) && path != "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := codexSourceID(home, thread, r.config.LegacyHome)
	old := r.threads[id]
	if path == "" {
		path = old.path
	} else if old.path != "" && old.path != path {
		// A process may still hold an older segment. Do not replace the current transcript with it.
		if previous, err := os.Stat(old.path); err == nil {
			if next, err := os.Stat(path); err == nil && (previous.ModTime().After(next.ModTime()) || previous.ModTime().Equal(next.ModTime()) && old.path > path) {
				path = old.path
			}
		}
	}
	r.threads[id] = codexSource{home, thread, path}
	return id
}

func (n *Node) codexSource(id string) (codexSource, bool) {
	r := n.codices
	if r == nil {
		return codexSource{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.threads[id]
	return s, ok
}

func (n *Node) externalCodex(id string) bool {
	if n.codexBlocked(id) {
		return true
	}
	if n.isAssistantSession(id) {
		return false
	}
	if a, live := n.agent(id); live && a.Agent == "codex" {
		return false
	}
	if _, ok := n.codexSource(id); ok {
		return true
	}
	// An old opaque key must never fall through to Claude, even before inventory finishes loading.
	return strings.HasPrefix(id, "c0de-")
}

func (n *Node) discoverCodex() {
	if n.sessionScope != nil {
		return
	}
	r := n.codices
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.config.Discovery != nil && !*r.config.Discovery {
		r.mu.Unlock()
		return
	}
	scan := r.scan
	r.mu.Unlock()
	// The TUI's environment identifies the pane's home before any rollout or
	// unrelated transcript directory is opened. An inherited pane variable on
	// an app-server subprocess must not override its terminal client.
	found := scan()
	byPane := map[string]string{}
	byPanePID := map[string]int{}
	for _, p := range found {
		if n.herdrSessionBlocked(p.herdrSession) {
			continue
		}
		home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
		if home == "" {
			continue
		}
		var pane string
		switch {
		case p.pane != "":
			if n.hosts != nil {
				pane = n.herdrPaneIn(p.pane, p.herdrSession)
			} else {
				pane = p.pane
			}
		case p.herdrCore != "":
			pane = host.PaneRef{Kind: host.HerdrCore, ID: p.herdrCore}.String()
		case p.ternPane != "":
			pane = host.PaneRef{Kind: host.Tern, ID: p.ternPane}.String()
		}
		if pane != "" && (p.endpoint == "" || byPane[pane] == "") {
			byPane[pane] = home
		}
		if p.endpoint == "" && p.pid > 0 {
			pidPane := pane
			if pidPane == "" && p.orcaPane != "" {
				pidPane = host.PaneRef{Kind: host.Orca, ID: p.orcaPane}.String()
			}
			if pidPane != "" {
				byPanePID[pidPane] = p.pid
			}
		}
	}
	r.mu.Lock()
	r.byPane = byPane
	r.byPanePID = byPanePID
	r.mu.Unlock()
	claudeIDs := map[string]bool{}
	for _, path := range n.globProjects("*", "*.jsonl") {
		claudeIDs[strings.TrimSuffix(filepath.Base(path), ".jsonl")] = true
	}
	r.mu.Lock()
	r.claudeIDs = claudeIDs
	r.mu.Unlock()
	changed := false
	liveRollouts := map[string]string{}
	liveEndpoints := map[string]string{}
	liveCanQuit := map[string]bool{}
	stdioHomes := map[string]bool{}
	for _, p := range found {
		if n.herdrSessionBlocked(p.herdrSession) {
			continue
		}
		home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
		if home != "" && p.endpoint == "stdio://" {
			stdioHomes[home] = true
		}
	}
	for pid, paths := range openCodexRollouts(found) {
		for _, p := range found {
			if p.pid != pid {
				continue
			}
			holder := codexHolderCLI
			if p.endpoint != "" {
				holder = codexHolderAppServer
			}
			home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
			sharedEndpoint := codexResolvedEndpoint(home, p.endpoint)
			canQuit := p.endpoint == "" && runtime.GOOS != "windows" && p.hasTakeoverTerminal()
			for _, path := range paths {
				if !pathWithin(path, home) { // Windows paths do not tell case apart (codex_desktop.go)
					continue
				}
				if thread := rolloutThreadID(home, path); thread != "" {
					if n.herdrSessionBlocked(p.herdrSession) {
						id := n.codexPublicID(home, thread)
						if !n.isAssistantSession(id) && !n.keptSessions()[id] {
							n.noteHerdrSessionID(id, p.herdrSession)
							continue
						}
					}
					id := n.registerCodex(home, thread, path)
					liveRollouts[id] = holder
					if sharedEndpoint != "" {
						liveEndpoints[id] = sharedEndpoint
					}
					if prev, exists := liveCanQuit[id]; !exists {
						liveCanQuit[id] = canQuit
					} else {
						liveCanQuit[id] = prev && canQuit
					}
				}
			}
		}
	}
	r.mu.Lock()
	r.liveRollouts = liveRollouts
	r.liveEndpoints = liveEndpoints
	r.liveCanQuit = liveCanQuit
	r.stdioHomes = stdioHomes
	r.mu.Unlock()
	desktopHomes := map[string]bool{}
	for _, p := range found {
		if n.herdrSessionBlocked(p.herdrSession) {
			continue
		}
		home := canonicalCodexHome(firstNonEmpty(p.home, n.codexHome()), n.home)
		if home == "" {
			continue
		}
		if p.desktop {
			desktopHomes[home] = true
		}
		r.mu.Lock()
		if !slices.Contains(r.config.Homes, home) {
			r.config.Homes = append(r.config.Homes, home)
			changed = true
		}
		if endpoint := codexResolvedEndpoint(home, p.endpoint); endpoint != "" {
			sum := sha256.Sum256([]byte(home + "\x00" + endpoint))
			id := "discovered-" + hex.EncodeToString(sum[:12])
			if !slices.ContainsFunc(r.config.Servers, func(s proto.CodexServer) bool { return s.Home == home && s.Endpoint == endpoint }) {
				r.config.Servers = append(r.config.Servers, proto.CodexServer{ID: id, Home: home, Endpoint: endpoint})
				changed = true
			}
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.desktopHomes = desktopHomes
	r.mu.Unlock()
	cfg := n.codexInventory()
	// Existing official control sockets are discoverable without starting a daemon.
	for _, home := range cfg.Homes {
		socket := filepath.Join(home, "app-server-control", "app-server-control.sock")
		// 0.163's socket is an alias to one in a shared private directory (codexSocketPath).
		if p, err := codexSocketPath(socket); err != nil {
			continue
		} else if st, err := os.Lstat(p); err == nil && st.Mode()&os.ModeSocket != 0 {
			endpoint := "unix://" + socket
			r.mu.Lock()
			if !slices.ContainsFunc(r.config.Servers, func(s proto.CodexServer) bool { return s.Endpoint == endpoint }) {
				sum := sha256.Sum256([]byte(home + "\x00" + endpoint))
				r.config.Servers = append(r.config.Servers, proto.CodexServer{ID: "discovered-" + hex.EncodeToString(sum[:12]), Home: home, Endpoint: endpoint})
				changed = true
			}
			r.mu.Unlock()
		}
	}
	if changed {
		n.rememberCodex()
	}
}

func (n *Node) runCodex(ctx context.Context) {
	select {
	case <-n.readyCh():
	case <-time.After(configWait):
	case <-ctx.Done():
		return
	}
	n.configureCodex(proto.CodexConfig{})
	go n.publishCodex(ctx)
	n.rememberCodex()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	scanAt := time.Time{}
	for {
		// Also scan right after the Claude scan took a newer process snapshot, so both share one walk of /proc.
		if time.Since(scanAt) >= 30*time.Second || procSnapshotAfter(scanAt) {
			n.discoverCodex()
			scanAt = time.Now()
		}
		cfg := n.codexInventory()
		deadline := time.Now().Add(n.codexHomeListBudget())
		for _, home := range cfg.Homes {
			n.indexCodexHome(home, deadline)
		}
		n.attachCodexServers(ctx, cfg.Servers) // but not to the ones a runtime claims (codex_instance_claims.go)
		select {
		case <-ctx.Done():
			n.codices.mu.Lock()
			remotes := slices.Collect(mapRemoteValues(n.codices.remotes))
			n.codices.mu.Unlock()
			for _, c := range remotes {
				c.close()
			}
			return
		case <-tick.C:
		}
	}
}

// registerRollouts registers the rollouts rollouts(home) lists, with home resolved once for all of them.
func (n *Node) registerRollouts(home string) {
	list := rollouts(home)
	if len(list) == 0 {
		return
	}
	canonical := canonicalCodexHome(home, n.home)
	for _, f := range list {
		n.registerCanonicalCodex(canonical, f.id, f.path)
	}
}

func mapRemoteValues(m map[string]*codexRemote) func(func(*codexRemote) bool) {
	return func(yield func(*codexRemote) bool) {
		for _, v := range m {
			if !yield(v) {
				return
			}
		}
	}
}

// What holds a rollout open, as the read-only reason names it.
const (
	codexHolderAppServer = "app-server"
	codexHolderCLI       = "Codex"
)

// codexHeldIdle: a rollout held open by a process node cannot join, untouched this long, is history (#2425). A bridge
// that keeps stdio app-servers for months holds the threads they once loaded: they showed "Unread" all along.
const codexHeldIdle = 24 * time.Hour

// codexHeldWorkingStale: a held rollout with an open turn that has seen no new write or activity this long stops
// reporting StatusWorking so runaway timers do not climb for hours when an external CLI waits on a prompt or stalls.
const codexHeldWorkingStale = 20 * time.Minute

// heldRecently: the rollout at path was written within codexHeldIdle (its last line's time or its mtime, the later).
func heldRecently(m quickMeta, path string, pass *codexListPass) bool {
	st, err := pass.stat(path)
	if err != nil {
		return false
	}
	at := st.ModTime()
	if m.activity.After(at) {
		at = m.activity
	}
	return time.Since(at) < codexHeldIdle
}

// heldTurnStalled reports whether a held rollout's open turn has gone without new writes or activity for at least
// codexHeldWorkingStale.
func heldTurnStalled(m quickMeta, path string, pass *codexListPass) bool {
	var at time.Time
	if st, err := pass.stat(path); err == nil {
		at = st.ModTime()
	}
	if m.turnActivityAt.After(at) {
		at = m.turnActivityAt
	}
	if m.contentAt.After(at) {
		at = m.contentAt
	}
	return !at.IsZero() && time.Since(at) >= codexHeldWorkingStale
}

// codexSharedReadOnlyReason explains why a rollout held by a shared --listen app-server could not be joined live.
func (n *Node) codexSharedReadOnlyReason(home, endpoint string) string {
	if endpoint == codexControlEndpoint(home) && codexDaemonSocketGone(home) != nil {
		return i18n.T(i18n.Default, "codex-history.shared_socket_gone")
	}
	diag := codexDiagNone
	var matched *codexRemote
	if n.codices != nil {
		n.codices.mu.Lock()
		diag = n.codices.remoteDiags[endpoint]
		for _, c := range n.codices.remotes {
			if c.spec.Endpoint == endpoint {
				matched = c
				break
			}
		}
		n.codices.mu.Unlock()
	}
	ready := false
	if matched != nil {
		matched.mu.Lock()
		ready = matched.ready
		matched.mu.Unlock()
	}
	switch diag {
	case codexDiagSocketGone:
		return i18n.T(i18n.Default, "codex-history.shared_socket_gone")
	case codexDiagHomeMismatch:
		return i18n.T(i18n.Default, "codex-history.shared_home_mismatch")
	case codexDiagEndpointInvalid:
		return i18n.T(i18n.Default, "codex-history.shared_endpoint_invalid")
	case codexDiagUnreachable, codexDiagHandshakeFailed:
		return i18n.T(i18n.Default, "codex-history.shared_unreachable")
	}
	if ready {
		return i18n.T(i18n.Default, "codex-history.shared_not_loaded")
	}
	if _, _, err := validateCodexEndpoint(endpoint); err != nil {
		return i18n.T(i18n.Default, "codex-history.shared_endpoint_invalid")
	}
	return i18n.T(i18n.Default, "codex-history.shared_unreachable")
}

// codexListPass holds what one session-list rebuild (sessionList, codexSessions) looks up once instead of once per row.
// The list is rebuilt every second, and with a few hundred rollouts each thread resolved its directory's symlinks (a
// stat per part of the path, a readlink per link), stat'ed its rollout up to three times, looked for the Codex App's
// socket and rebuilt the assistant's project name with a regexp: about 19 stat calls per rollout per pass. Every Claude
// row's decoration (ultracodeRow) also resolved the data directory and rebuilt that name. It lives for one pass of one
// caller, so nothing in it is shared or needs invalidating. A nil pass looks everything up each time.
type codexListPass struct {
	unavailable      map[string]bool          // previously observed rows whose filesystem is unavailable on this pass
	unavailableHomes map[string]bool          // Codex homes whose index or rollout cache mutex was unavailable on this pass
	liveRows         map[string]proto.Session // healthy live Codex pane rows observed on this pass, for stall fallback
	real             map[string]string        // directory → realPath
	stats            map[string]codexStat     // rollout path → os.Stat
	desktop          map[string]bool          // Codex home → codexDesktopBusAvailable
	desktopRun       map[string]bool          // Codex home → desktop App process or bus running
	own              string                   // assistantProject, once ownSet
	ownSet           bool
	// runtimes is the runtime session each Codex thread is listed as (thread → session, runtimecodex.go): the list
	// has that session's row, so the thread's own is left out and its subagents hang off the session.
	runtimes map[string]codexClaim
	// paginated is codexPaginatedThreads by Codex home: each call globs the home for the App's databases and stats
	// them, which every row's codexAppHistoryPath did (#5970).
	paginated map[string]map[string]codexAppThread
	// apps is loadCodexAppState by Codex home: every App row asked for it twice, and each ask globbed the home for the
	// App's state database first, 1,100 globs a second with 550 rows.
	apps map[string]*codexAppState
	// looks is the missLook by Codex home that rolloutPathIn checks each thread with no rollout against.
	looks map[string]*missLook
	// agentKinds maps a session id listed in this pass to its row's agent kind. Transcript lookup uses it to skip a TRAE
	// probe for a row sessionListIn already knows belongs to another agent. Nil/untagged ids keep the probe.
	agentKinds map[string]string
}

func (p *codexListPass) markHomeUnavailable(home string) {
	if p == nil || home == "" {
		return
	}
	if p.unavailableHomes == nil {
		p.unavailableHomes = make(map[string]bool)
	}
	p.unavailableHomes[home] = true
}

func (p *codexListPass) homeUnavailable(home string) bool {
	return p != nil && home != "" && p.unavailableHomes[home]
}

// runtimeOf is the runtime session that names Codex thread as its conversation; the zero claim when none does.
func (p *codexListPass) runtimeOf(thread string) codexClaim {
	if p == nil || thread == "" {
		return codexClaim{}
	}
	return p.runtimes[thread]
}

type codexStat struct {
	info os.FileInfo
	err  error
}

func newCodexListPass() *codexListPass {
	return &codexListPass{real: map[string]string{}, stats: map[string]codexStat{}, desktop: map[string]bool{},
		desktopRun: map[string]bool{}, paginated: map[string]map[string]codexAppThread{}, apps: map[string]*codexAppState{},
		looks: map[string]*missLook{}}
}

func (p *codexListPass) realPath(dir string) string {
	if p == nil {
		return realPath(dir)
	}
	real, ok := p.real[dir]
	if !ok {
		real = realPath(dir)
		p.real[dir] = real
	}
	return real
}

func (p *codexListPass) stat(path string) (os.FileInfo, error) {
	if p == nil {
		return os.Stat(path)
	}
	st, ok := p.stats[path]
	if !ok {
		st.info, st.err = os.Stat(path)
		p.stats[path] = st
	}
	return st.info, st.err
}

func (p *codexListPass) desktopBusAvailable(home string) bool {
	if p == nil {
		return codexDesktopBusAvailable(home)
	}
	up, ok := p.desktop[home]
	if !ok {
		up = codexDesktopBusAvailable(home)
		p.desktop[home] = up
	}
	return up
}

func (p *codexListPass) desktopAvailable(home string) bool {
	if p.desktopBusAvailable(home) {
		return true
	}
	return codexDesktopQueueCLI(home) != ""
}

// desktopRunning reports whether the Codex App bus is live or discoverCodex saw a desktop App process for home.
func (p *codexListPass) desktopRunning(n *Node, home string) bool {
	if p.desktopAvailable(home) {
		return true
	}
	if n == nil || n.codices == nil {
		return false
	}
	if p != nil {
		if run, ok := p.desktopRun[home]; ok {
			return run
		}
	}
	n.codices.mu.Lock()
	run := n.codices.desktopHomes[canonicalCodexHome(home, n.home)]
	n.codices.mu.Unlock()
	if p != nil {
		if p.desktopRun == nil {
			p.desktopRun = map[string]bool{}
		}
		p.desktopRun[home] = run
	}
	return run
}

// appState is loadCodexAppState with each home's state looked up once per pass.
func (p *codexListPass) appState(home string) *codexAppState {
	if p == nil {
		return loadCodexAppState(home)
	}
	app, ok := p.apps[home]
	if !ok {
		app = loadCodexAppState(home)
		p.apps[home] = app
	}
	return app
}

// missLook is newMissLook with each home's look taken once per pass, and again once it is a second old or a listing of
// every directory began after it (keepMiss keeps nothing for such a look): a long pass, the first after the node
// starts, still keeps the misses it finds.
func (p *codexListPass) missLook(home string) *missLook {
	if p == nil {
		return newMissLook(home)
	}
	if p.homeUnavailable(home) {
		return nil
	}
	look := p.looks[home]
	relisted := false
	if look != nil && time.Since(look.at) <= time.Second {
		var ok bool
		relisted, ok = relistedSinceIn(home, look.at, p)
		if !ok {
			return nil
		}
	}
	if look == nil || time.Since(look.at) > time.Second || relisted {
		look = newMissLookIn(home, p)
		p.looks[home] = look
	}
	return look
}

// paginatedThreadOf is codexPaginatedThreadOf with each home's paginated threads looked up once per pass.
func (p *codexListPass) paginatedThreadOf(src codexSource) (codexAppThread, bool) {
	if p == nil {
		return codexPaginatedThreadOf(src)
	}
	threads, ok := p.paginated[src.home]
	if !ok {
		threads = codexPaginatedThreads(src.home)
		p.paginated[src.home] = threads
	}
	t, ok := threads[src.thread]
	return t, ok
}

// knownNonTraexAgent reports whether this pass's list row for id is an agent other than TRAE, so a fresh cached
// transcript path needs no TRAE database probe. A nil pass or an id the pass did not tag answers false and preserves
// the probe, keeping every caller without list agent knowledge on the original lookup order.
func (p *codexListPass) knownNonTraexAgent(id string) bool {
	if p == nil || p.agentKinds == nil {
		return false
	}
	kind := p.agentKinds[id]
	return kind != "" && kind != traexAgent
}

func (p *codexListPass) assistantProject(n *Node) string {
	if p == nil {
		return n.assistantProject()
	}
	if !p.ownSet {
		p.own, p.ownSet = n.assistantProject(), true
	}
	return p.own
}

func (n *Node) lastCodexRow(id string) (proto.Session, bool) {
	if n.codices == nil {
		return proto.Session{}, false
	}
	n.codices.mu.Lock()
	row, ok := n.codices.lastRows[id]
	n.codices.mu.Unlock()
	return row, ok
}

func (n *Node) codexSessions(seen map[string]bool) []proto.Session {
	return n.codexSessionsIn(seen, newCodexListPass())
}

// codexSessionsIn lists the Codex threads as offline rows, looking up through pass (nil: each time).
func (n *Node) codexSessionsIn(seen map[string]bool, pass *codexListPass) []proto.Session {
	if n.codices == nil {
		return nil
	}
	if pass == nil {
		pass = newCodexListPass()
	}
	cfg := n.codexInventory()
	deadline := time.Now().Add(n.codexHomeListBudget())
	for _, home := range cfg.Homes {
		if !n.indexCodexHome(home, deadline) {
			pass.markHomeUnavailable(home)
		}
	}
	n.codices.mu.Lock()
	n.codices.listSeq++
	seq, previous := n.codices.listSeq, n.codices.lastRows
	sources := make(map[string]codexSource, len(n.codices.threads))
	for k, v := range n.codices.threads {
		sources[k] = v
	}
	liveRollouts := make(map[string]string, len(n.codices.liveRollouts))
	for k, v := range n.codices.liveRollouts {
		liveRollouts[k] = v
	}
	liveEndpoints := make(map[string]string, len(n.codices.liveEndpoints))
	for k, v := range n.codices.liveEndpoints {
		liveEndpoints[k] = v
	}
	n.codices.mu.Unlock()
	var out []proto.Session
	projects := map[string]string{}
	// Threads a runtime's session names (runtimecodex.go). own: those listed as their own row all the same, because
	// another program has them now. adopted: subagent rows whose parent or root thread a session names, by their
	// place in out: they hang off that session unless the thread's own row is listed.
	own := map[string]bool{}
	type adoption struct {
		row          int
		parent, root string
	}
	var adopted []adoption
	desktopAsks := map[string]codexSource{}
	emitUnavailable := func(id string, src codexSource) {
		if last, ok := previous[id]; ok && !seen[id] && !pass.runtimeOf(src.thread).live &&
			(!n.agentFiltered("codex", src.path) || n.keptSessions()[id]) {
			seen[id] = true
			out = append(out, unavailableCodexRow(last))
			if pass.unavailable == nil {
				pass.unavailable = make(map[string]bool)
			}
			pass.unavailable[id] = true
		}
	}
	suppressed := map[string]bool{}
	for id, src := range sources {
		if pass.homeUnavailable(src.home) {
			emitUnavailable(id, src)
			continue // decorating it from the filesystem would wait on the same home
		}
		if seen[id] {
			suppressed[id] = true
			continue
		}
		src, visible := n.listedCodexSource(id, src, pass)
		if pass.homeUnavailable(src.home) {
			emitUnavailable(id, src)
			continue
		}
		if !visible || n.isAssistantSessionIn(id, pass) || n.ownedAssistantSource(src.thread) {
			continue
		}
		// A runtime's session is this thread: the conversation is listed once, as that session. While the session
		// runs, always; once it does not, unless another program has the thread (elsewhere, below).
		claim := pass.runtimeOf(src.thread)
		if claim.live {
			suppressed[id] = true
			continue
		}
		elsewhere := false
		desktopViewOnly := false
		ownerAmbiguous := false
		if n.programRun(id, src, pass) {
			continue
		}
		if codexThreadOrigin(src).guardian { // an approval review of a tool call, not an Agent (#5486), its rollout gone too (#8191)
			continue
		}
		if n.agentFiltered("codex", src.path) && !n.keptSessions()[id] { // another platform's runs (agentfilters.go)
			continue
		}
		s := proto.Session{SessionID: id, Agent: "codex", SourceKind: "codex_history", SourceThreadID: src.thread, Status: proto.StatusOffline,
			Capabilities: &proto.SessionCapabilities{Read: true}, ReadOnlyReason: "仅查看：未发现可共享的 app-server 入口，请在电脑上打开会话"}
		var m quickMeta
		readPath := src.path
		if p := n.codexAppHistoryPath(src, pass); p != "" {
			readPath = p
		}
		if readPath != "" {
			if st, err := pass.stat(readPath); err == nil {
				m = n.meta(readPath, st)
				if !n.sessionScopeAllows(m.cwd) {
					continue
				}
				if identity.IsAssistantDir(m.cwd) {
					continue
				}
				s.Cwd, s.Model, s.LastText, s.UpdatedAt, s.Rev = m.cwd, m.model, snippet(m.lastText), lastUsed(m, st), st.Size()
				if m.codex && m.lastText != "" {
					s.Asks, s.AsksRead = m.asks, true
				}
				s.AgentSurface = m.surface
				s.Effort = m.effort
				m.turnErr.set(&s)
				setContext(&s, m)
				s.Goal, s.Plan = m.goal, m.plan
				title, ok := threadTitleIn(src.home, src.thread, pass)
				if !ok {
					emitUnavailable(id, src)
					continue
				}
				s.Title = title
				untitled(&s, m)
			}
		} else if t, ok := codexAppThreadOf(src); ok {
			// A Codex App chat whose rollout is gone: its row in the App's database (codex_appdb.go).
			if !n.sessionScopeAllows(t.Cwd) || identity.IsAssistantDir(t.Cwd) {
				continue
			}
			s.Cwd, s.Title, s.UpdatedAt, s.Rev = t.Cwd, t.Title, t.updated(), t.UpdatedAtMs
		}
		s.AgentName = filepath.Base(src.home)
		if holder := liveRollouts[id]; holder != "" && heldRecently(m, src.path, pass) {
			// A stdio app-server (the Codex desktop App's own) has no way in, but the rollout it holds open says
			// whether a turn runs; unknown put a desktop task at work under the app's quiet spaces.
			// Bound open-turn Working by inactivity so a stalled or prompt-blocked external CLI does not climb forever.
			s.Status = proto.StatusUnknown
			stalled := false
			if m.turnOpen {
				if heldTurnStalled(m, src.path, pass) {
					stalled = true
				} else {
					s.Status = proto.StatusWorking
				}
			} else if m.turnClosed {
				s.Status = proto.StatusDone // as a loaded idle thread on an app-server (codex_remote.go)
			}
			if ep := liveEndpoints[id]; ep != "" {
				s.ReadOnlyReason = n.codexSharedReadOnlyReason(src.home, ep)
			} else if stalled {
				s.ReadOnlyReason = i18n.T(i18n.Default, "codex-history.stalled_turn", i18n.Args{"holder": holder})
			} else {
				s.ReadOnlyReason = holder + " 正在运行，但未提供共享入口；只能同步本地历史"
			}
		} else if m.turnOpen && !m.turnClosed && !m.turnActivityAt.IsZero() && time.Since(m.turnActivityAt) < codexFreshWorking && time.Since(m.turnActivityAt) >= -5*time.Minute {
			// liveRollouts refreshes every 30s; when a rollout is actively writing an open turn between scans or on
			// hosts where lsof misses it, keep StatusWorking instead of misreporting it as stopped (#12389).
			s.Status = proto.StatusWorking
		}
		if c, t, err := n.codexOwner(id); err == nil {
			elsewhere = true
			s.SourceKind, s.SourceInstanceID, s.Status, s.Attached = "codex_app_server", c.spec.ID, t.status, t.joined
			if t.failure != "" { // turn/completed said it failed: before the rollout read has it
				reported(t.failure, t.failureKind, m.turnErr).set(&s)
			}
			if c.spec.Name != "" {
				s.AgentName = c.spec.Name
			}
			s.ParentThreadID = firstNonEmpty(t.parent, readCodexOrigin(src.path).parent)
			if t.title != "" {
				s.Title = t.title
			}
			if t.cwd != "" {
				s.Cwd = t.cwd
			}
			if t.model != "" {
				s.Model = t.model
			}
			if cOwner, tOwner, driven := n.codexEffortThread(id); driven {
				s.Model = n.codexModelNow(id, cOwner, tOwner, s.Model)
				s.Capabilities.Model = proto.Bool(true)
			}
			s.Capabilities.Send, s.Capabilities.Attach = t.parent == "", t.parent == ""
			s.Capabilities.Approve, s.Capabilities.Interrupt = t.joined, t.turn != "" && t.parent == ""
			s.ReadOnlyReason = ""
			if t.parent != "" {
				s.ReadOnlyReason = "仅查看：这个子 Agent 由父 Agent 驱动"
			}
			if src.path == "" {
				s.ReadOnlyReason = "此会话尚未生成本地历史，实时接入后可查看新输出"
			}
			n.codexEffortRow(id, &s.Efforts, &s.Effort, m) // codex_effort.go
			n.codexSpeedRow(&s)
			n.codexRewindRow(id, &s)
		} else if err != errCodexNoOwner {
			ownerAmbiguous = true
			s.ReadOnlyReason = err.Error()
		} else if codexDesktopChatIn(src, pass) {
			// Retain App identity and grouping when an update removes its bus, including database-only chats.
			s.AgentName, s.AgentSurface = "Codex App", transcript.SurfaceDesktop
			busUp := pass.desktopBusAvailable(src.home)
			s.Capabilities.Send = busUp || codexDesktopQueueCLI(src.home) != ""
			elsewhere, desktopViewOnly = s.Capabilities.Send, !s.Capabilities.Send
			s.ReadOnlyReason = i18n.T(i18n.Default, codexDesktopUnavailableKey)
			n.placeCodexDesktopIn(&s, src, src.thread, false, pass)
			if !busUp {
				// Interrupting a running turn requires the legacy follower bus; native queue delivery only enqueues messages.
				s.Capabilities.Interrupt = false
			}
			if s.Capabilities.Send {
				s.Capabilities.AnswerQuestions = true
				s.ReadOnlyReason = ""
				if busUp {
					n.codexDesktopEffortRow(&s, src, m) // codex_desktop_effort.go
				} else if s.Model == "" {
					if app := codexAppThreads(src.home)[src.thread]; app.Model != "" {
						s.Model = app.Model
						if s.Effort == "" {
							s.Effort = app.Effort
						}
					}
				}
				if time.Since(s.UpdatedAt) < codexAskMaxAge {
					desktopAsks[id] = src
				}
			}
		} else if o := codexThreadOrigin(src); o.desktop { // a subagent whose rollout is gone too: its database row (#8191)
			if o.parent != "" {
				s.AgentName, s.ParentThreadID, s.AgentSurface = "Codex App", o.parent, transcript.SurfaceDesktop
				s.ReadOnlyReason = "仅查看：这个子 Agent 由父 Agent 驱动"
				n.placeCodexDesktopIn(&s, src, o.parent, true, pass)
			}
		} else if o.cli {
			s.AgentName, s.AgentSurface = "Codex CLI", transcript.SurfaceCLI
		}
		if claim.session != "" {
			if !elsewhere {
				suppressed[id] = true
				continue // nothing else has the thread: the runtime's session, though not running, is the conversation
			}
			own[src.thread] = true
		}
		n.decorateCodexSessionIn(&s, src, cfg.LegacyHome, projects, pass)
		if s.IsSubagent && pass != nil && len(pass.runtimes) > 0 {
			if root := codexThreadOrigin(src).root; pass.runtimeOf(s.ParentThreadID).session != "" || pass.runtimeOf(root).session != "" {
				adopted = append(adopted, adoption{len(out), s.ParentThreadID, root})
			}
		}
		if s.SourceKind == "codex_history" && !s.Capabilities.Send && !s.IsSubagent && !n.codexBlocked(id) && !ownerAmbiguous {
			// A shared stamp corroborated by the App sidebar uses the same takeover safeguards.
			if codexAppOrigin(src) || desktopViewOnly && codexThreadOrigin(src).sharedAppStamp() && pass.appState(src.home).listed(src.thread) {
				liveWriter := liveRollouts[id] != "" || s.Status == proto.StatusWorking || n.codexRemoteConnecting(src.home)
				if !liveWriter && !pass.desktopAvailable(src.home) {
					resumable := n.codexResumableIn(context.Background(), id, pass)
					if resumable {
						s.Capabilities.Terminal = true
						s.Capabilities.StopCause = n.codexHistoryStopCause(id, src, m, true)
					}
					if takeover := n.codexTakeoverRowIn(id, src, m, false, pass); takeover != nil {
						s.Capabilities.Takeover = takeover
						if takeover.CanResume || resumable {
							s.ReadOnlyReason = i18n.T(i18n.Default, codexDesktopRecoverableKey)
						} else {
							s.ReadOnlyReason = i18n.T(i18n.Default, codexDesktopContinueOnlyKey)
						}
					} else if resumable {
						s.ReadOnlyReason = i18n.T(i18n.Default, codexDesktopRecoverableKey)
					}
				}
			} else if !desktopViewOnly {
				if s.Status == proto.StatusOffline && n.codexResumableIn(context.Background(), id, pass) {
					// Nothing has it open: a stopped agent, read-only like any (stopped.go, #4754); its terminal page's Resume
					// brings it back in a new tab (session.start, codex_resume.go).
					s.Capabilities.Terminal = true
					s.ReadOnlyReason = stoppedReason
					s.Capabilities.StopCause = n.stopCause(id)
				}
				if !pass.homeUnavailable(src.home) {
					s.Capabilities.Takeover = n.codexTakeoverRowIn(id, src, m, liveRollouts[id] != "" || s.Status == proto.StatusWorking, pass)
				}
			}
		}
		if pass.homeUnavailable(src.home) {
			emitUnavailable(id, src)
			continue
		}
		if n.codexBlocked(id) {
			markCodexConflict(&s)
		}
		if s.Title == "" {
			if s.Cwd != "" {
				s.Title = filepath.Base(s.Cwd)
			} else {
				s.Title = "Codex"
			}
		}
		if !s.Capabilities.Send || s.IsSubagent {
			delete(desktopAsks, id)
		}
		seen[id] = true
		out = append(out, s)
	}
	// A subagent of a thread a runtime's session is (codex_thread): that session is its parent and its root, in place
	// of the thread's own row, which is not listed.
	for _, a := range adopted {
		if c := pass.runtimeOf(a.parent); c.session != "" && !own[a.parent] {
			out[a.row].ParentSessionID = c.session
		}
		if c := pass.runtimeOf(a.root); c.session != "" && !own[a.root] {
			out[a.row].RootSessionID = c.session
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	resolveCodexHierarchy(out)
	slotCodexDesktop(out)
	saved := make(map[string]proto.Session, len(out)+len(suppressed))
	for id, last := range previous {
		if pass.homeUnavailable(sources[id].home) || pass.unavailable[id] || suppressed[id] {
			saved[id] = last // keep observed metadata across stalls and live/runtime ownership
		}
	}
	for id, row := range pass.liveRows {
		if !pass.unavailable[id] && !pass.homeUnavailable(sources[id].home) {
			if src, known := sources[id]; known {
				if prev, ok := previous[id]; ok {
					if row.SourceKind == "" {
						row.SourceKind = prev.SourceKind
					}
					if row.AgentName == "" {
						row.AgentName = prev.AgentName
					}
					if row.AgentSurface == "" {
						row.AgentSurface = prev.AgentSurface
					}
					if row.Title == "" {
						row.Title = prev.Title
					}
				}
				if row.SourceKind == "" {
					row.SourceKind = "codex_history"
				}
				if row.SourceThreadID == "" {
					row.SourceThreadID = src.thread
				}
				saved[id] = row
			}
		}
	}
	for i := range out {
		id := out[i].SessionID
		if !pass.unavailable[id] {
			saved[id] = out[i]
		}
	}
	n.codices.mu.Lock()
	n.codices.desktopAsks = desktopAsks
	if seq > n.codices.savedSeq {
		n.codices.lastRows, n.codices.desktopAsks, n.codices.savedSeq = saved, desktopAsks, seq
	}
	n.codices.mu.Unlock()
	return out
}
