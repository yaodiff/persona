package seam

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/rpc"
)

// BusMethod is one bus method a module answers: its name and how it answers. The zero BusMethod is no method; only
// NewBusMethod makes one.
type BusMethod struct {
	name   string
	answer func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
}

// NewBusMethod is the bus method name, answered by fn. The call's params are decoded into a P once, as encoding/json
// decodes (a field P lacks is ignored), and a body that does not decode is answered invalid_params, as BusHub.request
// answers one; fn's reply is encoded as the bus encodes answers (< > & left as they are). An empty name, a name that
// is not lower-case words joined by dots ("inbox.repo_mutes"), a name no domain lists as a module's
// (assistantwire.IsModuleMethod), or a nil fn is a defect. The list keeps a module off the names the central dispatch
// answers (BusHub.request's own cases, the extension manager's and the devices'), and archguard's
// TestModuleBusMethodsAreNoCentralCase keeps those names off the list.
func NewBusMethod[P, R any](name string, fn func(context.Context, P) (R, error)) BusMethod {
	const op = "seam.NewBusMethod"
	if name == "" {
		defect.Raise(op, "a bus method name is not empty")
	}
	if !dotted(name) {
		defect.Raise(op, strconv.Quote(name)+" is no bus method name: lower-case words of [a-z0-9_] joined by dots, "+
			"as \"inbox.repo_mutes\"")
	}
	if !assistantwire.IsModuleMethod(name) {
		defect.Raise(op, strconv.Quote(name)+" is no module's bus method: a name no domain lists is the central "+
			"dispatch's (BusHub.request's cases, the extension manager's, the devices'); list a module's own from an "+
			"init() in its domain's file, assistantwire/<domain>.go, func init() { addModuleMethods(…) }, as "+
			"inbox_bus.go does")
	}
	if fn == nil {
		defect.Raise(op, strconv.Quote(name)+" has no function")
	}
	return BusMethod{name: name, answer: func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: name + ": " + err.Error()}
		}
		reply, err := fn(ctx, p)
		if err != nil {
			return nil, err
		}
		return encode(reply)
	}}
}

// NewTypedBusMethod binds a module handler to the request and reply types of a bus method descriptor.
func NewTypedBusMethod[P, R any](m buswire.Method[P, R], fn func(context.Context, P) (R, error)) BusMethod {
	return NewBusMethod(m.Name, fn)
}

// dotted says whether name is two or more words of [a-z0-9_], each starting with a letter, joined by single dots.
func dotted(name string) bool {
	words, start := 0, true
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '.':
			if start {
				return false
			}
			start = true
		case start && c >= 'a' && c <= 'z':
			start = false
			words++
		case !start && (c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_'):
		default:
			return false
		}
	}
	return !start && words >= 2
}

// encode is v as the bus writes an answer (internal/rpc's marshal): JSON with < > & as they are and no newline, so a
// reply a module returns reaches the hub with the bytes it had when package assistant returned it itself.
func encode(v any) (json.RawMessage, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

func (m BusMethod) made(op string) {
	if m.answer == nil {
		defect.Raise(op, "a BusMethod comes from NewBusMethod")
	}
}

// Bus answers m's calls once BusHub.request has found none of its own cases for them (Registry.Answer). A zero
// BusMethod, a name another module registered, or a call after Seal is a defect.
func (k Kernel) Bus(m BusMethod) {
	const op = "seam.Kernel.Bus"
	reg := k.registry(op)
	reg.open(op)
	m.made(op)
	if owner, taken := reg.methods[m.name]; taken {
		defect.Raise(op, strconv.Quote(m.name)+" is registered twice, by "+strconv.Quote(owner.module.name)+" and "+
			strconv.Quote(k.module.name))
	}
	reg.methods[m.name] = registeredMethod{module: k.module, method: m}
}

// registeredMethod is a bus method and the module that registered it.
type registeredMethod struct {
	module ModuleName
	method BusMethod
}

// Answer answers the bus method name with params when a module registered it: ok is false when none did, and the
// caller goes on to its own. The reply is the method's answer, encoded; err is its error, or invalid_params for params
// that do not decode.
func (r *Registry) Answer(ctx context.Context, name string, params json.RawMessage) (reply json.RawMessage, ok bool, err error) {
	r.made("seam.Registry.Answer")
	m, ok := r.methods[name]
	if !ok {
		return nil, false, nil
	}
	reply, err = m.method.answer(ctx, params)
	return reply, true, err
}
