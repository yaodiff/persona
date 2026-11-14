package node

import (
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// Interim lines are optional: a suspended local client may delay one update only briefly, and loses later updates
// after a failed write. The final response retains the local protocol's original blocking behavior.
const mcpWaitInterimWriteTimeout = 100 * time.Millisecond

// pendingMCPWait is one explicitly opted-in local socket call. Its mutex orders interim updates with the final
// response: once finish starts, no update can follow that response.
type pendingMCPWait struct {
	mu            sync.Mutex
	conn          net.Conn
	done          bool
	interimFailed bool
}

func (p *pendingMCPWait) writeLocked(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = p.conn.Write(append(b, '\n'))
	return err
}

func (p *pendingMCPWait) update(kind proto.MCPWaitKind) {
	// Updates are serial on the hub read loop, so only finish can hold the lock here. Drop the optional interim line
	// rather than waiting behind a final write that a non-reading client may hold open without a deadline.
	if !p.mu.TryLock() {
		return
	}
	defer p.mu.Unlock()
	if p.done || p.interimFailed {
		return
	}
	if err := p.conn.SetWriteDeadline(time.Now().Add(mcpWaitInterimWriteTimeout)); err != nil {
		p.interimFailed = true
		return
	}
	// The local response is deliberately smaller than the relayed event: one enum, no correlator or other content.
	err := p.writeLocked(struct {
		WaitKind proto.MCPWaitKind `json:"wait_kind"`
	}{WaitKind: proto.NormalizeMCPWaitKind(kind)})
	if clearErr := p.conn.SetWriteDeadline(time.Time{}); err == nil {
		err = clearErr
	}
	p.interimFailed = err != nil
}

func (p *pendingMCPWait) finish(response any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.done {
		return
	}
	p.done = true
	p.writeLocked(response)
}

func (n *Node) beginMCPWait(conn net.Conn) (string, *pendingMCPWait) {
	if !n.hubMCPWait.Load() {
		return "", nil
	}
	p := &pendingMCPWait{conn: conn}
	n.mcpWaitMu.Lock()
	defer n.mcpWaitMu.Unlock()
	if n.mcpWaits == nil {
		n.mcpWaits = map[string]*pendingMCPWait{}
	}
	id := nodeclient.NewAttempt()
	for n.mcpWaits[id] != nil {
		id = nodeclient.NewAttempt()
	}
	n.mcpWaits[id] = p
	return id, p
}

func (n *Node) finishMCPWait(id string, pending *pendingMCPWait, response any) {
	n.mcpWaitMu.Lock()
	if n.mcpWaits[id] == pending {
		delete(n.mcpWaits, id)
	}
	n.mcpWaitMu.Unlock()
	pending.finish(response)
}

// onMCPWait takes the manager's enum-only event to its one pending local call. Unknown calls are already finished;
// unknown values become unknown rather than being reflected onto the local socket.
func (n *Node) onMCPWait(event string, params json.RawMessage) bool {
	if event != proto.EventMCPWait {
		return false
	}
	var update proto.MCPWaitEvent
	if json.Unmarshal(params, &update) != nil || !proto.ValidMCPWaitCallID(update.CallID) {
		return true
	}
	n.mcpWaitMu.Lock()
	pending := n.mcpWaits[update.CallID]
	n.mcpWaitMu.Unlock()
	if pending != nil {
		pending.update(update.Kind)
	}
	return true
}
