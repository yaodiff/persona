package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/agentref/agentrefadapt"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	kitoutcome "github.com/nwverse/persona/server/internal/domainkit/outcome" // the package's tests declare an outcome
)

// Where the Service runs what its modules registered through the kernel (kernel/seam; docs/server/architecture.md
// "Validated and sealed"): each slot runs where the central line it replaced ran.
//   - Routes: Service.handle, or routesThrough below for route functions written before the kernel.
//   - Bus methods: BusHub.request asks s.kernel.Answer after its own cases, where the Inbox's switch was (bus.go).
//   - Settled-turn hooks: turnSettledHooks below, inside turnEnded's !meeting branch (service.go).
//   - Handshake hooks: s.kernel.Connected on a goroutine of its own once the connection is up (BusHub.onHello).
//   - Snapshot parts: s.kernel.Snapshot where snap.Inbox was filled, before snap.Work (Service.snapshot).

// routesThrough runs routes, a module's route functions written before the kernel, with each Service.handle in them
// registering through k: the functions stay as they were, and their routes are k's module's, served under the
// kernel's catch site. A call inside another is a defect.
func (s *Service) routesThrough(k seam.Kernel, routes func()) {
	if s.routing != nil {
		defect.Raise("assistant.routesThrough", "a module's routes are being registered already")
	}
	s.routing = &k
	defer func() { s.routing = nil }()
	routes()
}

// turnSettledHooks runs the modules' settled-turn hooks for r's session once its turn stayed still (afterSettle's
// delay, read now), on a timer as afterSettle does. The ids become the session value once, here; ids that name no
// session run no hook and are logged as they came: that is the hub's bad input, not a programmer's error.
func (s *Service) turnSettledHooks(ctx context.Context, r SessionRef) {
	agent, f := kitoutcome.Unpack(agentrefadapt.FromWire(r))
	if f != nil {
		s.logf("assistant: turn ended with machine %q and session %q, which name no session: no settled-turn hook runs",
			r.MachineID, r.SessionID)
		return
	}
	time.AfterFunc(s.settleDelay(ctx), func() { s.kernel.TurnSettled(ctx, agent) })
}
