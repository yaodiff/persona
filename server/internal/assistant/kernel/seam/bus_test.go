package seam_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// item and answer are a bus method's params and reply as a module types them.
type item struct {
	ID string `json:"id"`
}

type answer struct {
	Text string `json:"text"`
	OK   bool   `json:"ok"`
}

func echo(_ context.Context, p item) (answer, error) { return answer{Text: p.ID, OK: true}, nil }

func TestBusMethodNames(t *testing.T) {
	for _, name := range []string{assistantwire.MethodInboxItem, assistantwire.MethodInboxRepoMutes} {
		seam.NewBusMethod(name, echo)
	}
	raises(t, "seam.NewBusMethod: a bus method name is not empty", func() { seam.NewBusMethod("", echo) })
	for _, name := range []string{"inbox", "Inbox.item", "inbox..item", ".inbox", "inbox.", "inbox.repo-mutes",
		"inbox.2fa", "inbox item", "inbox/item", "inbox.ítem"} {
		raises(t, "is no bus method name", func() { seam.NewBusMethod(name, echo) })
	}
	// Well-formed names no domain lists as a module's: the central dispatch's own (BusHub.request's, the extension
	// manager's, the devices'), and names nobody answers yet.
	for _, name := range []string{assistantwire.MethodKBOwner, assistantwire.MethodExtensionEvent, extwire.MethodAccept,
		devicewire.MethodHoldStart, "agents.closeout.brief", "kb.day2", "inbox.collect"} {
		raises(t, "seam.NewBusMethod: "+strconv.Quote(name)+" is no module's bus method", func() {
			seam.NewBusMethod(name, echo)
		})
	}
	raises(t, `seam.NewBusMethod: "inbox.item" has no function`, func() {
		seam.NewBusMethod[item, answer]("inbox.item", nil)
	})
}

func TestABusMethodIsRegisteredOnce(t *testing.T) {
	reg := newPorts().registry()
	reg.Register(named("inbox", func(k seam.Kernel) {
		k.Bus(seam.NewBusMethod("inbox.item", echo))
		raises(t, `seam.Kernel.Bus: "inbox.item" is registered twice, by "inbox" and "inbox"`, func() {
			k.Bus(seam.NewBusMethod("inbox.item", echo))
		})
		raises(t, "seam.Kernel.Bus: a BusMethod comes from NewBusMethod", func() { k.Bus(seam.BusMethod{}) })
	}))
	reg.Register(named("saved", func(k seam.Kernel) {
		raises(t, `seam.Kernel.Bus: "inbox.item" is registered twice, by "inbox" and "saved"`, func() {
			k.Bus(seam.NewBusMethod("inbox.item", echo))
		})
	}))
}

// A bus method decodes its params into P once, as encoding/json does: a field P lacks is ignored, so a newer peer's
// extra field does not break an older manager. Its reply is encoded as the bus encodes answers.
func TestABusMethodAnswers(t *testing.T) {
	calls := 0
	reg := newPorts().registry()
	reg.Register(named("inbox", func(k seam.Kernel) {
		k.Bus(seam.NewBusMethod("inbox.item", func(ctx context.Context, p item) (answer, error) {
			calls++
			return echo(ctx, p)
		}))
		k.Bus(seam.NewBusMethod("inbox.forget", func(context.Context, item) (answer, error) {
			return answer{}, errors.New("the store is down")
		}))
	}))
	reg.Seal()
	ctx := context.Background()
	reply, ok, err := reg.Answer(ctx, "inbox.item", json.RawMessage(`{"id":"<a&b>","from_a_newer_peer":1}`))
	if !ok || err != nil || string(reply) != `{"text":"<a&b>","ok":true}` || calls != 1 {
		t.Errorf("Answer = %s, %v, %v after %d calls; want the reply with < > & as they are, from one call", reply, ok, err, calls)
	}
	_, ok, err = reg.Answer(ctx, "inbox.item", json.RawMessage(`{"id":3}`))
	var bad *rpc.Error
	if !ok || !errors.As(err, &bad) || bad.Code != bus.CodeInvalidParams ||
		bad.Message != "inbox.item: json: cannot unmarshal number into Go struct field item.id of type string" {
		t.Errorf("params that do not decode: %v, %v; want invalid_params naming the method", ok, err)
	}
	if calls != 1 {
		t.Errorf("params that do not decode reached the method (%d calls)", calls)
	}
	if reply, ok, err := reg.Answer(ctx, "inbox.forget", json.RawMessage(`{}`)); !ok || reply != nil || err == nil ||
		err.Error() != "the store is down" {
		t.Errorf("a method's error: %s, %v, %v; want it as the method returned it", reply, ok, err)
	}
	if reply, ok, err := reg.Answer(ctx, "inbox.mutes", json.RawMessage(`{}`)); ok || reply != nil || err != nil {
		t.Errorf("a method nobody registered: %s, %v, %v; want ok false for the caller's own cases", reply, ok, err)
	}
	var zero seam.Registry
	raises(t, "seam.Registry.Answer: a Registry comes from NewRegistry", func() {
		_, _, _ = zero.Answer(ctx, "inbox.item", nil)
	})
}

func TestTypedBusMethodAnswers(t *testing.T) {
	var id assistantwire.InboxMuteID
	var handlerErr error
	reg := newPorts().registry()
	reg.Register(named("inbox", func(k seam.Kernel) {
		k.Bus(seam.NewTypedBusMethod(assistantwire.InboxUnmuteMethod, func(_ context.Context, p assistantwire.InboxUnmuteParams) (assistantwire.InboxAck, error) {
			id = p.ID
			return assistantwire.InboxAck{}, handlerErr
		}))
	}))
	reg.Seal()
	ctx := context.Background()
	params := json.RawMessage(`{"id":"m1","from_a_newer_peer":true}`)
	reply, ok, err := reg.Answer(ctx, assistantwire.MethodInboxUnmute, params)
	if !ok || err != nil || id != "m1" || string(reply) != `{}` {
		t.Fatalf("Answer = %s, %v, %v with id %q, want {} for m1", reply, ok, err, id)
	}
	handlerErr = &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "the store is down"}
	if reply, ok, err := reg.Answer(ctx, assistantwire.MethodInboxUnmute, params); !ok || reply != nil || err != handlerErr {
		t.Errorf("Answer = %s, %v, %v, want the original RPC error", reply, ok, err)
	}
}
