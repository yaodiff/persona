package iphone

// The iphone program's host on a real computer: the pairing records and the bridge's sessions of internal/iphone
// (docs/extensions/iphone.md), seen through iphoneHost.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/iphone"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func init() { iphones = iphoneSessions{iphone.Default} }

// iphoneSessions is iphoneHost over the node's iphone.Manager. It asks m each time: iphone.Default makes the manager
// (and reads its directory) only once something asks about iPhones.
type iphoneSessions struct{ m func() *iphone.Manager }

// iphoneBridgeSeen is the last answer of Bridge, kept a while: finding the bridge runs it (its version), and the
// manager asks for the status every few seconds while the app waits on a pairing.
var iphoneBridgeSeen struct {
	sync.Mutex
	at        time.Time
	installed bool
	version   string
}

const iphoneBridgeFresh = 30 * time.Second

// Bridge: installed, and its version ("persona-iphone-bridge 0.1.0 (wire 1)" says 0.1.0).
func (s iphoneSessions) Bridge(ctx context.Context) (bool, string) {
	seen := &iphoneBridgeSeen
	seen.Lock()
	defer seen.Unlock()
	if time.Since(seen.at) < iphoneBridgeFresh {
		return seen.installed, seen.version
	}
	b := s.m().Bridge(ctx)
	version := b.Version
	if f := strings.Fields(version); len(f) >= 2 {
		version = f[1]
	}
	seen.at, seen.installed, seen.version = time.Now(), b.Installed, version
	return b.Installed, version
}

// Devices are the manager's devices as links. Their network addresses stay on this computer.
func (s iphoneSessions) Devices(ctx context.Context) []iphoneLink {
	devices := s.m().Devices(ctx)
	out := make([]iphoneLink, 0, len(devices))
	for _, d := range devices {
		out = append(out, iphoneLink{ID: d.UDID, Name: d.Name, ProductType: d.ProductType, IOS: d.OSVersion, Paired: d.Paired,
			USB: d.USB, State: d.State, Transport: d.Transport, ErrorCode: d.LastErrorCode, Info: d.Info})
	}
	return out
}

// Call is the session's call, its error as an *rpc.Error with the session's code, and whether the operation was sent
// when the session can tell (a bridge's own answer leaves that to its code, iphoneStepErr).
func (s iphoneSessions) Call(ctx context.Context, device, op string, args json.RawMessage) (json.RawMessage, error) {
	out, err := s.m().Call(ctx, device, op, args)
	if err != nil {
		return nil, iphoneSessionErr(err)
	}
	return out, nil
}

func iphoneSessionErr(err error) error {
	e := &rpc.Error{Code: iphone.Code(err), Message: err.Error()}
	switch iphone.Sent(err) {
	case proto.IPhoneNotSent:
		e.Data, _ = json.Marshal(proto.IPhoneErrorData{Sent: proto.IPhoneNotSent})
	case proto.IPhoneMaybeSent:
		e.Data, _ = json.Marshal(proto.IPhoneErrorData{Sent: proto.IPhoneMaybeSent})
	}
	return e
}

func (s iphoneSessions) Connect(device string) error { return s.m().Connect(device) }
func (s iphoneSessions) Pair(device string) error    { return s.m().StartPair(device) }
func (s iphoneSessions) Forget(device string) error  { return s.m().StartForget(device) }
func (s iphoneSessions) Cancel()                     { s.m().Cancel() }

func (s iphoneSessions) Job() *iphoneJob {
	j := s.m().Job()
	if j == nil {
		return nil
	}
	return &iphoneJob{Kind: j.Kind, State: j.State, Step: j.Step, ErrorCode: j.Code, Error: j.Error, Started: j.Started}
}
