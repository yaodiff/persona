package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- calls through the manager ----

// deviceBusMethod keeps cancellation scoped to guarded retirement and the long file operations the node can cancel.
func deviceBusMethod(method string) buswire.Method[devicewire.NodeCall, json.RawMessage] {
	if method == nodeproto.StopIfIdle {
		return devicewire.RetireCallMethod
	}
	if method == "file.copy" || method == "file.zip" {
		return devicewire.FileCallMethod
	}
	return devicewire.CallMethod
}

// busNode is a machine's node as the hub reaches it: through the manager.
type busNode struct {
	h    *Hub
	id   string
	done chan struct{} // never closed: the manager says when the node is gone
}

func (b *busNode) Call(ctx context.Context, method string, params, out any) error {
	owner := b.h.machineOwner(b.id)
	if caller, ok := callerOf(ctx); ok && caller != owner { // a manager's device.call, the machine moved since its check
		return &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("%s on machine %q: another manager's now", clip(method, 64), clip(b.id, 64))}
	}
	if err := b.h.waitDevicesFor(ctx, owner); err != nil {
		return fmt.Errorf("%s: %w", b.id, err)
	}
	var streamVersion int
	if dial, ok := params.(devicewire.PortDial); method == devicewire.MethodPortDial && ok {
		// An old manager ignores the outer offer and forwards only the legacy port.dial parameters.
		streamVersion, dial.StreamVersion = dial.StreamVersion, 0
		params = dial
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	p := devicewire.NodeCall{MachineID: b.id, Method: method, Params: raw, StreamVersion: streamVersion}
	if method == "session.interrupt" {
		attachNodeCaller(ctx, &p)
	}
	if method == "session.modes" { // the manager records who changed a mode (#10297)
		p.Actor = modesActor(ctx)
	}
	if method == "session.send" { // the owner's message to an Agent: after held ones, maybe held itself (send.go, #10661)
		p.Hold, p.Ordered = holdOf(ctx), orderedOf(ctx)
	}
	if devicewire.IsSessionStart(method) || method == "session.send" {
		attachNodeCaller(ctx, &p)
		if p.Caller != nil && p.Caller.OwnerSubject != "" {
			if store := b.h.shareRoutingStore(); store != nil {
				if route, err := store.ShareRouting(ctx, b.h.hubID(), b.id, owner); err == nil {
					p.Authority = &route.Authority
				}
			}
		}
	}
	if dl, ok := ctx.Deadline(); ok {
		p.TimeoutMS = managerTimeout(time.Until(dl)).Milliseconds()
	}
	busMethod := deviceBusMethod(method)
	caller := managerBusCaller{id: owner, call: b.h.callBusAsIs}
	res, err := buswire.Call(ctx, caller, busMethod, p)
	var rpcErr *rpc.Error
	if busMethod == devicewire.FileCallMethod && errors.As(err, &rpcErr) && rpcErr.Code == bus.CodeUnknownMethod {
		// An older manager rejects file_call before reaching the node. Preserve its existing copy / zip behavior;
		// only this explicit refusal may repeat the request, and the fallback intentionally does not propagate cancel.
		res, err = buswire.Call(ctx, caller, devicewire.CallMethod, p)
	}
	if err != nil {
		return deviceError(b.id, err)
	}
	if out != nil && len(res) > 0 {
		return json.Unmarshal(res, out)
	}
	return nil
}

// managerTimeout is how long the manager gives a call the hub waits left for: a little less, so its answer (the
// node's, or its deadline: CodeDeadline) is back before the hub gives up itself.
func managerTimeout(left time.Duration) time.Duration {
	margin := min(left/10, 500*time.Millisecond)
	return max(time.Millisecond, left-margin)
}

func (b *busNode) Notify(event string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if err := b.h.notifyBus(b.h.machineOwner(b.id), devicewire.EventNotify, devicewire.NodeEvent{MachineID: b.id, Event: event, Params: raw}); err != nil {
		return fmt.Errorf("%s: %w", b.id, errManagerOffline)
	}
	return nil
}

func (b *busNode) Done() <-chan struct{} { return b.done }
func (b *busNode) Close(error)           {}

// managerDroppedError: the manager's connection dropped with the call out. The app is told the manager is not there
// (503 assistant_offline, callErr), but it is not "offline" (nothing sent): the node may have done it, so a send's
// outcome is unknown (sendOutcome) and the same message is not sent again.
type managerDroppedError struct{ id string }

func (e managerDroppedError) Error() string {
	return e.id + ": the connection to the manager dropped during the call"
}

func (managerDroppedError) Is(target error) bool {
	return target == errManagerOffline || target == rpc.ErrDropped || target == rpc.ErrClosed
}

// nodeError is an error the manager coded, given back its kind.
type nodeError struct {
	msg  string
	kind error
}

func (e *nodeError) Error() string { return e.msg }
func (e *nodeError) Unwrap() error { return e.kind }

// deviceError turns the manager's coded errors back into the hub's own: the node offline, its connection dropped,
// the call's time up, the manager gone; a node's own coded refusal stays as it is. What may have reached the node —
// the bus dropping with the call out, the connection to the node dropping, the time running out on the manager's
// side — reads as a node connection that dropped or timed out (a send's outcome unknown, send.go), never as offline.
func deviceError(id string, err error) error {
	switch {
	case errors.Is(err, errBusDropped):
		return managerDroppedError{id: id}
	case errors.Is(err, errBusOffline):
		return fmt.Errorf("%s: %w", id, errManagerOffline)
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	switch re.Code {
	case devicewire.CodeMachineOffline, devicewire.CodeNoDevice:
		return &nodeError{msg: re.Message, kind: errOffline}
	case devicewire.CodeNodeClosed:
		return &nodeError{msg: re.Message, kind: rpc.ErrDropped}
	case devicewire.CodeDeadline:
		return &nodeError{msg: re.Message, kind: context.DeadlineExceeded}
	}
	return err
}

// modesActor is who asks for a session.modes call, for the manager's record of mode changes (#10297): the device and
// its name, the hub's own token, or an extension. Display only; the request was checked before it got here.
func modesActor(ctx context.Context) *devicewire.Actor {
	if a := devicewire.ModeActorOf(ctx); a != nil {
		return a
	}
	if _, ok := callerOf(ctx); ok {
		return devicewire.AssistantModeActor()
	}
	pr := principalOf(ctx)
	switch {
	case pr.Role == roleExtension:
		return &devicewire.Actor{Kind: "extension", Extension: rowText(pr.Extension, 128)}
	case pr.ID == "":
		return &devicewire.Actor{Kind: "hub"}
	}
	return &devicewire.Actor{Kind: "device", DeviceID: rowText(pr.ID, 128), DeviceName: rowText(pr.Name, 128)}
}
