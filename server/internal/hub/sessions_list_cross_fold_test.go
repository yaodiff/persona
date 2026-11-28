package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// crossFoldRoots is where the paged index folds each of box's rows: "mac/<root>" for a row folded under a mac
// session, "" for a row on its own.
func crossFoldRoots(t *testing.T, h *Hub, box *Machine) map[string]string {
	t.Helper()
	var ids []string
	for _, s := range box.keptSessions() {
		ids = append(ids, runtimeListID("box", s.SessionID))
	}
	items := h.listCache.index.Items(ids)
	out := make(map[string]string, len(items))
	for i, it := range items {
		if it.ID != ids[i] {
			t.Fatalf("item %d is %q, want %q", i, it.ID, ids[i])
		}
		root := ""
		if it.Fold != nil && it.Fold.Root != "" {
			root = it.Fold.RootMachine + "/" + it.Fold.Root
		}
		out[box.keptSessions()[i].SessionID] = root
	}
	return out
}

// wantCrossFoldRoots is crossFoldRoots as the index made it before #15899, by walking mac's shown list for each row:
// a row folds under its mac parent only while the hub shows that parent.
func wantCrossFoldRoots(h *Hub, mac, box *Machine) map[string]string {
	out := map[string]string{}
	for _, s := range box.keptSessions() {
		out[s.SessionID] = ""
		for _, p := range h.shown("mac", mac.keptSessions()) {
			if p.SessionID == s.FoldedUnder() {
				out[s.SessionID] = "mac/" + p.SessionID
				break
			}
		}
	}
	return out
}

// The cross-machine fold index looks parents up in a set built once per pass (#15899) and folds exactly as the
// per-row walk did: under a parent the hub shows, not under one deleted, archived or not listed, and after the
// parent machine updates, by its new list rather than the one an earlier pass read.
func TestCrossFoldIndexFoldsUnderShownParents(t *testing.T) {
	h, mac, box := crossFoldHub(t, 60, 40, 5) // every 5th parent deleted; kids from p0040 on name parents mac does not list
	mac.mu.Lock()
	mac.sessions[3].Archived = true // archived: not shown either
	mac.mu.Unlock()
	h.sessionsChanged(mac)

	check := func(step string) map[string]string {
		t.Helper()
		got, want := crossFoldRoots(t, h, box), wantCrossFoldRoots(h, mac, box)
		folded := 0
		for id, root := range want {
			if got[id] != root {
				t.Errorf("%s: %s folds under %q, want %q", step, id, got[id], root)
			}
			if root != "" {
				folded++
			}
		}
		if folded == 0 || folded == len(want) {
			t.Fatalf("%s: %d of %d rows folded; the case needs both", step, folded, len(want))
		}
		return got
	}
	before := check("first pass")
	// k0000 → p0000 (deleted), k0001 → p0007 (shown), k0003 → p0021 (shown), k0006 → p0042 (not listed),
	// k0013 → p0003 (archived).
	for id, root := range map[string]string{"k0000": "", "k0001": "mac/p0007", "k0003": "mac/p0021", "k0006": "", "k0013": ""} {
		if before[id] != root {
			t.Fatalf("first pass: %s folds under %q, want %q", id, before[id], root)
		}
	}

	// The parent machine updates: p0000 comes back, p0007 is deleted, p0021 leaves the list, p0042 joins it.
	mac.mu.Lock()
	next := make([]proto.Session, 0, len(mac.sessions)+1)
	for _, s := range mac.sessions {
		switch s.SessionID {
		case "p0000":
			s.SetDeleted(false)
		case "p0007":
			s.SetDeleted(true)
		case "p0021":
			continue
		}
		next = append(next, s)
	}
	next = append(next, proto.Session{MachineID: "mac", SessionID: "p0042", Title: "Lead", State: proto.StateIdle, Cwd: "/code/app"})
	mac.sessions = next
	mac.mu.Unlock()
	h.sessionsChanged(mac)

	after := check("after the parent machine's update")
	for id, root := range map[string]string{"k0000": "mac/p0000", "k0001": "", "k0003": "", "k0006": "mac/p0042", "k0013": ""} {
		if after[id] != root {
			t.Fatalf("after the parent machine's update: %s folds under %q, want %q", id, after[id], root)
		}
	}

	// The child machine's own pass reads the parent list the same way.
	h.sessionsChanged(box)
	check("child machine's own pass")
}
