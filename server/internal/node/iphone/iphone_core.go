package iphone

// The iphone program's driver on a real computer: the core of internal/iphonectl (element numbers, reading the screen,
// limits, whether an input took effect, the owner's approval), reaching the iPhone through the bridge's operations on
// the session the program hands it (docs/extensions/iphone-control.md).

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/iphonectl"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// iphoneControl is iphoneDriver over one iphonectl.Controller for the whole node: the controller keeps each
// conversation's numbered read and the step waiting for the owner in memory, so every call must reach the same one.
type iphoneControl struct {
	once sync.Once
	c    *iphonectl.Controller

	mu     sync.Mutex
	listed map[string]iphoneAppsRead // by device: when its apps were last read for Persona's own
}

// iphoneAppsRead is one device's last read of its apps for Persona's own: when, and whether it answered.
type iphoneAppsRead struct {
	at time.Time
	ok bool
}

func (d *iphoneControl) controller() *iphonectl.Controller {
	d.once.Do(func() { d.c = iphonectl.NewController(iphonectl.Options{Rules: iphoneRules()}) })
	return d.c
}

// iphoneSelfNames is Persona's app as the iPhone names it: the product's public display name. A front app with this
// name is never operated, whatever its bundle id, so the rule holds before any bundle id is known.
var iphoneSelfNames = []string{"Persona"}

// iphoneRules are the rules the core starts with: Persona's app by its name. Its bundle ids are added as they become
// known: from each device's own app list (selfApps) and from the calls (proto.IPhoneCall.SelfApps); the core only
// ever adds to them.
func iphoneRules() iphonectl.Rules {
	return iphonectl.Rules{SelfNames: slices.Clone(iphoneSelfNames)}
}

// How often a device's apps are read again for Persona's own, after an answer and after none, and how long a read
// may take of a call.
const (
	iphoneAppsFresh = time.Hour
	iphoneAppsRetry = time.Minute
	iphoneAppsWait  = 5 * time.Second
)

// selfApps are the bundle ids of the apps on device named like Persona's (names: iphoneSelfNames and the call's),
// read with its own list_apps at most once in iphoneAppsFresh (iphoneAppsRetry after a failure), so a launch of it by
// bundle id is refused too. Nil when not due or the list can't be read: the name rule still holds. The ids stay in
// memory only (the core keeps them); they are never logged.
func (d *iphoneControl) selfApps(ctx context.Context, device string, w *iphonectl.Wire, names []string) []string {
	now := time.Now()
	d.mu.Lock()
	last, seen := d.listed[device]
	again := iphoneAppsRetry
	if last.ok {
		again = iphoneAppsFresh
	}
	due := !seen || now.Sub(last.at) >= again
	if due {
		if d.listed == nil {
			d.listed = map[string]iphoneAppsRead{}
		}
		d.listed[device] = iphoneAppsRead{at: now} // one read at a time: a call meanwhile goes on without it
	}
	d.mu.Unlock()
	if !due {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, iphoneAppsWait)
	defer cancel()
	apps, err := w.ListApps(lctx, device)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	d.listed[device] = iphoneAppsRead{at: time.Now(), ok: true}
	d.mu.Unlock()
	names = append(slices.Clone(iphoneSelfNames), names...)
	var out []string
	for _, a := range apps {
		if a.BundleID != "" && slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(strings.TrimSpace(n), strings.TrimSpace(a.Name)) }) {
			out = append(out, a.BundleID)
		}
	}
	return out
}

// Run is one look or do: the core answers within the call's context whatever the bridge does, so a failure of the
// iPhone or of a step is in the answer (what was sent, what was not). Only a call the core turned down as a whole
// before sending anything is an error, with the core's code for why (bad_args, not_pending, busy: the approval still
// pending), saying nothing was sent, in the core's words.
func (d *iphoneControl) Run(ctx context.Context, r iphoneRun) (iphoneOutcome, error) {
	w := &iphonectl.Wire{Call: iphoneWireCall(r.Bridge), TempDir: r.TempDir}
	self := append(slices.Clone(r.SelfApps), d.selfApps(ctx, r.Device, w, r.SelfNames)...)
	resp := d.controller().Handle(ctx, w, iphonectl.Request{Device: r.Device, Session: r.Session, Op: r.Op, Args: r.Args,
		Approved: r.Approved, Confirm: r.Confirm, SelfApps: self, SelfNames: r.SelfNames})
	if resp.Refused != "" {
		return iphoneOutcome{}, iphoneErr(resp.Refused, strings.TrimSpace(resp.Text), proto.IPhoneNotSent)
	}
	out := iphoneOutcome{Output: resp.Output, Text: resp.Text, NeedsApproval: resp.NeedsApproval, ApprovalID: resp.ApprovalID}
	if resp.Approval != nil {
		// Data for the manager, which words it in the owner's language; the node never phrases it.
		a, err := iphoneApproval(resp.Approval)
		if err != nil {
			return iphoneOutcome{}, err
		}
		out.Approval = a
	}
	if img := resp.Image; img != nil {
		out.ImageFile = img.File
		if out.ImageFile == "" && len(img.Data) > 0 {
			out.ImageFile = iphoneSaveImage(r.TempDir, img.Data)
		}
	}
	return out, nil
}

// iphoneApproval is the core's approval as the manager reads it: proto.IPhoneApproval mirrors iphonectl.Approval
// field by field under the same JSON names (TestIPhoneApprovalMirrorsTheCore keeps the two in step).
func iphoneApproval(a *iphonectl.Approval) (*proto.IPhoneApproval, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var out proto.IPhoneApproval
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// iphoneSaysMaybeSent: err's data says the operation may have reached the iPhone (the session handed it over and lost
// the answer), whatever its code.
func iphoneSaysMaybeSent(err error) bool {
	var coded *rpc.Error
	if !errors.As(err, &coded) || len(coded.Data) == 0 {
		return false
	}
	var d proto.IPhoneErrorData
	return json.Unmarshal(coded.Data, &d) == nil && d.Sent == proto.IPhoneMaybeSent
}

// iphoneSaveImage writes a screenshot the bridge answered inline into dir, where the program reads it from; "" when
// it can't.
func iphoneSaveImage(dir string, data []byte) string {
	f, err := os.CreateTemp(dir, "screenshot-*")
	if err != nil {
		return ""
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return ""
	}
	return f.Name()
}

// iphoneWireCall is the session's call as the core's wire: an error with a code becomes the core's *Error with the
// same code, marked NotSent when the session or the code says the operation never reached the iPhone (iphoneSent)
// and MaybeSent when the session says it may have, so the core never counts such an input as refused; any other error
// (the context's) passes as it is, so the core sees its own deadline.
func iphoneWireCall(bridge func(context.Context, string, json.RawMessage) (json.RawMessage, error)) func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, op string, args json.RawMessage) (json.RawMessage, error) {
		out, err := bridge(ctx, op, args)
		if err == nil {
			return out, nil
		}
		code := iphoneErrorCode(err)
		if code == "" {
			return nil, err
		}
		msg := err.Error()
		var coded *rpc.Error
		if errors.As(err, &coded) {
			msg = coded.Message
		}
		return nil, &iphonectl.Error{Code: code, Message: msg, NotSent: iphoneSent(err) == proto.IPhoneNotSent,
			MaybeSent: iphoneSaysMaybeSent(err)}
	}
}
