package hub

import (
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// crossFoldHub is a hub whose machine "box" lists kids rows folded under sessions of machine "mac", which lists
// parents sessions; every hidden-th mac session is deleted (left out of what the apps get), so the rows folded
// under it stand on their own. The paged index is built.
func crossFoldHub(t *testing.T, kids, parents, hidden int) (h *Hub, mac, box *Machine) {
	t.Helper()
	h = newTestHub(t)
	mac = newMachine(h, MachineConfig{ID: "mac"})
	box = newMachine(h, MachineConfig{ID: "box"})
	h.machines, h.byID["mac"], h.byID["box"] = []*Machine{mac, box}, mac, box
	at := func(n int) time.Time { return time.Unix(int64(5000+n), 0) }
	mac.sessions = make([]proto.Session, parents)
	for i := range mac.sessions {
		mac.sessions[i] = proto.Session{MachineID: "mac", SessionID: fmt.Sprintf("p%04d", i), Title: "Lead", State: proto.StateIdle,
			Cwd: "/code/app", UpdatedAt: at(i)}
		if hidden > 0 && i%hidden == 0 {
			mac.sessions[i].SetDeleted(true)
		}
	}
	box.sessions = make([]proto.Session, kids)
	for i := range box.sessions {
		s := proto.Session{MachineID: "box", SessionID: fmt.Sprintf("k%04d", i), Title: "Kid", State: proto.StateWorking, Cwd: "/code/app",
			Agent: "codex", IsSubagent: true, UpdatedAt: at(parents + i)}
		// Spread the kids over the parent list, the last ones under a session mac does not list at all.
		parent := fmt.Sprintf("p%04d", (i*7)%(parents+parents/10))
		s.ParentSessionID = parent
		s.SetSubagentProvenance("mac", "task", proto.SubagentOriginA2A)
		s.SetFoldWithMachine("mac", parent, "", "", nil)
		box.sessions[i] = s
	}
	h.sessionListIndex()
	return h, mac, box
}

// BenchmarkCrossFoldIndex is one paged-index pass under sessionsMu for a machine of 200 rows folded under sessions of
// another machine that lists 1,000 (#15899): the child machine's own update (child), and the parent machine's update,
// which re-indexes the child machine as well (parent); with no parent hidden, and with every 50th deleted (Hub.shown
// then copies the parent list).
func BenchmarkCrossFoldIndex(b *testing.B) {
	const kids, parents = 200, 1000
	for _, hidden := range []int{0, 50} {
		for _, side := range []string{"child", "parent"} {
			b.Run(fmt.Sprintf("kids=%d/parents=%d/hidden=%d/%s", kids, parents, hidden, side), func(b *testing.B) {
				h, mac, box := crossFoldHub(&testing.T{}, kids, parents, hidden)
				m := box
				if side == "parent" {
					m = mac
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					h.sessionsMu.Lock()
					h.indexKeptLocked(m)
					h.sessionsMu.Unlock()
				}
			})
		}
	}
}
