package node

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// resolveAgyCompanionStop authorizes session.stop's handling of the Antigravity companion row id
// agy-<conversation-uuid>, which is not a terminal row id itself. A to-do keeps the id an Agent was
// quoted or addressed by, so after a companion restart a continues takeover asks session.stop for that
// exact id. The plain "invalid session id" answer the generic guard gives for it is not authoritative:
// a live pane normally runs the conversation under its pane-derived row, and when two panes report the
// same conversation the session list omits the companion alias from both rows (nameAgyAliases).
//
// The decision is therefore made from the node's own raw per-pane attribution (agyReportedLocked),
// which keeps two panes reporting one conversation as two attributions instead of suppressing the
// alias. It returns:
//
//   - stopID unchanged for every id that is not the agy companion shape;
//   - the unique attributing pane's row id, so the ordinary stoppable/quitAgent path stops it;
//   - a codeNotFound refusal when no pane/fresh hook remains and no active runtime ownership, start or
//     held presence lock belongs to the conversation: the authoritative orphaned answer the hub releases on;
//   - another refusal (never not_found) for every uncertain or live case: several panes attribute it,
//     an unplaced raw pane or a just-reported hook names it, an active runtime/assistant owns the id, a
//     start is in flight, or an unseen terminal may hold its presence lock. Those stay fail-closed in the hub.
func (n *Node) resolveAgyCompanionStop(id string, retire bool) (stopID string, err error) {
	conversation, ok := agyConversationID(id)
	if !ok {
		return id, nil
	}
	rowID, attributions := n.agyCompanionStopAttribution(conversation)
	switch {
	case attributions.ambiguous:
		// Two (or more) panes, or a raw pane placement did not surface as a stoppable row: stopping on a
		// guess could quit the wrong Agent, so this is a hard refusal, never a gone answer.
		return "", refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	case attributions.freshHook:
		// The agy hook named the conversation moments ago for a pane no row attributes now (the next
		// snapshot can lag the hook by agyReportGrace): a second pane may be undiscovered, so fail closed.
		return "", refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	case rowID != "":
		return rowID, nil
	}
	// No pane attributes the conversation. Keep every ownership gate the ordinary stoppable path has
	// before certifying it as gone, with the same retire code mapping as its no() helper.
	no := func(code, why string) (string, error) {
		if retire {
			code = notRetirable // what hubs before #4754 know
		}
		return "", refusal(code, why, nil)
	}
	switch {
	case n.isAssistantSession(id) || n.runtimeAssistant(id):
		return no(notRetirable, "assistant conversations are not stopped")
	case n.externalCodex(id):
		return no(noTerminal, "this session belongs to another tool")
	}
	if h, _, known := n.runtimes.belongs(id); h != nil || known {
		// "Absent from the runtime's ACTIVE list" is the veto that matters. A companion that still lists
		// the conversation at an at-rest status (done/idle/offline) is not running an Agent: the hub list
		// shows that stopped row and expects the authoritative gone answer, so let it fall through. An
		// owner/current row whose status cannot be read as at-rest stays fail-closed no_terminal.
		if row, _, ok := n.runtimes.rowOf(id); !ok || agyRuntimeRowActive(row) {
			return no(noTerminal, "this session belongs to another tool")
		}
	}
	if n.restarting(id) {
		return no(notRetirable, "still starting")
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return no(notRetirable, "still starting")
	}
	// Probe the presence lock state directly rather than agyPresenceHeldWith's send-path fallback: for an
	// authoritative "gone" answer an unreadable/unknown lock table cannot be read as free, so Held and
	// Unknown both veto not_found (Unknown's transcript-window fallback belongs to the send guard).
	switch agyPresenceLockProbe(filepath.Join(n.agyDataRoot(), "presence", conversation+".lock")) {
	case agyLockHeld, agyLockUnknown:
		// An agy outside any pane the node knows may hold the conversation open: "not found" would be
		// unsafe (agyOpenIn, #13603).
		return "", refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	}
	return "", refusal(codeNotFound, fmt.Sprintf("session %q is not on this computer", id), nil)
}

// agyCompanionStopRecheck closes the window between resolveAgyCompanionStopAttribution and actually
// quitting the routed row. The routed row MUST still be the unique attribution of conversation: if a
// refresh moved that same pane/row to another conversation (the pane-derived row id stays identical), the
// attribution for conversation becomes empty and a loose check would let the Ctrl+C reach the new
// conversation. Any ambiguity, fresh hook, missing attribution or another row therefore refuses hard;
// only rowID == routedRow is allowed. A pane that exited in this window takes one fail-closed retry,
// which then resolves to authoritative not_found.
func (n *Node) agyCompanionStopRecheck(conversation, routedRow string) error {
	rowID, got := n.agyCompanionStopAttribution(conversation)
	if got.ambiguous || got.freshHook || rowID != routedRow {
		return refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	}
	return nil
}

// agyCompanionStopPostQuit verifies after a successful Ctrl+C quit that NOTHING still attributes the
// conversation: a twin that appeared during quit verification, or a refresh interleaving that leaves a
// raw claim for the just-quit pane, would survive quitAgent's routed-row-only disappearance check. Any
// remaining claim is therefore a hard refusal, as is a presence lock still held by another terminal.
func (n *Node) agyCompanionStopPostQuit(conversation string) error {
	panes, got := n.agyCompanionAttributedPanes(conversation)
	if got.ambiguous || got.freshHook || len(panes) > 0 {
		return refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	}
	return n.agyCompanionPresenceVeto(conversation)
}

// agyCompanionStopIfAnyLive covers the stoppable !ok path: the routed row is already absent, so ANY
// remaining pane attribution (even a raw-only one a mixed refresh published) or a held/unknown presence
// lock means the conversation may still be live. It must not be answered as idempotent stopped=false.
func (n *Node) agyCompanionStopIfAnyLive(conversation string) error {
	panes, got := n.agyCompanionAttributedPanes(conversation)
	if got.ambiguous || got.freshHook || len(panes) > 0 {
		return refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	}
	return n.agyCompanionPresenceVeto(conversation)
}

// agyStopAbsentVerdict is stopWithCause's decision when stoppable reports the resolved row not running.
// For a companion uuid that resolved to a pane row, a refresh may have moved the conversation elsewhere,
// so re-check raw attribution; every other id keeps the old idempotent (false,nil) answer.
func (n *Node) agyStopAbsentVerdict(origID, resolvedID string) error {
	if conversation, isCompanion := agyConversationID(origID); isCompanion && resolvedID != origID {
		return n.agyCompanionStopIfAnyLive(conversation)
	}
	return nil
}

// agyCompanionPresenceVeto refuses not_found/success when the conversation's presence lock is held or its
// state cannot be read: an untracked terminal may still run it. Missing/free are the only clear states.
func (n *Node) agyCompanionPresenceVeto(conversation string) error {
	switch agyPresenceLockProbe(filepath.Join(n.agyDataRoot(), "presence", conversation+".lock")) {
	case agyLockHeld, agyLockUnknown:
		return refusal(codeAgyInTerminal, i18n.T(i18n.Default, "rpc-errors.agy_in_terminal"), nil)
	}
	return nil
}

// agyRuntimeRowActive reports whether a runtime's own row is still doing Agent work. An explicit at-rest
// status is not active; an empty/unknown status stays conservative (active) so ownership is not dropped.
func agyRuntimeRowActive(row proto.Session) bool {
	switch row.Status {
	case proto.StatusIdle, proto.StatusDone, proto.StatusOffline, proto.StatusInterrupted:
		return false
	default:
		return true
	}
}

// agyCompanionAttribution summarizes which local panes raw-report one Antigravity conversation.
type agyCompanionAttribution struct {
	ambiguous bool // more than one pane attributes it, an attributing raw pane has no stoppable row,
	// or a transcript needed to rule out a subagent of the conversation could not be read
	freshHook bool // a hook report within agyReportGrace names it for a pane no row attributes now
}

// agyPaneClaim is what the node's raw local sources say about one pane's Antigravity conversation.
type agyPaneClaim struct {
	rowID string // n.agents row key that stops it, "" when only a raw snapshot names the pane
	raw   []string
	bound string // verified parent conversation already bound to the pane (agypane.go)
}

// hasRaw reports whether id is already among the pane's raw claims.
func (c *agyPaneClaim) hasRaw(id string) bool {
	for _, r := range c.raw {
		if r == id {
			return true
		}
	}
	return false
}

// agyCompanionStopAttribution returns the single stoppable n.agents row id when exactly one placed pane
// attributes conversation; see agyCompanionAttributedPanes for the scan.
func (n *Node) agyCompanionStopAttribution(conversation string) (rowID string, got agyCompanionAttribution) {
	attributed, got := n.agyCompanionAttributedPanes(conversation)
	if len(attributed) > 1 {
		got.ambiguous = true
		return "", got
	}
	for _, id := range attributed {
		if id == "" {
			got.ambiguous = true // raw snapshot attributes it, placement has no row to stop
			return "", got
		}
		rowID = id
	}
	return rowID, got
}

// agyCompanionAttributedPanes is the raw per-pane scan: it maps every pane attributing conversation to
// its n.agents row id ("" when only a raw snapshot names the pane). Unlike
// agyConversation/rawAgyConversationLocked it does not suppress a report because a second pane gives the
// same conversation: that duplication is exactly the alias-omission case the stop decision stays hard on.
// A raw id can also be a built-in subagent of conversation (#13336), so the sender parent chain is
// resolved here as agyConversation does, without its cross-pane suppression.
func (n *Node) agyCompanionAttributedPanes(conversation string) (attributed map[string]string, got agyCompanionAttribution) {
	n.mu.Lock()
	panes := map[string]*agyPaneClaim{}
	claim := func(pane string) *agyPaneClaim {
		c := panes[pane]
		if c == nil {
			c = &agyPaneClaim{}
			panes[pane] = c
		}
		return c
	}
	addRaw := func(c *agyPaneClaim, id string) {
		if id != "" && !c.hasRaw(id) {
			c.raw = append(c.raw, id)
		}
	}
	for id, a := range n.agents {
		if a.PaneID == "" {
			continue
		}
		c := claim(a.PaneID)
		c.rowID = id
		addRaw(c, reportedAgyConversation(a))
		addRaw(c, n.agyReports[a.PaneID].conversation)
		if b := n.agyBound[a.PaneID]; b.verified && b.conversation != "" {
			c.bound = b.conversation
		}
	}
	for _, agents := range n.hostAgents {
		for _, a := range agents {
			if a.PaneID == "" {
				continue
			}
			c := claim(a.PaneID)
			addRaw(c, reportedAgyConversation(a))
			addRaw(c, n.agyReports[a.PaneID].conversation)
		}
	}
	// A hook report for a pane no row or raw snapshot lists cannot place a stop; fresh it only vetoes
	// "gone" (the next snapshot can lag the hook), stale it says nothing.
	for pane, rep := range n.agyReports {
		if rep.conversation != conversation {
			continue
		}
		if _, known := panes[pane]; known {
			continue
		}
		if time.Since(rep.at) < agyReportGrace {
			got.freshHook = true
		}
	}
	n.mu.Unlock()

	dir := n.agyDataRoot()
	attributed = map[string]string{} // pane -> its n.agents row id ("" when unplaced)
	for pane, c := range panes {
		yes := c.bound == conversation
		if !yes {
			for _, r := range c.raw {
				if r == conversation {
					yes = true
					break
				}
			}
		}
		if !yes {
			for _, r := range c.raw {
				if r == "" || r == conversation {
					continue
				}
				switch probeAgyConversation(dir, r) {
				case agyTranscriptSubagent:
					if resolveAgySubagentParent(dir, r) == conversation {
						yes = true
					}
				case agyTranscriptUnreadable:
					// Could be a subagent of conversation and the transcript that would clear it is
					// unreadable: fail closed rather than certify the conversation gone.
					got.ambiguous = true
				}
				if yes {
					break
				}
			}
		}
		if yes {
			attributed[pane] = c.rowID
		}
	}
	return attributed, got
}
