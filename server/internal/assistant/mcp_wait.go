package assistant

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/proto"
)

type mcpWaitEntry struct {
	id   uint64
	kind proto.MCPWaitKind
}

// mcpWaitReporter belongs to one pending node request. Each open interaction has its own entry; resolving one removes
// that entry, and the newest remaining kind wins. With no interaction left, remote execution is the base state again.
type mcpWaitReporter struct {
	machine string
	callID  string
	notify  func(devicewire.NodeEvent)

	mu    sync.Mutex
	next  uint64
	base  proto.MCPWaitKind
	stack []mcpWaitEntry
	done  bool
}

type mcpWaitContextKey struct{}

type mcpWaitNotifier interface {
	Notify(devicewire.NodeEvent)
}

// mcpWaitContext opts in only a request that carries a valid UUID from a node whose hub advertised the feature. The
// manager records the origin itself: a later computer.elicit can only join this live call, never choose its machine.
// A duplicate live id or a devices implementation without Notify keeps the unknown fallback.
func (s *Service) mcpWaitContext(ctx context.Context, machine, callID string, enabled bool) context.Context {
	notifier, ok := s.devs.(mcpWaitNotifier)
	if !enabled || !proto.ValidMCPWaitCallID(callID) || !ok {
		return ctx
	}
	r := &mcpWaitReporter{machine: machine, callID: callID, notify: notifier.Notify, base: proto.MCPWaitComputer}
	r.mu.Lock()
	s.mcpWaitMu.Lock()
	if s.mcpWaits[callID] != nil {
		s.mcpWaitMu.Unlock()
		r.mu.Unlock()
		return ctx
	}
	if s.mcpWaits == nil {
		s.mcpWaits = map[string]*mcpWaitReporter{}
	}
	s.mcpWaits[callID] = r
	s.mcpWaitMu.Unlock()
	r.publishLocked(r.base)
	r.mu.Unlock()
	return withMCPWaitReporter(ctx, r)
}

// mcpWaitElicitContext joins a nested elicitation to the reporter the manager issued for this call. It deliberately
// does not publish a new base state or register another call; the outer mcp.call owns the reporter's lifetime.
func (s *Service) mcpWaitElicitContext(ctx context.Context, callID string) context.Context {
	if !proto.ValidMCPWaitCallID(callID) {
		return ctx
	}
	s.mcpWaitMu.Lock()
	r := s.mcpWaits[callID]
	s.mcpWaitMu.Unlock()
	if r == nil {
		return ctx
	}
	return withMCPWaitReporter(ctx, r)
}

// endMCPWait removes only the registration owned by this request. A duplicate id could not replace the live entry.
func (s *Service) endMCPWait(ctx context.Context) {
	r := mcpWaitFrom(ctx)
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	s.mcpWaitMu.Lock()
	if s.mcpWaits[r.callID] == r {
		delete(s.mcpWaits, r.callID)
	}
	s.mcpWaitMu.Unlock()
}

func mcpWaitFrom(ctx context.Context) *mcpWaitReporter {
	r, _ := ctx.Value(mcpWaitContextKey{}).(*mcpWaitReporter)
	return r
}

func withMCPWaitReporter(ctx context.Context, r *mcpWaitReporter) context.Context {
	ctx = mcpwait.WithPush(mcpwait.WithCall(ctx, r.callID), r.push)
	return context.WithValue(ctx, mcpWaitContextKey{}, r)
}

func (r *mcpWaitReporter) publishLocked(kind proto.MCPWaitKind) {
	if r.done {
		return
	}
	params, err := json.Marshal(proto.MCPWaitEvent{CallID: r.callID, Kind: proto.NormalizeMCPWaitKind(kind)})
	if err != nil {
		return
	}
	r.notify(devicewire.NodeEvent{MachineID: r.machine, Event: proto.EventMCPWait, Params: params})
}

// push publishes kind and returns a one-time restoration that removes this push's own entry. Resolution order
// therefore does not matter: the newest entry still open wins, with the call's base state underneath every card.
func (r *mcpWaitReporter) push(kind proto.MCPWaitKind) func() {
	kind = proto.NormalizeMCPWaitKind(kind)
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return func() {}
	}
	r.next++
	entry := mcpWaitEntry{id: r.next, kind: kind}
	r.stack = append(r.stack, entry)
	r.publishLocked(kind)
	r.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.done {
				return
			}
			at := -1
			for i := range r.stack {
				if r.stack[i].id == entry.id {
					at = i
					break
				}
			}
			if at < 0 {
				return
			}
			r.stack = append(r.stack[:at], r.stack[at+1:]...)
			current := r.base
			if len(r.stack) > 0 {
				current = r.stack[len(r.stack)-1].kind
			}
			r.publishLocked(current)
		})
	}
}

func pushMCPWait(ctx context.Context, kind proto.MCPWaitKind) func() {
	if r := mcpWaitFrom(ctx); r != nil {
		return r.push(kind)
	}
	return mcpwait.Push(ctx, kind)
}

// mcpWaitParams adds only the correlators the hub needs to relay an approval's wait kind back through the existing
// devices.notify channel. An existing machine_id belongs to the tool call and is never replaced; a mismatch with the
// reporter's origin disables wait updates for that call. The tool name and arguments stay in their existing fields.
func mcpWaitParams(ctx context.Context, params map[string]any) {
	r := mcpWaitFrom(ctx)
	if r == nil {
		return
	}
	if value, present := params["machine_id"]; present {
		machine, ok := value.(string)
		if !ok || machine != r.machine {
			return
		}
	} else {
		params["machine_id"] = r.machine
	}
	params["call_id"] = r.callID
}

func pushMCPInteraction(ctx context.Context, ix proto.Interaction) func() {
	if kind, ok := mcpwait.InteractionKindFrom(ctx, ix.ID); ok {
		return pushMCPWait(ctx, kind)
	}
	switch ix.Kind {
	case proto.IxPermission, proto.IxPlan:
		return pushMCPWait(ctx, proto.MCPWaitApproval)
	case proto.IxQuestion, proto.IxElicitation:
		return pushMCPWait(ctx, proto.MCPWaitAnswer)
	default:
		return func() {}
	}
}
