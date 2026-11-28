// Package proc is a connector that runs in a program of its own and answers for it in the hub. It
// implements every capability and declares what the program said it has (a proxy's manifest): the hub uses only
// those. The program is either a plugin's, run by the plugin host (Config.Link: the hub's way, docs/extensions/extensions.md), or
// started here with the connector protocol's own opening (Config.Path), which only this package's tests still do.
// Connector extensions such as Telegram (#1704) run the first way, outside personad for their dependencies
// (docs/archive/extensions/plan-telegram.md).
package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Link is a plugin's process (plugin/host) as one of its connectors reaches it: calls go to the connector namespace
// with the connector's id as the contribution; the process runs while the connector holds it.
type Link struct {
	Proc *host.Proc
	ID   string // the contribution: the connector's id
}

func (l Link) call(ctx context.Context, method string, params, out any) error {
	p, err := extension.Targeted(params, l.ID)
	if err != nil {
		return err
	}
	err = l.Proc.Call(ctx, serve.InPlugin(method), p, out)
	if errors.Is(err, host.ErrNotRunning) {
		return fmt.Errorf("%w", errNotRunning)
	}
	return err
}

// Config is how to run the program.
type Config struct {
	// ID and Name are the connector's before the program says (while it starts, or cannot).
	ID, Name string
	// Manifest is what the program declares, known before it ever runs: a connector switched off from the start
	// still lists its capabilities and can be signed in to and out of. The program's own (at init) is checked against
	// it, a difference logged; this one is used. Without capabilities here, the program's is used once it runs.
	Manifest connector.Manifest
	Path     string // the program (next to personad)
	Args     []string
	Env      []string // added to the hub's environment
	// Version is what `<Path> version -v` must print (the hub's own): the two come from one build. "" skips it.
	Version string
	// Settings is sent with init: the connector's section of hub.json. SettingsNow, when set, is asked instead at
	// each start (what the hub learns later: the user's time zone).
	Settings    json.RawMessage
	SettingsNow func() json.RawMessage
	// Secrets is the hub's store for this connector: the program's secrets.* calls reach only this one.
	Secrets connector.Secrets
	// KB is the knowledge base's side of the program's posts.* and archive.chats (nil: none here); only a connector
	// declaring an archive may use it.
	KB   connector.KnowledgeBase
	Logf func(string, ...any)
	// Link, when set, is the plugin process the connector runs in: Path, Args, Env, Version and Settings are then the
	// plugin host's business, and the hub passes the program's calls and events on (Handle, Event).
	Link *Link
	// GhToken, when set, answers the program's credentials.gh_token: a computer's gh login. The hub sets it only for
	// an extension it granted gh_login (docs/extensions/extensions.md "Permissions"); nil refuses.
	GhToken func(ctx context.Context, machine, host string) (string, error)
	// RecallNotBefore, when set, is when this install of the extension began (extension.Record.InstalledAt): a send the
	// ledger wrote before it was not this extension's (an earlier one with the connector's id, since uninstalled), so
	// Recall does not offer it to the program. Zero: no such limit (built-in and hub.json extensions, which are never
	// replaced by another under the same id).
	RecallNotBefore time.Time
	// Identity, when set, is the hub-verified installation provenance of the extension running this connector
	// (populated by extmgr from extension.Record, never from extension self-reports).
	Identity *connector.ExtensionIdentity
}

// Restarts of the program after it exits: doubling from the first to the last, back to the first once it ran a while.
var (
	restartFirst = 5 * time.Second
	restartLast  = 5 * time.Minute
	ranAWhile    = time.Minute
	statusEvery  = 30 * time.Second // how often a running program's status is asked (Status answers from memory)
	callTimeout  = 2 * time.Minute
)

// Conn is the connector of one program.
type Conn struct {
	cfg    Config
	events chan struct{} // a scope is pending; one slot, so Watch wakes once for any number of them

	mu sync.Mutex
	// pending are the scopes the program reported changed that Watch has not passed on yet: one each, so a backlog
	// of one scope never pushes out another's.
	pending map[connector.Scope]bool
	peer    *rpc.Peer // nil while the program is not running
	man     serve.Manifest
	tools   []proto.Tool
	actions []proto.Tool
	status  connector.Status
	// options are the connector's own settings (connector.Options) as the program last gave them; sent (true) once
	// the running program has the ones the hub set, which go again each time it starts.
	options json.RawMessage
	sent    bool
	// auth is the user's choice of where the sign-in comes from (SetAuth), given again at each start: the program
	// does not keep it.
	auth *serve.AuthSource
	// trouble is what failed in the last refresh ("" nothing), so the same failure is logged once (noteTrouble).
	trouble string
	// refreshing is the round of fetch running (nil when none); again is the round asked for while it runs, which
	// starts when it ends.
	refreshing, again *round
	// run is the context of the Run under way (hold), nil outside one: what calls with no caller's context of their
	// own (MachineOnline, SetOptions) use, so they end with the connector instead of outliving it.
	run context.Context
	// gen advances on setDown so an in-flight refresh from before the connector went down
	// cannot overwrite status or tools after setDown.
	gen uint64
}

// New is the connector of the program cfg describes; nothing runs until Run.
func New(cfg Config) *Conn {
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	return &Conn{cfg: cfg, events: make(chan struct{}, 1), pending: map[connector.Scope]bool{},
		man:    serve.Manifest{ID: cfg.ID, Name: cfg.Name},
		status: connector.Status{State: connector.StateError, Error: cfg.Name + " 进程还没启动"}}
}

func (c *Conn) ID() string { return c.cfg.ID }

// ExtensionIdentity is the hub-verified installation identity of the extension running this connector, when set.
func (c *Conn) ExtensionIdentity() (connector.ExtensionIdentity, bool) {
	if c.cfg.Identity == nil || c.cfg.Identity.Extension == "" {
		return connector.ExtensionIdentity{}, false
	}
	return *c.cfg.Identity, true
}

func (c *Conn) Name() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.man.Name
}

// Manifest is the one Config declares, as a proxy's; without one, what the program said at init (nothing before).
// Its status is always the hub's answer for it (connector.StatusOf asks a proxy's).
func (c *Conn) Manifest() connector.Manifest {
	if len(c.cfg.Manifest.Capabilities) > 0 {
		m := c.cfg.Manifest
		m.Proxy = true
		return m
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.man.Of()
}

// sameManifest says whether the program declares what Config does (capabilities in any order).
func sameManifest(declared, said connector.Manifest) bool {
	norm := func(m connector.Manifest) connector.Manifest {
		m.Capabilities = slices.Clone(m.Capabilities)
		slices.Sort(m.Capabilities)
		m.Proxy = false
		return m
	}
	a, b := norm(declared), norm(said)
	return slices.Equal(a.Capabilities, b.Capabilities) && slices.Equal(a.Hosts, b.Hosts) && slices.Equal(a.AgentTools, b.AgentTools) &&
		slices.Equal(a.Unlisted, b.Unlisted) && slices.Equal(a.ConfirmAlways, b.ConfirmAlways) && a.Poll == b.Poll && slices.Equal(a.Kinds, b.Kinds) &&
		slices.Equal(a.ArchiveSources, b.ArchiveSources) && a.MetaDescription == b.MetaDescription && a.CLI == b.CLI
}

// Status answers from memory: the program's last status, or why it is not running.
func (c *Conn) Status(context.Context) connector.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

func (c *Conn) setDown(why string) {
	c.mu.Lock()
	c.gen++
	c.peer = nil
	c.status = connector.Status{State: connector.StateError, Error: why}
	c.mu.Unlock()
}

// Run keeps the program running until ctx ends: checks it is the hub's build, starts it, restarts it after a
// backoff when it exits. A linked one holds its plugin's process instead, which the plugin host runs.
func (c *Conn) Run(ctx context.Context) {
	ctx, stop := c.hold(ctx)
	defer stop()
	if c.cfg.Link != nil {
		c.runLinked(ctx)
		return
	}
	wait := restartFirst
	for ctx.Err() == nil {
		start := time.Now()
		ch, err := c.spawn(ctx)
		if err == nil {
			c.mu.Lock()
			c.peer, c.sent = ch.peer, false
			c.mu.Unlock()
			c.refresh(ctx)
			c.keepUp(ctx, ch)
			ch.stop()
		}
		if ctx.Err() != nil {
			c.setDown(c.cfg.Name + " 已停止")
			return
		}
		if time.Since(start) >= ranAWhile {
			wait = restartFirst
		}
		why := c.cfg.Name + " 进程在重启"
		if err != nil {
			why = err.Error()
		}
		c.setDown(why)
		c.cfg.Logf("connector %s: the program stopped (%v), again in %s", c.cfg.ID, ch.err(err), wait)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, restartLast)
	}
}

// hold makes ctx the connector's run (life) until stop, which ends what was started under it.
func (c *Conn) hold(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.run = ctx
	c.mu.Unlock()
	return ctx, func() {
		c.mu.Lock()
		if c.run == ctx {
			c.run = nil
		}
		c.mu.Unlock()
		cancel()
	}
}

// life is the context of the connector's run; outside one, a background one (a call then finds the program not
// running, as before Run).
func (c *Conn) life() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.run == nil {
		return context.Background()
	}
	return c.run
}

// runLinked holds the plugin's process while ctx lasts, refreshing the status and tools each time it comes up and
// every so often while it is up.
func (c *Conn) runLinked(ctx context.Context) {
	p := c.cfg.Link.Proc
	release := p.Hold()
	defer release()
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	up := false
	for {
		changes := p.Changes()
		switch ready := p.Ready(); {
		case ready && !up:
			up = true
			c.mu.Lock()
			c.sent = false
			c.mu.Unlock()
			c.refresh(ctx)
		case !ready:
			up = false
			state, why := p.Status()
			switch state {
			case host.StateError:
			case host.StateStarting:
				why = c.cfg.Name + " 正在启动"
			default:
				why = c.cfg.Name + " 进程还没启动"
			}
			c.setDown(why)
		}
		select {
		case <-ctx.Done():
			c.setDown(c.cfg.Name + " 已停止")
			return
		case <-changes:
		case <-t.C:
			if up {
				c.refresh(ctx)
			}
		}
	}
}

// keepUp refreshes the status every so often until the program exits or ctx ends.
func (c *Conn) keepUp(ctx context.Context, ch *child) {
	t := time.NewTicker(statusEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch.peer.Done():
			return
		case <-t.C:
			c.refresh(ctx)
		}
	}
}
