package proc

import (
	"context"
	"errors"
	"strings"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// round is one fetch that every refresh asked for meanwhile shares. Its context ends only once every asker's has,
// so one asker giving up (a sign-in request whose client went away) does not cancel it for the others.
type round struct {
	done   chan struct{} // closed when the fetch is done
	ctx    context.Context
	cancel context.CancelFunc
	live   int           // askers whose context has not ended; guarded by Conn.mu
	stops  []func() bool // unregister the askers' context.AfterFunc once the round is over
}

// refresh asks the running program for its status and tools and waits for the answer (or ctx), one ask at a time:
// see kick.
func (c *Conn) refresh(ctx context.Context) {
	select {
	case <-c.kick(ctx):
	case <-ctx.Done():
	}
}

var closedRefreshDone = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

// kick starts a refresh and returns a channel closed when it is done. While one runs, it is not run alongside (a
// program that says changed often would have many in flight, and an older answer landing last would overwrite a
// newer one): one more round is due when it ends, which all the asks meanwhile share.
func (c *Conn) kick(ctx context.Context) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing != nil {
		if c.again != nil {
			if ctx.Err() == nil {
				c.join(c.again, ctx)
			}
			return c.again.done
		}
		if ctx.Err() != nil {
			return closedRefreshDone
		}
		c.again = newRound(ctx)
		c.join(c.again, ctx)
		return c.again.done
	}
	if ctx.Err() != nil {
		return closedRefreshDone
	}
	r := newRound(ctx)
	c.join(r, ctx)
	c.refreshing = r
	go c.refreshRounds(r)
	return r.done
}

// newRound is a round not yet run, whose context keeps ctx's values but not its end (join adds that).
func newRound(ctx context.Context) *round {
	rctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &round{done: make(chan struct{}), ctx: rctx, cancel: cancel}
}

// join makes ctx one of r's askers: r's context ends once every asker's has. c.mu is held.
func (c *Conn) join(r *round, ctx context.Context) {
	r.live++
	r.stops = append(r.stops, context.AfterFunc(ctx, func() {
		c.mu.Lock()
		r.live--
		last := r.live == 0
		detached := false
		var stops []func() bool
		if last && c.again == r {
			// Detach a queued round whose askers all canceled so a later healthy caller starts a fresh round
			// instead of inheriting a canceled context.
			c.again = nil
			detached = true
			stops = r.stops
			r.stops = nil
		}
		c.mu.Unlock()
		if last {
			r.cancel()
		}
		if detached {
			for _, stop := range stops {
				stop()
			}
			close(r.done)
		}
	}))
}

// refreshRounds runs fetch for r, then the round asked for meanwhile, until none is.
func (c *Conn) refreshRounds(r *round) {
	for r != nil {
		c.fetch(r.ctx)
		close(r.done)
		c.mu.Lock()
		stops := r.stops
		c.refreshing, c.again = c.again, nil
		next := c.refreshing
		c.mu.Unlock()
		for _, stop := range stops {
			stop()
		}
		r.cancel()
		r = next
	}
}

// fetch asks the running program for its status and tools. A tools or actions list whose call fails keeps the
// last one; a status call that fails puts the connector in error, so a hung program is not shown as healthy.
func (c *Conn) fetch(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, callTimeout)
	defer cancel()
	var call func(method string, params, out any) error
	var peer *rpc.Peer // the child's run asked; nil for a linked program
	var linkGen uint64 // the linked plugin run asked; 0 for a standalone program
	c.mu.Lock()
	gen := c.gen
	peer = c.peer
	c.mu.Unlock()
	if l := c.cfg.Link; l != nil {
		linkGen = l.Proc.Generation()
		if linkGen == 0 {
			return
		}
		call = func(method string, params, out any) error { return l.call(ctx, method, params, out) }
	} else {
		if peer == nil {
			return
		}
		call = func(method string, params, out any) error { return peer.Call(ctx, method, params, out) }
	}
	sameRunLocked := func() bool {
		if c.gen != gen {
			return false
		}
		if l := c.cfg.Link; l != nil {
			return l.Proc.Generation() == linkGen
		}
		return c.peer == peer
	}
	c.syncOptions(ctx, call)
	var st connector.Status
	if err := call(serve.MStatus, struct{}{}, &st); err != nil {
		if parent.Err() != nil {
			return // stopping, not a fault of the program
		}
		c.mu.Lock()
		same := sameRunLocked()
		if same {
			c.status = connector.Status{State: connector.StateError, Error: c.cfg.Name + " 没有回应：" + err.Error()}
		}
		c.mu.Unlock()
		if same {
			c.noteTrouble(serve.MStatus + ": " + err.Error())
		}
		return
	}
	tools, toolsErr := listTools(call, serve.MTools)
	if errors.Is(toolsErr, errNotRunning) || errors.Is(toolsErr, rpc.ErrClosed) {
		return
	}
	var actions []proto.Tool
	var actionsErr error
	if c.Manifest().Has(connector.CapActions) {
		actions, actionsErr = listTools(call, serve.MActions)
	}
	// If the peer closed or stopped while listing actions, do not commit a stale StateOK
	// over a process that is already tearing down.
	if errors.Is(actionsErr, errNotRunning) || errors.Is(actionsErr, rpc.ErrClosed) {
		return
	}
	c.mu.Lock()
	same := sameRunLocked()
	if same {
		c.status = st
		if toolsErr == nil {
			c.tools = tools
		}
		if actionsErr == nil {
			c.actions = actions
		}
	}
	c.mu.Unlock()
	if !same || parent.Err() != nil {
		return
	}
	var trouble []string
	for _, e := range []struct {
		method string
		err    error
	}{{serve.MTools, toolsErr}, {serve.MActions, actionsErr}} {
		if e.err != nil {
			trouble = append(trouble, e.method+": "+e.err.Error()+" (the last list is kept)")
		}
	}
	c.noteTrouble(strings.Join(trouble, "; "))
}

// listTools asks for the program's tools or actions; one answering it has none of them has an empty list.
func listTools(call func(method string, params, out any) error, method string) ([]proto.Tool, error) {
	var r serve.ToolsResult
	err := call(method, struct{}{}, &r)
	if re := (*rpc.Error)(nil); errors.As(err, &re) && re.Code == serve.CodeNoCapability {
		return nil, nil
	}
	return r.Tools, err
}

// noteTrouble logs what went wrong in a refresh ("" nothing) when it differs from the last refresh's, so a program
// failing every refresh the same way is logged once and again when it recovers.
func (c *Conn) noteTrouble(trouble string) {
	c.mu.Lock()
	last := c.trouble
	c.trouble = trouble
	c.mu.Unlock()
	switch {
	case trouble == last:
	case trouble == "":
		c.cfg.Logf("connector %s: the program answers again", c.cfg.ID)
	default:
		c.cfg.Logf("connector %s: refresh: %s", c.cfg.ID, trouble)
	}
}
