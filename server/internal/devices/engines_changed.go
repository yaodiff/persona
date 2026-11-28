package devices

import (
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// onEnginesChanged takes the engines a node says again after node.info (proto.EventEnginesChanged: Orca opened or
// quit while it ran, node/orcaengine.go, #17037) into the machine, and tells the apps, so the engine pickers, the
// Orca worktree actions and the "Orca isn't running" notices follow without the node reconnecting. A list that is
// empty or the same as the one kept changes nothing; a late one from a machine let go is dropped.
func (m *Machine) onEnginesChanged(params json.RawMessage) {
	var p proto.EnginesChanged
	if json.Unmarshal(params, &p) != nil || len(p.Engines) == 0 || m.stopped() {
		return
	}
	m.mu.Lock()
	same := slices.Equal(m.info.Engines, p.Engines)
	if !same {
		m.info.Engines = p.Engines
	}
	m.mu.Unlock()
	if !same {
		m.d.changed()
	}
}
