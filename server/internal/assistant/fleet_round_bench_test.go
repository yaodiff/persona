package assistant

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	kitoutcome "github.com/nwverse/persona/server/internal/domainkit/outcome" // the package's tests have an outcome helper
	"github.com/nwverse/persona/server/internal/proto"
)

// The copies the core makes, kept where the compiler cannot drop them.
var (
	keptAgents fleetcount.Agents
	keptCards  fleetcount.Cards
)

// roundFleet is the needs-you gate's fleet (docs/plans/plan-derived-counts.md, step 5): 60 agents on two computers, a
// third each waiting for the user, working and finished unseen, and 12 pending cards on the waiting ones (two of
// them with two cards, some told, one a question the assistant is still deciding on, one an MCP form).
func roundFleet() ([]fleetInput, []proto.Interaction, map[string]bool) {
	base := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	states := []string{proto.StateNeedsYou, proto.StateWorking, proto.StateUnread}
	agents := make([]fleetInput, 0, 60)
	for i := range 60 {
		machine := fmt.Sprintf("mac%d", i%2)
		s := &proto.Session{MachineID: machine, SessionID: fmt.Sprintf("s%02d", i), Agent: "claude", Cwd: "/code/app",
			SpaceName: "app", Title: fmt.Sprintf("Task %d", i), LastText: "All tests pass.", State: states[i%3],
			UpdatedAt: base.Add(time.Duration(i) * time.Minute), StateSince: base.Add(time.Duration(i) * time.Minute)}
		agents = append(agents, fleetInput{Machine: "Computer " + machine, Session: s, Muted: i%17 == 0,
			TurnStart: base.Add(time.Duration(i) * time.Minute)})
	}
	kinds := []string{proto.IxPermission, proto.IxQuestion, proto.IxPlan, proto.IxScreen}
	ixs := make([]proto.Interaction, 0, 12)
	told := map[string]bool{}
	for i := range 12 {
		a := agents[(i%10)*3].Session // the waiting ones; the first two get a second card
		ix := proto.Interaction{ID: fmt.Sprintf("ix%02d", i), MachineID: a.MachineID, SessionID: a.SessionID,
			Kind: kinds[i%len(kinds)], CreatedAt: base.Add(time.Duration(i) * time.Minute), Summary: "Run the tests",
			ToolName: "Bash"}
		switch i {
		case 4:
			ix.Kind, ix.Delegating = proto.IxQuestion, true
		case 7:
			ix.Kind = proto.IxElicitation
		}
		if i%2 == 0 {
			told[ix.ID] = true
		}
		ixs = append(ixs, ix)
	}
	return agents, ixs, told
}

// BenchmarkRound is one live round's needs-you work on roundFleet, as liveLoop does it over the arrays its last round
// kept (round): the Live Activity's state, and Persona Link's summary and the counts view built from one fill of the
// agents and cards, with the to-dos counted (roundTodos). allocs/op is the gate (docs/plans/plan-derived-counts.md,
// steps 5 and 7a); B/op may rise only by the counting core's own copies of the agents and cards, which core-copies
// makes for both fills (fleetcount.NewAgents and NewCards, twice: the summary and the view share theirs). counts-tell
// is the view's telling in a round whose counts did not change, which is most rounds.
func BenchmarkRound(b *testing.B) {
	agents, ixs, told := roundFleet()
	todos := roundTodos()
	b.Run("round", func(b *testing.B) {
		var r fleetRound
		if st := r.buildFleet("en", agents, ixs); st.NeedsYou == 0 || st.Working == 0 || st.Unread == 0 {
			b.Fatalf("the fleet: %+v", st)
		}
		if a, v := r.linkAndCounts("en", agents, ixs, told, nil, todos); a.NeedsYou == 0 || v.Counts.AgentsNeedsYou.N == 0 ||
			v.Counts.TodosNeedsYou == nil || v.Counts.PersonaCards == nil {
			b.Fatalf("the summary: %+v; the view: %+v", a, v.Counts)
		}
		b.ReportAllocs()
		for b.Loop() {
			r.buildFleet("en", agents, ixs)
			r.linkAndCounts("en", agents, ixs, told, nil, todos)
		}
	})
	b.Run("counts-tell", func(b *testing.B) {
		s := New(Options{Hub: newFakeHub(), Logf: func(string, ...any) {}, Devices: newFleetDevs()})
		var r fleetRound
		_, v := r.linkAndCounts("en", agents, ixs, told, nil, todos)
		s.tellCounts(v)
		b.ReportAllocs()
		for b.Loop() {
			s.tellCounts(v)
		}
	})
	b.Run("core-copies", func(b *testing.B) {
		list, cards := coreValues(b, agents, ixs)
		b.ReportAllocs()
		for b.Loop() {
			for range 2 {
				keptAgents, keptCards = fleetcount.NewAgents(list), fleetcount.NewCards(cards)
			}
		}
	})
}

// roundTodos is the to-do list's counts a round's view carries once the manager has counted its to-dos.
func roundTodos() opt.Option[assistantwire.TodoCounts] {
	return opt.Some(assistantwire.TodoCounts{Open: 9, NeedsYou: 3, InProgress: 4, Sections: map[string]int{
		assistantwire.TodoListSectionNeedsYou: 3, assistantwire.TodoListSectionWorking: 4}})
}

// coreValues is a round's agents and cards as fleetcount's values, built through its adapter.
func coreValues(tb testing.TB, agents []fleetInput, ixs []proto.Interaction) ([]fleetcount.Agent, []fleetcount.Card) {
	tb.Helper()
	list := make([]fleetcount.Agent, 0, len(agents))
	for i := range agents {
		a, failed := kitoutcome.Unpack(fleetcountadapt.Agent(agents[i].Session, agents[i].Muted, i))
		if failed != nil {
			tb.Logf("agent %d: %s", i, fleetcountadapt.Log(failed))
			tb.FailNow()
		}
		list = append(list, a)
	}
	cards := make([]fleetcount.Card, 0, len(ixs))
	for i := range ixs {
		c, failed := kitoutcome.Unpack(fleetcountadapt.Card(&ixs[i], false, i))
		if failed != nil {
			tb.Logf("card %d: %s", i, fleetcountadapt.Log(failed))
			tb.FailNow()
		}
		cards = append(cards, c)
	}
	return list, cards
}

// surfaceCopies is what the counting core's own copies of a round's agents and cards take on one surface, in bytes:
// fleetcount.NewAgents and NewCards copy what they are handed, so that the core owns its arrays.
func surfaceCopies(tb testing.TB, agents []fleetInput, ixs []proto.Interaction) uint64 {
	tb.Helper()
	list, cards := coreValues(tb, agents, ixs)
	const runs = 20
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		keptAgents, keptCards = fleetcount.NewAgents(list), fleetcount.NewCards(cards)
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / runs
}
