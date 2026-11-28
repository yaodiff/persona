package assistant

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestPersonaCardsLiveRoundIsolatesQuietAgents(t *testing.T) {
	ft := newFleetTestNoStore(t)
	s := ft.s
	mac, _ := s.fleetMachine("mac")
	at := time.Now().Add(-time.Hour).UTC()
	rows := []proto.Session{
		{SessionID: "persona", Cwd: mac.assistantCwd(), Status: proto.StatusIdle, UpdatedAt: at},
		{SessionID: "worker", Status: proto.StatusIdle, UpdatedAt: at},
		{SessionID: "quiet-side", Status: proto.StatusIdle, UpdatedAt: at},
		{SessionID: "quiet-parent", Status: proto.StatusIdle, UpdatedAt: at},
		{SessionID: "visible-side", Status: proto.StatusWorking, UpdatedAt: at},
		{SessionID: "muted-quiet", SpaceName: "muted", Status: proto.StatusIdle, UpdatedAt: at},
		{SessionID: "held-quiet", Status: proto.StatusIdle, UpdatedAt: at},
	}
	rows[2].SetParentAgentID("worker")
	rows[4].SetParentAgentID("quiet-parent")
	ft.d.setSessions("mac", rows)
	s.fleetTakeSnapshot(Snapshot{Todos: &TodoReport{Quiet: []SessionRef{
		{MachineID: "mac", SessionID: "quiet-side"}, {MachineID: "mac", SessionID: "quiet-parent"},
		{MachineID: "mac", SessionID: "muted-quiet"}, {MachineID: "mac", SessionID: "held-quiet"},
	}}})
	setMutes(s, Pins{Spaces: []string{"mac/muted"}})
	askedAt := at
	add := func(id, session, kind string, delegating bool) {
		askedAt = askedAt.Add(time.Minute)
		putPending(s, &pending{ix: proto.Interaction{ID: id, MachineID: "mac", SessionID: session,
			Kind: kind, Delegating: delegating, CreatedAt: askedAt}, told: true})
	}
	add("ix-persona", "persona", proto.IxPermission, false)
	add("ix-worker", "worker", proto.IxPermission, false)
	add("ix-sessionless", "", proto.IxQuestion, false)

	next := func(n int, want ...assistantwire.CountsCardID) assistantwire.CountsView {
		t.Helper()
		s.liveChanged(false)
		waitUntil(t, func() bool { return len(ft.toldOf(assistantwire.EventCountsView)) >= n })
		views, raw := countsTold(t, ft)
		v := views[n-1]
		count := v.Counts.PersonaCards
		if len(views) != n || v.Rev != n || count == nil || v.Totals == nil || v.Totals.PersonaCards == nil ||
			count.N != len(want) || *v.Totals.PersonaCards != count.N || len(count.Keys) != count.N ||
			count.More != 0 || len(count.Held) != 0 {
			t.Fatalf("round %d, want persona_cards %v: %s", n, want, raw[n-1])
		}
		for i, key := range count.Keys {
			if key.ID != assistantwire.CountsRowKey("card:"+want[i]) ||
				!slices.Equal(key.Cards, []assistantwire.CountsCardID{want[i]}) {
				t.Fatalf("round %d Persona key %d: %+v, want %s", n, i, key, want[i])
			}
		}
		return v
	}

	startAttention(t, s)
	first := next(1, "ix-persona")
	if first.Counts.AgentsNeedsYou.N != 2 {
		t.Fatalf("expected the worker and sessionless card in the Agents count: %+v", first.Counts.AgentsNeedsYou)
	}
	attention := ft.d.handedAttention()[0]
	if len(attention.Items) != 1 || attention.Items[0].InteractionID != "ix-worker" {
		t.Fatalf("the worker should be the sole Persona Link item: %+v", attention)
	}
	live := s.fleetSnapshot()
	if live.Working != 1 || !slices.ContainsFunc(live.Items, func(item FleetItem) bool { return item.ID == "mac/visible-side" }) {
		t.Fatalf("the visible side of a quiet parent should remain a working row of its own: %+v", live)
	}

	add("ix-quiet", "quiet-side", proto.IxPermission, false)
	add("ix-muted", "muted-quiet", proto.IxPermission, false)
	add("ix-held", "held-quiet", proto.IxScreen, true)
	second := next(2, "ix-persona", "ix-quiet")
	if !reflect.DeepEqual(second.Counts.AgentsNeedsYou, first.Counts.AgentsNeedsYou) {
		t.Fatalf("quiet sessions changed Agents keys or held rows: before %+v, after %+v",
			first.Counts.AgentsNeedsYou, second.Counts.AgentsNeedsYou)
	}
	all := ft.d.handedAttention()
	if !reflect.DeepEqual(all[len(all)-1], attention) {
		t.Fatalf("quiet sessions changed Persona Link: before %+v, after %+v", attention, all[len(all)-1])
	}
	if raw := ft.toldOf(assistantwire.EventFleetLive); len(raw) != 1 {
		t.Fatalf("quiet sessions sent another Live Activity state: %s", raw)
	}

	s.fleet.mu.Lock()
	delete(s.fleet.interactions, "ix-quiet")
	s.fleet.mu.Unlock()
	next(3, "ix-persona")
}
