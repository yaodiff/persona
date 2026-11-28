package node

import (
	"context"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type answerAgainAfterKey struct{}

// answerAgainCurrent runs under the same typing lock as owner sends. It requires the settled
// process and transcript tail the manager inspected. A newer send, stopped process or turn that
// has resumed on its own makes the follow-up obsolete. An unchanged transcript
// whose process is gone needs a manual retry, unless the owner stopped it.
func (n *Node) answerAgainCurrent(ctx context.Context, id string) error {
	after, _ := ctx.Value(answerAgainAfterKey{}).(string)
	if after == "" {
		return nil
	}
	changed := func() error {
		return refusal(assistantwire.CodeAnswerAgainChanged, "the conversation moved on", nil)
	}
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if cause := n.stopCause(id); cause == proto.StopUser || cause == proto.StopExit {
		return changed()
	}
	f, err := n.file(id)
	if err != nil {
		return changed()
	}
	if err := n.refresh(id, f); err != nil {
		return err
	}
	items, _, _ := f.Page(0, 1)
	if len(items) == 0 || items[len(items)-1].ID != after {
		return changed()
	}
	if p == nil || !p.alive() {
		return errors.New("the conversation process is unavailable for a follow-up")
	}
	p.mu.Lock()
	busy := p.workingLocked() || p.stopReason != "" || p.turnError != ""
	p.mu.Unlock()
	if busy || !p.alive() {
		return changed()
	}
	return nil
}
