package node

import (
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Node-side authority for the orphaned Antigravity companion id (issue #16091): a to-do can keep
// agy-<conversation-uuid> after the companion restarts, and session.stop must answer typed not_found
// only when the computer has no pane or runtime trace of that conversation. A raw per-pane scan keeps
// the two-panes-one-conversation alias omission fail-closed.

const agyCompanionStopID = "agy-" + agyTestID

// A companion id with no pane, runtime session or presence lock is authoritatively not_found, not the
// old empty-code "invalid session id": the hub's continues takeover reads that code as idempotent gone
// and releases the stale to-do link.
func TestAgyCompanionStopOrphanAnswersNotFound(t *testing.T) {
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	listSessions(t, n)

	res, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != codeNotFound {
		t.Fatalf("orphan companion stop code = %q err=%v, want %s", code, err, codeNotFound)
	}
	if stopped, _ := res["stopped"].(bool); stopped {
		t.Fatalf("stopped = true for a session the computer does not have: %v", res)
	}
	if keys := f.trace("pane.send_keys"); len(keys) != 0 {
		t.Fatalf("an orphan must not touch any pane: %v", keys)
	}

	// retire maps the ordinary ownership refusals to not_retirable, but the authoritative not_found
	// stays not_found so the hub can still settle the link.
	_, err = request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID, "retire": true})
	if code, _ := refusalOf(t, err); code != codeNotFound {
		t.Fatalf("retire orphan code = %q, want %s", code, codeNotFound)
	}
	_ = home
}

// Exactly one pane raw-reports the conversation: the companion id is routed to that pane's row and the
// ordinary Ctrl+C stop succeeds, so a genuinely live Agent is stopped normally instead of being let go.
func TestAgyCompanionStopUniquePaneQuitsIt(t *testing.T) {
	withStop(t, 1, 2*time.Second)
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	listSessions(t, n)
	f.reset()

	gone := shellPane(agyTestPane, "w1:t1", "w1", home, "")
	quitsOn(f, agyTestPane, agyTestRow, nil, gone)
	res, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if err != nil || res["stopped"] != true {
		t.Fatalf("companion id routed to its pane: stop = %v, %v", res, err)
	}
	if got := f.trace("pane.send_keys"); len(got) != 2 || got[0] != sendKeys(agyTestPane, "ctrl+c") || got[1] != sendKeys(agyTestPane, "ctrl+c") {
		t.Fatalf("keys %v, want two Ctrl+C in %s", got, agyTestPane)
	}
	closesNothing(t, f)
}

// Two panes reporting the same conversation make nameAgyAliases omit the companion alias from both
// rows. The raw scan still counts both, so the node refuses hard instead of certifying not_found.
func TestAgyCompanionStopTwoPanesStayHard(t *testing.T) {
	n, f, home := herdrNode(t)
	const secondPane = "w1:p2"
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		agyTestAgent(agyTestPane, home, agyTestID),
		agyTestAgent(secondPane, home, agyTestID),
	}, nil))
	listSessions(t, n)

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code == codeNotFound {
		t.Fatalf("two panes one conversation must never answer not_found, got %v", err)
	} else if code != codeAgyInTerminal {
		t.Fatalf("two panes code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}
	if keys := f.trace("pane.send_keys"); len(keys) != 0 {
		t.Fatalf("an ambiguous companion stop must not press keys: %v", keys)
	}

	// The attribution primitive itself keeps both panes (the mutation target behind this guarantee).
	rowID, got := n.agyCompanionStopAttribution(agyTestID)
	if rowID != "" || !got.ambiguous {
		t.Fatalf("attribution = row %q %+v, want ambiguous with no single row", rowID, got)
	}
}

// A companion runtime that still lists the id at an ACTIVE status owns it: the node must not call it
// not_found. The ordinary stoppable ownership answer (no_terminal; not_retirable for retire) is preserved.
func TestAgyCompanionStopRuntimeOwnedStaysHard(t *testing.T) {
	n, _, home := herdrNode(t)
	n.runtimes = newRuntimeRegistry()
	rt := n.runtimes.add(nil)
	rt.ready, rt.hello.Runtime = true, agyAgent
	n.runtimes.setSessions(rt, []proto.Session{
		{SessionID: agyCompanionStopID, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking},
	})

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != noTerminal {
		t.Fatalf("runtime-owned stop code = %q, want %s: %v", code, noTerminal, err)
	}
	_, err = request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID, "retire": true})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("runtime-owned retire code = %q, want %s: %v", code, notRetirable, err)
	}
}

// A companion that still lists the conversation only at an at-rest status is not running an Agent: the
// stopped exact row regression must still reach authoritative not_found instead of a hard no_terminal.
func TestAgyCompanionStopStoppedRuntimeRowAllowsNotFound(t *testing.T) {
	withAgyPresenceHooks(t, func(string) agyLockState { return agyLockMissing }, nil)
	for _, status := range []string{proto.StatusDone, proto.StatusIdle, proto.StatusOffline} {
		t.Run(status, func(t *testing.T) {
			n, _, home := herdrNode(t)
			n.runtimes = newRuntimeRegistry()
			rt := n.runtimes.add(nil)
			rt.ready, rt.hello.Runtime = true, agyAgent
			n.runtimes.setSessions(rt, []proto.Session{
				{SessionID: agyCompanionStopID, Agent: agyAgent, Cwd: home, Status: status},
			})
			_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
			if code, _ := refusalOf(t, err); code != codeNotFound {
				t.Fatalf("runtime row %s: code=%q err=%v, want %s", status, code, err, codeNotFound)
			}
		})
	}
}

// A held presence lock says an agy the node cannot place may have the conversation open: hard refusal,
// never not_found.
func TestAgyCompanionStopHeldPresenceStaysHard(t *testing.T) {
	withAgyPresenceHooks(t, func(string) agyLockState { return agyLockHeld }, nil)
	n, _, _ := herdrNode(t)

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("held presence code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}
}

// A fresh hook report naming the conversation for a pane no row attributes yet blocks not_found: the
// snapshot can simply lag the hook.
func TestAgyCompanionStopFreshHookReportStaysHard(t *testing.T) {
	n, _, _ := herdrNode(t)
	n.agyReports = map[string]agyHookReport{
		"w1:p9": {conversation: agyTestID, at: time.Now()},
	}

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("fresh hook code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}

	// Past the grace window the stale report no longer vetoes: authoritative not_found again.
	n.agyReports["w1:p9"] = agyHookReport{conversation: agyTestID, at: time.Now().Add(-agyReportGrace - time.Second)}
	_, err = request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != codeNotFound {
		t.Fatalf("stale hook code = %q, want %s: %v", code, codeNotFound, err)
	}
}

// Ids outside the companion shape pass through untouched so every existing stop path is byte-for-byte
// the same call.
func TestResolveAgyCompanionStopPassesOtherIDs(t *testing.T) {
	n := &Node{}
	for _, id := range []string{agyTestRow, "agent-live-hex-id", "11111111-2222-4333-8444-555555555555", ""} {
		got, err := n.resolveAgyCompanionStop(id, false)
		if err != nil || got != id {
			t.Fatalf("resolve(%q) = %q, %v; want %q unchanged", id, got, err, id)
		}
	}
}

// The post-lock recheck keeps the routed row only while it stays the conversation's unique attribution:
// a twin or a moved pane refuses hard; the same row or a pane that simply exited stays allowed.
func TestAgyCompanionStopRecheck(t *testing.T) {
	n := &Node{}
	agentFor := func(pane string) host.Agent {
		return host.Agent{
			Agent:        agyAgent,
			PaneID:       pane,
			AgentSession: &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: agyTestID},
		}
	}
	setAgents := func(m map[string]host.Agent) {
		n.mu.Lock()
		n.agents = m
		n.mu.Unlock()
	}

	setAgents(map[string]host.Agent{"agy-w1-p1": agentFor("w1:p1")})
	if err := n.agyCompanionStopRecheck(agyTestID, "agy-w1-p1"); err != nil {
		t.Fatalf("same unique row must pass, got %v", err)
	}

	setAgents(map[string]host.Agent{
		"agy-w1-p1": agentFor("w1:p1"),
		"agy-w1-p2": agentFor("w1:p2"),
	})
	if err := n.agyCompanionStopRecheck(agyTestID, "agy-w1-p1"); err == nil {
		t.Fatal("a twin pane appearing after resolution must refuse, not Ctrl+C one pane")
	} else if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("twin recheck code = %q, want %s", code, codeAgyInTerminal)
	}

	setAgents(map[string]host.Agent{"agy-w9-p9": agentFor("w9:p9")})
	if err := n.agyCompanionStopRecheck(agyTestID, "agy-w1-p1"); err == nil {
		t.Fatal("attribution moving to another row must refuse")
	} else if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("moved recheck code = %q, want %s", code, codeAgyInTerminal)
	}

	// The same pane/row id can switch to another conversation: attribution for the original then reads
	// empty, and the strict check must still refuse so the Ctrl+C cannot reach the new conversation.
	setAgents(map[string]host.Agent{agyTestRow: func() host.Agent {
		b := agentFor(agyTestPane)
		b.AgentSession = &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: agyOtherTestID}
		return b
	}()})
	if err := n.agyCompanionStopRecheck(agyTestID, agyTestRow); err == nil {
		t.Fatal("a same-pane switch to another conversation must refuse before sending Ctrl+C")
	} else if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("same-pane switch code = %q, want %s", code, codeAgyInTerminal)
	}

	// Attribution going empty (the pane exited in the gap) now fails closed for this round; a retry
	// resolves zero panes to authoritative not_found via the !ok/resolve path.
	setAgents(map[string]host.Agent{})
	if err := n.agyCompanionStopRecheck(agyTestID, "agy-w1-p1"); err == nil {
		t.Fatal("empty pre-quit attribution must fail closed for this round, not risk a wrong-pane key")
	}
}

// The stoppable !ok verdict: once the resolved row is absent, any remaining attribution (the
// conversation moved to another pane) must refuse; a clear machine and non-companion ids stay idempotent.
func TestAgyStopAbsentVerdict(t *testing.T) {
	withAgyPresenceHooks(t, func(string) agyLockState { return agyLockMissing }, nil)
	agentFor := func(pane string) host.Agent {
		return host.Agent{
			Agent:        agyAgent,
			PaneID:       pane,
			AgentSession: &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: agyTestID},
		}
	}

	n := &Node{}
	if err := n.agyStopAbsentVerdict(agyCompanionStopID, agyTestRow); err != nil {
		t.Fatalf("no remaining pane must be idempotent, got %v", err)
	}

	n.mu.Lock()
	n.agents = map[string]host.Agent{"agy-w2-p2": agentFor("w2:p2")}
	n.mu.Unlock()
	if err := n.agyStopAbsentVerdict(agyCompanionStopID, agyTestRow); err == nil {
		t.Fatal("a moved conversation attributed to another pane must refuse the idempotent stopped=false answer")
	} else if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("moved verdict code = %q, want %s", code, codeAgyInTerminal)
	}

	// The form gate: a plain row id keeps the old idempotent answer even with unrelated agents present.
	if err := n.agyStopAbsentVerdict(agyTestRow, agyTestRow); err != nil {
		t.Fatalf("non-companion id must keep idempotent !ok, got %v", err)
	}
}

// A pane whose raw report is a built-in subagent id still belongs to the parent companion conversation
// named by the subagent transcript's sender chain: stopping agy-<parent-uuid> must route to that pane,
// not answer not_found.
func TestAgyCompanionStopAttributesSubagentPaneToParent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", root)
	const subID = agyOtherTestID
	agyTestFile(t, root, subID,
		`{"step_index":0,"type":"SYSTEM_MESSAGE","source":"SYSTEM","content":"Subagent prompt sender=`+agyTestID+`"}`+"\n")
	agyTestFile(t, root, agyTestID,
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"parent prompt"}`+"\n")

	n := &Node{}
	a := host.Agent{
		Agent:        agyAgent,
		PaneID:       agyTestPane,
		AgentSession: &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: subID},
	}
	n.mu.Lock()
	n.agents = map[string]host.Agent{agyTestRow: a}
	n.mu.Unlock()

	rowID, got := n.agyCompanionStopAttribution(agyTestID)
	if got.ambiguous || got.freshHook || rowID != agyTestRow {
		t.Fatalf("subagent pane attribution = row %q %+v, want %s", rowID, got, agyTestRow)
	}
	stopID, err := n.resolveAgyCompanionStop(agyCompanionStopID, false)
	if err != nil || stopID != agyTestRow {
		t.Fatalf("resolve parent companion id = %q, %v; want route to %s", stopID, err, agyTestRow)
	}
}

// A second claimant that only a raw host snapshot sees (placement has no row for it) still makes the
// decision ambiguous: dropping the hostAgents scan would route to the one placed row instead.
func TestAgyCompanionStopRawOnlySecondClaimantStaysHard(t *testing.T) {
	claim := func(pane string) host.Agent {
		return host.Agent{
			Agent:        agyAgent,
			PaneID:       pane,
			AgentSession: &host.AgentSession{Source: "herdr:antigravity_cli", Agent: agyAgent, Kind: "id", Value: agyTestID},
		}
	}
	n := &Node{}
	n.mu.Lock()
	n.agents = map[string]host.Agent{agyTestRow: claim(agyTestPane)}
	n.hostAgents = map[string][]host.Agent{"herdr": {claim("w2:p2")}}
	n.mu.Unlock()

	if rowID, got := n.agyCompanionStopAttribution(agyTestID); !got.ambiguous || rowID != "" {
		t.Fatalf("raw-only second claimant: row=%q attr=%+v, want ambiguous with no single row", rowID, got)
	}
	if _, err := n.resolveAgyCompanionStop(agyCompanionStopID, false); err == nil {
		t.Fatal("a raw-only second claimant must refuse hard, not route or go")
	} else if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("raw-only claimant code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}
}

// An unreadable/unknown presence lock table cannot be read as free for an authoritative gone answer:
// Unknown vetoes not_found just as Held does.
func TestAgyCompanionStopUnknownPresenceStaysHard(t *testing.T) {
	withAgyPresenceHooks(t, func(string) agyLockState { return agyLockUnknown }, nil)
	n, _, _ := herdrNode(t)

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("unknown presence lock code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}
}

// A twin pane that appears only during the Ctrl+C quit verification survives quitAgent's routed-row-only
// disappearance check. The post-quit attribution scan must turn the otherwise successful stop into a hard
// refusal so the hub does not release a conversation still live in that twin.
func TestAgyCompanionStopTwinAppearingDuringQuitStaysHard(t *testing.T) {
	withStop(t, 1, 2*time.Second)
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, agyTestID)}, nil))
	listSessions(t, n)
	f.reset()

	var mu sync.Mutex
	presses := 0
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_keys" || str(c.Params["pane_id"]) != agyTestPane {
			return
		}
		mu.Lock()
		presses++
		count := presses
		mu.Unlock()
		if count == 2 { // both Ctrl+C sent: the routed pane is gone, a twin now reports the conversation
			twin := agyTestAgent("w2:p2", home, agyTestID)
			f.reply("session.snapshot", snapshot(nil, nil, []any{twin}, nil))
		}
	})

	_, err := request(t, n, "session.stop", map[string]any{"session_id": agyCompanionStopID})
	if err == nil {
		t.Fatal("a twin present after the quit must make the stop a hard refusal, not success")
	}
	if code, _ := refusalOf(t, err); code != codeAgyInTerminal {
		t.Fatalf("post-quit twin code = %q, want %s: %v", code, codeAgyInTerminal, err)
	}
	if got := f.trace("pane.send_keys"); len(got) != 2 {
		t.Fatalf("the routed pane should still have received both Ctrl+C, keys=%v", got)
	}
}
