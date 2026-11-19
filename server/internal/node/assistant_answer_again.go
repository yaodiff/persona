package node

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type answerAgainAfterKey struct{}

// answerAgainProcessCurrent runs under the typing lock. It requires the same settled local
// process and transcript tail the manager inspected.
func (n *Node) answerAgainProcessCurrent(ctx context.Context, id, after string) error {
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p == nil || !p.alive() {
		return answerAgainChanged()
	}
	if err := n.answerAgainTail(ctx, id, after, nil); err != nil {
		return err
	}
	p.mu.Lock()
	busy := p.workingLocked() || p.stopReason != "" || p.turnError != ""
	p.mu.Unlock()
	if busy || !p.alive() {
		return answerAgainChanged()
	}
	return nil
}

// answerAgainRuntimeCurrent runs under the typing lock, right before delivering to the runtime.
func (n *Node) answerAgainRuntimeCurrent(ctx context.Context, h *runtimeConn, id, after string) error {
	row, owner, ok := n.runtimes.rowOf(id)
	if !ok {
		return errors.New("cannot verify the runtime conversation")
	}
	if owner != h || !answerAgainRuntimeIdle(row) {
		return answerAgainChanged()
	}
	if err := n.answerAgainTail(ctx, id, after, owner); err != nil {
		return err
	}
	// Runtime callbacks can change the row while the page is being read.
	latest, currentOwner, ok := n.runtimes.rowOf(id)
	if !ok || currentOwner != owner || latest.Rev != row.Rev || !answerAgainRuntimeIdle(latest) {
		return answerAgainChanged()
	}
	return nil
}

func answerAgainRuntimeIdle(row proto.Session) bool {
	return (row.Status == proto.StatusIdle || row.Status == proto.StatusDone) && !row.Starting &&
		len(row.Queued) == 0 && len(row.Background) == 0 && row.TurnError == "" && row.TurnErrorKind != proto.TurnErrorStopped
}

func (n *Node) answerAgainTail(ctx context.Context, id, after string, owner *runtimeConn) error {
	var items []proto.Item
	if owner != nil && n.runtimeTranscript(id) == "" {
		// Some runtimes serve a local transcript through the node. For other runtimes the owner serves the items.
		var page struct {
			Items []proto.Item `json:"items"`
		}
		cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
		defer cancel()
		if err := owner.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1, "lazy_images": true}, &page); err != nil {
			return err
		}
		items = page.Items
	} else {
		f, err := n.file(id)
		if err != nil {
			return answerAgainChanged()
		}
		if err := n.refresh(id, f); err != nil {
			return err
		}
		items, _, _ = f.Page(0, 1)
	}
	if len(items) == 0 || items[len(items)-1].ID != after {
		return answerAgainChanged()
	}
	return nil
}

func answerAgainChanged() error {
	return refusal(assistantwire.CodeAnswerAgainChanged, "the conversation moved on", nil)
}
