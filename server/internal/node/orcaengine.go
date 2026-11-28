package node

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// Orca's row of node.info's engines is kept current while the node is connected (#17037): node.info is asked once,
// when the node connects, and Orca is opened and quit while the node runs, which left every engine picker, the web's
// Orca worktree actions and the clients' "Orca isn't running" notice as they were until the node reconnected. So the
// node asks Orca again every orcaEngineEvery (one status.get on its local socket, or only a look for its metadata
// file when it is not there) and, when the row changed, tells the hub the engines again (engines.changed); the
// manager takes them into the machine and the clients get the machine as on any change.

// orcaEngineEvery is how often the node asks Orca whether it runs.
var orcaEngineEvery = 10 * time.Second

// orcaEngineWait bounds one ask.
const orcaEngineWait = 5 * time.Second

// engineReport is the engines node.info said last on this connection, with Orca's row as the node has told since.
type engineReport struct {
	mu   sync.Mutex
	list []proto.Engine
}

// said keeps list as what the hub was told (node.info) and returns it.
func (r *engineReport) said(list []proto.Engine) []proto.Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = slices.Clone(list)
	return list
}

// replace puts e in place of the row with its id, and returns the engines when that changed them; nil when it did not,
// or before node.info said any (the hub has none to update then).
func (r *engineReport) replace(e proto.Engine) []proto.Engine {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.list, func(x proto.Engine) bool { return x.ID == e.ID })
	if i < 0 || r.list[i] == e {
		return nil
	}
	r.list[i] = e
	return slices.Clone(r.list)
}

// watchOrcaEngine asks Orca every orcaEngineEvery for as long as the connection lasts (checkOrcaEngine).
func (n *Node) watchOrcaEngine(ctx context.Context) {
	t := time.NewTicker(orcaEngineEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.checkOrcaEngine(ctx)
		}
	}
}

// checkOrcaEngine asks Orca once and tells the hub the engines again when Orca's row changed (engines.changed).
func (n *Node) checkOrcaEngine(ctx context.Context) {
	probe, cancel := context.WithTimeout(ctx, orcaEngineWait)
	e := orcaEngine(probe, orcaClient())
	cancel()
	if ctx.Err() != nil {
		return // the connection ended meanwhile: what the ask found says nothing about Orca
	}
	if list := n.engRep.replace(e); list != nil && n.peer != nil {
		_ = n.peer.Notify(proto.EventEnginesChanged, proto.EnginesChanged{Engines: list})
	}
}

// Why a start could not reach an engine (engine_missing's data reason, the hub's rpc-errors.engine_<reason>): Orca's
// (orcaMissing), and since #17057 any engine's: it did not answer (unavailable), herdr's autostart failed too
// (start_failed), a stopped session's resume found no engine to open it in (resume_unavailable), the start named an
// engine the computer does not turn on (not_enabled), the computer turns on none (none).
const (
	engineNotRunning        = "not_running"
	engineNotInstalled      = "not_installed"
	engineIncompatible      = "incompatible"
	engineUnavailable       = "unavailable"
	engineStartFailed       = "start_failed"
	engineResumeUnavailable = "resume_unavailable"
	engineNotEnabled        = "not_enabled"
	engineNone              = "none"
)

// orcaUnreachable: err says Orca is not running or speaks a protocol Persona does not (what orcaMissing words).
func orcaUnreachable(err error) bool {
	return errors.Is(err, orcart.ErrNotRunning) || errors.Is(err, orcart.ErrIncompatible)
}

// orcaMissing is the refusal of a start Orca could not take, err being why (orcaUnreachable): engine_missing naming
// Orca and what to do on the computer (open it, install it, update it), not herdr's words (#17037). The message is
// the default language's; the hub words it in the reader's by its data (engine, reason: apierr's engine_missing).
// Orca's own error is the detail. Nothing was started or typed.
func orcaMissing(err error) error {
	reason := engineNotRunning
	switch {
	case errors.Is(err, orcart.ErrIncompatible):
		reason = engineIncompatible
	case !orcaFound():
		reason = engineNotInstalled
	}
	name := proto.EngineNames[proto.EngineOrca]
	return refusal(engineMissing, i18n.T(i18n.Default, "rpc-errors.engine_"+reason, i18n.Args{"engine": name}),
		map[string]any{"engine": proto.EngineOrca, "reason": reason, "detail": err.Error()})
}

// orcaFound: Orca is installed on this machine (orcaInstalled).
func orcaFound() bool {
	found, _ := orcaInstalled()
	return found
}
