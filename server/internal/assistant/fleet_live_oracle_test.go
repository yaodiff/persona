package assistant

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount/fleetcountadapt"
	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// The fleet as it was worked out before fleetInput held a pointer to its session and the items were made only for
// what is shown (BenchmarkFleetSnapshot): kept here as the oracle the random fleets below are checked against.

type oracleInput struct {
	Machine   string
	Session   proto.Session
	Muted     bool
	TurnStart time.Time
}

type oracleNeeder struct {
	agent oracleInput
	card  *proto.Interaction
}

func oracleNeedsYou(agents []oracleInput, ixs []proto.Interaction, counts func(oracleInput) bool, stands func(proto.Interaction) bool) []oracleNeeder {
	byKey := map[string]oracleInput{}
	for _, a := range agents {
		if counts(a) {
			byKey[a.Session.MachineID+"/"+a.Session.SessionID] = a
		}
	}
	// An agent every pending card of which its task's supervisor holds (delegating, not a question) is not the user's.
	held := map[string]bool{}
	for _, ix := range ixs {
		key := ix.MachineID + "/" + ix.SessionID
		if _, seen := held[key]; !seen {
			held[key] = true
		}
		if !ix.Delegating || ix.Kind == proto.IxQuestion {
			held[key] = false
		}
	}
	var out []oracleNeeder
	asked := map[string]bool{}
	for _, ix := range ixs {
		key := ix.MachineID + "/" + ix.SessionID
		if a, ok := byKey[key]; ok && !asked[key] && !held[key] && stands(ix) {
			asked[key] = true
			out = append(out, oracleNeeder{agent: a, card: &ix})
		}
	}
	for _, a := range agents {
		key := a.Session.MachineID + "/" + a.Session.SessionID
		if counts(a) && !asked[key] && !held[key] && a.Session.State == proto.StateNeedsYou && !a.Session.Assistant {
			out = append(out, oracleNeeder{agent: a})
		}
	}
	return out
}

func oracleLiveCounts(a oracleInput) bool { return !a.Session.IsCodexSubagent() && !a.Muted }

func oracleBuildFleetIn(l i18n.Lang, agents []oracleInput, ixs []proto.Interaction) FleetState {
	var st FleetState
	var needs, working, unread []FleetItem
	asked := map[string]bool{}
	for _, n := range oracleNeedsYou(agents, ixs, oracleLiveCounts, cardStands) {
		sess := n.agent.Session
		key := sess.MachineID + "/" + sess.SessionID
		asked[key] = true
		item := FleetItem{ID: key, Title: agentTitle(&sess, fleetTitleMax), Place: agentPlace(n.agent.Machine, &sess, fleetPlaceMax),
			Kind: proto.IxScreen, Detail: i18n.T(l, "hub-liveactivity.screen")}
		if ix := n.card; ix != nil {
			item.Kind, item.Detail = ix.Kind, clip(oneLine(interactionDetail(l, *ix)), fleetDetailMax)
			item.Interaction, item.Since = ix.ID, unixSeconds(ix.CreatedAt)
			if !ix.CreatedAt.IsZero() {
				item.InteractionCreatedAt = ix.CreatedAt.UTC().Format(time.RFC3339Nano)
			}
			// #12982: no buttons for a permission without a summary or a plan without a title.
			blind := ix.Kind == proto.IxPermission && strings.TrimSpace(ix.Summary) == "" ||
				ix.Kind == proto.IxPlan && strings.TrimSpace(planTitle(ix.Plan)) == ""
			if len(ix.Fields) > 0 || blind {
				item.Interaction = ""
				item.InteractionCreatedAt = ""
			}
		}
		needs = append(needs, item)
	}
	for _, a := range agents {
		sess := a.Session
		key := sess.MachineID + "/" + sess.SessionID
		if !oracleLiveCounts(a) || asked[key] {
			continue
		}
		switch sess.State {
		case proto.StateWorking:
			if sess.Assistant {
				st.Assistant = true
				continue
			}
			since := a.TurnStart
			if sess.BackgroundWorking {
				var started time.Time
				for _, w := range sess.Workflows {
					if w.Running && !w.StartedAt.IsZero() && (started.IsZero() || w.StartedAt.Before(started)) {
						started = w.StartedAt
					}
				}
				if !started.IsZero() {
					since = started
				}
			}
			if since.IsZero() {
				since = sess.UpdatedAt
			}
			it := FleetItem{ID: key, Title: agentTitle(&sess, fleetTitleMax), Place: agentPlace(a.Machine, &sess, fleetPlaceMax), Kind: "working",
				Since: unixSeconds(since)}
			if sess.BackgroundWorking {
				it.Background, it.Detail = true, backgroundDetail(l, &sess)
				it.Progress = backgroundProgress(&sess)
			}
			working = append(working, it)
		case proto.StateUnread:
			if sess.Assistant || sess.Loop != nil {
				continue
			}
			unread = append(unread, FleetItem{ID: key, Title: agentTitle(&sess, fleetTitleMax), Place: agentPlace(a.Machine, &sess, fleetPlaceMax), Kind: "unread",
				Detail: clip(oneLine(firstLine(sess.LastText)), fleetDetailMax), Since: unixSeconds(sess.UpdatedAt)})
		}
	}
	sort.SliceStable(working, func(i, j int) bool { return working[i].Since < working[j].Since })
	sort.SliceStable(unread, func(i, j int) bool { return unread[i].Since > unread[j].Since })
	st.NeedsYou, st.Working, st.Unread = len(needs), len(working), len(unread)
	switch {
	case st.NeedsYou > 0:
		st.Phase = "needsYou"
	case st.Working > 0:
		st.Phase = "working"
	case st.Unread > 0:
		st.Phase = "done"
	}
	if len(working) > 0 {
		st.WorkingSince = working[0].Since
	}
	st.Items = append(append(append([]FleetItem{}, needs...), working...), unread...)
	if len(st.Items) > fleetMaxItems {
		st.Items = st.Items[:fleetMaxItems]
	}
	return st
}

func oracleLinkAttention(l i18n.Lang, agents []oracleInput, ixs []proto.Interaction, told map[string]bool) link.Attention {
	key := func(machine, session string) string { return machine + "/" + session }
	handed := func(ix proto.Interaction) bool { return told[ix.ID] && !ix.Delegating }
	ready := map[string]bool{}
	for _, ix := range ixs {
		if handed(ix) {
			ready[key(ix.MachineID, ix.SessionID)] = true
		}
	}
	cards := slices.Clone(ixs)
	slices.SortStableFunc(cards, func(a, b proto.Interaction) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	counts := func(a oracleInput) bool {
		return !a.Session.Assistant && !a.Session.Quiet && a.Session.State == proto.StateNeedsYou
	}
	stands := func(ix proto.Interaction) bool {
		return cardStands(ix) && (handed(ix) || !ready[key(ix.MachineID, ix.SessionID)])
	}
	needs := oracleNeedsYou(agents, cards, counts, stands)
	slices.SortStableFunc(needs, func(a, b oracleNeeder) int {
		return cmp.Or(oracleWaitingSince(b).Compare(oracleWaitingSince(a)), cmp.Compare(a.agent.Session.MachineID, b.agent.Session.MachineID),
			cmp.Compare(a.agent.Session.SessionID, b.agent.Session.SessionID))
	})
	out := link.Attention{NeedsYou: len(needs), Items: []link.AttentionItem{}}
	for i, n := range needs {
		if i == link.AttentionMaxItems {
			out.More = len(needs) - i
			break
		}
		sess := n.agent.Session
		it := link.AttentionItem{MachineID: sess.MachineID, SessionID: sess.SessionID, Title: fit(agentTitle(&sess, linkTextMax), linkTextMax),
			Place: fit(agentPlace(n.agent.Machine, &sess, linkTextMax), linkTextMax), Kind: proto.IxScreen,
			Detail: i18n.T(l, "hub-liveactivity.screen"), Muted: n.agent.Muted, Route: sessionPath(sess.MachineID, sess.SessionID, false)}
		if ix := n.card; ix != nil {
			it.Kind, it.Detail, it.InteractionID = ix.Kind, fit(oneLine(interactionDetail(l, *ix)), linkDetailMax), ix.ID
		}
		if since := oracleWaitingSince(n); !since.IsZero() {
			it.Since = since.UTC().Truncate(time.Second).Format(time.RFC3339)
		}
		it.Notify = !n.agent.Muted && ready[key(it.MachineID, it.SessionID)]
		out.Items = append(out.Items, it)
	}
	return out
}

func oracleWaitingSince(n oracleNeeder) time.Time {
	switch {
	case n.card != nil:
		return n.card.CreatedAt
	case !n.agent.Session.StateSince.IsZero():
		return n.agent.Session.StateSince
	}
	return n.agent.Session.UpdatedAt
}

// randomFleet is a fleet of a few machines whose agents are in every state the fleet tells apart, with cards on some
// of them (and on agents it does not list), some told, some delegating.
func randomFleet(rng *rand.Rand) ([]fleetInput, []proto.Interaction, map[string]bool) {
	base := time.Unix(1700000000, 0)
	at := func() time.Time {
		if rng.IntN(8) == 0 {
			return time.Time{}
		}
		return base.Add(time.Duration(rng.IntN(40)) * time.Second) // ties on purpose: the sorts must stay stable
	}
	pick := func(s ...string) string { return s[rng.IntN(len(s))] }
	var agents []fleetInput
	for m := range 1 + rng.IntN(3) {
		mid := fmt.Sprintf("m%d", m)
		for i := range rng.IntN(12) + rng.IntN(2)*rng.IntN(60) { // past Persona Link's cap now and then
			s := &proto.Session{MachineID: mid, SessionID: fmt.Sprintf("s%d", rng.IntN(10)+i/2*10), // now and then listed twice
				State:     pick(proto.StateNeedsYou, proto.StateWorking, proto.StateUnread, proto.StateIdle, proto.StateWorking),
				Title:     pick("", "Claude Code", "Fix the build", "a very long title that goes on and on past every limit", "two\nlines"),
				Cwd:       pick("/code/app", "/code/web", ""),
				SpaceName: pick("", "app", "web"), SlotName: pick("", "2", "tests"), SlotIndex: int32(rng.IntN(3)),
				LastText:  pick("", "done", "first line\nsecond", "a long reply that is clipped somewhere after sixty runes, really quite long"),
				Assistant: rng.IntN(8) == 0, Quiet: rng.IntN(10) == 0, UpdatedAt: at(), StateSince: at(),
				Agent: pick("claude", "codex"), IsSubagent: rng.IntN(6) == 0, BackgroundWorking: rng.IntN(4) == 0}
			if rng.IntN(6) == 0 {
				s.Loop = &proto.LoopInfo{}
			}
			if s.BackgroundWorking {
				for range rng.IntN(3) {
					s.Workflows = append(s.Workflows, proto.Workflow{Name: pick("", "parity-audit"), Phase: pick("", "Verify"),
						Running: rng.IntN(3) > 0, StartedAt: at(), AgentsStarted: rng.IntN(5), AgentsDone: rng.IntN(5) - 1})
				}
				for range rng.IntN(3) {
					s.Subagents = append(s.Subagents, proto.Subagent{Running: rng.IntN(2) == 0})
				}
				for range rng.IntN(2) {
					s.Background = append(s.Background, proto.BackgroundTask{})
				}
			}
			agents = append(agents, fleetInput{Machine: pick("Mac", "Box"), Session: s, Muted: rng.IntN(6) == 0, TurnStart: at()})
		}
	}
	var ixs []proto.Interaction
	told := map[string]bool{}
	for i := range rng.IntN(10) {
		ix := proto.Interaction{ID: fmt.Sprintf("ix%d", rng.IntN(20)), Kind: pick(proto.IxPermission, proto.IxPlan, proto.IxQuestion, proto.IxScreen, proto.IxElicitation),
			CreatedAt: at(), Summary: pick("", "Run make"), ToolName: pick("", "Bash"), Delegating: rng.IntN(5) == 0}
		if len(agents) > 0 && rng.IntN(5) > 0 {
			a := agents[rng.IntN(len(agents))].Session
			ix.MachineID, ix.SessionID = a.MachineID, a.SessionID
		} else {
			ix.MachineID, ix.SessionID = "m9", fmt.Sprintf("gone%d", i)
		}
		if rng.IntN(6) == 0 {
			ix.Fields = []proto.Field{{}}
		}
		if ix.Kind == proto.IxQuestion && rng.IntN(2) == 0 {
			ix.Questions = []proto.Question{{Question: "Which one?"}}
		}
		if rng.IntN(2) == 0 {
			told[ix.ID] = true
		}
		ixs = append(ixs, ix)
	}
	return agents, ixs, told
}

// The fleet and Persona Link's summary are what they were before the agents were read by pointer and only the shown
// items were made, byte for byte, for random fleets in each language. The agents are the ones fleetInputsInto hands
// over, without quiet tasks' and directory-hidden ones (a quiet task's cards stay, as visiblePendingCards keeps them):
// fleetcount leaves those agents off the Live Activity, whose rule the working rows follow too, where the oracle
// counted every agent it was handed.
func TestFleetMatchesOracle(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for round := range 1500 {
		agents, ixs, told := randomFleet(rng)
		agents = slices.DeleteFunc(agents, func(a fleetInput) bool { return fleetcountadapt.Visibility(a.Session).LeftOut() })
		old := make([]oracleInput, len(agents))
		for i, a := range agents {
			old[i] = oracleInput{Machine: a.Machine, Session: *a.Session, Muted: a.Muted, TurnStart: a.TurnStart}
		}
		l := i18n.Langs[round%len(i18n.Langs)]
		got, _ := json.Marshal(buildFleetIn(l, agents, ixs))
		want, _ := json.Marshal(oracleBuildFleetIn(l, old, ixs))
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d fleet:\n got %s\nwant %s", round, got, want)
		}
		got, _ = json.Marshal(linkAttention(l, agents, ixs, told))
		want, _ = json.Marshal(oracleLinkAttention(l, old, ixs, told))
		if !bytes.Equal(got, want) {
			t.Fatalf("round %d attention:\n got %s\nwant %s", round, got, want)
		}
	}
}

// The oracle's names for what it shares with the fleet, now that the rule a form stands for no agent is fleetcount's
// and the text is fleetview's: the oracle keeps the words it was written in.
func cardStands(ix proto.Interaction) bool { return ix.Kind != proto.IxElicitation }

func agentTitle(sess *proto.Session, n int) string {
	return fleetview.AgentTitle(sessionTitle(sess), n)
}

var (
	agentPlace        = fleetview.AgentPlace
	interactionDetail = fleetview.InteractionDetail
	unixSeconds       = fleetview.UnixSeconds
	fit               = fleetview.Fit
)
