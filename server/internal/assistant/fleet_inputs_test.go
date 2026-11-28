package assistant

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// liveRound is what one of liveLoop's rounds reads and makes of the fleet: every agent's row copied out, the
// interactions, and the state and Persona Link's summary built from them.
type liveRound struct {
	rows      []proto.Session
	machines  []string
	muted     []bool
	started   []time.Time
	ixs       []proto.Interaction
	told      map[string]bool
	state     assistantwire.FleetState
	attention link.Attention
	json      string // state and attention, as they are told
}

func roundOf(t *testing.T, agents []fleetInput, ixs []proto.Interaction, told map[string]bool) liveRound {
	t.Helper()
	r := liveRound{ixs: ixs, told: told, state: buildFleetIn("en", agents, ixs), attention: linkAttention("en", agents, ixs, told)}
	for _, a := range agents {
		r.rows, r.machines, r.muted, r.started = append(r.rows, *a.Session), append(r.machines, a.Machine), append(r.muted, a.Muted), append(r.started, a.TurnStart)
	}
	r.json = roundJSON(t, r)
	return r
}

func roundJSON(t *testing.T, r liveRound) string {
	t.Helper()
	b, err := json.Marshal([]any{r.state, r.attention})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func liveRows(machine string, n int, status string) []proto.Session {
	list := make([]proto.Session, n)
	for i := range list {
		list[i] = proto.Session{SessionID: fmt.Sprintf("%s-s%d", machine, i), Cwd: "/code/app", Agent: "claude", Status: status,
			PaneID: fmt.Sprintf("p%d", i), SpaceName: "app", Title: fmt.Sprintf("Task %d on %s", i, machine),
			LastText: "All tests pass.", UpdatedAt: time.Date(2026, 11, 11, 9, i, 0, 0, time.UTC)}
	}
	return list
}

// A round over the lists the last one kept reads and makes exactly what a round over new copies does, while the
// lists change, shrink, outgrow their arrays and empty, cards come and go, and a machine leaves; the arrays are the
// last round's while the lists fit, rows a shorter list left behind are cleared, and nothing built from an earlier
// round changes when its arrays are written again.
func TestLiveRoundsOverKeptListsAreFreshRounds(t *testing.T) {
	d := newFleetDevs()
	for _, id := range []string{"mac", "box", "far"} {
		d.add(id, "Computer "+id)
	}
	s := New(Options{Hub: newFakeHub(), Logf: func(string, ...any) {}, Devices: d})
	kept := map[string][]proto.Session{}
	var rounds []liveRound
	arrayOf := func(id string) *proto.Session {
		if list := kept[id]; cap(list) > 0 {
			return &list[:1][0]
		}
		return nil
	}
	round := func(what string) {
		t.Helper()
		agents, ixs, told := s.fleetInputsInto(kept)
		got := roundOf(t, agents, ixs, told)
		agents, ixs, told = s.fleetInputs()
		want := roundOf(t, agents, ixs, told)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: over the kept lists\n%+v\nover new copies\n%+v", what, got, want)
		}
		rounds = append(rounds, got)
	}

	working := liveRows("mac", 4, proto.StatusDone)
	working[1].Status = proto.StatusWorking
	d.setSessions("mac", working)
	d.setSessions("box", liveRows("box", 3, proto.StatusIdle))
	d.setSessions("far", liveRows("far", 2, proto.StatusDone))
	round("the first lists")
	if len(rounds[0].rows) != 9 || rounds[0].state.Working != 1 || len(kept) != 3 {
		t.Fatalf("the first round: %d rows, %+v, %d lists kept", len(rounds[0].rows), rounds[0].state, len(kept))
	}
	mac, box := arrayOf("mac"), arrayOf("box")

	card := proto.Interaction{ID: "ix-1", MachineID: "mac", SessionID: "mac-s2", Kind: proto.IxPermission, ToolName: "Bash",
		Summary: "npm test", CreatedAt: time.Now()}
	putPending(s, &pending{ix: card})
	s.markTold(card)
	round("a card")
	if rounds[1].state.NeedsYou != 1 || rounds[1].rows[2].PendingInteractions != 1 || rounds[1].attention.NeedsYou != 1 {
		t.Fatalf("with a card: %+v, %+v", rounds[1].state, rounds[1].attention)
	}
	if arrayOf("mac") != mac || arrayOf("box") != box {
		t.Fatal("a round whose lists fit took new arrays")
	}

	s.fleet.mu.Lock()
	delete(s.fleet.interactions, card.ID)
	s.fleet.mu.Unlock()
	ended := liveRows("mac", 4, proto.StatusDone)
	ended[1].LastText, ended[3].Status = "The retry waits for the write now.", proto.StatusWorking
	d.setSessions("mac", ended)
	round("the card answered and a turn ended")
	if rounds[2].state.NeedsYou != 0 || rounds[2].rows[2].PendingInteractions != 0 {
		t.Fatalf("after the card: %+v", rounds[2].state)
	}

	d.setSessions("box", liveRows("box", 1, proto.StatusIdle))
	d.setSessions("far", liveRows("far", 40, proto.StatusWorking))
	round("a list shrank and another outgrew its array")
	if arrayOf("box") != box || len(kept["box"]) != 1 {
		t.Fatalf("the shorter list: %d rows, a new array %v", len(kept["box"]), arrayOf("box") != box)
	}
	for i, row := range kept["box"][:3][1:] {
		if !reflect.DeepEqual(row, proto.Session{}) {
			t.Errorf("row %d left behind by the shorter list is not cleared: %+v", i+1, row)
		}
	}

	d.setSessions("mac", nil)
	d.mu.Lock()
	delete(d.machines, "far")
	d.order = []string{"mac", "box"}
	d.mu.Unlock()
	round("a list emptied and a machine left")
	if _, ok := kept["far"]; ok || len(kept) != 2 || len(rounds[4].rows) != 1 {
		t.Fatalf("after the machine left: %d lists kept, %d rows", len(kept), len(rounds[4].rows))
	}

	d.setSessions("mac", liveRows("mac", 3, proto.StatusWorking))
	round("the emptied list filled again")
	if arrayOf("mac") != mac {
		t.Fatal("the list that filled again took a new array")
	}

	// What each round made still says what it said: nothing in it points into an array written since.
	for i, r := range rounds {
		if now := roundJSON(t, r); now != r.json {
			t.Errorf("round %d changed after later rounds:\n%s\nwas\n%s", i, now, r.json)
		}
	}
}

// twiceDevs lists its first machine twice, as a list caught between two regroups could.
type twiceDevs struct{ *fleetDevs }

func (d twiceDevs) List() []devices.Summary {
	list := d.fleetDevs.List()
	return append(list, list[0])
}

// A machine listed twice is decorated into the kept array once: its second list is a copy of its own, as it was.
func TestLiveRoundWithAMachineListedTwice(t *testing.T) {
	d := twiceDevs{newFleetDevs()}
	d.add("mac", "Mac")
	d.setSessions("mac", liveRows("mac", 3, proto.StatusWorking))
	s := New(Options{Hub: newFakeHub(), Logf: func(string, ...any) {}, Devices: d})
	kept := map[string][]proto.Session{}
	for range 3 {
		agents, ixs, told := s.fleetInputsInto(kept)
		fresh, fixs, ftold := s.fleetInputs()
		if got, want := roundOf(t, agents, ixs, told), roundOf(t, fresh, fixs, ftold); !reflect.DeepEqual(got, want) || len(agents) != 6 {
			t.Fatalf("over the kept list\n%+v\nover new copies\n%+v", got, want)
		}
		if agents[0].Session == agents[3].Session || agents[0].Session != &kept["mac"][0] {
			t.Fatal("the machine's two lists share an array, or the first is not the kept one")
		}
	}
}

// liveFleet is a manager with machines computers of rows sessions each, one of them working on each: the fleet soak's
// shape.
func liveFleet(tb testing.TB, machines, rows int) *Service {
	tb.Helper()
	d := newFleetDevs()
	for m := range machines {
		id := fmt.Sprintf("soak%02d", m+1)
		d.add(id, "Computer "+id)
		list := make([]proto.Session, rows)
		for i := range list {
			list[i] = proto.Session{SessionID: fmt.Sprintf("s%03d", i), Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
				PaneID: fmt.Sprintf("p%d", i), SpaceName: "app", Title: "a session", LastText: strings.Repeat("the last words of the turn ", 8)}
		}
		list[m%rows].Status = proto.StatusWorking
		d.setSessions(id, list)
	}
	return New(Options{Hub: newFakeHub(), Logf: func(string, ...any) {}, Devices: d})
}

// liveRoundOnce is one of liveLoop's rounds without the telling: the fleet read (over kept, nil: new copies) and the
// state, the summary and the counts view built from it, over the needs-you arrays r kept from its last round, as
// liveLoop's.
func liveRoundOnce(s *Service, kept map[string][]proto.Session, r *fleetRound) (assistantwire.FleetState, link.Attention) {
	var quiet []fleetInput
	agents, ixs, told := s.fleetInputsForRound(kept, &quiet)
	st := r.buildFleet("en", agents, ixs)
	attention, _ := r.linkAndCounts("en", agents, ixs, told, quiet, s.fleet.live.counts.todos.kept())
	return st, attention
}

// BenchmarkLiveRound: one of liveLoop's rounds over 10 computers of 50 sessions, with new copies of the lists
// (way=fresh, how every round was) and over the lists the last round kept (way=kept). Compare the two with
// `benchstat -col /way`.
func BenchmarkLiveRound(b *testing.B) {
	for _, way := range []string{"fresh", "kept"} {
		b.Run("way="+way, func(b *testing.B) {
			s := liveFleet(b, 10, 50)
			var kept map[string][]proto.Session
			if way == "kept" {
				kept = map[string][]proto.Session{}
			}
			var r fleetRound
			if st, _ := liveRoundOnce(s, kept, &r); st.Working != 10 {
				b.Fatalf("the fleet: %+v", st)
			}
			b.ReportAllocs()
			for b.Loop() {
				liveRoundOnce(s, kept, &r)
			}
		})
	}
}

// liveRoundLimit bounds what a round over kept lists allocates for 10 computers of 50 sessions: 79,524 to 80,265
// bytes measured (the agents, the maps a decoration reads, the state and the summary), plus 5%. Over new copies of
// the lists a round was 905,941 to 908,537 bytes. The counting core's own copies of the agents and cards come on top
// (surfaceCopies): each surface's fleetcount.NewAgents and NewCards copy what the round hands them.
const liveRoundLimit = 84000

// A round over the lists the last one kept copies no list into a new array.
func TestLiveRoundKeepsItsLists(t *testing.T) {
	if raceEnabled {
		t.Skip("race instrumentation allocates")
	}
	if !isolated(t) {
		return
	}
	var copies uint64
	measure := func(kept map[string][]proto.Session) uint64 {
		s := liveFleet(t, 10, 50)
		var r fleetRound
		liveRoundOnce(s, kept, &r)
		agents, ixs, _ := s.fleetInputs()
		copies = 2 * surfaceCopies(t, agents, ixs) // the Live Activity's and Persona Link's
		const runs = 100
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for range runs {
			liveRoundOnce(s, kept, &r)
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / runs
	}
	if per := measure(map[string][]proto.Session{}); per > liveRoundLimit+copies {
		t.Errorf("a round over kept lists allocated %d bytes, want at most %d and the core's %d", per, liveRoundLimit, copies)
	}
	if per := measure(nil); per <= liveRoundLimit+copies {
		t.Errorf("a round over new copies allocated %d bytes, want more than %d and the core's %d: the limit no longer tells the two apart",
			per, liveRoundLimit, copies)
	}
}

// A round over the kept lists folds children under their parents as a round over new copies does (decorated folds
// the machine's whole list, foldInventory): the rows carry the same folded_under and children, the live counts leave
// the children out the same way, and the fold kept for rows decorated on their own is the machine's.
func TestLiveRoundsOverKeptListsFoldChildren(t *testing.T) {
	d := newFleetDevs()
	d.add("mac", "Mac")
	rows := liveRows("mac", 4, proto.StatusWorking)
	rows[2].ParentSessionID = rows[0].SessionID
	rows[3].ParentSessionID = rows[0].SessionID
	d.setSessions("mac", rows)
	s := New(Options{Hub: newFakeHub(), Logf: func(string, ...any) {}, Devices: d})
	kept := map[string][]proto.Session{}
	for i := range 2 { // the second round decorates into the array the first one kept
		s.fleet.foldMu.Lock()
		s.fleet.folds = nil
		s.fleet.foldMu.Unlock()
		agents, ixs, told := s.fleetInputsInto(kept)
		got := roundOf(t, agents, ixs, told)
		s.fleet.foldMu.Lock()
		keptFold := s.fleet.folds["mac"]
		s.fleet.foldMu.Unlock()
		fresh, fixs, ftold := s.fleetInputs()
		want := roundOf(t, fresh, fixs, ftold)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d: over the kept list\n%+v\nover new copies\n%+v", i, got, want)
		}
		if kept["mac"][2].FoldedUnder() != rows[0].SessionID || kept["mac"][0].Children().Total != 2 {
			t.Fatalf("round %d: the kept list is not folded: %+v", i, kept["mac"])
		}
		s.fleet.foldMu.Lock()
		freshFold := s.fleet.folds["mac"]
		s.fleet.foldMu.Unlock()
		if !reflect.DeepEqual(keptFold, freshFold) {
			t.Fatalf("round %d: the fold kept for the machine\n%+v\nwant\n%+v", i, keptFold, freshFold)
		}
	}
}
