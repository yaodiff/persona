package seam_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/agentref/agentrefadapt"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/domaintest"
)

// agent is the session a test turn settles on.
func agent(t *testing.T, machine, session string) agentref.Ref {
	t.Helper()
	return domaintest.Ok(t, agentrefadapt.FromWire(assistantwire.SessionRef{MachineID: machine, SessionID: session}))
}

// hookModule registers one hook of each kind, each noting in heard what it was told: settled, for want's session.
func hookModule(name string, want agentref.Ref, heard *[]string) module {
	return named(name, func(k seam.Kernel) {
		k.TurnSettled(func(_ context.Context, a agentref.Ref) {
			if a.IsSame(want) {
				*heard = append(*heard, name+" settled")
			}
		})
		k.Connected(func(context.Context) { *heard = append(*heard, name+" connected") })
		k.Snapshot(func(_ context.Context, snap *assistantwire.Snapshot) {
			snap.Partial = true
			*heard = append(*heard, name+" snapshot")
		})
	})
}

// Hooks run in registration order, each module's once, where the Registry is told what happened.
func TestHooksRunInRegistrationOrder(t *testing.T) {
	var heard []string
	settled := agent(t, "mac", "s1")
	reg := newPorts().registry()
	reg.Register(hookModule("inbox", settled, &heard))
	reg.Register(named("saved", func(seam.Kernel) {}))
	reg.Register(hookModule("tasks", settled, &heard))
	reg.Seal()
	ctx := context.Background()
	reg.TurnSettled(ctx, agent(t, "mac", "s1"))
	reg.Connected(ctx)
	var snap assistantwire.Snapshot
	reg.Snapshot(ctx, &snap)
	want := "inbox settled, tasks settled, inbox connected, tasks connected, inbox snapshot, tasks snapshot"
	if got := strings.Join(heard, ", "); got != want {
		t.Errorf("heard %s, want %s", got, want)
	}
	if !snap.Partial {
		t.Error("the Snapshot hooks did not fill the snapshot they were handed")
	}
	raises(t, "seam.Registry.Snapshot: the snapshot is nil", func() { reg.Snapshot(ctx, nil) })
}

// A module has one hook of each kind, and none is nil.
func TestAHookIsRegisteredOncePerModule(t *testing.T) {
	reg := newPorts().registry()
	settled := func(context.Context, agentref.Ref) {}
	connected := func(context.Context) {}
	part := func(context.Context, *assistantwire.Snapshot) {}
	reg.Register(named("inbox", func(k seam.Kernel) {
		k.TurnSettled(settled)
		k.Connected(connected)
		k.Snapshot(part)
		raises(t, `seam.Kernel.TurnSettled: module "inbox" has a TurnSettled hook already`, func() { k.TurnSettled(settled) })
		raises(t, `seam.Kernel.Connected: module "inbox" has a Connected hook already`, func() { k.Connected(connected) })
		raises(t, `seam.Kernel.Snapshot: module "inbox" has a Snapshot hook already`, func() { k.Snapshot(part) })
	}))
	reg.Register(named("tasks", func(k seam.Kernel) {
		raises(t, `seam.Kernel.TurnSettled: module "tasks"'s TurnSettled hook is nil`, func() { k.TurnSettled(nil) })
		raises(t, `seam.Kernel.Connected: module "tasks"'s Connected hook is nil`, func() { k.Connected(nil) })
		raises(t, `seam.Kernel.Snapshot: module "tasks"'s Snapshot hook is nil`, func() { k.Snapshot(nil) })
		k.TurnSettled(settled) // another module's hook of the same kind is its own
	}))
}

// After Seal no slot takes an entry, and a zero Kernel or Registry is a defect in every slot.
func TestNoSlotTakesAnEntryAfterSeal(t *testing.T) {
	var kept seam.Kernel
	reg := newPorts().registry()
	reg.Register(named("inbox", func(k seam.Kernel) { kept = k }))
	reg.Seal()
	raises(t, "seam.Kernel.Bus: the registry is sealed", func() { kept.Bus(seam.NewBusMethod("inbox.item", echo)) })
	raises(t, "seam.Kernel.TurnSettled: the registry is sealed", func() { kept.TurnSettled(func(context.Context, agentref.Ref) {}) })
	raises(t, "seam.Kernel.Connected: the registry is sealed", func() { kept.Connected(func(context.Context) {}) })
	raises(t, "seam.Kernel.Snapshot: the registry is sealed", func() {
		kept.Snapshot(func(context.Context, *assistantwire.Snapshot) {})
	})
	var k seam.Kernel
	raises(t, "seam.Kernel.Bus: a Kernel comes from Registry.Register", func() { k.Bus(seam.NewBusMethod("inbox.item", echo)) })
	raises(t, "seam.Kernel.TurnSettled: a Kernel comes from Registry.Register", func() {
		k.TurnSettled(func(context.Context, agentref.Ref) {})
	})
	raises(t, "seam.Kernel.Connected: a Kernel comes from Registry.Register", func() { k.Connected(func(context.Context) {}) })
	raises(t, "seam.Kernel.Snapshot: a Kernel comes from Registry.Register", func() {
		k.Snapshot(func(context.Context, *assistantwire.Snapshot) {})
	})
	ctx := context.Background()
	var none *seam.Registry
	raises(t, "seam.Registry.TurnSettled: a Registry comes from NewRegistry", func() { none.TurnSettled(ctx, agent(t, "mac", "s1")) })
	raises(t, "seam.Registry.Connected: a Registry comes from NewRegistry", func() { none.Connected(ctx) })
	raises(t, "seam.Registry.Snapshot: a Registry comes from NewRegistry", func() { none.Snapshot(ctx, &assistantwire.Snapshot{}) })
	raises(t, "seam.Registry.Answer: a Registry comes from NewRegistry", func() { _, _, _ = none.Answer(ctx, "inbox.item", json.RawMessage(`{}`)) })
}
