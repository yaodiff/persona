package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestFleetReconnectPreservesAnUnansweredAskUser(t *testing.T) {
	ft, _ := newHookedFleet(t)
	ft.s.fleet.started.Do(func() {}) // the reconnect itself is the only fleet callback this test drives
	ctx, cancel := context.WithCancel(t.Context())
	var answer string
	var toolErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		answer, toolErr = ft.s.toolAskUser(ctx, "mac", "conversation", []byte(`{"why_owner":"priority","questions":[{"question":"Which?","options":[{"label":"A"},{"label":"B"}]}]}`))
	}()
	defer func() { cancel(); <-done }()
	ix := ft.waitPending(1)[0]
	ft.s.fleet.askMu.Lock()
	original := ft.s.fleet.asked[ix.ID]
	ft.s.fleet.askMu.Unlock()
	if original == nil {
		t.Fatal("the original ask_user was not waiting")
	}
	ft.s.FleetUp(ctx)
	ft.s.fleet.askMu.Lock()
	current := ft.s.fleet.asked[ix.ID]
	ft.s.fleet.askMu.Unlock()
	if current != original {
		t.Fatal("reconnect withdrew the live ask_user while restoring review cards")
	}
	if err := ft.s.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"Which?": "A"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if toolErr != nil || !strings.Contains(answer, "Which? → A") {
			t.Fatalf("the original caller got %q, %v", answer, toolErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached the original caller")
	}
	ft.s.FleetUp(ctx)
	ft.waitPending(0)
}

func TestReconnectAndFullSnapshotWithdrawOnlyTheCardsTheyCover(t *testing.T) {
	ft, _ := newHookedFleet(t)
	ft.s.fleet.started.Do(func() {})
	review, ordinary := false, false
	ft.s.fleet.askMu.Lock()
	ft.s.fleet.asked = map[string]*askedOne{
		sessionReviewPrefix + "removed": {cancel: func() { review = true }},
		"work-question":                 {cancel: func() { ordinary = true }},
	}
	ft.s.fleet.askMu.Unlock()
	ft.s.FleetUp(t.Context()) // the store knows of no pending session review
	if !review || ordinary {
		t.Fatalf("review-only refresh cancelled review=%t ordinary=%t", review, ordinary)
	}
	ft.s.askOnPhone(nil) // the full snapshot also withdrew the work question
	if !ordinary {
		t.Fatal("a full snapshot left the withdrawn question running")
	}
}

func TestReconnectInventoryOrdersConcurrentCardChanges(t *testing.T) {
	for _, withdrawing := range []bool{false, true} {
		name := "new card"
		if withdrawing {
			name = "withdrawn card"
		}
		t.Run(name, func(t *testing.T) {
			ft, wire := newHookedFleet(t)
			// This test controls the reconnect write itself. It needs no polling or expiry loops.
			ft.s.fleet.started.Do(func() {})
			ix := proto.Interaction{ID: "reconnect", Kind: proto.IxPermission, MachineID: "mac", SessionID: "s1", CreatedAt: time.Now()}
			if withdrawing {
				ft.hold(ix)
			}
			captured, release := make(chan struct{}), make(chan struct{})
			wire.hook = func(event string, params any) {
				if event == assistantwire.EventFleetInteractions {
					close(captured)
					<-release
				}
			}
			up, changed := make(chan struct{}), make(chan struct{})
			go func() { ft.s.FleetUp(context.Background()); close(up) }()
			<-captured
			started := make(chan struct{})
			go func() {
				close(started)
				if withdrawing {
					ft.s.finishInteraction(ix.ID)
				} else {
					ft.hold(ix)
					ft.s.tellCard(ix)
				}
				close(changed)
			}()
			<-started
			waitFor(t, "the pending set to change during reconnect", func() bool {
				return (len(ft.s.pendingInteractions()) == 0) == withdrawing
			})
			// Let an unsequenced change reach the transport while the snapshot is still in flight.
			select {
			case <-changed:
			case <-time.After(150 * time.Millisecond):
			}
			close(release)
			<-up
			<-changed
			want := assistantwire.EventFleetInteraction
			if withdrawing {
				want = assistantwire.EventFleetResolved
			}
			var order []string
			ft.f.mu.Lock()
			for _, tell := range ft.f.told {
				if tell.event == assistantwire.EventFleetInteractions || tell.event == want {
					order = append(order, tell.event)
				}
			}
			ft.f.mu.Unlock()
			if len(order) != 2 || order[0] != assistantwire.EventFleetInteractions || order[1] != want {
				t.Fatalf("the reconnect inventory overtook the card change: %v", order)
			}
		})
	}
}

type delayedStartupStore struct {
	*restoreMem
	scanning  chan struct{}
	release   chan struct{}
	persisted chan struct{}
}

func (s *delayedStartupStore) ExpireStaleInteractions(ctx context.Context) ([]proto.Interaction, []proto.Interaction, error) {
	close(s.scanning)
	<-s.release
	return s.restoreMem.ExpireStaleInteractions(ctx)
}

func (s *delayedStartupStore) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	err := s.fleetMem.InsertInteraction(ctx, ix)
	close(s.persisted)
	return err
}

func TestFirstInteractionCannotBeExpiredByStartup(t *testing.T) {
	store := &delayedStartupStore{restoreMem: &restoreMem{newFleetMem()}, scanning: make(chan struct{}), release: make(chan struct{}), persisted: make(chan struct{})}
	hub := newFakeHub()
	devices := newFleetDevs()
	devices.add("mac", "Mac")
	svc := New(Options{Hub: hub, Devices: devices, Store: func() Store { return store }, Logf: testLogf(t)})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	old := proto.Interaction{ID: "left-by-previous-process", MachineID: "mac", SessionID: "old", Kind: proto.IxQuestion}
	if err := store.fleetMem.InsertInteraction(ctx, old); err != nil {
		t.Fatal(err)
	}
	restored := make(chan struct{})
	go func() { svc.restoreInteractions(ctx); close(restored) }()
	<-store.scanning
	card := proto.Interaction{ID: "new-process-card", MachineID: "mac", SessionID: "new", Kind: proto.IxPermission, OutwardWrite: true}
	done := make(chan struct{})
	go func() { _, _ = svc.waitInteraction(ctx, card); close(done) }()
	select {
	case <-store.persisted:
	case <-time.After(100 * time.Millisecond):
	}
	close(store.release)
	<-restored
	select {
	case <-store.persisted:
	case <-time.After(5 * time.Second):
		t.Fatal("waiting card did not reach storage after startup")
	}
	store.fmu.Lock()
	oldState, newState := store.status[old.ID], store.status[card.ID]
	store.fmu.Unlock()
	cancel()
	<-done
	if oldState != "cancelled" || newState != "pending" {
		t.Fatalf("startup statuses: old=%s, new=%s", oldState, newState)
	}
}
