package hub

import (
	"context"
	"errors"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A large request body goes to the manager on a relay stream of its own (#6574). Inside the http.request line it
// held up every other call on the bus until it was written: the bus is one WebSocket, one message at a time, and a
// message goes out as one frame. On a slow link the keepalive's ping, which waits at most 5 s for that frame, failed
// and the bus dropped with every call on it.
//
// The request itself still goes on the bus (http.request_streamed: the same params without the body, plus the
// relay's id and the body's size), and so does the answer: everything but the body's bytes travels as before. The
// manager dials the relay, reads exactly that many bytes, and only then serves the request as http.request does.

// bodyStreamMin is the smallest body the hub sends on a stream of its own: below it, the relay's extra round trip
// costs more than the bus is held up.
const bodyStreamMin = 1 << 20

// callHTTP calls http.request for req on manager id, with its body on a relay stream when it is large and the
// manager takes one (assistantwire.CapabilityHTTPBodyStream). A manager that turns the stream down before the request
// ran (an unknown method, a body that did not arrive whole) gets http.request with the body inline, as an older one
// does: nothing has run, so nothing runs twice.
func (h *Hub) callHTTP(ctx context.Context, id string, req assistantwire.HTTPRequest, res *assistantwire.HTTPResponse) error {
	if len(req.Body) >= bodyStreamMin && h.managerCan(id, assistantwire.CapabilityHTTPBodyStream) {
		err := h.callHTTPStreamed(ctx, id, req, res)
		var coded *rpc.Error
		if !errors.As(err, &coded) || (coded.Code != assistantwire.CodeBodyIncomplete && coded.Code != bus.CodeUnknownMethod) {
			return err
		}
		h.logf("bus: [%s] %s %s: the body stream was turned down (%s), sent inline", id, req.Method, req.Path, coded.Code)
	}
	return callHTTPInline(ctx, h.managerCaller(id), assistantwire.HTTPMethod, req, res)
}

// callHTTPInline binds the request and reply types while letting the RPC encoder
// append the body directly to the outgoing line.
func callHTTPInline(ctx context.Context, caller buswire.Caller, method buswire.Method[assistantwire.HTTPRequest, assistantwire.HTTPResponse], req assistantwire.HTTPRequest, res *assistantwire.HTTPResponse) error {
	return caller.Call(ctx, method.Name, httpParams(req), res)
}

// managerCan: manager id's current connection named capability in its manager.hello.
func (h *Hub) managerCan(id, capability string) bool {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	c := h.bus.current[id]
	return c != nil && slices.Contains(c.presence.Capabilities, capability)
}

// callHTTPStreamed is http.request_streamed: req without its body on the bus, the body on a relay only manager id
// may take, written while the call waits for its answer.
func (h *Hub) callHTTPStreamed(ctx context.Context, id string, req assistantwire.HTTPRequest, res *assistantwire.HTTPResponse) error {
	body := req.Body
	rid, taken, drop := h.offerRelay(relayFor{manager: id})
	defer drop() // after the pump is told to stop: a relay dialed too late goes away
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	go pumpBody(pctx, taken, body)
	req.Body = nil
	req.BodyStream = &assistantwire.BodyStream{ID: rid, Size: int64(len(body))}
	out, err := buswire.Call(ctx, h.managerCaller(id), assistantwire.HTTPStreamedMethod, req)
	if err == nil {
		*res = out
	}
	return err
}

// pumpBody writes body to the relay the manager takes from taken, as a body stream (bus.WriteBody). It gives up when
// ctx ends (the call is over) before the manager took it, or while writing; the manager then reads less than it was
// told and turns the request down.
func pumpBody(ctx context.Context, taken <-chan *relayTaken, body []byte) {
	var t *relayTaken
	select {
	case t = <-taken:
	case <-ctx.Done():
		return
	}
	defer close(t.done) // serveRelay lets the relay go
	_ = bus.WriteBody(ctx, t.conn, body)
}
