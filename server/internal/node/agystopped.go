package node

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// An agy conversation the user closed or stopped is listed as stopped (#14520). While a live pane runs it, its row is
// the pane's (agyaliases.go). Once the user closes the pane or stops agy in it, the Antigravity companion lists the
// conversation under its own id, agy-<conversation>, as it lists every conversation it holds: done or idle, as if a
// turn had just ended there. A Claude Code pane the user closed is listed as not running, stopped by the user
// (markStopped, #4754), and so is this conversation's row: offline, with the user's stop cause, until it runs again (a
// live pane shows it, the user continues it through the companion, or its row works, waits on the user or changes after
// the close). It keeps what the companion lets it do: a message from the user continues it there, as there is no
// terminal to resume it in, and an A2A message does not wake it, as it wakes no Agent the user stopped (assistant
// a2aStopped). The marks are kept in memory, for the agyStoppedMost latest: after a restart the companion's rows are
// listed as it says.

const (
	// agyStopGrace: what the conversation does in the minute after the close is the close's own (agy ending its turn,
	// herdr taking the pane away), not new work.
	agyStopGrace   = time.Minute
	agyStoppedMost = 256
)

// agyStop is the pane row the user closed or stopped a conversation in, and when.
type agyStop struct {
	row string
	at  time.Time
}

// noteAgyStopped: the user is closing or stopping session id's Agent (saveStopCause). When id is a live agy pane's row
// whose conversation is known, that conversation is listed as stopped from now on (markAgyStopped).
func (n *Node) noteAgyStopped(id string) {
	a, ok := n.agent(id)
	if !ok || a.Agent != agyAgent || a.PaneID == "" || id != otherSession(a) {
		return
	}
	conversation := n.agyConversation(id, a.PaneID)
	if conversation == "" {
		return
	}
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.agyStopped == nil {
		n.agyStopped = map[string]agyStop{}
	}
	n.agyStopped[conversation] = agyStop{row: id, at: now}
	if over := len(n.agyStopped) - agyStoppedMost; over > 0 {
		oldest := slices.SortedFunc(maps.Keys(n.agyStopped), func(a, b string) int {
			return cmp.Or(n.agyStopped[a].at.Compare(n.agyStopped[b].at), strings.Compare(a, b))
		})
		for _, c := range oldest[:over] {
			delete(n.agyStopped, c)
		}
	}
}

// clearAgyStopped forgets a user-stop mark on conversation id once a companion send or compact continues it, so a
// resumed turn inside agyStopGrace is neither masked as StatusOffline (StopUser) nor trapped offline if it finishes
// within that minute. Marks recorded after at are kept so a concurrent user close during an in-flight call wins.
func (n *Node) clearAgyStopped(id string, at time.Time) bool {
	conversation, ok := strings.CutPrefix(id, agyAgent+"-")
	if !ok || conversation == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	st, ok := n.agyStopped[conversation]
	if !ok || st.at.After(at) {
		return false
	}
	delete(n.agyStopped, conversation)
	return true
}

// markAgyStopped lists the conversations the user stopped (noteAgyStopped) as stopped on list's rows under their own
// ids, and forgets the ones that run again. live is the own id (agy-<conversation>) of the conversation each live agy
// pane's row shows in this pass, by row (sessionListIn).
func (n *Node) markAgyStopped(list []proto.Session, live map[string]string) {
	now := time.Now()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.agyStopped) == 0 {
		return
	}
	for row, own := range live {
		// A pane runs it again: another one, or the same one once the close had time to take it away (it did not).
		conversation := strings.TrimPrefix(own, agyAgent+"-")
		if st, ok := n.agyStopped[conversation]; ok && (row != st.row || now.Sub(st.at) > agyStopGrace) {
			delete(n.agyStopped, conversation)
		}
	}
	for i := range list {
		s := &list[i]
		conversation, ok := strings.CutPrefix(s.SessionID, agyAgent+"-")
		if !ok || s.PaneID != "" {
			continue
		}
		st, ok := n.agyStopped[conversation]
		if !ok {
			continue
		}
		busy := s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked
		if busy && now.Sub(st.at) > agyStopGrace || s.UpdatedAt.After(st.at.Add(agyStopGrace)) {
			delete(n.agyStopped, conversation) // it runs again
			continue
		}
		caps := proto.SessionCapabilities{} // the row's own are shared with its other copies: a new one
		if s.Capabilities != nil {
			caps = *s.Capabilities
		}
		caps.StopCause = proto.StopUser
		s.Status, s.Capabilities = proto.StatusOffline, &caps
		s.Background = nil
	}
}
